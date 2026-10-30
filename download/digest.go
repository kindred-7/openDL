package download

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"

	"openDL/i18n"
)

type digestChallenge struct {
	params    map[string]string
	algorithm string
	strength  int
}

type digestResponseAuth struct {
	newHash                         func() hash.Hash
	a1, nonce, cnonce, uri, qop, nc string
}

type digestCacheEntry struct {
	resource  string
	challenge digestChallenge
	expires   time.Time
}

func digestResourceKey(address *url.URL) string {
	copyURL := *address
	copyURL.User = nil
	copyURL.Fragment = ""
	return resourceFingerprint(&copyURL)
}

func (t *Task) cachedDigest(resource string) (digestChallenge, bool) {
	if t == nil {
		return digestChallenge{}, false
	}
	t.digestMu.Lock()
	defer t.digestMu.Unlock()
	if t.digestCache == nil || t.digestCache.resource != resource || !time.Now().Before(t.digestCache.expires) {
		return digestChallenge{}, false
	}
	return t.digestCache.challenge, true
}

func (t *Task) rememberDigest(resource string, challenge digestChallenge) {
	if t == nil {
		return
	}
	t.digestMu.Lock()
	if t.digestCache != nil && t.digestCache.resource == resource && time.Now().Before(t.digestCache.expires) && t.digestCache.challenge.params["nonce"] != challenge.params["nonce"] {
		// A newer response already changed this resource's nonce.
		t.digestMu.Unlock()
		return
	}
	t.digestCache = &digestCacheEntry{resource: resource, challenge: challenge, expires: time.Now().Add(5 * time.Minute)}
	t.digestMu.Unlock()
}

func (t *Task) advanceDigestNonce(resource, oldNonce, nextNonce string) {
	if t == nil || nextNonce == "" || len(nextNonce) > 8192 {
		return
	}
	t.digestMu.Lock()
	defer t.digestMu.Unlock()
	if t.digestCache == nil || t.digestCache.resource != resource || t.digestCache.challenge.params["nonce"] != oldNonce || !time.Now().Before(t.digestCache.expires) {
		return
	}
	challenge := t.digestCache.challenge
	params := make(map[string]string, len(challenge.params))
	for k, v := range challenge.params {
		params[k] = v
	}
	params["nonce"] = nextNonce
	challenge.params = params
	t.digestCache = &digestCacheEntry{resource: resource, challenge: challenge, expires: time.Now().Add(5 * time.Minute)}
}

func (t *Task) clearDigestNonce(resource, nonce string) {
	if t == nil {
		return
	}
	t.digestMu.Lock()
	if t.digestCache != nil && t.digestCache.resource == resource && t.digestCache.challenge.params["nonce"] == nonce {
		t.digestCache = nil
	}
	t.digestMu.Unlock()
}

func (t *Task) clearDigest() {
	if t == nil {
		return
	}
	t.digestMu.Lock()
	t.digestCache = nil
	t.digestMu.Unlock()
}

// A nonce can be shared by several resources on an origin. Allocate each
// nonce count under one lock, including challenges received concurrently.
func (t *Task) nextDigestCount(address *url.URL, challenge digestChallenge) (uint32, error) {
	if t == nil {
		return 1, nil
	}
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%d:%s:%d:%s:%d:%s:%d:%s:%d:%s", len(address.Scheme), strings.ToLower(address.Scheme), len(address.Hostname()), strings.ToLower(address.Hostname()), len(originPort(address)), originPort(address), len(challenge.params["realm"]), challenge.params["realm"], len(challenge.params["nonce"]), challenge.params["nonce"])
	key := hex.EncodeToString(h.Sum(nil))
	t.digestMu.Lock()
	defer t.digestMu.Unlock()
	if t.digestCounts == nil {
		t.digestCounts = make(map[string]uint32)
	}
	count, exists := t.digestCounts[key]
	if (!exists && len(t.digestCounts) >= 65536) || count == ^uint32(0) {
		return 0, i18n.NewError("Digest 认证计数超过限制，请重新创建任务")
	}
	count++
	t.digestCounts[key] = count
	return count, nil
}

