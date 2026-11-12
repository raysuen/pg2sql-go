// Author: raysuen
package app

import (
	"fmt"
	"pg2sql/internal/catalog"
	"pg2sql/internal/datatype"
	"pg2sql/internal/ddl"
	"pg2sql/internal/engine"
	"pg2sql/internal/integrity"
	"sort"
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

// generateDDL：生成 CREATE TABLE（前置类型段 + 表结构）。
//
// v1.2.10：三个前置类型段（枚举/域/复合）从"只看列的直接类型"升级为
// **类型依赖闭包**：域 → 递归 typbasetype（base 为数组则转到元素类型）、
// 复合 → 递归每个字段类型、枚举为终点。此前同一自定义类型只要藏在下层
// （域链 ≥2 层、域 over 复合/枚举、复合字段引用自定义类型），前置段就只
// 输出顶层一条 `CREATE DOMAIN c9 AS c8`，c1..c8 / comp_t / mood 全缺，
// 空库回灌 `ERROR: type "public.c8" does not exist`，连 CREATE TABLE 都建不出。
// 三桶先后顺序（枚举→域→复合）与桶内列序保持不变，旧场景输出逐字节一致
// （闭包只在旧版漏掉的场景里追加前置语句）。
func generateDDL(tm *engine.TableMeta) string {
	var sb strings.Builder
	seen := map[uint32]bool{}
	visiting := map[uint32]bool{} // 防环（类型依赖正常无环，防御性）
	// v1.2.17：记录本文件涉及的非默认 schema，用于在头部补 CREATE SCHEMA。
	// 表在 "My Sch"、其域也在 "My Sch" 时，旧输出直接从 CREATE DOMAIN 开始，
	// 空库回灌第一条就报 schema "My Sch" does not exist。
	needSchema := map[string]bool{}

	// depElemOID：依赖追踪视角的类型 oid —— 数组类型转到其元素类型
	// （CREATE DOMAIN d AS ct[] 的依赖是 ct 本身，不是内部名 _ct）。
	depElemOID := func(oid uint32) uint32 {
		if eo, ok := datatype.UserArrayElemOID(oid); ok {
			return eo
		}
		return oid
	}

	// emitTypeClosure：递归发射 oid 的全部自定义类型依赖（被依赖者先输出），
	// 再发射 oid 自身。seen 全局去重（同类型只输出一次），visiting 防环。
	var emitTypeClosure func(oid uint32)
	emitTypeClosure = func(oid uint32) {
		if seen[oid] || visiting[oid] || datatype.DynamicTypeDefs == nil {
			return
		}
		td, ok := datatype.DynamicTypeDefs[oid]
		if !ok {
			return
		}
		visiting[oid] = true
		// v1.2.11（L-1）：seen 只在"真的发射了语句"时置位。
		// 三个 emit 函数都可能成功返回但零输出（EnumMapSnapshot 缺该 oid /
		// 域 td.Base==0 / 复合 CompositeAttrs 为空），旧实现无条件置 seen ——
		// 等于把"没发出去"记成"已发过"。当前不可触发（发射成败取决于该 oid
		// 自身的目录数据，与访问顺序无关，重复访问结果必然相同），但一旦出现
		// "先失败、后可用"的目录状态（惰性加载/增量补齐），就是静默缺类型。
		emitted := false
		defer func() {
			delete(visiting, oid)
			if emitted {
				seen[oid] = true
				// v1.2.17：类型自己也可能在非默认 schema 里（域/枚举/复合都算）
				needSchema[catalog.TypeSchemaName(oid)] = true
			}
		}()
		switch td.Type {
		case 'e':
			emitted = emitEnumStmt(&sb, tm, oid)
		case 'd':
			if td.Base != 0 {
				emitTypeClosure(depElemOID(td.Base))
			}
			emitted = emitDomainStmt(&sb, tm, oid)
		case 'c':
			if td.TypRelid != 0 {
				for _, a := range datatype.CompositeAttrs[td.TypRelid] {
					emitTypeClosure(depElemOID(a.AttTypID))
				}
			}
			emitted = emitCompositeStmt(&sb, tm, oid)
		}
	}

	// 枚举段：枚举无依赖，保持按列序（v1.0.48 P1-F3：数组列剥层后同样命中）。
	for _, c := range tm.Columns {
		if c.Dropped {
			continue
		}
		if _, ok := datatype.EnumMapSnapshot()[depElemOID(c.TypeOID)]; ok {
			emitTypeClosure(depElemOID(c.TypeOID))
		}
	}
	// 域段：列的直接类型（或其数组元素）是域时，闭包发射整条域链与基复合/
	// 基枚举。"域 over 复合"的基复合也在这步正确发射（旧版复合段只认列的
	// 直接类型，域列会整个跳过它）。
	if datatype.DynamicTypeDefs != nil {
		for _, c := range tm.Columns {
			if c.Dropped {
				continue
			}
			if td := datatype.DynamicTypeDefs[depElemOID(c.TypeOID)]; td != nil && td.Type == 'd' {
				emitTypeClosure(depElemOID(c.TypeOID))
			}
		}
		// 复合段：列的直接类型（或其数组元素）是复合时，闭包发射其字段依赖。
		for _, c := range tm.Columns {
			if c.Dropped {
				continue
			}
			if td := datatype.DynamicTypeDefs[depElemOID(c.TypeOID)]; td != nil && td.Type == 'c' {
				emitTypeClosure(depElemOID(c.TypeOID))
			}
		}
	}
	var cols []string
	for _, c := range tm.Columns {
		if c.Dropped {
			continue
		}
		s := "  " + engine.QuoteName(c.Name) + " " + colTypeSQL(tm, c)
		// v1.0.63 修复（B-8）：COLLATE 必须紧跟类型名；此前 attcollation 完全不采集，
		// `text COLLATE "C"` 之类回灌后落到库默认排序规则，比较/索引行为与原库不同。
		if coll := catalog.CollationSQL(tm.DbDir, c.Collation, tm.PgVersion, tm.IsKB); coll != "" {
			s += coll
		}
		// v1.0.63 修复（B-8）：IDENTITY 列此前退化成普通列（无自增），
		// 回灌后 INSERT 不指定该列即报 null value in column violates not-null。
		switch c.Identity {
		case "a":
			s += " GENERATED ALWAYS AS IDENTITY"
		case "d":
			s += " GENERATED BY DEFAULT AS IDENTITY"
		}
		// v1.0.65 修复（F4）：PG18 虚拟生成列（AttGenerated=='v'）不落盘，
		// INSERT 列清单不含该列（heapfile.go buildFmtCtx/decodeFields 排除 'v'）。
		// 若 DDL 保留 NOT NULL，每条 INSERT 都会报
		// null value in column ... violates not-null，整表导入失败。
		// 回灌后该列为普通可空列，生成表达式按注记人工补建。
		if c.NotNull && c.AttGenerated != "v" {
			s += " NOT NULL"
		}
		if c.AttGenerated == "v" {
			sb.WriteString("-- 注意: 表 " + tm.FullName() + " 的列 " + engine.QuoteName(c.Name) +
				" 是虚拟生成列（GENERATED ... AS (...) VIRTUAL，PG18+），不落盘故数据未导出；" +
				"已按普通列输出并剥离 NOT NULL 以免导入失败，导入完成后请按源库表达式重建" +
				"（pg_get_expr(adbin, adrelid) 查看）\n")
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
	// v1.2.16：同时登记 integrity 跳过项并在 verdict 里判 fail —— 旧实现只写一行
	// 注释，报告结论可能仍是 ok，回灌后分区语义静默丢失（回归报告 D2）。
	if tm.PartBoundFail || (tm.PartParent != 0 && tm.PartParentQual == "") {
		integrity.AddSkip(integrity.SkipEntry{
			Table:   tm.FullName(),
			Schema:  tm.Schema,
			RelKind: tm.RelKind,
			Reason:  "partition_bound_unresolved",
			Detail:  "分区边界常量无法离线还原，已按普通表输出；回灌后需人工 ATTACH PARTITION 挂回",
		})
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
	// v1.0.63（B-8）：UNLOGGED 表此前按普通表输出（回灌后多出 WAL 开销、语义不符）
	head := "CREATE TABLE IF NOT EXISTS "
	if tm.Persistence == "u" {
		head = "CREATE UNLOGGED TABLE IF NOT EXISTS "
	}
	needSchema[tm.Schema] = true
	return schemaDDLPrefix(needSchema) + sb.String() + head + engine.QuoteName(tm.Schema) + "." + engine.QuoteName(tm.RelName) + " (\n" +
		strings.Join(cols, ",\n") + tail
}

// schemaDDLPrefix：为本次 DDL 涉及的非默认 schema 生成 CREATE SCHEMA 前缀（v1.2.17）。
//
// 背景：pg2sql 的 DDL 按文件输出，每个文件自包含。表与它引用的域/枚举可能都在
// 同一个非 public schema 里，旧实现从不发 CREATE SCHEMA，空库按文件名顺序回灌时
// 第一条 `CREATE DOMAIN "My Sch"."dom_d"` 就报 schema 不存在——整表建不出来。
//
// 用 IF NOT EXISTS 而不是裸 CREATE SCHEMA：多文件共用一个 schema 时，逐个文件
// 回灌不会在第二个文件上失败（与 wrapDuplicateGuard 的取舍一致：保持文件自包含
// 且可重复执行）。
func schemaDDLPrefix(need map[string]bool) string {
	names := make([]string, 0, len(need))
	for n := range need {
		if isDefaultSchemaName(n) {
			continue
		}
		names = append(names, n)
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	var sb strings.Builder
	for _, n := range names {
		sb.WriteString("CREATE SCHEMA IF NOT EXISTS " + engine.QuoteName(n) + ";\n")
	}
	sb.WriteString("\n")
	return sb.String()
}

// isDefaultSchemaName：不需要显式创建的 schema —— 目标库自带。
//
// v1.2.18 修复（P2）：改为转调 ddl.IsBuiltinSchemaForCreate（精确名单）。
// v1.2.17 的 `HasPrefix(n, "sys") || HasPrefix(n, "pg_")` 前缀判定过宽，
// 用户自建的 syslog / system / sysutils / pg_mine 被误判为系统 schema，DDL 头部
// 不生成 CREATE SCHEMA，空库回灌直接 `ERROR: schema "syslog" does not exist`。
//
// 注意与 integrity.IsSystemSchemaTable 的口径差异（那边宁可多认系统，这边宁可少认），
// 理由见 internal/ddl/builtin_schema.go 文件头——两处不可合并成同一实现。
func isDefaultSchemaName(n string) bool {
	return ddl.IsBuiltinSchemaForCreate(n)
}

// baseTypeSQLName：CREATE DOMAIN 基类型名的 SQL 渲染。
// v1.2.10：基类型为自定义数组时输出 "<元素>[]"（如 "public"."comp_t"[]），
// 禁止 pg_type 内部名（_comp_t）落入 CREATE DOMAIN ... AS —— 内部名依赖
// search_path 且语义错。内置数组（text[] 等）走 builtinTypeNames 已带 []。
func baseTypeSQLName(tm *engine.TableMeta, oid uint32) string {
	if eo, ok := datatype.UserArrayElemOID(oid); ok {
		base := typeDisplayName(tm, eo)
		if isUserTypeOID(eo) {
			base = qualTypeName(tm, eo)
		}
		return base + "[]"
	}
	if isUserTypeOID(oid) {
		return qualTypeName(tm, oid)
	}
	return typeDisplayName(tm, oid)
}

// emitEnumStmt：发射 CREATE TYPE ... AS ENUM（含重复守卫；v1.0.49 唯一 dollar-tag）。
// v1.2.11：返回是否真的写入（EnumMap 缺该 oid 时为 false，供调用方决定是否记 seen）。
func emitEnumStmt(sb *strings.Builder, tm *engine.TableMeta, eoid uint32) bool {
	m, ok := datatype.EnumMapSnapshot()[eoid]
	if !ok {
		return false
	}
	labels := make([]string, 0, len(m))
	for _, mem := range m {
		labels = append(labels, "'"+strings.ReplaceAll(mem.Label, "'", "''")+"'")
	}
	sb.WriteString(wrapDuplicateGuard("CREATE TYPE " + qualTypeName(tm, eoid) + " AS ENUM (" + strings.Join(labels, ", ") + ")"))
	return true
}

// emitDomainStmt：发射 CREATE DOMAIN（基类型 + NOT NULL + CHECK 还原）。
// v1.0.48（P1-F1 收尾）引入；v1.2.10 基类型渲染改走 baseTypeSQLName（数组基修正）。
// v1.2.11：返回是否真的写入（域定义缺失/Base==0 时为 false）。
func emitDomainStmt(sb *strings.Builder, tm *engine.TableMeta, doid uint32) bool {
	td := datatype.DynamicTypeDefs[doid]
	if td == nil || td.Base == 0 {
		return false
	}
	s := "CREATE DOMAIN " + qualTypeName(tm, doid) + " AS " + baseTypeSQLName(tm, td.Base)
	// v1.2.17 修复（P2）：域 DEFAULT 此前整段丢失——`CREATE DOMAIN d AS int DEFAULT 7`
	// 导出成 `CREATE DOMAIN d AS int`，回灌后该域所有列的默认值消失。
	// 位置按 PG 语法：AS <type> [DEFAULT expr] [NOT NULL] [CHECK ...]。
	// typdefault 是 text 形态，已是可直接落 SQL 的字面量（如 `7` / `'dflt'::text`），
	// 不再二次 quoting——加了会把 'dflt'::text 变成 '''dflt''::text'。
	if td.TypDefault != "" {
		s += " DEFAULT " + td.TypDefault
	} else if td.TypDefaultBin != "" {
		// v1.2.18 修复（P3）：typdefaultbin 存在但反解失败 —— 明确留占位注释，
		// 而不是静默少一段 DEFAULT（与 v1.2.14 的 "-- 列 x 的默认值表达式无法
		// 离线还原" 同一风格）。同时会在收尾告警与 report 的 skipped[] 里出现。
		sb.WriteString("-- 域 " + qualTypeName(tm, doid) +
			" 的 DEFAULT 表达式无法离线还原，需人工补回（原 typdefaultbin 已留档）\n")
	}
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
	return true
}

// emitCompositeStmt：发射 CREATE TYPE ... AS (...)。
// 字段清单复用 datatype.CompositeAttrs（datatype.DecodeComposite 同一份 pg_attribute 缓存）；
// 字段的类型渲染走 colTypeSQL（含数组 [] 与 schema 限定）。
// v1.2.11：返回是否真的写入（无 typrelid / 字段清单为空时为 false）。
func emitCompositeStmt(sb *strings.Builder, tm *engine.TableMeta, coid uint32) bool {
	td := datatype.DynamicTypeDefs[coid]
	if td == nil || td.TypRelid == 0 {
		return false
	}
	attrs := datatype.CompositeAttrs[td.TypRelid]
	if len(attrs) == 0 {
		return false
	}
	fs := make([]string, 0, len(attrs))
	for _, a := range attrs {
		fs = append(fs, "  "+engine.QuoteName(a.AttName)+" "+
			colTypeSQL(tm, engine.ColumnDef{Name: a.AttName, TypeOID: a.AttTypID, TypMod: a.AttTypMod}))
	}
	sb.WriteString(wrapDuplicateGuard("CREATE TYPE " + qualTypeName(tm, coid) + " AS (\n" +
		strings.Join(fs, ",\n") + "\n)"))
	return true
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
	// v1.2.7 修复（F1）：oid 18/1002 必须带引号，否则裸 `char` 被解析成
	// character(1)（oid 1042），类型静默降级、数据回灌后不可还原。
	// 此处优先于 tm.TypeNames —— pg_type.typname 同样是裸 `char`，两条来源都得过。
	if n, ok := datatype.CharTypeSQL(oid); ok {
		return n
	}
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
		base := typeDisplayName(tm, eo)
		// 仅自定义元素类型加 schema 限定；内置数组保持 int4[]/text[] 裸名
		if isUserTypeOID(eo) {
			base = qualTypeName(tm, eo)
		}
		// v1.0.63 修复（A-3）：数组列必须带元素 typmod。PG 的 atttypmod 对数组列
		// 存的就是元素 typmod，旧实现在这里提前 return，导致 varchar(10)[] 变
		// varchar[]、numeric(10,2)[] 变 numeric[]、timestamp(3)[] 变 timestamp()——
		// 不报错，只静默放宽约束（长度不限、精度变 6 位）。
		return applyTypeMod(base, eo, col.TypMod) + "[]"
	}
	base := typeDisplayName(tm, col.TypeOID)
	// v1.0.48：自定义类型（枚举/域/复合）加 schema 限定，摆脱对 search_path 的依赖
	if isUserTypeOID(col.TypeOID) {
		base = qualTypeName(tm, col.TypeOID)
	}
	return applyTypeMod(base, col.TypeOID, col.TypMod)
}

// applyTypeMod：按类型 OID 把 typmod 渲染成 SQL 类型修饰。
// 数组列复用同一份逻辑（传元素 OID），保证 varchar(10)[] 与 varchar(10) 一致。
func applyTypeMod(base string, oid uint32, typmod int) string {
	switch oid {
	case datatype.BPCHAROID, datatype.VARCHAROID:
		if typmod >= 4 {
			return fmt.Sprintf("%s(%d)", base, typmod-4)
		}
		return base
	case datatype.NUMERICOID:
		if typmod >= 4 {
			tmp := typmod - 4
			prec := (tmp >> 16) & 0xFFFF
			scale := tmp & 0xFFFF
			return fmt.Sprintf("numeric(%d,%d)", prec, scale)
		}
		return "numeric"
	case 1560, 1562: // bit/varbit
		if typmod != 0 {
			return fmt.Sprintf("%s(%d)", base, typmod)
		}
		return base
	// v1.0.46 修复（金仓 mysql 模式 DDL 丢长度）：binary/varbinary 的 typmod 与
	// varchar 同约定（长度+4），mysql BIT（oid 4655）typmod 即位数。
	// 旧实现落到默认分支，输出 "binary"/"bit"，而 mysql 模式 binary==binary(1)、
	// bit==bit(1)，回灌 16 字节二进制列/8 位 BIT 列直接报 value too long。
	case 3383, 3384: // 金仓 mysql binary / varbinary
		if typmod >= 4 {
			return fmt.Sprintf("%s(%d)", base, typmod-4)
		}
		return base
	case 4655: // 金仓 mysql BIT
		if typmod > 0 {
			return fmt.Sprintf("%s(%d)", base, typmod)
		}
		return base
	case datatype.TIMEOID, datatype.TIMETZOID, datatype.TIMESTAMPOID, datatype.TIMESTAMPTZOID:
		if typmod >= 0 {
			return fmt.Sprintf("%s(%d)", base, typmod)
		}
		return base
	}
	return base
}
