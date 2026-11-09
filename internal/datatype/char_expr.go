// Author: raysuen
package datatype

// v1.2.8 修复（P2）："char"(oid 18) 的【容器载体】SQL 表达式生成。
//
// 背景：v1.2.6 只修了标量列——sqlQuote 判定 `col.TypeOID == 18` 后输出
// `(B-256)::"char"`。但同一个字节值只要装进数组元素（oid 1002）、域的基类型
// （domain over "char"）、复合类型字段（record 内 "char"），判断就失效：
// 三者导出的都是 '...' 文本字面量，裸 0xC3/0x00 在服务端 escape 层被 mb 校验
// 拒绝（实测 PG17：invalid byte sequence for encoding "UTF8": 0xc3 0x2c），
// 整表 0 行入库；且导出侧没有任何告警，用户只在回灌时才暴露。
//
// 本文件的思路：把"已解码文本"按该类型自身的文本语法重新解析成结构
// （数组树 / record 字段列表），再把含高危字节的**叶子**逐个换成
// `(B-256)::"char"`，整体重建成 ARRAY[...] / ROW(...)::type 表达式。
//
// 为什么要重新解析文本而不是直接读字节：sqlQuote 的入参只有
// (已解码值, ColumnDef)，拿不到原始 varlena；且 DecodeArray/DecodeComposite
// 只产出文本。文本由本工具的 arrayQuote/RecordQuote 生成，语法受控，
// 解析失败时一律回退原路径（此时照旧计数告警，不静默）。
//
// 只在"确实含 NUL 或 ≥0x80 字节"时改写，普通值输出与 v1.2.7 完全一致，
// 保证既有回归锚点不动。
//
// 不可表达的场景（实测确认）：自定义下界数组（'[0:1]={…}'）。PG 没有
// array_set（PG17 实测：function array_set(…) does not exist），array_fill
// 只能填同一值，文本字面量里的裸字节又过不了 escape 层 → 只能降级为原样
// 输出并告警。

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// charContainerNone：不是 char 容器（不参与改写）
const (
	charContainerNone = iota
	charContainerScalar
	charContainerArray
	charContainerComposite
)

// 类型链解析上限（v1.2.9）。
//
// v1.2.8 及以前这两类上限是散落三处的裸魔数（8 / 8 / 4），且到顶即"按当前层
// 返回、不记账"：域链 ≥9 层时 resolveCharBase 停在仍是域的中间层，char 容器
// 判定为 none → SQL 不改写、CSV/text 不告警，裸字节原样落盘（实测 9/10 层域
// 回灌报 invalid byte sequence）。现统一为具名常量，超限一律记账并告警，
// 不再有静默分支。真实实例的域链 100% 为 depth=1，32 层留足余量。
const (
	maxTypeChainHops  = 32 // typbasetype 域链最大跳数（resolveCharBase / compositeBaseOID 共用）
	maxContainerDepth = 8  // 数组元素 / 复合字段互相嵌套的最大递归深度
)

// resolveCharBase：沿 typbasetype 递归解析域，返回最终基类型 oid。
// 解析不完整（超过 maxTypeChainHops）时返回当前层——需要区分该情形的调用方
// 用 resolveCharBaseEx 取 ok。
func resolveCharBase(oid uint32) uint32 {
	base, _ := resolveCharBaseEx(oid)
	return base
}

// resolveCharBaseEx：ok=false 表示域链跳数用尽仍未到基类型（类型目录异常或
// 人为构造的超深域链），此时返回的 oid 仍是域，调用方必须按"无法判定"处理
// 并记账告警，不能当作"不含 char 容器"。
func resolveCharBaseEx(oid uint32) (uint32, bool) {
	for i := 0; i < maxTypeChainHops; i++ {
		if DynamicTypeDefs == nil {
			return oid, true
		}
		td := DynamicTypeDefs[oid]
		if td == nil || td.Type != 'd' || td.Base == 0 {
			return oid, true
		}
		oid = td.Base
	}
	return oid, false
}

// isCharArrayType：oid 是否为"元素含 char 容器的数组"（含内置 1002、
// 自定义数组类型、以及域 over 上述数组）。
// v1.2.8 补充：元素可以是复合类型（ct[]），此时同样需要逐元素重建。
func isCharArrayType(oid uint32) bool {
	elem := charArrayElemOID(oid)
	if elem == 0 {
		return false
	}
	return typeHasCharContainer(elem, 1)
}

