package datatype

import "testing"

// v1.0.59：钉死 varlena 头位语义（与真实磁盘数据核对，勿按"记忆中的上游宏"改写）。
// 实证样本：金仓 V8R6C9B14 pg_proc.prosrc 短字符串，磁盘字节 0x11 "lsegin"
// → 1B 头 total = 8 = 头 1B + "lsegin" 7B。
// 规则：1B 头 = 首字节奇数（(total<<1)|1，total∈[2,127]）；
//
//	4B 头 = 首字节偶数，&0x03==0x02 为压缩（4BC），否则未压缩；
//	total_4B = (LE32>>2)&0x3FFFFFFF；外联指针 = 0x01 0x12。
func TestVarlenaHeaderSemanticsPinned(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
		kind string
		tot  int
	}{
		{"1b_total8_真实样本", []byte{0x11, 'l', 's', 'e', 'g', 'i', 'n', 0x00}, VARLENA_1B, 8},
		{"1b_total5", []byte{0x0B, 'Z', 'Z', 'Z', 'Z'}, VARLENA_1B, 5},
		{"1b_total2_最小", []byte{0x05, 'x'}, VARLENA_1B, 2},
		{"4bu_低2位00", []byte{0x10, 0x00, 0x00, 0x00, 0x01, 0x02, 0x03, 0x04}, VARLENA_4B, 4},
		{"4bc_低2位02", []byte{0x22, 0x00, 0x00, 0x00, 0x01, 0x02, 0x03, 0x04, 0x99}, VARLENA_4BC, 8},
		{"4b_total太小_非法", []byte{0x08, 0x00, 0x00, 0x00}, "", 0}, // 偶数→4B 路径，total=2<4 → 非法
	}
	for _, c := range cases {
		kind, tot, _, _ := VarlenaParse(c.raw, 0)
		if kind != c.kind || tot != c.tot {
			t.Errorf("%s: got kind=%q total=%d, want kind=%q total=%d", c.name, kind, tot, c.kind, c.tot)
		}
	}
	// 外联指针：0x01 + VARTAG_ONDISK(0x12)
	ext := append([]byte{0x01, VARTAG_ONDISK}, make([]byte, 16)...)
	kind, _, _, _ := VarlenaParse(ext, 0)
	if kind != VARLENA_EXTERNAL {
		t.Errorf("外联指针: got kind=%q, want %q", kind, VARLENA_EXTERNAL)
	}
}

// v1.0.59 回归（P1-3）：decodeOidvector/decodeInt2vector 必须剥 varlena 头 +
// 20B 数组头，按 dim1 输出元素（旧实现把头字节当元素、长度含头）。
func TestDecodeVectorStripHeaders(t *testing.T) {
	// oidvector [1, 2, 16384]：varlena 4B 头 total=4+20+12=36 → word=(36<<2)=0x90
	ov := []byte{0x90, 0x00, 0x00, 0x00} // varlena 头
	ov = append(ov, 1, 0, 0, 0)          // ndim=1
	ov = append(ov, 0, 0, 0, 0)          // dataoffset=0
	ov = append(ov, 30, 0, 0, 0)         // elemtype=30 (oidvector)
	ov = append(ov, 3, 0, 0, 0)          // dim1=3
	ov = append(ov, 1, 0, 0, 0)          // lbound=1
	for _, v := range []uint32{1, 2, 16384} {
		ov = append(ov, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
	}
	if got := decodeOidvector(ov); got != "1 2 16384" {
		t.Errorf("decodeOidvector = %q, want %q", got, "1 2 16384")
	}
	// int2vector [10, -1]：varlena 4B 头 total=4+20+4=28 → word=0x70
	iv := []byte{0x70, 0x00, 0x00, 0x00}
	iv = append(iv, 1, 0, 0, 0, 0, 0, 0, 0, 21, 0, 0, 0, 2, 0, 0, 0, 1, 0, 0, 0)
	iv = append(iv, 0x0A, 0x00, 0xFF, 0xFF)
	if got := decodeInt2vector(iv); got != "10 -1" {
		t.Errorf("decodeInt2vector = %q, want %q", got, "10 -1")
	}
	// 1B varlena 头同样要剥：1 元素 int2vector，total=1+20+2=23 → 头=(23<<1)|1=0x2F
	iv1 := []byte{0x2F}
	iv1 = append(iv1, 1, 0, 0, 0, 0, 0, 0, 0, 21, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0)
	iv1 = append(iv1, 0x07, 0x00)
	if got := decodeInt2vector(iv1); got != "7" {
		t.Errorf("decodeInt2vector(1B头) = %q, want %q", got, "7")
	}
}
