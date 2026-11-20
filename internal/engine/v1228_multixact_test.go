package engine

import (
	"encoding/binary"
	"testing"
)

// buildMultiTuple：构造一个 t_infomask / t_infomask2 / xmax 可控的最小 heap 元组。
// 头部 23B：xmin@0 xmax@4 ctid@12 infomask2@18 infomask@20 thoff@22。
func buildMultiTuple(xmax uint32, infomask, infomask2 uint16) []byte {
	raw := make([]byte, 24)
	binary.LittleEndian.PutUint32(raw[0:], 500) // xmin
	binary.LittleEndian.PutUint32(raw[4:], xmax)
	binary.LittleEndian.PutUint16(raw[12:], 0) // ctid bi_hi
	binary.LittleEndian.PutUint16(raw[14:], 0) // ctid bi_lo
	binary.LittleEndian.PutUint16(raw[16:], 1) // ctid offnum
	binary.LittleEndian.PutUint16(raw[18:], infomask2)
	binary.LittleEndian.PutUint16(raw[20:], infomask)
	raw[22] = 24 // thoff
	return raw
}

// v1.2.28（审计 #1）：FOR KEY SHARE 锁 + 另一事务 DELETE 后，xmax 为 multixact、
// HEAP_XMAX_IS_MULTI 置于 t_infomask、HEAP_KEYS_UPDATED 置于 t_infomask2。
// 旧实现拿 t_infomask 测 0x2000 恒假 → 删行被当活行导出。
// v1.2.29（审计残留 P1）：NoKeyUpdate updater 不置 KEYS_UPDATED，multi 分支改为
// 「LOCK_ONLY 未置即判删」——IS_MULTI 且无 LOCK_ONLY 一律不可见。
func TestMultiXactKeysUpdatedIsDeleted(t *testing.T) {
	// 多成员 multi（含 updater，已提交）：infomask=IS_MULTI，infomask2=KEYS_UPDATED
	tup := ParseTuple(buildMultiTuple(9001, HEAP_XMAX_IS_MULTI, HEAP_KEYS_UPDATED), 16)
	if tup == nil {
		t.Fatal("ParseTuple 失败")
	}
	if deleted, certain := tup.IsDeletedCLog(nil); !deleted || !certain {
		t.Fatalf("multixact 含 updater 应判已删且确定，got deleted=%v certain=%v", deleted, certain)
	}
	// NoKeyUpdate updater（审计残留 P1）：LOCK_ONLY 清、KEYS_UPDATED 未置——
	// v1.2.28 及之前判活（错误），v1.2.29 起判删
	tupNku := ParseTuple(buildMultiTuple(9001, HEAP_XMAX_IS_MULTI, HEAP_HOT_UPDATED), 16)
	if deleted, _ := tupNku.IsDeletedCLog(nil); !deleted {
		t.Fatal("含 NoKeyUpdate updater 的 multixact 应判已删（v1.2.29 修复点）")
	}
	// 纯锁 multi（KEYS_UPDATED 未置位）：PG 会置 LOCK_ONLY，顶部拦截判活
	tup2 := ParseTuple(buildMultiTuple(9001, HEAP_XMAX_IS_MULTI|HEAP_XMAX_LOCK_ONLY, HEAP_HOT_UPDATED), 16)
	if deleted, _ := tup2.IsDeletedCLog(nil); deleted {
		t.Fatal("纯锁 multixact（LOCK_ONLY 置位）不应判已删")
	}
	// 单事务普通删行（XMAX_COMMITTED）：行为不变
	tup3 := ParseTuple(buildMultiTuple(99, HEAP_XMAX_COMMITTED, 0), 16)
	if deleted, _ := tup3.IsDeletedCLog(nil); !deleted {
		t.Fatal("XMAX_COMMITTED 应判已删")
	}
	// 无 hint、xmax 非 0：不确定（主导出按活）
	tup4 := ParseTuple(buildMultiTuple(9001, 0, 0), 16)
	deleted, certain := tup4.IsDeletedCLog(nil)
	if deleted != true || certain != false {
		t.Fatalf("无 hint 应为不确定删行候选，got deleted=%v certain=%v", deleted, certain)
	}
}

// 回归护栏：普通 UPDATE 链（HOT_UPDATED 置位）不应被 KEYS_UPDATED 误伤。
// HEAP_KEYS_UPDATED 虽然也在 infomask2 里，但判定只应在 IS_MULTI 分支使用。
func TestKeysUpdatedNonMultiUnchanged(t *testing.T) {
	// xmax=0（无删除）+ HOT_UPDATED（普通更新链旧版本）
	tup := ParseTuple(buildMultiTuple(0, 0, HEAP_HOT_UPDATED|HEAP_KEYS_UPDATED), 16)
	if deleted, _ := tup.IsDeletedCLog(nil); deleted {
		t.Fatal("xmax=0 的更新链版本不应判已删")
	}
}
