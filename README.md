# pg2sql v1.0.19

PostgreSQL / KingbaseES 数据文件离线解析导出工具（Go 版，单文件零依赖）。

直接读取 PG/金仓堆文件（含 TOAST），无需数据库在线即可导出：

- `--sql`：INSERT 语句（自动发现表结构，含 TOAST 重组/已删除行审计；配合 `--ddl` 输出建表语句）
- `--data`：CSV（`--header` 首行字段名，`--delimiter` 自定义分隔符）
- `--count`：行数统计；`--fields` 指定字段导出
- 自动发现表结构、自动探测页大小（8/16/32KB）、自动探测库编码（UTF-8/GBK/Latin1）
- 支持 PG 12~18、KingbaseES V8/V9，兼容 8/16/32KB 块大小
- `--list-tables-db` 列出库内用户对象（默认过滤系统对象）；`--list-tables-all` 列出全部对象（含系统对象）
- `--tables` / `--all-tables` 批量导出全部用户表 / 全部表（含系统表）；`--schema NAME` 独立使用等价于 `--tables --schema NAME`（批量导出指定模式）；多表导出必须搭配 `-o` 输出目录与 `--sql` / `--data` 导出类型
- DDL 自动输出 PRIMARY KEY（解析 pg_index/sys_index，自动兼容 PG12-18 / 金仓 V8 / 金仓 V9 布局差异）
- 分区表：叶子分区各自独立 relfilenode，可按分区 relfilenode 或 `--table-name` 逐分区导出（父表 relfilenode=0 无独立存储，无可导出数据）

## 构建

```bash
go build -o pg2sql .
# 交叉编译 aarch64
GOOS=linux GOARCH=arm64 go build -o pg2sql-arm64 .
```

## 使用示例

```bash
# 导出 INSERT 语句（自动发现表结构）
pg2sql /pgdata/base/16384/16391 --sql -o out.sql

# 导出 DDL + INSERT
pg2sql /pgdata/base/16384/16391 --sql --ddl -o out.sql

# 导出 CSV（首行字段名）
pg2sql /pgdata/base/16384/16391 --data --header -o out.csv

# 指定字段导出
pg2sql /pgdata/base/16384/16391 --data --fields id,name --header -o out.csv

# -o 传目录时按 schema.relname 自动命名: 目录/schema.table.sql|.csv|.ddl（按导出类型取扩展名）
#   例: --sql → /tmp/schema.table.sql；--data --header → /tmp/schema.table.csv；--ddl → /tmp/schema.table.ddl
pg2sql /pgdata/base/16384/16391 --sql -o /tmp/
pg2sql /pgdata/base/16384/16391 --data --header -o /tmp/

# 统计行数
pg2sql /pgdata/base/16384/16391 --count

# 含已删除行 / 仅已删除行
pg2sql /pgdata/base/16384/16391 --sql --deleted
pg2sql /pgdata/base/16384/16391 --sql --only-deleted

# 指定表名 + 数据目录 + 库 OID（多表目录时）
pg2sql /pgdata/base/16384/16391 --datadir /pgdata --db-oid 16384 --table-name t1 --sql

# 列出数据库 / 库内表
pg2sql --datadir /pgdata --list-db
pg2sql /pgdata/base/16384 --list-tables-db      # 只列用户对象
pg2sql /pgdata/base/16384 --list-tables-all     # 列出全部对象（含系统对象）
pg2sql /pgdata/base/16384 --tables --sql -o /out/            # 批量导出全部用户表（SQL）
pg2sql /pgdata/base/16384 --all-tables --data -o /out/       # 批量导出全部表（含系统对象，CSV）
pg2sql /pgdata/base/16384 --tables --schema ray --sql -o /out/  # 指定 schema 批量导出
pg2sql /pgdata/base/16384 --schema ray --sql -o /out/             # 等价：--schema 独立触发批量

# 导出元数据 JSON，并离线回灌（catalog-json 模式）
pg2sql /pgdata/base/16384 --export-meta -o meta.json
pg2sql /pgdata/base/16384/16391 --catalog-json meta.json --table-name t1 --sql --ddl

# 指定库编码（默认自动探测）
pg2sql /pgdata/base/16384/16391 --data --encoding gbk -o out.csv

# 并发解析（大表提速，TOAST 索引并行预建）
pg2sql /pgdata/base/16384/16391 --sql --parallel 4 -o out.sql
```

