// v1.2.16 回归（外部回归报告 D3）：pg_authid 口令/有效期按布局尾部锚定。
//
// 背景：金仓 V8/V9 的 pg_authid 在 rolconnlimit 之后多出 rolconntime(attnum 11,
// int8) 与 rolconninterval(attnum 12, varlena)，rolpassword/rolvaliduntil 顺延到
// 13/14。旧实现按 PG12 固定位置取 off+9/off+10，读到的是 rolconntime/
// rolconninterval → PASSWORD 与 VALID UNTIL 静默丢失（实机 V8R6C9B14 复现）。
package objects

import (
	"pg2sql/internal/datatype"
	"testing"
)

func cl(n int, varlena bool) datatype.ColLen {
	return datatype.ColLen{AttLen: n, IsVarlena: varlena, AttAlign: "i"}
}

// pgAuthidLayout：PG 12+ 的 12 列布局（含 oid）。
func pgAuthidLayout() []datatype.ColLen {
	l := []datatype.ColLen{cl(4, false), {AttLen: 64, AttAlign: "c"}}
	for i := 0; i < 7; i++ {
		l = append(l, datatype.ColLen{AttLen: 1, AttAlign: "c"})
	}
	return append(l, cl(4, false), cl(0, true), cl(8, false))
}

// kbAuthidLayout：金仓 V8/V9 的 14 列布局（rolconntime 8B + rolconninterval varlena）。
func kbAuthidLayout() []datatype.ColLen {
	l := []datatype.ColLen{cl(4, false), {AttLen: 64, AttAlign: "c"}}
	for i := 0; i < 7; i++ {
		l = append(l, datatype.ColLen{AttLen: 1, AttAlign: "c"})
	}
	return append(l, cl(4, false), cl(8, false), cl(0, true), cl(0, true), cl(8, false))
}

func TestV1216AuthidTailIndexes(t *testing.T) {
	cases := []struct {
		name   string
		layout []datatype.ColLen
		wantPW int
		wantVU int
	}{
		{"pg12", pgAuthidLayout(), 10, 11},
		{"kingbase", kbAuthidLayout(), 12, 13},
	}
	for _, c := range cases {
		pw, vu := authidTailIndexes(c.layout, 1)
		if pw != c.wantPW || vu != c.wantVU {
			t.Errorf("%s: authidTailIndexes = (%d,%d)，期望 (%d,%d)",
				c.name, pw, vu, c.wantPW, c.wantVU)
		}
		if !validAuthidLayout(c.layout) {
			t.Errorf("%s: validAuthidLayout 判定为非法布局", c.name)
		}
	}
}

// 金仓 14 列布局必须通过校验（旧实现因 l[off+10] 是 varlena 而拒绝，
// 回退 PG 固定布局后口令/有效期全部错位）。
func TestV1216AuthidKingbaseLayoutAccepted(t *testing.T) {
	if !validAuthidLayout(kbAuthidLayout()) {
		t.Fatal("金仓 14 列 pg_authid 布局被判定为非法，将回退固定布局导致口令/有效期丢失")
	}
}

// 尾部不可锚定（末列不是 8B、或倒数第二列不是 varlena）时应拒绝，避免误判。
func TestV1216AuthidBadLayoutRejected(t *testing.T) {
	bad := kbAuthidLayout()
	bad[len(bad)-1] = cl(4, false) // rolvaliduntil 长度不对
	if validAuthidLayout(bad) {
		t.Error("末列非 8B 的布局不应通过校验")
	}
	bad2 := pgAuthidLayout()
	bad2[10] = cl(8, false) // 把 rolpassword 槽改成定长 → 尾部无法锚定 varlena 列
	if pw, _ := authidTailIndexes(bad2, 1); pw >= 0 {
		t.Errorf("尾部没有 varlena 列时不应锚定出口令位置，得到 %d", pw)
	}
	if validAuthidLayout(bad2) {
		t.Error("尾部无法锚定口令列的布局不应通过校验")
	}
}
