// Author: raysuen
package app

import (
	"bufio"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"pg2sql/internal/catalog"
	"pg2sql/internal/charset"
	"pg2sql/internal/config"
	"pg2sql/internal/datatype"
	"pg2sql/internal/engine"
	"pg2sql/internal/integrity"
	"pg2sql/internal/logx"
	"pg2sql/internal/paid"
	"pg2sql/internal/util"
	"strings"
	"sync/atomic"
)

func exportOneTable(o *config.Options, tm *engine.TableMeta, parent *engine.TableMeta) error {
	// ---- 定位表文件（支持目录 + --table-name）----
	tablePath := o.DataFile
	dbDir := resolveDbDir(o)
	if tablePath == "" {
		// --datadir + --db-oid 模式：以数据库目录为基准
		tablePath = dbDir
	}
	if fi, err := os.Stat(tablePath); err == nil && fi.IsDir() && tm.RelFileNode != 0 {
		cand := config.ResolveTableFilePath(o, dbDir, tm.RelFileNode, tm.TableSpace)
		if cand != "" {
			if fi2, err := os.Stat(cand); err == nil && fi2.Mode().IsRegular() {
				tablePath = cand
			} else if tm.TableSpace != 0 {
				return fmt.Errorf("表 %s 位于表空间 %d，但数据文件不存在: %s（pg_tblspc 软链接可能悬空或目录缺失，v1.0.34）", tm.FullName(), tm.TableSpace, cand)
			}
		} else if tm.TableSpace != 0 {
			return fmt.Errorf("表 %s 位于表空间 %d，但未找到表空间数据目录 pg_tblspc/%d（软链接悬空或目录缺失，v1.0.34）", tm.FullName(), tm.TableSpace, tm.TableSpace)
		}
	}

	// ---- v1.0.55 B2：表数据文件最终校验（此前缺失 → 静默导出 0 行、退出码 0）----
	// 两种实测静默场景：① 输入为 fsm/vm 辅助文件（被当堆页解析 → 0 行）；②
	// --datadir 模式下无效位置参数被当作 tablePath（stat 失败后一路走到探测失败或 0 行）。
	// 修法：fsm/vm 后缀自动纠正到真实数据文件（纠正不了则报错）；文件不存在时按
	// relfilenode 重定位；重定位仍失败 → 硬报错。分区父表（relkind='p'）无独立
	// 数据文件，不在此列。
	if tm.RelKind != "p" {
		base := filepath.Base(tablePath)
		isAuxFile := strings.HasSuffix(base, "_fsm") || strings.HasSuffix(base, "_vm") || strings.HasSuffix(base, "_init")
		if isAuxFile && tm.RelFileNode != 0 && dbDir != "" {
			if cand := config.ResolveTableFilePath(o, dbDir, tm.RelFileNode, tm.TableSpace); cand != "" {
				if fi2, err := os.Stat(cand); err == nil && fi2.Mode().IsRegular() {
					logx.Logf("提示: 输入为 FSM/VM 辅助文件（非表数据文件），已自动改用真实数据文件 %s", cand)
					tablePath = cand
					isAuxFile = false
				}
			}
		}
		if isAuxFile {
			return fmt.Errorf("输入文件是 FSM/VM 辅助文件，不是表数据文件: %s（请改用不带后缀的 relfilenode 数据文件，或用 --datadir + --db-oid + --table-name 指定）", tablePath)
		}
		if tm.RelFileNode != 0 {
			if fi, err := os.Stat(tablePath); err != nil || !fi.Mode().IsRegular() {
				if dbDir != "" {
					if cand := config.ResolveTableFilePath(o, dbDir, tm.RelFileNode, tm.TableSpace); cand != "" {
						if fi2, err := os.Stat(cand); err == nil && fi2.Mode().IsRegular() {
							logx.Logf("提示: 指定路径不存在或不可读（%s），已按 relfilenode %d 自动改用 %s", tablePath, tm.RelFileNode, cand)
							tablePath = cand
						}
					}
				}
				if fi2, err := os.Stat(tablePath); err != nil || !fi2.Mode().IsRegular() {
					return fmt.Errorf("表 %s 数据文件不存在或不可读: %s（请核实 --table-name/--db-oid 与数据目录；数据库实例在线时新写入可能未 CHECKPOINT，relfilenode 可能尚未落盘）", tm.FullName(), tablePath)
				}
			}
		}
	}

	// ---- v1.0.60 修复（P2-12）：主段 0 字节 → Total=0、0 行、exit 0 ----
	// 表的数据文件存在但为空，通常是拷贝不全/被截断；导出会"成功"产出 0 行，
	// 对取证与迁移都是静默错误。给出明确告警（表本可空，故不硬失败）。
	if fi, err := os.Stat(tablePath); err == nil && fi.Mode().IsRegular() && fi.Size() == 0 && tm.RelKind != "p" {
		logx.Logf("警告: 表 %s 的主数据文件为 0 字节: %s —— 若该表本应有数据，说明文件被截断/未落盘，导出将得到 0 行", tm.FullName(), tablePath)
	}

	// ---- 页大小 ----
	ps := o.PageSize
	if ps == 0 {
		ps = engine.ProbePageSize(tablePath)
	}
	if ps == 0 {
		// v1.0.52：分区父表（relkind='p'）无独立数据文件，页大小仅占位
		if tm.RelKind == "p" {
			ps = 8192
		} else {
			return fmt.Errorf("页大小探测失败，请用 --page-size 指定")
		}
	}

	// ---- v1.0.52 编码：先复位库级基线，再做表级精化 ----
	// 批量模式各表依次进入本函数，前一张表的表级覆盖不能泄漏到本表。
	if o.EncBase != "" {
		charset.SetTextEncoding(o.EncBase)
	}
	refineTableEncoding(o, dbDir, tm, tablePath, ps)
	if tm.EncOverride != "" {
		charset.SetTextEncoding(normalizeEncoding(tm.EncOverride))
	}

	// ---- TOAST ----
	toastPath := o.Toast
	if toastPath == "" && tm.ToastRelID != 0 {
		dbDir := resolveDbDir(o)
		if dbDir != "" {
			// reltoastrelid 是 OID；TRUNCATE/重建过的表其 relfilenode != OID，
			// 磁盘文件按 relfilenode 命名。优先用 ToastRelFile（自动发现已解析），
			// catalog-json 旧文件无该字段时回退 OID（原逻辑）。
			rf := tm.ToastRelFile
			if rf == 0 {
				rf = tm.ToastRelID
			}
			// toast 与主表同表空间；ToastTableSpace 解析失败时回退主表表空间（v1.0.34）
			ts := tm.ToastTableSpace
			if ts == 0 {
				ts = tm.TableSpace
			}
			cand := config.ResolveTableFilePath(o, dbDir, rf, ts)
			if fi, err := os.Stat(cand); err == nil && fi.Mode().IsRegular() {
				toastPath = cand
			}
		}
	}
	pgVersion := catalog.DetectPgVersion(filepath.Dir(filepath.Dir(o.DataFile)))
	if pgVersion == 0 {
		pgVersion = catalog.DetectPgVersion(o.Datadir)
	}
	if pgVersion == 0 {
		pgVersion = 12
	}
	isKB := catalog.IsKingbaseDatadir(filepath.Dir(filepath.Dir(o.DataFile)))
	if !isKB {
		isKB = catalog.IsKingbaseDatadir(o.Datadir)
	}
	tm.IsKB = isKB
	// 方案 C：目录动态类型解码器（覆盖 --catalog-json 路径，v1.0.16）
	if dbDir := resolveDbDir(o); dbDir != "" {
		datatype.InitDynamicDecodersOnce(dbDir, pgVersion, isKB)
	}

	// ---- 幽灵列截断 ----
	// 金仓 ALTER 残留 att 行（attnum 超出数据行实际列数）时，按数据行最大列数截断列集，
	// 使导出的列与数据库可见列一致（避免 CSV 列数不匹配导致 COPY 失败、行解析错位丢行）。
	if !o.DDL || o.SQL || o.Data || o.Count {
		if maxn := engine.MaxRowNatts(tablePath, ps, isKB); maxn > 0 && maxn < len(tm.Columns) {
			kept := make([]engine.ColumnDef, 0, maxn)
			for _, c := range tm.Columns {
				if c.AttNum <= maxn {
					kept = append(kept, c)
				}
			}
			if len(kept) > 0 && len(kept) < len(tm.Columns) {
				if o.Verbose {
					logx.Logf("按数据行列数截断幽灵列: %d -> %d 列", len(tm.Columns), len(kept))
				}
				tm.Columns = kept
			}
		}
	}

	// v1.0.39 TDE：构造页解密器（--enckey / --wallet-password）
	// v1.0.56 修复（P3-G11）：本块原在输出文件 os.Create 之后——错密钥时旧版
	// 先把上一次的正确导出文件截断为空再报错退出，用户损失既有成果。
	// 校验只读 tablePath/ps，提前到建文件之前执行。
	dec := paid.BuildPaidDecryptor(o, tm, resolveDbDir(o))
	if dec != nil {
		if err := dec.Init(); err != nil {
			logx.Logf("警告: 表解密初始化失败: %v（按明文继续，可能导出乱码/0 行）", err)
			dec = nil
		} else if err := paid.VerifyDecryptorKey(dec, tablePath, ps, len(tm.Columns)); err != nil {
			// v1.0.46 修复：错密钥不再静默输出乱码行（旧行为：解出 2 行垃圾数据、exit=0）
			return err
		}
	}

	// ---- 输出 ----
	var outFile io.Writer = os.Stdout
	var writer *bufio.Writer
	outPath := "" // 文件模式输出路径（报告/导入脚本用，v1.0.38）
	ddlPath := "" // v1.0.48：DDL 独立文件路径（非空时 DDL 不再写入数据文件）
	var hashH hash.Hash
	if o.Checksum != "" {
		switch o.Checksum {
		case "crc32":
			hashH = crc32.NewIEEE()
		case "md5":
			hashH = md5.New()
		}
	}
	startMs := util.NowMs()
	fileType := "sql"
	if o.Data {
		fileType = "csv"
	} else if o.DDL && !o.SQL && !o.Count && !o.Deleted && !o.OnlyDeleted {
		fileType = "ddl" // 纯 DDL 模式独立后缀，避免覆盖同名 .sql
	}
	if o.Output != "" {
		// 对齐 Python _resolve_output_path：
		//   目录或以 / 结尾 → 目录/schema.table.ext；带扩展名 → 完整路径；否则 → 前缀 + .ext
		outPath = resolveOutputPath(o.Output, tm, fileType)
		if dir := filepath.Dir(outPath); dir != "" && dir != "." {
			os.MkdirAll(dir, 0755)
		}
		// v1.0.48（P3）：--ddl 与其它输出同时开启时，DDL 独立成 .ddl 文件，
		// 数据文件（.sql/.csv/.txt）保持纯净。原实现把 DDL 拼在数据文件头部，
		// CSV 模式下直接产生非法文件（psql 执行报 `syntax error at or near "id"`）。
		if o.DDL && (o.SQL || o.Data || o.Count || o.Deleted || o.OnlyDeleted) {
			ddlPath = resolveOutputPath(o.Output, tm, "ddl")
			if ddlPath == outPath {
				ddlPath = ddlOutputPath(outPath)
				logx.Logf("警告: -o 路径 %s 与 DDL 文件冲突，DDL 改输出到 %s", outPath, ddlPath)
			}
			if dir := filepath.Dir(ddlPath); dir != "" && dir != "." {
				os.MkdirAll(dir, 0755)
			}
		}
		// v1.0.59 修复（P0-3）：-o 指向源堆文件（或其符号链接）时先截断源库
		// 再导出 0 行，属不可逆证据破坏，SameFile 命中即拒绝
		if err := guardOutputAgainstSource(outPath, tablePath, toastPath, o.DataFile); err != nil {
			return err
		}
		f, err := os.Create(outPath)
		if err != nil {
			return fmt.Errorf("无法创建输出文件: " + err.Error())
		}
		defer f.Close()
		if hashH != nil {
			outFile = io.MultiWriter(f, hashH)
		} else {
			outFile = f
		}
		if o.Verbose {
			logx.Logf("输出到: %s", outPath)
		}
	} else if hashH != nil {
		logx.Logf("--checksum 仅对文件输出生效（无 -o 时忽略）")
		hashH = nil
	}
	writer = bufio.NewWriterSize(outFile, 1<<20)
	defer writer.Flush()
	// v1.0.52：分区父表（relkind='p'）无独立数据文件 → 强制按 DDL-only 收尾。
	// 不改动共享 o（批量 --jobs 并发下多个表共用 o），用局部布尔参与后续判定。
	isPartParent := tm.RelKind == "p"
	// v1.0.35：写盘错误检测——磁盘满/IO 失败时返回错误而非静默截断输出
	writeErr := error(nil)
	write := func(s string) {
		if writeErr != nil {
			return
		}
		if _, err := writer.WriteString(s); err != nil {
			writeErr = err
		}
	}

	// v1.0.54 修复 C2：SQL 数据文件恒为 UTF-8 字节，必须显式声明 client_encoding。
	// 否则目标库（GBK/GB18030/EUC_CN 等）会按自身编码解释 UTF-8 字节：轻则报
	// invalid byte sequence 中途中断，重则静默双重编码（v1.0.53 实测 200 行全进、
	// 退出码 0、md5 与源库不一致）。pg_dump 亦以 SET 语句开头，此处对齐。
	// v1.0.62 修复（P3）：同时钉住 standard_conforming_strings。QuoteSQLLiteral
	// 按"反斜杠即普通字符"生成字面量（不双写 \），该语义依赖会话设置；
	// 目标端若为 off（PG 9.1 前的默认值、或被 ALTER DATABASE 改过），
	// 含 \ 的文本回灌后会被当转义序列改写。显式 SET 比依赖默认值可靠。
	sqlDataOut := !o.Data && !o.Count && (o.SQL || !o.DDL)
	if sqlDataOut || o.DDL {
		write("SET client_encoding = 'UTF8';\nSET standard_conforming_strings = on;\n")
	}

	// DDL（基础 CREATE TABLE + 扩展：索引/序列默认值/注释）
	if o.DDL {
		var dsb strings.Builder
		// v1.0.54 修复 C1：分区子表的 DDL 是 PARTITION OF 挂靠语句，父表必须先建。
		// 单表模式指定分区父表时，父表 DDL 在此前置输出（批量模式由任务集纳入）。
		if parent != nil && parent.RelKind == "p" {
			dsb.WriteString("-- 分区父表（子表 PARTITION OF 依赖，v1.0.54 自动补出）\n")
			dsb.WriteString(generateDDL(parent) + "\n")
			if dbDir := resolveDbDir(o); dbDir != "" {
				if pstmts, _, _ := catalog.BuildDdlStatements(dbDir, parent, pgVersion, isKB); len(pstmts) > 0 {
					for _, s := range pstmts {
						dsb.WriteString(s + "\n")
					}
				}
			}
		}
		dsb.WriteString(generateDDL(tm) + "\n")
		if dbDir := resolveDbDir(o); dbDir != "" {
			ddlStmts, _, ddlWarn := catalog.BuildDdlStatements(dbDir, tm, pgVersion, isKB)
			if ddlWarn != "" {
				// v1.0.44 P2-4：目录缺失不再静默，给出 DDL 扩展不完整提示
				logx.Logf("警告: %s", ddlWarn)
			}
			for _, s := range ddlStmts {
				dsb.WriteString(s + "\n")
			}
		}
		if ddlPath != "" {
			// v1.0.48：DDL 独立成文件，数据文件不再掺杂建表语句
			// v1.0.59 修复（P0-3）：DDL 文件同样不得指向源数据文件
			if err := guardOutputAgainstSource(ddlPath, tablePath, toastPath, o.DataFile); err != nil {
				return err
			}
			// v1.0.62（P3）：独立 .ddl 文件同样需要钉住会话编码与字符串语义
			ddlBody := "SET client_encoding = 'UTF8';\nSET standard_conforming_strings = on;\n" + dsb.String()
			if err := os.WriteFile(ddlPath, []byte(ddlBody), 0644); err != nil {
				return fmt.Errorf("无法创建 DDL 输出文件: " + err.Error())
			}
			if o.Verbose {
				logx.Logf("DDL 输出到: %s", ddlPath)
			}
			if o.SQL && !o.Data {
				write("-- 建表 DDL 已单独输出到 " + filepath.Base(ddlPath) + "，请先执行该文件再执行本文件\n")
			}
		} else {
			write(dsb.String())
		}
		if o.Verbose {
			logx.Logf("DDL 输出完成")
		}
		// v1.0.54：直接点名分区子表导出时父表 DDL 不在本次输出内，回灌必失败 → 明确提示
		// 批量导出（含单表展开后的批量路径）里父表 DDL 已由任务集带出，不再提示。
		if parent == nil && tm.PartParent != 0 && tm.PartParentQual != "" && !batchExportActive {
			logx.Logf("提示: %s 是分区子表，其 DDL 为 PARTITION OF %s，回灌前需先建父表（可用 --table-name %s 自动带出父表 DDL）",
				tm.FullName(), tm.PartParentQual, tm.PartParentQual)
		}
	}
	if (o.DDL && !o.SQL && !o.Data && !o.Count && !o.Deleted && !o.OnlyDeleted) || isPartParent {
		if err := writer.Flush(); err != nil && writeErr == nil {
			writeErr = err
		}
		if writeErr != nil {
			return fmt.Errorf("写入输出文件失败: %v", writeErr)
		}
		e := integrity.ExportReportEntry{
			Table: tm.FullName(), Format: fileType, Rows: 0, BadBlocks: 0,
			File: outPath, Bytes: util.StatSize(outPath), Checksum: hashSum(o, hashH),
			ElapsedMs: util.NowMs() - startMs, PageSize: ps, Encoding: currentTextEncoding(),
		}
		fillEvidence(&e, tm, o, tablePath, ps)
		reportSink.add(e)
		if o.ImportScript != "none" && outPath != "" && !isPartParent {
			makeImportScript(o, tm, outPath, fileType, nil, "")
		}
		return nil
	}

	// 一次性注入角色映射（解析前单线程阶段；并发解析期只读，避免 data race）
	if tm.RoleMap != nil {
		datatype.SetRoleMap(tm.RoleMap)
	}

	// 构造 engine.RowIter
	ri := &engine.RowIter{
		TM:           tm,
		IncludeDel:   o.Deleted || o.OnlyDeleted,
		OnlyDeleted:  o.OnlyDeleted,
		Limit:        o.Limit,
		PageSize:     ps,
		PGVersion:    pgVersion,
		IsKB:         isKB,
		Path:         tablePath,
		VerboseDebug: o.Verbose,
		Parallel:     o.Parallel,
		Decryptor:    dec,
	}
	// v1.0.40 --progress：数据扫描进度与 ETA（stderr；TTY \r 覆盖，管道换行）
	if o.Progress {
		totalPages := ri.PageCount()
		stop := startProgressReporter(ri, totalPages, startMs)
		defer stop()
	}
	// TOAST 关联
	// v1.0.56 修复（P3-G16）：--count 只统计行数、不解码字段值，外置 TOAST
	// 不参与计数。旧版在 count 分支之前无条件构建 TOAST 索引（大 TOAST 数十
	// GB 白扫一遍），count 模式直接跳过。
	if toastPath != "" && !o.Count {
		tf := &engine.ToastFile{
			Path:      toastPath,
			PageSize:  ps,
			PageCache: map[int]map[int][]byte{},
			CacheMax:  64,
			PgVersion: pgVersion,
			IsKB:      isKB,
			Decryptor: dec,
		}
		if fi, err := os.Stat(toastPath); err == nil && fi.Size() > 0 {
			tps := engine.ProbePageSize(toastPath)
			tf.PageSize = tps
		}
		idxStart := util.NowMs()
		if o.Parallel > 1 {
			// 并行预建索引（goroutine 分页）
			tf.BuildIndexParallel(o.Parallel, o.Verbose)
		} else {
			tf.BuildIndex(o.Verbose)
		}
		if o.Verbose {
			logx.Logf("TOAST 索引预建完成: %d 个 valueid, 耗时 %.1fs", len(tf.PosIndex), float64(util.NowMs()-idxStart)/1000.0)
		}
		if len(tf.PosIndex) == 0 {
			if fi, err := os.Stat(toastPath); err == nil && fi.Size() > 8192 {
				logx.Logf("警告: TOAST 文件非空(%d 字节)但索引中未找到任何 valueid。可能数据库实例在线且最近写入尚未 CHECKPOINT，磁盘 TOAST 索引页未完整落盘，外置列数据可能不完整。建议先对实例执行 CHECKPOINT 后再导出。", fi.Size())
			}
		}
		ri.Toast = tf
	} else if tm.ToastRelID != 0 && !o.Count {
		// v1.0.44 P2-3：TOAST 文件缺失仍建空索引对象，decodeFields 走缺失计数（toast_missing）
		// 并保持 SQL 输出 NULL（可导入）；不静默丢失——报告/警告体现。
		ri.Toast = &engine.ToastFile{
			Path:      "",
			PageSize:  ps,
			PageCache: map[int]map[int][]byte{},
			CacheMax:  64,
			PgVersion: pgVersion,
			IsKB:      isKB,
			Decryptor: dec,
		}
		if o.Verbose {
			logx.Logf("警告: TOAST 文件不存在(relfilenode 缺失)，外置列将导出为 NULL 并在报告中计数 toast_missing")
		}
	}

	// count 模式（v1.0.30：流式计数，无全量收集）
	if o.Count {
		if o.Limit > 0 { // v1.0.55 B3：明确语义，计数不受 limit 影响
			logx.Logf("提示: --count 统计全表行数，--limit %d 不影响计数（仅数据导出生效）", o.Limit)
		}
		count := ri.CountRows()
		write(fmt.Sprintf("-- 总行数: %d\n", count))
		if err := writer.Flush(); err != nil && writeErr == nil {
			writeErr = err
		}
		if o.Verbose {
			logx.Logf("统计完成: %d 行", count)
		}
		// v1.0.46：--count 同样要对密文页告警。加密表在无密钥下页结构被打乱，
		// 行数统计会给出随机值（实测金仓加密 TOAST 表 10 行 → 统计为 1），不能静默。
		engine.WarnIfCipherPages(ri, ps, dec, int64(count))
		if writeErr != nil {
			return fmt.Errorf("写入输出文件失败: %v", writeErr)
		}
		e := integrity.ExportReportEntry{
			Table: tm.FullName(), Format: "count", Rows: int64(count),
			BadBlocks: atomic.LoadInt64(&ri.BadCount), BadPages: ri.BadPagesSnapshot(),
			ToastMissing: ri.ToastMissing(), ToastCorrupt: ri.ToastCorrupt(),
			File: outPath, Bytes: util.StatSize(outPath),
			Checksum: hashSum(o, hashH), ElapsedMs: util.NowMs() - startMs,
			PageSize: ps, Encoding: currentTextEncoding(),
		}
		fillEvidence(&e, tm, o, tablePath, ps)
		reportSink.add(e)
		return nil
	}

	// SQL / DATA 输出
	fields := []string(nil)
	if o.Fields != "" {
		fields = strings.Split(o.Fields, ",")
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}
		// v1.0.42：未命中字段给出警告（防笔误静默丢列）
		known := map[string]bool{}
		for _, c := range tm.Columns {
			if !c.Dropped {
				known[c.Name] = true
			}
		}
		for _, fn := range fields {
			if fn != "" && !known[fn] {
				logx.Logf("警告: 字段 %q 不存在于表 %s，已忽略（可用 --schema-objects 或 DDL 查看字段清单）", fn, tm.FullName())
			}
		}
	}
	nWritten := 0
	if o.Data {
		// v1.0.46 修复：CSV 表头不计入数据行（旧实现把表头算 1 行，导致
		// ① 报告 Rows +1；② 加密表导出 0 行时 nWritten==1，B12 密文告警永不触发）
		headerPending := o.Header
		for line := range engine.ToData(ri, o.Delimiter, fields, o.Header) {
			write(line + "\n")
			if headerPending {
				headerPending = false
				continue
			}
			nWritten++
			if nWritten%10000 == 0 && o.Verbose {
				logx.Logf("已输出 %d 行...", nWritten)
			}
		}
	} else if o.SQL || !o.DDL {
		// v1.0.38：--batch-rows N>1 时多值 INSERT 合并输出（默认 1=逐行单条，输出不变）
		if o.BatchRows > 1 {
			var batchRows int64
			for stmt := range engine.ToSQLBatch(ri, o.CompleteInsert, fields, o.BatchRows, &batchRows) {
				write(stmt + "\n")
				nWritten++
				if nWritten%10000 == 0 && o.Verbose {
					logx.Logf("已输出 %d 条...", nWritten)
				}
			}
			// 报告行数用实际数据行数（多值合并后语句数≠行数）
			if batchRows > 0 {
				nWritten = int(batchRows)
			}
		} else {
			for stmt := range engine.ToSQL(ri, o.CompleteInsert, fields) {
				write(stmt + "\n")
				nWritten++
				if nWritten%10000 == 0 && o.Verbose {
					logx.Logf("已输出 %d 行...", nWritten)
				}
			}
		}
		// 序列同步：setval 在数据之后执行（DDL 与 SQL 分开导入时自增列仍从最大值继续）
		if o.SQL && fields == nil {
			if dbDir := resolveDbDir(o); dbDir != "" {
				_, seqSync, ddlWarn := catalog.BuildDdlStatements(dbDir, tm, pgVersion, isKB)
				if ddlWarn != "" {
					// v1.0.44 P2-4：目录缺失不再静默
					logx.Logf("警告: %s", ddlWarn)
				}
				for _, s := range seqSync {
					write(s + "\n")
				}
			}
		}
	}
	if err := writer.Flush(); err != nil && writeErr == nil {
		writeErr = err
	}
	if writeErr != nil {
		return fmt.Errorf("写入输出文件失败: %v", writeErr)
	}
	// B12 修复：导出 0 行时探测表数据页是否疑似 TDE 密文，
	// 消除"无密钥/错密钥导出 0 行、exit=0、零告警"的静默失败
	engine.WarnIfCipherPages(ri, ps, ri.Decryptor, int64(nWritten))
	if o.Verbose {
		logx.Logf("完成，共输出 %d 行", nWritten)
	}
	// v1.0.38 收尾：导入辅助脚本 + 报告条目
	if o.ImportScript != "none" && outPath != "" {
		makeImportScript(o, tm, outPath, fileType, fields, ddlPath)
	}
	e := integrity.ExportReportEntry{
		Table: tm.FullName(), Format: fileType, Rows: int64(nWritten),
		BadBlocks: atomic.LoadInt64(&ri.BadCount), BadPages: ri.BadPagesSnapshot(),
		ToastMissing: ri.ToastMissing(), ToastCorrupt: ri.ToastCorrupt(),
		File: outPath, Bytes: util.StatSize(outPath),
		Checksum: hashSum(o, hashH), ElapsedMs: util.NowMs() - startMs,
		PageSize: ps, Encoding: currentTextEncoding(),
	}
	fillEvidence(&e, tm, o, tablePath, ps)
	reportSink.add(e)
	// v1.0.53：类型解码覆盖度登记（每表一次，报告聚合为 unknown_types）
	integrity.CollectTypeCoverage(tm)
	return nil
}

// fillEvidence：为报告条目补齐完整性证据（行数/页数对照 + 表级编码覆盖）。
// 判定失败只降级为缺省，不影响导出结果。
func fillEvidence(e *integrity.ExportReportEntry, tm *engine.TableMeta, o *config.Options, tablePath string, ps int) {
	if e == nil || tm == nil {
		return
	}
	ddlOnly := e.Format == "ddl"
	est, chk := integrity.RowsVerdict(e.Rows, tm.RelTuples, o.Limit > 0, o.Deleted || o.OnlyDeleted, ddlOnly)
	e.RowsEstimate = est
	e.RowsCheck = chk
	if !ddlOnly {
		e.PagesExpected = tm.RelPages
		e.PagesActual = integrity.MainForkPages(tablePath, ps)
		e.PagesCheck = integrity.PagesVerdict(tm.RelPages, e.PagesActual)
	}
	e.EncOverride = tm.EncOverride
}

// hashSum：checksum 十六进制串（未启用返回 ""）
func hashSum(o *config.Options, h hash.Hash) string {
	if h == nil {
		return ""
	}
	return o.Checksum + ":" + hex.EncodeToString(h.Sum(nil))
}
