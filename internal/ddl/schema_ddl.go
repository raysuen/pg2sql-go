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

// ParseCharArray：char[] 磁盘值（已剥 varlena 头）→ 字节数组（元素 1 字节）。
// v1.0.61（P2-9）：proargmodes 是 char[]，头部布局与 int2vector/oidvector 相同
// （占 20B），只是元素宽度 1 字节。
func ParseCharArray(payload []byte) []byte {
	if len(payload) < 20 {
		return nil
	}
	dim1 := int(binary.LittleEndian.Uint32(payload[12:16]))
	if dim1 < 0 || dim1 > 4000 {
		return nil
	}
	vals := payload[20:]
	if dim1 > len(vals) {
		dim1 = len(vals)
	}
	out := make([]byte, dim1)
	copy(out, vals[:dim1])
	return out
}

// ParseTextArray：text[] 磁盘值（已剥 varlena 头）→ 字符串切片。
// v1.0.63 修复（A-1）：proargnames 是 text[]，用于还原 RETURNS TABLE 的列名；
// PG 的语法要求 RETURNS TABLE (列名 类型)，只给类型是非法 SQL。
// 元素是 varlena（可能 1B/4B 头），元素间按 INTALIGN(4) 对齐；dataoffset 非 0
// 时 20..dataoffset 之间是 NULL 位图。未命名的参数在 proargnames 里是空串。
func ParseTextArray(payload []byte) []string {
	if len(payload) < 20 {
		return nil
	}
	if ndim := int(int32(binary.LittleEndian.Uint32(payload[0:4]))); ndim != 1 {
		return nil
	}
	dataOff := int(binary.LittleEndian.Uint32(payload[4:8]))
	dim1 := int(int32(binary.LittleEndian.Uint32(payload[12:16])))
	if dim1 < 0 || dim1 > 4000 {
		return nil
	}
	pos := 20
	if dataOff > 20 && dataOff < len(payload) {
		pos = dataOff
	}
	out := make([]string, 0, dim1)
	for i := 0; i < dim1; i++ {
		// NULL 位图：位序与元素顺序一致
		if dataOff > 20 && 20+i/8 < len(payload) && payload[20+i/8]&(1<<uint(i%8)) != 0 {
			out = append(out, "")
			continue
		}
		if pos < 0 || pos >= len(payload) {
			out = append(out, "")
			continue
		}
		kind, total, _, _ := datatype.VarlenaParse(payload, pos)
		if kind == "" || total < 1 || pos+total > len(payload) {
			out = append(out, "")
			pos = len(payload)
			continue
		}
		out = append(out, strings.TrimRight(datatype.DecodeVarlenaText(payload[pos:pos+total]), "\x00"))
		next := pos + total
		// 元素按 4 字节对齐；先探测对齐位置是否能解析出合法 varlena 头再前移，
		// 避免把 1B 头元素的紧凑排列误对齐（proargnames 两种都存在）
		if aligned := (next + 3) &^ 3; aligned > next && aligned < len(payload) {
			if k2, _, _, _ := datatype.VarlenaParse(payload, aligned); k2 != "" {
				next = aligned
			}
		}
		pos = next
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

// ddlUserTypeOID：该 oid 是否为枚举/域/复合等用户自定义类型。
func ddlUserTypeOID(oid uint32) bool {
	if datatype.DynamicTypeDefs == nil {
		return false
	}
	td, ok := datatype.DynamicTypeDefs[oid]
	if !ok {
		return false
	}
	return td.Type == 'e' || td.Type == 'd' || td.Type == 'c'
}

// isPlainIdent：是否只含 [A-Za-z0-9_]（可裸输出、无需引号）。
func isPlainIdent(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' {
			continue
		}
		return false
	}
	return true
}

// procTypeName：函数签名/返回类型用的类型名（v1.0.61 修复 P1-8）。
// 旧实现只用 pg_type.typname 裸名：自定义类型（枚举/域/复合）既不加 schema 限定
// 也不加引号，回灌时按 search_path 解析 → 跨 schema 同名类型静默绑错，
// 名字含大写/空格/保留字时直接语法错误；typname 本身还可能注入任意 SQL。
// 现在：用户自定义类型走 catalog.TypeSchemaName + QuoteIdent，数组输出 "elem[]"；
// 内置类型保持裸名（varchar/int4 等，加引号反而会改变类型解析）。
func procTypeName(sc *SchemaCatalog, oid uint32) string {
	if oid == 0 {
		return "oid"
	}
	// 数组：元素类型自定义时输出 schema 限定的 "elem"[]
	if eo, ok := datatype.UserArrayElemOID(oid); ok {
		return procTypeName(sc, eo) + "[]"
	}
	return procScalarTypeName(sc, oid)
}

