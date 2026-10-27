# pg2sql 双版本构建与打包指南（v1.0.45 起，当前 v1.0.53）

本文档说明 pg2sql **免费版**与 **TDE 付费版**的源码布局、构建方法、打包规范与交付前验证清单，适用于从源码包自行构建/打包的场景。

> **版本基线**：本文档的示例命令统一用 `VER=1.0.53`；实际版本以 `main.go` 的 `progVersion` 为准（打包脚本 `release.sh` 会自动从源码里读取，不需要手工维护版本号）。
>
> **文件清单维护约定**：`COMMON_GO` / `TEST_GO` 两份清单**只存在于 `release.sh`**（第 11–13 行），本文档 3.3 节不再复制。新增 `.go` 文件只需改 `release.sh` 一处，然后跑 3.3 节的 `bash release.sh` 即可；打包前建议先跑第 6 节自检，确认清单与磁盘一致。

## 1. 双版本与源码布局

同一套源码通过 **Go 编译期 build tag**（`-tags tde`）门控，产出两个二进制系列：

| 版本 | 构建命令 | `--version` 输出 | 二进制内 TDE 代码 |
|---|---|---|---|
| 免费版 | `go build .` | `pg2sql 1.0.53` | **编译期剔除**（strings 0 命中，无法提取破解） |
| TDE 版 | `go build -tags tde .` | `pg2sql 1.0.53 (TDE)` | 含 SM4-ECB/SM3 国密实现 |

### 源码文件职责

```
pg2sql-go/
├── main.go              # 主程序（progVersion=1.0.53；TDE 参数经 parsePaidArg/paidHelpText/paidVersionSuffix 接入）
├── heapfile.go          # 页读取主链路（通过 pageDecryptor 接口解耦解密器，与具体 TDE 实现无关）
├── binary.go / catalog.go / types.go / gb18030_ext.go / gbk_tab.go   # 解析/解码（两版共用）
│
│   ── 以下为 v1.0.46~53 陆续新增，均为两版共用的普通源码（无 build tag）──
├── schema_ddl.go        # 完整 Schema DDL 导出（--full-schema / --schema-objects，v1.0.40 起，913 行）
├── large_object.go      # 大对象导出（--large-objects / --large-object-oids，v1.0.40 起，387 行）
├── nodetree.go          # pg_node_tree 通用反解层（v1.0.51，797 行）——列默认值/表达式索引/部分索引/域 CHECK 都靠它
├── domain_check.go      # 域 CHECK 约束还原（v1.0.50，96 行；通用反解已拆至 nodetree.go）
├── partition.go         # 声明式分区 DDL 还原（v1.0.52，332 行；父 PARTITION BY + 子 PARTITION OF）
├── integrity.go         # 完整性自证：report.json 的 db_state/rows_*/pages_*/skipped/unknown_types/verdict（v1.0.53，639 行）
├── roles.go             # 角色离线导出（--roles / --roles-nopassword，v1.0.53，512 行；读 global/pg_authid）
│
├── paid_stub.go         # //go:build !tde —— 免费版：TDE 参数报错、解密器为 nil、help 无 TDE 项（曾用名 tde_free.go）
├── paid_impl.go         # //go:build tde  —— TDE 版：参数解析 + 帮助文本 + 版本后缀 " (TDE)"（曾用名 tde_tde.go）
├── paid_build.go        # //go:build tde  —— TDE 版：buildPaidDecryptor 实现（构造解密器，曾用名 tde_build_tde.go）
├── tde_verify.go        # //go:build tde  —— TDE 版：密钥正确性校验（v1.0.46，123 行；**免费版源码包不含**）
├── tde/
│   ├── sm4.go           # 自研 SM4-ECB 国密实现（不依赖外部库）
│   ├── wallet.go        # 钱包链解析（masterkey.kr / tspkey.kr，KEK→MK→TSPK 离线推导）
│   ├── tde.go           # 解密器：页/TOAST 解密入口（--enckey 表加密、--wallet-dir 表空间加密）
│   └── tde_selfcheck_test.go  # 三向量自测
│
├── 测试文件（两版共用，无 build tag）
│   ├── b5_verify_test.go / int16_verify_test.go / mysqlts_verify_test.go   # 早期类型验证夹具
│   ├── v1048_verify_test.go        # DDL 分离（.ddl 独立文件）验证
│   ├── v1050_domain_check_test.go  # 域 CHECK 还原
│   ├── v1051_nodetree_test.go      # pg_node_tree 反解
│   ├── v1052_partition_test.go     # 分区 DDL 还原
│   └── v1053_integrity_test.go     # 完整性自证 + 角色导出 + 参数守卫
│
├── go.mod / LICENSE / README.md / README.en.md   # README.en.md 为英文版（与 README.md 顶部互链切换语言）
└── docs/
    ├── USER_GUIDE.md    # 用户手册
    └── BUILD.md         # 本文档（仓库内位于 BUILD/BUILD.md，打包时复制为源码包的 BUILD/BUILD.md）
```

