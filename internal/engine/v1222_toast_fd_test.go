// v1.2.22（复审 T-1）：TOAST 段句柄 LRU 的行为锁定。
//
// 背景：旧 openFile 一次性打开全部续段且永不关闭（21 段 = 21 个常驻 FD，多表导出
// 按表累积）。改为按需打开 + 小 LRU 后必须保证三件事，否则比原缺陷更危险：
//  1. 跨段随机读的内容必须逐字节正确（TOAST 是随机读，不能像主表那样只缓存当前段）；
//  2. 同时打开的 FD 不超过上限（否则改动无意义）；
//  3. 并发 worker 下句柄被淘汰/关闭不能发生在读的过程中（否则表现为假 toast_missing）。
package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

const (
	t1222PageSize  = 8192
	t1222Segs      = 6
	t1222PagesPer  = 3
	t1222TotalPage = t1222Segs * t1222PagesPer
)

// pageByte：页号 → 填充值（各页互不相同，便于识别错位）
func pageByte(pg int) byte { return byte(pg*13 + 1) }

// buildToastSegs：造 t1222Segs 个等长段文件（主文件 + .1..N），每页填 pageByte(pg)。
func buildToastSegs(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	base := filepath.Join(dir, "2619")
	for s := 0; s < t1222Segs; s++ {
		p := base
		if s > 0 {
			p = fmt.Sprintf("%s.%d", base, s)
		}
		buf := make([]byte, t1222PageSize*t1222PagesPer)
		for i := 0; i < t1222PagesPer; i++ {
			pg := s*t1222PagesPer + i
			for j := 0; j < t1222PageSize; j++ {
				buf[i*t1222PageSize+j] = pageByte(pg)
			}
		}
		if err := os.WriteFile(p, buf, 0o644); err != nil {
			t.Fatalf("写段 %d 失败: %v", s, err)
		}
	}
	return base
}

func assertPage(t *testing.T, buf []byte, pg int) {
	t.Helper()
	want := pageByte(pg)
	if len(buf) == 0 || buf[0] != want || buf[len(buf)-1] != want {
		got := byte(0)
		if len(buf) > 0 {
			got = buf[0]
		}
		t.Fatalf("页 %d 内容不符：期望全 0x%02x，首字节实得 0x%02x", pg, want, got)
	}
}

// countOpenUnder：统计当前进程打开的、解析后落在 dir 下的 FD 数（仅 Linux 可数）。
func countOpenUnder(dir string) int {
	if runtime.GOOS != "linux" {
		return -1
	}
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	n := 0
	for _, e := range ents {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name()))
		if err == nil && strings.HasPrefix(target, dir) {
			n++
		}
	}
	return n
}

// TestV1222ToastSegRandomCross：跨段随机读，内容逐页正确。
func TestV1222ToastSegRandomCross(t *testing.T) {
	base := buildToastSegs(t)
	tf := &ToastFile{Path: base, PageSize: t1222PageSize}
	defer tf.Close()
	tf.openFile()
	if len(tf.segPaths) != t1222Segs {
		t.Fatalf("识别段数 = %d，期望 %d", len(tf.segPaths), t1222Segs)
	}
	buf := make([]byte, t1222PageSize)
	for k := 0; k < 120; k++ {
		pg := (k*7 + 5) % t1222TotalPage
		if _, err := tf.segReadAt(buf, pg); err != nil {
			t.Fatalf("读页 %d 失败: %v", pg, err)
		}
		assertPage(t, buf, pg)
	}
}

// TestV1222ToastSegFDCap：6 段全部访问后，常驻 FD 不超过 LRU 上限；Close 后归零。
func TestV1222ToastSegFDCap(t *testing.T) {
	base := buildToastSegs(t)
	dir := filepath.Dir(base)
	tf := &ToastFile{Path: base, PageSize: t1222PageSize}
	tf.openFile()
	buf := make([]byte, t1222PageSize)
	// 顺序走完全部页，让 6 个段都进过缓存
	for pg := 0; pg < t1222TotalPage; pg++ {
		if _, err := tf.segReadAt(buf, pg); err != nil {
			t.Fatalf("读页 %d 失败: %v", pg, err)
		}
	}
	if n := countOpenUnder(dir); n >= 0 && n > toastSegCacheMax {
		t.Fatalf("TOAST 段常驻 FD = %d，超过上限 %d（LRU 未生效）", n, toastSegCacheMax)
	}
	tf.Close()
	if n := countOpenUnder(dir); n > 0 {
		t.Fatalf("Close 后仍持有 %d 个 TOAST 段句柄", n)
	}
	// 幂等：重复 Close 不应 panic
	tf.Close()
}

// TestV1222ToastSegDeadMemory：打不开的段记忆为 dead，不反复进缓存。
func TestV1222ToastSegDeadMemory(t *testing.T) {
	c := newToastSegCache([]string{filepath.Join(t.TempDir(), "nope")}, toastSegCacheMax)
	if _, err := c.acquire(0); err == nil {
		t.Fatal("不存在的段应返回错误")
	}
	if !c.dead[0] {
		t.Fatal("失败段未记忆 dead（会在每页读时重复 open）")
	}
	for i := 0; i < 10; i++ {
		if _, err := c.acquire(0); err == nil {
			t.Fatal("dead 段后续读应直接失败")
		}
	}
	if len(c.h) != 0 {
		t.Fatalf("失败段不应进入缓存，实有 %d 个句柄", len(c.h))
	}
	// 越界索引同样安全
	if _, err := c.acquire(-1); err == nil {
		t.Fatal("负索引应返回错误")
	}
	if _, err := c.acquire(99); err == nil {
		t.Fatal("越界索引应返回错误")
	}
	c.Close()
}

// TestV1222ToastSegConcurrent：8 worker 并发随机读（配合 -race），内容正确且无假缺失。
func TestV1222ToastSegConcurrent(t *testing.T) {
	base := buildToastSegs(t)
	tf := &ToastFile{Path: base, PageSize: t1222PageSize}
	defer tf.Close()
	tf.openFile()

	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			buf := make([]byte, t1222PageSize)
			for k := 0; k < 300; k++ {
				pg := (seed*31 + k*7) % t1222TotalPage
				if _, err := tf.segReadAt(buf, pg); err != nil {
					errs <- fmt.Errorf("worker %d 读页 %d: %v", seed, pg, err)
					return
				}
				if buf[0] != pageByte(pg) || buf[t1222PageSize-1] != pageByte(pg) {
					errs <- fmt.Errorf("worker %d 读页 %d 内容错位（句柄被提前关闭？）", seed, pg)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	// 并发读全程不应产生任何"段打开失败"记账
	if segs, _ := tf.SegErrorsSnapshot(); len(segs) != 0 {
		t.Fatalf("并发读出现段错误记账: %v", segs)
	}
}

// TestV1222ToastSegMissingPrimary：主文件缺失时不 panic，读返回 ErrNotExist。
func TestV1222ToastSegMissingPrimary(t *testing.T) {
	tf := &ToastFile{Path: filepath.Join(t.TempDir(), "absent"), PageSize: t1222PageSize}
	defer tf.Close()
	tf.openFile()
	if len(tf.segPaths) != 0 {
		t.Fatalf("主文件缺失时 segPaths 应为空，实得 %d", len(tf.segPaths))
	}
	if _, err := tf.segReadAt(make([]byte, t1222PageSize), 0); err != os.ErrNotExist {
		t.Fatalf("缺失主文件读取应返回 ErrNotExist，实得 %v", err)
	}
}
