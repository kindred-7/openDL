package download

// queueTicket represents one attempt, not one task: a resumed task joins at
// the end of the waiting queue. All fields except the channels require m.mu.
type queueTicket struct {
	ready    chan struct{}
	cancel   <-chan struct{}
	queue    string // 队列标识；空为主队列
	task     *Task  // 归属任务，支持跨队列迁移
	granted  bool
	released bool
}

// enqueueLocked establishes ordering while the caller still holds m.mu.
func (m *Manager) enqueueLocked(t *Task) *queueTicket {
	ticket := &queueTicket{ready: make(chan struct{}), cancel: t.cancel, queue: t.queue, task: t}
	t.ticket = ticket
	m.waiting = append(m.waiting, ticket)
	m.dispatchLocked()
	return ticket
}

// queueLimitLocked 返回队列并发上限；0 表示只受全局上限约束。主队列以空
// 标识存储。
func (m *Manager) queueLimitLocked(queue string) int {
	return m.queueLimits[queue]
}

// dispatchLocked 按等待顺序发放名额；队列达到自身并发上限时跳过并继续
// 检查后面的等待者，避免一个满载队列阻塞其他队列。
func (m *Manager) dispatchLocked() {
	for m.active < m.concurrency && len(m.waiting) > 0 {
		next := -1
		for i, ticket := range m.waiting {
			select {
			case <-ticket.cancel:
				continue
			default:
			}
			if limit := m.queueLimitLocked(ticket.queue); limit > 0 && m.queueActive[ticket.queue] >= limit {
				continue
			}
			next = i
			break
		}
		if next < 0 {
			return
		}
		ticket := m.waiting[next]
		copy(m.waiting[next:], m.waiting[next+1:])
		m.waiting[len(m.waiting)-1] = nil
		m.waiting = m.waiting[:len(m.waiting)-1]
		ticket.granted = true
		m.active++
		m.queueActive[ticket.queue]++
		close(ticket.ready)
	}
}

// releaseTicket removes an abandoned waiter or frees an allocated slot.
// It also advances the queue when a waiter fails before starting.
func (m *Manager) releaseTicket(ticket *queueTicket) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ticket.released {
		return
	}
	ticket.released = true
	if ticket.task != nil && ticket.task.ticket == ticket {
		ticket.task.ticket = nil
	}
	if ticket.granted {
		m.active--
		m.queueActive[ticket.queue]--
	} else {
		for i, queued := range m.waiting {
			if queued == ticket {
				copy(m.waiting[i:], m.waiting[i+1:])
				m.waiting[len(m.waiting)-1] = nil
				m.waiting = m.waiting[:len(m.waiting)-1]
				break
			}
		}
	}
	m.dispatchLocked()
}
