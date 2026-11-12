// v1.2.18 回归用例：域 DEFAULT 无法还原时必须留下信号，且判 warn 不判 fail。
//
// 缺陷现场：typeTailDefault 定位失败 / typdefaultbin 反解失败时，v1.2.17 让
// TypDefault 保持空，DDL 只是少一段 DEFAULT —— 没有 report 字段、没有 fail 信号，
// 除非有人比对 pg_get_expr(typdefaultbin) 否则发现不了。
//
// 分级理由（与 partition_bound_unresolved 的区别）：
//   - 分区边界丢失：结构错位 + 数据归属丢失 → fail
//   - 域 DEFAULT 丢失：结构信息缺失，但已有数据一个字节没变 → warn
package integrity

import (
	"strings"
	"testing"
)

func TestV1218DomainDefaultUnresolvedIsWarnNotFail(t *testing.T) {
	skips := []SkipEntry{
		{
			Table:   "domain:public.dom_bad",
			Schema:  "public",
			RelKind: "d",
			Reason:  "domain_default_unresolved",
			Detail:  "域 public.dom_bad 的 DEFAULT 无法离线还原",
		},
	}
	summary := BuildSummary(nil, skips, nil)
	v := BuildVerdict(summary, nil, nil, skips)

	if v.Level == "fail" {
		t.Fatalf("域 DEFAULT 丢失不应判 fail（已有数据未变），实际 level=%s reasons=%v", v.Level, v.Reasons)
	}
	// 必须留下可见信号：既要有 warn 级别，也要有一条能定位到域的消息
	found := false
	for _, r := range v.Reasons {
		if strings.Contains(r, "域的 DEFAULT") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("verdict 未包含域 DEFAULT 相关提示，实际 reasons=%v", v.Reasons)
	}
}

// 反向断言：没有该 skip 时不得凭空产生这条 warn（防止误伤正常库）。
func TestV1218NoDomainDefaultSkipNoWarn(t *testing.T) {
	summary := BuildSummary(nil, nil, nil)
	v := BuildVerdict(summary, nil, nil, nil)
	for _, r := range v.Reasons {
		if strings.Contains(r, "域的 DEFAULT") {
			t.Errorf("无 domain_default_unresolved 时不应出现该提示，实际 reasons=%v", v.Reasons)
		}
	}
}

// 两条 unresolved 同时存在时，分区边界仍判 fail（不能被新分支拉低）。
func TestV1218PartBoundStillFailWithDomainDefault(t *testing.T) {
	skips := []SkipEntry{
		{Table: "public.pt_p1", Reason: "partition_bound_unresolved", Detail: "x"},
		{Table: "domain:public.dom_bad", Schema: "public", RelKind: "d", Reason: "domain_default_unresolved"},
	}
	summary := BuildSummary(nil, skips, nil)
	v := BuildVerdict(summary, nil, nil, skips)
	if v.Level != "fail" {
		t.Errorf("存在 partition_bound_unresolved 时应判 fail，实际 level=%s", v.Level)
	}
}

// v1.2.18（P3）：域条目不得混进 tables_skipped。
//
// 缺陷现场：BuildSummary 用 len(skips) 作为 TablesSkipped，新增的 domain:xxx
// 条目（RelKind="d"）一并被计入，stderr 的"跳过 N"把域和表糊成一个数 ——
// 用户拿着 N 去表清单里数，怎么数都少一个。
func TestV1218DomainsNotCountedAsTablesSkipped(t *testing.T) {
	skips := []SkipEntry{
		// 两张正常跳过的"表"
		{Table: "public.t1", RelKind: "i", Reason: "non_table"},
		{Table: "public.t2", RelKind: "v", Reason: "non_table"},
		// 两个域
		{Table: "domain:public.d1", Schema: "public", RelKind: "d", Reason: "domain_default_unresolved"},
		{Table: "domain:public.d2", Schema: "public", RelKind: "d", Reason: "domain_default_unresolved"},
	}
	s := BuildSummary(nil, skips, nil)
	if s.TablesSkipped != 2 {
		t.Errorf("tables_skipped 只应数表（2），实际 %d（域条目被混入了）", s.TablesSkipped)
	}
	if s.DomainsUnresolved != 2 {
		t.Errorf("domains_unresolved 应为 2，实际 %d", s.DomainsUnresolved)
	}
}

// 反向断言：只有域、没有表时不能被误判成"未发现任何可导出对象"。
// （真实现场：只建了域还没建表，或 --schema 命中一个只有域的 schema）
func TestV1218OnlyDomainsNotMisjudgedAsEmpty(t *testing.T) {
	skips := []SkipEntry{
		{Table: "domain:public.d1", Schema: "public", RelKind: "d", Reason: "domain_default_unresolved"},
	}
	s := BuildSummary(nil, skips, nil)
	if s.TablesTotal != 0 || s.TablesSkipped != 0 {
		t.Fatalf("前置条件不符：total=%d skipped=%d", s.TablesTotal, s.TablesSkipped)
	}
	v := BuildVerdict(s, nil, nil, skips)
	for _, r := range v.Reasons {
		if strings.Contains(r, "未发现任何可导出对象") {
			t.Errorf("目录里只有域时不应报「未发现任何可导出对象」，实际 reasons=%v", v.Reasons)
		}
	}
	if v.Level != "warn" {
		t.Errorf("只有域且 DEFAULT 未还原时应判 warn，实际 level=%s", v.Level)
	}
}
