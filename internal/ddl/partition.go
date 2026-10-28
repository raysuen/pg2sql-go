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
package ddl

import (
	"pg2sql/internal/catalog"
	"pg2sql/internal/engine"
	"sort"
	"strings"
)

const pgPartitionedTableRelFile = 3350

// PartitionByClause：父表 "PARTITION BY <STRAT> (key)" 子句。
// 列名经 nameByCol 映射；表达式键位（col==0）依次取 exprs 反解结果。
// 任一键位无法还原 → 返回空串（调用方回退 + 提示）。
func PartitionByClause(ki *catalog.PartKeyInfo, exprs []string, nameByCol map[int]string) string {
	if ki == nil || ki.Strat == 0 {
		return ""
	}
	word := map[byte]string{'r': "RANGE", 'l': "LIST", 'h': "HASH"}[ki.Strat]
	if word == "" {
		return ""
	}
	if len(ki.Cols) == 0 {
		return ""
	}
	exprIdx := 0
	parts := make([]string, 0, len(ki.Cols))
	for _, c := range ki.Cols {
		if c > 0 {
			if nm, ok := nameByCol[c]; ok && nm != "" {
				parts = append(parts, engine.QuoteName(nm))
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

// ---------- engine.TableMeta 分区辅助 ----------

// PartitionByClauseSelf：engine.TableMeta → "PARTITION BY <STRAT> (key)"；失败返回空串。
func PartitionByClauseSelf(tm *engine.TableMeta) string {
	if tm.PartStrat == 0 || len(tm.PartKeyCols) == 0 {
		return ""
	}
	nameByCol := map[int]string{}
	for _, c := range tm.Columns {
		nameByCol[c.AttNum] = c.Name
	}
	ki := &catalog.PartKeyInfo{Strat: tm.PartStrat, Cols: tm.PartKeyCols}
	return PartitionByClause(ki, tm.PartExprs, nameByCol)
}

// PkCoversPartKey：主键是否覆盖全部分区键列（PG 对分区父表的硬约束）。
// 表达式键位（col==0）无法按名比较，跳过。
func PkCoversPartKey(tm *engine.TableMeta) bool {
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

// SortTablesPartitionFirst：按分区链深度稳定排序（父先于子）。
// 非分区表与不在任务集内的父链视为深度 0，保持原相对顺序。
func SortTablesPartitionFirst(tasks []*engine.TableMeta) {
	if len(tasks) < 2 {
		return
	}
	byOID := map[uint32]*engine.TableMeta{}
	for _, t := range tasks {
		byOID[t.OID] = t
	}
	depth := map[uint32]int{}
	var calc func(t *engine.TableMeta) int
	calc = func(t *engine.TableMeta) int {
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
