//go:build !windows && !darwin && !linux

package download

// platformPACAvailable 报告当前平台是否支持 PAC 求值。
func platformPACAvailable() bool { return false }

// platformPACProxy 在非 Windows 平台不提供 PAC 求值。
func platformPACProxy(pacURL, target string) (string, error) {
	return "", errPACUnavailable
}
