package ddl

// v1.0.63 第 4 轮修复回归（D-1 扩展成员过滤 / D-2 C 函数 AS 子句）。
// 只覆盖纯函数层（不扫磁盘）：虚拟实例上没有装 pageinspect 的目录可读。

import (
	"strings"
	"testing"

	"pg2sql/internal/catalog"
)

// TestV1063CProcAsClause —— D-2：LANGUAGE c 必须输出「'共享库', '符号名'」。
// 旧实现只输出符号名，PG 把它当共享库路径 → could not access file "brin_metapage_info"。
func TestV1063CProcAsClause(t *testing.T) {
	got := procAsClause("c", "$libdir/pageinspect", "brin_metapage_info", "brin_metapage_info", "$q$")
	want := "'$libdir/pageinspect', 'brin_metapage_info'"
	if got != want {
		t.Fatalf("C 函数 AS 子句不对\n got=%s\nwant=%s", got, want)
	}
}

// TestV1063CProcAsClause符号为空时退回函数名
func TestV1063CProcAsClauseEmptySymbol(t *testing.T) {
	got := procAsClause("c", "$libdir/x", "", "myfunc", "$q$")
	if !strings.Contains(got, "'myfunc'") {
		t.Fatalf("prosrc 为空时应退回函数名，得到 %s", got)
	}
}

// TestV1063CProcAsClauseQuoteEscape —— 库名/符号名含单引号不能破句
func TestV1063CProcAsClauseQuoteEscape(t *testing.T) {
	got := procAsClause("c", "$libdir/a'b", "sym'bol", "x", "$q$")
	if got != "'$libdir/a''b', 'sym''bol'" {
		t.Fatalf("转义结果不对: %s", got)
	}
}

// TestV1063SQLProcAsClause —— 非 C 语言保持美元引号包裹的函数体
func TestV1063SQLProcAsClause(t *testing.T) {
	got := procAsClause("plpgsql", "", "BEGIN RETURN 1; END", "f", "$pg2sql$")
	want := "$pg2sql$\nBEGIN RETURN 1; END\n$pg2sql$"
	if got != want {
		t.Fatalf("plpgsql AS 子句不对\n got=%q\nwant=%q", got, want)
	}
}

// TestV1063ExtMemberLookup —— D-1：扩展成员判定
func TestV1063ExtMemberLookup(t *testing.T) {
	sc := &SchemaCatalog{extMembers: map[catalog.ExtKey]bool{
		{ClassID: catalog.ExtClassProc, ObjID: 12345}: true,
	}}
	if !sc.extMember(catalog.ExtClassProc, 12345) {
		t.Fatal("扩展成员函数应被识别")
	}
	if sc.extMember(catalog.ExtClassProc, 999) {
		t.Fatal("非成员对象不应被误判")
	}
	// 目录未读到时（空集合）必须一律 false，保持旧行为不误删对象
	empty := &SchemaCatalog{}
	if empty.extMember(catalog.ExtClassProc, 12345) {
		t.Fatal("pg_depend 未读到时应保持旧行为（不跳过任何对象）")
	}
}
