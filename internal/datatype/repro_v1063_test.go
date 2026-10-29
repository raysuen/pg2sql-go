//go:build repro

// v1.0.62 深度审计复现用例（datatype 侧）
// 说明：本文件的断言写的是【正确行为】。当前版本跑起来应当是 FAIL——
// 每一条失败就是审计报告里对应缺陷的一次可重复复现（详见
// pg2sql_v1.0.62_深度审计_剩余缺陷.md）。
package datatype

import (
	"encoding/binary"
	"testing"
	"time"
)

// varlena4：给 payload 加 4 字节 varlena 头（total<<2）
func varlena4(payload []byte) []byte {
	total := len(payload) + 4
	out := make([]byte, total)
	binary.LittleEndian.PutUint32(out, uint32(total)<<2)
	copy(out[4:], payload)
	return out
}

// A-5：time 合法上界 24:00:00（内部值 86400*1e6）被取模成 00:00:00
func TestReproA5Time2400(t *testing.T) {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, 86400*1000000)
	if got := DecodeValue(TIMEOID, b); got != "24:00:00" {
		t.Fatalf("time 24:00:00 解码为 %q，期望 %q（差整一天）", got, "24:00:00")
	}
}

// A-5b：timetz 走同一条 timeFromUS 路径
func TestReproA5Timetz2400(t *testing.T) {
	b := make([]byte, 12)
	binary.LittleEndian.PutUint64(b, 86400*1000000)
	zone := int32(-28800) // +08：PG timetz 时区字段西正
	binary.LittleEndian.PutUint32(b[8:], uint32(zone))
	if got := DecodeValue(TIMETZOID, b); got != "24:00:00+08" {
		t.Fatalf("timetz 24:00:00+08 解码为 %q，期望 %q", got, "24:00:00+08")
	}
}

// B-4：BC 时间戳的小数秒被拼在 " BC" 之后 → PG 会把 ".5 BC" 整段当垃圾，
// 既丢 BC 标志也丢微秒（实测 '0044-03-15 12:00:00 BC.5'::timestamp → 0044-03-15 12:00:00）
func TestReproB4BCFractionOrder(t *testing.T) {
	// 公元前 44 年 = 天文纪年 -43（无公元 0 年）；Go 与 PG 同用前推格里历
	base := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	bc := time.Date(-43, time.March, 15, 12, 0, 0, 500000000, time.UTC)
	// 注意：不能直接用 Sub()——两千年跨度超出 time.Duration 的 int64 纳秒范围会溢出
	us := (bc.Unix()-base.Unix())*1000000 + int64(bc.Nanosecond())/1000

	got := TimestampFromUS(us)
	want := "0044-03-15 12:00:00.5 BC"
	if got != want {
		t.Fatalf("BC 小数秒顺序错误：得到 %q，期望 %q", got, want)
	}
}

// B-9：空 tsquery 的 payload 只有 4 字节（size=0），被 len<8 的守卫拦在 size<=0 之前
func TestReproB9EmptyTsquery(t *testing.T) {
	payload := make([]byte, 4) // int32 size = 0
	got := DecodeValue(3615, varlena4(payload))
	if got != "" {
		t.Fatalf("空 tsquery 解码为 %q，期望空串（输出 \\x 后 tsquery_in 会当成词元）", got)
	}
}

// B-13：数组下界（lbs）从未解析，[0:2]={1,2,3} 导出成 {1,2,3}
func TestReproB13ArrayLowerBound(t *testing.T) {
	payload := make([]byte, 20+3*4)
	binary.LittleEndian.PutUint32(payload[0:], 1)  // ndim
	binary.LittleEndian.PutUint32(payload[4:], 0)  // dataoffset（无 NULL）
	binary.LittleEndian.PutUint32(payload[8:], 23) // elemtype = int4
	binary.LittleEndian.PutUint32(payload[12:], 3) // dims[0] = 3
	binary.LittleEndian.PutUint32(payload[16:], 0) // lbs[0] = 0
	binary.LittleEndian.PutUint32(payload[20:], 1) // 1
	binary.LittleEndian.PutUint32(payload[24:], 2) // 2
	binary.LittleEndian.PutUint32(payload[28:], 3) // 3
	got := DecodeValue(1007, varlena4(payload))
	want := "[0:2]={1,2,3}"
	if got != want {
		t.Fatalf("数组下界丢失：得到 %q，期望 %q", got, want)
	}
}

// A-2：numeric[] 元素在 arrayElemInfo 里 degrade=true，绕过 DecodeNumeric 直接按文本转码
func TestReproA2NumericArray(t *testing.T) {
	elem1 := varlena4([]byte{
		0x02, 0x00, // dscale=2，sign=0
		0x00, 0x00, // weight=0
		0x01, 0x00, // digit 1
		0x88, 0x13, // digit 5000 → 1.50
	})
	elem2 := varlena4([]byte{
		0x02, 0x40, // dscale=2，sign=0x4000（负）
		0x00, 0x00, // weight=0
		0x02, 0x00, // digit 2
		0xc4, 0x09, // digit 2500 → -2.25
	})
	payload := make([]byte, 20, 20+len(elem1)+len(elem2))
	binary.LittleEndian.PutUint32(payload[0:], 1)    // ndim
	binary.LittleEndian.PutUint32(payload[4:], 0)    // dataoffset
	binary.LittleEndian.PutUint32(payload[8:], 1700) // elemtype = numeric
	binary.LittleEndian.PutUint32(payload[12:], 2)   // dims[0] = 2
	binary.LittleEndian.PutUint32(payload[16:], 1)   // lbs[0] = 1
	payload = append(payload, elem1...)
	payload = append(payload, elem2...)

	got := DecodeValue(1231, varlena4(payload))
	want := "{1.50,-2.25}"
	if got != want {
		t.Fatalf("numeric[] 元素被当文本解码：得到 %q，期望 %q", got, want)
	}
}

// A-2b：bytea[] 同样 degrade=true → 原始字节被当文本编码转换
func TestReproA2ByteaArray(t *testing.T) {
	elem1 := varlena4([]byte{0xde, 0xad, 0xbe, 0xef})
	payload := make([]byte, 20, 20+len(elem1))
	binary.LittleEndian.PutUint32(payload[0:], 1)
	binary.LittleEndian.PutUint32(payload[4:], 0)
	binary.LittleEndian.PutUint32(payload[8:], 17) // elemtype = bytea
	binary.LittleEndian.PutUint32(payload[12:], 1)
	binary.LittleEndian.PutUint32(payload[16:], 1)
	payload = append(payload, elem1...)

	got := DecodeValue(1001, varlena4(payload))
	want := `{"\\xdeadbeef"}`
	if got != want {
		t.Fatalf("bytea[] 元素被当文本解码：得到 %q，期望 %q", got, want)
	}
}

// A-2c（jsonb[]）无法用合成字节稳定复现，改用 141 实机样本验证——
// 实测导出 '{"","",""}'，回灌报 invalid input syntax for type json。
// 见报告 A-2 的实机证据段。

// A-6：multirange 六个 OID 全无解码器 → 整列 \x 十六进制
func TestReproA6Multirange(t *testing.T) {
	for _, oid := range []uint32{4451, 4532, 4533, 4534, 4535, 4536} {
		if _, ok := Decoders[oid]; !ok {
			t.Fatalf("multirange oid %d 无解码器（导出为 \\x 十六进制，multirange_in 拒绝）", oid)
		}
	}
}
