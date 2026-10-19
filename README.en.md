# pg2sql v1.0.43

**Language: [中文](README.md) | English**

PostgreSQL / KingbaseES heap file offline parser and export tool (Go, single binary, zero dependencies).

> 📖 **Detailed User Guide** (currently in Chinese): [`docs/USER_GUIDE.md`](docs/USER_GUIDE.md) — every parameter explained, parameter combination matrix, scenario topics (partitioned tables / tablespaces / TOAST / bad blocks / encodings), FAQ. The execution package extracts to `pg2sql-<version>-<platform>/` (containing the `pg2sql` binary and a `docs/` manual directory, shipped together).

Reads PG/Kingbase heap files directly (including TOAST) — no live database required for export:

- `--sql`: INSERT statements (auto-discovered table structure, incl. TOAST reassembly / deleted-row audit; combine with `--ddl` to output CREATE TABLE)
- `--data`: CSV (`--header` first-row field names, `--delimiter` custom separator)
- `--count`: row count; `--fields`: export specific columns
- Auto-discovers table structure, auto-detects page size (8/16/32KB), auto-detects database encoding (UTF-8/GBK/Latin1)
- Supports PG 12–18 and KingbaseES V8/V9 (ORA/MySQL/PG compatibility modes; V9 includes R1C10/R3C18 sub-versions), compatible with 8/16/32KB block sizes
- `--list-tables-db` lists user objects in the database (system objects filtered by default); `--list-tables-all` lists all objects (including system objects)
- `--tables` / `--all-tables` batch-export all user tables / all tables (incl. system tables); `--schema NAME` used standalone is equivalent to `--tables --schema NAME` (batch-export a specific schema); multi-table export requires `-o` output directory plus `--sql` / `--data` export type
- DDL auto-outputs PRIMARY KEY (parses pg_index/sys_index, auto-compatible with PG12-18 / Kingbase V8 / Kingbase V9 layout differences)
- Partitioned tables: leaf partitions have independent relfilenodes and can be exported per-partition by relfilenode or `--table-name`; `--table-name` with a partitioned parent (relkind='p') auto-expands all leaf partitions for export (v1.0.36, recursive multi-level partitioning)
- **New in v1.0.38**: `--batch-rows N` multi-value INSERT (default 1=off, faster restore); `--jobs N` cross-table parallelism (default 1=off, one file per table); `--report` export report report.json (table/rows/bad blocks/file/bytes/checksum/elapsed); `--checksum crc32|md5` streaming checksum; `--import-script none|auto|sql|csv` import helper script (default none=off; sql→psql/ksql -f, csv→\copy combined with `--fields`)
- **New in v1.0.41**: `--progress` now prints the **current table name** (e.g. `表 public.huge_a`), and page progress is fixed under `--parallel` (standard parallel path was missing the progress counter; 200M-row test: 41.3%→80.7%→100.0%, pages 9385→18332→22728). Multi-table batches (`--jobs`) show per-table independent progress.
- **v1.0.40**: `--full-schema` full Schema DDL (with `--ddl`: functions/triggers/foreign keys/CHECK/view-dependency listing, filter categories via `--schema-objects`); `--large-objects` export pg_largeobject large objects (`--large-object-oids` to select OIDs, `lo_from_bytea` statements, linked with `--import-script`); `--progress` export progress & ETA (stderr output, inline refresh on TTY / line-by-line on pipe)

## Build

```bash
go build -o pg2sql .
# Cross-compile for aarch64
GOOS=linux GOARCH=arm64 go build -o pg2sql-arm64 .
```

## Usage Examples

> 💡 Quick-start below; **full parameter details, combination matrix and scenario topics (partitioned tables / tablespaces / TOAST / bad blocks / encodings / import) are in [`docs/USER_GUIDE.md`](docs/USER_GUIDE.md)**.

```bash
# Export INSERT statements (auto-discovered table structure)
pg2sql /pgdata/base/16384/16391 --sql -o out.sql

# Export DDL + INSERT
pg2sql /pgdata/base/16384/16391 --sql --ddl -o out.sql

# Export CSV (first row = field names)
pg2sql /pgdata/base/16384/16391 --data --header -o out.csv

# Export specific fields
pg2sql /pgdata/base/16384/16391 --data --fields id,name --header -o out.csv

# When -o is a directory, files are named schema.relname: dir/schema.table.sql|.csv|.ddl (extension by export type)
#   e.g. --sql → /tmp/schema.table.sql; --data --header → /tmp/schema.table.csv; --ddl → /tmp/schema.table.ddl
pg2sql /pgdata/base/16384/16391 --sql -o /tmp/
pg2sql /pgdata/base/16384/16391 --data --header -o /tmp/

# Count rows
pg2sql /pgdata/base/16384/16391 --count

# Include deleted rows / only deleted rows
pg2sql /pgdata/base/16384/16391 --sql --deleted
pg2sql /pgdata/base/16384/16391 --sql --only-deleted

# Specify table name + data directory + database OID (for multi-table data directories)
pg2sql /pgdata/base/16384/16391 --datadir /pgdata --db-oid 16384 --table-name t1 --sql

# --table-name with comma-separated multiple tables (requires --sql/--data and -o directory; one file per table)
pg2sql /pgdata/base/16384 --sql --ddl --table-name t1,t2 -o /out/        # export the given tables (SQL+DDL)
pg2sql /pgdata/base/16384 --data --header --table-name s.t1,s.t2 -o /out/  # export the given tables (CSV)
# Table names containing commas/double quotes must be wrapped in double quotes (single-quote the double quotes in shell);
# commas inside quotes are NOT treated as separators
# Compare: no quotes = comma-separated multiple tables; quotes = a complete table name inside
pg2sql /pgdata/base/16384 --sql --table-name a,b -o /out/               # 2 tables: a, b
pg2sql /pgdata/base/16384 --sql --table-name '"a,b"' -o /out/           # 1 table: name contains comma "a,b"
pg2sql /pgdata/base/16384 --sql --table-name '"a,b",c' -o /out/         # 2 tables: a,b + c mixed
pg2sql /pgdata/base/16384 --sql --table-name '"a""b"' -o /out/          # 1 table: name contains double quote a"b (""=literal ")

# --table-name with a partitioned parent: auto-expand all leaf partitions (v1.0.36, multi-level supported)
pg2sql /pgdata/base/16384 --sql --table-name public.sales -o /out/      # parent sales expands to sales_2023..2026 batch export (-o needs directory)
pg2sql /pgdata/base/16384 --sql --table-name public.sales -o out.sql   # single leaf partition keeps single-table semantics (-o may be a file)
pg2sql /pgdata/base/16384 --sql --table-name public.sales,public.t1 -o /out/  # mixed: parent expansion + normal table
# Note: empty partitioned parent (no partitions) exports 0 rows; legacy INHERITS parents (relkind='r') are NOT expanded, exported table by table

# List databases / tables in a database
pg2sql --datadir /pgdata --list-db
pg2sql /pgdata/base/16384 --list-tables-db      # user objects only
pg2sql /pgdata/base/16384 --list-tables-all     # all objects (incl. system objects)
pg2sql /pgdata/base/16384 --tables --sql -o /out/            # batch-export all user tables (SQL)
pg2sql /pgdata/base/16384 --all-tables --data -o /out/       # batch-export all tables (incl. system objects, CSV)
pg2sql /pgdata/base/16384 --tables --schema ray --sql -o /out/  # batch-export a specific schema
pg2sql /pgdata/base/16384 --schema ray --sql -o /out/             # equivalent: --schema standalone triggers batch

# Tablespace tables (real directory not base/, pg_tblspc/ symlink prefix auto-discovery, no extra args; sys_tblspc/ for Kingbase)
#   pg_relation_filepath returns e.g. pg_tblspc/16391/PG_17_202307071/16384/16391; prepend the data root and export directly:
pg2sql /pgdata/pg_tblspc/16391/PG_17_202307071/16384/16391 --sql -o out.sql
# Fallback: dangling symlink / only real path → absolute real path + --datadir/--db-oid to locate catalog explicitly:
pg2sql /ts_data/PG_17_202307071/16384/16391 --datadir /pgdata --db-oid 16384 --sql -o out.sql
# v1.0.34 directory mode (--datadir + --db-oid equivalent to base/<oid>, tablespace tables auto-discovered, real path not needed):
pg2sql --datadir /pgdata --list-db                          # filepath column multi-line (base + one line per tablespace)
pg2sql --datadir /pgdata --db-oid 16384 --list-tables-db    # "tablespace" column (0=base, non-0=pg_tblspc/<oid>)
pg2sql --datadir /pgdata --db-oid 16384 --schema public --sql -o /out/   # batch export (incl. tablespace tables)
pg2sql --datadir /pgdata --db-oid 16384 --table-name public.t_ts --sql -o out.sql   # single table (incl. tablespace tables)

# Export metadata JSON and replay it offline (catalog-json mode)
pg2sql /pgdata/base/16384 --export-meta -o meta.json
pg2sql /pgdata/base/16384/16391 --catalog-json meta.json --table-name t1 --sql --ddl

# Specify database encoding (default: auto-detect)
pg2sql /pgdata/base/16384/16391 --data --encoding gbk -o out.csv

# Parallel parsing (speed up large tables; workers do parse→escape→produce + page-order window ordered forwarding; memory O(window×parallelism), large tables don't OOM)
pg2sql /pgdata/base/16384/16391 --sql --parallel 4 -o out.sql

# New in v1.0.38:
# Multi-value INSERT (restore speed-up: N≥2 merges into INSERT ... VALUES (...),(...), default 1=one row per statement)
pg2sql /pgdata/base/16384/16391 --sql --batch-rows 1000 -o out.sql
# Cross-table parallelism (one file per table, default 1=serial; stacks with --parallel intra-table parallelism)
pg2sql /pgdata/base/16384 --tables --sql --jobs 4 -o /out/
pg2sql /pgdata/base/16384 --sql --table-name t1,t2,t3 --jobs 4 -o /out/
# Export report + checksum (report.json: table/rows/bad blocks/file/bytes/checksum/elapsed)
pg2sql /pgdata/base/16384 --tables --sql --jobs 4 --report --checksum md5 -o /out/
# Import helper script (sql→psql/ksql -f; csv→\copy combined with --fields; batch generates import_all.sh)
pg2sql /pgdata/base/16384/16391 --sql --import-script auto -o /tmp/
pg2sql /pgdata/base/16384/16391 --data --fields id,name --import-script csv -o /tmp/
```

