// v1.2.2 回归测试：审计模式标记一致性（BUG-10）
//
// Author: raysuen
package engine

import (
	"strings"
	"testing"
)

// TestB19AuditMarkConsistency：同一"疑似删除"元组，在 --deleted 与
// --only-deleted 两个同为审计语义的模式下，标记必须一致。
//
// 旧实现（v1.2.1）：
//   - --deleted        → deleted=true, certain=false, live=false（audit 宽口径）
//     → marked = definitelyDeleted = false  → 不打标记
//   - --only-deleted   → 同一元组 marked = true（因 `if ri.OnlyDeleted && deleted`）
//
// → 同一行两模式标记不一致，下游按标记去重必错。
func TestB19AuditMarkConsistency(t *testing.T) {
	// hint 缺失（无 XMAX 位）、xmax!=0、无 CLOG → judgeXmax 返回 uncertain
	tu := mkTuple(100, 200, HEAP_XMIN_COMMITTED)

	// --deleted
	riD := &RowIter{PageSize: 8192, PGVersion: 17, TM: mkTestMeta(), IncludeDel: true}
	rowD := riD.buildRow(tu, [2]int{0, 1})
	if rowD == nil {
		t.Fatal("--deleted 应产出该行")
	}

	// --only-deleted（生产路径 export.go:392 同时置 IncludeDel=true）
	riO := &RowIter{
		PageSize: 8192, PGVersion: 17, TM: mkTestMeta(),
		IncludeDel: true, OnlyDeleted: true,
	}
	rowO := riO.buildRow(tu, [2]int{0, 1})
	if rowO == nil {
		t.Fatal("--only-deleted 应产出该行（宽口径）")
	}

	if rowD.Deleted != rowO.Deleted {
		t.Errorf("标记不一致：--deleted=%v, --only-deleted=%v", rowD.Deleted, rowO.Deleted)
	}
	if !rowD.Deleted {
		t.Error("审计模式下疑似删除行应被打标记（否则 --only-deleted 输出无标记的死行）")
	}
	if !rowD.Suspect || !rowO.Suspect {
		t.Errorf("疑似删除应置 Suspect=true：--deleted=%v, --only-deleted=%v", rowD.Suspect, rowO.Suspect)
	}
}

// TestB19MainPathNoMark：主导出路径不得把疑似删除行标成死行（v1.2.1 语义锁定）
func TestB19MainPathNoMark(t *testing.T) {
	tu := mkTuple(100, 200, HEAP_XMIN_COMMITTED)
	ri := &RowIter{PageSize: 8192, PGVersion: 17, TM: mkTestMeta()}
	row := ri.buildRow(tu, [2]int{0, 1})
	if row == nil {
		t.Fatal("主导出应产出该行（防丢行）")
	}
	if row.Deleted || row.Suspect {
		t.Errorf("主导出不应打任何死亡标记：Deleted=%v Suspect=%v", row.Deleted, row.Suspect)
	}
}

// TestDelMarkFormat：确定删除与疑似删除的注释格式
func TestDelMarkFormat(t *testing.T) {
	certain := delMark(&Row{CTID: "(0,1)"})
	if certain != "-- DELETED ctid=(0,1)" {
		t.Errorf("确定删除标记格式不符：%q", certain)
	}
	suspect := delMark(&Row{CTID: "(0,2)", Suspect: true})
	if suspect != "-- SUSPECTED_DELETED ctid=(0,2)" {
		t.Errorf("疑似删除标记格式不符：%q", suspect)
	}
	if !strings.Contains(suspect, "SUSPECTED") {
		t.Error("疑似删除标记应含 SUSPECTED 以便下游区分")
	}
}

// TestB19RealDeleteMarkStillDeleted：真删除（hint 完整）仍是确定删除，Suspect=false
//
// 注意：--only-deleted 在生产路径上由 export.go:392 同时置 IncludeDel=true
// （`IncludeDel: o.Deleted || o.OnlyDeleted`），此处必须一并设置，否则
// `!IncludeDel && definitelyDeleted` 会提前 return nil（仅为测试构造问题，
// 非产品缺陷）。
func TestB19RealDeleteMarkStillDeleted(t *testing.T) {
	tu := mkTuple(100, 200, HEAP_XMIN_COMMITTED|HEAP_XMAX_COMMITTED)
	ri := &RowIter{
		PageSize: 8192, PGVersion: 17, TM: mkTestMeta(),
		IncludeDel: true, OnlyDeleted: true,
	}
	row := ri.buildRow(tu, [2]int{0, 3})
	if row == nil {
		t.Fatal("真删除行应产出")
	}
	if !row.Deleted {
		t.Error("真删除应标 Deleted=true")
	}
	if row.Suspect {
		t.Error("真删除不应标 Suspect")
	}
}
