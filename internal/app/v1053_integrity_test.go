package app

// v1.0.53 完整性自证 + 角色导出的单元测试
// 夹具来源：PG17.5 与金仓 V9R1C10 实库 pg_control / pg_authid 字节样本

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"pg2sql/internal/cli"
	"pg2sql/internal/config"
	"pg2sql/internal/datatype"
	"pg2sql/internal/integrity"
	"pg2sql/internal/objects"
	"strings"
	"testing"
)

// buildControl 构造 pg_control 字节流（偏移与实库一致：sysid@0/ver@8/cat@12/state@16/time@24/ckpt@32/redo@40）
func buildControl(sysID uint64, ver, catVer, state uint32, tm int64, ckpt, redo uint64) []byte {
	b := make([]byte, 256)
	binary.LittleEndian.PutUint64(b[0:], sysID)
	binary.LittleEndian.PutUint32(b[8:], ver)
	binary.LittleEndian.PutUint32(b[12:], catVer)
	binary.LittleEndian.PutUint32(b[16:], state)
	binary.LittleEndian.PutUint64(b[24:], uint64(tm))
	binary.LittleEndian.PutUint64(b[32:], ckpt)
	binary.LittleEndian.PutUint64(b[40:], redo)
	return b
}

func TestV1053ParsePgControl(t *testing.T) {
	// PG17.5 实库样本：sysid=0x6adefba9811f204d, ver=1700, cat=202406281, state=6(运行中)
	const tm = 1793188969
	b := buildControl(0x6adefba9811f204d, 1700, 202406281, 6, tm, 0x068A4B88, 0x068A4AF8)
	ci := integrity.ParsePgControl(b)
	if ci == nil {
		t.Fatalf("PG17 控制文件解析失败")
	}
	if ci.CtrlVer != 1700 || ci.CatVer != 202406281 || ci.State != 6 {
		t.Fatalf("字段解析错误: ver=%d cat=%d state=%d", ci.CtrlVer, ci.CatVer, ci.State)
	}
	if got := integrity.ControlStateName(ci.State); got != "DB_IN_PRODUCTION" {
		t.Fatalf("状态名错误: %s", got)
	}
	if got := integrity.LSNString(ci.CkptLSN); got != "0/68A4B88" {
		t.Fatalf("LSN 渲染错误: %s", got)
	}
	// 金仓 V9 样本：ver=1201, cat=0x0C11F07F, state=1
	b2 := buildControl(0x6adefbcf6fbbc064, 1201, 0x0C11F07F, 1, tm, 0x2B000028, 0x2B000028)
	ci2 := integrity.ParsePgControl(b2)
	if ci2 == nil || integrity.ControlStateName(ci2.State) != "DB_SHUTDOWNED" {
		t.Fatalf("金仓控制文件解析失败或状态名错误")
	}
	// 拒绝：版本越界 / 状态越界 / 时间不合理 / 长度不足
	bad := [][]byte{
		buildControl(1, 999, 1, 6, tm, 1, 1),
		buildControl(1, 1700, 1, 0, tm, 1, 1),
		buildControl(1, 1700, 1, 7, tm, 1, 1),
		buildControl(1, 1700, 1, 6, 100, 1, 1),
		make([]byte, 40),
	}
	for i, bb := range bad {
		if integrity.ParsePgControl(bb) != nil {
			t.Fatalf("非法控制文件未被拒绝: case %d", i)
		}
	}
}

func TestV1053RowsVerdict(t *testing.T) {
	// DDL-only 不做判定；--limit/--deleted 造成的行数变化不算少导
	if _, c := integrity.RowsVerdict(0, 100, false, false, true); c != "" {
		t.Fatalf("DDL-only 应无判定, got %q", c)
	}
	if _, c := integrity.RowsVerdict(10, 100, true, false, false); c != "limited" {
		t.Fatalf("--limit 应判 limited, got %q", c)
	}
	if _, c := integrity.RowsVerdict(10, 100, false, true, false); c != "filtered" {
		t.Fatalf("deleted 过滤应判 filtered, got %q", c)
	}
	if _, c := integrity.RowsVerdict(5, -1, false, false, false); c != "unanalyzed" {
		t.Fatalf("reltuples=-1 应判 unanalyzed, got %q", c)
	}
	cases := []struct {
		actual int64
		est    float32
		want   string
		estOut int64
	}{
		{100, 100, "match", 100},
		{950, 1000, "match", 1000},
		{500, 1000, "under", 1000}, // 少导 50% → 告警
		{0, 1000, "under", 1000},   // 一行没导 → 严重
		{1200, 1000, "over", 1000}, // 估计偏低（analyze 陈旧）→ 不告警
		{0, 0, "match", 0},
	}
	for _, c := range cases {
		est, got := integrity.RowsVerdict(c.actual, c.est, false, false, false)
		if got != c.want || est != c.estOut {
			t.Fatalf("integrity.RowsVerdict(%d,%v)=(%d,%q), want (%d,%q)", c.actual, c.est, est, got, c.estOut, c.want)
		}
	}
}

