// Author: raysuen
package engine

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"pg2sql/internal/datatype"
	"sort"
	"strconv"
	"strings"
)

// ---------- 系统表元组遍历 ----------
// v1.0.49 修复（E1）：由 chan 改为 range-over-func（Go 1.23）。
// 原 chan 版本在消费者提前 break 时（如 OID 查找路径只取第一条），
// 生产者 goroutine 永久阻塞在 out <- 上，泄漏 goroutine + 文件句柄；
// 且 2019-2027 索引查找 O(N²) 调用点反复触发。改用 yield 后，
// break 即返回 false，遍历立即退出并释放 fd。签名对 `for range` 调用点完全兼容。
func IterSysTuples(path string, pgVersion int, isKB bool) func(yield func(*HeapTuple) bool) {
	return func(yield func(*HeapTuple) bool) {
		// v1.0.56 修复（P3-G12）：系统目录（pg_attribute/pg_class 等）超过 1GB
		// 存在 .1/.2 续段文件，旧版单文件顺序读会静默丢失后半段表/列。
		// 与用户表路径统一走 SegFiles（主文件 + 续段，存在即加入）。
		for _, seg := range SegFiles(path) {
			if !IterSysTuplesOne(seg, pgVersion, isKB, yield) {
				return
			}
		}
	}
}

// IterSysTuplesOne：扫描单个段文件；yield 返回 false 时返回 false 终止整体遍历
func IterSysTuplesOne(path string, pgVersion int, isKB bool, yield func(*HeapTuple) bool) bool {
	f, err := os.Open(path)
	if err != nil {
		return true
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
		lay := TryStandardLayout(pageBuf, ps)
		if lay == nil {
			lay = TryAutoLayout(pageBuf, ps)
		}
		if lay == nil {
			continue
		}
		lower := int(lay.Lower)
		for i := 0; lay.HeaderEnd+4*i+4 <= lower; i++ {
			rawID := uint32(datatype.U16(pageBuf, lay.HeaderEnd+4*i)) | uint32(datatype.U16(pageBuf, lay.HeaderEnd+4*i+2))<<16
			off := int(rawID & 0x7FFF)
			flags := (rawID >> 15) & 0x03
			ln := int((rawID >> 17) & 0x7FFF)
			if flags != ITEMID_NORMAL || off >= ps || ln > ps || off+ln > ps {
				continue
			}
			tup := ParseTuple(append([]byte(nil), pageBuf[off:off+ln]...), pgVersion)
			if tup == nil || !tup.IsLive() {
				continue
			}
			if !yield(tup) {
				return false
			}
		}
	}
	return true
}

