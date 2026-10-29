// Author: raysuen
package app

import (
	"fmt"
	"os"
	"path/filepath"
	"pg2sql/internal/config"
	"pg2sql/internal/engine"
	"pg2sql/internal/integrity"
	"pg2sql/internal/logx"
	"strings"
)

// makeImportScript：生成单表导入辅助脚本（--import-script 非 none 时调用）
// sql → psql/ksql -f；csv → \copy（--fields 联动列清单）；auto 按导出类型决定
func makeImportScript(o *config.Options, tm *engine.TableMeta, outPath, fileType string, fields []string, ddlFile string) {
	mode := o.ImportScript
	if mode == "none" || mode == "auto" && fileType != "sql" && fileType != "csv" {
		return
	}
	// B1 修复：auto 按实际导出类型选择导入方式（csv→\copy、sql→psql -f），
	// 避免对 CSV 生成 psql -f 脚本（CSV 被当 SQL 执行必然失败）
	if mode == "auto" {
		if fileType == "csv" {
			mode = "csv"
		} else {
			mode = "sql"
		}
	}
	if mode != "sql" && mode != "csv" {
		return
	}
	db := "postgres"
	name := filepath.Base(outPath)
	base := strings.TrimSuffix(name, "."+fileType)
	if base == "" {
		base = strings.ReplaceAll(tm.FullName(), ".", "_")
	}
	sh := filepath.Join(filepath.Dir(outPath), "import_"+base+".sh")
	// v1.0.48：DDL 独立成 .ddl 后，导入脚本需先建表再灌数据
	ddlStep := ""
	if ddlFile != "" {
		ddlStep = "\"$PSQL\" -d \"$DB\" -v ON_ERROR_STOP=1 -f '" + filepath.Base(ddlFile) + "'\n"
	}
	// B2/B3 修复：导出文件恒为 UTF-8（ToData/SQL 输出均为 UTF-8），
	// 脚本 PGCLIENTENCODING 默认与文件一致（用户可覆盖），避免源库编码（GBK/EUC_CN 等）注入造成双重转换
	var body string
	if mode == "sql" {
		body = fmt.Sprintf(`#!/bin/sh
# pg2sql 导入辅助脚本 (v1.0.38) — 自动生成，勿手改
# 用法: sh %[1]s [数据库名]
# 环境变量可覆盖: PSQL(默认 psql，金仓设 PSQL=ksql)  DB  PGCLIENTENCODING
DB=${DB:-${1:-%[2]s}}
PSQL=${PSQL:-psql}
export PGCLIENTENCODING=${PGCLIENTENCODING:-%[3]s}
%[5]s"$PSQL" -d "$DB" -v ON_ERROR_STOP=1 -f '%[4]s'
`, filepath.Base(sh), db, "UTF8", name, ddlStep)
	} else {
		cols := ""
		if len(fields) > 0 {
			q := make([]string, len(fields))
			for i, f := range fields {
				q[i] = engine.QuoteName(f)
			}
			cols = " (" + strings.Join(q, ", ") + ")"
		}
		body = fmt.Sprintf(`#!/bin/sh
# pg2sql 导入辅助脚本 (v1.0.38) — 自动生成，勿手改
# 用法: sh %[1]s [数据库名]
# 环境变量可覆盖: PSQL(默认 psql，金仓设 PSQL=ksql)  DB  PGCLIENTENCODING
DB=${DB:-${1:-%[2]s}}
PSQL=${PSQL:-psql}
export PGCLIENTENCODING=${PGCLIENTENCODING:-%[3]s}
%[8]s"$PSQL" -d "$DB" -c "\copy %[4]s%[5]s FROM '%[6]s' WITH (FORMAT csv, HEADER %[7]t, DELIMITER ',', ENCODING 'UTF8', NULL '\N')"
`, filepath.Base(sh), db, "UTF8", shellDQLEscape(engine.QuoteName(tm.Schema)+"."+engine.QuoteName(tm.RelName)), cols, shellSQEscape(name), o.Header, ddlStep)
	}
	if err := os.WriteFile(sh, []byte(body), 0755); err != nil {
		logx.Logf("导入脚本生成失败: %v", err)
		return
	}
	logx.Logf("导入辅助脚本: %s", sh)
}

