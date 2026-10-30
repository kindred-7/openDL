package download

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"

	"openDL/i18n"
)

var ErrRequestIDConflict = i18n.NewError("请求编号已用于其他下载内容，请使用新的请求编号")

func ValidClientRequestID(value string) bool {
	if len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// Do not hash authentication headers or URL passwords into a persisted value:
// retries may refresh credentials, and a credential-derived hash could expose
// low-entropy passwords to offline guessing. The ID identifies one submission.
func submissionFingerprint(req Request) (string, error) {
	req.Headers = nil
	req.ClientRequestID = ""
	req.Title, req.PageURL = "", "" // page metadata can change while retrying
	if req.Queue == "main" {
		req.Queue = ""
	}
	if parsed, err := url.Parse(req.URL); err == nil {
		parsed.User = nil
		parsed.Fragment = ""
		req.URL = parsed.String()
	}
	data, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
