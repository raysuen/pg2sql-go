# pg2sql v1.0.33

PostgreSQL / KingbaseES 数据文件离线解析导出工具（Go 版，单文件零依赖）。

> 📖 **详细使用手册**：[`docs/USER_GUIDE.md`](docs/USER_GUIDE.md) —— 每个参数详解、参数搭配矩阵、场景专题（分区表/表空间/TOAST/坏块/编码）、FAQ。执行包解压后为 `pg2sql-版本号-平台/` 目录（内含 `pg2sql` 二进制与 `docs/` 手册目录，一同分发）。

直接读取 PG/金仓堆文件（含 TOAST），无需数据库在线即可导出：

- `--sql`：INSERT 语句（自动发现表结构，含 TOAST 重组/已删除行审计；配合 `--ddl` 输出建表语句）
- `--data`：CSV（`--header` 首行字段名，`--delimiter` 自定义分隔符）
- `--count`：行数统计；`--fields` 指定字段导出
- 自动发现表结构、自动探测页大小（8/16/32KB）、自动探测库编码（UTF-8/GBK/Latin1）
- 支持 PG 12~18、KingbaseES V8/V9（ORA/MySQL/PG 三种兼容模式），兼容 8/16/32KB 块大小
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

# --table-name 逗号分隔多表（配合 --sql/--data，必须 -o 目录，逐表独立文件）
pg2sql /pgdata/base/16384 --sql --ddl --table-name t1,t2 -o /out/        # 导出指定多表（SQL+DDL）
pg2sql /pgdata/base/16384 --data --header --table-name s.t1,s.t2 -o /out/  # 导出指定多表（CSV）
# 表名本身含逗号/双引号时用双引号包裹（shell 需单引号包双引号），引号内逗号不分割
# 对照：不加引号 = 逗号分隔多表；加引号 = 引号内是一个完整表名
pg2sql /pgdata/base/16384 --sql --table-name a,b -o /out/               # 2 张表：a、b
pg2sql /pgdata/base/16384 --sql --table-name '"a,b"' -o /out/           # 1 张表：表名含逗号 a,b
pg2sql /pgdata/base/16384 --sql --table-name '"a,b",c' -o /out/         # 2 张表：a,b + c 混排
pg2sql /pgdata/base/16384 --sql --table-name '"a""b"' -o /out/          # 1 张表：表名含双引号 a"b（""=字面"）

# 列出数据库 / 库内表
pg2sql --datadir /pgdata --list-db
pg2sql /pgdata/base/16384 --list-tables-db      # 只列用户对象
pg2sql /pgdata/base/16384 --list-tables-all     # 列出全部对象（含系统对象）
pg2sql /pgdata/base/16384 --tables --sql -o /out/            # 批量导出全部用户表（SQL）
pg2sql /pgdata/base/16384 --all-tables --data -o /out/       # 批量导出全部表（含系统对象，CSV）
pg2sql /pgdata/base/16384 --tables --schema ray --sql -o /out/  # 指定 schema 批量导出
pg2sql /pgdata/base/16384 --schema ray --sql -o /out/             # 等价：--schema 独立触发批量

# 表空间表（真实目录非 base/，含 pg_tblspc/ 软链接前缀路径自动发现，免额外参数；金仓为 sys_tblspc/）
#   pg_relation_filepath 返回如 pg_tblspc/16391/PG_17_202307071/16384/16391，拼数据根目录直接导出：
pg2sql /pgdata/pg_tblspc/16391/PG_17_202307071/16384/16391 --sql -o out.sql
# 兜底：软链接悬空/仅真实路径时，用绝对真实路径 + --datadir/--db-oid 显式定位 catalog：
pg2sql /ts_data/PG_17_202307071/16384/16391 --datadir /pgdata --db-oid 16384 --sql -o out.sql

# 导出元数据 JSON，并离线回灌（catalog-json 模式）
pg2sql /pgdata/base/16384 --export-meta -o meta.json
pg2sql /pgdata/base/16384/16391 --catalog-json meta.json --table-name t1 --sql --ddl

# 指定库编码（默认自动探测）
pg2sql /pgdata/base/16384/16391 --data --encoding gbk -o out.csv

