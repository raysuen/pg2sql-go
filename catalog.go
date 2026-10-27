// pg2sql catalog 层：系统目录自动发现、编码探测、meta.json 加载/导出
// 对应 Python 版 pg2sql/catalog.py（v3.3），版本感知布局逐条等价翻译
// Author: raysuen
package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	PG_CLASS_RELFILE     = 1259
	PG_ATTRIBUTE_RELFILE = 1249
	PG_TYPE_RELFILE      = 1247
	PG_ENUM_RELFILE      = 3501
	PG_AUTHID_RELFILE    = 1260
	PG_DATABASE_RELFILE  = 1262
	PG_NAMESPACE_RELFILE = 2615
	PG_INDEX_RELFILE     = 2610
	PG_INHERITS_RELFILE  = 2611
)

// 编码 ID → codec（PG pg_wchar.h pg_enc 枚举）
// B4 修复：补齐 PG 标准 EUC_JP(1)/EUC_CN(2)/EUC_KR(3)/EUC_TW(4)/EUC_JIS_2004(5)/MULE_INTERNAL(7)，
// 避免缺失映射落入 utf8Fallback 逐字节 Latin-1 静默损坏数据
var pgEncodingIDToCodec = map[int]string{
	0: "sql_ascii", 1: "euc-jp", 2: "euc-cn", 3: "euc-kr", 4: "euc-tw",
	5: "euc-jis-2004", 6: "utf-8", 7: "mule-internal",
	8: "latin1", 9: "latin1", 10: "latin1",
	11: "latin1", 12: "latin1", 13: "latin1", 14: "latin1", 15: "latin1",
	16: "latin1", 17: "latin1", 18: "latin1", 19: "latin1", 20: "latin1",
	21: "latin1", 22: "latin1", 23: "latin1", 24: "latin1", 25: "latin1",
	26: "latin1", 27: "latin1", 28: "latin1", 29: "latin1", 30: "latin1",
	31: "latin1", 32: "latin1", 33: "latin1", 34: "latin1", 35: "gbk",
	36: "utf-8", 37: "gbk", 38: "gbk", 39: "gbk", 40: "gb18030",
	41: "latin1", 42: "utf-8", 43: "utf-8", 44: "utf-8", 45: "utf-8",
	46: "utf-8", 47: "utf-8", 48: "utf-8",
}