> 门控原理：`paid_stub.go` 与 `paid_impl.go`/`paid_build.go`/`tde_verify.go` 的文件级 `//go:build` 互斥；普通 `go build` 只编译免费版文件，`-tags tde` 只编译 TDE 版文件，因此免费版二进制**不包含任何 TDE 代码与字符串**（strings 0 命中为硬性门槛；文件与函数改名即为了消除二进制内 TDE 字符串痕迹）。
>
> 注意 `tde_verify.go` 带 `//go:build tde`，因此它属于"TDE 专属"，免费版源码包里**不复制**它（免费版 `go test ./...` 也不需要它）。

## 2. 构建方法

### 2.1 本机构建（当前平台）

```bash
cd pg2sql-go

# 免费版
go build -o pg2sql .

# TDE 版
go build -tags tde -o pg2sql-tde .
```

### 2.2 交叉编译（Linux 双平台）

```bash
cd pg2sql-go

# 免费版 x86_64 / aarch64
GOOS=linux GOARCH=amd64 go build -o pg2sql-linux-x86_64 .
GOOS=linux GOARCH=arm64 go build -o pg2sql-linux-aarch64 .

# TDE 版 x86_64 / aarch64
GOOS=linux GOARCH=amd64 go build -tags tde -o pg2sql-tde-linux-x86_64 .
GOOS=linux GOARCH=arm64 go build -tags tde -o pg2sql-tde-linux-aarch64 .
```

> 本项目为纯 Go、无 cgo，交叉编译产物为静态链接单文件，可直接拷到目标机运行。

### 2.3 构建验证

```bash
# 版本与帮助
./pg2sql --version          # pg2sql 1.0.53（无 TDE 后缀）
./pg2sql-tde --version      # pg2sql 1.0.53 (TDE)

# 免费版不应出现 TDE 参数
./pg2sql --enckey x         # 应报错：--enckey 为 TDE 付费版功能...

# 二进制特征检查（免费版应为 0，TDE 版应 >0）
strings ./pg2sql     | grep -c 'SM4-ECB\|masterkey'
strings ./pg2sql-tde | grep -c 'SM4-ECB\|masterkey'
```

## 3. 打包规范

### 3.1 目录规范（两版一致）

```
pg2sql-1.0.53-linux_x86_64/        # 免费版执行包（仅面向使用者：二进制 + 用户手册）
├── pg2sql                          # 二进制
└── docs/
    └── USER_GUIDE.md               # 用户手册（无 BUILD 内容）

pg2sql-1.0.53-tde-linux_x86_64/    # TDE 版执行包（目录名加 -tde，同样仅 USER_GUIDE.md）
├── pg2sql
└── docs/
    └── USER_GUIDE.md

pg2sql-1.0.53-src/                 # 免费版源码包（面向构建/打包者）
├── <源码文件...>                   # 14 个公共 .go + paid_stub.go + 8 个测试文件
├── docs/
│   └── USER_GUIDE.md
└── BUILD/
    └── BUILD.md                   # 本文档：双版本构建与打包指南（独立 BUILD 文件夹）

pg2sql-1.0.53-tde-src/             # TDE 版源码包
├── <源码文件...>                   # 公共部分 + 3 个门控文件 + tde_verify.go（含 tde/ 目录）
├── docs/
│   └── USER_GUIDE.md
└── BUILD/
    └── BUILD.md
```

