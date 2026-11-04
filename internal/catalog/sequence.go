// 序列（SEQUENCE）目录读取（v1.0.63，修复 B-6/N-2）
//
// 两处历史缺口：
//  1. B-6：`CREATE SEQUENCE IF NOT EXISTS x;` 不带任何选项，INCREMENT/MINVALUE/
//     MAXVALUE/START/CACHE/CYCLE/AS 全丢；回灌后 nextval 步长变成 1。
//  2. N-2：只有被列 DEFAULT nextval 引用的序列才会输出（走 colSeq），
//     **独立序列（没有任何列引用）在任何导出路径都不出现** —— 整库搬迁后
//     nextval('seq') 直接报 relation does not exist。
//
// 数据来源：
//   - pg_class（relkind='S'）：schema/name/relfilenode
//   - pg_sequence（PG10+，relfilenode 2224）：seqtypid/seqstart/seqincrement/
//     seqmax/seqmin/seqcache/seqcycle
//   - 序列关系自身：last_value（PG14+ 还有 log_cnt/is_called 两列）
package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"

	"pg2sql/internal/engine"
)

// resolveTablespaceRel：按 pg_tblspc/sys_tblspc 解析表空间关系文件路径。
// dbDir 形如 <root>/base/<dboid>（金仓 base/ 同构）；版本目录可能有多个，
// 取「目标文件真实存在」的那个。返回 "" 表示定位失败。
func resolveTablespaceRel(dbDir string, relfilenode, tablespace uint32) string {
	if dbDir == "" || relfilenode == 0 || tablespace == 0 {
		return ""
	}
	root := filepath.Dir(filepath.Dir(dbDir))
	dboid := filepath.Base(dbDir)
	rf := strconv.FormatUint(uint64(relfilenode), 10)
	ts := strconv.FormatUint(uint64(tablespace), 10)
	for _, tsRoot := range []string{"pg_tblspc", "sys_tblspc"} {
		tsDir := filepath.Join(root, tsRoot, ts)
		entries, err := os.ReadDir(tsDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			cand := filepath.Join(tsDir, e.Name(), dboid, rf)
			if fi, err := os.Stat(cand); err == nil && fi.Mode().IsRegular() {
				return cand
			}
		}
	}
	// 软链接解析兜底：pg_tblspc/<ts> 本身是软链接，个别挂载场景需显式 EvalSymlinks。
	// v1.0.65 修复（F16）：金仓用 sys_tblspc，兜底此前只试 pg_tblspc，软链接悬空时
	// 金仓表空间序列仍定位失败。
	for _, tsRoot := range []string{"pg_tblspc", "sys_tblspc"} {
		real, err := filepath.EvalSymlinks(filepath.Join(root, tsRoot, ts))
		if err != nil {
			continue
		}
		entries, err := os.ReadDir(real)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			cand := filepath.Join(real, e.Name(), dboid, rf)
			if fi, err := os.Stat(cand); err == nil && fi.Mode().IsRegular() {
				return cand
			}
		}
	}
	return ""
}

// SequenceInfo：一个序列的完整定义 + 当前值
type SequenceInfo struct {
	OID         uint32
	Schema      string
	Name        string
	RelFileNode uint32
	TableSpace  uint32
	TypeOID     uint32 // seqtypid：20=bigint（默认）, 21=smallint, 23=integer
	Start       int64
	Increment   int64
	Max         int64
	Min         int64
	Cache       int64
	Cycle       bool
	LastValue   int64
	IsCalled    bool
	HasAttrs    bool // pg_sequence 读到（PG10+）
	HasLast     bool // 读到了序列关系里的当前值
	// Internal：pg_depend deptype='i' 的内部序列（IDENTITY 列自动创建）。
	// 这类序列会随 CREATE TABLE ... GENERATED AS IDENTITY 自动重建，
	// 导出端不能再单独 CREATE（会报 relation already exists），只能补 setval。
	Internal bool
	// OwnerRel/OwnerAtt：IDENTITY 序列的宿主表 OID 与列号（pg_depend.refobjid/refobjsubid）
	OwnerRel uint32
	OwnerAtt int
	// Warn：当前值读取失败的原因（v1.0.64 修复 E-8，典型：表空间序列文件未定位到）。
	// 非空时调用方应输出告警注释——旧实现对表空间序列静默跳过 last_value，
	// 回灌后从 START 重计数可能撞主键且零提示。
	Warn string
}

// QualifiedName：schema.name（未加引号）
func (si *SequenceInfo) QualifiedName() string {
	if si.Schema == "" {
		return si.Name
	}
	return si.Schema + "." + si.Name
}

