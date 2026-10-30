// Package appupdate downloads only releases authorized by a pinned signing key.
package appupdate

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"openDL/i18n"
	"openDL/signature"
)

const MaxArtifactSize int64 = 512 << 20

type Config struct {
	URL         string             `json:"url,omitempty"`
	PublicKey   string             `json:"public_key,omitempty"`
	Provider    string             `json:"provider,omitempty"`
	Automatic   bool               `json:"automatic"`
	LastChecked time.Time          `json:"last_checked,omitempty"`
	Transport   http.RoundTripper  `json:"-"`
	Progress    func(int64, int64) `json:"-"`
}

type Envelope struct {
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

type Manifest struct {
	Schema  int       `json:"schema"`
	Version string    `json:"version"`
	Expires time.Time `json:"expires"`
	Notes   string    `json:"notes"`
	Assets  []Asset   `json:"assets"`
}

type Asset struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	URL    string `json:"url"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	// Kind 区分独立可执行文件（默认）与安装器升级包。
	Kind string `json:"kind,omitempty"`
}

// assetKind 规范化安装包类型；空值按独立可执行文件处理。
func AssetKind(asset Asset) (string, error) {
	switch strings.ToLower(strings.TrimSpace(asset.Kind)) {
	case "", "binary":
		return "binary", nil
	case "installer":
		return "installer", nil
	default:
		return "", i18n.NewError("更新安装包类型无效")
	}
}

// Release retains the signed envelope so installation can authenticate it again.
type Release struct {
	Manifest Manifest
	Asset    Asset
	Envelope Envelope
}

func secureURL(text string) error {
	u, err := url.Parse(text)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return i18n.NewError("更新地址必须使用不含登录凭据的 HTTPS URL")
	}
	return nil
}

func (c Config) Validate() error {
	if c.Provider != "" && c.Provider != "gitcode" {
		return i18n.NewError("更新源类型无效")
	}
	if c.URL == "" && c.PublicKey == "" && !c.Automatic {
		return nil
	}
	// 地址在每次请求时仍要求 HTTPS；仅验签（无地址）时只校验公钥。
	if c.URL != "" {
		if err := secureURL(c.URL); err != nil {
			return err
		}
		if c.Provider == "gitcode" {
			if _, err := gitcodeReleaseBase(c.URL); err != nil {
				return err
			}
		}
	}
	_, err := signature.PublicKey(c.PublicKey)
	return err
}

// gitcodeReleaseBase accepts only the public latest-release API endpoint.
// Release metadata is untrusted; only the subsequently signed manifest can
// authorize an artifact, its version, or its download URL.
func gitcodeReleaseBase(address string) (string, error) {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "api.gitcode.com") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", i18n.NewError("GitCode 更新源地址无效")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 7 || parts[0] != "api" || parts[1] != "v5" || parts[2] != "repos" || parts[5] != "releases" || parts[6] != "latest" {
		return "", i18n.NewError("GitCode 更新源地址无效")
	}
	for _, name := range parts[3:5] {
		if name == "" || name == "." || name == ".." {
			return "", i18n.NewError("GitCode 更新源地址无效")
		}
		for _, ch := range name {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_' || ch == '.') {
				return "", i18n.NewError("GitCode 更新源地址无效")
			}
		}
	}
	return "https://api.gitcode.com/api/v5/repos/" + url.PathEscape(parts[3]) + "/" + url.PathEscape(parts[4]) + "/releases/", nil
}

func gitcodeManifestURL(ctx context.Context, config Config) (string, error) {
	base, err := gitcodeReleaseBase(config.URL)
	if err != nil {
		return "", err
	}
	resp, err := get(ctx, config.URL, config.Transport)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return "", err
	}
	if len(data) > 1<<20 {
		return "", i18n.NewError("更新清单超过大小限制")
	}
	var metadata struct {
		Tag        string `json:"tag_name"`
		Prerelease bool   `json:"prerelease"`
		Draft      bool   `json:"draft"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		return "", err
	}
	version, err := parseSemVer(metadata.Tag)
	if err != nil || len(version.pre) != 0 || metadata.Prerelease || metadata.Draft {
		return "", i18n.NewError("GitCode 发行版本无效")
	}
	return base + url.PathEscape(metadata.Tag) + "/attach_files/releases.json/download", nil
}

