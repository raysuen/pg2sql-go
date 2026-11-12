// v1.2.18 回归用例：mysql 模式的时间必须钉住会话时区（P2）。
//
// 缺陷现场：金仓 mysql 兼容模式的 timestamptz 与分区边界字面量输出本地时间、
// **不带时区后缀**（v1.2.16 D7）。而导出脚本头部只有 SET client_encoding，
// 没有 SET TIME ZONE → 回灌时按目标会话时区解释：
//
//	源 Asia/Shanghai(+08) 导出 "2024-01-01 09:00:00"，目标默认 UTC →
//	实际存成 09:00 UTC = 源库 17:00+08，整体平移 8 小时，INSERT 不报错。
//
// 分区边界字面量同理会让分区落错，且同样不报错。
package datatype

import (
	"strings"
	"testing"
	"time"
)

func TestV1218MySQLLocalTZSQLValueIANA(t *testing.T) {
	oldLoc, oldOff, oldSet := MysqlTZLoc, MysqlTZOffsetUS, MysqlTZSet
	t.Cleanup(func() { MysqlTZLoc, MysqlTZOffsetUS, MysqlTZSet = oldLoc, oldOff, oldSet })

	if !SetMysqlTZByName("Asia/Shanghai") {
		t.Fatal("前置条件：Asia/Shanghai 应能加载")
	}
	got := MySQLLocalTZSQLValue()
	// IANA 名必须原样输出：只有名字能表达"按当时规则解释"（历史 LMT / 夏令时）
	if got != "'Asia/Shanghai'" {
		t.Errorf("MySQLLocalTZSQLValue() = %q, want 'Asia/Shanghai'", got)
	}
	if strings.Contains(got, "INTERVAL") {
		t.Errorf("IANA 名可用时不应退化成 INTERVAL 形式，实际 %q", got)
	}
}

func TestV1218MySQLLocalTZSQLValueFixedOffset(t *testing.T) {
	oldLoc, oldOff, oldSet := MysqlTZLoc, MysqlTZOffsetUS, MysqlTZSet
	t.Cleanup(func() { MysqlTZLoc, MysqlTZOffsetUS, MysqlTZSet = oldLoc, oldOff, oldSet })

	// 固定偏移路径（--tz +09:00）
	if !SetMysqlTZByName("+09:00") {
		t.Fatal("前置条件：+09:00 应能解析为固定偏移")
	}
	if MysqlTZLoc != nil {
		t.Fatal("固定偏移路径不应设置 MysqlTZLoc")
	}
	got := MySQLLocalTZSQLValue()
	want := "INTERVAL '+09:00' HOUR TO MINUTE"
	if got != want {
		t.Errorf("MySQLLocalTZSQLValue() = %q, want %q", got, want)
	}

	// 负偏移
	if !SetMysqlTZByName("-05:30") {
		t.Fatal("前置条件：-05:30 应能解析")
	}
	got = MySQLLocalTZSQLValue()
	want = "INTERVAL '-05:30' HOUR TO MINUTE"
	if got != want {
		t.Errorf("负偏移渲染错误：got %q, want %q", got, want)
	}
}

// 默认值（未指定 --tz、conf 也读不到）必须落在 +08:00，且渲染为 INTERVAL 而非空。
func TestV1218MySQLLocalTZDefaultIsPlus8(t *testing.T) {
	oldLoc, oldOff, oldSet := MysqlTZLoc, MysqlTZOffsetUS, MysqlTZSet
	t.Cleanup(func() { MysqlTZLoc, MysqlTZOffsetUS, MysqlTZSet = oldLoc, oldOff, oldSet })

	MysqlTZLoc = nil
	MysqlTZOffsetUS = 8 * 3600 * 1000000
	MysqlTZSet = false
	got := MySQLLocalTZSQLValue()
	if got != "INTERVAL '+08:00' HOUR TO MINUTE" {
		t.Errorf("默认偏移应为 +08:00，实际 %q", got)
	}
}

// UTC 必须渲染成 INTERVAL '+00:00'（而不是空串或 'UTC' 以外的怪形态）
func TestV1218MySQLLocalTZUTC(t *testing.T) {
	oldLoc, oldOff, oldSet := MysqlTZLoc, MysqlTZOffsetUS, MysqlTZSet
	t.Cleanup(func() { MysqlTZLoc, MysqlTZOffsetUS, MysqlTZSet = oldLoc, oldOff, oldSet })

	if !SetMysqlTZByName("UTC") {
		t.Fatal("前置条件：UTC 应能加载")
	}
	got := MySQLLocalTZSQLValue()
	// time.LoadLocation("UTC") 成功 → IANA 分支，输出 'UTC'
	if got != "'UTC'" && got != "INTERVAL '+00:00' HOUR TO MINUTE" {
		t.Errorf("UTC 渲染异常：%q", got)
	}
	_ = time.UTC
}
