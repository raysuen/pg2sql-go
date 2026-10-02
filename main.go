// pg2sql Go 版：离线解析 PostgreSQL/KingbaseES 堆文件 → DDL/INSERT/CSV + deleted 审计
// 对应 Python 版 main.py（v1.29），CLI 与输出行为逐条对齐
// Author: raysuen
package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var progVersion = "1.0.19"

func logf(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "[pg2sql] "+format+"\n", a...)
}



func errorExit(msg string) {
	fmt.Fprintln(os.Stderr, "[pg2sql] 错误:", msg)
	os.Exit(1)
}

type Options struct {
	DataFile       string
	CatalogJSON    string
	Datadir        string
	DBOID          int
	TableName      string
	DDL            bool
	SQL            bool
	Data           bool
	Deleted        bool
	OnlyDeleted    bool
	Count          bool
	ListDB         bool
	ListTablesDB   bool
	ListTablesAll  bool
	Tables         bool
	AllTables      bool
	Schema         string
	ExportMeta     bool
	Output         string
	Limit          int
	Fields         string
	Header         bool
	CompleteInsert bool
	Delimiter      string
	Toast          string
	PageSize       int
	Parallel       int
	Encoding       string
	Verbose        bool
}

func parseArgs(args []string) *Options {
	o := &Options{DBOID: 5, CompleteInsert: true, Delimiter: ",", Encoding: "auto"}
	i := 0
	for i < len(args) {
		a := args[i]
		next := func() string {
			i++
			if i >= len(args) {
				errorExit("参数 " + a + " 缺少值")
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
		case "--encoding":
			o.Encoding = next()
		case "--verbose":
			o.Verbose = true
		case "--version":
			fmt.Println("pg2sql " + progVersion)
			os.Exit(0)
		case "--help", "-h":
			printHelp()
			os.Exit(0)
		default:
			if strings.HasPrefix(a, "-") {
				errorExit("未知参数: " + a)
			}
			o.DataFile = a
		}
		i++
	}
	return o
}

func atoi(s string) int {
	v, err := strconv.Atoi(s)
	if err != nil {
		errorExit("数值参数错误: " + s)
	}
	return v
}

func printHelp() {
	fmt.Println(`pg2sql - PostgreSQL/KingbaseES 堆文件离线解析导出工具 (Go 版 v` + progVersion + `)

用法:
  pg2sql <数据文件> [选项]

核心示例:
  pg2sql data_file --sql                     # 自动发现表结构，导出 INSERT 语句
  pg2sql data_file --ddl --sql               # 同时导出 DDL + INSERT
  pg2sql data_file --data --header -o out.csv  # 导出 CSV（首行字段名，COPY 兼容）
  pg2sql data_file --datadir /pgdata --db-oid 16384 --sql
  pg2sql data_file --count                   # 统计行数
  pg2sql data_file --sql --deleted           # 含已删除行（-- DELETED ctid 注释）
  pg2sql data_file --sql --only-deleted      # 只导出已删除行
  pg2sql data_file --sql --fields id,name   # 只导出指定字段
  pg2sql data_file --data --encoding gbk     # 指定库编码
  pg2sql --datadir /pgdata --list-db         # 列出数据库
  pg2sql /pgdata/base/16384 --list-tables-db   # 列出库内用户对象（默认）
  pg2sql /pgdata/base/16384 --list-tables-all  # 列出库内全部对象（含系统对象）
  pg2sql /pgdata/base/16384 --tables --sql -o /out/            # 批量导出全部用户表（SQL）
  pg2sql /pgdata/base/16384 --all-tables --data -o /out/       # 批量导出全部表（含系统对象，CSV）
  pg2sql /pgdata/base/16384 --schema ray --sql -o /out/           # 指定 schema 批量导出（等价 --tables --schema）
  pg2sql /pgdata/base/16384 --export-meta -o meta.json

选项:
  --catalog-json FILE   表结构 JSON（不指定则自动发现）
  --datadir DIR         PG 数据目录（自动发现/编码探测用）
  --db-oid OID          目标库 OID（默认 5）
  --table-name NAME     指定表名
  --ddl / --sql / --data / --count
  --deleted / --only-deleted
  --list-db / --list-tables-db / --list-tables-all / --export-meta
  --tables / --all-tables    批量导出用户表/全部表（配合 --sql/--data，必须 -o 目录）
  --schema NAME              批量导出指定模式（独立使用等价 --tables --schema NAME）
  -o, --output PATH     输出文件（目录时自动命名 schema.table.sql|.csv|.ddl）
  --limit N             最多输出 N 行
  --fields C1,C2        只导出指定字段
  --header              CSV 首行输出字段名
  --no-complete-insert  INSERT 省略列名
  --delimiter CHAR      CSV 分隔符（默认 ,）
  --toast FILE          TOAST 表文件
  --page-size N         页大小（默认自动探测 8/16/32KB）
  --parallel N          并发页数
  --encoding CODEC      库编码（默认 auto）

支持类型: PG/金仓内置类型全覆盖——数值/字符/二进制/布尔/位/日期时间/JSON/XML/数组/几何/网络/全文/范围/reg*/pg_lsn/txid_snapshot/枚举
（复合类型 record 输出原始字节 E'\\x...' 兜底，字节可逆）
  --verbose             详细日志
  --version`)
}

// ---------- 主流程 ----------
func main() {
	o := parseArgs(os.Args[1:])
	setTextEncoding("")

	// 编码处理
	if o.Encoding != "" && o.Encoding != "auto" {
		setTextEncoding(normalizeEncoding(o.Encoding))
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
			errorExit("--export-meta 需要数据库目录 (位置参数或 --datadir+--db-oid)")
		}
		ps := o.PageSize
		if err := exportMetaJSON(dbDir, ps, o.Output); err != nil {
			errorExit("导出 meta 失败: " + err.Error())
		}
		logf("元数据已导出到: %s", o.Output)
		return
	}

	if o.DataFile == "" {
		errorExit("需要指定数据文件路径 (或使用 --list-db / --export-meta / --list-tables-db / --list-tables-all)")
	}

	// ---- 批量导出模式（--tables 用户表 / --all-tables 全部表 / --schema 指定模式）----
	// --schema NAME 独立使用等价于 --tables --schema NAME（批量导出该模式下的用户表）
	if o.Tables || o.AllTables || o.Schema != "" {
		if err := runAllTables(o); err != nil {
			errorExit(err.Error())
		}
		return
	}

	// ---- 解析元数据 ----
	var tables map[string]*TableMeta
	if o.CatalogJSON != "" {
		_, tables, _ = loadMetaJSON(o.CatalogJSON)
	} else {
		dbDir := resolveDbDir(o)
		if dbDir == "" {
			errorExit("自动发现需要数据库目录 (位置参数目录或 --datadir+--db-oid)")
		}
		ps := o.PageSize
		_, tableList := autoDiscoverAllTables(dbDir, ps)
		tables = map[string]*TableMeta{}
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
		ver := detectPgVersion(dbDir)
		if ver == 0 {
			ver = 12
		}
		kb := isKingbaseDatadir(dbDir)
		if n := initDynamicDecoders(dbDir, ver, kb); o.Verbose {
			if n >= 0 {
				logf("动态类型映射: %d 个 (目录 1247)", n)
			}
		}
	}
	if tables == nil || len(tables) == 0 {
		errorExit("未能解析表结构")
	}
	// 自动编码探测（需要数据根目录：global/1262 在数据根下）
	if o.Encoding == "auto" {
		root := resolveDataRoot(o)
		if root != "" && o.DBOID != 0 {
			if enc := detectDatabaseEncoding(root, o.DBOID); enc != "" {
				setTextEncoding(normalizeEncoding(enc))
				if o.Verbose {
					logf("库编码: %s", enc)
				}
			}
		}
	}

	// ---- 定位目标表 ----
	tm := findTableInMeta(tables, o.TableName)
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
		errorExit("未找到目标表，请用 --table-name 指定 (可用: " + listTableNames(tables) + ")。提示: 若数据库实例在线且最近写入未 CHECKPOINT，磁盘 sys_class 可能与数据文件 relfilenode 不一致，建议先 CHECKPOINT、指定 --table-name，或使用 --tables/--all-tables/--schema 批量导出")
	}
	if o.Verbose {
	ncol := 0
	for _, c := range tm.Columns {
		if !c.Dropped {
			ncol++
		}
	}
	logf("自动发现表结构: %s (%d 列)", tm.FullName(), ncol)
	}

	if err := exportOneTable(o, tm); err != nil {
		errorExit(err.Error())
	}
}

