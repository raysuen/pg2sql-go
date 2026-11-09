// v1.2.7 测试（F2-1）：加密表识别（供编码精化跳过）。
package engine

import (
	"encoding/binary"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// 随机字节页（模拟透明加密后的页内容）应被判为密文
func TestV127SuspectCipherRandomPages(t *testing.T) {
	const ps = 8192
	buf := make([]byte, ps*4)
	r := rand.New(rand.NewSource(127))
	r.Read(buf)
	// 页头 pd_lower/pd_upper 置 0（金仓 TDE 变体形态），避免被「空闲区大」短路
	for i := 0; i < 4; i++ {
		off := i * ps
		binary.LittleEndian.PutUint16(buf[off+12:], 0)
		binary.LittleEndian.PutUint16(buf[off+14:], 0)
	}
	p := filepath.Join(t.TempDir(), "cipher")
	if err := os.WriteFile(p, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	if !SuspectCipherTable(p, ps) {
		t.Fatal("随机字节页未被识别为密文表")
	}
}

// 正常堆页（页头合法、空闲区大）不应被误判
func TestV127SuspectCipherNormalPage(t *testing.T) {
	const ps = 8192
	buf := make([]byte, ps*2)
	for i := 0; i < 2; i++ {
		off := i * ps
		binary.LittleEndian.PutUint16(buf[off+12:], 28)   // pd_lower
		binary.LittleEndian.PutUint16(buf[off+14:], 8000) // pd_upper → 空闲区 7972B
	}
	p := filepath.Join(t.TempDir(), "normal")
	if err := os.WriteFile(p, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	if SuspectCipherTable(p, ps) {
		t.Fatal("正常堆页被误判为密文表")
	}
}

// 阈值必须是「过半」：1/4 页可疑不应判定为加密表（普通表偶含二进制大字段）
func TestV127SuspectCipherThreshold(t *testing.T) {
	const ps = 8192
	buf := make([]byte, ps*4)
	r := rand.New(rand.NewSource(99))
	// 第 0 页随机（可疑），其余三页正常
	r.Read(buf[:ps])
	binary.LittleEndian.PutUint16(buf[12:], 0)
	binary.LittleEndian.PutUint16(buf[14:], 0)
	for i := 1; i < 4; i++ {
		off := i * ps
		binary.LittleEndian.PutUint16(buf[off+12:], 28)
		binary.LittleEndian.PutUint16(buf[off+14:], 8000)
	}
	p := filepath.Join(t.TempDir(), "mixed")
	if err := os.WriteFile(p, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	if SuspectCipherTable(p, ps) {
		t.Fatal("1/4 可疑页被误判为加密表（阈值应过半）")
	}
}
