package download

import (
	"fmt"
	"os"

	"openDL/i18n"
)

const fat32MaxFileSize int64 = 1<<32 - 1

type fileSizeLimitError struct{ limit int64 }

func (e *fileSizeLimitError) Error() string {
	if e.limit == fat32MaxFileSize {
		return i18n.T("保存目录使用 FAT32，单个文件不能达到 4 GiB；请选择 NTFS 或 exFAT 保存目录")
	}
	return fmt.Sprintf(i18n.T("下载文件超过文件系统的单文件大小限制（%d 字节）"), e.limit)
}

// Query the opened file rather than its drive letter, so mounted volumes and
// directory junctions are checked against the actual output filesystem.
func (m *Manager) prepareOutputLimit(t *Task, file *os.File, total int64) error {
	limit := platformOutputLimit(file)
	t.outputLimit.Store(limit)
	if err := checkDeclaredFileSize(limit, total); err != nil {
		return err
	}
	return m.checkAvailableSpace(t, file, total)
}

func checkDeclaredFileSize(limit, total int64) error {
	if limit > 0 && total > limit {
		return &fileSizeLimitError{limit: limit}
	}
	return nil
}

func checkOutputGrowth(t *Task, incoming int) error {
	limit := t.outputLimit.Load()
	if limit > 0 && (int64(incoming) > limit || t.done.Load() > limit-int64(incoming)) {
		return &fileSizeLimitError{limit: limit}
	}
	return nil
}