func exportOneTable(o *Options, tm *TableMeta) error {
	// ---- 定位表文件（支持目录 + --table-name）----
	tablePath := o.DataFile
	if fi, err := os.Stat(o.DataFile); err == nil && fi.IsDir() && tm.RelFileNode != 0 {
		cand := filepath.Join(o.DataFile, strconv.FormatUint(uint64(tm.RelFileNode), 10))
		if fi2, err := os.Stat(cand); err == nil && fi2.Mode().IsRegular() {
			tablePath = cand
		}
	}

	// ---- 页大小 ----
	ps := o.PageSize
	if ps == 0 {
		ps = probePageSize(tablePath)
	}
	if ps == 0 {
		return fmt.Errorf("页大小探测失败，请用 --page-size 指定")
	}

	// ---- TOAST ----
	toastPath := o.Toast
	if toastPath == "" && tm.ToastRelID != 0 {
		dbDir := resolveDbDir(o)
		if dbDir != "" {
			cand := filepath.Join(dbDir, strconv.FormatUint(uint64(tm.ToastRelID), 10))
			if fi, err := os.Stat(cand); err == nil && fi.Mode().IsRegular() {
				toastPath = cand
			}
		}
	}
	pgVersion := detectPgVersion(filepath.Dir(filepath.Dir(o.DataFile)))
	if pgVersion == 0 {
		pgVersion = detectPgVersion(o.Datadir)
	}
	if pgVersion == 0 {
		pgVersion = 12
	}
	isKB := isKingbaseDatadir(filepath.Dir(filepath.Dir(o.DataFile)))
	if !isKB {
		isKB = isKingbaseDatadir(o.Datadir)
	}
	tm.IsKB = isKB
	// 方案 C：目录动态类型解码器（覆盖 --catalog-json 路径，v1.0.16）
	if dbDir := resolveDbDir(o); dbDir != "" {
		initDynamicDecoders(dbDir, pgVersion, isKB)
	}

	// ---- 幽灵列截断 ----
	// 金仓 ALTER 残留 att 行（attnum 超出数据行实际列数）时，按数据行最大列数截断列集，
	// 使导出的列与数据库可见列一致（避免 CSV 列数不匹配导致 COPY 失败、行解析错位丢行）。
	if !o.DDL || o.SQL || o.Data || o.Count {
		if maxn := maxRowNatts(tablePath, ps, isKB); maxn > 0 && maxn < len(tm.Columns) {
			kept := make([]ColumnDef, 0, maxn)
			for _, c := range tm.Columns {
				if c.AttNum <= maxn {
					kept = append(kept, c)
				}
			}
			if len(kept) > 0 && len(kept) < len(tm.Columns) {
				if o.Verbose {
					logf("按数据行列数截断幽灵列: %d -> %d 列", len(tm.Columns), len(kept))
				}
				tm.Columns = kept
			}
		}
	}

	// ---- 输出 ----
	outFile := os.Stdout
	var writer *bufio.Writer
	if o.Output != "" {
		// 对齐 Python _resolve_output_path：
		//   目录或以 / 结尾 → 目录/schema.table.ext；带扩展名 → 完整路径；否则 → 前缀 + .ext
		fileType := "sql"
		if o.Data {
			fileType = "csv"
		} else if o.DDL && !o.SQL && !o.Count && !o.Deleted && !o.OnlyDeleted {
			fileType = "ddl" // 纯 DDL 模式独立后缀，避免覆盖同名 .sql
		}
		outPath := resolveOutputPath(o.Output, tm, fileType)
		if dir := filepath.Dir(outPath); dir != "" && dir != "." {
			os.MkdirAll(dir, 0755)
		}
		f, err := os.Create(outPath)
		if err != nil {
			return fmt.Errorf("无法创建输出文件: " + err.Error())
		}
		defer f.Close()
		outFile = f
		if o.Verbose {
			logf("输出到: %s", outPath)
		}
	}
	writer = bufio.NewWriterSize(outFile, 1<<20)
	defer writer.Flush()

	// DDL（基础 CREATE TABLE + 扩展：索引/序列默认值/注释）
	if o.DDL {
		writer.WriteString(tm.GenerateDDL() + "\n")
		if dbDir := resolveDbDir(o); dbDir != "" {
			ddlStmts, _ := buildDdlStatements(dbDir, tm, pgVersion, isKB)
			for _, s := range ddlStmts {
				writer.WriteString(s + "\n")
			}
		}
		if o.Verbose {
			logf("DDL 输出完成")
		}
	}
	if o.DDL && !o.SQL && !o.Data && !o.Count && !o.Deleted && !o.OnlyDeleted {
		return nil
	}

	// 构造 RowIter
	ri := &RowIter{
		tm:          tm,
		includeDel:  o.Deleted || o.OnlyDeleted,
		onlyDeleted: o.OnlyDeleted,
		limit:       o.Limit,
		pageSize:    ps,
		pgVersion:   pgVersion,
		isKB:        isKB,
		path:        tablePath,
		verboseDebug: o.Verbose,
	}
	// TOAST 关联
	if toastPath != "" {
		tf := &ToastFile{
			Path:      toastPath,
			PageSize:  ps,
			PageCache: map[int]map[int][]byte{},
			CacheMax:  64,
			PgVersion: pgVersion,
			IsKB:      isKB,
		}
		if fi, err := os.Stat(toastPath); err == nil && fi.Size() > 0 {
			tps := probePageSize(toastPath)
			tf.PageSize = tps
		}
		idxStart := nowMs()
		if o.Parallel > 1 {
			// 并行预建索引（goroutine 分页）
			tf.BuildIndexParallel(o.Parallel, o.Verbose)
		} else {
			tf.BuildIndex(o.Verbose)
		}
		if o.Verbose {
			logf("TOAST 索引预建完成: %d 个 valueid, 耗时 %.1fs", len(tf.PosIndex), float64(nowMs()-idxStart)/1000.0)
		}
		if len(tf.PosIndex) == 0 {
			if fi, err := os.Stat(toastPath); err == nil && fi.Size() > 8192 {
				logf("警告: TOAST 文件非空(%d 字节)但索引中未找到任何 valueid。可能数据库实例在线且最近写入尚未 CHECKPOINT，磁盘 TOAST 索引页未完整落盘，外置列数据可能不完整。建议先对实例执行 CHECKPOINT 后再导出。")
			}
		}
		ri.toast = tf
	}

	// count 模式
	if o.Count {
		count := 0
		for row := range ri.DumpRows() {
			_ = row
			count++
		}
		writer.WriteString(fmt.Sprintf("-- 总行数: %d\n", count))
		if o.Verbose {
			logf("统计完成: %d 行", count)
		}
		return nil
	}

	// SQL / DATA 输出
	fields := []string(nil)
	if o.Fields != "" {
		fields = strings.Split(o.Fields, ",")
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}
	}
	nWritten := 0
	if o.Data {
		for line := range ToData(ri, o.Delimiter, fields, o.Header) {
			writer.WriteString(line + "\n")
			nWritten++
			if nWritten%10000 == 0 && o.Verbose {
				logf("已输出 %d 行...", nWritten)
			}
		}
	} else if o.SQL || !o.DDL {
		for stmt := range ToSQL(ri, o.CompleteInsert, fields) {
			writer.WriteString(stmt + "\n")
			nWritten++
			if nWritten%10000 == 0 && o.Verbose {
				logf("已输出 %d 行...", nWritten)
			}
		}
		// 序列同步：setval 在数据之后执行（DDL 与 SQL 分开导入时自增列仍从最大值继续）
		if o.SQL && fields == nil {
			if dbDir := resolveDbDir(o); dbDir != "" {
				_, seqSync := buildDdlStatements(dbDir, tm, pgVersion, isKB)
				for _, s := range seqSync {
					writer.WriteString(s + "\n")
				}
			}
		}
	}
	writer.Flush()
	if o.Verbose {
		logf("完成，共输出 %d 行", nWritten)
	}
	return nil
}