func ProbePageSize(path string) int {
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
func AttrFields(tup *HeapTuple, version int, isKB bool) *datatype.AttrRow {
	layout := pgAttributeLayout(version, isKB)
	nulls := tup.GetNulls()
	fields := datatype.ExtractFieldsDirect(tup.Raw, tup.THoff, nulls, layout)
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
		name = datatype.CString(*aname, 0)
	}
	tx := uint32(0)
	if len(tup.Raw) >= 4 {
		tx = binary.LittleEndian.Uint32(tup.Raw[0:4])
	}
	return &datatype.AttrRow{
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

// ---------- 系统文件探测 ----------
func DetectSysFile(directory string, standardOID int, knownNames map[string]bool, pageSize int) string {
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
			lay := TryStandardLayout(buf, pageSize)
			if lay == nil {
				lay = TryAutoLayout(buf, pageSize)
			}
			if lay == nil {
				continue
			}
			lower := int(lay.Lower)
			for i := 0; lay.HeaderEnd+4*i+4 <= lower; i++ {
				rawID := uint32(datatype.U16(buf, lay.HeaderEnd+4*i)) | uint32(datatype.U16(buf, lay.HeaderEnd+4*i+2))<<16
				off := int(rawID & 0x7FFF)
				flags := (rawID >> 15) & 0x03
				ln := int((rawID >> 17) & 0x7FFF)
				if flags != ITEMID_NORMAL || off >= pageSize || ln > pageSize || off+ln > pageSize {
					continue
				}
				tup := ParseTuple(buf[off:off+ln], 12)
				if tup == nil || !tup.IsLive() {
					continue
				}
				nulls := tup.GetNulls()
				fields := datatype.ExtractFieldsDirect(tup.Raw, tup.THoff, nulls, []datatype.ColLen{{AttLen: 64, IsVarlena: false, AttAlign: "c"}})
				if len(fields) == 0 || fields[0] == nil {
					continue
				}
				nm := datatype.CString(*fields[0], 0)
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

// pg_attribute 全列布局（版本感知）
func pgAttributeLayout(version int, isKB bool) []datatype.ColLen {
	if isKB {
		fixed := []datatype.ColLen{
			{AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 64, IsVarlena: false, AttAlign: "c"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, // relid name typid
			{AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 2, IsVarlena: false, AttAlign: "s"}, {AttLen: 2, IsVarlena: false, AttAlign: "s"}, // stattarg len num
			{AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, // ndims(int4!) cacheoff typmod
			{AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, // byval storage align
			{AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, // notnull hasdef hasmissing
			{AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, // identity generated isdropped
			{AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 2, IsVarlena: false, AttAlign: "s"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, // islocal inhcount collation
		}
		return append(fixed, []datatype.ColLen{{AttLen: 0, IsVarlena: true, AttAlign: "i"}, {AttLen: 0, IsVarlena: true, AttAlign: "i"}, {AttLen: 0, IsVarlena: true, AttAlign: "i"}, {AttLen: 0, IsVarlena: true, AttAlign: "i"}}...)
	}
	if version >= 18 {
		fixed := []datatype.ColLen{
			{AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 64, IsVarlena: false, AttAlign: "c"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, // relid name typid
			{AttLen: 2, IsVarlena: false, AttAlign: "s"}, {AttLen: 2, IsVarlena: false, AttAlign: "s"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, // len num typmod
			{AttLen: 2, IsVarlena: false, AttAlign: "s"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, // ndims byval align
			{AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, // storage compression notnull
			{AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, // hasdef hasmissing identity
			{AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, // generated isdropped islocal
			{AttLen: 2, IsVarlena: false, AttAlign: "s"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 2, IsVarlena: false, AttAlign: "s"}, // inhcount collation stattarg
		}
		return append(fixed, []datatype.ColLen{{AttLen: 0, IsVarlena: true, AttAlign: "i"}, {AttLen: 0, IsVarlena: true, AttAlign: "i"}, {AttLen: 0, IsVarlena: true, AttAlign: "i"}, {AttLen: 0, IsVarlena: true, AttAlign: "i"}}...)
	}
	if version == 17 {
		fixed := []datatype.ColLen{
			{AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 64, IsVarlena: false, AttAlign: "c"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, // relid name typid
			{AttLen: 2, IsVarlena: false, AttAlign: "s"}, {AttLen: 2, IsVarlena: false, AttAlign: "s"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, // len num cacheoff
			{AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 2, IsVarlena: false, AttAlign: "s"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, // typmod ndims byval
			{AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, // align storage compression
			{AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, // notnull hasdef hasmissing
			{AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, // identity generated isdropped
			{AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 2, IsVarlena: false, AttAlign: "s"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, // islocal inhcount collation
			{AttLen: 2, IsVarlena: false, AttAlign: "s"}, // stattarg
		}
		return append(fixed, []datatype.ColLen{{AttLen: 0, IsVarlena: true, AttAlign: "i"}, {AttLen: 0, IsVarlena: true, AttAlign: "i"}, {AttLen: 0, IsVarlena: true, AttAlign: "i"}, {AttLen: 0, IsVarlena: true, AttAlign: "i"}}...)
	}
	if version >= 16 {
		fixed := []datatype.ColLen{
			{AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 64, IsVarlena: false, AttAlign: "c"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, // relid name typid
			{AttLen: 2, IsVarlena: false, AttAlign: "s"}, {AttLen: 2, IsVarlena: false, AttAlign: "s"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, // len num cacheoff
			{AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 2, IsVarlena: false, AttAlign: "s"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, // typmod ndims byval
			{AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, // align storage compression
			{AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, // notnull hasdef hasmissing
			{AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, // identity generated isdropped
			{AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 2, IsVarlena: false, AttAlign: "s"}, {AttLen: 2, IsVarlena: false, AttAlign: "s"}, // islocal inhcount stattarg
			{AttLen: 4, IsVarlena: false, AttAlign: "i"}, // collation
		}
		return append(fixed, []datatype.ColLen{{AttLen: 0, IsVarlena: true, AttAlign: "i"}, {AttLen: 0, IsVarlena: true, AttAlign: "i"}, {AttLen: 0, IsVarlena: true, AttAlign: "i"}, {AttLen: 0, IsVarlena: true, AttAlign: "i"}}...)
	}
	if version >= 14 {
		fixed := []datatype.ColLen{
			{AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 64, IsVarlena: false, AttAlign: "c"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, // relid name typid
			{AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 2, IsVarlena: false, AttAlign: "s"}, {AttLen: 2, IsVarlena: false, AttAlign: "s"}, // stattarg len num
			{AttLen: 2, IsVarlena: false, AttAlign: "s"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, // ndims cacheoff typmod
			{AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, // byval align storage
			{AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, // compression notnull hasdef
			{AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, // hasmissing identity generated
			{AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, // isdropped islocal inhcount(int4!)
			{AttLen: 4, IsVarlena: false, AttAlign: "i"}, // collation
		}
		return append(fixed, []datatype.ColLen{{AttLen: 0, IsVarlena: true, AttAlign: "i"}, {AttLen: 0, IsVarlena: true, AttAlign: "i"}, {AttLen: 0, IsVarlena: true, AttAlign: "i"}, {AttLen: 0, IsVarlena: true, AttAlign: "i"}}...)
	}
	// version >= 12（PG12/13）
	fixed := []datatype.ColLen{
		{AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 64, IsVarlena: false, AttAlign: "c"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, // relid name typid
		{AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 2, IsVarlena: false, AttAlign: "s"}, {AttLen: 2, IsVarlena: false, AttAlign: "s"}, // stattarg len num
		{AttLen: 2, IsVarlena: false, AttAlign: "s"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, // ndims cacheoff typmod
		{AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, // byval storage align
		{AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, // notnull hasdef hasmissing
		{AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, // identity generated isdropped
		{AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 2, IsVarlena: false, AttAlign: "s"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, // islocal inhcount(int4)
		{AttLen: 4, IsVarlena: false, AttAlign: "i"}, // collation
	}
	return append(fixed, []datatype.ColLen{{AttLen: 0, IsVarlena: true, AttAlign: "i"}, {AttLen: 0, IsVarlena: true, AttAlign: "i"}, {AttLen: 0, IsVarlena: true, AttAlign: "i"}, {AttLen: 0, IsVarlena: true, AttAlign: "i"}}...)
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
