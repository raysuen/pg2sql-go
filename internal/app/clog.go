// v1.2.0：--clog 的参数解析、CLOG 可用性校验与单例缓存。
// v1.2.1：自检文案随默认路径语义变更（hint 缺失按活行导出）调整。
// Author: raysuen
package app

import (
	"pg2sql/internal/config"
	"pg2sql/internal/engine"
	"pg2sql/internal/logx"
	"sync"
	"sync/atomic"
)

// atomicLoad：RowIter 的统计字段由并行 worker 原子累加，这里统一原子读
func atomicLoad(p *int64) int64 { return atomic.LoadInt64(p) }

var (
	clogMu     sync.Mutex
	clogByDir  = map[string]*engine.ClogReader{}
	clogWarned = map[string]bool{}
)

// clogReaderFor 返回本次导出应使用的 CLOG reader；未开 --clog 返回 nil。
//
// 缓存按 datadir 分桶：批量导出（--tables/--schema）会对同一 datadir 反复
// 取 reader，重复打开段文件没意义。
func clogReaderFor(o *config.Options) *engine.ClogReader {
	if o == nil || !o.Clog {
		return nil
	}
	dd := o.Datadir
	if dd == "" {
		// 未给 --datadir 时无法定位 pg_xact，只能告警后降级
		clogMu.Lock()
		defer clogMu.Unlock()
		if !clogWarned[""] {
			clogWarned[""] = true
			logx.Logf("警告: --clog 需要 --datadir 才能定位 pg_xact/sys_xact，本次降级为仅用 hint bit 判定")
		}
		return nil
	}

	clogMu.Lock()
	defer clogMu.Unlock()
	if c, ok := clogByDir[dd]; ok {
		return c
	}
	c := engine.NewClogReader(dd)
	clogByDir[dd] = c

	if !c.Available() {
		if !clogWarned[dd] {
			clogWarned[dd] = true
			logx.Logf("警告: 在 %s 下未找到 pg_xact/sys_xact 目录，--clog 降级为仅用 hint bit 判定", dd)
		}
		return nil
	}

	if !clogWarned["ok:"+dd] {
		clogWarned["ok:"+dd] = true
		if nx, err := engine.ReadNextXid(dd); err == nil {
			logx.Logf("提示: --clog 已启用（%s），当前 nextXid=%d。CLOG 为延迟落盘，"+
				"请确保导出前已 CHECKPOINT 或数据库已停机，否则部分事务会读成 IN_PROGRESS 并按活行导出",
				c.Dir(), nx)
		} else {
			logx.Logf("提示: --clog 已启用（%s）。无法读取 nextXid（%v），跳过水位校验", c.Dir(), err)
		}
	}
	return c
}

// clogHintRatio：hint 缺失元组占比告警阈值。
//
// 依据 v1.2.0 验证结论：正常运行并做过 VACUUM 的库，hint 覆盖率接近 100%，
// 缺失率通常 <1%。超过 10% 基本只有三种成因——崩溃恢复中断的事务、物理
// 拷贝/裸设备导出的文件、hint 被第三方工具破坏过。
const clogHintWarnRatio = 0.10

// emitClogSelfCheck 在导出结束后输出自检结果。
//
// v1.2.1 语义变更：默认路径（未开 --clog）遇到 hint 缺失时，不再把「xmax 非 0」
// 直接当成已删除——那会把 DELETE+ROLLBACK 的残留元组整表丢掉。改为按活行导出，
// 因此自检要提醒的是「可能混入了已删除行」，而不是 v1.2.0 的「可能多导」。
func emitClogSelfCheck(table string, ri *engine.RowIter) {
	if ri == nil {
		return
	}
	missing := atomicLoad(&ri.HintMissingTuples)
	uncertain := atomicLoad(&ri.UncertainTuples)
	rows := atomicLoad(&ri.ProgressRows)
	if missing == 0 && ri.Clog == nil && uncertain == 0 {
		return
	}
	total := rows
	if total < missing {
		total = missing
	}
	ratio := 0.0
	if total > 0 {
		ratio = float64(missing) / float64(total)
	}

	if ri.Clog != nil {
		q, cm, ab, unk := ri.Clog.Stats()
		killed := atomicLoad(&ri.ClogKilledTuples)
		logx.Logf("提示: [%s] CLOG 复核：查询 %d 次（已提交 %d / 已回滚 %d / 不可判 %d），"+
			"剔除 hint-only 会误导的行 %d 条", table, q, cm, ab, unk, killed)
		if unk > 0 {
			logx.Logf("警告: [%s] 有 %d 个事务在 CLOG 中为 IN_PROGRESS 或不可判，"+
				"已按活行导出。若数据库在线运行，请先 CHECKPOINT 后重试", table, unk)
		}
		return
	}

	// 未开 --clog：分两种情况说明后果
	if uncertain > 0 {
		logx.Logf("警告: [%s] 有 %d 行 xmax 非 0 且缺少 hint bit，无法判定其是否已删除，"+
			"已按活行导出（宁可多导不丢行）。如需精确结果，请加 --clog --datadir <PGDATA> 复核",
			table, uncertain)
		return
	}
	if ratio >= clogHintWarnRatio {
		// v1.2.2 修复（BUG-9）：原文案只提「崩溃恢复 / 物理拷贝 / 裸设备导出」，
		// 但实测正常在线库的表（尤其刚建表/刚批量写入、尚未被 SELECT 触发
		// hint 回填的表，以及透明加密表）同样会出现高占比缺 hint —— 文案与
		// 现实不符，用户会误以为实例出了故障。补充说明其余常见成因。
		logx.Logf("警告: [%s] 约 %.1f%% 的元组缺少 hint bit（可见性结论来自启发式）。"+
			"常见成因：实例曾崩溃恢复、堆文件是物理拷贝/裸设备导出、"+
			"或该表在写入后尚未被查询过（hint 未回填，属正常现象）。"+
			"若需精确判定删除状态，建议加 --clog --datadir <PGDATA> 复核", table, ratio*100)
	}
}
