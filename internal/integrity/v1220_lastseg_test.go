package integrity

import (
	"os"
	"path/filepath"
	"testing"
)

// v1.2.20：末段恰满 1GB 段上限的弱信号判定。
// 用 os.Truncate 稀疏文件构造 1GB 尾段（不占磁盘）。
func TestV1220LastSegExactlyFull(t *testing.T) {
	dir := t.TempDir()
	ps := 8192
	main := filepath.Join(dir, "16391")
	seg1 := main + ".1"
	if err := os.WriteFile(main, make([]byte, ps*10), 0644); err != nil {
		t.Fatal(err)
	}
	// 末段不满 → false
	if err := os.WriteFile(seg1, make([]byte, ps), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(seg1, int64(ps)*100); err != nil {
		t.Fatal(err)
	}
	if LastSegExactlyFull(main, ps) {
		t.Error("末段 100 页（不满）不应命中弱信号")
	}
	// 末段恰满 RELSEG_SIZE（131072 页 × 8K = 1GB，稀疏）→ true
	if err := os.Truncate(seg1, int64(ps)*131072); err != nil {
		t.Fatal(err)
	}
	if !LastSegExactlyFull(main, ps) {
		t.Error("末段恰满 1GB 应命中弱信号")
	}
	// 无续段、主文件小 → false
	if LastSegExactlyFull(filepath.Join(dir, "nonexistent"), ps) {
		t.Error("不存在的文件不应命中")
	}
}
