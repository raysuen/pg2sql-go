// pg2sql binary 层：小端读取、varlena 解析（PG 真实磁盘格式）、TOAST 压缩解压
// 对应 Python 版 pg2sql/binary.py（v2.3），逐字节等价翻译
// Author: raysuen
package main

import (
	"encoding/binary"
	"unicode/utf8"
)

// ---------- 小端读取 ----------
func u16(b []byte, off int) uint16 { return binary.LittleEndian.Uint16(b[off:]) }
func u32(b []byte, off int) uint32 { return binary.LittleEndian.Uint32(b[off:]) }
func i32(b []byte, off int) int32  { return int32(binary.LittleEndian.Uint32(b[off:])) }
func u64(b []byte, off int) uint64 { return binary.LittleEndian.Uint64(b[off:]) }
func i64(b []byte, off int) int64  { return int64(binary.LittleEndian.Uint64(b[off:])) }

func align4(n int) int { return (n + 3) &^ 3 }

// ---------- 库文本编码 ----------
// 默认 UTF-8；非 UTF-8 库（LATIN1/GB18030/GBK/SQL_ASCII 等）按库编码解码，
// 解码失败字节回退 latin-1（逐字节 0x00-0xFF 均可逆，不产生数据损坏）。
var textEncoding = "utf-8"

func setTextEncoding(enc string) {
	if enc == "" {
		enc = "utf-8"
	}
	textEncoding = enc
}

