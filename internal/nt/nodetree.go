package nt

// pg_node_tree 通用反解模块（v1.0.51，自 domain_check.go 泛化）
//
// PostgreSQL 目录中的表达式类对象（默认值/索引表达式/分区边界/视图/域 CHECK 等）
// 均以序列化解析树文本（pg_node_tree）存储。本文件提供与具体目录无关的通用层：
//
//	L0 解析器：ntParser / ntNode / ntVal —— {NODE :attr value :sub ({...})} 递归下降
//	L1 反解器：DeparseNodeTree / ntDeparse —— 节点 → SQL 表达式
//	L2 名字解析：buildOperatorMap（pg_operator 算符符号）+ 调用方传入 procMap（pg_proc）
//	L3 安全网：未知节点返回空串，调用方回退为人工提示注释，绝不输出错误 SQL
//
// VAR 语义由调用方通过 varAt 钩子决定：
//   - 域 CHECK 上下文：varAt=nil，VAR/COERCETODOMAINVALUE 统一渲染为 VALUE
//   - 表达式索引等上下文：varAt(attno) 返回限定列名；返回空串表示无法解析 → 整体不可还原
//
// 支持的节点：OPEXPR / SCALARARRAYOPEXPR(+ARRAYEXPR/ARRAY/常量数组) / FUNCEXPR / CONST /
// VAR / COERCETODOMAINVALUE / BOOLEXPR / NULLTEST / RELABELTYPE / COERCEVIAIO / FUNCCOERCE。
// 常量类型：bool/int2/int4/int8/float4/float8/numeric/text/varchar/bpchar/name/"char"/
// date/time/timestamp/timestamptz（时间类按 UTC 基准渲染，为已知限制）。

import (
	"encoding/hex"
	"fmt"
	"math"
	"pg2sql/internal/datatype"
	"strconv"
	"strings"
	"time"
)

// ---------- L2：算符映射 ----------

// OpInfo：算符符号与结合形式（oprkind：b=中缀 l=前缀 r=后缀）
type OpInfo struct {
	Name string
	Kind byte
}

// ---------- L0：节点树文本解析 ----------

type ntVal struct {
	node   *Node
	list   []*ntVal
	str    string // 标量（去引号、已解转义）
	isNode bool
	isList bool
}

type Node struct {
	Kind  string
	Attrs map[string]*ntVal
}

func (n *Node) s(name string) string {
	if n == nil {
		return ""
	}
	if v, ok := n.Attrs[name]; ok && !v.isNode && !v.isList {
		return v.str
	}
	return ""
}

func (n *Node) i(name string) int {
	s := n.s(name)
	if s == "" {
		return 0
	}
	v, err := strconv.Atoi(strings.TrimPrefix(s, "+"))
	if err != nil {
		return 0
	}
	return v
}

func (n *Node) sub(name string) *Node {
	if n == nil {
		return nil
	}
	if v, ok := n.Attrs[name]; ok && v.isNode {
		return v.node
	}
	return nil
}

func (n *Node) subs(name string) []*Node {
	if n == nil {
		return nil
	}
	if v, ok := n.Attrs[name]; ok && v.isList {
		out := make([]*Node, 0, len(v.list))
		for _, e := range v.list {
			if e.isNode {
				out = append(out, e.node)
			}
		}
		return out
	}
	return nil
}

type Parser struct {
	Src string
	pos int
	err bool
}

func (p *Parser) skipWS() {
	for p.pos < len(p.Src) && (p.Src[p.pos] == ' ' || p.Src[p.pos] == '\n' || p.Src[p.pos] == '\t') {
		p.pos++
	}
}

