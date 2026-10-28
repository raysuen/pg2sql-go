package app

// v1.0.56 回归测试：三批修复 + R2/R3 专项
//
// 第一批：multi-xmax 判活（P1-G1）/ 编码枚举映射（P2-G2）/ proargtypes
// oidvector 解析（P2-G3）/ 分区父表 + --count 多叶子（P2-G4，errorExit 进程级
// 行为由 141 实机回归覆盖）。
// 第二批：datatype.RecordQuote（P2-G5）/ AttGenerated 往返（P2-G6）/ engine.TryAutoLayout
// 短文件越界（P3-G7）/ logf %d（P3-G8）/ quoteIdent（P3-G9）/
// --schema-objects 校验（P3-G10）。
// 第三批：TDE 校验前移（P3-G11，进程级）/ engine.IterSysTuples 多段（P3-G12）/
// jsonb 越界 + nodetree 死循环（P3-G13）/ BC 日期（P3-G14）/ 大对象分块
//（P3-G15）。
// 专项：segPageTable 段边界统一（R3）/ 归并窗口有界告警（R2，告警路径）/
// count 跳过 TOAST（P3-G16，性能）。

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"pg2sql/internal/catalog"
	"pg2sql/internal/charset"
	"pg2sql/internal/datatype"
	"pg2sql/internal/ddl"
	"pg2sql/internal/engine"
	"pg2sql/internal/nt"
	"pg2sql/internal/objects"
	"testing"
	"time"
)

// ---- P1-G1：multixact 判活 ----

func TestV1056MultiXactKeysUpdatedDeleted(t *testing.T) {
	// multi xmax + engine.HEAP_KEYS_UPDATED：multi 内含 updater → 必须判删除
	tup := &engine.HeapTuple{TInfomask: engine.HEAP_XMAX_IS_MULTI | engine.HEAP_KEYS_UPDATED, TXmax: 12345}
	if !tup.IsDeleted() {
		t.Fatalf("multi-xmax 含 updater 的行被判为活行（复现 P1-G1：已删行被导出）")
	}
	// 纯锁 multi（无 updater）→ 判活
	tup2 := &engine.HeapTuple{TInfomask: engine.HEAP_XMAX_IS_MULTI, TXmax: 12345}
	if tup2.IsDeleted() {
		t.Fatalf("纯锁 multi 被误判为删除行")
	}
	// multi + KEYS_UPDATED 但 updater 已 abort（XMAX_INVALID）→ 判活
	tup3 := &engine.HeapTuple{TInfomask: engine.HEAP_XMAX_IS_MULTI | engine.HEAP_KEYS_UPDATED | engine.HEAP_XMAX_INVALID, TXmax: 12345}
	if tup3.IsDeleted() {
		t.Fatalf("aborted updater 的 multi 行被误判为删除")
	}
	// 普通 xmax committed 删除不受影响
	tup4 := &engine.HeapTuple{TInfomask: engine.HEAP_XMAX_COMMITTED, TXmax: 99}
	if !tup4.IsDeleted() {
		t.Fatalf("普通 committed 删除判定回归")
	}
}

// ---- P2-G2：编码枚举映射 ----

func TestV1056EncodingMap(t *testing.T) {
	cases := map[int]string{
		6: "utf-8", 8: "latin1", 37: "gbk",
		35: "sjis", 36: "big5", 38: "uhc", 39: "gb18030", 40: "johab", 41: "shift-jis-2004",
	}
	for id, want := range cases {
		if got := charset.EncodingIDToCodec[id]; got != want {
			t.Fatalf("编码 ID %d: 期望 %s, 实际 %s", id, want, got)
		}
	}
}

// GB18030（ID 39）库的双字节文本必须按 GB18030 正确解码（旧版按 GBK 路径恰
// 好覆盖双字节区，此处验证映射走 gb18030 codec 后不回归）
func TestV1056GB18030Decode(t *testing.T) {
	old := charset.CurTextEncoding()
	defer charset.SetTextEncoding(old)
	charset.SetTextEncoding("gb18030")
	// "中" GB18030/GBK 双字节 0xD6D0
	if got := charset.DecodeBytes([]byte{0xD6, 0xD0}); got != "中" {
		t.Fatalf("gb18030 解码回归: %q", got)
	}
}

