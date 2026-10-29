//go:build repro

// v1.0.62 深度审计复现用例（catalog 侧）
// A-4：PG10 及更早（含早期金仓 V8）的 pg_index 布局没有 indnkeyatts，
// hoff+10/11 是 indisunique/indisprimary 两个 bool。indexFields 用
// "nkey <= 32 && nkey <= natts" 猜布局，普通索引（值 0/1）会误落入 PG11+ 分支。
package catalog

import (
	"encoding/binary"
	"testing"

	"pg2sql/internal/engine"
)

// oldLayoutIndexTuple 构造 PG≤10 的 pg_index 元组（hoff=24）。
// 列偏移：indnatts@8、indisunique@10、indisprimary@11、indisexclusion@12、
// indimmediate@13、indisclustered@14、indisvalid@15 … indkey@20。
func oldLayoutIndexTuple(unique, primary bool) *engine.HeapTuple {
	const hoff = 24
	raw := make([]byte, hoff+32)
	binary.LittleEndian.PutUint32(raw[hoff:], 50001)   // indexrelid
	binary.LittleEndian.PutUint32(raw[hoff+4:], 50000) // indrelid
	binary.LittleEndian.PutUint16(raw[hoff+8:], 1)     // indnatts = 1

	set := func(off int, v bool) {
		if v {
			raw[hoff+off] = 1
		}
	}
	set(10, unique)  // indisunique
	set(11, primary) // indisprimary
	set(12, false)   // indisexclusion
	set(13, true)    // indimmediate（非延迟索引恒为真）
	set(14, false)   // indisclustered
	set(15, true)    // indisvalid
	set(16, false)   // indcheckxmin
	set(17, true)    // indisready
	set(18, true)    // indislive
	set(19, false)   // indisreplident
	return &engine.HeapTuple{Raw: raw, THoff: hoff}
}

func TestReproA4OldLayoutPlainIndex(t *testing.T) {
	ir := indexFields(oldLayoutIndexTuple(false, false), 9, false)
	if ir == nil {
		t.Fatal("indexFields 返回 nil")
	}
	if ir.IndIsUnique {
		t.Errorf("普通索引被解析成唯一索引")
	}
	if ir.IndIsPrimary {
		t.Errorf("普通索引被解析成主键索引（IndIsPrimary=true）→ 会被当作主键过滤，索引整段丢失")
	}
}

func TestReproA4OldLayoutUniqueIndex(t *testing.T) {
	ir := indexFields(oldLayoutIndexTuple(true, false), 9, false)
	if ir == nil {
		t.Fatal("indexFields 返回 nil")
	}
	if !ir.IndIsUnique {
		t.Errorf("唯一索引被解析成普通索引（IndIsUnique=false）→ 唯一性约束丢失")
	}
	if ir.IndIsPrimary {
		t.Errorf("唯一索引被解析成主键索引（IndIsPrimary=true）")
	}
}

func TestReproA4OldLayoutPrimaryIndex(t *testing.T) {
	// 主键索引值 = indisunique + 256*indisprimary = 257 > 32，落到 else 分支，应当正确
	ir := indexFields(oldLayoutIndexTuple(true, true), 9, false)
	if ir == nil {
		t.Fatal("indexFields 返回 nil")
	}
	if !ir.IndIsUnique || !ir.IndIsPrimary {
		t.Errorf("主键索引解析错误：unique=%v primary=%v", ir.IndIsUnique, ir.IndIsPrimary)
	}
}
