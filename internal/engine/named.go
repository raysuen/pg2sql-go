// Author: raysuen
package engine

import (
	"encoding/binary"
	"pg2sql/internal/datatype"
	"sort"
)

// NamedCol：带名字的列布局（从 pg_attribute 实时读取，版本感知）
type NamedCol struct {
	Name   string
	AttNum int
	datatype.ColLen
}

// NamedTableLayout：按 pg_attribute 读取 relOID 的用户列布局（含列名/attnum）
func NamedTableLayout(dbDir string, relOID uint32, version int, isKB bool) []NamedCol {
	p := DetectSysFile(dbDir, 1249, map[string]bool{"pg_attribute": true, "sys_attribute": true}, 0)
	if p == "" {
		return nil
	}
	var cols []NamedCol
	for tup := range IterSysTuples(p, version, isKB) {
		ar := AttrFields(tup, version, isKB)
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
		cols = append(cols, NamedCol{Name: ar.AttName, AttNum: ar.AttNum, ColLen: cl})
	}
	if len(cols) == 0 {
		return nil
	}
	sort.Slice(cols, func(i, j int) bool { return cols[i].AttNum < cols[j].AttNum })
	return cols
}

// ParseNamedTuple：按命名布局提取行字段（支持 oid 系统列在行首的 hasoids 表）。
// hasOID=true 时行数据自 tHoff 起为 oid(4B) + 用户列；null 位图仅覆盖用户列。
func ParseNamedTuple(tup *HeapTuple, cols []NamedCol, hasOID bool, skipOIDMeta bool) (map[string][]byte, uint32) {
	raw := tup.Raw
	nraw := len(raw)
	nulls := tup.GetNulls()
	out := map[string][]byte{}
	var oid uint32
	pos := 0
	base := tup.THoff
	if hasOID {
		if base+4 <= nraw {
			oid = datatype.U32(raw, base)
		}
		pos = 4
	}
	for _, item := range cols {
		align := datatype.LayoutAlign(item.AttLen, item.AttAlign)
		// PG 标准 hasoids 表：pg_attribute 中 oid 系统列（attnum=1）为元数据行，
		// 数据区已在行首单独读取，必须跳过否则后续列整体错位 4B。
		// 金仓（isKB）hasoids 语义不同，不跳过。
		if skipOIDMeta && hasOID && item.AttNum == 1 && (item.Name == "oid" || item.Name == "xoid") {
			continue
		}
		// v1.0.59 修复（P1-1）：GetNulls 的下标语义是 attnum-1（t_bits 覆盖
		// 全部 natts 属性，含已 DROP 列）。旧实现用过滤后 cols 下标 i 索引，
		// PG12+ 目录（pg_proc 等）首列是已 DROP 的 oid（attnum=1）被
		// NamedTableLayout 过滤后，从第一个真实列起 NULL 判定整体错位一列——
		// prosrc 被误判 NULL → PG12+ 全部用户函数体导出为空。
		if idx := item.AttNum - 1; idx >= 0 && idx < len(nulls) && nulls[idx] {
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
				break
			}
			kind, total, _, _ := datatype.VarlenaParse(raw, offset)
			switch kind {
			case datatype.VARLENA_EXTERNAL:
				if offset+datatype.KB_EXTERNAL_SIZE > nraw {
					pos++
					continue
				}
				out[item.Name] = raw[offset : offset+datatype.KB_EXTERNAL_SIZE]
				pos += datatype.KB_EXTERNAL_SIZE
			case datatype.VARLENA_1B:
				if offset+total > nraw {
					pos++
					continue
				}
				out[item.Name] = raw[offset : offset+total]
				pos += total
			case datatype.VARLENA_4B, datatype.VARLENA_4BC:
				if offset+total > nraw {
					pos++
					continue
				}
				out[item.Name] = raw[offset : offset+total]
				pos += total
			default:
				pos++
			}
		} else {
			if align > 1 {
				pos = (pos + align - 1) &^ (align - 1)
			}
			alen := item.AttLen
			if alen <= 0 {
				alen = 1
			}
			offset := base + pos
			if offset+alen > nraw {
				pos += alen
				continue
			}
			out[item.Name] = raw[offset : offset+alen]
			pos += alen
		}
	}
	return out, oid
}

func FU32(f map[string][]byte, name string) uint32 {
	b, ok := f[name]
	if !ok || len(b) < 4 {
		return 0
	}
	return binary.LittleEndian.Uint32(b)
}

func FU16(f map[string][]byte, name string) uint16 {
	b, ok := f[name]
	if !ok || len(b) < 2 {
		return 0
	}
	return binary.LittleEndian.Uint16(b)
}

// FInt64：读取 int8 定长列（pg_sequence.seqstart 等）。缺失/长度不足返回 0。
func FInt64(f map[string][]byte, name string) int64 {
	b, ok := f[name]
	if !ok || len(b) < 8 {
		return 0
	}
	return int64(binary.LittleEndian.Uint64(b))
}

// FInt32：读取 int4 定长列（有符号）
func FInt32(f map[string][]byte, name string) int32 {
	b, ok := f[name]
	if !ok || len(b) < 4 {
		return 0
	}
	return int32(binary.LittleEndian.Uint32(b))
}

func FByte(f map[string][]byte, name string) byte {
	b, ok := f[name]
	if !ok || len(b) < 1 {
		return 0
	}
	return b[0]
}

func FBool(f map[string][]byte, name string) bool {
	return FByte(f, name) != 0
}

func FCstr(f map[string][]byte, name string) string {
	b, ok := f[name]
	if !ok {
		return ""
	}
	return datatype.CString(b, 0)
}
