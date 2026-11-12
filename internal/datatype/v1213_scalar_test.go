package datatype

import (
	"encoding/binary"
	"testing"
)

// ---------- v1.2.13 标量解码修复用例 ----------
// 覆盖用户清单 #1(money) #2(tid) #6(interval) #7(inet) #8(regtype/regclass)
// 期望值全部来自 PG 17.6 实机输出（见 V1.2.13_BUG_ANALYSIS.md）。

func putI64(v int64) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, uint64(v))
	return b
}

func tidBytes(hi, lo, off uint16) []byte {
	b := make([]byte, 6)
	binary.LittleEndian.PutUint16(b[0:], hi)
	binary.LittleEndian.PutUint16(b[2:], lo)
	binary.LittleEndian.PutUint16(b[4:], off)
	return b
}

func intervalBytes(timeUS int64, day, month int32) []byte {
	b := make([]byte, 16)
	binary.LittleEndian.PutUint64(b[0:], uint64(timeUS))
	binary.LittleEndian.PutUint32(b[8:], uint32(day))
	binary.LittleEndian.PutUint32(b[12:], uint32(month))
	return b
}

func inetBytes(family, bits byte, addr []byte) []byte {
	pay := append([]byte{family, bits}, addr...)
	b := make([]byte, 4+len(pay))
	binary.LittleEndian.PutUint32(b, uint32(len(b))<<2)
	copy(b[4:], pay)
	return b
}

// #2：ItemPointerData = {bi_hi, bi_lo, offset}，块号 = bi_hi<<16 | bi_lo。
// 旧实现按 uint32 小端直读，两个半字整体互换（(1,2)→(65536,2)）。
func TestV1213TidBlockNumber(t *testing.T) {
	cases := []struct {
		hi, lo, off uint16
		want        string
	}{
		{0, 1, 2, "(1,2)"},     // 最常见形态：块号 1
		{1, 0, 2, "(65536,2)"}, // 高半字非零
		{0, 0, 1, "(0,1)"},     // 块号 0（旧实现唯一侥幸正确的值）
		{0xFFFF, 0xFFFF, 0, "(4294967295,0)"},
		{0, 43981, 17, "(43981,17)"},
	}
	for _, c := range cases {
		got := DecodeValue(TIDOID, tidBytes(c.hi, c.lo, c.off))
		if got != c.want {
			t.Errorf("tid(hi=%d,lo=%d,off=%d) = %q，期望 %q", c.hi, c.lo, c.off, got, c.want)
		}
	}
}

// #1：money 全值域 = INT64_MIN/100 .. INT64_MAX/100，INT64_MIN 合法可达。
// 旧实现 `v = -v` 在 INT64_MIN 溢出，产出 "-$-92,233,720,368,547,758.-8"。
func TestV1213MoneyInt64Min(t *testing.T) {
	cases := []struct {
		v    int64
		want string
	}{
		{-9223372036854775808, "-$92,233,720,368,547,758.08"}, // INT64_MIN
		{9223372036854775807, "$92,233,720,368,547,758.07"},   // INT64_MAX
		{-1, "-$0.01"},
		{0, "$0.00"},
		{-123456, "-$1,234.56"},
		{123456, "$1,234.56"},
		{-100000000, "-$1,000,000.00"},
	}
	for _, c := range cases {
		got := DecodeValue(MONEYOID, putI64(c.v))
		if got != c.want {
			t.Errorf("money(%d) = %q，期望 %q", c.v, got, c.want)
		}
	}
}

