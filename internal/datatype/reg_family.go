// reg* 反查表与解码器（v1.2.13 补齐族内剩余 8 个）
//
// 背景：regtype(2206)/regclass(2205)/regnamespace(4089) 此前已实现名字输出；
// regproc(24)/regprocedure(2202)/regoper(2203)/regoperator(2204)/regconfig(3734)/
// regdictionary(3769)/regrole(4096)/regcollation(4191) 仍走 decodeRegOid 输出裸 OID。
// 裸 OID 对内置对象侥幸可回灌，但 regproc/regprocedure 指向的用户函数 OID、
// regconfig/regdictionary 指向的全文搜索配置 OID、regrole 指向的角色 OID 均为
// 每库独立分配，跨库回灌必然解析失败（或错指）。本轮按 regtype 同路径补齐：
// catalog.ScanDynamic 构建反查表 → DynamicScan 注入 → 本文件解码。
//
// 输出形态以 PG17.6 实机为准（探针实测，非纸面推断）：
//   - regproc/regprocedure/regconfig/regdictionary：可见 → 名字原文；
//     不可见 → quote_qualified_identifier（schema 与名字各自按需 quoting，
//     实测 'zzprobe."MyFn"'::regproc → zzprobe."MyFn"）。
//   - regoper/regoperator：可见 → 名字原文（"+" 不加引号）；不可见 →
//     nsp.name 原文拼接【不做 quoting】（实测 'zzprobe.##'::regoper →
//     zzprobe.##，与 regproc 的 quoting 行为不同）。
//   - regcollation：可见 → quote_identifier（"C" 输出带引号！）；不可见 →
//     quote_qualified_identifier。
//   - regprocedure/regoperator 的参数类型按【类型自身】可见性决定是否限定
//     （format_type_be 语义）：pg_catalog/public 内置类型 → 裸名
//     （含 "double precision"/数组 "text[]"），其余 schema → nsp.name 限定
//     （实测 'zzprobe.zzfn3(zzprobe.dom)' → zzprobe.zzfn3(zzprobe.dom)，
//     而内置参数保持 integer 不限定——与宿主函数可见性无关）。
//
// 可见性近似（离线导出无会话 search_path，与 regclass/regtype 同一约定）：
// nsp == "pg_catalog"（隐式 search_path 首项）或 nsp == DefaultRegNamespace
// （public，PG 默认 search_path 落点）视为可见。
package datatype

import (
	"encoding/binary"
	"strconv"
	"strings"
)

// RegProcInfo：pg_proc 反查行（regproc/regprocedure）。
type RegProcInfo struct {
	Name string
	Nsp  uint32 // pronamespace
	Args []uint32
}

// RegOperInfo：pg_operator 反查行（regoper/regoperator）。
type RegOperInfo struct {
	Name        string
	Nsp         uint32 // oprnamespace
	Kind        byte   // 'b'=双目 'l'=前缀 'r'=后缀（PG14 起无后缀算符）
	Left, Right uint32 // oprleft/oprright；前缀算符 Left=0，后缀 Right=0
}

// RegNamed：pg_ts_config / pg_ts_dict / pg_collation 反查行。
type RegNamed struct {
	Name string
	Nsp  uint32
}

var (
	regProcInfos    map[uint32]RegProcInfo
	regOperInfos    map[uint32]RegOperInfo
	regConfigInfos  map[uint32]RegNamed
	regDictInfos    map[uint32]RegNamed
	regRoleNames    map[uint32]string
	regCollInfos    map[uint32]RegNamed
	regOperNONEArgs bool // PG16+：前缀/后缀算符的缺失参数输出 NONE（见 regOperSignature）
)

// regNspVisible：nsp 是否落在近似 search_path 内（pg_catalog / public）。
// 反查表不可用时按不可见处理（保守：输出完整限定形式，绝不丢信息）。
func regNspVisible(nsp uint32) bool {
	if nsp != 0 && regNamespaceNames != nil {
		if n, ok := regNamespaceNames[nsp]; ok && n != "" {
			return n == "pg_catalog" || n == DefaultRegNamespace
		}
	}
	return false
}

