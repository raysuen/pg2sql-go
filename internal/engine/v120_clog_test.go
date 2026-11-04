// v1.2.0 CLOG 读取与可见性复核的单测
// Author: raysuen
package engine

import (
	"os"
	"path/filepath"
	"testing"
)

// writeClogSeg：按 xid→状态 的映射造一个 pg_xact 段文件
func writeClogSeg(t *testing.T, dir string, segno uint32, states map[uint32]int) {
	t.Helper()
	buf := make([]byte, clogSegBytes)
	for xid, st := range states {
		off := xid % clogXactsPerS
		byteNo := off / 4
		shift := (off % 4) * 2
		buf[byteNo] |= byte(st) << shift
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, segName(segno))
	if err := os.WriteFile(p, buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

func segName(segno uint32) string {
	const hexdigits = "0123456789ABCDEF"
	return string([]byte{
		hexdigits[(segno>>12)&0xF], hexdigits[(segno>>8)&0xF],
		hexdigits[(segno>>4)&0xF], hexdigits[segno&0xF],
	})
}

// TestClogSlotDecoding：2-bit 槽位定位与状态码
func TestClogSlotDecoding(t *testing.T) {
	dd := t.TempDir()
	// 同一个字节内放 4 个事务，验证 byte/shift 计算
	writeClogSeg(t, filepath.Join(dd, "pg_xact"), 0, map[uint32]int{
		100: ClogCommitted,
		101: ClogAborted,
		102: ClogInProgress,
		103: ClogSubCommitted,
		0:   ClogCommitted, // 特例：xid 0 直接返回已提交
		1:   ClogCommitted,
		2:   ClogCommitted,
	})
	c := NewClogReader(dd)
	if !c.Available() {
		t.Fatalf("CLOG 应可用，dir=%q", c.Dir())
	}
	if c.Dir() != filepath.Join(dd, "pg_xact") {
		t.Fatalf("目录定位错误: %s", c.Dir())
	}
	cases := []struct {
		xid  uint32
		want int
	}{
		{100, ClogCommitted}, {101, ClogAborted}, {102, ClogInProgress},
		{103, ClogSubCommitted}, {0, ClogCommitted}, {1, ClogCommitted}, {2, ClogCommitted},
		{999, ClogInProgress}, // 段内该槽位未写过 → 0（IN_PROGRESS），不是 -1
	}
	for _, tc := range cases {
		got := c.Status(tc.xid)
		if got != tc.want {
			t.Errorf("xid=%d: got %d, want %d", tc.xid, got, tc.want)
		}
	}
	// 段文件缺失 → -1
	if got := c.Status(uint32(clogXactsPerS) + 5); got != -1 {
		t.Errorf("缺失段应返回 -1，实际 %d", got)
	}
}

// TestClogV9SysXact：金仓目录名 sys_xact 自动识别
func TestClogV9SysXact(t *testing.T) {
	dd := t.TempDir()
	writeClogSeg(t, filepath.Join(dd, "sys_xact"), 0, map[uint32]int{500: ClogAborted})
	c := NewClogReader(dd)
	if !c.Available() {
		t.Fatal("sys_xact 应被识别")
	}
	if got := c.Status(500); got != ClogAborted {
		t.Errorf("got %d, want %d", got, ClogAborted)
	}
}

// TestClogUnavailable：目录不存在时所有查询不可判
func TestClogUnavailable(t *testing.T) {
	c := NewClogReader(t.TempDir())
	if c.Available() {
		t.Fatal("空目录不应判定为可用")
	}
	if got := c.Status(12345); got != -1 {
		t.Errorf("不可用时应返回 -1，实际 %d", got)
	}
	// nil reader 也不能 panic
	var nilC *ClogReader
	if got := nilC.Status(1); got != -1 {
		t.Errorf("nil reader 应返回 -1，实际 %d", got)
	}
	if nilC.Available() {
		t.Error("nil reader 不应可用")
	}
}

// TestReadNextXid：pg_control / sys_control 偏移 64
func TestReadNextXid(t *testing.T) {
	for _, name := range []string{"pg_control", "sys_control"} {
		dd := t.TempDir()
		g := filepath.Join(dd, "global")
		if err := os.MkdirAll(g, 0o755); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 8192)
		// nextXid = 2039549 = 0x1F1EFD → LE: fd 1e 1f 00
		buf[64], buf[65], buf[66], buf[67] = 0xfd, 0x1e, 0x1f, 0x00
		if err := os.WriteFile(filepath.Join(g, name), buf, 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := ReadNextXid(dd)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != 2039549 {
			t.Errorf("%s: got %d, want 2039549", name, got)
		}
	}
	if _, err := ReadNextXid(t.TempDir()); err == nil {
		t.Error("无 control 文件时应报错")
	}
}

// mkTuple：构造一个最小堆元组（23B 头 + 可选数据）
func mkTuple(xmin, xmax uint32, infomask uint16) *HeapTuple {
	raw := make([]byte, 32)
	raw[0], raw[1], raw[2], raw[3] = byte(xmin), byte(xmin>>8), byte(xmin>>16), byte(xmin>>24)
	raw[4], raw[5], raw[6], raw[7] = byte(xmax), byte(xmax>>8), byte(xmax>>16), byte(xmax>>24)
	// t_infomask2 = natts 2
	raw[18], raw[19] = 0x02, 0x00
	raw[20], raw[21] = byte(infomask), byte(infomask>>8)
	raw[22] = 24
	return ParseTuple(raw, 17)
}

// TestClogIsLiveInsertAborted：hint 缺失时 CLOG 判定回滚的插入
func TestClogIsLiveInsertAborted(t *testing.T) {
	dd := t.TempDir()
	writeClogSeg(t, filepath.Join(dd, "pg_xact"), 0, map[uint32]int{
		100: ClogCommitted,
		200: ClogAborted,
	})
	c := NewClogReader(dd)

	// xmin=200 回滚，且没有 hint（infomask 干净）→ CLOG 应判不可见
	tu := mkTuple(200, 0, 0)
	if !tu.HintMissing() {
		t.Fatal("hint 应为缺失状态")
	}
	// 锁定缺陷：旧逻辑 infomask=0、xmax=0 → IsDeleted=false → IsLive=true，
	// 把回滚 INSERT 的元组当活行导出。
	if !tu.IsLive() {
		t.Error("旧逻辑应判为可见（这正是 v1.1.x 的多导缺陷）")
	}
	if tu.IsLiveCLog(c, false) {
		t.Error("CLOG 应判定 xmin=200（已回滚）的元组不可见")
	}

	// xmin=100 已提交 → 活
	tu2 := mkTuple(100, 0, 0)
	if !tu2.IsLiveCLog(c, false) {
		t.Error("CLOG 应判定 xmin=100（已提交）的元组可见")
	}
}

// TestClogIsLiveXmaxRolledBack：hint 缺失时 CLOG 判定删除被回滚 → 行仍活
func TestClogIsLiveXmaxRolledBack(t *testing.T) {
	dd := t.TempDir()
	writeClogSeg(t, filepath.Join(dd, "pg_xact"), 0, map[uint32]int{
		100: ClogCommitted,
		300: ClogAborted, // 删除事务回滚
		301: ClogCommitted,
	})
	c := NewClogReader(dd)

	// xmax=300 无 hint：旧逻辑按「已删除」，CLOG 应救回
	tu := mkTuple(100, 300, 0)
	if !tu.IsLive() {
		// 旧逻辑 IsDeleted 返回 TXmax!=0 → true → IsLive=false
	}
	if tu.IsLiveCLog(c, false) {
		t.Log("CLOG 正确：删除事务回滚，行仍活")
	} else {
		t.Error("CLOG 应判定 xmax=300（已回滚删除）的行仍可见")
	}
	if d, _ := tu.IsDeletedCLog(c); d {
		t.Error("删除事务回滚时 IsDeleted 应为 false")
	}

	// xmax=301 已提交删除 → 不可见
	tu2 := mkTuple(100, 301, 0)
	if tu2.IsLiveCLog(c, false) {
		t.Error("CLOG 应判定 xmax=301（已提交删除）的行不可见")
	}
	if d, _ := tu2.IsDeletedCLog(c); !d {
		t.Error("已提交删除应返回 IsDeleted=true")
	}
}

// TestClogHintCompleteUnchanged：hint 完整时 CLOG 不改变任何结论
func TestClogHintCompleteUnchanged(t *testing.T) {
	dd := t.TempDir()
	writeClogSeg(t, filepath.Join(dd, "pg_xact"), 0, map[uint32]int{
		100: ClogAborted, // 故意与 hint 冲突
	})
	c := NewClogReader(dd)

	// hint 明确写了 committed，即使 CLOG 说 aborted 也应以 hint 为准
	// （生产路径上不会出现这种矛盾；此测试锁定「hint 优先」的选择）
	tu := mkTuple(100, 0, HEAP_XMIN_COMMITTED)
	if !tu.IsLiveCLog(c, false) {
		t.Error("hint 完整（COMMITTED）时应判活，不受 CLOG 影响")
	}
	if tu.HintMissing() {
		t.Error("hint 完整时 HintMissing 应为 false")
	}

	// frozen 同理
	tu2 := mkTuple(100, 0, HEAP_XMIN_FROZEN)
	if !tu2.IsLiveCLog(c, false) {
		t.Error("FROZEN 元组应判活")
	}
	if tu2.HintMissing() {
		t.Error("FROZEN 时 HintMissing 应为 false")
	}

	// hint 写 INVALID → 不可见
	tu3 := mkTuple(100, 0, HEAP_XMIN_INVALID)
	if tu3.IsLiveCLog(c, false) {
		t.Error("HEAP_XMIN_INVALID 应判不可见")
	}
}

// TestClogInProgressConservative：IN_PROGRESS 一律按活（绝不能丢行）
func TestClogInProgressConservative(t *testing.T) {
	dd := t.TempDir()
	writeClogSeg(t, filepath.Join(dd, "pg_xact"), 0, map[uint32]int{})
	c := NewClogReader(dd)

	tu := mkTuple(500, 0, 0)
	if !tu.IsLiveCLog(c, false) {
		t.Error("CLOG 读到 IN_PROGRESS 时必须按活行处理，不能丢行")
	}
	if !tu.IsLive() {
		t.Error("旧逻辑同样应判活")
	}
}

// TestClogKilledCounts：开了 CLOG 后，被剔除的元组要被统计
func TestClogKilledCounts(t *testing.T) {
	dd := t.TempDir()
	writeClogSeg(t, filepath.Join(dd, "pg_xact"), 0, map[uint32]int{700: ClogAborted})
	c := NewClogReader(dd)

	ri := &RowIter{Clog: c, PageSize: 8192, PGVersion: 17, TM: mkTestMeta()}
	tu := mkTuple(700, 0, 0)
	if row := ri.buildRow(tu, [2]int{0, 1}); row != nil {
		t.Error("回滚插入的元组不应产出行")
	}
	if ri.ClogKilledTuples != 1 {
		t.Errorf("ClogKilledTuples 应为 1，实际 %d", ri.ClogKilledTuples)
	}
}

// ---------- v1.2.1（B19 修复）专项用例 ----------

// TestB19PlannerDefault：DELETE+ROLLBACK 的元组（infomask=0x0102，无任何 XMAX 位）
// 在主导出路径必须按活行处理 —— 旧逻辑会判死导致整表丢光。
func TestB19PlannerDefault(t *testing.T) {
	// 真实形态：xmin 已提交、xmax 有值（回滚的删除事务）、t_infomask=0x0102
	tu := mkTuple(100, 200, HEAP_XMIN_COMMITTED)
	if !tu.HintMissing() {
		t.Fatal("该形态的 xmax hint 应缺失")
	}

	// 1) 无 CLOG + 主导出路径 → 必须判活（B19 修复核心）
	if !tu.IsLiveCLog(nil, false) {
		t.Error("B19：hint 缺失时主导出路径必须按活行处理，否则整表丢光")
	}
	if !tu.IsLive() {
		t.Error("B19：IsLive() 无参版本同样应判活")
	}

	// 2) 无 CLOG + 审计模式 → 保留宽口径，算疑似死行
	if tu.IsLiveCLog(nil, true) {
		t.Error("审计模式应把不可判元组当疑似死行")
	}
	d, certain := tu.IsDeletedCLog(nil)
	if !d || certain {
		t.Errorf("审计语义应为 (疑似删除=true, 确定=false)，实际 (%v, %v)", d, certain)
	}

	// 3) 有 CLOG 且删除事务回滚 → 确认为活行，两种模式一致
	dd := t.TempDir()
	writeClogSeg(t, filepath.Join(dd, "pg_xact"), 0, map[uint32]int{200: ClogAborted})
	c := NewClogReader(dd)
	if !tu.IsLiveCLog(c, false) || !tu.IsLiveCLog(c, true) {
		t.Error("CLOG 确认删除已回滚，两种模式都应判活")
	}
	if d, certain := tu.IsDeletedCLog(c); d || !certain {
		t.Errorf("应为 (删除=false, 确定=true)，实际 (%v, %v)", d, certain)
	}

	// 4) 有 CLOG 且删除事务已提交 → 确认为死行
	dd2 := t.TempDir()
	writeClogSeg(t, filepath.Join(dd2, "pg_xact"), 0, map[uint32]int{200: ClogCommitted})
	c2 := NewClogReader(dd2)
	if tu.IsLiveCLog(c2, false) || tu.IsLiveCLog(c2, true) {
		t.Error("CLOG 确认删除已提交，两种模式都应判死")
	}
	if d, certain := tu.IsDeletedCLog(c2); !d || !certain {
		t.Errorf("应为 (删除=true, 确定=true)，实际 (%v, %v)", d, certain)
	}
}

// TestB19AuditModeKeepsWideNet：审计模式与主导出路径在同一元组上故意不一致
func TestB19AuditModeKeepsWideNet(t *testing.T) {
	tu := mkTuple(100, 200, HEAP_XMIN_COMMITTED)
	liveMain := tu.IsLiveCLog(nil, false)
	liveAudit := tu.IsLiveCLog(nil, true)
	if !liveMain {
		t.Error("主导出：应判活（防丢行）")
	}
	if liveAudit {
		t.Error("审计模式：应判疑似死行（宽口径）")
	}
}

// TestB19UncertainCount：不可判元组要计入 UncertainTuples
func TestB19UncertainCount(t *testing.T) {
	tu := mkTuple(100, 200, HEAP_XMIN_COMMITTED) // 无 CLOG 可判

	ri := &RowIter{PageSize: 8192, PGVersion: 17, TM: mkTestMeta()}
	row := ri.buildRow(tu, [2]int{0, 1})
	if row == nil {
		t.Fatal("主导出路径应产出该行（防丢行）")
	}
	if ri.UncertainTuples != 1 {
		t.Errorf("UncertainTuples 应为 1，实际 %d", ri.UncertainTuples)
	}
	if row.Deleted {
		t.Error("主导出路径不应把该行标记为已删除")
	}
}

// TestB19RealDeleteStillWorks：真删除（hint 完整）不受影响
func TestB19RealDeleteStillWorks(t *testing.T) {
	tu := mkTuple(100, 200, HEAP_XMIN_COMMITTED|HEAP_XMAX_COMMITTED)
	if tu.HintMissing() {
		t.Fatal("hint 完整时不应算缺失")
	}
	if tu.IsLiveCLog(nil, false) || tu.IsLiveCLog(nil, true) {
		t.Error("XMAX_COMMITTED 应判死，两种模式一致")
	}
	if d, certain := tu.IsDeletedCLog(nil); !d || !certain {
		t.Errorf("应为 (true, true)，实际 (%v, %v)", d, certain)
	}
}

// TestB19XmaxInvalidAlive：XMAX_INVALID（删除回滚后 hint 已回填）判活
func TestB19XmaxInvalidAlive(t *testing.T) {
	tu := mkTuple(100, 200, HEAP_XMIN_COMMITTED|HEAP_XMAX_INVALID)
	if !tu.IsLiveCLog(nil, false) || !tu.IsLiveCLog(nil, true) {
		t.Error("XMAX_INVALID 应判活，两种模式一致")
	}
	if tu.HintMissing() {
		t.Error("hint 完整时不应算缺失")
	}
}

// mkTestMeta：buildRow 需要的最小表元数据（2 列 int4 + text，与 mkTuple 的 natts=2 对应）
func mkTestMeta() *TableMeta {
	return &TableMeta{
		Schema:  "t",
		RelName: "t",
		Columns: []ColumnDef{
			{Name: "id", TypeOID: 23, AttLen: 4, AttNum: 1, AttAlign: "i", AttByVal: true},
			{Name: "v", TypeOID: 25, AttLen: -1, AttNum: 2, AttAlign: "i"},
		},
	}
}
