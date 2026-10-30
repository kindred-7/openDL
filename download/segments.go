package download

import (
	"sync/atomic"
	"time"
)

// Segment is a snapshot of one transfer range, with an inclusive end. End is
// -1 for a single stream whose total size is still unknown. Downloaded is
// its contiguous prefix, not an estimate from total progress.
type Segment struct {
	Start      int64 `json:"start"`
	End        int64 `json:"end"`
	Downloaded int64 `json:"downloaded"`
	Active     bool  `json:"active"`
	Speed      int64 `json:"speed"` // bytes written per second over the latest sample
}

type segmentProgress struct {
	start, end       int64
	done             atomic.Int64
	active           atomic.Bool
	integrityPending atomic.Bool // an auth-int response must reach EOF intact
	written          atomic.Int64
	speed            atomic.Int64
	lastSample       time.Time // guarded by Task.segmentMu
	lastBytes        int64     // guarded by Task.segmentMu
}

func (t *Task) resetSegments(completed []chunkSpan) {
	t.segmentMu.Lock()
	defer t.segmentMu.Unlock()
	t.segments = nil
	for _, c := range completed {
		segment := &segmentProgress{start: c.start, end: c.end}
		segment.done.Store(c.end - c.start + 1)
		t.segments = append(t.segments, segment)
	}
}

func (t *Task) startSegment(c chunkSpan) *segmentProgress {
	segment := &segmentProgress{start: c.start, end: c.end, lastSample: time.Now()}
	segment.active.Store(true)
	t.segmentMu.Lock()
	t.segments = append(t.segments, segment)
	t.segmentMu.Unlock()
	return segment
}

func (t *Task) finishStream(segment *segmentProgress, written int64) {
	t.segmentMu.Lock()
	segment.end = written - 1
	segment.active.Store(false)
	t.segmentMu.Unlock()
}

func (s *segmentProgress) addWritten(n int) {
	s.done.Add(int64(n))
	s.written.Add(int64(n))
}

func (t *Task) sampleSegments(now time.Time) {
	t.segmentMu.Lock()
	defer t.segmentMu.Unlock()
	for _, segment := range t.segments {
		if !segment.active.Load() {
			segment.speed.Store(0)
			continue
		}
		bytes := segment.written.Load()
		if elapsed := now.Sub(segment.lastSample); elapsed > 0 {
			segment.speed.Store(int64(float64(bytes-segment.lastBytes) / elapsed.Seconds()))
			segment.lastBytes, segment.lastSample = bytes, now
		}
	}
}

func (t *Task) removeSegment(segment *segmentProgress) {
	t.segmentMu.Lock()
	defer t.segmentMu.Unlock()
	for i, s := range t.segments {
		if s == segment {
			t.segments = append(t.segments[:i], t.segments[i+1:]...)
			return
		}
	}
}

func (t *Task) segmentSnapshots() []Segment {
	t.segmentMu.Lock()
	defer t.segmentMu.Unlock()
	if len(t.segments) == 0 {
		return nil
	}
	out := make([]Segment, 0, len(t.segments))
	for _, s := range t.segments {
		active := s.active.Load()
		speed := int64(0)
		if active {
			speed = s.speed.Load()
		}
		out = append(out, Segment{Start: s.start, End: s.end, Downloaded: s.done.Load(), Active: active, Speed: speed})
	}
	return out
}
