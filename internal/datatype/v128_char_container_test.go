// v1.2.8 P2 测试：char 容器（数组元素 / 域基类型 / 复合字段）的 SQL 表达式生成。
// 作者: raysuen
package datatype

import (
	"strings"
	"testing"
)

// withCharCatalog 临时注入类型目录（数组/域/复合），返回恢复函数。
func withCharCatalog(t *testing.T) func() {
	t.Helper()
	oldDefs, oldAttrs, oldNsp := DynamicTypeDefs, CompositeAttrs, regNamespaceNames
	DynamicTypeDefs = map[uint32]*TypeDef{
		18:    {OID: 18, Name: "char", Type: 'b'},
		23:    {OID: 23, Name: "int4", Type: 'b'},
		1002:  {OID: 1002, Name: "_char", Type: 'b', Elem: 18, Array: 1002},
		80001: {OID: 80001, Name: "g_d", Nsp: 2200, Type: 'd', Base: 18},
		80002: {OID: 80002, Name: "g_darr", Nsp: 2200, Type: 'd', Base: 1002},
		80010: {OID: 80010, Name: "ct1", Nsp: 2200, Type: 'c', TypRelid: 90001},
		80011: {OID: 80011, Name: "ct2", Nsp: 2200, Type: 'c', TypRelid: 90002},
		80012: {OID: 80012, Name: "d_ct", Nsp: 2200, Type: 'd', Base: 80010},               // 域 over 复合
		80020: {OID: 80020, Name: "_ct1", Nsp: 2200, Type: 'b', Elem: 80010, Array: 80020}, // ct1[]
	}
	CompositeAttrs = map[uint32][]*AttrRow{
		90001: {{AttName: "c", AttTypID: 18, AttLen: 1}, {AttName: "n", AttTypID: 23, AttLen: 4}},
		90002: {{AttName: "a", AttTypID: 1002, AttLen: -1}, {AttName: "in_c", AttTypID: 80010, AttLen: -1}},
	}
	regNamespaceNames = map[uint32]string{2200: "public"}
	ResetCharContainerKindCache() // 目录已替换，缓存必须清空
	return func() {
		DynamicTypeDefs, CompositeAttrs, regNamespaceNames = oldDefs, oldAttrs, oldNsp
		ResetCharContainerKindCache()
	}
}

func TestV128CharContainerScalarDomain(t *testing.T) {
	defer withCharCatalog(t)()
	// 域 over "char"：直接输出 "char" 强转，不需要域名
	got, ok := CharContainerExpr(80001, string([]byte{0xC3}))
	if !ok || got != `(-61)::"char"` {
		t.Fatalf("域标量 0xC3 = %q ok=%v，期望 (-61)::\"char\"", got, ok)
	}
	if got, ok := CharContainerExpr(18, string([]byte{0})); !ok || got != `(0)::"char"` {
		t.Fatalf("标量 NUL = %q ok=%v", got, ok)
	}
	// 普通 ASCII 不改写
	if _, ok := CharContainerExpr(80001, "A"); ok {
		t.Fatal("ASCII 不应改写")
	}
}

func TestV128CharArrayExpr(t *testing.T) {
	defer withCharCatalog(t)()
	cases := []struct{ name, in, want string }{
		{"高位字节", "{" + string([]byte{0xC3}) + ",A}", `ARRAY[(-61)::"char",'A'::"char"]`},
		{"NUL", "{" + string([]byte{0}) + ",A}", `ARRAY[(0)::"char",'A'::"char"]`},
		{"含NULL", "{NULL," + string([]byte{0xC3}) + "}", `ARRAY[NULL::"char",(-61)::"char"]`},
		{"多维", "{{A," + string([]byte{0xC3}) + "},{C,D}}",
			`ARRAY[['A'::"char",(-61)::"char"],['C'::"char",'D'::"char"]]`},
		{"带引号元素", `{"\"",` + string([]byte{0xC3}) + `}`, `ARRAY['"'::"char",(-61)::"char"]`},
	}
	for _, c := range cases {
		got, ok := CharContainerExpr(1002, c.in)
		if !ok || got != c.want {
			t.Fatalf("%s: got %q ok=%v，期望 %q", c.name, got, ok, c.want)
		}
	}
	// 域 over "char"[]
	if got, ok := CharContainerExpr(80002, "{"+string([]byte{0x80})+"}"); !ok || got != `ARRAY[(-128)::"char"]` {
		t.Fatalf("域数组 0x80 = %q ok=%v", got, ok)
	}
}

func TestV128CharArrayNoChange(t *testing.T) {
	defer withCharCatalog(t)()
	takeCharContainerDegradedCount()
	for _, in := range []string{"{A,B}", "{}", "{{A,B},{C,D}}"} {
		if got, ok := CharContainerExpr(1002, in); ok {
			t.Fatalf("%q 不应改写，得到 %q", in, got)
		}
	}
	if n := takeCharContainerDegradedCount(); n != 0 {
		t.Fatalf("正常数组不应计降级，得到 %d", n)
	}
}

func TestV128CharArrayCustomLowerBound(t *testing.T) {
	defer withCharCatalog(t)()
	takeCharContainerDegradedCount()
	// 自定义下界：ARRAY[...] 恒为 lb=1，PG 无 array_set → 不可表达，降级+计数
	in := "[0:1]={" + string([]byte{0xC3}) + ",A}"
	got, ok := CharContainerExpr(1002, in)
	if ok || got != "" {
		t.Fatalf("自定义下界应降级，得到 %q ok=%v", got, ok)
	}
	if n := takeCharContainerDegradedCount(); n != 1 {
		t.Fatalf("降级计数应为 1，得到 %d", n)
	}
}