func (p *Parser) ParseNode() *Node {
	p.skipWS()
	if p.err || p.pos >= len(p.Src) || p.Src[p.pos] != '{' {
		p.err = true
		return nil
	}
	p.pos++
	p.skipWS()
	start := p.pos
	for p.pos < len(p.Src) && p.Src[p.pos] != ' ' && p.Src[p.pos] != '}' {
		p.pos++
	}
	n := &Node{Kind: p.Src[start:p.pos], Attrs: map[string]*ntVal{}}
	for {
		p.skipWS()
		if p.err || p.pos >= len(p.Src) {
			p.err = true
			return nil
		}
		if p.Src[p.pos] == '}' {
			p.pos++
			return n
		}
		if p.Src[p.pos] != ':' {
			// 容错：非属性 token（异常格式），跳过
			before := p.pos
			p.skipToken()
			// v1.0.56 修复（P3-G13）：skipToken 遇 ) } ( { 等终止字符时不前进，
			// 畸形输入下 continue 原地打转 → 无限循环挂死。未前进时强制消费 1 字符。
			if p.pos == before {
				p.pos++
			}
			continue
		}
		p.pos++
		start := p.pos
		for p.pos < len(p.Src) && p.Src[p.pos] != ' ' {
			p.pos++
		}
		attr := p.Src[start:p.pos]
		v := p.parseValue()
		if p.err {
			return nil
		}
		n.Attrs[attr] = v
	}
}

func (p *Parser) skipToken() {
	for p.pos < len(p.Src) && p.Src[p.pos] != ' ' && p.Src[p.pos] != ')' &&
		p.Src[p.pos] != '}' && p.Src[p.pos] != '(' && p.Src[p.pos] != '{' {
		p.pos++
	}
}

func (p *Parser) parseValue() *ntVal {
	p.skipWS()
	if p.err || p.pos >= len(p.Src) {
		p.err = true
		return nil
	}
	switch p.Src[p.pos] {
	case '{':
		n := p.ParseNode()
		if p.err {
			return nil
		}
		return &ntVal{node: n, isNode: true}
	case '(':
		p.pos++
		list := []*ntVal{}
		for {
			p.skipWS()
			if p.err || p.pos >= len(p.Src) {
				p.err = true
				return nil
			}
			if p.Src[p.pos] == ')' {
				p.pos++
				break
			}
			if p.Src[p.pos] == '(' {
				// 嵌套裸列表（少见）→ 原样捕获
				depth := 0
				start := p.pos
				for p.pos < len(p.Src) {
					if p.Src[p.pos] == '(' {
						depth++
					} else if p.Src[p.pos] == ')' {
						depth--
						if depth == 0 {
							p.pos++
							break
						}
					}
					p.pos++
				}
				list = append(list, &ntVal{str: p.Src[start:p.pos]})
				continue
			}
			v := p.parseValue()
			if p.err {
				return nil
			}
			list = append(list, v)
		}
		return &ntVal{list: list, isList: true}
	case '<':
		if p.pos+1 < len(p.Src) && p.Src[p.pos+1] == '>' {
			p.pos += 2
			return &ntVal{str: ""}
		}
		p.err = true
		return nil
	case '"':
		p.pos++
		var sb strings.Builder
		for p.pos < len(p.Src) {
			c := p.Src[p.pos]
			if c == '\\' && p.pos+1 < len(p.Src) {
				sb.WriteByte(p.Src[p.pos+1])
				p.pos += 2
				continue
			}
			if c == '"' {
				p.pos++
				break
			}
			sb.WriteByte(c)
			p.pos++
		}
		return &ntVal{str: sb.String()}
	default:
		start := p.pos
		p.skipToken()
		tok := p.Src[start:p.pos]
		// constvalue 的 "<len> [ b b ... ]" 形式：裸 token 后跟 [...] 字节列表，
		// 一并吞入（节点树中裸 token 后紧跟 '[' 仅此一处）
		save := p.pos
		p.skipWS()
		if p.pos < len(p.Src) && p.Src[p.pos] == '[' {
			depth := 0
			for p.pos < len(p.Src) {
				if p.Src[p.pos] == '[' {
					depth++
				} else if p.Src[p.pos] == ']' {
					depth--
					if depth == 0 {
						p.pos++
						break
					}
				}
				p.pos++
			}
			tok = p.Src[start:p.pos]
		} else {
			p.pos = save
		}
		return &ntVal{str: tok}
	}
}

// ---------- L1：反解 ----------

