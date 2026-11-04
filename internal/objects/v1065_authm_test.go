// v1.0.65 修复回归用例（objects 侧）：
// F7 金仓 pg_auth_members 布局锚点 / F15 --large-object-oids 非法项告警
package objects

import (
	"testing"

	"pg2sql/internal/datatype"
)

// ---- F7：布局校验必须能识别"系统列混入正 attnum"的伪布局 ----

func TestV1065AuthMembersLayoutAnchor(t *testing.T) {
	col := func(n int) datatype.ColLen { return datatype.ColLen{AttLen: n, AttAlign: "i"} }

	// 正确布局：[oid 4][roleid 4][member 4][grantor 4][admin 1]
	good := []datatype.ColLen{col(4), col(4), col(4), col(4), {AttLen: 1, AttAlign: "c"}}
	if !validAuthMembersLayout(good) {
		t.Fatalf("标准 pg_auth_members 布局应通过校验")
	}

	// 金仓 V8/V9：系统列正 attnum 1-6 混入 → [tableoid][cmax][xmax][cmin][xmin]...
	// 旧判据只查前三个 4B，会把 cmax/xmax 当成 roleid/member 解析。
	sys := []datatype.ColLen{col(4), col(4), col(4), col(4), col(4), col(4)}
	if validAuthMembersLayout(sys) {
		t.Fatalf("含系统列的伪布局必须被拒绝（否则成员关系会被静默丢成 0）")
	}

	// 列数不足同样拒绝
	if validAuthMembersLayout([]datatype.ColLen{col(4), col(4)}) {
		t.Fatalf("列数不足应拒绝")
	}
}

// ---- F15：非法 OID 项不得静默丢弃（解析层面至少不污染结果集） ----

func TestV1065ParseLargeObjectOIDs(t *testing.T) {
	m := ParseLargeObjectOIDs(" 90001, 90003 ,,abc, 90005")
	if len(m) != 3 {
		t.Fatalf("应保留 3 个合法 OID，实际 %d: %v", len(m), m)
	}
	for _, oid := range []uint32{90001, 90003, 90005} {
		if !m[oid] {
			t.Fatalf("合法 OID %d 丢失", oid)
		}
	}
	if ParseLargeObjectOIDs("   ") != nil {
		t.Fatalf("空白输入应返回 nil（不过滤）")
	}
}
