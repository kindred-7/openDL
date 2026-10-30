package download

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type originRoundTripFunc func(*http.Request) (*http.Response, error)

func (f originRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOriginLimitCancellationAndIndependence(t *testing.T) {
	var calls atomic.Int64
	transport := &originTransport{limit: 1, base: originRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("body")), Header: make(http.Header)}, nil
	})}
	request := func(ctx context.Context, target string) *http.Request {
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	first, err := transport.RoundTrip(request(context.Background(), "https://example.test/first"))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Body.Close()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := transport.RoundTrip(request(ctx, "https://example.test/second"))
		result <- err
	}()
	other, err := transport.RoundTrip(request(context.Background(), "https://other.test/file"))
	if err != nil {
		t.Fatal(err)
	}
	_ = other.Body.Close()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting request returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiting request did not cancel")
	}
	if calls.Load() != 2 {
		t.Fatalf("blocked request reached transport: %d", calls.Load())
	}
	_ = first.Body.Close()
	last, err := transport.RoundTrip(request(context.Background(), "https://example.test/third"))
	if err != nil {
		t.Fatal(err)
	}
	_ = last.Body.Close()
}

func TestQueuedRequestChecksNewCooldown(t *testing.T) {
	var calls atomic.Int64
	m := NewManager(t.TempDir(), 1, 1)
	transport := &originTransport{limit: 1, before: m.waitOrigin, base: originRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("body")), Header: make(http.Header)}, nil
	})}
	firstReq, _ := http.NewRequest(http.MethodGet, "https://example.test/first", nil)
	first, err := transport.RoundTrip(firstReq)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Body.Close()
	secondReq, _ := http.NewRequest(http.MethodGet, "https://example.test/second", nil)
	result := make(chan error, 1)
	go func() { _, err := transport.RoundTrip(secondReq); result <- err }()
	origin := retryOrigin(firstReq.URL)
	deadline := time.Now().Add(time.Second)
	for {
		transport.mu.Lock()
		queued := transport.gates[origin] != nil && transport.gates[origin].users == 2
		transport.mu.Unlock()
		if queued {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second request did not enter the queue")
		}
		time.Sleep(time.Millisecond)
	}
	m.delayOrigin(origin, time.Hour)
	_ = first.Body.Close()
	select {
	case err := <-result:
		var cooldown *originCooldownError
		if !errors.As(err, &cooldown) {
			t.Fatalf("expected cooldown error, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued request did not return")
	}
	if calls.Load() != 1 {
		t.Fatalf("request bypassed cooldown: %d", calls.Load())
	}
}

func TestOriginSlotsServeQueuedRequestsInOrder(t *testing.T) {
	transport := &originTransport{limit: 1, base: originRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("body")), Header: make(http.Header)}, nil
	})}
	firstReq, _ := http.NewRequest(http.MethodGet, "https://example.test/first", nil)
	first, err := transport.RoundTrip(firstReq)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Body.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		index int
		resp  *http.Response
		err   error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.test/file", nil)
		go func(index int) {
			resp, err := transport.RoundTrip(req)
			results <- result{index: index, resp: resp, err: err}
		}(i)
		deadline := time.Now().Add(time.Second)
		for {
			transport.mu.Lock()
			gate := transport.gates[retryOrigin(req.URL)]
			transport.mu.Unlock()
			gate.slots.mu.Lock()
			queued := len(gate.slots.waiters)
			gate.slots.mu.Unlock()
			if queued == i+1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("origin request did not enter the queue")
			}
			time.Sleep(time.Millisecond)
		}
	}
	_ = first.Body.Close()
	for _, want := range []int{0, 1} {
		select {
		case got := <-results:
			if got.err != nil || got.index != want {
				t.Fatalf("got request %d, error %v; want %d", got.index, got.err, want)
			}
			_ = got.resp.Body.Close()
		case <-time.After(time.Second):
			t.Fatal("origin request did not start")
		}
	}
}

func TestGlobalSlotWaitRechecksOriginCooldown(t *testing.T) {
	m := NewManager(t.TempDir(), 1, 1)
	m.limiter.setLimit(1)
	releaseSlot, err := m.limiter.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer releaseSlot()
	entered := make(chan struct{}, 1)
	var calls atomic.Int64
	transport := &originTransport{limit: 1, limiter: m.limiter, cooling: m.isOriginCooling,
		before: func(ctx context.Context, origin string) error {
			err := m.waitOrigin(ctx, origin)
			if err == nil {
				select {
				case entered <- struct{}{}:
				default:
				}
			}
			return err
		},
		base: originRoundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("body")), Header: make(http.Header)}, nil
		})}
	req, _ := http.NewRequest(http.MethodGet, "https://example.test/file", nil)
	result := make(chan error, 1)
	go func() { _, err := transport.RoundTrip(req); result <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request did not reach global limiter")
	}
	m.delayOrigin(retryOrigin(req.URL), time.Hour)
	releaseSlot()
	select {
	case err := <-result:
		var cooldown *originCooldownError
		if !errors.As(err, &cooldown) {
			t.Fatalf("expected cooldown error, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("request did not recheck cooldown")
	}
	if calls.Load() != 0 {
		t.Fatal("request bypassed the origin cooldown")
	}
	// The blocked origin must not hold the global connection slot.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	unlocked, err := m.limiter.acquire(ctx)
	if err != nil {
		t.Fatalf("global slot was not released: %v", err)
	}
	unlocked()
}
