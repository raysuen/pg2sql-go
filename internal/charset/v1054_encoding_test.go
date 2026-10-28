package charset

// v1.0.54 回归测试（编码侧）：C4 多页投票编码判定。
//
// 用例与实机一致：把同一段文本按 GBK / UTF8 铺进若干数据页，再让
// ProbeFileEncodingMajority 逐页投票；页间矛盾时保守返回空串。

import (
	"os"
	"path/filepath"
	"testing"
)

const t54ItemIDNormal = 1 // pg 页内 ItemId 标志位：正常元组

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
		raw := uint32(t54ItemIDNormal<<15) | uint32(off&0x7FFF) | uint32(len(t)&0x7FFF)<<17
		page[24+4*i] = byte(raw)
		page[24+4*i+1] = byte(raw >> 8)
		page[24+4*i+2] = byte(raw >> 16)
		page[24+4*i+3] = byte(raw >> 24)
	}
	t54Put16(page, 12, 24+4*len(tuples)) // pd_lower
	t54Put16(page, 14, upper)            // pd_upper
	return page
}

func t54WritePages(t *testing.T, pages [][]byte) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "17631")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, pg := range pages {
		if _, err := f.Write(pg); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func t54TextPage(text []byte) []byte {
	var tuples [][]byte
	for i := 0; i < 30; i++ {
		tuples = append(tuples, t54Tuple(2, text))
	}
	return t54Page(tuples, 300)
}

// GBK 页占多数 → gbk
func TestV1054EncodingMajorityGBK(t *testing.T) {
	gbk := []byte{0xD3, 0xC3, 0xBB, 0xA7, 0xD0, 0xD5, 0xC3, 0xFB, 0xB2, 0xE2, 0xCA, 0xD4} // 用户姓名测试
	pages := [][]byte{t54TextPage(gbk), t54TextPage(gbk), t54TextPage(gbk), t54TextPage(gbk)}
	got := ProbeFileEncodingMajority(t54WritePages(t, pages), 8192)
	if got != "gbk" {
		t.Fatalf("GBK 数据页投票结果 = %q，期望 gbk", got)
	}
}

// UTF8 页 → utf8
func TestV1054EncodingMajorityUTF8(t *testing.T) {
	utf8Text := []byte("用户姓名测试数据")
	pages := [][]byte{t54TextPage(utf8Text), t54TextPage(utf8Text), t54TextPage(utf8Text)}
	got := ProbeFileEncodingMajority(t54WritePages(t, pages), 8192)
	if got != "utf8" {
		t.Fatalf("UTF8 数据页投票结果 = %q，期望 utf8", got)
	}
}

// 页间矛盾（一半 GBK 一半 UTF8）：保守返回 unknown，不改库级判定
func TestV1054EncodingMajorityConflict(t *testing.T) {
	gbk := []byte{0xD3, 0xC3, 0xBB, 0xA7, 0xD0, 0xD5, 0xC3, 0xFB}
	utf8Text := []byte("用户姓名测试数据")
	pages := [][]byte{t54TextPage(gbk), t54TextPage(utf8Text)}
	if got := ProbeFileEncodingMajority(t54WritePages(t, pages), 8192); got != "" {
		t.Fatalf("矛盾页投票结果 = %q，期望空（保守不改）", got)
	}
}
