// v1.2.9 P2 测试：类型链上限（B-3）、不规则数组降级记账（B-2）、
// 降级告警文案（B-1 的可验证部分）。
// 作者: raysuen
package datatype

import (
	"strings"
	"testing"
)

// buildDomainChain：构造 n 层域链（第 1 层 over "char"），返回最外层 oid。
// oid 从 start 递增分配。
func buildDomainChain(t *testing.T, start uint32, n int) uint32 {
	t.Helper()
	base := uint32(CHAROID)
	cur := base
	for i := 0; i < n; i++ {
		oid := start + uint32(i)
		DynamicTypeDefs[oid] = &TypeDef{OID: oid, Name: "d", Nsp: 2200, Type: 'd', Base: cur}
		cur = oid
	}
	return cur
}

// B-3：9 层 / 10 层域链必须仍能识别为 char 标量并改写。
// v1.2.8 的 8 跳上限会停在仍是域的 d1 上，判为 none → 静默漏改写。
func TestV129DeepDomainChain(t *testing.T) {
	defer withCharCatalog(t)()
	takeTypeChainUnresolved()
	c3 := string([]byte{0xC3})
	for _, n := range []int{8, 9, 10} {
		top := buildDomainChain(t, uint32(80040+n*20), n)
		ResetCharContainerKindCache()
		got, ok := CharContainerExpr(top, c3)
		if !ok || got != `(-61)::"char"` {
			t.Fatalf("%d 层域链 = %q ok=%v，期望 (-61)::\"char\"", n, got, ok)
		}
	}
	if s := TypeChainUnresolvedSummary(); s != "" {
		t.Fatalf("未超限不应告警，得到 %q", s)
	}
}

// B-3：超过上限时不再静默——不改写，但必须记账并在汇总里点名 oid。
func TestV129OverLimitDomainChain(t *testing.T) {
	defer withCharCatalog(t)()
	takeTypeChainUnresolved()
	takeCharContainerDegradedCount()
	top := buildDomainChain(t, 80100, maxTypeChainHops+8)
	ResetCharContainerKindCache()
	if got, ok := CharContainerExpr(top, string([]byte{0xC3})); ok || got != "" {
		t.Fatalf("超限类型不应改写，得到 %q ok=%v", got, ok)
	}
	s := TypeChainUnresolvedSummary()
	if !strings.Contains(s, "域链") || !strings.Contains(s, "80139") {
		t.Fatalf("超限告警应点名 oid 与原因，得到 %q", s)
	}
	if s2 := TypeChainUnresolvedSummary(); s2 != "" {
		t.Fatalf("告警应自清零，二次读到 %q", s2)
	}
}

// B-3：域 over 复合 的解析上限同步提升（v1.2.8 上限 8 时 9 层会回落 \x 十六进制）。
func TestV129CompositeBaseOIDDeepChain(t *testing.T) {
	defer withCharCatalog(t)()
	takeTypeChainUnresolved()
	// 80012 已是「域 over 复合(80010)」，再套 8 层 → 共 9 层域
	cur := uint32(80012)
	for i := 0; i < 8; i++ {
		oid := uint32(80200 + i)
		DynamicTypeDefs[oid] = &TypeDef{OID: oid, Name: "dd", Nsp: 2200, Type: 'd', Base: cur}
		cur = oid
	}
	if got := compositeBaseOID(cur); got != 80010 {
		t.Fatalf("9 层域 over 复合 = %d，期望 80010", got)
	}
	// 继续加到超过上限
	for i := 0; i < maxTypeChainHops; i++ {
		oid := uint32(80300 + i)
		DynamicTypeDefs[oid] = &TypeDef{OID: oid, Name: "dd", Nsp: 2200, Type: 'd', Base: cur}
		cur = oid
	}
	if got := compositeBaseOID(cur); got != 0 {
		t.Fatalf("超限应返回 0，得到 %d", got)
	}
	if s := TypeChainUnresolvedSummary(); !strings.Contains(s, "域链") {
		t.Fatalf("超限应告警，得到 %q", s)
	}
}

