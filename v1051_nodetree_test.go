package main

// v1.0.51 单测：nodetree 泛化后的默认值（adbin）与表达式/部分索引（indexprs/indpred）反解。
// 夹具全部为 PG17 实库真实节点树（nt51.t1，2026-10-27 抓取）；
// 期望值与 pg_get_expr / pg_get_indexdef 语义对齐（常量按我们更简的等价形式断言）。

import "testing"

func ntTestMaps() (map[uint32]opInfo, map[uint32]string) {
	opMap := map[uint32]opInfo{
		96:   {name: "=", kind: 'b'},
		514:  {name: "*", kind: 'b'},
		521:  {name: ">", kind: 'b'},
		551:  {name: "+", kind: 'b'},
		1756: {name: ">", kind: 'b'},
	}
	procMap := map[uint32]string{
		870:  "lower",
		1299: "now",
		1740: "numeric",
		1746: "float8",
		3432: "gen_random_uuid",
	}
	return opMap, procMap
}

// t1 列：id=1 uid=2 amt=3 name=4 created=5 ...
func ntTestVarAt() func(int) string {
	names := map[int]string{2: "\"uid\"", 3: "\"amt\"", 4: "\"name\""}
	return func(attno int) string { return names[attno] }
}

func TestV1051DefaultDeparse(t *testing.T) {
	opMap, procMap := ntTestMaps()
	cases := []struct{ name, tree, want string }{
		{"now()", `{FUNCEXPR :funcid 1299 :funcresulttype 1184 :funcretset false :funcvariadic false :funcformat 0 :funccollid 0 :inputcollid 0 :args <> :location -1}`,
			"now()"},
		{"gen_random_uuid()", `{FUNCEXPR :funcid 3432 :funcresulttype 2950 :funcretset false :funcvariadic false :funcformat 0 :funccollid 0 :inputcollid 0 :args <> :location -1}`,
			"gen_random_uuid()"},
		{"int4 负常量", `{CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ -1 -1 -1 -1 -1 -1 -1 -1 ]}`,
			"-1"},
		{"text 含引号", `{CONST :consttype 25 :consttypmod -1 :constcollid 100 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 8 [ 32 0 0 0 97 98 39 99 ]}`,
			`'ab''c'`},
		{"bool", `{CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 1 0 0 0 0 0 0 0 ]}`,
			"TRUE"},
		{"float8(numeric 转型)", `{FUNCEXPR :funcid 1746 :funcresulttype 701 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -128 2 0 -120 19 ]}) :location -1}`,
			"2.5"},
	}
	for _, c := range cases {
		got := deparseNodeTree(c.tree, opMap, procMap, nil)
		if got != c.want {
			t.Errorf("[%s] got %q want %q", c.name, got, c.want)
		}
	}
	// nextval 形态（regclass consttype 2205 无 ntLiteral 分支）→ 不可反解，
	// 由 buildDdlStatements 的正则路径处理，这里确认反解器安全返回空串
	nextval := `{FUNCEXPR :funcid 480 :funcresulttype 23 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({FUNCEXPR :funcid 1574 :funcresulttype 20 :funcretset false :funcvariadic false :funcformat 0 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 2205 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 14 67 0 0 0 0 0 0 ]}) :location -1}) :location -1}`
	if got := deparseNodeTree(nextval, opMap, procMap, nil); got != "" {
		t.Errorf("[nextval] 期望空串走 colSeq 正则路径，got %q", got)
	}
}

