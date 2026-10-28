// pg2sql 完整 Schema DDL 导出（--full-schema + --schema-objects，v1.0.40）
// 输出：函数（pg_proc）、触发器（pg_trigger）、外键/CHECK 约束（pg_constraint）、
//
//	视图（pg_class relkind='v'：占位注释 + pg_depend 依赖清单）。
//
// 视图/CHECK 表达式为 pg_node_tree 序列化树（nodeToString），离线反编译不可行 →
// 输出占位注释 + 依赖清单（与用户确认的默认决策一致）。
package ddl

import (
	"encoding/binary"
	"fmt"
	"pg2sql/internal/catalog"
	"pg2sql/internal/datatype"
	"pg2sql/internal/engine"
	"pg2sql/internal/util"
	"sort"
	"strconv"
	"strings"
)

// ParseInt2Vector：int2vector 磁盘值 → int32 数组（2 字节元素）
// 内容（去 varlena 头）：[0:4) ndim=1 [4:8) dataoffset [8:12) elemtype [12:16) dim1 [16:20) lb [20:) 元素
func ParseInt2Vector(payload []byte) []int32 {
	if len(payload) < 20 {
		return nil
	}
	dim1 := int(binary.LittleEndian.Uint32(payload[12:16]))
	if dim1 < 0 || dim1 > 4000 {
		return nil
	}
	vals := payload[20:]
	out := make([]int32, 0, dim1)
	for i := 0; i < dim1 && i*2+2 <= len(vals); i++ {
		out = append(out, int32(int16(binary.LittleEndian.Uint16(vals[i*2:]))))
	}
	return out
}

// ParseOidVector：oidvector 磁盘值 → []uint32（4 字节元素）
// v1.0.56 修复（P2-G3）：proargtypes 是 oidvector（元素为 4 字节 Oid），
// 旧版复用 ParseInt2Vector 按 2 字节步长解析，第 2 个参数起全部错位
// （多数落空回退 "oid"），--full-schema 导出的多参函数签名错误、回灌失败。
// 头部布局与 int2vector 相同（20B），仅元素宽度不同。
func ParseOidVector(payload []byte) []uint32 {
	if len(payload) < 20 {
		return nil
	}
	dim1 := int(binary.LittleEndian.Uint32(payload[12:16]))
	if dim1 < 0 || dim1 > 4000 {
		return nil
	}
	vals := payload[20:]
	out := make([]uint32, 0, dim1)
	for i := 0; i < dim1 && i*4+4 <= len(vals); i++ {
		out = append(out, binary.LittleEndian.Uint32(vals[i*4:]))
	}
	return out
}

// sqlTypeName：typname → SQL 类型名（数组 "_xxx" → "xxx[]"）
func sqlTypeName(typname string) string {
	if typname == "" {
		return "oid"
	}
	if strings.HasPrefix(typname, "_") {
		inner := sqlTypeName(typname[1:])
		return inner + "[]"
	}
	return typname
}

// dollarQuote：选择不与 body 冲突的美元引号
func dollarQuote(body string) string {
	for i := 0; ; i++ {
		tag := "$pg2sql"
		if i > 0 {
			tag += strconv.Itoa(i)
		}
		tag += "$"
		if !strings.Contains(body, tag) {
			return tag
		}
	}
}

// SchemaCatalog：--full-schema 的目录读取上下文（一次读取，多次复用）
type SchemaCatalog struct {
	version    int
	isKB       bool
	dbDir      string
	nsMap      map[uint32]string
	classMap   map[uint32]*catalog.ClassRow // OID → pg_class 行
	classByRF  map[uint32]*catalog.ClassRow // relfilenode → 行（表空间自动发现）
	typeNames  map[uint32]string
	roleNames  map[uint32]string
	langNames  map[uint32]string // prolang OID → lanname
	ProcRows   []procRow
	TrigRows   []triggerRow
	ConstrRows []constrRow
	ViewRows   []viewRow
}

