# pg2sql v1.0.53

**Language: [中文](README.md) | English**

Offline data-file parser and exporter for PostgreSQL / KingbaseES (Go, single binary, zero dependencies).

> 📖 **Full user guide**: [`docs/USER_GUIDE.md`](docs/USER_GUIDE.md) — per-option reference, option combination matrix, scenario topics (partitions / tablespaces / TOAST / bad blocks / encoding), FAQ. The binary package extracts to `pg2sql-<version>-<platform>/` (containing the `pg2sql` binary and the `docs/` manual, shipped together).

Reads PG / Kingbase heap files (including TOAST) directly — no live database needed:

- `--sql`: INSERT statements (auto-discovers table structure, TOAST reassembly, deleted-row audit; combine with `--ddl` to emit DDL)
- `--data`: CSV (`--header` writes column names on the first line, `--delimiter` sets a custom separator)
- `--count`: row counting; `--fields` exports selected columns only
- Auto-discovers table structure, auto-detects page size (8/16/32KB), auto-detects database encoding (UTF-8/GBK/Latin1)
- Supports PG 12~18 and KingbaseES V8/V9 (ORA/MySQL/PG compatibility modes; V9 includes R1C10/R3C18 sub-versions), all with 8/16/32KB block sizes
- `--list-tables-db` lists user objects in the database (system objects filtered by default); `--list-tables-all` lists every object including system ones
- `--tables` / `--all-tables` batch-export all user tables / all tables (including system tables); `--schema NAME` on its own is equivalent to `--tables --schema NAME` (batch export of one schema); multi-table export requires `-o` output directory plus `--sql` / `--data`
- DDL automatically emits PRIMARY KEY (parses pg_index/sys_index, handling the layout differences of PG12-18 / Kingbase V8 / Kingbase V9)
- Partitioned tables: each leaf partition has its own relfilenode, so tables can be exported per-partition via relfilenode or `--table-name`; pointing `--table-name` at a partition parent (relkind='p') expands to all leaf children (v1.0.36, recursive for multi-level partitioning)
- **New in v1.0.38**: `--batch-rows N` multi-row INSERT (default 1 = off, speeds up restore); `--jobs N` table-level parallelism (default 1 = off, one file per table); `--report` export report report.json (tables/rows/bad blocks/files/checksum/elapsed); `--checksum crc32|md5` streaming checksums; `--import-script none|auto|sql|csv` import helper scripts (default none = off; sql→psql/ksql -f, csv→\copy linked with `--fields`)
- **v1.0.45**: P2/P3 defect round (12 of 13 review items fixed): B1 import-script selection, B2/B3 single-table/batch script encoding and HEADER linkage, B4 PG encoding ID mapping, B5 PG18 relnatts per version, B6 int16 two's complement, B7 parallel roleMap race, B9 --tz time zone, B10 interval sign, B11 expression-index warning, B12 TDE wrong-key warning, B13 report double-write. Packaged after full-version regression (PG12-18 + Kingbase V8R6/V9R1C10, 3 modes × 3 page sizes each, multi-charset focus).

- **New in v1.0.40**: `--full-schema` complete schema DDL (with `--ddl`: functions/triggers/FKs/CHECK/view dependency list, filtered by `--schema-objects`); `--large-objects` exports pg_largeobject large objects (`--large-object-oids` selects OIDs, `lo_from_bytea` statements, integrates with `--import-script`); `--progress` export progress and ETA (stderr, in-place refresh on TTY, line-by-line on pipes)

## Build

```bash
go build -o pg2sql .
# cross-compile for aarch64
GOOS=linux GOARCH=arm64 go build -o pg2sql-arm64 .
```

## Usage Examples

> 💡 This is a quick tour of basic usage. **Per-option reference, option combination matrix and scenario topics (partitions / tablespaces / TOAST / bad blocks / encoding / import) live in [`docs/USER_GUIDE.md`](docs/USER_GUIDE.md)**.

```bash
# Export INSERT statements (table structure auto-discovered)
pg2sql /pgdata/base/16384/16391 --sql -o out.sql

# Export DDL + INSERT
pg2sql /pgdata/base/16384/16391 --sql --ddl -o out.sql

# Export CSV (column names on the first line)
pg2sql /pgdata/base/16384/16391 --data --header -o out.csv

# Export selected columns
pg2sql /pgdata/base/16384/16391 --data --fields id,name --header -o out.csv

# When -o is a directory, files are named schema.relname: dir/schema.table.sql|.csv|.ddl (extension by export type)
#   e.g. --sql → /tmp/schema.table.sql；--data --header → /tmp/schema.table.csv；--ddl → /tmp/schema.table.ddl
pg2sql /pgdata/base/16384/16391 --sql -o /tmp/
pg2sql /pgdata/base/16384/16391 --data --header -o /tmp/

# Count rows
pg2sql /pgdata/base/16384/16391 --count

# Include deleted rows / only deleted rows
pg2sql /pgdata/base/16384/16391 --sql --deleted
pg2sql /pgdata/base/16384/16391 --sql --only-deleted

# Specify table name + data directory + database OID (multi-database directories)
pg2sql /pgdata/base/16384/16391 --datadir /pgdata --db-oid 16384 --table-name t1 --sql

# --table-name with comma-separated tables (needs --sql/--data, requires -o directory, one file per table)
pg2sql /pgdata/base/16384 --sql --ddl --table-name t1,t2 -o /out/        # multiple tables (SQL+DDL)
pg2sql /pgdata/base/16384 --data --header --table-name s.t1,s.t2 -o /out/  # multiple tables (CSV)
# Wrap a table name in double quotes when it contains a comma or a double quote (shell needs single quotes outside)
# Contrast: no quotes = comma-separated list; quoted = one complete table name
pg2sql /pgdata/base/16384 --sql --table-name a,b -o /out/               # 2 tables: a, b
pg2sql /pgdata/base/16384 --sql --table-name '"a,b"' -o /out/           # 1 table named a,b
pg2sql /pgdata/base/16384 --sql --table-name '"a,b",c' -o /out/         # 2 tables: a,b + c
pg2sql /pgdata/base/16384 --sql --table-name '"a""b"' -o /out/          # 1 table named a"b ("" = literal ")

# --table-name pointing at a partition parent: expands to all leaf children (v1.0.36, multi-level)
pg2sql /pgdata/base/16384 --sql --table-name public.sales -o /out/      # parent sales expands to sales_2023..2026 (-o must be a directory)
pg2sql /pgdata/base/16384 --sql --table-name public.sales -o out.sql   # single leaf keeps single-table semantics (-o may be a file)
pg2sql /pgdata/base/16384 --sql --table-name public.sales,public.t1 -o /out/  # mixed: expanded parent + plain table
# Note: an empty partition parent (no children) exports 0 rows; legacy INHERITS parents (relkind='r') are not expanded, exported per table

# List databases / tables in a database
pg2sql --datadir /pgdata --list-db
pg2sql /pgdata/base/16384 --list-tables-db      # user objects only
pg2sql /pgdata/base/16384 --list-tables-all     # all objects (including system)
pg2sql /pgdata/base/16384 --tables --sql -o /out/            # batch export all user tables (SQL)
pg2sql /pgdata/base/16384 --all-tables --data -o /out/       # batch export all tables (incl. system, CSV)
pg2sql /pgdata/base/16384 --tables --schema ray --sql -o /out/  # batch export one schema
pg2sql /pgdata/base/16384 --schema ray --sql -o /out/             # same as above (--schema alone triggers batch)

# Tablespace tables (real directory outside base/, auto-discovers the pg_tblspc/ symlink prefix, no extra flags; sys_tblspc/ on Kingbase)
#   pg_relation_filepath returns e.g. pg_tblspc/16391/PG_17_202307071/16384/16391 — prepend the data root and export:
pg2sql /pgdata/pg_tblspc/16391/PG_17_202307071/16384/16391 --sql -o out.sql
# Fallback: dangling symlink or only the real path — use the absolute real path plus --datadir/--db-oid:
pg2sql /ts_data/PG_17_202307071/16384/16391 --datadir /pgdata --db-oid 16384 --sql -o out.sql
# v1.0.34 directory mode (--datadir + --db-oid equals base/<oid>, tablespace tables auto-discovered):
pg2sql --datadir /pgdata --list-db                          # multi-line filepath column (base + one line per tablespace)
pg2sql --datadir /pgdata --db-oid 16384 --list-tables-db    # includes a "tablespace" column (0=base, non-0=pg_tblspc/<oid>)
pg2sql --datadir /pgdata --db-oid 16384 --schema public --sql -o /out/   # batch export (incl. tablespace tables)
pg2sql --datadir /pgdata --db-oid 16384 --table-name public.t_ts --sql -o out.sql   # single table (incl. tablespace tables)

# Export metadata JSON and replay it offline (catalog-json mode)
pg2sql /pgdata/base/16384 --export-meta -o meta.json
pg2sql /pgdata/base/16384/16391 --catalog-json meta.json --table-name t1 --sql --ddl

# Force the database encoding (auto-detected by default)
pg2sql /pgdata/base/16384/16391 --data --encoding gbk -o out.csv

# Parallel parsing (faster on large tables; each worker parses→escapes→emits strings, page-order window keeps output ordered; memory is O(window × parallelism), so large tables do not OOM)
pg2sql /pgdata/base/16384/16391 --sql --parallel 4 -o out.sql

# New in v1.0.38:
# Multi-row INSERT (faster restore: N≥2 merges into INSERT ... VALUES (...),(...), default 1 = one row per statement)
pg2sql /pgdata/base/16384/16391 --sql --batch-rows 1000 -o out.sql
# Table-level parallelism (one file per table, default 1 = serial; stacks with --parallel)
pg2sql /pgdata/base/16384 --tables --sql --jobs 4 -o /out/
pg2sql /pgdata/base/16384 --sql --table-name t1,t2,t3 --jobs 4 -o /out/
# Export report + checksum (report.json: tables/rows/bad blocks/files/bytes/checksum/elapsed)
pg2sql /pgdata/base/16384 --tables --sql --jobs 4 --report --checksum md5 -o /out/
# Import helper scripts (sql→psql/ksql -f; csv→\copy linked with --fields; batch import_all.sh)
pg2sql /pgdata/base/16384/16391 --sql --import-script auto -o /tmp/
pg2sql /pgdata/base/16384/16391 --data --fields id,name --import-script csv -o /tmp/
# New in v1.0.40:
# Complete schema DDL (with --ddl: functions/triggers/FKs/CHECK/view dependencies; --schema-objects filters categories)
pg2sql /pgdata/base/16384 --ddl --full-schema -o schema_objects.ddl
pg2sql /pgdata/base/16384 --ddl --full-schema --schema-objects fk,function -o fk_fn.ddl
# Large objects (pg_largeobject → lo_from_bytea statements; --large-object-oids selects OIDs; works with --import-script)
pg2sql /pgdata/base/16384 --large-objects -o large_objects.sql
pg2sql /pgdata/base/16384 --large-objects --large-object-oids 990001 -o lob.sql
# Export progress and ETA (stderr; in-place refresh on TTY, line-by-line on pipes)
pg2sql /pgdata/base/16384/16391 --sql --progress -o out.sql
# New in v1.0.53:
# Self-verifying report (db_state shutdown state + reltuples/relpages comparison + skip list + type coverage + verdict)
pg2sql /pgdata/base/16384 --tables --sql --report -o /out/
# Role DDL (offline parse of global/pg_authid + pg_auth_members → roles.ddl, with password hashes/validity/memberships)
pg2sql /pgdata/base/16384 --roles -o /out/
pg2sql /pgdata/base/16384 --roles-nopassword -o /out/     # same, but without PASSWORD hashes
```

