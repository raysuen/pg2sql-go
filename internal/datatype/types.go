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
	"pg2sql/internal/charset"
	"pg2sql/internal/logx"
	"pg2sql/internal/util"
	"sort"
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
	CHARARRAYOID   = 1002 // pg_type "char"[]（元素 oid 18）
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

// CharTypeSQL：oid 18/1002 在 DDL 里的正确写法（必须带双引号）。
// v1.2.7 修复（F1）：pg_type.typname 是 `char`，但 SQL 里的裸 `char` 是
// character(1) 的别名（oid 1042）。DDL 少写引号会静默降级：
//   - 表能建、导入不报错、行数正确，但列已从「单字节」变「单字符」；
//   - 高位字节（如 0xC3）落库后被当 Latin-1 字符二次编码，每轮迁移扩一层，
//     数据永久不可还原；
//   - 0x00 完全无法表达；`c::int` 等单字节语义的表达式直接报错。
//
// 因此按 oid 特判，不能按类型名判断（1042 的 character(n) 必须保持裸名）。
// 返回 ok=false 表示不是该类型。
func CharTypeSQL(oid uint32) (string, bool) {
	switch oid {
	case CHAROID:
		return `"char"`, true
	case CHARARRAYOID:
		return `"char"[]`, true
	}
	return "", false
}

// B12（v1.0.66，实机发现）：金仓在 ACL 位 12 使用自有权限字符 'p'
// （V8R6C9B14 / V9R1C2B14 / V9R1C10 三版本实机 aclitemout 一致：owner 默认
// ACL = arwdDxtp，对应磁盘位图 bits0-6+bit12），而 PG16+ 同一位是 ACL_SET
// 小写 's'。aclitem 权限字符按引擎选表：金仓位 12 输出 'p'（否则导出 's'
// 回灌金仓 aclitem_in 可能拒绝），PG 系仍用官方 ACL_ALL_RIGHTS_STR。
// KbEngine 由目录扫描（initDynamicDecoders）按 isKB 注入；未扫描时按 PG 表。
var KbEngine atomic.Bool

// kbAclChars：金仓 ACL 权限字符表——位 12 = 'p'（金仓自有权限），其余位序
// 与 PG 一致（金仓系 PG12 内核 fork；bit13+ 金仓未定义，置位时不输出即可）。
const kbAclChars = "arwdDxtXUCTcpAm"

// ACL 权限字符（与 PG acl.h ACL_ALL_RIGHTS_STR 一致："arwdDxtXUCTcsAm"）。
// 位 12 = ACL_SET 小写 's'（PG9.5 引入 GRANT SET ON PARAMETER，各版本均为小写）；
// 位 13 = ACL_ALTER_SYSTEM 大写 'A'（PG15+）。v1.0.65 曾误把位 12 改成大写 'S'
// （GRANT SET 导出后回灌 aclitem_in 报 "invalid mode character"），本轮纠正。
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
			// v1.0.65 修复（F5）：内联压缩数据解压失败此前完全静默——列被当空串
			// 导出（既非 NULL 也非 __TOAST_CORRUPT__，与引擎层 TOAST 路径口径
			// 不一致）。数据流保持不变（占位符经 VarPayload 无法区分语义，
			// 且会以字面量泄漏进数组/复合元素），但至少留下日志线索。
			logx.Logf("警告: 内联压缩 varlena 解压失败（压缩后 %d 字节，期望原始 %d 字节），该值导出为空串", len(comp)-4, rawlen)
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
	// v1.0.63 修复（B-9）：空 tsquery 的 payload 只有 4 字节（size=0），旧实现
	// 先撞上 len<8 守卫退化成 \x 十六进制，psql 回灌时 E'\\x2000…' 会被
	// tsquery_in 当成一个词元（静默变成"匹配 x2000000000000000"的错误查询）。
	// size 判定必须放在长度守卫之前。
	if len(Payload) >= 4 {
		if sz := int(int32(binary.LittleEndian.Uint32(Payload))); sz == 0 {
			return ""
		}
	}
	// v1.0.63（C-1）：删除生产代码里的 DBG_TSQ 调试钩子（向 stderr 打印原始字节）。
	if len(Payload) < 8 {
		return DecodeDefault(B)
	}
	size := int(int32(binary.LittleEndian.Uint32(Payload)))
	if size <= 0 {
		return DecodeDefault(B) // 负 size = 损坏数据
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

// decodeInt1：1 字节有符号整数。
// 不直接注册在 Decoders 主表——"char"(18) 走 decodeChar；
// 本函数经 inputFnDecoders["tinyintin"] 服务于金仓 V9 mysql 模式 TINYINT
// （initDynamicDecoders 按 typinput 函数名动态分派，v1.2.4 扫描时曾被误判为死代码）。
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

// v1.2.13 修复（P1）：PG 的 ItemPointerData = {BlockIdData{bi_hi, bi_lo}, OffsetNumber}，
// 两个 uint16 按字段声明顺序落盘（bi_hi 在低地址），块号语义是 bi_hi<<16 | bi_lo。
// 旧实现按 uint32 小端直读，等价于 bi_hi | (bi_lo<<16)——两个半字被整体互换：
// (1,2) 导出成 (65536,2)、(65536,2) 导出成 (1,2)，回灌不报错但指针错位（静默数据损坏）。
func decodeTid(B []byte) string {
	blk := uint32(binary.LittleEndian.Uint16(B))<<16 | uint32(binary.LittleEndian.Uint16(B[2:]))
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
	// v1.0.63 修复（A-5）：PG 的 time 合法上界是 24:00:00（内部值 86400*1e6），
	// 旧实现无条件 `us %= 86400*1e6` 把它归零成 00:00:00 —— 语义上整整差一天，
	// 且 timetz 走同一条路径（'24:00:00+08' → '00:00:00+08'）。
	// 只在真正越界（> 24h 或负值）时回绕，与 decodeMysqlTime 的口径保持一致。
	if us < 0 || us > 86400*1000000 {
		us %= 86400 * 1000000
		if us < 0 {
			us += 86400 * 1000000
		}
	}
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
	// v1.2.16 修复（回归报告 D7）：金仓 mysql 模式的 timestamptz_out 按实例时区
	// 输出本地时间且**不带** "+00"（实测 V9R1C10：'2024-01-01 09:00:00+08' 存 01:00Z，
	// COPY 输出 "2024-01-01 09:00:00"）。PG / oracle 模式维持带 +00 的绝对形态——
	// 那是跨时区回灌唯一无歧义的写法，不可一并改掉。
	if kbMysqlMode {
		return TimestampFromUS(mysqlLocalUS(us))
	}
	return TimestampFromUS(us) + "+00"
}

// mysqlLocalUS：UTC 微秒 → 金仓 mysql 模式会话时区下的本地微秒。
// 复用 mysql timestamp 的时区来源（--tz > 数据目录 conf timezone > 默认 +8 并告警），
// 与 DecodeMysqlTimestamp 保持同一套规则，避免同实例内两种时间类型口径不一致。
func mysqlLocalUS(us int64) int64 {
	if !MysqlTZSet {
		mysqlTZWarned.Do(func() {
			logx.Logf("警告: 未能确定金仓 mysql 实例时区，timestamptz 按 UTC+8 渲染（可用 --tz 指定，如 --tz +09:00）")
		})
	}
	if MysqlTZLoc != nil {
		return us + mysqlTZOffsetAtUS(us)
	}
	return us + MysqlTZOffsetUS
}

// MysqlTZOffsetUS：金仓 mysql 模式 timestamp 解码偏移（微秒）。
// B9 修复：由 --tz 参数或数据目录 conf 的 timezone 推导（main.go parseTZOffset/parseConfTimezone），
// 未指定且推导失败时保持 +8（向后兼容）并告警一次。
var (
	MysqlTZOffsetUS int64 = 8 * 3600 * 1000000
	MysqlTZSet      bool
	mysqlTZWarned   sync.Once
	// MysqlTZLoc：金仓 mysql/ora 模式 timestamp 解码所用的完整时区规则（IANA）。
	// v1.1.1 修复（B17）：固定偏移无法表达历史偏移——金仓按「会话时区的当时规则」把本地
	// 时间转 UTC 存储，1901 年前为 LMT（Asia/Shanghai=+08:05:43）、1986-1991 中国夏令时为 +09:00，
	// 用固定 +08:00 解码会静默偏移（实测 1988-07-01 12:00 → 11:00，1900-01-01 12:00 → 11:54:17）。
	// 非 nil 时按每个值所在时刻查表取偏移，优先于 MysqlTZOffsetUS。
	MysqlTZLoc *time.Location
)

// SetMysqlTZByName 按 IANA 时区名设置解码规则；名字非 IANA（如 +08:00/UTC 形式）时退回固定偏移。
// 返回是否成功设置（固定偏移路径解析失败返回 false）。
func SetMysqlTZByName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	if loc, err := time.LoadLocation(name); err == nil {
		MysqlTZLoc = loc
		MysqlTZSet = true
		return true
	}
	if off, ok := util.ParseTZOffset(name); ok {
		MysqlTZLoc = nil
		MysqlTZOffsetUS = off
		MysqlTZSet = true
		return true
	}
	return false
}

// mysqlTZOffsetAtUS 返回相对 2000-01-01 00:00:00 UTC 的 us 时刻、在 MysqlTZLoc 下的偏移（微秒）。
// 用 time.Unix（int64 秒）而非 time.Duration 构造，避免 ±292 年溢出（BC 时间戳需要）。
func mysqlTZOffsetAtUS(us int64) int64 {
	sec := us / 1000000
	// 银仓 mysql 模式微秒基准 2000-01-01 00:00:00 UTC（unix 946684800）
	t := time.Unix(946684800+sec, 0).In(MysqlTZLoc)
	_, off := t.Zone()
	return int64(off) * 1000000
}

