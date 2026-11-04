package datatype

// v1.0.65 回归用例（F3）：aclitem WITH GRANT OPTION（高 16 位）与 ACL_SET 's'。
// 上游依据：PG acl.h ACLITEM_GET_GOPTIONS(item) = (item).ai_privs >> 16，
// aclitemout 对 grant option 权限逐位输出 "字符*"；ACL_ALL_RIGHTS_STR = "arwdDxtXUCTcsAm"
//（位 12 ACL_SET 小写 's'、位 13 ACL_ALTER_SYSTEM 大写 'A'，PG9.5~PG18 一致）。

// B8 修复（v1.0.66）：AclItem 内存布局按 PG acl.h 为 [ai_grantee][ai_grantor][ai_privs]，
// grantee 在前。v1.0.65 的助手写成 [grantor][grantee]，与实现共享同一错误布局，
// 自洽通过、掩盖缺陷（跨角色授权项 grantor/grantee 互换、PUBLIC 授权项静默丢失）。

import (
	"encoding/binary"
	"testing"
)

// aclitemBytes 按 PG 内存布局构造 AclItem：grantee 在前、grantor 居中、privs 在后。
func aclitemBytes(grantee, grantor, privs uint32) []byte {
	b := make([]byte, 12)
	binary.LittleEndian.PutUint32(b[0:], grantee)
	binary.LittleEndian.PutUint32(b[4:], grantor)
	binary.LittleEndian.PutUint32(b[8:], privs)
	return b
}

// GRANT SELECT, UPDATE ON t TO alice WITH GRANT OPTION（grantor=bob）：
// 低 16 位 r|w = 0b0110，高 16 位 r 的 grant option = 1<<17。
// aclitemout 规则：priv 输出字母、goption 仅输出 '*'（同位穿插）→ "r*w"。
func TestAclitemGrantOption(t *testing.T) {
	old := roleMapSnapshot()
	SetRoleMap(map[uint32]string{10: "alice", 20: "bob"})
	t.Cleanup(func() { SetRoleMap(old) })

	got := decodeAclitem(aclitemBytes(10, 20, 0x06|1<<17))
	if want := "alice=r*w/bob"; got != want {
		t.Fatalf("grant option 丢失: got %q want %q", got, want)
	}
}