Import verification: `psql -d newdb -f out.sql` or `ksql -d newdb -f out.sql` (import into the same version you exported from).

## Built-in Type Support (PG 12~18 / KingbaseES V8/V9, all block sizes)

| Category | Types (oid/family) | Notes |
| --- | --- | --- |
| Numeric | int2/int4/int8, numeric, float4/float8, money, oid, serial family, Kingbase mysql TINYINT(8100)/MEDIUMINT(7016)/YEAR(7025)/UNSIGNED series | full precision |
| Character | text, varchar, bpchar, name, Kingbase clob(8014), Kingbase mysql DATETIME(7952)/ENUM (dynamic oid) | Chinese/special chars/newlines/empty string/NULL; ENUM decoded by enum members |
| Binary | bytea, Kingbase mysql BINARY(3383) | bytea 0x hex reversible; BINARY strips varlena header and trailing \x00 padding to text; embedded \x00 outputs hex (byte-reversible) |
| Boolean/Bit | bool, bit(n), varbit, Kingbase mysql BIT(4655) | bit-string/hex dual channel |
| Date/Time | date, time, timetz, timestamp, timestamptz, interval, Kingbase datetime(7952)/timestamp(7954)/time(7950)/date(7944)/ora_date(8020)/mysql_datetime_in etc. | incl. negative microseconds before 2000, 24:00:00 boundary |
| JSON/XML | json, jsonb (incl. Kingbase mysql_json 4802), xml (PG 4B marker/Kingbase plain text) | |
| Arrays | arrays of any type (1-D/multi-D/empty; int[]/text[]/varchar[] etc.) | `{{1,2},{3,4}}`/`{}` |
| Geometric | point, line, lseg, box, path, polygon, circle | |
| Network | inet, cidr, macaddr, macaddr8 | |
| Full-text | tsvector, tsquery | byte-identical with PG text output |
| Range | int4range/int8range/numrange/daterange/tsrange/tstzrange | incl. empty/half-open intervals |
| System | regclass/regproc reg* family, pg_lsn, txid_snapshot, enum | |
| Composite | record (user-defined, dynamic oid) | **Limit**: outputs raw bytes `E'\x...'` (byte-reversible); prefer online pg_dump; planned for future versions |

**Limitations**: ① composite types (record) on-disk layout is affected by heap_fill_tuple short-varlena conversion and alignment and varies widely across versions; currently fall back to raw bytes (no data corruption); ② Kingbase MySQL-mode SET type (dynamic oid, typtype=y): on-disk 16-byte member bitmask, member names not stored in catalog (neither pg_enum nor typtypmod); exported as \x hex (byte-level auditable/backup-safe); direct SQL/CSV reload zeros the member mask (Kingbase set_in cannot restore byte-wise), requiring application-layer semantic conversion before re-import. All other types decode to reversible text/binary literals.

## Data Reliability

- TOAST out-of-line fields auto-linked and reassembled (light index / page-level LRU cache)
- Encoding detection prefers `pg_database` (UTF-8/GBK/Latin1 reversible decoding)
- Page size auto-detection: 8KB/16KB/32KB and Kingbase variants

## Large Table Export: Memory & Time Estimates (measured in v1.0.30)

**Measured baseline** (PG16.15, 2-core machine, data in OS page cache): 50-column table (id + 49 varchar/text mix, incl. Chinese/special chars) with 1.5M rows, heap file **1.9GB** (PG 8KB-page 1GB segments; actually `16459` + `16459.1` two segments; v1.0.30 auto-reads across segments):

| Export mode | Elapsed | MaxRSS |
| --- | --- | --- |
| Serial SQL | 33.3s | 17MB |
| `--parallel 4` SQL | 27.8s | 17MB |
| Serial CSV (with header) | 24.8s | 17MB |
| `--parallel 4` CSV | 18.4s | 17MB |
| `--count` (parallel 4) | 11.8s | 17MB |

Output file size: SQL ≈ heap × **0.77** (measured 1.47GB/1.9GB), CSV ≈ heap × **0.51** (measured 0.97GB/1.9GB).

**Memory conclusion**: v1.0.30 plan B is O(window×parallelism) constant memory — 1.5M rows/1.9GB stays at **constant 17MB MaxRSS** throughout (identical serial and parallel); the old implementation (v1.0.29 and earlier) collected all rows for the same size (50 cols × 1.5M rows ≈ 5-6GB) and would inevitably OOM (measured: a 3GB machine's parallel regression once OOM-killed all Kingbase instances). A 50GB table still uses constant memory (~20MB + fixed TOAST page-LRU cap), **independent of table size**.

### Memory Comparison vs v1.0.29 and earlier (50-column table, ~1.3KB avg row)

| Table size | v1.0.29 and earlier (full collection) | v1.0.30 (streaming + page-order window) |
| --- | --- | --- |
| 100K rows | ≈350-400MB | ≈17MB |
| 1M rows | ≈3.5-4GB | ≈17MB |
| 1.5M rows (1.9GB heap) | ≈5-6GB, **OOM on 3GB machines** (measured: OOM-killed all Kingbase instances) | **17MB** (serial / `--parallel 4` identical) |
| 50GB-scale | ≈170GB+, infeasible | ≈20MB constant (+TOAST page-LRU fixed cap) |

Memory drops from **O(rows) to O(window×parallelism) constant**, independent of table size.

### Time Comparison vs v1.0.29 and earlier (2 cores, 1.5M rows / 1.9GB heap, two segments)

| Mode | v1.0.29 (single-thread escape) | v1.0.30 (workers: parse→escape→produce) | Improvement |
| --- | --- | --- | --- |
| Serial SQL | OOM at this size, no data | 33.3s | — |
| `--parallel 4` SQL | OOM at this size | 27.8s | **-17%** vs serial |
| Serial CSV | OOM at this size | 24.8s | — |
| `--parallel 4` CSV | OOM at this size | 18.4s | **-26%** vs serial |
| `--count` | OOM at this size | 11.8s (parallel 4) | — |

> v1.0.29 OOM'd at the 1.5M-row scale due to full collection, so no comparable timing exists; at 100K rows the two versions produced byte-identical md5 output across 9 groups (zero semantic regression). On 8 cores, parallel speedup is 2.5–3x (historically measured).

**Time extrapolation** (linear, 2-core baseline; output files need extra disk space; `--parallel` pays off more with more cores — measured 2.5–3x on 8 cores):

| Heap size | Serial SQL | par4 SQL | par4 CSV | SQL output | CSV output |
| --- | --- | --- | --- | --- | --- |
| 5GB | ~1.5 min | ~1.2 min | ~0.8 min | ~3.9GB | ~2.6GB |
| 10GB | ~2.9 min | ~2.4 min | ~1.6 min | ~7.7GB | ~5.1GB |
| 20GB | ~5.8 min | ~4.9 min | ~3.2 min | ~15.4GB | ~10.2GB |
| 30GB | ~8.7 min | ~7.3 min | ~4.8 min | ~23.1GB | ~15.3GB |
| 50GB | ~14.5 min | ~12.2 min | ~8.1 min | ~38.5GB | ~25.5GB |

> Basis: time = heap-size ÷ 1.9GB × measured elapsed, linear extrapolation; the main variables are CPU parse/escape cores and write bandwidth (CPU is the bottleneck on 2-core machines; fast disks add no write time); the 50-column wide-row baseline (~1.3KB avg row) is the reference — narrow tables have more rows per volume and cost slightly more per-row, wide tables (more TOAST out-of-line) have additional TOAST reassembly overhead.

### jobs × parallel Resource Calculation (v1.0.38)

`--jobs N` (cross-table parallelism) and `--parallel P` (intra-table page-level parallelism) stack orthogonally:

- **Total parallelism = jobs × parallel**; align it with core count (oversubscription → context switches + disk I/O contention; on unstable rescue disks, lower `--jobs` first).
- **Memory formula**: `total ≈ jobs × (per-table base ~12MB + parallel × per-worker increment)`; 8KB-page worker increment ≈2-3MB, 16KB ≈3-5MB, 32KB ≈4-8MB (page-order window O(parallelism), independent of table size). **Tens to hundreds of MB — always in the safe zone.**

Example on an 8-core machine (32KB pages, 8MB per-worker increment):

| Combination | Total parallelism | Estimated memory |
| --- | --- | --- |
| `--jobs 1 --parallel 8` | 8 | ~12MB + 8×8MB ≈ **76MB** |
| `--jobs 4 --parallel 2` | 8 | 4×(12MB + 2×8MB) ≈ **112MB** (recommended, ≤4× serial memory) |
| `--jobs 8 --parallel 1` | 8 | 8×12MB ≈ **96MB** (cross-table, minimal per-table memory) |
| `--jobs 8 --parallel 4` | 32 | 8×(12MB + 4×8MB) ≈ **352MB** (heavily oversubscribed, not recommended) |

**Rescue-scenario advice**: on unstable/slow-I/O disks, first fix `--parallel 2~4` for single-table throughput, then allocate `--jobs` by core count; 50-table example (16 cores, `--jobs 8 --parallel 2`): memory ≈ 8×(12MB+2×8MB)=224MB, far below the old full-collection single-table 5-6GB scale.

## Bad Block (Corrupt Page) Handling

pg2sql uses a "page-by-page, tuple-by-tuple protection + skip corrupt units + export the rest normally" strategy: when data pages contain bad blocks, the export **does not abort**; everything that can be parsed is output normally, corrupted parts are skipped and honestly reflected in the row count:

| Level | Protection logic | Bad-block behavior |
| --- | --- | --- |
| Page header | lower/upper/special range checks (`24 ≤ lower ≤ upper ≤ special ≤ pageSize`), `tryStandardLayout`/`tryAutoLayout` dual-mode probing | whole page skipped if header corrupt, continue with next page |
| Page items | itemid checks: flags≠NORMAL, offset/length out of range (`off ≥ pageSize`, `off+ln > pageSize`, `ln < header size`) | skip only that item; other valid items on the page continue |
| Tuple header | `parseTuple` length ≥ 24, `headerConsistent` check (t_hoff range 24~256 and bitmap/oid length consistent with t_hoff) | tuples with inconsistent headers are judged bad and skipped |
| Transaction status | `isInsertAborted` (XMIN_INVALID, uncommitted) / `isDeleted` (XMAX delete) determination | aborted rows skipped; deleted rows skipped by default (`--deleted` / `--only-deleted` export for audit) |
| Field level | `extractFieldsDirect` out-of-bounds → NULL, `varPayload` returns empty for corrupt/oversliced TOAST headers, 4B/4BC varlena out-of-bounds protection | a single bad field outputs NULL/empty without aborting the rest of the row; undecodable bytes fall back to latin-1 (0x00-0xFF byte-reversible, no data corruption) |
| Table level (batch) | auto-discovery failure / table-file parse failure counted | `--tables`/`--all-tables` batch export ends with "succeeded N tables, skipped N, failed N"; bad tables skipped without affecting the rest |

Notes:
- Bad-block skipping happens at page/item/tuple/field granularity; the finer the granularity, the less data is affected and the more complete the remaining export.
- Single-table direct export: rows parseable within a bad page are still output (count less than actual); batch mode summarizes skipped/failed stats.
- If a TOAST out-of-line pointer is corrupt or a TOAST page is bad, the affected field outputs NULL/empty (no error abort); other fields and rows are unaffected.
- Known limit: when a whole page is corrupt and its header is unrecognizable, that page's data is unrecoverable (equivalent to PG's `zero_damaged_pages` behavior); for bad-page detail, cross-check with `pg_filedump`/`--verbose` output.