导入验证：`psql -d newdb -f out.sql` 或 `ksql -d newdb -f out.sql`（从哪个版本导出，就导入到哪个版本）。

## 内置类型支持清单（PG 12~18 / KingbaseES V8/V9 全 block size）

| 类别 | 类型（oid/族） | 说明 |
| --- | --- | --- |
| 数值 | int2/int4/int8、numeric、float4/float8、money、oid、serial 族 | 完整精度 |
| 字符 | text、varchar、bpchar、name、金仓 clob(8014) | 中文/特殊字符/换行/空串/NULL |
| 二进制 | bytea | 0x hex 可逆 |
| 布尔/位 | bool、bit(n)、varbit、金仓 mysql BIT(4655) | 位串/hex 双通道 |
| 日期时间 | date、time、timetz、timestamp、timestamptz、interval、金仓 datetime(7952)/timestamp(7954)/time(7950)/date(7944)/ora_date(8020)/mysql_datetime_in 等 | 含 2000 年前负微秒、24:00:00 边界 |
| JSON/XML | json、jsonb（含金仓 mysql_json 4802）、xml（PG 4B 标记/金仓纯文本） | |
| 数组 | 任意类型数组（一维/多维/空数组，int[]/text[]/varchar[] 等） | `{{1,2},{3,4}}`/`{}` |
| 几何 | point、line、lseg、box、path、polygon、circle | |
| 网络 | inet、cidr、macaddr、macaddr8 | |
| 全文 | tsvector、tsquery | 与 PG 文本输出逐字一致 |
| 范围 | int4range/int8range/numrange/daterange/tsrange/tstzrange | 含 empty/半开区间 |
| 系统 | regclass/regproc 等 reg* 系列、pg_lsn、txid_snapshot、枚举（enum） | |
| 复合类型 | record（用户自定义，oid 动态） | **限制**：输出原始字节 E'\x...'（字节可逆），建议在线 pg_dump；后续版本攻关 |

**限制说明**：复合类型（record）磁盘布局受 heap_fill_tuple 的 short-varlena 化与对齐影响、跨版本差异大，当前以原始字节兜底输出（不损坏数据）；其余 PG 内置类型均解码为可逆文本/二进制字面量。

## 数据可靠性

- TOAST 外联字段自动关联并重组（light 索引 / 页级 LRU 缓存）
- 编码探测优先读 `pg_database`（UTF-8/GBK/Latin1 可逆解码）
- 页大小自动探测，支持 8KB/16KB/32KB 及金仓变体

## 更新记录

- **v1.0.19**：修复 txid_snapshot 空 xip 快照导出缺尾冒号 bug（PG `txid_snapshot_out` 固定输出 `xmin:xmax:`，nxip=0 时原实现输出 `1:100` 导致 SQL/CSV 双通道导入报 `invalid input syntax for type pg_snapshot`；现固定保留尾冒号，`1:100:` 可正常导入）。同步补充全数据类型测试脚本 `pgbuild/t_alltypes_100.sql`（56 列覆盖数值/字符/二进制/布尔/位/日期时间/JSON/XML/UUID/数组/网络/几何/全文/范围/系统/枚举/复合 + 主键 + 9 索引 + 100 行中英文特殊字符数据）。

