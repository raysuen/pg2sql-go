// Author: raysuen
package util

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestV1062MaskKeyUTF8：P3 回归——密钥脱敏不得按字节切。
// 旧实现 s[:2] 对中文密钥（3B/字）会切出半截 UTF-8 → 日志非法字节。
func TestV1062MaskKeyUTF8(t *testing.T) {
	if got := MaskKey("abcdef"); got != "ab***" {
		t.Errorf("MaskKey(abcdef) = %q", got)
	}
	if got := MaskKey("ab"); got != "***" {
		t.Errorf("MaskKey(ab) = %q, want ***（过短全掩）", got)
	}
	zh := MaskKey("中文密钥")
	if zh != "中文***" {
		t.Errorf("MaskKey(中文密钥) = %q, want 中文***", zh)
	}
	if !utf8.ValidString(zh) {
		t.Errorf("中文密钥脱敏结果不是合法 UTF-8: %q", zh)
	}
	if strings.ContainsRune(zh, utf8.RuneError) {
		t.Errorf("脱敏结果含替换字符（说明按字节切断了多字节序列）: %q", zh)
	}
}

// TestV1062TZOffsetZoneinfoFirst：P3 回归——IANA 名优先走 zoneinfo。
// 旧实现硬编码表优先，欧美/大洋洲写的是标准时偏移，夏季数据整体偏 1 小时，
// 且永远走不到 LoadLocation 的 DST 计算。
func TestV1062TZOffsetZoneinfoFirst(t *testing.T) {
	// 无 DST 区域：两种路径都应得到同一偏移
	for _, name := range []string{"Asia/Shanghai", "UTC", "Asia/Tokyo"} {
		off, ok := ParseTZOffset(name)
		if !ok {
			t.Fatalf("ParseTZOffset(%q) 解析失败", name)
		}
		want := map[string]int64{"Asia/Shanghai": 8, "UTC": 0, "Asia/Tokyo": 9}[name]
		if off != want*3600*1000000 {
			t.Errorf("ParseTZOffset(%q) = %d 微秒, want %d", name, off, want*3600*1000000)
		}
	}
	// 显式数值偏移不受影响
	if off, ok := ParseTZOffset("+05:30"); !ok || off != (5*3600+30*60)*1000000 {
		t.Errorf("ParseTZOffset(+05:30) = %d, %v", off, ok)
	}
}
