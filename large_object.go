// pg2sql 大对象导出（--large-objects / --large-object-oids，v1.0.40）
// 从 pg_largeobject(2613) 读取大对象分页数据（含 TOAST 外联解析），
// 从 pg_largeobject_metadata(2614) 读取 owner 元数据，输出可导入 SQL。
// 导入语句采用 pg_catalog.lo_from_bytea(loid, '\x...')：单语句原子写入整个大对象
// （PG 9.4+ 与金仓 V8/V9 均支持；lowrite 多块方案需跨语句 fd，psql 单语句自动提交下不可靠）。
package main

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// largeObjectEntry：单个大对象（DataHex 为全部 pageno 按序拼接的 hex 字符串，不含 \x 前缀）
type largeObjectEntry struct {
	OID      uint32
	OwnerOID uint32 // 0=未知
	DataHex  string
}

// largeObjectRows：从 pg_largeobject 扫描全部 (loid, pageno, dataHex) 行
func scanLargeObjectRows(ri *RowIter, emit func(loid uint32, pageno uint32, dataHex string) bool) {
	npages := ri.pageCount()
	if npages == 0 {
		return
	}
	buf := make([]byte, ri.pageSize)
	base := 0
	for _, seg := range segFiles(ri.path) {
		f, err := os.Open(seg)
		if err != nil {
			continue
		}
		pageno := base
		for {
			n, err := f.Read(buf)
			if err != nil || n < ri.pageSize {
				break
			}
			if ri.decryptor != nil {
				if _, derr := ri.decryptor.DecryptPage(buf[:n]); derr != nil {
					break
				}
			}
			items := ri.iterTuples(pageno, buf)
			for _, it := range items {
				tup := it[1].(*HeapTuple)
				if !tup.isLive() {
					continue
				}
				// 固定 3 列直解（loid/pageno/data）：金仓 V8/V9 的 pg_attribute
				// 把系统列（tableoid/cmax/xmax/cmin/xmin/ctid）列为正 attnum 1-6，
				// 用 tm.Columns 会错位；行数据按是否有 oid 系统列定起点。
				hasOIDSys := false
				for _, c := range ri.tm.Columns {
					if c.Name == "oid" {
						hasOIDSys = true
						break
					}
				}
				base := tup.THoff
				if hasOIDSys {
					base += 4
				}
				nulls := tup.getNulls()
				fields := extractFieldsDirect(tup.Raw, base, nulls, []ColLen{
					{4, false, "i"}, {4, false, "i"}, {0, true, "i"},
				})
				if len(fields) < 3 || fields[0] == nil || fields[1] == nil || fields[2] == nil {
					continue
				}
				loid := u32(*fields[0], 0)
				pno := u32(*fields[1], 0)
				rawB := *fields[2]
				// bytea 外联（TOAST）重组
				if len(rawB) > 0 {
					_, isExt, ext := checkExternal(rawB)
					if isExt && ri.toast != nil {
						reassembled := ri.toast.fetchAndReassemble(ext.ValueID, 0)
						if reassembled == nil {
							continue
						}
						expected := int(ext.RawSize) - 4
						if ext.Compressed {
							data := toastDecompress(reassembled, expected, ext.Method)
							if data == nil {
								continue
							}
							rawB = data
						} else if len(reassembled) != expected {
							continue
						} else {
							rawB = reassembled
						}
						rawB = rebuildVarlena(rawB)
					} else if isExt {
						continue
					}
				}
				decoded := decodeValue(17, rawB)
				if !strings.HasPrefix(decoded, `\x`) {
					continue
				}
				if !emit(loid, pno, decoded[2:]) {
					f.Close()
					return
				}
			}
			pageno++
		}
		f.Close()
		if fi, err := os.Stat(seg); err == nil {
			base += int(fi.Size() / int64(ri.pageSize))
		}
	}
}

func ptrStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// collectLargeObjects：收集大对象（按 loid 聚合、pageno 排序；filter 为空=全部）
func collectLargeObjects(o *Options, dbDir string, version int, isKB bool, pageSize int, filter map[uint32]bool) ([]*largeObjectEntry, error) {
	_, tables := autoDiscoverAllTables(dbDir, pageSize)
	var loTM *TableMeta
	for _, tm := range tables {
		if tm.RelName == "pg_largeobject" || tm.RelName == "sys_largeobject" {
			loTM = tm
			break
		}
	}
	if loTM == nil {
		return nil, fmt.Errorf("未发现 pg_largeobject 系统目录")
	}
	// PG 标准物理文件名恒为 2613（pg_largeobject 的 relfilenode）。
	// 金仓 V8/V9 的 pg_class.relfilenode 存的是 OID（12031）而非 relfilenode，
	// resolveTableFilePath 会指向空文件 → 优先内容探测 2613，失败再回退 relfilenode 路径。
	path := detectSysFile(dbDir, 2613, map[string]bool{"pg_largeobject": true, "sys_largeobject": true}, pageSize)
	if path == "" {
		path = resolveTableFilePath(o, dbDir, loTM.RelFileNode, loTM.TableSpace)
	}
	if path == "" {
		return nil, fmt.Errorf("无法定位 pg_largeobject 数据文件")
	}
	ps := pageSize
	if ps == 0 {
		ps = probePageSize(path)
	}
	if ps == 0 {
		return nil, fmt.Errorf("无法探测 pg_largeobject 页大小")
	}
	ri := &RowIter{
		tm: loTM, pageSize: ps, pgVersion: version, isKB: isKB, path: path,
		verboseDebug: o.Verbose, Parallel: o.Parallel,
		decryptor: buildPaidDecryptor(o, loTM, dbDir),
	}
	if ri.decryptor != nil {
		if err := ri.decryptor.Init(); err != nil {
			ri.decryptor = nil
		}
	}
	// TOAST 关联（bytea 大块数据外联到 pg_toast_2613）
	if loTM.ToastRelFile != 0 {
		ts := loTM.ToastTableSpace
		if ts == 0 {
			ts = loTM.TableSpace
		}
		tp := resolveTableFilePath(o, dbDir, loTM.ToastRelFile, ts)
		if tp != "" {
			tf := &ToastFile{
				Path: tp, PageSize: ps, PageCache: map[int]map[int][]byte{}, CacheMax: 64,
				PgVersion: version, IsKB: isKB, decryptor: ri.decryptor,
			}
			if fi, err := os.Stat(tp); err == nil && fi.Size() > 0 {
				tf.PageSize = probePageSize(tp)
			}
			tf.BuildIndex(o.Verbose)
			ri.toast = tf
		}
	}
	// 扫描并按 loid 聚合
	type loRow struct {
		loid   uint32
		pageno uint32
		data   string
	}
	var rows []loRow
	scanLargeObjectRows(ri, func(loid, pageno uint32, data string) bool {
		if len(filter) > 0 && !filter[loid] {
			return true
		}
		rows = append(rows, loRow{loid, pageno, data})
		return true
	})
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].loid != rows[j].loid {
			return rows[i].loid < rows[j].loid
		}
		return rows[i].pageno < rows[j].pageno
	})
	var out []*largeObjectEntry
	var cur *largeObjectEntry
	for _, r := range rows {
		if cur == nil || cur.OID != r.loid {
			if cur != nil {
				out = append(out, cur)
			}
			cur = &largeObjectEntry{OID: r.loid}
		}
		cur.DataHex += r.data
	}
	if cur != nil {
		out = append(out, cur)
	}
	// owner 元数据（pg_largeobject_metadata 2614：元组 OID 即大对象 OID）
	owners := collectLargeObjectOwners(dbDir, version, isKB, ps)
	for _, e := range out {
		e.OwnerOID = owners[e.OID]
	}
	if o.Verbose {
		logf("大对象扫描完成: %d 个", len(out))
	}
	return out, nil
}

// collectLargeObjectOwners：pg_largeobject_metadata（2614）→ loid → lomowner
// 布局：行数据 = oid 系统列(4B) + lomowner(4B) + lomacl(varlena 可空)。
// oid 系统列不占 null 位图；用户列从 tHoff+4 开始。
func collectLargeObjectOwners(dbDir string, version int, isKB bool, pageSize int) map[uint32]uint32 {
	out := map[uint32]uint32{}
	ps := pageSize
	if ps == 0 {
		ps = probePageSize(filepath.Join(dbDir, strconv.Itoa(PG_TYPE_RELFILE)))
	}
	metaPath := detectSysFile(dbDir, 2614, map[string]bool{"pg_largeobject_metadata": true, "sys_largeobject_metadata": true}, ps)
	if metaPath == "" {
		return out
	}
	userCols := []ColLen{{4, false, "i"}, {0, true, "i"}} // lomowner, lomacl
	for tup := range iterSysTuples(metaPath, version, isKB) {
		if !tup.isLive() {
			continue
		}
		if tup.THoff+4 > len(tup.Raw) {
			continue
		}
		loid := u32(tup.Raw, tup.THoff) // oid 系统列
		nulls := tup.getNulls()
		fields := extractFieldsDirect(tup.Raw, tup.THoff+4, nulls, userCols)
		if len(fields) >= 1 && fields[0] != nil && len(*fields[0]) >= 4 {
			out[loid] = u32(*fields[0], 0)
		}
	}
	return out
}