// #6：符号按 PG EncodeInterval 的"逐分量继承"规则输出——与前一非零分量同号
// 省略符号，异号显式标出；零分量不重置继承状态。全部期望值取自 PG 17.6 实测。
func TestV1213IntervalSign(t *testing.T) {
	hm := int64((2*3600 + 3*60 + 4) * 1000000)
	h3 := int64(3 * 3600 * 1000000)
	cases := []struct {
		timeUS     int64
		day, month int32
		want       string
	}{
		{hm, -1, 0, "-1 days +02:03:04"},  // 日期负、时间正 → 必须 +
		{-hm, 1, 0, "1 day -02:03:04"},    // 日期正、时间负
		{-hm, -1, 0, "-1 days -02:03:04"}, // 双负：同号，负号来自时间自身
		{hm, 0, 0, "02:03:04"},            // 日期为零：正时间不加 +
		{-hm, 0, 0, "-02:03:04"},          // 日期为零：负时间带 -
		{hm, 0, -2, "-2 mons +02:03:04"},  // 月份负（年/月也算日期部分）

		// 以下四条是 v1.2.13 修正"日期非零即加 +"错误规则后的关键回归防护
		{hm, 3, 14, "1 year 2 mons 3 days 02:03:04"},   // 全正：不得出现任何 +
		{h3, 2, -1, "-1 mons +2 days 03:00:00"},        // 月负、日正 → 日带 +；时间与日同号 → 省略
		{h3, 2, -12, "-1 years +2 days 03:00:00"},      // 中间月份为零，不重置继承状态
		{h3, -2, 0, "-2 days +03:00:00"},               // 日负、时正
		{-h3, 3, 14, "1 year 2 mons 3 days -03:00:00"}, // 全正日期 + 负时间 → 时间自带 -
	}
	for _, c := range cases {
		got := DecodeValue(INTERVALOID, intervalBytes(c.timeUS, c.day, c.month))
		if got != c.want {
			t.Errorf("interval(timeUS=%d,day=%d,mon=%d) = %q，期望 %q",
				c.timeUS, c.day, c.month, got, c.want)
		}
	}
}

// #7：IPv6 按 RFC 5952 压缩（旧实现固定 8 组十六进制）。
func TestV1213InetIPv6Compress(t *testing.T) {
	v6 := func(parts ...uint16) []byte {
		b := make([]byte, 16)
		for i, p := range parts {
			binary.BigEndian.PutUint16(b[i*2:], p)
		}
		return b
	}
	// ::1
	loop := make([]byte, 16)
	loop[15] = 1
	// 2001:db8::
	doc := v6(0x2001, 0x0db8, 0, 0, 0, 0, 0, 0)
	// ::ffff:192.168.1.1
	mapped := v6(0, 0, 0, 0, 0, 0xffff, 0xc0a8, 0x0101)
	// 2001:db8::1
	mid := v6(0x2001, 0x0db8, 0, 0, 0, 0, 0, 1)
	// fe80:0:1:2:3:4:5:6（只有一个零组，RFC 5952 不压缩）
	single := v6(0xfe80, 0, 1, 2, 3, 4, 5, 6)

	cases := []struct {
		raw  []byte
		bits byte
		want string
	}{
		{loop, 128, "::1"},
		{doc, 64, "2001:db8::/64"},
		{mid, 128, "2001:db8::1"},
		{mapped, 128, "::ffff:192.168.1.1"},
		{make([]byte, 16), 128, "::"},
		{single, 128, "fe80:0:1:2:3:4:5:6"},
	}
	for _, c := range cases {
		got := DecodeValue(INETOID, inetBytes(3, c.bits, c.raw))
		if got != c.want {
			t.Errorf("inet(% X,/%d) = %q，期望 %q", c.raw, c.bits, got, c.want)
		}
	}
	// IPv4 不受影响
	v4 := []byte{192, 168, 1, 1}
	if got := DecodeValue(INETOID, inetBytes(2, 32, v4)); got != "192.168.1.1" {
		t.Errorf("inet v4 = %q，期望 192.168.1.1", got)
	}
}

