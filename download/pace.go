package download

import (
	"sync"
	"time"
)

// paceLimiter is a shared token bucket. The burst is at most a quarter
// second of the configured rate and never exceeds 32 KiB.
type paceLimiter struct {
	mu      sync.Mutex
	rate    int64
	tokens  float64
	last    time.Time
	epoch   uint64
	changed chan struct{}
}

type readAllowance struct {
	bytes   int
	epoch   uint64
	limited bool
}

func (p *paceLimiter) burst() int64 { return min(int64(32<<10), max(int64(1), p.rate/4)) }

// replenishLocked is only called with a nonzero rate.
func (p *paceLimiter) replenishLocked(now time.Time) {
	if !p.last.IsZero() {
		p.tokens = min(float64(p.burst()), p.tokens+now.Sub(p.last).Seconds()*float64(p.rate))
	}
	p.last = now
}

func (p *paceLimiter) setRate(rate int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if p.rate > 0 {
		p.replenishLocked(now)
	}
	oldRate := p.rate
	p.rate = rate
	if rate > 0 {
		if oldRate == 0 {
			p.tokens = 0
		}
		p.tokens = min(p.tokens, float64(p.burst()))
	} else {
		p.tokens = 0
	}
	p.last = now
	p.epoch++
	if p.changed != nil {
		close(p.changed)
	}
	p.changed = make(chan struct{})
}

func (p *paceLimiter) getRate() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.rate
}

// acquire waits before reading, so concurrent streams cannot each burst a
// full read buffer before the cap is applied. Changes and cancellation wake it.
func (p *paceLimiter) acquire(maxBytes int, canceled <-chan struct{}) (readAllowance, error) {
	for {
		select {
		case <-canceled:
			return readAllowance{}, contextCanceled
		default:
		}
		p.mu.Lock()
		if p.rate <= 0 {
			p.mu.Unlock()
			return readAllowance{bytes: maxBytes}, nil
		}
		if p.changed == nil {
			p.changed = make(chan struct{})
		}
		now := time.Now()
		p.replenishLocked(now)
		want := min(int64(maxBytes), p.burst())
		if p.tokens >= float64(want) {
			p.tokens -= float64(want)
			allowance := readAllowance{bytes: int(want), epoch: p.epoch, limited: true}
			p.mu.Unlock()
			return allowance, nil
		}
		delay := time.Duration((float64(want) - p.tokens) / float64(p.rate) * float64(time.Second))
		if delay < time.Millisecond {
			delay = time.Millisecond
		}
		changed := p.changed
		p.mu.Unlock()
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-changed:
		case <-canceled:
			timer.Stop()
			return readAllowance{}, contextCanceled
		}
		timer.Stop()
	}
}

// Return unused tokens after short reads; never apply an old reservation to
// a newly configured limit.
func (p *paceLimiter) refund(allowance readAllowance, actual int) {
	if !allowance.limited || actual >= allowance.bytes {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.rate <= 0 || p.epoch != allowance.epoch {
		return
	}
	p.replenishLocked(time.Now())
	p.tokens = min(float64(p.burst()), p.tokens+float64(allowance.bytes-actual))
	if p.changed != nil {
		close(p.changed)
		p.changed = make(chan struct{})
	}
}
