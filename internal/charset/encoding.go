// Author: raysuen
package charset

import (
	"encoding/hex"
	"os"
	"pg2sql/internal/logx"
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
	textEncoding := CurTextEncoding()
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
		if utf8.Valid(raw) {
			return string(raw)
		}
		return utf8Fallback(raw)
	}
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
		if r != utf8.RuneError || size == 1 {
			if r == utf8.RuneError && size == 1 && raw[i] < 0x80 {
				out = append(out, raw[i])
			} else if r == utf8.RuneError && size == 1 {
				// 非法单字节 → U+00XX
				c := raw[i]
				out = append(out, 0xC0|c>>6, 0x80|c&0x3F)
			} else {
				out = append(out, raw[i:i+size]...)
			}
			i += size
		} else {
			// 非法序列：逐字节 latin-1 兜底
			c := raw[i]
			out = append(out, 0xC0|c>>6, 0x80|c&0x3F)
			i++
		}
	}
	return string(out)
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
		// 四字节候选：b2∈30-39 且剩余 ≥4 字节且 b3∈81-FE、b4∈30-39
		if lo >= 0x30 && lo <= 0x39 && i+3 < len(raw) {
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
// v1.0.58 修复（P1）：P2-G2 校正对上游正确，但金仓在 35–44 用自有服务端
// 编码编号（V8R6C9B14 以 pg_encoding_to_char 实测：35=GBK、36=UNICODE、
// 37=UTF8MB4、38=GB2312、39=GB18030、40=SJIS、41=BIG5、42=UHC、43=JOHAB、
// 44=SHIFT_JIS_2004），导致金仓 GBK 库（enc=35）被当 sjis 处理、数据转义为
// E'\x…' hex 文本。金仓路径改走 EncodingIDToCodecKB，本表保留上游语义。
var EncodingIDToCodec = map[int]string{
	0: "sql_ascii", 1: "euc-jp", 2: "euc-cn", 3: "euc-kr", 4: "euc-tw",
	5: "euc-jis-2004", 6: "utf-8", 7: "mule-internal",
	8: "latin1", 9: "latin1", 10: "latin1",
	11: "latin1", 12: "latin1", 13: "latin1", 14: "latin1", 15: "latin1",
	16: "latin1", 17: "latin1", 18: "latin1", 19: "latin1", 20: "latin1",
	21: "latin1", 22: "latin1", 23: "latin1", 24: "latin1", 25: "latin1",
	26: "latin1", 27: "latin1", 28: "latin1", 29: "latin1", 30: "latin1",
	31: "latin1", 32: "latin1", 33: "latin1", 34: "latin1", 35: "sjis",
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

// ProbeFileEncodingMajority：单文件三态编码分类的「多页投票」版（v1.0.54）。
//
// 背景（C4）：库级 probeDirUtf8Ratio 只采样全库最大的 3 个文件、任一呈 UTF8 特征
// 就把整库按 UTF8 解码；混合存储库（金仓「标 GBK 实存 UTF8」与真 GBK 表共存）里
// 真 GBK 表会被整表按 UTF8 解成乱码，而旧的表级精化在库级已判 UTF8 时直接跳过。
//
// 本函数对目标表自身数据文件均匀采样最多 16 页逐页投票：真 GBK 表所有页一致判 gbk，
// 真 UTF8 表一致判 utf8，压缩页等噪声页因互相矛盾而落到 unknown（保守不动）。
// 返回 "utf8" / "gbk" / ""（无法判定）。
func ProbeFileEncodingMajority(path string, pageSize int) string {
	if path == "" || pageSize < 8192 {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	total := int(fi.Size() / int64(pageSize))
	if total < 1 {
		return ""
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
	if nUTF8+nGBK < 1 {
		return ""
	}
	if nUTF8 > 2*nGBK {
		return "utf8"
	}
	if nGBK > 2*nUTF8 {
		return "gbk"
	}
	return "" // 票数接近 = 页间矛盾（压缩/二进制居多），保守不改
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
