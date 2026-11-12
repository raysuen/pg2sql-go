// v1.2.15：regtype/regprocedure 的引号语义对齐 PG format_type_be。
//
// 缺陷背景（P1，v1.2.13/v1.2.14 均存在）：
//   - regtype 对类型名无差别走 regQuoteIdent，含空格的标准名被包成
//     "character varying"（PG 里该写法 does not exist，回灌必失败）；
//   - "char"(18) 未在别名表内，输出裸 char，回灌静默落到 bpchar(1)；
//   - varbit(1562) 缺别名，输出 varbit 而非 bit varying（文本与源库不一致）；
//   - regprocedure 的参数类型走另一条路径（裸名），与 regtype 行为不一致，
//     且 "char" 同样丢引号。
//
// 期望值全部来自 PG 17.6 实机 `oid::regtype::text` / `oid::regprocedure::text`，
// 不是照 PG 源码纸面推断（例：2276 any 真机输出带引号的 "any"）。
package datatype

import (
	"testing"
)

// v1215Setup 注入 pg_type / pg_namespace 快照，返回还原函数。
func v1215Setup(t *testing.T) func() {
	t.Helper()
	oldNsp, oldDefs := regNamespaceNames, DynamicTypeDefs
	oldDefault := DefaultRegNamespace
	DefaultRegNamespace = "public"

	regNamespaceNames = map[uint32]string{
		11:    "pg_catalog",
		2200:  "public",
		90000: "MyS", // 大写 schema：必须引号
	}
	DynamicTypeDefs = map[uint32]*TypeDef{
		// 内置：多词标准名
		1043: {OID: 1043, Name: "varchar", Nsp: 11},
		701:  {OID: 701, Name: "float8", Nsp: 11},
		1562: {OID: 1562, Name: "varbit", Nsp: 11},
		1560: {OID: 1560, Name: "bit", Nsp: 11},
		1186: {OID: 1186, Name: "interval", Nsp: 11},
		1700: {OID: 1700, Name: "numeric", Nsp: 11},
		1114: {OID: 1114, Name: "timestamp", Nsp: 11},
		114:  {OID: 114, Name: "json", Nsp: 11},
		199:  {OID: 199, Name: "_json", Nsp: 11, Elem: 114},
		// 内置：需要引号的特殊名
		18:   {OID: 18, Name: "char", Nsp: 11},
		2276: {OID: 2276, Name: "any", Nsp: 11},
		1042: {OID: 1042, Name: "bpchar", Nsp: 11},
		23:   {OID: 23, Name: "int4", Nsp: 11},
		25:   {OID: 25, Name: "text", Nsp: 11},
		2279: {OID: 2279, Name: "trigger", Nsp: 11},
		// 数组（归属按元素类型）
		1015: {OID: 1015, Name: "_varchar", Nsp: 11, Elem: 1043},
		1002: {OID: 1002, Name: "_char", Nsp: 11, Elem: 18},
		1022: {OID: 1022, Name: "_float8", Nsp: 11, Elem: 701},
		1115: {OID: 1115, Name: "_timestamp", Nsp: 11, Elem: 1114},
		1000: {OID: 1000, Name: "_bool", Nsp: 11, Elem: 16},
		// 自定义：含空格 / 关键字 / 大写
		90001: {OID: 90001, Name: "My Type", Nsp: 90000},
		90002: {OID: 90002, Name: "char", Nsp: 90000},
		90003: {OID: 90003, Name: "mytype", Nsp: 90000},
		90004: {OID: 90004, Name: "_mytype", Nsp: 90000, Elem: 90003},
	}
	return func() {
		regNamespaceNames, DynamicTypeDefs = oldNsp, oldDefs
		DefaultRegNamespace = oldDefault
	}
}

