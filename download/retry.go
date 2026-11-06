package download

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"openDL/i18n"
)

const maxRequestAttempts = 4
const maxRetryWait = time.Minute
const maxRetryDuration = time.Duration(1<<63 - 1)
const maxRetryJitter = 250 * time.Millisecond

func retryJitter() time.Duration {
	return time.Duration(rand.Int63n(int64(maxRetryJitter) + 1))
}

type httpStatusError struct {
	code       int
	retryAfter time.Duration
}

func (e *httpStatusError) Error() string { return fmt.Sprintf("HTTP %d", e.code) }

type responseReadError struct{ err error }

type originCooldownError struct{}

func (*originCooldownError) Error() string {
	return i18n.T("服务器要求稍后重试，等待时间超过自动重试上限")
}

func (e *responseReadError) Error() string { return e.err.Error() }
func (e *responseReadError) Unwrap() error { return e.err }

func transientError(err error) bool {
	if nonRetryableProxyError(err) {
		return false
	}
	var cooldown *originCooldownError
	if errors.As(err, &cooldown) {
		return false
	}
	var status *httpStatusError
	if errors.As(err, &status) {
		switch status.code {
		case 408, 429, 500, 502, 503, 504:
			return true
		}
		return false
	}
	// 域名不存在与证书校验失败重试不会自行恢复：立即按永久失败结束，
	// 避免无意义的退避等待。这里必须先于 net.Error 判断，握手错误常被
	// 包进实现 net.Error 的网络错误里。
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return false
	}
	var unknownAuthority x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthority) {
		return false
	}
	var certificateInvalid x509.CertificateInvalidError
	if errors.As(err, &certificateInvalid) {
		return false
	}
	var hostnameMismatch x509.HostnameError
	if errors.As(err, &hostnameMismatch) {
		return false
	}
	var network net.Error
	return errors.As(err, &network) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}

// retryWithBackoff 统一瞬时读取错误的重试样板：fn 执行一次尝试，仅当错误
// 是暂时性的 *responseReadError 时按 attempts 次上限、指数基数 base 退避重
// 试；退避等待成功后才调用 rollback 回滚上一次尝试的写入（返回错误则放弃
// 重试并原样上报）。cancel 关闭时立即返回 contextCanceled。
func retryWithBackoff(cancel <-chan struct{}, attempts int, base time.Duration, rollback func() error, fn func() error) error {
	for attempt := 0; ; attempt++ {
		select {
		case <-cancel:
			return contextCanceled
		default:
		}
		err := fn()
		if err == nil {
			return nil
		}
		var readErr *responseReadError
		if !errors.As(err, &readErr) || !transientError(readErr) || attempt >= attempts-1 {
			return err
		}
		if waitErr := waitRetry(cancel, base*time.Duration(1<<attempt)); waitErr != nil {
			return waitErr
		}
		select {
		case <-cancel:
			return contextCanceled
		default:
		}
		if rollback != nil {
			if rollbackErr := rollback(); rollbackErr != nil {
				return rollbackErr
			}
		}
	}
}

// mergeCancel 合并两个取消信号：任一关闭时返回的通道随即关闭。stop 释放
// 合并 goroutine，必须在使用方结束时调用。
func mergeCancel(a, b <-chan struct{}) (<-chan struct{}, func()) {
	merged := make(chan struct{})
	done := make(chan struct{})
	go func() {
		select {
		case <-a:
		case <-b:
		case <-done:
			return
		}
		close(merged)
	}()
	return merged, func() { close(done) }
}

func retryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		if seconds > int64(maxRetryDuration/time.Second) {
			return maxRetryDuration
		}
		return time.Duration(seconds) * time.Second
	}
	if value != "" && strings.Trim(value, "0123456789") == "" {
		// An out-of-range delta must not become an immediate retry.
		return maxRetryDuration
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}

func waitRetry(cancel <-chan struct{}, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-cancel:
		return contextCanceled
	}
}

// The cooldown is shared by tasks requesting the same origin, so concurrent
// connections do not immediately retry while the server asks them to wait.
func (m *Manager) waitOrigin(ctx context.Context, origin string) error {
	waited := false
	for {
		m.retryMu.Lock()
		until := m.retryUntil[origin]
		if !until.After(time.Now()) {
			delete(m.retryUntil, origin)
		}
		m.retryMu.Unlock()
		delay := time.Until(until)
		if delay <= 0 {
			if !waited {
				return nil
			}
			// Spread requests after the shared deadline, then recheck in case
			// another response extended it while this request was waiting.
			waited = false
			if err := waitRetry(ctx.Done(), retryJitter()); err != nil {
				return err
			}
			continue
		}
		if delay > maxRetryWait {
			return &originCooldownError{}
		}
		if err := waitRetry(ctx.Done(), delay); err != nil {
			return err
		}
		waited = true
	}
}

