# pg2sql 双版本构建与打包指南（当前 v1.0.58）

本文档说明 pg2sql **免费版**与 **TDE 付费版**的源码布局、构建方法、打包规范与交付前验证清单，适用于从源码包自行构建/打包的场景。

> **版本基线**：本文档的示例命令统一用 `VER=1.0.58`；实际版本以 `internal/cli/version.go` 的 `Version` 为准（打包脚本 `release.sh` 会自动从源码里读取，不需要手工维护版本号）。
>
> **v1.0.57 目录重构**：源码不再是"全部平铺在 `package main`"，而是 `cmd/` + `internal/` 多包结构。因此源码包改为**整树复制 + 按包排除**，不再维护 `COMMON_GO` / `TEST_GO` 逐文件清单——新增 `.go` 只要放进对应的包目录即可，不会再有"清单漏文件"的漂移点。

## 1. 双版本与源码布局

同一套源码通过 **Go 编译期 build tag**（`-tags tde`）门控，产出两个二进制系列：

| 版本 | 构建命令 | `--version` 输出 | 二进制内 TDE 代码 |
|---|---|---|---|
| 免费版 | `go build ./cmd/pg2sql` | `pg2sql 1.0.58` | **编译期剔除**（strings 0 命中，无法提取破解） |
| TDE 版 | `go build -tags tde ./cmd/pg2sql` | `pg2sql 1.0.58 (TDE)` | 含 SM4-ECB/SM3 国密实现 |

### 源码文件职责

```
pg2sql/
├── cmd/pg2sql/main.go           # 唯一入口：os.Exit(app.Run(os.Args[1:]))，不含业务逻辑
├── internal/
│   ├── cli/                     # 参数解析（args.go）、帮助文本（help.go）、版本号（version.go）
│   ├── app/                     # 业务编排（原 main.go 主体）：
│   │                            #   run.go 主流程 / export.go 单表导出 / batch.go 批量与并发
│   │                            #   report.go report.json 落盘 / script.go 导入脚本
│   │                            #   encoding.go 库级+表级编码 / paths.go 输出与数据文件定位
│   │                            #   list.go --list-db/--list-tables / ddlgen.go CREATE TABLE
│   │                            #   progress.go 进度行 / schema.go 完整 Schema DDL / objects.go 角色+大对象入口
│   ├── config/                  # Options 结构体、表空间与数据根解析
│   ├── util/                    # 标识符转义、文件大小、时区偏移、密钥掩码
│   ├── logx/                    # 日志输出与 logx.ErrorExit
│   ├── charset/                 # 编码探测（UTF8/GBK/GB18030/BIG5 投票）与解码
│   ├── datatype/                # 全类型解码器（数组/枚举/复合/域/几何/时间/JSON…）+ TOAST 解压
│   ├── engine/                  # 页与元组读取主链路（heapfile.go / sys.go / named.go），页解密经接口解耦
│   ├── nt/                      # pg_node_tree 反解（nodetree.go）+ 分区边界（partbound.go）
│   ├── catalog/                 # 系统目录扫描：pg_class/pg_type/pg_attribute/分区元数据/meta.json
│   ├── ddl/                     # DDL 生成：完整 Schema（schema_ddl.go）、分区（partition.go）、域 CHECK（domain_check.go）
│   ├── objects/                 # 角色导出（roles.go，读 global/pg_authid）、大对象导出（large_object.go）
│   ├── integrity/               # 完整性自证：pg_control 状态、rows/pages 对照、跳过期对象、类型覆盖、verdict
│   └── paid/                    # 付费门控：paid_stub.go（!tde）/ paid_impl.go + paid_build.go + tde_verify.go（tde）
├── tde/                         # TDE 国密实现：sm4.go（SM4-ECB）/ tde.go（解密入口）/ wallet.go（钱包链）/ tde_selfcheck_test.go
├── docs/USER_GUIDE.md           # 用户手册
├── BUILD/BUILD.md               # 本文档
└── go.mod / LICENSE / README.md / README.en.md
```

**包依赖方向（单向，无环）**：

```
cmd/pg2sql → internal/app → { ddl, objects, catalog, integrity, cli }
                          → engine → { datatype, charset, nt }
                          → config / util / logx
```

