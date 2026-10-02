// pg2sql types 层：PostgreSQL/金仓内置类型解码
// 对应 Python 版 pg2sql/types.py（v2.5），逐字节等价翻译
// Author: raysuen
package main

import (
	"os"
	"math/big"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var date2000 = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// ---------- 类型 OID ----------
const (
	BOOLOID        = 16
	BYTEAOID       = 17
	CHAROID        = 18
	NAMEOID        = 19
	INT8OID        = 20
	INT2OID        = 21
	INT2VECTOROID  = 22
	INT4OID        = 23
	REGPROCOID     = 24
	TEXTOID        = 25
	OIDOID         = 26
	TIDOID         = 27
	XIDOID         = 28
	CIDOID         = 29
	OIDVECTOROID   = 30
	PG_DDL_COMMAND = 32
	JSONOID        = 114
	XMLOID         = 142
	PG_NODE_TREE   = 194
	CIDROID        = 650
	FLOAT4OID      = 700
	FLOAT8OID      = 701
	UNKNOWNOID     = 705
	CIRCLEOID      = 718
	MONEYOID       = 790
	MACADDROID     = 829
	INETOID        = 869
	MACADDR8OID    = 774
	BPCHAROID      = 1042
	VARCHAROID     = 1043
	DATEOID        = 1082
	TIMEOID        = 1083
	TIMESTAMPOID   = 1114
	TIMESTAMPTZOID = 1184
	INTERVALOID    = 1186
	TIMETZOID      = 1266
	NUMERICOID     = 1700
	UUIDOID        = 2950
	JSONBOID       = 3802
	BITOID         = 1560
	VARBITOID      = 1562
	ACLITEMOID     = 1033
)

// ACL 权限字符
const aclChars = "arwdDxtXUCTcsAm"

// roleMap：角色 oid → 角色名（catalog 注入）
var roleMap = map[uint32]string{}

// EnumMember：枚举成员（按 sortorder 有序）
type EnumMember struct {
	MemberOID uint32
	Label     string
	SortOrder float32
}

// enumMap：{枚举类型 oid: 有序成员列表}（catalog 注入）
var enumMap = map[uint32][]EnumMember{}

func setRoleMap(m map[uint32]string) { roleMap = m }
func setEnumMap(m map[uint32][]EnumMember) {
	enumMap = m
	if enumMap == nil {
		enumMap = map[uint32][]EnumMember{}
	}
}

// ---------- varlena payload 提取 ----------
// varPayload 返回 (payload, isExternal, extInfo)
func varPayload(b []byte) ([]byte, bool, *ExternalInfo) {
	if len(b) == 0 {
		return b, false, nil
	}
	kind, total, _, _ := varlenaParse(b, 0)
	switch kind {
	case VARLENA_EXTERNAL:
		ext := parseExternalPointer(b, 0)
		if ext != nil {
			return []byte{}, true, ext
		}
		return []byte{}, false, nil
	case VARLENA_1B:
		p := total - 1
		if p < 0 {
			p = 0
		}
		if 1+p > len(b) {
			p = len(b) - 1
		}
		return b[1 : 1+p], false, nil
	case VARLENA_4B:
		p := total - 4
		if p < 0 {
			p = 0
		}
		if 4+p > len(b) {
			p = len(b) - 4
		}
		return b[4 : 4+p], false, nil
	case VARLENA_4BC:
		comp := b[4:total]
		if len(comp) >= 4 {
			rawlen := int(u32(comp, 0) & 0x3FFFFFFF)
			data := toastDecompress(comp, rawlen, TOAST_COMPRESS_METHOD_PGLZ)
			if data != nil {
				return data, false, nil
			}
		}
		return []byte{}, false, nil
	}
	return []byte{}, false, nil
}

// decodeTsquery：移植 PG tsqueryout（tsquery.c infix）中缀输出逻辑
// TSQueryData = [varlena头][int32 size][QueryItem(12B)*size][operands c-strings]
// QueryOperand(12B): type(1) weight(1) prefix(1) pad(1) valcrc(4) distance:20/length:12(4)
// QueryOperator(8B): type(1) oper(1) distance(2) left(4)；union 按 4B 对齐为 12B
func decodeTsquery(b []byte) string {
	payload, _, _ := varPayload(b)
	if os.Getenv("DBG_TSQ") != "" {
		fmt.Fprintf(os.Stderr, "[dbg] tsq b=%x payload=%x\n", b, payload)
	}
	if len(payload) < 8 {
		return decodeDefault(b)
	}
	size := int(int32(binary.LittleEndian.Uint32(payload)))
	if size <= 0 {
		return ""
	}
	itemsStart := 4
	itemsEnd := itemsStart + 12*size
	if itemsEnd > len(payload) {
		return decodeDefault(b)
	}
	operands := payload[itemsEnd:]
	curPos := 0
	var sb strings.Builder
	var infix func(out *strings.Builder, parentPriority int, rightPhraseOp bool)
	infix = func(out *strings.Builder, parentPriority int, rightPhraseOp bool) {
		if curPos >= size {
			return
		}
		item := payload[itemsStart+12*curPos : itemsStart+12*curPos+12]
		typ := item[0]
		switch typ {
		case 1: // QI_VAL
			weight := item[1]
			prefix := item[2]
			wordBits := binary.LittleEndian.Uint32(item[8:12])
			length := wordBits & 0xFFF // 低 12 位
			distance := wordBits >> 12 // 高 20 位
			if int(distance)+int(length) > len(operands) {
				curPos++
				return
			}
			op := operands[distance : distance+length]
			out.WriteByte('\'')
			for _, ch := range op {
				if ch == '\'' || ch == '\\' {
					out.WriteByte(ch)
				}
				out.WriteByte(ch)
			}
			out.WriteByte('\'')
			if weight != 0 || prefix != 0 {
				out.WriteByte(':')
				if prefix != 0 {
					out.WriteByte('*')
				}
				if weight&8 != 0 {
					out.WriteByte('A')
				}
				if weight&4 != 0 {
					out.WriteByte('B')
				}
				if weight&2 != 0 {
					out.WriteByte('C')
				}
				if weight&1 != 0 {
					out.WriteByte('D')
				}
			}
			curPos++
		case 2: // QI_OPR
			oper := item[1]
			priority := 0
			switch oper {
			case 1: // OP_NOT
				priority = 4
			case 2: // OP_AND
				priority = 2
			case 3: // OP_OR
				priority = 1
			case 4: // OP_PHRASE
				priority = 3
			}
			distance := int(int16(binary.LittleEndian.Uint16(item[2:4])))
			needParen := priority < parentPriority || (oper == 4 && rightPhraseOp)
			if needParen {
				out.WriteString("( ")
			}
			curPos++
			if oper == 1 { // NOT 一元
				out.WriteByte('!')
				infix(out, priority, false)
			} else {
				// 与 PG infix 完全一致：先递归右子树（暂存到临时 buf），
				// 再递归左子树（写主 buf），最后在主 buf 追加 " op " + 右子树文本
				var rightBuf strings.Builder
				infix(&rightBuf, priority, oper == 4) // right
				infix(out, priority, false)           // left
				switch oper {
				case 2:
					out.WriteString(" & ")
				case 3:
					out.WriteString(" | ")
				case 4:
					if distance != 1 {
						out.WriteString(fmt.Sprintf(" <%d> ", distance))
					} else {
						out.WriteString(" <-> ")
					}
				}
				out.WriteString(rightBuf.String())
			}
			if needParen {
				out.WriteString(" )")
			}
		default:
			curPos++
		}
	}
	infix(&sb, -1, false)
	return sb.String()
}

// ---------- 基础解码器 ----------
func decodeBool(b []byte) string {
	if len(b) == 0 {
		return "false"
	}
	v := b[0]
	if v == 1 || v == 0x74 || v == 't' || v == 0x31 {
		return "true"
	}
	return "false"
}

func decodeInt2(b []byte) string { return strconv.Itoa(int(int16(binary.LittleEndian.Uint16(b)))) }
func decodeInt4(b []byte) string { return strconv.Itoa(int(int32(binary.LittleEndian.Uint32(b)))) }
func decodeInt8(b []byte) string {
	return strconv.FormatInt(int64(binary.LittleEndian.Uint64(b)), 10)
}
func decodeInt16KB(b []byte) string {
	// 金仓 int16（4659）：16 字节小端有符号
	if len(b) < 16 {
		return "0"
	}
	lo := binary.LittleEndian.Uint64(b[:8])
	hi := binary.LittleEndian.Uint64(b[8:16])
	if hi == 0 {
		return strconv.FormatUint(lo, 10)
	}
	if hi == ^uint64(0) {
		// 负数：按 16 字节补码取低 8 字节并保持符号
		return strconv.FormatInt(int64(lo), 10)
	}
	// 超出 int64 范围：用 big.Int 完整表示
	bi := new(big.Int).SetUint64(hi)
	bi.Lsh(bi, 64)
	bi.Or(bi, new(big.Int).SetUint64(lo))
	if hi>>63 == 1 {
		max := new(big.Int).Lsh(big.NewInt(1), 128)
		max.Sub(max, big.NewInt(1))
		bi.Sub(max, bi)
		bi.Neg(bi)
	}
	return bi.String()
}

func fmtFloat(v float64) string {
	if math.IsNaN(v) {
		return "NaN"
	}
	if math.IsInf(v, 1) {
		return "Infinity"
	}
	if math.IsInf(v, -1) {
		return "-Infinity"
	}
	// 等价 Python repr（float_repr_style=short）：
	// 最短表示；指数在 [-4, 15] 时用定点，否则科学计数（指数两位）
	s := strconv.FormatFloat(v, 'g', -1, 64)
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		exp, err := strconv.Atoi(s[i+1:])
		if err == nil && exp >= -4 && exp <= 15 {
			s = strconv.FormatFloat(v, 'f', -1, 64)
			if !strings.ContainsAny(s, ".eE") {
				s += ".0"
			}
		}
		return s
	}
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}

