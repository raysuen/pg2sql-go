package app

// v1.2.10 回归用例：DDL 类型依赖闭包与拓扑序。
// 缺口（外部审计报告第十节 #1）：前置类型段只看列的直接类型，域链 ≥2 层 /
// 域 over 复合·枚举 / 复合字段引用自定义类型时，依赖（c1..c8 / comp_t / mood）
// 全缺，空库回灌 `ERROR: type "public.c8" does not exist`，CREATE TABLE 都建不出。
// 另含：域基为自定义数组时输出内部名 _comp_t 的渲染修正。

import (
	"strings"
	"testing"

	"pg2sql/internal/datatype"
	"pg2sql/internal/ddl"
	"pg2sql/internal/engine"
)

// mock ddldep：模拟 PG17 实测夹具（9 层域链 + 域 over 复合 + 域 over 枚举 + 嵌套复合）
const (
	oChar     = 18
	oCompT    = 91001 // comp_t (a "char", n int)
	oCompTArr = 91002 // _comp_t
	oC1       = 91011 // c1 domain over "char"
	oC2       = 91012
	oC3       = 91013
	oD1       = 91021 // d1 domain over comp_t
	oD2       = 91022
	oD3       = 91023
	oDArr     = 91031 // d_arr domain over comp_t[]
	oMood     = 91041 // mood enum
	oDEnum    = 91051 // d_enum domain over mood
	oDEnum2   = 91052
	oOuter    = 91061 // outer_t (d c3, m mood, sub comp_t)
)

func withDepFixture(t *testing.T) {
	t.Helper()
	oldDefs, oldAttrs, oldEnum := datatype.DynamicTypeDefs, datatype.CompositeAttrs, datatype.EnumMapSnapshot()
	oldChecks := ddl.DomainCheckSnapshot()
	datatype.DynamicTypeDefs = map[uint32]*datatype.TypeDef{
		oCompT:    {OID: oCompT, Name: "comp_t", Type: 'c', TypRelid: 91090, Array: oCompTArr},
		oCompTArr: {OID: oCompTArr, Name: "_comp_t", Type: 'b', Elem: oCompT, Array: oCompTArr},
		oC1:       {OID: oC1, Name: "c1", Type: 'd', Base: oChar},
		oC2:       {OID: oC2, Name: "c2", Type: 'd', Base: oC1},
		oC3:       {OID: oC3, Name: "c3", Type: 'd', Base: oC2},
		oD1:       {OID: oD1, Name: "d1", Type: 'd', Base: oCompT},
		oD2:       {OID: oD2, Name: "d2", Type: 'd', Base: oD1},
		oD3:       {OID: oD3, Name: "d3", Type: 'd', Base: oD2},
		oDArr:     {OID: oDArr, Name: "d_arr", Type: 'd', Base: oCompTArr},
		oMood:     {OID: oMood, Name: "mood", Type: 'e', Array: 0},
		oDEnum:    {OID: oDEnum, Name: "d_enum", Type: 'd', Base: oMood},
		oDEnum2:   {OID: oDEnum2, Name: "d_enum2", Type: 'd', Base: oDEnum},
		oOuter:    {OID: oOuter, Name: "outer_t", Type: 'c', TypRelid: 91091},
	}
	datatype.CompositeAttrs = map[uint32][]*datatype.AttrRow{
		91090: {
			{AttName: "a", AttTypID: oChar},
			{AttName: "n", AttTypID: 23},
		},
		91091: {
			{AttName: "d", AttTypID: oC3},
			{AttName: "m", AttTypID: oMood},
			{AttName: "sub", AttTypID: oCompT},
		},
	}
	datatype.SetEnumMap(map[uint32][]datatype.EnumMember{
		oMood: {
			{Label: "happy", SortOrder: 1},
			{Label: "sad", SortOrder: 2},
		},
	})
	ddl.SetDomainChecks(map[uint32][]ddl.DomainCheck{}) // 已读到目录、无 CHECK 行 → 不出注释
	t.Cleanup(func() {
		datatype.DynamicTypeDefs, datatype.CompositeAttrs = oldDefs, oldAttrs
		datatype.SetEnumMap(oldEnum)
		ddl.SetDomainChecks(oldChecks)
	})
}