// regQualIdent：regproc/regprocedure/regconfig/regdictionary 的名字输出。
// 可见 → 只输出名字，但**名字本身仍走 quote_identifier**；
// 不可见 → quote_qualified_identifier（schema 与名字各自 quoting）。
//
// v1.2.17 修复（P2）：可见分支旧实现直接 `return name`，理由是"PG 的
// regconfigout 可见分支是 pstrdup 原名"。该结论只在被探测的那几个名字上成立——
// `english` / `simple` 这类全小写合法标识符，quote_identifier 前后完全一样，
// 看不出差别；一旦名字需要引号就露馅：`My Cfg2`（挂在 public，属可见）
// 旧版输出裸 `My Cfg2`，而服务端是 `"My Cfg2"`，回灌时 `My Cfg2` 会被切成
// 两个 token。实测金仓 V9R1C10（pg 模式）：
//
//	My Role / My Cfg2 / My Dict2  →  "My Role" / "My Cfg2" / "My Dict2"
//	english / simple / system     →  原样（quote_identifier 判定无需引号）
//	"My Sch"."My Cfg"（跨 schema）→  "My Sch"."My Cfg"（旧版这条是对的）
//
// 即：带 schema 前缀的分支一直正确，只有"省略前缀"这条分支漏了 quoting。
func regQualIdent(nsp uint32, name string) string {
	if regNspVisible(nsp) {
		return regQuoteIdent(name)
	}
	if nsp != 0 && regNamespaceNames != nil {
		if n, ok := regNamespaceNames[nsp]; ok && n != "" {
			return regQuoteIdent(n) + "." + regQuoteIdent(name)
		}
	}
	return regQuoteIdent(name)
}

// regOperNameText：regoper/regoperator 的算符名输出。
// 可见 → 原文；不可见 → nsp.name 原文拼接，【不】做 quoting——与 regproc
// 的 quote_qualified_identifier 行为不同（实测 'zzprobe.##'::regoper 输出
// zzprobe.## 而非 zzprobe."##"；算符名不是标识符，quoting 反而破坏算符语法）。
func regOperNameText(nsp uint32, name string) string {
	if regNspVisible(nsp) {
		return name
	}
	if nsp != 0 && regNamespaceNames != nil {
		if n, ok := regNamespaceNames[nsp]; ok && n != "" {
			return n + "." + name
		}
	}
	return name
}

// regCollNameText：regcollation 的名字输出。
// 可见 → quote_identifier（实测 'C' 输出为带引号的 "C"，大写名必须可回灌）；
// 不可见 → quote_qualified_identifier。
func regCollNameText(nsp uint32, name string) string {
	if regNspVisible(nsp) {
		return regQuoteIdent(name)
	}
	if nsp != 0 && regNamespaceNames != nil {
		if n, ok := regNamespaceNames[nsp]; ok && n != "" {
			return regQuoteIdent(n) + "." + regQuoteIdent(name)
		}
	}
	return regQuoteIdent(name)
}