func decodeFloat4(b []byte) string {
	return fmtFloat(float64(math.Float32frombits(binary.LittleEndian.Uint32(b))))
}
func decodeFloat8(b []byte) string {
	return fmtFloat(math.Float64frombits(binary.LittleEndian.Uint64(b)))
}

func decodeVarlenaText(b []byte) string {
	payload, _, _ := varPayload(b)
	return decodeBytes(payload)
}

func decodeText(b []byte) string   { return decodeVarlenaText(b) }
func decodeBpchar(b []byte) string { return decodeVarlenaText(b) }
func decodeVarchar(b []byte) string {
	return decodeVarlenaText(b)
}
func decodeName(b []byte) string { return cstring(b, 0) }

func decodeBytea(b []byte) string {
	payload, _, _ := varPayload(b)
	return "\\x" + hex.EncodeToString(payload)
}

func decodeOid(b []byte) string { return strconv.FormatUint(uint64(binary.LittleEndian.Uint32(b)), 10) }
func decodeXid(b []byte) string { return decodeOid(b) }
func decodeCid(b []byte) string { return decodeOid(b) }

func decodeTid(b []byte) string {
	blk := binary.LittleEndian.Uint32(b)
	off := binary.LittleEndian.Uint16(b[4:])
	return fmt.Sprintf("(%d,%d)", blk, off)
}

func decodeDate(b []byte) string {
	days := int32(binary.LittleEndian.Uint32(b))
	if days == 0x7FFFFFFF {
		return "infinity"
	}
	if days == -0x80000000 {
		return "-infinity"
	}
	// 2000-01-01 + days
	t := date2000.AddDate(0, 0, int(days))
	return t.Format("2006-01-02")
}

func timeFromUS(us int64) string {
	us %= 86400 * 1000000
	h := us / (3600 * 1000000)
	us %= 3600 * 1000000
	m := us / (60 * 1000000)
	us %= 60 * 1000000
	s := us / 1000000
	micro := us % 1000000
	if micro != 0 {
		return fmt.Sprintf("%02d:%02d:%02d.%s", h, m, s, strings.TrimRight(fmt.Sprintf("%06d", micro), "0"))
	}
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}

func decodeTime(b []byte) string {
	us := int64(binary.LittleEndian.Uint64(b))
	return timeFromUS(us)
}

// decodeMysqlTime 金仓 V9 mysql 模式 time（oid 7950）。
// 磁盘格式：int64 微秒自 0 点，支持 24:00:00（86400 秒，MySQL TIME 合法上限）。
// 与 decodeTime 不同：不做 86400 秒取模（避免 24:00:00 被归一化为 00:00:00）。
func decodeMysqlTime(b []byte) string {
	us := int64(binary.LittleEndian.Uint64(b))
	neg := false
	if us < 0 {
		neg = true
		us = -us
	}
	h := us / (3600 * 1000000)
	us %= 3600 * 1000000
	m := us / (60 * 1000000)
	us %= 60 * 1000000
	s := us / 1000000
	micro := us % 1000000
	var body string
	if micro != 0 {
		body = fmt.Sprintf("%02d:%02d:%02d.%s", h, m, s, strings.TrimRight(fmt.Sprintf("%06d", micro), "0"))
	} else {
		body = fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	}
	if neg {
		return "-" + body
	}
	return body
}

func decodeTimestamp(b []byte) string {
	us := int64(binary.LittleEndian.Uint64(b))
	if us == 0x7FFFFFFFFFFFFFFF {
		return "infinity"
	}
	if us == -0x8000000000000000 {
		return "-infinity"
	}
	return timestampFromUS(us)
}

func decodeTimestamptz(b []byte) string {
	us := int64(binary.LittleEndian.Uint64(b))
	if us == 0x7FFFFFFFFFFFFFFF {
		return "infinity"
	}
	if us == -0x8000000000000000 {
		return "-infinity"
	}
	return timestampFromUS(us) + "+00"
}

// decodeMysqlTimestamp 金仓 V9 mysql 模式 timestamp（oid 7954）。
// 磁盘格式：int64 微秒，基准 2000-01-01 00:00:00 UTC（mysql_timestamp_in 将本地时间转 UTC 存储，秒精度）。
// 输出与 mysql_timestamp_out 一致：按会话时区（实例默认 Asia/Singapore = UTC+8）转回本地时间文本。
func decodeMysqlTimestamp(b []byte) string {
	us := int64(binary.LittleEndian.Uint64(b))
	if us == 0x7FFFFFFFFFFFFFFF {
		return "infinity"
	}
	if us == -0x8000000000000000 {
		return "-infinity"
	}
	const utc8 = 8 * 3600 * 1000000
	return timestampFromUS(us + utc8)
}

func timestampFromUS(us int64) string {
	// 2000-01-01 00:00:00 + us
	sec := us / 1000000
	micro := us % 1000000
	if micro < 0 {
		// 2000 年前的微秒为负：向下取整，micro 归一化为正
		sec--
		micro += 1000000
	}
	t := date2000.Add(time.Duration(sec) * time.Second)
	base := t.Format("2006-01-02 15:04:05")
	if micro != 0 {
		return base + "." + strings.TrimRight(fmt.Sprintf("%06d", micro), "0")
	}
	return base
}

func decodeTimetz(b []byte) string {
	us := int64(binary.LittleEndian.Uint64(b))
	zone := int32(binary.LittleEndian.Uint32(b[8:]))
	t := timeFromUS(us)
	sign := "+"
	if zone > 0 {
		// PG timetz 时区字段：正数=UTC 以西（西时区），显示 -
		sign = "-"
	}
	z := zone
	if z < 0 {
		z = -z
	}
	return fmt.Sprintf("%s%s%02d:%02d", t, sign, z/3600, z%3600/60)
}

// tsvector 内部格式（PG ts_type.h）：
//   int32 size（词数，LE）
//   WordEntry[size]：uint32 位打包（小端：bit0=haspos，bit1-11=len 词长，bit12-31=pos 词字符串偏移）
//   词字符串按 entry 顺序连续存储；若 haspos=1，词后有 2 字节对齐填充 + uint16 位置数 + uint16 WordEntryPos[]
// WordEntryPos 为 uint16：位 13-0 位置，位 15-14 权重（0=A,1=B,2=C,3=D）
func decodeTsvector(b []byte) string {
	payload, _, _ := varPayload(b)
	b = payload
	if len(b) < 4 {
		return decodeDefault(b)
	}
	size := int32(binary.LittleEndian.Uint32(b[:4]))
	if size < 0 || 4+int(size)*4 > len(b) {
		return decodeDefault(b)
	}
	cur := 4 + int(size)*4 // data 区起点
	var parts []string
	for i := 0; i < int(size); i++ {
		e := binary.LittleEndian.Uint32(b[4+i*4:])
		haspos := e & 1
		l := int((e >> 1) & 0x7FF)
		if cur+l > len(b) {
			break
		}
		w := string(b[cur : cur+l])
		cur += l
		if haspos != 0 {
			if cur&1 != 0 {
				cur++ // 2 字节对齐填充
			}
			if cur+2 > len(b) {
				break
			}
			np := int(binary.LittleEndian.Uint16(b[cur:]))
			cur += 2
			var poss []string
			for j := 0; j < np && cur+2 <= len(b); j++ {
				p16 := binary.LittleEndian.Uint16(b[cur:])
				cur += 2
				p := p16 & 0x3FFF
				wgt := p16 >> 14
				s := strconv.Itoa(int(p))
				// PG tsvectorout：WEP 权重 3→'A'、2→'B'、1→'C'、0→不显示
				switch wgt {
				case 1:
					s += "C"
				case 2:
					s += "B"
				case 3:
					s += "A"
				}
				poss = append(poss, s)
			}
			wEsc := strings.Replace(w, "'", "''", -1)
			parts = append(parts, "'"+wEsc+"':"+strings.Join(poss, ","))
		} else {
			wEsc := strings.Replace(w, "'", "''", -1)
			parts = append(parts, "'"+wEsc+"'")
		}
	}
	return strings.Join(parts, " ")
}

