package download

import (
	"strings"

	"openDL/i18n"
)

// DefaultUserAgent is the browser-compatible identity for unconfigured tasks.
const DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/157.0.0.0 Safari/537.36"

// NormalizeUserAgent accepts one HTTP header value; empty selects the built-in default.
func NormalizeUserAgent(value string) (string, error) {
	if len(value) > 4096 {
		return "", i18n.NewError("用户代理不能超过 4096 字节")
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] == 0x7f {
			return "", i18n.NewError("用户代理必须为单行，且不能包含控制字符")
		}
	}
	return strings.TrimSpace(value), nil
}

// SetUserAgent changes the fallback captured by future tasks. Existing tasks
// retain their headers so range requests and retries use the same identity.
func (m *Manager) SetUserAgent(value string) error {
	value, err := NormalizeUserAgent(value)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.userAgent = value
	m.mu.Unlock()
	return nil
}

// downloadHeaders preserves explicit identities and records the fallback in
// ordinary task headers, including the existing credential-free journal.
func downloadHeaders(src map[string]string, fallback string) map[string]string {
	headers := cloneHeaders(src)
	hasUserAgent := false
	for key, value := range headers {
		if strings.EqualFold(key, "User-Agent") {
			if strings.TrimSpace(value) == "" {
				delete(headers, key)
			} else {
				hasUserAgent = true
			}
		}
	}
	if !hasUserAgent {
		if headers == nil {
			headers = make(map[string]string)
		}
		if fallback == "" {
			fallback = DefaultUserAgent
		}
		headers["User-Agent"] = fallback
	}
	return headers
}
