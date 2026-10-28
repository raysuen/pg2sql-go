// Author: raysuen
package app

import (
	"fmt"
	"os"
	"path/filepath"
	"pg2sql/internal/catalog"
	"pg2sql/internal/config"
	"pg2sql/internal/datatype"
	"pg2sql/internal/engine"
	"pg2sql/internal/logx"
	"strconv"
	"strings"
)

// ---------- list-db ----------
func listDatabases(o *config.Options) {
	var baseDir string
	if o.DataFile != "" {
		baseDir = o.DataFile
	} else if o.Datadir != "" {
		baseDir = filepath.Join(o.Datadir, "base")
	} else {
		logx.ErrorExit("--list-db 需要指定数据目录路径 (位置参数或 --datadir)")
	}
	fi, err := os.Stat(baseDir)
	if err != nil || !fi.IsDir() {
		logx.ErrorExit("目录不存在: " + baseDir)
	}
	baseDir, _ = filepath.Abs(baseDir)
	pgdata := filepath.Dir(baseDir)
	dbOIDs := []int{}
	entries, _ := os.ReadDir(baseDir)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if v, err := strconv.Atoi(e.Name()); err == nil {
			dbOIDs = append(dbOIDs, v)
		}
	}
	sortInts(dbOIDs)
	if len(dbOIDs) == 0 {
		logx.Logf("未在 base/ 下找到数据库目录")
		return
	}
	// 解析 pg_database
	dbNames := map[int]string{}
	gf := filepath.Join(pgdata, "global", "1262")
	if fi, err := os.Stat(gf); err == nil && fi.Mode().IsRegular() {
		version := catalog.DetectPgVersion(pgdata)
		if version == 0 {
			version = 16
		}
		isKB := catalog.IsKingbaseDatadir(pgdata)
		for tup := range engine.IterSysTuples(gf, version, isKB) {
			layout := catalog.WithOID([]datatype.ColLen{{AttLen: 64, IsVarlena: false, AttAlign: "c"}})
			nulls := tup.GetNulls()
			fields := datatype.ExtractFieldsDirect(tup.Raw, tup.THoff, nulls, layout)
			if len(fields) < 2 || fields[0] == nil || fields[1] == nil {
				continue
			}
			oid := int(binaryLEU32(*fields[0]))
			name := datatype.CString(*fields[1], 0)
			if oid != 0 && name != "" {
				dbNames[oid] = name
			}
		}
	}
	// 扫描表空间目录：<root>/pg_tblspc|<sys_tblspc>/<ts_oid>/<版本目录>/<dboid>
	// 存在该层级即说明库使用了该表空间（v1.0.34 多行 filepath）
	dbTblspcs := map[int][]string{}
	pgdataRoot := filepath.Dir(baseDir)
	for _, tspName := range []string{"pg_tblspc", "sys_tblspc"} {
		tspRoot := filepath.Join(pgdataRoot, tspName)
		tsEntries, err := os.ReadDir(tspRoot)
		if err != nil {
			continue
		}
		for _, tsE := range tsEntries {
			// 表空间目录通常是软链接（pg_tblspc/<oid> → 真实目录），
			// DirEntry.IsDir() 对 symlink 返回 false，必须 os.Stat follow 判断。
			tsFull := filepath.Join(tspRoot, tsE.Name())
			if fi, err := os.Stat(tsFull); err != nil || !fi.IsDir() {
				continue
			}
			if _, err := strconv.Atoi(tsE.Name()); err != nil {
				continue
			}
			verEntries, err := os.ReadDir(tsFull)
			if err != nil {
				continue
			}
			for _, verE := range verEntries {
				if !verE.IsDir() {
					continue
				}
				dbSub := filepath.Join(tspRoot, tsE.Name(), verE.Name())
				dbEntries, err := os.ReadDir(dbSub)
				if err != nil {
					continue
				}
				for _, dbE := range dbEntries {
					if !dbE.IsDir() {
						continue
					}
					if v, err := strconv.Atoi(dbE.Name()); err == nil && v != 0 {
						dbTblspcs[v] = append(dbTblspcs[v], filepath.Join(tspName, tsE.Name(), verE.Name(), dbE.Name()))
					}
				}
			}
		}
	}
	fmt.Printf("%-12s %-25s %s\n", "OID", "数据库名称", "filepath")
	fmt.Println(strings.Repeat("-", 90))
	for _, oid := range dbOIDs {
		name, ok := dbNames[oid]
		if !ok {
			name = "(未知)"
		}
		// 第一行：默认表空间 base/<oid>；后续每行：一个表空间（多行显示）
		fmt.Printf("%-12d %-25s %s\n", oid, name, filepath.Join("base", strconv.Itoa(oid)))
		for _, tsPath := range dbTblspcs[oid] {
			fmt.Printf("%-12s %-25s %s\n", "", "", tsPath)
		}
	}
}

