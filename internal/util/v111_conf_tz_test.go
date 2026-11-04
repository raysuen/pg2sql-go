// Author: raysuen
package util

import (
	"os"
	"path/filepath"
	"testing"
)

// TestV111ConfTimezoneLastWins：B15 回归——同一 conf 内 timezone 多次赋值时，
// PG 语义为「后出现的覆盖先出现的」。金仓镜像常见 initdb 写 UTC + 运维追加 Asia/Shanghai，
// 旧实现取首个匹配 → mysql 模式 timestamp 导出整体 -8 小时且静默。
func TestV111ConfTimezoneLastWins(t *testing.T) {
	dir := t.TempDir()
	conf := "" +
		"#timezone = 'Europe/London'\n" +
		"log_timezone = 'UTC'\n" +
		"timezone = 'UTC'\n" +
		"timezone = 'Asia/Shanghai'\n"
	if err := os.WriteFile(filepath.Join(dir, "kingbase.conf"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	off, ok := ParseConfTimezone(dir)
	if !ok {
		t.Fatal("ParseConfTimezone 未解析出 timezone")
	}
	if want := int64(8 * 3600 * 1000000); off != want {
		t.Errorf("offset = %d, want %d（应取最后一个赋值 Asia/Shanghai，而非首个 UTC）", off, want)
	}
}

// TestV111ConfTimezoneAutoOverride：kingbase.auto.conf（ALTER SYSTEM）优先级高于主 conf。
func TestV111ConfTimezoneAutoOverride(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kingbase.conf"), []byte("timezone = 'UTC'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kingbase.auto.conf"), []byte("timezone = 'Asia/Tokyo'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	off, ok := ParseConfTimezone(dir)
	if !ok {
		t.Fatal("ParseConfTimezone 未解析出 timezone")
	}
	if want := int64(9 * 3600 * 1000000); off != want {
		t.Errorf("offset = %d, want %d（auto.conf 覆盖主 conf）", off, want)
	}
}

// TestV111ConfTimezoneFromSubdir：dbDir 为 <datadir>/base/<oid> 时需向上回溯找到 conf。
func TestV111ConfTimezoneFromSubdir(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "base", "16069")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "kingbase.conf"), []byte("timezone = 'UTC'\ntimezone = 'Asia/Shanghai'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	off, ok := ParseConfTimezone(sub)
	if !ok {
		t.Fatal("ParseConfTimezone 未能从 base/<oid> 向上回溯到 conf")
	}
	if want := int64(8 * 3600 * 1000000); off != want {
		t.Errorf("offset = %d, want %d", off, want)
	}
}