Import verification: `psql -d newdb -f out.sql` or `ksql -d newdb -f out.sql` (import into the same version you exported from).

## Built-in Type Support (PG 12~18 / KingbaseES V8/V9, all block sizes)

| Category | Types (oid / family) | Notes |
| --- | --- | --- |
| Numeric | int2/int4/int8, numeric, float4/float8, money, oid, serial family, Kingbase MySQL TINYINT(8100)/MEDIUMINT(7016)/YEAR(7025)/UNSIGNED series | Full precision |
| Character | text, varchar, bpchar, name, Kingbase clob(8014), Kingbase MySQL DATETIME(7952)/ENUM (dynamic oid) | Chinese/special characters/newlines/empty strings/NULL; ENUM decoded by enum label |
| Binary | bytea, Kingbase MySQL BINARY(3383) | bytea as reversible 0x hex; BINARY strips the varlena header and trailing \x00 padding, falling back to hex when an interior \x00 exists (byte-reversible) |
| Boolean/bit | bool, bit(n), varbit, Kingbase MySQL BIT(4655) | Bit-string and hex channels |
| Date/time | date, time, timetz, timestamp, timestamptz, interval, Kingbase datetime(7952)/timestamp(7954)/time(7950)/date(7944)/ora_date(8020)/mysql_datetime_in etc. | Includes pre-2000 negative microseconds and the 24:00:00 boundary |
| JSON/XML | json, jsonb (including Kingbase mysql_json 4802), xml (PG 4B marker / Kingbase plain text) | |
| Array | Arrays of any type (1-D/N-D/empty, int[]/text[]/varchar[] …) | `{{1,2},{3,4}}` / `{}` |
| Geometric | point, line, lseg, box, path, polygon, circle | |
| Network | inet, cidr, macaddr, macaddr8 | |
| Full-text | tsvector, tsquery | Byte-identical to PG text output |
| Range | int4range/int8range/numrange/daterange/tsrange/tstzrange | Includes empty and half-open ranges |
| System | regclass/regproc and the reg* family, pg_lsn, txid_snapshot, enum | |
| Composite | record (user-defined, dynamic oid) | **Limitation**: emitted as raw bytes E'\x...' (byte-reversible); prefer online pg_dump. Planned for a later release |

**Limitations**: ① composite (record) disk layout is affected by heap_fill_tuple short-varlena packing and alignment and varies across versions, so it currently falls back to raw bytes (no data corruption); ② Kingbase MySQL `SET` (dynamic oid, typtype=y) is stored as a 16-byte member bitmask whose labels are not in the catalog (neither pg_enum nor typtypmod), so it is exported as `\x` hex (byte-auditable/backup-safe); importing it back through plain SQL/CSV cannot restore it because Kingbase set_in clears the mask — convert at the application layer. All other types decode to reversible text/binary literals.

## Data Reliability

- TOAST external values are automatically linked and reassembled (light index / page-level LRU cache)
- Encoding detection reads `pg_database` first (UTF-8/GBK/Latin1 reversible decoding)
- Page size auto-detected: 8KB/16KB/32KB plus Kingbase variants

## Large Table Export: Memory & Time Estimates (measured in v1.0.30)

**Measured baseline** (PG16.15, 2-core machine, data in the OS page cache): a 50-column table (id + 49 mixed varchar/text columns with Chinese/special characters), 1.5M rows, heap file **1.9GB** (PG 8KB pages, 1GB segments — physically `16459` + `16459.1`; v1.0.30 reads across segments automatically):

| Export mode | Elapsed | MaxRSS |
| --- | --- | --- |
| Serial SQL | 33.3s | 17MB |
| `--parallel 4` SQL | 27.8s | 17MB |
| Serial CSV (with header) | 24.8s | 17MB |
| `--parallel 4` CSV | 18.4s | 17MB |
| `--count` (parallel 4) | 11.8s | 17MB |

Output size: SQL ≈ heap × **0.77** (measured 1.47GB/1.9GB), CSV ≈ heap × **0.51** (measured 0.97GB/1.9GB).

**Memory conclusion**: the v1.0.30 approach keeps memory at O(window × parallelism) — for 1.5M rows / 1.9GB MaxRSS stayed **flat at 17MB** (serial and parallel alike). The old implementation (v1.0.29 and earlier) collected all rows (≈5-6GB for 50 columns × 1.5M rows) and would OOM (a parallel regression run on a 3GB machine once OOM-killed every Kingbase instance). A 50GB table still uses constant memory (≈20MB + a fixed TOAST page-LRU cap), **independent of table size**.

### Memory comparison with v1.0.29 and earlier (50 columns, ~1.3KB per row)

| Table size | v1.0.29 and earlier (collect everything) | v1.0.30 (streaming + page-order window) |
| --- | --- | --- |
| 100K rows | ≈350-400MB | ≈17MB |
| 1M rows | ≈3.5-4GB | ≈17MB |
| 1.5M rows (1.9GB heap) | ≈5-6GB, **guaranteed OOM on a 3GB machine** (measured: OOM-killed all Kingbase instances) | **17MB** (serial / `--parallel 4` alike) |
| 50GB class | ≈170GB+, infeasible | ≈20MB constant (+ fixed TOAST page-LRU cap) |

Memory dropped from **O(rows) to O(window × parallelism)**, independent of table size.

### Time comparison with v1.0.29 and earlier (2 cores, 1.5M rows / 1.9GB heap, across two segment files)

| Mode | v1.0.29 (single-threaded escaping) | v1.0.30 (parse→escape→emit inside each worker) | Gain |
| --- | --- | --- | --- |
| Serial SQL | OOM at this size, no usable data | 33.3s | — |
| `--parallel 4` SQL | OOM at this size | 27.8s | **-17%** vs serial |
| Serial CSV | OOM at this size | 24.8s | — |
| `--parallel 4` CSV | OOM at this size | 18.4s | **-26%** vs serial |
| `--count` | OOM at this size | 11.8s (parallel 4) | — |

> v1.0.29 OOMed at the 1.5M-row scale and produced no comparable timing; at 100K rows all 9 output combinations of the two versions were md5-identical (zero semantic regression). On 8 cores parallel speedup is 2.5~3x (historical measurement).

**Time extrapolation** (linear from the 2-core baseline; output files need extra disk space; more cores make `--parallel` pay off more — 2.5~3x measured on 8 cores):

| Heap size | Serial SQL | par4 SQL | par4 CSV | SQL output | CSV output |
| --- | --- | --- | --- | --- | --- |
| 5GB | ~1.5 min | ~1.2 min | ~0.8 min | ~3.9GB | ~2.6GB |
| 10GB | ~2.9 min | ~2.4 min | ~1.6 min | ~7.7GB | ~5.1GB |
| 20GB | ~5.8 min | ~4.9 min | ~3.2 min | ~15.4GB | ~10.2GB |
| 30GB | ~8.7 min | ~7.3 min | ~4.8 min | ~23.1GB | ~15.3GB |
| 50GB | ~14.5 min | ~12.2 min | ~8.1 min | ~38.5GB | ~25.5GB |

> Method: time = heap size ÷ 1.9GB × measured elapsed, extrapolated linearly. The dominant variables are CPU cores for parsing/escaping and disk write bandwidth (2 cores makes CPU the bottleneck; with fast disks writing does not add much). Numbers assume wide 50-column rows (~1.3KB each); narrow tables have more rows per byte (slightly more per-row cost), wide tables with more TOAST references add reassembly overhead.

### jobs × parallel resource sizing (v1.0.38)

`--jobs N` (across tables) and `--parallel P` (page-level within a table) are orthogonal and multiply:

- **Total parallelism = jobs × parallel**; align it with the core count (oversubscription causes context switching and disk I/O contention — when the rescue disk is unstable, lower `--jobs` first).
- **Memory formula**: `total ≈ jobs × (~12MB per table base + parallel × per-worker increment)`; the per-worker increment is about 2-3MB for 8KB pages, 3-5MB for 16KB and 4-8MB for 32KB (the page-order window is O(parallelism), independent of table size). **Tens to a few hundred MB — always a safe zone.**

Four combinations on an 8-core machine (32KB pages, 8MB per worker):

| Combination | Total parallelism | Estimated memory |
| --- | --- | --- |
| `--jobs 1 --parallel 8` | 8 | ~12MB + 8×8MB ≈ **76MB** |
| `--jobs 4 --parallel 2` | 8 | 4×(12MB + 2×8MB) ≈ **112MB** (recommended, within 4x serial) |
| `--jobs 8 --parallel 1` | 8 | 8×12MB ≈ **96MB** (across tables, lowest per-table memory) |
| `--jobs 8 --parallel 4` | 32 | 8×(12MB + 4×8MB) ≈ **352MB** (badly oversubscribed, not recommended) |

**Rescue guidance**: with an unstable disk or slow I/O, pin `--parallel 2~4` for single-table throughput first, then distribute `--jobs` across cores. Example: 50 tables on 16 cores with `--jobs 8 --parallel 2` → ≈8×(12MB+2×8MB)=224MB, far below the 5-6GB that the old collect-everything design needed for one table.

## Bad Block (Corrupt Page) Handling

pg2sql uses "defend page by page and tuple by tuple, skip the damaged unit, export the rest": when a data page is corrupt the export **does not abort** — everything readable is still written, the damaged parts are skipped and reflected honestly in the row count:

| Level | Defence | Behaviour on corruption |
| --- | --- | --- |
| Page header | lower/upper/special range check (`24 ≤ lower ≤ upper ≤ special ≤ pageSize`), dual detection via `tryStandardLayout`/`tryAutoLayout` | The whole page is skipped, parsing continues with the next page |
| Line pointers | itemid check: flags≠NORMAL, offset/length out of range (`off ≥ pageSize`, `off+ln > pageSize`, `ln < header size`) | Only that item is skipped; other items on the page are unaffected |
| Tuple header | `parseTuple` needs length ≥ 24, `headerConsistent` checks t_hoff in 24~256 and bitmap/oid length matching t_hoff | Inconsistent tuples are treated as corrupt and skipped |
| Transaction state | `isInsertAborted` (XMIN_INVALID and not committed) / `isDeleted` (XMAX deleted) | Aborted rows skipped; deleted rows skipped by default (`--deleted` / `--only-deleted` for audit) |
| Field level | `extractFieldsDirect` sets out-of-range fields to NULL, `varPayload` returns empty for damaged/short TOAST headers, 4B/4BC varlena bounds checks | A bad field becomes NULL/empty without breaking the rest of the row; undecodable bytes fall back to latin-1 (0x00-0xFF byte-reversible, no data corruption) |
| Table level (batch) | Failure counters for discovery and per-table parsing | `--tables`/`--all-tables` prints "succeeded N, skipped N, failed N" at the end; a bad table does not affect the rest |