# 并发解析（大表提速，worker 内 解析→转义→产出字符串 + 页序窗口保序转发；内存 O(窗口×并行度)，大表不 OOM）
pg2sql /pgdata/base/16384/16391 --sql --parallel 4 -o out.sql
```

导入验证：`psql -d newdb -f out.sql` 或 `ksql -d newdb -f out.sql`（从哪个版本导出，就导入到哪个版本）。

## 内置类型支持清单（PG 12~18 / KingbaseES V8/V9 全 block size）

| 类别 | 类型（oid/族） | 说明 |
| --- | --- | --- |
| 数值 | int2/int4/int8、numeric、float4/float8、money、oid、serial 族、金仓 mysql TINYINT(8100)/MEDIUMINT(7016)/YEAR(7025)/UNSIGNED 系列 | 完整精度 |
| 字符 | text、varchar、bpchar、name、金仓 clob(8014)、金仓 mysql DATETIME(7952)/ENUM（动态 oid） | 中文/特殊字符/换行/空串/NULL；ENUM 按枚举成员解码 |
| 二进制 | bytea、金仓 mysql BINARY(3383) | bytea 0x hex 可逆；BINARY 去 varlena 头与尾随 \\x00 填充输出文本，中间含 \\x00 时输出 hex（字节可逆） |
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

**限制说明**：① 复合类型（record）磁盘布局受 heap_fill_tuple 的 short-varlena 化与对齐影响、跨版本差异大，当前以原始字节兜底输出（不损坏数据）；② 金仓 MySQL 模式 SET 类型（oid 动态，typtype=y）：磁盘为 16 字节成员位掩码，成员名称不落 catalog（pg_enum/typtypmod 均无），导出为 \\x hex（字节级可审计/可备份），直接 SQL/CSV 回导时金仓 set_in 无法按字节还原（成员掩码被清零），需应用层按成员语义转换后回灌；其余类型均解码为可逆文本/二进制字面量。

## 数据可靠性

- TOAST 外联字段自动关联并重组（light 索引 / 页级 LRU 缓存）
- 编码探测优先读 `pg_database`（UTF-8/GBK/Latin1 可逆解码）
- 页大小自动探测，支持 8KB/16KB/32KB 及金仓变体

## 大表导出：内存与时间估算（v1.0.30 实测）

**实测基准**（PG16.15、2 核机器、数据在 OS page cache 内）：50 列表（id + 49 个 varchar/text 混合，含中文/特殊字符）150 万行，堆文件 **1.9GB**（PG 8KB 页 1GB 分段，实际为 `16459` + `16459.1` 两段，v1.0.30 自动跨段读取）：

| 导出模式 | 耗时 | MaxRSS |
| --- | --- | --- |
| 串行 SQL | 33.3s | 17MB |
| `--parallel 4` SQL | 27.8s | 17MB |
| 串行 CSV（含表头） | 24.8s | 17MB |
| `--parallel 4` CSV | 18.4s | 17MB |
| `--count`（parallel 4） | 11.8s | 17MB |

输出文件体积：SQL ≈ 堆 × **0.77**（实测 1.47GB/1.9GB），CSV ≈ 堆 × **0.51**（实测 0.97GB/1.9GB）。

**内存结论**：v1.0.30 方案 B 为 O(窗口×并行度) 常数级内存——150 万行/1.9GB 全程 MaxRSS **恒 17MB**（串行与并行一致）；旧实现（v1.0.29 及以前）同规模需收集全量行（50 列 150 万行 ≈ 5-6GB）必 OOM（实测 3GB 机器并行回归曾 OOM 杀掉金仓全部实例）。50GB 级表内存仍为常数级（约 20MB + TOAST 页级 LRU 固定上限），**与表大小无关**。

### 与 v1.0.29 及以前的内存对比（50 列表，行均 ~1.3KB）

| 表规模 | v1.0.29 及以前（全量收集） | v1.0.30（流式 + 页序窗口） |
| --- | --- | --- |
| 10 万行 | ≈350-400MB | ≈17MB |
| 100 万行 | ≈3.5-4GB | ≈17MB |
| 150 万行（1.9GB 堆） | ≈5-6GB，**3GB 机器必 OOM**（实测曾 OOM 杀掉金仓全部实例） | **17MB**（串行 / `--parallel 4` 一致） |
| 50GB 级 | ≈170GB+，不可行 | ≈20MB 常数级（+TOAST 页级 LRU 固定上限） |

内存由 **O(行数) 降为 O(窗口×并行度) 常数级**，与表大小无关。

### 与 v1.0.29 及以前的时间对比（2 核，150 万行 / 1.9GB 堆，跨两段文件）

| 模式 | v1.0.29（转义单线程） | v1.0.30（worker 内 解析→转义→产出） | 提升 |
| --- | --- | --- | --- |
| 串行 SQL | 同规模 OOM，无有效数据 | 33.3s | — |
| `--parallel 4` SQL | 同规模 OOM | 27.8s | 较串行 **-17%** |
| 串行 CSV | 同规模 OOM | 24.8s | — |
| `--parallel 4` CSV | 同规模 OOM | 18.4s | 较串行 **-26%** |
| `--count` | 同规模 OOM | 11.8s（parallel 4） | — |

> v1.0.29 在 150 万行级因全量收集直接 OOM，无法取得同规模时间数据；10 万行级别两版本 9 组输出逐字节 md5 一致（语义零回归）。8 核环境并行加速 2.5~3x（历史实测）。

**时间外推**（线性比例，2 核基准；输出文件需额外磁盘空间；更多核下 `--parallel` 收益更高，8 核实测并行加速 2.5~3x）：

| 堆大小 | 串行 SQL | par4 SQL | par4 CSV | 输出 SQL | 输出 CSV |
| --- | --- | --- | --- | --- | --- |
| 5GB | ~1.5 min | ~1.2 min | ~0.8 min | ~3.9GB | ~2.6GB |
| 10GB | ~2.9 min | ~2.4 min | ~1.6 min | ~7.7GB | ~5.1GB |
| 20GB | ~5.8 min | ~4.9 min | ~3.2 min | ~15.4GB | ~10.2GB |
| 30GB | ~8.7 min | ~7.3 min | ~4.8 min | ~23.1GB | ~15.3GB |
| 50GB | ~14.5 min | ~12.2 min | ~8.1 min | ~38.5GB | ~25.5GB |

> 口径：时间为"堆大小 ÷ 1.9GB × 实测耗时"线性外推；主变量是 CPU 解析/转义核数与写盘带宽（2 核机器 CPU 为瓶颈，磁盘快时写盘不额外占时）；50 列大行（行均 ~1.3KB）为基准，窄表（行小）同体积行数更多、按行计费略增，宽表（TOAST 外联多）另有 TOAST 重组开销。

## 坏块（坏页）处理说明

pg2sql 采用"逐页逐元组防护 + 跳过损坏单元 + 剩余数据正常导出"的策略，数据页存在坏块时**不中断导出**，能导出的部分照常输出，坏的部分跳过并在行数中如实反映：

| 层级 | 防护逻辑 | 坏块行为 |
| --- | --- | --- |
| 页头 | lower/upper/special 范围校验（`24 ≤ lower ≤ upper ≤ special ≤ pageSize`），`tryStandardLayout`/`tryAutoLayout` 双模式探测 | 页头损坏的整页跳过，继续解析下一页 |
| 页内条目 | itemid 校验：flags≠NORMAL、偏移/长度越界（`off ≥ pageSize`、`off+ln > pageSize`、`ln < 头大小`） | 仅跳过该条目，不中断整页其他正常条目 |
| 元组头 | `parseTuple` 长度 ≥ 24、`headerConsistent` 校验（t_hoff 范围 24~256 且 位图/oid 长度与 t_hoff 一致） | 头部不一致的元组判坏跳过 |
| 事务状态 | `isInsertAborted`（XMIN_INVALID 且未提交）/ `isDeleted`（XMAX 删除）判定 | aborted 行跳过；已删除行默认跳过（`--deleted` / `--only-deleted` 可导出审计） |
| 字段级 | `extractFieldsDirect` 越界置 NULL、`varPayload` 对 TOAST 头部损坏/超切片返回空、4B/4BC varlena 越界保护 | 单个坏字段按 NULL/空输出，不中断整行其余字段；解码失败字节回退 latin-1（0x00-0xFF 逐字节可逆，不损坏数据） |
| 表级（批量） | 自动发现失败 / 表文件解析失败计数 | `--tables`/`--all-tables` 批量导出结束后输出"成功 N 张表, 跳过 N, 失败 N"，坏表跳过不影响其余表 |

适用说明：
- 坏块跳过发生在页/条目/元组/字段四个粒度，粒度越小，坏块影响的数据越少、剩余导出越完整。
- 单表直接导出时，坏页内可解析的行照常输出（行数少于实际）；批量模式会汇总跳过/失败统计。
- TOAST 外联指针损坏或 TOAST 页坏块时，对应字段输出 NULL/空（不报错中断），其余字段与行不受影响。
- 已知限制：整页损坏且页头无法识别时该页数据不可恢复（与 PG 零填充页 `zero_damaged_pages` 行为等价）；如需坏页明细请结合 `pg_filedump`/`--verbose` 输出核对。

## 功能覆盖矩阵

全功能回归 = 数据面（全类型导出→导入闭环，覆盖 PG12-18 各 block size 与金仓 V8/V9）+ 参数面（CLI 全部参数行为断言）。v1.0.33 起参数面补齐全部 CLI 功能段，并与数据面一起在全版本代表矩阵上执行。

| 功能 | 覆盖方式 | PG12-18 | 金仓 V8/V9 |
| --- | --- | --- | --- |
| `--sql`/`--data` 导出（含中文/特殊字符/NULL/空串/50 列大表） | 数据面 + 参数面 count | 7 版本 × 8/16/32K 全绿 | V8 ORA/PG 模式 + V9 全绿 |
| `--ddl` 建表/索引/序列/默认值/注释 | 参数面 ddl_imp/ddl_struct（建库重放闭环） | 9 组合全绿 | 3 实例全绿 |
| `--count` | 参数面 count（行数精确比对） | 9 组合全绿 | 3 实例全绿 |
| `--fields` 指定字段 | 参数面 fields（CSV 导入闭环） | 9 组合全绿 | 3 实例全绿 |
| `--limit` | 参数面 limit | 9 组合全绿 | 3 实例全绿 |
| `--parallel` 并行导出 | 参数面 parallel（串并行 md5 一致） | 9 组合全绿 | 3 实例全绿 |
| `--list-tables-db`/`--list-tables-all` | 参数面 listdb/listall（用户 vs 系统对象过滤） | 9 组合全绿 | 3 实例全绿 |
| `--list-db` | 参数面 list_db | 9 组合全绿 | 3 实例全绿 |
| `--tables`/`--all-tables` 批量导出 | 参数面 batch/alltables | 9 组合全绿 | 3 实例全绿 |
| `--schema` 批量 | 参数面 batch | 9 组合全绿 | 3 实例全绿 |
| `--table-name`（单表/多表/引号标识符） | 数据面多表 + 引号专项 | 7 版本全绿 | V8/V9 全绿 |
| `--export-meta` + `--catalog-json` | 参数面 meta | 9 组合全绿 | 3 实例全绿 |
| `--encoding`（库解码编码） | 参数面 encoding：UTF8 显式=auto、LATIN1 库闭环、金仓 GBK/GB18030 库闭环 | 9 组合全绿 | V9 全绿；V8 GBK 全绿、GB18030 `SKIP(env)`* |
| `--deleted`/`--only-deleted` | 参数面 deleted（DELETE 未 VACUUM 场景 70/100/30 断言） | 9 组合全绿 | 3 实例全绿 |
| `--header`（CSV 首行字段名） | 数据面 CSV 导入闭环 + 参数面 fields | 7 版本全绿 | V8/V9 全绿 |
| 坏块跳过（页/条目/元组/字段四级） | 参数面 badpage（dd 破坏中间页） | 9 组合全绿 | 3 实例全绿 |
| 表空间（真实目录非 base/，自动发现+兜底） | 参数面 tablespace | 9 组合全绿 | 3 实例全绿 |
| TOAST 外联/压缩/跨段（>1GB） | 数据面 TOAST 专项 + 150 万行大表专项 | 7 版本全绿 | V8/V9 全绿 |
| 多 block size 自动探测（8/16/32K） | 数据面 + 参数面 | 12.22/16.15/18.6 × 8/16/32K 全绿 | V8 8/16/32K + V9 8K 全绿 |

\* V8 实例（ORA/PG 模式）的 ksql 客户端无法向 GB18030 库安全写入 UTF8 SQL（`invalid byte sequence`），属测试环境限制而非工具缺陷；GB18030 解码为字节级码表查找（与内核版本无关），完整断言由 V9 覆盖，GBK 解码在 V8/V9 均验证通过。

数据面回归资产：`pgbuild/reg_pg_129_serial.sh`（PG 66 项）、`pgbuild/reg_kb_130.sh`（金仓 25 项）；参数面：`pgbuild/test_params_pg.sh`（16 断言）、`pgbuild/test_params_kb.sh`（21-22 断言）、`pgbuild/reg_params_all.sh`（全版本调度）。

## 更新记录

- **v1.0.33**：全功能回归补测 + README 功能覆盖矩阵：
  ① 补齐 5 个此前无断言的 CLI 功能段（`--deleted`/`--only-deleted`、`--list-db`、`--list-tables-all`、`--all-tables`、`--encoding`）——`test_params_pg.sh` 断言扩至 **16 项**、`test_params_kb.sh` 扩至 **21-22 项**；
  ② 全版本参数面矩阵：**PG 12.22/16.15/18.6 × 8K/16K/32K 共 9 组合 × 16 断言 = 144 项全绿**；**金仓 V8 ORA 8K / V8 PG 模式 / V9 共 3 实例 × 21-22 断言全绿**（V8 实例的 GB18030 库因 ksql 客户端无法安全写入 UTF8 SQL 标记 `SKIP(env)`，GB18030 解码完整断言由 V9 覆盖，GBK 解码 V8/V9 均验证通过）；
  ③ 测试脚本修复：表空间段目录残留清理（CREATE TABLESPACE 要求空目录）、金仓表空间版本目录层级预建、label 参数化、`--encoding` 段按工具语义（输入解码编码）重设计为"UTF8 库显式 utf8 与 auto 一致 + LATIN1 库（LC_COLLATE 'C'）解码闭环 + 金仓 GBK/GB18030 库闭环"；
  ④ 详见下文"功能覆盖矩阵"。逻辑零改动（仅文档与测试脚本），工具代码无变更。
- **v1.0.32**：表空间路径自动发现（免 `--datadir`/`--db-oid`）：
  ① 单表文件路径含 `pg_tblspc/`（金仓 `sys_tblspc/`）软链接前缀时，自动从路径识别数据根目录与库 OID，直接读取 catalog 导出——`pg_relation_filepath` 返回的相对路径拼上数据根目录即可使用，无需额外参数；
  ② 兜底不变：软链接悬空/目标目录被删/仅持有真实目录路径（无前缀）时，仍用绝对真实路径 + `--datadir` + `--db-oid` 显式定位；
  ③ 补测脚本表空间段升级为"自动发现 + 兜底"双断言：PG16.15 **11/11**、金仓 V9 **19/19** 全绿；PG/金仓表空间表（真实目录非 base/）100 行 SQL/CSV 导出→导入闭环通过；
  ④ README/USER_GUIDE/`--help` 同步新增"表空间自动发现与兜底"使用方法。
- **v1.0.31**：参数面回归补全 + 文档与打包规范更新：
  ① 新增参数面自动化回归脚本 `pgbuild/test_params_pg.sh`（PG 版，10 项检查：`--ddl` 建库重放闭环绕结构/`--count`/`--fields` CSV 导入/`--limit`/`--parallel` vs 串行 md5/`--list-tables-db`/`--schema --tables --ddl` 批量/`--export-meta`+`--catalog-json`/坏块/表空间）与 `pgbuild/test_params_kb.sh`（金仓版，GB18030/GBK 编码库 SQL+CSV 双通道闭环 + 上述参数面 + 表空间 + 坏块）——PG16.15 实测 **11/11**、金仓 V9 实测 **19/19** 全绿；
  ② README"大表导出：内存与时间估算"章节补充 **v1.0.29 vs v1.0.30 内存/时间对比表格**（内存由 O(行数) 降为 O(窗口×并行度) 常数级；时间 2 核基准 par4 SQL -17%、par4 CSV -26%）；
  ③ 表空间表导出说明：表文件不在 `base/` 目录（真实目录位于表空间）时，单文件模式需配合 `--datadir` + `--db-oid` 提供 catalog 才能自动发现；
  ④ 执行包解压目录规范改为 **`pg2sql-版本号-平台/`（二进制 + `docs/` 手册目录）**，与 README/USER_GUIDE 同步更新。
- **v1.0.30**：方案 B 落地——大表导出从"全量收集 merged 后转发"改为 **O(窗口) 流式**（消除大表 OOM 风险）：
  ① **worker 内完成 解析→转义→产出字符串**（`--parallel N` 下转义随解析并行，此前转义仅格式化 goroutine 单线程，是大表 ~90% 耗时瓶颈）；
  ② **页序滑动窗口保序转发**（pending map + next 指针，窗口大小 O(并行度)，不收集全量行）——内存占用从 O(行数)（50 列 100 万行约 3.5-4GB，3GB 机器必 OOM，实测曾 OOM 杀掉金仓全部实例）降为 O(窗口×并行度) 常数级；
  ③ 串行路径同样流式化（预扫轻量判定 + 第二遍流式输出，`probeStandard`/`probeRowValid` 只判 NULL/空串/TOAST 缺失，不构建行不格式化；预扫成本约完整解析 15-25%，页缓存第二遍命中）；两阶段回退（标准 ItemId / 数据区扫描）语义不变；
  ④ `--count` 改为流式计数（CountRows，无全量收集）；
  ⑤ **多段文件支持（>1GB 表）**：PG 8KB 页 RELSEG_SIZE=131072 页=1GB，>1GB 表自动拆分 `relfilenode`、`relfilenode.1`…（16KB=2GB/32KB=4GB 同规则）；v1.0.30 起主表/TOAST/索引预建/预扫全链路自动跨段读取——**此前任何版本 >1GB 表只导主段（1.9GB 表曾只导 80 万行）**；
  ⑥ **并行页序窗口空页修复**：worker 对无行/坏布局/读失败页也发送空结果，保证窗口 next 指针逐页推进（此前空页不发结果 → next 卡死、后续行全丢）。
  验收：PG16 50 列 10 万行（含中文/特殊字符/NULL/空串）+ 11 行特殊字符表，`--sql`/`--data`/`--count`/`--limit 1000`/`--fields` 串行与 `--parallel 4` 共 9 组输出与 v1.0.29 逐字节 md5 一致；SQL/CSV 导入闭环 count/min/max 全对；**150 万行（1.9GB 堆，跨两段）专项**：串行 SQL 33.3s / par4 SQL 27.8s / par4 CSV 18.4s / count 11.8s，四通道均全量 1500001 行、串并行 md5 一致、SQL 导入 1500000 行（202s）/ CSV 导入 1500000 行（60s）闭环通过，MaxRSS 全程恒 17MB（旧实现同规模 ≈5-6GB 必 OOM）；5/10/20/30/50GB 内存-时间估算见 README"大表导出内存与时间估算"章节。
- **v1.0.29**：新增 `docs/USER_GUIDE.md` 详细用户手册（参数详解/搭配矩阵/场景专题/FAQ），源码包包含 `docs/`，执行包内置 `pg2sql-docs/` 目录与二进制一同分发；README 顶部加手册链接。逻辑零改动（仅版本号/文档），全版本回归后打包。
- **v1.0.28**：`--table-name` 帮助与 README 示例补齐为完整对照（不加引号=逗号分隔多表 / 加引号=引号内完整表名）：`--table-name a,b`（2 张表）、`--table-name '"a,b"'`（1 张表名含逗号）、`--table-name '"a,b",c'`（含逗号表名+普通表混排）、`--table-name '"a""b"'`（表名含双引号转义）。`--help` 已同步，逻辑零改动。
- **v1.0.27**：`--table-name` 支持双引号包裹表名（消除含逗号/引号标识符歧义，与 SQL 标识符惯例一致）：引号内逗号=字面字符不分割；`""` 双写=字面双引号（PG quote_ident 同规则）；引号未闭合精确报错；空表名/空引号过滤。shell 侧需单引号包双引号（`--table-name '"a,b",c'`），否则 shell 会先吃掉双引号。单表路径改用解析后表名（去引号/trim），未传 `--table-name` 时保持原兜底链。`--help` 已同步。验证：含逗号表名（`"a,b"`）单表/多表/`--ddl` 重建导入闭环、含引号表名（`a"b`）转义、引号未闭合报错、现有场景（单表/relfilenode/空输入/不带引号多表）全部兼容；全版本回归后打包。
- **v1.0.26**：`--table-name` 支持逗号分隔多表（如 `--table-name ray.t_special_test,ray.test02`）——拆分后逐表定位导出，每表独立输出文件（沿用批量命名 `schema.table.sql|.csv`）；多表导出强制要求 `-o` 目录 + `--sql` 或 `--data`，未匹配的表名精确报错（防拼错静默丢表），逗号间空白自动清理。单表行为完全不变（无 `-o` 时输出到当前目录）。`--help` 已同步。验证：多表 SQL/CSV 导出→导入闭环（含中英文特殊字符）通过 + 边界（缺 -o/缺类型/未匹配/空表名）全通过；全版本回归后打包。
- **v1.0.25**：深度代码审核修复（5 项，全版本回归通过后发布）：
  ① **P-1【严重·PG 数据丢失】maxRowNatts 重读 t_infomask2 高位标志位当列数**：PG 分支 `(i2>>11)&0x7FF` 误把 HEAP_KEYS_UPDATED/HOT_UPDATED/ONLY_TUPLE/IS_PARTITION/CANT_RECORD（0x0800~0x8000）标志当行内列数（读成 1/2/4/8/16），幽灵列截断误伤正常表——实测 PG18 21 列表 + 等长 UPDATE（HOT 行 infomask2=0x8006）被截成 16 列、c16~c20 数据静默丢失。统一改回低 11 位（HeapTupleHeaderGetNatts = i2 & 0x07FF，与金仓一致；v1.0.4 曾修复、后经重构回归）。新增 `pgbuild/test_hot.sh` 全版本 HOT 专项回归：PG12-18 × 8/16/32KB 共 11 组建 21 列表 + 等长 UPDATE + VACUUM，验证 21 列完整 + SQL/CSV 双通道 md5 闭环全绿；金仓 V9 MySQL HOT 场景 21 列完整、导入 0 错误。
  ② **P-2【功能级】TOAST 索引预建真正并行化**：BuildIndexParallel 原实现 extractPageChunks 解析在生产者单 goroutine 串行（worker 仅做 map append），预建阶段并行收益≈0；重构为 worker 池各自 ReadAt+页内解析（消除共享读缓冲复用覆盖风险），归并后按 valueid+seq 排序输出。实测（金仓 V9 MySQL，2 万行/2 列 7KB 约 300MB TOAST）：serial 3.72s → parallel=4 1.54s（2.42x），serial/parallel 导出 SQL md5 完全一致。
  ③ **P-3【低】PageCache 真 LRU 淘汰**：CacheOrder 队列记录访问顺序，命中页移到队尾、超限从队头淘汰最久未用（原实现 map 随机遍历删除，注释声称"最旧"但实际随机）。
  ④ **P-4【低·理论】timestampFromUS 溢出修复**：原 `time.Duration(sec)*time.Second` 上限约 292 年（公元 2262 年后 timestamp 溢出为错值），改用 Howard Hinnant 公历逆算法（civilFromDays，int64 全程无溢出）支持 PG 全时间范围（4713BC~294276AD）；2000-01-01 前后正常范围输出与旧实现逐字节一致（单测验证）。
  ⑤ **P-5【低·质量】buildDdlStatements 缩进清理**。
  另：logf 增加互斥锁防并发模式下日志交错。
  验证：PG12-18 × 8/16/32KB 全类型/GAP/HOT/TRUNCATE 四类 44 组全绿；金仓 V8 ORA×3（8/16/32K）+ V9 MySQL + V8 PG 模式 5 实例全类型/P1/TOAST TRUNCATE 15 项全 OK。
