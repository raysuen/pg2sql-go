package integrity

// v1.0.53：完整性自证（report.json 增强）
//
// 离线导出最大的风险不是"报错"，而是"静默不完整"——导出成功退出码 0、
// 但少了段文件/少了 TOAST/少了表，现场无感知。本文件提供三类可核对的证据：
//  1. db_state      —— 快照来源状态（pg_control 离线解析）：干净关闭 or 运行中/崩溃恢复中
//  2. rows/pages    —— 逐表 pg_class.reltuples/relpages 与实测值对照（少导即告警）
//  3. skipped/types —— 跳过对象清单（含原因码）与类型解码覆盖度
//
// 设计原则：全部为"只读推断"，任一环节解析失败只降级不报错（evidence 缺省即视为无法自证）。

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"pg2sql/internal/catalog"
	"pg2sql/internal/datatype"
	"pg2sql/internal/engine"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------- pg_control 离线解析 ----------

// DbState：数据目录快照的关库状态证据
type DbState struct {
	Datadir       string `json:"datadir,omitempty"`
	ControlFile   string `json:"control_file,omitempty"`
	PgVersion     int    `json:"pg_version,omitempty"`
	IsKingbase    bool   `json:"is_kingbase,omitempty"`
	ControlVer    uint32 `json:"control_version,omitempty"`
	CatalogVer    uint32 `json:"catalog_version_no,omitempty"`
	SystemID      uint64 `json:"system_identifier,omitempty"`
	StateCode     uint32 `json:"control_state_code,omitempty"`
	State         string `json:"control_state,omitempty"`
	CheckpointAt  string `json:"checkpoint_time,omitempty"`
	CheckpointLSN string `json:"checkpoint_lsn,omitempty"`
	RedoLSN       string `json:"redo_lsn,omitempty"`
	Note          string `json:"note,omitempty"`
}

// ControlStateName：ControlFileData.state 枚举（PG 12-18 与金仓 V8/V9 一致）
func ControlStateName(code uint32) string {
	switch code {
	case 0:
		return "DB_STARTUP"
	case 1:
		return "DB_SHUTDOWNED"
	case 2:
		return "DB_SHUTDOWNED_IN_RECOVERY"
	case 3:
		return "DB_SHUTDOWNING"
	case 4:
		return "DB_IN_CRASH_RECOVERY"
	case 5:
		return "DB_IN_ARCHIVE_RECOVERY"
	case 6:
		return "DB_IN_PRODUCTION"
	}
	return ""
}

// datadirRoot：库目录（<root>/base/<oid>）→ 数据根目录
func DatadirRoot(dbDir string) string {
	if dbDir == "" {
		return ""
	}
	if filepath.Base(filepath.Dir(dbDir)) == "base" {
		return filepath.Dir(filepath.Dir(dbDir))
	}
	if fi, err := os.Stat(filepath.Join(dbDir, "global")); err == nil && fi.IsDir() {
		return dbDir
	}
	return filepath.Dir(filepath.Dir(dbDir))
}

// LSNString：XLogRecPtr → PG 风格 "hi/lo"（hi=段号，lo=段内偏移）
func LSNString(v uint64) string {
	return fmt.Sprintf("%X/%X", v>>32, v&0xFFFFFFFF)
}

// pgControlInfo：pg_control 关键字段（偏移经 PG17/金仓 V8/V9 实库校验）
// PgControlInfo：pg_control 关键字段的离线解析结果。
// 字段导出是为了让跨包的 report 生成/回归用例可以直接断言。
type PgControlInfo struct {
	SysID    uint64
	CtrlVer  uint32
	CatVer   uint32
	State    uint32
	CkptTime int64
	CkptLSN  uint64
	RedoLSN  uint64
}

