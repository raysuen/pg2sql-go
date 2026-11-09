// Author: raysuen
package cli

import (
	"errors"
	"fmt"
	"os"
	"pg2sql/internal/charset"
	"pg2sql/internal/config"
	"pg2sql/internal/logx"
	"pg2sql/internal/paid"
	"strconv"
	"strings"
)

// IsNumericStr 判断字符串是否全为数字（用于 relfilenode 文件名校验）
func IsNumericStr(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func ParseArgs(args []string) *config.Options {
	o := &config.Options{DBOID: 5, CompleteInsert: true, Delimiter: "", Encoding: "auto", BatchRows: 1, Jobs: 1, ImportScript: "none"}
	// v1.2.5：显式记录旧三旗标是否出现（--text 会隐含置位 o.Data，运行后无法区分，
	// 互斥校验必须依赖解析期的显式出现标记）
	sawSQL, sawData, sawText := false, false, false
	i := 0
	for i < len(args) {
		a := args[i]
		next := func() string {
			i++
			if i >= len(args) {
				logx.ErrorExit("参数 " + a + " 缺少值")
			}
			return args[i]
		}
		switch a {
		case "--catalog-json":
			o.CatalogJSON = next()
		case "--datadir":
			o.Datadir = next()
		case "--db-oid":
			o.DBOID = atoi(next())
		case "--table-name":
			o.TableName = next()
		case "--ddl":
			o.DDL = true
		case "--sql":
			o.SQL = true
			sawSQL = true
		case "--data":
			o.Data = true
			sawData = true
		case "--text":
			// v1.1.1：COPY text 格式（制表符分隔 + 反斜杠转义 + \N 表 NULL）。
			// 复用数据导出链路，仅格式不同；-o 产物后缀为 .txt。
			o.Data = true
			o.TextFormat = true
			sawText = true
		case "--format":
			// v1.2.5：--format 值参数收口数据格式（sql/csv/text），替代 --sql/--data/--text 三旗标。
			// 只能出现一次，且不能与旧三旗标混用（见下方统一校验块）。
			if o.Format != "" {
				logx.ErrorExit("--format 只能指定一次")
			}
			o.Format = next()
		case "--clog":
			// v1.2.0：读 CLOG（pg_xact/sys_xact）复核元组可见性。
			// 仅对「hint bit 缺失」的元组生效，常规库行为不变。
			o.Clog = true
		case "--deleted":
			o.Deleted = true
		case "--only-deleted":
			o.OnlyDeleted = true
		case "--count":
			o.Count = true
		case "--list-db":
			o.ListDB = true
		case "--list-tables-db":
			o.ListTablesDB = true
		case "--list-tables-all":
			o.ListTablesAll = true
		case "--tables":
			o.Tables = true
		case "--all-tables":
			o.AllTables = true
		case "--schema":
			o.Schema = next()
		case "--export-meta":
			o.ExportMeta = true
		case "--output", "-o":
			o.Output = next()
		case "--limit":
			o.Limit = atoi(next())
		case "--fields":
			o.Fields = next()
		case "--header":
			o.Header = true
		case "--complete-insert":
			o.CompleteInsert = true
		case "--no-complete-insert":
			o.CompleteInsert = false
		case "--delimiter":
			o.Delimiter = next()
		case "--toast":
			o.Toast = next()
		case "--page-size":
			o.PageSize = atoi(next())
		case "--parallel":
			o.Parallel = atoi(next())
		case "--batch-rows":
			o.BatchRows = atoi(next())
		case "--jobs":
			o.Jobs = atoi(next())
		case "--report":
			o.Report = true
		case "--report-file":
			// v1.0.53：报告落盘路径可指定（默认 <输出目录>/report.json）
			o.Report = true
			o.ReportPath = next()
		case "--roles":
			o.Roles = true
		case "--roles-nopassword":
			o.Roles = true
			o.RolesNoPassword = true
		case "--checksum":
			o.Checksum = next()
			if o.Checksum != "crc32" && o.Checksum != "md5" {
				logx.ErrorExit("--checksum 仅支持 crc32 或 md5")
			}
		case "--import-script":
			o.ImportScript = next()
			switch o.ImportScript {
			case "none", "auto", "sql", "csv":
			default:
				logx.ErrorExit("--import-script 仅支持 none/auto/sql/csv")
			}
		case "--tz":
			o.MysqlTZ = next()
		case "--enckey", "--wallet-password", "--wallet-dir":
			paid.ParsePaidArg(a, o, next)
		case "--full-schema":
			o.FullSchema = true
		case "--schema-objects":
			o.SchemaObjects = next()
		case "--large-objects":
			o.LargeObjects = true
		case "--large-object-oids":
			o.LargeObjectOIDs = next()
		case "--progress":
			o.Progress = true
		case "--encoding":
			o.Encoding = next()
		case "--verbose":
			o.Verbose = true
		case "--version":
			fmt.Println("pg2sql " + Version + paid.PaidVersionSuffix())
			os.Exit(0)
		case "--help", "-h":
			PrintHelp()
			os.Exit(0)
		default:
			if strings.HasPrefix(a, "-") {
				logx.ErrorExit("未知参数: " + a)
			}
			if err := SetPositional(o, a); err != nil {
				logx.ErrorExit(err.Error())
			}
		}
		i++
	}
	// v1.2.5：数据格式统一收口（--format 值参数 + 旧三旗标互斥 + --header 仅限 csv）。
	// 背景：v1.2.4 及之前 --sql 与 --data 同现时 --data 静默优先（export.go 分支顺序），
	// --text 与 --header 报错但 --sql 与 --header 静默忽略，语义不统一。全部改为解析期显式报错。
	if o.Format != "" {
		if o.Format != "sql" && o.Format != "csv" && o.Format != "text" {
			logx.ErrorExit("--format 仅支持 sql/csv/text")
		}
		if o.SQL || o.Data || o.TextFormat {
			logx.ErrorExit("--format 与 --sql/--data/--text 不能混用（新写法 --format 与旧旗标二选一）")
		}
	}
	// 旧三旗标互斥：按解析期显式出现计数（--text 隐含的 Data 位不算独立格式）
	nFmt := 0
	for _, saw := range []bool{sawSQL, sawData, sawText} {
		if saw {
			nFmt++
		}
	}
	if nFmt > 1 {
		logx.ErrorExit("--sql / --data / --text 互斥，最多指定一个（sql=INSERT，data=CSV，text=COPY text；或用 --format）")
	}
	// 归一化 --format → 内部三旗标（下游判断逻辑零改动）
	if o.Format != "" {
		o.SQL = o.Format == "sql"
		o.Data = o.Format != "sql"
		o.TextFormat = o.Format == "text"
	}
	// v1.1.1：--text 的互斥与无意义组合在解析期拦下，避免产出歧义内容或非法文件
	if o.TextFormat {
		if o.SQL {
			logx.ErrorExit("--text 与 --sql 不能同时使用（数据格式二选一：--sql 输出 INSERT，--text 输出 COPY text）")
		}
	}
	// v1.2.5：--header 仅限 csv 格式（--format csv 或旧写法 --data）。
	// 原实现 --sql/--header 静默忽略、--text/--header 报错，现统一为非 csv 一律报错。
	fmtIsCsv := o.Format == "csv" || (o.Format == "" && o.Data && !o.TextFormat)
	if o.Header && !fmtIsCsv {
		logx.ErrorExit("--header 仅适用于 csv 格式（--format csv 或 --data）；sql/text 格式不含表头行")
	}
	// v1.2.6（P3）：--encoding 白名单校验。原实现未知名（如老文档写法 euc_cn、
	// 拼写错误 win1252）原样穿透 → DecodeBytes 落 default 分支 → 非 UTF-8 字节
	// 逐字节 Latin-1 静默乱码、无告警。现解析期显式报错（下划线别名已在
	// charset.NormalizeEncodingName 归一，euc_cn 等合法写法不受影响）。
	if o.Encoding != "" && o.Encoding != "auto" && !charset.IsSupportedEncoding(o.Encoding) {
		logx.ErrorExit("--encoding 不支持 \"" + o.Encoding + "\"（支持：auto、utf8/utf-8、gbk/gb2312、gb18030、" +
			"euc-cn/euc_cn、euc-jp、euc-kr、euc-tw、latin1、sql_ascii、sjis、big5、uhc、johab、mule-internal、shift-jis-2004）")
	}
	o.Delimiter = NormalizeDelimiter(o.Delimiter)
	// v1.2.12 修复（A3）：--fields 裁剪列 + 关闭列清单 → INSERT 的 VALUES 个数
	// 与表列数不符（`--fields a,b --no-complete-insert` 对 3 列表产出 2 值），
	// 回灌必然报错。与 --header/--delimiter 同口径，在解析期拦下并给出改法。
	if o.Fields != "" && !o.CompleteInsert && o.SQL {
		logx.ErrorExit("--fields 与 --no-complete-insert 不能同时使用：裁剪列后必须保留 INSERT 列清单（省略 --no-complete-insert），否则 VALUES 个数与表列数不符，回灌必然报错")
	}
	return o
}

// NormalizeDelimiter 归一化 --delimiter 取值（v1.1.1）。
// shell 里 `--delimiter '\t'` 传进来是「反斜杠 + t」两个字符，直接当分隔符会静默
// 产出错位文件（旧实现 csvField 只比较首字节，两个字符的分隔符实际只认反斜杠）。
// 单字符直接放行；已知转义写法归一；其余多字符报错并给出写法提示。
func NormalizeDelimiter(s string) string {
	switch s {
	case "", "\t":
		return s
	case `\t`, "tab", "TAB":
		return "\t"
	case `\n`:
		return "\n"
	}
	if len([]rune(s)) > 1 {
		logx.ErrorExit("--delimiter 只支持单字符（制表符写 --delimiter '\\t'）: " + s)
	}
	return s
}

// SetPositional：登记唯一的位置参数（数据文件或数据库目录）。
// v1.0.53 修复：旧实现"后者覆盖前者"，`--report /path/report.json` 这类把路径误当参数值的
// 写法会静默把数据目录替换成该路径，批量导出退化为"0 表 0 行"——必须在解析期报错。
func SetPositional(o *config.Options, a string) error {
	if o.DataFile != "" && o.DataFile != a {
		return errors.New("只允许一个位置参数（数据文件或数据库目录），多余参数: " + a +
			"\n  提示: --report 是开关（不需要跟路径），报告固定写入 <输出目录>/report.json；" +
			"要指定路径请用 --report-file <路径>")
	}
	o.DataFile = a
	return nil
}

func atoi(s string) int {
	v, err := strconv.Atoi(s)
	if err != nil {
		logx.ErrorExit("数值参数错误: " + s)
	}
	return v
}
