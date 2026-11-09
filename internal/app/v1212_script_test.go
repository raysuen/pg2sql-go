// v1.2.12：批量导入脚本"无数据步骤不生成"（A4）回归。
// 作者: raysuen
package app

import (
	"os"
	"path/filepath"
	"testing"

	"pg2sql/internal/integrity"
)

// TestV1212ImportScriptEmptyNotWritten：v1.2.12（A4）回归。
//
// 背景：守卫写作 `if len(lines) <= 11`，但头部实际是 12 行（shebang + 2 注释 +
// 用法 + 4 环境变量 + ensureDB 4 行 + set -e），条件永远为假。全部条目标记失败
// 时仍会写出一个只有建库、没有任何数据步骤的 import_all.sh——用户照着执行
// 会得到"导入成功"的错觉，实际一行数据都没进。
func TestV1212ImportScriptEmptyNotWritten(t *testing.T) {
	dir := t.TempDir()
	entries := []integrity.ExportReportEntry{
		{Table: "t1", File: filepath.Join(dir, "t1.sql"), Format: "sql", Error: "坏页"},
		{Table: "t2", File: filepath.Join(dir, "t2.sql"), Format: "sql", Error: "坏页"},
	}
	makeImportAllScript(dir, entries, false, ",", nil)

	if _, err := os.Stat(filepath.Join(dir, "import_all.sh")); err == nil {
		t.Fatalf("全部条目失败时不应生成 import_all.sh（会误导用户以为导入成功）")
	}
}

// TestV1212ImportScriptWithDataWritten：有成功条目时脚本必须照常生成，
// 且包含数据步骤（回归护栏，防止修复把正常路径一起关掉）。
func TestV1212ImportScriptWithDataWritten(t *testing.T) {
	dir := t.TempDir()
	entries := []integrity.ExportReportEntry{
		{Table: "t1", File: filepath.Join(dir, "t1.sql"), Format: "sql"},
	}
	makeImportAllScript(dir, entries, false, ",", nil)

	p := filepath.Join(dir, "import_all.sh")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("有成功条目时必须生成脚本: %v", err)
	}
	if len(b) == 0 {
		t.Fatalf("脚本为空")
	}
	s := string(b)
	if !containsStr(s, "t1.sql") || !containsStr(s, "set -e") {
		t.Fatalf("脚本缺少数据步骤或 shell 头: %s", s)
	}
}

func containsStr(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
