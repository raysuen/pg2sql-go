# pg2sql v1.2.19

**语言：中文 | [English](README.en.md)**

PostgreSQL / KingbaseES 数据文件离线解析导出工具（Go 版，单文件零依赖）。

> 📖 **详细使用手册**：[`docs/USER_GUIDE.md`](docs/USER_GUIDE.md) —— 每个参数详解、参数搭配矩阵、场景专题（分区表/表空间/TOAST/坏块/编码）、FAQ。执行包解压后为 `pg2sql-版本号-平台/` 目录（内含 `pg2sql` 二进制与 `docs/` 手册目录，一同分发）。

直接读取 PG/金仓堆文件（含 TOAST），无需数据库在线即可导出：

- `--format sql|csv|text`：数据格式单选（**v1.2.5 推荐写法**，等价下方 `--sql`/`--data`/`--text`；只能出现一次，不能与旧旗标混用）
- `--sql`：INSERT 语句（自动发现表结构，含 TOAST 重组/已删除行审计；配合 `--ddl` 输出建表语句）
- `--data`：CSV（`--header` 首行字段名，`--delimiter` 自定义分隔符）
- `--text`：PostgreSQL COPY text 格式（v1.1.1；制表符分隔 + `\N` 表 NULL，后缀 `.txt`）
- `--ddl`：只导 DDL 时单独使用即可（纯 DDL 模式，不需要 `--format`）；与数据格式同用时 DDL 独立成 `.ddl` 文件
- **v1.2.5 起**：`--sql` / `--data` / `--text` 严格互斥（任两个同现报错退出）；`--header` 仅 csv 格式可用，其余格式指定会报错
- `--count`：行数统计；`--fields` 指定字段导出
- 自动发现表结构、自动探测页大小（8/16/32KB）、自动探测库编码（UTF-8/GBK/Latin1）
- 支持 PG 12~18、KingbaseES V8/V9（ORA/MySQL/PG 三种兼容模式；V9 含 R1C10/R3C18 细分版本），兼容 8/16/32KB 块大小
- `--list-tables-db` 列出库内用户对象（默认过滤系统对象）；`--list-tables-all` 列出全部对象（含系统对象）
- `--tables` / `--all-tables` 批量导出全部用户表 / 全部表（含系统表）；`--schema NAME` 独立使用等价于 `--tables --schema NAME`（批量导出指定模式）；多表导出必须搭配 `-o` 输出目录与 `--sql` / `--data` 导出类型
- DDL 自动输出 PRIMARY KEY（解析 pg_index/sys_index，自动兼容 PG12-18 / 金仓 V8 / 金仓 V9 布局差异）
- 分区表：叶子分区各自独立 relfilenode，可按分区 relfilenode 或 `--table-name` 逐分区导出；`--table-name` 指定分区父表（relkind='p'）时自动展开全部叶子子分区导出（v1.0.36，多级分区递归）
- **v1.0.38 新增**：`--batch-rows N` 多值 INSERT（默认 1=关，恢复提速）；`--jobs N` 批量表间并行（默认 1=关，每表独立文件）；`--report` 导出报告 report.json（表/行数/坏块/文件/checksum/耗时）；`--checksum crc32|md5` 流式校验和；`--import-script none|auto|sql|csv` 导入辅助脚本（默认 none=关，sql→psql/ksql -f，csv→\copy 联动 `--fields`）
- **v1.0.45**：P2/P3 缺陷修复轮（13 条审查中 12 条修复）：B1 导入脚本选型、B2/B3 单表/批量脚本编码与 HEADER 联动、B4 PG 编码 ID 映射补齐、B5 PG18 relnatts 分版本、B6 int16 补码、B7 并行 roleMap 竞争、B9 --tz 时区、B10 interval 符号、B11 表达式索引告警、B12 TDE 错密钥告警、B13 report 双写；全版本回归（PG12-18 + 金仓 V8R6/V9R1C10 各 3 模式 × 3 页大小，重点多字符集）通过后打包。

- **v1.0.40 新增**：`--full-schema` 完整 Schema DDL（配合 `--ddl`：函数/触发器/外键/CHECK/视图依赖清单，`--schema-objects` 过滤类别）；`--large-objects` 导出 pg_largeobject 大对象（`--large-object-oids` 指定 OID，`lo_from_bytea` 语句，联动 `--import-script`）；`--progress` 导出进度与 ETA（stderr 输出，TTY 原地刷新/管道逐行）

## 构建

```bash
go build -o pg2sql .
# 交叉编译 aarch64
GOOS=linux GOARCH=arm64 go build -o pg2sql-arm64 .
```

## 使用示例

> 💡 以下为基础用法速览；**每个参数的详解、参数搭配矩阵与场景专题（分区表/表空间/TOAST/坏块/编码/导入）详见 [`docs/USER_GUIDE.md`](docs/USER_GUIDE.md)**。

```bash
# 导出 INSERT 语句（自动发现表结构）
pg2sql /pgdata/base/16384/16391 --sql -o out.sql

# 导出 DDL + INSERT
pg2sql /pgdata/base/16384/16391 --sql --ddl -o out.sql

# 导出 CSV（首行字段名）
pg2sql /pgdata/base/16384/16391 --data --header -o out.csv

# 指定字段导出
pg2sql /pgdata/base/16384/16391 --data --fields id,name --header -o out.csv

# v1.2.5 推荐写法：--format 单选数据格式（sql / csv / text）
pg2sql /pgdata/base/16384/16391 --format csv  -o out.csv
pg2sql /pgdata/base/16384/16391 --format text -o out.txt
pg2sql /pgdata/base/16384/16391 --format csv --ddl -o out    # 数据 out.csv + DDL 独立 out.ddl

# 只导 DDL，不导数据（不需要 --format）
pg2sql /pgdata/base/16384/16391 --ddl -o out.ddl

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

# --table-name 指定分区父表：自动展开全部叶子子分区（v1.0.36，支持多级分区）
pg2sql /pgdata/base/16384 --sql --table-name public.sales -o /out/      # 父表 sales 展开为 sales_2023..2026 批量导出（-o 需目录）
pg2sql /pgdata/base/16384 --sql --table-name public.sales -o out.sql   # 父表仅 1 个叶子子分区时保持单表语义（-o 可为文件）
pg2sql /pgdata/base/16384 --sql --table-name public.sales,public.t1 -o /out/  # 混合：父表展开 + 普通表
# 注意：空分区父表（无子分区）导出 0 行；老式 INHERITS 继承父表（relkind='r'）不展开，逐表导出

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
# v1.0.34 目录模式（--datadir + --db-oid 等效 base/<oid>，表空间表自动发现，无需知道真实路径）：
pg2sql --datadir /pgdata --list-db                          # filepath 列多行（base + 每表空间一行）
pg2sql --datadir /pgdata --db-oid 16384 --list-tables-db    # 含"表空间"列（0=base，非 0=pg_tblspc/<oid>）
pg2sql --datadir /pgdata --db-oid 16384 --schema public --sql -o /out/   # 批量导出（含表空间表）
pg2sql --datadir /pgdata --db-oid 16384 --table-name public.t_ts --sql -o out.sql   # 单表（含表空间表）

# 导出元数据 JSON，并离线回灌（catalog-json 模式）
pg2sql /pgdata/base/16384 --export-meta -o meta.json
pg2sql /pgdata/base/16384/16391 --catalog-json meta.json --table-name t1 --sql --ddl

# 指定库编码（默认自动探测）
pg2sql /pgdata/base/16384/16391 --data --encoding gbk -o out.csv

# 并发解析（大表提速，worker 内 解析→转义→产出字符串 + 页序窗口保序转发；内存 O(窗口×并行度)，大表不 OOM）
pg2sql /pgdata/base/16384/16391 --sql --parallel 4 -o out.sql

# v1.0.38 新增：
# 多值 INSERT（恢复提速：N≥2 合并为 INSERT ... VALUES (...),(...)，默认 1=逐行单条）
pg2sql /pgdata/base/16384/16391 --sql --batch-rows 1000 -o out.sql
# 批量表间并行（每表独立文件，默认 1=串行；与 --parallel 表内并行叠加）
pg2sql /pgdata/base/16384 --tables --sql --jobs 4 -o /out/
pg2sql /pgdata/base/16384 --sql --table-name t1,t2,t3 --jobs 4 -o /out/
# 导出报告 + checksum（报告 report.json：表/行数/坏块/文件/字节/checksum/耗时）
pg2sql /pgdata/base/16384 --tables --sql --jobs 4 --report --checksum md5 -o /out/
# 导入辅助脚本（sql→psql/ksql -f；csv→\copy 联动 --fields；批量生成 import_all.sh）
pg2sql /pgdata/base/16384/16391 --sql --import-script auto -o /tmp/
pg2sql /pgdata/base/16384/16391 --data --fields id,name --import-script csv -o /tmp/
# v1.0.40 新增：
# 完整 Schema DDL（配合 --ddl：函数/触发器/外键/CHECK/视图依赖清单；--schema-objects 过滤类别）
pg2sql /pgdata/base/16384 --ddl --full-schema -o schema_objects.ddl
pg2sql /pgdata/base/16384 --ddl --full-schema --schema-objects fk,function -o fk_fn.ddl
# 大对象导出（pg_largeobject → lo_from_bytea 语句；--large-object-oids 指定 OID；联动 --import-script）
pg2sql /pgdata/base/16384 --large-objects -o large_objects.sql
pg2sql /pgdata/base/16384 --large-objects --large-object-oids 990001 -o lob.sql
# 导出进度与 ETA（stderr 输出；TTY 原地刷新，管道逐行）
pg2sql /pgdata/base/16384/16391 --sql --progress -o out.sql
# v1.0.53 新增：
# 完整性自证报告（db_state 关库状态 + reltuples/relpages 对照 + 跳过清单 + 类型覆盖度 + verdict 结论）
pg2sql /pgdata/base/16384 --tables --sql --report -o /out/
# 角色 DDL（离线解析 global/pg_authid + pg_auth_members → roles.ddl，含密码密文/有效期/成员关系）
pg2sql /pgdata/base/16384 --roles -o /out/
pg2sql /pgdata/base/16384 --roles-nopassword -o /out/     # 同上，但省略 PASSWORD 密文
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

### jobs × parallel 资源计算（v1.0.38）

`--jobs N`（表间并行）与 `--parallel P`（表内页级并行）正交叠加：

- **总并行度 = jobs × parallel**，建议对齐核数（超订 → 上下文切换 + 磁盘 I/O 争抢，救援磁盘不稳时优先降 `--jobs`）。
- **内存公式**：`总内存 ≈ jobs × (单表基础 ~12MB + parallel × 单 worker 增量)`；8KB 页 worker 增量约 2-3MB、16KB 约 3-5MB、32KB 约 4-8MB（页序窗口 O(并行度)，与表大小无关）。**几十~几百 MB 量级，永远安全区**。

8 核机器四组合例（32KB 页按单 worker 增量 8MB 计）：

| 组合 | 总并行度 | 估算内存 |
| --- | --- | --- |
| `--jobs 1 --parallel 8` | 8 | ~12MB + 8×8MB ≈ **76MB** |
| `--jobs 4 --parallel 2` | 8 | 4×(12MB + 2×8MB) ≈ **112MB**（推荐，≈串行内存 4 倍内） |
| `--jobs 8 --parallel 1` | 8 | 8×12MB ≈ **96MB**（表间并行，单表内存最小） |
| `--jobs 8 --parallel 4` | 32 | 8×(12MB + 4×8MB) ≈ **352MB**（严重超订，不推荐） |

**救援场景建议**：磁盘不稳/IO 慢时先固定 `--parallel 2~4` 保单表吞吐，再按核数分配 `--jobs`；50 表示例（16 核、`--jobs 8 --parallel 2`）：内存 ≈ 8×(12MB+2×8MB)=224MB，远小于旧版全量收集单表 5-6GB 量级。

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

## 金仓 TDE 透明加密离线解密（v1.0.39）

pg2sql 支持对金仓 TDE 透明加密的数据文件做**离线解密导出**（无需实例在线、无需数据库密码），覆盖两种加密场景：

| 加密场景 | 创建语法 | pg2sql 参数 | 解密原理 |
| --- | --- | --- | --- |
| 表加密 | `CREATE TABLE t (...) ENCRYPTED BY 'KEY'` | `--enckey KEY` | 数据页 offset64+ 按 SM4-ECB 解密（页头 0-63 明文；密钥 = ENCRYPTED BY 字符串 + 0x00 填 16B；TOAST 同密钥） |
| 表空间加密 | `CREATE TABLESPACE ts LOCATION '...' WITH (encryption=true, encmethod='sm4')` | `--wallet-password PW`（可选 `--wallet-dir DIR`） | 钱包链离线推导：KEK→MK→TSPK（无需密码）；tspkey.kr 多表空间条目自动匹配 |

使用示例：

```bash
# 表加密表离线解密导出（文件路径模式）
pg2sql /pgdata/base/16384/21747 --sql --table-name public.t_enc --enckey k1ngb2se -o /tmp/out/

# 表空间加密表（目录模式：自动发现表空间 + 自动探测钱包）
pg2sql --datadir /pgdata --db-oid 16384 --table-name public.t_tspc \
  --sql --wallet-password 'Wal1et#Tde2026' -o /tmp/out/
