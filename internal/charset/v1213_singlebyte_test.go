package charset

import (
	"strings"
	"testing"
)

// ---------- v1.2.13：单字节编码族按真实码表解码 ----------
// 期望值来自各编码的标准映射（与 PG 的 pg_wchar 表同源）。
// 修复前 9-34 全部按 latin1 展开，0x80-0xFF 区除 LATIN1 外全部错误。

func TestV1213SingleByteTables(t *testing.T) {
	old := CurTextEncoding()
	t.Cleanup(func() { SetTextEncoding(old) })
	cases := []struct {
		enc  string
		raw  []byte
		want string
	}{
		// WIN1252 的 0x80-0x9F 是拉丁标点区（latin1 此处是 C1 控制符）
		{"win1252", []byte{0x80}, "\u20ac"},             // €
		{"win1252", []byte{0x93, 0x94}, "\u201c\u201d"}, // “”
		{"win1252", []byte{0x85}, "\u2026"},             // …
		{"win1252", []byte{0xE9}, "\u00e9"},             // é（与 latin1 一致）
		// LATIN2（ISO-8859-2）：0xA1=Ą 0xB1=ą 0xE8=č
		{"latin2", []byte{0xA1, 0xB1, 0xE8}, "\u0104\u0105\u010d"},
		// LATIN9（ISO-8859-15）：0xA4=€ 0xA6=Š
		{"latin9", []byte{0xA4, 0xA6}, "\u20ac\u0160"},
		// KOI8R：0xC1=а 0xE1=А
		{"koi8r", []byte{0xC1, 0xE1}, "\u0430\u0410"},
		// ISO-8859-7（希腊）：0xC1=Α
		{"iso8859-7", []byte{0xC1}, "\u0391"},
		// WIN1251（西里尔）：0xC0=А 0xFF=я
		{"win1251", []byte{0xC0, 0xFF}, "\u0410\u044f"},
	}
	for _, c := range cases {
		SetTextEncoding(c.enc)
		if got := DecodeBytes(c.raw); got != c.want {
			t.Errorf("%s % X = %q，期望 %q", c.enc, c.raw, got, c.want)
		}
	}
}

// 纯 ASCII 在任何单字节编码下都必须原样直通
func TestV1213SingleByteASCII(t *testing.T) {
	old := CurTextEncoding()
	t.Cleanup(func() { SetTextEncoding(old) })
	for _, enc := range []string{"win1252", "latin2", "koi8r", "iso8859-5"} {
		SetTextEncoding(enc)
		if got := DecodeBytes([]byte("hello, world")); got != "hello, world" {
			t.Errorf("%s ASCII 直通失败: %q", enc, got)
		}
	}
}

// 未定义码位（WIN1252 的 0x81/0x8D/0x8F/0x90/0x9D）回退 U+00XX 并计数告警，
// 不得静默丢弃、也不得整串走 latin1。
func TestV1213SingleByteUndefinedByte(t *testing.T) {
	old := CurTextEncoding()
	t.Cleanup(func() { SetTextEncoding(old) })
	SingleByteUndefSummary() // 清掉其它用例的残留

	SetTextEncoding("win1252")
	got := DecodeBytes([]byte{0x81})
	if got != "\u0081" {
		t.Fatalf("win1252 0x81（未定义）= %q，期望 U+0081 原样保留", got)
	}
	s := SingleByteUndefSummary()
	if s == "" {
		t.Fatal("未定义字节未触发告警汇总")
	}
	if !strings.Contains(s, "win1252") {
		t.Errorf("告警未点名编码: %s", s)
	}
	if again := SingleByteUndefSummary(); again != "" {
		t.Errorf("汇总调用后应清零，实际: %s", again)
	}
}

// 归一化：PG 官方名 / 连字符 / 下划线三种写法都要落到同一码表键
func TestV1213SingleByteNormalize(t *testing.T) {
	cases := map[string]string{
		"WIN1252":    "win1252",
		"win1252":    "win1252",
		"LATIN2":     "latin2",
		"ISO_8859_5": "iso8859-5",
		"iso-8859-7": "iso8859-7",
		"iso8859_8":  "iso8859-8",
		"KOI8R":      "koi8r",
		"WIN874":     "win874",
	}
	for in, want := range cases {
		if got := NormalizeEncodingName(in); got != want {
			t.Errorf("NormalizeEncodingName(%q) = %q，期望 %q", in, got, want)
		}
		if !IsSupportedEncoding(in) {
			t.Errorf("IsSupportedEncoding(%q) = false，应为 true", in)
		}
	}
}

// pg_enc 编号 → 编码名：21 是 WIN874（不是 WIN1252），24 才是 WIN1252
func TestV1213EncodingIDMap(t *testing.T) {
	cases := map[int]string{
		8: "latin1", 9: "latin2", 17: "latin10", 21: "win874",
		22: "koi8r", 23: "win1251", 24: "win1252", 34: "koi8u",
	}
	for id, want := range cases {
		if got := EncodingIDToCodec[id]; got != want {
			t.Errorf("EncodingIDToCodec[%d] = %q，期望 %q", id, got, want)
		}
	}
	// 26 种单字节编码必须全部有码表
	for id := 9; id <= 34; id++ {
		name := NormalizeEncodingName(EncodingIDToCodec[id])
		if _, ok := sbTables[name]; !ok {
			t.Errorf("pg_enc %d (%s) 缺码表", id, EncodingIDToCodec[id])
		}
	}
}