## Kingbase TDE Transparent Encryption Offline Decryption (v1.0.39)

pg2sql supports **offline decryption export** of Kingbase TDE-transparent-encrypted data files (no live instance, no database password), covering two encryption scenarios:

| Encryption scenario | Creation syntax | pg2sql parameters | Decryption principle |
| --- | --- | --- | --- |
| Table encryption | `CREATE TABLE t (...) ENCRYPTED BY 'KEY'` | `--enckey KEY` | data page offset64+ decrypted with SM4-ECB (page header 0-63 plaintext; key = ENCRYPTED BY string + 0x00 padded to 16B; TOAST uses the same key) |
| Tablespace encryption | `CREATE TABLESPACE ts LOCATION '...' WITH (encryption=true, encmethod='sm4')` | `--wallet-password PW` (optional `--wallet-dir DIR`) | wallet chain offline derivation: KEK→MK→TSPK (no password needed); tspkey.kr multi-tablespace entries matched automatically |

Usage examples:

```bash
# Table-encrypted offline decryption export (file-path mode)
pg2sql /pgdata/base/16384/21747 --sql --table-name public.t_enc --enckey k1ngb2se -o /tmp/out/

# Tablespace-encrypted (directory mode: auto-discover tablespace + auto-probe wallet)
pg2sql --datadir /pgdata --db-oid 16384 --table-name public.t_tspc \
  --sql --wallet-password 'Wal1et#Tde2026' -o /tmp/out/
```

Notes and boundaries:
- **Tablespace keys are derivable offline without a password** (the wallet ships with the data directory; directly usable in crash scenarios); `--wallet-password` only does an SM3 check and warns (V8 check offset differs from V9; a "password check failed" warning does not affect decryption).
- `--wallet-dir` defaults to auto-probing `<PGDATA>/.wallet`; multi-tablespace/multi-wallet entries are matched by tablespace OID automatically.
- Decryption is injected into both serial and parallel (`--parallel N`) paths; exported content is byte-identical.
- Prerequisite: the Kingbase instance must load `sysencrypt` (first entry of `shared_preload_libraries` contains `sysencrypt`) to create encrypted objects; the offline decryption side does not depend on that extension.
- Without the key, encrypted pages parsed as plaintext yield garbage/0 rows — if the instance is online and encryption is suspected, first confirm whether the table is `ENCRYPTED BY` / the tablespace has `encryption=true`.

## Feature Coverage Matrix

Full-feature regression = data plane (all-type export→import closed loop across PG12-18 at every block size and Kingbase V8/V9) + parameter plane (CLI behavior assertions for every parameter). Since v1.0.33 the parameter plane covers all CLI feature segments, executed together with the data plane on the full-version representative matrix; since v1.0.34 it additionally covers "directory-mode tablespace auto-discovery (multi-tablespace + 50K-row tablespace big table)" assertions.

| Feature | Coverage | PG12-18 | Kingbase V8/V9 |
| --- | --- | --- | --- |
| `--sql`/`--data` export (incl. Chinese/special chars/NULL/empty/50-col big tables) | data plane + parameter-plane count | 7 versions × 8/16/32K all green | V8 ORA/PG mode + V9 all green |
| `--ddl` CREATE/index/sequence/default/comment | parameter-plane ddl_imp/ddl_struct (database-replay closed loop) | 9 combos all green | 3 instances all green |
| `--count` | parameter-plane count (exact row compare) | 9 combos all green | 3 instances all green |
| `--fields` specific columns | parameter-plane fields (CSV import closed loop) | 9 combos all green | 3 instances all green |
| `--limit` | parameter-plane limit | 9 combos all green | 3 instances all green |
| `--parallel` parallel export | parameter-plane parallel (serial/parallel md5 identical) | 9 combos all green | 3 instances all green |
| `--batch-rows` multi-value INSERT (v1.0.38) | parameter-plane batch (multi-value import closed loop + md5 identical to row-by-row) | 9 combos all green | 3 instances all green |
| `--jobs` cross-table parallel (v1.0.38) | parameter-plane jobs (jobs>1 vs jobs=1 output md5 identical + per-table files complete) | 9 combos all green | 3 instances all green |
| `--report`/`--checksum` (v1.0.38) | parameter-plane report (report.json fields + checksum matches independent calc + bad-block count) | 9 combos all green | 3 instances all green |
| `--import-script` (v1.0.38) | parameter-plane imp_script (sql/csv script generation + content assertions + import_all.sh) | 9 combos all green | 3 instances all green |
| `--list-tables-db`/`--list-tables-all` | parameter-plane listdb/listall (user vs system object filter) | 9 combos all green | 3 instances all green |
| `--list-db` | parameter-plane list_db | 9 combos all green | 3 instances all green |
| `--tables`/`--all-tables` batch | parameter-plane batch/alltables | 9 combos all green | 3 instances all green |
| `--schema` batch | parameter-plane batch | 9 combos all green | 3 instances all green |
| `--table-name` (single/multi/quoted identifiers) | data-plane multi-table + quoted-identifier special tests | 7 versions all green | V8/V9 all green |
| `--export-meta` + `--catalog-json` | parameter-plane meta | 9 combos all green | 3 instances all green |
| `--encoding` (database decode encoding) | parameter-plane encoding: UTF8 explicit=auto, LATIN1 closed loop, Kingbase GBK/GB18030 closed loop | 9 combos all green | V9 all green; V8 GBK all green, GB18030 `SKIP(env)`* |
| `--deleted`/`--only-deleted` | parameter-plane deleted (DELETE-without-VACUUM 70/100/30 assertions) | 9 combos all green | 3 instances all green |
| `--header` (CSV first-row field names) | data-plane CSV import closed loop + parameter-plane fields | 7 versions all green | V8/V9 all green |
| Bad-block skip (page/item/tuple/field 4-level) | parameter-plane badpage (dd-corrupted middle page) | 9 combos all green | 3 instances all green |
| Tablespace (real dir not base/, auto-discovery + fallback) | parameter-plane tablespace + tspace_big/tsdir_batch/tspace_col | 9 combos all green | 3 instances all green |
| Directory-mode tablespace auto-discovery (v1.0.34: multi-line filepath in `--list-db` / tablespace column in `--list-tables-db` / `--schema`/`--table-name`/`--all-tables` locate tablespace tables under `--datadir --db-oid`) | parameter-plane listdb_multi/tspace_col/tsdir_batch/tspace_big (multi-tablespace + 50K-row Chinese/special-char tablespace big table SQL+CSV import closed loop) | 9 combos all green | 3 instances all green |
| Kingbase TDE transparent encryption (v1.0.39: `--enckey` table / `--wallet-password`+`--wallet-dir` tablespace, incl. TOAST) | full-version encryption special test (reg_tde_r1c10.sh + V8/V9 measured): table-encrypted 100 rows incl. TOAST long text + tablespace-encrypted 50 rows plaintext verified | N/A (no TDE in PG) | V8R6 ORA/PG 8/16/32K + V9R1C10 3 modes × 3 page sizes + V9 legacy, 14 instances 28/28 all green |
| Large-table export (100K-row t_big + 50K-row tablespace big table; 1.5M-row special test) | parameter-plane parallel/badpage + data-plane big-table special test | 9 combos all green | 3 instances all green |
| TOAST out-of-line/compressed/multi-segment (>1GB) | data-plane TOAST special test + 1.5M-row big-table special test | 7 versions all green | V8/V9 all green |
| Multi block-size auto-detection (8/16/32K) | data plane + parameter plane | 12.22/16.15/18.6 × 8/16/32K all green | V8 8/16/32K + V9 8K all green |

\* On V8 instances (ORA/PG modes), the ksql client cannot safely write UTF8 SQL into a GB18030 database (`invalid byte sequence`) — a test-environment limitation, not a tool defect; GB18030 decoding is byte-level code-table lookup (kernel-version independent), fully asserted on V9, and GBK decoding is verified on both V8 and V9.

## Dual-Version Release (since v1.0.39)

Since v1.0.39, two binary series are released — **free edition** and **TDE paid edition** (compile-time `-tags tde` gated, same source tree):

| Dimension | Free edition `pg2sql` | TDE edition `pg2sql-tde` (version string carries `(TDE)`) |
|---|---|---|
| Build | `go build .` | `go build -tags tde .` |
| `--enckey` / `--wallet-password` / `--wallet-dir` | unrecognized (errors, suggests TDE edition) | fully supported |
| TDE code in binary | **excluded at compile time** (0 strings hits, cannot be extracted) | includes SM4-ECB/SM3 national-crypto implementation |
| `--version` / `--help` | `pg2sql 1.0.39` / no TDE parameters | `pg2sql 1.0.39 (TDE)` / includes 3 TDE parameters |
| Export features | identical to TDE edition (no decryption) | identical to free edition (with decryption) |

- Package naming: free edition `pg2sql-<version>-<platform>/`, TDE edition `pg2sql-<version>-tde-<platform>/` (both extract to `binary + docs/USER_GUIDE.md`).
- Source packages: free-edition source does not contain the `tde/` directory (`go build` compiles directly); TDE-edition source contains `tde/` (`go build -tags tde`).
- Both share the same regression baseline: free edition = full-version regular regression; TDE edition = regular regression + Kingbase TDE encryption special test (14 instances 28/28 all green, incl. plaintext closed-loop md5 verification).

Data-plane regression assets: `pgbuild/reg_pg_129_serial.sh` (PG 66 items), `pgbuild/reg_kb_130.sh` (Kingbase 25 items); parameter plane: `pgbuild/test_params_pg.sh` (20 assertions), `pgbuild/test_params_kb.sh` (26-27 assertions), `pgbuild/reg_params_all.sh` (full-version scheduler); big-table + tablespace special tests: `pgbuild/reg_bigts.sh`.