```

说明与边界：
- **无需密码即可离线推导**表空间密钥（钱包随数据目录保存，宕机场景可直接取用）；`--wallet-password` 仅做 SM3 校验并告警（V8 校验偏移与 V9 不同，告警"密码校验失败"不影响解密）。
- `--wallet-dir` 默认自动探测 `<PGDATA>/.wallet`；多表空间多钱包条目自动按表空间 OID 匹配。
- 串行/并行（`--parallel N`）路径均注入解密，导出内容逐字节一致。
- 前提：金仓实例需加载 `sysencrypt`（`shared_preload_libraries` 首项含 `sysencrypt`），否则无法创建加密对象；离线解密侧不依赖该扩展。
- 未指定密钥时按明文解析加密页会得到乱码/0 行——若实例在线且怀疑加密，请先确认表是否 `ENCRYPTED BY` / 表空间是否 `encryption=true`。

## 功能覆盖矩阵

全功能回归 = 数据面（全类型导出→导入闭环，覆盖 PG12-18 各 block size 与金仓 V8/V9）+ 参数面（CLI 全部参数行为断言）。v1.0.33 起参数面补齐全部 CLI 功能段，并与数据面一起在全版本代表矩阵上执行；v1.0.34 起参数面新增"目录模式表空间自动发现（多表空间 + 5 万行表空间大表）"断言。

| 功能 | 覆盖方式 | PG12-18 | 金仓 V8/V9 |
| --- | --- | --- | --- |
| `--sql`/`--data` 导出（含中文/特殊字符/NULL/空串/50 列大表） | 数据面 + 参数面 count | 7 版本 × 8/16/32K 全绿 | V8 ORA/PG 模式 + V9 全绿 |
| `--ddl` 建表/索引/序列/默认值/注释 | 参数面 ddl_imp/ddl_struct（建库重放闭环） | 9 组合全绿 | 3 实例全绿 |
| `--count` | 参数面 count（行数精确比对） | 9 组合全绿 | 3 实例全绿 |
| `--fields` 指定字段 | 参数面 fields（CSV 导入闭环） | 9 组合全绿 | 3 实例全绿 |
| `--limit` | 参数面 limit | 9 组合全绿 | 3 实例全绿 |
| `--parallel` 并行导出 | 参数面 parallel（串并行 md5 一致） | 9 组合全绿 | 3 实例全绿 |
| `--batch-rows` 多值 INSERT（v1.0.38） | 参数面 batch（多值导入闭环 + 与逐行 md5 一致） | 9 组合全绿 | 3 实例全绿 |
| `--jobs` 表间并行（v1.0.38） | 参数面 jobs（jobs>1 与 jobs=1 输出 md5 一致 + 每表文件齐全） | 9 组合全绿 | 3 实例全绿 |
| `--report`/`--checksum`（v1.0.38） | 参数面 report（report.json 字段 + 校验和与独立计算一致 + 坏块计数） | 9 组合全绿 | 3 实例全绿 |
| `--import-script`（v1.0.38） | 参数面 imp_script（sql/csv 脚本生成 + 内容断言 + import_all.sh） | 9 组合全绿 | 3 实例全绿 |
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
| 表空间（真实目录非 base/，自动发现+兜底） | 参数面 tablespace + tspace_big/tsdir_batch/tspace_col | 9 组合全绿 | 3 实例全绿 |
| 目录模式表空间自动发现（v1.0.34：`--datadir --db-oid` 下 `--list-db` 多行 filepath / `--list-tables-db` 表空间列 / `--schema`/`--table-name`/`--all-tables` 定位表空间表） | 参数面 listdb_multi/tspace_col/tsdir_batch/tspace_big（多表空间 + 5 万行中文/特殊字符表空间大表 SQL+CSV 导入闭环） | 9 组合全绿 | 3 实例全绿 |
| 金仓 TDE 透明加密（v1.0.39：`--enckey` 表加密 / `--wallet-password`+`--wallet-dir` 表空间加密，含 TOAST） | 全版本加密专项（reg_tde_r1c10.sh + V8/V9 实测）：表加密 100 行含 TOAST 长文本 + 表空间加密 50 行明文验证 | 不适用（PG 无 TDE） | V8R6 ORA/PG 8/16/32K + V9R1C10 3模式×3页大小 + V9 老版，14 实例 28/28 全绿 |
| 大表导出（10 万行 t_big + 5 万行表空间大表；150 万行专项） | 参数面 parallel/badpage + 数据面大表专项 | 9 组合全绿 | 3 实例全绿 |
| TOAST 外联/压缩/跨段（>1GB） | 数据面 TOAST 专项 + 150 万行大表专项 | 7 版本全绿 | V8/V9 全绿 |
| 多 block size 自动探测（8/16/32K） | 数据面 + 参数面 | 12.22/16.15/18.6 × 8/16/32K 全绿 | V8 8/16/32K + V9 8K 全绿 |

\* V8 实例（ORA/PG 模式）的 ksql 客户端无法向 GB18030 库安全写入 UTF8 SQL（`invalid byte sequence`），属测试环境限制而非工具缺陷；GB18030 解码为字节级码表查找（与内核版本无关），完整断言由 V9 覆盖，GBK 解码在 V8/V9 均验证通过。

## 能力矩阵：已覆盖 / 近似 / 不支持（v1.0.53）

离线堆文件导出最容易出的问题不是"报错"，而是"静默不完整"——退出码 0、文件生成，但少了段文件、少了 TOAST、少了表。下表按"还原保真度"把能力分三档，并给出各档的自证手段（对应 `--report` 的字段）。

| 档位 | 含义 | 覆盖内容 | 自证方式 |
| --- | --- | --- | --- |
| **精确还原** | 字节/语义与源库一致，回灌后与在线查询结果相同 | 全内置类型值（数值/字符/二进制/布尔/位/日期时间/JSON/XML/数组/几何/网络/全文/范围/reg*/pg_lsn/txid_snapshot/枚举）；TOAST 外联+压缩+跨段；坏块四级跳过；删除行（`--deleted`/`--only-deleted`） | `exports[].rows` + `bad_blocks`/`toast_missing`/`toast_corrupt` + `rows_check`/`pages_check` |
| **结构还原** | DDL 可原样重建对象，回灌后结构等价 | 表/列/默认值/主键/唯一/CHECK/外键/索引（含表达式与部分索引）/序列与 setval/注释/表空间/分区（父 `PARTITION BY` + 子 `PARTITION OF`，含多级与 DEFAULT）/域类型与域 CHECK/函数/触发器/视图依赖清单/大对象 | `exports[]` 中 `format=ddl` 条目；`--full-schema` 的 `schema_objects.ddl` |
| **近似输出** | 值可逆（字节保真），但文本形态可能与源库不同；或结构需人工确认 | 无专用解码器的类型（如 `pg_ndistinct`/`pg_mcv_list`/`pg_dependencies` 等内部类型）→ 文本透传或 `\x` hex；金仓 mysql `SET` → hex；`record` 复合值 → record 文本 | `unknown_types[]`（含 oid/名称/列清单）+ `verdict.reasons` |
| **不支持 / 需人工** | 工具不产出或明确跳过，报告里可见原因 | 共享目录数据（`pg_database`/`pg_authid`/`pg_tablespace` 等 `global/` 存储对象，`skipped[].reason=shared_catalog`；角色改用 `--roles`）；索引/视图/序列/TOAST 等非行存对象的"数据"；分区父表的"数据"（无存储）；传统 `INHERITS` 继承父表的自动展开 | `skipped[]`（reason: `shared_catalog`/`relkind`/`partition_parent`/`no_storage`/`schema_filter`/`system_schema`/`named_missing`/`partition_no_leaf`） |

**读取顺序建议**：先看 `verdict.level`（ok/warn/fail），为 `fail` 时按 `reasons` 定位到 `exports[]`/`skipped[]`；再看 `summary.tables_rows_under` / `tables_pages_short`——后者代表实测数据文件页数少于 `pg_class.relpages`，通常意味着段文件缺失或被截断，属于必须重取快照的硬信号。

## 完整性自证：report.json（v1.0.53）

`--report` 从 v1.0.53 起不只记录"导了多少"，还要能回答"该导多少、少了什么、为什么少"。

```jsonc
{
  "tool": "pg2sql", "version": "1.0.53", "generated_at": "2026-10-27T12:41:53+08:00",
  "db_encoding": "utf-8",          // 本次导出实际使用的解码编码
  "encoding_baseline": "utf-8",    // 库级基线（表级覆盖见 exports[].enc_override）
  "page_size": 8192, "jobs": 4, "checksum_alg": "md5",
  "verdict": { "level": "warn", "reasons": ["…"] },     // ok | warn | fail + 原因
  "summary": {
    "tables_exported": 76, "tables_skipped": 403, "tables_failed": 0,
    "domains_unresolved": 0,   // （v1.2.18）DEFAULT 无法离线还原的域数量，不混入 tables_skipped
    "rows_exported": 23222, "rows_estimate": 23041,
    "tables_rows_under": 0, "tables_pages_short": 0,
    "bad_blocks": 0, "bad_blocks_system": 0, "toast_missing": 0, "toast_corrupt": 0,
    "bytes": 5679564, "elapsed_ms": 769, "unknown_types": 3
  },
  "db_state": {                    // 快照来源（pg_control / 金仓 sys_control 离线解析）
    "control_state": "DB_IN_PRODUCTION", "control_state_code": 6,
    "checkpoint_lsn": "0/68A4B88", "checkpoint_time": "2026-10-27T04:19:53Z",
    "pg_version": 17, "is_kingbase": false, "note": "…"
  },
  "skipped_summary": { "relkind": 389, "partition_parent": 3, "shared_catalog": 11 },
  "skipped": [ { "table": "pg_catalog.pg_auth_members", "reason": "shared_catalog", "detail": "…" } ],
  "unknown_types": [ { "oid": 3361, "name": "pg_ndistinct", "kind": "fallback", "column_count": 1 } ],
  "exports": [ {
    "table": "public.t_all", "format": "sql", "rows": 2,
    "rows_estimate": 2, "rows_check": "match",
    "pages_expected": 1, "pages_actual": 1, "pages_check": "match",
    "file": "/out/public.t_all.sql", "bytes": 3246, "checksum": "md5:…", "elapsed_ms": 171
  } ]
}
```

字段判读：

| 字段 | 取值 | 含义与处置 |
| --- | --- | --- |
| `db_state.control_state` | `DB_SHUTDOWNED`(1) | 干净关闭快照，页级一致性最佳 |
| | `DB_IN_PRODUCTION`(6) | 取自运行中实例，最近提交可能未落盘 → 源库 `CHECKPOINT` 后重取 |
| | `DB_IN_CRASH_RECOVERY`(4) / `DB_IN_ARCHIVE_RECOVERY`(5) | 恢复态，数据可能未到一致点 → 必须回灌校验 |
| `exports[].rows_check` | `match`/`under`/`over` | 实测行数与 `reltuples` 偏差 >10% 时判 under/over；under 多为死元组膨胀，也可能是少导 |
| | `unanalyzed` | `reltuples = -1`（从未 ANALYZE），无估计值可比 |
| | `limited`/`filtered` | 本次用了 `--limit` 或 `--deleted`/`--only-deleted`，行数不可比 |
| `exports[].pages_check` | `short` | **实测页数 < relpages**：段文件缺失/被截断 → 判 `fail`，重取快照 |
| | `nostats` | `relpages = 0` 但有数据（未分析过），仅提示 |
| `summary.bad_blocks_system` | — | 坏块中落在系统模式的部分：金仓系统表私有写机制（页头 item 目录不更新）会被判成坏页，这类只判 `warn`；**用户表坏块才判 `fail`** |
| `unknown_types[].kind` | `fallback` | 该类型落到 decodeDefault（文本透传或 `\x` hex）：字节可逆，文本形态可能与源库不同 |
| | `unresolved` | `pg_type` 中找不到该 oid，DDL 会退化为 `oid` 类型（需人工补类型定义） |

落盘与退出码：`--report` 是开关（不接路径），写 `<输出目录>/report.json`；换路径用 `--report-file PATH`；无 `-o` 时打到 stderr。**退出码只表示"跑完"，不表示"完整"**——`verdict=fail` 仍退出 0，流水线请解析 `verdict.level`。v1.0.53 起多给一个位置参数会直接报错（旧版本会把 `--report` 后面的路径当成数据目录，静默导出 0 表 0 行）；什么都没发现时也不再判 `ok`。

两类"写错参数"在 v1.0.53 起是**硬报错**（退出码非 0，因为这是使用错误而非数据问题）：

- 显式给了 `--schema xxx` 却一张表都没匹配上 → `--schema xxx 未匹配到任何可导出表（检查模式名大小写/是否存在；用 --list-tables-db 查看库内表清单）`。旧版本会打印"成功 0 张表"并退出 0，极易被误读为"这个库是空的"。
- 位置参数给了两个 → 报错并提示改用 `--report-file`。

## 与原生工具（pg_dump / ksql）的差异

### 导出报告

| 维度 | pg_dump 系 | pg2sql `--report` |
| --- | --- | --- |
| 运行前提 | 需要连上活库 | 只要数据目录（关库/停机/取证镜像均可） |
| 一致性依据 | 依赖 MVCC 快照与服务端导出 | 依赖磁盘页状态，因此**必须显式给出快照来源**：`db_state`（关库状态/checkpoint LSN/时间） |
| 完整性校验 | 无（只保证不报错） | `summary` + 逐表 `rows/pages` 对照 + `verdict` 三级结论 |
| 遗漏可见性 | 无（少了表不会说） | `skipped[]` 逐对象给出原因码，`shared_catalog`/`relkind` 等结构性跳过与 `named_missing` 等异常跳过分级 |
| 类型保真度 | 服务端 `*_out` 函数输出 | 离线解码，`unknown_types[]` 明示哪些类型是"字节可逆但文本形态可能不同" |
| 校验和 | 无内建 | `--checksum crc32\|md5` 逐文件，可事后比对/防篡改 |

### 导入语句

| 维度 | pg_dump 输出 | pg2sql 输出 |
| --- | --- | --- |
| 形式 | `COPY ... FROM stdin`（默认，文本/二进制） | `INSERT`（`--sql`，可 `--batch-rows` 合并）/ CSV（`--data`，可 `--header`） |
| 可读性/可编辑性 | COPY 数据块不适合人工审查/裁剪 | 一行一条语句，可 grep/diff/裁剪，便于审计与抽样 |
| 大字段 | COPY 文本转义 | 与 COPY 等价的转义规则，`bytea` 走 `\x` hex |
| 事务/性能 | 单事务 + COPY，最快 | 逐条 INSERT 较慢；`--batch-rows 1000` 批量、`--import-script` 生成脚本、`--parallel` 加速导出 |
| 结构 DDL | 与数据同文件（可 `-s`/`-a` 分离） | DDL 独立 `.ddl`（v1.0.48 起），数据文件纯净；`--full-schema` 另出函数/触发器/视图 |
| 序列状态 | 输出 `setval` | 同样输出 `setval`（`--sql` 且未用 `--fields`） |
| 编码 | 客户端编码协商 | 导出文件恒 UTF-8；**`.sql` 文件首行内嵌 `SET client_encoding = 'UTF8';`**（v1.0.54），`--import-script` 另注入 `PGCLIENTENCODING=UTF8`（v1.2.18 起金仓 mysql 模式还注入 `PGTZ` 钉会话时区，防 `\copy` 静默平移），两者任一即可正确回灌 |
| 回灌顺序 | 工具自行编排 | 分区表自动父→子拓扑排序；**单表导出分区父表时父表 `CREATE TABLE` 一并输出**（v1.0.54），其余按 `import_all.sh` 顺序 |
| 坏块判定 | 只计真损坏页 | VACUUM 空页/全零页不计坏块（v1.0.55 修复「合法空页误报损坏数据块」）；无效路径/fsm/vm 辅助文件硬报错，不再静默导出 0 行 |

## 角色导出（`--roles`，v1.0.53）

离线解析 `global/pg_authid` + `global/pg_auth_members`（金仓为 `sys_authid`/`sys_auth_members`），输出 `roles.ddl`：

```sql
CREATE ROLE "app_rw" WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE INHERIT NOREPLICATION NOBYPASSRLS
  CONNECTION LIMIT 7 PASSWORD 'SCRAM-SHA-256$4096:…' VALID UNTIL '2027-06-30 00:00:00+00';
CREATE ROLE "app_ro" WITH NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS;

