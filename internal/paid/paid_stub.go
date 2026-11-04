//go:build !tde

// 免费版（无 -tags tde）：加密解密功能编译期剔除。
// 本文件提供空实现：付费参数报错、解密器恒 nil、帮助不显示付费参数。
// 注意：本文件不得出现 "TDE"/"tde" 字面量（免费版二进制 strings 审计 0 处）。
package paid

import (
	"errors"
	"pg2sql/internal/config"
	"pg2sql/internal/engine"
	"pg2sql/internal/logx"
)

// ParsePaidArg：免费版对付费参数直接报错（提示使用付费版）。
func ParsePaidArg(a string, o *config.Options, next func() string) bool {
	logx.ErrorExit(a + " 为付费版功能（加密离线解密），请使用支持此功能的版本")
	return false
}

// BuildPaidDecryptor：免费版恒返回 nil（不启用任何解密）。
func BuildPaidDecryptor(o *config.Options, tm *engine.TableMeta, dbDir string) engine.PageDecryptor {
	return nil
}

// PaidHelpText：免费版帮助不显示付费参数。
func PaidHelpText() string { return "" }

// PaidVersionSuffix：免费版无版本后缀。
func PaidVersionSuffix() string { return "" }

// ErrKeyUnverified：免费版占位（永不会返回，仅为与 tde 构建保持同一 API 面）。
var ErrKeyUnverified = errors.New("TDE 密钥校验未取得证据")

// VerifyDecryptorKey：免费版无解密器（恒 nil），密钥校验直接放行。
func VerifyDecryptorKey(dec engine.PageDecryptor, path string, ps int, ncols int) error { return nil }
