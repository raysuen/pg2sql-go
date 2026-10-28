// Package util 提供 pg2sql 各模块共用的小工具（文件、时间、时区、脱敏）。
// Author: raysuen
package util

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// FileExists 常规文件存在性判断（TDE 钱包/密钥文件探测用）。
func FileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// StatSize 文件字节数（不存在/错误返回 0）。
func StatSize(p string) int64 {
	if p == "" {
		return 0
	}
	if fi, err := os.Stat(p); err == nil {
		return fi.Size()
	}
	return 0
}

// NowMs 当前毫秒时间戳。
func NowMs() int64 {
	return time.Now().UnixMilli()
}

// MaskKey 密钥脱敏（TDE 参数日志用）。
func MaskKey(s string) string {
	if len(s) <= 2 {
		return "***"
	}
	return s[:2] + "***"
}

// ParseTZOffset 解析时区表示为微秒偏移。支持 ±HH:MM / ±HHMM / ±HH、
// 常见 IANA 时区名表，其它 IANA 名用 zoneinfo 推算 2000-01-01 偏移（实例固定时区近似）。
func ParseTZOffset(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '+' || s[0] == '-') {
		sign := int64(1)
		if s[0] == '-' {
			sign = -1
		}
		body := s[1:]
		var h, m int
		var err error
		switch len(body) {
		case 5: // ±HH:MM
			// v1.0.49 修复（C1）：Atoi 错误此前被忽略，"+ab:cd" 被当 (0,0) 静默接受为 UTC
			h, err = strconv.Atoi(body[:2])
			if err != nil || body[2] != ':' {
				return 0, false
			}
			m, err = strconv.Atoi(body[3:])
			if err != nil {
				return 0, false
			}
		case 4: // ±HHMM
			h, err = strconv.Atoi(body[:2])
			if err != nil {
				return 0, false
			}
			m, err = strconv.Atoi(body[2:])
			if err != nil {
				return 0, false
			}
		case 1, 2: // ±HH
			h, err = strconv.Atoi(body)
			if err != nil {
				return 0, false
			}
		default:
			return 0, false
		}
		if h < 0 || h > 14 || m < 0 || m > 59 {
			return 0, false
		}
		return sign * int64(h*3600+m*60) * 1000000, true
	}
	known := map[string]int64{
		"UTC": 0, "GMT": 0, "Etc/UTC": 0, "Etc/GMT": 0,
		"Asia/Shanghai": 8, "Asia/Singapore": 8, "Asia/Hong_Kong": 8, "Asia/Taipei": 8,
		"Asia/Manila": 8, "Asia/Perth": 8, "Asia/Macau": 8,
		"Asia/Tokyo": 9, "Asia/Seoul": 9, "Asia/Yakutsk": 9,
		"Asia/Kolkata": 5*3600 + 30*60, "Asia/Kathmandu": 5*3600 + 45*60,
		"Asia/Jakarta": 7, "Asia/Bangkok": 7, "Asia/Ho_Chi_Minh": 7, "Asia/Vientiane": 7,
		"Asia/Dubai": 4, "Asia/Riyadh": 3, "Europe/Moscow": 3,
		"Europe/London": 0, "Europe/Paris": 1, "Europe/Berlin": 1, "Europe/Rome": 1,
		"America/New_York": -5, "America/Chicago": -6, "America/Denver": -7,
		"America/Los_Angeles": -8, "America/Sao_Paulo": -3,
		"Australia/Sydney": 10, "Australia/Brisbane": 10, "Australia/Perth": 8,
		"Pacific/Auckland": 12, "Pacific/Honolulu": -10,
	}
	if v, ok := known[s]; ok {
		return v * 3600 * 1000000, true
	}
	if loc, err := time.LoadLocation(s); err == nil {
		t := time.Date(2000, 1, 1, 0, 0, 0, 0, loc)
		_, offSec := t.Zone()
		return int64(offSec) * 1000000, true
	}
	return 0, false
}

// ParseConfTimezone 从数据目录 conf 读取 timezone 参数并解析为偏移。
//
// v1.0.46 修复：dbDir 常常是 <datadir>/base/<dboid>（位置参数 / --datadir+--db-oid 场景），
// 旧实现只在 dbDir 本级找 kingbase.conf/postgresql.conf → 永远找不到 → 退回默认 UTC+8，
// 实例实际为 UTC 时 mysql 模式 timestamp 整体偏移 8 小时（实测 2024-06-15 12:34:57 → 20:34:57）。
// 改为像 detectPgVersion 一样逐级向上回溯，并兼容单/双引号、大小写与注释行。
func ParseConfTimezone(dbDir string) (int64, bool) {
	re := regexp.MustCompile(`(?mi)^[ \t]*(?:timezone)[ \t]*=[ \t]*['"]?([^'"#\r\n]+)['"]?`)
	d, _ := filepath.Abs(dbDir)
	for range 6 {
		for _, name := range []string{"kingbase.conf", "postgresql.conf", "sys_kingbase.conf"} {
			data, err := os.ReadFile(filepath.Join(d, name))
			if err != nil {
				continue
			}
			if m := re.FindSubmatch(data); m != nil {
				if off, ok := ParseTZOffset(strings.TrimSpace(string(m[1]))); ok {
					return off, true
				}
			}
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return 0, false
}
func QuoteIdent(name string) string {
	if name == "" {
		return `""`
	}
	return "\"" + strings.ReplaceAll(name, "\"", "\"\"") + "\""
}
