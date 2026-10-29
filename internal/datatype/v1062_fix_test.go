// Author: raysuen
package datatype

import "testing"

// TestV1062LayoutAlignLowercase：P3 回归——PG 的 attalign 是小写。
// 旧实现只认大写 "S"，2 字节对齐列全部落到 attlen 兜底（int2 侥幸一致，
// 但 char(n)/定长自定义类型会错位）。
func TestV1062LayoutAlignLowercase(t *testing.T) {
	cases := []struct {
		attlen int
		align  string
		want   int
	}{
		{1, "c", 1}, {2, "s", 2}, {4, "i", 4}, {8, "d", 8},
		{2, "S", 2}, {4, "I", 4}, {8, "D", 8}, // 大写容错
		{-1, "i", 4}, // varlena
		{2, "", 2},   // 无 attalign 时按长度兜底
		{0, "", 1},   // 未知
		{16, "", 8},  // 超长定长按 8 对齐
		{3, "", 2},   // 3 字节 → 2 对齐
	}
	for _, c := range cases {
		if got := LayoutAlign(c.attlen, c.align); got != c.want {
			t.Errorf("LayoutAlign(%d, %q) = %d, want %d", c.attlen, c.align, got, c.want)
		}
	}
}