// B-3：容器嵌套超限时按顶层类型记账（告警里的 oid 要能对上用户 DDL）。
func TestV129DeepContainerNesting(t *testing.T) {
	defer withCharCatalog(t)()
	takeTypeChainUnresolved()
	// 数组 ↔ 复合 交替嵌套，最内层是 "char"
	cur := uint32(CHAROID)
	relid := uint32(91000)
	next := uint32(80400)
	for i := 0; i < maxContainerDepth; i++ {
		arr := next
		next++
		DynamicTypeDefs[arr] = &TypeDef{OID: arr, Name: "arr", Nsp: 2200, Type: 'b', Elem: cur, Array: arr}
		comp := next
		next++
		DynamicTypeDefs[comp] = &TypeDef{OID: comp, Name: "comp", Nsp: 2200, Type: 'c', TypRelid: relid}
		CompositeAttrs[relid] = []*AttrRow{{AttName: "x", AttTypID: arr, AttLen: -1}}
		cur = comp
		relid++
	}
	ResetCharContainerKindCache()
	// 16 层嵌套 > 8 → 判不了，不能静默当"无 char"
	if typeHasCharContainer(cur, 1) {
		t.Fatal("超限时应返回 false（无法判定）")
	}
	s := TypeChainUnresolvedSummary()
	if !strings.Contains(s, "容器嵌套") {
		t.Fatalf("超限应告警并标明原因，得到 %q", s)
	}
	if !strings.Contains(s, "80415") {
		t.Fatalf("应点名顶层类型 oid 80415，得到 %q", s)
	}
}

// B-2：不规则数组在 SQL 侧也要计降级（v1.2.8 静默返回，与 CSV/text 侧不对称）。
func TestV129IrregularArrayDegraded(t *testing.T) {
	defer withCharCatalog(t)()
	takeCharContainerDegradedCount()
	c3 := string([]byte{0xC3})
	in := "{{" + c3 + ",A},B}" // 第一支 2 元素、第二支是叶子 → 维长不一致
	if got, ok := CharContainerExpr(1002, in); ok || got != "" {
		t.Fatalf("不规则数组不应改写，得到 %q ok=%v", got, ok)
	}
	if n := takeCharContainerDegradedCount(); n != 1 {
		t.Fatalf("不规则数组应计 1 次降级，得到 %d", n)
	}
	// 同一段文本在 CSV/text 侧本就判为风险，两侧口径一致
	if !CharContainerRisk(1002, in) {
		t.Fatal("不规则数组在 CSV/text 侧应命中风险")
	}
	// 无高危字节的不规则数组仍不改写、不计数（不产生噪音）
	takeCharContainerDegradedCount()
	if _, ok := CharContainerExpr(1002, "{{A,B},C}"); ok {
		t.Fatal("普通不规则数组不应改写")
	}
	if n := takeCharContainerDegradedCount(); n != 0 {
		t.Fatalf("无高危字节不应计降级，得到 %d", n)
	}
}

// B-1：降级文案不得再指向不存在的 --format binary，且要给出真实可行路径。
func TestV129DegradedMessage(t *testing.T) {
	takeCharHighByteCount()
	takeCharContainerDegradedCount()
	takeTypeChainUnresolved()
	RecordCharContainerDegraded()
	s := CharHighByteSummary()
	if strings.Contains(s, "--format binary") {
		t.Fatalf("文案不应再出现未实现的 --format binary: %q", s)
	}
	if !strings.Contains(s, "latin1") || !strings.Contains(s, "用户手册") {
		t.Fatalf("降级文案应给出可行处理路径并指向手册: %q", s)
	}
	// 混合场景（部分可还原 + 部分降级）也要包含处置指向
	RecordCharHighByte()
	RecordCharContainerDegraded()
	s = CharHighByteSummary()
	if strings.Contains(s, "--format binary") || !strings.Contains(s, "另有 1 个值") {
		t.Fatalf("混合场景文案异常: %q", s)
	}
}