// authParts separates challenges and parameters without splitting quoted qop
// lists or escaped quotes. Malformed challenges are rejected by the parser.
func authParts(value string) []string {
	var parts []string
	start, quoted, escaped := 0, false, false
	for i := 0; i < len(value); i++ {
		c := value[i]
		if escaped {
			escaped = false
			continue
		}
		if quoted && c == '\\' {
			escaped = true
			continue
		}
		if c == '"' {
			quoted = !quoted
			continue
		}
		if c == ',' && !quoted {
			parts = append(parts, strings.TrimSpace(value[start:i]))
			start = i + 1
		}
	}
	if quoted || escaped {
		return nil
	}
	return append(parts, strings.TrimSpace(value[start:]))
}

func digestUnquote(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	if value[0] != '"' {
		for _, r := range value {
			if r <= 0x20 || r == 0x7f || r == '"' {
				return "", false
			}
		}
		return value, true
	}
	if len(value) < 2 || value[len(value)-1] != '"' {
		return "", false
	}
	var b strings.Builder
	for i := 1; i < len(value)-1; i++ {
		if value[i] == '\\' {
			i++
			if i >= len(value)-1 {
				return "", false
			}
		}
		if value[i] < 0x20 || value[i] == 0x7f {
			return "", false
		}
		b.WriteByte(value[i])
	}
	return b.String(), true
}

func digestChallenges(headers []string) []digestChallenge {
	var challenges []digestChallenge
	for _, header := range headers {
		var current map[string]string
		finish := func() {
			if current == nil || current["nonce"] == "" {
				return
			}
			if _, ok := current["realm"]; !ok {
				return
			}
			algorithm := strings.ToUpper(current["algorithm"])
			if algorithm == "" {
				algorithm = "MD5"
			}
			base := strings.TrimSuffix(algorithm, "-SESS")
			strength := map[string]int{"MD5": 1, "SHA-256": 2, "SHA-512-256": 3}[base]
			if strength == 0 {
				return
			}
			if charset := current["charset"]; charset != "" && !strings.EqualFold(charset, "UTF-8") {
				return
			}
			if current["userhash"] != "" && !strings.EqualFold(current["userhash"], "true") && !strings.EqualFold(current["userhash"], "false") {
				return
			}
			if qop, present := current["qop"]; present {
				found := false
				for _, token := range strings.Split(qop, ",") {
					if strings.EqualFold(strings.TrimSpace(token), "auth") || strings.EqualFold(strings.TrimSpace(token), "auth-int") {
						found = true
					}
				}
				if !found {
					return
				}
			}
			challenges = append(challenges, digestChallenge{params: current, algorithm: algorithm, strength: strength})
		}
		for _, part := range authParts(header) {
			if index := strings.IndexAny(part, " \t"); index > 0 && !strings.Contains(part[:index], "=") && !strings.HasPrefix(strings.TrimSpace(part[index:]), "=") {
				finish()
				current = nil
				if strings.EqualFold(part[:index], "Digest") {
					current = make(map[string]string)
					part = strings.TrimSpace(part[index:])
				} else {
					continue
				}
			}
			if current == nil {
				continue
			}
			key, value, ok := strings.Cut(part, "=")
			key = strings.ToLower(strings.TrimSpace(key))
			parsed, valid := digestUnquote(value)
			if !ok || key == "" || !valid {
				current = nil
				continue
			}
			if _, exists := current[key]; exists {
				current = nil
				continue
			}
			current[key] = parsed
		}
		finish()
	}
	return challenges
}

func digestQuote(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
}

func digestUserParam(username string) string {
	for _, r := range username {
		if r >= 0x80 {
			return "username*=UTF-8''" + strings.ReplaceAll(url.QueryEscape(username), "+", "%20")
		}
	}
	return "username=" + digestQuote(username)
}

func digestAuthorization(challenge digestChallenge, req *http.Request, username, password string) (string, *digestResponseAuth, error) {
	return digestAuthorizationWithCount(challenge, req, username, password, 1)
}

