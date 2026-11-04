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

// MaskKey 密钥脱敏（加密参数日志用）。
// v1.0.62 修复（P3）：旧实现按字节切 s[:2]，中文密钥（3 字节/字）会被切成
// 半截 UTF-8 序列，日志里出现非法字节（终端显示乱码，某些采集链路会截断/改写）。
func MaskKey(s string) string {
	r := []rune(s)
	if len(r) <= 2 {
		return "***"
	}
	return string(r[:2]) + "***"
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
	// v1.0.62 修复（P3）：改为"先 zoneinfo 再固定表"。
	// 旧顺序是固定表优先，而表里的欧美/大洋洲区域写的是**标准时**偏移，
	// 夏季数据会整体偏 1 小时（且永远不会走到 LoadLocation 的 DST 计算）。
	// 现在优先用系统 zoneinfo 按**当前**时点取偏移（贴近取证/导出时点），
	// 固定表只在容器缺 tzdata、LoadLocation 失败时兜底。
	if loc, err := time.LoadLocation(s); err == nil {
		t := time.Now().In(loc)
		_, offSec := t.Zone()
		return int64(offSec) * 1000000, true
	}
	// 兜底：无 DST 的常用区域（这些值全年恒定，可安全硬编码）
	known := map[string]int64{
		"UTC": 0, "GMT": 0, "Etc/UTC": 0, "Etc/GMT": 0,
		"Asia/Shanghai": 8, "Asia/Singapore": 8, "Asia/Hong_Kong": 8, "Asia/Taipei": 8,
		"Asia/Manila": 8, "Asia/Perth": 8, "Asia/Macau": 8,
		"Asia/Tokyo": 9, "Asia/Seoul": 9, "Asia/Yakutsk": 9,
		"Asia/Kolkata": 5*3600 + 30*60, "Asia/Kathmandu": 5*3600 + 45*60,
		"Asia/Jakarta": 7, "Asia/Bangkok": 7, "Asia/Ho_Chi_Minh": 7, "Asia/Vientiane": 7,
		"Asia/Dubai": 4, "Asia/Riyadh": 3, "Europe/Moscow": 3,
		"America/Sao_Paulo": -3, "Australia/Brisbane": 10, "Australia/Perth": 8,
		"Pacific/Honolulu": -10,
	}
	if v, ok := known[s]; ok {
		return v * 3600 * 1000000, true
	}
	// 兜底（含夏令时的区域，取值按标准时；缺 tzdata 时最多差 1 小时，
	// 比直接判定"无法解析"更有用，故保留但不再优先于 zoneinfo）
	dstFallback := map[string]int64{
		"Europe/London": 0, "Europe/Paris": 1, "Europe/Berlin": 1, "Europe/Rome": 1,
		"America/New_York": -5, "America/Chicago": -6, "America/Denver": -7,
		"America/Los_Angeles": -8, "Australia/Sydney": 10, "Pacific/Auckland": 12,
	}
	if v, ok := dstFallback[s]; ok {
		return v * 3600 * 1000000, true
	}
	return 0, false
}

// ParseConfTimezone 从数据目录 conf 读取 timezone 参数并解析为偏移。
//
// v1.0.46 修复：dbDir 常常是 <datadir>/base/<dboid>（位置参数 / --datadir+--db-oid 场景），
// 旧实现只在 dbDir 本级找 kingbase.conf/postgresql.conf → 永远找不到 → 退回默认 UTC+8，
// 实例实际为 UTC 时 mysql 模式 timestamp 整体偏移 8 小时（实测 2024-06-15 12:34:57 → 20:34:57）。
// 改为像 detectPgVersion 一样逐级向上回溯，并兼容单/双引号、大小写与注释行。
//
// v1.1.1 修复（B15）：同一 conf 内 timezone 可能被多次赋值（initdb 先写 UTC、运维再追加
// Asia/Shanghai，金仓镜像常见），PG 语义为「后出现的覆盖先出现的」，旧实现取首个匹配
// → 实测 V9R1C2B14(mysql 模式) timestamp 导出整体 -8 小时且不报错。
// 现改为取最后一个可解析的赋值；kingbase.auto.conf / postgresql.auto.conf（ALTER SYSTEM 落盘）
// 优先级高于主 conf，先查 auto.conf。
func ParseConfTimezone(dbDir string) (int64, bool) {
	d, _ := filepath.Abs(dbDir)
	for range 6 {
		for _, name := range []string{"kingbase.auto.conf", "postgresql.auto.conf"} {
			if off, ok := lastTimezoneInFile(filepath.Join(d, name)); ok {
				return off, true
			}
		}
		for _, name := range []string{"kingbase.conf", "postgresql.conf", "sys_kingbase.conf"} {
			if off, ok := lastTimezoneInFile(filepath.Join(d, name)); ok {
				return off, true
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

// lastTimezoneInFile 取文件中最后一个可解析的 timezone 赋值（后值覆盖前值），
// 注释行（# 开头）与非 timezone 参数（如 log_timezone）不参与匹配。
func lastTimezoneInFile(path string) (int64, bool) {
	name, ok := lastTimezoneNameInFile(path)
	if !ok {
		return 0, false
	}
	return ParseTZOffset(name)
}

// lastTimezoneNameInFile 取文件中最后一个 timezone 赋值的原始字符串（不做偏移解析）。
// v1.1.1：用于按 IANA 时区名加载完整时区规则（历史 LMT/DST 偏移），
// 固定偏移在 1901 年前与 1986-1991 中国夏令时期间会差 1 小时以内。
func lastTimezoneNameInFile(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	re := regexp.MustCompile(`(?mi)^[ \t]*timezone[ \t]*=[ \t]*['"]?([^'"#\r\n]+)['"]?`)
	ms := re.FindAllSubmatch(data, -1)
	for i := len(ms) - 1; i >= 0; i-- {
		v := strings.TrimSpace(string(ms[i][1]))
		v = strings.TrimRight(v, " \t'\"")
		if v != "" {
			return v, true
		}
	}
	return "", false
}

// ParseConfTimezoneName 从数据目录 conf 读取最后一个 timezone 赋值（原始字符串）。
func ParseConfTimezoneName(dbDir string) (string, bool) {
	d, _ := filepath.Abs(dbDir)
	for range 6 {
		for _, name := range []string{"kingbase.auto.conf", "postgresql.auto.conf",
			"kingbase.conf", "postgresql.conf", "sys_kingbase.conf"} {
			if v, ok := lastTimezoneNameInFile(filepath.Join(d, name)); ok {
				return v, true
			}
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return "", false
}
func QuoteIdent(name string) string {
	if name == "" {
		return `""`
	}
	return "\"" + strings.ReplaceAll(name, "\"", "\"\"") + "\""
}