type procRow struct {
	OID        uint32
	Schema     string
	Name       string
	RetType    uint32
	ArgTypes   []uint32
	Language   uint32
	Volatile   byte
	IsStrict   bool
	SecDef     bool
	Body       string
	ConfigNote string
}

type triggerRow struct {
	OID        uint32
	Schema     string
	Table      string
	FuncSchema string
	Func       string
	Name       string
	TGType     uint16
	Enabled    byte
	Internal   bool
	ArgsNote   string
}

type constrRow struct {
	OID          uint32
	Schema       string
	Table        string
	Name         string
	Type         byte
	ConKey       []int32
	ConfRelID    uint32
	ConfKey      []int32
	UpdType      byte
	DelType      byte
	Defrable     bool
	Deferred     bool
	Validated    bool
	ColNames     map[int]string
	ConfColNames map[int]string
}

type viewRow struct {
	OID    uint32
	Schema string
	Name   string
	Deps   []string // 依赖对象（schema.name 形式，去重）
}

// OpenSchemaCatalog：构建目录上下文
func OpenSchemaCatalog(dbDir string, version int, isKB bool) (*SchemaCatalog, error) {
	sc := &SchemaCatalog{
		version: version, isKB: isKB, dbDir: dbDir,
		nsMap: map[uint32]string{}, classMap: map[uint32]*catalog.ClassRow{},
		classByRF: map[uint32]*catalog.ClassRow{}, typeNames: map[uint32]string{},
		roleNames: map[uint32]string{}, langNames: map[uint32]string{},
	}
	// namespace
	if p := engine.DetectSysFile(dbDir, catalog.PG_NAMESPACE_RELFILE, map[string]bool{"pg_namespace": true, "sys_namespace": true}, 0); p != "" {
		for tup := range engine.IterSysTuples(p, version, isKB) {
			oid, name := catalog.NsFields(tup, version)
			if name != "" {
				sc.nsMap[oid] = name
			}
		}
	}
	// pg_class 全表
	if p := engine.DetectSysFile(dbDir, catalog.PG_CLASS_RELFILE, map[string]bool{"pg_class": true, "sys_class": true}, 0); p != "" {
		for tup := range engine.IterSysTuples(p, version, isKB) {
			row := catalog.ClassFields(tup, version)
			if row != nil {
				sc.classMap[row.OID] = row
				if row.RelFileNode != 0 {
					sc.classByRF[row.RelFileNode] = row
				}
			}
		}
	}
	sc.typeNames = catalog.BuildTypeNameMap(dbDir)
	sc.roleNames = catalog.BuildRoleNameMap(dbDir)
	// pg_language（2612）：oid + lanname
	if p := engine.DetectSysFile(dbDir, 2612, map[string]bool{"pg_language": true, "sys_language": true}, 0); p != "" {
		for tup := range engine.IterSysTuples(p, version, isKB) {
			oid := uint32(0)
			if tup.THoff+4 <= len(tup.Raw) {
				oid = datatype.U32(tup.Raw, tup.THoff)
			}
			name := datatype.CString(tup.Raw, tup.THoff+4)
			if name != "" {
				sc.langNames[oid] = name
			}
		}
	}
	return sc, nil
}

func (sc *SchemaCatalog) relName(oid uint32) string {
	row, ok := sc.classMap[oid]
	if !ok {
		return ""
	}
	schema := sc.nsMap[row.RelNamespace]
	if schema == "" {
		schema = "public"
	}
	// v1.0.56 修复（P3-G9）：返回值直接拼进 FK 的 REFERENCES 与视图依赖清单，
	// 旧版不加引号，大写/空格/保留字表名生成非法 SQL。统一走 quoteIdent。
	return util.QuoteIdent(schema) + "." + util.QuoteIdent(row.RelName)
}