// ---------- 版本探测 ----------
func detectPgVersion(datadir string) int {
	d, _ := filepath.Abs(datadir)
	for range 6 {
		for _, name := range []string{"PG_VERSION", "SYS_VERSION"} {
			p := filepath.Join(d, name)
			if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
				data, err := os.ReadFile(p)
				if err != nil {
					return 0
				}
				ver := strings.TrimSpace(string(data))
				major := ver
				if i := strings.IndexByte(ver, '.'); i >= 0 {
					major = ver[:i]
				}
				if v, err := strconv.Atoi(major); err == nil {
					return v
				}
				return 0
			}
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return 0
}

func isKingbaseDatadir(datadir string) bool {
	d, _ := filepath.Abs(datadir)
	for range 6 {
		if _, err := os.Stat(filepath.Join(d, "SYS_VERSION")); err == nil {
			return true
		}
		if _, err := os.Stat(filepath.Join(d, "PG_VERSION")); err == nil {
			return false
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return false
}

// ---------- 布局常量 ----------
// pg_class 前 16 个用户列（PG11-16 一致）
var pgClassCols16 = []ColLen{
	{64, false, "c"}, // relname
	{4, false, "i"},  // relnamespace
	{4, false, "i"},  // reltype
	{4, false, "i"},  // reloftype
	{4, false, "i"},  // relowner
	{4, false, "i"},  // relam
	{4, false, "i"},  // relfilenode
	{4, false, "i"},  // reltablespace
	{4, false, "i"},  // relpages
	{4, false, "i"},  // reltuples (float4!)
	{4, false, "i"},  // relallvisible
	{4, false, "i"},  // reltoastrelid
	{1, false, "c"},  // relhasindex
	{1, false, "c"},  // relisshared
	{1, false, "c"},  // relpersistence
	{1, false, "c"},  // relkind
}

// PG18+：relallvisible 后新增 relallfrozen(int4)，relkind 顺延
var pgClassCols18 = append(
	append(append([]ColLen{}, pgClassCols16[:11]...), ColLen{4, false, "i"}),
	pgClassCols16[11:]...)

// relnatts(int2)：表当前最大有效 attnum（金仓/PG 12+ 在 relkind 之后）
var pgClassCols16WithNatts = append(append([]ColLen{}, pgClassCols16...), ColLen{2, false, "s"})
var pgClassCols18WithNatts = append(append([]ColLen{}, pgClassCols18...), ColLen{2, false, "s"})

var pgNamespaceCols = []ColLen{
	{64, false, "c"}, // nspname
	{4, false, "i"},  // nspowner
}

func withOID(cols []ColLen) []ColLen {
	return append([]ColLen{{4, false, "i"}}, cols...)
}

// pg_attribute 全列布局（版本感知）
func pgAttributeLayout(version int, isKB bool) []ColLen {
	if isKB {
		fixed := []ColLen{
			{4, false, "i"}, {64, false, "c"}, {4, false, "i"}, // relid name typid
			{4, false, "i"}, {2, false, "s"}, {2, false, "s"}, // stattarg len num
			{4, false, "i"}, {4, false, "i"}, {4, false, "i"}, // ndims(int4!) cacheoff typmod
			{1, false, "c"}, {1, false, "c"}, {1, false, "c"}, // byval storage align
			{1, false, "c"}, {1, false, "c"}, {1, false, "c"}, // notnull hasdef hasmissing
			{1, false, "c"}, {1, false, "c"}, {1, false, "c"}, // identity generated isdropped
			{1, false, "c"}, {2, false, "s"}, {4, false, "i"}, // islocal inhcount collation
		}
		return append(fixed, []ColLen{{0, true, "i"}, {0, true, "i"}, {0, true, "i"}, {0, true, "i"}}...)
	}
	if version >= 18 {
		fixed := []ColLen{
			{4, false, "i"}, {64, false, "c"}, {4, false, "i"}, // relid name typid
			{2, false, "s"}, {2, false, "s"}, {4, false, "i"}, // len num typmod
			{2, false, "s"}, {1, false, "c"}, {1, false, "c"}, // ndims byval align
			{1, false, "c"}, {1, false, "c"}, {1, false, "c"}, // storage compression notnull
			{1, false, "c"}, {1, false, "c"}, {1, false, "c"}, // hasdef hasmissing identity
			{1, false, "c"}, {1, false, "c"}, {1, false, "c"}, // generated isdropped islocal
			{2, false, "s"}, {4, false, "i"}, {2, false, "s"}, // inhcount collation stattarg
		}
		return append(fixed, []ColLen{{0, true, "i"}, {0, true, "i"}, {0, true, "i"}, {0, true, "i"}}...)
	}
	if version == 17 {
		fixed := []ColLen{
			{4, false, "i"}, {64, false, "c"}, {4, false, "i"}, // relid name typid
			{2, false, "s"}, {2, false, "s"}, {4, false, "i"}, // len num cacheoff
			{4, false, "i"}, {2, false, "s"}, {1, false, "c"}, // typmod ndims byval
			{1, false, "c"}, {1, false, "c"}, {1, false, "c"}, // align storage compression
			{1, false, "c"}, {1, false, "c"}, {1, false, "c"}, // notnull hasdef hasmissing
			{1, false, "c"}, {1, false, "c"}, {1, false, "c"}, // identity generated isdropped
			{1, false, "c"}, {2, false, "s"}, {4, false, "i"}, // islocal inhcount collation
			{2, false, "s"}, // stattarg
		}
		return append(fixed, []ColLen{{0, true, "i"}, {0, true, "i"}, {0, true, "i"}, {0, true, "i"}}...)
	}
	if version >= 16 {
		fixed := []ColLen{
			{4, false, "i"}, {64, false, "c"}, {4, false, "i"}, // relid name typid
			{2, false, "s"}, {2, false, "s"}, {4, false, "i"}, // len num cacheoff
			{4, false, "i"}, {2, false, "s"}, {1, false, "c"}, // typmod ndims byval
			{1, false, "c"}, {1, false, "c"}, {1, false, "c"}, // align storage compression
			{1, false, "c"}, {1, false, "c"}, {1, false, "c"}, // notnull hasdef hasmissing
			{1, false, "c"}, {1, false, "c"}, {1, false, "c"}, // identity generated isdropped
			{1, false, "c"}, {2, false, "s"}, {2, false, "s"}, // islocal inhcount stattarg
			{4, false, "i"}, // collation
		}
		return append(fixed, []ColLen{{0, true, "i"}, {0, true, "i"}, {0, true, "i"}, {0, true, "i"}}...)
	}
	if version >= 14 {
		fixed := []ColLen{
			{4, false, "i"}, {64, false, "c"}, {4, false, "i"}, // relid name typid
			{4, false, "i"}, {2, false, "s"}, {2, false, "s"}, // stattarg len num
			{2, false, "s"}, {4, false, "i"}, {4, false, "i"}, // ndims cacheoff typmod
			{1, false, "c"}, {1, false, "c"}, {1, false, "c"}, // byval align storage
			{1, false, "c"}, {1, false, "c"}, {1, false, "c"}, // compression notnull hasdef
			{1, false, "c"}, {1, false, "c"}, {1, false, "c"}, // hasmissing identity generated
			{1, false, "c"}, {1, false, "c"}, {4, false, "i"}, // isdropped islocal inhcount(int4!)
			{4, false, "i"}, // collation
		}
		return append(fixed, []ColLen{{0, true, "i"}, {0, true, "i"}, {0, true, "i"}, {0, true, "i"}}...)
	}
	// version >= 12（PG12/13）
	fixed := []ColLen{
		{4, false, "i"}, {64, false, "c"}, {4, false, "i"}, // relid name typid
		{4, false, "i"}, {2, false, "s"}, {2, false, "s"}, // stattarg len num
		{2, false, "s"}, {4, false, "i"}, {4, false, "i"}, // ndims cacheoff typmod
		{1, false, "c"}, {1, false, "c"}, {1, false, "c"}, // byval storage align
		{1, false, "c"}, {1, false, "c"}, {1, false, "c"}, // notnull hasdef hasmissing
		{1, false, "c"}, {1, false, "c"}, {1, false, "c"}, // identity generated isdropped
		{1, false, "c"}, {2, false, "s"}, {4, false, "i"}, // islocal inhcount(int4)
		{4, false, "i"}, // collation
	}
	return append(fixed, []ColLen{{0, true, "i"}, {0, true, "i"}, {0, true, "i"}, {0, true, "i"}}...)
}

// 字段索引（版本感知）
func attrIdx(version int, isKB bool) map[string]int {
	if isKB {
		return map[string]int{"attrelid": 0, "attname": 1, "atttypid": 2, "attstattarget": 3, "attlen": 4, "attnum": 5, "attndims": 6, "attcacheoff": 7, "atttypmod": 8, "attbyval": 9, "attstorage": 10, "attalign": 11, "attnotnull": 12, "attisdropped": 17, "attcollation": 20, "attgenerated": 16}
	}
	if version >= 18 {
		return map[string]int{"attrelid": 0, "attname": 1, "atttypid": 2, "attlen": 3, "attnum": 4, "atttypmod": 5, "attndims": 6, "attbyval": 7, "attalign": 8, "attstorage": 9, "attnotnull": 11, "attisdropped": 16, "attstattarget": 20, "attcollation": 19, "attgenerated": 15}
	}
	if version == 17 {
		return map[string]int{"attrelid": 0, "attname": 1, "atttypid": 2, "attlen": 3, "attnum": 4, "attcacheoff": 5, "atttypmod": 6, "attndims": 7, "attbyval": 8, "attalign": 9, "attstorage": 10, "attnotnull": 12, "attisdropped": 17, "attstattarget": 21, "attcollation": 20, "attgenerated": 16}
	}
	if version >= 16 {
		return map[string]int{"attrelid": 0, "attname": 1, "atttypid": 2, "attlen": 3, "attnum": 4, "attcacheoff": 5, "atttypmod": 6, "attndims": 7, "attbyval": 8, "attalign": 9, "attstorage": 10, "attnotnull": 12, "attisdropped": 17, "attstattarget": 20, "attcollation": 21, "attgenerated": 16}
	}
	if version >= 14 {
		return map[string]int{"attrelid": 0, "attname": 1, "atttypid": 2, "attstattarget": 3, "attlen": 4, "attnum": 5, "attndims": 6, "attcacheoff": 7, "atttypmod": 8, "attbyval": 9, "attalign": 10, "attstorage": 11, "attnotnull": 13, "attisdropped": 18, "attcollation": 21, "attgenerated": 17}
	}
	return map[string]int{"attrelid": 0, "attname": 1, "atttypid": 2, "attstattarget": 3, "attlen": 4, "attnum": 5, "attndims": 6, "attcacheoff": 7, "atttypmod": 8, "attbyval": 9, "attstorage": 10, "attalign": 11, "attnotnull": 12, "attisdropped": 17, "attcollation": 20, "attgenerated": 16}
}

// ---------- 系统表元组遍历 ----------
// v1.0.49 修复（E1）：由 chan 改为 range-over-func（Go 1.23）。
// 原 chan 版本在消费者提前 break 时（如 OID 查找路径只取第一条），
// 生产者 goroutine 永久阻塞在 out <- 上，泄漏 goroutine + 文件句柄；
// 且 2019-2027 索引查找 O(N²) 调用点反复触发。改用 yield 后，
// break 即返回 false，遍历立即退出并释放 fd。签名对 `for range` 调用点完全兼容。
func iterSysTuples(path string, pgVersion int, isKB bool) func(yield func(*HeapTuple) bool) {
	return func(yield func(*HeapTuple) bool) {
		f, err := os.Open(path)
		if err != nil {
			return
		}
		defer f.Close()
		fi, _ := f.Stat()
		ps := 8192
		buf := make([]byte, 130)
		if n, _ := f.Read(buf); n >= 130 {
			ps = detectPageSize(buf[:n])
		}
		if ps == 0 {
			if fi.Size() > 0 && fi.Size() <= 32768 {
				ps = int(fi.Size())
			} else {
				ps = 8192
			}
		}
		npages := int(fi.Size()) / ps
		pageBuf := make([]byte, ps)
		f.Seek(0, 0)
		for pageno := 0; pageno < npages; pageno++ {
			n, err := f.Read(pageBuf)
			if err != nil || n < ps {
				break
			}
			lay := tryStandardLayout(pageBuf, ps)
			if lay == nil {
				lay = tryAutoLayout(pageBuf, ps)
			}
			if lay == nil {
				continue
			}
			lower := int(lay.Lower)
			for i := 0; lay.HeaderEnd+4*i+4 <= lower; i++ {
				rawID := uint32(u16(pageBuf, lay.HeaderEnd+4*i)) | uint32(u16(pageBuf, lay.HeaderEnd+4*i+2))<<16
				off := int(rawID & 0x7FFF)
				flags := (rawID >> 15) & 0x03
				ln := int((rawID >> 17) & 0x7FFF)
				if flags != ITEMID_NORMAL || off >= ps || ln > ps || off+ln > ps {
					continue
				}
				tup := parseTuple(append([]byte(nil), pageBuf[off:off+ln]...), pgVersion)
				if tup == nil || !tup.isLive() {
					continue
				}
				if !yield(tup) {
					return
				}
			}
		}
	}
}

// ---------- 系统表行解析 ----------
type AttrRow struct {
	AttRelID     uint32
	AttName      string
	AttTypID     uint32
	AttLen       int
	AttNum       int
	AttTypMod    int
	AttByVal     bool
	AttAlign     string
	AttStorage   string
	AttNotNull   bool
	AttIsDropped bool
	AttGenerated string // '' 普通；'s'=存储生成列（PG12+，金仓 V9 同布局；V8 无此列恒空）
	TXmin        uint32
}

// ---- pg_index 主键解析（自动发现 DDL 主键，PG12-18/金仓同源布局）----
// pg_index 前 14 列定长（indexrelid/indrelid/indnatts/indnkeyatts + 10 个 bool），
// indkey（int2vector）为第 15 列。PG12-18 与金仓同源布局（PG15+ 新增列在 varlen 区，不影响偏移）。
var pgIndexCols = []ColLen{
	{4, false, "i"}, {4, false, "i"}, {2, false, "s"}, {2, false, "s"},
	{1, false, "c"}, {1, false, "c"}, {1, false, "c"}, {1, false, "c"},
	{1, false, "c"}, {1, false, "c"}, {1, false, "c"}, {1, false, "c"},
	{1, false, "c"}, {1, false, "c"},
	{-1, true, "i"}, // indkey (int2vector, 第 15 列)
}

type IndexRow struct {
	IndRelID      uint32 // indrelid（父表 OID）
	IndIndexRelID uint32 // indexrelid（索引自身 OID）
	IndIsPrimary  bool
	IndIsUnique   bool
	IndKey        []int
}

// indexIndkey：解析 int2vector 内容为 attnum 列表。
// int2vector 磁盘格式与 ArrayType 匹配（PG c.h）：vl_len_(4B varlena 头) +
// ndim(4) + dataoffset(4) + elemtype(4) + dim1(4) + lbound1(4) + int16 values[dim1]。
func indexIndkey(f *[]byte) []int {
	if f == nil || len(*f) < 1 {
		return nil
	}
	b := *f
	var content []byte
	if len(b) >= 4 && b[0]&1 == 0 {
		content = b[4:] // 4B varlena 头（vl_len_）
	} else {
		if len(b) < 2 {
			return nil
		}
		content = b[1:] // 1B varlena 头
	}
	if len(content) < 20 {
		return nil
	}
	dim1 := int(binary.LittleEndian.Uint32(content[12:16]))
	if dim1 < 0 || dim1 > 32 {
		return nil
	}
	vals := content[20:]
	atts := make([]int, 0, dim1)
	for i := 0; i < dim1 && i*2+2 <= len(vals); i++ {
		// 0 = 表达式键位（indexprs 顺序对应），必须保留位置；
		// 过滤会破坏 [0]/[0,0]/[2,0] 等表达式/混合索引的键序（v1.0.51 修复）
		atts = append(atts, int(int16(binary.LittleEndian.Uint16(vals[i*2:]))))
	}
	return atts
}

// indexFields：解析 pg_index/sys_index 行，提取 indrelid、indisprimary、indkey。
// 布局差异（自动兼容）：
//
//	PG12-18：indnatts@8、indnkeyatts@10、10 个 bool@12-21、indkey@24；
//	金仓 V9（在 10 个 bool 后追加列）：indkey@26；
//	金仓 V8（PG9.6/10 内核，无 indnkeyatts）：indisprimary@11、indkey@20。
//
// indkey 采用数据区 varlena 定位法，不依赖列布局，天然兼容各版本偏移。
func indexFields(tup *HeapTuple, version int, isKB bool) *IndexRow {
	raw := tup.Raw
	hoff := tup.THoff
	if hoff+8 > len(raw) {
		return nil
	}
	ir := &IndexRow{}
	ir.IndIndexRelID = u32(raw, hoff+0) // pg_index 第 1 列 = indexrelid（无 OID 列）
	ir.IndRelID = u32(raw, hoff+4)      // 第 2 列 = indrelid（父表）
	// indisprimary 定位：用 indnkeyatts（PG11+）的取值判断布局
	// （PG10- 的 @hoff+10/11 是 indisunique/indisprimary 两个 bool，值组合不会 ≤32 且 ≤indnatts）
	// PG15+ 在 indisunique 后插入了 indnullsnotdistinct，indisprimary 由 @13 移到 @14；
	// 金仓 V8/V9 均无 indnullsnotdistinct（V9 实测 indisprimary 仍在 @13）。
	if hoff+15 <= len(raw) {
		natts := u16(raw, hoff+8)
		nkey := u16(raw, hoff+10)
		if nkey <= 32 && nkey <= natts {
			ir.IndIsUnique = raw[hoff+12] != 0 // PG11+：indisunique 恒定在 @12
			if !isKB && version >= 15 {
				ir.IndIsPrimary = raw[hoff+14] != 0 // PG15+（indnullsnotdistinct 占 @13）
			} else {
				ir.IndIsPrimary = raw[hoff+13] != 0 // PG11-14 / 金仓 V9
			}
		} else if hoff+12 <= len(raw) {
			ir.IndIsUnique = raw[hoff+10] != 0 // PG10 及更早
			ir.IndIsPrimary = raw[hoff+11] != 0
		}
	}
	// indkey：定位数据区内第一个合法 varlena（跳过前 14 列范围）
	pos := locateIndkey(raw, hoff)
	if pos >= 0 && hoff+pos+4 <= len(raw) {
		kind, total, _, _ := varlenaParse(raw, hoff+pos)
		if kind != "" && total >= 4 && hoff+pos+total <= len(raw) {
			f := raw[hoff+pos : hoff+pos+total]
			ir.IndKey = indexIndkey(&f)
		}
	}
	return ir
}

// locateIndkey：在数据区 [18,34) 范围内定位第一个合法 varlena 头（indkey 起始偏移，相对数据区）。
// 该范围覆盖：PG12-18 @24、金仓 V9 @26、金仓 V8 @20/@22。
// bool 区（值 0/1）与 padding（0x00）均不会产生合法 varlena 头（0x00/0x01 total=0 非法），
// 因此首个合法头即 indkey。
func locateIndkey(raw []byte, hoff int) int {
	start := hoff + 18
	end := hoff + 34
	if end > len(raw) {
		end = len(raw)
	}
	for off := start; off+4 <= end; off++ {
		kind, total, _, _ := varlenaParse(raw, off)
		if kind != "" && total >= 4 && off+total <= len(raw) {
			return off - hoff
		}
	}
	return -1
}

func attrFields(tup *HeapTuple, version int, isKB bool) *AttrRow {
	layout := pgAttributeLayout(version, isKB)
	nulls := tup.getNulls()
	fields := extractFieldsDirect(tup.Raw, tup.THoff, nulls, layout)
	if len(fields) < 9 {
		return nil
	}
	idx := attrIdx(version, isKB)
	// 语义校验 attalign/attstorage
	aalign := fields[idx["attalign"]]
	astorage := fields[idx["attstorage"]]
	if aalign == nil || len(*aalign) < 1 {
		return nil
	}
	ac := (*aalign)[0]
	if ac != 'c' && ac != 's' && ac != 'i' && ac != 'd' {
		return nil
	}
	if astorage == nil || len(*astorage) < 1 {
		return nil
	}
	sc := (*astorage)[0]
	if sc != 'p' && sc != 'e' && sc != 'm' && sc != 'x' {
		return nil
	}
	u32f := func(i int) uint32 {
		f := fields[i]
		if f != nil && len(*f) >= 4 {
			return binary.LittleEndian.Uint32(*f)
		}
		return 0
	}
	i16f := func(i int) int {
		f := fields[i]
		if f != nil && len(*f) >= 2 {
			return int(int16(binary.LittleEndian.Uint16(*f)))
		}
		return 0
	}
	i32f := func(i int) int {
		f := fields[i]
		if f != nil && len(*f) >= 4 {
			return int(int32(binary.LittleEndian.Uint32(*f)))
		}
		return -1
	}
	c1f := func(i int) string {
		f := fields[i]
		if f != nil && len(*f) >= 1 {
			return string((*f)[0])
		}
		return ""
	}
	b1f := func(i int) bool {
		f := fields[i]
		if f == nil || len(*f) == 0 {
			return false
		}
		v := (*f)[0]
		return v == 1 || v == 0x74 || v == 't'
	}
	aname := fields[idx["attname"]]
	name := ""
	if aname != nil {
		name = cstring(*aname, 0)
	}
	tx := uint32(0)
	if len(tup.Raw) >= 4 {
		tx = binary.LittleEndian.Uint32(tup.Raw[0:4])
	}
	return &AttrRow{
		AttRelID:     u32f(idx["attrelid"]),
		AttName:      name,
		AttTypID:     u32f(idx["atttypid"]),
		AttLen:       i16f(idx["attlen"]),
		AttNum:       i16f(idx["attnum"]),
		AttTypMod:    i32f(idx["atttypmod"]),
		AttByVal:     b1f(idx["attbyval"]),
		AttAlign:     c1f(idx["attalign"]),
		AttStorage:   c1f(idx["attstorage"]),
		AttNotNull:   b1f(idx["attnotnull"]),
		AttIsDropped: b1f(idx["attisdropped"]),
		AttGenerated: func() string {
			// 仅 's'（存储生成列）有效；普通列该字节为 '\x00'，归一化为空串
			if g := c1f(idx["attgenerated"]); g == "s" {
				return g
			}
			return ""
		}(),
		TXmin: tx,
	}
}

type ClassRow struct {
	OID          uint32
	RelName      string
	RelNamespace uint32
	RelFileNode  uint32
	RelKind      string
	ToastRelID   uint32
	RelNatts     int
	RelAm        uint32
	TableSpace   uint32 // pg_class.reltablespace：0=默认表空间(base/)，非 0=pg_tblspc/<oid>（v1.0.34）
	RelShared    bool   // relisshared：共享系统表（文件在 global/）
	// v1.0.53 完整性自证：relpages/reltuples（float4）——上次 VACUUM/ANALYZE 记录值，
	// 用于与实测页数/行数对照，检出段文件缺失与静默少导。
	RelPages  int32
	RelTuples float32
}

// tableLayout 动态探测系统目录表的列布局（从 sys_attribute 读取真实列定义）。
// 金仓 V9R1C10 的 sys_description(2609) 在 objsubid 后新增 name(attlen=64) 列，
// description 由标准第 4 列变为第 5 列；PG 及金仓 V8/V9R3C18 为标准 4 列。
// 动态探测按真实列布局解析，避免硬编码版本号。
func tableLayout(dbDir string, relOID uint32, version int, isKB bool) []ColLen {
	p := detectSysFile(dbDir, 1249, map[string]bool{"pg_attribute": true, "sys_attribute": true}, 0)
	if p == "" {
		return nil
	}
	type acol struct {
		num int
		cl  ColLen
	}
	var cols []acol
	for tup := range iterSysTuples(p, version, isKB) {
		ar := attrFields(tup, version, isKB)
		if ar == nil || ar.AttRelID != relOID || ar.AttNum <= 0 || ar.AttIsDropped {
			continue
		}
		cl := ColLen{}
		if ar.AttLen > 0 {
			cl.AttLen = ar.AttLen
			cl.IsVarlena = false
			cl.AttAlign = ar.AttAlign
		} else {
			cl.AttLen = 0
			cl.IsVarlena = true
			cl.AttAlign = "i"
		}
		cols = append(cols, acol{ar.AttNum, cl})
	}
	if len(cols) == 0 {
		return nil
	}
	sort.Slice(cols, func(i, j int) bool { return cols[i].num < cols[j].num })
	out := make([]ColLen, len(cols))
	for i, c := range cols {
		out[i] = c.cl
	}
	return out
}

func classFields(tup *HeapTuple, version int) *ClassRow {
	var layout []ColLen
	if version >= 18 {
		layout = withOID(pgClassCols18WithNatts)
	} else if version >= 12 {
		layout = withOID(pgClassCols16WithNatts)
	} else {
		layout = pgClassCols16
	}
	nulls := tup.getNulls()
	fields := extractFieldsDirect(tup.Raw, tup.THoff, nulls, layout)
	if len(fields) < 8 {
		return nil
	}
	var oidF *[]byte
	var relnameF, relnamespaceF, relfilenodeF, relkindF, reltoastF, relamF *[]byte
	var reltablespaceF, relsharedF *[]byte
	if version >= 12 {
		oidF = fields[0]
		relnameF = fields[1]
		relnamespaceF = fields[2]
		relamF = fields[6]
		relfilenodeF = fields[7]
		if len(fields) > 8 {
			reltablespaceF = fields[8]
		}
		if version >= 18 {
			if len(fields) > 17 {
				relkindF = fields[17]
			}
			if len(fields) > 13 {
				reltoastF = fields[13]
			}
			if len(fields) > 15 {
				relsharedF = fields[15]
			}
		} else {
			if len(fields) > 16 {
				relkindF = fields[16]
			}
			if len(fields) > 12 {
				reltoastF = fields[12]
			}
			if len(fields) > 14 {
				relsharedF = fields[14]
			}
		}
	} else {
		relnameF = fields[0]
		relnamespaceF = fields[1]
		relfilenodeF = fields[6]
		if len(fields) > 15 {
			relkindF = fields[15]
		}
	}
	if relnameF == nil {
		return nil
	}
	relname := cstring(*relnameF, 0)
	relnamespace := uint32(0)
	if relnamespaceF != nil && len(*relnamespaceF) >= 4 {
		relnamespace = binary.LittleEndian.Uint32(*relnamespaceF)
	}
	relfilenode := uint32(0)
	if relfilenodeF != nil && len(*relfilenodeF) >= 4 {
		relfilenode = binary.LittleEndian.Uint32(*relfilenodeF)
	}
	var oid uint32
	if version >= 12 {
		if oidF != nil && len(*oidF) >= 4 {
			oid = binary.LittleEndian.Uint32(*oidF)
		}
	} else {
		oid = 0 // PG<=11 OID 在 t_hoff-4（本工具最低支持 12）
	}
	if relfilenode == 0 {
		relfilenode = oid
	}
	relkind := "r"
	if relkindF != nil && len(*relkindF) >= 1 {
		relkind = string((*relkindF)[0])
	}
	relam := uint32(0)
	if relamF != nil && len(*relamF) >= 4 {
		relam = binary.LittleEndian.Uint32(*relamF)
	}
	toastRelID := uint32(0)
	if reltoastF != nil && len(*reltoastF) >= 4 {
		toastRelID = binary.LittleEndian.Uint32(*reltoastF)
	}
	natts := 0
	if version >= 18 {
		// B5 修复：PG18+ 布局新增 relallfrozen，relkind=fields[17]、relnatts=fields[18]；
		// 原实现两分支共用 fields[17]（=relkind，1 字节），len>=2 检查失败 → natts 恒 0，
		// 幽灵列过滤（maxRowNatts/relNatts）失效
		if len(fields) > 18 {
			if f := fields[18]; f != nil && len(*f) >= 2 {
				natts = int(int16(binary.LittleEndian.Uint16(*f)))
			}
		}
	} else if version >= 12 {
		if len(fields) > 17 {
			if f := fields[17]; f != nil && len(*f) >= 2 {
				natts = int(int16(binary.LittleEndian.Uint16(*f)))
			}
		}
	}
	tablespace := uint32(0)
	if reltablespaceF != nil && len(*reltablespaceF) >= 4 {
		tablespace = binary.LittleEndian.Uint32(*reltablespaceF)
	}
	relShared := false
	if relsharedF != nil && len(*relsharedF) >= 1 {
		relShared = (*relsharedF)[0] != 0
	}
	// v1.0.53：relpages(int4)/reltuples(float4) —— 布局内位置随是否带 oid 系统列偏移
	var relPagesF, relTuplesF *[]byte
	if version >= 12 {
		if len(fields) > 9 {
			relPagesF = fields[9]
		}
		if len(fields) > 10 {
			relTuplesF = fields[10]
		}
	} else {
		if len(fields) > 8 {
			relPagesF = fields[8]
		}
		if len(fields) > 9 {
			relTuplesF = fields[9]
		}
	}
	relPages := int32(0)
	if relPagesF != nil && len(*relPagesF) >= 4 {
		relPages = int32(binary.LittleEndian.Uint32(*relPagesF))
	}
	relTuples := float32(-1)
	if relTuplesF != nil && len(*relTuplesF) >= 4 {
		relTuples = math.Float32frombits(binary.LittleEndian.Uint32(*relTuplesF))
	}
	return &ClassRow{
		OID: oid, RelName: relname, RelNamespace: relnamespace,
		RelFileNode: relfilenode, RelKind: relkind, ToastRelID: toastRelID,
		RelNatts: natts, RelAm: relam, TableSpace: tablespace, RelShared: relShared,
		RelPages: relPages, RelTuples: relTuples,
	}
}

func nsFields(tup *HeapTuple, version int) (uint32, string) {
	layout := withOID(pgNamespaceCols)
	nulls := tup.getNulls()
	fields := extractFieldsDirect(tup.Raw, tup.THoff, nulls, layout)
	if len(fields) < 2 {
		return 0, ""
	}
	var oid uint32
	if fields[0] != nil && len(*fields[0]) >= 4 {
		oid = binary.LittleEndian.Uint32(*fields[0])
	}
	name := ""
	if fields[1] != nil {
		name = cstring(*fields[1], 0)
	}
	return oid, name
}

// ---------- 系统文件探测 ----------
func detectSysFile(directory string, standardOID int, knownNames map[string]bool, pageSize int) string {
	if pageSize <= 0 {
		pageSize = 8192 // v1.0.44：调用方未传页大小时用默认，避免候选文件扫描时 0 长度 buf 越界 panic
	}
	stdPath := filepath.Join(directory, strconv.Itoa(standardOID))
	if fi, err := os.Stat(stdPath); err == nil && fi.Mode().IsRegular() {
		return stdPath
	}
	if fi, err := os.Stat(directory); err != nil || !fi.IsDir() {
		return ""
	}
	entries, _ := os.ReadDir(directory)
	names := make([]string, 0)
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		if strings.HasSuffix(name, "_fsm") || strings.HasSuffix(name, "_vm") || strings.HasSuffix(name, "_init") {
			continue
		}
		if _, err := strconv.Atoi(name); err != nil {
			continue
		}
		fpath := filepath.Join(directory, name)
		fi, err := os.Stat(fpath)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() < int64(pageSize) {
			continue
		}
		// 内容检查：前 2 页，第一列 name 是否含已知名称
		f, err := os.Open(fpath)
		if err != nil {
			continue
		}
		match, checked := 0, 0
		buf := make([]byte, pageSize)
		for range 2 {
			n, _ := f.Read(buf)
			if n < pageSize {
				break
			}
			lay := tryStandardLayout(buf, pageSize)
			if lay == nil {
				lay = tryAutoLayout(buf, pageSize)
			}
			if lay == nil {
				continue
			}
			lower := int(lay.Lower)
			for i := 0; lay.HeaderEnd+4*i+4 <= lower; i++ {
				rawID := uint32(u16(buf, lay.HeaderEnd+4*i)) | uint32(u16(buf, lay.HeaderEnd+4*i+2))<<16
				off := int(rawID & 0x7FFF)
				flags := (rawID >> 15) & 0x03
				ln := int((rawID >> 17) & 0x7FFF)
				if flags != ITEMID_NORMAL || off >= pageSize || ln > pageSize || off+ln > pageSize {
					continue
				}
				tup := parseTuple(buf[off:off+ln], 12)
				if tup == nil || !tup.isLive() {
					continue
				}
				nulls := tup.getNulls()
				fields := extractFieldsDirect(tup.Raw, tup.THoff, nulls, []ColLen{{64, false, "c"}})
				if len(fields) == 0 || fields[0] == nil {
					continue
				}
				nm := cstring(*fields[0], 0)
				checked++
				if knownNames[nm] {
					match++
				}
				if match >= 2 {
					f.Close()
					return fpath
				}
				if checked > 50 {
					break
				}
			}
		}
		f.Close()
	}
	return ""
}

func detectPgAttribute(directory string, standardOID int, targetOID uint32, pageSize int) string {
	stdPath := filepath.Join(directory, strconv.Itoa(standardOID))
	if fi, err := os.Stat(stdPath); err == nil && fi.Mode().IsRegular() {
		return stdPath
	}
	if fi, err := os.Stat(directory); err != nil || !fi.IsDir() {
		return ""
	}
	entries, _ := os.ReadDir(directory)
	names := make([]string, 0)
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		if strings.HasSuffix(name, "_fsm") || strings.HasSuffix(name, "_vm") || strings.HasSuffix(name, "_init") {
			continue
		}
		if _, err := strconv.Atoi(name); err != nil {
			continue
		}
		fpath := filepath.Join(directory, name)
		fi, err := os.Stat(fpath)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() < int64(pageSize) {
			continue
		}
		f, err := os.Open(fpath)
		if err != nil {
			continue
		}
		count := 0
		buf := make([]byte, pageSize)
		n, _ := f.Read(buf)
		if n >= pageSize {
			lay := tryStandardLayout(buf, pageSize)
			if lay == nil {
				lay = tryAutoLayout(buf, pageSize)
			}
			if lay != nil {
				lower := int(lay.Lower)
				for i := 0; lay.HeaderEnd+4*i+4 <= lower; i++ {
					rawID := uint32(u16(buf, lay.HeaderEnd+4*i)) | uint32(u16(buf, lay.HeaderEnd+4*i+2))<<16
					off := int(rawID & 0x7FFF)
					flags := (rawID >> 15) & 0x03
					ln := int((rawID >> 17) & 0x7FFF)
					if flags != ITEMID_NORMAL || off >= pageSize || ln > pageSize || off+ln > pageSize {
						continue
					}
					tup := parseTuple(buf[off:off+ln], 12)
					if tup == nil || !tup.isLive() {
						continue
					}
					nulls := tup.getNulls()
					fields := extractFieldsDirect(tup.Raw, tup.THoff, nulls, []ColLen{{4, false, "i"}, {64, false, "c"}})
					if len(fields) < 2 {
						continue
					}
					if fields[0] != nil && len(*fields[0]) >= 4 {
						attrelid := binary.LittleEndian.Uint32(*fields[0])
						if fields[1] != nil {
							_ = cstring(*fields[1], 0)
							if attrelid == targetOID || count >= 5 {
								f.Close()
								return fpath
							}
						}
						count++
					}
					if count > 100 {
						break
					}
				}
			}
		}
		f.Close()
	}
	return ""
}

func probePageSize(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 8192
	}
	defer f.Close()
	buf := make([]byte, 130)
	n, _ := f.Read(buf)
	if n < 130 {
		fi, _ := f.Stat()
		if fi.Size() > 0 && fi.Size() <= 32768 {
			return int(fi.Size())
		}
		return 8192
	}
	ps := detectPageSize(buf)
	if ps > 0 {
		return ps
	}
	fi, _ := f.Stat()
	if fi.Size() > 0 && fi.Size() <= 32768 {
		return int(fi.Size())
	}
	return 8192
}

