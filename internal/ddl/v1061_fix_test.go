package ddl

// v1.0.61 第三批修复回归（P1-8 / P2-9）。
// 只覆盖纯函数层（不走磁盘目录扫描）：类型名限定/引号、char[] 解析、
// 函数 DDL 的 prokind/proretset/proargmodes 语义。

import (
	"strings"
	"testing"

	"pg2sql/internal/datatype"
)

// TestV1061ProcTypeNameQuote —— P1-8：函数签名类型名必须加引号防注入。
func TestV1061ProcTypeNameQuote(t *testing.T) {
	sc := &SchemaCatalog{typeNames: map[uint32]string{
		25:   "text",
		1043: "varchar",
		9001: "x; DROP TABLE t; --",
	}}
	// 内置类型保持裸名（加引号会改变类型解析，如 "character varying"）
	if got := procTypeName(sc, 25); got != "text" {
		t.Errorf("内置 text: got %q", got)
	}
	// 非标识符字符的 typname 必须被引号包裹（不得裸奔进 SQL）
	if got := procTypeName(sc, 9001); !strings.HasPrefix(got, `"`) {
		t.Errorf("恶意 typname 未加引号: %q", got)
	}
	// 未知 oid 兜底为 oid，不得产生空类型名
	if got := procTypeName(sc, 0); got != "oid" {
		t.Errorf("oid=0 兜底: got %q", got)
	}
	if got := procTypeName(sc, 123456); got != "oid" {
		t.Errorf("未知 oid 兜底: got %q", got)
	}
}

// TestV1061ProcTypeNameArray —— 数组类型输出 "elem[]"，自定义元素加限定。
func TestV1061ProcTypeNameArray(t *testing.T) {
	datatype.DynamicTypeDefs = map[uint32]*datatype.TypeDef{
		9100: {OID: 9100, Name: "mood", Type: 'e', Array: 9101},
		9101: {OID: 9101, Name: "_mood", Type: 'b', Elem: 9100},
	}
	defer func() { datatype.DynamicTypeDefs = nil }()
	sc := &SchemaCatalog{typeNames: map[uint32]string{9100: "mood", 9101: "_mood"}}
	got := procTypeName(sc, 9101)
	// 元数据里没有 nsp 映射 → 只能保证「元素名带引号 + 方括号在引号外」
	if got != `"mood"[]` {
		t.Errorf("自定义数组元素类型名: got %q, want %q", got, `"mood"[]`)
	}
}

// TestV1061ParseCharArray —— P2-9：proargmodes（char[]）元素宽度 1 字节。
func TestV1061ParseCharArray(t *testing.T) {
	// 头部 20B：ndim=1, dataoffset=20, elemtype=18(char), dim1=3, lbound=1
	hdr := make([]byte, 20)
	put32 := func(off, v int) {
		hdr[off] = byte(v)
		hdr[off+1] = byte(v >> 8)
		hdr[off+2] = byte(v >> 16)
		hdr[off+3] = byte(v >> 24)
	}
	put32(0, 1)
	put32(4, 20)
	put32(8, 18)
	put32(12, 3)
	put32(16, 1)
	payload := append(hdr, 'i', 'o', 't')
	got := ParseCharArray(payload)
	if string(got) != "iot" {
		t.Fatalf("ParseCharArray = %q, want %q", string(got), "iot")
	}
	if ParseCharArray([]byte{1, 2, 3}) != nil {
		t.Error("过短输入应返回 nil")
	}
}

// TestV1061ProcDDLRetSet —— P2-9：proretset → RETURNS SETOF；存储过程用 CREATE PROCEDURE。
func TestV1061ProcDDLRetSet(t *testing.T) {
	sc := &SchemaCatalog{
		typeNames: map[uint32]string{23: "int4", 25: "text"},
		langNames: map[uint32]string{14: "sql"},
		ProcRows: []procRow{
			{OID: 1, Schema: "public", Name: "gen_rows", RetType: 23, Language: 14,
				Volatile: 'v', Kind: 'f', RetSet: true, Body: "SELECT 1"},
			{OID: 2, Schema: "public", Name: "do_work", RetType: 2278, Language: 14,
				Volatile: 'v', Kind: 'p', Body: "BEGIN NULL; END"},
		},
	}
	out := GenFullSchemaDDL(sc, map[string]bool{"function": true})
	if !strings.Contains(out, "RETURNS SETOF int4") {
		t.Errorf("proretset 未输出 SETOF:\n%s", out)
	}
	if !strings.Contains(out, "CREATE OR REPLACE PROCEDURE") {
		t.Errorf("prokind='p' 未输出 CREATE PROCEDURE:\n%s", out)
	}
	if strings.Contains(out, "PROCEDURE \"public\".\"do_work\"() RETURNS") {
		t.Errorf("存储过程不应带 RETURNS:\n%s", out)
	}
}