// ParsePgControl：解析 global/pg_control（金仓为 global/sys_control），
// 校验失败返回 nil（宁可"无法自证"也不给出错误结论）。
//
// 布局（PG10+ 与金仓 V8/V9，均经实库 od 比对）：
//
//	@0  uint64 system_identifier
//	@8  uint32 pg_control_version
//	@12 uint32 catalog_version_no
//	@16 uint32 state（DBState 枚举）
//	@24 int64  time（pg_time_t，8 字节对齐，@20 为填充）
//	@32 uint64 checkPoint（XLogRecPtr）
//	@40 uint64 checkPointCopy.redo
func ParsePgControl(b []byte) *PgControlInfo {
	if len(b) < 48 {
		return nil
	}
	ci := &PgControlInfo{
		SysID:    binary.LittleEndian.Uint64(b[0:8]),
		CtrlVer:  binary.LittleEndian.Uint32(b[8:12]),
		CatVer:   binary.LittleEndian.Uint32(b[12:16]),
		State:    binary.LittleEndian.Uint32(b[16:20]),
		CkptTime: int64(binary.LittleEndian.Uint64(b[24:32])),
		CkptLSN:  binary.LittleEndian.Uint64(b[32:40]),
		RedoLSN:  binary.LittleEndian.Uint64(b[40:48]),
	}
	// 三重校验：版本号/状态枚举/checkpoint 时间落在合理区间（2001-01-01 之后）
	// v1.0.62 修复（P2-6）：下界从 1000 放宽到 900——PG 9.5/9.6 的
	// PG_CONTROL_VERSION 是 960（不是 9xx 的"新版格式"），旧校验把整个
	// PG9.6 快照的 db_state 证据直接判成"无法自证"而丢弃。
	// 上界 1999 保留（PG13+ 为 1300，金仓 V8/V9 同族）。
	if ci.CtrlVer < 900 || ci.CtrlVer > 1999 {
		return nil
	}
	// v1.0.62 修复（P2-6）：state=0（DB_STARTUP）是合法枚举值——实例刚启动、
	// 尚未做过一次 checkpoint 时控制文件就是这个值。旧校验 `State < 1`
	// 把它当损坏丢弃，恰恰是"取快照时实例在跑"这一最需要提示的场景。
	if ci.State > 6 {
		return nil
	}
	if ci.CkptTime < 978307200 || ci.CkptTime > 4102444800 { // 2001-01-01 ~ 2100-01-01
		return nil
	}
	return ci
}