// typeHasCharContainer：该类型内部（数组元素 / 复合字段，可嵌套）是否存在
// "char" 容器。用于把"只含 int4"的普通数组/复合类型排除在改写路径外，
// 避免无谓的文本解析。
func typeHasCharContainer(oid uint32, depth int) bool {
	return typeHasCharContainerFrom(oid, depth, oid)
}

// typeHasCharContainerFrom：root 是本次判定的顶层类型 oid，超限时按 root
// 记账（告警里要给出用户能对上 DDL 的那个类型，而不是深层中间类型）。
func typeHasCharContainerFrom(oid uint32, depth int, root uint32) bool {
	if depth > maxContainerDepth {
		// 嵌套超过上限：无法判断，按"不含"返回（保持旧行为），但必须留痕，
		// 否则这类列会静默漏改写（v1.2.9 补齐）。
		RecordTypeChainUnresolved(root, "容器嵌套")
		return false
	}
	base := resolveCharBase(oid)
	if base == CHAROID {
		return true
	}
	if elem := charArrayElemOID(base); elem != 0 {
		return typeHasCharContainerFrom(elem, depth+1, root)
	}
	if DynamicTypeDefs != nil {
		if td := DynamicTypeDefs[base]; td != nil && td.Type == 'c' && td.TypRelid != 0 {
			for _, a := range CompositeAttrs[td.TypRelid] {
				if typeHasCharContainerFrom(a.AttTypID, depth+1, root) {
					return true
				}
			}
		}
	}
	return false
}

// charArrayElemOID：数组 oid → 元素 oid（已沿域递归解析元素本身）
func charArrayElemOID(oid uint32) uint32 {
	oid = resolveCharBase(oid)
	if oid == CHARARRAYOID {
		return CHAROID
	}
	if DynamicTypeDefs == nil {
		return 0
	}
	if td := DynamicTypeDefs[oid]; td != nil && td.Type == 'b' && td.Elem != 0 {
		return resolveCharBase(td.Elem)
	}
	return 0
}

// charContainerKind：判定 oid 属于哪类 char 容器。
func charContainerKind(oid uint32) int {
	if oid == CHAROID {
		return charContainerScalar
	}
	base, resolved := resolveCharBaseEx(oid)
	if !resolved {
		// 域链超限：无法判定，不能当"无容器"静默放过（v1.2.9）。
		// 本函数被 charContainerKindCached 按 oid 缓存，故每个类型只记一次。
		RecordTypeChainUnresolved(oid, "域链")
		return charContainerNone
	}
	if base == CHAROID {
		return charContainerScalar // 域 over "char"
	}
	if isCharArrayType(base) {
		return charContainerArray
	}
	if DynamicTypeDefs != nil {
		if td := DynamicTypeDefs[base]; td != nil && td.Type == 'c' && typeHasCharContainer(base, 1) {
			return charContainerComposite
		}
	}
	return charContainerNone
}

// charContainerKindCached：charContainerKind 的读优化版本。
// sqlQuote 对【每行每列】都要判定一次，而类型目录在一次导出内固定不变，
// 直接判定会做 2~3 次 map 查找（百万行 × 十余列 = 数千万次，实测导出
// 1M 行多列大表慢约 3.5%）。这里用 copy-on-write + atomic.Value 做无锁读：
// 命中路径只有一次 atomic load + 一次 map 读，未命中才写入新副本。
var charKindStore atomic.Value // map[uint32]int

func charContainerKindCached(oid uint32) int {
	if m, ok := charKindStore.Load().(map[uint32]int); ok {
		if k, ok2 := m[oid]; ok2 {
			return k
		}
	}
	k := charContainerKind(oid)
	old, _ := charKindStore.Load().(map[uint32]int)
	m := make(map[uint32]int, len(old)+1)
	for kk, vv := range old {
		m[kk] = vv
	}
	m[oid] = k
	charKindStore.Store(m)
	return k
}

