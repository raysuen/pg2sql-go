// pg2sql CLOG（事务状态日志）离线读取：v1.2.0 新增。
//
// 背景：堆元组的 t_infomask 里有 HEAP_XMIN_COMMITTED / HEAP_XMIN_INVALID 这类
// 「hint bit」，但 hint bit 只是缓存、不是权威状态。PG 只在需要时才回填
// （lazy），崩溃恢复中断的事务、物理拷贝/裸设备导出的文件、被第三方工具复制
// 过的堆文件都可能存在「hint 从未被写过」的元组。此时仅凭 hint 判定会把回滚
// 残留的行当成活行导出（实测可多导 50/150 = 33%）。
//
// 权威来源是 CLOG：<datadir>/pg_xact/XXXX（金仓为 <datadir>/sys_xact/XXXX），
// 每个事务占 2 bit，4 个事务/pg 1 字节，段文件 262144 字节（= 2^18，容纳 2^20
// 个事务）。状态：0=IN_PROGRESS 1=COMMITTED 2=ABORTED 3=SUB_COMMITTED。
//
// 重要限制（务必在文档中保留）：CLOG 是延迟落盘的。事务 commit/abort 之后状态
// 可能仍停留在共享缓冲区，磁盘上的 pg_xact 还读不到。因此本功能：
//   - 只作为 --clog 显式开启的复核手段，不改变默认行为；
//   - 读到 IN_PROGRESS 时一律按「活」处理并计数告警，绝不据此判死（宁可多导
//     不可丢行）；
//   - 段文件缺失（xid 太老已被 truncate，或太新）同样按「不可判」处理。
//
// Author: raysuen
package engine

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// ---------- CLOG 状态码 ----------
const (
	ClogInProgress   = 0
	ClogCommitted    = 1
	ClogAborted      = 2
	ClogSubCommitted = 3
)

const (
	clogSegBytes  = 262144           // 段文件大小 2^18
	clogXactsPerS = clogSegBytes * 4 // 每段容纳的事务数 2^20
	clogCtlOffset = 64               // pg_control 中 nextXid 的偏移（实测 PG12~18 / 金仓 V8R6 / V9 一致）
)

// ClogReader：CLOG 段文件读取器（并发安全）。
//
// 段文件按需 mmap 级别的整段读入（262144B，体积很小），用 map 缓存；
// xid → 状态的结果也做一层缓存——同一张表里同一事务往往覆盖成千上万个元组，
// 缓存命中率通常 >99%。
type ClogReader struct {
	dir  string // <datadir>/pg_xact 或 <datadir>/sys_xact
	name string // "pg_xact" / "sys_xact"（错误信息用）

	// nextXid：实例的分配水位（pg_control offset 64）。0 = 未知，不做水位校验。
	//
	// v1.2.2 修复（BUG-3）：旧实现从不校验 xid 与 nextXid 的关系。当元组头里的
	// xmin/xmax 是垃圾值（页损坏、非本表数据混入）或指向「本实例尚未分配」的
	// 未来 xid 时，代码照样按 segno/offset 去 CLOG 里取 2 bit——而那片区域要么
	// 不存在（返回 -1 尚可），要么是 [[已分配但未被该 xid 使用]] 的邻接字节，
	// 读出来的 1/2 会被当成 COMMITTED/ABORTED 这种**强结论**，直接把行判成
	// 活行或死行。xid >= nextXid 在物理上不可能是已结束事务，正确做法是
	// 一律报「不可判」（-1），让上层按保守策略处理。
	NextXid uint32

	segMu    sync.Mutex
	segCache map[uint32][]byte // segno → 段内容（nil 表示读取失败）

	resMu    sync.Mutex
	resCache map[uint32]uint8 // xid → 状态（0..3），缺失不缓存

	// 统计（atomic，并行 worker 安全）
	Queries   int64 // 查 CLOG 次数
	Committed int64 // 判为已提交
	Aborted   int64 // 判为已回滚
	Unknown   int64 // 段缺失/不可判
	NoXactDir int64 // xact 目录整体不可用（首次告警用）
}

// NewClogReader 依据 datadir 自动定位 CLOG 目录。
// PG：<datadir>/pg_xact；金仓：<datadir>/sys_xact。两者都试，先命中者用。
func NewClogReader(datadir string) *ClogReader {
	c := &ClogReader{segCache: map[uint32][]byte{}, resCache: map[uint32]uint8{}}
	if datadir == "" {
		return c
	}
	// v1.2.2 修复（BUG-3）：顺带取 nextXid 作为水位。读不到就留 0（不校验），
	// 不影响原有判定能力——宁可不校验，也不能因为控制文件缺失而整体降级。
	if nx, err := ReadNextXid(datadir); err == nil {
		c.NextXid = nx
	}
	for _, n := range []string{"pg_xact", "sys_xact"} {
		p := filepath.Join(datadir, n)
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			c.dir = p
			c.name = n
			return c
		}
	}
	return c
}

// Available：CLOG 目录是否可用（不可用时所有查询返回「不可判」）
func (c *ClogReader) Available() bool { return c != nil && c.dir != "" }

// Dir 返回实际使用的 CLOG 目录（空=不可用）
func (c *ClogReader) Dir() string {
	if c == nil {
		return ""
	}
	return c.dir
}