// DeparseNodeTree：pg_node_tree 文本 → SQL 表达式；失败返回空串。
// varAt 语义见文件头注释；nil 时 VAR 渲染为 VALUE（域上下文）。
func DeparseNodeTree(s string, opMap map[uint32]OpInfo, procMap map[uint32]string, varAt func(int) string) string {
	p := &Parser{Src: s}
	n := p.ParseNode()
	if p.err || n == nil {
		return ""
	}
	expr := DeparseNode(n, opMap, procMap, varAt)
	if strings.TrimSpace(expr) == "" {
		return ""
	}
	return expr
}

// DeparseNodeTreeList：pg_node_tree List（indexprs 等表达式列表）→ 逐项反解。
// 兼容两种序列化形态：PG13+ 的 `{NODE} {NODE} ...` 与旧格式的 `({NODE} {NODE})`。
// 任一项反解失败整体返回 nil（调用方回退人工提示）。
func DeparseNodeTreeList(s string, opMap map[uint32]OpInfo, procMap map[uint32]string, varAt func(int) string) []string {
	p := &Parser{Src: s}
	var out []string
	for {
		p.skipWS()
		if p.err || p.pos >= len(p.Src) {
			break
		}
		switch p.Src[p.pos] {
		case '(': // 旧格式外层列表括号
			p.pos++
			continue
		case ')':
			p.pos++
			continue
		case '{':
			n := p.ParseNode()
			if p.err || n == nil {
				return nil
			}
			d := DeparseNode(n, opMap, procMap, varAt)
			if d == "" {
				return nil
			}
			out = append(out, d)
		default:
			before := p.pos
			p.skipToken()
			// v1.0.56 修复（P3-G13）：skipToken 不前进时强制消费，防原地打转死循环
			if p.pos == before {
				p.pos++
			}
			if p.pos >= len(p.Src) {
				return nil
			}
		}
	}
	return out
}

// ntBuiltinCastTypes：单参数同名转型函数（如 numeric(integer)）→ CAST 形式
var ntBuiltinCastTypes = map[string]bool{
	"bool": true, "bytea": true, "char": true, "name": true, "int8": true,
	"int2": true, "int4": true, "text": true, "oid": true, "float4": true,
	"float8": true, "numeric": true, "date": true, "time": true,
	"timestamp": true, "timestamptz": true, "interval": true, "varchar": true,
	"bpchar": true, "uuid": true, "json": true, "jsonb": true, "money": true,
	"regclass": true, "xml": true, "macaddr": true, "inet": true, "cidr": true,
}

// ntNumericCastTypes：数值族转型目标——纯数字字面量可直接透传（SQL 隐式收敛）
var ntNumericCastTypes = map[string]bool{
	"int2": true, "int4": true, "int8": true,
	"float4": true, "float8": true, "numeric": true,
}

