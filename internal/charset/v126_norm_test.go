// v1.2.6 P3 测试：编码名归一化别名 + 白名单。
// 作者: raysuen
package charset

import "testing"

func TestV126NormalizeUnderscoreAliases(t *testing.T) {
	cases := map[string]string{
		"euc_cn":         "euc-cn", // P3 修复点：老文档写法
		"EUC_CN":         "euc-cn",
		"euc_jp":         "euc-jp",
		"euc_kr":         "euc-kr",
		"euc_tw":         "euc-tw",
		"euc_jis_2004":   "euc-jis-2004",
		"shift_jis_2004": "shift-jis-2004",
		"mule_internal":  "mule-internal",
		// 原有别名回归锚
		"utf8": "utf-8", "utf_8": "utf-8", "UTF-8": "utf-8",
		"latin1": "latin1", "sql_ascii": "latin1", "SQL_ASCII": "latin1",
		"gbk": "gbk", "gb2312": "gbk", "936": "gbk", "gb18030": "gb18030",
		// 规范名原样
		"euc-cn": "euc-cn", "sjis": "sjis", "big5": "big5", "uhc": "uhc", "johab": "johab",
	}
	for in, want := range cases {
		if got := NormalizeEncodingName(in); got != want {
			t.Errorf("NormalizeEncodingName(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestV126IsSupportedEncoding(t *testing.T) {
	// P3 修复核心断言：euc_cn 必须受支持（修复前静默落 default 分支乱码）
	// 注：auto 由 cli 解析层单独处理，不进白名单。
	for _, ok := range []string{"euc_cn", "euc-cn", "utf8", "gbk", "gb18030", "latin1",
		"sql_ascii", "sjis", "big5", "uhc", "johab", "mule_internal"} {
		if !IsSupportedEncoding(ok) {
			t.Errorf("IsSupportedEncoding(%q) = false，应为 true", ok)
		}
	}
	// 未知名必须拒绝（修复前静默 latin-1 乱码）
	for _, bad := range []string{"win1252", "utf8mb4", "gbk_2312", "latin-9", "euckr"} {
		if IsSupportedEncoding(bad) {
			t.Errorf("IsSupportedEncoding(%q) = true，应为 false", bad)
		}
	}
}

func TestV126DecodeBytesEucCnAlias(t *testing.T) {
	// 端到端：归一化后的 euc_cn 走 gbkDecode 而非 default latin-1 兜底
	SetTextEncoding("euc_cn")
	defer SetTextEncoding("utf-8")
	got := DecodeBytes([]byte{0xBD, 0xF0, 0xB2, 0xD6}) // GBK "金仓"
	if got != "金仓" {
		t.Fatalf("--encoding euc_cn 解码 GBK 字节 = %q，期望 金仓（修复前为 latin-1 乱码 ½ð²Ö）", got)
	}
}
