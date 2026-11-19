// v1.2.22（P1）：pg_class 同 OID 多版本元组的合并口径。
//
// 背景：建表 = 插初始行（reltoastrelid=0）+ UPDATE 补 TOAST OID，堆页里两份都在。
// 旧实现取物理序第一份（初始行）→ 该表 TOAST 不被识别 → 外置列静默全 NULL。
// 实测：建表+插入+CHECKPOINT 后立刻导出，旧版 pad 列 100% \N；ANALYZE 后才恢复。
package catalog

import (
	"testing"

	"pg2sql/internal/engine"
)

func mkRow(oid uint32, toast uint32) *ClassRow {
	return &ClassRow{OID: oid, RelName: "t", ToastRelID: toast}
}

// TestV1222MergeTakesChainEnd：初始行（reltoastrelid=0，非链尾）在前，
// 补值行（链尾）在后 —— 必须取链尾。
func TestV1222MergeTakesChainEnd(t *testing.T) {
	cands := []classCand{
		{row: mkRow(100, 0), chainEnd: false},  // 初始插入
		{row: mkRow(100, 200), chainEnd: true}, // UPDATE 补 reltoastrelid
	}
	got := mergeClassCands(cands)
	if len(got) != 1 {
		t.Fatalf("同一 OID 应合并成 1 行，实得 %d", len(got))
	}
	if got[0].ToastRelID != 200 {
		t.Fatalf("应取链尾版本的 reltoastrelid=200，实得 %d（会丢 TOAST）", got[0].ToastRelID)
	}
}

// TestV1222MergeChainEndBeatsLaterOld：链尾版本即使在物理序上更早，也不应被
// 后面的旧版本覆盖（页内顺序不保证）。
func TestV1222MergeChainEndBeatsLaterOld(t *testing.T) {
	cands := []classCand{
		{row: mkRow(100, 200), chainEnd: true},
		{row: mkRow(100, 0), chainEnd: false},
	}
	got := mergeClassCands(cands)
	if len(got) != 1 || got[0].ToastRelID != 200 {
		t.Fatalf("链尾版本被旧版本覆盖：%+v", got[0])
	}
}

// TestV1222MergeAllOldFallsBackToLast：链被截断（两份都不是链尾）时取后出现的，
// 不丢表、不返回空。
func TestV1222MergeAllOldFallsBackToLast(t *testing.T) {
	cands := []classCand{
		{row: mkRow(100, 0), chainEnd: false},
		{row: mkRow(100, 200), chainEnd: false},
	}
	got := mergeClassCands(cands)
	if len(got) != 1 || got[0].ToastRelID != 200 {
		t.Fatalf("全为旧版本时应取后者：%+v", got[0])
	}
}

// TestV1222MergeKeepsOrderAndDistinct：不同 OID 保持首次出现顺序，数量不变。
func TestV1222MergeKeepsOrderAndDistinct(t *testing.T) {
	cands := []classCand{
		{row: mkRow(300, 0), chainEnd: true},
		{row: mkRow(100, 0), chainEnd: true},
		{row: mkRow(300, 310), chainEnd: true},
		{row: mkRow(200, 0), chainEnd: true},
	}
	got := mergeClassCands(cands)
	if len(got) != 3 {
		t.Fatalf("应得 3 行，实得 %d", len(got))
	}
	if got[0].OID != 300 || got[1].OID != 100 || got[2].OID != 200 {
		t.Fatalf("顺序应保持首次出现：%d,%d,%d", got[0].OID, got[1].OID, got[2].OID)
	}
	if got[0].ToastRelID != 310 {
		t.Fatalf("OID 300 应取后一条链尾（310），实得 %d", got[0].ToastRelID)
	}
}

// TestV1222MergeNilRowSkipped：空行（解析失败）不参与合并。
func TestV1222MergeNilRowSkipped(t *testing.T) {
	got := mergeClassCands([]classCand{{row: nil}, {row: mkRow(100, 200), chainEnd: true}})
	if len(got) != 1 || got[0].OID != 100 {
		t.Fatalf("nil 行应被跳过：%+v", got)
	}
}

// TestV1222TupleCTIDParse：t_ctid 解析口径（BlockIdData{hi,lo} + posid）。
func TestV1222TupleCTIDParse(t *testing.T) {
	raw := make([]byte, 24)
	// t_ctid @12：block=7（hi=0, lo=7）、offnum=3
	raw[12], raw[13] = 0, 0
	raw[14], raw[15] = 7, 0
	raw[16], raw[17] = 3, 0
	raw[22] = 23 // t_hoff
	tup := engine.ParseTuple(raw, 17)
	if tup == nil {
		t.Fatal("ParseTuple 返回 nil")
	}
	if want := engine.PackTID(7, 3); tup.CTID != want {
		t.Fatalf("CTID = 0x%x，期望 0x%x", tup.CTID, want)
	}
	// SelfTID 未回填 → 视为链尾（不误杀）
	if !tup.IsSelfCTID() {
		t.Fatal("SelfTID 未回填时应返回 true")
	}
	tup.SelfTID = engine.PackTID(7, 3)
	if !tup.IsSelfCTID() {
		t.Fatal("t_ctid 自指时应判定为链尾")
	}
	tup.SelfTID = engine.PackTID(7, 4)
	if tup.IsSelfCTID() {
		t.Fatal("t_ctid 指向别处时应判定为非链尾（已更新）")
	}
	// 高位块号（跨段）不能被位宽截断
	raw2 := make([]byte, 24)
	raw2[12], raw2[13] = 1, 0 // hi=1 → block 高位
	raw2[14], raw2[15] = 0, 0
	raw2[16], raw2[17] = 2, 0
	raw2[22] = 23
	if t2 := engine.ParseTuple(raw2, 17); t2 == nil || t2.CTID != engine.PackTID(1<<16, 2) {
		t.Fatalf("大块号解析错误：%+v", t2)
	}
}
