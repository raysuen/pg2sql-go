package datatype

// v1.1.1：numeric 磁盘布局解码回归（B13 数值损坏修复）。
//
// PG numeric 用 base-10000 存整数位：value = Σ digits[i] × 10000^(weight-i)。
// 当整数部分「尾组全零」时 weight >= ndigits（如 10000 只有 1 个数字组但 weight=1），
// 旧实现把整数首位判成 0，整段整数坍缩为 "0"（numeric / json / jsonb 里的
// 10000、20000、1000000 这类值全部静默输出 0）。这里把各种边界钉死。

import (
	"encoding/binary"
	"testing"
)

// numericPayload 构造 long 格式 numeric 磁盘负载（裸 payload，无 varlena 头）
func numericPayload(neg bool, weight int16, dscale int, digits []uint16) []byte {
	p := make([]byte, 4+2*len(digits))
	h := uint16(dscale & 0x3FFF)
	if neg {
		h |= 0x4000
	}
	binary.LittleEndian.PutUint16(p[0:], h)
	binary.LittleEndian.PutUint16(p[2:], uint16(weight))
	for i, d := range digits {
		binary.LittleEndian.PutUint16(p[4+i*2:], d)
	}
	return p
}

func TestNumericTrailingZeroGroups(t *testing.T) {
	cases := []struct {
		name   string
		neg    bool
		weight int16
		dscale int
		digits []uint16
		want   string
	}{
		{"10000", false, 1, 0, []uint16{1}, "10000"}, // B13 主症状
		{"20000", false, 1, 0, []uint16{2}, "20000"},
		{"1000000", false, 1, 0, []uint16{100}, "1000000"}, // 100×10000^1，单组但 weight>0
		{"1e8", false, 2, 0, []uint16{1}, "100000000"},
		{"9999", false, 0, 0, []uint16{9999}, "9999"},
		{"12345678", false, 1, 0, []uint16{1234, 5678}, "12345678"},
		{"10000.5", false, 1, 1, []uint16{1, 0, 5000}, "10000.5"},
		{"1234.5678", false, 0, 4, []uint16{1234, 5678}, "1234.5678"},
		{"0.5", false, -1, 1, []uint16{5000}, "0.5"},
		{"-10000", true, 1, 0, []uint16{1}, "-10000"},
		{"0", false, 0, 0, nil, "0"},
		{"10000.00", false, 1, 2, []uint16{1}, "10000.00"},
	}
	for _, c := range cases {
		got := numericDiskToStr(numericPayload(c.neg, c.weight, c.dscale, c.digits))
		if got != c.want {
			t.Fatalf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// TestNumericPreservesZeroGroups：显式带 0 数字组的形态（10000.5 的真实存储）
func TestNumericPreservesZeroGroups(t *testing.T) {
	// 10000.5 → weight=1, digits=[1, 0, 5000], dscale=1
	got := numericDiskToStr(numericPayload(false, 1, 1, []uint16{1, 0, 5000}))
	if got != "10000.5" {
		t.Fatalf("got %q want 10000.5", got)
	}
	// 100000000.25 → weight=2, digits=[1, 0, 0, 2500], dscale=2
	got = numericDiskToStr(numericPayload(false, 2, 2, []uint16{1, 0, 0, 2500}))
	if got != "100000000.25" {
		t.Fatalf("got %q want 100000000.25", got)
	}
}
