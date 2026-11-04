package charset

// v1.0.65 回归用例（F6）：GB18030 截断兜底不得产出非法 UTF-8。
// 场景：b1∈81-FE、b2∈30-39（四字节序列开头）但剩余不足 4 字节（损坏页/截断 tuple）。
// 旧实现把剩余每个字节统一套 0xC0|c>>6/0x80|c&0x3F，ASCII 字节会变成 C0/C1 开头的
// 非法 UTF-8（且 U+00B0 反向映射不可逆）。

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestGB18030TruncatedKeepsValidUTF8(t *testing.T) {
	cases := [][]byte{
		{0x81, 0x30, 0x41},       // 截断四字节序列 + ASCII 'A'
		{0x81, 0x30, 0x81},       // 截断四字节序列 + 高位字节
		{0x81, 0x30},             // 仅两字节
		{0x41, 0x81, 0x30, 0x42}, // ASCII 前缀 + 截断序列
	}
	for _, in := range cases {
		got := decodeGB18030(in)
		if !utf8.ValidString(got) {
			t.Errorf("decodeGB18030(% X) = %q 不是合法 UTF-8", in, got)
		}
		// ASCII 字节必须原样直通（不受高位字节兜底逻辑影响）
		for _, b := range in {
			if b < 0x80 && !strings.ContainsRune(got, rune(b)) {
				t.Errorf("decodeGB18030(% X) 丢失 ASCII 字节 %q: got %q", in, string(rune(b)), got)
			}
		}
	}
}

// 正常四字节序列（U+10000）不受影响
func TestGB18030FourByteStillWorks(t *testing.T) {
	if got := decodeGB18030([]byte{0x90, 0x30, 0x81, 0x30}); got != "\U00010000" {
		t.Fatalf("U+10000 解码错误: got %q", got)
	}
}