func DeparseNode(n *Node, opMap map[uint32]OpInfo, procMap map[uint32]string, varAt func(int) string) string {
	if n == nil {
		return ""
	}
	switch n.Kind {
	case "VAR":
		if varAt != nil {
			// 表达式上下文：attno → 列名；解析失败整体不可还原
			return varAt(n.i("varattno"))
		}
		return "VALUE" // 域上下文：VAR 即域值

	case "COERCETODOMAINVALUE":
		return "VALUE"

	case "OPEXPR":
		args := n.subs("args")
		oi, ok := opMap[uint32(n.i("opno"))]
		if !ok || oi.Name == "" {
			return ""
		}
		if oi.Kind == 'b' && len(args) == 2 {
			l, r := DeparseNode(args[0], opMap, procMap, varAt), DeparseNode(args[1], opMap, procMap, varAt)
			if l == "" || r == "" {
				return ""
			}
			return l + " " + oi.Name + " " + r
		}
		if len(args) == 1 {
			a := DeparseNode(args[0], opMap, procMap, varAt)
			if a == "" {
				return ""
			}
			if oi.Kind == 'l' { // 前缀
				return oi.Name + " " + a
			}
			if oi.Kind == 'r' { // 后缀
				return a + " " + oi.Name
			}
			return oi.Name + "(" + a + ")"
		}
		return ""

	case "SCALARARRAYOPEXPR":
		args := n.subs("args")
		if len(args) != 2 {
			return ""
		}
		lhs := DeparseNode(args[0], opMap, procMap, varAt)
		if lhs == "" {
			return ""
		}
		elems := ntArrayElemsNode(args[1], opMap, procMap, varAt)
		if len(elems) == 0 {
			return ""
		}
		op := opMap[uint32(n.i("opno"))]
		useOr := n.s("useOr") == "true"
		list := strings.Join(elems, ", ")
		switch {
		case op.Name == "=" && useOr:
			return lhs + " IN (" + list + ")"
		case op.Name == "<>" && !useOr:
			return lhs + " NOT IN (" + list + ")"
		case op.Name == "":
			return ""
		case useOr:
			return lhs + " " + op.Name + " ANY (ARRAY[" + list + "])"
		default:
			return lhs + " " + op.Name + " ALL (ARRAY[" + list + "])"
		}

	case "FUNCEXPR":
		fid := uint32(n.i("funcid"))
		name := procMap[fid]
		args := n.subs("args")
		if name == "" {
			return ""
		}
		var parts []string
		for _, a := range args {
			d := DeparseNode(a, opMap, procMap, varAt)
			if d == "" {
				return ""
			}
			parts = append(parts, d)
		}
		if len(parts) == 1 && ntBuiltinCastTypes[name] {
			inner := parts[0]
			// 纯数字字面量经数值族单参转型 → 直接字面量
			// （VALUE >= CAST(0 AS numeric) → VALUE >= 0；2.5::float8 → 2.5）
			if isPlainNumber(inner) && ntNumericCastTypes[name] {
				return inner
			}
			return "CAST(" + inner + " AS " + name + ")"
		}
		return name + "(" + strings.Join(parts, ", ") + ")"

	case "CONST":
		return ntConstDeparse(n)

	case "BOOLEXPR":
		args := n.subs("args")
		bo := n.s("boolop")
		var parts []string
		for _, a := range args {
			d := DeparseNode(a, opMap, procMap, varAt)
			if d == "" {
				return ""
			}
			parts = append(parts, d)
		}
		switch bo {
		case "and", "0": // AND_EXPR
			if len(parts) < 2 {
				return ""
			}
			return "(" + strings.Join(parts, " AND ") + ")"
		case "or", "1": // OR_EXPR
			if len(parts) < 2 {
				return ""
			}
			return "(" + strings.Join(parts, " OR ") + ")"
		case "not", "2": // NOT_EXPR
			if len(parts) != 1 {
				return ""
			}
			return "NOT (" + parts[0] + ")"
		}
		return ""

	case "NULLTEST":
		a := n.sub("arg")
		if a == nil {
			return ""
		}
		inner := DeparseNode(a, opMap, procMap, varAt)
		if inner == "" {
			return ""
		}
		if strings.Contains(n.s("nulltesttype"), "not") || n.s("nulltesttype") == "1" {
			return inner + " IS NOT NULL"
		}
		return inner + " IS NULL"

	case "RELABELTYPE", "COERCEVIAIO":
		// 二进制兼容/IO 隐式转型，SQL 文本不可见 → 透传
		if a := n.sub("arg"); a != nil {
			return DeparseNode(a, opMap, procMap, varAt)
		}
		return ""

	case "FUNCCOERCE":
		// 函数式二进制兼容转型：透传入参
		if args := n.subs("args"); len(args) >= 1 {
			return DeparseNode(args[0], opMap, procMap, varAt)
		}
		return ""
	}
	return "" // 未知节点 → 不可还原
}

// ntArrayElemsNode：IN 列表右侧（ARRAYEXPR/ARRAY 或数组类型 CONST）→ 字面量列表
func ntArrayElemsNode(v *Node, opMap map[uint32]OpInfo, procMap map[uint32]string, varAt func(int) string) []string {
	if v == nil {
		return nil
	}
	switch v.Kind {
	case "ARRAYEXPR", "ARRAY": // PG16+ / PG15-（金仓 V9 内核）的数组构造节点
		var out []string
		for _, e := range v.subs("elements") {
			d := DeparseNode(e, opMap, procMap, varAt)
			if d == "" {
				return nil
			}
			out = append(out, d)
		}
		return out
	case "CONST":
		return ntArrayDatumElems(v)
	}
	return nil
}

