package ddl

// v1.0.50 回归：域 CHECK 约束节点树反解。
// 夹具为 PG17 实库导出的真实 conbin（dchk schema，4 个域）+ 覆盖各分支的构造用例。

import "testing"

import (
	"pg2sql/internal/nt"
)

// 实库样本的算符/函数映射（与 pg_operator/pg_proc 中这几项一致）
func dchkMaps() (map[uint32]nt.OpInfo, map[uint32]string) {
	opMap := map[uint32]nt.OpInfo{
		521:  {}, // int4 >  (kind 会被 catalog.BuildOperatorMap 覆盖，这里手工填)
		523:  {}, // int4 <=
		531:  {}, // text <>
		1755: {}, // numeric <=
		1757: {}, // numeric >=
		98:   {}, // text =
	}
	// kind 字段上面写错了字符，直接重填为 'b'
	for k := range opMap {
		opMap[k] = nt.OpInfo{Name: map[uint32]string{521: ">", 523: "<=", 531: "<>", 1755: "<=", 1757: ">=", 98: "="}[k], Kind: 'b'}
	}
	procMap := map[uint32]string{
		1381: "char_length",
		1740: "numeric", // numeric(integer) 转型
	}
	return opMap, procMap
}

func TestDeparseDomainChecks(t *testing.T) {
	opMap, procMap := dchkMaps()
	cases := []struct {
		name string
		tree string
		want string
	}{
		{"posint", `{OPEXPR :opno 521 :opfuncid 147 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({COERCETODOMAINVALUE :typeId 23 :typeMod -1 :collation 0 :location -1} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 0 0 0 0 0 0 0 0 ]}) :location -1}`,
			"VALUE > 0"},
		{"shortname", `{BOOLEXPR :boolop and :args ({OPEXPR :opno 523 :opfuncid 149 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({FUNCEXPR :funcid 1381 :funcresulttype 23 :funcretset false :funcvariadic false :funcformat 0 :funccollid 0 :inputcollid 100 :args ({COERCETODOMAINVALUE :typeId 25 :typeMod -1 :collation 100 :location -1}) :location -1} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 5 0 0 0 0 0 0 0 ]}) :location -1} {OPEXPR :opno 531 :opfuncid 157 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 100 :args ({COERCETODOMAINVALUE :typeId 25 :typeMod -1 :collation 100 :location -1} {CONST :consttype 25 :consttypmod -1 :constcollid 100 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 9 [ 36 0 0 0 97 100 109 105 110 ]}) :location -1}) :location -1}`,
			"(char_length(VALUE) <= 5 AND VALUE <> 'admin')"},
		{"grade(ARRAYEXPR)", `{SCALARARRAYOPEXPR :opno 98 :opfuncid 67 :hashfuncid 0 :negfuncid 0 :useOr true :inputcollid 100 :args ({COERCETODOMAINVALUE :typeId 25 :typeMod -1 :collation 100 :location -1} {ARRAYEXPR :array_typeid 1009 :array_collid 100 :element_typeid 25 :elements ({CONST :consttype 25 :consttypmod -1 :constcollid 100 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 5 [ 20 0 0 0 65 ]} {CONST :consttype 25 :consttypmod -1 :constcollid 100 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 5 [ 20 0 0 0 66 ]} {CONST :consttype 25 :consttypmod -1 :constcollid 100 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 5 [ 20 0 0 0 67 ]}) :multidims false :location -1}) :location -1}`,
			"VALUE IN ('A', 'B', 'C')"},
		{"grade(ARRAY 金仓V9/PG15-)", `{SCALARARRAYOPEXPR :opno 98 :opfuncid 67 :hashfuncid 0 :useOr true :inputcollid 100 :args ({COERCETODOMAINVALUE :typeId 25 :typeMod -1 :collation 100 :location 130} {ARRAY :array_typeid 1009 :array_collid 100 :element_typeid 25 :elements ({CONST :consttype 25 :consttypmod -1 :constcollid 100 :constlen -1 :constbyval false :constisnull false :location 140 :constvalue 5 [ 20 0 0 0 65 ]} {CONST :consttype 25 :consttypmod -1 :constcollid 100 :constlen -1 :constbyval false :constisnull false :location 144 :constvalue 5 [ 20 0 0 0 66 ]} {CONST :consttype 25 :consttypmod -1 :constcollid 100 :constlen -1 :constbyval false :constisnull false :location 148 :constvalue 5 [ 20 0 0 0 67 ]}) :multidims false :location -1}) :location 136}`,
			"VALUE IN ('A', 'B', 'C')"},
		{"pct(BETWEEN→numeric cast)", `{BOOLEXPR :boolop and :args ({OPEXPR :opno 1757 :opfuncid 1721 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({COERCETODOMAINVALUE :typeId 1700 :typeMod 327686 :collation 0 :location -1} {FUNCEXPR :funcid 1740 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 0 0 0 0 0 0 0 0 ]}) :location -1}) :location -1} {OPEXPR :opno 1755 :opfuncid 1723 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({COERCETODOMAINVALUE :typeId 1700 :typeMod 327686 :collation 0 :location -1} {FUNCEXPR :funcid 1740 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 100 0 0 0 0 0 0 0 ]}) :location -1}) :location -1}) :location -1}`,
			"(VALUE >= 0 AND VALUE <= 100)"},
	}
	for _, c := range cases {
		got := nt.DeparseNodeTree(c.tree, opMap, procMap, nil)
		if got != c.want {
			t.Errorf("%s:\n  got  %s\n  want %s", c.name, got, c.want)
		}
	}
}

