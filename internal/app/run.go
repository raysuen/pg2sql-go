// pg2sql Go 版：离线解析 PostgreSQL/KingbaseES 堆文件 → DDL/INSERT/CSV + deleted 审计
// 对应 Python 版 main.py（v1.29），CLI 与输出行为逐条对齐
// Author: raysuen
package app

import (
	"os"
	"path/filepath"
	"pg2sql/internal/catalog"
	"pg2sql/internal/charset"
	"pg2sql/internal/cli"
	"pg2sql/internal/datatype"
	"pg2sql/internal/ddl"
	"pg2sql/internal/engine"
	"pg2sql/internal/logx"
	"pg2sql/internal/util"
	"strconv"
	"strings"
)

// init：包级依赖注入（v1.0.57 目录重构）——datatype 通过回调获取目录扫描能力，
// 避免 datatype ↔ catalog/engine 循环依赖。TDE/免费版共用。
func init() {
	datatype.DynamicScanner = catalog.ScanDynamic
	datatype.PostScanHook = func(dbDir string, version int, isKB bool) {
		ddl.SetDomainChecks(ddl.BuildDomainChecks(dbDir, version, isKB))
	}
}

// ---------- 主流程 ----------
func run(args []string) {
	o := cli.ParseArgs(args)
	charset.SetTextEncoding("")

	// B9 修复：--tz 显式指定 mysql 实例时区（优先于 conf 推导）
	if o.MysqlTZ != "" {
		if off, ok := util.ParseTZOffset(o.MysqlTZ); ok {
			datatype.MysqlTZOffsetUS = off
			datatype.MysqlTZSet = true
		} else {
			logx.ErrorExit("--tz 无法解析（支持 ±HH:MM/±HHMM/±HH 或 IANA 时区名，如 Asia/Shanghai、+09:00、UTC）: " + o.MysqlTZ)
		}
	}

	// v1.0.40：--full-schema 必须搭配 --ddl
	if o.FullSchema && !o.DDL {
		logx.ErrorExit("--full-schema 需要 --ddl（生成 DDL 模式）")
	}

	// v1.0.42：--deleted 与 --only-deleted 语义互斥
	if o.Deleted && o.OnlyDeleted {
		logx.ErrorExit("--deleted 与 --only-deleted 不能同时使用（语义互斥，请二选一）")
	}
	// v1.0.42：--batch-rows 仅 SQL 模式生效，非 SQL 模式给出提示
	if o.BatchRows > 1 && (o.Data || (o.DDL && !o.SQL)) {
		logx.Logf("提示: --batch-rows 仅 SQL 模式生效，当前模式已忽略该参数")
	}

	// 编码处理
	if o.Encoding != "" && o.Encoding != "auto" {
		charset.SetTextEncoding(normalizeEncoding(o.Encoding))
	}

	// ---- 列表类操作 ----
	if o.ListDB {
		listDatabases(o)
		return
	}
	if o.ListTablesDB {
		listTablesInDB(o, false)
		return
	}
	if o.ListTablesAll {
		listTablesInDB(o, true)
		return
	}
	if o.ExportMeta {
		dbDir := resolveDbDir(o)
		if dbDir == "" {
			logx.ErrorExit("--export-meta 需要数据库目录 (位置参数或 --datadir+--db-oid)")
		}
		ps := o.PageSize
		if err := catalog.ExportMetaJSON(dbDir, ps, o.Output); err != nil {
			logx.ErrorExit("导出 meta 失败: " + err.Error())
		}
		logx.Logf("元数据已导出到: %s", o.Output)
		return
	}

	if o.DataFile == "" && o.Datadir == "" {
		logx.ErrorExit("需要指定数据文件路径 (或使用 --list-db / --export-meta / --list-tables-db / --list-tables-all)")
	}

	// ---- 批量导出模式（--tables 用户表 / --all-tables 全部表 / --schema 指定模式）----
	// --schema NAME 独立使用等价于 --tables --schema NAME（批量导出该模式下的用户表）
	// v1.0.56 修复（P3-G10）：本分支先于 --table-name 解析执行，与 --table-name
	// 同用时后者被静默忽略（导出全模式表但报告仍记录指定表名）。互斥参数显式报错。
	if o.TableName != "" && (o.Tables || o.AllTables || o.Schema != "") {
		logx.ErrorExit("--table-name 与 --tables/--all-tables/--schema 互斥，不能同时指定")
	}
	if o.Tables || o.AllTables || o.Schema != "" {
		if err := runAllTables(o); err != nil {
			// B13 修复：批量部分失败时也先落盘报告（含已成功表），再退出
			writeReport(o)
			logx.ErrorExit(err.Error())
		}
		// v1.0.40：批量模式后的 Schema DDL / 大对象（独立于表导出，可同时指定）
		if o.FullSchema {
			if _, err := exportSchemaObjects(o); err != nil {
				logx.ErrorExit(err.Error())
			}
		}
		if o.LargeObjects {
			if _, err := exportLargeObjects(o); err != nil {
				logx.ErrorExit(err.Error())
			}
		}
		// v1.0.53：角色 DDL（与表导出并行指定）
		if o.Roles {
			if _, err := exportRoles(o); err != nil {
				logx.ErrorExit(err.Error())
			}
		}
		writeReport(o)
		return
	}

	// ---- v1.0.40：仅 Schema DDL / 仅大对象导出（--full-schema / --large-objects，无表名/批量标志时独立运行）----
	// v1.0.46 修复：位置参数为【数据库目录】时也应走此分支。旧判断用 `o.DataFile == ""` 区分
	// 单文件模式，但目录同样是写给 DataFile 的 → `pg2sql <dbdir> --ddl --full-schema -o x.ddl`
	// （README 示例写法）会落到"定位目标表"分支报 "未找到目标表，请用 --table-name 指定"。
	if o.TableName == "" && !singleFileArg(o) && !o.Tables && !o.AllTables && o.Schema == "" {
		if o.FullSchema {
			if _, err := exportSchemaObjects(o); err != nil {
				logx.ErrorExit(err.Error())
			}
			writeReport(o)
			return
		}
		if o.LargeObjects {
			if _, err := exportLargeObjects(o); err != nil {
				logx.ErrorExit(err.Error())
			}
			writeReport(o)
			return
		}
		// v1.0.53：--roles 独立运行（角色 DDL 与表数据无关）
		if o.Roles {
			if _, err := exportRoles(o); err != nil {
				logx.ErrorExit(err.Error())
			}
			writeReport(o)
			return
		}
	}

	// ---- 解析元数据 ----
	var tables map[string]*engine.TableMeta
	var dbDir string
	if o.CatalogJSON != "" {
		_, tables, _ = catalog.LoadMetaJSON(o.CatalogJSON)
	} else {
		dbDir = resolveDbDir(o)
		if dbDir == "" {
			logx.ErrorExit("自动发现需要数据库目录 (位置参数目录或 --datadir+--db-oid)")
		}
		ps := o.PageSize
		_, tableList := catalog.AutoDiscoverAllTables(dbDir, ps)
		tables = map[string]*engine.TableMeta{}
		for _, t := range tableList {
			tables[t.FullName()] = t
			if t.RelFileNode != 0 {
				key := strconv.FormatUint(uint64(t.RelFileNode), 10)
				if _, ok := tables[key]; !ok {
					tables[key] = t
				}
			}
		}
		// 方案 C：目录动态类型解码器（v1.0.16）
		ver := catalog.DetectPgVersion(dbDir)
		if ver == 0 {
			ver = 12
		}
		kb := catalog.IsKingbaseDatadir(dbDir)
		if n := datatype.InitDynamicDecodersOnce(dbDir, ver, kb); o.Verbose {
			if n >= 0 {
				logx.Logf("动态类型映射: %d 个 (目录 1247)", n)
			}
		}
	}
	if tables == nil || len(tables) == 0 {
		logx.ErrorExit("未能解析表结构")
	}
	// 自动编码探测（需要数据根目录：global/1262 在数据根下）
	applyAutoEncoding(o, dbDir)
	o.EncBase = currentTextEncoding()

	// ---- 定位目标表 ----
	// --table-name 支持逗号分隔多表 + 双引号包裹（多表走批量导出，需 -o 目录 + --sql/--data）
	names, err := splitTableNames(o.TableName)
	if err != nil {
		logx.ErrorExit(err.Error())
	}
	if len(names) > 1 {
		if err := runNamedTables(o, names, tables); err != nil {
			logx.ErrorExit(err.Error())
		}
		// v1.0.40：多表模式后的 Schema DDL / 大对象
		if o.FullSchema {
			if _, err := exportSchemaObjects(o); err != nil {
				logx.ErrorExit(err.Error())
			}
		}
		if o.LargeObjects {
			if _, err := exportLargeObjects(o); err != nil {
				logx.ErrorExit(err.Error())
			}
		}
		// v1.0.53：角色 DDL
		if o.Roles {
			if _, err := exportRoles(o); err != nil {
				logx.ErrorExit(err.Error())
			}
		}
		writeReport(o)
		return
	}
	// 单表：优先用解析后的表名（去引号/trim）；未传 --table-name 时保持原兜底链
	target := o.TableName
	if len(names) == 1 {
		target = names[0]
	}
	tm := catalog.FindTableInMeta(tables, target)
	if tm == nil {
		// 按数据文件名（relfilenode 或 basename）找
		base := filepath.Base(o.DataFile)
		if t2, ok := tables[base]; ok {
			tm = t2
		} else if t3, ok := tables[strings.TrimSuffix(base, "_fsm")]; ok {
			tm = t3
		}
	}
	if tm == nil {
		// 唯一表时直接使用
		if len(tables) == 1 {
			for _, t := range tables {
				tm = t
			}
		}
	}
	if tm == nil {
		msg := "未找到目标表，请用 --table-name 指定 (可用: " + listTableNames(tables) + ")。提示: 若数据库实例在线且最近写入未 CHECKPOINT，磁盘 sys_class 可能与数据文件 relfilenode 不一致，建议先 CHECKPOINT、指定 --table-name，或使用 --tables/--all-tables/--schema 批量导出"
		// 目标文件存在但未解析到表：可能是 pg_attribute 损坏导致该表属性读取失败
		base := filepath.Base(o.DataFile)
		if _, err := os.Stat(o.DataFile); err == nil && cli.IsNumericStr(base) {
			msg += "。注意: 目标文件存在但未在解析结果中，其表属性可能因 pg_attribute 读取失败(文件损坏/未落盘)而缺失"
		}
		logx.ErrorExit(msg)
	}
	if o.Verbose {
		ncol := 0
		for _, c := range tm.Columns {
			if !c.Dropped {
				ncol++
			}
		}
		logx.Logf("自动发现表结构: %s (%d 列)", tm.FullName(), ncol)
	}

	// ---- v1.0.36: 分区父表自动展开为叶子子分区 ----
	// 单表模式指定声明式分区父表（relkind='p'）时，递归展开其全部叶子子分区（relkind='r'）：
	// 单个叶子保持单表语义（不强制 -o 目录）；多个叶子走批量导出（需 -o 目录 + --sql/--data）。
	// 空分区父表（无子分区）保持原行为（导出 0 行），verbose 下提示。
	// v1.0.52：纯 --ddl 模式不展开——直接输出父表 DDL（含 PARTITION BY），
	// 展开.只能拿到子表 PARTITION OF 而丢失父表定义。
	// v1.0.54 修复 C1：单表导出分区父表时，父表 DDL 必须一并输出。
	// 旧实现只把叶子子分区交给导出流程，子表 DDL 又是 PARTITION OF 挂靠语句，
	// 回灌时必然报 relation "schema.parent" does not exist（数据全丢）。
	// 现在与批量模式统一：多叶子走 runNamedTables（其内部对分区父表在 --ddl 时
	// 以 DDL-only 纳入任务集），单叶子在 exportOneTable 内前置父表 DDL。
	var parentForDDL *engine.TableMeta
	if tm.RelKind == "p" && len(tm.ChildOIDs) > 0 && (o.SQL || o.Data || o.Count || o.OnlyDeleted || o.Deleted) {
		parent := tm
		leaves := expandParentToLeaves(tables, tm)
		if len(leaves) == 1 {
			tm = leaves[0]
			if o.DDL {
				parentForDDL = parent
			}
		} else if len(leaves) > 1 {
			// v1.0.56 修复（P2-G4）：--count/--deleted/--only-deleted 属数据类
			// 模式，命中上方展开分支，但 runNamedTables 硬性要求 --sql/--data，
			// 导致"分区父表 + 纯 --count"多叶子时必然 errorExit（单叶子正常）。
			// 纯 --count 只统计行数、不写批量文件，改为逐叶子调用 exportOneTable
			// 统计（无 -o 时逐叶输出到 stdout，有 -o 时按 schema.table.sql 落盘）。
			if o.Count && !o.SQL && !o.Data && !o.DDL && !o.Deleted && !o.OnlyDeleted {
				for _, l := range leaves {
					if err := exportOneTable(o, l, nil); err != nil {
						logx.ErrorExit(err.Error())
					}
				}
				return
			}
			names := make([]string, 0, len(leaves))
			for _, l := range leaves {
				names = append(names, l.FullName())
			}
			logx.Logf("分区父表 %s 展开为 %d 个子分区: %s", tm.FullName(), len(leaves), strings.Join(names, ", "))
			// 只传父表名：runNamedTables 内部会展开叶子，并在 --ddl 时把父表
			// 以 DDL-only 纳入任务集（与批量模式同一条路径，避免重复导出）。
			if err := runNamedTables(o, []string{parent.FullName()}, tables); err != nil {
				logx.ErrorExit(err.Error())
			}
			return
		}
	} else if tm.RelKind == "p" && o.Verbose {
		logx.Logf("分区父表 %s 无子分区，无独立数据文件，导出 0 行", tm.FullName())
	}

	if err := safeExportOneTable(o, tm, parentForDDL); err != nil {
		logx.ErrorExit(err.Error())
	}
	// v1.0.40：单表模式后的 Schema DDL / 大对象
	if o.FullSchema {
		if _, err := exportSchemaObjects(o); err != nil {
			logx.ErrorExit(err.Error())
		}
	}
	if o.LargeObjects {
		if _, err := exportLargeObjects(o); err != nil {
			logx.ErrorExit(err.Error())
		}
	}
	// v1.0.53：角色 DDL（单表模式下同样可与 --roles 组合）
	if o.Roles {
		if _, err := exportRoles(o); err != nil {
			logx.ErrorExit(err.Error())
		}
	}
	writeReport(o)
}

