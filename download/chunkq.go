package download

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// chunkQ assigns the largest unclaimed range first.
type chunkQ struct {
	mu             sync.Mutex
	task           *Task
	chunks         []chunkSpan
	sequential     bool              // a single worker fills the earliest remaining range first
	automatic      bool              // automatic scheduling favors long, useful responses
	checkpoints    *rangeCheckpoints // immutable once the task's workers start
	steady         atomic.Bool       // stable target permits longer uninterrupted claims
	target         *atomic.Int32     // desired workers, assigned before workers start
	stopped        bool
	active         map[*chunkWork]struct{}
	wake           chan struct{}
	workerRates    map[int]int64 // last completed useful rate for each worker, guarded by mu
	workerFinished map[int]time.Time
	refusalEpoch   atomic.Uint64
	refusalUntil   time.Time
	refusedRanges  map[int64]int
	refusalCap     int32
	refusalBatches int
}

// Additional-request rejection is task-local evidence. Keep healthy responses
// intact, constrain new dispatch once per batch, and bound requeues per range.
func (q *chunkQ) deferRefused(work *chunkWork, target *atomic.Int32) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.stopped || target == nil {
		return false
	}
	if q.refusedRanges == nil {
		q.refusedRanges = make(map[int64]int)
	}
	q.refusedRanges[work.span.start]++
	if q.refusedRanges[work.span.start] > 3 {
		return false
	}
	now := time.Now()
	if !now.Before(q.refusalUntil) {
		next := max(1, target.Load()/2)
		if q.refusalBatches > 0 {
			next = 1
		}
		q.refusalBatches++
		target.Store(next)
		q.refusalCap = next
		q.task.effectiveConnections.Store(next)
		q.refusalUntil = now.Add(autoConnectionPressureInterval)
		q.refusalEpoch.Add(1)
	}
	q.signalLocked()
	return true
}

type chunkWork struct {
	span    chunkSpan
	ctx     context.Context
	cancel  context.CancelFunc
	segment *segmentProgress // guarded by q.mu
	started time.Time
	stolen  bool // guarded by q.mu
	worker  int
}

// retire cancels excess workers so that a lower automatic target takes effect
// without waiting for their entire current ranges to finish. Their completed
// prefixes are committed as ordinary checkpoints before the tail is requeued.
func (q *chunkQ) retire(target int32) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for work := range q.active {
		if int32(work.worker) >= target && !work.stolen && work.segment != nil && work.segment.bodyReady.Load() && work.segment.done.Load() > 0 && !work.segment.integrityPending.Load() {
			work.stolen = true
			work.cancel()
		}
	}
	q.signalLocked()
}

// excessActive reports work that still belongs to a retired worker. A new
// automatic sample must not include bytes from these old connections.
func (q *chunkQ) excessActive(target int32) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for work := range q.active {
		if int32(work.worker) >= target {
			return true
		}
	}
	return false
}

func (q *chunkQ) signalLocked() {
	if q.wake != nil {
		close(q.wake)
	}
	q.wake = make(chan struct{})
}

// stop prevents any additional claims while in-flight chunks finish.
func (q *chunkQ) stop() {
	q.mu.Lock()
	q.stopped = true
	q.signalLocked()
	q.mu.Unlock()
}

// take hands out part of the largest unclaimed range, bounded by maxSize.
func (q *chunkQ) take(minSize, maxSize int64) (chunkSpan, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.takeLocked(minSize, maxSize, -1)
}