func decodeInterval(b []byte) string {
	timeUS := int64(binary.LittleEndian.Uint64(b))
	day := int32(binary.LittleEndian.Uint32(b[8:]))
	month := int32(binary.LittleEndian.Uint32(b[12:]))
	// C 整除语义（向零截断）
	year := month / 12
	month -= year * 12
	var parts []string
	if year != 0 {
		if year == 1 {
			parts = append(parts, "1 year")
		} else {
			parts = append(parts, fmt.Sprintf("%d years", year))
		}
	}
	if month != 0 {
		if month == 1 {
			parts = append(parts, "1 mon")
		} else {
			parts = append(parts, fmt.Sprintf("%d mons", month))
		}
	}
	if day != 0 {
		if day == 1 || day == -1 {
			parts = append(parts, fmt.Sprintf("%d day", day))
		} else {
			parts = append(parts, fmt.Sprintf("%d days", day))
		}
	}
	sign := ""
	if timeUS < 0 {
		sign = "-"
	}
	t := timeUS
	if t < 0 {
		t = -t
	}
	us := t % 1000000
	totalS := t / 1000000
	hh := totalS / 3600
	rem := totalS % 3600
	mm := rem / 60
	ss := rem % 60
	timeStr := fmt.Sprintf("%s%02d:%02d:%02d", sign, hh, mm, ss)
	if us != 0 {
		timeStr += "." + strings.TrimRight(fmt.Sprintf("%06d", us), "0")
	}
	if timeUS != 0 {
		parts = append(parts, timeStr)
	}
	if len(parts) == 0 {
		return "00:00:00"
	}
	return strings.Join(parts, " ")
}

// ---------- numeric ----------
func decodeNumeric(b []byte) string {
	payload, _, _ := varPayload(b)
	return numericDiskToStr(payload)
}

func numericDiskToStr(payload []byte) string {
	if len(payload) < 2 {
		return "0"
	}
	header := binary.LittleEndian.Uint16(payload)
	flags := header & 0xC000
	if flags == 0xC000 {
		if header == 0xD000 {
			return "Infinity"
		}
		if header == 0xF000 {
			return "-Infinity"
		}
		return "NaN"
	}
	var neg bool
	var dscale, weight int
	var digitsRaw []byte
	if flags == 0x8000 {
		// Short 格式
		neg = header&0x2000 != 0
		dscale = int(header&0x1F80) >> 7
		if header&0x0040 != 0 {
			weight = int(^uint16(0x3F))|int(header&0x3F)
			weight &= 0xFFFF
			if weight&0x8000 != 0 {
				weight -= 0x10000
			}
		} else {
			weight = int(header & 0x3F)
		}
		digitsRaw = payload[2:]
	} else {
		// Long 格式
		if len(payload) < 4 {
			return "0"
		}
		neg = header&0x4000 != 0
		dscale = int(header & 0x3FFF)
		weight = int(int16(binary.LittleEndian.Uint16(payload[2:])))
		digitsRaw = payload[4:]
	}
	ndigits := len(digitsRaw) / 2
	digits := make([]int, ndigits)
	for i := 0; i < ndigits; i++ {
		digits[i] = int(binary.LittleEndian.Uint16(digitsRaw[i*2:]))
	}
	// 整数部分
	var intStr string
	fracStart := 0
	if weight < 0 {
		intStr = "0"
		fracStart = weight + 1
	} else {
		var sb strings.Builder
		first := 0
		if weight+1 <= ndigits {
			first = digits[0]
		}
		sb.WriteString(strconv.Itoa(first))
		for i := 1; i <= weight; i++ {
			d := 0
			if i < ndigits {
				d = digits[i]
			}
			fmt.Fprintf(&sb, "%04d", d)
		}
		intStr = strings.TrimLeft(sb.String(), "0")
		if intStr == "" {
			intStr = "0"
		}
		fracStart = weight + 1
	}
	// 小数部分
	var result string
	if dscale > 0 {
		ngroups := (dscale + 3) / 4
		var frac strings.Builder
		for g := 0; g < ngroups; g++ {
			di := fracStart + g
			d := 0
			if 0 <= di && di < ndigits {
				d = digits[di]
			}
			fmt.Fprintf(&frac, "%04d", d)
		}
		f := frac.String()
		if len(f) > dscale {
			f = f[:dscale]
		}
		result = intStr + "." + f
	} else {
		result = intStr
	}
	if neg {
		result = "-" + result
	}
	return result
}

