// Package download is the download engine the desktop app runs on behalf of
// the browser extension: queued tasks, multi-connection HTTP Range transfer,
// progress, resume and cancellation.
package download

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"openDL/pathrec"
	"openDL/signature"

	"openDL/i18n"
)

// State is a task's lifecycle state.
type State string

const (
	Queued   State = "queued"
	Running  State = "running"
	Done     State = "done"
	ErrorS   State = "error"
	Canceled State = "canceled"
)

// Request is one download order, as the extension sends it.
type Request struct {
	URL           string            `json:"url"`
	PageURL       string            `json:"pageUrl,omitempty"`
	Title         string            `json:"title,omitempty"`
	Name          string            `json:"filename,omitempty"`
	KeepExtension bool              `json:"keepExtension,omitempty"`
	Headers       map[string]string `json:"headers,omitempty"`
	// Connections is how many parallel Range connections this task uses;
	// Zero takes the manager default; -1 selects automatic adaptation.
	Connections      int    `json:"connections,omitempty"`
	Directory        string `json:"directory,omitempty"`
	OpenFolder       bool   `json:"openFolder,omitempty"`
	Queue            string `json:"queue,omitempty"`
	SHA256           string `json:"sha256,omitempty"`
	RequireSignature bool   `json:"requireSignature,omitempty"`
	Signature        string `json:"signature,omitempty"`    // Ed25519 分离签名（Base64），覆盖文件 SHA-256 摘要
	SignatureKey     string `json:"signatureKey,omitempty"` // Ed25519 公钥（Base64）
	MediaMode        string `json:"mediaMode,omitempty"`
	MediaQuality     string `json:"mediaQuality,omitempty"`
	AudioLanguage    string `json:"audioLanguage,omitempty"`
	SyncMinutes      int    `json:"syncMinutes,omitempty"`
	ClientRequestID  string `json:"clientRequestId,omitempty"`
	BrowserOrigin    bool   `json:"-"`
}

// Snapshot is an immutable copy of a task for the UI thread.
type Snapshot struct {
	ID                                   string
	URL                                  string
	Name                                 string
	FilePath                             string
	State                                State
	Total                                int64
	Done                                 int64
	Speed                                int64 // bytes per second
	Err                                  string
	Created                              time.Time
	Finished                             time.Time // zero while the task is still going
	BrowserOrigin                        bool
	Elapsed                              time.Duration // accumulated running time
	Connections                          int
	Held                                 bool
	RangeKnown, SupportsRanges           bool
	ResumeSafe                           bool
	PageURL                              string
	OpenFolder                           bool
	Queue                                string
	Night                                bool
	NightSince                           time.Time
	Segments                             []Segment
	ExpectedSHA256, VerifiedSHA256       string
	Verifying                            bool
	Reencoding                           bool
	LiveRecording                        bool
	FinishingLive                        bool
	SignatureRequired, SignatureVerified bool
	SyncMinutes                          int
	AuthProtocol                         string
	MediaMode, MediaQuality              string
	AudioLanguage                        string
}

// Task is one download; the engine mutates it behind the manager lock and
// atomic counters.
type Task struct {
	id                              string
	url                             string
	pageURL                         string
	title                           string
	nameHint                        string
	keepExtension                   bool
	mediaMode                       string
	mediaQuality                    string
	audioLanguage                   string
	queue                           string       // 队列标识；空为主队列
	syncMinutes                     int          // 资源变化同步周期；0 表示不同步
	nextSync                        time.Time    // 下次资源探测时间，由 m.mu 保护
	syncProbing                     bool         // guarded by m.mu
	syncRevision                    uint64       // guarded by m.mu; invalidates stale probes
	remoteValidator, remoteResource string       // guarded by checkpointMu
	syncOriginalSHA256              string       // guarded by metaMu; old file is retained until publication
	ticket                          *queueTicket // 当前排队或运行的名额，由 m.mu 保护
	hlsKeyMu                        sync.Mutex
	hlsKeys                         map[string][]byte // 按地址缓存的媒体密钥，不落盘
	headers                         map[string]string
	authProtocol                    string // guarded by metaMu; tokens and credentials are never persisted
	conns                           int    // per-task Range connections; 0 is the manager default
	directory                       string
	openFolder                      bool
	expectedSHA256                  string // guarded by metaMu
	signature                       string // Ed25519 分离签名，guarded by metaMu
	signatureKey                    string // Ed25519 公钥，guarded by metaMu
	verifiedSHA256                  string // guarded by metaMu
	failedSHA256                    string // guarded by metaMu; checksum of a failed verification
	requireSignature                bool   // guarded by metaMu
	signatureVerified               atomic.Bool
	publishSHA256                   string // guarded by metaMu
	publishSize                     int64  // guarded by metaMu
	verifying                       atomic.Bool
	reencoding                      atomic.Bool
	liveRecording                   atomic.Bool
	finishLive                      atomic.Bool
	liveMu                          sync.Mutex // protects the per-recording wake channel
	liveWake                        chan struct{}
	folderOpened                    atomic.Bool
	held                            atomic.Bool
	night                           atomic.Bool
	rangeSupport                    atomic.Int32 // 0 unknown, 1 no, 2 yes
	resumeSafe                      atomic.Bool

	metaMu      sync.Mutex
	filePath    string
	created     time.Time
	finished    time.Time
	elapsed     atomic.Int64
	nightSince  time.Time // guarded by metaMu
	total       atomic.Int64
	done        atomic.Int64
	outputLimit atomic.Int64 // zero when unknown or no supported filesystem limit
	transferred atomic.Int64 // bytes actually written, including retried data
	speed       atomic.Int64
	state       atomic.Value // State
	errMu       sync.Mutex
	err         string
	cancel      chan struct{}
	cancelOnce  sync.Once
	// A stopped run leaves only fully validated chunks eligible for reuse.
	completed            []chunkSpan
	resumeTotal          int64
	resumeValidator      string
	resumeResource       string // SHA-256 of the final URL, not the signed URL itself
	resumeStableResource string // GitHub release asset identity without temporary query parameters
	resumeUnverified     bool   // retained chunks were matched without proving resource identity
	hlsPlaylist          string // fingerprint of the selected playlist's resolved segments
	hlsFormat            string // empty for legacy TS; fmp4 permits non-188-byte checkpoints
	hlsCompleted         []hlsCheckpoint
	hlsSplice            *dashSpliceState        // 多周期缝合状态，与检查点同锁
	dashVideo, dashAudio dashTrackCheckpoint     // guarded by checkpointMu
	dashPeriods          [8]dashPeriodCheckpoint // guarded by checkpointMu
	checkpointMu         sync.Mutex
	removed              atomic.Bool
	needsAuth            atomic.Bool // restored credentials are deliberately not persisted
	editing              bool        // guarded by Manager.mu
	deferredForUpdate    bool        // guarded by Manager.mu
	segmentMu            sync.Mutex
	segments             []*segmentProgress
	digestMu             sync.Mutex
	digestCache          *digestCacheEntry // transient, scoped to one exact resource
	digestCounts         map[string]uint32 // guarded by digestMu; never persisted
	clientRequestID      string            // immutable submission identity, not the transport request ID
	browserOrigin        bool
	submissionSHA256     string // immutable fingerprint of non-credential request fields
}

// stop closes the cancel channel once.
func (t *Task) stop() {
	t.cancelOnce.Do(func() { close(t.cancel) })
}

func (t *Task) beginLive() <-chan struct{} {
	t.liveMu.Lock()
	defer t.liveMu.Unlock()
	t.liveWake = make(chan struct{})
	t.finishLive.Store(false)
	t.liveRecording.Store(true)
	return t.liveWake
}

func (t *Task) endLive() {
	t.liveMu.Lock()
	t.liveRecording.Store(false)
	t.liveWake = nil
	t.liveMu.Unlock()
}

// newRequest builds a request whose context dies with the task, so a cancel
// unblocks even a stalled body read.
func (t *Task) newRequest(url string) (*http.Request, context.CancelFunc, error) {
	ctx, cancel := context.WithCancel(context.Background())
	stopped := t.cancel
	go func() {
		select {
		case <-stopped:
			cancel()
		case <-ctx.Done():
		}
	}()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		cancel()
		return nil, cancel, err
	}
	t.metaMu.Lock()
	headers, pageURL := cloneHeaders(t.headers), t.pageURL
	t.metaMu.Unlock()
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("Referer") == "" {
		if referer := sourceReferer(pageURL, req.URL); referer != "" {
			req.Header.Set("Referer", referer)
		}
	}
	// Byte ranges refer to the unencoded representation, not a compressed
	// response chosen independently by each connection.
	req.Header.Set("Accept-Encoding", "identity")
	return req, cancel, nil
}

// Match the common browser strict-origin-when-cross-origin policy: strip
// credentials and fragments, limit cross-origin detail and prevent downgrade.
func sourceReferer(page string, target *url.URL) string {
	source, err := url.Parse(page)
	if err != nil || source.Host == "" || (source.Scheme != "http" && source.Scheme != "https") {
		return ""
	}
	if source.Scheme == "https" && target.Scheme == "http" {
		return ""
	}
	source.User = nil
	source.Fragment = ""
	if !strings.EqualFold(source.Scheme, target.Scheme) || !strings.EqualFold(source.Host, target.Host) {
		return source.Scheme + "://" + source.Host + "/"
	}
	return source.String()
}

func (t *Task) snapshot() Snapshot {
	s := t.snapshotBase()
	s.Segments = t.segmentSnapshots()
	return s
}

func (t *Task) snapshotBase() Snapshot {
	t.metaMu.Lock()
	filePath, finished, address, pageURL, nightSince := t.filePath, t.finished, t.url, t.pageURL, t.nightSince
	expected, verified := t.expectedSHA256, t.verifiedSHA256
	authProtocol := t.authProtocol
	requireSignature := t.requireSignature
	syncMinutes := t.syncMinutes
	mediaMode, mediaQuality, audioLanguage := t.mediaMode, t.mediaQuality, t.audioLanguage
	t.metaMu.Unlock()
	name := t.nameHint
	if filePath != "" {
		name = filepath.Base(filePath)
	}
	return Snapshot{
		ID: t.id, URL: address, Name: name, FilePath: filePath,
		State: t.state.Load().(State), Total: t.total.Load(),
		Done: t.done.Load(), Speed: t.speed.Load(), Err: t.errText(),
		Created: t.created, Finished: finished, Elapsed: time.Duration(t.elapsed.Load()), BrowserOrigin: t.browserOrigin,
		Connections: t.conns, Held: t.held.Load(), PageURL: pageURL, OpenFolder: t.openFolder, Queue: t.queue, Night: t.night.Load(),
		NightSince: nightSince,
		RangeKnown: t.rangeSupport.Load() != 0, SupportsRanges: t.rangeSupport.Load() == 2,
		ResumeSafe:     t.resumeSafe.Load(),
		ExpectedSHA256: expected, VerifiedSHA256: verified, Verifying: t.verifying.Load(), Reencoding: t.reencoding.Load(),
		LiveRecording:     t.liveRecording.Load() && !t.finishLive.Load(),
		FinishingLive:     t.finishLive.Load() && t.state.Load().(State) == Running,
		SignatureRequired: requireSignature, SignatureVerified: t.signatureVerified.Load(),
		SyncMinutes:   syncMinutes,
		AuthProtocol:  authProtocol,
		MediaMode:     mediaMode,
		MediaQuality:  mediaQuality,
		AudioLanguage: audioLanguage,
	}
}

