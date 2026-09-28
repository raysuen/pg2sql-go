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
)

// 编码 ID → codec（PG pg_wchar.h pg_enc 枚举）
var pgEncodingIDToCodec = map[int]string{
	0:  "sql_ascii", 6: "utf-8", 8: "latin1", 9: "latin1", 10: "latin1",
	11: "latin1", 12: "latin1", 13: "latin1", 14: "latin1", 15: "latin1",
	16: "latin1", 17: "latin1", 18: "latin1", 19: "latin1", 20: "latin1",
	21: "latin1", 22: "latin1", 23: "latin1", 24: "latin1", 25: "latin1",
	26: "latin1", 27: "latin1", 28: "latin1", 29: "latin1", 30: "latin1",
	31: "latin1", 32: "latin1", 33: "latin1", 34: "latin1", 35: "latin1",
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
		return map[string]int{"attrelid": 0, "attname": 1, "atttypid": 2, "attstattarget": 3, "attlen": 4, "attnum": 5, "attndims": 6, "attcacheoff": 7, "atttypmod": 8, "attbyval": 9, "attstorage": 10, "attalign": 11, "attnotnull": 12, "attisdropped": 17, "attcollation": 20}
	}
	if version >= 18 {
		return map[string]int{"attrelid": 0, "attname": 1, "atttypid": 2, "attlen": 3, "attnum": 4, "atttypmod": 5, "attndims": 6, "attbyval": 7, "attalign": 8, "attstorage": 9, "attnotnull": 11, "attisdropped": 16, "attstattarget": 20, "attcollation": 19}
	}
	if version == 17 {
		return map[string]int{"attrelid": 0, "attname": 1, "atttypid": 2, "attlen": 3, "attnum": 4, "attcacheoff": 5, "atttypmod": 6, "attndims": 7, "attbyval": 8, "attalign": 9, "attstorage": 10, "attnotnull": 12, "attisdropped": 17, "attstattarget": 21, "attcollation": 20}
	}
	if version >= 16 {
		return map[string]int{"attrelid": 0, "attname": 1, "atttypid": 2, "attlen": 3, "attnum": 4, "attcacheoff": 5, "atttypmod": 6, "attndims": 7, "attbyval": 8, "attalign": 9, "attstorage": 10, "attnotnull": 12, "attisdropped": 17, "attstattarget": 20, "attcollation": 21}
	}
	if version >= 14 {
		return map[string]int{"attrelid": 0, "attname": 1, "atttypid": 2, "attstattarget": 3, "attlen": 4, "attnum": 5, "attndims": 6, "attcacheoff": 7, "atttypmod": 8, "attbyval": 9, "attalign": 10, "attstorage": 11, "attnotnull": 13, "attisdropped": 18, "attcollation": 21}
	}
	return map[string]int{"attrelid": 0, "attname": 1, "atttypid": 2, "attstattarget": 3, "attlen": 4, "attnum": 5, "attndims": 6, "attcacheoff": 7, "atttypmod": 8, "attbyval": 9, "attstorage": 10, "attalign": 11, "attnotnull": 12, "attisdropped": 17, "attcollation": 20}
}

// ---------- 系统表元组遍历 ----------
func iterSysTuples(path string, pgVersion int, isKB bool) chan *HeapTuple {
	out := make(chan *HeapTuple, 64)
	go func() {
		defer close(out)
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
				out <- tup
			}
		}
	}()
	return out
}

