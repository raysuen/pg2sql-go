// v1.2.18 回归用例：SQL 会话头的模式门控。
//
// 核心断言是**反向**的：SET TIME ZONE 只能出现在金仓 mysql 兼容模式，
// 绝不可泄漏到 PG / oracle 模式 —— 否则会改变 v1.2.16 已经校准的正确行为
// （PG 模式 timestamptz 带偏移、oracle 模式有自己的渲染规则）。
package app

import (
	"strings"
	"testing"

	"pg2sql/internal/datatype"
)

func TestV1218SessionHeaderMySQLModeIncludesTimeZone(t *testing.T) {
	old := datatype.IsKbMysqlMode()
	t.Cleanup(func() { datatype.SetKbMysqlMode(old) })

	datatype.SetKbMysqlMode(true)
	h := sqlSessionHeader()

	if !strings.Contains(h, "SET client_encoding = 'UTF8';") {
		t.Errorf("缺少 client_encoding，实际：%q", h)
	}
	if !strings.Contains(h, "SET standard_conforming_strings = on;") {
		t.Errorf("缺少 standard_conforming_strings，实际：%q", h)
	}
	if !strings.Contains(h, "SET TIME ZONE ") {
		t.Fatalf("mysql 模式必须钉住会话时区（否则 timestamptz / 分区边界整体平移 8 小时），实际：%q", h)
	}
	// 顺序：TIME ZONE 必须在同一个头部里、且在两条 SET 之后
	if strings.Index(h, "SET TIME ZONE") < strings.Index(h, "standard_conforming_strings") {
		t.Errorf("SET TIME ZONE 应排在两条基础 SET 之后，实际：%q", h)
	}
}

func TestV1218SessionHeaderPgModeNoTimeZone(t *testing.T) {
	old := datatype.IsKbMysqlMode()
	t.Cleanup(func() { datatype.SetKbMysqlMode(old) })

	datatype.SetKbMysqlMode(false)
	h := sqlSessionHeader()

	if strings.Contains(h, "TIME ZONE") {
		t.Errorf("非 mysql 模式不得出现 SET TIME ZONE（会改变 v1.2.16 已校准的 PG/oracle 行为），实际：%q", h)
	}
	want := "SET client_encoding = 'UTF8';\nSET standard_conforming_strings = on;\n"
	if h != want {
		t.Errorf("PG 模式的会话头必须与 v1.2.17 逐字节一致\n got: %q\nwant: %q", h, want)
	}
}
