// Author: raysuen
package datatype

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
	AttGenerated string // '' 普通；'S'=存储生成列（PG12+，金仓 V9 同布局；V8 无此列恒空）
	TXmin        uint32
}

// TypeDef：pg_type 行的关键字段（方案 C 动态发现）
type TypeDef struct {
	OID      uint32
	Name     string
	Nsp      uint32 // typnamespace（v1.0.48：DDL 需按真实 schema 限定类型名）
	Len      int
	ByVal    bool
	Type     byte // B/d/e/c/p/r/m
	Category byte
	Elem     uint32 // typelem
	Array    uint32 // typarray
	Input    uint32 // typinput（pg_proc oid）
	Base     uint32 // typbasetype（domain）
	Align    byte   // typalign
	TypRelid uint32 // typrelid（复合类型对应的行类型 pg_class oid）
	NotNull  bool   // typnotnull（域类型 NOT NULL 约束）
}

// 列长度信息
type ColLen struct {
	AttLen    int
	IsVarlena bool
	AttAlign  string
}

func LayoutAlign(attlen int, attalign string) int {
	if attalign != "" {
		switch attalign {
		case "c":
			return 1
		case "S":
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

// ---------- 字段提取（PG 磁盘格式顺序提取） ----------
func ExtractFieldsDirect(raw []byte, tHoff int, nulls []bool, colLengths []ColLen) []*[]byte {
	pos := 0
	fields := make([]*[]byte, len(colLengths))
	nraw := len(raw)
	for i, item := range colLengths {
		align := LayoutAlign(item.AttLen, item.AttAlign)
		if i < len(nulls) && nulls[i] {
			fields[i] = nil
			continue
		}
		if item.IsVarlena {
			// PG heap_fill_tuple 布局规则：short varlena（1B 头）与外部指针不对齐，
			// 仅 4B 头 varlena（含压缩 4BC）按 attalign 对齐。先探测原始位置（下一字段起始），
			// 非法（前为 0x00 padding）或未对齐的 4B/4BC 头时再按 attalign 对齐取。
			kind0, _, _, _ := VarlenaParse(raw, tHoff+pos)
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
			kind, total, _, _ := VarlenaParse(raw, offset)
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

// HEAP 页内元组头常量（原 heapfile 定义，下沉以便 datatype 自洽解析复合值）
const (
	HEAP_HASNULL    = 0x0001
	HEAP_NATTS_MASK = 0x07FF
)

// KB_EXTERNAL_SIZE：金仓外置存储指针长度（原 heapfile 常量，下沉 datatype 用）
const KB_EXTERNAL_SIZE = 18

// VarlenaPayloadBytes：varlena 原始字节 → payload 内容
func VarlenaPayloadBytes(b []byte) []byte {
	payload, _, _ := VarPayload(b)
	return payload
}
