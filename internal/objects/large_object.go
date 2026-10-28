// pg2sql 大对象导出（--large-objects / --large-object-oids，v1.0.40）
// 从 pg_largeobject(2613) 读取大对象分页数据（含 TOAST 外联解析），
// 从 pg_largeobject_metadata(2614) 读取 owner 元数据，输出可导入 SQL。
// 导入语句采用 pg_catalog.lo_from_bytea(loid, '\x...')：单语句原子写入整个大对象
// （PG 9.4+ 与金仓 V8/V9 均支持；lowrite 多块方案需跨语句 fd，psql 单语句自动提交下不可靠）。
package objects

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"pg2sql/internal/catalog"
	"pg2sql/internal/config"
	"pg2sql/internal/datatype"
	"pg2sql/internal/engine"
	"pg2sql/internal/logx"
	"pg2sql/internal/paid"
	"sort"
	"strconv"
	"strings"
)

// LargeObjectEntry：单个大对象（DataHex 为全部 pageno 按序拼接的 hex 字符串，不含 \x 前缀）
type LargeObjectEntry struct {
	OID      uint32
	OwnerOID uint32 // 0=未知
	DataHex  string
}

// largeObjectRows：从 pg_largeobject 扫描全部 (loid, pageno, dataHex) 行
func scanLargeObjectRows(ri *engine.RowIter, emit func(loid uint32, pageno uint32, dataHex string) bool) {
	npages := ri.PageCount()
	if npages == 0 {
		return
	}
	buf := make([]byte, ri.PageSize)
	base := 0
	for _, seg := range engine.SegFiles(ri.Path) {
		f, err := os.Open(seg)
		if err != nil {
			continue
		}
		pageno := base
		for {
			n, err := f.Read(buf)
			if err != nil || n < ri.PageSize {
				break
			}
			if ri.Decryptor != nil {
				if _, derr := ri.Decryptor.DecryptPage(buf[:n]); derr != nil {
					break
				}
			}
			items, _ := ri.IterTuples(pageno, buf)
			for _, it := range items {
				tup := it[1].(*engine.HeapTuple)
				if !tup.IsLive() {
					continue
				}
				// 固定 3 列直解（loid/pageno/data）：金仓 V8/V9 的 pg_attribute
				// 把系统列（tableoid/cmax/xmax/cmin/xmin/ctid）列为正 attnum 1-6，
				// 用 tm.Columns 会错位；行数据按是否有 oid 系统列定起点。
				hasOIDSys := false
				for _, c := range ri.TM.Columns {
					if c.Name == "oid" {
						hasOIDSys = true
						break
					}
				}
				base := tup.THoff
				if hasOIDSys {
					base += 4
				}
				nulls := tup.GetNulls()
				fields := datatype.ExtractFieldsDirect(tup.Raw, base, nulls, []datatype.ColLen{
					{AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 0, IsVarlena: true, AttAlign: "i"},
				})
				if len(fields) < 3 || fields[0] == nil || fields[1] == nil || fields[2] == nil {
					continue
				}
				loid := datatype.U32(*fields[0], 0)
				pno := datatype.U32(*fields[1], 0)
				rawB := *fields[2]
				// bytea 外联（TOAST）重组
				if len(rawB) > 0 {
					_, isExt, ext := engine.CheckExternal(rawB)
					if isExt && ri.Toast != nil {
						reassembled := ri.Toast.FetchAndReassemble(ext.ValueID, 0)
						if reassembled == nil {
							continue
						}
						expected := int(ext.RawSize) - 4
						if ext.Compressed {
							data := datatype.ToastDecompress(reassembled, expected, ext.Method)
							if data == nil {
								continue
							}
							rawB = data
						} else if len(reassembled) != expected {
							continue
						} else {
							rawB = reassembled
						}
						rawB = datatype.RebuildVarlena(rawB)
					} else if isExt {
						continue
					}
				}
				decoded := datatype.DecodeValue(17, rawB)
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
			base += int(fi.Size() / int64(ri.PageSize))
		}
	}
}

// CollectLargeObjects：收集大对象（按 loid 聚合、pageno 排序；filter 为空=全部）
func CollectLargeObjects(o *config.Options, dbDir string, version int, isKB bool, pageSize int, filter map[uint32]bool) ([]*LargeObjectEntry, error) {
	_, tables := catalog.AutoDiscoverAllTables(dbDir, pageSize)
	var loTM *engine.TableMeta
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
	// config.ResolveTableFilePath 会指向空文件 → 优先内容探测 2613，失败再回退 relfilenode 路径。
	path := engine.DetectSysFile(dbDir, 2613, map[string]bool{"pg_largeobject": true, "sys_largeobject": true}, pageSize)
	if path == "" {
		path = config.ResolveTableFilePath(o, dbDir, loTM.RelFileNode, loTM.TableSpace)
	}
	if path == "" {
		return nil, fmt.Errorf("无法定位 pg_largeobject 数据文件")
	}
	ps := pageSize
	if ps == 0 {
		ps = engine.ProbePageSize(path)
	}
	if ps == 0 {
		return nil, fmt.Errorf("无法探测 pg_largeobject 页大小")
	}
	ri := &engine.RowIter{
		TM: loTM, PageSize: ps, PGVersion: version, IsKB: isKB, Path: path,
		VerboseDebug: o.Verbose, Parallel: o.Parallel,
		Decryptor: paid.BuildPaidDecryptor(o, loTM, dbDir),
	}
	if ri.Decryptor != nil {
		if err := ri.Decryptor.Init(); err != nil {
			ri.Decryptor = nil
		}
	}
	// TOAST 关联（bytea 大块数据外联到 pg_toast_2613）
	if loTM.ToastRelFile != 0 {
		ts := loTM.ToastTableSpace
		if ts == 0 {
			ts = loTM.TableSpace
		}
		tp := config.ResolveTableFilePath(o, dbDir, loTM.ToastRelFile, ts)
		if tp != "" {
			tf := &engine.ToastFile{
				Path: tp, PageSize: ps, PageCache: map[int]map[int][]byte{}, CacheMax: 64,
				PgVersion: version, IsKB: isKB, Decryptor: ri.Decryptor,
			}
			if fi, err := os.Stat(tp); err == nil && fi.Size() > 0 {
				tf.PageSize = engine.ProbePageSize(tp)
			}
			tf.BuildIndex(o.Verbose)
			ri.Toast = tf
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
	var out []*LargeObjectEntry
	var cur *LargeObjectEntry
	// v1.0.56 修复（P3-G15）：`DataHex += r.data` 对 N 页大对象是 O(n²) 重分配
	//（10 万页对象累计拷贝数十 GB），改 strings.Builder 均摊 O(n)，聚合完成
	// 时一次性 String()。
	var curBuf strings.Builder
	for _, r := range rows {
		if cur == nil || cur.OID != r.loid {
			if cur != nil {
				cur.DataHex = curBuf.String()
				out = append(out, cur)
			}
			cur = &LargeObjectEntry{OID: r.loid}
			curBuf.Reset()
		}
		curBuf.WriteString(r.data)
	}
	if cur != nil {
		cur.DataHex = curBuf.String()
		out = append(out, cur)
	}
	// owner 元数据（pg_largeobject_metadata 2614：元组 OID 即大对象 OID）
	owners := CollectLargeObjectOwners(dbDir, version, isKB, ps)
	for _, e := range out {
		e.OwnerOID = owners[e.OID]
	}
	if o.Verbose {
		logx.Logf("大对象扫描完成: %d 个", len(out))
	}
	return out, nil
}

// CollectLargeObjectOwners：pg_largeobject_metadata（2614）→ loid → lomowner
// 布局：行数据 = oid 系统列(4B) + lomowner(4B) + lomacl(varlena 可空)。
// oid 系统列不占 null 位图；用户列从 tHoff+4 开始。
func CollectLargeObjectOwners(dbDir string, version int, isKB bool, pageSize int) map[uint32]uint32 {
	out := map[uint32]uint32{}
	ps := pageSize
	if ps == 0 {
		ps = engine.ProbePageSize(filepath.Join(dbDir, strconv.Itoa(catalog.PG_TYPE_RELFILE)))
	}
	metaPath := engine.DetectSysFile(dbDir, 2614, map[string]bool{"pg_largeobject_metadata": true, "sys_largeobject_metadata": true}, ps)
	if metaPath == "" {
		return out
	}
	userCols := []datatype.ColLen{{AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 0, IsVarlena: true, AttAlign: "i"}} // lomowner, lomacl
	for tup := range engine.IterSysTuples(metaPath, version, isKB) {
		if !tup.IsLive() {
			continue
		}
		if tup.THoff+4 > len(tup.Raw) {
			continue
		}
		loid := datatype.U32(tup.Raw, tup.THoff) // oid 系统列
		nulls := tup.GetNulls()
		fields := datatype.ExtractFieldsDirect(tup.Raw, tup.THoff+4, nulls, userCols)
		if len(fields) >= 1 && fields[0] != nil && len(*fields[0]) >= 4 {
			out[loid] = datatype.U32(*fields[0], 0)
		}
	}
	return out
}

// WriteLargeObjectsSQL：写出大对象 SQL（lo_from_bytea + owner 授权）
// ownerNames 为 oid→角色名（可空）；返回写入的大对象数
// LoChunkBytes：单条 SQL 语句携带的大对象二进制上限（16MB）。
// v1.0.56 修复（P3-G15）：旧版单条 lo_from_bytea 内嵌全对象 hex，PG text/bytea
// 入参上限 1GB —— 超过 ~512MB 的大对象生成必然导入失败的语句且无告警。
// 大对象改 lo_create + 分块 lo_put(loid, offset, decode(hex))，每块 16MB
// （hex 32MB 字面量，远低于上限），任意大小均可导入。
const LoChunkBytes = 16 << 20

func WriteLargeObjectsSQL(w io.Writer, entries []*LargeObjectEntry, ownerNames map[uint32]string) (int, error) {
	if len(entries) == 0 {
		fmt.Fprintf(w, "-- pg2sql 大对象导出：无大对象\n")
		return 0, nil
	}
	fmt.Fprintf(w, "-- pg2sql 大对象导出（%d 个，v1.0.40）\n", len(entries))
	fmt.Fprintf(w, "-- 导入: psql -f large_objects.sql <数据库>\n\n")
	n := 0
	for _, e := range entries {
		if len(e.DataHex) <= LoChunkBytes*2 {
			fmt.Fprintf(w, "SELECT pg_catalog.lo_from_bytea(%d, '\\x%s');\n", e.OID, e.DataHex)
		} else {
			logx.Logf("提示: 大对象 %d 体积超过单语句上限（%.1f MB），已分块为 lo_put 导入",
				e.OID, float64(len(e.DataHex))/2/1024/1024)
			fmt.Fprintf(w, "SELECT pg_catalog.lo_create(%d);\n", e.OID)
			for start := 0; start < len(e.DataHex); start += LoChunkBytes * 2 {
				end := start + LoChunkBytes*2
				if end > len(e.DataHex) {
					end = len(e.DataHex)
				}
				fmt.Fprintf(w, "SELECT pg_catalog.lo_put(%d, %d, decode('%s','hex'));\n",
					e.OID, start/2, e.DataHex[start:end])
			}
		}
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

// ParseLargeObjectOIDs：解析 --large-object-oids 逗号列表（空=全部）
func ParseLargeObjectOIDs(s string) map[uint32]bool {
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