// ---------- 批量导出（--tables 用户表 / --all-tables 全部表 / --schema 指定模式）----------
func runAllTables(o *Options) error {
	// 强制约束：批量导出必须指定导出类型 --sql 或 --data
	if !o.SQL && !o.Data {
		return fmt.Errorf("--schema/--tables/--all-tables 批量导出必须指定导出类型: --sql 或 --data")
	}
	// 强制约束：批量导出必须使用 -o 指定输出目录
	if o.Output == "" {
		return fmt.Errorf("--schema/--tables/--all-tables 批量导出多表必须使用 -o 指定输出目录")
	}
	outDir := o.Output
	if fi, err := os.Stat(outDir); err != nil || !fi.IsDir() {
		if !strings.HasSuffix(outDir, "/") {
			return fmt.Errorf("-o 必须是已存在的目录（多表导出）: %s", outDir)
		}
		if err := os.MkdirAll(outDir, 0755); err != nil {
			return fmt.Errorf("无法创建输出目录: " + err.Error())
		}
	}
	dbDir := resolveDbDir(o)
	if dbDir == "" {
		return fmt.Errorf("批量导出需要数据库目录（位置参数目录或 --datadir+--db-oid）")
	}
	_, tableList := autoDiscoverAllTables(dbDir, o.PageSize)
	// 方案 C：目录动态类型解码器（v1.0.16）
	ver := detectPgVersion(dbDir)
	if ver == 0 {
		ver = 12
	}
	kb := isKingbaseDatadir(dbDir)
	initDynamicDecoders(dbDir, ver, kb)
	var failed []string
	exported := 0
	skipped := 0
	for _, tm := range tableList {
		fn := tm.FullName()
		// schema 过滤（精确前缀 schema.）
		if o.Schema != "" && !strings.HasPrefix(fn, o.Schema+".") {
			skipped++
			continue
		}
		// 用户表过滤（--tables 默认排除系统对象；--all-tables 含系统对象）
		if !o.AllTables && isSystemSchema(fn) {
			skipped++
			continue
		}
		// 只导出普通表/叶子分区（relkind=r）；跳过 p/i/t/S/v/m/f 等无独立数据文件的对象
		if tm.RelKind != "r" || tm.RelFileNode == 0 {
			skipped++
			continue
		}
		if o.Verbose {
			logf("批量导出表: %s", fn)
		}
		if err := exportOneTable(o, tm); err != nil {
			logf("表 %s 导出失败: %v", fn, err)
			failed = append(failed, fn)
			continue
		}
		exported++
	}
	logf("批量导出完成: 成功 %d 张表, 跳过 %d, 失败 %d", exported, skipped, len(failed))
	if len(failed) > 0 {
		return fmt.Errorf("以下表导出失败: %s", strings.Join(failed, ", "))
	}
	return nil
}

