// v1.2.12 参数测试：--fields 与 --no-complete-insert 互斥（A3）。
// 作者: raysuen
package cli

import "testing"

// TestV1212FieldsRequiresColumnList：v1.2.12（A3）回归。
//
// 背景：buildFmtCtx 的 colStr 只由 completeInsert 决定，与 --fields 裁剪无关。
// `--fields a,b --no-complete-insert` 对 3 列表产出 `INSERT INTO t VALUES (a,b)`，
// VALUES 个数与表列数不符，回灌必然报错，而旧实现零提示。现解析期拦下。
func TestV1212FieldsRequiresColumnList(t *testing.T) {
	mustExit(t, []string{"f", "--sql", "--fields", "a,b", "--no-complete-insert"}, "--fields")
	mustExit(t, []string{"f", "--format", "sql", "--fields", "a,b", "--no-complete-insert"}, "--fields")
}

// TestV1212FieldsWithColumnListOK：保留列清单（默认 complete-insert）时必须放行。
func TestV1212FieldsWithColumnListOK(t *testing.T) {
	mustNotExit(t, []string{"f", "--sql", "--fields", "a,b"})
	mustNotExit(t, []string{"f", "--sql", "--fields", "a,b", "--complete-insert"})
}

// TestV1212FieldsCsvNotAffected：csv/text 无列清单概念，--no-complete-insert
// 与 --fields 组合不得误报（列清单只对 SQL INSERT 有意义）。
func TestV1212FieldsCsvNotAffected(t *testing.T) {
	mustNotExit(t, []string{"f", "--data", "--fields", "a,b", "--no-complete-insert"})
	mustNotExit(t, []string{"f", "--format", "csv", "--fields", "a,b", "--no-complete-insert"})
	mustNotExit(t, []string{"f", "--format", "text", "--fields", "a,b", "--no-complete-insert"})
}

// TestV1212NoFieldsCompleteInsertOff：不带 --fields 时全列 VALUES 与表列数一致，
// --no-complete-insert 仍是合法组合（既有行为不得回归）。
func TestV1212NoFieldsCompleteInsertOff(t *testing.T) {
	o := ParseArgs([]string{"f", "--sql", "--no-complete-insert"})
	if o.CompleteInsert {
		t.Fatalf("--no-complete-insert 未生效")
	}
}
