// v1.2.18 回归用例：import 脚本的时区钉住（P2）。
//
// 缺陷现场：`--data --format csv/txt --import-script` 生成的是 shell 脚本，
// \copy 在客户端执行，**没有 SQL 会话头**可挂 SET TIME ZONE；csv/txt 数据文件
// 按设计也不带会话头。而金仓 mysql 模式的 timestamptz 输出本地时间、不带偏移
// （v1.2.16 D7）。于是回灌时按目标端默认时区解释：
//
//	源库 Asia/Shanghai(+08)，目标默认 UTC → 全部 timestamptz 静默平移 8 小时，
//	\copy 成功、行数一致、不报任何错。
//
// v1.2.18 第一版只补了 SQL 路径（sqlSessionHeader），脚本路径漏了。本文件锁死脚本路径。
package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pg2sql/internal/config"
	"pg2sql/internal/datatype"
	"pg2sql/internal/engine"
	"pg2sql/internal/integrity"
)

// setTZ 用 IANA 名设置时区并注册还原
func setTZ(t *testing.T, name string) {
	t.Helper()
	oldLoc, oldSet := datatype.MysqlTZLoc, datatype.MysqlTZSet
	if !datatype.SetMysqlTZByName(name) {
		t.Fatalf("SetMysqlTZByName(%q) 失败（本机缺 tzdata？）", name)
	}
	t.Cleanup(func() { datatype.MysqlTZLoc, datatype.MysqlTZSet = oldLoc, oldSet })
}

// setMysqlMode 临时切换 mysql 兼容模式并注册还原
func setMysqlMode(t *testing.T, on bool) {
	t.Helper()
	old := datatype.IsKbMysqlMode()
	datatype.SetKbMysqlMode(on)
	t.Cleanup(func() { datatype.SetKbMysqlMode(old) })
}

// IANA 名：必须原样写进 PGTZ（不能退化成固定偏移 —— 目标端若有该名字，
// 才能按"当时的历史规则"解释夏令时）
func TestV1218ImportScriptTZExportIANA(t *testing.T) {
	setMysqlMode(t, true)
	setTZ(t, "Asia/Shanghai")

	got := importScriptTZExport()
	if !strings.Contains(got, "export PGTZ=${PGTZ:-Asia/Shanghai}") {
		t.Errorf("脚本必须 export PGTZ 且钉住源库时区名，实际：%q", got)
	}
	if strings.Contains(got, "INTERVAL") || strings.Contains(got, "SET TIME ZONE") {
		t.Errorf("PGTZ 是环境变量，不能写 SET TIME ZONE / INTERVAL 那套 SQL 语法，实际：%q", got)
	}
}

// 固定偏移：PGTZ 要 +HH:MM 形式，无空格、无引号
func TestV1218ImportScriptTZExportFixedOffset(t *testing.T) {
	setMysqlMode(t, true)
	oldLoc, oldOff, oldSet := datatype.MysqlTZLoc, datatype.MysqlTZOffsetUS, datatype.MysqlTZSet
	t.Cleanup(func() {
		datatype.MysqlTZLoc, datatype.MysqlTZOffsetUS, datatype.MysqlTZSet = oldLoc, oldOff, oldSet
	})
	datatype.MysqlTZLoc = nil
	datatype.MysqlTZOffsetUS = 9*3600*1000000 + 30*60*1000000 // +09:30
	datatype.MysqlTZSet = true

	got := importScriptTZExport()
	if !strings.Contains(got, "export PGTZ=${PGTZ:-+09:30}") {
		t.Errorf("固定偏移必须写成 +09:30（PGTZ 不接受 INTERVAL 写法），实际：%q", got)
	}
}

// 反向断言：非 mysql 模式绝不能出现 PGTZ。
// PG 模式的 timestamptz 自带偏移，钉时区反而会改变 v1.2.16 已校准的行为。
func TestV1218ImportScriptTZExportPgModeSilent(t *testing.T) {
	setMysqlMode(t, false)
	setTZ(t, "Asia/Shanghai")

	if got := importScriptTZExport(); got != "" {
		t.Errorf("非 mysql 模式不得输出 PGTZ，实际：%q", got)
	}
}

// 时区未知（!MysqlTZSet）：必须回退 +08:00，与渲染口径同源。
// v1.2.18 R3 修正：原断言"未知时不写"是错的 —— mysqlLocalUS 渲染 timestamptz
// 在同一场景已按默认 +8 落字面量（并告警），SQL 会话头也照写 +08；导出内容
// 已经"承诺"了 +08，脚本再不钉，目标端非 +08 时 \copy 照样静默平移。
// 三条出口必须钉同一个值。
func TestV1218ImportScriptTZExportUnknownTZ(t *testing.T) {
	setMysqlMode(t, true)
	oldLoc, oldOff, oldSet := datatype.MysqlTZLoc, datatype.MysqlTZOffsetUS, datatype.MysqlTZSet
	t.Cleanup(func() {
		datatype.MysqlTZLoc, datatype.MysqlTZOffsetUS, datatype.MysqlTZSet = oldLoc, oldOff, oldSet
	})
	datatype.MysqlTZLoc = nil
	datatype.MysqlTZOffsetUS = 8 * 3600 * 1000000 // 默认回退 +8（保持显式，防全局污染）
	datatype.MysqlTZSet = false

	got := importScriptTZExport()
	if !strings.Contains(got, "export PGTZ=${PGTZ:-+08:00}") {
		t.Errorf("时区未知时必须回退 +08:00（与字面量渲染 / SET TIME ZONE 同源，否则脚本不钉、目标端非 +08 仍平移），实际：%q", got)
	}
}