// TestV1215RegtypeQuoting —— regtype 输出必须与 PG regtypeout 逐字符一致。
func TestV1215RegtypeQuoting(t *testing.T) {
	defer v1215Setup(t)()

	cases := []struct {
		oid  uint32
		want string
		note string
	}{
		{1043, "character varying", "P1：多词标准名不得加引号"},
		{701, "double precision", "P1：同上"},
		{1114, "timestamp without time zone", "P1：同上（含 4 个词）"},
		{1562, "bit varying", "P2：varbit 别名缺失，PG 输出 bit varying"},
		{1560, "bit", "BIT 是关键字，但 hard-wired 不加引号"},
		{1186, "interval", "INTERVAL 是关键字，hard-wired 不加引号"},
		{1700, "numeric", "NUMERIC 是关键字，hard-wired 不加引号"},
		{1042, "character", "bpchar 无 typmod → character"},
		{18, `"char"`, "P1：char 必须加引号，否则回灌落 bpchar(1)（PG 靠关键字规则而非 hard-wire）"},
		{2276, `"any"`, "保留字命名的伪类型：真机输出 \"any\""},
		{2279, "trigger", "TRIGGER 属 unreserved，PG 不加引号"},
		{23, "integer", "int4 → integer"},
		{25, "text", "普通标识符裸写"},
		{114, "json", "JSONOID 在 PG 里是 hard-wired；json 是关键字，漏收录会被误加引号"},
		{199, "json[]", "json 数组"},
		// 数组
		{1015, "character varying[]", "P1：数组后缀不得让整串被引号包住"},
		{1002, `"char"[]`, "P1：\"char\"[] 保留元素引号"},
		{1022, "double precision[]", "P1：同上"},
		{1115, "timestamp without time zone[]", "P1：同上"},
		// 自定义
		{90001, `"MyS"."My Type"`, "含空格的类型名：schema 与名字各自引号"},
		{90002, `"MyS"."char"`, "关键字的类型名：必须引号（真机 \"MyS\".\"char\"）"},
		{90003, `"MyS".mytype`, "普通小写名：schema 大写需引号、名字裸写"},
		{90004, `"MyS".mytype[]`, "自定义数组"},
		{0, "-", "InvalidOid 约定"},
	}
	for _, c := range cases {
		if got := decodeRegtype(oidBytes(t, c.oid)); got != c.want {
			t.Errorf("decodeRegtype(%d) = %q，期望 %q（%s）", c.oid, got, c.want, c.note)
		}
	}
}

// TestV1215RegArgTypeName —— regprocedure/regoperator 的参数类型走同一套规则。
func TestV1215RegArgTypeName(t *testing.T) {
	defer v1215Setup(t)()

	cases := []struct {
		oid  uint32
		want string
	}{
		{18, `"char"`},
		{1562, "bit varying"},
		{1043, "character varying"},
		{701, "double precision"},
		{23, "integer"},
		{1002, `"char"[]`},
		{1015, "character varying[]"},
		{90001, `"MyS"."My Type"`},
		{90002, `"MyS"."char"`},
	}
	for _, c := range cases {
		if got := regArgTypeName(c.oid); got != c.want {
			t.Errorf("regArgTypeName(%d) = %q，期望 %q", c.oid, got, c.want)
		}
	}
}

// TestV1215QuoteIdent —— quote_identifier 等价性（含关键字与 $ 字符）。
func TestV1215QuoteIdent(t *testing.T) {
	cases := []struct{ in, want string }{
		{"text", "text"},
		{"mytype", "mytype"},
		{"_x1", "_x1"},
		{"char", `"char"`},       // col_name 关键字
		{"any", `"any"`},         // reserved
		{"left", `"left"`},       // type_func_name
		{"table", `"table"`},     // reserved
		{"trigger", "trigger"},   // unreserved：PG 不加引号
		{"MyType", `"MyType"`},   // 大写
		{"a$b", `"a$b"`},         // PG 视 $ 为非法裸写字符
		{"My Type", `"My Type"`}, // 空格
		{"a\"b", `"a""b"`},       // 内嵌双引号要翻倍
		{"1abc", `"1abc"`},       // 数字开头
		{"", `""`},               // 空串
	}
	for _, c := range cases {
		if got := regQuoteIdent(c.in); got != c.want {
			t.Errorf("regQuoteIdent(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}
