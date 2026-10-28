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
}
