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

// errorExitHook 单测钩子：置位后 ErrorExit 转入钩子（默认 nil 走真实 os.Exit）。
// v1.2.5 引入，仅为参数解析互斥校验可测；生产路径行为不变。
var errorExitHook func(msg string)

// SetErrorExitHookForTest 供测试注入 ErrorExit 钩子（返回恢复函数）。
func SetErrorExitHookForTest(h func(msg string)) (restore func()) {
	old := errorExitHook
	errorExitHook = h
	return func() { errorExitHook = old }
}

// ErrorExit 输出错误并以退出码 1 结束进程。
func ErrorExit(msg string) {
	if errorExitHook != nil {
		errorExitHook(msg)
		return
	}
	fmt.Fprintln(os.Stderr, "[pg2sql] 错误:", msg)
	os.Exit(1)
}
