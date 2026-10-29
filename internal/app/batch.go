// Author: raysuen
package app

import (
	"fmt"
	"os"
	"pg2sql/internal/catalog"
	"pg2sql/internal/config"
	"pg2sql/internal/datatype"
	"pg2sql/internal/ddl"
	"pg2sql/internal/engine"
	"pg2sql/internal/integrity"
	"pg2sql/internal/logx"
	"runtime/debug"
	"strings"
	"sync"
)

// ---------- --table-name 逗号分隔多表 + 双引号包裹导出（v1.0.27）----------
// 解析规则（与 SQL 标识符惯例一致）：
//   - 引号外逗号 = 表名边界；引号内逗号 = 字面字符（含逗号表名）
//   - "" 双写 = 字面双引号（PG quote_ident 同规则，如 "a""b" = a"b）
//   - 引号未闭合 → 报错；空表名/空引号 → 过滤；表名 trim 空白
//
// 注：shell 层需用单引号包裹双引号，如 --table-name '"a,b",c'
func splitTableNames(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' && !inQuote:
			inQuote = true
		case c == '"' && inQuote:
			// SQL 风格双写转义："" → 字面 "
			if i+1 < len(s) && s[i+1] == '"' {
				cur.WriteByte('"')
				i++
			} else {
				inQuote = false
			}
		case c == ',' && !inQuote:
			n := strings.TrimSpace(cur.String())
			cur.Reset()
			if n != "" {
				out = append(out, n)
			}
		default:
			cur.WriteByte(c)
		}
	}
	if inQuote {
		return nil, fmt.Errorf("表名引号未闭合: %s", s)
	}
	if n := strings.TrimSpace(cur.String()); n != "" {
		out = append(out, n)
	}
	return out, nil
}

