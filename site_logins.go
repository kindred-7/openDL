package main

// 站点登录表：匹配站点（含子域）的下载自动使用保存的登录凭据。凭据与
// 代理密码一样加密保存，不写入任务记录。

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"openDL/download"
)

type siteLoginEntry struct {
	Host     string `json:"host"`
	User     string `json:"user"`
	Password string `json:"password"`
}

const maxSiteLogins = 256

// sealSiteLogins 加密保存整个站点登录表。
func sealSiteLogins(entries []siteLoginEntry) (string, error) {
	data, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	defer clear(data)
	if len(data) > 64<<10 {
		return "", fmt.Errorf(tr("站点登录表过大"))
	}
	sealed, err := encryptProxySecret(data)
	if err != nil {
		return "", fmt.Errorf(tr("无法安全保存站点登录凭据：%w"), err)
	}
	return "dpapi1:" + base64.StdEncoding.EncodeToString(sealed), nil
}

// openSiteLogins 解开站点登录表；格式或平台不匹配时要求重新输入。
func openSiteLogins(protected string) ([]siteLoginEntry, error) {
	if protected == "" {
		return nil, nil
	}
	if !strings.HasPrefix(protected, "dpapi1:") || len(protected) > 512<<10 {
		return nil, fmt.Errorf(tr("已保存的站点登录格式无效"))
	}
	sealed, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(protected, "dpapi1:"))
	if err != nil || len(sealed) == 0 {
		return nil, fmt.Errorf(tr("已保存的站点登录格式无效"))
	}
	data, err := decryptProxySecret(sealed)
	if err != nil {
		return nil, fmt.Errorf(tr("无法读取已保存的站点登录，请在原 Windows 账户使用或重新输入"))
	}
	defer clear(data)
	var entries []siteLoginEntry
	if len(data) > 64<<10 || json.Unmarshal(data, &entries) != nil {
		return nil, fmt.Errorf(tr("已保存的站点登录格式无效"))
	}
	return normalizeSiteLogins(entries), nil
}

// normalizeSiteLogins 校验站点与用户名格式，同站点保留最后一条。
func normalizeSiteLogins(entries []siteLoginEntry) []siteLoginEntry {
	out := make([]siteLoginEntry, 0, len(entries))
	for _, entry := range entries {
		host := strings.ToLower(strings.TrimSpace(entry.Host))
		for _, prefix := range []string{"https://", "http://"} {
			host = strings.TrimPrefix(host, prefix)
		}
		if i := strings.IndexAny(host, "/?#@:"); i >= 0 {
			host = host[:i]
		}
		user := strings.TrimSpace(entry.User)
		if host == "" || len(host) > 253 || strings.ContainsAny(host, " \\") || user == "" || len(user) > 128 || len(entry.Password) > 512 {
			continue
		}
		for i := range out {
			if out[i].Host == host {
				out = append(out[:i], out[i+1:]...)
				break
			}
		}
		out = append(out, siteLoginEntry{Host: host, User: user, Password: entry.Password})
		if len(out) >= maxSiteLogins {
			break
		}
	}
	return out
}

// siteLoginEngine 转换给下载引擎的登录表。
func siteLoginEngine(entries []siteLoginEntry) []download.SiteLogin {
	out := make([]download.SiteLogin, 0, len(entries))
	for _, entry := range entries {
		out = append(out, download.SiteLogin{Host: entry.Host, User: entry.User, Password: entry.Password})
	}
	return out
}
