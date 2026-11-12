// v1.2.18 回归用例：sys / pg_ 裸前缀误判导致用户 schema 漏建（P2）。
//
// 缺陷现场：用户建 CREATE SCHEMA syslog + CREATE TABLE syslog.t(id int)。
//   - integrity.IsSystemSchemaTable("syslog.t") 为 false → 表被正常纳入导出；
//   - 但 v1.2.17 的 isDefaultSchemaName 用 HasPrefix(n, "sys") → 判为系统 schema →
//     DDL 头部不生成 CREATE SCHEMA → 空库回灌第一条就报
//     ERROR: schema "syslog" does not exist，整表建不出来。
//
// system / sysutils / pg_mine 同理（pg_ 前缀同构）。
package ddl

import (
	"strings"
	"testing"

	"pg2sql/internal/catalog"
)

func TestV1218BuiltinSchemaForCreate(t *testing.T) {
	// 必须判为"自带，不需要 CREATE SCHEMA"
	for _, n := range []string{
		"", "public", "pg_catalog", "information_schema",
		"sys", "sys_catalog", "sysaudit", "sysmac", "sys_toast", "kdb_schedule",
		"pg_toast", // v1.2.18 补漏：注释声称含、名单曾漏（pg_ 保留名，目标端建不出来）
		"pg_temp_3", "pg_toast_temp_3",
	} {
		if !IsBuiltinSchemaForCreate(n) {
			t.Errorf("IsBuiltinSchemaForCreate(%q) 应为 true（目标库自带）", n)
		}
	}

	// 必须判为"用户 schema，需要 CREATE SCHEMA"
	// 前四个是本缺陷的直接现场；后面是反向断言，防止收紧过头。
	for _, n := range []string{
		"syslog", "system", "sysutils", "sysdata", // sys 前缀误判
		"pg_mine", "pgx", // pg_ 前缀同构
		"My Sch", "zzprobe", "public2", // 普通用户 schema
		"sys_audit",            // 金仓确实用，但导出侧已被 isSystemSchema 过滤，这里不得放行
		"pg_temp",              // 不是 pg_temp_N 形态
		"pg_temp_abc",          // 非纯数字后缀
		"anon", "src_restrict", // v1.2.18 R3：撤出名单 —— 仅装了对应组件的实例才有，
		// 源库用户 schema 同名时漏建会回灌失败；IF NOT EXISTS 保证多建无害
	} {
		if IsBuiltinSchemaForCreate(n) {
			t.Errorf("IsBuiltinSchemaForCreate(%q) 应为 false（用户 schema 必须建）", n)
		}
	}
}

// CreateSchemaStatements 端到端：用户 syslog 必须出现在 CREATE SCHEMA 列表里，
// 系统 schema 不得出现。
func TestV1218CreateSchemaStatementsSysPrefix(t *testing.T) {
	sc := &SchemaCatalog{
		SeqRows:  []*catalog.SequenceInfo{{Schema: "syslog", Name: "s"}},
		ProcRows: []procRow{{Schema: "system", Name: "f"}},
		ViewRows: []viewRow{{Schema: "pg_mine", Name: "v"}},
	}
	out := CreateSchemaStatements(sc)
	for _, want := range []string{`"syslog"`, `"system"`, `"pg_mine"`} {
		if !strings.Contains(out, "CREATE SCHEMA IF NOT EXISTS "+want) {
			t.Errorf("输出缺少 CREATE SCHEMA %s\n实际输出：\n%s", want, out)
		}
	}
	// 系统 schema 不得被建（否则回灌报 already exists / 权限不足）
	for _, bad := range []string{`"sys"`, `"pg_catalog"`, `"public"`, `"sysaudit"`} {
		if strings.Contains(out, "CREATE SCHEMA IF NOT EXISTS "+bad) {
			t.Errorf("输出不应包含 CREATE SCHEMA %s\n实际输出：\n%s", bad, out)
		}
	}
}
