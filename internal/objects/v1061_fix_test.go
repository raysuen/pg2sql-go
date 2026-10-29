package objects

// v1.0.61 第三批回归（P2-10）：pg_authid 布局归一化与 hasoids 行的 NULL 位图对齐。
//
// hasoids 目录（PG<12 / 金仓 V8）的行数据以 oid(4B) 开头，但 NULL 位图只覆盖
// tupdesc 列（不含系统 oid）。旧实现要么整行错位 4B（固定布局不含 oid 槽），
// 要么把 rolname 读成 oid 的一部分。这里用合成行钉死正确形状。

import (
	"encoding/binary"
	"testing"

	"pg2sql/internal/datatype"
)

// TestV1061NormalizeOidFirst —— 布局归一化：补 / 保持首个 4B oid 槽。
func TestV1061NormalizeOidFirst(t *testing.T) {
	// hasoids 场景：pg_attribute 探测结果从 rolname(64B) 开始
	raw := []datatype.ColLen{{AttLen: 64, IsVarlena: false, AttAlign: "c"}}
	got := normalizeOidFirst(raw)
	if len(got) != 2 || got[0].AttLen != 4 {
		t.Fatalf("未补 oid 槽: %+v", got)
	}
	// PG12+ 场景：首列已是 4B oid，不得重复添加
	pg12 := []datatype.ColLen{{AttLen: 4}, {AttLen: 64}}
	if got := normalizeOidFirst(pg12); len(got) != 2 {
		t.Fatalf("重复添加 oid 槽: %+v", got)
	}
	if got := normalizeOidFirst(nil); got != nil {
		t.Errorf("空布局应原样返回: %+v", got)
	}
}

// TestV1061AuthidOidFirstRow —— 归一化布局 + nulls 前置后，rolname 必须正确落在
// oid 之后的第 4 字节处，且各属性位对齐。
func TestV1061AuthidOidFirstRow(t *testing.T) {
	layout := fixedAuthidLayout()
	if len(layout) != 12 || layout[0].AttLen != 4 || layout[1].AttLen != 64 {
		t.Fatalf("固定布局形状不符: %+v", layout)
	}
	thoff := 24
	raw := make([]byte, thoff)
	// t_hoff@22
	raw[22] = byte(thoff)
	body := make([]byte, 0, 128)
	var oidB [4]byte
	binary.LittleEndian.PutUint32(oidB[:], 16390) // 用户角色（>=16384）
	body = append(body, oidB[:]...)               // 系统 oid（hasoids 行首）
	name := make([]byte, 64)
	copy(name, "app_user")
	body = append(body, name...) // rolname(64)
	// 7 个 bool：super=false inherit=true createrole=false createdb=false canlogin=true repl=false bypass=false
	body = append(body, 0, 1, 0, 0, 1, 0, 0)
	body = append(body, 0) // 对齐填充：4+64+7=75 → rolconnlimit 需 4B 对齐到 76
	var cl [4]byte
	binary.LittleEndian.PutUint32(cl[:], 10) // rolconnlimit=10
	body = append(body, cl[:]...)
	raw = append(raw, body...)

	// hasoids：位图不含 oid → 前置一位 false
	nulls := make([]bool, 0, len(layout))
	nulls = append(nulls, false)
	for i := 1; i < len(layout); i++ {
		nulls = append(nulls, false)
	}
	fields := datatype.ExtractFieldsDirect(raw, thoff, nulls, layout)
	if len(fields) < 10 {
		t.Fatalf("字段数不足: %d", len(fields))
	}
	if fields[0] == nil || datatype.U32(*fields[0], 0) != 16390 {
		t.Fatalf("oid 解析错误: %v", fields[0])
	}
	if fields[1] == nil {
		t.Fatal("rolname 为 nil")
	}
	if nm := datatype.CString(*fields[1], 0); nm != "app_user" {
		t.Fatalf("rolname = %q，want %q（错位 4B 时这里会是 oid 的尾部字节）", nm, "app_user")
	}
	bit := func(i int) bool {
		return fields[i] != nil && (*fields[i])[0] != 0
	}
	if bit(2) || !bit(3) || bit(4) || bit(5) || !bit(6) || bit(7) || bit(8) {
		t.Errorf("属性位错位: super=%v inherit=%v createrole=%v createdb=%v canlogin=%v repl=%v bypass=%v",
			bit(2), bit(3), bit(4), bit(5), bit(6), bit(7), bit(8))
	}
	if fields[9] == nil || int32(datatype.U32(*fields[9], 0)) != 10 {
		t.Errorf("rolconnlimit 解析错误: %v", fields[9])
	}
}