func TestV1053PagesVerdict(t *testing.T) {
	cases := []struct {
		exp  int32
		act  int64
		want string
	}{
		{10, 10, "match"},
		{10, 12, "match"}, // 正常增长不告警
		{10, 7, "short"},  // 段文件缺失/被截断 → 严重
		{0, 5, "nostats"}, // 未分析过但确有数据
		{0, 0, ""},        // 空表
	}
	for _, c := range cases {
		if got := integrity.PagesVerdict(c.exp, c.act); got != c.want {
			t.Fatalf("integrity.PagesVerdict(%d,%d)=%q, want %q", c.exp, c.act, got, c.want)
		}
	}
}

func TestV1053MainForkPages(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "16624")
	const ps = 8192
	if err := os.WriteFile(base, make([]byte, ps*3), 0644); err != nil {
		t.Fatal(err)
	}
	// .1 段：分片文件必须计入（漏算会误报 pages_short）
	if err := os.WriteFile(base+".1", make([]byte, ps*2), 0644); err != nil {
		t.Fatal(err)
	}
	if got := integrity.MainForkPages(base, ps); got != 5 {
		t.Fatalf("integrity.MainForkPages=%d, want 5", got)
	}
	if got := integrity.MainForkPages(filepath.Join(dir, "nope"), ps); got != 0 {
		t.Fatalf("缺失文件应返回 0, got %d", got)
	}
	if got := integrity.MainForkPages(base, 0); got != 0 {
		t.Fatalf("页大小为 0 应返回 0, got %d", got)
	}
}

func TestV1053TypeCoverageKind(t *testing.T) {
	if k := integrity.TypeCoverageKind(23, 0); k != "exact" { // int4 有专用解码器
		t.Fatalf("int4 应为 exact, got %q", k)
	}
	if k := integrity.TypeCoverageKind(4294967000, 0); k != "unresolved" { // pg_type 中不存在
		t.Fatalf("未知 oid 应为 unresolved, got %q", k)
	}
	// 域类型按基类型判定（注入临时定义，测完恢复）
	old := datatype.DynamicTypeDefs
	defer func() { datatype.DynamicTypeDefs = old }()
	datatype.DynamicTypeDefs = map[uint32]*datatype.TypeDef{
		4300001: {OID: 4300001, Name: "mydomain", Type: 'd', Base: 23},
		4300002: {OID: 4300002, Name: "domain_of_unknown", Type: 'd', Base: 4294967001},
	}
	if k := integrity.TypeCoverageKind(4300001, 0); k != "exact" {
		t.Fatalf("域(基类型 int4) 应为 exact, got %q", k)
	}
	if k := integrity.TypeCoverageKind(4300002, 0); k != "unresolved" {
		t.Fatalf("域(基类型未知) 应为 unresolved, got %q", k)
	}
}

func TestV1053PgTimestamptz(t *testing.T) {
	cases := []struct {
		v    int64
		want string
	}{
		{0, "2000-01-01 00:00:00+00"},
		{86400 * 1000000, "2000-01-02 00:00:00+00"},
		{int64(0x7FFFFFFFFFFFFFFF), "infinity"},
		{int64(-0x8000000000000000), "-infinity"},
	}
	for _, c := range cases {
		if got := objects.PgTimestamptzString(c.v); got != c.want {
			t.Fatalf("objects.PgTimestamptzString(%d)=%q, want %q", c.v, got, c.want)
		}
	}
}