func nowMs() int64 {
	return timeNowMs()
}

func normalizeEncoding(enc string) string {
	e := strings.ToLower(strings.TrimSpace(enc))
	switch e {
	case "utf8", "utf-8", "unicode", "utf_8":
		return "utf-8"
	case "latin1", "latin-1", "iso8859-1", "iso-8859-1", "latin_1", "sql_ascii", "sql-ascii":
		return "latin1"
	case "gbk", "gb2312", "936":
		return "gbk"
	case "gb18030":
		return "gb18030"
	}
	return e
}

func resolveDbDir(o *Options) string {
	if o.Datadir != "" {
		if o.DBOID != 0 {
			return filepath.Join(o.Datadir, "base", strconv.Itoa(o.DBOID))
		}
		return o.Datadir
	}
	if o.DataFile != "" {
		// 数据文件可能是 base/<dboid>/<rel> 或 base/<dboid> 或 datadir
		d := filepath.Dir(o.DataFile)
		if fi, err := os.Stat(o.DataFile); err == nil && fi.IsDir() {
			return o.DataFile
		}
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			// 检查 d 是否为数据库目录（含 1247/1259）
			if _, err := os.Stat(filepath.Join(d, "1259")); err == nil {
				return d
			}
			if _, err := os.Stat(filepath.Join(d, "1247")); err == nil {
				return d
			}
			// 可能是 datadir/base/<dboid> 的父级
			if _, err := os.Stat(filepath.Join(d, "global")); err == nil {
				return d
			}
		}
		// 向上找
		cur := filepath.Dir(o.DataFile)
		for range 4 {
			if _, err := os.Stat(filepath.Join(cur, "1259")); err == nil {
				return cur
			}
			if _, err := os.Stat(filepath.Join(cur, "PG_VERSION")); err == nil {
				return filepath.Join(cur, "base", strconv.Itoa(o.DBOID))
			}
			parent := filepath.Dir(cur)
			if parent == cur {
				break
			}
			cur = parent
		}
	}
	return ""
}