// ---------- uuid/json ----------
func decodeUUID(b []byte) string {
	if len(b) < 16 {
		return ""
	}
	u := b[:16]
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

func decodeJSON(b []byte) string { return decodeVarlenaText(b) }

// ---------- jsonb（PG jsonb_util.c 语义） ----------
const (
	JB_FSCALAR      = 0x10000000
	JB_FOBJECT      = 0x20000000
	JB_FARRAY       = 0x40000000
	JENTRY_HAS_OFF  = 0x80000000
	JENTRY_TYPEMASK = 0x70000000
	JENTRY_ISNUM    = 0x10000000
	JENTRY_ISBOOLF  = 0x20000000
	JENTRY_ISBOOLT  = 0x30000000
	JENTRY_ISNULL   = 0x40000000
	JENTRY_ISCONTAIN = 0x50000000
)

func jsonbScalarToText(je uint32, payload []byte, dataOff, s, e int) string {
	t := je & JENTRY_TYPEMASK
	switch t {
	case 0:
		j, _ := json.Marshal(decodeBytes(payload[dataOff+s : dataOff+e]))
		return string(j)
	case JENTRY_ISNUM:
		aligned := dataOff + ((s + 3) &^ 3)
		vp, _, _ := varPayload(payload[aligned : dataOff+e])
		return numericDiskToStr(vp)
	case JENTRY_ISBOOLT:
		return "true"
	case JENTRY_ISBOOLF:
		return "false"
	case JENTRY_ISNULL:
		return "null"
	case JENTRY_ISCONTAIN:
		aligned := dataOff + ((s + 3) &^ 3)
		return jsonbContainerToText(payload[aligned : dataOff+e])
	}
	return "null"
}

func jsonbContainerToText(payload []byte) string {
	if len(payload) < 4 {
		return ""
	}
	header := binary.LittleEndian.Uint32(payload)
	flags := header & 0xF0000000
	count := int(header & 0x0FFFFFFF)
	scalar := flags&JB_FSCALAR != 0
	isObj := flags&JB_FOBJECT != 0
	nEnt := count
	if isObj {
		nEnt = count * 2
	}
	dataOff := 4 + 4*nEnt
	if dataOff > len(payload) {
		return ""
	}
	ents := make([]uint32, nEnt)
	for i := 0; i < nEnt; i++ {
		ents[i] = binary.LittleEndian.Uint32(payload[4+4*i:])
	}
	getOffset := func(idx int) int {
		off := 0
		for i := 0; i < idx; i++ {
			if ents[i]&JENTRY_HAS_OFF != 0 {
				off = int(ents[i] & 0x0FFFFFFF)
			} else {
				off += int(ents[i] & 0x0FFFFFFF)
			}
		}
		return off
	}
	jlen := func(idx int) int {
		if ents[idx]&JENTRY_HAS_OFF != 0 {
			return int(ents[idx]&0x0FFFFFFF) - getOffset(idx)
		}
		return int(ents[idx] & 0x0FFFFFFF)
	}
	emit := func(idx int) string {
		s := getOffset(idx)
		return jsonbScalarToText(ents[idx], payload, dataOff, s, s+jlen(idx))
	}
	if scalar {
		return emit(0)
	}
	if isObj {
		var sb strings.Builder
		sb.WriteString("{")
		keyOff := 0
		for i := 0; i < count; i++ {
			klen := jlen(i)
			key := decodeBytes(payload[dataOff+keyOff : dataOff+keyOff+klen])
			vs := getOffset(count + i)
			v := jsonbScalarToText(ents[count+i], payload, dataOff, vs, vs+jlen(count+i))
			kj, _ := json.Marshal(key)
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.Write(kj)
			sb.WriteString(": ")
			sb.WriteString(v)
			if ents[i]&JENTRY_HAS_OFF != 0 {
				keyOff = int(ents[i] & 0x0FFFFFFF)
			} else {
				keyOff += int(ents[i] & 0x0FFFFFFF)
			}
		}
		sb.WriteString("}")
		return sb.String()
	}
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i < count; i++ {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(emit(i))
	}
	sb.WriteString("]")
	return sb.String()
}

func decodeJSONB(b []byte) string {
	payload, _, _ := varPayload(b)
	return jsonbContainerToText(payload)
}

// ---------- 数组 ----------
// 常见数组元素类型 → (attlen, attalign, 解码函数, 文本退化?)
var arrayElemInfo = map[uint32][4]interface{}{
	BOOLOID:      {1, "c", "bool", false},
	BYTEAOID:     {-1, "i", "", true},
	CHAROID:      {1, "c", "char", false},
	NAMEOID:      {64, "c", "name", false},
	INT8OID:      {8, "d", "int8", false},
	INT2OID:      {2, "s", "int2", false},
	INT4OID:      {4, "i", "int4", false},
	TEXTOID:      {-1, "i", "", true},
	OIDOID:       {4, "i", "oid", false},
	FLOAT4OID:    {4, "i", "float4", false},
	FLOAT8OID:    {8, "d", "float8", false},
	UNKNOWNOID:   {-1, "i", "", true},
	BPCHAROID:    {-1, "i", "", true},
	VARCHAROID:   {-1, "i", "", true},
	DATEOID:      {4, "i", "date", false},
	TIMEOID:      {8, "d", "time", false},
	TIMESTAMPOID: {8, "d", "timestamp", false},
	TIMESTAMPTZOID: {8, "d", "timestamptz", false},
	TIMETZOID:    {12, "d", "timetz", false},
	INTERVALOID:  {16, "d", "interval", false},
	NUMERICOID:   {-1, "i", "", true},
	UUIDOID:      {16, "c", "uuid", false},
	JSONBOID:     {-1, "i", "jsonb", false},
	ACLITEMOID:   {12, "i", "aclitem", false},
}

// 数组类型 OID → 元素类型 OID
var arrayElemOID = map[uint32]uint32{
	1000: 16, 1001: 17, 1002: 18, 1003: 19, 1005: 21, 1006: 22,
	1007: 23, 1008: 24, 1009: 25, 1010: 27, 1011: 28, 1012: 29,
	1013: 30, 1014: 1042, 1015: 1043, 1016: 20, 1021: 700, 1022: 701,
	1024: 26, 1027: 829, 1028: 869, 1040: 774, 1115: 1114, 1182: 1082,
	1183: 1083, 1185: 1184, 1187: 1186, 1231: 1700, 1263: 2275,
	1270: 1266, 1561: 1560, 1563: 1562, 2951: 2950, 3807: 3802,
	1034: 1033,
}

var alignSizes = map[string]int{"c": 1, "s": 2, "i": 4, "d": 8}

func arrayElemText(payload []byte, pos int, elemOID uint32) (string, int) {
	if info, ok := arrayElemInfo[elemOID]; ok {
		alen := info[0].(int)
		aalign := info[1].(string)
		fn := info[2].(string)
		degrade := info[3].(bool)
		if alen == -1 {
			first := payload[pos]
			if first&1 != 0 {
				total := int(first >> 1)
				seg := payload[pos+1 : pos+total]
				if degrade {
					return decodeBytes(seg), pos + total
				}
				return callElemDecoder(fn, seg), pos + total
			}
			a := alignSizes[aalign]
			if a > 1 && pos%a != 0 {
				pos = (pos + a - 1) &^ (a - 1)
			}
			total := int(binary.LittleEndian.Uint32(payload[pos:]) >> 2)
			seg := payload[pos+4 : pos+total]
			if degrade {
				return decodeBytes(seg), pos + total
			}
			return callElemDecoder(fn, seg), pos + total
		}
		a := alignSizes[aalign]
		if a > 1 {
			pos = (pos + a - 1) &^ (a - 1)
		}
		seg := payload[pos : pos+alen]
		if degrade {
			return decodeBytes(seg), pos + alen
		}
		return callElemDecoder(fn, seg), pos + alen
	}
	// 未知元素类型：先查方案 C 动态类型定义（v1.0.16）
	if dynamicTypeDefs != nil {
		if td, ok := dynamicTypeDefs[elemOID]; ok {
			dec := func(b []byte) string { return decodeBytes(b) }
			if dynamicDecoders != nil {
				if d, ok2 := dynamicDecoders[elemOID]; ok2 {
					dec = d
				}
			}
			if td.Len < 0 { // varlena 元素
				first := payload[pos]
				if first&1 != 0 {
					total := int(first >> 1)
					return dec(payload[pos+1 : pos+total]), pos + total
				}
				total := int(binary.LittleEndian.Uint32(payload[pos:]) >> 2)
				return dec(payload[pos+4 : pos+total]), pos + total
			}
			// 定长元素：按 typalign 对齐
			if td.Len > 0 {
				a := 1
				if s, ok := alignSizes[string([]byte{td.Align})]; ok {
					a = s
				}
				if a > 1 {
					pos = (pos + a - 1) &^ (a - 1)
				}
				if pos+td.Len > len(payload) {
					return "", len(payload)
				}
				return dec(payload[pos : pos+td.Len]), pos + td.Len
			}
		}
	}
	// 未知元素类型：按 varlena 文本退化
	first := payload[pos]
	if first&1 != 0 {
		total := int(first >> 1)
		return decodeBytes(payload[pos+1 : pos+total]), pos + total
	}
	total := int(binary.LittleEndian.Uint32(payload[pos:]) >> 2)
	return decodeBytes(payload[pos+4 : pos+total]), pos + total
}

func callElemDecoder(fn string, seg []byte) string {
	switch fn {
	case "bool":
		return decodeBool(seg)
	case "char":
		return decodeChar(seg)
	case "name":
		return decodeName(seg)
	case "int8":
		return decodeInt8(seg)
	case "int2":
		return decodeInt2(seg)
	case "int4":
		return decodeInt4(seg)
	case "oid":
		return decodeOid(seg)
	case "float4":
		return decodeFloat4(seg)
	case "float8":
		return decodeFloat8(seg)
	case "date":
		return decodeDate(seg)
	case "time":
		return decodeTime(seg)
	case "timestamp":
		return decodeTimestamp(seg)
	case "timestamptz":
		return decodeTimestamptz(seg)
	case "timetz":
		return decodeTimetz(seg)
	case "interval":
		return decodeInterval(seg)
	case "uuid":
		return decodeUUID(seg)
	case "jsonb":
		return decodeJSONB(seg)
	case "aclitem":
		return decodeAclitem(seg)
	}
	return decodeBytes(seg)
}

func decodeArray(b []byte) string {
	payload, _, _ := varPayload(b)
	if os.Getenv("DBG_ARR") != "" {
		fmt.Fprintf(os.Stderr, "[dbg] arr b=%x payload=%x\n", b, payload)
	}
	if len(payload) < 12 {
		return "{}"
	}
	ndim := int(int32(binary.LittleEndian.Uint32(payload)))
	dataoffset := int(int32(binary.LittleEndian.Uint32(payload[4:])))
	elemtype := binary.LittleEndian.Uint32(payload[8:])
	if ndim <= 0 || ndim > 6 {
		return "{}"
	}
	dims := make([]int, ndim)
	nelems := 1
	for i := 0; i < ndim; i++ {
		dims[i] = int(int32(binary.LittleEndian.Uint32(payload[12+4*i:])))
		if dims[i] <= 0 {
			return "{}"
		}
		nelems *= dims[i]
	}
	bodyOff := 12 + 8*ndim
	var nulls []bool
	elemOff := bodyOff
	if dataoffset > 0 {
		bmLen := (nelems + 7) / 8
		bm := payload[bodyOff : bodyOff+bmLen]
		nulls = make([]bool, nelems)
		for i := 0; i < nelems; i++ {
			nulls[i] = bm[i/8]&(1<<(i%8)) == 0
		}
		elemOff = dataoffset - 4
		elemOff = (elemOff + 3) &^ 3
	}
	texts := make([]string, 0, nelems)
	pos := elemOff
	for i := 0; i < nelems; i++ {
		if nulls != nil && nulls[i] {
			texts = append(texts, "NULL")
			continue
		}
		t, np := arrayElemText(payload, pos, elemtype)
		pos = np
		texts = append(texts, t)
	}
	var elems interface{}
	if ndim == 1 {
		elems = texts
	} else {
		// 嵌套组装
		var build func(items []string, idx int) (interface{}, int)
		build = func(items []string, idx int) (interface{}, int) {
			d := dims[idx]
			if idx == ndim-1 {
				seg := make([]interface{}, d)
				for j := 0; j < d; j++ {
					seg[j] = items[j]
				}
				return seg, d
			}
			out := []interface{}{}
			cnt := 0
			for range d {
				chunk, used := build(items[cnt:], idx+1)
				cnt += used
				out = append(out, chunk)
			}
			return out, cnt
		}
		elems, _ = build(texts, 0)
	}
	var fmtArr func(items interface{}) string
	fmtArr = func(items interface{}) string {
		var sb strings.Builder
		switch lst := items.(type) {
		case []interface{}:
			if len(lst) == 0 {
				return "{}"
			}
			if _, nested := lst[0].([]interface{}); nested {
				sb.WriteString("{")
				for i, x := range lst {
					if i > 0 {
						sb.WriteString(",")
					}
					sb.WriteString(fmtArr(x))
				}
				sb.WriteString("}")
				return sb.String()
			}
			sb.WriteString("{")
			for i, x := range lst {
				if i > 0 {
					sb.WriteString(",")
				}
				s, _ := x.(string)
				sb.WriteString(arrayQuote(s))
			}
			sb.WriteString("}")
			return sb.String()
		case []string:
			sb.WriteString("{")
			for i, x := range lst {
				if i > 0 {
					sb.WriteString(",")
				}
				sb.WriteString(arrayQuote(x))
			}
			sb.WriteString("}")
			return sb.String()
		}
		return "{}"
	}
	return fmtArr(elems)
}

func arrayQuote(s string) string {
	if s == "NULL" {
		return s
	}
	needs := s == "" || strings.ContainsAny(s, ",{}\"\\") ||
		(s != "" && (s[0] == ' ' || s[len(s)-1] == ' '))
	if !needs {
		return s
	}
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	return "\"" + s + "\""
}

// ---------- inet/cidr/macaddr/bit/money ----------
func decodeInet(b []byte, forceCIDR bool) string {
	payload, _, _ := varPayload(b)
	if len(payload) < 2 {
		return ""
	}
	family := payload[0]
	bits := payload[1]
	if family != 2 && family != 3 {
		return ""
	}
	nbytes := 4
	if family == 3 {
		nbytes = 16
	}
	addr := payload[2 : 2+nbytes]
	var s string
	if family == 2 {
		parts := make([]string, 4)
		for i := 0; i < 4; i++ {
			parts[i] = strconv.Itoa(int(addr[i]))
		}
		s = strings.Join(parts, ".")
	} else {
		parts := make([]string, 8)
		for i := 0; i < 8; i++ {
			parts[i] = fmt.Sprintf("%x", int(addr[i*2])<<8|int(addr[i*2+1]))
		}
		s = strings.Join(parts, ":")
	}
	defaultBits := uint8(32)
	if family == 3 {
		defaultBits = 128
	}
	if forceCIDR || bits != defaultBits {
		return fmt.Sprintf("%s/%d", s, bits)
	}
	return s
}

func decodeCidr(b []byte) string { return decodeInet(b, true) }

func decodeMacaddr(b []byte) string {
	if len(b) < 6 {
		return ""
	}
	parts := make([]string, 6)
	for i := 0; i < 6; i++ {
		parts[i] = fmt.Sprintf("%02x", b[i])
	}
	return strings.Join(parts, ":")
}

func decodeMacaddr8(b []byte) string {
	if len(b) < 8 {
		return ""
	}
	parts := make([]string, 8)
	for i := 0; i < 8; i++ {
		parts[i] = fmt.Sprintf("%02x", b[i])
	}
	return strings.Join(parts, ":")
}

func bitsToStr(payload []byte) string {
	if len(payload) < 4 {
		return ""
	}
	nbits := int(int32(binary.LittleEndian.Uint32(payload)))
	data := payload[4:]
	var sb strings.Builder
	for i := 0; i < nbits; i++ {
		byteIdx := i / 8
		if byteIdx >= len(data) {
			break
		}
		bit := data[byteIdx] & (1 << (7 - (i % 8)))
		if bit != 0 {
			sb.WriteByte('1')
		} else {
			sb.WriteByte('0')
		}
	}
	return sb.String()
}

// ---------- 金仓 V9 mysql 模式 BIT 类型（oid 4655, typinput=mysql_bit_in）----------
// 磁盘格式（varlena，实测 V9R3C18 mysql 模式）：
//   内容 = 4B 小端 A + 4B 小端 B + ceil(N/8) 字节大端数据
//   A = (N-1) - bitpos（bitpos 为最高 1 位位置，全 0 时 A=N）
//   B = bitpos + 1（全 0 时 B=0），N = A+B 即位长
//   数据 = 原值 << (8*nbytes - B)，即原值高位对齐到字节最高位
// 解码：原值 = 数据 >> (8*nbytes - B)；文本输出 = N 位二进制串（高位在前）
// 导入要求（实测）：SQL 必须用 B'...' 位字面量；CSV COPY 必须用 0x 前缀大写 hex
// （mysql_bit_out 同款格式，如 0x8001 / 0x02AA）

// parseMysqlBit 解析 mysql_bit varlena，返回 (位长 N, 数据字节数, 原值)
func parseMysqlBit(b []byte) (int, int, *big.Int, bool) {
	payload, _, _ := varPayload(b)
	if len(payload) < 8 {
		return 0, 0, nil, false
	}
	a := binary.LittleEndian.Uint32(payload)
	bb := binary.LittleEndian.Uint32(payload[4:])
	n := int(a + bb)
	if n < 0 || n > 1<<20 {
		return 0, 0, nil, false
	}
	if n == 0 {
		return 0, 0, big.NewInt(0), true // 全 0 值（A=B=0）
	}
	nbytes := (n + 7) / 8
	if len(payload) < 8+nbytes {
		nbytes = len(payload) - 8
		if nbytes < 0 {
			return 0, 0, nil, false
		}
	}
	val := new(big.Int)
	for i := 0; i < nbytes; i++ {
		val.Lsh(val, 8)
		val.Or(val, big.NewInt(int64(payload[8+i])))
	}
	shift := 8*nbytes - int(bb)
	if shift < 0 {
		shift = 0
	}
	if shift > 0 {
		val.Rsh(val, uint(shift))
	}
	return n, nbytes, val, true
}

// decodeMysqlBit 输出位串（A+B 位，高位在前），供 SQL B'...' 字面量使用
// 注意：A+B 为存储位宽（可变，≤ 声明位宽）；全 0 时 A=B=0，输出 "0"（值 0）
func decodeMysqlBit(b []byte) string {
	n, _, val, ok := parseMysqlBit(b)
	if !ok {
		return decodeDefault(b)
	}
	if n == 0 {
		return "0" // 全 0 值（值语义 0）
	}
	bin := val.Text(2)
	if len(bin) < n {
		var sb strings.Builder
		sb.Grow(n)
		sb.WriteString(strings.Repeat("0", n-len(bin)))
		sb.WriteString(bin)
		return sb.String()
	}
	return bin
}

// bitBinToHex 将 N 位二进制串转为 mysql_bit_out 同款 hex 文本（0x 前缀大写）
// 规则：左补 0 到 8*ceil(N/8) 位，按字节转大写 hex
func bitBinToHex(bin string) string {
	n := len(bin)
	if n == 0 {
		return "0x00"
	}
	nbytes := (n + 7) / 8
	padded := strings.Repeat("0", 8*nbytes-n) + bin
	var sb strings.Builder
	sb.Grow(2 + nbytes*2)
	sb.WriteString("0x")
	for i := 0; i < len(padded); i += 8 {
		v, _ := strconv.ParseUint(padded[i:i+8], 2, 8)
		sb.WriteString(fmt.Sprintf("%02X", v))
	}
	return sb.String()
}

func decodeBit(b []byte) string {
	payload, _, _ := varPayload(b)
	return bitsToStr(payload)
}

// decodeXml 解码 xml 类型（oid 142 / typinput=xml_in）
// 金仓 mysql 模式：varlena 纯文本（实测无类型标记）
// PG 标准：varlena 内容 = 4B 类型标记（bit0: 0=CONTENT,1=DOCUMENT，其余 reserved）+ 文本
func decodeXml(b []byte) string {
	payload, _, _ := varPayload(b)
	if len(payload) >= 5 && payload[0] <= 1 && payload[1] == 0 && payload[2] == 0 && payload[3] == 0 {
		payload = payload[4:] // 跳过 PG 4B 类型标记
	}
	if utf8.Valid(payload) {
		return string(payload)
	}
	return "\\x" + hex.EncodeToString(payload)
}

func decodeMoney(b []byte) string {
	v := int64(binary.LittleEndian.Uint64(b))
	sign := ""
	if v < 0 {
		sign = "-"
		v = -v
	}
	return fmt.Sprintf("%s%d.%02d", sign, v/100, v%100)
}

// ---------- 内置缺口类型：pg_lsn / txid_snapshot / reg* / range ----------

// decodePgLsn：pg_lsn（oid 3220，8B）。PG 内部 XLogRecPtr 统一大端序存储。
// 输出与 pg_lsn_out 一致：高 32 位/低 32 位大写十六进制，无 0x 前缀。
func decodePgLsn(b []byte) string {
	if len(b) < 8 {
		return decodeDefault(b)
	}
	v := binary.LittleEndian.Uint64(b)
	return fmt.Sprintf("%X/%X", uint32(v>>32), uint32(v))
}

// decodeTxidSnapshot：txid_snapshot（oid 5030）varlena。
// 磁盘格式（PG txid.h TxidSnapshot）：4B xmin(LE) + 4B xmax(LE) + 4B nxip(LE) + nxip*8B xip(LE)。
// 输出与 txid_snapshot_out 一致：xmin:xmax 或 xmin:xmax:xip1,xip2,...
func decodeTxidSnapshot(b []byte) string {
	payload, _, _ := varPayload(b)
	if os.Getenv("DBG_RANGE") != "" {
		fmt.Fprintf(os.Stderr, "[dbg] txid b=%x payload=%x\n", b, payload)
	}
	// PG 全版本（PG12-18/金仓）txid_snapshot/pg_snapshot 磁盘布局：
	// [nxip int32][xmin int64][xmax int64][xip int64...]（见 xid.c/xid8funcs.c recv 函数）
	if len(payload) < 20 {
		return decodeDefault(b)
	}
	nxip := binary.LittleEndian.Uint32(payload)
	xmin := binary.LittleEndian.Uint64(payload[4:])
	xmax := binary.LittleEndian.Uint64(payload[12:])
	var sb strings.Builder
	sb.WriteString(strconv.FormatUint(xmin, 10))
	sb.WriteByte(':')
	sb.WriteString(strconv.FormatUint(xmax, 10))
	for i := 0; i < int(nxip) && 20+8*i+8 <= len(payload); i++ {
		if i == 0 {
			sb.WriteByte(':')
		} else {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.FormatUint(binary.LittleEndian.Uint64(payload[20+8*i:]), 10))
	}
	return sb.String()
}

// decodeRegOid：reg* 系列（regclass/regproc/regtype 等，int4 byval，磁盘为 4B oid 小端）。
// 输出 oid 数字（'数字'::regclass 按 oid 解析，可逆）。
func decodeRegOid(b []byte) string {
	if len(b) < 4 {
		return decodeDefault(b)
	}
	return strconv.FormatUint(uint64(binary.LittleEndian.Uint32(b)), 10)
}

// rangeSub：range 子类型定义（wid=定长宽度；0=变长 varlena 内嵌，需 4B 长度前缀）
type rangeSub struct {
	wid int
	dec func([]byte) string
}

var rangeSubtypes = map[uint32]rangeSub{
	3904: {4, decodeInt4},        // int4range
	3926: {8, decodeInt8},        // int8range
	3906: {0, decodeNumeric},     // numrange（numeric 为 varlena）
	3912: {4, decodeDate},        // daterange
	3908: {8, decodeTimestamp},   // tsrange
	3910: {8, decodeTimestamptz}, // tstzrange
}

// decodeRange：PG 范围类型 varlena（PG rangetypes.h）。
// 磁盘：lower + upper + 1B flags（变长子类型带 4B varlena 长度前缀）。
// flags: 0x01 EMPTY 0x02 LB_INC 0x04 UB_INC 0x08 LB_INF 0x10 UB_INF 0x20 LB_NULL 0x40 UB_NULL。
// 输出与 range_out 一致：empty / [1,10) / (,10] / [1,) 等。
func decodeRange(b []byte, oid uint32) string {
	if os.Getenv("DBG_RANGE") != "" {
		fmt.Fprintf(os.Stderr, "[dbg] oid=%d b=%x len=%d\n", oid, b, len(b))
	}
	sub, ok := rangeSubtypes[oid]
	if !ok {
		return decodeDefault(b)
	}
	payload, _, _ := varPayload(b)
	if len(payload) < 5 {
		return decodeDefault(b)
	}
	// PG range 磁盘：开头 4B 为 range 类型自身 oid（实测 3904/3926 等），随后为边界与 flags
	payload = payload[4:]
	flags := payload[len(payload)-1]
	if flags&0x01 != 0 {
		return "empty"
	}
	var lo, hi []byte
	pos := 0
	if flags&0x08 != 0 {
		lo = nil // lower unbounded
	} else if pos < len(payload)-1 {
		if sub.wid > 0 {
			lo = payload[pos : pos+sub.wid]
			pos += sub.wid
		} else {
			// 变长 subtype：varlena 字节（1B short / 4B 头），用 varlenaParse 逐边界解析
			kind, total, _, _ := varlenaParse(payload, pos)
			if kind != "" && total > 0 && pos+total <= len(payload)-1 {
				lo = payload[pos : pos+total] // 含 varlena 头，decodeNumeric 内部剥
				pos += total
			}
		}
	}
	if flags&0x10 != 0 {
		hi = nil // upper unbounded
	} else if pos < len(payload)-1 {
		if sub.wid > 0 {
			hi = payload[pos : pos+sub.wid]
		} else {
			kind, total, _, _ := varlenaParse(payload, pos)
			if kind != "" && total > 0 && pos+total <= len(payload)-1 {
				hi = payload[pos : pos+total]
			}
		}
	}
	var sb strings.Builder
	if flags&0x02 != 0 {
		sb.WriteByte('[')
	} else {
		sb.WriteByte('(')
	}
	if lo != nil {
		sb.WriteString(sub.dec(lo))
	}
	sb.WriteByte(',')
	if hi != nil {
		sb.WriteString(sub.dec(hi))
	}
	if flags&0x04 != 0 {
		sb.WriteByte(']')
	} else {
		sb.WriteByte(')')
	}
	return sb.String()
}

// ---------- 几何 ----------
func geomPt(b []byte, off int) string {
	x := math.Float64frombits(binary.LittleEndian.Uint64(b[off:]))
	y := math.Float64frombits(binary.LittleEndian.Uint64(b[off+8:]))
	return fmt.Sprintf("(%s,%s)", fmtFloat(x), fmtFloat(y))
}

func decodePoint(b []byte) string {
	if len(b) < 16 {
		return ""
	}
	return geomPt(b, 0)
}

func decodeLseg(b []byte) string {
	if len(b) < 32 {
		return ""
	}
	return fmt.Sprintf("[%s,%s]", geomPt(b, 0), geomPt(b, 16))
}

func decodeBox(b []byte) string {
	if len(b) < 32 {
		return ""
	}
	return fmt.Sprintf("%s,%s", geomPt(b, 0), geomPt(b, 16))
}

func decodePath(b []byte) string {
	payload, _, _ := varPayload(b)
	if len(payload) < 12 {
		return ""
	}
	npts := int(int32(binary.LittleEndian.Uint32(payload)))
	closed := int32(binary.LittleEndian.Uint32(payload[4:]))
	if npts <= 0 || len(payload) < 12+16*npts {
		return ""
	}
	pts := make([]string, npts)
	for i := 0; i < npts; i++ {
		pts[i] = geomPt(payload, 12+16*i)
	}
	if closed != 0 {
		return "(" + strings.Join(pts, ",") + ")"
	}
	return "[" + strings.Join(pts, ",") + "]"
}

func decodePolygon(b []byte) string {
	payload, _, _ := varPayload(b)
	if len(payload) < 36 {
		return ""
	}
	npts := int(int32(binary.LittleEndian.Uint32(payload)))
	if npts <= 0 || len(payload) < 36+16*npts {
		return ""
	}
	pts := make([]string, npts)
	for i := 0; i < npts; i++ {
		pts[i] = geomPt(payload, 36+16*i)
	}
	return "(" + strings.Join(pts, ",") + ")"
}

func decodeLine(b []byte) string {
	if len(b) < 24 {
		return ""
	}
	a := math.Float64frombits(binary.LittleEndian.Uint64(b))
	bb := math.Float64frombits(binary.LittleEndian.Uint64(b[8:]))
	c := math.Float64frombits(binary.LittleEndian.Uint64(b[16:]))
	return fmt.Sprintf("{%s,%s,%s}", fmtFloat(a), fmtFloat(bb), fmtFloat(c))
}

func decodeCircle(b []byte) string {
	if len(b) < 24 {
		return ""
	}
	x := math.Float64frombits(binary.LittleEndian.Uint64(b))
	y := math.Float64frombits(binary.LittleEndian.Uint64(b[8:]))
	r := math.Float64frombits(binary.LittleEndian.Uint64(b[16:]))
	return fmt.Sprintf("<(%s,%s),%s>", fmtFloat(x), fmtFloat(y), fmtFloat(r))
}

// ---------- 其他 ----------
func decodeChar(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return string(rune(b[0]))
}

func decodeUnknown(b []byte) string { return decodeBytes(b) }

func decodePgNodeTree(b []byte) string { return decodeVarlenaText(b) }

func decodeOidvector(b []byte) string {
	n := len(b) / 4
	parts := make([]string, n)
	for i := 0; i < n; i++ {
		parts[i] = strconv.FormatUint(uint64(binary.LittleEndian.Uint32(b[i*4:])), 10)
	}
	return strings.Join(parts, " ")
}

func decodeInt2vector(b []byte) string {
	n := len(b) / 2
	parts := make([]string, n)
	for i := 0; i < n; i++ {
		parts[i] = strconv.Itoa(int(int16(binary.LittleEndian.Uint16(b[i*2:]))))
	}
	return strings.Join(parts, " ")
}

func decodeDefault(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	return "\\x" + hex.EncodeToString(b)
}

func decodeAclitem(b []byte) string {
	if len(b) < 12 {
		return "oid:0"
	}
	grantor := binary.LittleEndian.Uint32(b)
	grantee := binary.LittleEndian.Uint32(b[4:])
	privs := binary.LittleEndian.Uint32(b[8:])
	e := ""
	if grantee != 0 {
		if name, ok := roleMap[grantee]; ok {
			e = name
		} else {
			e = fmt.Sprintf("oid:%d", grantee)
		}
	}
	var chars strings.Builder
	for i, ch := range aclChars {
		if privs&(1<<i) != 0 {
			chars.WriteByte(byte(ch))
		}
	}
	if grantor == 0 {
		return fmt.Sprintf("%s=%s", e, chars.String())
	}
	g := ""
	if name, ok := roleMap[grantor]; ok {
		g = name
	} else {
		g = fmt.Sprintf("oid:%d", grantor)
	}
	return fmt.Sprintf("%s=%s/%s", e, chars.String(), g)
}

// ---------- 分发 ----------
// 方案 C：目录动态发现的解码器映射（v1.0.16）
// 硬编码 decoders 表优先；动态映射覆盖"未知 oid"（金仓实例相关 oid、未来版本新类型等）。
var dynamicDecoders map[uint32]func([]byte) string
// compositeAttrs：复合类型 typrelid → 字段行（按 attnum 升序，不含 dropped）
var compositeAttrs map[uint32][]*AttrRow

// buildCompositeAttrs 读取 pg_attribute(1249) 按 attrelid 聚合字段（复合类型行类型）
func buildCompositeAttrs(dbDir string, version int, isKB bool) {
	compositeAttrs = map[uint32][]*AttrRow{}
	path := filepath.Join(dbDir, strconv.Itoa(PG_ATTRIBUTE_RELFILE))
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return
	}
	for tup := range iterSysTuples(path, version, isKB) {
		ar := attrFields(tup, version, isKB)
		if ar == nil || ar.AttRelID == 0 || ar.AttNum < 1 || ar.AttIsDropped {
			continue
		}
		compositeAttrs[ar.AttRelID] = append(compositeAttrs[ar.AttRelID], ar)
	}
	for _, list := range compositeAttrs {
		sort.Slice(list, func(i, j int) bool { return list[i].AttNum < list[j].AttNum })
	}
}

