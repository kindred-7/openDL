package download

import (
	"os"
	"testing"
	"time"
)

func TestSegmentSnapshotsTrackAndDetachProgress(t *testing.T) {
	task := &Task{}
	task.resetSegments([]chunkSpan{{start: 0, end: 3}})
	active := task.startSegment(chunkSpan{start: 8, end: 15})
	f, err := os.CreateTemp(t.TempDir(), "segment")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := offsetWriter{f: f, off: 8, segment: active}
	if _, err := w.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	firstSample := active.lastSample.Add(time.Second)
	task.sampleSegments(firstSample)
	first := task.segmentSnapshots()
	if len(first) != 2 || first[0].Downloaded != 4 || first[0].Active || first[1].Start != 8 || first[1].Downloaded != 3 || !first[1].Active || first[1].Speed != 3 {
		t.Fatalf("wrong segment progress: %+v", first)
	}
	active.done.Store(0)
	if _, err := w.Write([]byte("xyz")); err != nil {
		t.Fatal(err)
	}
	task.sampleSegments(firstSample.Add(time.Second))
	if got := task.segmentSnapshots()[1].Speed; got != 3 {
		t.Fatalf("retry throughput lost: %d", got)
	}
	task.sampleSegments(firstSample.Add(2 * time.Second))
	if got := task.segmentSnapshots()[1].Speed; got != 0 {
		t.Fatalf("stalled segment speed did not clear: %d", got)
	}
	if first[1].Downloaded != 3 {
		t.Fatal("snapshot changed after retry")
	}
	task.removeSegment(active)
	if len(task.segmentSnapshots()) != 1 {
		t.Fatal("failed segment remains visible")
	}
}
