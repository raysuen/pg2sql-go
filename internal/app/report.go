// Author: raysuen
package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"pg2sql/internal/cli"
	"pg2sql/internal/config"
	"pg2sql/internal/datatype"
	"pg2sql/internal/engine"
	"pg2sql/internal/integrity"
	"pg2sql/internal/logx"
	"sync"
	"time"
)

// ReportSink：批量/并行导出时的报告收集器（jobs 并发安全）
type ReportSink struct {
	mu      sync.Mutex
	entries []integrity.ExportReportEntry
}

var reportSink = &ReportSink{}

func (r *ReportSink) add(e integrity.ExportReportEntry) {
	r.mu.Lock()
	r.entries = append(r.entries, e)
	r.mu.Unlock()
}

func (r *ReportSink) snapshot() []integrity.ExportReportEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]integrity.ExportReportEntry, len(r.entries))
	copy(out, r.entries)
	return out
}

// recordFailure：导出失败的表也写进报告（含错误原文），
// 保证 report.json 的 exports[] 与"成功+失败"总数一致，不留空白。
func recordFailure(tm *engine.TableMeta, err error) {
	if tm == nil || err == nil {
		return
	}
	reportSink.add(integrity.ExportReportEntry{
		Table: tm.FullName(), Format: "error", Error: err.Error(),
		PagesExpected: tm.RelPages,
	})
}

// writeReport：--report 开启时输出 report.json（-o 目录/文件同目录；无 -o 打印 stderr JSON）
func writeReport(o *config.Options) {
	// v1.2.18：域 DEFAULT 无法还原的域 → 转成 skipped[] 条目（reason=domain_default_unresolved）。
	// 必须放在 --report 早退之前：不开报告时也要清理全局状态，否则同一进程多次
	// 调用（--jobs 多轮）会把上一轮的记录带进下一轮。
	registerDomainDefaultSkips()
	if !o.Report {
		return
	}
	entries := reportSink.snapshot()
	skips := integrity.SkippedObjects.Snapshot()
	types := integrity.TypeCoverageSnapshot()
	var db *integrity.DbState
	dbDir := resolveDbDir(o)
	if dbDir != "" {
		db = integrity.ProbeDbState(dbDir)
	}
	summary := integrity.BuildSummary(entries, skips, types)
	verdict := integrity.BuildVerdict(summary, db, types, skips)
	pageSize := o.PageSize
	if pageSize == 0 {
		for _, e := range entries {
			if e.PageSize > 0 {
				pageSize = e.PageSize
				break
			}
		}
	}
	rep := struct {
		Tool           string                        `json:"tool"`
		Version        string                        `json:"version"`
		Generated      string                        `json:"generated_at"`
		Encoding       string                        `json:"db_encoding,omitempty"`
		EncBaseline    string                        `json:"encoding_baseline,omitempty"`
		PageSize       int                           `json:"page_size,omitempty"`
		Jobs           int                           `json:"jobs,omitempty"`
		ChecksumAlg    string                        `json:"checksum_alg,omitempty"`
		TableFilter    string                        `json:"table_filter,omitempty"`
		Limit          int                           `json:"limit,omitempty"`
		Verdict        integrity.ReportVerdict       `json:"verdict"`
		Summary        integrity.ReportSummary       `json:"summary"`
		DbState        *integrity.DbState            `json:"db_state,omitempty"`
		SkippedSummary map[string]int                `json:"skipped_summary,omitempty"`
		Skipped        []integrity.SkipEntry         `json:"skipped,omitempty"`
		UnknownTypes   []integrity.TypeCoverage      `json:"unknown_types,omitempty"`
		Exports        []integrity.ExportReportEntry `json:"exports"`
	}{
		Tool: "pg2sql", Version: cli.Version, Generated: time.Now().Format(time.RFC3339),
		Encoding: currentTextEncoding(), EncBaseline: o.EncBase, PageSize: pageSize,
		Jobs: o.Jobs, ChecksumAlg: o.Checksum, TableFilter: o.TableName, Limit: o.Limit,
		Verdict: verdict, Summary: summary, DbState: db,
		SkippedSummary: integrity.SummarizeSkips(skips), Skipped: skips, UnknownTypes: types,
		Exports: entries,
	}
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		logx.Logf("报告生成失败: %v", err)
		return
	}
	// stderr 一行结论：现场不打开 JSON 也能判断这次导出是否可交付
	if msg := verdictLine(verdict, summary); msg != "" {
		logx.Logf("%s", msg)
	}
	if db != nil && db.State != "" && db.StateCode != 1 {
		logx.Logf("提示: %s（control_state=%s）", db.Note, db.State)
	}
	if o.Output == "" && o.ReportPath == "" {
		fmt.Fprintln(os.Stderr, string(data))
		return
	}
	// v1.0.53：--report-file 优先（显式路径），否则 <输出目录>/report.json
	if o.ReportPath != "" {
		// v1.0.64 修复（E-11）：--report-file 与 -o 指向同一文件时，
		// 报告会用 JSON 覆盖刚导出的数据产物（自证体系被自身截断）——拒绝并硬失败
		if o.Output != "" && sameFilePath(o.ReportPath, o.Output) {
			logx.ErrorExit("--report-file 与 -o 指向同一路径 " + o.Output +
				"：报告将覆盖刚导出的数据文件，请为 --report-file 指定其他路径")
		}
		if d := filepath.Dir(o.ReportPath); d != "" && d != "." {
			os.MkdirAll(d, 0755)
		}
		if err := os.WriteFile(o.ReportPath, data, 0644); err != nil {
			// v1.0.60 修复（P2-12）：--report 显式开启时报告写不出来仍 exit 0，
			// 调用方（取证/交付）会误认为证据文件已产出。改为硬失败。
			logx.ErrorExit("报告写入失败: " + err.Error() + "（--report 已开启，报告是交付物的一部分，不能静默成功）")
		}
		logx.Logf("导出报告: %s", o.ReportPath)
		return
	}
	dir := o.Output
	if fi, err := os.Stat(o.Output); err == nil && !fi.IsDir() {
		dir = filepath.Dir(o.Output)
	}
	if dir != "" && dir != "." {
		os.MkdirAll(dir, 0755)
	}
	path := filepath.Join(dir, "report.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		logx.ErrorExit("报告写入失败: " + err.Error() + "（--report 已开启，报告是交付物的一部分，不能静默成功）")
	}
	logx.Logf("导出报告: %s", path)
}