// 端到端：csv 导入脚本的头部必须含 PGTZ，且位置在 \copy 之前
func TestV1218ImportScriptCSVIncludesTZ(t *testing.T) {
	setMysqlMode(t, true)
	setTZ(t, "Asia/Shanghai")

	dir := t.TempDir()
	out := filepath.Join(dir, "export.csv")
	if err := os.WriteFile(out, []byte("a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tm := &engine.TableMeta{Schema: "public", RelName: "t_ts"}
	o := &config.Options{ImportScript: "csv"}
	makeImportScript(o, tm, out, "csv", []string{"ts"}, "")

	b, err := os.ReadFile(filepath.Join(dir, "import_export.sh"))
	if err != nil {
		t.Fatalf("读脚本失败: %v", err)
	}
	s := string(b)
	if !strings.Contains(s, "export PGTZ=${PGTZ:-Asia/Shanghai}") {
		t.Fatalf("csv 导入脚本必须钉住 PGTZ（否则 timestamptz 静默平移 8 小时）\n%s", s)
	}
	// 注意：注释里也含 "\\copy" 字样，比对必须用真正的执行行
	if i, j := strings.Index(s, "export PGTZ"), strings.Index(s, "-c \"\\copy"); i > j || j < 0 {
		t.Errorf("PGTZ 必须在 \\copy 执行行之前生效\n%s", s)
	}
}

// 端到端：批量 import_all.sh 同样要带 PGTZ
func TestV1218ImportAllScriptIncludesTZ(t *testing.T) {
	setMysqlMode(t, true)
	setTZ(t, "Asia/Shanghai")

	dir := t.TempDir()
	data := filepath.Join(dir, "a.csv")
	if err := os.WriteFile(data, []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	entries := []integrity.ExportReportEntry{
		{Table: "public.a", File: data, Format: "csv", Rows: 1},
	}
	makeImportAllScript(dir, entries, true, ",", nil)

	b, err := os.ReadFile(filepath.Join(dir, "import_all.sh"))
	if err != nil {
		t.Fatalf("读 import_all.sh 失败: %v", err)
	}
	s := string(b)
	if !strings.Contains(s, "export PGTZ=${PGTZ:-Asia/Shanghai}") {
		t.Fatalf("import_all.sh 必须钉住 PGTZ，否则批量回灌的 timestamptz 全部平移\n%s", s)
	}
	if i, j := strings.Index(s, "export PGTZ"), strings.Index(s, "-c \"\\copy"); i > j || j < 0 {
		t.Errorf("PGTZ 必须在第一个 \\copy 执行行之前生效\n%s", s)
	}
}

// 反向断言：PG 模式的批量脚本不得出现 PGTZ（v1.2.17 逐字节等价）
func TestV1218ImportAllScriptPgModeNoTZ(t *testing.T) {
	setMysqlMode(t, false)
	setTZ(t, "Asia/Shanghai")

	dir := t.TempDir()
	data := filepath.Join(dir, "a.csv")
	if err := os.WriteFile(data, []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	entries := []integrity.ExportReportEntry{
		{Table: "public.a", File: data, Format: "csv", Rows: 1},
	}
	makeImportAllScript(dir, entries, true, ",", nil)

	b, err := os.ReadFile(filepath.Join(dir, "import_all.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "PGTZ") {
		t.Errorf("非 mysql 模式的脚本不得出现 PGTZ（会改变 v1.2.16 已校准的 PG 行为）\n%s", b)
	}
}

// 偏移格式化边界：负偏移、零偏移
func TestV1218TZEnvValueShape(t *testing.T) {
	oldLoc, oldSet := datatype.MysqlTZLoc, datatype.MysqlTZSet
	t.Cleanup(func() { datatype.MysqlTZLoc, datatype.MysqlTZSet = oldLoc, oldSet })

	datatype.MysqlTZLoc = nil
	datatype.MysqlTZSet = true

	datatype.MysqlTZOffsetUS = -5 * 3600 * 1000000
	if got := datatype.MySQLLocalTZEnvValue(); got != "-05:00" {
		t.Errorf("负偏移格式错误：got %q, want -05:00", got)
	}
	datatype.MysqlTZOffsetUS = 0
	if got := datatype.MySQLLocalTZEnvValue(); got != "+00:00" {
		t.Errorf("零偏移格式错误：got %q, want +00:00", got)
	}
}
