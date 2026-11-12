// SQL 会话头部（v1.2.18）。
//
// 历史：`SET client_encoding` / `SET standard_conforming_strings` 两行曾硬编码在
// 三处（export.go 数据流、export.go 独立 .ddl、schema.go --full-schema 头），
// 极易改漏——v1.2.18 的"mysql 时区未钉住"就是漏项。
package app

import "pg2sql/internal/datatype"

// sqlSessionHeader：数据文件 / DDL 文件 / schema 对象文件统一使用的 SQL 会话头。
//
// 内容：
//   - client_encoding = 'UTF8'：工具输出一律 UTF-8
//   - standard_conforming_strings = on：QuoteSQLLiteral 按"反斜杠即普通字符"生成
//     字面量（不双写 \），该语义依赖此设置；目标端若为 off，含 \ 的文本回灌后会被
//     当转义序列改写（v1.0.62 P3）
//   - SET TIME ZONE（**仅金仓 mysql 兼容模式**）：v1.2.18 修复（P2）。
//     mysql 模式的 timestamptz 与分区边界字面量输出本地时间、不带时区后缀
//     （v1.2.16 D7，为与 mysql 服务端 SELECT 文本一致）。若目标会话时区不同，
//     这些字面量按目标时区解释 → 整体平移（实测 +08 → UTC 平移 8 小时），
//     INSERT 不报错；分区边界字面量落错则分区归属错误，同样不报错。
//
// 为什么只钉 mysql 模式：PG 模式与 oracle 模式的 timestamptz 输出带偏移或按
// UTC 语义渲染，钉时区反而会改变现有正确行为（v1.2.16 的
// TestV1216TimestamptzPgFormatUnchanged 反向断言锁死了"不得泄漏到 PG 模式"）。
//
// 为什么 timestamp（非 tz）不受影响：它存的就是本地时间、渲染也是本地时间，
// 与会话时区无关。所以这条 SET 只解决 timestamptz / 分区边界。
func sqlSessionHeader() string {
	h := "SET client_encoding = 'UTF8';\nSET standard_conforming_strings = on;\n"
	if datatype.IsKbMysqlMode() {
		h += "SET TIME ZONE " + datatype.MySQLLocalTZSQLValue() + ";\n"
	}
	return h
}

// importScriptTZExport：shell 导入脚本里钉住会话时区的 `export PGTZ=...` 行（含注释），
// 非 mysql 模式返回空串。
//
// v1.2.18 补漏（P2）：`--data --format csv/txt --import-script` 生成的是 shell 脚本，
// `\copy` 在客户端执行，**没有 SQL 会话头可以挂 SET TIME ZONE**。而 csv/txt 数据文件
// 按设计也不带会话头。此时 mysql 模式导出的 timestamptz 字面量（本地时间、不带后缀）
// 会被目标端的默认时区解释 —— +08 → UTC 即整体平移 8 小时，`\copy` 不报错。
//
// 唯一可用的载体是 libpq 环境变量 PGTZ（psql/ksql 均读它，\copy 随之生效）。
// 注意 PGTZ 的取值形态与 `SET TIME ZONE` 不同：固定偏移要写 `+08:00`，
// 不能写 `INTERVAL '+08:00' HOUR TO MINUTE`。
//
// 为什么只钉 mysql 模式：与 sqlSessionHeader 完全同源，见上方函数注释。
func importScriptTZExport() string {
	if !datatype.IsKbMysqlMode() {
		return ""
	}
	tz := datatype.MySQLLocalTZEnvValue()
	if tz == "" {
		// 防御分支：MySQLLocalTZEnvValue 在 mysql 模式下恒非空（未知时区也回退
		// +08:00，与字面量渲染 / SET TIME ZONE 同源，见该函数注释）。保留空串
		// 检查只为"将来出现真正拿不到值的新形态"兜底。
		return ""
	}
	return "# 钉住会话时区：mysql 模式的 timestamptz 输出为本地时间且不带偏移，\n" +
		"# 目标库时区不同会导致 \\copy 静默平移（+08 → UTC 即偏 8 小时）。\n" +
		"# 环境变量可覆盖；源库为固定偏移时此处写 +HH:MM 形式。\n" +
		"export PGTZ=${PGTZ:-" + tz + "}\n"
}
