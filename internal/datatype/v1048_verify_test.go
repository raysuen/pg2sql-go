package datatype

// v1.0.48 回归用例：P0 数组越界守卫 + P1 自定义类型数组解码 / 标识符转义。
// 这些用例直接构造 PG 磁盘格式的 varlena 字节，不依赖真实数据目录。

import (
	"encoding/binary"
	"strings"
	"testing"
)

// 固定一批 oid 模拟真实 PG17 夹具（见 auditfix_fixture.sql）：
//
//	_mood=16865 (typtype=b, typelem=16866, typarray=0)
//	mood =16866 (typtype=e, typarray=16865, typlen=4)
const (
	tMoodArrOID = 16865
	tMoodOID    = 16866
	mSAD        = 16868
	mOK         = 16870
	mHAPPY      = 16871
)

func withFakeTypes(t *testing.T) {
	t.Helper()
	oldDefs, oldDec, oldEnum := DynamicTypeDefs, DynamicDecoders, EnumMapSnapshot()
	DynamicTypeDefs = map[uint32]*TypeDef{
		tMoodArrOID: {OID: tMoodArrOID, Name: "_mood", Type: 'b', Len: -1, Elem: tMoodOID, Array: 0},
		tMoodOID:    {OID: tMoodOID, Name: "mood", Type: 'e', Len: 4, Elem: 0, Array: tMoodArrOID},
	}
	DynamicDecoders = map[uint32]func([]byte) string{tMoodArrOID: DecodeArray}
	SetEnumMap(map[uint32][]EnumMember{
		tMoodOID: {
			{MemberOID: mSAD, Label: "sad", SortOrder: 1},
			{MemberOID: mOK, Label: "ok", SortOrder: 2},
			{MemberOID: mHAPPY, Label: "happy", SortOrder: 3},
		},
	})
	t.Cleanup(func() {
		DynamicTypeDefs, DynamicDecoders = oldDefs, oldDec
		SetEnumMap(oldEnum)
	})
}

// varlena：PG 短头 = (总长<<1)|1，总长含 1 字节头
func wrapShort(payload []byte) []byte {
	total := len(payload) + 1
	return append([]byte{byte(total<<1) | 1}, payload...)
}

func arrPayload(ndim, dataoffset int, elemOID uint32, dims []int32, lbs []int32, bitmap []byte, elems []byte) []byte {
	p := make([]byte, 12+8*ndim)
	binary.LittleEndian.PutUint32(p[0:], uint32(ndim))
	binary.LittleEndian.PutUint32(p[4:], uint32(dataoffset))
	binary.LittleEndian.PutUint32(p[8:], elemOID)
	for i := 0; i < ndim; i++ {
		binary.LittleEndian.PutUint32(p[12+4*i:], uint32(dims[i]))
		binary.LittleEndian.PutUint32(p[12+4*ndim+4*i:], uint32(lbs[i]))
	}
	p = append(p, bitmap...)
	// 元素区起点对齐到 4
	for len(p)%4 != 0 {
		p = append(p, 0)
	}
	return append(p, elems...)
}

// P1-F1：一维枚举数组（含 NULL 元素）
func TestEnumArrayDecode1D(t *testing.T) {
	withFakeTypes(t)
	// {sad, NULL, happy}：位图 0b101（bit=1 表示该元素非 NULL）
	var elems []byte
	for _, o := range []uint32{mSAD, mHAPPY} {
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, o)
		elems = append(elems, b...)
	}
	raw := wrapShort(arrPayload(1, 25, tMoodOID, []int32{3}, []int32{1}, []byte{0b101}, elems))
	got := DecodeValue(tMoodArrOID, raw)
	if want := `{sad,NULL,happy}`; got != want {
		t.Fatalf("枚举数组解码错误: got %q want %q", got, want)
	}
}

