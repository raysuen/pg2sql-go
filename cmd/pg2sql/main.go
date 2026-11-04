// pg2sql 命令入口。
//
// 这里只做一件事：把进程参数交给 internal/app，并用其返回值作为退出码。
// 参数解析在 internal/cli，业务编排在 internal/app，底层解析在各 internal/* 包。
package main

import (
	"os"
	// 内嵌 IANA 时区库（v1.1.1/B17）：金仓 mysql/ora 模式 timestamp 解码需要历史偏移
	// （1901 年前 LMT、1986-1991 中国夏令时等），信创离线环境常缺 /usr/share/zoneinfo，
	// 内嵌后单文件二进制自带 tzdata（约 +450KB），保证 --tz Asia/Shanghai / conf 推导可用。
	_ "time/tzdata"

	"pg2sql/internal/app"
)

func main() {
	os.Exit(app.Run(os.Args[1:]))
}