// regArgTypeName：proargtypes 元素 → 参数类型文本（format_type_be 语义）。
//   - pg_type 反查 miss → 裸 OID（保信息）；
//   - 数组类型（typname "_" 前缀 + typelem）输出 "元素类型[]"，归属按元素类型判定；
//   - 类型 schema 不在近似 search_path 内 → nsp.name 限定（按类型自身可见性，
//     与宿主函数/算符的可见性无关，见文件头实测记录）。
func regArgTypeName(oid uint32) string {
	if DynamicTypeDefs != nil {
		if td, ok := DynamicTypeDefs[oid]; ok && td.Name != "" {
			// v1.2.15：名字部分已按 hard-wired / quote_identifier 定稿（含必要引号），
			// 下面只负责拼 schema 前缀与 "[]" 后缀，不能再二次 quoting
			// （否则 `"char"` 会被包成 """char"""）。
			name := regTypeNameText(oid, td.Name)
			nsp := td.Nsp
			isArray := false
			if strings.HasPrefix(td.Name, "_") && td.Elem != 0 {
				if etd, ok2 := DynamicTypeDefs[td.Elem]; ok2 && etd.Name != "" {
					name = regTypeNameText(td.Elem, etd.Name)
					nsp = etd.Nsp
					isArray = true
				}
			}
			suffix := ""
			if isArray {
				suffix = "[]" // [] 不是合法标识符，不能整串过 regQuoteIdent
			}
			if !regNspVisible(nsp) && nsp != 0 && regNamespaceNames != nil {
				if n, ok2 := regNamespaceNames[nsp]; ok2 && n != "" {
					return regQuoteIdent(n) + "." + name + suffix
				}
			}
			return name + suffix
		}
	}
	return strconv.FormatUint(uint64(oid), 10)
}

// regProcSignature：regprocedure 的 name(argtypes) 拼装。
func regProcSignature(e RegProcInfo) string {
	var sb strings.Builder
	sb.WriteString(regQualIdent(e.Nsp, e.Name))
	sb.WriteByte('(')
	for i, a := range e.Args {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(regArgTypeName(a))
	}
	sb.WriteByte(')')
	return sb.String()
}

// regOperSignature：regoperator 的 format_operator 拼装。
// 缺失参数的输出形态按版本分派（实机 PG17.6 实测 + PG16 行为变更）：
//   - PG16+：前缀 name(NONE,right)、后缀 (left,NONE)name——实测
//     '-(none,integer)'::regoperator 输出 -(NONE,integer)；
//   - PG12-15 / 金仓：前缀 name(right)、后缀 (left)name（旧形态；NONE 形态
//     在旧版回灌会报 invalid name syntax，故必须按目标库版本选择）。
func regOperSignature(e RegOperInfo) string {
	nm := regOperNameText(e.Nsp, e.Name)
	switch e.Kind {
	case 'l': // 前缀算符：只有右参数
		if regOperNONEArgs {
			return nm + "(NONE," + regArgTypeName(e.Right) + ")"
		}
		return nm + "(" + regArgTypeName(e.Right) + ")"
	case 'r': // 后缀算符（PG13-）：只有左参数
		if regOperNONEArgs {
			return "(" + regArgTypeName(e.Left) + ",NONE)" + nm
		}
		return "(" + regArgTypeName(e.Left) + ")" + nm
	default: // 'b' 双目
		return nm + "(" + regArgTypeName(e.Left) + "," + regArgTypeName(e.Right) + ")"
	}
}

// decodeRegproc（oid 24）：对齐 PG regprocout——输出函数名，不可见时 schema 限定。
func decodeRegproc(B []byte) string {
	if len(B) < 4 {
		return DecodeDefault(B)
	}
	oid := binary.LittleEndian.Uint32(B)
	if oid == 0 {
		return "-"
	}
	if regProcInfos != nil {
		if e, ok := regProcInfos[oid]; ok && e.Name != "" {
			return regQualIdent(e.Nsp, e.Name)
		}
	}
	return strconv.FormatUint(uint64(oid), 10)
}

// decodeRegprocedure（oid 2202）：对齐 PG regprocedureout/format_procedure——
// 输出 name(argtypes)；参数类型按类型自身可见性限定，pg_type 反查 miss 的
// 参数回退裸 OID。
func decodeRegprocedure(B []byte) string {
	if len(B) < 4 {
		return DecodeDefault(B)
	}
	oid := binary.LittleEndian.Uint32(B)
	if oid == 0 {
		return "-"
	}
	if regProcInfos != nil {
		if e, ok := regProcInfos[oid]; ok && e.Name != "" {
			return regProcSignature(e)
		}
	}
	return strconv.FormatUint(uint64(oid), 10)
}

