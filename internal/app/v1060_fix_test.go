package app

// v1.0.60 回归测试（P2-12）：输出文件名穿越 + 生成脚本的命令注入。

import (
	"path/filepath"
	"strings"
	"testing"

	"pg2sql/internal/engine"
)

// 表名/schema 来自目标库目录（可被构造）：带 / 或 .. 时不得写出 -o 目录之外。
func TestV1060OutputPathTraversal(t *testing.T) {
	dir := t.TempDir() + "/"
	cases := []struct{ schema, rel string }{
		{"../../etc", "passwd"},
		{"public", "../../../tmp/evil"},
		{"..", ".."},
		{"a/b", `c\d`},
	}
	for _, c := range cases {
		tm := &engine.TableMeta{Schema: c.schema, RelName: c.rel}
		got := resolveOutputPath(dir, tm, "sql")
		// 规范化后必须仍落在目标目录内
		absDir, _ := filepath.Abs(dir)
		absGot, _ := filepath.Abs(got)
		if !strings.HasPrefix(absGot, absDir+string(filepath.Separator)) {
			t.Errorf("schema=%q rel=%q → %q 逃出了 %q", c.schema, c.rel, absGot, absDir)
		}
	}
}

// 正常标识符不应被改写（大写/中文/空格保留，回灌靠引号处理）。
func TestV1060OutputPathKeepsNormalNames(t *testing.T) {
	dir := t.TempDir() + "/"
	tm := &engine.TableMeta{Schema: "public", RelName: "订单 表"}
	got := resolveOutputPath(dir, tm, "csv")
	if !strings.HasSuffix(got, "public.订单 表.csv") {
		t.Fatalf("正常表名被改写: %q", got)
	}
}

// 嵌入 sh 双引号上下文：$(...)、反引号、引号、反斜杠必须被转义。
func TestV1060ShellDQLEscape(t *testing.T) {
	in := `"x"; $(rm -rf /) ` + "`id`" + ` \n`
	out := shellDQLEscape(in)
	for _, bad := range []string{"$(", "`id`"} {
		if strings.Contains(out, bad) && !strings.Contains(out, `\`+string(bad[0])) {
			t.Errorf("%q 未转义: %q", bad, out)
		}
	}
	if !strings.Contains(out, `\"x\"`) {
		t.Errorf("双引号未转义: %q", out)
	}
	if !strings.Contains(out, `\$(`) {
		t.Errorf("$ 未转义: %q", out)
	}
}

func TestV1060ShellSQEscape(t *testing.T) {
	out := shellSQEscape("a'b")
	if out != `a'\''b` {
		t.Fatalf("单引号转义错误: %q", out)
	}
}