// resolveDataRoot：定位数据根目录（含 global/ 与 PG_VERSION/SYS_VERSION）
func resolveDataRoot(o *Options) string {
	if o.Datadir != "" {
		if fi, err := os.Stat(o.Datadir); err == nil && fi.IsDir() {
			return o.Datadir
		}
	}
	if o.DataFile != "" {
		cur := o.DataFile
		if fi, err := os.Stat(cur); err == nil && !fi.IsDir() {
			cur = filepath.Dir(cur)
		}
		for range 6 {
			if fi, err := os.Stat(filepath.Join(cur, "global")); err == nil && fi.IsDir() {
				return cur
			}
			if _, err := os.Stat(filepath.Join(cur, "PG_VERSION")); err == nil {
				return cur
			}
			if _, err := os.Stat(filepath.Join(cur, "SYS_VERSION")); err == nil {
				return cur
			}
			parent := filepath.Dir(cur)
			if parent == cur {
				break
			}
			cur = parent
		}
	}
	return ""
}

// resolveOutputPath：解析 --output 目标路径（对齐 Python _resolve_output_path）
//   - output 以 / 结尾或已是目录 → <目录>/<schema>.<table>.<ext>
//   - output 带任意扩展名 → 视为完整文件路径直接使用
//   - 否则 → 视为文件前缀，追加 .<ext>
func resolveOutputPath(output string, tm *TableMeta, fileType string) string {
	ext := "txt"
	switch fileType {
	case "sql":
		ext = "sql"
	case "ddl":
		ext = "ddl"
	case "csv":
		ext = "csv"
	}
	base := tm.Schema + "." + tm.RelName + "." + ext
	if strings.HasSuffix(output, "/") {
		return filepath.Join(output, base)
	}
	if fi, err := os.Stat(output); err == nil && fi.IsDir() {
		return filepath.Join(output, base)
	}
	if filepath.Ext(output) != "" {
		return output
	}
	return output + "." + ext
}

