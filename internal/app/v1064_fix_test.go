// v1.0.64 深审修复回归用例：
// E-1 虚拟生成列跳过 / E-3 \copy 注入转义 / E-5 DELIMITER 联动 /
// E-6 排序 / E-7 ensure-DB 前置段
package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pg2sql/internal/datatype"
	"pg2sql/internal/integrity"
)

// ---- E-1：虚拟生成列不落盘，ExtractFieldsDirect 不消费字节 ----

func TestV1064VirtualColumnSkip(t *testing.T) {
	// 布局：col1 int4 | col2 virtual | col3 int4
	// 磁盘上只有 col1+col3 共 8 字节；若 virtual 被消费，col3 会读到 4..8 之外
	raw := []byte{0x11, 0, 0, 0, 0x22, 0, 0, 0}
	cols := []datatype.ColLen{
		{AttLen: 4, AttAlign: "i"},
		{AttLen: 4, AttAlign: "i", Virtual: true},
		{AttLen: 4, AttAlign: "i"},
	}
	fields := datatype.ExtractFieldsDirect(raw, 0, []bool{false, false, false}, cols)
	if fields[0] == nil || (*fields[0])[0] != 0x11 {
		t.Fatalf("col1 解析错误: %v", fields[0])
	}
	if fields[1] != nil {
		t.Fatalf("virtual 列应无数据: %v", *fields[1])
	}
	if fields[2] == nil || (*fields[2])[0] != 0x22 {
		t.Fatalf("col3 应读到第二组 4 字节（virtual 未消费字节）: %v", fields[2])
	}
}

// ---- E-3/E-5/E-6/E-7：import_all.sh 注入转义 / DELIMITER / 排序 / ensure-DB ----

func TestV1064ImportAllScript(t *testing.T) {
	dir := t.TempDir()
	entries := []integrity.ExportReportEntry{
		{Table: `pub."b$(touch /tmp/pwned)b"`, File: filepath.Join(dir, "zz_b.csv"), Format: "csv"},
		{Table: "pub.aa", File: filepath.Join(dir, "aa.csv"), Format: "csv"},
	}
	makeImportAllScript(dir, entries, true, "|", nil)
	data, err := os.ReadFile(filepath.Join(dir, "import_all.sh"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	// E-3：表名里的 $( 不得原样出现在双引号上下文
	if strings.Contains(s, `b$(touch`) {
		t.Fatalf("表名 $() 未转义，存在命令注入: %s", s)
	}
	if !strings.Contains(s, `b\$\(touch`) && !strings.Contains(s, `b\$(touch`) {
		t.Fatalf("表名应输出转义形态（\\$\\()")
	}
	// E-5：DELIMITER 与导出联动
	if !strings.Contains(s, `DELIMITER '|'`) {
		t.Fatalf("DELIMITER 应为 '|'（导出时 --delimiter 实值）")
	}
	if strings.Contains(s, `DELIMITER ','`) {
		t.Fatalf("不应再出现硬编码 ','")
	}
	// E-6：条目按文件名排序（aa 在 zz 前，与传入顺序相反）
	iAA := strings.Index(s, "aa.csv")
	iZZ := strings.Index(s, "zz_b.csv")
	if iAA < 0 || iZZ < 0 || iAA > iZZ {
		t.Fatalf("条目未按文件名排序")
	}
	// E-7：目标库存在性前置段（生成文本里 "$DB" 带 \" 转义）
	if !strings.Contains(s, "SELECT 1 FROM pg_database WHERE datname='$DB'") ||
		!strings.Contains(s, `CREATE DATABASE \"`) {
		t.Fatalf("缺少 ensure-DB 前置段")
	}
}

// E-3 补充：单表 csv 脚本的文件名与列名转义
func TestV1064ImportScriptEscape(t *testing.T) {
	if got := shellDQLEscape("a`b'c"); strings.Count(got, "`") != 1 || !strings.Contains(got, "\\`") {
		t.Fatalf("反引号未转义: %q", got)
	}
	if got := shellSQEscape("x'y"); got != `x'\''y` {
		t.Fatalf("单引号转义错误: %q", got)
	}
}
