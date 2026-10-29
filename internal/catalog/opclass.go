// operator class（pg_opclass）目录读取（v1.0.63，修复 B-7 衍生缺陷）
//
// EXCLUDE 约束的每个键列都绑定一个 opclass（pg_index.indclass）。默认 opclass
// 可以省略，但 gist/hash 等访问方法上大多数类型没有默认 opclass（如
// btree_gist 提供的 int4_ops(gist)）——漏写 opclass 时回灌会直接报
// `data type integer has no default operator class for access method "gist"`。
package catalog

import (
	"sync"

	"pg2sql/internal/engine"
)

// PG_OPCLASS_RELFILE：pg_opclass 的 relfilenode
const PG_OPCLASS_RELFILE = 2616

// OpclassInfo：opclass 名称与是否默认（默认可省略）
type OpclassInfo struct {
	Name    string
	Schema  string
	Default bool
}

var (
	opcCacheMu sync.Mutex
	opcCacheDB string
	opcCacheV  map[uint32]OpclassInfo
	opcCacheOK bool
)

// OpclassMapCached：opclass OID → 名称/是否默认（按 dbDir 缓存）
func OpclassMapCached(dbDir string, version int, isKB bool) map[uint32]OpclassInfo {
	opcCacheMu.Lock()
	defer opcCacheMu.Unlock()
	if opcCacheOK && opcCacheDB == dbDir {
		return opcCacheV
	}
	m := map[uint32]OpclassInfo{}
	if p := engine.DetectSysFile(dbDir, PG_OPCLASS_RELFILE, map[string]bool{"pg_opclass": true, "sys_opclass": true}, 0); p != "" {
		if cols := engine.NamedTableLayout(dbDir, PG_OPCLASS_RELFILE, version, isKB); len(cols) > 0 {
			nsMap := map[uint32]string{}
			if np := engine.DetectSysFile(dbDir, PG_NAMESPACE_RELFILE, map[string]bool{"pg_namespace": true, "sys_namespace": true}, 0); np != "" {
				for tup := range engine.IterSysTuples(np, version, isKB) {
					if oid, name := NsFields(tup, version); name != "" {
						nsMap[oid] = name
					}
				}
			}
			for tup := range engine.IterSysTuples(p, version, isKB) {
				f, _ := engine.ParseNamedTuple(tup, cols, false, false)
				if len(f) == 0 {
					continue
				}
				oid := engine.FU32(f, "oid")
				name := engine.FCstr(f, "opcname")
				if oid == 0 || name == "" {
					continue
				}
				m[oid] = OpclassInfo{Name: name, Schema: nsMap[engine.FU32(f, "opcnamespace")],
					Default: engine.FBool(f, "opcdefault")}
			}
		}
	}
	opcCacheDB, opcCacheV, opcCacheOK = dbDir, m, true
	return m
}
