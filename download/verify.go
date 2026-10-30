package download

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"openDL/i18n"
	"openDL/signature"
)

var errPublicationChanged = i18n.NewError("已下载文件与发布记录不一致")

// NormalizeSHA256 accepts an optional hexadecimal publisher checksum.
func NormalizeSHA256(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", nil
	}
	if len(value) != 64 {
		return "", fmt.Errorf(i18n.T("SHA-256 必须是 64 位十六进制字符"))
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", fmt.Errorf(i18n.T("SHA-256 必须是 64 位十六进制字符"))
	}
	return value, nil
}

// NormalizeSignatureKey 校验签名公钥：Base64 的 32 字节 Ed25519 公钥，或
// OpenPGP 公钥（装甲或二进制，RSA/ECDSA/EdDSA）；空值表示不校验。
func NormalizeSignatureKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if len(value) > 64<<10 {
		return "", fmt.Errorf(i18n.T("公钥必须是 Ed25519、OpenPGP 或 Minisign 公钥"))
	}
	if raw, err := base64.StdEncoding.DecodeString(value); err == nil && len(raw) == ed25519.PublicKeySize {
		return base64.StdEncoding.EncodeToString(raw), nil
	}
	if signature.CheckMinisignKey(value) == nil {
		return value, nil
	}
	if err := signature.CheckOpenPGPKey([]byte(value)); err != nil {
		return "", fmt.Errorf(i18n.T("公钥必须是 Ed25519、OpenPGP 或 Minisign 公钥"))
	}
	return value, nil
}

// NormalizeDetachedSignature 校验分离签名：Base64 的 64 字节 Ed25519 签名
// （覆盖文件的 SHA-256 摘要字节），或 OpenPGP 分离签名；空值表示不校验。
func NormalizeDetachedSignature(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if len(value) > 64<<10 {
		return "", fmt.Errorf(i18n.T("签名必须是 Ed25519、OpenPGP 或 Minisign 分离签名"))
	}
	if raw, err := base64.StdEncoding.DecodeString(value); err == nil && len(raw) == ed25519.SignatureSize {
		return base64.StdEncoding.EncodeToString(raw), nil
	}
	if signature.CheckMinisignSignature(value) == nil {
		return value, nil
	}
	if err := signature.CheckOpenPGPSignature([]byte(value)); err != nil {
		return "", fmt.Errorf(i18n.T("签名必须是 Ed25519、OpenPGP 或 Minisign 分离签名"))
	}
	return value, nil
}

type cancelReaderAt struct {
	reader   io.ReaderAt
	canceled <-chan struct{}
}

func (r cancelReaderAt) ReadAt(p []byte, offset int64) (int, error) {
	select {
	case <-r.canceled:
		return 0, contextCanceled
	default:
		return r.reader.ReadAt(p, offset)
	}
}

// verifyDetachedSignature 校验原始 Ed25519、OpenPGP 或 Minisign 签名。
func verifyDetachedSignature(publicKey, detached, digestHex string, content io.ReaderAt, size int64) error {
	if len(publicKey) > 64<<10 || len(detached) > 64<<10 {
		return i18n.NewError("分离签名或公钥超过大小限制")
	}
	key, keyErr := base64.StdEncoding.DecodeString(publicKey)
	detachedBytes, detachedErr := base64.StdEncoding.DecodeString(detached)
	if keyErr == nil && len(key) == ed25519.PublicKeySize && detachedErr == nil && len(detachedBytes) == ed25519.SignatureSize {
		digest, err := hex.DecodeString(digestHex)
		if err != nil {
			return fmt.Errorf(i18n.T("SHA-256 必须是 64 位十六进制字符"))
		}
		if !ed25519.Verify(ed25519.PublicKey(key), digest, detachedBytes) {
			return i18n.NewError("Ed25519 签名校验失败")
		}
		return nil
	}
	if content == nil || size < 0 {
		return fmt.Errorf(i18n.T("公钥必须是 Ed25519、OpenPGP 或 Minisign 公钥"))
	}
	if signature.CheckMinisignKey(publicKey) == nil || signature.CheckMinisignSignature(detached) == nil {
		return signature.VerifyMinisign(publicKey, detached, io.NewSectionReader(content, 0, size))
	}
	return signature.VerifyOpenPGP([]byte(publicKey), []byte(detached), io.NewSectionReader(content, 0, size))
}