// ntArrayDatumElems：一维数组常量 datum → 元素字面量（不含 NULL 位图的数组）
func ntArrayDatumElems(n *Node) []string {
	raw := ntConstBytes(n)
	if len(raw) < 20 {
		return nil
	}
	ndim := int(int32(datatype.U32(raw, 0)))
	dataOff := int(int32(datatype.U32(raw, 4)))
	if ndim != 1 || dataOff != 0 { // 含 NULL 位图或多维 → 不支持
		return nil
	}
	elemType := datatype.U32(raw, 8)
	dim := int(int32(datatype.U32(raw, 12)))
	if dim < 0 || dim > 10000 {
		return nil
	}
	pos := 20
	var out []string
	for i := 0; i < dim; i++ {
		b, next, ok := ntArrayElemBytes(raw, pos, elemType)
		if !ok {
			return nil
		}
		lit := ntLiteral(elemType, b)
		if lit == "" {
			return nil
		}
		out = append(out, lit)
		pos = next
	}
	return out
}

// ntArrayElemBytes：从数组 datum 中按元素类型取对齐后的元素字节
func ntArrayElemBytes(b []byte, pos int, elemType uint32) ([]byte, int, bool) {
	align, size := ntElemLayout(elemType)
	if align > 1 {
		pos = (pos + align - 1) &^ (align - 1)
	}
	var payload []byte
	if size > 0 { // 定长
		if pos+size > len(b) {
			return nil, pos, false
		}
		payload = b[pos : pos+size]
		pos += size
	} else { // varlena
		kind, total, _, _ := datatype.VarlenaParse(b, pos)
		if kind == "" || pos+total > len(b) {
			return nil, pos, false
		}
		payload = b[pos : pos+total]
		pos += total
	}
	return payload, pos, true
}

// ntElemLayout：元素类型的 (对齐, 定长字节数；0=varlena)
func ntElemLayout(t uint32) (int, int) {
	switch t {
	case 21: // int2
		return 2, 2
	case 23, 700, 1082: // int4, float4, date
		return 4, 4
	case 20, 701, 1114, 1184, 1083: // int8, float8, timestamp, timestamptz, time
		return 8, 8
	case 16: // bool
		return 1, 1
	default: // text/varchar/numeric 等按 varlena，4B 对齐
		return 4, 0
	}
}

// ntConstBytes：CONST 节点的 constvalue 标量 → 原始 datum 字节。
// 节点树格式：<len> [ b b ... ]（十进制字节）或 \x<十六进制>。
func ntConstBytes(n *Node) []byte {
	tok := n.s("constvalue")
	if tok == "" {
		return nil
	}
	if strings.HasPrefix(tok, "\\x") {
		b, err := hex.DecodeString(tok[2:])
		if err != nil {
			return nil
		}
		return b
	}
	// "<len> [ b b b ... ]"
	lt := strings.IndexByte(tok, '[')
	gt := strings.IndexByte(tok, ']')
	if lt < 0 || gt < lt {
		return nil
	}
	fields := strings.Fields(tok[lt+1 : gt])
	out := make([]byte, 0, len(fields))
	for _, fs := range fields {
		// nodeToString 把 >127 的字节输出为有符号负数（如 -1/-128），转回无符号
		v, err := strconv.ParseInt(fs, 10, 32)
		if err != nil || v < -128 || v > 255 {
			return nil
		}
		out = append(out, byte(v&0xFF))
	}
	return out
}

// ntConstDeparse：CONST 节点 → 字面量
func ntConstDeparse(n *Node) string {
	if n.s("constisnull") == "true" {
		return "NULL"
	}
	ct := uint32(n.i("consttype"))
	byval := n.s("constbyval") == "true"
	raw := ntConstBytes(n)
	if raw == nil {
		return ""
	}
	if byval {
		// constvalue 可能是 8B Datum 宽度，定长类型取前 |typlen| 字节
		if cl := n.i("constlen"); cl > 0 && len(raw) > cl {
			raw = raw[:cl]
		}
	} else if _, ext, _ := datatype.VarPayload(raw); ext {
		return "" // 外联 datum 不支持（防御）
	}
	return ntLiteral(ct, raw)
}

