package charset

// v1.0.58 回归测试（P1）：金仓 GBK 库（pg_database.encoding=35）导出被转义为
// E'\x…' hex 文本。根因是 EncodingIDToCodec 按上游 PG 语义把 35 映射为 sjis
// （不支持解码 → \x hex 兜底），而金仓把 35–44 重排为自有服务端编码
// （35=GBK、36=UNICODE、37=UTF8MB4、38=GB2312、39=GB18030，V8R6C9B14
// pg_encoding_to_char 实测）。修复：金仓路径改用 EncodingIDToCodecKB。
//
// 这里钉住两层：① 编号→codec 映射不得回退；② 报告现场的原始 GBK 字节
// （"中文测试数据名称" = d6d0cec4b2e2cad4cafdbeddc3fbb3c6）必须解码为中文。

import "testing"

func TestV1058KBEncodingMap(t *testing.T) {
	kb := map[int]string{
		35: "gbk", 36: "utf-8", 37: "utf-8", 38: "euc-cn", 39: "gb18030",
		40: "sjis", 41: "big5", 42: "uhc", 43: "johab", 44: "shift-jis-2004",
	}
	for id, want := range kb {
		if got := EncodingIDToCodecKB[id]; got != want {
			t.Errorf("EncodingIDToCodecKB[%d] = %q，期望 %q（金仓 pg_encoding_to_char 实测编号）", id, got, want)
		}
	}
	// 0–34 必须与上游一致
	for id, want := range EncodingIDToCodec {
		if id <= 34 && EncodingIDToCodecKB[id] != want {
			t.Errorf("EncodingIDToCodecKB[%d] = %q，与上游 %q 不一致", id, EncodingIDToCodecKB[id], want)
		}
	}
	// 上游表保持 35=sjis（仅客户端语义，钉住防止再次双向漂移）
	if EncodingIDToCodec[35] != "sjis" || EncodingIDToCodec[39] != "gb18030" {
		t.Errorf("上游表 35/39 被改动：%q/%q", EncodingIDToCodec[35], EncodingIDToCodec[39])
	}
}

// 报告现场的原始 GBK 字节：源库 octet_length=16、导入后变 34 字符转义文本。
func TestV1058GBKReportBytesDecode(t *testing.T) {
	raw := []byte{0xD6, 0xD0, 0xCE, 0xC4, 0xB2, 0xE2, 0xCA, 0xD4,
		0xCA, 0xFD, 0xBE, 0xDD, 0xC3, 0xFB, 0xB3, 0xC6}
	want := "中文测试数据名称"
	for _, enc := range []string{"gbk", "euc-cn", "gb18030"} {
		SetTextEncoding(enc)
		if got := DecodeBytes(raw); got != want {
			t.Errorf("DecodeBytes(当前编码=%s, 报告现场GBK字节) = %q，期望 %q", enc, got, want)
		}
	}
}
