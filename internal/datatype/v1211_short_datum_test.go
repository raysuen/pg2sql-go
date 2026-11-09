// v1.2.11 回归用例（BUG-2）：定长解码器对短输入的越界 panic。
//
// 缺陷：23 个定长解码器直接按下标取值（binary.LittleEndian.Uint64(B) 等），
// 对 len < 类型宽度 的输入立即越界 panic（空输入即
// "index out of range [7] with length 0"）。DecodeValue 现已按
// minDecoderWidth 预检，不足则按十六进制原文输出并计数告警。
package datatype

import (
	"encoding/hex"
	"strings"
	"testing"
)

// decodePanics：调用解码器，判断是否 panic。
func decodePanics(d func([]byte) string, n int) (panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
		}
	}()
	d(make([]byte, n))
	return false
}

// TestAllDecodersRejectShortInput：穷举所有已注册解码器 × 长度 0~16，
// 经 DecodeValue 统一入口调用必须不 panic。新增定长解码器而忘记补
// minDecoderWidth 时本用例会直接失败。
func TestAllDecodersRejectShortInput(t *testing.T) {
	if len(Decoders) == 0 {
		t.Fatal("Decoders 为空，用例失效")
	}
	for oid := range Decoders {
		for n := 0; n <= 16; n++ {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("oid=%d len=%d 经 DecodeValue 仍 panic: %v", oid, n, r)
					}
				}()
				_ = DecodeValue(oid, make([]byte, n))
			}()
		}
	}
}

// TestMinDecoderWidthMatchesTable：minDecoderWidth 的每一项都必须"恰好"——
// 宽度-1 直接调解码器必 panic（证明不小于实际读取宽度），
// 宽度直接调解码器必不 panic（证明不虚高，不会误伤合法数据）。
func TestMinDecoderWidthMatchesTable(t *testing.T) {
	if len(minDecoderWidth) == 0 {
		t.Fatal("minDecoderWidth 为空，用例失效")
	}
	for oid, need := range minDecoderWidth {
		d := Decoders[oid]
		if d == nil {
			t.Errorf("oid=%d 在 minDecoderWidth 中但 Decoders 未注册", oid)
			continue
		}
		if need < 2 {
			t.Errorf("oid=%d 宽度 %d 不像定长类型", oid, need)
			continue
		}
		if !decodePanics(d, need-1) {
			t.Errorf("oid=%d 宽度偏大：%d 字节即可解码，表里写 %d", oid, need-1, need)
		}
		if decodePanics(d, need) {
			t.Errorf("oid=%d 宽度偏小：%d 字节仍 panic", oid, need)
		}
	}
	// 反向：所有"会 panic 的定长解码器"都必须在表里，避免遗漏。
	for oid := range Decoders {
		if _, has := minDecoderWidth[oid]; has {
			continue
		}
		if decodePanics(Decoders[oid], 0) || decodePanics(Decoders[oid], 1) {
			t.Errorf("oid=%d 对短输入 panic 但未登记 minDecoderWidth（需补表）", oid)
		}
	}
}

// TestDecodeValueShortDatumHex：长度不足的值按 \x 原文输出，且不污染
// DefaultHitSummary（两个计数器语义不同）。
func TestDecodeValueShortDatumHex(t *testing.T) {
	_ = DefaultHitSummary() // 清零
	_ = ShortDatumSummary() // 清零

	raw := []byte{0x01, 0x02, 0x03}
	got := DecodeValue(1114, raw) // timestamp 需 8 字节
	want := "\\x" + hex.EncodeToString(raw)
	if got != want {
		t.Errorf("长度不足应输出十六进制原文：got=%q want=%q", got, want)
	}
	// 长度足够时行为不变（回归保护：不能把合法值也打成 hex）
	ok := make([]byte, 8)
	if s := DecodeValue(1114, ok); strings.HasPrefix(s, "\\x") {
		t.Errorf("长度足够的 timestamp 不应退化为 hex：%q", s)
	}

	if s := DefaultHitSummary(); s != "" {
		t.Errorf("短值不应计入 \\x 退化统计：%q", s)
	}
	s := ShortDatumSummary()
	if !strings.Contains(s, "1114") {
		t.Errorf("ShortDatumSummary 未报出 oid=1114：%q", s)
	}
	// 自清零
	if s := ShortDatumSummary(); s != "" {
		t.Errorf("ShortDatumSummary 未自清零：%q", s)
	}
}

// TestDecodeValueShortDatumAllWidths：表里每个 oid 的宽度-1 输入都必须走
// 十六进制兜底（端到端，不经 recover）。
func TestDecodeValueShortDatumAllWidths(t *testing.T) {
	_ = ShortDatumSummary()
	for oid, need := range minDecoderWidth {
		got := DecodeValue(oid, make([]byte, need-1))
		if !strings.HasPrefix(got, "\\x") {
			t.Errorf("oid=%d 宽度-1 输入未走 \\x 兜底：%q", oid, got)
		}
	}
	if s := ShortDatumSummary(); s == "" {
		t.Error("全部短值未产生汇总告警")
	}
}