// loadSeg：读取段文件（带缓存）。返回 nil 表示该段不存在/读取失败。
//
// v1.2.2 修复（BUG-2）：旧实现只调用一次 f.Read(buf)，Go 的 Read 不保证读满，
// 短读（n < clogSegBytes 且 err==nil）后 buf 被截成 buf[:n] 存入缓存；
// 之后同段的其他 xid 若落在 n 之后，会因 int(byteNo) >= len(buf) 被误判为
// 「段缺失」，把本可判定的元组归入 Unknown（不可判）。现改为循环读满。
func (c *ClogReader) loadSeg(segno uint32) []byte {
	c.segMu.Lock()
	defer c.segMu.Unlock()
	if c.segCache == nil {
		c.segCache = map[uint32][]byte{}
	}
	if v, ok := c.segCache[segno]; ok {
		return v
	}
	buf := make([]byte, clogSegBytes)
	p := filepath.Join(c.dir, fmt.Sprintf("%04X", segno))
	f, err := os.Open(p)
	if err != nil {
		c.segCache[segno] = nil
		return nil
	}
	defer f.Close()
	n, err := io.ReadFull(f, buf)
	if n == 0 {
		c.segCache[segno] = nil
		return nil
	}
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		c.segCache[segno] = nil
		return nil
	}
	if n < clogSegBytes {
		// 段文件通常固定 262144B；尾部短读表示该段被截断（正常情况不会发生），
		// 保留可用前缀，越界访问仍按「不可判」处理（Status 里有 len 检查）。
		buf = buf[:n]
	}
	c.segCache[segno] = buf
	return buf
}

// Status 返回 xid 的 CLOG 状态；返回 -1 表示不可判（段缺失/xid 越界）。
//
// v1.2.2 修复（BUG-1）：命中 resCache 时直接返回、不累加统计，导致自检文案
// 「CLOG 复核：查询 N 次」在缓存命中（同事务覆盖大量元组的常见情形）时严重
// 低估，用户会误以为 CLOG 没生效。现改为命中缓存同样计入 Queries/分类计数。
func (c *ClogReader) Status(xid uint32) int {
	if c == nil || c.dir == "" {
		return -1
	}
	// 特殊 xid：0=InvalidTransactionId，1=BootstrapTransactionId，
	// 2=FrozenTransactionId —— 按 PG 语义都视为已提交。
	if xid < 3 {
		return ClogCommitted
	}
	// v1.2.2 修复（BUG-3）：xid >= nextXid 表示该事务「尚未开始」，物理上不可能
	// 有 CLOG 状态。此时去读对应 bit 得到的是邻接事务的残留值，会被误当成
	// COMMITTED/ABORTED 的强结论。一律返回 -1（不可判），让上层走保守路径。
	// NextXid==0 表示控制文件读不到，退回旧的「不校验」行为。
	if c.NextXid != 0 && xid >= c.NextXid {
		atomic.AddInt64(&c.Unknown, 1)
		atomic.AddInt64(&c.Queries, 1)
		return -1
	}
	c.resMu.Lock()
	if v, ok := c.resCache[xid]; ok {
		c.resMu.Unlock()
		// 缓存命中：状态已知，按同样口径计入统计（BUG-1 修复）
		atomic.AddInt64(&c.Queries, 1)
		switch int(v) {
		case ClogCommitted, ClogSubCommitted:
			atomic.AddInt64(&c.Committed, 1)
		case ClogAborted:
			atomic.AddInt64(&c.Aborted, 1)
		default:
			atomic.AddInt64(&c.Unknown, 1)
		}
		return int(v)
	}
	c.resMu.Unlock()

	segno := xid / clogXactsPerS
	off := xid % clogXactsPerS
	byteNo := off / 4
	shift := (off % 4) * 2

	buf := c.loadSeg(segno)
	if buf == nil || int(byteNo) >= len(buf) {
		atomic.AddInt64(&c.Unknown, 1)
		return -1
	}
	st := int((buf[byteNo] >> shift) & 0x03)

	// SUB_COMMITTED 只出现在子事务自身，用作可见性判断时等同 COMMITTED
	atomic.AddInt64(&c.Queries, 1)
	switch st {
	case ClogCommitted, ClogSubCommitted:
		atomic.AddInt64(&c.Committed, 1)
	case ClogAborted:
		atomic.AddInt64(&c.Aborted, 1)
	default:
		atomic.AddInt64(&c.Unknown, 1)
	}

	c.resMu.Lock()
	if c.resCache == nil {
		c.resCache = map[uint32]uint8{}
	}
	c.resCache[xid] = uint8(st)
	c.resMu.Unlock()
	return st
}

// Stats 返回统计快照
func (c *ClogReader) Stats() (queries, committed, aborted, unknown int64) {
	if c == nil {
		return
	}
	return atomic.LoadInt64(&c.Queries), atomic.LoadInt64(&c.Committed),
		atomic.LoadInt64(&c.Aborted), atomic.LoadInt64(&c.Unknown)
}

// ---------- pg_control 解析 ----------

// ReadNextXid 从 pg_control（金仓 sys_control）读取 nextXid。
//
// 布局（实测，PG 12~18 与金仓 V8R6C9B14 / V9R1C2B14 一致）：
//
//	offset  0  pg_control_version  uint32
//	...
//	offset 24  checkPoint LSN...
//	offset 64  nextXid             uint32  ← 本函数目标
//
// 金仓把文件名改成了 global/sys_control，内容布局未变。
func ReadNextXid(datadir string) (uint32, error) {
	if datadir == "" {
		return 0, fmt.Errorf("未指定数据目录")
	}
	var lastErr error
	for _, n := range []string{"pg_control", "sys_control"} {
		p := filepath.Join(datadir, "global", n)
		b, err := os.ReadFile(p)
		if err != nil {
			lastErr = err
			continue
		}
		if len(b) < clogCtlOffset+4 {
			lastErr = fmt.Errorf("%s 长度不足（%d 字节）", p, len(b))
			continue
		}
		return binary.LittleEndian.Uint32(b[clogCtlOffset : clogCtlOffset+4]), nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("未找到 pg_control / sys_control")
	}
	return 0, lastErr
}