// auxOutputPath：--full-schema / --large-objects / --roles 的输出路径解析。
// v1.0.59 修复（P0-2）：o.Output 为具体文件时是【数据导出保留路径】，
// 辅助导出绝不再复用它做 os.Create(O_TRUNC)——旧行为会让 Schema DDL /
// 大对象 / 角色 DDL 依次覆盖刚导出的数据文件（数据丢失且 report 记录伪校验和）。
// 统一规则：-o 是目录（或以 / 结尾）→ <dir>/<defaultName>；
// -o 是具体文件 → 改用其所在目录下的 <defaultName> 并打印提示。
func auxOutputPath(o *config.Options, defaultName string) string {
	if o.Output == "" {
		return ""
	}
	if fi, err := os.Stat(o.Output); (err == nil && fi.IsDir()) || strings.HasSuffix(o.Output, "/") {
		return filepath.Join(o.Output, defaultName)
	}
	dir := filepath.Dir(o.Output)
	logx.Logf("提示: -o %s 为数据导出保留路径，%s 改为输出到 %s", o.Output, defaultName, filepath.Join(dir, defaultName))
	return filepath.Join(dir, defaultName)
}

// guardOutputAgainstSource：v1.0.59 修复（P0-3）。
// os.Create(O_TRUNC) 发生在读取任何源页之前——若 -o 指向源堆文件本身（或其
// 符号链接/硬链接），会先把源库数据文件清零、再导出 0 行并 exit 0，
// 对取证场景等于销毁证据源。创建输出文件前与全部源路径做 os.SameFile
// 比对（Stat 解引用符号链接），命中即拒绝。
// v1.2.12 修复（A1）：sources 此前只含主段（数据文件 / TOAST / --data-file），
// >1GB 的表有 16391.1 / 16391.2 等续段，`-o 16391.1` 时守卫比对不到 →
// os.Create 先截断续段、再回去读它，源数据段清零且导出 0 行。
// 现对每个源路径展开 engine.SegFiles（有 sync.Map 缓存，重复调用零 IO）。
func guardOutputAgainstSource(outPath string, sources ...string) error {
	ofi, err := os.Stat(outPath)
	if err != nil {
		return nil // 输出文件不存在，必然无冲突
	}
	seen := make(map[string]bool, len(sources))
	for _, src := range sources {
		if src == "" {
			continue
		}
		cand := []string{src}
		if fs := engine.SegFiles(src); len(fs) > 0 {
			cand = fs
		}
		for _, real := range cand {
			if real == "" || seen[real] {
				continue
			}
			seen[real] = true
			sfi, err := os.Stat(real)
			if err != nil {
				continue
			}
			if os.SameFile(ofi, sfi) {
				name := ""
				if real != src {
					name = "（续段文件 " + real + "）"
				}
				return fmt.Errorf("拒绝输出: -o 路径 %s 与源数据文件 %s%s 指向同一文件（含符号链接/硬链接），写入会先截断源库再导出 0 行，已中止", outPath, src, name)
			}
		}
	}
	return nil
}