// recordQuote：PG record_out 的字段转义（含 , ( ) " \ 或首尾空格 → 双引号包裹）
func recordQuote(s string) string {
	needs := strings.ContainsAny(s, ",()\"") ||
		(s != "" && (s[0] == ' ' || s[len(s)-1] == ' '))
	if !needs {
		return s
	}
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	return "\"" + s + "\""
}

// decodeComposite：解析 record varlena（HeapTupleHeader + 行数据）为 (f1,f2,...)
// 字段类型/对齐来自 pg_attribute，递归调用 decodeValue 解码各字段
func decodeComposite(oid uint32, b []byte) string {
	payload, _, _ := varPayload(b)
	if dynamicTypeDefs == nil {
		return decodeDefault(b)
	}
	td := dynamicTypeDefs[oid]
	if td == nil || td.Type != 'c' || td.TypRelid == 0 {
		return decodeDefault(b)
	}
	attrs := compositeAttrs[td.TypRelid]
	if len(attrs) == 0 {
		return decodeDefault(b)
	}
	t := parseTuple(payload, 12)
	if t == nil || t.THoff < 23 || t.THoff > len(payload) {
		return decodeDefault(b)
	}
	nulls := t.getNulls()
	cols := make([]ColLen, len(attrs))
	for i, a := range attrs {
		cols[i] = ColLen{AttLen: a.AttLen, IsVarlena: a.AttLen == -1, AttAlign: a.AttAlign}
	}
	fields := extractFieldsDirect(payload, t.THoff, nulls, cols)
	var sb strings.Builder
	sb.WriteByte('(')
	for i, f := range fields {
		if i > 0 {
			sb.WriteByte(',')
		}
		if f == nil {
			sb.WriteString("NULL")
			continue
		}
		sb.WriteString(recordQuote(decodeValue(attrs[i].AttTypID, *f)))
	}
	sb.WriteByte(')')
	return sb.String()
}