// quotedFullName：将 "schema.table" 全名各段加引号（v1.0.56 P3-G9）。
// \copy 等脚本场景只有全名字符串可用；大写/空格/保留字表名不加引号回灌必失败。
func quotedFullName(full string) string {
	if i := strings.Index(full, "."); i >= 0 {
		return engine.QuoteName(full[:i]) + "." + engine.QuoteName(full[i+1:])
	}
	return engine.QuoteName(full)
}

// shellDQLEscape：把文本安全嵌入 sh 双引号字符串（v1.0.60 P2-12）。
// 表名来自目标库目录（可被构造）：含 `"` 可提前闭合双引号、`$(...)`/反引号
// 会被 shell 执行 → 生成的 import 脚本变成命令注入载体。
func shellDQLEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\', '"', '`', '$':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// shellSQEscape：把文本安全嵌入 sh 单引号字符串。
func shellSQEscape(s string) string {
	return strings.ReplaceAll(s, "'", `'\''`)
}

// makeImportAllScript：批量导出结束时生成 import_all.sh（含全部成功导出表）
// B3 修复：header 参数联动 CSV 是否含表头（--header），避免 HEADER true 误吞首行数据
func makeImportAllScript(outDir string, entries []integrity.ExportReportEntry, header bool) {
	if len(entries) == 0 {
		return
	}
	var lines []string
	lines = append(lines, "#!/bin/sh",
		"# pg2sql 批量导入辅助脚本 (v1.0.38) — 自动生成，勿手改",
		"# 用法: sh import_all.sh [数据库名]",
		"# 环境变量可覆盖: PSQL(默认 psql，金仓设 PSQL=ksql)  DB  PGCLIENTENCODING",
		"DB=${DB:-${1:-postgres}}",
		"PSQL=${PSQL:-psql}",
		"export PGCLIENTENCODING=${PGCLIENTENCODING:-UTF8}",
		"set -e")
	for _, e := range entries {
		if e.Error != "" || e.File == "" {
			continue
		}
		fn := shellSQEscape(filepath.Base(e.File))
		// v1.0.48：DDL 独立成 .ddl，批量脚本先建表（不存在则跳过，兼容未拆分场景）
		if e.Format == "csv" || e.Format == "sql" {
			ddlFn := strings.TrimSuffix(fn, filepath.Ext(fn)) + ".ddl"
			lines = append(lines, fmt.Sprintf(`if [ -f '%s' ]; then "$PSQL" -d "$DB" -v ON_ERROR_STOP=1 -f '%s'; fi`, ddlFn, ddlFn))
		}
		if e.Format == "csv" {
			lines = append(lines, fmt.Sprintf(`"$PSQL" -d "$DB" -c "\copy %s FROM '%s' WITH (FORMAT csv, HEADER %t, DELIMITER ',', ENCODING 'UTF8', NULL '\N')"`, shellDQLEscape(quotedFullName(e.Table)), fn, header))
		} else if e.Format == "sql" {
			lines = append(lines, fmt.Sprintf(`"$PSQL" -d "$DB" -v ON_ERROR_STOP=1 -f '%s'`, fn))
		}
	}
	if len(lines) <= 8 {
		return
	}
	sh := filepath.Join(outDir, "import_all.sh")
	if err := os.WriteFile(sh, []byte(strings.Join(lines, "\n")+"\n"), 0755); err != nil {
		logx.Logf("import_all.sh 生成失败: %v", err)
		return
	}
	logx.Logf("批量导入辅助脚本: %s", sh)
}