// 系统 schema 前缀（错误提示时过滤，避免刷屏）
func isSystemSchema(name string) bool {
	dot := strings.IndexByte(name, '.')
	if dot < 0 {
		return true // 无 schema 前缀（如 relfilenode 数字键）视为系统
	}
	s := name[:dot]
	switch s {
	case "pg_catalog", "pg_toast", "information_schema", "sys_catalog",
		"sysaudit", "sysmac", "kdb_schedule", "anon", "src_restrict", "sys":
		return true
	}
	return strings.HasPrefix(s, "SYS_HM") || strings.HasPrefix(s, "sys_")
}

func listTableNames(tables map[string]*TableMeta) string {
	names := make([]string, 0)
	user := make([]string, 0)
	for n := range tables {
		if isSystemSchema(n) {
			names = append(names, n)
		} else {
			user = append(user, n)
		}
	}
	sortStrings(user)
	sortStrings(names)
	if len(user) > 0 {
		return strings.Join(user, ", ") + " (另有 " + strconv.Itoa(len(names)) + " 张系统表)"
	}
	return strings.Join(names, ", ")
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// ---------- list-db ----------
func listDatabases(o *Options) {
	var baseDir string
	if o.DataFile != "" {
		baseDir = o.DataFile
	} else if o.Datadir != "" {
		baseDir = filepath.Join(o.Datadir, "base")
	} else {
		errorExit("--list-db 需要指定数据目录路径 (位置参数或 --datadir)")
	}
	fi, err := os.Stat(baseDir)
	if err != nil || !fi.IsDir() {
		errorExit("目录不存在: " + baseDir)
	}
	baseDir, _ = filepath.Abs(baseDir)
	pgdata := filepath.Dir(baseDir)
	dbOIDs := []int{}
	entries, _ := os.ReadDir(baseDir)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if v, err := strconv.Atoi(e.Name()); err == nil {
			dbOIDs = append(dbOIDs, v)
		}
	}
	sortInts(dbOIDs)
	if len(dbOIDs) == 0 {
		logf("未在 base/ 下找到数据库目录")
		return
	}
	// 解析 pg_database
	dbNames := map[int]string{}
	gf := filepath.Join(pgdata, "global", "1262")
	if fi, err := os.Stat(gf); err == nil && fi.Mode().IsRegular() {
		version := detectPgVersion(pgdata)
		if version == 0 {
			version = 16
		}
		isKB := isKingbaseDatadir(pgdata)
		for tup := range iterSysTuples(gf, version, isKB) {
			layout := withOID([]ColLen{{64, false, "c"}})
			nulls := tup.getNulls()
			fields := extractFieldsDirect(tup.Raw, tup.THoff, nulls, layout)
			if len(fields) < 2 || fields[0] == nil || fields[1] == nil {
				continue
			}
			oid := int(binaryLEU32(*fields[0]))
			name := cstring(*fields[1], 0)
			if oid != 0 && name != "" {
				dbNames[oid] = name
			}
		}
	}
	fmt.Printf("%-12s %-25s %s\n", "OID", "数据库名称", "目录路径")
	fmt.Println(strings.Repeat("-", 70))
	for _, oid := range dbOIDs {
		name, ok := dbNames[oid]
		if !ok {
			name = "(未知)"
		}
		fmt.Printf("%-12d %-25s %s\n", oid, name, filepath.Join(baseDir, strconv.Itoa(oid)))
	}
}

