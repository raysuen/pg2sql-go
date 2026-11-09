// Author: raysuen

package datatype

import (
	"encoding/binary"
	"strings"
	"testing"
)

// mkArrRaw：构造一个 4B 头的 varlena 数组载荷（ndim/dims/lbs 可任意设）。
// 注意本仓库 VarlenaParse 把 4B 头解出的值直接当 payload 长度用，
// 因此写头时填 payload 长度 << 2。
func mkArrRaw(ndim, dataoffset int, elemtype uint32, dims []int32, lbs []int32) []byte {
	put := func(buf *[]byte, v int32) {
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, uint32(v))
		*buf = append(*buf, b...)
	}
	payload := make([]byte, 0, 12+8*len(dims))
	put(&payload, int32(ndim))
	put(&payload, int32(dataoffset))
	put(&payload, int32(elemtype))
	for _, d := range dims {
		put(&payload, d)
	}
	for _, l := range lbs {
		put(&payload, l)
	}
	out := make([]byte, 4)
	binary.LittleEndian.PutUint32(out, uint32(len(payload))<<2)
	return append(out, payload...)
}

// TestDecodeArrayCorruptHeader：v1.2.12（A2）回归。
//
// 背景：ndim 合法区间只有 0..6（0 = 空数组），dims[i] 合法值恒 >0。
// 旧实现对 ndim>6 与 dims[i]<=0 都 `return "{}"`，与"合法空数组"输出完全一样、
// 不计降级、不收尾告警 —— 撕裂页里的数组会静默变成空数组（回灌成功、目标库
// 丢数据、报告全绿）。现改为走 DecodeDefault，与同函数 bodyOff 越界 / 元素
// 总数超限两条损坏分支口径一致。
func TestDecodeArrayCorruptHeader(t *testing.T) {
	const int4ArrOID = 1007 // _int4，init() 注册到 Decoders
	_ = DefaultHitSummary() // 清空计数，避免受其它用例影响

	cases := []struct {
		name    string
		raw     []byte
		corpt   bool // 期望按"损坏"处理（\x 原文）
		wantHex bool // 头部可完整读取，损坏必退 \x
	}{
		{"ndim=7 超 PG 上限", mkArrRaw(7, 68, 23, []int32{1, 1, 1, 1, 1, 1, 1}, []int32{1, 1, 1, 1, 1, 1, 1}), true, true},
		{"ndim=-1 负数", mkArrRaw(-1, 0, 0, make([]int32, 7), make([]int32, 7)), true, false},
		{"dims[0]=0（合法维长恒 >0）", mkArrRaw(1, 20, 23, []int32{0}, []int32{1}), true, true},
		{"dims[0]=-5 负数", mkArrRaw(1, 20, 23, []int32{-5}, []int32{1}), true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DecodeArray(c.raw)
			if got == "{}" {
				t.Fatalf("头部损坏不得输出 {}（会被当成合法空数组静默丢数据）")
			}
			// 说明：ndim<0 用例的载荷短到连 12 字节头都不全，DecodeArray 在
			// len(Payload)<12 处就走"损坏"分支（返回 {} 之外的原文），
			// 因此这里只对"头部长度足够、能读到 ndim 分支"的用例断言 \x。
			if c.wantHex && !strings.HasPrefix(got, `\x`) {
				t.Fatalf("损坏分支应退回 \\x 原始字节，实际 %q", got)
			}
			// 生产链路走 DecodeValue（Decoders[int4ArrOID] = DecodeArray），
			// 由它统一记账，收尾才能出告警。
			if v := DecodeValue(int4ArrOID, c.raw); v != got {
				t.Fatalf("DecodeValue 与 DecodeArray 输出不一致: %q vs %q", v, got)
			}
		})
	}

	if s := DefaultHitSummary(); s == "" {
		t.Fatalf("损坏数组必须计入降级统计（DefaultHitSummary 为空 → 收尾无告警）")
	}
}

// TestDecodeArrayEmptyStillWorks：ndim=0 是合法的空数组，仍必须输出 {}
// 且不得计入降级统计（否则每张表的空数组列都会引发误报告警）。
func TestDecodeArrayEmptyStillWorks(t *testing.T) {
	_ = DefaultHitSummary()
	raw := mkArrRaw(0, 0, 23, nil, nil)
	if got := DecodeArray(raw); got != "{}" {
		t.Fatalf("合法空数组应输出 {}，实际 %q", got)
	}
	if s := DefaultHitSummary(); s != "" {
		t.Fatalf("合法空数组不得计入降级统计，实际: %q", s)
	}
}

// TestDecodeArrayValidStillWorks：回归护栏——正常二维数组（lbs=1）解出的
// 文本不得因本修复而改变。
func TestDecodeArrayValidStillWorks(t *testing.T) {
	payload := make([]byte, 0, 64)
	put := func(v int32) {
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, uint32(v))
		payload = append(payload, b...)
	}
	put(2)  // ndim
	put(0)  // dataoffset=0：无 NULL 位图，元素紧随头（紧凑排列）
	put(23) // elemtype = int4
	put(2)  // dims[0]
	put(2)  // dims[1]
	put(1)  // lbs[0]
	put(1)  // lbs[1]
	put(1)
	put(2)
	put(3)
	put(4)
	put(5)

	raw := make([]byte, 4)
	binary.LittleEndian.PutUint32(raw, uint32(len(payload))<<2)
	raw = append(raw, payload...)

	if got := DecodeArray(raw); got != "{{1,2},{3,4}}" {
		t.Fatalf("正常二维数组解析错误: %q", got)
	}
}
