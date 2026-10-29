package engine

// v1.0.60 回归测试（P1-9）：TryAutoLayout 页头长度少 4 字节。
//
// 现场背景（审计 2026-10-28）：堆页头是 24B（pd_lsn..pd_pagesize_version 共 20B
// + pd_prune_xid 4B）。旧实现 headerEnd=(psvOff+2+3)&^3=20，把 pd_prune_xid
// 当成 ItemId[0]；而 TryStandardLayout 只认页版本 4，金仓页版本=5 → 金仓所有堆页
// 都走本函数。后果：CTID 页内偏移全体 +1；pd_prune_xid 碰巧长得像 ItemId 时还会
// 多解析出一条垃圾行。
//
// 这里用合成 v5 页把两条都钉住：HeaderEnd 必须是 24，且把 pd_prune_xid 写成
// 「合法 ItemId 形态」后仍然只能解析出真实的那一条元组。

import "testing"

// t60KBPageV5：构造金仓页（pd_pagesize_version = 8192<<8 | 5），
// 一页一个元组，pruneXid 写入 20–23 字节。
func t60KBPageV5(pruneXid uint32) (page []byte, tupleOff int) {
	ps := 8192
	page = make([]byte, ps)
	tup := t54Tuple(2, []byte("abcdefgh"))
	tupleOff = ps - len(tup)
	copy(page[tupleOff:], tup)

	// pd_lower：页头 24B + 1 个 ItemId
	t54Put16(page, 12, 28)
	t54Put16(page, 14, tupleOff)
	t54Put16(page, 16, ps)
	// pd_pagesize_version：高字节存 8192>>8=0x20、低字节存版本号 → U16 值 8197
	t54Put16(page, 18, ps+KB_PAGE_VERSION)
	// pd_prune_xid（20–23）：页版本 4/5 起存在
	page[20] = byte(pruneXid)
	page[21] = byte(pruneXid >> 8)
	page[22] = byte(pruneXid >> 16)
	page[23] = byte(pruneXid >> 24)

	// 真正的 ItemId[0] 从 24 开始
	raw := uint32(ITEMID_NORMAL<<15) | uint32(tupleOff&0x7FFF) | uint32(len(tup)&0x7FFF)<<17
	page[24] = byte(raw)
	page[25] = byte(raw >> 8)
	page[26] = byte(raw >> 16)
	page[27] = byte(raw >> 24)
	return page, tupleOff
}

// prune_xid 取一个"恰好像 NORMAL ItemId"的值：flags=1/off=1000/len=32。
func t60PruneLooksLikeItemId() uint32 {
	return uint32(ITEMID_NORMAL<<15) | uint32(1000) | uint32(32)<<17
}

func TestV1060AutoLayoutHeaderEnd24(t *testing.T) {
	page, _ := t60KBPageV5(t60PruneLooksLikeItemId())
	lay := TryAutoLayout(page, 8192)
	if lay == nil {
		t.Fatal("金仓 v5 页应能走 auto 布局（TryStandardLayout 只认 v4）")
	}
	if lay.HeaderEnd != 24 {
		t.Fatalf("HeaderEnd = %d，want 24（页头含 pd_prune_xid，少 4B 会让 ItemId 数组整体错位）", lay.HeaderEnd)
	}
	if lay.Version != KB_PAGE_VERSION {
		t.Fatalf("Version = %d，want %d", lay.Version, KB_PAGE_VERSION)
	}
}

func TestV1060PruneXidNotItemId(t *testing.T) {
	page, tupleOff := t60KBPageV5(t60PruneLooksLikeItemId())
	ri := &RowIter{PageSize: 8192, PGVersion: 12}
	items, ok := ri.IterTuples(0, page)
	if !ok {
		t.Fatal("布局应可解析")
	}
	if len(items) != 1 {
		t.Fatalf("解析出 %d 条元组，want 1（pd_prune_xid 被当成 ItemId[0] 时旧实现会多出一条/整体错位）", len(items))
	}
	tup := items[0][1].(*HeapTuple)
	want := page[tupleOff:]
	if len(tup.Raw) != len(want) || string(tup.Raw) != string(want) {
		t.Fatalf("解析到的元组不是页尾那条（len=%d want=%d）——ItemId 数组起点从 20 起算时整表 CTID 会全体 +1 且可能读到 prune_xid 位置", len(tup.Raw), len(want))
	}
}