Notes:
- Skipping happens at four granularities — page, item, tuple, field. The finer the granularity, the less data a bad block takes with it.
- For a single-table export, parseable rows inside a damaged page are still emitted (the row count is lower than reality); batch mode summarizes skipped/failed counts.
- When a TOAST pointer or a TOAST page is damaged, the affected field becomes NULL/empty (no abort) and other fields/rows are unaffected.
- Known limitation: a fully damaged page whose header cannot be recognized is unrecoverable (equivalent to PG's `zero_damaged_pages` behaviour); for page-level detail, cross-check with `pg_filedump`/`--verbose` output.

## Kingbase TDE Transparent Encryption Offline Decryption (v1.0.39)

pg2sql can **decrypt and export** Kingbase TDE-encrypted data files offline (no live instance, no database password), covering both encryption scenarios:

| Scenario | Creation syntax | pg2sql option | Decryption |
| --- | --- | --- | --- |
| Table encryption | `CREATE TABLE t (...) ENCRYPTED BY 'KEY'` | `--enckey KEY` | Data page offset 64+ decrypted with SM4-ECB (header bytes 0-63 are plaintext; key = the ENCRYPTED BY string padded with 0x00 to 16 bytes; TOAST uses the same key) |
| Tablespace encryption | `CREATE TABLESPACE ts LOCATION '...' WITH (encryption=true, encmethod='sm4')` | `--wallet-password PW` (optional `--wallet-dir DIR`) | Wallet chain derived offline: KEK→MK→TSPK (no password needed); tspkey.kr entries are matched per tablespace |

Examples:

```bash
# Table-encrypted table, offline decrypt export (file path mode)
pg2sql /pgdata/base/16384/21747 --sql --table-name public.t_enc --enckey k1ngb2se -o /tmp/out/

# Tablespace-encrypted table (directory mode: tablespace auto-discovery + wallet auto-detection)
pg2sql --datadir /pgdata --db-oid 16384 --table-name public.t_tspc \
  --sql --wallet-password 'Wal1et#Tde2026' -o /tmp/out/
```

Notes and boundaries:
- The tablespace key can be **derived offline without any password** (the wallet travels with the data directory, so it is available after a crash); `--wallet-password` only performs an SM3 check and warns (the check offset differs between V8 and V9 — a "password verification failed" warning does not affect decryption).
- `--wallet-dir` defaults to auto-detecting `<PGDATA>/.wallet`; multiple tablespaces/wallet entries are matched by tablespace OID.
- Decryption is injected on both serial and parallel (`--parallel N`) paths, producing byte-identical output.
- Prerequisite: the Kingbase instance must load `sysencrypt` (first entry of `shared_preload_libraries`) to create encrypted objects; offline decryption does not depend on the extension.
- Parsing encrypted pages without a key yields garbage or 0 rows — if the instance is online and encryption is suspected, check whether the table is `ENCRYPTED BY` or the tablespace has `encryption=true`.

## Feature Coverage Matrix

Full-feature regression = data plane (all-type export→import closed loop across PG12-18 at every block size and Kingbase V8/V9) + parameter plane (behavioural assertions for every CLI option). Since v1.0.33 the parameter plane covers all CLI feature segments and runs together with the data plane on the full-version representative matrix; since v1.0.34 it also asserts "directory-mode tablespace auto-discovery (multi-tablespace + a 50K-row tablespace big table)".

| Feature | Coverage | PG12-18 | Kingbase V8/V9 |
| --- | --- | --- | --- |
| `--sql`/`--data` export (Chinese/special chars/NULL/empty strings/50-column big table) | Data plane + parameter-plane count | 7 versions × 8/16/32K all green | V8 ORA/PG modes + V9 all green |
| `--ddl` table/index/sequence/default/comment | Parameter plane ddl_imp/ddl_struct (rebuild-and-replay closed loop) | 9 combinations green | 3 instances green |
| `--count` | Parameter plane count (exact row comparison) | 9 combinations green | 3 instances green |
| `--fields` selected columns | Parameter plane fields (CSV import closed loop) | 9 combinations green | 3 instances green |
| `--limit` | Parameter plane limit | 9 combinations green | 3 instances green |
| `--parallel` parallel export | Parameter plane parallel (serial/parallel md5 identical) | 9 combinations green | 3 instances green |
| `--batch-rows` multi-row INSERT (v1.0.38) | Parameter plane batch (multi-row import closed loop + md5 identical to row-by-row) | 9 combinations green | 3 instances green |
| `--jobs` table parallelism (v1.0.38) | Parameter plane jobs (jobs>1 vs jobs=1 md5 identical + complete per-table files) | 9 combinations green | 3 instances green |
| `--report`/`--checksum` (v1.0.38) | Parameter plane report (report.json fields + checksum matches an independent computation + bad-block counts) | 9 combinations green | 3 instances green |
| `--import-script` (v1.0.38) | Parameter plane imp_script (sql/csv script generation + content assertions + import_all.sh) | 9 combinations green | 3 instances green |
| `--list-tables-db`/`--list-tables-all` | Parameter plane listdb/listall (user vs system object filtering) | 9 combinations green | 3 instances green |
| `--list-db` | Parameter plane list_db | 9 combinations green | 3 instances green |
| `--tables`/`--all-tables` batch | Parameter plane batch/alltables | 9 combinations green | 3 instances green |
| `--schema` batch | Parameter plane batch | 9 combinations green | 3 instances green |
| `--table-name` (single/multiple/quoted identifiers) | Data plane multi-table + identifier-quoting suite | 7 versions green | V8/V9 green |
| `--export-meta` + `--catalog-json` | Parameter plane meta | 9 combinations green | 3 instances green |
| `--encoding` (database decode encoding) | Parameter plane encoding: explicit UTF8 = auto, LATIN1 closed loop, Kingbase GBK/GB18030 closed loop | 9 combinations green | V9 green; V8 GBK green, GB18030 `SKIP(env)`* |
| `--deleted`/`--only-deleted` | Parameter plane deleted (DELETE without VACUUM, 70/100/30 assertions) | 9 combinations green | 3 instances green |
| `--header` (CSV first-line column names) | Data-plane CSV import closed loop + parameter plane fields | 7 versions green | V8/V9 green |
| Bad-block skipping (page/item/tuple/field) | Parameter plane badpage (dd corrupts a middle page) | 9 combinations green | 3 instances green |
| Tablespaces (real directory outside base/, auto-discovery + fallback) | Parameter plane tablespace + tspace_big/tsdir_batch/tspace_col | 9 combinations green | 3 instances green |
| Directory-mode tablespace auto-discovery (v1.0.34: `--list-db` multi-line filepath / `--list-tables-db` tablespace column / `--schema`, `--table-name`, `--all-tables` locating tablespace tables) | Parameter plane listdb_multi/tspace_col/tsdir_batch/tspace_big (multi-tablespace + 50K-row Chinese/special-char tablespace big table, SQL+CSV import closed loop) | 9 combinations green | 3 instances green |
| Kingbase TDE transparent encryption (v1.0.39: `--enckey` table encryption / `--wallet-password`+`--wallet-dir` tablespace encryption, incl. TOAST) | Full-version encryption suite (reg_tde_r1c10.sh + V8/V9 measured): 100 rows with TOAST long text + 50 rows plaintext tablespace verification | N/A (PG has no TDE) | V8R6 ORA/PG 8/16/32K + V9R1C10 3 modes × 3 page sizes + older V9: 14 instances, 28/28 green |
| Large-table export (100K-row t_big + 50K-row tablespace big table; 1.5M-row suite) | Parameter plane parallel/badpage + data-plane big-table suite | 9 combinations green | 3 instances green |
| TOAST external/compressed/cross-segment (>1GB) | Data-plane TOAST suite + 1.5M-row suite | 7 versions green | V8/V9 green |
| Multi block-size auto-detection (8/16/32K) | Data plane + parameter plane | 12.22/16.15/18.6 × 8/16/32K green | V8 8/16/32K + V9 8K green |

\* On V8 instances (ORA/PG modes) the ksql client cannot safely write UTF8 SQL into a GB18030 database (`invalid byte sequence`) — a test-environment limit, not a tool defect. GB18030 decoding is a byte-level table lookup (independent of the kernel version), so full assertions are covered on V9; GBK decoding is verified on both V8 and V9.

## Capability Matrix: Exact / Approximate / Unsupported (v1.0.53)

The usual failure mode of offline heap export is not an error but **silent incompleteness** — exit code 0, files produced, yet a segment file, some TOAST data or whole tables are missing. The table below groups capabilities by restore fidelity and names the self-verification evidence for each tier (all fields come from `--report`).

| Tier | Meaning | Coverage | Evidence |
| --- | --- | --- | --- |
| **Exact restore** | Bytes/semantics identical to the source; after import, query results match | All built-in type values (numeric/character/binary/boolean/bit/date-time/JSON/XML/array/geometric/network/full-text/range/reg*/pg_lsn/txid_snapshot/enum); TOAST external + compressed + cross-segment; four-level bad-block skipping; deleted rows (`--deleted`/`--only-deleted`) | `exports[].rows` + `bad_blocks`/`toast_missing`/`toast_corrupt` + `rows_check`/`pages_check` |
| **Structural restore** | DDL rebuilds objects as-is; structure equivalent after import | Table/columns/defaults/PK/unique/CHECK/FK/indexes (incl. expression and partial)/sequences and setval/comments/tablespaces/partitions (parent `PARTITION BY` + child `PARTITION OF`, incl. multi-level and DEFAULT)/domain types and domain CHECK/functions/triggers/view dependency list/large objects | `exports[]` entries with `format=ddl`; `schema_objects.ddl` from `--full-schema` |
| **Approximate output** | Value reversible (byte-faithful) but the text form may differ from the source, or structure needs manual confirmation | Types without a dedicated decoder (internal types such as `pg_ndistinct`/`pg_mcv_list`/`pg_dependencies`) → text passthrough or `\x` hex; Kingbase MySQL `SET` → hex; `record` composite values → record text | `unknown_types[]` (oid/name/column list) + `verdict.reasons` |
| **Unsupported / manual** | Not produced or explicitly skipped; the reason is visible in the report | Shared-catalog data (`pg_database`/`pg_authid`/`pg_tablespace` and other `global/` objects, `skipped[].reason=shared_catalog`; use `--roles` for roles); "data" of indexes/views/sequences/TOAST and other non-row-store objects; "data" of partition parents (no storage); automatic expansion of legacy `INHERITS` parents | `skipped[]` (reason: `shared_catalog`/`relkind`/`partition_parent`/`no_storage`/`schema_filter`/`system_schema`/`named_missing`/`partition_no_leaf`) |

**Suggested reading order**: start with `verdict.level` (ok/warn/fail); when `fail`, follow `reasons` into `exports[]`/`skipped[]`; then check `summary.tables_rows_under` / `tables_pages_short`. The latter means the measured page count is below `pg_class.relpages`, which usually indicates a missing or truncated segment file — a hard signal to retake the snapshot.

## Self-Verifying Report: report.json (v1.0.53)

Since v1.0.53 `--report` no longer just records "how much was exported" — it answers "how much should have been exported, what is missing, and why".

```jsonc
{
  "tool": "pg2sql", "version": "1.0.53", "generated_at": "2026-10-27T12:41:53+08:00",
  "db_encoding": "utf-8",          // decode encoding actually used for this export
  "encoding_baseline": "utf-8",    // database-level baseline (per-table overrides in exports[].enc_override)
  "page_size": 8192, "jobs": 4, "checksum_alg": "md5",
  "verdict": { "level": "warn", "reasons": ["…"] },     // ok | warn | fail + reasons
  "summary": {
    "tables_exported": 76, "tables_skipped": 403, "tables_failed": 0,
    "rows_exported": 23222, "rows_estimate": 23041,
    "tables_rows_under": 0, "tables_pages_short": 0,
    "bad_blocks": 0, "bad_blocks_system": 0, "toast_missing": 0, "toast_corrupt": 0,
    "bytes": 5679564, "elapsed_ms": 769, "unknown_types": 3
  },
  "db_state": {                    // snapshot provenance (offline parse of pg_control / Kingbase sys_control)
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

Field reference:

| Field | Value | Meaning and action |
| --- | --- | --- |
| `db_state.control_state` | `DB_SHUTDOWNED`(1) | Cleanly shut-down snapshot — best page-level consistency |
| | `DB_IN_PRODUCTION`(6) | Taken from a running instance; recent commits may not be on disk → `CHECKPOINT` the source and retake |
| | `DB_IN_CRASH_RECOVERY`(4) / `DB_IN_ARCHIVE_RECOVERY`(5) | In recovery; data may not have reached a consistent point → import verification is mandatory |
| `exports[].rows_check` | `match`/`under`/`over` | Judged under/over when the measured row count deviates from `reltuples` by more than 10%; `under` is usually dead-tuple bloat but may also mean missing rows |
| | `unanalyzed` | `reltuples = -1` (never ANALYZEd) — no estimate to compare with |
| | `limited`/`filtered` | `--limit` or `--deleted`/`--only-deleted` was used, so counts are not comparable |
| `exports[].pages_check` | `short` | **Measured pages < relpages**: segment file missing or truncated → verdict `fail`, retake the snapshot |
| | `nostats` | `relpages = 0` but the table has data (never analyzed) — informational only |
| `summary.bad_blocks_system` | — | The part of bad blocks falling in system schemas: Kingbase system tables use a private write path (page header item directory not updated) and look corrupt — these only downgrade to `warn`; **bad blocks in user tables still mean `fail`** |
| `unknown_types[].kind` | `fallback` | The type fell through to decodeDefault (text passthrough or `\x` hex): byte-reversible, but the text form may differ from the source |
| | `unresolved` | The oid is absent from `pg_type`; DDL degrades to the `oid` type (add the type definition manually) |

Output and exit codes: `--report` is a switch (takes no path) and writes `<output dir>/report.json`; use `--report-file PATH` for another location; without `-o` the report goes to stderr. **The exit code only means "the run finished", not "the export is complete"** — `verdict=fail` still exits 0, so pipelines must parse `verdict.level`. Since v1.0.53 a second positional argument is an immediate error (older versions treated the path after `--report` as the data directory and silently exported 0 tables / 0 rows); discovering nothing also no longer yields `ok`.

Two kinds of "wrong option" are **hard errors** since v1.0.53 (non-zero exit, because this is usage error rather than data quality):

- An explicit `--schema xxx` that matches no table → `--schema xxx 未匹配到任何可导出表（检查模式名大小写/是否存在；用 --list-tables-db 查看库内表清单）`. Older versions printed "succeeded 0 tables" and exited 0, which is easily misread as "the database is empty".
- Two positional arguments → error pointing at `--report-file`.

## Differences from Native Tools (pg_dump / ksql)

### Export report

| Dimension | pg_dump family | pg2sql `--report` |
| --- | --- | --- |
| Prerequisite | Requires a live database connection | Only the data directory (shut-down / stopped / forensic image all work) |
| Consistency basis | Relies on an MVCC snapshot and server-side export | Relies on on-disk page state, so it **must state the snapshot provenance**: `db_state` (shutdown state / checkpoint LSN / time) |
| Completeness check | None (only "no error") | `summary` + per-table `rows`/`pages` comparison + three-level `verdict` |
| Visibility of omissions | None (a missing table is not reported) | `skipped[]` gives a reason code per object, graded between structural skips (`shared_catalog`/`relkind`) and abnormal ones (`named_missing`) |
| Type fidelity | Server-side `*_out` functions | Offline decoding, with `unknown_types[]` stating which types are "byte-reversible but text form may differ" |
| Checksums | None built in | `--checksum crc32\|md5` per file, for later comparison / tamper detection |

### Import statements

| Dimension | pg_dump output | pg2sql output |
| --- | --- | --- |
| Form | `COPY ... FROM stdin` (default; text or binary) | `INSERT` (`--sql`, can merge with `--batch-rows`) / CSV (`--data`, optional `--header`) |
| Readability / editability | COPY data blocks are not meant for human review or trimming | One statement per line — grep/diff/trim friendly, suited to audit and sampling |
| Large fields | COPY text escaping | Escaping equivalent to COPY, `bytea` as `\x` hex |
| Transactions / performance | Single transaction + COPY, fastest | Row-by-row INSERT is slower; use `--batch-rows 1000`, `--import-script`, `--parallel` |
| Structural DDL | Same file as the data (separable with `-s`/`-a`) | DDL in its own `.ddl` (since v1.0.48), keeping data files clean; `--full-schema` emits functions/triggers/views separately |
| Sequence state | Emits `setval` | Also emits `setval` (`--sql` without `--fields`) |
| Encoding | Client-encoding negotiation | Export files are always UTF-8; `--import-script` injects `PGCLIENTENCODING=UTF8` |
| Replay order | Arranged by the tool | Partitioned tables are topologically sorted parent→child; the rest follow `import_all.sh` order |

## Role Export (`--roles`, v1.0.53)

Offline parse of `global/pg_authid` + `global/pg_auth_members` (Kingbase: `sys_authid`/`sys_auth_members`), emitting `roles.ddl`:

```sql
CREATE ROLE "app_rw" WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE INHERIT NOREPLICATION NOBYPASSRLS
  CONNECTION LIMIT 7 PASSWORD 'SCRAM-SHA-256$4096:…' VALID UNTIL '2027-06-30 00:00:00+00';
CREATE ROLE "app_ro" WITH NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS;

-- Role memberships
GRANT "app_ro" TO "app_rw" WITH ADMIN OPTION;
GRANT "pg_read_all_data" TO "app_rw";
```

Key points:

- **Only password hashes are exported** (SCRAM-SHA-256 / md5 verbatim); the tool never touches plaintext. Add `--roles-nopassword` to redact for compliance.
- **Built-in roles are skipped automatically**: OID < 16384 (created by initdb, including Kingbase `system`/`sso`/`sao`/`sys_*`) or a `pg_*` name in the list — the target instance already has them and recreating them aborts the script.
- **Memberships are preserved**: granting a built-in role (e.g. `GRANT pg_read_all_data TO app_rw`) is a real privilege delegation and is kept; if the grantee is a built-in role the row is skipped.
- `CONNECTION LIMIT` is emitted only for non-default values (≠ -1); `0` (no connections allowed) is emitted explicitly.
- The layout is probed dynamically from `pg_attribute` (works for PG12-18 and Kingbase V8/V9), falling back to a hard-coded layout if probing fails.

## Dual-Version Release (since v1.0.39)

Since v1.0.39 two binary families are released — **free** and **TDE (paid)** — gated at compile time by `-tags tde` from a single source tree:

| Dimension | Free `pg2sql` | TDE `pg2sql-tde` (version string carries `(TDE)`) |
|---|---|---|
| Build | `go build .` | `go build -tags tde .` |
| `--enckey` / `--wallet-password` / `--wallet-dir` | Not recognized (error points at the TDE build) | Fully supported |
| TDE code in the binary | **Stripped at compile time** (strings: 0 hits, cannot be extracted) | Contains SM4-ECB/SM3 implementations |
| `--version` / `--help` | `pg2sql 1.0.39` / no TDE options | `pg2sql 1.0.39 (TDE)` / 3 TDE options |
| Export features | Identical to the TDE build (minus decryption) | Identical to the free build (plus decryption) |

- Package naming: free `pg2sql-<version>-<platform>/`, TDE `pg2sql-<version>-tde-<platform>/` (both extract to `binary + docs/USER_GUIDE.md`).
- Source packages: the free source tree has no `tde/` directory (`go build` works directly); the TDE source tree includes `tde/` (`go build -tags tde`).
- Both share one regression baseline: free = regular full-version regression; TDE = regular regression + Kingbase TDE encryption suite (14 instances, 28/28 green, with plaintext closed-loop md5 checks).

Data-plane regression assets: `pgbuild/reg_pg_129_serial.sh` (66 PG checks), `pgbuild/reg_kb_130.sh` (25 Kingbase checks); parameter plane: `pgbuild/test_params_pg.sh` (20 assertions), `pgbuild/test_params_kb.sh` (26-27 assertions), `pgbuild/reg_params_all.sh` (full-version scheduler); big-table + tablespace suite: `pgbuild/reg_bigts.sh`.

## Changelog

- **v1.0.53**: self-verifying report + role export (regression 85/85):
  1) `--report` upgraded to a **self-verifying report**: new `db_state` (offline parse of `pg_control`/Kingbase `sys_control`: shutdown state / checkpoint LSN / time / system identifier), `summary` (tables exported/skipped/failed, rows vs estimate, bad blocks, TOAST, uncovered types), per-table `rows_estimate`/`rows_check` (against `pg_class.reltuples`) and `pages_expected`/`pages_actual`/`pages_check` (against `relpages`; `short` = missing segment file), `skipped[]` (reason codes), `unknown_types[]` (type-decode coverage), `verdict` (ok/warn/fail + reasons), plus a one-line conclusion on stderr;
  2) new `--roles` / `--roles-nopassword`: offline parse of `global/pg_authid` + `pg_auth_members`, emitting `roles.ddl` (all attributes + password hash + VALID UNTIL + `GRANT` memberships; initdb built-in roles with OID<16384 skipped automatically);
  3) `--all-tables` no longer fails wholesale because of shared catalogs (`pg_database`/`pg_authid`/`pg_tablespace` and 8 more `global/` objects) — they become `skipped[].reason=shared_catalog` and the exit code stays 0;
  4) tables that fail to export are also recorded in `exports[]` (`error` field), so the report total equals succeeded + failed;
  5) documentation adds "capability matrix (exact/approximate/unsupported)", "report.json field reference" and "report & import-statement differences vs pg_dump/ksql";
  6) parse-time guards: only one positional argument is allowed (`--report <path>` no longer silently replaces the data directory; the error points at `--report-file`), new `--report-file PATH`, and 0 tables / 0 skips no longer yields `ok` (`fail` + "no exportable objects discovered").
- **v1.0.52**: offline partition DDL restore + per-table encoding refinement:
  1) declarative partition restore (`pg_partitioned_table` + `relpartbound` node-tree deparse): parent `PARTITION BY RANGE/LIST/HASH`, children `PARTITION OF … FOR VALUES …` (including DEFAULT/multi-level/expression keys), emitted in parent→child→grandchild topological order, with a warning when the primary key excludes the partition key;
  2) per-table encoding refinement: a database-level probe contaminated by an old large table caused whole-database mojibake (a real Kingbase V8 GBK case) → each table is now judged from its own data file in three states, only utf8→other one-way override is allowed, and mixed-storage databases fall back to serial;
  3) six self-found issues fixed during regression (`pg_class` oid as a normal column causing 4-byte misalignment, single-node `PARTITIONRANGEDATUM` value, boundary deparse return-value semantics, timestamptz partition boundary drift, partition-parent relfilenode read offset, CLOB compressed page triggering a wrong encoding override).
- **v1.0.51**: `pg_node_tree` deparse kernel (`nodetree.go`) + column defaults / expression indexes / partial indexes, providing the basis for v1.0.52 partitions and v1.0.50 domain CHECK.
- **v1.0.50**: domain CHECK constraint offline restore (`pg_constraint.conbin` node tree); USER_GUIDE version and packaging script aligned.
- **v1.0.46–v1.0.49**: missing encoding probe in batch export, DDL as a separate file, `IF NOT EXISTS`, audit items P0/P1/P2 — see the `pg2sql_v1.0.4x_*.md` reports in the repository root.
- **v1.0.44**: P2 destructive-scenario round (packaged after full-version full-feature regression):
  1) **P2-1**: tablespace files are now included in encoding probing — TABLESPACE databases (data under `PGDATA/pg_tblspc`) previously went unsampled, so encoding was undetermined. After the fix a V8 32K tablespace database correctly falls back to UTF8 and a V9R1C10 tablespace database stays true GBK, both matching online;
  2) **P2-2** bad-page detail: report.json / stderr emit `bad_blocks` and `bad_pages` (the actual page numbers); rows inside bad pages are kept in the counts (measured: 157 extra rows retained);
  3) **P2-3** TOAST-file-missing counting: a missing TOAST file no longer silently yields NULL without a counter — an empty ToastFile is created so `toast_missing` works;
  4) **P2-4** DDL directory-missing warning: `--ddl` now warns explicitly when the sys/ directory files it needs are missing (previously silent);
  5) **four robustness bugs fixed**: pg_index empty segment path panic, `detectSysFile` panic on pageSize=0 with a zero-length buffer, `tryAutoLayout` short-buffer overrun, and silent NULL without counting when the TOAST file is missing;
  6) regression: `pgbuild/reg_v1044.sh` gains a [V1044] P2 destructive assertion block (encoding fallback / bad-page detail / TOAST counting / DDL warning); three-stage matrix (parameter plane 21 instances + big table 16 instances + mixed 22 combinations + partitions) plus the TDE suite all green.
- **v1.0.43**: GBK databases retested across every V8/V9 compatibility mode, with corrected encoding conclusions:
  1) **32K-page sampling miss fixed**: `probeDirUtf8Ratio` read only the first 16384 bytes per file — heap **row data lives at the end of the page** (tuples grow backwards), so 8K/16K pages were covered but **the first 16KB of a 32K page holds only the header and free space, no row data** → the UTF8 signature never matched → no fallback → mojibake (caught on a V8 ORA 32K instance). Fix: read the last page instead (`ReadAt(size-pageSize)`), which triggers on 8K/16K/32K alike;
  2) **mode conclusions corrected** (V8 ORA/PG/MySQL + V9R1C10 3 modes × 8/16/32K, 9 instances): **V8 labels databases GBK but physically stores UTF8** (even with `client_encoding=GBK`, on-disk hex `f0 a0 80 80`); **V9 stores true double-byte GBK** (on-disk `b1 e0` = 编, `c6 d5` = 普) — auto correctly does not fall back, and GBK decoding matches online; forcing `--encoding utf8` is what produces garbage. So "Kingbase GBK always stores UTF8" is false: **it differs by major version — V8 stores UTF8, V9 stores true GBK**;
  3) regression: `pgbuild/reg_v1043.sh` adds 2 V8 ORA 32K fallback assertions + 2 V9 true-GBK no-fallback/content assertions.
- **v1.0.42**: fixes after a full code/feature bug analysis (conclusion: no P1; core parsing/parallelism/streaming/comparison all correct):
  1) **P2-1 auto-fallback for "labelled GBK, stored UTF8"**: a Kingbase V8 database created with `ENCODING 'GBK'` stores `编码𠀀堃测试` as **UTF8 bytes** (hex `f0 a0 80 80`) while `pg_database.encoding=GBK` → the old build decoded as GBK and produced mojibake. Fix: a) correct the Kingbase GBK encoding number **35** mapping (previously misclassified as latin1); b) when auto detects a non-UTF8 declaration, **sample user-table data bytes for a UTF8 signature** (per-file decision: multi-byte validity ≥0.7 with ≥4 sequences) and switch to UTF8 with the notice `已自动切换 UTF8（可用 --encoding gbk 强制）`; genuine GBK/GB18030 double-byte streams stay below 0.5 and do not trigger (pure GBK ratio = 0.0 measured); system catalog files (relfilenode<16384) are skipped to avoid directory noise;
  2) **P3-1**: `--fields` with an unmatched column → stderr warning (was silently ignored, so a typo dropped a column without notice);
  3) **P3-2**: `--deleted` together with `--only-deleted` → error (mutually exclusive; previously silently took only-deleted);
  4) **P3-3**: `--batch-rows` in non-SQL mode (`--data`/pure `--ddl`) → stderr notice that it is ignored;
  5) **P3-4**: `--limit 0` semantics documented (0 = unlimited, the default) in help/README/manual;
  6) regression: `pgbuild/reg_v1042.sh` adds P2-1 plus P3-1..P3-4 assertions.
- **v1.0.41**: two `--progress` fixes:
  1) **BUG-B (parallel page progress stuck at 0)**: `streamStdParallel` (the standard-layout parallel path used by `--parallel` for ordinary tables) lacked the `progressDone` hook, so `--progress --parallel N` showed 0 pages and a frozen percentage. Fixed with the same atomic hook as the serial/auto-layout paths; measured on a 2M-row table: 41.3%→80.7%→100.0%, pages 9385→18332→22728;
  2) **progress line shows the current table name**: `[pg2sql] 进度 41.3% 表 public.huge_a 页 9385/22728 行 825774 耗时 1.0s ETA 1.4s` — with `--jobs`, each table reports its own progress and name;
  3) regression-script fix: PG13/14/15/17 use the `pgdata_ft_` prefix while the script only looked for `pgdata_prm_`, so those versions were effectively skipped (the 4 FAILs in the v1.0.40 log came from this). v1.0.41 falls back between prm/ft, giving true full coverage; `--progress` assertions strengthened (table name + `--parallel` page progress > 0);
  4) regression: full version/feature matrix green before packaging.
- **v1.0.40**: 3 new features + 1 metadata column-misalignment bug:
  1) `--full-schema` complete schema DDL (with `--ddl`, emitting `schema_objects.ddl`): functions (`CREATE OR REPLACE FUNCTION`, column-by-column from pg_proc), triggers (`CREATE TRIGGER`, from pg_trigger), FK/CHECK constraints (pg_constraint, FK column list + raw CHECK expression), and views/CHECK as "placeholder comment + dependency list" (no offline pg_node_tree decompilation, matching pg_dump's decision); `--schema-objects LIST` filters categories (view,function,trigger,constraint,fk; all by default);
  2) `--large-objects` exports pg_largeobject large objects (`lo_from_bytea` single statement assembled from 2KB chunks, `--large-object-oids` selects OIDs, integrates with `--import-script`). Kingbase adaptation: `pg_class.relfilenode` holds the OID rather than the relfilenode → probe the standard physical file 2613 first and fall back to the relfilenode path; Kingbase `pg_attribute` lists system columns with positive attnums → direct 3-column decoding + TOAST 4BC reassembly;
  3) `--progress` export progress and ETA (stderr: pages/rows/elapsed/ETA; `\r` in-place on TTY, line-by-line on pipes; the double-counting pre-scan was removed so progress reaches exactly 100%);
  4) **BUG-A fixed**: with hasOID=true, `parseNamedTuple` read the pg_attribute oid metadata row (attnum=1) as a user column, shifting every following column by 4 bytes → on PG instances all user functions in pg_proc became unreadable (`boolin` read as "in"). Fixed by skipping the attnum=1 oid metadata row only for standard PG (!isKB), keeping Kingbase behaviour (its hasoids semantics differ);
  5) **known limitation (Kingbase)**: user functions (pg_proc) exist in the data pages but the page header item directory is not updated (Kingbase's private write path leaves lower=0), so `iterSysTuples` cannot enumerate them → the Kingbase full-schema function section is empty. Triggers/constraints (incl. FK)/view dependencies/large objects all work;
  6) regression: full matrix (PG 12/13/14/15/16/17/18 × block sizes + Kingbase V8 8K/16K/32K + V9 8K, covering SQL/CSV/full-schema/large-objects/progress + SPECIAL batch/checksum/report/jobs/import + TDE regular export, PASS=54 FAIL=0).
- **v1.0.39**: Kingbase TDE transparent-encryption offline decryption (standalone `tde/` module, laying the groundwork for paid TDE) + **dual-version release** (free / TDE, gated by `-tags tde`):
  1) in-house SM4-ECB/SM3 implementations (fixed missing τ in key expansion and output byte order; three test vectors PASS), no external libraries;
  2) `--enckey KEY`: table-encryption offline decryption (`CREATE TABLE ... ENCRYPTED BY 'KEY'`, page offset 64+ SM4-ECB, same key for TOAST, both serial and parallel paths);
  3) `--wallet-password PW` + `--wallet-dir DIR` (auto-detects `<PGDATA>/.wallet`): tablespace-encryption offline decryption (wallet chain KEK→MK→TSPK derived offline, no password needed; tspkey.kr entries matched per tablespace);
  4) full-version encryption regression: Kingbase V8R6 (ORA 8K + PG 8K/16K/32K) and V9R1C10 (PG/ORA/MySQL × 8K/16K/32K, 9 instances) + older V9 — 14 instances, 28/28 green (100 encrypted rows with TOAST long text + 50 plaintext tablespace rows);
  5) boundaries: TDE requires `sysencrypt` in `shared_preload_libraries`; the V8 wallet password SM3 check offset differs from V9 (only affects the `--wallet-password` warning, not decryption).
- **v1.0.38**: 4 new features (all off by default) + 3 tablespace-resolution bugs:
  1) `--batch-rows N` multi-row INSERT (default 1 = off): consecutive rows merge into `INSERT ... VALUES (...),(...)` (deleted rows are emitted separately as `-- DELETED`, ordered with `--parallel`), speeding up restore/import;
  2) `--jobs N` table-level parallelism (default 1 = off): a goroutine pool across tables for `--table-name` lists / `--tables` / `--schema` / `--all-tables`, one file per table, failures recorded independently without aborting, output byte-identical to serial (self-tested with 3 and 4 tables, identical md5);
  3) `--report` + `--checksum crc32|md5` (default off): `report.json` (table/format/rows/bad blocks/files/bytes/checksum/elapsed/page size/encoding, concurrency-safe under jobs) as an integrity receipt for rescue exports; checksums are computed while writing (JSON to stderr when there is no `-o`);
  4) `--import-script none|auto|sql|csv` (default none): per-table `import_<schema>_<table>.sh` (sql→`psql/ksql -f`, csv→`\copy` with the `--fields` column list, overridable via `PSQL`/`DB`/`PGCLIENTENCODING`) plus a batch `import_all.sh`;
  5) **fixes**: `resolveDataRoot` mistook `base/<dboid>/PG_VERSION` (PG17+) or Kingbase `base/<dboid>/SYS_VERSION` for the data root, breaking tablespace tables on PG17/18 and Kingbase — it now recognises database-directory markers (`pg_filenode.map`/`sys_filenode.map`/catalog `1247`) and anchors the data root on `global/`; `Options.DBOID` was hard-coded to 5, breaking tablespace location and encoding probing whenever the OID differed — dboid is now derived from the dbDir name first. Verified with PG16 t_ts/t_ts2 and Kingbase V9 t_kts/t_kts2 tablespace tables, plus restored encoding probing;
  6) documentation: README/USER_GUIDE/`--help` gain the 5 options and a **jobs×parallel memory/CPU sizing** section; full-version regression (new batch/jobs/report/imp_script assertions + tablespace coverage on PG17/18 and Kingbase) green before packaging.
- **v1.0.37**: Kingbase V9R1C10 x86 (9 instances: ORA/PG/MySQL × 8/16/32KB) added to the full regression matrix (parameter plane + big-table suite + mixed scenarios + partition suite); fixed R1C10 COMMENT mojibake caused by the new `name` column in `sys_description` (comment parsing now probes the real `sys_attribute` layout dynamically).
- **v1.0.36**: `--table-name` on a partition parent expands all leaf children:
  1) **feature**: pointing single-table mode at a declarative partition parent (relkind='p') recursively expands every leaf child (relkind='r', multi-level supported) — one leaf keeps single-table semantics (`-o` may be a file), multiple leaves switch to batch export (`-o` must be a directory plus `--sql`/`--data`); `--table-name parent,plain_table` mixes both; an empty parent (no children) keeps the old behaviour (0 rows + verbose notice);
  2) **implementation**: `catalog.go` parses `pg_inherits` (Kingbase `sys_inherits`) via `detectSysFile`, using the first two fields (`inhrelid`/`inhparent`, so PG14+ trailing columns do not shift offsets), builds an `inhparent → direct child OID` map and adds `OID`/`ChildOIDs` to `TableMeta`; `main.go` adds `expandParentToLeaves` (with a seen-set against cycles), wired into both the single-table path and `runNamedTables`;
  3) **boundaries**: only declarative partitions (relkind='p') expand; legacy INHERITS parents (relkind='r') stay per-table; expanded children get their own DDL (same as `--tables` batch; the parent `PARTITION BY` DDL is not emitted);
  4) regression: data plane + parameter plane (PG 9 combinations × 20+ assertions + Kingbase 3 instances × 26-27 assertions) + a partition-expansion suite (multi-leaf parent batch, single-leaf single-table, parent+plain mix, multi-level, empty parent).
- **v1.0.35**: pre-release audit fixes (A1/A2 parallel leaks → B1-B3 corrupt-data panics → C1 array "NULL" distortion → D4 silent write errors):
  1) **A1/A2 (concurrency)**: `streamStdParallel`/`streamScanParallel` use polling `sendPageResult`/`sendJob` (select default + stop polling + Gosched), fixing a goroutine leak where an early return after `--limit` was reached left workers/the jobs producer blocked forever (accumulating across batch tables with `--limit`), and closing the producer's "check stop before send" race window; `go vet` clean;
  2) **B1-B3 (corrupt-data panics)**: `decodeArray` bitmap overrun (dataoffset/bitmap length checks, falling back to raw bytes `E'\x...'`), `decodeInet` address overrun (length check, empty string on failure), `decodeRange` fixed-length bounds crossing the flags byte (aligned with the varlena branch) — truncated/corrupt fields are skipped instead of crashing;
  3) **C1 (array "NULL" distortion)**: a new `arrayNullSentinel` (`\x00PGSQL_ARRAY_NULL`, safe since PG text never contains `\x00`) distinguishes a real NULL from a string element; `arrayQuote` quotes the string `"NULL"` normally, so `{NULL,"NULL"}` imports without confusion;
  4) **D4 (silent write errors)**: `exportOneTable` routes all writes through a `write()` closure that collects errors — any failure in DDL/count/SQL/DATA/sequence sync/final Flush returns `写入输出文件失败` instead of reporting success on a full disk with a truncated file;
  5) regression: data plane + parameter plane (PG 9 combinations × 20 assertions + Kingbase 3 instances × 26-27 assertions) + big-table suite (1M-row tablespace table) + mixed scenarios.
- **v1.0.34**: directory-mode tablespace auto-discovery (features 3/2/1):
  1) **feature 3**: with `--datadir DIR --db-oid OID`, `--schema`/`--table-name`/`--tables`/`--all-tables` auto-discover tablespace tables — `classFields` reads `pg_class.reltablespace` (column 9 in PG12/16/18 layouts; Kingbase shares PG12's), `TableMeta` gains `TableSpace`/`ToastTableSpace`; location resolution: `reltablespace==0` keeps `base/<oid>/<rf>` (default-tablespace behaviour unchanged), non-zero resolves `pg_tblspc|<sys_tblspc>/<ts_oid>/<version dir>/<dboid>/<rf>` (the version directory is detected by the presence of a `<dboid>` child, tolerating multiple version directories); TOAST resolves through the toast table's own tablespace; dangling symlinks or missing directories produce explicit errors;
  2) **feature 2**: `--list-tables-db`/`--list-tables-all` gain a "tablespace" column (`0`=base, non-0=`pg_tblspc/<oid>`);
  3) **feature 1**: `--datadir DIR --list-db` shows a multi-line filepath column (one line for base + one per tablespace; `pg_tblspc`/`sys_tblspc` auto-detected; `os.Stat` follows symlinks);
  4) entry validation relaxed: `--datadir+--db-oid` needs no positional argument for single-table (`--table-name`) or batch (`--schema`/`--tables`/`--all-tables`) export;
  5) regression strengthened: `test_params_pg.sh` grows to **20 assertions** (new `tspace_big`/`tsdir_batch`/`listdb_multi`/`tspace_col`: a second tablespace + a 50K-row Chinese/special-char tablespace big table with SQL+CSV import, directory-mode `--schema` batch including tablespace tables, multi-line `--list-db` filepath, tablespace column in `--list-tables-db`), `test_params_kb.sh` to **26-27**; new `pgbuild/reg_bigts.sh` big-table + tablespace suite (1M rows, SQL/CSV closed loop + MaxRSS sampling);
  6) tablespace test assets moved from `/tmp` to `pgbuild/tblspc_data{2}` (PG) and `pgbuild/kb_tblspc_data{2}` (Kingbase) so `/tmp` cleanup cannot destroy them.
- **v1.0.33**: full-feature regression completion + README feature coverage matrix:
  1) five CLI features that previously had no assertions (`--deleted`/`--only-deleted`, `--list-db`, `--list-tables-all`, `--all-tables`, `--encoding`) — `test_params_pg.sh` to **16 assertions**, `test_params_kb.sh` to **21-22**;
  2) full parameter-plane matrix: **PG 12.22/16.15/18.6 × 8K/16K/32K = 9 combinations × 16 assertions = 144 items green**; **Kingbase V8 ORA 8K / V8 PG mode / V9 = 3 instances × 21-22 assertions green** (the V8 GB18030 database is marked `SKIP(env)` because ksql cannot safely write UTF8 SQL; V9 covers the full GB18030 assertions, GBK verified on both);
  3) test-script fixes: tablespace directory cleanup (CREATE TABLESPACE needs an empty directory), pre-created Kingbase tablespace version directories, parameterised labels, and `--encoding` redesigned around the tool's semantics (input decode encoding): explicit utf8 equals auto on a UTF8 database, LATIN1 (LC_COLLATE 'C') closed loop, Kingbase GBK/GB18030 closed loop;
  4) see "Feature Coverage Matrix" above. Zero logic change (docs and test scripts only).
- **v1.0.32**: tablespace path auto-discovery (no `--datadir`/`--db-oid` needed):
  1) when a single-table file path contains a `pg_tblspc/` (Kingbase `sys_tblspc/`) symlink prefix, the data root and database OID are derived from the path, so the catalog can be read directly — just prepend the data root to what `pg_relation_filepath` returns;
  2) the fallback is unchanged: dangling symlink, deleted target or only the real path still use the absolute path plus `--datadir` + `--db-oid`;
  3) the tablespace section of the test script became "auto-discovery + fallback" double assertions: PG16.15 **11/11**, Kingbase V9 **19/19** green; 100-row SQL/CSV export→import closed loop for PG and Kingbase tablespace tables;
  4) README/USER_GUIDE/`--help` document "tablespace auto-discovery and fallback".
- **v1.0.31**: parameter-plane regression completion + doc/packaging conventions:
  1) new automated parameter-plane scripts `pgbuild/test_params_pg.sh` (10 checks: `--ddl` replay closure/`--count`/`--fields` CSV import/`--limit`/`--parallel` vs serial md5/`--list-tables-db`/`--schema --tables --ddl` batch/`--export-meta`+`--catalog-json`/bad blocks/tablespaces) and `pgbuild/test_params_kb.sh` (Kingbase: GB18030/GBK SQL+CSV closed loops plus the same parameter plane) — PG16.15 **11/11**, Kingbase V9 **19/19** green;
  2) README adds the **v1.0.29 vs v1.0.30 memory/time comparison** (memory from O(rows) to O(window × parallelism); on the 2-core baseline par4 SQL -17%, par4 CSV -26%);
  3) tablespace export note: when a table file is not under `base/`, single-file mode needs `--datadir` + `--db-oid` to reach the catalog;
  4) the binary package layout convention becomes **`pg2sql-<version>-<platform>/` (binary + `docs/`)**.
- **v1.0.30**: approach B landed — large-table export switched from "collect everything then forward" to **O(window) streaming** (eliminating large-table OOM):
  1) **parse→escape→emit completed inside each worker** (with `--parallel N` escaping runs alongside parsing; previously escaping was a single formatting goroutine and accounted for ~90% of the elapsed time on big tables);
  2) **page-order sliding window with ordered forwarding** (pending map + next pointer, window size O(parallelism), never collecting all rows) — memory drops from O(rows) (≈3.5-4GB for 50 columns × 1M rows, guaranteed OOM on a 3GB machine, once OOM-killed every Kingbase instance) to O(window × parallelism);
  3) the serial path streams as well (lightweight pre-scan then a second streaming pass; `probeStandard`/`probeRowValid` only judge NULL/empty/TOAST-missing without building or formatting rows; the pre-scan costs ~15-25% of a full parse and the second pass hits the page cache); the two-stage fallback (standard ItemId / data-area scan) is unchanged semantically;
  4) `--count` became streaming (CountRows, no accumulation);
  5) **multi-segment files (>1GB tables)**: with 8KB pages RELSEG_SIZE=131072 pages=1GB, so tables over 1GB split into `relfilenode`, `relfilenode.1`, … (16KB=2GB, 32KB=4GB); since v1.0.30 the main table, TOAST, index pre-build and pre-scan all read across segments — **every earlier version exported only the first segment of a >1GB table (a 1.9GB table yielded only 800K rows)**;
  6) **empty-page fix in the parallel window**: workers also send an empty result for pages with no rows, a bad layout or a read failure, so the window's next pointer advances page by page (previously such pages sent nothing, next stalled and all following rows were lost).
  Acceptance: PG16 50 columns × 100K rows (Chinese/special chars/NULL/empty strings) + an 11-row special-char table; `--sql`/`--data`/`--count`/`--limit 1000`/`--fields` serial and `--parallel 4`, 9 combinations md5-identical to v1.0.29; SQL/CSV import closed loop with count/min/max all correct; **1.5M-row suite (1.9GB heap, two segments)**: serial SQL 33.3s / par4 SQL 27.8s / par4 CSV 18.4s / count 11.8s, all four channels full 1500001 rows, serial/parallel md5 identical, SQL import 1500000 rows (202s) and CSV import 1500000 rows (60s); MaxRSS flat at 17MB (the old design needed ≈5-6GB and OOMed); 5/10/20/30/50GB estimates are in the README section above.
- **v1.0.29**: added `docs/USER_GUIDE.md` (per-option reference / combination matrix / scenario topics / FAQ); the source package includes `docs/`, binary packages embed `pg2sql-docs/` alongside the binary; README links the manual at the top. Zero logic change (version and docs only).
- **v1.0.28**: `--table-name` help and README examples completed as a full contrast (unquoted = comma list / quoted = one complete name): `--table-name a,b` (2 tables), `--table-name '"a,b"'` (1 table with a comma), `--table-name '"a,b",c'` (mixed), `--table-name '"a""b"'` (embedded double quote). `--help` synced; zero logic change.
- **v1.0.27**: `--table-name` accepts double-quoted names (removing ambiguity for identifiers containing commas/quotes, matching SQL conventions): commas inside quotes are literal, `""` is a literal double quote (same rule as PG quote_ident), an unterminated quote is an exact error, empty names/quotes are filtered. The shell needs single quotes around the double quotes (`--table-name '"a,b",c'`). The single-table path uses the parsed name; without `--table-name` the old fallback chain applies. Verified: comma-containing names, quoted names, unterminated quotes, and all existing scenarios.
- **v1.0.26**: `--table-name` accepts comma-separated tables (e.g. `--table-name ray.t_special_test,ray.test02`) — each table is located and exported to its own file (batch naming `schema.table.sql|.csv`); multi-table export requires `-o` directory + `--sql` or `--data`, and unmatched names are exact errors (no silent table loss); whitespace around commas is trimmed. Single-table behaviour is unchanged.
- **v1.0.25**: deep code-audit round (5 items):
  1) **P-1 [critical, PG data loss] maxRowNatts read t_infomask2 high flag bits as the column count**: the PG branch `(i2>>11)&0x7FF` treated HEAP_KEYS_UPDATED/HOT_UPDATED/ONLY_TUPLE/IS_PARTITION/CANT_RECORD (0x0800~0x8000) as the in-row column count (reading 1/2/4/8/16), truncating real tables — measured on PG18 with 21 columns and a same-width UPDATE (HOT row infomask2=0x8006) being cut to 16 columns, silently losing c16~c20. Reverted to the low 11 bits (`HeapTupleHeaderGetNatts = i2 & 0x07FF`, same as Kingbase; fixed in v1.0.4 and regressed during a refactor). New `pgbuild/test_hot.sh` HOT suite: PG12-18 × 8/16/32KB, 11 combinations, 21 columns + same-width UPDATE + VACUUM, verifying all 21 columns and SQL/CSV md5 closure; Kingbase V9 MySQL HOT 21 columns intact, 0 import errors;
  2) **P-2 [functional] TOAST index pre-build truly parallel**: `BuildIndexParallel` previously parsed chunks in a single producer goroutine (workers only appended to a map), so pre-build gained almost nothing; rebuilt so each worker does its own ReadAt and in-page parse (removing shared read-buffer overwrite risk), merged and sorted by valueid+seq. Measured (Kingbase V9 MySQL, 20K rows / 2 columns / 7KB ≈ 300MB TOAST): serial 3.72s → parallel=4 1.54s (2.42x), exported SQL md5 identical;
  3) **P-3 [low] PageCache real LRU eviction**: CacheOrder tracks access order, hits move to the tail, overflow evicts the least recently used from the head (previously a random map iteration while the comment claimed "oldest");
  4) **P-4 [low, theoretical] timestampFromUS overflow**: `time.Duration(sec)*time.Second` capped out around 292 years (timestamps after 2262 overflowed); replaced with Howard Hinnant's civil-from-days algorithm (int64 throughout, no overflow) covering the full PG range (4713BC~294276AD), byte-identical to the old output in the normal range;
  5) **P-5 [low, quality]**: `buildDdlStatements` indentation cleanup. Also `logf` now uses a mutex so concurrent logs do not interleave.
  Verified: PG12-18 × 8/16/32KB for all-types/GAP/HOT/TRUNCATE, 44 groups green; Kingbase V8 ORA×3 (8/16/32K) + V9 MySQL + V8 PG mode, 5 instances, 15 items OK.
- **v1.0.24**: concurrent main-table row parsing + TOAST reassembly (`--parallel N` speeds up both TOAST index pre-build and page-sharded row parsing): a worker pool parses per page and a sliding window merges in order, producing output byte-identical to serial (md5 identical); fixed a data race injecting roleMap during concurrent parsing (role maps are now injected once before parsing and read-only afterwards); PageCache gained an RWMutex and TOAST file handles use sync.Once (race detector clean in all modes). Measured (2 cores, 20K rows / 333MB TOAST): parallel=2/4 cut total time by 7-8%; 4-8 cores should give 1.5-2.5x by Amdahl.
- **v1.0.23**: README gains the "Bad Block (Corrupt Page) Handling" section (docs only, no logic change): the four-level defence, aborted/deleted row handling, NULL/empty protection for bad fields and TOAST pointers, and batch success/skip/fail statistics.
- **v1.0.22**: Kingbase MySQL compatibility-mode types completed (all three modes share one layout; V8 PG mode verified):
  1) **TINYINT** (oid 8100, typinput=tinyintin, 1-byte signed): previously raw bytes, now `tinyintin→decodeInt1` (`127`/`-128`), with unsigned variants matching via domain recursion;
  2) **BINARY** (oid 3383, typinput=binaryin): previously raw bytes including the varlena header and `\x00` padding, causing `invalid byte sequence` on import; now `binaryin→decodeBinary` (strip the header, drop trailing `\x00` padding, fall back to `\x` hex when an interior `\x00` exists);
  3) **SET** (dynamic oid, typtype=y): stored as a 16-byte member bitmask ([10:14] int32) with labels absent from the catalog, exported as `\x` hex (byte-auditable); measured that Kingbase set_in accepts `'x,y'` text but cannot restore the `\x` bytes (mask cleared) — documented as a limitation;
  4) **decodeDefault hardened**: `utf8.Valid` accepts `\x00`, and unknown types containing `\x00` used to break imports; they now always emit `\x` hex (byte-reversible).
  Verified: Kingbase V9R3C18 MySQL mode `t_my_types` (17 columns, 3 rows, TINYINT/MEDIUMINT/DATETIME/YEAR/BLOB/TEXT/ENUM/SET/FLOAT/DOUBLE/DECIMAL/BINARY) — 16 of 17 columns round-trip with EXCEPT 0 difference (SET excepted); V8R6C9B14 ORA mode `t_ora_types` (13 columns) SQL/CSV md5 identical; V8 PG mode all-types partition/P1/TRUNCATE green; PG12-18 × 8/16/32KB green.
- **v1.0.21**: fixed a TOAST external-read path defect (found by bad-block analysis, P1):
  1) **TOAST path used the OID instead of the relfilenode**: `reltoastrelid` stores the toast table **OID** while the file is named after the **relfilenode**. After TRUNCATE/rebuild they diverge (measured: reltoastrelid=24619, toast file=27622), so the old code pointed at a stale/empty file → `TOAST 索引预建 0 个 valueid` → **every external (large) field silently exported as NULL** with no error. Discovery now builds an `OID→relfilenode` map (`ToastRelFile` field) and prefers relfilenode; old `--catalog-json` files without the field fall back to the OID (backwards compatible). Verified: a TRUNCATEd 3000-row large-field table went from 3000 NULLs to 0 NULLs;
  2) **better discovery-failure hints**: when the target file exists (numeric relfilenode) but no table resolves, the error now adds "its table attributes may be missing because pg_attribute could not be read (corrupt file / not flushed)" instead of only the generic CHECKPOINT hint.
  Also measured bad-block behaviour: a corrupt main-table page header skips the page, a corrupt itemid skips one row, all-zero holes skip the page, truncation cuts the tail; corrupt/truncated TOAST pages degrade the affected field to NULL while keeping the row; the program never crashes. A corrupt pg_attribute exits with an explicit error and code 1.
- **v1.0.20**: three P1 decompression/boundary defects fixed and regression-verified (Kingbase V8/V9 + PG12-18 × 8/16/32KB all types, 50-column special chars, gap types, 40+ P1 matrix groups):
  1) **4BC in-row compression mask** (varlenaParse): little-endian disk format is `VARATT_IS_4B_C = (header & 0x03) == 0x02`, `SET_VARSIZE_4B_C = (len<<2)|0x02`; the old `(first & 0x06) == 0x06` only matches when the compressed length is odd, so even lengths were treated as uncompressed 4B → compressed streams (with NUL control bytes) were emitted as text → import errors. Fixed to the official `(first&0x03)==0x02`;
  2) **varPayload bounds** (types.go): the 4BC branch `comp := b[4:total]` gained a `total>len(b)` check plus a `len>=5` test for the compressed stream; the method bits are dispatched by toastDecompress from the top 2 bits of tcinfo (PGLZ=0/LZ4=1);
  3) **pglzDecompress byte shift overflow** (deepest root cause): `off := int((b1&0xF0)<<4)|int(b2)` shifted a uint8, truncating the top 4 bits of off (max 255), so matches at off≥256 copied from the wrong position and produced inconsistent md5 (reproduced on Kingbase V8 c_extcomp; PG escaped only by luck). Fixed to `int(b1&0xF0)<<4 | int(b2)`.
  Also fixed the P1 regression script to look up the postgres database OID dynamically (PG12-14 start at 12975, not 5).
- **v1.0.19**: fixed txid_snapshot exporting a missing trailing colon for empty xip snapshots (PG's `txid_snapshot_out` always emits `xmin:xmax:`; with nxip=0 the old code wrote `1:100`, breaking imports with `invalid input syntax for type pg_snapshot`). Added a full-type test script `pgbuild/t_alltypes_100.sql` (56 columns covering numeric/character/binary/boolean/bit/date-time/JSON/XML/UUID/array/network/geometric/full-text/range/system/enum/composite + PK + 9 indexes + 100 rows of Chinese/English special characters).
- **v1.0.18**: filled the remaining PG/Kingbase built-in type gaps — the full range family (int4range/int8range/numrange/daterange/tsrange/tstzrange, including empty and half-open `(,10)`/`[5,)`, parsed per the rangetypes disk format: strip the 4-byte range oid header, fixed-length bounds by attalign, varlena bounds as whole varlenas, plus the 1-byte flags), `pg_lsn` (8-byte little-endian, uppercase hex X/Y), `txid_snapshot` (`[nxip][xmin][xmax][xip]`, text `100:200:110,140`), the `reg*` family (numeric oid output, importable), `tsquery` (12-byte QueryItems with packed positions and NUL-terminated operands, NOT/AND/OR/PHRASE precedence identical to PG infix; verified verbatim on `'fat' & ( 'rat' | 'cat' )`), `macaddr8`, `path`/`circle` geometry, N-D arrays `{{1,2},{3,4}}` and empty arrays `{}`, enum values (members looked up via typrelid). Verified on PG18 `t_gap` (18 columns × 3 rows: ranges/lsn/txid/tsquery/regclass/path/circle/macaddr8/enum/2-D array/empty array) — every column matched PG; 17 of 18 columns round-tripped (count=3 after TRUNCATE + `\i`, spot-checked values equal). **Composite types (user-defined record, oid 16505 etc.) remain limited**: their layout depends on heap_fill_tuple short-varlena packing and alignment and varies across versions, so raw bytes `E'\x...'` are emitted (byte-reversible, no corruption; replay manually or use online pg_dump) — still being worked on.
- **v1.0.17**: fixed Kingbase V9 MySQL-mode BIT (oid 4655, typinput=mysql_bit_in) exporting raw varlena bytes and failing import — reverse-engineered the mysql_bit disk format (varlena content = 4-byte LE A + 4-byte LE B + ceil(N/8) big-endian data bytes, A=(N-1)-bitpos, B=bitpos+1, N=A+B the storage width, data = value<<(8*nbytes-B); all-zero gives A=B=0) and added decodeMysqlBit (bit string) and bitBinToHex (0x uppercase hex). Also confirmed import requirements: SQL must use `B'...'` bit literals (plain strings hit varchar_bit cast and fail with "bit string length exceeds 64"), and CSV COPY must use 0x-prefixed hex (plain 0/1 text misparses; measured 0x8001→0xC4C4). Fixed xml exporting a leftover varlena header (Kingbase MySQL xml is plain varlena text, so header bytes such as 0x2b='+' leaked into the output; PG xml carries a 4-byte type marker) via decodeXml. Verified: Kingbase V9R3C18 `t_fulltype_part` (36 columns incl. BIT(16)/VARBIT/XML/JSONB/geometry/Chinese/special chars, 3 partitions, 10 rows) — SQL and CSV round-trip with identical md5; BIT(8)/BIT(10)/BIT(16) boundary values (0x00/0x01/0x80/0xFF/0x02AA) consistent; Kingbase V8 bit(1560)/xml table and a 31-column all-type table md5 identical; PG12-18 × 8/16/32KB 10/10 green.
- **v1.0.16**: added "dynamic type discovery from the catalog" (approach C) — reads `pg_type/sys_type` (1247) and `pg_proc/sys_proc` (1255) and maps decoder families by `typinput` function name (`mysql_timestamp_in→UTC seconds`, `mysql_datetime_in→local microseconds`, `ora_date_in→timestamp format`, `jsonb_in→JSONB`, `textin→text`, …); `typtype='d'` domains recurse through `typbasetype`, array types self-register via `typelem+typarray`; the hard-coded decoder table still takes precedence and a missing catalog does not disable anything. This solves "instance-specific oids / future types cannot be pre-mapped" (e.g. 4802 mysql_json on V9R3C18 now resolves dynamically to decodeJSONB, while 12636 has been reused as a toast row type on V9R3 and no longer relies on a hard-coded assumption). pg_type layout is version-aware: PG18+ inserts typsubscript (4B) after typrelid, while PG12-17 and Kingbase V8/V9 share one layout. Verified: 42 dynamic mappings on a real V9R3C18 instance (incl. 4802, SQL+CSV EXCEPT 0), 28 on V8 plus a 31-column all-type table EXCEPT 0, PG12-18 10/10 green.
- **v1.0.15**: fixed 3 type-decode bugs in Kingbase V9 MySQL mode (found on a real V9R3C18 30-column all-type table): ① `7954 timestamp` (mysql_timestamp_in stores second-precision int64 microseconds from 2000-01-01 UTC; output converted back through session time zone UTC+8) had no mapping and exported raw bytes — added decodeMysqlTimestamp; ② `7950 time` (MySQL TIME semantics allowing 24:00:00) reused decodeTime, which wrapped 86400 seconds to 00:00:00 (id=3's 23:59:59.999999 rounded to 24:00:00 was normalised away) — added decodeMysqlTime without the modulo; ③ `7024 json` (domain of 4802 mysql_json, binary jsonb_in/jsonb_out storage) had no mapping and exported raw varlena bytes — mapped to decodeJSONB. Verified: 30-column all-type table on a real V9R3C18 x86 instance (7944 date/7952 datetime/7954 timestamp/7950 time/8020 ora_date/4189 datetime/clob/money/json/jsonb/arrays/Chinese/special chars/NULL/24:00:00 boundary) — SQL and CSV EXCEPT 0 both ways; V8 31-column table EXCEPT 0; PG12-18 10/10 green.
- **v1.0.14**: fixed Kingbase CLOB (oid 8014) exporting a leftover varlena header (`#clob中文内容`/`\x17clob末尾`) — Kingbase clob is standard textin/textout varlena storage (measured header bytes 0x05/0x07/0x0d/0x0f matching content lengths 1/2/5/6 as total=header>>1), so 8014→decodeText was added; fixed money (oid 790) being single-quoted in SQL output (`'12.34'`) by adding 790 to the numeric noQuoteOIDs set. Verified: V8 online 31-column all-type table (int2/int4/int8/oid/numeric/money/float4/float8/varchar/char/text/name/clob/bool/bytea/json/jsonb/uuid/date/datetime/ora_date/time/timetz/timestamp/timestamptz/interval/int[]/text[]/varchar[], with Chinese/special chars/NULL/boundary times/leap years) SQL and CSV EXCEPT 0 both ways; PG12-18 × 8/16/32KB green.
- **v1.0.13**: fixed Kingbase V9R1C10 partition tables exporting raw bytes for DATE columns — V9 introduced `7944 date` (4-byte days, same as PG) with no mapping, so the Kingbase time family was completed: `7944 date`, `7952 datetime`, `4189 datetime` (domain of timestamp), `12636 ora_date` (domain of 8020). Also fixed two bugs exposed by the PG all-type export: `timestampFromUS` formatting pre-2000 timestamps (negative microseconds) as `00:00:00.-01` (negative microseconds are now normalised), and `decodeTimetz` printing `-00:00` for a zero offset. Added a `tsvector` (oid 3614) decoder following PG ts_type.h (packed WordEntry haspos/len/pos, contiguous word strings, 2-byte alignment padding and WordEntryPos arrays, weights 3→A/2→B/1→C/0 omitted), byte-identical to tsvectorout including weight letters. Verified: PG16 all-type table (28 columns incl. tsvector/time/arrays/json/Chinese special chars/pre-2000 times) SQL/CSV EXCEPT 0 both ways; V8 online datetime/date fixed; PG12-18 green.
- **v1.0.12**: `--schema NAME` can be used on its own (equivalent to `--tables --schema NAME`), batch-exporting all user tables in that schema; batch validation messages unified across `--schema/--tables/--all-tables`.
- **v1.0.11**: added batch export `--tables` (user tables) / `--all-tables` (all tables incl. system objects) and `--schema NAME` filtering; multi-table export requires `-o` output directory and `--sql` or `--data`; without `--ddl` only data is exported, with `--ddl` the DDL is emitted too (one file per table, `schema.table.sql|.csv|.ddl`).
- **v1.0.10**: added `--list-tables-all` (all objects including system); `--list-tables-db` now lists user objects by default (via isSystemSchema: pg_catalog/pg_toast/information_schema plus Kingbase sys_catalog/sysaudit/sysmac/kdb_schedule/anon/src_restrict/sys/SYS_HM*/sys_*).
- **v1.0.9**: removed redundant options (`--list-tables`, `--replace`); fixed a data race in the parallel TOAST index build (per-worker maps merged by one goroutine, race detector clean); complete DDL support — CREATE SEQUENCE, ALTER COLUMN SET DEFAULT nextval, indexes/PK (USING btree), COMMENT ON TABLE/COLUMN, and a trailing `SELECT setval` to sync sequences when DDL and data are imported separately (preventing auto-increment lag and PK conflicts); pure-DDL mode writes a `.ddl` suffix; fixed sequence-default parsing on 16/32KB pages (signed-byte regex `-?\d+` + uint32 wraparound); reverted the XMIN_COMMITTED check in isLive (PG updates hint bits lazily, which broke regression; Kingbase V8 aborted rows are still caught by isInsertAborted). Verified: PG12-18 × 8/16/32KB 10/10 green (imp_sql=0/0, imp_csv=0/0, rows_csv=100000, ddl_pk=1) + Kingbase V8 8KB count=100000, V9 SQL/CSV import 0 errors (auto-increment at 100001).
- **v1.0.8**: complete GB18030 decoding — double-byte area extended with GB18030-specific code points (private use/extension table); four-byte sequence decoding added (low range N<39420 uses a 206-entry table covering U+0080-U+FFFF code points without a double-byte form; high range N∈[189000,1237575] maps linearly to U+10000-U+10FFFF; the middle is a reserved illegal range). Verified against Python's official gb18030 codec over 500 random pairs plus a real Kingbase GB18030 instance (correct Chinese/special-char export, closed-loop import). PG servers do not support GB18030 (confirmed by test); this targets Kingbase.
- **v1.0.7**: fixed missing PRIMARY KEY DDL on PG15+ — since PG15, pg_index inserts indnullsnotdistinct after indisunique, moving indisprimary from @13 to @14 (detected per version for PG15-18; PG12-14/Kingbase V9 stay @13, Kingbase V8 (PG10 kernel) @11); all debug output removed.
- **v1.0.6**: DDL emits PRIMARY KEY (pg_index/sys_index parsing with adaptive indisprimary layout — PG15+ @14 because of indnullsnotdistinct, PG11-14/Kingbase V9 @13, PG10- kernel @11; indkey parsed correctly as int2vector disk format (vl_len_+ndim+dataoffset+elemtype+dim1+lbound1+values, previously misread as int16), with a data-area varlena locator making the indkey offset adaptive (PG12-18 @24 / Kingbase V9 with the extra column @26 / Kingbase V8 @20)); removed the dead `--toast-cache` (full was never implemented) and `--force` (never implemented) options; per-partition export verified (`--list-tables-db` shows relkind='p' parents and 'r' leaves, leaves export by relfilenode, SQL/CSV import 0 missing; a parent's relfilenode=0 has no data and its DDL omits PARTITION BY, so partitions must be rebuilt manually).
- **v1.0.5**: runtime notices — warn when a TOAST file is non-empty but the index has 0 valueids (instance online with recent writes not yet CHECKPOINTed, e.g. a 57049-row case), and when the target table is not found suggest it may not be CHECKPOINTed (disk sys_class relfilenode mismatching the data file). Confirmed logic-by-logic parity with the Python version: natts low bits (`t_infomask2 & 0x07FF`), TOAST failure placeholder (`__TOAST_MISSING__` → NULL), page-size auto-detection, 32 CLI options aligned; Kingbase V8 online/V9 copy full regression + PG12-18 × 8/16/32KB all 21 items: 50 columns × 100K rows of random Chinese data exported→imported (SQL and CSV) with count=100000 and EXCEPT 0/0 both ways.
- **v1.0.4**: fixed Kingbase V9 ghost columns — att rows left over after ALTER exceeded the actual in-row column count, producing 55 garbage columns, CSV column-count mismatch (COPY: "extra data after last expected column") and misaligned parsing that dropped rows (only 90189 of 100000 exported). Fix: pre-scan row headers and truncate the column set to the maximum in-row count (maxRowNatts); read in-row counts from the low 11 bits of t_infomask2 uniformly (`HeapTupleHeaderGetNatts`, verified identical on V8/V9/PG12-18, reverting the earlier incorrect PG high-bit branch); inject IsKB consistently (including the `--catalog-json` path); fix the 64-bit left-shift overflow in Kingbase int16 (4659) decoding (use big.Int). Verified on Kingbase V8 online with 50 columns × 100K rows of random Chinese/English data: 0 missing, 0 extra on SQL/CSV import; PG12-18 × 8/16/32KB green.
- **v1.0.3**: fixed duplicate/garbage columns caused by leftover sys_attribute rows after ALTER DROP COLUMN (deduplicated by attrelid+attnum, preferring non-dropped and the newest transaction for equal state); column counts in discovery and `--list-tables-db` exclude dropped columns; regression across the full matrix green.
- **v1.0.2**: fixed output path resolution when `-o` names a directory.
- **v1.0.1**: `--list-tables-db` output alignment.
- **v1.0.0**: first Go release, verified across the full version matrix.


MIT License. Author: raysuen
