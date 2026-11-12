package nt

// 函数 ↔ schema 归属（v1.2.18）。
//
// 背景：FUNCEXPR 分支原本只用 procMap[fid] 拿函数**名**，而 procMap 只读
// pg_proc 的 oid + proname（见 catalog.BuildProcNameMap），**不含 pronamespace**。
// 于是 ntTypmodCastSpec 对 name == "numeric" 一律按 PG 内置语义解码 typmod：
//
//	CREATE FUNCTION myschema.numeric(numeric, integer) RETURNS numeric ...
//	-- 某默认值/CHECK 里调用 myschema.numeric(col, 2)
//	v1.2.17 输出：CAST(col AS numeric)      ← 把函数调用悄悄变成了类型转换
//
// 这比渲染成非法 SQL 更糟：非法 SQL 至少会报错，而静默改写语义不会。
//
// 修法：额外维护一份 oid → nspname，只让**系统 schema 下**的同名函数走 typmod 分支；
// 用户 schema 下的同名函数退回通用 name(a, b, ...) 渲染（语义正确，即使长得不像原调用）。
//
// 为什么不合并进 procMap：procMap 被大量调用点当作"函数名"使用（单参转型、
// 通用函数渲染、算符参数类型等），改类型会波及整条链路。新增一份并行映射的
// 成本更低，也更容易在缺失时安全退化（映射为空 → 全部按用户函数处理 → 不误判）。

var (
	// procNamespaces：函数 OID → schema 名（仅用于区分系统函数与用户同名函数）。
	procNamespaces map[uint32]string
	// systemSchemaNames：被视为"系统 schema"的名字集合（pg_catalog / sys / sys_catalog）。
	systemSchemaNames = map[string]bool{
		"pg_catalog":  true,
		"sys":         true,
		"sys_catalog": true,
		"pg_toast":    true,
	}
)

// SetProcNamespaces：注入函数 OID → schema 名映射（由 catalog 层在构建 procMap 时一并提供）。
// 传 nil 或空映射时，ntTypmodCastSpec 退化为"只认系统 schema"无法判定 → 全部拒绝走
// typmod 分支吗？**不**：那样会让 v1.2.14 的修复失效（PG 内置 typmod 默认值又会输出
// 非法 SQL）。所以缺失时保持向后兼容：按"系统函数"处理（与 v1.2.17 行为一致），
// 但此时无法防御同名用户函数——这是有意的取舍：宁可漏防一个罕见场景，也不能让
// 每张带 typmod 默认值的表都回退到非法 SQL。
func SetProcNamespaces(m map[uint32]string) {
	procNamespaces = m
}

// isSystemProc：该函数是否属于系统 schema。
//
// 三态：
//   - procNamespaces == nil（目录未提供该映射）：返回 true（向后兼容，见 SetProcNamespaces）
//   - 命中且 schema 是系统 schema：true
//   - 命中但 schema 是用户 schema：false（→ 不得走 typmod 分支）
//   - 未命中（OID 不在 pg_proc 里，如 catalog 读失败）：true（保守，与旧行为一致）
func isSystemProc(oid uint32) bool {
	if procNamespaces == nil {
		return true
	}
	nsp, ok := procNamespaces[oid]
	if !ok {
		return true
	}
	return systemSchemaNames[nsp]
}

// ResetProcNamespaces：清空注入（单测用，保证用例间互不影响）。
func ResetProcNamespaces() { procNamespaces = nil }

// ProcNamespacesInjected：是否已执行注入步骤（未注入为 false）。
//
// 语义边界（v1.2.18 R3）：true 只代表 catalog.ScanDynamic 走到了 SetProcNamespaces
// 这一步，**不代表映射内容可用**——catalog.buildProcNamespaceMap 在 pg_proc 文件
// 缺失时返回非 nil 空 map，此时注入成功但查不到任何函数，isSystemProc 全部走
// "未命中 → true" 的保守路径。本探针仅供顺序断言（注入必须早于域 DEFAULT 反解）
// 使用；若将来要做"映射内容可用"的功能断言，应另外检查 map 非空，不能复用本函数。
//
// 供上层做顺序断言用。这不是装饰性接口：域 DEFAULT 的反解发生在 catalog.buildTypeDefs
// 内部，若注入晚于它，那一刻映射是 nil，isSystemProc 一律返回 true ——
// 用户 myschema.numeric(x, 2) 会被改写成 CAST(x AS numeric)，且不报错。
// v1.2.18 就踩过这个坑（注入写在 buildTypeDefs 之后），所以把"是否已注入"
// 暴露出来，让单测能锁住顺序。
func ProcNamespacesInjected() bool { return procNamespaces != nil }

// procSchemaQualifier：返回函数调用需不需要（以及用什么）schema 限定。
//
// 为什么需要：v1.2.18 第一版只做了"拒绝走 typmod 分支"，退回通用 name(a, b, ...)
// 渲染。但用户 schema 下的函数若**不加限定**，回灌时 PG 按 search_path 解析，
// 会命中 pg_catalog 里的同名函数，结果反而变成语法错误：
//
//	SET DEFAULT numeric(12.5, 2)   -- ERROR: syntax error at or near ";"
//
// 所以拒绝 typmod 分支后必须补上限定名，才能得到语义正确的
// `syslog.numeric(12.5, 2)`。
//
// 返回 "" 表示不需要限定（public 不必加；映射缺失时保持旧行为）。
func procSchemaQualifier(oid uint32) string {
	if procNamespaces == nil {
		return ""
	}
	nsp, ok := procNamespaces[oid]
	if !ok || nsp == "" || nsp == "public" {
		return ""
	}
	return nsp
}
