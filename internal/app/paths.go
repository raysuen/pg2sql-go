// Author: raysuen
package app

import (
	"fmt"
	"os"
	"path/filepath"
	"pg2sql/internal/config"
	"pg2sql/internal/engine"
	"pg2sql/internal/integrity"
	"pg2sql/internal/logx"
	"strconv"
	"strings"
)

// singleFileArg：位置参数是否为"单文件"（裸数据文件）而非数据库目录。
// 目录 → false（可走 --full-schema/--large-objects 独立分支）；文件 → true（需 --table-name 解析列信息）。
// v1.0.55 B2：路径不存在此前无条件按"单文件"处理（注释声称按扩展形态兜底，实现并未判断），
// 结合 --datadir 时 catalog 解析成功但表文件路径错 → 静默导出 0 行、退出码 0（实测 fsm 文件
// 与无效位置参数两种场景均复现）。不存在的路径没有任何合法解释，直接硬报错（对齐 --schema
// 等参数错误的"硬报错"语义）。
func singleFileArg(o *config.Options) bool {
	if o.DataFile == "" {
		return false
	}
	if fi, err := os.Stat(o.DataFile); err == nil {
		return !fi.IsDir()
	}
	logx.ErrorExit(fmt.Sprintf("位置参数路径不存在: %s（若为数据库目录请检查拼写；单文件模式请指向 base/<oid>/<relfilenode> 表数据文件；fsm/vm 为辅助文件不能作为输入）", o.DataFile))
	return true
}

func resolveDbDir(o *config.Options) string {
	if o.Datadir != "" {
		if o.DBOID != 0 {
			return filepath.Join(o.Datadir, "base", strconv.Itoa(o.DBOID))
		}
		return o.Datadir
	}
	if o.DataFile != "" {
		// 表空间路径（pg_tblspc/sys_tblspc 前缀）→ 数据根 + dboid，自动发现 catalog
		// v1.0.62 修复（P2-3）：不再回写 o.DBOID（批量 --jobs 共享 Options，写 = 数据竞态），
		// 直接用解析出的 dboid 拼库目录。
		if root, dboid, ok := config.ParseTblspcPath(o.DataFile); ok && root != "" {
			return filepath.Join(root, "base", strconv.Itoa(dboid))
		}
		// 数据文件可能是 base/<dboid>/<rel> 或 base/<dboid> 或 datadir
		d := filepath.Dir(o.DataFile)
		if fi, err := os.Stat(o.DataFile); err == nil && fi.IsDir() {
			return o.DataFile
		}
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			// 检查 d 是否为数据库目录（含 1247/1259）
			if _, err := os.Stat(filepath.Join(d, "1259")); err == nil {
				return d
			}
			if _, err := os.Stat(filepath.Join(d, "1247")); err == nil {
				return d
			}
			// 可能是 datadir/base/<dboid> 的父级
			if _, err := os.Stat(filepath.Join(d, "global")); err == nil {
				return d
			}
		}
		// 向上找
		cur := filepath.Dir(o.DataFile)
		for range 4 {
			if _, err := os.Stat(filepath.Join(cur, "1259")); err == nil {
				return cur
			}
			if _, err := os.Stat(filepath.Join(cur, "PG_VERSION")); err == nil {
				return filepath.Join(cur, "base", strconv.Itoa(o.DBOID))
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

// sanitizeFileComponent：把标识符（schema/表名）转成安全的单层文件名组件。
// v1.0.60 修复（P2-12）：schema/表名来自目标库目录（可被构造），此前直接拼进
// 输出路径，名称里带 "/" 或 ".." 时可写出 -o 目录之外（甚至覆盖数据目录文件）。
func sanitizeFileComponent(s string) string {
	if s == "" {
		return "_"
	}
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '/', '\\', 0:
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	out := b.String()
	// "." / ".." / "..." 之类的纯点组件在 Join 中会被当成相对路径
	if strings.Trim(out, ".") == "" {
		return "_"
	}
	return out
}

// resolveOutputPath：解析 --output 目标路径（对齐 Python _resolve_output_path）
//   - output 以 / 结尾或已是目录 → <目录>/<schema>.<table>.<ext>
//   - output 带任意扩展名 → 视为完整文件路径直接使用
//   - 否则 → 视为文件前缀，追加 .<ext>
func resolveOutputPath(output string, tm *engine.TableMeta, fileType string) string {
	ext := "txt"
	switch fileType {
	case "sql":
		ext = "sql"
	case "ddl":
		ext = "ddl"
	case "csv":
		ext = "csv"
	}
	// v1.0.60（P2-12）：文件名组件消毒，防 ../ 穿越
	base := sanitizeFileComponent(tm.Schema) + "." + sanitizeFileComponent(tm.RelName) + "." + ext
	if strings.HasSuffix(output, "/") {
		return filepath.Join(output, base)
	}
	if fi, err := os.Stat(output); err == nil && fi.IsDir() {
		return filepath.Join(output, base)
	}
	if filepath.Ext(output) != "" {
		return output
	}
	return output + "." + ext
}

// ddlOutputPath：根据数据文件路径推导 DDL 独立文件路径，保证与数据文件不碰撞。
//   - 常规：同主名换 .ddl 后缀（x.csv → x.ddl）
//   - v1.0.49 修复（G2）：-o x.ddl 且 --ddl --data 时 outPath 本身就是 .ddl，
//     兜底路径再次碰撞 → DDL 与数据写同一文件互相覆盖。改用 x.schema.ddl。
func ddlOutputPath(outPath string) string {
	ddlPath := strings.TrimSuffix(outPath, filepath.Ext(outPath)) + ".ddl"
	if ddlPath == outPath {
		ddlPath = strings.TrimSuffix(outPath, ".ddl") + ".schema.ddl"
	}
	return ddlPath
}

// 系统 schema 前缀（错误提示时过滤，避免刷屏）
// v1.0.62（P3）：口径统一到 integrity.IsSystemSchemaTable，避免"批量过滤"
// 与"坏块归属统计"用两套名单（系统表坏块被算到用户 verdict → 假阳性 fail）。
func isSystemSchema(name string) bool {
	if strings.IndexByte(name, '.') < 0 {
		return true // 无 schema 前缀（如 relfilenode 数字键）视为系统
	}
	return integrity.IsSystemSchemaTable(name)
}

func listTableNames(tables map[string]*engine.TableMeta) string {
	names := make([]string, 0)
	user := make([]string, 0)
	for n := range tables {
		if isSystemSchema(n) {
			names = append(names, n)
		} else {
			user = append(user, n)
		}
	}
	sortStrings(user)
	sortStrings(names)
	if len(user) > 0 {
		return strings.Join(user, ", ") + " (另有 " + strconv.Itoa(len(names)) + " 张系统表)"
	}
	return strings.Join(names, ", ")
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func binaryLEU32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

func sortInts(s []int) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
