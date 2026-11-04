//go:build tde

// TDE 密钥正确性校验（v1.0.46）
//
// 背景：金仓 TDE 页 offset64 起为 SM4-ECB 密文，页头 0-63B 为明文。密钥错误时 SM4-ECB
// 仍会"成功"解出一页随机字节，旧实现照常按行解析 → 静默输出少量乱码行（实测 --enckey wrongkey
// 得到 2 行垃圾数据、exit=0，且无任何告警），属于数据正确性级别的缺陷。
//
// 判定方法：解密首若干非空数据页，用【严格元组自洽性】验证——
//
//	① 行指针（ItemId）必须落在页内且长度 ≥ 24；
//	② 行内列数 natts = t_infomask2 & 0x07FF 必须 1 ≤ natts ≤ 表列数（含少量 ALTER 残留容差）；
//	③ t_hoff 必须等于按空值位图推出的对齐值（±8 容差），且不越出行长。
//
// 正确密钥下真实元组必然满足；错误密钥下 natts/t_hoff 为随机值，几乎不可能同时满足。
// 若"有行指针但零个合法元组"，即判定密钥错误并中止导出（不再产出乱码文件）。
package paid

import (
	"errors"
	"fmt"
	"io"
	"os"
	"pg2sql/internal/datatype"
	"pg2sql/internal/engine"
)

// VerifyDecryptorKey：校验解密器密钥；密钥明显错误时返回 error，调用方应终止导出。
func VerifyDecryptorKey(dec engine.PageDecryptor, path string, ps int, ncols int) error {
	if dec == nil || path == "" || ps < 1024 {
		return nil
	}
	checkedPages, itemids, validTuples := 0, 0, 0
	buf := make([]byte, ps)
	// v1.2.2 修复（BUG-6）：旧实现每段只扫前 32 页，且终判据为
	// `itemids > 0 && validTuples == 0`。若前 32 页恰好全是空页/全零页
	// （大表 DELETE 后、稀疏表、表头段已回收等），itemids 恒为 0 → 判据
	// 不成立 → 直接 return nil，「错误密钥」被静默放行，后续导出乱码。
	//
	// 现已改为：跨所有段累计扫描，直到出现「第一个含行指针的页」为止
	// （不再按段重置 32 页上限），并单独统计「读到密文页却解不出合法元组」
	// 的次数。只要观测到任一页存在行指针，就用 validTuples 做强判据。
	const maxPagesPerSeg = 32  // 单段最多扫多少页（控制启动开销）
	const maxTotalPages = 4096 // 总量上限（跨段累计，避免超大表拖慢启动）
	totalPages := 0
	// v1.2.2 修复（BUG-5 连带）：解密调用本身报错的页数。旧实现拿到 derr 就
	// `continue` 且不计数——若密文页全部无法解密（页结构异常/密钥长度不符），
	// checkedPages 与 itemids 恒为 0，函数一路落到末尾 `return nil` 放行，
	// 等于「一页都没解开也算密钥正确」，随后导出满屏乱码且无任何报错。
	decryptErrs := 0
	for _, seg := range engine.SegFiles(path) {
		f, err := os.Open(seg)
		if err != nil {
			continue
		}
		for p := 0; p < maxPagesPerSeg && totalPages < maxTotalPages; p++ {
			n, rerr := io.ReadFull(f, buf)
			if rerr != nil || n < ps {
				break
			}
			totalPages++
			if engine.ZeroPage(buf[:n]) {
				continue // 空闲页无密文，跳过
			}
			page := make([]byte, n)
			copy(page, buf[:n])
			if _, derr := dec.DecryptPage(page); derr != nil {
				decryptErrs++
				continue
			}
			checkedPages++
			lay := engine.TryStandardLayout(page, ps)
			if lay == nil {
				lay = engine.TryAutoLayout(page, ps)
			}
			if lay == nil {
				continue
			}
			for i := 0; lay.HeaderEnd+4*i+4 <= int(lay.Lower); i++ {
				raw := uint32(datatype.U16(page, lay.HeaderEnd+4*i)) | uint32(datatype.U16(page, lay.HeaderEnd+4*i+2))<<16
				if (raw>>15)&0x03 != engine.ITEMID_NORMAL {
					continue
				}
				off := int(raw & 0x7FFF)
				ln := int((raw >> 17) & 0x7FFF)
				itemids++
				if ln < 24 || off < 24 || off+ln > len(page) {
					continue
				}
				if engine.TupleSane(page[off:off+ln], ncols) {
					validTuples++
				}
			}
			if validTuples > 0 {
				f.Close()
				return nil // 已确认密钥可用，立即放行（不额外扫描，保持启动开销恒定）
			}
		}
		f.Close()
		if validTuples > 0 {
			break
		}
	}
	if itemids > 0 && validTuples == 0 {
		return fmt.Errorf("TDE 解密失败：密钥不正确（已解密 %d 页、%d 个行指针，无一能解析为合法元组）。"+
			"请核对 --enckey 是否与 CREATE TABLE ... ENCRYPTED BY 的密钥一致，或 --wallet-dir/--wallet-password 是否指向正确钱包", checkedPages, itemids)
	}
	// v1.2.2（BUG-5 连带）：存在密文页但一页都解不开，且没有任何一页成功解密。
	// 这是明确的失败信号（页结构异常 / --enckey 长度不符 / 表并非该密钥所加密），
	// 不能落到末尾 `return nil` 静默放行。空表/全删表的所有页是 ZeroPage，在循环里
	// 就被 continue 掉了，decryptErrs 保持 0，不会误伤。
	if checkedPages == 0 && decryptErrs > 0 {
		return fmt.Errorf("TDE 解密失败：%d 个数据页全部无法解密（页结构异常或密钥不匹配）。"+
			"数据无法按加密表解析，请核对 --enckey 是否与加密时的密钥一致、"+
			"或 --wallet-dir/--wallet-password 是否指向正确钱包", decryptErrs)
	}
	// v1.2.2（BUG-6）：扫了若干页却一个行指针都没拿到（整段稀疏/空页），
	// 无法证明密钥正确。不中止（空表是合法场景），但返回可辨识的哨兵错误，
	// 由调用方降级为告警——避免「错误密钥被当成正确密钥」静默放行。
	if checkedPages > 0 && itemids == 0 {
		return ErrKeyUnverified
	}
	return nil
}

// ErrKeyUnverified：TDE 密钥校验未能取得证据（页已解密但无行指针）。
//
// 调用方应视为「告警」而非「失败」：空表 / 全表删除后导出是合法场景，
// 但也不能据此宣称密钥正确。v1.2.2 引入（BUG-6）。
var ErrKeyUnverified = errors.New("TDE 密钥校验未能取得证据：已解密数据页但未找到任何行指针" +
	"（表可能为空或已全表删除）；无法确认密钥是否正确，导出结果请自行复核")

// tupleSane / zeroPage 已下沉到 heapfile.go（v1.0.54：密文页判据在免费版同样需要）。
