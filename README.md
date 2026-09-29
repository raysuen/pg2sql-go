# pg2sql v1.0.7

PostgreSQL / KingbaseES 数据文件离线解析导出工具（Go 版，单文件零依赖）。

直接读取 PG/金仓堆文件（含 TOAST），无需数据库在线即可导出：

- `--sql`：INSERT 语句（含枚举/TOAST 重组/已删除行审计）
- `--data`：CSV（`--header` 首行字段名，`--delimiter` 自定义分隔符）
- `--count`：行数统计；`--fields` 指定字段导出
- 自动发现表结构、自动探测页大小（8/16/32KB）、自动探测库编码（UTF-8/GBK/Latin1）
- 支持 PG 12~18、KingbaseES V8/V9，兼容 8/16/32KB 块大小
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

# -o 传目录时自动命名: 目录/schema.table.sql|.csv
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
pg2sql /pgdata/base/16384 --list-tables-db

# 导出元数据 JSON，并离线回灌（catalog-json 模式）
pg2sql /pgdata/base/16384 --export-meta -o meta.json
pg2sql /pgdata/base/16384/16391 --catalog-json meta.json --table-name t1 --sql --ddl

# 指定库编码（默认自动探测）
pg2sql /pgdata/base/16384/16391 --data --encoding gbk -o out.csv

# 并发解析（大表提速，TOAST 索引并行预建）
pg2sql /pgdata/base/16384/16391 --sql --parallel 4 -o out.sql
```

导入验证：`psql -d newdb -f out.sql` 或 `ksql -d newdb -f out.sql`（从哪个版本导出，就导入到哪个版本）。

## 数据可靠性

- TOAST 外联字段自动关联并重组（light 索引 / 页级 LRU 缓存）
- 编码探测优先读 `pg_database`（UTF-8/GBK/Latin1 可逆解码）
- 页大小自动探测，支持 8KB/16KB/32KB 及金仓变体

## 更新记录

- **v1.0.3**：修复金仓 ALTER DROP COLUMN 残留多代 sys_attribute 行导致列重复/垃圾列（按 attrelid+attnum 去重，优先非 dropped、同状态取最新事务）；自动发现与 --list-tables-db 的列数统计排除 dropped 列；全版本（金仓 V8R6C8B14/B20/C9B14/V9R1C10 + PG12-18 × 8/16/32KB）回归通过。
- **v1.0.4**：修复金仓 V9 幽灵列——ALTER 后残留的 att 行 attnum 超出数据行实际列数，导致导出 55 列垃圾列、CSV 列数与表定义不匹配（COPY 报 "extra data after last expected column"）、行解析错位丢行（100000 行只导出 90189 行）。修复：导出前预扫数据文件行头，按行内最大列数（maxRowNatts）截断列集；行内列数统一按 t_infomask2 低 11 位读取（PG 源码 HeapTupleHeaderGetNatts = t_infomask2 & 0x07FF，实测 V8/V9/PG12-18 一致，撤销此前错误的 PG 高位分支）；统一注入 IsKB 标识（含 --catalog-json 路径）；修复金仓 int16（4659）16 字节整数解码的 64 位左移溢出（改用 big.Int 完整表示）。验证：金仓 V8 在线 50 列 10 万行中英文随机数据 SQL/CSV 导出→导入 0 缺失 0 多余；PG12-18 × 8/16/32KB 全版本回归。
- **v1.0.7**：修复 PG15+ 主键 DDL 丢失——PG15 起 pg_index 在 indisunique 后插入 indnullsnotdistinct，indisprimary 偏移由 @13 移到 @14（PG15-18 按版本自动识别，PG12-14/金仓 V9 保持 @13、金仓 V8(PG10 内核) @11）；同步清理全部调试输出。
- **v1.0.6**：DDL 输出 PRIMARY KEY（自动发现解析 pg_index/sys_index：indisprimary 布局自适应——PG15+ 因 indisunique 后插入 indnullsnotdistinct 而 @14、PG11-14/金仓 V9 @13、PG10- 内核 @11；indkey 按 int2vector 磁盘格式正确解析（vl_len_+ndim+dataoffset+elemtype+dim1+lbound1+values，与 ArrayType 匹配，此前按 int16 直读错误），并用数据区 varlena 定位法自动兼容 indkey 偏移（PG12-18 @24 / 金仓 V9 追加列 @26 / 金仓 V8 @20）；移除死参数 `--toast-cache`（full 未实现）与 `--force`（无实现）；分区表逐分区导出实测通过（--list-tables-db 可见 relkind='p' 父表与 'r' 叶子分区，叶子分区按 relfilenode 独立导出，SQL/CSV 导入验证 0 缺失；父表 relfilenode=0 无可导出数据，DDL 输出不含 PARTITION BY，导入需手工重建分区结构）。
- **v1.0.5**：新增运行提示——TOAST 文件非空但索引 0 valueid（数据库实例在线且最近写入未 CHECKPOINT，磁盘堆/TOAST 页未完整落盘，如用户实测的 57049 行场景）时输出警告；未找到目标表时提示可能未 CHECKPOINT（磁盘 sys_class 与数据文件 relfilenode 不一致，建议 CHECKPOINT 或 --table-name）。与 Python 版逐逻辑对照确认一致：natts 低位读取（t_infomask2 & 0x07FF）、TOAST 失败占位（__TOAST_MISSING__ 输出 NULL）、页大小自动探测、CLI 32 个参数完全对齐；金仓 V8 在线/V9 副本全功能回归 + PG12-18 全版本 × 8/16/32KB 全 21 项：50 列 10 万行中文随机数据导出→导入（SQL 与 CSV 双通道）全部 count=100000、双向 EXCEPT 0/0 通过。
- **v1.0.2**：修复 `-o` 指定目录时输出文件路径解析。
- **v1.0.1**：--list-tables-db 对齐输出。
- **v1.0.0**：Go 版重写首发，全版本矩阵验证。

## 许可证

MIT License。作者：raysuen
