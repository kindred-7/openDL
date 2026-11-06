package download

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"
)

// originTransport limits concurrent HTTP responses per origin across tasks.
// A slot remains held until the response body is consumed or closed. The
// optional limiter additionally caps concurrent responses across every origin.
type originTransport struct {
	base      http.RoundTripper
	limit     int
	siteLimit func(string) int // optional; actual transfer origin, including redirect destinations
	before    func(context.Context, string) error
	cooling   func(string) bool
	limiter   *connLimiter
	mu        sync.Mutex
	gates     map[string]*originGate
}

type originGate struct {
	slots *connLimiter
	users int // active holders and waiting requests, protected by mu
}

// The optional Range probe runs while the full response is still open. It
// must never queue behind a connection slot that response is holding.
type rangeProbeKey struct{}

func (t *originTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.roundTrip(req, t.base)
}

// effectiveLimit preserves zero-as-unlimited for standalone transports. A
// matching site rule can only lower the transport's configured origin cap.
func (t *originTransport) effectiveLimit(origin string) int {
	limit := t.limit
	if t.siteLimit != nil {
		if site := t.siteLimit(origin); site > 0 && (limit <= 0 || site < limit) {
			limit = site
		}
	}
	return limit
}

// refreshLimits wakes already queued callers when a rule is raised or removed.
// A lower cap takes effect as active responses release their slots; healthy
// responses remain intact. Callers must release configuration locks first.
func (t *originTransport) refreshLimits() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for origin, gate := range t.gates {
		gate.slots.setLimit(t.effectiveLimit(origin))
	}
}

// roundTrip shares the existing gates with an isolated authentication route.
func (t *originTransport) roundTrip(req *http.Request, base http.RoundTripper) (*http.Response, error) {
	origin := retryOrigin(req.URL)
	_, probing := req.Context().Value(rangeProbeKey{}).(struct{})
	acquire := (*connLimiter).acquire
	if probing {
		acquire = (*connLimiter).tryAcquire
	}
	t.mu.Lock()
	if t.gates == nil {
		t.gates = make(map[string]*originGate)
	}
	gate := t.gates[origin]
	if gate == nil {
		gate = &originGate{slots: newConnLimiter()}
		t.gates[origin] = gate
	}
	// Refresh on every acquisition too, so newly arriving requests never use
	// an old gate limit while a settings update is publishing its refresh.
	gate.slots.setLimit(t.effectiveLimit(origin))
	gate.users++
	t.mu.Unlock()

	forget := func() {
		t.mu.Lock()
		gate.users--
		if gate.users == 0 {
			delete(t.gates, origin)
		}
		t.mu.Unlock()
	}
	unlockOrigin, err := acquire(gate.slots, req.Context())
	if err != nil {
		forget()
		return nil, err
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			unlockOrigin()
			forget()
		})
	}
	unlock := func() {}
	for {
		if t.before != nil {
			if err := t.before(req.Context(), origin); err != nil {
				release()
				return nil, err
			}
		}
		// Cooldown waits never occupy a global slot. Recheck after acquiring
		// one, because the deadline may have changed while we were queued.
		if t.limiter != nil {
			var err error
			unlock, err = acquire(t.limiter, req.Context())
			if err != nil {
				release()
				return nil, err
			}
		}
		if t.before == nil || t.cooling == nil || !t.cooling(origin) {
			break
		}
		unlock()
	}
	releaseAll := func() {
		release()
		unlock()
	}
	started := time.Now()
	resp, err := base.RoundTrip(req)
	observeRangeRequest(req.Context(), started)
	if err != nil || resp == nil || resp.Body == nil {
		releaseAll()
		return resp, err
	}
	resp.Body = &originBody{ReadCloser: resp.Body, release: releaseAll}
	return resp, nil
}

type originBody struct {
	io.ReadCloser
	release func()
}

func (b *originBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF {
		b.release()
	}
	return n, err
}

func (b *originBody) Close() error {
	err := b.ReadCloser.Close()
	b.release()
	return err
}