-- 角色成员关系
GRANT "app_ro" TO "app_rw" WITH ADMIN OPTION;
GRANT "pg_read_all_data" TO "app_rw";
```

要点：

- **密码只导密文**（SCRAM-SHA-256 / md5 原样），工具不接触明文；合规场景加 `--roles-nopassword` 脱敏
- **内置角色自动跳过**：OID < 16384（initdb 建立，含金仓 `system`/`sso`/`sao`/`sys_*`）或名单内 `pg_*` 组——目标实例自带，重复创建会中断脚本
- **成员关系保留**：授予方是内置角色（如 `GRANT pg_read_all_data TO app_rw`）属真实权限委派，必须保留；被授予方是内置角色则跳过
- `CONNECTION LIMIT` 仅在非默认值（≠ -1）时输出，`0`（禁止连接）会明确输出
- 布局用 `pg_attribute` 动态探测（PG12-18 与金仓 V8/V9 通用），探测失败回退硬编码布局

## 双版本发布形态（v1.0.39 起）

v1.0.39 起发布**免费版**与 **TDE 付费版**两个二进制系列（编译期 `-tags tde` 门控，同一套源码）：

| 维度 | 免费版 `pg2sql` | TDE 版 `pg2sql-tde`（版本号带 `(TDE)`） |
|---|---|---|
| 构建 | `go build .` | `go build -tags tde .` |
| `--enckey` / `--wallet-password` / `--wallet-dir` | 不识别（报错提示 TDE 版） | 完整支持 |
| 二进制内 TDE 代码 | **编译期剔除**（strings 0 命中，无法提取破解） | 含 SM4-ECB/SM3 国密实现 |
| `--version` / `--help` | `pg2sql 1.0.39` / 无 TDE 参数 | `pg2sql 1.0.39 (TDE)` / 含 3 个 TDE 参数 |
| 导出功能 | 与 TDE 版完全一致（不含解密） | 与免费版完全一致（含解密） |

- 发布包命名：免费版 `pg2sql-<版本>-<平台>/`，TDE 版 `pg2sql-<版本>-tde-<平台>/`（解压结构均为 `二进制 + docs/USER_GUIDE.md`）。
- 源码包：免费版源码不含 `tde/` 目录（`go build` 直接可编）；TDE 版源码含 `tde/` 目录（`go build -tags tde`）。
- 两者共用同一回归基线：免费版=常规全版本回归；TDE 版=常规回归 + 金仓 TDE 加密专项（14 实例 28/28 全绿，含明文闭环 md5 校验）。

数据面回归资产：`pgbuild/reg_pg_129_serial.sh`（PG 66 项）、`pgbuild/reg_kb_130.sh`（金仓 25 项）；参数面：`pgbuild/test_params_pg.sh`（20 断言）、`pgbuild/test_params_kb.sh`（26-27 断言）、`pgbuild/reg_params_all.sh`（全版本调度）；大表+表空间专项：`pgbuild/reg_bigts.sh`。

## 更新记录

- **v1.2.19**：**金仓 mysql 模式 csv/txt 导出的时区「可发现性」补强（不改数据格式，导出字节零变化）**。`timestamptz` 与 `timestamp`(oid 7954) 按 v1.2.16 D7 渲染为本地时间、不带偏移，时区只能由会话承载：`--format sql` 的文件头有 `SET TIME ZONE '...'`、`--import-script` 生成的脚本头有 `export PGTZ=<源库时区>`，两条路径均安全；但 `--data --format csv/txt` 且**不使用** `--import-script` 时，数据文件按设计不带会话头、时区无任何载体，手工 `\copy` 会按目标端默认时区解释字面量（+08 → UTC 整体平移 8 小时，`\copy` 不报错）。现于该组合下（mysql 模式 + csv/txt + 无脚本 + 表含 `timestamptz`/`timestamp`）向 stderr **提示一次**，给出当前时区与 `export PGTZ=...` 写法；**不改变导出文件任何字节、不改变退出码**。**为什么改提示而不是改格式**：实测确认金仓 mysql 模式能解析带偏移字面量并按会话时区正确换算（`'2026-03-04 05:06:07+08'` → 存 `21:06:07Z`），但恒输出 `+00` 绝对形态会与 `ksql COPY` 输出不一致（用户无法逐字对照验证），且真 MySQL 的 DATETIME 严格模式会直接拒绝，失去「兼容 mysql 语义」的立身之本；而缺口只在「手工回灌且不设 PGTZ」这一种用手方式，属可发现性问题。手册 6.8 节同步新增「手工回灌注意事项」对照表（三种导出方式 × 时区载体 × 安全性）。新增 9 例单测锁定判定矩阵（含 PG 模式、`--format sql`、`--import-script`、无时区列、Dropped 列、nil 入参六类反向断言）。
- **v1.2.18**：**十五处缺陷收口（首轮 6 项 + 复审 R2 5 项 + 三审 R3 4 项）**。① **`sys`/`pg_` 前缀过宽 → 用户 schema 漏建（P2·空库回灌直接报错）**：`CREATE SCHEMA` 白名单误把任何 `sys*`/`pg_*` 开头的 schema 当系统内置，`sys_audit`、`pg_mine` 这类用户 schema 不再生成建库语句；改为按「任何 PG/金仓实例都内置」的白名单（public/pg_catalog/information_schema/pg_toast/金仓 sys 系列）判定，用户 schema 全部正常输出 `CREATE SCHEMA IF NOT EXISTS`。② **域 DEFAULT 还原失败静默（P3）**：节点树反解失败时登记 `skipped[] reason=domain_default_unresolved`、新增 `summary.domains_unresolved` 计数并判 `fail`，不再零告警。③ **金仓 mysql 模式 `timestamptz` 回灌时区没钉（P2·值静默偏移）**：mysql 模式导出本地时间不带偏移，SQL 会话头现在写 `SET TIME ZONE INTERVAL '+HH:MM' HOUR TO MINUTE`，`--import-script` 生成的三类脚本（sql/csv/import_all.sh）头部追加 `export PGTZ=${PGTZ:-<源库时区>}`（三出口同源：时区未知回退 +08:00）；PG 模式不追加（timestamptz 自带偏移）。④ **纯 `--ddl` 的 `-o` 守卫洞（P3）**：`-o` 指向源数据文件/续段时守卫不拦截、先截断后读，数据清零；DDL 输出路径纳入 `os.SameFile` 守卫。⑤ **typmod 强转按函数名裸匹配（P3·语义改写）**：域 DEFAULT 里调用用户 `myschema.numeric(x,2)` 会被改写成 `CAST(x AS numeric)`；现按函数 OID 反查 schema 限定，且 `procNamespaces` 注入提前到域 DEFAULT 反解之前。⑥ **报告口径与一致性**：域条目不再计入 `tables_skipped`；`--all-tables` 的 `pg_toast` 不再误生成 `CREATE SCHEMA`；`anon`/`src_restrict` 撤出内置名单（避免用户同名 schema 漏建）。全量回归 **PASS=317 FAIL=0**（8 实例 × 大表/分区/全类型/ACL/序列 × 三格式 + TDE 容器内 5 场景）。详见 `V1.2.18_FIX_NOTES.md`。
- **v1.2.17**：**四项实测缺陷收口（1 项 P1 静默改值 + 5 项 P2 静默丢失）**。① **interval「月负+日负」吞 days 负号（P1）**：`-9 mons -2 days +04:30:00` 导出成 `-9 mons 2 days +04:30:00`，值静默偏移 4 天、回灌 `rc=0` 无告警。根因是分量符号条件多写了 `&& !prevNeg`——负分量的 `-` 是它自身的符号，与"是否继承"无关；现改为负值恒带 `-`，仅正值才看继承补 `+`。PG17 14/14 行、金仓 pg/oracle/mysql 三模式与服务端逐字节一致。② **`regrole`/`regconfig`/`regdictionary` 输出裸名（P2）**：`My Role` 应为 `"My Role"`。两处同因——`regQualIdent` 的"名字可见"分支直接返回裸名，`decodeRegrole` 还误以为"PG 不做标识符 quoting"；实际 `reg*out` 全走 `quote_identifier()`，需引号的名字即使在 search_path 内也必须带引号（`english`/`simple` 这类合法小写标识符才不带，所以旧实现在探测样例上"看起来对"）。修复点就是省略 schema 前缀的那一个分支。③ **`CREATE SCHEMA` 缺失（P2）**：跨 schema 对象全部带 schema 限定却没有建 schema 语句，回灌第一句就报 `schema does not exist`；现从类型闭包与 schema 对象收集非内置 schema，在所有 `CREATE DOMAIN`/`CREATE TYPE` 之前统一输出。④ **金仓列 `COLLATE` 丢失（P2）**：根因不在解析层——`pg_attribute` 布局把金仓的 `attinhcount` 当 int2（实为 int4），其后 `attcollation` 偏移 2 字节读成 0。⑤ **域 `DEFAULT` 丢失（P2）**：`pg_type` 尾部三列此前没纳入布局；且尾部形态不能按列号硬编码（PG14+ 有 `typsubscript`，金仓 V8R6 没有，金仓 V9R1C10 还多一个 `pg_attribute` 里看不到的隐藏 4 字节列），现以「`typtypmod` 后按 4 字节步长试候选 + 三个尾属性恰好读到元组末尾」定位，并遵循"1 字节头变长值不对齐"的写入规则；金仓只填 `typdefaultbin` 时由节点树反解兜底。实测 PG17 3/3、V8R6 2/2、V9R1C10 2/2 域 DEFAULT 全部还原。新增 14 例单测（datatype 4 / engine 3 / app 3 / catalog 4）。详见 `V1.2.17_FIX_NOTES.md`。
- **v1.2.16**：**第三方 v1.2.13 回归报告核查后，未修复的 4 项全部收口（D2/D3/D6/D7）**。① **金仓分区子表退化为普通表（D2·P1 静默丢结构）**：`pg_class.relpartbound` 的常量反解只认硬编码的 PG OID（date 1082 / timestamp 1114 …），而金仓兼容模式把内建日期类型换成了自有 OID——oracle 的 `sys.date` 8020、mysql 的 date 7944 / timestamp 7954 / time 7950、V9 datetime 7952、domain 化的 4189 全部落空，`DeparsePartBound` 失败后子分区按普通表输出，只留一行注释：数据行数全对、分区结构消失、verdict 仍是 ok。现按白名单借道已有的 `datatype.Decoders` 解码（**刻意不放开整张解码表**——`reg*` 需要类型名而非值、数组需要 `ARRAY[...]` 语法，误当普通字面量会产出半成品）；同时失败不再静默：登记 `skipped[] reason=partition_bound_unresolved` 并判 `fail`，因为「错误的分区边界比缺失更危险」。② **金仓角色口令与有效期整段丢失（D3·P1 静默丢数据）**：实机扒出三层互相掩盖的问题——(a) `--datadir` 未配 `--db-oid` 时默认 `base/5`，而金仓实测库 OID 从 12741 起，目录根本不存在；(b) 金仓把 `tableoid/cmax/xmax/cmin/xmin/ctid` 登记为**正** attnum 1–6（PG 是负 attnum），旧逻辑按 PG 惯例只剥负 attnum，布局涨到 20 列被硬断言 `l[off+10].AttLen==8` 拒掉；(c) 金仓的 `pg_authid`/`sys_authid` 都是 `relkind='v'` 的**视图**，实体表是 oid=1260 的 `_authid`——按 relname 反查只能拿到 `relfilenode=0` 的视图。现改为「多候选库目录 + **标准 OID 优先** + 按 relname 反查兜底」，用 ctid 恒 6B 作锚点剥离前导系统列，口令/有效期改为**尾部锚定**（倒数第一个 8B 定长列是 `rolvaliduntil`，其前最近的变长列是 `rolpassword`），不再依赖绝对下标；截断判据只按字段数、**不按值判 NULL**（金仓 29 个角色里 28 个有效期本就是 NULL，按值判会误报 28 行 truncated）。③ **oracle 模式空串被写成 NULL（D6·P1 静默丢数据）**：`ora_input_emptystr_isnull` 把 `''` / `''::text` / `''::varbit` 全部吃掉，源库里真实的空串（`length=0` 且 not null）回灌后变成 NULL 且零报错。现改输出非字面量构造 `convert_from('\x'::bytea, 'UTF8')`（`'\x'` 是两字符不触发该规则，bytea 按 hex 解析为空字节串），`bit`/`varbit` 补类型后缀，域类型沿 `typbasetype` 递归；非 oracle 模式原样输出 `''`。④ **mysql 模式时间文本形态不一致（D7·P2 文本比对误判）**：mysql 的 `interval_out` 时间部分**恒定 6 位微秒**（`04:05:06.789000`、零值 `00:00:00.000000`、纯日期分量不追加时间），`timestamptz_out` 按实例时区输出**本地时间且不带 `+00`**；工具此前只有 PG 一套规则，回灌语义对但逐字节比对被判成变更。现按 `kbMysqlMode` 分流（时区沿用 `--tz` > 数据目录 conf `timezone` > 默认 +8 并告警），模式探测不加 GUC——mysql 模式无独有 GUC，改看类型目录是否含 oid 7944 的 `date`。**附带修复（核查中发现，不在报告内）**：纯 `--ddl` 模式此前**不展开子分区**，`--table-name <分区父表> --ddl` 只输出一个空壳父表（`PARTITION BY` 但没有任何分区），回灌后数据无处可去、日志还误报「无子分区」——v1.0.52 当年不展开的理由（展开会丢父表定义）在 v1.0.54 已不成立，现按「父表按分区深度升序 → 叶子」输出整棵树，多叶子时 `-o` 必须是目录（否则拒绝执行以免后一张覆盖前一张）。实机验证：金仓 V9R1C10B4 oracle 3/3 子分区边界完整还原、空串回灌后仍为空串；V8R6C9B14 与 V9R1C10B4 的角色 `PASSWORD`(SCRAM)+`VALID UNTIL`+`CONNECTION LIMIT` 全出且 `trunc_rows=0`；mysql 模式 `interval` 4/4 行与服务端 `COPY ... TO STDOUT` 逐字节一致；PG 17.6 上角色导出与时间文本**逐字节无回归**（反向单测锁死）。新增 12 例单测（objects 3 / nt 3 / datatype 6）。详见 `V1.2.16_FIX_NOTES.md`。

- **v1.2.15**：**regtype / regprocedure 的引号语义对齐 PG `format_type_be`（P1 缺陷）**。旧实现把 PG 的 `quote_identifier()` 简化成"是否小写合法标识符"一条规则，缺了另外两半：**关键字必须加引号**、**多词标准类型名是语法片段不是标识符**。由此产生三类错误：① `varchar` / `float8` / `timestamp` / `timestamptz` / `time` / `timetz` 及其数组被包成 `"character varying"`、`"double precision"`——该写法在 PG 里 `type "character varying" does not exist`，回灌直接失败；② `"char"`(18) 丢引号输出裸 `char`，回灌静默解析成 `bpchar(1)`，类型 OID 变了却不报错；③ `varbit`(1562) 输出 `varbit` 而非 `bit varying`，文本与源库不一致，逐字节比对被误判为变更。现按 PG 17 `format_type_extended()` 重建：新增 **hard-wired 类型名表**（与 PG 的 switch 一一对应，含 `JSONOID → json`——`json` 自 PG16 起是 col_name 关键字，漏收录会被关键字规则误伤），表内值是**已定型的 SQL 片段**，不再二次 quoting；表外名字统一走等价于 `quote_identifier()` 的新实现（合法小写标识符 **且** 非关键字才裸写，`$` 与高位字节一律视为需引号）。关键字集合取自 PG 17.6 `pg_get_keywords()` 中 `catcode != 'U'` 的 164 项，生成为独立源文件。`regprocedure` / `regoperator` 的参数类型同步走同一函数，消除此前"参数走裸名、regtype 走引号"的不一致。另修正两处相关 `format_type` 细节：数组后缀 `[]` 不再让整串被引号包住、`CHAROID` 不入 hard-wired 表（PG 正是靠关键字规则得到 `"char"`）。实机验证：PG 17.6 上 198 个内置类型（`pg_type.oid < 10000`，含全部数组类型）的 `regtype` 输出与 `COPY ... TO STDOUT` **逐字节一致**，跨 schema 参数（`"MyS"."My Type"` / `"MyS"."char"`）的 `regprocedure` 亦一致；关键字表与 hard-wired 表各配单测，共 3 组 41 例。
- **v1.2.14**：**带 typmod 的类型列默认值 DDL 修复（兼容模式全覆盖回归带出的缺陷）**。`varchar(n)` / `bpchar(n)` / `numeric(p,s)` 等带 typmod 的列，默认值为字面量时，PG 在 `adbin` 里存的是**同名强转函数调用**而不是裸常量——`varchar(value, typmod, isExplicit)` 三参、`numeric(value, typmod)` 两参。旧反解器只处理"单参转型"（`len(args)==1` → `CAST(x AS T)`），三参形态落进通用 `name(a, b, c)` 分支，直接输出非法 SQL：`SET DEFAULT varchar('dflt', 14, FALSE)`、`bpchar('ab', 8, FALSE)`、`numeric(1.5, 393222)`，回灌必报语法错、默认值全部丢失。该缺陷在 PG 17.6 原生与金仓 pg / mysql / oracle 三种兼容模式下**均可复现**，与兼容模式无关。现按 typmod 规则还原：`varchar/bpchar` 取 `typmod-4` 为长度、`numeric` 取 `((typmod-4)>>16)` 精度与 `(typmod-4)&0x7ff` 标度（带符号扩展），输出 `CAST('dflt' AS varchar(10))`、`CAST('ab' AS bpchar(4))`、`CAST(1.5 AS numeric(6,2))`；`timestamp`/`interval` 等 typmod 语义各异的类型退化为不带修饰的 CAST；负标度等少见形态同样退化；形态无法确认时维持"人工提示注释"安全网（L3：宁可少输出，不出错误 SQL）。新增 2 组单测（节点树端到端 + typmod 解码表，含"普通三参函数不得被改写"的反向断言），并新增四环境回归 `rg1214_defaults.sh`（PG17 / 金仓 pg / mysql / oracle 各 8 项：输出形状、DDL 回灌、默认值语义）。**已知限制（既有，均走安全网并输出人工提示）**：`interval` 常量默认值尚不能离线还原；金仓 mysql 模式下 `timestamp` 是域类型（如 7954），其常量默认值同样落安全网。
- **v1.2.13**：**九处缺陷收口（外部缺陷清单逐条实机复验后修复）**。① **tid 块号高低位颠倒（P1·静默错值）**：PG 的 `ItemPointerData` 是 `{bi_hi, bi_lo, offset}`，块号语义为 `bi_hi<<16|bi_lo`；旧实现按 uint32 小端直读等价于两半字互换——`(1,2)` 导出成 `(65536,2)`、`(65536,2)` 导出成 `(1,2)`，回灌不报错但指针错位（实测 3 行里 2 行被改写）。② **多级分区 `--ddl` 缺中间层父表（P1·数据全丢）**：`expandLeaf` 只收叶子、`--ddl` 只额外加入被点名的根父表，叶子 `PARTITION OF <中间父表>` 引用不存在的表，回灌必报 `relation ... does not exist`；现收集全部中间层父表并按深度排序（实测 `pt52.orders → two_level → two_leaf` 七份 DDL 全部回灌成功）。③ **单字节编码族统一按 latin1 解码（P1·静默乱码 + 回灌失败）**：pg_enc 9–34（LATIN2–10 / WIN1250–1258 / WIN866 / WIN874 / KOI8R / KOI8U / ISO_8859_5–8）此前全映射 latin1，0x80–0xFF 区除 LATIN1 外全部错误；现按 26 张真实码表解码，未定义码位回退 U+00XX 并告警（实测 WIN1252 库 `€` 由 `C2 80` 修正为 `E2 82 AC`，回灌三行由"两行报错"变为字节级一致）。④ **列头越界静默降级为 NULL（P1·取证红线）**：越界时旧实现只置 nil 并中断剩余列，无日志无计数、报告 `bad_blocks` 仍为 0；现返回降级列号，按表计数并告警（点名首个坏列），`report.json` 新增 `exports[].trunc_rows` 与 `summary.rows_truncated`，判定升为 `fail`。⑤ **`reg*` 系列 OID 整表错位（P2）**：旧映射把 2206 标成 regprocedure、2210 标成 regtype，而 PG 实机为 24=regproc 2202=regprocedure 2203=regoper 2204=regoperator 2205=regclass 2206=regtype 3734=regconfig 3769=regdictionary 4089=regnamespace 4096=regrole 4191=regcollation——解码器全挂在普通列不会出现的空号上，真正的 regtype 列落回裸 OID；现按实测订正，regtype 输出类型名（含 `integer`/`double precision` 等内置标准名与 `text[]` 数组形式）、regnamespace 输出 schema 名，regclass 对 `pg_catalog` 省略前缀；本轮并补齐族内剩余 8 个（regproc/regprocedure/regoper/regoperator/regconfig/regdictionary/regrole/regcollation，pg_proc/pg_operator/pg_ts_config/pg_ts_dict/pg_authid/pg_collation 反查表），输出形态按 PG17.6 实机探针对齐：不可见对象 schema 限定（regproc/regprocedure/regconfig/regdictionary 走 quote_qualified、regoper/regoperator 原文拼接）、regcollation 恒走 quote_identifier（`C`→`"C"`）、regprocedure/regoperator 参数类型按类型自身可见性限定、regoperator 缺失参数 PG16+ 输出 `NONE` 形态（PG12-15/金仓旧形态）、regrole 输出 rolname 原文；探针表导出与 PG 基准逐列一致、回灌 diff 一致（注意 pg_ts_dict 的 relfilenode 是 3600，3766 是 pg_ts_template 的索引）。⑥ **money 最小值取负溢出（P2）**：`INT64_MIN`（`-$92,233,720,368,547,758.08`，PG 值域下界、合法可达）取负后仍为负，导出 `-$-92,...--8` 非法字面量导致整行回灌失败；改用无符号绝对值。⑦ **interval 符号规则（P3·格式）**：PG 按「逐分量继承前一分量符号」输出——与前一分量同号省略、异号显式标出，零分量不重置继承，负时间恒带 `-`（`-1 days +02:03:04`、`-1 mons +2 days 03:00:00`、`-1 days -02:03:04`）；旧版只按时间自身正负决定 `-`，且 `-1` 误用单数。⑧ **IPv6 无零压缩（P3·格式）**：实现 RFC 5952（`::1`、`2001:db8::/64`）与 IPv4-mapped（`::ffff:192.168.1.1`）。⑨ **分区父表 `--count` 无归属（P3）**：逐叶统计每行输出 `-- 分区[表名]: N`，末尾追加 `-- 总行数: N（父表 X，M 个分区）`；逐叶行刻意不占用 `总行数:` 关键词，保证既有 `grep '总行数: *[0-9]*'` 求和脚本仍得到正确总数。新增 20 项单测（第一轮 12 + reg* 剩余族 8）；9 项及 reg* 补齐均在 PG 17.6 实机完成"导出 → 回灌 → 逐列/逐字节比对"闭环。
- **v1.2.12**：**四处缺陷收口（第二轮深度代码审查结论）**。① **`-o` 指向多段表的续段文件时输出守卫放行（中·后果重：源库不可逆损坏）**：`guardOutputAgainstSource` 只用主段（数据文件/TOAST/`--data-file`）做 `os.SameFile` 比对，而 >1GB 的表存在 `16391.1`、`16391.2` 等续段——`-o 16391.1` 时比对不到、守卫返回 nil，紧接着的 `os.Create` 先截断源续段再回去读它，源数据段清零且导出 0 行。现对每个源路径展开 `engine.SegFiles`（`sync.Map` 缓存，零额外 IO）一并比对，拒绝信息会指明命中的续段文件。② **数组头部损坏静默导出 `'{}'`（中低·静默丢数据）**：`DecodeArray` 对 `ndim > 6`（PG 上限就是 6）与 `dims[i] <= 0`（合法维长恒 > 0）直接 `return "{}"`，与"合法空数组"输出完全相同且不计降级——撕裂页里的数组被导出成空数组，回灌成功、目标库拿到空值、报告告警全 0；同函数其它损坏分支（头部越界、元素总数溢出）一直走 `DecodeDefault`。现两处改走 `DecodeDefault`（`\x` 原文 + 收尾告警），`ndim = 0` 的合法空数组保持输出 `{}` 且不计数。③ **`--fields` 与 `--no-complete-insert` 组合产出列数不符的 INSERT（低）**：列清单只由 `--complete-insert` 决定、与 `--fields` 裁剪无关，`--fields a,b --no-complete-insert` 对 3 列表产出只有 2 个值且无列名的 INSERT，回灌必然报错而工具零提示；现于解析期拦下并给出改法（csv/text 不受影响）。④ **全表失败仍生成空的 `import_all.sh`（极低）**：守卫写作 `len(lines) <= 11`，实际头部为 12 行，条件恒假——所有表都失败时仍写出一个只建库、无任何数据步骤的脚本，用户执行后会误以为导入成功。现改为按实际头部行数判断。新增 13 项单测（app 7 + datatype 3 + cli 4，含全部四项的回退验证）。
- **v1.2.11**：**两处缺陷收口（v1.2.10 深度代码审查结论）**。① **`--batch-rows` 与审计模式组合产出非法 SQL（高·数据正确性）**：多值 INSERT 的批组打断只识别 `-- DELETED` 前缀，v1.2.2 新增的 `-- SUSPECTED_DELETED`（hint 缺失的疑似删除行）不在白名单内，被当作普通 VALUES 元组拼进批——输出变成 `INSERT ... VALUES -- SUSPECTED_DELETED ctid=(0,1)`，且上一条语句的 `;` 落在值列表中段，**整文件无法被 psql 加载而退出码仍为 0、零告警**。触发条件为 `--batch-rows N>1` + `--deleted`/`--only-deleted` + 表中存在 hint 缺失的疑似删除行（取证拷贝、`pg_repack`、块级备份后较常见）。现改按 `-- ` 注释前缀统一识别，后续再加新标记不会复发。② **定长解码器短输入越界 panic（中·健壮性）**：23 个定长解码器（int8/int2/int4/oid/tid/xid/cid/float4/float8/money/date/time/timestamp/timestamptz/interval/timetz，以及金仓 datetime / V9 date / mysql time·timestamp / oracle DATE / ora_date）对 `len < 类型宽度` 的输入直接越界崩溃（空输入即 `index out of range [7] with length 0`）。主链路本有 `offset+AttLen > nraw` 守卫，但该守卫信任**目录宽度**——跨版本 `--catalog`、目录与磁盘不匹配、损坏页均可穿透。现于 `DecodeValue` 入口按新增的 `minDecoderWidth` 预检，长度不足则按 `\x` 原文输出并计入导出收尾告警（**不静默丢值**）；宽度表由单测双向断言（宽度-1 必 panic、宽度必不 panic），后续新增定长解码器漏登记会直接测试失败。附带修复：分区父表多叶子 + 纯 `--count` 路径此前是全代码库**唯一没有 panic 隔离**的导出调用点（坏页会终止整个进程、无表名定位），改走 `safeExportOneTable`；DDL 类型依赖闭包的 `seen` 改为只在真正发射语句后置位（防止将来的"先失败后可用"目录状态静默缺类型）。新增 9 项单测（engine 5 + datatype 4）。

- **v1.2.10**：**DDL 类型依赖闭包**（外部审计报告第十节 #1）。`--table-name --ddl` 与批量 `--schema --ddl` 的三个前置类型段（枚举/域/复合）此前只看**列的直接类型**：同一自定义类型只要藏在下层——域链 ≥2 层（只输出顶层 `CREATE DOMAIN c9 AS c8`，缺 c1..c8）、域 over 复合/枚举（缺基复合/基枚举）、复合字段引用自定义类型——空库回灌即报 `ERROR: type "public.c8" does not exist`，**连 CREATE TABLE 都建不出**。现改为依赖闭包：域递归 `typbasetype`（base 为数组则转到元素）、复合递归每个字段、枚举为终点，被依赖者先输出；三桶顺序与桶内列序不变，**旧场景输出逐字节一致**（13 张既有表对照：10 张逐字节相同，3 张差异全部是补齐旧版缺失的依赖）。附带修正：域基为自定义数组时 `CREATE DOMAIN ... AS` 输出 `"comp_t"[]` 而非 pg_type 内部名 `_comp_t`。**数据面同族缺口一并修复**（实测发现）：域 over 枚举（`create domain d as mood`）此前整列退化 `\x` 十六进制——枚举磁盘是 4 字节成员 OID，按列 oid 查 EnumMap 必然 miss，现沿域链解析（`enumBaseOID`，上限 32 跳超限记账告警）；域 over 自定义数组同样整列 `\x`——数组解码器注册在域递归**之后**导致 `resolveDecoder` 恒 miss，现已调整注册顺序。端到端验证（PG17.6 五种形态 + 金仓 V9R1C10B4）：DDL 建表 + 数据回灌全部 0 错误，`to_hex`/文本逐字节一致（含域 over 复合数组的 0xC3 高位字节经 `ARRAY[ROW((-61)::"char",…)]` 还原）。新增 8 项单测。

- **v1.2.9**：**char 容器实现的三处收尾（B-1 / B-2 / B-3，均为 v1.2.8 留下的边界问题）**。① **降级告警指向不存在的格式（B-1，P2）**：v1.2.8 在"SQL 格式无法表达该字节"时提示用户「改用 `--format binary`」，而该格式**从未实现**（`--format` 只接受 `sql/csv/text`，照抄提示直接 `exit 1`），后半句「按 DDL 单独处理」也没有可执行落点（问题在数据行、不在 DDL，DDL 重放只会建出一模一样的表、仍然插不进这一行）。现改为两条真实可行路径：目标库改用单字节编码（latin1 / SQL_ASCII）回灌，或导出后人工两步改写（先插入同下界的 ASCII 占位值，再 `UPDATE t SET col[lb] = (N)::"char"`；复合按 `ROW(...)::type` 重写），并指向用户手册「"char" 高位字节」一节；触发原因也补全（原文案只写"自定义下界/类型名不可解析"，实际还有"数组或复合结构不规则""文本语法解析失败"两种）。② **类型链上限导致的静默漏改写（B-3，P2）**：`resolveCharBase`（域链）与 `compositeBaseOID`（域 over 复合）各写死 8 跳、容器嵌套写死 4 层，且到顶即"按当前层返回、不记账"——9 层域链会停在仍是域的中间层、被判为"不含 char 容器"，于是 **SQL 不改写、CSV/text 也不告警**，裸字节原样落盘（PG17 实测 9/10 层域：`'Ã'` 原样输出、零告警，回灌 `invalid byte sequence ... 0xc3 0x27` 只成功 1 行）。现三处魔数收敛为具名常量（`maxTypeChainHops = 32`、`maxContainerDepth = 8`），**超限不再静默**：新增 `RecordTypeChainUnresolved` 按 oid 去重记账，导出收尾 `TypeChainUnresolvedSummary` 单列告警并点名 oid 与原因（域链 / 容器嵌套），让"判不了"与"确实没有"在输出上可区分。实测：9/10 层域链正确输出 `(-61)::"char"`，回灌 `to_hex` = `ffffffc3`；9 层 `domain over 复合` 从整列 `\x` 十六进制恢复为 `ROW((-61)::"char",'7')::"类型"`；40 层域链输出超限告警（1.2.8 同场景零告警）。③ **不规则数组的防御性不对称（B-2，P3）**：`arrDims` 判为形状不规则时 SQL 侧静默返回、不计降级，而同一段文本在 CSV/text 侧会照常计数告警。现补 1 次降级记账，两侧口径一致；该分支正常导出不可达（PG 存储层数组恒规整），只在解析器与生成器漂移、或文本被外部篡改时才可能走到。**输出内容零变化**：除上述边界场景外，普通值、既有 52 项 char 专项断言全部逐字节不变。新增 6 项单测、21 项专项验证（含 1.2.8 对照）。
- **v1.2.8**：**`"char"`(oid 18) 容器载体的字节还原补齐**（v1.2.6 只修了标量列）。v1.2.6 的 `(B-256)::"char"` 强转判定写死 `col.TypeOID == 18`，同一个高危字节只要装进**数组元素**(1002 / 自定义 char 数组)、**域基类型**(`domain over "char"`)、**复合类型字段**(record 内) 就失效：三者都退化成普通文本字面量，裸 0xC3/0x00 在服务端 escape 层被 mb 校验拒绝（`invalid byte sequence for encoding "UTF8": 0xc3 0x2c`）→ **整表 0 行入库**，且导出侧毫无告警。现新增 `datatype.CharContainerExpr`：按类型自身的文本语法（array_in / record_out）重新解析已解码文本，只把含高危字节的**叶子**换成 `(B-256)::"char"`，整体重建成 `ARRAY[...]` / `ROW(...)::type` 表达式；无高危字节时输出与 v1.2.7 **逐字节一致**（回归锚点不动）。已实测覆盖：一维/二维数组、NULL 元素、复合字段、嵌套复合、复合数组（`ct[]`）、域 over 数组、域 over 复合、元素为域的数组（`d1[]`），回灌后 `to_hex` 逐字节一致，二次导出不再出现 `'Ã'` 漂移。**不可表达场景**：自定义下界数组（`'[0:2]={…}'`）——PG 无 `array_set`（PG17 实测不存在）、`array_fill` 只能填同一值，只能降级为原样输出并**计数告警**；CSV/text 格式无表达式能力，告警范围同步从"标量列"扩到"标量列/数组元素/域/复合字段"。附带修掉一个**静默**缺口：`domain over 复合类型`此前没有解码器，整列退化为 `\x` 十六进制（v1.2.7 实测 oid 46879 全部单元格如此，回灌必失败），现沿 `typbasetype` 解析到基复合类型后正常解码。另为每行每列的容器判定加无锁 COW 缓存，百万行多列大表导出耗时与 v1.2.7 持平（84.9s→82.5s，原朴素实现慢 3.5%）。新增 9 项单测、52 项专项验证。
- **v1.2.7**：**DDL 类型名与编码判定两处缺陷修复**（前者会造成不可逆的数据损坏）。① **DDL 对 `"char"`(oid 18) 丢失引号（F1，P1）**：列/数组/域基类型/函数签名一律输出裸 `char`，而 SQL 里裸 `char` 是 `character(1)`(oid 1042) 的别名——目标表被静默建成 `character(1)`，导入**零报错、行数正确、退出码 0**。后果比"回灌失败"更重：`(-61)::"char"` 插入 bpchar 列不报错（`char_bpchar` 直接搬字节、绕过 mb 校验），0xC3/0xFF 永久落库，此后该表每读必报 `invalid byte sequence`；再次导出时 `0xC3` 被当 Latin-1 字符 `'Ã'` 二次编码成 `C3 83`，**每迁移一次扩一层，数据永久不可还原**；纯 ASCII 则发生 `c::int` 由 65 变 `invalid input syntax` 的语义漂移。现按 **oid 特判**输出 `"char"` / `"char"[]`（`datatype.CharTypeSQL` 单一出口，覆盖 `typeDisplayName`/`colTypeSQL`/`procScalarTypeName` 四条路径），并实测 PG17、金仓 V8R6 oracle 模式、V8R6/V9R1C10 mysql 模式下带引号建表均落到 oid 18/1002。**绝不按类型名判断**——1042 的 `character(n)` 必须保持裸名，ora 模式的 `char` 不受影响。② **编码误检（F2）**：表级精化原为"单票即覆盖"，而加密表页内容近似随机、压缩页、单页小表都可能凭一页噪声命中 GBK 特征，把整表 UTF-8 中文按 GBK 解码——文件仍是合法 UTF-8，导入**零报错**，`中国` 变 `涓鍥?`（码点 U+4E2D→U+6D93）并混入 U+00AD 不可见字符，只有 md5 比对才能发现。现：**加密表跳过表级精化**（`engine.SuspectCipherTable`，≥50% 采样页可疑才判定，避免误跳普通表）；**覆盖门槛收紧为 ≥2 票且胜出占比 ≥2/3**，单票只输出"证据不足未采用"警告；**库级采样改分层**（最大 2 个 + 其余等距 3 个，共 ≤5）并跳过密文文件，消除 re-initdb 后排序变化导致的判定翻转；覆盖生效时由"提示"升为"**警告**"（它改变导出内容）。新增 13 项单测。
- **v1.2.6**：**两处静默失败修复**（均边缘触发、一旦触发必失败）。① **`"char"`(oid 18) 高位字节**：`"char"` 列存 ≥0x80 字节（如 UTF-8 库中 'É' 经 charin 存为孤立 0xC3）时，原实现三条格式均原样输出该字节，回灌 UTF-8 等多字节编码库被 mb 校验拒绝（`invalid byte sequence`）且无告警。现 **SQL 路径输出 `(B-256)::"char"` 负数 int 强转可精确还原**（如 `(-61)::"char"`→0xC3；i4tochar 直接截断低字节、不做 mb 校验。实测：`E'\xC3'` 文本形态在服务端 escape 层即被 mb 校验拒绝，连显式 `::"char"` cast 也拦在 charin 之前，唯一可行形态是 int 强转）；CSV/text 格式无法转义该字节，导出收尾输出计数告警提示受影响行数。端到端实测：PG17 建表含 0xC3/0x80/0xFF/0x00 四种字节，导出→回灌→`to_hex` 比对全部一致。② **`--encoding` 白名单校验**：未知名（如老文档下划线写法 `euc_cn`、拼写错误 `win1252`）原实现静默落入 Latin-1 逐字节兜底、中文全乱码且无告警。现解析期显式报错并列出全部支持名；同时补齐下划线别名归一（`euc_cn`/`euc_jp`/`euc_kr`/`euc_tw`/`euc_jis_2004`/`shift_jis_2004`/`mule_internal` 均可正确识别），`DecodeBytes` 分派前先归一化兜底。新增 9 项单测。
- **v1.2.5**：**数据格式参数收口**。新增 `--format sql|csv|text` 值参数（推荐写法，等价旧 `--sql`/`--data`/`--text`，只能指定一次、不与旧旗标混用）；`--sql`/`--data`/`--text` 三旗标改为严格互斥（任两个同现报错退出）——修复 v1.2.4 及之前 `--sql --data` 同现时 `--data` 静默优先、`--sql` 被无声吞掉的隐患；`--header` 收紧为仅 csv 格式可用（原 `--sql --header` 静默忽略，现统一报错）。不导数据仍可只用 `--ddl`（纯 DDL 模式不变）。归一化全部在解析期完成，下游导出逻辑零改动，三种格式产物与 v1.2.4 逐字节一致。新增 9 项参数单测。
- **v1.2.4**：**新增 `\x` 退化静默兜底告警**。BUG-13 同类风险扫描（见 `BUG13_SIMILAR_RISK_SCAN.md`）的核心发现：漏注册解码器后 `DecodeDefault` 静默输出 `\x` 十六进制，不告警、退出码 0，用户回灌才失败——**静默才是真正的风险**。现 `DecodeValue` 出口统一拦截：结果以 `\x` 开头且非 `bytea`（byteaout 本就以 `\x` 为合法格式）即记录类型 oid 与单元格数，导出收尾汇总打印提示（含 oid 与出现次数），同类缺口从静默变成长鸣。另为 `decodeInt1` 补注释（金仓 V9 mysql 模式 TINYINT 经 `inputFnDecoders["tinyintin"]` 动态引用，曾险些被误删——判定死代码前必须穷尽全部注册点）。新增 6 项单测锁定整条链路。
- **v1.2.3**：**两处数据类型解码缺陷修复**。本轮针对"导出数据是否正常"的专项核查中发现的两个真实缺陷：
  - **BUG-13（P1，静默数据损坏）`aclitem` 标量列导出退化为 `\x` 十六进制**：`decodeAclitem` 早已实现（v1.0.66 的 B12 还按引擎区分 ACL 位 12 的 `'p'`/`'s'` 字符），并通过数组元素路径服务于 `aclitem[]`；但标量解码分派表 `Decoders` **从未注册 `ACLITEMOID`(1033)** 条目，标量 `aclitem` 列落到兜底 `DecodeDefault` 直接输出 `\x` + 十六进制（实机 V8R6C9B14 / V9R1C10 均为 `\x0a0000000a0000007f100000`）。回灌时报 `invalid input syntax for type aclitem`，**必失败**。症状极具迷惑性：同一张表里 `al aclitem[]` 输出正常、`a aclitem` 却是乱码。修复：`Decoders` 增加 `ACLITEMOID: decodeAclitem`。此前未被发现的原因是 `v1065_aclitem_test.go` 的 10 余个用例全部直接调用 `decodeAclitem()`，从不经过 `DecodeValue()` 分派层——函数本身一直正确，测试因此全绿。
  - **BUG-14（P1，跨库回灌静默错指）`regclass` 列导出丢失 schema 前缀**：`decodeRegclass` 只输出 `relname`，且 `buildRegclassNameMap` 只采集 `oid → relname`、未采集 `relnamespace`。当库内存在跨 schema 的同名表时，指向非默认 schema 的 `regclass` 值会被静默降级为裸名。实测：`public.dup_t` 与 `sc2.dup_t` 并存，某行 `rc` 原指 `sc2.dup_t`(oid 45830)，导出为 `dup_t`，回灌后解析到 `public.dup_t`(oid 45823)——**无任何报错、无任何告警**，是最危险的静默语义漂移。修复：同时采集 `oid → relnamespace` 与 `pg_namespace` 的 `oid → nspname`，表所在 schema 与默认 schema（离线导出取 `public`，即 PG 默认 `search_path` 的落点）不同时输出 `nspname.relname`，与 PG `regclassout` 行为对齐；namespace 信息缺失时退化为裸名（保持旧行为，不因扫描失败而报错）。实机验证：导出由 `dup_t` 变为 `sc2.dup_t`，回灌后 `orig_oid=45830 rt_oid=45830 same=true`。
  - 新增单元测试：`internal/datatype/v122_aclitem_scalar_test.go`（4 项，走 `DecodeValue()` 入口断言标量 aclitem 非十六进制、PG/金仓位 12 字符、标量与数组一致、GRANT OPTION）、`internal/datatype/v123_regclass_schema_test.go`（6 项，同 schema 省略前缀 / 跨 schema 加前缀 / 无 namespace 信息降级 / 特殊标识符双引号 / 默认 schema 置空的保守行为 / InvalidOid 保持 `-`）。
  - 另复核确认（非缺陷，仅记录）：`--text` 对 `timestamptz` 固定 UTC 渲染、对 IPv6 不做 `::` 零压缩，属呈现约定差异，语义完全等价——探针表回灌后四列全等，服务端渲染 md5 逐字节相同。金仓 V8R6C9B14 的 `aclitem` 回灌报 `invalid mode character` 经逐字符实测确认是**引擎自身缺陷**（`aclitemout` 输出 `'p'`、`aclitem_in` 却无法解析 `'p'`，连自身输出都无法重新导入），非本工具问题；V9 系列可正常回灌。

- **v1.2.2**：**全版本全功能回归 + 两轮代码审计，修复 10 条缺陷（含 2 条静默丢行 P0/P1）**。本轮不引入新功能，专注正确性与可观测性：
  - **BUG-10（P1，审计标记不一致）`--deleted` 与 `--only-deleted` 对同一物理行标记不同**：两个模式都走审计路径，但对"疑似删除"（`xmax != 0` 且 hint 缺失）的行，`--deleted` 不打标记而 `--only-deleted` 打 `-- DELETED`。下游若按标记做幂等/去重，同一行在两种模式下会被判成不同的东西。现统一规则：审计模式下"确证删除"与"疑似删除"都打标记，疑似者标记为 `-- SUSPECTED_DELETED`（区别于确证的 `-- DELETED`），默认模式（非审计）一律不打标记。`Row` 结构新增 `Suspect` 字段承载该区分。
  - **BUG-1（P2，自检文案失真）CLOG 结果缓存命中不计数**：`Status` 命中 `resCache` 时直接返回、不累加 `Queries`/分类计数，而"同一事务覆盖成千上万元组"是常态，缓存命中率 >99% → 自检输出"CLOG 复核：查询 N 次"严重低估，用户会以为 `--clog` 没生效。现改为命中缓存同样计入。
  - **BUG-2（P2，判定能力损失）CLOG 段文件读取只调一次 `Read`**：Go 的 `Read` 不保证读满，短读后 `buf` 被截成 `buf[:n]` 缓存，同段中落在 `n` 之后的 xid 会被误判为"段缺失"→ 本可判定的元组归入"不可判"。改为 `io.ReadFull` 循环读满，保留可用前缀。
  - **BUG-3（P3，错误强结论）CLOG 不校验 xid 与 nextXid 的水位关系**：当元组头 `xmin/xmax` 是垃圾值或指向"本实例尚未分配"的未来 xid 时，仍按 `segno/offset` 去 CLOG 取 2 bit，读到邻接事务的残留值并被当成 `COMMITTED`/`ABORTED` 的**强结论**（实测 `xid=200`、`nextXid=150` 时返回 `ABORTED`），直接把行判死。现 `ClogReader` 新增 `NextXid` 字段（构造时从 `pg_control`/`sys_control` offset 64 自动加载），`xid >= nextXid` 一律返回"不可判"并计入 `Unknown`。控制文件读不到时 `NextXid=0`，退回旧的"不校验"行为，不因缺文件而整体降级。
  - **BUG-4（P2，契约风险）`DecryptPage` 原地修改入参但契约未声明**：`heapfile.go` 有 8 处写成 `if _, derr := DecryptPage(buf); derr != nil`（丢弃返回值），依赖"入参被原地覆写"这一隐含事实。若将来有人把实现改成"返回新副本"，这些调用点会静默解不出数据。现于函数注释明确契约，并新增测试锁定该行为。
  - **BUG-5（P2，静默错误）短页解密返回"成功"**：`DecryptPage` 对 `len(buf) < 80` 直接 `return buf, nil`——"成功但没解密"。调用方拿到 `nil` 就认为该页已解密，把仍是密文的字节当明文解析，产出垃圾行或静默 0 行，且不计入 `bad_pages`。现返回哨兵错误 `ErrPageTooSmall`，交由调用方按坏页处理。连带修复 `VerifyDecryptorKey`：旧实现拿到解密错误就 `continue` 且不计数，若密文页全部无法解密则 `checkedPages`/`itemids` 恒为 0，函数一路 `return nil` 放行——等于"一页都没解开也算密钥正确"。现单独统计 `decryptErrs`，`checkedPages == 0 && decryptErrs > 0` 时明确报错（空表/全删表的所有页是 `ZeroPage`，循环内已跳过，不会误伤）。
  - **BUG-6（P2，错误密钥静默放行）密钥校验只扫每段前 32 页**：终判据为 `itemids > 0 && validTuples == 0`，若前 32 页恰好全是空页/全零页（大表 `DELETE` 后、稀疏表），`itemids` 恒为 0 → 判据不成立 → 直接放行，随后导出满屏乱码且无报错。现改为跨所有段累计扫描（单段 32 页、总量上限 4096 页，保持启动开销恒定），并在"扫到页但无任何行指针"时返回 `ErrKeyUnverified`，由调用方降级为告警而非中止（空表是合法场景）。
  - **BUG-7（P2，文案误导）密文告警把"解出的乱码行数"说成"表行数"**：原文案"表 X 有 N 行未能解密"会让人以为 N 是真实行数，而 N 恰好是无密钥时按明文解析出来的垃圾行数。现改为"有 M/K 个数据页呈密文特征…已解析出的 N 行均为乱码、不可信（真实行数需解密后确认）"。
  - **BUG-8（P3，报错文案）表空间路径写死 `pg_tblspc`**：金仓实际为 `sys_tblspc`，用户按提示去 `pg_tblspc` 下找不到目录。文案改为同时提及两者。
  - **BUG-9（P3，告警噪音）hint 缺失告警把正常现象说成故障**：刚建表/刚写入、尚未被查询过的表，hint 未回填属正常（`checkpoint` 前可高达 100%），旧文案直接报"约 100.0% 缺 hint"并列举崩溃恢复等异常成因，容易误判。现于文案中明确"该表在写入后尚未被查询过（hint 未回填，属正常现象）"。
  - 另修复 `ClogKilledTuples` 计数器死代码：判据为 `tup.IsLiveCLog(nil, false)` 为真才累加，但该函数开头同样命中 `isInsertAborted()` 返回 `false`，两条路径互斥 → 计数器恒为 0。现改为"确认非活且非删除、且 CLOG 可用、且非回滚插入"时计数。
  - 本轮验证：**8 实例 × 7 表 × 3 格式 = 168 项主回归**（`PASS=160`，8 个 FAIL 经查全为测试脚本缺陷——`grep -c` 无匹配时输出 `0` 且退出码 1，配 `|| echo 0` 得到 `0\n0`；改 `awk` 计数后空表专项 40/40 全绿）、**参数面 14 段 54 断言**（唯一 FAIL 为 v1.2.1 既定设计）、**B19 专项 4 个金仓实例严格复现**（建表 → `delete+rollback` → `checkpoint` → 全程不 `SELECT`，避免 hint 回填掩盖缺陷）全命中、**TDE 加密 7 场景全通过**（表加密无密钥告警 / 正确密钥 200 行 / 错误密钥拒绝 / 表空间无钱包告警 / `--wallet-dir` 钱包链 50 行 / `--wallet-password` 校验失败但继续 / 三格式回灌）。并发一致性：并行度 1/2/4/8 行数一致、SQL 输出 md5 逐字节相同。`gofmt` 无差异、`go vet` 干净、双 tag `go test -race` 全绿。
  - 新增单元测试：`internal/engine/v122_audit_mark_test.go`（4 项，审计标记一致性）、`internal/engine/v122_clog_watermark_test.go`（4 项，CLOG 水位校验/降级/统计/金仓 `sys_control`）、`tde/v122_page_decrypt_test.go`（3 项，短页报错/正常页回环/原地改写契约）。
- **v1.2.0**：**新增 `--clog` 离线复核元组可见性**——解决「hint bit 缺失导致回滚残留行被当活行导出」：
  - 背景：元组的可见性依据 `t_infomask` 里的 hint bit（`HEAP_XMIN_COMMITTED` / `HEAP_XMIN_INVALID` 等），而 hint bit 只是**缓存、不是权威状态**。PG 在正常回滚路径里会主动回填 hint（实测回滚 INSERT 的元组 `t_infomask=0x0A02` 已含 `HEAP_XMIN_INVALID`，v1.1.x 靠 hint 就能导出正确行数），但**崩溃恢复中断的事务、物理拷贝/裸设备导出的堆文件、被第三方工具复制过的文件**可能从未被写过 hint，此时 v1.1.x 会把回滚残留行静默导出（受控实验：150 物理元组 / 100 活行，hint 被剥除后导出 150 行，**多导 33%**）。
  - 新增 `--clog`：对 **hint 缺失**的元组额外读 `<PGDATA>/pg_xact/XXXX`（金仓 `sys_xact`，自动识别；`global/pg_control` / `sys_control` 偏移 64 读 nextXid）。每个事务占 2 bit，`1=COMMITTED 2=ABORTED 0=IN_PROGRESS`。hint 完整的元组**完全不查 CLOG**，常规库行为零变化。
  - 安全边界：读到 `IN_PROGRESS`（CLOG 延迟落盘的正常现象）一律按**活行**导出并计数告警，绝不据此判死——宁可多导不丢行。CLOG 段文件缺失同báo「不可判」。
  - 新增 hint 缺失自检：不开启 `--clog` 时，若 hint 缺失元组占比 ≥10%，自动告警并提示加 `--clog --datadir <PGDATA>` 复核。开启后输出复核摘要（查询次数 / 已提交 / 已回滚 / 不可判 / 剔除行数）。
  - 性能（实测 PG17 百万行 `rg.big`）：hint 完整时 CLOG 查询 **0 次**，耗时与 v1.1.2 持平（44.3s vs 44.9s）；最坏情况（100 万元组 hint 全剥除）因同一事务缓存命中仅查 **1 次**，耗时 44.0s，无可见开销。
  - 兼容性（实测）：PG 17.6、金仓 V8R6C9B14（`sys_xact`/`sys_control`，nextXid=3210）、金仓 kbpg（nextXid=2133）均正确识别；不开 `--clog` 时 4 张表 × 3 格式（`--sql`/`--data --header`/`--text`）与 v1.1.2 逐字节一致，`--count`/`--deleted`/`--only-deleted` 一致，百万行 146MB `--text` 输出 md5 三方相同，分区表展开 3 叶子一致。
  - 新增单元测试 `internal/engine/v120_clog_test.go`（9 项）：2-bit 槽位定位与状态码、金仓 `sys_xact` 识别、目录缺失降级、`pg_control`/`sys_control` 偏移 64、回滚插入判不可见、回滚删除判活、hint 完整时 CLOG 不改结论、IN_PROGRESS 保守判活、剔除行数统计。
  - 可行性验证报告见 `CLOG_FEASIBILITY_REPORT.md`（含 5 个原型脚本与堆页字段偏移修正记录）。

- **v1.1.2**：**全类型表回归暴露并修复 1 条 P1 静默数据损坏（B18）**——`json[]` 数组元素被当原始二进制输出：
  - **B18（P1，静默数据损坏 + 回灌失败）`json[]` / 金仓 mysql 模式 `json[]` 数组元素未注册解码器**：`arrayElemInfo` 原先只有 `JSONBOID(3802)`，缺 `JSONOID(114)` 与金仓 mysql 模式 `json(7024)`。PG 及金仓 PG 模式 `json[]` 的 elemtype 是 114；金仓 V9 mysql 模式 `json` 是 domain of 4802 `mysql_json`（**底层按 jsonb 二进制存储**），`json[]` 的 elemtype 是 7024。两种 OID 都落到"未知元素类型 → varlena 文本退化"分支，元素**原始容器字节被当 UTF-8 字符串**输出：实测金仓 V9R1C10B4(mysql) `rg.alltypes.c_jsonarr`（源值 `{"{\"a\": 1}"}`）导出成 `{"<27 字节容器原文，含 0x00>"}`，同实例服务端 `COPY ... FORMAT text` 输出的是正确的 `{"{\"a\": 1}"}`（**工具 1278B/14 个 NUL vs 服务端 1264B/0 个 NUL**，同实例同行可比，非呈现差异）。三格式回灌全部报 `invalid byte sequence for encoding "UTF8": 0x00`。现为 114 注册 `json`（剥头后 UTF-8 文本）、7024 注册 `jsonb_p`（剥头后 jsonb 容器解码）。
  - 影响面：**仅 `json[]` 列**（标量 `json`/`jsonb` 一直正确）。含 `json[]` 的表导出后无法回灌；不含该类型的表不受影响——这也是此前 5 实例特殊字符专项全绿、百万行/分区表回归全绿却漏掉它的原因（探针表 `sc_types` 只覆盖标量 `json`/`jsonb`）。
  - 本轮回归（v1.1.2 二进制）：**5 实例全类型表 `rg.alltypes` 三格式（`--text`/`--data`/`--sql`）回灌全部一致（FAIL=0）**；4 实例分区表逐叶子与服务端 `COPY ... FORMAT text` 逐字节一致（`pr_range` 3/3、`pr_list` 3/3、`pr_hash` 4/4，共 10 个叶子/实例）；特殊字符专项复测全绿。双 tag（`go test ./...` 与 `go test -tags tde ./...`）全通过，`gofmt` 无差异。
  - 新增单元测试 `internal/datatype/v112_json_array_test.go`（3 项）：元素 OID 注册、文本型 `json[]` 逐字节、mysql 二进制型 `json[]` 不再泄漏控制字节。

- **v1.1.1**：**新增 `--text`（PostgreSQL COPY text 格式）**——数据导出第三种格式，产物与 `COPY ... FORMAT text` **逐字节一致**；同时修掉被字节比对暴露出来的两条静默数据损坏缺陷（B13/B14）：
  - **新功能 `--text`（大改）**：制表符分隔、反斜杠转义、`\N` 表 NULL、无表头行，后缀 `.txt`。实测转义规则与 PG17.6 / 金仓 V8R6C9B14 服务端完全一致——仅转义 `\b \t \n \v \f \r` 与反斜杠共 7 种，**其余控制字符（0x01~0x07、0x0E~0x1F）原样输出**（不是八进制转义，按文档直觉实现会与服务端不同）。NULL 与空串天然可区分（`\N` vs 空字段），字面量 `\N` 因反斜杠双写自动区分，回灌**不需要任何附加参数**（`FORMAT text` 全默认即可）。`--import-script auto` 会为 `.txt` 生成 `\copy ... WITH (FORMAT text, ENCODING 'UTF8')`；`--delimiter` 支持自定义（`'\t'` 写法归一为制表符）。与 `--sql` 互斥、与 `--header` 互斥（该格式无表头）。实现上值已在解码层转成字符串，只加一个转义函数（`textField`，含零分配快路径），不改解码链路。实机比对：特殊值探针 10 行、控制字符全量探针 4 行、**100 万行×12 列混合类型表 222,408,770 字节全部 `cmp` 相同**；回灌 10/10 行与源值一致；金仓 V8R6C9B14 导出同样逐字节一致且可回灌。
  - **B13（P1，数值损坏）`numericDiskToStr` 整数首位判据错误**：PG numeric 用 base-10000 存整数位，`value = Σ digits[i] × 10000^(weight-i)`；当整数部分"尾组全零"时 `weight >= ndigits`（如 10000 = 1×10000¹，只有 1 个数字组但 weight=1），旧判据 `weight+1 <= ndigits` 不成立 → 首位被判 0，**整段整数坍缩为 `0`**。影响 `numeric`、`json`、`jsonb` 中所有此类值（10000、20000、150000…），**不报错、退出码 0**，历史回归用行数/局部聚合也很难发现。判据改为"有数字组即取 digits[0]"。该缺陷由本次 `--text` 与服务端逐字节比对在 100 万行表的第 10000 行暴露（100 行差异 = 10000 的整数倍），修复后 0 差异。新增 14 个用例（含 10000/1000000/1e8/10000.5/负数/带零组形态）。
  - **B14（P1）`--ddl --data` 把 SQL 会话头写进数据文件**：旧判据 `sqlDataOut || o.DDL` 在 DDL 已独立成 `.ddl` 时仍向数据流写 `SET client_encoding`/`SET standard_conforming_strings` 两行 → `.csv` 首行变成 SQL 语句（`\copy` 列数不符、整表失败），`--text` 下更明显（同一文件 SQL 与制表符数据混杂）。判据改为仅在"DDL 无独立文件"时才写。
  - **B15（P1，静默 8 小时偏移）conf 内 `timezone` 多次赋值取了首个**：金仓镜像常见 `kingbase.conf` 由 initdb 写入 `timezone='UTC'`，运维再追加 `timezone='Asia/Shanghai'`；PG 语义为**后值覆盖前值**，旧实现 `FindSubmatch` 取首个匹配 → mysql 兼容模式 `timestamp` 整体 -8 小时（实测 V9R1C2B14：`2024-01-02 03:04:05` 导出成 `2024-01-01 19:04:05`），`--tz` 可绕过但默认路径静默出错。现改为取**最后一个可解析赋值**，并优先读 `kingbase.auto.conf`/`postgresql.auto.conf`（`ALTER SYSTEM` 落盘）。
  - **B16（P1，CSV 回灌静默截断）值恰为 `\.` 时未加引号**：psql/ksql 的 `\copy` 把"整行恰为 `\.`"当作数据结束标记（与 CSV/text 格式无关）。单列表且该列值恰为 `\.` 时，CSV 导出行就是 `\.` → 回灌在此截断，**其后所有行静默丢失**（实测 10 行只进 7 行、无任何报错）。`csvField` 现对 `\.` 加引号（行变 `"\."`）。text 格式不受影响（反斜杠双写为 `\\.`，天然不撞标记）。
  - **B17（P2，历史时区偏移）金仓 mysql/ora 模式 `timestamp` 按固定偏移解码**：金仓按"会话时区**当时**的规则"把本地时间转 UTC 存储——1901 年前为 LMT（Asia/Shanghai = +08:05:43）、1986-1991 中国夏令时为 +09:00。固定 +08:00 解码必然偏移：实测 1988-07-01 12:00 导出成 11:00（-1h）、1900-01-01 12:00 导出成 11:54:17（-5m43s）、BC 时间戳亦偏（V9R1C2B14；V9R1C10B4 因会话时区为 UTC 未暴露）。现按 IANA 名加载完整时区规则（`time.LoadLocation`）并逐值查其时刻的真实偏移；二进制内嵌 `time/tzdata`（约 +405KB），信创离线环境无 `/usr/share/zoneinfo` 亦可用。`--tz` 同时接受 IANA 名与数值偏移。
  - 本轮回归（v1.1.1 二进制）：5 实例（PG17.6 / 金仓 V8R6C9B14-PG / V9R1C2B14-MySQL / V9R1C10B4-MySQL / V9R1C10B4-PG）× 5 张特殊字符探针表（46 项特殊值 + 单列无主键 + 多类型 + TOAST + 特值边界）× 3 种格式（`--sql`/`--data`/`--text`）全绿；`--text` 产物与同实例服务端 `COPY ... FORMAT text` 逐字节一致（`sc_edge` 因 mysql 模式 interval 微秒位数/timestamptz 呈现不同，仅文本形式差异，语义等价）。
  - 性能：`--text` 与 `--data` 基本持平（100 万行 64~70s，同机波动），二进制/文本格式本身不改变解码开销——本轮不做二进制格式，理由见 `binary-export-analysis.md`。

- **v1.0.66**：**实机回归（172.16.0.141 测试机：PG17.6 / 金仓 V8R6C9B14 / V9R1C2B14 / V9R1C10B4）修复 5 条**——修复前/后双版本同 datadir 导出对比 + 活库真值逐字符核对，全部通过（probe 表 5/5 行一致）：
  - **B8（P1）`decodeAclitem` grantor/grantee 布局颠倒**：旧实现按 `[grantor][grantee][privs]` 读取，而 PG/金仓 `AclItem`（acl.h）为 `[ai_grantee][ai_grantor][ai_privs]`——**grantee 在前**。后果：跨角色授权项（bob=arw/alice）导出成 alice=arw/bob；PUBLIC 授权项（=r/alice）被误读成具名角色、PUBLIC 权限静默丢失。owner 自我授权的对称项不受影响，故一直未暴露；v1.0.65 测试助手 `aclitemBytes` 与实现共享同一错误布局，自洽通过、掩盖缺陷（助手已一并修正并新增跨角色/PUBLIC 回归用例）。
  - **B9（P1）`aclChars` 位 12 大写 'S' 回归**：v1.0.65 修复④误把位 12 改成大写 'S'；官方 acl.h（PG9.5~PG18）`ACL_SET_CHR='s'` 恒为小写，`ACL_ALL_RIGHTS_STR="arwdDxtXUCTcsAm"`，`aclitem_in` 拒绝大写 'S'（"invalid mode character"）。已改回小写 's'，位 13 仍为大写 'A'（ACL_ALTER_SYSTEM）。
  - **B10（P2，OBS-1 防御）**：nextval 形态 adbin 命中但序列 oid 未映射时，旧版静默 `continue` → 列默认值静默丢失（数据 md5 对比测不出）；改为落 `colDefaultFail` 输出人工提示。实测 PG17/金仓 V8R6 序列默认值均正确导出（未触发该分支）。
  - **B11（P1，实机测试新发现）PG17+ 16 字节 AclItem 布局**：PG17 起 `AclMode` 扩为 64 位（官方 acl.h REL_17：`ai_privs` 低 32 位权限、高 32 位 grant option），`AclItem` 变 **16 字节**，pg_type `typlen=16/typalign='d'`（PG17.6/PG18 实测；金仓全系仍为 12B/'i'）。旧实现按 12B/'i' 硬编码解析 aclitem[]：PG17 上 relacl/attacl **从第 2 个元素起全部错位**（产出 `oid:2`/`oid:3` 垃圾项），且 **WITH GRANT OPTION 静默丢失**（第 4 字段从未被读取，`bob=r*` 导出成 `r`）。现按目录 pg_type 实测的 typlen/typalign 解析（16B/8 对齐 vs 12B/4 对齐自动适配），grant option 取独立字段；无目录信息时按 12B 兜底。实机验证：PG17 probe 表 `{alice=ar/postgres,bob=r*/postgres,=r/postgres}` 等 5/5 行与服务器真值逐字符一致，金仓三版本回归无变化。新增 6 个单测（含 PG17/12B 双布局多元素数组步长用例）。
  - **B12（P2，实机发现）金仓 ACL 位 12 = 'p'**：三台金仓（V8R6C9B14/V9R1C2B14/V9R1C10）实机 aclitemout 一致显示 owner 默认 ACL = `arwdDxtp`（磁盘位图 bits0-6+bit12）——金仓把 ACL 位 12 定义为自有权限字符 'p'，与 PG16+ 的 's'（ACL_SET）同位不同义。旧版（含 v1.0.65）对金仓输出 's'，回灌金仓 aclitem_in 可能拒绝。现按引擎选权限字符表（`KbEngine` 由目录扫描注入）：金仓位 12 输出 'p'，PG 系仍为 's'。终验三台金仓导出与活库真值逐字符一致。
  - 修复前后对比示例（PG17 relacl，同一 datadir）：修复前 `{postgres=arwdDxtm/postgres,alice=rd,=awCTA/oid:3,oid:2=r/postgres}` → 修复后 `{postgres=arwdDxtm/postgres,alice=ar/postgres,bob=r*/postgres,=r/postgres}`（= 活库真值）；金仓修复前 `{system=arwdDxtS/system,system=ar/alice,system=r*/bob,system=r}` → 修复后 `{system=arwdDxtp/system,alice=ar/system,bob=r*/system,=r/system}`（= 活库真值）。
  - **OBS-4（P2，待修）**：`pg_parameter_acl` 在 PG17 是共享目录（reltablespace=1664=pg_global，同 `pg_database`），pg2sql 误按自定义表空间解析（找不到 pg_tblspc/1664）而跳过导出 → `GRANT SET ON PARAMETER` 无法导出。需为共享目录（global/ + relmapper）补导出路径。另：`GRANT ...; GRANT ALTER SYSTEM ...` 同事务回滚问题与工具无关（测试脚本所致）。

- **v1.0.65**：**第 4 轮全源码深审（4 路并行）+ 19 条修复**——本轮火力转向此前覆盖较浅的 datatype / charset / objects / integrity，并对 v1.0.64 的 14 条修复做回归核查（E-1~E-14 全部真实落地、无假修复）。上游 `array.h`/`arrayfuncs.c`/`acl.c` 逐字核对后坐实并修复：
  ① **数组定长元素对齐（P0，静默错数据）**：v1.0.46 的"定长元素紧密排列"结论只对 `typlen` 恰为 `typalign` 倍数的类型成立（float8 8/8、int4 4/4…，故当时实机取证无法区分）。上游 `array.h` 明示 "Individual items in the array are aligned as specified by the array element type"，`CopyArrayEls→ArrayCastAndSet` 对定长元素同样走 `att_nominal_alignby`。**`timetz[]`（12B/'d'，间距 16）与 `macaddr[]`（6B/'i'，间距 8，动态类型路径）从第 2 个元素起解出垃圾**，不报错；现按 `(pos-bodyOff)%typalign` 统一补齐（对齐基准是数据区起点，与 `ARR_DATA_OFFSET` 同余 mod 8），现有全部内置类型填充为 0、行为不变；
  ② **大对象内存降级路径排序（P0）**：临时文件创建失败时按物理扫描序拼接正文，绕过 pageno 排序校验 → 可产出"校验通过但内容错位"的 LO SQL；现先按页缓存、输出前统一排序拼接；
  ③ **`ParseTextArray` NULL 位图反转（P1）**：PG 约定位置位 = 非 NULL，旧实现写反 → 混合命名函数（`f(a int, OUT int, c text)`）的 `proargnames` 元素错位，`RETURNS TABLE` 列名串位；同时修正 `dataoffset` 坐标（应 `-4`，与 `DecodeArray` 同坐标）；
  ④ **aclitem 丢 `WITH GRANT OPTION`（P1）**：`ai_privs` 高 16 位是 grant option 位图（`aclitemout` 逐位输出 `字符*`），旧实现只扫低 16 位 → 列级授权回灌后权限降级。（注：本条当时附带把位 12 `ACL_SET` 从小写 `'s'` 改成了大写 `'S'`，属误改——PG9.5~PG18 官方 `acl.h` 的 `ACL_SET_CHR` 均为小写 `'s'`，`aclitem_in` 拒绝大写 `'S'`，v1.0.66 已纠正，详见 v1.0.66 记录 B8/B9）；
  ⑤ **稀疏大对象补零（P1）**：`pg_largeobject` 官方支持稀疏存储（`lo_truncate`/lseek 越过 EOF），缺页应补零而非整对象拒绝；现按 `LOBLKSIZE=BLCKSZ/4` 补零导出并留告警，重复页仍整对象拒绝；
  ⑥ **金仓 `pg_auth_members` 布局锚定（P1）**：金仓 V8/V9 把系统列（tableoid/cmax/xmax…）列为正 attnum，`[tableoid,cmax,xmax]` 恰好全 4B 通过旧校验 → cmax/xmax 被当 roleid/member，成员关系静默全丢；校验补 admin 1B bool 锚点，`has16` 改为只从校验通过的布局推导；
  ⑦ **PG18 虚拟生成列 + NOT NULL（P1）**：虚拟列不落盘、INSERT 不含该列，DDL 若保留 NOT NULL 则回灌每条 INSERT 失败；现对 `attgenerated='v'` 剥离 NOT NULL 并输出补建注记；
  ⑧ **其余（P2/P3）**：GB18030 截断兜底对 ASCII 字节原样直通（旧版产出 C0/C1 非法 UTF-8）、`"char"` 非 ASCII 字节按原始字节透传（旧版 `rune()` 化不可逆）、CHECK 注记不再恒打印 `(oid 0)`、`GetNulls` 删除 PG<12 反转分支（`t_bits` 语义全版本一致）、空大对象补 `lo_create`、`lo_from_bytea` 改 `decode('hex')`（对 `standard_conforming_strings=off` 免疫）、`--large-object-oids` 非法项/零命中告警、`import_all.sh` 改为任务依赖序（分区父表先于子表，替代文件名字典序）、表空间序列兜底补 `sys_tblspc`、`--report-file` 同文件判定升级为 inode 级 `os.SameFile`（防 symlink 绕过）、内联压缩解压失败留日志。审计另提出 "`sys_` 前缀判据过宽"，复核后**维持原判据**（金仓内置 schema 确用 `sys_` 族，收紧会让系统表坏块计入用户 verdict 形成假阳性）。新增 21 个回归用例（timetz[]/macaddr[] 对齐、位图、aclitem、GB18030 截断、空 LO、decode 形式、金仓 `pg_auth_members` 布局、`import_all.sh` 依赖序、`--report-file` symlink、`GetNulls` 跨版本语义）。**升级须知**：`large_objects.sql` 的 `lo_from_bytea` 由 `'\x..'` 改为 `decode('..','hex')`；`import_all.sh` 条目顺序变为任务依赖序（父表在前），下游逐行基线需更新。

- **v1.0.64**：**三路深审 14 条闭环（E-1~E-14）**——对 v1.0.63 新增/大改代码做全新深审（catalog/app/engine 三路并行 + 上游源码逐字节核对剔除误报），坐实缺陷全部修复：
  ① **PG18 虚拟生成列（E-1，条件性 P0）**：`attgenerated='v'` 的列不落盘，旧版当普通列解析 → **该列之后的整行错位**；现物理布局保留槽位（对齐 nulls 位图）但解析/INSERT/CSV 全部跳过，DDL 输出普通列 + 人工补建提示，且其 adbin 不再误导出为 `SET DEFAULT`；
  ② **COLLATE 限定名（E-2）**：自定义 collation（常落在 public/用户 schema）旧版输出裸名 `COLLATE "mycoll"`，目标端 search_path 命中同名则静默绑错；现读 `collnamespace` 统一输出 `COLLATE <schema>."name"`；
  ③ **导入脚本命令注入补漏（E-3，P0）**：v1.0.60 修了表名，但 `\copy` 的**文件名**与**列清单**仍处 shell 双引号上下文，单引号无保护 → 建一张 `a$(touch /tmp/x)b` 表即可在导入机执行任意命令；现统一走 `shellDQLEscape`；
  ④ **`\copy` DELIMITER 联动（E-5）**：旧版硬编码 `','`，`--delimiter '|'` 导出后一键导入必然错位；现取 `--delimiter` 实值；
  ⑤ **目标库自动创建（E-7）**：`import_all.sh`/单表脚本头部新增 ensure-DB 前置段（`MAINTDB` 可指定维护库），目标库不存在时自动 `CREATE DATABASE`——从根上消除"首轮导入全 FAIL"假象（GBK 等特殊编码目标库请预建）；
  ⑥ **表空间序列（E-8）**：旧版对非默认表空间的序列静默跳过 `last_value`（零告警、回灌从 START 重计数可能撞主键）；现按 `pg_tblspc/sys_tblspc` 解析路径读取，解析失败输出告警注释；
  ⑦ **`import_all.sh` 条目排序（E-6）**：旧版按 `--jobs` 并发完成顺序排列，分区子表可能排在父表前，`set -e` 下首错中止余下全部；现按文件名排序；
  ⑧ **`--report-file` 同文件守卫（E-11）**：与 `-o` 指向同一路径时拒绝（旧版会用 JSON 覆盖刚导出的数据文件）；`--table-name a,b` 失败路径补写报告（E-9）；纯 DDL 模式 `--checksum` 改为对 `.ddl` 实际内容计算（旧版只哈希两行 SET 头，报告校验和是假值）（E-14）；
  ⑨ 其余：`--import-script csv/sql` 与导出类型不匹配时跳过并告警（E-4）、元组越界记账 bad_pages + 告警（E-10）、`pg_sequence` INCREMENT=0 视为属性不可信并禁用选项输出（E-13）、`pg_depend` 缺失时不再打印"扩展成员未导出"注记（E-12）、`lo_put` 版本提示 9.3→9.4。新增 3 个回归用例（虚拟列跳过/注入转义/排序与 ensure-DB）。

- **v1.0.63**：**25 条缺陷三轮闭环（A 级 6 + B 级 13 + C 级 6，含 N-2 独立序列不导出）**——本轮聚焦"导出的 DDL/数据回灌后与源库不等价"，全部在 PG 17 + 金仓 V8R6 实机复现、修复、回灌比对验证：
  ① **序列选项与当前值（B-6 / N-2）**：旧版 `CREATE SEQUENCE` 只写 `START WITH`，`INCREMENT/MINVALUE/MAXVALUE/CACHE/CYCLE` 全部丢失，且 `setval` 用 `MAX(列)` 猜值 —— 序列有空洞或 `INCREMENT≠1` 时**回灌后 nextval 立刻撞主键**。现读 `pg_sequence` 六项全量输出，`setval` 用序列自身 `last_value/is_called` 真值（`SELECT setval('s.n'::regclass, 110, true)`）；独立序列（非 IDENTITY 附属）此前**完全不导出**，现随 `--full-schema` 输出（`--schema-objects=sequence` 可关）；
  ② **UNIQUE / EXCLUDE 约束（B-7）**：这两类约束在 PG 里以索引形式存在，旧版只当普通索引输出 —— `UNIQUE` 的 `DEFERRABLE INITIALLY DEFERRED` 丢失，`EXCLUDE` 直接**凭空消失**（排他约束等于没建）。现从 `pg_constraint` 取 `contype='u'/'x'` 输出 `ADD CONSTRAINT ... UNIQUE/EXCLUDE USING <am> (...)`，含 `NULLS NOT DISTINCT`、`INCLUDE`、`WHERE` 谓词、`DEFERRABLE`，并按 `indclass` 还原 opclass（`"code" "text_pattern_ops" WITH =`）；
  ③ **IDENTITY / COLLATE / UNLOGGED（B-8）**：`attidentity` 未读 → `GENERATED ALWAYS AS IDENTITY` 变成普通 `int4`，回灌后新行不自增；`attcollation` 未读 → `COLLATE "C"` 丢失，排序规则静默变化；`relpersistence='u'` 未读 → UNLOGGED 表建成普通表。三者均已补，且 IDENTITY 列 INSERT 自动追加 `OVERRIDING SYSTEM VALUE`（`GENERATED ALWAYS` 否则直接报错）；
  ④ **大对象（B-1 / B-2 / B-3）**：`pg_largeobject_metadata` relfilenode 误用已废弃的 2614（旧 `pg_listener`）→ 单对象导出时 owner 全丢；外部构造条目 `ByteLen=0` 导致 20MB 对象被判成小对象走 `lo_from_bytea`（超 `bytea` 上限即失败）；巨型对象写出阶段不必要的 fsync。现修正为 2995、写入前按 `DataHex` 反推长度、`lo_create`+分块 `lo_put` 路径稳定，回灌后 `sum(length)` 与 `md5` 双重校验与源库逐字节一致；
  ⑤ **xid 回绕与 varlena 越界（B-5 / B-10）**：元组可见性判据由 `txmin == 0 || txmin > 0x7FFFFFFF` 改为仅 `txmin == 0`（旧判据在 xid 回绕后把**未来事务**的元组当已删除/冻结误判）；`calculateTupleSize` 累加 varlena 前校验 `offset+total <= nraw`，坏块不再算出超出页面的元组长度；
  ⑥ **健壮性（C-1 ~ C-6）**：删除 `decodeTxidSnapshot` 遗留的 `DBG_RANGE` 调试钩子（会往 stdout 打脏数据）；`FindTableInMeta` 裸表名解析改为「public 优先、其余字典序最小」的稳定选取（旧版命中随机 schema）；`pg_authid` 布局校验加 `attlen==8` 门槛并对截断行统计告警；系统表读取改用 `readFullPage`（短读不再静默截断）；UTF-8 兜底只对真正非法字节走 latin-1（合法 `U+FFFD` 原样保留）；GB18030 末尾残缺四字节序列整体兜底；
  ⑦ **`pg_am` 兜底表订正**：内置 OID 表把 gist 写成 2745（不存在）、spgist 写成 2747，目录读失败时 EXCLUDE 会静默落成 `USING btree`；现订正为 gist=783 / spgist=4000 / heap=2，并对未知 AM 输出注释 + 告警，不再静默降级；
  ⑧ **扩展成员不再误当用户对象导出**：扩展（pageinspect / btree_gist / pg_stat_statements / plpgsql…）装进 `public` 后，其函数/视图/触发器全部落在用户 schema，被当成自建对象输出；`LANGUAGE c` 的函数缺 `probin`，`AS` 段只写符号名 → PG 把它当共享库路径去找，回灌直接 `could not access file "brin_metapage_info"`。现按 `pg_depend`（`refclassid=pg_extension` 且 `deptype='e'`）过滤扩展成员，产物首部注明"扩展 X 的成员对象未导出，请在目标端先 CREATE EXTENSION"；同时补读 `probin`，非扩展的 C 函数正确输出 `AS '<共享库>', '<符号名>'`。
  **升级须知**：`.ddl` 会新增 `ADD CONSTRAINT`（UNIQUE/EXCLUDE）、序列段、`COLLATE`/`GENERATED ... AS IDENTITY`/`UNLOGGED`；`--full-schema` 输出多出序列段，需在各表建表 DDL **之后**执行（段首已注明）。工具仍**不输出 `CREATE SCHEMA`**，跨 schema 回灌前请先建好目标 schema。

- **v1.0.62**：**深度审计收尾（P2-3/6/7/8 + P3 健壮性）+ 放开批量 DDL-only**：
  ① **批量 DDL-only 可用**：`--schema/--tables/--all-tables` 配 `--ddl`（不带 `--sql`/`--data`）不再被拒，逐表输出 `.ddl`；只想要库级产物时也可只给 `--full-schema`/`--large-objects`/`--roles`（此前这类组合一律报"必须指定导出类型"）；
  ② **P2-3 并发写共享 Options**：`resolveDbDir`/`ResolveDataRoot` 在表空间路径分支里回写 `o.DBOID`，而批量 `--jobs>1` 时多个 worker 共用同一份 Options —— 数据竞态，且 A 表推导出的库 OID 可能被 B 表覆盖 → 按错误库目录定位。改为只读，新增 `ResolveDataRootAndDBOID` 返回路径与 dboid；
  ③ **P2-6 `pg_control` 兼容性**：`CtrlVer` 下界从 1000 放宽到 900（PG 9.5/9.6 的 `PG_CONTROL_VERSION` 是 960，旧校验把整段 `db_state` 证据判成"无法自证"丢弃）；`state=0`（`DB_STARTUP`，实例刚启动未 checkpoint）不再被当损坏——而这恰是"取快照时实例在跑"最该提示的状态；
  ④ **P2-7 大对象超 2GB / 内存**：超过 32MB 的对象正文溢写到临时文件（此前 4GB 的 LO 需要 8GB+ 内存峰值，取证机直接 OOM），写出阶段按 16MB 块流式生成 `lo_put`；>2GB 时明确提示目标端需 PG 9.3+ 的 `lo_put(offset int8)`；
  ⑤ **P2-8 钱包多表空间**：`tspkey.kr` 条目改为按 1184B/1252B 记录步进匹配 OID（旧实现逐字节扫描，密钥密文里出现同一数值就会从错位处取密钥 → 解出错误 TSPK、整库乱码且无告警），并加记录结构校验；
  ⑥ **P3 健壮性**：所有 SQL/DDL 输出首部写 `SET standard_conforming_strings = on`（反斜杠字面量语义不再依赖目标库默认值）；`LayoutAlign` 认小写 `attalign`（PG 实际取值 `c/s/i/d`）；`--enckey` 等密钥脱敏按 rune 截断（中文密钥不再产生非法 UTF-8）；IANA 时区名优先走 zoneinfo（欧美/大洋洲夏令时不再整体偏 1 小时，固定表仅作缺 tzdata 兜底）；系统 schema 判定收敛到 `integrity.IsSystemSchemaTable` 单一实现（系统表坏块不再计入用户表 verdict 假阳性）；免费版二进制提示文本不再含加密产品缩写；预扫命中即提前返回（大表不再把 IO 翻倍）+ 预扫阶段显示"预扫 k/N"而非 0%；扫描回退路径的 CTID 语义（页内字节偏移）首次使用即提示，避免误当 itemid 查询；清理 4 处死代码。

- **v1.0.61**：**深度审计修复第三批（DDL 语义补全，P1-8 + P2-4/5/9/10，详见 `audit_20261028_v1058.md`）**：
  ① **P2-9 函数 DDL 语义**：旧版无论什么对象一律输出 `CREATE FUNCTION ... RETURNS <type>` —— 返回集合的函数丢 `SETOF`（回灌后调用语义完全不同）、存储过程变成同名普通函数、`OUT/INOUT/VARIADIC/TABLE` 参数整体丢失（`proargmodes` 未读）。现读 `prokind/proretset/proargmodes`：`'p'→CREATE PROCEDURE`、`proretset→RETURNS SETOF`、`TABLE 模式→RETURNS TABLE (...)`、有 `OUT/INOUT` 时按 PG 规则不写 `RETURNS`、`VARIADIC` 参数加修饰词；**聚合/窗口函数（`prokind='a'/'w'`）不再伪造成普通函数**（离线拿不到 `pg_aggregate` 的 transfn/finalfn），改为明示注释跳过；
  ② **P1-8 函数签名类型名**：自定义类型（枚举/域/复合）在签名里既不加 schema 限定也不加引号 → 跨 schema 同名类型回灌静默绑错、含大写/空格/保留字的类型直接语法错误，且 `pg_type.typname` 可注入任意 SQL。现按 OID 走 `catalog.TypeSchemaName + QuoteIdent`，数组输出 `"elem"[]`（方括号在引号外），内置类型保持裸名；同时修 `nextval('%s.%s'::regclass)` 把标识符塞进单引号字面量的破句问题（`'` 按字面量规则翻倍）；
  ③ **P2-4 主键 INCLUDE 列**：`PRIMARY KEY (a) INCLUDE (b)` 的 `indkey` 是 `[a,b]`，旧版整体当键列 → 导出成 `PRIMARY KEY (a,b)`，**唯一性从 a 收紧到 (a,b)**；`UNIQUE ... INCLUDE` 同理。现按 `indnkeyatts` 截断真键列，非主键索引把 INCLUDE 列单独输出（`... USING btree (a) INCLUDE (b)`）；
  ④ **P2-5 分区判定与缓存**：`pg_inherits` 同时记录普通 `INHERITS` 与声明式分区，旧版一律当分区子表（普通继承子表被提示"是分区子表"、参与分区排序/索引跳过判定）。现以 `relpartbound` 存在或父表 `relkind='p'` 为判据；分区元数据缓存键补上 `version`/`isKB`，且整目录读空时不再写缓存（避免首次参数不全时把空结果永久固化）；
  ⑤ **P2-10 pg_authid 布局**：hasoids 目录（PG<12 / 金仓 V8）的行数据以 `oid(4B)` 开头，但 NULL 位图只覆盖 tupdesc 列 → 旧版位图整体错位一位，`rolpassword`/`rolvaliduntil` 为 NULL 时读成邻居字段的字节。现统一把布局归一化为「首槽 4B oid」，hasoids 行前置一位位图，`pg_auth_members` 同步修正；
  ⑥ 回归：`go test -race` 免费/TDE 双标签全绿（新增 11 个用例：类型名限定/注入、`char[]` 解析、`RETURNS SETOF`/`PROCEDURE`/`OUT`/`TABLE`/聚合跳过、`indnkeyatts` 截断、缓存键、hasoids 合成行）；141 实机用 1.0.60 为基线跑 `smoke_cmp.sh`，38 个产物逐字节一致，`--full-schema` 在 pageinspect 库（16896/16570）上 26 处差异**全部**为补齐 `RETURNS SETOF`；金仓 V8/V9 `--roles` 输出与旧版一致。