func probeDirPageSize(dbDir string) int {
	for _, oid := range []int{1249, 1259, 1247, 1260, 2615, 1262} {
		p := filepath.Join(dbDir, strconv.Itoa(oid))
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Size() >= 8192 {
			return probePageSize(p)
		}
	}
	return 8192
}

// ---------- 类型/角色/枚举映射 ----------
// ---------- 方案 C：sys_type 动态类型发现（v1.0.16）----------
// 原理：读取数据库目录内 pg_type/sys_type（relfilenode 1247）与 pg_proc/sys_proc（1255），
// 按 typinput 函数名 / typbasetype 递归 / typelem 自指，把"未知 oid"动态映射到解码器族。
// 硬编码 decoders 表优先（兜底），目录发现失败时不影响原有功能。
// pg_type/sys_type 布局（版本感知）：
//
//	PG12-17（金仓同源，实测 V8/V9 一致）：
//	  oid(4) typname(64) typnamespace(4) typowner(4) typlen(2) typbyval(1) typtype(1)
//	  typcategory(1) typispreferred(1) typisdefined(1) typdelim(1) typrelid(4)
//	  typelem(4) typarray(4) typinput(4) typoutput(4) typreceive(4) typsend(4)
//	  typmodin(4) typmodout(4) typanalyze(4) typalign(1) typstorage(1) typnotnull(1)
//	  typbasetype(4)
//	PG18+：typrelid 后新增 typsubscript(4B)，后续列顺延（typelem→13/typarray→14/typinput→15/typalign→22/typbasetype→25）
//
// pgTypeHasSubscript：pg_type 自 PG14 起在 typrelid 之后新增 typsubscript 列，
// 使其后的 typelem/typarray/typinput/... 以及 typalign/typbasetype 全部后移一位。
// 金仓 V8/V9 基于 PG12，没有该列。
//
// v1.0.48 修复（P1-F1 深层根因）：原实现只在 PG18+ 处理该偏移，PG14/15/16/17
// 全部错位——实测 PG17 上 `_mood` 读到 Elem=6179（实为 typsubscript 的值
// array_subscript_handler），Array=16866（实为 typelem）。后果是
// dynamicDecoders 三档（base/domain/array）在 PG14-17 上一个都注册不上，
// `--verbose` 打印"动态类型映射: 0 个"。
func pgTypeHasSubscript(version int, isKB bool) bool { return !isKB && version >= 14 }

