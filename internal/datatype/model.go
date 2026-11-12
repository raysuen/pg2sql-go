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
	// v1.0.63 修复（B-8）：attidentity（'a'=GENERATED ALWAYS / 'd'=BY DEFAULT / ''=无）
	// 与 attcollation（非默认排序规则时非 0）此前全代码库未采集，
	// 导出 DDL 里 IDENTITY 列退化成普通列、COLLATE 丢失。
	AttIdentity  string
	AttCollation uint32
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
	// TypDefault：域类型的 DEFAULT 表达式原文（pg_type.typdefault，text）。
	// v1.2.17 新增：此前完全没采集，`CREATE DOMAIN d AS int DEFAULT 7` 导出成
	// `CREATE DOMAIN d AS int`，回灌后列默认值丢失（表级默认值不受影响）。
	// 优先取 text 列：它已是可直接落 SQL 的字面量（实测 PG17
	// `'ab'::character varying` / `1.50`，金仓 V9 `7` / `'dflt'::text`）。
	TypDefault string
	// TypDefaultBin：pg_type.typdefaultbin 原文（pg_node_tree）。
	// 金仓（V8R6/V9R1C10 均实测）只写这一列，typdefault 为 NULL——服务端查询
	// 能给值，是它读取时现从节点树算的。TypDefault 为空时由 catalog 层用
	// nt 反解本字段补齐，反解失败则留空（宁可少输出 DEFAULT，不输出错值）。
	TypDefaultBin string
}

// 列长度信息
type ColLen struct {
	AttLen    int
	IsVarlena bool
	AttAlign  string
	// Virtual：PG18+ 虚拟生成列（attgenerated='v'）。该列【不落盘】，
	// 物理布局必须保留槽位（保持 nulls 位图按 attnum 对齐），但
	// ExtractFieldsDirect/calculateTupleSize 在此槽位不消费任何字节。
	Virtual bool
}

func LayoutAlign(attlen int, attalign string) int {
	if attalign != "" {
		// v1.0.62 修复（P3）：PG 的 pg_attribute.attalign 是小写 'c'/'s'/'i'/'d'，
		// 旧实现只认大写 "S"，2 字节对齐的列全部落到 attlen 兜底判断
		//（int2 恰好也是 2，侥幸一致；但 char(n)/自定长类型会错位）。
		// 两种大小写都接受，避免上游/自定义目录写入大小写不一致时静默滑位。
		switch attalign {
		case "c", "C":
			return 1
		case "s", "S":
			return 2
		case "i", "I":
			return 4
		case "d", "D":
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
// ExtractFieldsDirect 保持原签名（系统目录/复合类型等调用方不关心降级细节）。
func ExtractFieldsDirect(raw []byte, tHoff int, nulls []bool, colLengths []ColLen) []*[]byte {
	fields, _ := ExtractFieldsDirectEx(raw, tHoff, nulls, colLengths)
	return fields
}

// ExtractFieldsDirectEx 是 ExtractFieldsDirect 的带诊断版本。
//
// v1.2.13 修复（P2→P1）：列头非法/越界时旧实现只是把该列及其后各列置 nil
// （= 输出 NULL），既无计数也无日志，报告里 bad_blocks 依旧为 0 —— 回灌成功、
// 报告全绿、值悄悄变成 NULL，与 USER_GUIDE「会输出警告日志」的承诺矛盾。
// 现在把"首个降级的逻辑列号"返回给调用方（数据路径据此按表计数并告警）。
//
// 返回值 badCol：-1 表示整行完好；>=0 表示从该列起被降级为 NULL。
func ExtractFieldsDirectEx(raw []byte, tHoff int, nulls []bool, colLengths []ColLen) ([]*[]byte, int) {
	pos := 0
	badCol := -1
	fields := make([]*[]byte, len(colLengths))
	nraw := len(raw)
	for i, item := range colLengths {
		align := LayoutAlign(item.AttLen, item.AttAlign)
		// v1.0.64 修复（E-1）：虚拟生成列不落盘，此槽位不消费字节；
		// 槽位本身保留（fields[i]=nil），保证 nulls 下标与 attnum 对齐不被破坏。
		if item.Virtual {
			fields[i] = nil
			continue
		}
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
				badCol = i
				break
			}
			kind, total, _, _ := VarlenaParse(raw, offset)
			ok := false
			switch kind {
			case VARLENA_EXTERNAL:
				if offset+KB_EXTERNAL_SIZE <= nraw {
					f := raw[offset : offset+KB_EXTERNAL_SIZE]
					fields[i] = &f
					pos += KB_EXTERNAL_SIZE
					ok = true
				}
			case VARLENA_1B, VARLENA_4B, VARLENA_4BC:
				if offset+total <= nraw {
					f := raw[offset : offset+total]
					fields[i] = &f
					pos += total
					ok = true
				}
			}
			if !ok {
				// v1.0.59 修复（P1-2）：失败列置 nil 并终止本行剩余列。
				// 旧行为 switch 内 break 不推进 pos，后续列从同一偏移重新探测——
				// 同值被复制到后续列，或对齐滑动后"碰巧"解析出垃圾。
				// 列越界/头非法后布局已不可信，剩余列统一 nil（证据安全）。
				fields[i] = nil
				for j := i + 1; j < len(fields); j++ {
					fields[j] = nil
				}
				badCol = i
				break
			}
		} else {
			if align > 1 {
				pos = (pos + align - 1) &^ (align - 1)
			}
			offset := tHoff + pos
			if offset+item.AttLen > nraw {
				fields[i] = nil
				badCol = i
				break
			}
			f := raw[offset : offset+item.AttLen]
			fields[i] = &f
			pos += item.AttLen
		}
	}
	return fields, badCol
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
