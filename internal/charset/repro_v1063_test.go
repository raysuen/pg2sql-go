//go:build repro

// v1.0.62 深度审计复现用例（charset 侧）
// 断言写的是【正确行为】，当前版本应当是 FAIL。
package charset

import (
	"strings"
	"testing"
)

// C-5：utf8Fallback 把合法的 U+FFFD（EF BF BD）也当非法序列，逐字节拆成 "ï¿½"。
// 触发条件：整串里有一处真非法字节（否则 utf8.Valid 直接放行），且另外含 U+FFFD。
func TestReproC5ValidReplacementChar(t *testing.T) {
	raw := []byte{0xEF, 0xBF, 0xBD, 0xFF} // 合法 U+FFFD + 非法单字节
	got := utf8Fallback(raw)
	if !strings.HasPrefix(got, "\uFFFD") {
		t.Fatalf("合法 U+FFFD 被破坏：得到 %q，期望以 %q 开头", got, "\uFFFD")
	}
}

// C-6：GB18030 四字节序列在末尾被截断（只剩 3 字节）时，不整体替换，
// 而是拿 b1/b2 去查双字节表 → 必然落空 → 吞 1 字节，剩下的 0x30 按 ASCII 输出 "0"。
func TestReproC6TruncatedGB18030(t *testing.T) {
	got := decodeGB18030([]byte{0x81, 0x30, 0x81})
	if strings.Contains(got, "0") {
		t.Fatalf("截断的四字节序列被拆成 ASCII：得到 %q（不应出现字符 0）", got)
	}
}
