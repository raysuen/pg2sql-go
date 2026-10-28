package engine

// v1.0.55 回归测试：B1（合法空页误判坏块）/ B3（--count --limit 语义）
//
// B1 的现场复现（141 实机）：2 万行表 DELETE 前半 + VACUUM(TRUNCATE OFF) 后
// 导出 10000 行完全正确，却被报「102 个损坏数据块」、verdict=fail——这 102 页
// 是 VACUUM 清空后的合法空页（lower=24/upper=8192、行指针为 0、页头自洽）。
// B2（静默空文件）的主修复在 singleFileArg/exportOneTable 的路径校验里，
// errorExit 会直接 os.Exit，无法在本文件内做进程内单测，由 141 实机回归覆盖。
// B3 复现：5000 行表 `--count --limit 10` 输出「总行数: 10」。

import (
	"os"
	"path/filepath"
	"testing"
)

// t55FixHeader：t54Page 只填 pd_lower/pd_upper，这里补齐 pd_special 与
// pd_pagesize_version（8192<<8|4），使页能通过 tryStandardLayout。
func t55FixHeader(page []byte) []byte {
	t54Put16(page, 16, 8192) // pd_special
	t54Put16(page, 18, 8196) // pd_pagesize_version
	return page
}

// t55EmptyPage：构造 VACUUM 后的合法空页——页头完全自洽、无行指针、无元组。
func t55EmptyPage() []byte {
	page := make([]byte, 8192)
	t54Put16(page, 12, 24)   // pd_lower：仅页头
	t54Put16(page, 14, 8192) // pd_upper：数据区顶到页尾
	t54Put16(page, 16, 8192) // pd_special
	t54Put16(page, 18, 8196) // pd_pagesize_version
	return page
}

// t55BadPage：构造真坏页——布局非法（lower > upper 违反堆页不变量）且非全零。
func t55BadPage() []byte {
	page := make([]byte, 8192)
	for i := 24; i < 8192; i += 64 {
		page[i] = 0xAB
	}
	t54Put16(page, 12, 5000) // pd_lower
	t54Put16(page, 14, 200)  // pd_upper < lower → 布局非法
	return page
}

// t55Tuple：构造 (int4, text) 两列的真实布局元组——int4 定长 4B，
// text 走 1 字节短 varlena（(总长<<1)|1），两列相对 t_hoff 均已 4 字节对齐。
func t55Tuple(id int, text []byte) []byte {
	body := make([]byte, 0, 5+len(text))
	body = append(body, byte(id), byte(id>>8), byte(id>>16), byte(id>>24))
	body = append(body, byte(((len(text)+1)<<1)|1))
	body = append(body, text...)
	return t54Tuple(2, body)
}

// t55DataPage：30 行/页的合法数据页（完整页头 + 可解码元组）。
func t55DataPage(text []byte) []byte {
	var tuples [][]byte
	for i := 0; i < 30; i++ {
		tuples = append(tuples, t55Tuple(i, text))
	}
	return t55FixHeader(t54Page(tuples, 300))
}

// t55BuildIter：给 RowIter 指定一个真实存在的数据文件（stream/Count 需要 path）。
func t55BuildIter(t *testing.T, pages [][]byte, limit int) *RowIter {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "17631")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, pg := range pages {
		if _, err := f.Write(pg); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	return &RowIter{
		TM: &TableMeta{Schema: "public", RelName: "t55", Columns: []ColumnDef{
			{Name: "id", AttNum: 1, TypeOID: 23, AttLen: 4, AttAlign: "i"},
			{Name: "v", AttNum: 2, TypeOID: 25, AttLen: -1, AttAlign: "i"},
		}},
		Path:      p,
		PageSize:  8192,
		PGVersion: 17,
		Limit:     limit,
	}
}

// ---- B1：IterTuples 三态拆分 ----

// 合法空页：布局合法（第二返回值 true），不计坏页（复现 v1.0.54 B1 的核心场景）
func TestV1055EmptyPageIsNotBadPage(t *testing.T) {
	ri := &RowIter{PageSize: 8192}
	items, layOK := ri.IterTuples(0, t55EmptyPage())
	if !layOK {
		t.Fatalf("合法空页被判为布局非法（将误报坏块，复现 B1）")
	}
	if len(items) != 0 {
		t.Fatalf("空页不应有元组")
	}
	// 调用点判据：!layOK && !ZeroPage → 空页不进坏页表
	if !layOK && !ZeroPage(t55EmptyPage()) {
		t.Fatalf("空页被判坏页")
	}
}

// 全零页：布局非法，但 ZeroPage 拦截 → 不计坏页
func TestV1055ZeroPageIsNotBadPage(t *testing.T) {
	ri := &RowIter{PageSize: 8192}
	zero := make([]byte, 8192)
	_, layOK := ri.IterTuples(0, zero)
	if layOK {
		t.Fatalf("全零页不应返回布局合法")
	}
	if !ZeroPage(zero) {
		t.Fatalf("ZeroPage 未识别全零页")
	}
}

// 真坏页：布局非法且非全零 → 必须计坏页（不能把坏页检测修没了）
func TestV1055GarbagePageIsBadPage(t *testing.T) {
	ri := &RowIter{PageSize: 8192}
	bad := t55BadPage()
	_, layOK := ri.IterTuples(0, bad)
	if layOK {
		t.Fatalf("非法布局页不应返回布局合法")
	}
	if ZeroPage(bad) {
		t.Fatalf("非全零坏页被 ZeroPage 误吞")
	}
	if !(!layOK && !ZeroPage(bad)) {
		t.Fatalf("坏页判据失效")
	}
}

// 端到端：混排【数据页+空页+数据页+全零页+坏页】的文件，坏块只计真坏页那 1 个
func TestV1055BadBlockCountOnlyRealBrokenPages(t *testing.T) {
	dataPage := t55DataPage([]byte("用户姓名测试数据"))
	ri := t55BuildIter(t, [][]byte{
		dataPage, t55EmptyPage(), dataPage, make([]byte, 8192), t55BadPage(), dataPage,
	}, 0)
	n := ri.CountRows()
	if n == 0 {
		t.Fatalf("数据页元组未被导出/计数")
	}
	if got := ri.BadCount; got != 1 {
		t.Fatalf("坏块计数 = %d，期望 1（空页/全零页不算坏块）——复现 v1.0.54 B1", got)
	}
}

// ---- B3：--count 忽略 --limit ----

func TestV1055CountIgnoresLimit(t *testing.T) {
	// 每页 30 行 × 5 页 = 150 行，limit=10
	var pages [][]byte
	for i := 0; i < 5; i++ {
		pages = append(pages, t55DataPage([]byte("用户姓名测试数据")))
	}
	ri := t55BuildIter(t, pages, 10)
	n := ri.CountRows()
	if n <= 10 {
		t.Fatalf("--count 受 --limit 影响：统计 %d 行（复现 v1.0.54 B3，期望全表计数）", n)
	}
	if ri.Limit != 10 {
		t.Fatalf("CountRows 结束后 limit 未恢复: %d", ri.Limit)
	}
	// 对照：数据导出路径 limit 依然生效（streamStdSerial 的 emit 返回 false 停止）
	ri2 := t55BuildIter(t, pages, 10)
	exported := 0
	ri2.streamStdSerial(nil, func(string) bool {
		exported++
		return exported < 10
	})
	if exported != 10 {
		t.Fatalf("导出路径 emit 停止语义变化: %d", exported)
	}
}
