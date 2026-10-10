# pg2sql 用户手册 v1.0.33

> PostgreSQL / KingbaseES 堆文件离线解析导出工具（Go 版）
> 作者：raysuen · License：MIT

---

## 目录

- [1. 工具简介](#1-工具简介)
- [2. 快速上手](#2-快速上手)
- [3. 参数速查总表](#3-参数速查总表)
- [4. 参数详解](#4-参数详解)
  - [4.1 输入与定位](#41-输入与定位)
  - [4.2 导出类型](#42-导出类型)
  - [4.3 批量导出与对象列表](#43-批量导出与对象列表)
  - [4.4 数据定制](#44-数据定制)
  - [4.5 引擎控制](#45-引擎控制)
  - [4.6 输出与其他](#46-输出与其他)
- [5. 参数搭配矩阵](#5-参数搭配矩阵)
- [6. 场景专题示例](#6-场景专题示例)
- [7. 常见问题 FAQ](#7-常见问题-faq)
- [8. 限制与说明](#8-限制与说明)

---

## 1. 工具简介

**pg2sql** 直接解析 PostgreSQL / KingbaseES 数据目录中的**堆文件（heap file）**，不连接数据库、不依赖实例在线，即可离线导出：

- **DDL**：建表语句（含主键、索引、序列、默认值、注释）
- **INSERT / CSV**：表数据（全类型解码）
- **行数统计 / 已删除行审计**：数据页级信息

### 适用场景

| 场景 | 说明 |
| --- | --- |
| 数据库无法启动 / 数据文件离线 | 直接从数据目录导出 |
| 紧急恢复 | 从 `base/<dboid>/<relfilenode>` 文件抢救数据 |
| 迁移 / 归档 | 导出 SQL 或 CSV 到其他实例导入 |
| 审计 | 导出已删除行（`--deleted` / `--only-deleted`） |
| 在线库只读旁路 | 复制数据文件后离线解析（不影响生产） |

### 不适用场景

- 需要函数、触发器、视图、外键等完整对象定义的迁移（`--ddl` 只重建表结构，不重建函数/触发器/视图）
- 在线实例且数据未 CHECKPOINT 落盘（磁盘文件可能落后于内存，详见 [FAQ](#7-常见问题-faq)）

### 支持范围

| 维度 | 支持 |
| --- | --- |
| PG 版本 | 12 ~ 18（已全版本回归） |
| 金仓版本 | V8（ORA/MySQL/PG 三兼容模式）、V9 |
| 页大小 | 8KB / 16KB / 32KB（自动探测，可用 `--page-size` 指定） |
| 数据类型 | 数值/字符/二进制/布尔/位/日期时间/JSON/XML/数组/几何/网络/全文/范围/reg*/pg_lsn/txid_snapshot/枚举；金仓 MySQL 模式 TINYINT/MEDIUMINT/YEAR/DATETIME/BINARY/ENUM |
| 编码 | UTF8 / GBK / GB18030（默认自动探测，可用 `--encoding` 指定） |

---

## 2. 快速上手

```bash
# 1. 先找到目标表的数据文件（relfilenode）
ls /pgdata/base/16384/                    # 库 OID=16384，用户表的 relfilenode 文件名
pg2sql /pgdata/base/16384 --list-tables-db # 或用工具列出库内用户对象

# 2. 单表导出 INSERT
pg2sql /pgdata/base/16384/16391 --sql

# 3. 单表导出 DDL + INSERT
pg2sql /pgdata/base/16384/16391 --sql --ddl

# 4. 单表导出 CSV（首行字段名）
pg2sql /pgdata/base/16384/16391 --data --header -o /tmp/t1.csv

# 5. 指定表名 + 数据目录（自动发现表结构）
pg2sql /pgdata/base/16384 --table-name public.t1 --sql --ddl

# 6. 批量导出全部用户表
pg2sql /pgdata/base/16384 --tables --sql -o /tmp/export/

# 7. 导入验证
#   SQL:  \i /tmp/t1.sql          （psql/ksql 内执行）
#   CSV:  \copy t1 FROM '/tmp/t1.csv' WITH (FORMAT csv, HEADER true, DELIMITER ',', ENCODING 'UTF8', NULL '\N')
```

---

## 3. 参数速查总表

| 参数 | 类型 | 默认 | 作用 |
| --- | --- | --- | --- |
| `<数据文件>` | 位置参数 | — | 目标 relfilenode 文件，或 `base/<dboid>` 库目录 |
| `--catalog-json FILE` | 值 | 自动发现 | 使用离线元数据 JSON（`--export-meta` 产物） |
| `--datadir DIR` | 值 | 无 | PG 数据目录（自动发现 / 编码探测用） |
| `--db-oid OID` | 值 | 5 | 目标库 OID |
| `--table-name NAME[,NAME...]` | 值 | 无 | 指定表名（单表或多表，多表必须配 `-o` 目录） |
| `--ddl` | 开关 | 关 | 输出 DDL（建表/主键/索引/序列/默认值/注释） |
| `--sql` | 开关 | 关 | 输出 INSERT 语句 |
| `--data` | 开关 | 关 | 输出 CSV |
| `--count` | 开关 | 关 | 只统计行数，不输出数据 |
| `--deleted` | 开关 | 关 | 包含已删除行（`-- DELETED ctid` 注释） |
| `--only-deleted` | 开关 | 关 | 只导出已删除行 |
| `--list-db` | 开关 | 关 | 列出数据目录下的数据库 |
| `--list-tables-db` | 开关 | 关 | 列出库内用户对象（默认不列系统对象） |
| `--list-tables-all` | 开关 | 关 | 列出库内全部对象（含系统对象） |
| `--tables` | 开关 | 关 | 批量导出全部用户表 |
| `--all-tables` | 开关 | 关 | 批量导出全部表（含系统对象） |
| `--schema NAME` | 值 | 无 | 批量导出指定模式（独立使用等价 `--tables --schema NAME`） |
| `--export-meta` | 开关 | 关 | 导出元数据 JSON |
| `-o, --output PATH` | 值 | 当前目录/标准输出 | 输出文件或目录（目录时自动命名） |
| `--limit N` | 值 | 无限制 | 最多输出 N 行 |
| `--fields C1,C2` | 值 | 全部 | 只导出指定字段 |
| `--header` | 开关 | 关 | CSV 首行输出字段名 |
| `--complete-insert` | 开关 | 开 | INSERT 带列名（默认） |
| `--no-complete-insert` | 开关 | 关 | INSERT 省略列名 |
| `--delimiter CHAR` | 值 | `,` | CSV 分隔符 |
| `--toast FILE` | 值 | 自动 | 指定 TOAST 表文件（自动找 reltoastrelid） |
| `--page-size N` | 值 | 自动 | 页大小（8/16/32KB） |
| `--parallel N` | 值 | 串行 | 并发 worker 数（TOAST 索引预建 + 主表页分片行解析） |
| `--encoding CODEC` | 值 | auto | 库编码（UTF8/GBK/GB18030） |
| `--verbose` | 开关 | 关 | 详细日志 |
| `--version` | 开关 | — | 输出版本号 |

---

## 4. 参数详解

### 4.1 输入与定位

#### 位置参数 `<数据文件>`

两种形态：

```bash
# 形态 A：单个 relfilenode 文件（单表，最精确）
pg2sql /pgdata/base/16384/16391 --sql

# 形态 B：库目录 base/<dboid>（配合 --table-name / --tables 等）
pg2sql /pgdata/base/16384 --table-name public.t1 --sql
```

- **形态 A**：按数据文件名（relfilenode）定位表，无需 `--table-name`。
- **形态 B**：目录模式，需配合表定位参数（`--table-name` / `--tables` / `--all-tables` / `--schema`）；若目录 basename 恰等于某表 relfilenode 会静默命中该表（极小概率巧合，建议显式指定表名）。

#### `--datadir DIR` + `--db-oid OID`

数据目录与库 OID，供自动发现表结构、探测编码时定位系统目录文件：

```bash
pg2sql /pgdata/base/16384/16391 --datadir /pgdata --db-oid 16384 --sql
```

- `--db-oid` 默认 **5**（postgres 库），若目标库非 OID 5 建议显式指定。

#### `--catalog-json FILE`

离线元数据模式：先用 `--export-meta` 在可读环境导出表结构 JSON，再到离线环境使用（避免离线时自动发现失败）：

```bash
# 第一步（在线/可读环境）：
pg2sql /pgdata/base/16384 --export-meta -o meta.json
# 第二步（离线环境）：
pg2sql /pgdata/base/16384/16391 --catalog-json meta.json --table-name t1 --sql --ddl
```

#### `--table-name NAME[,NAME...]`

指定表名。**v1.0.27+ 支持双引号包裹**，消除含逗号标识符歧义：

```bash
# 1 张表
pg2sql /pgdata/base/16384 --sql --table-name s.t1

# 2 张表（引号外逗号=多表分隔）
pg2sql /pgdata/base/16384 --sql --table-name t1,t2 -o /out/

# 1 张表，表名本身含逗号（引号内逗号=表名的一部分）
pg2sql /pgdata/base/16384 --sql --table-name '"a,b"' -o /out/

# 混排：含逗号表名 a,b + 普通表 c（共 2 张）
pg2sql /pgdata/base/16384 --sql --table-name '"a,b",c' -o /out/

# 表名含双引号（"" 双写 = 字面 "，与 PG quote_ident 同规则）
pg2sql /pgdata/base/16384 --sql --table-name '"a""b"' -o /out/
```

规则：
- **引号内的逗号是表名的一部分**；引号外的逗号分隔多表。
- shell 层必须**单引号包双引号**（`'"a,b"'`），否则 shell 先吃掉双引号。
- 多表必须 `-o` 目录 + `--sql` 或 `--data`。
- 引号未闭合会报错 `表名引号未闭合`。
- 未匹配的表名精确报错（防拼错静默丢表）。

---

### 4.2 导出类型

四个互斥/可组合的导出类型：`--ddl`、`--sql`、`--data`、`--count`。

| 组合 | 输出 | 典型命令 |
| --- | --- | --- |
| `--sql` | 仅 INSERT | `pg2sql <数据文件> --sql` |
| `--ddl` | 仅 DDL（建表+主键+索引+序列+默认值+注释） | `pg2sql <数据文件> --ddl` |
| `--sql --ddl` | DDL + INSERT（重建导入闭环） | `pg2sql <数据文件> --sql --ddl` |
| `--data` | CSV | `pg2sql <数据文件> --data` |
| `--count` | 仅行数 | `pg2sql <数据文件> --count` |
| `--deleted` | 在输出中追加已删除行（`-- DELETED (ctid)` 注释） | `pg2sql <数据文件> --sql --deleted` |
| `--only-deleted` | 只输出已删除行 | `pg2sql <数据文件> --sql --only-deleted` |

示例：

```bash
pg2sql /pgdata/base/16384/16391 --count              # 输出行数
pg2sql /pgdata/base/16384/16391 --sql --deleted      # 正常行 + 已删除行（注释标注）
pg2sql /pgdata/base/16384/16391 --sql --only-deleted # 只导出死行（审计场景）
```

> 说明：`--deleted` / `--only-deleted` 仅对堆页中标记已删除、但尚未被 VACUUM 清理的行有效；已清理的死行物理上不存在，无法导出。

---

### 4.3 批量导出与对象列表

| 参数 | 行为 | 约束 |
| --- | --- | --- |
| `--list-db` | 列出数据目录下的数据库 | 需 `--datadir` |
| `--list-tables-db` | 列出库内**用户对象**（表/索引/序列/视图等，不含系统对象） | 位置参数为库目录 |
| `--list-tables-all` | 列出库内**全部对象**（含系统对象） | 位置参数为库目录 |
| `--tables` | 批量导出全部**用户表** | 必须 `-o` 目录 + `--sql`/`--data` |
| `--all-tables` | 批量导出全部表（**含系统表**） | 必须 `-o` 目录 + `--sql`/`--data` |
| `--schema NAME` | 批量导出指定模式的表（独立使用等价 `--tables --schema NAME`） | 多表必须 `-o` 目录 + `--sql`/`--data` |

示例：

```bash
pg2sql --datadir /pgdata --list-db                    # 列出数据库
pg2sql /pgdata/base/16384 --list-tables-db            # 库内用户对象
pg2sql /pgdata/base/16384 --list-tables-all           # 库内全部对象
pg2sql /pgdata/base/16384 --tables --sql -o /out/     # 全部用户表导出 SQL
pg2sql /pgdata/base/16384 --all-tables --data -o /out/# 全部表（含系统）导出 CSV
pg2sql /pgdata/base/16384 --schema ray --sql -o /out/ # 只导出 ray 模式
```

批量导出时逐表独立文件，命名 `schema.table.sql` / `schema.table.csv`（含逗号表名时文件名含逗号，如 `public.a,b.sql`）。

---

### 4.4 数据定制

#### `--fields C1,C2` —— 指定字段

```bash
pg2sql /pgdata/base/16384/16391 --sql --fields id,name        # 只导 id、name
pg2sql /pgdata/base/16384/16391 --data --fields id,name -o x.csv
```

#### `--limit N` —— 限制行数

```bash
pg2sql /pgdata/base/16384/16391 --sql --limit 1000    # 只导前 1000 行
```

#### `--header` —— CSV 首行字段名

```bash
pg2sql /pgdata/base/16384/16391 --data --header -o out.csv
# out.csv 首行: id,col1,col2,...
# 导入:
# \copy t1 FROM 'out.csv' WITH (FORMAT csv, HEADER true, DELIMITER ',', ENCODING 'UTF8', NULL '\N')
```

#### `--complete-insert` / `--no-complete-insert` —— INSERT 是否带列名

```bash
pg2sql <数据文件> --sql --complete-insert     # INSERT INTO "s"."t" ("id","name") VALUES (...);（默认）
pg2sql <数据文件> --sql --no-complete-insert  # INSERT INTO "s"."t" VALUES (...);
```

#### `--delimiter CHAR` —— CSV 分隔符

```bash
pg2sql <数据文件> --data --delimiter '|' -o out.csv    # 竖线分隔
pg2sql <数据文件> --data --delimiter $'\t' -o out.csv  # Tab 分隔
```

#### `--encoding CODEC` —— 库编码

```bash
pg2sql <数据文件> --data --encoding gbk -o out.csv     # 按 GBK 输出中文
pg2sql <数据文件> --sql --encoding gb18030             # 按 GB18030 输出
pg2sql <数据文件> --sql                                # 默认 auto（自动探测）
```

支持：`auto`（默认）、`utf8`/`UTF8`、`gbk`、`gb18030`。中文/特殊字符（单引号、双引号、反斜杠、逗号、换行、Tab、NULL）在 SQL/CSV 双通道导出→导入均验证通过。

---

### 4.5 引擎控制

#### `--page-size N`

```bash
pg2sql <数据文件> --sql --page-size 16384   # 强制 16KB 页解析（一般无需，自动探测 8/16/32KB）
```

用于：页大小探测失败、或离线元数据不完整时手工指定。支持 8 / 16 / 32（单位 KB）。

#### `--parallel N` —— 并发加速

```bash
pg2sql <数据文件> --sql --parallel 4 -o out.sql   # 4 worker：TOAST 索引预建并行 + 主表按页分片 解析→转义→产出字符串
```

- 大表（数十万行以上）显著提速，实测 8 核以上机器加速比约 **2.5~3x**（转义随解析并行，此前转义为单 goroutine，是大表 ~90% 耗时瓶颈）。
- **输出与串行模式逐字节一致**（worker 池 + 页序滑动窗口保序转发），不影响导入正确性。
- **内存 O(窗口×并行度) 常数级**：流式产出、页序窗口转发，不做全量收集——大表不再触发 OOM（50 列 100 万行旧实现约 3.5-4GB，v1.0.30 起实测恒 **17MB**）。
- TOAST 索引预建共享，多表/并发场景避免重复扫描。
- 内部先做一遍轻量预扫判定（只判 NULL/空串/TOAST 缺失，不构建行），再按标准 ItemId 或数据区扫描流式输出，两阶段回退语义与历史版本完全一致。

#### `--toast FILE`

```bash
pg2sql <数据文件> --sql --toast /pgdata/base/16384/12345   # 手工指定 TOAST 表文件
```

一般不指定：工具自动按 `reltoastrelid` 找 TOAST 文件；仅当自动定位失败（如元数据缺失）时使用。

#### `--verbose`

```bash
pg2sql <数据文件> --sql --verbose
```

输出详细日志：自动发现表结构、TOAST 索引预建进度、逐表输出进度、行数等。

---

### 4.6 输出与其他

#### `-o, --output PATH`

```bash
# 单表 + 文件路径：直接写到指定文件
pg2sql <数据文件> --sql -o /tmp/t1.sql

# 单表 + 目录：自动命名 schema.table.sql|.csv|.ddl
pg2sql <数据文件> --sql -o /tmp/export/          # → /tmp/export/public.t1.sql

# 多表/批量：必须目录
pg2sql /pgdata/base/16384 --tables --sql -o /tmp/export/

# 无 -o：单表输出到标准输出（管道/重定向自行处理）
pg2sql <数据文件> --sql
```

#### `--version`

```bash
pg2sql --version    # pg2sql 1.0.28
```

---

## 5. 参数搭配矩阵

| 需求 | 推荐命令 | 关键点 |
| --- | --- | --- |
| 单表全量迁移 | `pg2sql <数据文件> --sql --ddl` | DDL+INSERT 一条龙 |
| 单表数据入现有表 | `pg2sql <数据文件> --sql` | 目标表需已存在 |
| 单表 CSV 给外部系统 | `pg2sql <数据文件> --data --header -o out.csv` | 首行字段名，COPY 兼容 |
| 指定字段导出 | `pg2sql <数据文件> --sql --fields id,name` | 可与 --data 组合 |
| 只导部分行 | `pg2sql <数据文件> --sql --limit 10000` | 可与 --fields 组合 |
| 多表导出 | `pg2sql base --table-name t1,t2 --sql -o out/` | 必须 -o 目录 + 类型 |
| 含逗号表名 | `pg2sql base --table-name '"a,b"' --sql -o out/` | shell 单引号包双引号 |
| 全部用户表 | `pg2sql base --tables --sql -o out/` | 自动逐表独立文件 |
| 指定模式 | `pg2sql base --schema ray --sql -o out/` | 等价 --tables --schema |
| 含系统表 | `pg2sql base --all-tables --data -o out/` | 谨慎，量大 |
| 大表加速 | `pg2sql <数据文件> --sql --parallel 4 -o out.sql` | 输出与串行一致 |
| 已删除行审计 | `pg2sql <数据文件> --sql --only-deleted` | 仅未 VACUUM 的死行 |
| 行数统计 | `pg2sql <数据文件> --count` | 不输出数据 |
| 离线环境 | `pg2sql <数据文件> --catalog-json meta.json --table-name t1 --sql` | 先用 --export-meta |
| 指定编码 | `pg2sql <数据文件> --data --encoding gbk -o out.csv` | 中文不乱码 |
| 指定页大小 | `pg2sql <数据文件> --sql --page-size 32768` | 自动探测失败时兜底 |

**约束速记**：
- 多表/批量（`--table-name` 多表、`--tables`、`--all-tables`、`--schema`）**必须** `-o` 目录 + `--sql` 或 `--data`。
- `--sql` / `--data` / `--count` 互斥选择其一作为数据形态；`--ddl` 可与 `--sql` 组合。
- `--header`、`--delimiter`、`--fields`、`--limit` 主要作用于 CSV/数据输出。

---

## 6. 场景专题示例

### 6.1 分区表（按分区导出）

叶子分区各自独立 relfilenode，父表无独立存储：

```bash
pg2sql /pgdata/base/16384 --list-tables-db        # 找到各分区 relfilenode
pg2sql /pgdata/base/16384/10420111 --sql --ddl    # 导出 sales_2023
pg2sql /pgdata/base/16384/10420113 --sql --ddl    # 导出 sales_2024
# 或按表名逐分区
pg2sql /pgdata/base/16384 --table-name public.sales_2023 --sql --ddl -o out/
```

导入：先建父表+子分区，再逐分区 `\i`。父表本身 relfilenode=0，无可导出数据（直接导出会报未找到）。

### 6.2 表空间不在默认数据目录

表空间目录结构：`<tablespace>/PG_<主版本>_<目录版本号>/<dboid>/<relfilenode>`（金仓为 `sys_tblspc/` + `SYS_<目录版本号>`）。

**推荐：软链接路径自动发现（v1.0.32 起，免额外参数）**。`pg_relation_filepath()` 返回数据目录相对路径（含 `pg_tblspc/` 软链接前缀），拼上数据目录即可直接导出，工具自动从路径识别数据根目录与库 OID、读取 catalog：

```bash
# 获取相对路径（实例在线时）
SELECT pg_relation_filepath('t_ts');   -- → pg_tblspc/16391/PG_17_202307071/16384/16391
# 软链接路径直接导出（无需 --datadir/--db-oid）
pg2sql /pgdata/pg_tblspc/16391/PG_17_202307071/16384/16391 --sql
```

**兜底：绝对真实路径 + `--datadir`/`--db-oid`**。表空间软链接悬空、目标目录被删、或只拿到真实目录路径（不含 `pg_tblspc/` 前缀）时，工具无法从路径反推数据目录，需显式提供 catalog 定位：

```bash
# 手工定位真实文件路径后，配合数据根目录与库 OID
pg2sql /ts_data/PG_17_202307071/16384/16391 --datadir /pgdata --db-oid 16384 --sql
```

### 6.3 大字段（TOAST）

超长字段自动走 TOAST 重组，无需额外参数；`--parallel` 可加速 TOAST 索引预建：

```bash
pg2sql <数据文件> --sql --parallel 4 -o out.sql
```

TOAST 文件非空但索引 0 valueid（实例在线且未 CHECKPOINT）时输出警告，需 CHECKPOINT 后重试。

### 6.4 坏块

- 数据页校验失败：跳过坏页并输出警告，**不中断导出**（能导出的页正常导出）。
- TOAST 链断裂：该字段输出 `NULL` 占位（`__TOAST_MISSING__` 日志），其余字段不受影响。
- 目标表元数据读取失败：自动发现跳过并提示（可 `--catalog-json` 离线元数据兜底）。

### 6.5 金仓透明加密数据文件

**不支持**：金仓透明加密（TDE）数据文件为加密存储，pg2sql 无法直接解密导出。需在金仓实例内解密（关闭 TDE 或导出明文副本）后再用本工具解析。

### 6.6 在线实例一致性

实例在线且最近有写入时，磁盘文件可能落后于内存（未 CHECKPOINT）——表现为"未找到目标表"或 TOAST 0 valueid。先执行：

```sql
CHECKPOINT;
```

再导出。或对数据目录做一致性快照（`pg_basebackup` / 停库拷贝）后离线解析。

---

## 7. 常见问题 FAQ

**Q1：报"未找到目标表，请用 --table-name 指定"？**
A：多为磁盘 `sys_class/pg_class` 与数据文件不一致——先 `CHECKPOINT` 重试；仍失败则用 `--table-name` 显式指定，或 `--catalog-json` 离线元数据。

**Q2：目录模式 + 不传表名会怎样？**
A：basename（库 OID 数字）若恰等于某表 relfilenode 会静默导出该表（极小概率）；否则报"未找到目标表"。建议显式 `--table-name`。

**Q3：导出 CSV 导入报"extra data after last expected column"？**
A：CSV 列数与表实际列数不一致——常见于目标表已存在且结构不同，或 `--fields` 与导入列清单不匹配。核对表结构，或用 `\copy t (列清单) FROM ...` 精确指定。

**Q4：中文乱码？**
A：指定 `--encoding gbk` / `gb18030`（库编码非 UTF8 时）；默认 auto 自动探测。

**Q5：金仓 MySQL 模式的 BIT 大长度（如 4655）报错？**
A：已修复支持（v1.0.17+），按实际 bit 长度解码；如仍异常确认页大小与版本匹配。

**Q6：--parallel 输出会和串行不一致吗？大表会不会 OOM？**
A：输出不会不一致——worker 池 + 页序滑动窗口保序转发，输出与串行逐字节一致（已回归验证 md5 一致）。OOM 风险在 v1.0.30 起消除：流式产出、内存 O(窗口×并行度) 常数级（50 列 100 万行实测 Max RSS 恒 17MB（与表大小无关），旧实现约 3.5-4GB）。

**Q7：--table-name "a,b"（shell 双引号）怎么变成 2 张表了？**
A：shell 双引号只合并 argv，程序收到裸 `a,b`，按逗号拆成 2 张表。要表达"表名含逗号"，用 shell 单引号包双引号：`'"a,b"'`。

**Q8：导出的 SQL 里位串/大对象导入报长度超限？**
A：确认目标表列类型与源一致（如 BIT(16) vs 默认 64）；金仓/版本差异时核对类型映射。

---

## 8. 限制与说明

- `--ddl` 重建**表结构**（列/主键/索引/序列/默认值/注释），不重建函数、触发器、视图、外键约束、扩展。
- 在线实例建议先 `CHECKPOINT` 或使用一致性快照，避免磁盘与内存不一致。
- 金仓透明加密（TDE）文件不支持直接导出。
- 复合类型（record）输出原始字节 `E'\x...'` 兜底（字节可逆，不可文本化）；金仓 MySQL 模式 SET 输出 `\x` hex。
- 系统表导出（`--all-tables`）量大且包含内部状态，导入目标库需谨慎。
- 坏块/截断仅影响对应页或字段（NULL 占位），不中断整体导出，结果中会输出警告日志。

---

*本手册对应 v1.0.29；每次版本变更随 README 更新记录同步。*