func TestV1051IndexDeparse(t *testing.T) {
	opMap, procMap := ntTestMaps()
	varAt := ntTestVarAt()

	// idx_lower：单表达式键（旧格式外层括号列表）
	lower := `({FUNCEXPR :funcid 870 :funcresulttype 25 :funcretset false :funcvariadic false :funcformat 0 :funccollid 100 :inputcollid 100 :args ({VAR :varno 1 :varattno 4 :vartype 25 :vartypmod -1 :varcollid 100 :varnullingrels (b) :varlevelsup 0 :varnosyn 1 :varattnosyn 4 :location -1}) :location -1})`
	got := deparseNodeTreeList(lower, opMap, procMap, varAt)
	if len(got) != 1 || got[0] != "lower(\"name\")" {
		t.Errorf("[idx_lower] got %v", got)
	}

	// idx_expr：双表达式键（uid*2+1, lower(name)）
	expr := `({OPEXPR :opno 551 :opfuncid 177 :opresulttype 23 :opretset false :opcollid 0 :inputcollid 0 :args ({OPEXPR :opno 514 :opfuncid 141 :opresulttype 23 :opretset false :opcollid 0 :inputcollid 0 :args ({VAR :varno 1 :varattno 2 :vartype 23 :vartypmod -1 :varcollid 0 :varnullingrels (b) :varlevelsup 0 :varnosyn 1 :varattnosyn 2 :location -1} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 2 0 0 0 0 0 0 0 ]}) :location -1} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]}) :location -1} {FUNCEXPR :funcid 870 :funcresulttype 25 :funcretset false :funcvariadic false :funcformat 0 :funccollid 100 :inputcollid 100 :args ({VAR :varno 1 :varattno 4 :vartype 25 :vartypmod -1 :varcollid 100 :varnullingrels (b) :varlevelsup 0 :varnosyn 1 :varattnosyn 4 :location -1}) :location -1})`
	got = deparseNodeTreeList(expr, opMap, procMap, varAt)
	if len(got) != 2 || got[0] != "\"uid\" * 2 + 1" || got[1] != "lower(\"name\")" {
		t.Errorf("[idx_expr] got %v", got)
	}

	// idx_partial：纯列键 + numeric 比较谓词
	pred := `{OPEXPR :opno 1756 :opfuncid 1720 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({VAR :varno 1 :varattno 3 :vartype 1700 :vartypmod 655366 :varcollid 0 :varnullingrels (b) :varlevelsup 0 :varnosyn 1 :varattnosyn 3 :location -1} {FUNCEXPR :funcid 1740 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 100 0 0 0 0 0 0 0 ]}) :location -1}) :location -1}`
	if got := deparseNodeTree(pred, opMap, procMap, varAt); got != "\"amt\" > 100" {
		t.Errorf("[idx_partial] got %q", got)
	}

	// idx_in：IN 谓词（SCALARARRAYOPEXPR + ARRAYEXPR）
	predIn := `{SCALARARRAYOPEXPR :opno 96 :opfuncid 65 :hashfuncid 0 :negfuncid 0 :useOr true :inputcollid 0 :args ({VAR :varno 1 :varattno 2 :vartype 23 :vartypmod -1 :varcollid 0 :varnullingrels (b) :varlevelsup 0 :varnosyn 1 :varattnosyn 2 :location -1} {ARRAYEXPR :array_typeid 1007 :array_collid 0 :element_typeid 23 :elements ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 1 0 0 0 0 0 0 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 2 0 0 0 0 0 0 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 3 0 0 0 0 0 0 0 ]}) :multidims false :location -1}) :location -1}`
	if got := deparseNodeTree(predIn, opMap, procMap, varAt); got != "\"uid\" IN (1, 2, 3)" {
		t.Errorf("[idx_in] got %q", got)
	}
}

func TestV1051Fallbacks(t *testing.T) {
	opMap, procMap := ntTestMaps()
	varAt := ntTestVarAt()
	// 截断节点树 → 空串（回退注释，不 panic）
	if got := deparseNodeTree(`{FUNCEXPR :funcid 1299 :args ({VAR`, opMap, procMap, varAt); got != "" {
		t.Errorf("截断树应回退空串，got %q", got)
	}
	// VAR 指向不存在列号（varAt 返回空）→ 整体不可还原
	if got := deparseNodeTree(`{FUNCEXPR :funcid 870 :args ({VAR :varattno 99})}`, opMap, procMap, varAt); got != "" {
		t.Errorf("未知列应整体回退，got %q", got)
	}
	// 列表项反解失败 → 列表整体 nil
	if got := deparseNodeTreeList(`{FUNCEXPR :funcid 870 :args ({VAR :varattno 99})} {CONST :consttype 23 :constlen 4 :constbyval true :constisnull false :constvalue 4 [ 1 0 0 0 ]}`, opMap, procMap, varAt); got != nil {
		t.Errorf("列表失败项应整体 nil，got %v", got)
	}
}
