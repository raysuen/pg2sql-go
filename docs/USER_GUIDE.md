# pg2sql 用户手册 v1.0.42

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
| 金仓版本 | V8（ORA/MySQL/PG 三兼容模式）、V9（含 R1C10 / R3C18 细分版本，均全版本回归） |
| 页大小 | 8KB / 16KB / 32KB（自动探测，可用 `--page-size` 指定） |
| 数据类型 | 数值/字符/二进制/布尔/位/日期时间/JSON/XML/数组/几何/网络/全文/范围/reg*/pg_lsn/txid_snapshot/枚举；金仓 MySQL 模式 TINYINT/MEDIUMINT/YEAR/DATETIME/BINARY/ENUM |
| 编码 | UTF8 / GBK / GB18030（默认自动探测 + **v1.0.42 字节特征回退**，可用 `--encoding` 指定） |

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
| `--datadir DIR` | 值 | 无 | PG 数据目录（自动发现 / 编码探测 / 目录模式基础；配 `--db-oid` 等效 `base/<oid>`，v1.0.34 起自动发现表空间表） |
| `--db-oid OID` | 值 | 5 | 目标库 OID（目录模式：`--list-tables-db`/`--schema`/`--table-name`/`--tables`/`--all-tables` 自动定位表空间表，v1.0.34） |
| `--table-name NAME[,NAME...]` | 值 | 无 | 指定表名（单表或多表，多表必须配 `-o` 目录） |
| `--ddl` | 开关 | 关 | 输出 DDL（建表/主键/索引/序列/默认值/注释） |
| `--sql` | 开关 | 关 | 输出 INSERT 语句 |
| `--data` | 开关 | 关 | 输出 CSV |
| `--count` | 开关 | 关 | 只统计行数，不输出数据 |
| `--deleted` | 开关 | 关 | 包含已删除行（`-- DELETED ctid` 注释） |
| `--only-deleted` | 开关 | 关 | 只导出已删除行 |
| `--list-db` | 开关 | 关 | 列出数据目录下的数据库（v1.0.34 起 filepath 列多行：base 一行 + 每表空间一行） |
| `--list-tables-db` | 开关 | 关 | 列出库内用户对象（默认不列系统对象；v1.0.34 起含"表空间"列，0=base） |
| `--list-tables-all` | 开关 | 关 | 列出库内全部对象（含系统对象；同样含"表空间"列） |
| `--tables` | 开关 | 关 | 批量导出全部用户表 |
| `--all-tables` | 开关 | 关 | 批量导出全部表（含系统对象） |
| `--schema NAME` | 值 | 无 | 批量导出指定模式（独立使用等价 `--tables --schema NAME`） |
| `--export-meta` | 开关 | 关 | 导出元数据 JSON |
| `-o, --output PATH` | 值 | 当前目录/标准输出 | 输出文件或目录（目录时自动命名） |
| `--limit N` | 值 | 无限制 | 最多输出 N 行（**0=不限制，默认**；v1.0.42 文档化） |
| `--fields C1,C2` | 值 | 全部 | 只导出指定字段 |
| `--header` | 开关 | 关 | CSV 首行输出字段名 |
| `--complete-insert` | 开关 | 开 | INSERT 带列名（默认） |
| `--no-complete-insert` | 开关 | 关 | INSERT 省略列名 |
| `--delimiter CHAR` | 值 | `,` | CSV 分隔符 |
| `--toast FILE` | 值 | 自动 | 指定 TOAST 表文件（自动找 reltoastrelid） |
| `--page-size N` | 值 | 自动 | 页大小（8/16/32KB） |
| `--parallel N` | 值 | 串行 | 并发 worker 数（TOAST 索引预建 + 主表页分片行解析） |
| `--jobs N` | 值 | 1 | 批量表间并行（`--table-name` 多表/`--tables`/`--schema`/`--all-tables`，每表独立文件；v1.0.38） |
| `--batch-rows N` | 值 | 1 | 多值 INSERT 批大小（N≥2 合并 `INSERT ... VALUES (...),(...)`，默认 1=逐行；v1.0.38） |
| `--report` | 开关 | 关 | 导出报告 `report.json`（表/格式/行数/坏块数/文件/字节/checksum/耗时；v1.0.38） |
| `--checksum crc32\|md5` | 值 | 无 | 输出校验和（与 `--report` 配合，流式计算；v1.0.38） |
| `--import-script none\|auto\|sql\|csv` | 值 | none | 生成导入辅助脚本（sql→`psql/ksql -f`，csv→`\copy` 联动 `--fields`；v1.0.38） |
| `--enckey KEY` | 值 | 无 | 金仓 TDE 表加密密钥（`CREATE TABLE ... ENCRYPTED BY 'KEY'`；页 offset64+ SM4-ECB 离线解密，TOAST 同密钥；v1.0.39） |
| `--wallet-password PW` | 值 | 无 | 金仓 TDE 表空间加密钱包密码（密钥链 KEK→MK→TSPK 离线推导本不需密码，密码仅做 SM3 校验并告警；v1.0.39） |
| `--wallet-dir DIR` | 值 | 自动 | 钱包目录（默认自动探测 `<PGDATA>/.wallet`；需 masterkey.kr/tspkey.kr，多表空间条目自动匹配；v1.0.39） |
| `--encoding CODEC` | 值 | auto | 库编码（UTF8/GBK/GB18030）；auto 在声明非 UTF8 时自动做 UTF8 字节特征回退（v1.0.42） |
| `--full-schema` | 开关 | 关 | 完整 Schema DDL（需 `--ddl`）：函数/触发器/外键/CHECK/视图依赖清单，输出 `schema_objects.ddl`（v1.0.40） |
| `--schema-objects LIST` | 值 | 全部 | 过滤 Schema 对象类别（逗号分隔：view,function,trigger,constraint,fk；v1.0.40） |
| `--large-objects` | 开关 | 关 | 导出 pg_largeobject 大对象（`lo_from_bytea` 语句；需库目录；v1.0.40） |
| `--large-object-oids LIST` | 值 | 全部 | 只导出指定大对象 OID（逗号分隔；v1.0.40） |
| `--progress` | 开关 | 关 | 导出进度与 ETA（stderr 输出：**当前表名**/页/行/耗时/ETA；TTY 原地刷新，管道逐行；v1.0.41） |
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

