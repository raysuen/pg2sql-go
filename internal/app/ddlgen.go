// Author: raysuen
package app

import (
	"fmt"
	"pg2sql/internal/catalog"
	"pg2sql/internal/datatype"
	"pg2sql/internal/ddl"
	"pg2sql/internal/engine"
	"strings"
)

// qualTypeName：用户自定义类型（枚举/域/复合）输出为 schema 限定的带引号名。
// v1.0.48：原实现只用裸类型名，落到 search_path 指向的 schema（通常 public）；
// 若两个 schema 存在同名类型，回灌会静默绑到错误的类型上。内置类型不加限定。
func qualTypeName(tm *engine.TableMeta, oid uint32) string {
	base := typeDisplayName(tm, oid)
	if !isUserTypeOID(oid) {
		return engine.QuoteName(base)
	}
	if sch := catalog.TypeSchemaName(oid); sch != "" {
		return engine.QuoteName(sch) + "." + engine.QuoteName(base)
	}
	// 拿不到 typnamespace（元数据缺失）时退回表自身 schema，保证自洽
	if tm.Schema != "" {
		return engine.QuoteName(tm.Schema) + "." + engine.QuoteName(base)
	}
	return engine.QuoteName(base)
}

// isUserTypeOID：该 oid 是否为枚举/域/复合等用户自定义类型。
func isUserTypeOID(oid uint32) bool {
	if datatype.DynamicTypeDefs == nil {
		return false
	}
	td, ok := datatype.DynamicTypeDefs[oid]
	if !ok {
		return false
	}
	return td.Type == 'e' || td.Type == 'd' || td.Type == 'c'
}

// wrapDuplicateGuard：把 CREATE TYPE / CREATE DOMAIN 包进 DO 块，已存在时静默跳过。
// v1.0.48：pg2sql 按表输出文件，同一 schema 下多表共用同一枚举/域时，每个文件都会
// 自带 CREATE TYPE；整目录顺序回灌时第二个文件必然报
// `ERROR: type "mood" already exists`。加守卫后文件保持自包含且可重复执行。
func wrapDuplicateGuard(stmt string) string {
	lines := strings.Split(strings.TrimSuffix(stmt, ";"), "\n")
	for i := 1; i < len(lines); i++ {
		lines[i] = "  " + lines[i]
	}
	body := strings.Join(lines, "\n")
	// v1.0.49 修复（N2）：DO 块定界符不能与语句体内已有的 dollar-quote 标签冲突
	// （如枚举标签 'a$$b' 会把 $$ 定界的 DO 块提前闭合 → 回灌语法错误）。
	// 探测一个语句体中不存在的唯一 tag。
	tag := "$$"
	for i := 1; ; i++ {
		cand := fmt.Sprintf("$do%d$", i)
		if !strings.Contains(body, cand) {
			tag = cand
			break
		}
	}
	return "DO " + tag + " BEGIN\n  " + body +
		";\nEXCEPTION WHEN duplicate_object THEN NULL;\nEND " + tag + ";\n"
}