var dynamicTypeDefs map[uint32]*TypeDef
// mysqlBitOIDs：typinput=mysql_bit_in 的类型 oid 集合（CSV/SQL 输出层特判用）
var mysqlBitOIDs = map[uint32]bool{4655: true}

// inputFnDecoders：typinput 函数名 → 解码器族（方案 C 语义映射核心）
var inputFnDecoders = map[string]func([]byte) string{
	"date_in":         decodeDate,
	"time_in":         decodeTime,
	"timetz_in":       decodeTimetz,
	"timestamp_in":    decodeTimestamp,
	"timestamptz_in":  decodeTimestamptz,
	"interval_in":     decodeInterval,
	"json_in":         decodeJSON,
	"jsonb_in":        decodeJSONB,
	"textin":          decodeText,
	"varcharin":       decodeVarchar,
	"bpcharin":        decodeBpchar,
	"namein":          decodeName,
	"int2in":          decodeInt2,
	"int4in":          decodeInt4,
	"int8in":          decodeInt8,
	"oidin":           decodeOid,
	"float4in":        decodeFloat4,
	"float8in":        decodeFloat8,
	"numeric_in":      decodeNumeric,
	"boolin":          decodeBool,
	"uuid_in":         decodeUUID,
	"cash_in":         decodeMoney, // 金仓 V8 money 实测 typinput=cash_in
	"money_in":        decodeMoney,
	"byteain":         decodeBytea,
	"charin":          decodeChar,
	"unknownin":       decodeUnknown,
	"inet_in":         func(b []byte) string { return decodeInet(b, false) },
	"cidr_in":         decodeCidr,
	"macaddr_in":      decodeMacaddr,
	"macaddr8_in":     decodeMacaddr8,
	"bit_in":          decodeBit,
	"varbit_in":       decodeBit,
	"xml_in":          decodeXml,       // PG 标准 xml + 金仓（纯文本 varlena）
	"mysql_bit_in":    decodeMysqlBit, // 金仓 V9 mysql 模式 BIT(4655)
	"tsvectorin":      decodeTsvector,
	// 金仓 mysql/oracle 模式
	"mysql_date_in":      decodeDate,
	"mysql_datetime_in":  decodeTimestamp,
	"mysql_timestamp_in": decodeMysqlTimestamp,
	"mysql_time_in":      decodeMysqlTime,
	"ora_date_in":        decodeTimestamp,
	"datetime_in":        decodeTimestamp,
}