func binaryLEU32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

func sortInts(s []int) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// ---------- list-tables-db / list-tables-all ----------
// includeSystem=false：只列用户对象（默认）；true：列出全部对象（含系统对象）
func listTablesInDB(o *Options, includeSystem bool) {
	dbDir := resolveDbDir(o)
	if dbDir == "" {
		errorExit("--list-tables-db/--list-tables-all 需要数据库目录")
	}
	ps := o.PageSize
	_, tables := autoDiscoverAllTables(dbDir, ps)
	// 按表名排序，输出稳定
	byName := map[string]*TableMeta{}
	names := make([]string, 0, len(tables))
	for _, tm := range tables {
		fn := tm.FullName()
		if includeSystem || !isSystemSchema(fn) {
			if _, ok := byName[fn]; !ok {
				byName[fn] = tm
				names = append(names, fn)
			}
		}
	}
	sortStrings(names)
	type trow struct {
		name string
		rel  uint32
		kind string
		n    int
	}
	rows := make([]trow, 0, len(names))
	for _, n := range names {
		tm := byName[n]
		ncol := 0
		for _, c := range tm.Columns {
			if !c.Dropped {
				ncol++
			}
		}
		rows = append(rows, trow{tm.FullName(), tm.RelFileNode, tm.RelKind, ncol})
	}
	// 定长列（relfilenode/relkind/列数）动态对齐在前，不定长的表名放最后
	w2, w3 := 11, 7 // relfilenode/relkind（字节宽）
	for _, r := range rows {
		l := len(strconv.Itoa(int(r.rel)))
		if l > w2 {
			w2 = l
		}
		if len(r.kind) > w3 {
			w3 = len(r.kind)
		}
	}
	w4 := len(strconv.Itoa(len(rows)))
	if len(rows) > 0 {
		for _, r := range rows {
			l := len(strconv.Itoa(r.n))
			if l > w4 {
				w4 = l
			}
		}
	}
	fmt.Printf("%-*s %-*s %-*s  %s\n", w2, "relfilenode", w3, "relkind", w4, "列数", "表名")
	fmt.Println(strings.Repeat("-", w2+w3+w4+4))
	for _, r := range rows {
		fmt.Printf("%-*d %-*s %-*d  %s\n", w2, r.rel, w3, r.kind, w4, r.n, r.name)
	}
}

// GenerateDDL：生成 CREATE TABLE（枚举列前置 CREATE TYPE）
func (tm *TableMeta) GenerateDDL() string {
	var sb strings.Builder
	seen := map[uint32]bool{}
	for _, c := range tm.Columns {
		if c.Dropped {
			continue
		}
		m, ok := enumMap[c.TypeOID]
		if !ok || seen[c.TypeOID] {
			continue
		}
		seen[c.TypeOID] = true
		tname := tm.ColTypeSQL(c)
		labels := make([]string, 0, len(m))
		for _, mem := range m {
			labels = append(labels, "'"+strings.ReplaceAll(mem.Label, "'", "''")+"'")
		}
		sb.WriteString("CREATE TYPE \"" + tname + "\" AS ENUM (" + strings.Join(labels, ", ") + ");\n")
	}
	var cols []string
	for _, c := range tm.Columns {
		if c.Dropped {
			continue
		}
		s := "  \"" + c.Name + "\" " + tm.ColTypeSQL(c)
		if c.NotNull {
			s += " NOT NULL"
		}
		cols = append(cols, s)
	}
	if len(tm.PrimaryKey) > 0 {
		pks := make([]string, 0, len(tm.PrimaryKey))
		for _, p := range tm.PrimaryKey {
			pks = append(pks, quoteName(p))
		}
		cols = append(cols, "  PRIMARY KEY ("+strings.Join(pks, ", ")+")")
	}
	return sb.String() + "CREATE TABLE \"" + tm.Schema + "\".\"" + tm.RelName + "\" (\n" +
		strings.Join(cols, ",\n") + "\n);"
}

