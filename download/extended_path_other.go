//go:build !windows

package download

// extendedPath 在其他平台是恒等映射。
func extendedPath(path string) string { return path }