`datatype` 需要目录信息（自定义类型/枚举/复合类型）但又被 `catalog` 调用，为打破循环，改为**回调注入**：`datatype.DynamicScanner` / `PostScanHook` 由 `internal/app` 的 `init()` 注入 `catalog.ScanDynamic` 与 `ddl.BuildDomainChecks` 的实现。

> 门控原理：`internal/paid/paid_stub.go` 与 `paid_impl.go`/`paid_build.go`/`tde_verify.go` 的文件级 `//go:build` 互斥；普通 `go build` 只编译免费版文件，`-tags tde` 只编译 TDE 版文件，因此免费版二进制**不包含任何 TDE 代码与字符串**（strings 0 命中为硬性门槛）。

## 2. 构建方法

### 2.1 本机构建（当前平台）

```bash
cd pg2sql

# 免费版
go build -o pg2sql ./cmd/pg2sql

# TDE 版
go build -tags tde -o pg2sql-tde ./cmd/pg2sql
```

### 2.2 交叉编译（Linux 双平台）

```bash
cd pg2sql

# 免费版 x86_64 / aarch64
GOOS=linux GOARCH=amd64 go build -o pg2sql-linux-x86_64 ./cmd/pg2sql
GOOS=linux GOARCH=arm64 go build -o pg2sql-linux-aarch64 ./cmd/pg2sql

# TDE 版 x86_64 / aarch64
GOOS=linux GOARCH=amd64 go build -tags tde -o pg2sql-tde-linux-x86_64 ./cmd/pg2sql
GOOS=linux GOARCH=arm64 go build -tags tde -o pg2sql-tde-linux-aarch64 ./cmd/pg2sql
```

> 本项目为纯 Go、无 cgo，交叉编译产物为静态链接单文件，可直接拷到目标机运行。

### 2.3 构建验证

```bash
# 版本与帮助
./pg2sql --version          # pg2sql 1.0.58（无 TDE 后缀）
./pg2sql-tde --version      # pg2sql 1.0.58 (TDE)

# 免费版不应出现 TDE 参数
./pg2sql --enckey x         # 应报错：--enckey 为 TDE 付费版功能...

# 二进制特征检查（免费版应为 0，TDE 版应 >0）
strings ./pg2sql     | grep -c 'SM4-ECB\|masterkey'
strings ./pg2sql-tde | grep -c 'SM4-ECB\|masterkey'

# 静态检查与单测（两个 tag 都要绿）
gofmt -l . ; go vet ./... ; go test ./...
go vet -tags tde ./... ; go test -tags tde ./...
```

## 3. 打包规范

### 3.1 目录规范（两版一致）

```
pg2sql-1.0.56-linux_x86_64/        # 免费版执行包（仅面向使用者：二进制 + 用户手册）
├── pg2sql                          # 二进制
└── docs/
    └── USER_GUIDE.md               # 用户手册（无 BUILD 内容）

pg2sql-1.0.56-tde-linux_x86_64/    # TDE 版执行包（目录名加 -tde，同样仅 USER_GUIDE.md）
├── pg2sql
└── docs/
    └── USER_GUIDE.md

pg2sql-1.0.56-src/                 # 免费版源码包（面向构建/打包者）
├── cmd/pg2sql/main.go             # 入口
├── internal/...                   # 全部 internal 包（剔除 paid 的 TDE 实现与 tde/）
├── go.mod / LICENSE / README.md / README.en.md
├── docs/USER_GUIDE.md
└── BUILD/BUILD.md                 # 本文档

pg2sql-1.0.56-tde-src/             # TDE 版源码包
├── cmd/ internal/ tde/            # 含 tde/ 与 paid 的 TDE 实现
├── go.mod / LICENSE / README.md / README.en.md
├── docs/USER_GUIDE.md
└── BUILD/BUILD.md
```

> 源码包解压后直接在包根执行 `go build ./cmd/pg2sql` 即可，不需要再摆弄文件清单。

### 3.2 源码包差异（重点）

