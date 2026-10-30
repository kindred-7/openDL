//go:build windows

package download

import "strings"

// extendedPath 给超长路径加 Windows 扩展前缀，绕过 MAX_PATH 限制；已带
// 前缀的路径保持原样。
func extendedPath(path string) string {
	cleaned := strings.ReplaceAll(path, "/", "\\")
	if strings.HasPrefix(cleaned, "\\\\?\\") {
		return cleaned
	}
	if strings.HasPrefix(cleaned, "\\") {
		return "\\\\?\\UNC\\" + strings.TrimPrefix(cleaned, "\\")
	}
	return "\\\\?\\" + cleaned
}
