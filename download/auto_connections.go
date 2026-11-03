package download

import (
	"context"
	"net/url"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

const autoConnectionInterval = 2 * time.Second
const autoConnectionSampleWindows = 3
const autoConnectionHold = 30 * time.Second
const autoConnectionPressureInterval = 10 * time.Second
const autoConnectionReprobes = 6

type connectionRefusalError struct{ err error }

func (e *connectionRefusalError) Error() string { return e.err.Error() }
func (e *connectionRefusalError) Unwrap() error { return e.err }

func singleResponseDownload(u *url.URL) bool {
	return u != nil && strings.EqualFold(strings.TrimRight(u.Path, "/"), "/multiple-download") && u.Query().Get("token") != ""
}

// The size/CPU estimate is a resource ceiling, not a claim that all of those
// connections improve throughput. Each task measures that independently.
func autoConnectionBudget(total int64, logicalCPUs int) int {
	if total <= 2<<20 {
		return 1
	}
	cpuLimit := min(max(logicalCPUs, 1), 16) * 4
	return int(min(total/(1<<20), int64(cpuLimit), 64))
}

func autoConnectionInitial(ceiling int32, total int64) int32 {
	initial := int32(4)
	if total <= 2<<20 {
		initial = 1
	}
	return max(1, min(initial, ceiling))
}

func autoConnectionProbeWorthwhile(remaining, minSize int64, target int32, rate float64) bool {
	return remaining > int64(target)*max(minSize, int64(1<<20)) &&
		(rate <= 0 || float64(remaining)/rate > 8)
}

func autoConnectionTail(remaining, total int64, ceiling int32) bool {
	return remaining < int64(ceiling)*(2<<20) && remaining < total/50
}

func (m *Manager) automaticConnectionCeiling(t *Task, workers int, resourceOrigins ...string) int32 {
	return min(int32(autoConnectionBudget(t.total.Load(), runtime.NumCPU())), m.automaticConnectionHardCeiling(t, workers, resourceOrigins...))
}

func (m *Manager) automaticConnectionHardCeiling(t *Task, workers int, resourceOrigins ...string) int32 {
	ceiling := min(workers, 64)
	if limit := m.siteConnectionLimit(t.sourceURL()); limit > 0 && limit < ceiling {
		ceiling = limit
	}
	for _, origin := range resourceOrigins {
		if limit := m.siteConnectionLimit(origin); limit > 0 && limit < ceiling {
			ceiling = limit
		}
	}
	if limit := m.MaxConnections(); limit > 0 && limit < ceiling {
		ceiling = limit
	}
	return int32(max(1, ceiling))
}

func (m *Manager) automaticConnectionsLimited(t *Task, rate float64) bool {
	m.mu.Lock()
	queue := t.queue
	active := m.active
	queueKey := queue
	if queueKey == "" {
		queueKey = "main"
	}
	queueActive := m.queueActive[queueKey]
	m.mu.Unlock()
	queuePace := m.queuePace(queue)
	waiting, _ := m.limiter.contentionState()
	if waiting {
		return true
	}
	// A configured cap does not mask capacity until it is nearly saturated.
	// With multiple sharers the per-task sample cannot identify who consumed
	// the shared allowance, so conservatively avoid learning from it.
	if cap := m.pace.getRate(); cap > 0 && (active > 1 || rate >= float64(cap)*0.85) {
		return true
	}
	if queuePace != nil {
		if cap := queuePace.getRate(); cap > 0 && (queueActive > 1 || rate >= float64(cap)*0.85) {
			return true
		}
	}
	return false
}

// A sample at target N is meaningful only when its N workers reached a
// validated response body. In particular, do not interpret handshake delays
// or a peer gate's waiting workers as evidence against the new target.
func autoConnectionBodiesReady(q *chunkQ, target int32) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	ready := make(map[int]bool)
	for work := range q.active {
		if int32(work.worker) < target && !work.stolen && work.segment != nil && work.segment.bodyReady.Load() && work.segment.done.Load() > 0 && work.segment.routeEpoch.Load() == q.task.rangeRouteEpoch.Load() {
			ready[work.worker] = true
		}
	}
	// Fast ranges can finish between ticks and spend the next tick flushing
	// their checkpoint. Their recent valid progress still proves readiness.
	for worker, finished := range q.workerFinished {
		if int32(worker) < target && time.Since(finished) <= 2*autoConnectionInterval {
			ready[worker] = true
		}
	}
	return int32(len(ready)) >= target
}

