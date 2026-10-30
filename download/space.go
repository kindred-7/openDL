package download

import (
	"fmt"
	"os"
	"path/filepath"

	"openDL/i18n"
)

type insufficientSpaceError struct{ needed, available int64 }

func (e *insufficientSpaceError) Error() string {
	return fmt.Sprintf(i18n.T("保存目录可用空间不足（还需 %s，可用 %s），请释放空间或选择其他目录"), storageAmount(e.needed), storageAmount(e.available))
}

func storageAmount(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	value := float64(bytes) / 1024
	for i, unit := range units {
		if value < 1024 || i == len(units)-1 {
			return fmt.Sprintf("%.1f %s", value, unit)
		}
		value /= 1024
	}
	return ""
}

// spaceReservation 是一个任务在其保存目录上的预计剩余占用。
type spaceReservation struct {
	dir  string
	need int64
}

// reserveSpace 登记任务的预计剩余占用，供并行任务的空间预检互相扣减。
func (m *Manager) reserveSpace(dir, id string, need int64) {
	m.spaceMu.Lock()
	defer m.spaceMu.Unlock()
	if m.spaceReservations == nil {
		m.spaceReservations = map[string]spaceReservation{}
	}
	m.spaceReservations[id] = spaceReservation{dir: dir, need: need}
}

// releaseSpace 释放任务的预计剩余占用。
func (m *Manager) releaseSpace(id string) {
	m.spaceMu.Lock()
	defer m.spaceMu.Unlock()
	delete(m.spaceReservations, id)
}

// reservedByOthers 返回同目录下其他任务占用的预计剩余空间。
func (m *Manager) reservedByOthers(dir, id string) int64 {
	m.spaceMu.Lock()
	defer m.spaceMu.Unlock()
	var total int64
	for other, reservation := range m.spaceReservations {
		if other != id && reservation.dir == dir {
			total += reservation.need
		}
	}
	return total
}

// checkAvailableSpace 按已知大小预检空间；同目录的并行任务按预计剩余占用
// 互相扣减，任务通过预检后登记自己的预计剩余占用。
func (m *Manager) checkAvailableSpace(t *Task, file *os.File, total int64) error {
	if total < 0 {
		return nil
	}
	free, allocated, ok := platformAvailableSpace(file)
	if !ok {
		return nil
	}
	dir := filepath.Dir(file.Name())
	if reserved := m.reservedByOthers(dir, t.id); reserved > 0 {
		free -= reserved
		if free < 0 {
			free = 0
		}
	}
	if err := checkSpaceRequirement(total, allocated, free); err != nil {
		return err
	}
	if needed := total - allocated; needed > 0 {
		m.reserveSpace(dir, t.id, needed)
	}
	return nil
}

// Physical allocation, rather than the logical file length, determines how
// much extra space is needed. Unknown allocation is not guessed from EOF.
func checkSpaceRequirement(total, allocated, free int64) error {
	if total < 0 || allocated < 0 || free < 0 || total <= allocated {
		return nil
	}
	needed := total - allocated
	if needed > free {
		return &insufficientSpaceError{needed: needed, available: free}
	}
	return nil
}

func describeStorageError(err error) error {
	if platformDiskFull(err) {
		return fmt.Errorf(i18n.T("磁盘可用空间不足，请释放空间后继续：%w"), err)
	}
	return err
}