| 内容 | 免费版源码包 | TDE 版源码包 |
|---|---|---|
| `cmd/`、`internal/{app,cli,config,util,logx,charset,datatype,engine,nt,catalog,ddl,objects,integrity}`、`tde/` 之外的其余目录 | ✅ | ✅ |
| `internal/paid/paid_stub.go`（`//go:build !tde`） | ✅ | ✅ |
| `internal/paid/paid_impl.go` / `paid_build.go` / `tde_verify.go`（均 `//go:build tde`） | ❌ 剔除 | ✅ |
| `tde/` 目录（sm4.go/tde.go/wallet.go/自测） | ❌ 剔除 | ✅ |
| `go.mod` / `LICENSE` / `README.md` / `README.en.md` / `docs/` / `BUILD/` | ✅ | ✅ |
| 构建命令 | `go build ./cmd/pg2sql` | `go build -tags tde ./cmd/pg2sql` |
| 测试命令 | `go test ./...` | `go test -tags tde ./...` |

> 免费版剔除的是**带 `//go:build tde` 的文件与 `tde/` 包**。`internal/paid/paid_stub.go` 必须保留：它提供免费版的空实现（付费参数报错、解密器恒 nil、help 不显示付费项），少了它 `paid.*` 符号会 undefined。

### 3.3 打包命令

**只有一条命令**（在仓库根执行）：

```bash
bash release.sh
```

它会依次完成：从 `internal/cli/version.go` 读 `Version` → 交叉编译 4 个二进制 → 整树复制出 2 个源码目录（按包排除 TDE 实现）→ 各自 `go build` 自检 → 出 6 个 `tar.gz` → 生成 `SHA256SUMS.txt`。

> **不再有 `COMMON_GO` / `TEST_GO` 逐文件清单**。v1.0.46~53 期间清单存在两份（`release.sh` 与本文档），长期漂移导致"少打 7 个文件、`go build` 一堆 undefined"。改为整树复制后，新增文件只需落到正确的包目录里，天然不会漏。

#### 不使用 `release.sh` 时的手工流程

```bash
set -e
cd <仓库根>
SRC=./pg2sql                 # 源码目录
REL=./release
VER=$(grep -oE 'Version = "[0-9.]+"' "$SRC/internal/cli/version.go" | grep -oE '[0-9.]+')
mkdir -p "$REL"

build_pair() { # $1=tag 前缀(空|tde) $2=输出目录
	local t=$1 d=$2 tags=""
	[ -n "$t" ] && tags="-tags tde"
	(cd "$SRC" && GOOS=linux GOARCH=amd64 go build $tags -trimpath -ldflags="-s -w" -o "$d/pg2sql" ./cmd/pg2sql)
}

# 免费版执行包
F="$REL/pg2sql-$VER-linux_x86_64"
mkdir -p "$F/docs"; build_pair "" "$F"; cp "$SRC/docs/USER_GUIDE.md" "$F/docs/"

# 免费版源码包（剔除 TDE 实现）
S="$REL/pg2sql-$VER-src"
mkdir -p "$S"
rsync -a --exclude=pg2sql --exclude=pg2sql-tde --exclude='*.bak*' \
	--exclude=internal/paid/paid_impl.go --exclude=internal/paid/paid_build.go \
	--exclude=internal/paid/tde_verify.go --exclude=tde \
	"$SRC/cmd" "$SRC/internal" "$SRC/docs" "$SRC/BUILD" \
	"$SRC/README.md" "$SRC/README.en.md" "$SRC/LICENSE" "$SRC/go.mod" "$S/"

# TDE 版：build_pair tde "$REL/pg2sql-$VER-tde-linux_x86_64"，源码包去掉上面的 --exclude 即可
(cd "$S" && go build -o /dev/null ./cmd/pg2sql)   # 源码包自检：必须能独立编过

tar czf "$REL/pg2sql_${VER}_src.tar.gz" -C "$REL" "pg2sql-$VER-src"
cd "$REL" && shasum -a 256 pg2sql_${VER}_*.tar.gz > SHA256SUMS.txt
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
./pg2sql     --version   # pg2sql 1.0.58
./pg2sql-tde --version   # pg2sql 1.0.58 (TDE)
./pg2sql --enckey x      # 报错提示 TDE 版
./pg2sql-tde --enckey x  # 正常解析（缺数据文件时报文件错误而非参数错误）

# 3) 二进制特征
strings ./pg2sql     | grep -c 'SM4-ECB\|masterkey'   # = 0
strings ./pg2sql-tde | grep -c 'SM4-ECB\|masterkey'   # > 0

# 4) 源码包可编译性 + 单测（解压后分别构建；这一步能直接暴露"清单漏文件"）
cd pg2sql-1.0.56-src      && go build ./cmd/pg2sql && ./pg2sql --version                        # 1.0.53
cd pg2sql-1.0.56-src      && go test ./...                                           # ok
cd pg2sql-1.0.56-tde-src  && go build -tags tde ./cmd/pg2sql && ./pg2sql --version              # 1.0.53 (TDE)
cd pg2sql-1.0.56-tde-src  && go test -tags tde ./...                                 # ok

# 5) 常规功能等价（免费 vs TDE 对同一普通表导出应逐字节一致）
#    对同一实例同一表分别用两版导出，diff 应为空
```

