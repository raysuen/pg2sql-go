// Package logx 提供 pg2sql 全局日志与错误退出（并发安全）。
// Author: raysuen
package logx

import (
	"fmt"
	"os"
	"sync"
)

// mu 并发解析时保护日志输出不交错
var mu sync.Mutex

// Logf 输出带 [pg2sql] 前缀的日志到 stderr（并发安全）。
func Logf(format string, a ...interface{}) {
	mu.Lock()
	defer mu.Unlock()
	fmt.Fprintf(os.Stderr, "[pg2sql] "+format+"\n", a...)
}

// ErrorExit 输出错误并以退出码 1 结束进程。
func ErrorExit(msg string) {
	fmt.Fprintln(os.Stderr, "[pg2sql] 错误:", msg)
	os.Exit(1)
}
