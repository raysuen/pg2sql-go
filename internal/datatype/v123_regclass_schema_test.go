package datatype

import (
	"encoding/binary"
	"testing"
)

// v1.2.3 回归用例（BUG-14）：regclass 导出丢 schema 前缀导致跨库回灌静默错指。
//
// 缺陷：decodeRegclass 只输出 relname，buildRegclassNameMaps 也只存 oid→relname。
// 实机复现（PG17）：public.dup_t 与 sc2.dup_t 同名并存，
//   public.dup_t 中某行 rc 原指 sc2.dup_t(oid 45830)
//   → 旧实现导出 "dup_t"，回灌后解析到 public.dup_t(oid 45823)，same=false，无任何报错
//
// 修复：同时采集 oid→relnamespace 与 namespace oid→nspname，
// 表所在 schema 与 DefaultRegNamespace（离线导出取 "public"）不同时输出 "nspname.relname"。

// setRegClassMaps 设置 regclass 反查表并返回清理函数。
func setRegClassMaps(names map[uint32]string, nspOf map[uint32]uint32, nspNames map[uint32]string) func() {
	oldN, oldS, oldSpace := regClassNames, regClassNamespaces, regNamespaceNames
	regClassNames, regClassNamespaces, regNamespaceNames = names, nspOf, nspNames
	return func() {
		regClassNames, regClassNamespaces, regNamespaceNames = oldN, oldS, oldSpace
	}
}

func regclassBytes(oid uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, oid)
	return b
}

// TestB14SameSchemaOmitsPrefix：与 DefaultRegNamespace 同 schema 的表，仍输出裸名（与 PG 一致）。
func TestB14SameSchemaOmitsPrefix(t *testing.T) {
	// public(2200) 下的 dup_t
	defer setRegClassMaps(
		map[uint32]string{45823: "dup_t"},
		map[uint32]uint32{45823: 2200},
		map[uint32]string{2200: "public", 2201: "sc2"},
	)()

	if got := decodeRegclass(regclassBytes(45823)); got != "dup_t" {
		t.Fatalf("public 下的表应省略 schema 前缀: got %q want %q", got, "dup_t")
	}
}

// TestB14CrossSchemaAddsPrefix：非 DefaultRegNamespace 的表必须带 schema 前缀。
func TestB14CrossSchemaAddsPrefix(t *testing.T) {
	defer setRegClassMaps(
		map[uint32]string{45823: "dup_t", 45830: "dup_t"},
		map[uint32]uint32{45823: 2200, 45830: 2201},
		map[uint32]string{2200: "public", 2201: "sc2"},
	)()

	// BUG-14 现场：sc2.dup_t(oid 45830) 旧实现输出 "dup_t" → 回灌错指
	if got := decodeRegclass(regclassBytes(45830)); got != "sc2.dup_t" {
		t.Fatalf("跨 schema 表必须带前缀: got %q want %q", got, "sc2.dup_t")
	}
	// 同批的 public 表不受影响
	if got := decodeRegclass(regclassBytes(45823)); got != "dup_t" {
		t.Fatalf("public 表不应被误加前缀: got %q", got)
	}
}

// TestB14NoNamespaceInfoFallsBack：无 namespace 信息时退化为裸名（保持旧行为，不 panic）。
func TestB14NoNamespaceInfoFallsBack(t *testing.T) {
	defer setRegClassMaps(
		map[uint32]string{45830: "dup_t"},
		nil, // 扫描失败场景
		nil,
	)()

	if got := decodeRegclass(regclassBytes(45830)); got != "dup_t" {
		t.Fatalf("无 namespace 信息应退化为裸名: got %q", got)
	}
}

// TestB14QuotedIdentInSchema：含特殊字符的 schema / relname 需双引号包裹。
func TestB14QuotedIdentInSchema(t *testing.T) {
	defer setRegClassMaps(
		map[uint32]string{90001: "My Table"},
		map[uint32]uint32{90001: 90010},
		map[uint32]string{90010: "My Schema"},
	)()

	got := decodeRegclass(regclassBytes(90001))
	want := `"My Schema"."My Table"`
	if got != want {
		t.Fatalf("特殊标识符需双引号: got %q want %q", got, want)
	}
}

// TestB14EmptyDefaultNamespaceIsConservative：DefaultRegNamespace 置空时一律完整限定，
// 保证绝不丢信息（宁可多写前缀，也不能静默错指）。
func TestB14EmptyDefaultNamespaceIsConservative(t *testing.T) {
	oldDefault := DefaultRegNamespace
	DefaultRegNamespace = ""
	t.Cleanup(func() { DefaultRegNamespace = oldDefault })

	defer setRegClassMaps(
		map[uint32]string{45823: "dup_t"},
		map[uint32]uint32{45823: 2200},
		map[uint32]string{2200: "public"},
	)()

	if got := decodeRegclass(regclassBytes(45823)); got != "public.dup_t" {
		t.Fatalf("无 search_path 上下文时应完整限定: got %q want %q", got, "public.dup_t")
	}
}

// TestB14InvalidOidStillDash：InvalidOid 仍输出 "-"（PG reg*out 约定，不被本修复影响）。
func TestB14InvalidOidStillDash(t *testing.T) {
	if got := decodeRegclass(regclassBytes(0)); got != "-" {
		t.Fatalf("InvalidOid 应输出 '-': got %q", got)
	}
}