func pgTypeLayout(version int, isKB bool) []ColLen {
	pre := []ColLen{
		{4, false, "i"}, {64, false, "c"}, {4, false, "i"}, {4, false, "i"},
		{2, false, "s"}, {1, false, "c"}, {1, false, "c"}, {1, false, "c"},
		{1, false, "c"}, {1, false, "c"}, {1, false, "c"}, {4, false, "i"},
	}
	if pgTypeHasSubscript(version, isKB) {
		pre = append(pre, ColLen{4, false, "i"}) // typsubscript（PG14+）
	}
	mid := []ColLen{
		{4, false, "i"}, // typelem
		{4, false, "i"}, // typarray
		{4, false, "i"}, // typinput
		{4, false, "i"}, // typoutput
		{4, false, "i"}, // typreceive
		{4, false, "i"}, // typsend
		{4, false, "i"}, // typmodin
		{4, false, "i"}, // typmodout
		{4, false, "i"}, // typanalyze
		{1, false, "c"}, // typalign
		{1, false, "c"}, // typstorage
		{1, false, "c"}, // typnotnull
		{4, false, "i"}, // typbasetype
	}
	return append(pre, mid...)
}

// TypeDef：pg_type 行的关键字段（方案 C 动态发现）
type TypeDef struct {
	OID      uint32
	Name     string
	Nsp      uint32 // typnamespace（v1.0.48：DDL 需按真实 schema 限定类型名）
	Len      int
	ByVal    bool
	Type     byte // b/d/e/c/p/r/m
	Category byte
	Elem     uint32 // typelem
	Array    uint32 // typarray
	Input    uint32 // typinput（pg_proc oid）
	Base     uint32 // typbasetype（domain）
	Align    byte   // typalign
	TypRelid uint32 // typrelid（复合类型对应的行类型 pg_class oid）
	NotNull  bool   // typnotnull（域类型 NOT NULL 约束）
}

// typeNspNames：pg_namespace oid → nspname（v1.0.48）。
// 供 DDL 把 CREATE TYPE/DOMAIN 限定到类型原本所在的 schema；
// 原实现不带限定，靠 search_path 落库，跨 schema 同名类型会静默绑错。
var typeNspNames map[uint32]string

// buildTypeNspMap 读 2615（pg_namespace）构建 oid → 名称映射。
func buildTypeNspMap(dbDir string, version int, isKB bool) map[uint32]string {
	m := map[uint32]string{}
	pageSize := probeDirPageSize(dbDir)
	nsPath := detectSysFile(dbDir, PG_NAMESPACE_RELFILE,
		map[string]bool{"public": true, "pg_catalog": true, "pg_toast": true,
			"information_schema": true, "sys": true}, pageSize)
	if nsPath == "" {
		return m
	}
	for tup := range iterSysTuples(nsPath, version, isKB) {
		oid, name := nsFields(tup, version)
		if oid != 0 && name != "" {
			m[oid] = name
		}
	}
	return m
}

// typeSchemaName 返回类型定义所在 schema 名；未知返回空串。
func typeSchemaName(oid uint32) string {
	if typeNspNames == nil || dynamicTypeDefs == nil {
		return ""
	}
	td, ok := dynamicTypeDefs[oid]
	if !ok || td.Nsp == 0 {
		return ""
	}
	return typeNspNames[td.Nsp]
}

// buildTypeDefs 读 1247 构建 oid→类型定义映射。
func buildTypeDefs(dbDir string, version int, isKB bool) map[uint32]*TypeDef {
	defs := map[uint32]*TypeDef{}
	path := filepath.Join(dbDir, strconv.Itoa(PG_TYPE_RELFILE))
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return defs
	}
	cols := pgTypeLayout(version, isKB)
	// typsubscript 偏移（PG14+，见 pgTypeHasSubscript）
	off := 0
	if pgTypeHasSubscript(version, isKB) {
		off = 1
	}
	bt := 24 + off // typbasetype 列号
	for tup := range iterSysTuples(path, version, isKB) {
		nulls := tup.getNulls()
		fields := extractFieldsDirect(tup.Raw, tup.THoff, nulls, cols)
		if len(fields) < 15+off {
			continue
		}
		td := &TypeDef{}
		if fields[0] != nil && len(*fields[0]) >= 4 {
			td.OID = binary.LittleEndian.Uint32(*fields[0])
		}
		if td.OID == 0 {
			continue
		}
		if fields[1] != nil {
			td.Name = cstring(*fields[1], 0)
		}
		// v1.0.48：typnamespace（pg_type 第 3 列，off 68）
		if fields[2] != nil && len(*fields[2]) >= 4 {
			td.Nsp = binary.LittleEndian.Uint32(*fields[2])
		}
		if fields[4] != nil && len(*fields[4]) >= 2 {
			td.Len = int(int16(binary.LittleEndian.Uint16(*fields[4])))
		}
		if fields[5] != nil && len(*fields[5]) >= 1 {
			td.ByVal = (*fields[5])[0] != 0
		}
		if fields[6] != nil && len(*fields[6]) >= 1 {
			td.Type = (*fields[6])[0]
		}
		if fields[7] != nil && len(*fields[7]) >= 1 {
			td.Category = (*fields[7])[0]
		}
		if fields[12+off] != nil && len(*fields[12+off]) >= 4 {
			td.Elem = binary.LittleEndian.Uint32(*fields[12+off])
		}
		if fields[13+off] != nil && len(*fields[13+off]) >= 4 {
			td.Array = binary.LittleEndian.Uint32(*fields[13+off])
		}
		if fields[14+off] != nil && len(*fields[14+off]) >= 4 {
			td.Input = binary.LittleEndian.Uint32(*fields[14+off])
		}
		if len(fields) > 11 && fields[11] != nil && len(*fields[11]) >= 4 {
			td.TypRelid = binary.LittleEndian.Uint32(*fields[11])
		}
		if len(fields) > 21+off && fields[21+off] != nil && len(*fields[21+off]) >= 1 {
			td.Align = (*fields[21+off])[0]
		}
		if len(fields) > bt && fields[bt] != nil && len(*fields[bt]) >= 4 {
			td.Base = binary.LittleEndian.Uint32(*fields[bt])
		}
		if len(fields) > bt-1 && fields[bt-1] != nil && len(*fields[bt-1]) >= 1 {
			td.NotNull = (*fields[bt-1])[0] != 0 // typnotnull（紧邻 typbasetype 之前）
		}
		defs[td.OID] = td
	}
	return defs
}

// buildProcNameMap 读 1255 构建函数 oid→函数名映射（解析 typinput）。
func buildProcNameMap(dbDir string, version int, isKB bool) map[uint32]string {
	procMap := map[uint32]string{}
	path := filepath.Join(dbDir, "1255")
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return procMap
	}
	for tup := range iterSysTuples(path, version, isKB) {
		nulls := tup.getNulls()
		fields := extractFieldsDirect(tup.Raw, tup.THoff, nulls, []ColLen{{4, false, "i"}, {64, false, "c"}})
		if len(fields) < 2 || fields[0] == nil || fields[1] == nil {
			continue
		}
		oid := binary.LittleEndian.Uint32(*fields[0])
		name := cstring(*fields[1], 0)
		if oid != 0 && name != "" {
			procMap[oid] = name
		}
	}
	return procMap
}

// buildTypeNameMap 读取 pg_type 目录前两列构建 oid→类型名映射（供 --verbose/报错提示）。
func buildTypeNameMap(dbDir string) map[uint32]string {
	typeMap := map[uint32]string{}
	path := filepath.Join(dbDir, "1247")
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return typeMap
	}
	version := detectPgVersion(dbDir)
	if version == 0 {
		version = 12
	}
	isKB := isKingbaseDatadir(dbDir)
	for tup := range iterSysTuples(path, version, isKB) {
		nulls := tup.getNulls()
		fields := extractFieldsDirect(tup.Raw, tup.THoff, nulls, []ColLen{{4, false, "i"}, {64, false, "c"}})
		if len(fields) < 2 || fields[0] == nil || fields[1] == nil {
			continue
		}
		oid := binary.LittleEndian.Uint32(*fields[0])
		name := cstring(*fields[1], 0)
		if oid != 0 && name != "" {
			typeMap[oid] = name
		}
	}
	if isKB {
		if _, ok := typeMap[4659]; ok {
			typeMap[4659] = "numeric"
		}
	}
	return typeMap
}

// buildRegclassNameMap：解析 pg_class，构建 oid → relname 反查表（regclass 输出用，v1.0.47）。
// 复用 detectSysFile 定位 + iterSysTuples 逐行 + classFields 解析，与 autoDiscover 同源。
func buildRegclassNameMap(dbDir string, version int, isKB bool) map[uint32]string {
	m := map[uint32]string{}
	pageSize := probeDirPageSize(dbDir)
	classPath := detectSysFile(dbDir, PG_CLASS_RELFILE,
		map[string]bool{"pg_class": true, "sys_class": true, "pg_type": true, "sys_type": true}, pageSize)
	if classPath == "" {
		return m
	}
	for tup := range iterSysTuples(classPath, version, isKB) {
		cr := classFields(tup, version)
		if cr != nil && cr.OID != 0 && cr.RelName != "" {
			m[cr.OID] = cr.RelName
		}
	}
	return m
}

