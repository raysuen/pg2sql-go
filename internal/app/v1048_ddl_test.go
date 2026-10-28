package app

// v1.0.48 回归用例（DDL 生成侧）：标识符转义、枚举数组列的类型限定、重复执行守卫。
// 数组解码（P0 越界/P1 自定义类型）用例在 internal/datatype/v1048_verify_test.go。

import (
	"encoding/binary"
	"pg2sql/internal/datatype"
	"pg2sql/internal/engine"
	"pg2sql/internal/util"
	"strings"
	"testing"
)

// 固定一批 oid 模拟真实 PG17 夹具（见 auditfix_fixture.sql）：
//
//	_mood=16865 (typtype=b, typelem=16866, typarray=0)
//	mood =16866 (typtype=e, typarray=16865, typlen=4)
const (
	tMoodArrOID = 16865
	tMoodOID    = 16866
	mSAD        = 16868
	mOK         = 16870
	mHAPPY      = 16871
)

func withFakeTypes(t *testing.T) {
	t.Helper()
	oldDefs, oldDec, oldEnum := datatype.DynamicTypeDefs, datatype.DynamicDecoders, datatype.EnumMapSnapshot()
	datatype.DynamicTypeDefs = map[uint32]*datatype.TypeDef{
		tMoodArrOID: {OID: tMoodArrOID, Name: "_mood", Type: 'b', Len: -1, Elem: tMoodOID, Array: 0},
		tMoodOID:    {OID: tMoodOID, Name: "mood", Type: 'e', Len: 4, Elem: 0, Array: tMoodArrOID},
	}
	datatype.DynamicDecoders = map[uint32]func([]byte) string{tMoodArrOID: datatype.DecodeArray}
	datatype.SetEnumMap(map[uint32][]datatype.EnumMember{
		tMoodOID: {
			{MemberOID: mSAD, Label: "sad", SortOrder: 1},
			{MemberOID: mOK, Label: "ok", SortOrder: 2},
			{MemberOID: mHAPPY, Label: "happy", SortOrder: 3},
		},
	})
	t.Cleanup(func() {
		datatype.DynamicTypeDefs, datatype.DynamicDecoders = oldDefs, oldDec
		datatype.SetEnumMap(oldEnum)
	})
}

// varlena：PG 短头 = (总长<<1)|1，总长含 1 字节头
func wrapShort(payload []byte) []byte {
	total := len(payload) + 1
	return append([]byte{byte(total<<1) | 1}, payload...)
}

func arrPayload(ndim, dataoffset int, elemOID uint32, dims []int32, lbs []int32, bitmap []byte, elems []byte) []byte {
	p := make([]byte, 12+8*ndim)
	binary.LittleEndian.PutUint32(p[0:], uint32(ndim))
	binary.LittleEndian.PutUint32(p[4:], uint32(dataoffset))
	binary.LittleEndian.PutUint32(p[8:], elemOID)
	for i := 0; i < ndim; i++ {
		binary.LittleEndian.PutUint32(p[12+4*i:], uint32(dims[i]))
		binary.LittleEndian.PutUint32(p[12+4*ndim+4*i:], uint32(lbs[i]))
	}
	p = append(p, bitmap...)
	// 元素区起点对齐到 4
	for len(p)%4 != 0 {
		p = append(p, 0)
	}
	return append(p, elems...)
}

// P1-F2：标识符内双引号必须转义为两个
func TestQuoteNameEscapes(t *testing.T) {
	cases := map[string]string{
		`plain`:  `"plain"`,
		`quo"te`: `"quo""te"`,
		`"`:      `""""`,
		``:       `""`,
	}
	for in, want := range cases {
		if got := util.QuoteIdent(in); got != want {
			t.Fatalf("util.QuoteIdent(%q) = %q want %q", in, got, want)
		}
	}
}