// resolveDecoder 递归解析 oid 对应的解码器：
// 硬编码表 → 已动态映射 → base 类型按 typinput 函数名 → domain 沿 typbasetype 递归。
func resolveDecoder(oid uint32, defs map[uint32]*TypeDef, procs map[uint32]string, dd map[uint32]func([]byte) string) func([]byte) string {
	if dec, ok := decoders[oid]; ok {
		return dec
	}
	if dec, ok := dd[oid]; ok {
		return dec
	}
	td, ok := defs[oid]
	if !ok {
		return nil
	}
	if td.Type == 'b' || td.Type == 'e' || td.Type == 'r' {
		if fn := procs[td.Input]; fn != "" {
			if dec, ok := inputFnDecoders[fn]; ok {
				return dec
			}
		}
		return nil
	}
	if td.Type == 'd' && td.Base != 0 {
		return resolveDecoder(td.Base, defs, procs, dd)
	}
	return nil
}

// initDynamicDecoders 从数据目录 sys_type/sys_proc 构建动态解码器映射。
// 返回动态映射数量（-1 表示目录缺失，保持仅硬编码表）。
func initDynamicDecoders(dbDir string, version int, isKB bool) int {
	dynamicTypeDefs = buildTypeDefs(dbDir, version, isKB)
	buildCompositeAttrs(dbDir, version, isKB)
	if len(dynamicTypeDefs) == 0 {
		dynamicDecoders = nil
		return -1
	}
	procs := buildProcNameMap(dbDir, version, isKB)
	dd := map[uint32]func([]byte) string{}
	// 收集 mysql_bit_in 类型 oid（CSV/SQL 输出特判）
	for oid, td := range dynamicTypeDefs {
		if fn := procs[td.Input]; fn == "mysql_bit_in" {
			mysqlBitOIDs[oid] = true
		}
	}
	// 1) base 类型：typinput 函数名映射
	for oid, td := range dynamicTypeDefs {
		if _, ok := decoders[oid]; ok {
			continue // 硬编码优先
		}
		if td.Type == 'b' {
			if fn := procs[td.Input]; fn != "" {
				if dec, ok := inputFnDecoders[fn]; ok {
					dd[oid] = dec
				}
			}
		}
	}
	// 2) domain：沿 typbasetype 递归
	for oid, td := range dynamicTypeDefs {
		if _, ok := decoders[oid]; ok {
			continue
		}
		if _, ok := dd[oid]; ok {
			continue
		}
		if td.Type == 'd' && td.Base != 0 {
			if dec := resolveDecoder(td.Base, dynamicTypeDefs, procs, dd); dec != nil {
				dd[oid] = dec
			}
		}
	}
	// 3) 数组：typelem!=0 且 typarray 自指
	for oid, td := range dynamicTypeDefs {
		if _, ok := decoders[oid]; ok {
			continue
		}
		if _, ok := dd[oid]; ok {
			continue
		}
		if td.Type == 'b' && td.Elem != 0 && td.Array == oid {
			dd[oid] = decodeArray
		}
	}
	dynamicDecoders = dd
	return len(dd)
}

