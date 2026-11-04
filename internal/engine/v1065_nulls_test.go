// v1.0.65 修复回归用例（engine 侧）：
// F12 HeapTuple.GetNulls 的 t_bits 语义必须全版本一致（置位 = 非空）
package engine

import "testing"

func TestV1065GetNullsBitmapSemantics(t *testing.T) {
	// t_bits 3 位：bit0=1(非空) bit1=0(NULL) bit2=1(非空)
	raw := make([]byte, HEAP_TUPLE_HEADER_SIZE+1)
	raw[HEAP_TUPLE_HEADER_SIZE] = 0x05

	want := []bool{false, true, false} // false=非空, true=NULL
	for _, ver := range []int{11, 12, 15, 17, 18} {
		tup := &HeapTuple{Raw: raw, NAttrs: 3, TInfomask: HEAP_HASNULL, PgVersion: ver}
		got := tup.GetNulls()
		if len(got) != 3 {
			t.Fatalf("PG%d 返回长度 %d", ver, len(got))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("PG%d 第 %d 列 NULL 判定错误: got=%v want=%v（旧实现 PG<12 取反）", ver, i, got[i], want[i])
			}
		}
	}
}

func TestV1065GetNullsNoFlagAllNonNull(t *testing.T) {
	// 无 HEAP_HASNULL 标志 → 全非空，且不读取 t_bits
	tup := &HeapTuple{Raw: make([]byte, HEAP_TUPLE_HEADER_SIZE), NAttrs: 2, PgVersion: 9}
	got := tup.GetNulls()
	for i, v := range got {
		if v {
			t.Fatalf("无 NULL 位图时第 %d 列不应为 NULL", i)
		}
	}
}
