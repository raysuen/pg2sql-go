package nt

import (
	"testing"
	"time"
)

// 审计复现：列表分支遇未闭合 '}' 时 parseValue 无进展 → 死循环。
// 用 goroutine + 看门狗验证，避免挂死测试进程。
func TestAuditListBraceHang(t *testing.T) {
	// v1.0.59 已修复（P0-1）：parseValue 无进展即 p.err=true，回归验证不挂死且报错
	done := make(chan struct{})
	go func() {
		defer close(done)
		p := &Parser{Src: "{FUNCEXPR :args (}"}
		p.ParseNode()
	}()
	select {
	case <-done:
		// 修复后应立即返回
	case <-time.After(2 * time.Second):
		t.Fatal("回归：ParseNode 在列表分支遇 '}' 时 2 秒无进展（应为 p.err=true 立即返回）")
	}
}

// v1.0.59 回归：畸形列表输入应置 p.err，DeparseNodeTree 返回空串而非挂死
func TestAuditListBraceErr(t *testing.T) {
	p := &Parser{Src: "{FUNCEXPR :args (}"}
	p.ParseNode()
	if !p.err {
		t.Fatal("期望 p.err=true（列表分支遇 '}' 无进展），实际未置错")
	}
}