func TestV1053GenOneRole(t *testing.T) {
	r := objects.RoleRow{
		Name: "app_user", CanLogin: true, Inherit: true,
		ConnLimit: 5, ConnLimitSet: true,
		HasPassword: true, Password: "SCRAM-SHA-256$4096:c2FsdA==$c3RvcmVk",
		ValidUntil: "2027-01-01 00:00:00+00",
	}
	got := objects.GenOneRole(r, true)
	want := `CREATE ROLE "app_user" WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE INHERIT NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 5 PASSWORD 'SCRAM-SHA-256$4096:c2FsdA==$c3RvcmVk' VALID UNTIL '2027-01-01 00:00:00+00';`
	if strings.TrimSpace(got) != want {
		t.Fatalf("CREATE ROLE 生成不符\n got: %s\nwant: %s", strings.TrimSpace(got), want)
	}
	// --roles-nopassword：密文不落盘
	got2 := objects.GenOneRole(r, false)
	if strings.Contains(got2, "PASSWORD") {
		t.Fatalf("--roles-nopassword 仍输出密码: %s", got2)
	}
	// 单引号转义（口令密文/角色名含引号的极端情况不产生语法错误）
	if s := objects.SQLLiteral("a'b"); s != "'a''b'" {
		t.Fatalf("objects.SQLLiteral 转义错误: %s", s)
	}
	// CONNECTION LIMIT 默认值 -1 不输出；0（禁止连接）必须输出
	def := objects.RoleRow{Name: "r1", ConnLimit: -1, ConnLimitSet: true}
	if strings.Contains(objects.GenOneRole(def, false), "CONNECTION") {
		t.Fatalf("-1 应视为默认不输出: %s", objects.GenOneRole(def, false))
	}
	zero := objects.RoleRow{Name: "r1", ConnLimit: 0, ConnLimitSet: true}
	if !strings.Contains(objects.GenOneRole(zero, false), "CONNECTION LIMIT 0") {
		t.Fatalf("CONNECTION LIMIT 0 必须输出: %s", objects.GenOneRole(zero, false))
	}
}

func TestV1053BuiltinRoleFilter(t *testing.T) {
	if !objects.IsBuiltinRole("pg_monitor") || !objects.IsBuiltinRole("pg_read_all_data") || !objects.IsBuiltinRole("pg_maintain") {
		t.Fatalf("内置角色组未识别")
	}
	if objects.IsBuiltinRole("app_user") || objects.IsBuiltinRole("postgres") {
		t.Fatalf("普通/超级用户被误判为内置角色组")
	}
	// OID 优先：initdb 建立的 system/sa（金仓）即便不在名单也按内置处理
	if !objects.IsSystemRole(objects.RoleRow{Name: "system", OID: 10}) || !objects.IsSystemRole(objects.RoleRow{Name: "sa", OID: 16383}) {
		t.Fatalf("OID<16384 的角色应判为内置")
	}
	if objects.IsSystemRole(objects.RoleRow{Name: "app_user", OID: 16384}) || objects.IsSystemRole(objects.RoleRow{Name: "app_user", OID: 20000}) {
		t.Fatalf("OID>=16384 的用户角色被误判为内置")
	}
	// OID 缺失时退回名单
	if !objects.IsSystemRole(objects.RoleRow{Name: "pg_monitor"}) {
		t.Fatalf("OID 缺失时未按名单兜底")
	}
}

