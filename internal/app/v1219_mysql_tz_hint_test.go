// v1.2.19 用例：mysql 模式 csv/txt 无脚本导出时的时区提示。
//
// 缺陷性质是「可发现性」而非「值错误」：mysql 模式的 timestamptz / timestamp(7954)
// 导出为本地时间、不带偏移（v1.2.16 D7），载体只有会话；sql 格式有 SET TIME ZONE 头，
// csv/txt + --import-script 有 PGTZ。唯一缺口是「csv/txt 且不用脚本」，此时手工 \copy
// 会按目标默认时区静默平移。v1.2.19 只补提示，**不改任何输出字节**。
package app

import (
	"testing"

	"pg2sql/internal/config"
	"pg2sql/internal/engine"
)

// metaWithOIDs：构造只带列类型 OID 的 TableMeta（提示判定只看 TypeOID）
func metaWithOIDs(oids ...uint32) *engine.TableMeta {
	tm := &engine.TableMeta{Schema: "public", RelName: "t", RelKind: "r"}
	for i, oid := range oids {
		tm.Columns = append(tm.Columns, engine.ColumnDef{Name: "c", TypeOID: oid, AttNum: i + 1})
	}
	return tm
}

// csvDataOpts：--data --format csv 的等价形态
func csvDataOpts(importScript string) *config.Options {
	return &config.Options{Data: true, SQL: false, ImportScript: importScript}
}

// 正向：mysql 模式 + csv + 无脚本 + 含 timestamptz → 必须提示
func TestV1219HintMysqlCsvTimestamptz(t *testing.T) {
	setMysqlMode(t, true)
	setTZ(t, "Asia/Shanghai")

	if !needMysqlTZHint(csvDataOpts("none"), metaWithOIDs(23, 1184)) {
		t.Error("mysql 模式导出含 timestamptz 的 csv 且未用 --import-script，应当提示")
	}
}

// 正向：mysql 模式的 timestamp(7954) 同样受会话时区影响，也要提示
func TestV1219HintMysqlCsvMysqlTimestamp(t *testing.T) {
	setMysqlMode(t, true)

	if !needMysqlTZHint(csvDataOpts(""), metaWithOIDs(23, 7954)) {
		t.Error("mysql 模式的 timestamp(7954) 按会话时区渲染，应当提示")
	}
}

// 反向：表里没有受影响类型（只有 int/date/timestamp1114/datetime7952）→ 不打扰
func TestV1219HintNoAffectedColumn(t *testing.T) {
	setMysqlMode(t, true)

	// 1114=PG timestamp（字面量），7952=mysql datetime（无时区），1082=date
	for _, oid := range []uint32{23, 1114, 7952, 1082, 1083} {
		if needMysqlTZHint(csvDataOpts("none"), metaWithOIDs(oid)) {
			t.Errorf("类型 OID %d 与会话时区无关，不该提示", oid)
		}
	}
}

// 反向：已用 --import-script（脚本会注入 PGTZ）→ 不提示
func TestV1219HintSuppressedByImportScript(t *testing.T) {
	setMysqlMode(t, true)

	for _, s := range []string{"csv", "sql", "auto"} {
		if needMysqlTZHint(csvDataOpts(s), metaWithOIDs(1184)) {
			t.Errorf("--import-script %s 已注入 PGTZ，不该提示", s)
		}
	}
}

// 反向：sql 格式自带 SET TIME ZONE 头 → 不提示
func TestV1219HintSuppressedOnSqlFormat(t *testing.T) {
	setMysqlMode(t, true)

	o := &config.Options{Data: false, SQL: true, ImportScript: "none"}
	if needMysqlTZHint(o, metaWithOIDs(1184)) {
		t.Error("--format sql 自带 SET TIME ZONE 头，不该提示")
	}
}

// 反向：PG 模式 timestamptz 恒带 +00，不提示
func TestV1219HintSuppressedOnPgMode(t *testing.T) {
	setMysqlMode(t, false)

	if needMysqlTZHint(csvDataOpts("none"), metaWithOIDs(1184)) {
		t.Error("PG 模式 timestamptz 输出带 +00 偏移，不该提示")
	}
}

// 反向：已删除列（Dropped）不参与判定，避免幽灵列触发提示
func TestV1219HintIgnoresDroppedColumn(t *testing.T) {
	setMysqlMode(t, true)

	tm := &engine.TableMeta{RelKind: "r"}
	tm.Columns = append(tm.Columns,
		engine.ColumnDef{Name: "c_drop", TypeOID: 1184, Dropped: true},
		engine.ColumnDef{Name: "c_int", TypeOID: 23, AttNum: 1})
	if needMysqlTZHint(csvDataOpts("none"), tm) {
		t.Error("Dropped 列不导出数据，不该因为它提示")
	}
}

// 防御：nil 入参不 panic（warnMysqlTZHintIfNeeded 在 exportOneTable 里被调用，
// 理论上 tm 恒非 nil，但批量/DDL-only 路径曾出现 nil 传参的先例）
func TestV1219HintNilSafe(t *testing.T) {
	setMysqlMode(t, true)

	if needMysqlTZHint(nil, metaWithOIDs(1184)) {
		t.Error("nil options 应返回 false")
	}
	if needMysqlTZHint(csvDataOpts("none"), nil) {
		t.Error("nil table meta 应返回 false")
	}
	warnMysqlTZHintIfNeeded(nil, nil) // 不应 panic
}

// 提示只打一次：连调两次不应重复输出（sync.Once 语义，靠不 panic + 覆盖调用路径保证）
func TestV1219HintOnceNoPanic(t *testing.T) {
	setMysqlMode(t, true)
	setTZ(t, "Asia/Shanghai")

	o := csvDataOpts("none")
	tm := metaWithOIDs(1184)
	warnMysqlTZHintIfNeeded(o, tm)
	warnMysqlTZHintIfNeeded(o, tm)
}
