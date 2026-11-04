package objects

// v1.0.65 回归用例（F10/F12）：空大对象输出 lo_create；hex 走 decode() 不依赖 SCS。

import (
	"strings"
	"testing"
)

func TestEmptyLargeObjectEmitsLoCreate(t *testing.T) {
	e := &LargeObjectEntry{OID: 4242}
	var sb strings.Builder
	n, err := WriteLargeObjectsSQL(&sb, []*LargeObjectEntry{e}, nil)
	if err != nil {
		t.Fatalf("写出失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("计数错误: %d", n)
	}
	if !strings.Contains(sb.String(), "SELECT pg_catalog.lo_create(4242);") {
		t.Fatalf("空大对象应输出 lo_create: %s", sb.String())
	}
	if strings.Contains(sb.String(), "lo_from_bytea") {
		t.Fatalf("空大对象不应走 lo_from_bytea: %s", sb.String())
	}
}

func TestLoFromByteaUsesDecode(t *testing.T) {
	e := &LargeObjectEntry{OID: 7, DataHex: "00ff7f"}
	var sb strings.Builder
	if _, err := WriteLargeObjectsSQL(&sb, []*LargeObjectEntry{e}, nil); err != nil {
		t.Fatalf("写出失败: %v", err)
	}
	if !strings.Contains(sb.String(), `lo_from_bytea(7, decode('00ff7f','hex'))`) {
		t.Fatalf("应使用 decode('hex') 形式（SCS 无关）: %s", sb.String())
	}
	if strings.Contains(sb.String(), `'\x00ff7f'`) {
		t.Fatalf("不应再输出 '\\x..' 字面量: %s", sb.String())
	}
}