> **v1.0.42：auto 的字节特征回退**（金仓"标 GBK 实存 UTF8"场景）
>
> 实测金仓 V8 建 `ENCODING 'GBK'` 库插入含扩展字符文本（如 `编码𠀀堃测试`）时，库内**实际以 UTF8 字节存储**（hex `f0 a0 80 80` 证实），但 `pg_database.encoding=GBK`。旧版按声明 GBK 解码会导出乱码 `'ç¼ç ð åµè¯'`。
>
> v1.0.42 起：auto 探测到非 UTF8 声明（GBK/GB18030 等）时，**采样用户表数据字节做 UTF8 特征检测**（逐文件独立判定，跳过系统目录文件）——呈 UTF8 特征则自动切换 UTF8 并提示：
>
> ```text
> [pg2sql] 提示: 库声明编码 gbk 但数据字节呈 UTF8 特征，已自动切换 UTF8（如非预期可用 --encoding gbk 强制）
> ```
>
> 实测回退后导出与在线 100% 一致；**真 GBK/GB18030 双字节库不会被误判**（双字节流 UTF8 合法率 <0.5，实测纯 GBK 字节 ratio=0.0）。如确需按声明编码解码，用 `--encoding gbk` 强制即可。
>
> **v1.0.44：P2 破坏性场景修复轮**
>
> - **表空间文件纳入编码探测**：TABLESPACE 库（数据在 `PGDATA/pg_tblspc`）原先采样不到、编码判定缺失；修复后 V8 32K 表空间库正确回退 UTF8、V9R1C10 表空间库按真 GBK 不回退，均与在线一致。
> - **坏页明细**：`--report` 的 report.json 与 stderr 输出 `bad_blocks`、`bad_pages`（具体坏页号），坏页行跳过保留统计。
> - **TOAST 文件缺失计数**：TOAST 文件缺失时不再静默输出 NULL 无计数——缺失也建空 ToastFile，`toast_missing` 计数生效。
> - **DDL 目录缺失警告**：`--ddl` 依赖目录/文件缺失时输出明确警告（原静默）。
> - **修复 4 个健壮性 BUG**：pg_index 空路径 panic、pageSize=0 越界 panic、tryAutoLayout 短 buf 越界、TOAST 缺失静默丢数。
>
> **v1.0.43：模式实测结论修正 + 32K 页采样修复**
>
> - 补测 V8 ORA/PG/MySQL 三模式 + **V9R1C10 三模式×8K/16K/32K 全 9 实例**：**V8 三模式标 GBK 均实存 UTF8**（含 `client_encoding=GBK` 插入，落盘 hex `f0 a0 80 80` 证实）；**V9 全系标 GBK 实存真 GBK 双字节**（落盘 `b1 e0`=编、`c6 d5`=普，生僻字插入时即被替换为 `?`）——"金仓 GBK 恒存 UTF8"**不成立，按大版本而异：V8 实存 UTF8、V9 实存真 GBK**。auto 双向验证正确：V8 库回退、V9 真 GBK 库不回退（按 GBK 解码与在线一致，仅 `--encoding utf8` 强制才乱）。
> - 修复 32K 页漏回退：PG 堆页行数据在**页尾**，原采样读"文件前 16384 字节"在 32K 页下只有页头+空闲区、采样不到数据 → 漏回退乱码（V8 ORA 32K 实测抓到）。v1.0.43 改为**读文件末尾一页**（`ReadAt(size-pageSize)`），8K/16K/32K 全部正确触发。

