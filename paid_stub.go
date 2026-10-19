//go:build !tde

// 免费版（无 -tags tde）：加密解密功能编译期剔除。
// 本文件提供空实现：付费参数报错、解密器恒 nil、帮助不显示付费参数。
// 注意：本文件不得出现 "TDE"/"tde" 字面量（免费版二进制 strings 审计 0 处）。
package main

// parsePaidArg：免费版对付费参数直接报错（提示使用付费版）。
func parsePaidArg(a string, o *Options, next func() string) bool {
	errorExit(a + " 为付费版功能（加密离线解密），请使用支持此功能的版本")
	return false
}

// buildPaidDecryptor：免费版恒返回 nil（不启用任何解密）。
func buildPaidDecryptor(o *Options, tm *TableMeta, dbDir string) pageDecryptor {
	return nil
}

// paidHelpText：免费版帮助不显示付费参数。
func paidHelpText() string { return "" }

// paidVersionSuffix：免费版无版本后缀。
func paidVersionSuffix() string { return "" }