func (t *Task) sourceURL() string {
	t.metaMu.Lock()
	defer t.metaMu.Unlock()
	return t.url
}

func (t *Task) setFinished(finished time.Time) {
	t.metaMu.Lock()
	t.finished = finished
	t.metaMu.Unlock()
}

func (t *Task) errText() string {
	t.errMu.Lock()
	defer t.errMu.Unlock()
	return t.err
}

func (t *Task) setErr(err error) {
	t.errMu.Lock()
	t.err = err.Error()
	t.errMu.Unlock()
}

// Manager owns the task queue and the worker pool.
type Manager struct {
	mu                    sync.Mutex
	tasks                 []*Task
	dir                   string
	concurrency           int
	conns                 int // default connections per task
	onChange              func()
	client                *http.Client
	proxy                 *proxyTransport
	waiting               []*queueTicket
	active                int
	storeDir              string     // configured before tasks are added
	submissionMu          sync.Mutex // serialize durable API submissions and duplicate checks
	retryMu               sync.Mutex
	retryUntil            map[string]time.Time
	bodyIdleTimeout       time.Duration
	allowUnverifiedResume atomic.Bool
	overwriteExisting     atomic.Bool

	pace              paceLimiter
	quota             trafficQuota
	limiter           *connLimiter
	siteLogins        []SiteLogin
	windowsAuth       []WindowsAuthSite
	windowsAuthError  error
	mediaMuxProgram   string // guarded by mu; empty means system PATH
	updateStarting    bool   // guarded by mu
	categoryDirs      map[string]string
	categoryFallback  string
	queueLimits       map[string]int
	queueActive       map[string]int
	spaceMu           sync.Mutex
	spaceReservations map[string]spaceReservation
	queuePaceMu       sync.Mutex
	queuePaces        map[string]*paceLimiter
	siteConnMu        sync.Mutex
	siteConns         map[string]int
}

// ValidQueueID 报告队列标识是否合法：内置的 main/night 或自定义短标识。
func ValidQueueID(queue string) bool {
	if queue == "" || queue == "main" || queue == "night" {
		return true
	}
	if len(queue) > 32 {
		return false
	}
	for i := 0; i < len(queue); i++ {
		c := queue[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			continue
		}
		return false
	}
	return true
}

// MoveToQueue 把任务移到另一个队列：等待中的任务按新队列的上限重新排队，
// 运行中的任务继续下载并计到新队列；夜间任务直接迁移时退出夜间等待。
func (m *Manager) MoveToQueue(id, queue string) bool {
	if !ValidQueueID(queue) {
		return false
	}
	m.mu.Lock()
	moved := false
	for _, t := range m.tasks {
		if t.id != id || t.removed.Load() {
			continue
		}
		if t.night.Load() {
			// 夜间任务直接迁移即退出夜间语义：取消标记与等待，交由新队列排队。
			t.night.Store(false)
			t.held.Store(false)
			t.metaMu.Lock()
			t.nightSince = time.Time{}
			t.metaMu.Unlock()
		}
		old := t.queue
		if old == queue {
			moved = true
			break
		}
		t.queue = queue
		if ticket := t.ticket; ticket != nil && ticket.granted && !ticket.released {
			// 运行中的任务继续下载，把占用的名额计到新队列。
			ticket.queue = queue
			m.queueActive[old]--
			m.queueActive[queue]++
		} else {
			if ticket := t.ticket; ticket != nil {
				ticket.queue = queue
			}
			m.dispatchLocked()
		}
		moved = true
		break
	}
	m.mu.Unlock()
	if moved {
		m.notify()
	}
	return moved
}

// SetSiteConnectionLimits 设置每站点的连接数上限；键为主机名，匹配含子域。
func (m *Manager) SetSiteConnectionLimits(limits map[string]int) {
	m.siteConnMu.Lock()
	defer m.siteConnMu.Unlock()
	m.siteConns = make(map[string]int, len(limits))
	for host, limit := range limits {
		if limit > 0 {
			m.siteConns[strings.ToLower(strings.TrimSpace(host))] = limit
		}
	}
}

// siteConnectionLimit 返回地址命中的每站点连接上限；未配置返回 0。
func (m *Manager) siteConnectionLimit(address string) int {
	u, err := url.Parse(address)
	if err != nil {
		return 0
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return 0
	}
	m.siteConnMu.Lock()
	defer m.siteConnMu.Unlock()
	if limit := m.siteConns[host]; limit > 0 {
		return limit
	}
	for site, limit := range m.siteConns {
		if site != "" && strings.HasSuffix(host, "."+site) {
			return limit
		}
	}
	return 0
}

// SetQueueRates 设置自定义队列的速度额度（字节/秒）；0 或缺省表示不限速。
func (m *Manager) SetQueueRates(rates map[string]int64) {
	m.queuePaceMu.Lock()
	defer m.queuePaceMu.Unlock()
	if m.queuePaces == nil {
		m.queuePaces = map[string]*paceLimiter{}
	}
	for queue, rate := range rates {
		if rate <= 0 {
			delete(m.queuePaces, queue)
			continue
		}
		limiter, ok := m.queuePaces[queue]
		if !ok {
			limiter = &paceLimiter{}
			m.queuePaces[queue] = limiter
		}
		limiter.setRate(rate)
	}
	for queue := range m.queuePaces {
		if _, ok := rates[queue]; !ok {
			delete(m.queuePaces, queue)
		}
	}
}

// queuePace 返回任务队列的速度额度；未配置时返回 nil。主队列以 main 标识。
func (m *Manager) queuePace(queue string) *paceLimiter {
	if queue == "" {
		queue = "main"
	}
	m.queuePaceMu.Lock()
	defer m.queuePaceMu.Unlock()
	return m.queuePaces[queue]
}

// acquirePaced 先取队列额度再取全局额度；持有较窄的队列额度等待全局额度，
// 不挤压其他队列。返回本次可读字节数与退还函数。
func (m *Manager) acquirePaced(t *Task, size int, canceled <-chan struct{}) (int, func(int), error) {
	limiter := m.queuePace(t.queue)
	var queueAllowance readAllowance
	if limiter != nil {
		allowance, err := limiter.acquire(size, canceled)
		if err != nil {
			return 0, nil, err
		}
		queueAllowance = allowance
	}
	allowance, err := m.pace.acquire(size, canceled)
	if err != nil {
		if limiter != nil {
			limiter.refund(queueAllowance, 0)
		}
		return 0, nil, err
	}
	bytes := allowance.bytes
	if limiter != nil && queueAllowance.bytes < bytes {
		bytes = queueAllowance.bytes
	}
	return bytes, func(actual int) {
		m.pace.refund(allowance, actual)
		if limiter != nil {
			limiter.refund(queueAllowance, actual)
		}
	}, nil
}

// SetQueueLimits 设置队列的并发上限；0 或缺省表示只受全局上限约束。主队
// 列以 main 标识接受并按空标识存储。调高上限会立即唤醒等待中的任务。
func (m *Manager) SetQueueLimits(limits map[string]int) {
	cleaned := make(map[string]int, len(limits))
	for queue, limit := range limits {
		if limit < 0 || limit > 64 {
			continue
		}
		key := queue
		switch {
		case queue == "main":
			key = ""
		case queue == "":
			continue
		case queue != "night" && !ValidQueueID(queue):
			continue
		}
		cleaned[key] = limit
	}
	m.mu.Lock()
	m.queueLimits = cleaned
	m.dispatchLocked()
	m.mu.Unlock()
}

// SiteLogin 是站点登录表条目：主机（含子域）匹配的下载自动使用基本认证，
// 同一地址上的 Digest 挑战复用同一份凭据。
type SiteLogin struct {
	Host, User, Password string
}

// SetSiteLogins 替换站点登录表，只影响尚未提交的下载。
func (m *Manager) SetSiteLogins(logins []SiteLogin) {
	m.mu.Lock()
	m.siteLogins = append([]SiteLogin(nil), logins...)
	m.mu.Unlock()
}

// SetCategoryDirs 设置分类归档：扩展名（含点）到默认目录下的子目录，
// fallback 用于未匹配的扩展名。只有未指定目录（或指定的就是默认目录）的
// 任务才按分类归档。
func (m *Manager) SetCategoryDirs(byExt map[string]string, fallback string) {
	dirs := make(map[string]string, len(byExt))
	for ext, subdir := range byExt {
		if clean := CleanCategoryDir(subdir); clean != "" {
			dirs[strings.ToLower(ext)] = clean
		}
	}
	m.mu.Lock()
	m.categoryDirs = dirs
	m.categoryFallback = CleanCategoryDir(fallback)
	m.mu.Unlock()
}

// CleanCategoryDir 只接受不逃逸默认目录的相对子目录；空值表示不归档。
func CleanCategoryDir(subdir string) string {
	subdir = strings.TrimSpace(subdir)
	if subdir == "" {
		return ""
	}
	if filepath.IsAbs(subdir) || strings.Contains(subdir, "..") {
		return ""
	}
	cleaned := filepath.Clean(subdir)
	if cleaned == "." || cleaned == ".." || filepath.IsAbs(cleaned) {
		return ""
	}
	return cleaned
}

// applyCategoryDir 把未指定目录的任务归档到默认目录的分类子目录。
func (m *Manager) applyCategoryDir(req *Request) {
	if req == nil || req.URL == "" {
		return
	}
	ext := ""
	if u, err := url.Parse(req.URL); err == nil {
		ext = strings.ToLower(filepath.Ext(u.Path))
	}
	if req.Name != "" {
		if name := strings.ToLower(filepath.Ext(req.Name)); name != "" {
			ext = name
		}
	}
	if req.MediaMode == "hls-ts" {
		ext = ".ts"
	} else if req.MediaMode == "dash-fmp4" {
		ext = ".mp4"
	}
	if ext == "" {
		return
	}
	m.mu.Lock()
	base := m.dir
	subdir := m.categoryDirs[ext]
	if subdir == "" {
		subdir = m.categoryFallback
	}
	m.mu.Unlock()
	if subdir == "" {
		return
	}
	requested := strings.TrimSpace(req.Directory)
	if requested != "" && filepath.Clean(requested) != filepath.Clean(base) {
		return
	}
	req.Directory = filepath.Join(base, subdir)
}