// 不支持的多字节编码（如 BIG5）必须走 hex 可逆输出，禁止落回 gbk/utf-8 误解析
func TestV1056Big5HexFallback(t *testing.T) {
	old := charset.CurTextEncoding()
	defer charset.SetTextEncoding(old)
	charset.SetTextEncoding("big5")
	raw := []byte{0xA4, 0xA4} // BIG5 "中"
	got := charset.DecodeBytes(raw)
	if got != "\\x"+hexOf(raw) {
		t.Fatalf("big5 应走 hex 可逆输出, 实际 %q", got)
	}
}

func hexOf(b []byte) string {
	const hexDigits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, hexDigits[c>>4], hexDigits[c&0x0F])
	}
	return string(out)
}

// ---- P2-G3：oidvector 解析 ----

func t56VectorPayload(vals []uint32) []byte {
	// 去 varlena 头后的数组布局：ndim(4) dataoffset(4) elemtype(4) dim1(4) lb(4) data
	p := make([]byte, 20+4*len(vals))
	binary.LittleEndian.PutUint32(p[0:], 1)  // ndim
	binary.LittleEndian.PutUint32(p[4:], 0)  // dataoffset
	binary.LittleEndian.PutUint32(p[8:], 26) // elemtype = datatype.OIDOID
	binary.LittleEndian.PutUint32(p[12:], uint32(len(vals)))
	binary.LittleEndian.PutUint32(p[16:], 0) // lb
	for i, v := range vals {
		binary.LittleEndian.PutUint32(p[20+4*i:], v)
	}
	return p
}

func TestV1056OidVector(t *testing.T) {
	// proargtypes = (int4, char, varchar) —— 旧版 ddl.ParseInt2Vector 第 2 个起错位
	got := ddl.ParseOidVector(t56VectorPayload([]uint32{23, 18, 1043}))
	if len(got) != 3 || got[0] != 23 || got[1] != 18 || got[2] != 1043 {
		t.Fatalf("ddl.ParseOidVector 错误: %v", got)
	}
	// int2vector 路径不受影响：conkey (int2[])
	p2 := make([]byte, 20+2*2)
	binary.LittleEndian.PutUint32(p2[12:], 2)
	p2[20], p2[21] = 1, 0 // int16 1
	p2[22], p2[23] = 2, 0 // int16 2
	got2 := ddl.ParseInt2Vector(p2)
	if len(got2) != 2 || got2[0] != 1 || got2[1] != 2 {
		t.Fatalf("ddl.ParseInt2Vector 回归: %v", got2)
	}
}

// ---- P2-G5：datatype.RecordQuote ----

