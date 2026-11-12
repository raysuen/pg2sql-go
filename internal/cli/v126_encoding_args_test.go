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
		{"f", "--encoding", "win1252"}, // v1.2.13：单字节族已实现
		{"f"},                          // 默认 auto
	} {
		mustNotExit(t, args)
	}
}

func TestV126EncodingUnknownRejected(t *testing.T) {
	// 修复前 win1252 等未知名静默落入 latin-1 兜底乱码；现解析期报错
	// v1.2.13：win1252 已实现（按真实码表解码），从拒绝列表移除，改为支持断言
	for _, args := range [][]string{
		{"f", "--encoding", "utf8mb4"},
		{"f", "--encoding", "euckr"},
		{"f", "--encoding", "win9999"},
	} {
		mustExit(t, args, "--encoding")
	}
}