// detectKbOracleMode：金仓 oracle 兼容模式离线探测。
// oracle 模式 kingbase.conf 独有 ora_* GUC（实测 V9R1C10：ora_input_emptystr_isnull /
// ora_integer_div_returnfloat），pg/mysql 模式与 PG 均无 → 以此判定。
// 与 parseConfTimezone 一致：从 dbDir 向上回溯最多 6 级找 kingbase.conf。
func detectKbOracleMode(dbDir string) bool {
	re := regexp.MustCompile(`(?m)^[ \t]*ora_(input_emptystr_isnull|integer_div_returnfloat)[ \t]*=`)
	d, _ := filepath.Abs(dbDir)
	for range 6 {
		data, err := os.ReadFile(filepath.Join(d, "kingbase.conf"))
		if err == nil && re.Match(data) {
			return true
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return false
}

func buildRoleNameMap(dbDir string) map[uint32]string {
	roleMap := map[uint32]string{}
	candidates := []string{
		filepath.Join(dbDir, "1260"),
		filepath.Join(filepath.Dir(filepath.Dir(dbDir)), "global", "1260"),
	}
	version := detectPgVersion(dbDir)
	if version == 0 {
		version = 12
	}
	isKB := isKingbaseDatadir(dbDir)
	for _, path := range candidates {
		if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		for tup := range iterSysTuples(path, version, isKB) {
			nulls := tup.getNulls()
			fields := extractFieldsDirect(tup.Raw, tup.THoff, nulls, []ColLen{{4, false, "i"}, {64, false, "c"}})
			if len(fields) < 2 || fields[0] == nil || fields[1] == nil {
				continue
			}
			oid := binary.LittleEndian.Uint32(*fields[0])
			name := cstring(*fields[1], 0)
			if oid != 0 && name != "" {
				roleMap[oid] = name
			}
		}
		if len(roleMap) > 0 {
			break
		}
	}
	return roleMap
}

func loadEnumMap(dbDir string, version int, isKB bool) {
	path := filepath.Join(dbDir, strconv.Itoa(PG_ENUM_RELFILE))
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		setEnumMap(map[uint32][]EnumMember{})
		return
	}
	em := map[uint32][]EnumMember{}
	// pg_enum: [oid 4B][enumtypid 4B][enumsortorder float4 4B][enumlabel]
	for tup := range iterSysTuples(path, version, isKB) {
		var layout []ColLen
		if isKB {
			layout = []ColLen{{4, false, "i"}, {4, false, "i"}, {4, false, "i"}, {0, true, "i"}}
		} else {
			layout = []ColLen{{4, false, "i"}, {4, false, "i"}, {4, false, "i"}, {64, false, "c"}}
		}
		nulls := tup.getNulls()
		fields := extractFieldsDirect(tup.Raw, tup.THoff, nulls, layout)
		if len(fields) < 4 || fields[0] == nil || fields[1] == nil || fields[3] == nil {
			continue
		}
		memberOID := binary.LittleEndian.Uint32(*fields[0])
		enumTypID := binary.LittleEndian.Uint32(*fields[1])
		label := ""
		if isKB {
			kind, total, _, _ := varlenaParse(*fields[3], 0)
			if kind == VARLENA_1B {
				label = decodeBytes((*fields[3])[1 : 1+total-1])
			} else if kind == VARLENA_4B {
				label = decodeBytes((*fields[3])[4 : 4+total-4])
			} else if kind == VARLENA_4BC {
				label = decodeBytes((*fields[3])[4 : 4+total-4])
			}
		} else {
			label = cstring(*fields[3], 0)
		}
		// 按 enumsortorder 有序插入（float4 小端）
		sortorder := float32(math.Float32frombits(binary.LittleEndian.Uint32(*fields[2])))
		em[enumTypID] = append(em[enumTypID], EnumMember{memberOID, label, sortorder})
	}
	for tid := range em {
		sort.Slice(em[tid], func(a, b int) bool { return em[tid][a].SortOrder < em[tid][b].SortOrder })
	}
	setEnumMap(em)
}

// ---------- 编码探测 ----------
func detectDatabaseEncoding(datadir string, dbOID int) string {
	if datadir == "" || dbOID == 0 {
		return ""
	}
	f := filepath.Join(datadir, "global", strconv.Itoa(PG_DATABASE_RELFILE))
	if fi, err := os.Stat(f); err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	version := detectPgVersion(datadir)
	if version == 0 {
		version = 16
	}
	isKB := isKingbaseDatadir(datadir)
	layout := withOID([]ColLen{{64, false, "c"}, {4, false, "i"}, {4, false, "i"}})
	for tup := range iterSysTuples(f, version, isKB) {
		nulls := tup.getNulls()
		fields := extractFieldsDirect(tup.Raw, tup.THoff, nulls, layout)
		if len(fields) < 4 || fields[0] == nil {
			continue
		}
		if binary.LittleEndian.Uint32(*fields[0]) != uint32(dbOID) {
			continue
		}
		if fields[3] == nil || len(*fields[3]) < 4 {
			return ""
		}
		encID := int(int32(binary.LittleEndian.Uint32(*fields[3])))
		return pgEncodingIDToCodec[encID]
	}
	return ""
}

// utf8SeqStats：评估字节流 UTF8 多字节序列（合法数/候选数/合法序列数）。
// 判定逻辑与 v1.0.42/v1.0.43 逐字节一致（含 E0/ED/F0/F4 边界收紧），仅作公共内核抽取。
func utf8SeqStats(buf []byte) (valid, total, seq int) {
	for i := 0; i < len(buf); {
		b := buf[i]
		if b < 0x80 {
			i++
			continue
		}
		var need int
		switch {
		case b >= 0xF0 && b <= 0xF4:
			need = 3
		case b >= 0xE0 && b <= 0xEF:
			need = 2
		case b >= 0xC2 && b <= 0xDF:
			need = 1
		default:
			i++ // GBK 首字节多落于此：非法 UTF8 首字节，跳过
			continue
		}
		if i+need >= len(buf) {
			break
		}
		ok := true
		for j := 1; j <= need; j++ {
			if buf[i+j] < 0x80 || buf[i+j] > 0xBF {
				ok = false
				break
			}
		}
		total++
		if ok {
			// 收紧边界防伪序列：E0 后须 ≥A0；ED 后须 ≤9F；F0 后须 ≥90；F4 后须 ≤8F
			if b == 0xE0 && buf[i+1] < 0xA0 {
				ok = false
			}
			if b == 0xED && buf[i+1] > 0x9F {
				ok = false
			}
			if b == 0xF0 && buf[i+1] < 0x90 {
				ok = false
			}
			if b == 0xF4 && buf[i+1] > 0x8F {
				ok = false
			}
			if ok {
				valid++
				seq++
			}
		}
		i += need + 1
	}
	return
}

// gbkSeqStats：评估字节流 GBK 双字节序列合法率（首字节 0x81-0xFE，次字节 0x40-0xFE 且非 0x7F）。
// 仅用于单文件三态分类（边界①）：UTF8 判定不达标时，真 GBK 双字节流在此合法率极高。
func gbkSeqStats(buf []byte) (valid, total int) {
	for i := 0; i < len(buf); {
		b := buf[i]
		if b < 0x80 {
			i++
			continue
		}
		if b >= 0x81 && b <= 0xFE {
			total++
			if i+1 < len(buf) {
				lo := buf[i+1]
				if (lo >= 0x40 && lo <= 0xFE) && lo != 0x7F {
					valid++
					i += 2
					continue
				}
			}
		}
		i++
	}
	return
}

// probeFileUtf8Ratio：对单个数据文件末尾一页采样，判定是否呈 UTF8 字节特征。
// 与 probeDirUtf8Ratio 同一判定内核（v1.0.44 抽取复用；读文件末尾一页为 v1.0.43 行为）。
func probeFileUtf8Ratio(path string, pageSize int) bool {
	if path == "" || pageSize < 8192 {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	tmp := make([]byte, pageSize)
	off := fi.Size() - int64(pageSize)
	if off < 0 {
		off = 0
	}
	n, _ := f.ReadAt(tmp, off)
	if n <= 0 {
		return false
	}
	valid, total, seq := utf8SeqStats(tmp[:n])
	if seq < 4 || total == 0 {
		return false // 纯 ASCII 或采样不足
	}
	// 阈值 0.7：真实 UTF8 数据页含 int/timestamp/tuple 头等二进制字段，合法率通常 0.85+；
	// 真 GBK/GB18030/EUC_CN 双字节流合法率普遍 <0.5，可明确区分
	return float64(valid)/float64(total) >= 0.7
}

// probeFileEncoding：单文件三态编码分类（v1.0.44 边界①——脱离数据根的单文件模式兜底）。
// 返回 "utf8" / "gbk" / "unknown"。
//   - UTF8 特征达标 → utf8（V8 实存 UTF8、PG UTF8 声明文件均命中）
//   - UTF8 不达标但 GBK 双字节特征达标 → gbk（V9 实存真 GBK 命中）
//   - 纯 ASCII / 二进制大字段 / 无特征 → unknown（调用方默认 utf8 并给出警告）
//
// 注意顺序：UTF8 优先。UTF8 中文流在 GBK 判定下也呈高合法率（0xE0-0xEF 恰在 GBK 首字节区），
// 故不能以"GBK 达标"单独判 gbk；真 GBK 流 UTF8 合法率 <0.5，顺序判定可严格区分。
func probeFileEncoding(path string, pageSize int) string {
	if path == "" || pageSize < 8192 {
		return "unknown"
	}
	f, err := os.Open(path)
	if err != nil {
		return "unknown"
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return "unknown"
	}
	if fi.Size() < 1024 {
		return "unknown"
	}
	tmp := make([]byte, pageSize)
	off := fi.Size() - int64(pageSize)
	if off < 0 {
		off = 0
	}
	n, _ := f.ReadAt(tmp, off)
	if n <= 0 {
		return "unknown"
	}
	uv, ut, us := utf8SeqStats(tmp[:n])
	utf8OK := us >= 4 && ut > 0 && float64(uv)/float64(ut) >= 0.7
	if utf8OK {
		return "utf8"
	}
	// v1.0.52 收紧 GBK 判据：随机二进制（bytea/geometry 等压缩/密文列）双字节
	// 合法率天然 ≈0.74（首字节 0x81-0xFE 占比 × 尾字节 0x40-0xFE 合法率），
	// 旧阈值 0.7 会把纯二进制页误判为 GBK。真实 GBK 文本页合法率 ≥0.95（几乎
	// 所有候选对都成字），0.85 + 最少 8 对可稳定区分。
	gv, gt := gbkSeqStats(tmp[:n])
	if gt >= 8 && float64(gv)/float64(gt) >= 0.85 {
		return "gbk"
	}
	return "unknown"
}

// tablespaceFile：构造表空间内数据文件路径 <datadir>/pg_tblspc|<sys_tblspc>/<ts>/<版本目录>/base/<dboid>/<rel>
// 返回空表示路径不存在或结构异常。
func tablespaceFile(datadir string, tsOID uint32, dbDir string, rel uint32) string {
	if datadir == "" || tsOID == 0 || rel == 0 {
		return ""
	}
	var tsRoot string
	for _, prefix := range []string{"pg_tblspc", "sys_tblspc"} {
		p := filepath.Join(datadir, prefix, strconv.FormatUint(uint64(tsOID), 10))
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			tsRoot = p
			break
		}
	}
	if tsRoot == "" {
		return ""
	}
	ents, err := os.ReadDir(tsRoot)
	if err != nil {
		return ""
	}
	var verDir string
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasPrefix(n, "PG_") || strings.HasPrefix(n, "SYS_") {
			verDir = n
			break
		}
	}
	if verDir == "" {
		return ""
	}
	// v1.0.44 修正：表空间真实结构为 <tsRoot>/<verDir>/<dboid>/<rel>（PG 与金仓 V8 均无 base 层），
	// 旧实现多拼了 "base" 层导致表空间表采样不到、GBK→UTF8 回退失效（实测乱码）。
	// 先按标准布局探测，再兜底带 base 层的发行版变体。
	relS := strconv.FormatUint(uint64(rel), 10)
	dbS := filepath.Base(dbDir)
	p1 := filepath.Join(tsRoot, verDir, dbS, relS)
	if fi, err := os.Stat(p1); err == nil && fi.Mode().IsRegular() {
		return p1
	}
	p2 := filepath.Join(tsRoot, verDir, "base", dbS, relS)
	if fi, err := os.Stat(p2); err == nil && fi.Mode().IsRegular() {
		return p2
	}
	return ""
}

// collectUserDataFiles：收集库内用户表数据文件候选（v1.0.44 边界②/③）。
// 从 pg_class 取用户表 relfilenode（≥16384）：
//   - tablespace=0 → base/<dboid>/<rel>（自动跳过 TOAST 段 <rel>_n、VM <rel>_vm、FSM <rel>_fsm）
//   - tablespace≠0 → 跟随 pg_tblspc/sys_tblspc 软链到实际表空间文件（边界②：采样覆盖表空间）
//
// 返回空表时由调用方退回"最大文件"兜底逻辑（系统目录损坏/无用户表场景）。
func collectUserDataFiles(dbDir string, pageSize int) []string {
	if dbDir == "" {
		return nil
	}
	version := detectPgVersion(dbDir)
	if version == 0 {
		version = 16
	}
	isKB := isKingbaseDatadir(dbDir)
	classPath := detectSysFile(dbDir, PG_CLASS_RELFILE, map[string]bool{"pg_class": true, "sys_class": true}, pageSize)
	seen := map[string]bool{}
	var files []string
	if classPath != "" {
		datadir := filepath.Dir(filepath.Dir(dbDir))
		for tup := range iterSysTuples(classPath, version, isKB) {
			row := classFields(tup, version)
			if row == nil || row.RelFileNode == 0 || row.RelFileNode < 16384 {
				continue // 系统目录不参与采样（其字节可能按库编码存储）
			}
			var p string
			if row.TableSpace == 0 {
				p = filepath.Join(dbDir, strconv.FormatUint(uint64(row.RelFileNode), 10))
				if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
					continue
				}
			} else {
				p = tablespaceFile(datadir, row.TableSpace, dbDir, row.RelFileNode)
				if p == "" {
					continue
				}
			}
			if !seen[p] {
				seen[p] = true
				files = append(files, p)
			}
		}
	}
	return files
}

