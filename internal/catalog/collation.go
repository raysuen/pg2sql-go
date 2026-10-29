// 排序规则（COLLATE）目录读取（v1.0.63，修复 B-8）
//
// attcollation 是 pg_collation 的 OID；导出 DDL 需要名字形式 `COLLATE sch.name`。
// 历史实现完全没有采集 attcollation，依赖排序规则的列（如 `text COLLATE "C"`）
// 回灌后落到库默认排序规则，比较/索引行为与原库不一致。
package catalog

import (
	"sync"

	"pg2sql/internal/engine"
)

const PG_COLLATION_RELFILE = 3456

// collDefaultOID：pg_catalog."default" —— 库默认排序规则，不需要输出 COLLATE
const collDefaultOID = 100

var (
	collCacheMu sync.Mutex
	collCacheDB string
	collCacheV  map[uint32]string
	collCacheOK bool
)

// CollationNamesCached：OID → pg_collation.collname（按 dbDir 缓存）
func CollationNamesCached(dbDir string, version int, isKB bool) map[uint32]string {
	collCacheMu.Lock()
	defer collCacheMu.Unlock()
	if collCacheOK && collCacheDB == dbDir {
		return collCacheV
	}
	m := map[uint32]string{}
	if p := engine.DetectSysFile(dbDir, PG_COLLATION_RELFILE, map[string]bool{"pg_collation": true, "sys_collation": true}, 0); p != "" {
		if cols := engine.NamedTableLayout(dbDir, PG_COLLATION_RELFILE, version, isKB); len(cols) > 0 {
			for tup := range engine.IterSysTuples(p, version, isKB) {
				f, _ := engine.ParseNamedTuple(tup, cols, false, false)
				if len(f) == 0 {
					continue
				}
				oid := engine.FU32(f, "oid")
				name := engine.FCstr(f, "collname")
				if oid != 0 && name != "" {
					m[oid] = name
				}
			}
		}
	}
	collCacheDB, collCacheV, collCacheOK = dbDir, m, true
	return m
}

// CollationSQL：把 attcollation OID 转成 DDL 片段（默认排序规则返回空串）。
// 返回值已带前导空格，可直接拼在类型名之后。
func CollationSQL(dbDir string, oid uint32, version int, isKB bool) string {
	if oid == 0 || oid == collDefaultOID {
		return ""
	}
	name := CollationNamesCached(dbDir, version, isKB)[oid]
	if name == "" {
		return ""
	}
	if name == "default" {
		return ""
	}
	// pg_collation 里除 "C"/"POSIX"/"ucs_basic"/"default" 外都在 pg_catalog；
	// 统一限定 pg_catalog 反而对自定义 collation 报错，故按是否内建名区分。
	switch name {
	case "C", "POSIX", "ucs_basic", "unicode", "default":
		return " COLLATE pg_catalog." + engine.QuoteName(name)
	}
	return " COLLATE " + engine.QuoteName(name)
}
