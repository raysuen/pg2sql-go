//go:build tde

// TDE 版（-tags tde）：透明加密离线解密功能入口。
package paid

import (
	"pg2sql/internal/config"
)

// ParsePaidArg：解析 TDE 三个参数。
func ParsePaidArg(a string, o *config.Options, next func() string) bool {
	switch a {
	case "--enckey":
		o.EncKey = next()
	case "--wallet-password":
		o.WalletPassword = next()
	case "--wallet-dir":
		o.WalletDir = next()
	default:
		return false
	}
	return true
}

// PaidHelpText：TDE 版帮助补充三个参数说明。
func PaidHelpText() string {
	return `
  --enckey KEY           金仓 TDE 表加密密钥（CREATE TABLE ... ENCRYPTED BY 'KEY'；页 offset64+ SM4-ECB 离线解密，TOAST 同密钥）
  --wallet-password PW   金仓 TDE 表空间加密钱包密码（表空间 WITH (encryption=true) 场景；密钥链 KEK→MK→TSPK 离线推导本不需密码，密码仅做 SM3 校验并告警）
  --wallet-dir DIR       钱包目录（默认自动探测 <PGDATA>/.wallet 或数据根/.wallet；需 masterkey.kr/tspkey.kr，多表空间条目自动匹配）`
}

// PaidVersionSuffix：TDE 版版本标识。
func PaidVersionSuffix() string { return " (TDE)" }
