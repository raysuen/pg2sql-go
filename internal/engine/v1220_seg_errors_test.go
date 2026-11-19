package engine

import (
	"fmt"
	"testing"
)

// v1.2.20：段打开失败机读记账（segments_unreadable）。
// 验证三点：同路径去重、封顶 50 条后只置 truncated、RowIter/ToastFile 两套收集器一致。
func TestV1220SegErrorRecord(t *testing.T) {
	ri := &RowIter{}
	for i := 0; i < maxSegErrorsDetail+10; i++ {
		ri.RecordSegError(fmt.Sprintf("/data/base/5/47666.%d", i), "permission denied")
	}
	ri.RecordSegError("/data/base/5/47666.0", "dup") // 同路径不覆盖、不重复计
	m, trunc := ri.SegErrorsSnapshot()
	if len(m) != maxSegErrorsDetail {
		t.Errorf("明细条数 = %d, want %d", len(m), maxSegErrorsDetail)
	}
	if !trunc {
		t.Error("超封顶后 truncated 应为 true")
	}
	if m["/data/base/5/47666.0"] != "permission denied" {
		t.Errorf("同路径首条应保留，got %q", m["/data/base/5/47666.0"])
	}
	if _, ok := m[fmt.Sprintf("/data/base/5/47666.%d", maxSegErrorsDetail)]; ok {
		t.Error("第 51 条不应进明细")
	}

	// 空收集器：nil map + false，报告字段 omitempty 生效
	ri2 := &RowIter{}
	if m2, tr2 := ri2.SegErrorsSnapshot(); m2 != nil || tr2 {
		t.Errorf("空收集器应返回 nil,false，got %v,%v", m2, tr2)
	}

	// ToastFile 同款
	tf := &ToastFile{}
	for i := 0; i < maxSegErrorsDetail+2; i++ {
		tf.RecordSegError(fmt.Sprintf("/data/base/5/12345.%d", i), "stale file handle")
	}
	mt, trunct := tf.SegErrorsSnapshot()
	if len(mt) != maxSegErrorsDetail || !trunct {
		t.Errorf("ToastFile 明细 = %d, truncated = %v；want %d, true", len(mt), trunct, maxSegErrorsDetail)
	}

	// nil 接收者安全（warnSegOpenOnce 经接口调用，防御 typed-nil）
	var nilRI *RowIter
	nilRI.RecordSegError("/x", "y") // 不得 panic
	var nilTF *ToastFile
	nilTF.RecordSegError("/x", "y")
}