// ColTypeSQL：列的 SQL 类型表示（含 typmod）
// 内置类型名（对齐 Python types.py TYPE_NAMES；catalog-json 离线模式兜底）
var builtinTypeNames = map[uint32]string{
	16: "bool", 17: "bytea", 18: "char", 19: "name", 20: "bigint", 21: "smallint",
	23: "integer", 24: "regproc", 25: "text", 26: "oid", 27: "tid", 28: "xid",
	29: "cid", 114: "json", 142: "xml", 199: "json[]", 700: "real", 701: "double precision",
	705: "unknown", 790: "money", 829: "macaddr", 869: "inet", 650: "cidr",
	774: "macaddr8", 1042: "character", 1043: "character varying", 1082: "date",
	1083: "time", 1114: "timestamp", 1184: "timestamptz", 1186: "interval",
	1266: "timetz", 1560: "bit", 1562: "varbit", 1700: "numeric", 2950: "uuid",
	3802: "jsonb", 22: "int2vector", 30: "oidvector", 7050: "pg_node_tree",
	32: "pg_ddl_command",
	1000: "bool[]", 1001: "bytea[]", 1002: "char[]", 1003: "name[]", 1005: "smallint[]",
	1006: "int2vector[]", 1007: "integer[]", 1008: "regproc[]", 1009: "text[]",
	1010: "tid[]", 1011: "xid[]", 1012: "cid[]", 1013: "oidvector[]", 1014: "bpchar[]",
	1015: "varchar[]", 1016: "int8[]", 1017: "point[]", 1018: "lseg[]", 1019: "path[]",
	1020: "box[]", 1021: "float4[]", 1022: "float8[]", 1023: "interval[]",
	1024: "timetz[]", 1025: "time[]", 1026: "timestamp[]", 1027: "timestamptz[]",
	1028: "oid[]", 1030: "macaddr[]", 1031: "inet[]", 1033: "aclitem[]",
	1040: "macaddr8[]", 1041: "bit[]", 1044: "bool[]", 1182: "date[]", 1183: "time[]",
	1185: "timestamp[]", 1187: "timestamptz[]", 1231: "numeric[]", 1270: "tid[]",
	1561: "bit[]", 1563: "varbit[]", 2951: "uuid[]", 3642: "xml[]", 3807: "jsonb[]",
}

func (tm *TableMeta) ColTypeSQL(col ColumnDef) string {
	base := ""
	if tm.TypeNames != nil {
		if n, ok := tm.TypeNames[col.TypeOID]; ok {
			base = n
		}
	}
	if base == "" {
		if n, ok := builtinTypeNames[col.TypeOID]; ok {
			base = n
		} else {
			base = fmt.Sprintf("oid:%d", col.TypeOID)
		}
	}
	switch col.TypeOID {
	case BPCHAROID, VARCHAROID:
		if col.TypMod >= 4 {
			return fmt.Sprintf("%s(%d)", base, col.TypMod-4)
		}
		return base
	case NUMERICOID:
		if col.TypMod >= 4 {
			tmp := col.TypMod - 4
			prec := (tmp >> 16) & 0xFFFF
			scale := tmp & 0xFFFF
			return fmt.Sprintf("numeric(%d,%d)", prec, scale)
		}
		return "numeric"
	case 1560, 1562: // bit/varbit
		if col.TypMod != 0 {
			return fmt.Sprintf("%s(%d)", base, col.TypMod)
		}
		return base
	case TIMEOID, TIMETZOID, TIMESTAMPOID, TIMESTAMPTZOID:
		if col.TypMod >= 0 {
			return fmt.Sprintf("%s(%d)", base, col.TypMod)
		}
		return base
	}
	return base
}

func timeNowMs() int64 {
	return time.Now().UnixNano() / 1e6
}
