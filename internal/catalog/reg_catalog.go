// reg* 反查表构建（v1.2.13 补齐族内剩余 8 个；regfamily 注释见 datatype/reg_family.go）
//
// 数据源与读取方式（NamedTableLayout 按 pg_attribute 实时布局，版本/金仓感知）：
//   - pg_proc      (1255)：oid/proname/pronamespace/proargtypes（oidvector）
//   - pg_operator  (2617)：oid/oprname/oprnamespace/oprkind/oprleft/oprright
//   - pg_ts_config (3602)：oid/cfgname/cfgnamespace
//   - pg_ts_dict   (3766)：oid/dictname/dictnamespace
//   - pg_authid    (1260，位于 <PGDATA>/global/)：oid/rolname（复用 BuildRoleNameMap）
//   - pg_collation (3456)：oid/collname/collnamespace
//
// pg_proc/pg_operator/pg_ts_config/pg_ts_dict/pg_authid/pg_collation 均为按 OID
// 查 syscache 的 nailed 目录，PG12-18/金仓均保留物理 oid 列（pg_operator 与
// pg_collation 已有实机同路径读取佐证）。
package catalog

import (
	"pg2sql/internal/datatype"
	"pg2sql/internal/engine"
)

const (
	pgProcRelFile     = 1255
	pgOperatorRelFile = 2617
	pgTSConfigRelFile = 3602
	// pg_ts_dict=3600（PG17.6 实机：parser=3601、config=3602、config_map=3603、
	// template=3764）。注意 3766 是 pg_ts_template_tmplname_index 的 relfilenode，
	// 不能当作 pg_ts_dict——按 3766 读会得到 template 列布局、反查表恒空。
	pgTSDictRelFile = 3600
)

// buildRegProcInfos：pg_proc → regproc/regprocedure 反查表。
// proargtypes 是 oidvector（元素定长 4B、无数组头），parseOidVector 复用。
func buildRegProcInfos(dbDir string, version int, isKB bool) map[uint32]datatype.RegProcInfo {
	m := map[uint32]datatype.RegProcInfo{}
	cols := engine.NamedTableLayout(dbDir, pgProcRelFile, version, isKB)
	if len(cols) == 0 {
		// pg_attribute 不可读：退化为仅函数名（无 schema/签名），
		// regproc 仍能输出名字，regprocedure 退化为 name()。
		for oid, name := range BuildProcNameMap(dbDir, version, isKB) {
			m[oid] = datatype.RegProcInfo{Name: name}
		}
		return m
	}
	p := engine.DetectSysFile(dbDir, pgProcRelFile,
		map[string]bool{"pg_proc": true, "sys_proc": true}, 0)
	if p == "" {
		return m
	}
	for tup := range engine.IterSysTuples(p, version, isKB) {
		f, _ := engine.ParseNamedTuple(tup, cols, false, false)
		if len(f) == 0 {
			continue
		}
		oid := engine.FU32(f, "oid")
		name := engine.FCstr(f, "proname")
		if oid == 0 || name == "" {
			continue
		}
		info := datatype.RegProcInfo{Name: name, Nsp: engine.FU32(f, "pronamespace")}
		if b, ok := f["proargtypes"]; ok && len(b) > 0 {
			info.Args = parseOidVector(datatype.VarlenaPayloadBytes(b))
		}
		m[oid] = info
	}
	return m
}

// buildRegOperInfos：pg_operator → regoper/regoperator 反查表。
func buildRegOperInfos(dbDir string, version int, isKB bool) map[uint32]datatype.RegOperInfo {
	m := map[uint32]datatype.RegOperInfo{}
	cols := engine.NamedTableLayout(dbDir, pgOperatorRelFile, version, isKB)
	if len(cols) == 0 {
		return m
	}
	p := engine.DetectSysFile(dbDir, pgOperatorRelFile,
		map[string]bool{"pg_operator": true, "sys_operator": true}, 0)
	if p == "" {
		return m
	}
	for tup := range engine.IterSysTuples(p, version, isKB) {
		f, _ := engine.ParseNamedTuple(tup, cols, false, false)
		if len(f) == 0 {
			continue
		}
		oid := engine.FU32(f, "oid")
		name := engine.FCstr(f, "oprname")
		if oid == 0 || name == "" {
			continue
		}
		m[oid] = datatype.RegOperInfo{
			Name:  name,
			Nsp:   engine.FU32(f, "oprnamespace"),
			Kind:  engine.FByte(f, "oprkind"),
			Left:  engine.FU32(f, "oprleft"),
			Right: engine.FU32(f, "oprright"),
		}
	}
	return m
}

// buildRegTSNamed：pg_ts_config / pg_ts_dict 的通用读取（两者前 3 列同构：
// oid / 名称(64B name) / namespace）。
func buildRegTSNamed(dbDir string, version int, isKB bool, relOID int,
	names map[string]bool) map[uint32]datatype.RegNamed {
	m := map[uint32]datatype.RegNamed{}
	cols := engine.NamedTableLayout(dbDir, uint32(relOID), version, isKB)
	if len(cols) == 0 {
		return m
	}
	p := engine.DetectSysFile(dbDir, relOID, names, 0)
	if p == "" {
		return m
	}
	for tup := range engine.IterSysTuples(p, version, isKB) {
		f, _ := engine.ParseNamedTuple(tup, cols, false, false)
		if len(f) == 0 {
			continue
		}
		oid := engine.FU32(f, "oid")
		// cfgname / dictname
		name := engine.FCstr(f, "cfgname")
		if name == "" {
			name = engine.FCstr(f, "dictname")
		}
		nsp := engine.FU32(f, "cfgnamespace")
		if nsp == 0 {
			nsp = engine.FU32(f, "dictnamespace")
		}
		if oid == 0 || name == "" {
			continue
		}
		m[oid] = datatype.RegNamed{Name: name, Nsp: nsp}
	}
	return m
}

// buildRegCollInfos：pg_collation → regcollation 反查表。
func buildRegCollInfos(dbDir string, version int, isKB bool) map[uint32]datatype.RegNamed {
	m := map[uint32]datatype.RegNamed{}
	cols := engine.NamedTableLayout(dbDir, PG_COLLATION_RELFILE, version, isKB)
	if len(cols) == 0 {
		return m
	}
	p := engine.DetectSysFile(dbDir, PG_COLLATION_RELFILE,
		map[string]bool{"pg_collation": true, "sys_collation": true}, 0)
	if p == "" {
		return m
	}
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
		m[oid] = datatype.RegNamed{Name: name, Nsp: engine.FU32(f, "collnamespace")}
	}
	return m
}
