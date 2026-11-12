// v1.2.17 回归用例：DDL 三缺口中的两项——CREATE SCHEMA 与域 DEFAULT。
// （第三项 COLLATE 的根因是目录布局，见 engine/v1217_attr_layout_test.go）
//
// 缺陷现场（金仓 V9R1C10B4，oracle/pg 模式均可复现）：
//
//	CREATE TABLE "My Sch"."t_cross" / 域 "My Sch"."dom_d" AS int DEFAULT 7
//	旧输出：直接从 CREATE DOMAIN "My Sch"."dom_d" AS int4 开始，
//	        既没有 CREATE SCHEMA，也没有 DEFAULT —— 空库回灌第一条就报
//	        schema "My Sch" does not exist；即便补上 schema，域默认值也已丢失。
package app

import (
	"strings"
	"testing"

	"pg2sql/internal/datatype"
	"pg2sql/internal/ddl"
	"pg2sql/internal/engine"
)

const (
	oDomD  = 17010 // "My Sch".dom_d  int4  DEFAULT 7
	oDomT  = 17012 // "My Sch".dom_t  text  DEFAULT 'dflt'::text
	oDomN  = 17014 // 无默认值的域（反向断言：不得凭空多出 DEFAULT）
	oPlain = 17016 // "My Sch".notdom  普通基类型记录（非域）
)

func withSchemaFixture(t *testing.T) {
	t.Helper()
	oldDefs := datatype.DynamicTypeDefs
	oldChecks := ddl.DomainCheckSnapshot()
	datatype.DynamicTypeDefs = map[uint32]*datatype.TypeDef{
		oDomD: {OID: oDomD, Name: "dom_d", Type: 'd', Base: 23, TypDefault: "7"},
		oDomT: {OID: oDomT, Name: "dom_t", Type: 'd', Base: 25, TypDefault: "'dflt'::text"},
		oDomN: {OID: oDomN, Name: "dom_n", Type: 'd', Base: 23},
	}
	datatype.CompositeAttrs = map[uint32][]*datatype.AttrRow{}
	ddl.SetDomainChecks(map[uint32][]ddl.DomainCheck{})
	t.Cleanup(func() {
		datatype.DynamicTypeDefs = oldDefs
		ddl.SetDomainChecks(oldChecks)
	})
}

func crossDDL(schema string) string {
	return generateDDL(&engine.TableMeta{
		Schema:  schema,
		RelName: "t_cross",
		TypeNames: map[uint32]string{
			23: "int4", 25: "text",
			oDomD: "dom_d", oDomT: "dom_t", oDomN: "dom_n",
		},
		Columns: []engine.ColumnDef{
			{Name: "id", TypeOID: 23},
			{Name: "d", TypeOID: oDomD},
			{Name: "dt", TypeOID: oDomT},
			{Name: "n", TypeOID: oDomN},
		},
	})
}

// 非默认 schema：必须先 CREATE SCHEMA，且早于任何 CREATE DOMAIN / CREATE TABLE。
func TestV1217CreateSchemaBeforeTypes(t *testing.T) {
	withSchemaFixture(t)
	got := crossDDL("My Sch")
	want := `CREATE SCHEMA IF NOT EXISTS "My Sch";`
	if !strings.Contains(got, want) {
		t.Fatalf("缺少 %s\n---- 实际 ----\n%s", want, got)
	}
	iSchema := strings.Index(got, want)
	for _, later := range []string{`CREATE DOMAIN "My Sch"."dom_d"`, `CREATE TABLE IF NOT EXISTS "My Sch"."t_cross"`} {
		j := strings.Index(got, later)
		if j < 0 {
			t.Fatalf("缺少语句 %s\n---- 实际 ----\n%s", later, got)
		}
		if iSchema > j {
			t.Errorf("CREATE SCHEMA 必须早于 %s", later)
		}
	}
}

// public：不得输出 CREATE SCHEMA（目标库自带，多文件回灌全是噪音）。
func TestV1217CreateSchemaSkippedForPublic(t *testing.T) {
	withSchemaFixture(t)
	got := crossDDL("public")
	if strings.Contains(got, "CREATE SCHEMA") {
		t.Errorf("public schema 不应输出 CREATE SCHEMA\n---- 实际 ----\n%s", got)
	}
}

// 域 DEFAULT：位置在 AS <type> 之后、NOT NULL 之前，且不再二次 quoting。
func TestV1217DomainDefault(t *testing.T) {
	withSchemaFixture(t)
	got := crossDDL("My Sch")
	cases := []struct{ want, why string }{
		{`CREATE DOMAIN "My Sch"."dom_d" AS int4 DEFAULT 7`, "整型常量默认值"},
		{`CREATE DOMAIN "My Sch"."dom_t" AS text DEFAULT 'dflt'::text`, "文本常量默认值（typdefault 原文，不得再包引号）"},
		{`CREATE DOMAIN "My Sch"."dom_n" AS int4`, "无默认值的域"},
	}
	for _, c := range cases {
		if !strings.Contains(got, c.want) {
			t.Errorf("缺少 %q（%s）\n---- 实际 ----\n%s", c.want, c.why, got)
		}
	}
	if strings.Contains(got, `'''dflt''::text'`) {
		t.Errorf("typdefault 被二次 quoting：'dflt'::text 变成了 '''dflt''::text'")
	}
	// 无默认值的域不得多出 DEFAULT
	if strings.Contains(got, `"dom_n" AS int4 DEFAULT`) {
		t.Errorf("dom_n 无默认值却输出了 DEFAULT\n---- 实际 ----\n%s", got)
	}
}