- **v1.0.24**：主表行解析 + TOAST 重组并发化（`--parallel N` 同时加速 TOAST 索引预建与主表按页分片行解析）：worker 池按页分片解析 + 滑动窗口保序归并输出，SQL/CSV 输出行序与串行逐字节一致（实测 md5 相同）；修复并发解析期 roleMap 注入 data race（角色映射改为解析前一次性注入，解析期只读）；PageCache 增加 RWMutex 保护、TOAST 文件句柄 sync.Once 初始化，保障并发安全（race detector 全模式通过）。实测（2 核、2 万行/333MB TOAST）：parallel=2/4 总耗时较串行 -7%~-8%；多核（4~8 核）环境按 Amdahl 估算可获 1.5~2.5x 加速。
- **v1.0.23**：README 补充"坏块（坏页）处理说明"章节（文档更新，代码逻辑不变）：明确逐页逐元组防护策略——页头损坏整页跳过、页内坏 itemid 单条跳过、元组头不一致判坏跳过、aborted/已删除行跳过（`--deleted` 可审计）、坏字段按 NULL/空保护不中断、TOAST 坏指针/坏页对应字段输出 NULL/空，批量导出输出成功/跳过/失败统计；坏块粒度越小影响越少，剩余数据正常导出。
- **v1.0.22**：补齐金仓 MySQL 兼容模式特有类型解码（金仓三种兼容模式 ORA/MySQL/PG 实测布局一致，V8 PG 模式实例新增验证通过）：
  ① **TINYINT**（oid 8100，typinput=tinyintin，1 字节有符号）：此前导出原始字节，新增 `tinyintin→decodeInt1` 解码为数值（`127`/`-128`），unsigned 系列经 domain 递归自动命中；
  ② **BINARY**（oid 3383，typinput=binaryin）：此前导出含 varlena 头的原始字节（含 \\x00 填充导致 SQL/CSV 导入报 `invalid byte sequence`），新增 `binaryin→decodeBinary`（剥离 varlena 头、去除尾随 \\x00 填充输出文本，中间含 \\x00 时输出 \\x hex 字节可逆）；
  ③ **SET 类型**（oid 动态，typtype=y）：磁盘为 16 字节成员位掩码（[10:14] int32），成员名称不落 catalog（pg_enum/typtypmod 均无），导出为 \\x hex（字节级可审计）；实测金仓 set_in 接受 `'x,y'` 文本但无法按字节还原 \\x 输入（掩码清零），已记录 README 限制说明；
  ④ **decodeDefault 兜底加固**：utf8.Valid 允许 \x00，含 \x00 的未知类型此前按文本输出导致 SQL/CSV 导入失败，现含 \x00 一律输出 \\x hex（字节可逆）。
  验证：金仓 V9R3C18 MySQL 模式 t_my_types 特有类型表（TINYINT/MEDIUMINT/DATETIME/YEAR/BLOB/TEXT/ENUM/SET/FLOAT/DOUBLE/DECIMAL/BINARY 17 列 3 行，含中英文/特殊字符/极值/空串/NULL）除 SET 外 16 列导出→导入 EXCEPT 0 差异；金仓 V8R6C9B14 ORA 模式 t_ora_types 特有类型表（NUMBER/VARCHAR2/NVARCHAR2/BYTEA(RAW)/CLOB/BLOB/DATE/TIMESTAMP/TIMESTAMPTZ/LONG 13 列 3 行）SQL/CSV 双通道 md5 全一致；金仓 V8 PG 兼容模式（--dbmode=pg initdb 实例）全类型分区表/P1/TRUNCATE 专项全绿；金仓 V8×3（ORA 8/16/32KB）+V9（MySQL）+PG 模式全类型 SQL/CSV 双通道 EXCEPT 0；PG12-18 × 8/16/32KB 全类型/P1/GAP/TRUNCATE 11 组全绿。
