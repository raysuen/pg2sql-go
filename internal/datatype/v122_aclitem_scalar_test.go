package datatype

import (
	"encoding/binary"
	"testing"
)

// v1.2.2 回归用例（BUG-13）：aclitem 标量列导出退化为 \x 十六进制。
//
// 缺陷：decodeAclitem 早已实现（B12 还按引擎区分位 12 字符 'p'/'s'），
// 且通过 arrayElemText 的 "aclitem" 分支覆盖了 aclitem[] 数组路径；
// 但 Decoders 分派表里**从未注册** ACLITEMOID（1033）条目。
// 结果：CREATE TABLE t(a aclitem, al aclitem[]) 中
//   - al（aclitem[]，oid 1034）→ 走 DecodeArray → arrayElemText "aclitem" → 正常
//   - a （aclitem 标量，oid 1033）→ Decoders 无条目 → DecodeDefault → \x 十六进制
// 实机（V8R6C9B14 / V9R1C10）复现：
//   1   \x0a0000000a0000007f100000   {system=arwdDxtp/system,acl_probe=ar/system}
// 十六进制值无法回灌 aclitem_in（报 invalid input syntax for type aclitem）。
//
// 本用例在 DecodeValue 层面（真实导出路径）断言，而非只测 decodeAclitem 本身——
// 后者一直是对的，所以老用例全绿却掩盖了缺陷。

// TestB13AclitemScalarNotHex：aclitem 标量必须输出文本形式，不能是 \x 十六进制。
func TestB13AclitemScalarNotHex(t *testing.T) {
	old := roleMapSnapshot()
	SetRoleMap(map[uint32]string{10: "system", 42500: "acl_probe"})
	t.Cleanup(func() { SetRoleMap(old) })

	oldKb := KbEngine.Load()
	KbEngine.Store(true)
	t.Cleanup(func() { KbEngine.Store(oldKb) })

	// 金仓 owner 默认 ACL：bits 0-6 + bit12('p') = 0x7F|1<<12
	raw := aclitemBytes(10, 10, 0x7F|1<<12)
	got := DecodeValue(ACLITEMOID, raw)
	want := "system=arwdDxtp/system"
	if got != want {
		t.Fatalf("aclitem 标量解码错误（疑 Decoders 缺 ACLITEMOID 条目）: got %q want %q", got, want)
	}
	if len(got) > 0 && got[0] == '\\' {
		t.Fatalf("aclitem 标量仍导出为十六进制转义，不可回灌: %q", got)
	}
}

// TestB13AclitemScalarPG：PG 引擎同位图输出 's'，且同样走文本路径。
func TestB13AclitemScalarPG(t *testing.T) {
	old := roleMapSnapshot()
	SetRoleMap(map[uint32]string{10: "postgres"})
	t.Cleanup(func() { SetRoleMap(old) })

	oldKb := KbEngine.Load()
	KbEngine.Store(false)
	t.Cleanup(func() { KbEngine.Store(oldKb) })

	raw := aclitemBytes(10, 10, 0x7F|1<<12)
	got := DecodeValue(ACLITEMOID, raw)
	want := "postgres=arwdDxts/postgres"
	if got != want {
		t.Fatalf("PG aclitem 标量解码错误: got %q want %q", got, want)
	}
}

// TestB13AclitemScalarVsArrayConsistent：同一 aclitem 值，标量列与数组元素
// 必须渲染一致（两者本就同一 decodeAclitem，此前因分派缺失而不一致）。
func TestB13AclitemScalarVsArrayConsistent(t *testing.T) {
	old := roleMapSnapshot()
	SetRoleMap(map[uint32]string{10: "system", 42500: "acl_probe"})
	t.Cleanup(func() { SetRoleMap(old) })

	oldKb := KbEngine.Load()
	KbEngine.Store(true)
	t.Cleanup(func() { KbEngine.Store(oldKb) })

	owner := aclitemBytes(10, 10, 0x7F|1<<12)
	scalar := DecodeValue(ACLITEMOID, owner)
	elem := decodeAclitem(owner)
	if scalar == "" || elem == "" {
		t.Fatalf("空解码结果: scalar=%q elem=%q", scalar, elem)
	}
	if aclitemText(scalar) != aclitemText(elem) {
		t.Fatalf("标量与数组元素渲染不一致: scalar=%q elem=%q", scalar, elem)
	}
}

// aclitemText 去掉可能存在的 varlena 头转义差异，仅用于比较可读文本。
func aclitemText(s string) string {
	for len(s) > 1 && s[0] == '\\' {
		s = s[2:]
	}
	return s
}

// TestB13AclitemScalarGrantOption：WITH GRANT OPTION 的高 16 位同样要走文本路径。
func TestB13AclitemScalarGrantOption(t *testing.T) {
	old := roleMapSnapshot()
	SetRoleMap(map[uint32]string{10: "system", 42500: "alice"})
	t.Cleanup(func() { SetRoleMap(old) })

	oldKb := KbEngine.Load()
	KbEngine.Store(false)
	t.Cleanup(func() { KbEngine.Store(oldKb) })

	// grantee=42500, grantor=10, privs=r|w, goptions=r|w → "alice=r*w*/system"
	raw := aclitemBytes16(42500, 10, 0x06, 0x06)
	got := DecodeValue(ACLITEMOID, raw)
	if got == "" {
		t.Fatal("grant option aclitem 解码为空")
	}
	for i := 0; i < len(got); i++ {
		if got[i] == '\\' {
			t.Fatalf("grant option aclitem 仍为十六进制: %q", got)
		}
	}
	// 位序：r* w* 交错输出
	if got != "alice=r*w*/system" {
		t.Fatalf("grant option 渲染错误: got %q", got)
	}
	_ = binary.LittleEndian
}
