//go:build darwin

package download

import (
	"os/exec"
	"strings"
)

// platformSystemProxy 读取 macOS 的系统代理声明（scutil --proxy）。
func platformSystemProxy() (systemProxySettings, string) {
	output, err := exec.Command("scutil", "--proxy").Output()
	if err != nil {
		return systemProxySettings{}, "无法读取系统代理设置"
	}
	values := map[string]string{}
	list := []string{}
	inList := false
	for _, line := range strings.Split(string(output), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "ExceptionsList") {
			inList = true
			continue
		}
		if inList {
			if trimmed == "}" {
				inList = false
				continue
			}
			if _, value, ok := strings.Cut(trimmed, ":"); ok {
				list = append(list, strings.TrimSpace(value))
			}
			continue
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	declaration := func(scheme, enable, host, port string) string {
		if values[enable] != "1" || values[host] == "" {
			return ""
		}
		address := values[host]
		if values[port] != "" {
			address += ":" + values[port]
		}
		return scheme + "=" + address
	}
	httpAddress := declaration("http", "HTTPEnable", "HTTPProxy", "HTTPPort")
	httpsAddress := declaration("https", "HTTPSEnable", "HTTPSProxy", "HTTPSPort")
	declarationParts := []string{}
	if httpAddress != "" {
		declarationParts = append(declarationParts, httpAddress)
	}
	if httpsAddress != "" {
		declarationParts = append(declarationParts, httpsAddress)
	}
	settings := systemProxySettings{
		available: true,
		enabled:   httpAddress != "" || httpsAddress != "" || values["ProxyAutoConfigEnable"] == "1" || values["ProxyAutoDiscoveryEnable"] == "1",
		server:    strings.Join(declarationParts, ";"),
		override:  strings.Join(list, ";"),
	}
	if values["ProxyAutoConfigEnable"] == "1" {
		settings.autoConfigURL = values["ProxyAutoConfigURLString"]
	}
	return settings, ""
}
