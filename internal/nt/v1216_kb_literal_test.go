// v1.2.16 回归（外部回归报告 D2）：金仓兼容模式内建日期时间类型的分区边界常量。
//
// 旧实现 ntLiteral 只认硬编码 PG OID：金仓 oracle 模式的 sys.date（8020）、
// mysql 模式的 date（7944）等一律返回空串 → DeparsePartBound 失败 →
// 分区子表退化为普通表（回灌后根表空、数据在独立表里）。
package nt

import (
	"encoding/binary"
	"strconv"
	"strings"
	"testing"
	"time"
)

func usFrom(y int, mo time.Month, d int) int64 {
	base := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	t := time.Date(y, mo, d, 0, 0, 0, 0, time.UTC)
	return int64(t.Sub(base) / time.Microsecond)
}

func TestV1216KbDateTimeLiteral(t *testing.T) {
	// 8020：金仓 oracle DATE（8B timestamp 格式）；服务端 pg_get_expr 输出
	// '2024-01-01 00:00:00'（V9R1C10 实测）。
	ts := make([]byte, 8)
	binary.LittleEndian.PutUint64(ts, uint64(usFrom(2024, 1, 1)))
	// 7944：金仓 mysql date（4B 天数，与 PG date 同格式）
	days := make([]byte, 4)
	binary.LittleEndian.PutUint32(days, uint32(usFrom(2024, 1, 1)/86400000000))

	cases := []struct {
		name string
		oid  uint32
		raw  []byte
		want string
	}{
		{"oracle date 8020", 8020, ts, "'2024-01-01 00:00:00'"},
		{"kingbase datetime 4189", 4189, ts, "'2024-01-01 00:00:00'"},
		{"v9 datetime 7952", 7952, ts, "'2024-01-01 00:00:00'"},
		{"mysql date 7944", 7944, days, "'2024-01-01'"},
	}
	for _, c := range cases {
		if got := ntLiteral(c.oid, c.raw); got != c.want {
			t.Errorf("%s: ntLiteral = %q，期望 %q", c.name, got, c.want)
		}
	}
}

// 未收录的 OID 仍须返回空串（调用方按"不可还原"处理），不得输出半成品。
func TestV1216KbDateTimeLiteralUnknownOID(t *testing.T) {
	if got := ntLiteral(999999, make([]byte, 8)); got != "" {
		t.Errorf("未知 OID 应返回空串，得到 %q", got)
	}
}

// 分区边界整体反解：oracle 模式 date 列的 RANGE 边界必须还原出 FOR VALUES。
// 夹具形态对齐 PG nodeToString（见 v1052_partition_test.go 的 pbQ1）。
func TestV1216PartBoundKbDate(t *testing.T) {
	lo := make([]byte, 8)
	hi := make([]byte, 8)
	binary.LittleEndian.PutUint64(lo, uint64(usFrom(2024, 1, 1)))
	binary.LittleEndian.PutUint64(hi, uint64(usFrom(2025, 1, 1)))
	raw := "{PARTITIONBOUNDSPEC :strategy r :is_default false :modulus 0 :remainder 0 " +
		":listdatums <> :lowerdatums (" + pbDatum(8020, lo) + ") :upperdatums (" + pbDatum(8020, hi) +
		") :location -1}"
	want := "FOR VALUES FROM ('2024-01-01 00:00:00') TO ('2025-01-01 00:00:00')"
	if got := DeparsePartBound(raw); got != want {
		t.Errorf("DeparsePartBound = %q，期望 %q", got, want)
	}
}

// pbDatum：CONST 节点（8B byval），constvalue 按 nodeToString 逐字节有符号渲染。
func pbDatum(oid uint32, b []byte) string {
	var sb strings.Builder
	sb.WriteString("{PARTITIONRANGEDATUM :kind 0 :value {CONST :consttype ")
	sb.WriteString(strconv.FormatUint(uint64(oid), 10))
	sb.WriteString(" :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true ")
	sb.WriteString(":constisnull false :location -1 :constvalue 8 [")
	for _, c := range b {
		sb.WriteString(" ")
		sb.WriteString(strconv.Itoa(int(int8(c))))
	}
	sb.WriteString(" ]} :location -1}")
	return sb.String()
}
