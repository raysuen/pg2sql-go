package catalog

import (
	"pg2sql/internal/engine"
	"testing"
)

// B5 验证：ClassFields 读取 relnatts 的字段索引
// PG18 真实 pg_class：...relkind(1B) 在 attnum18、relnatts(2B) 在 attnum19
// 工具 v18 布局 WithOID(pgClassCols18WithNatts)：[17]=relkind, [18]=relnatts
// L652-658 却读 fields[17]（relkind，1 字节）→ len<2 → natts=0
func TestB5ClassFieldsNattsPG18(t *testing.T) {
	pg18 := "/tmp/pg18data/base/5/1259" // postgres 库 pg_class 堆文件
	nonZero := 0
	checked := 0
	var pgClassRow *ClassRow
	for tup := range engine.IterSysTuples(pg18, 18, false) {
		cr := ClassFields(tup, 18)
		if cr == nil || cr.RelName == "" {
			continue
		}
		checked++
		if cr.RelNatts > 0 {
			nonZero++
		}
		if cr.RelName == "pg_class" {
			pgClassRow = cr
		}
		// 抽样打印：relkind 正确与否 + natts
		if cr.RelName == "pg_class" || cr.RelName == "pg_type" || cr.RelName == "information_schema._pg_foreign_data_wrappers" {
			t.Logf("PG18(v18布局) %-36s kind=%q natts=%d toast=%d shared=%v",
				cr.RelName, cr.RelKind, cr.RelNatts, cr.ToastRelID, cr.RelShared)
		}
	}
	t.Logf("共解析 %d 行；natts>0 的行数=%d（B5 预期=0）", checked, nonZero)
	if pgClassRow != nil {
		t.Logf("pg_class 自身行：工具读 relnatts=%d，真实=34（PG18 psql 实证）→ %s",
			pgClassRow.RelNatts, map[bool]string{true: "BUG 复现", false: "未复现"}[pgClassRow.RelNatts != 34])
	}
	// B5 断言：natts 恒 0
	if nonZero > 0 {
		t.Logf("注意：有 %d 行 natts>0，B5 未必全量触发", nonZero)
	}
}

// 对照组：同一文件用 v16 布局（工具对金仓 V8/V9 的路径——SYS_VERSION=12 → 走 v12 分支）
// 金仓 V9 真实 pg_class 前 18 字段与 PG16 相同（relflags 插在 relrewrite 之后），故 v12 布局读取正确
func TestB5KBV9Control(t *testing.T) {
	kb9 := "/home/user/Doubao/chats/38448089095814658/work/kb9_pg/base/12745/1259" // V9 test 库 pg_class 文件?
	// 金仓 test 库的 pg_class 文件在 base/12745/1259（relfilenode=oid 场景）
	for tup := range engine.IterSysTuples(kb9, 12, true) {
		cr := ClassFields(tup, 12)
		if cr == nil || cr.RelName == "" {
			continue
		}
		if cr.RelName == "pg_class" || cr.RelName == "_rel" || cr.RelName == "_att" {
			t.Logf("KB V9(v12布局) %-28s kind=%q natts=%d toast=%d", cr.RelName, cr.RelKind, cr.RelNatts, cr.ToastRelID)
		}
	}
}