// finishFile verifies before publishing, so a mismatch leaves the partial
// file in place and cannot trigger completion actions or replace a file.
func (m *Manager) finishFile(t *Task, part string) error {
	return m.finishFileWithPrior(t, part, "", 0)
}

// priorHash and priorSize identify the complete file saved before a failed
// publication. A changed partial must never be published as that file.
func (m *Manager) finishFileWithPrior(t *Task, part, priorHash string, priorSize int64) error {
	t.metaMu.Lock()
	expected := t.expectedSHA256
	requireSignature := t.requireSignature
	signatureText, signatureKey := t.signature, t.signatureKey
	syncMinutes, original := t.syncMinutes, t.syncOriginalSHA256
	t.verifiedSHA256 = ""
	t.metaMu.Unlock()
	t.signatureVerified.Store(false)
	expected, err := NormalizeSHA256(expected)
	if err != nil {
		return err
	}
	var signedDigest string
	if expected != "" || m.storeDir != "" || priorHash != "" || syncMinutes > 0 || requireSignature || signatureText != "" {
		t.verifying.Store(true)
		m.notify()
		defer func() { t.verifying.Store(false); m.notify() }()
		f, err := os.OpenFile(extendedPath(part), os.O_RDWR, 0)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := f.Sync(); err != nil {
			return fmt.Errorf(i18n.T("无法同步下载文件：%w"), err)
		}
		var signedFile os.FileInfo
		if signatureText != "" {
			signedFile, err = f.Stat()
			if err != nil {
				return err
			}
			pathInfo, statErr := os.Lstat(extendedPath(part))
			if statErr != nil {
				return statErr
			}
			if !pathInfo.Mode().IsRegular() || !os.SameFile(signedFile, pathInfo) {
				return i18n.NewError("文件在签名验证期间发生变化")
			}
		}
		actual, size, err := digestFile(f, t.cancel)
		if err != nil {
			return err
		}
		signedDigest = actual
		if priorHash != "" && (actual != priorHash || size != priorSize) {
			return errPublicationChanged
		}
		if expected != "" && actual != expected {
			t.metaMu.Lock()
			t.failedSHA256 = expected
			t.metaMu.Unlock()
			if err := m.saveTask(t); err != nil {
				return fmt.Errorf(i18n.T("SHA-256 校验失败：文件摘要 %s，与预期不一致；无法保存校验状态：%v"), actual, err)
			}
			return fmt.Errorf(i18n.T("SHA-256 校验失败：文件摘要 %s，与预期不一致"), actual)
		}
		if signatureText != "" {
			if err := verifyDetachedSignature(signatureKey, signatureText, actual, cancelReaderAt{f, t.cancel}, size); err != nil {
				return err
			}
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				return err
			}
			afterHash, afterSize, err := digestFile(f, t.cancel)
			if err != nil {
				return err
			}
			pathInfo, err := os.Lstat(extendedPath(part))
			if err != nil {
				return err
			}
			if !pathInfo.Mode().IsRegular() || !os.SameFile(signedFile, pathInfo) || signedFile.Size() != pathInfo.Size() || !signedFile.ModTime().Equal(pathInfo.ModTime()) || actual != afterHash || size != afterSize {
				return i18n.NewError("文件在签名验证期间发生变化")
			}
		}
		// Windows requires the file to be closed before publishing.
		if err := f.Close(); err != nil {
			return err
		}
		t.metaMu.Lock()
		t.failedSHA256 = ""
		if expected != "" {
			t.verifiedSHA256 = actual
		}
		if m.storeDir != "" || syncMinutes > 0 {
			t.publishSHA256, t.publishSize = actual, size
		}
		t.metaMu.Unlock()
		if err := m.saveTask(t); err != nil {
			return fmt.Errorf(i18n.T("无法保存文件发布记录：%w"), err)
		}
	}
	if requireSignature {
		t.verifying.Store(true)
		m.notify()
		defer func() { t.verifying.Store(false); m.notify() }()
		if err := signature.VerifyFile(extendedPath(part)); err != nil {
			return err
		}
		afterHash, _, err := signatureFileDigest(part, t.cancel)
		if err != nil {
			return err
		}
		if afterHash != signedDigest {
			return i18n.NewError("文件在签名验证期间发生变化")
		}
		t.signatureVerified.Store(true)
		if err := m.saveTask(t); err != nil {
			return err
		}
	}
	select {
	case <-t.cancel:
		return contextCanceled
	default:
	}
	destination := strings.TrimSuffix(part, ".part")
	for attempt := 0; ; attempt++ {
		var err error
		if original != "" {
			if err = matchesOriginal(destination, original, t.cancel); err != nil {
				return err
			}
			err = replaceSyncedFile(part, destination)
		} else {
			err = publishFile(part, destination)
		}
		if err == nil {
			t.metaMu.Lock()
			t.syncOriginalSHA256 = ""
			t.metaMu.Unlock()
			return nil
		}
		if !retryablePublicationError(err) || attempt >= 3 {
			return err
		}
		// A target created after the original name was claimed must never be
		// overwritten, and waiting cannot make that collision safe.
		if _, statErr := os.Lstat(destination); original == "" && (statErr == nil || !os.IsNotExist(statErr)) {
			return err
		}
		if err := waitRetry(t.cancel, time.Duration(250*(1<<attempt))*time.Millisecond); err != nil {
			return err
		}
	}
}