// probeDirUtf8Ratio：采样库内用户表数据文件（含表空间），评估字节流是否呈 UTF8 特征。
// 适用场景：金仓"库标 GBK/GB18030 但实际以 UTF8 字节存储"（P2-1，v1.0.42 修复）。
// v1.0.43：采样改为读文件末尾一页（PG 堆页行数据在页尾；32K 页前 16KB 无数据，原读法漏回退）。
// v1.0.44：候选优先为 pg_class 用户表文件（边界③：过滤 TOAST/VM/FSM 段文件；
// 边界②：跟随表空间软链，避免"表全在表空间时 base 下采样不到用户数据→漏回退"）。
// 返回 true 表示任一采样文件呈强 UTF8 特征（逐文件判定，避免真 GBK 文件拉低合并统计）。
func probeDirUtf8Ratio(dbDir string, pageSize int) bool {
	if dbDir == "" {
		return false
	}
	if pageSize < 8192 {
		pageSize = 8192 // 兜底：至少一页
	}
	type cand struct {
		path string
		size int64
	}
	var c []cand
	files := collectUserDataFiles(dbDir, pageSize)
	for _, p := range files {
		if fi, err := os.Stat(p); err == nil && fi.Size() >= 1024 {
			c = append(c, cand{p, fi.Size()})
		}
	}
	if len(c) == 0 {
		// 兜底：base 下数值名数据文件按大小取最大 3 个（过滤段文件与系统目录）
		ents, err := os.ReadDir(dbDir)
		if err != nil {
			return false
		}
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.Contains(name, "_") {
				continue // 跳过 TOAST/VM/FSM 段文件
			}
			fi, err := e.Info()
			if err != nil || fi.Size() < 1024 {
				continue
			}
			rf, err := strconv.Atoi(name)
			if err != nil || rf < 16384 {
				continue
			}
			c = append(c, cand{filepath.Join(dbDir, name), fi.Size()})
		}
	}
	if len(c) == 0 {
		return false
	}
	sort.Slice(c, func(i, j int) bool { return c[i].size > c[j].size })
	if len(c) > 3 {
		c = c[:3]
	}
	for _, cc := range c {
		if probeFileUtf8Ratio(cc.path, pageSize) {
			return true // 任一文件达到 UTF8 特征即判定
		}
	}
	return false
}

// ---------- 自动发现 ----------
func autoDiscoverAllTables(dbDir string, pageSize int) (string, []*TableMeta) {
	if pageSize == 0 {
		pageSize = probeDirPageSize(dbDir)
	}
	dbName := filepath.Base(dbDir)
	version := detectPgVersion(dbDir)
	if version == 0 {
		version = 16
	}
	isKB := isKingbaseDatadir(dbDir)

	knownNS := map[string]bool{"public": true, "pg_catalog": true, "pg_toast": true, "information_schema": true}
	knownClass := map[string]bool{"pg_class": true, "sys_class": true, "pg_type": true, "sys_type": true, "pg_authid": true, "sys_authid": true, "pg_namespace": true, "sys_namespace": true}

	nsMap := map[uint32]string{}
	nsPath := detectSysFile(dbDir, PG_NAMESPACE_RELFILE, knownNS, pageSize)
	if nsPath != "" {
		for tup := range iterSysTuples(nsPath, version, isKB) {
			oid, name := nsFields(tup, version)
			if name != "" {
				nsMap[oid] = name
			}
		}
	}

	classEntries := []*ClassRow{}
	classPath := detectSysFile(dbDir, PG_CLASS_RELFILE, knownClass, pageSize)
	if classPath != "" {
		for tup := range iterSysTuples(classPath, version, isKB) {
			row := classFields(tup, version)
			if row != nil {
				classEntries = append(classEntries, row)
			}
		}
	}
	if len(classEntries) == 0 {
		return dbName, nil
	}
	// OID -> relfilenode 映射：TOAST 路径推导用（reltoastrelid 是 OID，
	// TRUNCATE/重建过的表其 relfilenode != OID，磁盘文件名按 relfilenode 命名）。
	rfByOID := map[uint32]uint32{}
	tsByOID := map[uint32]uint32{}
	for _, row := range classEntries {
		rfByOID[row.OID] = row.RelFileNode
		tsByOID[row.OID] = row.TableSpace
	}
	targetOID := classEntries[0].OID
	attPath := detectPgAttribute(dbDir, PG_ATTRIBUTE_RELFILE, targetOID, pageSize)
	type attKey struct {
		rel uint32
		num int
	}
	// 幽灵列过滤依据：sys_class.relnatts（表当前最大有效列数）。
	// 金仓 ALTER TABLE 会残留 attnum 超出 relnatts 的幽灵 att 行（dropped=0 且名字被覆写），
	// 该过滤对 PG 无副作用（PG attnum 恒 <= relnatts，dropped 行也保留原 attnum）。
	relNatts := map[uint32]int{}
	for _, row := range classEntries {
		if row.RelNatts > 0 {
			relNatts[row.OID] = row.RelNatts
		}
	}
	attByRel := map[uint32][]ColumnDef{}
	if attPath != "" {
		// 金仓 ALTER DROP COLUMN 可能残留多代 sys_attribute 行（同 attnum 重复），
		// 按 attrelid+attnum 去重：优先非 dropped，同 dropped 状态取 TXmin 最新；
		// PG 每列仅一行，此逻辑对 PG 无副作用。
		best := map[attKey]ColumnDef{}
		for tup := range iterSysTuples(attPath, version, isKB) {
			ar := attrFields(tup, version, isKB)
			if ar == nil || ar.AttNum <= 0 {
				continue
			}
			if natts, ok := relNatts[ar.AttRelID]; ok && ar.AttNum > natts {
				continue // 幽灵列（超出表最大有效列数）
			}
			cd := ColumnDef{
				Name:         ar.AttName,
				TypeOID:      ar.AttTypID,
				AttLen:       ar.AttLen,
				AttNum:       ar.AttNum,
				TypMod:       ar.AttTypMod,
				NotNull:      ar.AttNotNull,
				Dropped:      ar.AttIsDropped,
				AttAlign:     ar.AttAlign,
				AttByVal:     ar.AttByVal,
				AttStorage:   ar.AttStorage,
				AttGenerated: ar.AttGenerated,
				TXmin:        ar.TXmin,
			}
			k := attKey{ar.AttRelID, ar.AttNum}
			if old, ok := best[k]; ok {
				if (!ar.AttIsDropped && old.Dropped) ||
					(ar.AttIsDropped == old.Dropped && ar.TXmin >= old.TXmin) {
					best[k] = cd
				}
			} else {
				best[k] = cd
			}
		}
		for k, cd := range best {
			attByRel[k.rel] = append(attByRel[k.rel], cd)
		}
	}
	typeNames := buildTypeNameMap(dbDir)
	roleNames := buildRoleNameMap(dbDir)
	loadEnumMap(dbDir, version, isKB)

	// 主键解析（pg_index.indisprimary + indkey → attnum → 列名）
	knownIndex := map[string]bool{"pg_index": true, "sys_index": true}
	pkByRel := map[uint32][]int{}
	if idxPath := detectSysFile(dbDir, PG_INDEX_RELFILE, knownIndex, pageSize); idxPath != "" {
		for tup := range iterSysTuples(idxPath, version, isKB) {
			ir := indexFields(tup, version, isKB)
			if ir != nil && ir.IndIsPrimary && len(ir.IndKey) > 0 {
				if cur, ok := pkByRel[ir.IndRelID]; !ok || len(ir.IndKey) > len(cur) {
					pkByRel[ir.IndRelID] = ir.IndKey
				}
			}
		}
	}

	// 分区父子映射（pg_inherits.inhparent → 直接子表 OID 列表，v1.0.36）
	// 布局（PG12-18 与金仓 V8/V9 稳定）：inhrelid(oid,off0) inhparent(oid,off4) inhseqno(int4,off8)
	// （PG14+ 末尾新增 inhdetachpending，不影响前置字段偏移）
	// 声明式分区（relkind='p'）与多级分区均通过 pg_inherits 递归串联，直到叶子 relkind='r'。
	knownInherits := map[string]bool{"pg_inherits": true, "sys_inherits": true}
	inhChildren := map[uint32][]uint32{}
	if inhPath := detectSysFile(dbDir, PG_INHERITS_RELFILE, knownInherits, pageSize); inhPath != "" {
		inhOIDs := map[uint32]bool{}
		for _, row := range classEntries {
			inhOIDs[row.OID] = true
		}
		for tup := range iterSysTuples(inhPath, version, isKB) {
			nulls := tup.getNulls()
			fields := extractFieldsDirect(tup.Raw, tup.THoff, nulls, []ColLen{{4, false, "i"}, {4, false, "i"}})
			if len(fields) < 2 || fields[0] == nil || fields[1] == nil {
				continue
			}
			childOID := u32(*fields[0], 0)
			parentOID := u32(*fields[1], 0)
			// 仅保留本库内真实表（过滤索引继承等非表对象）
			if !inhOIDs[childOID] || !inhOIDs[parentOID] {
				continue
			}
			inhChildren[parentOID] = append(inhChildren[parentOID], childOID)
		}
	}

	// v1.0.52：分区元数据（pg_partitioned_table + relpartbound + 父子反查）
	pm := loadPartMeta(dbDir, version, isKB)
	oidRow := map[uint32]*ClassRow{}
	for _, r := range classEntries {
		oidRow[r.OID] = r
	}

	tables := []*TableMeta{}
	seen := map[uint32]bool{}
	for _, row := range classEntries {
		cols := attByRel[row.OID]
		if len(cols) == 0 {
			continue
		}
		sort.Slice(cols, func(a, b int) bool { return cols[a].AttNum < cols[b].AttNum })
		schema := nsMap[row.RelNamespace]
		if schema == "" {
			schema = "public"
		}
		pkCols := pkByRel[row.OID]
		pkNames := make([]string, 0, len(pkCols))
		if len(pkCols) > 0 {
			byNum := map[int]string{}
			for _, cd := range cols {
				byNum[cd.AttNum] = cd.Name
			}
			for _, n := range pkCols {
				if nm, ok := byNum[n]; ok {
					pkNames = append(pkNames, nm)
				}
			}
		}
		tm := &TableMeta{
			OID:             row.OID,
			Schema:          schema,
			RelName:         row.RelName,
			RelFileNode:     row.RelFileNode,
			ToastRelID:      row.ToastRelID,
			ToastRelFile:    rfByOID[row.ToastRelID],
			TableSpace:      row.TableSpace,
			ToastTableSpace: tsByOID[row.ToastRelID],
			RelKind:         row.RelKind,
			Columns:         cols,
			RoleMap:         roleNames,
			TypeNames:       typeNames,
			IsKB:            isKB,
			PrimaryKey:      pkNames,
			ChildOIDs:       inhChildren[row.OID],
			RelPages:        row.RelPages,
			RelTuples:       row.RelTuples,
			Shared:          row.RelShared,
		}
		// v1.0.52：分区字段填充
		if pm != nil {
			if ki := pm.byParent[row.OID]; ki != nil && ki.strat != 0 {
				tm.PartStrat = ki.strat
				tm.PartKeyCols = ki.cols
				if ki.exprs != "" {
					nameByCol := map[int]string{}
					for _, cd := range cols {
						nameByCol[cd.AttNum] = cd.Name
					}
					tm.PartExprs = deparsePartExprs(ki.exprs, dbDir, version, isKB, nameByCol)
				}
			}
			if raw, ok := pm.boundBy[row.OID]; ok && raw != "" {
				if b := deparsePartBound(raw); b != "" {
					tm.PartBound = b
				} else {
					tm.PartBoundFail = true // 反解失败 → 回退普通表 + 人工提示
				}
			}
			if pid := pm.parentOf[row.OID]; pid != 0 {
				tm.PartParent = pid
				if pr, ok := oidRow[pid]; ok {
					pns := nsMap[pr.RelNamespace]
					if pns == "" {
						pns = "public"
					}
					tm.PartParentQual = quoteName(pns) + "." + quoteName(pr.RelName)
				}
			}
		}
		if !seen[row.OID] {
			seen[row.OID] = true
			tables = append(tables, tm)
		}
	}
	return dbName, tables
}

