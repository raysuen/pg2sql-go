package ddl

// v1.0.65 回归用例（F5）：ParseTextArray NULL 位图语义 + dataoffset 坐标。
// 上游依据：PG array.h "a 1 indicates a non-null entry"（位图位置位 = 非 NULL），
// dataoffset = 含 4 字节 varlena 头的 datum 内偏移（ARR_DATA_PTR(a) = a + dataoffset），
// 本函数 payload 已剥头，body 实际在 payload 坐标 dataoffset-4 处。

import (
	"encoding/binary"
	"reflect"
	"testing"
)

// textArrPayload 构造 1 维 text[] 磁盘 payload（已剥 varlena 头）。
// withBitmap=true 时 dataoffset = MAXALIGN(24+bitmapBytes)（datum 坐标）。
func textArrPayload(dim1, dataOff int, bitmap []byte, elems []byte) []byte {
	p := make([]byte, 20)
	binary.LittleEndian.PutUint32(p[0:], 1) // ndim
	binary.LittleEndian.PutUint32(p[4:], uint32(dataOff))
	binary.LittleEndian.PutUint32(p[8:], 25) // elemtype = TEXTOID
	binary.LittleEndian.PutUint32(p[12:], uint32(dim1))
	binary.LittleEndian.PutUint32(p[16:], 1) // lb
	p = append(p, bitmap...)
	for len(p) < dataOff-4 { // 补齐到 body 起点（payload 坐标 = dataOff-4）
		p = append(p, 0)
	}
	return append(p, elems...)
}

// 1B 短头 text 元素：total = 1+len，头 = (total<<1)|1
func shortTextElem(s string) []byte {
	return append([]byte{byte((len(s)+1)<<1) | 1}, []byte(s)...)
}

// F5：{a, NULL, c}——混合命名函数 proargnames 的典型布局。
// 位图 0b101（bit0/2 非 NULL），body 在 payload 坐标 28（dataoffset=32）。
func TestParseTextArrayNullBitmap(t *testing.T) {
	var elems []byte
	elems = append(elems, shortTextElem("a")...) // payload 28..29
	elems = append(elems, shortTextElem("c")...) // payload 30..31
	raw := textArrPayload(3, 32, []byte{0b101}, elems)
	got := ParseTextArray(raw)
	if want := []string{"a", "", "c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("NULL 位图解码错误: got %q want %q", got, want)
	}
}

// 回归护栏：无 NULL（dataoffset=0，无位图）的常见路径不受影响。
func TestParseTextArrayNoBitmap(t *testing.T) {
	var elems []byte
	elems = append(elems, shortTextElem("x")...)
	elems = append(elems, shortTextElem("y")...)
	raw := textArrPayload(2, 0, nil, elems)
	got := ParseTextArray(raw)
	if want := []string{"x", "y"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("无位图数组解码错误: got %q want %q", got, want)
	}
}

// 全 NULL：位图 0b000，body 为空。
func TestParseTextArrayAllNull(t *testing.T) {
	raw := textArrPayload(2, 32, []byte{0b00}, nil)
	got := ParseTextArray(raw)
	if want := []string{"", ""}; !reflect.DeepEqual(got, want) {
		t.Fatalf("全 NULL 数组解码错误: got %q want %q", got, want)
	}
}
