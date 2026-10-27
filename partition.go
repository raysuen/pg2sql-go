// partition.go：声明式分区 DDL 还原（v1.0.52）
// 数据源：
//
//	pg_partitioned_table（filenode 3350，PG12+ 与金仓 V9 同构）—— 父表分区策略与键
//	pg_class.relpartbound（named 动态布局，规避版本列偏移差异）  —— 子表边界节点树
//	pg_inherits（filenode 2611，固定布局 inhrelid@0/inhparent@4）—— 父子 OID 反查
//
// 节点树实测样本（PG17 与金仓 V9 一致）：
//
//	{PARTITIONBOUNDSPEC :strategy r :is_default false :modulus 0 :remainder 0
//	  :listdatums ({CONST ...}) :lowerdatums ({PARTITIONRANGEDATUM :kind 0 :value {CONST ...}})
//	  :upperdatums ({PARTITIONRANGEDATUM :kind 1 :value <>}) :location -1}
//	PARTITIONRANGEDATUM.kind 实测映射：-1=MINVALUE(:value <>)  0=有限值  1=MAXVALUE(:value <>)
//	（与 PG 源码枚举差 1，以两个实库抓取为准；未知 kind 安全回退）
//
// 安全网：任何解析失败 → 返回空串，调用方回退为普通表 DDL + 人工提示注释，
// 绝不输出语义错误的分区子句。
package main

import (
	"sort"
	"strconv"
	"strings"
	"sync"
)

const pgPartitionedTableRelFile = 3350

// partKeyInfo：分区父表的策略与键（pg_partitioned_table 一行）
type partKeyInfo struct {
	strat byte   // 'r'=RANGE 'l'=LIST 'h'=HASH
	cols  []int  // 分区键列号（partattrs；0 位=表达式键）
	exprs string // partexprs 原文（pg_node_tree 列表，与 0 位一一对应）；空=无表达式键
}

// partMeta：库级分区元数据（懒加载 + 每库缓存）
type partMeta struct {
	byParent map[uint32]*partKeyInfo // 父表 OID → 策略/键
	boundBy  map[uint32]string       // 子表 OID → relpartbound 节点树原文
	parentOf map[uint32]uint32       // 子表 OID → 父表 OID
}

var partMetaCache sync.Map // dbDir(string) → *partMeta

