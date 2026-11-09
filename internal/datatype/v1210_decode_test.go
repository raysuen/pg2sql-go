package datatype

// v1.2.10 数据面回归：域 over 枚举 / 域 over 自定义数组 此前无解码器，
// 整列退化 \x 十六进制（外部审计报告第十节 #1 的数据面同族缺口）。

import (
	"encoding/binary"
	"testing"
)

func withV1210DecodeFixture(t *testing.T) {
	t.Helper()
	oldDefs, oldDec, oldEnum, oldAttrs := DynamicTypeDefs, DynamicDecoders, EnumMapSnapshot(), CompositeAttrs
	compT := uint32(93001)
	compTArr := uint32(93002)
	c1 := uint32(93011)
	mood := uint32(93041)
	dEnum := uint32(93051)
	dEnum2 := uint32(93052)
	dArr := uint32(93061)
	DynamicTypeDefs = map[uint32]*TypeDef{
		compT:    {OID: compT, Name: "comp_t", Type: 'c', TypRelid: 93090, Array: compTArr},
		compTArr: {OID: compTArr, Name: "_comp_t", Type: 'b', Elem: compT, Array: compTArr},
		c1:       {OID: c1, Name: "c1", Type: 'd', Base: 18},
		mood:     {OID: mood, Name: "mood", Type: 'e', Len: 4},
		dEnum:    {OID: dEnum, Name: "d_enum", Type: 'd', Base: mood},
		dEnum2:   {OID: dEnum2, Name: "d_enum2", Type: 'd', Base: dEnum},
		dArr:     {OID: dArr, Name: "d_arr", Type: 'd', Base: compTArr},
	}
	CompositeAttrs = map[uint32][]*AttrRow{
		93090: {
			{AttName: "a", AttTypID: 18},
			{AttName: "n", AttTypID: 23},
		},
	}
	SetEnumMap(map[uint32][]EnumMember{
		mood: {
			{MemberOID: 93042, Label: "happy", SortOrder: 1},
			{MemberOID: 93043, Label: "sad", SortOrder: 2},
		},
	})
	initDecodersForTest(t, compT, compTArr, c1, mood, dEnum, dEnum2, dArr)
	t.Cleanup(func() {
		DynamicTypeDefs, DynamicDecoders, CompositeAttrs = oldDefs, oldDec, oldAttrs
		SetEnumMap(oldEnum)
	})
}

// initDecodersForTest：绕过目录 IO，按与 initDynamicDecoders 相同的顺序装配
// DynamicDecoders（先数组后域，v1.2.10 的顺序修正）。
func initDecodersForTest(t *testing.T, oids ...uint32) {
	t.Helper()
	dd := map[uint32]func([]byte) string{}
	for _, oid := range oids {
		if _, ok := UserArrayElemOID(oid); ok {
			dd[oid] = DecodeArray
		}
	}
	for _, oid := range oids {
		td := DynamicTypeDefs[oid]
		if td != nil && td.Type == 'd' && td.Base != 0 {
			if dec := resolveDecoder(td.Base, DynamicTypeDefs, map[uint32]string{}, dd); dec != nil {
				dd[oid] = dec
			}
		}
	}
	DynamicDecoders = dd
}

// 域 over 域 over 枚举：磁盘 4 字节成员 OID → 标签
func TestV1210DecodeDomainOverEnum(t *testing.T) {
	withV1210DecodeFixture(t)
	raw := make([]byte, 4)
	binary.LittleEndian.PutUint32(raw, 93042)
	if got := DecodeValue(93052, raw); got != "happy" {
		t.Fatalf("域 over 枚举解码错误: got %q want happy", got)
	}
	binary.LittleEndian.PutUint32(raw, 93043)
	if got := DecodeValue(93051, raw); got != "sad" {
		t.Fatalf("单层枚举域解码错误: got %q want sad", got)
	}
}
