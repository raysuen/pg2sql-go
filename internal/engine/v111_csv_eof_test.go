// Author: raysuen
package engine

import "testing"

// TestV111CsvFieldEofMarker：B16 回归——单列且值恰为 `\.` 时，CSV 导出行就是 `\.`，
// psql/ksql 的 \copy 会把它当成数据结束标记 → 后续行静默丢弃（实测 10 行只进 7 行）。
// 修复后必须加引号，行变成 `"\."`。
func TestV111CsvFieldEofMarker(t *testing.T) {
	cases := []struct{ in, want string }{
		{`\.`, `"\."`},
		{`\N`, `"\N"`},   // NULL 记号同样必须加引号
		{`a\.b`, `a\.b`}, // 非整行匹配，无需引号
		{`..`, `..`},
		{"", ""},
		{"a,b", `"a,b"`},
		{"a\"b", `"a""b"`},
	}
	for _, c := range cases {
		if got := csvField(c.in, ","); got != c.want {
			t.Errorf("csvField(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestV111TextFieldEofMarker：text 格式下 `\.` 的反斜杠被翻倍为 `\\.`，天然不会撞上 EOF 标记。
func TestV111TextFieldEofMarker(t *testing.T) {
	if got := textField(`\.`, "\t"); got != `\\.` {
		t.Errorf("textField(`\\.`) = %q, want `\\\\.`", got)
	}
	if got := textField(`\N`, "\t"); got != `\\N` {
		t.Errorf("textField(`\\N`) = %q, want `\\\\N`", got)
	}
}
