package download

import (
	"context"
	"errors"
	"io"
	"log"
	"net/url"
	"os"
	"sync"
	"time"

	"openDL/i18n"
)

// 媒体分片预取流水线：并发取回到内存缓冲、按序提交写盘与记账。
// fMP4 时间轴缝合（shiftTfdt/accept）与检查点仍严格按分片顺序执行；
// 加密分片本就全量驻留内存，预取同样安全。预取 goroutine 的请求上下文由
// t.cancel 派生，取消时立即释放；取回失败只丢弃当前及预取缓冲，不影响已
// 提交分片。

// segmentPrefetchWindow 预取流水线宽度：2-4 个分片并发取回，这里取 3。
const segmentPrefetchWindow = 3

// maxPrefetchedSegment 单个预取分片的内存缓冲上限；更大的分片退回串行
// 流式路径，保持既有内存占用语义。
const maxPrefetchedSegment = 32 << 20

// errPrefetchOversize 标记分片超过内存缓冲上限，由调用方退回串行路径。
var errPrefetchOversize = errors.New("segment exceeds prefetch buffer")

// fetchedSegment is a response awaiting its turn to be written.
type fetchedSegment struct {
	payload []byte
	record  hlsCheckpoint
	ranges  map[string]hlsRangeState
	err     error
}

// mediaJournal 把媒体分片检查点的整份 JSON 重写按条数或时间批量落盘，消除
// 每分片一次全量重写的放大。崩溃窗口内未记账的分片恢复时重新下载，
// hlsCheckpoint 的前缀校验幂等。Sync 先于记账的顺序不变：分片字节先
// out.Sync，批量落盘只推迟 journal 重写。
type mediaJournal struct {
	m       *Manager
	t       *Task
	pending int
	last    time.Time
}

const mediaJournalBatch = 8
const mediaJournalInterval = 2 * time.Second

// saved 登记一条检查点，达到批量条件时立即落盘。
func (j *mediaJournal) saved() error {
	j.pending++
	if j.pending >= mediaJournalBatch || time.Since(j.last) >= mediaJournalInterval {
		return j.flush()
	}
	return nil
}

// flush 立即重写检查点 journal。
func (j *mediaJournal) flush() error {
	if j.pending == 0 {
		return nil
	}
	j.t.checkpointMu.Lock()
	err := j.m.saveTaskLocked(j.t)
	j.t.checkpointMu.Unlock()
	if err == nil {
		j.pending = 0
		j.last = time.Now()
	}
	return err
}

// flushLog 收尾时尽力落盘；错误与取消路径不再覆盖原错误。
func (j *mediaJournal) flushLog() {
	if err := j.flush(); err != nil {
		log.Printf(i18n.T("保存下载记录 %s: %v"), j.t.id, err)
	}
}

// Prefetch consecutive segments in a fixed-size ring. Only the coordinator
// publishes range state, so a later byte range for the same resource waits
// until the earlier response is validated and committed.
func (m *Manager) prefetchMediaSegments(t *Task, original, page *url.URL, segments []hlsSegment, ranges map[string]hlsRangeState, commit func(int, *fetchedSegment) error, fallback func(int) error) error {
	if len(segments) == 0 {
		return nil
	}
	ctx, stop := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	workers.Go(func() {
		select {
		case <-t.cancel:
			stop()
		case <-ctx.Done():
		}
	})
	defer func() {
		stop()
		workers.Wait()
	}()

	var slots [segmentPrefetchWindow]chan *fetchedSegment
	inFlightRanges := make(map[string]bool, len(slots))
	next := 0
	for i, segment := range segments {
		select {
		case <-t.cancel:
			return contextCanceled
		default:
		}
		for next < len(segments) && next-i < len(slots) {
			candidate := segments[next]
			var snapshot map[string]hlsRangeState
			if candidate.length > 0 {
				if inFlightRanges[candidate.url] {
					break
				}
				inFlightRanges[candidate.url] = true
				snapshot = make(map[string]hlsRangeState, 1)
				if previous, ok := ranges[candidate.url]; ok {
					snapshot[candidate.url] = previous
				}
			}
			slot := next % len(slots)
			if slots[slot] == nil {
				slots[slot] = make(chan *fetchedSegment, 1)
			}
			result := slots[slot]
			workers.Go(func() {
				result <- m.fetchHLSSegment(t, ctx, original, page, candidate, snapshot)
			})
			next++
		}
		var fetched *fetchedSegment
		select {
		case fetched = <-slots[i%len(slots)]:
		case <-ctx.Done():
			return contextCanceled
		}
		if ctx.Err() != nil {
			return contextCanceled
		}
		if errors.Is(fetched.err, errPrefetchOversize) {
			stop()
			workers.Wait()
			for j := i + 1; j < next; j++ {
				<-slots[j%len(slots)]
			}
			for j := i; j < len(segments); j++ {
				if err := fallback(j); err != nil {
					return err
				}
			}
			return nil
		}
		if fetched.err != nil {
			return fetched.err
		}
		if err := commit(i, fetched); err != nil {
			return err
		}
		if segment.length > 0 {
			if state, ok := fetched.ranges[segment.url]; ok {
				ranges[segment.url] = state
			}
			delete(inFlightRanges, segment.url)
		}
	}
	return nil
}