## 5. 版本与回归约束

- 任何代码变更：版本号 `Version` +0.1（`internal/cli/version.go`）。
- 每个 bug 修复必须**全版本回归通过**才算完成（金仓 V8/V9 各 block size + PG12-18 各 block size）。
- TDE 功能改动需跑加密专项回归：`pgbuild/reg_tde_r1c10.sh` + `reg_tde_v8.sh` + `reg_tde_imp.sh`（明文闭环，14 实例 28/28 全绿为基准）。
- TDE 版单独发布不影响免费版；免费版二进制**不得**残留任何 TDE 可调用代码（strings 检查为硬性门槛）。

## 6. 源码树自检（新增包/文件后必跑）

目录重构后不再有"文件清单"，改成**整树自检**：每个 `.go` 都必须属于一个能被 `go list` 看见的包。`doc_check.sh` 已内置这一项，单独跑也可以：

```bash
bash doc_check.sh        # 第 3 项：源码树完整性
```

手工等价命令：

```bash
cd pg2sql
go list ./... | wc -l                                  # 包数量（当前 15 个）
find cmd internal tde -name '*.go' | wc -l             # .go 文件数（当前 60 个）
go build ./... && go build -tags tde ./...             # 双标签都必须过
```

判定标准：

| 现象 | 含义 | 处理 |
|---|---|---|
| `go list ./...` 报错 | 某目录有语法错误或包名冲突 | 修到能列出为止 |
| `go build ./...` 报 undefined | 跨包标识符没导出（Go 要求跨包使用大写名） | 把符号导出，或在同包内下沉 |
| `go build ./...` 报 import cycle | 包依赖成环 | 用回调注入打破（参考 `datatype.DynamicScanner`） |
| 免费源码包 `go build` 报 `paid.*` undefined | 漏了 `paid_stub.go` | 确认 `--exclude` 没把它剔掉 |

## 7. 发版流程与文档同步责任

### 7.1 一条命令

```bash
bash release.sh
```

它会先跑 `doc_check.sh`（文档一致性自检），**FAIL 时直接终止打包**，通过后才编译与出包。所以只要习惯用这条命令，下面 7.2 的检查项就不会被漏掉。

### 7.2 自检能自动兜住的（无需人工记忆）

| 检查项 | 判定 | 失败时说明 |
|---|---|---|
| 三份文档标题版本 = `Version`（internal/cli/version.go） | FAIL | README / 手册 / BUILD.md 忘了改版本号 |
| README、手册含当前版本条目 | FAIL | 忘了写更新记录 |
| **英文 README 与中文 README 章节数一致** | FAIL | 新增中文章节没翻译（v1.0.53 前英文版长期停在 10 章，标题却写新版本） |
| 英文 README 二级标题无中文残留 | FAIL | 只改了标题没翻译正文 |
| 源码树完整性（无游离 `.go`、双标签可编） | FAIL | 新增文件放错目录或跨包符号没导出（会打出编不过的源码包） |
| CLI 定义的每个参数都在手册中出现 | WARN | 新增/改名参数没写进参数速查表 |
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

> 落盘原则：**一个事实只写一处，其它地方指过去**。例如 `report.json` 字段的判读只在 README 写全，手册 4.7 给摘要并链接过去；源码包排除规则只在 `release.sh` 写全，BUILD.md 指过去。重复内容迟早漂移——v1.0.53 的两个文档缺陷（BUILD.md 清单过期、手册缺 `--schema` 报错说明）都是重复与遗漏造成的。