func TestV1053SkipSinkConcurrent(t *testing.T) {
	integrity.ResetSkip()
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(i int) {
			integrity.AddSkip(integrity.SkipEntry{Table: "public.t1", Reason: "relkind"})
			integrity.AddSkip(integrity.SkipEntry{Table: "public.t2", Reason: "no_storage"})
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	got := integrity.SkippedObjects.Snapshot()
	// 去重后应恰好 2 条（并发 append 不重复）
	if len(got) != 2 {
		t.Fatalf("跳过清单去重失败: %d 条", len(got))
	}
	if got[0].Table != "public.t1" || got[1].Table != "public.t2" {
		t.Fatalf("跳过清单未按表名排序: %+v", got)
	}
	integrity.ResetSkip()
	if len(integrity.SkippedObjects.Snapshot()) != 0 {
		t.Fatalf("integrity.ResetSkip 未清空")
	}
}

func TestV1053Verdict(t *testing.T) {
	// 全绿
	v := integrity.BuildVerdict(integrity.ReportSummary{TablesTotal: 3, RowsExported: 10}, &integrity.DbState{StateCode: 1, State: "DB_SHUTDOWNED"}, nil, nil)
	if v.Level != "ok" {
		t.Fatalf("干净导出的判定应为 ok, got %+v", v)
	}
	// 坏块 → fail
	v = integrity.BuildVerdict(integrity.ReportSummary{TablesTotal: 1, BadBlocks: 2}, nil, nil, nil)
	if v.Level != "fail" {
		t.Fatalf("存在坏块应判 fail, got %+v", v)
	}
	// 页数缺失 → fail（段文件丢了比行数偏低严重）
	v = integrity.BuildVerdict(integrity.ReportSummary{TablesTotal: 1, TablesPagesShort: 1}, nil, nil, nil)
	if v.Level != "fail" {
		t.Fatalf("页数缺失应判 fail, got %+v", v)
	}
	// 运行中快照 + 行数偏低 → warn
	v = integrity.BuildVerdict(integrity.ReportSummary{TablesTotal: 2, TablesRowsShort: 1},
		&integrity.DbState{StateCode: 6, State: "DB_IN_PRODUCTION"}, nil, nil)
	if v.Level != "warn" || len(v.Reasons) != 2 {
		t.Fatalf("应判 warn 且有 2 条原因, got %+v", v)
	}
}

// TestV1053PositionalGuard：位置参数唯一性守卫。
// 历史坑：`pg2sql <dbdir> --all-tables --report /out/report.json`（把路径当成 --report 的值）
// 会因"后者覆盖前者"把数据目录换成 /out/report.json，批量导出静默退化为 0 表 0 行。
func TestV1053PositionalGuard(t *testing.T) {
	o := &config.Options{}
	if err := cli.SetPositional(o, "/pgdata/base/16384"); err != nil {
		t.Fatalf("首个位置参数应接受: %v", err)
	}
	if o.DataFile != "/pgdata/base/16384" {
		t.Fatalf("DataFile 未登记: %q", o.DataFile)
	}
	// 重复但不相同 → 必须报错（而不是静默覆盖）
	err := cli.SetPositional(o, "/out/report.json")
	if err == nil {
		t.Fatal("第二个位置参数应报错，否则数据目录会被静默替换")
	}
	if !strings.Contains(err.Error(), "--report-file") {
		t.Fatalf("提示应指向 --report-file: %v", err)
	}
	// 相同值重复 → 幂等放行
	if err := cli.SetPositional(o, "/pgdata/base/16384"); err != nil {
		t.Fatalf("重复相同位置参数应放行: %v", err)
	}
	if o.DataFile != "/pgdata/base/16384" {
		t.Fatalf("DataFile 被改写: %q", o.DataFile)
	}
}

// TestV1053ReportPath：--report-file 覆盖默认落盘路径（默认 <输出目录>/report.json）。
func TestV1053ReportPath(t *testing.T) {
	dir := t.TempDir()
	o := &config.Options{Report: true, ReportPath: filepath.Join(dir, "sub", "selfcheck.json")}
	writeReport(o)
	b, err := os.ReadFile(o.ReportPath)
	if err != nil {
		t.Fatalf("报告未写入指定路径: %v", err)
	}
	if !strings.Contains(string(b), `"verdict"`) || !strings.Contains(string(b), `"summary"`) {
		t.Fatalf("报告缺少 verdict/summary 段: %s", string(b)[:min(len(b), 200)])
	}
	// 空路径 + 无 -o → 走 stderr，不落盘
	o2 := &config.Options{Report: true}
	writeReport(o2)
}

// TestV1053EmptyDiscovery：什么都没发现（目录/表名错）不能判 ok。
func TestV1053EmptyDiscovery(t *testing.T) {
	v := integrity.BuildVerdict(integrity.ReportSummary{}, nil, nil, nil)
	if v.Level != "fail" {
		t.Fatalf("0 表 0 跳过 应判 fail, got %+v", v)
	}
	if len(v.Reasons) == 0 || !strings.Contains(v.Reasons[0], "未发现任何可导出对象") {
		t.Fatalf("原因应提示未发现对象: %+v", v)
	}
	// --schema 全被过滤（有 skip）→ 不判 fail，只列清单
	v = integrity.BuildVerdict(integrity.ReportSummary{TablesSkipped: 5}, nil, nil,
		[]integrity.SkipEntry{{Table: "public.t", Reason: "schema_filter"}})
	if v.Level != "ok" {
		t.Fatalf("结构性跳过不应判 fail, got %+v", v)
	}
}

// TestV1053SystemBadBlocks：系统表坏块不把整体判定拉到 fail（金仓私有写机制的噪声），
// 用户表坏块仍然 fail。
func TestV1053SystemBadBlocks(t *testing.T) {
	entries := []integrity.ExportReportEntry{
		{Table: "pg_catalog._initprivs", BadBlocks: 7},
		{Table: `"sys"."sys_class"`, BadBlocks: 2},
		{Table: "public.t_all", BadBlocks: 0},
	}
	s := integrity.BuildSummary(entries, nil, nil)
	if s.BadBlocks != 9 || s.BadBlocksSystem != 9 {
		t.Fatalf("系统表坏块归集错误: %+v", s)
	}
	if v := integrity.BuildVerdict(s, nil, nil, nil); v.Level != "warn" {
		t.Fatalf("仅系统表坏块应判 warn, got %+v", v)
	}
	// 混入用户表坏块 → fail
	s2 := integrity.BuildSummary(append(entries, integrity.ExportReportEntry{Table: "public.t_bad", BadBlocks: 1}), nil, nil)
	if s2.BadBlocks != 10 || s2.BadBlocksSystem != 9 {
		t.Fatalf("汇总错误: %+v", s2)
	}
	if v := integrity.BuildVerdict(s2, nil, nil, nil); v.Level != "fail" {
		t.Fatalf("用户表坏块应判 fail, got %+v", v)
	}
}