// procScalarTypeName：非数组类型的 SQL 名（用户自定义类型加 schema + 引号）。
func procScalarTypeName(sc *SchemaCatalog, oid uint32) string {
	raw := typeNameRaw(sc, oid)
	if ddlUserTypeOID(oid) {
		if sch := catalog.TypeSchemaName(oid); sch != "" {
			return util.QuoteIdent(sch) + "." + util.QuoteIdent(raw)
		}
		// 拿不到 typnamespace（元数据缺失）时退回裸引号名，保证语法可解析
		return util.QuoteIdent(raw)
	}
	base := sqlTypeName(raw) // "_xxx" → "xxx[]"
	// 内置类型：正常标识符裸输出（加引号会改变类型解析，如 "character varying"）
	if isPlainIdent(base) {
		return base
	}
	// 数组内部名兜底（typarray 未互指时拿不到元素 oid）：只给元素名加引号
	if strings.HasSuffix(base, "[]") {
		if inner := strings.TrimSuffix(base, "[]"); !isPlainIdent(inner) {
			return util.QuoteIdent(inner) + "[]"
		}
		return base
	}
	return util.QuoteIdent(base) // 防 typname 注入
}

// typeNameRaw：oid → pg_type.typname（数组内部名 "_xxx" 换成 "xxx[]" 之前的原始名）
func typeNameRaw(sc *SchemaCatalog, oid uint32) string {
	if sc != nil {
		if n := sc.typeNames[oid]; n != "" {
			return n
		}
	}
	return ""
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
	version   int
	isKB      bool
	dbDir     string
	nsMap     map[uint32]string
	classMap  map[uint32]*catalog.ClassRow // OID → pg_class 行
	classByRF map[uint32]*catalog.ClassRow // relfilenode → 行（表空间自动发现）
	typeNames map[uint32]string
	roleNames map[uint32]string
	langNames map[uint32]string // prolang OID → lanname
	// v1.0.63 第 4 轮（D-1）：扩展成员对象。扩展装进 public 后其函数/视图/触发器
	// 全部落在用户 schema，被当成自建对象导出 → 回灌时 LANGUAGE c 的 AS 段被当作
	// 共享库路径，报 could not access file。目标端 CREATE EXTENSION 即可装齐，
	// 故这类对象一律跳过，产物里注明。
	extMembers map[catalog.ExtKey]bool
	extNames   []string
	ProcRows   []procRow
	TrigRows   []triggerRow
	ConstrRows []constrRow
	ViewRows   []viewRow
	// v1.0.63（N-2）：独立序列（无任何列 DEFAULT nextval 引用的序列）此前
	// 在任何导出路径都不出现，整库搬迁后 nextval('seq') 直接 relation does not exist
	SeqRows []*catalog.SequenceInfo
}

type procRow struct {
	OID      uint32
	Schema   string
	Name     string
	RetType  uint32
	ArgTypes []uint32
	Language uint32
	Volatile byte
	IsStrict bool
	SecDef   bool
	Body     string
	// v1.0.63 第 4 轮（D-2）：probin —— C 语言函数的共享库路径。
	// LANGUAGE c 的 AS 段是「'共享库', '符号名'」两部分：只写符号名时 PG 把
	// 它当库路径去找文件，回灌报 could not access file "xxx"。
	ProBin     string
	ConfigNote string
	// v1.0.61 修复（P2-9）：补全把函数变成"普通函数"的语义字段。
	Kind     byte   // prokind：'f' 普通 'p' 存储过程 'a' 聚合 'w' 窗口（PG11+；旧版由 proisagg/proiswindow 推）
	RetSet   bool   // proretset：返回集合 → RETURNS SETOF
	ArgModes []byte // proargmodes：每个参数的模式字符 i/o/b/v/t（空=全 IN）
	// v1.0.63 修复（A-1）：proargnames（text[]，与 ArgTypes 同序）——RETURNS TABLE
	// 必须输出「列名 类型」，此前全代码库没有读取点，TABLE 函数整段 DDL 语法非法。
	ArgNames []string
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
		extMembers: map[catalog.ExtKey]bool{},
	}
	// 扩展成员集合（v1.0.63 D-1）：读不到 pg_depend 时为空，行为与旧版一致
	sc.extMembers = catalog.ExtensionMembers(dbDir, version, isKB)
	sc.extNames = catalog.ExtensionNames(dbDir, version, isKB)
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

