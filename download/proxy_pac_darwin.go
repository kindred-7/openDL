//go:build darwin

package download

import (
	"context"
	"encoding/json"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

// platformPACAvailable 报告当前平台是否支持 PAC 求值。
func platformPACAvailable() bool { return true }

// platformPACProxy 用 macOS 的 JavaScript 自动化执行 PAC 脚本求值；返回代
// 理声明，直连时返回空。
func platformPACProxy(pacURL, target string) (string, error) {
	parsed, err := url.Parse(target)
	if err != nil {
		return "", errPACUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	source, err := pacScriptSource(ctx, pacURL)
	if err != nil {
		return "", errPACUnavailable
	}
	address, _ := json.Marshal(target)
	host, _ := json.Marshal(parsed.Hostname())
	script := pacHelperScript + source + "
FindProxyForURL(" + string(address) + ", " + string(host) + ")
"
	command := exec.CommandContext(ctx, "osascript", "-l", "JavaScript")
	command.Stdin = strings.NewReader(script)
	output, err := command.Output()
	if err != nil {
		return "", errPACUnavailable
	}
	return parsePACResult(string(output)), nil
}
