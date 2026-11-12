// v1.2.13：reg* 剩余族（regproc/regprocedure/regoper/regoperator/regconfig/
// regdictionary/regrole/regcollation）反查表解码的单测。
// 目录数据按 PG17 真实布局手工构造；fallback（反查 miss → 裸 OID、
// InvalidOid → "-"）与可见性限定（pg_catalog/public 省略前缀）一并覆盖。
package datatype

import (
	"encoding/binary"
	"testing"
)

func oidBytes(t *testing.T, oid uint32) []byte {
	t.Helper()
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, oid)
	return b
}

// regFamilySetup：注入反查表 + pg_type/pg_namespace 快照，返回还原函数。
func regFamilySetup(t *testing.T) func() {
	t.Helper()
	oldNsp, oldDefs := regNamespaceNames, DynamicTypeDefs
	oldProc, oldOper := regProcInfos, regOperInfos
	oldCfg, oldDict := regConfigInfos, regDictInfos
	oldRole, oldColl := regRoleNames, regCollInfos
	oldDefault := DefaultRegNamespace
	oldNONE := regOperNONEArgs
	regOperNONEArgs = true // 按 PG16+ 形态断言；旧形态另测

	regNamespaceNames = map[uint32]string{11: "pg_catalog", 2200: "public", 90000: "biz"}
	DynamicTypeDefs = map[uint32]*TypeDef{
		23:    {OID: 23, Name: "int4", Nsp: 11},
		20:    {OID: 20, Name: "int8", Nsp: 11},
		701:   {OID: 701, Name: "float8", Nsp: 11},
		90001: {OID: 90001, Name: "mytype", Nsp: 90000},
		90002: {OID: 90002, Name: "_mytype", Nsp: 0, Elem: 90001}, // 数组类型，归属按元素
	}
	regProcInfos = map[uint32]RegProcInfo{
		42:    {Name: "now", Nsp: 11}, // 无参
		44:    {Name: "int4pl", Nsp: 11, Args: []uint32{23, 23}},
		50001: {Name: "myfn", Nsp: 90000, Args: []uint32{90001}},
		50002: {Name: "arrfn", Nsp: 90000, Args: []uint32{90002}},
		50003: {Name: "mixfn", Nsp: 90000, Args: []uint32{23, 99999}}, // 99999 不在 pg_type
	}
	regOperInfos = map[uint32]RegOperInfo{
		55:  {Name: "+", Nsp: 11, Kind: 'b', Left: 23, Right: 23},
		56:  {Name: "-", Nsp: 11, Kind: 'l', Right: 23},
		57:  {Name: "+", Nsp: 11, Kind: 'r', Left: 23}, // PG13- 后缀算符
		58:  {Name: "int8pl", Nsp: 11, Kind: 'b', Left: 20, Right: 20},
		59:  {Name: "myop", Nsp: 90000, Kind: 'b', Left: 90001, Right: 90001},
		500: {Name: "#", Nsp: 90000, Kind: 'b', Left: 23, Right: 23}, // 算符名非法标识符
	}
	regConfigInfos = map[uint32]RegNamed{
		3730:  {Name: "english", Nsp: 11},
		50004: {Name: "mycfg", Nsp: 90000},
	}
	regDictInfos = map[uint32]RegNamed{
		3735:  {Name: "simple", Nsp: 11},
		50005: {Name: "mydict", Nsp: 90000},
	}
	regRoleNames = map[uint32]string{10: "postgres", 50100: "MyRole"}
	regCollInfos = map[uint32]RegNamed{
		877:   {Name: "C", Nsp: 11},
		50006: {Name: "mycoll", Nsp: 90000},
	}
	return func() {
		regNamespaceNames, DynamicTypeDefs = oldNsp, oldDefs
		regProcInfos, regOperInfos = oldProc, oldOper
		regConfigInfos, regDictInfos = oldCfg, oldDict
		regRoleNames, regCollInfos = oldRole, oldColl
		DefaultRegNamespace = oldDefault
		regOperNONEArgs = oldNONE
	}
}