// GenerateDDL：生成 CREATE TABLE（枚举列前置 CREATE TYPE）
func generateDDL(tm *engine.TableMeta) string {
	var sb strings.Builder
	seen := map[uint32]bool{}
	// v1.0.48 修复（P1-F3）：枚举标量列与枚举数组列都要输出 CREATE TYPE。
	// 数组列的 atttypid 是数组类型 oid（如 _mood），而 datatype.EnumMap 的 key 是枚举类型
	// 自身 oid（mood），原实现直接查表必然 miss —— 表里只有 mood[] 列时
	// DDL 完全缺 CREATE TYPE，回灌报 type "mood" does not exist。
	for _, c := range tm.Columns {
		if c.Dropped {
			continue
		}
		eoid := c.TypeOID
		if eo, ok := datatype.UserArrayElemOID(eoid); ok {
			eoid = eo
		}
		m, ok := datatype.EnumMapSnapshot()[eoid]
		if !ok || seen[eoid] {
			continue
		}
		seen[eoid] = true
		tname := qualTypeName(tm, eoid)
		labels := make([]string, 0, len(m))
		for _, mem := range m {
			labels = append(labels, "'"+strings.ReplaceAll(mem.Label, "'", "''")+"'")
		}
		sb.WriteString(wrapDuplicateGuard("CREATE TYPE " + tname + " AS ENUM (" + strings.Join(labels, ", ") + ")"))
	}
	// v1.0.48（P1-F1 收尾）：域类型（CREATE DOMAIN）。列类型里直接写域名，
	// 不建域则 CREATE TABLE 报 type "posint" does not exist。
	// 注意：域上的 CHECK 约束离线不可还原（pg_constraint.conbin 是节点树），
	// 这里只还原基类型与 NOT NULL，并给出显式提示注释。
	for _, c := range tm.Columns {
		if c.Dropped || datatype.DynamicTypeDefs == nil {
			continue
		}
		doid := c.TypeOID
		if eo, ok := datatype.UserArrayElemOID(doid); ok {
			doid = eo
		}
		td, ok := datatype.DynamicTypeDefs[doid]
		if !ok || td.Type != 'd' || td.Base == 0 || seen[doid] {
			continue
		}
		seen[doid] = true
		baseName := typeDisplayName(tm, td.Base)
		if isUserTypeOID(td.Base) {
			baseName = qualTypeName(tm, td.Base)
		}
		s := "CREATE DOMAIN " + qualTypeName(tm, doid) + " AS " + baseName
		if td.NotNull {
			s += " NOT NULL"
		}
		// v1.0.50：域 CHECK 约束还原（pg_constraint conbin 节点树反解，见 domain_check.go）。
		// 全部成功 → 输出 CONSTRAINT name CHECK (expr)；部分/全部失败 → 人工提示注释。
		dcs := ddl.DomainCheckSnapshot()[doid]
		if len(dcs) > 0 {
			var parts []string
			var failed []string
			for _, dc := range dcs {
				if dc.Expr == "" {
					failed = append(failed, dc.Name)
					continue
				}
				parts = append(parts, "CONSTRAINT "+ddl.ConstraintNameSQL(dc.Name)+" CHECK ("+dc.Expr+")")
			}
			if len(parts) > 0 {
				s += " " + strings.Join(parts, " ")
			}
			if len(failed) > 0 {
				sb.WriteString("-- 注意: 域 " + typeDisplayName(tm, doid) +
					" 的 CHECK 约束 [" + strings.Join(failed, ", ") + "] 表达式无法离线还原，需人工补回\n")
			}
		} else if ddl.DomainCheckSnapshot() == nil {
			// 目录缺失（无 pg_constraint 文件/布局读取失败）→ 保留旧提示；
			// 目录已读但该域无 CHECK 行 → 域本无约束，不需要提示
			sb.WriteString("-- 注意: 域 " + typeDisplayName(tm, doid) + " 的 CHECK 约束无法离线还原，需人工补回\n")
		}
		sb.WriteString(wrapDuplicateGuard(s))
	}
	// v1.0.48（P1-F1 收尾）：复合类型列/复合数组列必须先建类型，否则
	// CREATE TABLE ... ca contact[] 回灌直接报 type "contact" does not exist。
	// 字段清单复用 datatype.CompositeAttrs（datatype.DecodeComposite 同一份 pg_attribute 缓存）。
	for _, c := range tm.Columns {
		if c.Dropped || datatype.DynamicTypeDefs == nil {
			continue
		}
		coid := c.TypeOID
		if eo, ok := datatype.UserArrayElemOID(coid); ok {
			coid = eo
		}
		td, ok := datatype.DynamicTypeDefs[coid]
		if !ok || td.Type != 'c' || td.TypRelid == 0 || seen[coid] {
			continue
		}
		attrs := datatype.CompositeAttrs[td.TypRelid]
		if len(attrs) == 0 {
			continue
		}
		seen[coid] = true
		fs := make([]string, 0, len(attrs))
		for _, a := range attrs {
			fs = append(fs, "  "+engine.QuoteName(a.AttName)+" "+
				colTypeSQL(tm, engine.ColumnDef{Name: a.AttName, TypeOID: a.AttTypID, TypMod: a.AttTypMod}))
		}
		sb.WriteString(wrapDuplicateGuard("CREATE TYPE " + qualTypeName(tm, coid) + " AS (\n" +
			strings.Join(fs, ",\n") + "\n)"))
	}
	var cols []string
	for _, c := range tm.Columns {
		if c.Dropped {
			continue
		}
		s := "  " + engine.QuoteName(c.Name) + " " + colTypeSQL(tm, c)
		if c.NotNull {
			s += " NOT NULL"
		}
		cols = append(cols, s)
	}
	if len(tm.PrimaryKey) > 0 {
		pks := make([]string, 0, len(tm.PrimaryKey))
		for _, p := range tm.PrimaryKey {
			pks = append(pks, engine.QuoteName(p))
		}
		cols = append(cols, "  PRIMARY KEY ("+strings.Join(pks, ", ")+")")
	}
	// v1.0.52：分区子表 → PARTITION OF 语法（列结构自父表继承，PG 不允许带列清单）。
	// 枚举/域/复合类型前置语句已输出（单表导出子表时仍需要）。
	if tm.PartParent != 0 && tm.PartBound != "" && tm.PartParentQual != "" {
		stmt := "CREATE TABLE IF NOT EXISTS " + engine.QuoteName(tm.Schema) + "." + engine.QuoteName(tm.RelName) +
			" PARTITION OF " + tm.PartParentQual + " " + tm.PartBound
		if pb := ddl.PartitionByClauseSelf(tm); pb != "" {
			stmt += " " + pb // 多级分区：子表自身也是分区父表
		}
		return sb.String() + stmt + ";"
	}
	// 分区子表但边界/父名无法还原 → 回退普通表（数据可正常导入），显式提示人工挂回
	if tm.PartBoundFail || (tm.PartParent != 0 && tm.PartParentQual == "") {
		sb.WriteString("-- 注意: 表 " + tm.FullName() + " 是分区子表，但分区边界无法离线还原；" +
			"已按普通表输出，回灌后数据在独立表中，需人工 ATTACH PARTITION 挂回\n")
	}
	// 分区父表：尾部追加 PARTITION BY；主键未包含分区键时回灌必失败 → 显式告警
	tail := "\n);"
	if tm.PartStrat != 0 {
		if pb := ddl.PartitionByClauseSelf(tm); pb != "" {
			tail = "\n) " + pb + ";"
			if len(tm.PrimaryKey) > 0 && !ddl.PkCoversPartKey(tm) {
				sb.WriteString("-- 注意: 分区键未完全包含在主键内，回灌将报错" +
					"（unique constraint on partitioned table must include all partitioning columns）；" +
					"可能源库主键读取不完整，请核对源库主键定义\n")
			}
		} else {
			sb.WriteString("-- 注意: 分区键无法离线还原（表达式键或目录读取失败），已按普通表输出；" +
				"回灌后需人工补 PARTITION BY 并重建分区\n")
		}
	}
	return sb.String() + "CREATE TABLE IF NOT EXISTS " + engine.QuoteName(tm.Schema) + "." + engine.QuoteName(tm.RelName) + " (\n" +
		strings.Join(cols, ",\n") + tail
}

