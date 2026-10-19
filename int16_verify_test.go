package main

import (
	"encoding/binary"
	"math/big"
	"testing"
)

// B6 验证：金仓 int16 (4659) 16 字节小端有符号解码
// 手工构造 16 字节二补码，调用 decodeInt16KB，与 big.Int 精确值对比
func int16bytes(hi, lo uint64) []byte {
	b := make([]byte, 16)
	binary.LittleEndian.PutUint64(b[0:8], lo)
	binary.LittleEndian.PutUint64(b[8:16], hi)
	return b
}

func exact128(hi, lo uint64) string {
	v := new(big.Int).Lsh(new(big.Int).SetUint64(hi), 64)
	v.Or(v, new(big.Int).SetUint64(lo))
	if v.Bit(127) == 1 {
		m := new(big.Int).Lsh(big.NewInt(1), 128)
		v.Sub(v, m)
	}
	return v.String()
}

func TestDecodeInt16KBB6(t *testing.T) {
	cases := []struct {
		name string
		hi   uint64
		lo   uint64
	}{
		{"min int128 (2^127 负值)", 0x8000000000000000, 0},
		{"min int128 +1", 0x8000000000000000, 1},
		{"hi=全1 lo=1 (-(2^64-1))", 0xFFFFFFFFFFFFFFFF, 1},
		{"hi=全1 lo=0x7FFFFFFFFFFFFFFF", 0xFFFFFFFFFFFFFFFF, 0x7FFFFFFFFFFFFFFF},
		{"hi=全1 lo=0x8000000000000000 (=int64 min)", 0xFFFFFFFFFFFFFFFF, 0x8000000000000000},
		{"-1", 0xFFFFFFFFFFFFFFFF, 0xFFFFFFFFFFFFFFFF},
		{"0", 0, 0},
		{"max int64", 0, 0x7FFFFFFFFFFFFFFF},
		{"2^127-1", 0x7FFFFFFFFFFFFFFF, 0xFFFFFFFFFFFFFFFF},
		{"-(2^63)-1", 0xFFFFFFFFFFFFFFFF, 0x7FFFFFFFFFFFFFFF}, // -2^63-1? no: lo-2^64 = -2^63-1
	}
	for _, c := range cases {
		want := exact128(c.hi, c.lo)
		got := decodeInt16KB(int16bytes(c.hi, c.lo))
		status := "PASS"
		if got != want {
			status = "FAIL"
		}
		t.Logf("[%s] %-28s got=%s want=%s", status, c.name, got, want)
		if got != want {
			t.Errorf("%s: got %s want %s", c.name, got, want)
		}
	}
}