func runNamedTables(o *config.Options, names []string, tables map[string]*engine.TableMeta) error {
	// 多表导出必须指定导出类型 --sql 或 --data
	if !o.SQL && !o.Data {
		return fmt.Errorf("--table-name 多表导出必须指定导出类型: --sql 或 --data")
	}
	// 多表导出必须使用 -o 指定输出目录
	if o.Output == "" {
		return fmt.Errorf("--table-name 多表导出必须使用 -o 指定输出目录")
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
	var missing []string
	// v1.0.36: 分区父表展开（多级递归至叶子）在循环内复用同一 oidMap
	oidMap := map[uint32]*engine.TableMeta{}
	for _, t := range tables {
		if t.OID != 0 {
			oidMap[t.OID] = t
		}
	}
	var expandLeaf func(t *engine.TableMeta) []*engine.TableMeta
	expandLeaf = func(t *engine.TableMeta) []*engine.TableMeta {
		if t == nil || t.RelKind != "p" || len(t.ChildOIDs) == 0 {
			return nil
		}
		var leaves []*engine.TableMeta
		var rec func(x *engine.TableMeta, seen map[uint32]bool)
		rec = func(x *engine.TableMeta, seen map[uint32]bool) {
			if x == nil || seen[x.OID] {
				return
			}
			seen[x.OID] = true
			if len(x.ChildOIDs) == 0 {
				if x.RelKind == "r" {
					leaves = append(leaves, x)
				}
				return
			}
			for _, cid := range x.ChildOIDs {
				rec(oidMap[cid], seen)
			}
		}
		rec(t, map[uint32]bool{})
		return leaves
	}
	// v1.0.38：先收集导出任务（分区父表展开为叶子），再按 --jobs 并行/串行导出
	var tasks []*engine.TableMeta
	integrity.ResetSkip()
	integrity.ResetTypeCoverage()
	for _, n := range names {
		tm := catalog.FindTableInMeta(tables, n)
		if tm == nil {
			missing = append(missing, n)
			integrity.AddSkip(integrity.SkipEntry{Table: n, Reason: "named_missing", Detail: "catalog 中未找到该表名"})
			continue
		}
		if tm.Shared {
			// v1.0.53：共享目录数据文件在 global/，按库定位必然失败 → 明确跳过
			logx.Logf("跳过共享目录 %s（global/ 存储，集群级对象）", tm.FullName())
			integrity.AddSkip(integrity.SkipEntry{Table: tm.FullName(), Schema: tm.Schema, RelKind: tm.RelKind,
				Reason: "shared_catalog", Detail: "集群级共享目录；角色可用 --roles 单独导出"})
			continue
		}
		if tm.RelKind == "p" && len(tm.ChildOIDs) > 0 {
			// v1.0.52：--ddl 时父表以 DDL-only 纳入任务集（PARTITION OF 依赖父表先建）
			if o.DDL {
				tasks = append(tasks, tm)
			}
			leaves := expandLeaf(tm)
			if len(leaves) == 0 {
				logx.Logf("分区父表 %s 无叶子子分区，跳过", tm.FullName())
				integrity.AddSkip(integrity.SkipEntry{Table: tm.FullName(), Schema: tm.Schema, RelKind: tm.RelKind,
					Reason: "partition_no_leaf", Detail: "分区父表无叶子子分区（无数据可导）"})
				continue
			}
			if o.Verbose {
				logx.Logf("分区父表 %s 展开为 %d 个子分区", tm.FullName(), len(leaves))
			}
			tasks = append(tasks, leaves...)
			continue
		}
		tasks = append(tasks, tm)
	}
	exported, failed := exportTasks(o, tasks)
	// B13 修复：writeReport 仅在主流程调用一次（避免批量成功路径双写）；
	// 主流程错误分支会先写报告（保留部分结果）再退出
	if o.ImportScript != "none" {
		makeImportAllScript(outDir, reportSink.snapshot(), o.Header)
	}
	logx.Logf("批量导出完成: 成功 %d 张表, 未匹配 %d, 失败 %d", exported, len(missing), len(failed))
	if len(missing) > 0 {
		return fmt.Errorf("以下表名未找到: %s (可用: %s)", strings.Join(missing, ", "), listTableNames(tables))
	}
	if len(failed) > 0 {
		return fmt.Errorf("以下表导出失败: %s", strings.Join(failed, ", "))
	}
	return nil
}

// exportTasks：任务表列表导出（v1.0.38 --jobs N>1 时 goroutine 池表间并行，每表独立文件；
// 各表失败独立记录不中断；默认 jobs=1 串行，行为与旧版一致）
// batchExportActive：当前进程是否处于批量导出（exportTasks）内。
// v1.0.54：单表模式指定分区父表时会转走批量导出，此时父表 DDL 已由任务集带出，
// exportOneTable 里的「需先建父表」提示不应再出现。
// 只在派发 goroutine 之前置位、导出期只读，无并发写。
var batchExportActive bool

func exportTasks(o *config.Options, tasks []*engine.TableMeta) (exported int, failed []string) {
	batchExportActive = true
	// v1.0.52：分区依赖排序——父表必须先于子表建表（PARTITION OF 语法要求）。
	// 按分区链深度稳定排序：父(0) → 子(1) → 孙(2)；非分区表深度 0 保持原顺序。
	ddl.SortTablesPartitionFirst(tasks)
	jobs := o.Jobs
	if jobs < 1 {
		jobs = 1
	}
	// v1.0.52：表级编码精化预扫。表间编码不一致（混合存储库）时，
	// exportOneTable 内的 setTextEncoding 互相覆盖，jobs>1 并发下 worker
	// 可能用别表的编码解码 → 检出混合即强制串行（仅罕见混合库受影响）。
	if jobs > 1 && len(tasks) > 1 && (o.Encoding == "" || o.Encoding == "auto") {
		if dbDir := resolveDbDir(o); dbDir != "" && catalog.IsKingbaseDatadir(dbDir) {
			ps := o.PageSize
			if ps <= 0 {
				// v1.0.60 修复（P2-2）：预扫此前从 o.PageSize（默认 0）起，首个成功解析
				// 数据文件的任务才顺带探定页大小——其前的任务全部以 ps=0 调
				// ProbeFileEncodingMajority（要求 ps>=8192）恒空转 → 混合编码漏判 →
				// jobs>1 下 worker 中途翻转进程级编码（表级乱码）。预扫前统一探定。
				ps = catalog.ProbeDirPageSize(dbDir)
			}
			seenEnc := map[string]bool{o.EncBase: true}
			mixed := false
			for _, tm := range tasks {
				tp := o.DataFile
				if tp == "" {
					tp = dbDir
				}
				if fi, err := os.Stat(tp); err == nil && fi.IsDir() && tm.RelFileNode != 0 {
					if cand := config.ResolveTableFilePath(o, dbDir, tm.RelFileNode, tm.TableSpace); cand != "" {
						if fi2, err := os.Stat(cand); err == nil && fi2.Mode().IsRegular() {
							tp = cand
							if ps == 0 {
								ps = engine.ProbePageSize(cand)
							}
						}
					}
				}
				refineTableEncoding(o, dbDir, tm, tp, ps)
				enc := tm.EncOverride
				if enc == "" {
					enc = o.EncBase
				}
				if !seenEnc[enc] {
					seenEnc[enc] = true
					if len(seenEnc) > 1 {
						mixed = true
					}
				}
			}
			if mixed {
				logx.Logf("提示: 库内表数据编码不一致（混合存储），表级编码互斥 → 批量导出自动改为串行（--jobs 按 1 执行）")
				jobs = 1
			}
		}
	}
	if jobs == 1 || len(tasks) <= 1 {
		for _, tm := range tasks {
			if o.Verbose {
				logx.Logf("批量导出表: %s", tm.FullName())
			}
			if err := safeExportOneTable(o, tm, nil); err != nil {
				logx.Logf("表 %s 导出失败: %v", tm.FullName(), err)
				recordFailure(tm, err)
				failed = append(failed, tm.FullName())
				continue
			}
			exported++
		}
		return exported, failed
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, jobs)
	for _, tm := range tasks {
		tm := tm
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if o.Verbose {
				logx.Logf("批量导出表: %s", tm.FullName())
			}
			if err := safeExportOneTable(o, tm, nil); err != nil {
				logx.Logf("表 %s 导出失败: %v", tm.FullName(), err)
				recordFailure(tm, err)
				mu.Lock()
				failed = append(failed, tm.FullName())
				mu.Unlock()
				return
			}
			mu.Lock()
			exported++
			mu.Unlock()
		}()
	}
	wg.Wait()
	return exported, failed
}

