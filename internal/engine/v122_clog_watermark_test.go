// v1.2.2 第二轮代码分析修复的回归测试（BUG-3 CLOG 水位校验）
// Author: raysuen
package engine

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// writeControl：造一个最小 pg_control，只需 offset 64 处的 nextXid 有效
func writeControl(t *testing.T, datadir string, name string, nextXid uint32) {
	t.Helper()
	buf := make([]byte, 300)
	binary.LittleEndian.PutUint32(buf[clogCtlOffset:clogCtlOffset+4], nextXid)
	if err := os.MkdirAll(filepath.Join(datadir, "global"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(datadir, "global", name), buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestB3FutureXidIsUnknown：xid >= nextXid 必须返回 -1（不可判），而不是把
// 邻接事务的残留 bit 当成 COMMITTED/ABORTED 的强结论。
//
// 复现场景：段文件里 xid=200 处写了 ABORTED，但实例 nextXid=150——说明 200
// 这个事务根本还没开始，那 2 bit 是别人留下的。旧实现会返回 ABORTED，
// 把一个垃圾 xmin 的元组判死（静默丢行）。
func TestB3FutureXidIsUnknown(t *testing.T) {
	dd := t.TempDir()
	writeClogSeg(t, filepath.Join(dd, "pg_xact"), 0, map[uint32]int{
		100: ClogCommitted,
		200: ClogAborted,
	})
	writeControl(t, dd, "pg_control", 150)

	c := NewClogReader(dd)
	if !c.Available() {
		t.Fatalf("CLOG 应可用，dir=%q", c.Dir())
	}
	if c.NextXid != 150 {
		t.Fatalf("nextXid 未加载：got %d want 150", c.NextXid)
	}

	// 水位内：正常判定
	if got := c.Status(100); got != ClogCommitted {
		t.Fatalf("xid=100 应判 COMMITTED，got %d", got)
	}
	// 水位外：必须不可判
	if got := c.Status(200); got != -1 {
		t.Fatalf("xid=200 >= nextXid 应判不可判(-1)，got %d（BUG-3 回归）", got)
	}
	if got := c.Status(149); got != -1 && got != ClogInProgress {
		// 149 在水位内但没有对应的段内记录（槽位为 0 = IN_PROGRESS），合法
		t.Fatalf("xid=149 结果异常: %d", got)
	}
	// 边界：xid == nextXid 同样越界
	if got := c.Status(150); got != -1 {
		t.Fatalf("xid=nextXid 边界应判不可判(-1)，got %d", got)
	}
}

// TestB3NoControlFileDegrades：控制文件缺失时不校验水位（NextXid=0），
// 保持旧的判定能力，不能因为读不到 nextXid 就整体降级。
func TestB3NoControlFileDegrades(t *testing.T) {
	dd := t.TempDir()
	writeClogSeg(t, filepath.Join(dd, "pg_xact"), 0, map[uint32]int{
		200: ClogAborted,
	})
	c := NewClogReader(dd)
	if c.NextXid != 0 {
		t.Fatalf("无控制文件时 NextXid 应为 0，got %d", c.NextXid)
	}
	if got := c.Status(200); got != ClogAborted {
		t.Fatalf("无水位时应正常判定 ABORTED，got %d", got)
	}
}

// TestB3WatermarkCountsUnknown：越界查询要计入 Unknown/Queries，便于自检文案反映。
func TestB3WatermarkCountsUnknown(t *testing.T) {
	dd := t.TempDir()
	writeClogSeg(t, filepath.Join(dd, "pg_xact"), 0, map[uint32]int{100: ClogCommitted})
	writeControl(t, dd, "pg_control", 120)

	c := NewClogReader(dd)
	for i := 0; i < 5; i++ {
		_ = c.Status(999) // 全部越界
	}
	q, _, _, unk := c.Stats()
	if unk != 5 || q != 5 {
		t.Fatalf("越界查询统计错误：queries=%d unknown=%d，want 5/5", q, unk)
	}
}

// TestB3KingbaseSysControl：金仓用 sys_control，同样要能读到 nextXid。
func TestB3KingbaseSysControl(t *testing.T) {
	dd := t.TempDir()
	writeClogSeg(t, filepath.Join(dd, "sys_xact"), 0, map[uint32]int{50: ClogAborted})
	writeControl(t, dd, "sys_control", 80)

	c := NewClogReader(dd)
	if !c.Available() {
		t.Fatalf("金仓 sys_xact 应可用")
	}
	if c.NextXid != 80 {
		t.Fatalf("sys_control 的 nextXid 未加载：got %d want 80", c.NextXid)
	}
	if got := c.Status(50); got != ClogAborted {
		t.Fatalf("xid=50 应判 ABORTED，got %d", got)
	}
	if got := c.Status(100); got != -1 {
		t.Fatalf("xid=100 >= nextXid 应判不可判，got %d", got)
	}
}