// semVer 是 SemVer 2.0.0 的版本号：major.minor.patch[-prerelease][+build]。
// 构建元数据不参与优先级比较。
type semVer struct {
	major, minor, patch string
	pre                 []string // 预发布标识；空表示正式版
}

// parseSemVer 解析 SemVer 版本号，接受历史写法的 v 前缀。
func parseSemVer(version string) (semVer, error) {
	var v semVer
	s := strings.TrimPrefix(strings.TrimSpace(version), "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		build := strings.Split(s[i+1:], ".")
		s = s[:i]
		for _, id := range build {
			if !semVerIdent(id, true) {
				return v, i18n.NewError("更新版本号无效")
			}
		}
	}
	core, pre, hasPre := strings.Cut(s, "-")
	fields := strings.Split(core, ".")
	if len(fields) != 3 {
		return v, i18n.NewError("更新版本号必须采用 major.minor.patch 格式")
	}
	for _, field := range fields {
		if !semVerNumeric(field) {
			return v, i18n.NewError("更新版本号无效")
		}
	}
	v.major, v.minor, v.patch = fields[0], fields[1], fields[2]
	if hasPre {
		ids := strings.Split(pre, ".")
		for _, id := range ids {
			if !semVerIdent(id, false) {
				return v, i18n.NewError("更新版本号无效")
			}
		}
		v.pre = ids
	}
	return v, nil
}

// semVerNumeric 解析不带前导零的非负整数标识。
func semVerNumeric(s string) bool {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// semVerIdent 校验预发布或构建标识：非空的 [0-9A-Za-z-]；loose 时允许数字带前导零。
func semVerIdent(s string, loose bool) bool {
	if s == "" {
		return false
	}
	numeric := true
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '-':
			numeric = false
		default:
			return false
		}
	}
	return loose || !numeric || len(s) == 1 || s[0] != '0'
}

