// v1.2.17 回归用例：interval 负分量符号 + reg* 省略 schema 前缀分支的引号。
//
// 两类缺陷的共同点：旧实现"看起来对"是因为探测样例恰好落在边界上——
// interval 只试了「月负 + 日正」，reg* 只试了 english/simple 这类无需引号的名字。
// 因此本文件的期望值全部取自服务端 ::text 输出（金仓 V9R1C10B4，pg 模式），
// 不是按代码逻辑推的。
package datatype

import (
	"encoding/binary"
	"fmt"
	"testing"
)

// mkInterval：构造 interval 磁盘表示（16 字节：time int64 / day int32 / month int32）。
func mkInterval(timeUS int64, day, month int32) []byte {
	b := make([]byte, 16)
	binary.LittleEndian.PutUint64(b[0:], uint64(timeUS))
	binary.LittleEndian.PutUint32(b[8:], uint32(day))
	binary.LittleEndian.PutUint32(b[12:], uint32(month))
	return b
}

// TestV1217IntervalNegativeComponentSign：
// 负分量的 "-" 与"是否继承前一分量"无关，任何情况下都不能省。
// 旧实现写成 `v < 0 && !prevNeg`，于是「月负 + 日负」时日负号被吞：
//
//	-9 mons -2 days +04:30:00  →  -9 mons 2 days +04:30:00   （值变了 4 天）
//
// 期望值 = 服务端 `select v::text` 的逐字节输出。
func TestV1217IntervalNegativeComponentSign(t *testing.T) {
	cases := []struct {
		id     int
		timeUS int64
		day    int32
		month  int32
		want   string
	}{
		{1, 16200000000, -2, -9, "-9 mons -2 days +04:30:00"},
		{2, -10800000000, -2, -1, "-1 mons -2 days -03:00:00"},
		{3, 14706000000, 3, 14, "1 year 2 mons 3 days 04:05:06"},
		{4, 10800000000, 2, -1, "-1 mons +2 days 03:00:00"},
		{5, 7384000000, -1, 0, "-1 days +02:03:04"},
		{6, -7384000000, -1, 0, "-1 days -02:03:04"},
		{7, 0, 2, -12, "-1 years +2 days"},
		{8, 0, -2, -1, "-1 mons -2 days"},
		{9, 0, 0, 0, "00:00:00"},
		{10, 1800000000, -2, 0, "-2 days +00:30:00"},
		{11, -14706000000, -3, -14, "-1 years -2 mons -3 days -04:05:06"},
		{12, 0, -2, -12, "-1 years -2 days"},
		{13, -16200000000, 2, 9, "9 mons 2 days -04:30:00"},
		{14, -16200000000, 2, -9, "-9 mons +2 days -04:30:00"},
	}
	for _, c := range cases {
		got := decodeInterval(mkInterval(c.timeUS, c.day, c.month))
		if got != c.want {
			t.Errorf("#%d got %q, want %q", c.id, got, c.want)
		}
	}
}

// TestV1217IntervalPgModeUnchanged：PG / 金仓 oracle 模式不受 mysql 分支影响，
// 负号规则与上面同一套（mysql 只额外改时间部分的 6 位微秒形态）。
func TestV1217IntervalMysqlKeepsSigns(t *testing.T) {
	old := kbMysqlMode
	kbMysqlMode = true
	defer func() { kbMysqlMode = old }()
	// -9 mons -2 days：时间部分为 0，mysql 形态下纯日期分量不追加时间
	if got := decodeInterval(mkInterval(0, -2, -9)); got != "-9 mons -2 days" {
		t.Errorf("mysql mode got %q, want %q", got, "-9 mons -2 days")
	}
	// 有负时间：恒 6 位微秒，负号保留
	if got := decodeInterval(mkInterval(-10800000000, -2, -1)); got != "-1 mons -2 days -03:00:00.000000" {
		t.Errorf("mysql mode got %q, want %q", got, "-1 mons -2 days -03:00:00.000000")
	}
}

// TestV1217RegQualIdentVisibleQuoting：
// 「省略 schema 前缀」的分支也必须 quote_identifier。
// 旧实现直接返回原名，只有 english / simple 这类本来就合法的名字才碰巧一致。
func TestV1217RegQualIdentVisibleQuoting(t *testing.T) {
	defer regFamilySetup(t)()
	// 注入到 public（DefaultRegNamespace）与 pg_catalog 的"需要引号"的名字
	regConfigInfos[50010] = RegNamed{Name: "My Cfg2", Nsp: 2200}
	regDictInfos[50011] = RegNamed{Name: "My Dict2", Nsp: 2200}
	regConfigInfos[50012] = RegNamed{Name: "My Cfg", Nsp: 90000}
	regRoleNames[50101] = "My Role"
	// 与报告一致的跨 schema 形态：schema 名与对象名都需要引号
	regNamespaceNames[90001] = "My Sch"
	regConfigInfos[50013] = RegNamed{Name: "My Cfg", Nsp: 90001}

	cases := []struct {
		name string
		got  string
		want string
	}{
		{"regconfig 可见·需引号", decodeRegconfig(oidBytes(t, 50010)), `"My Cfg2"`},
		{"regdictionary 可见·需引号", decodeRegdictionary(oidBytes(t, 50011)), `"My Dict2"`},
		// biz 本身是合法标识符 → 不加引号；只有名字部分需要
		{"regconfig 跨 schema·普通 schema 名", decodeRegconfig(oidBytes(t, 50012)), `biz."My Cfg"`},
		{"regconfig 跨 schema·双需引号", decodeRegconfig(oidBytes(t, 50013)), `"My Sch"."My Cfg"`},
		{"regrole 需引号", decodeRegrole(oidBytes(t, 50101)), `"My Role"`},
		// 反向断言：本来合法的名字不得被加引号
		{"regconfig english", decodeRegconfig(oidBytes(t, 3730)), "english"},
		{"regdictionary simple", decodeRegdictionary(oidBytes(t, 3735)), "simple"},
		{"regrole 普通名", decodeRegrole(oidBytes(t, 10)), "postgres"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
}

// 兜底分支（nsp 查不到 schema 名）同样要 quoting —— 旧实现这里也返回裸名。
func TestV1217RegQualIdentFallbackQuoting(t *testing.T) {
	defer regFamilySetup(t)()
	got := regQualIdent(0, "My Cfg2")
	if got != `"My Cfg2"` {
		t.Errorf("fallback = %q, want %q", got, `"My Cfg2"`)
	}
	if got := regQualIdent(0, "english"); got != "english" {
		t.Errorf("fallback plain = %q, want english", got)
	}
	_ = fmt.Sprintf("%s", got)
}
