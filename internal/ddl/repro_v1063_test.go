//go:build repro

// v1.0.62 深度审计复现用例（ddl 侧）
// 断言写的是【正确行为】，当前版本应当是 FAIL。
package ddl

import (
	"regexp"
	"strings"
	"testing"
)

// A-1：RETURNS TABLE 的列定义必须是「列名 类型」，只给类型是非法语法
// （实测 CREATE FUNCTION x(int) RETURNS TABLE (int4) → syntax error at or near ")"）
func TestReproA1TableColumnNeedsName(t *testing.T) {
	sc := &SchemaCatalog{
		langNames: map[uint32]string{14: "sql"},
		ProcRows: []procRow{{
			Schema: "public", Name: "f_tbl",
			ArgTypes: []uint32{23, 23, 25}, // 对应 pg_proc 的 proallargtypes
			ArgModes: []byte{'i', 't', 't'},
			RetType:  2249, RetSet: true, Kind: 'f', Volatile: 'v',
			Language: 14, Body: "SELECT 1, 'x'::text",
		}},
	}
	out := GenFullSchemaDDL(sc, map[string]bool{"function": true})
	m := regexp.MustCompile(`RETURNS TABLE \(([^)]*)\)`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("没有生成 RETURNS TABLE 子句，实际输出：\n%s", out)
	}
	for _, col := range strings.Split(m[1], ", ") {
		if len(strings.Fields(col)) < 2 {
			t.Fatalf("RETURNS TABLE 列 %q 缺少列名（应为「列名 类型」），整段 DDL 回灌语法错误：\n%s", col, out)
		}
	}
}

// B-12：public 下的触发器函数名不加引号 → 'MyTrig' 被折成 mytrig
// （实测回灌：ERROR: function mytrig() does not exist）
func TestReproB12TriggerFuncQuoted(t *testing.T) {
	sc := &SchemaCatalog{
		TrigRows: []triggerRow{{
			Schema: "public", Table: "t", FuncSchema: "public", Func: "MyTrig",
			Name: "trg1", TGType: 0x0005, // BEFORE INSERT ROW
		}},
	}
	out := GenFullSchemaDDL(sc, map[string]bool{"trigger": true})
	if !strings.Contains(out, `"MyTrig"()`) {
		t.Fatalf("触发器函数名未加引号，回灌会解析成小写 mytrig：\n%s", out)
	}
}

// B-11：SetDomainChecks(nil) 会把 nil 归一成空 map，
// 于是 "DomainCheckSnapshot() == nil"（目录缺失判据）永不成立 → 提示是死代码
func TestReproB11DomainSnapshotNeverNil(t *testing.T) {
	SetDomainChecks(nil)
	if DomainCheckSnapshot() != nil {
		t.Fatal("SetDomainChecks(nil) 之后快照非 nil：\"目录缺失\"判据失效，域 CHECK 丢约束时不再提示")
	}
}
