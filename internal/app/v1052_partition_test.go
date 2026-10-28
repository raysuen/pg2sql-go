// v1052_partition_test.go：v1.0.52 分区 DDL 还原 + 表级编码单元测试。
// 节点树夹具全部来自 PG17（pt52 夹具库）与金仓 V9 实库抓取。
package app

import (
	"encoding/hex"
	"os"
	"pg2sql/internal/catalog"
	"pg2sql/internal/charset"
	"pg2sql/internal/ddl"
	"pg2sql/internal/engine"
	"pg2sql/internal/nt"
	"strings"
	"testing"
)

func osWriteFile(path string, buf []byte) error {
	return os.WriteFile(path, buf, 0644)
}

const pbQ1 = `{PARTITIONBOUNDSPEC :strategy r :is_default false :modulus 0 :remainder 0 :listdatums <> :lowerdatums ({PARTITIONRANGEDATUM :kind 0 :value {CONST :consttype 1184 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 -128 -44 126 -104 -51 2 0 ]} :location -1}) :upperdatums ({PARTITIONRANGEDATUM :kind 0 :value {CONST :consttype 1184 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 64 -116 -4 -86 -44 2 0 ]} :location -1}) :location -1}`

const pbQ2 = `{PARTITIONBOUNDSPEC :strategy r :is_default false :modulus 0 :remainder 0 :listdatums <> :lowerdatums ({PARTITIONRANGEDATUM :kind 0 :value {CONST :consttype 1184 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 96 -24 10 71 -22 2 0 ]} :location -1}) :upperdatums ({PARTITIONRANGEDATUM :kind 1 :value <> :location -1}) :location -1}`

const pbMinMax = `{PARTITIONBOUNDSPEC :strategy r :is_default false :modulus 0 :remainder 0 :listdatums <> :lowerdatums ({PARTITIONRANGEDATUM :kind -1 :value <> :location -1}) :upperdatums ({PARTITIONRANGEDATUM :kind 0 :value {CONST :consttype 1184 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 -128 -44 126 -104 -51 2 0 ]} :location -1}) :location -1}`

const pbDef = `{PARTITIONBOUNDSPEC :strategy r :is_default true :modulus 0 :remainder 0 :listdatums <> :lowerdatums <> :upperdatums <> :location -1}`

const pbList = `{PARTITIONBOUNDSPEC :strategy l :is_default false :modulus 0 :remainder 0 :listdatums ({CONST :consttype 25 :consttypmod -1 :constcollid 100 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 6 [ 24 0 0 0 66 74 ]} {CONST :consttype 25 :consttypmod -1 :constcollid 100 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 6 [ 24 0 0 0 84 74 ]}) :lowerdatums <> :upperdatums <> :location -1}`

const pbHash = `{PARTITIONBOUNDSPEC :strategy h :is_default false :modulus 2 :remainder 0 :listdatums <> :lowerdatums <> :upperdatums <> :location -1}`

func TestV1052PartBoundDeparse(t *testing.T) {
	cases := []struct {
		name, raw, want, wantSub string
	}{
		{"range-finite", pbQ1, "FOR VALUES FROM ('2025-01-01 00:00:00+00') TO ('2025-04-01 00:00:00+00')", ""},
		{"range-maxvalue", pbQ2, "FOR VALUES FROM ('2026-01-01 00:00:00+00') TO (MAXVALUE)", ""},
		{"range-minvalue", pbMinMax, "FOR VALUES FROM (MINVALUE) TO ('2025-01-01 00:00:00+00')", ""},
		{"default", pbDef, "DEFAULT", ""},
		{"list-text", pbList, "FOR VALUES IN ('BJ', 'TJ')", ""},
		{"hash", pbHash, "FOR VALUES WITH (MODULUS 2, REMAINDER 0)", ""},
	}
	for _, c := range cases {
		got := nt.DeparsePartBound(c.raw)
		if c.want != "" && got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
		if c.wantSub != "" && !strings.Contains(got, c.wantSub) {
			t.Errorf("%s: got %q want sub %q", c.name, got, c.wantSub)
		}
	}
	// 截断/乱输入 → 安全回退空串
	if nt.DeparsePartBound("{PARTITIONBOUNDSPEC :strategy r") != "" {
		t.Error("截断输入应回退空串")
	}
	if nt.DeparsePartBound("garbage") != "" {
		t.Error("非法输入应回退空串")
	}
}

func TestV1052PartitionByClause(t *testing.T) {
	ki := &catalog.PartKeyInfo{Strat: 'r', Cols: []int{4}}
	names := map[int]string{1: "id", 2: "uid", 3: "amt", 4: "created"}
	if got := ddl.PartitionByClause(ki, nil, names); got != `PARTITION BY RANGE ("created")` {
		t.Errorf("单列键: got %q", got)
	}
	// 表达式键：col 0 位由 partexprs 反解补充
	ki2 := &catalog.PartKeyInfo{Strat: 'h', Cols: []int{0}, Exprs: `{FUNCEXPR :funcid 123 :funcresulttype 23 :args ({VAR :varno 1 :varattno 2 :vartype 23 :vartypmod -1 :varcollid 0 :varlevelsup 0 :location -1}) :location -1}`}
	got := ddl.PartitionByClause(ki2, []string{`("uid" * 2)`}, names)
	if got != `PARTITION BY HASH ((`+`"uid" * 2`+`))` {
		t.Errorf("表达式键: got %q", got)
	}
	// 表达式反解缺失 → 回退空串
	if got := ddl.PartitionByClause(ki2, nil, names); got != "" {
		t.Errorf("表达式缺失应回退空串: got %q", got)
	}
}

