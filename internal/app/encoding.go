// Author: raysuen
package app

import (
	"os"
	"path/filepath"
	"pg2sql/internal/catalog"
	"pg2sql/internal/charset"
	"pg2sql/internal/config"
	"pg2sql/internal/engine"
	"pg2sql/internal/logx"
	"strconv"
	"strings"
)

func currentTextEncoding() string { return charset.CurTextEncoding() }

// encName3state：内部编码名 → 三态探测名（utf8/gbk）；其余编码不参与表级覆盖。
func encName3state(enc string) string {
	switch normalizeEncoding(enc) {
	case "utf-8":
		return "utf8"
	case "gbk":
		return "gbk"
	}
	return ""
}

// refineTableEncoding：表级编码精化（v1.0.52 引入，v1.0.54 改为双向）。
// 缺陷背景：applyAutoEncoding 的库级 UTF8 回退（catalog.ProbeDirUtf8Ratio）只采样
// 全库最大的 3 个数据文件——同库的历史 GBK/UTF8 大表会挤占采样名额，导致
// 实存另一种编码的目标表按库级判定解码，整表中文乱码。
//
// v1.0.54 修复 C4：原实现只在「库级判 GBK、本表实存 UTF8」这一方向覆盖，
// 且库级一旦被判成 UTF8 就完全跳过精化——混合存储库（既有 UTF8 字节表又有真 GBK
// 表）里真 GBK 表会被整表解成 Latin-1 乱码，退出码 0 且无告警。现改为双向：
// 用目标表自身数据文件的「多页投票」判定（probeFileEncodingMajority），
// 只要与库级判定不一致就按表覆盖，并对两种方向都给出明确提示。
func refineTableEncoding(o *config.Options, dbDir string, tm *engine.TableMeta, tablePath string, ps int) {
	if tm.EncOverride != "" || o.EncBase == "" {
		return
	}
	if !(o.Encoding == "" || o.Encoding == "auto") || dbDir == "" || !catalog.IsKingbaseDatadir(dbDir) {
		return
	}
	base3 := encName3state(o.EncBase)
	if base3 == "" {
		return // 库级既非 utf8 也非 gbk（如 EUC_CN/LATIN1），不参与三态精化
	}
	fi, err := os.Stat(tablePath)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() < 1024 {
		return
	}
	got := charset.ProbeFileEncodingMajority(tablePath, ps)
	if got == "" || got == base3 {
		return
	}
	tm.EncOverride = got
	logx.Logf("提示: 表 %s 数据字节呈 %s 特征（库级判定 %s，同库其他表可能污染库级采样），本表按 %s 解码；如非预期可用 --encoding 强制",
		tm.FullName(), strings.ToUpper(got), strings.ToUpper(base3), strings.ToUpper(got))
}

// applyAutoEncoding：自动编码探测并使生效（v1.0.46 抽取为公共函数）。
//
// 旧实现只在"单表导出"主流程内做探测，而 --tables/--all-tables/--schema 批量导出、
// --full-schema、--large-objects 走各自的入口函数，从未设置库编码 → 非 UTF-8 库
// （EUC_CN/LATIN1/GBK...）在批量模式下按默认 UTF-8 解码失败后逐字节 latin-1 兜底，
// 中文全部变乱码（实测 PG17 EUC_CN 库 t_gbk 表：'金仓中文测试EUC编码' → '½ð²ÖÖÐÎĲâÊÔEUC±àÂë'）。
//
// 优先级：--encoding 显式指定（已在 main 提前 setTextEncoding）> 库声明编码 > 单文件字节特征兜底。
func applyAutoEncoding(o *config.Options, dbDir string) {
	if o.Encoding != "" && o.Encoding != "auto" {
		return
	}
	root := config.ResolveDataRoot(o)
	dboid := o.DBOID
	if dbDir != "" {
		if v, err := strconv.Atoi(filepath.Base(dbDir)); err == nil {
			dboid = v // 位置参数 base/<oid> 模式以目录名优先
		}
	}
	enc := ""
	if root != "" && dboid != 0 {
		enc = catalog.DetectDatabaseEncoding(root, dboid)
	}
	if enc == "" && o.DataFile != "" {
		// v1.0.44 边界①：脱离数据根的单文件模式（拷出文件直接导出）——
		// 读不到 pg_database 声明，对该文件末尾一页做三态字节特征分类兜底，
		// 避免"无提示默认 UTF8 → GBK 库乱码"（V8 实存 UTF8 → utf8；V9 实存 GBK → gbk）。
		if fi, err := os.Stat(o.DataFile); err == nil && !fi.IsDir() {
			pe := charset.ProbeFileEncoding(o.DataFile, o.PageSize)
			if pe == "utf8" || pe == "gbk" {
				charset.SetTextEncoding(pe)
				logx.Logf("提示: 无法读取库编码声明（单文件模式），按数据字节特征使用 %s（如非预期可用 --encoding 强制）", pe)
			} else {
				logx.Logf("警告: 无法确认库编码（单文件模式），默认按 UTF-8 处理；如乱码请用 --encoding gbk/utf8 强制")
			}
			enc = pe
		}
	}
	if enc == "" {
		return
	}
	// v1.0.42：声明非 UTF8 时做字节特征校验（金仓"标 GBK 实存 UTF8"场景自动回退）；
	// 该回退只对金仓有意义——PG 在写入时已按库编码转换，声明即真实，不应推翻。
	if enc != "utf8" && dbDir != "" && catalog.IsKingbaseDatadir(dbDir) {
		if catalog.ProbeDirUtf8Ratio(dbDir, o.PageSize) {
			logx.Logf("提示: 库声明编码 %s 但数据字节呈 UTF8 特征，已自动切换 UTF8（如非预期可用 --encoding %s 强制）", enc, enc)
			enc = "utf8"
		}
	}
	charset.SetTextEncoding(normalizeEncoding(enc))
	if o.Verbose {
		logx.Logf("库编码: %s", currentTextEncoding())
	}
}

func normalizeEncoding(enc string) string {
	e := strings.ToLower(strings.TrimSpace(enc))
	switch e {
	case "utf8", "utf-8", "unicode", "utf_8":
		return "utf-8"
	case "latin1", "latin-1", "iso8859-1", "iso-8859-1", "latin_1", "sql_ascii", "sql-ascii":
		return "latin1"
	case "gbk", "gb2312", "936":
		return "gbk"
	case "gb18030":
		return "gb18030"
	}
	return e
}
