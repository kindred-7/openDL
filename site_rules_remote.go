package main

// 站点规则源：定时从 HTTPS 地址同步站点识别规则到配置目录缓存，本地规则
// 与原生主机在加载时合并，本地规则优先。

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"openDL/pathrec"
)

const siteRulesRemoteLimit = 512 << 10

var (
	siteRulesSourceMu sync.Mutex
	siteRulesSource   string
	siteRulesFetchMu  sync.Mutex
)

// setSiteRulesSource 设置远程规则源；空值停止同步。
func setSiteRulesSource(source string) {
	siteRulesSourceMu.Lock()
	siteRulesSource = strings.TrimSpace(source)
	siteRulesSourceMu.Unlock()
}

func currentSiteRulesSource() string {
	siteRulesSourceMu.Lock()
	defer siteRulesSourceMu.Unlock()
	return siteRulesSource
}

// refreshSiteRulesRemote 拉取一次远程规则表；逐条校验后把原始字节原子写入
// 缓存，加载侧负责合并与去重。
func refreshSiteRulesRemote(ctx context.Context, source string) error {
	siteRulesFetchMu.Lock()
	defer siteRulesFetchMu.Unlock()
	if currentSiteRulesSource() != source {
		return nil
	}
	parsed, err := url.Parse(source)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf(tr("规则源必须使用不含登录凭据的 HTTPS URL"))
	}
	client := &http.Client{Timeout: 60 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf(tr("规则源返回 HTTP %d"), resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, siteRulesRemoteLimit+1))
	if err != nil {
		return err
	}
	if len(data) > siteRulesRemoteLimit {
		return fmt.Errorf(tr("规则源内容超过大小限制"))
	}
	if len(pathrec.ParseSiteRules(data)) == 0 {
		return fmt.Errorf(tr("规则源没有可用规则"))
	}
	siteRulesSourceMu.Lock()
	defer siteRulesSourceMu.Unlock()
	if siteRulesSource != source {
		return nil
	}
	path, err := pathrec.SiteRulesRemotePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

// syncSiteRulesRemote 同步一次规则源；空源不动作。错误供设置界面报告。
func syncSiteRulesRemote() error {
	source := currentSiteRulesSource()
	if source == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return refreshSiteRulesRemote(ctx, source)
}

// siteRulesRemoteWatch 定时同步规则源；失败保留上次缓存，不在后台打扰。
func (pg *mainPage) siteRulesRemoteWatch() {
	_ = syncSiteRulesRemote()
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-pg.stop:
			return
		case <-ticker.C:
			_ = syncSiteRulesRemote()
		}
	}
}