type autoConnectionPhase uint8

const (
	autoConnectionBaseline autoConnectionPhase = iota
	autoConnectionProbe
	autoConnectionHoldPhase
	autoConnectionBackoff
	autoConnectionDrain
)

// All measurements and trial decisions belong to this task only. Growth and
// reduction probes have a contemporaneous reference, rather than deriving a
// connection count from an absolute bandwidth threshold.
type autoConnectionControl struct {
	phase          autoConnectionPhase
	afterDrain     autoConnectionPhase
	reference      int32
	referenceRate  float64
	peak           float64
	holdUntil      time.Time
	cooldown       time.Duration
	reducing       bool
	recovery       bool
	hard           bool
	shrinkDisabled bool
	drops          int
	windows        int
	rateSum        float64
	holdSamples    int
	failures       int
	reprobes       int
}

// A contaminated sample discards the comparison, never the task's rejection
// evidence or finite exploration budgets. A new route explicitly clears those.
func (c *autoConnectionControl) reset() {
	*c = autoConnectionControl{phase: autoConnectionBaseline, hard: c.hard,
		shrinkDisabled: c.shrinkDisabled, failures: c.failures, reprobes: c.reprobes,
		cooldown: c.cooldown}
}

func (c *autoConnectionControl) hold(now time.Time, rate float64) {
	c.phase = autoConnectionHoldPhase
	c.peak = rate
	if c.cooldown <= 0 {
		c.cooldown = autoConnectionHold
	}
	c.holdUntil = now.Add(c.cooldown)
	c.drops, c.windows, c.holdSamples = 0, 0, 0
	c.rateSum = 0
	c.recovery, c.reducing = false, false
	if c.reprobes >= autoConnectionReprobes {
		c.hard = true
	}
}

func (c *autoConnectionControl) backoff(now time.Time) {
	c.reset()
	c.hard = true
	c.phase = autoConnectionBackoff
	c.holdUntil = now.Add(autoConnectionHold)
}

func (c *autoConnectionControl) drain() {
	if c.phase != autoConnectionDrain {
		c.afterDrain, c.phase = c.phase, autoConnectionDrain
	}
}
func (c *autoConnectionControl) drained() {
	if c.phase == autoConnectionDrain {
		c.phase = c.afterDrain
	}
}
func (c *autoConnectionControl) startProbe(current, next int32, rate float64, reducing bool) int32 {
	c.phase, c.reference, c.referenceRate = autoConnectionProbe, current, rate
	c.reducing, c.windows = reducing, 0
	c.rateSum = 0
	return next
}

// Only near-linear measured scaling can lift the size/CPU prior. Explicit
// global/site limits remain hard, and every new worker needs 4 MiB of work.
func (c *autoConnectionControl) uncap(current, ceiling, hard int32, rate float64, remaining int64) int32 {
	if c.phase != autoConnectionProbe || c.reducing || c.hard ||
		current < ceiling || c.reference < 1 || current <= c.reference || c.referenceRate <= 0 ||
		c.windows+1 < autoConnectionSampleWindows {
		return ceiling
	}
	rate = (c.rateSum + max(rate, 0)) / float64(c.windows+1)
	if rate <= c.referenceRate*1.05 {
		return ceiling
	}
	gain := float64(current)/float64(c.reference) - 1
	if rate/c.referenceRate-1 < gain*0.8 {
		return ceiling
	}
	next := min(hard, ceiling*2)
	if next > ceiling && remaining >= int64(next)*(4<<20) {
		return next
	}
	return ceiling
}

