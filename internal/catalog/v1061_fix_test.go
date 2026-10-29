package catalog

// v1.0.61 第三批回归（P2-4 / P2-5）：索引键列与 INCLUDE 列分离、分区元数据缓存键。

import (
	"testing"
)

// TestV1061IndexKeyAtts —— P2-4：indnkeyatts 截断，INCLUDE 列不得并入键列。
func TestV1061IndexKeyAtts(t *testing.T) {
	ir := &IndexRow{IndKey: []int{1, 2, 3}, IndNKeyAtts: 1}
	if got := ir.keyAtts(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("keyAtts = %v, want [1]", got)
	}
	if got := ir.includeAtts(); len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("includeAtts = %v, want [2 3]", got)
	}
	// 无 indnkeyatts（PG10-/金仓 V8）：全键，无 INCLUDE
	old := &IndexRow{IndKey: []int{4, 5}}
	if got := old.keyAtts(); len(got) != 2 {
		t.Errorf("hasoids 布局 keyAtts = %v, want 全 2 列", got)
	}
	if got := old.includeAtts(); got != nil {
		t.Errorf("hasoids 布局不应有 INCLUDE: %v", got)
	}
	// nkey >= len：无 INCLUDE
	full := &IndexRow{IndKey: []int{7}, IndNKeyAtts: 1}
	if full.includeAtts() != nil {
		t.Error("nkey==len 时不应产生 INCLUDE")
	}
	// nil 接收者安全
	var nilIR *IndexRow
	if nilIR.keyAtts() != nil || nilIR.includeAtts() != nil {
		t.Error("nil IndexRow 应返回 nil")
	}
}

// TestV1061PartMetaKey —— P2-5：缓存键必须区分 version/isKB。
func TestV1061PartMetaKey(t *testing.T) {
	a := partMetaKey("/data/base/16384", 16, false)
	b := partMetaKey("/data/base/16384", 9, false)
	c := partMetaKey("/data/base/16384", 16, true)
	if a == b || a == c || b == c {
		t.Fatalf("缓存键未区分参数: %q %q %q", a, b, c)
	}
	if a != partMetaKey("/data/base/16384", 16, false) {
		t.Error("相同参数应产生相同键")
	}
}
