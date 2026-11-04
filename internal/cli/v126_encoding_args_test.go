// v1.2.6 参数测试：--encoding 白名单校验（P3）。
// 作者: raysuen
package cli

import "testing"

func TestV126EncodingAliasOK(t *testing.T) {
	// 下划线别名（老 README 写法）与规范名、auto 均应通过
	for _, args := range [][]string{
		{"f", "--encoding", "euc_cn"},
		{"f", "--encoding", "euc-cn"},
		{"f", "--encoding", "gbk"},
		{"f", "--encoding", "gb18030"},
		{"f", "--encoding", "sql_ascii"},
		{"f", "--encoding", "auto"},
		{"f"}, // 默认 auto
	} {
		mustNotExit(t, args)
	}
}

func TestV126EncodingUnknownRejected(t *testing.T) {
	// 修复前 win1252 等未知名静默落入 latin-1 兜底乱码；现解析期报错
	for _, args := range [][]string{
		{"f", "--encoding", "win1252"},
		{"f", "--encoding", "utf8mb4"},
		{"f", "--encoding", "euckr"},
	} {
		mustExit(t, args, "--encoding")
	}
}