// userSchema：过滤系统 schema（只导出用户对象）
func (sc *SchemaCatalog) userSchema(oid uint32) string {
	ns := sc.nsMap[oid]
	switch ns {
	case "pg_catalog", "information_schema", "pg_toast", "sys_catalog", "":
		return ""
	}
	if strings.HasPrefix(ns, "pg_") || strings.HasPrefix(ns, "sys_") {
		return ""
	}
	return ns
}

// CollectProcs：pg_proc（1255）函数
func (sc *SchemaCatalog) CollectProcs() {
	p := engine.DetectSysFile(sc.dbDir, 1255, map[string]bool{"pg_proc": true, "sys_proc": true}, 0)
	if p == "" {
		return
	}
	cols := engine.NamedTableLayout(sc.dbDir, 1255, sc.version, sc.isKB)
	if len(cols) == 0 {
		return
	}
	for tup := range engine.IterSysTuples(p, sc.version, sc.isKB) {
		if !tup.IsLive() {
			continue
		}
		f, oid := engine.ParseNamedTuple(tup, cols, true, !sc.isKB)
		if oid == 0 {
			continue
		}
		schema := sc.userSchema(engine.FU32(f, "pronamespace"))
		if schema == "" {
			continue
		}
		row := procRow{
			OID: oid, Schema: schema, Name: engine.FCstr(f, "proname"),
			RetType: engine.FU32(f, "prorettype"), Language: engine.FU32(f, "prolang"),
			Volatile: engine.FByte(f, "provolatile"), IsStrict: engine.FBool(f, "proisstrict"),
			SecDef: engine.FBool(f, "prosecdef"),
		}
		if b, ok := f["proargtypes"]; ok {
			row.ArgTypes = ParseOidVector(datatype.VarlenaPayloadBytes(b))
		}
		if b, ok := f["prosrc"]; ok {
			row.Body = datatype.DecodeVarlenaText(b)
		}
		if b, ok := f["proconfig"]; ok && len(b) > 0 {
			row.ConfigNote = "（含 GUC 配置 proconfig，未导出）"
		}
		sc.ProcRows = append(sc.ProcRows, row)
	}
	sort.Slice(sc.ProcRows, func(i, j int) bool {
		if sc.ProcRows[i].Schema != sc.ProcRows[j].Schema {
			return sc.ProcRows[i].Schema < sc.ProcRows[j].Schema
		}
		return sc.ProcRows[i].Name < sc.ProcRows[j].Name
	})
}

// CollectTriggers：pg_trigger（2620）
func (sc *SchemaCatalog) CollectTriggers() {
	p := engine.DetectSysFile(sc.dbDir, 2620, map[string]bool{"pg_trigger": true, "sys_trigger": true}, 0)
	if p == "" {
		return
	}
	cols := engine.NamedTableLayout(sc.dbDir, 2620, sc.version, sc.isKB)
	if len(cols) == 0 {
		return
	}
	for tup := range engine.IterSysTuples(p, sc.version, sc.isKB) {
		if !tup.IsLive() {
			continue
		}
		f, oid := engine.ParseNamedTuple(tup, cols, true, !sc.isKB)
		if oid == 0 {
			continue
		}
		row := triggerRow{
			OID: oid, Name: engine.FCstr(f, "tgname"),
			TGType: engine.FU16(f, "tgtype"), Enabled: engine.FByte(f, "tgenabled"),
			Internal: engine.FBool(f, "tgisinternal"),
		}
		if row.Internal {
			continue
		}
		relOID := engine.FU32(f, "tgrelid")
		row.Schema, row.Table = sc.splitRelName(relOID)
		if row.Schema == "" {
			continue
		}
		fnOID := engine.FU32(f, "tgfoid")
		row.FuncSchema, row.Func = sc.splitProcName(fnOID)
		if b, ok := f["tgargs"]; ok && len(b) > 0 {
			row.ArgsNote = "（触发器参数未离线导出）"
		}
		sc.TrigRows = append(sc.TrigRows, row)
	}
	sort.Slice(sc.TrigRows, func(i, j int) bool {
		if sc.TrigRows[i].Schema != sc.TrigRows[j].Schema {
			return sc.TrigRows[i].Schema < sc.TrigRows[j].Schema
		}
		return sc.TrigRows[i].Table < sc.TrigRows[j].Table
	})
}