// ResetCharContainerKindCache：类型目录重建（initDynamicDecoders）或测试
// 替换 DynamicTypeDefs 后必须调用，否则会读到上一份目录的判定结果。
func ResetCharContainerKindCache() {
	charKindStore.Store(map[uint32]int{})
}

// intToCharLit：单字节 → `(B-256)::"char"`。i4tochar 直接截断低字节、
// 不做 mb 校验（接受 -128..127），是唯一能精确还原任意字节的文本表达。
func intToCharLit(b byte) string {
	i := int(b)
	if i >= 128 {
		i -= 256
	}
	return "(" + strconv.Itoa(i) + ")::\"char\""
}

// charByteRisky：该单字节在文本字面量里无法表达（NUL 或多字节编码的高位字节）
func charByteRisky(b byte) bool { return b == 0 || b >= 0x80 }

// sqlLitBytes：字节级 SQL 字符串字面量（不用 SQLStringLiteral 的 rune 遍历，
// 避免非法 UTF-8 字节被替换成 U+FFFD 而改变内容）。
func sqlLitBytes(v string) string {
	needs := false
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c == '\'' || c == '\\' || c < 32 || c == 127 {
			needs = true
			break
		}
	}
	if !needs {
		return "'" + v + "'"
	}
	var sb strings.Builder
	sb.Grow(len(v) + 8)
	sb.WriteString("E'")
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch c {
		case '\'':
			sb.WriteString("''")
		case '\\':
			sb.WriteString("\\\\")
		case '\n':
			sb.WriteString("\\n")
		case '\r':
			sb.WriteString("\\r")
		case '\t':
			sb.WriteString("\\t")
		default:
			if c < 32 || c == 127 {
				fmt.Fprintf(&sb, "\\x%02X", c)
			} else {
				sb.WriteByte(c)
			}
		}
	}
	sb.WriteString("'")
	return sb.String()
}

// ---------- 数组文本解析（PG array_in 语法） ----------

// arrNode：解析后的数组树。sub 非 nil 表示内部节点，否则是叶子。
type arrNode struct {
	sub  []*arrNode
	val  string
	null bool
}

// parseArrayLiteral：解析 "{" ... "}" 或 "[lb:ub]=…" 前缀。
// 返回 lbs（每维下界；空切片表示无显式下界）、根节点。
func parseArrayLiteral(s string) (lbs []int, root *arrNode, ok bool) {
	i := 0
	for i < len(s) && s[i] == '[' {
		j := strings.IndexByte(s[i:], ']')
		if j < 0 {
			return nil, nil, false
		}
		spec := s[i+1 : i+j]
		k := strings.IndexByte(spec, ':')
		if k < 0 {
			return nil, nil, false
		}
		lo, err := strconv.Atoi(strings.TrimSpace(spec[:k]))
		if err != nil {
			return nil, nil, false
		}
		if _, err := strconv.Atoi(strings.TrimSpace(spec[k+1:])); err != nil {
			return nil, nil, false
		}
		lbs = append(lbs, lo)
		i += j + 1
	}
	if len(lbs) > 0 {
		if i >= len(s) || s[i] != '=' {
			return nil, nil, false
		}
		i++
	}
	root, ni, ok := parseArrNode(s, i)
	if !ok || ni != len(s) {
		return nil, nil, false
	}
	return lbs, root, true
}

