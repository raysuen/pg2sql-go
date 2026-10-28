// Author: raysuen
package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"pg2sql/internal/catalog"
	"pg2sql/internal/config"
	"pg2sql/internal/engine"
	"pg2sql/internal/integrity"
	"pg2sql/internal/logx"
	"pg2sql/internal/objects"
	"pg2sql/internal/util"
	"strings"
)

// exportRoles：--roles 主流程（独立运行或与批量导出并行调用）
func exportRoles(o *config.Options) (int64, error) {
	dbDir := resolveDbDir(o)
	if dbDir == "" {
		return 0, fmt.Errorf("--roles 需要数据库目录（位置参数目录或 --datadir+--db-oid）")
	}
	root := integrity.DatadirRoot(dbDir)
	version := catalog.DetectPgVersion(dbDir)
	if version == 0 {
		version = 12
	}
	isKB := catalog.IsKingbaseDatadir(dbDir)
	startMs := util.NowMs()
	rs, err := objects.LoadRoles(dbDir, root, version, isKB, o.PageSize)
	if err != nil {
		return 0, err
	}
	body, notes := objects.GenRolesDDL(rs, !o.RolesNoPassword)
	for _, n := range notes {
		logx.Logf("角色导出: %s", n)
	}
	nRoles := 0
	for _, r := range rs.Roles {
		if !objects.IsSystemRole(r) {
			nRoles++
		}
	}
	if o.Verbose {
		logx.Logf("角色导出: %d 个角色, %d 条成员关系（布局 %s）", nRoles, len(rs.Members), rs.Layout)
		// 逐条打印解析结果（OID/属性），便于现场核对布局是否匹配
		for _, r := range rs.Roles {
			kind := "用户"
			if objects.IsSystemRole(r) {
				kind = "内置"
			}
			logx.Logf("  角色[%s] %s oid=%d login=%v super=%v createrole=%v createdb=%v repl=%v bypassrls=%v connlimit=%d pwd=%v valid=%q",
				kind, r.Name, r.OID, r.CanLogin, r.Super, r.CreateRole, r.CreateDB,
				r.Replication, r.BypassRLS, r.ConnLimit, r.HasPassword, r.ValidUntil)
		}
	}
	// 输出：目录 → roles.ddl；指定文件 → 原样；无 -o → stdout
	if o.Output == "" {
		os.Stdout.WriteString(body)
		reportSink.add(integrity.ExportReportEntry{
			Table: "pg_catalog.pg_authid", Format: "ddl", Rows: int64(nRoles),
			File: "", Bytes: int64(len(body)), ElapsedMs: util.NowMs() - startMs,
			PageSize: o.PageSize, Encoding: currentTextEncoding(),
		})
		return int64(len(body)), nil
	}
	outPath := o.Output
	if fi, err := os.Stat(o.Output); err == nil && fi.IsDir() || strings.HasSuffix(o.Output, "/") {
		outPath = filepath.Join(o.Output, "roles.ddl")
	} else if filepath.Ext(o.Output) == "" {
		outPath = o.Output + ".ddl"
	}
	if dir := filepath.Dir(outPath); dir != "" && dir != "." {
		os.MkdirAll(dir, 0755)
	}
	if err := os.WriteFile(outPath, []byte(body), 0644); err != nil {
		return 0, fmt.Errorf("写入角色 DDL 失败: %v", err)
	}
	logx.Logf("角色 DDL 输出: %s（%d 个角色）", outPath, nRoles)
	reportSink.add(integrity.ExportReportEntry{
		Table: "pg_catalog.pg_authid", Format: "ddl", Rows: int64(nRoles),
		File: outPath, Bytes: util.StatSize(outPath), ElapsedMs: util.NowMs() - startMs,
		PageSize: o.PageSize, Encoding: currentTextEncoding(),
	})
	return int64(len(body)), nil
}

// exportLargeObjects：main 集成入口——校验、收集、写盘（-o 目录/文件；无 -o 走 stdout）
// 返回大对象数；同时写 report 条目与导入脚本（import_lob.sh）
func exportLargeObjects(o *config.Options) (int, error) {
	dbDir := resolveDbDir(o)
	if dbDir == "" {
		return 0, fmt.Errorf("--large-objects 需要数据库目录（位置参数目录或 --datadir+--db-oid）")
	}
	applyAutoEncoding(o, dbDir) // v1.0.46：大对象文本按库编码解码
	version := catalog.DetectPgVersion(dbDir)
	if version == 0 {
		version = 12
	}
	isKB := catalog.IsKingbaseDatadir(dbDir)
	filter := objects.ParseLargeObjectOIDs(o.LargeObjectOIDs)
	entries, err := objects.CollectLargeObjects(o, dbDir, version, isKB, o.PageSize, filter)
	if err != nil {
		return 0, err
	}
	roleNames := catalog.BuildRoleNameMap(dbDir)
	startMs := util.NowMs()
	var w io.Writer = os.Stdout
	outPath := ""
	if o.Output != "" {
		outPath = o.Output
		if fi, err := os.Stat(o.Output); err == nil && fi.IsDir() || strings.HasSuffix(o.Output, "/") {
			outPath = filepath.Join(o.Output, "large_objects.sql")
		} else if filepath.Ext(o.Output) == "" {
			outPath = o.Output + ".sql"
		}
		if dir := filepath.Dir(outPath); dir != "" && dir != "." {
			os.MkdirAll(dir, 0755)
		}
		f, err := os.Create(outPath)
		if err != nil {
			return 0, fmt.Errorf("无法创建输出文件: " + err.Error())
		}
		defer f.Close()
		w = f
		if o.Verbose {
			logx.Logf("输出到: %s", outPath)
		}
	}
	n, err := objects.WriteLargeObjectsSQL(w, entries, roleNames)
	if err != nil {
		return 0, err
	}
	if f, ok := w.(*os.File); ok {
		if ferr := f.Sync(); ferr != nil {
			return 0, fmt.Errorf("写入输出文件失败: %v", ferr)
		}
	}
	if o.Verbose {
		logx.Logf("完成，共输出 %d 个大对象", n)
	}
	// report 条目 + 导入脚本
	reportSink.add(integrity.ExportReportEntry{
		Table: "pg_catalog.large_objects", Format: "sql", Rows: int64(n), BadBlocks: 0,
		File: outPath, Bytes: util.StatSize(outPath), Checksum: "",
		ElapsedMs: util.NowMs() - startMs, PageSize: o.PageSize, Encoding: currentTextEncoding(),
	})
	if o.ImportScript != "none" && outPath != "" {
		makeImportScript(o, &engine.TableMeta{Schema: "pg_catalog", RelName: "large_objects"}, outPath, "sql", nil, "")
	}
	return n, nil
}