// ProbeDbState：从库目录反推数据根，读取并解析控制文件。
// 读不到/解析不通过返回 nil（报告里 db_state 缺省）。
func ProbeDbState(dbDir string) *DbState {
	root := DatadirRoot(dbDir)
	if root == "" {
		return nil
	}
	ps := catalog.ProbeDirPageSize(dbDir)
	if ps == 0 {
		ps = 8192
	}
	type cand struct{ name, kind string }
	cands := []cand{
		{filepath.Join(root, "global", "pg_control"), "pg"},
		{filepath.Join(root, "global", "sys_control"), "kb"},
	}
	var path string
	for _, c := range cands {
		if fi, err := os.Stat(c.name); err == nil && fi.Mode().IsRegular() && fi.Size() >= 48 {
			path = c.name
			break
		}
	}
	if path == "" {
		return nil
	}
	buf := make([]byte, 512)
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	n, _ := f.Read(buf)
	f.Close()
	ci := ParsePgControl(buf[:n])
	if ci == nil {
		return nil
	}
	version := catalog.DetectPgVersion(dbDir)
	isKB := catalog.IsKingbaseDatadir(dbDir)
	st := &DbState{
		Datadir:       root,
		ControlFile:   filepath.Base(path),
		PgVersion:     version,
		IsKingbase:    isKB,
		ControlVer:    ci.CtrlVer,
		CatalogVer:    ci.CatVer,
		SystemID:      ci.SysID,
		StateCode:     ci.State,
		State:         ControlStateName(ci.State),
		CheckpointAt:  time.Unix(ci.CkptTime, 0).UTC().Format(time.RFC3339),
		CheckpointLSN: LSNString(ci.CkptLSN),
		RedoLSN:       LSNString(ci.RedoLSN),
	}
	// 结论性提示：只陈述"快照来自什么状态"，不替用户判定业务一致性
	switch ci.State {
	case 0:
		st.Note = "控制文件处于 DB_STARTUP（实例启动中、尚未完成首次 checkpoint），此时库内尚无一致的数据页快照，建议等实例稳定后重新取证"
	case 1:
		st.Note = "数据库为干净关闭时的快照（DB_SHUTDOWNED），磁盘页一致性最佳"
	case 2:
		st.Note = "数据库在恢复中关闭（DB_SHUTDOWNED_IN_RECOVERY），可能含未完成的重放"
	case 3:
		st.Note = "取快照时数据库正在关闭（DB_SHUTDOWNING），建议关库完成后重新拷贝"
	case 4:
		st.Note = "数据库处于崩溃恢复态（DB_IN_CRASH_RECOVERY），磁盘数据可能未一致，导出结果需回灌校验"
	case 5:
		st.Note = "数据库处于归档恢复态（DB_IN_ARCHIVE_RECOVERY），数据可能尚未到达一致点"
	case 6:
		st.Note = "数据库取自运行中的实例（DB_IN_PRODUCTION），最近提交可能未落盘；建议源库 CHECKPOINT 后重新取快照"
	}
	if isKB {
		st.Note += "（金仓使用 global/sys_control，状态位与 PG 同构）"
	}
	return st
}

// ---------- 行数/页数对照 ----------

// RowsVerdict：实测行数 vs pg_class.reltuples（分析估计值）。
// 返回 (估计行数, 判定)。reltuples 含死元组，故 actual<est 属常见膨胀，
// 但偏差过大即说明"少导"（坏页/段文件缺失/权限受限读到的旧快照）。
func RowsVerdict(actual int64, reltuples float32, limited, filtered, ddlOnly bool) (int64, string) {
	if ddlOnly {
		return 0, ""
	}
	if limited {
		return 0, "limited"
	}
	if filtered {
		return 0, "filtered"
	}
	if reltuples < 0 { // PG14+ 未分析过：reltuples = -1
		return 0, "unanalyzed"
	}
	est := int64(reltuples + 0.5)
	if est <= 0 {
		if actual == 0 {
			return 0, "match"
		}
		return 0, "" // 无估计值（空表/未统计），不做判定
	}
	switch {
	case actual == 0:
		return est, "under"
	case float64(actual) < float64(est)*0.9:
		return est, "under"
	case float64(actual) > float64(est)*1.1:
		return est, "over"
	}
	return est, "match"
}

// PagesVerdict：relpages（上次 VACUUM/ANALYZE 记录的页数）vs 实测主文件页数。
// 实测 < 期望 = 段文件缺失/被截断（严重，必须告警）；实测 > 期望 = 正常增长（不告警）。
func PagesVerdict(expected int32, actual int64) string {
	if expected <= 0 {
		if actual > 0 {
			return "nostats"
		}
		return ""
	}
	if actual < int64(expected) {
		return "short"
	}
	return "match"
}

// MainForkPages：主文件（含 .1/.2 段）总页数
func MainForkPages(path string, pageSize int) int64 {
	if path == "" || pageSize <= 0 {
		return 0
	}
	var total int64
	for _, seg := range engine.SegFiles(path) {
		if fi, err := os.Stat(seg); err == nil && fi.Size() > 0 {
			total += fi.Size() / int64(pageSize)
		}
	}
	return total
}

// ---------- 跳过清单 ----------

