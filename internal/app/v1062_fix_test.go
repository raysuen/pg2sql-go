// Author: raysuen
package app

import (
	"os"
	"path/filepath"
	"pg2sql/internal/config"
	"pg2sql/internal/integrity"
	"testing"
)

// TestV1062BatchDDLOnly：批量 DDL-only 放开（旧校验只认 --sql/--data，
// `--schema s --ddl --full-schema` 被直接拒绝，批量建表 DDL 无法产出）。
func TestV1062BatchDDLOnly(t *testing.T) {
	if !rowOutputType(&config.Options{DDL: true}) {
		t.Fatalf("--ddl 单独应被视为有效输出类型（批量 DDL-only）")
	}
	if !rowOutputType(&config.Options{SQL: true}) || !rowOutputType(&config.Options{Data: true}) {
		t.Fatalf("--sql/--data 仍应有效")
	}
	if rowOutputType(&config.Options{}) {
		t.Fatalf("无任何输出类型时不应视为有效")
	}
	// 仅辅助产物（--full-schema/--large-objects/--roles）时应允许进入批量入口
	o := &config.Options{FullSchema: true, DDL: true}
	if !rowOutputType(o) && !(o.FullSchema || o.LargeObjects || o.Roles) {
		t.Fatalf("辅助产物组合应被允许")
	}
}

// TestV1062SystemSchemaParity：系统 schema 口径统一（app 批量过滤 vs 报告坏块归属）。
// 两处名单不一致会让系统表坏块计入用户表 verdict（假阳性 fail）。
func TestV1062SystemSchemaParity(t *testing.T) {
	sys := []string{
		"pg_catalog.pg_class", "information_schema.tables", "sys.sys_table",
		"sys_catalog.sys_class", "pg_toast.pg_toast_12345", "sys_toast.sys_toast_1",
		"kdb_schedule.job", "anon.t", "src_restrict.t", "sys_audit.log", "SYS_HM_META.t",
	}
	for _, n := range sys {
		if !isSystemSchema(n) {
			t.Errorf("app.isSystemSchema(%q) 应为系统对象", n)
		}
		if !integrity.IsSystemSchemaTable(n) {
			t.Errorf("integrity.IsSystemSchemaTable(%q) 应为系统对象（两处口径必须一致）", n)
		}
	}
	user := []string{"public.t", "app.orders", `"MySchema".t`}
	for _, n := range user {
		if isSystemSchema(n) {
			t.Errorf("app.isSystemSchema(%q) 不应判为系统", n)
		}
		if integrity.IsSystemSchemaTable(n) {
			t.Errorf("integrity.IsSystemSchemaTable(%q) 不应判为系统", n)
		}
	}
}

// TestV1062ResolveDbDirNoWrite：P2-3 回归——resolveDbDir 不得回写共享 Options。
// 批量 --jobs>1 时多个 worker 共用 *config.Options，读函数里写 o.DBOID 是数据竞态，
// 且 worker A 推导出的 dboid 可能被 worker B 覆盖 → 按错误库目录定位。
func TestV1062ResolveDbDirNoWrite(t *testing.T) {
	root := t.TempDir()
	rel := filepath.Join(root, "pg_tblspc", "1663", "PG_17_202406281", "16385", "12345")
	if err := os.MkdirAll(filepath.Dir(rel), 0755); err != nil {
		t.Fatalf("建临时表空间目录失败: %v", err)
	}
	if err := os.WriteFile(rel, []byte("x"), 0644); err != nil {
		t.Fatalf("建临时文件失败: %v", err)
	}
	o := &config.Options{DataFile: rel}
	got := resolveDbDir(o)
	want := filepath.Join(root, "base", "16385")
	if got != want {
		t.Fatalf("resolveDbDir = %q, want %q", got, want)
	}
	if o.DBOID != 0 {
		t.Fatalf("resolveDbDir 不应回写 o.DBOID，got %d（并发场景即数据竞态）", o.DBOID)
	}
	// config.ResolveDataRoot 同样不得回写
	o2 := &config.Options{DataFile: rel}
	if r := config.ResolveDataRoot(o2); r != root {
		t.Fatalf("ResolveDataRoot = %q, want %q", r, root)
	}
	if o2.DBOID != 0 {
		t.Fatalf("ResolveDataRoot 不应回写 o.DBOID，got %d", o2.DBOID)
	}
	// 需要 dboid 的调用方走双返回值版本
	root2, dboid := config.ResolveDataRootAndDBOID(&config.Options{DataFile: rel})
	if root2 != root || dboid != 16385 {
		t.Fatalf("ResolveDataRootAndDBOID = (%q,%d), want (%q,16385)", root2, dboid, root)
	}
}
