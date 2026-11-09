package api

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"openDL/i18n"
)

// LinkReplay hands "re-request the original link" jobs to the connected
// browser extension and returns what the browser actually sent. The desktop
// refreshes login information this way without persisting credentials:
// results stay in memory and never reach the task journal.
type LinkReplay struct {
	mu       sync.Mutex
	lastSeen time.Time
	jobs     chan ReplayJob
	results  map[string]chan ReplayResult
}

// ReplayJob asks the browser to re-request one link in its login state.
type ReplayJob struct {
	ID      string `json:"id"`
	URL     string `json:"url"`
	PageURL string `json:"pageUrl,omitempty"`
}

// ReplayResult is the request the browser actually sent for the link.
type ReplayResult struct {
	JobID    string            `json:"jobId"`
	OK       bool              `json:"ok"`
	FinalURL string            `json:"finalUrl,omitempty"`
	Status   int               `json:"status,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Reason   string            `json:"reason,omitempty"`
}

const (
	// replayHostLiveness is how long after the last poll a browser host
	// still counts as connected.
	replayHostLiveness = 60 * time.Second
	// replayJobTTL reaps results whose waiter already timed out.
	replayJobTTL = 60 * time.Second
)

// Request queues one replay job and returns its result channel. It fails
// right away when no browser host is connected.
func (l *LinkReplay) Request(url, pageURL string) (<-chan ReplayResult, error) {
	l.mu.Lock()
	l.initLocked()
	if time.Since(l.lastSeen) > replayHostLiveness {
		l.mu.Unlock()
		return nil, i18n.NewError("浏览器扩展未连接，请打开浏览器后重试")
	}
	id := replayJobID()
	ch := make(chan ReplayResult, 1)
	l.results[id] = ch
	jobs := l.jobs
	l.mu.Unlock()
	select {
	case jobs <- ReplayJob{ID: id, URL: url, PageURL: pageURL}:
	default:
		l.mu.Lock()
		delete(l.results, id)
		l.mu.Unlock()
		return nil, i18n.NewError("浏览器重放任务过多，请稍后重试")
	}
	time.AfterFunc(replayJobTTL, func() {
		l.mu.Lock()
		delete(l.results, id)
		l.mu.Unlock()
	})
	return ch, nil
}

// Wait blocks until the next job or the deadline; browser hosts long-poll it.
func (l *LinkReplay) Wait(deadline time.Duration) (ReplayJob, bool) {
	l.mu.Lock()
	l.initLocked()
	l.lastSeen = time.Now()
	jobs := l.jobs
	l.mu.Unlock()
	select {
	case job := <-jobs:
		return job, true
	case <-time.After(deadline):
		return ReplayJob{}, false
	}
}

// Complete delivers a replay result to its waiting caller, if any.
func (l *LinkReplay) Complete(res ReplayResult) {
	l.mu.Lock()
	ch := l.results[res.JobID]
	delete(l.results, res.JobID)
	l.mu.Unlock()
	if ch != nil {
		ch <- res
	}
}

func (l *LinkReplay) initLocked() {
	if l.jobs == nil {
		l.jobs = make(chan ReplayJob, 8)
	}
	if l.results == nil {
		l.results = make(map[string]chan ReplayResult)
	}
}

// replayJobID returns a random identifier binding one job to its result.
func replayJobID() string {
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	return hex.EncodeToString(raw[:])
}
