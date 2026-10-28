// Author: raysuen
package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ParseTblspcPath：识别表空间路径 <root>/pg_tblspc/<ts_oid>/<版本目录>/<dboid>/<relfilenode>
// （金仓为 sys_tblspc/；版本目录如 PG_16_202307071 / SYS_12_202511101）。
// 返回 (数据根目录, dboid, ok)；纯相对路径 "pg_tblspc/..." 以当前工作目录为数据根。
func ParseTblspcPath(p string) (string, int, bool) {
	if !strings.HasPrefix(p, "/") {
		if strings.HasPrefix(p, "pg_tblspc/") || strings.HasPrefix(p, "sys_tblspc/") {
			if cwd, err := os.Getwd(); err == nil {
				p = filepath.Join(cwd, p)
			}
		}
	}
	idx := -1
	for _, marker := range []string{"/pg_tblspc/", "/sys_tblspc/"} {
		if i := strings.Index(p, marker); i >= 0 {
			idx = i
			break
		}
	}
	if idx < 0 {
		return "", 0, false
	}
	rest := p[idx+1:]
	parts := strings.Split(rest, "/")
	// ["pg_tblspc", ts_oid, versiondir, dboid, relfilenode] 至少 5 段
	if len(parts) < 5 {
		return "", 0, false
	}
	dboid, err := strconv.Atoi(parts[3])
	if err != nil || dboid <= 0 {
		return "", 0, false
	}
	return p[:idx], dboid, true
}

// ResolveDataRoot：定位数据根目录（含 global/ 与 PG_VERSION/SYS_VERSION）
func ResolveDataRoot(o *Options) string {
	if o.Datadir != "" {
		if fi, err := os.Stat(o.Datadir); err == nil && fi.IsDir() {
			return o.Datadir
		}
	}
	if o.DataFile != "" {
		// 表空间路径 → 数据根
		if root, dboid, ok := ParseTblspcPath(o.DataFile); ok && root != "" {
			o.DBOID = dboid
			return root
		}
		cur := o.DataFile
		if fi, err := os.Stat(cur); err == nil && !fi.IsDir() {
			cur = filepath.Dir(cur)
		}
		for i := 0; i < 6; i++ {
			// 数据根充分特征：global/ 目录（base/<dboid> 数据库目录无 global）
			if fi, err := os.Stat(filepath.Join(cur, "global")); err == nil && fi.IsDir() {
				return cur
			}
			// 数据库目录特征（base/<dboid>，含 pg_filenode.map / sys_filenode.map / catalog 1247）：
			// PG17+ 该目录也有 PG_VERSION、金仓也有 SYS_VERSION，不能据版本文件误判为数据根
			// （v1.0.38 修复）——跳过继续向上
			dbDirMark := func() bool {
				for _, m := range []string{"pg_filenode.map", "sys_filenode.map", "1247"} {
					if _, err := os.Stat(filepath.Join(cur, m)); err == nil {
						return true
					}
				}
				return false
			}
			if dbDirMark() {
				parent := filepath.Dir(cur)
				if parent == cur {
					break
				}
				cur = parent
				continue
			}
			// 兼容兜底：global 目录缺失（异常/损坏数据根）时，版本文件仍可定位根
			if _, err := os.Stat(filepath.Join(cur, "PG_VERSION")); err == nil {
				return cur
			}
			if _, err := os.Stat(filepath.Join(cur, "SYS_VERSION")); err == nil {
				return cur
			}
			parent := filepath.Dir(cur)
			if parent == cur {
				break
			}
			cur = parent
		}
	}
	return ""
}

// ResolveTableFilePath：按表空间定位数据文件（v1.0.34）
//
//	tablespace == 0（默认表空间）→ <dbDir>/<relfilenode>
//	tablespace != 0             → <数据根>/pg_tblspc|<sys_tblspc>/<ts>/<版本目录>/<dboid>/<relfilenode>
//
// 版本目录探测：同一表空间可能被多套版本目录（PG_x / SYS_x）复用，
// 逐个扫描子目录，取存在 <dboid> 子目录者（最稳）。
// 返回 "" 表示定位失败（软链接悬空/目录缺失）。
func ResolveTableFilePath(o *Options, dbDir string, relfilenode uint32, tablespace uint32) string {
	if relfilenode == 0 {
		return ""
	}
	rf := strconv.FormatUint(uint64(relfilenode), 10)
	if tablespace == 0 {
		if dbDir == "" {
			return ""
		}
		return filepath.Join(dbDir, rf)
	}
	root := ResolveDataRoot(o)
	if root == "" && dbDir != "" {
		// dbDir = <root>/base/<oid>：向上两级取数据根
		root = filepath.Dir(filepath.Dir(dbDir))
	}
	if root == "" {
		return ""
	}
	// dboid 推导：dbDir 为 base/<oid> 时以目录名优先（位置参数模式），
	// 避免 Options 默认 DBOID=5 误用于其他库（金仓 test 库 OID≠5，v1.0.38 修复）
	dboid := 0
	if dbDir != "" {
		if v, err := strconv.Atoi(filepath.Base(dbDir)); err == nil {
			dboid = v
		}
	}
	if dboid == 0 {
		dboid = o.DBOID
	}
	if dboid == 0 {
		return ""
	}
	tsRoot := filepath.Join(root, "pg_tblspc")
	if isKingbaseDatadir(root) {
		tsRoot = filepath.Join(root, "sys_tblspc")
	}
	tsDir := filepath.Join(tsRoot, strconv.FormatUint(uint64(tablespace), 10))
	entries, err := os.ReadDir(tsDir)
	if err != nil {
		// 金仓/PG 名称差异兜底：反向再试一次
		alt := filepath.Join(root, "pg_tblspc")
		if tsRoot != alt {
			if e2, err2 := os.ReadDir(alt + "/" + strconv.FormatUint(uint64(tablespace), 10)); err2 == nil {
				entries, tsDir = e2, alt+"/"+strconv.FormatUint(uint64(tablespace), 10)
			}
		}
		if err != nil && len(entries) == 0 {
			return ""
		}
	}
	dbStr := strconv.Itoa(dboid)
	// 同一表空间可能被多套版本目录（PG_x/SYS_x）复用，且各自下都有同名 <dboid> 子目录。
	// 必须取"目标 relfilenode 文件真实存在"的版本目录，避免取到另一版本目录下的不存在路径
	// 误报"数据文件不存在"（v1.0.34 审查修复）。
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		verDir := filepath.Join(tsDir, e.Name())
		if fi, err := os.Stat(filepath.Join(verDir, dbStr)); err == nil && fi.IsDir() {
			cand := filepath.Join(verDir, dbStr, rf)
			if f2, err2 := os.Stat(cand); err2 == nil && f2.Mode().IsRegular() {
				return cand
			}
		}
	}
	return ""
}

// isKingbaseDatadir：判断数据根是否金仓（V8/V9 用 SYS_VERSION 标记，
// PG 用 PG_VERSION）。与 catalog.IsKingbaseDatadir 判定口径一致，
// 此处内置一份以避免 config 反向依赖 catalog。
func isKingbaseDatadir(datadir string) bool {
	d, _ := filepath.Abs(datadir)
	for range 6 {
		if _, err := os.Stat(filepath.Join(d, "SYS_VERSION")); err == nil {
			return true
		}
		if _, err := os.Stat(filepath.Join(d, "PG_VERSION")); err == nil {
			return false
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return false
}
