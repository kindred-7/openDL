package download

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A peer may take only bytes the original writer has not written, and the
// original writer must stop at the new boundary even if one read crosses it.
func TestSplitActiveRangeKeepsWritesDisjoint(t *testing.T) {
	out, err := os.Create(filepath.Join(t.TempDir(), "part"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()

	task := &Task{}
	span := chunkSpan{start: 0, end: 31}
	segment := task.startSegment(span)
	// This fixture represents a validated response already transferring data.
	segment.created = time.Now().Add(-3 * time.Second)
	segment.bodyStarted.Store(1)
	segment.bodyReady.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	work := &chunkWork{span: span, ctx: ctx, cancel: cancel, segment: segment, started: time.Now().Add(-3 * time.Second)}
	q := &chunkQ{task: task, active: map[*chunkWork]struct{}{work: {}}}
	writer := &offsetWriter{f: out, segment: segment}
	if n, err := writer.Write([]byte("abcdefgh")); err != nil || n != 8 {
		t.Fatalf("initial write = %d, %v", n, err)
	}

	peer, ok := q.claim(context.Background(), 4, 128, -1, 1, nil)
	if !ok || peer == nil {
		t.Fatal("expected a peer to claim the split tail")
	}
	defer peer.cancel()
	if work.span.end != 19 || peer.span.start != 20 || peer.span.end != 31 {
		t.Fatalf("original = %+v, peer = %+v", work.span, peer.span)
	}
	if n, err := writer.Write([]byte("ijklmnopqrstuvwxyz012345")); n != 12 || !errors.Is(err, errChunkBoundary) {
		t.Fatalf("boundary write = %d, %v; want 12 and boundary error", n, err)
	}
	peerSegment := task.startSegment(peer.span)
	peerWriter := &offsetWriter{f: out, off: peer.span.start, segment: peerSegment}
	if n, err := peerWriter.Write([]byte("UVWXYZ012345")); err != nil || n != 12 {
		t.Fatalf("peer write = %d, %v", n, err)
	}
	data, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "abcdefghijklmnopqrstUVWXYZ012345"; got != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}