// compare 返回 a 相对 b 的优先级，取值 -1、0 或 1。
func (a semVer) compare(b semVer) int {
	for _, pair := range [][2]string{{a.major, b.major}, {a.minor, b.minor}, {a.patch, b.patch}} {
		if result := compareSemVerIdent(pair[0], pair[1]); result != 0 {
			return result
		}
	}
	// 正式版高于任何预发布版，例如 1.5.0 > 1.5.0-rc.1。
	if len(a.pre) == 0 || len(b.pre) == 0 {
		switch {
		case len(a.pre) == 0 && len(b.pre) == 0:
			return 0
		case len(a.pre) == 0:
			return 1
		default:
			return -1
		}
	}
	for i := 0; i < min(len(a.pre), len(b.pre)); i++ {
		if c := compareSemVerIdent(a.pre[i], b.pre[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a.pre) < len(b.pre):
		return -1
	case len(a.pre) > len(b.pre):
		return 1
	}
	return 0
}

// compareSemVerIdent 比较预发布标识：纯数字按数值，其余按 ASCII 字节序；
// 数字标识低于字母标识。
func compareSemVerIdent(a, b string) int {
	aNum, bNum := semVerDigits(a), semVerDigits(b)
	switch {
	case aNum && bNum:
		// Prerelease numeric identifiers are not limited to uint64. Their
		// validated form has no leading zero, so length and lexical order
		// compare their numeric values without overflow.
		if len(a) < len(b) {
			return -1
		}
		if len(a) > len(b) {
			return 1
		}
	case aNum:
		return -1
	case bNum:
		return 1
	}
	if a != b {
		if a < b {
			return -1
		}
		return 1
	}
	return 0
}

func semVerDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Newer 报告 candidate 的 SemVer 优先级是否高于 current。
func Newer(candidate, current string) (bool, error) {
	a, err := parseSemVer(candidate)
	if err != nil {
		return false, err
	}
	b, err := parseSemVer(current)
	if err != nil {
		return false, err
	}
	return a.compare(b) > 0, nil
}

func validateAsset(asset Asset) error {
	if err := secureURL(asset.URL); err != nil {
		return err
	}
	digest, err := hex.DecodeString(asset.SHA256)
	if err != nil || len(digest) != sha256.Size || asset.Size <= 0 || asset.Size > MaxArtifactSize {
		return i18n.NewError("更新安装包信息无效")
	}
	return nil
}

// ValidateManifestForSigning checks every platform before the release is
// signed. The client validates only its selected platform for compatibility
// with already published manifests.
func ValidateManifestForSigning(manifest Manifest, now time.Time) error {
	if manifest.Schema != 1 || manifest.Expires.IsZero() || !now.Before(manifest.Expires) || len(manifest.Assets) == 0 || len(manifest.Assets) > 32 || len(manifest.Notes) > 65536 {
		return i18n.NewError("更新清单无效或已经过期")
	}
	version, err := parseSemVer(manifest.Version)
	if err != nil {
		return err
	}
	if len(version.pre) > 0 {
		return i18n.NewError("更新清单只接受稳定版本")
	}
	seen := make(map[[3]string]bool)
	for _, asset := range manifest.Assets {
		if asset.OS == "" || asset.Arch == "" {
			return i18n.NewError("更新安装包信息无效")
		}
		kind, err := AssetKind(asset)
		if err != nil {
			return err
		}
		key := [3]string{asset.OS, asset.Arch, kind}
		if seen[key] {
			return i18n.NewError("更新清单包含重复的平台安装包")
		}
		seen[key] = true
		if err := validateAsset(asset); err != nil {
			return err
		}
	}
	return nil
}

// DecodeManifest authenticates and parses the signed manifest without choosing
// an artifact for the machine performing the verification.
func DecodeManifest(config Config, envelope Envelope, now time.Time) (*Manifest, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	key, err := signature.PublicKey(config.PublicKey)
	if err != nil {
		return nil, err
	}
	if len(envelope.Payload) > 1<<20 {
		return nil, i18n.NewError("更新清单超过大小限制")
	}
	payload, err := base64.StdEncoding.DecodeString(envelope.Payload)
	if err != nil {
		return nil, err
	}
	if err = signature.Verify(key, payload, envelope.Signature); err != nil {
		return nil, err
	}
	var manifest Manifest
	if err = json.Unmarshal(payload, &manifest); err != nil {
		return nil, err
	}
	if manifest.Schema != 1 || manifest.Expires.IsZero() || !now.Before(manifest.Expires) || len(manifest.Assets) > 32 || len(manifest.Notes) > 65536 {
		return nil, i18n.NewError("更新清单无效或已经过期")
	}
	version, err := parseSemVer(manifest.Version)
	if err != nil {
		return nil, err
	}
	if len(version.pre) > 0 {
		return nil, i18n.NewError("更新清单只接受稳定版本")
	}
	return &manifest, nil
}

func Decode(config Config, envelope Envelope, now time.Time) (*Release, error) {
	manifest, err := DecodeManifest(config, envelope, now)
	if err != nil {
		return nil, err
	}
	var chosen, installerChosen *Asset
	for _, asset := range manifest.Assets {
		if asset.OS != runtime.GOOS || asset.Arch != runtime.GOARCH {
			continue
		}
		kind, err := AssetKind(asset)
		if err != nil {
			return nil, err
		}
		if err := validateAsset(asset); err != nil {
			return nil, err
		}
		copy := asset
		if kind == "installer" {
			if installerChosen != nil {
				return nil, i18n.NewError("更新清单包含重复的平台安装包")
			}
			installerChosen = &copy
			continue
		}
		if chosen != nil {
			return nil, i18n.NewError("更新清单包含重复的平台安装包")
		}
		chosen = &copy
	}
	if chosen == nil {
		// 只有安装器升级包时按安装器升级处理。
		chosen = installerChosen
	}
	if chosen == nil {
		return nil, i18n.NewError("没有适用于当前系统和架构的更新")
	}
	return &Release{Manifest: *manifest, Asset: *chosen, Envelope: envelope}, nil
}

// A dedicated anonymous client never carries download credentials or cookies.
// Environment proxy support follows net/http. HTTPS is enforced on every hop.
func client(transport http.RoundTripper) *http.Client {
	return &http.Client{Transport: transport, Timeout: 30 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 8 {
			return i18n.NewError("更新重定向次数过多")
		}
		return secureURL(req.URL.String())
	}}
}

