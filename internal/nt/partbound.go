// Author: raysuen
package nt

import (
	"strconv"
	"strings"
)

// DeparsePartBound：relpartbound 节点树 → "FOR VALUES ..." / "DEFAULT" 子句。
// 失败返回空串（调用方回退普通表 + 提示）。
func DeparsePartBound(raw string) string {
	p := &Parser{Src: raw}
	n := p.ParseNode()
	if p.err || n == nil || n.Kind != "PARTITIONBOUNDSPEC" {
		return ""
	}
	if n.s("is_default") == "true" {
		return "DEFAULT"
	}
	switch n.s("strategy") {
	case "h":
		mod, rem := n.i("modulus"), n.i("remainder")
		if mod <= 0 {
			return ""
		}
		return "FOR VALUES WITH (MODULUS " + strconv.Itoa(mod) + ", REMAINDER " + strconv.Itoa(rem) + ")"
	case "l":
		elems := n.subs("listdatums")
		if len(elems) == 0 {
			return ""
		}
		vals := make([]string, 0, len(elems))
		for _, e := range elems {
			d := DeparseNode(e, nil, nil, nil)
			if d == "" {
				return ""
			}
			vals = append(vals, d)
		}
		return "FOR VALUES IN (" + strings.Join(vals, ", ") + ")"
	case "r":
		lower, err := DeparseRangeDatums(n.subs("lowerdatums"))
		if err {
			return ""
		}
		upper, err := DeparseRangeDatums(n.subs("upperdatums"))
		if err {
			return ""
		}
		if lower == "" || upper == "" {
			return ""
		}
		return "FOR VALUES FROM (" + lower + ") TO (" + upper + ")"
	}
	return ""
}

// DeparseRangeDatums：PARTITIONRANGEDATUM 列表 → 逗号分隔字面量；第二返回值为失败标记。
// kind：-1=MINVALUE 0=有限值(value CONST) 1=MAXVALUE（实测映射，见文件头）。
func DeparseRangeDatums(datums []*Node) (string, bool) {
	const fail = true
	if len(datums) == 0 {
		return "", fail
	}
	out := make([]string, 0, len(datums))
	for _, d := range datums {
		if d == nil || d.Kind != "PARTITIONRANGEDATUM" {
			return "", fail
		}
		switch d.i("kind") {
		case -1:
			out = append(out, "MINVALUE")
		case 1:
			out = append(out, "MAXVALUE")
		case 0:
			// :value 可能是单节点（PARTITIONRANGEDATUM 的 value 字段为 isNode 存储）
			var cn *Node
			if v := d.sub("value"); v != nil {
				cn = v
			} else if vs := d.subs("value"); len(vs) == 1 {
				cn = vs[0]
			}
			if cn == nil {
				return "", fail
			}
			expr := DeparseNode(cn, nil, nil, nil)
			if expr == "" {
				return "", fail
			}
			out = append(out, expr)
		default:
			return "", fail // 未知 kind → 安全回退
		}
	}
	return strings.Join(out, ", "), false
}
