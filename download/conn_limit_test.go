package download

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestConnectionLimiterWakesWhenMadeUnlimited(t *testing.T) {
	l := newConnLimiter()
	l.setLimit(1)
	release, err := l.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ready := make(chan func(), 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		unlock, err := l.acquire(ctx)
		if err == nil {
			ready <- unlock
		}
	}()
	deadline := time.Now().Add(time.Second)
	for {
		l.mu.Lock()
		waiting := len(l.waiters)
		l.mu.Unlock()
		if waiting == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("connection did not enter the waiting queue")
		}
		time.Sleep(time.Millisecond)
	}
	l.setLimit(0)
	select {
	case unlock := <-ready:
		unlock()
	case <-time.After(time.Second):
		t.Fatal("connection remained blocked after the limit was removed")
	}
}

func TestConnectionLimiterRejectsCanceledContext(t *testing.T) {
	l := newConnLimiter()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	unlock, err := l.acquire(ctx)
	if unlock != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled connection accepted: %v", err)
	}
}

func TestConnectionLimiterServesWaitersInOrderAndSkipsCanceled(t *testing.T) {
	l := newConnLimiter()
	l.setLimit(1)
	release, err := l.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	canceled, cancelSecond := context.WithCancel(ctx)
	defer cancelSecond()
	order := make(chan int, 3)
	for i := 0; i < 3; i++ {
		workerCtx := ctx
		if i == 1 {
			workerCtx = canceled
		}
		go func(index int) {
			unlock, err := l.acquire(workerCtx)
			if err == nil {
				order <- index
				unlock()
			}
		}(i)
		deadline := time.Now().Add(time.Second)
		for {
			l.mu.Lock()
			count := len(l.waiters)
			l.mu.Unlock()
			if count == i+1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("request did not enter the queue")
			}
			time.Sleep(time.Millisecond)
		}
	}
	cancelSecond()
	deadline := time.Now().Add(time.Second)
	for {
		l.mu.Lock()
		count := len(l.waiters)
		l.mu.Unlock()
		if count == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("canceled request remained queued")
		}
		time.Sleep(time.Millisecond)
	}
	release()
	for _, want := range []int{0, 2} {
		select {
		case got := <-order:
			if got != want {
				t.Fatalf("got waiter %d, want %d", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("queued connection did not start")
		}
	}
}