// Compare several clean windows so request transitions and short bursts do not
// repeatedly change concurrency. Contaminated windows are gated by the caller.
func (c *autoConnectionControl) observe(now time.Time, current, ceiling int32, rate float64, allowGrowth func(int32) bool) int32 {
	if c.phase != autoConnectionBackoff {
		if c.phase == autoConnectionHoldPhase {
			c.holdSamples++
		}
		c.windows++
		c.rateSum += max(rate, 0)
		if c.windows < autoConnectionSampleWindows {
			return current
		}
		rate = c.rateSum / float64(c.windows)
		c.windows, c.rateSum = 0, 0
	}
	switch c.phase {
	case autoConnectionBackoff:
		if now.Before(c.holdUntil) {
			return current
		}
		c.hold(now, rate)
		return current
	case autoConnectionProbe:
		if c.reducing {
			if rate > c.referenceRate*1.05 {
				c.hold(now, rate)
				return current
			}
			previous := c.reference
			c.shrinkDisabled = true
			c.hold(now, rate)
			return previous
		}
		if rate < c.referenceRate*0.5 {
			previous := c.reference
			c.hard = true
			c.hold(now, rate)
			return previous
		}
		if rate <= c.referenceRate*1.05 {
			if c.recovery {
				previous := c.reference
				c.failures++
				if c.cooldown <= 0 {
					c.cooldown = autoConnectionHold
				}
				c.cooldown *= 2
				if c.failures >= 2 {
					c.hard = true
				}
				c.hold(now, rate)
				return previous
			}
			// Extra streams without a sustained benefit are not an accepted
			// scale. Restore the measured reference and let surplus bodies drain.
			previous, referenceRate := c.reference, c.referenceRate
			c.failures, c.cooldown = 0, autoConnectionHold
			c.hold(now, referenceRate)
			return previous
		}
		c.recovery, c.failures, c.cooldown = false, 0, autoConnectionHold
		c.phase = autoConnectionBaseline
	case autoConnectionHoldPhase:
		c.peak = max(rate, c.peak*0.99)
		if !c.shrinkDisabled && current > 1 && c.peak > 0 && rate < c.peak*0.5 {
			c.drops++
		} else {
			c.drops = 0
		}
		if c.drops >= 2 {
			c.hard = true
			c.drops = 0
			return c.startProbe(current, max(1, current/2), rate, true)
		}
		// A timer alone is not evidence that a stable transfer needs another
		// connection. Reserve recovery probes for sustained throughput loss.
		if c.hard || rate >= c.peak*0.8 || now.Before(c.holdUntil) || c.holdSamples < int(c.cooldown/autoConnectionInterval) || c.reprobes >= autoConnectionReprobes || current >= ceiling || !allowGrowth(current+1) {
			return current
		}
		c.reprobes++
		c.recovery = true
		return c.startProbe(current, current+1, rate, false)
	}
	if rate <= 0 {
		return current
	}
	next := min(ceiling, current*2)
	if !c.hard && next > current && allowGrowth(next) {
		return c.startProbe(current, next, rate, false)
	}
	c.hold(now, rate)
	return current
}