// MySQLLocalTZSQLValue：把"解码时用的时区"转成可直接用于 `SET TIME ZONE` 的字面量。
//
// v1.2.18 修复（P2）：mysql 模式的 timestamptz / 分区边界字面量输出**本地时间且不带
// 时区后缀**（v1.2.16 D7，为与 mysql 服务端 SELECT 文本一致）。但导出脚本头部只有
// `SET client_encoding`，没有 `SET TIME ZONE` —— 回灌时这些无后缀字面量按**目标会话**
// 的 TimeZone 解释。源库 Asia/Shanghai(+08)、目标默认 UTC 时整体平移 8 小时，
// INSERT 不报错；分区边界字面量同理会让分区落错且不报错。
//
// 返回值语义：
//   - IANA 名可用（MysqlTZLoc != nil）：返回 'Area/City' 形式，交给服务端解析。
//     **必须是源库那个名字**，不能换成等价的固定偏移：目标端若有历史夏令时规则差异，
//     只有名字能表达"按当时规则解释"。
//   - 固定偏移：返回 INTERVAL '+08:00' HOUR TO MINUTE 形式（SET TIME ZONE 接受
//     INTERVAL，且不依赖目标端是否存在该 IANA 名）。
//
// 仅在 mysql 兼容模式（kbMysqlMode）下有意义；调用方自行按模式门控。
func MySQLLocalTZSQLValue() string {
	if MysqlTZLoc != nil {
		// IANA 名只含 [A-Za-z0-9_+-/]，不含单引号，直接单引号包裹即可；
		// 仍防御性替换一次（时区名理论上不可能含 '，但别留隐患）。
		return "'" + strings.ReplaceAll(MysqlTZLoc.String(), "'", "''") + "'"
	}
	us := MysqlTZOffsetUS
	sign := "+"
	if us < 0 {
		sign = "-"
		us = -us
	}
	total := us / 1000000
	hh := total / 3600
	mm := (total % 3600) / 60
	return fmt.Sprintf("INTERVAL '%s%02d:%02d' HOUR TO MINUTE", sign, hh, mm)
}

// MySQLLocalTZEnvValue：与 MySQLLocalTZSQLValue 同源，但输出 **PGTZ 环境变量**的取值
// （不带 SQL 引号、不带 INTERVAL 关键字）。
//
// v1.2.18 补漏（P2）：`--import-script` 生成的是 shell 脚本，`\copy` 走的是客户端，
// 没有 SQL 会话头可以挂 `SET TIME ZONE`。只能靠 PGTZ 环境变量把会话时区钉住。
// 之前脚本只导出了 PGCLIENTENCODING，mysql 模式下 timestamptz 字面量（本地时间、
// 不带后缀）回灌时按目标默认时区解释：+08 → UTC 即整体平移 8 小时，且不报错。
//
// 取值形态：
//   - IANA 名：原样返回 "Asia/Shanghai"
//   - 固定偏移：返回 "+09:00" / "-05:30" —— libpq 的 PGTZ 接受这种 `+HH:MM` 形式
//     （注意它要求**无空格**，与 SET TIME ZONE 的 INTERVAL 写法不同）。
//
// 返回值与渲染口径**严格同源**：MysqlTZLoc != nil 时返回 IANA 名；否则返回
// MysqlTZOffsetUS 的 +HH:MM 形式（含 MysqlTZSet=false 的默认回退 +08:00）。
// 为什么不在这里"宁缺毋滥"返回空串：mysqlLocalUS 渲染 timestamptz 时对
// !MysqlTZSet 已经按默认 +8 落了字面量（并告警），SQL 会话头同样照写 +08——
// 导出内容已经"承诺"了 +08，脚本再不钉，目标端非 +08 时 \copy 照样静默平移。
// 三条出口（字面量 / SET TIME ZONE / PGTZ）必须钉同一个值。
// v1.2.18 R3 修正：原实现在 !MysqlTZSet 时返回空串，与上述两条出口不一致。
func MySQLLocalTZEnvValue() string {
	if MysqlTZLoc != nil {
		return MysqlTZLoc.String()
	}
	us := MysqlTZOffsetUS
	sign := "+"
	if us < 0 {
		sign = "-"
		us = -us
	}
	total := us / 1000000
	hh := total / 3600
	mm := (total % 3600) / 60
	return fmt.Sprintf("%s%02d:%02d", sign, hh, mm)
}

// kbOracleMode：金仓 oracle 兼容模式（离线探测：kingbase.conf 含 ora_* GUC）。
// oracle 模式 timestamp_out 微秒恒定 6 位（".789000"），PG/PG 兼容模式去尾零。
// 由 InitDynamicDecodersOnce 按数据目录设置（v1.0.47）。
var kbOracleMode bool

// kbMysqlMode：金仓 mysql 兼容模式（离线探测：类型目录含 oid 7944 的 date）。
// v1.2.16（回归报告 D7）：mysql 模式的文本形态与 PG 不同——
//   - interval 时间部分恒定 6 位微秒（"04:05:06.789000"），零值输出 "00:00:00.000000"；
//   - timestamptz 按实例时区输出本地时间，**不带** "+00" 后缀。
//
// 仅用于文本/字面量渲染，不影响存储解析。
var kbMysqlMode bool

// IsKbOracleMode：当前数据目录是否为金仓 oracle 兼容模式（供 engine 层字面量渲染用）。
func IsKbOracleMode() bool { return kbOracleMode }

// SetKbMysqlMode：显式设置 mysql 兼容模式开关（v1.2.18）。
// 生产路径由 DynamicScan.KbMysqlMode 经 InitDynamicDecoders 设置；本入口供
// 会话头构造（app.sqlSessionHeader）在单测中验证模式门控，也方便将来做前置探测。
func SetKbMysqlMode(v bool) { kbMysqlMode = v }

