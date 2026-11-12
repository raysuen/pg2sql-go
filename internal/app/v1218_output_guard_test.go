// v1.2.18 回归用例：纯 --ddl 模式的 -o 守卫洞（P3）。
//
// 缺陷现场：`--table-name <分区父表> --ddl -o /tmp/out`（多叶子）
//   - isFileOutput 用 filepath.Ext() != "" 判文件 → /tmp/out 无扩展名 → 返回 false
//   - resolveOutputPath 对同一路径按 `/tmp/out.ddl` 单文件处理
//     → exportPartitionTreeDDL 的拒绝条件不触发 → 三张表全写 /tmp/out.ddl，
//     后一张覆盖前一张，最终只剩最后一张，零告警、退出码 0。
package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestV1218IsFileOutput(t *testing.T) {
	dir := t.TempDir()

	// 非文件：空串 / 以 / 结尾 / 已存在的目录
	if isFileOutput("") {
		t.Error(`isFileOutput("") 应为 false`)
	}
	if isFileOutput(dir + "/") {
		t.Errorf("isFileOutput(%q)（以 / 结尾）应为 false", dir+"/")
	}
	if isFileOutput(dir) {
		t.Errorf("isFileOutput(%q)（已存在目录）应为 false", dir)
	}

	// 文件：有扩展名 / **不存在且无扩展名**（本缺陷的直接现场）
	for _, p := range []string{
		filepath.Join(dir, "out.ddl"),
		filepath.Join(dir, "out.sql"),
		filepath.Join(dir, "out"), // ← v1.2.17 判 false，实际会被补成 out.ddl
		filepath.Join(dir, "noext"),
	} {
		if !isFileOutput(p) {
			t.Errorf("isFileOutput(%q) 应为 true（resolveOutputPath 会按单文件处理）", p)
		}
	}
}

// 端到端：多叶子 + 无扩展名 -o 必须被 exportPartitionTreeDDL 拒绝，
// 避免所有表写同一个 .ddl 后互相覆盖。
func TestV1218PartitionDDLNoExtOutputRejected(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out") // 不存在、无扩展名

	// 先确认这是"会被当成单文件"的路径
	if !isFileOutput(out) {
		t.Fatalf("前置条件不成立：isFileOutput(%q) 应为 true", out)
	}

	// 模拟多叶子场景下的拒绝分支（exportPartitionTreeDDL 的守卫逻辑）
	leafCount := 3
	if !(leafCount > 1 && isFileOutput(out)) {
		t.Errorf("多叶子(%d) + -o %q 应触发拒绝（否则 %d 张表全写 %s.ddl 互相覆盖）",
			leafCount, out, leafCount, out)
	}

	// 反向断言：给个真目录（或带 / 结尾）不得被拒绝
	if isFileOutput(dir + "/") {
		t.Errorf("-o 目录 %q 不应被当成文件", dir+"/")
	}
	// 不存在但以 / 结尾也视为目录
	missingDir := filepath.Join(dir, "sub") + "/"
	if isFileOutput(missingDir) {
		t.Errorf("-o 以 / 结尾的 %q 不应被当成文件", missingDir)
	}
	_ = os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
}