func TestV128CharCompositeExpr(t *testing.T) {
	defer withCharCatalog(t)()
	// 顶层复合：(char,int)
	got, ok := CharContainerExpr(80010, "("+string([]byte{0xC3})+",7)")
	want := `ROW((-61)::"char",'7')::"public"."ct1"`
	if !ok || got != want {
		t.Fatalf("复合 = %q ok=%v，期望 %q", got, ok, want)
	}
	// NULL 字段（ct1 是 (c "char", n int)，NULL 在 int 位，char 位是高危字节）
	if got, ok := CharContainerExpr(80010, "("+string([]byte{0xC3})+",)"); !ok ||
		got != `ROW((-61)::"char",NULL)::"public"."ct1"` {
		t.Fatalf("复合含 NULL = %q ok=%v", got, ok)
	}
	// 无高危 → 不改写
	if _, ok := CharContainerExpr(80010, "(A,7)"); ok {
		t.Fatal("普通复合不应改写")
	}
}

func TestV128CharCompositeNested(t *testing.T) {
	defer withCharCatalog(t)()
	// ct2(a "char"[], in_c ct1)：数组字段用 ARRAY[...]，嵌套复合用 ROW(...)::type
	// record 文本里含逗号/括号的字段被 RecordQuote 加引号
	c3 := string([]byte{0xC3})
	innerQuoted := `"(` + c3 + `,7)"`
	arrQuoted := `"{` + c3 + `,A}"`
	in := "(" + arrQuoted + "," + innerQuoted + ")"
	got, ok := CharContainerExpr(80011, in)
	if !ok {
		t.Fatalf("嵌套复合应改写，输入 %q", in)
	}
	if !strings.Contains(got, `ARRAY[(-61)::"char",'A'::"char"]`) {
		t.Fatalf("数组字段未正确改写: %q", got)
	}
	if !strings.Contains(got, `ROW((-61)::"char",'7')::"public"."ct1"`) {
		t.Fatalf("嵌套复合未正确改写: %q", got)
	}
	if !strings.HasSuffix(got, `::"public"."ct2"`) {
		t.Fatalf("顶层类型 cast 缺失: %q", got)
	}
}

func TestV128CharArrayOfComposite(t *testing.T) {
	defer withCharCatalog(t)()
	c3 := string([]byte{0xC3})
	// ct1[]：元素是复合类型，record 文本含逗号 → arrayQuote 加引号
	got, ok := CharContainerExpr(80020, `{"(`+c3+`,7)","(A,8)"}`)
	want := `ARRAY[ROW((-61)::"char",'7')::"public"."ct1",'(A,8)'::"public"."ct1"]`
	if !ok || got != want {
		t.Fatalf("复合数组 = %q ok=%v，期望 %q", got, ok, want)
	}
	// 元素全无高危 → 不改写
	if _, ok := CharContainerExpr(80020, `{"(A,7)","(B,8)"}`); ok {
		t.Fatal("普通复合数组不应改写")
	}
	// 域 over 复合类型：cast 到域名
	if got, ok := CharContainerExpr(80012, `("`+c3+`",7)`); !ok ||
		got != `ROW((-61)::"char",'7')::"public"."d_ct"` {
		t.Fatalf("域复合 = %q ok=%v", got, ok)
	}
}

func TestV128CompositeBaseOID(t *testing.T) {
	defer withCharCatalog(t)()
	cases := []struct{ oid, want uint32 }{
		{80010, 80010}, // 复合本身
		{80012, 80010}, // 域 over 复合 → 基复合
		{18, 0},        // 标量
		{1002, 0},      // 数组
		{80001, 0},     // 域 over 标量
		{99999, 0},     // 未知
	}
	for _, c := range cases {
		if got := compositeBaseOID(c.oid); got != c.want {
			t.Fatalf("compositeBaseOID(%d) = %d，期望 %d", c.oid, got, c.want)
		}
	}
	// 域套域 → 仍能落到复合
	DynamicTypeDefs[80013] = &TypeDef{OID: 80013, Name: "dd_ct", Nsp: 2200, Type: 'd', Base: 80012}
	if got := compositeBaseOID(80013); got != 80010 {
		t.Fatalf("域套域 = %d，期望 80010", got)
	}
}

func TestV128CharContainerRisk(t *testing.T) {
	defer withCharCatalog(t)()
	if !CharContainerRisk(18, string([]byte{0xC3})) {
		t.Fatal("标量应命中风险")
	}
	if !CharContainerRisk(80001, string([]byte{0xC3})) {
		t.Fatal("域标量应命中风险")
	}
	if !CharContainerRisk(1002, "{"+string([]byte{0xC3})+",A}") {
		t.Fatal("数组元素应命中风险")
	}
	if CharContainerRisk(1002, "{A,B}") {
		t.Fatal("普通数组不应命中风险")
	}
	if !CharContainerRisk(80010, "("+string([]byte{0xC3})+",7)") {
		t.Fatal("复合字段应命中风险")
	}
	if CharContainerRisk(80010, "(A,7)") {
		t.Fatal("普通复合不应命中风险")
	}
}
