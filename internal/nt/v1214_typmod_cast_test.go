package nt

// v1.2.14 单测：typmod 强转函数（T(value, typmod[, isExplicit])）的默认值反解。
//
// 背景：PG 对"unknown 字面量 → 带 typmod 的类型"的强转，adbin 里存的是同名
// 函数调用而非裸 CONST。旧代码只处理单参转型，三参形态掉进通用 name(a,b,c)
// 分支，输出 `SET DEFAULT varchar('dflt', 14, FALSE)` 这类非法 SQL。
// 该缺陷在 PG 17.6 原生与金仓 pg/mysql/oracle 三种兼容模式下均可复现。
//
// 夹具中的 FUNCEXPR 形态取自实机 adbin（varchar(10) DEFAULT 'dflt' 等），
// 函数名经 procMap 注入，与生产路径一致。

import "testing"

func v1214Maps() (map[uint32]OpInfo, map[uint32]string) {
	return map[uint32]OpInfo{}, map[uint32]string{
		156:  "varchar", // pg_catalog.varchar(character varying, integer, boolean)
		669:  "bpchar",  // pg_catalog.bpchar(character, integer, boolean)
		1234: "substr",  // 对照：真正的三参函数，不得被改写
	}
}

// text 常量 'dflt'：constvalue 首 4 字节为 varlena 头，存的是"总长 << 2"
// （总长 8 → 头 32；节点树里 varlena 头不做大小端转换），其后为内容字节
const v1214TextDflt = `{CONST :consttype 25 :consttypmod -1 :constcollid 100 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 8 [ 32 0 0 0 100 102 108 116 ]}`

// int4 常量 typmod=14（varchar(10)：14 = 10 + VARHDRSZ）
const v1214Typmod14 = `{CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 14 0 0 0 0 0 0 0 ]}`

// int4 常量 typmod=8（bpchar(4)：8 = 4 + VARHDRSZ）
const v1214Typmod8 = `{CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 8 0 0 0 0 0 0 0 ]}`

// bool 常量 FALSE（isExplicit）
const v1214BoolFalse = `{CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 0 0 0 0 ]}`

// int4 常量 2（对照：普通三参函数的第二个实参）
const v1214Int2 = `{CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 2 0 0 0 0 0 0 0 ]}`

func TestV1214TypmodCast(t *testing.T) {
	opMap, procMap := v1214Maps()
	cases := []struct {
		name string
		tree string
		want string
	}{
		{
			"varchar(10) DEFAULT 'dflt'（三参 typmod 强转）",
			`{FUNCEXPR :funcid 156 :funcresulttype 1043 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args (` +
				v1214TextDflt + ` ` + v1214Typmod14 + ` ` + v1214BoolFalse + `) :location -1}`,
			`CAST('dflt' AS varchar(10))`,
		},
		{
			"bpchar(4) DEFAULT 'ab'（三参 typmod 强转）",
			`{FUNCEXPR :funcid 669 :funcresulttype 1042 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args (` +
				v1214TextDflt + ` ` + v1214Typmod8 + ` ` + v1214BoolFalse + `) :location -1}`,
			`CAST('dflt' AS bpchar(4))`,
		},
		{
			"普通三参函数不得被改写",
			`{FUNCEXPR :funcid 1234 :funcresulttype 25 :funcretset false :funcvariadic false :funcformat 0 :funccollid 0 :inputcollid 0 :args (` +
				v1214TextDflt + ` ` + v1214Int2 + ` ` + v1214Typmod14 + `) :location -1}`,
			`substr('dflt', 2, 14)`,
		},
	}
	for _, c := range cases {
		if got := DeparseNodeTree(c.tree, opMap, procMap, nil); got != c.want {
			t.Errorf("%s:\n  得到 %q\n  期望 %q", c.name, got, c.want)
		}
	}
}

// TestV1214TypmodSpec 直接校验 typmod 解码规则（含两个实参的 numeric 形态）。
func TestV1214TypmodSpec(t *testing.T) {
	cases := []struct {
		name  string
		typ   string
		parts []string
		want  string
		ok    bool
	}{
		{"varchar(10)", "varchar", []string{"'dflt'", "14", "FALSE"}, "CAST('dflt' AS varchar(10))", true},
		{"bpchar(4)", "bpchar", []string{"'ab'", "8", "FALSE"}, "CAST('ab' AS bpchar(4))", true},
		{"numeric(6,2)", "numeric", []string{"1.5", "393222"}, "CAST(1.5 AS numeric(6,2))", true},
		{"numeric(6)", "numeric", []string{"1", "393220"}, "CAST(1 AS numeric(6))", true},
		{"typmod=-1 退化", "varchar", []string{"'x'", "-1", "FALSE"}, "CAST('x' AS varchar)", true},
		{"timestamp(3) 只保类型", "timestamp", []string{"'2024-01-01 00:00:00'", "3"}, "CAST('2024-01-01 00:00:00' AS timestamp)", true},
		{"非 typmod 类型不接管", "text", []string{"'x'", "3"}, "", false},
		{"第二参非整数不接管", "varchar", []string{"'x'", "VALUE"}, "", false},
		{"第三参非布尔不接管", "varchar", []string{"'x'", "14", "VALUE"}, "", false},
	}
	for _, c := range cases {
		got, ok := ntTypmodCastSpec(c.typ, c.parts)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: 得到 (%q, %v)，期望 (%q, %v)", c.name, got, ok, c.want, c.ok)
		}
	}
}
