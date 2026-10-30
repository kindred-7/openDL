package download

import (
	"fmt"
	"os"

	"openDL/i18n"
	"openDL/signature"
)

// signatureFileDigest binds a digest to a regular file identity. It detects
// replacement or ordinary edits during the read; the caller compares two
// snapshots around the platform trust check.
func signatureFileDigest(path string, canceled <-chan struct{}) (string, os.FileInfo, error) {
	before, err := os.Lstat(extendedPath(path))
	if err != nil {
		return "", nil, err
	}
	if !before.Mode().IsRegular() {
		return "", nil, i18n.NewError("签名验证目标不是普通文件")
	}
	f, err := os.Open(extendedPath(path))
	if err != nil {
		return "", nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return "", nil, err
	}
	if !os.SameFile(before, opened) {
		return "", nil, i18n.NewError("文件在签名验证期间发生变化")
	}
	digest, _, err := digestFile(f, canceled)
	if err != nil {
		return "", nil, err
	}
	after, err := os.Lstat(extendedPath(path))
	if err != nil {
		return "", nil, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(opened, after) || opened.Size() != after.Size() || !opened.ModTime().Equal(after.ModTime()) {
		return "", nil, i18n.NewError("文件在签名验证期间发生变化")
	}
	return digest, after, nil
}

// VerifySignature checks an already completed file without changing its queue
// state or opening it. Run on a worker; trust/revocation checks may perform I/O.
func (m *Manager) VerifySignature(id string) error {
	m.mu.Lock()
	if m.updateStarting {
		m.mu.Unlock()
		return ErrUpdateInProgress
	}
	var task *Task
	for _, candidate := range m.tasks {
		if candidate.id == id {
			task = candidate
			break
		}
	}
	if task == nil {
		m.mu.Unlock()
		return i18n.NewError("任务不存在")
	}
	if task.state.Load().(State) != Done || task.editing || !task.verifying.CompareAndSwap(false, true) {
		m.mu.Unlock()
		return i18n.NewError("请等待文件下载完成后再验证数字签名")
	}
	m.mu.Unlock()
	defer func() { task.verifying.Store(false); m.notify() }()
	previous := task.signatureVerified.Swap(false)
	m.notify()
	// Persist invalidation before the trust check so interruption cannot
	// resurrect a previous successful result as the result of this attempt.
	if err := m.saveTask(task); err != nil {
		task.signatureVerified.Store(previous)
		return fmt.Errorf(i18n.T("无法保存签名验证状态：%w"), err)
	}
	task.metaMu.Lock()
	path := task.filePath
	task.metaMu.Unlock()
	beforeHash, before, err := signatureFileDigest(path, task.cancel)
	if err == nil {
		err = signature.VerifyFile(extendedPath(path))
	}
	if err == nil {
		var after os.FileInfo
		var afterHash string
		afterHash, after, err = signatureFileDigest(path, task.cancel)
		if err == nil && (beforeHash != afterHash || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime())) {
			err = i18n.NewError("文件在签名验证期间发生变化")
		}
	}
	if err == nil {
		task.signatureVerified.Store(true)
	}
	if err == nil {
		if saveErr := m.saveTask(task); saveErr != nil {
			task.signatureVerified.Store(false)
			return fmt.Errorf(i18n.T("无法保存签名验证状态：%w"), saveErr)
		}
	}
	return err
}
