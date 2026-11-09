// v1.2.7 测试（F1）：DDL 类型名对 oid 18/1002 强制加引号。
package app

import (
	"pg2sql/internal/engine"
	"testing"
)

func TestV127TypeDisplayNameCharQuoting(t *testing.T) {
	tm := &engine.TableMeta{}
	cases := []struct {
		oid  uint32
		want string
	}{
		{18, `"char"`},      // pg_type "char"：内部单字节类型，必须加引号
		{1002, `"char"[]`},  // 其数组
		{1042, "character"}, // bpchar：绝不能加引号，且不能被 18 的规则误伤
		{1043, "character varying"},
		{25, "text"},
		{23, "integer"},
	}
	for _, c := range cases {
		if got := typeDisplayName(tm, c.oid); got != c.want {
			t.Errorf("typeDisplayName(%d) = %q，期望 %q", c.oid, got, c.want)
		}
	}
}

func TestV127ColTypeSQLCharQuoting(t *testing.T) {
	tm := &engine.TableMeta{}
	// 标量列：裸 char 会被 PG 解析成 character(1)，是静默降级的根源
	if got := colTypeSQL(tm, engine.ColumnDef{Name: "c", TypeOID: 18, TypMod: -1}); got != `"char"` {
		t.Errorf(`colTypeSQL("char") = %q，期望 "char"`, got)
	}
	// 数组列：char[] 同理（1014 character(1)[]）
	if got := colTypeSQL(tm, engine.ColumnDef{Name: "cs", TypeOID: 1002, TypMod: -1}); got != `"char"[]` {
		t.Errorf(`colTypeSQL("char"[]) = %q，期望 "char"[]`, got)
	}
	// bpchar(3) 必须保持 character(3)：typmod 4+3
	if got := colTypeSQL(tm, engine.ColumnDef{Name: "b", TypeOID: 1042, TypMod: 7}); got != "character(3)" {
		t.Errorf("colTypeSQL(bpchar(3)) = %q，期望 character(3)", got)
	}
}

// 域基类型走同一个 typeDisplayName，一并验证签名语义
func TestV127DomainBaseCharQuoting(t *testing.T) {
	tm := &engine.TableMeta{}
	if got := typeDisplayName(tm, 18); got != `"char"` {
		t.Fatalf("域基类型 char 输出 = %q，期望 \"char\"", got)
	}
}
