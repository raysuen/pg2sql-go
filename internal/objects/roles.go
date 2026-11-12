package objects

// v1.0.53：角色离线导出（--roles）
//
// 数据源：pg_authid（relfilenode 1260，位于 <PGDATA>/global/；金仓为 sys_authid）
//         pg_auth_members（1261，角色成员关系）
//
// 关键约束：
//   - 两表都是【共享目录】，文件在 global/ 下，不是 base/<dboid>/
//   - PG12+ 起 oid 是 attnum=1 的普通列（不是行首系统列），列布局用 catalog.TableLayout 动态探测
//   - rolpassword 只存密文（SCRAM-SHA-256 / md5），本工具不接触明文，也不反推
//   - 排除内置角色组（pg_*：pg_read_all_data 等），它们由 initdb 建立，重建会报错

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"pg2sql/internal/catalog"
	"pg2sql/internal/datatype"
	"pg2sql/internal/engine"
	"pg2sql/internal/logx"
	"sort"
	"strings"
	"time"
)

// RoleRow：单角色定义
type RoleRow struct {
	OID          uint32
	Name         string
	Super        bool
	Inherit      bool
	CreateRole   bool
	CreateDB     bool
	CanLogin     bool
	Replication  bool
	BypassRLS    bool
	ConnLimit    int32
	ConnLimitSet bool   // rolconnlimit 是否读到（区分"默认 -1"与"解析失败 0"）
	Password     string // 密文原文（scrub 后）；空 = 无密码
	HasPassword  bool
	ValidUntil   string // 已格式化的 timestamptz 字面量内容；空 = 无期限
}

// RoleMember：角色成员关系（GRANT role TO member）
type RoleMember struct {
	Role     uint32
	Member   uint32
	Admin    bool
	Inherit  bool
	SetOpt   bool
	Has16Col bool // PG16+ 才有 inherit_option/set_option 列
}

// RoleSet：一次导出的完整结果
type RoleSet struct {
	Roles   []RoleRow
	Members []RoleMember
	Layout  string // "dynamic" | "fixed"（调试/日志用）
}

// FirstNormalObjectId：initdb 阶段创建对象的 OID 上界（PG 与金仓同值）。
// 小于它的角色由 initdb 建立（pg_monitor、pg_read_all_data、金仓 system/sa 等），
// 目标实例必然已存在，重复 CREATE 会直接报错中断脚本 → 一律跳过。
const firstNormalObjectID = 16384

// IsSystemRole：initdb 内置角色判定（OID 优先，OID 缺失时退回名单）
func IsSystemRole(r RoleRow) bool {
	if r.OID != 0 {
		return r.OID < firstNormalObjectID
	}
	return IsBuiltinRole(r.Name)
}

// IsBuiltinRole：initdb 内置角色名单（兜底，覆盖 PG12-18 已知项；金仓 system/sa 不在名单内，
// 由 OID 判定兜住）
func IsBuiltinRole(name string) bool {
	switch name {
	case "pg_monitor", "pg_read_all_settings", "pg_read_all_stats",
		"pg_stat_scan_tables", "pg_read_all_data", "pg_write_all_data",
		"pg_database_owner", "pg_signal_backend", "pg_checkpoint",
		"pg_use_reserved_connections", "pg_create_subscription",
		"pg_read_server_files", "pg_write_server_files", "pg_execute_server_program",
		"pg_maintain":
		return true
	}
	return false
}

// PgTimestamptzString：int64 微秒（自 2000-01-01）→ SQL 字面量内容
func PgTimestamptzString(v int64) string {
	const (
		endInf = int64(0x7FFFFFFFFFFFFFFF)
		negInf = int64(-0x8000000000000000)
	)
	switch v {
	case endInf:
		return "infinity"
	case negInf:
		return "-infinity"
	}
	// 2000-01-01 00:00:00 UTC 的 Unix 秒
	// v1.0.56 修复（P3-G14）：time.Duration(v)*time.Microsecond 在远期时间
	// （≳292 年）溢出 int64，改天/日内微秒两级换算
	base := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	days := v / (86400 * 1000000)
	rem := v % (86400 * 1000000)
	t := base.AddDate(0, 0, int(days)).Add(time.Duration(rem) * time.Microsecond)
	return t.UTC().Format("2006-01-02 15:04:05.999999+00")
}

