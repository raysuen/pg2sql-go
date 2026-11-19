package engine

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeRec：segErrRecorder 测试桩。
type fakeRec struct{ errs map[string]string }

func (f *fakeRec) RecordSegError(path, msg string) {
	if f.errs == nil {
		f.errs = map[string]string{}
	}
	f.errs[path] = msg
}

// v1.2.20（T-2）：buildSegPageTableFromPaths 的 Stat 失败/零页段语义。
func TestV1220PageTableStatFailAndZeroSeg(t *testing.T) {
	dir := t.TempDir()
	ps := 8192
	seg0 := filepath.Join(dir, "16391")     // 10 页
	seg2 := filepath.Join(dir, "16391.2")   // 5 页（.1 缺失，模拟 Stat 失败场景用不存在路径）
	segZero := filepath.Join(dir, "16391z") // 0 字节
	seg3 := filepath.Join(dir, "16391.3")   // 3 页
	for _, tc := range []struct {
		path  string
		pages int
	}{{seg0, 10}, {seg2, 5}, {segZero, 0}, {seg3, 3}} {
		if tc.pages == 0 {
			if err := os.WriteFile(tc.path, nil, 0644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(tc.path, make([]byte, ps*tc.pages), 0644); err != nil {
			t.Fatal(err)
		}
	}
	rec := &fakeRec{}
	// Stat 失败（不存在的路径）：记账 + 截断（宁少读不错读），后续段不进表
	st := buildSegPageTableFromPaths(rec, []string{seg0, filepath.Join(dir, "no_such_seg"), seg2}, ps)
	if st.Total != 10 {
		t.Errorf("Stat 失败应截断链：Total=%d，期望 10（仅 seg0）", st.Total)
	}
	if len(rec.errs) != 1 {
		t.Errorf("Stat 失败应记账 1 条，实际 %d", len(rec.errs))
	}
	// 零页段：跳过不截断，后续段正常进表、Base 正确
	rec2 := &fakeRec{}
	st2 := buildSegPageTableFromPaths(rec2, []string{seg0, segZero, seg3}, ps)
	if st2.Total != 13 {
		t.Errorf("零页段应跳过不截断：Total=%d，期望 13", st2.Total)
	}
	// v1.2.28（审计 #12）：零页段占位，Paths/Base 与调用方手里的原始路径列表
	// 1:1 对应（旧实现压缩 Paths，Locate 下标与 segPaths 错位 → 读错段静默丢页）
	if len(st2.Base) != 3 || st2.Base[1] != 10 || st2.Base[2] != 10 {
		t.Errorf("零页段应占位：Base 期望 [0 10 10]，实际 %v", st2.Base)
	}
	if len(st2.Paths) != 3 || st2.Paths[1] != segZero || st2.Paths[2] != seg3 {
		t.Errorf("Paths 应与输入路径 1:1 对应，实际 %v", st2.Paths)
	}
	// Locate 反向扫描天然跳过零页段：第 10 页应落 seg3（下标 2）
	if si, pi := st2.Locate(10); si != 2 || pi != 0 {
		t.Errorf("Locate(10)=%d,%d，期望 2,0", si, pi)
	}
	if len(rec2.errs) != 0 {
		t.Errorf("零页段不应记账，实际 %v", rec2.errs)
	}
	// nil rec（integrity.MainForkPages 路径）不 panic
	st3 := buildSegPageTableFromPaths(nil, []string{seg0}, ps)
	if st3.Total != 10 {
		t.Errorf("nil rec 路径 Total=%d，期望 10", st3.Total)
	}
}
