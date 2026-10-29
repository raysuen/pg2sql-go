// pg2sql catalog 层：系统目录自动发现、编码探测、meta.json 加载/导出
// 对应 Python 版 pg2sql/catalog.py（v3.3），版本感知布局逐条等价翻译
// Author: raysuen
package catalog

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"pg2sql/internal/charset"
	"pg2sql/internal/datatype"
	"pg2sql/internal/engine"
	"pg2sql/internal/logx"
	"pg2sql/internal/nt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	PG_DEPEND_RELFILE    = 2608
)

// ---------- 版本探测 ----------
func DetectPgVersion(datadir string) int {
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

func IsKingbaseDatadir(datadir string) bool {
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
var pgClassCols16 = []datatype.ColLen{
	{AttLen: 64, IsVarlena: false, AttAlign: "c"}, // relname
	{AttLen: 4, IsVarlena: false, AttAlign: "i"},  // relnamespace
	{AttLen: 4, IsVarlena: false, AttAlign: "i"},  // reltype
	{AttLen: 4, IsVarlena: false, AttAlign: "i"},  // reloftype
	{AttLen: 4, IsVarlena: false, AttAlign: "i"},  // relowner
	{AttLen: 4, IsVarlena: false, AttAlign: "i"},  // relam
	{AttLen: 4, IsVarlena: false, AttAlign: "i"},  // relfilenode
	{AttLen: 4, IsVarlena: false, AttAlign: "i"},  // reltablespace
	{AttLen: 4, IsVarlena: false, AttAlign: "i"},  // relpages
	{AttLen: 4, IsVarlena: false, AttAlign: "i"},  // reltuples (float4!)
	{AttLen: 4, IsVarlena: false, AttAlign: "i"},  // relallvisible
	{AttLen: 4, IsVarlena: false, AttAlign: "i"},  // reltoastrelid
	{AttLen: 1, IsVarlena: false, AttAlign: "c"},  // relhasindex
	{AttLen: 1, IsVarlena: false, AttAlign: "c"},  // relisshared
	{AttLen: 1, IsVarlena: false, AttAlign: "c"},  // relpersistence
	{AttLen: 1, IsVarlena: false, AttAlign: "c"},  // relkind
}

// PG18+：relallvisible 后新增 relallfrozen(int4)，relkind 顺延
var pgClassCols18 = append(
	append(append([]datatype.ColLen{}, pgClassCols16[:11]...), datatype.ColLen{AttLen: 4, IsVarlena: false, AttAlign: "i"}),
	pgClassCols16[11:]...)

// relnatts(int2)：表当前最大有效 attnum（金仓/PG 12+ 在 relkind 之后）
var pgClassCols16WithNatts = append(append([]datatype.ColLen{}, pgClassCols16...), datatype.ColLen{AttLen: 2, IsVarlena: false, AttAlign: "s"})

var pgClassCols18WithNatts = append(append([]datatype.ColLen{}, pgClassCols18...), datatype.ColLen{AttLen: 2, IsVarlena: false, AttAlign: "s"})

var pgNamespaceCols = []datatype.ColLen{
	{AttLen: 64, IsVarlena: false, AttAlign: "c"}, // nspname
	{AttLen: 4, IsVarlena: false, AttAlign: "i"},  // nspowner
}

func WithOID(cols []datatype.ColLen) []datatype.ColLen {
	return append([]datatype.ColLen{{AttLen: 4, IsVarlena: false, AttAlign: "i"}}, cols...)
}

// ---- pg_index 主键解析（自动发现 DDL 主键，PG12-18/金仓同源布局）----
// pg_index 前 14 列定长（indexrelid/indrelid/indnatts/indnkeyatts + 10 个 bool），
// indkey（int2vector）为第 15 列。PG12-18 与金仓同源布局（PG15+ 新增列在 varlen 区，不影响偏移）。
var pgIndexCols = []datatype.ColLen{
	{AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 2, IsVarlena: false, AttAlign: "s"}, {AttLen: 2, IsVarlena: false, AttAlign: "s"},
	{AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"},
	{AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"},
	{AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"},
	{AttLen: -1, IsVarlena: true, AttAlign: "i"}, // indkey (int2vector, 第 15 列)
}

type IndexRow struct {
	IndRelID      uint32 // indrelid（父表 OID）
	IndIndexRelID uint32 // indexrelid（索引自身 OID）
	IndIsPrimary  bool
	IndIsUnique   bool
	IndKey        []int
	// IndNKeyAtts：indnkeyatts（真键列数，不含 INCLUDE 列）。v1.0.61 修复（P2-4）：
	// indkey 的长度是 indnatts（含 INCLUDE），旧实现把 INCLUDE 列也当键列 →
	// PRIMARY KEY (a) INCLUDE (b) 被导出成 PRIMARY KEY (a, b)（唯一性语义被改变），
	// UNIQUE ... INCLUDE 同理。0 表示布局无该字段（PG10-/金仓 V8），按全键处理。
	IndNKeyAtts int
}

// keyAtts：真正的键列（截掉 INCLUDE 尾部）。
func (ir *IndexRow) keyAtts() []int {
	if ir == nil {
		return nil
	}
	if ir.IndNKeyAtts > 0 && ir.IndNKeyAtts < len(ir.IndKey) {
		return ir.IndKey[:ir.IndNKeyAtts]
	}
	return ir.IndKey
}

// includeAtts：INCLUDE 列（indkey 中 nkey 之后的尾部）。
func (ir *IndexRow) includeAtts() []int {
	if ir == nil || ir.IndNKeyAtts <= 0 || ir.IndNKeyAtts >= len(ir.IndKey) {
		return nil
	}
	return ir.IndKey[ir.IndNKeyAtts:]
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

// attrNumVector：int2vector 的 payload（已去 varlena 头）→ []int32。
// 布局：[0:4) ndim=1 [4:8) dataoffset [8:12) elemtype [12:16) dim1 [16:20) lb [20:) 元素（2B）。
// v1.0.63（B-7）：pg_constraint.conkey 用。
func attrNumVector(payload []byte) []int32 {
	if len(payload) < 20 {
		return nil
	}
	dim1 := int(int32(binary.LittleEndian.Uint32(payload[12:16])))
	if dim1 <= 0 || dim1 > 4000 {
		return nil
	}
	vals := payload[20:]
	out := make([]int32, 0, dim1)
	for i := 0; i < dim1 && i*2+2 <= len(vals); i++ {
		out = append(out, int32(int16(binary.LittleEndian.Uint16(vals[i*2:]))))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// parseOidVector：oidvector 的 payload（已去 varlena 头）→ []uint32。
// 头部同 int2vector，元素为 4 字节 OID。v1.0.63（B-7）：pg_constraint.conexclop 用。
func parseOidVector(payload []byte) []uint32 {
	if len(payload) < 20 {
		return nil
	}
	dim1 := int(int32(binary.LittleEndian.Uint32(payload[12:16])))
	if dim1 <= 0 || dim1 > 4000 {
		return nil
	}
	vals := payload[20:]
	out := make([]uint32, 0, dim1)
	for i := 0; i < dim1 && i*4+4 <= len(vals); i++ {
		out = append(out, binary.LittleEndian.Uint32(vals[i*4:]))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// indexFields：解析 pg_index/sys_index 行，提取 indrelid、indisprimary、indkey。
// 布局差异（自动兼容）：
//
//	PG12-18：indnatts@8、indnkeyatts@10、10 个 bool@12-21、indkey@24；
//	金仓 V9（在 10 个 bool 后追加列）：indkey@26；
//	金仓 V8（PG9.6/10 内核，无 indnkeyatts）：indisprimary@11、indkey@20。
//
// indkey 采用数据区 varlena 定位法，不依赖列布局，天然兼容各版本偏移。
func indexFields(tup *engine.HeapTuple, version int, isKB bool) *IndexRow {
	return indexFieldsLayout(tup, version, isKB, nil)
}

// indexFieldsLayout：indexFields 的布局感知版本。cols 为 pg_index 的动态列布局
// （engine.NamedTableLayout(dbDir, 2610, ...)），非空时【按列名取值】，完全不依赖
// 硬编码偏移与版本推断。
//
// v1.0.63 修复（A-4）：旧实现用「indnkeyatts 取值 ≤32 且 ≤indnatts」猜布局，对
// PG10- 的普通索引（@10 = indisunique = 0）恒成立 → 落进 PG11+ 分支，把
// raw[+12]（indisexclusion）/ raw[+13]（indimmediate）当成 indisunique/indisprimary：
//   - 普通索引导出成 CREATE UNIQUE INDEX（丢行/回灌报重复键）
//   - 唯一索引 indisunique 读出 0 → 丢唯一性
//   - indimmediate 恒为 1 → 普通索引被当主键，又被主键过滤逻辑整段丢弃
//
// 现在改为：① 有动态布局按名取（唯一可信路径）；② 否则按 version 显式分流；
// ③ version 探测失败（0）时用「@12==0 且 @13==1 → 旧布局」这一对 bool 的
// 共存特征兜底（PG11+ 里 indisunique=0 时 indisprimary 必为 0，且 @14
// indnullsnotdistinct 默认 0；旧布局里 indimmediate 对任何可用索引恒为 1）。
func indexFieldsLayout(tup *engine.HeapTuple, version int, isKB bool, cols []engine.NamedCol) *IndexRow {
	raw := tup.Raw
	hoff := tup.THoff
	if hoff+8 > len(raw) {
		return nil
	}
	ir := &IndexRow{}
	ir.IndIndexRelID = datatype.U32(raw, hoff+0) // pg_index 第 1 列 = indexrelid（无 OID 列）
	ir.IndRelID = datatype.U32(raw, hoff+4)      // 第 2 列 = indrelid（父表）
	if len(cols) > 0 {
		if f, _ := engine.ParseNamedTuple(tup, cols, false, false); len(f) > 0 {
			ir.IndIsUnique = engine.FBool(f, "indisunique")
			ir.IndIsPrimary = engine.FBool(f, "indisprimary")
			if _, ok := f["indnkeyatts"]; ok {
				if nk := int(engine.FU16(f, "indnkeyatts")); nk > 0 {
					ir.IndNKeyAtts = nk
				}
			}
			if b, ok := f["indkey"]; ok && len(b) > 0 {
				bb := b
				ir.IndKey = indexIndkey(&bb)
			}
			if ir.IndNKeyAtts > len(ir.IndKey) {
				ir.IndNKeyAtts = len(ir.IndKey)
			}
			return ir
		}
	}
	// ---- 无动态布局：按版本分流 ----
	// 旧布局（PG9.5/9.6/10、金仓 V8R6C6 及更早）：indisunique@10、indisprimary@11、
	// indkey@20。新布局（PG11+、金仓 V8R6C8+）：indnatts@8、indnkeyatts@10、
	// indisunique@12、indisprimary@13（PG15+ 因 indnullsnotdistinct 移到 @14）。
	oldLayout := false
	switch {
	case version >= 11:
		oldLayout = false
	case version >= 1:
		oldLayout = true
	default: // 版本探测失败：靠两处 bool 的共存特征判别
		if hoff+14 <= len(raw) && raw[hoff+12] == 0 && raw[hoff+13] != 0 {
			oldLayout = true
		}
	}
	if oldLayout {
		if hoff+12 <= len(raw) {
			ir.IndIsUnique = raw[hoff+10] != 0
			ir.IndIsPrimary = raw[hoff+11] != 0
		}
	} else if hoff+15 <= len(raw) {
		ir.IndIsUnique = raw[hoff+12] != 0
		if !isKB && version >= 15 {
			ir.IndIsPrimary = raw[hoff+14] != 0 // PG15+（indnullsnotdistinct 占 @13）
		} else {
			ir.IndIsPrimary = raw[hoff+13] != 0 // PG11-14 / 金仓
		}
		natts := datatype.U16(raw, hoff+8)
		if nkey := datatype.U16(raw, hoff+10); nkey > 0 && nkey <= natts && nkey <= 32 {
			ir.IndNKeyAtts = int(nkey)
		}
	}
	// indkey：定位数据区内第一个合法 varlena（跳过前 14 列范围）
	pos := locateIndkey(raw, hoff)
	if pos >= 0 && hoff+pos+4 <= len(raw) {
		kind, total, _, _ := datatype.VarlenaParse(raw, hoff+pos)
		if kind != "" && total >= 4 && hoff+pos+total <= len(raw) {
			f := raw[hoff+pos : hoff+pos+total]
			ir.IndKey = indexIndkey(&f)
			if ir.IndNKeyAtts > len(ir.IndKey) {
				ir.IndNKeyAtts = len(ir.IndKey)
			}
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
		kind, total, _, _ := datatype.VarlenaParse(raw, off)
		if kind != "" && total >= 4 && off+total <= len(raw) {
			return off - hoff
		}
	}
	return -1
}

type ClassRow struct {
	OID          uint32
	RelName      string
	RelNamespace uint32
	RelFileNode  uint32
	RelKind      string
	// RelPersistence：relpersistence（'p'/'u'/'t'），v1.0.63（B-8）采集
	RelPersistence string
	ToastRelID     uint32
	RelNatts       int
	RelAm          uint32
	TableSpace     uint32 // pg_class.reltablespace：0=默认表空间(base/)，非 0=pg_tblspc/<oid>（v1.0.34）
	RelShared      bool   // relisshared：共享系统表（文件在 global/）
	// v1.0.53 完整性自证：relpages/reltuples（float4）——上次 VACUUM/ANALYZE 记录值，
	// 用于与实测页数/行数对照，检出段文件缺失与静默少导。
	RelPages  int32
	RelTuples float32
}

// TableLayout 动态探测系统目录表的列布局（从 sys_attribute 读取真实列定义）。
// 金仓 V9R1C10 的 sys_description(2609) 在 objsubid 后新增 name(attlen=64) 列，
// description 由标准第 4 列变为第 5 列；PG 及金仓 V8/V9R3C18 为标准 4 列。
// 动态探测按真实列布局解析，避免硬编码版本号。
func TableLayout(dbDir string, relOID uint32, version int, isKB bool) []datatype.ColLen {
	p := engine.DetectSysFile(dbDir, 1249, map[string]bool{"pg_attribute": true, "sys_attribute": true}, 0)
	if p == "" {
		return nil
	}
	type acol struct {
		num int
		cl  datatype.ColLen
	}
	var cols []acol
	for tup := range engine.IterSysTuples(p, version, isKB) {
		ar := engine.AttrFields(tup, version, isKB)
		if ar == nil || ar.AttRelID != relOID || ar.AttNum <= 0 || ar.AttIsDropped {
			continue
		}
		cl := datatype.ColLen{}
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
	out := make([]datatype.ColLen, len(cols))
	for i, c := range cols {
		out[i] = c.cl
	}
	return out
}

func ClassFields(tup *engine.HeapTuple, version int) *ClassRow {
	var layout []datatype.ColLen
	if version >= 18 {
		layout = WithOID(pgClassCols18WithNatts)
	} else if version >= 12 {
		layout = WithOID(pgClassCols16WithNatts)
	} else {
		layout = pgClassCols16
	}
	nulls := tup.GetNulls()
	fields := datatype.ExtractFieldsDirect(tup.Raw, tup.THoff, nulls, layout)
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
	relname := datatype.CString(*relnameF, 0)
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
	// v1.0.63（B-8）：relpersistence 在元组里紧邻 relkind 之前
	// （PG12+ 带 oid 系统列：relkind@16 → persistence@15；PG18+ relkind@17 → @16）
	relpersist := ""
	{
		kid := 15
		if version >= 12 {
			kid = 16
			if version >= 18 {
				kid = 17
			}
		}
		if len(fields) > kid-1 {
			if f := fields[kid-1]; f != nil && len(*f) >= 1 {
				switch (*f)[0] {
				case 'p', 'u', 't':
					relpersist = string((*f)[0])
				}
			}
		}
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
		// 幽灵列过滤（engine.MaxRowNatts/relNatts）失效
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
		RelPages: relPages, RelTuples: relTuples, RelPersistence: relpersist,
	}
}

func NsFields(tup *engine.HeapTuple, version int) (uint32, string) {
	layout := WithOID(pgNamespaceCols)
	nulls := tup.GetNulls()
	fields := datatype.ExtractFieldsDirect(tup.Raw, tup.THoff, nulls, layout)
	if len(fields) < 2 {
		return 0, ""
	}
	var oid uint32
	if fields[0] != nil && len(*fields[0]) >= 4 {
		oid = binary.LittleEndian.Uint32(*fields[0])
	}
	name := ""
	if fields[1] != nil {
		name = datatype.CString(*fields[1], 0)
	}
	return oid, name
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
			lay := engine.TryStandardLayout(buf, pageSize)
			if lay == nil {
				lay = engine.TryAutoLayout(buf, pageSize)
			}
			if lay != nil {
				lower := int(lay.Lower)
				for i := 0; lay.HeaderEnd+4*i+4 <= lower; i++ {
					rawID := uint32(datatype.U16(buf, lay.HeaderEnd+4*i)) | uint32(datatype.U16(buf, lay.HeaderEnd+4*i+2))<<16
					off := int(rawID & 0x7FFF)
					flags := (rawID >> 15) & 0x03
					ln := int((rawID >> 17) & 0x7FFF)
					if flags != engine.ITEMID_NORMAL || off >= pageSize || ln > pageSize || off+ln > pageSize {
						continue
					}
					tup := engine.ParseTuple(buf[off:off+ln], 12)
					if tup == nil || !tup.IsLive() {
						continue
					}
					nulls := tup.GetNulls()
					fields := datatype.ExtractFieldsDirect(tup.Raw, tup.THoff, nulls, []datatype.ColLen{{AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 64, IsVarlena: false, AttAlign: "c"}})
					if len(fields) < 2 {
						continue
					}
					if fields[0] != nil && len(*fields[0]) >= 4 {
						attrelid := binary.LittleEndian.Uint32(*fields[0])
						if fields[1] != nil {
							_ = datatype.CString(*fields[1], 0)
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

func ProbeDirPageSize(dbDir string) int {
	for _, oid := range []int{1249, 1259, 1247, 1260, 2615, 1262} {
		p := filepath.Join(dbDir, strconv.Itoa(oid))
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Size() >= 8192 {
			return engine.ProbePageSize(p)
		}
	}
	return 8192
}

// ---------- 类型/角色/枚举映射 ----------
// ---------- 方案 C：sys_type 动态类型发现（v1.0.16）----------
// 原理：读取数据库目录内 pg_type/sys_type（relfilenode 1247）与 pg_proc/sys_proc（1255），
// 按 typinput 函数名 / typbasetype 递归 / typelem 自指，把"未知 oid"动态映射到解码器族。
// 硬编码 datatype.Decoders 表优先（兜底），目录发现失败时不影响原有功能。
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
// datatype.DynamicDecoders 三档（base/domain/array）在 PG14-17 上一个都注册不上，
// `--verbose` 打印"动态类型映射: 0 个"。
func pgTypeHasSubscript(version int, isKB bool) bool { return !isKB && version >= 14 }

func pgTypeLayout(version int, isKB bool) []datatype.ColLen {
	pre := []datatype.ColLen{
		{AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 64, IsVarlena: false, AttAlign: "c"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"},
		{AttLen: 2, IsVarlena: false, AttAlign: "s"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"},
		{AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"},
	}
	if pgTypeHasSubscript(version, isKB) {
		pre = append(pre, datatype.ColLen{AttLen: 4, IsVarlena: false, AttAlign: "i"}) // typsubscript（PG14+）
	}
	mid := []datatype.ColLen{
		{AttLen: 4, IsVarlena: false, AttAlign: "i"}, // typelem
		{AttLen: 4, IsVarlena: false, AttAlign: "i"}, // typarray
		{AttLen: 4, IsVarlena: false, AttAlign: "i"}, // typinput
		{AttLen: 4, IsVarlena: false, AttAlign: "i"}, // typoutput
		{AttLen: 4, IsVarlena: false, AttAlign: "i"}, // typreceive
		{AttLen: 4, IsVarlena: false, AttAlign: "i"}, // typsend
		{AttLen: 4, IsVarlena: false, AttAlign: "i"}, // typmodin
		{AttLen: 4, IsVarlena: false, AttAlign: "i"}, // typmodout
		{AttLen: 4, IsVarlena: false, AttAlign: "i"}, // typanalyze
		{AttLen: 1, IsVarlena: false, AttAlign: "c"}, // typalign
		{AttLen: 1, IsVarlena: false, AttAlign: "c"}, // typstorage
		{AttLen: 1, IsVarlena: false, AttAlign: "c"}, // typnotnull
		{AttLen: 4, IsVarlena: false, AttAlign: "i"}, // typbasetype
	}
	return append(pre, mid...)
}

// typeNspNames：pg_namespace oid → nspname（v1.0.48）。
// 供 DDL 把 CREATE TYPE/DOMAIN 限定到类型原本所在的 schema；
// 原实现不带限定，靠 search_path 落库，跨 schema 同名类型会静默绑错。
var typeNspNames map[uint32]string

// buildTypeNspMap 读 2615（pg_namespace）构建 oid → 名称映射。
func buildTypeNspMap(dbDir string, version int, isKB bool) map[uint32]string {
	m := map[uint32]string{}
	pageSize := ProbeDirPageSize(dbDir)
	nsPath := engine.DetectSysFile(dbDir, PG_NAMESPACE_RELFILE,
		map[string]bool{"public": true, "pg_catalog": true, "pg_toast": true,
			"information_schema": true, "sys": true}, pageSize)
	if nsPath == "" {
		return m
	}
	for tup := range engine.IterSysTuples(nsPath, version, isKB) {
		oid, name := NsFields(tup, version)
		if oid != 0 && name != "" {
			m[oid] = name
		}
	}
	return m
}

// TypeSchemaName 返回类型定义所在 schema 名；未知返回空串。
func TypeSchemaName(oid uint32) string {
	if typeNspNames == nil || datatype.DynamicTypeDefs == nil {
		return ""
	}
	td, ok := datatype.DynamicTypeDefs[oid]
	if !ok || td.Nsp == 0 {
		return ""
	}
	return typeNspNames[td.Nsp]
}

// buildTypeDefs 读 1247 构建 oid→类型定义映射。
func buildTypeDefs(dbDir string, version int, isKB bool) map[uint32]*datatype.TypeDef {
	defs := map[uint32]*datatype.TypeDef{}
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
	for tup := range engine.IterSysTuples(path, version, isKB) {
		nulls := tup.GetNulls()
		fields := datatype.ExtractFieldsDirect(tup.Raw, tup.THoff, nulls, cols)
		if len(fields) < 15+off {
			continue
		}
		td := &datatype.TypeDef{}
		if fields[0] != nil && len(*fields[0]) >= 4 {
			td.OID = binary.LittleEndian.Uint32(*fields[0])
		}
		if td.OID == 0 {
			continue
		}
		if fields[1] != nil {
			td.Name = datatype.CString(*fields[1], 0)
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

// BuildProcNameMap 读 1255 构建函数 oid→函数名映射（解析 typinput）。
func BuildProcNameMap(dbDir string, version int, isKB bool) map[uint32]string {
	procMap := map[uint32]string{}
	path := filepath.Join(dbDir, "1255")
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return procMap
	}
	for tup := range engine.IterSysTuples(path, version, isKB) {
		nulls := tup.GetNulls()
		fields := datatype.ExtractFieldsDirect(tup.Raw, tup.THoff, nulls, []datatype.ColLen{{AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 64, IsVarlena: false, AttAlign: "c"}})
		if len(fields) < 2 || fields[0] == nil || fields[1] == nil {
			continue
		}
		oid := binary.LittleEndian.Uint32(*fields[0])
		name := datatype.CString(*fields[1], 0)
		if oid != 0 && name != "" {
			procMap[oid] = name
		}
	}
	return procMap
}

// BuildTypeNameMap 读取 pg_type 目录前两列构建 oid→类型名映射（供 --verbose/报错提示）。
func BuildTypeNameMap(dbDir string) map[uint32]string {
	typeMap := map[uint32]string{}
	path := filepath.Join(dbDir, "1247")
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return typeMap
	}
	version := DetectPgVersion(dbDir)
	if version == 0 {
		version = 12
	}
	isKB := IsKingbaseDatadir(dbDir)
	for tup := range engine.IterSysTuples(path, version, isKB) {
		nulls := tup.GetNulls()
		fields := datatype.ExtractFieldsDirect(tup.Raw, tup.THoff, nulls, []datatype.ColLen{{AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 64, IsVarlena: false, AttAlign: "c"}})
		if len(fields) < 2 || fields[0] == nil || fields[1] == nil {
			continue
		}
		oid := binary.LittleEndian.Uint32(*fields[0])
		name := datatype.CString(*fields[1], 0)
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
// 复用 engine.DetectSysFile 定位 + iterSysTuples 逐行 + ClassFields 解析，与 autoDiscover 同源。
func buildRegclassNameMap(dbDir string, version int, isKB bool) map[uint32]string {
	m := map[uint32]string{}
	pageSize := ProbeDirPageSize(dbDir)
	classPath := engine.DetectSysFile(dbDir, PG_CLASS_RELFILE,
		map[string]bool{"pg_class": true, "sys_class": true, "pg_type": true, "sys_type": true}, pageSize)
	if classPath == "" {
		return m
	}
	for tup := range engine.IterSysTuples(classPath, version, isKB) {
		cr := ClassFields(tup, version)
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

func BuildRoleNameMap(dbDir string) map[uint32]string {
	rm := map[uint32]string{}
	candidates := []string{
		filepath.Join(dbDir, "1260"),
		filepath.Join(filepath.Dir(filepath.Dir(dbDir)), "global", "1260"),
	}
	version := DetectPgVersion(dbDir)
	if version == 0 {
		version = 12
	}
	isKB := IsKingbaseDatadir(dbDir)
	for _, path := range candidates {
		if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		for tup := range engine.IterSysTuples(path, version, isKB) {
			nulls := tup.GetNulls()
			fields := datatype.ExtractFieldsDirect(tup.Raw, tup.THoff, nulls, []datatype.ColLen{{AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 64, IsVarlena: false, AttAlign: "c"}})
			if len(fields) < 2 || fields[0] == nil || fields[1] == nil {
				continue
			}
			oid := binary.LittleEndian.Uint32(*fields[0])
			name := datatype.CString(*fields[1], 0)
			if oid != 0 && name != "" {
				rm[oid] = name
			}
		}
		if len(rm) > 0 {
			break
		}
	}
	return rm
}

func loadEnumMap(dbDir string, version int, isKB bool) {
	path := filepath.Join(dbDir, strconv.Itoa(PG_ENUM_RELFILE))
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		datatype.SetEnumMap(map[uint32][]datatype.EnumMember{})
		return
	}
	em := map[uint32][]datatype.EnumMember{}
	// pg_enum: [oid 4B][enumtypid 4B][enumsortorder float4 4B][enumlabel]
	for tup := range engine.IterSysTuples(path, version, isKB) {
		var layout []datatype.ColLen
		if isKB {
			layout = []datatype.ColLen{{AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 0, IsVarlena: true, AttAlign: "i"}}
		} else {
			layout = []datatype.ColLen{{AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 64, IsVarlena: false, AttAlign: "c"}}
		}
		nulls := tup.GetNulls()
		fields := datatype.ExtractFieldsDirect(tup.Raw, tup.THoff, nulls, layout)
		if len(fields) < 4 || fields[0] == nil || fields[1] == nil || fields[3] == nil {
			continue
		}
		memberOID := binary.LittleEndian.Uint32(*fields[0])
		enumTypID := binary.LittleEndian.Uint32(*fields[1])
		label := ""
		if isKB {
			kind, total, _, _ := datatype.VarlenaParse(*fields[3], 0)
			if kind == datatype.VARLENA_1B {
				label = charset.DecodeBytes((*fields[3])[1 : 1+total-1])
			} else if kind == datatype.VARLENA_4B {
				label = charset.DecodeBytes((*fields[3])[4 : 4+total-4])
			} else if kind == datatype.VARLENA_4BC {
				label = charset.DecodeBytes((*fields[3])[4 : 4+total-4])
			}
		} else {
			label = datatype.CString(*fields[3], 0)
		}
		// 按 enumsortorder 有序插入（float4 小端）
		sortorder := float32(math.Float32frombits(binary.LittleEndian.Uint32(*fields[2])))
		em[enumTypID] = append(em[enumTypID], datatype.EnumMember{MemberOID: memberOID, Label: label, SortOrder: sortorder})
	}
	for tid := range em {
		sort.Slice(em[tid], func(a, b int) bool { return em[tid][a].SortOrder < em[tid][b].SortOrder })
	}
	datatype.SetEnumMap(em)
}

// ---------- 编码探测 ----------
func DetectDatabaseEncoding(datadir string, dbOID int) string {
	if datadir == "" || dbOID == 0 {
		return ""
	}
	version := DetectPgVersion(datadir)
	if version == 0 {
		version = 16
	}
	isKB := IsKingbaseDatadir(datadir)
	layout := WithOID([]datatype.ColLen{{AttLen: 64, IsVarlena: false, AttAlign: "c"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}})
	// v1.0.60 修复（P2-1）：pg_database 经 VACUUM FULL/CLUSTER 后 relfilenode 重排，
	// 硬编码 global/1262 失效 → 编码探测静默失败。标准路径不存在时回退扫描
	// global/ 数字文件：以 pg_database 布局解析出「oid 匹配 + 编码字段可映射」
	// 的文件视为候选，多个候选取最大（更可能是活跃主文件）。
	gdir := filepath.Join(datadir, "global")
	cands := []string{filepath.Join(gdir, strconv.Itoa(PG_DATABASE_RELFILE))}
	if fi, err := os.Stat(cands[0]); err != nil || !fi.Mode().IsRegular() {
		cands = nil
		if ents, err2 := os.ReadDir(gdir); err2 == nil {
			var alt []string
			var altSizes []int64
			for _, e := range ents {
				n := e.Name()
				if strings.HasSuffix(n, "_fsm") || strings.HasSuffix(n, "_vm") || strings.HasSuffix(n, "_init") {
					continue
				}
				if _, err3 := strconv.Atoi(n); err3 != nil {
					continue
				}
				p := filepath.Join(gdir, n)
				if fi2, err4 := os.Stat(p); err4 == nil && fi2.Mode().IsRegular() {
					alt = append(alt, p)
					altSizes = append(altSizes, fi2.Size())
				}
			}
			// 大文件优先：VACUUM FULL 后旧文件可能残留，活文件通常更大
			for i := 0; i < len(alt); i++ {
				for j := i + 1; j < len(alt); j++ {
					if altSizes[j] > altSizes[i] {
						alt[i], alt[j] = alt[j], alt[i]
						altSizes[i], altSizes[j] = altSizes[j], altSizes[i]
					}
				}
			}
			cands = alt
		}
	}
	for _, f := range cands {
		enc, found := scanDatabaseEncodingFile(f, dbOID, version, isKB, layout)
		if found {
			return enc
		}
	}
	return ""
}

// scanDatabaseEncodingFile：在单个 global 文件里找 dbOID 对应的 pg_database 元组，
// 返回 (编码名, 是否找到匹配元组)。编码编号未映射时告警并返回 ""（found=true），
// 由上层保持默认 UTF-8——比静默按 UTF-8 多一条可追查的线索。
var warnUnknownEncID sync.Once

func scanDatabaseEncodingFile(f string, dbOID int, version int, isKB bool, layout []datatype.ColLen) (string, bool) {
	for tup := range engine.IterSysTuples(f, version, isKB) {
		nulls := tup.GetNulls()
		fields := datatype.ExtractFieldsDirect(tup.Raw, tup.THoff, nulls, layout)
		if len(fields) < 4 || fields[0] == nil {
			continue
		}
		if binary.LittleEndian.Uint32(*fields[0]) != uint32(dbOID) {
			continue
		}
		if fields[3] == nil || len(*fields[3]) < 4 {
			return "", true
		}
		encID := int(int32(binary.LittleEndian.Uint32(*fields[3])))
		// v1.0.58（P1）：金仓把 pg_enc 35–44 重排为自有服务端编码（35=GBK 等），
		// 与上游 PG 仅客户端编号 35–41 语义完全不同，必须按数据目录类型分流。
		// 上游 PG 库编码只可能落在 0–34，两张表在 0–34 上一致，不会互相干扰。
		var enc string
		if isKB {
			enc = charset.EncodingIDToCodecKB[encID]
		} else {
			enc = charset.EncodingIDToCodec[encID]
		}
		if enc == "" {
			// v1.0.60 修复（P2-1）：负数/未知编号此前静默回落 UTF-8 且无任何提示。
			warnUnknownEncID.Do(func() {
				logx.Logf("警告: pg_database 编码编号 %d 无映射（%s），默认按 UTF-8 解码；如乱码请用 --encoding 强制", encID, map[bool]string{true: "金仓", false: "PG"}[isKB])
			})
		}
		return enc, true
	}
	return "", false
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
	version := DetectPgVersion(dbDir)
	if version == 0 {
		version = 16
	}
	isKB := IsKingbaseDatadir(dbDir)
	classPath := engine.DetectSysFile(dbDir, PG_CLASS_RELFILE, map[string]bool{"pg_class": true, "sys_class": true}, pageSize)
	seen := map[string]bool{}
	var files []string
	if classPath != "" {
		datadir := filepath.Dir(filepath.Dir(dbDir))
		for tup := range engine.IterSysTuples(classPath, version, isKB) {
			row := ClassFields(tup, version)
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

// ProbeDirUtf8Ratio：采样库内用户表数据文件（含表空间），评估字节流是否呈 UTF8 特征。
// 适用场景：金仓"库标 GBK/GB18030 但实际以 UTF8 字节存储"（P2-1，v1.0.42 修复）。
// v1.0.43：采样改为读文件末尾一页（PG 堆页行数据在页尾；32K 页前 16KB 无数据，原读法漏回退）。
// v1.0.44：候选优先为 pg_class 用户表文件（边界③：过滤 TOAST/VM/FSM 段文件；
// 边界②：跟随表空间软链，避免"表全在表空间时 base 下采样不到用户数据→漏回退"）。
// 返回 true 表示任一采样文件呈强 UTF8 特征（逐文件判定，避免真 GBK 文件拉低合并统计）。
func ProbeDirUtf8Ratio(dbDir string, pageSize int) bool {
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
		if charset.ProbeFileUTF8Ratio(cc.path, pageSize) {
			return true // 任一文件达到 UTF8 特征即判定
		}
	}
	return false
}

// ---------- 自动发现 ----------
func AutoDiscoverAllTables(dbDir string, pageSize int) (string, []*engine.TableMeta) {
	if pageSize == 0 {
		pageSize = ProbeDirPageSize(dbDir)
	}
	dbName := filepath.Base(dbDir)
	version := DetectPgVersion(dbDir)
	if version == 0 {
		version = 16
	}
	isKB := IsKingbaseDatadir(dbDir)

	knownNS := map[string]bool{"public": true, "pg_catalog": true, "pg_toast": true, "information_schema": true}
	knownClass := map[string]bool{"pg_class": true, "sys_class": true, "pg_type": true, "sys_type": true, "pg_authid": true, "sys_authid": true, "pg_namespace": true, "sys_namespace": true}

	nsMap := map[uint32]string{}
	nsPath := engine.DetectSysFile(dbDir, PG_NAMESPACE_RELFILE, knownNS, pageSize)
	if nsPath != "" {
		for tup := range engine.IterSysTuples(nsPath, version, isKB) {
			oid, name := NsFields(tup, version)
			if name != "" {
				nsMap[oid] = name
			}
		}
	}

	classEntries := []*ClassRow{}
	classPath := engine.DetectSysFile(dbDir, PG_CLASS_RELFILE, knownClass, pageSize)
	if classPath != "" {
		for tup := range engine.IterSysTuples(classPath, version, isKB) {
			row := ClassFields(tup, version)
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
	attByRel := map[uint32][]engine.ColumnDef{}
	if attPath != "" {
		// 金仓 ALTER DROP COLUMN 可能残留多代 sys_attribute 行（同 attnum 重复），
		// 按 attrelid+attnum 去重：优先非 dropped，同 dropped 状态取 TXmin 最新；
		// PG 每列仅一行，此逻辑对 PG 无副作用。
		best := map[attKey]engine.ColumnDef{}
		for tup := range engine.IterSysTuples(attPath, version, isKB) {
			ar := engine.AttrFields(tup, version, isKB)
			if ar == nil || ar.AttNum <= 0 {
				continue
			}
			if natts, ok := relNatts[ar.AttRelID]; ok && ar.AttNum > natts {
				continue // 幽灵列（超出表最大有效列数）
			}
			cd := engine.ColumnDef{
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
				Identity:     ar.AttIdentity,
				Collation:    ar.AttCollation,
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
	typeNames := BuildTypeNameMap(dbDir)
	roleNames := BuildRoleNameMap(dbDir)
	loadEnumMap(dbDir, version, isKB)

	// 主键解析（pg_index.indisprimary + indkey → attnum → 列名）
	knownIndex := map[string]bool{"pg_index": true, "sys_index": true}
	pkByRel := map[uint32][]int{}
	if idxPath := engine.DetectSysFile(dbDir, PG_INDEX_RELFILE, knownIndex, pageSize); idxPath != "" {
		// v1.0.63 修复（A-4）：按 pg_attribute 动态列布局读 pg_index，避免版本推断
		var pkIdxCols []engine.NamedCol
		pkIdxColsDone := false
		for tup := range engine.IterSysTuples(idxPath, version, isKB) {
			if !pkIdxColsDone {
				pkIdxCols = engine.NamedTableLayout(dbDir, PG_INDEX_RELFILE, version, isKB)
				pkIdxColsDone = true
			}
			ir := indexFieldsLayout(tup, version, isKB, pkIdxCols)
			// v1.0.61（P2-4）：主键只取真键列 —— PRIMARY KEY (a) INCLUDE (b) 的
			// indkey 是 [a,b]，旧实现整体当键列 → 导出成 PRIMARY KEY (a,b)，
			// 唯一性从 a 收紧到 (a,b)，回灌后语义与原库不同。
			pkKeys := ir.keyAtts()
			if ir != nil && ir.IndIsPrimary && len(pkKeys) > 0 {
				if cur, ok := pkByRel[ir.IndRelID]; !ok || len(pkKeys) > len(cur) {
					pkByRel[ir.IndRelID] = append([]int(nil), pkKeys...)
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
	if inhPath := engine.DetectSysFile(dbDir, PG_INHERITS_RELFILE, knownInherits, pageSize); inhPath != "" {
		inhOIDs := map[uint32]bool{}
		for _, row := range classEntries {
			inhOIDs[row.OID] = true
		}
		for tup := range engine.IterSysTuples(inhPath, version, isKB) {
			nulls := tup.GetNulls()
			fields := datatype.ExtractFieldsDirect(tup.Raw, tup.THoff, nulls, []datatype.ColLen{{AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}})
			if len(fields) < 2 || fields[0] == nil || fields[1] == nil {
				continue
			}
			childOID := datatype.U32(*fields[0], 0)
			parentOID := datatype.U32(*fields[1], 0)
			// 仅保留本库内真实表（过滤索引继承等非表对象）
			if !inhOIDs[childOID] || !inhOIDs[parentOID] {
				continue
			}
			inhChildren[parentOID] = append(inhChildren[parentOID], childOID)
		}
	}

	// v1.0.52：分区元数据（pg_partitioned_table + relpartbound + 父子反查）
	pm := LoadPartMeta(dbDir, version, isKB)
	oidRow := map[uint32]*ClassRow{}
	for _, r := range classEntries {
		oidRow[r.OID] = r
	}

	tables := []*engine.TableMeta{}
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
		tm := &engine.TableMeta{
			OID:             row.OID,
			Schema:          schema,
			RelName:         row.RelName,
			RelFileNode:     row.RelFileNode,
			ToastRelID:      row.ToastRelID,
			ToastRelFile:    rfByOID[row.ToastRelID],
			TableSpace:      row.TableSpace,
			ToastTableSpace: tsByOID[row.ToastRelID],
			RelKind:         row.RelKind,
			Persistence:     row.RelPersistence,
			Columns:         cols,
			RoleMap:         roleNames,
			TypeNames:       typeNames,
			IsKB:            isKB,
			PrimaryKey:      pkNames,
			ChildOIDs:       inhChildren[row.OID],
			RelPages:        row.RelPages,
			RelTuples:       row.RelTuples,
			Shared:          row.RelShared,
			DbDir:           dbDir,
			PgVersion:       version,
		}
		// v1.0.52：分区字段填充
		if pm != nil {
			if ki := pm.ByParent[row.OID]; ki != nil && ki.Strat != 0 {
				tm.PartStrat = ki.Strat
				tm.PartKeyCols = ki.Cols
				if ki.Exprs != "" {
					nameByCol := map[int]string{}
					for _, cd := range cols {
						nameByCol[cd.AttNum] = cd.Name
					}
					tm.PartExprs = DeparsePartExprs(ki.Exprs, dbDir, version, isKB, nameByCol)
				}
			}
			if raw, ok := pm.BoundBy[row.OID]; ok && raw != "" {
				if b := nt.DeparsePartBound(raw); b != "" {
					tm.PartBound = b
				} else {
					tm.PartBoundFail = true // 反解失败 → 回退普通表 + 人工提示
				}
			}
			// v1.0.61 修复（P2-5）：pg_inherits 同时记录声明式分区与普通
			// CREATE TABLE ... INHERITS 的父子关系，旧实现一律当分区子表 →
			// 普通继承子表被加上"分区子表"提示、按分区语义参与排序/索引跳过判定。
			// 判据：子表有 relpartbound（分区子表必填，继承子表为 NULL），
			// 或父表 relkind='p'（分区父表）。
			if pid := pm.ParentOf[row.OID]; pid != 0 {
				isPart := false
				if raw, ok := pm.BoundBy[row.OID]; ok && raw != "" {
					isPart = true
				}
				if pr, ok := oidRow[pid]; ok && pr.RelKind == "p" {
					isPart = true
				}
				if isPart {
					tm.PartParent = pid
					if pr, ok := oidRow[pid]; ok {
						pns := nsMap[pr.RelNamespace]
						if pns == "" {
							pns = "public"
						}
						tm.PartParentQual = engine.QuoteName(pns) + "." + engine.QuoteName(pr.RelName)
					}
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
	// v1.0.56 修复（P2-G6）：旧版不序列化/回填 attgenerated，meta.json 往返后
	// 生成列被当普通列，BuildDdlStatements 不再跳过其 adbin，反解输出
	// SET DEFAULT <expr>（PG 对 GENERATED ALWAYS AS 列拒绝 SET DEFAULT，
	// 回灌报错）。omitempty 兼容旧 meta.json（缺省视为普通列）。
	AttGenerated string `json:"attgenerated,omitempty"`
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

func LoadMetaJSON(path string) (string, map[string]*engine.TableMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	var meta MetaJSON
	if err := json.Unmarshal(data, &meta); err != nil {
		return "", nil, err
	}
	tables := map[string]*engine.TableMeta{}
	for _, t := range meta.Tables {
		cols := make([]engine.ColumnDef, 0, len(t.Columns))
		for _, c := range t.Columns {
			cols = append(cols, engine.ColumnDef{
				Name:         c.Name,
				TypeOID:      uint32(c.TypeOID),
				AttLen:       c.Len,
				AttNum:       c.AttNum,
				TypMod:       c.TypMod,
				NotNull:      c.NotNull,
				Dropped:      c.Dropped,
				AttAlign:     c.AttAlign,
				AttByVal:     c.AttByVal,
				AttStorage:   c.AttStorage,
				AttGenerated: c.AttGenerated,
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
		tm := &engine.TableMeta{
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

func ExportMetaJSON(dbDir string, pageSize int, out string) error {
	dbName, tables := AutoDiscoverAllTables(dbDir, pageSize)
	meta := MetaJSON{Database: dbName, PgVersion: DetectPgVersion(dbDir), Tables: []MetaTableJSON{}}
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
				Name:         c.Name,
				TypeOID:      int(c.TypeOID),
				Len:          c.AttLen,
				AttNum:       c.AttNum,
				TypMod:       c.TypMod,
				NotNull:      c.NotNull,
				Dropped:      c.Dropped,
				AttAlign:     c.AttAlign,
				AttByVal:     c.AttByVal,
				AttStorage:   c.AttStorage,
				AttGenerated: c.AttGenerated,
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
func FindTableInMeta(tables map[string]*engine.TableMeta, name string) *engine.TableMeta {
	// v1.0.63 修复（C-2）：旧实现的三个 `tables[name]` 是同一个表达式，后两个是
	// 死分支；裸表名靠遍历 map → 多个 schema 有同名表时返回哪张完全随机。
	// 现在：完全匹配 → 裸表名匹配按 public 优先、其余按 schema 名排序取第一个，
	// 保证同一输入稳定命中同一张表。
	if tm, ok := tables[name]; ok {
		return tm
	}
	if strings.Contains(name, ".") {
		// 大小写/引号差异：再尝试一次精确键（map 键为 schema.table 原文）
		if tm, ok := tables[name]; ok {
			return tm
		}
		return nil
	}
	var pick *engine.TableMeta
	var pickKey string
	for k, tm := range tables {
		if tm.RelName != name {
			continue
		}
		key := k
		if tm.Schema != "" {
			key = tm.Schema + "." + tm.RelName // 裸表名键会覆盖，统一用限定名比较
		}
		if pick == nil {
			pick, pickKey = tm, key
			continue
		}
		// public 优先；其余字典序最小（稳定、可预期）
		if (tm.Schema == "public" && pick.Schema != "public") || (tm.Schema == pick.Schema && key < pickKey) {
			pick, pickKey = tm, key
		}
	}
	return pick
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

// BuildDdlStatements：为目标表生成扩展 DDL 语句列表（不含基础 CREATE TABLE）。
// 返回三组：ddlStmts（索引/序列定义/默认值/注释）、seqSyncStmts（setval 序列同步，需在数据导入后执行）
// 与 warn（非空表示 DDL 扩展不完整的提示，v1.0.44 P2-4：不再静默吞掉目录缺失）。
func BuildDdlStatements(dbDir string, tm *engine.TableMeta, version int, isKB bool) ([]string, []string, string) {
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
	if p := engine.DetectSysFile(dbDir, PG_NAMESPACE_RELFILE, map[string]bool{"public": true, "pg_catalog": true, "pg_toast": true, "information_schema": true}, 0); p != "" {
		for tup := range engine.IterSysTuples(p, version, isKB) {
			oid, name := NsFields(tup, version)
			if name != "" {
				nsMap[oid] = name
			}
		}
	}
	if p := engine.DetectSysFile(dbDir, PG_CLASS_RELFILE, map[string]bool{"pg_class": true, "sys_class": true, "pg_type": true, "sys_type": true}, 0); p != "" {
		for tup := range engine.IterSysTuples(p, version, isKB) {
			row := ClassFields(tup, version)
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
	// v1.0.63 修正：旧的兜底表把 gist 写成 2745、spgist 写成 2747 —— 实际
	// pg_am 里 gist=783、spgist=4000（2745/2747 不存在）。pg_am 目录读不出来时
	// （global/2601 缺失）gist 索引会被静默当成 btree 输出，EXCLUDE 约束直接错。
	amName := map[uint32]string{2: "heap", 403: "btree", 405: "hash", 783: "gist",
		2742: "gin", 3580: "brin", 4000: "spgist"}
	if globalDir := filepath.Dir(filepath.Dir(dbDir)); globalDir != "" {
		if p := engine.DetectSysFile(globalDir, 2601, nil, 0); p != "" {
			for tup := range engine.IterSysTuples(p, version, isKB) {
				nulls := tup.GetNulls()
				fields := datatype.ExtractFieldsDirect(tup.Raw, tup.THoff, nulls, []datatype.ColLen{{AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 64, IsVarlena: false, AttAlign: "c"}})
				if len(fields) >= 2 && fields[0] != nil && fields[1] != nil {
					oid := binary.LittleEndian.Uint32(*fields[0])
					name := datatype.CString(*fields[1], 0)
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
		include []int  // INCLUDE 列（v1.0.61 P2-4：与键列分开输出）
		exprRaw string // indexprs 原文（表达式索引，v1.0.51）
		predRaw string // indpred 原文（部分索引谓词，v1.0.51）
		// v1.0.63（B-7）：索引 OID 与 NULLS NOT DISTINCT（PG15+）——
		// 约束背后的索引必须按约束还原，不能再输出 CREATE INDEX
		indid uint32
		// indclass：每个键列的 operator class OID（EXCLUDE 需要非默认 opclass 才能重建）
		indclass []uint32
		nullsND  bool
	}
	idxList := []idxRow{}
	var idxColsLazy []engine.NamedCol // pg_index 动态列布局（首个非主键索引时懒加载）
	idxColsDone := false              // 布局已尝试加载（含加载失败），避免每条元组重扫 pg_attribute
	idxPath := engine.DetectSysFile(dbDir, PG_INDEX_RELFILE, map[string]bool{"pg_index": true, "sys_index": true}, 0)
	if idxPath == "" {
		collectWarn("pg_index/sys_index")
	}
	classPath := engine.DetectSysFile(dbDir, PG_CLASS_RELFILE, map[string]bool{"pg_class": true, "sys_class": true}, 0)
	if idxPath != "" {
		for tup := range engine.IterSysTuples(idxPath, version, isKB) {
			raw := tup.Raw
			hoff := tup.THoff
			if hoff+8 > len(raw) {
				continue
			}
			indrelid := datatype.U32(raw, hoff+4)
			if indrelid != targetOID {
				continue
			}
			// v1.0.63 修复（A-4）：pg_index 动态列布局必须在解析索引行之前就绪
			if !idxColsDone {
				idxColsLazy = engine.NamedTableLayout(dbDir, PG_INDEX_RELFILE, version, isKB)
				idxColsDone = true
			}
			ir := indexFieldsLayout(tup, version, isKB, idxColsLazy)
			if ir == nil || ir.IndIsPrimary {
				continue
			}
			// B11 修复：表达式索引 indkey 为空/全 0（金仓实测 indkey=0）也进入 idxList，
			// 由组装阶段统一"注释+告警"（不再静默过滤）；仅主键跳过
			// indexrelid → pg_class 取索引名/am
			name := ""
			am := uint32(0)
			if classPath != "" {
				for tup2 := range engine.IterSysTuples(classPath, version, isKB) {
					row2 := ClassFields(tup2, version)
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
			// v1.0.51：表达式/部分索引 → 经 engine.NamedTableLayout 取 indexprs/indpred 原文。
			// 覆盖两类：indkey 含 0（表达式键）；indkey 全普通列但带 WHERE 谓词（部分索引）。
			exprRaw, predRaw := "", ""
			nullsND := false
			var indclass []uint32
			if len(idxColsLazy) > 0 {
				if f, _ := engine.ParseNamedTuple(tup, idxColsLazy, false, false); len(f) > 0 {
					if b, ok := f["indexprs"]; ok && len(b) > 0 {
						exprRaw = string(datatype.VarlenaPayloadBytes(b))
					}
					if b, ok := f["indpred"]; ok && len(b) > 0 {
						predRaw = string(datatype.VarlenaPayloadBytes(b))
					}
					nullsND = engine.FBool(f, "indnullsnotdistinct")
					if b, ok := f["indclass"]; ok {
						indclass = parseOidVector(datatype.VarlenaPayloadBytes(b))
					}
				}
			}
			idxList = append(idxList, idxRow{name: name, uniq: ir.IndIsUnique, am: am,
				keys: ir.keyAtts(), include: ir.includeAtts(), exprRaw: exprRaw, predRaw: predRaw,
				indid: ir.IndIndexRelID, indclass: indclass, nullsND: nullsND})
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
	opMapD := BuildOperatorMap(dbDir, version, isKB)
	procMapD := BuildProcNameMap(dbDir, version, isKB)
	colDefaultExpr := map[int]string{} // adnum → 反解出的 DEFAULT 表达式
	colDefaultFail := map[int]bool{}   // adnum → 反解失败（输出人工提示）
	attrdefPath := engine.DetectSysFile(dbDir, 2604, map[string]bool{"pg_attrdef": true, "sys_attrdef": true}, 0)
	if attrdefPath == "" {
		collectWarn("pg_attrdef/sys_attrdef")
	}
	if attrdefPath != "" {
		re := regexp.MustCompile(`:constvalue 4 \[ (-?\d+) (-?\d+) (-?\d+) (-?\d+)`)
		for tup := range engine.IterSysTuples(attrdefPath, version, isKB) {
			nulls := tup.GetNulls()
			fields := datatype.ExtractFieldsDirect(tup.Raw, tup.THoff, nulls, []datatype.ColLen{{AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 2, IsVarlena: false, AttAlign: "s"}, {AttLen: 0, IsVarlena: true, AttAlign: "i"}})
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
			kind, total, _, _ := datatype.VarlenaParse(adbinRaw, 0)
			var txt string
			switch kind {
			case datatype.VARLENA_1B:
				txt = charset.DecodeBytes(adbinRaw[1 : 1+total-1])
			case datatype.VARLENA_4B, datatype.VARLENA_4BC:
				txt = charset.DecodeBytes(adbinRaw[4 : 4+total-4])
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
			if expr := nt.DeparseNodeTree(txt, opMapD, procMapD, nil); expr != "" {
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
	descPath := engine.DetectSysFile(dbDir, 2609, map[string]bool{"pg_description": true, "sys_description": true}, 0)
	if descPath == "" {
		collectWarn("pg_description/sys_description")
	}
	if descPath != "" {
		// 动态探测 2609 列布局：金仓 V9R1C10 在 objsubid 后多 name(64B) 列，
		// description 由标准第 4 列变为第 5 列（TableLayout 从 sys_attribute 读取真实布局）
		descLayout := []datatype.ColLen{{AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 0, IsVarlena: true, AttAlign: "i"}}
		if isKB {
			if l := TableLayout(dbDir, 2609, version, isKB); len(l) >= 4 {
				descLayout = l
			}
		}
		for tup := range engine.IterSysTuples(descPath, version, isKB) {
			nulls := tup.GetNulls()
			fields := datatype.ExtractFieldsDirect(tup.Raw, tup.THoff, nulls, descLayout)
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
			kind, total, _, _ := datatype.VarlenaParse(descRaw, 0)
			var txt string
			switch kind {
			case datatype.VARLENA_1B:
				txt = charset.DecodeBytes(descRaw[1 : 1+total-1])
			case datatype.VARLENA_4B, datatype.VARLENA_4BC:
				txt = charset.DecodeBytes(descRaw[4 : 4+total-4])
			default:
				continue
			}
			comments = append(comments, commentRow{objsubid, txt})
		}
	}
	// 6.5 v1.0.63 修复（B-7）：UNIQUE / EXCLUDE 约束（pg_constraint contype='u'/'x'）
	// 旧实现只按索引输出 CREATE UNIQUE INDEX：唯一性还在，但 pg_constraint 对象不存在
	// → ON CONFLICT ON CONSTRAINT / ALTER TABLE ... DROP CONSTRAINT 全部失效，
	// EXCLUDE 的 operator、DEFERRABLE / INITIALLY DEFERRED 也整个丢失。
	type ueRow struct {
		name       string
		typ        byte
		indid      uint32
		keys       []int32
		ops        []uint32
		deferrable bool
		deferred   bool
	}
	ueRows := []*ueRow{}
	ueByInd := map[uint32]*ueRow{}
	if p := engine.DetectSysFile(dbDir, 2606, map[string]bool{"pg_constraint": true, "sys_constraint": true}, 0); p != "" {
		if cc := engine.NamedTableLayout(dbDir, 2606, version, isKB); len(cc) > 0 {
			for tup := range engine.IterSysTuples(p, version, isKB) {
				if !tup.IsLive() {
					continue
				}
				f, _ := engine.ParseNamedTuple(tup, cc, false, false)
				if len(f) == 0 || engine.FU32(f, "conrelid") != targetOID {
					continue
				}
				typ := engine.FByte(f, "contype")
				if typ != 'u' && typ != 'x' {
					continue
				}
				row := &ueRow{
					name: engine.FCstr(f, "conname"), typ: typ,
					indid:      engine.FU32(f, "conindid"),
					deferrable: engine.FBool(f, "condeferrable"),
					deferred:   engine.FBool(f, "condeferred"),
				}
				if b, ok := f["conkey"]; ok {
					row.keys = attrNumVector(datatype.VarlenaPayloadBytes(b))
				}
				if typ == 'x' {
					if b, ok := f["conexclop"]; ok {
						row.ops = parseOidVector(datatype.VarlenaPayloadBytes(b))
					}
				}
				ueRows = append(ueRows, row)
				if row.indid != 0 {
					ueByInd[row.indid] = row
				}
			}
		}
	}
	sort.Slice(ueRows, func(i, j int) bool { return ueRows[i].name < ueRows[j].name })
	idxByInd := map[uint32]int{}
	for i := range idxList {
		if idxList[i].indid != 0 {
			idxByInd[idxList[i].indid] = i
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
			// v1.0.63（B-7）：约束背后的索引改由 ALTER TABLE ADD CONSTRAINT 还原
			if _, isCon := ueByInd[ix.indid]; isCon {
				continue
			}
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
							return engine.QuoteName(nm)
						}
						return ""
					}
					exprs = nt.DeparseNodeTreeList(ix.exprRaw, opMapD, procMapD, varAt)
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
						keyParts = append(keyParts, engine.QuoteName(nm))
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
						return engine.QuoteName(nm)
					}
					return ""
				}
				if w := nt.DeparseNodeTree(ix.predRaw, opMapD, procMapD, varAt); w != "" {
					where = " WHERE " + w
				} else {
					exprOK = false // 谓词还原失败：宁可不导也不能丢 WHERE
				}
			}
			if !exprOK || len(keyParts) == 0 {
				// B11 修复：表达式索引/未知列索引不再静默跳过——
				// 输出注释占位 + 告警，避免重建后索引定义与源库不一致而用户无感知
				warn = "表达式索引 " + engine.QuoteName(ix.name) + " 暂不支持反编译，已跳过（DDL 不完整，建议手工补建；表达式请用 pg_get_indexdef 查看）"
				stmts = append(stmts, fmt.Sprintf("-- 表达式索引 %s 未导出（表达式或谓词无法离线反解）；源库索引定义请用 pg_get_indexdef 查看", engine.QuoteName(ix.name)))
				continue
			}
			am := ix.am
			useAM := amName[am]
			if useAM == "" {
				// v1.0.63：AM 未知时不再静默按 btree 输出（gist/自定义 AM 会建成错的索引）
				if am != 0 {
					collectWarn("pg_am（索引访问方法 OID " + strconv.FormatUint(uint64(am), 10) + " 无法解析）")
					stmts = append(stmts, fmt.Sprintf("-- 索引 %s 的访问方法（pg_am OID %d）无法离线解析，已按 btree 输出，需人工核对",
						engine.QuoteName(ix.name), am))
				}
				am = 403 // 兜底 btree
				useAM = "btree"
			}
			uniq := ""
			if ix.uniq {
				uniq = "UNIQUE "
			}
			// v1.0.61（P2-4）：INCLUDE 列单独输出 —— 拼进键列表会把
			// UNIQUE (a) INCLUDE (b) 变成 UNIQUE (a,b)，唯一性被收紧。
			incl := ""
			if len(ix.include) > 0 {
				parts := make([]string, 0, len(ix.include))
				for _, n := range ix.include {
					if nm, ok := colNameByNum[n]; ok {
						parts = append(parts, engine.QuoteName(nm))
					}
				}
				if len(parts) == len(ix.include) {
					incl = " INCLUDE (" + strings.Join(parts, ", ") + ")"
				}
			}
			stmts = append(stmts, fmt.Sprintf("CREATE %sINDEX %s ON %s.%s USING %s (%s)%s%s;",
				uniq, engine.QuoteName(ix.name), engine.QuoteName(tm.Schema), engine.QuoteName(tm.RelName), useAM, strings.Join(keyParts, ", "), incl, where))
		}
	} // v1.0.52 分区子表索引跳过分支闭合
	// 7.1b v1.0.63（B-7）：UNIQUE / EXCLUDE 约束本体
	for _, cr := range ueRows {
		var keyParts []string
		keysOK := len(cr.keys) > 0
		for _, n := range cr.keys {
			if nm, ok := colNameByNum[int(n)]; ok {
				keyParts = append(keyParts, engine.QuoteName(nm))
			} else {
				keysOK = false
			}
		}
		if !keysOK {
			stmts = append(stmts, fmt.Sprintf("-- 约束 %s 的列无法解析（conkey 含已删除列），需人工补建", engine.QuoteName(cr.name)))
			continue
		}
		var idx idxRow
		hasIdx := false
		if i, ok := idxByInd[cr.indid]; ok {
			idx, hasIdx = idxList[i], true
		}
		incl := ""
		if hasIdx && len(idx.include) > 0 {
			parts := make([]string, 0, len(idx.include))
			okAll := true
			for _, n := range idx.include {
				if nm, ok := colNameByNum[n]; ok {
					parts = append(parts, engine.QuoteName(nm))
				} else {
					okAll = false
				}
			}
			if okAll && len(parts) > 0 {
				incl = " INCLUDE (" + strings.Join(parts, ", ") + ")"
			}
		}
		deferClause := ""
		if cr.deferrable {
			deferClause = " DEFERRABLE"
			if cr.deferred {
				deferClause += " INITIALLY DEFERRED"
			}
		}
		if cr.typ == 'u' {
			nullsND := ""
			if hasIdx && idx.nullsND {
				nullsND = " NULLS NOT DISTINCT"
			}
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s.%s ADD CONSTRAINT %s UNIQUE%s (%s)%s%s;",
				engine.QuoteName(tm.Schema), engine.QuoteName(tm.RelName), engine.QuoteName(cr.name),
				nullsND, strings.Join(keyParts, ", "), incl, deferClause))
			continue
		}
		// EXCLUDE：每个键列配一个 operator（pg_constraint.conexclop）
		if len(cr.ops) != len(cr.keys) {
			stmts = append(stmts, fmt.Sprintf("-- EXCLUDE 约束 %s 的 conexclop 与 conkey 数量不一致，需人工补建", engine.QuoteName(cr.name)))
			collectWarn("pg_constraint.conexclop")
			continue
		}
		elem := make([]string, 0, len(cr.keys))
		opsOK := true
		opcMap := OpclassMapCached(dbDir, version, isKB)
		for i, n := range cr.keys {
			nm, ok := colNameByNum[int(n)]
			opName := opMapD[cr.ops[i]].Name
			if !ok || opName == "" {
				opsOK = false
				break
			}
			item := engine.QuoteName(nm)
			// v1.0.63：非默认 opclass 必须写出（gist 上的 int4_ops 来自 btree_gist，
			// 省略会报 has no default operator class for access method "gist"）
			if hasIdx && i < len(idx.indclass) {
				if oc, found := opcMap[idx.indclass[i]]; found && !oc.Default && oc.Name != "" {
					if oc.Schema != "" && oc.Schema != "pg_catalog" {
						item += " " + engine.QuoteName(oc.Schema) + "." + engine.QuoteName(oc.Name)
					} else {
						item += " " + engine.QuoteName(oc.Name)
					}
				}
			}
			elem = append(elem, item+" WITH "+opName)
		}
		if !opsOK {
			stmts = append(stmts, fmt.Sprintf("-- EXCLUDE 约束 %s 的 operator 无法离线解析，需人工补建（源库 pg_get_constraintdef 可查）", engine.QuoteName(cr.name)))
			collectWarn("pg_constraint.conexclop")
			continue
		}
		am := ""
		if hasIdx {
			am = amName[idx.am]
		}
		if am == "" {
			// v1.0.63：EXCLUDE 的访问方法拿不到时绝不能猜 btree（gist 排它约束会变成
			// btree 排它约束，回灌直接报错或语义完全错），改为注释 + 告警。
			stmts = append(stmts, "-- EXCLUDE 约束 "+engine.QuoteName(cr.name)+" 的访问方法无法离线解析，需人工补建（源库 pg_get_constraintdef 可查）")
			collectWarn("pg_am（EXCLUDE 约束访问方法）")
			continue
		}
		where := ""
		if hasIdx && idx.predRaw != "" {
			varAt := func(attno int) string {
				if nm, ok := colNameByNum[attno]; ok {
					return engine.QuoteName(nm)
				}
				return ""
			}
			w := nt.DeparseNodeTree(idx.predRaw, opMapD, procMapD, varAt)
			if w == "" {
				stmts = append(stmts, "-- EXCLUDE 约束 "+engine.QuoteName(cr.name)+" 的 WHERE 谓词无法离线还原，需人工补建")
				collectWarn("pg_index.indpred")
				continue
			}
			where = " WHERE (" + w + ")"
		}
		stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s.%s ADD CONSTRAINT %s EXCLUDE USING %s (%s)%s%s%s;",
			engine.QuoteName(tm.Schema), engine.QuoteName(tm.RelName), engine.QuoteName(cr.name),
			am, strings.Join(elem, ", "), incl, where, deferClause))
	}
	// 7.2 序列：CREATE SEQUENCE + 默认值（DDL 内联 DEFAULT）+ setval 同步
	// 列序号排序（稳定输出）
	seqNums := make([]int, 0, len(colSeq))
	for n := range colSeq {
		seqNums = append(seqNums, n)
	}
	sort.Ints(seqNums)
	// v1.0.63 修复（B-6）：CREATE SEQUENCE 必须带 AS/INCREMENT/MINVALUE/MAXVALUE/
	// START/CACHE/CYCLE 全部选项，否则回灌后步长回到 1、缓存回到 1。
	// 目录只扫一次（逐表生成时反复查询）。
	seqFull := SequencesCached(dbDir, version, isKB)
	seenSeq := map[string]bool{}
	seenSync := map[string]bool{} // 同一序列被多列引用时 setval 只输出一次
	for _, n := range seqNums {
		nm, ok := colNameByNum[n]
		if !ok {
			continue
		}
		si := colSeq[n]
		seqKey := si.Schema + "." + si.Name
		full := seqFull[seqKey]
		if !seenSeq[seqKey] {
			seenSeq[seqKey] = true
			if full != nil {
				stmts = append(stmts, full.CreateSQL(engine.QuoteName))
			} else {
				stmts = append(stmts, fmt.Sprintf("CREATE SEQUENCE IF NOT EXISTS %s.%s;",
					engine.QuoteName(si.Schema), engine.QuoteName(si.Name)))
			}
		}
		// v1.0.61 修复（P1-8）：regclass 实参是单引号字面量，标识符里的 ' 必须先
		// 按字面量规则翻倍（QuoteName 只处理双引号），否则表/序列名含 ' 直接破句。
		seqReg := nt.QuoteSQLLiteral(engine.QuoteName(si.Schema) + "." + engine.QuoteName(si.Name))
		stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s.%s ALTER COLUMN %s SET DEFAULT nextval(%s::regclass);",
			engine.QuoteName(tm.Schema), engine.QuoteName(tm.RelName), engine.QuoteName(nm), seqReg))
		// v1.0.63 修复（B-6 续）：setval 用序列真实的 last_value/is_called，
		// 不再用 MAX(col) —— 源库删过行/序列被多表共享时 MAX(col) 偏小，
		// 回灌后立即碰撞主键。读不到当前值才退回 MAX(col)。
		if seenSync[seqKey] {
			continue
		}
		seenSync[seqKey] = true
		setval := ""
		if full != nil {
			setval = full.SetvalSQL(engine.QuoteName, nt.QuoteSQLLiteral)
		}
		if setval != "" {
			seqSync = append(seqSync, setval)
		} else {
			seqSync = append(seqSync, fmt.Sprintf("SELECT setval(%s, COALESCE((SELECT MAX(%s) FROM %s.%s), 1), true);",
				seqReg, engine.QuoteName(nm), engine.QuoteName(tm.Schema), engine.QuoteName(tm.RelName)))
		}
	}
	// 7.2b v1.0.63（B-8）：IDENTITY 列的序列由 CREATE TABLE 自动创建，
	// 但它的当前值必须同步，否则回灌后首个 nextval 与已导入的主键相撞。
	// 稳定顺序：按序列名排序。
	if len(seqFull) > 0 {
		idSeqs := make([]*SequenceInfo, 0, 2)
		for _, si := range seqFull {
			if si.Internal && si.OwnerRel == targetOID {
				idSeqs = append(idSeqs, si)
			}
		}
		sort.Slice(idSeqs, func(i, j int) bool { return idSeqs[i].QualifiedName() < idSeqs[j].QualifiedName() })
		for _, si := range idSeqs {
			if s := si.SetvalSQL(engine.QuoteName, nt.QuoteSQLLiteral); s != "" {
				seqSync = append(seqSync, s)
			}
		}
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
			engine.QuoteName(tm.Schema), engine.QuoteName(tm.RelName), engine.QuoteName(nm), colDefaultExpr[n]))
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
				engine.QuoteName(nm)))
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
				engine.QuoteName(nm)))
		}
	}
	// 7.4 注释
	for _, c := range comments {
		if c.objsubid == 0 {
			stmts = append(stmts, fmt.Sprintf("COMMENT ON TABLE %s.%s IS %s;",
				engine.QuoteName(tm.Schema), engine.QuoteName(tm.RelName), datatype.SQLStringLiteral(c.text)))
		} else if nm, ok := colNameByNum[c.objsubid]; ok {
			stmts = append(stmts, fmt.Sprintf("COMMENT ON COLUMN %s.%s.%s IS %s;",
				engine.QuoteName(tm.Schema), engine.QuoteName(tm.RelName), engine.QuoteName(nm), datatype.SQLStringLiteral(c.text)))
		}
	}
	return stmts, seqSync, warn
}

// buildCompositeAttrs 读取 pg_attribute(1249) 按 attrelid 聚合字段（复合类型行类型）
func buildCompositeAttrs(dbDir string, version int, isKB bool) map[uint32][]*datatype.AttrRow {
	datatype.CompositeAttrs = map[uint32][]*datatype.AttrRow{}
	path := filepath.Join(dbDir, strconv.Itoa(PG_ATTRIBUTE_RELFILE))
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return datatype.CompositeAttrs
	}
	for tup := range engine.IterSysTuples(path, version, isKB) {
		ar := engine.AttrFields(tup, version, isKB)
		if ar == nil || ar.AttRelID == 0 || ar.AttNum < 1 || ar.AttIsDropped {
			continue
		}
		datatype.CompositeAttrs[ar.AttRelID] = append(datatype.CompositeAttrs[ar.AttRelID], ar)
	}
	for _, list := range datatype.CompositeAttrs {
		sort.Slice(list, func(i, j int) bool { return list[i].AttNum < list[j].AttNum })
	}
	return datatype.CompositeAttrs
}

// ScanDynamic：动态类型解码所需的目录扫描（v1.0.57 目录重构）。
// 原 datatype.initDynamicDecoders 内的目录读取逻辑集中于此并通过
// datatype.DynamicScanner 注入，避免 datatype 反向依赖 engine/catalog。
func ScanDynamic(dbDir string, version int, isKB bool) *datatype.DynamicScan {
	typeNspNames = buildTypeNspMap(dbDir, version, isKB)
	return &datatype.DynamicScan{
		KbOracleMode:   detectKbOracleMode(dbDir),
		RegClassNames:  buildRegclassNameMap(dbDir, version, isKB),
		TypeDefs:       buildTypeDefs(dbDir, version, isKB),
		CompositeAttrs: buildCompositeAttrs(dbDir, version, isKB),
		ProcNames:      BuildProcNameMap(dbDir, version, isKB),
	}
}

// BuildOperatorMap 读 pg_operator（2617）构建 oid → 算符映射。
// pg_operator 在 PG12-18/金仓均保留物理 oid 列（首用户列），engine.NamedTableLayout
// 会把 oid 作为第一个命名列带出。
func BuildOperatorMap(dbDir string, version int, isKB bool) map[uint32]nt.OpInfo {
	m := map[uint32]nt.OpInfo{}
	p := engine.DetectSysFile(dbDir, 2617, map[string]bool{"pg_operator": true, "sys_operator": true}, 0)
	if p == "" {
		return m
	}
	cols := engine.NamedTableLayout(dbDir, 2617, version, isKB)
	if len(cols) == 0 {
		return m
	}
	for tup := range engine.IterSysTuples(p, version, isKB) {
		if !tup.IsLive() {
			continue
		}
		f, _ := engine.ParseNamedTuple(tup, cols, false, false)
		if len(f) == 0 {
			continue
		}
		oid := engine.FU32(f, "oid")
		name := engine.FCstr(f, "oprname")
		if oid == 0 || name == "" {
			continue
		}
		m[oid] = nt.OpInfo{Name: name, Kind: engine.FByte(f, "oprkind")}
	}
	return m
}

// PartKeyInfo：分区父表的策略与键（pg_partitioned_table 一行）
type PartKeyInfo struct {
	Strat byte   // 'r'=RANGE 'l'=LIST 'h'=HASH
	Cols  []int  // 分区键列号（partattrs；0 位=表达式键）
	Exprs string // partexprs 原文（pg_node_tree 列表，与 0 位一一对应）；空=无表达式键
}

// partMeta：库级分区元数据（懒加载 + 每库缓存）
type partMeta struct {
	ByParent map[uint32]*PartKeyInfo // 父表 OID → 策略/键
	BoundBy  map[uint32]string       // 子表 OID → relpartbound 节点树原文
	ParentOf map[uint32]uint32       // 子表 OID → 父表 OID
}

var partMetaCache sync.Map

func LoadPartMeta(dbDir string, version int, isKB bool) *partMeta {
	if dbDir == "" {
		return nil
	}
	if v, ok := partMetaCache.Load(partMetaKey(dbDir, version, isKB)); ok {
		return v.(*partMeta)
	}
	pm := &partMeta{
		ByParent: map[uint32]*PartKeyInfo{},
		BoundBy:  map[uint32]string{},
		ParentOf: map[uint32]uint32{},
	}
	// 1. pg_inherits：父子映射（固定布局，与 buildTableMeta 同一判据）
	knownInh := map[string]bool{"pg_inherits": true, "sys_inherits": true}
	if p := engine.DetectSysFile(dbDir, PG_INHERITS_RELFILE, knownInh, 0); p != "" {
		for tup := range engine.IterSysTuples(p, version, isKB) {
			fields := datatype.ExtractFieldsDirect(tup.Raw, tup.THoff, tup.GetNulls(), []datatype.ColLen{{AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}})
			if len(fields) < 2 || fields[0] == nil || fields[1] == nil {
				continue
			}
			pm.ParentOf[datatype.U32(*fields[0], 0)] = datatype.U32(*fields[1], 0)
		}
	}
	// 2. pg_partitioned_table：父表策略/键（named 布局，PG13+ 多 partdefid 列不影响前置列）
	if cols := engine.NamedTableLayout(dbDir, 3350, version, isKB); len(cols) > 0 {
		if p := engine.DetectSysFile(dbDir, 3350, map[string]bool{"pg_partitioned_table": true, "sys_partitioned_table": true}, 0); p != "" {
			for tup := range engine.IterSysTuples(p, version, isKB) {
				f, _ := engine.ParseNamedTuple(tup, cols, false, false)
				if len(f) == 0 {
					continue
				}
				pb, ok := f["partrelid"]
				if !ok || len(pb) < 4 {
					continue
				}
				oid := datatype.U32(pb, 0)
				ki := &PartKeyInfo{}
				if sb, ok := f["partstrat"]; ok && len(sb) >= 1 {
					ki.Strat = sb[0]
				}
				if ab, ok := f["partattrs"]; ok && len(ab) > 0 {
					ki.Cols = indexIndkey(&ab) // int2vector 与 pg_index.indkey 同构
				}
				if eb, ok := f["partexprs"]; ok && len(eb) > 0 {
					ki.Exprs = string(datatype.VarlenaPayloadBytes(eb))
				}
				pm.ByParent[oid] = ki
			}
		}
	}
	// 3. pg_class.relpartbound：子表边界节点树（named 布局整扫一次，库级缓存）
	// 注意：PG12+ 的 pg_class oid 是 attnum=1 的普通列（行首无 oid 系统列），
	// 必须 hasOID=false，行 OID 从 f["oid"] 取（v1.0.52 实测：hasOID=true 会整体错位 4B）。
	if cols := engine.NamedTableLayout(dbDir, 1259, version, isKB); len(cols) > 0 {
		if p := engine.DetectSysFile(dbDir, 1259, map[string]bool{"pg_class": true, "sys_class": true}, 0); p != "" {
			for tup := range engine.IterSysTuples(p, version, isKB) {
				f, _ := engine.ParseNamedTuple(tup, cols, false, false)
				oidB, ok2 := f["oid"]
				if !ok2 || len(oidB) < 4 {
					continue
				}
				rowOID := datatype.U32(oidB, 0)
				if bb, ok := f["relpartbound"]; ok && len(bb) > 0 {
					if txt := string(datatype.VarlenaPayloadBytes(bb)); txt != "" {
						pm.BoundBy[rowOID] = txt
					}
				}
			}
		}
	}
	// v1.0.61（P2-5）：整目录都没读到时不写缓存 —— 若首次调用发生在
	// version/页大小尚未确定（或数据文件暂不可读）时，空结果一旦缓存，
	// 后续参数正确的调用也永远拿不到分区元数据。
	if len(pm.ByParent) == 0 && len(pm.BoundBy) == 0 && len(pm.ParentOf) == 0 {
		return pm
	}
	v, _ := partMetaCache.LoadOrStore(partMetaKey(dbDir, version, isKB), pm)
	return v.(*partMeta)
}

// partMetaKey：分区元数据缓存键（v1.0.61 修复 P2-5）。
// 旧实现只用 dbDir：同一进程内先以错误 version/isKB 解析过一次，
// 之后正确参数的调用也命中错误缓存（空元数据永久生效）。
func partMetaKey(dbDir string, version int, isKB bool) string {
	return fmt.Sprintf("%s|%d|%t", dbDir, version, isKB)
}

// DeparsePartExprs：partexprs 节点树列表 → 表达式数组（varAt 引用表列名）。
func DeparsePartExprs(raw string, dbDir string, version int, isKB bool, nameByCol map[int]string) []string {
	if raw == "" {
		return nil
	}
	varAt := func(pos int) string {
		if nm, ok := nameByCol[pos]; ok {
			return engine.QuoteName(nm)
		}
		return ""
	}
	return nt.DeparseNodeTreeList(raw, BuildOperatorMap(dbDir, version, isKB), BuildProcNameMap(dbDir, version, isKB), varAt)
}
