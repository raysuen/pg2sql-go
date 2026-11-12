// v1.2.18 回归用例：procNamespaces 必须在域 DEFAULT 反解之前注入（P3）。
//
// 缺陷现场：ScanDynamic 里 buildTypeDefs（第 2807 行）先跑，而
// nt.SetProcNamespaces 到第 2811 行才调用。域 DEFAULT 的节点树反解就发生在
// buildTypeDefs 内部（typeTailDefault → nt.DeparseNodeTree），那一刻映射仍是 nil：
//
//	isSystemProc(oid)     → nil 分支返回 true
//	procSchemaQualifier() → nil 分支返回 ""
//
// 于是 v1.2.18 新加的防护对域默认值完全不生效：域 DEFAULT 里调用
// myschema.numeric(x, 2) 照样被改写成 CAST(x AS numeric)，其他用户函数
// 也不会补 schema 限定。而这正是本次要防的场景之一。
//
// 断言方式：ScanDynamic 走完整路径后，检查 buildTypeDefs 执行时刻已记录
// "注入已完成"——顺序事实由 catalog 内部埋点 typeDefsProcNsInjected 记录。
package catalog

import (
	"os"
	"path/filepath"
	"testing"

	"pg2sql/internal/nt"
)

func TestV1218ProcNsInjectedBeforeTypeDefs(t *testing.T) {
	nt.ResetProcNamespaces()
	t.Cleanup(nt.ResetProcNamespaces)

	dir := t.TempDir()
	// 空 pg_type（1247）：让 buildTypeDefs 能进入函数体并记录顺序标记。
	// 元组读不到不影响断言——我们只关心"进入那一刻注入有没有发生"。
	if err := os.WriteFile(filepath.Join(dir, "1247"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	typeDefsProcNsInjected = false
	_ = ScanDynamic(dir, 17, false)

	if !typeDefsProcNsInjected {
		t.Fatalf("buildTypeDefs 执行时 procNamespaces 尚未注入：域 DEFAULT 里的用户函数" +
			"会被静默改写成 CAST（isSystemProc 恒 true、procSchemaQualifier 恒空）")
	}
}

// 反向断言：ScanDynamic 结束时注入状态必须是"已注入"（非 nil）。
// nil 与"空 map"语义不同：nil 表示"完全不知道"，会让 isSystemProc 无条件返回 true。
func TestV1218ScanDynamicInjectsProcNs(t *testing.T) {
	nt.ResetProcNamespaces()
	t.Cleanup(nt.ResetProcNamespaces)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "1247"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	_ = ScanDynamic(dir, 17, false)
	if !nt.ProcNamespacesInjected() {
		t.Errorf("ScanDynamic 结束后 procNamespaces 仍为 nil（未注入）")
	}
}