### 3.2 源码包差异（重点）

| 内容 | 免费版源码包 | TDE 版源码包 |
|---|---|---|
| `COMMON_GO`：main / binary / catalog / heapfile / types / gb18030_ext / gbk_tab / schema_ddl / large_object / domain_check / nodetree / partition / integrity / roles（14 个） | ✅ | ✅ |
| `TEST_GO`：b5_verify / int16_verify / mysqlts_verify / v1048_verify / v1050_domain_check / v1051_nodetree / v1052_partition / v1053_integrity（8 个） | ✅ | ✅ |
| `go.mod` / `LICENSE` / `README.md` / `README.en.md` / `docs/` / `BUILD/` | ✅ | ✅ |
| `paid_stub.go` | ✅（`!tde` 门控，无 tde import） | ✅ |
| `paid_impl.go` / `paid_build.go` / `tde_verify.go` | ❌ 剔除（均为 `//go:build tde`） | ✅ |
| `tde/` 目录（sm4.go/wallet.go/tde.go/自测） | ❌ 剔除 | ✅ |
| 构建命令 | `go build .`（直接可编） | `go build -tags tde .` |
| 测试命令 | `go test ./...` | `go test -tags tde ./...` |

> 免费版剔除的是**带 `//go:build tde` 的文件**，不是"所有含 tde 字样的文件"——`COMMON_GO` 里的 7 个功能文件（`schema_ddl.go` 起）虽然和 TDE 同期开发，但都是无条件编译的公共源码，两个版本都必须带上，否则 main.go 里的调用会 undefined。

### 3.3 打包命令

**只有一条命令**（在仓库根执行）：

```bash
bash release.sh
```

它会依次完成：从 `main.go` 读 `progVersion` → 交叉编译 4 个二进制 → 组装 6 个目录 → 出 6 个 `tar.gz` → 生成 `SHA256SUMS.txt`，最后打印包清单。

> **文件清单的唯一来源是 `release.sh` 第 11–13 行的 `COMMON_GO` / `TEST_GO`。本文档不再重复列出文件名。**
>
> 原因：v1.0.46~53 期间清单在两处各有一份，`release.sh` 更新了而本文档停在 v1.0.45，照文档手工打包会得到"缺 7 个文件、`go build` 报一堆 undefined"的源码包。重复一份清单就等于多一个漂移点，所以这里改为单点引用。

#### 不使用 `release.sh` 时的手工流程

若需在自己环境复现或改造流程，从 `release.sh` **读取**清单而不是手抄：

