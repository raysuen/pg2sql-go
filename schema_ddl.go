// pg2sql 完整 Schema DDL 导出（--full-schema + --schema-objects，v1.0.40）
// 输出：函数（pg_proc）、触发器（pg_trigger）、外键/CHECK 约束（pg_constraint）、
//
//	视图（pg_class relkind='v'：占位注释 + pg_depend 依赖清单）。
//
// 视图/CHECK 表达式为 pg_node_tree 序列化树（nodeToString），离线反编译不可行 →
// 输出占位注释 + 依赖清单（与用户确认的默认决策一致）。
package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// NamedCol：带名字的列布局（从 pg_attribute 实时读取，版本感知）
type NamedCol struct {
	Name   string
	AttNum int
	ColLen
}

// namedTableLayout：按 pg_attribute 读取 relOID 的用户列布局（含列名/attnum）
func namedTableLayout(dbDir string, relOID uint32, version int, isKB bool) []NamedCol {
	p := detectSysFile(dbDir, 1249, map[string]bool{"pg_attribute": true, "sys_attribute": true}, 0)
	if p == "" {
		return nil
	}
	var cols []NamedCol
	for tup := range iterSysTuples(p, version, isKB) {
		ar := attrFields(tup, version, isKB)
		if ar == nil || ar.AttRelID != relOID || ar.AttNum <= 0 || ar.AttIsDropped {
			continue
		}
		cl := ColLen{}
		if ar.AttLen > 0 {
			cl.AttLen = ar.AttLen
			cl.IsVarlena = false
			cl.AttAlign = ar.AttAlign
		} else {
			cl.AttLen = 0
			cl.IsVarlena = true
			cl.AttAlign = "i"
		}
		cols = append(cols, NamedCol{Name: ar.AttName, AttNum: ar.AttNum, ColLen: cl})
	}
	if len(cols) == 0 {
		return nil
	}
	sort.Slice(cols, func(i, j int) bool { return cols[i].AttNum < cols[j].AttNum })
	return cols
}


// catHasOID：该系统目录是否有 oid 系统列（行数据自 tHoff 起为 oid 4B）。
// PG12+ 与金仓 V9：pg_proc/pg_trigger/pg_class/pg_attribute/pg_namespace/pg_type/
// pg_language 保留 oid；pg_constraint/pg_rewrite/pg_depend/pg_largeobject 等已移除。
// 金仓 V8（PG10/11 内核）各目录均带 oid；此处按"移除 oid"目录判 false，
// V8 上这些目录若为私有写入读不到行，判 false 不影响（字段过滤兜底）。
func catHasOID(relName string) bool {
	switch relName {
	case "pg_proc", "pg_trigger", "pg_class", "pg_attribute", "pg_namespace",
		"pg_type", "pg_language", "pg_largeobject_metadata":
		return true
	}
	return false
}

