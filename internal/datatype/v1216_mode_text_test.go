// v1.2.16 回归（外部回归报告 D6 / D7）：金仓 oracle / mysql 兼容模式的文本形态。
//
// D6：oracle 模式 ora_input_emptystr_isnull 把空串字面量变 NULL →
//
//	真空串必须用 convert_from('\x'::bytea,'UTF8') 构造。
//
// D7：mysql 模式 interval 时间部分恒 6 位微秒、timestamptz 不带 "+00"。
package datatype

import (
	"encoding/binary"
	"testing"
	"time"
)

func setMode(oracle, mysql bool) func() {
	oldO, oldM := kbOracleMode, kbMysqlMode
	kbOracleMode, kbMysqlMode = oracle, mysql
	return func() { kbOracleMode, kbMysqlMode = oldO, oldM }
}

// ---- D7：interval ----

func TestV1216IntervalMysqlFormat(t *testing.T) {
	defer setMode(false, true)()
	hm := int64((4*3600+5*60+6)*1000000 + 789000)
	cases := []struct {
		name       string
		timeUS     int64
		day, month int32
		want       string
	}{
		// 服务端实测（V9R1C10 mysql 模式 COPY 输出）
		{"full", hm, 3, 14, "1 year 2 mons 3 days 04:05:06.789000"},
		{"date-only", 0, 3, 0, "3 days"},
		{"zero", 0, 0, 0, "00:00:00.000000"},
		{"half-second", 500000, 1, 0, "1 day 00:00:00.500000"},
		{"neg-day-pos-time", int64((2*3600 + 3*60 + 4) * 1000000), -1, 0, "-1 days +02:03:04.000000"},
	}
	for _, c := range cases {
		got := DecodeValue(INTERVALOID, intervalBytes(c.timeUS, c.day, c.month))
		if got != c.want {
			t.Errorf("%s: interval = %q，期望 %q", c.name, got, c.want)
		}
	}
}

// PG / oracle 模式必须保持去尾零形态（回归防护：模式分支不得外溢）。
func TestV1216IntervalPgFormatUnchanged(t *testing.T) {
	defer setMode(false, false)()
	hm := int64((2*3600+3*60+4)*1000000 + 500000)
	if got := DecodeValue(INTERVALOID, intervalBytes(hm, 3, 14)); got != "1 year 2 mons 3 days 02:03:04.5" {
		t.Errorf("PG 模式 interval = %q，期望去尾零形态", got)
	}
}

// ---- D7：timestamptz ----

func tsBytes(y int, mo time.Month, d, h int) []byte {
	base := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	us := int64(time.Date(y, mo, d, h, 0, 0, 0, time.UTC).Sub(base) / time.Microsecond)
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, uint64(us))
	return b
}

func TestV1216TimestamptzMysqlFormat(t *testing.T) {
	defer setMode(false, true)()
	oldSet, oldLoc, oldOff := MysqlTZSet, MysqlTZLoc, MysqlTZOffsetUS
	MysqlTZSet, MysqlTZLoc, MysqlTZOffsetUS = true, nil, 0 // 实例 timezone=UTC
	defer func() { MysqlTZSet, MysqlTZLoc, MysqlTZOffsetUS = oldSet, oldLoc, oldOff }()

	if got := DecodeValue(TIMESTAMPTZOID, tsBytes(2024, 1, 1, 9)); got != "2024-01-01 09:00:00" {
		t.Errorf("mysql 模式 timestamptz = %q，期望不带 +00 的本地形态", got)
	}
}

func TestV1216TimestamptzPgFormatUnchanged(t *testing.T) {
	defer setMode(false, false)()
	if got := DecodeValue(TIMESTAMPTZOID, tsBytes(2024, 1, 1, 9)); got != "2024-01-01 09:00:00+00" {
		t.Errorf("PG 模式 timestamptz = %q，期望保留 +00", got)
	}
}

// ---- D6：oracle 空串 ----

func TestV1216OracleEmptyString(t *testing.T) {
	defer setMode(true, false)()
	const empty = "convert_from('\\x'::bytea, 'UTF8')"
	cases := []struct {
		oid  uint32
		want string
	}{
		{TEXTOID, empty},
		{VARCHAROID, empty},
		{BPCHAROID, empty},
		{VARBITOID, empty + "::varbit"},
		{BITOID, empty + "::bit"},
	}
	for _, c := range cases {
		got, ok := OracleEmptyStringExpr(c.oid)
		if !ok || got != c.want {
			t.Errorf("oid %d: OracleEmptyStringExpr = (%q,%v)，期望 %q", c.oid, got, ok, c.want)
		}
	}
	// 非字符串类类型不改造（整数空串无意义）
	if _, ok := OracleEmptyStringExpr(INT4OID); ok {
		t.Error("int4 不应走空串改写")
	}
}

func TestV1216OracleEmptyStringDisabledInPgMode(t *testing.T) {
	defer setMode(false, false)()
	if _, ok := OracleEmptyStringExpr(TEXTOID); ok {
		t.Error("非 oracle 模式不得改写空串（否则 PG 上产物与 '' 不一致）")
	}
}
