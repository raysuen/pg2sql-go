// v1.2.6 P2 测试："char"(18) 高位字节 —— SQL 可逆转义 + CSV/text 计数告警。
// 作者: raysuen
package engine

import (
	"testing"

	"pg2sql/internal/datatype"
)

func TestV126CharHighByteSQL(t *testing.T) {
	// 孤立 0xC3（UTF-8 库 'É' 经 charin 存的首字节）
	col := &ColumnDef{Name: "c", TypeOID: datatype.CHAROID}
	got := sqlQuote(string([]byte{0xC3}), col)
	if got != `(-61)::"char"` {
		t.Fatalf("sqlQuote(char 0xC3) = %q，期望 (-61)::\"char\"", got)
	}
	// 0x80/0xFF/0x00 边界
	if got := sqlQuote(string([]byte{0x80}), col); got != `(-128)::"char"` {
		t.Fatalf("sqlQuote(char 0x80) = %q", got)
	}
	if got := sqlQuote(string([]byte{0xFF}), col); got != `(-1)::"char"` {
		t.Fatalf("sqlQuote(char 0xFF) = %q", got)
	}
	if got := sqlQuote(string([]byte{0x00}), col); got != `(0)::"char"` {
		t.Fatalf("sqlQuote(char NUL) = %q", got)
	}
	// ASCII 与 0x7F 走原有路径
	if got := sqlQuote("A", col); got != "'A'" {
		t.Fatalf("sqlQuote(char 'A') = %q", got)
	}
	if got := sqlQuote(string([]byte{0x7F}), col); got != `E'\x7F'` {
		t.Fatalf("sqlQuote(char 0x7F) = %q（0x7F 由 SQLStringLiteral 原有控制字符路径处理）", got)
	}
	// 非 char 类型的 0xC3 值不受影响（如 text 列已由 DecodeBytes 保证合法 UTF-8）
	txt := &ColumnDef{Name: "t", TypeOID: datatype.TEXTOID}
	if got := sqlQuote(string([]byte{0xC3}), txt); got != string([]byte{'\'', 0xC3, '\''}) {
		t.Fatalf("sqlQuote(text 0xC3) = % X，应保持原样（其他类型语义不同，不代为转义）", got)
	}
}

func TestV126CharHighByteFormatRowCounters(t *testing.T) {
	v := string([]byte{0xC3})
	ascii := "A"
	mkCtx := func(mode fmtMode) *fmtCtx {
		return &fmtCtx{
			mode:     mode,
			liveCols: []ColumnDef{{Name: "c", TypeOID: datatype.CHAROID}},
			outIdx:   []int{0},
			delim:    ",",
		}
	}
	row := &Row{Values: []*string{&v}}

	// SQL 路径不计数（已可逆转义）
	if out := mkCtx(fmtSQL).formatRow(row); out == "" {
		t.Fatal("formatRow(fmtSQL) 空输出")
	}
	if n := datatype.TakeCharHighByteCountForTest(); n != 0 {
		t.Fatalf("SQL 路径不应计数，实际 %d", n)
	}

	// CSV / text 路径各计 1
	for _, mode := range []fmtMode{fmtCSV, fmtText} {
		out := mkCtx(mode).formatRow(row)
		if len(out) == 0 || out[0] != 0xC3 {
			t.Fatalf("mode=%d 输出应含原始 0xC3 字节: % X", mode, out)
		}
	}
	if n := datatype.TakeCharHighByteCountForTest(); n != 2 {
		t.Fatalf("CSV+text 应累计计数 2，实际 %d", n)
	}

	// ASCII 值不计数
	rowA := &Row{Values: []*string{&ascii}}
	_ = mkCtx(fmtCSV).formatRow(rowA)
	_ = mkCtx(fmtText).formatRow(rowA)
	if n := datatype.TakeCharHighByteCountForTest(); n != 0 {
		t.Fatalf("ASCII 值不应计数，实际 %d", n)
	}

	// NULL 不计数
	rowN := &Row{Values: []*string{nil}}
	_ = mkCtx(fmtCSV).formatRow(rowN)
	if n := datatype.TakeCharHighByteCountForTest(); n != 0 {
		t.Fatalf("NULL 不应计数，实际 %d", n)
	}
}