- **v1.0.21**：修复 TOAST 外联读取路径缺陷（坏块分析实测发现，P1 级）：
  ① **TOAST 路径用 relfilenode 而非 OID**：`reltoastrelid` 存的是 toast 表 **OID**，磁盘文件按 **relfilenode** 命名。表经 TRUNCATE/重建后两者分离（实测：reltoastrelid=24619，toast 数据文件=27622），原实现拿 OID 拼路径指向旧文件/空文件 → `TOAST 索引预建 0 个 valueid` → **外联字段（大字段/TOAST 值）静默全量导出 NULL**，且无任何报错。现自动发现阶段建立 `OID→relfilenode` 映射（`ToastRelFile` 字段），路径推导优先用 relfilenode；`--catalog-json` 旧文件无该字段时回退 OID（向后兼容）。验证：TRUNCATE 过的 3000 行大字段表修复前 3000 行全 NULL → 修复后 3000 行 0 NULL。
  ② **自动发现失败提示增强**：目标数据文件存在（relfilenode 数字名）但未解析到对应表时，错误信息追加"其表属性可能因 pg_attribute 读取失败(文件损坏/未落盘)而缺失"的针对性提示（原仅泛化提示未 CHECKPOINT）。
  另：坏块健壮性实测结论——主表页头损坏跳整页、itemid 损坏跳单行、全零洞跳页、文件截断截尾，TOAST 页损坏/截断时受影响字段降级 NULL、行保留，程序全程不崩溃；pg_attribute 损坏时自动发现显式报错退出 1。