func digestAuthorizationWithCount(challenge digestChallenge, req *http.Request, username, password string, count uint32) (string, *digestResponseAuth, error) {
	var newHash func() hash.Hash
	switch strings.TrimSuffix(challenge.algorithm, "-SESS") {
	case "MD5":
		newHash = md5.New // required for legacy HTTP Digest compatibility
	case "SHA-256":
		newHash = sha256.New
	case "SHA-512-256":
		newHash = sha512.New512_256
	default:
		return "", nil, fmt.Errorf(i18n.T("不支持的 Digest 算法"))
	}
	for _, field := range []string{username, password} {
		for _, r := range field {
			if r < 0x20 || r == 0x7f {
				return "", nil, fmt.Errorf(i18n.T("认证信息包含无效字符"))
			}
		}
	}
	if strings.EqualFold(challenge.params["charset"], "UTF-8") {
		username, password = norm.NFC.String(username), norm.NFC.String(password)
	}
	hashText := func(value string) string {
		h := newHash()
		_, _ = h.Write([]byte(value))
		return hex.EncodeToString(h.Sum(nil))
	}
	var nonceBytes [16]byte
	if _, err := rand.Read(nonceBytes[:]); err != nil {
		return "", nil, err
	}
	cnonce := hex.EncodeToString(nonceBytes[:])
	realm, nonce := challenge.params["realm"], challenge.params["nonce"]
	uri := req.URL.RequestURI()
	a1 := hashText(username + ":" + realm + ":" + password)
	if strings.HasSuffix(challenge.algorithm, "-SESS") {
		a1 = hashText(a1 + ":" + nonce + ":" + cnonce)
	}
	qop := ""
	for _, token := range strings.Split(challenge.params["qop"], ",") {
		token = strings.ToLower(strings.TrimSpace(token))
		if token == "auth" {
			qop = "auth"
			break
		}
		if token == "auth-int" {
			qop = "auth-int"
		}
	}
	a2Input := req.Method + ":" + uri
	if qop == "auth-int" {
		if req.Body != nil && req.Body != http.NoBody {
			return "", nil, fmt.Errorf(i18n.T("暂不支持带请求体的 Digest auth-int"))
		}
		a2Input += ":" + hashText("")
	}
	a2 := hashText(a2Input)
	nc := fmt.Sprintf("%08x", count)
	response := ""
	if qop != "" {
		response = hashText(a1 + ":" + nonce + ":" + nc + ":" + cnonce + ":" + qop + ":" + a2)
	} else {
		response = hashText(a1 + ":" + nonce + ":" + a2)
	}
	userField := digestUserParam(username)
	if strings.EqualFold(challenge.params["userhash"], "true") {
		userField = "username=" + digestQuote(hashText(username+":"+realm)) + ", userhash=true"
	}
	fields := []string{userField, "realm=" + digestQuote(realm), "nonce=" + digestQuote(nonce), "uri=" + digestQuote(uri), "algorithm=" + challenge.algorithm, "response=" + digestQuote(response)}
	if opaque, ok := challenge.params["opaque"]; ok {
		fields = append(fields, "opaque="+digestQuote(opaque))
	}
	if qop != "" {
		fields = append(fields, "qop="+qop, "nc="+nc, "cnonce="+digestQuote(cnonce))
	} else if strings.HasSuffix(challenge.algorithm, "-SESS") {
		fields = append(fields, "cnonce="+digestQuote(cnonce))
	}
	state := &digestResponseAuth{newHash: newHash, a1: a1, nonce: nonce, cnonce: cnonce, uri: uri, qop: qop, nc: nc}
	return "Digest " + strings.Join(fields, ", "), state, nil
}

// digestIntegrityBody verifies rspauth at EOF when supplied in headers or
// trailers. Without rspauth, RFC 7616 permits the response but it cannot be
// independently authenticated by this mechanism.
type digestIntegrityBody struct {
	io.ReadCloser
	response    *http.Response
	state       *digestResponseAuth
	bodyHash    hash.Hash
	finished    bool
	verifyErr   error
	nextNonce   string
	onNextNonce func(string)
}

