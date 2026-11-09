// v1.2.11 回归用例（BUG-1）：--batch-rows N>1 与审计模式的批组打断。
//
// 缺陷：ToSQLBatch 的 emit 只认 "-- DELETED" 前缀，v1.2.2 新增的
// "-- SUSPECTED_DELETED"（hint 缺失的疑似删除行）未纳入白名单 →
// delMark 返回的【含换行两行字符串】被当作 live 元组 append 进 pending，
// flush 后用 ", " 拼进 VALUES 列表，产出
//
//	INSERT ... VALUES -- SUSPECTED_DELETED ctid=(0,1)
//	INSERT ... VALUES (2,'b');, -- SUSPECTED_DELETED ctid=(0,2)
//	INSERT ... VALUES (3,'c');;
//
// 整文件无法被 psql 加载且 exit 0、零告警。
package engine

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mkAuditTuple：构造 24B 头 + (int4, text) 数据的元组。
func mkAuditTuple(xmin, xmax uint32, infomask uint16, id int32, v string) []byte {
	data := make([]byte, 6)
	binary.LittleEndian.PutUint32(data[0:], uint32(id))
	data[4] = byte(len(v)+1)<<1 | 1 // 1B varlena 头
	copy(data[5:], v)
	t := make([]byte, 24)
	binary.LittleEndian.PutUint32(t[0:], xmin)
	binary.LittleEndian.PutUint32(t[4:], xmax)
	binary.LittleEndian.PutUint16(t[18:], 2) // natts
	binary.LittleEndian.PutUint16(t[20:], infomask)
	t[22] = 24
	return append(t, data...)
}

// buildAuditPage：把元组按 PG 页布局（ItemId 从 pd_lower 起、元组从页尾倒排）落成一页。
func buildAuditPage(tuples [][]byte) []byte {
	const ps = 8192
	pg := make([]byte, ps)
	binary.LittleEndian.PutUint16(pg[12:], uint16(24+4*len(tuples))) // pd_lower
	binary.LittleEndian.PutUint16(pg[16:], ps)                       // pd_special
	binary.LittleEndian.PutUint16(pg[18:], uint16(ps)|4)             // pd_pagesize_version
	off := ps
	for i, tp := range tuples {
		off -= len(tp)
		copy(pg[off:], tp)
		raw := uint32(off) | 1<<15 | uint32(len(tp))<<17
		binary.LittleEndian.PutUint16(pg[24+4*i:], uint16(raw))
		binary.LittleEndian.PutUint16(pg[24+4*i+2:], uint16(raw>>16))
	}
	binary.LittleEndian.PutUint16(pg[14:], uint16(off)) // pd_upper
	return pg
}

// collectBatch：跑一遍 ToSQLBatch，返回全部输出元素。
func collectBatch(t *testing.T, page []byte, batchN int, includeDel, onlyDel bool) []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "12345")
	if err := os.WriteFile(path, page, 0644); err != nil {
		t.Fatal(err)
	}
	ri := &RowIter{
		TM: mkTestMeta(), PageSize: 8192, PGVersion: 17, Path: path,
		IncludeDel: includeDel, OnlyDeleted: onlyDel, Parallel: 1,
	}
	var out []string
	for stmt := range ToSQLBatch(ri, true, nil, batchN, nil) {
		out = append(out, stmt)
	}
	return out
}

// assertNoCommentInValues：批组里绝不能出现"注释被当作值"的形态。
func assertNoCommentInValues(t *testing.T, out []string) {
	t.Helper()
	for i, s := range out {
		if strings.Contains(s, "VALUES -- ") || strings.Contains(s, "VALUES (, ") {
			t.Errorf("batch[%d] 注释行被塞进 VALUES 列表：%q", i, s)
		}
		if strings.Contains(s, ";,") || strings.Contains(s, ";;") {
			t.Errorf("batch[%d] 语句终止符落在批内部（批组未被打断）：%q", i, s)
		}
	}
}

// TestBatchSuspectedDeletedBreaksBatch：核心回归——疑似删除行必须独立成条。
func TestBatchSuspectedDeletedBreaksBatch(t *testing.T) {
	page := buildAuditPage([][]byte{
		mkAuditTuple(100, 0, HEAP_XMIN_COMMITTED, 1, "a"),   // 活行
		mkAuditTuple(100, 200, HEAP_XMIN_COMMITTED, 2, "b"), // xmax 非零、无 xmax hint → 疑似删除
		mkAuditTuple(100, 201, HEAP_XMIN_COMMITTED, 3, "c"), // 疑似删除
	})
	out := collectBatch(t, page, 5, true, true)
	assertNoCommentInValues(t, out)

	// --only-deleted 只输出 2 条疑似删除行（活行被过滤），各自独立成条。
	if len(out) != 2 {
		t.Fatalf("期望 2 个输出元素（2 条疑似删除行各自独立），实际 %d 个：%q", len(out), out)
	}
	for i, s := range out {
		if !strings.HasPrefix(s, "-- SUSPECTED_DELETED ctid=") {
			t.Errorf("out[%d] 未以疑似删除标记开头：%q", i, s)
		}
		// 标记之后的第二条语句必须是完整单条 INSERT（以 ";" 结尾）。
		body := s[strings.Index(s, "\n")+1:]
		if !strings.HasPrefix(body, `INSERT INTO "t"."t"`) || !strings.HasSuffix(body, ");") {
			t.Errorf("out[%d] 标记后不是完整单条 INSERT：%q", i, body)
		}
		if strings.Contains(body, "\n") {
			t.Errorf("out[%d] 单条 INSERT 内出现换行：%q", i, body)
		}
	}
}