func TestV1213Regproc(t *testing.T) {
	defer regFamilySetup(t)()
	cases := []struct {
		oid  uint32
		want string
	}{
		{42, "now"},         // pg_catalog → 省略前缀
		{50001, "biz.myfn"}, // 非 search_path → schema 限定
		{0, "-"},            // InvalidOid
		{777777, "777777"},  // pg_proc 反查 miss → 裸 OID
	}
	for _, c := range cases {
		if got := decodeRegproc(oidBytes(t, c.oid)); got != c.want {
			t.Errorf("decodeRegproc(%d) = %q, want %q", c.oid, got, c.want)
		}
	}
}

func TestV1213Regprocedure(t *testing.T) {
	defer regFamilySetup(t)()
	cases := []struct {
		oid  uint32
		want string
	}{
		{42, "now()"},
		{44, "int4pl(integer,integer)"},
		{50001, "biz.myfn(biz.mytype)"},     // 函数不可见 → 参数类型一并限定
		{50002, "biz.arrfn(biz.mytype[])"},  // 数组参数：typname "_mytype" → "mytype[]"
		{50003, "biz.mixfn(integer,99999)"}, // pg_type miss 的参数回退裸 OID
		{0, "-"},
		{888888, "888888"},
	}
	for _, c := range cases {
		if got := decodeRegprocedure(oidBytes(t, c.oid)); got != c.want {
			t.Errorf("decodeRegprocedure(%d) = %q, want %q", c.oid, got, c.want)
		}
	}
}

func TestV1213Regoper(t *testing.T) {
	defer regFamilySetup(t)()
	cases := []struct {
		oid  uint32
		want string
	}{
		{55, "+"},        // 可见算符：输出原名，不加引号（对齐 PG regoperout）
		{59, "biz.myop"}, // 不可见：schema 限定
		{500, "biz.#"},   // 不可见算符：原文拼接不 quote（实测 'zzprobe.##'::regoper）
		{0, "-"},
		{999999, "999999"},
	}
	for _, c := range cases {
		if got := decodeRegoper(oidBytes(t, c.oid)); got != c.want {
			t.Errorf("decodeRegoper(%d) = %q, want %q", c.oid, got, c.want)
		}
	}
}

func TestV1213Regoperator(t *testing.T) {
	defer regFamilySetup(t)()
	cases := []struct {
		oid  uint32
		want string
	}{
		{55, "+(integer,integer)"}, // 双目
		{56, "-(NONE,integer)"},    // 前缀（PG16+：缺失参数输出 NONE）
		{57, "(integer,NONE)+"},    // 后缀（PG13- 才存在；按 PG16+ 形态断言）
		{58, "int8pl(bigint,bigint)"},
		{59, "biz.myop(biz.mytype,biz.mytype)"},
		{500, "biz.#(integer,integer)"},
		{0, "-"},
		{999999, "999999"},
	}
	for _, c := range cases {
		if got := decodeRegoperator(oidBytes(t, c.oid)); got != c.want {
			t.Errorf("decodeRegoperator(%d) = %q, want %q", c.oid, got, c.want)
		}
	}
	// PG12-15 / 金仓的旧形态：缺失参数直接省略（回灌旧版必须用此形态）
	regOperNONEArgs = false
	defer func() { regOperNONEArgs = true }()
	if got := decodeRegoperator(oidBytes(t, 56)); got != "-(integer)" {
		t.Errorf("decodeRegoperator(56) 旧形态 = %q, want -(integer)", got)
	}
	if got := decodeRegoperator(oidBytes(t, 57)); got != "(integer)+" {
		t.Errorf("decodeRegoperator(57) 旧形态 = %q, want (integer)+", got)
	}
}

