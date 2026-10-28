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
	layout := catalog.TableLayout(dbDir, catalog.PG_AUTHID_RELFILE, version, isKB)
	layoutKind := "dynamic"
	if !validAuthidLayout(layout) {
		layout = fixedAuthidLayout(isKB)
		layoutKind = "fixed"
	}
	rs.Layout = layoutKind
	for tup := range engine.IterSysTuples(authPath, version, isKB) {
		nulls := tup.GetNulls()
		fields := datatype.ExtractFieldsDirect(tup.Raw, tup.THoff, nulls, layout)
		if len(fields) < 11 {
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
		// createdb/canlogin/replication/bypassrls），+8 = rolconnlimit(int4)、
		// +9 = rolpassword(text)、+10 = rolvaliduntil(timestamptz 8B)。
		// 注：PG12+ 的 pg_authid 无 rolconfig 列（已移除），勿按旧布局多算一位。
		if off+8 < len(fields) && fields[off+8] != nil && len(*fields[off+8]) >= 4 {
			r.ConnLimit = int32(binary.LittleEndian.Uint32(*fields[off+8]))
			r.ConnLimitSet = true
		}
		if off+9 < len(fields) && fields[off+9] != nil {
			if s, ok := varlenaText(*fields[off+9]); ok && s != "" {
				r.Password = s
				r.HasPassword = true
			}
		}
		if off+10 < len(fields) && fields[off+10] != nil && len(*fields[off+10]) >= 8 {
			r.ValidUntil = PgTimestamptzString(int64(binary.LittleEndian.Uint64(*fields[off+10])))
		}
		rs.Roles = append(rs.Roles, r)
	}
	if len(rs.Roles) == 0 {
		return nil, fmt.Errorf("pg_authid 未解析出任何角色（布局不匹配：%s）", layoutKind)
	}
	sort.Slice(rs.Roles, func(i, j int) bool { return rs.Roles[i].Name < rs.Roles[j].Name })

	// ---- 成员关系 ----
	memPath := sharedCatalogPath(root, 1261, map[string]bool{"pg_auth_members": true, "sys_auth_members": true}, ps)
	if memPath != "" {
		mlayout := catalog.TableLayout(dbDir, 1261, version, isKB)
		has16 := len(mlayout) >= 7
		if !validAuthMembersLayout(mlayout) {
			mlayout = fixedAuthMembersLayout(has16)
		}
		for tup := range engine.IterSysTuples(memPath, version, isKB) {
			nulls := tup.GetNulls()
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

// validAuthidLayout：动态布局合理性校验（rolname 定长 64、bool 1 字节、最后是 timestamptz）
func validAuthidLayout(l []datatype.ColLen) bool {
	if len(l) < 10 {
		return false
	}
	off := 0
	if l[0].AttLen == 4 && !l[0].IsVarlena {
		off = 1
	}
	if l[off].AttLen != 64 {
		return false
	}
	for i := 1; i <= 7; i++ { // 7 个布尔属性（PG12+ 无 rolconfig）
		if l[off+i].AttLen != 1 {
			return false
		}
	}
	return l[off+8].AttLen == 4
}

func validAuthMembersLayout(l []datatype.ColLen) bool {
	if len(l) < 5 {
		return false
	}
	off := 0
	if l[0].AttLen == 4 {
		off = 1
	}
	return l[off].AttLen == 4 && l[off+1].AttLen == 4 && l[off+2].AttLen == 4
}

// fixedAuthidLayout：catalog.TableLayout 不可用时的兜底（PG12-18 与金仓 V8/V9 实测布局）
//
//	[oid 4][rolname 64][rolsuper..rolbypassrls 8×1][rolconnlimit 4][rolpassword text][rolvaliduntil 8]
func fixedAuthidLayout(isKB bool) []datatype.ColLen {
	_ = isKB
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
