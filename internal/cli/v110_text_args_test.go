package cli

// v1.1.1：--text 参数解析与分隔符归一化测试。
// 重点防两类静默事故：
//  1. --delimiter '\t' 传进来是「反斜杠+t」两字符，旧实现只比较首字节 → 静默产出错位文件；
//  2. --text --header 同时给 → text 格式无表头，若忽略会把首行数据当列名/漏行。

import "testing"

func TestNormalizeDelimiter(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},   // 未指定：交给 ResolveDelimiter 按格式定默认值
		{",", ","}, // 单字符原样
		{"|", "|"},
		{"\t", "\t"}, // 真制表符
		{`\t`, "\t"}, // shell 里写的 '\t' 归一为制表符
		{"tab", "\t"},
	}
	for _, c := range cases {
		if got := NormalizeDelimiter(c.in); got != c.want {
			t.Fatalf("NormalizeDelimiter(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseArgsTextFlag(t *testing.T) {
	o := ParseArgs([]string{"datafile", "--text"})
	if !o.TextFormat {
		t.Fatal("--text 应置位 TextFormat")
	}
	if !o.Data {
		t.Fatal("--text 应同时置位 Data（复用数据导出链路）")
	}
	if got := o.ResolveDelimiter(); got != "\t" {
		t.Fatalf("--text 默认分隔符应为制表符，got %q", got)
	}
}

func TestParseArgsTextCustomDelimiter(t *testing.T) {
	o := ParseArgs([]string{"datafile", "--text", "--delimiter", "|"})
	if got := o.ResolveDelimiter(); got != "|" {
		t.Fatalf("显式 --delimiter 应生效，got %q", got)
	}
}

func TestParseArgsCsvDelimiterDefault(t *testing.T) {
	o := ParseArgs([]string{"datafile", "--data"})
	if got := o.ResolveDelimiter(); got != "," {
		t.Fatalf("--data 默认分隔符仍应为 ','，got %q", got)
	}
}