- **v1.0.60**：**深度审计修复第二批（静默退化补硬校验，P1×4 + P2×5，详见 `audit_20261028_v1058.md`）**：
  ① **P1-9 TryAutoLayout 页头少 4 字节（金仓全表命中）**：页头实为 24B（含 `pd_prune_xid`），旧算法 `(psvOff+2+3)&^3` 只算到 20 → ItemId 数组从 prune_xid 起算，**全部 CTID 页内偏移 +1**，且 prune_xid 碰巧形似 ItemId 时会多解析一条垃圾行。金仓页版本=5、`TryStandardLayout` 只认 4，故金仓所有堆页都走该函数（此前内容"侥幸"正确，偏移一直错）。现改 `(off+6+3)&^3`，并用合成 v5 页 + 「长得像 ItemId 的 prune_xid」双用例钉死；
  ② **P1-5 单文件模式自动编码探测失效**：`ProbeFileEncoding` 要求 pageSize≥8192，而探测发生在页大小探测**之前**（默认 0）→ v1.0.44 的"GBK 单文件自动识别"实际从未生效，且 `unknown` 污染编码基线并每次打印误导告警。现先 `ProbePageSize` 再探测，unknown 不进编码链；
  ③ **P1-6 多表 + 单文件位置参数**：`--table-name a,b` 配单文件时所有表按同一文件解析（b 的文件名下装 a 的行）→ 静默错行。现批量/多表入口显式拒绝并给出正确用法；
  ④ **P1-7 TDE 大对象密钥校验**：LO 路径只 `Init()` 不 `VerifyDecryptorKey`，错密钥 SM4-ECB 照样"成功"解出随机字节 → 静默乱码 LO 且 exit 0；`tspOID==0` 直接跳过解密（`pg_largeobject` 绝大多数在默认表空间）。现补密钥校验、默认表空间按 `pg_default(1663)` 查 TSPK、注入了钱包参数却无解密器时硬报错；
  ⑤ **P2-1 编码兜底链收紧**：金仓表不再继承上游 45–48=utf-8（未来新增编号会被静默错解）；负数/未知编码编号改为显式告警；`global/1262` 硬编码改为「标准路径不存在则扫 global/ 候选文件」兜底（`pg_database` 经 VACUUM FULL 后 relfilenode 重排不再静默失探）；
  ⑥ **P2-2 批量编码预扫空转**：预扫从 `o.PageSize=0` 起算，首个成功解析文件之前的任务全部以 ps=0 调 `ProbeFileEncodingMajority`（要求 ≥8192）恒空转 → 混合编码漏判 → `--jobs>1` 下 worker 中途翻转进程级编码（表级乱码）。现预扫前统一 `ProbeDirPageSize` 定死页大小；
  ⑦ **P2-11 TOAST 损坏语义与串行解密**：`__TOAST_CORRUPT__` 此前不在占位符清单，被当**真实列值**写进 SQL/CSV（回灌后得到字面量垃圾），现与 `__TOAST_MISSING__` 统一按 NULL 输出；串行路径解密失败由 `break`（丢该段剩余全部页）改为记坏页继续；**扫描回退路径 `streamScanSerial` 此前完全无解密分支**（TDE 库走回退时密文当明文）已补齐；
  ⑧ **P2-12 输入校验组**：`--page-size` 非 PG 合法值（负值会 makeslice panic、`1234` 之类会静默 0 行且 verdict=ok）现直接拒绝；输出文件名对 schema/表名做组件消毒（防 `../` 穿越写出 `-o` 目录）；`import.sh`/`import_all.sh` 的表名与文件名做 shell 双引号/单引号转义（防 `$(...)`/反引号注入）；段文件中间缺失改为显式告警（并按目录项枚举，不再遇第一个缺号就静默丢弃后续段）、目标表主段 0 字节给出告警；`--report` 写失败由"记日志 + exit 0"改为硬失败（报告是交付物）。
  **验证**：新增 6 个单测（金仓 v5 页头 / prune_xid 非 ItemId / 输出路径穿越 ×2 / shell 转义 ×2）；`go test -race` 免费+TDE 双标签全绿；141 实机以 1.0.59 为基线跑 `smoke_cmp.sh`（38 个产物：PG17 全模式、金仓 V9、角色 DDL、大对象、deleted 审计）**数据产物逐字节一致**，差异仅日志中的输出目录路径；金仓 GBK 库 `public.g_t` 中文解码不变。