// decodeRegoper（oid 2203）：对齐 PG regoperout——可见算符输出原名（如 "+"）。
func decodeRegoper(B []byte) string {
	if len(B) < 4 {
		return DecodeDefault(B)
	}
	oid := binary.LittleEndian.Uint32(B)
	if oid == 0 {
		return "-"
	}
	if regOperInfos != nil {
		if e, ok := regOperInfos[oid]; ok && e.Name != "" {
			return regOperNameText(e.Nsp, e.Name)
		}
	}
	return strconv.FormatUint(uint64(oid), 10)
}

// decodeRegoperator（oid 2204）：对齐 PG regoperatorout/format_operator——
// 按 oprkind 拼参数（前缀 name(right)、后缀 (left)name、双目 name(left,right)）。
func decodeRegoperator(B []byte) string {
	if len(B) < 4 {
		return DecodeDefault(B)
	}
	oid := binary.LittleEndian.Uint32(B)
	if oid == 0 {
		return "-"
	}
	if regOperInfos != nil {
		if e, ok := regOperInfos[oid]; ok && e.Name != "" {
			return regOperSignature(e)
		}
	}
	return strconv.FormatUint(uint64(oid), 10)
}

// decodeRegconfig（oid 3734）：对齐 PG regconfigout——输出全文搜索配置名。
func decodeRegconfig(B []byte) string {
	if len(B) < 4 {
		return DecodeDefault(B)
	}
	oid := binary.LittleEndian.Uint32(B)
	if oid == 0 {
		return "-"
	}
	if regConfigInfos != nil {
		if e, ok := regConfigInfos[oid]; ok && e.Name != "" {
			return regQualIdent(e.Nsp, e.Name)
		}
	}
	return strconv.FormatUint(uint64(oid), 10)
}

// decodeRegdictionary（oid 3769）：对齐 PG regdictionaryout——输出全文搜索词典名。
func decodeRegdictionary(B []byte) string {
	if len(B) < 4 {
		return DecodeDefault(B)
	}
	oid := binary.LittleEndian.Uint32(B)
	if oid == 0 {
		return "-"
	}
	if regDictInfos != nil {
		if e, ok := regDictInfos[oid]; ok && e.Name != "" {
			return regQualIdent(e.Nsp, e.Name)
		}
	}
	return strconv.FormatUint(uint64(oid), 10)
}

// decodeRegrole（oid 4096）：对齐 PG regroleout——输出 rolname，走 quote_identifier。
//
// v1.2.17 修复（P2）：旧实现输出裸名，注释还写着"PG 本身不做标识符 quoting"，
// 实测正好相反。PG 的 regroleout_internal 走的是 quote_identifier(rolname)：
//
//	'My Role'::regrole → "My Role"     （需引号的名字必须带引号，否则回灌被切成两个 token）
//	'system'::regrole  → system        （quote_identifier 判定无需引号）
//
// 角色无 schema 概念，所以只做单段 quoting，不做 schema 限定。
func decodeRegrole(B []byte) string {
	if len(B) < 4 {
		return DecodeDefault(B)
	}
	oid := binary.LittleEndian.Uint32(B)
	if oid == 0 {
		return "-"
	}
	if regRoleNames != nil {
		if n, ok := regRoleNames[oid]; ok && n != "" {
			return regQuoteIdent(n)
		}
	}
	return strconv.FormatUint(uint64(oid), 10)
}

// decodeRegcollation（oid 4191）：对齐 PG regcollationout——可见也走
// quote_identifier（"C" 输出为 "C"），不可见时 quote_qualified_identifier。
func decodeRegcollation(B []byte) string {
	if len(B) < 4 {
		return DecodeDefault(B)
	}
	oid := binary.LittleEndian.Uint32(B)
	if oid == 0 {
		return "-"
	}
	if regCollInfos != nil {
		if e, ok := regCollInfos[oid]; ok && e.Name != "" {
			return regCollNameText(e.Nsp, e.Name)
		}
	}
	return strconv.FormatUint(uint64(oid), 10)
}