// ---------- list-tables-db / list-tables-all ----------
// includeSystem=false：只列用户对象（默认）；true：列出全部对象（含系统对象）
func listTablesInDB(o *config.Options, includeSystem bool) {
	dbDir := resolveDbDir(o)
	if dbDir == "" {
		logx.ErrorExit("--list-tables-db/--list-tables-all 需要数据库目录")
	}
	ps := o.PageSize
	_, tables := catalog.AutoDiscoverAllTables(dbDir, ps)
	// 按表名排序，输出稳定
	byName := map[string]*engine.TableMeta{}
	names := make([]string, 0, len(tables))
	for _, tm := range tables {
		fn := tm.FullName()
		if includeSystem || !isSystemSchema(fn) {
			if _, ok := byName[fn]; !ok {
				byName[fn] = tm
				names = append(names, fn)
			}
		}
	}
	sortStrings(names)
	type trow struct {
		name string
		rel  uint32
		kind string
		ts   uint32 // 表空间：0=默认表空间（base/），非 0=pg_tblspc/<oid>（v1.0.34）
		n    int
	}
	rows := make([]trow, 0, len(names))
	for _, n := range names {
		tm := byName[n]
		ncol := 0
		for _, c := range tm.Columns {
			if !c.Dropped {
				ncol++
			}
		}
		rows = append(rows, trow{tm.FullName(), tm.RelFileNode, tm.RelKind, tm.TableSpace, ncol})
	}
	// 定长列（relfilenode/relkind/表空间/列数）动态对齐在前，不定长的表名放最后
	w2, w3 := 11, 7 // relfilenode/relkind（字节宽）
	wts := len("表空间")
	for _, r := range rows {
		l := len(strconv.Itoa(int(r.rel)))
		if l > w2 {
			w2 = l
		}
		if len(r.kind) > w3 {
			w3 = len(r.kind)
		}
		tsLabel := "base"
		if r.ts != 0 {
			tsLabel = strconv.Itoa(int(r.ts))
		}
		if len(tsLabel) > wts {
			wts = len(tsLabel)
		}
	}
	w4 := len(strconv.Itoa(len(rows)))
	if len(rows) > 0 {
		for _, r := range rows {
			l := len(strconv.Itoa(r.n))
			if l > w4 {
				w4 = l
			}
		}
	}
	fmt.Printf("%-*s %-*s %-*s %-*s  %s\n", w2, "relfilenode", w3, "relkind", wts, "表空间", w4, "列数", "表名")
	fmt.Println(strings.Repeat("-", w2+w3+wts+w4+6))
	for _, r := range rows {
		tsLabel := "base"
		if r.ts != 0 {
			tsLabel = strconv.Itoa(int(r.ts))
		}
		fmt.Printf("%-*d %-*s %-*s %-*d  %s\n", w2, r.rel, w3, r.kind, wts, tsLabel, w4, r.n, r.name)
	}
}