// P1-F1：二维枚举数组
func TestEnumArrayDecode2D(t *testing.T) {
	withFakeTypes(t)
	var elems []byte
	for _, o := range []uint32{mSAD, mOK, mHAPPY, mSAD} {
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, o)
		elems = append(elems, b...)
	}
	raw := wrapShort(arrPayload(2, 0, tMoodOID, []int32{2, 2}, []int32{1, 1}, nil, elems))
	got := DecodeValue(tMoodArrOID, raw)
	if want := `{{sad,ok},{happy,sad}}`; got != want {
		t.Fatalf("二维枚举数组解码错误: got %q want %q", got, want)
	}
}

// P1-F1：空枚举数组 / 全 NULL 枚举数组
func TestEnumArrayDecodeEmptyAndAllNull(t *testing.T) {
	withFakeTypes(t)
	// 空数组：PG 用 ndim=0 表示
	empty := wrapShort([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	if got := DecodeValue(tMoodArrOID, empty); got != "{}" {
		t.Fatalf("空枚举数组: got %q want {}", got)
	}
	// 全 NULL 数组元素区为空，dataoffset 指向末尾之外
	raw := wrapShort(arrPayload(1, 25, tMoodOID, []int32{2}, []int32{1}, []byte{0b00}, nil))
	if got := DecodeValue(tMoodArrOID, raw); got != "{NULL,NULL}" {
		t.Fatalf("全 NULL 枚举数组: got %q want {NULL,NULL}", got)
	}
}

// UserArrayElemOID：旧判据 td.Array==oid 恒不成立，此处锁死新判据
func TestUserArrayElemOID(t *testing.T) {
	withFakeTypes(t)
	if eo, ok := UserArrayElemOID(tMoodArrOID); !ok || eo != tMoodOID {
		t.Fatalf("UserArrayElemOID(_mood) = %d,%v want %d,true", eo, ok, tMoodOID)
	}
	if _, ok := UserArrayElemOID(tMoodOID); ok {
		t.Fatalf("UserArrayElemOID(mood) 不应判定为数组类型")
	}
}

// P0-A1：dims 数组被截断（ndim=3 但只有 2 个 dim）→ 兜底不 panic
func TestArrayTruncatedDimsNoPanic(t *testing.T) {
	p := make([]byte, 20)
	binary.LittleEndian.PutUint32(p[0:], 3)  // ndim=3
	binary.LittleEndian.PutUint32(p[4:], 0)  // dataoffset=0
	binary.LittleEndian.PutUint32(p[8:], 23) // elemtype int4
	binary.LittleEndian.PutUint32(p[12:], 2) // dims[0]
	binary.LittleEndian.PutUint32(p[16:], 2) // dims[1] —— dims[2]/lbs 区已缺失
	got := DecodeArray(wrapShort(p))
	if !strings.HasPrefix(got, `\x`) {
		t.Fatalf("截断数组应退回 \\x 兜底，got %q", got)
	}
}

// P0-A2：dims 连乘溢出（6 维各 1<<30）→ 兜底不 panic
func TestArrayHugeDimsNoPanic(t *testing.T) {
	p := make([]byte, 12+8*6)
	binary.LittleEndian.PutUint32(p[0:], 6)
	binary.LittleEndian.PutUint32(p[4:], 0)
	binary.LittleEndian.PutUint32(p[8:], 23)
	for i := 0; i < 6; i++ {
		binary.LittleEndian.PutUint32(p[12+4*i:], 1<<30)
	}
	got := DecodeArray(wrapShort(p))
	if !strings.HasPrefix(got, `\x`) {
		t.Fatalf("dims 溢出应退回 \\x 兜底，got %q", got)
	}
}

// P0-A5：元素区起点越界（dims 声称 10 个元素但 payload 只有 16 字节）
func TestArrayElemPosOverrunNoPanic(t *testing.T) {
	p := make([]byte, 16)
	binary.LittleEndian.PutUint32(p[0:], 1)
	binary.LittleEndian.PutUint32(p[4:], 0)
	binary.LittleEndian.PutUint32(p[8:], 25) // text 元素（varlena）
	binary.LittleEndian.PutUint32(p[12:], 10)
	raw := wrapShort(p) // 头部丢掉 lbs，制造越界
	if got := DecodeArray(raw); got == "" {
		t.Fatalf("越界数组不应返回空串")
	}
}
