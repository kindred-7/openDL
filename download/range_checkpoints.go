package download

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"os"
	"sync/atomic"
	"time"
)

const rangeCheckpointBlock = 16 << 20
const rangeCheckpointBatch = 64

// Only descriptors and incremental hashes are queued, never response bodies.
// One task owns this writer; closing it is a barrier before closing/publishing
// the output or resetting the representation during a fallback.
type rangeCheckpoints struct {
	m       *Manager
	t       *Task
	out     *os.File
	queue   chan rangeCheckpointEvent
	done    chan struct{}
	waiting atomic.Int32
	err     error // published by closing done
	fail    func(error)
}

type rangeCheckpointEvent struct {
	owner   *rangeCheckpointStream
	span    chunkSpan
	finish  bool
	discard chan error
}

// Mutable hash state belongs exclusively to the response's download worker.
// The background writer uses the pointer only as an ownership identity.
type rangeCheckpointStream struct {
	writer    *rangeCheckpoints
	start     int64
	next      int64
	bytes     int64
	hash      hash.Hash
	protected bool // hash continuously, but publish nothing until EOF authentication succeeds
	lastSeal  time.Time
}

func newRangeCheckpoints(m *Manager, t *Task, out *os.File, fail func(error)) *rangeCheckpoints {
	p := &rangeCheckpoints{m: m, t: t, out: out, fail: fail,
		queue: make(chan rangeCheckpointEvent, 2*rangeCheckpointBatch), done: make(chan struct{})}
	go p.run()
	return p
}

func (p *rangeCheckpoints) stream(start int64) *rangeCheckpointStream {
	return &rangeCheckpointStream{writer: p, start: start, next: start, hash: sha256.New(), lastSeal: time.Now()}
}

func (p *rangeCheckpoints) enqueue(event rangeCheckpointEvent) {
	select {
	case p.queue <- event:
		return
	default:
	}
	p.waiting.Add(1)
	p.queue <- event
	p.waiting.Add(-1)
}

func (p *rangeCheckpoints) backlogged() bool {
	return p.waiting.Load() > 0 || len(p.queue) >= cap(p.queue)*3/4
}

func (p *rangeCheckpoints) close() error {
	close(p.queue)
	<-p.done
	return p.err
}

func (s *rangeCheckpointStream) write(data []byte) {
	if s.protected {
		_, _ = s.hash.Write(data)
		s.bytes += int64(len(data))
		return
	}
	for len(data) > 0 {
		n := min(len(data), rangeCheckpointBlock-int(s.bytes))
		_, _ = s.hash.Write(data[:n])
		s.bytes += int64(n)
		data = data[n:]
		if s.bytes == rangeCheckpointBlock {
			s.seal()
		}
	}
	// A slow, long response still gets recoverable progress without issuing
	// another request. Keep a floor so trickle traffic cannot flood the journal.
	if s.bytes >= 256<<10 && time.Since(s.lastSeal) >= 5*time.Second {
		s.seal()
	}
}

func (s *rangeCheckpointStream) seal() {
	if s.bytes == 0 {
		return
	}
	s.writer.enqueue(rangeCheckpointEvent{owner: s, span: chunkSpan{
		start: s.next, end: s.next + s.bytes - 1, digest: hex.EncodeToString(s.hash.Sum(nil))}})
	s.next += s.bytes
	s.bytes = 0
	s.hash.Reset()
	s.lastSeal = time.Now()
}

func (s *rangeCheckpointStream) finish(span chunkSpan) {
	if s.next == s.start && s.bytes == 0 {
		// Fetchers without a streaming writer hand off only a fully accepted
		// range; retain that path without forcing disk hashing onto their worker.
		s.writer.enqueue(rangeCheckpointEvent{owner: s, span: span})
	} else {
		s.seal()
	}
	s.writer.enqueue(rangeCheckpointEvent{owner: s, finish: true})
}

// The worker must wait for this journal retraction before overwriting bytes
// on retry. FIFO ordering also covers descriptors not yet synchronized.
func (s *rangeCheckpointStream) discard() error {
	ack := make(chan error, 1)
	s.writer.enqueue(rangeCheckpointEvent{owner: s, discard: ack})
	return <-ack
}

type rangeCheckpointWriter struct {
	writer io.Writer
	stream *rangeCheckpointStream
}

func (w rangeCheckpointWriter) Write(data []byte) (int, error) {
	n, err := w.writer.Write(data)
	if n > 0 {
		// The offset writer has released its split lock. Queuing can apply
		// disk backpressure without blocking the range ownership lock.
		w.stream.write(data[:n])
	}
	return n, err
}

func (p *rangeCheckpoints) run() {
	defer close(p.done)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	pending := make(map[*rangeCheckpointStream][]chunkSpan)
	persisted := make(map[*rangeCheckpointStream][]chunkSpan)
	finished := make(map[*rangeCheckpointStream]bool)
	count := 0
	setError := func(err error) {
		if p.err == nil && err != nil {
			p.err = err
			p.fail(err)
		}
	}
	flush := func() {
		if count == 0 || p.err != nil {
			return
		}
		started := time.Now()
		if err := p.out.Sync(); err != nil {
			setError(err)
			return
		}
		// All spans are immutable while pending. Retries must go through the
		// discard barrier; new owners are handed only disjoint remaining data.
		for owner, spans := range pending {
			for i := range spans {
				if spans[i].digest == "" {
					var err error
					spans[i].digest, err = chunkDigest(p.out, spans[i])
					if err != nil {
						setError(err)
						return
					}
				}
			}
			pending[owner] = spans
		}
		p.t.checkpointMu.Lock()
		for owner, spans := range pending {
			p.t.completed = append(p.t.completed, spans...)
			persisted[owner] = append(persisted[owner], spans...)
		}
		err := p.m.saveTaskLocked(p.t)
		p.t.checkpointMu.Unlock()
		pending = make(map[*rangeCheckpointStream][]chunkSpan)
		count = 0
		for owner := range finished {
			delete(persisted, owner)
			delete(finished, owner)
		}
		if err != nil {
			setError(err)
		} else {
			observeRangeCost(&p.t.rangeCommitNanos, time.Since(started))
		}
	}
	for {
		select {
		case <-ticker.C:
			flush()
		case event, ok := <-p.queue:
			if !ok {
				flush()
				return
			}
			if event.discard != nil {
				count -= len(pending[event.owner])
				delete(pending, event.owner)
				spans := persisted[event.owner]
				if len(spans) > 0 && p.err == nil {
					remove := make(map[[2]int64]bool, len(spans))
					for _, span := range spans {
						remove[[2]int64{span.start, span.end}] = true
					}
					p.t.checkpointMu.Lock()
					kept := p.t.completed[:0]
					for _, span := range p.t.completed {
						if !remove[[2]int64{span.start, span.end}] {
							kept = append(kept, span)
						}
					}
					p.t.completed = kept
					err := p.m.saveTaskLocked(p.t)
					p.t.checkpointMu.Unlock()
					setError(err)
				}
				delete(persisted, event.owner)
				delete(finished, event.owner)
				event.discard <- p.err
				continue
			}
			// After a disk error keep draining and acknowledging barriers so
			// canceled workers cannot deadlock while the task is being stopped.
			if p.err != nil {
				continue
			}
			if event.finish {
				if len(pending[event.owner]) == 0 {
					delete(persisted, event.owner)
				} else {
					finished[event.owner] = true
				}
			} else {
				pending[event.owner] = append(pending[event.owner], event.span)
				count++
			}
			if count >= rangeCheckpointBatch {
				flush()
			}
		}
	}
}