```bash
# 必须用 bash（zsh 默认不做单词拆分，cp $COMMON_GO 会报 cp: bad '...'）
set -e
cd <仓库根>
VER=$(grep -o 'progVersion = "[0-9.]*"' pg2sql-*-tde-src/main.go | grep -o '[0-9.]*')
SRC=pg2sql-1.0.45-tde-src          # 仓库内源码目录（沿用历史命名，按实际改）
REL=./release

# ↓ 清单从 release.sh 读取，与打包脚本永远同源
eval "$(grep -E '^(COMMON_GO|TEST_GO)=' release.sh)"

# --- 免费版三件套 ---
mkdir -p /tmp/pkg/pg2sql-$VER-linux_x86_64/docs /tmp/pkg/pg2sql-$VER-linux_aarch64/docs \
         /tmp/pkg/pg2sql-$VER-src/docs /tmp/pkg/pg2sql-$VER-src/BUILD
(cd "$SRC" && GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /tmp/pkg/pg2sql-$VER-linux_x86_64/pg2sql .)
(cd "$SRC" && GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o /tmp/pkg/pg2sql-$VER-linux_aarch64/pg2sql .)
chmod 0755 /tmp/pkg/pg2sql-$VER-linux_*/pg2sql

cp "$SRC/docs/USER_GUIDE.md" /tmp/pkg/pg2sql-$VER-linux_x86_64/docs/
cp "$SRC/docs/USER_GUIDE.md" /tmp/pkg/pg2sql-$VER-linux_aarch64/docs/
cp "$SRC/docs/USER_GUIDE.md" /tmp/pkg/pg2sql-$VER-src/docs/
cp "$SRC/BUILD/BUILD.md"     /tmp/pkg/pg2sql-$VER-src/BUILD/
# 免费源码包：公共 + 测试 + paid_stub.go（不含 paid_impl/paid_build/tde_verify.go、tde/）
(cd "$SRC" && cp $COMMON_GO $TEST_GO paid_stub.go go.mod LICENSE README.md README.en.md /tmp/pkg/pg2sql-$VER-src/)
(cd /tmp/pkg && tar czf "$OLDPWD/$REL/pg2sql_${VER}_src.tar.gz" pg2sql-$VER-src)

# --- TDE 版三件套 ---
mkdir -p /tmp/pkg/pg2sql-$VER-tde-linux_x86_64/docs /tmp/pkg/pg2sql-$VER-tde-linux_aarch64/docs \
         /tmp/pkg/pg2sql-$VER-tde-src/docs /tmp/pkg/pg2sql-$VER-tde-src/BUILD /tmp/pkg/pg2sql-$VER-tde-src/tde
(cd "$SRC" && GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -tags tde -o /tmp/pkg/pg2sql-$VER-tde-linux_x86_64/pg2sql .)
(cd "$SRC" && GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -tags tde -o /tmp/pkg/pg2sql-$VER-tde-linux_aarch64/pg2sql .)
chmod 0755 /tmp/pkg/pg2sql-$VER-tde-linux_*/pg2sql

cp "$SRC/docs/USER_GUIDE.md" /tmp/pkg/pg2sql-$VER-tde-linux_x86_64/docs/
cp "$SRC/docs/USER_GUIDE.md" /tmp/pkg/pg2sql-$VER-tde-linux_aarch64/docs/
cp "$SRC/docs/USER_GUIDE.md" /tmp/pkg/pg2sql-$VER-tde-src/docs/
cp "$SRC/BUILD/BUILD.md"     /tmp/pkg/pg2sql-$VER-tde-src/BUILD/
(cd "$SRC" && cp $COMMON_GO $TEST_GO paid_stub.go paid_impl.go paid_build.go tde_verify.go \
   go.mod LICENSE README.md README.en.md /tmp/pkg/pg2sql-$VER-tde-src/)
(cd "$SRC" && cp tde/sm4.go tde/wallet.go tde/tde.go tde/tde_selfcheck_test.go /tmp/pkg/pg2sql-$VER-tde-src/tde/)

# --- 出包 + 校验和 ---
cd /tmp/pkg
tar czf "$OLDPWD/$REL/pg2sql_${VER}_tde_src.tar.gz"            pg2sql-$VER-tde-src
tar czf "$OLDPWD/$REL/pg2sql_${VER}_tde_linux_x86_64.tar.gz"   pg2sql-$VER-tde-linux_x86_64
tar czf "$OLDPWD/$REL/pg2sql_${VER}_tde_linux_aarch64.tar.gz"  pg2sql-$VER-tde-linux_aarch64
cd "$OLDPWD/$REL" && shasum -a 256 pg2sql_${VER}_*.tar.gz > SHA256SUMS.txt
```

> 旧版 `pgbuild/pkg139.sh` 为历史打包脚本，清单已过期，不要再用。

## 4. 交付前验证清单（打包后逐项执行）

