// 金仓 mysql 模式「本地时间不带偏移」的可发现性提示（v1.2.19）。
//
// 背景：mysql 模式的 timestamptz 与 timestamp（oid 7954）导出为**本地时间、不带偏移**
// （v1.2.16 D7 对齐 mysql DATETIME 文本，见 datatype.decodeTimestamptz）。此时时区不在
// 文本里，只能由会话承载，而各载体能力不同：
//
//   - sql 格式：文件头有 `SET TIME ZONE '...'`（v1.2.18 首轮）——安全；
//   - csv / txt：按设计不带会话头（纯净数据文件，可被任意工具读）；
//     靠 `--import-script` 生成的脚本注入 `export PGTZ=...`（v1.2.18 R2-①）——安全；
//   - csv / txt 且**不使用** --import-script：回灌时按目标端默认时区解释字面量，
//     +08 → UTC 即整体平移 8 小时，且 `\copy` 不报错——这是唯一残留缺口。
//
// v1.2.19 决策：**不改导出文本**，只补一次 stderr 提示。
//   - 改文本（恒输出 `+00` 绝对形态）技术上可行——实测金仓 mysql 模式能解析带偏移
//     字面量并按会话时区正确换算——但会与 `ksql COPY` 输出不一致（用户无法逐字对照
//     验证），且真 MySQL 的 DATETIME 严格模式会拒绝，失去"兼容 mysql 语义"的立身之本；
//   - 缺口只在"手工回灌且不设 PGTZ"这一种用手方式，属可发现性问题，
//     用告警消除比改数据契约划算得多。
package app

import (
	"sync"

	"pg2sql/internal/config"
	"pg2sql/internal/datatype"
	"pg2sql/internal/engine"
	"pg2sql/internal/logx"
)

const (
	// oidTimestamptz：PG / 金仓共用的 timestamptz。
	oidTimestamptz = 1184
	// oidMysqlTimestamp：金仓 V9 mysql 模式 timestamp（MySQL TIMESTAMP 语义，
	// 存 UTC、按会话时区渲染），与 PG 的 timestamp(1114) 语义不同。
	oidMysqlTimestamp = 7954
)

// mysqlTZHintOnce：同一次运行只提示一次（--jobs 并发 / 多表导出下不刷屏）。
var mysqlTZHintOnce sync.Once

// hasMysqlLocalTimeColumn：表中是否存在"按会话时区渲染、文本不带偏移"的列。
//
// 只有这两类：timestamptz(1184) 与 mysql timestamp(7954)。
// PG 的 timestamp(1114)、time、date、datetime(7952)、interval 都与会话时区无关，
// 不需要提示。
func hasMysqlLocalTimeColumn(tm *engine.TableMeta) bool {
	if tm == nil {
		return false
	}
	for _, c := range tm.Columns {
		if c.Dropped {
			continue
		}
		if c.TypeOID == oidTimestamptz || c.TypeOID == oidMysqlTimestamp {
			return true
		}
	}
	return false
}

// needMysqlTZHint：是否需要提示。抽成纯函数便于单测（不依赖 stderr 捕获）。
//
// 四个条件必须同时成立：
//  1. 金仓 mysql 兼容模式（其它模式的时间文本自带偏移或与会话时区无关）；
//  2. 正在输出**裸数据**（o.Data）且不是 sql 格式（o.SQL）——
//     即 --data --format csv/txt。sql 格式自带 SET TIME ZONE 头，无需提示；
//  3. 未指定 --import-script（none/空）——指定了则由脚本注入 PGTZ，已经安全；
//  4. 表里确实有受影响的列（避免对纯数字表 / 纯 date 表打扰）。
func needMysqlTZHint(o *config.Options, tm *engine.TableMeta) bool {
	if o == nil || !datatype.IsKbMysqlMode() {
		return false
	}
	if !o.Data || o.SQL {
		return false
	}
	if o.ImportScript != "" && o.ImportScript != "none" {
		return false
	}
	return hasMysqlLocalTimeColumn(tm)
}

// warnMysqlTZHintIfNeeded：条件成立时向 stderr 提示一次。
//
// 不改变任何输出字节、不改变退出码 —— 导出结果与无提示时完全一致。
func warnMysqlTZHintIfNeeded(o *config.Options, tm *engine.TableMeta) {
	if !needMysqlTZHint(o, tm) {
		return
	}
	mysqlTZHintOnce.Do(func() {
		tz := datatype.MySQLLocalTZEnvValue()
		if tz == "" {
			// 防御分支：mysql 模式下恒非空（未知时区也回退 +08:00，
			// 与字面量渲染 / SET TIME ZONE 同源，见 datatype.MySQLLocalTZEnvValue）。
			tz = "UTC"
		}
		logx.Logf("提示: 金仓 mysql 模式的 timestamptz / timestamp 导出为本地时间且不带偏移（当前时区 %s）；"+
			"手工回灌（\\copy / psql -f）前请先 export PGTZ=%s，或改用 --import-script 生成带 PGTZ 的导入脚本；"+
			"--format sql 自带 SET TIME ZONE 头，不受此影响", tz, tz)
	})
}