func (b *digestIntegrityBody) Read(p []byte) (int, error) {
	if b.finished {
		if b.verifyErr != nil {
			return 0, b.verifyErr
		}
		return 0, io.EOF
	}
	n, err := b.ReadCloser.Read(p)
	if n > 0 && b.state.qop == "auth-int" {
		_, _ = b.bodyHash.Write(p[:n])
	}
	if err == io.EOF {
		b.finished = true
		b.verifyErr = b.verify()
		if b.verifyErr != nil {
			return n, b.verifyErr
		}
		if b.nextNonce != "" && b.onNextNonce != nil {
			b.onNextNonce(b.nextNonce)
		}
	}
	return n, err
}

func (b *digestIntegrityBody) verify() error {
	values := append(append([]string(nil), b.response.Header.Values("Authentication-Info")...), b.response.Trailer.Values("Authentication-Info")...)
	if len(values) == 0 {
		return nil
	}
	params := make(map[string]string)
	for _, value := range values {
		parts := authParts(value)
		if parts == nil {
			return fmt.Errorf(i18n.T("Digest 响应认证信息无效"))
		}
		for _, part := range parts {
			name, text, ok := strings.Cut(part, "=")
			name = strings.ToLower(strings.TrimSpace(name))
			parsed, valid := digestUnquote(text)
			if !ok || name == "" || !valid {
				return fmt.Errorf(i18n.T("Digest 响应认证信息无效"))
			}
			if _, exists := params[name]; exists {
				return fmt.Errorf(i18n.T("Digest 响应认证信息重复"))
			}
			params[name] = parsed
		}
	}
	nextNonce := params["nextnonce"]
	if len(nextNonce) > 8192 || strings.ContainsAny(nextNonce, "\r\n\x00") {
		return fmt.Errorf(i18n.T("Digest 响应认证信息无效"))
	}
	for _, r := range nextNonce {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf(i18n.T("Digest 响应认证信息无效"))
		}
	}
	if _, hasResponse := params["rspauth"]; !hasResponse {
		// A server can send only nextnonce or extension parameters. That is
		// not mutual authentication, but it is not a failed rspauth check.
		if _, hasQOP := params["qop"]; hasQOP {
			return fmt.Errorf(i18n.T("Digest 响应认证参数不匹配"))
		}
		if _, hasCNonce := params["cnonce"]; hasCNonce {
			return fmt.Errorf(i18n.T("Digest 响应认证参数不匹配"))
		}
		if _, hasCount := params["nc"]; hasCount {
			return fmt.Errorf(i18n.T("Digest 响应认证参数不匹配"))
		}
		b.nextNonce = nextNonce
		return nil
	}
	if !strings.EqualFold(params["qop"], b.state.qop) || params["cnonce"] != b.state.cnonce || params["nc"] != b.state.nc {
		return fmt.Errorf(i18n.T("Digest 响应认证参数不匹配"))
	}
	hashText := func(s string) string {
		h := b.state.newHash()
		_, _ = h.Write([]byte(s))
		return hex.EncodeToString(h.Sum(nil))
	}
	a2Input := ":" + b.state.uri
	if b.state.qop == "auth-int" {
		a2Input += ":" + hex.EncodeToString(b.bodyHash.Sum(nil))
	}
	a2 := hashText(a2Input)
	want := hashText(b.state.a1 + ":" + b.state.nonce + ":" + b.state.nc + ":" + b.state.cnonce + ":" + b.state.qop + ":" + a2)
	got, err := hex.DecodeString(params["rspauth"])
	expected, _ := hex.DecodeString(want)
	if err != nil || subtle.ConstantTimeCompare(got, expected) != 1 {
		return fmt.Errorf(i18n.T("Digest 响应完整性校验失败"))
	}
	b.nextNonce = nextNonce
	return nil
}