func TestV1213RegconfigRegdictionaryRegcollation(t *testing.T) {
	defer regFamilySetup(t)()
	if got := decodeRegconfig(oidBytes(t, 3730)); got != "english" {
		t.Errorf("regconfig builtin = %q, want english", got)
	}
	if got := decodeRegconfig(oidBytes(t, 50004)); got != "biz.mycfg" {
		t.Errorf("regconfig custom = %q, want biz.mycfg", got)
	}
	if got := decodeRegdictionary(oidBytes(t, 3735)); got != "simple" {
		t.Errorf("regdictionary builtin = %q, want simple", got)
	}
	if got := decodeRegdictionary(oidBytes(t, 50005)); got != "biz.mydict" {
		t.Errorf("regdictionary custom = %q, want biz.mydict", got)
	}
	// 实测：regcollationout 可见也走 quote_identifier，"C" 输出为带引号的 "C"
	if got := decodeRegcollation(oidBytes(t, 877)); got != `"C"` {
		t.Errorf("regcollation builtin = %q, want %q (quote_identifier 恒走)", got, `"C"`)
	}
	if got := decodeRegcollation(oidBytes(t, 50006)); got != "biz.mycoll" {
		t.Errorf("regcollation custom = %q, want biz.mycoll", got)
	}
	for _, oid := range []uint32{0, 999999} {
		want := "-"
		if oid != 0 {
			want = "999999"
		}
		if got := decodeRegconfig(oidBytes(t, oid)); got != want {
			t.Errorf("regconfig(%d) = %q, want %q", oid, got, want)
		}
	}
}

func TestV1213Regrole(t *testing.T) {
	defer regFamilySetup(t)()
	if got := decodeRegrole(oidBytes(t, 10)); got != "postgres" {
		t.Errorf("regrole = %q, want postgres", got)
	}
	// v1.2.17 更正：PG regroleout 走 quote_identifier(rolname)，需引号的名字必须带引号。
	// 该断言在 v1.2.13 写反了（当时断言"原文输出"），实测金仓 V9R1C10 / PG 17.6：
	// 'My Role'::regrole → "My Role"；'system'::regrole → system。
	if got := decodeRegrole(oidBytes(t, 50100)); got != `"MyRole"` {
		t.Errorf("regrole quoted-name = %q, want %q (quote_identifier)", got, `"MyRole"`)
	}
	if got := decodeRegrole(oidBytes(t, 0)); got != "-" {
		t.Errorf("regrole(0) = %q, want -", got)
	}
	if got := decodeRegrole(oidBytes(t, 999999)); got != "999999" {
		t.Errorf("regrole miss = %q, want 999999", got)
	}
}

func TestV1213RegFamilyNilMaps(t *testing.T) {
	// 反查表整体缺失（如 pg_attribute 不可读）时全部退化为裸 OID，不得 panic
	oldProc, oldOper := regProcInfos, regOperInfos
	oldCfg, oldDict := regConfigInfos, regDictInfos
	oldRole, oldColl := regRoleNames, regCollInfos
	regProcInfos, regOperInfos = nil, nil
	regConfigInfos, regDictInfos = nil, nil
	regRoleNames, regCollInfos = nil, nil
	defer func() {
		regProcInfos, regOperInfos = oldProc, oldOper
		regConfigInfos, regDictInfos = oldCfg, oldDict
		regRoleNames, regCollInfos = oldRole, oldColl
	}()
	for oid, dec := range map[string]func([]byte) string{
		"regproc": decodeRegproc, "regprocedure": decodeRegprocedure,
		"regoper": decodeRegoper, "regoperator": decodeRegoperator,
		"regconfig": decodeRegconfig, "regdictionary": decodeRegdictionary,
		"regrole": decodeRegrole, "regcollation": decodeRegcollation,
	} {
		if got := dec(oidBytes(t, 42)); got != "42" {
			t.Errorf("%s(nil maps) = %q, want 42", oid, got)
		}
	}
}
