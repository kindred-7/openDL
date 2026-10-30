package download

// PAC 求值共用件：脚本辅助函数与源码缓存供各平台求值器复用。DNS 相关辅助
// 函数提供诚实存根；PAC 脚本按用户在系统设置中的指定执行。

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"openDL/i18n"
)

var (
	pacSourceMu sync.Mutex
	pacSources  = map[string]pacSourceEntry{}
)

type pacSourceEntry struct {
	source string
	at     time.Time
}

// pacScriptSource 读取 PAC 脚本；按地址缓存十分钟。
func pacScriptSource(ctx context.Context, pacURL string) (string, error) {
	now := time.Now()
	pacSourceMu.Lock()
	entry, ok := pacSources[pacURL]
	pacSourceMu.Unlock()
	if ok && now.Sub(entry.at) < 10*time.Minute {
		return entry.source, nil
	}
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pacURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf(i18n.T("规则源返回 HTTP %d"), resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return "", err
	}
	source := string(data)
	pacSourceMu.Lock()
	pacSources[pacURL] = pacSourceEntry{source: source, at: now}
	pacSourceMu.Unlock()
	return source, nil
}

const pacHelperScript = `
function dnsDomainIs(host, domain) {
  if (host === domain) { return true; }
  var suffix = domain.charAt(0) === "." ? domain : "." + domain;
  return host.slice(-suffix.length) === suffix;
}
function shExpMatch(str, shexp) {
  var re = "^" + shexp.replace(/[.+^${}()|[\]\\]/g, "\\$&").replace(/\*/g, ".*").replace(/\?/g, ".") + "$";
  return new RegExp(re).test(str);
}
function dnsDomainLevels(host) { return host.split(".").length - 1; }
function myIpAddress() { return "127.0.0.1"; }
function dnsResolve() { return ""; }
function isInNet() { return false; }
function isPlainHostName(host) { return host.indexOf(".") === -1; }
function localHostOrDomainIs(host, hostdom) { return host === hostdom || hostdom.indexOf(host + ".") === 0; }
function isResolvable() { return false; }
function isInNetEx() { return false; }
function dnsResolveEx() { return []; }
function myIpAddressEx() { return []; }
function weekdayRange() { return true; }
function dateRange() { return true; }
function timeRange() { return true; }
function alert() { }
`

// parsePACResult 取 PAC 结果中的首个代理条目：PROXY 按主机声明返回，SOCKS
// 条目返回 socks5 声明；只有 DIRECT 或无条目时返回空。
func parsePACResult(output string) string {
	result := strings.TrimSpace(output)
	result = strings.Trim(result, "\"")
	for _, entry := range strings.Split(result, ";") {
		entry = strings.TrimSpace(entry)
		upper := strings.ToUpper(entry)
		switch {
		case strings.HasPrefix(upper, "PROXY "):
			return strings.TrimSpace(entry[len("PROXY "):])
		case strings.HasPrefix(upper, "SOCKS "):
			return "socks5://" + strings.TrimSpace(entry[len("SOCKS "):])
		case strings.HasPrefix(upper, "SOCKS5 "):
			return "socks5://" + strings.TrimSpace(entry[len("SOCKS5 "):])
		}
	}
	return ""
}