// decodeBytes：按库编码解码字节；失败时 latin-1 逐字节兜底（字节可逆，绝不丢失）。
// 返回的 string 始终是合法 UTF-8（与 Python 版行为一致：源字节映射为 U+0080-U+00FF）。
func decodeBytes(raw []byte) string {
	switch textEncoding {
	case "latin1", "latin-1", "iso8859-1", "iso-8859-1", "sql_ascii", "sql-ascii":
		return latin1ToUTF8(raw)
	case "utf-8", "utf8", "unicode":
		if utf8.Valid(raw) {
			return string(raw)
		}
		return utf8Fallback(raw)
	case "gbk", "gb18030", "gb2312":
		// GBK/GB18030 解码（简易 GBK 双字节表；GB18030 四字节序列按 GBK 降级）
		return gbkDecode(raw)
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

// cstring：读取 C 风格 NUL 结尾字符串
func cstring(b []byte, off int) string {
	end := off
	for end < len(b) && b[end] != 0 {
		end++
	}
	return decodeBytes(b[off:end])
}

// ---------- varlena 核心（PG 真实磁盘格式，小端主机布局） ----------
// VARATT_IS_1B_E:  首字节 == 0x01（外联指针 tag，第二个字节 0x12=VARTAG_ONDISK）
// VARATT_IS_1B:    首字节 & 0x01（短头，VARSIZE = first >> 1，含头最大 127）
// VARATT_IS_4B_U:  首字节 & 0x03 == 0x00（4B 头未压缩）
// VARATT_IS_4B_C:  首字节 & 0x03 == 0x02（4B 头内联压缩）
// VARSIZE_4B:      (小端 u32 >> 2) & 0x3FFFFFFF

const VARTAG_ONDISK = 0x12

const (
	VARLENA_EXTERNAL = "ext"
	VARLENA_1B       = "1b"
	VARLENA_4B       = "4b"
	VARLENA_4BC      = "4bc"
)

// varlenaParse 返回 (kind, totalSize, payloadOff, payloadLen)
func varlenaParse(b []byte, off int) (string, int, int, int) {
	if off >= len(b) {
		return "", 0, 0, 0
	}
	first := b[off]
	// 外联指针优先判定（0x01 也是奇数，必须先判）
	if first == 0x01 {
		if off+2 <= len(b) && b[off+1] == VARTAG_ONDISK {
			return VARLENA_EXTERNAL, 18, off + 2, 16
		}
		return "", 0, 0, 0
	}
	if first&0x01 != 0 {
		total := int(first >> 1)
		if total == 0 {
			return "", 0, 0, 0
		}
		return VARLENA_1B, total, off + 1, total - 1
	}
	if off+4 > len(b) {
		return "", 0, 0, 0
	}
	word := u32(b, off)
	total := int((word >> 2) & 0x3FFFFFFF)
	if total < 4 {
		return "", 0, 0, 0
	}
	if (first & 0x06) == 0x06 {
		return VARLENA_4BC, total, off + 4, total - 4
	}
	return VARLENA_4B, total, off + 4, total - 4
}

// ExternalInfo：TOAST 外联指针解析结果
type ExternalInfo struct {
	RawSize    uint32
	ExtSize    uint32
	ValueID    uint32
	ToastRelID uint32
	Method     uint32
	Compressed bool
}

// parseExternalPointer：解析 18B TOAST 外联指针（0x01 0x12 + 16B varatt_external，小端）
// 布局: [va_rawsize 4B][va_extinfo 4B][va_valueid 4B][va_toastrelid 4B]
// 金仓实测数据此前按 [extsize][rawsize] 标注（未压缩时两值相等无法区分），
// 按掩码后大小自适应两种顺序。
func parseExternalPointer(b []byte, off int) *ExternalInfo {
	if off+18 > len(b) {
		return nil
	}
	if b[off] != 0x01 || b[off+1] != VARTAG_ONDISK {
		return nil
	}
	f1 := u32(b, off+2)
	f2 := u32(b, off+6)
	valueid := u32(b, off+10)
	toastrelid := u32(b, off+14)
	if !(100 <= valueid && valueid <= 100000000) {
		return nil
	}
	if !(100 <= toastrelid && toastrelid <= 100000000) {
		return nil
	}
	f1v := f1 & 0x3FFFFFFF
	f2v := f2 & 0x3FFFFFFF
	var rawsize, extinfo uint32
	if f1v >= f2v {
		rawsize, extinfo = f1, f2
	} else {
		rawsize, extinfo = f2, f1
	}
	if !(1 <= rawsize && rawsize <= 100000000) {
		return nil
	}
	extsize := extinfo & 0x3FFFFFFF
	method := (extinfo >> 30) & 0x03
	if !(1 <= extsize && extsize <= 100000000) {
		return nil
	}
	return &ExternalInfo{
		RawSize:    rawsize,
		ExtSize:    extsize,
		ValueID:    valueid,
		ToastRelID: toastrelid,
		Method:     method,
		Compressed: extsize < rawsize-4,
	}
}

// ---------- TOAST 压缩解压 ----------
const (
	TOAST_COMPRESS_METHOD_PGLZ = 0
	TOAST_COMPRESS_METHOD_LZ4  = 1
)

// pglzDecompress：PGLZ 解压（PG pg_lzcompress.c pglz_decompress 忠实移植）
// 控制字节 8 个 tag 位 LSB-first；1=匹配(2B)，0=字面(1B)
func pglzDecompress(data []byte, expectedSize int) []byte {
	out := make([]byte, 0, expectedSize)
	sp := 0
	n := len(data)
	for sp < n && len(out) < expectedSize {
		ctrl := data[sp]
		sp++
		for range 8 {
			if sp >= n || len(out) >= expectedSize {
				break
			}
			if ctrl&1 != 0 {
				if sp+2 > n {
					return nil
				}
				b1 := data[sp]
				b2 := data[sp+1]
				sp += 2
				length := int(b1&0x0F) + 3
				off := int((b1&0xF0)<<4) | int(b2)
				if length == 18 {
					if sp >= n {
						return nil
					}
					length += int(data[sp])
					sp++
				}
				if off == 0 {
					return nil
				}
				if len(out) < off {
					return nil
				}
				remaining := length
				if expectedSize-len(out) < remaining {
					remaining = expectedSize - len(out)
				}
				src := len(out) - off
				for i := 0; i < remaining; i++ {
					out = append(out, out[src+i])
				}
			} else {
				out = append(out, data[sp])
				sp++
			}
			ctrl >>= 1
		}
	}
	if len(out) != expectedSize {
		return nil
	}
	return out
}

// lz4BlockDecompress：LZ4 block 格式解压
func lz4BlockDecompress(data []byte, expectedSize int) []byte {
	out := make([]byte, 0, expectedSize)
	sp := 0
	n := len(data)
	for sp < n {
		token := data[sp]
		sp++
		litLen := int(token >> 4)
		if litLen == 15 {
			for {
				if sp >= n {
					return nil
				}
				b := data[sp]
				sp++
				litLen += int(b)
				if b != 255 {
					break
				}
			}
		}
		if sp+litLen > n {
			return nil
		}
		out = append(out, data[sp:sp+litLen]...)
		sp += litLen
		if sp >= n {
			break
		}
		if sp+2 > n {
			return nil
		}
		offset := int(data[sp]) | int(data[sp+1])<<8
		sp += 2
		if offset == 0 {
			return nil
		}
		matchLen := int(token&0x0F) + 4
		if token&0x0F == 15 {
			for {
				if sp >= n {
					return nil
				}
				b := data[sp]
				sp++
				matchLen += int(b)
				if b != 255 {
					break
				}
			}
		}
		if len(out) < offset {
			return nil
		}
		src := len(out) - offset
		for i := 0; i < matchLen; i++ {
			out = append(out, out[src+i])
		}
	}
	if len(out) != expectedSize {
		return nil
	}
	return out
}

// toastDecompress：解压 TOAST 压缩数据
// payload 前 4 字节小端 tcinfo: 低 30 位 = 原始数据长度，高 2 位 = 压缩方法
func toastDecompress(payload []byte, expectedSize int, method uint32) []byte {
	if len(payload) < 5 {
		return nil
	}
	tcinfo := u32(payload, 0)
	rawsize := int(tcinfo & 0x3FFFFFFF)
	tcMethod := (tcinfo >> 30) & 0x03
	if rawsize != expectedSize {
		return nil
	}
	body := payload[4:]
	if tcMethod == TOAST_COMPRESS_METHOD_LZ4 {
		return lz4BlockDecompress(body, rawsize)
	}
	if tcMethod == TOAST_COMPRESS_METHOD_PGLZ {
		return pglzDecompress(body, rawsize)
	}
	return nil
}

// rebuildVarlena：将纯 payload 重建为合法 varlena（含头），小端 4B 头
func rebuildVarlena(payload []byte) []byte {
	total := 4 + len(payload)
	if total < 128 {
		return append([]byte{byte((total << 1) | 1)}, payload...)
	}
	head := make([]byte, 4)
	binary.LittleEndian.PutUint32(head, uint32(total<<2))
	return append(head, payload...)
}