// CreateSQL：带全部选项的 CREATE SEQUENCE。
// 统一带 IF NOT EXISTS：同一序列常被逐表 DDL 与 --full-schema 两条路径重复输出，
// 且用户重复执行脚本时不应报错（选项以首次创建为准，与 PG 原生语义一致）。
func (si *SequenceInfo) CreateSQL(q func(string) string) string {
	s := "CREATE SEQUENCE IF NOT EXISTS " + q(si.Schema) + "." + q(si.Name)
	if si.HasAttrs {
		switch si.TypeOID {
		case 21:
			s += " AS smallint"
		case 23:
			s += " AS integer"
		}
		if si.Increment != 0 {
			s += " INCREMENT BY " + strconv.FormatInt(si.Increment, 10)
		}
		// MINVALUE/MAXVALUE 必须显式给出：AS integer 的默认区间与 bigint 不同
		s += " MINVALUE " + strconv.FormatInt(si.Min, 10) +
			" MAXVALUE " + strconv.FormatInt(si.Max, 10)
		if si.Start != 0 {
			s += " START WITH " + strconv.FormatInt(si.Start, 10)
		}
		if si.Cache != 0 {
			s += " CACHE " + strconv.FormatInt(si.Cache, 10)
		}
		if si.Cycle {
			s += " CYCLE"
		} else {
			s += " NO CYCLE"
		}
	}
	return s + ";"
}

// SetvalSQL：把当前值同步到目标库（空串 = 没读到当前值，不输出）
func (si *SequenceInfo) SetvalSQL(q func(string) string, literal func(string) string) string {
	if !si.HasLast {
		return ""
	}
	called := "true"
	if !si.IsCalled {
		called = "false"
	}
	return "SELECT setval(" + literal(q(si.Schema)+"."+q(si.Name)) + "::regclass, " +
		strconv.FormatInt(si.LastValue, 10) + ", " + called + ");"
}

// WarnSQL：当前值读取失败的告警注释（空串 = 无告警）
func (si *SequenceInfo) WarnSQL(q func(string) string) string {
	if si.Warn == "" {
		return ""
	}
	return "-- 警告: 序列 " + q(si.Schema) + "." + q(si.Name) + " " + si.Warn
}

var (
	seqCacheMu sync.Mutex
	seqCacheDB string
	seqCacheV  map[string]*SequenceInfo
	seqCacheOK bool
)

// SequencesCached：按 dbDir 缓存的序列清单（key = "schema.name"）。
// 目录扫描只做一次：逐表 DDL 生成时会反复查询。
func SequencesCached(dbDir string, version int, isKB bool) map[string]*SequenceInfo {
	seqCacheMu.Lock()
	defer seqCacheMu.Unlock()
	if seqCacheOK && seqCacheDB == dbDir {
		return seqCacheV
	}
	m := CollectSequences(dbDir, version, isKB)
	seqCacheDB, seqCacheV, seqCacheOK = dbDir, m, true
	return m
}

// ResetSequenceCache：测试/多库切换时清缓存
func ResetSequenceCache() {
	seqCacheMu.Lock()
	seqCacheDB, seqCacheV, seqCacheOK = "", nil, false
	seqCacheMu.Unlock()
}

