// Author: raysuen
package charset

import (
	"encoding/hex"
	"fmt"
	"os"
	"pg2sql/internal/logx"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

// ---------- 库文本编码 ----------
// 默认 UTF-8；非 UTF-8 库（LATIN1/GB18030/GBK/SQL_ASCII 等）按库编码解码，
// v1.0.52：textEncoding 由普通全局变量改为 atomic.Value——批量 --jobs>1 时
// 各表 worker 并发跑 exportOneTable，表级编码精化（Per-table override）会在
// worker 内 SetTextEncoding，普通变量存在数据竞争（go test -race 检出）。
var textEncodingStore atomic.Value

func init() { textEncodingStore.Store("utf-8") }

func SetTextEncoding(enc string) {
	if enc == "" {
		enc = "utf-8"
	}
	textEncodingStore.Store(enc)
}

// CurTextEncoding：当前文本编码（并发安全读）。
func CurTextEncoding() string {
	if v, ok := textEncodingStore.Load().(string); ok && v != "" {
		return v
	}
	return "utf-8"
}

// DecodeBytes：按库编码解码字节；失败时 latin-1 逐字节兜底（字节可逆，绝不丢失）。
// 返回的 string 始终是合法 UTF-8（与 Python 版行为一致：源字节映射为 U+0080-U+00FF）。
// B4 修复：补齐 EUC-CN/EUC-JP/EUC-KR/EUC-TW/EUC-JIS-2004/MULE_INTERNAL 处理，
// 未实现解码的编码告警一次后按 \x hex 输出（字节可逆，不再静默 Latin-1 损坏）
var warnUnsupportedEnc sync.Once

func DecodeBytes(raw []byte) string {
	// v1.2.6（P3）：先归一化再分派——任何调用方绕过 normalizeEncoding 直接
	// SetTextEncoding("euc_cn") 也不会落入 default 分支静默 latin-1 乱码。
	textEncoding := NormalizeEncodingName(CurTextEncoding())
	switch textEncoding {
	case "latin1", "latin-1", "iso8859-1", "iso-8859-1", "sql_ascii", "sql-ascii":
		return latin1ToUTF8(raw)
	case "utf-8", "utf8", "unicode":
		if utf8.Valid(raw) {
			return string(raw)
		}
		return utf8Fallback(raw)
	case "gbk", "gb2312":
		// GBK 解码（双字节码表全覆盖 GBK 汉字区）
		return gbkDecode(raw)
	case "euc-cn":
		// B4：EUC-CN(GB2312) 双字节区是 GBK 码表子集，用 GBK 解码无损
		return gbkDecode(raw)
	case "euc-jp", "euc-kr", "euc-tw", "euc-jis-2004", "mule-internal",
		"sjis", "big5", "uhc", "johab", "shift-jis-2004":
		// B4：暂无完整码表实现；告警一次 + hex 可逆输出（不静默损坏）
		// v1.0.56：sjis/big5/uhc/johab/shift-jis-2004 为 pg_enc 35/36/38/40/41，
		// 旧版误映射到 gbk/utf-8/gb18030/latin1 导致静默损坏，现改为显式不支持
		warnUnsupportedEnc.Do(func() {
			logx.Logf("警告: 编码 %s 未实现解码，非 UTF-8 字节按 \\x hex 输出（字节可逆）", textEncoding)
		})
		if utf8.Valid(raw) {
			return string(raw)
		}
		return "\\x" + hex.EncodeToString(raw)
	case "gb18030":
		// GB18030 完整解码：双字节（GBK 表 + 扩展表）+ 四字节（低区查表 + 高区线性）
		return decodeGB18030(raw)
	default:
		// v1.2.13：单字节编码族（LATIN2-10 / WIN125x / WIN866 / WIN874 / KOI8R /
		// KOI8U / ISO_8859_5-8）按各自码表解码。必须先于 utf8.Valid 判定——
		// 否则高位字节恰好构成合法 UTF-8 序列时会被误当 UTF-8 直通。
		if tbl, ok := sbTables[textEncoding]; ok {
			return singleByteToUTF8(raw, textEncoding, tbl)
		}
		if utf8.Valid(raw) {
			return string(raw)
		}
		return utf8Fallback(raw)
	}
}

// singleByteUndefBytes：单字节编码里"该字节在该编码中未定义"的出现次数。
// v1.2.13：这类字节回退 U+00XX（字节可逆，不丢信息），但必须让用户知道
// 有字节没能按源编码解释，否则又是一次静默近似。
var singleByteUndefBytes int64
var singleByteUndefEnc sync.Map // 编码名 → 计数（用于告警时点名）

// singleByteToUTF8：按 [128]rune 码表解码单字节编码（0x80-0xFF 查表，ASCII 直通）。
// 未定义字节回退 U+00XX 并计数，由 SingleByteUndefSummary 在导出收尾告警。
func singleByteToUTF8(raw []byte, encName string, tbl *[128]rune) string {
	if !hasHighByte(raw) {
		return string(raw) // 纯 ASCII：任何单字节编码都与 ASCII 一致
	}
	out := make([]byte, 0, len(raw)*3)
	for _, c := range raw {
		if c < 0x80 {
			out = append(out, c)
			continue
		}
		r := tbl[c-0x80]
		if r < 0 {
			// 未定义字节 → U+00XX（与 latin-1 兜底同款，字节可逆）
			out = append(out, 0xC0|c>>6, 0x80|c&0x3F)
			atomic.AddInt64(&singleByteUndefBytes, 1)
			if v, ok := singleByteUndefEnc.Load(encName); ok {
				singleByteUndefEnc.Store(encName, v.(int64)+1)
			} else {
				singleByteUndefEnc.Store(encName, int64(1))
			}
			continue
		}
		out = append(out, []byte(string(r))...)
	}
	return string(out)
}

// SingleByteUndefSummary：导出收尾汇总（无异常返回空串；调用后清零）。
func SingleByteUndefSummary() string {
	n := atomic.SwapInt64(&singleByteUndefBytes, 0)
	if n == 0 {
		return ""
	}
	var encs []string
	singleByteUndefEnc.Range(func(k, v any) bool {
		encs = append(encs, k.(string))
		singleByteUndefEnc.Delete(k)
		return true
	})
	sort.Strings(encs)
	return fmt.Sprintf("警告: %d 个字节在源库编码中被定义为未使用码位（编码: %s），"+
		"已按 U+00XX 原样保留（字节可逆）；若目标库同为该编码，回灌这些字符会报"+
		"「no equivalent」，请核对源数据是否本身含非法字节", n, strings.Join(encs, ", "))
}

// latin1ToUTF8：每字节直接映射 U+0000-U+00FF
func latin1ToUTF8(raw []byte) string {
	if utf8.Valid(raw) && !hasHighByte(raw) {
		return string(raw)
	}
	out := make([]byte, 0, len(raw)*2)
	for _, c := range raw {
		if c < 0x80 {
			out = append(out, c)
		} else {
			out = append(out, 0xC0|c>>6, 0x80|c&0x3F)
		}
	}
	return string(out)
}

func hasHighByte(raw []byte) bool {
	for _, c := range raw {
		if c >= 0x80 {
			return true
		}
	}
	return false
}

// utf8Fallback：非法 UTF-8 序列逐字节 latin-1 兜底
func utf8Fallback(raw []byte) string {
	if utf8.Valid(raw) {
		return string(raw)
	}
	out := make([]byte, 0, len(raw)*2)
	i := 0
	for i < len(raw) {
		r, size := utf8.DecodeRune(raw[i:])
		// v1.0.63 修复（C-5）：合法的 U+FFFD（EF BF BD）经 DecodeRune 返回
		// (RuneError, 3)，旧实现把它当非法序列逐字节拆成 "ï¿½"（3 个假字符）。
		// 只有 size==1 才是真正的非法字节（UTF-8 最短合法序列长度 >=1，
		// RuneError 且 size==1 表示该位置起不构成任何合法序列）。
		if r == utf8.RuneError && size <= 1 {
			c := raw[i]
			if c < 0x80 {
				out = append(out, c)
			} else {
				// 非法单字节 → U+00XX（latin-1，字节可逆）
				out = append(out, 0xC0|c>>6, 0x80|c&0x3F)
			}
			i++
			continue
		}
		out = append(out, raw[i:i+size]...)
		i += size
	}
	return string(out)
}

// ---------- 编码名归一化与白名单（v1.2.6，P3） ----------
//
// 缺陷背景：--encoding euc_cn（下划线拼写，README 老文档写法）原实现归一化不识别、
// 原样穿透 SetTextEncoding → DecodeBytes 无 euc_cn 分支落入 default → 非 UTF-8
// 字节走 utf8Fallback 逐字节 Latin-1 静默乱码，全程无告警（实测 euc_cn→"½ð²Ö"、
// euc-cn→"金仓"）。自动探测路径（pg_enc 映射）产出的都是规范名不受影响，
// 仅手动拼写错名/别名时触发。
//
// 修复两层：① NormalizeEncodingName 补齐下划线别名归一；② IsSupportedEncoding
// 白名单供 cli 解析期拦截未知名（把静默乱码变成显式报错，与 v1.2.5 参数收口同风格）。

// NormalizeEncodingName：把用户输入/内部编码名归一为 DecodeBytes 可识别的规范名。
// 与 app.normalizeEncoding 语义一致（app 已改为委托本函数，消除两处映射漂移）。
func NormalizeEncodingName(enc string) string {
	e := strings.ToLower(strings.TrimSpace(enc))
	switch e {
	case "utf8", "utf-8", "unicode", "utf_8":
		return "utf-8"
	case "latin1", "latin-1", "iso8859-1", "iso-8859-1", "latin_1", "sql_ascii", "sql-ascii":
		return "latin1"
	case "gbk", "gb2312", "936":
		return "gbk"
	case "gb18030":
		return "gb18030"
	// v1.2.6（P3）：补齐下划线别名（PG 服务端编码名的下划线拼写形式）
	case "euc_cn":
		return "euc-cn"
	case "euc_jp":
		return "euc-jp"
	case "euc_kr":
		return "euc-kr"
	case "euc_tw":
		return "euc-tw"
	case "euc_jis_2004":
		return "euc-jis-2004"
	case "shift_jis_2004":
		return "shift-jis-2004"
	case "mule_internal":
		return "mule-internal"
	// v1.2.13：单字节族别名（PG 官方名 ISO_8859_5 经 ToLower 后是 iso_8859_5；
	// 用户也常写 iso-8859-5 / iso8859-5，统一归一到表键 iso8859-5）
	case "iso_8859_5", "iso8859_5", "iso-8859-5":
		return "iso8859-5"
	case "iso_8859_6", "iso8859_6", "iso-8859-6":
		return "iso8859-6"
	case "iso_8859_7", "iso8859_7", "iso-8859-7":
		return "iso8859-7"
	case "iso_8859_8", "iso8859_8", "iso-8859-8":
		return "iso8859-8"
	}
	return e
}

// supportedEncodings：DecodeBytes 明确处理的全部规范名（含"告警 + \x hex 可逆输出"
// 的暂不支持多字节族——它们行为显式、不静默损坏，属于受支持范畴）。
var supportedEncodings = map[string]bool{
	"utf-8": true, "latin1": true, "gbk": true, "gb18030": true,
	"euc-cn": true, "euc-jp": true, "euc-kr": true, "euc-tw": true,
	"euc-jis-2004": true, "shift-jis-2004": true, "mule-internal": true,
	"sjis": true, "big5": true, "uhc": true, "johab": true,
	// v1.2.13：单字节族（pg_enc 9-34），按各自码表解码
	"latin2": true, "latin3": true, "latin4": true, "latin5": true,
	"latin6": true, "latin7": true, "latin8": true, "latin9": true,
	"latin10": true, "win1250": true, "win1251": true, "win1252": true,
	"win1253": true, "win1254": true, "win1255": true, "win1256": true,
	"win1257": true, "win1258": true, "win866": true, "win874": true,
	"koi8r": true, "koi8u": true,
	"iso8859-5": true, "iso8859-6": true, "iso8859-7": true, "iso8859-8": true,
}

// IsSupportedEncoding：判断（未归一的）编码名是否受支持。未知名返回 false，
// 调用方（cli 解析期）应显式报错而非静默兜底。
func IsSupportedEncoding(enc string) bool {
	return supportedEncodings[NormalizeEncodingName(enc)]
}

// gbkDecode：简易 GBK 双字节解码（含单字节 ASCII；无映射时按 latin-1 兜底）
func gbkDecode(raw []byte) string {
	out := make([]byte, 0, len(raw)*2)
	i := 0
	for i < len(raw) {
		c := raw[i]
		if c < 0x80 {
			out = append(out, c)
			i++
			continue
		}
		if i+1 < len(raw) {
			hi, lo := c, raw[i+1]
			if r, ok := gbkMap[uint16(hi)<<8|uint16(lo)]; ok {
				out = append(out, []byte(string(r))...)
				i += 2
				continue
			}
		}
		out = append(out, 0xC0|c>>6, 0x80|c&0x3F)
		i++
	}
	return string(out)
}

// decodeGB18030：GB18030 完整解码
// 单字节：0x00-0x7F；双字节：b1∈81-FE × b2∈40-FE(≠7F)，查 gbkMap + gb18030ExtMap；
// 四字节：b1∈81-FE × b2∈30-39 × b3∈81-FE × b4∈30-39，
//
//	序号 N=((b1-0x81)*10+(b2-0x30))*1260+(b3-0x81)*10+(b4-0x30)
//	低区 N<39420 查 gb18030LowSegs（U+0080-U+FFFF 无双字节映射码点）；
//	高区 189000<=N<=1237575 → U+ = N-189000+0x10000（U+10000-U+10FFFF）；
//	其余序号为预留/非法，逐字节 latin-1 兜底（字节可逆）。
func decodeGB18030(raw []byte) string {
	out := make([]byte, 0, len(raw)*2)
	i := 0
	for i < len(raw) {
		c := raw[i]
		if c < 0x80 {
			out = append(out, c)
			i++
			continue
		}
		if i+1 >= len(raw) {
			out = append(out, 0xC0|c>>6, 0x80|c&0x3F)
			i++
			continue
		}
		hi, lo := c, raw[i+1]
		// v1.0.63 修复（C-6）：b2∈30-39 说明这是一个四字节序列的开头——
		// 若剩余不足 4 字节（序列在末尾被截断），必须整体按 latin-1 兜底，
		// 否则会把 b1b2 当双字节查表（必然落空）→ 吞 1 字节后剩余按 ASCII 输出，
		// 例如 \x81\x30\x81 被解成 "0"（信息丢失且不可逆）。
		if lo >= 0x30 && lo <= 0x39 && i+4 > len(raw) {
			// v1.0.65 修复（F6）：截断兜底必须对 ASCII 字节（0x00-0x7F）原样直通。
			// 旧实现统一套 0xC0|c>>6, 0x80|c&0x3F——ASCII 字节会变成 C0/C1 开头的
			// 两个字节，C0/C1 是非法 UTF-8 前导，违反本函数"输出恒为合法 UTF-8"
			// 的契约，且 U+00B0 反向映射回 0xB0 而非原字节（不可逆）。
			for ; i < len(raw); i++ {
				c := raw[i]
				if c < 0x80 {
					out = append(out, c)
				} else {
					out = append(out, 0xC0|c>>6, 0x80|c&0x3F)
				}
			}
			break
		}
		// 四字节候选：b2∈30-39 且剩余 ≥4 字节且 b3∈81-FE、b4∈30-39
		if lo >= 0x30 && lo <= 0x39 && i+4 <= len(raw) {
			b3, b4 := raw[i+2], raw[i+3]
			if b3 >= 0x81 && b3 <= 0xFE && b4 >= 0x30 && b4 <= 0x39 {
				N := (int(c)-0x81)*12600 + (int(lo)-0x30)*1260 + (int(b3)-0x81)*10 + (int(b4) - 0x30)
				var r rune
				ok := false
				if N >= 189000 && N <= 1237575 {
					r, ok = rune(N-189000+0x10000), true
				} else if N < 39420 {
					r, ok = gb18030LowLookup(N)
				}
				if ok {
					out = append(out, []byte(string(r))...)
					i += 4
					continue
				}
			}
		}
		// 双字节：GBK 表优先，GB18030 扩展表兜底
		code := uint16(hi)<<8 | uint16(lo)
		if r, ok := gbkMap[code]; ok {
			out = append(out, []byte(string(r))...)
			i += 2
			continue
		}
		if r, ok := gb18030ExtMap[code]; ok {
			out = append(out, []byte(string(r))...)
			i += 2
			continue
		}
		out = append(out, 0xC0|c>>6, 0x80|c&0x3F)
		i++
	}
	return string(out)
}

// 编码 ID → codec（上游 PG pg_wchar.h pg_enc 枚举）
// 仅适用于 PostgreSQL 数据目录：上游 PG 的服务端编码止于 34（KOI8U），
// 35–41（SJIS/BIG5/GBK/UHC/GB18030/JOHAB/SHIFT_JIS_2004）为"仅客户端编码"，
// 不可能出现在 pg_database.encoding 里——本表 35+ 条目只为语义完备。
// 金仓数据目录必须用 EncodingIDToCodecKB（金仓重排了 35–44）。
//
// B4 修复：补齐 PG 标准 EUC_JP(1)/EUC_CN(2)/EUC_KR(3)/EUC_TW(4)/EUC_JIS_2004(5)/MULE_INTERNAL(7)，
// 避免缺失映射落入 utf8Fallback 逐字节 Latin-1 静默损坏数据。
// v1.0.56 修复（P2-G2）：把 35–41 按上游语义校正；单字节族（9~34）latin1 映射
// 为逐字节 1:1 可逆的近似显示，维持不变；多字节不支持族报真实编码名，
// 由 DecodeBytes 走"告警 + \x hex 可逆输出"。
//
// v1.2.13 修复（P1）：上一行的"近似显示"只在导回 LATIN1 时成立——9~34 全部映射
// latin1 意味着 LATIN2-10 / WIN125x / WIN866 / WIN874 / KOI8R / KOI8U / ISO_8859_5-8
// 在 0x80-0xFF 区静默乱码（latin1 与它们并不相同）。实测 WIN1252 库 0x80（€）
// 被解成 U+0080，回灌直接报 no equivalent in encoding "WIN1252"。
// 现全部改为真实编码名，由 singleByteToUTF8 按码表解码（表见 singlebyte_tables.go）。
// 编号以 PG17 `pg_encoding_to_char` 实测为准（21=WIN874，24=WIN1252，23=WIN1251）。
// v1.0.58 修复（P1）：P2-G2 校正对上游正确，但金仓在 35–44 用自有服务端
// 编码编号（V8R6C9B14 以 pg_encoding_to_char 实测：35=GBK、36=UNICODE、
// 37=UTF8MB4、38=GB2312、39=GB18030、40=SJIS、41=BIG5、42=UHC、43=JOHAB、
// 44=SHIFT_JIS_2004），导致金仓 GBK 库（enc=35）被当 sjis 处理、数据转义为
// E'\x…' hex 文本。金仓路径改走 EncodingIDToCodecKB，本表保留上游语义。
var EncodingIDToCodec = map[int]string{
	0: "sql_ascii", 1: "euc-jp", 2: "euc-cn", 3: "euc-kr", 4: "euc-tw",
	5: "euc-jis-2004", 6: "utf-8", 7: "mule-internal",
	8: "latin1",
	// v1.2.13：9-34 单字节族按真实编码名解码（此前统一 latin1，非 LATIN1 全错）
	9:  "latin2",    // LATIN2  (ISO-8859-2)
	10: "latin3",    // LATIN3  (ISO-8859-3)
	11: "latin4",    // LATIN4  (ISO-8859-4)
	12: "latin5",    // LATIN5  (ISO-8859-9)
	13: "latin6",    // LATIN6  (ISO-8859-10)
	14: "latin7",    // LATIN7  (ISO-8859-13)
	15: "latin8",    // LATIN8  (ISO-8859-14)
	16: "latin9",    // LATIN9  (ISO-8859-15)
	17: "latin10",   // LATIN10 (ISO-8859-16)
	18: "win1256",   // WIN1256
	19: "win1258",   // WIN1258
	20: "win866",    // WIN866
	21: "win874",    // WIN874（注意：不是 WIN1252）
	22: "koi8r",     // KOI8R
	23: "win1251",   // WIN1251
	24: "win1252",   // WIN1252
	25: "iso8859-5", // ISO_8859_5
	26: "iso8859-6", // ISO_8859_6
	27: "iso8859-7", // ISO_8859_7
	28: "iso8859-8", // ISO_8859_8
	29: "win1250",   // WIN1250
	30: "win1253",   // WIN1253
	31: "win1254",   // WIN1254
	32: "win1255",   // WIN1255
	33: "win1257",   // WIN1257
	34: "koi8u",     // KOI8U
	35: "sjis",
	36: "big5", 37: "gbk", 38: "uhc", 39: "gb18030", 40: "johab",
	41: "shift-jis-2004", 42: "utf-8", 43: "utf-8", 44: "utf-8", 45: "utf-8",
	46: "utf-8", 47: "utf-8", 48: "utf-8",
}

// EncodingIDToCodecKB：金仓（KingbaseES）库编码映射。
// 金仓把 GBK/GB18030 等提升为服务端编码并重排 pg_enc 35–44（与上游仅客户端
// 编号 35–41 完全不同），pg_database.encoding 落在该区间的只能是金仓。
// 0–34 与上游一致（UTF8=6、EUC_CN=2、LATIN1=8 已由 PG/金仓矩阵回归背书）。
// 编号以金仓 V8R6C9B14 `select i, pg_encoding_to_char(i)` 实测为准；
// SJIS/BIG5/UHC/JOHAB/SHIFT_JIS_2004 虽被金仓编号但非服务端编码（文档标注
// Server=No），映射保留以走"告警 + \x hex 可逆输出"兜底，不会静默损坏。
var EncodingIDToCodecKB = func() map[int]string {
	m := make(map[int]string, len(EncodingIDToCodec)+3)
	for k, v := range EncodingIDToCodec {
		m[k] = v
	}
	// 金仓自有服务端编码段（35–44）
	m[35] = "gbk"     // GBK（上游 35=SJIS，金仓重排——v1.0.57 及之前误判为 sjis）
	m[36] = "utf-8"   // UNICODE（金仓对 UTF8 的别名）
	m[37] = "utf-8"   // UTF8MB4
	m[38] = "euc-cn"  // GB2312（GBK 码表子集，DecodeBytes euc-cn 分支按 GBK 解码无损）
	m[39] = "gb18030" // GB18030（与上游编号巧合一致）
	m[40] = "sjis"
	m[41] = "big5"
	m[42] = "uhc"
	m[43] = "johab"
	m[44] = "shift-jis-2004"
	// v1.0.60 修复（P2-1）：删除从上游表继承的 45–48=utf-8 兜底。金仓 V8R6C9B14
	// 只定义到 44；继承的 45–48 意味着未来金仓新增编号会被静默按 UTF-8 解码
	// （同族复发点）。缺号 → DetectDatabaseEncoding 命中 miss → 显式告警。
	for _, k := range []int{45, 46, 47, 48} {
		delete(m, k)
	}
	return m
}()

// utf8SeqStats：评估字节流 UTF8 多字节序列（合法数/候选数/合法序列数）。
// 判定逻辑与 v1.0.42/v1.0.43 逐字节一致（含 E0/ED/F0/F4 边界收紧），仅作公共内核抽取。
func utf8SeqStats(buf []byte) (valid, total, seq int) {
	for i := 0; i < len(buf); {
		b := buf[i]
		if b < 0x80 {
			i++
			continue
		}
		var need int
		switch {
		case b >= 0xF0 && b <= 0xF4:
			need = 3
		case b >= 0xE0 && b <= 0xEF:
			need = 2
		case b >= 0xC2 && b <= 0xDF:
			need = 1
		default:
			i++ // GBK 首字节多落于此：非法 UTF8 首字节，跳过
			continue
		}
		if i+need >= len(buf) {
			break
		}
		ok := true
		for j := 1; j <= need; j++ {
			if buf[i+j] < 0x80 || buf[i+j] > 0xBF {
				ok = false
				break
			}
		}
		total++
		if ok {
			// 收紧边界防伪序列：E0 后须 ≥A0；ED 后须 ≤9F；F0 后须 ≥90；F4 后须 ≤8F
			if b == 0xE0 && buf[i+1] < 0xA0 {
				ok = false
			}
			if b == 0xED && buf[i+1] > 0x9F {
				ok = false
			}
			if b == 0xF0 && buf[i+1] < 0x90 {
				ok = false
			}
			if b == 0xF4 && buf[i+1] > 0x8F {
				ok = false
			}
			if ok {
				valid++
				seq++
			}
		}
		i += need + 1
	}
	return
}

// gbkSeqStats：评估字节流 GBK 双字节序列合法率（首字节 0x81-0xFE，次字节 0x40-0xFE 且非 0x7F）。
// 仅用于单文件三态分类（边界①）：UTF8 判定不达标时，真 GBK 双字节流在此合法率极高。
func gbkSeqStats(buf []byte) (valid, total int) {
	for i := 0; i < len(buf); {
		b := buf[i]
		if b < 0x80 {
			i++
			continue
		}
		if b >= 0x81 && b <= 0xFE {
			total++
			if i+1 < len(buf) {
				lo := buf[i+1]
				if (lo >= 0x40 && lo <= 0xFE) && lo != 0x7F {
					valid++
					i += 2
					continue
				}
			}
		}
		i++
	}
	return
}

// ProbeFileUTF8Ratio：对单个数据文件末尾一页采样，判定是否呈 UTF8 字节特征。
// 与 probeDirUtf8Ratio 同一判定内核（v1.0.44 抽取复用；读文件末尾一页为 v1.0.43 行为）。
func ProbeFileUTF8Ratio(path string, pageSize int) bool {
	if path == "" || pageSize < 8192 {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	tmp := make([]byte, pageSize)
	off := fi.Size() - int64(pageSize)
	if off < 0 {
		off = 0
	}
	n, _ := f.ReadAt(tmp, off)
	if n <= 0 {
		return false
	}
	valid, total, seq := utf8SeqStats(tmp[:n])
	if seq < 4 || total == 0 {
		return false // 纯 ASCII 或采样不足
	}
	// 阈值 0.7：真实 UTF8 数据页含 int/timestamp/tuple 头等二进制字段，合法率通常 0.85+；
	// 真 GBK/GB18030/EUC_CN 双字节流合法率普遍 <0.5，可明确区分
	return float64(valid)/float64(total) >= 0.7
}

// EncodingVote：表级多页投票的明细结果（v1.2.7，F2）。
// 旧接口只返回结论，调用方无法区分「一致度高」与「仅 1 页命中」——后者在加密表、
// 压缩页、单页小表上极易随机发生，一旦据此覆盖库级判定，整表中文会被静默解成乱码。
type EncodingVote struct {
	Enc      string // 最终结论："utf8"/"gbk"；证据不足时为空串
	WinEnc   string // 得票最多的一方（无论是否达到采用阈值）
	WinVotes int    // 胜出方票数
	Total    int    // 有效票数（UTF8 票 + GBK 票）
}

// ProbeFileEncodingVote：多页投票 + 证据强度（v1.2.7）。
//
// 采用门槛（v1.2.7 收紧，F2）：
//   - 有效票 ≥ 2 票：单票不采用（加密表页内容近似随机、压缩页、单页小表都可能
//     凭一页噪声命中 GBK 特征，实测 TDE 表 t_tsp 因此被判 GBK，md5 DIFF）；
//   - 胜出方票数 ≥ 2 且 票数占比 ≥ 2/3：页间矛盾时保守不改（与 v1.0.54 一致）。
//
// 未达门槛时 Enc 为空、但 WinEnc/WinVotes/Total 仍带回，供调用方输出「证据不足」
// 级别的人工提示（而不是静默覆盖）。
func ProbeFileEncodingVote(path string, pageSize int) EncodingVote {
	var v EncodingVote
	if path == "" || pageSize < 8192 {
		return v
	}
	f, err := os.Open(path)
	if err != nil {
		return v
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return v
	}
	total := int(fi.Size() / int64(pageSize))
	if total < 1 {
		return v
	}
	const maxSamples = 16
	step := 1
	if total > maxSamples {
		step = total / maxSamples
	}
	buf := make([]byte, pageSize)
	nUTF8, nGBK := 0, 0
	sampled := 0
	for p := 0; p < total && sampled < maxSamples; p += step {
		n, _ := f.ReadAt(buf, int64(p)*int64(pageSize))
		if n < pageSize {
			break
		}
		pg := buf[:n]
		if zeroPage(pg) {
			continue // 空闲页无文本特征
		}
		sampled++
		if uv, ut, us := utf8SeqStats(pg); us >= 4 && ut > 0 && float64(uv)/float64(ut) >= 0.7 {
			nUTF8++
			continue
		}
		if gv, gt := gbkSeqStats(pg); gt >= 8 && float64(gv)/float64(gt) >= 0.85 {
			nGBK++
		}
	}
	v.Total = nUTF8 + nGBK
	if v.Total < 1 {
		return v
	}
	switch {
	case nUTF8 > nGBK:
		v.WinEnc, v.WinVotes = "utf8", nUTF8
	case nGBK > nUTF8:
		v.WinEnc, v.WinVotes = "gbk", nGBK
	default:
		return v // 1:1 平票 = 页间矛盾，保守不改
	}
	// 整数比较避免浮点误差：win/total >= 2/3  ⇔  win*3 >= total*2
	if v.WinVotes < 2 || v.WinVotes*3 < v.Total*2 {
		return v // 证据不足：保留 WinEnc 供调用方提示，但不给结论
	}
	v.Enc = v.WinEnc
	return v
}

// ProbeFileEncodingMajority：单文件三态编码分类的「多页投票」版（v1.0.54）。
//
// 背景（C4）：库级 probeDirUtf8Ratio 只采样全库最大的 3 个文件、任一呈 UTF8 特征
// 就把整库按 UTF8 解码；混合存储库（金仓「标 GBK 实存 UTF8」与真 GBK 表共存）里
// 真 GBK 表会被整表按 UTF8 解成乱码，而旧的表级精化在库级已判 UTF8 时直接跳过。
//
// 本函数对目标表自身数据文件均匀采样最多 16 页逐页投票：真 GBK 表所有页一致判 gbk，
// 真 UTF8 表一致判 utf8，压缩页等噪声页因互相矛盾而落到 unknown（保守不动）。
// 返回 "utf8" / "gbk" / ""（无法判定或证据不足，门槛见 ProbeFileEncodingVote）。
func ProbeFileEncodingMajority(path string, pageSize int) string {
	return ProbeFileEncodingVote(path, pageSize).Enc
}

// ProbeFileEncoding：单文件三态编码分类（v1.0.44 边界①——脱离数据根的单文件模式兜底）。
// 返回 "utf8" / "gbk" / "unknown"。
//   - UTF8 特征达标 → utf8（V8 实存 UTF8、PG UTF8 声明文件均命中）
//   - UTF8 不达标但 GBK 双字节特征达标 → gbk（V9 实存真 GBK 命中）
//   - 纯 ASCII / 二进制大字段 / 无特征 → unknown（调用方默认 utf8 并给出警告）
//
// 注意顺序：UTF8 优先。UTF8 中文流在 GBK 判定下也呈高合法率（0xE0-0xEF 恰在 GBK 首字节区），
// 故不能以"GBK 达标"单独判 gbk；真 GBK 流 UTF8 合法率 <0.5，顺序判定可严格区分。
func ProbeFileEncoding(path string, pageSize int) string {
	if path == "" || pageSize < 8192 {
		return "unknown"
	}
	f, err := os.Open(path)
	if err != nil {
		return "unknown"
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return "unknown"
	}
	if fi.Size() < 1024 {
		return "unknown"
	}
	tmp := make([]byte, pageSize)
	off := fi.Size() - int64(pageSize)
	if off < 0 {
		off = 0
	}
	n, _ := f.ReadAt(tmp, off)
	if n <= 0 {
		return "unknown"
	}
	uv, ut, us := utf8SeqStats(tmp[:n])
	utf8OK := us >= 4 && ut > 0 && float64(uv)/float64(ut) >= 0.7
	if utf8OK {
		return "utf8"
	}
	// v1.0.52 收紧 GBK 判据：随机二进制（bytea/geometry 等压缩/密文列）双字节
	// 合法率天然 ≈0.74（首字节 0x81-0xFE 占比 × 尾字节 0x40-0xFE 合法率），
	// 旧阈值 0.7 会把纯二进制页误判为 GBK。真实 GBK 文本页合法率 ≥0.95（几乎
	// 所有候选对都成字），0.85 + 最少 8 对可稳定区分。
	gv, gt := gbkSeqStats(tmp[:n])
	if gt >= 8 && float64(gv)/float64(gt) >= 0.85 {
		return "gbk"
	}
	return "unknown"
}

// zeroPage：整页全零判断（探测用；本地实现以维持 charset 不依赖 engine 的分层）。
func zeroPage(buf []byte) bool {
	for _, b := range buf {
		if b != 0 {
			return false
		}
	}
	return true
}
