//go:build linux

package download

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// gsettingsValue 读取一项 GNOME 网络代理设置；失败返回空。
func gsettingsValue(schema, key string) string {
	output, err := exec.Command("gsettings", "get", schema, key).Output()
	if err != nil {
		return ""
	}
	value := strings.TrimSpace(string(output))
	value = strings.TrimPrefix(value, "'")
	value = strings.TrimSuffix(value, "'")
	return strings.TrimSpace(value)
}


// kconfigUnescape 还原 KConfig 的十六进制转义。
func kconfigUnescape(value string) string {
	if !strings.Contains(value, "\\x") {
		return value
	}
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] == '\\' && i+3 < len(value) && value[i+1] == 'x' {
			if hi, ok1 := hexNibble(value[i+2]); ok1 {
				if lo, ok2 := hexNibble(value[i+3]); ok2 {
					out.WriteByte(hi<<4 | lo)
					i += 3
					continue
				}
			}
		}
		out.WriteByte(value[i])
	}
	return out.String()
}

func hexNibble(b byte) (byte, bool) {
	switch {
	case b >= '0' && b <= '9':
		return b - '0', true
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10, true
	case b >= 'A' && b <= 'F':
		return b - 'A' + 10, true
	}
	return 0, false
}

// kioslavercProxy 读取 KDE 的 kioslaverc 代理声明。
func kioslavercProxy() (systemProxySettings, string) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return systemProxySettings{}, "无法读取系统代理设置"
	}
	data, err := os.ReadFile(filepath.Join(dir, "kioslaverc"))
	if err != nil {
		return systemProxySettings{}, "无法读取系统代理设置"
	}
	values := map[string]string{}
	inProxy := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			inProxy = strings.EqualFold(trimmed, "[Proxy Settings]")
			continue
		}
		if !inProxy {
			continue
		}
		key, value, ok := strings.Cut(trimmed, "=")
		if ok {
			values[strings.TrimSpace(key)] = kconfigUnescape(strings.TrimSpace(value))
		}
	}
	declaration := func(key, scheme string) string {
		address := values[key]
		if address == "" {
			return ""
		}
		address = strings.TrimPrefix(address, "http://")
		address = strings.TrimPrefix(address, "https://")
		return scheme + "=" + address
	}
	settings := systemProxySettings{available: true}
	switch values["ProxyType"] {
	case "0":
		return settings, ""
	case "1":
		parts := []string{}
		for _, item := range []struct{ key, scheme string }{{"httpProxy", "http"}, {"httpsProxy", "https"}} {
			if part := declaration(item.key, item.scheme); part != "" {
				parts = append(parts, part)
			}
		}
		if len(parts) == 0 {
			return settings, ""
		}
		settings.enabled = true
		settings.server = strings.Join(parts, ";")
	case "2":
		settings.enabled = true
		settings.autoConfigURL = values["Proxy Config Script"]
		if settings.autoConfigURL == "" {
			settings.autoConfigURL = "wpad://"
		}
	case "3":
		settings.enabled = true
		settings.autoConfigURL = "wpad://"
	default:
		return systemProxySettings{}, "无法读取系统代理设置"
	}
	settings.override = strings.Join(strings.Fields(values["NoProxyFor"]), ";")
	return settings, ""
}

// platformSystemProxy 读取 GNOME 系（gsettings）或 KDE（kioslaverc）的系统
// 代理声明。
func platformSystemProxy() (systemProxySettings, string) {
	mode := gsettingsValue("org.gnome.system.proxy", "mode")
	if mode == "" {
		return kioslavercProxy()
	}
	build := func(prefix string) string {
		host := gsettingsValue("org.gnome.system.proxy."+prefix, "host")
		if host == "" {
			return ""
		}
		port := gsettingsValue("org.gnome.system.proxy."+prefix, "port")
		if port == "" || port == "0" {
			return prefix + "=" + host
		}
		return prefix + "=" + host + ":" + port
	}
	declarationParts := []string{}
	if mode == "manual" {
		for _, prefix := range []string{"http", "https"} {
			if part := build(prefix); part != "" {
				declarationParts = append(declarationParts, part)
			}
		}
	}
	settings := systemProxySettings{
		available: true,
		enabled:   mode != "none",
		server:    strings.Join(declarationParts, ";"),
	}
	if mode == "auto" {
		settings.autoConfigURL = gsettingsValue("org.gnome.system.proxy", "autoconfig-url")
		if settings.autoConfigURL == "" {
			// WPAD 自动探测没有显式地址；按自动配置说明处理。
			settings.autoConfigURL = "wpad://"
		}
	}
	ignore := gsettingsValue("org.gnome.system.proxy", "ignore-hosts")
	entries := []string{}
	for _, item := range strings.Split(strings.Trim(ignore, "[]"), ",") {
		item = strings.Trim(strings.TrimSpace(item), "'")
		if item != "" {
			entries = append(entries, item)
		}
	}
	settings.override = strings.Join(entries, ";")
	return settings, ""
}
