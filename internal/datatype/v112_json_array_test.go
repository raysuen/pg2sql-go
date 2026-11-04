// v1.1.2 修复（B18）：json[] / 金仓 mysql 模式 json[] 数组元素解码。
//
// 缺陷：arrayElemInfo 只有 JSONBOID(3802)，没有 JSONOID(114) 与金仓
// mysql 模式 json(7024)。PG 与金仓 PG 模式 json[] 的 elemtype=114；
// 金仓 V9 mysql 模式 json 是 domain of 4802 mysql_json（底层 jsonb 二进制），
// json[] 的 elemtype=7024。两种 OID 都落到"未知元素类型 → varlena 文本退化"，
// 元素原始容器字节被当 UTF-8 字符串输出：
//
//	json[] 导出 '{"\x01\x00\x00 \x01\x00\x00\xc2\x80\\v…"}'（含真实 0x00 字节），
//	回灌报 invalid byte sequence for encoding "UTF8": 0x00。
//
// 实机证据：金仓 V9R1C10B4 mysql 模式 rg.alltypes.c_jsonarr，源值 {"{\"a\": 1}"}，
// 工具输出 27 字节容器原文，服务端 COPY text 输出正确的 {"{\"a\": 1}"}。
package datatype

import (
	"encoding/binary"
	"strings"
	"testing"
)

// makeArray1D 组装 1 维、下界 1 的数组（含 varlena 头），elems 为各元素完整
// varlena 字节（含自身头）。
func makeArray1D(elemOID uint32, elems ...[]byte) []byte {
	body := make([]byte, 0, 20)
	hdr := make([]byte, 20)
	binary.LittleEndian.PutUint32(hdr[0:], 1) // ndim
	binary.LittleEndian.PutUint32(hdr[4:], 0) // dataoffset=0（无 NULL 位图）
	binary.LittleEndian.PutUint32(hdr[8:], elemOID)
	binary.LittleEndian.PutUint32(hdr[12:], uint32(len(elems))) // dim0
	binary.LittleEndian.PutUint32(hdr[16:], 1)                  // lbound
	body = append(body, hdr...)
	for _, e := range elems {
		body = append(body, e...)
	}
	full := make([]byte, 4+len(body))
	binary.LittleEndian.PutUint32(full[0:], uint32(len(full))<<2)
	copy(full[4:], body)
	return full
}

// varlena 包装：4 字节头 + payload
func varlena(payload []byte) []byte {
	out := make([]byte, 4+len(payload))
	binary.LittleEndian.PutUint32(out[0:], uint32(len(out))<<2)
	copy(out[4:], payload)
	return out
}

// jsonbContainerA1 实机取证字节：金仓 V9R1C10B4 mysql 模式 rg.alltypes.c_jsonarr
// 的元素容器（源值 {"a": 1}），取自修复前工具的错误输出（原始容器被当字符串
// 吐出）。注意：text 格式会把容器里的 0x5c 转义成 \x5c\x5c，此处已还原为单字节。
var jsonbContainerA1 = []byte{
	0x01, 0x00, 0x00, 0x20, 0x01, 0x00, 0x00, 0xc2, 0x80, 0x5c,
	0x76, 0x00, 0x00, 0x10, 0x61, 0x00, 0x00, 0x00, 0x20, 0x00,
	0x00, 0x00, 0x00, 0xc2, 0x80, 0x01, 0x00,
}

// TestV112JsonArrayElemInfo 确认 json[] 的元素 OID 均已注册。
func TestV112JsonArrayElemInfo(t *testing.T) {
	cases := []struct {
		oid  uint32
		name string
	}{
		{JSONOID, "json(114) PG/金仓PG 模式"},
		{7024, "json(7024) 金仓 mysql 模式（底层 jsonb）"},
	}
	for _, c := range cases {
		info, ok := arrayElemInfo[c.oid]
		if !ok {
			t.Fatalf("%s OID=%d 未注册 arrayElemInfo（B18 回归）", c.name, c.oid)
		}
		fn := info[2].(string)
		if fn == "" {
			t.Fatalf("%s OID=%d 解码函数名为空，会退化成原始字节", c.name, c.oid)
		}
	}
}

// TestV112JsonArrayText 文本型 json[]（OID 114）：元素是带 varlena 头的 UTF-8 文本，
// 输出应是 {"{\"a\": 1}"}（与 PG array_out 一致：内层引号被转义）。
func TestV112JsonArrayText(t *testing.T) {
	elem := varlena([]byte(`{"a": 1}`))
	raw := makeArray1D(JSONOID, elem)
	got := DecodeArray(raw)
	want := `{"{\"a\": 1}"}`
	if got != want {
		t.Fatalf("json[] 解码不符：got=%q want=%q", got, want)
	}
}

// TestV112JsonArrayMysqlBinary 金仓 mysql 模式 json[]（OID 7024，底层 jsonb 二进制）：
// 只需确认走的是 jsonb 容器解码器（不是"varlena 文本退化"），
// 即输出是可解析的 JSON 文本、不含原始控制字节。逐字节正确性由实机
// rt_alltypes.sh 全类型回灌（与服务端 COPY text 比对）覆盖。
func TestV112JsonArrayMysqlBinary(t *testing.T) {
	// 用最小合法容器：空对象 {}（header=JB_FOBJECT|0）
	empty := []byte{0x00, 0x00, 0x00, 0x20}
	raw := makeArray1D(7024, varlena(empty))
	got := DecodeArray(raw)
	if strings.ContainsAny(got, "\x00\x01\x02\x03") {
		t.Fatalf("mysql 模式 json[] 仍输出原始控制字节：%q", got)
	}
	if !strings.HasPrefix(got, `{"`) {
		t.Fatalf("mysql 模式 json[] 未按 JSON 文本输出：%q", got)
	}
	t.Logf("mysql 模式 json[]（空对象元素）解码 = %q", got)
}
