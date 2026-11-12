package catalog

// v1.2.17（P2）域 DEFAULT 读取单测。
//
// 两段 fixture 是测试机真实行字节（从 pg_type 数据页 dump 的原始元组）：
//
//	kbV9：金仓 V9R1C10B4 test 库 "My Sch".dom_d（int DEFAULT 7）
//	      natts=32、typbasetype@160、typtypmod@164、168/172 两个 0 dword（其中
//	      一个是 pg_attribute 里看不到的隐藏列）、typcollation@176、
//	      typdefaultbin@180（151 字节）、typdefault@331（2 字节 1 字节头 "7"）
//	kbV8：金仓 V8R6C9B14  test 库 dtest.d1（int DEFAULT 7）
//	      natts=31、typbasetype@160、typtypmod@164、typndims@168、
//	      typcollation@172、typdefaultbin@176（151 字节）、typdefault@327
//
// 两者差一个 4 字节槽位，正是"不能按列号硬编码"的原因；定位靠
// "恰好读到元组末尾"的校验，见 typeTailDefault 注释。

import (
	"encoding/hex"
	"testing"

	"pg2sql/internal/datatype"
)

const kbV9DomDRaw = "0604000000000000000000000000180019002000030920ffffff7f000000000072420000646f6d5f64000000000000000000" +
	"0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000" +
	"6c4200000a000000040001644e00012c000000000000000071420000250a00002b000000260a000067090000000000000000" +
	"0000000000006970000017000000ffffffff0000000000000000000000005c0200007b434f4e5354203a636f6e7374747970" +
	"65203233203a636f6e73747479706d6f64202d31203a636f6e7374636f6c6c69642030203a636f6e73746c656e2034203a63" +
	"6f6e7374627976616c2074727565203a636f6e737469736e756c6c2066616c7365203a6c6f636174696f6e203434203a636f" +
	"6e737476616c75652034205b20372030203020302030203020302030205d7d0537"

const kbV8DomDRaw = "38110000000000000100000000001d00cb001f00030820ffffff3f00000000008e8f00006431000000000000000000000000" +
	"0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000" +
	"8c8f00000a000000040001644e00012c00000000000000008d8f0000250a00002b000000260a000067090000000000000000" +
	"0000000000006970000017000000ffffffff00000000000000005c0200007b434f4e5354203a636f6e737474797065203233" +
	"203a636f6e73747479706d6f64202d31203a636f6e7374636f6c6c69642030203a636f6e73746c656e2034203a636f6e7374" +
	"627976616c2074727565203a636f6e737469736e756c6c2066616c7365203a6c6f636174696f6e203733203a636f6e737476" +
	"616c75652034205b20372030203020302030203020302030205d7d0537"

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("fixture hex 解码失败: %v", err)
	}
	return b
}

// mkNulls：构造 GetNulls 形态的位图切片（true = NULL），只把末尾 typacl 置空。
func mkNulls(n int) []bool {
	nulls := make([]bool, n)
	nulls[n-1] = true
	return nulls
}

func TestV1217TypeTailDefaultKbV9HiddenColumn(t *testing.T) {
	raw := mustHex(t, kbV9DomDRaw)
	if len(raw) != 333 {
		t.Fatalf("fixture 长度 = %d, want 333", len(raw))
	}
	bin, def, ok := typeTailDefault(raw, 32, pgTypeLayout(17, true), 24, mkNulls(32))
	if !ok {
		t.Fatal("v1.2.18：尾部定位成功时 ok 应为 true")
	}
	if datatype.DecodeVarlenaText(def) != "7" {
		t.Fatalf("typdefault = %q, want 7（V9 尾部多一个隐藏列，k 应为 3）",
			datatype.DecodeVarlenaText(def))
	}
	if got := datatype.DecodeVarlenaText(bin); got == "" || got[0] != '{' {
		t.Errorf("typdefaultbin = %q, want 以 { 开头的节点树", got)
	}
}

func TestV1217TypeTailDefaultKbV8StandardTail(t *testing.T) {
	raw := mustHex(t, kbV8DomDRaw)
	if len(raw) != 329 {
		t.Fatalf("fixture 长度 = %d, want 329", len(raw))
	}
	bin, def, ok := typeTailDefault(raw, 32, pgTypeLayout(12, true), 24, mkNulls(31))
	if !ok {
		t.Fatal("v1.2.18：尾部定位成功时 ok 应为 true")
	}
	if datatype.DecodeVarlenaText(def) != "7" {
		t.Fatalf("typdefault = %q, want 7（V8 尾部为标准 PG 形态，k 应为 2）",
			datatype.DecodeVarlenaText(def))
	}
	if got := datatype.DecodeVarlenaText(bin); got == "" || got[0] != '{' {
		t.Errorf("typdefaultbin = %q, want 以 { 开头的节点树", got)
	}
}

// 截断元组必须整体放弃，不能吐出半截值（错误的 DEFAULT 比缺失更危险）。
func TestV1217TypeTailDefaultTruncatedGivesNothing(t *testing.T) {
	full := mustHex(t, kbV9DomDRaw)
	for _, cut := range []int{1, 2, 5} {
		raw := full[:len(full)-cut]
		bin, def, ok := typeTailDefault(raw, 32, pgTypeLayout(17, true), 24, mkNulls(32))
		if bin != nil || def != nil {
			t.Errorf("截断 %d 字节后仍返回数据（bin=%d, def=%d），应全部放弃", cut, len(bin), len(def))
		}
		// v1.2.18：截断属"定位失败"，必须 ok=false —— 上层据此登记
		// domain_default_unresolved，不能再像旧版那样静默当作"该域没有 DEFAULT"。
		if ok {
			t.Errorf("截断 %d 字节后 ok 应为 false（定位失败 ≠ 无默认值），否则静默丢失 DEFAULT", cut)
		}
	}
}

// 域无 DEFAULT：尾部三列全 NULL，应返回 nil 且不 panic。
// 这里把 fixture 从 typdefaultbin 起点截断来模拟（真实无默认值的域行即此形态）。
func TestV1217TypeTailAllNull(t *testing.T) {
	raw := mustHex(t, kbV9DomDRaw)[:180] // V9：typdefaultbin 起于 180
	nulls := make([]bool, 32)
	for i := 29; i < 32; i++ {
		nulls[i] = true
	}
	bin, def, ok := typeTailDefault(raw, 32, pgTypeLayout(17, true), 24, nulls)
	if bin != nil || def != nil {
		t.Errorf("尾部全 NULL 时不应返回数据（bin=%d, def=%d）", len(bin), len(def))
	}
	// 尾部全 NULL 是"该域确实没有 DEFAULT"的合法形态：定位**成功**（ok=true）
	// 但值是空的。这一点与截断（ok=false）必须区分开，否则每个无默认值的域
	// 都会被误报 domain_default_unresolved。
	if !ok {
		t.Error("尾部三列全 NULL（域无 DEFAULT）时 ok 应为 true —— 这是合法空值，不是定位失败")
	}
}