// TestBatchConfirmedDeletedBreaksBatch：确定删除行（有 xmax hint）同样打断批组，
// 且与疑似删除行同一批内交错时不粘连。
func TestBatchConfirmedDeletedBreaksBatch(t *testing.T) {
	page := buildAuditPage([][]byte{
		mkAuditTuple(100, 0, HEAP_XMIN_COMMITTED, 1, "a"),                       // 活
		mkAuditTuple(100, 200, HEAP_XMIN_COMMITTED|HEAP_XMAX_COMMITTED, 2, "b"), // 确定删除
		mkAuditTuple(100, 201, HEAP_XMIN_COMMITTED, 3, "c"),                     // 疑似删除
	})
	out := collectBatch(t, page, 5, true, true)
	assertNoCommentInValues(t, out)
	if len(out) != 2 {
		t.Fatalf("期望 2 个输出元素（--only-deleted 过滤活行），实际 %d 个：%q", len(out), out)
	}
	if !strings.HasPrefix(out[0], "-- DELETED ctid=") {
		t.Errorf("确定删除行标记错误：%q", out[0])
	}
	if !strings.HasPrefix(out[1], "-- SUSPECTED_DELETED ctid=") {
		t.Errorf("疑似删除行标记错误：%q", out[1])
	}
}

// TestBatchDeletedModeFlushesBeforeMark：--deleted（不含 only）时活行先正常攒批，
// 遇到删除标记必须先 flush 已攒的活行，再单独输出标记行——顺序不能倒。
func TestBatchDeletedModeFlushesBeforeMark(t *testing.T) {
	page := buildAuditPage([][]byte{
		mkAuditTuple(100, 0, HEAP_XMIN_COMMITTED, 1, "a"),   // 活
		mkAuditTuple(100, 0, HEAP_XMIN_COMMITTED, 2, "b"),   // 活
		mkAuditTuple(100, 200, HEAP_XMIN_COMMITTED, 3, "c"), // 疑似删除
	})
	out := collectBatch(t, page, 5, true, false)
	assertNoCommentInValues(t, out)
	if len(out) != 2 {
		t.Fatalf("期望 2 个输出元素（1 条批 + 1 条标记行），实际 %d 个：%q", len(out), out)
	}
	if strings.Contains(out[0], "-- ") {
		t.Errorf("out[0] 应为纯活行批语句，不应含注释：%q", out[0])
	}
	if strings.Count(out[0], "), (") != 1 {
		t.Errorf("out[0] 应合并 2 条活行元组：%q", out[0])
	}
	if !strings.HasPrefix(out[1], "-- SUSPECTED_DELETED ctid=") {
		t.Errorf("out[1] 应为标记行：%q", out[1])
	}
}

// TestBatchLiveRowsStillBatch：无审计标记时批组合并行为保持不变（防回归）。
func TestBatchLiveRowsStillBatch(t *testing.T) {
	page := buildAuditPage([][]byte{
		mkAuditTuple(100, 0, HEAP_XMIN_COMMITTED, 1, "a"),
		mkAuditTuple(100, 0, HEAP_XMIN_COMMITTED, 2, "b"),
		mkAuditTuple(100, 0, HEAP_XMIN_COMMITTED, 3, "c"),
	})
	out := collectBatch(t, page, 5, false, false)
	if len(out) != 1 {
		t.Fatalf("3 行 batchN=5 应合并为 1 条，实际 %d 条：%q", len(out), out)
	}
	if strings.Count(out[0], "), (") != 2 {
		t.Errorf("多值元组未按 (…), (…), (…) 合并：%q", out[0])
	}
	if !strings.HasSuffix(out[0], ");") {
		t.Errorf("批语句未以 ); 结尾：%q", out[0])
	}
}

// TestBatchNoAuditMarksNoComment：默认模式不应产出任何注释行（前缀判定放宽后
// 也不能误伤 normal 行）。
func TestBatchNoAuditMarksNoComment(t *testing.T) {
	page := buildAuditPage([][]byte{
		mkAuditTuple(100, 0, HEAP_XMIN_COMMITTED, 1, "a"),
	})
	out := collectBatch(t, page, 5, false, false)
	for i, s := range out {
		if strings.Contains(s, "-- ") {
			t.Errorf("out[%d] 默认模式出现注释：%q", i, s)
		}
	}
}
