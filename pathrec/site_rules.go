package pathrec

// 站点规则文件：JSON 数组，每项形如 {"tag","host","path","exts","siteKind"}。
// 规则用于识别没有扩展名的下载路径；无效条目跳过，其余继续生效。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	maxSiteRules   = 256
	maxSiteRuleLen = 512
)

// SiteRulesPath 返回用户配置目录下的站点规则文件路径。
func SiteRulesPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "opendl", "site-rules.json"), nil
}


// SiteRulesRemotePath 返回远程规则源的本地缓存路径。
func SiteRulesRemotePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "opendl", "site-rules-remote.json"), nil
}

// LoadSiteRulesWithRemote 合并本地规则与远程规则缓存；本地规则优先，合并
// 后的总数仍受同一上限约束。
func LoadSiteRulesWithRemote(path string) []SiteRule {
	local := LoadSiteRulesFile(path)
	remotePath, err := SiteRulesRemotePath()
	if err != nil {
		return local
	}
	remote := LoadSiteRulesFile(remotePath)
	if len(remote) == 0 {
		return local
	}
	merged := append([]SiteRule(nil), local...)
	for _, rule := range remote {
		if len(merged) >= maxSiteRules {
			break
		}
		merged = append(merged, rule)
	}
	return merged
}

// LoadSiteRulesFile 读取规则文件；文件不存在或不可读时返回空表。
func LoadSiteRulesFile(path string) []SiteRule {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return ParseSiteRules(data)
}

// ParseSiteRules 校验并接收规则；超出上限的条目与无效条目被跳过。
func ParseSiteRules(data []byte) []SiteRule {
	var raw []SiteRule
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	rules := make([]SiteRule, 0, len(raw))
	for _, rule := range raw {
		if !validSiteRule(rule) {
			continue
		}
		rules = append(rules, rule)
		if len(rules) >= maxSiteRules {
			break
		}
	}
	return rules
}

// validSiteRule 检查长度、正则与扩展名格式；路径按展开后的完整形态编译。
func validSiteRule(rule SiteRule) bool {
	if rule.Path == "" || len(rule.Path) > maxSiteRuleLen || len(rule.Host) > maxSiteRuleLen || len(rule.Tag) > 128 {
		return false
	}
	if rule.SiteKind < 0 || rule.SiteKind > 5 {
		return false
	}
	if _, err := regexp.Compile(ExpandPathPattern(rule.Path, rule.Host)); err != nil {
		return false
	}
	if rule.Host != "" {
		if _, err := regexp.Compile("(?i)" + rule.Host); err != nil {
			return false
		}
	}
	for _, ext := range rule.Exts {
		if len(ext) > 16 || !strings.HasPrefix(ext, ".") || strings.ContainsAny(ext, "/\\") {
			return false
		}
	}
	return true
}