// 旧内核（PG12/金仓）：布尔枚举为整数、域值为 VAR、text 常量 \x 十六进制
func TestDeparseLegacyForms(t *testing.T) {
	opMap, procMap := dchkMaps()
	// boolop=0(AND)、VAR、\x hex text const（'admin' = 9B in-memory varlena: 24 00 00 00 61 64 6d 69 6e）
	tree := `{BOOLEXPR :boolop 0 :args ({OPEXPR :opno 531 :opfuncid 157 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 100 :args ({VAR :varno 1 :varattno 1 :vartype 25 :vartypmod -1 :varcollid 100 :varlevelsup 0 :varnosyn 1 :varattnosyn 1 :location -1} {CONST :consttype 25 :consttypmod -1 :constcollid 100 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue \x2400000061646d696e}) :location -1} {OPEXPR :opno 523 :opfuncid 149 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({VAR :varno 1 :varattno 1 :vartype 25 :vartypmod -1 :varcollid 100 :varlevelsup 0 :varnosyn 1 :varattnosyn 1 :location -1} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 5 0 0 0 0 0 0 0 ]}) :location -1}) :location -1}`
	got := nt.DeparseNodeTree(tree, opMap, procMap, nil)
	want := "(VALUE <> 'admin' AND VALUE <= 5)"
	if got != want {
		t.Errorf("legacy: got %s want %s", got, want)
	}
}

// 不可还原场景：未知节点/未知算符/未知函数 → 空串（DDL 端回退人工提示）
func TestDeparseUnsupported(t *testing.T) {
	opMap, procMap := dchkMaps()
	if got := nt.DeparseNodeTree(`{SUBPLAN :something 1}`, opMap, procMap, nil); got != "" {
		t.Errorf("未知节点应返回空串: %q", got)
	}
	if got := nt.DeparseNodeTree(`{OPEXPR :opno 999999 :args ({COERCETODOMAINVALUE} {CONST :consttype 23 :constlen 4 :constbyval true :constisnull false :constvalue 4 [ 1 0 0 0 ]})}`, opMap, procMap, nil); got != "" {
		t.Errorf("未知算符应返回空串: %q", got)
	}
	if got := nt.DeparseNodeTree(`{FUNCEXPR :funcid 999999 :args ({COERCETODOMAINVALUE})}`, opMap, procMap, nil); got != "" {
		t.Errorf("未知函数应返回空串: %q", got)
	}
	if got := nt.DeparseNodeTree(`不是节点树`, opMap, procMap, nil); got != "" {
		t.Errorf("非法文本应返回空串: %q", got)
	}
}

// 常量字面量：int8/float/bool/date/time/numeric/NULL
func TestDeparseConstLiterals(t *testing.T) {
	opMap, procMap := dchkMaps()
	cases := []struct{ name, tree, want string }{
		{"int8", `{CONST :consttype 20 :constlen 8 :constbyval true :constisnull false :constvalue 8 [ 255 255 255 255 255 255 255 255 ]}`, "-1"},
		{"bool", `{CONST :consttype 16 :constlen 1 :constbyval true :constisnull false :constvalue 1 [ 1 ]}`, "TRUE"},
		{"null", `{CONST :consttype 23 :constlen 4 :constbyval true :constisnull true :constvalue <>}`, "NULL"},
		{"text-hex", `{CONST :consttype 25 :constlen -1 :constbyval false :constisnull false :constvalue \x1c000000616263}`, "'abc'"},
		{"quote-in-text", `{CONST :consttype 25 :constlen -1 :constbyval false :constisnull false :constvalue \x180000006127}`, "'a'''"},
	}
	for _, c := range cases {
		got := nt.DeparseNodeTree(c.tree, opMap, procMap, nil)
		if got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
	// date：天数 1 → '2000-01-02'
	if got := nt.DeparseNodeTree(`{CONST :consttype 1082 :constlen 4 :constbyval true :constisnull false :constvalue 4 [ 1 0 0 0 ]}`, opMap, procMap, nil); got != "'2000-01-02'" {
		t.Errorf("date: got %q", got)
	}
	// float8：2.5
	if got := nt.DeparseNodeTree(`{CONST :consttype 701 :constlen 8 :constbyval true :constisnull false :constvalue 8 [ 0 0 0 0 0 0 4 64 ]}`, opMap, procMap, nil); got != "2.5" {
		t.Errorf("float8: got %q", got)
	}
}

// 约束名输出
func TestConstraintNameSQL(t *testing.T) {
	if got := ConstraintNameSQL("posint_check"); got != "posint_check" {
		t.Errorf("got %q", got)
	}
	if got := ConstraintNameSQL("weird name"); got != `"weird name"` {
		t.Errorf("got %q", got)
	}
	if got := ConstraintNameSQL("9start"); got != `"9start"` {
		t.Errorf("got %q", got)
	}
}