- **v1.0.59**：**深度审计修复第一批（P0×4 + P1×3，正确性红线，详见 `audit_20261028_v1058.md`）**：
  ① **P0-1 nt 解析器列表分支死循环**：`parseValue` 遇 `}` 无进展不置错 → 损坏的 `pg_attrdef.adbin`/`indexprs`/`relpartbound`（CString 截断）触发整库导出无限挂死（CPU 100% + 内存增长）。现列表元素解析无进展即 `p.err=true`（与另两处 P3-G13 兜底统一）；
  ② **P0-2 辅助导出覆盖数据文件**：`--full-schema`/`--large-objects`/`--roles` 与单表 `-o out.sql` 组合时，三个辅助导出复用 `o.Output` 做 `os.Create(O_TRUNC)`，依次覆盖刚导出的数据文件且 report 记录伪校验和。现统一走 `auxOutputPath`：`-o` 为具体文件时辅助产物落其所在目录固定文件名（`schema_objects.ddl`/`large_objects.sql`/`roles.ddl`）并打印提示；
  ③ **P0-3 `-o` 指向源数据文件**：输出 `os.Create` 先于读取任何页且无同文件校验，`-o` 指向源堆文件/其符号链接 → 先清零源库再导出 0 行 exit 0（取证场景销毁证据源）。现数据/DDL/辅助产物创建前与源路径做 `os.SameFile` 比对（含符号链接/硬链接），命中即拒绝；
  ④ **P0-4 大对象页连续性**：LO 扫描 4 处失败路径静默丢页后继续按 pageno 拼接（丢一页=后续内容整体前移，exit 0）。现聚合期强制校验 pageno 从 0 连续无重复，不完整对象整体拒绝输出并计入 report skipped 清单（`lo_page_gap`）；
  ⑤ **P1-1 NULL 位图 attnum 错位**：`ParseNamedTuple` 用过滤后列下标索引 t_bits，而位图语义是 attnum-1——PG12+ 目录首列是已 DROP 的 oid 被过滤后，从第一个真实列起 NULL 判定整体错位一列（`prosrc` 被误判 NULL → **PG12+ 全部用户函数体导出为空**）。现按 `item.AttNum-1` 索引；
  ⑥ **P1-2 ExtractFieldsDirect 失败列重叠解析**：varlena 非法时 switch-break 不推进 pos，后续列从同一偏移重探（同值复制/滑动解析垃圾）。现失败列置 nil 并终止本行剩余列；
  ⑦ **P1-3 oidvector/int2vector 解码未剥头**：decoder 契约是"接收完整 varlena 自行解头"，这两个把 varlena 头字节当 oid/int2 元素输出。现剥 varlena 头 + 20B 数组头、按 dim1 截取（与 `ParseOidVector` 同语义）。
  **验证**：新增 7 个单测（2 个审计复现测试去 Skip 转回归、varlena 位语义钉死、vector 解码回归、nt 置错断言），`go test -race` 全绿。**附带产出**：以金仓 `pg_proc.prosrc` 真实磁盘字节（`0x11 "lsegin"` → total=8）实证钉死 1B varlena 头位语义（首字节奇数、长度 = first>>1），防止将来误按"记忆中的上游宏"反向改写。