// prefetchHLSSegments 以有界流水线预取点播 HLS/DASH 分片并按序提交。
func (m *Manager) prefetchHLSSegments(t *Task, out *os.File, progress *segmentProgress, original, base *url.URL, segments []hlsSegment, ranges map[string]hlsRangeState, mp4 *hlsMP4State, expected string, safePrefix *bool, journal *mediaJournal) error {
	if len(segments) == 0 {
		return nil
	}
	commit := func(i int, f *fetchedSegment) error {
		start := t.done.Load()
		target := io.Writer(out)
		if !segments[i].mp4 {
			target = &tsWriter{w: out}
		}
		writer := &segmentWriter{writer: target, segment: progress}
		written, err := writeHLSPlaintext(t, writer, f.payload)
		if err != nil {
			return err
		}
		if written != int64(len(f.payload)) {
			return io.ErrShortWrite
		}
		return m.commitHLSSegment(t, out, progress, segments[i], mp4, expected, safePrefix, false, start, &f.record, journal)
	}
	fallback := func(i int) error {
		return m.appendHLSSegment(t, out, progress, original, base, segments[i], ranges, mp4, expected, safePrefix, false, journal)
	}
	return m.prefetchMediaSegments(t, original, base, segments, ranges, commit, fallback)
}

// Only this worker sees its range snapshot until the ordered commit.
func (m *Manager) fetchHLSSegment(t *Task, ctx context.Context, original, page *url.URL, segment hlsSegment, ranges map[string]hlsRangeState) *fetchedSegment {
	f := &fetchedSegment{ranges: ranges}
	previous, existed := ranges[segment.url]
	f.err = retryWithBackoff(ctx.Done(), maxRequestAttempts, 250*time.Millisecond, func() error {
		if existed {
			ranges[segment.url] = previous
		} else {
			delete(ranges, segment.url)
		}
		return nil
	}, func() error {
		f.record = hlsCheckpoint{}
		payload, err := m.fetchHLSSegmentOnce(t, ctx, original, page, segment, ranges, &f.record)
		if err != nil {
			return err
		}
		f.payload = payload
		return nil
	})
	return f
}

func (m *Manager) fetchHLSSegmentOnce(t *Task, ctx context.Context, original, page *url.URL, segment hlsSegment, ranges map[string]hlsRangeState, record *hlsCheckpoint) ([]byte, error) {
	var key []byte
	if segment.keyURL != "" {
		var err error
		key, err = m.hlsKey(t, ctx, original, page, segment.keyURL)
		if err != nil {
			return nil, err
		}
	}
	resp, cancel, initLimit, err := m.openHLSSegment(t, ctx, original, page, segment, ranges, record)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer resp.Body.Close()
	body := &idleReader{body: resp.Body, timeout: m.bodyIdleTimeout, cancel: cancel}
	network := mediaNetworkReader{manager: m, task: t, source: body, canceled: ctx.Done()}
	bodyLength := resp.ContentLength
	if segment.length > 0 {
		bodyLength = segment.length
	}
	if bodyLength > maxPrefetchedSegment {
		return nil, errPrefetchOversize
	}
	var source io.Reader = network
	if bodyLength >= 0 {
		source = io.LimitReader(network, bodyLength)
	}
	if segment.init {
		source = io.LimitReader(source, initLimit+1)
	}
	var payload []byte
	if segment.keyURL != "" {
		plaintext, err := readHLSAESWithLimit(source, key, segment.iv, bodyLength, maxPrefetchedSegment, errPrefetchOversize)
		if err != nil {
			return nil, err
		}
		payload = plaintext
	} else {
		data, err := io.ReadAll(io.LimitReader(source, maxPrefetchedSegment+1))
		if err != nil {
			return nil, &responseReadError{err: err}
		}
		payload = data
	}
	if int64(len(payload)) > maxPrefetchedSegment {
		return nil, errPrefetchOversize
	}
	if err := checkHLSSegmentWritten(segment, body, resp.ContentLength, record, int64(len(payload)), bodyLength >= 0); err != nil {
		return nil, err
	}
	record.SHA256 = digestSegmentBytes(payload)
	return payload, nil
}
