package download

import (
	"container/list"
	"sync"
	"time"
)

const paceQuantum = 32 << 10

// paceLimiter shares a token bucket among tasks. A task with pending reads
// gets one byte quantum per turn regardless of how many streams it has.
type paceLimiter struct {
	mu     sync.Mutex
	rate   int64
	tokens float64
	last   time.Time
	epoch  uint64
	timer  *time.Timer
	tasks  list.List // *paceTask, in service order
	byTask map[*Task]*paceTask
}

type paceTask struct {
	task    *Task
	element *list.Element
	reads   list.List // *paceRead, in arrival order
	credit  int
}

type paceRead struct {
	size      int
	canceled  <-chan struct{}
	ready     chan struct{}
	allowance readAllowance
}

type readAllowance struct {
	bytes   int
	epoch   uint64
	limited bool
}

func (p *paceLimiter) burst() int64 { return min(max(p.rate/4, int64(paceQuantum)), int64(256<<10)) }

// replenishLocked is only called with a nonzero rate.
func (p *paceLimiter) replenishLocked(now time.Time) {
	if !p.last.IsZero() {
		p.tokens = min(float64(p.burst()), p.tokens+now.Sub(p.last).Seconds()*float64(p.rate))
	}
	p.last = now
}

func (p *paceLimiter) setRate(rate int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if p.rate > 0 {
		p.replenishLocked(now)
	}
	p.rate = rate
	if rate > 0 {
		p.tokens = min(p.tokens, float64(p.burst()))
	} else {
		p.tokens = 0
	}
	p.last = now
	p.epoch++
	p.dispatchLocked()
}

func (p *paceLimiter) getRate() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.rate
}

func (p *paceLimiter) readSize(size int) int {
	// At low rates a full read buffer could take many seconds to accrue.
	return int(min(int64(size), p.burst(), max(1, p.rate/4)))
}

func (p *paceLimiter) grantLocked(bytes int) readAllowance {
	p.tokens -= float64(bytes)
	return readAllowance{bytes: bytes, epoch: p.epoch, limited: true}
}

func (p *paceLimiter) removeReadLocked(task *paceTask, read *list.Element) {
	task.reads.Remove(read)
	if task.reads.Len() == 0 {
		p.tasks.Remove(task.element)
		delete(p.byTask, task.task)
	}
}

func (p *paceLimiter) dispatchLocked() {
	if p.rate > 0 {
		p.replenishLocked(time.Now())
	}
	for p.tasks.Len() > 0 {
		task := p.tasks.Front().Value.(*paceTask)
		element := task.reads.Front()
		read := element.Value.(*paceRead)
		select {
		case <-read.canceled:
			p.removeReadLocked(task, element)
			close(read.ready)
			continue
		default:
		}
		if p.rate <= 0 {
			read.allowance.bytes = read.size
		} else {
			bytes := p.readSize(read.size)
			if p.tasks.Len() > 1 {
				bytes = min(bytes, task.credit)
			}
			if p.tokens < float64(bytes) {
				delay := time.Duration((float64(bytes) - p.tokens) / float64(p.rate) * float64(time.Second))
				if p.timer == nil {
					p.timer = time.AfterFunc(max(delay, time.Microsecond), p.wake)
				} else {
					p.timer.Reset(max(delay, time.Microsecond))
				}
				return
			}
			read.allowance = p.grantLocked(bytes)
			task.credit -= bytes
		}
		p.removeReadLocked(task, element)
		if task.reads.Len() > 0 && task.credit <= 0 {
			task.credit = paceQuantum
			p.tasks.MoveToBack(task.element)
		}
		close(read.ready)
	}
	if p.timer != nil {
		p.timer.Stop()
	}
}

func (p *paceLimiter) wake() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dispatchLocked()
}

// acquire waits before reading. Configuration changes and refunds dispatch
// only the reads that can proceed under the current limit.
func (p *paceLimiter) acquire(t *Task, size int, canceled <-chan struct{}) (readAllowance, error) {
	select {
	case <-canceled:
		return readAllowance{}, contextCanceled
	default:
	}
	p.mu.Lock()
	if p.rate <= 0 || size == 0 {
		p.mu.Unlock()
		return readAllowance{bytes: size}, nil
	}
	if p.tasks.Len() == 0 {
		p.replenishLocked(time.Now())
		bytes := p.readSize(size)
		if p.tokens >= float64(bytes) {
			allowance := p.grantLocked(bytes)
			p.mu.Unlock()
			return allowance, nil
		}
	}
	if p.byTask == nil {
		p.byTask = make(map[*Task]*paceTask)
	}
	task := p.byTask[t]
	if task == nil {
		task = &paceTask{task: t, credit: paceQuantum}
		task.element = p.tasks.PushBack(task)
		p.byTask[t] = task
	}
	read := &paceRead{size: size, canceled: canceled, ready: make(chan struct{})}
	element := task.reads.PushBack(read)
	p.dispatchLocked()
	p.mu.Unlock()
	select {
	case <-read.ready:
		if read.allowance.bytes == 0 {
			return readAllowance{}, contextCanceled
		}
		return read.allowance, nil
	case <-canceled:
		p.mu.Lock()
		select {
		case <-read.ready:
			p.refundLocked(read.allowance, 0)
		default:
			p.removeReadLocked(task, element)
		}
		p.dispatchLocked()
		p.mu.Unlock()
		return readAllowance{}, contextCanceled
	}
}

func (p *paceLimiter) refundLocked(allowance readAllowance, actual int) {
	if p.rate > 0 && p.epoch == allowance.epoch && allowance.limited && actual < allowance.bytes {
		p.replenishLocked(time.Now())
		p.tokens = min(float64(p.burst()), p.tokens+float64(allowance.bytes-actual))
	}
}

// Never apply an old reservation to a newly configured limit.
func (p *paceLimiter) refund(allowance readAllowance, actual int) {
	if !allowance.limited || actual >= allowance.bytes {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refundLocked(allowance, actual)
	p.dispatchLocked()
}
