// pg2sql 命令入口。
//
// 这里只做一件事：把进程参数交给 internal/app，并用其返回值作为退出码。
// 参数解析在 internal/cli，业务编排在 internal/app，底层解析在各 internal/* 包。
package main

import (
	"os"

	"pg2sql/internal/app"
)

func main() {
	os.Exit(app.Run(os.Args[1:]))
}