// #8：regtype 输出类型名（旧实现输出裸 OID）；regclass 对 pg_catalog 省略前缀。
func TestV1213RegtypeName(t *testing.T) {
	oldDefs, oldNsp, oldCls, oldClsNsp, oldDefault := DynamicTypeDefs, regNamespaceNames,
		regClassNames, regClassNamespaces, DefaultRegNamespace
	t.Cleanup(func() {
		DynamicTypeDefs, regNamespaceNames = oldDefs, oldNsp
		regClassNames, regClassNamespaces = oldCls, oldClsNsp
		DefaultRegNamespace = oldDefault
	})
	DynamicTypeDefs = map[uint32]*TypeDef{
		23:    {OID: 23, Name: "integer", Nsp: 11, Type: 'b'},
		25:    {OID: 25, Name: "text", Nsp: 11, Type: 'b'},
		1009:  {OID: 1009, Name: "_text", Nsp: 11, Type: 'b', Elem: 25},
		90001: {OID: 90001, Name: "mood", Nsp: 90000, Type: 'e'},
	}
	regNamespaceNames = map[uint32]string{11: "pg_catalog", 2200: "public", 90000: "biz"}
	DefaultRegNamespace = "public"
	regClassNames = map[uint32]string{1259: "pg_class", 16384: "orders"}
	regClassNamespaces = map[uint32]uint32{1259: 11, 16384: 90000}

	cases := []struct {
		oid  uint32
		want string
	}{
		{23, "integer"}, // pg_catalog 内：不加 schema
		{25, "text"},
		{1009, "text[]"},    // 数组：元素名 + []
		{90001, "biz.mood"}, // 自定义 schema：限定
		{0, "-"},            // InvalidOid
	}
	for _, c := range cases {
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, c.oid)
		if got := DecodeValue(uint32(2206), b); got != c.want {
			t.Errorf("regtype(%d) = %q，期望 %q", c.oid, got, c.want)
		}
	}

	// regclass：pg_catalog 内省略前缀（PG 输出 pg_class，旧实现输出 pg_catalog.pg_class）
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, 1259)
	if got := DecodeValue(2205, b); got != "pg_class" {
		t.Errorf("regclass(pg_class) = %q，期望 pg_class", got)
	}
	binary.LittleEndian.PutUint32(b, 16384)
	if got := DecodeValue(2205, b); got != "biz.orders" {
		t.Errorf("regclass(biz.orders) = %q，期望 biz.orders", got)
	}
	// regnamespace(4089)：输出 schema 名（旧映射把 4089 当 regconfig，输出裸 OID）
	binary.LittleEndian.PutUint32(b, 2200)
	if got := DecodeValue(4089, b); got != "public" {
		t.Errorf("regnamespace(2200) = %q，期望 public", got)
	}
	// OID 映射本身：2206 是 regtype、2202 是 regprocedure（旧版错位）
	if Decoders[2206] == nil || Decoders[2206]([]byte{23, 0, 0, 0}) != "integer" {
		t.Errorf("Decoders[2206] 不是 regtype 解码器（OID 映射错位未修）")
	}
}

// regtype 反查 miss 时回退裸 OID（跨库不可移植，但信息不丢）
func TestV1213RegtypeFallbackOID(t *testing.T) {
	old := DynamicTypeDefs
	t.Cleanup(func() { DynamicTypeDefs = old })
	DynamicTypeDefs = map[uint32]*TypeDef{}
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, 12345)
	if got := DecodeValue(uint32(2206), b); got != "12345" {
		t.Errorf("regtype 反查 miss = %q，期望裸 OID 12345", got)
	}
}

// #5：列头越界时必须返回降级列号（旧实现只置 nil，调用方无从得知）
func TestV1213ExtractFieldsBadCol(t *testing.T) {
	// 布局：id(int4) + v(varlena)
	cols := []ColLen{
		{AttLen: 4, AttAlign: "i"},
		{AttLen: -1, IsVarlena: true, AttAlign: "i"},
	}
	// 正常：1B 头 varlena，total=4（payload 3）
	ok := []byte{0, 0, 0, 1, 0x07, 'a', 'b', 'c'}
	if _, bad := ExtractFieldsDirectEx(ok, 0, nil, cols); bad != -1 {
		t.Fatalf("完好行返回 badCol=%d，期望 -1", bad)
	}
	// 损坏：1B 头声明 total=127，超出元组长度
	bad := []byte{0, 0, 0, 1, 0xFE, 'a', 'b', 'c'}
	fields, badCol := ExtractFieldsDirectEx(bad, 0, nil, cols)
	if badCol != 1 {
		t.Fatalf("越界行返回 badCol=%d，期望 1", badCol)
	}
	if fields[1] != nil {
		t.Errorf("越界列应为 nil")
	}
	// 定长列越界：只有 id 4 字节，第二个 int4 读不到
	cols2 := []ColLen{{AttLen: 4, AttAlign: "i"}, {AttLen: 4, AttAlign: "i"}}
	if _, badCol = ExtractFieldsDirectEx([]byte{0, 0, 0, 1}, 0, nil, cols2); badCol != 1 {
		t.Fatalf("定长越界返回 badCol=%d，期望 1", badCol)
	}
	// 兼容层：旧签名仍返回同样的字段数组
	if got := ExtractFieldsDirect(bad, 0, nil, cols); got[1] != nil {
		t.Errorf("ExtractFieldsDirect 应与 Ex 行为一致")
	}
}