// doDigestRequest retries a 401 only with explicitly supplied Basic/URL
// credentials and only at the original origin. No credential is persisted.
func doDigestRequest(client *http.Client, req *http.Request, task *Task) (*http.Response, error) {
	username, password, ok := req.BasicAuth()
	if !ok && req.Header.Get("Authorization") == "" && req.URL.User != nil {
		username = req.URL.User.Username()
		password, _ = req.URL.User.Password()
		ok = true
	}
	current := req.Clone(req.Context())
	var state *digestResponseAuth
	var activeChallenge digestChallenge
	resource := digestResourceKey(req.URL)
	if challenge, found := task.cachedDigest(resource); ok && found {
		address := *req.URL
		address.User = nil
		current.URL = &address
		count, err := task.nextDigestCount(current.URL, challenge)
		if err != nil {
			return nil, err
		}
		header, cachedState, err := digestAuthorizationWithCount(challenge, current, username, password, count)
		if err != nil {
			return nil, err
		}
		current.Header.Set("Authorization", header)
		state, activeChallenge = cachedState, challenge
	}
	for authAttempt := 0; ; authAttempt++ {
		resp, err := client.Do(current)
		var responseAuth *digestIntegrityBody
		if err == nil && resp.StatusCode != http.StatusUnauthorized && state != nil && (state.qop == "auth-int" || state.qop == "auth") {
			matched := resp.Request != nil && resp.Request.URL != nil && sameOrigin(current.URL, resp.Request.URL) && resp.Request.URL.RequestURI() == state.uri
			if !matched && state.qop == "auth-int" {
				resp.Body.Close()
				return nil, fmt.Errorf(i18n.T("Digest auth-int 响应重定向后无法校验"))
			}
			if matched {
				body := &digestIntegrityBody{ReadCloser: resp.Body, response: resp, state: state, bodyHash: state.newHash()}
				_, infoTrailer := resp.Trailer["Authentication-Info"]
				noBody := current.Method == http.MethodHead || resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotModified
				if (state.qop == "auth" || noBody) && len(resp.Header.Values("Authentication-Info")) > 0 && !infoTrailer {
					if verifyErr := body.verify(); verifyErr != nil {
						resp.Body.Close()
						return nil, verifyErr
					}
				}
				resp.Body = body
				responseAuth = body
			}
		}
		if err == nil && resp.StatusCode < 400 && state != nil && resp.Request != nil && resp.Request.URL != nil && digestResourceKey(resp.Request.URL) == resource {
			task.rememberDigest(resource, activeChallenge)
			if responseAuth != nil {
				oldNonce := state.nonce
				responseAuth.onNextNonce = func(next string) { task.advanceDigestNonce(resource, oldNonce, next) }
				if responseAuth.nextNonce != "" {
					responseAuth.onNextNonce(responseAuth.nextNonce)
				}
			}
		}
		if err == nil && resp.StatusCode == http.StatusUnauthorized && state != nil {
			task.clearDigestNonce(resource, state.nonce)
		}
		if err != nil || resp.StatusCode != http.StatusUnauthorized || !ok || authAttempt >= 2 || resp.Request == nil || resp.Request.URL == nil || !sameOrigin(req.URL, resp.Request.URL) {
			return resp, err
		}
		var best digestChallenge
		for _, candidate := range digestChallenges(resp.Header.Values("WWW-Authenticate")) {
			if authAttempt > 0 && !strings.EqualFold(candidate.params["stale"], "true") {
				continue
			}
			if candidate.strength > best.strength {
				best = candidate
			}
		}
		if best.strength == 0 {
			if ntlmChallenge(resp.Header) {
				return doNTLMRequest(client, resp, username, password)
			}
			return resp, nil
		}
		next := resp.Request.Clone(req.Context())
		address := *resp.Request.URL
		address.User = nil
		next.URL = &address
		count, err := task.nextDigestCount(next.URL, best)
		if err != nil {
			resp.Body.Close()
			return nil, err
		}
		header, nextState, err := digestAuthorizationWithCount(best, next, username, password, count)
		if err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()
		next.Header.Set("Authorization", header)
		current = next
		state = nextState
		activeChallenge = best
	}
}
