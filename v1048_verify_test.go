package main

// v1.0.48 回归用例：P0 数组越界守卫 + P1 自定义类型数组解码 / 标识符转义。
// 这些用例直接构造 PG 磁盘格式的 varlena 字节，不依赖真实数据目录。

import (
	"encoding/binary"
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
	oldDefs, oldDec, oldEnum := dynamicTypeDefs, dynamicDecoders, enumMapSnapshot()
	dynamicTypeDefs = map[uint32]*TypeDef{
		tMoodArrOID: {OID: tMoodArrOID, Name: "_mood", Type: 'b', Len: -1, Elem: tMoodOID, Array: 0},
		tMoodOID:    {OID: tMoodOID, Name: "mood", Type: 'e', Len: 4, Elem: 0, Array: tMoodArrOID},
	}
	dynamicDecoders = map[uint32]func([]byte) string{tMoodArrOID: decodeArray}
	setEnumMap(map[uint32][]EnumMember{
		tMoodOID: {
			{MemberOID: mSAD, Label: "sad", SortOrder: 1},
			{MemberOID: mOK, Label: "ok", SortOrder: 2},
			{MemberOID: mHAPPY, Label: "happy", SortOrder: 3},
		},
	})
	t.Cleanup(func() {
		dynamicTypeDefs, dynamicDecoders = oldDefs, oldDec
		setEnumMap(oldEnum)
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

// P1-F1：一维枚举数组（含 NULL 元素）
func TestEnumArrayDecode1D(t *testing.T) {
	withFakeTypes(t)
	// {sad, NULL, happy}：位图 0b101（bit=1 表示该元素非 NULL）
	var elems []byte
	for _, o := range []uint32{mSAD, mHAPPY} {
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, o)
		elems = append(elems, b...)
	}
	raw := wrapShort(arrPayload(1, 25, tMoodOID, []int32{3}, []int32{1}, []byte{0b101}, elems))
	got := decodeValue(tMoodArrOID, raw)
	if want := `{sad,NULL,happy}`; got != want {
		t.Fatalf("枚举数组解码错误: got %q want %q", got, want)
	}
}

// P1-F1：二维枚举数组
func TestEnumArrayDecode2D(t *testing.T) {
	withFakeTypes(t)
	var elems []byte
	for _, o := range []uint32{mSAD, mOK, mHAPPY, mSAD} {
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, o)
		elems = append(elems, b...)
	}
	raw := wrapShort(arrPayload(2, 0, tMoodOID, []int32{2, 2}, []int32{1, 1}, nil, elems))
	got := decodeValue(tMoodArrOID, raw)
	if want := `{{sad,ok},{happy,sad}}`; got != want {
		t.Fatalf("二维枚举数组解码错误: got %q want %q", got, want)
	}
}

// P1-F1：空枚举数组 / 全 NULL 枚举数组
func TestEnumArrayDecodeEmptyAndAllNull(t *testing.T) {
	withFakeTypes(t)
	// 空数组：PG 用 ndim=0 表示
	empty := wrapShort([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	if got := decodeValue(tMoodArrOID, empty); got != "{}" {
		t.Fatalf("空枚举数组: got %q want {}", got)
	}
	// 全 NULL 数组元素区为空，dataoffset 指向末尾之外
	raw := wrapShort(arrPayload(1, 25, tMoodOID, []int32{2}, []int32{1}, []byte{0b00}, nil))
	if got := decodeValue(tMoodArrOID, raw); got != "{NULL,NULL}" {
		t.Fatalf("全 NULL 枚举数组: got %q want {NULL,NULL}", got)
	}
}

// userArrayElemOID：旧判据 td.Array==oid 恒不成立，此处锁死新判据
func TestUserArrayElemOID(t *testing.T) {
	withFakeTypes(t)
	if eo, ok := userArrayElemOID(tMoodArrOID); !ok || eo != tMoodOID {
		t.Fatalf("userArrayElemOID(_mood) = %d,%v want %d,true", eo, ok, tMoodOID)
	}
	if _, ok := userArrayElemOID(tMoodOID); ok {
		t.Fatalf("userArrayElemOID(mood) 不应判定为数组类型")
	}
}

// P0-A1：dims 数组被截断（ndim=3 但只有 2 个 dim）→ 兜底不 panic
func TestArrayTruncatedDimsNoPanic(t *testing.T) {
	p := make([]byte, 20)
	binary.LittleEndian.PutUint32(p[0:], 3)  // ndim=3
	binary.LittleEndian.PutUint32(p[4:], 0)  // dataoffset=0
	binary.LittleEndian.PutUint32(p[8:], 23) // elemtype int4
	binary.LittleEndian.PutUint32(p[12:], 2) // dims[0]
	binary.LittleEndian.PutUint32(p[16:], 2) // dims[1] —— dims[2]/lbs 区已缺失
	got := decodeArray(wrapShort(p))
	if !strings.HasPrefix(got, `\x`) {
		t.Fatalf("截断数组应退回 \\x 兜底，got %q", got)
	}
}

// P0-A2：dims 连乘溢出（6 维各 1<<30）→ 兜底不 panic
func TestArrayHugeDimsNoPanic(t *testing.T) {
	p := make([]byte, 12+8*6)
	binary.LittleEndian.PutUint32(p[0:], 6)
	binary.LittleEndian.PutUint32(p[4:], 0)
	binary.LittleEndian.PutUint32(p[8:], 23)
	for i := 0; i < 6; i++ {
		binary.LittleEndian.PutUint32(p[12+4*i:], 1<<30)
	}
	got := decodeArray(wrapShort(p))
	if !strings.HasPrefix(got, `\x`) {
		t.Fatalf("dims 溢出应退回 \\x 兜底，got %q", got)
	}
}

// P0-A5：元素区起点越界（dims 声称 10 个元素但 payload 只有 16 字节）
func TestArrayElemPosOverrunNoPanic(t *testing.T) {
	p := make([]byte, 16)
	binary.LittleEndian.PutUint32(p[0:], 1)
	binary.LittleEndian.PutUint32(p[4:], 0)
	binary.LittleEndian.PutUint32(p[8:], 25) // text 元素（varlena）
	binary.LittleEndian.PutUint32(p[12:], 10)
	raw := wrapShort(p) // 头部丢掉 lbs，制造越界
	if got := decodeArray(raw); got == "" {
		t.Fatalf("越界数组不应返回空串")
	}
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
		if got := quoteName(in); got != want {
			t.Fatalf("quoteName(%q) = %q want %q", in, got, want)
		}
	}
}

// P1-F2/F3：CREATE TABLE 转义 + 枚举数组列必须带 CREATE TYPE
func TestGenerateDDLEnumArray(t *testing.T) {
	withFakeTypes(t)
	tm := &TableMeta{
		Schema:  `s"ch`,
		RelName: `quo"ted`,
		TypeNames: map[uint32]string{
			23:          "int4",
			tMoodArrOID: "_mood",
			tMoodOID:    "mood",
		},
		Columns: []ColumnDef{
			{Name: `id`, TypeOID: 23, NotNull: true},
			{Name: `m`, TypeOID: tMoodOID},
			{Name: `ma`, TypeOID: tMoodArrOID},
		},
		PrimaryKey: []string{`id`},
	}
	ddl := tm.GenerateDDL()
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
	tm := &TableMeta{
		Schema:    "public",
		RelName:   "only_enum",
		TypeNames: map[uint32]string{tMoodArrOID: "_mood", tMoodOID: "mood"},
		Columns: []ColumnDef{
			{Name: "i", TypeOID: 23},
			{Name: "ma", TypeOID: tMoodArrOID},
		},
	}
	ddl := tm.GenerateDDL()
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
	tm := &TableMeta{
		Schema:    "public",
		RelName:   "t",
		TypeNames: map[uint32]string{tMoodOID: "mood"},
		Columns:   []ColumnDef{{Name: "m", TypeOID: tMoodOID}},
	}
	ddl := tm.GenerateDDL()
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