// CollectConstraints：pg_constraint（2606）FK + CHECK
func (sc *SchemaCatalog) CollectConstraints() {
	p := engine.DetectSysFile(sc.dbDir, 2606, map[string]bool{"pg_constraint": true, "sys_constraint": true}, 0)
	if p == "" {
		return
	}
	cols := engine.NamedTableLayout(sc.dbDir, 2606, sc.version, sc.isKB)
	if len(cols) == 0 {
		return
	}
	for tup := range engine.IterSysTuples(p, sc.version, sc.isKB) {
		if !tup.IsLive() {
			continue
		}
		// PG12+/V9 的 pg_constraint 无 oid 系统列 → 字段判据
		f, _ := engine.ParseNamedTuple(tup, cols, false, false)
		if len(f) == 0 {
			continue
		}
		typ := engine.FByte(f, "contype")
		if typ != 'f' && typ != 'c' {
			continue
		}
		relOID := engine.FU32(f, "conrelid")
		schema, table := sc.splitRelName(relOID)
		if schema == "" {
			continue
		}
		row := constrRow{
			OID: 0, Schema: schema, Table: table, Name: engine.FCstr(f, "conname"),
			Type: typ, ConfRelID: engine.FU32(f, "confrelid"),
			UpdType: engine.FByte(f, "confupdtype"), DelType: engine.FByte(f, "confdeltype"),
			Defrable: engine.FBool(f, "condeferrable"), Deferred: engine.FBool(f, "condeferred"),
			Validated: engine.FBool(f, "convalidated"),
		}
		if b, ok := f["conkey"]; ok {
			row.ConKey = ParseInt2Vector(datatype.VarlenaPayloadBytes(b))
		}
		if b, ok := f["confkey"]; ok {
			row.ConfKey = ParseInt2Vector(datatype.VarlenaPayloadBytes(b))
		}
		row.ColNames = sc.colNameMap(relOID)
		if typ == 'f' {
			row.ConfColNames = sc.colNameMap(row.ConfRelID)
		}
		sc.ConstrRows = append(sc.ConstrRows, row)
	}
	sort.Slice(sc.ConstrRows, func(i, j int) bool {
		if sc.ConstrRows[i].Schema != sc.ConstrRows[j].Schema {
			return sc.ConstrRows[i].Schema < sc.ConstrRows[j].Schema
		}
		if sc.ConstrRows[i].Table != sc.ConstrRows[j].Table {
			return sc.ConstrRows[i].Table < sc.ConstrRows[j].Table
		}
		return sc.ConstrRows[i].Name < sc.ConstrRows[j].Name
	})
}

