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
	"pg2sql/internal/integrity"
	"pg2sql/internal/logx"
	"pg2sql/internal/paid"
	"sort"
	"strconv"
	"strings"
)

// loPageRef：一页 hex 正文在共享溢写文件中的位置。
// v1.0.63 修复（B-2）：聚合阶段只保留这个 16 字节的索引，正文一律落临时文件。
type loPageRef struct {
	pageno uint32
	off    int64
	hexLen int32
}

// loSpill：所有大对象共用同一个溢写文件（避免上千个临时文件）。
// 单线程写入，偏移由写入方记录，读回时用 ReadAt 精确取页。
type loSpill struct {
	f    *os.File
	path string
}

func newLoSpill() (*loSpill, error) {
	f, err := os.CreateTemp("", "pg2sql-lo-*.hex")
	if err != nil {
		return nil, err
	}
	return &loSpill{f: f, path: f.Name()}, nil
}

func (s *loSpill) appendHex(hex string) (int64, error) {
	off, err := s.f.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}
	if _, err := s.f.WriteString(hex); err != nil {
		return 0, err
	}
	return off, nil
}

func (s *loSpill) close() {
	if s == nil {
		return
	}
	if s.f != nil {
		s.f.Close()
		s.f = nil
	}
	if s.path != "" {
		os.Remove(s.path)
		s.path = ""
	}
}

// LargeObjectEntry：单个大对象。
// v1.0.62 引入溢写；v1.0.63（B-2）改为【页索引 + 共享溢写文件】：
// 聚合期不再把所有页的 hex 攒在 rows 切片里（4GB 对象要 30GB 峰值），
// 边扫边写临时文件，内存只剩 16B/页。DataHex 仅在临时文件创建失败时启用。
type LargeObjectEntry struct {
	OID      uint32
	OwnerOID uint32 // 0=未知
	DataHex  string // 降级路径：无法创建临时文件时的内存正文
	ByteLen  int64  // 大对象真实字节数
	pages    []loPageRef
	spill    *loSpill
}

// LoSpillThreshold：保留常量供调用方/文档引用（v1.0.63 起正文一律溢写）
const LoSpillThreshold = 64 << 20

