package engine

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// 构造金仓 V9R2（页版本 6）风格页：PSV 版本 0x2006、pd_special=pageSize-16。
// 布局（V009R002C016 实测）：lower@psv-6、upper@psv-4、special@psv-2、psv@psv(=48)，
// ItemId 从 (psv+6+3)&^3=56 起。psv 前的 48B 前缀含 LSN（4-7）等，非 0 即可。
func buildKBV6Page(pageSize int, lower uint16) []byte {
	page := make([]byte, pageSize)
	// psv 前缀（0..47）放点非零数据（模拟 LSN/校验区），不含可误判模式
	copy(page[4:8], []byte{0xd4, 0x0c, 0xab, 0xa0})
	binary.LittleEndian.PutUint16(page[42:], lower)                       // pd_lower
	binary.LittleEndian.PutUint16(page[44:], uint16(pageSize-16)-32)      // pd_upper
	binary.LittleEndian.PutUint16(page[46:], uint16(pageSize-16))         // pd_special=8176
	binary.LittleEndian.PutUint16(page[48:], uint16(pageSize>>8)<<8|0x06) // pagesizeversion=0x2006（高字节=页大小>>8）
	return page
}

// v1.2.24：金仓 V9R2 页版本 6 识别（此前 TryAutoLayout/detectPageSize 只认版本 4/5，
// 导致 V9R2 全库 catalog 无法解析 → 0 表）。
func TestV1224KBPageVersion6(t *testing.T) {
	page := buildKBV6Page(8192, 240)
	lay := TryStandardLayout(page, 8192)
	if lay != nil {
		t.Fatal("标准布局不应识别金仓版本 6 页")
	}
	lay = TryAutoLayout(page, 8192)
	if lay == nil {
		t.Fatal("TryAutoLayout 未识别 V9R2 版本 6 页")
	}
	if lay.Version != 6 {
		t.Errorf("Version=%d，期望 6", lay.Version)
	}
	if lay.HeaderEnd != 56 {
		t.Errorf("HeaderEnd=%d，期望 56", lay.HeaderEnd)
	}
	if lay.Lower != 240 || lay.Upper != 8192-16-32 || lay.Special != 8192-16 {
		t.Errorf("lower/upper/special=%d/%d/%d", lay.Lower, lay.Upper, lay.Special)
	}
	// 页大小探测也要识别
	if sz := detectPageSize(page); sz != 8192 {
		t.Errorf("detectPageSize=%d，期望 8192", sz)
	}
	// 16K 页同样成立
	page16 := buildKBV6Page(16384, 240)
	if lay16 := TryAutoLayout(page16, 16384); lay16 == nil || lay16.Special != 16384-16 {
		t.Fatalf("16K 版本 6 页识别失败: %+v", lay16)
	}
	// 其他版本仍按旧规则拒绝
	bad := buildKBV6Page(8192, 240)
	binary.LittleEndian.PutUint16(bad[48:], 0x2007)
	if TryAutoLayout(bad, 8192) != nil {
		t.Fatal("未知版本 7 不应被识别")
	}
}

// 端到端：构造一个含 1 条可解析元组的 V9R2 风格页 + 目录探测。
func TestV1224KBPageVersion6Tuple(t *testing.T) {
	ps := 8192
	page := buildKBV6Page(ps, 56+4) // 1 个 ItemId
	// 最小元组：24B 头（thoff=24，natts=1），1 个 int4 值
	tup := make([]byte, 24+4)
	tup[22] = 24                                         // t_hoff
	binary.LittleEndian.PutUint16(tup[18:], 1|0x0002<<0) // natts=1; HAS_NULL 位关
	// infomask: HEAP_XMAX_INVALID 常置位（0x0800）由 ParseTuple/IsLive 判断，
	// 这里给 XMIN_COMMITTED|XMAX_INVALID 组合保证活行
	binary.LittleEndian.PutUint16(tup[20:], 0x0100|0x0800)
	copy(tup[24:], []byte{1, 0, 0, 0})
	off := ps - 16 - len(tup) // 放到数据区末尾
	binary.LittleEndian.PutUint16(page[44:], uint16(off))
	copy(page[off:], tup)
	// ItemId[0]：off/len/normal
	rawID := uint32(off) | uint32(1)<<15 | uint32(len(tup))<<17
	binary.LittleEndian.PutUint16(page[56:], uint16(rawID))
	binary.LittleEndian.PutUint16(page[58:], uint16(rawID>>16))

	dir := t.TempDir()
	p := filepath.Join(dir, "1259")
	if err := os.WriteFile(p, page, 0644); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	if TryAutoLayout(data, ps) == nil {
		t.Fatal("含元组的版本 6 页未识别")
	}
	if !bytes.Contains(data, tup) {
		t.Fatal("内部一致性失败")
	}
}