// SkipEntry：未导出对象及原因（自证"少了哪些、为什么少"）
type SkipEntry struct {
	Table   string `json:"table"`
	Schema  string `json:"schema,omitempty"`
	RelKind string `json:"relkind,omitempty"`
	Reason  string `json:"reason"`           // 机器可读原因码
	Detail  string `json:"detail,omitempty"` // 人类可读补充
}

type skipSink struct {
	mu      sync.Mutex
	entries []SkipEntry
	seen    map[string]bool
}

var SkippedObjects = &skipSink{seen: map[string]bool{}}

// AddSkip：记录一条跳过（按 table+reason 去重，并发安全）
func AddSkip(e SkipEntry) {
	if e.Table == "" {
		return
	}
	SkippedObjects.mu.Lock()
	defer SkippedObjects.mu.Unlock()
	if SkippedObjects.seen == nil {
		SkippedObjects.seen = map[string]bool{}
	}
	k := e.Table + "|" + e.Reason
	if SkippedObjects.seen[k] {
		return
	}
	SkippedObjects.seen[k] = true
	SkippedObjects.entries = append(SkippedObjects.entries, e)
}

// snapshot：按表名排序返回（报告可 diff）
func (s *skipSink) Snapshot() []SkipEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SkipEntry, len(s.entries))
	copy(out, s.entries)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Table == out[j].Table {
			return out[i].Reason < out[j].Reason
		}
		return out[i].Table < out[j].Table
	})
	return out
}

// ResetSkip：单次进程只需一份（多入口重复调用 runAllTables 的场景不会被重复统计）
func ResetSkip() {
	SkippedObjects.mu.Lock()
	SkippedObjects.entries = nil
	SkippedObjects.seen = map[string]bool{}
	SkippedObjects.mu.Unlock()
}

// ---------- 类型解码覆盖度 ----------

// TypeCoverage：类型解码覆盖度（"哪些类型的值不是精确还原"）
type TypeCoverage struct {
	OID      uint32   `json:"oid"`
	Name     string   `json:"name"`
	Kind     string   `json:"kind"` // fallback=文本/hex 退化 | unresolved=pg_type 查不到名字
	Columns  []string `json:"columns,omitempty"`
	ColCount int      `json:"column_count"`
}

type typeSink struct {
	mu   sync.Mutex
	byID map[uint32]*TypeCoverage
}

var typeCoverageSink = &typeSink{byID: map[uint32]*TypeCoverage{}}

func ResetTypeCoverage() {
	typeCoverageSink.mu.Lock()
	typeCoverageSink.byID = map[uint32]*TypeCoverage{}
	typeCoverageSink.mu.Unlock()
}

// TypeCoverageKind：按 datatype.DecodeValue 的分发顺序判定精确度
//
//	exact     —— 有专用解码器（硬编码/动态 typinput 映射/数组/枚举/复合）
//	fallback  —— 落到 datatype.DecodeDefault（文本透传或 \x hex），字节可逆但不保证与源库文本一致
//	unresolved——pg_type 中也找不到该 oid（DDL 将退化为 oid 类型）
func TypeCoverageKind(oid uint32, depth int) string {
	if _, ok := datatype.Decoders[oid]; ok {
		return "exact"
	}
	if _, ok := datatype.EnumMapSnapshot()[oid]; ok {
		return "exact"
	}
	td, has := datatype.DynamicTypeDefs[oid]
	if !has {
		return "unresolved"
	}
	switch td.Type {
	case 'c': // 复合类型：record 文本输出
		return "exact"
	case 'e': // 枚举
		return "exact"
	case 'd': // 域：按基类型判定（限递归深度，防自引用环）
		if td.Base != 0 && depth < 8 {
			return TypeCoverageKind(td.Base, depth+1)
		}
		return "fallback"
	case 'p': // 伪类型，不落存储
		return "exact"
	}
	if _, ok := datatype.DynamicDecoders[oid]; ok {
		return "exact"
	}
	return "fallback"
}

