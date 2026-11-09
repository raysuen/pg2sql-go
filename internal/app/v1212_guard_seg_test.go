// Author: raysuen

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGuardOutputAgainstSourceSegment：v1.2.12（A1）回归。
//
// 背景：PG 单段上限 1GB，超了就有 16391.1 / 16391.2 等续段。v1.0.59 的守卫
// 只用传入的 sources（主段）做 os.SameFile 比对，`-o 16391.1` 时比对不到 →
// os.Create(O_TRUNC) 先截断续段、再回去读它，源数据段清零且导出 0 行。
//
// 断言：主段、每个续段、以及继承段（.1 = 主段的硬链接）都必须被拒绝；
// 路径里带 .1 但是同名目录下另一张表（16392.1）不得误拒。
func TestGuardOutputAgainstSourceSegment(t *testing.T) {
	dir := t.TempDir()
	rel := filepath.Join(dir, "16391")
	seg1 := rel + ".1"

	if err := os.WriteFile(rel, []byte("main"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(seg1, []byte("seg1"), 0644); err != nil {
		t.Fatal(err)
	}
	// 已有的输出文件（守卫只在目标文件已存在时才可能拦截，与生产路径一致）
	other := filepath.Join(dir, "16392.1")
	if err := os.WriteFile(other, []byte("other"), 0644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		out     string
		wantErr bool
	}{
		{"主段自身", rel, true},
		{"续段 .1", seg1, true},
		{"续段经符号链接", filepath.Join(dir, "link.1"), true},
		{"无关表续段", other, false},
	}
	// 符号链接指向续段
	if err := os.Symlink(seg1, filepath.Join(dir, "link.1")); err != nil {
		t.Fatal(err)
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := guardOutputAgainstSource(c.out, rel, "")
			if c.wantErr && err == nil {
				t.Fatalf("期望拒绝（%s 与源数据文件同文件），实际放行——续段截断会导致源库清零", c.out)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("期望放行（%s 与源不同文件），实际误拒: %v", c.out, err)
			}
		})
	}
}

// TestGuardOutputAgainstSourceEmptySources：空 sources 不得 panic，且放行。
func TestGuardOutputAgainstSourceEmptySources(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "x")
	if err := os.WriteFile(out, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := guardOutputAgainstSource(out, "", ""); err != nil {
		t.Fatalf("空源路径不应报错: %v", err)
	}
}

// TestGuardOutputAgainstSourceNotExist：输出文件不存在时守卫直接放行
// （os.Create 会新建，不存在截断风险）。
func TestGuardOutputAgainstSourceNotExist(t *testing.T) {
	dir := t.TempDir()
	rel := filepath.Join(dir, "17000")
	if err := os.WriteFile(rel, []byte("main"), 0644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "17000.out")
	if err := guardOutputAgainstSource(out, rel); err != nil {
		t.Fatalf("输出文件不存在应放行: %v", err)
	}
}

// TestGuardSegErrorMessage：拒绝信息里要能看出命中哪个续段，便于用户改正 -o。
func TestGuardSegErrorMessage(t *testing.T) {
	dir := t.TempDir()
	rel := filepath.Join(dir, "16391")
	seg := rel + ".2"
	for _, p := range []string{rel, rel + ".1"} {
		if err := os.WriteFile(p, []byte("m"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(seg, []byte("m"), 0644); err != nil {
		t.Fatal(err)
	}
	err := guardOutputAgainstSource(seg, rel)
	if err == nil {
		t.Fatal("期望拒绝 .2 续段")
	}
	if !strings.Contains(err.Error(), ".2") {
		t.Fatalf("拒绝信息应指明命中的续段，实际: %v", err)
	}
}
