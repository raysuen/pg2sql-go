// v1.2.7 测试（F1）：函数签名/域基类型的类型名对 oid 18/1002 强制加引号。
package ddl

import "testing"

func TestV127ProcTypeNameCharQuoting(t *testing.T) {
	sc := &SchemaCatalog{typeNames: map[uint32]string{
		18:   "char",
		1002: "_char",
		1042: "bpchar",
		1043: "varchar",
		25:   "text",
	}}
	cases := []struct {
		oid  uint32
		want string
	}{
		{18, `"char"`},
		{1002, `"char"[]`},
		{25, "text"},     // 普通内置类型仍裸名
		{1042, "bpchar"}, // 不能被 18 的规则牵连
	}
	for _, c := range cases {
		if got := procTypeName(sc, c.oid); got != c.want {
			t.Errorf("procTypeName(%d) = %q，期望 %q", c.oid, got, c.want)
		}
	}
}