// CollectTypeCoverage：登记单表内所有非精确类型（并发安全，按 oid 聚合列名）
func CollectTypeCoverage(tm *engine.TableMeta) {
	if tm == nil {
		return
	}
	datatype.CatalogMu.Lock()
	type hit struct {
		oid  uint32
		name string
		kind string
	}
	var found []hit
	for _, cd := range tm.Columns {
		if cd.Dropped || cd.Name == "" {
			continue
		}
		if k := TypeCoverageKind(cd.TypeOID, 0); k != "exact" {
			nm := tm.TypeNames[cd.TypeOID]
			if nm == "" {
				if td, ok := datatype.DynamicTypeDefs[cd.TypeOID]; ok {
					nm = td.Name
				}
			}
			found = append(found, hit{cd.TypeOID, nm, k})
		}
	}
	datatype.CatalogMu.Unlock()
	if len(found) == 0 {
		return
	}
	typeCoverageSink.mu.Lock()
	defer typeCoverageSink.mu.Unlock()
	for _, h := range found {
		e := typeCoverageSink.byID[h.oid]
		if e == nil {
			e = &TypeCoverage{OID: h.oid, Name: h.name, Kind: h.kind}
			typeCoverageSink.byID[h.oid] = e
		}
		e.ColCount++
		// 列清单封顶 200 条，避免超大库报告膨胀
		if len(e.Columns) < 200 {
			e.Columns = append(e.Columns, tm.FullName()+"."+h.name)
		}
	}
}

