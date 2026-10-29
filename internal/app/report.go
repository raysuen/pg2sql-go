// Author: raysuen
package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pg2sql/internal/cli"
	"pg2sql/internal/config"
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
func guardOutputAgainstSource(outPath string, sources ...string) error {
	ofi, err := os.Stat(outPath)
	if err != nil {
		return nil // 输出文件不存在，必然无冲突
	}
	for _, src := range sources {
		if src == "" {
			continue
		}
		sfi, err := os.Stat(src)
		if err != nil {
			continue
		}
		if os.SameFile(ofi, sfi) {
			return fmt.Errorf("拒绝输出: -o 路径 %s 与源数据文件 %s 指向同一文件（含符号链接/硬链接），写入会先截断源库再导出 0 行，已中止", outPath, src)
		}
	}
	return nil
}

// verdictLine：把判定渲染成一行中文结论（stderr）
func verdictLine(v integrity.ReportVerdict, s integrity.ReportSummary) string {
	head := map[string]string{"ok": "完整性自证: 通过", "warn": "完整性自证: 有告警", "fail": "完整性自证: 不完整"}
	h, ok := head[v.Level]
	if !ok {
		return ""
	}
	return fmt.Sprintf("%s — 导出 %d 表 / %d 行（估计 %d）；跳过 %d；失败 %d；坏块 %d；行数偏低 %d 表；页数缺失 %d 表",
		h, s.TablesTotal, s.RowsExported, s.RowsEstimate, s.TablesSkipped, s.TablesFailed,
		s.BadBlocks, s.TablesRowsShort, s.TablesPagesShort)
}
