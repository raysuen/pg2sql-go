// v1.2.18 回归用例：typmod 强转不得按函数名裸匹配（P3）。
//
// 缺陷现场：procMap 只有函数名、没有 namespace，ntTypmodCastSpec 对 name == "numeric"
// 一律按 PG 内置语义解码第二参为 typmod：
//
//	CREATE FUNCTION myschema.numeric(numeric, integer) RETURNS numeric ...
//	-- 默认值/CHECK 里调用 myschema.numeric(col, 2)
//	v1.2.17 输出：CAST(col AS numeric)     ← 函数调用被静默改成类型转换
//
// 这比渲染成非法 SQL 更糟：非法 SQL 至少报错，静默改写语义不会。
package nt

import (
	"strings"
	"testing"
)

// 构造一个两参 FUNCEXPR 节点树：target(inner, 2)
func funcExprTree(funcid uint32, inner string) string {
	return `{FUNCEXPR :funcid ` + uitoa(funcid) + ` :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 0 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location 1 :constvalue 4 [ ` + inner + ` ]})}`
}

func uitoa(v uint32) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}

// 系统 schema 下的 numeric(value, typmod) 仍必须还原成 CAST
// （这是 v1.2.14 的修复，不能被本轮的 namespace 门控误伤）。
func TestV1218TypmodCastSystemProcStillWorks(t *testing.T) {
	old := procNamespaces
	t.Cleanup(func() { procNamespaces = old })

	procNamespaces = map[uint32]string{1701: "pg_catalog"}
	procMap := map[uint32]string{1701: "numeric"}
	opMap := map[uint32]OpInfo{}

	// 第二参 typmod = ((6<<16)|2)+4 = 393222 = 0x00060006
	// 注意 constvalue 里的数字是**小端字节序列**（nodeToString 输出有符号字节），
	// 不是十进制数值本身：0x00060006 → 06 00 06 00。
	raw := `{FUNCEXPR :funcid 1701 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 0 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location 1 :constvalue 4 [ 0 0 0 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location 2 :constvalue 4 [ 6 0 6 0 ]})}`
	got := DeparseNodeTree(raw, opMap, procMap, nil)
	if !strings.Contains(got, "CAST(") || !strings.Contains(got, "numeric(6,2)") {
		t.Errorf("系统 numeric(value, typmod) 应还原为 CAST(... AS numeric(6,2))，实际 %q", got)
	}
}

// 用户 schema 下的同名两参函数不得被改写成 CAST。
func TestV1218TypmodCastUserProcNotRewritten(t *testing.T) {
	old := procNamespaces
	t.Cleanup(func() { procNamespaces = old })

	procNamespaces = map[uint32]string{99999: "myschema"}
	procMap := map[uint32]string{99999: "numeric"}
	opMap := map[uint32]OpInfo{}

	raw := `{FUNCEXPR :funcid 99999 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 0 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location 1 :constvalue 4 [ 0 0 0 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location 2 :constvalue 4 [ 2 0 0 0 ]})}`
	got := DeparseNodeTree(raw, opMap, procMap, nil)
	if strings.Contains(got, "CAST(") {
		t.Errorf("用户 schema 下的 myschema.numeric(col, 2) 被误渲染成 CAST（函数调用被静默改成类型转换）: %q", got)
	}
	if !strings.Contains(got, "numeric(") {
		t.Errorf("应退回通用函数调用渲染 numeric(a, b)，实际 %q", got)
	}
	// 必须补上 schema 限定：裸 numeric(12.5, 2) 回灌时会被 search_path 解析到
	// pg_catalog.numeric，进而变成语法错误（PG: syntax error at or near ";"）。
	// 这是 v1.2.18 第一版漏掉的一环 —— 只拒绝 typmod 分支还不够。
	if !strings.Contains(got, `"myschema".numeric(`) {
		t.Errorf("用户 schema 下的函数调用必须带 schema 限定（否则回灌被解析成 pg_catalog 同名函数 → 语法错误），实际 %q", got)
	}
}

// 映射缺失（目录读不到）时必须向后兼容：按系统函数处理，
// 否则每张带 typmod 默认值的表都会退回 v1.2.13 的非法 SQL。
func TestV1218TypmodCastNilMapKeepsCompat(t *testing.T) {
	old := procNamespaces
	t.Cleanup(func() { procNamespaces = old })

	procNamespaces = nil
	procMap := map[uint32]string{1701: "varchar"}
	opMap := map[uint32]OpInfo{}

	// varchar(value, 14, FALSE) → typmod 14-4 = 10 → varchar(10)
	raw := `{FUNCEXPR :funcid 1701 :funcresulttype 1043 :funcretset false :funcvariadic false :funcformat 0 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 25 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location 1 :constvalue 4 [ 0 0 0 0 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location 2 :constvalue 4 [ 14 0 0 0 ]} {CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location 3 :constvalue 1 [ 0 ]})}`
	got := DeparseNodeTree(raw, opMap, procMap, nil)
	if !strings.Contains(got, "CAST(") {
		t.Errorf("映射缺失时应保持 v1.2.17 行为（还原 CAST），否则 typmod 默认值会退回非法 SQL：%q", got)
	}
}

// isSystemProc 三态直测
func TestV1218IsSystemProc(t *testing.T) {
	old := procNamespaces
	t.Cleanup(func() { procNamespaces = old })

	procNamespaces = nil
	if !isSystemProc(1) {
		t.Error("映射为 nil 时应保守返回 true（向后兼容）")
	}

	procNamespaces = map[uint32]string{
		1: "pg_catalog",
		2: "sys",
		3: "sys_catalog",
		4: "myschema",
		5: "public",
	}
	cases := map[uint32]bool{1: true, 2: true, 3: true, 4: false, 5: false, 99: true}
	for oid, want := range cases {
		if got := isSystemProc(oid); got != want {
			t.Errorf("isSystemProc(%d) = %v, want %v", oid, got, want)
		}
	}
}