// safeExportOneTable：v1.0.55 R1 panic 隔离。批量导出此前无 recover，
// 一张表解析中 panic（极端损坏页/未知结构）会让整个进程崩溃、其余表的
// 已导出成果与 report 全部丢失。现单表 panic 转为该表失败记录。
func safeExportOneTable(o *config.Options, tm *engine.TableMeta, parent *engine.TableMeta) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("内部错误(panic): %v", r)
			logx.Logf("表 %s 导出 panic（已隔离，不影响其他表）: %v\n%s", tm.FullName(), r, debug.Stack())
		}
	}()
	return exportOneTable(o, tm, parent)
}

// rowOutputType：是否请求了逐表产物（数据/DDL/计数/审计行）。
// v1.0.62：批量 DDL-only 放开——旧校验只认 --sql/--data，导致
// `--schema s --ddl --full-schema`（不带数据）被拒，批量建表 DDL 根本无法产出。
func rowOutputType(o *config.Options) bool {
	return o.SQL || o.Data || o.DDL || o.Count || o.Deleted || o.OnlyDeleted
}

// ---------- 批量导出（--tables 用户表 / --all-tables 全部表 / --schema 指定模式）----------
func runAllTables(o *config.Options) error {
	// 强制约束：批量导出至少要有一件产物（逐表行/DDL，或库级辅助产物）
	if !rowOutputType(o) && !o.FullSchema && !o.LargeObjects && !o.Roles {
		return fmt.Errorf("--schema/--tables/--all-tables 批量导出必须指定输出类型: --sql / --data / --ddl（或 --full-schema / --large-objects / --roles）")
	}
	doRows := rowOutputType(o)
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
	// v1.0.46：批量导出同样需要库编码探测（旧实现缺失 → 非 UTF-8 库中文全乱码）
	applyAutoEncoding(o, dbDir)
	o.EncBase = currentTextEncoding()
	_, tableList := catalog.AutoDiscoverAllTables(dbDir, o.PageSize)
	// 方案 C：目录动态类型解码器（v1.0.16）
	ver := catalog.DetectPgVersion(dbDir)
	if ver == 0 {
		ver = 12
	}
	kb := catalog.IsKingbaseDatadir(dbDir)
	datatype.InitDynamicDecodersOnce(dbDir, ver, kb)
	// v1.0.38：先收集任务再按 --jobs 并行/串行导出
	var tasks []*engine.TableMeta
	skipped := 0
	// v1.0.53：跳过清单每次运行重建（报告自证"少了哪些、为什么少"）
	integrity.ResetSkip()
	integrity.ResetTypeCoverage()
	for _, tm := range tableList {
		fn := tm.FullName()
		// schema 过滤（精确前缀 schema.）
		if o.Schema != "" && !strings.HasPrefix(fn, o.Schema+".") {
			skipped++
			integrity.AddSkip(integrity.SkipEntry{Table: fn, Schema: tm.Schema, RelKind: tm.RelKind,
				Reason: "schema_filter", Detail: "--schema " + o.Schema + " 之外的表"})
			continue
		}
		// 用户表过滤（--tables 默认排除系统对象；--all-tables 含系统对象）
		if !o.AllTables && isSystemSchema(fn) {
			skipped++
			integrity.AddSkip(integrity.SkipEntry{Table: fn, Schema: tm.Schema, RelKind: tm.RelKind,
				Reason: "system_schema", Detail: "系统模式对象（加 --all-tables 可导出）"})
			continue
		}
		// 只导出普通表/叶子分区（relkind=r）；跳过 i/t/S/v/m/f 等无独立数据文件的对象。
		// v1.0.52：分区父表（relkind=p）在 --ddl 时以 DDL-only 形式纳入任务集
		// （PARTITION OF 子表 DDL 依赖父表先建，父表缺失则回灌失败）。
		// 不依赖 RelFileNode==0（catalog.ClassFields 固定布局对 PG17 p 表的 relfilenode 读取有偏差）。
		if tm.RelKind == "p" {
			if o.DDL {
				tasks = append(tasks, tm)
			} else {
				skipped++
				integrity.AddSkip(integrity.SkipEntry{Table: fn, Schema: tm.Schema, RelKind: tm.RelKind,
					Reason: "partition_parent", Detail: "分区父表无自身存储，叶子子表已单独导出（--ddl 时输出父表定义）"})
			}
			continue
		}
		if tm.RelKind != "r" || tm.RelFileNode == 0 {
			skipped++
			reason, detail := "relkind", ""
			if tm.RelKind == "r" && tm.RelFileNode == 0 {
				reason, detail = "no_storage", "relfilenode=0（无数据文件）"
			} else {
				detail = "非行存表（" + integrity.RelKindName(tm.RelKind) + "）"
			}
			integrity.AddSkip(integrity.SkipEntry{Table: fn, Schema: tm.Schema, RelKind: tm.RelKind,
				Reason: reason, Detail: detail})
			continue
		}
		// v1.0.53：共享目录（relisshared）数据文件在 <PGDATA>/global/，按库定位必然失败；
		// 明确记为跳过而不是让它"导出失败"，避免 --all-tables 无谓非零退出。
		if tm.Shared {
			skipped++
			integrity.AddSkip(integrity.SkipEntry{Table: fn, Schema: tm.Schema, RelKind: tm.RelKind,
				Reason: "shared_catalog",
				Detail: "集群级共享目录（global/ 存储，按库导出的语义不适用）；角色可用 --roles 单独导出"})
			continue
		}
		tasks = append(tasks, tm)
	}
	var exported, failedCount int
	var failed []string
	if doRows {
		exported, failed = exportTasks(o, tasks)
		failedCount = len(failed)
	}
	// B13 修复：writeReport 仅在主流程调用一次（避免批量成功路径双写）
	if o.ImportScript != "none" {
		makeImportAllScript(outDir, reportSink.snapshot(), o.Header)
	}
	if doRows {
		logx.Logf("批量导出完成: 成功 %d 张表, 跳过 %d, 失败 %d", exported, skipped, failedCount)
	} else {
		logx.Logf("批量任务集扫描完成: 命中 %d 张表（本次仅导出库级辅助产物，未逐表输出）, 跳过 %d", len(tasks), skipped)
	}
	// v1.0.53：显式给了 --schema 却一张都没匹配上，多半是模式名写错——
	// 旧行为是"成功 0 张表 / 跳过 N"，退出码 0，现场很容易当成"这库就是空的"。
	if doRows && len(tasks) == 0 && len(failed) == 0 && o.Schema != "" {
		return fmt.Errorf("--schema %s 未匹配到任何可导出表（检查模式名大小写/是否存在；用 --list-tables-db 查看库内表清单）", o.Schema)
	}
	if len(failed) > 0 {
		return fmt.Errorf("以下表导出失败: %s", strings.Join(failed, ", "))
	}
	return nil
}