func (q *chunkQ) takeLocked(minSize, maxSize, nextOffset int64) (chunkSpan, bool) {
	if q.stopped || len(q.chunks) == 0 {
		return chunkSpan{}, false
	}
	best := 0
	preferred := -1
	for i := range q.chunks {
		if q.chunks[i].start == nextOffset {
			preferred = i
		}
		if (q.sequential && q.chunks[i].start < q.chunks[best].start) ||
			(!q.sequential && q.chunks[i].end-q.chunks[i].start > q.chunks[best].end-q.chunks[best].start) {
			best = i
		}
	}
	if preferred >= 0 && !q.sequential {
		best = preferred
	}
	c := q.chunks[best]
	size := c.end - c.start + 1
	if size > maxSize && size-maxSize >= minSize {
		q.chunks[best] = chunkSpan{start: c.start + maxSize, end: c.end}
		return chunkSpan{start: c.start, end: c.start + maxSize - 1}, true
	}
	claimMinimum := minSize
	if q.target != nil {
		var throughput int64
		for _, rate := range q.workerRates {
			throughput += max(0, rate)
		}
		claimMinimum = max(claimMinimum, automaticSplitMinimum(throughput))
	}
	if q.target != nil && len(q.active)+len(q.chunks) < int(q.target.Load()) && size >= 2*claimMinimum {
		// A resumed file can have fewer gaps than available workers. Split
		// only while extra parallel work is needed, leaving both halves useful.
		mid := c.start + size/2 - 1
		q.chunks[best] = chunkSpan{start: mid + 1, end: c.end}
		return chunkSpan{start: c.start, end: mid}, true
	}
	// Otherwise keep the range whole to avoid short requests and checkpoints.
	q.chunks = append(q.chunks[:best], q.chunks[best+1:]...)
	return c, true
}

// splitLocked publishes at most one non-overlapping tail. q.mu is held;
// segment.splitMu is acquired before changing the writer's exclusive boundary.
func (q *chunkQ) splitLocked(minSize int64, worker int) bool {
	if q.automatic && q.checkpoints != nil && q.checkpoints.backlogged() {
		return false
	}
	helperRate := q.workerRates[worker]
	var throughput int64
	for work := range q.active {
		if work.segment != nil && work.segment.bodyReady.Load() {
			rate := max(0, work.segment.speed.Load())
			helperRate = max(helperRate, rate)
			throughput += rate
		}
	}
	setup := time.Duration(q.task.rangeSetupNanos.Load())
	if setup <= 0 {
		setup = 250 * time.Millisecond
	}
	commit := time.Duration(q.task.rangeCommitNanos.Load())
	if q.checkpoints != nil {
		// Journal syncs are batched independently of response boundaries.
		commit = 0
	}
	minimum := minSize
	if q.target != nil {
		minimum = max(minSize, automaticSplitMinimum(throughput))
	}
	for pass := 0; pass < 2; pass++ {
		var candidate *chunkWork
		var largest int64
		var longest float64
		for work := range q.active {
			segment := work.segment
			if work.stolen || segment == nil || !segment.bodyReady.Load() || segment.integrityPending.Load() {
				continue
			}
			if q.target != nil && segment.routeEpoch.Load() != q.task.rangeRouteEpoch.Load() {
				continue
			}
			readyAt := segment.bodyStarted.Load()
			if readyAt <= 0 {
				continue
			}
			age := time.Since(segment.created) - time.Duration(readyAt)
			if age < 2*time.Second {
				continue
			}
			done := segment.done.Load()
			left := work.span.end - work.span.start + 1 - done
			if left < 2*minimum {
				continue
			}
			speed := segment.speed.Load()
			if q.automatic {
				// Splitting a healthy body closes its original response early.
				// Require seconds of useful work on both sides; micro rescue is
				// reserved for stalled bodies, not small but progressing tails.
				if (pass > 0 && (speed > 0 || age < 5*time.Second)) ||
					(speed > 0 && helperRate > 0 && float64(left) < (float64(speed)+float64(helperRate))*8) {
					continue
				}
			}
			if done < minimum/2 && (speed > 0 || age < 5*time.Second) {
				continue
			}
			if _, useful := planRangeSplit(left, minimum, speed, helperRate, setup, commit); !useful {
				continue
			}
			eta := float64(0)
			if speed > 0 {
				eta = float64(left) / float64(speed)
			} else if age >= 5*time.Second {
				eta = 1e30
			}
			if candidate == nil || eta > longest || (eta == longest && left > largest) {
				candidate, largest, longest = work, left, eta
			}
		}
		if candidate != nil {
			segment := candidate.segment
			segment.splitMu.Lock()
			position := candidate.span.start + segment.done.Load()
			remaining := candidate.span.end - position + 1
			keep, useful := planRangeSplit(remaining, minimum, segment.speed.Load(), helperRate, setup, commit)
			if useful && segment.bodyReady.Load() && !segment.integrityPending.Load() {
				boundary := position + keep
				q.chunks = append(q.chunks, chunkSpan{start: boundary, end: candidate.span.end})
				candidate.span.end = boundary - 1
				segment.writeLimit = boundary
				q.task.segmentMu.Lock()
				segment.end = boundary - 1
				q.task.segmentMu.Unlock()
				segment.splitMu.Unlock()
				q.signalLocked()
				return true
			}
			segment.splitMu.Unlock()
		}
		if q.target == nil || minimum <= minSize {
			break
		}
		// Only a failed normal pass enables micro rescue; both sides still
		// keep at least the absolute minimum, so tiny equal tails cannot cascade.
		minimum = minSize
	}
	return false
}