func retryOrigin(u *url.URL) string {
	return strings.ToLower(u.Scheme) + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), originPort(u))
}

func (m *Manager) delayOrigin(origin string, delay time.Duration) {
	m.retryMu.Lock()
	if m.retryUntil == nil {
		m.retryUntil = make(map[string]time.Time)
	}
	until := time.Now().Add(delay)
	if until.After(m.retryUntil[origin]) {
		m.retryUntil[origin] = until
	}
	m.retryMu.Unlock()
}

// isOriginCooling rechecks a deadline after waiting for the global connection
// limiter, where another request may have observed a new Retry-After.
func (m *Manager) isOriginCooling(origin string) bool {
	m.retryMu.Lock()
	defer m.retryMu.Unlock()
	return m.retryUntil[origin].After(time.Now())
}

// doWithRetry only retries safe GET requests and closes failed responses.
// Retry-After values beyond our automatic wait limit remain an error rather
// than retrying sooner than the server requested.
func (m *Manager) doWithRetry(t *Task, req *http.Request) (*http.Response, error) {
	origin := retryOrigin(req.URL)
	client := *m.client
	client.Transport = windowsAuthTransport{base: client.Transport, manager: m, task: t}
	if _, configured := m.windowsAuthFor(req.URL); configured {
		req = stripIntegratedAuthorization(req)
	}
	redirect := client.CheckRedirect
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if redirect != nil {
			if err := redirect(next, via); err != nil {
				return err
			}
		} else if len(via) >= 10 {
			return fmt.Errorf(i18n.T("重定向次数过多"))
		}
		return m.waitOrigin(next.Context(), retryOrigin(next.URL))
	}
	attempts := maxRequestAttempts
	if req.Context().Value(rangeProbeKey{}) != nil {
		attempts = 1
	}
	for attempt := 0; attempt < attempts; attempt++ {
		if err := m.waitOrigin(req.Context(), origin); err != nil {
			return nil, err
		}
		resp, err := doDigestRequest(&client, req, t)
		if err == nil && (resp.StatusCode < 400 || resp.StatusCode > 599) {
			return resp, nil
		}
		responseOrigin := origin
		var responseBody io.ReadCloser
		if err == nil {
			if resp.Request != nil && resp.Request.URL != nil {
				responseOrigin = retryOrigin(resp.Request.URL)
			}
			err = &httpStatusError{code: resp.StatusCode, retryAfter: retryAfter(resp.Header.Get("Retry-After"), time.Now())}
			responseBody = resp.Body
			if timing, ok := req.Context().Value(rangeRequestTimingKey{}).(*rangeRequestTiming); ok &&
				timing.task != nil && timing.task.conns == -1 &&
				(resp.StatusCode == 403 || resp.StatusCode == 429 || resp.StatusCode == 503) && timing.task.hasHealthyRange(timing.segment) {
				// An extra request was rejected while valid old bodies still
				// progress. Hand this to the coordinator immediately instead of
				// spending the whole generic retry budget at the old concurrency.
				status := err.(*httpStatusError)
				if status.code == 429 || status.code == 503 {
					m.delayOrigin(responseOrigin, max(2*time.Second, status.retryAfter))
				}
				responseBody.Close()
				return nil, &connectionRefusalError{err: err}
			}
		}
		if !transientError(err) {
			if responseBody != nil {
				responseBody.Close()
			}
			return nil, err
		}
		delay := time.Duration(250*(1<<attempt)) * time.Millisecond
		var status *httpStatusError
		if errors.As(err, &status) && status.retryAfter > delay {
			delay = status.retryAfter
		}
		if status != nil && (status.code == 429 || status.code == 503) {
			m.delayOrigin(responseOrigin, delay)
		}
		// Publish the cooldown before releasing the response's origin slot.
		if responseBody != nil {
			responseBody.Close()
		}
		if delay > maxRetryWait || attempt == attempts-1 {
			return nil, err
		}
		if err := waitRetry(req.Context().Done(), delay+retryJitter()); err != nil {
			return nil, err
		}
	}
	panic("unreachable")
}
