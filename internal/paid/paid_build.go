//go:build tde

// TDE 版解密器构建（原 main.go BuildPaidDecryptor 实现，随 -tags tde 编译）。
package paid

import (
	"path/filepath"
	"pg2sql/internal/config"
	"pg2sql/internal/engine"
	"pg2sql/internal/logx"
	"pg2sql/internal/util"
	"pg2sql/tde"
)

// BuildPaidDecryptor：根据 --enckey / --wallet-password / --wallet-dir 构造页解密器。
// 返回 nil 表示未启用 TDE 解密。
// 表加密（--enckey）：密钥 = 字符串 + 0x00 填 16B，ECB 解页 offset64+。
// 表空间加密（--wallet-password）：钱包链 KEK→MK→TSPK；钱包目录自动探测
//
//	（--wallet-dir > --datadir/.wallet > 数据根/.wallet）；tspOID 取表所属表空间。
func BuildPaidDecryptor(o *config.Options, tm *engine.TableMeta, dbDir string) engine.PageDecryptor {
	if o.EncKey != "" {
		d := tde.NewTableDecryptor(o.EncKey)
		if o.Verbose {
			logx.Logf("TDE 解密: 表加密 (SM4-ECB, enckey=%s…)", util.MaskKey(o.EncKey))
		}
		return d
	}
	if o.WalletPassword == "" && o.WalletDir == "" {
		return nil
	}
	// 表空间加密：需要表位于非默认表空间
	tspOID := uint32(0)
	if tm != nil {
		tspOID = tm.TableSpace
	}
	if tspOID == 0 {
		// v1.0.60 修复（P1-7）：此前 tspOID==0（默认表空间）直接返回 nil 并跳过解密。
		// 但 pg_largeobject 等目录对象绝大多数就在默认表空间，结果表空间加密库的
		// 大对象一律"解密跳过"→ 密文当明文导出。默认表空间同样有 TSPK 条目，
		// 改用 pg_default 的固定 OID(1663) 查钱包；查不到才降级（由调用方决定是否硬报错）。
		tspOID = 1663 // pg_default
		if o.Verbose {
			logx.Logf("TDE: 表未标注表空间，按默认表空间 pg_default(1663) 查表空间密钥")
		}
	}
	walletDir := o.WalletDir
	if walletDir == "" {
		// 自动探测：--datadir/.wallet 或数据根/.wallet
		if o.Datadir != "" {
			walletDir = filepath.Join(o.Datadir, ".wallet")
		} else if root := config.ResolveDataRoot(o); root != "" {
			walletDir = filepath.Join(root, ".wallet")
		}
	}
	if walletDir == "" || !util.FileExists(filepath.Join(walletDir, "masterkey.kr")) {
		logx.Logf("警告: 未找到钱包目录 %s（需 masterkey.kr/tspkey.kr），TDE 表空间解密跳过", walletDir)
		return nil
	}
	d := tde.NewTablespaceDecryptor(walletDir, tspOID)
	if o.WalletPassword != "" {
		// 密码校验（密钥链离线推导本身不需要密码）
		if wc, err := tde.ParseWallet(walletDir, tspOID); err == nil {
			if !wc.VerifyPassword(walletDir, o.WalletPassword) {
				logx.Logf("警告: 钱包密码校验失败（--wallet-password），密钥链仍按离线推导继续")
			}
		}
	}
	if o.Verbose {
		logx.Logf("TDE 解密: 表空间加密 (钱包=%s, tspOID=%d)", walletDir, tspOID)
	}
	return d
}