// parseArrNode：从 s[i]（必须是 '{'）解析一层。
func parseArrNode(s string, i int) (*arrNode, int, bool) {
	if i >= len(s) || s[i] != '{' {
		return nil, i, false
	}
	i++
	n := &arrNode{sub: []*arrNode{}}
	if i < len(s) && s[i] == '}' {
		return n, i + 1, true
	}
	for {
		if i >= len(s) {
			return nil, i, false
		}
		if s[i] == '{' {
			child, ni, ok := parseArrNode(s, i)
			if !ok {
				return nil, i, false
			}
			n.sub = append(n.sub, child)
			i = ni
		} else if s[i] == '"' {
			i++
			var sb strings.Builder
			closed := false
			for i < len(s) {
				c := s[i]
				if c == '\\' && i+1 < len(s) {
					sb.WriteByte(s[i+1])
					i += 2
					continue
				}
				if c == '"' {
					i++
					closed = true
					break
				}
				sb.WriteByte(c)
				i++
			}
			if !closed {
				return nil, i, false
			}
			n.sub = append(n.sub, &arrNode{val: sb.String()})
		} else {
			start := i
			for i < len(s) && s[i] != ',' && s[i] != '}' {
				i++
			}
			raw := strings.TrimSpace(s[start:i])
			if raw == "" {
				return nil, i, false
			}
			if strings.EqualFold(raw, "NULL") {
				n.sub = append(n.sub, &arrNode{null: true})
			} else {
				// 裸元素里的反斜杠转义（array_in 语义），本工具生成侧不会出现，
				// 但外部拼装的文本可能有，按语义还原。
				if strings.IndexByte(raw, '\\') >= 0 {
					var sb strings.Builder
					for k := 0; k < len(raw); k++ {
						if raw[k] == '\\' && k+1 < len(raw) {
							k++
						}
						sb.WriteByte(raw[k])
					}
					raw = sb.String()
				}
				n.sub = append(n.sub, &arrNode{val: raw})
			}
		}
		if i >= len(s) {
			return nil, i, false
		}
		switch s[i] {
		case ',':
			i++
		case '}':
			return n, i + 1, true
		default:
			return nil, i, false
		}
	}
}

// arrLeafWalk：遍历所有叶子（把多维树摊平，行主序）
func arrLeafWalk(n *arrNode, fn func(*arrNode) bool) bool {
	if n == nil {
		return true
	}
	if n.sub == nil {
		return fn(n)
	}
	for _, c := range n.sub {
		if !arrLeafWalk(c, fn) {
			return false
		}
	}
	return true
}

// arrDims：各维长度；形状不规则时返回 nil。
func arrDims(n *arrNode, out []int) []int {
	if n == nil || n.sub == nil {
		return out
	}
	out = append(out, len(n.sub))
	if len(n.sub) == 0 {
		return out
	}
	var child []int
	for i, c := range n.sub {
		d := arrDims(c, nil)
		if i == 0 {
			child = d
			continue
		}
		if len(d) != len(child) {
			return nil
		}
		for k := range d {
			if d[k] != child[k] {
				return nil
			}
		}
	}
	return append(out, child...)
}

// ---------- 复合文本解析（record_out 语法） ----------

// parseRecordLiteral：解析 "(f1,f2,…)"，返回字段原文（已反转义）与 NULL 标记。
// 与 RecordQuote 的生成规则对齐：空串/含 ,()"\ 或空白 → 双引号包裹，
// 内部 `\` 与 `"` 用反斜杠转义。
func parseRecordLiteral(s string) (vals []string, nulls []bool, ok bool) {
	if len(s) < 2 || s[0] != '(' || s[len(s)-1] != ')' {
		return nil, nil, false
	}
	i := 1
	end := len(s) - 1
	if i == end { // "()" —— 零字段复合类型
		return []string{}, []bool{}, true
	}
	for {
		if i > end {
			return nil, nil, false
		}
		if i == end {
			// 末尾空字段：record_out 把 NULL 写成空段，"(x,)" 的第二个字段必须是 NULL
			return append(vals, ""), append(nulls, true), true
		}
		if s[i] == '"' {
			i++
			var sb strings.Builder
			closed := false
			for i < end {
				c := s[i]
				if c == '\\' && i+1 < end {
					sb.WriteByte(s[i+1])
					i += 2
					continue
				}
				if c == '"' {
					i++
					closed = true
					break
				}
				sb.WriteByte(c)
				i++
			}
			if !closed {
				return nil, nil, false
			}
			vals = append(vals, sb.String())
			nulls = append(nulls, false)
		} else {
			start := i
			for i < end && s[i] != ',' {
				i++
			}
			raw := strings.TrimSpace(s[start:i])
			if raw == "" {
				vals = append(vals, "")
				nulls = append(nulls, true)
			} else {
				vals = append(vals, raw)
				nulls = append(nulls, false)
			}
		}
		if i >= end {
			return vals, nulls, true
		}
		if s[i] != ',' {
			return nil, nil, false
		}
		i++
	}
}

// ---------- 类型全名（cast 用） ----------

