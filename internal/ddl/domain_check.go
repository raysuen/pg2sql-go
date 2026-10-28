package ddl

// 域（DOMAIN）CHECK 约束离线还原（v1.0.50；v1.0.51 起通用反解层拆至 nodetree.go）
//
// 数据来源：pg_constraint（2606）。域 CHECK 的存储形式随内核版本不同：
//   - PG13-（含金仓 V8/V9）：contype='d'，conrelid=0，contypid=域类型 OID
//   - PG14+：contype='c'，conrelid=0，contypid=域类型 OID（与表 CHECK 同型，
//     靠 conrelid=0 区分）
// 约束表达式存于 conbin（pg_node_tree 文本），本文件只负责域上下文的目录读取
// 与快照；节点树解析/反解由 nodetree.go 提供。域上下文中 VAR 渲染为 VALUE
// （varAt 钩子传 nil）。

import (
	"pg2sql/internal/catalog"
	"pg2sql/internal/datatype"
	"pg2sql/internal/engine"
	"pg2sql/internal/nt"
	"sync/atomic"
)

// ---------- 快照存储 ----------

// DomainCheck：域 CHECK 约束
type DomainCheck struct {
	Name string
	Expr string // 反解出的 SQL（VALUE 代表域值）；空串=不可还原
}

var domainCheckStore atomic.Value // map[uint32][]DomainCheck

func SetDomainChecks(m map[uint32][]DomainCheck) {
	if m == nil {
		m = map[uint32][]DomainCheck{}
	}
	domainCheckStore.Store(m)
}

func DomainCheckSnapshot() map[uint32][]DomainCheck {
	if v := domainCheckStore.Load(); v != nil {
		return v.(map[uint32][]DomainCheck)
	}
	return nil
}

// ---------- 目录读取 ----------

// BuildDomainChecks 读 pg_constraint（2606）提取域 CHECK 约束。
func BuildDomainChecks(dbDir string, version int, isKB bool) map[uint32][]DomainCheck {
	m := map[uint32][]DomainCheck{}
	p := engine.DetectSysFile(dbDir, 2606, map[string]bool{"pg_constraint": true, "sys_constraint": true}, 0)
	if p == "" {
		return m
	}
	cols := engine.NamedTableLayout(dbDir, 2606, version, isKB)
	if len(cols) == 0 {
		return m
	}
	opMap := catalog.BuildOperatorMap(dbDir, version, isKB)
	procMap := catalog.BuildProcNameMap(dbDir, version, isKB)
	for tup := range engine.IterSysTuples(p, version, isKB) {
		if !tup.IsLive() {
			continue
		}
		// PG12+/V9 的 pg_constraint 无 oid 系统列 → 字段判据
		f, _ := engine.ParseNamedTuple(tup, cols, false, false)
		if len(f) == 0 {
			continue
		}
		typ := engine.FByte(f, "contype")
		// 域 CHECK：PG13- 为 'd'；PG14+ 为 'c' 且 conrelid=0（表 CHECK 的 conrelid 非 0）
		isDomainCheck := typ == 'd' || (typ == 'c' && engine.FU32(f, "conrelid") == 0)
		if !isDomainCheck {
			continue
		}
		doid := engine.FU32(f, "contypid")
		if doid == 0 {
			continue
		}
		dc := DomainCheck{Name: engine.FCstr(f, "conname")}
		if b, ok := f["conbin"]; ok && len(b) > 0 {
			dc.Expr = nt.DeparseNodeTree(string(datatype.VarlenaPayloadBytes(b)), opMap, procMap, nil)
		}
		m[doid] = append(m[doid], dc)
	}
	return m
}

// ConstraintNameSQL：约束名输出（简单标识符裸写，其余加引号）
func ConstraintNameSQL(name string) string {
	if name == "" {
		return ""
	}
	for i, c := range name {
		ok := c == '_' || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9' && i > 0)
		if !ok {
			return nt.QuoteSQLIdent(name)
		}
	}
	return name
}
