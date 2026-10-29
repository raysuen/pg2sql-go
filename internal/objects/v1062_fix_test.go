// Author: raysuen
package objects

import (
	"os"
	"strings"
	"testing"
)

// newTestSpill：构造一个共享溢写文件，返回 (spill, 追加函数)
func newTestSpill(t *testing.T) (*loSpill, func(string) loPageRef) {
	t.Helper()
	s, err := newLoSpill()
	if err != nil {
		t.Fatalf("创建溢写文件失败: %v", err)
	}
	return s, func(hex string) loPageRef {
		off, err := s.appendHex(hex)
		if err != nil {
			t.Fatalf("溢写失败: %v", err)
		}
		return loPageRef{off: off, hexLen: int32(len(hex))}
	}
}

// TestV1062LargeObjectSpill：P2-7 溢写路径回归（v1.0.63 改为页索引 + 共享文件）。
// 验证：分块 lo_put、offset 用数据字节、页索引可乱序（按 pageno 排序后读回）、
// 清理会删除临时文件。
func TestV1062LargeObjectSpill(t *testing.T) {
	spill, put := newTestSpill(t)

	// 2 个 chunk + 1 个小尾巴（hex，每字符半字节）
	chunkHex := strings.Repeat("ab", LoChunkBytes) // 16MB 数据 → 32MB hex
	tailHex := strings.Repeat("cd", 1024)          // 1KB 数据
	p0 := put(chunkHex)
	p0.pageno = 0
	p1 := put(tailHex)
	p1.pageno = 1

	e := &LargeObjectEntry{
		OID:     4242,
		ByteLen: int64((len(chunkHex) + len(tailHex)) / 2),
		spill:   spill,
		pages:   []loPageRef{p0, p1},
	}
	var sb strings.Builder
	n, err := WriteLargeObjectsSQL(&sb, []*LargeObjectEntry{e}, nil)
	if err != nil {
		t.Fatalf("写出大对象 SQL 失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("写出对象数 = %d, want 1", n)
	}
	out := sb.String()
	if !strings.Contains(out, "SELECT pg_catalog.lo_create(4242);") {
		t.Errorf("溢写对象应走 lo_create 路径:\n%.200s", out)
	}
	if got := strings.Count(out, "lo_put(4242,"); got != 2 {
		t.Errorf("lo_put 语句数 = %d, want 2（16MB 块 + 1KB 尾块）", got)
	}
	// 第二块偏移必须等于第一块的数据字节数（16MB），不能是 hex 长度
	if !strings.Contains(out, "lo_put(4242, 16777216, decode('") {
		t.Errorf("第二块 offset 应为 16777216（16MB 数据偏移，不是 hex 长度）")
	}
	// 溢写对象不回填 DataHex（否则内存优化失效）
	if e.DataHex != "" {
		t.Errorf("溢写对象不应回填 DataHex")
	}
	CleanupLargeObjectSpill([]*LargeObjectEntry{e})
	if _, err := os.Stat(spill.path); !os.IsNotExist(err) {
		t.Errorf("CleanupLargeObjectSpill 未删除临时文件")
	}
}

// TestV1062LargeObjectInline：小对象仍走单语句 lo_from_bytea（行为不回归）
func TestV1062LargeObjectInline(t *testing.T) {
	e := &LargeObjectEntry{OID: 7, DataHex: "deadbeef"}
	var sb strings.Builder
	if _, err := WriteLargeObjectsSQL(&sb, []*LargeObjectEntry{e}, nil); err != nil {
		t.Fatalf("写出失败: %v", err)
	}
	if !strings.Contains(sb.String(), `lo_from_bytea(7, '\xdeadbeef')`) {
		t.Errorf("小对象应走 lo_from_bytea 单语句: %s", sb.String())
	}
}

// TestV1063LargeObjectSmallFromSpill：小对象即使走溢写，也必须输出单语句
// lo_from_bytea，且正文按 pageno 顺序拼回（与内存路径等价）。
func TestV1063LargeObjectSmallFromSpill(t *testing.T) {
	spill, put := newTestSpill(t)
	// 故意乱序追加：先 pageno=1，再 pageno=0
	p1 := put("cd")
	p1.pageno = 1
	p0 := put("ab")
	p0.pageno = 0
	e := &LargeObjectEntry{OID: 9, ByteLen: 2, spill: spill, pages: []loPageRef{p1, p0}}
	// 模拟聚合期排序
	if e.pages[0].pageno > e.pages[1].pageno {
		e.pages[0], e.pages[1] = e.pages[1], e.pages[0]
	}
	var sb strings.Builder
	if _, err := WriteLargeObjectsSQL(&sb, []*LargeObjectEntry{e}, nil); err != nil {
		t.Fatalf("写出失败: %v", err)
	}
	if !strings.Contains(sb.String(), `lo_from_bytea(9, '\xabcd')`) {
		t.Errorf("小对象应按 pageno 顺序拼回单语句: %s", sb.String())
	}
	CleanupLargeObjectSpill([]*LargeObjectEntry{e})
}