// CollectSequences：扫描 pg_class(relkind='S') + pg_sequence + 序列关系本身
func CollectSequences(dbDir string, version int, isKB bool) map[string]*SequenceInfo {
	out := map[string]*SequenceInfo{}
	classPath := engine.DetectSysFile(dbDir, PG_CLASS_RELFILE, map[string]bool{"pg_class": true, "sys_class": true}, 0)
	if classPath == "" {
		return out
	}
	nsMap := map[uint32]string{}
	if p := engine.DetectSysFile(dbDir, PG_NAMESPACE_RELFILE, map[string]bool{"pg_namespace": true, "sys_namespace": true}, 0); p != "" {
		for tup := range engine.IterSysTuples(p, version, isKB) {
			oid, name := NsFields(tup, version)
			if name != "" {
				nsMap[oid] = name
			}
		}
	}
	byOID := map[uint32]*SequenceInfo{}
	for tup := range engine.IterSysTuples(classPath, version, isKB) {
		row := ClassFields(tup, version)
		if row == nil || row.RelKind != "S" {
			continue
		}
		ns := nsMap[row.RelNamespace]
		if ns == "" {
			ns = "public"
		}
		si := &SequenceInfo{
			OID: row.OID, Schema: ns, Name: row.RelName,
			RelFileNode: row.RelFileNode, TableSpace: row.TableSpace,
		}
		byOID[row.OID] = si
		out[si.QualifiedName()] = si
	}
	if len(out) == 0 {
		return out
	}
	// pg_sequence（PG10+）：relfilenode 2224
	if p := engine.DetectSysFile(dbDir, 2224, map[string]bool{"pg_sequence": true, "sys_sequence": true}, 0); p != "" {
		if cols := engine.NamedTableLayout(dbDir, 2224, version, isKB); len(cols) > 0 {
			for tup := range engine.IterSysTuples(p, version, isKB) {
				f, _ := engine.ParseNamedTuple(tup, cols, false, false)
				if len(f) == 0 {
					continue
				}
				si := byOID[engine.FU32(f, "seqrelid")]
				if si == nil {
					continue
				}
				si.TypeOID = engine.FU32(f, "seqtypid")
				si.Start = engine.FInt64(f, "seqstart")
				si.Increment = engine.FInt64(f, "seqincrement")
				si.Max = engine.FInt64(f, "seqmax")
				si.Min = engine.FInt64(f, "seqmin")
				si.Cache = engine.FInt64(f, "seqcache")
				si.Cycle = engine.FBool(f, "seqcycle")
				// v1.0.64 修复（E-13）：INCREMENT 0 对任何真序列都不合法（FInt64 解析
				// 失败时静默返 0 的兜底值）。视为整行属性不可信，禁用选项输出，
				// 否则会生成 `INCREMENT BY 0 START WITH 0` 的非法 DDL。
				if si.Increment == 0 {
					si.HasAttrs = false
					si.Warn = "pg_sequence 属性解析异常（INCREMENT=0），选项未导出，请按目标端默认重建后人工校准"
					continue
				}
				si.HasAttrs = true
			}
		}
	}
	// pg_depend（2608）：IDENTITY 列的序列是 deptype='i' 的内部依赖
	if p := engine.DetectSysFile(dbDir, PG_DEPEND_RELFILE, map[string]bool{"pg_depend": true, "sys_depend": true}, 0); p != "" {
		if cols := engine.NamedTableLayout(dbDir, PG_DEPEND_RELFILE, version, isKB); len(cols) > 0 {
			for tup := range engine.IterSysTuples(p, version, isKB) {
				f, _ := engine.ParseNamedTuple(tup, cols, false, false)
				if len(f) == 0 {
					continue
				}
				if engine.FU32(f, "classid") != PG_CLASS_RELFILE {
					continue
				}
				if c := engine.FCstr(f, "deptype"); len(c) > 0 && c[0] == 'i' {
					if si := byOID[engine.FU32(f, "objid")]; si != nil {
						si.Internal = true
						if engine.FU32(f, "refclassid") == PG_CLASS_RELFILE {
							si.OwnerRel = engine.FU32(f, "refobjid")
							si.OwnerAtt = int(int16(engine.FU16(f, "refobjsubid")))
						}
					}
				}
			}
		}
	}
	// 序列关系自身：last_value（+ PG14 起的 is_called）
	for oid, si := range byOID {
		var path string
		if si.TableSpace != 0 {
			// v1.0.64 修复（E-8）：表空间序列按 pg_tblspc/sys_tblspc 解析路径；
			// 解析不到时置 Warn（调用方输出告警注释），不再静默跳过
			path = resolveTablespaceRel(dbDir, si.RelFileNode, si.TableSpace)
			if path == "" {
				si.Warn = fmt.Sprintf("位于表空间 %d，数据文件未定位到（pg_tblspc 软链接悬空或目录缺失），当前值未同步，回灌后将从 START WITH 重新计数，可能与已有数据撞主键", si.TableSpace)
				continue
			}
		} else {
			path = filepath.Join(dbDir, strconv.Itoa(int(si.RelFileNode)))
		}
		cols := engine.NamedTableLayout(dbDir, oid, version, isKB)
		if len(cols) == 0 {
			si.Warn = "列布局解析失败，当前值未同步"
			continue
		}
		for tup := range engine.IterSysTuples(path, version, isKB) {
			if !tup.IsLive() {
				continue
			}
			f, _ := engine.ParseNamedTuple(tup, cols, false, false)
			if len(f) == 0 {
				continue
			}
			si.LastValue = engine.FInt64(f, "last_value")
			si.HasLast = true
			if _, ok := f["is_called"]; ok {
				si.IsCalled = engine.FBool(f, "is_called")
			} else {
				// PG10-13 的序列关系只有 last_value 一列，is_called 不在元组里。
				// 取 true（宁可跳号也不产生重复值）：last==start 且未调用过时
				// 会多跳一个，但绝不会把已用过的值再发一次。
				si.IsCalled = true
			}
			break
		}
	}
	// 稳定顺序由调用方排序（map 本身无序）
	return out
}

// SortedSequences：按 schema.name 排序，保证输出稳定
func SortedSequences(m map[string]*SequenceInfo) []*SequenceInfo {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]*SequenceInfo, 0, len(keys))
	for _, k := range keys {
		out = append(out, m[k])
	}
	return out
}
