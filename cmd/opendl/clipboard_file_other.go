//go:build !windows

package main

// 复制/剪切文件：其他平台暂缺统一的粘贴移动提示，只写文件列表，粘贴为复制。

import "github.com/egoist/mygo/transfer"

func fileClipboardData(path string, cut bool) (transfer.Data, error) {
	return transfer.FileData(path)
}