// varlenaText：varlena 值 → 文本（不含头部）
func varlenaText(b []byte) (string, bool) {
	if len(b) == 0 {
		return "", false
	}
	kind, total, doff, dlen := datatype.VarlenaParse(b, 0)
	if kind == datatype.VARLENA_EXTERNAL || total <= 0 || doff < 0 || dlen < 0 || doff+dlen > len(b) {
		return "", false
	}
	return string(b[doff : doff+dlen]), true
}

// sharedCatalogPath：定位共享目录（pg_authid / pg_auth_members）在 global/ 下的文件
func sharedCatalogPath(root string, relOID int, known map[string]bool, ps int) string {
	dir := filepath.Join(root, "global")
	if p := engine.DetectSysFile(dir, relOID, known, ps); p != "" {
		return p
	}
	p := filepath.Join(dir, fmt.Sprint(relOID))
	if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
		return p
	}
	return ""
}

// normalizeOidFirst：把系统目录布局归一化为「首槽 = 4B oid」（v1.0.61 P2-10）。
//   - PG12+：oid 是 attnum=1 的普通列，pg_attribute 里就有该行 → 已是该形状；
//   - hasoids 目录（PG<12 / 金仓 V8）：oid 是系统列、不在 tupdesc 里，
//     pg_attribute 探测出的布局直接从 rolname(64B) 开始 → 需要补一个 4B 槽。
//
// 归一化后 ExtractFieldsDirect 的 off=1 分支对两种目录通用。
func normalizeOidFirst(l []datatype.ColLen) []datatype.ColLen {
	if len(l) == 0 {
		return l
	}
	if l[0].AttLen == 4 && !l[0].IsVarlena {
		return l
	}
	return append([]datatype.ColLen{{AttLen: 4, IsVarlena: false, AttAlign: "i"}}, l...)
}

// sysCatalogLayout：共享系统目录（pg_authid / pg_auth_members）的物理列布局。
//
// v1.2.16 修复（D3 的前置条件）：这条链路原来有两处断点，导致动态布局**从未生效**：
//  1. 共享目录的属性行存在各库自己的 pg_attribute 里，而 --roles 常以
//     base/<oid> 或数据目录为 dbDir 调用，TableLayout 按固定 relfilenode
//     常量（1260）过滤 attrelid → 命中 0 行 → 直接返回 nil；
//  2. 金仓的系统目录 OID 与 PG 常量不同（实测 V8R6C9B14：pg_authid=12007、
//     sys_authid=12313），而数据文件仍是 global/1260 —— 即使 dbDir 正确，
//     用 relfilenode 当 attrelid 也查不到。
//
// 因此这里改成：先按 relname 反查真实 OID（sys_* 优先，金仓的物理定义在 sys_* 上），
// 再逐个尝试 TableLayout；全部失败才由调用方回退固定布局。
func sysCatalogLayout(dbDir string, relFile uint32, names []string, version int, isKB bool) []datatype.ColLen {
	for _, d := range catalogDbCandidates(dbDir) {
		// 标准 OID 优先：金仓把 pg_authid/sys_authid 都做成了**视图**（relkind='v'、
		// relfilenode=0），实体表是 oid=1260 的 _authid（relkind='r'）。数据文件
		// global/1260 对应的正是它，所以先按标准 OID 取属性；视图的列定义可能是
		// 兼容层的 12 列，按它解析会漏掉金仓新增的 rolconntime/rolconninterval。
		ids := []uint32{relFile}
		for _, id := range relOIDsByNames(d, names, version, isKB) {
			if id != relFile {
				ids = append(ids, id)
			}
		}
		for _, id := range ids {
			if l := catalog.TableLayout(d, id, version, isKB); len(l) > 0 {
				return normalizeOidFirst(stripKbSystemCols(l))
			}
		}
	}
	return nil
}