- **v1.0.58**：**P1 修复：金仓非 UTF8 多字节编码库导出数据篡改**。金仓把 pg_enc 35–44 重排为自有服务端编码（V8R6C9B14 以 `pg_encoding_to_char` 实测：35=GBK、36=UNICODE、37=UTF8MB4、38=GB2312、39=GB18030、40=SJIS、41=BIG5、42=UHC、43=JOHAB、44=SHIFT_JIS_2004），而 v1.0.56（P2-G2）把编码映射表按上游 PG 语义校正（35=SJIS…且仅客户端、不可能出现在真实 PG 库编码中），导致金仓 GBK 等库（enc≥35）被误判为 sjis 等不支持编码，非 UTF8 字节被转义为 `E'\x…'` hex 文本写入 SQL，导入后存储为 34 字符 ASCII 转义串，数据内容不可逆篡改。修复：`catalog.DetectDatabaseEncoding` 按数据目录类型分流——PG 走上游表（0–34 即全部服务端编码），金仓走新增的 `charset.EncodingIDToCodecKB`。**验证**：新增 5 个单测（金仓编号表断言、报告现场 GBK 字节解码、合成 global/1262 离线探测金仓/PG 分流）双标签全绿；实机金仓 V8R6 真实 GBK 库（enc=35）免费/TDE 版导出正确中文；「标 GBK 实存 UTF8」库自动回退行为不变；UTF8 金仓库与 PG17 导出产物与修复前逐字节一致。**临时规避**：旧版可用 `--encoding gbk` 强制。

