package download

import (
	"context"
	"errors"
	"sync"
)

var errConnectionBusy = errors.New("no connection slot available")

// connLimiter caps concurrent responses across all tasks and origins. A zero
// limit is unlimited; changing it never interrupts an existing response.
type connLimiter struct {
	mu         sync.Mutex
	limit      int
	used       int
	waiters    []*connWaiter
	contention uint64
}

type connWaiter struct {
	ctx     context.Context
	ready   chan struct{}
	granted bool // protected by connLimiter.mu
}

func newConnLimiter() *connLimiter { return &connLimiter{} }

func (l *connLimiter) dispatchLocked() {
	for len(l.waiters) > 0 && (l.limit == 0 || l.used < l.limit) {
		waiter := l.waiters[0]
		l.waiters[0] = nil
		l.waiters = l.waiters[1:]
		if waiter.ctx.Err() != nil {
			continue
		}
		waiter.granted = true
		l.used++
		close(waiter.ready)
	}
}

func (l *connLimiter) setLimit(limit int) {
	if l == nil {
		return
	}
	if limit < 0 {
		limit = 0
	}
	l.mu.Lock()
	l.limit = limit
	l.dispatchLocked()
	l.mu.Unlock()
}

func (l *connLimiter) getLimit() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.limit
}

// contended reports queued requests, whose throughput cannot be attributed to
// the chosen per-task connection target.
func (l *connLimiter) contentionState() (bool, uint64) {
	if l == nil {
		return false, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.waiters) > 0, l.contention
}

// tryAcquire gives speculative requests a slot only when no queued request
// would be bypassed.
func (l *connLimiter) tryAcquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.waiters) > 0 || (l.limit > 0 && l.used >= l.limit) {
		return nil, errConnectionBusy
	}
	l.used++
	return l.releaseFunc(), nil
}

// acquire serves queued requests in arrival order. Cancellation removes a
// waiter or releases a concurrently granted slot before waking the next one.
func (l *connLimiter) acquire(ctx context.Context) (func(), error) {
	if l == nil {
		return func() {}, nil
	}
	l.mu.Lock()
	if err := ctx.Err(); err != nil {
		l.mu.Unlock()
		return nil, err
	}
	if len(l.waiters) == 0 && (l.limit == 0 || l.used < l.limit) {
		l.used++
		l.mu.Unlock()
		return l.releaseFunc(), nil
	}
	waiter := &connWaiter{ctx: ctx, ready: make(chan struct{})}
	l.contention++
	l.waiters = append(l.waiters, waiter)
	l.dispatchLocked()
	l.mu.Unlock()
	select {
	case <-waiter.ready:
		if err := ctx.Err(); err != nil {
			l.releaseFunc()()
			return nil, err
		}
		return l.releaseFunc(), nil
	case <-ctx.Done():
		l.mu.Lock()
		if waiter.granted {
			l.used--
		} else {
			for i, queued := range l.waiters {
				if queued == waiter {
					copy(l.waiters[i:], l.waiters[i+1:])
					l.waiters[len(l.waiters)-1] = nil
					l.waiters = l.waiters[:len(l.waiters)-1]
					break
				}
			}
		}
		l.dispatchLocked()
		l.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (l *connLimiter) releaseFunc() func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			l.used--
			l.dispatchLocked()
			l.mu.Unlock()
		})
	}
}