// catalogDbCandidates：可用于读 pg_class/pg_attribute 的候选数据库目录。
//
// 共享目录的属性行分布在**每个库**的 pg_attribute 里，而 --roles 传入的 dbDir
// 可能是数据目录、也可能是不存在的 base/5（--datadir 未配 --db-oid 时的默认值，
// 实测 V8R6C9B14 的库 OID 从 12741 起，base/5 根本不存在）。所以这里先取 dbDir
// 本身，再从数据目录的 base/* 里逐个补候选。
func catalogDbCandidates(dbDir string) []string {
	out := []string{dbDir}
	root := ""
	if fi, err := os.Stat(filepath.Join(dbDir, "base")); err == nil && fi.IsDir() {
		root = dbDir // dbDir 就是数据目录
	} else if filepath.Base(filepath.Dir(dbDir)) == "base" {
		root = filepath.Dir(filepath.Dir(dbDir)) // dbDir = <datadir>/base/<oid>
	}
	if root == "" {
		return out
	}
	ents, err := os.ReadDir(filepath.Join(root, "base"))
	if err != nil {
		return out
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names {
		d := filepath.Join(root, "base", n)
		if d != dbDir {
			out = append(out, d)
		}
	}
	return out
}

// stripKbSystemCols：剥掉金仓 pg_attribute 里混入的前导系统列。
//
// 金仓 V8/V9 把 tableoid/cmax/xmax/cmin/xmin/ctid 也登记为正 attnum 1-6
// （PG 里这些是 attnum ≤ 0 的隐藏列，TableLayout 会过滤掉）。ctid 恒为 6B，
// 是这段前缀最可靠的锚点；不剥掉会让后续所有下标整体后移 6，口令/有效期错位。
func stripKbSystemCols(l []datatype.ColLen) []datatype.ColLen {
	if len(l) >= 7 && l[5].AttLen == 6 && !l[5].IsVarlena &&
		l[0].AttLen == 4 && l[1].AttLen == 4 && l[2].AttLen == 4 &&
		l[3].AttLen == 4 && l[4].AttLen == 4 {
		return l[6:]
	}
	return l
}

// relOIDsByNames：按 relname 反查关系 OID（见 sysCatalogLayout 注释）。
func relOIDsByNames(dbDir string, names []string, version int, isKB bool) []uint32 {
	p := engine.DetectSysFile(dbDir, 1259, map[string]bool{"pg_class": true, "sys_class": true}, 0)
	if p == "" {
		return nil
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	var out []uint32
	for tup := range engine.IterSysTuples(p, version, isKB) {
		row := catalog.ClassFields(tup, version)
		if row == nil || !want[row.RelName] {
			continue
		}
		out = append(out, row.OID)
	}
	return out
}

// LoadRoles：读取 pg_authid + pg_auth_members
func LoadRoles(dbDir, root string, version int, isKB bool, ps int) (*RoleSet, error) {
	rs := &RoleSet{}
	if ps <= 0 {
		ps = 8192
	}
	authPath := sharedCatalogPath(root, catalog.PG_AUTHID_RELFILE, map[string]bool{"pg_authid": true, "sys_authid": true}, ps)
	if authPath == "" {
		return nil, fmt.Errorf("未找到 pg_authid（%s/global/%d）", root, catalog.PG_AUTHID_RELFILE)
	}
	// 动态布局（pg_attribute 中各列的真实顺序/长度），失败回退硬编码
	layout := sysCatalogLayout(dbDir, catalog.PG_AUTHID_RELFILE,
		[]string{"sys_authid", "pg_authid"}, version, isKB)
	layoutKind := "dynamic"
	if !validAuthidLayout(layout) {
		layout = fixedAuthidLayout()
		layoutKind = "fixed"
	}
	rs.Layout = layoutKind
	// v1.2.16 修复（D3）：口令/有效期按布局**尾部锚定**取值，不再写死 off+9/off+10。
	// 金仓 V8/V9 的 pg_authid 在 rolconnlimit 之后多出 rolconntime(attnum 11, int8)
	// 与 rolconninterval(attnum 12, varlena)，rolpassword/rolvaliduntil 顺延到 13/14。
	// 旧实现按 PG12 固定位置取 off+9/off+10 → 实际读到 rolconntime/rolconninterval，
	// PASSWORD 与 VALID UNTIL 静默丢失（实机 V8R6C9B14 复现）。
	authOff := 0
	if len(layout) > 0 && layout[0].AttLen == 4 {
		authOff = 1
	}
	pwIdx, vuIdx := authidTailIndexes(layout, authOff)
	shortRows := 0
	for tup := range engine.IterSysTuples(authPath, version, isKB) {
		nulls := tup.GetNulls()
		// v1.0.61（P2-10）：hasoids 行数据以 oid(4B) 开头，但 NULL 位图只覆盖
		// tupdesc 列（不含系统 oid）→ 位图前置一位，让位图与归一化布局对齐。
		if tup.TInfomask&heapHasOid != 0 {
			nulls = append([]bool{false}, nulls...)
		}
		fields := datatype.ExtractFieldsDirect(tup.Raw, tup.THoff, nulls, layout)
		if len(fields) < 2 {
			continue
		}
		off := 0
		var oid uint32
		if layout[0].AttLen == 4 { // PG12+：oid 为 attnum=1 普通列
			if fields[0] != nil && len(*fields[0]) >= 4 {
				oid = binary.LittleEndian.Uint32(*fields[0])
			}
			off = 1
		}
		if len(fields) < off+1 {
			continue
		}
		// v1.0.63 修复（C-3）：字段数不足以覆盖 rolvaliduntil 时不再静默丢属性——
		// 计数并在 Layout 上标注，避免"看起来一切正常"。
		// v1.2.16：判据改按锚定后的 vuIdx，金仓 14 列布局同样适用。
		// 注意：rolvaliduntil 为 NULL 是**正常**语义（金仓实测 29 个角色里 28 个
		// 未设有效期，存的是 NULL 而不是 PG 的 'infinity'），不能算"行被截断"——
		// 旧判据只看字段数，自然不会误计；改为按值判空会把正常库报成 truncated。
		if vuIdx < 0 || len(fields) <= vuIdx {
			shortRows++
		}
		nameF := fields[off]
		if nameF == nil {
			continue
		}
		nm := datatype.CString(*nameF, 0)
		if nm == "" {
			continue
		}
		r := RoleRow{OID: oid, Name: nm}
		bit := func(i int) bool {
			if off+i >= len(fields) || fields[off+i] == nil {
				return false
			}
			return (*fields[off+i])[0] != 0
		}
		r.Super = bit(1)
		r.Inherit = bit(2)
		r.CreateRole = bit(3)
		r.CreateDB = bit(4)
		r.CanLogin = bit(5)
		r.Replication = bit(6)
		r.BypassRLS = bit(7)
		// 固定部分偏移（off 指向 rolname）：+1..+7 = 7 个布尔（super/inherit/createrole/
		// createdb/canlogin/replication/bypassrls）、+8 = rolconnlimit(int4)。
		// 口令/有效期位置随目录版本变化（金仓多两列），用 authidTailIndexes 锚定，
		// 见函数注释；不得写死 +9/+10。
		if off+8 < len(fields) && fields[off+8] != nil && len(*fields[off+8]) >= 4 {
			r.ConnLimit = int32(binary.LittleEndian.Uint32(*fields[off+8]))
			r.ConnLimitSet = true
		}
		if pwIdx >= 0 && pwIdx < len(fields) && fields[pwIdx] != nil {
			if s, ok := varlenaText(*fields[pwIdx]); ok && s != "" {
				r.Password = s
				r.HasPassword = true
			}
		}
		if vuIdx >= 0 && vuIdx < len(fields) && fields[vuIdx] != nil && len(*fields[vuIdx]) >= 8 {
			r.ValidUntil = PgTimestamptzString(int64(binary.LittleEndian.Uint64(*fields[vuIdx])))
		}
		rs.Roles = append(rs.Roles, r)
	}
	if len(rs.Roles) == 0 {
		return nil, fmt.Errorf("pg_authid 未解析出任何角色（布局不匹配：%s）", layoutKind)
	}
	if shortRows > 0 {
		// v1.0.63（C-3）：如实反映"部分属性缺失"，不再只报 dynamic
		rs.Layout = fmt.Sprintf("%s(truncated:%d行缺VALID UNTIL)", layoutKind, shortRows)
		logx.Logf("警告: pg_authid 有 %d 行的字段数不足（缺 rolvaliduntil），这些角色的 VALID UNTIL 未导出", shortRows)
	}
	sort.Slice(rs.Roles, func(i, j int) bool { return rs.Roles[i].Name < rs.Roles[j].Name })

	// ---- 成员关系 ----
	memPath := sharedCatalogPath(root, 1261, map[string]bool{"pg_auth_members": true, "sys_auth_members": true}, ps)
	if memPath != "" {
		mlayout := sysCatalogLayout(dbDir, 1261,
			[]string{"sys_auth_members", "pg_auth_members"}, version, isKB)
		// v1.0.65 修复（P1-4）：has16 必须来自【校验通过】的布局——金仓 V8/V9 的
		// pg_attribute 把系统列（tableoid/cmax/xmax/cmin/xmin/ctid）列为正 attnum
		// 1-6 且 TableLayout 未剔除，未校验前 len>=7 恒真，Has16Col 被错误置位，
		// 随后按垃圾字段输出 INHERIT/SET 选项。
		normalized := normalizeOidFirst(mlayout)
		has16 := false
		if validAuthMembersLayout(normalized) {
			mlayout = normalized
			has16 = len(mlayout) >= 7
		} else {
			// 布局不可信（金仓系统列混入 / TableLayout 失败）：回退固定布局。
			// inherit/set 列是 PG16 才引入的，按内核版本判定。
			mlayout = fixedAuthMembersLayout(version >= 16)
			has16 = version >= 16
		}
		for tup := range engine.IterSysTuples(memPath, version, isKB) {
			nulls := tup.GetNulls()
			if tup.TInfomask&heapHasOid != 0 {
				nulls = append([]bool{false}, nulls...)
			}
			fields := datatype.ExtractFieldsDirect(tup.Raw, tup.THoff, nulls, mlayout)
			base := 0
			if len(mlayout) > 0 && mlayout[0].AttLen == 4 && len(mlayout) > 4 {
				base = 1 // oid 普通列
			}
			if len(fields) < base+4 {
				continue
			}
			get := func(i int) uint32 {
				if fields[i] == nil || len(*fields[i]) < 4 {
					return 0
				}
				return binary.LittleEndian.Uint32(*fields[i])
			}
			m := RoleMember{Role: get(base), Member: get(base + 1), Has16Col: has16}
			if fields[base+3] != nil && len(*fields[base+3]) >= 1 {
				m.Admin = (*fields[base+3])[0] != 0
			}
			if has16 && len(fields) >= base+6 {
				if fields[base+4] != nil && len(*fields[base+4]) >= 1 {
					m.Inherit = (*fields[base+4])[0] != 0
				}
				if fields[base+5] != nil && len(*fields[base+5]) >= 1 {
					m.SetOpt = (*fields[base+5])[0] != 0
				}
			} else {
				m.Inherit = true
				m.SetOpt = true
			}
			if m.Role != 0 && m.Member != 0 {
				rs.Members = append(rs.Members, m)
			}
		}
	}
	return rs, nil
}

// authidTailIndexes：从布局尾部锚定 rolpassword / rolvaliduntil 的下标。
//
// v1.2.16 新增（D3）。pg_authid 尾部在两种内核上不同：
//
//	PG 12+ … rolconnlimit(int4) | rolpassword(text)      | rolvaliduntil(timestamptz 8B)
//	金仓 V8/V9 … rolconnlimit | rolconntime(int8) | rolconninterval(text) |
//	             rolpassword(text) | rolvaliduntil(timestamptz 8B)
//
// 金仓多出的两列不改变"尾部形态"：**最后一个 8B 定长列恒为 rolvaliduntil**，
// 它前面最近的一个 varlena 列恒为 rolpassword。按此锚定可同时适配两者，
// 且不依赖列名（catalog.TableLayout 只返回长度信息）。
// 返回 -1 表示无法锚定（布局异常，调用方按固定布局回退）。
func authidTailIndexes(l []datatype.ColLen, off int) (pw, vu int) {
	vu = -1
	for i := len(l) - 1; i > off; i-- {
		if l[i].AttLen == 8 && !l[i].IsVarlena {
			vu = i
			break
		}
	}
	if vu <= off {
		return -1, -1
	}
	pw = -1
	for i := vu - 1; i > off; i-- {
		if l[i].IsVarlena {
			pw = i
			break
		}
	}
	return pw, vu
}

// validAuthidLayout：动态布局合理性校验（rolname 定长 64、bool 1 字节、尾部可锚定）
func validAuthidLayout(l []datatype.ColLen) bool {
	if len(l) < 11 {
		return false
	}
	off := 0
	if l[0].AttLen == 4 && !l[0].IsVarlena {
		off = 1
	}
	// v1.0.63 修复（C-3）：原判据 len(l) < 10 只保证到 rolconnlimit，
	// 而 rolvaliduntil 需要 off+10（12 列）。恰好 11 列时布局被判为
	// dynamic 通过，但 VALID UNTIL 静默丢失且 Layout 仍报 dynamic。
	//
	// v1.2.16 修复（D3）：改为按尾部锚定判定。原判据硬性要求 l[off+10] 是 8B，
	// 金仓 14 列布局该位是 rolconninterval(varlena) → 校验失败 → 回退 PG 固定布局
	// → 口令/有效期按错位读取，静默丢失。
	if len(l) < off+11 {
		return false
	}
	if l[off].AttLen != 64 {
		return false
	}
	for i := 1; i <= 7; i++ { // 7 个布尔属性（PG12+ 无 rolconfig）
		if l[off+i].AttLen != 1 {
			return false
		}
	}
	if l[off+8].AttLen != 4 { // rolconnlimit
		return false
	}
	pw, vu := authidTailIndexes(l, off)
	if pw < 0 || vu < 0 { // 尾部必须能锚定出口令与有效期
		return false
	}
	return true
}

func validAuthMembersLayout(l []datatype.ColLen) bool {
	if len(l) < 5 {
		return false
	}
	off := 0
	if l[0].AttLen == 4 && !l[0].IsVarlena {
		off = 1
	}
	// v1.0.65 修复（P1-4）：在 roleid/member/grantor 全 4B 之上增加 admin 1B bool
	// 锚点。旧判据只查前三个 4B——金仓 V8/V9 系统列（tableoid/cmax/xmax/cmin/
	// xmin/ctid）作为正 attnum 混入 TableLayout 时，[tableoid,cmax,xmax] 恰好全 4B
	// 通过校验，cmax/xmax 被当成 roleid/member 解析（通常为 0 → 成员关系被
	// Role!=0 && Member!=0 过滤而静默全丢；有更新残留时产出垃圾 GRANT）。
	// admin 列恒为 1B bool，含系统列的布局必然在此失败并回退固定布局。
	if len(l) < off+4 {
		return false
	}
	if l[off].AttLen != 4 || l[off+1].AttLen != 4 || l[off+2].AttLen != 4 {
		return false
	}
	return l[off+3].AttLen == 1
}

// heapHasOid：HeapTupleHeader.t_infomask 的 HEAP_HASOID 位（PG<12 / 金仓 V8 的
// hasoids 目录行会在数据区开头额外存 4 字节 oid）。
const heapHasOid = 0x0008

// fixedAuthidLayout：catalog.TableLayout 不可用时的兜底（PG12-18 与金仓 V8/V9 实测布局）
//
//	[oid 4][rolname 64][rolsuper..rolbypassrls 7×1][rolconnlimit 4][rolpassword text][rolvaliduntil 8]
//
// 首槽恒为 4B oid：PG12+ 是真实列、hasoids 目录是行内 oid（配 nulls 前置对齐）。
func fixedAuthidLayout() []datatype.ColLen {
	return []datatype.ColLen{
		{AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 64, IsVarlena: false, AttAlign: "c"},
		{AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"},
		{AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"},
		{AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 0, IsVarlena: true, AttAlign: "i"}, {AttLen: 8, IsVarlena: false, AttAlign: "d"},
	}
}

// fixedAuthMembersLayout：[oid][roleid][member][grantor][admin]（PG16+ 追加 inherit/set）
func fixedAuthMembersLayout(has16 bool) []datatype.ColLen {
	l := []datatype.ColLen{
		{AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 4, IsVarlena: false, AttAlign: "i"}, {AttLen: 1, IsVarlena: false, AttAlign: "c"},
	}
	if has16 {
		l = append(l, datatype.ColLen{AttLen: 1, IsVarlena: false, AttAlign: "c"}, datatype.ColLen{AttLen: 1, IsVarlena: false, AttAlign: "c"})
	}
	return l
}

// RoleNameMap：OID → 角色名（成员关系渲染用）
func RoleNameMap(rs *RoleSet) map[uint32]string {
	m := make(map[uint32]string, len(rs.Roles))
	for _, r := range rs.Roles {
		if r.OID != 0 {
			m[r.OID] = r.Name
		}
	}
	return m
}

// GenRolesDDL：角色 + 成员关系 → SQL 文本
func GenRolesDDL(rs *RoleSet, withPassword bool) (string, []string) {
	var sb strings.Builder
	var notes []string
	sb.WriteString("-- pg2sql 角色导出（--roles，v1.0.53）\n")
	sb.WriteString("-- 来源: pg_authid / pg_auth_members 离线解析（global/ 共享目录）\n")
	if withPassword {
		sb.WriteString("-- 警告: PASSWORD 为源库密文（SCRAM-SHA-256 / md5）原样导出，请按敏感信息保管；\n")
		sb.WriteString("--       如需脱敏重出，加 --roles-nopassword。\n")
	} else {
		sb.WriteString("-- 说明: 已按 --roles-nopassword 省略密码密文（与原库同口令需另行设置）。\n")
	}
	sb.WriteString("-- 重复执行会因 role already exists 报错，回灌前请确认目标实例角色状态。\n\n")

	names := RoleNameMap(rs)
	byOID := make(map[uint32]RoleRow, len(rs.Roles))
	builtin := 0
	for _, r := range rs.Roles {
		if r.OID != 0 {
			byOID[r.OID] = r
		}
		if IsSystemRole(r) {
			builtin++
			continue
		}
		sb.WriteString(GenOneRole(r, withPassword))
	}
	if builtin > 0 {
		notes = append(notes, fmt.Sprintf("跳过 %d 个 initdb 内置角色（OID<%d 或已知 pg_* 组，目标实例自带）", builtin, firstNormalObjectID))
	}
	if builtin > 0 && builtin == len(rs.Roles) {
		sb.WriteString("-- 本库没有用户创建的角色：仅有 initdb 内置角色（system/sso/sao/sys_*、pg_* 组等），\n")
		sb.WriteString("-- 目标实例自带同名角色，无需重建。\n")
	}

	// 成员关系（角色名排序，输出稳定）。
	// 被授予方是内置角色时跳过（目标实例的既定状态，重复授予无意义且可能因权限失败）；
	// 授予方是内置角色（如 GRANT pg_read_all_data TO app）必须保留——这是真实的权限委派。
	type grant struct{ role, member, line string }
	var grants []grant
	missing := 0
	for _, m := range rs.Members {
		rn, ok1 := names[m.Role]
		mn, ok2 := names[m.Member]
		if !ok1 || !ok2 {
			missing++
			continue
		}
		if r, ok := byOID[m.Member]; ok && IsSystemRole(r) {
			continue
		}
		g := "GRANT " + engine.QuoteName(rn) + " TO " + engine.QuoteName(mn)
		var opts []string
		if m.Admin {
			opts = append(opts, "ADMIN OPTION")
		}
		if m.Has16Col {
			if !m.Inherit {
				opts = append(opts, "INHERIT FALSE")
			}
			if !m.SetOpt {
				opts = append(opts, "SET FALSE")
			}
		}
		if len(opts) > 0 {
			g += " WITH " + strings.Join(opts, ", ")
		}
		grants = append(grants, grant{rn, mn, g + ";"})
	}
	if len(grants) > 0 {
		sort.Slice(grants, func(i, j int) bool {
			if grants[i].role == grants[j].role {
				return grants[i].member < grants[j].member
			}
			return grants[i].role < grants[j].role
		})
		sb.WriteString("\n-- 角色成员关系\n")
		for _, g := range grants {
			sb.WriteString(g.line + "\n")
		}
	}
	if missing > 0 {
		notes = append(notes, fmt.Sprintf("%d 条成员关系引用了未导出的角色 OID（已跳过）", missing))
	}
	return sb.String(), notes
}

// GenOneRole：单条 CREATE ROLE（属性顺序固定，便于 diff）
func GenOneRole(r RoleRow, withPassword bool) string {
	b := func(v bool, on, off string) string {
		if v {
			return on
		}
		return off
	}
	parts := []string{
		b(r.CanLogin, "LOGIN", "NOLOGIN"),
		b(r.Super, "SUPERUSER", "NOSUPERUSER"),
		b(r.CreateDB, "CREATEDB", "NOCREATEDB"),
		b(r.CreateRole, "CREATEROLE", "NOCREATEROLE"),
		b(r.Inherit, "INHERIT", "NOINHERIT"),
		b(r.Replication, "REPLICATION", "NOREPLICATION"),
		b(r.BypassRLS, "BYPASSRLS", "NOBYPASSRLS"),
	}
	// -1 = 默认（不限连接），无需输出；0 = 禁止连接，必须输出（易被误当成默认值）
	if r.ConnLimitSet && r.ConnLimit != -1 {
		parts = append(parts, fmt.Sprintf("CONNECTION LIMIT %d", r.ConnLimit))
	}
	if withPassword && r.HasPassword {
		parts = append(parts, "PASSWORD "+SQLLiteral(r.Password))
	}
	if r.ValidUntil != "" {
		parts = append(parts, "VALID UNTIL "+SQLLiteral(r.ValidUntil))
	}
	return "CREATE ROLE " + engine.QuoteName(r.Name) + " WITH " + strings.Join(parts, " ") + ";\n"
}

// SQLLiteral：单引号字符串字面量（内部单引号双写）
func SQLLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