func TestV1052PKCoversPartKey(t *testing.T) {
	tm := &engine.TableMeta{
		PartStrat:   'r',
		PartKeyCols: []int{4},
		PrimaryKey:  []string{"id", "created", "uid"},
		Columns: []engine.ColumnDef{
			{Name: "id", AttNum: 1}, {Name: "uid", AttNum: 2},
			{Name: "amt", AttNum: 3}, {Name: "created", AttNum: 4},
		},
	}
	if !ddl.PkCoversPartKey(tm) {
		t.Error("主键含分区键应判定覆盖")
	}
	tm.PrimaryKey = []string{"id"}
	if ddl.PkCoversPartKey(tm) {
		t.Error("主键缺分区键应判定不覆盖")
	}
}

func TestV1052SortPartitionFirst(t *testing.T) {
	parent := &engine.TableMeta{OID: 10, RelName: "p", PartStrat: 'r', PartKeyCols: []int{1}}
	child := &engine.TableMeta{OID: 11, RelName: "c", PartParent: 10, PartBound: "DEFAULT", PartParentQual: `"public"."p"`}
	grand := &engine.TableMeta{OID: 12, RelName: "g", PartParent: 11, PartBound: "DEFAULT", PartParentQual: `"public"."c"`}
	lone := &engine.TableMeta{OID: 13, RelName: "lone"}
	tasks := []*engine.TableMeta{grand, lone, child, parent}
	ddl.SortTablesPartitionFirst(tasks)
	pos := map[string]int{}
	for i, x := range tasks {
		pos[x.RelName] = i
	}
	if !(pos["p"] < pos["c"] && pos["c"] < pos["g"]) {
		t.Errorf("拓扑序错误: p=%d c=%d g=%d（父须先于子）", pos["p"], pos["c"], pos["g"])
	}
}

func TestV1052EncName3State(t *testing.T) {
	if encName3state("utf-8") != "utf8" || encName3state("gbk") != "gbk" || encName3state("gb18030") != "" {
		t.Error("encName3state 映射错误")
	}
}

func TestV1052ProbeFileEncodingBinarySafe(t *testing.T) {
	// 随机二进制（bytea/geometry 特征）不应判为 GBK（v1.0.52 收紧 0.7→0.85）
	path := t.TempDir() + "/bin.dat"
	buf := make([]byte, 32768)
	rng := uint32(12345)
	for i := range buf {
		rng = rng*1664525 + 1013904223 // 简易 LCG，模拟压缩/密文字节
		buf[i] = byte(rng >> 16)
	}
	if err := osWriteFile(path, buf); err != nil {
		t.Fatal(err)
	}
	if pe := charset.ProbeFileEncoding(path, 8192); pe == "gbk" {
		t.Errorf("随机二进制被误判为 GBK（应 unknown/utf8），got %s", pe)
	}
}

func TestV1052ProbeFileEncodingTriState(t *testing.T) {
	dir := t.TempDir()
	// 构造"类页"字节流：24B 页头 + 文本载荷（探测读文件末页整页字节）
	mk := func(payload func() []byte) []byte {
		page := make([]byte, 8192)
		copy(page[64:], payload())
		return page
	}
	// ① 真 GBK 中文字节流（金仓 V8 标 GBK 库的实存形态）
	// "历史数据行内容填充" 的 GBK 编码字节，重复填充页面
	const gbkHex = "c0facab7cafdbeddd0d0c4dac8ddcceeb3e4"
	gbk := func() []byte {
		raw, _ := hex.DecodeString(gbkHex)
		var b []byte
		for i := 0; i < 440; i++ {
			b = append(b, raw...)
		}
		return b
	}
	if err := osWriteFile(dir+"/gbk.dat", mk(gbk)); err != nil {
		t.Fatal(err)
	}
	if pe := charset.ProbeFileEncoding(dir+"/gbk.dat", 8192); pe != "gbk" {
		t.Errorf("真 GBK 字节流应判 gbk，got %s", pe)
	}
	// ② UTF8 中文字节流（金仓 V9 标 GBK 库的实存形态）
	utf := func() []byte {
		var b []byte
		for i := 0; i < 900; i++ {
			b = append(b, "历史数据行内容填充"...)
		}
		return b
	}
	if err := osWriteFile(dir+"/utf8.dat", mk(utf)); err != nil {
		t.Fatal(err)
	}
	if pe := charset.ProbeFileEncoding(dir+"/utf8.dat", 8192); pe != "utf8" {
		t.Errorf("UTF8 字节流应判 utf8，got %s", pe)
	}
	// ③ 纯 ASCII → unknown（采样不足，保持基线）
	if err := osWriteFile(dir+"/ascii.dat", mk(func() []byte { return []byte(strings.Repeat("abc123", 1300)) })); err != nil {
		t.Fatal(err)
	}
	if pe := charset.ProbeFileEncoding(dir+"/ascii.dat", 8192); pe != "unknown" {
		t.Errorf("纯 ASCII 应判 unknown，got %s", pe)
	}
}