// parseNamedTuple：按命名布局提取行字段（支持 oid 系统列在行首的 hasoids 表）。
// hasOID=true 时行数据自 tHoff 起为 oid(4B) + 用户列；null 位图仅覆盖用户列。
func parseNamedTuple(tup *HeapTuple, cols []NamedCol, hasOID bool, skipOIDMeta bool) (map[string][]byte, uint32) {
	raw := tup.Raw
	nraw := len(raw)
	nulls := tup.getNulls()
	out := map[string][]byte{}
	var oid uint32
	pos := 0
	base := tup.THoff
	if hasOID {
		if base+4 <= nraw {
			oid = u32(raw, base)
		}
		pos = 4
	}
	for i, item := range cols {
		align := layoutAlign(item.AttLen, item.AttAlign)
		// PG 标准 hasoids 表：pg_attribute 中 oid 系统列（attnum=1）为元数据行，
		// 数据区已在行首单独读取，必须跳过否则后续列整体错位 4B。
		// 金仓（isKB）hasoids 语义不同，不跳过。
		if skipOIDMeta && hasOID && item.AttNum == 1 && (item.Name == "oid" || item.Name == "xoid") {
			continue
		}
		if i < len(nulls) && nulls[i] {
			continue
		}
		if item.IsVarlena {
			kind0, _, _, _ := varlenaParse(raw, base+pos)
			if kind0 == "" || ((kind0 == VARLENA_4B || kind0 == VARLENA_4BC) && align > 1 && (pos&(align-1)) != 0) {
				if align > 1 {
					pos = (pos + align - 1) &^ (align - 1)
				}
			}
			offset := base + pos
			if offset >= nraw {
				break
			}
			kind, total, _, _ := varlenaParse(raw, offset)
			switch kind {
			case VARLENA_EXTERNAL:
				if offset+KB_EXTERNAL_SIZE > nraw {
					pos++
					continue
				}
				out[item.Name] = raw[offset : offset+KB_EXTERNAL_SIZE]
				pos += KB_EXTERNAL_SIZE
			case VARLENA_1B:
				if offset+total > nraw {
					pos++
					continue
				}
				out[item.Name] = raw[offset : offset+total]
				pos += total
			case VARLENA_4B, VARLENA_4BC:
				if offset+total > nraw {
					pos++
					continue
				}
				out[item.Name] = raw[offset : offset+total]
				pos += total
			default:
				pos++
			}
		} else {
			if align > 1 {
				pos = (pos + align - 1) &^ (align - 1)
			}
			alen := item.AttLen
			if alen <= 0 {
				alen = 1
			}
			offset := base + pos
			if offset+alen > nraw {
				pos += alen
				continue
			}
			out[item.Name] = raw[offset : offset+alen]
			pos += alen
		}
	}
	return out, oid
}

