package datatype

import "testing"

// 实机 PG17.5 抓到的真实字节：int4multirange '{[1,3),[5,8)}'，datum 33 字节
func TestTmpMultiRangeReal(t *testing.T) {
	raw := []byte{67, 99, 17, 0, 0, 2, 0, 0, 0, 8, 0, 0, 0, 2, 2, 0, 0, 1, 0, 0, 0, 3, 0, 0, 0, 5, 0, 0, 0, 8, 0, 0, 0}
	got := DecodeValue(4451, raw)
	want := "{[1,3),[5,8)}"
	if got != want {
		t.Fatalf("得到 %q，期望 %q", got, want)
	}
}