// extMember：该（系统目录, 对象 OID）是否为某个扩展的成员对象。
// pg_depend 未读到（老目录/坏块）时 extMembers 为空 → 一律返回 false，保持旧行为。
func (sc *SchemaCatalog) extMember(classID, objID uint32) bool {
	if len(sc.extMembers) == 0 {
		return false
	}
	return sc.extMembers[catalog.ExtKey{ClassID: classID, ObjID: objID}]
}

// procAsClause：函数的 AS 子句。
//
// v1.0.63 第 4 轮（D-2）：LANGUAGE c 的 AS 段是「'共享库', '符号名'」两个字符串——
// probin 是共享库路径、prosrc 是符号名。只写符号名时 PG 把它当库文件名去找，
// 回灌报 could not access file "brin_metapage_info": No such file or directory。
// 其它语言（sql/plpgsql/internal）的 AS 段就是函数体本身，用美元引号包起来。
func procAsClause(lang, probin, body, name, q string) string {
	if lang != "c" {
		return q + "\n" + body + "\n" + q
	}
	sym := body
	if sym == "" {
		sym = name
	}
	return "'" + strings.ReplaceAll(probin, "'", "''") + "', '" +
		strings.ReplaceAll(sym, "'", "''") + "'"
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
		// v1.0.63 第 4 轮（D-1）：扩展自带的函数不导出（目标端 CREATE EXTENSION 即可）。
		// 导出的 LANGUAGE c 函数缺少 probin，回灌必然 could not access file。
		if sc.extMember(catalog.ExtClassProc, oid) {
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
		// v1.0.61（P2-9）：prokind（PG11+）。金仓 V8/PG10 无此列 → 由
		// proisagg/proiswindow 两个旧 bool 推回，否则一律按普通函数处理。
		row.Kind = 'f'
		if k := engine.FByte(f, "prokind"); k != 0 {
			row.Kind = k
		} else if engine.FBool(f, "proisagg") {
			row.Kind = 'a'
		} else if engine.FBool(f, "proiswindow") {
			row.Kind = 'w'
		}
		row.RetSet = engine.FBool(f, "proretset")
		if b, ok := f["proargmodes"]; ok && len(b) > 0 {
			row.ArgModes = ParseCharArray(datatype.VarlenaPayloadBytes(b))
		}
		if b, ok := f["proargtypes"]; ok {
			row.ArgTypes = ParseOidVector(datatype.VarlenaPayloadBytes(b))
		}
		// v1.0.63 修复（A-1）：proallargtypes 是【全部】参数（含 OUT/TABLE/INOUT），
		// proargtypes 只有 IN。只读后者时 proargmodes 比 ArgTypes 长，TABLE 参数
		// 整个缺席 → RETURNS TABLE 退化成 RETURNS SETOF record。
		if b, ok := f["proallargtypes"]; ok && len(b) > 0 {
			if all := ParseOidVector(datatype.VarlenaPayloadBytes(b)); len(all) > 0 {
				row.ArgTypes = all
			}
		}
		if b, ok := f["proargnames"]; ok && len(b) > 0 {
			row.ArgNames = ParseTextArray(datatype.VarlenaPayloadBytes(b))
		}
		if b, ok := f["prosrc"]; ok {
			row.Body = datatype.DecodeVarlenaText(b)
		}
		// v1.0.63 第 4 轮（D-2）：probin（C 函数的共享库）
		row.ProBin = engine.FCstr(f, "probin")
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
		// v1.0.63 第 4 轮（D-1）：扩展自带的触发器（及其函数）由 CREATE EXTENSION 重建
		if sc.extMember(catalog.ExtClassTrigger, oid) {
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
		// v1.0.63 第 4 轮（D-1）：扩展自带的视图（pg_stat_statements 等）不导出
		if sc.extMember(catalog.ExtClassClass, row.OID) {
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

// CollectSequences：收集库内全部序列（v1.0.63 N-2）。
// 走 catalog.CollectSequences（pg_class + pg_sequence + 序列关系 + pg_depend），
// 逐表 DDL 只会输出"被列引用"的序列，独立序列必须在这里兜住。
func (sc *SchemaCatalog) CollectSequences() {
	all := catalog.SortedSequences(catalog.CollectSequences(sc.dbDir, sc.version, sc.isKB))
	// v1.0.63 第 4 轮（D-1 延伸）：扩展自带的序列（金仓 sysmac/sys_hm 等）会随
	// CREATE EXTENSION 自动创建，且宿主 schema 在目标端可能尚不存在 →
	// 单独 CREATE SEQUENCE 会报 schema does not exist。一并跳过。
	if len(sc.extMembers) == 0 {
		sc.SeqRows = all
		return
	}
	for _, si := range all {
		if sc.extMember(catalog.ExtClassClass, si.OID) {
			continue
		}
		sc.SeqRows = append(sc.SeqRows, si)
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
		filter = map[string]bool{"view": true, "function": true, "trigger": true, "constraint": true, "fk": true, "sequence": true}
	}
	// v1.0.63 第 4 轮（D-1）：扩展成员一律不导出，这里统一注明（与是否有函数无关，
	// 否则扩展全被过滤后使用者会以为是漏导）。
	if len(sc.extNames) > 0 {
		w("--\n-- 扩展（pg_extension）\n--\n")
		w("-- 注: 扩展 " + strings.Join(sc.extNames, ", ") +
			" 的成员对象（函数/视图/触发器）未导出，请在目标端先执行 CREATE EXTENSION。\n\n")
	}
	// v1.0.63 修复（N-2）：序列段放最前面（无依赖），
	// 独立序列（无任何列 DEFAULT nextval 引用）只有这里会输出。
	if filter["sequence"] && len(sc.SeqRows) > 0 {
		w("--\n-- 序列（pg_class relkind='S' + pg_sequence，v1.0.63 起含独立序列与完整选项）\n--\n")
		w("-- 注意：本段必须在各表的建表 DDL 之后执行——IDENTITY 列的序列由 CREATE TABLE 创建，\n-- 此处只补 setval 同步当前值。\n")
		for _, si := range sc.SeqRows {
			if si.Internal {
				// IDENTITY 列的内部序列：随 CREATE TABLE ... GENERATED AS IDENTITY
				// 自动重建，单独 CREATE 会报 relation already exists；只补当前值。
				if s := si.SetvalSQL(util.QuoteIdent, datatype.SQLStringLiteral); s != "" {
					w("-- 序列 " + util.QuoteIdent(si.Schema) + "." + util.QuoteIdent(si.Name) +
						" 由 IDENTITY 列自动创建，仅同步当前值\n")
					w(s + "\n")
				}
				continue
			}
			w(si.CreateSQL(util.QuoteIdent) + "\n")
			if s := si.SetvalSQL(util.QuoteIdent, datatype.SQLStringLiteral); s != "" {
				w(s + "\n")
			}
		}
		w("\n")
	}
	// 函数
	if filter["function"] && len(sc.ProcRows) > 0 {
		w("--\n-- 函数（pg_proc）\n--\n")
		for _, p := range sc.ProcRows {
			// v1.0.61 修复（P2-9）：聚合/窗口函数此前被当作普通函数输出
			//（CREATE FUNCTION 回灌后变成一个同名的普通 SQL 函数，行为完全不同），
			// 离线拿不到 pg_aggregate 的 transfn/finalfn，只能跳过并明示。
			if p.Kind == 'a' || p.Kind == 'w' {
				kindName := "聚合函数"
				if p.Kind == 'w' {
					kindName = "窗口函数"
				}
				w(fmt.Sprintf("-- 注: %s.%s 是%s（prokind=%c），需 pg_aggregate/pg_window 才能还原，本工具不导出\n\n",
					util.QuoteIdent(p.Schema), util.QuoteIdent(p.Name), kindName, p.Kind))
				continue
			}
			args := make([]string, 0, len(p.ArgTypes))
			var tableCols []string
			hasOut, hasTable := false, false
			tableIdx := 0
			for i, a := range p.ArgTypes {
				mode := byte('i')
				if i < len(p.ArgModes) {
					mode = p.ArgModes[i]
				}
				tn := procTypeName(sc, a)
				// v1.0.63 修复（A-1）：proargnames 与 ArgTypes 同序（含 OUT/TABLE）。
				// RETURNS TABLE 的列定义里列名是必填项；proargnames 该位为空
				// （未命名 TABLE 参数）时按 PG 自身约定回退 column1..N，
				// 否则整段 DDL 报 syntax error at or near ")"。
				argName := ""
				if i < len(p.ArgNames) {
					argName = strings.TrimSpace(p.ArgNames[i])
				}
				switch mode {
				case 'o':
					hasOut = true
					if argName != "" {
						args = append(args, "OUT "+util.QuoteIdent(argName)+" "+tn)
					} else {
						args = append(args, "OUT "+tn)
					}
				case 'b':
					hasOut = true
					if argName != "" {
						args = append(args, "INOUT "+util.QuoteIdent(argName)+" "+tn)
					} else {
						args = append(args, "INOUT "+tn)
					}
				case 'v':
					args = append(args, "VARIADIC "+tn)
				case 't':
					// TABLE 模式：只出现在 RETURNS TABLE 列表里
					hasOut, hasTable = true, true
					tableIdx++
					if argName == "" {
						argName = fmt.Sprintf("column%d", tableIdx)
					}
					tableCols = append(tableCols, util.QuoteIdent(argName)+" "+tn)
				default:
					args = append(args, tn)
				}
			}
			ret := procTypeName(sc, p.RetType)
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
			// 返回子句：TABLE > OUT 参数（此时不得写 RETURNS） > 普通/SETOF
			retClause := ""
			switch {
			case hasTable:
				retClause = " RETURNS TABLE (" + strings.Join(tableCols, ", ") + ")"
			case hasOut:
				// 有 OUT/INOUT 参数时 PG 禁止写 RETURNS，返回类型由 OUT 参数决定
			case p.Kind == 'p':
				// 存储过程没有返回值子句
			case p.RetSet:
				retClause = " RETURNS SETOF " + ret
			default:
				retClause = " RETURNS " + ret
			}
			// v1.0.61（P2-9）：存储过程用 CREATE PROCEDURE（此前统一 CREATE
			// FUNCTION，回灌报错或建出错误对象）；返回子句按 TABLE/OUT/SETOF 分流。
			kind := "FUNCTION"
			if p.Kind == 'p' {
				kind = "PROCEDURE"
			}
			// v1.0.63 第 4 轮（D-2）：C 函数的 AS 段必须是「'共享库', '符号名'」。
			asBody := procAsClause(lang, p.ProBin, p.Body, p.Name, q)
			w(fmt.Sprintf("CREATE OR REPLACE %s %s.%s(%s)%s\n    LANGUAGE %s\n    %s\nAS %s;\n\n",
				kind, util.QuoteIdent(p.Schema), util.QuoteIdent(p.Name), strings.Join(args, ", "), retClause,
				util.QuoteIdent(lang), strings.Join(flags, " "), asBody))
		}
	}
	// 触发器
	if filter["trigger"] && len(sc.TrigRows) > 0 {
		w("--\n-- 触发器（pg_trigger）\n--\n")
		for _, t := range sc.TrigRows {
			// v1.0.63 修复（B-12）：函数名一律加引号。旧实现在 schema=="public"
			// 时裸输出 t.Func，函数名含大写/特殊字符时 'MyTrig' 被解析器折成
			// mytrig → 回灌报 function mytrig() does not exist。
			fnTarget := util.QuoteIdent(t.Func)
			if t.FuncSchema != "" {
				fnTarget = util.QuoteIdent(t.FuncSchema) + "." + fnTarget
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
	known := map[string]bool{"view": true, "function": true, "trigger": true, "constraint": true, "fk": true, "sequence": true}
	out := base
	for _, k := range strings.Split(s, ",") {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if !known[k] {
			return nil, fmt.Errorf("--schema-objects 含非法取值 %q（合法值: view,function,trigger,constraint,fk,sequence）", k)
		}
		out[k] = true
	}
	return out, nil
}