func TestV1056RecordQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", `""`},           // 空串：旧行为输出裸空段被解析为 NULL
		{"a\\b", `"a\\b"`},   // 反斜杠触发引号并转义
		{"a\tb", "\"a\tb\""}, // 内部 tab 触发引号
		{"plain", "plain"},   // 普通值不加引号
		{`a"b`, `"a\"b"`},    // 引号转义保持
		{"(x)", `"(x)"`},     // 括号保持旧行为
	}
	for _, c := range cases {
		if got := datatype.RecordQuote(c.in); got != c.want {
			t.Fatalf("datatype.RecordQuote(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

// ---- P2-G6：meta.json AttGenerated 往返 ----

func TestV1056MetaJSONAttGenerated(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "meta.json")
	// 导出侧：结构体含 attgenerated 字段
	m := catalog.MetaColumnJSON{Name: "total", TypeOID: 23, AttNum: 2, AttGenerated: "s"}
	data, _ := json.Marshal(m)
	if !bytes.Contains(data, []byte(`"attgenerated":"s"`)) {
		t.Fatalf("catalog.MetaColumnJSON 未序列化 attgenerated: %s", data)
	}
	// 加载侧：回填 engine.ColumnDef.AttGenerated
	if err := os.WriteFile(p, []byte(`{"database":"d","pg_version":17,"tables":[{"schema":"public","table":"t","relfilenode":17631,"toastrelid":0,"primary_key":null,"columns":[{"name":"total","type_oid":23,"attnum":2,"attgenerated":"s"}]}]}`), 0644); err != nil {
		t.Fatal(err)
	}
	_, tables, err := catalog.LoadMetaJSON(p)
	if err != nil {
		t.Fatal(err)
	}
	tm := tables["public.t"]
	if tm == nil || len(tm.Columns) != 1 || tm.Columns[0].AttGenerated != "s" {
		t.Fatalf("catalog.LoadMetaJSON 未回填 AttGenerated: %+v", tm)
	}
}

// ---- P3-G7：engine.TryAutoLayout 短文件越界 ----

func TestV1056TryAutoLayoutShortBuf(t *testing.T) {
	for _, n := range []int{0, 8, 20, 64, 65} {
		buf := make([]byte, n)
		for i := range buf {
			buf[i] = 0xFF // 高位字节填充，最大化暴露越界
		}
		if lay := engine.TryAutoLayout(buf, 8192); lay != nil {
			t.Fatalf("%d 字节缓冲不应解出布局", n)
		}
	}
}

// ---- P3-G9：标识符引号 ----

func TestV1056QuotedFullName(t *testing.T) {
	if got := quotedFullName("public.Order"); got != `"public"."Order"` {
		t.Fatalf("quotedFullName = %q", got)
	}
	if got := quotedFullName("plain_t"); got != `"plain_t"` {
		t.Fatalf("quotedFullName 无点回退 = %q", got)
	}
}

// ---- P3-G10：--schema-objects 校验 ----

func TestV1056SchemaObjectFilter(t *testing.T) {
	if _, err := ddl.ParseSchemaObjectFilter("views", map[string]bool{}); err == nil {
		t.Fatalf("拼写错误 views 应报错（复现 P3-G10 静默空文件）")
	}
	f, err := ddl.ParseSchemaObjectFilter("view, fk ,", map[string]bool{})
	if err != nil || !f["view"] || !f["fk"] {
		t.Fatalf("合法取值解析回归: %v %v", f, err)
	}
}

// ---- P3-G13：jsonb 越界防护 ----

func TestV1056JSONBGuard(t *testing.T) {
	payload := []byte{0x01, 0x02, 0x03, 0x04}
	// e 远超 payload 末尾：旧版直接 panic
	if got := datatype.JsonbScalarToText(0, payload, 4, 0, 1000); got != "null" {
		t.Fatalf("越界 jsonb 标量应输出 null, 实际 %q", got)
	}
	// aligned > dataOff+e（numeric 分支畸形长度）
	if got := datatype.JsonbScalarToText(datatype.JENTRY_ISNUM, payload, 4, 1, 2); got != "null" {
		t.Fatalf("numeric aligned 越界应输出 null, 实际 %q", got)
	}
}

// ---- P3-G13：nodetree 死循环 ----

func TestV1056NodeTreeNoHang(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		// 顶层 '}'：旧版 skipToken 不前进 → 死循环
		if got := nt.DeparseNodeTreeList("}", nil, nil, nil); got != nil {
			t.Logf("nt.DeparseNodeTreeList(\"}\") = %v", got)
		}
		// 容错分支遇 ')'：旧版原地打转（kind 后跟 stop 字符）
		p := &nt.Parser{Src: "{x )"}
		p.ParseNode()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("nodetree 解析死循环（复现 P3-G13）")
	}
}

// ---- P3-G14：BC 日期 ----

func TestV1056BCDate(t *testing.T) {
	// 天文年 0 = 公元前 1 年（PG 无公元 0 年）。
	// 注意不能用 Sub()：Duration 是 int64 纳秒（上限 ~292 年），跨 2000 年溢出。
	d1bc := int((time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC).Unix() - datatype.Date2000.Unix()) / 86400)
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, uint32(int32(d1bc)))
	if got := datatype.DecodeDate(b); got != "0001-01-01 BC" {
		t.Fatalf("datatype.DecodeDate BC = %q, 期望 0001-01-01 BC", got)
	}
	// timestamp BC
	us := int64(d1bc) * 86400 * 1000000
	tb := make([]byte, 8)
	binary.LittleEndian.PutUint64(tb, uint64(us))
	if got := datatype.TimestampFromUS(us); got[:16] != "0001-01-01 00:00" || got[len(got)-3:] != " BC" {
		t.Fatalf("datatype.TimestampFromUS BC = %q", got)
	}
	// 现代日期不回归
	d2024 := int((time.Date(2024, 5, 6, 0, 0, 0, 0, time.UTC).Unix() - datatype.Date2000.Unix()) / 86400)
	binary.LittleEndian.PutUint32(b, uint32(int32(d2024)))
	if got := datatype.DecodeDate(b); got != "2024-05-06" {
		t.Fatalf("datatype.DecodeDate 现代日期回归: %q", got)
	}
}

