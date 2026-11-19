package engine

import (
	"os"
	"path/filepath"
	"testing"
)

// v1.2.23：恰好 1GB 整边界段验证。
// 场景：主段恰好写满 1GB（131072 页 @8K，RELSEG_SIZE 整边界）后续段 .1 才会
// 出现；此时主段页数 = relSegPagesOf(8192)，Base 推进与 segPos 兜底必须一致。
// 用 Truncate 造稀疏文件，不实际占用 1GB 磁盘。
func TestV1223SegBoundaryExact1GB(t *testing.T) {
	dir := t.TempDir()
	ps := 8192
	segPages := relSegPagesOf(ps) // 131072
	if segPages != 131072 {
		t.Fatalf("relSegPagesOf(8192)=%d，期望 131072", segPages)
	}
	oneGB := int64(ps) * int64(segPages)
	if oneGB != 1<<30 {
		t.Fatalf("1GB=%d，期望 %d", oneGB, int64(1)<<30)
	}

	mkSparse := func(path string, size int64) {
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(size); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	mkPages := func(path string, pages int) {
		if err := os.WriteFile(path, make([]byte, int64(ps)*int64(pages)), 0644); err != nil {
			t.Fatal(err)
		}
	}

	seg0 := filepath.Join(dir, "20001")   // 恰好 1GB
	seg1 := filepath.Join(dir, "20001.1") // 3 页
	segZ := filepath.Join(dir, "20001z")  // 0 字节（独立文件，不参与链）
	seg2 := filepath.Join(dir, "20001.2") // 5 页
	mkSparse(seg0, oneGB)
	mkPages(seg1, 3)
	if err := os.WriteFile(segZ, nil, 0644); err != nil {
		t.Fatal(err)
	}
	mkPages(seg2, 5)

	// segPos 兜底：第 131072 页（全局）应落 .1 段第 0 页
	si, pi := segPos(segPages, ps)
	if si != 1 || pi != 0 {
		t.Errorf("segPos(%d)=%d,%d，期望 1,0", segPages, si, pi)
	}

	// 整边界主段 + .1：Base 推进 = 131072
	st := buildSegPageTableFromPaths(nil, []string{seg0, seg1}, ps)
	if st.Total != segPages+3 {
		t.Errorf("Total=%d，期望 %d", st.Total, segPages+3)
	}
	if len(st.Base) != 2 || st.Base[0] != 0 || st.Base[1] != segPages {
		t.Errorf("Base=%v，期望 [0 %d]", st.Base, segPages)
	}
	if si, pi := st.Locate(segPages); si != 1 || pi != 0 {
		t.Errorf("Locate(%d)=%d,%d，期望 1,0", segPages, si, pi)
	}
	if si, pi := st.Locate(segPages + 2); si != 1 || pi != 2 {
		t.Errorf("Locate(%d)=%d,%d，期望 1,2", segPages+2, si, pi)
	}
	if si, pi := st.Locate(segPages - 1); si != 0 || pi != segPages-1 {
		t.Errorf("Locate(%d)=%d,%d，期望 0,%d", segPages-1, si, pi, segPages-1)
	}

	// 整边界主段 + 零页 .1 + .2：零页段占位不截断，.2 的 Base 仍 = 131072
	// v1.2.28（审计 #12）：Paths/Base 与原始路径列表 1:1 对应（零页段占位），
	// Locate 下标即 segPaths 下标——第 131072 页必须落 .2（下标 2）而非压缩表
	// 语义里的 1（调用方按 1 索引原始 segPaths 会打开 .1，读失败静默丢页）
	st2 := buildSegPageTableFromPaths(nil, []string{seg0, segZ, seg2}, ps)
	if st2.Total != segPages+5 {
		t.Errorf("含零页段 Total=%d，期望 %d", st2.Total, segPages+5)
	}
	if len(st2.Base) != 3 || st2.Base[1] != segPages || st2.Base[2] != segPages {
		t.Errorf("含零页段 Base 期望 [0 %d %d]（零页段占位），实际 %v", segPages, segPages, st2.Base)
	}
	if si, pi := st2.Locate(segPages); si != 2 || pi != 0 {
		t.Errorf("含零页段 Locate(%d)=%d,%d，期望 2,0", segPages, si, pi)
	}

	// 整边界主段独立存在（无续段）：Total = 131072
	st3 := buildSegPageTableFromPaths(nil, []string{seg0}, ps)
	if st3.Total != segPages {
		t.Errorf("仅主段 Total=%d，期望 %d", st3.Total, segPages)
	}
}
