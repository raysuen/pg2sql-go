// Author: raysuen
package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"pg2sql/internal/catalog"
	"pg2sql/internal/config"
	"pg2sql/internal/ddl"
	"pg2sql/internal/integrity"
	"pg2sql/internal/logx"
	"pg2sql/internal/util"
	"strings"
)

// exportSchemaObjects：--full-schema 入口（main 集成；输出 schema_objects.ddl）
// 无 -o 时追加 stdout；-o 目录/文件时写 <dir>/schema_objects.ddl
// 返回写入字节数；report 加条目。
func exportSchemaObjects(o *config.Options) (int64, error) {
	if !o.DDL {
		return 0, fmt.Errorf("--full-schema 需要 --ddl（生成 DDL 模式）")
	}
	dbDir := resolveDbDir(o)
	if dbDir == "" {
		return 0, fmt.Errorf("--full-schema 需要数据库目录（位置参数目录或 --datadir+--db-oid）")
	}
	applyAutoEncoding(o, dbDir) // v1.0.46：schema DDL 文本同样依赖库编码
	version := catalog.DetectPgVersion(dbDir)
	if version == 0 {
		version = 12
	}
	isKB := catalog.IsKingbaseDatadir(dbDir)
	sc, err := ddl.OpenSchemaCatalog(dbDir, version, isKB)
	if err != nil {
		return 0, err
	}
	filter := map[string]bool{}
	if o.SchemaObjects != "" {
		// v1.0.56 修复（P3-G10）：旧版对 --schema-objects 的取值不做合法性校验，
		// 拼写错误（如复数 "views"）时 filter 非空但所有分段判断全部落空，
		// 静默输出仅剩头注释且 exit 0（与 B2 同族问题）。非法 key 直接报错。
		filter, err = ddl.ParseSchemaObjectFilter(o.SchemaObjects, filter)
		if err != nil {
			return 0, err
		}
	}
	startMs := util.NowMs()
	sc.CollectProcs()
	sc.CollectTriggers()
	sc.CollectConstraints()
	sc.CollectViews()
	body := ddl.GenFullSchemaDDL(sc, filter)
	if o.Verbose {
		logx.Logf("Schema DDL 生成完成: 函数 %d, 触发器 %d, 约束 %d, 视图 %d",
			len(sc.ProcRows), len(sc.TrigRows), len(sc.ConstrRows), len(sc.ViewRows))
	}
	header := "-- pg2sql 完整 Schema DDL 导出（--full-schema，v1.0.40）\n" +
		"-- 覆盖: 函数/触发器/外键/CHECK/视图（视图与 CHECK 为占位+依赖清单）\n\n"
	// 写盘
	if o.Output == "" {
		io.WriteString(os.Stdout, header)
		io.WriteString(os.Stdout, body)
		if o.Verbose {
			logx.Logf("Schema DDL 输出完成")
		}
		reportSink.add(integrity.ExportReportEntry{
			Table: "pg_catalog.schema_objects", Format: "ddl", Rows: 0, BadBlocks: 0,
			File: "", Bytes: int64(len(header) + len(body)), Checksum: "",
			ElapsedMs: util.NowMs() - startMs, PageSize: o.PageSize, Encoding: currentTextEncoding(),
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
		logx.Logf("输出到: %s", outPath)
	}
	reportSink.add(integrity.ExportReportEntry{
		Table: "pg_catalog.schema_objects", Format: "ddl", Rows: 0, BadBlocks: 0,
		File: outPath, Bytes: int64(n), Checksum: "",
		ElapsedMs: util.NowMs() - startMs, PageSize: o.PageSize, Encoding: currentTextEncoding(),
	})
	return int64(n), nil
}
