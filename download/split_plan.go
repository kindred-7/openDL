package download

import (
	"context"
	"math"
	"sync/atomic"
	"time"
)

type rangeRequestTimingKey struct{}

// One request chain owns this observation. Redirects and authentication retries
// replace it with the last transport attempt, excluding gates and retry sleeps.
type rangeRequestTiming struct {
	nanos   atomic.Int64
	task    *Task
	segment *segmentProgress
}

func (r *rangeRequestTiming) elapsed(started time.Time) time.Duration {
	if elapsed := r.nanos.Load(); elapsed > 0 {
		return time.Duration(elapsed)
	}
	return time.Since(started)
}

func observeRangeRequest(ctx context.Context, started time.Time) {
	if timing, ok := ctx.Value(rangeRequestTimingKey{}).(*rangeRequestTiming); ok {
		timing.nanos.Store(max(1, time.Since(started).Nanoseconds()))
	}
}

// Costs belong to this task only. A moving average avoids one slow disk flush
// or authentication exchange dictating every later split.
func observeRangeCost(value *atomic.Int64, elapsed time.Duration) {
	sample := int64(min(max(elapsed, time.Millisecond), time.Minute))
	for {
		old := value.Load()
		next := sample
		if old > 0 {
			next = old + (sample-old)/4
		}
		if value.CompareAndSwap(old, next) {
			return
		}
	}
}

func (t *Task) rangeBodyReady(segment *segmentProgress, elapsed time.Duration) {
	if segment.routeEpoch.Load() == t.rangeRouteEpoch.Load() {
		observeRangeCost(&t.rangeSetupNanos, elapsed)
	}
	segment.bodyStarted.Store(max(1, time.Since(segment.created).Nanoseconds()))
	segment.bodyReady.Store(true)
}

// planRangeSplit balances completion times. Keeping K bytes with the holder
// gives K/vh = setup + (remaining-K)/va; checkpoint overhead must also be
// recovered before it is worth starting a helper. This is a scheduling estimate,
// never permission to skip response validation or shorten an auth-int body.
func planRangeSplit(remaining, minimum, holderRate, helperRate int64, setup, commit time.Duration) (int64, bool) {
	if minimum <= 0 || remaining < 2*minimum {
		return 0, false
	}
	if holderRate <= 0 || helperRate <= 0 {
		// Before rate samples exist, retain the predictable midpoint rule.
		return remaining / 2, true
	}
	owner, helper := float64(holderRate), float64(helperRate)
	delay := max(setup.Seconds(), 0)
	baseline := float64(remaining) / owner
	if baseline <= delay {
		return 0, false
	}
	keep := (float64(remaining) + delay*helper) * owner / (owner + helper)
	keep = math.Max(float64(minimum), math.Min(float64(remaining-minimum), keep))
	finish := math.Max(keep/owner, delay+(float64(remaining)-keep)/helper)
	// Do not trade a new request, file flush and checkpoint for milliseconds.
	if baseline-finish < max(0.25, delay/2)+max(commit.Seconds(), 0) {
		return 0, false
	}
	return int64(keep), true
}

// Bound automatic HTTP claims by a few seconds of useful work. Natural
// retirement cannot respond promptly if a slow worker owns tens of MiB, while
// fast/reused connections still need large claims to amortize request costs.
func automaticClaimLimit(upper, minimum, rate int64, setup time.Duration) int64 {
	if rate <= 0 {
		return max(minimum, min(upper, int64(1<<20)))
	}
	seconds := min(16.0, max(4.0, setup.Seconds()*4))
	bytes := math.Min(float64(upper), float64(rate)*seconds)
	return min(upper, max(minimum, int64(512<<10), int64(bytes)))
}

func automaticSplitMinimum(throughput int64) int64 {
	if throughput < 1<<20 {
		return 512 << 10
	}
	if throughput < 10<<20 {
		return 1 << 20
	}
	return 2 << 20
}