// CleanupLargeObjectSpill：关闭并删除共享溢写文件（调用方 defer 即可，失败无副作用）
func CleanupLargeObjectSpill(entries []*LargeObjectEntry) {
	seen := map[*loSpill]bool{}
	for _, e := range entries {
		if e == nil || e.spill == nil || seen[e.spill] {
			continue
		}
		seen[e.spill] = true
		e.spill.close()
	}
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
	var metaTM *engine.TableMeta
	for _, tm := range tables {
		if tm.RelName == "pg_largeobject" || tm.RelName == "sys_largeobject" {
			loTM = tm
		}
		// v1.0.63（B-1）：owner 元数据表按 pg_class 实际 relfilenode 兜底定位
		if tm.RelName == "pg_largeobject_metadata" || tm.RelName == "sys_largeobject_metadata" {
			metaTM = tm
		}
	}
	metaPathHint := ""
	if metaTM != nil {
		metaPathHint = config.ResolveTableFilePath(o, dbDir, metaTM.RelFileNode, metaTM.TableSpace)
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
			// v1.0.60 修复（P1-7）：大对象路径此前只 Init() 不校验密钥，
			// 且 Init 失败静默降级为明文（密文当明文导出）。
			// 注入了加密参数却拿不到解密器 = 必然乱码，直接硬错误。
			return nil, fmt.Errorf("表解密初始化失败: %v（已指定加密参数但无法构建解密器，拒绝输出乱码大对象）", err)
		}
		// v1.0.60 修复（P1-7）：与表路径（v1.0.46）对齐——错密钥 SM4-ECB 仍能"成功"
		// 解出随机字节，不校验就会静默导出乱码 LO 且 exit 0。
		if verr := paid.VerifyDecryptorKey(ri.Decryptor, path, ps, len(loTM.Columns)); verr != nil {
			return nil, verr
		}
	} else if o.WalletPassword != "" || o.WalletDir != "" {
		// v1.0.60 修复（P1-7）：注入了表空间加密参数却没构建出解密器（钱包/密钥缺失）
		// → 继续导出等于把密文当明文。取证/交付场景不能静默产出乱码数据。
		return nil, fmt.Errorf("已指定 --wallet-password/--wallet-dir，但未能构建表空间解密器（钱包路径或表空间密钥缺失），拒绝把密文当明文导出大对象")
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
	//
	// v1.0.63 修复（B-2）：旧实现先把所有页的 hex 全量 append 进 rows 切片，
	// 聚合/溢写发生在其后 —— 4GB 对象峰值仍是 rows(8GB) + builder(8GB) ≈ 16GB，
	// v1.0.62 报告里"内存上限降到 32MB 级"不成立。现在改为边扫边写：
	//   * 打开一个【共享】溢写文件，每页 hex 追加进去，只留 16B 的页索引；
	//   * 按 loid 维护累加器（物理页序与 loid 交错，不能只保留一个当前对象）；
	//   * 全部扫描结束后，每个对象按 pageno 排序 + 连续性校验，再决定输出/拒绝。
	// 内存占用从 O(对象总字节) 降到 O(页数)，4GB 对象约 32MB 索引。
	type loAccum struct {
		oid   uint32
		pages []loPageRef
		bytes int64
		ok    bool
		// 降级：临时文件创建失败时退回内存聚合（不能因为 /tmp 不可写就导不出）
		mem strings.Builder
	}
	var spill *loSpill
	if s, err := newLoSpill(); err == nil {
		spill = s
	} else {
		logx.Logf("警告: 无法创建大对象溢写临时文件: %v（退回内存聚合，超大对象可能耗尽内存）", err)
	}
	if spill != nil {
		// 临时文件的删除交给调用方的 CleanupLargeObjectSpill：此处不能 defer close，
		// 否则写出阶段（entry.pages 仍指向该文件）会读不到内容。
		// 本函数创建 spill 之后没有提前 return 的路径，不会遗留临时文件。
	}
	accs := map[uint32]*loAccum{}
	loSkipped := 0
	scanLargeObjectRows(ri, func(loid, pageno uint32, data string) bool {
		if len(filter) > 0 && !filter[loid] {
			return true
		}
		a := accs[loid]
		if a == nil {
			a = &loAccum{oid: loid, ok: true}
			accs[loid] = a
		}
		if !a.ok || data == "" {
			return true
		}
		if spill != nil {
			off, err := spill.appendHex(data)
			if err != nil {
				logx.Logf("警告: 大对象 %d 溢写临时文件失败: %v（该对象拒绝输出）", loid, err)
				a.ok = false
				return true
			}
			a.pages = append(a.pages, loPageRef{pageno: pageno, off: off, hexLen: int32(len(data))})
		} else {
			a.mem.WriteString(data)
			a.pages = append(a.pages, loPageRef{pageno: pageno, off: -1, hexLen: int32(len(data))})
		}
		a.bytes += int64(len(data) / 2)
		return true
	})
	// 对象顺序保持 loid 升序（与旧实现的全局排序结果一致）
	oids := make([]uint32, 0, len(accs))
	for k := range accs {
		oids = append(oids, k)
	}
	sort.Slice(oids, func(i, j int) bool { return oids[i] < oids[j] })
	var out []*LargeObjectEntry
	for _, loid := range oids {
		a := accs[loid]
		// v1.0.59 修复（P0-4）：LO 是无自描述边界的 2KB 块流，扫描期任何失败路径
		//（TOAST 缺块/无 TOAST 关联/解密失败/non-hex）都会静默丢页，丢一页 = 后续
		// 内容整体前移。按 pageno 排序后强制校验从 0 连续、无重复，
		// 不完整对象整体拒绝输出并计入 skip 清单，绝不产出"错位但成功"的 SQL。
		sort.Slice(a.pages, func(i, j int) bool { return a.pages[i].pageno < a.pages[j].pageno })
		if a.ok && len(a.pages) == 0 {
			a.ok = false
		}
		if a.ok {
			var expect uint32
			for _, p := range a.pages {
				if p.pageno != expect {
					kind := "缺页"
					if p.pageno < expect {
						kind = "重复页"
					}
					integrity.AddSkip(integrity.SkipEntry{
						Table:  fmt.Sprintf("large_object.%d", loid),
						Reason: "lo_page_gap",
						Detail: fmt.Sprintf("%s: 期望 pageno=%d 实得 %d，已拒绝输出该大对象（输出错位比缺失更危险）", kind, expect, p.pageno),
					})
					logx.Logf("警告: 大对象 %d %s（期望 pageno=%d 实得 %d），拒绝输出该大对象", loid, kind, expect, p.pageno)
					a.ok = false
					break
				}
				expect++
			}
		}
		if !a.ok {
			loSkipped++
			continue
		}
		e := &LargeObjectEntry{OID: loid, ByteLen: a.bytes, spill: spill, pages: a.pages}
		if spill == nil {
			e.DataHex = a.mem.String()
			e.pages = nil
		}
		out = append(out, e)
	}
	accs = nil // 及时释放
	if loSkipped > 0 {
		logx.Logf("大对象导出: %d 个因页序列不完整被拒绝（详见 report skipped 清单）", loSkipped)
	}
	// owner 元数据（pg_largeobject_metadata relfilenode 2995：元组 OID 即大对象 OID）
	owners := CollectLargeObjectOwners(dbDir, version, isKB, ps, metaPathHint)
	for _, e := range out {
		e.OwnerOID = owners[e.OID]
	}
	if o.Verbose {
		logx.Logf("大对象扫描完成: %d 个", len(out))
	}
	return out, nil
}

