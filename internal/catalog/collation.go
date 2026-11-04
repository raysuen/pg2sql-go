// 排序规则（COLLATE）目录读取（v1.0.63，修复 B-8；v1.0.64 修复 E-2 限定名）
//
// attcollation 是 pg_collation 的 OID；导出 DDL 需要名字形式 `COLLATE sch.name`。
// 历史实现完全没有采集 attcollation，依赖排序规则的列（如 `text COLLATE "C"`）
// 回灌后落到库默认排序规则，比较/索引行为与原库不一致。
//
// v1.0.64（E-2）：自定义 collation 可能不在 pg_catalog（CREATE COLLATION
// 默认落在当前 search_path 的 schema，常见是 public 或用户 schema）。
// 旧实现输出裸名 `COLLATE "mycoll"`：目标端 search_path 命中同名 collation
// 时静默绑错，未命中时报 relation does not exist。现缓存 collnamespace，
// 统一输出 `COLLATE <ns>."<name>"`（pg_catalog 内建名保持 pg_catalog 前缀）。
package catalog

import (
	"sync"

	"pg2sql/internal/engine"
)

const PG_COLLATION_RELFILE = 3456

// collDefaultOID：pg_catalog."default" —— 库默认排序规则，不需要输出 COLLATE
const collDefaultOID = 100

type collEntry struct {
	NS   string // collnamespace 的 schema 名（pg_catalog / 自定义 schema）
	Name string // collname
}

var (
	collCacheMu sync.Mutex
	collCacheDB string
	collCacheV  map[uint32]collEntry
	collCacheOK bool
)

// CollationNamesCached：OID → collname（按 dbDir 缓存；兼容旧调用点）
func CollationNamesCached(dbDir string, version int, isKB bool) map[uint32]string {
	entries := CollationEntriesCached(dbDir, version, isKB)
	m := make(map[uint32]string, len(entries))
	for oid, e := range entries {
		m[oid] = e.Name
	}
	return m
}

// CollationEntriesCached：OID → {schema, collname}（按 dbDir 缓存）
func CollationEntriesCached(dbDir string, version int, isKB bool) map[uint32]collEntry {
	collCacheMu.Lock()
	defer collCacheMu.Unlock()
	if collCacheOK && collCacheDB == dbDir {
		return collCacheV
	}
	m := map[uint32]collEntry{}
	if p := engine.DetectSysFile(dbDir, PG_COLLATION_RELFILE, map[string]bool{"pg_collation": true, "sys_collation": true}, 0); p != "" {
		if cols := engine.NamedTableLayout(dbDir, PG_COLLATION_RELFILE, version, isKB); len(cols) > 0 {
			nsMap := namespaceMap(dbDir, version, isKB)
			for tup := range engine.IterSysTuples(p, version, isKB) {
				f, _ := engine.ParseNamedTuple(tup, cols, false, false)
				if len(f) == 0 {
					continue
				}
				oid := engine.FU32(f, "oid")
				name := engine.FCstr(f, "collname")
				if oid == 0 || name == "" {
					continue
				}
				e := collEntry{Name: name, NS: "pg_catalog"}
				if n := nsMap[engine.FU32(f, "collnamespace")]; n != "" {
					e.NS = n
				}
				m[oid] = e
			}
		}
	}
	collCacheDB, collCacheV, collCacheOK = dbDir, m, true
	return m
}

// namespaceMap：OID → schema 名（pg_namespace=2615；读不到时返回空表，
// 此时自定义 collation 退回 pg_catalog 前缀——比裸名安全）
func namespaceMap(dbDir string, version int, isKB bool) map[uint32]string {
	m := map[uint32]string{}
	if p := engine.DetectSysFile(dbDir, PG_NAMESPACE_RELFILE, map[string]bool{"pg_namespace": true, "sys_namespace": true}, 0); p != "" {
		if cols := engine.NamedTableLayout(dbDir, PG_NAMESPACE_RELFILE, version, isKB); len(cols) > 0 {
			for tup := range engine.IterSysTuples(p, version, isKB) {
				f, _ := engine.ParseNamedTuple(tup, cols, false, false)
				if len(f) == 0 {
					continue
				}
				oid := engine.FU32(f, "oid")
				name := engine.FCstr(f, "nspname")
				if oid != 0 && name != "" {
					m[oid] = name
				}
			}
		}
	}
	return m
}

// CollationSQL：把 attcollation OID 转成 DDL 片段（默认排序规则返回空串）。
// 返回值已带前导空格，可直接拼在类型名之后。
//
// v1.0.64（E-2）：统一输出 `COLLATE <schema>."<name>"`：
//   - 内建名（C/POSIX/ucs_basic/unicode，全部位于 pg_catalog）→ pg_catalog 前缀；
//   - 自定义 collation → 真实所属 schema 前缀（目录读不到 schema 时退 pg_catalog，
//     不再输出裸名导致 search_path 解析歧义）。
func CollationSQL(dbDir string, oid uint32, version int, isKB bool) string {
	if oid == 0 || oid == collDefaultOID {
		return ""
	}
	e, ok := CollationEntriesCached(dbDir, version, isKB)[oid]
	if !ok || e.Name == "" || e.Name == "default" {
		return ""
	}
	return " COLLATE " + engine.QuoteName(e.NS) + "." + engine.QuoteName(e.Name)
}
