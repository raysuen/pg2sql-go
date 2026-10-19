package main

import (
	"encoding/binary"
	"testing"
)

// B9 验证：金仓 mysql 模式 timestamp 硬编码 UTC+8
// 存储格式 int64 微秒，基准 2000-01-01 00:00:00 UTC。
// 若实例时区为 UTC+8：本地 2000-01-01 12:00:00 → 存储 04:00:00 UTC (us=4h)
// 解码应输出 2000-01-01 12:00:00（当前实现：us+8h 恒成立）
// 若实例时区为 UTC+9：本地 12:00 → 存储 03:00:00 (us=3h)，解码仍 +8 → 11:00:00（错 1 小时）
func mysqlTS(us int64) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, uint64(us))
	return b
}

func TestDecodeMysqlTimestampB9(t *testing.T) {
	h := int64(3600 * 1000000)
	cases := []struct {
		name string
		us   int64 // 存储值（UTC 微秒，基准 2000-01-01）
		want string
	}{
		{"epoch(2000-01-01 00:00:00 UTC)", 0, "2000-01-01 08:00:00"},    // +8 硬编码可观测
		{"UTC+8 实例本地12:00(存4h)", 4 * h, "2000-01-01 12:00:00"},      // 默认实例正确
		{"UTC+9 实例本地12:00(存3h)", 3 * h, "2000-01-01 11:00:00"},      // 非+8实例: 输出11点而非12点
		{"UTC+0 实例本地12:00(存12h)", 12 * h, "2000-01-01 20:00:00"},    // 非+8实例: 输出20点
		{"infinity", 0x7FFFFFFFFFFFFFFF, "infinity"},
		{"-infinity", -0x8000000000000000, "-infinity"},
	}
	for _, c := range cases {
		got := decodeMysqlTimestamp(mysqlTS(c.us))
		status := "PASS"
		if got != c.want {
			status = "FAIL"
		}
		t.Logf("[%s] %-34s us=%d got=%s want=%s", status, c.name, c.us, got, c.want)
		if got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}
