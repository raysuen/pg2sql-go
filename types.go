// pg2sql types 层：PostgreSQL/金仓内置类型解码
// 对应 Python 版 pg2sql/types.py（v2.5），逐字节等价翻译
// Author: raysuen
package main

import (
	"math/big"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
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

func timestampFromUS(us int64) string {
	// 2000-01-01 00:00:00 + us
	sec := us / 1000000
	micro := us % 1000000
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
	if zone >= 0 {
		sign = "-"
	}
	z := zone
	if z < 0 {
		z = -z
	}
	return fmt.Sprintf("%s%s%02d:%02d", t, sign, z/3600, z%3600/60)
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
				return items[:d], d
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
		if lst, ok := items.([]interface{}); ok && len(lst) > 0 {
			if _, ok2 := lst[0].([]interface{}); ok2 {
				var sb strings.Builder
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
		}
		var sb strings.Builder
		sb.WriteString("{")
		ts, _ := items.([]string)
		for i, x := range ts {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString(arrayQuote(x))
		}
		sb.WriteString("}")
		return sb.String()
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

func decodeBit(b []byte) string {
	payload, _, _ := varPayload(b)
	return bitsToStr(payload)
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
	NAMEOID:        decodeName,
	BPCHAROID:      decodeBpchar,
	VARCHAROID:     decodeVarchar,
	BYTEAOID:       decodeBytea,
	OIDOID:         decodeOid,
	XIDOID:         decodeXid,
	CIDOID:         decodeCid,
	TIDOID:         decodeTid,
	DATEOID:        decodeDate,
	8020:           decodeTimestamp, // 金仓 oracle DATE
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