// ntLiteral：按类型 OID 把 datum 字节渲染为 SQL 字面量
func ntLiteral(t uint32, raw []byte) string {
	switch t {
	case 16: // bool
		if len(raw) >= 1 && raw[0] != 0 {
			return "TRUE"
		}
		return "FALSE"
	case 21: // int2
		if len(raw) < 2 {
			return ""
		}
		return strconv.Itoa(int(int16(datatype.U16(raw, 0))))
	case 23: // int4
		if len(raw) < 4 {
			return ""
		}
		return strconv.Itoa(int(int32(datatype.U32(raw, 0))))
	case 20: // int8
		if len(raw) < 8 {
			return ""
		}
		return strconv.FormatInt(int64(binary_U64LE(raw)), 10)
	case 700: // float4
		if len(raw) < 4 {
			return ""
		}
		return strconv.FormatFloat(float64(math.Float32frombits(datatype.U32(raw, 0))), 'g', -1, 32)
	case 701: // float8
		if len(raw) < 8 {
			return ""
		}
		return strconv.FormatFloat(math.Float64frombits(binary_U64LE(raw)), 'g', -1, 64)
	case 1700: // numeric（varlena datum）
		return datatype.DecodeNumeric(raw)
	case 25, 1043, 1042, 19: // text, varchar, bpchar, name
		payload, _, _ := datatype.VarPayload(raw)
		return QuoteSQLLiteral(datatype.CString(payload, 0))
	case 18: // "char"
		if len(raw) < 1 {
			return ""
		}
		return QuoteSQLLiteral(string(raw[:1]))
	case 1082: // date：int32 天数自 2000-01-01
		if len(raw) < 4 {
			return ""
		}
		days := int(int32(datatype.U32(raw, 0)))
		d := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, days)
		return QuoteSQLLiteral(d.Format("2006-01-02"))
	case 1083: // time：int64 微秒自 00:00:00
		if len(raw) < 8 {
			return ""
		}
		us := int64(binary_U64LE(raw))
		return QuoteSQLLiteral(formatTimeOfDay(us))
	case 1114, 1184: // timestamp / timestamptz：int64 微秒自 2000-01-01（UTC 基准）
		if len(raw) < 8 {
			return ""
		}
		us := int64(binary_U64LE(raw))
		// v1.0.56 修复（P3-G14）：us*1000 在年份 ≳2292 时 int64 溢出
		//（PG 时间戳上限 294276 年），改秒/微秒拆分传入 time.Unix
		ts := time.Unix(946684800+us/1000000, (us%1000000)*1000).UTC()
		s := ts.Format("2006-01-02 15:04:05")
		if frac := ts.Nanosecond(); frac != 0 {
			s += fmt.Sprintf(".%06d", frac/1000)
		}
		// v1.0.52：timestamptz 字面量必须显式 +00——无时区后缀的裸字面量
		// 回灌时按会话时区解释，分区边界/约束比较会随会话时区漂移
		if t == 1184 {
			s += "+00"
		}
		return QuoteSQLLiteral(s)
	}
	return ""
}

// ---------- 辅助 ----------

func binary_U64LE(b []byte) uint64 {
	if len(b) < 8 {
		return 0
	}
	return uint64(b[0]) | uint64(b[1])<<8 | uint64(b[2])<<16 | uint64(b[3])<<24 |
		uint64(b[4])<<32 | uint64(b[5])<<40 | uint64(b[6])<<48 | uint64(b[7])<<56
}

func formatTimeOfDay(us int64) string {
	if us < 0 {
		us = 0
	}
	sec := us / 1e6
	frac := us % 1e6
	h, m, s := sec/3600%24, sec/60%60, sec%60
	if frac != 0 {
		return fmt.Sprintf("%02d:%02d:%02d.%06d", h, m, s, frac)
	}
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}

func isPlainNumber(s string) bool {
	if s == "" {
		return false
	}
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

func QuoteSQLLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func QuoteSQLIdent(name string) string {
	return "\"" + strings.ReplaceAll(name, "\"", "\"\"") + "\""
}