// IsKbMysqlMode：当前数据目录是否为金仓 mysql 兼容模式。
func IsKbMysqlMode() bool { return kbMysqlMode }

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
	if MysqlTZLoc != nil {
		return TimestampFromUS(us + mysqlTZOffsetAtUS(us))
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
	// v1.0.63 修复（B-4）：小数秒必须拼在 " BC" 之前。旧实现在 base 之后拼，
	// 输出 "0044-03-15 12:00:00 BC.5"，timestamp_in 把 ".5 BC" 整段当垃圾 →
	// 丢 BC 标志也丢微秒，且不报错。
	frac := ""
	if micro != 0 {
		f := fmt.Sprintf("%06d", micro)
		// v1.0.47：金仓 oracle 模式 timestamp_out 微秒恒定 6 位（".789000"），PG 风格去尾零（".789"）
		if !kbOracleMode {
			f = strings.TrimRight(f, "0")
		}
		frac = "." + f
	}
	if y <= 0 {
		return fmt.Sprintf("%04d-%02d-%02d %02d:%02d:%02d%s BC", 1-y, m, d, hh, mm, ss, frac)
	}
	return fmt.Sprintf("%04d-%02d-%02d %02d:%02d:%02d%s", y, m, d, hh, mm, ss, frac)
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

	// v1.2.13 修复（P2）：PG interval_out 的符号规则不是"时间为正就加 +"，而是
	// **逐分量继承前一分量的符号**：分量与前面最近一个非零分量同号则省略符号，
	// 异号必须显式标出。零分量不改变继承状态（PG datetime.c EncodeInterval 的
	// is_before 只在分量非零时更新）。实测 PG 17.6：
	//
	//   -1 days  02:03:04  →  -1 days +02:03:04   （日负、时正 → 异号，加 +）
	//   -1 mons  2 days 03:00:00 → -1 mons +2 days 03:00:00（日已转正，时间又同号）
	//    1 year  2 mons 3 days 04:05:06 → 原样（全正，无任何 +）
	//   -1 year  0 mons 2 days 03:00:00 → -1 years +2 days 03:00:00（零分量不重置）
	//   -1 day  -02:03:04  →  -1 days -02:03:04   （同号，负号来自时间自身）
	//    0 days  02:00:00  →  02:00:00            （无日期部分，正时间不加 +）
	//
	// 旧实现（v1.0.47 起）只按 timeUS<0 决定 "-"，正时间一律不标，导出成
	// "-1 days 02:03:04"——回灌值虽等价（interval_in 对无符号时间默认取正），
	// 但产物与 PG 逐字节不一致，下游基线比对/审计会误判为变更。
	prevNeg := false
	var parts []string
	addUnit := func(v int64, unit string) {
		if v == 0 {
			return // 零分量不输出、也不改变符号继承
		}
		var sign string
		switch {
		// v1.2.17 修复（P1）：负分量的 "-" 与"是否继承"无关——它就是这个分量
		// 自身的符号，任何情况下都不能省。旧条件多写了 `&& !prevNeg`，导致
		// 「月负 + 日负」时日的负号被吞：`-9 mons -2 days 04:30:00` 输出成
		// `-9 mons 2 days +04:30:00`，静默把值改了 4 天且回灌 rc=0。
		// 实测 PG 17.6 / 金仓 V9R1C10（pg 模式）：
		//   -9 mons -2 days +04:30:00 → -9 mons -2 days +04:30:00
		//   -1 mons -2 days -03:00:00 → -1 mons -2 days -03:00:00
		//   -1 mons  2 days  03:00:00 → -1 mons +2 days 03:00:00（正值才看继承）
		case v < 0:
			sign = "-"
		case v > 0 && prevNeg:
			sign = "+"
		}
		av := v
		if av < 0 {
			av = -av
		}
		// PG 只在 +1 时用单数；-1 依旧是复数（"-1 days" / "-1 mons" / "-1 years"）
		if av == 1 && v > 0 {
			parts = append(parts, fmt.Sprintf("%s1 %s", sign, unit))
		} else {
			parts = append(parts, fmt.Sprintf("%s%d %ss", sign, av, unit))
		}
		prevNeg = v < 0
	}
	addUnit(int64(year), "year")
	addUnit(int64(month), "mon")
	addUnit(int64(day), "day")

	// 时间部分的规则与日期分量不同：负时间**总是**带 "-"（它是时间自身的符号，
	// 不是分隔符）；只有正时间才看继承状态（前一分量为负时补 "+"）。
	// 实测 "-1 days -02:03:04" 不会省成 "-1 days 02:03:04"。
	sign := ""
	if timeUS < 0 {
		sign = "-"
	} else if timeUS > 0 && prevNeg {
		sign = "+"
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
	// v1.2.16 修复（回归报告 D7）：金仓 mysql 模式的 interval_out 时间部分恒定
	// 6 位微秒，"0" 输出 "00:00:00.000000"，纯日期分量时不追加时间部分——
	// 与 PG 的"去尾零 / 无小数"形态不同（实测 V9R1C10 COPY 输出：
	// "1 year 2 mons 3 days 04:05:06.789000" / "1 day 00:00:00.500000"）。
	if kbMysqlMode {
		if timeUS == 0 {
			if len(parts) == 0 {
				return "00:00:00.000000"
			}
			return strings.Join(parts, " ")
		}
		parts = append(parts, fmt.Sprintf("%s%02d:%02d:%02d.%06d", sign, hh, mm, ss, us))
		return strings.Join(parts, " ")
	}
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
		// B13 修复（v1.1.1，P1 数值损坏）：整数首位恒为首个 base-10000 组。
		// 旧实现条件写成 weight+1 <= ndigits（即"整数部分不越界"）才取 digits[0]，
		// 但 weight >= ndigits 恰恰是「尾组全零」的正常形态：
		//   10000 = 1 × 10000^1 → ndigits=1, weight=1, digits=[1]
		// 此时首位被判 0，整段整数丢成 "0"（numeric/json/jsonb 中 10000、20000 …
		// 这类值全部输出 0，且不报错）。判据只需"有数字组"。
		first := 0
		if ndigits > 0 {
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
//
// v1.0.63 修复（A-2）：numeric/bytea 此前标 degrade=true，绕过解码器直接按
// 「文本编码转换」处理 —— numeric 的 base-10000 二进制（ndjson/weight/sign +
// int16 digits）与 bytea 的原始字节被当成字符串，导出的 SQL 里带 0x02/0x88
// 这类控制字节：numeric[] 回灌报 invalid input syntax，bytea[] 变成乱码。
// 现在两者都走真正的元素解码器（"_p" 后缀 = 接收【已剥 varlena 头】的 payload），
// jsonb 同理——decodeJSONB 内部还会再剥一次头，直接喂 payload 才能解出内容
// （旧实现在实机上把 jsonb[] 元素全解成空串 '{"",""}'）。
var arrayElemInfo = map[uint32][4]interface{}{
	BOOLOID:        {1, "c", "bool", false},
	BYTEAOID:       {-1, "i", "bytea_p", false},
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
	NUMERICOID:     {-1, "i", "numeric_p", false},
	UUIDOID:        {16, "c", "uuid", false},
	JSONBOID:       {-1, "i", "jsonb_p", false},
	// v1.1.2 修复（B18）：json[] / 金仓 mysql 模式 json[] 元素。
	// PG 与金仓 PG 模式 json 是文本（OID 114，json[] = 199，elemtype=114）；
	// 金仓 V9 mysql 模式 json 是 domain of 4802 mysql_json，底层存储为 jsonb
	// 二进制（OID 7024，_json = 12529/12564，elemtype=7024）。
	// 旧实现两种 OID 都没有条目 → 落到"未知元素类型 → varlena 文本退化"，
	// 元素原始容器字节被当字符串输出（json[] 导出成 '{"\x01\x00\x00 \x01…"}'），
	// 回灌 json_in 报 invalid input syntax / 0x00 非法字节。
	// 114 走 "json"（剥头后的 UTF-8 文本）；7024 走 "jsonb_p"（剥头后的容器字节）。
	JSONOID:    {-1, "i", "json", false},
	7024:       {-1, "i", "jsonb_p", false},
	ACLITEMOID: {12, "i", "aclitem", false},
	// v1.0.65 修复（F2 配套）：macaddr/macaddr8 标量本就有解码器，但数组路径
	// 缺条目 → 走动态分支退化成原始字节（macaddr[] 导出 E'{\x08\x00+...}'，
	// macaddr_in 拒绝，回灌必失败）。补齐后与标量同一文本格式；
	// macaddr（6B/'i'）元素间距 8，正是 F2 对齐修复的直接受益者。
	MACADDROID:  {6, "i", "macaddr", false},
	MACADDR8OID: {8, "i", "macaddr8", false},
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
	// v1.0.63（A-6）：multirange 数组（PG14+）
	6150: 4451, 6151: 4532, 6152: 4533, 6153: 4534, 6155: 4535, 6157: 4536,
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
	// v1.2.10：走 enumBaseOID——"域 over 枚举"做元素（create domain d as mood;
	// create table t (v d[])）同样按 4 字节成员 OID 存储，按列 oid 查 EnumMap miss。
	if eo := enumBaseOID(elemOID); eo != 0 {
		if em, ok := EnumMapSnapshot()[eo]; ok {
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
	}
	if info, ok := arrayElemInfo[elemOID]; ok {
		alen := info[0].(int)
		aalign := info[1].(string)
		fn := info[2].(string)
		degrade := info[3].(bool)
		// B11：aclitem 元素长度按目录实测（PG17+: 16B），覆盖硬编码 12
		if elemOID == ACLITEMOID {
			alen, _ = aclitemElemLenAlign()
		}
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
		// v1.0.46 修复（float8[]/int8[]/timestamp[] 错位）→ v1.0.65 修正（F1）：
		// 定长元素【返回未填充的元素尾】——对齐填充由 DecodeArray 调用方统一
		// 按 (pos-bodyOff)%elemAlign 补齐。对齐基准是数据区起点（bodyOff 与
		// ARR_DATA_OFFSET 同余 mod 8），不是 datum 起点；v1.0.45 旧实现把
		// 1 维数组 bodyOff=20 按 8 对齐推到 24，错 4 字节，根因是基准错了，
		// 不是"不该对齐"。
		// 实机取证（PG17 + 金仓，pageinspect + 原始页字节）：{1.5,-2.25} 元素从
		// Payload+20 起——float8 typlen=8 恰为 align='d' 倍数，两种理解等价，
		// 故该取证不能区分"紧密排列"与"typalign 对齐"。上游 array.h 注释
		// "Individual items in the array are aligned as specified by the array
		// element type" + CopyArrayEls→ArrayCastAndSet（att_nominal_alignby）
		// 是定长元素同样对齐的权威依据；timetz(12/'d') 第 2 元素间距 16。
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
			// 定长元素：返回元素尾（未填充）；对齐填充由 DecodeArray 调用方统一处理
			//（v1.0.65 修复 F2：macaddr 6B/'i' 等定长元素同样按 typalign 对齐，
			// 见 dynElemAlign 注释；v1.0.46 的"紧密排列"结论仅对 typlen 恰为
			// align 倍数的类型成立）
			if td.Len > 0 {
				if pos+td.Len > len(Payload) {
					return "", len(Payload)
				}
				return dec(Payload[pos : pos+td.Len]), pos + td.Len
			}
		}
	}
	// v1.0.63 修复（A-6）：multirange 数组元素（PG14+）。multirange 是 varlena，
	// 没有对应的 arrayElemInfo 条目，旧实现落到下面的"varlena 文本退化"，
	// 输出的是原始二进制 → 回灌 multirange_in 报 malformed literal。
	if roid, ok := multiRangeRanges[elemOID]; ok {
		kind, total, _, _ := VarlenaParse(Payload, pos)
		if kind == "" || total < 1 || pos+total > len(Payload) {
			return "", len(Payload)
		}
		return decodeMultiRange(Payload[pos:pos+total], roid), pos + total
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
	// v1.0.63 修复（A-2）：payload 级解码器——seg 已剥 varlena 头，
	// 上面对应的 decodeBytea/DecodeNumeric/decodeJSONB 都会再剥一次。
	case "bytea_p":
		return "\\x" + hex.EncodeToString(seg)
	case "numeric_p":
		return numericDiskToStr(seg)
	case "jsonb_p":
		return jsonbContainerToText(seg)
	case "aclitem":
		return decodeAclitem(seg)
	case "macaddr":
		return decodeMacaddr(seg)
	case "macaddr8":
		return decodeMacaddr8(seg)
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
// v1.0.65 修复（F1/F2）：定长元素同样按 typalign 对齐——PG array.h 明文
// "Individual items in the array are aligned as specified by the array element
// type"，CopyArrayEls→ArrayCastAndSet 对定长元素同样走 att_nominal_alignby。
// macaddr（6B/'i'）元素间距是 8 不是 6，旧实现按紧密排列读，第 2 个元素起全错。
// typlen 恰为 align 倍数的类型（tid 6/'s'、money 8/'d'、macaddr8 8/'i'）填充为 0，
// 行为不变。
func dynElemAlign(elemOID uint32) int {
	if DynamicTypeDefs == nil {
		return 1
	}
	td, ok := DynamicTypeDefs[elemOID]
	if !ok {
		return 1
	}
	if td.Len > 0 {
		if a, ok := alignSizes[string(td.Align)]; ok {
			return a
		}
		return 1 // 定长类型无 Align 信息时按紧密排列兜底
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
	// v1.2.12 修复（A2）：ndim 合法值只有 0..6（0 = 空数组）。ndim<0 或 >6
	// 只可能是头部损坏；旧实现与合法空数组一样返回 "{}"、不计降级、不发告警，
	// 撕裂页里的数组会静默变成空数组（回灌成功、目标库丢数据、报告全绿）。
	// 现改为走 DecodeDefault（\x 原文 + DefaultHitSummary 告警），与同函数
	// bodyOff 越界 / 元素超限两条损坏分支口径一致。
	if ndim < 0 || ndim > 6 {
		return DecodeDefault(B)
	}
	if ndim == 0 {
		return "{}" // 合法空数组（PG array_out 输出 "{}"）
	}
	dims := make([]int, ndim)
	lbs := make([]int, ndim)
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
			return DecodeDefault(B) // v1.2.12（A2）：合法维长恒 >0，<=0 即头部损坏
		}
		if dims[i] > maxArrayElems || nelems > maxArrayElems/dims[i] {
			return DecodeDefault(B) // 元素总数超限/溢出 → 退回原始字节
		}
		nelems *= dims[i]
	}
	// v1.0.63 修复（B-13）：读下界 lbs[]。PG array_out 在任一维下界 ≠ 1 时输出
	// "[lb:ub][lb:ub]=…" 前缀；旧实现只读 dims，'[0:2]={1,2,3}' 导出成 '{1,2,3}'，
	// 回灌后 array_lower() 从 0 变 1（静默改变下标语义）。
	needBounds := false
	for i := 0; i < ndim; i++ {
		lbs[i] = int(int32(binary.LittleEndian.Uint32(Payload[12+4*ndim+4*i:])))
		if lbs[i] != 1 {
			needBounds = true
		}
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
	// v1.0.65 修复（F1/F2）：对齐规则扩展到【所有】元素——PG array.h 注释
	// "Individual items in the array are aligned as specified by the array element
	// type"，定长元素由 CopyArrayEls→ArrayCastAndSet 同样做 att_nominal_alignby。
	// 硬编码表里 typlen 恰为 align 倍数的类型（float8 8/'d'、int4 4/'i'、uuid 16/'c'
	// 等）填充为 0，行为与 v1.0.46 的实机取证完全一致；真正受影响的只有
	// timetz(12/'d'，间距 16) 这类 typlen 非 align 倍数的定长类型。
	// varlena 元素（alen==-1）由 arrayElemText 内部按「1 字节短头探测」处理，
	// 不能在此补齐（短头元素恰可能落在未对齐位置，att_align_pointer 语义）。
	elemAlign := 1
	if info, ok := arrayElemInfo[elemtype]; ok {
		if alen := info[0].(int); alen > 0 {
			elemAlign = alignSizes[info[1].(string)]
		}
	} else {
		elemAlign = dynElemAlign(elemtype)
	}
	// B11：aclitem 元素长度/对齐按目录实测（PG17+: 16B/'d'），覆盖硬编码 {12,"i"}
	if elemtype == ACLITEMOID {
		_, elemAlign = aclitemElemLenAlign()
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
	body := fmtArr(elems)
	if !needBounds {
		return body
	}
	var pre strings.Builder
	for i := 0; i < ndim; i++ {
		fmt.Fprintf(&pre, "[%d:%d]", lbs[i], lbs[i]+dims[i]-1)
	}
	return pre.String() + "=" + body
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
		S = formatIPv6(addr)
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

// formatIPv6：按 RFC 5952 输出 IPv6 文本，对齐 PG inet_out（inet_net_ntop）。
//
// v1.2.13 修复（P2）：旧实现固定拼 8 组十六进制（"0:0:0:0:0:0:0:1"），
// 与 PG 的 "::1"、"2001:db8::/64" 不一致——回灌虽等价，但产物无法与源库
// 逐字节比对。规则：
//  1. 最长连续全零组（长度≥2）压缩为 "::"，等长取最靠前的一段；
//  2. IPv4-mapped（前 5 组为 0、第 6 组为 ffff）输出 "::ffff:a.b.c.d"；
//  3. 全零输出 "::"。
func formatIPv6(addr []byte) string {
	var g [8]uint16
	for i := 0; i < 8; i++ {
		g[i] = uint16(addr[i*2])<<8 | uint16(addr[i*2+1])
	}
	// IPv4-mapped：::ffff:a.b.c.d（PG 实测输出此形式，而非 8 组十六进制）
	if g[0] == 0 && g[1] == 0 && g[2] == 0 && g[3] == 0 && g[4] == 0 && g[5] == 0xffff {
		return fmt.Sprintf("::ffff:%d.%d.%d.%d", addr[12], addr[13], addr[14], addr[15])
	}
	bestStart, bestLen := -1, 0
	for i := 0; i < 8; {
		if g[i] != 0 {
			i++
			continue
		}
		j := i
		for j < 8 && g[j] == 0 {
			j++
		}
		if j-i > bestLen {
			bestStart, bestLen = i, j-i
		}
		i = j
	}
	if bestLen < 2 { // 单个零组不压缩（RFC 5952 §4.2.2）
		bestStart, bestLen = -1, 0
	}
	var head []string
	for i := 0; i < bestStart; i++ {
		head = append(head, strconv.FormatUint(uint64(g[i]), 16))
	}
	if bestStart < 0 {
		out := make([]string, 8)
		for i := 0; i < 8; i++ {
			out[i] = strconv.FormatUint(uint64(g[i]), 16)
		}
		return strings.Join(out, ":")
	}
	var tail []string
	for i := bestStart + bestLen; i < 8; i++ {
		tail = append(tail, strconv.FormatUint(uint64(g[i]), 16))
	}
	return strings.Join(head, ":") + "::" + strings.Join(tail, ":")
}

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
//
// v1.2.13 修复（P1）：PG 的 money 全值域恰为 INT64_MIN/100 .. INT64_MAX/100，
// 故 INT64_MIN（-$92,233,720,368,547,758.08）是合法可达值。旧实现对 v<0 做
// `v = -v`，在 INT64_MIN 上溢出后仍为负，随后 v/100、v%100 全为负，
// 产出非法字面量 "-$-92,233,720,368,547,758.-8"，回灌 money_in 直接报错。
// 改为先求绝对值到 uint64（-(v+1)+1 规避溢出），再走无符号格式化。
func decodeMoney(B []byte) string {
	v := int64(binary.LittleEndian.Uint64(B))
	sign := ""
	var u uint64
	if v < 0 {
		sign = "-"
		u = uint64(-(v + 1)) + 1
	} else {
		u = uint64(v)
	}
	S := strconv.FormatUint(u/100, 10)
	var groups []string
	for len(S) > 3 {
		groups = append([]string{S[len(S)-3:]}, groups...)
		S = S[:len(S)-3]
	}
	groups = append([]string{S}, groups...)
	return sign + "$" + strings.Join(groups, ",") + fmt.Sprintf(".%02d", u%100)
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
	// v1.0.63（C-1）：删除遗留的 DBG_RANGE 调试钩子（会向 stderr 打印原始字节，
	// 生产环境既污染日志也泄露数据内容）。
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

// regClassNamespaces：pg_class oid → relnamespace（v1.2.3 / BUG-14）。
// 与 regClassNames 同源同生命周期，供 decodeRegclass 判断是否需加 schema 前缀。
var regClassNamespaces map[uint32]uint32

// regNamespaceNames：pg_namespace oid → nspname（v1.2.3 / BUG-14）。
var regNamespaceNames map[uint32]string

// DefaultRegNamespace 当前会话的等效 search_path 首项（v1.2.3 / BUG-14）。
// 离线导出无会话上下文，取 "public" 作为 PG 默认 search_path（$user, public）的实际落点：
// 与 BUG-14 现场实测的 PG 服务端行为一致（public 下的表省略前缀）。
// 置空串表示「无从判断」，此时一律输出 nspname.relname 的完整限定形式（保守、绝不丢信息）。
var DefaultRegNamespace = "public"

// decodeRegclass：regclass（oid 2205）。对齐 PG regclass_out——输出关系名；
// 标识符合法（^[a-z_][a-z0-9_$]*$）时裸输出，否则双引号包裹（内部 " 翻倍）。
// InvalidOid(0) 输出 "-"（PG reg*out 约定）；pg_class 反查失败时回退裸 OID。
//
// v1.2.3 修复（BUG-14）：补 schema 限定。
// 缺陷：旧实现只输出 relname（buildRegclassNameMap 只存 oid→relname，未存 relnamespace），
// 于是 regclass 列中指向非当前 search_path 的表会被静默降级为同名裸名。
// 实机复现：public.dup_t 与 sc2.dup_t 同名并存，某行 rc 原指 sc2.dup_t(oid 45830)，
// 导出成 "dup_t"，回灌后解析到 public.dup_t(oid 45823) —— same=false，无任何报错。
//
// 修复规则（对齐 PG regclassout）：oid 对应表的 namespace 与会话 search_path 首项不同时，
// 输出 "nspname.relname"；相同则只输出 relname。离线导出无会话，DefaultRegNamespace
// 取 "public"（PG 默认 search_path 的落点）。
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
			// BUG-14：判断是否需加 schema 前缀
			if nspOID, hasNsp := regClassNamespaces[oid]; hasNsp && regNamespaceNames != nil {
				if nspName, ok2 := regNamespaceNames[nspOID]; ok2 && nspName != "" {
					// v1.2.13 修复（P2）：PG 的 regclassout 用 RelationIsVisible，
					// 而 pg_catalog 是 search_path 的隐式前置项（无论用户是否显式列出），
					// 因此 pg_catalog 内的对象【不】加前缀。旧实现只把 public 当落点，
					// 把 'pg_class'::regclass 导出成 "pg_catalog.pg_class"（回灌正确但与 PG 不一致）。
					if nspName != DefaultRegNamespace && nspName != "pg_catalog" {
						return regQuoteIdent(nspName) + "." + regQuoteIdent(name)
					}
				}
			}
			return regQuoteIdent(name)
		}
	}
	return strconv.FormatUint(uint64(oid), 10)
}

// regHardwiredTypeNames：PG format_type.c 里 hard-wired 的类型名（typemod < 0 形态）。
//
// v1.2.15 修正（P1）：此前的 regtypeAliases 只做了"rename"，之后仍整串过
// regQuoteIdent；而这些名字是 **SQL 语法片段，不是标识符**，quote_identifier 的
// "小写标识符" 规则对它们完全不适用：
//   - "character varying" / "double precision" / "timestamp with time zone" 等含空格，
//     一旦按标识符加引号就变成 "character varying"（PG 里 type "character varying"
//     does not exist，回灌直接失败）；
//   - "char" 反过来 **必须** 带引号，否则 'char' 会被解析成 bpchar(1)（静默错类型）——
//     PG 这里并不 hard-wire，而是靠"命中关键字必须加引号"的兜底路径；
//   - varbit(1562)/bit(1560)/interval(1186)/numeric(1700) 的 typname 恰是关键字，
//     走 quote_identifier 会被错误加引号。
//
// 因此这里存的是 **已定型的最终文本**，调用方不得再 quoting。
//
// 表项与 PG 17 format_type_extended() 的 switch 一一对应，值由 PG 17.6 实机
// `oid::regtype::text` 逐条核对（见 v1215 单测与回归探针）。两个易错点：
//   - JSONOID 在 PG 里是 hard-wired（输出 json）。漏掉它就会被关键字规则误伤：
//     "json" 自 PG16 起是 col_name 关键字，会被加引号成 "json"；
//   - CHAROID **故意不收录**：PG 的 switch 里没有它，"char" 的引号恰是靠
//     "命中关键字必须加引号" 的兜底路径得来的（format_type.c 注释：
//     "This behavior is essential for some cases, such as types bit and char"）。
var regHardwiredTypeNames = map[uint32]string{
	16:   "boolean",                     // bool
	20:   "bigint",                      // int8
	21:   "smallint",                    // int2
	23:   "integer",                     // int4
	114:  "json",                        // json（PG16+ 是 col_name 关键字，必须 hard-wired）
	700:  "real",                        // float4
	701:  "double precision",            // float8
	1042: "character",                   // bpchar
	1043: "character varying",           // varchar
	1083: "time without time zone",      // time
	1114: "timestamp without time zone", // timestamp
	1184: "timestamp with time zone",    // timestamptz
	1186: "interval",                    // interval（INTERVAL 是关键字，裸 typname 会被误加引号）
	1266: "time with time zone",         // timetz
	1560: "bit",                         // bit（BIT 是关键字）
	1562: "bit varying",                 // varbit（PG 输出 bit varying，不是 varbit）
	1700: "numeric",                     // numeric（NUMERIC 是关键字）
}

// regTypeNameText：类型名的最终 SQL 文本（仅名字部分，不含 schema 限定与 "[]" 后缀）。
//   - hard-wired 类型：直接返回语法片段，**不得**再 quoting；
//   - 其余类型：按 PG quote_identifier 语义（合法小写标识符且非关键字才裸写）。
func regTypeNameText(oid uint32, typname string) string {
	if n, ok := regHardwiredTypeNames[oid]; ok {
		return n
	}
	return regQuoteIdent(typname)
}

// decodeRegtype：regtype（oid 2206）对齐 PG regtypeout —— 输出类型名而非裸 OID。
//
// v1.2.13 修复（P2）：旧实现直接走 decodeRegOid，把 23 导出成 "23"（PG 输出 "integer"）。
// 内置类型 OID 在各库稳定，回灌侥幸正确；但用户自定义类型/域/枚举的 OID 跨库不同，
// 裸 OID 会让回灌静默指向另一个类型（或直接解析失败）。
//
// 规则（对齐 format_type.c 的 format_type_be 路径）：
//   - 类型名先按 hard-wired 表取语法片段，其余按 quote_identifier 语义 quoting
//     （v1.2.15：多词标准名不加引号、"char" 必须加引号、关键字命名的自定义类型加引号）；
//   - pg_catalog 内的类型省略 schema 前缀（隐式 search_path 优先）；
//   - 数组类型（typname 以 "_" 开头且 typelem 非 0）输出 "元素类型[]"；
//   - 自定义 schema 的类型按 "nsp.typ" 限定（nsp 与 typ 各自按需 quoting）；
//   - pg_type 反查 miss 时回退裸 OID（保信息不丢，但会走 DefaultHit 计数）。
func decodeRegtype(B []byte) string {
	if len(B) < 4 {
		return DecodeDefault(B)
	}
	oid := binary.LittleEndian.Uint32(B)
	if oid == 0 {
		return "-" // PG reg*out 对 InvalidOid 的输出约定
	}
	if DynamicTypeDefs != nil {
		if td, ok := DynamicTypeDefs[oid]; ok && td.Name != "" {
			// 数组类型：typname 形如 "_int4"，PG 的 format_type 输出 "integer[]"。
			// 注意 [] 不是合法标识符，不能整串过 regQuoteIdent（会输出 "text[]"）。
			quoted := regTypeNameText(oid, td.Name)
			nsp := td.Nsp
			if strings.HasPrefix(td.Name, "_") && td.Elem != 0 {
				if etd, ok2 := DynamicTypeDefs[td.Elem]; ok2 && etd.Name != "" {
					quoted = regTypeNameText(td.Elem, etd.Name) + "[]"
					nsp = etd.Nsp // 数组的 schema 归属按元素类型判定
				}
			}
			if nsp != 0 && regNamespaceNames != nil {
				if nspName, ok2 := regNamespaceNames[nsp]; ok2 && nspName != "" &&
					nspName != "pg_catalog" && nspName != DefaultRegNamespace {
					return regQuoteIdent(nspName) + "." + quoted
				}
			}
			return quoted
		}
	}
	return strconv.FormatUint(uint64(oid), 10)
}

// decodeRegnamespace：regnamespace（oid 4089）对齐 PG regnamespaceout —— 输出 schema 名。
// v1.2.13：修正 OID 映射时一并实现（旧版把 4089 当成 regconfig，真 regnamespace
// 列的 OID 落到无解码器路径，输出裸数字，跨库无意义）。pg_namespace 反查表已有。
func decodeRegnamespace(B []byte) string {
	if len(B) < 4 {
		return DecodeDefault(B)
	}
	oid := binary.LittleEndian.Uint32(B)
	if oid == 0 {
		return "-"
	}
	if regNamespaceNames != nil {
		if n, ok := regNamespaceNames[oid]; ok && n != "" {
			return regQuoteIdent(n)
		}
	}
	return strconv.FormatUint(uint64(oid), 10)
}

// regQuoteIdent：等价于 PG 的 quote_identifier()。
//
// v1.2.15 修正（P1 根因）：旧实现只判断"是否小写合法标识符"，漏了 PG 的另一半规则
// —— **关键字必须加引号**（ScanKeywordCategories != UNRESERVED_KEYWORD）。后果是
// '"char"'::regtype 输出成裸 char（回灌落到 bpchar，静默错类型）、
// 名为 "any" 的伪类型输出裸 any、自定义域 "MyS"."char" 输出 MyS.char（解析报错）。
// 关键字集合见 reg_keywords.go（由 PG 17.6 pg_get_keywords() 生成）。
//
// 注意：类型名里 hard-wired 的标准名（"character varying" 等）**不走本函数**，
// 见 regTypeNameText —— 它们是 SQL 语法片段，不是标识符。
func regQuoteIdent(S string) string {
	if regValidIdent(S) && !regKeywordMustQuote[S] {
		return S
	}
	return `"` + strings.ReplaceAll(S, `"`, `""`) + `"`
}

// regValidIdent：PG quote_identifier 的"可裸写"判定（小写标识符部分）。
// 首字符限 a-z / _；其余限 a-z / 0-9 / _（**不含 $**，PG 也不认为带 $ 的名字可裸写）；
// 高位字节与非 ASCII 一律视为需要引号。
func regValidIdent(S string) bool {
	if S == "" {
		return false
	}
	if c := S[0]; c != '_' && !(c >= 'a' && c <= 'z') {
		return false
	}
	for _, r := range S {
		if r != '_' && !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') {
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

// multiRangeRanges：multirange OID → 对应标量 range 的 OID（PG14+）。
// v1.0.63 修复（A-6）：此前六个 multirange 全无解码器，整列退化成 \x 十六进制，
// multirange_in 不接受这种文本 → 回灌必然失败。
var multiRangeRanges = map[uint32]uint32{
	4451: 3904, // int4multirange → int4range
	4532: 3906, // nummultirange  → numrange
	4533: 3908, // tsmultirange   → tsrange
	4534: 3910, // tstzmultirange → tstzrange
	4535: 3912, // datemultirange → daterange
	4536: 3926, // int8multirange → int8range
}

// multiRangeElemAlign：各 range 子类型的元素对齐（PG attalign：int4/date/numeric
// 为 'i'=4，int8/timestamp/timestamptz 为 'd'=8）。
var multiRangeElemAlign = map[uint32]int{
	3904: 4, 3926: 8, 3906: 4, 3912: 4, 3908: 8, 3910: 8,
}

// decodeMultiRange：multirange 磁盘格式（PG multirangetypes.c 文件头注释）：
//
//	4B   multirange 类型自身 OID
//	4B   range 个数 n
//	4*(n-1)B  items（每 4 个一项是绝对偏移，其余是相对长度）
//	nB   flags（每个 range 一个字节）
//	对齐到元素 typalign 后：各 range 的【边界值】依次排列（不含 range 头、不含 flags）
//
// v1.0.63 修复（A-6）：此前六个 multirange OID 全无解码器，整列退化成 \x，
// multirange_in 不接受这种文本 → 回灌必然失败。
// 实现方式：把每个 range 的边界 + flags 重组成一个标准 RangeType datum，
// 直接复用 decodeRange 的括号/引号/空值逻辑，保证与 range_out 完全一致。
func decodeMultiRange(B []byte, rangeOID uint32) string {
	Payload, _, _ := VarPayload(B)
	if len(Payload) < 8 {
		return DecodeDefault(B)
	}
	n := int(int32(binary.LittleEndian.Uint32(Payload[4:])))
	if n < 0 || n > maxArrayElems {
		return DecodeDefault(B)
	}
	if n == 0 {
		return "{}"
	}
	align := multiRangeElemAlign[rangeOID]
	if align == 0 {
		align = 4
	}
	flagsOff := 8 + 4*(n-1)
	if flagsOff+n > len(Payload) {
		return DecodeDefault(B)
	}
	boundsOff := flagsOff + n
	if r := boundsOff % align; r != 0 {
		boundsOff += align - r
	}
	if boundsOff > len(Payload) {
		return DecodeDefault(B)
	}
	sub, ok := rangeSubtypes[rangeOID]
	if !ok {
		return DecodeDefault(B)
	}
	pos := boundsOff
	parts := make([]string, 0, n)
	for i := 0; i < n; i++ {
		flags := Payload[flagsOff+i]
		if flags&0x01 != 0 { // RANGE_EMPTY
			parts = append(parts, "empty")
			continue
		}
		var lo, hi []byte
		var okb bool
		if flags&0x08 == 0 { // 有下界
			if lo, pos, okb = multiRangeBound(Payload, pos, boundsOff, sub.wid, align); !okb {
				return DecodeDefault(B)
			}
		}
		if flags&0x10 == 0 { // 有上界
			if hi, pos, okb = multiRangeBound(Payload, pos, boundsOff, sub.wid, align); !okb {
				return DecodeDefault(B)
			}
		}
		parts = append(parts, decodeRange(buildRangeDatum(rangeOID, lo, hi, flags), rangeOID))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// multiRangeBound：读一个边界值（定长子类型按 wid 取，变长按 varlena 取），
// 变长值读完后按元素对齐推进（datum_write 的 att_align_nominal 行为）。
func multiRangeBound(p []byte, pos, base, wid, align int) ([]byte, int, bool) {
	if wid > 0 {
		if pos < 0 || pos+wid > len(p) {
			return nil, pos, false
		}
		return p[pos : pos+wid], pos + wid, true
	}
	kind, total, _, _ := VarlenaParse(p, pos)
	if kind == "" || total < 1 || pos+total > len(p) {
		return nil, pos, false
	}
	next := pos + total
	if r := (next - base) % align; r != 0 {
		next += align - r
	}
	return p[pos : pos+total], next, true
}

// buildRangeDatum：把边界与 flags 重组成标准 RangeType varlena
// （4B 长度头 + range 类型 OID + 下界 + 上界 + 1B flags），供 decodeRange 解析。
func buildRangeDatum(rangeOID uint32, lo, hi []byte, flags byte) []byte {
	buf := make([]byte, 8, 8+len(lo)+len(hi)+1)
	binary.LittleEndian.PutUint32(buf[4:], rangeOID)
	buf = append(buf, lo...)
	buf = append(buf, hi...)
	buf = append(buf, flags)
	binary.LittleEndian.PutUint32(buf[0:], uint32(len(buf))<<2)
	return buf
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
// decodeChar：内部类型 "char"（CHAROID）存取均为【原始单字节】（charout 直接
// 写 1 字节）。v1.0.65 修复（F7）：旧实现 string(rune(B[0])) 把 ≥0x80 的字节
// 转成 UTF-8 双字节（0xE9 → C3 A9），导出值改变且不可逆（回灌取首字节 0xC3）。
// 与 charout 一致按原始字节透传。
func decodeChar(B []byte) string {
	if len(B) == 0 {
		return ""
	}
	return string(B[:1])
}

func decodeUnknown(B []byte) string { return charset.DecodeBytes(B) }

func decodePgNodeTree(B []byte) string { return DecodeVarlenaText(B) }

// decodeOidvector / decodeInt2vector（v1.0.59 修复 P1-3）：
// 解码器契约是"接收完整 varlena 自行解头"（对照 decodeBytea）。
// 旧实现把 varlena 头字节当 oid/int2 元素输出且长度含头。现先剥 varlena 头
// （1B/4B/4BC），再跳 20B 数组头（ndim/dataoff/elemtype/dim1/lbound），
// 按 dim1 截取元素（与 ddl.ParseOidVector 同一语义）。
func decodeOidvector(B []byte) string {
	payload, _, _ := VarPayload(B)
	if len(payload) < 20 {
		return ""
	}
	dim1 := int(int32(binary.LittleEndian.Uint32(payload[12:16])))
	if dim1 < 0 || dim1 > 4000 {
		return ""
	}
	vals := payload[20:]
	parts := make([]string, 0, dim1)
	for i := 0; i < dim1 && i*4+4 <= len(vals); i++ {
		parts = append(parts, strconv.FormatUint(uint64(binary.LittleEndian.Uint32(vals[i*4:])), 10))
	}
	return strings.Join(parts, " ")
}

func decodeInt2vector(B []byte) string {
	payload, _, _ := VarPayload(B)
	if len(payload) < 20 {
		return ""
	}
	dim1 := int(int32(binary.LittleEndian.Uint32(payload[12:16])))
	if dim1 < 0 || dim1 > 4000 {
		return ""
	}
	vals := payload[20:]
	parts := make([]string, 0, dim1)
	for i := 0; i < dim1 && i*2+2 <= len(vals); i++ {
		parts = append(parts, strconv.Itoa(int(int16(binary.LittleEndian.Uint16(vals[i*2:])))))
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

// B8 修复（v1.0.66）：AclItem 内存布局按 PG acl.h 校正为 [ai_grantee][ai_grantor][ai_privs]
// （grantee 在前），旧实现按 [grantor][grantee] 读取 → 跨角色授权项 grantor/grantee
// 互换（bob=arw/alice 导出成 alice=arw/bob）、PUBLIC 授权项（grantee=0，"=r/alice"）
// 被误当具名角色导出、PUBLIC 权限静默丢失。owner 自我授权两项对称故此前未暴露。
// aclitemElemLenAlign：aclitem 元素的磁盘长度/对齐（数组路径用）。
// B11 修复（v1.0.66）：PG17 起 AclMode 扩为 64 位（acl.h：ai_privs 低 32 位为
// 权限位、高 32 位为 grant option 位图），AclItem 变 16 字节、pg_type typlen=16/
// typalign='d'（PG17.6/PG18 实测 + 官方 acl.h 证实）；PG16 及以前（含金仓全系
// V8R6/V9）为 12 字节、typalign='i'，grant option 在 ai_privs 高 16 位。
// v1.0.65 的硬编码 {12,"i"} 在 PG17 上从第 2 个元素起全部错位、grant option
// 静默丢失。以目录 pg_type 实测值（buildTypeDefs 注入）为准，无目录信息时按
// 12/'i' 兜底（兼容老版本/目录读取失败场景）。
func aclitemElemLenAlign() (int, int) {
	if td, ok := DynamicTypeDefs[ACLITEMOID]; ok && td.Len == 16 {
		return 16, 8
	}
	return 12, 4
}

func decodeAclitem(B []byte) string {
	if len(B) < 12 {
		return "oid:0"
	}
	grantee := binary.LittleEndian.Uint32(B)
	grantor := binary.LittleEndian.Uint32(B[4:])
	privs := binary.LittleEndian.Uint32(B[8:])
	// B11：PG17+ 的 ai_privs 是 64 位——高 32 位为 grant option 位图（独立 4 字节）；
	// PG16- 的 64 位字段中高 16 位才是 grant option。两种布局 grantee/grantor
	// 位置相同（均为 grantee 在前，B8 修复对两者同时生效）。
	goptions := privs >> 16
	if len(B) >= 16 {
		goptions = binary.LittleEndian.Uint32(B[12:])
	}
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
	// v1.0.65 修复（F3）：ai_privs 高 16 位是 WITH GRANT OPTION 位图
	//（acl.h ACLITEM_GET_GOPTIONS = ai_privs>>16）。aclitemout 逐位输出：
	// priv 置位 → 权限字符；goption 置位 → 仅 '*'（紧跟同位字符后，priv 未置位
	// 而 goption 置位时输出孤立 '*'）。旧实现只扫低 16 位，GRANT ... WITH GRANT
	// OPTION 导出后静默降级为普通授权。
	// B12：权限字符按引擎选表（金仓位 12 = 'p'，PG = 's'）
	privChars := aclChars
	if KbEngine.Load() {
		privChars = kbAclChars
	}
	for i, ch := range privChars {
		if privs&(1<<i) != 0 {
			chars.WriteByte(byte(ch))
		}
		if goptions&(1<<i) != 0 {
			chars.WriteByte('*')
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
	// --tz 显式指定优先，见 run.go parseTZOffset/applyConfTimezone）
	// v1.1.1（B17）：优先按 IANA 名加载完整时区规则（含历史 LMT/夏令时），
	// 加载失败再退回固定偏移；两者都不行才保持默认 +8 并告警。
	if !MysqlTZSet {
		if name, ok := util.ParseConfTimezoneName(dbDir); ok {
			if !SetMysqlTZByName(name) {
				if off, ok2 := util.ParseTZOffset(name); ok2 {
					MysqlTZOffsetUS = off
					MysqlTZSet = true
				}
			}
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
	KbMysqlMode    bool
	RegClassNames  map[uint32]string
	TypeDefs       map[uint32]*TypeDef
	CompositeAttrs map[uint32][]*AttrRow
	ProcNames      map[uint32]string
	// v1.2.3（BUG-14）：regclass 的 schema 限定所需
	RegClassNamespaces map[uint32]uint32
	RegNamespaceNames  map[uint32]string
	// v1.2.13：reg* 剩余族（regproc/regprocedure/regoper/regoperator/
	// regconfig/regdictionary/regrole/regcollation）的反查表，
	// 由 catalog.ScanDynamic 构建、reg_family.go 消费。
	RegProc   map[uint32]RegProcInfo
	RegOper   map[uint32]RegOperInfo
	RegConfig map[uint32]RegNamed
	RegDict   map[uint32]RegNamed
	RegRole   map[uint32]string
	RegColl   map[uint32]RegNamed
}

// DynamicScanner 目录扫描回调（app 启动时注册 catalog.ScanDynamic）。
// 为 nil 时退化为「仅硬编码解码表」。
var DynamicScanner func(dbDir string, version int, isKB bool) *DynamicScan

// PostScanHook 目录扫描之外的补充初始化回调（app 注册：域 CHECK 约束加载等）。
var PostScanHook func(dbDir string, version int, isKB bool)

// initDynamicDecoders 构建动态解码器映射（目录信息由 DynamicScanner 注入）。
// 返回动态映射数量（-1 表示目录缺失，保持仅硬编码表）。
func initDynamicDecoders(dbDir string, version int, isKB bool) int {
	// B12：ACL 权限字符表按引擎切换（金仓位 12 = 'p'）
	KbEngine.Store(isKB)
	// v1.0.50：域 CHECK 约束（pg_constraint conbin 节点树反解）
	if PostScanHook != nil {
		PostScanHook(dbDir, version, isKB)
	}
	procs := map[uint32]string{}
	// v1.2.13：regoperator 缺失参数输出形态（PG16+ 为 NONE，PG12-15/金仓为旧形态）
	regOperNONEArgs = !isKB && version >= 16
	if DynamicScanner != nil {
		if scan := DynamicScanner(dbDir, version, isKB); scan != nil {
			// v1.0.47：oracle 兼容模式探测（timestamp 微秒 6 位）与 regclass 反查表。
			kbOracleMode = isKB && scan.KbOracleMode
			// v1.2.16（D7）：mysql 兼容模式探测（时间类型文本形态对齐 mysql_out）
			kbMysqlMode = isKB && scan.KbMysqlMode
			regClassNames = scan.RegClassNames
			// v1.2.3（BUG-14）：schema 限定所需的 namespace 映射
			regClassNamespaces = scan.RegClassNamespaces
			regNamespaceNames = scan.RegNamespaceNames
			// v1.2.13：reg* 剩余族反查表（须在 TypeDefs 早退 return 之前赋值）
			regProcInfos = scan.RegProc
			regOperInfos = scan.RegOper
			regConfigInfos = scan.RegConfig
			regDictInfos = scan.RegDict
			regRoleNames = scan.RegRole
			regCollInfos = scan.RegColl
			DynamicTypeDefs = scan.TypeDefs
			// v1.2.8：目录换代，清空 char 容器判定缓存（见 char_expr.go）
			ResetCharContainerKindCache()
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
	// 2) 数组：元素类型的 typarray 反向指回本 oid。
	// v1.0.48 修复（P1-F1）：原判据 `td.Array == oid` 永远不成立——pg_type 里
	// 数组类型自身的 typarray 恒为 0，只有元素类型的 typarray 指向数组类型。
	// 该分支此前是死代码，自定义数组（enum[]/复合[]/域[]）全部没有解码器。
	// v1.2.10：本循环必须先于 domain 递归——"域 over 自定义数组"的解码器
	// 靠 resolveDecoder 命中 dd[数组oid]，数组未注册前域循环拿到的恒为 nil
	// （实测 create domain d_arr as comp_t[] 整列退化 \x 十六进制）。
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
	// 3) domain：沿 typbasetype 递归
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
	// v1.2.10：枚举判断走 enumBaseOID——"域 over 枚举"（如 create domain d as mood、
	// 域链任意深度）磁盘上同样是 4 字节成员 OID，此前只查列自身 oid 的 EnumMap
	// 必然 miss，整列退化成 \x 十六进制。普通枚举列直查命中，零额外开销。
	if eo := enumBaseOID(oid); eo != 0 {
		if em, ok := EnumMapSnapshot()[eo]; ok && len(raw) >= 4 {
			memberOID := binary.LittleEndian.Uint32(raw)
			for _, m := range em {
				if m.MemberOID == memberOID {
					return m.Label
				}
			}
			return strconv.FormatUint(uint64(memberOID), 10)
		}
	}
	dec, ok := Decoders[oid]
	if !ok {
		// v1.2.8（P2）：复合类型直接解码；域 over 复合沿 typbasetype 解析后按
		// 基复合类型解码。此前只判 Type=='c'，导致 `create domain d as ct1` 这类
		// 域列没有解码器、整列退化成 \x 十六进制（v1.2.7 实测：oid 46879 全部
		// 单元格以 \x 原文输出，回灌必失败），且该文本连 char 容器路径都进不去。
		if cOID := compositeBaseOID(oid); cOID != 0 {
			return recordDefaultHit(oid, DecodeComposite(cOID, raw))
		}
		if DynamicDecoders != nil {
			if dec2, ok2 := DynamicDecoders[oid]; ok2 {
				return recordDefaultHit(oid, dec2(raw))
			}
		}
		return recordDefaultHit(oid, DecodeDefault(raw))
	}
	// v1.2.11（BUG-2）：定长解码器对 len < 类型宽度 会越界 panic，先守卫再派发。
	if need, has := minDecoderWidth[oid]; has && len(raw) < need {
		return recordShortDatum(oid, raw)
	}
	return recordDefaultHit(oid, dec(raw))
}

// compositeBaseOID：oid 本身是复合类型则返回自身；是域则沿 typbasetype
// 递归找到最终的复合类型（域套域最多 maxTypeChainHops 跳）；不是复合系返回 0。
// v1.2.9：上限从裸魔数 8 改为具名常量，且超限不再静默——记账后由导出收尾
// 的 TypeChainUnresolvedSummary 告警（此前 ≥9 层会直接回落 \x 十六进制）。
func compositeBaseOID(oid uint32) uint32 {
	if DynamicTypeDefs == nil {
		return 0
	}
	cur := oid
	for i := 0; i < maxTypeChainHops; i++ {
		td := DynamicTypeDefs[cur]
		if td == nil {
			return 0
		}
		switch td.Type {
		case 'c':
			return cur
		case 'd':
			if td.Base == 0 {
				return 0
			}
			cur = td.Base
		default:
			return 0
		}
	}
	RecordTypeChainUnresolved(oid, "域链")
	return 0
}

// enumBaseOID：oid 本身是枚举（或已在 EnumMap）则返回自身；是域则沿
// typbasetype 找最终枚举（域套域最多 maxTypeChainHops 跳）；不是枚举系返回 0。
// v1.2.10：域 over 枚举的磁盘表示与枚举相同（4 字节成员 OID），此前按列
// oid 查 EnumMap 必然 miss，整列退化 \x 十六进制。普通枚举列首查即命中，
// 无额外开销；域链解析与 compositeBaseOID 同待遇（超限记账）。
func enumBaseOID(oid uint32) uint32 {
	if _, ok := EnumMapSnapshot()[oid]; ok {
		return oid
	}
	if DynamicTypeDefs == nil {
		return 0
	}
	cur := oid
	for i := 0; i < maxTypeChainHops; i++ {
		td := DynamicTypeDefs[cur]
		if td == nil {
			return 0
		}
		switch td.Type {
		case 'e':
			if _, ok := EnumMapSnapshot()[cur]; ok {
				return cur
			}
			return 0
		case 'd':
			if td.Base == 0 {
				return 0
			}
			cur = td.Base
		default:
			return 0
		}
	}
	RecordTypeChainUnresolved(oid, "域链")
	return 0
}

// ---------- v1.2.4：\x 退化静默兜底告警（BUG-13 同类风险扫描结论 §5-1） ----------
//
// 背景：BUG-13（aclitem 标量未注册到 Decoders）潜伏多年的根因之一，是漏注册后
// DecodeDefault 静默输出 \x 十六进制——不告警、退出码 0，用户拿到看似成功的文件
// 回灌才报错。本机制在 DecodeValue 出口统一拦截：凡结果以 \x 开头且类型不是
// bytea（byteaout 本就以 \x 为合法输出格式），记录 oid 与出现次数；
// 导出收尾由 CLI 调 DefaultHitSummary() 汇总打印，让同类缺口从静默变成长鸣。

var (
	defaultHitMu     sync.Mutex
	defaultHitCounts = map[uint32]int{} // oid → 以 \x 退化的单元格数
)

// hexLegalOIDs：输出 \x 前缀属正常语义的类型（byteaout 默认格式，byteain 接受）。
var hexLegalOIDs = map[uint32]bool{
	BYTEAOID: true, // 17
	8013:     true, // 金仓 blob（decodeByteaEscape 输出 escape 格式，兼容 \x 场景）
}

func recordDefaultHit(oid uint32, result string) string {
	if len(result) >= 2 && result[0] == '\\' && result[1] == 'x' && !hexLegalOIDs[oid] {
		defaultHitMu.Lock()
		defaultHitCounts[oid]++
		defaultHitMu.Unlock()
	}
	return result
}

// DefaultHitSummary：导出收尾汇总。无退化时返回空串。
// 调用后清零（每轮导出独立统计，避免 --jobs 多轮叠加）。
func DefaultHitSummary() string {
	defaultHitMu.Lock()
	defer defaultHitMu.Unlock()
	if len(defaultHitCounts) == 0 {
		return ""
	}
	oids := make([]int, 0, len(defaultHitCounts))
	total := 0
	for oid, n := range defaultHitCounts {
		oids = append(oids, int(oid))
		total += n
	}
	sort.Ints(oids)
	parts := make([]string, 0, len(oids))
	for _, oid := range oids {
		parts = append(parts, fmt.Sprintf("%d(×%d)", oid, defaultHitCounts[uint32(oid)]))
	}
	defaultHitCounts = map[uint32]int{}
	return fmt.Sprintf("提示: 有 %d 个单元格的值无法按列类型解码，已按十六进制原文输出（\\x 前缀）。"+
		"该类型可能未注册解码器，回灌会失败——请反馈并提供类型 oid: %s",
		total, strings.Join(parts, ", "))
}

// ---------- v1.2.18：域 DEFAULT 无法还原（P3） ----------

// DomainDefaultUnresolved：一个 DEFAULT 无法离线还原的域。
type DomainDefaultUnresolved struct {
	OID    uint32
	Name   string
	Schema string
}

var (
	domainDefMu         sync.Mutex
	domainDefUnresolved []DomainDefaultUnresolved
)

// RecordDomainDefaultUnresolved：记录一个"域的 DEFAULT 存在但没能还原"的类型。
//
// 为什么需要：typeTailDefault 定位失败、或 typdefaultbin 反解失败时，旧实现直接
// 让 TypDefault 保持空，DDL 只是少一段 DEFAULT —— 没有 report 字段、没有 fail 信号，
// 除非有人比对 pg_get_expr(typdefaultbin)，否则发现不了（对照 v1.2.16 给分区边界
// 加的 partition_bound_unresolved 计数，这里原本完全没有）。
//
// 放在 datatype 包而非直接调 integrity.AddSkip：integrity 已 import catalog，
// 而 catalog 需要在这里登记，反向引用会成环。由 app 层在导出收尾消费。
func RecordDomainDefaultUnresolved(oid uint32, name, schema string) {
	domainDefMu.Lock()
	defer domainDefMu.Unlock()
	for _, d := range domainDefUnresolved {
		if d.OID == oid {
			return
		}
	}
	domainDefUnresolved = append(domainDefUnresolved, DomainDefaultUnresolved{
		OID: oid, Name: name, Schema: schema,
	})
}

// DomainDefaultUnresolvedSummary：取走并清空未解析列表（导出收尾调用）。
func DomainDefaultUnresolvedSummary() []DomainDefaultUnresolved {
	domainDefMu.Lock()
	defer domainDefMu.Unlock()
	if len(domainDefUnresolved) == 0 {
		return nil
	}
	out := domainDefUnresolved
	domainDefUnresolved = nil
	return out
}

// ResetDomainDefaultUnresolved：清空（单测用，保证用例间互不影响）。
func ResetDomainDefaultUnresolved() {
	domainDefMu.Lock()
	defer domainDefMu.Unlock()
	domainDefUnresolved = nil
}

// ---------- v1.2.11：定长值长度不足守卫（BUG-2） ----------
//
// 缺陷背景：23 个定长解码器直接按下标取值（binary.LittleEndian.Uint64(B) 等），
// 对 len < 类型宽度 的输入立即越界 panic（空输入即
// "index out of range [7] with length 0"）。
//
// 主链路本来有间接保护——ExtractFieldsDirect 对定长列有
// "offset+AttLen > nraw → nil" 守卫，切出的切片恒等于目录里的 AttLen。
// 但该守卫【信任目录宽度】，以下三类情形会漏：
//  1. 目录与磁盘不一致：跨版本 --catalog JSON、金仓 V8/V9 同 oid 不同 typlen、
//     手工编辑的目录 JSON —— AttLen 小于解码器实际读取宽度；
//  2. 某类型 typlen=-1 却注册了定长解码器（新增金仓兼容类型时易发生）——
//     1B 短头 varlena 的 payload 只有 1~2 字节；
//  3. 后续新增调用点未复刻该守卫（run.go 的纯 --count 逐叶子路径即此类，
//     v1.2.11 已同步补 recover，见 BUG-2a）。
//
// 处理：DecodeValue 在派发到 Decoders 之前按 minDecoderWidth 预检，长度不足
// 则按十六进制原文输出并计数，由导出收尾 ShortDatumSummary 统一告警——
// 与 v1.2.4 的 \x 退化、v1.2.6 的 "char" 高位字节同款"不静默"机制
// （静默丢值比报错更危险，取证场景尤其）。
//
// minDecoderWidth 的取值不是估算：由 TestMinDecoderWidthMatchesTable 对每个
// oid 实测（宽度-1 必 panic、宽度必不 panic）双向断言，新增定长解码器时
// TestAllDecodersRejectShortInput 会直接失败，强制补表。
var minDecoderWidth = map[uint32]int{
	20:    8,  // int8
	21:    2,  // int2
	23:    4,  // int4
	26:    4,  // oid
	27:    6,  // tid（block 4 + offset 2）
	28:    4,  // xid
	29:    4,  // cid
	700:   4,  // float4
	701:   8,  // float8
	790:   8,  // money
	1082:  4,  // date
	1083:  8,  // time
	1114:  8,  // timestamp
	1184:  8,  // timestamptz
	1186:  16, // interval（time 8 + day 4 + month 4）
	1266:  12, // timetz（time 8 + zone 4）
	4189:  8,  // 金仓 datetime（domain of 1114）
	7944:  4,  // 金仓 V9 date
	7950:  8,  // 金仓 mysql time（微秒 int64）
	7952:  8,  // 金仓 V9 datetime
	7954:  8,  // 金仓 mysql timestamp（微秒 int64 + 时区换算）
	8020:  8,  // 金仓 oracle DATE
	12636: 8,  // 金仓 ora_date（domain of 8020）
}

var (
	shortDatumMu     sync.Mutex
	shortDatumCounts = map[uint32]int{} // oid → 长度不足的单元格数
)

// recordShortDatum：长度不足的定长值按原始字节十六进制输出并计数。
// 不走 recordDefaultHit——两个计数器语义不同（一个是"无解码器"，一个是
// "有解码器但数据不足"），混在一起会让收尾告警文案失真。
func recordShortDatum(oid uint32, raw []byte) string {
	shortDatumMu.Lock()
	shortDatumCounts[oid]++
	shortDatumMu.Unlock()
	return "\\x" + hex.EncodeToString(raw)
}

// ShortDatumSummary：导出收尾汇总。无异常时返回空串；调用后清零。
func ShortDatumSummary() string {
	shortDatumMu.Lock()
	defer shortDatumMu.Unlock()
	if len(shortDatumCounts) == 0 {
		return ""
	}
	oids := make([]int, 0, len(shortDatumCounts))
	total := 0
	for oid, n := range shortDatumCounts {
		oids = append(oids, int(oid))
		total += n
	}
	sort.Ints(oids)
	parts := make([]string, 0, len(oids))
	for _, oid := range oids {
		parts = append(parts, fmt.Sprintf("%d(×%d, 需%d字节)", oid,
			shortDatumCounts[uint32(oid)], minDecoderWidth[uint32(oid)]))
	}
	shortDatumCounts = map[uint32]int{}
	return fmt.Sprintf("警告: 有 %d 个单元格的原始字节长度不足其类型的定长宽度，已按十六进制原文输出（\\x 前缀）。"+
		"通常意味着目录与数据文件不匹配（跨版本 catalog / 元数据被篡改）或数据页损坏，回灌会失败——请核对 oid: %s",
		total, strings.Join(parts, ", "))
}

// ---------- "char"(18) 高位字节告警（v1.2.6，P2） ----------
//
// 缺陷背景：decodeChar 按原始单字节透传（v1.0.65 F7），"char" 列存 ≥0x80 字节
// （如 UTF-8 库中 'É' 经 charin 存为孤立 0xC3）时，SQL/CSV/text 三条格式均原样
// 输出该字节 → UTF-8 等多字节编码目标库回灌时 mb 校验拒绝
// （invalid byte sequence for encoding "UTF8"），该行无法导入且无告警。
//
// SQL 路径已可逆修复（engine.sqlQuote 输出 E'\xC3'）；CSV/text 的 COPY 格式
// 无法表达非法编码字节（\x 形态服务端不认），只能检测到时计数，导出收尾统一
// 告警（与 v1.2.4 DefaultHitSummary 同款机制，自清零，多轮导出独立统计）。

var (
	charHiMu    sync.Mutex
	charHiCount int
)

// RecordCharHighByte：fmtRow 的 CSV/text 分支检测到 "char" 高位字节值时计数。
func RecordCharHighByte() {
	charHiMu.Lock()
	charHiCount++
	charHiMu.Unlock()
}

// takeCharHighByteCount：内部用，取走计数（自清零）。
func takeCharHighByteCount() int {
	charHiMu.Lock()
	defer charHiMu.Unlock()
	n := charHiCount
	charHiCount = 0
	return n
}

// CharHighByteSummary：导出收尾汇总 "char" 高位字节告警；无则返回 ""。
// v1.2.8（P2）：告警范围扩到 char 容器（数组元素 / 域基类型 / 复合字段），
// 并单列「SQL 格式也无法表达、已降级为原样输出」的条数（自定义下界数组等）。
// v1.2.9：降级分支的处置建议重写——原文案让用户「改用 --format binary」，
// 而该格式从未实现（args.go 只接受 sql/csv/text，照做直接 exit 1），
// 「按 DDL 单独处理」也没有可执行落点。现改为两条真实可行的路径 + 手册指向，
// 并把触发原因补全（原文案只提自定义下界/类型名，漏了结构不规则与解析失败）。
func CharHighByteSummary() string {
	n := takeCharHighByteCount()
	deg := takeCharContainerDegradedCount()
	if n == 0 && deg == 0 {
		return ""
	}
	if n == 0 {
		return fmt.Sprintf("警告: 有 %d 个 \"char\" 容器值含 NUL 或非 ASCII 字节，"+
			"但无法用单条 SQL 表达式还原（数组下界非 1 / 数组或复合结构不规则 / 类型全名不可得），"+
			"SQL 格式无法表达该字节，已按原始字节原样输出——回灌 UTF-8 等多字节编码库时对应行将被拒绝。可行处理："+
			"① 目标库改用单字节编码（如 latin1 / SQL_ASCII）回灌；"+
			"② 导出后人工两步改写该行（数组：先插入同下界的 ASCII 占位值，再 UPDATE t SET col[lb] = (N)::\"char\"；"+
			"复合：按 ROW(...)::type 形式重写）；"+
			"详见用户手册「\"char\" 高位字节」一节", deg)
	}
	msg := fmt.Sprintf("提示: 有 %d 个 \"char\" 容器值（标量列/数组元素/域/复合字段）含 NUL 或非 ASCII 字节，"+
		"已按原始字节输出；CSV/text 格式无法转义该字节，回灌 UTF-8 等多字节编码库时对应行将被拒绝"+
		"（SQL 格式已输出 (N)::\"char\" 或 ARRAY[...]/ROW(...) 强转形式，可正确回灌）", n)
	if deg > 0 {
		msg += fmt.Sprintf("；另有 %d 个值因数组下界非 1 / 结构不规则 / 类型全名不可得，SQL 格式同样无法还原，"+
			"仍会被拒绝（处理方式见用户手册「\"char\" 高位字节」一节）", deg)
	}
	return msg
}

// TakeCharHighByteCountForTest：跨包测试用，取走当前计数（自清零）。
func TakeCharHighByteCountForTest() int {
	return takeCharHighByteCount()
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
	3220: decodePgLsn,        // pg_lsn
	2970: decodeTxidSnapshot, // txid_snapshot
	3615: decodeTsquery,      // tsquery
	// v1.2.13 修复（P2）：reg* 系列的 OID 曾整表错位——旧版把 2206 标成
	// regprocedure、2210 标成 regtype，而 PG17.6 实机
	// (select oid,typname from pg_type where typname like 'reg%') 是：
	// 24=regproc 2202=regprocedure 2203=regoper 2204=regoperator 2205=regclass
	// 2206=regtype 3734=regconfig 3769=regdictionary 4089=regnamespace
	// 4096=regrole 4191=regcollation。旧映射把解码器挂在普通列根本不会出现的
	// 空号（2210/2207/2208/2209/4090）上，真正的 regtype(2206) 因此落到裸 OID，
	// 真 regnamespace(4089) 被当 regconfig。无 pg_proc/操作符/排序规则反查表的
	// 族仍输出裸 OID（跨库不可移植，但不再错位到别的类型语义）。
	// v1.2.13 补齐：regproc/regprocedure/regoper/regoperator/regconfig/
	// regdictionary/regrole/regcollation 反查表（pg_proc/pg_operator/
	// pg_ts_config/pg_ts_dict/pg_authid/pg_collation，catalog.ScanDynamic 注入）。
	// 反查 miss（目录读不到/行缺失）时回退裸 OID，与 regtype 同策略。
	24:   decodeRegproc,       // regproc（函数名）
	2202: decodeRegprocedure,  // regprocedure（name(argtypes)）
	2203: decodeRegoper,       // regoper（算符名）
	2204: decodeRegoperator,   // regoperator（name(left,right) 等）
	2205: decodeRegclass,      // regclass（v1.0.47：输出关系名，pg_class 反查）
	2206: decodeRegtype,       // regtype（v1.2.13：输出类型名，pg_type 反查）
	3734: decodeRegconfig,     // regconfig（全文搜索配置名）
	3769: decodeRegdictionary, // regdictionary（全文搜索词典名）
	4089: decodeRegnamespace,  // regnamespace（v1.2.13：输出 schema 名）
	4096: decodeRegrole,       // regrole（rolname 原文）
	4191: decodeRegcollation,  // regcollation（collation 名）
	// v1.2.2 修复（BUG-13）：aclitem 标量列必须走 decodeAclitem。
	// 此前 Decoders 缺本条目：decodeAclitem 早已实现并被 arrayElemText 的
	// "aclitem" 分支用于 aclitem[]（oid 1034）元素，但标量 aclitem（1033）
	// 落到 DecodeDefault → 整列导出 \x 十六进制（实机 V8R6C9B14/V9R1C10：
	// "\x0a0000000a0000007f100000"），回灌 aclitem_in 报 invalid input syntax。
	// 表现为「同表里 al aclitem[] 正常、a aclitem 乱码」的分裂症状。
	ACLITEMOID: decodeAclitem,                                         // aclitem（标量；位 12 按引擎 'p'/'s'）
	3904:       func(B []byte) string { return decodeRange(B, 3904) }, // int4range
	3926:       func(B []byte) string { return decodeRange(B, 3926) }, // int8range
	3906:       func(B []byte) string { return decodeRange(B, 3906) }, // numrange
	3912:       func(B []byte) string { return decodeRange(B, 3912) }, // daterange
	3908:       func(B []byte) string { return decodeRange(B, 3908) }, // tsrange
	3910:       func(B []byte) string { return decodeRange(B, 3910) }, // tstzrange
	// ---- PG14+ multirange（v1.0.63 修复 A-6）----
	4451: func(B []byte) string { return decodeMultiRange(B, 3904) }, // int4multirange
	4532: func(B []byte) string { return decodeMultiRange(B, 3906) }, // nummultirange
	4533: func(B []byte) string { return decodeMultiRange(B, 3908) }, // tsmultirange
	4534: func(B []byte) string { return decodeMultiRange(B, 3910) }, // tstzmultirange
	4535: func(B []byte) string { return decodeMultiRange(B, 3912) }, // datemultirange
	4536: func(B []byte) string { return decodeMultiRange(B, 3926) }, // int8multirange
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

// oracleEmptyCastSuffix：金仓 oracle 模式下"真空串"需要补的类型转换后缀。
//
// v1.2.16 新增（回归报告 D6）。oracle 模式的 ora_input_emptystr_isnull 把**空字符串
// 字面量**直接变成 NULL，所以 '' / ''::text / ''::varbit 回灌后全是 NULL，
// 源库里真实存在的空串（length=0 且 not null）会静默变成 NULL。
// 必须用非字面量构造空串：convert_from('\x'::bytea, 'UTF8')——'\x' 是两个字符，
// 不触发该规则，bytea 按 hex 解析为空字节串，再转成 UTF8 文本即真空串。
// 实测 V9R1C10：length()=0 且 is null=false，写入 text / varchar(n) / varbit
// 列后仍为空串（见回归用例）。
var oracleEmptyCastSuffix = map[uint32]string{
	TEXTOID:    "",
	VARCHAROID: "",
	BPCHAROID:  "",
	NAMEOID:    "",
	8014:       "", // 金仓 clob（textin/textout）
	BITOID:     "::bit",
	VARBITOID:  "::varbit",
}

// OracleEmptyStringExpr：空串在 oracle 模式下的安全写法；非 oracle 模式或
// 非字符串类类型返回 ok=false（调用方照旧输出 ''）。
// 域类型沿 typbasetype 解析——列是 "domain over text" 时按 text 处理。
func OracleEmptyStringExpr(oid uint32) (string, bool) {
	if !kbOracleMode {
		return "", false
	}
	suffix, ok := oracleEmptyCastSuffixOf(oid, 0)
	if !ok {
		return "", false
	}
	return "convert_from('\\x'::bytea, 'UTF8')" + suffix, true
}

func oracleEmptyCastSuffixOf(oid uint32, depth int) (string, bool) {
	if s, ok := oracleEmptyCastSuffix[oid]; ok {
		return s, true
	}
	if depth < 8 {
		if td, ok := DynamicTypeDefs[oid]; ok && td != nil && td.Type == 'd' && td.Base != 0 {
			return oracleEmptyCastSuffixOf(td.Base, depth+1)
		}
	}
	return "", false
}

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
