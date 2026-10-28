package catalog

// v1.0.58 回归测试（P1）：库编码探测必须按金仓/PG 分流取编号表。
// 金仓 GBK 库（pg_database.encoding=35）此前被上游表映射为 sjis，
// 导致导出转义为 E'\x…' hex 文本（数据篡改）。
// 这里用合成的 global/1262 堆文件离线驱动 DetectDatabaseEncoding，
// 不依赖真实实例：同一份堆字节，仅 SYS_VERSION/PG_VERSION 标记不同，
// 金仓路径应得 gbk，PG 路径保持上游语义 sjis。

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

const t58ItemIDNormal = 1 // pg 页内 ItemId 标志位：正常元组

func t58Put16(b []byte, off int, v int) {
	b[off] = byte(v)
	b[off+1] = byte(v >> 8)
}

// t58DBTuple：pg_database 一行的堆元组（oid int4 / datname name64 / datdba int4 / encoding int4）
func t58DBTuple(oid uint32, name string, enc int32) []byte {
	data := make([]byte, 76)
	binary.LittleEndian.PutUint32(data[0:], oid)
	copy(data[4:68], name) // name 类型定长 64 字节，NUL 填充
	binary.LittleEndian.PutUint32(data[68:], 10 /* datdba */)
	binary.LittleEndian.PutUint32(data[72:], uint32(enc))

	tup := make([]byte, 24+len(data))
	binary.LittleEndian.PutUint16(tup[18:], 4) // t_infomask2：natts=4
	tup[22] = 24                               // t_hoff：无空值位图 (23+7)&^7 = 24
	copy(tup[24:], data)
	return tup
}

// t58Page：把元组按从页尾向前的顺序铺进一页（与真实堆布局一致）
func t58Page(tuples [][]byte) []byte {
	const ps = 8192
	page := make([]byte, ps)
	upper := ps
	for i, t := range tuples {
		off := upper - len(t)
		upper = off
		copy(page[off:], t)
		raw := uint32(t58ItemIDNormal<<15) | uint32(off&0x7FFF) | uint32(len(t)&0x7FFF)<<17
		binary.LittleEndian.PutUint32(page[24+4*i:], raw)
	}
	t58Put16(page, 12, 24+4*len(tuples)) // pd_lower
	t58Put16(page, 14, upper)            // pd_upper
	t58Put16(page, 16, ps)               // pd_special
	t58Put16(page, 18, 8196)             // pd_pagesize_version（v4 堆布局）
	return page
}

// t58Datadir：构造最小数据目录（版本标记 + global/1262 一页）
func t58Datadir(t *testing.T, kb bool, tuples [][]byte) string {
	t.Helper()
	dir := t.TempDir()
	mark := "PG_VERSION"
	if kb {
		mark = "SYS_VERSION"
	}
	if err := os.WriteFile(filepath.Join(dir, mark), []byte("12\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "global"), 0755); err != nil {
		t.Fatal(err)
	}
	var f []byte
	for _, pg := range tuples {
		f = append(f, pg...)
	}
	if err := os.WriteFile(filepath.Join(dir, "global", "1262"), f, 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// 金仓 GBK 库（enc=35）→ gbk；金仓 UTF8 库（enc=6）→ utf-8
func TestV1058KBDetectDatabaseEncoding(t *testing.T) {
	page := t58Page([][]byte{
		t58DBTuple(1, "template1", 6),
		t58DBTuple(14651, "test", 35), // 金仓 GBK
	})
	dir := t58Datadir(t, true, [][]byte{page})
	if got := DetectDatabaseEncoding(dir, 14651); got != "gbk" {
		t.Fatalf("金仓 enc=35 探测结果 = %q，期望 gbk（此前误判 sjis → 导出 E'\\x…' 数据篡改）", got)
	}
	if got := DetectDatabaseEncoding(dir, 1); got != "utf-8" {
		t.Fatalf("金仓 enc=6 探测结果 = %q，期望 utf-8", got)
	}
}

// 金仓 GB18030（enc=39，与上游编号巧合一致）→ gb18030
func TestV1058KBDetectGB18030(t *testing.T) {
	page := t58Page([][]byte{t58DBTuple(14651, "g30", 39)})
	dir := t58Datadir(t, true, [][]byte{page})
	if got := DetectDatabaseEncoding(dir, 14651); got != "gb18030" {
		t.Fatalf("金仓 enc=39 探测结果 = %q，期望 gb18030", got)
	}
}

// 对照组：PG 数据目录同字节 → 上游语义（35=sjis，钉住防止双向漂移）
func TestV1058PGUpstreamSemantics(t *testing.T) {
	page := t58Page([][]byte{t58DBTuple(5, "regress", 35)})
	dir := t58Datadir(t, false, [][]byte{page})
	if got := DetectDatabaseEncoding(dir, 5); got != "sjis" {
		t.Fatalf("PG enc=35 探测结果 = %q，期望 sjis（上游仅客户端语义，PG 库实际不会出现）", got)
	}
}
