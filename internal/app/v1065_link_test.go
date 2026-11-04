// v1.0.65 修复回归用例（导出链路侧）：
// F16 import_all.sh 依赖序 / F18 --report-file symlink 同文件判定 / F8 虚拟列剥离 NOT NULL
package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pg2sql/internal/integrity"
)

// ---- F16：import_all.sh 必须按任务依赖序（父表先于子表），而非文件名序 ----

func TestV1065ImportAllDependencyOrder(t *testing.T) {
	dir := t.TempDir()
	// 文件名序（aa 在前）与依赖序（parent 在前）故意相反
	entries := []integrity.ExportReportEntry{
		{Table: "s.child", File: filepath.Join(dir, "aa_child.csv"), Format: "csv"},
		{Table: "s.parent", File: filepath.Join(dir, "zz_parent.csv"), Format: "csv"},
	}
	makeImportAllScript(dir, entries, true, "|", []string{"s.parent", "s.child"})
	data, err := os.ReadFile(filepath.Join(dir, "import_all.sh"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	ip := strings.Index(s, "zz_parent.csv")
	ic := strings.Index(s, "aa_child.csv")
	if ip < 0 || ic < 0 {
		t.Fatalf("脚本缺少表导入行:\n%s", s)
	}
	if ip > ic {
		t.Fatalf("import_all.sh 未按依赖序排列（父表应在子表之前）:\n%s", s)
	}
}

// order 缺项时也必须稳定：未列出的表按文件名序排在已列出的表之后。
func TestV1065ImportAllOrderFallback(t *testing.T) {
	dir := t.TempDir()
	entries := []integrity.ExportReportEntry{
		{Table: "s.b", File: filepath.Join(dir, "b.csv"), Format: "csv"},
		{Table: "s.a", File: filepath.Join(dir, "a.csv"), Format: "csv"},
		{Table: "s.c", File: filepath.Join(dir, "c.csv"), Format: "csv"},
	}
	makeImportAllScript(dir, entries, true, "|", []string{"s.b"})
	data, err := os.ReadFile(filepath.Join(dir, "import_all.sh"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	ib := strings.Index(s, "b.csv")
	ia := strings.Index(s, "a.csv")
	if ib < 0 || ia < 0 || ib > ia {
		t.Fatalf("已列出的表应排在最前，其余按文件名序:\n%s", s)
	}
}

// ---- F18：--report-file 与数据目录同一文件时，symlink 不能绕过判定 ----

func TestV1065SameFilePathSymlink(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("当前文件系统不支持 symlink: %v", err)
	}

	// 同一文件、不同路径表示（经 symlink 目录）→ 必须判为同一文件
	a := filepath.Join(real, "report.json")
	b := filepath.Join(link, "report.json")
	if !sameFilePath(a, b) {
		t.Fatalf("symlink 目录下的同名文件应判为同一路径: %s vs %s", a, b)
	}
	// 目录本身也一致（resolveParentLinks 走 EvalSymlinks）
	if !sameFilePath(real, link) {
		t.Fatalf("symlink 目录应指向真实目录")
	}
	// 不同文件名 → 不同文件
	if sameFilePath(filepath.Join(real, "x.json"), filepath.Join(real, "y.json")) {
		t.Fatalf("不同文件名不应判为同一文件")
	}
	// 同一路径原样传入
	if !sameFilePath(filepath.Join(real, "same.json"), filepath.Join(real, "same.json")) {
		t.Fatalf("相同路径必须判为同一文件")
	}
}