// applySiteLogin 为没有显式认证的请求注入站点登录表中的基本认证。
// SiteAuthorization 返回站点登录表中匹配地址的基本认证头；显式配置
// Windows 集成认证的站点不返回，由请求层自行协商。
func (m *Manager) SiteAuthorization(u *url.URL) string {
	if u == nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return ""
	}
	if _, configured := m.windowsAuthFor(u); configured {
		return ""
	}
	m.mu.Lock()
	logins := append([]SiteLogin(nil), m.siteLogins...)
	m.mu.Unlock()
	for _, login := range logins {
		site := strings.ToLower(strings.TrimSpace(login.Host))
		if site == "" || (host != site && !strings.HasSuffix(host, "."+site)) {
			continue
		}
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(login.User+":"+login.Password))
	}
	return ""
}

func (m *Manager) applySiteLogin(req *Request) {
	if req == nil || req.URL == "" || req.Headers["Authorization"] != "" {
		return
	}
	u, err := url.Parse(req.URL)
	if err != nil {
		return
	}
	if auth := m.SiteAuthorization(u); auth != "" {
		if req.Headers == nil {
			req.Headers = map[string]string{}
		}
		req.Headers["Authorization"] = auth
	}
}

// SetRateLimit caps the transfer rate of every task in bytes per second;
// zero is unlimited.
func (m *Manager) SetRateLimit(bps int64) {
	if bps < 0 {
		bps = 0
	}
	m.pace.setRate(bps)
}

// RateLimit reports the cap in bytes per second, zero when unlimited.
func (m *Manager) RateLimit() int64 { return m.pace.getRate() }

// SetTrafficQuota 配置流量配额：period 为 day/week/month，limit 为字节；
// period 为空表示不限制。用量统计与到期恢复由引擎自动处理。
func (m *Manager) SetTrafficQuota(period string, limit int64) error {
	if err := m.quota.set(period, limit); err != nil {
		return err
	}
	m.notify()
	return nil
}

// QuotaExhausted 报告流量配额是否已用完。
func (m *Manager) QuotaExhausted() bool { return m.quota.exhausted() }

// SetMaxConnections caps how many transfers run at once across every task;
// zero is unlimited. Transfers already running keep their connection and the
// new cap applies to the next request.
func (m *Manager) SetMaxConnections(n int) { m.limiter.setLimit(n) }

// MaxConnections reports the global transfer cap, zero when unlimited.
func (m *Manager) MaxConnections() int { return m.limiter.getLimit() }

// SetAllowUnverifiedResume permits reuse of ordinary HTTP chunks when a
// refreshed resource cannot be identified. It does not disable SHA-256 checks.
func (m *Manager) SetAllowUnverifiedResume(allow bool) { m.allowUnverifiedResume.Store(allow) }

// SetOverwriteExisting 让下载替换目标目录中的同名文件，而不是创建带编号的副本。
func (m *Manager) SetOverwriteExisting(overwrite bool) { m.overwriteExisting.Store(overwrite) }

// NewManager creates a manager saving into dir with at most parallel tasks
// and conns connections each.
func NewManager(dir string, parallel, conns int) *Manager {
	if parallel < 1 {
		parallel = 1
	}
	if conns < 1 && conns != -1 {
		conns = 1
	}
	client := &http.Client{Timeout: 0, CheckRedirect: checkDownloadRedirect}
	transport := http.DefaultTransport
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		clone := base.Clone()
		clone.ResponseHeaderTimeout = 30 * time.Second
		transport = clone
	}
	m := &Manager{
		dir:               dir,
		concurrency:       parallel,
		conns:             conns,
		client:            client,
		bodyIdleTimeout:   30 * time.Second,
		limiter:           newConnLimiter(),
		queueActive:       map[string]int{},
		spaceReservations: map[string]spaceReservation{},
	}
	m.proxy = &proxyTransport{base: transport, current: transport}
	client.Transport = &originTransport{base: m.proxy, limit: 32, before: m.waitOrigin, cooling: m.isOriginCooling, limiter: m.limiter}
	return m
}

// SetDownloadDefaults changes defaults for future tasks; existing tasks keep
// the directory and connection count captured when they were created.
func (m *Manager) SetDownloadDefaults(dir string, conns int) {
	if dir == "" || (conns < 1 && conns != -1) {
		return
	}
	m.mu.Lock()
	m.dir, m.conns = dir, conns
	m.mu.Unlock()
}

// OnChange sets the callback the manager runs after every state change, for
// the UI to redraw. It runs off the UI thread.
func (m *Manager) OnChange(fn func()) {
	m.mu.Lock()
	m.onChange = fn
	m.mu.Unlock()
}