// ColTypeSQL：列的 SQL 类型表示（含 typmod）
// 内置类型名（对齐 Python types.py TYPE_NAMES；catalog-json 离线模式兜底）
var builtinTypeNames = map[uint32]string{
	16: "bool", 17: "bytea", 18: "char", 19: "name", 20: "bigint", 21: "smallint",
	23: "integer", 24: "regproc", 25: "text", 26: "oid", 27: "tid", 28: "xid",
	29: "cid", 114: "json", 142: "xml", 199: "json[]", 700: "real", 701: "double precision",
	705: "unknown", 790: "money", 829: "macaddr", 869: "inet", 650: "cidr",
	774: "macaddr8", 1042: "character", 1043: "character varying", 1082: "date",
	1083: "time", 1114: "timestamp", 1184: "timestamptz", 1186: "interval",
	1266: "timetz", 1560: "bit", 1562: "varbit", 1700: "numeric", 2950: "uuid",
	3802: "jsonb", 22: "int2vector", 30: "oidvector", 7050: "pg_node_tree",
	32:   "pg_ddl_command",
	1000: "bool[]", 1001: "bytea[]", 1002: "char[]", 1003: "name[]", 1005: "smallint[]",
	1006: "int2vector[]", 1007: "integer[]", 1008: "regproc[]", 1009: "text[]",
	1010: "tid[]", 1011: "xid[]", 1012: "cid[]", 1013: "oidvector[]", 1014: "bpchar[]",
	1015: "varchar[]", 1016: "int8[]", 1017: "point[]", 1018: "lseg[]", 1019: "path[]",
	1020: "box[]", 1021: "float4[]", 1022: "float8[]", 1023: "interval[]",
	1024: "timetz[]", 1025: "time[]", 1026: "timestamp[]", 1027: "timestamptz[]",
	1028: "oid[]", 1030: "macaddr[]", 1031: "inet[]", 1033: "aclitem[]",
	1040: "macaddr8[]", 1041: "bit[]", 1044: "bool[]", 1182: "date[]", 1183: "time[]",
	1185: "timestamp[]", 1187: "timestamptz[]", 1231: "numeric[]", 1270: "tid[]",
	1561: "bit[]", 1563: "varbit[]", 2951: "uuid[]", 3642: "xml[]", 3807: "jsonb[]",
}

