package download

// 流量配额：按日/周/月统计经本应用下载的正文流量，用完后暂停新的读取，
// 到期自动恢复。用量持久化到任务目录；崩溃时最多丢失一段统计。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"openDL/i18n"
)

// quotaSaveStep 是用量落盘的最小增量，避免每次读都写文件。
const quotaSaveStep = 16 << 20

type quotaRecord struct {
	Period string    `json:"period"`
	Window time.Time `json:"window"`
	Used   int64     `json:"used"`
}

type trafficQuota struct {
	mu      sync.Mutex
	period  string
	limit   int64
	window  time.Time
	used    int64
	saved   int64
	path    string
	warned  bool
	changed chan struct{}
	notify  func()
}

// quotaWindowStart 返回周期的本地起点：每日零点、每周一零点、每月一日。
func quotaWindowStart(now time.Time, period string) time.Time {
	local := now.Local()
	y, m, d := local.Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, local.Location())
	switch period {
	case "week":
		return day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
	case "month":
		return time.Date(y, m, 1, 0, 0, 0, 0, local.Location())
	default:
		return day
	}
}

// quotaWindowEnd 返回周期终点；跨夏令时以日历天推进。
func quotaWindowEnd(window time.Time, period string) time.Time {
	switch period {
	case "week":
		return window.AddDate(0, 0, 7)
	case "month":
		return window.AddDate(0, 1, 0)
	default:
		return window.AddDate(0, 0, 1)
	}
}

// attach 挂载持久化文件与状态回调；用量在 set 时按周期对齐后恢复。
func (q *trafficQuota) attach(path string, notify func()) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.path = path
	q.notify = notify
}

// set 配置周期与额度（字节）；周期为空表示不限制。换周期重新开始统计，
// 同周期调整额度保留已用流量。
func (q *trafficQuota) set(period string, limit int64) error {
	switch period {
	case "", "day", "week", "month":
	default:
		return fmt.Errorf(i18n.T("流量配额周期无效"))
	}
	if period != "" && limit <= 0 {
		return fmt.Errorf(i18n.T("流量配额额度无效"))
	}
	q.mu.Lock()
	notify := false
	if q.period != period {
		q.period = period
		q.window = quotaWindowStart(time.Now(), period)
		q.used, q.saved, q.warned = 0, 0, false
		if record, ok := q.loadLocked(); ok && record.Period == period && record.Window.Equal(q.window) {
			q.used, q.saved = record.Used, record.Used
		}
		notify = true
	}
	q.limit = limit
	q.saveLocked()
	q.wakeLocked()
	fn := q.notify
	q.mu.Unlock()
	if notify && fn != nil {
		fn()
	}
	return nil
}

// add 统计读到的正文流量。
func (q *trafficQuota) add(n int64) {
	if n <= 0 {
		return
	}
	q.mu.Lock()
	if q.period == "" {
		q.mu.Unlock()
		return
	}
	notify := q.rollLocked(time.Now())
	q.used += n
	if q.used-q.saved >= quotaSaveStep {
		q.saved = q.used
		q.saveLocked()
	}
	if q.limit > 0 && !q.warned && q.used >= q.limit {
		q.warned = true
		notify = true
	}
	fn := q.notify
	q.mu.Unlock()
	if notify && fn != nil {
		fn()
	}
}

// wait 在配额用完后阻塞到下个周期；取消立即返回。
func (q *trafficQuota) wait(canceled <-chan struct{}) error {
	for {
		select {
		case <-canceled:
			return contextCanceled
		default:
		}
		q.mu.Lock()
		q.rollLocked(time.Now())
		if q.period == "" || q.limit <= 0 || q.used < q.limit {
			q.mu.Unlock()
			return nil
		}
		if q.changed == nil {
			q.changed = make(chan struct{})
		}
		changed := q.changed
		end := quotaWindowEnd(q.window, q.period)
		q.mu.Unlock()
		delay := time.Until(end)
		if delay < time.Millisecond {
			delay = time.Millisecond
		}
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-changed:
		case <-canceled:
			timer.Stop()
			return contextCanceled
		}
		timer.Stop()
	}
}

// exhausted 报告当前是否已用完配额。
func (q *trafficQuota) exhausted() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.period == "" || q.limit <= 0 {
		return false
	}
	q.rollLocked(time.Now())
	return q.used >= q.limit
}

// rollLocked 进入新周期时清零用量并唤醒等待；调用方负责随后通知。
func (q *trafficQuota) rollLocked(now time.Time) bool {
	if q.period == "" || (!q.window.IsZero() && now.Before(quotaWindowEnd(q.window, q.period))) {
		return false
	}
	q.window = quotaWindowStart(now, q.period)
	q.used, q.saved, q.warned = 0, 0, false
	q.saveLocked()
	q.wakeLocked()
	return true
}

func (q *trafficQuota) wakeLocked() {
	if q.changed != nil {
		close(q.changed)
		q.changed = nil
	}
}

func (q *trafficQuota) loadLocked() (quotaRecord, bool) {
	var record quotaRecord
	if q.path == "" {
		return record, false
	}
	data, err := os.ReadFile(q.path)
	if err != nil || json.Unmarshal(data, &record) != nil {
		return quotaRecord{}, false
	}
	return record, true
}

func (q *trafficQuota) saveLocked() {
	if q.path == "" {
		return
	}
	data, err := json.Marshal(quotaRecord{Period: q.period, Window: q.window, Used: q.used})
	if err != nil {
		return
	}
	temp, err := os.CreateTemp(filepath.Dir(q.path), ".quota-*")
	if err != nil {
		return
	}
	name := temp.Name()
	if _, err := temp.Write(data); err == nil {
		temp.Close()
		_ = os.Rename(name, q.path)
	} else {
		temp.Close()
		_ = os.Remove(name)
	}
}
