// Author: raysuen
package cli

import (
	"fmt"
	"pg2sql/internal/paid"
)

func PrintHelp() {
	fmt.Println(`pg2sql - PostgreSQL/KingbaseES 堆文件离线解析导出工具 (Go 版 v` + Version + paid.PaidVersionSuffix() + `)

用法:
  pg2sql <数据文件> [选项]

核心示例:
  pg2sql data_file --sql                     # 自动发现表结构，导出 INSERT 语句
  pg2sql data_file --ddl --sql               # 同时导出 DDL + INSERT
  pg2sql data_file --data --header -o out.csv  # 导出 CSV（首行字段名，COPY 兼容）
  pg2sql data_file --text -o out.txt         # 导出 COPY text 格式（制表符分隔 + \N，psql \\copy 默认格式）
  pg2sql /pgdata/pg_tblspc/16391/PG_17_202307071/16384/16391 --sql   # 表空间表：软链接路径自动发现（免额外参数）
  pg2sql /ts_data/PG_17_202307071/16384/16391 --datadir /pgdata --db-oid 16384 --sql   # 表空间表兜底：真实路径 + 显式 catalog 定位
  pg2sql data_file --datadir /pgdata --db-oid 16384 --sql
  pg2sql data_file --count                   # 统计行数
  pg2sql data_file --sql --deleted           # 含已删除行（-- DELETED ctid 注释）
  pg2sql data_file --sql --only-deleted      # 只导出已删除行
  pg2sql data_file --sql --fields id,name   # 只导出指定字段
  pg2sql data_file --data --encoding gbk     # 指定库编码
  pg2sql data_file --count --clog --datadir /pgdata --db-oid 16384   # 读 CLOG 复核可见性（崩溃恢复/物理拷贝场景）
  pg2sql --datadir /pgdata --list-db         # 列出数据库
  pg2sql /pgdata/base/16384 --list-tables-db   # 列出库内用户对象（默认）
  pg2sql /pgdata/base/16384 --list-tables-all  # 列出库内全部对象（含系统对象）
  pg2sql /pgdata/base/16384 --tables --sql -o /out/            # 批量导出全部用户表（SQL）
  pg2sql /pgdata/base/16384 --all-tables --data -o /out/       # 批量导出全部表（含系统对象，CSV）
  pg2sql /pgdata/base/16384 --schema ray --sql -o /out/           # 指定 schema 批量导出（等价 --tables --schema）
  pg2sql /pgdata/base/16384 --export-meta -o meta.json
  # v1.0.34：--datadir + --db-oid 目录模式（等效 base/<oid>，表空间表自动发现）
  pg2sql --datadir /pgdata --list-db             # 列出数据库 + filepath（多表空间多行显示）
  pg2sql --datadir /pgdata --db-oid 16384 --list-tables-db   # 列库内表 + 表空间列（0=base）
  pg2sql --datadir /pgdata --db-oid 16384 --schema public --sql -o /out/   # 批量导出（含表空间表）
  pg2sql --datadir /pgdata --db-oid 16384 --table-name public.t_ts --sql   # 单表导出（含表空间表）

选项:
  --catalog-json FILE   表结构 JSON（不指定则自动发现）
  --datadir DIR         PG 数据目录。目录模式（配合 --db-oid）等效 base/<oid>：--list-tables-db/
                        --schema/--table-name/--tables/--all-tables 自动发现表空间表
                        （reltablespace!=0 的表定位 pg_tblspc/<oid>/<版本目录>/<dboid>/<rf>，v1.0.34）
  --db-oid OID          目标库 OID（默认 5；表空间表软链接路径可自动识别，无需显式指定）
  --table-name NAME[,NAME...]  指定表名（逗号分隔多表；多表必须 -o 目录 + --sql/--data；含逗号表名用双引号包裹，shell 单引号包双引号）
      示例：--table-name a,b            2 张表（a、b）
            --table-name '"a,b"'       1 张表（表名含逗号：a,b）
            --table-name '"a,b",c'     2 张表（含逗号表名 a,b + 普通表 c）
            --table-name 分区父表       自动展开全部叶子子分区导出（v1.0.36：多叶子需 -o 目录+--sql/--data；单叶子保持单表；混合场景同样展开）
  --ddl / --sql / --data / --count
  --format FMT          数据格式单选（v1.2.5，推荐写法）：sql=INSERT（默认）、csv=CSV 数据、
                        text=COPY text（等效旧 --text）。只能指定一次，不能与 --sql/--data/--text
                        混用。--header 仅在 csv 格式下可用。
  --text                COPY text 数据格式（v1.1.1，v1.2.5 起兼容保留，推荐 --format text）：
                        制表符分隔、反斜杠转义（\b\t\n\v\f\r \\）、
                        NULL 记作 \N，无表头行；产物后缀 .txt，可直接
                        \copy 表 from 'x.txt' with (format text) 回灌（psql/ksql 均支持）。
                        与 --sql/--data 互斥（最多指定一个）。
  --deleted / --only-deleted
  --clog                （v1.2.0）读事务状态日志复核元组可见性。
                        默认只看堆元组里的 hint bit（HEAP_XMIN_COMMITTED/INVALID 等），
                        而 hint bit 是缓存、非权威状态：崩溃恢复中断的事务、物理拷贝/
                        裸设备导出的堆文件可能从未被写过 hint，此时回滚残留行会被当活行
                        导出。加本参数后，对 hint 缺失的元组额外查
                        <PGDATA>/pg_xact/XXXX（金仓为 sys_xact，自动识别），
                        每事务 2 bit：1=已提交 2=已回滚。
                        要求 --datadir，且 CLOG 已落盘（导出前先 CHECKPOINT 或停机）；
                        读到 IN_PROGRESS 时一律按活行导出并告警，绝不据此判死。
                        常规库（hint 覆盖完整）加不加结果相同，仅多一点开销。
                        hint 缺失占比超 10% 时，即使不加本参数也会告警提示。
  --list-db / --list-tables-db / --list-tables-all / --export-meta
                        --list-db：filepath 列多行（base 一行 + 每个表空间一行，v1.0.34）
                        --list-tables-db/all：含"表空间"列（0=base，非 0=pg_tblspc/<oid>，v1.0.34）
  --tables / --all-tables    批量导出用户表/全部表（配合 --sql/--data，必须 -o 目录）
  --schema NAME              批量导出指定模式（独立使用等价 --tables --schema NAME）
  -o, --output PATH     输出文件（目录时自动命名 schema.table.sql|.csv|.txt|.ddl）
  --limit N             最多输出 N 行（0=不限制，默认）
  --fields C1,C2        只导出指定字段
  --header              CSV 首行输出字段名（仅 csv 格式可用，v1.2.5：sql/text 格式下报错）
  --no-complete-insert  INSERT 省略列名
  --delimiter CHAR      列分隔符（CSV 默认 ,；--text 默认制表符；制表符写 --delimiter '\t'）
  --toast FILE          TOAST 表文件
  --page-size N         页大小（默认自动探测 8/16/32KB）
  --parallel N          并发 worker 数（TOAST 索引预建 + 主表按页分片 解析→转义→产出字符串，页序窗口保序转发；内存 O(窗口×并行度)，大表不 OOM）
  --jobs N              批量导出（--tables/--schema/--all-tables/--table-name 多表）表间并行数（默认 1=逐表串行，每表独立文件；与 --parallel 表内并行叠加，jobs×parallel≈核数）
  --batch-rows N        多值 INSERT 批大小（默认 1=逐行单条；N≥2 合并为 INSERT ... VALUES (...),(...)，仅 SQL 模式生效）
  --report              导出报告 report.json：逐表行数/字节/checksum/耗时 + v1.0.53 完整性自证
                        （db_state 关库状态、reltuples/relpages 对照、skipped 跳过清单、unknown_types 类型覆盖度、verdict 结论；无 -o 时打印 stderr）
  --report-file PATH    指定报告落盘路径（默认 <输出目录>/report.json；隐含开启 --report）
  --checksum crc32|md5  输出文件流式计算校验和（写入报告；仅文件输出生效）
  --roles               角色 DDL 导出（离线解析 global/pg_authid + pg_auth_members）：
                        CREATE ROLE 全属性 + 密码密文 + VALID UNTIL + GRANT 成员关系，输出 roles.ddl
  --roles-nopassword    同 --roles，但省略 PASSWORD 密文（合规脱敏/交付第三方场景）
  --import-script none|auto|sql|csv   生成导入辅助脚本 import_<schema>_<table>.sh（批量模式 import_all.sh；默认 none=关；sql→psql/ksql -f，csv→\copy 并联动 --fields；auto 按导出类型选择）
  --tz NAME|±HH:MM      金仓 mysql/ora 模式 timestamp 解码时区（默认自动解析数据目录 conf timezone，取最后一处赋值，失败按 UTC+8 并告警；
                        IANA 名按完整时区规则取历史偏移（含 1901 前 LMT/夏令时），数值偏移按固定偏移；如 Asia/Shanghai、+09:00、UTC）
  --full-schema         完整 Schema DDL 导出（需 --ddl）：序列（含独立序列）/函数/触发器/外键/CHECK/视图依赖清单，输出 schema_objects.ddl（无 -o 时追加 stdout）
  --schema-objects LIST 过滤 Schema 对象类别（逗号分隔: sequence,view,function,trigger,constraint,fk；默认全部）
  --large-objects       导出 pg_largeobject 大对象（lo_from_bytea 语句；需库目录；无表名时独立运行）
  --large-object-oids LIST  只导出指定大对象 OID（逗号分隔；默认全部）
  --progress            导出进度与 ETA（stderr 输出：当前表名/页/行/耗时/ETA；TTY 原地刷新，管道逐行；多表批量每表独立进度）` + paid.PaidHelpText() + `
  --encoding CODEC      库编码（默认 auto）

支持类型: PG/金仓内置类型全覆盖——数值/字符/二进制/布尔/位/日期时间/JSON/XML/数组/几何/网络/全文/范围/reg*/pg_lsn/txid_snapshot/枚举
金仓兼容模式: ORA/MySQL/PG 三种模式目录布局一致自动识别；MySQL 特有 TINYINT/MEDIUMINT/YEAR/DATETIME/BINARY/ENUM 已解码，
SET 输出 \\x hex（字节可审计，成员名不落 catalog 无法文本化）；复合类型 record 输出原始字节 E'\\x...' 兜底，字节可逆
  --verbose             详细日志
  --version`)
}