// CollectLargeObjectOwners：pg_largeobject_metadata → loid → lomowner
// 布局：行数据 = oid 系统列(4B) + lomowner(4B) + lomacl(varlena 可空)。
// oid 系统列不占 null 位图；用户列从 tHoff+4 开始。
//
// v1.0.63 修复（B-1）：关系 OID 从 2614 改为 **2995**。2614 是早已废弃的
// pg_listener（PG 9.0 起就没有数据文件），标准目录里 pg_largeobject_metadata
// 的 relfilenode 是 2995 —— 旧实现按 2614 找不到文件就退化成"首列 64B Name"
// 内容探测，而该表首列是 4 字节的 lomowner，永不命中 → owner map 恒空，
// ALTER LARGE OBJECT ... OWNER TO 从不输出，连"不可解析"的注释也不打。
func CollectLargeObjectOwners(dbDir string, version int, isKB bool, pageSize int, pathHint string) map[uint32]uint32 {
	out := map[uint32]uint32{}
	ps := pageSize
	if ps == 0 {
		ps = engine.ProbePageSize(filepath.Join(dbDir, strconv.Itoa(catalog.PG_TYPE_RELFILE)))
	}
	metaPath := engine.DetectSysFile(dbDir, 2995, map[string]bool{"pg_largeobject_metadata": true, "sys_largeobject_metadata": true}, ps)
	if metaPath == "" {
		// 非标准 relfilenode（个别 fork/迁移后的库）→ 用 pg_class 里读到的实际路径
		metaPath = pathHint
	}
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
		// v1.0.63：ByteLen 未填（内存态条目/外部构造）时按 DataHex 长度推导，
		// 否则 0 会被判成"小对象"走 lo_from_bytea，把 32MB 的 hex 塞进单条语句。
		if e.ByteLen == 0 && len(e.DataHex) > 0 {
			e.ByteLen = int64(len(e.DataHex) / 2)
		}
		if e.ByteLen <= LoChunkBytes {
			// 小对象：单语句 lo_from_bytea（原子写入，输出格式与旧版一致）
			hexs, err := e.readHex()
			if err != nil {
				return n, err
			}
			fmt.Fprintf(w, "SELECT pg_catalog.lo_from_bytea(%d, '\\x%s');\n", e.OID, hexs)
		} else {
			logx.Logf("提示: 大对象 %d 体积超过单语句上限（%.1f MB），已分块为 lo_put 导入",
				e.OID, float64(e.ByteLen)/1024/1024)
			fmt.Fprintf(w, "SELECT pg_catalog.lo_create(%d);\n", e.OID)
			if e.ByteLen > 2<<30 {
				// v1.0.62（P2-7）：PG 9.3+ 的 lo_put(loid, offset int8, data) 支持 >2GB；
				// 更早版本/vendor 分支的 offset 为 int4，超过 2GB 会报 offset 越界。
				logx.Logf("提示: 大对象 %d 超过 2GB（%.2f GB）—— 目标端需 PG 9.3+ 的 lo_put(offset int8)；如导入报 offset 越界，请改用 psql 的 \\lo_import 导入原始文件",
					e.OID, float64(e.ByteLen)/1024/1024/1024)
			}
			if err := writeLoPutChunks(w, e); err != nil {
				return n, err
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

// readHex：按 pageno 顺序读回整个对象的 hex 正文（内存对象直接返回）。
// 仅用于 <= LoChunkBytes 的小对象，内存上界 = 2×LoChunkBytes。
func (e *LargeObjectEntry) readHex() (string, error) {
	if e.spill == nil || len(e.pages) == 0 {
		return e.DataHex, nil
	}
	var sb strings.Builder
	sb.Grow(int(e.ByteLen) * 2)
	for _, p := range e.pages {
		buf := make([]byte, p.hexLen)
		if _, err := e.spill.f.ReadAt(buf, p.off); err != nil {
			return "", fmt.Errorf("读取大对象 %d 溢写页失败: %v", e.OID, err)
		}
		sb.Write(buf)
	}
	return sb.String(), nil
}

// writeLoPutChunks：按 LoChunkBytes 分段输出 lo_put 语句（内存对象与溢写对象统一路径）
func writeLoPutChunks(w io.Writer, e *LargeObjectEntry) error {
	if e.spill == nil || len(e.pages) == 0 {
		for start := 0; start < len(e.DataHex); start += LoChunkBytes * 2 {
			end := start + LoChunkBytes*2
			if end > len(e.DataHex) {
				end = len(e.DataHex)
			}
			if _, err := fmt.Fprintf(w, "SELECT pg_catalog.lo_put(%d, %d, decode('%s','hex'));\n",
				e.OID, start/2, e.DataHex[start:end]); err != nil {
				return fmt.Errorf("写入大对象 %d SQL 失败: %v", e.OID, err)
			}
		}
		return nil
	}
	// v1.0.63（B-2）：逐页从共享溢写文件读回，攒满 LoChunkBytes 再写一条 lo_put。
	// offset 累加的是【数据字节】（hexLen/2），不是 hex 字符数。
	buf := make([]byte, 0, LoChunkBytes*2)
	var off int64
	flush := func() error {
		if len(buf) == 0 {
			return nil
		}
		if _, err := fmt.Fprintf(w, "SELECT pg_catalog.lo_put(%d, %d, decode('%s','hex'));\n",
			e.OID, off, string(buf)); err != nil {
			return fmt.Errorf("写入大对象 %d SQL 失败: %v", e.OID, err)
		}
		off += int64(len(buf) / 2)
		buf = buf[:0]
		return nil
	}
	for _, p := range e.pages {
		if p.hexLen <= 0 {
			continue
		}
		if len(buf)+int(p.hexLen) > cap(buf) {
			if err := flush(); err != nil {
				return err
			}
		}
		page := make([]byte, p.hexLen)
		if _, err := e.spill.f.ReadAt(page, p.off); err != nil {
			return fmt.Errorf("读取大对象 %d 溢写页失败: %v", e.OID, err)
		}
		buf = append(buf, page...)
	}
	return flush()
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