// hasPublicationIntent only checks the recorded metadata; the file itself is
// checked on the worker before it can be published.
func (t *Task) hasPublicationIntent() bool {
	t.metaMu.Lock()
	defer t.metaMu.Unlock()
	expected, err := NormalizeSHA256(t.expectedSHA256)
	return err == nil && t.filePath != "" && t.publishSize >= 0 && validFingerprint(t.publishSHA256) && (expected == "" || expected == t.publishSHA256)
}

// resumePublication avoids another network request after a complete file was
// already synced and recorded but its final move failed or was interrupted.
func (m *Manager) resumePublication(t *Task) (bool, error) {
	t.metaMu.Lock()
	path, hash, size, expected := t.filePath, t.publishSHA256, t.publishSize, t.expectedSHA256
	t.metaMu.Unlock()
	expected, err := NormalizeSHA256(expected)
	if err != nil || path == "" || size < 0 || !validFingerprint(hash) || (expected != "" && expected != hash) {
		return false, nil
	}
	info, err := os.Lstat(path + ".part")
	if err != nil {
		return true, fmt.Errorf(i18n.T("无法读取已下载文件：%w"), err)
	}
	if !info.Mode().IsRegular() {
		return true, fmt.Errorf(i18n.T("已下载文件不是普通文件"))
	}
	err = m.finishFileWithPrior(t, path+".part", hash, size)
	if errors.Is(err, errPublicationChanged) {
		// Clear the intent before fetching a new representation so a crash
		// cannot later mistake the modified bytes for the completed file.
		t.metaMu.Lock()
		t.publishSHA256, t.publishSize = "", 0
		t.metaMu.Unlock()
		if err := m.saveTask(t); err != nil {
			return true, fmt.Errorf(i18n.T("无法清除过期的文件发布记录：%w"), err)
		}
		return false, nil
	}
	if err == nil {
		t.total.Store(size)
		t.done.Store(size)
		cleanupDualMedia(t)
	}
	return true, err
}