func ddlOf(cols ...engine.ColumnDef) string {
	names := map[uint32]string{
		oChar: "char", 23: "int4",
		oCompT: "comp_t", oCompTArr: "_comp_t",
		oC1: "c1", oC2: "c2", oC3: "c3",
		oD1: "d1", oD2: "d2", oD3: "d3", oDArr: "d_arr",
		oMood: "mood", oDEnum: "d_enum", oDEnum2: "d_enum2", oOuter: "outer_t",
	}
	return generateDDL(&engine.TableMeta{
		Schema: "public", RelName: "t_dep", TypeNames: names, Columns: cols,
	})
}

// 依赖必须先于被依赖者输出（拓扑序）
func assertBefore(t *testing.T, ddl string, first, second string) {
	t.Helper()
	i, j := strings.Index(ddl, first), strings.Index(ddl, second)
	if i < 0 || j < 0 {
		t.Fatalf("DDL 缺少语句:\n  want1: %s\n  want2: %s\n---- 实际 ----\n%s", first, second, ddl)
	}
	if i > j {
		t.Fatalf("依赖顺序错误：%q 应先于 %q\n---- 实际 ----\n%s", first, second, ddl)
	}
}

// 域链 3 层：c1..c3 全部输出且自底向上
func TestV1210DomainChainClosure(t *testing.T) {
	withDepFixture(t)
	got := ddlOf(engine.ColumnDef{Name: "v", TypeOID: oC3})
	for _, want := range []string{
		`CREATE DOMAIN "public"."c1" AS "char"`,
		`CREATE DOMAIN "public"."c2" AS "public"."c1"`,
		`CREATE DOMAIN "public"."c3" AS "public"."c2"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("域链缺 %q:\n%s", want, got)
		}
	}
	assertBefore(t, got, `"c1" AS`, `"c2" AS`)
	assertBefore(t, got, `"c2" AS`, `"c3" AS`)
}

// 域 over 复合：comp_t 与整条域链都要输出
func TestV1210DomainOverCompositeClosure(t *testing.T) {
	withDepFixture(t)
	got := ddlOf(engine.ColumnDef{Name: "v", TypeOID: oD3})
	for _, want := range []string{
		`CREATE TYPE "public"."comp_t" AS (`,
		`CREATE DOMAIN "public"."d1" AS "public"."comp_t"`,
		`CREATE DOMAIN "public"."d2" AS "public"."d1"`,
		`CREATE DOMAIN "public"."d3" AS "public"."d2"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺 %q:\n%s", want, got)
		}
	}
	assertBefore(t, got, `CREATE TYPE "public"."comp_t"`, `CREATE DOMAIN "public"."d1"`)
	assertBefore(t, got, `"d1" AS`, `"d2" AS`)
	assertBefore(t, got, `"d2" AS`, `"d3" AS`)
}

// 域 over 枚举：CREATE TYPE ... AS ENUM 先于域
func TestV1210DomainOverEnumClosure(t *testing.T) {
	withDepFixture(t)
	got := ddlOf(engine.ColumnDef{Name: "v", TypeOID: oDEnum2})
	if !strings.Contains(got, `CREATE TYPE "public"."mood" AS ENUM ('happy', 'sad')`) {
		t.Fatalf("缺枚举 CREATE TYPE:\n%s", got)
	}
	assertBefore(t, got, `CREATE TYPE "public"."mood"`, `CREATE DOMAIN "public"."d_enum"`)
	assertBefore(t, got, `"d_enum" AS`, `"d_enum2" AS`)
}