// verdictLine：把判定渲染成一行中文结论（stderr）
//
// v1.2.18：域条目不再并入"跳过"计数。域不是表，混在一起会让用户拿着"跳过 5"
// 去表清单里数却只找到 4 条。有域未还原时单独追加一段。
func verdictLine(v integrity.ReportVerdict, s integrity.ReportSummary) string {
	head := map[string]string{"ok": "完整性自证: 通过", "warn": "完整性自证: 有告警", "fail": "完整性自证: 不完整"}
	h, ok := head[v.Level]
	if !ok {
		return ""
	}
	line := fmt.Sprintf("%s — 导出 %d 表 / %d 行（估计 %d）；跳过 %d；失败 %d；坏块 %d；行数偏低 %d 表；页数缺失 %d 表",
		h, s.TablesTotal, s.RowsExported, s.RowsEstimate, s.TablesSkipped, s.TablesFailed,
		s.BadBlocks, s.TablesRowsShort, s.TablesPagesShort)
	if s.DomainsUnresolved > 0 {
		line += fmt.Sprintf("；域 DEFAULT 未还原 %d 个", s.DomainsUnresolved)
	}
	return line
}

// sameFilePath：跨符号链接判定两路径是否指向同一文件（v1.0.65 修复 F15）。
// v1.0.64 的 E-11 守卫只用 filepath.Abs 比较字符串，经 symlink 指向同一文件
// 可绕过（报告覆盖数据产物）。优先用 os.SameFile（inode 级），文件尚不存在
// 时退化为"父目录 EvalSymlinks + 文件名"比较。
func sameFilePath(a, b string) bool {
	if fi1, e1 := os.Stat(a); e1 == nil {
		if fi2, e2 := os.Stat(b); e2 == nil {
			return os.SameFile(fi1, fi2)
		}
	}
	ra, ea := resolveParentLinks(a)
	rb, eb := resolveParentLinks(b)
	if ea == nil && eb == nil && ra == rb {
		return true
	}
	aa, e1 := filepath.Abs(a)
	bb, e2 := filepath.Abs(b)
	return e1 == nil && e2 == nil && aa == bb
}

func resolveParentLinks(p string) (string, error) {
	d, base := filepath.Split(filepath.Clean(p))
	rd, err := filepath.EvalSymlinks(d)
	if err != nil {
		return "", err
	}
	return filepath.Join(rd, base), nil
}

// registerDomainDefaultSkips：把 datatype 侧记录"域 DEFAULT 无法还原"的域
// 转成 skipped[] 条目，并打一条收尾告警（v1.2.18，P3）。
//
// 背景：typeTailDefault 定位失败 / typdefaultbin 反解失败时，旧实现让 TypDefault
// 保持空，DDL 只是少一段 DEFAULT：没有 report 字段、没有 fail 信号，除非有人比对
// pg_get_expr(typdefaultbin) 否则发现不了。对照 v1.2.16 给分区边界加的
// partition_bound_unresolved 计数，这里原本完全没有对应处置。
//
// 判 warn 不判 fail（见 integrity.BuildVerdict）：列默认值丢失不改变已有数据，
// 与 partition_bound_unresolved 那种"结构错位 + 数据归属丢失"的取证红线区别对待。
func registerDomainDefaultSkips() {
	list := datatype.DomainDefaultUnresolvedSummary()
	if len(list) == 0 {
		return
	}
	for _, d := range list {
		label := d.Name
		if d.Schema != "" && d.Schema != "-" {
			label = d.Schema + "." + d.Name
		}
		integrity.AddSkip(integrity.SkipEntry{
			Table:   "domain:" + label,
			Schema:  d.Schema,
			RelKind: "d",
			Reason:  "domain_default_unresolved",
			Detail: fmt.Sprintf("域 %s(oid=%d) 的 DEFAULT 表达式无法离线还原，"+
				"DDL 已省略 DEFAULT，回灌后引用该域的列默认值缺失，需人工补回",
				label, d.OID),
		})
	}
	names := make([]string, 0, len(list))
	for _, d := range list {
		n := d.Name
		if d.Schema != "" && d.Schema != "-" {
			n = d.Schema + "." + d.Name
		}
		names = append(names, fmt.Sprintf("%s(oid=%d)", n, d.OID))
	}
	sort.Strings(names)
	logx.Logf("警告: %d 个域的 DEFAULT 表达式无法离线还原（%s）——DDL 已省略 DEFAULT，"+
		"回灌后引用这些域的列默认值会丢失，需人工补回。请反馈并附上域 oid",
		len(names), strings.Join(names, ", "))
}
