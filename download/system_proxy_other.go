//go:build !windows && !darwin && !linux

package download

// systemProxySettings 在非 Windows 平台不提供系统代理读取；字段与 Windows
// 实现保持一致。
type systemProxySettings struct {
	enabled       bool
	server        string
	autoConfigURL string
	override      string
	available     bool
}

func platformSystemProxy() (systemProxySettings, string) {
	return systemProxySettings{}, "当前平台不支持读取系统代理设置"
}