## Changelog

- **v1.0.44**: P2 destructive-scenario fix round (full-version × full-feature regression passed before packaging):
  ① **P2-1 supplement: tablespace files included in encoding probe** — TABLESPACE databases (data under `PGDATA/pg_tblspc`) were not sampled, so the encoding decision was missing. Fixed: V8 32K tablespace databases now fall back to UTF8 correctly, V9R1C10 tablespace databases stay on true GBK (no fallback) — both match online 100%;
  ② **P2-2 bad-page detail** — `report.json` / stderr now report `bad_blocks` and `bad_pages` (explicit bad-page list) with preserved skip counts for rows on bad pages;
  ③ **P2-3 TOAST missing-file counter** — when a TOAST file is missing, pg2sql no longer silently emits NULLs with no count; an empty ToastFile is still built so the `toast_missing` counter takes effect;
  ④ **P2-4 DDL directory-missing warning** — files/directories required by `--ddl` that are missing now produce an explicit warning (was silent);
  ⑤ **4 robustness bugs fixed** — pg_index-segment empty-path panic; `detectSysFile` with pageSize=0 zero-length-buffer out-of-range panic; `tryAutoLayout` short-buffer out-of-range; TOAST file missing causing silent NULLs with no count;
  ⑥ Regression: `pgbuild/reg_v1044.sh` adds a [V1044] P2 destructive-assertion section (encoding fallback / bad-page detail / TOAST counter / DDL warning); the three-stage suite (21 parameter instances + 16 big-table instances + 22 mixed combinations + partitions) and the TDE special suite all passed.

- **v1.0.43**: GBK-library probe extended to V8/V9 compatibility modes; one real bug fixed and the encoding-behavior conclusion corrected:
  ① **Fix: 32K-page sampling missed the fallback** — `probeDirUtf8Ratio` read only the first 16384 bytes of each file; PG heap tuples sit at the **end of each page** (they grow backwards from the tail), so 8K/16K pages were fully covered but on a **32K page the first 16KB is only page header + free space with no row data** → UTF8 characteristics missed → no fallback → mojibake (caught on V8 ORA 32K: `缂栫爜馉鍫冩祴璇`). Fix: read the **last page of the file** (`ReadAt(size-pageSize)`); 8K/16K/32K all trigger now;
  ② **Corrected mode-matrix conclusion** (measured V8 ORA/PG/MySQL + all 9 V9R1C10 instances, 3 modes × 8K/16K/32K): **all V8 modes store UTF8 bytes for GBK-declared databases** (even `client_encoding=GBK` inserts; hex `f0 a0 80 80`; auto fallback matches online 100%); **all V9 stores true GBK double-byte** (V9R1C10 ORA/PG/MySQL × 8/16/32K 9 instances + V009R003C018 MySQL all measured: `b1 e0`=编, `c6 d5`=普; the extension char is replaced with `?` at insert time) — auto **correctly does NOT fall back**, decodes as GBK matching online, and only `--encoding utf8` produces mojibake. So "Kingbase GBK always stored as UTF8" is NOT true — it differs by major version: **V8 stores UTF8, V9 stores true GBK**; the fallback mechanism is verified correct in both directions (UTF8 databases fall back, true-GBK databases are never harmed);
  ③ Regression: `pgbuild/reg_v1043.sh` adds V8 ORA 32K fallback assertions ×2 + V9 true-GBK no-fallback/content assertions ×2; full versions × full features passed before packaging.

- **v1.0.42**: Fixes from a full code/functional BUG analysis (conclusion: no P1; core parsing / parallelism / streaming / cross-checks all correct):
  ① **P2-1 Kingbase "declared GBK but stored as UTF8" auto fallback**: measured on Kingbase V8 — a database created with `ENCODING 'GBK'` holding `编码𠀀堃测试` (incl. extension characters) actually stores **UTF8 bytes** (hex `f0 a0 80 80`), while `pg_database.encoding=GBK` made the old build decode as GBK and emit mojibake `'ç¼ç ð åµè¯'`. Fix: a) correct the Kingbase GBK encoding id **35** mapping (was wrongly latin1); b) when auto-detection finds a non-UTF8 declaration, **sample user-table data bytes for UTF8 characteristics** (per-file independent check: multi-byte sequence validity ≥0.7 with ≥4 sequences triggers fallback); UTF8-featured bytes auto-switch to UTF8 with a hint `已自动切换 UTF8（可用 --encoding gbk 强制）` — measured export `编码𠀀堃测试` 100% matches online; true GBK/GB18030 double-byte streams stay below 0.5 validity and never trigger (pure-GBK bytes ratio=0.0 measured); sampling skips system-catalog files (relfilenode<16384);
  ② **P3-1**: `--fields` with unmatched column names now emits a stderr warning (was silently ignored — a typo could silently drop a column);
  ③ **P3-2**: `--deleted` + `--only-deleted` together now error out (semantically mutually exclusive; was silently taking only-deleted);
  ④ **P3-3**: `--batch-rows` outside SQL mode (`--data` / pure `--ddl`) now hints on stderr that it is ignored (was silent);
  ⑤ **P3-4**: `--limit 0` semantics documented (0 = unlimited, default) — help / README / user guide synced;
  ⑥ Regression: `pgbuild/reg_v1042.sh` adds P2-1 (Kingbase testgbk auto fallback + export-content match) and P3-1~P3-4 assertions; full versions × full features passed before packaging.

- **v1.0.41**: Fixed two `--progress` issues:
  ① **BUG-B fixed (page progress stuck at 0 under --parallel)**: `streamStdParallel` (standard-layout parallel path used by `--parallel` on ordinary tables) was missing the `progressDone` counter, so `--progress --parallel N` showed 0 pages and a frozen percentage. Fixed by adding the atomic counter (consistent with serial/auto-layout paths). Verified on a 2M-row table with `--parallel 4`: 41.3%→80.7%→100.0%, pages 9385→18332→22728;
  ② **Progress line now shows the current table name**: `[pg2sql] 进度 41.3% 表 public.huge_a 页 9385/22728 行 825774 耗时 1.0s ETA 1.4s` — under multi-table batch (`--jobs` inter-table parallelism) each table reports its own progress with its name, so you can always tell which table is being exported;
  ③ Regression-script fix: PG13/14/15/17 data directories use the `pgdata_ft_` prefix, but the script only looked for `pgdata_prm_`, so those versions were actually skipped (the 4 leftover FAILs in the v1.0.40 log were this skip). v1.0.41 falls back prm→ft, so the full-version matrix is truly covered; --progress assertions strengthened (table name + page progress > 0 under --parallel);
  ④ Regression: full versions × full features (PG 12-18 × compiled block size + Kingbase V8 8K/16K/32K + V9) passed before packaging.

- **v1.0.40**: Added 3 features + fixed 1 catalog column-alignment bug:
  ① `--full-schema` full Schema DDL (with `--ddl`, writes `schema_objects.ddl`): functions (CREATE OR REPLACE FUNCTION, rebuilt column-by-column from pg_proc; function sections now read on all PG versions), triggers (CREATE TRIGGER, rebuilt from pg_trigger), foreign-key/CHECK constraints (pg_constraint; FK referenced-column list + CHECK expression text extraction), views/CHECK emitted as "placeholder comment + dependency listing" (no offline pg_node_tree decompilation, same decision as pg_dump); `--schema-objects LIST` comma-separated category filter (view,function,trigger,constraint,fk; default all);
  ② `--large-objects` exports pg_largeobject large objects (`lo_from_bytea` single statement + 2KB chunk assembly; `--large-object-oids` selects OIDs; linked with `--import-script`). Kingbase adaptations: pg_class.relfilenode stores OID rather than relfilenode → probe the standard physical file 2613 by content first, fall back to the relfilenode path; Kingbase pg_attribute lists system columns as positive attnums → large objects decoded with a fixed 3-column layout + TOAST 4BC reassembly;
  ③ `--progress` export progress & ETA (stderr output: pages/rows/elapsed/ETA; TTY detection `\r` inline refresh, line-by-line on pipe; pre-scan double-counting removed, progress accurate to 100%);
  ④ **BUG-A fix**: `parseNamedTuple` with hasOID=true re-read the oid system-row metadata column (attnum=1) in pg_attribute as a user column, shifting subsequent columns by 4B → user functions in pg_proc became unreadable on PG instances ("boolin" read as "in"). Fix: only on standard PG (!isKB) skip the attnum=1 oid metadata row; Kingbase behavior unchanged (different hasoids semantics). After the fix, full-schema function sections read correctly on all PG versions;
  ⑤ **Known limitation (Kingbase)**: user functions (pg_proc) on Kingbase V8/V9 have row bytes present in data pages, but the page-header item directory is not updated (Kingbase private write mechanism, page-header lower=0), so `iterSysTuples` cannot enumerate them → full-schema function section = 0 on Kingbase. Triggers/constraints (incl. FK)/view dependencies/large objects all work;
  ⑥ Regression: full-version full-function (PG 12/13/14/15/16/17/18 × compiled block sizes + Kingbase V8 8K/16K/32K + V9 8K, incl. SQL/CSV/full-schema/large-objects/progress + SPECIAL batch/checksum/report/jobs/import + TDE regular export, PASS=54 FAIL=0) passed before packaging.
- **v1.0.39**: Added Kingbase TDE transparent-encryption offline decryption (independent module `tde/`, architecture prepared for future TDE monetization) + **dual-version release** (free / TDE editions, compile-time `-tags tde` gated):
  ① Self-developed SM4-ECB/SM3 national-crypto implementation (fixed key-expansion missing τ + output byte order; three-vector self-test all PASS), no external dependencies;
  ② `--enckey KEY`: table-encryption offline decryption (`CREATE TABLE ... ENCRYPTED BY 'KEY'`, page offset64+ SM4-ECB, TOAST same key, serial/parallel paths fully covered);
  ③ `--wallet-password PW` + `--wallet-dir DIR` (auto-probes `<PGDATA>/.wallet`): tablespace-encryption offline decryption (wallet chain KEK→MK→TSPK derived offline, no password needed; tspkey.kr multi-tablespace entries matched automatically);
  ④ Full-version encryption regression: Kingbase V8R6 (ORA 8K + PG 8K/16K/32K) + V9R1C10 (PG/ORA/MySQL × 8K/16K/32K, 9 instances) + V9 legacy, 14 instances 28/28 all green (table-encrypted 100 rows incl. TOAST long text + tablespace-encrypted 50 rows plaintext verified);
  ⑤ Boundary notes: TDE prerequisite is `shared_preload_libraries` containing `sysencrypt`; the V8 wallet-password SM3 check offset differs from V9 (only affects the `--wallet-password` check warning; offline derivation decryption unaffected).
