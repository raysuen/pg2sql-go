package engine

// v1.0.54 回归测试（引擎侧）：C3 密文页误报。
//
// 现场复现都在 141 实机做过，这里用可离线运行的构造页把判定内核钉住，
// 避免以后改判据时又把「装满的明文页」判成密文。
// C1（分区 DDL）/C4（编码精化）的用例分别落在 internal/app、internal/charset。

import (
	"math/rand"
	"testing"
)

// ---- 构造页工具 ----

func t54Put16(b []byte, off int, v int) {
	b[off] = byte(v)
	b[off+1] = byte(v >> 8)
}

// t54Tuple：构造一个元组头自洽的元组（natts 列，payload 为列数据）
func t54Tuple(natts int, payload []byte) []byte {
	b := make([]byte, 24+len(payload))
	b[18] = byte(natts & 0xFF) // infomask2 低 8 位（natts）
	b[19] = byte(natts >> 8)
	b[22] = 24 // t_hoff：无空值位图时 (23+7)&^7 = 24
	copy(b[24:], payload)
	return b
}

// t54Page：把若干元组铺进一页，返回页字节（free 为预留空闲区字节数）
func t54Page(tuples [][]byte, free int) []byte {
	ps := 8192
	page := make([]byte, ps)
	upper := ps - free
	for i, t := range tuples {
		off := upper - len(t)
		upper = off
		copy(page[off:], t)
		raw := uint32(ITEMID_NORMAL<<15) | uint32(off&0x7FFF) | uint32(len(t)&0x7FFF)<<17
		page[24+4*i] = byte(raw)
		page[24+4*i+1] = byte(raw >> 8)
		page[24+4*i+2] = byte(raw >> 16)
		page[24+4*i+3] = byte(raw >> 24)
	}
	t54Put16(page, 12, 24+4*len(tuples)) // pd_lower
	t54Put16(page, 14, upper)            // pd_upper
	return page
}

// ---- C3：密文页判定 ----

// 满页明文（v1.0.53 被误判为密文的典型形态）：空闲 < 128B，中文 GBK 字节内容
func TestV1054SuspectCipherPackedPlainPage(t *testing.T) {
	gbkText := []byte{0xD6, 0xD0, 0xCE, 0xC4, 0xB2, 0xE2, 0xCA, 0xD4, 0xD3, 0xC3, 0xBB, 0xA7} // 中文测试用户
	var tuples [][]byte
	for i := 0; i < 40; i++ {
		p := make([]byte, 0, 180)
		for j := 0; j < 15; j++ {
			p = append(p, gbkText...)
		}
		tuples = append(tuples, t54Tuple(3, p))
	}
	page := t54Page(tuples, 8) // free=8 < 128，旧判据必误报
	if suspectCipherPage(page) {
		t.Fatalf("满页明文被误判为密文（free=8, 复现 v1.0.53 C3）")
	}
}

// 纯 ASCII 满页（z2 场景）
func TestV1054SuspectCipherAsciiPackedPage(t *testing.T) {
	var tuples [][]byte
	for i := 0; i < 60; i++ {
		p := make([]byte, 100)
		for j := range p {
			p[j] = 'x'
		}
		tuples = append(tuples, t54Tuple(2, p))
	}
	if suspectCipherPage(t54Page(tuples, 20)) {
		t.Fatalf("ASCII 满页被误判为密文（复现 v1.0.53 C3）")
	}
}

// 真密文页（页头 64B 明文 + 其余随机）：必须判为密文
func TestV1054SuspectCipherRealCipherPage(t *testing.T) {
	page := t54Page(nil, 0)
	rng := rand.New(rand.NewSource(20261027))
	for i := 64; i < len(page); i++ {
		page[i] = byte(rng.Intn(256))
	}
	// 页头保持自洽（真实 TDE 页头 0-63B 为明文），且为「装满」形态（free < 128）
	t54Put16(page, 12, 24+4*3)
	t54Put16(page, 14, 24+4*3+8)
	if !suspectCipherPage(page) {
		t.Fatalf("真密文页未被识别")
	}
}

// 稀疏页（大量空闲）：旧判据正常，新判据同样不能误报
func TestV1054SuspectCipherSparsePage(t *testing.T) {
	if suspectCipherPage(t54Page(nil, 7000)) {
		t.Fatalf("稀疏/空页被误判为密文")
	}
	if suspectCipherPage(make([]byte, 8192)) {
		t.Fatalf("全零页被误判为密文")
	}
}

// 明文但内容是随机字节（bytea/压缩列）：零密度低，但元组头自洽 → 不是密文
func TestV1054SuspectCipherBinaryPlainPage(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	blob := make([]byte, 4000)
	for i := range blob {
		blob[i] = byte(rng.Intn(256))
	}
	page := t54Page([][]byte{t54Tuple(2, blob), t54Tuple(2, blob)}, 4)
	if suspectCipherPage(page) {
		t.Fatalf("含随机字节列的明文页被误判为密文")
	}
}