```bash
# 1) 解压结构
#    执行包：pg2sql + docs/USER_GUIDE.md（无 BUILD 内容）
tar tzf release/pg2sql_${VER}_linux_x86_64.tar.gz
tar tzf release/pg2sql_${VER}_tde_linux_x86_64.tar.gz
#    源码包：<源码> + docs/USER_GUIDE.md + BUILD/BUILD.md
tar tzf release/pg2sql_${VER}_src.tar.gz
tar tzf release/pg2sql_${VER}_tde_src.tar.gz

# 2) 双版本行为
./pg2sql    --version    # pg2sql 1.0.53
./pg2sql-tde --version   # pg2sql 1.0.53 (TDE)
./pg2sql --enckey x      # 报错提示 TDE 版
./pg2sql-tde --enckey x  # 正常解析（缺数据文件时报文件错误而非参数错误）

# 3) 二进制特征
strings ./pg2sql     | grep -c 'SM4-ECB\|masterkey'   # = 0
strings ./pg2sql-tde | grep -c 'SM4-ECB\|masterkey'   # > 0

# 4) 源码包可编译性 + 单测（解压后分别构建；这一步能直接暴露"清单漏文件"）
cd pg2sql-1.0.53-src      && go build . && ./pg2sql --version                        # 1.0.53
cd pg2sql-1.0.53-src      && go test ./...                                           # ok
cd pg2sql-1.0.53-tde-src  && go build -tags tde . && ./pg2sql --version              # 1.0.53 (TDE)
cd pg2sql-1.0.53-tde-src  && go test -tags tde ./...                                 # ok

# 5) 常规功能等价（免费 vs TDE 对同一普通表导出应逐字节一致）
#    对同一实例同一表分别用两版导出，diff 应为空
```

## 5. 版本与回归约束

- 任何代码变更：版本号 `progVersion` +0.1（`main.go`）。
- 每个 bug 修复必须**全版本回归通过**才算完成（金仓 V8/V9 各 block size + PG12-18 各 block size）。
- TDE 功能改动需跑加密专项回归：`pgbuild/reg_tde_r1c10.sh` + `reg_tde_v8.sh` + `reg_tde_imp.sh`（明文闭环，14 实例 28/28 全绿为基准）。
- TDE 版单独发布不影响免费版；免费版二进制**不得**残留任何 TDE 可调用代码（strings 检查为硬性门槛）。

## 6. 文件清单自检（新增 .go 后必跑）

源码包漏文件的表现是"包能解开、`go build` 报一堆 undefined"，出问题的人往往已经离开现场。打包前用下面这段比对，**两段输出都必须为空**：

```bash
#!/bin/bash
# 在仓库根（同时存在 release.sh 与 pg2sql-*-tde-src 目录）执行
SRC=./pg2sql-1.0.45-tde-src   # 仓库内源码目录（目录名沿用 1.0.45 历史命名，按实际改）
CGO=$(grep '^COMMON_GO=' release.sh | sed 's/^COMMON_GO="//; s/"$//')
TGO=$(grep '^TEST_GO='   release.sh | sed 's/^TEST_GO="//;   s/"$//')
LISTED=$(printf '%s %s paid_stub.go paid_impl.go paid_build.go tde_verify.go\n' "$CGO" "$TGO" \
         | tr ' ' '\n' | grep -v '^$' | sort -u)
ACTUAL=$(cd "$SRC" && ls *.go | sort -u)

echo "--- A. 清单里有、磁盘上没有（cp 会失败）---"
comm -13 <(echo "$ACTUAL") <(echo "$LISTED")
echo "--- B. 磁盘上有、清单里没有（会被漏打进源码包）---"
comm -23 <(echo "$ACTUAL") <(echo "$LISTED")
```

判定标准：

| 输出 | 含义 | 处理 |
|---|---|---|
| A、B 皆空 | 清单与磁盘一致 | 可打包 |
| A 有内容 | 清单写了不存在的文件 | 从 `COMMON_GO`/`TEST_GO` 删掉该名字 |
| B 有内容 | 新增的 `.go` 没进清单 | 加入 `release.sh` 的 `COMMON_GO`（非测试）或 `TEST_GO`（`*_test.go`）——**只需改这一处** |

