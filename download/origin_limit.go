package download

import (
	"context"
	"io"
	"net/http"
	"sync"
)

// originTransport limits concurrent HTTP responses per origin across tasks.
// A slot remains held until the response body is consumed or closed. The
// optional limiter additionally caps concurrent responses across every origin.
type originTransport struct {
	base    http.RoundTripper
	limit   int
	before  func(context.Context, string) error
	cooling func(string) bool
	limiter *connLimiter
	mu      sync.Mutex
	gates   map[string]*originGate
}

type originGate struct {
	slots *connLimiter
	users int // active holders and waiting requests, protected by mu
}

func (t *originTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.roundTrip(req, t.base)
}

// roundTrip shares the existing gates with an isolated authentication route.
func (t *originTransport) roundTrip(req *http.Request, base http.RoundTripper) (*http.Response, error) {
	origin := retryOrigin(req.URL)
	t.mu.Lock()
	if t.gates == nil {
		t.gates = make(map[string]*originGate)
	}
	gate := t.gates[origin]
	if gate == nil {
		gate = &originGate{slots: newConnLimiter()}
		gate.slots.setLimit(t.limit)
		t.gates[origin] = gate
	}
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
	unlockOrigin, err := gate.slots.acquire(req.Context())
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
			unlock, err = t.limiter.acquire(req.Context())
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
	resp, err := base.RoundTrip(req)
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
