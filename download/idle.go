package download

import (
	"context"
	"io"
	"time"

	"openDL/i18n"
)

// idleReader times only a blocked network read, not disk writes or the
// rate limiter. Canceling the request interrupts just this HTTP stream.
type idleReader struct {
	body    io.Reader
	timeout time.Duration
	cancel  context.CancelFunc
}

type idleTimeoutError struct{}

func (idleTimeoutError) Error() string   { return i18n.T("下载连接长时间未收到数据") }
func (idleTimeoutError) Timeout() bool   { return true }
func (idleTimeoutError) Temporary() bool { return true }

func (r idleReader) Read(p []byte) (int, error) {
	if r.timeout <= 0 {
		return r.body.Read(p)
	}
	fired := make(chan struct{})
	timer := time.AfterFunc(r.timeout, func() {
		r.cancel()
		close(fired)
	})
	n, err := r.body.Read(p)
	if !timer.Stop() {
		<-fired
		return n, idleTimeoutError{}
	}
	return n, err
}
