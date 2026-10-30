package download

import (
	"context"
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
	var network net.Error
	return errors.As(err, &network) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
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
	for attempt := 0; attempt < maxRequestAttempts; attempt++ {
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
		if delay > maxRetryWait || attempt == maxRequestAttempts-1 {
			return nil, err
		}
		if err := waitRetry(req.Context().Done(), delay+retryJitter()); err != nil {
			return nil, err
		}
	}
	panic("unreachable")
}
