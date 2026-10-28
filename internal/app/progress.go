// Author: raysuen
package app

import (
	"fmt"
	"os"
	"pg2sql/internal/engine"
	"pg2sql/internal/util"
	"strings"
	"sync/atomic"
	"time"
)

// ---------- v1.0.40 --progress 进度报告 ----------
// 输出到 stderr（stdout 是数据通道，管道/重定向场景不污染数据流）；
// 终端 TTY 时 \r 原地覆盖，非 TTY（管道/重定向）时每条换行，便于日志留痕。
func startProgressReporter(ri *engine.RowIter, totalPages int, startMs int64) (stop func()) {
	stopCh := make(chan struct{})
	done := make(chan struct{})
	var stopped int32
	isTTY := false
	if fi, err := os.Stderr.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		isTTY = true
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				progressLine(ri, totalPages, startMs, isTTY, true)
				return
			case <-ticker.C:
				progressLine(ri, totalPages, startMs, isTTY, false)
			}
		}
	}()
	return func() {
		if atomic.CompareAndSwapInt32(&stopped, 0, 1) {
			close(stopCh)
			<-done
		}
	}
}

// progressLine：输出一行进度（页/行/耗时/ETA）
func progressLine(ri *engine.RowIter, totalPages int, startMs int64, isTTY bool, final bool) {
	pagesDone := atomic.LoadInt64(&ri.ProgressDone)
	rowsDone := atomic.LoadInt64(&ri.ProgressRows)
	elapsed := float64(util.NowMs()-startMs) / 1000.0
	pct := 0.0
	eta := 0.0
	if totalPages > 0 {
		pct = float64(pagesDone) / float64(totalPages) * 100.0
		if pagesDone > 0 {
			eta = elapsed / float64(pagesDone) * float64(int64(totalPages)-pagesDone)
		}
	}
	tbl := ""
	if ri.TM != nil {
		tbl = "  表 " + ri.TM.FullName()
	}
	line := fmt.Sprintf("\r[pg2sql] 进度 %5.1f%%%s  页 %d/%d  行 %d  耗时 %.1fs  ETA %.1fs   ",
		pct, tbl, pagesDone, totalPages, rowsDone, elapsed, eta)
	if !isTTY {
		line = strings.TrimSpace(line) + "\n"
	}
	fmt.Fprint(os.Stderr, line)
	if final || !isTTY {
		fmt.Fprint(os.Stderr, "\n")
	}
}

func timeNowMs() int64 {
	return time.Now().UnixNano() / 1e6
}