var arrayTypeOIDs = map[uint32]bool{
	1000: true, 1001: true, 1002: true, 1003: true, 1005: true, 1006: true,
	1007: true, 1008: true, 1009: true, 1010: true, 1011: true, 1012: true,
	1013: true, 1014: true, 1015: true, 1016: true, 1017: true, 1018: true,
	1019: true, 1020: true, 1021: true, 1022: true, 1023: true, 1024: true,
	1027: true, 1028: true, 1040: true, 1041: true, 1034: true, 1115: true,
	1182: true, 1183: true, 1185: true, 1187: true, 1231: true, 1263: true,
	1270: true, 1561: true, 1563: true, 2951: true, 3807: true,
}

func decodeValue(oid uint32, raw []byte) string {
	if raw == nil {
		return "NULL"
	}
	// 枚举类型
	if em, ok := enumMap[oid]; ok && len(raw) >= 4 {
		memberOID := binary.LittleEndian.Uint32(raw)
		for _, m := range em {
			if m.MemberOID == memberOID {
				return m.Label
			}
		}
		return strconv.FormatUint(uint64(memberOID), 10)
	}
	dec, ok := decoders[oid]
	if !ok {
		if dynamicTypeDefs != nil {
			if td, ok2 := dynamicTypeDefs[oid]; ok2 && td.Type == 'c' {
				return decodeComposite(oid, raw)
			}
		}
		if dynamicDecoders != nil {
			if dec2, ok2 := dynamicDecoders[oid]; ok2 {
				return dec2(raw)
			}
		}
		return decodeDefault(raw)
	}
	return dec(raw)
}

var decoders = map[uint32]func([]byte) string{
	BOOLOID:        decodeBool,
	INT2OID:        decodeInt2,
	INT4OID:        decodeInt4,
	INT8OID:        decodeInt8,
	4659:           decodeInt16KB,
	FLOAT4OID:      decodeFloat4,
	FLOAT8OID:      decodeFloat8,
	TEXTOID:        decodeText,
	8014:           decodeText, // 金仓 clob（textin/textout，标准 varlena 存储）
	4655:           decodeMysqlBit, // 金仓 V9 mysql 模式 BIT（typinput=mysql_bit_in，实测）
	XMLOID:         decodeXml,     // PG 标准 xml（142）
	NAMEOID:        decodeName,
	BPCHAROID:      decodeBpchar,
	VARCHAROID:     decodeVarchar,
	BYTEAOID:       decodeBytea,
	OIDOID:         decodeOid,
	XIDOID:         decodeXid,
	CIDOID:         decodeCid,
	TIDOID:         decodeTid,
	DATEOID:        decodeDate,
	7944:           decodeDate,        // 金仓 V9 date（4 字节天数，与 PG date 同格式）
	8020:           decodeTimestamp, // 金仓 oracle DATE（8 字节 timestamp 格式）
	7952:           decodeTimestamp, // 金仓 V9 datetime 基础类型（8 字节 timestamp 格式）
	7954:           decodeMysqlTimestamp, // 金仓 V9 mysql 模式 timestamp（UTC 秒 + 时区转换）
	7950:           decodeMysqlTime,      // 金仓 V9 mysql 模式 time（支持 24:00:00，不做取模）
	7024:           decodeJSONB,          // 金仓 V9 mysql 模式 json（domain of 4802 mysql_json，jsonb 二进制）
	4189:           decodeTimestamp, // 金仓 datetime（domain of timestamp 1114）
	12636:          decodeTimestamp, // 金仓 ora_date（domain of 8020，8 字节 timestamp 格式）
	TIMEOID:        decodeTime,
	TIMESTAMPOID:   decodeTimestamp,
	TIMESTAMPTZOID: decodeTimestamptz,
	TIMETZOID:      decodeTimetz,
	INTERVALOID:    decodeInterval,
	NUMERICOID:     decodeNumeric,
	UUIDOID:        decodeUUID,
	JSONOID:        decodeJSON,
	JSONBOID:       decodeJSONB,
	INETOID:        func(b []byte) string { return decodeInet(b, false) },
	CIDROID:        decodeCidr,
	MACADDROID:     decodeMacaddr,
	MACADDR8OID:    decodeMacaddr8,
	3614:           decodeTsvector, // tsvector
	BITOID:         decodeBit,
	VARBITOID:      decodeBit,
	MONEYOID:       decodeMoney,
	CHAROID:        decodeChar,
	UNKNOWNOID:     decodeUnknown,
	PG_NODE_TREE:   decodePgNodeTree,
	OIDVECTOROID:   decodeOidvector,
	INT2VECTOROID:  decodeInt2vector,
	600:            decodePoint,
	601:            decodeLseg,
	602:            decodePath,
	603:            decodeBox,
	604:            decodePolygon,
	628:            decodeLine,
	CIRCLEOID:      decodeCircle,
	// ---- 内置缺口类型：range / pg_lsn / txid_snapshot / reg* ----
	3220:           decodePgLsn,            // pg_lsn
	2970:           decodeTxidSnapshot,     // txid_snapshot
	3615:           decodeTsquery,          // tsquery
	2205:           decodeRegOid,           // regclass
	24:             decodeRegOid,           // regproc
	2206:           decodeRegOid,           // regprocedure
	2207:           decodeRegOid,           // regoper
	2208:           decodeRegOid,           // regoperator
	2209:           decodeRegOid,           // regnamespace
	2210:           decodeRegOid,           // regtype
	4096:           decodeRegOid,           // regrole
	4089:           decodeRegOid,           // regconfig
	4090:           decodeRegOid,           // regdictionary
	3904:           func(b []byte) string { return decodeRange(b, 3904) }, // int4range
	3926:           func(b []byte) string { return decodeRange(b, 3926) }, // int8range
	3906:           func(b []byte) string { return decodeRange(b, 3906) }, // numrange
	3912:           func(b []byte) string { return decodeRange(b, 3912) }, // daterange
	3908:           func(b []byte) string { return decodeRange(b, 3908) }, // tsrange
	3910:           func(b []byte) string { return decodeRange(b, 3910) }, // tstzrange
}

func init() {
	for oid := range arrayTypeOIDs {
		decoders[oid] = decodeArray
	}
}

// ---------- SQL 字面量 ----------
func sqlStringLiteral(v string) string {
	needsEscape := false
	for _, ch := range v {
		if ch == '\'' || ch == '\\' || ch < 32 || ch == 127 {
			needsEscape = true
			break
		}
	}
	if !needsEscape {
		return "'" + v + "'"
	}
	var sb strings.Builder
	sb.WriteString("E'")
	for _, ch := range v {
		switch ch {
		case '\'':
			sb.WriteString("''")
		case '\\':
			sb.WriteString("\\\\")
		case '\n':
			sb.WriteString("\\n")
		case '\r':
			sb.WriteString("\\r")
		case '\t':
			sb.WriteString("\\t")
		default:
			if ch < 32 || ch == 127 {
				fmt.Fprintf(&sb, "\\x%02X", ch)
			} else {
				sb.WriteRune(ch)
			}
		}
	}
	sb.WriteString("'")
	return sb.String()
}
