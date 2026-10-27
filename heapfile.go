// pg2sql heapfile 层：堆文件页遍历、元组提取、字段提取、TOAST 重组、行/SQL/CSV 生成
// 对应 Python 版 pg2sql/{page.py, tuple.py, heapfile.py, toast.py}，语义等价翻译
// Author: raysuen
package main

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// pageDecryptor：TDE 页解密器接口（v1.0.39）。
// 免费版编译（无 -tags tde）时不包含 tde 包，用接口解耦；TDE 版由 *tde.Decryptor 实现。
type pageDecryptor interface {
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
	KB_EXTERNAL_SIZE = 18

	HEAP_TUPLE_HEADER_SIZE = 23
	HEAP_HASNULL           = 0x0001
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
	HEAP_NATTS_MASK        = 0x07FF
)

var knownPageSizes = []uint16{8192, 16384, 32768}

// ---------- 页大小探测 ----------
func detectPageSize(raw []byte) int {
	if len(raw) < 130 {
		return 0
	}
	for off := 8; off < 128; off += 2 {
		psv := u16(raw, off)
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
		lower := u16(raw, off-6)
		upper := u16(raw, off-4)
		special := u16(raw, off-2)
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

func tryStandardLayout(raw []byte, pageSize int) *PageLayout {
	if len(raw) < 24 {
		return nil
	}
	psv := u16(raw, 18)
	if psv&0xFF != PG_PAGE_VERSION || int(psv&0xFF00) != pageSize {
		return nil
	}
	lower, upper, special := u16(raw, 12), u16(raw, 14), u16(raw, 16)
	if !(24 <= int(lower) && int(lower) <= int(upper) && int(upper) <= int(special) && int(special) <= pageSize) {
		return nil
	}
	return &PageLayout{18, 24, pageSize, psv & 0xFF, lower, upper, special}
}

func tryAutoLayout(raw []byte, pageSize int) *PageLayout {
	if len(raw) < 8 {
		return nil // v1.0.44：坏页/短 buf 防御，避免越界 panic
	}
	for off := 8; off < 64; off += 2 {
		psv := u16(raw, off)
		version := psv & 0xFF
		if version != PG_PAGE_VERSION && version != KB_PAGE_VERSION {
			continue
		}
		if int(psv&0xFF00) != pageSize {
			continue
		}
		lower, upper, special := u16(raw, off-6), u16(raw, off-4), u16(raw, off-2)
		headerEnd := (off + 2 + 3) &^ 3
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

func parseTuple(raw []byte, pgVersion int) *HeapTuple {
	if len(raw) < HEAP_TUPLE_HEADER_SIZE {
		return nil
	}
	infomask2 := u16(raw, 18)
	infomask := u16(raw, 20)
	t := &HeapTuple{
		Raw:       raw,
		TXmin:     u32(raw, 0),
		TXmax:     u32(raw, 4),
		TInfomask: infomask,
		THoff:     int(raw[22]),
		NAttrs:    int(infomask2 & HEAP_NATTS_MASK),
		PgVersion: pgVersion,
	}
	return t
}

func (t *HeapTuple) headerConsistent() bool {
	if t.THoff < 24 || t.THoff > 256 {
		return false
	}
	bitmapLen := 0
	if t.TInfomask&HEAP_HASNULL != 0 {
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

func (t *HeapTuple) isDeleted() bool {
	if t.isInsertAborted() {
		return false
	}
	if t.TInfomask&HEAP_XMAX_INVALID != 0 {
		return false
	}
	if t.TInfomask&HEAP_XMAX_LOCK_ONLY != 0 {
		return false
	}
	if t.TInfomask&HEAP_XMAX_IS_MULTI != 0 {
		return false
	}
	if t.TInfomask&HEAP_XMAX_COMMITTED != 0 {
		return true
	}
	return t.TXmax != 0
}

func (t *HeapTuple) isLive() bool {
	if t.isInsertAborted() {
		return false
	}
	if t.isDeleted() {
		return false
	}
	return true
}

// getNulls：PG12+ bit 置 1 = 非空，清 0 = NULL；PG<=11 相反
func (t *HeapTuple) getNulls() []bool {
	n := t.NAttrs
	res := make([]bool, n)
	if t.TInfomask&HEAP_HASNULL == 0 {
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
	TXmin        uint32
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
	Columns         []ColumnDef
	RoleMap         map[uint32]string
	TypeNames       map[uint32]string
	HasToast        bool
	PrimaryKey      []string
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
}

func (tm *TableMeta) FullName() string { return tm.Schema + "." + tm.RelName }

// 列长度信息
type ColLen struct {
	AttLen    int
	IsVarlena bool
	AttAlign  string
}

// 与 Python types.VARLENA_TYPES 严格对齐：仅"可外联的 varlena 类型"。
// 注意：数组/几何/网络等 attlen==-1 的类型无需在此列出（attlen 判定已覆盖）；
// 定长类型（uuid/money/macaddr/point 等）绝不能误标 varlena，否则提取错位。
var varlenaTypes = map[uint32]bool{
	25: true, 1043: true, 1042: true, 17: true, 114: true, 3802: true,
	1700: true, 1560: true, 1562: true, 705: true, 142: true, 32: true,
	3361: true, 3402: true, 5017: true,
}

func layoutAlign(attlen int, attalign string) int {
	if attalign != "" {
		switch attalign {
		case "c":
			return 1
		case "s":
			return 2
		case "i":
			return 4
		case "d":
			return 8
		}
	}
	if attlen == -1 {
		return 4
	}
	if attlen >= 8 {
		return 8
	}
	if attlen >= 4 {
		return 4
	}
	if attlen >= 2 {
		return 2
	}
	return 1
}

// maxRowNatts：扫描数据文件所有行头，返回最大行内列数（金仓低位/PG 高位）。
// 用于幽灵列截断：金仓 ALTER 残留 att 行（attnum 超出数据行实际列数）时，
// 行内列数 < catalog 列数，按行内最大列数截断列集，避免位图/值区错位丢行。
func maxRowNatts(path string, pageSize int, isKB bool) int {
	ps := pageSize
	if ps == 0 {
		ps = probePageSize(path)
	}
	if ps == 0 {
		return 0
	}
	buf := make([]byte, ps)
	maxn := 0
	for _, seg := range segFiles(path) {
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
			lay := tryStandardLayout(buf, ps)
			if lay == nil {
				lay = tryAutoLayout(buf, ps)
			}
			if lay == nil {
				continue
			}
			lower := int(lay.Lower)
			itemEnd := int(lay.HeaderEnd)
			for i := 0; itemEnd+4*i+4 <= lower; i++ {
				rawID := uint32(u16(buf, itemEnd+4*i)) | uint32(u16(buf, itemEnd+4*i+2))<<16
				o := int(rawID & 0x7FFF)
				flags := (rawID >> 15) & 0x03
				ln := int((rawID >> 17) & 0x7FFF)
				if flags != 1 || o >= ps || ln > ps || o+ln > ps || ln < HEAP_TUPLE_HEADER_SIZE {
					continue
				}
				td := buf[o : o+ln]
				i2 := u16(td, 18)
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

func buildColLengths(tm *TableMeta) []ColLen {
	res := make([]ColLen, 0, len(tm.Columns))
	for _, col := range tm.Columns {
		isVar := col.AttLen == -1 || varlenaTypes[col.TypeOID]
		attlen := col.AttLen
		if attlen < 0 {
			attlen = 0
		}
		res = append(res, ColLen{attlen, isVar, col.AttAlign})
	}
	return res
}

// ---------- 字段提取（PG 磁盘格式顺序提取） ----------
func extractFieldsDirect(raw []byte, tHoff int, nulls []bool, colLengths []ColLen) []*[]byte {
	pos := 0
	fields := make([]*[]byte, len(colLengths))
	nraw := len(raw)
	for i, item := range colLengths {
		align := layoutAlign(item.AttLen, item.AttAlign)
		if i < len(nulls) && nulls[i] {
			fields[i] = nil
			continue
		}
		if item.IsVarlena {
			// PG heap_fill_tuple 布局规则：short varlena（1B 头）与外部指针不对齐，
			// 仅 4B 头 varlena（含压缩 4BC）按 attalign 对齐。先探测原始位置（下一字段起始），
			// 非法（前为 0x00 padding）或未对齐的 4B/4BC 头时再按 attalign 对齐取。
			kind0, _, _, _ := varlenaParse(raw, tHoff+pos)
			if kind0 == "" || ((kind0 == VARLENA_4B || kind0 == VARLENA_4BC) && align > 1 && (pos&(align-1)) != 0) {
				if align > 1 {
					pos = (pos + align - 1) &^ (align - 1)
				}
			}
			offset := tHoff + pos
			if offset >= nraw {
				fields[i] = nil
				break
			}
			kind, total, _, _ := varlenaParse(raw, offset)
			switch kind {
			case VARLENA_EXTERNAL:
				if offset+KB_EXTERNAL_SIZE > nraw {
					fields[i] = nil
					break
				}
				f := raw[offset : offset+KB_EXTERNAL_SIZE]
				fields[i] = &f
				pos += KB_EXTERNAL_SIZE
			case VARLENA_1B:
				if offset+total > nraw {
					fields[i] = nil
					break
				}
				f := raw[offset : offset+total]
				fields[i] = &f
				pos += total
			case VARLENA_4B, VARLENA_4BC:
				if offset+total > nraw {
					fields[i] = nil
					break
				}
				f := raw[offset : offset+total]
				fields[i] = &f
				pos += total
			default:
				fields[i] = nil
				break
			}
		} else {
			if align > 1 {
				pos = (pos + align - 1) &^ (align - 1)
			}
			offset := tHoff + pos
			if offset+item.AttLen > nraw {
				fields[i] = nil
				break
			}
			f := raw[offset : offset+item.AttLen]
			fields[i] = &f
			pos += item.AttLen
		}
	}
	return fields
}

func calculateTupleSize(t *HeapTuple, colLengths []ColLen) int {
	nulls := t.getNulls()
	dataSize := 0
	nraw := len(t.Raw)
	for i, item := range colLengths {
		align := layoutAlign(item.AttLen, item.AttAlign)
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
			kind, total, _, _ := varlenaParse(t.Raw, offset)
			if kind == VARLENA_EXTERNAL {
				dataSize += KB_EXTERNAL_SIZE
			} else if kind != "" {
				dataSize += total
			} else {
				break
			}
		} else {
			if align > 1 {
				dataSize = (dataSize + align - 1) &^ (align - 1)
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
	decryptor  pageDecryptor // v1.0.39 TDE：TOAST 页解密（与主表同密钥；免费版为 nil）
	file       *os.File
	fileOnce   sync.Once    // 并发解析时仅初始化一次文件句柄
	segFs      []*os.File   // 多段文件句柄（>1GB 续段 .1/.2/...）
	mu         sync.RWMutex // 并发解析时保护 PageCache 读写与 LRU 淘汰
	// v1.0.44 P2-3：TOAST 数据损坏计数（atomic，并行 worker 安全；报告输出）
	MissingCount int64 // 外联 chunk 缺失/长度不符（整列降级占位符）
	CorruptCount int64 // 压缩数据解压失败（toastDecompress 返回 nil）
}

// openFile：打开主文件 + 续段文件链（v1.0.30 多段支持）
func (tf *ToastFile) openFile() {
	tf.fileOnce.Do(func() {
		paths := segFiles(tf.Path)
		tf.segFs = make([]*os.File, len(paths))
		for i, p := range paths {
			if f, err := os.Open(p); err == nil {
				tf.segFs[i] = f
			}
		}
		if len(tf.segFs) > 0 {
			tf.file = tf.segFs[0]
		}
	})
}

// segReadAt：按全局页号跨段读取（v1.0.30；v1.0.49 段边界按页大小自适应）
func (tf *ToastFile) segReadAt(buf []byte, pageno int) (int, error) {
	seg, off := segPos(pageno, tf.PageSize)
	if seg >= len(tf.segFs) || tf.segFs[seg] == nil {
		return 0, os.ErrNotExist
	}
	n, err := tf.segFs[seg].ReadAt(buf, int64(off)*int64(tf.PageSize))
	if err != nil && n == 0 {
		return n, err
	}
	// v1.0.39 TDE：TOAST 页同样 offset64+ 加密（与主表同密钥），读后解密
	if tf.decryptor != nil && n >= 80 {
		if _, derr := tf.decryptor.DecryptPage(buf[:n]); derr != nil {
			return n, derr
		}
	}
	return n, err
}

var toastColLengths = []ColLen{
	{4, false, "i"}, // chunk_id
	{4, false, "i"}, // chunk_seq
	{0, true, "i"},  // chunk_data (varlena)
}

// 提取 TOAST 元组的 (chunk_id, chunk_seq)
func toastLightTuple(raw []byte, tHoff int, nulls []bool) (uint32, uint32) {
	if tHoff+8 > len(raw) {
		return 0, 0
	}
	if len(nulls) >= 2 && (nulls[0] || nulls[1]) {
		return 0, 0
	}
	chunkID := u32(raw, tHoff)
	chunkSeq := u32(raw, tHoff+4)
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
	kind, total, _, _ := varlenaParse(raw, voff)
	if kind == "" {
		return 0, 0
	}
	if kind == VARLENA_1B && total <= 1 {
		return 0, 0
	}
	if (kind == VARLENA_4B || kind == VARLENA_4BC) && total <= 4 {
		return 0, 0
	}
	return chunkID, chunkSeq
}

// 提取 TOAST 元组三字段 → chunk
func fieldsToChunk(fields []*[]byte) (uint32, uint32, []byte) {
	if len(fields) < 3 || fields[0] == nil || fields[1] == nil || fields[2] == nil {
		return 0, 0, nil
	}
	chunkID := u32(*fields[0], 0)
	chunkSeq := u32(*fields[1], 0)
	if !(1 <= chunkID && chunkID <= 100000000) {
		return 0, 0, nil
	}
	if !(0 <= chunkSeq && chunkSeq <= 1000000) {
		return 0, 0, nil
	}
	f := *fields[2]
	var payload []byte
	kind, total, _, _ := varlenaParse(f, 0)
	switch kind {
	case VARLENA_1B:
		payload = f[1 : 1+total-1]
	case VARLENA_4B:
		payload = f[4 : 4+total-4]
	case VARLENA_4BC:
		// chunk_data 内联压缩极少见；此处按原样（解码层处理）
		payload = f[4 : 4+total-4]
	default:
		return 0, 0, nil
	}
	return chunkID, chunkSeq, payload
}

// 解析一页，返回 [(offset, chunk_id, chunk_seq, payload_or_nil)]
func extractPageChunks(raw []byte, pageno int, ps int, wantPayload bool) [][4]interface{} {
	lay := tryStandardLayout(raw, ps)
	if lay == nil {
		lay = tryAutoLayout(raw, ps)
	}
	if lay == nil {
		return nil
	}
	var out [][4]interface{}
	lower := int(lay.Lower)
	itemFound := false
	for i := 0; lay.HeaderEnd+4*i+4 <= lower; i++ {
		rawID := uint32(u16(raw, lay.HeaderEnd+4*i)) | uint32(u16(raw, lay.HeaderEnd+4*i+2))<<16
		off := int(rawID & 0x7FFF)
		flags := (rawID >> 15) & 0x03
		ln := int((rawID >> 17) & 0x7FFF)
		if flags != ITEMID_NORMAL || off >= ps || ln > ps || off+ln > ps {
			continue
		}
		itemFound = true
		td := raw[off : off+ln]
		tup := parseTuple(td, 12)
		if tup == nil || !tup.headerConsistent() || !tup.isLive() {
			continue
		}
		nulls := tup.getNulls()
		if wantPayload {
			fields := extractFieldsDirect(td, tup.THoff, nulls, toastColLengths)
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
			tup := parseTuple(td, 12)
			if tup != nil && tup.headerConsistent() && tup.isLive() {
				nulls := tup.getNulls()
				if wantPayload {
					fields := extractFieldsDirect(td, tup.THoff, nulls, toastColLengths)
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
	for _, p := range segFiles(tf.Path) {
		if fi, err := os.Stat(p); err == nil {
			npages += int(fi.Size()) / tf.PageSize
		}
	}
	buf := make([]byte, tf.PageSize)
	base := 0
	for _, seg := range segFiles(tf.Path) {
		f, err := os.Open(seg)
		if err != nil {
			continue
		}
		pageno := base
		for {
			n, err := f.Read(buf)
			if err != nil || n < tf.PageSize {
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
	for _, p := range segFiles(tf.Path) {
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
			segPaths := segFiles(tf.Path)
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
				seg, off := segPos(pageno, tf.PageSize)
				if seg >= len(segFs) || segFs[seg] == nil {
					continue
				}
				n, err := segFs[seg].ReadAt(buf, int64(off)*int64(tf.PageSize))
				if err != nil || n < tf.PageSize {
					continue
				}
				// v1.0.39 TDE：TOAST 索引预建同样需要先解密页
				if tf.decryptor != nil {
					if _, derr := tf.decryptor.DecryptPage(buf[:n]); derr != nil {
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
					logf("  TOAST 索引: %d/%d 页...", pageno, npages)
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

func (tf *ToastFile) fetchAndReassemble(valueid uint32, expectedSize int) []byte {
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
func checkExternal(raw []byte) ([]byte, bool, *ExternalInfo) {
	if len(raw) == 0 {
		return raw, false, nil
	}
	kind, total, _, _ := varlenaParse(raw, 0)
	switch kind {
	case VARLENA_EXTERNAL:
		ext := parseExternalPointer(raw, 0)
		if ext != nil {
			return []byte{}, true, ext
		}
		return raw, false, nil
	case VARLENA_1B:
		p := total - 1
		if p < 0 {
			p = 0
		}
		if 1+p > len(raw) {
			p = len(raw) - 1
		}
		return raw[1 : 1+p], false, nil
	case VARLENA_4B, VARLENA_4BC:
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
	// 注意：roleMap 由 main.go 在解析开始前一次性 setRoleMap 注入（并发解析期只读，不再此处写）
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
			_, isExt, ext := checkExternal(rawB)
			if isExt && toast != nil {
				reassembled := toast.fetchAndReassemble(ext.ValueID, 0)
				if reassembled == nil {
					// v1.0.44 P2-3：TOAST 缺失计数（不再静默）
					atomic.AddInt64(&toast.MissingCount, 1)
					s := "__TOAST_MISSING__"
					values = append(values, &s)
					continue
				}
				expected := int(ext.RawSize) - 4
				if ext.Compressed {
					data := toastDecompress(reassembled, expected, ext.Method)
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
				rawB = rebuildVarlena(rawB)
			} else if isExt && toast == nil {
				s := "__TOAST_MISSING__"
				values = append(values, &s)
				continue
			}
			// 非外联：rawB 保持完整 varlena（含头），decoder 内部自行解头
		}
		decoded := decodeValue(col.TypeOID, rawB)
		values = append(values, &decoded)
	}
	return values
}

// 行是否有效：至少一个非空非占位值
func rowHasValid(values []*string) bool {
	for _, v := range values {
		if v != nil && *v != "" && *v != "__TOAST_MISSING__" {
			return true
		}
	}
	return false
}

// dumpRows：两阶段（标准 ItemId → 扫描回退）
type RowIter struct {
	tm           *TableMeta
	toast        *ToastFile
	includeDel   bool
	onlyDeleted  bool
	limit        int
	pageSize     int
	pgVersion    int
	isKB         bool
	path         string
	badPages     []int
	badMu        sync.Mutex // v1.0.44 P2-2：保护 badPages 追加（并行 worker 安全）
	badCount     int64      // v1.0.38：坏页计数（atomic，并行 worker 安全；报告用）
	verboseDebug bool
	Parallel     int           // 行解析 worker 数（>1 启用按页分片并发解析）
	decryptor    pageDecryptor // v1.0.39 TDE：页读取后解密（offset64+ SM4-ECB；免费版为 nil）
	// v1.0.40 --progress：已处理页数/行数（原子，串并行通用；exportOneTable 的 progress goroutine 读取）
	progressDone int64
	progressRows int64
}

// addBadPage：记录坏页（v1.0.44 P2-2：原子计数 + 明细页号记录，报告输出 bad_pages 列表）
func (ri *RowIter) addBadPage(pageno int) {
	atomic.AddInt64(&ri.badCount, 1)
	ri.badMu.Lock()
	ri.badPages = append(ri.badPages, pageno)
	ri.badMu.Unlock()
}

// ---------- B12 修复：TDE 密文页轻量探测（不依赖 -tags tde 构建） ----------

// suspectCipherPage：单页密文启发式（与 tde.Detect 等价）：
// ① 页头非全 0 时必须自洽（pd_lower/pd_upper 合理）；TDE 加密页页头 0-63B 可为全 0（金仓实测 pd_lower/pd_upper=0）；
// ② offset 64 起 16B 全 0 块很少（正常页空闲区成片全 0，密文接近随机）→ 疑似加密页。
func suspectCipherPage(b []byte) bool {
	if len(b) < 80 {
		return false
	}
	// v1.0.49 修复（B1）：pd_lower/pd_upper 位于页头 offset 12/14，
	// 此前误读 offset 24/26（pd_prune_xid），导致真密文页漏报、静默导出垃圾。
	lower := int(b[12]) | int(b[13])<<8
	upper := int(b[14]) | int(b[15])<<8
	if (lower != 0 || upper != 0) && (lower < 28 || upper > len(b) || lower > upper) {
		return false
	}
	zc := 0
	for i := 64; i+1 < len(b); i += 16 {
		all := true
		for j := 0; j < 16 && i+j < len(b); j++ {
			if b[i+j] != 0 {
				all = false
				break
			}
		}
		if all {
			zc++
		}
	}
	return zc < 8
}

// probeTableCipher：对表数据文件前 32 页采样，返回 (检查页数, 疑似密文页数)。
// path 支持多段文件（segFiles 模式）与单文件。
func probeTableCipher(path string, pageSize int) (int, int) {
	if path == "" || pageSize < 1024 {
		return 0, 0
	}
	checked, suspect := 0, 0
	for _, seg := range segFiles(path) {
		f, err := os.Open(seg)
		if err != nil {
			continue
		}
		buf := make([]byte, pageSize)
		for i := 0; i < 32; i++ {
			n, err := f.Read(buf)
			if err != nil || n < pageSize {
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

// warnIfCipherPages：密文页告警（B12）。v1.0.46 从"仅 0 行"扩展到所有无解密器的输出场景
// （--count / --data / --sql），因为加密表解不出正确行结构时可能给出少量假行（实测金仓加密
// TOAST 表 10 行在无密钥时统计为 1 行、加密表错密钥时导出 2 行垃圾），静默比 0 行更危险。
func warnIfCipherPages(ri *RowIter, ps int, dec pageDecryptor, rows int64) {
	if ri == nil || dec != nil || ri.path == "" {
		return
	}
	checked, suspect := probeTableCipher(ri.path, ps)
	if checked < 1 || suspect == 0 {
		return
	}
	name := ri.path
	if ri.tm != nil {
		name = ri.tm.FullName()
	}
	if rows == 0 {
		logf("警告: 表 %s 导出 0 行但 %d/%d 个数据页呈密文特征——可能为 TDE 加密表且未提供密钥/密钥无效（--enckey 或 --wallet-password），或表为空/全部已删除，请核实", name, suspect, checked)
		return
	}
	logf("警告: 表 %s 有 %d/%d 个数据页呈密文特征（疑 TDE 加密表且未提供密钥），当前输出 %d 行不可信；请提供 --enckey/--wallet-password 或使用付费版", name, suspect, checked, rows)
}

// badPagesSnapshot：返回坏页明细副本（报告用）
func (ri *RowIter) badPagesSnapshot() []int {
	ri.badMu.Lock()
	out := make([]int, len(ri.badPages))
	copy(out, ri.badPages)
	ri.badMu.Unlock()
	return out
}

// toastMissing / toastCorrupt：TOAST 损坏计数（v1.0.44 P2-3，报告用；无 TOAST 时为 0）
func (ri *RowIter) toastMissing() int64 {
	if ri.toast == nil {
		return 0
	}
	return atomic.LoadInt64(&ri.toast.MissingCount)
}
func (ri *RowIter) toastCorrupt() int64 {
	if ri.toast == nil {
		return 0
	}
	return atomic.LoadInt64(&ri.toast.CorruptCount)
}

func (ri *RowIter) iterTuples(pageno int, page []byte) [][2]interface{} {
	// 返回 [(index_or_pos, *HeapTuple)]
	lay := tryStandardLayout(page, ri.pageSize)
	if lay == nil {
		lay = tryAutoLayout(page, ri.pageSize)
	}
	if lay == nil {
		return nil
	}
	var out [][2]interface{}
	lower := int(lay.Lower)
	for i := 0; lay.HeaderEnd+4*i+4 <= lower; i++ {
		rawID := uint32(u16(page, lay.HeaderEnd+4*i)) | uint32(u16(page, lay.HeaderEnd+4*i+2))<<16
		off := int(rawID & 0x7FFF)
		flags := (rawID >> 15) & 0x03
		ln := int((rawID >> 17) & 0x7FFF)
		if flags != ITEMID_NORMAL || off >= ri.pageSize || ln > ri.pageSize || off+ln > ri.pageSize {
			continue
		}
		tup := parseTuple(page[off:off+ln], ri.pgVersion)
		if tup == nil {
			continue
		}
		out = append(out, [2]interface{}{i, tup})
	}
	return out
}

func (ri *RowIter) buildRow(tup *HeapTuple, posTag [2]int) *Row {
	deleted := tup.isDeleted()
	if !tup.isLive() && !deleted {
		return nil
	}
	if !ri.includeDel && deleted {
		return nil
	}
	if ri.onlyDeleted && !deleted {
		return nil
	}
	// 行内列数：金仓与 PG 均为 t_infomask2 低 11 位（PG 源码 HeapTupleHeaderGetNatts
	// = t_infomask2 & 0x07FF，实测 V8/V9/PG12-18 一致）。金仓 ALTER 残留幽灵 att 行
	// （attnum 超出数据行实际列数）时，行内列数 < catalog 列数，必须按行内列数截断，
	// 否则位图/值区错位导致丢行。
	natts := tup.NAttrs
	colLengths := buildColLengths(ri.tm)
	if natts > 0 && natts < len(colLengths) {
		colLengths = colLengths[:natts]
		tup.NAttrs = natts
	}
	nulls := tup.getNulls()
	fields := extractFieldsDirect(tup.Raw, tup.THoff, nulls, colLengths)
	values := decodeFields(fields, ri.tm, ri.toast)
	atomic.AddInt64(&ri.progressRows, 1) // v1.0.40 --progress
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
	batch    bool // v1.0.38：多值 INSERT 模式，formatRow 输出 VALUES 元组（不含 INSERT 头）
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
		c.target = quoteName(tm.Schema) + "." + quoteName(tm.RelName)
		if completeInsert {
			quoted := make([]string, len(outIdx))
			for i, idx := range outIdx {
				quoted[i] = quoteName(liveCols[idx].Name)
			}
			c.colStr = "(" + strings.Join(quoted, ", ") + ")"
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
			if row.Values[idx] == nil || *row.Values[idx] == "__TOAST_MISSING__" {
				parts[i] = "\\N"
			} else {
				v := *row.Values[idx]
				if mysqlBitOIDs[c.liveCols[idx].TypeOID] {
					v = bitBinToHex(v)
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
		if row.Values[c.outIdx[i]] == nil || v == "__TOAST_MISSING__" {
			sqlVals[i] = "NULL"
		} else {
			sqlVals[i] = sqlQuote(v, &c.liveCols[c.outIdx[i]])
		}
	}
	// v1.0.38：多值 INSERT 批模式——live 行输出 VALUES 元组，deleted 行输出完整单条
	if c.batch {
		if row.Deleted {
			return "-- DELETED ctid=" + row.CTID + "\n" + c.verb + " " + c.target + " " + c.colStr + " VALUES (" + strings.Join(sqlVals, ", ") + ");"
		}
		return "(" + strings.Join(sqlVals, ", ") + ")"
	}
	stmt := c.verb + " " + c.target + " " + c.colStr + " VALUES (" + strings.Join(sqlVals, ", ") + ");"
	if row.Deleted {
		stmt = "-- DELETED ctid=" + row.CTID + "\n" + stmt
	}
	return stmt
}

// pageCount：主表完整页数（多段文件总和，整除）
func (ri *RowIter) pageCount() int {
	total := 0
	for _, p := range segFiles(ri.path) {
		if fi, err := os.Stat(p); err == nil {
			total += int(fi.Size() / int64(ri.pageSize))
		}
	}
	return total
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

// segFiles：主文件 + 续段文件列表（存在即加入，上限 1024 段防异常）
func segFiles(path string) []string {
	files := []string{path}
	for i := 1; i < 1024; i++ {
		p := fmt.Sprintf("%s.%d", path, i)
		if _, err := os.Stat(p); err == nil {
			files = append(files, p)
		} else {
			break
		}
	}
	return files
}

// segPos：全局页号 → (段索引, 段内页号)；段边界按实际页大小计算（v1.0.49）
func segPos(pageno, pageSize int) (int, int) {
	segPages := relSegPagesOf(pageSize)
	return pageno / segPages, pageno % segPages
}

// probeRowValid：轻量判定行是否含有效值（NULL/空串/TOAST 缺失列之外任一列有值）
// 与 rowHasValid(decodeFields 后) 语义一致：NULL→无效；空串("")→无效；__TOAST_MISSING__→无效
func (ri *RowIter) probeRowValid(tup *HeapTuple) bool {
	natts := tup.NAttrs
	colLengths := buildColLengths(ri.tm)
	if natts > 0 && natts < len(colLengths) {
		colLengths = colLengths[:natts]
	}
	nulls := tup.getNulls()
	raw := tup.Raw
	base := tup.THoff
	nraw := len(raw)
	pos := 0
	for i, item := range colLengths {
		align := layoutAlign(item.AttLen, item.AttAlign)
		if i < len(nulls) && nulls[i] {
			continue
		}
		if item.IsVarlena {
			kind0, _, _, _ := varlenaParse(raw, base+pos)
			if kind0 == "" || ((kind0 == VARLENA_4B || kind0 == VARLENA_4BC) && align > 1 && (pos&(align-1)) != 0) {
				if align > 1 {
					pos = (pos + align - 1) &^ (align - 1)
				}
			}
			offset := base + pos
			if offset >= nraw {
				return false
			}
			kind, total, _, _ := varlenaParse(raw, offset)
			switch kind {
			case VARLENA_EXTERNAL:
				if offset+KB_EXTERNAL_SIZE <= nraw {
					ext := parseExternalPointer(raw, offset)
					if ext != nil && ri.toast != nil {
						if _, ok := ri.toast.PosIndex[ext.ValueID]; !ok {
							pos += KB_EXTERNAL_SIZE
							continue // TOAST 缺失 → 无效列
						}
					}
					return true
				}
				return false
			case VARLENA_1B:
				if offset+total > nraw {
					return false
				}
				pos += total
				if total > 1 {
					return true
				}
			case VARLENA_4B, VARLENA_4BC:
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
	npages := ri.pageCount()
	if npages == 0 {
		return false, false
	}
	buf := make([]byte, ri.pageSize)
	count := 0
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
			// v1.0.39 TDE：串行路径读页后解密
			if ri.decryptor != nil {
				if _, derr := ri.decryptor.DecryptPage(buf[:n]); derr != nil {
					break
				}
			}
			items := ri.iterTuples(pageno, buf)
			if len(items) > 0 {
				foundStandard = true
			}
			for _, it := range items {
				tup := it[1].(*HeapTuple)
				if !hasValid && ri.probeRowValid(tup) {
					hasValid = true
				}
				count++
				if ri.limit > 0 && count >= ri.limit {
					f.Close()
					return foundStandard, hasValid
				}
			}
			pageno++
		}
		f.Close()
		if fi, err := os.Stat(seg); err == nil {
			base += int(fi.Size() / int64(ri.pageSize))
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
	npages := ri.pageCount()
	if npages == 0 {
		return
	}
	buf := make([]byte, ri.pageSize)
	count := 0
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
			// v1.0.39 TDE：串行路径读页后解密
			if ri.decryptor != nil {
				if _, derr := ri.decryptor.DecryptPage(buf[:n]); derr != nil {
					break
				}
			}
			atomic.AddInt64(&ri.progressDone, 1) // v1.0.40 --progress
			items := ri.iterTuples(pageno, buf)
			if items == nil {
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
				if ri.limit > 0 && count >= ri.limit {
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

// streamScanSerial：数据区扫描回退流式输出（串行；多段文件遍历）
func (ri *RowIter) streamScanSerial(c *fmtCtx, emit func(string) bool) {
	npages := ri.pageCount()
	if npages == 0 {
		return
	}
	colLengths := buildColLengths(ri.tm)
	nExpected := len(ri.tm.Columns)
	buf := make([]byte, ri.pageSize)
	count := 0
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
			atomic.AddInt64(&ri.progressDone, 1) // v1.0.40 --progress
			lay := tryStandardLayout(buf, ri.pageSize)
			if lay == nil {
				lay = tryAutoLayout(buf, ri.pageSize)
			}
			if lay == nil {
				ri.addBadPage(pageno)
				pageno++
				continue
			}
			pos := int(lay.Upper)
			special := int(lay.Special)
			for pos+HEAP_TUPLE_HEADER_SIZE <= special {
				td := buf[pos:special]
				tHoff := int(td[22])
				infomask := u16(td, 20)
				nattrs := int(u16(td, 18) & HEAP_NATTS_MASK)
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
				txmin := u32(td, 0)
				if txmin == 0 || txmin > 0x7FFFFFFF {
					pos += 4
					continue
				}
				if infomask&0xFF00 == 0 {
					pos += 4
					continue
				}
				tup := parseTuple(td, ri.pgVersion)
				if tup == nil || !tup.headerConsistent() {
					pos += 4
					continue
				}
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
					if ri.limit > 0 && count >= ri.limit {
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
			base += int(fi.Size() / int64(ri.pageSize))
		}
	}
}

// streamStdParallel：标准 ItemId 模式流式输出（worker 池：worker 内解析+转义，页序窗口保序转发；多段文件支持）
func (ri *RowIter) streamStdParallel(c *fmtCtx, emit func(string) bool) {
	npages := ri.pageCount()
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
			segPaths := segFiles(ri.path)
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
			buf := make([]byte, ri.pageSize)
			for pageno := range jobs {
				if atomic.LoadInt32(&stop) == 1 {
					return
				}
				seg, off := segPos(pageno, ri.pageSize)
				if seg >= len(segFs) || segFs[seg] == nil {
					pr := pageLinesResult{pageno: pageno}
					if !sendPageResult(results, pr, &stop) {
						return
					}
					continue
				}
				if _, err := segFs[seg].ReadAt(buf, int64(off)*int64(ri.pageSize)); err != nil {
					pr := pageLinesResult{pageno: pageno}
					if !sendPageResult(results, pr, &stop) {
						return
					}
					continue
				}
				// v1.0.39 TDE：页读取后、行解析前解密
				if ri.decryptor != nil {
					if _, derr := ri.decryptor.DecryptPage(buf); derr != nil {
						pr := pageLinesResult{pageno: pageno}
						if !sendPageResult(results, pr, &stop) {
							return
						}
						continue
					}
				}
				atomic.AddInt64(&ri.progressDone, 1) // v1.0.40 --progress（v1.0.41 补：标准并行路径缺埋点致页进度恒 0）
				items := ri.iterTuples(pageno, buf)
				if items == nil {
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
	pending := make(map[int][]string)
	next := 0
	count := 0
	for pr := range results {
		if atomic.LoadInt32(&stop) == 1 {
			continue
		}
		pending[pr.pageno] = pr.lines
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
				if ri.limit > 0 && count >= ri.limit {
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
	npages := ri.pageCount()
	if npages == 0 {
		return
	}
	colLengths := buildColLengths(ri.tm)
	nExpected := len(ri.tm.Columns)
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
			segPaths := segFiles(ri.path)
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
			buf := make([]byte, ri.pageSize)
			for pageno := range jobs {
				if atomic.LoadInt32(&stop) == 1 {
					return
				}
				seg, off := segPos(pageno, ri.pageSize)
				if seg >= len(segFs) || segFs[seg] == nil {
					pr := pageLinesResult{pageno: pageno}
					if !sendPageResult(results, pr, &stop) {
						return
					}
					continue
				}
				if _, err := segFs[seg].ReadAt(buf, int64(off)*int64(ri.pageSize)); err != nil {
					pr := pageLinesResult{pageno: pageno}
					if !sendPageResult(results, pr, &stop) {
						return
					}
					continue
				}
				// v1.0.39 TDE：串行路径同样解密
				if ri.decryptor != nil {
					if _, derr := ri.decryptor.DecryptPage(buf); derr != nil {
						pr := pageLinesResult{pageno: pageno}
						if !sendPageResult(results, pr, &stop) {
							return
						}
						continue
					}
				}
				atomic.AddInt64(&ri.progressDone, 1) // v1.0.40 --progress
				lay := tryStandardLayout(buf, ri.pageSize)
				if lay == nil {
					lay = tryAutoLayout(buf, ri.pageSize)
				}
				if lay == nil {
					ri.addBadPage(pageno)
				}
				pr := pageLinesResult{pageno: pageno}
				if lay != nil {
					pos := int(lay.Upper)
					special := int(lay.Special)
					for pos+HEAP_TUPLE_HEADER_SIZE <= special {
						td := buf[pos:special]
						tHoff := int(td[22])
						infomask := u16(td, 20)
						nattrs := int(u16(td, 18) & HEAP_NATTS_MASK)
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
						txmin := u32(td, 0)
						if txmin == 0 || txmin > 0x7FFFFFFF {
							pos += 4
							continue
						}
						if infomask&0xFF00 == 0 {
							pos += 4
							continue
						}
						tup := parseTuple(td, ri.pgVersion)
						if tup == nil || !tup.headerConsistent() {
							pos += 4
							continue
						}
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
	pending := make(map[int][]string)
	next := 0
	count := 0
	for pr := range results {
		if atomic.LoadInt32(&stop) == 1 {
			continue
		}
		pending[pr.pageno] = pr.lines
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
				if ri.limit > 0 && count >= ri.limit {
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
	if col != nil && mysqlBitOIDs[col.TypeOID] {
		// 金仓 mysql BIT：SQL 导入必须 B'...' 位字面量（普通字符串走 varchar_bit cast 会失败）
		return "B'" + value + "'"
	}
	if col != nil && mysqlBinOIDs[col.TypeOID] && strings.HasPrefix(value, "0x") {
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
	if col != nil && mysqlBinOIDs[col.TypeOID] && strings.HasPrefix(value, "\\x") {
		// v1.0.46 修复（金仓 mysql binary/varbinary 回灌被污染）：
		// 这两个类型（oid 3383/3384）的 typinput=binaryin 没有 hex 文本格式，
		// 直接把 '\x00ff…' 当字符串喂进去会被按【字面量字节】存入
		// （实测存成 0x5C7830306666… = '\','x','0','0',… 的 ASCII，并按 binary(n) 截断）。
		// 加 ::bytea 让 bytea 先按 hex 解析，再由 bytea→binary 的 assignment cast 还原字节。
		return sqlStringLiteral(value) + "::bytea"
	}
	if col != nil && noQuoteOIDs[col.TypeOID] {
		if value == "NaN" || value == "Infinity" || value == "-Infinity" {
			return sqlStringLiteral(value)
		}
		return value
	}
	return sqlStringLiteral(value)
}

// quoteName：标识符加引号。v1.0.48 修复（P1-F2）：原实现直接 `"\"" + name + "\""`，
// 标识符内部的双引号未转义，`aud"it` 这种名字会拼出 `"aud"it"` —— 语法非法。
// 统一委托给 schema_ddl.go 的 quoteIdent（`"` → `""`），消除两套不一致实现。
func quoteName(name string) string { return quoteIdent(name) }

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
	c := buildFmtCtx(ri.tm, fmtSQL, completeInsert, fields, ",")
	return ri.StreamFormatted(c)
}

// ToSQLBatch 多值 INSERT（v1.0.38）：每 batchN 行合并一条 INSERT ... VALUES (...),(...)
// live 行攒批输出；deleted 行单独输出完整单条（含 -- DELETED 注释，不破坏多值语法）。
// 与 --parallel 正交（各 worker 页序保序转发，批在 emit 层组装，顺序与单条一致）。
// rowCount 非空时累计实际数据行数（报告用；多值合并后语句数≠行数）。
func ToSQLBatch(ri *RowIter, completeInsert bool, fields []string, batchN int, rowCount *int64) chan string {
	c := buildFmtCtx(ri.tm, fmtSQL, completeInsert, fields, ",")
	c.batch = true
	out := make(chan string, 16)
	go func() {
		defer close(out)
		var pending []string
		flush := func() {
			if len(pending) == 0 {
				return
			}
			out <- c.verb + " " + c.target + " " + c.colStr + " VALUES " + strings.Join(pending, ", ") + ";"
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
	c := buildFmtCtx(ri.tm, fmtCSV, false, fields, delimiter)
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