func digestFile(f *os.File, canceled <-chan struct{}) (string, int64, error) {
	h := sha256.New()
	buf := make([]byte, 128<<10)
	var size int64
	for {
		select {
		case <-canceled:
			return "", size, contextCanceled
		default:
		}
		n, readErr := f.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n])
			size += int64(n)
		}
		if readErr == io.EOF {
			return hex.EncodeToString(h.Sum(nil)), size, nil
		}
		if readErr != nil {
			return "", size, fmt.Errorf(i18n.T("SHA-256 校验读取失败：%w"), readErr)
		}
	}
}

// A final path is only a candidate if the partial path is absent or is the
// same hard link. This prevents treating an unrelated collision as completion.
func publicationCandidate(t *Task) bool {
	if t.filePath == "" || t.publishSize < 0 {
		return false
	}
	normalized, err := NormalizeSHA256(t.publishSHA256)
	if err != nil || normalized == "" || normalized != t.publishSHA256 {
		return false
	}
	final, err := os.Lstat(t.filePath)
	if err != nil || !final.Mode().IsRegular() {
		return false
	}
	part, err := os.Lstat(t.filePath + ".part")
	return os.IsNotExist(err) || (err == nil && part.Mode().IsRegular() && os.SameFile(final, part))
}

func (m *Manager) recoverPublished(t *Task) {
	t.metaMu.Lock()
	path, expectedHash, expectedSize := t.filePath, t.publishSHA256, t.publishSize
	t.metaMu.Unlock()
	f, err := os.Open(path)
	var actual string
	var size int64
	var checked os.FileInfo
	if err == nil {
		var opened os.FileInfo
		opened, err = f.Stat()
		if err == nil {
			actual, size, err = digestFile(f, t.cancel)
		}
		_ = f.Close()
		if err == nil {
			var current os.FileInfo
			current, err = os.Lstat(path)
			if err == nil && (!current.Mode().IsRegular() || !os.SameFile(opened, current) || !publicationCandidate(t)) {
				err = fmt.Errorf(i18n.T("文件在检查期间发生变化"))
			}
			if err == nil {
				checked = current
			}
		}
	}
	if err == nil && (actual != expectedHash || size != expectedSize) {
		err = fmt.Errorf(i18n.T("已发布文件与记录不一致"))
	}
	if err == nil && t.requireSignature {
		err = signature.VerifyFile(extendedPath(path))
		if err == nil {
			var after os.FileInfo
			var afterHash string
			afterHash, after, err = signatureFileDigest(path, t.cancel)
			if err == nil && (afterHash != expectedHash || !os.SameFile(checked, after) || checked.Size() != after.Size() || !checked.ModTime().Equal(after.ModTime()) || !publicationCandidate(t)) {
				err = i18n.NewError("文件在签名验证期间发生变化")
			}
			if err == nil {
				t.signatureVerified.Store(true)
			}
		}
	}
	m.mu.Lock()
	if t.removed.Load() {
		m.mu.Unlock()
		return
	}
	if err == nil {
		t.metaMu.Lock()
		t.syncOriginalSHA256 = ""
		t.metaMu.Unlock()
		t.night.Store(false)
		t.held.Store(false)
		t.total.Store(size)
		t.done.Store(size)
		t.setFinished(time.Now())
		t.folderOpened.Store(true)
		t.errMu.Lock()
		t.err = ""
		t.errMu.Unlock()
		t.state.Store(Done)
	} else {
		t.setErr(fmt.Errorf(i18n.T("恢复完成状态失败：%w"), err))
		t.state.Store(ErrorS)
	}
	t.verifying.Store(false)
	m.mu.Unlock()
	if err == nil {
		cleanupDualMedia(t)
	}
	m.saveTaskLog(t)
	m.notify()
}