- **v1.0.38**: Added 4 features (all off by default) + fixed 3 tablespace-resolution bugs:
  ① `--batch-rows N` multi-value INSERT (default 1=off): consecutive rows merged into `INSERT ... VALUES (...),(...)` (deleted rows output separately as single `-- DELETED` statements, orthogonal page-order preservation with `--parallel`), faster restore/import;
  ② `--jobs N` cross-table parallelism (default 1=off): goroutine pool exports tables in parallel under `--table-name` multi-table / `--tables` / `--schema` / `--all-tables`, one file per table, per-table failures recorded independently without aborting, output byte-identical to serial (self-tested 3-table / Kingbase 4-table md5 all identical);
  ③ `--report` + `--checksum crc32|md5` (default off): `report.json` (table/format/rows/bad blocks/file/bytes/checksum/elapsed/page size/encoding, jobs concurrency-safe), providing integrity evidence for rescue exports; checksum files computed streaming (no `-o`: JSON printed to stderr);
  ④ `--import-script none|auto|sql|csv` (default none=off): single-table import script `import_<schema>_<table>.sh` (sql→`psql/ksql -f`, csv→`\copy` combined with `--fields` column list, env-overridable `PSQL`/`DB`/`PGCLIENTENCODING`) + batch `import_all.sh`;
  ⑤ **Fix**: `resolveDataRoot` was misled by `base/<dboid>/PG_VERSION` (PG17+) or Kingbase `base/<dboid>/SYS_VERSION` into treating a database dir as the data root → PG17/18 and Kingbase tablespace tables could not be exported; now determined by database-directory features (`pg_filenode.map`/`sys_filenode.map`/catalog `1247` — any present → skip and keep walking up; data root confirmed by `global/`); Options hardcoded `DBOID:5` broke tablespace location/encoding detection when the database OID≠5 — dboid now derived from the dbDir directory name first. Fix verified: PG16 t_ts/t_ts2 (tablespace tables), Kingbase V9 t_kts/t_kts2 (tablespace tables) export OK; Kingbase database-encoding detection restored;
  ⑥ Docs synced: README/USER_GUIDE/`--help` gained the 5 parameters and the **jobs×parallel memory/CPU resource calculation** section; packaged after full-version regression (parameter plane gained batch/jobs/report/imp_script assertions + tablespace assertions covering PG17/18 and Kingbase).
