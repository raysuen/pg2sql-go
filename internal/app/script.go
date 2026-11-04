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
	"sort"
	"strings"
)

// makeImportScript：生成单表导入辅助脚本（--import-script 非 none 时调用）
// sql → psql/ksql -f；csv → \copy（--fields 联动列清单）；auto 按导出类型决定
func makeImportScript(o *config.Options, tm *engine.TableMeta, outPath, fileType string, fields []string, ddlFile string) {
	mode := o.ImportScript
	// v1.0.64 修复（E-4）：加括号明确优先级（语义不变）；csv 模式只对 .csv 产物生效、
	// sql 模式只对 .sql 产物生效——旧实现 `--import-script csv` 遇到 .sql 产物
	// 会生成对 SQL 文本做 \copy 的非法脚本。
	// v1.1.1：--text 产物（.txt）走 \copy 路径，与 csv 同属"按行灌数据"；COPY 选项按格式区分
	if mode == "none" || (mode == "auto" && fileType != "sql" && fileType != "csv" && fileType != "txt") {
		return
	}
	// B1 修复：auto 按实际导出类型选择导入方式（csv/txt→\copy、sql→psql -f），
	// 避免对 CSV 生成 psql -f 脚本（CSV 被当 SQL 执行必然失败）
	if mode == "auto" {
		if fileType == "csv" || fileType == "txt" {
			mode = "csv"
		} else {
			mode = "sql"
		}
	}
	if mode == "csv" && fileType != "csv" && fileType != "txt" {
		logx.Logf("警告: --import-script csv 与导出类型 %s 不匹配，跳过该表导入脚本", fileType)
		return
	}
	if mode == "sql" && fileType != "sql" {
		logx.Logf("警告: --import-script sql 与导出类型 %s 不匹配，跳过该表导入脚本", fileType)
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
		ddlStep = "\"$PSQL\" -d \"$DB\" -v ON_ERROR_STOP=1 -f '" + shellSQEscape(filepath.Base(ddlFile)) + "'\n"
	}
	// v1.0.64 修复（E-5）：\copy 的 DELIMITER 与导出时 --delimiter 联动
	//（旧实现硬编码 ','，--delimiter '|' 导出后一键导入必然错位/失败）
	// v1.1.1：delim 经 ResolveDelimiter 归一——CSV 默认 ','、COPY text 默认制表符
	delim := o.ResolveDelimiter()
	// v1.1.1：COPY 选项按格式拼装。
	//   csv → FORMAT csv + HEADER + DELIMITER + NULL '\N'（--data 输出把 NULL 记作 \N，
	//         必须显式声明，否则服务端按默认 NULL='' 解析会造成 NULL 与空串互换）
	//   txt → FORMAT text（该格式 NULL 默认即 \N、分隔符默认即制表符，无需额外参数；
	//         仅当用户自定义分隔符时才显式 DELIMITER）
	copyOpts := ""
	switch fileType {
	case "txt":
		copyOpts = "FORMAT text, ENCODING 'UTF8'"
		if delim != "\t" {
			copyOpts += fmt.Sprintf(", DELIMITER '%s'", shellDQLEscape(delim))
		}
	default:
		copyOpts = fmt.Sprintf("FORMAT csv, HEADER %t, DELIMITER '%s', ENCODING 'UTF8', NULL '\\N'", o.Header, shellDQLEscape(delim))
	}
	// B2/B3 修复：导出文件恒为 UTF-8（ToData/SQL 输出均为 UTF-8），
	// 脚本 PGCLIENTENCODING 默认与文件一致（用户可覆盖），避免源库编码（GBK/EUC_CN 等）注入造成双重转换
	// v1.0.64 修复（E-7）：确保目标库存在的前置段（消除"首轮导入全 FAIL"假象）。
	// MAINTDB 可覆盖维护库（金仓常为 kingbase）；GBK 等特殊编码目标库请预建。
	ensureDB := `# 确保目标库存在（v1.0.64 起；特殊编码目标库请用 CREATE DATABASE ... ENCODING 预建后设 DB）
if ! "$PSQL" -d "${MAINTDB:-postgres}" -Atc "SELECT 1 FROM pg_database WHERE datname='$DB'" | grep -qx 1; then
  echo "目标库 $DB 不存在，自动创建（默认编码；GBK 等请预建）..."
  "$PSQL" -d "${MAINTDB:-postgres}" -c "CREATE DATABASE \"$DB\""
fi
`
	var body string
	if mode == "sql" {
		body = fmt.Sprintf(`#!/bin/sh
# pg2sql 导入辅助脚本 (v1.1.1) — 自动生成，勿手改
# 用法: sh %[1]s [数据库名]
# 环境变量可覆盖: PSQL(默认 psql，金仓设 PSQL=ksql)  DB  MAINTDB(维护库，默认 postgres)  PGCLIENTENCODING
DB=${DB:-${1:-%[2]s}}
PSQL=${PSQL:-psql}
export PGCLIENTENCODING=${PGCLIENTENCODING:-%[3]s}
%[5]s"$PSQL" -d "$DB" -v ON_ERROR_STOP=1 -f '%[4]s'
`, filepath.Base(sh), db, "UTF8", shellSQEscape(name), ensureDB+ddlStep)
	} else {
		cols := ""
		if len(fields) > 0 {
			q := make([]string, len(fields))
			for i, f := range fields {
				// v1.0.64 修复（E-3）：列名经 QuoteName 后仍处 shell 双引号上下文，
				// `"`/`$`/反引号必须再过 shellDQLEscape（与表名同威胁模型）
				q[i] = shellDQLEscape(engine.QuoteName(f))
			}
			cols = " (" + strings.Join(q, ", ") + ")"
		}
		body = fmt.Sprintf(`#!/bin/sh
# pg2sql 导入辅助脚本 (v1.1.1) — 自动生成，勿手改
# 用法: sh %[1]s [数据库名]
# 环境变量可覆盖: PSQL(默认 psql，金仓设 PSQL=ksql)  DB  MAINTDB(维护库，默认 postgres)  PGCLIENTENCODING
DB=${DB:-${1:-%[2]s}}
PSQL=${PSQL:-psql}
export PGCLIENTENCODING=${PGCLIENTENCODING:-%[3]s}
%[8]s"$PSQL" -d "$DB" -c "\copy %[4]s%[5]s FROM '%[6]s' WITH (%[7]s)"
`, filepath.Base(sh), db, "UTF8", shellDQLEscape(engine.QuoteName(tm.Schema)+"."+engine.QuoteName(tm.RelName)), cols, shellDQLEscape(name), copyOpts, ensureDB+ddlStep)
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
// v1.0.64 修复（E-6）：条目按文件名排序（旧实现按 --jobs 并发完成顺序排列，
// set -e 下首错中止余下全部）；DELIMITER 与导出联动；
// \copy 的文件名改 shellDQLEscape（单引号在双引号上下文内无保护，$(...) 照样执行）
// v1.0.65 修复（F14）：排序改为【任务依赖序】——order 传入 exportTasks 之后的
// 任务名序列（已按分区深度稳定排序，父表在前）。旧实现纯按文件名排序，
// 子表 "2024_sales.csv" 字典序小于父表 "sales.csv" 时 PARTITION OF 先于父表
// 执行，set -e 下首轮即失败。order 为空（单表/测试路径）时回退文件名排序。
func makeImportAllScript(outDir string, entries []integrity.ExportReportEntry, header bool, delimiter string, order []string) {
	if len(entries) == 0 {
		return
	}
	sorted := make([]integrity.ExportReportEntry, len(entries))
	copy(sorted, entries)
	rank := make(map[string]int, len(order))
	for i, n := range order {
		if _, ok := rank[n]; !ok {
			rank[n] = i
		}
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		ri, iok := rank[sorted[i].Table]
		rj, jok := rank[sorted[j].Table]
		if iok && jok && ri != rj {
			return ri < rj
		}
		if iok != jok {
			return iok
		}
		return sorted[i].File < sorted[j].File
	})
	if delimiter == "" {
		delimiter = ","
	}
	var lines []string
	lines = append(lines, "#!/bin/sh",
		"# pg2sql 批量导入辅助脚本 (v1.1.1) — 自动生成，勿手改",
		"# 用法: sh import_all.sh [数据库名]",
		"# 环境变量可覆盖: PSQL(默认 psql，金仓设 PSQL=ksql)  DB  MAINTDB(维护库，默认 postgres)  PGCLIENTENCODING",
		"DB=${DB:-${1:-postgres}}",
		"PSQL=${PSQL:-psql}",
		"export PGCLIENTENCODING=${PGCLIENTENCODING:-UTF8}",
		// v1.0.64 修复（E-7）：目标库不存在时自动创建（特殊编码目标库请预建后设 DB）
		`if ! "$PSQL" -d "${MAINTDB:-postgres}" -Atc "SELECT 1 FROM pg_database WHERE datname='$DB'" | grep -qx 1; then`,
		`  echo "目标库 $DB 不存在，自动创建（默认编码；GBK 等请预建）..."`,
		`  "$PSQL" -d "${MAINTDB:-postgres}" -c "CREATE DATABASE \"$DB\""`,
		`fi`,
		"set -e")
	for _, e := range sorted {
		if e.Error != "" || e.File == "" {
			continue
		}
		base := filepath.Base(e.File)
		// v1.0.64 修复（E-3）：先在未转义原名上推导 .ddl 文件名，再按各自上下文转义
		if e.Format == "csv" || e.Format == "sql" || e.Format == "txt" {
			ddlFn := strings.TrimSuffix(base, filepath.Ext(base)) + ".ddl"
			ddlFn = shellSQEscape(ddlFn)
			lines = append(lines, fmt.Sprintf(`if [ -f '%s' ]; then "$PSQL" -d "$DB" -v ON_ERROR_STOP=1 -f '%s'; fi`, ddlFn, ddlFn))
		}
		if e.Format == "txt" {
			// v1.1.1：COPY text 产物。默认分隔符制表符、NULL 记 \N，与格式默认值天然一致
			opts := "FORMAT text, ENCODING 'UTF8'"
			if delimiter != "\t" {
				opts += fmt.Sprintf(", DELIMITER '%s'", shellDQLEscape(delimiter))
			}
			lines = append(lines, fmt.Sprintf(`"$PSQL" -d "$DB" -c "\copy %s FROM '%s' WITH (%s)"`,
				shellDQLEscape(quotedFullName(e.Table)), shellDQLEscape(base), opts))
		} else if e.Format == "csv" {
			lines = append(lines, fmt.Sprintf(`"$PSQL" -d "$DB" -c "\copy %s FROM '%s' WITH (FORMAT csv, HEADER %t, DELIMITER '%s', ENCODING 'UTF8', NULL '\N')"`,
				shellDQLEscape(quotedFullName(e.Table)), shellDQLEscape(base), header, shellDQLEscape(delimiter)))
		} else if e.Format == "sql" {
			lines = append(lines, fmt.Sprintf(`"$PSQL" -d "$DB" -v ON_ERROR_STOP=1 -f '%s'`, shellSQEscape(base)))
		}
	}
	if len(lines) <= 11 { // 8 行头 + 3 行 ensureDB，全部为固定内容
		return
	}
	sh := filepath.Join(outDir, "import_all.sh")
	if err := os.WriteFile(sh, []byte(strings.Join(lines, "\n")+"\n"), 0755); err != nil {
		logx.Logf("import_all.sh 生成失败: %v", err)
		return
	}
	logx.Logf("批量导入辅助脚本: %s", sh)
}
