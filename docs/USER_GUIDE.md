# pg2sql 用户手册 v1.2.12

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
  - [4.7 完整性自证报告（--report，v1.0.53）](#47-完整性自证报告--reportv1053)
  - [4.8 角色导出（--roles，v1.0.53）](#48-角色导出--rolesv1053)
- [5. 参数搭配矩阵](#5-参数搭配矩阵)
- [6. 场景专题示例](#6-场景专题示例)
- [7. 常见问题 FAQ](#7-常见问题-faq)
- [8. 金仓 TDE 透明加密离线解密（v1.0.39）](#8-金仓-tde-透明加密离线解密v1039)
- [9. 限制与说明](#9-限制与说明)
- [10. 能力矩阵：已覆盖 / 近似 / 不支持](#10-能力矩阵已覆盖--近似--不支持)
- [11. 双版本发布形态（v1.0.39 起）](#11-双版本发布形态v1039-起)

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
| `--format FMT` | 值 | 无（等价 `--sql`） | 数据格式单选：`sql`/`csv`/`text`（**v1.2.5 推荐写法**，等价旧 `--sql`/`--data`/`--text`；只能出现一次，不能与旧三旗标混用） |
| `--sql` | 开关 | 关 | 输出 INSERT 语句（v1.2.5 起与 `--data`/`--text` 严格互斥） |
| `--data` | 开关 | 关 | 输出 CSV（v1.2.5 起与 `--sql`/`--text` 严格互斥） |
| `--text` | 开关 | 关 | 输出 PostgreSQL COPY text 格式（v1.1.1；制表符分隔 + `\N` 表 NULL，后缀 `.txt`；v1.2.5 起与 `--sql`/`--data` 严格互斥） |
| `--count` | 开关 | 关 | 只统计行数，不输出数据 |
| `--deleted` | 开关 | 关 | 包含已删除行（`-- DELETED ctid` 注释） |
| `--only-deleted` | 开关 | 关 | 只导出已删除行 |
| `--clog` | 开关 | 关 | 读 CLOG（`pg_xact`/金仓 `sys_xact`）复核元组可见性（v1.2.0；仅对 hint bit 缺失的元组生效，需配合 `--datadir`） |
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
| `--header` | 开关 | 关 | CSV 首行输出字段名（**v1.2.5 起仅 csv 格式可用**；sql/text/纯 DDL 场景指定会报错） |
| `--complete-insert` | 开关 | 开 | INSERT 带列名（默认） |
| `--no-complete-insert` | 开关 | 关 | INSERT 省略列名 |
| `--delimiter CHAR` | 值 | `,` | CSV 分隔符 |
| `--toast FILE` | 值 | 自动 | 指定 TOAST 表文件（自动找 reltoastrelid） |
| `--page-size N` | 值 | 自动 | 页大小（8/16/32KB） |
| `--parallel N` | 值 | 串行 | 并发 worker 数（TOAST 索引预建 + 主表页分片行解析） |
| `--jobs N` | 值 | 1 | 批量表间并行（`--table-name` 多表/`--tables`/`--schema`/`--all-tables`，每表独立文件；v1.0.38） |
| `--batch-rows N` | 值 | 1 | 多值 INSERT 批大小（N≥2 合并 `INSERT ... VALUES (...),(...)`，默认 1=逐行；v1.0.38） |
| `--report` | 开关 | 关 | 导出报告 `report.json`：v1.0.38 基础字段（表/格式/行数/坏块/文件/字节/checksum/耗时）+ v1.0.53 **完整性自证**（`db_state`/`summary`/`rows_*`/`pages_*`/`skipped`/`unknown_types`/`verdict`） |
| `--report-file PATH` | 值 | — | 指定报告落盘路径（隐含 `--report`；默认 `<输出目录>/report.json`；v1.0.53） |
| `--checksum crc32\|md5` | 值 | 无 | 输出校验和（与 `--report` 配合，流式计算；v1.0.38） |
| `--import-script none\|auto\|sql\|csv` | 值 | none | 生成导入辅助脚本（sql→`psql/ksql -f`，csv→`\copy` 联动 `--fields`；v1.0.38） |
| `--tz NAME\|±HH:MM` | 值 | 自动 | **金仓 MySQL/ORA 模式**下 `timestamp` 解码时区（默认解析数据目录 conf 的 timezone，取**最后一处**赋值，失败按 UTC+8 并告警；IANA 名按完整时区规则逐值取历史偏移，数值偏移按固定偏移；v1.1.1） |
| `--enckey KEY` | 值 | 无 | 金仓 TDE 表加密密钥（`CREATE TABLE ... ENCRYPTED BY 'KEY'`；页 offset64+ SM4-ECB 离线解密，TOAST 同密钥；v1.0.39） |
| `--wallet-password PW` | 值 | 无 | 金仓 TDE 表空间加密钱包密码（密钥链 KEK→MK→TSPK 离线推导本不需密码，密码仅做 SM3 校验并告警；v1.0.39） |
| `--wallet-dir DIR` | 值 | 自动 | 钱包目录（默认自动探测 `<PGDATA>/.wallet`；需 masterkey.kr/tspkey.kr，多表空间条目自动匹配；v1.0.39） |
| `--encoding CODEC` | 值 | auto | 库编码（UTF8/GBK/GB18030；**v1.2.6 起白名单校验，未知名报错**，详见下方详解段）；auto 在声明非 UTF8 时自动做 UTF8 字节特征回退（v1.0.42） |
| `--full-schema` | 开关 | 关 | 完整 Schema DDL（需 `--ddl`）：函数/触发器/外键/CHECK/视图依赖清单，输出 `schema_objects.ddl`（v1.0.40） |
| `--schema-objects LIST` | 值 | 全部 | 过滤 Schema 对象类别（逗号分隔：view,function,trigger,constraint,fk；v1.0.40） |
| `--large-objects` | 开关 | 关 | 导出 pg_largeobject 大对象（`lo_from_bytea` 语句；需库目录；v1.0.40） |
| `--large-object-oids LIST` | 值 | 全部 | 只导出指定大对象 OID（逗号分隔；v1.0.40） |
| `--progress` | 开关 | 关 | 导出进度与 ETA（stderr 输出：**当前表名**/页/行/耗时/ETA；TTY 原地刷新，管道逐行；v1.0.41） |
| `--roles` | 开关 | 关 | 角色 DDL 导出：离线解析 `global/pg_authid` + `pg_auth_members`，输出 `roles.ddl`（全属性+密码密文+VALID UNTIL+GRANT 成员关系；v1.0.53） |
| `--roles-nopassword` | 开关 | 关 | 同 `--roles`，但省略 PASSWORD 密文（合规脱敏/第三方交付；v1.0.53） |
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

四个互斥/可组合的导出类型：`--ddl`、`--sql`、`--data`、`--count`；`--text` 是 `--data` 的格式变体（v1.1.1）。

| 组合 | 输出 | 典型命令 |
| --- | --- | --- |
| `--sql` | 仅 INSERT | `pg2sql <数据文件> --sql` |
| `--ddl` | 仅 DDL（建表+主键+索引+序列+默认值+注释） | `pg2sql <数据文件> --ddl` |
| `--sql --ddl` | DDL + INSERT（重建导入闭环） | `pg2sql <数据文件> --sql --ddl` |
| `--data` | CSV | `pg2sql <数据文件> --data` |
| `--text` | COPY text（制表符分隔） | `pg2sql <数据文件> --text` |
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

> **输出形态（重要，v1.2.11 补充说明）**：审计模式下每条删除行输出为**两行**——先一行注释标记，再一行**完整可执行的 INSERT**：
>
> ```sql
> -- DELETED ctid=(0,1)              ← 确定已删除
> INSERT INTO "public"."t" ("id") VALUES (1);
> -- SUSPECTED_DELETED ctid=(0,2)    ← 疑似已删除（hint 缺失，建议加 --clog 复核）
> INSERT INTO "public"."t" ("id") VALUES (2);
> ```
>
> 即注释只是**提示**，后面的 INSERT 并未被注释掉。因此审计输出直接 `psql -f` 回灌会把逻辑已删除的行**重新插入**；若只想看不想导，请自行过滤 `-- DELETED` / `-- SUSPECTED_DELETED` 行。
>
> 与 `--batch-rows` 组合时，删除行会**打断批组**单独成条（保证多值 INSERT 语法不被破坏）；该行为在 v1.2.11 前对"疑似删除"标记有缺陷（产出非法 SQL），现已修复。

#### 4.2.0 CLOG 可见性复核（`--clog`，v1.2.0）

**解决什么问题**

工具判断一行"是否可见"依赖堆元组头里的 **hint bit**（`t_infomask` 的 `HEAP_XMIN_COMMITTED` / `HEAP_XMIN_INVALID` / `HEAP_XMAX_INVALID` 等）。但 hint bit 只是 **缓存，不是权威状态**——PG 只在需要时才回填。

多数情况下没问题：PG 在**正常回滚路径**里会主动把回滚事务的 xmin 写成 INVALID。实测一张"100 活行 + 50 回滚 INSERT"的表（`relallvisible=0`，看似 hint 完全没覆盖），50 个回滚元组的 `t_infomask = 0x0A02` 已含 `HEAP_XMIN_INVALID`，不加任何参数就能正确导出 100 行。

真正会出问题的是 **hint 从未被写过的元组**，典型成因：

| 成因 | 说明 |
|---|---|
| 崩溃恢复 | 中断的事务被判定 aborted，但进程已死，`AbortTransaction` 未走到设 hint 的代码 |
| 物理拷贝 / 裸设备导出 | 从备库、快照、dd 出来的堆文件，hint 停在复制那一刻 |
| 第三方工具搬过文件 | 只复制了堆文件、未同步 hint |

受控实验（`sc.dead_probe`，150 物理元组 / 100 活行）：把 50 个回滚元组的 `t_infomask` 从 `0x0A02` 剥成 `0x0802`（抹掉 XMIN 两位），工具立刻导出 **150 行**——多导 33%。

**`--clog` 做什么**

对 **hint 缺失**的元组，额外读 `<PGDATA>/pg_xact/XXXX`（金仓自动识别 `sys_xact`；`global/pg_control` / `sys_control` 偏移 64 处读 nextXid）。每个事务占 2 bit：

| 值 | 含义 | 处理 |
|---|---|---|
| 1 | COMMITTED | 按已提交（插入则可见，删除则不可见） |
| 2 | ABORTED | 按已回滚（插入则不可见，删除则行仍活） |
| 0 | IN_PROGRESS | **按活行导出 + 告警**，绝不判死 |
| 3 | SUB_COMMITTED | 等同 COMMITTED |

hint 完整的元组**完全不查 CLOG**，所以常规库加不加结果相同。

**使用前提（重要）**

CLOG 是**延迟落盘**的：事务 commit/abort 之后状态可能仍在共享缓冲区。导出前请先 `CHECKPOINT`（或直接停机后离线导出）：

```bash
psql -c "CHECKPOINT"
```

否则部分事务会读成 `IN_PROGRESS`，工具会按活行导出并给出告警：

```
警告: [表名] 有 N 个事务在 CLOG 中为 IN_PROGRESS 或不可判，已按活行导出。
      若数据库在线运行，请先 CHECKPOINT 后重试
```

**自检告警（不开启也会提示）**

不加 `--clog` 时，工具仍会统计 hint 缺失占比。超过 10% 就告警：

```
警告: [表名] 约 33.3% 的元组缺少 hint bit（可见性结论来自启发式），可能多导回滚残留行。
      若本实例曾崩溃恢复、或堆文件是物理拷贝/裸设备导出，
      建议加 --clog --datadir <PGDATA> 复核
```

**用法**

```bash
# 加 --clog 复核（需 --datadir 才能定位 pg_xact）
pg2sql --datadir /pgdata --db-oid 16384 --table-name public.t1 --count --clog

# 金仓同理（自动识别 sys_xact）
pg2sql --datadir /home/kingbase/userdata/data --db-oid 12745 --table-name rg.big --text --clog -o out.txt
```

输出示例：

```
提示: --clog 已启用（/pgdata/pg_xact），当前 nextXid=2039564。CLOG 为延迟落盘……
提示: [sc.dead_probe] CLOG 复核：查询 1 次（已提交 0 / 已回滚 1 / 不可判 0），剔除 hint-only 会误导的行 50 条
-- 总行数: 100
```

**性能**

实测 PG 17 百万行表（149MB / 1,000,000 元组）：

| 场景 | CLOG 查询次数 | 耗时 |
|---|---|---|
| hint 完整，不加 `--clog` | — | 44.9s |
| hint 完整，加 `--clog` | 0 | 44.3s |
| hint 全剥除（最坏），加 `--clog` | 1 | 44.0s |

最坏情况之所以只查 1 次，是因为整表元组共用一个 xmin，缓存直接命中。开销可视为零。

**已知限制**

- 无法解析 multixact 成员（沿用既有 `HEAP_KEYS_UPDATED` 策略）
- CLOG 段被 truncate 后（极老的 xid）读不到，按"不可判"处理
- 只读，不回写 hint bit

#### 4.2.1 COPY text 格式（`--text`，v1.1.1）
PostgreSQL 的 `COPY ... WITH (FORMAT text)` 格式（也是 psql `\copy` 与 `pg_dump` 的默认数据格式）。产物后缀 `.txt`，与服务端 `COPY ... FORMAT text` 输出**逐字节一致**。

格式规则：

| 元素 | 表示 |
| --- | --- |
| 列分隔符 | 制表符 `\t`（可用 `--delimiter` 改，如 `--delimiter '|'`） |
| 行分隔符 | `\n`，末列后不加分隔符 |
| NULL | `\N`（字面量 `\N` 会被转义成 `\\N`，两者不混淆） |
| 空串 | 空字段（与 NULL 严格区分） |
| 转义 | 仅 `\b \t \n \v \f \r` 与反斜杠 `\\` 共 7 种 |
| 其他控制字符 | 原样输出（**不做八进制转义**，与服务端行为一致） |
| 引号 | 不使用（值内的 `"` 原样输出） |
| 表头 | 无（该格式不支持表头，`--header` 会报错） |
| 类型信息 | 无，全部按文本由目标表列类型解析 |

```bash
# 单表：导出 + 一键回灌脚本
pg2sql --datadir /pgdata --db-oid 16384 --table-name public.t1 \
       --text --ddl --import-script auto -o /tmp/out/
# /tmp/out 下：public.t1.ddl  public.t1.txt  import_public.t1.sh

# 批量
pg2sql --datadir /pgdata --db-oid 16384 --schema public --text --import-script auto -o /tmp/out/
```

回灌（`FORMAT text` 全默认参数即可，不需要 `NULL`/`DELIMITER` 附加项；psql 与金仓 ksql 都支持）：

```sql
\copy public.t1 FROM '/tmp/out/public.t1.txt' WITH (FORMAT text, ENCODING 'UTF8')
```

选型建议：

| 需求 | 选 `--text` | 选 `--data`（CSV） |
| --- | --- | --- |
| 直接 `\copy` 回灌、与 pg_dump 生态互通 | ✅ 首选 | 需带 `NULL '\N'` 参数，否则 NULL 与空串互换 |
| 给 Excel / BI 工具打开 | 不适合（制表符 + 转义） | ✅ 首选 |
| 需要表头行定位列 | 不支持 | ✅ `--header` |
| 人工 grep / 取证比对 | ✅ 行结构稳定、无引号 | 次之（含引号包裹） |

> 若把 `--data` 产物手工用 `COPY ... FORMAT csv` 回灌，**必须**加 `NULL '\N'`：该导出把 NULL 记作 `\N`、空串记作空字段，而服务端 CSV 默认认为空字段即 NULL，会造成 NULL 与空串互换（实测 10 行探针表错 2 行）。用 `--text` 则无此陷阱。

---

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

> **v1.2.12：`--fields` 不能与 `--no-complete-insert` 同用（sql 格式）。** 裁剪列后必须保留 INSERT 列清单，否则 `VALUES` 个数与表列数不符（`--fields a,b` 对 3 列表产出只有 2 个值的 INSERT），回灌必然报错。工具现在解析期直接拒绝并给出改法；csv / text 格式无列清单概念，不受影响。

#### `--limit N` —— 限制行数

> **v1.0.55：`--limit` 只影响数据导出，不影响 `--count`。** 旧版把 limit 同时用在计数路径，`--count --limit 10` 会把「前 10 行」当总行数输出；现 `--count` 恒为全表计数，同时指定时输出提示「--count 统计全表行数，--limit N 不影响计数（仅数据导出生效）」。

#### `--limit N` —— 限制行数

```bash
pg2sql /pgdata/base/16384/16391 --sql --limit 1000    # 只导前 1000 行
```

#### `--format FMT` —— 数据格式单选（v1.2.5，推荐写法）

```bash
pg2sql <数据文件> --format sql  -o out.sql   # 等价 --sql（默认）
pg2sql <数据文件> --format csv  -o out.csv   # 等价 --data
pg2sql <数据文件> --format text -o out.txt   # 等价 --text
pg2sql <数据文件> --format csv --ddl -o out  # 数据 CSV + DDL 独立成 out.ddl
```

**约束**（三条，均为解析期报错退出）：

1. **只能出现一次**——`--format csv --format text` 报 `--format 只能指定一次`；
2. **不能与旧三旗标混用**——`--format csv --sql` 报 `--format 与 --sql/--data/--text 不能混用`；
3. **只接受 `sql`/`csv`/`text`**——其它值报 `--format 仅支持 sql/csv/text`。

**旧三旗标严格互斥**（v1.2.5 行为变更）：`--sql`/`--data`/`--text` 任两个同现即报错
`--sql / --data / --text 互斥，最多指定一个`。v1.2.4 及之前 `--sql --data` 同现时 `--data` 静默优先、`--sql` 被丢弃且退出码为 0——**旧脚本若依赖该行为，升级后会报错退出，请改用 `--format`。**

**不导数据只导 DDL**：不需要 `--format` 也不需要旧旗标，直接 `--ddl` 即可（纯 DDL 模式，产物 `.ddl`）。

#### `--header` —— CSV 首行字段名

> **v1.2.5 起仅 csv 格式可用**（`--format csv` 或 `--data`）。`--text`/`--sql`/默认 INSERT 模式/纯 `--ddl` 指定 `--header` 一律报错
> `--header 仅适用于 csv 格式（--format csv 或 --data）`。此前 `--sql --header` 与单独 `--header` 是**静默忽略**，现改为显式报错。

```bash
pg2sql /pgdata/base/16384/16391 --data --header -o out.csv
pg2sql /pgdata/base/16384/16391 --format csv --header -o out.csv   # v1.2.5 等价写法
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

**v1.2.6 起完整的支持名清单**（解析期白名单校验，未知名报错退出，不再静默 Latin-1 兜底乱码）：

```text
auto、utf8/utf-8/unicode、gbk/gb2312/936、gb18030、latin1/latin-1/iso8859-1、sql_ascii、
euc-cn、euc-jp、euc-kr、euc-tw、euc-jis-2004、shift-jis-2004、mule-internal、
sjis、big5、uhc、johab
```

下划线别名（`euc_cn`、`euc_jp`、`euc_kr`、`euc_tw`、`euc_jis_2004`、`shift_jis_2004`、`mule_internal`）等价连字符写法；老文档中的 `--encoding euc_cn` 写法 **v1.2.6 起可正确识别**（v1.2.5 及之前会静默乱码，现归一为 `euc-cn` 走 GBK 无损解码）。其中 euc-jp/euc-kr/euc-tw/sjis/big5/uhc/johab 等暂无完整码表实现，非 UTF-8 字节按 `\x` hex 可逆输出并告警一次（不会静默损坏）。

```bash
pg2sql <数据文件> --encoding euc_cn -o out.sql   # v1.2.6 OK（等价 euc-cn）
pg2sql <数据文件> --encoding win1252             # 报错退出：--encoding 不支持 "win1252"（…支持清单）
```

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
> **v1.0.54：SQL 文件内嵌 client_encoding + 表级编码双向精化**
>
> - **`.sql` 首行现在固定输出 `SET client_encoding = 'UTF8';`**（对齐 pg_dump 惯例）。导出文件恒为 UTF-8 字节，但 GBK/GB18030 目标库默认按自身编码解释客户端字节：轻则 `invalid byte sequence for encoding "GBK"` 中途中断、已导入行参差；重则**静默双重编码**（实测 200 行全部"成功"入库、退出码 0、md5 与源库不一致）。回灌时直接 `psql -f x.sql` / `ksql -f x.sql` 即可，无需手工 SET；`--count` 与 CSV 输出不含该语句。
> - **表级编码精化改为双向**：旧版只在"库级判 GBK、本表实存 UTF8"方向覆盖，库级一旦被判成 UTF8 就跳过精化 → 混合存储库（同库既有 UTF8 字节表又有真 GBK 表）里真 GBK 表会被按 UTF8 解成 `'Óû§ÐÕÃû'` 形态的乱码且无告警。现按目标表数据文件的多页投票判定，两个方向都会覆盖并提示：
>
>   ```text
>   [pg2sql] 提示: 表 public.t_gbk2 数据字节呈 GBK 特征（库级判定 UTF8，同库其他表可能污染库级采样），本表按 GBK 解码；如非预期可用 --encoding 强制
>   ```
>
>   页间判定互相矛盾时（压缩页/二进制页占多数）保守不改库级判定。
>
> **v1.2.7：表级精化收紧（F2）**——原实现"单票即覆盖"存在静默乱码风险，现三条收紧：
>
> 1. **加密表跳过精化**：加密页内容近似随机，一页噪声即可命中 GBK 特征 → 整表 UTF-8 中文按 GBK 解码（文件仍是合法 UTF-8、导入零报错、只有 md5 能发现）。判定阈值取「≥50% 采样页可疑」，普通表偶含二进制大字段不会被误跳。此时输出：
>
>    ```text
>    [pg2sql] 提示: 表 v121.enc_t 数据页呈密文特征（疑透明加密表），已跳过表级编码精化；如导出中文乱码请用 --encoding 显式指定
>    ```
>
> 2. **覆盖门槛 ≥2 票且胜出占比 ≥2/3**：单票或 3/5 这类弱多数不再覆盖，改为"证据不足未采用"警告；多页一致时行为与 v1.0.54 相同（不回退）。
> 3. **库级采样改分层**：原"只取最大 3 个文件"，re-initdb / 批量导入 / VACUUM FULL 后排序一变，库级判定就在两次运行间翻转。现取「最大 2 个 + 其余等距 3 个」并跳过密文文件，结果确定性可复现。
>
> 覆盖生效时的日志由「提示」升为「**警告**」——该判定会改变导出内容，级别过低容易被日志淹没。

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

### 4.7 完整性自证报告（--report，v1.0.53）

离线导出最怕"静默不完整"：退出码 0、文件也在，但少了段文件、少了 TOAST、少了表。`--report` 从 v1.0.53 起把"导了多少"升级为"该导多少、少了什么、为什么少"。

```bash
# 批量导出 + 完整性自证（report.json 落在输出目录）
pg2sql /pgdata/base/16384 --tables --sql --jobs 4 --report --checksum md5 -o /out/

# 无 -o 时报告打到 stderr（便于管道下游采集）
pg2sql /pgdata/base/16384/16391 --sql --report | gzip > out.sql.gz
```

report.json 主要字段：

| 字段 | 说明 |
| --- | --- |
| `verdict` | `ok` / `warn` / `fail` + `reasons`；`fail` 出现在表导出失败、坏块、页数缺失（段文件丢失/截断）、TOAST 损坏时 |
| `summary` | 导出/跳过/失败表数、总行数与估计行数、坏块数（`bad_blocks`，其中 `bad_blocks_system` 为系统表部分）、TOAST 缺失/损坏数、字节数、耗时、未覆盖类型数 |
| `db_state` | 快照来源：`control_state`（1 干净关闭 / 6 运行中 / 4-5 恢复态）、`checkpoint_lsn`、`checkpoint_time`、`pg_version`、`is_kingbase`、中文 `note` |
| `exports[].rows_estimate` / `rows_check` | 与 `pg_class.reltuples` 对照：`match`/`under`/`over`/`unanalyzed`/`limited`/`filtered` |
| `exports[].pages_expected` / `pages_actual` / `pages_check` | 与 `pg_class.relpages` 对照：`short` 表示实测页数少于 catalog 记录（**段文件缺失/被截断，需重取快照**） |
| `skipped[]` / `skipped_summary` | 未导出对象及原因码：`schema_filter`/`system_schema`/`partition_parent`/`relkind`/`no_storage`/`shared_catalog`/`named_missing`/`partition_no_leaf` |
| `unknown_types[]` | 类型解码覆盖度：`kind=fallback`（文本/hex 退化，字节可逆）或 `unresolved`（pg_type 查不到，DDL 退化为 oid） |
| `exports[].error` | 导出失败的表也会出现在 `exports[]` 中并带错误原文（不再只在 stderr 一闪而过） |

判读顺序建议：`verdict.level` → `summary.tables_pages_short` / `tables_rows_under` → 必要时下钻 `exports[]`、`skipped[]`。

**落盘位置与退出码**：

- `--report` 是**开关**，不接路径；写 `<输出目录>/report.json`，无 `-o` 时打到 stderr。要换路径用 `--report-file /path/selfcheck.json`。
- 退出码只表示"工具是否正常跑完"，不表示"数据是否完整"：`verdict=fail` 仍退出 0（现场常见诉求是"不完整也要把能取的先取回来"）。**自动化流水线请解析 `verdict.level`，不要只看退出码。**
- v1.0.53 起解析期会拒绝多余的位置参数：`pg2sql <目录> --all-tables --report /out/report.json` 会直接报错并提示改用 `--report-file`——旧版本这种写法会把数据目录静默替换成 `/out/report.json`，导出退化成 0 表 0 行。
- 什么都没发现（目录/`--db-oid`/`--schema`/`--table-name` 写错）不再判 `ok`：`verdict=fail` +「未发现任何可导出对象」。
- **显式 `--schema` 未匹配是硬报错**（退出码非 0，不是 `verdict=fail`）：v1.0.53 起报 `--schema xxx 未匹配到任何可导出表（检查模式名大小写/是否存在；用 --list-tables-db 查看库内表清单）`。区别在于——`verdict=fail` 是"数据有问题但跑完了"，`--schema` 报错是"参数写错了，根本没跑"，自动化脚本不应把两者混为一谈。
- 系统表坏块单独归集到 `summary.bad_blocks_system`：金仓系统表有私有写入机制（页头 item 目录不更新），离线校验会把它判成坏页；这类只判 `warn`，**用户表坏块才判 `fail`**。

> 关于"和 pg_dump 报告有什么不同"：pg_dump 依赖活库 MVCC 快照、本身不产出完整性校验；pg2sql 读的是磁盘页，**必须**告诉你快照来自什么状态（`db_state`），并用 `reltuples`/`relpages` 做双向对照。详见 README「与原生工具（pg_dump / ksql）的差异」。

### 4.8 角色导出（--roles，v1.0.53）

角色定义存放在集群级共享目录 `global/pg_authid`（成员关系在 `global/pg_auth_members`），可以离线读出来还原：

```bash
pg2sql /pgdata/base/16384 --roles -o /out/            # → /out/roles.ddl（含密码密文）
pg2sql /pgdata/base/16384 --roles-nopassword -o /out/ # 同上，但省略 PASSWORD 密文
pg2sql /pgdata/base/16384 --tables --sql --roles -o /out/   # 与表导出同一批次完成
```

生成的语句形如：

```sql
CREATE ROLE "app_rw" WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE INHERIT NOREPLICATION NOBYPASSRLS
  CONNECTION LIMIT 7 PASSWORD 'SCRAM-SHA-256$4096:…' VALID UNTIL '2027-06-30 00:00:00+00';
GRANT "app_ro" TO "app_rw" WITH ADMIN OPTION;
GRANT "pg_read_all_data" TO "app_rw";
```

注意：

- **只导密码密文**（SCRAM-SHA-256 / md5），工具不接触明文；合规场景用 `--roles-nopassword` 脱敏。
- **initdb 内置角色自动跳过**（OID < 16384，含金仓 `system`/`sso`/`sao`/`sys_*` 与 `pg_*` 组），目标实例自带；若整库没有用户角色，文件里会写明这一点而不是留空。
- 授予方是内置角色（如 `GRANT pg_read_all_data TO app_rw`）属于真实权限委派，会保留；被授予方是内置角色则跳过。
- `CONNECTION LIMIT` 只在非默认值（≠ -1）时输出，`0`（禁止连接）会明确输出。
- 重复执行会因 `role already exists` 报错，回灌前请确认目标实例角色状态（脚本未加幂等包装，保持语句可审计）。

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

### 6.8 金仓兼容模式（ORA / MySQL / PG）与 `--tz`

金仓三种兼容模式的**目录布局一致**，工具自动识别，不需要额外参数；差异只在类型解码：

| 模式 | 特有类型 | 处理 |
| --- | --- | --- |
| PG | — | 与 PostgreSQL 完全一致 |
| ORA | `number`/`varchar2`/`raw`/`clob` 等 | 按别名映射到基类型解码 |
| MySQL | `tinyint`/`mediumint`/`year`/`datetime`/`binary`/`enum`/`set` | 专用解码；`set` 输出为 hex（见能力矩阵"近似输出"档） |

**唯一需要显式干预的是 MySQL 模式的 `timestamp` 时区**：这类值在磁盘上存 UTC 秒数，重建文本时必须知道源库时区，否则回灌后时刻会平移。

```bash
# 默认：自动从数据目录 conf 解析 timezone；解析失败按 UTC+8 处理并告警
pg2sql --datadir /data/kingbase --db-oid 16384 --table-name public.t_ts --sql

# 显式指定（推荐跨时区迁移时使用）
pg2sql --datadir /data/kingbase --db-oid 16384 --table-name public.t_ts --sql --tz Asia/Shanghai
pg2sql ... --tz +09:00        # 也接受 ±HH:MM / ±HHMM / ±HH 与 IANA 名称
```

判定建议：导入目标库的 `timezone` 与源库一致时，两者都可；若目标库时区不同，**必须显式 `--tz` 并在导入后复核**，否则 `timestamp`（不带时区）会按目标库时区重新解释。

> **v1.1.1 起（B15/B17）**：① conf 内 `timezone` 出现多次时按 **PG 语义取最后一处**（金仓镜像常先写 `UTC` 再追加 `Asia/Shanghai`，旧版取首个会导致整体 ±8 小时静默偏移），并优先读 `kingbase.auto.conf`/`postgresql.auto.conf`；② IANA 名（`Asia/Shanghai`）按**完整时区规则**逐值取当时偏移——1901 年前为 LMT（+08:05:43）、1986-1991 中国夏令时为 +09:00，固定 `+08:00` 无法表达这两段，旧版会偏移 1 小时以内；二进制已内嵌时区库，离线环境无需 `/usr/share/zoneinfo`。数值偏移（`+08:00`）仍按固定偏移处理，跨 1901/夏令时边界的库请用 IANA 名。

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

## 8. 金仓 TDE 透明加密离线解密（v1.0.39）

金仓 TDE 透明加密（sysencrypt）数据文件在**离线（宕机）场景**下同样可救：pg2sql 内置 SM4-ECB/SM3 国密实现（不依赖外部库），对加密页做离线解密后正常导出。

### 8.1 表加密（`--enckey KEY`）

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

### 8.2 表空间加密（`--wallet-password` / `--wallet-dir`）

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

### 8.3 边界

- 前提：实例需加载 `sysencrypt`（`shared_preload_libraries` 含 `sysencrypt`），否则无法创建加密对象；**离线解密侧不依赖该扩展**。
- 未指定密钥时按明文解析加密页会得到乱码/0 行——先确认表/表空间是否加密。
- 加密密钥为字符串（`ENCRYPTED BY`）或钱包链可推导时，本工具可离线解密；使用加密设备（HSM/密文卡）等外部密钥托管场景不在支持范围。

---

## 9. 限制与说明

- `--ddl` 重建**表结构**（列/主键/索引/序列/默认值/注释），不重建函数、触发器、视图、外键约束、扩展——这些对象由 v1.0.40 新增的 `--full-schema` 输出（函数/触发器/外键/CHECK/视图依赖清单）。
- **金仓函数段限制（v1.0.40 实测）**：金仓 V8/V9 用户函数（pg_proc）行字节在数据页中存在，但页头 item 目录不更新（金仓私有写入机制，页头 lower=0），离线枚举不到 → `--full-schema` 金仓函数段=0。触发器/外键/CHECK/视图依赖/大对象均正常。PG 全版本无此限制。
- **视图/CHECK 不反编译**：`--full-schema` 对视图与 CHECK 输出"占位注释 + 依赖清单"，不反编译 pg_node_tree 表达式（与 pg_dump 等价决策，工作量级相同）。
- 在线实例建议先 `CHECKPOINT` 或使用一致性快照，避免磁盘与内存不一致。
- 金仓透明加密（TDE）文件支持离线解密导出：表加密用 `--enckey`，表空间加密用 `--wallet-password`/`--wallet-dir`（见第 8 章）；外部密钥托管（HSM/密文卡）不支持。
- 复合类型（record）输出原始字节 `E'\x...'` 兜底（字节可逆，不可文本化）；金仓 MySQL 模式 SET 输出 `\x` hex。
- **`"char"` DDL 必须带引号（v1.2.7 起已正确处理）**：`--ddl` 对 oid 18/1002 输出 `"char"` / `"char"[]`（列、数组、域基类型、函数签名一致）。**v1.2.6 及之前输出裸 `char`**，而 SQL 里裸 `char` 是 `character(1)`(oid 1042) 的别名——目标表被静默建成 `character(1)`：导入不报错、行数正确，但高位字节落库后整表读取报 `invalid byte sequence`，且再次导出时会被当 Latin-1 二次编码（`0xC3` → `'Ã'` → `C3 83`），**每迁移一次扩一层、不可还原**。用 `atttypid` 校验目标列（18 才对，1042 说明踩了旧版缺陷）。
- **`"char"`（内部单字节类型，oid 18）含 ≥0x80 字节或 NUL 时（v1.2.6 起，v1.2.8 扩到容器）**：SQL 格式输出 `(B-256)::"char"` 负数 int 强转（如 `(-61)::"char"`），可精确还原回灌——文本字面量（含 `E'\xC3'`）会被服务端 mb 校验拒绝，int 强转是唯一文本可行形态。**v1.2.8 起该处理覆盖全部容器载体**：数组元素 → `ARRAY[(-61)::"char",'A'::"char"]`、域列、复合字段 → `ROW((-61)::"char",7)::类型`、复合数组 / 域 over 复合 / 元素为域的数组同样重建；无高危字节的普通值输出与旧版逐字节一致。**唯一无法表达的是自定义下界数组**（`'[0:2]={…}'`）：PG 没有 `array_set`、`array_fill` 只能填同一值，这类值降级为原样输出并计入告警。CSV/text 格式不能写表达式，导出收尾会输出计数告警（覆盖标量列/数组元素/域/复合字段），对应行回灌 UTF-8 等多字节编码库将被服务端拒绝（latin1 等单字节库不受影响）。
  - **降级值的两条处置路径（v1.2.9 起告警里直接给出）**：① 目标库改用单字节编码（`latin1` / `SQL_ASCII`）回灌；② 导出后人工两步改写——先插入同下界的 ASCII 占位值，再 `UPDATE t SET col[lb] = (N)::"char"`（复合值按 `ROW(...)::type` 重写）。**不要尝试 `--format binary`**：该格式从未实现（`--format` 仅接受 `sql/csv/text`，v1.2.8 的告警文案误写、v1.2.9 已更正）。
  - **类型链上限（v1.2.9）**：域套域最多解析 32 层、数组/复合互相嵌套最多 8 层。超过上限时工具无法判定该列是否含 `"char"` 容器，会输出 `警告: 有 N 个类型的域链或容器嵌套超过解析上限…（oid 列表）`，此时**对应列可能漏改写**，请把 oid 与 `create domain/type` 语句反馈给开发者。真实实例的域链几乎都是 1 层，正常不会触发。
- **`--encoding` 白名单（v1.2.6 行为）**：未知名解析期报错退出（原静默 Latin-1 兜底乱码）；下划线别名（`euc_cn` 等）自动归一，详见第 3 章 `--encoding` 详解段。
- 系统表导出（`--all-tables`）量大且包含内部状态，导入目标库需谨慎。
- 坏块/截断仅影响对应页或字段（NULL 占位），不中断整体导出，结果中会输出警告日志。

---

## 10. 能力矩阵：已覆盖 / 近似 / 不支持

离线堆文件导出最容易出的问题不是"报错"，而是"静默不完整"：退出码 0、文件已生成，但少了段文件、少了 TOAST、少了表。下表按**还原保真度**分四档，并给出每档在 `report.json` 里的自证字段（字段判读详见 [4.7](#47-完整性自证报告--reportv1053)）。

| 档位 | 含义 | 覆盖内容 | 自证方式 |
| --- | --- | --- | --- |
| **精确还原** | 字节/语义与源库一致，回灌后与在线查询结果相同 | 全内置类型值（数值/字符/二进制/布尔/位/日期时间/JSON/XML/数组/几何/网络/全文/范围/reg*/pg_lsn/txid_snapshot/枚举）；TOAST 外联+压缩+跨段；坏块四级跳过；删除行（`--deleted`/`--only-deleted`） | `exports[].rows` + `bad_blocks`/`toast_missing`/`toast_corrupt` + `rows_check`/`pages_check` |
| **结构还原** | DDL 可原样重建对象，回灌后结构等价 | 表/列/默认值/主键/唯一/CHECK/外键/索引（含表达式与部分索引）/序列与 setval/注释/表空间/分区（父 `PARTITION BY` + 子 `PARTITION OF`，含多级与 DEFAULT）/域类型与域 CHECK/函数/触发器/视图依赖清单/大对象 | `exports[]` 中 `format=ddl` 条目；`--full-schema` 的 `schema_objects.ddl` |
| **近似输出** | 值可逆（字节保真），但文本形态可能与源库不同；或结构需人工确认 | 无专用解码器的类型（如 `pg_ndistinct`/`pg_mcv_list`/`pg_dependencies` 等内部类型）→ 文本透传或 `\x` hex；金仓 MySQL 模式 `SET` → hex；`record` 复合值 → record 文本 | `unknown_types[]`（含 oid/名称/列清单）+ `verdict.reasons` |
| **不支持 / 需人工** | 工具不产出或明确跳过，报告里可见原因 | 共享目录数据（`pg_database`/`pg_authid`/`pg_tablespace` 等 `global/` 存储对象）→ 改用 `--roles`；索引/视图/序列/TOAST 等非行存对象的"数据"；分区父表的"数据"（无存储）；传统 `INHERITS` 继承父表的自动展开 | `skipped[]`（reason: `shared_catalog`/`relkind`/`partition_parent`/`no_storage`/`schema_filter`/`system_schema`/`named_missing`/`partition_no_leaf`） |

**读报告的推荐顺序**：

1. `verdict.level` —— `ok` 可直接用；`warn` 需看 `reasons`；`fail` 必须处理后再用。
2. `summary.tables_pages_short` —— **大于 0 说明实测数据文件页数少于 `pg_class.relpages`**，通常意味着段文件（`.1`/`.2`）缺失或被截断，属于必须重取快照的硬信号。
3. `summary.tables_rows_under` / `tables_rows_over` —— 与 `reltuples` 偏差超过 ±10%，多为死元组膨胀（`under`）或统计过旧（`over`），结合 `exports[].rows_check` 定位到具体表。
4. `unknown_types[]` —— 有 `fallback` 说明该列走了文本透传/hex，字节可逆但文本形态可能与源库不同；有 `unresolved` 说明 DDL 里该列退化成 `oid`，必须人工补类型。
5. `db_state.control_state` —— 判断这份快照"有多新、多干净"（见 4.7 判读表）。

> 结构性跳过（`relkind`/`partition_parent`/`shared_catalog` 等）是"按设计不导"，只会在 `skipped[]` 里列清单，不会把 `verdict` 拉成 `warn`；只有 `named_missing`（点名要的表不存在）才会告警。

---

## 11. 双版本发布形态（v1.0.39 起）

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

*本手册对应 v1.2.12；每次版本变更随 README 更新记录同步。*

---

## 更新日志（v1.0.43 - v1.2.12）

> v1.0.42 之后的特性摘要（详见 README 版本历史）：

- **v1.2.12**：**四处缺陷收口（第二轮深度代码审查结论）**。① `-o` 指向多段表（>1GB，存在 `16391.1`/`16391.2` 续段）的**续段文件**时，输出守卫只比对主段、放行后 `os.Create` 先把源续段截断再读，源数据清零且导出 0 行；现把 `engine.SegFiles` 展开的续段一并纳入比对，拒绝信息会指明命中的续段。② `DecodeArray` 对 `ndim>6` 与 `dims[i]<=0`（只能是头部损坏）静默返回 `{}`，与合法空数组无法区分、且不计降级——撕裂页里的数组会变成空数组（回灌成功、报告全绿）；现改走 `\x` 原文 + 收尾告警，`ndim=0` 的合法空数组仍输出 `{}` 且不计数。③ `--fields` 与 `--no-complete-insert` 组合会产出**列数不符**的 INSERT（裁剪后只有部分值、却无列名），回灌必然报错而工具无提示；现于解析期直接报错（csv/text 格式不受影响）。④ 全部表导出失败时仍会生成只建库、无数据步骤的空 `import_all.sh`（守卫阈值 11 行 vs 实际 12 行头部），已改为按实际头部行数判断。
- **v1.2.11**：**两处缺陷收口（深度代码审查结论）**。① `--batch-rows N>1` 与 `--deleted`/`--only-deleted` 组合、且表中存在 hint 缺失的"疑似删除"行时，批组打断只认 `-- DELETED` 前缀、漏掉 `-- SUSPECTED_DELETED`，该行被当作普通 VALUES 元组拼进批，产出 `INSERT ... VALUES -- SUSPECTED_DELETED ctid=(0,1)` 这类**非法 SQL**（退出码仍 0、无告警）；现按 `-- ` 注释前缀统一识别。② 23 个定长解码器（int8/int2/int4/oid/tid/xid/cid/float4/float8/money/日期时间族，及金仓 datetime/date/mysql time·timestamp/oracle DATE/ora_date）对 `len < 类型宽度` 的输入会越界 panic，主链路的 `offset+AttLen>nraw` 守卫只信任目录宽度、穿不过跨版本目录或损坏页；现于 `DecodeValue` 入口按 `minDecoderWidth` 预检，不足则输出 `\x` 原文并在导出收尾**告警**（不再静默，也不再崩进程）。附带：分区父表多叶子纯 `--count` 路径补齐 panic 隔离（唯一裸调用点），DDL 类型闭包的 `seen` 改为真正发射后才置位。
- **v1.2.10**：**DDL 类型依赖闭包**——`--ddl` 的前置类型段此前只看列的直接类型，域链 ≥2 层 / 域 over 复合·枚举 / 复合字段引用自定义类型时依赖全缺，空库回灌 `type "public.c8" does not exist`、连表都建不出；现按依赖闭包拓扑序输出（旧场景逐字节不变），域基为自定义数组时输出 `"comp_t"[]` 而非内部名 `_comp_t`。数据面同族缺口一并修复：域 over 枚举、域 over 自定义数组此前整列退化 `\x` 十六进制，现分别沿域链解析枚举成员、修正数组解码器注册顺序后正常解码回灌。

- **v1.2.9**：**char 容器三处边界收尾**——① 降级告警不再指向不存在的 `--format binary`（该格式从未实现，照做直接 `exit 1`），改为「单字节编码回灌 / 人工两步改写」两条可行路径并指向手册；② 域链（8→32 跳）与容器嵌套（4→8 层）上限提升且**超限不再静默**——9 层域链此前停在中间域被判"无 char 容器"、SQL 不改写也不告警，裸字节回灌失败，现正确输出 `(-61)::"char"`；域 over 复合 9 层从整列 `\x` 十六进制恢复为 `ROW(...)::type`；超限时按 oid 输出专项告警；③ 不规则数组在 SQL 侧补记降级计数（与 CSV/text 口径一致）。除上述边界场景外输出零变化。

- **v1.2.8**：**`"char"`(oid 18) 容器载体字节还原**——v1.2.6 的 `(B-256)::"char"` 只对**标量列**生效；同一字节装进**数组元素**、**域基类型**、**复合字段**时退化成普通文本字面量，回灌被 mb 校验拒绝（`invalid byte sequence`）**整表 0 行**且无告警。现按 array_in / record_out 语法重解析后重建为 `ARRAY[...]` / `ROW(...)::type`，覆盖复合数组、域 over 数组、域 over 复合、元素为域的数组；无高危字节时输出与 v1.2.7 逐字节一致。**自定义下界数组**（`'[0:2]={…}'`）PG 无法用表达式还原，降级原样输出并告警。附带修复 `domain over 复合`整列退化为 `\x` 十六进制的静默缺口。
- **v1.2.7**：**DDL 类型名与编码判定修复**——`--ddl` 对 oid 18/1002 输出 `"char"` / `"char"[]`（旧版裸 `char` 会建成 `character(1)`，造成不可逆的数据损坏）；表级编码精化收紧为 ≥2 票且 ≥2/3，加密表跳过精化。
- **v1.2.6**：**两处静默失败修复**——`"char"`(18) 高位字节 SQL 路径输出 `(B-256)::"char"` 强转可逆还原（实测 `E'\xC3'` 文本形态会被服务端 mb 校验拒绝，不可用）、CSV/text 输出计数告警；`--encoding` 未知名解析期报错（补 `euc_cn` 等下划线别名）。
- **v1.2.5**：**数据格式参数收口**——新增 `--format sql|csv|text`（推荐写法）；`--sql`/`--data`/`--text` 任两个同现即报错（修复 `--sql --data` 静默覆盖）；`--header` 仅 csv 格式可用，sql/text 下报错。
- **v1.2.4**：**新增 `\x` 退化静默兜底告警**——凡某单元格无法按列类型解码而输出 `\x` 十六进制（`bytea` 除外，其 `\x` 为合法格式），导出结束会汇总打印「有 N 个单元格的值无法按列类型解码…请反馈并提供类型 oid: xxx(×n)」。此前该情况完全静默（不告警、退出码 0），用户回灌失败才发现。若见到此提示，请把提示中的 oid 反馈给开发者。
- **v1.2.3**：**修复 2 条数据类型解码缺陷（均为静默失败）**——① **BUG-13（P1）`aclitem` 标量列导出退化为 `\x` 十六进制**：标量解码分派表从未注册 `ACLITEMOID(1033)`，标量 `aclitem` 落到兜底分支直接吐 `\x` + 十六进制（实机为 `\x0a0000000a0000007f100000`），回灌报 `invalid input syntax for type aclitem` 必失败；而同表 `aclitem[]` 数组列一直正常，症状极具迷惑性。修复后标量输出与 `aclitemout` 逐字符一致。② **BUG-14（P1）`regclass` 列丢 schema 前缀导致跨库回灌静默错指**：原只输出 `relname`，未采集 `relnamespace`；库内存在跨 schema 同名表时，指向非默认 schema 的值被降级为裸名，回灌后解析到错误的同名表（实测 `sc2.dup_t` → 误落 `public.dup_t`），**无任何报错**。现按 PG `regclassout` 规则，表不在默认 schema 时输出 `nspname.relname`。**升级须知**：含标量 `aclitem` 列的表导出内容与旧版不同（旧版错，新版正确）；含跨 schema 同名表引用的 `regclass` 列现在会带 schema 前缀。③ **另经实测确认（非工具缺陷）**：`--text` 对 `timestamptz` 固定 UTC 渲染、IPv6 不做 `::` 压缩，属呈现差异、语义完全等价（探针表回灌四列全等、服务端渲染 md5 逐字节相同）；金仓 V8R6C9B14 的 `aclitem` 回灌报 `invalid mode character` 是**引擎自身缺陷**（`aclitemout` 输出 `'p'`、`aclitem_in` 却无法解析 `'p'`，连自身输出都无法重新导入），V9 系列可正常回灌。

- **v1.2.0**：**新增 `--clog` 离线复核元组可见性**——元组的可见性依据 `t_infomask` 里的 hint bit（`HEAP_XMIN_COMMITTED` / `HEAP_XMIN_INVALID` 等），而 hint bit 只是**缓存、不是权威状态**。PG 在正常回滚路径里会主动回填 hint（实测回滚 INSERT 的元组 `t_infomask=0x0A02` 已含 `HEAP_XMIN_INVALID`，v1.1.x 靠 hint 就能导出正确行数），但**崩溃恢复中断的事务、物理拷贝/裸设备导出的堆文件**可能从未被写过 hint，此时 v1.1.x 会把回滚残留行静默导出（受控实验：150 物理元组 / 100 活行，hint 被剥除后导出 150 行，**多导 33%**）。新增 `--clog` 对 hint 缺失的元组读 `<PGDATA>/pg_xact/XXXX`（金仓 `sys_xact` 自动识别），每事务 2 bit：`1=COMMITTED 2=ABORTED 0=IN_PROGRESS`；hint 完整的元组完全不查 CLOG，**常规库行为零变化**。读到 `IN_PROGRESS`（CLOG 延迟落盘的正常现象）一律按活行导出并告警，绝不判死。另新增 hint 缺失自检：不开 `--clog` 时若缺失占比 ≥10% 自动告警并建议加 `--clog` 复核。**性能**：百万行 hint 完整时 CLOG 查询 0 次，耗时与 v1.1.2 持平；最坏情况（100 万元组 hint 全剥除）因同事务缓存命中仅查 1 次。**使用前提**：需 `--datadir`，且导出前已 `CHECKPOINT` 或数据库已停机。**兼容性**：不开 `--clog` 时 PG17.6 / 金仓 V8R6C9B14 的 4 表 × 3 格式、`--count`/`--deleted`/`--only-deleted`、百万行 146MB `--text` 全部与 v1.1.2 逐字节一致。

- **v1.1.2**：**全类型表回归暴露并修复 1 条 P1 静默数据损坏（B18）**——**`json[]` 数组元素被当原始二进制输出**：`arrayElemInfo` 原先只注册了 `JSONBOID(3802)`，缺 `JSONOID(114)` 与金仓 mysql 模式 `json(7024)`。PG 及金仓 PG 模式 `json[]` 元素类型是 114；金仓 V9 mysql 模式 `json` 是 domain of 4802 `mysql_json`（底层按 jsonb 二进制存储），`json[]` 元素类型是 7024。两者都落到"未知元素类型 → varlena 文本退化"，元素容器字节被当字符串吐出（实测金仓 V9R1C10B4-MySQL `rg.alltypes.c_jsonarr`：工具 1278B/含 14 个 `0x00`，同实例服务端 `COPY ... FORMAT text` 为 1264B/0 个 `0x00`，且文本正确），三格式回灌全部报 `invalid byte sequence for encoding "UTF8": 0x00`。已为 114 注册 `json`、7024 注册 `jsonb_p`。**影响面**：仅含 `json[]` 列的表（标量 `json`/`jsonb` 一直正确）；此类表旧版导出的数据**无法回灌**。**升级须知**：`json[]` 列导出内容与旧版不同（旧版错，新版与服务端一致）。**回归证据**：5 实例（PG17.6 / 金仓 V8R6C9B14-PG / V9R1C2B14-MySQL / V9R1C10B4-MySQL / V9R1C10B4-PG）全类型表三格式回灌 FAIL=0；4 实例分区表逐叶子与服务端 `COPY ... FORMAT text` 逐字节一致（10 叶子/实例）；特殊字符专项复测全绿。

- **v1.1.1**：**新增 `--text`（COPY text 格式）+ 2 条静默数据损坏修复**——① **新功能 `--text`**：导出 PostgreSQL `COPY ... FORMAT text` 格式（制表符分隔、反斜杠转义、`\N` 表 NULL、无表头，后缀 `.txt`），与服务端 `COPY ... FORMAT text` 输出**逐字节一致**（实测：特殊值探针、控制字符全量探针、100 万行×12 列混合表 222MB 全部 `cmp` 相同）；`FORMAT text` 全默认参数即可回灌，psql 与金仓 ksql 均验证通过；`--import-script auto` 自动生成对应 `\copy`；② **B13（P1）numeric 整数首位判据错误**：base-10000 下"尾组全零"的数（10000、20000、150000…）整数部分被整段输出成 `0`，影响 `numeric`/`json`/`jsonb`，**不报错**——由 `--text` 字节比对在 100 万行第 10000 行暴露（100 行差异），已修；③ **B14（P1）`--ddl --data` 把两行 SET 会话头写进数据文件**：`.csv` 首行变 SQL 导致 `\copy` 整表失败，`--text` 下 SQL 与数据混杂，已改为仅"DDL 无独立文件"时写入。**升级须知**：`--data` 的 CSV 产物在 `--ddl` 同用时不再带 SET 头（旧产物首行是 SQL 语句，回灌必失败）；`numeric`/`json`/`jsonb` 中 10000 整数倍的值导出结果与旧版不同（旧版错，新版本正确）。
  - **B15（P1）conf 内 `timezone` 多次赋值取了首个** → 金仓 mysql 模式 `timestamp` 整体 -8 小时（静默）。改为取最后一处，并优先 `*.auto.conf`。
  - **B16（P1）CSV 中值恰为 `\.` 未加引号** → `\copy` 把该行当数据结束标记，其后行**静默丢失**（实测 10 行只进 7 行）。`csvField` 现对 `\.` 加引号；`--text` 不受影响。
  - **B17（P2）金仓 mysql/ora 模式 `timestamp` 固定偏移解码** → 1901 年前（LMT +08:05:43）与 1986-1991 夏令时（+09:00）段偏移 1 小时以内；改为按 IANA 名加载完整规则并内嵌 `time/tzdata`。
  - **回归证据（v1.1.1）**：5 实例 × 5 张特殊字符探针表（46 项特殊值/单列无主键/多类型/TOAST/特值边界）× 3 格式全绿；`--text` 与同实例服务端 `COPY ... FORMAT text` 逐字节一致。

- **v1.0.66**：**实机回归修复 5 条（B8~B12）**——修复前/后双版本同 datadir 导出对比 + 活库真值逐字符核对（PG17.6 / 金仓 V8R6C9B14 / V9R1C2B14 / V9R1C10B4），全部通过：① **B8（P1）** `decodeAclitem` grantor/grantee 布局颠倒——跨角色授权项导出互换、PUBLIC 授权项静默丢失，已按官方 acl.h 布局 `[grantee][grantor][privs]` 修正；② **B9（P1）** ACL 位 12 大写 'S' 回归纠正为官方小写 's'（`GRANT SET ON PARAMETER` 回灌不再被 `aclitem_in` 拒绝）；③ **B10（P2）** nextval 形态但序列 oid 未映射时由静默丢失改为人工提示；④ **B11（P1，实机新发现）** PG17+ 的 `AclItem` 扩为 16 字节（grant option 独立第 4 字段）——旧版 12B 硬编码导致 PG17 上 aclitem[] 第 2 元素起全部错位、WITH GRANT OPTION 静默丢失，现按目录 pg_type 实测自适应；⑤ **B12（P2，实机发现）** 金仓 ACL 位 12 为自有权限字符 'p'（PG 系为 's'），按引擎切换字符表。**升级须知**：PG17 库的 ACL 类目录数据（relacl/attacl 等）导出结果与旧版不同——旧版错位/丢项，新版与活库真值一致；金仓 owner 默认 ACL 位 12 由 's' 变 'p'。已知待修（OBS-4）：`pg_parameter_acl` 共享目录暂不支持导出。
- **v1.0.65**：**第 4 轮全源码深审（4 路并行）+ 19 条修复**——本轮火力转向此前覆盖较浅的 datatype / charset / objects / integrity，并对 v1.0.64 的 14 条修复做回归核查（E-1~E-14 全部真实落地、无假修复）。上游 `array.h`/`arrayfuncs.c`/`acl.c` 逐字核对后坐实并修复：
  ① **数组定长元素对齐（P0，静默错数据）**：v1.0.46 的"定长元素紧密排列"结论只对 `typlen` 恰为 `typalign` 倍数的类型成立（float8 8/8、int4 4/4…，故当时实机取证无法区分）。上游 `array.h` 明示 "Individual items in the array are aligned as specified by the array element type"，`CopyArrayEls→ArrayCastAndSet` 对定长元素同样走 `att_nominal_alignby`。**`timetz[]`（12B/'d'，间距 16）与 `macaddr[]`（6B/'i'，间距 8，动态类型路径）从第 2 个元素起解出垃圾**，不报错；现按 `(pos-bodyOff)%typalign` 统一补齐（对齐基准是数据区起点，与 `ARR_DATA_OFFSET` 同余 mod 8），现有全部内置类型填充为 0、行为不变；
  ② **大对象内存降级路径排序（P0）**：临时文件创建失败时按物理扫描序拼接正文，绕过 pageno 排序校验 → 可产出"校验通过但内容错位"的 LO SQL；现先按页缓存、输出前统一排序拼接；
  ③ **`ParseTextArray` NULL 位图反转（P1）**：PG 约定位置位 = 非 NULL，旧实现写反 → 混合命名函数（`f(a int, OUT int, c text)`）的 `proargnames` 元素错位，`RETURNS TABLE` 列名串位；同时修正 `dataoffset` 坐标（应 `-4`，与 `DecodeArray` 同坐标）；
  ④ **aclitem 丢 `WITH GRANT OPTION`（P1）**：`ai_privs` 高 16 位是 grant option 位图（`aclitemout` 逐位输出 `字符*`），旧实现只扫低 16 位 → 列级授权回灌后权限降级。（注：本条当时附带把位 12 `ACL_SET` 从小写 `'s'` 改成了大写 `'S'`，属误改——PG9.5~PG18 官方 `acl.h` 的 `ACL_SET_CHR` 均为小写 `'s'`，v1.0.66 已纠正，详见 v1.0.66 记录 B8/B9）；
  ⑤ **稀疏大对象补零（P1）**：`pg_largeobject` 官方支持稀疏存储（`lo_truncate`/lseek 越过 EOF），缺页应补零而非整对象拒绝；现按 `LOBLKSIZE=BLCKSZ/4` 补零导出并留告警，重复页仍整对象拒绝；
  ⑥ **金仓 `pg_auth_members` 布局锚定（P1）**：金仓 V8/V9 把系统列（tableoid/cmax/xmax…）列为正 attnum，`[tableoid,cmax,xmax]` 恰好全 4B 通过旧校验 → cmax/xmax 被当 roleid/member，成员关系静默全丢；校验补 admin 1B bool 锚点，`has16` 改为只从校验通过的布局推导；
  ⑦ **PG18 虚拟生成列 + NOT NULL（P1）**：虚拟列不落盘、INSERT 不含该列，DDL 若保留 NOT NULL 则回灌每条 INSERT 失败；现对 `attgenerated='v'` 剥离 NOT NULL 并输出补建注记；
  ⑧ **其余（P2/P3）**：GB18030 截断兜底对 ASCII 字节原样直通（旧版产出 C0/C1 非法 UTF-8）、`"char"` 非 ASCII 字节按原始字节透传（旧版 `rune()` 化不可逆）、CHECK 注记不再恒打印 `(oid 0)`、`GetNulls` 删除 PG<12 反转分支（`t_bits` 语义全版本一致）、空大对象补 `lo_create`、`lo_from_bytea` 改 `decode('hex')`（对 `standard_conforming_strings=off` 免疫）、`--large-object-oids` 非法项/零命中告警、`import_all.sh` 改为任务依赖序（分区父表先于子表，替代文件名字典序）、表空间序列兜底补 `sys_tblspc`、`--report-file` 同文件判定升级为 inode 级 `os.SameFile`（防 symlink 绕过）、内联压缩解压失败留日志。审计另提出 "`sys_` 前缀判据过宽"，复核后**维持原判据**（金仓内置 schema 确用 `sys_` 族，收紧会让系统表坏块计入用户 verdict 形成假阳性）。新增 21 个回归用例（timetz[]/macaddr[] 对齐、位图、aclitem、GB18030 截断、空 LO、decode 形式、金仓 `pg_auth_members` 布局、`import_all.sh` 依赖序、`--report-file` symlink、`GetNulls` 跨版本语义）。**升级须知**：`large_objects.sql` 的 `lo_from_bytea` 由 `'\x..'` 改为 `decode('..','hex')`；`import_all.sh` 条目顺序变为任务依赖序（父表在前），下游逐行基线需更新。

- **v1.0.64**：**三路深审 14 条闭环（E-1~E-14）**——① **PG18 虚拟生成列**（`attgenerated='v'` 不落盘，旧版当普通列解析导致该列之后整行错位；现保留槽位但解析/INSERT/CSV 跳过，DDL 留列 + 补建提示，其表达式不再误导出为 DEFAULT）；② **COLLATE 限定名**（自定义 collation 统一输出 `COLLATE <schema>."name"`，不再裸名依赖 search_path）；③ **导入脚本注入补漏**（`\copy` 文件名与列清单改 `shellDQLEscape`，堵住表名之外的最后两个注入入口）；④ **`\copy` DELIMITER 与 `--delimiter` 联动**（旧版硬编码 `,`）；⑤ **导入脚本自动建库**（`MAINTDB` 可指定维护库，目标库不存在时 `CREATE DATABASE`，消除首轮全 FAIL 假象）；⑥ **表空间序列**（`pg_tblspc/sys_tblspc` 路径解析读 `last_value`，失败输出告警不再静默）；⑦ **`import_all.sh` 按文件名排序**（旧版按并发完成序，分区子表可能先于父表）；⑧ 报告守卫（`--report-file` 与 `-o` 同路径拒绝覆盖、`--table-name a,b` 失败补写报告、纯 DDL 模式 `--checksum` 对 `.ddl` 实际内容计算）；⑨ 杂项（import-script 模式与导出类型不匹配告警、元组越界记 bad_pages、`pg_sequence` INCREMENT=0 防御、`lo_put` 提示 9.4+）。**升级须知**：`import_*.sh` 头部多出 ensure-DB 段（目标库不存在会自动创建）；`\copy` 的 `DELIMITER` 跟随 `--delimiter`，下游逐行校验脚本请更新基线。
- **v1.0.63**：**25 条缺陷三轮闭环（6×A + 13×B + 6×C），全部实机复现→修复→回灌比对**——核心是"导出的 DDL/数据回灌后必须与源库等价"：① **序列**：`CREATE SEQUENCE` 补齐 `AS/INCREMENT/MINVALUE/MAXVALUE/CACHE/CYCLE`（此前只有 `START WITH`，`INCREMENT≠1` 或序列有空洞时**回灌后 nextval 必撞主键**），`setval` 改用序列自身 `last_value/is_called` 真值而非猜 `MAX(列)`；**独立序列（非 IDENTITY 附属）此前完全不导出**，现随 `--full-schema` 输出（`--schema-objects=sequence` 可关，须在各表建表 DDL 之后执行）；② **约束**：`UNIQUE`/`EXCLUDE` 在 PG 里以索引形式存在，旧版当普通索引输出——`DEFERRABLE INITIALLY DEFERRED` 丢失、**`EXCLUDE` 排他约束直接凭空消失**；现从 `pg_constraint` 还原 `ADD CONSTRAINT ... UNIQUE/EXCLUDE USING <am>`，含 `NULLS NOT DISTINCT`、`INCLUDE`、`WHERE` 谓词与 opclass（如 `"code" "text_pattern_ops" WITH =`）；③ **表属性**：补 `attidentity`（否则 `GENERATED ALWAYS AS IDENTITY` 退化成普通 `int4`，回灌后不自增，且 INSERT 自动带 `OVERRIDING SYSTEM VALUE`）、`attcollation`（`COLLATE "C"` 不再丢）、`relpersistence='u'`（`CREATE UNLOGGED TABLE`）；④ **大对象**：`pg_largeobject_metadata` relfilenode 误用已废弃的 2614（旧 `pg_listener`）→ 单对象导出 owner 全丢，订正为 2995；外部构造条目按 `DataHex` 反推长度，20MB 对象不再被判成小对象走 `lo_from_bytea`；⑤ **正确性**：xid 回绕后元组可见性判据收紧为仅 `txmin == 0`（旧判据会把未来事务的元组误当已删）、varlena 累加前校验页面边界、`FindTableInMeta` 裸表名稳定选取（旧版随机 schema）、`readFullPage` 替换短读即截断、UTF-8/GB18030 兜底只对真正非法字节生效；⑥ **`pg_am` 兜底表订正**（gist 2745→783、spgist 2747→4000），未知 AM 不再静默降级成 btree；⑦ **扩展成员不再误导**：pageinspect/btree_gist/plpgsql 等扩展装进 `public` 后其函数被当用户函数输出，`LANGUAGE c` 缺 `probin` → 回灌报 `could not access file "brin_metapage_info"`；现按 `pg_depend`（`refclassid=pg_extension`/`deptype='e'`）过滤，并在产物首部提示"目标端先 CREATE EXTENSION"，同时补读 `probin` 让非扩展 C 函数输出正确的 `AS '<库>', '<符号>'`。**升级须知**：`.ddl` 新增 `ADD CONSTRAINT`/序列段/`COLLATE`/`IDENTITY`/`UNLOGGED`，下游有逐行校验的请更新基线；工具仍不输出 `CREATE SCHEMA`，跨 schema 回灌前请先建 schema、并先 `CREATE EXTENSION` 装齐依赖扩展（`EXCLUDE USING gist` 需 `btree_gist`）。
- **v1.0.62**：**批量 DDL-only 放开 + 审计收尾（P2-3/6/7/8 + P3 健壮性）**——① `--schema s --ddl`（不给 `--sql`/`--data`）现在可用，逐表输出 `.ddl`；只要库级产物时也可单给 `--full-schema`/`--large-objects`/`--roles`（此前一律报"批量导出必须指定导出类型"，等于批量建表 DDL 无法产出）；② 批量 `--jobs>1` 下共享 Options 被回写 `DBOID` 的竞态修复（可能按错误库目录定位）；③ `pg_control` 兼容 PG 9.6（`CtrlVer=960`）与 `state=0`（`DB_STARTUP`），这两类快照此前整段 `db_state` 证据被丢弃；④ 大对象正文超过 32MB 溢写临时文件（4GB 对象不再需要 8GB+ 内存峰值），>2GB 时提示目标端需 `lo_put(offset int8)`（PG 9.3+）；⑤ 钱包多表空间按记录步进匹配 OID（逐字节扫描会误取密钥、解出错误表空间密钥导致整库乱码）；⑥ 所有 SQL/DDL 输出首部新增 `SET standard_conforming_strings = on`（**回灌脚本多两行，属预期变化**）；⑦ `attalign` 小写、密钥脱敏中文、IANA 时区夏令时、系统表坏块归属、免费版提示文本、预扫 IO/进度、扫描回退 CTID 语义等健壮性问题一并处理。**升级须知**：导出的 `.sql`/`.ddl` 首部多出 `SET` 行，若下游有逐行校验请更新基线。
- **v1.0.61**：**深度审计修复第三批（DDL 语义补全，1×P1 + 4×P2）**——① `--full-schema` 函数段按 `prokind/proretset/proargmodes` 还原：返回集合的函数补 `RETURNS SETOF`（此前丢掉 SETOF，回灌后调用方式完全不同）、存储过程输出 `CREATE PROCEDURE`、`OUT/INOUT/VARIADIC/TABLE` 参数按模式还原（有 OUT 时不再写 `RETURNS`，TABLE 模式输出 `RETURNS TABLE (...)`）；**聚合/窗口函数不再伪造成普通函数**，改为注释明示跳过（离线无法还原 `pg_aggregate`）；② 函数签名里的自定义类型加 schema 限定与引号（跨 schema 同名类型不再绑错、含大写/空格/保留字的类型不再语法错误），数组输出 `"elem"[]`；③ `nextval('schema.seq'::regclass)` 的标识符按字面量规则转义（序列名含 `'` 不再破句）；④ `PRIMARY KEY (a) INCLUDE (b)` 不再把 b 并进主键（此前唯一性被从 a 收紧到 (a,b)），非主键索引单独输出 `INCLUDE (...)`；⑤ 普通 `INHERITS` 子表不再被当作分区子表（判据改为 `relpartbound` 存在或父表 `relkind='p'`），分区元数据缓存键补 `version`/`isKB`；⑥ hasoids 目录（PG<12 / 金仓 V8）的 `pg_authid`/`pg_auth_members` 解析修正 NULL 位图错位（密码/有效期为空时不再读到邻居字段的字节）。
- **v1.0.60**：**深度审计修复第二批（4×P1 + 5×P2，静默退化路径一律改为硬校验）**——① 金仓堆页（版本 5）的页头长度修正：此前 ItemId 数组从 `pd_prune_xid` 起算，**所有 CTID 页内偏移错一位**，prune_xid 形似 ItemId 时还会多出一条垃圾行（内容此前"侥幸"正确，故长期未暴露）；② 单文件模式的自动编码探测此前被页大小=0 短路（v1.0.44 特性实际失效），现先探页大小再判编码，`unknown` 不进编码基线；③ `--table-name a,b` 配单文件位置参数由静默错行改为报错；④ TDE 大对象补密钥校验（错密钥不再静默导出乱码 LO）、默认表空间按 `pg_default` 查表空间密钥、给了钱包参数却无解密器时硬报错；⑤ 金仓编码表不再继承上游 45–48 兜底、未知/负编码编号显式告警、`pg_database` 定位不再硬编码 `global/1262`；⑥ 批量编码预扫不再以 ps=0 空转（混合编码漏判会导致 `--jobs>1` 表级乱码）；⑦ `__TOAST_CORRUPT__` 与 `__TOAST_MISSING__` 统一按 NULL 输出（不再回灌出字面量垃圾），串行解密失败不再丢整段，扫描回退路径补齐解密；⑧ `--page-size` 非法值拒绝、输出文件名防 `../` 穿越、导入脚本防命令注入、段文件缺号与主段 0 字节显式告警、`--report` 写失败改为非 0 退出。**行为变化**：`--page-size` 只接受 1024/2048/4096/8192/16384/32768/65536；多表/批量模式不再接受单文件位置参数；`--report` 写失败会以非 0 退出码结束。

- **v1.0.59**：**深度审计修复第一批（4×P0 + 3×P1，正确性红线）**——① 损坏的 `pg_node_tree`（截断 CString）不再使导出无限挂死；② `--full-schema`/`--large-objects`/`--roles` 不再覆盖 `-o` 指定的数据文件（辅助产物改落同目录固定文件名并提示）；③ `-o` 指向源数据文件/符号链接时直接拒绝（防源库被清零）；④ 大对象页序列不连续（缺页/重复）时拒绝输出该对象并计入报告 skipped 清单；⑤ PG12+ 用户函数体导出为空的 NULL 位图错位修复；⑥ 目录行解析失败列不再把同值复制进后续列；⑦ oidvector/int2vector 经 `DecodeValue` 路径的输出修正。`--report` 与 skipped 清单是发现 ③④ 类问题的现场抓手，导出取证场景建议始终开启。

- **v1.0.58**：**P1 修复**——金仓 GBK/GB18030 等非 UTF8 多字节编码库导出时数据被转义为 `E'\x…'` 文本（v1.0.56/v1.0.57 均受影响）。修复后正确解码输出。受影响版本可用 `--encoding gbk` 临时规避；`--encoding` 参数用法见 4.x 编码章节。
- **v1.0.57**：目录结构重构，**对外行为与参数零变化**（导出产物与 v1.0.56 逐字节一致）。仅影响「从源码构建/二次开发」的场景：入口变为 `go build ./cmd/pg2sql`，业务代码分置于 `internal/` 各包；二进制使用方式、参数、输出文件、`report.json` 字段完全不变。
- **v1.0.43-45**：并发巡检稳定性修复、目录解析加固。
- **v1.0.46**：多实例并发巡检（`--jobs`）、ORACLE 兼容模式探测、regclass 反查。
- **v1.0.47**：schema 对象导出（函数/触发器/FK/CHECK/视图）、大对象导出、`--progress`。
- **v1.0.48**：P0/P1 修复（数组解码越界守卫、枚举/复合/域数组解码、标识符转义、schema 限定）、DDL 独立 `.ddl` 文件、IF NOT EXISTS + DO 块防重复、多段映射按页大小自适应、导入脚本先建表。
- **v1.0.49**：DO 块 dollar-quote 唯一 tag 探测、`.ddl` 输出碰撞改 `x.schema.ddl`、TDE 密文页探测偏移修复、iterSysTuples 改 range-over-func（消除 goroutine/fd 泄漏）、时区参数严格校验。
- **v1.0.50**：**域 CHECK 约束离线还原**——从 pg_constraint conbin 节点树反解出 `CHECK (expr)` 并还原到 CREATE DOMAIN（支持 PG13- 的 contype='d' 与 PG14+ 的 contype='c'+conrelid=0 两种存储形式；支持比较/逻辑/IN/函数/转型/NULL 判断节点，常量覆盖整数/浮点/numeric/文本/日期/时间；无法还原的表达式回退为人工提示注释）。
- **v1.0.51**：`pg_node_tree` 反解内核（`nodetree.go` 递归下降解析 `{NAME :key value}` / 字节列表 / CONST 两种编码），支撑列默认值、表达式索引、部分索引还原。
- **v1.0.52**：分区表 DDL 离线还原（父 `PARTITION BY` + 子 `PARTITION OF … FOR VALUES …`，含 DEFAULT/多级/表达式键，父→子拓扑排序）；表级编码精化（逐表按自身数据文件三态判定，仅 utf8 单向覆盖，混合存储自动串行）。
- **v1.0.56**：遗留问题修复轮（三批 16 项 + R2/R3 专项）。① multi-xmax 判活：行级锁 + 更新并发下已提交删除行不再被静默导出（按 `HEAP_KEYS_UPDATED` 区分 updater 型 multi）；② 编码枚举校正：GB18030/BIG5/SJIS/UHC/JOHAB 的编码 ID 不再误映射（**GB18030 库生僻字静默乱码修复**），未实现的编码走告警 + hex 可逆输出；③ 函数 DDL：`proargtypes`（oidvector 4 字节）改用专用解析器，多参函数签名不再错位；④ `--table-name <分区父表> --count` 多叶子改逐叶统计；⑤ 复合类型 recordQuote 对齐 PG（空串/反斜杠/空白正确加引号）；⑥ meta.json 往返保留 `attgenerated`（生成列不再产出非法 `SET DEFAULT`）；⑦ 加固：短文件越界、jsonb/nodetree 坏数据 panic 与死循环、4713 BC~0000-02 日期解算错乱（`civilFromDays` 负 era）、BC 日期输出、TDE 错密钥不再截断旧输出、标识符引号、`--schema-objects`/`--table-name` 参数校验、系统目录多段、大对象 O(n²) 与 1GB 超限分块、`--count` 跳过 TOAST 预建；⑧ 段边界统一（`segPageTable` 按实际文件大小）+ 归并窗口超限告警。
- **v1.0.55**：判定与退出码修正（B1~B3 + R1）。① B1：VACUUM 空页/全零页不再计为坏块——旧版 `iterTuples` 用 nil 混淆「布局非法/全零页/合法空页」，删半 + VACUUM 后的正常表被误报「坏块 102」、verdict=fail（导出本身完全正确）；② B2：不存在的位置参数、fsm/vm 辅助文件、按 relfilenode 重定位失败均硬报错（非 0 退出码 + 明确文案），不再静默导出 0 字节文件；fsm/vm 输入自动纠正到真实数据文件；③ B3：`--count` 恒为全表计数，`--limit` 仅影响数据导出；④ R1：批量导出单表 panic 隔离为该表失败（`safeExportOneTable`），不再整进程崩溃。
- **v1.0.54**：四个现场缺陷修复（C1~C4）。① C1：单表 `--table-name <分区父表> --sql/--data` 只导出叶子、父表 `CREATE TABLE` 丢失 → 回灌报 `relation does not exist`，现与批量模式统一自动带出父表 DDL（单叶子另在数据前前置父表 DDL），点名导出子表时提示需先建父表；② C2：`.sql` 首行内嵌 `SET client_encoding = 'UTF8';`，修复 GBK/GB18030 库直接回灌时的中断与静默双重编码；③ C3：密文页判据由「16B 全零块 <8」改为「数据区零字节密度 <0.02 且元组头不可解析」，消除满页（空闲 <128B）被误报为「TDE 加密、输出不可信」；④ C4：表级编码精化改为多页投票的双向覆盖，修复混合存储库中真 GBK 表整表乱码。
- **v1.0.53**：完整性自证（`db_state`/`summary`/`rows_*`/`pages_*`/`skipped`/`unknown_types`/`verdict`）+ 角色导出（`--roles` / `--roles-nopassword`）；`--all-tables` 的共享目录改为明确跳过（不再整体失败）；新增 `--report-file`（指定报告路径），并加解析期守卫（位置参数唯一，避免 `--report <路径>` 把数据目录换掉；0 表 0 跳过判 `fail`）。
