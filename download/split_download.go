package download

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"openDL/i18n"
)

// Keep very large downloads from assigning hundreds of megabytes to one
// connection: shorter claims leave work available to faster workers for longer.
const maxAssignedChunkSize int64 = 64 << 20

func assignedChunkLimit(total, conns, minSize int64) int64 {
	return max(min(maxAssignedChunkSize, max(int64(4<<20), total/conns/4)), minSize)
}

// splitFetcher 取回一个区间写入文件；HTTP 范围与 FTP 分段共用动态分段驱动。
type splitFetcher func(out *os.File, start, end int64, validator string, segment *segmentProgress, splitCtx context.Context) (int64, error)

// downloadSplit runs ranged workers over the dynamic chunk list, writing
// into one pre-sized file.
func (m *Manager) downloadSplit(t *Task, part string, total int64, validator, cooldownOrigin string, fetch splitFetcher) error {
	out, err := os.OpenFile(extendedPath(part), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	if err := m.prepareOutputLimit(t, out, total); err != nil {
		return err
	}
	if err := out.Truncate(total); err != nil {
		return err
	}

	// Chunks smaller than this cost more in request overhead than they gain
	// in parallelism.
	automatic := t.conns == -1
	conns := t.conns
	if automatic {
		// Keep workers available for limits that may be raised during a run;
		// the automatic target below still respects the current limits.
		conns = 64
		if total <= 2<<20 {
			conns = 1
		}
	} else if conns <= 0 {
		m.mu.Lock()
		conns = m.conns
		m.mu.Unlock()
	}
	if conns < 1 {
		conns = 1
	}
	minSize := int64(1 << 20)
	if total < 4<<20 {
		minSize = max(int64(64<<10), total/8)
	}
	if automatic {
		// Idle workers can divide a slow tail down to useful 64 KiB pieces.
		minSize = 64 << 10
	}
	if avg := total / int64(conns); avg > 0 && avg < minSize {
		minSize = avg
	}
	if minSize < 64<<10 {
		minSize = 64 << 10
	}
	// Bound claims even for very large files, balancing request/checkpoint
	// overhead against work left available to faster connections.
	maxSize := assignedChunkLimit(total, int64(conns), minSize)
	t.checkpointMu.Lock()
	completed := append([]chunkSpan(nil), t.completed...)
	t.checkpointMu.Unlock()
	sort.Slice(completed, func(i, j int) bool { return completed[i].start < completed[j].start })
	if m.storeDir != "" {
		// Every retained span must match the bytes on disk. A checkpoint can
		// outlive a damaged or edited part file, even after a successful Sync.
		valid := completed[:0]
		var done int64
		for _, chunk := range completed {
			select {
			case <-t.cancel:
				return contextCanceled
			default:
			}
			digest, err := chunkDigest(out, chunk)
			if err == nil && digest == chunk.digest && digest != "" {
				valid = append(valid, chunk)
				done += chunk.end - chunk.start + 1
			}
		}
		completed = valid
		t.checkpointMu.Lock()
		t.completed = append([]chunkSpan(nil), completed...)
		t.done.Store(done)
		err := m.saveTaskLocked(t)
		t.checkpointMu.Unlock()
		if err != nil {
			return fmt.Errorf(i18n.T("无法保存下载记录：%w"), err)
		}
	}
	t.resetSegments(completed)
	q := &chunkQ{task: t, sequential: conns == 1, automatic: automatic}
	t.rangeRouteEpoch.Store(m.autoConnRouteEpoch.Load())
	var next int64
	for _, chunk := range completed {
		if next < chunk.start {
			q.chunks = append(q.chunks, chunkSpan{start: next, end: chunk.start - 1})
		}
		next = chunk.end + 1
	}
	if next < total {
		q.chunks = append(q.chunks, chunkSpan{start: next, end: total - 1})
	}
	// Spread initial claims across the file instead of repeatedly taking
	// short chunks from the beginning of one large remaining range. Split
	// only uncompleted gaps, so resumed checkpoints remain untouched.
	distribution := min(conns, int(total/minSize)+1)
	if automatic {
		// Spread only the initial batch. A resource ceiling of 64 must not
		// pre-fragment a file into 64 tiny requests before growth is justified.
		distribution = min(distribution, int(autoConnectionInitial(int32(conns), total)))
	}
	if distribution > 1 {
		var distributed []chunkSpan
		for _, gap := range q.chunks {
			start := gap.start
			for i := int64(1); i < int64(distribution); i++ {
				boundary := (total/int64(distribution))*i + (total%int64(distribution))*i/int64(distribution)
				if boundary <= start || boundary > gap.end {
					continue
				}
				distributed = append(distributed, chunkSpan{start: start, end: boundary - 1})
				start = boundary
			}
			distributed = append(distributed, chunkSpan{start: start, end: gap.end})
		}
		q.chunks = distributed
	}

	workers := conns
	if w := int(total/minSize) + 1; w < workers {
		workers = w
	}
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error
	splitCtx, stopSplit := context.WithCancel(context.Background())
	defer stopSplit()
	go func() {
		select {
		case <-t.cancel:
			stopSplit()
		case <-splitCtx.Done():
		}
	}()
	fail := func(err error) {
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
			q.stop()
			stopSplit()
		}
		errMu.Unlock()
	}
	var checkpoints *rangeCheckpoints
	if m.storeDir != "" {
		checkpoints = newRangeCheckpoints(m, t, out, func(err error) {
			fail(fmt.Errorf(i18n.T("无法保存分段进度：%w"), err))
		})
		q.checkpoints = checkpoints
	}
	var target atomic.Int32
	var rollbackEpoch atomic.Uint64
	target.Store(int32(workers))
	q.target = &target
	t.effectiveConnections.Store(int32(workers))
	var autoDone chan struct{}
	if automatic {
		// Estimate immediately from size and CPU capacity; bandwidth samples
		// refine the target without reusing another task's measurements.
		initial := m.automaticConnectionCeiling(t, workers, cooldownOrigin)
		initial = min(initial, int32(autoConnectionBudget(total-t.done.Load(), runtime.NumCPU())))
		initial = autoConnectionInitial(initial, total-t.done.Load())
		if cooldownOrigin != "" && (m.isOriginCooling(cooldownOrigin) || t.rangeSupport.Load() != 2) {
			initial = 1
		}
		target.Store(initial)
		t.effectiveConnections.Store(initial)
		routeEpoch := m.autoConnRouteEpoch.Load()
		autoDone = make(chan struct{})
		go func() {
			defer close(autoDone)
			m.adaptConnections(splitCtx, t, q, &target, &rollbackEpoch, workers, total, minSize, cooldownOrigin, routeEpoch)
		}()
	}

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			nextOffset := int64(-1)
			progressRetries := 0
			for {
				for automatic && int32(index) >= target.Load() {
					q.mu.Lock()
					done := q.stopped || (len(q.chunks) == 0 && len(q.active) == 0)
					eligible := int32(index) < target.Load()
					if q.wake == nil {
						q.wake = make(chan struct{})
					}
					wake := q.wake
					q.mu.Unlock()
					if done {
						return
					}
					if eligible {
						break
					}
					// 事件驱动等待：扩编或重新入队的唤醒信号即刻放行，1 秒兜底
					// 定时器覆盖状态变化之外的复核，替代 250ms 轮询。
					timer := time.NewTimer(time.Second)
					select {
					case <-splitCtx.Done():
						timer.Stop()
						return
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
				var autoTarget *atomic.Int32
				if automatic {
					autoTarget = &target
				}
				claimSize := maxSize
				if automatic {
					// A single-stream baseline must not be forced into the small
					// chunks calculated for the maximum possible worker count.
					claimSize = assignedChunkLimit(total, int64(max(target.Load(), 1)), minSize)
					if total > 2<<20 {
						q.mu.Lock()
						rate := q.workerRates[index]
						q.mu.Unlock()
						// Do not reapply the fixed-mode 64 MiB ceiling to fast
						// automatic streams: it would force subsecond requests.
						claimSize = automaticClaimLimit(max(minSize, total-t.done.Load()), minSize, rate,
							time.Duration(t.rangeSetupNanos.Load()), q.steady.Load())
					}
				}
				work, ok := q.claim(splitCtx, minSize, claimSize, nextOffset, index, autoTarget)
				if !ok {
					return
				}
				if work == nil {
					continue
				}
				c := work.span
				segment := t.startSegment(c)
				if checkpoints != nil {
					segment.checkpoint = checkpoints.stream(c.start, !automatic)
				}
				q.activate(work, segment)
				var written int64
				var err error
				var tail *chunkSpan
				attempts := maxRequestAttempts
				if automatic {
					attempts = 5
				}
				retryBase := 250 * time.Millisecond
				if automatic {
					retryBase = 2 * time.Second
				}
				// 重试与退避统一走 retryWithBackoff。fn 完成一次尝试的全部接受/
				// 回退决策；回滚（检查点回撤、计数与速率样本复位）必须在放弃前
				// 也执行，因此保留在 fn 的回落路径里，由 fn 返回值驱动重试判定。
				err = retryWithBackoff(splitCtx.Done(), attempts, retryBase, nil, func() error {
					written, err = fetch(out, c.start, c.end, validator, segment, work.ctx)
					q.mu.Lock()
					c.end = work.span.end
					q.mu.Unlock()
					if errors.Is(err, errChunkBoundary) && !segment.integrityPending.Load() {
						err = nil
					}
					if err == nil {
						return err
					}
					var refused *connectionRefusalError
					if automatic && errors.As(err, &refused) && q.deferRefused(work, &target) {
						// No body was accepted. Return ownership only after fetch
						// exited; the existing healthy writers remain untouched.
						if waitErr := waitRetry(splitCtx.Done(), 2*time.Second); waitErr != nil {
							err = waitErr
						} else {
							tail = &chunkSpan{start: c.start, end: c.end}
							err = nil
						}
						return err
					}
					var interruptedRead *responseReadError
					if automatic && written >= 256<<10 && written < c.end-c.start+1 &&
						!segment.integrityPending.Load() && splitCtx.Err() == nil && progressRetries < 20 &&
						errors.As(err, &interruptedRead) && transientError(interruptedRead) {
						// Headers already validated this prefix. Commit it through
						// the ordinary sync/hash/checkpoint path before resuming the
						// tail; bounded per-worker progress recovery avoids both
						// redownloading useful data and endless trickle reconnects.
						progressRetries++
						rollbackEpoch.Add(1)
						tail = &chunkSpan{start: c.start + written, end: c.end}
						c.end = c.start + written - 1
						t.segmentMu.Lock()
						segment.end = c.end
						t.segmentMu.Unlock()
						err = nil
						return err
					}
					paused := false
					select {
					case <-t.cancel:
						paused = true
					default:
					}
					if paused && written > 0 && !segment.integrityPending.Load() &&
						(errors.Is(err, contextCanceled) || errors.Is(err, context.Canceled) || errors.As(err, &interruptedRead)) {
						// Preserve a safely written prefix as an ordinary checked
						// checkpoint. Auth-int responses must instead reach EOF.
						c.end = c.start + written - 1
						t.segmentMu.Lock()
						segment.end = c.end
						t.segmentMu.Unlock()
						err = nil
						return err
					}
					if splitCtx.Err() == nil && work.ctx.Err() != nil && !segment.integrityPending.Load() && q.wasStolen(work) &&
						(errors.Is(err, contextCanceled) || errors.Is(err, context.Canceled) || errors.As(err, &interruptedRead)) {
						remaining := chunkSpan{start: c.start + written, end: c.end}
						if remaining.start <= remaining.end {
							tail = &remaining
						}
						if written > 0 {
							c.end = c.start + written - 1
							t.segmentMu.Lock()
							segment.end = c.end
							t.segmentMu.Unlock()
						}
						err = nil
						return err
					}
					if checkpoints != nil {
						// Retraction must be durable before this attempt's bytes
						// can be overwritten or reassigned to another request.
						if checkpointErr := segment.checkpoint.discard(); checkpointErr != nil {
							return fmt.Errorf(i18n.T("无法保存分段进度：%w"), checkpointErr)
						}
						segment.checkpoint = checkpoints.stream(c.start, !automatic)
					}
					rollbackEpoch.Add(1)
					t.done.Add(-written) // a retry must not double-count
					segment.done.Store(0)
					t.segmentMu.Lock()
					segment.written.Store(0)
					segment.speed.Store(0)
					segment.lastBytes, segment.lastSample = 0, time.Now()
					t.segmentMu.Unlock()
					return err
				})
				if err != nil {
					t.removeSegment(segment)
					fail(err)
					q.finish(work, nil)
					return
				}
				if written == 0 && tail != nil {
					t.removeSegment(segment)
					q.finish(work, tail)
					continue
				}
				if checkpoints != nil {
					segment.checkpoint.finish(c)
					segment.checkpoint = nil
					segment.active.Store(false)
					nextOffset = c.end + 1
					q.finish(work, tail)
					continue
				}
				t.checkpointMu.Lock()
				t.completed = append(t.completed, c)
				t.checkpointMu.Unlock()
				segment.active.Store(false)
				nextOffset = c.end + 1
				q.finish(work, tail)
			}
		}(i)
	}
	wg.Wait()
	var checkpointFailure error
	if checkpoints != nil {
		// Includes partial prefixes accepted on pause. Publication, fallback
		// and output close must never race the journal writer.
		if checkpointErr := checkpoints.close(); checkpointErr != nil {
			checkpointFailure = fmt.Errorf(i18n.T("无法保存分段进度：%w"), checkpointErr)
			fail(checkpointFailure)
		}
	}
	stopSplit()
	if autoDone != nil {
		<-autoDone
	}
	if checkpoints != nil && firstErr != nil {
		// A failed response may have earlier durable blocks. Reflect those
		// blocks, rather than either losing them or counting unrecorded bytes.
		t.checkpointMu.Lock()
		retained := append([]chunkSpan(nil), t.completed...)
		var done int64
		for _, span := range retained {
			done += span.end - span.start + 1
		}
		t.done.Store(done)
		t.checkpointMu.Unlock()
		t.resetSegments(retained)
	}
	if checkpointFailure != nil {
		return checkpointFailure
	}
	if firstErr != nil {
		return firstErr
	}
	if err := out.Close(); err != nil {
		return err
	}
	select {
	case <-t.cancel:
		return contextCanceled
	default:
	}
	return m.finishFile(t, part)
}