// P1-F2/F3：CREATE TABLE 转义 + 枚举数组列必须带 CREATE TYPE
func TestGenerateDDLEnumArray(t *testing.T) {
	withFakeTypes(t)
	tm := &engine.TableMeta{
		Schema:  `s"ch`,
		RelName: `quo"ted`,
		TypeNames: map[uint32]string{
			23:          "int4",
			tMoodArrOID: "_mood",
			tMoodOID:    "mood",
		},
		Columns: []engine.ColumnDef{
			{Name: `id`, TypeOID: 23, NotNull: true},
			{Name: `m`, TypeOID: tMoodOID},
			{Name: `ma`, TypeOID: tMoodArrOID},
		},
		PrimaryKey: []string{`id`},
	}
	ddl := generateDDL(tm)
	for _, want := range []string{
		// v1.0.48：类型名按 schema 限定，且包在 DO 块里防重复创建
		`CREATE TYPE "s""ch"."mood" AS ENUM ('sad', 'ok', 'happy');`,
		`EXCEPTION WHEN duplicate_object THEN NULL;`,
		`CREATE TABLE IF NOT EXISTS "s""ch"."quo""ted" (`,
		`"id" int4 NOT NULL`,
		`"ma" "s""ch"."mood"[]`,
		`PRIMARY KEY ("id")`,
	} {
		if !strings.Contains(ddl, want) {
			t.Fatalf("DDL 缺少 %q\n---- 实际 ----\n%s", want, ddl)
		}
	}
	if strings.Contains(ddl, `"quo"ted"`) || strings.Contains(ddl, `_mood`) {
		t.Fatalf("DDL 仍含未转义标识符或内部数组名:\n%s", ddl)
	}
}

// P1-F3：表里只有枚举数组列（没有标量枚举列）也必须输出 CREATE TYPE
func TestGenerateDDLOnlyEnumArray(t *testing.T) {
	withFakeTypes(t)
	tm := &engine.TableMeta{
		Schema:    "public",
		RelName:   "only_enum",
		TypeNames: map[uint32]string{tMoodArrOID: "_mood", tMoodOID: "mood"},
		Columns: []engine.ColumnDef{
			{Name: "i", TypeOID: 23},
			{Name: "ma", TypeOID: tMoodArrOID},
		},
	}
	ddl := generateDDL(tm)
	if !strings.Contains(ddl, `CREATE TYPE "public"."mood" AS ENUM`) {
		t.Fatalf("只有枚举数组列时缺 CREATE TYPE:\n%s", ddl)
	}
	if !strings.Contains(ddl, `"ma" "public"."mood"[]`) {
		t.Fatalf("枚举数组列类型未改写为 mood[]:\n%s", ddl)
	}
}

// v1.0.48（P3）：同一 DDL 重复执行不应报错（多表文件共用枚举/域的场景）
func TestDuplicateGuardWrapsCreateType(t *testing.T) {
	withFakeTypes(t)
	tm := &engine.TableMeta{
		Schema:    "public",
		RelName:   "t",
		TypeNames: map[uint32]string{tMoodOID: "mood"},
		Columns:   []engine.ColumnDef{{Name: "m", TypeOID: tMoodOID}},
	}
	ddl := generateDDL(tm)
	if !strings.HasPrefix(strings.TrimSpace(ddl), "DO $do1$ BEGIN") { // v1.0.49: 唯一 tag
		t.Fatalf("CREATE TYPE 未被 DO 块包裹:\n%s", ddl)
	}
	if strings.Count(ddl, "EXCEPTION WHEN duplicate_object") != 1 {
		t.Fatalf("守卫数量不对:\n%s", ddl)
	}
	if !strings.HasSuffix(strings.TrimSpace(ddl), "END $$;") {
		// 末尾应是 CREATE TABLE，这里只校验 DO 块闭合
		if !strings.Contains(ddl, "END $do1$;") { // v1.0.49: 唯一 tag
			t.Fatalf("DO 块未闭合:\n%s", ddl)
		}
	}
}
