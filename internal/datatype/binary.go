// pg2sql binary 层：小端读取、varlena 解析（PG 真实磁盘格式）、TOAST 压缩解压
// 对应 Python 版 pg2sql/binary.py（v2.3），逐字节等价翻译
// Author: raysuen
package datatype

import (
	"encoding/binary"
	"pg2sql/internal/charset"
)

// ---------- 小端读取 ----------
func U16(B []byte, off int) uint16 { return binary.LittleEndian.Uint16(B[off:]) }

func U32(B []byte, off int) uint32 { return binary.LittleEndian.Uint32(B[off:]) }

// CString：读取 C 风格 NUL 结尾字符串
func CString(B []byte, off int) string {
	end := off
	for end < len(B) && B[end] != 0 {
		end++
	}
	return charset.DecodeBytes(B[off:end])
}

// ---------- varlena 核心（PG 真实磁盘格式，小端主机布局） ----------
// VARATT_IS_1B_E:  首字节 == 0x01（外联指针 tag，第二个字节 0x12=VARTAG_ONDISK）
// VARATT_IS_1B:    首字节 & 0x01（短头，VARSIZE = first >> 1，含头最大 127）
// VARATT_IS_4B_U:  首字节 & 0x03 == 0x00（4B 头未压缩）
// VARATT_IS_4B_C:  首字节 & 0x03 == 0x02（4B 头内联压缩）
// VARSIZE_4B:      (小端 U32 >> 2) & 0x3FFFFFFF

const VARTAG_ONDISK = 0x12

const (
	VARLENA_EXTERNAL = "ext"
	VARLENA_1B       = "1b"
	VARLENA_4B       = "4b"
	VARLENA_4BC      = "4bc"
)

// VarlenaParse 返回 (kind, totalSize, payloadOff, payloadLen)
func VarlenaParse(B []byte, off int) (string, int, int, int) {
	if off >= len(B) {
		return "", 0, 0, 0
	}
	first := B[off]
	// 外联指针优先判定（0x01 也是奇数，必须先判）
	if first == 0x01 {
		if off+2 <= len(B) && B[off+1] == VARTAG_ONDISK {
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
	if off+4 > len(B) {
		return "", 0, 0, 0
	}
	word := U32(B, off)
	total := int((word >> 2) & 0x3FFFFFFF)
	if total < 4 {
		return "", 0, 0, 0
	}
	// PG 小端 4B 头低 2 位：00=未压缩(4B)，10=压缩(4BC)（VARATT_IS_4B_C=(hdr&3)==2）
	// 注意不能用 (first&0x06)==0x06：压缩后总长为偶数时 bit2=0，会误判为 4B 未压缩
	if (first & 0x03) == 0x02 {
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

// ParseExternalPointer：解析 18B TOAST 外联指针（0x01 0x12 + 16B varatt_external，小端）
// 布局: [va_rawsize 4B][va_extinfo 4B][va_valueid 4B][va_toastrelid 4B]
// 金仓实测数据此前按 [extsize][rawsize] 标注（未压缩时两值相等无法区分），
// 按掩码后大小自适应两种顺序。
func ParseExternalPointer(B []byte, off int) *ExternalInfo {
	if off+18 > len(B) {
		return nil
	}
	if B[off] != 0x01 || B[off+1] != VARTAG_ONDISK {
		return nil
	}
	f1 := U32(B, off+2)
	f2 := U32(B, off+6)
	valueid := U32(B, off+10)
	toastrelid := U32(B, off+14)
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
				// 注意：b1 为 byte(uint8)，(b1&0xF0)<<4 在 uint8 域移位会溢出
				// 截断高 4 位（bit8-11 丢失，off 最大仅 255），必须先将 b1 提升为 int
				off := int(b1&0xF0)<<4 | int(b2)
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
				B := data[sp]
				sp++
				litLen += int(B)
				if B != 255 {
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
				B := data[sp]
				sp++
				matchLen += int(B)
				if B != 255 {
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

// ToastDecompress：解压 TOAST 压缩数据
// Payload 前 4 字节小端 tcinfo: 低 30 位 = 原始数据长度，高 2 位 = 压缩方法
func ToastDecompress(Payload []byte, expectedSize int, method uint32) []byte {
	if len(Payload) < 5 {
		return nil
	}
	tcinfo := U32(Payload, 0)
	rawsize := int(tcinfo & 0x3FFFFFFF)
	tcMethod := (tcinfo >> 30) & 0x03
	if rawsize != expectedSize {
		return nil
	}
	body := Payload[4:]
	if tcMethod == TOAST_COMPRESS_METHOD_LZ4 {
		return lz4BlockDecompress(body, rawsize)
	}
	if tcMethod == TOAST_COMPRESS_METHOD_PGLZ {
		return pglzDecompress(body, rawsize)
	}
	return nil
}

// RebuildVarlena：将纯 Payload 重建为合法 varlena（含头），小端 4B 头
func RebuildVarlena(Payload []byte) []byte {
	total := 4 + len(Payload)
	if total < 128 {
		return append([]byte{byte((total << 1) | 1)}, Payload...)
	}
	head := make([]byte, 4)
	binary.LittleEndian.PutUint32(head, uint32(total<<2))
	return append(head, Payload...)
}
