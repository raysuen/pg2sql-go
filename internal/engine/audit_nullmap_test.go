package engine

import (
	"encoding/binary"
	"testing"

	"pg2sql/internal/datatype"
)

// 审计复现：ParseNamedTuple 的 NULL 位图索引用 cols 过滤后的下标 i，
// 而 GetNulls 的下标语义是 attnum-1。PG12+ 目录元组首列是已 DROP 的 oid
// （pg_attribute 里有 attisdropped 行，NamedTableLayout 已把它过滤掉），
// 因此从第一个真实列开始 NULL 判定整体错位一列。
//
// 构造：attnum1=oid(物理 4B，位图位=非空)，attnum2=8B 定长非空，
// attnum3=定长 NULL，attnum4=varlena 非空。
// 期望：att2 读到 payload、att3 为 NULL、att4 读到 varlena。
func TestAuditNamedTupleNullBitmapMisindex(t *testing.T) {
	// v1.0.59 已修复（P1-1）：位图改按 item.AttNum-1 索引，回归验证
	// 头布局：xmin(0-3) xmax(4-7) cid(8-11) ctid(12-17) infomask2(18-19)
	//        infomask(20-21) t_hoff(22) t_bits(23...)，HASNULL 时 t_hoff=MAXALIGN(23+1)=24
	thoff := 24
	raw := make([]byte, thoff)
	raw[18] = 0x04 // t_infomask2: natts=4（低 11 位）
	raw[20] = 0x01 // t_infomask = HEAP_HASNULL
	raw[22] = byte(thoff)
	raw[23] = 0b1011 // t_bits：bit0..3 = attnum1(oid)=1, attnum2=1, attnum3=0(NULL), attnum4=1

	body := []byte{}
	var oidB [4]byte
	binary.LittleEndian.PutUint32(oidB[:], 16385) // attnum1 物理空间（已 DROP 的 oid）
	body = append(body, oidB[:]...)
	body = append(body, []byte("abcdefgh")...)               // attnum2 定长 8B
	body = append(body, []byte{0x0B, 'Z', 'Z', 'Z', 'Z'}...) // attnum4: 1B varlena 头(total=5) + "ZZZZ"
	// 注意：1B 头首字节为奇数（(total<<1)|1，total=5 → 0x0B）。真实数据实证
	//（金仓 pg_proc.prosrc 0x11+"lsegin" → total=8），偶数首字节是 4B 头。
	raw = append(raw, body...)

	tup := &HeapTuple{Raw: raw, THoff: thoff, NAttrs: 4, TInfomask: 0x0001, PgVersion: 17}
	nulls := tup.GetNulls()
	if nulls[2] != true {
		t.Fatalf("前置条件失败：nulls[2]（attnum3）应为 true，GetNulls=%v", nulls)
	}
	cols := []NamedCol{
		{Name: "att2", AttNum: 2, ColLen: datatype.ColLen{AttLen: 8, IsVarlena: false, AttAlign: "c"}},
		{Name: "att3", AttNum: 3, ColLen: datatype.ColLen{AttLen: 4, IsVarlena: false, AttAlign: "i"}},
		{Name: "att4", AttNum: 4, ColLen: datatype.ColLen{AttLen: 0, IsVarlena: true, AttAlign: "i"}},
	}
	out, _ := ParseNamedTuple(tup, cols, true, true)

	if v, ok := out["att2"]; !ok || string(v) != "abcdefgh" {
		t.Errorf("att2 = %q (期望 abcdefgh)", v)
	}
	if v, ok := out["att3"]; ok {
		t.Errorf("att3 本应 NULL 却被读出 = %q（位图错位：读到 attnum2 的位）", v)
	}
	if v, ok := out["att4"]; !ok {
		t.Errorf("att4 丢失 —— ParseNamedTuple 把 attnum4 判为 NULL（位图错位：读到 attnum3 的位）")
	} else if string(v[len(v)-4:]) != "ZZZZ" {
		t.Errorf("att4 错值 = %q", v)
	}
}
