//go:build !windows

package main

import "fmt"

const proxyPasswordStorageAvailable = false

func encryptProxySecret([]byte) ([]byte, error) {
	return nil, fmt.Errorf(tr("当前平台暂不支持安全保存代理密码"))
}

func decryptProxySecret([]byte) ([]byte, error) {
	return nil, fmt.Errorf(tr("当前平台无法读取 Windows 保存的代理密码"))
}