// 普通授权（无 grant option）不受影响。
func TestAclitemPlainPrivs(t *testing.T) {
	old := roleMapSnapshot()
	SetRoleMap(map[uint32]string{10: "alice", 20: "bob"})
	t.Cleanup(func() { SetRoleMap(old) })

	got := decodeAclitem(aclitemBytes(10, 20, 0x06))
	if want := "alice=rw/bob"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// B8 回归：跨角色授权项 grantor/grantee 不得互换。
// 内存里 grantee=10(alice)、grantor=20(bob)，导出必须是 alice=arw/bob；
// 旧实现按 [grantor][grantee] 读取会导出成 bob=arw/alice。
func TestAclitemCrossRoleLayout(t *testing.T) {
	old := roleMapSnapshot()
	SetRoleMap(map[uint32]string{10: "alice", 20: "bob"})
	t.Cleanup(func() { SetRoleMap(old) })

	got := decodeAclitem(aclitemBytes(10, 20, 0x07)) // a|r|w
	if want := "alice=arw/bob"; got != want {
		t.Fatalf("grantee/grantor 布局颠倒: got %q want %q", got, want)
	}
}

// B8 回归：PUBLIC 授权项（grantee=0）必须导出为 "=r/grantor"，
// 不得误读成具名角色（旧实现把 grantor 读到 grantee 位 → "alice=r"，
// 回灌后 PUBLIC 权限静默丢失）。
func TestAclitemPublicGrantee(t *testing.T) {
	old := roleMapSnapshot()
	SetRoleMap(map[uint32]string{10: "alice", 20: "bob"})
	t.Cleanup(func() { SetRoleMap(old) })

	got := decodeAclitem(aclitemBytes(0, 10, 1<<1)) // grantee=0(PUBLIC), grantor=alice, r
	if want := "=r/alice"; got != want {
		t.Fatalf("PUBLIC 授权项丢失: got %q want %q", got, want)
	}
}

// 位 12 ACL_SET 必须输出小写 's'（acl.h ACL_SET_CHR='s'，PG9.5~PG18 一致；
// aclitem_in 的模式字符表不含大写 'S'，导出大写会被 "invalid mode character" 拒绝）。
func TestAclitemSetPrivChar(t *testing.T) {
	old := roleMapSnapshot()
	SetRoleMap(map[uint32]string{10: "alice"})
	t.Cleanup(func() { SetRoleMap(old) })

	got := decodeAclitem(aclitemBytes(10, 0, 1<<12))
	if want := "alice=s"; got != want {
		t.Fatalf("ACL_SET 字符错误: got %q want %q", got, want)
	}
}

// 位 13 ACL_ALTER_SYSTEM（PG15+）仍为大写 'A'。
func TestAclitemAlterSystemPrivChar(t *testing.T) {
	old := roleMapSnapshot()
	SetRoleMap(map[uint32]string{10: "alice"})
	t.Cleanup(func() { SetRoleMap(old) })

	got := decodeAclitem(aclitemBytes(10, 0, 1<<13))
	if want := "alice=A"; got != want {
		t.Fatalf("ACL_ALTER_SYSTEM 字符错误: got %q want %q", got, want)
	}
}

// ---------- B11：PG17+ 16 字节 AclItem 布局 ----------
// PG17 起 AclMode 扩为 64 位：[grantee][grantor][privs(4B)][goptions(4B)]，
// pg_type typlen=16/typalign='d'；grant option 不再在 privs 高 16 位。

func aclitemBytes16(grantee, grantor, privs, goptions uint32) []byte {
	b := make([]byte, 16)
	binary.LittleEndian.PutUint32(b[0:], grantee)
	binary.LittleEndian.PutUint32(b[4:], grantor)
	binary.LittleEndian.PutUint32(b[8:], privs)
	binary.LittleEndian.PutUint32(b[12:], goptions)
	return b
}

// PG17 单元素：{bob=r*/postgres}（grant option 在第 4 字段）。
func TestAclitemPg17Layout(t *testing.T) {
	old := roleMapSnapshot()
	SetRoleMap(map[uint32]string{10: "postgres", 42501: "bob"})
	t.Cleanup(func() { SetRoleMap(old) })

	got := decodeAclitem(aclitemBytes16(42501, 10, 1<<1, 1<<1))
	if want := "bob=r*/postgres"; got != want {
		t.Fatalf("PG17 布局 grant option 丢失: got %q want %q", got, want)
	}
}

// PG17 无 grant option：{alice=ar/postgres}，第 4 字段为 0。
func TestAclitemPg17NoGoption(t *testing.T) {
	old := roleMapSnapshot()
	SetRoleMap(map[uint32]string{10: "postgres", 42500: "alice"})
	t.Cleanup(func() { SetRoleMap(old) })

	got := decodeAclitem(aclitemBytes16(42500, 10, 0x03, 0))
	if want := "alice=ar/postgres"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// PG17 多元素数组（relacl 典型形态）：{owner=arwdDxtm/owner, alice=ar/owner,
// bob=r*/owner, =r/owner}——16B 元素紧密排列，第 2 个元素起不得错位
// （v1.0.65 按 12B 步长解析时此处产出垃圾）。
func TestDecodeArrayAclitemPg17Stride(t *testing.T) {
	oldDefs, oldDec := DynamicTypeDefs, DynamicDecoders
	old := roleMapSnapshot()
	SetRoleMap(map[uint32]string{10: "postgres", 42500: "alice", 42501: "bob"})
	DynamicTypeDefs = map[uint32]*TypeDef{
		ACLITEMOID: {OID: ACLITEMOID, Name: "aclitem", Len: 16, Align: 'd', Type: 'b'},
	}
	t.Cleanup(func() {
		DynamicTypeDefs, DynamicDecoders = oldDefs, oldDec
		SetRoleMap(old)
	})

	// 1 维数组头：ndim=1, dataoffset=0, elemtype=1033, dim=4, lbound=1 → 20B
	hdr := make([]byte, 20)
	binary.LittleEndian.PutUint32(hdr[0:], 1)
	binary.LittleEndian.PutUint32(hdr[4:], 0)
	binary.LittleEndian.PutUint32(hdr[8:], ACLITEMOID)
	binary.LittleEndian.PutUint32(hdr[12:], 4)
	binary.LittleEndian.PutUint32(hdr[16:], 1)
	body := append([]byte{}, aclitemBytes16(10, 10, 0x407F, 0)...)
	body = append(body, aclitemBytes16(42500, 10, 0x03, 0)...)
	body = append(body, aclitemBytes16(42501, 10, 0x02, 0x02)...)
	body = append(body, aclitemBytes16(0, 10, 0x02, 0)...)
	// 外层 4B varlena 头（未压缩：word = total<<2）
	arr := append(hdr, body...)
	vl := make([]byte, 4)
	binary.LittleEndian.PutUint32(vl, uint32(len(arr)+4)<<2)
	got := DecodeArray(append(vl, arr...))
	want := "{postgres=arwdDxtm/postgres,alice=ar/postgres,bob=r*/postgres,=r/postgres}"
	if got != want {
		t.Fatalf("PG17 aclitem[] 步长错误: got %q want %q", got, want)
	}
}

// PG16- 12B 多元素数组回归：步长 12，goption 在 privs 高 16 位。
func TestDecodeArrayAclitemLegacyStride(t *testing.T) {
	oldDefs, oldDec := DynamicTypeDefs, DynamicDecoders
	old := roleMapSnapshot()
	SetRoleMap(map[uint32]string{10: "postgres", 42500: "alice"})
	DynamicTypeDefs = map[uint32]*TypeDef{
		ACLITEMOID: {OID: ACLITEMOID, Name: "aclitem", Len: 12, Align: 'i', Type: 'b'},
	}
	t.Cleanup(func() {
		DynamicTypeDefs, DynamicDecoders = oldDefs, oldDec
		SetRoleMap(old)
	})

	hdr := make([]byte, 20)
	binary.LittleEndian.PutUint32(hdr[0:], 1)
	binary.LittleEndian.PutUint32(hdr[4:], 0)
	binary.LittleEndian.PutUint32(hdr[8:], ACLITEMOID)
	binary.LittleEndian.PutUint32(hdr[12:], 2)
	binary.LittleEndian.PutUint32(hdr[16:], 1)
	e1 := make([]byte, 12)
	binary.LittleEndian.PutUint32(e1[0:], 10)
	binary.LittleEndian.PutUint32(e1[4:], 10)
	binary.LittleEndian.PutUint32(e1[8:], 0x03|1<<17) // ar + goption(a)
	e2 := make([]byte, 12)
	binary.LittleEndian.PutUint32(e2[0:], 42500)
	binary.LittleEndian.PutUint32(e2[4:], 10)
	binary.LittleEndian.PutUint32(e2[8:], 0x02)
	arr := append(append(hdr, e1...), e2...)
	vl := make([]byte, 4)
	binary.LittleEndian.PutUint32(vl, uint32(len(arr)+4)<<2)
	got := DecodeArray(append(vl, arr...))
	want := "{postgres=ar*/postgres,alice=r/postgres}"
	if got != want {
		t.Fatalf("12B aclitem[] 步长错误: got %q want %q", got, want)
	}
}

// B12：金仓 ACL 位 12 = 'p'（金仓自有权限），PG 位 12 = 's'。
func TestAclitemKbPrivChar(t *testing.T) {
	old := roleMapSnapshot()
	SetRoleMap(map[uint32]string{10: "system"})
	t.Cleanup(func() { SetRoleMap(old) })

	KbEngine.Store(true)
	t.Cleanup(func() { KbEngine.Store(false) })

	// owner 默认 ACL（金仓）：bits 0-6 + bit12 → arwdDxtp
	got := decodeAclitem(aclitemBytes(10, 10, 0x7F|1<<12))
	if want := "system=arwdDxtp/system"; got != want {
		t.Fatalf("金仓位 12 字符错误: got %q want %q", got, want)
	}
	// 同一位图在 PG 下输出 's'
	KbEngine.Store(false)
	got = decodeAclitem(aclitemBytes(10, 10, 0x7F|1<<12))
	if want := "system=arwdDxts/system"; got != want {
		t.Fatalf("PG 位 12 字符错误: got %q want %q", got, want)
	}
}
