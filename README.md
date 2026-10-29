# pg2sql v1.0.63

**语言：中文 | [English](README.en.md)**

PostgreSQL / KingbaseES 数据文件离线解析导出工具（Go 版，单文件零依赖）。

> 📖 **详细使用手册**：[`docs/USER_GUIDE.md`](docs/USER_GUIDE.md) —— 每个参数详解、参数搭配矩阵、场景专题（分区表/表空间/TOAST/坏块/编码）、FAQ。执行包解压后为 `pg2sql-版本号-平台/` 目录（内含 `pg2sql` 二进制与 `docs/` 手册目录，一同分发）。

直接读取 PG/金仓堆文件（含 TOAST），无需数据库在线即可导出：

- `--sql`：INSERT 语句（自动发现表结构，含 TOAST 重组/已删除行审计；配合 `--ddl` 输出建表语句）
- `--data`：CSV（`--header` 首行字段名，`--delimiter` 自定义分隔符）
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
| 编码 | 客户端编码协商 | 导出文件恒 UTF-8；**`.sql` 文件首行内嵌 `SET client_encoding = 'UTF8';`**（v1.0.54），`--import-script` 另注入 `PGCLIENTENCODING=UTF8`，两者任一即可正确回灌 |
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