// datatype.CivilFromDays 负数 era 修复：PG 下限 4713 BC 附近的解算正确性
func TestV1056CivilFromDaysBC(t *testing.T) {
	// Julian Day 0 = 4714-11-24 BC（公历延长线）= PG date 下限 -4713-01-01 BC 所在历法起点附近
	for _, tc := range []struct {
		year int // 天文年（0 = 1 BC，负数 = BC）
		mon  time.Month
		day  int
	}{{-4712, time.January, 1}, {-44, time.March, 15}, {0, time.February, 29}, {0, time.March, 1}, {1970, time.January, 1}} {
		epochDays := int((time.Date(tc.year, tc.mon, tc.day, 0, 0, 0, 0, time.UTC).Unix() - time.Unix(0, 0).UTC().Unix()) / 86400)
		y, m, d := datatype.CivilFromDays(int64(epochDays))
		if y != tc.year || int(m) != int(tc.mon) || d != tc.day {
			t.Fatalf("datatype.CivilFromDays(%d) = (%d,%d,%d), 期望 (%d,%d,%d)", epochDays, y, m, d, tc.year, tc.mon, tc.day)
		}
	}
}

// ---- R3：segPageTable 段边界统一 ----

func TestV1056SegPageTable(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "17631")
	ps := 8192
	// 段 0 仅 3 页（残缺段），段 1 有 2 页 —— 均匀容量推导会把全局页号 3..4
	// 路由回段 0 的越界偏移，实际大小推导正确指向段 1
	if err := os.WriteFile(main, make([]byte, 3*ps), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(main+".1", make([]byte, 2*ps), 0644); err != nil {
		t.Fatal(err)
	}
	st := engine.BuildSegPageTable(main, ps)
	if st.Total != 5 || len(st.Base) != 2 {
		t.Fatalf("segPageTable: total=%d segs=%d", st.Total, len(st.Base))
	}
	if seg, off := st.Locate(2); seg != 0 || off != 2 {
		t.Fatalf("locate(2) = (%d,%d)", seg, off)
	}
	if seg, off := st.Locate(3); seg != 1 || off != 0 {
		t.Fatalf("locate(3) = (%d,%d), 期望段 1 偏移 0（复现 R3 错位）", seg, off)
	}
	if seg, off := st.Locate(4); seg != 1 || off != 1 {
		t.Fatalf("locate(4) = (%d,%d)", seg, off)
	}
}

// ---- P3-G15：大对象分块 SQL ----

func TestV1056LargeObjectChunkedSQL(t *testing.T) {
	var buf bytes.Buffer
	big := bytes.Repeat([]byte{'a'}, objects.LoChunkBytes*4) // 64MB hex = 32MB 二进制
	e := &objects.LargeObjectEntry{OID: 12345, DataHex: string(big)}
	if _, err := objects.WriteLargeObjectsSQL(&buf, []*objects.LargeObjectEntry{e}, nil); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte("lo_create(12345)")) {
		t.Fatalf("大对象未走 lo_create 分块路径")
	}
	n := bytes.Count(buf.Bytes(), []byte("lo_put(12345, "))
	if n != 2 { // 32MB / 16MB = 2 块
		t.Fatalf("lo_put 分块数 = %d, 期望 2", n)
	}
	// 块内不得携带 \x 前缀（decode(...,'hex') 用裸 hex）
	for _, line := range bytes.Split(buf.Bytes(), []byte("\n")) {
		if bytes.HasPrefix(line, []byte("SELECT pg_catalog.lo_put")) {
			if bytes.Contains(line, []byte("\\x")) {
				t.Fatalf("lo_put 语句混入 \\x 前缀: %.80s", line)
			}
		}
	}
	_ = out
}

// 小对象仍走 lo_from_bytea 单语句（不回归）
func TestV1056LargeObjectSmallSQL(t *testing.T) {
	var buf bytes.Buffer
	e := &objects.LargeObjectEntry{OID: 7, DataHex: "414243"}
	if _, err := objects.WriteLargeObjectsSQL(&buf, []*objects.LargeObjectEntry{e}, nil); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("lo_from_bytea(7, '\\x414243')")) {
		t.Fatalf("小对象 lo_from_bytea 回归: %s", buf.String())
	}
}
