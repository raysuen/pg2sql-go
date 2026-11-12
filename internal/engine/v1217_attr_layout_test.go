// v1.2.17 回归用例：金仓 pg_attribute 布局的 attinhcount 宽度。
//
// 缺陷：金仓的 attinhcount 是 int4，PG 是 int2。KB 布局照抄 PG 写成 2 字节，
// 少算 2 字节后紧随其后的 attcollation 被读到 attinhcount 的槽位
// （普通列恒 0）→ ColumnDef.Collation=0 → DDL 里 `text COLLATE "C"` 的
// COLLATE 整段消失，回灌后排序规则静默回到库默认。
//
// 期望偏移取自实机目录（金仓 V8R6C9B14 / V9R1C10B4，pg/oracle/mysql 三种模式一致）：
//
//	attnum 26 attinhcount  atttypid=23(int4) attlen=4
//	attnum 27 attcollation atttypid=26(oid)  attlen=4
package engine

import (
	"testing"

	"pg2sql/internal/datatype"
)

// fieldOffsets：按布局自带的 attlen/attalign 复算每个字段的起始偏移。
// 只用 attlen>0 的定长列（varlena 列在 attnotnull 之后的尾部，不影响被测下标）。
func fieldOffsets(layout []datatype.ColLen) []int {
	off := 0
	offs := make([]int, len(layout))
	for i, c := range layout {
		if c.AttLen > 0 {
			a := datatype.LayoutAlign(c.AttLen, c.AttAlign)
			if a > 1 {
				off = (off + a - 1) / a * a
			}
		}
		offs[i] = off
		if c.AttLen > 0 {
			off += c.AttLen
		}
	}
	return offs
}

func TestV1217KbAttrLayoutCollationOffset(t *testing.T) {
	layout := pgAttributeLayout(12, true) // 金仓（PG12 内核）
	if len(layout) < 21 {
		t.Fatalf("layout 列数不足: %d", len(layout))
	}
	if got := layout[19]; got.AttLen != 4 || got.AttAlign != "i" {
		t.Errorf("attinhcount 列宽 = %d/%s, want 4/i（金仓为 int4）", got.AttLen, got.AttAlign)
	}
	offs := fieldOffsets(layout)
	// 4 字节 inhcount：islocal(101) 之后按 4 对齐到 104，collation 起于 108
	if offs[20] != 108 {
		t.Errorf("attcollation 偏移 = %d, want 108；若为 104 说明 inhcount 仍按 2 字节算，"+
			"读到的是 attinhcount(恒 0)", offs[20])
	}
}

// 反向断言：PG 的 attinhcount 确实是 int2，PG 17 布局不得被这次修改带偏。
func TestV1217PgAttrLayoutUnchanged(t *testing.T) {
	layout := pgAttributeLayout(17, false)
	if got := layout[19]; got.AttLen != 2 || got.AttAlign != "s" {
		t.Errorf("PG17 attinhcount 列宽 = %d/%s, want 2/s", got.AttLen, got.AttAlign)
	}
	offs := fieldOffsets(layout)
	if offs[20] != 100 {
		t.Errorf("PG17 attcollation 偏移 = %d, want 100", offs[20])
	}
}

// attrIdx 与布局必须一致：下标 20 既是布局里的 collation，也是名字表里的 attcollation。
func TestV1217AttrIdxMatchesLayout(t *testing.T) {
	for _, v := range []int{12, 14, 16, 17, 18} {
		for _, isKB := range []bool{true, false} {
			idx := attrIdx(v, isKB)
			if _, ok := idx["attcollation"]; !ok {
				t.Errorf("attrIdx(%d, kb=%v) 缺 attcollation", v, isKB)
			}
			if idx["attcollation"] >= len(pgAttributeLayout(v, isKB)) {
				t.Errorf("attrIdx(%d, kb=%v) attcollation 下标越界: %d",
					v, isKB, idx["attcollation"])
			}
		}
	}
}