// statusError 把更新请求的 HTTP 状态码翻译成面向用户的提示；
// 未知状态码保留编号，便于反馈排查。
func statusError(status int) error {
	switch {
	case status == http.StatusTooManyRequests:
		return i18n.NewError("检查过于频繁，请稍后再试")
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return i18n.NewError("更新服务器拒绝了本次请求，请稍后再试")
	case status == http.StatusNotFound:
		return i18n.NewError("未找到更新信息，请检查更新源设置")
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		return i18n.NewError("更新服务器响应超时，请稍后再试")
	case status >= 500:
		return i18n.NewError("更新服务器暂时不可用，请稍后再试")
	}
	return fmt.Errorf(i18n.T("更新服务器返回异常状态，请稍后再试（HTTP %d）"), status)
}

func get(ctx context.Context, address string, transport http.RoundTripper) (*http.Response, error) {
	if err := secureURL(address); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := client(transport).Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, statusError(resp.StatusCode)
	}
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
		resp.Body.Close()
		return nil, i18n.NewError("更新响应使用了不支持的内容编码")
	}
	return resp, nil
}

func Check(ctx context.Context, config Config, current string) (*Release, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	address := config.URL
	if config.Provider == "gitcode" {
		var err error
		address, err = gitcodeManifestURL(ctx, config)
		if err != nil {
			return nil, err
		}
	}
	resp, err := get(ctx, address, config.Transport)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, i18n.NewError("更新清单超过大小限制")
	}
	var envelope Envelope
	if err = json.Unmarshal(data, &envelope); err != nil {
		return nil, err
	}
	release, err := Decode(config, envelope, time.Now())
	if err != nil {
		return nil, err
	}
	newer, err := Newer(release.Manifest.Version, current)
	if err != nil {
		return nil, err
	}
	if !newer {
		return nil, nil
	}
	return release, nil
}

func verifyArtifact(path string, asset Asset) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != asset.Size {
		return i18n.NewError("更新文件大小与签名清单不一致")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, asset.Size+1))
	if err != nil {
		return err
	}
	if n != asset.Size || !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), asset.SHA256) {
		return i18n.NewError("更新文件摘要与签名清单不一致")
	}
	return nil
}

// Stage writes beside the installed executable, so publication uses same-volume
// renames. A failed download leaves the current executable untouched.
type progressReader struct {
	r           io.Reader
	done, total int64
	report      func(int64, int64)
}

func (r *progressReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.done += int64(n)
	if n > 0 && r.report != nil {
		r.report(r.done, r.total)
	}
	return n, err
}

func Stage(ctx context.Context, config Config, release *Release, current, target string) (*Pending, error) {
	if release == nil {
		return nil, i18n.NewError("没有可安装的更新")
	}
	verified, err := Decode(config, release.Envelope, time.Now())
	if err != nil {
		return nil, err
	}
	newer, err := Newer(verified.Manifest.Version, current)
	if err != nil {
		return nil, err
	}
	if !newer {
		return nil, i18n.NewError("拒绝安装旧版本或相同版本")
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(target)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, i18n.NewError("更新目标不是普通可执行文件")
	}
	dir, err := os.MkdirTemp(filepath.Dir(target), ".OpenDL-update-")
	if err != nil {
		return nil, err
	}
	pending := &Pending{Target: target, Directory: dir, Envelope: verified.Envelope, ParentPID: os.Getpid()}
	keep := false
	defer func() {
		if !keep {
			pending.Cleanup()
		}
	}()
	resp, err := get(ctx, verified.Asset.URL, config.Transport)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	f, err := os.OpenFile(pending.candidate(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return nil, err
	}
	n, err := io.Copy(f, &progressReader{r: io.LimitReader(resp.Body, verified.Asset.Size+1), total: verified.Asset.Size, report: config.Progress})
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	if n != verified.Asset.Size {
		return nil, i18n.NewError("更新文件大小与签名清单不一致")
	}
	if err = verifyArtifact(pending.candidate(), verified.Asset); err != nil {
		return nil, err
	}
	if err = copyFile(target, pending.helper()); err != nil {
		return nil, err
	}
	pending.OriginalSHA256, err = fileDigest(pending.helper())
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(pending)
	if err != nil {
		return nil, err
	}
	if err = os.WriteFile(pending.plan(), encoded, 0600); err != nil {
		return nil, err
	}
	keep = true
	return pending, nil
}