// TestV1061ProcDDLArgModes —— P2-9：OUT 参数时不得写 RETURNS；TABLE 模式 → RETURNS TABLE。
func TestV1061ProcDDLArgModes(t *testing.T) {
	sc := &SchemaCatalog{
		typeNames: map[uint32]string{23: "int4", 25: "text"},
		langNames: map[uint32]string{14: "sql"},
		ProcRows: []procRow{
			{OID: 3, Schema: "public", Name: "calc", RetType: 2249, Language: 14,
				Volatile: 'i', Kind: 'f', ArgTypes: []uint32{23, 23}, ArgModes: []byte{'i', 'o'}, Body: "SELECT $1"},
			{OID: 4, Schema: "public", Name: "tbl", RetType: 2249, Language: 14,
				Volatile: 's', Kind: 'f', RetSet: true, ArgTypes: []uint32{23, 25}, ArgModes: []byte{'i', 't'}, Body: "SELECT 1,'x'"},
		},
	}
	out := GenFullSchemaDDL(sc, map[string]bool{"function": true})
	if !strings.Contains(out, "OUT int4") {
		t.Errorf("OUT 模式未输出:\n%s", out)
	}
	if strings.Contains(out, "calc(int4, OUT int4) RETURNS") {
		t.Errorf("有 OUT 参数时不得写 RETURNS:\n%s", out)
	}
	// v1.0.63 修复（A-1）：RETURNS TABLE 必须带列名。PG 语法是
	// `RETURNS TABLE (列名 类型)`，只给类型（旧断言断言的正是 `RETURNS TABLE (text)`）
	// 会让整段 DDL 报 syntax error at or near ")" —— 旧断言把错误输出钉成了期望值。
	if !strings.Contains(out, `RETURNS TABLE ("column1" text)`) {
		t.Errorf("TABLE 模式未输出带列名的 RETURNS TABLE:\n%s", out)
	}
	if strings.Contains(out, "RETURNS TABLE (text)") {
		t.Errorf("RETURNS TABLE 缺少列名，回灌语法错误:\n%s", out)
	}
}

// TestV1063ProcDDLTableColumnNames —— A-1：proargnames 有名用名，无名回退 columnN。
func TestV1063ProcDDLTableColumnNames(t *testing.T) {
	sc := &SchemaCatalog{
		typeNames: map[uint32]string{23: "int4", 25: "text"},
		langNames: map[uint32]string{14: "sql"},
		ProcRows: []procRow{{
			OID: 6, Schema: "public", Name: "f_tbl", RetType: 2249, Language: 14,
			Volatile: 'v', Kind: 'f', RetSet: true,
			ArgTypes: []uint32{23, 23, 25}, ArgModes: []byte{'i', 't', 't'},
			ArgNames: []string{"a", "Total", ""}, Body: "SELECT 1,2,'x'",
		}},
	}
	out := GenFullSchemaDDL(sc, map[string]bool{"function": true})
	if !strings.Contains(out, `RETURNS TABLE ("Total" int4, "column2" text)`) {
		t.Errorf("RETURNS TABLE 列名还原错误（有名用名/无名回退 columnN/大写加引号）:\n%s", out)
	}
}

// TestV1061ProcDDLAggSkip —— P2-9：聚合/窗口函数不得当作普通函数导出。
func TestV1061ProcDDLAggSkip(t *testing.T) {
	sc := &SchemaCatalog{
		typeNames: map[uint32]string{23: "int4"},
		langNames: map[uint32]string{13: "internal"},
		ProcRows: []procRow{
			{OID: 5, Schema: "public", Name: "my_sum", RetType: 23, Language: 13,
				Kind: 'a', Body: "int4pl"},
		},
	}
	out := GenFullSchemaDDL(sc, map[string]bool{"function": true})
	if strings.Contains(out, "CREATE OR REPLACE FUNCTION") {
		t.Errorf("聚合函数不应生成 CREATE FUNCTION:\n%s", out)
	}
	if !strings.Contains(out, "聚合函数") {
		t.Errorf("缺少聚合函数说明注释:\n%s", out)
	}
}