// ---------- meta.json 加载/导出 ----------
type MetaColumnJSON struct {
	Name       string `json:"name"`
	TypeOID    int    `json:"type_oid"`
	Len        int    `json:"len"`
	AttNum     int    `json:"attnum"`
	TypMod     int    `json:"typmod"`
	NotNull    bool   `json:"notnull"`
	Dropped    bool   `json:"dropped"`
	AttAlign   string `json:"attalign"`
	AttByVal   bool   `json:"attbyval"`
	AttStorage string `json:"attstorage"`
}

type MetaTableJSON struct {
	Schema       string           `json:"schema"`
	Table        string           `json:"table"`
	RelFileNode  uint32           `json:"relfilenode"`
	ToastRelID   uint32           `json:"toastrelid"`
	ToastRelFile uint32           `json:"toastrelfile,omitempty"`
	PrimaryKey   any              `json:"primary_key"`
	Columns      []MetaColumnJSON `json:"columns"`
}

type MetaJSON struct {
	Database  string          `json:"database"`
	PgVersion int             `json:"pg_version"`
	Tables    []MetaTableJSON `json:"tables"`
}

func loadMetaJSON(path string) (string, map[string]*TableMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	var meta MetaJSON
	if err := json.Unmarshal(data, &meta); err != nil {
		return "", nil, err
	}
	tables := map[string]*TableMeta{}
	for _, t := range meta.Tables {
		cols := make([]ColumnDef, 0, len(t.Columns))
		for _, c := range t.Columns {
			cols = append(cols, ColumnDef{
				Name:       c.Name,
				TypeOID:    uint32(c.TypeOID),
				AttLen:     c.Len,
				AttNum:     c.AttNum,
				TypMod:     c.TypMod,
				NotNull:    c.NotNull,
				Dropped:    c.Dropped,
				AttAlign:   c.AttAlign,
				AttByVal:   c.AttByVal,
				AttStorage: c.AttStorage,
			})
		}
		pkNames := []string(nil)
		switch pk := t.PrimaryKey.(type) {
		case []string:
			pkNames = pk
		case []interface{}:
			for _, v := range pk {
				if s, ok := v.(string); ok {
					pkNames = append(pkNames, s)
				}
			}
		}
		tm := &TableMeta{
			Schema:       t.Schema,
			RelName:      t.Table,
			RelFileNode:  t.RelFileNode,
			ToastRelID:   t.ToastRelID,
			ToastRelFile: t.ToastRelFile,
			Columns:      cols,
			PrimaryKey:   pkNames,
		}
		full := tm.FullName()
		tables[full] = tm
		if tm.RelFileNode != 0 {
			key := strconv.FormatUint(uint64(tm.RelFileNode), 10)
			if _, ok := tables[key]; !ok {
				tables[key] = tm
			}
		}
	}
	return meta.Database, tables, nil
}