- **v1.0.37**: Kingbase V9R1C10 x86 9 instances (ORA/PG/MySQL × 8/16/32KB) added to the full-version regression matrix (parameter plane + big-table special test + mixed scenarios + partition special test); fixed R1C10 `sys_description` gaining a `name` column causing garbled COMMENT (comment parsing now dynamically probes the real `sys_attribute` column layout).
- **v1.0.36**: `--table-name` supports specifying a partitioned parent to auto-expand all leaf partitions (v1.0.36):
  ① **Feature**: in single-table mode, specifying a declarative partitioned parent (relkind='p') recursively expands all leaf partitions (relkind='r', multi-level supported) — a single leaf keeps single-table semantics (`-o` may be a file); multiple leaves auto-switch to batch export (`-o` must be a directory + `--sql`/`--data` required); in `--table-name parent,normal_table` mixed scenarios the parent expands too; empty partitioned parents (no partitions) keep original behavior (0 rows + verbose hint);
  ② **Implementation**: `catalog.go` added `pg_inherits` (Kingbase `sys_inherits`) parsing (`detectSysFile` auto-recognizes; layout takes first two fields `inhrelid`/`inhparent`; extra trailing columns in PG14+ don't affect offsets), building an `inhparent → direct child OID` map; `TableMeta` gained `OID`/`ChildOIDs`; `main.go` added `expandParentToLeaves` recursive expansion (cycle-guarded with a seen set), wired into both single-table path and `runNamedTables` batch path;
  ③ **Behavior boundaries**: only declarative partitioning (relkind='p') expands; legacy INHERITS parents (relkind='r') do not (table-by-table semantics preserved); expanded child DDL outputs each child's DDL (consistent with `--tables` batch; the parent `PARTITION BY` DDL is not emitted);
  ④ Regression: data plane (all-type closed loop) + parameter plane (PG 9 combos × 20+ assertions + Kingbase 3 instances × 26-27 assertions) + partition-expansion special test (PG 9 combos + Kingbase 3 instances: multi-leaf parent batch, single-leaf parent single-table, parent+normal-table mix, multi-level partitions, empty partitioned parent) all passed before packaging.
- **v1.0.35**: pre-release code audit fixes (A1/A2 parallel leaks → B1-B3 corrupt-data panics → C1 array "NULL" distortion → D4 silent write errors):
  ① **A1/A2 (concurrency)**: `streamStdParallel`/`streamScanParallel` introduced polling sends (`sendPageResult`/`sendJob` with select default + stop polling + Gosched) — fixed goroutine leaks where the main goroutine returned early on `--limit` while workers/jobs producers blocked forever (batch multi-table + `--limit` per-table accumulation), and eliminated the "check-stop-before-send" race window; `go vet` clean;
  ② **B1-B3 (corrupt-data panics)**: `decodeArray` bitmap-region out-of-bounds (dataoffset/bitmap-length checks, fallback to raw bytes `E'\x...'`), `decodeInet` address-region out-of-bounds (length check, fallback to empty string), `decodeRange` fixed-length boundary crossing the flags byte (aligned with the variable-length branch checks) — skip instead of crashing when field data is truncated/corrupt despite an intact page header;
  ③ **C1 (array "NULL" distortion)**: added `arrayNullSentinel` (`\x00PGSQL_ARRAY_NULL`, safe since PG text never contains `\x00`) to distinguish real NULL from string elements; `arrayQuote` now quotes string `"NULL"` elements normally — `{NULL,"NULL"}` no longer conflates NULL/string after import;
  ④ **D4 (silent write errors)**: `exportOneTable` writes unified through a `write()` closure collecting errors; any failure of DDL/count/SQL/DATA/sequence-sync/final Flush returns `failed to write output file` — no more "export succeeded" with truncated files on full disks;
  ⑤ Regression: data plane (all-type closed loop) + parameter plane (PG 9 combos × 20 assertions + Kingbase 3 instances × 26-27 assertions) + big-table special test (1M-row tablespace table) + mixed-scenario supplemental tests all passed before packaging.
- **v1.0.34**: directory-mode tablespace auto-discovery (features 3/2/1):
  ① **Feature 3**: under `--datadir DIR --db-oid OID` directory mode, `--schema`/`--table-name`/`--tables`/`--all-tables` auto-discover tablespace tables — `classFields` reads `pg_class.reltablespace` (column 9 in PG12/16/18 layout; Kingbase same as PG12), `TableMeta` gained `TableSpace`/`ToastTableSpace`; table-file location: `reltablespace==0` keeps the original `base/<oid>/<rf>` path (default-tablespace behavior unchanged), non-0 resolves `pg_tblspc|<sys_tblspc>/<ts_oid>/<version-dir>/<dboid>/<rf>` (version dir probed by "has <dboid> subdirectory", compatible with same-tablespace multi-version directory reuse); TOAST located by the toast table's own tablespace; dangling symlinks/missing dirs produce explicit errors;
  ② **Feature 2**: `--list-tables-db`/`--list-tables-all` output gained a "tablespace" column (`0`=base, non-0=pg_tblspc/<oid>);
  ③ **Feature 1**: `--datadir DIR --list-db` filepath column displays multi-line (base line + one line per tablespace; `pg_tblspc`/`sys_tblspc` auto-recognized; `os.Stat` follows symlinks);
  ④ Entry validation relaxed: `--datadir+--db-oid` needs no positional argument for single-table (`--table-name`)/batch (`--schema`/`--tables`/`--all-tables`) export;
  ⑤ Regression enhanced: `test_params_pg.sh` assertions grown to **20** (added `tspace_big`/`tsdir_batch`/`listdb_multi`/`tspace_col`: second tablespace + 50K-row Chinese/special-char tablespace big table SQL+CSV import closed loop, directory-mode `--schema` batch incl. tablespace tables, multi-line `--list-db` filepath, `--list-tables-db` tablespace column), `test_params_kb.sh` grown to **26-27**; new `pgbuild/reg_bigts.sh` big-table + tablespace special test (1M rows, SQL/CSV closed loop + MaxRSS sampling);
  ⑥ Tablespace test assets moved from `/tmp` to `pgbuild/tblspc_data{2}` (PG) / `pgbuild/kb_tblspc_data{2}` (Kingbase) to survive `/tmp` cleanup.
- **v1.0.33**: full-feature regression supplement + README feature coverage matrix:
  ① Completed 5 CLI segments previously without assertions (`--deleted`/`--only-deleted`, `--list-db`, `--list-tables-all`, `--all-tables`, `--encoding`) — `test_params_pg.sh` assertions grown to **16**, `test_params_kb.sh` to **21-22**;
  ② Full-version parameter-plane matrix: **PG 12.22/16.15/18.6 × 8K/16K/32K, 9 combos × 16 assertions = 144 items all green**; **Kingbase V8 ORA 8K / V8 PG mode / V9, 3 instances × 21-22 assertions all green** (V8 GB18030 databases marked `SKIP(env)` because ksql cannot safely write UTF8 SQL; GB18030 decoding fully asserted on V9; GBK decoding verified on both V8/V9);
  ③ Test-script fixes: tablespace-segment residual cleanup (CREATE TABLESPACE requires empty dir), Kingbase tablespace version-dir hierarchy pre-creation, label parameterization, `--encoding` segment redesigned per tool semantics (input decode encoding) as "UTF8-db explicit utf8 identical to auto + LATIN1-db (LC_COLLATE 'C') decode closed loop + Kingbase GBK/GB18030 closed loop";
  ④ See the feature coverage matrix below. Zero logic changes (docs and test scripts only; no tool-code changes).
- **v1.0.32**: tablespace path auto-discovery (no `--datadir`/`--db-oid` needed):
  ① When a single-table file path contains the `pg_tblspc/` (Kingbase `sys_tblspc/`) symlink prefix, the data root and database OID are auto-identified from the path and the catalog is read directly — the relative path returned by `pg_relation_filepath` prefixed with the data root works without extra arguments;
  ② Fallback unchanged: dangling symlink / deleted target dir / only holding the real directory path (no prefix) → still use absolute real path + `--datadir` + `--db-oid` explicit location;
  ③ Test-script tablespace segment upgraded to "auto-discovery + fallback" dual assertions: PG16.15 **11/11**, Kingbase V9 **19/19** all green; PG/Kingbase tablespace tables (real dirs, not base/) 100-row SQL/CSV export→import closed loop passed;
  ④ README/USER_GUIDE/`--help` synced with "tablespace auto-discovery and fallback" usage.
- **v1.0.31**: parameter-plane regression completion + doc/package spec updates:
  ① New automated parameter-plane regression scripts `pgbuild/test_params_pg.sh` (PG edition, 10 checks: `--ddl` database-replay closed loop/structure, `--count`, `--fields` CSV import, `--limit`, `--parallel` vs serial md5, `--list-tables-db`, `--schema --tables --ddl` batch, `--export-meta`+`--catalog-json`, bad blocks, tablespace) and `pgbuild/test_params_kb.sh` (Kingbase edition: GB18030/GBK encoding databases SQL+CSV dual-channel closed loop + the above parameter plane + tablespace + bad blocks) — PG16.15 measured **11/11**, Kingbase V9 measured **19/19** all green;
  ② README "Large Table Export: Memory & Time Estimates" gained **v1.0.29 vs v1.0.30 memory/time comparison tables** (memory from O(rows) to O(window×parallelism) constant; 2-core timing par4 SQL -17%, par4 CSV -26%);
  ③ Tablespace-table export note: when the table file is not under `base/` (real directory in a tablespace), single-file mode needs `--datadir` + `--db-oid` for the catalog to auto-discover;
  ④ Execution-package layout standardized to **`pg2sql-<version>-<platform>/` (binary + `docs/` manual dir)**, synced with README/USER_GUIDE.
- **v1.0.30**: plan B shipped — large-table export changed from "collect-all-then-forward" to **O(window) streaming** (eliminating large-table OOM risk):
  ① **Workers complete parse→escape→produce** (`--parallel N` escapes in parallel with parsing; previously escaping ran in a single formatting goroutine, ~90% of large-table time);
  ② **Page-order sliding-window ordered forwarding** (pending map + next pointer, window size O(parallelism), no full-row collection) — memory dropped from O(rows) (50-col 1M rows ≈3.5-4GB, guaranteed OOM on 3GB machines, measured OOM-killing all Kingbase instances) to O(window×parallelism) constant;
  ③ Serial path streamed too (light pre-scan + second-pass streaming output; `probeStandard`/`probeRowValid` only judge NULL/empty/TOAST-missing without building rows or formatting; pre-scan ≈15-25% of full parse cost, page cache hits on second pass); two-phase fallback (standard ItemId / data-area scan) semantics unchanged;
  ④ `--count` now streams (CountRows, no full collection);
  ⑤ **Multi-segment file support (>1GB tables)**: PG 8KB pages RELSEG_SIZE=131072 pages=1GB; >1GB tables auto-split `relfilenode`, `relfilenode.1`… (16KB=2GB/32KB=4GB same rule); since v1.0.30 the main table/TOAST/index-pre-build/pre-scan full chain auto-reads across segments — **previously every version exported only the main segment for >1GB tables (a 1.9GB table once exported only 800K rows)**;
  ⑥ **Parallel page-order window empty-page fix**: workers also send empty results for no-row/corrupt-layout/read-failed pages, keeping the window next pointer advancing page by page (previously empty pages sent nothing → next stuck, subsequent rows all lost).
  Acceptance: PG16 50-col 100K rows (incl. Chinese/special chars/NULL/empty) + 11-row special-char table, `--sql`/`--data`/`--count`/`--limit 1000`/`--fields` serial and `--parallel 4` — 9 output groups byte-identical md5 to v1.0.29; SQL/CSV import closed loop count/min/max all correct; **1.5M-row (1.9GB heap, two segments) special test**: serial SQL 33.3s / par4 SQL 27.8s / par4 CSV 18.4s / count 11.8s, all four channels fully 1500001 rows, serial/parallel md5 identical, SQL import 1.5M rows (202s) / CSV import 1.5M rows (60s) closed loop passed, MaxRSS constant 17MB throughout (old implementation ≈5-6GB at this size, guaranteed OOM); 5/10/20/30/50GB memory-time estimates in README "Large Table Export Memory & Time Estimates".

- **v1.0.29**: Added `docs/USER_GUIDE.md` detailed user manual (parameter details/combination matrix/scenario topics/FAQ); source packages include `docs/`; execution packages embed a `pg2sql-docs/` directory shipped with the binary; README top gained a manual link. Zero logic changes (version/docs only); packaged after full-version regression.
- **v1.0.28**: `--table-name` help and README examples completed as a full comparison (no quotes = comma-separated multiple tables / quoted = complete table name inside quotes): `--table-name a,b` (2 tables), `--table-name '"a,b"'` (1 table, name contains comma), `--table-name '"a,b",c'` (comma-name table + normal table mixed), `--table-name '"a""b"'` (table name containing double quotes, escaped). `--help` synced; zero logic changes.
- **v1.0.27**: `--table-name` supports double-quote-wrapped table names (removing comma/quote identifier ambiguity, consistent with SQL identifier conventions): commas inside quotes are literal characters, not separators; `""` doubled = literal double quote (same rule as PG quote_ident); unclosed quotes produce a precise error; empty names/empty quotes filtered. Shell side must single-quote around double quotes (`--table-name '"a,b",c'`), otherwise the shell eats the double quotes first. Single-table path uses the parsed name (unquoted/trimmed); when `--table-name` is absent the original fallback chain is preserved. `--help` synced. Verified: comma-name table (`"a,b"`) single/multi-table/`--ddl` rebuild import closed loop, quote-name table (`a"b`) escaping, unclosed-quote error, all existing scenarios (single/relfilenode/empty input/unquoted multi-table) compatible; packaged after full-version regression.
- **v1.0.26**: `--table-name` supports comma-separated multiple tables (e.g. `--table-name ray.t_special_test,ray.test02`) — split and located per table, one independent output file per table (reusing batch naming `schema.table.sql|.csv`); multi-table export requires `-o` directory + `--sql` or `--data`; unmatched table names produce precise errors (preventing silent table loss from typos); whitespace between commas auto-trimmed. Single-table behavior completely unchanged (no `-o` → output to current directory). `--help` synced. Verified: multi-table SQL/CSV export→import closed loop (incl. Chinese/English special chars) passed + boundaries (missing -o/missing type/unmatched/empty name) all passed; packaged after full-version regression.
- **v1.0.25**: deep code-audit fixes (5 items, released after full-version regression):
  ① **P-1 [Critical · PG data loss] maxRowNatts re-read high bits of t_infomask2 as column count**: the PG branch `(i2>>11)&0x7FF` mistook HEAP_KEYS_UPDATED/HOT_UPDATED/ONLY_TUPLE/IS_PARTITION/CANT_RECORD flags (0x0800~0x8000) for the in-row column count (read as 1/2/4/8/16), and ghost-column truncation damaged normal tables — measured: PG18 21-col table + equal-length UPDATE (HOT rows infomask2=0x8006) truncated to 16 columns, c16~c20 silently lost. Unified back to low 11 bits (HeapTupleHeaderGetNatts = i2 & 0x07FF, consistent with Kingbase; fixed in v1.0.4, regressed by later refactor). New `pgbuild/test_hot.sh` full-version HOT special regression: PG12-18 × 8/16/32KB, 11 groups, 21-col table + equal-length UPDATE + VACUUM, verifying 21 columns complete + SQL/CSV dual-channel md5 closed loop all green; Kingbase V9 MySQL HOT scenario 21 columns complete, import 0 errors.
  ② **P-2 [Functional] TOAST index pre-build truly parallelized**: BuildIndexParallel's original implementation parsed extractPageChunks serially in a single producer goroutine (workers only did map append), so pre-build parallelism gained ≈0; refactored to a worker pool with each worker doing its own ReadAt+in-page parse (eliminating shared read-buffer reuse-overwrite risk), merged and sorted by valueid+seq. Measured (Kingbase V9 MySQL, 20K rows / 2 cols 7KB ≈300MB TOAST): serial 3.72s → parallel=4 1.54s (2.42x); serial/parallel exported SQL md5 completely identical.
  ③ **P-3 [Low] PageCache true LRU eviction**: CacheOrder queue records access order; hits move to tail, overflow evicts the least-recently-used from head (original map iteration deleted randomly, comment claimed "oldest" but was random).
  ④ **P-4 [Low · theoretical] timestampFromUS overflow fix**: original `time.Duration(sec)*time.Second` caps at ~292 years (timestamps after AD 2262 overflow to wrong values); switched to the Howard Hinnant civil-from-days algorithm (int64 throughout, no overflow) supporting PG's full time range (4713BC~294276AD); normal range around 2000-01-01 byte-identical to old output (unit-tested).
  ⑤ **P-5 [Low · quality] buildDdlStatements indentation cleanup**.
  Also: logf gained a mutex to prevent log interleaving under concurrency.
  Verified: PG12-18 × 8/16/32KB all-type/GAP/HOT/TRUNCATE four classes, 44 groups all green; Kingbase V8 ORA×3 (8/16/32K) + V9 MySQL + V8 PG mode 5 instances all-type/P1/TOAST TRUNCATE 15 items all OK.
- **v1.0.24**: main-table row parsing + TOAST reassembly parallelized (`--parallel N` accelerates both TOAST index pre-build and main-table page-sharded row parsing): worker pool parses page shards + sliding-window ordered merge output, SQL/CSV output row order byte-identical to serial (measured md5 identical); fixed roleMap injection data race during parallel parsing (role mapping injected once before parsing, read-only during parsing); PageCache gained RWMutex protection, TOAST file handles sync.Once initialized, concurrency-safe (race detector all modes pass). Measured (2 cores, 20K rows/333MB TOAST): parallel=2/4 total elapsed -7%~-8% vs serial; multi-core (4-8 cores) per Amdahl estimate 1.5-2.5x speedup.
- **v1.0.23**: README gained "Bad Block (Corrupt Page) Handling" chapter (doc update, logic unchanged): page-by-page tuple-by-tuple protection strategy — corrupt page header skips whole page, bad itemid skips single item, inconsistent tuple header judged bad and skipped, aborted/deleted rows skipped (`--deleted` for audit), bad fields protected as NULL/empty without abort, bad TOAST pointers/pages output NULL/empty for the affected field, batch export outputs succeeded/skipped/failed stats; the finer the bad-block granularity, the less impact, remaining data exported normally.
- **v1.0.22**: completed Kingbase MySQL compatibility-mode specific type decoding (all three Kingbase modes ORA/MySQL/PG measured layout-identical; V8 PG-mode instance added to verification):
  ① **TINYINT** (oid 8100, typinput=tinyintin, 1-byte signed): previously raw bytes; added `tinyintin→decodeInt1` decoding to numeric (`127`/`-128`); unsigned series auto-hit via domain recursion;
  ② **BINARY** (oid 3383, typinput=binaryin): previously raw bytes incl. varlena header (with \x00 padding breaking SQL/CSV import with `invalid byte sequence`); added `binaryin→decodeBinary` (strips varlena header, trims trailing \x00 padding to text; embedded \x00 outputs \x hex, byte-reversible);
  ③ **SET type** (dynamic oid, typtype=y): on-disk 16-byte member bitmask ([10:14] int32), member names not in catalog (neither pg_enum nor typtypmod); exported as \x hex (byte-level auditable); measured: Kingbase set_in accepts `'x,y'` text but cannot restore \x input byte-wise (mask zeroed) — recorded in README limitations;
  ④ **decodeDefault fallback hardening**: utf8.Valid permits \x00; unknown types containing \x00 previously output as text breaking SQL/CSV import; now any \x00 outputs \x hex (byte-reversible).
  Verified: Kingbase V9R3C18 MySQL-mode t_my_types specific-type table (TINYINT/MEDIUMINT/DATETIME/YEAR/BLOB/TEXT/ENUM/SET/FLOAT/DOUBLE/DECIMAL/BINARY, 17 cols 3 rows, incl. Chinese/English/special chars/extremes/empty/NULL) except SET, 16 columns export→import EXCEPT 0 differences; Kingbase V8R6C9B14 ORA-mode t_ora_types table (NUMBER/VARCHAR2/NVARCHAR2/BYTEA(RAW)/CLOB/BLOB/DATE/TIMESTAMP/TIMESTAMPTZ/LONG, 13 cols 3 rows) SQL/CSV dual-channel md5 identical; Kingbase V8 PG compatibility mode (`--dbmode=pg` initdb instance) all-type partitioned table/P1/TRUNCATE special tests all green; Kingbase V8×3 (ORA 8/16/32KB)+V9 (MySQL)+PG mode all-type SQL/CSV dual-channel EXCEPT 0; PG12-18 × 8/16/32KB all-type/P1/GAP/TRUNCATE 11 groups all green.
- **v1.0.21**: fixed TOAST out-of-line read-path defect (found by bad-block measurement, P1 level):
  ① **TOAST path uses relfilenode, not OID**: `reltoastrelid` stores the toast table **OID**, but on-disk files are named by **relfilenode**. After TRUNCATE/rebuild the two diverge (measured: reltoastrelid=24619, toast data file=27622); the original implementation used the OID to build the path pointing at the old/empty file → `TOAST index pre-build 0 valueids` → **out-of-line fields (large fields/TOAST values) silently exported all NULL with no error**. Auto-discovery now builds an `OID→relfilenode` map (`ToastRelFile` field) and prefers relfilenode; `--catalog-json` old files fall back to OID when the field is absent (backward compatible). Verified: 3000-row large-field table after TRUNCATE — before fix 3000 rows all NULL → after fix 3000 rows 0 NULL.
  ② **Auto-discovery failure hint enhanced**: when the target data file exists (numeric relfilenode name) but no matching table was resolved, the error appends "table attributes may be missing due to pg_attribute read failure (corrupt file / not yet flushed)" (previously only a generic not-CHECKPOINTed hint).
  Also: bad-block robustness conclusions — main-table corrupt page header skips the page, corrupt itemid skips the row, all-zero holes skip pages, truncated files cut tails; corrupt/truncated TOAST pages degrade the affected field to NULL, rows preserved, program never crashes; corrupt pg_attribute → auto-discovery errors explicitly, exit 1.
- **v1.0.20**: fixed 3 P1-level unpack/boundary defects with full-version regression (Kingbase V8/V9 + PG12-18 × 8/16/32KB all-type, 50-col special chars, gap types, P1 special matrix 40+ groups all green):
  ① **4BC in-row compression mask** (varlenaParse): little-endian disk `VARATT_IS_4B_C = (header & 0x03) == 0x02`, `SET_VARSIZE_4B_C = (len<<2)|0x02`; the original used `(first & 0x06) == 0x06`, equivalent only when the compressed total length is odd; even lengths judged 4BC as uncompressed 4B → compressed streams (incl. NUL control bytes) output as text garbage → SQL/CSV import errors. Changed to official `(first&0x03)==0x02`.
  ② **varPayload out-of-bounds protection** (types.go): 4BC branch `comp := b[4:total]` added `total>len(b)` check + `len>=5` for compressed streams; method dispatch by toastDecompress on stream tcinfo high 2 bits (PGLZ=0/LZ4=1).
  ③ **pglzDecompress byte shift overflow** (deepest root cause): `off := int((b1&0xF0)<<4)|int(b2)` — b1 is uint8, `(b1&0xF0)<<4` shifts in the uint8 domain and truncates (off high 4 bits lost, max 255); matches at off≥256 copied from wrong positions causing md5 mismatch (reproduced on Kingbase V8 c_extcomp; PG never triggered — pure data coincidence). Fixed to `int(b1&0xF0)<<4 | int(b2)` (promote to int before shifting).
  Also: PG P1 regression script fixed to dynamically obtain the postgres database OID (PG12-14's postgres OID starts at 12975, not 5; hardcoding caused export path errors).
- **v1.0.19**: fixed txid_snapshot empty-xip export missing trailing colon (PG `txid_snapshot_out` always outputs `xmin:xmax:`; nxip=0 previously output `1:100`, breaking SQL/CSV import with `invalid input syntax for type pg_snapshot`; now the trailing colon is always kept — `1:100:` imports fine). Added full-data-type test script `pgbuild/t_alltypes_100.sql` (56 columns covering numeric/char/binary/bool/bit/date-time/JSON/XML/UUID/array/network/geometric/fulltext/range/system/enum/composite + primary key + 9 indexes + 100 rows of Chinese/English special-char data).

- **v1.0.18**: completed PG/Kingbase built-in type decoding gaps — range family (int4range/int8range/numrange/daterange/tsrange/tstzrange, incl. empty and half-open `(,10)`/`[5,)`, per rangetypes disk format: strip 4B range's own oid header + lower/upper fixed-length aligned by attalign, variable-length parsed as complete varlena per boundary + 1B flags), `pg_lsn` (8B little-endian, text X/Y uppercase hex), `txid_snapshot` ([nxip][xmin][xmax][xip] layout, text `100:200:110,140`), `reg*` family (output oid number, reversible import), `tsquery` (QueryItem 12B/packed + operands `\0`-terminated; NOT/AND/OR/PHRASE precedence exactly matching PG infix — measured `'fat' & ( 'rat' | 'cat' )` char-identical), `macaddr8`, `path`/`circle` geometric, multi-dimensional arrays `{{1,2},{3,4}}` and empty arrays `{}`, enum values (dynamically fetched enum members by typrelid). Verified: PG18 gap instance t_gap table (18 cols 3 rows, incl. full range family/lsn/txid/tsquery/regclass/path/circle/macaddr8/enum/2-D array/empty array) exported values column-identical to PG actual values; except composite, 17 columns export→import closed loop (TRUNCATE then `\i` import count=3, spot-checked values identical). **Composite type (user-defined record, oid 16505 etc.) limitation**: on-disk record layout is affected by heap_fill_tuple short-varlena conversion and attalign alignment, varies widely across versions; currently outputs raw bytes `E'\x...'` (byte-reversible, no data corruption; can be manually re-injected or handled by online pg_dump); continued in future versions.
- **v1.0.17**: fixed Kingbase V9 mysql-mode BIT type (oid 4655, typinput=mysql_bit_in) exporting raw varlena bytes, breaking SQL/CSV import — reverse-engineered mysql_bit disk format from measurement (varlena content = 4B little-endian A + 4B little-endian B + ceil(N/8) bytes big-endian data; A=(N-1)-bitpos, B=bitpos+1, N=A+B is the stored bit width, data = original value << (8*nbytes-B); all-zero → A=B=0); added decodeMysqlBit (outputs bit string) and bitBinToHex (0x uppercase hex); confirmed Kingbase mysql-mode import requirements: SQL must use `B'...'` bit literals (plain strings go through varchar_bit cast and fail with "bit string length exceeds 64"), CSV COPY must use 0x-prefixed hex (pure 0/1 text misparses — measured 0x8001→0xC4C4). Also fixed xml-type export leaving a varlena header residue (Kingbase mysql-mode xml is plain-text varlena — header bytes like 0x2b='+' were output as text; PG standard xml content has a 4B type marker); added decodeXml (compatible with both Kingbase plain text and PG 4B marker). Verified: Kingbase V9R3C18 user-created t_fulltype_part all-type partitioned table (36 cols incl. BIT(16)/VARBIT/XML/JSONB/geometric/Chinese/special chars, 3 partitions 10 rows) SQL and CSV dual-channel export→import whole-row md5 identical; BIT(8)/BIT(10)/BIT(16) boundaries (0x00/0x01/0x80/0xFF/0x02AA etc.) dual-channel import values identical; Kingbase V8 bit(1560)/xml table + 31-col all-type table dual-channel md5 identical; PG12-18 × 8/16/32KB full-version all-type regression 10/10 all green (incl. PG standard bit/xml).
- **v1.0.16**: added "catalog dynamic type discovery" (plan C) — on export, reads `pg_type/sys_type` (1247) and `pg_proc/sys_proc` (1255) from the database catalog, mapping decoder semantic families by `typinput` function name (`mysql_timestamp_in→UTC-seconds decoding`, `mysql_datetime_in→local microseconds`, `ora_date_in→timestamp format`, `jsonb_in→JSONB`, `textin→text`, etc.); `typtype='d'` domain types recurse via `typbasetype`; array types auto-register via `typelem+typarray` self-reference; hardcoded decoders table takes priority (fallback), function unaffected when catalog missing. Solves "instance-specific oids / future-version new types can't be pre-hardcoded" (e.g. V9R3C18 instance's 4802 mysql_json had no hardcoded mapping — now dynamically hit by decodeJSONB; 12636 is reused as toast row type in V9R3, no longer relying on hardcoded assumptions). pg_type layout version-aware: PG18+ inserts typsubscript(4B) after typrelid; PG12-17 and Kingbase V8/V9 share the source layout (measured). Verified: Kingbase V9R3C18 real instance 42 dynamic mappings (incl. 4802 mysql_json dynamic decoding, SQL+CSV dual-channel import EXCEPT 0 differences); Kingbase V8 28 dynamic mappings + 31-col all-type table dual-channel 0 differences; PG12-18 × 8/16/32KB full-version regression 10/10 all green.
- **v1.0.15**: fixed 3 Kingbase V9 mysql-mode type-decoding bugs (found by measured V9R3C18 real-instance 30-col all-type table): ① `7954 timestamp` (mysql_timestamp_in stores int64 microseconds since 2000-01-01 UTC; output converted back by session timezone UTC+8) previously unmapped → raw bytes; added decodeMysqlTimestamp; ② `7950 time` (MySQL TIME semantics, 24:00:00 valid upper bound) previously reused decodeTime which modulo'd 86400 seconds into 00:00:00 (id=3's 23:59:59.999999 rounded up to 24:00:00 wrongly normalized); added decodeMysqlTime without modulo; ③ `7024 json` (domain of 4802 mysql_json, binary jsonb_in/jsonb_out storage) previously unmapped → raw varlena bytes; mapped to decodeJSONB. Verified: Kingbase V9R3C18 x86 real instance 30-col all-type table (incl. 7944 date/7952 datetime/7954 timestamp/7950 time/8020 ora_date/4189 datetime/clob/money/json/jsonb/arrays/Chinese/special chars/NULL/24:00:00 boundary) SQL and CSV dual-channel export→import EXCEPT 0 differences both ways; Kingbase V8 31-col all-type table dual-channel 0 differences; PG12-18 × 8/16/32KB full-version regression 10/10 all green.
- **v1.0.14**: fixed Kingbase CLOB (oid 8014) export leaving a varlena header residue (`#clobChineseContent`/`\x17clobEnd`) — Kingbase clob uses standard textin/textout varlena storage (measured header bytes 0x05/0x07/0x0d/0x0f exactly matching content lengths 1/2/5/6 via total=header>>1); added 8014→decodeText; fixed money (oid 790) SQL export wrongly quoted (`'12.34'`) — 790 added to noQuoteOIDs numeric set. Verified: Kingbase V8 online 31-col all-type table (int2/int4/int8/oid/numeric/money/float4/float8/varchar/char/text/name/clob/bool/bytea/json/jsonb/uuid/date/datetime/ora_date/time/timetz/timestamp/timestamptz/interval/int[]/text[]/varchar[], incl. Chinese/special chars/NULL/boundary times/leap years) SQL and CSV dual-channel export→import EXCEPT 0 differences both ways; PG12-18 × 8/16/32KB full-version regression.
- **v1.0.13**: fixed Kingbase V9R1C10 partitioned-table DATE column exporting raw bytes — V9 added `7944 date` (4-byte day count, same format as PG date) unmapped; completed Kingbase time-type decoding: `7944 date`, `7952 datetime` (V9 base type), `4189 datetime` (domain of timestamp), `12636 ora_date` (domain of 8020); fixed two bugs exposed by PG all-type export: `timestampFromUS` wrong formatting for pre-2000 times (negative microseconds, output `00:00:00.-01`), negative microseconds normalized to positive; `decodeTimetz` zero offset `+00:00` wrongly output `-00:00`; added `tsvector` (oid 3614) decoder — per PG ts_type.h disk format (WordEntry bit-packing haspos/len/pos, word strings contiguous, 2-byte alignment padding and WordEntryPos arrays, weights 3→A/2→B/1→C/0 not shown), text output identical to tsvectorout (incl. weight letters). Verified: PG16 all-type table (28 cols incl. tsvector/times/arrays/json/Chinese special chars/pre-2000 times) SQL/CSV export→import EXCEPT 0 differences both ways; Kingbase V8 online datetime/date export correct after fix; PG12-18 × 8/16/32KB full-version regression.
- **v1.0.12**: `--schema NAME` usable standalone (equivalent to `--tables --schema NAME`, no need to write `--tables`), batch-exporting all user tables of a schema; batch validation error messages unified across `--schema/--tables/--all-tables` scenarios.
- **v1.0.11**: added batch export `--tables` (user tables) / `--all-tables` (all tables incl. system objects) and `--schema NAME` schema filtering; multi-table export requires `-o` output directory and `--sql` or `--data`; without `--ddl` only table data, with `--ddl` also outputs CREATE TABLE (one file per table, named schema.table.sql|.csv|.ddl).
- **v1.0.10**: added `--list-tables-all` (lists all objects in the database, incl. system objects); `--list-tables-db` changed to default user objects only (reuses isSystemSchema filtering: pg_catalog/pg_toast/information_schema and Kingbase sys_catalog/sysaudit/sysmac/kdb_schedule/anon/src_restrict/sys/SYS_HM*/sys_*).
- **v1.0.9**: cleaned redundant options (removed `--list-tables`, `--replace`); fixed TOAST parallel index build data race (per-worker independent maps + single-goroutine merge, race detector measured no DATA RACE); full DDL support — CREATE SEQUENCE, ALTER COLUMN SET DEFAULT nextval, indexes/primary keys (USING btree), COMMENT ON TABLE/COLUMN, SQL file appends SELECT setval to sync auto-increment sequences (solves primary-key conflicts when DDL and data import separately leaves auto-increment behind); pure-DDL mode independent `.ddl` suffix; fixed 16/32KB large-page sequence default parsing (nodeToString signed-byte regex `-?\d+` + uint32 wraparound); rolled back isLive's XMIN_COMMITTED check (PG disk-row commit bits lazy-update caused full-version regression failures; Kingbase V8 aborted rows captured correctly by isInsertAborted). Verified: PG12-18 × 8/16/32KB full-version regression 10/10 all green (imp_sql=0/0 imp_csv=0/0 rows_csv=100000 ddl_pk=1) + Kingbase V8 8KB count=100000, V9 SQL/CSV import closed loop 0 errors (INSERT auto-increment 100001).
- **v1.0.8**: full GB18030 decoding — complemented GB18030-specific code points in the double-byte region (private area/extended tables); added 4-byte sequence decoding (low zone N<39420 looks up a 206-segment mapping table covering U+0080-U+FFFF code points without double-byte encodings; high zone N∈[189000,1237575] linearly maps U+10000-U+10FFFF; middle is a reserved illegal zone), verified by 500 random comparisons with the Python official gb18030 codec + Kingbase GB18030 instance measurement (Chinese/special chars export correct, import closed loop); PG server does not support GB18030 encoding (measured confirmed), this scenario targets Kingbase.
- **v1.0.7**: fixed PG15+ primary-key DDL loss — PG15+ inserts indnullsnotdistinct after indisunique in pg_index, moving indisprimary offset from @13 to @14 (PG15-18 auto-detected by version; PG12-14/Kingbase V9 keep @13; Kingbase V8 (PG10 kernel) @11); cleaned all debug output.
- **v1.0.6**: DDL outputs PRIMARY KEY (auto-discovery parses pg_index/sys_index: indisprimary layout adaptive — PG15+ @14 due to indnullsnotdistinct insertion, PG11-14/Kingbase V9 @13, PG10- kernel @11; indkey parsed per int2vector disk format correctly (vl_len_+ndim+dataoffset+elemtype+dim1+lbound1+values, matching ArrayType — previously read directly as int16, wrong), plus data-area varlena location method auto-compatible with indkey offsets (PG12-18 @24 / Kingbase V9 appended columns @26 / Kingbase V8 @20); removed dead params `--toast-cache` (full unimplemented) and `--force` (no implementation); partitioned-table per-partition export measured OK (`--list-tables-db` shows relkind='p' parents and 'r' leaf partitions; leaves exported independently by relfilenode, SQL/CSV import verified 0 loss; parent relfilenode=0 has no exportable data; DDL output without PARTITION BY — import requires manual partition-structure rebuild).
- **v1.0.5**: added runtime hints — when the TOAST file is non-empty but the index has 0 valueids (database instance online and recent writes not CHECKPOINTed, disk heap/TOAST pages not fully flushed, e.g. the user-measured 57049-row scenario) warns; when the target table is not found, hints possible missing CHECKPOINT (disk sys_class vs data-file relfilenode mismatch; suggests CHECKPOINT or --table-name). Logic-verified identical to the Python version: natts low-bit read (t_infomask2 & 0x07FF), TOAST failure placeholder (`__TOAST_MISSING__` → NULL), page-size auto-detection, CLI 32 params fully aligned; Kingbase V8 online/V9 replica full-feature regression + PG12-18 full-version × 8/16/32KB all 21 items: 50-col 100K-row Chinese random data export→import (SQL and CSV dual-channel) all count=100000, EXCEPT 0/0 both ways.
- **v1.0.4**: fixed Kingbase V9 ghost columns — residual att rows after ALTER with attnum exceeding the data row's actual column count exported 55 garbage columns (CSV column count mismatching table definition, COPY "extra data after last expected column"), row parsing misaligned and dropped rows (100000 rows exported only 90189). Fix: pre-scan data-file row headers before export, truncate column set by max in-row column count (maxRowNatts); in-row column count uniformly read by t_infomask2 low 11 bits (PG source HeapTupleHeaderGetNatts = t_infomask2 & 0x07FF, measured V8/V9/PG12-18 identical; revoked the erroneous PG high-bit branch); unified IsKB injection (incl. --catalog-json path); fixed Kingbase int16 (4659) 16-byte integer 64-bit left-shift overflow (full representation via big.Int). Verified: Kingbase V8 online 50-col 100K-row Chinese/English random data SQL/CSV export→import 0 missing 0 extra; PG12-18 × 8/16/32KB full-version regression.
- **v1.0.3**: fixed Kingbase ALTER DROP COLUMN residual multi-generation sys_attribute rows causing duplicated/garbage columns (dedupe by attrelid+attnum, prefer non-dropped, same-state latest transaction); auto-discovery and --list-tables-db column-count stats exclude dropped columns; full-version (Kingbase V8R6C8B14/B20/C9B14/V9R1C10 + PG12-18 × 8/16/32KB) regression passed.
- **v1.0.2**: fixed output-file path resolution when `-o` specifies a directory.
- **v1.0.1**: `--list-tables-db` aligned output.
- **v1.0.0**: Go rewrite first release, verified across the full-version matrix.

MIT License. Author: raysuen