func TypeCoverageSnapshot() []TypeCoverage {
	typeCoverageSink.mu.Lock()
	defer typeCoverageSink.mu.Unlock()
	out := make([]TypeCoverage, 0, len(typeCoverageSink.byID))
	for _, e := range typeCoverageSink.byID {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind == out[j].Kind {
			return out[i].Name < out[j].Name
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// ---------- 汇总与判定 ----------

// ReportSummary：report.json 的汇总段（一行看清"该导多少、导了多少、少了什么"）
type ReportSummary struct {
	TablesTotal      int   `json:"tables_exported"`
	TablesSkipped    int   `json:"tables_skipped"`
	TablesFailed     int   `json:"tables_failed"`
	RowsExported     int64 `json:"rows_exported"`
	RowsEstimate     int64 `json:"rows_estimate,omitempty"`
	TablesRowsShort  int   `json:"tables_rows_under"`
	TablesPagesShort int   `json:"tables_pages_short"`
	BadBlocks        int64 `json:"bad_blocks"`
	// BadBlocksSystem：坏块中落在系统模式（pg_catalog/sys*/information_schema/pg_toast）的部分。
	// 金仓系统表存在私有写入机制（页头 item 目录不更新），离线校验会判"坏页"，
	// 这类噪声不应把整体判定拉到 fail——用户表坏块才是真信号。
	BadBlocksSystem int64 `json:"bad_blocks_system,omitempty"`
	ToastMissing    int64 `json:"toast_missing"`
	ToastCorrupt    int64 `json:"toast_corrupt"`
	Bytes           int64 `json:"bytes"`
	ElapsedMs       int64 `json:"elapsed_ms"`
	UnknownTypes    int   `json:"unknown_types"`
}

// ReportVerdict：一句话结论（脚本可按 level 判读，不必自己拼规则）
type ReportVerdict struct {
	Level   string   `json:"level"` // ok | warn | fail
	Reasons []string `json:"reasons,omitempty"`
}

func BuildSummary(entries []ExportReportEntry, skips []SkipEntry, types []TypeCoverage) ReportSummary {
	s := ReportSummary{TablesSkipped: len(skips), UnknownTypes: len(types)}
	for _, e := range entries {
		s.TablesTotal++
		if e.Error != "" {
			s.TablesFailed++
		}
		s.RowsExported += e.Rows
		s.RowsEstimate += e.RowsEstimate
		s.BadBlocks += e.BadBlocks
		if IsSystemSchemaTable(e.Table) {
			s.BadBlocksSystem += e.BadBlocks
		}
		s.ToastMissing += e.ToastMissing
		s.ToastCorrupt += e.ToastCorrupt
		s.Bytes += e.Bytes
		s.ElapsedMs += e.ElapsedMs
		switch e.RowsCheck {
		case "under":
			s.TablesRowsShort++
		}
		if e.PagesCheck == "short" {
			s.TablesPagesShort++
		}
	}
	return s
}

// BuildVerdict：把各项证据折成 ok/warn/fail 三级结论
func BuildVerdict(s ReportSummary, db *DbState, types []TypeCoverage, skips []SkipEntry) ReportVerdict {
	v := ReportVerdict{Level: "ok"}
	fail := func(f string, a ...any) { v.Level = "fail"; v.Reasons = append(v.Reasons, fmt.Sprintf(f, a...)) }
	warn := func(f string, a ...any) {
		if v.Level == "ok" {
			v.Level = "warn"
		}
		v.Reasons = append(v.Reasons, fmt.Sprintf(f, a...))
	}
	if s.TablesTotal == 0 && s.TablesSkipped == 0 && s.TablesFailed == 0 {
		// 什么都没发现 = 目标目录/表名错了，不能报"通过"（旧行为：0 表 0 行 → ok，
		// 与"静默不完整"同源，现场最容易漏）
		fail("未发现任何可导出对象（检查位置参数/--datadir+--db-oid、--schema、--table-name 是否正确）")
	}
	if s.TablesFailed > 0 {
		fail("%d 张表导出失败（见 exports[].error）", s.TablesFailed)
	}
	if userBad := s.BadBlocks - s.BadBlocksSystem; userBad > 0 {
		fail("检出 %d 个损坏数据块（见 exports[].bad_pages）", userBad)
	}
	if s.BadBlocksSystem > 0 {
		warn("%d 个系统表数据块不符合页头校验（金仓私有写机制常见；用户表未受影响，仅供排查参考）", s.BadBlocksSystem)
	}
	if s.TablesPagesShort > 0 {
		fail("%d 张表数据文件页数少于 pg_class.relpages（疑段文件缺失/被截断）", s.TablesPagesShort)
	}
	if s.ToastCorrupt > 0 {
		fail("检出 %d 处 TOAST 数据损坏", s.ToastCorrupt)
	}
	if s.ToastMissing > 0 {
		warn("%d 处外置列指向缺失的 TOAST 数据（已输出 NULL 并在行内标记）", s.ToastMissing)
	}
	if s.TablesRowsShort > 0 {
		warn("%d 张表导出行数明显低于 reltuples 估计（可能含大量死元组，也可能少导）", s.TablesRowsShort)
	}
	if s.UnknownTypes > 0 {
		warn("%d 种类型的值按文本/hex 退化输出（字节可逆，文本形态可能与源库不同）", s.UnknownTypes)
	}
	if db != nil {
		switch db.StateCode {
		case 1:
			// 干净关闭：最理想
		case 6:
			warn("快照取自运行中的实例（DB_IN_PRODUCTION），最近提交可能未落盘")
		default:
			warn("快照来源状态为 %s，磁盘数据可能未达一致点", db.State)
		}
	}
	if len(skips) > 0 {
		// 只有"用户点名要的表却没找到"才是异常；索引/视图/序列/系统对象/分区父表
		// 等结构性跳过属正常，仅列在 skipped[] 备查，不拉高判定级别（避免噪声淹没信号）。
		missing := 0
		for _, s := range skips {
			if s.Reason == "named_missing" {
				missing++
			}
		}
		if missing > 0 {
			warn("指定的 %d 张表在 catalog 中未找到（见 skipped[]，reason=named_missing）", missing)
		}
	}
	return v
}

// SummarizeSkips：按原因码聚合计数（报告顶部速览用）
func SummarizeSkips(skips []SkipEntry) map[string]int {
	out := map[string]int{}
	for _, s := range skips {
		out[s.Reason]++
	}
	return out
}

// IsSystemSchemaTable：表名是否为系统模式对象。
// v1.0.62 修复（P3）：本函数是"哪些坏块算系统自带噪声"的唯一口径。
// 旧实现只认 6 个 schema 名，而 app.isSystemSchema 还认 kdb_schedule/
// anon/src_restrict/SYS_HM*/sys_* —— 两处口径不一致导致这些系统 schema 的
// 坏块被计入内置表统计（BadBlocksSystem 漏计 → 用户表 verdict 假阳性 fail）。
// 现统一到本函数，app 侧改为直接调用。
func IsSystemSchemaTable(full string) bool {
	i := strings.IndexByte(full, '.')
	if i <= 0 {
		return false
	}
	s := strings.Trim(full[:i], `"`)
	switch s {
	case "pg_catalog", "information_schema", "sys", "sys_catalog",
		"sysaudit", "sysmac", "kdb_schedule", "anon", "src_restrict":
		return true
	}
	return strings.HasPrefix(s, "pg_toast") || strings.HasPrefix(s, "sys_toast") ||
		strings.HasPrefix(s, "SYS_HM") || strings.HasPrefix(s, "sys_")
}

// RelKindName：pg_class.relkind → 中文对象类型（跳过清单可读性）
func RelKindName(k string) string {
	switch k {
	case "r":
		return "普通表"
	case "i":
		return "索引"
	case "I":
		return "分区索引"
	case "S":
		return "序列"
	case "t":
		return "TOAST 表"
	case "v":
		return "视图"
	case "m":
		return "物化视图"
	case "c":
		return "复合类型"
	case "f":
		return "外部表"
	case "p":
		return "分区父表"
	case "":
		return "未知"
	}
	return "relkind=" + k
}

// ExportReportEntry：单表导出报告条目
type ExportReportEntry struct {
	Table     string `json:"table"`
	Format    string `json:"format"` // sql|csv|ddl
	Rows      int64  `json:"rows"`
	BadBlocks int64  `json:"bad_blocks"`
	// v1.0.44 P2-2/P2-3：坏页明细页号 + TOAST 损坏计数（宕机救数排障）
	BadPages     []int  `json:"bad_pages,omitempty"`
	ToastMissing int64  `json:"toast_missing,omitempty"`
	ToastCorrupt int64  `json:"toast_corrupt,omitempty"`
	File         string `json:"file,omitempty"`
	Bytes        int64  `json:"bytes,omitempty"`
	Checksum     string `json:"checksum,omitempty"`
	ElapsedMs    int64  `json:"elapsed_ms"`
	PageSize     int    `json:"page_size"`
	Encoding     string `json:"encoding,omitempty"`
	Error        string `json:"error,omitempty"`
	// ---- v1.0.53 完整性自证 ----
	// RowsEstimate：pg_class.reltuples 取整（上次 VACUUM/ANALYZE 的估计行数）
	RowsEstimate int64 `json:"rows_estimate,omitempty"`
	// RowsCheck：match|under|over|unanalyzed|limited|filtered
	//   under = 实测明显少于估计（可能死元组膨胀，也可能少导/坏页）
	RowsCheck string `json:"rows_check,omitempty"`
	// PagesExpected/PagesActual/PagesCheck：relpages 与实测主文件页数对照
	//   PagesCheck=short 表示实测少于 catalog 记录（段文件缺失/被截断，严重）
	PagesExpected int32  `json:"pages_expected,omitempty"`
	PagesActual   int64  `json:"pages_actual,omitempty"`
	PagesCheck    string `json:"pages_check,omitempty"`
	// EncOverride：表级编码覆盖（v1.0.52），非空表示本表未按库级基线解码
	EncOverride string `json:"enc_override,omitempty"`
}
