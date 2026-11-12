// 目标库自带、无需（也不应）在回灌时显式创建的 schema 判定。
//
// v1.2.18 修复（P2）：'sys' / 'pg_' 裸前缀过宽。
//
// 历史：v1.2.17 为修「非默认 schema 漏建」，在 app.isDefaultSchemaName 与
// ddl.CreateSchemaStatements 各自写了一份 `HasPrefix(n, "sys") || HasPrefix(n, "pg_")`
// 的判断。而金仓/ PG 的系统 schema 名单其实是精确的（见 integrity.IsSystemSchemaTable），
// 于是用户自建 `syslog` / `system` / `sysutils` / `pg_mine` 这类 schema 被误判为系统：
// 表照常被导出（integrity 不认为是系统表），但 DDL 头部不生成 CREATE SCHEMA，
// 空库回灌第一条 `CREATE TABLE "syslog".t` 就报 schema does not exist，整表建不出来。
//
// ---------------------------------------------------------------------------
// 本函数与 integrity.IsSystemSchemaTable 的口径**故意不同**，不要"统一"：
//
//	integrity.IsSystemSchemaTable（坏块归属）：宁可**多认**系统
//	    少认 → 系统表坏块计入用户 verdict → 假阳性 fail
//	    所以它接受 sys_ / sys_toast / pg_toast / SYS_HM 之类前缀泛化
//
//	IsBuiltinSchemaForCreate（决定建不建 schema）：宁可**少认**系统
//	    多认 → 用户 schema 漏建 → 回灌整表失败（本函数要修的就是这个）
//	    所以它只认精确名单
//
// 两处偏好方向相反，合并成同一个宽松实现会把本缺陷固化回来。
// ---------------------------------------------------------------------------
package ddl

import "strings"

// builtinSchemaNames：目标库一定自带、回灌时无需创建的 schema（精确名单）。
//
// 只收"任何 PG / 金仓实例都会存在"的 schema。刻意**不含**：
//   - sys_ / SYS_HM 等前缀族：金仓确实用它们，但这些 schema 下的对象
//     已被 app.isSystemSchema 过滤出导出任务集，根本不会走到这里；
//     若按前缀放行，反而会吞掉用户的 syslog / sysutils。
//   - pg_ 前缀泛化：只认 pg_catalog / pg_toast（+ pg_toast_temp_N 临时族，
//     由 isPgTempSchema 处理），不认 pg_mine。
//
// v1.2.18 补漏：此前注释声称含 pg_toast，名单里却没有。`--all-tables` 会把系统
// 对象纳入任务集（batch.go 的过滤条件是 `!o.AllTables`），此时
// IsBuiltinSchemaForCreate("pg_toast")==false → DDL 头部生成
// `CREATE SCHEMA pg_toast`，PG/金仓都直接拒绝（保留前缀）。实际影响有限
// （系统表 DDL 本来也几乎不可回灌），但注释与实现矛盾必须消除。
//
// v1.2.18 R3：**刻意不含** anon / src_restrict（曾有一版加入，已撤）。
// 它们只在装了对应组件的金仓实例存在，违反本名单"任何实例都有"的准入原则：
// 若源库 anon 是用户 schema（V8R6 未必内置该组件），不生成 CREATE SCHEMA 会让
// 空库回灌报 schema "anon" does not exist —— 与本文件要修的 syslog 缺陷同类。
// 方向判断：CREATE SCHEMA 用的是 IF NOT EXISTS（schema_ddl.go），"多建"在目标端
// 已存在时无害；"少建"才是硬失败。PG 目标对 anon / src_restrict 也无保留限制，
// 建得出来。pg_toast 除外：pg_ 保留前缀，PG/金仓都拒绝用户创建，必须留在名单里。
func builtinSchemaNames() map[string]bool {
	return map[string]bool{
		"public":             true, // 所有 PG/金仓库的默认 schema
		"pg_catalog":         true, // 系统目录
		"pg_toast":           true, // TOAST 主 schema（保留名，目标端建不出来）
		"information_schema": true, // 标准信息模式（保留名）
		"sys":                true, // 金仓系统 schema
		"sys_catalog":        true, // 金仓系统目录
		"sysaudit":           true, // 金仓审计
		"sysmac":             true, // 金仓强制访问控制
		"sys_toast":          true, // 金仓 TOAST
		"kdb_schedule":       true, // 金仓定时任务
	}
}

// IsBuiltinSchemaForCreate：该 schema 是否由目标库自带，回灌时不需要 CREATE SCHEMA。
//
// 空串按 true 处理（避免上游没取到 schema 名时生成 `CREATE SCHEMA ""`）。
func IsBuiltinSchemaForCreate(name string) bool {
	if name == "" {
		return true
	}
	if builtinSchemaNames()[name] {
		return true
	}
	// 临时 schema：pg_toast_temp_N / pg_temp_N，会话级存在，不需要建。
	// 只放行严格形态（下划线 + 数字），不做 pg_ 泛化。
	if isPgTempSchema(name) {
		return true
	}
	return false
}

// isPgTempSchema：pg_temp_N / pg_toast_temp_N。
func isPgTempSchema(name string) bool {
	for _, pre := range []string{"pg_temp_", "pg_toast_temp_"} {
		if !strings.HasPrefix(name, pre) {
			continue
		}
		rest := name[len(pre):]
		if rest == "" {
			return false
		}
		for _, c := range rest {
			if c < '0' || c > '9' {
				return false
			}
		}
		return true
	}
	return false
}