- **v1.0.18**：补齐 PG/金仓内置类型缺口解码——range 全系（int4range/int8range/numrange/daterange/tsrange/tstzrange，含 empty 与半开区间 `(,10)`/`[5,)`，按 rangetypes 磁盘格式：剥 4B range 自身 oid 头 + lower/upper 定长按 attalign 连续、变长按完整 varlena 逐边界解析 + 1B flags）、`pg_lsn`（8B 小端，文本 X/Y 大写 hex）、`txid_snapshot`（[nxip][xmin][xmax][xip] 布局，文本 `100:200:110,140`）、`reg*` 系列（输出 oid 数字可逆导入）、`tsquery`（QueryItem 12B/个位打包 + 操作数 `\0` 结尾，NOT/AND/OR/PHRASE 优先级与 PG infix 完全一致，实测 `'fat' & ( 'rat' | 'cat' )` 等逐字一致）、`macaddr8`、`path`/`circle` 几何、多维数组 `{{1,2},{3,4}}` 与空数组 `{}`、枚举值（按 typrelid 动态取枚举成员）。验证：PG18 缺口实例 t_gap 表（18 列 3 行，含 range 全系/lsn/txid/tsquery/regclass/path/circle/macaddr8/enum/二维数组/空数组）导出值与 PG 实际值逐列一致，除复合类型外 17 列导出→导入闭环（TRUNCATE 后 \`\i\` 导入 count=3、抽查值一致）。**复合类型（用户自定义 record，oid 16505 等）限制**：磁盘 record 布局受 heap_fill_tuple 的 short-varlena 化与 attalign 对齐影响、跨版本差异大，当前输出原始字节 \`E'\\x...'\`（字节可逆、不损坏数据，可手工回灌或在线 pg_dump 处理），后续版本继续攻关。

- **v1.0.17**：修复金仓 V9 mysql 模式 BIT 类型（oid 4655，typinput=mysql_bit_in）导出原始 varlena 字节、SQL/CSV 导入失败的 BUG——实测反推 mysql_bit 磁盘格式（varlena 内容 = 4B 小端 A + 4B 小端 B + ceil(N/8) 字节大端数据，A=(N-1)-bitpos、B=bitpos+1、N=A+B 为存储位宽、数据=原值<<(8*nbytes-B)；全 0 时 A=B=0），新增 decodeMysqlBit（输出位串）与 bitBinToHex（输出 0x 大写 hex）；并确认金仓 mysql 模式导入要求：SQL 必须用 `B'...'` 位字面量（普通字符串走 varchar_bit cast 会报 bit string length exceeds 64）、CSV COPY 必须用 0x 前缀 hex（纯 0/1 文本会解析错乱，实测 0x8001→0xC4C4）。同时修复 xml 类型导出带 varlena 头残留的 BUG（金仓 mysql 模式 xml 为纯文本 varlena，头字节如 0x2b='+' 会被当文本输出；PG 标准 xml 内容含 4B 类型标记），新增 decodeXml（兼容金仓纯文本与 PG 4B 标记）。验证：金仓 V9R3C18 用户建表语句 t_fulltype_part 全类型分区表（36 列含 BIT(16)/VARBIT/XML/JSONB/几何/中文/特殊字符，3 分区 10 行）SQL 与 CSV 双通道导出→导入整行 md5 全一致；BIT(8)/BIT(10)/BIT(16) 边界（0x00/0x01/0x80/0xFF/0x02AA 等）双通道导入值一致；金仓 V8 bit(1560)/xml 表 + 31 列全类型表双通道 md5 全一致；PG12-18 × 8/16/32KB 全版本全类型回归 10/10 全绿（含 PG 标准 bit/xml）。
- **v1.0.16**：新增"目录动态类型发现"（方案 C）——导出时读取数据库目录内 `pg_type/sys_type`（1247）与 `pg_proc/sys_proc`（1255），按 `typinput` 函数名映射解码器语义族（`mysql_timestamp_in→UTC 秒解码`、`mysql_datetime_in→本地微秒`、`ora_date_in→timestamp 格式`、`jsonb_in→JSONB`、`textin→文本`等），`typtype='d'` 域类型沿 `typbasetype` 递归解析，数组类型按 `typelem+typarray` 自指自动注册；硬编码 decoders 表优先（兜底），目录缺失时功能不受影响。解决"实例相关 oid / 未来版本新类型无法预先硬编码"问题（如 V9R3C18 实例中 4802 mysql_json 无硬编码映射，现由目录动态命中 decodeJSONB；而 12636 在 V9R3 已被复用为 toast 行类型，不再依赖硬编码假设）。pg_type 布局版本感知：PG18+ 在 typrelid 后新增 typsubscript(4B) 列，PG12-17 与金仓 V8/V9 为同源布局（实测）。验证：金仓 V9R3C18 真实实例 42 个动态映射（含 4802 mysql_json 动态解码，SQL+CSV 双通道导入 EXCEPT 0 差异）；金仓 V8 28 个动态映射 + 31 列全类型表双通道 0 差异；PG12-18 × 8/16/32KB 全版本回归 10/10 全绿。
- **v1.0.15**：修复金仓 V9 mysql 模式 3 个类型解码 BUG（由 V9R3C18 真实实例 30 列全类型表实测发现）：① `7954 timestamp`（mysql_timestamp_in 存储为 2000-01-01 UTC 基准的秒精度 int64 微秒，输出按会话时区 UTC+8 转回）此前缺映射导出原始字节，新增 decodeMysqlTimestamp；② `7950 time`（MySQL TIME 语义，支持 24:00:00 合法上限）此前复用 decodeTime 会把 86400 秒取模成 00:00:00（id=3 的 23:59:59.999999 舍入进位 24:00:00 被错误归一化），新增 decodeMysqlTime 不做取模；③ `7024 json`（domain of 4802 mysql_json，底层 jsonb_in/jsonb_out 二进制存储）此前缺映射导出 varlena 原始字节，映射到 decodeJSONB。验证：金仓 V9R3C18 x86 真实实例 30 列全类型表（含 7944 date/7952 datetime/7954 timestamp/7950 time/8020 ora_date/4189 datetime/clob/money/json/jsonb/数组/中文/特殊字符/NULL/24:00:00 边界）SQL 与 CSV 双通道导出→导入双向 EXCEPT 0 差异；金仓 V8 31 列全类型表双通道 0 差异；PG12-18 × 8/16/32KB 全版本回归 10/10 全绿。
- **v1.0.14**：修复金仓 CLOB（oid 8014）导出带 varlena 头残留（`#clob中文内容`/`\x17clob末尾`）——金仓 clob 为 textin/textout 的标准 varlena 存储（实测头字节 0x05/0x07/0x0d/0x0f 与内容长度 1/2/5/6 精确对应 total=头>>1），补齐 8014→decodeText 解码；修复 money（oid 790）SQL 导出误加单引号（`'12.34'`）——790 加入 noQuoteOIDs 数字类型集合。验证：金仓 V8 在线 31 列全类型表（int2/int4/int8/oid/numeric/money/float4/float8/varchar/char/text/name/clob/bool/bytea/json/jsonb/uuid/date/datetime/ora_date/time/timetz/timestamp/timestamptz/interval/int[]/text[]/varchar[]，含中文/特殊字符/NULL/边界时间/闰年）SQL 与 CSV 双通道导出→导入双向 EXCEPT 0 差异；PG12-18 × 8/16/32KB 全版本回归。
- **v1.0.13**：修复金仓 V9R1C10 分区表 DATE 列导出原始字节——金仓 V9 新增 `7944 date`（4 字节天数，与 PG date 同格式）未映射，补齐金仓时间类型解码：`7944 date`、`7952 datetime`（V9 基础类型）、`4189 datetime`（domain of timestamp）、`12636 ora_date`（domain of 8020）；修复 PG 全类型导出暴露的两个 bug：`timestampFromUS` 对 2000 年前（微秒为负）时间格式化错误（输出 `00:00:00.-01`），负微秒归一化为正；`decodeTimetz` 零时区偏移 `+00:00` 误输出 `-00:00`；新增 `tsvector`（oid 3614）解码器——按 PG ts_type.h 磁盘格式解析（WordEntry 位打包 haspos/len/pos、词字符串连续存储、2 字节对齐填充与 WordEntryPos 数组、权重 3→A/2→B/1→C/0 不显示），文本输出与 tsvectorout 一致（含权重字母）。验证：PG16 全类型表（28 列含 tsvector/时间/数组/json/中文特殊字符/2000 年前时间）SQL/CSV 导出→导入双向 EXCEPT 0 差异；金仓 V8 在线 datetime/date 修复后导出正确；PG12-18 × 8/16/32KB 全版本回归。
- **v1.0.12**：`--schema NAME` 可独立使用（等价 `--tables --schema NAME`，无需再写 `--tables`），批量导出指定模式下全部用户表；批量校验报错文案统一覆盖 `--schema/--tables/--all-tables` 场景。
- **v1.0.11**：新增批量导出 `--tables`（用户表）/ `--all-tables`（全部表含系统对象）与 `--schema NAME` 模式过滤；多表导出强制 `-o` 输出目录并必须指定 `--sql` 或 `--data`；不带 `--ddl` 只导出表数据，带 `--ddl` 同时输出建表 DDL（每表独立文件，命名 schema.table.sql|.csv|.ddl）。
- **v1.0.10**：新增 `--list-tables-all`（列出库内全部对象，含系统对象）；`--list-tables-db` 改为默认只列用户对象（复用 isSystemSchema 过滤：pg_catalog/pg_toast/information_schema 及金仓 sys_catalog/sysaudit/sysmac/kdb_schedule/anon/src_restrict/sys/SYS_HM*/sys_*）。
- **v1.0.9**：清理冗余选项（移除 `--list-tables`、`--replace`）；修复 TOAST 并行索引构建数据竞争（每 worker 独立 map + 单 goroutine 归并，race detector 实测无 DATA RACE）；完整 DDL 支持——CREATE SEQUENCE、ALTER COLUMN SET DEFAULT nextval、索引/主键（USING btree）、COMMENT ON TABLE/COLUMN，SQL 文件尾部追加 SELECT setval 同步自增序列（解决 DDL 与数据分离导入时自增列落后导致主键冲突）；纯 DDL 模式独立 `.ddl` 后缀；修复 16/32KB 大页序列默认值解析（nodeToString 有符号字节正则 `-?\d+` + uint32 回绕）；回退 isLive 的 XMIN_COMMITTED 检查（PG 磁盘行提交位懒更新导致全版本回归失败，金仓 V8 aborted 行由 isInsertAborted 正确捕获）。验证：PG12-18 × 8/16/32KB 全版本回归 10/10 全绿（imp_sql=0/0 imp_csv=0/0 rows_csv=100000 ddl_pk=1）+ 金仓 V8 8KB count=100000、V9 SQL/CSV 导入闭环 0 错误（INSERT 自增 100001）。
- **v1.0.8**：完整支持 GB18030 解码——双字节区补全 GB18030 特有码点（私有区/扩展表）；新增四字节序列解码（低区 N<39420 查 206 段映射表覆盖 U+0080-U+FFFF 无双字节码点；高区 N∈[189000,1237575] 线性映射 U+10000-U+10FFFF；中间为预留非法区），经 Python 官方 gb18030 编解码器 500 组随机对照 + 金仓 GB18030 实例实测（中文/特殊字符导出正确、导入闭环）验证；PG 服务端不支持 GB18030 编码（实测确认），该场景面向金仓。
- **v1.0.7**：修复 PG15+ 主键 DDL 丢失——PG15 起 pg_index 在 indisunique 后插入 indnullsnotdistinct，indisprimary 偏移由 @13 移到 @14（PG15-18 按版本自动识别，PG12-14/金仓 V9 保持 @13、金仓 V8(PG10 内核) @11）；同步清理全部调试输出。
- **v1.0.6**：DDL 输出 PRIMARY KEY（自动发现解析 pg_index/sys_index：indisprimary 布局自适应——PG15+ 因 indisunique 后插入 indnullsnotdistinct 而 @14、PG11-14/金仓 V9 @13、PG10- 内核 @11；indkey 按 int2vector 磁盘格式正确解析（vl_len_+ndim+dataoffset+elemtype+dim1+lbound1+values，与 ArrayType 匹配，此前按 int16 直读错误），并用数据区 varlena 定位法自动兼容 indkey 偏移（PG12-18 @24 / 金仓 V9 追加列 @26 / 金仓 V8 @20）；移除死参数 `--toast-cache`（full 未实现）与 `--force`（无实现）；分区表逐分区导出实测通过（--list-tables-db 可见 relkind='p' 父表与 'r' 叶子分区，叶子分区按 relfilenode 独立导出，SQL/CSV 导入验证 0 缺失；父表 relfilenode=0 无可导出数据，DDL 输出不含 PARTITION BY，导入需手工重建分区结构）。
- **v1.0.5**：新增运行提示——TOAST 文件非空但索引 0 valueid（数据库实例在线且最近写入未 CHECKPOINT，磁盘堆/TOAST 页未完整落盘，如用户实测的 57049 行场景）时输出警告；未找到目标表时提示可能未 CHECKPOINT（磁盘 sys_class 与数据文件 relfilenode 不一致，建议 CHECKPOINT 或 --table-name）。与 Python 版逐逻辑对照确认一致：natts 低位读取（t_infomask2 & 0x07FF）、TOAST 失败占位（__TOAST_MISSING__ 输出 NULL）、页大小自动探测、CLI 32 个参数完全对齐；金仓 V8 在线/V9 副本全功能回归 + PG12-18 全版本 × 8/16/32KB 全 21 项：50 列 10 万行中文随机数据导出→导入（SQL 与 CSV 双通道）全部 count=100000、双向 EXCEPT 0/0 通过。
- **v1.0.4**：修复金仓 V9 幽灵列——ALTER 后残留的 att 行 attnum 超出数据行实际列数，导致导出 55 列垃圾列、CSV 列数与表定义不匹配（COPY 报 "extra data after last expected column"）、行解析错位丢行（100000 行只导出 90189 行）。修复：导出前预扫数据文件行头，按行内最大列数（maxRowNatts）截断列集；行内列数统一按 t_infomask2 低 11 位读取（PG 源码 HeapTupleHeaderGetNatts = t_infomask2 & 0x07FF，实测 V8/V9/PG12-18 一致，撤销此前错误的 PG 高位分支）；统一注入 IsKB 标识（含 --catalog-json 路径）；修复金仓 int16（4659）16 字节整数解码的 64 位左移溢出（改用 big.Int 完整表示）。验证：金仓 V8 在线 50 列 10 万行中英文随机数据 SQL/CSV 导出→导入 0 缺失 0 多余；PG12-18 × 8/16/32KB 全版本回归。
- **v1.0.3**：修复金仓 ALTER DROP COLUMN 残留多代 sys_attribute 行导致列重复/垃圾列（按 attrelid+attnum 去重，优先非 dropped、同状态取最新事务）；自动发现与 --list-tables-db 的列数统计排除 dropped 列；全版本（金仓 V8R6C8B14/B20/C9B14/V9R1C10 + PG12-18 × 8/16/32KB）回归通过。
- **v1.0.2**：修复 `-o` 指定目录时输出文件路径解析。
- **v1.0.1**：--list-tables-db 对齐输出。
- **v1.0.0**：Go 版重写首发，全版本矩阵验证。


MIT License。作者：raysuen
