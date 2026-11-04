// v1.2.6 P2 测试："char" 高位字节计数汇总（自清零，与 DefaultHitSummary 同款机制）。
// 作者: raysuen
package datatype

import (
	"strings"
	"testing"
)

func TestV126CharHighByteSummary(t *testing.T) {
	takeCharHighByteCount() // 清零起点
	if s := CharHighByteSummary(); s != "" {
		t.Fatalf("零计数应返回空串，得到 %q", s)
	}
	RecordCharHighByte()
	RecordCharHighByte()
	s := CharHighByteSummary()
	if !strings.Contains(s, "2") || !strings.Contains(s, `"char"`) {
		t.Fatalf("汇总应含计数 2 与 \"char\" 说明: %q", s)
	}
	// 自清零
	if s2 := CharHighByteSummary(); s2 != "" {
		t.Fatalf("汇总应自清零，再次得到 %q", s2)
	}
}