// writeLargeObjectsSQL：写出大对象 SQL（lo_from_bytea + owner 授权）
// ownerNames 为 oid→角色名（可空）；返回写入的大对象数
func writeLargeObjectsSQL(w io.Writer, entries []*largeObjectEntry, ownerNames map[uint32]string) (int, error) {
	if len(entries) == 0 {
		fmt.Fprintf(w, "-- pg2sql 大对象导出：无大对象\n")
		return 0, nil
	}
	fmt.Fprintf(w, "-- pg2sql 大对象导出（%d 个，v1.0.40）\n", len(entries))
	fmt.Fprintf(w, "-- 导入: psql -f large_objects.sql <数据库>\n\n")
	n := 0
	for _, e := range entries {
		fmt.Fprintf(w, "SELECT pg_catalog.lo_from_bytea(%d, '\\x%s');\n", e.OID, e.DataHex)
		if e.OwnerOID != 0 {
			if nm, ok := ownerNames[e.OwnerOID]; ok && nm != "" {
				fmt.Fprintf(w, "ALTER LARGE OBJECT %d OWNER TO %s;\n", e.OID, quoteRoleName(nm))
			} else {
				fmt.Fprintf(w, "-- 大对象 %d owner oid=%d（角色名不可解析，跳过 ALTER）\n", e.OID, e.OwnerOID)
			}
		}
		n++
	}
	return n, nil
}

func quoteRoleName(name string) string {
	if name == "postgres" || name == "sysdba" || name == "public" {
		return name
	}
	return "\"" + strings.ReplaceAll(name, "\"", "\"\"") + "\""
}

// parseLargeObjectOIDs：解析 --large-object-oids 逗号列表（空=全部）
func parseLargeObjectOIDs(s string) map[uint32]bool {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	out := map[uint32]bool{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if v, err := strconv.ParseUint(p, 10, 32); err == nil {
			out[uint32(v)] = true
		}
	}
	return out
}

// hexBytes 校验用：确认拼接的 hex 可解码（防坏块污染）
func hexValid(s string) bool {
	if len(s)%2 != 0 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// exportLargeObjects：main 集成入口——校验、收集、写盘（-o 目录/文件；无 -o 走 stdout）
// 返回大对象数；同时写 report 条目与导入脚本（import_lob.sh）
func exportLargeObjects(o *Options) (int, error) {
	dbDir := resolveDbDir(o)
	if dbDir == "" {
		return 0, fmt.Errorf("--large-objects 需要数据库目录（位置参数目录或 --datadir+--db-oid）")
	}
	version := detectPgVersion(dbDir)
	if version == 0 {
		version = 12
	}
	isKB := isKingbaseDatadir(dbDir)
	filter := parseLargeObjectOIDs(o.LargeObjectOIDs)
	entries, err := collectLargeObjects(o, dbDir, version, isKB, o.PageSize, filter)
	if err != nil {
		return 0, err
	}
	roleNames := buildRoleNameMap(dbDir)
	startMs := nowMs()
	var w io.Writer = os.Stdout
	outPath := ""
	if o.Output != "" {
		outPath = o.Output
		if fi, err := os.Stat(o.Output); err == nil && fi.IsDir() || strings.HasSuffix(o.Output, "/") {
			outPath = filepath.Join(o.Output, "large_objects.sql")
		} else if filepath.Ext(o.Output) == "" {
			outPath = o.Output + ".sql"
		}
		if dir := filepath.Dir(outPath); dir != "" && dir != "." {
			os.MkdirAll(dir, 0755)
		}
		f, err := os.Create(outPath)
		if err != nil {
			return 0, fmt.Errorf("无法创建输出文件: " + err.Error())
		}
		defer f.Close()
		w = f
		if o.Verbose {
			logf("输出到: %s", outPath)
		}
	}
	n, err := writeLargeObjectsSQL(w, entries, roleNames)
	if err != nil {
		return 0, err
	}
	if f, ok := w.(*os.File); ok {
		if ferr := f.Sync(); ferr != nil {
			return 0, fmt.Errorf("写入输出文件失败: %v", ferr)
		}
	}
	if o.Verbose {
		logf("完成，共输出 %d 个大对象", n)
	}
	// report 条目 + 导入脚本
	reportSink.add(ExportReportEntry{
		Table: "pg_catalog.large_objects", Format: "sql", Rows: int64(n), BadBlocks: 0,
		File: outPath, Bytes: statSize(outPath), Checksum: "",
		ElapsedMs: nowMs() - startMs, PageSize: o.PageSize, Encoding: currentTextEncoding(),
	})
	if o.ImportScript != "none" && outPath != "" {
		makeImportScript(o, &TableMeta{Schema: "pg_catalog", RelName: "large_objects"}, outPath, "sql", nil)
	}
	return n, nil
}