// typeDisplayName：类型 oid 的 SQL 显示名（不含 typmod、不含数组方括号）
func typeDisplayName(tm *engine.TableMeta, oid uint32) string {
	if tm.TypeNames != nil {
		if n, ok := tm.TypeNames[oid]; ok && n != "" {
			return n
		}
	}
	if n, ok := builtinTypeNames[oid]; ok {
		return n
	}
	return fmt.Sprintf("oid:%d", oid)
}

func colTypeSQL(tm *engine.TableMeta, col engine.ColumnDef) string {
	// v1.0.48 修复（P1-F1）：数组列输出 "元素类型[]"，不再用 pg_type 里的内部名
	// "_xxx"。内部名虽然也能建表，但语义错、与枚举 CREATE TYPE 的命名对不上，
	// 且自定义类型数组此前完全拿不到解码器（见 types.go datatype.UserArrayElemOID）。
	if eo, ok := datatype.UserArrayElemOID(col.TypeOID); ok {
		// 仅自定义元素类型加 schema 限定；内置数组保持 int4[]/text[] 裸名
		if isUserTypeOID(eo) {
			return qualTypeName(tm, eo) + "[]"
		}
		return typeDisplayName(tm, eo) + "[]"
	}
	base := typeDisplayName(tm, col.TypeOID)
	// v1.0.48：自定义类型（枚举/域/复合）加 schema 限定，摆脱对 search_path 的依赖
	if isUserTypeOID(col.TypeOID) {
		base = qualTypeName(tm, col.TypeOID)
	}
	switch col.TypeOID {
	case datatype.BPCHAROID, datatype.VARCHAROID:
		if col.TypMod >= 4 {
			return fmt.Sprintf("%s(%d)", base, col.TypMod-4)
		}
		return base
	case datatype.NUMERICOID:
		if col.TypMod >= 4 {
			tmp := col.TypMod - 4
			prec := (tmp >> 16) & 0xFFFF
			scale := tmp & 0xFFFF
			return fmt.Sprintf("numeric(%d,%d)", prec, scale)
		}
		return "numeric"
	case 1560, 1562: // bit/varbit
		if col.TypMod != 0 {
			return fmt.Sprintf("%s(%d)", base, col.TypMod)
		}
		return base
	// v1.0.46 修复（金仓 mysql 模式 DDL 丢长度）：binary/varbinary 的 typmod 与
	// varchar 同约定（长度+4），mysql BIT（oid 4655）typmod 即位数。
	// 旧实现落到默认分支，输出 "binary"/"bit"，而 mysql 模式 binary==binary(1)、
	// bit==bit(1)，回灌 16 字节二进制列/8 位 BIT 列直接报 value too long。
	case 3383, 3384: // 金仓 mysql binary / varbinary
		if col.TypMod >= 4 {
			return fmt.Sprintf("%s(%d)", base, col.TypMod-4)
		}
		return base
	case 4655: // 金仓 mysql BIT
		if col.TypMod > 0 {
			return fmt.Sprintf("%s(%d)", base, col.TypMod)
		}
		return base
	case datatype.TIMEOID, datatype.TIMETZOID, datatype.TIMESTAMPOID, datatype.TIMESTAMPTZOID:
		if col.TypMod >= 0 {
			return fmt.Sprintf("%s(%d)", base, col.TypMod)
		}
		return base
	}
	return base
}
