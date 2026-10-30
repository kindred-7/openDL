package download

import (
	"context"
	"testing"
	"time"
)

func TestChunkQueueBoundsAndCoversRanges(t *testing.T) {
	const minSize int64 = 64 << 10
	const maxSize int64 = 4 << 20
	const total int64 = 21<<20 + 32<<10
	q := &chunkQ{chunks: []chunkSpan{{start: 0, end: total - 1}}}
	var next int64
	var count int
	for {
		span, ok := q.take(minSize, maxSize)
		if !ok {
			break
		}
		if span.start != next || span.end < span.start || span.end-span.start+1 > maxSize {
			t.Fatalf("invalid span %+v at offset %d", span, next)
		}
		next = span.end + 1
		count++
	}
	if next != total || count < 2 {
		t.Fatalf("covered %d of %d bytes in %d chunks", next, total, count)
	}
}

func TestChunkQueueStopPreventsFurtherClaims(t *testing.T) {
	q := &chunkQ{chunks: []chunkSpan{{start: 0, end: 20<<20 - 1}}}
	if _, ok := q.take(64<<10, 4<<20); !ok {
		t.Fatal("initial claim failed")
	}
	q.stop()
	if _, ok := q.take(64<<10, 4<<20); ok {
		t.Fatal("claimed a chunk after stop")
	}
	if len(q.chunks) == 0 {
		t.Fatal("stopping discarded unclaimed ranges")
	}
}

func TestAssignedChunkLimitBoundsLargeTransfers(t *testing.T) {
	const minimum int64 = 1 << 20
	if got := assignedChunkLimit(16<<30, 8, minimum); got != maxAssignedChunkSize {
		t.Fatalf("large transfer claim = %d, want %d", got, maxAssignedChunkSize)
	}
	if got := assignedChunkLimit(8<<20, 8, minimum); got != 4<<20 {
		t.Fatalf("small transfer claim = %d, want %d", got, 4<<20)
	}
}

func TestIdleWorkerReclaimsActiveRangeAfterPrefixIsCommitted(t *testing.T) {
	const minimum int64 = 64 << 10
	q := &chunkQ{chunks: []chunkSpan{{start: 0, end: 4<<20 - 1}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first, ok := q.claim(ctx, minimum, 4<<20)
	if !ok {
		t.Fatal("missing first claim")
	}
	// Remove the still-unclaimed half so the next worker has to wait for the
	// active range; the test supplies its tail only after the prefix commits.
	q.mu.Lock()
	q.chunks = nil
	first.started = time.Now().Add(-3 * time.Second)
	q.mu.Unlock()
	progress := &segmentProgress{}
	progress.done.Store(minimum)
	q.activate(first, progress)
	claimed := make(chan *chunkWork, 1)
	go func() {
		work, ok := q.claim(ctx, minimum, 4<<20)
		if ok {
			claimed <- work
		}
	}()
	select {
	case <-first.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("active range was not asked to yield")
	}
	tail := chunkSpan{start: first.span.start + minimum, end: first.span.end}
	q.finish(first, &tail)
	select {
	case second := <-claimed:
		if second.span.start != tail.start || second.span.end > tail.end {
			t.Fatalf("reclaimed range overlaps prefix: %+v", second.span)
		}
		q.finish(second, nil)
	case <-time.After(5 * time.Second):
		t.Fatal("reclaimed range was not assigned")
	}
}

func TestWaitingRangeWorkerStopsOnCancellation(t *testing.T) {
	q := &chunkQ{chunks: []chunkSpan{{start: 0, end: 1023}}}
	ctx, cancel := context.WithCancel(context.Background())
	first, ok := q.claim(ctx, 64, 2048)
	if !ok {
		t.Fatal("missing first claim")
	}
	q.mu.Lock()
	q.chunks = nil
	q.mu.Unlock()
	done := make(chan bool, 1)
	go func() { _, ok := q.claim(ctx, 64, 2048); done <- ok }()
	cancel()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("claim succeeded after cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiting worker did not stop")
	}
	q.finish(first, nil)
}

func TestOnlyOneActiveRangeIsAskedToYieldAtATime(t *testing.T) {
	const minimum int64 = 64 << 10
	q := &chunkQ{chunks: []chunkSpan{{start: 0, end: 8<<20 - 1}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first, ok := q.claim(ctx, minimum, 4<<20)
	if !ok {
		t.Fatal("missing first claim")
	}
	second, ok := q.claim(ctx, minimum, 4<<20)
	if !ok {
		t.Fatal("missing second claim")
	}
	q.mu.Lock()
	q.chunks = nil
	first.started, second.started = time.Now().Add(-3*time.Second), time.Now().Add(-3*time.Second)
	q.mu.Unlock()
	for _, work := range []*chunkWork{first, second} {
		progress := &segmentProgress{}
		progress.done.Store(minimum)
		q.activate(work, progress)
	}
	for i := 0; i < 2; i++ {
		go func() { q.claim(ctx, minimum, 4<<20) }()
	}
	select {
	case <-first.ctx.Done():
	case <-second.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("no active range was asked to yield")
	}
	// The next idle worker gets time to inspect the queue while the first
	// yielded range has not yet committed its prefix and returned its tail.
	deadline := time.NewTimer(700 * time.Millisecond)
	defer deadline.Stop()
	select {
	case <-first.ctx.Done():
		select {
		case <-second.ctx.Done():
			t.Fatal("two ranges were interrupted together")
		case <-deadline.C:
		}
	case <-second.ctx.Done():
		select {
		case <-first.ctx.Done():
			t.Fatal("two ranges were interrupted together")
		case <-deadline.C:
		}
	}
	cancel()
	q.finish(first, nil)
	q.finish(second, nil)
}
