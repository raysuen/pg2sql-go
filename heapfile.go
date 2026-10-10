// pg2sql heapfile 层：堆文件页遍历、元组提取、字段提取、TOAST 重组、行/SQL/CSV 生成
// 对应 Python 版 pg2sql/{page.py, tuple.py, heapfile.py, toast.py}，语义等价翻译
// Author: raysuen
package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

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
	TXmin      uint32
}

type TableMeta struct {
	Schema      string
	RelName     string
	RelFileNode uint32
	ToastRelID  uint32
	// ToastRelFile 为 toast 表的 relfilenode。reltoastrelid 存的是 OID，
	// TRUNCATE/重建过的表 relfilenode != OID，磁盘文件名按 relfilenode 命名，
	// 直接拿 OID 拼路径会指向旧文件/空文件导致外联字段全部 NULL（v1.0.21 修复）。
	ToastRelFile uint32
	IsKB         bool
	RelKind      string
	Columns      []ColumnDef
	RoleMap      map[uint32]string
	TypeNames    map[uint32]string
	HasToast     bool
	PrimaryKey   []string
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
	file       *os.File
	fileOnce   sync.Once    // 并发解析时仅初始化一次文件句柄
	segFs      []*os.File   // 多段文件句柄（>1GB 续段 .1/.2/...）
	mu         sync.RWMutex // 并发解析时保护 PageCache 读写与 LRU 淘汰
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

// segReadAt：按全局页号跨段读取（v1.0.30）
func (tf *ToastFile) segReadAt(buf []byte, pageno int) (int, error) {
	seg, off := segPos(pageno)
	if seg >= len(tf.segFs) || tf.segFs[seg] == nil {
		return 0, os.ErrNotExist
	}
	return tf.segFs[seg].ReadAt(buf, int64(off)*int64(tf.PageSize))
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
				seg, off := segPos(pageno)
				if seg >= len(segFs) || segFs[seg] == nil {
					continue
				}
				n, err := segFs[seg].ReadAt(buf, int64(off)*int64(tf.PageSize))
				if err != nil || n < tf.PageSize {
					continue
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
					s := "__TOAST_MISSING__"
					values = append(values, &s)
					continue
				}
				expected := int(ext.RawSize) - 4
				if ext.Compressed {
					data := toastDecompress(reassembled, expected, ext.Method)
					if data == nil {
						s := "__TOAST_CORRUPT__"
						values = append(values, &s)
						continue
					}
					rawB = data
				} else if len(reassembled) != expected {
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
	verboseDebug bool
	Parallel     int // 行解析 worker 数（>1 启用按页分片并发解析）
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
// PG 单段文件页数上限 RELSEG_SIZE = 0x40000000/BLCKSZ（8K=131072 页=1GB，16K=2GB，32K=4GB）
const relSegPages = 131072

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

// segPos：全局页号 → (段索引, 段内页号)
func segPos(pageno int) (int, int) {
	return pageno / relSegPages, pageno % relSegPages
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
			items := ri.iterTuples(pageno, buf)
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
			lay := tryStandardLayout(buf, ri.pageSize)
			if lay == nil {
				lay = tryAutoLayout(buf, ri.pageSize)
			}
			if lay == nil {
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
				seg, off := segPos(pageno)
				if seg >= len(segFs) || segFs[seg] == nil {
					pr := pageLinesResult{pageno: pageno}
					select {
					case results <- pr:
					default:
						if atomic.LoadInt32(&stop) == 1 {
							return
						}
						results <- pr
					}
					continue
				}
				if _, err := segFs[seg].ReadAt(buf, int64(off)*int64(ri.pageSize)); err != nil {
					pr := pageLinesResult{pageno: pageno}
					select {
					case results <- pr:
					default:
						if atomic.LoadInt32(&stop) == 1 {
							return
						}
						results <- pr
					}
					continue
				}
				items := ri.iterTuples(pageno, buf)
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
				select {
				case results <- pr:
				default:
					if atomic.LoadInt32(&stop) == 1 {
						return
					}
					results <- pr
				}
			}
		}()
	}
	go func() {
		for pageno := 0; pageno < npages; pageno++ {
			if atomic.LoadInt32(&stop) == 1 {
				break
			}
			jobs <- pageno
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
				seg, off := segPos(pageno)
				if seg >= len(segFs) || segFs[seg] == nil {
					pr := pageLinesResult{pageno: pageno}
					select {
					case results <- pr:
					default:
						if atomic.LoadInt32(&stop) == 1 {
							return
						}
						results <- pr
					}
					continue
				}
				if _, err := segFs[seg].ReadAt(buf, int64(off)*int64(ri.pageSize)); err != nil {
					pr := pageLinesResult{pageno: pageno}
					select {
					case results <- pr:
					default:
						if atomic.LoadInt32(&stop) == 1 {
							return
						}
						results <- pr
					}
					continue
				}
				lay := tryStandardLayout(buf, ri.pageSize)
				if lay == nil {
					lay = tryAutoLayout(buf, ri.pageSize)
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
				select {
				case results <- pr:
				default:
					if atomic.LoadInt32(&stop) == 1 {
						return
					}
					results <- pr
				}
			}
		}()
	}
	go func() {
		for pageno := 0; pageno < npages; pageno++ {
			if atomic.LoadInt32(&stop) == 1 {
				break
			}
			jobs <- pageno
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
var noQuoteOIDs = map[uint32]bool{
	16: true, 20: true, 21: true, 23: true, 26: true, 700: true, 701: true, 790: true, 1700: true,
}

func sqlQuote(value string, col *ColumnDef) string {
	if col != nil && mysqlBitOIDs[col.TypeOID] {
		// 金仓 mysql BIT：SQL 导入必须 B'...' 位字面量（普通字符串走 varchar_bit cast 会失败）
		return "B'" + value + "'"
	}
	if col != nil && noQuoteOIDs[col.TypeOID] {
		if value == "NaN" || value == "Infinity" || value == "-Infinity" {
			return sqlStringLiteral(value)
		}
		return value
	}
	return sqlStringLiteral(value)
}

func quoteName(name string) string { return "\"" + name + "\"" }

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
