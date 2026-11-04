// Author: raysuen
package config

type Options struct {
	DataFile       string
	CatalogJSON    string
	Datadir        string
	DBOID          int
	TableName      string
	DDL            bool
	SQL            bool
	Data           bool
	Deleted        bool
	OnlyDeleted    bool
	Count          bool
	ListDB         bool
	ListTablesDB   bool
	ListTablesAll  bool
	Tables         bool
	AllTables      bool
	Schema         string
	ExportMeta     bool
	Output         string
	Limit          int
	Fields         string
	Header         bool
	CompleteInsert bool
	Delimiter      string
	Toast          string
	PageSize       int
	Parallel       int
	Encoding       string
	Verbose        bool
	// v1.0.38 新功能
	BatchRows    int    // 多值 INSERT 批大小（默认 1=逐行单条）
	Jobs         int    // 批量导出表间并行数（默认 1=串行）
	Report       bool   // 导出报告 report.json
	Checksum     string // ""=关 | crc32 | md5
	ImportScript string // none | auto | sql | csv（默认 none=关）
	// v1.0.39 TDE（金仓透明加密离线解密）
	EncKey         string // 表加密密钥字符串（CREATE TABLE ... ENCRYPTED BY 'key'）
	WalletPassword string // 钱包密码（表空间加密校验；密钥链离线推导不需密码）
	WalletDir      string // 钱包目录（默认自动探测 <PGDATA>/.wallet）
	// v1.0.52：applyAutoEncoding 之后的库级编码基线（表级精化的复位锚点）
	EncBase string
	// v1.0.40 新功能
	FullSchema      bool   // 完整 Schema DDL（函数/触发器/约束/视图，需 --ddl）
	SchemaObjects   string // --schema-objects 过滤列表（view,function,trigger,constraint,fk，默认全开）
	LargeObjects    bool   // pg_largeobject 大对象导出
	LargeObjectOIDs string // --large-object-oids 逗号列表（默认全部）
	Progress        bool   // 导出进度与 ETA（stderr 输出）
	// v1.0.53：角色 DDL 导出（离线读 global/pg_authid + pg_auth_members）
	Roles           bool // 输出 roles.ddl（CREATE ROLE + GRANT 成员关系）
	RolesNoPassword bool // 省略 PASSWORD 密文（合规/交付第三方场景）
	// v1.0.53：--report-file 指定报告落盘路径（默认 <输出目录>/report.json）
	ReportPath string
	// B9 修复：mysql 模式 timestamp 解码时区（--tz，如 Asia/Shanghai、+09:00、UTC）
	MysqlTZ string // ""=自动（解析数据目录 conf timezone，失败回退 +8 并告警）
	// v1.1.1：--text 输出 PostgreSQL COPY text 格式（制表符分隔 + 反斜杠转义 + \N 表 NULL）。
	// 置位时 Data 恒为 true（共用数据导出的全部链路），仅格式与后缀不同。
	TextFormat bool
	// v1.2.0：--clog 读事务状态日志（pg_xact / 金仓 sys_xact）复核元组可见性。
	// 只影响 hint bit 缺失的元组，常规库无行为变化。要求 CLOG 已落盘
	// （先 CHECKPOINT 或停机），否则读到 IN_PROGRESS 会按活行导出并告警。
	Clog bool
	// v1.2.5：--format 值参数（sql/csv/text），替代 --sql/--data/--text 三旗标。
	// 仅作解析期输入：args.go 校验后归一化到 SQL/Data/TextFormat 三旗标，
	// 下游判断逻辑零改动。归一化完成后本字段不再参与任何判断。
	Format string
}

// ResolveDelimiter 返回本次导出实际使用的列分隔符。
// CSV 默认 ','；COPY text 默认制表符（与服务端 COPY ... FORMAT text 一致）。
// 用户显式 --delimiter 时以其值为准（--text 场景不再受默认值影响）。
func (o *Options) ResolveDelimiter() string {
	if o.Delimiter != "" {
		return o.Delimiter
	}
	if o.TextFormat {
		return "\t"
	}
	return ","
}