#### `--batch-rows N` —— 多值 INSERT（v1.0.38）

```bash
pg2sql <数据文件> --sql --batch-rows 1000 -o out.sql   # 连续行合并为 INSERT ... VALUES (...),(...);
pg2sql <数据文件> --sql                                # 默认 1=逐行单条（行为同 v1.0.37 及以前）
```

- N≥2 时把连续行攒批为一条多值 INSERT，**导入/恢复提速**（批越大客户端往返越少）。
- 输出仍保序（与 `--parallel` 正交可叠加），deleted 行单独输出含 `-- DELETED` 注释的单条语句，不破坏多值语法。
- 报告 `--report` 的 rows 字段统计实际数据行数（多值合并后语句数 ≠ 行数）。
- 默认 1 保持逐行输出，**不影响任何既有行为**。

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

#### `--jobs N` —— 批量表间并行（v1.0.38）

```bash
pg2sql /pgdata/base/16384 --tables --sql --jobs 4 -o /out/           # 全部用户表 4 路并行
pg2sql /pgdata/base/16384 --sql --table-name t1,t2,t3 --jobs 4 -o /out/  # 指定多表并行
pg2sql /pgdata/base/16384 --schema ray --sql --jobs 4 -o /out/       # 指定模式并行
```

- 仅对**批量导出**（`--table-name` 多表 / `--tables` / `--schema` / `--all-tables`）生效，每表独立文件；默认 1=逐表串行（行为同 v1.0.37 及以前）。
- 与 `--parallel`（表内页级并行）**正交叠加**：`jobs × parallel ≈ 核数`；输出与串行逐字节一致（已回归 md5 全同）。
- 内存/CPU 估算见 [6.7 多表并行与表内并行资源计算](#67-多表并行与表内并行资源计算)。
- 失败独立记录不中断（批量收尾输出"成功 N 张表, 未匹配 N, 失败 N"）。

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
pg2sql --version    # pg2sql 1.0.38
```

#### `--report` + `--checksum crc32|md5` —— 导出报告与校验和（v1.0.38）

```bash
pg2sql /pgdata/base/16384 --tables --sql --jobs 4 --report --checksum md5 -o /out/
pg2sql <数据文件> --sql --report --checksum crc32 -o out.sql
```

- `--report`（默认关）：在输出目录写 `report.json`——工具/版本/库编码/页大小/jobs，每表：表名、格式、行数、坏块数、输出文件、字节、checksum、耗时。无 `-o` 时打印到 stderr。
- `--checksum crc32|md5`：文件输出时**边写边算**（流式，不额外占内存）；坏块表行数与坏块数如实反映，为救援导出提供**完整性凭证**（导入后可核对 checksum 验证数据未变）。
- 两个参数可独立使用（`--report` 不带 checksum 时 report 内无 checksum 字段）。

#### `--import-script none|auto|sql|csv` —— 导入辅助脚本（v1.0.38，默认 none=关）

```bash
pg2sql <数据文件> --sql --import-script auto -o /tmp/          # 生成 import_<schema>_<table>.sh（psql/ksql -f）
pg2sql <数据文件> --data --fields id,name --import-script csv -o /tmp/  # \copy 脚本联动 --fields 列清单
pg2sql /pgdata/base/16384 --tables --sql --import-script sql -o /out/    # 批量：额外生成 import_all.sh
```

脚本内容：sql→`psql -U <owner> -d <db> -f <文件>`；csv→`\copy <表> FROM '<文件>' WITH (...)`（含 `--header`/`--delimiter`/`--encoding` 联动）。环境变量可覆盖：`PSQL`（金仓设 `PSQL=ksql`）、`DB`（目标库名）、`PGCLIENTENCODING`（导入端编码）。

#### `--full-schema` + `--schema-objects` —— 完整 Schema DDL（v1.0.40，默认关）

```bash
# 需配合 --ddl；输出 schema_objects.ddl（无 -o 时追加到 stdout 末尾）
pg2sql /pgdata/base/16384 --ddl --full-schema -o schema_objects.ddl
# 只导外键 + 函数
pg2sql /pgdata/base/16384 --ddl --full-schema --schema-objects fk,function -o fk_fn.ddl
```

- **函数**：解析 pg_proc 逐列还原，输出 `CREATE OR REPLACE FUNCTION ...`（PG 全版本函数段读出）。
- **触发器**：解析 pg_trigger，输出 `CREATE TRIGGER ... EXECUTE FUNCTION ...`。
- **外键**：解析 pg_constraint，输出 `ALTER TABLE ... ADD CONSTRAINT ... FOREIGN KEY (...) REFERENCES ...`。
- **CHECK / 视图**：输出**占位注释 + 依赖清单**（`-- 视图 view 依赖: ...`），离线不反编译 pg_node_tree 表达式（与 pg_dump 等价决策，工作量级相同）。
- `--schema-objects` 逗号分隔过滤：`view,function,trigger,constraint,fk`（默认全部）。
- **金仓限制**：金仓 V8/V9 用户函数 pg_proc 行字节在数据页存在但页头 item 目录不更新（金仓私有写入机制），离线枚举不到 → 金仓函数段=0。触发器/外键/CHECK/视图依赖/大对象均正常。

#### `--large-objects` + `--large-object-oids` —— 大对象导出（v1.0.40，默认关）

```bash
# 导出库内全部大对象（lo_from_bytea 语句）
pg2sql /pgdata/base/16384 --large-objects -o large_objects.sql
# 只导出指定 OID
pg2sql /pgdata/base/16384 --large-objects --large-object-oids 990001 -o lob.sql
# 联动导入辅助脚本
pg2sql /pgdata/base/16384 --large-objects --import-script auto -o /tmp/
```

- 读取 pg_largeobject，大对象数据 >2KB 自动分块拼接为 `lo_from_bytea('...')` 单语句。
- **导入**：`psql/ksql -d <db> -f large_objects.sql`（自动创建大对象并写入数据；导入脚本为 `import_lob.sh`）。
- 金仓适配：自动探测标准物理文件 2613（pg_class.relfilenode 存 OID 非 relfilenode），系统列布局差异自动处理。

#### `--progress` —— 导出进度与 ETA（v1.0.41，默认关）

```bash
pg2sql <数据文件> --sql --progress -o out.sql          # 单表
pg2sql base --tables --sql --jobs 4 --progress -o out/  # 多表批量：每表独立进度
```

- **stderr 输出**（stdout 保持纯净数据通道，管道/重定向不受污染）。
- TTY 下 `\r` 原地刷新显示 `表名/页/行/耗时/ETA`；管道下逐行输出。
- **显示当前表名**：`[pg2sql] 进度 41.3% 表 public.huge_a 页 9385/22728 行 825774 耗时 1.0s ETA 1.4s`——多表批量（`--jobs` 表间并行）每张表独立进度并标出表名，一眼可辨当前导哪张表。
- **与 `--parallel` 兼容**（v1.0.41 修复）：标准并行路径补齐页计数埋点，并行导出页进度正常递增（2 百万行表 `--parallel 4` 实测 41.3%→80.7%→100.0%）。
- 进度 = 已解析页 / 总页（含 TOAST），移除预扫双计数后精确到 100%。
- 与 `--verbose` 同开：TTY 下进度 `\r` 会覆盖 verbose 日志行（叠行），建议终端二选一；管道/日志落盘场景可同开（逐行交错、完整可读）。

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
| 多表批量加速 | `pg2sql base --tables --sql --jobs 4 -o out/` | 表间并行，每表独立文件；可再叠 `--parallel` |
| 恢复提速 | `pg2sql <数据文件> --sql --batch-rows 1000 -o out.sql` | 多值 INSERT，导入往返少 |
| 救援完整性凭证 | `pg2sql base --tables --sql --jobs 4 --report --checksum md5 -o out/` | report.json + 校验和，坏块数可审计 |
| 一键导入 | `pg2sql <数据文件> --sql --import-script auto -o out/` | 生成 psql/ksql 导入脚本 |
| 已删除行审计 | `pg2sql <数据文件> --sql --only-deleted` | 仅未 VACUUM 的死行 |
| 行数统计 | `pg2sql <数据文件> --count` | 不输出数据 |
| 离线环境 | `pg2sql <数据文件> --catalog-json meta.json --table-name t1 --sql` | 先用 --export-meta |
| 指定编码 | `pg2sql <数据文件> --data --encoding gbk -o out.csv` | 中文不乱码 |
| 指定页大小 | `pg2sql <数据文件> --sql --page-size 32768` | 自动探测失败时兜底 |
| 完整 Schema DDL | `pg2sql base --ddl --full-schema -o schema_objects.ddl` | 函数/触发器/外键/CHECK/视图依赖（需 --ddl） |
| 只导部分 Schema 对象 | `pg2sql base --ddl --full-schema --schema-objects fk,function -o fk_fn.ddl` | 逗号分隔类别 |
| 大对象导出 | `pg2sql base --large-objects -o large_objects.sql` | lo_from_bytea 语句，>2KB 自动分块 |
| 指定大对象 | `pg2sql base --large-objects --large-object-oids 990001 -o lob.sql` | 逗号分隔 OID |
| 大表进度可视化 | `pg2sql <数据文件> --sql --progress -o out.sql` | stderr 进度+ETA，TTY 原地刷新 |

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

**指定分区父表自动展开（v1.0.36 起）**：`--table-name` 传声明式分区父表（relkind='p'）时，自动递归展开全部叶子子分区（支持多级分区）：

```bash
# 父表 sales → 展开 sales_2023..2026 批量导出（多叶子，-o 需为目录 + --sql/--data）
pg2sql /pgdata/base/16384 --sql --table-name public.sales -o /out/
# 父表仅 1 个叶子子分区 → 保持单表语义（-o 可为文件）
pg2sql /pgdata/base/16384 --sql --table-name public.sales -o out.sql
# 混合：父表 + 普通表（父表同样展开）
pg2sql /pgdata/base/16384 --sql --table-name public.sales,public.t1 -o /out/
# 空分区父表（无子分区）→ 导出 0 行（verbose 提示）；老式 INHERITS 继承父表（relkind='r'）不展开，逐表导出
```

导入：先建父表+子分区，再逐分区 `\i`。父表本身 relfilenode=0，v1.0.35 及以前直接导出会报未找到，v1.0.36 起自动展开子分区。

### 6.2 表空间不在默认数据目录

表空间目录结构：`<tablespace>/PG_<主版本>_<目录版本号>/<dboid>/<relfilenode>`（金仓为 `sys_tblspc/` + `SYS_<目录版本号>`）。

**方式 A：软链接路径自动发现（v1.0.32 起，免额外参数）**。`pg_relation_filepath()` 返回数据目录相对路径（含 `pg_tblspc/` 软链接前缀），拼上数据目录即可直接导出，工具自动从路径识别数据根目录与库 OID、读取 catalog：

```bash
# 获取相对路径（实例在线时）
SELECT pg_relation_filepath('t_ts');   -- → pg_tblspc/16391/PG_17_202307071/16384/16391
# 软链接路径直接导出（无需 --datadir/--db-oid）
pg2sql /pgdata/pg_tblspc/16391/PG_17_202307071/16384/16391 --sql
```

**方式 B（v1.0.34 起，推荐）：目录模式自动发现**。`--datadir DIR --db-oid OID` 等效 `base/<oid>`，工具从 `pg_class.reltablespace` 自动定位表空间表，无需知道表空间真实路径/软链接状态：

```bash
# 列出数据库：filepath 列多行显示（base 一行 + 每个表空间一行）
pg2sql --datadir /pgdata --list-db
# 列库内表：输出含"表空间"列（0=base，非 0=pg_tblspc/<oid>）
pg2sql --datadir /pgdata --db-oid 16384 --list-tables-db
# 批量导出（--schema/--tables/--all-tables），自动包含表空间表
pg2sql --datadir /pgdata --db-oid 16384 --schema public --sql -o /out/
# 单表导出（表空间表亦可）
pg2sql --datadir /pgdata --db-oid 16384 --table-name public.t_ts --sql
```

**兜底：绝对真实路径 + `--datadir`/`--db-oid`**。表空间软链接悬空、目标目录被删、或只拿到真实目录路径（不含 `pg_tblspc/` 前缀）时，工具无法从路径反推数据目录，需显式提供 catalog 定位：

```bash
# 手工定位真实文件路径后，配合数据根目录与库 OID
pg2sql /ts_data/PG_17_202307071/16384/16391 --datadir /pgdata --db-oid 16384 --sql
```

定位失败时输出明确报错（"位于表空间 N，但数据文件不存在…pg_tblspc 软链接可能悬空或目录缺失"），不会静默跳过。

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

### 6.7 多表并行与表内并行资源计算（v1.0.38）

`--jobs N`（表间并行）与 `--parallel P`（表内页级并行）正交叠加，需按机器核数与救援磁盘状态规划：

**公式**：

- **总并行度 = jobs × parallel**。建议对齐核数；超订 → 线程上下文切换 + 磁盘 I/O 争抢，救援磁盘（机械盘/网络盘）不稳时优先降 `--jobs` 保吞吐。
- **内存**：`总内存 ≈ jobs × (单表基础 ~12MB + parallel × 单 worker 增量)`。worker 增量随页大小：8KB 页约 2-3MB、16KB 约 3-5MB、32KB 约 4-8MB（页序窗口 O(并行度)，**与表大小无关**）。总量几十~几百 MB，永远安全区（对比 v1.0.29 旧版全量收集单表 5-6GB）。

**8 核四组合例**（32KB 页按单 worker 增量 8MB 计）：

| 组合 | 总并行度 | 估算内存 | 建议 |
| --- | --- | --- | --- |
| `--jobs 1 --parallel 8` | 8 | ~12MB + 8×8MB ≈ **76MB** | 单表最大加速 |
| `--jobs 4 --parallel 2` | 8 | 4×(12+2×8) ≈ **112MB** | ✅ 推荐（均衡） |
| `--jobs 8 --parallel 1` | 8 | 8×12 ≈ **96MB** | 表多单表小 |
| `--jobs 8 --parallel 4` | 32 | 8×(12+4×8) ≈ **352MB** | ⚠️ 严重超订，不推荐 |

**16 核、50 表示例**（`--jobs 8 --parallel 2`，8KB 页按增量 2.5MB）：内存 ≈ 8×(12+2×2.5) = **136MB**；总并行度 16 对齐核数。

**救援场景建议**：先固定 `--parallel 2~4` 保单表解析吞吐（大表内并行收益核心），再把剩余核数分配给 `--jobs`；IO 慢/磁盘不稳时 `--jobs 1` + `--parallel 2` 最稳。

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

**Q9：--full-schema 在金仓上函数段为什么是 0？**
A：金仓 V8/V9 用户函数（pg_proc）行字节在数据页存在但页头 item 目录不更新（金仓私有写入机制），离线枚举不到——这是金仓侧的已知限制，触发器/外键/CHECK/视图依赖/大对象均正常。PG 全版本无此限制。

**Q10：--progress 为什么输出到 stderr 而不是 stdout？**
A：stdout 是数据通道（管道/重定向时保持纯净），stderr 是诊断通道。TTY 下 `\r` 原地刷新不污染屏幕输出，管道下逐行输出。

**Q11：--large-objects 导出的大对象怎么导入？**
A：`psql/ksql -d <db> -f large_objects.sql`（`lo_from_bytea` 语句自动创建并写入大对象）；配 `--import-script auto` 会生成 `import_lob.sh` 一键导入。

---

## 7. 金仓 TDE 透明加密离线解密（v1.0.39）

金仓 TDE 透明加密（sysencrypt）数据文件在**离线（宕机）场景**下同样可救：pg2sql 内置 SM4-ECB/SM3 国密实现（不依赖外部库），对加密页做离线解密后正常导出。

### 7.1 表加密（`--enckey KEY`）

创建侧（实例在线时）：

```sql
CREATE TABLE t_enc (id int, v text) ENCRYPTED BY 'k1ngb2se';
```

导出侧（离线）：

```bash
# 文件路径模式
pg2sql /pgdata/base/16384/21747 --sql --table-name public.t_enc --enckey k1ngb2se -o /tmp/out/
# 目录模式（自动发现表结构 + TOAST）
pg2sql --datadir /pgdata --db-oid 16384 --table-name public.t_enc --sql --enckey k1ngb2se -o /tmp/out/
```

- 密钥 = `ENCRYPTED BY` 字符串 + `0x00` 填 16B（超长截前 16B）；数据页 `offset 0-63` 页头明文、`offset 64` 起 SM4-ECB。
- TOAST 外联长文本与主表同密钥，自动解密重组；串行/并行（`--parallel N`）路径均覆盖。

### 7.2 表空间加密（`--wallet-password` / `--wallet-dir`）

创建侧：

```sql
CREATE TABLESPACE tsp_enc LOCATION '/data/tsp_enc' WITH (encryption=true, encmethod='sm4');
CREATE TABLE t_tspc (...) TABLESPACE tsp_enc;
```

导出侧（离线，**无需密码**即可推导密钥链）：

```bash
pg2sql --datadir /pgdata --db-oid 16384 --table-name public.t_tspc \
  --sql --wallet-password 'Wal1et#Tde2026' -o /tmp/out/
```

- 钱包链离线推导：`KEK → MK → TSPK`（masterkey.kr/tspkey.kr 随数据目录保存），`--wallet-password` 仅做 SM3 校验并告警。
- `--wallet-dir` 默认自动探测 `<PGDATA>/.wallet`；多表空间多钱包条目自动按表空间 OID 匹配。
- V8 钱包密码 SM3 校验偏移与 V9 不同（告警"密码校验失败"），离线推导解密不受影响。

### 7.3 边界

- 前提：实例需加载 `sysencrypt`（`shared_preload_libraries` 含 `sysencrypt`），否则无法创建加密对象；**离线解密侧不依赖该扩展**。
- 未指定密钥时按明文解析加密页会得到乱码/0 行——先确认表/表空间是否加密。
- 加密密钥为字符串（`ENCRYPTED BY`）或钱包链可推导时，本工具可离线解密；使用加密设备（HSM/密文卡）等外部密钥托管场景不在支持范围。

---

## 8. 限制与说明

- `--ddl` 重建**表结构**（列/主键/索引/序列/默认值/注释），不重建函数、触发器、视图、外键约束、扩展——这些对象由 v1.0.40 新增的 `--full-schema` 输出（函数/触发器/外键/CHECK/视图依赖清单）。
- **金仓函数段限制（v1.0.40 实测）**：金仓 V8/V9 用户函数（pg_proc）行字节在数据页中存在，但页头 item 目录不更新（金仓私有写入机制，页头 lower=0），离线枚举不到 → `--full-schema` 金仓函数段=0。触发器/外键/CHECK/视图依赖/大对象均正常。PG 全版本无此限制。
- **视图/CHECK 不反编译**：`--full-schema` 对视图与 CHECK 输出"占位注释 + 依赖清单"，不反编译 pg_node_tree 表达式（与 pg_dump 等价决策，工作量级相同）。
- 在线实例建议先 `CHECKPOINT` 或使用一致性快照，避免磁盘与内存不一致。
- 金仓透明加密（TDE）文件支持离线解密导出：表加密用 `--enckey`，表空间加密用 `--wallet-password`/`--wallet-dir`（见第 7 章）；外部密钥托管（HSM/密文卡）不支持。
- 复合类型（record）输出原始字节 `E'\x...'` 兜底（字节可逆，不可文本化）；金仓 MySQL 模式 SET 输出 `\x` hex。
- 系统表导出（`--all-tables`）量大且包含内部状态，导入目标库需谨慎。
- 坏块/截断仅影响对应页或字段（NULL 占位），不中断整体导出，结果中会输出警告日志。

---

## 9. 双版本发布形态（v1.0.39 起）

v1.0.39 起发布**免费版**与 **TDE 付费版**两个二进制系列（编译期 `-tags tde` 门控）：

| 维度 | 免费版 `pg2sql` | TDE 版（版本号带 `(TDE)`） |
|---|---|---|
| 构建 | `go build .` | `go build -tags tde .` |
| `--enckey` / `--wallet-password` / `--wallet-dir` | 不识别（报错提示 TDE 版） | 完整支持 |
| 二进制内 TDE 代码 | 编译期剔除（strings 0 命中） | 含 SM4-ECB/SM3 国密实现 |
| `--version` / `--help` | `pg2sql 1.0.39` / 无 TDE 参数 | `pg2sql 1.0.39 (TDE)` / 含 3 个 TDE 参数 |

- 发布包命名：免费版 `pg2sql-<版本>-<平台>/`，TDE 版 `pg2sql-<版本>-tde-<平台>/`（解压结构均为 `二进制 + docs/USER_GUIDE.md`）。
- 源码包：免费版源码不含 `tde/` 目录（`go build` 直接可编）；TDE 版源码含 `tde/` 目录（`go build -tags tde`）。
- 选择建议：仅需常规离线导出选免费版；需要金仓 TDE 透明加密离线解密（生产加密库宕机救援）选 TDE 版。

---

*本手册对应 v1.0.41；每次版本变更随 README 更新记录同步。*