// expandParentToLeaves 递归展开分区父表（relkind='p'）的全部叶子子分区（relkind='r'）。
// 支持多级分区（子分区再分区）；非父表 / 无子分区返回 nil。
func expandParentToLeaves(tables map[string]*engine.TableMeta, tm *engine.TableMeta) []*engine.TableMeta {
	if tm == nil || tm.RelKind != "p" || len(tm.ChildOIDs) == 0 {
		return nil
	}
	oidMap := map[uint32]*engine.TableMeta{}
	for _, t := range tables {
		if t.OID != 0 {
			oidMap[t.OID] = t
		}
	}
	var leaves []*engine.TableMeta
	var rec func(t *engine.TableMeta, seen map[uint32]bool)
	rec = func(t *engine.TableMeta, seen map[uint32]bool) {
		if t == nil || seen[t.OID] {
			return
		}
		seen[t.OID] = true
		if len(t.ChildOIDs) == 0 {
			if t.RelKind == "r" {
				leaves = append(leaves, t)
			}
			return
		}
		for _, cid := range t.ChildOIDs {
			rec(oidMap[cid], seen)
		}
	}
	rec(tm, map[uint32]bool{})
	return leaves
}

// Run：cmd/pg2sql 的唯一入口——解析参数并执行，返回进程退出码。
// 具体失败路径统一走 logx.ErrorExit（内部 os.Exit(1)），此处仅返回 0。
func Run(args []string) int {
	run(args)
	return 0
}