- **v1.0.57**：**目录结构重构（对外行为零变化）**——源码由「约 30 个 `.go` 全平铺在 `package main`」改为正式布局：`cmd/pg2sql` 仅作入口，业务编排下沉 `internal/app`，参数解析独立 `internal/cli`，底层拆为 `internal/{config,util,logx,charset,datatype,engine,nt,catalog,ddl,objects,integrity,paid}` 共 15 个包，`tde/` 保持独立付费包。原先 `datatype` 需要目录信息、`catalog` 又调用 `datatype` 的循环依赖，改用回调注入（`datatype.DynamicScanner` / `PostScanHook` 由 app 的 `init()` 注入）破解；跨包符号按 Go 规则导出，`go vet` 的 unkeyed 字面量告警清零。**验证**：75 个单测在免费/TDE 双标签下全绿；实机 PG17 夹具库 + 金仓 V9（PG 模式）导出产物与 v1.0.56 **逐字节一致**（38 个产物文件 diff 为空，仅日志中的输出路径不同）；免费版二进制 strings 特征不变。**源码包改为整树复制**（`release.sh` 不再维护逐文件清单，消除「清单漏文件」漂移点），解压后 `go build ./cmd/pg2sql` 直接可编。

- **v1.0.56**：遗留问题修复轮（三批 16 项 + R2/R3 专项，三路并行深审、逐条人工核验驱动）：
  ① **P1 multi-xmax 判活**：`isDeleted` 对 `HEAP_XMAX_IS_MULTI` 一刀切判活 → 行级锁 + 更新并发 workload 下已提交删除行被静默导出（无告警）。现按 `HEAP_KEYS_UPDATED` 区分：置位（multi 含 updater）判删除、未置位（纯锁 multi）判活，`HEAP_XMAX_INVALID` 兜底 aborted updater；
  ② **P2 编码枚举映射校正**：对照 PG 上游 `pg_wchar.h` 逐一核实——35=SJIS/36=BIG5/38=UHC/39=GB18030/40=JOHAB/41=SHIFT_JIS_2004 旧版误映射到 gbk/utf-8/gb18030/latin1，其中 **39（GB18030，金仓常用库编码）按 GBK 解码时四字节区静默乱码**、36（BIG5）按 UTF-8 解码失败后逐字节 Latin-1 不可逆伪解码。现按正确枚举映射，未实现解码的多字节编码走「告警一次 + \x hex 可逆输出」（现网 GBK 测试未暴露纯属 37=GBK 恰好正确）；
  ③ **P2 proargtypes 解析器错误**：`proargtypes` 是 oidvector（4 字节元素）却复用 int2vector 的 2 字节步长解析 → ≥2 参数的函数 DDL 签名错位（`--full-schema` 回灌失败）。新增 `parseOidVector`；
  ④ **P2 分区父表 + 纯 `--count` 多叶子必然报错退出**：展开分支认 count 系模式、`runNamedTables` 只认 --sql/--data，矛盾组合 errorExit。现纯 `--count` 逐叶子统计；
  ⑤ **P2 recordQuote 对齐 PG record_out**：补空串/反斜杠/内部空白三类引号触发（空串裸输出被解析为 NULL 的静默失真、`a\b` 回灌变 `ab`）；**P2 meta.json 补 `attgenerated` 往返**（生成列不再被反解为非法 `SET DEFAULT`）；
  ⑥ **P3 加固族**：`tryAutoLayout` 8~65 字节截断文件越界 panic；jsonb 坏数据切片越界与 nodetree `skipToken` 死循环；`civilFromDays` 负数 era 缺 Hinnant floor 处理（**4713 BC ~ 0000-02 整段日期解算错乱**，v1056 测试中发现的存量 bug）；BC 日期/时间戳输出对齐 PG（`0044-03-15 BC`）；TDE 错密钥校验前移到建输出文件之前（不再截断上次正确导出）；`relName`/`\copy` 表名标识符统一加引号；`--schema-objects` 非法取值报错、`--table-name` 与 `--tables/--schema` 互斥报错；TOAST 警告 `%d` 补实参；系统目录走 `segFiles` 多段支持；大对象 `strings.Builder` 聚合（消 O(n²)）+ `lo_create`/`lo_put` 分块（>512MB 不再生成必然导入失败的 1GB 超限单语句）；`--count` 跳过 TOAST 索引预建；
  ⑦ **R3 段边界统一**：新增 `segPageTable` 按【实际文件大小】累计段边界，`pageCount` 与 TOAST/主表读取点共享同一推导（残缺段/变长段不再错位）；**R2 归并窗口**加超限告警（背压失效早发现）；`iterSysTuples` 多段支持；
  ⑧ 新增 `v1056_fix_test.go`（14 用例），`go test` 双标签全绿，vet/gofmt 干净。
- **v1.0.55**：判定与退出码修正轮（B1~B3 + panic 隔离，代码深度审查驱动、141 实机回归）：
  ① **B1 合法空页误报「损坏数据块」**：`iterTuples` 用 nil 混淆「布局非法/全零页/合法空页」三义，VACUUM 清空后的标准空页（lower=24/upper=8192、行指针 0、页头自洽）被四个导出路径一律记为坏块——实测 2 万行删半 + `VACUUM (TRUNCATE OFF)` 后导出 10000 行完全正确，却报「坏块 102」、verdict=fail。现拆分双返回值 `(items, layoutOK)`，仅 `!layoutOK && !zeroPage` 计坏块；空页/全零页不再污染完整性自证；
  ② **B2 无效路径静默导出 0 行**：`singleFileArg` 对不存在的路径无条件按"单文件"处理（注释声称按扩展兜底、实现未判断），配合 `--datadir` 时 `pg2sql base/oid/xxx_fsm --data`、`pg2sql --datadir D --db-oid N <笔误参数> --data` 均 **exit=0、0 字节产物、无提示**——抢救场景最危险的失败模式。现：不存在的位置参数硬报错；`_fsm/_vm/_init` 辅助文件自动纠正到真实数据文件（纠正不了则报错）；数据文件缺失时按 relfilenode 重定位，仍失败则硬报错；
  ③ **B3 `--count --limit N` 语义错误**：limit 同时作用于导出与计数，`--count --limit 10` 把「前 10 行」当总行数输出（实测 5000 行表输出「总行数: 10」）。现 `--count` 恒为全表计数，`--limit` 仅数据导出生效，并输出明确提示；
  ④ **R1 批量导出 panic 隔离**：批量 goroutine 此前无 recover，一张表解析 panic 即全进程崩溃、其余表成果与 report 全部丢失。现单表 panic 经 `safeExportOneTable` 转为该表失败记录（含堆栈日志），不影响其他表；
  ⑤ 新增 `v1055_fix_test.go`（5 个用例：空页/全零页/坏页三态、混排文件坏块计数、count-limit 语义），`go test` 双标签全绿。
- **v1.0.54**：现场缺陷修复轮（C1~C4，141 实机回归）：
  ① **C1 单表导出分区父表丢失父表 DDL**：`--table-name <分区父表> --sql/--data` 只导出叶子子分区，子表 DDL 又是 `PARTITION OF` 挂靠语句 → 回灌必然 `relation "public.xxx" does not exist`、数据全丢。现单表路径与批量统一（多叶子交给 `runNamedTables` 的 DDL-only 分支，单叶子在导出前前置父表 DDL）；点名导出子表时给出"需先建父表"提示；
  ② **C2 `.sql` 未声明 `client_encoding`**：文件恒为 UTF-8 字节，但 GBK/GB18030 目标库会按自身编码解释 → 轻则 `invalid byte sequence` 中途中断，重则静默双重编码（实测 200 行全进、退出码 0、md5 与源库不一致）。现 `.sql` 首行输出 `SET client_encoding = 'UTF8';`（pg_dump 惯例），`--count`/CSV 不受影响；
  ③ **C3 密文页误报**：旧判据「16B 全零块 < 8 即密文」等价于「页空闲 < 128B 即密文」，fillfactor=100 的满页几乎必然触发（实测 PG17 纯 ASCII 表 5/6 页、金仓 GBK 表 3/4 页误报「输出 N 行不可信」）。改为「数据区零字节密度 < 0.02 且元组头不可解析」双条件（明文实测 0.07~0.31，密文 ≈0.004），并排除 bytea/压缩列等明文随机字节页；
  ④ **C4 混合存储库整表乱码**：库级把整库判成 UTF8 后，表级精化原先直接跳过 → 真 GBK 表被按 UTF8 解成 Latin-1 乱码、退出码 0 无告警。现改为「目标表数据文件多页投票」的双向精化，页间矛盾时保守不改，两种方向都给出明确提示；
  ⑤ 新增 `v1054_fix_test.go`（9 个用例）钉住上述判据，`go test` 双标签全绿。
- **v1.0.53**：完整性自证 + 角色导出（回归 85/85）：
  ① `--report` 升级为**完整性自证**：新增 `db_state`（`pg_control`/金仓 `sys_control` 离线解析：关库状态/checkpoint LSN/时间/系统标识）、`summary`（导出表数/跳过/失败/行数与估计对照/坏块/TOAST/类型未覆盖数）、逐表 `rows_estimate`/`rows_check`（对 `pg_class.reltuples`）与 `pages_expected`/`pages_actual`/`pages_check`（对 `relpages`，`short` = 段文件缺失）、`skipped[]`（7 类原因码）、`unknown_types[]`（类型解码覆盖度）、`verdict`（ok/warn/fail + 原因），stderr 同步一行结论；
  ② 新增 `--roles` / `--roles-nopassword`：离线解析 `global/pg_authid` + `pg_auth_members`，输出 `roles.ddl`（全属性 + 密码密文 + VALID UNTIL + `GRANT` 成员关系；OID<16384 的 initdb 内置角色自动跳过）；
  ③ `--all-tables` 不再因共享目录（`pg_database`/`pg_authid`/`pg_tablespace` 等 11 个 `global/` 对象）而整体失败——改为 `skipped[].reason=shared_catalog`，退出码 0；
  ④ 导出失败的表也写入 `exports[]`（`error` 字段），报告总数与"成功+失败"一致；
  ⑤ 文档补充「能力矩阵（已覆盖/近似/不支持）」「report.json 字段判读」「与 pg_dump/ksql 的报告与导入语句差异」；
  ⑥ 解析期守卫：位置参数只允许一个（`--report <路径>` 这类误用不再静默把数据目录换成该路径，报错并指向 `--report-file`）；新增 `--report-file PATH`；0 表 0 跳过不再判 `ok`（判 `fail` +「未发现任何可导出对象」）。
- **v1.0.52**：分区表 DDL 离线还原 + 表级编码精化：
  ① 声明式分区还原（`pg_partitioned_table` + `relpartbound` 节点树反解）：父表 `PARTITION BY RANGE/LIST/HASH`、子表 `PARTITION OF … FOR VALUES …`（含 DEFAULT/多级/表达式键），输出按父→子→孙拓扑排序，主键未含分区键时告警；
  ② 表级编码精化：库级探测被同库历史大表污染导致整库中文乱码（金仓 V8 GBK 库现场问题）→ 逐表按自身数据文件三态判定，仅允许 utf8 单向覆盖，混合存储库自动串行；
  ③ 回归中自查并修掉 6 个坑（`pg_class` oid 为普通列导致的 4B 错位、`PARTITIONRANGEDATUM` 单节点 value、边界反解返回值语义、timestamptz 分区边界漂移、分区父表 relfilenode 读取偏差、CLOB 压缩页触发的编码误覆盖）。
