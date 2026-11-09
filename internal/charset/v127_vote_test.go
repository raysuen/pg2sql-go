// v1.2.7 测试（F2-2）：表级编码投票的证据门槛。
// 旧实现「单票即覆盖」：加密表/压缩页/单页小表凭一页噪声就能把库级判定推翻，
// 整表中文按错误编码解码（静默乱码）。新规则要求 ≥2 票且胜出占比 ≥2/3。
package charset

import "testing"

func v127GBK() []byte {
	return []byte{0xD3, 0xC3, 0xBB, 0xA7, 0xD0, 0xD5, 0xC3, 0xFB, 0xB2, 0xE2, 0xCA, 0xD4}
}

func v127UTF8() []byte { return []byte("用户姓名测试数据") }

// 单页 GBK：证据不足，不给结论，但必须带回 WinEnc 供调用方告警
func TestV127VoteSinglePageNotAdopted(t *testing.T) {
	v := ProbeFileEncodingVote(t54WritePages(t, [][]byte{t54TextPage(v127GBK())}), 8192)
	if v.Enc != "" {
		t.Fatalf("单页 GBK 投票结论 = %q，期望空（证据不足）", v.Enc)
	}
	if v.WinEnc != "gbk" || v.WinVotes != 1 || v.Total != 1 {
		t.Fatalf("单页明细 = %+v，期望 {WinEnc:gbk WinVotes:1 Total:1}", v)
	}
}

// 单页 UTF8 同样不采用（对称）
func TestV127VoteSinglePageUTF8NotAdopted(t *testing.T) {
	v := ProbeFileEncodingVote(t54WritePages(t, [][]byte{t54TextPage(v127UTF8())}), 8192)
	if v.Enc != "" {
		t.Fatalf("单页 UTF8 投票结论 = %q，期望空", v.Enc)
	}
}

// 2 票 GBK + 1 票 UTF8 = 2/3，达阈值 → 采用 gbk
func TestV127VoteTwoThirdsAdopted(t *testing.T) {
	pages := [][]byte{t54TextPage(v127GBK()), t54TextPage(v127GBK()), t54TextPage(v127UTF8())}
	v := ProbeFileEncodingVote(t54WritePages(t, pages), 8192)
	if v.Enc != "gbk" || v.WinVotes != 2 || v.Total != 3 {
		t.Fatalf("2/3 投票 = %+v，期望 gbk 2/3", v)
	}
}

// 3 票 GBK + 2 票 UTF8 = 3/5 < 2/3 → 不采用（页间矛盾）
func TestV127VoteBelowThresholdRejected(t *testing.T) {
	pages := [][]byte{
		t54TextPage(v127GBK()), t54TextPage(v127GBK()), t54TextPage(v127GBK()),
		t54TextPage(v127UTF8()), t54TextPage(v127UTF8()),
	}
	v := ProbeFileEncodingVote(t54WritePages(t, pages), 8192)
	if v.Enc != "" {
		t.Fatalf("3/5 投票结论 = %q，期望空（未达 2/3）", v.Enc)
	}
	if v.WinEnc != "gbk" || v.WinVotes != 3 || v.Total != 5 {
		t.Fatalf("3/5 明细 = %+v", v)
	}
}

// 多页一致仍是强证据（v1.0.54 行为不回退）
func TestV127VoteUnanimousStillWorks(t *testing.T) {
	pages := [][]byte{t54TextPage(v127GBK()), t54TextPage(v127GBK()), t54TextPage(v127GBK())}
	if v := ProbeFileEncodingVote(t54WritePages(t, pages), 8192); v.Enc != "gbk" {
		t.Fatalf("3 票一致 GBK 结论 = %q，期望 gbk", v.Enc)
	}
	up := [][]byte{t54TextPage(v127UTF8()), t54TextPage(v127UTF8())}
	if v := ProbeFileEncodingVote(t54WritePages(t, up), 8192); v.Enc != "utf8" {
		t.Fatalf("2 票一致 UTF8 结论 = %q，期望 utf8", v.Enc)
	}
}

// 兼容包装：ProbeFileEncodingMajority 必须与新接口结论一致
func TestV127MajorityWrapperConsistent(t *testing.T) {
	p := t54WritePages(t, [][]byte{t54TextPage(v127GBK()), t54TextPage(v127GBK())})
	if got := ProbeFileEncodingMajority(p, 8192); got != "gbk" {
		t.Fatalf("包装函数 = %q，期望 gbk", got)
	}
}
