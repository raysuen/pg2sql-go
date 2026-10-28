// pg2sql types 层：PostgreSQL/金仓内置类型解码
// 对应 Python 版 pg2sql/types.py（v2.5），逐字节等价翻译
// Author: raysuen
package datatype

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"os"
	"pg2sql/internal/charset"
	"pg2sql/internal/logx"
	"pg2sql/internal/util"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

var Date2000 = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

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

// RoleMap：角色 oid → 角色名（catalog 注入）
// B7 修复：--jobs 表间并行时 exportOneTable 在各 goroutine 写 RoleMap，
// 而 decodeAclitem 读侧无锁 → DATA RACE（race detector 实锤）。改用 atomic.Value
// 无锁发布/读取：写侧 Store 完整 map（原子替换），读侧 Load 快照（无锁、无撕裂）。
var RoleMap atomic.Value

// map[uint32]string

// EnumMember：枚举成员（按 sortorder 有序）
type EnumMember struct {
	MemberOID uint32
	Label     string
	SortOrder float32
}

// EnumMap：{枚举类型 oid: 有序成员列表}（catalog 注入）
var EnumMap atomic.Value

// map[uint32][]EnumMember

// v1.0.38 --jobs 表间并行：全局目录注入状态加写锁（同库各表注入内容一致，
// 锁串行化写保证并发安全；解析期只读不持锁）
var CatalogMu sync.Mutex

func SetRoleMap(m map[uint32]string) {
	if m == nil {
		m = map[uint32]string{}
	}
	RoleMap.Store(m)
}

// roleMapSnapshot：无锁读快照（nil 表示未注入）
func roleMapSnapshot() map[uint32]string {
	if v := RoleMap.Load(); v != nil {
		return v.(map[uint32]string)
	}
	return nil
}

func SetEnumMap(m map[uint32][]EnumMember) {
	if m == nil {
		m = map[uint32][]EnumMember{}
	}
	EnumMap.Store(m)
}

// EnumMapSnapshot：无锁读快照（nil 表示未注入）
func EnumMapSnapshot() map[uint32][]EnumMember {
	if v := EnumMap.Load(); v != nil {
		return v.(map[uint32][]EnumMember)
	}
	return nil
}

