// pg2sql heapfile 层：堆文件页遍历、元组提取、字段提取、TOAST 重组、行/SQL/CSV 生成
// 对应 Python 版 pg2sql/{page.py, tuple.py, heapfile.py, toast.py}，语义等价翻译
// Author: raysuen
package engine

import (
	"io"
	"os"
	"path/filepath"
	"pg2sql/internal/datatype"
	"pg2sql/internal/logx"
	"pg2sql/internal/util"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// PageDecryptor：TDE 页解密器接口（v1.0.39）。
// 免费版编译（无 -tags tde）时不包含 tde 包，用接口解耦；TDE 版由 *tde.Decryptor 实现。
type PageDecryptor interface {
	Init() error
	DecryptPage(b []byte) ([]byte, error)
}

// ---------- 常量 ----------
const (
	PG_PAGE_VERSION  = 4
	KB_PAGE_VERSION  = 5
	PAGE_SIZE        = 8192
	PAGE_HEADER_SIZE = 24
	ITEMID_NORMAL    = 1
	KB_EXTERNAL_SIZE = datatype.KB_EXTERNAL_SIZE

	HEAP_TUPLE_HEADER_SIZE = 23
	HEAP_HASNULL           = datatype.HEAP_HASNULL
	HEAP_HASVARWIDTH       = 0x0002
	HEAP_HASEXTERNAL       = 0x0004
	HEAP_HASOID            = 0x0008
	HEAP_XMAX_LOCK_ONLY    = 0x0080
	HEAP_XMIN_COMMITTED    = 0x0100
	HEAP_XMIN_INVALID      = 0x0200
	HEAP_XMIN_FROZEN       = 0x0300
	HEAP_XMAX_COMMITTED    = 0x0400
	HEAP_XMAX_INVALID      = 0x0800
	HEAP_XMAX_IS_MULTI     = 0x1000
	HEAP_KEYS_UPDATED      = 0x2000
	HEAP_HOT_UPDATED       = 0x4000
	HEAP_NATTS_MASK        = datatype.HEAP_NATTS_MASK
)

var knownPageSizes = []uint16{8192, 16384, 32768}

// readFullPage：整页读取（v1.0.63 C-4）。
// 旧实现在串行路径用 `n, err := f.Read(buf); if err != nil || n < PageSize { break }`
// —— 把短读当成 EOF 静默结束扫描（NFS/CIFS 取证挂载、被截断的文件都会触发），
// 而并行路径用 ReadAt（短读必带 err）→ 同一文件 --parallel 1 与 4 行数可能不同。
// io.ReadFull 只在真正的 EOF 返回 io.EOF，短读返回 ErrUnexpectedEOF 并告警。
func readFullPage(f *os.File, buf []byte) (int, error) {
	n, err := io.ReadFull(f, buf)
	if err == io.ErrUnexpectedEOF {
		logx.Logf("警告: 短读（期望 %d 字节，实读 %d）—— 文件被截断或位于短读文件系统，该页及后续页未导出", len(buf), n)
	}
	return n, err
}

// ---------- 页大小探测 ----------
func detectPageSize(raw []byte) int {
	if len(raw) < 130 {
		return 0
	}
	for off := 8; off < 128; off += 2 {
		psv := datatype.U16(raw, off)
		version := psv & 0xFF
		size := psv & 0xFF00
		if version != PG_PAGE_VERSION && version != KB_PAGE_VERSION {
			continue
		}
		ok := false
		for _, s := range knownPageSizes {
			if size == s {
				ok = true
				break
			}
		}
		if !ok || off < 6 {
			continue
		}
		lower := datatype.U16(raw, off-6)
		upper := datatype.U16(raw, off-4)
		special := datatype.U16(raw, off-2)
		headerEnd := (off + 2 + 3) &^ 3
		if headerEnd <= int(lower) && int(lower) <= int(upper) && int(upper) <= int(special) && int(special) <= int(size) {
			return int(size)
		}
	}
	return 0
}

// ---------- 页布局 ----------
type PageLayout struct {
	PsvOffset int
	HeaderEnd int
	PageSize  int
	Version   uint16
	Lower     uint16
	Upper     uint16
	Special   uint16
}

func TryStandardLayout(raw []byte, pageSize int) *PageLayout {
	if len(raw) < 24 {
		return nil
	}
	psv := datatype.U16(raw, 18)
	if psv&0xFF != PG_PAGE_VERSION || int(psv&0xFF00) != pageSize {
		return nil
	}
	lower, upper, special := datatype.U16(raw, 12), datatype.U16(raw, 14), datatype.U16(raw, 16)
	if !(24 <= int(lower) && int(lower) <= int(upper) && int(upper) <= int(special) && int(special) <= pageSize) {
		return nil
	}
	return &PageLayout{18, 24, pageSize, psv & 0xFF, lower, upper, special}
}

func TryAutoLayout(raw []byte, pageSize int) *PageLayout {
	// v1.0.56 修复（P3-G7）：旧版只检查 len>=8，但扫描循环读 datatype.U16(raw,off) 至
	// off<64——8~65 字节的截断/损坏文件会越界 panic（IterSysTuples/MaxRowNatts
	// 对小文件整文件传入本函数，无前置长度兜底）。防御下限提到 66。
	if len(raw) < 66 {
		return nil
	}
	for off := 8; off < 64; off += 2 {
		psv := datatype.U16(raw, off)
		version := psv & 0xFF
		if version != PG_PAGE_VERSION && version != KB_PAGE_VERSION {
			continue
		}
		if int(psv&0xFF00) != pageSize {
			continue
		}
		lower, upper, special := datatype.U16(raw, off-6), datatype.U16(raw, off-4), datatype.U16(raw, off-2)
		// v1.0.60 修复（P1-9）：页头 24B 含 pd_prune_xid(20–23)，旧算法
		// (off+2+3)&^3 只算到 20 → ItemId 数组从 prune_xid 起算，伪 ItemId[0]
		// 使全部 CTID 页内偏移 +1，且 prune_xid 值恰好像 ItemId 时会多出一条垃圾行。
		// 金仓页版本=5、TryStandardLayout 只认 4 → 金仓所有堆页都走本函数，必中。
		headerEnd := (off + 6 + 3) &^ 3
		if headerEnd <= int(lower) && int(lower) <= int(upper) && int(upper) <= int(special) && int(special) <= pageSize {
			return &PageLayout{off, headerEnd, pageSize, version, lower, upper, special}
		}
	}
	return nil
}

// ---------- 元组 ----------
type HeapTuple struct {
	Raw       []byte
	TXmin     uint32
	TXmax     uint32
	TInfomask uint16
	THoff     int
	NAttrs    int
	PgVersion int
}

func ParseTuple(raw []byte, pgVersion int) *HeapTuple {
	if len(raw) < HEAP_TUPLE_HEADER_SIZE {
		return nil
	}
	infomask2 := datatype.U16(raw, 18)
	infomask := datatype.U16(raw, 20)
	t := &HeapTuple{
		Raw:       raw,
		TXmin:     datatype.U32(raw, 0),
		TXmax:     datatype.U32(raw, 4),
		TInfomask: infomask,
		THoff:     int(raw[22]),
		NAttrs:    int(infomask2 & datatype.HEAP_NATTS_MASK),
		PgVersion: pgVersion,
	}
	return t
}

func (t *HeapTuple) headerConsistent() bool {
	if t.THoff < 24 || t.THoff > 256 {
		return false
	}
	bitmapLen := 0
	if t.TInfomask&datatype.HEAP_HASNULL != 0 {
		bitmapLen = (t.NAttrs + 7) / 8
	}
	oidLen := 0
	if t.PgVersion < 12 && t.TInfomask&HEAP_HASOID != 0 {
		oidLen = 4
	}
	expected := (HEAP_TUPLE_HEADER_SIZE + bitmapLen + oidLen + 7) &^ 7
	return expected == t.THoff
}

func (t *HeapTuple) isInsertAborted() bool {
	if t.TInfomask&HEAP_XMIN_FROZEN == HEAP_XMIN_FROZEN {
		return false
	}
	return t.TInfomask&HEAP_XMIN_INVALID != 0 && t.TInfomask&HEAP_XMIN_COMMITTED == 0
}

func (t *HeapTuple) IsDeleted() bool {
	if t.isInsertAborted() {
		return false
	}
	if t.TInfomask&HEAP_XMAX_INVALID != 0 {
		return false
	}
	if t.TInfomask&HEAP_XMAX_LOCK_ONLY != 0 {
		return false
	}
	// v1.0.56 修复（P1-G1）：旧实现对 multixact xmax 一刀切判"未删除"。
	// PG 中行先被 FOR KEY SHARE/SHARE 行锁（xmax 变为 multixact）再被
	// UPDATE/DELETE 时，该 multi 含已提交 updater，行实际不可见，
	// 旧实现会把这类已删行当活行静默导出（外键表 + 并发锁更新 workload 常见）。
	// 离线无法解析 multixact 成员，但 HEAP_KEYS_UPDATED 位仅由更新型成员设置
	// （纯锁成员不设置）：置位即代表 multi 内存在 updater，按已删除处理；
	// 未置位为纯锁 multi，维持判活。aborted updater 在被其他事务 hint 时
	// 会补 HEAP_XMAX_INVALID，由上方 INVALID 检查兜底。
	if t.TInfomask&HEAP_XMAX_IS_MULTI != 0 {
		return t.TInfomask&HEAP_KEYS_UPDATED != 0
	}
	if t.TInfomask&HEAP_XMAX_COMMITTED != 0 {
		return true
	}
	return t.TXmax != 0
}

func (t *HeapTuple) IsLive() bool {
	if t.isInsertAborted() {
		return false
	}
	if t.IsDeleted() {
		return false
	}
	return true
}

// GetNulls：PG12+ bit 置 1 = 非空，清 0 = NULL；PG<=11 相反
func (t *HeapTuple) GetNulls() []bool {
	n := t.NAttrs
	res := make([]bool, n)
	if t.TInfomask&datatype.HEAP_HASNULL == 0 {
		return res
	}
	bitmapLen := (n + 7) / 8
	if HEAP_TUPLE_HEADER_SIZE+bitmapLen > len(t.Raw) {
		return res
	}
	bitmap := t.Raw[HEAP_TUPLE_HEADER_SIZE : HEAP_TUPLE_HEADER_SIZE+bitmapLen]
	for i := 0; i < n; i++ {
		bit := bitmap[i/8]&(1<<(i%8)) != 0
		if t.PgVersion >= 12 {
			res[i] = !bit
		} else {
			res[i] = bit
		}
	}
	return res
}

// ---------- 列布局 ----------
type ColumnDef struct {
	Name       string
	TypeOID    uint32
	AttLen     int
	AttNum     int
	TypMod     int
	NotNull    bool
	Dropped    bool
	AttAlign   string
	AttByVal   bool
	AttStorage string
	// AttGenerated：''=普通列；'s'=存储生成列（PG12+）。生成列的 adbin 语义是
	// 生成表达式而非 DEFAULT，导出 DDL 时必须跳过（v1.0.51）。
	AttGenerated string
	// v1.0.63（B-8）：attidentity（'a'=GENERATED ALWAYS / 'd'=BY DEFAULT）与
	// attcollation（排序规则 OID；0/默认 OID=不输出 COLLATE）
	Identity  string
	Collation uint32
	TXmin     uint32
}

type TableMeta struct {
	OID         uint32
	Schema      string
	RelName     string
	RelFileNode uint32
	ToastRelID  uint32
	// ToastRelFile 为 toast 表的 relfilenode。reltoastrelid 存的是 OID，
	// TRUNCATE/重建过的表 relfilenode != OID，磁盘文件名按 relfilenode 命名，
	// 直接拿 OID 拼路径会指向旧文件/空文件导致外联字段全部 NULL（v1.0.21 修复）。
	ToastRelFile uint32
	// TableSpace 为 pg_class.reltablespace：0=默认表空间（base/<dboid>/），非 0=pg_tblspc/<oid>（v1.0.34）。
	// ToastTableSpace 为 toast 表自身所在表空间（PG 中 toast 与主表同表空间，独立解析更严谨）。
	TableSpace      uint32
	ToastTableSpace uint32
	IsKB            bool
	RelKind         string
	// Persistence：pg_class.relpersistence（'p' 永久 / 'u' UNLOGGED / 't' 临时）
	// v1.0.63（B-8）：UNLOGGED 表此前按普通表输出，回灌后多出 WAL 开销/语义不符
	Persistence string
	Columns     []ColumnDef
	RoleMap     map[uint32]string
	TypeNames   map[uint32]string
	HasToast    bool
	PrimaryKey  []string
	// ChildOIDs 为分区/继承直接子表 OID 列表（pg_inherits.inhparent → inhrelid，v1.0.36）。
	// 仅收录本库内真实表对象（过滤索引继承）；声明式分区父表（relkind='p'）用它展开子分区。
	ChildOIDs []uint32
	// v1.0.52 表级编码覆盖：表数据文件三态探测命中且与库级判定不一致时设置
	// （"utf8"/"gbk"）；空 = 用库级基线。见 refineTableEncoding。
	EncOverride string
	// v1.0.52 分区元数据（见 partition.go）：
	PartStrat      byte     // 'r'/'l'/'h'（pg_partitioned_table.partstrat）；0=非分区父表
	PartKeyCols    []int    // 分区键列号（pg_partitioned_table.partattrs；含 0=表达式键）
	PartBound      string   // 子表绑定子句（relpartbound 反解）："FOR VALUES ..." / "DEFAULT"
	PartBoundFail  bool     // relpartbound 存在但反解失败 → 回退普通表 + 人工提示
	PartParent     uint32   // 子表父表 OID（pg_inherits 反查；0=非分区子表）
	PartParentQual string   // 父表限定名 schema.name（构建期解析；空=解析失败回退普通表）
	PartExprs      []string // 表达式分区键反解（partexprs，与 PartKeyCols 中 0 位对应）
	// v1.0.53 完整性自证：pg_class 统计量（上次 VACUUM/ANALYZE 的快照），
	// 与实测行数/页数对照，用于发现"静默少导"（-1=从未分析）。
	RelTuples float32
	RelPages  int32
	// v1.0.53：pg_class.relisshared —— 共享目录（pg_database/pg_authid 等）数据文件在
	// <PGDATA>/global/ 而非 base/<dboid>/，按库批量导出无法定位，需单独标注跳过。
	Shared bool
	// v1.0.63（B-8）：DDL 侧需要的库上下文（排序规则名/版本相关语法差异）。
	// DbDir 为数据库目录（base/<dboid>），PgVersion 为目录版本号。
	DbDir     string
	PgVersion int
}

func (tm *TableMeta) FullName() string { return tm.Schema + "." + tm.RelName }

// 与 Python types.VARLENA_TYPES 严格对齐：仅"可外联的 varlena 类型"。
// 注意：数组/几何/网络等 attlen==-1 的类型无需在此列出（attlen 判定已覆盖）；
// 定长类型（uuid/money/macaddr/point 等）绝不能误标 varlena，否则提取错位。
var varlenaTypes = map[uint32]bool{
	25: true, 1043: true, 1042: true, 17: true, 114: true, 3802: true,
	1700: true, 1560: true, 1562: true, 705: true, 142: true, 32: true,
	3361: true, 3402: true, 5017: true,
}

// MaxRowNatts：扫描数据文件所有行头，返回最大行内列数（金仓低位/PG 高位）。
// 用于幽灵列截断：金仓 ALTER 残留 att 行（attnum 超出数据行实际列数）时，
// 行内列数 < catalog 列数，按行内最大列数截断列集，避免位图/值区错位丢行。
func MaxRowNatts(path string, pageSize int, isKB bool) int {
	ps := pageSize
	if ps == 0 {
		ps = ProbePageSize(path)
	}
	if ps == 0 {
		return 0
	}
	buf := make([]byte, ps)
	maxn := 0
	for _, seg := range SegFiles(path) {
		f, err := os.Open(seg)
		if err != nil {
			continue
		}
		fi, err := f.Stat()
		if err != nil || fi.Size() <= 0 {
			f.Close()
			continue
		}
		total := int(fi.Size())
		for off := 0; off+ps <= total; off += ps {
			if _, err := f.ReadAt(buf, int64(off)); err != nil {
				break
			}
			lay := TryStandardLayout(buf, ps)
			if lay == nil {
				lay = TryAutoLayout(buf, ps)
			}
			if lay == nil {
				continue
			}
			lower := int(lay.Lower)
			itemEnd := int(lay.HeaderEnd)
			for i := 0; itemEnd+4*i+4 <= lower; i++ {
				rawID := uint32(datatype.U16(buf, itemEnd+4*i)) | uint32(datatype.U16(buf, itemEnd+4*i+2))<<16
				o := int(rawID & 0x7FFF)
				flags := (rawID >> 15) & 0x03
				ln := int((rawID >> 17) & 0x7FFF)
				if flags != 1 || o >= ps || ln > ps || o+ln > ps || ln < HEAP_TUPLE_HEADER_SIZE {
					continue
				}
				td := buf[o : o+ln]
				i2 := datatype.U16(td, 18)
				// 行内列数取 t_infomask2 低 11 位（PG HeapTupleHeaderGetNatts = i2 & 0x07FF，
				// 高 5 位为 KEYS_UPDATED/HOT_UPDATED/ONLY_TUPLE/IS_PARTITION/CANT_RECORD 标志位，
				// 误读高 11 位会把标志值当成列数导致幽灵列截断误伤正常表；金仓同布局，统一取低位）。
				natts := int(i2 & 0x7FF)
				if natts > maxn {
					maxn = natts
				}
			}
		}
		f.Close()
	}
	return maxn
}

func buildColLengths(tm *TableMeta) []datatype.ColLen {
	res := make([]datatype.ColLen, 0, len(tm.Columns))
	for _, col := range tm.Columns {
		isVar := col.AttLen == -1 || varlenaTypes[col.TypeOID]
		attlen := col.AttLen
		if attlen < 0 {
			attlen = 0
		}
		res = append(res, datatype.ColLen{AttLen: attlen, IsVarlena: isVar, AttAlign: col.AttAlign})
	}
	return res
}

func calculateTupleSize(t *HeapTuple, colLengths []datatype.ColLen) int {
	nulls := t.GetNulls()
	dataSize := 0
	nraw := len(t.Raw)
	for i, item := range colLengths {
		align := datatype.LayoutAlign(item.AttLen, item.AttAlign)
		if i < len(nulls) && nulls[i] {
			continue
		}
		if item.IsVarlena {
			offset := t.THoff + dataSize
			if offset >= nraw {
				break
			}
			if t.Raw[offset]&1 == 0 {
				if align > 1 && (t.THoff+dataSize)%align != 0 {
					dataSize = (dataSize + align - 1) &^ (align - 1)
					offset = t.THoff + dataSize
					if offset >= nraw {
						break
					}
				}
			}
			kind, total, _, _ := datatype.VarlenaParse(t.Raw, offset)
			if kind == datatype.VARLENA_EXTERNAL {
				// v1.0.63 修复（B-10）：累加前必须校验不越过元组末尾。
				// 旧实现在这里不校验（ExtractFieldsDirect 里有），坏 varlena 头会把
				// nextPos 顶出页外 → 该页后续可见元组静默丢失且不计 bad_pages。
				if offset+datatype.KB_EXTERNAL_SIZE > nraw {
					return nraw
				}
				dataSize += datatype.KB_EXTERNAL_SIZE
			} else if kind != "" {
				if total <= 0 || offset+total > nraw {
					return nraw
				}
				dataSize += total
			} else {
				break
			}
		} else {
			if align > 1 {
				dataSize = (dataSize + align - 1) &^ (align - 1)
			}
			if item.AttLen > 0 && t.THoff+dataSize+item.AttLen > nraw {
				return nraw // 定长列同样越界 → 按"元组延伸到数据区末"处理
			}
			dataSize += item.AttLen
		}
	}
	return t.THoff + dataSize
}

// ---------- TOAST ----------
type ChunkPos struct {
	Seq    uint32
	Pageno int
	Off    int
}

type ToastFile struct {
	Path       string
	PageSize   int
	PosIndex   map[uint32][]ChunkPos
	PageCache  map[int]map[int][]byte
	CacheOrder []int
	CacheMax   int
	PgVersion  int
	IsKB       bool
	Decryptor  PageDecryptor // v1.0.39 TDE：TOAST 页解密（与主表同密钥；免费版为 nil）
	file       *os.File
	fileOnce   sync.Once     // 并发解析时仅初始化一次文件句柄
	segFs      []*os.File    // 多段文件句柄（>1GB 续段 .1/.2/...）
	segTab     *SegPageTable // v1.0.56 R3：按实际大小推导的段边界表（openFile 时构建）
	mu         sync.RWMutex  // 并发解析时保护 PageCache 读写与 LRU 淘汰
	// v1.0.44 P2-3：TOAST 数据损坏计数（atomic，并行 worker 安全；报告输出）
	MissingCount int64 // 外联 chunk 缺失/长度不符（整列降级占位符）
	CorruptCount int64 // 压缩数据解压失败（datatype.ToastDecompress 返回 nil）
}

// openFile：打开主文件 + 续段文件链（v1.0.30 多段支持）
func (tf *ToastFile) openFile() {
	tf.fileOnce.Do(func() {
		paths := SegFiles(tf.Path)
		tf.segFs = make([]*os.File, len(paths))
		for i, p := range paths {
			if f, err := os.Open(p); err == nil {
				tf.segFs[i] = f
			}
		}
		if len(tf.segFs) > 0 {
			tf.file = tf.segFs[0]
		}
		// v1.0.56（R3）：段边界按实际文件大小统一推导
		tf.segTab = BuildSegPageTable(tf.Path, tf.PageSize)
	})
}

// segReadAt：按全局页号跨段读取（v1.0.30；v1.0.49 段边界按页大小自适应）
func (tf *ToastFile) segReadAt(buf []byte, pageno int) (int, error) {
	// v1.0.56（R3）：统一走按实际文件大小累计的段边界表；表不可用（未 openFile）
	// 时回退旧均匀容量推导
	seg, off := tf.segTab.Locate(pageno)
	if tf.segTab == nil || len(tf.segTab.Base) == 0 {
		seg, off = segPos(pageno, tf.PageSize)
	}
	if seg >= len(tf.segFs) || tf.segFs[seg] == nil {
		return 0, os.ErrNotExist
	}
	n, err := tf.segFs[seg].ReadAt(buf, int64(off)*int64(tf.PageSize))
	if err != nil && n == 0 {
		return n, err
	}
	// v1.0.39 TDE：TOAST 页同样 offset64+ 加密（与主表同密钥），读后解密
	if tf.Decryptor != nil && n >= 80 {
		if _, derr := tf.Decryptor.DecryptPage(buf[:n]); derr != nil {
			return n, derr
		}
	}
	return n, err
}

var toastColLengths = []datatype.ColLen{
	{AttLen: 4, IsVarlena: false, AttAlign: "i"}, // chunk_id
	{AttLen: 4, IsVarlena: false, AttAlign: "i"}, // chunk_seq
	{AttLen: 0, IsVarlena: true, AttAlign: "i"},  // chunk_data (varlena)
}

// 提取 TOAST 元组的 (chunk_id, chunk_seq)
func toastLightTuple(raw []byte, tHoff int, nulls []bool) (uint32, uint32) {
	if tHoff+8 > len(raw) {
		return 0, 0
	}
	if len(nulls) >= 2 && (nulls[0] || nulls[1]) {
		return 0, 0
	}
	chunkID := datatype.U32(raw, tHoff)
	chunkSeq := datatype.U32(raw, tHoff+4)
	if !(1 <= chunkID && chunkID <= 100000000) {
		return 0, 0
	}
	if !(0 <= chunkSeq && chunkSeq <= 1000000) {
		return 0, 0
	}
	if len(nulls) > 2 && nulls[2] {
		return 0, 0
	}
	voff := tHoff + 8
	if voff >= len(raw) {
		return 0, 0
	}
	kind, total, _, _ := datatype.VarlenaParse(raw, voff)
	if kind == "" {
		return 0, 0
	}
	if kind == datatype.VARLENA_1B && total <= 1 {
		return 0, 0
	}
	if (kind == datatype.VARLENA_4B || kind == datatype.VARLENA_4BC) && total <= 4 {
		return 0, 0
	}
	return chunkID, chunkSeq
}

// 提取 TOAST 元组三字段 → chunk
func fieldsToChunk(fields []*[]byte) (uint32, uint32, []byte) {
	if len(fields) < 3 || fields[0] == nil || fields[1] == nil || fields[2] == nil {
		return 0, 0, nil
	}
	chunkID := datatype.U32(*fields[0], 0)
	chunkSeq := datatype.U32(*fields[1], 0)
	if !(1 <= chunkID && chunkID <= 100000000) {
		return 0, 0, nil
	}
	if !(0 <= chunkSeq && chunkSeq <= 1000000) {
		return 0, 0, nil
	}
	f := *fields[2]
	var payload []byte
	kind, total, _, _ := datatype.VarlenaParse(f, 0)
	switch kind {
	case datatype.VARLENA_1B:
		payload = f[1 : 1+total-1]
	case datatype.VARLENA_4B:
		payload = f[4 : 4+total-4]
	case datatype.VARLENA_4BC:
		// chunk_data 内联压缩极少见；此处按原样（解码层处理）
		payload = f[4 : 4+total-4]
	default:
		return 0, 0, nil
	}
	return chunkID, chunkSeq, payload
}

// 解析一页，返回 [(offset, chunk_id, chunk_seq, payload_or_nil)]
func extractPageChunks(raw []byte, pageno int, ps int, wantPayload bool) [][4]interface{} {
	lay := TryStandardLayout(raw, ps)
	if lay == nil {
		lay = TryAutoLayout(raw, ps)
	}
	if lay == nil {
		return nil
	}
	var out [][4]interface{}
	lower := int(lay.Lower)
	itemFound := false
	for i := 0; lay.HeaderEnd+4*i+4 <= lower; i++ {
		rawID := uint32(datatype.U16(raw, lay.HeaderEnd+4*i)) | uint32(datatype.U16(raw, lay.HeaderEnd+4*i+2))<<16
		off := int(rawID & 0x7FFF)
		flags := (rawID >> 15) & 0x03
		ln := int((rawID >> 17) & 0x7FFF)
		if flags != ITEMID_NORMAL || off >= ps || ln > ps || off+ln > ps {
			continue
		}
		itemFound = true
		td := raw[off : off+ln]
		tup := ParseTuple(td, 12)
		if tup == nil || !tup.headerConsistent() || !tup.IsLive() {
			continue
		}
		nulls := tup.GetNulls()
		if wantPayload {
			fields := datatype.ExtractFieldsDirect(td, tup.THoff, nulls, toastColLengths)
			cid, cseq, payload := fieldsToChunk(fields)
			if cid != 0 {
				out = append(out, [4]interface{}{off, cid, cseq, payload})
			}
		} else {
			cid, cseq := toastLightTuple(td, tup.THoff, nulls)
			if cid != 0 {
				out = append(out, [4]interface{}{off, cid, cseq, nil})
			}
		}
	}
	if !itemFound {
		// 扫描模式回退
		pos := int(lay.Upper)
		special := int(lay.Special)
		for pos+HEAP_TUPLE_HEADER_SIZE <= special {
			td := raw[pos:special]
			tup := ParseTuple(td, 12)
			if tup != nil && tup.headerConsistent() && tup.IsLive() {
				nulls := tup.GetNulls()
				if wantPayload {
					fields := datatype.ExtractFieldsDirect(td, tup.THoff, nulls, toastColLengths)
					cid, cseq, payload := fieldsToChunk(fields)
					if cid != 0 {
						out = append(out, [4]interface{}{pos, cid, cseq, payload})
					}
				} else {
					cid, cseq := toastLightTuple(td, tup.THoff, nulls)
					if cid != 0 {
						out = append(out, [4]interface{}{pos, cid, cseq, nil})
					}
				}
			}
			est := 0
			if tup != nil {
				est = tup.THoff + tup.NAttrs
			} else {
				est = 23
			}
			nxt := (est + 7) &^ 7
			if nxt < 8 {
				nxt = 8
			}
			pos += nxt
		}
	}
	return out
}

// 构建位置索引（light 模式）
func (tf *ToastFile) BuildIndex(verbose bool) {
	tf.PosIndex = make(map[uint32][]ChunkPos)
	npages := 0
	for _, p := range SegFiles(tf.Path) {
		if fi, err := os.Stat(p); err == nil {
			npages += int(fi.Size()) / tf.PageSize
		}
	}
	buf := make([]byte, tf.PageSize)
	base := 0
	for _, seg := range SegFiles(tf.Path) {
		f, err := os.Open(seg)
		if err != nil {
			continue
		}
		pageno := base
		for {
			if _, err := readFullPage(f, buf); err != nil {
				break
			}
			allZero := true
			for i := 0; i < 64; i++ {
				if buf[i] != 0 {
					allZero = false
					break
				}
			}
			if allZero {
				pageno++
				continue
			}
			chunks := extractPageChunks(buf, pageno, tf.PageSize, false)
			for _, c := range chunks {
				off := c[0].(int)
				cid := c[1].(uint32)
				cseq := c[2].(uint32)
				tf.PosIndex[cid] = append(tf.PosIndex[cid], ChunkPos{cseq, pageno, off})
			}
			pageno++
		}
		f.Close()
		if fi, err := os.Stat(seg); err == nil {
			base += int(fi.Size()) / tf.PageSize
		}
	}
	for vid := range tf.PosIndex {
		sort.Slice(tf.PosIndex[vid], func(a, b int) bool {
			return tf.PosIndex[vid][a].Seq < tf.PosIndex[vid][b].Seq
		})
	}
}

// BuildIndexParallel：并发构建位置索引（worker 池按页分片，worker 各自 ReadAt + 页内解析，
// 避免共享读缓冲的复用覆盖；worker 独立本地 map，归并在 wg.Wait 后单 goroutine 执行）。
func (tf *ToastFile) BuildIndexParallel(workers int, verbose bool) {
	tf.PosIndex = make(map[uint32][]ChunkPos)
	npages := 0
	for _, p := range SegFiles(tf.Path) {
		if fi, err := os.Stat(p); err == nil {
			npages += int(fi.Size()) / tf.PageSize
		}
	}
	if workers <= 1 || npages <= 1 {
		tf.BuildIndex(verbose)
		return
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	localMaps := make([]map[uint32][]ChunkPos, workers)
	for w := 0; w < workers; w++ {
		local := map[uint32][]ChunkPos{}
		localMaps[w] = local
		wg.Add(1)
		go func(local map[uint32][]ChunkPos) {
			defer wg.Done()
			segPaths := SegFiles(tf.Path)
			segTab := buildSegPageTableFromPaths(segPaths, tf.PageSize) // v1.0.56 R3
			segFs := make([]*os.File, len(segPaths))
			defer func() {
				for _, sf := range segFs {
					if sf != nil {
						sf.Close()
					}
				}
			}()
			for i, p := range segPaths {
				if f, err := os.Open(p); err == nil {
					segFs[i] = f
				}
			}
			buf := make([]byte, tf.PageSize)
			processed := 0
			for pageno := range jobs {
				seg, off := segTab.Locate(pageno)
				if seg >= len(segFs) || segFs[seg] == nil {
					continue
				}
				n, err := segFs[seg].ReadAt(buf, int64(off)*int64(tf.PageSize))
				if err != nil || n < tf.PageSize {
					continue
				}
				// v1.0.39 TDE：TOAST 索引预建同样需要先解密页
				if tf.Decryptor != nil {
					if _, derr := tf.Decryptor.DecryptPage(buf[:n]); derr != nil {
						continue
					}
				}
				allZero := true
				for i := 0; i < 64; i++ {
					if buf[i] != 0 {
						allZero = false
						break
					}
				}
				if allZero {
					continue
				}
				chunks := extractPageChunks(buf, pageno, tf.PageSize, false)
				for _, c := range chunks {
					off := c[0].(int)
					cid := c[1].(uint32)
					cseq := c[2].(uint32)
					local[cid] = append(local[cid], ChunkPos{cseq, pageno, off})
				}
				processed++
				if verbose && processed%2000 == 0 {
					logx.Logf("  TOAST 索引: %d/%d 页...", pageno, npages)
				}
			}
		}(local)
	}
	for pageno := 0; pageno < npages; pageno++ {
		jobs <- pageno
	}
	close(jobs)
	wg.Wait()
	// 归并（单 goroutine，无竞争）
	for _, local := range localMaps {
		for vid, poss := range local {
			tf.PosIndex[vid] = append(tf.PosIndex[vid], poss...)
		}
	}
	for vid := range tf.PosIndex {
		sort.Slice(tf.PosIndex[vid], func(a, b int) bool {
			return tf.PosIndex[vid][a].Seq < tf.PosIndex[vid][b].Seq
		})
	}
}

func (tf *ToastFile) loadPagePayloads(pageno int) map[int][]byte {
	tf.mu.RLock()
	cached, ok := tf.PageCache[pageno]
	tf.mu.RUnlock()
	if ok {
		// LRU：命中页移到队列末尾
		tf.mu.Lock()
		for i, p := range tf.CacheOrder {
			if p == pageno {
				tf.CacheOrder = append(tf.CacheOrder[:i], tf.CacheOrder[i+1:]...)
				break
			}
		}
		tf.CacheOrder = append(tf.CacheOrder, pageno)
		tf.mu.Unlock()
		return cached
	}
	tf.openFile()
	if len(tf.segFs) == 0 || tf.segFs[0] == nil {
		return nil
	}
	buf := make([]byte, tf.PageSize)
	_, err := tf.segReadAt(buf, pageno)
	if err != nil {
		return nil
	}
	result := map[int][]byte{}
	chunks := extractPageChunks(buf, pageno, tf.PageSize, true)
	for _, c := range chunks {
		off := c[0].(int)
		payload := c[3].([]byte)
		if payload != nil {
			result[off] = payload
		}
	}
	tf.mu.Lock()
	tf.PageCache[pageno] = result
	tf.CacheOrder = append(tf.CacheOrder, pageno)
	// 真 LRU 淘汰：从队头（最久未用）删除，直到不超过 CacheMax
	for len(tf.PageCache) > tf.CacheMax && len(tf.CacheOrder) > 0 {
		victim := tf.CacheOrder[0]
		tf.CacheOrder = tf.CacheOrder[1:]
		if _, ok := tf.PageCache[victim]; ok {
			delete(tf.PageCache, victim)
		}
	}
	tf.mu.Unlock()
	return result
}

func (tf *ToastFile) FetchAndReassemble(valueid uint32, expectedSize int) []byte {
	positions, ok := tf.PosIndex[valueid]
	if !ok || len(positions) == 0 {
		return nil
	}
	parts := make([][]byte, 0, len(positions))
	total := 0
	for _, p := range positions {
		payloads := tf.loadPagePayloads(p.Pageno)
		if payloads == nil {
			continue
		}
		payload, ok2 := payloads[p.Off]
		if !ok2 {
			continue
		}
		parts = append(parts, payload)
		total += len(payload)
	}
	if len(parts) == 0 {
		return nil
	}
	out := make([]byte, 0, total)
	for _, p := range parts {
		out = append(out, p...)
	}
	if expectedSize > 0 && len(out) != expectedSize {
		return nil
	}
	return out
}

// ---------- 行生成 ----------
type Row struct {
	CTID    string
	Values  []*string // nil = NULL
	Deleted bool
}

// 检查 varlena 是否外联：返回 (payload, isExt, extInfo)
func CheckExternal(raw []byte) ([]byte, bool, *datatype.ExternalInfo) {
	if len(raw) == 0 {
		return raw, false, nil
	}
	kind, total, _, _ := datatype.VarlenaParse(raw, 0)
	switch kind {
	case datatype.VARLENA_EXTERNAL:
		ext := datatype.ParseExternalPointer(raw, 0)
		if ext != nil {
			return []byte{}, true, ext
		}
		return raw, false, nil
	case datatype.VARLENA_1B:
		p := total - 1
		if p < 0 {
			p = 0
		}
		if 1+p > len(raw) {
			p = len(raw) - 1
		}
		return raw[1 : 1+p], false, nil
	case datatype.VARLENA_4B, datatype.VARLENA_4BC:
		p := total - 4
		if p < 0 {
			p = 0
		}
		if 4+p > len(raw) {
			p = len(raw) - 4
		}
		return raw[4 : 4+p], false, nil
	}
	return raw, false, nil
}

func decodeFields(fields []*[]byte, tm *TableMeta, toast *ToastFile) []*string {
	values := make([]*string, 0, len(tm.Columns))
	// 注意：RoleMap 由 main.go 在解析开始前一次性 datatype.SetRoleMap 注入（并发解析期只读，不再此处写）
	for i, col := range tm.Columns {
		if col.Dropped {
			continue
		}
		if i >= len(fields) {
			values = append(values, nil)
			continue
		}
		raw := fields[i]
		if raw == nil {
			values = append(values, nil)
			continue
		}
		isVarCol := col.AttLen == -1 || varlenaTypes[col.TypeOID]
		rawB := *raw
		if isVarCol && len(rawB) > 0 {
			_, isExt, ext := CheckExternal(rawB)
			if isExt && toast != nil {
				reassembled := toast.FetchAndReassemble(ext.ValueID, 0)
				if reassembled == nil {
					// v1.0.44 P2-3：TOAST 缺失计数（不再静默）
					atomic.AddInt64(&toast.MissingCount, 1)
					s := "__TOAST_MISSING__"
					values = append(values, &s)
					continue
				}
				expected := int(ext.RawSize) - 4
				if ext.Compressed {
					data := datatype.ToastDecompress(reassembled, expected, ext.Method)
					if data == nil {
						// v1.0.44 P2-3：TOAST 损坏计数（解压失败）
						atomic.AddInt64(&toast.CorruptCount, 1)
						s := "__TOAST_CORRUPT__"
						values = append(values, &s)
						continue
					}
					rawB = data
				} else if len(reassembled) != expected {
					// v1.0.44 P2-3：长度不符归入缺失计数
					atomic.AddInt64(&toast.MissingCount, 1)
					s := "__TOAST_MISSING__"
					values = append(values, &s)
					continue
				} else {
					rawB = reassembled
				}
				rawB = datatype.RebuildVarlena(rawB)
			} else if isExt && toast == nil {
				s := "__TOAST_MISSING__"
				values = append(values, &s)
				continue
			}
			// 非外联：rawB 保持完整 varlena（含头），decoder 内部自行解头
		}
		decoded := datatype.DecodeValue(col.TypeOID, rawB)
		values = append(values, &decoded)
	}
	return values
}

// isToastPlaceholder：TOAST 降级占位符判定（缺失 + 损坏统一按 NULL 语义输出）。
// v1.0.60 修复（P2-11）：__TOAST_CORRUPT__ 此前不在占位符清单里，被当真实列值
// 写进 SQL/CSV（回灌后产生字面 '__TOAST_CORRUPT__' 数据，与 MISSING→NULL 不一致）。
func isToastPlaceholder(v *string) bool {
	if v == nil {
		return false
	}
	return *v == "__TOAST_MISSING__" || *v == "__TOAST_CORRUPT__"
}

// dumpRows：两阶段（标准 ItemId → 扫描回退）
type RowIter struct {
	TM           *TableMeta
	Toast        *ToastFile
	IncludeDel   bool
	OnlyDeleted  bool
	Limit        int
	PageSize     int
	PGVersion    int
	IsKB         bool
	Path         string
	badPages     []int
	badMu        sync.Mutex // v1.0.44 P2-2：保护 badPages 追加（并行 worker 安全）
	BadCount     int64      // v1.0.38：坏页计数（atomic，并行 worker 安全；报告用）
	VerboseDebug bool
	Parallel     int           // 行解析 worker 数（>1 启用按页分片并发解析）
	Decryptor    PageDecryptor // v1.0.39 TDE：页读取后解密（offset64+ SM4-ECB；免费版为 nil）
	// v1.0.40 --progress：已处理页数/行数（原子，串并行通用；exportOneTable 的 progress goroutine 读取）
	ProgressDone int64
	ProgressRows int64
	// v1.0.62（P3）：预扫阶段已扫页数。预扫发生在正式输出之前，旧实现不计数
	// → 大表在真正开始输出前的整段时间进度恒为 0%（进度条"卡死"观感）。
	// 单独计数，进度行在 ProgressDone==0 时改显示"预扫 k/N"。
	ProbePages int64
}

// addBadPage：记录坏页（v1.0.44 P2-2：原子计数 + 明细页号记录，报告输出 bad_pages 列表）
func (ri *RowIter) addBadPage(pageno int) {
	atomic.AddInt64(&ri.BadCount, 1)
	ri.badMu.Lock()
	ri.badPages = append(ri.badPages, pageno)
	ri.badMu.Unlock()
}

// ---------- B12 修复：TDE 密文页轻量探测（不依赖 -tags tde 构建） ----------

// suspectCipherPage：单页密文启发式（v1.0.54 重写）。
//
// 旧判据「offset 64 起 16B 全 0 块 < 8 即密文」等价于「页内空闲 < 128B 即密文」，
// 而 fillfactor=100 的正常页填到装不下下一行为止、空闲常仅几十字节 —— 于是几乎
// 所有装满的表都被误报为「TDE 加密、输出不可信」（v1.0.53 实测：PG17 纯 ASCII 表
// 5/6 页误报、金仓 GBK 表 3/4 页误报）。
//
// 新判据以「数据区零字节密度」为主信号（实测标定）：
//   - 明文满页（8K，含 tuple 头/空值位图/空闲区）：零密度 0.07~0.31
//   - SM4 密文页：零密度 ≈ 0.004（1/256 随机分布）
//
// 并用「元组头可解析」排除明文低零密度页（bytea/压缩值等随机字节列），
// 避免把纯二进制内容的正常页误判成密文。
func suspectCipherPage(b []byte) bool {
	if len(b) < 80 {
		return false
	}
	// v1.0.49 修复（B1）：pd_lower/pd_upper 位于页头 offset 12/14，
	// 此前误读 offset 24/26（pd_prune_xid），导致真密文页漏报、静默导出垃圾。
	lower := int(b[12]) | int(b[13])<<8
	upper := int(b[14]) | int(b[15])<<8
	if ZeroPage(b) {
		return false // 空闲页/未初始化页：无数据也无密文
	}
	sane := (lower != 0 || upper != 0) && lower >= 28 && upper <= len(b) && lower <= upper
	var seg []byte
	if sane {
		// 有充足空闲区（≥128B）的页必然是明文：密文页整段数据都是随机字节，
		// 不会出现成片的 0 空闲区。
		if upper-lower >= 128 {
			return false
		}
		// PG 堆页元组自页尾向前存放：数据区为 [pd_upper, 页尾)，
		// 密文（offset 64 起加密）在该区间内表现为随机字节。
		start := upper
		if start < 64 {
			start = 64
		}
		seg = b[start:]
	} else {
		// 金仓 TDE 变体页头可为 0（pd_lower/pd_upper 均 0），整段都是密文。
		seg = b[64:]
	}
	if zeroRatio(seg) >= 0.02 {
		return false // 明文实测 ≥0.07，密文 ≈0.004，0.02 两侧各留 3.5x/5x 余量
	}
	// 低零密度但元组头自洽 → 明文（bytea/压缩列等随机字节内容），不是密文。
	if sane && hasSaneTuple(b, lower, upper) {
		return false
	}
	return true
}

// TupleSane：严格元组自洽性判定（错密钥/密文页检测用，不对数据放宽）。
// v1.0.54：从 tde_verify.go（-tags tde）下沉到公共文件——免费版的密文页探测同样需要。
func TupleSane(raw []byte, ncols int) bool {
	if len(raw) < 24 {
		return false
	}
	infomask2 := datatype.U16(raw, 18)
	infomask := datatype.U16(raw, 20)
	natts := int(infomask2 & datatype.HEAP_NATTS_MASK)
	if natts < 1 || natts > 1600 {
		return false
	}
	if ncols > 0 && natts > ncols+8 { // +8：ALTER TABLE 残留 att 容差
		return false
	}
	hoff := int(raw[22])
	bm := 0
	if infomask&0x0001 != 0 { // datatype.HEAP_HASNULL
		bm = (natts + 7) / 8
	}
	want := (23 + bm + 7) &^ 7
	if hoff < want || hoff > want+8 || hoff > len(raw) {
		return false
	}
	return true
}

// ZeroPage：整页是否全 0（未使用的页/损坏页）。
func ZeroPage(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// zeroRatio：切片内零字节占比。
func zeroRatio(b []byte) float64 {
	if len(b) == 0 {
		return 1
	}
	z := 0
	for _, c := range b {
		if c == 0 {
			z++
		}
	}
	return float64(z) / float64(len(b))
}

// hasSaneTuple：页内是否存在可解析的行（用于区分「密文」与「明文随机字节内容」）。
// 只看 item 指针对应的元组头自洽性，不做完整解码。
//
// 判据：≥2 个元组头自洽即认为明文（真密文页的乱码指针偶然命中 1 次的概率不低，
// 命中 2 次极低）；页内只有 1 个候选元组时，命中 1 次即可。
func hasSaneTuple(b []byte, lower, upper int) bool {
	if lower <= 0 || upper > len(b) || lower > upper {
		return false
	}
	sane, cand := 0, 0
	for i := 0; cand < 16 && lower >= 24+4*(i+1); i++ { // 最多看 16 个行指针，保持探测开销恒定
		raw := uint32(datatype.U16(b, 24+4*i)) | uint32(datatype.U16(b, 24+4*i+2))<<16
		if (raw>>15)&0x03 != ITEMID_NORMAL {
			continue
		}
		cand++
		off := int(raw & 0x7FFF)
		ln := int((raw >> 17) & 0x7FFF)
		if ln < 24 || off < 24 || off+ln > len(b) {
			continue
		}
		if TupleSane(b[off:off+ln], 0) {
			sane++
			if sane >= 2 {
				return true
			}
		}
	}
	return sane == 1 && cand == 1
}

// probeTableCipher：对表数据文件前 32 页采样，返回 (检查页数, 疑似密文页数)。
// path 支持多段文件（SegFiles 模式）与单文件。
func probeTableCipher(path string, pageSize int) (int, int) {
	if path == "" || pageSize < 1024 {
		return 0, 0
	}
	checked, suspect := 0, 0
	for _, seg := range SegFiles(path) {
		f, err := os.Open(seg)
		if err != nil {
			continue
		}
		buf := make([]byte, pageSize)
		for i := 0; i < 32; i++ {
			n, err := readFullPage(f, buf)
			if err != nil {
				break
			}
			if suspectCipherPage(buf[:n]) {
				suspect++
			}
			checked++
		}
		f.Close()
		if checked >= 32 {
			break
		}
	}
	return checked, suspect
}

// WarnIfCipherPages：密文页告警（B12）。v1.0.46 从"仅 0 行"扩展到所有无解密器的输出场景
// （--count / --data / --sql），因为加密表解不出正确行结构时可能给出少量假行（实测金仓加密
// TOAST 表 10 行在无密钥时统计为 1 行、加密表错密钥时导出 2 行垃圾），静默比 0 行更危险。
func WarnIfCipherPages(ri *RowIter, ps int, dec PageDecryptor, rows int64) {
	if ri == nil || dec != nil || ri.Path == "" {
		return
	}
	checked, suspect := probeTableCipher(ri.Path, ps)
	if checked < 1 || suspect == 0 {
		return
	}
	name := ri.Path
	if ri.TM != nil {
		name = ri.TM.FullName()
	}
	if rows == 0 {
		// v1.0.62（P3）：免费版二进制 strings 审计要求 0 处 "TDE" 字面量，
		// 提示语改用"表/表空间透明加密"的中文表述（参数名本身不含该词，可保留）。
		logx.Logf("警告: 表 %s 导出 0 行但 %d/%d 个数据页呈密文特征——可能是透明加密表且未提供密钥/密钥无效（--enckey 或 --wallet-password），或表为空/全部已删除，请核实", name, suspect, checked)
		return
	}
	logx.Logf("警告: 表 %s 有 %d/%d 个数据页呈密文特征（疑透明加密表且未提供密钥），当前输出 %d 行不可信；请提供 --enckey/--wallet-password 或使用含离线解密能力的构建", name, suspect, checked, rows)
}

// BadPagesSnapshot：返回坏页明细副本（报告用）
func (ri *RowIter) BadPagesSnapshot() []int {
	ri.badMu.Lock()
	out := make([]int, len(ri.badPages))
	copy(out, ri.badPages)
	ri.badMu.Unlock()
	return out
}

// ToastMissing / ToastCorrupt：TOAST 损坏计数（v1.0.44 P2-3，报告用；无 TOAST 时为 0）
func (ri *RowIter) ToastMissing() int64 {
	if ri.Toast == nil {
		return 0
	}
	return atomic.LoadInt64(&ri.Toast.MissingCount)
}

func (ri *RowIter) ToastCorrupt() int64 {
	if ri.Toast == nil {
		return 0
	}
	return atomic.LoadInt64(&ri.Toast.CorruptCount)
}

// iterTuples 返回 (元组列表, 页布局是否合法)。
// v1.0.55（B1）：此前用 nil 同时表达三种含义——布局非法（真坏页）、全零页、
// 布局合法但页内无可见元组——调用点把 nil 一律记为坏页，导致 VACUUM 后的
// 合法空页（lower=24/upper=8192、行指针为 0，页头完全自洽）被误报
// 「损坏数据块」并拉低 verdict（实测 2 万行删半 + VACUUM 误报 102 坏页）。
// 现拆分为双返回值：只有布局非法且非全零页才算坏页；全零页与合法空页
// （含「有布局但 0 个 NORMAL 行指针」）都按正常页处理。
func (ri *RowIter) IterTuples(pageno int, page []byte) ([][2]interface{}, bool) {
	lay := TryStandardLayout(page, ri.PageSize)
	if lay == nil {
		lay = TryAutoLayout(page, ri.PageSize)
	}
	if lay == nil {
		return nil, false
	}
	var out [][2]interface{}
	lower := int(lay.Lower)
	for i := 0; lay.HeaderEnd+4*i+4 <= lower; i++ {
		rawID := uint32(datatype.U16(page, lay.HeaderEnd+4*i)) | uint32(datatype.U16(page, lay.HeaderEnd+4*i+2))<<16
		off := int(rawID & 0x7FFF)
		flags := (rawID >> 15) & 0x03
		ln := int((rawID >> 17) & 0x7FFF)
		if flags != ITEMID_NORMAL || off >= ri.PageSize || ln > ri.PageSize || off+ln > ri.PageSize {
			continue
		}
		tup := ParseTuple(page[off:off+ln], ri.PGVersion)
		if tup == nil {
			continue
		}
		out = append(out, [2]interface{}{i, tup})
	}
	return out, true
}

// buildRow：把已解析的元组转成输出行。
//
// v1.0.62（P3）语义澄清：posTag 第二项有两种来源，输出格式相同但含义不同——
//   - ItemId 路径（streamStd*）：posTag[1] = line pointer 下标（0 基，
//     等价 PG 的 ctid 第二项 = 下标+1，可直接用于 WHERE ctid='(p, i+1)'）；
//   - 线性扫描回退（streamScan*）：页布局不可识别，ItemId 数组不可用，
//     posTag[1] = 元组在页内的**字节偏移**，仅用于定位，不能当 itemid 查询。
//
// 两种形态都保留原输出格式（"-- DELETED ctid=(p,n)"），避免破坏既有解析脚本；
// 扫描回退首次触发时打一条提示，避免现场把字节偏移误当 itemid。
func (ri *RowIter) buildRow(tup *HeapTuple, posTag [2]int) *Row {
	deleted := tup.IsDeleted()
	if !tup.IsLive() && !deleted {
		return nil
	}
	if !ri.IncludeDel && deleted {
		return nil
	}
	if ri.OnlyDeleted && !deleted {
		return nil
	}
	// 行内列数：金仓与 PG 均为 t_infomask2 低 11 位（PG 源码 HeapTupleHeaderGetNatts
	// = t_infomask2 & 0x07FF，实测 V8/V9/PG12-18 一致）。金仓 ALTER 残留幽灵 att 行
	// （attnum 超出数据行实际列数）时，行内列数 < catalog 列数，必须按行内列数截断，
	// 否则位图/值区错位导致丢行。
	natts := tup.NAttrs
	colLengths := buildColLengths(ri.TM)
	if natts > 0 && natts < len(colLengths) {
		colLengths = colLengths[:natts]
		tup.NAttrs = natts
	}
	nulls := tup.GetNulls()
	fields := datatype.ExtractFieldsDirect(tup.Raw, tup.THoff, nulls, colLengths)
	values := decodeFields(fields, ri.TM, ri.Toast)
	atomic.AddInt64(&ri.ProgressRows, 1) // v1.0.40 --progress
	return &Row{
		CTID:    strings.Join([]string{"(" + itoa(posTag[0]) + "," + itoa(posTag[1]) + ")"}, ""),
		Deleted: deleted,
		Values:  values,
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// ---------- 流式格式化输出（v1.0.30：worker 内 解析→转义→产出字符串 + 页序窗口转发） ----------
// 内存 O(窗口×并行度)，消除全量收集；输出行序=页序，与历史串行逐字节一致（md5 不变）

// fmtCtx：预计算的格式化上下文（只读，worker 共享安全）
type fmtMode int

const (
	fmtSQL fmtMode = iota
	fmtCSV
)

type fmtCtx struct {
	mode     fmtMode
	complete bool
	delim    string
	liveCols []ColumnDef
	outIdx   []int
	target   string
	verb     string
	colStr   string
	// overriding：v1.0.63（B-8）表内含 GENERATED ALWAYS AS IDENTITY 列时必须带
	// `OVERRIDING SYSTEM VALUE`，否则显式导入主键值直接报
	// "cannot insert a non-DEFAULT value into column"。CSV/\copy 路径无需（COPY 不受限）。
	overriding string
	batch      bool // v1.0.38：多值 INSERT 模式，formatRow 输出 VALUES 元组（不含 INSERT 头）
}

func buildFmtCtx(tm *TableMeta, mode fmtMode, completeInsert bool, fields []string, delimiter string) *fmtCtx {
	liveCols := make([]ColumnDef, 0)
	for _, c := range tm.Columns {
		if !c.Dropped {
			liveCols = append(liveCols, c)
		}
	}
	outIdx := make([]int, 0)
	if fields != nil {
		fset := map[string]bool{}
		for _, f := range fields {
			fset[f] = true
		}
		for i, c := range liveCols {
			if fset[c.Name] {
				outIdx = append(outIdx, i)
			}
		}
	} else {
		for i := range liveCols {
			outIdx = append(outIdx, i)
		}
	}
	c := &fmtCtx{mode: mode, complete: completeInsert, delim: delimiter, liveCols: liveCols, outIdx: outIdx}
	if mode == fmtSQL {
		c.verb = "INSERT INTO"
		c.target = QuoteName(tm.Schema) + "." + QuoteName(tm.RelName)
		if completeInsert {
			quoted := make([]string, len(outIdx))
			for i, idx := range outIdx {
				quoted[i] = QuoteName(liveCols[idx].Name)
			}
			c.colStr = "(" + strings.Join(quoted, ", ") + ")"
		}
		for _, idx := range outIdx {
			if liveCols[idx].Identity == "a" {
				c.overriding = " OVERRIDING SYSTEM VALUE"
				break
			}
		}
	}
	return c
}

func (c *fmtCtx) headerLine() string {
	if c == nil || c.mode != fmtCSV {
		return ""
	}
	names := make([]string, len(c.outIdx))
	for i, idx := range c.outIdx {
		names[i] = csvField(c.liveCols[idx].Name, c.delim)
	}
	return strings.Join(names, c.delim)
}

// formatRow：把一行格式化为输出字符串（SQL INSERT 或 CSV 行），逻辑与 v1.0.29 ToSQL/ToData 逐字节一致
func (c *fmtCtx) formatRow(row *Row) string {
	if c.mode == fmtCSV {
		parts := make([]string, len(c.outIdx))
		for i, idx := range c.outIdx {
			if row.Values[idx] == nil || isToastPlaceholder(row.Values[idx]) {
				parts[i] = "\\N"
			} else {
				v := *row.Values[idx]
				if datatype.MysqlBitOIDs[c.liveCols[idx].TypeOID] {
					v = datatype.BitBinToHex(v)
				}
				parts[i] = csvField(v, c.delim)
			}
		}
		return strings.Join(parts, c.delim)
	}
	vals := make([]string, len(c.outIdx))
	for i, idx := range c.outIdx {
		if row.Values[idx] != nil {
			vals[i] = *row.Values[idx]
		}
	}
	sqlVals := make([]string, len(vals))
	for i, v := range vals {
		if row.Values[c.outIdx[i]] == nil || isToastPlaceholder(row.Values[c.outIdx[i]]) {
			sqlVals[i] = "NULL"
		} else {
			sqlVals[i] = sqlQuote(v, &c.liveCols[c.outIdx[i]])
		}
	}
	// v1.0.38：多值 INSERT 批模式——live 行输出 VALUES 元组，deleted 行输出完整单条
	if c.batch {
		if row.Deleted {
			return "-- DELETED ctid=" + row.CTID + "\n" + c.verb + " " + c.target + " " + c.colStr + c.overriding + " VALUES (" + strings.Join(sqlVals, ", ") + ");"
		}
		return "(" + strings.Join(sqlVals, ", ") + ")"
	}
	stmt := c.verb + " " + c.target + " " + c.colStr + c.overriding + " VALUES (" + strings.Join(sqlVals, ", ") + ");"
	if row.Deleted {
		stmt = "-- DELETED ctid=" + row.CTID + "\n" + stmt
	}
	return stmt
}

// pageCount：主表完整页数（多段文件总和，整除）
// v1.0.56（R3）：与 segReadAt/扫描 worker 共享按实际文件大小推导的段边界表
func (ri *RowIter) PageCount() int {
	return BuildSegPageTable(ri.Path, ri.PageSize).Total
}

// ---------- 多段文件（>1GB 续段 .1/.2/...）支持（v1.0.30） ----------
// PG 单段文件字节上限恒为 1GB（RELSEG_SIZE = 0x40000000/BLCKSZ），
// 故每段页数随页大小变化：8K=131072 页、16K=65536 页、32K=32768 页。
// v1.0.49 修复：此前硬编码 131072（8K 页），16K/32K 页 + >1GB 表 + --parallel 时段映射错乱。
func relSegPagesOf(pageSize int) int {
	if pageSize <= 0 {
		return 131072
	}
	return 0x40000000 / pageSize
}

// SegFiles：主文件 + 续段文件列表（按目录项一次性枚举，含中间缺口检测）。
// v1.0.60 修复（P2-12）：旧实现遇第一个缺失段即 break —— 段文件中间缺失
// （取证拷贝不全/被裁剪）时后续段被静默忽略，导出静默截断且 exit 0。
// 现按目录项枚举全部 <base>.<N>，缺号即告警；结果按路径缓存（运行期目录不变，
// 而 SegFiles 被每个 worker 反复调用）。
var segFilesCache sync.Map // path → []string

func SegFiles(path string) []string {
	if v, ok := segFilesCache.Load(path); ok {
		return v.([]string)
	}
	files := buildSegFiles(path)
	segFilesCache.Store(path, files)
	return files
}

func buildSegFiles(path string) []string {
	segs := map[int]string{0: path}
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		delete(segs, 0)
	}
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	if ents, err := os.ReadDir(dir); err == nil {
		for _, e := range ents {
			n := e.Name()
			if n == base || !strings.HasPrefix(n, base+".") {
				continue
			}
			suf := n[len(base)+1:]
			idx, err := strconv.Atoi(suf) // 排除 _fsm/_vm/_init 等非数字后缀
			if err != nil || idx <= 0 {
				continue
			}
			segs[idx] = filepath.Join(dir, n)
		}
	}
	if len(segs) == 0 {
		return []string{path}
	}
	maxIdx := 0
	for k := range segs {
		if k > maxIdx {
			maxIdx = k
		}
	}
	for i := 0; i <= maxIdx; i++ {
		if _, ok := segs[i]; !ok {
			logx.Logf("警告: 段文件缺失 %s.%d（后续段 .%d 存在）——该表从此处起数据不可读，导出会少行，请确认数据目录完整性", path, i, maxIdx)
			break
		}
	}
	out := make([]string, 0, len(segs))
	for i := 0; i <= maxIdx; i++ {
		if p, ok := segs[i]; ok {
			out = append(out, p)
		}
	}
	return out
}

// segPos：全局页号 → (段索引, 段内页号)；段边界按实际页大小计算（v1.0.49）
// v1.0.56（R3）后仅作为 segPageTable 不可用时的兜底；主路径统一走按实际
// 文件大小推导的段边界表（见 segPageTable）。
func segPos(pageno, pageSize int) (int, int) {
	segPages := relSegPagesOf(pageSize)
	return pageno / segPages, pageno % segPages
}

// segPageTable：按【实际文件大小】累计的段边界表（v1.0.56 R3 修复）。
// 旧实现双套推导并存：pageCount 按各段实际文件大小求和，segPos 按均匀容量
// （RELSEG_SIZE/pageSize）映射全局页号。健康库两者一致（PG 段 N 写满才建
// 段 N+1），但段文件残缺/复制不完整/金仓变长段场景下两者错位——segPos 把
// 页号路由到错误的文件偏移，pageCount 却计入实际页数，出现错读或漏读。
// 统一为一张按实际大小累计的表，页号定位与页数统计共享同一推导。
// SegPageTable：多段文件（1GB 分段）的全局页号 → (段文件, 段内页号) 映射表。
type SegPageTable struct {
	Paths []string
	Base  []int // 每段起始全局页号（按实际页数累计）
	Pages []int // 每段实际页数
	Total int
}

func BuildSegPageTable(path string, pageSize int) *SegPageTable {
	return buildSegPageTableFromPaths(SegFiles(path), pageSize)
}

func buildSegPageTableFromPaths(paths []string, pageSize int) *SegPageTable {
	st := &SegPageTable{}
	if pageSize <= 0 {
		return st
	}
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			break
		}
		n := int(fi.Size() / int64(pageSize))
		if n <= 0 {
			break
		}
		st.Paths = append(st.Paths, p)
		st.Base = append(st.Base, st.Total)
		st.Pages = append(st.Pages, n)
		st.Total += n
	}
	return st
}

// locate：全局页号 → (段索引, 段内页号)；pageno 超界时返回最后一段
func (st *SegPageTable) Locate(pageno int) (int, int) {
	if st == nil || len(st.Base) == 0 {
		return 0, pageno
	}
	for i := len(st.Base) - 1; i >= 0; i-- {
		if pageno >= st.Base[i] {
			return i, pageno - st.Base[i]
		}
	}
	return 0, pageno
}

// probeRowValid：轻量判定行是否含有效值（NULL/空串/TOAST 缺失列之外任一列有值）
// 与 rowHasValid(decodeFields 后) 语义一致：NULL→无效；空串("")→无效；__TOAST_MISSING__→无效
func (ri *RowIter) probeRowValid(tup *HeapTuple) bool {
	natts := tup.NAttrs
	colLengths := buildColLengths(ri.TM)
	if natts > 0 && natts < len(colLengths) {
		colLengths = colLengths[:natts]
	}
	nulls := tup.GetNulls()
	raw := tup.Raw
	base := tup.THoff
	nraw := len(raw)
	pos := 0
	for i, item := range colLengths {
		align := datatype.LayoutAlign(item.AttLen, item.AttAlign)
		if i < len(nulls) && nulls[i] {
			continue
		}
		if item.IsVarlena {
			kind0, _, _, _ := datatype.VarlenaParse(raw, base+pos)
			if kind0 == "" || ((kind0 == datatype.VARLENA_4B || kind0 == datatype.VARLENA_4BC) && align > 1 && (pos&(align-1)) != 0) {
				if align > 1 {
					pos = (pos + align - 1) &^ (align - 1)
				}
			}
			offset := base + pos
			if offset >= nraw {
				return false
			}
			kind, total, _, _ := datatype.VarlenaParse(raw, offset)
			switch kind {
			case datatype.VARLENA_EXTERNAL:
				if offset+datatype.KB_EXTERNAL_SIZE <= nraw {
					ext := datatype.ParseExternalPointer(raw, offset)
					if ext != nil && ri.Toast != nil {
						if _, ok := ri.Toast.PosIndex[ext.ValueID]; !ok {
							pos += datatype.KB_EXTERNAL_SIZE
							continue // TOAST 缺失 → 无效列
						}
					}
					return true
				}
				return false
			case datatype.VARLENA_1B:
				if offset+total > nraw {
					return false
				}
				pos += total
				if total > 1 {
					return true
				}
			case datatype.VARLENA_4B, datatype.VARLENA_4BC:
				if offset+total > nraw {
					return false
				}
				pos += total
				if total > 4 {
					return true
				}
			default:
				return false
			}
		} else {
			if align > 1 {
				pos = (pos + align - 1) &^ (align - 1)
			}
			offset := base + pos
			if offset+item.AttLen > nraw {
				return false
			}
			pos += item.AttLen
			return true // 定长非 NULL 即有值（decode 后必非空串）
		}
	}
	return false
}

// probeStandard：轻量预扫判定（第1遍），不构建行、不格式化（多段文件遍历）
func (ri *RowIter) probeStandard() (foundStandard, hasValid bool) {
	npages := ri.PageCount()
	if npages == 0 {
		return false, false
	}
	buf := make([]byte, ri.PageSize)
	count := 0
	base := 0
	for _, seg := range SegFiles(ri.Path) {
		f, err := os.Open(seg)
		if err != nil {
			continue
		}
		pageno := base
		for {
			n, err := readFullPage(f, buf)
			if err != nil {
				break
			}
			// v1.0.39 TDE：串行路径读页后解密
			if ri.Decryptor != nil {
				if _, derr := ri.Decryptor.DecryptPage(buf[:n]); derr != nil {
					// v1.0.60 修复（P2-11）：解密失败不再 break 丢剩余页，跳过本页继续
					//（预扫判定阶段，丢弃会导致标准布局误判/有效行误判，最终回退错路径）
					pageno++
					continue
				}
			}
			items, _ := ri.IterTuples(pageno, buf)
			if len(items) > 0 {
				foundStandard = true
			}
			for _, it := range items {
				tup := it[1].(*HeapTuple)
				if !hasValid && ri.probeRowValid(tup) {
					hasValid = true
				}
				count++
				if ri.Limit > 0 && count >= ri.Limit {
					f.Close()
					return foundStandard, hasValid
				}
			}
			// v1.0.62（P3）：两个判定都是"存在性"，一旦同时成立就再也不会翻转，
			// 立刻返回即可。旧实现固定扫完整个数据文件（大表等于把 IO 翻倍，
			// 且这段是"预扫"——真正输出还要再读一遍）。常见库首屏页就满足，
			// 命中时预扫 IO 从 O(表大小) 降到几页。
			if foundStandard && hasValid {
				f.Close()
				return true, true
			}
			atomic.AddInt64(&ri.ProbePages, 1) // v1.0.62：预扫进度（供 --progress 显示）
			pageno++
		}
		f.Close()
		if fi, err := os.Stat(seg); err == nil {
			base += int(fi.Size() / int64(ri.PageSize))
		}
	}
	return foundStandard, hasValid
}

// pageLinesResult：worker 产出一页的格式化行（保序归并单元）
type pageLinesResult struct {
	pageno int
	lines  []string
}

// sendPageResult：向 results 发送页结果，阻塞期间轮询 stop；stop 置位立即返回 false，
// 避免主 goroutine 提前 return（--limit 达标）后 worker 永久阻塞在裸发送上（v1.0.35 修复）。
func sendPageResult(results chan pageLinesResult, pr pageLinesResult, stop *int32) bool {
	for {
		select {
		case results <- pr:
			return true
		default:
			if atomic.LoadInt32(stop) == 1 {
				return false
			}
			runtime.Gosched()
		}
	}
}

// sendJob：向 jobs 发送页号，阻塞期间轮询 stop；stop 置位立即返回 false（v1.0.35 修复：
// 消除生产者"发前检查 stop"竞态窗口导致的永久泄漏）。
func sendJob(jobs chan int, pageno int, stop *int32) bool {
	for {
		select {
		case jobs <- pageno:
			return true
		default:
			if atomic.LoadInt32(stop) == 1 {
				return false
			}
			runtime.Gosched()
		}
	}
}

// streamStdSerial：标准 ItemId 模式流式输出（串行，Parallel<=1；多段文件遍历）
func (ri *RowIter) streamStdSerial(c *fmtCtx, emit func(string) bool) {
	npages := ri.PageCount()
	if npages == 0 {
		return
	}
	buf := make([]byte, ri.PageSize)
	count := 0
	base := 0
	for _, seg := range SegFiles(ri.Path) {
		f, err := os.Open(seg)
		if err != nil {
			continue
		}
		pageno := base
		for {
			n, err := readFullPage(f, buf)
			if err != nil {
				break
			}
			// v1.0.39 TDE：串行路径读页后解密
			if ri.Decryptor != nil {
				if _, derr := ri.Decryptor.DecryptPage(buf[:n]); derr != nil {
					// v1.0.60 修复（P2-11）：解密失败此前 break —— 该段剩余页全部
					// 静默丢弃（导出"成功"、行数凭空变少且无提示）。改记坏页后
					// 继续下一页，与坏页语义一致（bad_pages 进报告可追查）。
					ri.addBadPage(pageno)
					pageno++
					continue
				}
			}
			atomic.AddInt64(&ri.ProgressDone, 1) // v1.0.40 --progress
			items, layOK := ri.IterTuples(pageno, buf)
			if !layOK && !ZeroPage(buf) { // v1.0.55 B1：全零页与合法空页不算坏页
				ri.addBadPage(pageno)
			}
			for _, it := range items {
				tup := it[1].(*HeapTuple)
				idx := it[0].(int)
				row := ri.buildRow(tup, [2]int{pageno, idx})
				if row == nil {
					continue
				}
				var line string
				if c != nil {
					line = c.formatRow(row)
				}
				if !emit(line) {
					f.Close()
					return
				}
				count++
				if ri.Limit > 0 && count >= ri.Limit {
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

// ctidOffsetNotice：扫描回退路径的 CTID 语义提示（进程内只打一次）
var ctidOffsetNotice sync.Once

func noticeScanCTID() {
	ctidOffsetNotice.Do(func() {
		logx.Logf("提示: 部分数据页布局不可识别，已按元组头线性扫描恢复；该路径输出的 CTID 第二项是页内字节偏移（不是 line pointer 下标），仅用于定位，不能直接用于 WHERE ctid 查询")
	})
}

// streamScanSerial：数据区扫描回退流式输出（串行；多段文件遍历）
func (ri *RowIter) streamScanSerial(c *fmtCtx, emit func(string) bool) {
	npages := ri.PageCount()
	if npages == 0 {
		return
	}
	colLengths := buildColLengths(ri.TM)
	nExpected := len(ri.TM.Columns)
	buf := make([]byte, ri.PageSize)
	count := 0
	base := 0
	for _, seg := range SegFiles(ri.Path) {
		f, err := os.Open(seg)
		if err != nil {
			continue
		}
		pageno := base
		for {
			n, err := readFullPage(f, buf)
			if err != nil {
				break
			}
			atomic.AddInt64(&ri.ProgressDone, 1) // v1.0.40 --progress
			// v1.0.60 修复（P2-11）：扫描回退路径此前完全没有解密分支——TDE 库
			// 一旦落到扫描回退，密文被当明文解析（垃圾行 / 静默 0 行）。补上
			// 与 streamStdSerial 同语义的解密步骤，失败记坏页继续。
			if ri.Decryptor != nil {
				if _, derr := ri.Decryptor.DecryptPage(buf[:n]); derr != nil {
					ri.addBadPage(pageno)
					pageno++
					continue
				}
			}
			lay := TryStandardLayout(buf, ri.PageSize)
			if lay == nil {
				lay = TryAutoLayout(buf, ri.PageSize)
			}
			if lay == nil {
				if !ZeroPage(buf) { // v1.0.55 B1：全零页不算坏页（但仍是"无布局"页，跳过元组扫描）
					ri.addBadPage(pageno)
				}
				pageno++
				continue
			}
			pos := int(lay.Upper)
			special := int(lay.Special)
			for pos+HEAP_TUPLE_HEADER_SIZE <= special {
				td := buf[pos:special]
				tHoff := int(td[22])
				infomask := datatype.U16(td, 20)
				nattrs := int(datatype.U16(td, 18) & datatype.HEAP_NATTS_MASK)
				if tHoff < HEAP_TUPLE_HEADER_SIZE || tHoff > 256 {
					pos += 4
					continue
				}
				if nattrs == 0 || nattrs > 1600 {
					pos += 4
					continue
				}
				if nExpected > 0 && nattrs != nExpected {
					pos += 4
					continue
				}
				// v1.0.63 修复（B-5）：xid 是 32 位循环的，> 0x7FFFFFFF 完全合法
				// （实例累计事务超 2^31 后大量行都是"负数 xid"）。旧判据会把这些
				// 元组整片当垃圾丢掉（整页 0 行且无告警）。这里只排除 0（InvalidTransactionId），
				// 其余交给 infomask / headerConsistent 的自洽性校验。
				txmin := datatype.U32(td, 0)
				if txmin == 0 {
					pos += 4
					continue
				}
				if infomask&0xFF00 == 0 {
					pos += 4
					continue
				}
				tup := ParseTuple(td, ri.PGVersion)
				if tup == nil || !tup.headerConsistent() {
					pos += 4
					continue
				}
				noticeScanCTID()
				row := ri.buildRow(tup, [2]int{pageno, pos})
				if row != nil {
					var line string
					if c != nil {
						line = c.formatRow(row)
					}
					if !emit(line) {
						f.Close()
						return
					}
					count++
					if ri.Limit > 0 && count >= ri.Limit {
						f.Close()
						return
					}
				}
				actualSize := calculateTupleSize(tup, colLengths)
				nextPos := (actualSize + 7) &^ 7
				if nextPos < 8 {
					nextPos = 8
				}
				pos += nextPos
			}
			pageno++
		}
		f.Close()
		if fi, err := os.Stat(seg); err == nil {
			base += int(fi.Size() / int64(ri.PageSize))
		}
	}
}

// streamStdParallel：标准 ItemId 模式流式输出（worker 池：worker 内解析+转义，页序窗口保序转发；多段文件支持）
func (ri *RowIter) streamStdParallel(c *fmtCtx, emit func(string) bool) {
	npages := ri.PageCount()
	if npages == 0 {
		return
	}
	workers := ri.Parallel
	if workers < 1 {
		workers = 1
	}
	if workers > 64 {
		workers = 64
	}
	jobs := make(chan int, workers*2)
	results := make(chan pageLinesResult, workers*2)
	var wg sync.WaitGroup
	var stop int32
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			segPaths := SegFiles(ri.Path)
			segTab := buildSegPageTableFromPaths(segPaths, ri.PageSize) // v1.0.56 R3
			segFs := make([]*os.File, len(segPaths))
			defer func() {
				for _, sf := range segFs {
					if sf != nil {
						sf.Close()
					}
				}
			}()
			for i, p := range segPaths {
				f, err := os.Open(p)
				if err == nil {
					segFs[i] = f
				}
			}
			buf := make([]byte, ri.PageSize)
			for pageno := range jobs {
				if atomic.LoadInt32(&stop) == 1 {
					return
				}
				seg, off := segTab.Locate(pageno)
				if seg >= len(segFs) || segFs[seg] == nil {
					pr := pageLinesResult{pageno: pageno}
					if !sendPageResult(results, pr, &stop) {
						return
					}
					continue
				}
				if _, err := segFs[seg].ReadAt(buf, int64(off)*int64(ri.PageSize)); err != nil {
					pr := pageLinesResult{pageno: pageno}
					if !sendPageResult(results, pr, &stop) {
						return
					}
					continue
				}
				// v1.0.39 TDE：页读取后、行解析前解密
				if ri.Decryptor != nil {
					if _, derr := ri.Decryptor.DecryptPage(buf); derr != nil {
						pr := pageLinesResult{pageno: pageno}
						if !sendPageResult(results, pr, &stop) {
							return
						}
						continue
					}
				}
				atomic.AddInt64(&ri.ProgressDone, 1) // v1.0.40 --progress（v1.0.41 补：标准并行路径缺埋点致页进度恒 0）
				items, layOK := ri.IterTuples(pageno, buf)
				if !layOK && !ZeroPage(buf) { // v1.0.55 B1：全零页与合法空页不算坏页
					ri.addBadPage(pageno)
				}
				pr := pageLinesResult{pageno: pageno}
				for _, it := range items {
					tup := it[1].(*HeapTuple)
					idx := it[0].(int)
					row := ri.buildRow(tup, [2]int{pageno, idx})
					if row == nil {
						continue
					}
					var line string
					if c != nil {
						line = c.formatRow(row)
					}
					pr.lines = append(pr.lines, line)
				}
				if !sendPageResult(results, pr, &stop) {
					return
				}
			}
		}()
	}
	go func() {
		for pageno := 0; pageno < npages; pageno++ {
			if !sendJob(jobs, pageno, &stop) {
				break
			}
		}
		close(jobs)
	}()
	go func() {
		wg.Wait()
		close(results)
	}()
	// 页序滑动窗口归并 → 直接转发（不收集全量）
	// v1.0.56（R2）有界性保障：jobs/results 通道背压下 pending 上限 ≈ 5×workers
	// 页（jobs 2w + worker 持有 1w + results 2w），理论有界。此处加超限告警
	//（once）：超限说明流水线背压被破坏（如有人去掉通道缓冲），提示排查；
	// 不做丢页降级——保序正确性优先。
	pending := make(map[int][]string)
	maxPending := ri.Parallel*5 + 64
	var pendingWarned int32
	next := 0
	count := 0
	for pr := range results {
		if atomic.LoadInt32(&stop) == 1 {
			continue
		}
		pending[pr.pageno] = pr.lines
		if len(pending) > maxPending && atomic.CompareAndSwapInt32(&pendingWarned, 0, 1) {
			logx.Logf("警告: 页序归并窗口异常膨胀(%d 页，理论上限约 %d 页)，流水线背压可能失效，内存占用会随页数增长", len(pending), maxPending)
		}
		for {
			lines, ok := pending[next]
			if !ok {
				break
			}
			for _, line := range lines {
				if !emit(line) {
					atomic.StoreInt32(&stop, 1)
					return
				}
				count++
				if ri.Limit > 0 && count >= ri.Limit {
					atomic.StoreInt32(&stop, 1)
					return
				}
			}
			delete(pending, next)
			next++
		}
	}
}

// streamScanParallel：数据区扫描回退流式输出（worker 池 + 页序窗口转发；多段文件支持）
func (ri *RowIter) streamScanParallel(c *fmtCtx, emit func(string) bool) {
	npages := ri.PageCount()
	if npages == 0 {
		return
	}
	colLengths := buildColLengths(ri.TM)
	nExpected := len(ri.TM.Columns)
	workers := ri.Parallel
	if workers < 1 {
		workers = 1
	}
	if workers > 64 {
		workers = 64
	}
	jobs := make(chan int, workers*2)
	results := make(chan pageLinesResult, workers*2)
	var wg sync.WaitGroup
	var stop int32
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			segPaths := SegFiles(ri.Path)
			segTab := buildSegPageTableFromPaths(segPaths, ri.PageSize) // v1.0.56 R3
			segFs := make([]*os.File, len(segPaths))
			defer func() {
				for _, sf := range segFs {
					if sf != nil {
						sf.Close()
					}
				}
			}()
			for i, p := range segPaths {
				f, err := os.Open(p)
				if err == nil {
					segFs[i] = f
				}
			}
			buf := make([]byte, ri.PageSize)
			for pageno := range jobs {
				if atomic.LoadInt32(&stop) == 1 {
					return
				}
				seg, off := segTab.Locate(pageno)
				if seg >= len(segFs) || segFs[seg] == nil {
					pr := pageLinesResult{pageno: pageno}
					if !sendPageResult(results, pr, &stop) {
						return
					}
					continue
				}
				if _, err := segFs[seg].ReadAt(buf, int64(off)*int64(ri.PageSize)); err != nil {
					pr := pageLinesResult{pageno: pageno}
					if !sendPageResult(results, pr, &stop) {
						return
					}
					continue
				}
				// v1.0.39 TDE：串行路径同样解密
				if ri.Decryptor != nil {
					if _, derr := ri.Decryptor.DecryptPage(buf); derr != nil {
						pr := pageLinesResult{pageno: pageno}
						if !sendPageResult(results, pr, &stop) {
							return
						}
						continue
					}
				}
				atomic.AddInt64(&ri.ProgressDone, 1) // v1.0.40 --progress
				lay := TryStandardLayout(buf, ri.PageSize)
				if lay == nil {
					lay = TryAutoLayout(buf, ri.PageSize)
				}
				if lay == nil && !ZeroPage(buf) { // v1.0.55 B1：全零页不算坏页
					ri.addBadPage(pageno)
				}
				pr := pageLinesResult{pageno: pageno}
				if lay != nil {
					pos := int(lay.Upper)
					special := int(lay.Special)
					for pos+HEAP_TUPLE_HEADER_SIZE <= special {
						td := buf[pos:special]
						tHoff := int(td[22])
						infomask := datatype.U16(td, 20)
						nattrs := int(datatype.U16(td, 18) & datatype.HEAP_NATTS_MASK)
						if tHoff < HEAP_TUPLE_HEADER_SIZE || tHoff > 256 {
							pos += 4
							continue
						}
						if nattrs == 0 || nattrs > 1600 {
							pos += 4
							continue
						}
						if nExpected > 0 && nattrs != nExpected {
							pos += 4
							continue
						}
						// v1.0.63（B-5）：同上，xid 循环后 >0x7FFFFFFF 合法
						txmin := datatype.U32(td, 0)
						if txmin == 0 {
							pos += 4
							continue
						}
						if infomask&0xFF00 == 0 {
							pos += 4
							continue
						}
						tup := ParseTuple(td, ri.PGVersion)
						if tup == nil || !tup.headerConsistent() {
							pos += 4
							continue
						}
						noticeScanCTID()
						row := ri.buildRow(tup, [2]int{pageno, pos})
						if row != nil {
							var line string
							if c != nil {
								line = c.formatRow(row)
							}
							pr.lines = append(pr.lines, line)
						}
						actualSize := calculateTupleSize(tup, colLengths)
						nextPos := (actualSize + 7) &^ 7
						if nextPos < 8 {
							nextPos = 8
						}
						pos += nextPos
					}
				}
				// 空页也发送 pr（lines 为空），保证页序窗口 next 指针正常推进
				if !sendPageResult(results, pr, &stop) {
					return
				}
			}
		}()
	}
	go func() {
		for pageno := 0; pageno < npages; pageno++ {
			if !sendJob(jobs, pageno, &stop) {
				break
			}
		}
		close(jobs)
	}()
	go func() {
		wg.Wait()
		close(results)
	}()
	// v1.0.56（R2）同上：归并窗口有界性告警
	pending := make(map[int][]string)
	maxPending := ri.Parallel*5 + 64
	var pendingWarned int32
	next := 0
	count := 0
	for pr := range results {
		if atomic.LoadInt32(&stop) == 1 {
			continue
		}
		pending[pr.pageno] = pr.lines
		if len(pending) > maxPending && atomic.CompareAndSwapInt32(&pendingWarned, 0, 1) {
			logx.Logf("警告: 页序归并窗口异常膨胀(%d 页，理论上限约 %d 页)，流水线背压可能失效，内存占用会随页数增长", len(pending), maxPending)
		}
		for {
			lines, ok := pending[next]
			if !ok {
				break
			}
			for _, line := range lines {
				if !emit(line) {
					atomic.StoreInt32(&stop, 1)
					return
				}
				count++
				if ri.Limit > 0 && count >= ri.Limit {
					atomic.StoreInt32(&stop, 1)
					return
				}
			}
			delete(pending, next)
			next++
		}
	}
}

// StreamFormatted：主输出通道（预扫判定 → 标准流式 / 扫描回退流式）
func (ri *RowIter) StreamFormatted(c *fmtCtx) chan string {
	out := make(chan string, 256)
	go func() {
		defer close(out)
		emit := func(line string) bool {
			out <- line
			return true
		}
		found, hasValid := ri.probeStandard()
		if found && hasValid {
			if ri.Parallel > 1 {
				ri.streamStdParallel(c, emit)
			} else {
				ri.streamStdSerial(c, emit)
			}
			return
		}
		if ri.Parallel > 1 {
			ri.streamScanParallel(c, emit)
		} else {
			ri.streamScanSerial(c, emit)
		}
	}()
	return out
}

// CountRows：流式行数统计（预扫判定 + 计数，不格式化）
func (ri *RowIter) CountRows() int {
	// v1.0.55 B3：--count 忽略 --limit。此前 limit 同时作用于导出与计数两条路径，
	// `--count --limit 10` 会把「前 10 行」当总行数输出（实测 5000 行表输出「总行数: 10」），
	// 行数统计语义上必须是全表，故计数前临时清零、结束恢复。
	saved := ri.Limit
	ri.Limit = 0
	defer func() { ri.Limit = saved }()
	n := 0
	emit := func(string) bool {
		n++
		return true
	}
	found, hasValid := ri.probeStandard()
	if found && hasValid {
		if ri.Parallel > 1 {
			ri.streamStdParallel(nil, emit)
		} else {
			ri.streamStdSerial(nil, emit)
		}
		return n
	}
	if ri.Parallel > 1 {
		ri.streamScanParallel(nil, emit)
	} else {
		ri.streamScanSerial(nil, emit)
	}
	return n
}

// ---------- SQL/CSV 输出 ----------
// v1.0.47：移除 16(bool)/790(money)——两者输出格式对齐 PG 后不再是合法裸 SQL 字面量
// （bool→"t"/"f"、money→"$12,345.67"），须按字符串字面量输出（'t'、'$12,345.67' 回灌合法）。
var noQuoteOIDs = map[uint32]bool{
	20: true, 21: true, 23: true, 26: true, 700: true, 701: true, 1700: true,
}

func sqlQuote(value string, col *ColumnDef) string {
	if col != nil && datatype.MysqlBitOIDs[col.TypeOID] {
		// 金仓 mysql BIT：SQL 导入必须 B'...' 位字面量（普通字符串走 varchar_bit cast 会失败）
		return "B'" + value + "'"
	}
	if col != nil && datatype.MysqlBinOIDs[col.TypeOID] && strings.HasPrefix(value, "0x") {
		// v1.0.47：CSV 解码对齐金仓 binary_out 后值为 "0x00FF…"（大写 hex）。
		// 这两个类型（oid 3383/3384）的 typinput=binaryin 没有 hex 文本格式，
		// 直接把 '0x…' 当字符串喂进去会被按【字面量字节】存入；
		// 也没有 "0x" 输入格式（实测 binary'0x00ff' 存成 ASCII）。
		// 转 \x 小写 hex 喂 bytea（hex 解析），再由 bytea→binary assignment cast 还原字节。
		hexPart := strings.ToLower(strings.TrimPrefix(value, "0x"))
		if len(hexPart)%2 != 0 {
			hexPart = "0" + hexPart
		}
		return "'" + "\\x" + hexPart + "'::bytea"
	}
	if col != nil && datatype.MysqlBinOIDs[col.TypeOID] && strings.HasPrefix(value, "\\x") {
		// v1.0.46 修复（金仓 mysql binary/varbinary 回灌被污染）：
		// 这两个类型（oid 3383/3384）的 typinput=binaryin 没有 hex 文本格式，
		// 直接把 '\x00ff…' 当字符串喂进去会被按【字面量字节】存入
		// （实测存成 0x5C7830306666… = '\','x','0','0',… 的 ASCII，并按 binary(n) 截断）。
		// 加 ::bytea 让 bytea 先按 hex 解析，再由 bytea→binary 的 assignment cast 还原字节。
		return datatype.SQLStringLiteral(value) + "::bytea"
	}
	if col != nil && noQuoteOIDs[col.TypeOID] {
		if value == "NaN" || value == "Infinity" || value == "-Infinity" {
			return datatype.SQLStringLiteral(value)
		}
		return value
	}
	return datatype.SQLStringLiteral(value)
}

// QuoteName：标识符加引号。v1.0.48 修复（P1-F2）：原实现直接 `"\"" + name + "\""`，
// 标识符内部的双引号未转义，`aud"it` 这种名字会拼出 `"aud"it"` —— 语法非法。
// 统一委托给 schema_ddl.go 的 quoteIdent（`"` → `""`），消除两套不一致实现。
func QuoteName(name string) string { return util.QuoteIdent(name) }

func csvField(raw, delimiter string) string {
	needsQuote := strings.Contains(raw, delimiter) || strings.Contains(raw, "\n") ||
		strings.Contains(raw, "\r") || strings.Contains(raw, "\"") || raw == "\\N"
	if needsQuote {
		return "\"" + strings.ReplaceAll(raw, "\"", "\"\"") + "\""
	}
	return raw
}

// ToSQL 生成 INSERT 语句（v1.0.30：worker 内转义，流式输出）
func ToSQL(ri *RowIter, completeInsert bool, fields []string) chan string {
	c := buildFmtCtx(ri.TM, fmtSQL, completeInsert, fields, ",")
	return ri.StreamFormatted(c)
}

// ToSQLBatch 多值 INSERT（v1.0.38）：每 batchN 行合并一条 INSERT ... VALUES (...),(...)
// live 行攒批输出；deleted 行单独输出完整单条（含 -- DELETED 注释，不破坏多值语法）。
// 与 --parallel 正交（各 worker 页序保序转发，批在 emit 层组装，顺序与单条一致）。
// rowCount 非空时累计实际数据行数（报告用；多值合并后语句数≠行数）。
func ToSQLBatch(ri *RowIter, completeInsert bool, fields []string, batchN int, rowCount *int64) chan string {
	c := buildFmtCtx(ri.TM, fmtSQL, completeInsert, fields, ",")
	c.batch = true
	out := make(chan string, 16)
	go func() {
		defer close(out)
		var pending []string
		flush := func() {
			if len(pending) == 0 {
				return
			}
			out <- c.verb + " " + c.target + " " + c.colStr + c.overriding + " VALUES " + strings.Join(pending, ", ") + ";"
			pending = pending[:0]
		}
		emit := func(line string) bool {
			if line == "" {
				return true
			}
			if rowCount != nil {
				atomic.AddInt64(rowCount, 1)
			}
			if strings.HasPrefix(line, "-- DELETED") {
				flush()
				out <- line
				return true
			}
			pending = append(pending, line)
			if len(pending) >= batchN {
				flush()
			}
			return true
		}
		found, hasValid := ri.probeStandard()
		if found && hasValid {
			if ri.Parallel > 1 {
				ri.streamStdParallel(c, emit)
			} else {
				ri.streamStdSerial(c, emit)
			}
		} else {
			if ri.Parallel > 1 {
				ri.streamScanParallel(c, emit)
			} else {
				ri.streamScanSerial(c, emit)
			}
		}
		flush()
	}()
	return out
}

// ToData 生成 COPY CSV 格式数据行（v1.0.30：worker 内转义，流式输出）
func ToData(ri *RowIter, delimiter string, fields []string, header bool) chan string {
	c := buildFmtCtx(ri.TM, fmtCSV, false, fields, delimiter)
	out := make(chan string, 256)
	go func() {
		defer close(out)
		if header {
			out <- c.headerLine()
		}
		for line := range ri.StreamFormatted(c) {
			out <- line
		}
	}()
	return out
}

// ---- 兼容别名 ----
// v1.0.62（P3）：删除 extractFieldsDirect / layoutAlign 两个未被引用的
// 函数变量别名（v1.0.57 目录重构遗留的死代码，调用点已统一走 datatype.*）；
// ColLen 类型别名保留——它是对外契约（其他包可能按 engine.ColLen 书写）。
type ColLen = datatype.ColLen