// 复合字段引用自定义类型：字段依赖全部闭包（旧版只出 outer_t 一条）
func TestV1210CompositeFieldClosure(t *testing.T) {
	withDepFixture(t)
	got := ddlOf(engine.ColumnDef{Name: "v", TypeOID: oOuter})
	for _, want := range []string{
		`CREATE DOMAIN "public"."c1"`, `CREATE DOMAIN "public"."c2"`, `CREATE DOMAIN "public"."c3"`,
		`CREATE TYPE "public"."mood" AS ENUM`,
		`CREATE TYPE "public"."comp_t" AS (`,
		`CREATE TYPE "public"."outer_t" AS (`,
		`"d" "public"."c3"`, `"m" "public"."mood"`, `"sub" "public"."comp_t"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺 %q:\n%s", want, got)
		}
	}
	assertBefore(t, got, `"c3" AS`, `CREATE TYPE "public"."outer_t"`)
	assertBefore(t, got, `CREATE TYPE "public"."comp_t"`, `CREATE TYPE "public"."outer_t"`)
}

// 域基为自定义数组：输出 "comp_t"[] 而非内部名 _comp_t，且 comp_t 先建
func TestV1210DomainOverArrayBaseName(t *testing.T) {
	withDepFixture(t)
	got := ddlOf(engine.ColumnDef{Name: "v", TypeOID: oDArr})
	if !strings.Contains(got, `CREATE DOMAIN "public"."d_arr" AS "public"."comp_t"[]`) {
		t.Fatalf("数组基未渲染为 comp_t[]:\n%s", got)
	}
	if strings.Contains(got, "_comp_t") {
		t.Fatalf("仍输出内部数组名 _comp_t:\n%s", got)
	}
	assertBefore(t, got, `CREATE TYPE "public"."comp_t"`, `CREATE DOMAIN "public"."d_arr"`)
}

// 旧场景：单层域（base 内置）输出不变；多类型列共存时桶序仍为枚举→域→复合
func TestV1210LegacyShapeUnchanged(t *testing.T) {
	withDepFixture(t)
	// 单层域
	got := ddlOf(engine.ColumnDef{Name: "v", TypeOID: oC1})
	if !strings.Contains(got, `CREATE DOMAIN "public"."c1" AS "char"`) {
		t.Fatalf("单层域输出不符:\n%s", got)
	}
	// 枚举列 + 复合列共存：枚举段先于复合段（桶序不变）
	got = ddlOf(
		engine.ColumnDef{Name: "m", TypeOID: oMood},
		engine.ColumnDef{Name: "v", TypeOID: oCompT},
	)
	assertBefore(t, got, `CREATE TYPE "public"."mood"`, `CREATE TYPE "public"."comp_t"`)
	// 复合列 + 域列共存：域段先于复合段（桶序不变）
	got = ddlOf(
		engine.ColumnDef{Name: "v", TypeOID: oCompT},
		engine.ColumnDef{Name: "d", TypeOID: oC1},
	)
	assertBefore(t, got, `CREATE DOMAIN "public"."c1"`, `CREATE TYPE "public"."comp_t"`)
}

// 防御性：依赖成环不死循环（visiting 守卫）
func TestV1210CycleGuard(t *testing.T) {
	oldDefs := datatype.DynamicTypeDefs
	datatype.DynamicTypeDefs = map[uint32]*datatype.TypeDef{
		92001: {OID: 92001, Name: "ra", Type: 'd', Base: 92002},
		92002: {OID: 92002, Name: "rb", Type: 'd', Base: 92001},
	}
	t.Cleanup(func() { datatype.DynamicTypeDefs = oldDefs })
	got := ddlOf(engine.ColumnDef{Name: "v", TypeOID: 92001}) // 只要返回即通过
	if !strings.Contains(got, "CREATE DOMAIN") {
		t.Fatalf("环场景无输出:\n%s", got)
	}
}
