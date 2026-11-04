package engine

// v1.1.1 新增：COPY text 格式（--text）回归测试。
//
// 期望值全部来自 172.16.0.141 实机取证（PG17.6，`\copy ... with (format text)`
// 后 od -c 逐字节比对），不是按文档抄的——服务端对控制字符的实际行为是
// 「只转义 \b \t \n \v \f \r 与反斜杠，其余控制字符原样输出」，
// 与凭直觉加八进制转义的实现不同，必须钉死。

import (
	"strings"
	"testing"

	"pg2sql/internal/config"
)

// TestTextFieldSevenEscapes：7 种转义逐字符比对
func TestTextFieldSevenEscapes(t *testing.T) {
	cases := []struct{ in, want string }{
		{`back\slash`, `back\\slash`},    // 反斜杠双写
		{"a\tb", `a\tb`},                 // 0x09
		{"line1\nline2", `line1\nline2`}, // 0x0a
		{"a\rb", `a\rb`},                 // 0x0d
		{"a\bb", `a\bb`},                 // 0x08
		{"a\fb", `a\fb`},                 // 0x0c
		{"a\vb", `a\vb`},                 // 0x0b
		{"plain text", "plain text"},     // 无需转义：快路径原样返回
		{"中文测试", "中文测试"},
		{"", ""}, // 空串：空字段（与 NULL 的 \N 区分）
	}
	for _, c := range cases {
		if got := textField(c.in, "\t"); got != c.want {
			t.Fatalf("textField(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestTextFieldControlCharsRaw：0x01-0x1F 整段，对齐服务端实测输出
func TestTextFieldControlCharsRaw(t *testing.T) {
	var src strings.Builder
	for i := 1; i <= 31; i++ {
		src.WriteByte(byte(i))
	}
	// 实测（od -c）：
	//   01 02 03 04 05 06 07 \b \t \n \v \f \r 0e 0f 10 ... 1f
	want := "\x01\x02\x03\x04\x05\x06\x07" + `\b\t\n\v\f\r` + "\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f"
	got := textField(src.String(), "\t")
	if got != want {
		t.Fatalf("控制字符转义与服务端不一致:\n got %q\nwant %q", got, want)
	}
	if strings.Contains(got, `\001`) {
		t.Fatal("不应使用八进制转义：服务端 COPY text 对 0x01 是原样输出")
	}
}

// TestTextFieldLiteralNull：字面量 \N 必须与 NULL 可区分
func TestTextFieldLiteralNull(t *testing.T) {
	if got := textField(`\N`, "\t"); got != `\\N` {
		t.Fatalf("字面量 \\N 应转义为 \\\\N（否则回灌被当 NULL），got %q", got)
	}
	if got := textField(`\x41`, "\t"); got != `\\x41` {
		t.Fatalf("bytea 十六进制应双写反斜杠（实测 PG 输出 \\\\x41），got %q", got)
	}
}

// TestTextFieldCustomDelimiter：非制表符分隔符时，值内分隔符需前置反斜杠
func TestTextFieldCustomDelimiter(t *testing.T) {
	if got := textField("a|b", "|"); got != `a\|b` {
		t.Fatalf("自定义分隔符未转义: %q", got)
	}
	// 制表符分隔时，'|' 是普通字符，不转义
	if got := textField("a|b", "\t"); got != "a|b" {
		t.Fatalf("制表符分隔下 '|' 不应转义: %q", got)
	}
}

// TestResolveDelimiter：CSV 默认 ','、text 默认制表符、显式值优先
func TestResolveDelimiter(t *testing.T) {
	csv := &config.Options{}
	if got := csv.ResolveDelimiter(); got != "," {
		t.Fatalf("CSV 默认分隔符应为 ',': %q", got)
	}
	txt := &config.Options{TextFormat: true, Data: true}
	if got := txt.ResolveDelimiter(); got != "\t" {
		t.Fatalf("text 默认分隔符应为制表符: %q", got)
	}
	explicit := &config.Options{TextFormat: true, Data: true, Delimiter: "|"}
	if got := explicit.ResolveDelimiter(); got != "|" {
		t.Fatalf("显式 --delimiter 应优先: %q", got)
	}
}

// TestFormatRowText：整行装配（NULL / 空串 / 字面量 \N / 制表符）逐字节对齐
// 服务端 `\copy ... (format text)` 输出：
//
//	1\t<NULL>\t\N(字面)\t(空串)\t含制表符
func TestFormatRowText(t *testing.T) {
	tm := &TableMeta{
		Schema:  "public",
		RelName: "t_fmt",
		Columns: []ColumnDef{
			{Name: "id", TypeOID: 23, AttNum: 1},
			{Name: "nul", TypeOID: 25, AttNum: 2},
			{Name: "lit", TypeOID: 25, AttNum: 3},
			{Name: "emp", TypeOID: 25, AttNum: 4},
			{Name: "tab", TypeOID: 25, AttNum: 5},
		},
	}
	c := buildFmtCtx(tm, fmtText, false, nil, "\t")
	id, lit, emp, tabv := "1", `\N`, "", "a\tb"
	row := &Row{Values: []*string{&id, nil, &lit, &emp, &tabv}}
	got := c.formatRow(row)
	want := "1\t\\N\t\\\\N\t\ta\\tb"
	if got != want {
		t.Fatalf("整行不符: got %q want %q", got, want)
	}
	// 列数核对：5 列 → 4 个分隔符，且末字段后无分隔符
	if n := strings.Count(got, "\t"); n != 4 {
		t.Fatalf("分隔符数应为 4（末列后不加），实际 %d", n)
	}
	if c.headerLine() != "" {
		t.Fatal("COPY text 不应产生表头行")
	}
}
