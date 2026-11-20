// v1.2.29 修复（审计残留 P2）：--ddl 与 --deleted/--only-deleted 的拒绝条件
// 从「任意组合」收窄为「纯 DDL（无 --sql/--data 等数据格式）」。
// --ddl --sql --deleted 是合法组合：DDL 写 .ddl、删除行 INSERT 写 .sql
// （export.go 组合分支）；v1.2.28 的宽条件把它误拒了（v1.2.27 可通过）。
// 审计 #8 要拒的是纯 --ddl --deleted（0 字节 .sql、删行全丢）。
package cli

import "testing"

func TestV1229DDLDeletedCombo(t *testing.T) {
	// 合法：DDL + 数据格式 + 删除行
	mustNotExit(t, []string{"--ddl", "--sql", "--deleted", "-o", "/tmp/x"})
	mustNotExit(t, []string{"--ddl", "--data", "--deleted", "-o", "/tmp/x"})
	mustNotExit(t, []string{"--ddl", "--text", "--only-deleted", "-o", "/tmp/x"})
	// 非法：纯 DDL + 删除行（审计 #8 原目标：无数据输出通道，删行全丢）
	mustExit(t, []string{"--ddl", "--deleted", "-o", "/tmp/x"}, "--ddl")
	mustExit(t, []string{"--ddl", "--only-deleted", "-o", "/tmp/x"}, "--ddl")
	mustExit(t, []string{"--ddl", "--deleted", "--only-deleted", "-o", "/tmp/x"}, "--ddl")
}