func loadPartMeta(dbDir string, version int, isKB bool) *partMeta {
	if dbDir == "" {
		return nil
	}
	if v, ok := partMetaCache.Load(dbDir); ok {
		return v.(*partMeta)
	}
	pm := &partMeta{
		byParent: map[uint32]*partKeyInfo{},
		boundBy:  map[uint32]string{},
		parentOf: map[uint32]uint32{},
	}
	// 1. pg_inherits：父子映射（固定布局，与 buildTableMeta 同一判据）
	knownInh := map[string]bool{"pg_inherits": true, "sys_inherits": true}
	if p := detectSysFile(dbDir, PG_INHERITS_RELFILE, knownInh, 0); p != "" {
		for tup := range iterSysTuples(p, version, isKB) {
			fields := extractFieldsDirect(tup.Raw, tup.THoff, tup.getNulls(), []ColLen{{4, false, "i"}, {4, false, "i"}})
			if len(fields) < 2 || fields[0] == nil || fields[1] == nil {
				continue
			}
			pm.parentOf[u32(*fields[0], 0)] = u32(*fields[1], 0)
		}
	}
	// 2. pg_partitioned_table：父表策略/键（named 布局，PG13+ 多 partdefid 列不影响前置列）
	if cols := namedTableLayout(dbDir, pgPartitionedTableRelFile, version, isKB); len(cols) > 0 {
		if p := detectSysFile(dbDir, pgPartitionedTableRelFile, map[string]bool{"pg_partitioned_table": true, "sys_partitioned_table": true}, 0); p != "" {
			for tup := range iterSysTuples(p, version, isKB) {
				f, _ := parseNamedTuple(tup, cols, false, false)
				if len(f) == 0 {
					continue
				}
				pb, ok := f["partrelid"]
				if !ok || len(pb) < 4 {
					continue
				}
				oid := u32(pb, 0)
				ki := &partKeyInfo{}
				if sb, ok := f["partstrat"]; ok && len(sb) >= 1 {
					ki.strat = sb[0]
				}
				if ab, ok := f["partattrs"]; ok && len(ab) > 0 {
					ki.cols = indexIndkey(&ab) // int2vector 与 pg_index.indkey 同构
				}
				if eb, ok := f["partexprs"]; ok && len(eb) > 0 {
					ki.exprs = string(varlenaPayloadBytes(eb))
				}
				pm.byParent[oid] = ki
			}
		}
	}
	// 3. pg_class.relpartbound：子表边界节点树（named 布局整扫一次，库级缓存）
	// 注意：PG12+ 的 pg_class oid 是 attnum=1 的普通列（行首无 oid 系统列），
	// 必须 hasOID=false，行 OID 从 f["oid"] 取（v1.0.52 实测：hasOID=true 会整体错位 4B）。
	if cols := namedTableLayout(dbDir, 1259, version, isKB); len(cols) > 0 {
		if p := detectSysFile(dbDir, 1259, map[string]bool{"pg_class": true, "sys_class": true}, 0); p != "" {
			for tup := range iterSysTuples(p, version, isKB) {
				f, _ := parseNamedTuple(tup, cols, false, false)
				oidB, ok2 := f["oid"]
				if !ok2 || len(oidB) < 4 {
					continue
				}
				rowOID := u32(oidB, 0)
				if bb, ok := f["relpartbound"]; ok && len(bb) > 0 {
					if txt := string(varlenaPayloadBytes(bb)); txt != "" {
						pm.boundBy[rowOID] = txt
					}
				}
			}
		}
	}
	v, _ := partMetaCache.LoadOrStore(dbDir, pm)
	return v.(*partMeta)
}

