package app

// v1.0.54 回归测试（DDL 生成侧）：C1 父表 DDL 前置。
//
// 单表模式导出分区子表时，必须先有父表的 CREATE TABLE ... PARTITION BY，
// 否则子表的 PARTITION OF 无处可依，导入直接失败。密文页/编码投票用例
// 分别在 internal/engine/v1054_fix_test.go、internal/charset/v1054_encoding_test.go。

import (
	"strings"
	"testing"

	"pg2sql/internal/engine"
)

// 分区父表 DDL 必须带 PARTITION BY；子表必须是 PARTITION OF（两者组合才可导入）
func TestV1054PartitionDDLChain(t *testing.T) {
	parent := &engine.TableMeta{
		Schema: "public", RelName: "t_part", RelKind: "p",
		Columns:   []engine.ColumnDef{{Name: "id", AttNum: 1, TypeOID: 23}, {Name: "region", AttNum: 2, TypeOID: 25}},
		PartStrat: 'r', PartKeyCols: []int{2},
	}
	pddl := generateDDL(parent)
	if !strings.Contains(pddl, "PARTITION BY RANGE") {
		t.Fatalf("父表 DDL 缺 PARTITION BY: %s", pddl)
	}
	child := &engine.TableMeta{
		Schema: "public", RelName: "t_part_cn", RelKind: "r",
		Columns:        []engine.ColumnDef{{Name: "id", AttNum: 1, TypeOID: 23}},
		PartParent:     1,
		PartParentQual: `"public"."t_part"`,
		PartBound:      "FOR VALUES FROM ('CN') TO ('CZ')",
	}
	cddl := generateDDL(child)
	if !strings.Contains(cddl, "PARTITION OF") {
		t.Fatalf("子表 DDL 应为 PARTITION OF: %s", cddl)
	}
}