// ---------- varlena Payload 提取 ----------
// VarPayload 返回 (Payload, isExternal, extInfo)
func VarPayload(B []byte) ([]byte, bool, *ExternalInfo) {
	if len(B) == 0 {
		return B, false, nil
	}
	kind, total, _, _ := VarlenaParse(B, 0)
	switch kind {
	case VARLENA_EXTERNAL:
		ext := ParseExternalPointer(B, 0)
		if ext != nil {
			return []byte{}, true, ext
		}
		return []byte{}, false, nil
	case VARLENA_1B:
		p := total - 1
		if p < 0 {
			p = 0
		}
		if 1+p > len(B) {
			p = len(B) - 1
		}
		return B[1 : 1+p], false, nil
	case VARLENA_4B:
		p := total - 4
		if p < 0 {
			p = 0
		}
		if 4+p > len(B) {
			p = len(B) - 4
		}
		return B[4 : 4+p], false, nil
	case VARLENA_4BC:
		// 越界保护：total 来自头部，若头部损坏/字段错位可能超过实际切片
		if total > len(B) {
			return []byte{}, false, nil
		}
		comp := B[4:total]
		if len(comp) >= 5 {
			// 压缩方法位（PGLZ=0/LZ4=1）在 tcinfo 高 2 位，ToastDecompress 按流内方法位分发
			rawlen := int(U32(comp, 0) & 0x3FFFFFFF)
			data := ToastDecompress(comp, rawlen, TOAST_COMPRESS_METHOD_PGLZ)
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
func decodeTsquery(B []byte) string {
	Payload, _, _ := VarPayload(B)
	if os.Getenv("DBG_TSQ") != "" {
		fmt.Fprintf(os.Stderr, "[dbg] tsq B=%x Payload=%x\n", B, Payload)
	}
	if len(Payload) < 8 {
		return DecodeDefault(B)
	}
	size := int(int32(binary.LittleEndian.Uint32(Payload)))
	if size <= 0 {
		return ""
	}
	itemsStart := 4
	itemsEnd := itemsStart + 12*size
	if itemsEnd > len(Payload) {
		return DecodeDefault(B)
	}
	operands := Payload[itemsEnd:]
	curPos := 0
	var sb strings.Builder
	var infix func(out *strings.Builder, parentPriority int, rightPhraseOp bool)
	infix = func(out *strings.Builder, parentPriority int, rightPhraseOp bool) {
		if curPos >= size {
			return
		}
		item := Payload[itemsStart+12*curPos : itemsStart+12*curPos+12]
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
// v1.0.47：对齐 PG boolout（输出 "t"/"f"，非 "true"/"false"）。
func decodeBool(B []byte) string {
	if len(B) == 0 {
		return "f"
	}
	v := B[0]
	if v == 1 || v == 0x74 || v == 't' || v == 0x31 {
		return "t"
	}
	return "f"
}

func decodeInt1(B []byte) string {
	if len(B) == 0 {
		return "0"
	}
	return strconv.Itoa(int(int8(B[0])))
}

// decodeBinary：金仓 V9 mysql 模式 BINARY 类型。
// 存储为 varlena（短/长头）+ 定长空格/\x00 填充。导出策略：
//   - 剥离 varlena 头后，若内容为纯 UTF8 文本 → 直接输出（尾随 \x00 填充去除，导入时 binary 自动补齐）；
//   - 内容含 \x00（中间填充）或非法 UTF8 → 输出 \x hex（字节可逆，SQL E'\\x...' / CSV \x 均可回导）。
func decodeBinary(B []byte) string {
	Payload, _, _ := VarPayload(B)
	if Payload == nil {
		return ""
	}
	for len(Payload) > 0 && Payload[len(Payload)-1] == 0 {
		Payload = Payload[:len(Payload)-1]
	}
	for _, c := range Payload {
		if c == 0 {
			return "\\x" + hex.EncodeToString(Payload)
		}
	}
	if utf8.Valid(Payload) {
		return string(Payload)
	}
	return "\\x" + hex.EncodeToString(Payload)
}

func decodeInt2(B []byte) string { return strconv.Itoa(int(int16(binary.LittleEndian.Uint16(B)))) }

func decodeInt4(B []byte) string { return strconv.Itoa(int(int32(binary.LittleEndian.Uint32(B)))) }

func decodeInt8(B []byte) string {
	return strconv.FormatInt(int64(binary.LittleEndian.Uint64(B)), 10)
}

func DecodeInt16KB(B []byte) string {
	// 金仓 int16（4659）：16 字节小端有符号
	// B6 修复：统一 big.Int 补码处理，修复两处缺陷：
	//   ① 原 hi==^0 分支用 int64(lo) 直接返回，lo<2^63 时符号完全颠倒（负值输出为正）；
	//   ② 原 big.Int 分支 2^128-1-v 再 Neg 恒多减 1（min int128 got=-2^127+1）。
	if len(B) < 16 {
		return "0"
	}
	lo := binary.LittleEndian.Uint64(B[:8])
	hi := binary.LittleEndian.Uint64(B[8:16])
	bi := new(big.Int).SetUint64(hi)
	bi.Lsh(bi, 64)
	bi.Or(bi, new(big.Int).SetUint64(lo))
	if bi.Bit(127) == 1 {
		// 16 字节补码负数：减 2^128（等价 v - 2^128）
		bi.Sub(bi, new(big.Int).Lsh(big.NewInt(1), 128))
	}
	return bi.String()
}

// fmtFloat：对齐 PG float8out/float4out（PG14+ Ryu 最短十进制表示）。
// bits=64 对齐 float8out，bits=32 对齐 float4out（float4 用 32 位最短表示，
// 如 3.14f 实际存储 3.140000104904175，但 float4out 输出 "3.14"）。
// 指数在 [-4,15] 时用定点，否则科学计数（同 PG double 转换的记法阈值）；
// 整数值不加 ".0"（PG 输出 "1" 而非 "1.0"，v1.0.47 修复）。
func fmtFloat(v float64, bits int) string {
	if math.IsNaN(v) {
		return "NaN"
	}
	if math.IsInf(v, 1) {
		return "Infinity"
	}
	if math.IsInf(v, -1) {
		return "-Infinity"
	}
	S := strconv.FormatFloat(v, 'g', -1, bits)
	if i := strings.IndexAny(S, "eE"); i >= 0 {
		exp, err := strconv.Atoi(S[i+1:])
		if err == nil && exp >= -4 && exp <= 15 {
			S = strconv.FormatFloat(v, 'f', -1, bits)
		}
		return S
	}
	return S
}

func decodeFloat4(B []byte) string {
	return fmtFloat(float64(math.Float32frombits(binary.LittleEndian.Uint32(B))), 32)
}

func decodeFloat8(B []byte) string {
	return fmtFloat(math.Float64frombits(binary.LittleEndian.Uint64(B)), 64)
}

func DecodeVarlenaText(B []byte) string {
	Payload, _, _ := VarPayload(B)
	return charset.DecodeBytes(Payload)
}

func decodeText(B []byte) string { return DecodeVarlenaText(B) }

func decodeBpchar(B []byte) string { return DecodeVarlenaText(B) }

func decodeVarchar(B []byte) string {
	return DecodeVarlenaText(B)
}

func decodeName(B []byte) string { return CString(B, 0) }

func decodeBytea(B []byte) string {
	Payload, _, _ := VarPayload(B)
	return "\\x" + hex.EncodeToString(Payload)
}

// decodeByteaEscape：金仓 blob（oid 8013，typinput=byteain）的输出格式 =
// PG bytea escape：可打印 ASCII（0x20-0x7e，除 \）原样，\ 翻倍，其余 "\ooo" 八进制。
// 如 deadbeef... → "\336\255\276\357..."。v1.0.47 修复（旧实现输出 hex "\x..."）。
func decodeByteaEscape(B []byte) string {
	Payload, _, _ := VarPayload(B)
	var sb strings.Builder
	for _, c := range Payload {
		if c == '\\' {
			sb.WriteString(`\\`)
		} else if c >= 0x20 && c <= 0x7e {
			sb.WriteByte(c)
		} else {
			fmt.Fprintf(&sb, "\\%03o", c)
		}
	}
	return sb.String()
}

// decodeKBBinary：金仓 mysql/oracle 模式 binary/varbinary（oid 3383/3384，typinput=binaryin）。
// 输出与金仓 binary_out 一致："0x" + 大写 hex；binary(n) 尾部 \x00 填充属于数据，
// 不裁剪（实测 binary(8) 存 'ab' 输出 0x6162000000000000）。v1.0.47 修复（旧实现
// 走 binaryin→decodeBinary 的 UTF8/尾零推断，与金仓真实输出不符）。
func decodeKBBinary(B []byte) string {
	Payload, _, _ := VarPayload(B)
	if Payload == nil {
		return "0x"
	}
	return "0x" + strings.ToUpper(hex.EncodeToString(Payload))
}

func decodeOid(B []byte) string { return strconv.FormatUint(uint64(binary.LittleEndian.Uint32(B)), 10) }

func decodeXid(B []byte) string { return decodeOid(B) }

func decodeCid(B []byte) string { return decodeOid(B) }

func decodeTid(B []byte) string {
	blk := binary.LittleEndian.Uint32(B)
	off := binary.LittleEndian.Uint16(B[4:])
	return fmt.Sprintf("(%d,%d)", blk, off)
}

func DecodeDate(B []byte) string {
	days := int32(binary.LittleEndian.Uint32(B))
	if days == 0x7FFFFFFF {
		return "infinity"
	}
	if days == -0x80000000 {
		return "-infinity"
	}
	// 2000-01-01 + days
	t := Date2000.AddDate(0, 0, int(days))
	y, m, d := t.Date()
	if y <= 0 {
		// v1.0.56 修复（P3-G14）：BC 日期按 PG date_out 输出 "0044-03-15 BC"。
		// 旧版走 Go Format 负年份输出 "-0044-03-15"，date_in 拒绝、回灌失败。
		// PG 内部 year<=0 即 BC（无公元 0 年）：天文年 0 = 公元前 1 年。
		return fmt.Sprintf("%04d-%02d-%02d BC", 1-y, int(m), d)
	}
	return fmt.Sprintf("%04d-%02d-%02d", y, int(m), d)
}

func timeFromUS(us int64) string {
	us %= 86400 * 1000000
	h := us / (3600 * 1000000)
	us %= 3600 * 1000000
	m := us / (60 * 1000000)
	us %= 60 * 1000000
	S := us / 1000000
	micro := us % 1000000
	if micro != 0 {
		return fmt.Sprintf("%02d:%02d:%02d.%s", h, m, S, strings.TrimRight(fmt.Sprintf("%06d", micro), "0"))
	}
	return fmt.Sprintf("%02d:%02d:%02d", h, m, S)
}

func decodeTime(B []byte) string {
	us := int64(binary.LittleEndian.Uint64(B))
	return timeFromUS(us)
}

// decodeMysqlTime 金仓 V9 mysql 模式 time（oid 7950）。
// 磁盘格式：int64 微秒自 0 点，支持 24:00:00（86400 秒，MySQL TIME 合法上限）。
// 与 decodeTime 不同：不做 86400 秒取模（避免 24:00:00 被归一化为 00:00:00）。
func decodeMysqlTime(B []byte) string {
	us := int64(binary.LittleEndian.Uint64(B))
	neg := false
	if us < 0 {
		neg = true
		us = -us
	}
	h := us / (3600 * 1000000)
	us %= 3600 * 1000000
	m := us / (60 * 1000000)
	us %= 60 * 1000000
	S := us / 1000000
	micro := us % 1000000
	var body string
	if micro != 0 {
		body = fmt.Sprintf("%02d:%02d:%02d.%s", h, m, S, strings.TrimRight(fmt.Sprintf("%06d", micro), "0"))
	} else {
		body = fmt.Sprintf("%02d:%02d:%02d", h, m, S)
	}
	if neg {
		return "-" + body
	}
	return body
}

func decodeTimestamp(B []byte) string {
	us := int64(binary.LittleEndian.Uint64(B))
	if us == 0x7FFFFFFFFFFFFFFF {
		return "infinity"
	}
	if us == -0x8000000000000000 {
		return "-infinity"
	}
	return TimestampFromUS(us)
}

func decodeTimestamptz(B []byte) string {
	us := int64(binary.LittleEndian.Uint64(B))
	if us == 0x7FFFFFFFFFFFFFFF {
		return "infinity"
	}
	if us == -0x8000000000000000 {
		return "-infinity"
	}
	return TimestampFromUS(us) + "+00"
}

// MysqlTZOffsetUS：金仓 mysql 模式 timestamp 解码偏移（微秒）。
// B9 修复：由 --tz 参数或数据目录 conf 的 timezone 推导（main.go parseTZOffset/parseConfTimezone），
// 未指定且推导失败时保持 +8（向后兼容）并告警一次。
var (
	MysqlTZOffsetUS int64 = 8 * 3600 * 1000000
	MysqlTZSet      bool
	mysqlTZWarned   sync.Once
)

// kbOracleMode：金仓 oracle 兼容模式（离线探测：kingbase.conf 含 ora_* GUC）。
// oracle 模式 timestamp_out 微秒恒定 6 位（".789000"），PG/PG 兼容模式去尾零。
// 由 InitDynamicDecodersOnce 按数据目录设置（v1.0.47）。
var kbOracleMode bool

// DecodeMysqlTimestamp 金仓 V9 mysql 模式 timestamp（oid 7954）。
// 磁盘格式：int64 微秒，基准 2000-01-01 00:00:00 UTC（mysql_timestamp_in 将本地时间转 UTC 存储，秒精度）。
// 输出与 mysql_timestamp_out 一致：按会话时区转回本地时间文本。
// B9 修复：原硬编码 +8（const utc8=8*3600*1000000），非 +8 时区实例/会话写入的数据
// 导出偏移（金仓 V8 mysql 模式实测 +9 会话行偏移 1 小时）。改为全局可配置偏移：
// --tz 显式指定 > 数据目录 conf timezone 推导 > 默认 +8（并告警）。
func DecodeMysqlTimestamp(B []byte) string {
	us := int64(binary.LittleEndian.Uint64(B))
	if us == 0x7FFFFFFFFFFFFFFF {
		return "infinity"
	}
	if us == -0x8000000000000000 {
		return "-infinity"
	}
	if !MysqlTZSet {
		mysqlTZWarned.Do(func() {
			logx.Logf("警告: 未能确定金仓 mysql 实例时区，timestamp 按 UTC+8 解码（可用 --tz 指定，如 --tz +09:00）")
		})
	}
	return TimestampFromUS(us + MysqlTZOffsetUS)
}

// CivilFromDays：自 1970-01-01 起的天数 → 公历 (y,m,d)。
// Howard Hinnant days_from_civil 逆算法，int64 全程无溢出，支持 PG 全时间范围（4713BC~294276AD）。
// v1.0.56 修复（P3-G14 附带发现）：Go 的 / 向零截断，z 为负（0000-03-01 之前，
// 覆盖 PG 下限 4713 BC 起的整个区间）时 era 求商错位 → 年月日解算整体错乱
// （如 0001-01-01 BC 解出 (0,2,-29)）。补齐 Hinnant 原版的负数 floor 处理。
func CivilFromDays(z int64) (int, int, int) {
	z += 719468
	var era int64
	if z >= 0 {
		era = z / 146097
	} else {
		era = (z - 146096) / 146097 // floor 除法（Go / 向零截断的补偿）
	}
	doe := z - era*146097 // [0, 146096]，此后各除法分母均为正、操作数非负
	yoe := (doe - doe/1460 + doe/36524 - doe/146096) / 365
	y := yoe + era*400
	doy := doe - (365*yoe + yoe/4 - yoe/100)
	mp := (5*doy + 2) / 153
	d := doy - (153*mp+2)/5 + 1
	m := mp + 3
	if m > 12 {
		m -= 12
	}
	if m < 3 {
		y++
	}
	return int(y), int(m), int(d)
}

func TimestampFromUS(us int64) string {
	// 2000-01-01 00:00:00 + us
	sec := us / 1000000
	micro := us % 1000000
	if micro < 0 {
		// 2000 年前的微秒为负：向下取整，micro 归一化为正
		sec--
		micro += 1000000
	}
	// 天数/日内秒拆分（2000-01-01 基准；相对 1970-01-01 偏移 10957 天）
	days := sec / 86400
	rem := sec % 86400
	if rem < 0 {
		days--
		rem += 86400
	}
	y, m, d := CivilFromDays(days + 10957)
	hh := rem / 3600
	mm := (rem % 3600) / 60
	ss := rem % 60
	// v1.0.56 修复（P3-G14）：BC 时间戳对齐 PG timestamp_out（"0044-03-15 00:00:00 BC"），
	// 旧版负年份 "-0044-…" 回灌失败（同 DecodeDate）。
	base := ""
	if y <= 0 {
		base = fmt.Sprintf("%04d-%02d-%02d %02d:%02d:%02d BC", 1-y, m, d, hh, mm, ss)
	} else {
		base = fmt.Sprintf("%04d-%02d-%02d %02d:%02d:%02d", y, m, d, hh, mm, ss)
	}
	if micro != 0 {
		frac := fmt.Sprintf("%06d", micro)
		// v1.0.47：金仓 oracle 模式 timestamp_out 微秒恒定 6 位（".789000"），PG 风格去尾零（".789"）
		if !kbOracleMode {
			frac = strings.TrimRight(frac, "0")
		}
		return base + "." + frac
	}
	return base
}

func decodeTimetz(B []byte) string {
	us := int64(binary.LittleEndian.Uint64(B))
	zone := int32(binary.LittleEndian.Uint32(B[8:]))
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
	// v1.0.47：对齐 PG timetz_out——分钟为 0 时省略（"08:00:00+08"，非 "+08:00"）；秒永不输出
	off := fmt.Sprintf("%s%02d", sign, z/3600)
	if mm := z % 3600 / 60; mm != 0 {
		off += fmt.Sprintf(":%02d", mm)
	}
	return fmt.Sprintf("%s%s", t, off)
}

// tsvector 内部格式（PG ts_type.h）：
//
//	int32 size（词数，LE）
//	WordEntry[size]：uint32 位打包（小端：bit0=haspos，bit1-11=len 词长，bit12-31=pos 词字符串偏移）
//	词字符串按 entry 顺序连续存储；若 haspos=1，词后有 2 字节对齐填充 + uint16 位置数 + uint16 WordEntryPos[]
//
// WordEntryPos 为 uint16：位 13-0 位置，位 15-14 权重（0=A,1=B,2=C,3=D）
func decodeTsvector(B []byte) string {
	Payload, _, _ := VarPayload(B)
	B = Payload
	if len(B) < 4 {
		return DecodeDefault(B)
	}
	size := int32(binary.LittleEndian.Uint32(B[:4]))
	if size < 0 || 4+int(size)*4 > len(B) {
		return DecodeDefault(B)
	}
	cur := 4 + int(size)*4 // data 区起点
	var parts []string
	for i := 0; i < int(size); i++ {
		e := binary.LittleEndian.Uint32(B[4+i*4:])
		haspos := e & 1
		l := int((e >> 1) & 0x7FF)
		if cur+l > len(B) {
			break
		}
		w := string(B[cur : cur+l])
		cur += l
		if haspos != 0 {
			if cur&1 != 0 {
				cur++ // 2 字节对齐填充
			}
			if cur+2 > len(B) {
				break
			}
			np := int(binary.LittleEndian.Uint16(B[cur:]))
			cur += 2
			var poss []string
			for j := 0; j < np && cur+2 <= len(B); j++ {
				p16 := binary.LittleEndian.Uint16(B[cur:])
				cur += 2
				p := p16 & 0x3FFF
				wgt := p16 >> 14
				S := strconv.Itoa(int(p))
				// PG tsvectorout：WEP 权重 3→'A'、2→'B'、1→'C'、0→不显示
				switch wgt {
				case 1:
					S += "C"
				case 2:
					S += "B"
				case 3:
					S += "A"
				}
				poss = append(poss, S)
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

func decodeInterval(B []byte) string {
	timeUS := int64(binary.LittleEndian.Uint64(B))
	day := int32(binary.LittleEndian.Uint32(B[8:]))
	month := int32(binary.LittleEndian.Uint32(B[12:]))
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
		// B10 修复：仅 |day|==1 用单数（PG intervalout 语义：-1 也是复数 "-1 days"）
		if day == 1 {
			parts = append(parts, "1 day")
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
	// v1.0.47：对齐 PG interval_out——正时间部分【不加】"+"（"1 year 2 mons 3 days 04:05:06.789"），
	// 负时间部分才有 "-" 前缀（PG17/金仓 PG 模式实测一致，旧实现的 "+" 规则不成立）
	if timeUS != 0 {
		parts = append(parts, timeStr)
	}
	if len(parts) == 0 {
		return "00:00:00"
	}
	return strings.Join(parts, " ")
}

// ---------- numeric ----------
func DecodeNumeric(B []byte) string {
	Payload, _, _ := VarPayload(B)
	return numericDiskToStr(Payload)
}

func numericDiskToStr(Payload []byte) string {
	if len(Payload) < 2 {
		return "0"
	}
	header := binary.LittleEndian.Uint16(Payload)
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
			weight = int(^uint16(0x3F)) | int(header&0x3F)
			weight &= 0xFFFF
			if weight&0x8000 != 0 {
				weight -= 0x10000
			}
		} else {
			weight = int(header & 0x3F)
		}
		digitsRaw = Payload[2:]
	} else {
		// Long 格式
		if len(Payload) < 4 {
			return "0"
		}
		neg = header&0x4000 != 0
		dscale = int(header & 0x3FFF)
		weight = int(int16(binary.LittleEndian.Uint16(Payload[2:])))
		digitsRaw = Payload[4:]
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
func decodeUUID(B []byte) string {
	if len(B) < 16 {
		return ""
	}
	u := B[:16]
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

func decodeJSON(B []byte) string { return DecodeVarlenaText(B) }

// ---------- jsonb（PG jsonb_util.c 语义） ----------
const (
	JB_FSCALAR       = 0x10000000
	JB_FOBJECT       = 0x20000000
	JB_FARRAY        = 0x40000000
	JENTRY_HAS_OFF   = 0x80000000
	JENTRY_TYPEMASK  = 0x70000000
	JENTRY_ISNUM     = 0x10000000
	JENTRY_ISBOOLF   = 0x20000000
	JENTRY_ISBOOLT   = 0x30000000
	JENTRY_ISNULL    = 0x40000000
	JENTRY_ISCONTAIN = 0x50000000
)

// jsonbSlice：jsonb 坏数据切片钳制（v1.0.56 修复 P3-G13）。
// jsonb 头部/条目长度字段直接来自磁盘，坏页/截断元组中 e 可越过 Payload
// 末尾、numeric 分支的 aligned 可落在 dataOff+e 之后，直接切片会 panic。
// 统一在此校验，非法范围返回 false（上层输出 null）。
func jsonbSlice(Payload []byte, lo, hi int) ([]byte, bool) {
	if lo < 0 || hi < lo || lo > len(Payload) || hi > len(Payload) {
		return nil, false
	}
	return Payload[lo:hi], true
}

func JsonbScalarToText(je uint32, Payload []byte, dataOff, S, e int) string {
	t := je & JENTRY_TYPEMASK
	switch t {
	case 0:
		raw, ok := jsonbSlice(Payload, dataOff+S, dataOff+e)
		if !ok {
			return "null"
		}
		j, _ := json.Marshal(charset.DecodeBytes(raw))
		return string(j)
	case JENTRY_ISNUM:
		aligned := dataOff + ((S + 3) &^ 3)
		raw, ok := jsonbSlice(Payload, aligned, dataOff+e)
		if !ok {
			return "null"
		}
		vp, _, _ := VarPayload(raw)
		return numericDiskToStr(vp)
	case JENTRY_ISBOOLT:
		return "true"
	case JENTRY_ISBOOLF:
		return "false"
	case JENTRY_ISNULL:
		return "null"
	case JENTRY_ISCONTAIN:
		aligned := dataOff + ((S + 3) &^ 3)
		raw, ok := jsonbSlice(Payload, aligned, dataOff+e)
		if !ok {
			return "null"
		}
		return jsonbContainerToText(raw)
	}
	return "null"
}

func jsonbContainerToText(Payload []byte) string {
	if len(Payload) < 4 {
		return ""
	}
	header := binary.LittleEndian.Uint32(Payload)
	flags := header & 0xF0000000
	count := int(header & 0x0FFFFFFF)
	scalar := flags&JB_FSCALAR != 0
	isObj := flags&JB_FOBJECT != 0
	nEnt := count
	if isObj {
		nEnt = count * 2
	}
	dataOff := 4 + 4*nEnt
	if dataOff > len(Payload) {
		return ""
	}
	ents := make([]uint32, nEnt)
	for i := 0; i < nEnt; i++ {
		ents[i] = binary.LittleEndian.Uint32(Payload[4+4*i:])
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
		S := getOffset(idx)
		return JsonbScalarToText(ents[idx], Payload, dataOff, S, S+jlen(idx))
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
			keyRaw, ok := jsonbSlice(Payload, dataOff+keyOff, dataOff+keyOff+klen)
			if !ok {
				keyRaw = nil
			}
			key := charset.DecodeBytes(keyRaw)
			vs := getOffset(count + i)
			v := JsonbScalarToText(ents[count+i], Payload, dataOff, vs, vs+jlen(count+i))
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

func decodeJSONB(B []byte) string {
	Payload, _, _ := VarPayload(B)
	return jsonbContainerToText(Payload)
}

// ---------- 数组 ----------
// 常见数组元素类型 → (attlen, attalign, 解码函数, 文本退化?)
var arrayElemInfo = map[uint32][4]interface{}{
	BOOLOID:        {1, "c", "bool", false},
	BYTEAOID:       {-1, "i", "", true},
	CHAROID:        {1, "c", "char", false},
	NAMEOID:        {64, "c", "name", false},
	INT8OID:        {8, "d", "int8", false},
	INT2OID:        {2, "S", "int2", false},
	INT4OID:        {4, "i", "int4", false},
	TEXTOID:        {-1, "i", "", true},
	OIDOID:         {4, "i", "oid", false},
	FLOAT4OID:      {4, "i", "float4", false},
	FLOAT8OID:      {8, "d", "float8", false},
	UNKNOWNOID:     {-1, "i", "", true},
	BPCHAROID:      {-1, "i", "", true},
	VARCHAROID:     {-1, "i", "", true},
	DATEOID:        {4, "i", "date", false},
	TIMEOID:        {8, "d", "time", false},
	TIMESTAMPOID:   {8, "d", "timestamp", false},
	TIMESTAMPTZOID: {8, "d", "timestamptz", false},
	TIMETZOID:      {12, "d", "timetz", false},
	INTERVALOID:    {16, "d", "interval", false},
	NUMERICOID:     {-1, "i", "", true},
	UUIDOID:        {16, "c", "uuid", false},
	JSONBOID:       {-1, "i", "jsonb", false},
	ACLITEMOID:     {12, "i", "aclitem", false},
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

var alignSizes = map[string]int{"c": 1, "S": 2, "i": 4, "d": 8}

func arrayElemText(Payload []byte, pos int, elemOID uint32) (string, int) {
	// v1.0.48 修复（P0-A5）：三个 varlena 分支原先都没有 pos 边界检查，
	// dataoffset 损坏、或前一元素推进过头时 Payload[pos] 直接越界 panic。
	if pos < 0 || pos >= len(Payload) {
		return "", len(Payload)
	}
	// v1.0.48 修复（P1-F1）：枚举元素。枚举在磁盘上是 4 字节成员 OID，
	// 必须经 EnumMap 还原标签；原实现无此分支，枚举数组整列退化成 \x 十六进制。
	if em, ok := EnumMapSnapshot()[elemOID]; ok {
		if pos+4 > len(Payload) {
			return "", len(Payload)
		}
		mo := binary.LittleEndian.Uint32(Payload[pos:])
		for _, m := range em {
			if m.MemberOID == mo {
				return m.Label, pos + 4
			}
		}
		return strconv.FormatUint(uint64(mo), 10), pos + 4
	}
	if info, ok := arrayElemInfo[elemOID]; ok {
		alen := info[0].(int)
		aalign := info[1].(string)
		fn := info[2].(string)
		degrade := info[3].(bool)
		if alen == -1 {
			first := Payload[pos]
			if first&1 != 0 {
				total := int(first >> 1)
				if total < 1 || pos+total > len(Payload) {
					return "", len(Payload)
				}
				seg := Payload[pos+1 : pos+total]
				if degrade {
					return charset.DecodeBytes(seg), pos + total
				}
				return callElemDecoder(fn, seg), pos + total
			}
			a := alignSizes[aalign]
			if a > 1 && pos%a != 0 {
				pos = (pos + a - 1) &^ (a - 1)
			}
			if pos+4 > len(Payload) {
				return "", len(Payload)
			}
			total := int(binary.LittleEndian.Uint32(Payload[pos:]) >> 2)
			if total < 4 || pos+total > len(Payload) {
				return "", len(Payload)
			}
			seg := Payload[pos+4 : pos+total]
			if degrade {
				return charset.DecodeBytes(seg), pos + total
			}
			return callElemDecoder(fn, seg), pos + total
		}
		// v1.0.46 修复（float8[]/int8[]/timestamp[] 错位）：
		// PG 数组的定长元素在磁盘上【紧密排列】，元素之间不做对齐填充——
		// 对齐只作用于数组头（bodyOff = 12+8*ndim，恒为 4 的倍数）；
		// 仅 varlena 元素之间按 INTALIGN(4) 对齐（见上面 alen==-1 分支）。
		// 旧实现对 8 字节元素按 attalign=8 对齐：1 维数组 bodyOff=20 → 被推到 24，
		// 越 4 字节错位读取，解出 -9.12e+192 之类乱值。
		// 实机取证（PG17 + 金仓，pageinspect + 原始页字节）：{1.5,-2.25} 元素从 Payload+20 起，
		// 中间无任何填充；{{1.5,2.5},{3.5,4.5}} 从 Payload+28 起紧密排列。
		if pos+alen > len(Payload) {
			return "", len(Payload) // 损坏数据兜底：越界即停止推进
		}
		seg := Payload[pos : pos+alen]
		if degrade {
			return charset.DecodeBytes(seg), pos + alen
		}
		return callElemDecoder(fn, seg), pos + alen
	}
	// 未知元素类型：先查方案 C 动态类型定义（v1.0.16）
	if DynamicTypeDefs != nil {
		if td, ok := DynamicTypeDefs[elemOID]; ok {
			// v1.0.48 修复（P1-F1）：复合类型元素（如 contact[]）走 DecodeComposite，
			// 与标量复合列同一条解码链路；原实现落 decodeBytes 输出 \x hex。
			// 注意 DecodeComposite 内部自己剥 varlena 头（与 DecodeValue 调用约定一致），
			// 必须喂【含头】的完整字节，不能像 decodeBytes 那样先剥头。
			wantFull := td.Type == 'c'
			dec := func(B []byte) string { return charset.DecodeBytes(B) }
			if wantFull {
				dec = func(B []byte) string { return DecodeComposite(elemOID, B) }
			} else if DynamicDecoders != nil {
				if d, ok2 := DynamicDecoders[elemOID]; ok2 {
					dec = d
				}
			}
			if td.Len < 0 { // varlena 元素
				first := Payload[pos]
				if first&1 != 0 {
					total := int(first >> 1)
					if total < 1 || pos+total > len(Payload) {
						return "", len(Payload)
					}
					if wantFull {
						return dec(Payload[pos : pos+total]), pos + total
					}
					return dec(Payload[pos+1 : pos+total]), pos + total
				}
				if pos+4 > len(Payload) {
					return "", len(Payload)
				}
				total := int(binary.LittleEndian.Uint32(Payload[pos:]) >> 2)
				if total < 4 || pos+total > len(Payload) {
					return "", len(Payload)
				}
				if wantFull {
					return dec(Payload[pos : pos+total]), pos + total
				}
				return dec(Payload[pos+4 : pos+total]), pos + total
			}
			// 定长元素：紧密排列（v1.0.46 修复，理由同 arrayElemInfo 分支：元素间无对齐填充）
			if td.Len > 0 {
				if pos+td.Len > len(Payload) {
					return "", len(Payload)
				}
				return dec(Payload[pos : pos+td.Len]), pos + td.Len
			}
		}
	}
	// 未知元素类型：按 varlena 文本退化
	first := Payload[pos]
	if first&1 != 0 {
		total := int(first >> 1)
		if total < 1 || pos+total > len(Payload) {
			return "", len(Payload)
		}
		return charset.DecodeBytes(Payload[pos+1 : pos+total]), pos + total
	}
	if pos+4 > len(Payload) {
		return "", len(Payload)
	}
	total := int(binary.LittleEndian.Uint32(Payload[pos:]) >> 2)
	if total < 4 || pos+total > len(Payload) {
		return "", len(Payload)
	}
	return charset.DecodeBytes(Payload[pos+4 : pos+total]), pos + total
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
		return DecodeDate(seg)
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
	return charset.DecodeBytes(seg)
}

// maxArrayElems：单个数组值的元素数上限（v1.0.48）。
// PG 单值上限 1GB，元素数不可能超过 1<<27；取 1<<24 作为溢出/损坏数据的拦截阈值，
// 防止 dims 连乘时 int 回绕成负数导致 make([]bool, 负数) 之类 panic。
const maxArrayElems = 1 << 24

// UserArrayElemOID：若 oid 是【数组类型】且其元素类型反向指回自己，返回元素类型 oid。
// v1.0.48 修复（P1-F1）：pg_type 中数组类型自身的 typarray 恒为 0，
// 只有元素类型的 typarray 指向数组类型。旧判据 `td.Array == oid` 永远不成立，
// 导致自定义数组（enum[] / 复合[] / 域[]）从未注册 DecodeArray，
// 也拿不到 DDL 里的 "xxx[]" 名称，整列退化成 \x 十六进制。
func UserArrayElemOID(oid uint32) (uint32, bool) {
	if DynamicTypeDefs == nil {
		return 0, false
	}
	td, ok := DynamicTypeDefs[oid]
	if !ok || td.Type != 'b' || td.Elem == 0 {
		return 0, false
	}
	et, ok := DynamicTypeDefs[td.Elem]
	if !ok || et.Array != oid {
		return 0, false
	}
	return td.Elem, true
}

// dynElemAlign：自定义（非硬编码 arrayElemInfo）数组元素的 typalign（1/2/4/8）。
// 定长元素返回 1——PG 数组里定长元素紧密排列、不做对齐填充（见 arrayElemText 注释）。
// varlena 元素（复合类型 typalign='d' 等）需要按该对齐推进游标。
func dynElemAlign(elemOID uint32) int {
	if DynamicTypeDefs == nil {
		return 1
	}
	td, ok := DynamicTypeDefs[elemOID]
	if !ok || td.Len > 0 {
		return 1
	}
	if a, ok := alignSizes[string(td.Align)]; ok {
		return a
	}
	return 4 // varlena 默认 INTALIGN
}

func DecodeArray(B []byte) string {
	Payload, _, _ := VarPayload(B)
	if len(Payload) < 12 {
		return "{}"
	}
	ndim := int(int32(binary.LittleEndian.Uint32(Payload)))
	dataoffset := int(int32(binary.LittleEndian.Uint32(Payload[4:])))
	elemtype := binary.LittleEndian.Uint32(Payload[8:])
	if ndim <= 0 || ndim > 6 {
		return "{}"
	}
	dims := make([]int, ndim)
	nelems := 1
	// v1.0.48 修复（P0-A1/A2）：PG 数组头 = 12B 固定头 + dims[ndim] + lbs[ndim]，
	// 合计 12+8*ndim 字节。原实现只校验 ndim<=6，不校验 Payload 实际长度，
	// 字段被截断（页尾撕裂/坏页/TOAST 组装残缺）时 Payload[12+4*i:] 直接越界 panic；
	// 且 nelems 连乘无溢出保护，6 维各 1<<30 会回绕成负数，
	// 随后的 make([]bool, nelems) / 位图索引同样 panic。两者统一在此拦截。
	bodyOff := 12 + 8*ndim
	if bodyOff > len(Payload) {
		return DecodeDefault(B)
	}
	for i := 0; i < ndim; i++ {
		dims[i] = int(int32(binary.LittleEndian.Uint32(Payload[12+4*i:])))
		if dims[i] <= 0 {
			return "{}"
		}
		if dims[i] > maxArrayElems || nelems > maxArrayElems/dims[i] {
			return DecodeDefault(B) // 元素总数超限/溢出 → 退回原始字节
		}
		nelems *= dims[i]
	}
	var nulls []bool
	elemOff := bodyOff
	if dataoffset > 0 {
		bmLen := (nelems + 7) / 8
		// v1.0.35：损坏数据（位图区超长）时返回原始字节兜底，避免 slice panic
		if bodyOff+bmLen > len(Payload) {
			return DecodeDefault(B)
		}
		bm := Payload[bodyOff : bodyOff+bmLen]
		nulls = make([]bool, nelems)
		allNull := true
		for i := 0; i < nelems; i++ {
			nulls[i] = bm[i/8]&(1<<(i%8)) == 0
			if !nulls[i] {
				allNull = false
			}
		}
		elemOff = dataoffset - 4
		elemOff = (elemOff + 3) &^ 3
		// v1.0.46 修复：全 NULL 数组（如 '{NULL,NULL}'::int4[]）的 dataoffset 会指向
		// 数据区末尾之外（根本没有元素数据），旧守卫 `dataoffset > len(Payload)` 误判为
		// 损坏数据 → 整列退化成 \x 十六进制原文。全 NULL 时无需读取元素，直接放行。
		if elemOff > len(Payload) {
			if !allNull {
				return DecodeDefault(B)
			}
			elemOff = len(Payload)
		}
	}
	texts := make([]string, 0, nelems)
	pos := elemOff
	// v1.0.48 修复（P1-F1）：自定义 varlena 元素（复合/域/text 域等）在 PG 里按
	// 元素类型 typalign 对齐，且对齐基准是【数据区起点 bodyOff】而不是数组头起点。
	// PG17 实机取证：contact[]（typalign='d'）两个元素在 Payload 20 / 60，
	// 第一个 36 字节后补 4 字节 0，元素间距 40——旧实现按紧密排列读，
	// 第二个元素头读到 0x00 → 长度 0 → 输出空串（{"(\u2026)",""}）。
	// 硬编码表里已有的类型维持原样（其 1 字节短头/外部指针不对齐规则见 arrayElemText）。
	elemAlign := 1
	if _, known := arrayElemInfo[elemtype]; !known {
		elemAlign = dynElemAlign(elemtype)
	}
	for i := 0; i < nelems; i++ {
		if nulls != nil && nulls[i] {
			texts = append(texts, arrayNullSentinel)
			continue
		}
		t, np := arrayElemText(Payload, pos, elemtype)
		pos = np
		if elemAlign > 1 {
			if d := (pos - bodyOff) % elemAlign; d != 0 {
				pos += elemAlign - d
			}
		}
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
				S, _ := x.(string)
				sb.WriteString(arrayQuote(S))
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

// arrayNullSentinel：数组真 NULL 元素的内部标记（PG 文本不含 \x00，安全）；
// fmtArr 输出时还原为无引号 "NULL"，字符串元素恰为 "NULL" 时正常加引号（v1.0.35 修复数据失真）。
const arrayNullSentinel = "\x00PGSQL_ARRAY_NULL"

func arrayQuote(S string) string {
	if S == arrayNullSentinel {
		return "NULL"
	}
	// v1.0.46：与 PG array_out 对齐——元素含任意空白（不限于首尾）、分隔符、引号、反斜杠，
	// 或为空串/等于 NULL 时必须加引号（旧实现只判首尾空格，'a B' 直接裸输出，与 PG 真值不一致）
	needs := S == "" || strings.EqualFold(S, "NULL") || strings.ContainsAny(S, ",{}\"\\") ||
		strings.IndexFunc(S, unicode.IsSpace) >= 0
	if !needs {
		return S
	}
	S = strings.ReplaceAll(S, "\\", "\\\\")
	S = strings.ReplaceAll(S, "\"", "\\\"")
	return "\"" + S + "\""
}

// ---------- inet/cidr/macaddr/bit/money ----------
func decodeInet(B []byte, forceCIDR bool) string {
	Payload, _, _ := VarPayload(B)
	if len(Payload) < 2 {
		return ""
	}
	family := Payload[0]
	bits := Payload[1]
	if family != 2 && family != 3 {
		return ""
	}
	nbytes := 4
	if family == 3 {
		nbytes = 16
	}
	// v1.0.35：损坏数据（Payload 短于地址区）时返回空串兜底，避免 slice panic
	if len(Payload) < 2+nbytes {
		return ""
	}
	addr := Payload[2 : 2+nbytes]
	var S string
	if family == 2 {
		parts := make([]string, 4)
		for i := 0; i < 4; i++ {
			parts[i] = strconv.Itoa(int(addr[i]))
		}
		S = strings.Join(parts, ".")
	} else {
		parts := make([]string, 8)
		for i := 0; i < 8; i++ {
			parts[i] = fmt.Sprintf("%x", int(addr[i*2])<<8|int(addr[i*2+1]))
		}
		S = strings.Join(parts, ":")
	}
	defaultBits := uint8(32)
	if family == 3 {
		defaultBits = 128
	}
	if forceCIDR || bits != defaultBits {
		return fmt.Sprintf("%s/%d", S, bits)
	}
	return S
}

func decodeCidr(B []byte) string { return decodeInet(B, true) }

func decodeMacaddr(B []byte) string {
	if len(B) < 6 {
		return ""
	}
	parts := make([]string, 6)
	for i := 0; i < 6; i++ {
		parts[i] = fmt.Sprintf("%02x", B[i])
	}
	return strings.Join(parts, ":")
}

func decodeMacaddr8(B []byte) string {
	if len(B) < 8 {
		return ""
	}
	parts := make([]string, 8)
	for i := 0; i < 8; i++ {
		parts[i] = fmt.Sprintf("%02x", B[i])
	}
	return strings.Join(parts, ":")
}

func bitsToStr(Payload []byte) string {
	if len(Payload) < 4 {
		return ""
	}
	nbits := int(int32(binary.LittleEndian.Uint32(Payload)))
	data := Payload[4:]
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
func parseMysqlBit(B []byte) (int, int, *big.Int, bool) {
	Payload, _, _ := VarPayload(B)
	if len(Payload) < 8 {
		return 0, 0, nil, false
	}
	a := binary.LittleEndian.Uint32(Payload)
	bb := binary.LittleEndian.Uint32(Payload[4:])
	n := int(a + bb)
	if n < 0 || n > 1<<20 {
		return 0, 0, nil, false
	}
	if n == 0 {
		return 0, 0, big.NewInt(0), true // 全 0 值（A=B=0）
	}
	nbytes := (n + 7) / 8
	if len(Payload) < 8+nbytes {
		nbytes = len(Payload) - 8
		if nbytes < 0 {
			return 0, 0, nil, false
		}
	}
	val := new(big.Int)
	for i := 0; i < nbytes; i++ {
		val.Lsh(val, 8)
		val.Or(val, big.NewInt(int64(Payload[8+i])))
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
func decodeMysqlBit(B []byte) string {
	n, _, val, ok := parseMysqlBit(B)
	if !ok {
		return DecodeDefault(B)
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

// BitBinToHex 将 N 位二进制串转为 mysql_bit_out 同款 hex 文本（0x 前缀大写）
// 规则：左补 0 到 8*ceil(N/8) 位，按字节转大写 hex
func BitBinToHex(bin string) string {
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

func decodeBit(B []byte) string {
	Payload, _, _ := VarPayload(B)
	return bitsToStr(Payload)
}

// decodeXml 解码 xml 类型（oid 142 / typinput=xml_in）
// 金仓 mysql 模式：varlena 纯文本（实测无类型标记）
// PG 标准：varlena 内容 = 4B 类型标记（bit0: 0=CONTENT,1=DOCUMENT，其余 reserved）+ 文本
func decodeXml(B []byte) string {
	Payload, _, _ := VarPayload(B)
	if len(Payload) >= 5 && Payload[0] <= 1 && Payload[1] == 0 && Payload[2] == 0 && Payload[3] == 0 {
		Payload = Payload[4:] // 跳过 PG 4B 类型标记
	}
	if utf8.Valid(Payload) {
		return string(Payload)
	}
	return "\\x" + hex.EncodeToString(Payload)
}

// v1.0.47：对齐 PG money_out（PG17 实测 lc_monetary=C 时同样输出 "$12,345.67"）：
// "$" 前缀 + 千分位逗号分组 + 2 位小数，负号在最前（"-$12,345.67"）。
func decodeMoney(B []byte) string {
	v := int64(binary.LittleEndian.Uint64(B))
	sign := ""
	if v < 0 {
		sign = "-"
		v = -v
	}
	S := strconv.FormatInt(v/100, 10)
	var groups []string
	for len(S) > 3 {
		groups = append([]string{S[len(S)-3:]}, groups...)
		S = S[:len(S)-3]
	}
	groups = append([]string{S}, groups...)
	return sign + "$" + strings.Join(groups, ",") + fmt.Sprintf(".%02d", v%100)
}

// ---------- 内置缺口类型：pg_lsn / txid_snapshot / reg* / range ----------

// decodePgLsn：pg_lsn（oid 3220，8B）。PG 内部 XLogRecPtr 统一大端序存储。
// 输出与 pg_lsn_out 一致：高 32 位/低 32 位大写十六进制，无 0x 前缀。
func decodePgLsn(B []byte) string {
	if len(B) < 8 {
		return DecodeDefault(B)
	}
	v := binary.LittleEndian.Uint64(B)
	return fmt.Sprintf("%X/%X", uint32(v>>32), uint32(v))
}

// decodeTxidSnapshot：txid_snapshot（oid 5030）varlena。
// 磁盘格式（PG txid.h TxidSnapshot）：4B xmin(LE) + 4B xmax(LE) + 4B nxip(LE) + nxip*8B xip(LE)。
// 输出与 txid_snapshot_out 一致：xmin:xmax: 或 xmin:xmax:xip1,xip2,...
func decodeTxidSnapshot(B []byte) string {
	Payload, _, _ := VarPayload(B)
	if os.Getenv("DBG_RANGE") != "" {
		fmt.Fprintf(os.Stderr, "[dbg] txid B=%x Payload=%x\n", B, Payload)
	}
	// PG 全版本（PG12-18/金仓）txid_snapshot/pg_snapshot 磁盘布局：
	// [nxip int32][xmin int64][xmax int64][xip int64...]（见 xid.c/xid8funcs.c recv 函数）
	if len(Payload) < 20 {
		return DecodeDefault(B)
	}
	nxip := binary.LittleEndian.Uint32(Payload)
	xmin := binary.LittleEndian.Uint64(Payload[4:])
	xmax := binary.LittleEndian.Uint64(Payload[12:])
	var sb strings.Builder
	sb.WriteString(strconv.FormatUint(xmin, 10))
	sb.WriteByte(':')
	sb.WriteString(strconv.FormatUint(xmax, 10))
	// PG txid_snapshot_out 固定输出 "xmin:xmax:"（尾冒号必须保留，nxip=0 时也是）
	sb.WriteByte(':')
	for i := 0; i < int(nxip) && 20+8*i+8 <= len(Payload); i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.FormatUint(binary.LittleEndian.Uint64(Payload[20+8*i:]), 10))
	}
	return sb.String()
}

// decodeRegOid：reg* 系列（regclass/regproc/regtype 等，int4 byval，磁盘为 4B oid 小端）。
// 输出 oid 数字（'数字'::regclass 按 oid 解析，可逆）。
func decodeRegOid(B []byte) string {
	if len(B) < 4 {
		return DecodeDefault(B)
	}
	oid := binary.LittleEndian.Uint32(B)
	if oid == 0 {
		return "-" // PG reg*out 对 InvalidOid 的输出约定
	}
	return strconv.FormatUint(uint64(oid), 10)
}

// regClassNames：pg_class oid → relname 反查表（initDynamicDecoders 时构建，v1.0.47）。
// regclass_out 输出关系名（如 "t_all"）而非裸 OID；跨库回灌时 OID 无意义、名字可移植。
var regClassNames map[uint32]string

// decodeRegclass：regclass（oid 2205）。对齐 PG regclass_out——输出关系名；
// 标识符合法（^[a-z_][a-z0-9_$]*$）时裸输出，否则双引号包裹（内部 " 翻倍）。
// InvalidOid(0) 输出 "-"（PG reg*out 约定）；pg_class 反查失败时回退裸 OID。
func decodeRegclass(B []byte) string {
	if len(B) < 4 {
		return DecodeDefault(B)
	}
	oid := binary.LittleEndian.Uint32(B)
	if oid == 0 {
		return "-"
	}
	if regClassNames != nil {
		if name, ok := regClassNames[oid]; ok && name != "" {
			return regQuoteIdent(name)
		}
	}
	return strconv.FormatUint(uint64(oid), 10)
}

func regQuoteIdent(S string) string {
	if regValidIdent(S) {
		return S
	}
	return `"` + strings.ReplaceAll(S, `"`, `""`) + `"`
}

func regValidIdent(S string) bool {
	if S == "" {
		return false
	}
	if c := S[0]; c != '_' && !(c >= 'a' && c <= 'z') {
		return false
	}
	for _, r := range S {
		if r != '_' && r != '$' && !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// rangeSub：range 子类型定义（wid=定长宽度；0=变长 varlena 内嵌，需 4B 长度前缀）
type rangeSub struct {
	wid int
	dec func([]byte) string
}

var rangeSubtypes = map[uint32]rangeSub{
	3904: {4, decodeInt4},        // int4range
	3926: {8, decodeInt8},        // int8range
	3906: {0, DecodeNumeric},     // numrange（numeric 为 varlena）
	3912: {4, DecodeDate},        // daterange
	3908: {8, decodeTimestamp},   // tsrange
	3910: {8, decodeTimestamptz}, // tstzrange
}

// decodeRange：PG 范围类型 varlena（PG rangetypes.h）。
// 磁盘：lower + upper + 1B flags（变长子类型带 4B varlena 长度前缀）。
// flags: 0x01 EMPTY 0x02 LB_INC 0x04 UB_INC 0x08 LB_INF 0x10 UB_INF 0x20 LB_NULL 0x40 UB_NULL。
// 输出与 range_out 一致：empty / [1,10) / (,10] / [1,) 等。
func decodeRange(B []byte, oid uint32) string {
	if os.Getenv("DBG_RANGE") != "" {
		fmt.Fprintf(os.Stderr, "[dbg] oid=%d B=%x len=%d\n", oid, B, len(B))
	}
	sub, ok := rangeSubtypes[oid]
	if !ok {
		return DecodeDefault(B)
	}
	Payload, _, _ := VarPayload(B)
	if len(Payload) < 5 {
		return DecodeDefault(B)
	}
	// PG range 磁盘：开头 4B 为 range 类型自身 oid（实测 3904/3926 等），随后为边界与 flags
	Payload = Payload[4:]
	flags := Payload[len(Payload)-1]
	if flags&0x01 != 0 {
		return "empty"
	}
	var lo, hi []byte
	pos := 0
	if flags&0x08 != 0 {
		lo = nil // lower unbounded
	} else if pos < len(Payload)-1 {
		if sub.wid > 0 {
			// v1.0.35：损坏数据（定长边界越过 flags 字节）时跳过该边界，避免 slice panic
			if pos+sub.wid <= len(Payload)-1 {
				lo = Payload[pos : pos+sub.wid]
				pos += sub.wid
			}
		} else {
			// 变长 subtype：varlena 字节（1B short / 4B 头），用 VarlenaParse 逐边界解析
			kind, total, _, _ := VarlenaParse(Payload, pos)
			if kind != "" && total > 0 && pos+total <= len(Payload)-1 {
				lo = Payload[pos : pos+total] // 含 varlena 头，DecodeNumeric 内部剥
				pos += total
			}
		}
	}
	if flags&0x10 != 0 {
		hi = nil // upper unbounded
	} else if pos < len(Payload)-1 {
		if sub.wid > 0 {
			if pos+sub.wid <= len(Payload)-1 {
				hi = Payload[pos : pos+sub.wid]
			}
		} else {
			kind, total, _, _ := VarlenaParse(Payload, pos)
			if kind != "" && total > 0 && pos+total <= len(Payload)-1 {
				hi = Payload[pos : pos+total]
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
		sb.WriteString(rangeQuote(sub.dec(lo)))
	}
	sb.WriteByte(',')
	if hi != nil {
		sb.WriteString(rangeQuote(sub.dec(hi)))
	}
	if flags&0x04 != 0 {
		sb.WriteByte(']')
	} else {
		sb.WriteByte(')')
	}
	return sb.String()
}

// rangeQuote：对齐 PG range_out 边界引号规则——空串或含 ()[]",\ 及空白字符的边界
// 必须加双引号（内部 " 与 \ 翻倍转义），如 ("2024-01-01 00:00:00","2024-06-01 12:00:00"]。
// v1.0.47 修复：旧实现从不加引号，tsrange/tstzrange 边界（含空格）输出与 PG 不一致。
func rangeQuote(S string) string {
	if S == "" {
		return `""`
	}
	need := strings.IndexAny(S, `()[]",\`) >= 0 || strings.IndexFunc(S, unicode.IsSpace) >= 0
	if !need {
		return S
	}
	var sb strings.Builder
	sb.WriteByte('"')
	for _, r := range S {
		if r == '"' || r == '\\' {
			sb.WriteByte('\\')
		}
		sb.WriteRune(r)
	}
	sb.WriteByte('"')
	return sb.String()
}

// ---------- 几何 ----------
func geomPt(B []byte, off int) string {
	x := math.Float64frombits(binary.LittleEndian.Uint64(B[off:]))
	y := math.Float64frombits(binary.LittleEndian.Uint64(B[off+8:]))
	return fmt.Sprintf("(%s,%s)", fmtFloat(x, 64), fmtFloat(y, 64))
}

func decodePoint(B []byte) string {
	if len(B) < 16 {
		return ""
	}
	return geomPt(B, 0)
}

func decodeLseg(B []byte) string {
	if len(B) < 32 {
		return ""
	}
	return fmt.Sprintf("[%s,%s]", geomPt(B, 0), geomPt(B, 16))
}

func decodeBox(B []byte) string {
	if len(B) < 32 {
		return ""
	}
	return fmt.Sprintf("%s,%s", geomPt(B, 0), geomPt(B, 16))
}

func decodePath(B []byte) string {
	Payload, _, _ := VarPayload(B)
	if len(Payload) < 12 {
		return ""
	}
	npts := int(int32(binary.LittleEndian.Uint32(Payload)))
	closed := int32(binary.LittleEndian.Uint32(Payload[4:]))
	if npts <= 0 || len(Payload) < 12+16*npts {
		return ""
	}
	pts := make([]string, npts)
	for i := 0; i < npts; i++ {
		pts[i] = geomPt(Payload, 12+16*i)
	}
	if closed != 0 {
		return "(" + strings.Join(pts, ",") + ")"
	}
	return "[" + strings.Join(pts, ",") + "]"
}

func decodePolygon(B []byte) string {
	Payload, _, _ := VarPayload(B)
	if len(Payload) < 36 {
		return ""
	}
	npts := int(int32(binary.LittleEndian.Uint32(Payload)))
	if npts <= 0 || len(Payload) < 36+16*npts {
		return ""
	}
	pts := make([]string, npts)
	for i := 0; i < npts; i++ {
		pts[i] = geomPt(Payload, 36+16*i)
	}
	return "(" + strings.Join(pts, ",") + ")"
}

func decodeLine(B []byte) string {
	if len(B) < 24 {
		return ""
	}
	a := math.Float64frombits(binary.LittleEndian.Uint64(B))
	bb := math.Float64frombits(binary.LittleEndian.Uint64(B[8:]))
	c := math.Float64frombits(binary.LittleEndian.Uint64(B[16:]))
	return fmt.Sprintf("{%s,%s,%s}", fmtFloat(a, 64), fmtFloat(bb, 64), fmtFloat(c, 64))
}

func decodeCircle(B []byte) string {
	if len(B) < 24 {
		return ""
	}
	x := math.Float64frombits(binary.LittleEndian.Uint64(B))
	y := math.Float64frombits(binary.LittleEndian.Uint64(B[8:]))
	r := math.Float64frombits(binary.LittleEndian.Uint64(B[16:]))
	return fmt.Sprintf("<(%s,%s),%s>", fmtFloat(x, 64), fmtFloat(y, 64), fmtFloat(r, 64))
}

// ---------- 其他 ----------
func decodeChar(B []byte) string {
	if len(B) == 0 {
		return ""
	}
	return string(rune(B[0]))
}

func decodeUnknown(B []byte) string { return charset.DecodeBytes(B) }

func decodePgNodeTree(B []byte) string { return DecodeVarlenaText(B) }

func decodeOidvector(B []byte) string {
	n := len(B) / 4
	parts := make([]string, n)
	for i := 0; i < n; i++ {
		parts[i] = strconv.FormatUint(uint64(binary.LittleEndian.Uint32(B[i*4:])), 10)
	}
	return strings.Join(parts, " ")
}

func decodeInt2vector(B []byte) string {
	n := len(B) / 2
	parts := make([]string, n)
	for i := 0; i < n; i++ {
		parts[i] = strconv.Itoa(int(int16(binary.LittleEndian.Uint16(B[i*2:]))))
	}
	return strings.Join(parts, " ")
}

func DecodeDefault(B []byte) string {
	for _, c := range B {
		if c == 0 {
			return "\\x" + hex.EncodeToString(B)
		}
	}
	if utf8.Valid(B) {
		return string(B)
	}
	return "\\x" + hex.EncodeToString(B)
}

func decodeAclitem(B []byte) string {
	if len(B) < 12 {
		return "oid:0"
	}
	grantor := binary.LittleEndian.Uint32(B)
	grantee := binary.LittleEndian.Uint32(B[4:])
	privs := binary.LittleEndian.Uint32(B[8:])
	// B7 修复：RoleMap 改为 atomic 快照读（无锁，避免 --jobs 并行 DATA RACE）
	rm := roleMapSnapshot()
	e := ""
	if grantee != 0 {
		if rm != nil {
			if name, ok := rm[grantee]; ok {
				e = name
			} else {
				e = fmt.Sprintf("oid:%d", grantee)
			}
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
	if rm != nil {
		if name, ok := rm[grantor]; ok {
			g = name
		} else {
			g = fmt.Sprintf("oid:%d", grantor)
		}
	} else {
		g = fmt.Sprintf("oid:%d", grantor)
	}
	return fmt.Sprintf("%s=%s/%s", e, chars.String(), g)
}

// ---------- 分发 ----------
// 方案 C：目录动态发现的解码器映射（v1.0.16）
// 硬编码 Decoders 表优先；动态映射覆盖"未知 oid"（金仓实例相关 oid、未来版本新类型等）。
var DynamicDecoders map[uint32]func([]byte) string

// CompositeAttrs：复合类型 typrelid → 字段行（按 attnum 升序，不含 dropped）
var CompositeAttrs map[uint32][]*AttrRow

// RecordQuote：PG record_out 的字段转义（对齐 rowtypes.c 判据：
// 空串、含 , ( ) " \ 或任意空白（含 tab/换行）→ 双引号包裹）
// v1.0.56 修复（P2-G5）：旧版判据缺空串/反斜杠/内部空白三类触发，
// 空串导出为裸空段被 record_in 解析为 NULL（静默失真）、`a\B` 回灌被
// 当转义对解析成 `ab`、含 tab/换行的字段解析错位。同文件 arrayQuote
// 三项均已处理，此处为漏改。
func RecordQuote(S string) string {
	needs := S == "" || strings.ContainsAny(S, ",()\"\\") ||
		strings.ContainsFunc(S, func(r rune) bool {
			return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f'
		})
	if !needs {
		return S
	}
	S = strings.ReplaceAll(S, "\\", "\\\\")
	S = strings.ReplaceAll(S, "\"", "\\\"")
	return "\"" + S + "\""
}

// DecodeComposite：解析 record varlena（DatumTupleFields + 行数据）为 (f1,f2,...)
// 字段类型/对齐来自 pg_attribute，递归调用 DecodeValue 解码各字段
//
// v1.0.48 修复（P1-F1）：复合 datum 的头布局与普通堆元组【不同】——
// 其 t_choice 位置放的是 DatumTupleFields{datum_len_, datum_typmod, datum_typeid}，
// 而 datum_len_ 本身就是 varlena 头。因此相对"剥掉 varlena 头之后的 body"：
//
//	t_infomask2 → body[14]（普通元组是 18）
//	t_infomask  → body[16]（普通元组是 20）
//	t_hoff      → body[18]（普通元组是 22）
//
// 且 t_hoff 恒按 4 字节 datum_len_ 计算，与实际的 1 字节短头无关，
// 故数据区起点 = t_hoff - 4（1 字节头/4 字节头两种形式都成立）。
//
// 旧实现直接 parseTuple(Payload) 按普通元组读 body[22] 当 t_hoff，
// 实际读到的是行数据正文首字节（如 'e'=0x65=101），远超 Payload 长度，
// 于是整列退化成 \x 十六进制（PG17 实机：contact 列、contact[] 元素全部命中）。
func DecodeComposite(oid uint32, B []byte) string {
	if DynamicTypeDefs == nil {
		return DecodeDefault(B)
	}
	td := DynamicTypeDefs[oid]
	if td == nil || td.Type != 'c' || td.TypRelid == 0 {
		return DecodeDefault(B)
	}
	attrs := CompositeAttrs[td.TypRelid]
	if len(attrs) == 0 || len(B) < 4 {
		return DecodeDefault(B)
	}
	hdr := 4
	if B[0]&0x01 != 0 { // 1 字节短头（小复合值被压缩成短格式）
		hdr = 1
	}
	if hdr >= len(B) {
		return DecodeDefault(B)
	}
	body := B[hdr:]
	if len(body) < 19 {
		return DecodeDefault(B)
	}
	natts := int(U16(body, 14) & HEAP_NATTS_MASK)
	infomask := U16(body, 16)
	dataOff := int(body[18]) - 4
	if dataOff < 19 || dataOff > len(body) {
		return DecodeDefault(B)
	}
	var nulls []bool
	if infomask&HEAP_HASNULL != 0 {
		bmLen := (natts + 7) / 8
		if bmLen <= 0 || 19+bmLen > len(body) {
			return DecodeDefault(B)
		}
		nulls = make([]bool, natts)
		for i := 0; i < natts; i++ {
			nulls[i] = body[19+i/8]&(1<<(i%8)) == 0
		}
	}
	if natts > 0 && natts < len(attrs) {
		attrs = attrs[:natts]
	}
	cols := make([]ColLen, len(attrs))
	for i, a := range attrs {
		cols[i] = ColLen{AttLen: a.AttLen, IsVarlena: a.AttLen == -1, AttAlign: a.AttAlign}
	}
	fields := ExtractFieldsDirect(body, dataOff, nulls, cols)
	var sb strings.Builder
	sb.WriteByte('(')
	for i, f := range fields {
		if i > 0 {
			sb.WriteByte(',')
		}
		if f == nil {
			// v1.0.48：与 record_out 对齐——NULL 字段输出空（(,)），不是字面量 NULL。
			// 复合字面量里不带引号的 NULL 会被当成字符串 "NULL"，回灌即错。
			continue
		}
		sb.WriteString(RecordQuote(DecodeValue(attrs[i].AttTypID, *f)))
	}
	sb.WriteByte(')')
	return sb.String()
}

var DynamicTypeDefs map[uint32]*TypeDef

// MysqlBitOIDs：typinput=mysql_bit_in 的类型 oid 集合（CSV/SQL 输出层特判用）
var MysqlBitOIDs = map[uint32]bool{4655: true}

// MysqlBinOIDs：金仓 mysql 模式 binary/varbinary（oid 3383/3384）。
// 这两个类型 typinput=binaryin，无 hex 文本输入格式（既不吃 \x… 也不吃 0x…，
// 一律按字面量字节存入），SQL 输出必须补 ::bytea 才能还原字节（见 heapfile.sqlQuote）。
var MysqlBinOIDs = map[uint32]bool{3383: true, 3384: true}

// inputFnDecoders：typinput 函数名 → 解码器族（方案 C 语义映射核心）
var inputFnDecoders = map[string]func([]byte) string{
	"date_in":        DecodeDate,
	"time_in":        decodeTime,
	"timetz_in":      decodeTimetz,
	"timestamp_in":   decodeTimestamp,
	"timestamptz_in": decodeTimestamptz,
	"interval_in":    decodeInterval,
	"json_in":        decodeJSON,
	"jsonb_in":       decodeJSONB,
	"textin":         decodeText,
	"varcharin":      decodeVarchar,
	"bpcharin":       decodeBpchar,
	"namein":         decodeName,
	"int2in":         decodeInt2,
	"tinyintin":      decodeInt1, // 金仓 V9 mysql 模式 TINYINT（1 字节有符号整数）
	"int4in":         decodeInt4,
	"int8in":         decodeInt8,
	"oidin":          decodeOid,
	"float4in":       decodeFloat4,
	"float8in":       decodeFloat8,
	"numeric_in":     DecodeNumeric,
	"boolin":         decodeBool,
	"uuid_in":        decodeUUID,
	"cash_in":        decodeMoney, // 金仓 V8 money 实测 typinput=cash_in
	"money_in":       decodeMoney,
	"byteain":        decodeBytea,
	"charin":         decodeChar,
	"binaryin":       decodeBinary, // 金仓 V9 mysql 模式 BINARY（变长字符串 + \x00 填充）
	"unknownin":      decodeUnknown,
	"inet_in":        func(B []byte) string { return decodeInet(B, false) },
	"cidr_in":        decodeCidr,
	"macaddr_in":     decodeMacaddr,
	"macaddr8_in":    decodeMacaddr8,
	"bit_in":         decodeBit,
	"varbit_in":      decodeBit,
	"xml_in":         decodeXml,      // PG 标准 xml + 金仓（纯文本 varlena）
	"mysql_bit_in":   decodeMysqlBit, // 金仓 V9 mysql 模式 BIT(4655)
	"tsvectorin":     decodeTsvector,
	// 金仓 mysql/oracle 模式
	"mysql_date_in":      DecodeDate,
	"mysql_datetime_in":  decodeTimestamp,
	"mysql_timestamp_in": DecodeMysqlTimestamp,
	"mysql_time_in":      decodeMysqlTime,
	"ora_date_in":        decodeTimestamp,
	"datetime_in":        decodeTimestamp,
}

// resolveDecoder 递归解析 oid 对应的解码器：
// 硬编码表 → 已动态映射 → base 类型按 typinput 函数名 → domain 沿 typbasetype 递归。
func resolveDecoder(oid uint32, defs map[uint32]*TypeDef, procs map[uint32]string, dd map[uint32]func([]byte) string) func([]byte) string {
	if dec, ok := Decoders[oid]; ok {
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

// InitDynamicDecodersOnce：initDynamicDecoders 的幂等缓存版（v1.0.38 --jobs 表间并行用）。
// 同一数据目录+版本只初始化一次，避免 N 表并行时重复 IO/重建；写全局状态持 CatalogMu。
func InitDynamicDecodersOnce(dbDir string, version int, isKB bool) int {
	CatalogMu.Lock()
	defer CatalogMu.Unlock()
	key := fmt.Sprintf("%s|%d|%v", dbDir, version, isKB)
	if dynCacheKey == key {
		return dynCacheN
	}
	// B9 修复：解析数据目录 conf timezone（mysql 模式 timestamp 解码偏移；
	// --tz 显式指定优先，见 main.go parseTZOffset/applyConfTimezone）
	if !MysqlTZSet {
		if off, ok := util.ParseConfTimezone(dbDir); ok {
			MysqlTZOffsetUS = off
			MysqlTZSet = true
		}
	}
	n := initDynamicDecoders(dbDir, version, isKB)
	dynCacheKey = key
	dynCacheN = n
	return n
}

var (
	dynCacheKey string
	dynCacheN   int
)

// DynamicScan 目录扫描结果：动态类型解码所需的全部目录信息。
// datatype 自身不做目录 IO（避免与 engine/catalog 形成循环依赖），
// 由上层在启动时把 catalog.ScanDynamic 注入 DynamicScanner。
type DynamicScan struct {
	KbOracleMode   bool
	RegClassNames  map[uint32]string
	TypeDefs       map[uint32]*TypeDef
	CompositeAttrs map[uint32][]*AttrRow
	ProcNames      map[uint32]string
}

// DynamicScanner 目录扫描回调（app 启动时注册 catalog.ScanDynamic）。
// 为 nil 时退化为「仅硬编码解码表」。
var DynamicScanner func(dbDir string, version int, isKB bool) *DynamicScan

// PostScanHook 目录扫描之外的补充初始化回调（app 注册：域 CHECK 约束加载等）。
var PostScanHook func(dbDir string, version int, isKB bool)

// initDynamicDecoders 构建动态解码器映射（目录信息由 DynamicScanner 注入）。
// 返回动态映射数量（-1 表示目录缺失，保持仅硬编码表）。
func initDynamicDecoders(dbDir string, version int, isKB bool) int {
	// v1.0.50：域 CHECK 约束（pg_constraint conbin 节点树反解）
	if PostScanHook != nil {
		PostScanHook(dbDir, version, isKB)
	}
	procs := map[uint32]string{}
	if DynamicScanner != nil {
		if scan := DynamicScanner(dbDir, version, isKB); scan != nil {
			// v1.0.47：oracle 兼容模式探测（timestamp 微秒 6 位）与 regclass 反查表。
			kbOracleMode = isKB && scan.KbOracleMode
			regClassNames = scan.RegClassNames
			DynamicTypeDefs = scan.TypeDefs
			CompositeAttrs = scan.CompositeAttrs
			if scan.ProcNames != nil {
				procs = scan.ProcNames
			}
		}
	}
	if len(DynamicTypeDefs) == 0 {
		DynamicDecoders = nil
		return -1
	}
	dd := map[uint32]func([]byte) string{}
	// 收集 mysql_bit_in 类型 oid（CSV/SQL 输出特判）
	for oid, td := range DynamicTypeDefs {
		if fn := procs[td.Input]; fn == "mysql_bit_in" {
			MysqlBitOIDs[oid] = true
		}
	}
	// 1) base 类型：typinput 函数名映射
	for oid, td := range DynamicTypeDefs {
		if _, ok := Decoders[oid]; ok {
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
	for oid, td := range DynamicTypeDefs {
		if _, ok := Decoders[oid]; ok {
			continue
		}
		if _, ok := dd[oid]; ok {
			continue
		}
		if td.Type == 'd' && td.Base != 0 {
			if dec := resolveDecoder(td.Base, DynamicTypeDefs, procs, dd); dec != nil {
				dd[oid] = dec
			}
		}
	}
	// 3) 数组：元素类型的 typarray 反向指回本 oid
	// v1.0.48 修复（P1-F1）：原判据 `td.Array == oid` 永远不成立——pg_type 里
	// 数组类型自身的 typarray 恒为 0，只有元素类型的 typarray 指向数组类型。
	// 该分支此前是死代码，自定义数组（enum[]/复合[]/域[]）全部没有解码器。
	for oid := range DynamicTypeDefs {
		if _, ok := Decoders[oid]; ok {
			continue
		}
		if _, ok := dd[oid]; ok {
			continue
		}
		if _, ok := UserArrayElemOID(oid); ok {
			dd[oid] = DecodeArray
		}
	}
	DynamicDecoders = dd
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

func DecodeValue(oid uint32, raw []byte) string {
	if raw == nil {
		return "NULL"
	}
	// 枚举类型（B7 修复：EnumMap 改 atomic 快照读）
	if em, ok := EnumMapSnapshot()[oid]; ok && len(raw) >= 4 {
		memberOID := binary.LittleEndian.Uint32(raw)
		for _, m := range em {
			if m.MemberOID == memberOID {
				return m.Label
			}
		}
		return strconv.FormatUint(uint64(memberOID), 10)
	}
	dec, ok := Decoders[oid]
	if !ok {
		if DynamicTypeDefs != nil {
			if td, ok2 := DynamicTypeDefs[oid]; ok2 && td.Type == 'c' {
				return DecodeComposite(oid, raw)
			}
		}
		if DynamicDecoders != nil {
			if dec2, ok2 := DynamicDecoders[oid]; ok2 {
				return dec2(raw)
			}
		}
		return DecodeDefault(raw)
	}
	return dec(raw)
}

var Decoders = map[uint32]func([]byte) string{
	BOOLOID:        decodeBool,
	INT2OID:        decodeInt2,
	INT4OID:        decodeInt4,
	INT8OID:        decodeInt8,
	4659:           DecodeInt16KB,
	FLOAT4OID:      decodeFloat4,
	FLOAT8OID:      decodeFloat8,
	TEXTOID:        decodeText,
	8014:           decodeText,     // 金仓 clob（textin/textout，标准 varlena 存储）
	4655:           decodeMysqlBit, // 金仓 V9 mysql 模式 BIT（typinput=mysql_bit_in，实测）
	XMLOID:         decodeXml,      // PG 标准 xml（142）
	NAMEOID:        decodeName,
	BPCHAROID:      decodeBpchar,
	VARCHAROID:     decodeVarchar,
	BYTEAOID:       decodeBytea,
	OIDOID:         decodeOid,
	XIDOID:         decodeXid,
	CIDOID:         decodeCid,
	TIDOID:         decodeTid,
	DATEOID:        DecodeDate,
	7944:           DecodeDate,           // 金仓 V9 date（4 字节天数，与 PG date 同格式）
	8020:           decodeTimestamp,      // 金仓 oracle DATE（8 字节 timestamp 格式）
	7952:           decodeTimestamp,      // 金仓 V9 datetime 基础类型（8 字节 timestamp 格式）
	7954:           DecodeMysqlTimestamp, // 金仓 V9 mysql 模式 timestamp（UTC 秒 + 时区转换）
	7950:           decodeMysqlTime,      // 金仓 V9 mysql 模式 time（支持 24:00:00，不做取模）
	7024:           decodeJSONB,          // 金仓 V9 mysql 模式 json（domain of 4802 mysql_json，jsonb 二进制）
	4189:           decodeTimestamp,      // 金仓 datetime（domain of timestamp 1114）
	12636:          decodeTimestamp,      // 金仓 ora_date（domain of 8020，8 字节 timestamp 格式）
	TIMEOID:        decodeTime,
	TIMESTAMPOID:   decodeTimestamp,
	TIMESTAMPTZOID: decodeTimestamptz,
	TIMETZOID:      decodeTimetz,
	INTERVALOID:    decodeInterval,
	NUMERICOID:     DecodeNumeric,
	UUIDOID:        decodeUUID,
	JSONOID:        decodeJSON,
	JSONBOID:       decodeJSONB,
	INETOID:        func(B []byte) string { return decodeInet(B, false) },
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
	3220: decodePgLsn,                                           // pg_lsn
	2970: decodeTxidSnapshot,                                    // txid_snapshot
	3615: decodeTsquery,                                         // tsquery
	2205: decodeRegclass,                                        // regclass（v1.0.47：输出关系名，pg_class 反查）
	24:   decodeRegOid,                                          // regproc
	2206: decodeRegOid,                                          // regprocedure
	2207: decodeRegOid,                                          // regoper
	2208: decodeRegOid,                                          // regoperator
	2209: decodeRegOid,                                          // regnamespace
	2210: decodeRegOid,                                          // regtype
	4096: decodeRegOid,                                          // regrole
	4089: decodeRegOid,                                          // regconfig
	4090: decodeRegOid,                                          // regdictionary
	3904: func(B []byte) string { return decodeRange(B, 3904) }, // int4range
	3926: func(B []byte) string { return decodeRange(B, 3926) }, // int8range
	3906: func(B []byte) string { return decodeRange(B, 3906) }, // numrange
	3912: func(B []byte) string { return decodeRange(B, 3912) }, // daterange
	3908: func(B []byte) string { return decodeRange(B, 3908) }, // tsrange
	3910: func(B []byte) string { return decodeRange(B, 3910) }, // tstzrange
	// ---- 金仓专有类型（v1.0.47）----
	8013: decodeByteaEscape, // 金仓 blob（byteain，输出 escape 格式）
	3383: decodeKBBinary,    // 金仓 mysql/oracle binary（输出 0x+大写 hex）
	3384: decodeKBBinary,    // 金仓 mysql/oracle varbinary
}

func init() {
	for oid := range arrayTypeOIDs {
		Decoders[oid] = DecodeArray
	}
}

// ---------- SQL 字面量 ----------
func SQLStringLiteral(v string) string {
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