func (m *Manager) adaptConnections(ctx context.Context, t *Task, q *chunkQ, target *atomic.Int32,
	rollbackEpoch *atomic.Uint64, workers int, total, minSize int64, cooldownOrigin string, routeEpoch uint64) {
	ticker := time.NewTicker(autoConnectionInterval)
	defer ticker.Stop()
	var control autoConnectionControl
	control.reset()
	lastBytes, lastTime := t.done.Load(), time.Now()
	lastHard := m.automaticConnectionHardCeiling(t, workers, cooldownOrigin)
	softCeiling := m.automaticConnectionCeiling(t, workers, cooldownOrigin)
	_, lastContention := m.limiter.contentionState()
	lastRollback := rollbackEpoch.Load()
	settling, constrained, wasCooling := true, false, false
	var pressureUntil time.Time
	lastRefusal := uint64(0)
	// Only this initial flag can use the Range-ready startup shortcut. Proxy,
	// limit and pressure changes clear it permanently for this invocation.
	rangeStartup := cooldownOrigin != "" && target.Load() == 1 &&
		autoConnectionInitial(softCeiling, total) > 1 && !m.isOriginCooling(cooldownOrigin)
	change := func(next int32, pressure bool) {
		q.steady.Store(false)
		next = max(1, min(next, m.automaticConnectionHardCeiling(t, workers, cooldownOrigin)))
		q.mu.Lock()
		if q.refusalCap > 0 {
			next = min(next, q.refusalCap)
		}
		old := target.Swap(next)
		t.effectiveConnections.Store(next)
		q.signalLocked()
		q.mu.Unlock()
		if next < old {
			control.drain()
			if pressure {
				q.retire(next)
			}
		}
		settling = true
	}
	invalidate := func() {
		q.steady.Store(false)
		phase := control.phase
		if phase == autoConnectionDrain {
			phase = control.afterDrain
		}
		backoffUntil := control.holdUntil
		holdSamples := control.holdSamples
		var restore int32
		if phase == autoConnectionProbe {
			restore = control.reference
		}
		control.reset()
		if phase == autoConnectionBackoff || phase == autoConnectionHoldPhase {
			control.phase, control.holdUntil = phase, backoffUntil
			control.holdSamples = holdSamples
		}
		if restore > 0 {
			change(restore, false)
		}
		settling = true
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			bytes := t.done.Load()
			delta, elapsed := bytes-lastBytes, now.Sub(lastTime).Seconds()
			lastBytes, lastTime = bytes, now
			current := target.Load()
			hardCeiling := m.automaticConnectionHardCeiling(t, workers, cooldownOrigin)
			ceiling := min(softCeiling, hardCeiling)
			rollback := rollbackEpoch.Load()
			retried := rollback != lastRollback
			lastRollback = rollback
			generation := m.autoConnRouteEpoch.Load()
			if generation != routeEpoch {
				routeEpoch, rangeStartup = generation, false
				control = autoConnectionControl{reprobes: control.reprobes}
				softCeiling = m.automaticConnectionCeiling(t, workers, cooldownOrigin)
				ceiling = softCeiling
				t.rangeSetupNanos.Store(0)
				t.rangeRouteEpoch.Store(generation)
				q.mu.Lock()
				q.workerRates, q.workerFinished = nil, nil
				q.refusalUntil, q.refusedRanges = time.Time{}, nil
				q.refusalCap = 0
				q.refusalBatches = 0
				q.mu.Unlock()
				invalidate()
				change(autoConnectionInitial(ceiling, total-bytes), false)
				continue
			}
			if hardCeiling != lastHard {
				lastHard, rangeStartup = hardCeiling, false
				invalidate()
				change(min(current, ceiling), false)
				continue
			}
			cooling := cooldownOrigin != "" && m.isOriginCooling(cooldownOrigin)
			newCooling := cooling && !wasCooling
			wasCooling = cooling
			refusal := q.refusalEpoch.Load()
			newRefusal := refusal != lastRefusal
			lastRefusal = refusal
			if newCooling || newRefusal {
				rangeStartup = false
				current = target.Load()
				if !now.Before(pressureUntil) {
					control.backoff(now)
					if newRefusal {
						change(current, false)
					} else {
						change(max(1, current/2), false)
					}
					pressureUntil = now.Add(autoConnectionPressureInterval)
				} else {
					settling = true
				}
				continue
			}
			if cooling {
				settling = true
				continue
			}
			_, contention := m.limiter.contentionState()
			currentRate := float64(0)
			if elapsed > 0 && delta > 0 {
				currentRate = float64(delta) / elapsed
			}
			limited := m.automaticConnectionsLimited(t, currentRate) || contention != lastContention ||
				(q.checkpoints != nil && q.checkpoints.backlogged())
			lastContention = contention
			if limited {
				q.steady.Store(false)
				constrained, settling = true, true
				continue
			}
			if constrained {
				constrained = false
				invalidate()
				continue
			}
			if q.excessActive(current) {
				// Healthy target reductions drain naturally, preserving valid
				// ranges and reusable connections instead of cancelling them.
				settling = true
				continue
			}
			control.drained()
			if !autoConnectionBodiesReady(q, current) {
				settling = true
				continue
			}
			if rangeStartup && t.rangeSupport.Load() == 2 {
				rangeStartup = false
				next := autoConnectionInitial(ceiling, total-bytes)
				if next > current && autoConnectionProbeWorthwhile(total-bytes, minSize, next, 0) {
					invalidate()
					change(next, false)
					continue
				}
			}
			if retried || delta < 0 || elapsed < 0.5 {
				invalidate()
				continue
			}
			if settling {
				settling = false
				continue
			}
			rate := float64(delta) / elapsed
			if autoConnectionTail(total-bytes, total, ceiling) {
				continue
			}
			softCeiling = control.uncap(current, ceiling, hardCeiling, rate, total-bytes)
			ceiling = min(softCeiling, hardCeiling)
			next := control.observe(now, current, ceiling, rate, func(n int32) bool {
				return autoConnectionProbeWorthwhile(total-bytes, minSize, n, rate)
			})
			if next != current {
				change(next, false)
			} else {
				q.steady.Store(control.phase == autoConnectionHoldPhase)
			}
		}
	}
}