func exportMetaJSON(dbDir string, pageSize int, out string) error {
	dbName, tables := autoDiscoverAllTables(dbDir, pageSize)
	meta := MetaJSON{Database: dbName, PgVersion: detectPgVersion(dbDir), Tables: []MetaTableJSON{}}
	for _, tm := range tables {
		tj := MetaTableJSON{
			Schema:       tm.Schema,
			Table:        tm.RelName,
			RelFileNode:  tm.RelFileNode,
			ToastRelID:   tm.ToastRelID,
			ToastRelFile: tm.ToastRelFile,
			PrimaryKey:   tm.PrimaryKey,
		}
		for _, c := range tm.Columns {
			tj.Columns = append(tj.Columns, MetaColumnJSON{
				Name:       c.Name,
				TypeOID:    int(c.TypeOID),
				Len:        c.AttLen,
				AttNum:     c.AttNum,
				TypMod:     c.TypMod,
				NotNull:    c.NotNull,
				Dropped:    c.Dropped,
				AttAlign:   c.AttAlign,
				AttByVal:   c.AttByVal,
				AttStorage: c.AttStorage,
			})
		}
		meta.Tables = append(meta.Tables, tj)
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	if out != "" {
		return os.WriteFile(out, data, 0644)
	}
	fmt.Println(string(data))
	return nil
}

// 查找表（按名称或 relfilenode）
func findTableInMeta(tables map[string]*TableMeta, name string) *TableMeta {
	if tm, ok := tables[name]; ok {
		return tm
	}
	// 尝试 schema.table 拆分
	if strings.Contains(name, ".") {
		if tm, ok := tables[name]; ok {
			return tm
		}
	}
	// relfilenode
	if tm, ok := tables[name]; ok {
		return tm
	}
	for _, tm := range tables {
		if tm.RelName == name {
			return tm
		}
	}
	return nil
}

// ---------- M1: 完整 DDL 扩展（索引/序列默认值/注释） ----------
// 仅离线静态解析可得、且跨 PG12-18 与金仓 V8/V9 布局稳定的对象：
//   - 非主键索引（含 UNIQUE）：pg_index.indkey → 列名，pg_am 访问方法名；
//     表达式/部分索引（indkey 含 0）跳过（需内核表达式反解析）。
//   - 序列默认值：pg_attrdef.adbin 文本中提取 regclass 序列 OID（:constvalue 4 [...]），
//     校验为 relkind='S' 序列后输出 DEFAULT nextval(...)；同时输出 setval 同步语句，
//     保证导入后自增不冲突。常量/表达式默认值无法离线还原，跳过（README 说明）。
//   - 注释：pg_description（classoid=1259）输出 COMMENT ON TABLE/COLUMN。
// 不支持（README 声明）：外键/检查约束/表达式索引/部分索引。

type IndexInfo struct {
	IndexName   string
	IndIsUnique bool
	AMName      string
	IndKey      []int
}

// seqNameInfo：序列 OID → (schema, name)
type seqNameInfo struct {
	Schema string
	Name   string
}

// buildDdlStatements：为目标表生成扩展 DDL 语句列表（不含基础 CREATE TABLE）。
// 返回三组：ddlStmts（索引/序列定义/默认值/注释）、seqSyncStmts（setval 序列同步，需在数据导入后执行）
// 与 warn（非空表示 DDL 扩展不完整的提示，v1.0.44 P2-4：不再静默吞掉目录缺失）。
func buildDdlStatements(dbDir string, tm *TableMeta, version int, isKB bool) ([]string, []string, string) {
	var stmts []string
	seqSync := []string{}
	warn := ""
	collectWarn := func(kind string) {
		if warn == "" {
			warn = "系统目录缺失/不可读: " + kind + "（相关 DDL 扩展被跳过）"
		}
	}
	if dbDir == "" {
		return stmts, seqSync, warn
	}
	// 1. 序列映射：pg_class relkind='S'
	seqByOID := map[uint32]seqNameInfo{}
	// 目标表 OID（按 relfilenode 匹配，避开 TRUNCATE 换文件场景）
	var targetOID uint32
	nsMap := map[uint32]string{}
	if p := detectSysFile(dbDir, PG_NAMESPACE_RELFILE, map[string]bool{"public": true, "pg_catalog": true, "pg_toast": true, "information_schema": true}, 0); p != "" {
		for tup := range iterSysTuples(p, version, isKB) {
			oid, name := nsFields(tup, version)
			if name != "" {
				nsMap[oid] = name
			}
		}
	}
	if p := detectSysFile(dbDir, PG_CLASS_RELFILE, map[string]bool{"pg_class": true, "sys_class": true, "pg_type": true, "sys_type": true}, 0); p != "" {
		for tup := range iterSysTuples(p, version, isKB) {
			row := classFields(tup, version)
			if row == nil {
				continue
			}
			schema := nsMap[row.RelNamespace]
			if schema == "" {
				schema = "public"
			}
			if row.RelKind == "S" {
				seqByOID[row.OID] = seqNameInfo{schema, row.RelName}
				continue
			}
			if row.RelFileNode == tm.RelFileNode {
				targetOID = row.OID
			}
		}
	}
	if targetOID == 0 {
		return stmts, seqSync, warn
	}
	// 2. 访问方法：pg_am（共享目录 global/2601）+ 固定 OID 兜底
	amName := map[uint32]string{403: "btree", 405: "hash", 2742: "gin", 2745: "gist", 2747: "spgist", 3580: "brin"}
	if globalDir := filepath.Dir(filepath.Dir(dbDir)); globalDir != "" {
		if p := detectSysFile(globalDir, 2601, nil, 0); p != "" {
			for tup := range iterSysTuples(p, version, isKB) {
				nulls := tup.getNulls()
				fields := extractFieldsDirect(tup.Raw, tup.THoff, nulls, []ColLen{{4, false, "i"}, {64, false, "c"}})
				if len(fields) >= 2 && fields[0] != nil && fields[1] != nil {
					oid := binary.LittleEndian.Uint32(*fields[0])
					name := cstring(*fields[1], 0)
					if oid != 0 && name != "" {
						amName[oid] = name
					}
				}
			}
		}
	}
	// 3. 索引：pg_index（非主键）
	type idxRow struct {
		name    string
		uniq    bool
		am      uint32
		keys    []int
		exprRaw string // indexprs 原文（表达式索引，v1.0.51）
		predRaw string // indpred 原文（部分索引谓词，v1.0.51）
	}
	idxList := []idxRow{}
	var idxColsLazy []NamedCol // pg_index 动态列布局（首个非主键索引时懒加载）
	idxPath := detectSysFile(dbDir, PG_INDEX_RELFILE, map[string]bool{"pg_index": true, "sys_index": true}, 0)
	if idxPath == "" {
		collectWarn("pg_index/sys_index")
	}
	classPath := detectSysFile(dbDir, PG_CLASS_RELFILE, map[string]bool{"pg_class": true, "sys_class": true}, 0)
	if idxPath != "" {
		for tup := range iterSysTuples(idxPath, version, isKB) {
			raw := tup.Raw
			hoff := tup.THoff
			if hoff+8 > len(raw) {
				continue
			}
			indrelid := u32(raw, hoff+4)
			if indrelid != targetOID {
				continue
			}
			ir := indexFields(tup, version, isKB)
			if ir == nil || ir.IndIsPrimary {
				continue
			}
			// B11 修复：表达式索引 indkey 为空/全 0（金仓实测 indkey=0）也进入 idxList，
			// 由组装阶段统一"注释+告警"（不再静默过滤）；仅主键跳过
			// indexrelid → pg_class 取索引名/am
			name := ""
			am := uint32(0)
			if classPath != "" {
				for tup2 := range iterSysTuples(classPath, version, isKB) {
					row2 := classFields(tup2, version)
					if row2 == nil || row2.OID != ir.IndIndexRelID {
						continue
					}
					name = row2.RelName
					am = row2.RelAm
					break
				}
			}
			if name == "" {
				continue
			}
			// v1.0.51：表达式/部分索引 → 经 namedTableLayout 取 indexprs/indpred 原文。
			// 覆盖两类：indkey 含 0（表达式键）；indkey 全普通列但带 WHERE 谓词（部分索引）。
			exprRaw, predRaw := "", ""
			if idxColsLazy == nil {
				idxColsLazy = namedTableLayout(dbDir, PG_INDEX_RELFILE, version, isKB)
			}
			if len(idxColsLazy) > 0 {
				if f, _ := parseNamedTuple(tup, idxColsLazy, false, false); len(f) > 0 {
					if b, ok := f["indexprs"]; ok && len(b) > 0 {
						exprRaw = string(varlenaPayloadBytes(b))
					}
					if b, ok := f["indpred"]; ok && len(b) > 0 {
						predRaw = string(varlenaPayloadBytes(b))
					}
				}
			}
			idxList = append(idxList, idxRow{name: name, uniq: ir.IndIsUnique, am: am, keys: ir.IndKey, exprRaw: exprRaw, predRaw: predRaw})
		}
	}
	// 4. 列名映射
	colNameByNum := map[int]string{}
	for _, c := range tm.Columns {
		if !c.Dropped {
			colNameByNum[c.AttNum] = c.Name
		}
	}
	// 5. 序列默认值：pg_attrdef.adbin 文本提取（v1.0.51 扩展：非 nextval 默认值全量反解）
	colSeq := map[int]seqNameInfo{}
	// v1.0.51：生成列判定（adbin 是生成表达式而非 DEFAULT，禁止按 DEFAULT 导出）
	colGenerated := map[int]bool{}
	for _, c := range tm.Columns {
		if c.AttGenerated == "s" { // 仅存储生成列；普通列为空串
			colGenerated[c.AttNum] = true
		}
	}
	opMapD := buildOperatorMap(dbDir, version, isKB)
	procMapD := buildProcNameMap(dbDir, version, isKB)
	colDefaultExpr := map[int]string{} // adnum → 反解出的 DEFAULT 表达式
	colDefaultFail := map[int]bool{}   // adnum → 反解失败（输出人工提示）
	attrdefPath := detectSysFile(dbDir, 2604, map[string]bool{"pg_attrdef": true, "sys_attrdef": true}, 0)
	if attrdefPath == "" {
		collectWarn("pg_attrdef/sys_attrdef")
	}
	if attrdefPath != "" {
		re := regexp.MustCompile(`:constvalue 4 \[ (-?\d+) (-?\d+) (-?\d+) (-?\d+)`)
		for tup := range iterSysTuples(attrdefPath, version, isKB) {
			nulls := tup.getNulls()
			fields := extractFieldsDirect(tup.Raw, tup.THoff, nulls, []ColLen{{4, false, "i"}, {4, false, "i"}, {2, false, "s"}, {0, true, "i"}})
			if len(fields) < 4 || fields[1] == nil || fields[2] == nil || fields[3] == nil {
				continue
			}
			adrelid := binary.LittleEndian.Uint32(*fields[1])
			if adrelid != targetOID {
				continue
			}
			adnum := int(int16(binary.LittleEndian.Uint16(*fields[2])))
			// adbin 文本（varlena payload）
			adbinRaw := *fields[3]
			kind, total, _, _ := varlenaParse(adbinRaw, 0)
			var txt string
			switch kind {
			case VARLENA_1B:
				txt = decodeBytes(adbinRaw[1 : 1+total-1])
			case VARLENA_4B, VARLENA_4BC:
				txt = decodeBytes(adbinRaw[4 : 4+total-4])
			default:
				continue
			}
			m := re.FindStringSubmatch(txt)
			// 仅 nextval 形态（PG17 funcid=480 / PG12 内核 1579）走 colSeq 正则路径；
			// 否则任何含 4 字节常量的 adbin 都会被误吞（如 DEFAULT (-1)）
			if len(m) == 5 && (strings.Contains(txt, ":funcid 480 ") || strings.Contains(txt, ":funcid 1579 ")) {
				b0, _ := strconv.Atoi(m[1])
				b1, _ := strconv.Atoi(m[2])
				b2, _ := strconv.Atoi(m[3])
				b3, _ := strconv.Atoi(m[4])
				// nodeToString 输出有符号字节（>127 显示负数），转回无符号
				toByte := func(v int) uint32 { return uint32(v & 0xFF) }
				seqOID := toByte(b0) | toByte(b1)<<8 | toByte(b2)<<16 | toByte(b3)<<24
				if si, ok := seqByOID[seqOID]; ok {
					colSeq[adnum] = si
				}
				// nextval 形态：能映射则走 colSeq，否则保持现状（不进反解路径）
				continue
			}
			// v1.0.51：非 nextval 默认值 → nodetree 反解为 DEFAULT 表达式。
			// 生成列（attgenerated）的 adbin 语义不同，跳过（DDL 端输出人工提示）。
			if colGenerated[adnum] {
				continue
			}
			if expr := deparseNodeTree(txt, opMapD, procMapD, nil); expr != "" {
				colDefaultExpr[adnum] = expr
			} else {
				colDefaultFail[adnum] = true
			}
		}
	}
	// 6. 注释：pg_description（classoid=1259）
	type commentRow struct {
		objsubid int
		text     string
	}
	comments := []commentRow{}
	descPath := detectSysFile(dbDir, 2609, map[string]bool{"pg_description": true, "sys_description": true}, 0)
	if descPath == "" {
		collectWarn("pg_description/sys_description")
	}
	if descPath != "" {
		// 动态探测 2609 列布局：金仓 V9R1C10 在 objsubid 后多 name(64B) 列，
		// description 由标准第 4 列变为第 5 列（tableLayout 从 sys_attribute 读取真实布局）
		descLayout := []ColLen{{4, false, "i"}, {4, false, "i"}, {4, false, "i"}, {0, true, "i"}}
		if isKB {
			if l := tableLayout(dbDir, 2609, version, isKB); len(l) >= 4 {
				descLayout = l
			}
		}
		for tup := range iterSysTuples(descPath, version, isKB) {
			nulls := tup.getNulls()
			fields := extractFieldsDirect(tup.Raw, tup.THoff, nulls, descLayout)
			descIdx := len(descLayout) - 1
			if len(fields) < 4 || fields[0] == nil || fields[1] == nil || fields[2] == nil || fields[descIdx] == nil {
				continue
			}
			if binary.LittleEndian.Uint32(*fields[1]) != 1259 {
				continue
			}
			if binary.LittleEndian.Uint32(*fields[0]) != targetOID {
				continue
			}
			objsubid := int(int32(binary.LittleEndian.Uint32(*fields[2])))
			descRaw := *fields[descIdx]
			kind, total, _, _ := varlenaParse(descRaw, 0)
			var txt string
			switch kind {
			case VARLENA_1B:
				txt = decodeBytes(descRaw[1 : 1+total-1])
			case VARLENA_4B, VARLENA_4BC:
				txt = decodeBytes(descRaw[4 : 4+total-4])
			default:
				continue
			}
			comments = append(comments, commentRow{objsubid, txt})
		}
	}
	// 7. 组装语句
	// 7.1 CREATE INDEX
	// v1.0.52：分区子表的索引在 PARTITION OF 挂靠时自动继承父表分区索引，
	// 单独 CREATE 会与自动创建的同名索引冲突 → 不输出（极少数直接建在子表上的
	// 非继承索引也一并从简跳过，回灌后可按需补建）。
	if tm.PartParent != 0 && tm.PartBound != "" {
		stmts = append(stmts, "-- 分区子表索引由挂靠时自动继承父表索引，不单独输出")
	} else {
		for _, ix := range idxList {
			// v1.0.51：键列按 indkey 顺序重组——普通列直接取列名，indkey 为 0 的位置
			// 依次取 indexprs 反解出的表达式（VAR 的 varattno 是键位置，经此映射回真实列名）
			var keyParts []string
			exprOK := true
			var exprs []string
			zeroCount := 0
			for _, n := range ix.keys {
				if n <= 0 {
					zeroCount++
				}
			}
			if zeroCount > 0 {
				if ix.exprRaw == "" {
					exprOK = false
				} else {
					// indexprs 中 VAR 的 varattno 直接引用基表列号（varno=1，PG17 实测），
					// 并非 indkey 位置 —— 经 colNameByNum 还原列名
					varAt := func(attno int) string {
						if nm, ok := colNameByNum[attno]; ok {
							return quoteName(nm)
						}
						return ""
					}
					exprs = deparseNodeTreeList(ix.exprRaw, opMapD, procMapD, varAt)
					if len(exprs) != zeroCount {
						exprOK = false
					}
				}
			}
			if exprOK {
				ei := 0
				for _, n := range ix.keys {
					if n <= 0 {
						keyParts = append(keyParts, "("+exprs[ei]+")")
						ei++
						continue
					}
					if nm, ok := colNameByNum[n]; ok {
						keyParts = append(keyParts, quoteName(nm))
					} else {
						exprOK = false
						break
					}
				}
			}
			// 部分索引谓词（indkey 全普通列也可以有 WHERE）
			where := ""
			if exprOK && ix.predRaw != "" {
				varAt := func(attno int) string {
					if nm, ok := colNameByNum[attno]; ok {
						return quoteName(nm)
					}
					return ""
				}
				if w := deparseNodeTree(ix.predRaw, opMapD, procMapD, varAt); w != "" {
					where = " WHERE " + w
				} else {
					exprOK = false // 谓词还原失败：宁可不导也不能丢 WHERE
				}
			}
			if !exprOK || len(keyParts) == 0 {
				// B11 修复：表达式索引/未知列索引不再静默跳过——
				// 输出注释占位 + 告警，避免重建后索引定义与源库不一致而用户无感知
				warn = "表达式索引 " + quoteName(ix.name) + " 暂不支持反编译，已跳过（DDL 不完整，建议手工补建；表达式请用 pg_get_indexdef 查看）"
				stmts = append(stmts, fmt.Sprintf("-- 表达式索引 %s 未导出（表达式或谓词无法离线反解）；源库索引定义请用 pg_get_indexdef 查看", quoteName(ix.name)))
				continue
			}
			am := ix.am
			if am == 0 || amName[am] == "" {
				am = 403 // 兜底 btree
			}
			useAM := amName[am]
			if useAM == "" {
				useAM = "btree"
			}
			uniq := ""
			if ix.uniq {
				uniq = "UNIQUE "
			}
			stmts = append(stmts, fmt.Sprintf("CREATE %sINDEX %s ON %s.%s USING %s (%s)%s;",
				uniq, quoteName(ix.name), quoteName(tm.Schema), quoteName(tm.RelName), useAM, strings.Join(keyParts, ", "), where))
		}
	} // v1.0.52 分区子表索引跳过分支闭合
	// 7.2 序列：CREATE SEQUENCE + 默认值（DDL 内联 DEFAULT）+ setval 同步
	// 列序号排序（稳定输出）
	seqNums := make([]int, 0, len(colSeq))
	for n := range colSeq {
		seqNums = append(seqNums, n)
	}
	sort.Ints(seqNums)
	seenSeq := map[string]bool{}
	for _, n := range seqNums {
		nm, ok := colNameByNum[n]
		if !ok {
			continue
		}
		si := colSeq[n]
		seqKey := si.Schema + "." + si.Name
		if !seenSeq[seqKey] {
			seenSeq[seqKey] = true
			stmts = append(stmts, fmt.Sprintf("CREATE SEQUENCE IF NOT EXISTS %s.%s;",
				quoteName(si.Schema), quoteName(si.Name)))
		}
		stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s.%s ALTER COLUMN %s SET DEFAULT nextval('%s.%s'::regclass);",
			quoteName(tm.Schema), quoteName(tm.RelName), quoteName(nm),
			quoteName(si.Schema), quoteName(si.Name)))
		seqSync = append(seqSync, fmt.Sprintf("SELECT setval('%s.%s', COALESCE((SELECT MAX(%s) FROM %s.%s), 1), true);",
			quoteName(si.Schema), quoteName(si.Name), quoteName(nm), quoteName(tm.Schema), quoteName(tm.RelName)))
	}
	// 7.3 普通默认值（v1.0.51）：非 nextval 的 adbin 反解 → ALTER SET DEFAULT
	defNums := make([]int, 0, len(colDefaultExpr))
	for n := range colDefaultExpr {
		defNums = append(defNums, n)
	}
	sort.Ints(defNums)
	for _, n := range defNums {
		nm, ok := colNameByNum[n]
		if !ok {
			continue
		}
		stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s.%s ALTER COLUMN %s SET DEFAULT %s;",
			quoteName(tm.Schema), quoteName(tm.RelName), quoteName(nm), colDefaultExpr[n]))
	}
	// 反解失败 → 人工提示注释（L3 原则：宁可少输出，不出错误 SQL）
	failNums := make([]int, 0, len(colDefaultFail))
	for n := range colDefaultFail {
		failNums = append(failNums, n)
	}
	sort.Ints(failNums)
	for _, n := range failNums {
		if nm, ok := colNameByNum[n]; ok {
			stmts = append(stmts, fmt.Sprintf("-- 列 %s 的默认值表达式无法离线还原，需人工补回（源库可用 pg_get_expr(adbin, adrelid) 查看）",
				quoteName(nm)))
		}
	}
	// 生成列提示：DEFAULT 语义不等价（GENERATED ALWAYS AS），导出为普通列+数据
	genNums := make([]int, 0)
	for _, c := range tm.Columns {
		if c.AttGenerated != "" && !c.Dropped {
			genNums = append(genNums, c.AttNum)
		}
	}
	sort.Ints(genNums)
	for _, n := range genNums {
		if nm, ok := colNameByNum[n]; ok {
			stmts = append(stmts, fmt.Sprintf("-- 列 %s 为生成列（GENERATED ALWAYS AS），已导出为普通列；生成表达式需人工补建（源库用 \\d+ 或 pg_get_expr 查看）",
				quoteName(nm)))
		}
	}
	// 7.4 注释
	for _, c := range comments {
		if c.objsubid == 0 {
			stmts = append(stmts, fmt.Sprintf("COMMENT ON TABLE %s.%s IS %s;",
				quoteName(tm.Schema), quoteName(tm.RelName), sqlStringLiteral(c.text)))
		} else if nm, ok := colNameByNum[c.objsubid]; ok {
			stmts = append(stmts, fmt.Sprintf("COMMENT ON COLUMN %s.%s.%s IS %s;",
				quoteName(tm.Schema), quoteName(tm.RelName), quoteName(nm), sqlStringLiteral(c.text)))
		}
	}
	return stmts, seqSync, warn
}
