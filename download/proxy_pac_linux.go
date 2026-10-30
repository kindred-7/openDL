//go:build linux

package download

import (
	"context"
	"encoding/json"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

// pacEngine 是可用的 JavaScript 运行时及其调用方式。
type pacEngine struct {
	name    string
	fromStd bool
	args    []string
}

// pacEngines 按优先级探测 node、gjs 与 qjs。
func pacEngines() []pacEngine {
	engines := []pacEngine{}
	if _, err := exec.LookPath("node"); err == nil {
		engines = append(engines, pacEngine{name: "node", fromStd: true, args: []string{"-"}})
	}
	if _, err := exec.LookPath("gjs"); err == nil {
		engines = append(engines, pacEngine{name: "gjs", args: []string{"-c", ""}})
	}
	if _, err := exec.LookPath("qjs"); err == nil {
		engines = append(engines, pacEngine{name: "qjs", args: []string{"-e", ""}})
	}
	return engines
}

// platformPACAvailable 报告当前是否有可用的 PAC 求值运行时。
func platformPACAvailable() bool { return len(pacEngines()) > 0 }

// platformPACProxy 用系统可用的 JavaScript 运行时执行 PAC 脚本求值；返回
// 代理声明，直连时返回空。
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
	call := "FindProxyForURL(" + string(address) + ", " + string(host) + ")"
	// node 用 console 输出；gjs 与 qjs 用 print 输出。
	printScript := pacHelperScript + source + "
print(String(" + call + "))
"
	consoleScript := pacHelperScript + source + "
console.log(String(" + call + "))
"
	for _, engine := range pacEngines() {
		text := printScript
		if engine.fromStd {
			text = consoleScript
		}
		args := append([]string(nil), engine.args...)
		if !engine.fromStd {
			args[1] = text
		}
		command := exec.CommandContext(ctx, engine.name, args...)
		if engine.fromStd {
			command.Stdin = strings.NewReader(text)
		}
		output, err := command.Output()
		if err != nil {
			continue
		}
		return parsePACResult(string(output)), nil
	}
	return "", errPACUnavailable
}