// ---------- 系统表行解析 ----------
type AttrRow struct {
	AttRelID    uint32
	AttName     string
	AttTypID    uint32
	AttLen      int
	AttNum      int
	AttTypMod   int
	AttByVal    bool
	AttAlign    string
	AttStorage  string
	AttNotNull  bool
	AttIsDropped bool
	TXmin       uint32
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
		TXmin:        tx,
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
	var relnameF, relnamespaceF, relfilenodeF, relkindF, reltoastF *[]byte
	if version >= 12 {
		oidF = fields[0]
		relnameF = fields[1]
		relnamespaceF = fields[2]
		relfilenodeF = fields[7]
		if version >= 18 {
			if len(fields) > 17 {
				relkindF = fields[17]
			}
			if len(fields) > 13 {
				reltoastF = fields[13]
			}
		} else {
			if len(fields) > 16 {
				relkindF = fields[16]
			}
			if len(fields) > 12 {
				reltoastF = fields[12]
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
	toastRelID := uint32(0)
	if reltoastF != nil && len(*reltoastF) >= 4 {
		toastRelID = binary.LittleEndian.Uint32(*reltoastF)
	}
	natts := 0
	if version >= 12 {
		if len(fields) > 17 {
			if f := fields[17]; f != nil && len(*f) >= 2 {
				natts = int(int16(binary.LittleEndian.Uint16(*f)))
			}
		}
	}
	return &ClassRow{oid, relname, relnamespace, relfilenode, relkind, toastRelID, natts}
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
		if os.Getenv("P2S_DEBUG") != "" {
			fmt.Printf("[dbg] class oid=%d name=%s natts=%d kind=%s\n", row.OID, row.RelName, row.RelNatts, row.RelKind)
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
				if os.Getenv("P2S_DEBUG") != "" {
					fmt.Printf("[dbg] ghost filtered: rel=%d attnum=%d natts=%d name=%s\n", ar.AttRelID, ar.AttNum, natts, ar.AttName)
				}
				continue // 幽灵列（超出表最大有效列数）
			}
			cd := ColumnDef{
				Name:      ar.AttName,
				TypeOID:   ar.AttTypID,
				AttLen:    ar.AttLen,
				AttNum:    ar.AttNum,
				TypMod:    ar.AttTypMod,
				NotNull:   ar.AttNotNull,
				Dropped:   ar.AttIsDropped,
				AttAlign:  ar.AttAlign,
				AttByVal:  ar.AttByVal,
				AttStorage: ar.AttStorage,
				TXmin:     ar.TXmin,
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
		tm := &TableMeta{
			Schema:      schema,
			RelName:     row.RelName,
			RelFileNode: row.RelFileNode,
			ToastRelID:  row.ToastRelID,
			RelKind:     row.RelKind,
			Columns:     cols,
			RoleMap:     roleNames,
			TypeNames:   typeNames,
			IsKB:        isKB,
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
	Name      string `json:"name"`
	TypeOID   int    `json:"type_oid"`
	Len       int    `json:"len"`
	AttNum    int    `json:"attnum"`
	TypMod    int    `json:"typmod"`
	NotNull   bool   `json:"notnull"`
	Dropped   bool   `json:"dropped"`
	AttAlign  string `json:"attalign"`
	AttByVal  bool   `json:"attbyval"`
	AttStorage string `json:"attstorage"`
}

type MetaTableJSON struct {
	Schema      string            `json:"schema"`
	Table       string            `json:"table"`
	RelFileNode uint32            `json:"relfilenode"`
	ToastRelID  uint32            `json:"toastrelid"`
	PrimaryKey  any               `json:"primary_key"`
	Columns     []MetaColumnJSON  `json:"columns"`
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
				Name:      c.Name,
				TypeOID:   uint32(c.TypeOID),
				AttLen:    c.Len,
				AttNum:    c.AttNum,
				TypMod:    c.TypMod,
				NotNull:   c.NotNull,
				Dropped:   c.Dropped,
				AttAlign:  c.AttAlign,
				AttByVal:  c.AttByVal,
				AttStorage: c.AttStorage,
			})
		}
		tm := &TableMeta{
			Schema:      t.Schema,
			RelName:     t.Table,
			RelFileNode: t.RelFileNode,
			ToastRelID:  t.ToastRelID,
			Columns:     cols,
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
			Schema:      tm.Schema,
			Table:       tm.RelName,
			RelFileNode: tm.RelFileNode,
			ToastRelID:  tm.ToastRelID,
			PrimaryKey:  []string{},
		}
		for _, c := range tm.Columns {
			tj.Columns = append(tj.Columns, MetaColumnJSON{
				Name:      c.Name,
				TypeOID:   int(c.TypeOID),
				Len:       c.AttLen,
				AttNum:    c.AttNum,
				TypMod:    c.TypMod,
				NotNull:   c.NotNull,
				Dropped:   c.Dropped,
				AttAlign:  c.AttAlign,
				AttByVal:  c.AttByVal,
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
