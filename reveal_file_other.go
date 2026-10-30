//go:build !windows

package main

// 所在文件夹定位：其他平台暂由调用方退回打开目录。

func revealFile(path string) bool { return false }
