// 扩展（EXTENSION）成员识别（v1.0.63 第 4 轮，修复 D-1）
//
// 背景：`--full-schema` 按「对象在用户 schema（非 pg_catalog 等系统 schema）」判定
// 是否导出，但**扩展装到 public 里之后，其成员对象全都落在 public**。于是
// pageinspect / pg_stat_statements / btree_gist 等扩展的函数被当成用户自建函数
// 输出成：
//
//	CREATE OR REPLACE FUNCTION "public"."brin_metapage_info"(...) LANGUAGE "c" ...
//	AS $pg2sql$ brin_metapage_info $pg2sql$;
//
// 回灌时 PG 把 AS 里那段当【共享库路径】去找文件，直接：
//
//	ERROR: could not access file "brin_metapage_info": No such file or directory
//
// 而且扩展本身也不需要导出——目标实例 `CREATE EXTENSION` 一条命令就能装齐。
//
// 判据：pg_depend（2608）里 refclassid = pg_extension（3079）且 deptype = 'e'
// 的行，其 (classid, objid) 就是某个扩展的成员对象。classid 是对象所在系统
// 目录的 OID（pg_proc=1255、pg_trigger=2620、pg_class=1259、pg_constraint=2606…）。
package catalog

import "pg2sql/internal/engine"

// PG_EXTENSION_RELFILE：pg_extension 的 relfilenode
const PG_EXTENSION_RELFILE = 3079

// 扩展成员判定用到的系统目录 OID（即 pg_depend.classid 的取值）
const (
	ExtClassProc       = 1255 // pg_proc
	ExtClassClass      = 1259 // pg_class（表/视图/序列）
	ExtClassConstraint = 2606 // pg_constraint
	ExtClassTrigger    = 2620 // pg_trigger
	ExtClassType       = 1247 // pg_type
)

// ExtKey：pg_depend 的 (classid, objid) 组合
type ExtKey struct {
	ClassID uint32
	ObjID   uint32
}

// ExtensionMembers：一次性扫 pg_depend，返回全部扩展成员对象。
// 读不到 pg_depend 时返回空集合（调用方据此保持旧行为，不会误删对象）。
func ExtensionMembers(dbDir string, version int, isKB bool) map[ExtKey]bool {
	out := map[ExtKey]bool{}
	p := engine.DetectSysFile(dbDir, PG_DEPEND_RELFILE,
		map[string]bool{"pg_depend": true, "sys_depend": true}, 0)
	if p == "" {
		return out
	}
	cols := engine.NamedTableLayout(dbDir, PG_DEPEND_RELFILE, version, isKB)
	if len(cols) == 0 {
		return out
	}
	for tup := range engine.IterSysTuples(p, version, isKB) {
		if !tup.IsLive() {
			continue
		}
		f, _ := engine.ParseNamedTuple(tup, cols, false, false)
		if len(f) == 0 {
			continue
		}
		if engine.FU32(f, "refclassid") != PG_EXTENSION_RELFILE {
			continue
		}
		c := engine.FCstr(f, "deptype")
		if len(c) == 0 || c[0] != 'e' {
			continue
		}
		out[ExtKey{ClassID: engine.FU32(f, "classid"), ObjID: engine.FU32(f, "objid")}] = true
	}
	return out
}

// ExtensionNames：扩展名清单（用于在产物里注明「已跳过哪些扩展的成员对象」）
func ExtensionNames(dbDir string, version int, isKB bool) []string {
	var out []string
	p := engine.DetectSysFile(dbDir, PG_EXTENSION_RELFILE,
		map[string]bool{"pg_extension": true, "sys_extension": true}, 0)
	if p == "" {
		return out
	}
	cols := engine.NamedTableLayout(dbDir, PG_EXTENSION_RELFILE, version, isKB)
	if len(cols) == 0 {
		return out
	}
	for tup := range engine.IterSysTuples(p, version, isKB) {
		if !tup.IsLive() {
			continue
		}
		f, _ := engine.ParseNamedTuple(tup, cols, false, false)
		if len(f) == 0 {
			continue
		}
		if n := engine.FCstr(f, "extname"); n != "" {
			out = append(out, n)
		}
	}
	return out
}