> 历史教训：v1.0.46~53 陆续新增了 `schema_ddl.go` / `large_object.go` / `domain_check.go` / `nodetree.go` / `partition.go` / `integrity.go` / `roles.go` 七个文件，`release.sh` 及时更新了，但本文档 3.3 节的 `cp` 清单一直停在 v1.0.45。照旧文档打包会得到"缺 7 个文件"的源码包——v1.0.53 发版核查时发现并修正。清单存在两份就是漂移的根源，因此这里把自检固化成命令。

## 7. 发版流程与文档同步责任

### 7.1 一条命令

```bash
bash release.sh
```

它会先跑 `doc_check.sh`（文档一致性自检），**FAIL 时直接终止打包**，通过后才编译与出包。所以只要习惯用这条命令，下面 7.2 的检查项就不会被漏掉。

### 7.2 自检能自动兜住的（无需人工记忆）

| 检查项 | 判定 | 失败时说明 |
|---|---|---|
| 三份文档标题版本 = `progVersion` | FAIL | README / 手册 / BUILD.md 忘了改版本号 |
| README、手册含当前版本条目 | FAIL | 忘了写更新记录 |
| **英文 README 与中文 README 章节数一致** | FAIL | 新增中文章节没翻译（v1.0.53 前英文版长期停在 10 章，标题却写新版本） |
| 英文 README 二级标题无中文残留 | FAIL | 只改了标题没翻译正文 |
| 文件清单与磁盘一致 | FAIL | 新增 `.go` 没进 `release.sh`（会打出编不过的源码包） |
| `main.go` 里每个参数都在手册中出现 | WARN | 新增/改名参数没写进参数速查表 |
| 手册一级章节编号无重复 | FAIL | 插入章节时撞号（历史上出现过两个 `## 7`） |

### 7.3 自检兜不住的（必须人工判断）

自动检查只能发现"版本号/清单/参数名"这类**结构性**问题，**发现不了"功能写了但没解释清楚"**。以下三种改动必须人工过一遍：

| 改动类型 | 必须动的文档 | 具体位置 |
|---|---|---|
| **新增功能/参数** | 手册 + README | 手册：参数速查表 + 4.x 详解 + 必要时 6.x 场景；README：能力矩阵对应档位 + 更新记录 |
| **行为变更（尤其影响脚本）** | 手册"注意"段 + README 更新记录 | 例如 v1.0.53 的"`--schema` 未匹配改为硬报错"——退出码语义变了，不看文档的自动化脚本会踩 |
| **新增限制/不支持项** | README 能力矩阵"不支持"档 + 手册第 9 章限制 | 说清楚"按设计不导"还是"暂时不支持"，两者对客户预期完全不同 |

### 7.4 文档分工（写到哪儿）

| 文件 | 定位 | 读者 |
|---|---|---|
| `README.md` | 对外门面：能力矩阵、与 `pg_dump` 的差异、更新记录 | 评估要不要用的人 |
| `docs/USER_GUIDE.md` | 完整手册：参数、场景、FAQ、限制 | 已经要用的人 |
| `BUILD/BUILD.md` | 构建与打包：源码布局、打包命令、交付验证 | 要自己构建/打包的人 |
| `README.en.md` | 英文摘要（当前滞后于中文版，仅同步版本号/能力矩阵指针/更新记录） | 海外读者 |

> 落盘原则：**一个事实只写一处，其它地方指过去**。例如 `report.json` 字段的判读只在 README 写全，手册 4.7 给摘要并链接过去；文件清单只在 `release.sh` 写全，BUILD.md 指过去。重复内容迟早漂移——v1.0.53 的两个文档缺陷（BUILD.md 清单过期、手册缺 `--schema` 报错说明）都是重复与遗漏造成的。
