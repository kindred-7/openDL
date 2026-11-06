package download

import (
	"context"
	"io"
	"sync"
	"time"

	"openDL/i18n"
)

// idleReader times only a blocked network read, not disk writes or the
// rate limiter. Canceling the request interrupts just this HTTP stream.
// Each body reuses one timer across reads.
type idleReader struct {
	body          io.Reader
	timeout       time.Duration
	cancel        context.CancelFunc
	selectTimeout func() time.Duration
	watch         *idleWatch // 首次 Read 时创建
}

const defaultBodyIdleTimeout = 30 * time.Second

const healthyRangeCacheTTL = 500 * time.Millisecond

func (m *Manager) automaticBodyTimeout(t *Task, segment *segmentProgress) time.Duration {
	// Preserve explicit internal/test overrides and every fixed-concurrency flow.
	if t.conns != -1 || m.bodyIdleTimeout != defaultBodyIdleTimeout {
		return m.bodyIdleTimeout
	}
	if segment == nil || segment.integrityPending.Load() || !t.hasHealthyRangeCached(segment) {
		return 60 * time.Second
	}
	return 5 * time.Second
}

type idleTimeoutError struct{}

func (idleTimeoutError) Error() string   { return i18n.T("下载连接长时间未收到数据") }
func (idleTimeoutError) Timeout() bool   { return true }
func (idleTimeoutError) Temporary() bool { return true }

type idleWatch struct {
	mu       sync.Mutex
	timer    *time.Timer
	deadline time.Time
	fired    bool
	cancel   context.CancelFunc
}

func (w *idleWatch) arm(timeout time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.fired = false
	w.deadline = time.Now().Add(timeout)
	if w.timer == nil {
		w.timer = time.AfterFunc(timeout, w.check)
		return
	}
	w.timer.Reset(timeout)
}

func (w *idleWatch) check() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.deadline.IsZero() || w.fired {
		return
	}
	// A callback from a stopped timer may still be running after the next
	// read arms it. Only the current monotonic deadline can expire a read.
	if time.Until(w.deadline) > 0 {
		return
	}
	w.fired = true
	w.cancel()
}

// stop 结束一次观察并报告本次 Read 是否已被判超时。
func (w *idleWatch) stop() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deadline = time.Time{}
	w.timer.Stop()
	return w.fired
}

func (r *idleReader) Read(p []byte) (int, error) {
	timeout := r.timeout
	if r.selectTimeout != nil {
		timeout = r.selectTimeout()
	}
	if timeout <= 0 {
		return r.body.Read(p)
	}
	if r.watch == nil {
		r.watch = &idleWatch{cancel: r.cancel}
	}
	r.watch.arm(timeout)
	n, err := r.body.Read(p)
	if r.watch.stop() {
		return n, idleTimeoutError{}
	}
	return n, err
}
