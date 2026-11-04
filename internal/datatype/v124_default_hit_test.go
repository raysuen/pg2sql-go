// Author: raysuen
package datatype

import (
	"strings"
	"testing"
)

// v1.2.4：\x 退化静默兜底告警机制测试。
// 背景：BUG-13 同类风险扫描（BUG13_SIMILAR_RISK_SCAN.md §5）结论——
// 漏注册解码器后 DecodeDefault 静默输出 \x，不告警、退出码 0，才是真正风险。
// 本组用例锁定「出口拦截 → 计数 → 汇总 → 清零」整条链路。

func resetDefaultHits() func() {
	defaultHitMu.Lock()
	old := defaultHitCounts
	defaultHitCounts = map[uint32]int{}
	defaultHitMu.Unlock()
	return func() {
		defaultHitMu.Lock()
		defaultHitCounts = old
		defaultHitMu.Unlock()
	}
}

// 未注册 oid 输出 \x → 必须被记录
func TestV124DefaultHitRecorded(t *testing.T) {
	defer resetDefaultHits()()
	// 999999 不在 Decoders/DynamicDecoders → 落 DecodeDefault
	got := DecodeValue(999999, []byte{0x01, 0x02, 0x00, 0x03})
	if !strings.HasPrefix(got, `\x`) {
		t.Fatalf("应退化输出 \\x，got %q", got)
	}
	s := DefaultHitSummary()
	if s == "" {
		t.Fatal("退化后 DefaultHitSummary 应非空")
	}
	if !strings.Contains(s, "999999") || !strings.Contains(s, "×1") {
		t.Fatalf("汇总应含 oid 999999(×1)，got %q", s)
	}
}

// bytea 的 \x 是合法格式，不得计入
func TestV124ByteaHexNotRecorded(t *testing.T) {
	defer resetDefaultHits()()
	got := DecodeValue(BYTEAOID, []byte{0xde, 0xad, 0xbe, 0xef})
	if !strings.HasPrefix(got, `\x`) {
		t.Fatalf("bytea 应输出 \\x 格式，got %q", got)
	}
	if s := DefaultHitSummary(); s != "" {
		t.Fatalf("bytea 的 \\x 属合法输出，不应告警，got %q", s)
	}
}

// 正常解码不得误报
func TestV124NormalDecodeNotRecorded(t *testing.T) {
	defer resetDefaultHits()()
	_ = DecodeValue(INT4OID, []byte{1, 0, 0, 0})
	_ = DecodeValue(TEXTOID, []byte{5, 'h', 'i'})
	if s := DefaultHitSummary(); s != "" {
		t.Fatalf("正常解码不应告警，got %q", s)
	}
}

// 汇总后计数清零（每轮导出独立统计）
func TestV124SummaryResets(t *testing.T) {
	defer resetDefaultHits()()
	_ = DecodeValue(999999, []byte{0, 1, 2})
	if DefaultHitSummary() == "" {
		t.Fatal("首次汇总应为空前非空")
	}
	if s := DefaultHitSummary(); s != "" {
		t.Fatalf("汇总应自清零，第二次应为空，got %q", s)
	}
}

// 同一 oid 多次退化 → 计数累加
func TestV124HitCountAccumulates(t *testing.T) {
	defer resetDefaultHits()()
	for i := 0; i < 3; i++ {
		_ = DecodeValue(888888, []byte{0, 0, 0, 0})
	}
	s := DefaultHitSummary()
	if !strings.Contains(s, "888888(×3)") {
		t.Fatalf("应累计为 ×3，got %q", s)
	}
}

// 回归锚：BUG-13 原始场景——若有人撤掉 ACLITEMOID 注册，
// 该用例立即 FAIL，且 DefaultHitSummary 能报出 oid 1033。
func TestV124AclitemScalarStillRegistered(t *testing.T) {
	defer resetDefaultHits()()
	raw := []byte{0x0a, 0, 0, 0, 0x0a, 0, 0, 0, 0x7f, 0x10, 0, 0}
	got := DecodeValue(ACLITEMOID, raw)
	if strings.HasPrefix(got, `\x`) {
		t.Fatalf("BUG-13 回归：aclitem 标量退化为 \\x（分派表条目被删？）: %q", got)
	}
	if s := DefaultHitSummary(); s != "" {
		t.Fatalf("不应有退化告警，got %q", s)
	}
}