// charTypeSQLName：类型 oid → schema 限定的 SQL 名。schema 名不可得时返回
// 裸限定名（依赖 search_path），second 返回 false 供调用方决定是否降级。
func charTypeSQLName(oid uint32) (string, bool) {
	if DynamicTypeDefs == nil {
		return "", false
	}
	td := DynamicTypeDefs[oid]
	if td == nil || td.Name == "" {
		return "", false
	}
	if regNamespaceNames != nil && td.Nsp != 0 {
		if n, ok := regNamespaceNames[td.Nsp]; ok && n != "" {
			return quoteIdentLocal(n) + "." + quoteIdentLocal(td.Name), true
		}
	}
	return quoteIdentLocal(td.Name), false
}

// quoteIdentLocal：本地最小实现，避免 datatype→util 之外再引包（util 已依赖
// 本包的反向不存在，此处直接调用 util.QuoteIdent 亦可，见 util 同语义）。
func quoteIdentLocal(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// ---------- 表达式生成 ----------

// CharContainerExpr：把 char 容器的已解码文本转成可回灌的 SQL 表达式。
// ok=false 表示"不需要改写"或"无法表达"（此时调用方沿用原文本；
// 若原文含高危字节，内部已计降级告警）。
func CharContainerExpr(oid uint32, text string) (string, bool) {
	switch charContainerKindCached(oid) {
	case charContainerScalar:
		return charScalarExpr(text)
	case charContainerArray:
		return charArrayExprOf(oid, text)
	case charContainerComposite:
		return charCompositeExpr(oid, text)
	}
	return "", false
}

// charScalarExpr：标量 "char"（含域）——只有单字节且 NUL/≥0x80 才需改写。
// 域列直接赋 "char" 表达式是合法的（assignment cast + 域约束检查，实测 PG17
// `to_hex(d::int)` = ffffffc3），不需要知道域名。
func charScalarExpr(v string) (string, bool) {
	if len(v) != 1 || !charByteRisky(v[0]) {
		return "", false
	}
	return intToCharLit(v[0]), true
}

// charArrayExpr：数组（含域 over 数组、自定义 char 数组类型）。
// 无高危字节 → 不改写（保持 v1.2.7 输出，回归零影响）。
// v1.2.8 补充：元素可以是复合类型（ct[] 内含 "char" 字段），叶子按元素 oid
// 递归处理，不能一律写死 ::"char"。
func charArrayExpr(text string) (string, bool) {
	return charArrayExprOf(CHARARRAYOID, text)
}

func charArrayExprOf(oid uint32, text string) (string, bool) {
	elemOID := charArrayElemOID(oid)
	lbs, root, ok := parseArrayLiteral(text)
	if !ok || root == nil {
		if hasRiskyByte(text) {
			RecordCharContainerDegraded()
		}
		return "", false
	}
	risky := false
	arrLeafWalk(root, func(n *arrNode) bool {
		if !n.null && CharContainerRisk(elemOID, n.val) {
			risky = true
			return false
		}
		return true
	})
	if !risky {
		return "", false
	}
	// 形状不规则（各分支维长不一致）：ARRAY[...] 构造器无法表达，降级并计数。
	// 该分支只在解析器与生成器漂移/文本被外部篡改时可达（PG 存储层恒规整），
	// 但 v1.2.8 在此处是不记账的静默返回，与 CSV/text 路径（不看形状、照常
	// 计数告警）不对称，v1.2.9 补齐。
	if len(arrDims(root, nil)) == 0 {
		RecordCharContainerDegraded()
		return "", false
	}
	// 自定义下界：ARRAY[...] 构造器恒为 lb=1，PG 无 array_set（实测不存在），
	// array_fill 只能填同一值 → 无法表达，降级并告警。
	for _, lb := range lbs {
		if lb != 1 {
			RecordCharContainerDegraded()
			return "", false
		}
	}
	expr := buildArrExpr(root, true, elemOID)
	if !strings.HasPrefix(expr, "ARRAY[") {
		return "", false
	}
	return expr, true
}

// charElemExpr：数组叶子（元素）的表达式。
// 返回 ok=false 表示该元素无需改写（由调用方决定普通字面量形态）。
func charElemExpr(elemOID uint32, v string) (string, bool) {
	base := resolveCharBase(elemOID)
	switch {
	case base == CHAROID:
		return charScalarExpr(v)
	case DynamicTypeDefs != nil && DynamicTypeDefs[base] != nil && DynamicTypeDefs[base].Type == 'c':
		return charCompositeExpr(elemOID, v)
	}
	return "", false
}

// charElemFallback：元素无需改写时的普通字面量（带类型 cast，避免 ARRAY
// 构造器把 unknown 常量推断成 text）。
func charElemFallback(elemOID uint32, v string) string {
	base := resolveCharBase(elemOID)
	if base == CHAROID {
		return sqlLitBytes(v) + `::"char"`
	}
	if name, ok := charTypeSQLName(elemOID); ok {
		return sqlLitBytes(v) + "::" + name
	}
	return sqlLitBytes(v)
}

// buildArrExpr：树 → ARRAY[[a,b],[c,d]]。最外层用 ARRAY[，内层用 [；
// 叶子节点直接输出元素表达式（不加方括号，否则一维数组会变成 ARRAY[[x,y]]）。
func buildArrExpr(n *arrNode, isRoot bool, elemOID uint32) string {
	if n.sub == nil {
		if n.null {
			if base := resolveCharBase(elemOID); base == CHAROID {
				return `NULL::"char"`
			} else if name, ok := charTypeSQLName(elemOID); ok {
				return "NULL::" + name
			}
			return "NULL"
		}
		if e, ok := charElemExpr(elemOID, n.val); ok {
			return e
		}
		return charElemFallback(elemOID, n.val)
	}
	var sb strings.Builder
	if isRoot {
		sb.WriteString("ARRAY[")
	} else {
		sb.WriteByte('[')
	}
	for i, c := range n.sub {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(buildArrExpr(c, false, elemOID))
	}
	sb.WriteByte(']')
	return sb.String()
}

// charCompositeExpr：复合类型字段。
func charCompositeExpr(oid uint32, text string) (string, bool) {
	if DynamicTypeDefs == nil {
		return "", false
	}
	base := resolveCharBase(oid)
	td := DynamicTypeDefs[base]
	if td == nil || td.Type != 'c' || td.TypRelid == 0 {
		return "", false
	}
	attrs := CompositeAttrs[td.TypRelid]
	vals, nulls, ok := parseRecordLiteral(text)
	if !ok || len(attrs) == 0 || len(vals) != len(attrs) {
		if hasRiskyByte(text) {
			RecordCharContainerDegraded()
		}
		return "", false
	}
	// 域 over 复合：cast 到域全名（assignment cast 会展开到基复合 + 域约束）
	typeName, hasSchema := charTypeSQLName(oid)
	if typeName == "" {
		if hasRiskyByte(text) {
			RecordCharContainerDegraded()
		}
		return "", false
	}
	changed := false
	parts := make([]string, len(vals))
	for i := range vals {
		if nulls[i] {
			parts[i] = "NULL"
			continue
		}
		if e, ok := CharContainerExpr(attrs[i].AttTypID, vals[i]); ok {
			parts[i] = e
			changed = true
			continue
		}
		parts[i] = sqlLitBytes(vals[i])
	}
	if !changed {
		return "", false
	}
	if !hasSchema {
		// 没有 schema 名时 cast 可能落到错误类型（同名异构），按不可表达处理
		RecordCharContainerDegraded()
		return "", false
	}
	return "ROW(" + strings.Join(parts, ",") + ")::" + typeName, true
}

// hasRiskyByte：文本中是否存在无法直出的字节（NUL 或 ≥0x80）。
// 对 char 容器来说 ≥0x80 一律不可信（元素/字段本应是单字节）。
func hasRiskyByte(s string) bool {
	for i := 0; i < len(s); i++ {
		if charByteRisky(s[i]) {
			return true
		}
	}
	return false
}

// CharContainerRisk：该值在 CSV/text 格式下是否含无法转义的字节。
// CSV/text 没有任何表达式能力，只能计数告警；覆盖数组/域/复合三类载体。
func CharContainerRisk(oid uint32, text string) bool {
	switch charContainerKindCached(oid) {
	case charContainerScalar:
		return len(text) == 1 && charByteRisky(text[0])
	case charContainerArray:
		_, root, ok := parseArrayLiteral(text)
		if !ok || root == nil {
			return false
		}
		risky := false
		arrLeafWalk(root, func(n *arrNode) bool {
			if !n.null && CharContainerRisk(charArrayElemOID(oid), n.val) {
				risky = true
				return false
			}
			return true
		})
		return risky
	case charContainerComposite:
		if DynamicTypeDefs == nil {
			return false
		}
		td := DynamicTypeDefs[resolveCharBase(oid)]
		if td == nil || td.Type != 'c' || td.TypRelid == 0 {
			return false
		}
		attrs := CompositeAttrs[td.TypRelid]
		vals, nulls, ok := parseRecordLiteral(text)
		if !ok || len(attrs) == 0 || len(vals) != len(attrs) {
			return false
		}
		for i := range vals {
			if nulls[i] {
				continue
			}
			if CharContainerRisk(attrs[i].AttTypID, vals[i]) {
				return true
			}
		}
	}
	return false
}

// ---------- 降级告警（SQL 格式无法表达） ----------

var (
	charDegMu    sync.Mutex
	charDegCount int
)

// RecordCharContainerDegraded：SQL 格式遇到无法表达的 char 容器值（自定义下界
// 数组、复合类型缺 schema 名、文本语法不匹配等），计数供导出收尾告警。
func RecordCharContainerDegraded() {
	charDegMu.Lock()
	charDegCount++
	charDegMu.Unlock()
}

func takeCharContainerDegradedCount() int {
	charDegMu.Lock()
	defer charDegMu.Unlock()
	n := charDegCount
	charDegCount = 0
	return n
}

// TakeCharContainerDegradedCountForTest：跨包测试用。
func TakeCharContainerDegradedCountForTest() int {
	return takeCharContainerDegradedCount()
}

// ---------- 类型链超限告警（v1.2.9） ----------
//
// 域链（typbasetype）或容器嵌套（数组元素/复合字段）超过解析上限时，
// "是否含 char 容器"就无法判定——旧实现直接返回 false，等于告诉调用方
// "这列没有 char"，于是 SQL 侧不改写、CSV/text 侧不告警，裸字节原样落盘。
// 这里按 oid 去重记账（不随行数放大），并在导出收尾统一告警，让"判不了"
// 与"确实没有"在输出上可区分。

var (
	chainUnresMu sync.Mutex
	chainUnres   = map[uint32]string{} // oid → 原因
)

// RecordTypeChainUnresolved：类型链解析超限记账（按 oid 去重）。
func RecordTypeChainUnresolved(oid uint32, reason string) {
	chainUnresMu.Lock()
	defer chainUnresMu.Unlock()
	if chainUnres == nil {
		chainUnres = map[uint32]string{}
	}
	if _, exists := chainUnres[oid]; exists {
		return
	}
	chainUnres[oid] = reason
}

// takeTypeChainUnresolved：取走并按 oid 升序返回（自清零）。
func takeTypeChainUnresolved() []string {
	chainUnresMu.Lock()
	defer chainUnresMu.Unlock()
	if len(chainUnres) == 0 {
		return nil
	}
	oids := make([]int, 0, len(chainUnres))
	for oid := range chainUnres {
		oids = append(oids, int(oid))
	}
	sort.Ints(oids)
	out := make([]string, 0, len(oids))
	for _, o := range oids {
		reason := chainUnres[uint32(o)]
		if reason == "" {
			reason = "类型链"
		}
		out = append(out, fmt.Sprintf("%d(%s)", o, reason))
	}
	chainUnres = map[uint32]string{}
	return out
}

// TypeChainUnresolvedSummary：导出收尾汇总"类型链超限无法判定"告警；
// 无此类类型时返回 ""。只提示可能漏改写，不改变任何输出内容。
func TypeChainUnresolvedSummary() string {
	list := takeTypeChainUnresolved()
	if len(list) == 0 {
		return ""
	}
	return fmt.Sprintf("警告: 有 %d 个类型的域链或容器嵌套超过解析上限（%d/%d 层），无法判定是否含 \"char\" 容器，"+
		"相关列可能漏改写（回灌 UTF-8 等多字节编码库时对应行将被拒绝）：%s"+
		"—— 请反馈上述 oid 及类型定义（create domain / create type 语句）",
		len(list), maxTypeChainHops, maxContainerDepth, strings.Join(list, ", "))
}