// CollectViews：视图（relkind='v'）+ pg_depend 依赖清单
func (sc *SchemaCatalog) CollectViews() {
	for _, row := range sc.classMap {
		if row.RelKind != "v" && row.RelKind != "m" {
			continue
		}
		schema := sc.userSchema(row.RelNamespace)
		if schema == "" {
			continue
		}
		v := viewRow{OID: row.OID, Schema: schema, Name: row.RelName}
		sc.ViewRows = append(sc.ViewRows, v)
	}
	if len(sc.ViewRows) == 0 {
		return
	}
	// pg_depend(2608)：无 oid 系统列。视图依赖直连 pg_depend.classid=1259(pg_class)
	// 且 objid=视图OID 的行 → refobjid 即依赖对象（规避 pg_rewrite 无 oid 无法反查）。
	viewSet := map[uint32]bool{}
	for _, v := range sc.ViewRows {
		viewSet[v.OID] = true
	}
	if p := engine.DetectSysFile(sc.dbDir, 2608, map[string]bool{"pg_depend": true, "sys_depend": true}, 0); p != "" {
		for tup := range engine.IterSysTuples(p, sc.version, sc.isKB) {
			if !tup.IsLive() {
				continue
			}
			f, _ := engine.ParseNamedTuple(tup, []engine.NamedCol{
				{Name: "classid", AttNum: 1, ColLen: datatype.ColLen{AttLen: 4, IsVarlena: false, AttAlign: "i"}},
				{Name: "objid", AttNum: 2, ColLen: datatype.ColLen{AttLen: 4, IsVarlena: false, AttAlign: "i"}},
				{Name: "objsubid", AttNum: 3, ColLen: datatype.ColLen{AttLen: 4, IsVarlena: false, AttAlign: "i"}},
				{Name: "refclassid", AttNum: 4, ColLen: datatype.ColLen{AttLen: 4, IsVarlena: false, AttAlign: "i"}},
				{Name: "refobjid", AttNum: 5, ColLen: datatype.ColLen{AttLen: 4, IsVarlena: false, AttAlign: "i"}},
				{Name: "refobjsubid", AttNum: 6, ColLen: datatype.ColLen{AttLen: 4, IsVarlena: false, AttAlign: "i"}},
				{Name: "deptype", AttNum: 7, ColLen: datatype.ColLen{AttLen: 1, IsVarlena: false, AttAlign: "c"}},
			}, false, false)
			if len(f) == 0 || engine.FU32(f, "classid") != 1259 {
				continue
			}
			viewOID := engine.FU32(f, "objid")
			if !viewSet[viewOID] {
				continue
			}
			if engine.FU32(f, "refclassid") != 1259 {
				continue
			}
			depName := sc.relName(engine.FU32(f, "refobjid"))
			if depName == "" {
				continue
			}
			for i := range sc.ViewRows {
				if sc.ViewRows[i].OID == viewOID {
					sc.ViewRows[i].Deps = append(sc.ViewRows[i].Deps, depName)
					break
				}
			}
		}
	}
	sort.Slice(sc.ViewRows, func(i, j int) bool {
		if sc.ViewRows[i].Schema != sc.ViewRows[j].Schema {
			return sc.ViewRows[i].Schema < sc.ViewRows[j].Schema
		}
		return sc.ViewRows[i].Name < sc.ViewRows[j].Name
	})
	// 依赖去重排序
	for i := range sc.ViewRows {
		seen := map[string]bool{}
		var deps []string
		for _, d := range sc.ViewRows[i].Deps {
			if !seen[d] {
				seen[d] = true
				deps = append(deps, d)
			}
		}
		sort.Strings(deps)
		sc.ViewRows[i].Deps = deps
	}
}

// splitRelName：按 OID 取 (schema, relname)
func (sc *SchemaCatalog) splitRelName(oid uint32) (string, string) {
	row, ok := sc.classMap[oid]
	if !ok {
		return "", ""
	}
	schema := sc.nsMap[row.RelNamespace]
	if schema == "" {
		schema = "public"
	}
	return schema, row.RelName
}

// splitProcName：按函数 OID 取 (schema, 函数名)；名称同签名不可得，返回基础名
func (sc *SchemaCatalog) splitProcName(oid uint32) (string, string) {
	for _, pr := range sc.ProcRows {
		if pr.OID == oid {
			return pr.Schema, pr.Name
		}
	}
	// 回退：函数名 = 无 schema 前缀（触发器中由调用方补 schema）
	return "public", fmt.Sprintf("oid_%d", oid)
}

// colNameMap：表 OID → attnum → 列名
func (sc *SchemaCatalog) colNameMap(relOID uint32) map[int]string {
	cols := engine.NamedTableLayout(sc.dbDir, relOID, sc.version, sc.isKB)
	out := map[int]string{}
	for _, c := range cols {
		out[c.AttNum] = c.Name
	}
	return out
}

// ---------- 输出 ----------

