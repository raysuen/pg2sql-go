// v1.2.5 参数收口测试：--format 值参数 + 旧三旗标互斥 + --header 仅限 csv。
// 作者: raysuen
package cli

import (
	"testing"

	"pg2sql/internal/logx"
)

// mustExit 断言 args 触发 ErrorExit 且文案含 want 片段。
func mustExit(t *testing.T, args []string, want string) {
	t.Helper()
	hooked := false
	restore := logx.SetErrorExitHookForTest(func(msg string) {
		hooked = true
		if want != "" && !contains(msg, want) {
			t.Errorf("%v 报错文案不含 %q: %s", args, want, msg)
		}
		panic("exit")
	})
	defer restore()
	func() {
		defer func() { _ = recover() }()
		ParseArgs(args)
	}()
	if !hooked {
		t.Errorf("%v 未触发 ErrorExit", args)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func mustNotExit(t *testing.T, args []string) {
	t.Helper()
	restore := logx.SetErrorExitHookForTest(func(msg string) {
		t.Errorf("%v 不应报错: %s", args, msg)
		panic("exit")
	})
	defer restore()
	ParseArgs(args)
}

func TestV125FormatSql(t *testing.T) {
	o := ParseArgs([]string{"f", "--format", "sql"})
	if !o.SQL || o.Data || o.TextFormat || o.Format != "sql" {
		t.Fatalf("--format sql 归一化错误: SQL=%v Data=%v Text=%v", o.SQL, o.Data, o.TextFormat)
	}
}

func TestV125FormatCsv(t *testing.T) {
	o := ParseArgs([]string{"f", "--format", "csv"})
	if o.SQL || !o.Data || o.TextFormat {
		t.Fatalf("--format csv 归一化错误: SQL=%v Data=%v Text=%v", o.SQL, o.Data, o.TextFormat)
	}
}

func TestV125FormatText(t *testing.T) {
	o := ParseArgs([]string{"f", "--format", "text"})
	if o.SQL || !o.Data || !o.TextFormat {
		t.Fatalf("--format text 归一化错误: SQL=%v Data=%v Text=%v", o.SQL, o.Data, o.TextFormat)
	}
}

func TestV125FormatInvalidValue(t *testing.T) {
	mustExit(t, []string{"f", "--format", "json"}, "仅支持 sql/csv/text")
}

func TestV125FormatDuplicate(t *testing.T) {
	mustExit(t, []string{"f", "--format", "csv", "--format", "text"}, "只能指定一次")
}

func TestV125FormatMixedWithOldFlags(t *testing.T) {
	for _, args := range [][]string{
		{"f", "--format", "csv", "--sql"},
		{"f", "--format", "sql", "--data"},
		{"f", "--format", "text", "--text"},
	} {
		mustExit(t, args, "不能混用")
	}
}

func TestV125OldFlagsMutualExclusive(t *testing.T) {
	for _, args := range [][]string{
		{"f", "--sql", "--data"}, // v1.2.4 静默覆盖点，v1.2.5 修复
		{"f", "--sql", "--text"},
		{"f", "--data", "--text"}, // 新增拦截
		{"f", "--sql", "--data", "--text"},
	} {
		mustExit(t, args, "互斥")
	}
}

func TestV125OldFlagsSingleOK(t *testing.T) {
	for _, args := range [][]string{
		{"f", "--sql"},
		{"f", "--data"},
		{"f", "--text"},
		{"f"}, // 默认 sql
	} {
		mustNotExit(t, args)
	}
}

func TestV125HeaderCsvOnly(t *testing.T) {
	// csv 两种写法都允许 --header
	for _, args := range [][]string{
		{"f", "--data", "--header"},
		{"f", "--format", "csv", "--header"},
	} {
		mustNotExit(t, args)
	}
	// sql / text / 默认 / ddl 场景 --header 一律报错
	for _, args := range [][]string{
		{"f", "--sql", "--header"},
		{"f", "--header"}, // 默认 sql
		{"f", "--format", "sql", "--header"},
		{"f", "--text", "--header"},
		{"f", "--format", "text", "--header"},
		{"f", "--ddl", "--header"},
	} {
		mustExit(t, args, "--header")
	}
}

func TestV125FormatWithDdlParallel(t *testing.T) {
	o := ParseArgs([]string{"f", "--format", "csv", "--ddl"})
	if !o.DDL || !o.Data || o.SQL {
		t.Fatalf("--format csv --ddl 应并行: DDL=%v Data=%v SQL=%v", o.DDL, o.Data, o.SQL)
	}
}
