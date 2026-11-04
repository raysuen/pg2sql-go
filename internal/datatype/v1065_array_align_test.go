package datatype

// v1.0.65 回归用例（F1/F2）：数组定长元素按 typalign 对齐。
// 上游依据：PG array.h "Individual items in the array are aligned as specified
// by the array element type" + arrayfuncs.c CopyArrayEls→ArrayCastAndSet
//（att_nominal_alignby 对定长元素同样生效）。
// 旧实现（v1.0.46"紧密排列"结论）只对 typlen 恰为 align 倍数的类型成立：
//   - timetz[]（12B/'d'）元素间距 16，旧实现按 12 推进 → 第 2 元素起解出垃圾；
//   - macaddr[]（6B/'i'，动态类型路径）元素间距 8，旧实现按 6 推进。
// 用例直接构造 PG 磁盘字节（含对齐填充），模拟真实落盘布局。

import (
	"encoding/binary"
	"math"
	"testing"
)

// timetz：8 字节微秒 + 4 字节时区（秒，正=UTC 以西）
func timetzElem(usec int64, zone int32) []byte {
	b := make([]byte, 12)
	binary.LittleEndian.PutUint64(b[0:], uint64(usec))
	binary.LittleEndian.PutUint32(b[8:], uint32(zone))
	return b
}

// F1（表内路径）：timetz[] 两个元素，间距 16（12+4 填充）。
func TestTimetzArrayElemAlign(t *testing.T) {
	var elems []byte
	elems = append(elems, timetzElem(54000000000, -8*3600)...) // 15:00:00+08
	elems = append(elems, 0, 0, 0, 0)                          // 'd' 对齐填充
	elems = append(elems, timetzElem(59400000000, -8*3600)...) // 16:30:00+08
	raw := wrapShort(arrPayload(1, 0, 1266, []int32{2}, []int32{1}, nil, elems))
	got := DecodeArray(raw)
	if want := "{15:00:00+08,16:30:00+08}"; got != want {
		t.Fatalf("timetz[] 定长元素对齐错误: got %q want %q", got, want)
	}
}

// 对照组：timetz[] 紧密排列（无填充）时第 2 元素本就该解错——
// 锁定"填充必须被消费"这一前提，防止有人把对齐逻辑删回紧密排列。
func TestTimetzArrayElemAlignTightIsGarbage(t *testing.T) {
	var elems []byte
	elems = append(elems, timetzElem(54000000000, -8*3600)...)
	elems = append(elems, timetzElem(59400000000, -8*3600)...) // 无填充（真实 PG 不产生此布局）
	raw := wrapShort(arrPayload(1, 0, 1266, []int32{2}, []int32{1}, nil, elems))
	got := DecodeArray(raw)
	if got == "{15:00:00+08,16:30:00+08}" {
		t.Fatalf("紧密布局不应解出正确结果（说明对齐填充未被消费）: got %q", got)
	}
}

// F2（macaddr[] 6B/'i'）：两个元素，间距 8（6+2 填充）。
// v1.0.65 起 macaddr/macaddr8 已进 arrayElemInfo（此前数组路径缺条目，
// 退化成原始字节 E'{\x08\x00+...}'，macaddr_in 拒绝），此用例同时锁定
// 「对齐正确」与「文本格式正确」。
func TestMacaddrArrayElemAlign(t *testing.T) {
	const macOID = 829
	var elems []byte
	elems = append(elems, 1, 2, 3, 4, 5, 6)
	elems = append(elems, 0, 0) // 'i' 对齐填充
	elems = append(elems, 17, 18, 19, 20, 21, 22)
	raw := wrapShort(arrPayload(1, 0, macOID, []int32{2}, []int32{1}, nil, elems))
	got := DecodeArray(raw)
	if want := "{01:02:03:04:05:06,11:12:13:14:15:16}"; got != want {
		t.Fatalf("macaddr[] 定长元素对齐错误: got %q want %q", got, want)
	}
}

// 回归护栏：typlen 恰为 align 倍数的类型（float8 8/'d'）在填充为 0 时行为不变。
func TestFloat8ArrayNoPadStillWorks(t *testing.T) {
	var elems []byte
	for _, f := range []float64{1.5, -2.25} {
		b := make([]byte, 8)
		binary.LittleEndian.PutUint64(b, math.Float64bits(f))
		elems = append(elems, b...)
	}
	raw := wrapShort(arrPayload(1, 0, 701, []int32{2}, []int32{1}, nil, elems))
	got := DecodeArray(raw)
	if want := "{1.5,-2.25}"; got != want {
		t.Fatalf("float8[] 回归: got %q want %q", got, want)
	}
}