// parseInt2Vector：oidvector/int2vector 磁盘值 → int32 数组
// 内容（去 varlena 头）：[0:4) ndim=1 [4:8) dim0=1 [8:12) dim1=N [12:16)? 按 indexFields 实测口径
func parseInt2Vector(payload []byte) []int32 {
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

// schemaCatalog：--full-schema 的目录读取上下文（一次读取，多次复用）
type schemaCatalog struct {
	version    int
	isKB       bool
	dbDir      string
	nsMap      map[uint32]string
	classMap   map[uint32]*ClassRow // OID → pg_class 行
	classByRF  map[uint32]*ClassRow // relfilenode → 行（表空间自动发现）
	typeNames  map[uint32]string
	roleNames  map[uint32]string
	langNames  map[uint32]string // prolang OID → lanname
	procRows   []procRow
	trigRows   []triggerRow
	constrRows []constrRow
	viewRows   []viewRow
}

type procRow struct {
	OID        uint32
	Schema     string
	Name       string
	RetType    uint32
	ArgTypes   []int32
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

// openSchemaCatalog：构建目录上下文
func openSchemaCatalog(dbDir string, version int, isKB bool) (*schemaCatalog, error) {
	sc := &schemaCatalog{
		version: version, isKB: isKB, dbDir: dbDir,
		nsMap: map[uint32]string{}, classMap: map[uint32]*ClassRow{},
		classByRF: map[uint32]*ClassRow{}, typeNames: map[uint32]string{},
		roleNames: map[uint32]string{}, langNames: map[uint32]string{},
	}
	// namespace
	if p := detectSysFile(dbDir, PG_NAMESPACE_RELFILE, map[string]bool{"pg_namespace": true, "sys_namespace": true}, 0); p != "" {
		for tup := range iterSysTuples(p, version, isKB) {
			oid, name := nsFields(tup, version)
			if name != "" {
				sc.nsMap[oid] = name
			}
		}
	}
	// pg_class 全表
	if p := detectSysFile(dbDir, PG_CLASS_RELFILE, map[string]bool{"pg_class": true, "sys_class": true}, 0); p != "" {
		for tup := range iterSysTuples(p, version, isKB) {
			row := classFields(tup, version)
			if row != nil {
				sc.classMap[row.OID] = row
				if row.RelFileNode != 0 {
					sc.classByRF[row.RelFileNode] = row
				}
			}
		}
	}
	sc.typeNames = buildTypeNameMap(dbDir)
	sc.roleNames = buildRoleNameMap(dbDir)
	// pg_language（2612）：oid + lanname
	if p := detectSysFile(dbDir, 2612, map[string]bool{"pg_language": true, "sys_language": true}, 0); p != "" {
		for tup := range iterSysTuples(p, version, isKB) {
			oid := uint32(0)
			if tup.THoff+4 <= len(tup.Raw) {
				oid = u32(tup.Raw, tup.THoff)
			}
			name := cstring(tup.Raw, tup.THoff+4)
			if name != "" {
				sc.langNames[oid] = name
			}
		}
	}
	return sc, nil
}

func (sc *schemaCatalog) relName(oid uint32) string {
	row, ok := sc.classMap[oid]
	if !ok {
		return ""
	}
	schema := sc.nsMap[row.RelNamespace]
	if schema == "" {
		schema = "public"
	}
	return schema + "." + row.RelName
}

// userSchema：过滤系统 schema（只导出用户对象）
func (sc *schemaCatalog) userSchema(oid uint32) string {
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

// collectProcs：pg_proc（1255）函数
func (sc *schemaCatalog) collectProcs() {
	p := detectSysFile(sc.dbDir, 1255, map[string]bool{"pg_proc": true, "sys_proc": true}, 0)
	if p == "" {
		return
	}
	cols := namedTableLayout(sc.dbDir, 1255, sc.version, sc.isKB)
	if len(cols) == 0 {
		return
	}
	for tup := range iterSysTuples(p, sc.version, sc.isKB) {
		if !tup.isLive() {
			continue
		}
		f, oid := parseNamedTuple(tup, cols, true, !sc.isKB)
		if oid == 0 {
			continue
		}
		schema := sc.userSchema(fU32(f, "pronamespace"))
		if schema == "" {
			continue
		}
		row := procRow{
			OID: oid, Schema: schema, Name: fCstr(f, "proname"),
			RetType: fU32(f, "prorettype"), Language: fU32(f, "prolang"),
			Volatile: fByte(f, "provolatile"), IsStrict: fBool(f, "proisstrict"),
			SecDef: fBool(f, "prosecdef"),
		}
		if b, ok := f["proargtypes"]; ok {
			row.ArgTypes = parseInt2Vector(varlenaPayloadBytes(b))
		}
		if b, ok := f["prosrc"]; ok {
			row.Body = decodeVarlenaText(b)
		}
		if b, ok := f["proconfig"]; ok && len(b) > 0 {
			row.ConfigNote = "（含 GUC 配置 proconfig，未导出）"
		}
		sc.procRows = append(sc.procRows, row)
	}
	sort.Slice(sc.procRows, func(i, j int) bool {
		if sc.procRows[i].Schema != sc.procRows[j].Schema {
			return sc.procRows[i].Schema < sc.procRows[j].Schema
		}
		return sc.procRows[i].Name < sc.procRows[j].Name
	})
}

// collectTriggers：pg_trigger（2620）
func (sc *schemaCatalog) collectTriggers() {
	p := detectSysFile(sc.dbDir, 2620, map[string]bool{"pg_trigger": true, "sys_trigger": true}, 0)
	if p == "" {
		return
	}
	cols := namedTableLayout(sc.dbDir, 2620, sc.version, sc.isKB)
	if len(cols) == 0 {
		return
	}
	for tup := range iterSysTuples(p, sc.version, sc.isKB) {
		if !tup.isLive() {
			continue
		}
		f, oid := parseNamedTuple(tup, cols, true, !sc.isKB)
		if oid == 0 {
			continue
		}
		row := triggerRow{
			OID: oid, Name: fCstr(f, "tgname"),
			TGType: fU16(f, "tgtype"), Enabled: fByte(f, "tgenabled"),
			Internal: fBool(f, "tgisinternal"),
		}
		if row.Internal {
			continue
		}
		relOID := fU32(f, "tgrelid")
		row.Schema, row.Table = sc.splitRelName(relOID)
		if row.Schema == "" {
			continue
		}
		fnOID := fU32(f, "tgfoid")
		row.FuncSchema, row.Func = sc.splitProcName(fnOID)
		if b, ok := f["tgargs"]; ok && len(b) > 0 {
			row.ArgsNote = "（触发器参数未离线导出）"
		}
		sc.trigRows = append(sc.trigRows, row)
	}
	sort.Slice(sc.trigRows, func(i, j int) bool {
		if sc.trigRows[i].Schema != sc.trigRows[j].Schema {
			return sc.trigRows[i].Schema < sc.trigRows[j].Schema
		}
		return sc.trigRows[i].Table < sc.trigRows[j].Table
	})
}

// collectConstraints：pg_constraint（2606）FK + CHECK
func (sc *schemaCatalog) collectConstraints() {
	p := detectSysFile(sc.dbDir, 2606, map[string]bool{"pg_constraint": true, "sys_constraint": true}, 0)
	if p == "" {
		return
	}
	cols := namedTableLayout(sc.dbDir, 2606, sc.version, sc.isKB)
	if len(cols) == 0 {
		return
	}
	for tup := range iterSysTuples(p, sc.version, sc.isKB) {
		if !tup.isLive() {
			continue
		}
		// PG12+/V9 的 pg_constraint 无 oid 系统列 → 字段判据
		f, _ := parseNamedTuple(tup, cols, false, false)
		if len(f) == 0 {
			continue
		}
		typ := fByte(f, "contype")
		if typ != 'f' && typ != 'c' {
			continue
		}
		relOID := fU32(f, "conrelid")
		schema, table := sc.splitRelName(relOID)
		if schema == "" {
			continue
		}
		row := constrRow{
			OID: 0, Schema: schema, Table: table, Name: fCstr(f, "conname"),
			Type: typ, ConfRelID: fU32(f, "confrelid"),
			UpdType: fByte(f, "confupdtype"), DelType: fByte(f, "confdeltype"),
			Defrable: fBool(f, "condeferrable"), Deferred: fBool(f, "condeferred"),
			Validated: fBool(f, "convalidated"),
		}
		if b, ok := f["conkey"]; ok {
			row.ConKey = parseInt2Vector(varlenaPayloadBytes(b))
		}
		if b, ok := f["confkey"]; ok {
			row.ConfKey = parseInt2Vector(varlenaPayloadBytes(b))
		}
		row.ColNames = sc.colNameMap(relOID)
		if typ == 'f' {
			row.ConfColNames = sc.colNameMap(row.ConfRelID)
		}
		sc.constrRows = append(sc.constrRows, row)
	}
	sort.Slice(sc.constrRows, func(i, j int) bool {
		if sc.constrRows[i].Schema != sc.constrRows[j].Schema {
			return sc.constrRows[i].Schema < sc.constrRows[j].Schema
		}
		if sc.constrRows[i].Table != sc.constrRows[j].Table {
			return sc.constrRows[i].Table < sc.constrRows[j].Table
		}
		return sc.constrRows[i].Name < sc.constrRows[j].Name
	})
}

// collectViews：视图（relkind='v'）+ pg_depend 依赖清单
func (sc *schemaCatalog) collectViews() {
	for _, row := range sc.classMap {
		if row.RelKind != "v" && row.RelKind != "m" {
			continue
		}
		schema := sc.userSchema(row.RelNamespace)
		if schema == "" {
			continue
		}
		v := viewRow{OID: row.OID, Schema: schema, Name: row.RelName}
		sc.viewRows = append(sc.viewRows, v)
	}
	if len(sc.viewRows) == 0 {
		return
	}
	// pg_depend(2608)：无 oid 系统列。视图依赖直连 pg_depend.classid=1259(pg_class)
	// 且 objid=视图OID 的行 → refobjid 即依赖对象（规避 pg_rewrite 无 oid 无法反查）。
	viewSet := map[uint32]bool{}
	for _, v := range sc.viewRows {
		viewSet[v.OID] = true
	}
	if p := detectSysFile(sc.dbDir, 2608, map[string]bool{"pg_depend": true, "sys_depend": true}, 0); p != "" {
		for tup := range iterSysTuples(p, sc.version, sc.isKB) {
			if !tup.isLive() {
				continue
			}
			f, _ := parseNamedTuple(tup, []NamedCol{
				{Name: "classid", AttNum: 1, ColLen: ColLen{4, false, "i"}},
				{Name: "objid", AttNum: 2, ColLen: ColLen{4, false, "i"}},
				{Name: "objsubid", AttNum: 3, ColLen: ColLen{4, false, "i"}},
				{Name: "refclassid", AttNum: 4, ColLen: ColLen{4, false, "i"}},
				{Name: "refobjid", AttNum: 5, ColLen: ColLen{4, false, "i"}},
				{Name: "refobjsubid", AttNum: 6, ColLen: ColLen{4, false, "i"}},
				{Name: "deptype", AttNum: 7, ColLen: ColLen{1, false, "c"}},
			}, false, false)
			if len(f) == 0 || fU32(f, "classid") != 1259 {
				continue
			}
			viewOID := fU32(f, "objid")
			if !viewSet[viewOID] {
				continue
			}
			if fU32(f, "refclassid") != 1259 {
				continue
			}
			depName := sc.relName(fU32(f, "refobjid"))
			if depName == "" {
				continue
			}
			for i := range sc.viewRows {
				if sc.viewRows[i].OID == viewOID {
					sc.viewRows[i].Deps = append(sc.viewRows[i].Deps, depName)
					break
				}
			}
		}
	}
	sort.Slice(sc.viewRows, func(i, j int) bool {
		if sc.viewRows[i].Schema != sc.viewRows[j].Schema {
			return sc.viewRows[i].Schema < sc.viewRows[j].Schema
		}
		return sc.viewRows[i].Name < sc.viewRows[j].Name
	})
	// 依赖去重排序
	for i := range sc.viewRows {
		seen := map[string]bool{}
		var deps []string
		for _, d := range sc.viewRows[i].Deps {
			if !seen[d] {
				seen[d] = true
				deps = append(deps, d)
			}
		}
		sort.Strings(deps)
		sc.viewRows[i].Deps = deps
	}
}

// splitRelName：按 OID 取 (schema, relname)
func (sc *schemaCatalog) splitRelName(oid uint32) (string, string) {
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
func (sc *schemaCatalog) splitProcName(oid uint32) (string, string) {
	for _, pr := range sc.procRows {
		if pr.OID == oid {
			return pr.Schema, pr.Name
		}
	}
	// 回退：函数名 = 无 schema 前缀（触发器中由调用方补 schema）
	return "public", fmt.Sprintf("oid_%d", oid)
}

// colNameMap：表 OID → attnum → 列名
func (sc *schemaCatalog) colNameMap(relOID uint32) map[int]string {
	cols := namedTableLayout(sc.dbDir, relOID, sc.version, sc.isKB)
	out := map[int]string{}
	for _, c := range cols {
		out[c.AttNum] = c.Name
	}
	return out
}

func fU32(f map[string][]byte, name string) uint32 {
	b, ok := f[name]
	if !ok || len(b) < 4 {
		return 0
	}
	return binary.LittleEndian.Uint32(b)
}

func fU16(f map[string][]byte, name string) uint16 {
	b, ok := f[name]
	if !ok || len(b) < 2 {
		return 0
	}
	return binary.LittleEndian.Uint16(b)
}

func fByte(f map[string][]byte, name string) byte {
	b, ok := f[name]
	if !ok || len(b) < 1 {
		return 0
	}
	return b[0]
}

func fBool(f map[string][]byte, name string) bool {
	return fByte(f, name) != 0
}

func fCstr(f map[string][]byte, name string) string {
	b, ok := f[name]
	if !ok {
		return ""
	}
	return cstring(b, 0)
}

// varlenaPayloadBytes：varlena 原始字节 → payload 内容
func varlenaPayloadBytes(b []byte) []byte {
	payload, _, _ := varPayload(b)
	return payload
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

// genFullSchemaDDL：生成完整 Schema DDL 文本（按 --schema-objects 过滤）
func genFullSchemaDDL(sc *schemaCatalog, filter map[string]bool) string {
	var sb strings.Builder
	w := func(s string) { sb.WriteString(s) }
	if len(filter) == 0 {
		filter = map[string]bool{"view": true, "function": true, "trigger": true, "constraint": true, "fk": true}
	}
	// 函数
	if filter["function"] && len(sc.procRows) > 0 {
		w("--\n-- 函数（pg_proc）\n--\n")
		for _, p := range sc.procRows {
			args := make([]string, 0, len(p.ArgTypes))
			for _, a := range p.ArgTypes {
				args = append(args, sqlTypeName(sc.typeNames[uint32(a)]))
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
				quoteIdent(p.Schema), quoteIdent(p.Name), strings.Join(args, ", "), ret,
				quoteIdent(lang), strings.Join(flags, " "), q, p.Body, q))
		}
	}
	// 触发器
	if filter["trigger"] && len(sc.trigRows) > 0 {
		w("--\n-- 触发器（pg_trigger）\n--\n")
		for _, t := range sc.trigRows {
			fnTarget := t.Func
			if t.FuncSchema != "" && t.FuncSchema != "public" {
				fnTarget = quoteIdent(t.FuncSchema) + "." + quoteIdent(t.Func)
			}
			w(fmt.Sprintf("CREATE TRIGGER %s %s %s ON %s FOR EACH %s EXECUTE FUNCTION %s();\n",
				quoteIdent(t.Name), tgTiming(t.TGType), tgEvents(t.TGType),
				quoteIdent(t.Schema)+"."+quoteIdent(t.Table),
				map[bool]string{true: "ROW", false: "STATEMENT"}[t.TGType&0x0001 != 0],
				fnTarget))
			if t.Enabled != 'O' {
				w(fmt.Sprintf("-- 注: 原触发器状态 enabled=%c（非 'O'），需按需 ALTER TABLE %s DISABLE TRIGGER %s;\n",
					t.Enabled, quoteIdent(t.Schema)+"."+quoteIdent(t.Table), quoteIdent(t.Name)))
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
		for _, c := range sc.constrRows {
			if c.Type == 'f' {
				if !filter["fk"] && !filter["constraint"] {
					continue
				}
				var cols, refCols []string
				for _, k := range c.ConKey {
					cols = append(cols, quoteIdent(c.ColNames[int(k)]))
				}
				for _, k := range c.ConfKey {
					refCols = append(refCols, quoteIdent(c.ConfColNames[int(k)]))
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
					quoteIdent(c.Schema)+"."+quoteIdent(c.Table), quoteIdent(c.Name),
					strings.Join(cols, ", "), ref, strings.Join(refCols, ", "), act, def, notVal))
			} else if c.Type == 'c' && filter["constraint"] {
				w(fmt.Sprintf("-- CHECK 约束 %s (oid %d): 表达式为 pg_node_tree 序列化树，离线无法反编译。\n--   如需完整定义，请使用在线 pg_dump --schema-only（同库建表语句）。\n\n",
					quoteIdent(c.Schema)+"."+quoteIdent(c.Table)+"."+quoteIdent(c.Name), c.OID))
			}
		}
	}
	// 视图（占位 + 依赖清单）
	if filter["view"] && len(sc.viewRows) > 0 {
		w("--\n-- 视图（pg_class relkind='v'）：定义不可离线反编译，输出依赖清单\n--\n")
		for _, v := range sc.viewRows {
			w(fmt.Sprintf("-- 视图 %s (oid %d)\n", quoteIdent(v.Schema)+"."+quoteIdent(v.Name), v.OID))
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

func quoteIdent(name string) string {
	if name == "" {
		return `""`
	}
	return "\"" + strings.ReplaceAll(name, "\"", "\"\"") + "\""
}

// exportSchemaObjects：--full-schema 入口（main 集成；输出 schema_objects.ddl）
// 无 -o 时追加 stdout；-o 目录/文件时写 <dir>/schema_objects.ddl
// 返回写入字节数；report 加条目。
func exportSchemaObjects(o *Options) (int64, error) {
	if !o.DDL {
		return 0, fmt.Errorf("--full-schema 需要 --ddl（生成 DDL 模式）")
	}
	dbDir := resolveDbDir(o)
	if dbDir == "" {
		return 0, fmt.Errorf("--full-schema 需要数据库目录（位置参数目录或 --datadir+--db-oid）")
	}
	version := detectPgVersion(dbDir)
	if version == 0 {
		version = 12
	}
	isKB := isKingbaseDatadir(dbDir)
	sc, err := openSchemaCatalog(dbDir, version, isKB)
	if err != nil {
		return 0, err
	}
	filter := map[string]bool{}
	if o.SchemaObjects != "" {
		for _, k := range strings.Split(o.SchemaObjects, ",") {
			filter[strings.TrimSpace(k)] = true
		}
	}
	startMs := nowMs()
	sc.collectProcs()
	sc.collectTriggers()
	sc.collectConstraints()
	sc.collectViews()
	body := genFullSchemaDDL(sc, filter)
	if o.Verbose {
		logf("Schema DDL 生成完成: 函数 %d, 触发器 %d, 约束 %d, 视图 %d",
			len(sc.procRows), len(sc.trigRows), len(sc.constrRows), len(sc.viewRows))
	}
	header := "-- pg2sql 完整 Schema DDL 导出（--full-schema，v1.0.40）\n" +
		"-- 覆盖: 函数/触发器/外键/CHECK/视图（视图与 CHECK 为占位+依赖清单）\n\n"
	// 写盘
	if o.Output == "" {
		io.WriteString(os.Stdout, header)
		io.WriteString(os.Stdout, body)
		if o.Verbose {
			logf("Schema DDL 输出完成")
		}
		reportSink.add(ExportReportEntry{
			Table: "pg_catalog.schema_objects", Format: "ddl", Rows: 0, BadBlocks: 0,
			File: "", Bytes: int64(len(header) + len(body)), Checksum: "",
			ElapsedMs: nowMs() - startMs, PageSize: o.PageSize, Encoding: currentTextEncoding(),
		})
		return int64(len(header) + len(body)), nil
	}
	outPath := o.Output
	if fi, err := os.Stat(o.Output); err == nil && fi.IsDir() || strings.HasSuffix(o.Output, "/") {
		outPath = filepath.Join(o.Output, "schema_objects.ddl")
	} else if filepath.Ext(o.Output) == "" {
		outPath = o.Output + ".ddl"
	}
	if dir := filepath.Dir(outPath); dir != "" && dir != "." {
		os.MkdirAll(dir, 0755)
	}
	f, err := os.Create(outPath)
	if err != nil {
		return 0, fmt.Errorf("无法创建输出文件: " + err.Error())
	}
	defer f.Close()
	n, err := io.WriteString(f, header+body)
	if err != nil {
		return 0, fmt.Errorf("写入输出文件失败: %v", err)
	}
	if err := f.Sync(); err != nil {
		return 0, fmt.Errorf("写入输出文件失败: %v", err)
	}
	if o.Verbose {
		logf("输出到: %s", outPath)
	}
	reportSink.add(ExportReportEntry{
		Table: "pg_catalog.schema_objects", Format: "ddl", Rows: 0, BadBlocks: 0,
		File: outPath, Bytes: int64(n), Checksum: "",
		ElapsedMs: nowMs() - startMs, PageSize: o.PageSize, Encoding: currentTextEncoding(),
	})
	return int64(n), nil
}
