// Author: raysuen
package cli

import (
	"errors"
	"fmt"
	"os"
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
	o := &config.Options{DBOID: 5, CompleteInsert: true, Delimiter: ",", Encoding: "auto", BatchRows: 1, Jobs: 1, ImportScript: "none"}
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
		case "--data":
			o.Data = true
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
	return o
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