- **v1.0.20**：修复 3 处 P1 级解压/边界缺陷并全版本回归验证（金仓 V8/V9 + PG12-18 × 8/16/32KB 全类型、50 列特殊字符、缺口类型、P1 专项矩阵 40+ 组全绿）：
  ① **4BC 行内压缩掩码**（varlenaParse）：小端磁盘 `VARATT_IS_4B_C = (header & 0x03) == 0x02`、`SET_VARSIZE_4B_C = (len<<2)|0x02`；原实现用 `(first & 0x06) == 0x06`，仅当压缩后总长为奇数时等价，偶数时把 4BC 判成未压缩 4B → 压缩流（含 NUL 控制字节）按文本输出乱码 → SQL/CSV 导入报错。已改为官方 `(first&0x03)==0x02`。
  ② **varPayload 越界保护**（types.go）：4BC 分支 `comp := b[4:total]` 补 `total>len(b)` 校验 + 压缩流 `len>=5` 判定；方法位由 toastDecompress 按流内 tcinfo 高 2 位分发（PGLZ=0/LZ4=1）。
  ③ **pglzDecompress byte 移位溢出**（最深根因）：`off := int((b1&0xF0)<<4)|int(b2)` 中 b1 为 uint8，`(b1&0xF0)<<4` 在 uint8 域移位截断（off 高 4 位丢失，最大仅 255），off≥256 的 match 从错误位置复制导致解压 md5 不一致（金仓 V8 c_extcomp 复现、PG 未触发纯属数据偶然）。修复为 `int(b1&0xF0)<<4 | int(b2)`（先提升 int 再移位）。
  另：PG P1 回归脚本修正 postgres 库 OID 动态获取（PG12-14 的 postgres 库 OID 从 12975 起而非 5，写死导致导出路径错误）。
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