// deparsePartBound：relpartbound 节点树 → "FOR VALUES ..." / "DEFAULT" 子句。
// 失败返回空串（调用方回退普通表 + 提示）。
func deparsePartBound(raw string) string {
	p := &ntParser{src: raw}
	n := p.parseNode()
	if p.err || n == nil || n.kind != "PARTITIONBOUNDSPEC" {
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
			d := ntDeparse(e, nil, nil, nil)
			if d == "" {
				return ""
			}
			vals = append(vals, d)
		}
		return "FOR VALUES IN (" + strings.Join(vals, ", ") + ")"
	case "r":
		lower, err := deparseRangeDatums(n.subs("lowerdatums"))
		if err {
			return ""
		}
		upper, err := deparseRangeDatums(n.subs("upperdatums"))
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

// deparseRangeDatums：PARTITIONRANGEDATUM 列表 → 逗号分隔字面量；第二返回值为失败标记。
// kind：-1=MINVALUE 0=有限值(value CONST) 1=MAXVALUE（实测映射，见文件头）。
func deparseRangeDatums(datums []*ntNode) (string, bool) {
	const fail = true
	if len(datums) == 0 {
		return "", fail
	}
	out := make([]string, 0, len(datums))
	for _, d := range datums {
		if d == nil || d.kind != "PARTITIONRANGEDATUM" {
			return "", fail
		}
		switch d.i("kind") {
		case -1:
			out = append(out, "MINVALUE")
		case 1:
			out = append(out, "MAXVALUE")
		case 0:
			// :value 可能是单节点（PARTITIONRANGEDATUM 的 value 字段为 isNode 存储）
			var cn *ntNode
			if v := d.sub("value"); v != nil {
				cn = v
			} else if vs := d.subs("value"); len(vs) == 1 {
				cn = vs[0]
			}
			if cn == nil {
				return "", fail
			}
			expr := ntDeparse(cn, nil, nil, nil)
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

// partitionByClause：父表 "PARTITION BY <STRAT> (key)" 子句。
// 列名经 nameByCol 映射；表达式键位（col==0）依次取 exprs 反解结果。
// 任一键位无法还原 → 返回空串（调用方回退 + 提示）。
func partitionByClause(ki *partKeyInfo, exprs []string, nameByCol map[int]string) string {
	if ki == nil || ki.strat == 0 {
		return ""
	}
	word := map[byte]string{'r': "RANGE", 'l': "LIST", 'h': "HASH"}[ki.strat]
	if word == "" {
		return ""
	}
	if len(ki.cols) == 0 {
		return ""
	}
	exprIdx := 0
	parts := make([]string, 0, len(ki.cols))
	for _, c := range ki.cols {
		if c > 0 {
			if nm, ok := nameByCol[c]; ok && nm != "" {
				parts = append(parts, quoteName(nm))
				continue
			}
			return ""
		}
		// 表达式键位（deparse 结果通常自带一层括号，避免双重括号）
		if exprIdx >= len(exprs) || exprs[exprIdx] == "" {
			return ""
		}
		e := exprs[exprIdx]
		if !(strings.HasPrefix(e, "(") && strings.HasSuffix(e, ")")) {
			e = "(" + e + ")"
		}
		parts = append(parts, e)
		exprIdx++
	}
	return "PARTITION BY " + word + " (" + strings.Join(parts, ", ") + ")"
}

// deparsePartExprs：partexprs 节点树列表 → 表达式数组（varAt 引用表列名）。
func deparsePartExprs(raw string, dbDir string, version int, isKB bool, nameByCol map[int]string) []string {
	if raw == "" {
		return nil
	}
	varAt := func(pos int) string {
		if nm, ok := nameByCol[pos]; ok {
			return quoteName(nm)
		}
		return ""
	}
	return deparseNodeTreeList(raw, buildOperatorMap(dbDir, version, isKB), buildProcNameMap(dbDir, version, isKB), varAt)
}

// ---------- TableMeta 分区辅助 ----------

// partitionByClauseSelf：TableMeta → "PARTITION BY <STRAT> (key)"；失败返回空串。
func (tm *TableMeta) partitionByClauseSelf() string {
	if tm.PartStrat == 0 || len(tm.PartKeyCols) == 0 {
		return ""
	}
	nameByCol := map[int]string{}
	for _, c := range tm.Columns {
		nameByCol[c.AttNum] = c.Name
	}
	ki := &partKeyInfo{strat: tm.PartStrat, cols: tm.PartKeyCols}
	return partitionByClause(ki, tm.PartExprs, nameByCol)
}

// pkCoversPartKey：主键是否覆盖全部分区键列（PG 对分区父表的硬约束）。
// 表达式键位（col==0）无法按名比较，跳过。
func (tm *TableMeta) pkCoversPartKey() bool {
	pk := map[string]bool{}
	for _, p := range tm.PrimaryKey {
		pk[p] = true
	}
	nameByCol := map[int]string{}
	for _, c := range tm.Columns {
		nameByCol[c.AttNum] = c.Name
	}
	for _, k := range tm.PartKeyCols {
		if k == 0 {
			continue
		}
		if nm, ok := nameByCol[k]; ok && !pk[nm] {
			return false
		}
	}
	return true
}

// sortTablesPartitionFirst：按分区链深度稳定排序（父先于子）。
// 非分区表与不在任务集内的父链视为深度 0，保持原相对顺序。
func sortTablesPartitionFirst(tasks []*TableMeta) {
	if len(tasks) < 2 {
		return
	}
	byOID := map[uint32]*TableMeta{}
	for _, t := range tasks {
		byOID[t.OID] = t
	}
	depth := map[uint32]int{}
	var calc func(t *TableMeta) int
	calc = func(t *TableMeta) int {
		if t == nil || t.PartParent == 0 {
			return 0
		}
		if d, ok := depth[t.OID]; ok {
			return d
		}
		depth[t.OID] = 0 // 环防护
		d := 0
		if p, ok := byOID[t.PartParent]; ok {
			d = calc(p) + 1
		}
		depth[t.OID] = d
		return d
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		return calc(tasks[i]) < calc(tasks[j])
	})
}
