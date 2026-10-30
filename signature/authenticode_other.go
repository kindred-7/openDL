//go:build !windows

package signature

import "openDL/i18n"

const Available = false

func VerifyFile(path string) error {
	return i18n.NewError("当前平台不支持 Windows 文件数字签名验证")
}