func (m *Manager) notify() {
	m.mu.Lock()
	fn := m.onChange
	m.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// Add queues a request and starts it when a worker is free.
func (m *Manager) Add(req Request) string {
	return m.add(req, false)
}

// AddRecorded writes the initial journal before exposing or starting a task.
// A request for the night queue remains held until the scheduler starts it.
// The caller may perform disk I/O and must not call this on the UI thread.
func (m *Manager) AddRecorded(req Request) (string, error) {
	if !ValidClientRequestID(req.ClientRequestID) {
		return "", fmt.Errorf(i18n.T("下载请求编号格式无效"))
	}
	m.applySiteLogin(&req)
	m.applyCategoryDir(&req)
	var fingerprint string
	if req.ClientRequestID != "" {
		var err error
		fingerprint, err = submissionFingerprint(req)
		if err != nil {
			return "", err
		}
	}
	m.submissionMu.Lock()
	locked := true
	defer func() {
		if locked {
			m.submissionMu.Unlock()
		}
	}()
	m.mu.Lock()
	updating := m.updateStarting
	m.mu.Unlock()
	if updating {
		return "", ErrUpdateInProgress
	}
	if req.ClientRequestID != "" {
		m.mu.Lock()
		for _, existing := range m.tasks {
			if existing.clientRequestID == req.ClientRequestID {
				m.mu.Unlock()
				if existing.submissionSHA256 != fingerprint {
					return "", ErrRequestIDConflict
				}
				return existing.id, nil
			}
		}
		m.mu.Unlock()
	}
	t := &Task{
		id: newID(), url: req.URL, pageURL: req.PageURL,
		title: req.Title, nameHint: req.Name, keepExtension: req.KeepExtension, mediaMode: req.MediaMode, mediaQuality: req.MediaQuality, audioLanguage: req.AudioLanguage, queue: req.Queue, syncMinutes: req.SyncMinutes, headers: cloneHeaders(req.Headers),
		conns: req.Connections, directory: req.Directory, openFolder: req.OpenFolder, expectedSHA256: req.SHA256, signature: req.Signature, signatureKey: req.SignatureKey, requireSignature: req.RequireSignature,
		cancel: make(chan struct{}), created: time.Now(),
		clientRequestID: req.ClientRequestID, submissionSHA256: fingerprint, browserOrigin: req.BrowserOrigin,
	}
	t.state.Store(Queued)
	if req.Queue == "night" {
		t.held.Store(true)
		t.night.Store(true)
		t.nightSince = t.created
	}
	m.mu.Lock()
	if t.conns == 0 {
		t.conns = m.conns
	}
	if limit := m.siteConnectionLimit(req.URL); limit > 0 && t.conns > limit {
		t.conns = limit
	}
	if t.directory == "" {
		t.directory = m.dir
	}
	m.mu.Unlock()
	if err := m.saveTask(t); err != nil {
		return "", fmt.Errorf(i18n.T("无法保存下载记录：%w"), err)
	}
	m.mu.Lock()
	m.tasks = append(m.tasks, t)
	var ticket *queueTicket
	if !t.held.Load() {
		ticket = m.enqueueLocked(t)
	}
	m.mu.Unlock()
	m.submissionMu.Unlock()
	locked = false
	m.notify()
	if ticket != nil {
		go m.run(t, ticket)
	}
	return t.id, nil
}

// AddPaused keeps a request in the main queue until Resume is called.
func (m *Manager) AddPaused(req Request) string { return m.add(req, true) }

// AddNight retains a task in the scheduled queue until explicitly started.
func (m *Manager) AddNight(req Request) string {
	req.Queue = "night"
	return m.add(req, true)
}

func (m *Manager) add(req Request, held bool) string {
	m.applySiteLogin(&req)
	m.applyCategoryDir(&req)
	id := newID()
	t := &Task{
		id: id, url: req.URL, pageURL: req.PageURL,
		title: req.Title, nameHint: req.Name, keepExtension: req.KeepExtension, mediaMode: req.MediaMode, mediaQuality: req.MediaQuality, audioLanguage: req.AudioLanguage, queue: req.Queue, syncMinutes: req.SyncMinutes, headers: cloneHeaders(req.Headers),
		conns: req.Connections, directory: req.Directory, openFolder: req.OpenFolder, expectedSHA256: req.SHA256, signature: req.Signature, signatureKey: req.SignatureKey, requireSignature: req.RequireSignature,
		cancel: make(chan struct{}), created: time.Now(), browserOrigin: req.BrowserOrigin,
	}
	t.held.Store(held)
	t.night.Store(held && req.Queue == "night")
	if t.night.Load() {
		t.nightSince = t.created
	}
	t.state.Store(Queued)
	m.mu.Lock()
	persistImmediately := m.updateStarting
	if m.updateStarting && !held {
		held = true
		t.held.Store(true)
		t.deferredForUpdate = true
	}
	if t.conns == 0 {
		t.conns = m.conns
	}
	if limit := m.siteConnectionLimit(req.URL); limit > 0 && t.conns > limit {
		t.conns = limit
	}
	if t.directory == "" {
		t.directory = m.dir
	}
	m.tasks = append(m.tasks, t)
	var ticket *queueTicket
	if !held {
		ticket = m.enqueueLocked(t)
	}
	m.mu.Unlock()
	m.notify()
	if !held {
		go m.run(t, ticket)
	} else if persistImmediately {
		m.saveTaskLog(t)
	} else {
		go m.saveTaskLog(t)
	}
	return id
}

func cloneHeaders(src map[string]string) map[string]string {
	if src == nil {
		return nil
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// Request returns a detached copy, preserving options when moving or retrying.
func (m *Manager) Request(id string) (Request, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tasks {
		if t.id == id {
			return taskRequest(t), true
		}
	}
	return Request{}, false
}

// taskRequest is called with m.mu held and returns detached headers.
func taskRequest(t *Task) Request {
	t.metaMu.Lock()
	address, pageURL, headers, expected := t.url, t.pageURL, cloneHeaders(t.headers), t.expectedSHA256
	signatureText, signatureKey := t.signature, t.signatureKey
	requireSignature := t.requireSignature
	t.metaMu.Unlock()
	queue := ""
	if t.night.Load() {
		queue = "night"
	}
	return Request{URL: address, PageURL: pageURL, Title: t.title, Name: t.nameHint, KeepExtension: t.keepExtension, MediaMode: t.mediaMode, MediaQuality: t.mediaQuality, AudioLanguage: t.audioLanguage, SyncMinutes: t.syncMinutes, Headers: headers, Connections: t.conns, Directory: t.directory, OpenFolder: t.openFolder, Queue: queue, SHA256: expected, RequireSignature: requireSignature, Signature: signatureText, SignatureKey: signatureKey, BrowserOrigin: t.browserOrigin}
}

// Redownload replaces a task only after confirming its original credentials
// are available and no source edit or file verification is in progress.
func (m *Manager) Redownload(id string) (string, error) {
	m.mu.Lock()
	if m.updateStarting {
		m.mu.Unlock()
		return "", ErrUpdateInProgress
	}
	for i, t := range m.tasks {
		if t.id != id {
			continue
		}
		if t.editing || t.verifying.Load() {
			m.mu.Unlock()
			return "", fmt.Errorf(i18n.T("任务正在更新或校验，请稍后重试"))
		}
		if t.needsAuth.Load() && !m.canSystemAuthenticateLocked(t) {
			m.mu.Unlock()
			return "", fmt.Errorf(i18n.T("登录信息未保存，请先更新下载信息；已完成任务请重新添加下载"))
		}
		req := taskRequest(t)
		if t.night.Load() {
			t.metaMu.Lock()
			if t.filePath != "" {
				req.Name = filepath.Base(t.filePath)
			}
			t.metaMu.Unlock()
		}
		req.Queue = ""
		t.stop()
		t.removed.Store(true)
		m.tasks = append(m.tasks[:i], m.tasks[i+1:]...)
		m.mu.Unlock()
		go m.removeTaskJournal(t)
		return m.Add(req), nil
	}
	m.mu.Unlock()
	return "", fmt.Errorf(i18n.T("任务不存在"))
}

func (m *Manager) Concurrency() int { return m.concurrency }

// HasMaintenanceWork reports task work that can run while a task still shows
// Done, such as an independent signature check or a resource-sync probe.
func (m *Manager) HasMaintenanceWork() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tasks {
		if !t.removed.Load() && (t.syncProbing || t.editing || t.verifying.Load()) {
			return true
		}
	}
	return false
}

var ErrUpdateInProgress = i18n.NewError("应用正在准备更新，请稍后再操作")

// PrepareForUpdate serializes with durable submissions and checks task work
// while holding the scheduler lock. New work remains frozen until exit or abort.
func (m *Manager) PrepareForUpdate() bool {
	if !m.submissionMu.TryLock() {
		return false
	}
	defer m.submissionMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.updateStarting || m.active > 0 {
		return false
	}
	for _, t := range m.tasks {
		state := t.state.Load().(State)
		if !t.removed.Load() && (t.syncProbing || t.editing || t.verifying.Load() || state == Running || (state == Queued && !t.held.Load())) {
			return false
		}
	}
	m.updateStarting = true
	return true
}

// AbortUpdate restores work deferred solely during a failed helper launch.
func (m *Manager) AbortUpdate() {
	type launch struct {
		task   *Task
		ticket *queueTicket
	}
	var starts []launch
	m.mu.Lock()
	if !m.updateStarting {
		m.mu.Unlock()
		return
	}
	m.updateStarting = false
	for _, t := range m.tasks {
		if !t.deferredForUpdate {
			continue
		}
		t.deferredForUpdate = false
		if !t.removed.Load() && t.held.Load() && !t.night.Load() && t.state.Load().(State) == Queued {
			t.held.Store(false)
			starts = append(starts, launch{t, m.enqueueLocked(t)})
		}
	}
	m.mu.Unlock()
	for _, item := range starts {
		go m.run(item.task, item.ticket)
	}
	m.notify()
}

// ClaimOpenFolder delivers the completion action at most once per task run,
// even if several notifications observe completion concurrently.
func (m *Manager) ClaimOpenFolder(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tasks {
		if t.id == id && t.openFolder && t.state.Load().(State) == Done {
			return t.folderOpened.CompareAndSwap(false, true)
		}
	}
	return false
}

func (m *Manager) Resume(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.updateStarting {
		return false
	}
	for _, t := range m.tasks {
		if t.id != id {
			continue
		}
		if t.editing || t.verifying.Load() || (t.needsAuth.Load() && !t.hasPublicationIntent() && !m.canSystemAuthenticateLocked(t)) {
			return false
		}
		if t.night.Load() {
			return false
		}
		if !t.held.CompareAndSwap(true, false) {
			state := t.state.Load().(State)
			if state != Canceled && state != ErrorS {
				return false
			}
			t.cancel = make(chan struct{})
			t.cancelOnce = sync.Once{}
			t.setFinished(time.Time{})
			t.errMu.Lock()
			t.err = ""
			t.errMu.Unlock()
			t.state.Store(Queued)
		} else {
			select {
			case <-t.cancel:
				t.cancel = make(chan struct{})
				t.cancelOnce = sync.Once{}
			default:
			}
			t.setFinished(time.Time{})
			t.errMu.Lock()
			t.err = ""
			t.errMu.Unlock()
		}
		t.finishLive.Store(false)
		ticket := m.enqueueLocked(t)
		go m.run(t, ticket)
		return true
	}
	return false
}

// MoveToNight retains the task identity and completed data. Active work is
// canceled first; it becomes held once its worker has closed the file.
func (m *Manager) MoveToNight(id string) bool {
	m.mu.Lock()
	for _, t := range m.tasks {
		if t.id != id {
			continue
		}
		state := t.state.Load().(State)
		if t.editing || t.verifying.Load() || state == Done || t.night.Load() {
			m.mu.Unlock()
			return false
		}
		t.metaMu.Lock()
		t.nightSince = time.Now()
		t.metaMu.Unlock()
		t.night.Store(true)
		if (state == Running || state == Queued) && !t.held.Load() {
			t.stop()
		} else {
			t.held.Store(true)
			if state != ErrorS {
				t.state.Store(Queued)
			}
		}
		m.mu.Unlock()
		go m.saveTaskLog(t)
		m.notify()
		return true
	}
	m.mu.Unlock()
	return false
}

// MoveToMain changes queue without replacing the task; start requests resume.
func (m *Manager) MoveToMain(id string, start bool) bool {
	m.mu.Lock()
	if start && m.updateStarting {
		m.mu.Unlock()
		return false
	}
	for _, t := range m.tasks {
		if t.id != id {
			continue
		}
		if t.editing || t.verifying.Load() || !t.night.Load() || !t.held.Load() || (start && t.needsAuth.Load() && !t.hasPublicationIntent() && !m.canSystemAuthenticateLocked(t)) {
			m.mu.Unlock()
			return false
		}
		t.night.Store(false)
		m.mu.Unlock()
		if start {
			return m.Resume(id)
		}
		go m.saveTaskLog(t)
		m.notify()
		return true
	}
	m.mu.Unlock()
	return false
}

func (m *Manager) finishCanceled(t *Task) {
	m.mu.Lock()
	t.setFinished(time.Now())
	if t.night.Load() {
		t.held.Store(true)
		t.state.Store(Queued)
	} else {
		t.state.Store(Canceled)
	}
	m.mu.Unlock()
}

// Cancel stops a running task or removes a queued one.
func (m *Manager) Cancel(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var t *Task
	for _, x := range m.tasks {
		if x.id == id {
			t = x
		}
	}
	if t == nil {
		return
	}
	if t.night.Load() {
		t.night.Store(false)
	}
	if t.held.CompareAndSwap(true, false) {
		t.stop()
		t.state.Store(Canceled)
		go m.saveTaskLog(t)
		go m.notify()
		return
	}
	switch t.state.Load().(State) {
	case Done, ErrorS, Canceled:
		return
	}
	// The worker records the canceled state once its files are closed, so
	// callers and tests never observe a half-closed transfer.
	t.stop()
}

// FinishLive requests a graceful end after the current batch of complete
// segments; unlike Cancel, it leaves the task context usable for publication.
func (m *Manager) FinishLive(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tasks {
		if t.id == id && !t.removed.Load() && t.state.Load().(State) == Running {
			select {
			case <-t.cancel:
				return false
			default:
			}
			t.liveMu.Lock()
			accepted := t.liveRecording.Load() && t.liveWake != nil && t.finishLive.CompareAndSwap(false, true)
			if accepted {
				close(t.liveWake)
			}
			t.liveMu.Unlock()
			if accepted {
				go m.notify()
			}
			return accepted
		}
	}
	return false
}

// ResumeAll 继续全部可恢复的任务（暂停/取消/出错），返回继续数量；夜间等
// 待的任务需先移出夜间或到计划窗口执行。
func (m *Manager) ResumeAll() int {
	m.mu.Lock()
	ids := make([]string, 0, len(m.tasks))
	for _, t := range m.tasks {
		if t.removed.Load() {
			continue
		}
		if t.night.Load() {
			continue
		}
		state := t.state.Load().(State)
		if t.held.Load() || state == Canceled || state == ErrorS {
			ids = append(ids, t.id)
		}
	}
	m.mu.Unlock()
	count := 0
	for _, id := range ids {
		if m.Resume(id) {
			count++
		}
	}
	return count
}

// PauseAll 停止全部运行与排队中的任务并保留可恢复状态，返回停止数量。
func (m *Manager) PauseAll() int {
	m.mu.Lock()
	ids := make([]string, 0, len(m.tasks))
	for _, t := range m.tasks {
		if t.removed.Load() {
			continue
		}
		switch t.state.Load().(State) {
		case Queued, Running:
			ids = append(ids, t.id)
		}
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.Cancel(id)
	}
	return len(ids)
}

// Snapshots returns task copies ordered by creation time.
func (m *Manager) Snapshots() []Snapshot {
	m.mu.Lock()
	ts := append([]*Task(nil), m.tasks...)
	m.mu.Unlock()
	out := make([]Snapshot, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.snapshot())
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

// Remove deletes a finished task from the list; a live task must be
// canceled first.
func (m *Manager) Remove(id string) {
	removed := false
	var removedTask *Task
	m.mu.Lock()
	for i, t := range m.tasks {
		if t.id != id {
			continue
		}
		// Removing a live task cancels it first; the worker keeps its own
		// reference and finishes closing quietly in the background.
		t.stop()
		t.removed.Store(true)
		removedTask = t
		m.tasks = append(m.tasks[:i], m.tasks[i+1:]...)
		removed = true
		break
	}
	m.mu.Unlock()
	if removed {
		go m.removeTaskJournal(removedTask)
		m.notify()
	}
}

// run downloads one task: probe headers, derive the name, then transfer.
func (m *Manager) run(t *Task, ticket *queueTicket) {
	defer m.releaseSpace(t.id)
	defer m.releaseTicket(ticket)
	if err := m.saveTask(t); err != nil {
		select {
		case <-ticket.cancel:
			m.finishCanceled(t)
		default:
			t.setErr(fmt.Errorf(i18n.T("无法保存下载记录：%w"), err))
			t.state.Store(ErrorS)
		}
		m.notify()
		return
	}
	select {
	case <-ticket.ready:
	case <-ticket.cancel:
		m.finishCanceled(t)
		m.saveTaskLog(t)
		m.notify()
		return
	}
	// Cancellation and a free slot may become ready simultaneously.
	select {
	case <-ticket.cancel:
		m.finishCanceled(t)
		m.saveTaskLog(t)
		m.notify()
		return
	default:
	}
	t.state.Store(Running)
	runStarted := time.Now()
	m.saveTaskLog(t)
	m.notify()
	speedStop := make(chan struct{})
	speedDone := make(chan struct{})
	go m.sampleSpeed(t, speedStop, speedDone)
	var downloadErr error
	for attempt := 0; attempt < maxRequestAttempts; attempt++ {
		downloadErr = m.download(t)
		var readErr *responseReadError
		if downloadErr == nil || !errors.As(downloadErr, &readErr) || !transientError(readErr) || attempt == maxRequestAttempts-1 {
			break
		}
		if err := waitRetry(t.cancel, time.Duration(250*(1<<attempt))*time.Millisecond); err != nil {
			downloadErr = err
			break
		}
	}
	close(speedStop)
	<-speedDone
	t.elapsed.Add(int64(time.Since(runStarted)))
	if err := downloadErr; err != nil {
		select {
		case <-t.cancel:
			m.finishCanceled(t)
		default:
			t.setErr(describeStorageError(err))
			t.setFinished(time.Now())
			t.state.Store(ErrorS)
		}
	} else {
		m.mu.Lock()
		t.night.Store(false)
		t.setFinished(time.Now())
		t.state.Store(Done)
		m.mu.Unlock()
	}
	t.speed.Store(0)
	m.saveTaskLog(t)
	m.notify()
}

func (m *Manager) download(t *Task) error {
	t.metaMu.Lock()
	requireSignature := t.requireSignature
	syncMinutes := t.syncMinutes
	t.metaMu.Unlock()
	if _, err := NormalizeSyncMinutes(syncMinutes); err != nil {
		return err
	}
	if syncMinutes > 0 && t.mediaMode != "" {
		return i18n.NewError("周期同步仅支持普通 HTTP 或 HTTPS 文件")
	}
	if syncMinutes > 0 && !strings.HasPrefix(t.sourceURL(), "http://") && !strings.HasPrefix(t.sourceURL(), "https://") {
		return i18n.NewError("周期同步仅支持普通 HTTP 或 HTTPS 文件")
	}
	if requireSignature && !signature.Available {
		return i18n.NewError("当前平台不支持 Windows 文件数字签名验证")
	}
	if handled, err := m.resumePublication(t); handled {
		return err
	}
	if t.needsAuth.Load() && !m.canSystemAuthenticate(t) {
		return fmt.Errorf(i18n.T("恢复任务需要重新获取登录信息或下载链接"))
	}
	if lower := strings.ToLower(t.sourceURL()); strings.HasPrefix(lower, "ftp:") || strings.HasPrefix(lower, "ftps:") {
		return m.downloadFTP(t)
	}
	if t.mediaMode == "dash-fmp4" {
		return m.downloadDASH(t)
	}
	if t.mediaMode == "hls-ts" {
		return m.downloadHLS(t)
	}
	if t.mediaMode != "" {
		return fmt.Errorf(i18n.T("不支持的媒体下载模式"))
	}
	t.metaMu.Lock()
	expected := t.expectedSHA256
	t.metaMu.Unlock()
	expected, err := NormalizeSHA256(expected)
	if err != nil {
		return err
	}
	t.rangeSupport.Store(0)
	t.resumeSafe.Store(false)
	address := t.sourceURL()
	req, cancelReq, err := t.newRequest(address)
	if err != nil {
		return err
	}
	defer cancelReq()
	// Captured browser requests may carry a Range header for a media segment.
	// The initial request must describe the complete representation.
	req.Header.Del("Range")
	req.Header.Del("If-Range")
	resp, err := m.doWithRetry(t, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rangeHeader := strings.TrimSpace(strings.Join(resp.Header.Values("Accept-Ranges"), ","))
	if rangeHeader != "" {
		t.rangeSupport.Store(1)
		for _, unit := range strings.Split(rangeHeader, ",") {
			if strings.EqualFold(strings.TrimSpace(unit), "bytes") {
				t.rangeSupport.Store(2)
				break
			}
		}
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return fmt.Errorf(i18n.T("服务器返回了不支持的内容编码：%s"), encoding)
	}
	validator := strongValidator(resp.Header)
	t.checkpointMu.Lock()
	t.remoteValidator = syncValidator(resp.Header)
	if resp.Request != nil && resp.Request.URL != nil {
		t.remoteResource = resourceFingerprint(resp.Request.URL)
	}
	t.checkpointMu.Unlock()

	filenameURL := address
	// Download endpoints may redirect to a file whose name exists only in the
	// final URL. Keep an explicit original filename and the response header as
	// higher-priority sources.
	initialExt := strings.ToLower(filepath.Ext(pathrec.FromURLPath(address)))
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL := resp.Request.URL.String()
		finalExt := filepath.Ext(pathrec.FromURLPath(finalURL))
		if finalExt != "" && (initialExt == "" || initialExt == ".html" || initialExt == ".htm" || initialExt == ".php" || initialExt == ".asp" || initialExt == ".aspx" || initialExt == ".jsp") {
			filenameURL = finalURL
		}
	}
	originalName := pathrec.DeriveFilename(pathrec.Source{
		URL: filenameURL, ContentDisposition: resp.Header.Get("Content-Disposition"),
		ContentType: resp.Header.Get("Content-Type"), Title: t.title,
	})
	name := t.nameHint
	if name == "" {
		name = originalName
	} else if t.keepExtension {
		name = preserveExtension(name, originalName)
	} else {
		name = pathrec.SanitizeFilename(name)
	}
	if err := os.MkdirAll(t.directory, 0o755); err != nil {
		return err
	}
	t.metaMu.Lock()
	filePath := t.filePath
	t.metaMu.Unlock()
	part := filePath + ".part"
	if filePath == "" {
		filePath, part, err = claimPath(t.directory, name, m.overwriteExisting.Load())
		if err != nil {
			return err
		}
		t.metaMu.Lock()
		t.filePath = filePath
		t.metaMu.Unlock()
	}

	total := resp.ContentLength
	t.total.Store(total)
	t.resumeSafe.Store(total >= 256<<10 && t.rangeSupport.Load() == 2 && (validator != "" || expected != ""))

	// Single stream transfer; the multi-connection split below takes over
	// when the server supports ranges and the size is known.
	out, err := os.OpenFile(extendedPath(part), os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()

	if err := m.prepareOutputLimit(t, out, total); err != nil {
		return err
	}
	allowUnverified := expected == "" && m.allowUnverifiedResume.Load()
	if total >= 256<<10 && t.rangeSupport.Load() != 1 && (validator != "" || expected != "" || allowUnverified) {
		info, statErr := out.Stat()
		// Keep the exact final request, including the headers retained by the
		// redirect policy, for this transfer's range requests. Re-resolving the
		// original URL for every range can produce a different signed URL.
		var resolvedRequest *http.Request
		if resp.Request != nil && resp.Request.URL != nil {
			resolvedRequest = resp.Request.Clone(context.Background())
		}
		resp.Body.Close()
		_ = out.Close()
		if resp.Request == nil || resp.Request.URL == nil {
			return fmt.Errorf(i18n.T("无法确认下载文件的最终地址"))
		}
		resource := resourceFingerprint(resp.Request.URL)
		stable := githubReleaseResource(address, resp.Request.URL)
		t.checkpointMu.Lock()
		githubCandidate := resource != t.resumeResource && stable != "" && stable == t.resumeStableResource && strings.HasPrefix(validator, `"`) && validator == t.resumeValidator && total == t.resumeTotal && len(t.completed) > 0
		t.checkpointMu.Unlock()
		githubVerified := false
		if githubCandidate && expected == "" && !allowUnverified {
			var probeErr error
			githubVerified, probeErr = m.probeGitHubResume(t, resolvedRequest, total, validator)
			if probeErr != nil {
				return probeErr
			}
		}
		t.checkpointMu.Lock()
		t.metaMu.Lock()
		failedChecksum := t.failedSHA256
		t.failedSHA256 = ""
		t.metaMu.Unlock()
		invalidated := failedChecksum != "" && (expected == failedChecksum || expected == "")
		sameResource := validator != "" && validator == t.resumeValidator && resource == t.resumeResource
		githubRefresh := githubCandidate && githubVerified && stable == t.resumeStableResource && validator == t.resumeValidator && total == t.resumeTotal
		unverified := !sameResource && !githubRefresh && expected == ""
		if invalidated || (t.resumeUnverified && expected == "" && !allowUnverified) || (unverified && !allowUnverified) || total != t.resumeTotal || statErr != nil || info.Size() != total {
			t.completed = nil
			t.done.Store(0)
			t.resumeUnverified = false
		} else {
			if unverified && len(t.completed) > 0 {
				t.resumeUnverified = true
			}
			var done int64
			for _, chunk := range t.completed {
				done += chunk.end - chunk.start + 1
			}
			t.done.Store(done)
		}
		t.resumeTotal, t.resumeValidator, t.resumeResource = total, validator, resource
		t.resumeStableResource = stable
		if t.resumeUnverified && expected == "" {
			t.resumeSafe.Store(false)
		}
		err := m.saveTaskLocked(t)
		t.checkpointMu.Unlock()
		if err != nil {
			return fmt.Errorf(i18n.T("无法保存下载记录：%w"), err)
		}
		for refreshes := 0; ; refreshes++ {
			err = m.downloadSplit(t, part, total, validator, func(out *os.File, start, end int64, validator string, segment *segmentProgress, splitCtx context.Context) (int64, error) {
				return m.downloadRangeResolved(t, out, start, end, total, validator, segment, splitCtx, resolvedRequest)
			})
			if !errors.Is(err, errRangeExpired) || refreshes >= 2 {
				if errors.Is(err, errRangeExpired) {
					err = errRangeIgnored
				}
				break
			}
			fresh, freshCancel, requestErr := t.newRequest(address)
			if requestErr != nil {
				return requestErr
			}
			fresh.Header.Del("Range")
			fresh.Header.Del("If-Range")
			freshResp, requestErr := m.doWithRetry(t, fresh)
			if requestErr != nil {
				freshCancel()
				return requestErr
			}
			freshValidator := strongValidator(freshResp.Header)
			var freshStable, freshResource string
			if freshResp.Request != nil && freshResp.Request.URL != nil {
				freshResource = resourceFingerprint(freshResp.Request.URL)
				freshStable = githubReleaseResource(address, freshResp.Request.URL)
			}
			same := freshResource != "" && validator != "" && freshValidator == validator && freshResource == resource
			githubSame := stable != "" && freshStable == stable && strings.HasPrefix(validator, `"`) && freshValidator == validator
			encoding := freshResp.Header.Get("Content-Encoding")
			if freshResp.StatusCode != http.StatusOK || freshResp.ContentLength != total || (encoding != "" && !strings.EqualFold(encoding, "identity")) || (!same && !githubSame && expected == "" && !allowUnverified) || freshResource == "" {
				freshResp.Body.Close()
				freshCancel()
				err = errRangeIgnored
				break
			}
			resolvedRequest = freshResp.Request.Clone(context.Background())
			freshResp.Body.Close()
			freshCancel()
			validator, resource, stable = freshValidator, freshResource, freshStable
			t.checkpointMu.Lock()
			if !same && !githubSame && expected == "" && len(t.completed) > 0 {
				t.resumeUnverified = true
			}
			if t.resumeUnverified && expected == "" {
				t.resumeSafe.Store(false)
			}
			t.resumeValidator, t.resumeResource, t.resumeStableResource = validator, resource, stable
			requestErr = m.saveTaskLocked(t)
			t.checkpointMu.Unlock()
			if requestErr != nil {
				return fmt.Errorf(i18n.T("无法保存下载记录：%w"), requestErr)
			}
		}
		if !errors.Is(err, errRangeIgnored) {
			return err
		}
		select {
		case <-t.cancel:
			return contextCanceled
		default:
		}
		// The server ignored Range (or changed the representation after the
		// probe). Fetch a fresh full response, discard the old checkpoints,
		// and only then replace the partial data with a single stream.
		t.rangeSupport.Store(1)
		t.resumeSafe.Store(false)
		fullReq, fullCancel, err := t.newRequest(address)
		if err != nil {
			return err
		}
		defer fullCancel()
		fullReq.Header.Del("Range")
		fullReq.Header.Del("If-Range")
		fullResp, err := m.doWithRetry(t, fullReq)
		if err != nil {
			return err
		}
		defer fullResp.Body.Close()
		if fullResp.StatusCode != http.StatusOK {
			return fmt.Errorf("HTTP %d", fullResp.StatusCode)
		}
		t.total.Store(fullResp.ContentLength)
		return m.downloadSingle(t, part, fullResp, fullCancel)
	}
	if err := out.Close(); err != nil {
		return err
	}
	return m.downloadSingle(t, part, resp, cancelReq)
}

func (m *Manager) downloadSingle(t *Task, part string, resp *http.Response, cancel context.CancelFunc) error {
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return fmt.Errorf(i18n.T("服务器返回了不支持的内容编码：%s"), encoding)
	}
	out, err := os.OpenFile(extendedPath(part), os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	if err := m.prepareOutputLimit(t, out, resp.ContentLength); err != nil {
		return err
	}
	t.checkpointMu.Lock()
	t.completed = nil
	t.resumeTotal, t.resumeValidator, t.resumeResource = 0, "", ""
	t.resumeStableResource, t.resumeUnverified = "", false
	err = m.saveTaskLocked(t)
	t.checkpointMu.Unlock()
	if err != nil {
		return fmt.Errorf(i18n.T("无法保存下载记录：%w"), err)
	}
	t.resetSegments(nil)
	t.done.Store(0)
	if err := out.Truncate(0); err != nil {
		return err
	}
	end := int64(-1)
	if resp.ContentLength >= 0 {
		end = resp.ContentLength - 1
	}
	segment := t.startSegment(chunkSpan{start: 0, end: end})
	finished := false
	defer func() {
		if !finished {
			t.removeSegment(segment)
		}
	}()

	body := idleReader{body: resp.Body, timeout: m.bodyIdleTimeout, cancel: cancel}
	written, err := m.copyWithProgress(t, &segmentWriter{writer: out, segment: segment}, body)
	if err != nil {
		return err
	}
	if resp.ContentLength >= 0 && written != resp.ContentLength {
		if written < resp.ContentLength {
			return &responseReadError{err: fmt.Errorf(i18n.T("下载数据不完整：%d，预期 %d: %w"), written, resp.ContentLength, io.ErrUnexpectedEOF)}
		}
		return fmt.Errorf(i18n.T("下载数据超过声明长度：%d，预期 %d"), written, resp.ContentLength)
	}
	// Windows refuses to rename an open file.
	if err := out.Close(); err != nil {
		return err
	}
	t.finishStream(segment, written)
	finished = true
	// A chunked or close-delimited response has no length until EOF.
	t.total.Store(written)
	select {
	case <-t.cancel:
		return contextCanceled
	default:
	}
	return m.finishFile(t, part)
}

// chunkSpan is one remaining byte range, inclusive.
type chunkSpan struct {
	start, end int64
	digest     string
}

// Keep very large downloads from assigning hundreds of megabytes to one
// connection: shorter claims leave work available to faster workers for longer.
const maxAssignedChunkSize int64 = 64 << 20

func assignedChunkLimit(total, conns, minSize int64) int64 {
	return max(min(maxAssignedChunkSize, max(int64(4<<20), total/conns/4)), minSize)
}

// chunkQ assigns the largest unclaimed range first.
type chunkQ struct {
	mu       sync.Mutex
	chunks   []chunkSpan
	stopped  bool
	active   map[*chunkWork]struct{}
	wake     chan struct{}
	yielding bool // one outstanding request to yield at a time
}

type chunkWork struct {
	span    chunkSpan
	ctx     context.Context
	cancel  context.CancelFunc
	segment *segmentProgress // guarded by q.mu
	started time.Time
	stolen  bool // guarded by q.mu
}

func (q *chunkQ) signalLocked() {
	if q.wake != nil {
		close(q.wake)
	}
	q.wake = make(chan struct{})
}

// stop prevents any additional claims while in-flight chunks finish.
func (q *chunkQ) stop() {
	q.mu.Lock()
	q.stopped = true
	q.signalLocked()
	q.mu.Unlock()
}

// take hands out part of the largest unclaimed range, bounded by maxSize.
func (q *chunkQ) take(minSize, maxSize int64) (chunkSpan, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.takeLocked(minSize, maxSize)
}

func (q *chunkQ) takeLocked(minSize, maxSize int64) (chunkSpan, bool) {
	if q.stopped || len(q.chunks) == 0 {
		return chunkSpan{}, false
	}
	best := 0
	for i := range q.chunks {
		if q.chunks[i].end-q.chunks[i].start > q.chunks[best].end-q.chunks[best].start {
			best = i
		}
	}
	c := q.chunks[best]
	size := c.end - c.start + 1
	if size > maxSize && size-maxSize >= minSize {
		q.chunks[best] = chunkSpan{start: c.start + maxSize, end: c.end}
		return chunkSpan{start: c.start, end: c.start + maxSize - 1}, true
	}
	if size > 2*minSize {
		mid := c.start + size/2 - 1
		q.chunks[best] = chunkSpan{start: mid + 1, end: c.end}
		return chunkSpan{start: c.start, end: mid}, true
	}
	q.chunks = append(q.chunks[:best], q.chunks[best+1:]...)
	return c, true
}

// claim waits while a peer is working. If no unclaimed data remains, it asks
// the peer with the longest estimated remaining time to yield its written prefix.
func (q *chunkQ) claim(ctx context.Context, minSize, maxSize int64) (*chunkWork, bool) {
	for {
		q.mu.Lock()
		if q.stopped || ctx.Err() != nil {
			q.mu.Unlock()
			return nil, false
		}
		if span, ok := q.takeLocked(minSize, maxSize); ok {
			workCtx, cancel := context.WithCancel(ctx)
			work := &chunkWork{span: span, ctx: workCtx, cancel: cancel, started: time.Now()}
			if q.active == nil {
				q.active = make(map[*chunkWork]struct{})
			}
			q.active[work] = struct{}{}
			q.mu.Unlock()
			return work, true
		}
		if len(q.active) == 0 {
			q.mu.Unlock()
			return nil, false
		}
		var candidate *chunkWork
		var remaining int64
		var longest float64
		for work := range q.active {
			if q.yielding || work.stolen || work.segment == nil || work.segment.integrityPending.Load() || time.Since(work.started) < 2*time.Second {
				continue
			}
			done := work.segment.done.Load()
			left := work.span.end - work.span.start + 1 - done
			if done < minSize/2 || left < 2*minSize {
				continue
			}
			// A sampled stalled transfer has the greatest urgency. Before
			// sampling settles, prefer the largest remaining range.
			speed := work.segment.speed.Load()
			estimate := float64(0)
			if speed > 0 {
				estimate = float64(left) / float64(speed)
			} else if time.Since(work.started) >= 5*time.Second {
				estimate = 1e30
			}
			if candidate == nil || estimate > longest || (estimate == longest && left > remaining) {
				candidate, remaining, longest = work, left, estimate
			}
		}
		if candidate != nil {
			candidate.stolen = true
			q.yielding = true
			candidate.cancel()
		}
		if q.wake == nil {
			q.wake = make(chan struct{})
		}
		wake := q.wake
		q.mu.Unlock()
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
		case <-wake:
		case <-timer.C:
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
}

func (q *chunkQ) activate(work *chunkWork, segment *segmentProgress) {
	q.mu.Lock()
	work.segment = segment
	q.mu.Unlock()
}

func (q *chunkQ) wasStolen(work *chunkWork) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return work.stolen
}

func (q *chunkQ) finish(work *chunkWork, tail *chunkSpan) {
	q.mu.Lock()
	delete(q.active, work)
	if work.stolen {
		q.yielding = false
	}
	if tail != nil && !q.stopped {
		q.chunks = append(q.chunks, *tail)
	}
	q.signalLocked()
	q.mu.Unlock()
	work.cancel()
}

// splitFetcher 取回一个区间写入文件；HTTP 范围与 FTP 分段共用动态分段驱动。
type splitFetcher func(out *os.File, start, end int64, validator string, segment *segmentProgress, splitCtx context.Context) (int64, error)

// downloadSplit runs ranged workers over the dynamic chunk list, writing
// into one pre-sized file.
func (m *Manager) downloadSplit(t *Task, part string, total int64, validator string, fetch splitFetcher) error {
	out, err := os.OpenFile(extendedPath(part), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	if err := m.prepareOutputLimit(t, out, total); err != nil {
		return err
	}
	if err := out.Truncate(total); err != nil {
		return err
	}

	// Chunks smaller than this cost more in request overhead than they gain
	// in parallelism.
	automatic := t.conns == -1
	conns := t.conns
	if automatic {
		conns = 16
		if limit := m.siteConnectionLimit(t.sourceURL()); limit > 0 && limit < conns {
			conns = limit
		}
		if limit := m.MaxConnections(); limit > 0 && limit < conns {
			conns = limit
		}
	} else if conns <= 0 {
		m.mu.Lock()
		conns = m.conns
		m.mu.Unlock()
	}
	if conns < 1 {
		conns = 1
	}
	minSize := int64(1 << 20)
	if total < 4<<20 {
		minSize = max(int64(64<<10), total/8)
	}
	if avg := total / int64(conns); avg > 0 && avg < minSize {
		minSize = avg
	}
	if minSize < 64<<10 {
		minSize = 64 << 10
	}
	// Bound claims even for very large files, balancing request/checkpoint
	// overhead against work left available to faster connections.
	maxSize := assignedChunkLimit(total, int64(conns), minSize)
	t.checkpointMu.Lock()
	completed := append([]chunkSpan(nil), t.completed...)
	t.checkpointMu.Unlock()
	if m.storeDir != "" {
		valid := completed[:0]
		var done int64
		for _, chunk := range completed {
			digest, err := chunkDigest(out, chunk)
			if err == nil && digest == chunk.digest && digest != "" {
				valid = append(valid, chunk)
				done += chunk.end - chunk.start + 1
			}
		}
		completed = valid
		t.checkpointMu.Lock()
		t.completed = append([]chunkSpan(nil), completed...)
		t.done.Store(done)
		err := m.saveTaskLocked(t)
		t.checkpointMu.Unlock()
		if err != nil {
			return fmt.Errorf(i18n.T("无法保存下载记录：%w"), err)
		}
	}
	t.resetSegments(completed)
	sort.Slice(completed, func(i, j int) bool { return completed[i].start < completed[j].start })
	q := &chunkQ{}
	var next int64
	for _, chunk := range completed {
		if next < chunk.start {
			q.chunks = append(q.chunks, chunkSpan{start: next, end: chunk.start - 1})
		}
		next = chunk.end + 1
	}
	if next < total {
		q.chunks = append(q.chunks, chunkSpan{start: next, end: total - 1})
	}
	// Spread initial claims across the file instead of repeatedly taking
	// short chunks from the beginning of one large remaining range. Split
	// only uncompleted gaps, so resumed checkpoints remain untouched.
	distribution := min(conns, int(total/minSize)+1)
	if automatic {
		distribution = min(distribution, 4)
	}
	if distribution > 1 {
		var distributed []chunkSpan
		for _, gap := range q.chunks {
			start := gap.start
			for i := int64(1); i < int64(distribution); i++ {
				boundary := (total/int64(distribution))*i + (total%int64(distribution))*i/int64(distribution)
				if boundary <= start || boundary > gap.end {
					continue
				}
				distributed = append(distributed, chunkSpan{start: start, end: boundary - 1})
				start = boundary
			}
			distributed = append(distributed, chunkSpan{start: start, end: gap.end})
		}
		q.chunks = distributed
	}

	workers := conns
	if w := int(total/minSize) + 1; w < workers {
		workers = w
	}
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error
	splitCtx, stopSplit := context.WithCancel(context.Background())
	defer stopSplit()
	go func() {
		select {
		case <-t.cancel:
			stopSplit()
		case <-splitCtx.Done():
		}
	}()
	fail := func(err error) {
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
			q.stop()
			stopSplit()
		}
		errMu.Unlock()
	}
	var target atomic.Int32
	target.Store(int32(workers))
	if automatic {
		// Measure each candidate over two complete intervals. Stop increasing
		// when the extra connection fails to produce a meaningful gain.
		initial := int32(min(workers, 4))
		target.Store(initial)
		go func() {
			ticker := time.NewTicker(3 * time.Second)
			defer ticker.Stop()
			lastBytes := t.transferred.Load()
			var bestBytes int64
			bestTarget := initial
			var sampleBytes int64
			samples := 0
			settling := false
			finished := false
			for {
				select {
				case <-splitCtx.Done():
					return
				case <-ticker.C:
					now := t.transferred.Load()
					delta := max(int64(0), now-lastBytes)
					lastBytes = now
					current := target.Load()
					if u, err := url.Parse(t.sourceURL()); err == nil && m.isOriginCooling(retryOrigin(u)) {
						if current > 1 {
							target.Store(current - 1)
						}
						bestTarget, bestBytes = target.Load(), 0
						samples, sampleBytes, settling, finished = 0, 0, true, false
						continue
					}
					if settling {
						settling = false
						continue
					}
					if finished {
						continue
					}
					sampleBytes += delta
					samples++
					if samples < 2 {
						continue
					}
					if sampleBytes == 0 {
						samples, sampleBytes = 0, 0
						continue
					}
					if bestBytes > 0 && sampleBytes <= bestBytes+bestBytes/20 {
						target.Store(bestTarget)
						finished = true
						continue
					}
					bestTarget, bestBytes = current, sampleBytes
					samples, sampleBytes = 0, 0
					if current < int32(workers) && total-t.done.Load() > int64(current+1)*(1<<20) {
						target.Store(current + 1)
						settling = true
					} else {
						finished = true
					}
				}
			}
		}()
	}

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			for {
				for automatic && int32(index) >= target.Load() {
					q.mu.Lock()
					done := q.stopped || (len(q.chunks) == 0 && len(q.active) == 0)
					q.mu.Unlock()
					if done {
						return
					}
					select {
					case <-splitCtx.Done():
						return
					case <-time.After(250 * time.Millisecond):
					}
				}
				work, ok := q.claim(splitCtx, minSize, maxSize)
				if !ok {
					return
				}
				c := work.span
				segment := t.startSegment(c)
				q.activate(work, segment)
				var written int64
				var err error
				var tail *chunkSpan
				for attempt := 0; attempt < maxRequestAttempts; attempt++ {
					written, err = fetch(out, c.start, c.end, validator, segment, work.ctx)
					if err == nil {
						break
					}
					var interruptedRead *responseReadError
					if splitCtx.Err() == nil && work.ctx.Err() != nil && q.wasStolen(work) &&
						(errors.Is(err, contextCanceled) || errors.Is(err, context.Canceled) || errors.As(err, &interruptedRead)) {
						remaining := chunkSpan{start: c.start + written, end: c.end}
						if remaining.start <= remaining.end {
							tail = &remaining
						}
						if written > 0 {
							c.end = c.start + written - 1
							t.segmentMu.Lock()
							segment.end = c.end
							t.segmentMu.Unlock()
						}
						err = nil
						break
					}
					t.done.Add(-written) // a retry must not double-count
					segment.done.Store(0)
					var readErr *responseReadError
					if splitCtx.Err() != nil || !errors.As(err, &readErr) || !transientError(readErr) || attempt == maxRequestAttempts-1 {
						break
					}
					if waitErr := waitRetry(splitCtx.Done(), time.Duration(250*(1<<attempt))*time.Millisecond); waitErr != nil {
						t.removeSegment(segment)
						fail(waitErr)
						q.finish(work, nil)
						return
					}
				}
				if err != nil {
					t.removeSegment(segment)
					fail(err)
					q.finish(work, nil)
					return
				}
				if written == 0 && tail != nil {
					t.removeSegment(segment)
					q.finish(work, tail)
					continue
				}
				t.checkpointMu.Lock()
				recorded := false
				if m.storeDir != "" {
					err = out.Sync()
					if err == nil {
						c.digest, err = chunkDigest(out, c)
					}
				}
				if err == nil {
					t.completed = append(t.completed, c)
					recorded = true
					err = m.saveTaskLocked(t)
				}
				t.checkpointMu.Unlock()
				if err != nil {
					if recorded {
						segment.active.Store(false)
					} else {
						t.removeSegment(segment)
						t.done.Add(-written)
					}
					fail(fmt.Errorf(i18n.T("无法保存分段进度：%w"), err))
					q.finish(work, nil)
					return
				}
				segment.active.Store(false)
				q.finish(work, tail)
			}
		}(i)
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	if err := out.Close(); err != nil {
		return err
	}
	select {
	case <-t.cancel:
		return contextCanceled
	default:
	}
	return m.finishFile(t, part)
}

func resourceFingerprint(u *url.URL) string {
	if u == nil {
		return ""
	}
	sum := sha256.Sum256([]byte(u.String()))
	return hex.EncodeToString(sum[:])
}

// githubReleaseResource excludes only the expiring query of GitHub's known
// release asset endpoint. The hash never persists the signed URL itself.
func githubReleaseResource(source string, final *url.URL) string {
	original, err := url.Parse(source)
	if err != nil || original.User != nil || original.Scheme != "https" || !strings.EqualFold(original.Host, "github.com") || !strings.Contains(original.EscapedPath(), "/releases/download/") || final == nil || final.User != nil || final.Scheme != "https" || !strings.EqualFold(final.Host, "release-assets.githubusercontent.com") || !strings.HasPrefix(final.EscapedPath(), "/github-production-release-asset/") {
		return ""
	}
	canonical := *final
	canonical.RawQuery, canonical.ForceQuery, canonical.Fragment = "", false, ""
	return resourceFingerprint(&canonical)
}

// probeGitHubResume checks that a refreshed signed URL still serves the same
// strong-ETag representation before reusing previously persisted chunks.
func (m *Manager) probeGitHubResume(t *Task, resolved *http.Request, total int64, validator string) (bool, error) {
	if resolved == nil || resolved.URL == nil {
		return false, nil
	}
	req, cancel, err := t.newRequest(resolved.URL.String())
	if err != nil {
		return false, err
	}
	defer cancel()
	req.Header = resolved.Header.Clone()
	req.Header.Set("Range", "bytes=0-0")
	req.Header.Set("If-Range", validator)
	resp, err := m.doWithRetry(t, req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || resp.Request == nil || resourceFingerprint(resp.Request.URL) != resourceFingerprint(resolved.URL) || resp.Header.Get("ETag") != validator || resp.Header.Get("Content-Range") != fmt.Sprintf("bytes 0-0/%d", total) || (resp.ContentLength >= 0 && resp.ContentLength != 1) {
		return false, nil
	}
	encoding := resp.Header.Get("Content-Encoding")
	if encoding != "" && !strings.EqualFold(encoding, "identity") {
		return false, nil
	}
	body, err := io.ReadAll(io.LimitReader(idleReader{body: resp.Body, timeout: m.bodyIdleTimeout, cancel: cancel}, 2))
	if err != nil {
		return false, err
	}
	return len(body) == 1, nil
}

// downloadRangeResolved reuses only headers already allowed on the resolved
// request. In particular, it must not copy the original source's credentials
// onto a different redirect origin.
func (m *Manager) downloadRangeResolved(t *Task, out *os.File, start, end, total int64, validator string, segment *segmentProgress, splitCtx context.Context, resolved *http.Request) (int64, error) {
	address := t.sourceURL()
	if resolved != nil && resolved.URL != nil {
		address = resolved.URL.String()
	}
	req, cancelReq, err := t.newRequest(address)
	if err != nil {
		return 0, err
	}
	if resolved != nil && resolved.URL != nil {
		req.Header = resolved.Header.Clone()
	}
	defer cancelReq()
	if splitCtx != nil {
		stop := context.AfterFunc(splitCtx, cancelReq)
		defer stop()
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	if validator != "" {
		req.Header.Set("If-Range", validator)
	} else {
		req.Header.Del("If-Range")
	}
	resp, err := m.doWithRetry(t, req)
	if err != nil {
		var status *httpStatusError
		if errors.As(err, &status) {
			if status.code == http.StatusForbidden && resolved != nil && resolved.URL != nil && resolved.URL.String() != t.sourceURL() {
				return 0, errRangeExpired
			}
			switch status.code {
			case http.StatusBadRequest, http.StatusForbidden, http.StatusMethodNotAllowed,
				http.StatusPreconditionFailed, http.StatusRequestedRangeNotSatisfiable, http.StatusNotImplemented:
				// The origin may reject ranges or the representation may have
				// changed. A fresh full GET must succeed before old data is reset.
				return 0, errRangeIgnored
			}
		}
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return 0, errRangeIgnored
	}
	if resp.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf(i18n.T("服务器未正确返回分段数据（HTTP %d）"), resp.StatusCode)
	}
	if segment != nil {
		_, protected := resp.Body.(*digestIntegrityBody)
		segment.integrityPending.Store(protected)
	}
	t.checkpointMu.Lock()
	resource := t.resumeResource
	t.checkpointMu.Unlock()
	if resource != "" && (resp.Request == nil || resourceFingerprint(resp.Request.URL) != resource) {
		// A validator is scoped to a resource; identical ETags at different
		// redirect targets cannot prove that their bytes are interchangeable.
		// A publisher checksum permits this transfer because finishFile checks
		// the entire result before publishing it.
		t.metaMu.Lock()
		hasExpectedChecksum := t.expectedSHA256 != ""
		t.metaMu.Unlock()
		if !hasExpectedChecksum && !m.allowUnverifiedResume.Load() {
			return 0, errRangeIgnored
		}
		if !hasExpectedChecksum {
			t.checkpointMu.Lock()
			if !t.resumeUnverified {
				t.resumeUnverified = true
				if err := m.saveTaskLocked(t); err != nil {
					t.checkpointMu.Unlock()
					return 0, fmt.Errorf(i18n.T("无法保存下载记录：%w"), err)
				}
			}
			t.checkpointMu.Unlock()
			t.resumeSafe.Store(false)
		}
	}
	expected := fmt.Sprintf("bytes %d-%d/%d", start, end, total)
	if actual := strings.TrimSpace(resp.Header.Get("Content-Range")); actual != expected {
		return 0, fmt.Errorf(i18n.T("服务器返回的分段范围无效：%q"), actual)
	}
	if validator != "" {
		header := "Last-Modified"
		if strings.HasPrefix(validator, `"`) {
			header = "ETag"
		}
		if actual := resp.Header.Get(header); actual != validator && (actual != "" || (resolved != nil && githubReleaseResource(t.sourceURL(), resolved.URL) != "")) {
			return 0, fmt.Errorf(i18n.T("下载期间服务器文件已改变"))
		}
	}
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return 0, fmt.Errorf(i18n.T("服务器返回了压缩的分段数据：%s"), encoding)
	}
	size := end - start + 1
	if resp.ContentLength >= 0 && resp.ContentLength != size {
		return 0, fmt.Errorf(i18n.T("服务器返回的分段长度无效：%d，预期 %d"), resp.ContentLength, size)
	}
	// A valid 206 confirms range support even without Accept-Ranges.
	t.rangeSupport.Store(2)
	t.metaMu.Lock()
	hasExpectedChecksum := t.expectedSHA256 != ""
	t.metaMu.Unlock()
	t.checkpointMu.Lock()
	unverified := t.resumeUnverified
	t.checkpointMu.Unlock()
	if (validator != "" || hasExpectedChecksum) && (!unverified || hasExpectedChecksum) {
		t.resumeSafe.Store(true)
	}
	body := idleReader{body: resp.Body, timeout: m.bodyIdleTimeout, cancel: cancelReq}
	var canceled <-chan struct{} = t.cancel
	if splitCtx != nil {
		canceled = splitCtx.Done()
	}
	written, err := m.copyWithProgressCancel(t, &offsetWriter{f: out, off: start, segment: segment}, io.LimitReader(body, size), canceled)
	if err == nil && written != size {
		err = &responseReadError{err: fmt.Errorf(i18n.T("分段数据不完整：%d，预期 %d: %w"), written, size, io.ErrUnexpectedEOF)}
	}
	if err == nil {
		var extra [1]byte
		n, readErr := body.Read(extra[:])
		if n != 0 {
			err = fmt.Errorf(i18n.T("服务器返回的分段数据超过预期长度 %d"), size)
		} else if readErr != nil && readErr != io.EOF {
			err = &responseReadError{err: readErr}
		}
	}
	return written, err
}

// offsetWriter writes into a fixed file region; each range worker owns one.
type offsetWriter struct {
	f       *os.File
	off     int64
	segment *segmentProgress
}

func (o *offsetWriter) Write(p []byte) (int, error) {
	n, err := o.f.WriteAt(p, o.off)
	o.off += int64(n)
	if o.segment != nil {
		o.segment.addWritten(n)
	}
	return n, err
}

type segmentWriter struct {
	writer  io.Writer
	segment *segmentProgress
}

func (w *segmentWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	w.segment.addWritten(n)
	return n, err
}

// copyWithProgress streams body into w, accumulating task progress.
func (m *Manager) copyWithProgress(t *Task, w io.Writer, body io.Reader) (int64, error) {
	return m.copyWithProgressCancel(t, w, body, t.cancel)
}

func (m *Manager) copyWithProgressCancel(t *Task, w io.Writer, body io.Reader, canceled <-chan struct{}) (int64, error) {
	buf := make([]byte, 128<<10)
	var written int64
	for {
		select {
		case <-canceled:
			return written, contextCanceled
		default:
		}
		if err := m.quota.wait(canceled); err != nil {
			return written, err
		}
		allowed, refund, err := m.acquirePaced(t, len(buf), canceled)
		if err != nil {
			return written, err
		}
		n, rerr := body.Read(buf[:allowed])
		refund(n)
		if n > 0 {
			// Network throughput includes bytes read even if the disk later
			// accepts only part of this buffer.
			t.transferred.Add(int64(n))
			m.quota.add(int64(n))
			if err := checkOutputGrowth(t, n); err != nil {
				return written, err
			}
			nw, werr := w.Write(buf[:n])
			if nw > 0 {
				written += int64(nw)
				t.done.Add(int64(nw))
			}
			if werr != nil {
				return written, werr
			}
			if nw != n {
				return written, io.ErrShortWrite
			}
		}
		if rerr == io.EOF {
			return written, nil
		}
		if rerr != nil {
			return written, &responseReadError{err: rerr}
		}
	}
}

// sampleSpeed combines all connections and counts retransmitted bytes without
// confusing them with completed file progress. It also reports zero on stalls.
func (m *Manager) sampleSpeed(t *Task, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	lastTime, lastBytes := time.Now(), t.transferred.Load()
	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			bytes := t.transferred.Load()
			if elapsed := now.Sub(lastTime); elapsed > 0 {
				t.speed.Store(int64(float64(bytes-lastBytes) / elapsed.Seconds()))
			}
			lastTime, lastBytes = now, bytes
			t.sampleSegments(now)
			m.notify()
		}
	}
}

var contextCanceled = errCanceled("canceled")

var errRangeIgnored = i18n.NewError("服务器忽略分段请求")
var errRangeExpired = errors.New("resolved download URL expired or was rejected")

type errCanceled string

func (e errCanceled) Error() string { return string(e) }

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// claimPath keeps two same-named downloads apart by creating the .part file
// exclusively: concurrent tasks cannot claim the same name. Overwrite mode
// keeps the plain name even when the destination already exists.
func claimPath(dir, name string, overwrite bool) (filePath, part string, err error) {
	base := name
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; i < 10000; i++ {
		p := filepath.Join(dir, base)
		f, e := os.OpenFile(extendedPath(p+".part"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o644)
		if e == nil {
			if e = f.Close(); e != nil {
				_ = os.Remove(extendedPath(p + ".part"))
				return "", "", e
			}
			if _, e = os.Lstat(extendedPath(p)); e == nil {
				if overwrite {
					return p, p + ".part", nil
				}
				if e = os.Remove(extendedPath(p + ".part")); e != nil {
					return "", "", e
				}
			} else if os.IsNotExist(e) {
				return p, p + ".part", nil
			} else {
				_ = os.Remove(extendedPath(p + ".part"))
				return "", "", e
			}
		} else if !os.IsExist(e) {
			return "", "", e
		}
		base = fmt.Sprintf("%s (%d)%s", stem, i, ext)
	}
	return "", "", fmt.Errorf(i18n.T("文件名 %q 的可用编号已用尽"), name)
}