// claim waits while a peer is working. If no unclaimed data remains, it splits
// a slow peer's remaining range without interrupting its current prefix.
func (q *chunkQ) claim(ctx context.Context, minSize, maxSize, nextOffset int64, worker int, target *atomic.Int32) (*chunkWork, bool) {
	for {
		q.mu.Lock()
		if q.stopped || ctx.Err() != nil {
			q.mu.Unlock()
			return nil, false
		}
		if target != nil && int32(worker) >= target.Load() {
			q.mu.Unlock()
			return nil, true
		}
		if span, ok := q.takeLocked(minSize, maxSize, nextOffset); ok {
			workCtx, cancel := context.WithCancel(ctx)
			work := &chunkWork{span: span, ctx: workCtx, cancel: cancel, started: time.Now(), worker: worker}
			if q.active == nil {
				q.active = make(map[*chunkWork]struct{})
			}
			q.active[work] = struct{}{}
			q.mu.Unlock()
			return work, true
		}
		if len(q.active) == 0 {
			q.mu.Unlock()
			return nil, false
		}
		if q.splitLocked(minSize, worker) {
			q.mu.Unlock()
			continue
		}
		if q.wake == nil {
			q.wake = make(chan struct{})
		}
		wake := q.wake
		q.mu.Unlock()
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
		case <-wake:
		case <-timer.C:
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
}

func (q *chunkQ) activate(work *chunkWork, segment *segmentProgress) {
	q.mu.Lock()
	work.segment = segment
	q.mu.Unlock()
}

func (q *chunkQ) wasStolen(work *chunkWork) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return work.stolen
}

func (q *chunkQ) finish(work *chunkWork, tail *chunkSpan) {
	q.mu.Lock()
	if work.segment != nil && !work.segment.active.Load() && !q.stopped && (q.target == nil || work.segment.routeEpoch.Load() == q.task.rangeRouteEpoch.Load()) {
		started := work.segment.bodyStarted.Load()
		if bytes := work.segment.done.Load(); started > 0 && bytes > 0 {
			elapsed := (time.Since(work.segment.created) - time.Duration(started)).Seconds()
			if elapsed > 0 {
				if q.workerRates == nil {
					q.workerRates = make(map[int]int64)
				}
				if q.workerFinished == nil {
					q.workerFinished = make(map[int]time.Time)
				}
				q.workerRates[work.worker] = int64(float64(bytes) / elapsed)
				q.workerFinished[work.worker] = time.Now()
			}
		}
	}
	delete(q.active, work)
	if tail != nil && !q.stopped {
		q.chunks = append(q.chunks, *tail)
	}
	q.signalLocked()
	q.mu.Unlock()
	work.cancel()
}