// tgTiming：tgtype 时机
func tgTiming(t uint16) string {
	if t&0x0040 != 0 {
		return "INSTEAD OF"
	}
	if t&0x0002 != 0 {
		return "BEFORE"
	}
	return "AFTER"
}

// tgEvents：tgtype 事件（可组合）
func tgEvents(t uint16) string {
	var evs []string
	if t&0x0004 != 0 {
		evs = append(evs, "INSERT")
	}
	if t&0x0008 != 0 {
		evs = append(evs, "DELETE")
	}
	if t&0x0010 != 0 {
		evs = append(evs, "UPDATE")
	}
	if t&0x0020 != 0 {
		evs = append(evs, "TRUNCATE")
	}
	if len(evs) == 0 {
		evs = append(evs, "INSERT")
	}
	return strings.Join(evs, " OR ")
}

// fkAction：confupdtype/confdeltype 动作
func fkAction(t byte) string {
	switch t {
	case 'a':
		return "NO ACTION"
	case 'r':
		return "RESTRICT"
	case 'c':
		return "CASCADE"
	case 'n':
		return "SET NULL"
	case 'd':
		return "SET DEFAULT"
	}
	return "NO ACTION"
}

// GenFullSchemaDDL：生成完整 Schema DDL 文本（按 --schema-objects 过滤）
func GenFullSchemaDDL(sc *SchemaCatalog, filter map[string]bool) string {
	var sb strings.Builder
	w := func(s string) { sb.WriteString(s) }
	if len(filter) == 0 {
		filter = map[string]bool{"view": true, "function": true, "trigger": true, "constraint": true, "fk": true}
	}
	// 函数
	if filter["function"] && len(sc.ProcRows) > 0 {
		w("--\n-- 函数（pg_proc）\n--\n")
		for _, p := range sc.ProcRows {
			args := make([]string, 0, len(p.ArgTypes))
			for _, a := range p.ArgTypes {
				args = append(args, sqlTypeName(sc.typeNames[a]))
			}
			ret := sqlTypeName(sc.typeNames[p.RetType])
			lang := sc.langNames[p.Language]
			if lang == "" {
				lang = "sql"
			}
			q := dollarQuote(p.Body)
			var flags []string
			switch p.Volatile {
			case 'i':
				flags = append(flags, "IMMUTABLE")
			case 's':
				flags = append(flags, "STABLE")
			default:
				flags = append(flags, "VOLATILE")
			}
			if p.IsStrict {
				flags = append(flags, "STRICT")
			}
			if p.SecDef {
				flags = append(flags, "SECURITY DEFINER")
			}
			w(fmt.Sprintf("CREATE OR REPLACE FUNCTION %s.%s(%s) RETURNS %s\n    LANGUAGE %s\n    %s\nAS %s\n%s\n%s;\n\n",
				util.QuoteIdent(p.Schema), util.QuoteIdent(p.Name), strings.Join(args, ", "), ret,
				util.QuoteIdent(lang), strings.Join(flags, " "), q, p.Body, q))
		}
	}
	// 触发器
	if filter["trigger"] && len(sc.TrigRows) > 0 {
		w("--\n-- 触发器（pg_trigger）\n--\n")
		for _, t := range sc.TrigRows {
			fnTarget := t.Func
			if t.FuncSchema != "" && t.FuncSchema != "public" {
				fnTarget = util.QuoteIdent(t.FuncSchema) + "." + util.QuoteIdent(t.Func)
			}
			w(fmt.Sprintf("CREATE TRIGGER %s %s %s ON %s FOR EACH %s EXECUTE FUNCTION %s();\n",
				util.QuoteIdent(t.Name), tgTiming(t.TGType), tgEvents(t.TGType),
				util.QuoteIdent(t.Schema)+"."+util.QuoteIdent(t.Table),
				map[bool]string{true: "ROW", false: "STATEMENT"}[t.TGType&0x0001 != 0],
				fnTarget))
			if t.Enabled != 'O' {
				w(fmt.Sprintf("-- 注: 原触发器状态 enabled=%c（非 'O'），需按需 ALTER TABLE %s DISABLE TRIGGER %s;\n",
					t.Enabled, util.QuoteIdent(t.Schema)+"."+util.QuoteIdent(t.Table), util.QuoteIdent(t.Name)))
			}
			if t.ArgsNote != "" {
				w("-- 注: " + t.ArgsNote + "\n")
			}
			w("\n")
		}
	}
	// 约束（FK + CHECK）
	if filter["constraint"] || filter["fk"] {
		w("--\n-- 约束（pg_constraint: 外键/CHECK）\n--\n")
		for _, c := range sc.ConstrRows {
			if c.Type == 'f' {
				if !filter["fk"] && !filter["constraint"] {
					continue
				}
				var cols, refCols []string
				for _, k := range c.ConKey {
					cols = append(cols, util.QuoteIdent(c.ColNames[int(k)]))
				}
				for _, k := range c.ConfKey {
					refCols = append(refCols, util.QuoteIdent(c.ConfColNames[int(k)]))
				}
				ref := sc.relName(c.ConfRelID)
				act := ""
				if c.UpdType != 'a' {
					act += " ON UPDATE " + fkAction(c.UpdType)
				}
				if c.DelType != 'a' {
					act += " ON DELETE " + fkAction(c.DelType)
				}
				def := ""
				if c.Defrable {
					def = " DEFERRABLE"
					if c.Deferred {
						def += " INITIALLY DEFERRED"
					}
				}
				notVal := ""
				if !c.Validated {
					notVal = " NOT VALID"
				}
				w(fmt.Sprintf("ALTER TABLE ONLY %s\n    ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s (%s)%s%s%s;\n\n",
					util.QuoteIdent(c.Schema)+"."+util.QuoteIdent(c.Table), util.QuoteIdent(c.Name),
					strings.Join(cols, ", "), ref, strings.Join(refCols, ", "), act, def, notVal))
			} else if c.Type == 'c' && filter["constraint"] {
				w(fmt.Sprintf("-- CHECK 约束 %s (oid %d): 表达式为 pg_node_tree 序列化树，离线无法反编译。\n--   如需完整定义，请使用在线 pg_dump --schema-only（同库建表语句）。\n\n",
					util.QuoteIdent(c.Schema)+"."+util.QuoteIdent(c.Table)+"."+util.QuoteIdent(c.Name), c.OID))
			}
		}
	}
	// 视图（占位 + 依赖清单）
	if filter["view"] && len(sc.ViewRows) > 0 {
		w("--\n-- 视图（pg_class relkind='v'）：定义不可离线反编译，输出依赖清单\n--\n")
		for _, v := range sc.ViewRows {
			w(fmt.Sprintf("-- 视图 %s (oid %d)\n", util.QuoteIdent(v.Schema)+"."+util.QuoteIdent(v.Name), v.OID))
			if len(v.Deps) == 0 {
				w("--   依赖: （无/不可解析）\n")
			} else {
				w("--   依赖: " + strings.Join(v.Deps, ", ") + "\n")
			}
			w("-- 如需完整视图 DDL，请使用在线 pg_dump --schema-only\n\n")
		}
	}
	return sb.String()
}

// ParseSchemaObjectFilter：解析 --schema-objects 逗号列表（v1.0.56 P3-G10）
// 非法取值直接报错（旧版静默落空 → 输出仅剩头注释且 exit 0）
func ParseSchemaObjectFilter(s string, base map[string]bool) (map[string]bool, error) {
	known := map[string]bool{"view": true, "function": true, "trigger": true, "constraint": true, "fk": true}
	out := base
	for _, k := range strings.Split(s, ",") {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if !known[k] {
			return nil, fmt.Errorf("--schema-objects 含非法取值 %q（合法值: view,function,trigger,constraint,fk）", k)
		}
		out[k] = true
	}
	return out, nil
}