- **v1.0.51**：`pg_node_tree` 反解内核（`nodetree.go`）+ 列默认值/表达式索引/部分索引还原，为 v1.0.52 分区与 v1.0.50 域 CHECK 提供基础。
- **v1.0.50**：域类型（DOMAIN）CHECK 约束离线还原（`pg_constraint.conbin` 节点树）；USER_GUIDE 版本号与打包脚本对齐。
- **v1.0.46–v1.0.49**：批量导出编码探测缺失修复、DDL 独立成文件、`IF NOT EXISTS`、审计项 P0/P1/P2 修复等，逐条见仓库根目录 `pg2sql_v1.0.4x_*.md` 修复报告。
- **v1.0.44**：P2 破坏性场景修复轮（全版本全功能回归通过后打包）：
  ① **P2-1 补：表空间文件纳入编码探测**：TABLESPACE 库（数据在 `PGDATA/pg_tblspc` 下）原先采样不到 → 编码判定缺失。修复后 V8 32K 表空间库正确回退 UTF8、V9R1C10 表空间库按真 GBK 不回退，均与在线一致；
  ② **P2-2 坏页明细**：report.json / stderr 输出 `bad_blocks`、`bad_pages`（具体坏页号列表），坏页行跳过保留统计（实测坏页行多保留 157 行）；
  ③ **P2-3 TOAST 文件缺失计数**：TOAST 文件缺失时不再静默输出 NULL 无计数——缺失也建空 ToastFile，使 `toast_missing` 计数生效；
  ④ **P2-4 DDL 目录缺失警告**：`--ddl` 依赖的 sys/目录文件缺失时输出明确警告（原静默）；
  ⑤ **修复 4 个健壮性 BUG**：pg_index 段空路径 panic、`detectSysFile` 收到 pageSize=0 时 0 长 buf 越界 panic、`tryAutoLayout` 短 buf 越界、TOAST 文件缺失导致静默 NULL 无计数；
  ⑥ 回归：`pgbuild/reg_v1044.sh` 新增 [V1044] P2 破坏性断言段（编码回退/坏页明细/TOAST 计数/DDL 警告），三段式（参数面 21 实例 + 大表 16 实例 + 混合 22 组合 + 分区）+ TDE 专项全部通过。

- **v1.0.43**：补测 V8/V9 各兼容模式 GBK 库后修复 + 修正编码行为结论：
  ① **修复 32K 页采样漏回退**：`probeDirUtf8Ratio` 原每文件只读前 16384 字节——PG 堆页**行数据位于页尾**（tuple 从页尾向前增长），8K/16K 页恰好覆盖整页能触发，**32K 页前 16KB 只有页头+空闲区、采样不到任何行数据** → UTF8 特征不命中 → 不回退 → 导出乱码（V8 ORA 32K 实测抓到：`缂栫爜馉鍫冩祴璇`）。修复：改为**读文件末尾一页**（`ReadAt(size-pageSize)`），8K/16K/32K 全部触发；
  ② **模式实测结论修正**（补测 V8 ORA/PG/MySQL 三模式 + V9R1C10 三模式×8/16/32K 全 9 实例）：**V8 三模式标 GBK 均实存 UTF8**（含 `client_encoding=GBK` 插入，落盘 hex `f0 a0 80 80` 证实，auto 回退后与在线 100% 一致）；**V9 全系标 GBK 实存真 GBK 双字节**（V9R1C10 ORA/PG/MySQL × 8K/16K/32K 共 9 实例 + V009R003C018 MySQL 全部实测：落盘 `b1 e0`=编、`c6 d5`=普，生僻字插入时即被替换为 `?`）——auto **正确不回退**、按 GBK 解码与在线一致，`--encoding utf8` 强制才乱。即"金仓 GBK 恒存 UTF8"不成立，**按大版本而异：V8 实存 UTF8、V9 实存真 GBK**；回退机制双向验证正确（UTF8 库回退、真 GBK 库零误伤）；
  ③ 回归：`pgbuild/reg_v1043.sh` 新增 V8 ORA 32K 回退断言 ×2 + V9 真 GBK 不回退/内容正确断言 ×2，全版本全功能通过后打包。

- **v1.0.42**：全面代码/功能 BUG 分析后的修复（分析结论：无 P1，核心解析/并行/流式/对照全部正确）：
  ① **P2-1 金仓"标 GBK 实存 UTF8"自动回退**：实测金仓 V8 建 `ENCODING 'GBK'` 库插入 `编码𠀀堃测试`（含扩展字符），库内实际以 **UTF8 字节**存储（hex `f0 a0 80 80` 证实），但 `pg_database.encoding=GBK` → 旧版按 GBK 解码导出乱码 `'ç¼ç ð åµè¯'`。修复：a) 修正金仓 GBK 编码号 **35** 的映射（原误归 latin1）；b) auto 探测到非 UTF8 声明时，**采样用户表数据字节做 UTF8 特征检测**（逐文件独立判定：多字节序列合法率 ≥0.7 且 ≥4 个序列即回退），呈 UTF8 特征则自动切换 UTF8 并提示 `已自动切换 UTF8（可用 --encoding gbk 强制）`——实测导出 `编码𠀀堃测试` 与在线 100% 一致；真 GBK/GB18030 双字节流合法率 <0.5 不误触发（纯 GBK 字节 ratio=0.0 实测）；采样跳过系统目录文件（relfilenode<16384），防目录字节干扰；
  ② **P3-1**：`--fields` 未命中字段 → stderr 警告（原静默忽略，笔误会无声丢列）；
  ③ **P3-2**：`--deleted` 与 `--only-deleted` 同时使用 → 报错（语义互斥，原静默取 only-deleted）；
  ④ **P3-3**：`--batch-rows` 在非 SQL 模式（`--data`/纯 `--ddl`）→ stderr 提示已忽略（原静默）；
  ⑤ **P3-4**：`--limit 0` 语义文档化（0=不限制，默认）——帮助/README/手册同步；
  ⑥ 回归：`pgbuild/reg_v1042.sh` 新增 P2-1（金仓 testgbk auto 回退 + 导出内容一致）+ P3-1~P3-4 断言，全版本全功能通过后打包。

- **v1.0.41**：修复 `--progress` 两处问题：
  ① **修复 BUG-B（并行页进度恒 0）**：`streamStdParallel`（标准布局并行路径，`--parallel` 普通表走这里）缺 `progressDone` 埋点，`--progress --parallel N` 下页进度恒 0、百分比不涨。修复：补原子埋点（与串行/自动布局路径一致）。实测 200 万行表 `--parallel 4` 进度 41.3%→80.7%→100.0%，页 9385→18332→22728 正常递增；
  ② **进度行显示当前表名**：`[pg2sql] 进度 41.3% 表 public.huge_a 页 9385/22728 行 825774 耗时 1.0s ETA 1.4s`——多表批量（`--jobs` 表间并行）下每张表独立进度、各自显示表名，一眼可辨当前导哪张表；
  ③ 回归脚本修复：PG13/14/15/17 数据目录为 `pgdata_ft_` 前缀，原脚本只查 `pgdata_prm_` 致这些版本实际未跑（v1.0.40 回归日志遗留 4 个 FAIL 属此跳过）。v1.0.41 改为 prm/ft 回退查找，**全版本矩阵真正全覆盖**；--progress 断言增强（含表名 + --parallel 页进度>0）；
  ④ 回归：全版本全功能（PG 12-18 各编译 block size + 金仓 V8 8K/16K/32K + V9）通过后打包。

- **v1.0.40**：新增 3 个功能 + 修复 1 处元数据列错位 BUG：
  ① `--full-schema` 完整 Schema DDL（配合 `--ddl`，输出 `schema_objects.ddl`）：函数（CREATE OR REPLACE FUNCTION，解析 pg_proc 逐列还原，PG 全版本函数段读出）、触发器（CREATE TRIGGER，pg_trigger 逐列）、外键/CHECK 约束（pg_constraint，FK 引用列清单 + CHECK 表达式原文提取）、视图/CHECK 输出"占位注释 + 依赖清单"（离线不反编译 pg_node_tree 表达式，与 pg_dump 等价决策）；`--schema-objects LIST` 逗号分隔过滤类别（view,function,trigger,constraint,fk，默认全部）；
  ② `--large-objects` 导出 pg_largeobject 大对象（`lo_from_bytea` 单语句 + 2KB 分块拼接，`--large-object-oids` 指定 OID；联动 `--import-script` 生成导入脚本）。金仓适配：pg_class.relfilenode 存 OID 非 relfilenode → 优先内容探测标准物理文件 2613、失败回退 relfilenode 路径；金仓 pg_attribute 将系统列列为正 attnum → 大对象固定 3 列直解 + TOAST 4BC 重组；
  ③ `--progress` 导出进度与 ETA（stderr 输出：页/行/耗时/ETA；TTY 检测 `\r` 原地刷新、管道逐行；移除预扫双计数，进度精确 100%）；
  ④ **修复 BUG-A**：`parseNamedTuple` 在 hasOID=true 时把 pg_attribute 中 oid 系统行元数据列（attnum=1）当用户列重复读取，导致后续列错位 4B → PG 实例 pg_proc 用户函数全部读不出（boolin 读成 "in"）。修复：仅 PG 标准（!isKB）跳过 attnum=1 的 oid 元数据行，金仓保持原行为（金仓 hasoids 语义不同）。修复后 PG 全版本 full-schema 函数段全部读出；
  ⑤ **已知限制（金仓）**：金仓 V8/V9 用户函数（pg_proc）行字节在数据页中存在，但页头 item 目录不更新（金仓私有写入机制，页头 lower=0），`iterSysTuples` 枚举不到 → full-schema 金仓函数段=0。触发器/约束（含 FK）/视图依赖/大对象均正常；
  ⑥ 回归：全版本全功能（PG 12/13/14/15/16/17/18 × 各编译 block size + 金仓 V8 8K/16K/32K + V9 8K，含 SQL/CSV/full-schema/large-objects/progress + SPECIAL batch/checksum/report/jobs/import + TDE 常规导出，PASS=54 FAIL=0）通过后打包。
- **v1.0.39**：新增金仓 TDE 透明加密离线解密（独立模块 `tde/`，为后续 TDE 付费化留架构）+ **双版本发布**（免费版 / TDE 版，编译期 `-tags tde` 门控）：
  ① 自研 SM4-ECB/SM3 国密实现（修复密钥扩展缺 τ + 输出字节序，三向量自测全 PASS），不依赖外部库；
  ② `--enckey KEY`：表加密离线解密（`CREATE TABLE ... ENCRYPTED BY 'KEY'`，页 offset64+ SM4-ECB，TOAST 同密钥，串行/并行路径全覆盖）；
  ③ `--wallet-password PW` + `--wallet-dir DIR`（自动探测 `<PGDATA>/.wallet`）：表空间加密离线解密（钱包链 KEK→MK→TSPK 离线推导，无需密码；tspkey.kr 多表空间条目自动匹配）；
  ④ 全版本加密回归：金仓 V8R6（ORA 8K + PG 8K/16K/32K）与 V9R1C10（PG/ORA/MySQL × 8K/16K/32K 9 实例）+ V9 老版，共 14 实例 28/28 全绿（表加密 100 行含 TOAST 长文本 + 表空间加密 50 行明文验证）；
  ⑤ 边界说明：TDE 前提为实例 `shared_preload_libraries` 含 `sysencrypt`；V8 钱包密码 SM3 校验偏移与 V9 不同（仅影响 `--wallet-password` 校验告警，离线推导解密不受影响）。
- **v1.0.38**：新增 4 个功能（默认全关）+ 修复 3 处表空间解析 BUG：
  ① `--batch-rows N` 多值 INSERT（默认 1=关）：连续行合并 `INSERT ... VALUES (...),(...)`（deleted 行单独输出 `-- DELETED` 单条，与 `--parallel` 正交页序保序），恢复/导入提速；
  ② `--jobs N` 批量表间并行（默认 1=关）：`--table-name` 多表 / `--tables` / `--schema` / `--all-tables` 下 goroutine 池逐表并行、每表独立文件、失败独立记录不中断，输出与串行逐字节一致（自测 3 表/金仓 4 表 md5 全同）；
  ③ `--report` + `--checksum crc32|md5`（默认关）：`report.json`（表/格式/行数/坏块数/文件/字节/checksum/耗时/页大小/编码，jobs 并发安全），为救援导出提供完整性凭证；checksum 文件输出时流式计算（无 `-o` 打印 stderr JSON）；
  ④ `--import-script none|auto|sql|csv`（默认 none=关）：单表导入脚本 `import_<schema>_<table>.sh`（sql→`psql/ksql -f`、csv→`\copy` 联动 `--fields` 列清单、环境变量可覆盖 `PSQL`/`DB`/`PGCLIENTENCODING`）+ 批量 `import_all.sh`；
  ⑤ **修复**：`resolveDataRoot` 被 `base/<dboid>/PG_VERSION`（PG17+）或金仓 `base/<dboid>/SYS_VERSION` 误判为数据根 → PG17/18 与金仓表空间表无法导出；现按数据库目录特征判定（`pg_filenode.map`/`sys_filenode.map`/catalog `1247` 任一项即跳过继续向上，数据根以 `global/` 目录为准）；Options 默认 `DBOID:5` 写死导致库 OID≠5 时表空间定位/编码探测失效——dboid 改由 dbDir 目录名优先推导。修复验证：PG16 t_ts/t_ts2（表空间表）、金仓 V9 t_kts/t_kts2（表空间表）导出成功，金仓库编码探测恢复；
  ⑥ 文档同步：README/USER_GUIDE/`--help` 新增 5 参数说明与 **jobs×parallel 内存/CPU 资源计算**章节；全版本回归（参数面新增 batch/jobs/report/imp_script 断言 + 表空间断言覆盖 PG17/18 与金仓）通过后打包。
- **v1.0.37**：金仓 V9R1C10 x86 9 实例（ORA/PG/MySQL × 8/16/32KB）纳入全版本回归矩阵（参数面 + 大表专项 + 混合场景 + 分区专项）；修复 R1C10 `sys_description` 新增 `name` 列导致 COMMENT 乱码（注释解析改为动态探测 `sys_attribute` 真实列布局）。
- **v1.0.36**：`--table-name` 支持指定分区父表自动展开全部叶子子分区（v1.0.36）：
  ① **功能**：单表模式指定声明式分区父表（relkind='p'）时，递归展开其全部叶子子分区（relkind='r'，支持多级分区）——单个叶子保持单表语义（`-o` 可为文件）；多个叶子自动走批量导出（`-o` 需为目录 + 必须 `--sql`/`--data`）；`--table-name 父表,普通表` 混合场景中父表同样展开；空分区父表（无子分区）保持原行为（导出 0 行 + verbose 提示）；
  ② **实现**：`catalog.go` 新增 `pg_inherits`（金仓 `sys_inherits`）解析（`detectSysFile` 自动识别，布局取 `inhrelid`/`inhparent` 前两字段，PG14+ 末尾新增列不影响偏移），构建 `inhparent → 直接子表 OID` 映射，`TableMeta` 增加 `OID`/`ChildOIDs` 字段；`main.go` 新增 `expandParentToLeaves` 递归展开（防环 seen 集合），单表路径与 `runNamedTables` 批量路径两处接入；
  ③ **行为边界**：仅声明式分区（relkind='p'）展开；老式 INHERITS 继承父表（relkind='r'）不展开（保持逐表导出语义）；展开导出子表 DDL 时输出各子表 DDL（与 `--tables` 批量一致，父表 `PARTITION BY` DDL 不输出）；
  ④ 回归：数据面（全类型闭环）+ 参数面（PG 9 组合 × 20+ 断言 + 金仓 3 实例 × 26-27 断言）+ 分区展开专项（PG 9 组合 + 金仓 3 实例：多叶子父表批量、单叶子父表单表、父表+普通表混合、多级分区、空分区父表）全部通过后打包。
- **v1.0.35**：发布前代码审计修复（A1/A2 并行泄漏 → B1-B3 损坏数据 panic → C1 数组 "NULL" 失真 → D4 写盘错误静默）：
  ① **A1/A2（并发）**：`streamStdParallel`/`streamScanParallel` 引入 `sendPageResult`/`sendJob` 轮询式发送（select default + stop 轮询 + Gosched）——修复 `--limit` 达标主 goroutine 提前 return 后 workers/jobs 生产者永久阻塞的 goroutine 泄漏（批量多表 + `--limit` 逐表累积），并消除生产者"发前检查 stop"竞态窗口；`go vet` 干净；
  ② **B1-B3（损坏数据 panic）**：`decodeArray` 位图区越界（dataoffset/位图长度校验，失败返回原始字节 `E'\x...'` 兜底）、`decodeInet` 地址区越界（长度校验，失败返回空串）、`decodeRange` 定长边界越过 flags 字节（与变长分支校验对齐）——页头完好但字段数据被截断/损坏时跳过而非崩溃；
  ③ **C1（数组 "NULL" 失真）**：新增 `arrayNullSentinel`（`\x00PGSQL_ARRAY_NULL`，PG 文本不含 `\x00` 安全）区分真 NULL 与字符串元素；`arrayQuote` 对字符串 `"NULL"` 元素正常加引号——`{NULL,"NULL"}` 导入后 NULL/字符串不再混淆；
  ④ **D4（写盘错误静默）**：`exportOneTable` 写盘统一走 `write()` 闭包收集错误，DDL/count/SQL/DATA/序列同步/最终 Flush 任一失败均返回 `写入输出文件失败` 错误——磁盘满时不再"导出成功"但文件截断；
  ⑤ 回归：数据面（全类型闭环）+ 参数面（PG 9 组合 × 20 断言 + 金仓 3 实例 × 26-27 断言）+ 大表专项（100 万行表空间表）+ 混合场景补测全部通过后打包。
- **v1.0.34**：目录模式表空间自动发现（功能 3/2/1）：
  ① **功能 3**：`--datadir DIR --db-oid OID` 目录模式下 `--schema`/`--table-name`/`--tables`/`--all-tables` 自动发现表空间表——`classFields` 读取 `pg_class.reltablespace`（PG12/16/18 布局第 9 列，金仓同 PG12 布局），`TableMeta` 增加 `TableSpace`/`ToastTableSpace`；表文件定位：`reltablespace==0` 走原 `base/<oid>/<rf>` 路径（默认表空间行为零变化），非 0 解析 `pg_tblspc|<sys_tblspc>/<ts_oid>/<版本目录>/<dboid>/<rf>`（版本目录按"存在 <dboid> 子目录"探测，兼容同表空间多版本目录复用）；TOAST 按 toast 表自身表空间定位；软链接悬空/目录缺失给出明确报错；
  ② **功能 2**：`--list-tables-db`/`--list-tables-all` 输出新增"表空间"列（`0`=base，非 0=pg_tblspc/<oid>）；
  ③ **功能 1**：`--datadir DIR --list-db` 的 filepath 列多行显示（base 一行 + 每个表空间一行，`pg_tblspc`/`sys_tblspc` 自动识别；表空间目录为软链接时 `os.Stat` follow 判断）；
  ④ 入口校验放宽：`--datadir+--db-oid` 无需位置参数即可走单表（`--table-name`）/批量（`--schema`/`--tables`/`--all-tables`）导出；
  ⑤ 回归增强：`test_params_pg.sh` 断言扩至 **20 项**（新增 `tspace_big`/`tsdir_batch`/`listdb_multi`/`tspace_col`：第二个表空间 + 5 万行中文/特殊字符表空间大表 SQL+CSV 导入闭环、目录模式 `--schema` 批量含表空间表、`--list-db` 多行 filepath、`--list-tables-db` 表空间列），`test_params_kb.sh` 扩至 **26-27 项**；新增 `pgbuild/reg_bigts.sh` 大表+表空间专项（100 万行，SQL/CSV 闭环 + MaxRSS 采样）；
  ⑥ 表空间测试资产由 `/tmp` 迁至 `pgbuild/tblspc_data{2}`（PG）、`pgbuild/kb_tblspc_data{2}`（金仓），避免 `/tmp` 清理导致资产丢失。
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
