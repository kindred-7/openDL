//go:build windows

package signature

import (
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"openDL/i18n"
)

const Available = true

var trustLoad sync.Once
var trustProc *syscall.LazyProc
var trustLoadErr error

func trustProvider() (*syscall.LazyProc, error) {
	trustLoad.Do(func() {
		var directory [512]uint16
		getDirectory := syscall.NewLazyDLL("kernel32.dll").NewProc("GetSystemDirectoryW")
		n, _, err := getDirectory.Call(uintptr(unsafe.Pointer(&directory[0])), uintptr(len(directory)))
		if n == 0 {
			trustLoadErr = err
			return
		}
		if n >= uintptr(len(directory)) {
			trustLoadErr = syscall.Errno(122)
			return
		}
		// Resolve the provider in System32, not the executable/current directory.
		trustProc = syscall.NewLazyDLL(filepath.Join(syscall.UTF16ToString(directory[:]), "wintrust.dll")).NewProc("WinVerifyTrust")
		trustLoadErr = trustProc.Find()
	})
	return trustProc, trustLoadErr
}

type trustGUID struct {
	Data1        uint32
	Data2, Data3 uint16
	Data4        [8]byte
}

type trustFile struct {
	Size    uint32
	Path    *uint16
	Handle  uintptr
	Subject *trustGUID
}

// Layout follows WINTRUST_DATA on both 32-bit and 64-bit Windows.
type trustData struct {
	Size                                    uint32
	PolicyCallback, SIPClient               uintptr
	UIChoice, RevocationChecks, UnionChoice uint32
	File                                    *trustFile
	StateAction                             uint32
	State                                   uintptr
	URLReference                            *uint16
	ProviderFlags, UIContext                uint32
	SignatureSettings                       uintptr
}

// VerifyFile uses the Windows Authenticode policy, including certificate chain,
// timestamp and revocation validation. Only a zero WinVerifyTrust result passes.
// The read handle denies writes/deletes while the provider checks the file.
func VerifyFile(path string) error {
	winVerifyTrust, err := trustProvider()
	if err != nil {
		return err
	}
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ, syscall.FILE_SHARE_READ, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(handle)
	file := trustFile{Path: name, Handle: uintptr(handle)}
	file.Size = uint32(unsafe.Sizeof(file))
	data := trustData{UIChoice: 2, RevocationChecks: 1, UnionChoice: 1, File: &file, StateAction: 1, ProviderFlags: 0x80 | 0x2000}
	data.Size = uint32(unsafe.Sizeof(data))
	action := trustGUID{0x00aac56b, 0xcd44, 0x11d0, [8]byte{0x8c, 0xc2, 0x00, 0xc0, 0x4f, 0xc2, 0x95, 0xee}}
	status, _, _ := winVerifyTrust.Call(^uintptr(0), uintptr(unsafe.Pointer(&action)), uintptr(unsafe.Pointer(&data)))
	data.StateAction = 2 // WTD_STATEACTION_CLOSE releases provider state on failure too.
	winVerifyTrust.Call(^uintptr(0), uintptr(unsafe.Pointer(&action)), uintptr(unsafe.Pointer(&data)))
	runtime.KeepAlive(name)
	runtime.KeepAlive(file)
	code := uint32(status)
	if code == 0 {
		return nil
	}
	switch code {
	case 0x800b0100:
		return fmt.Errorf(i18n.T("文件没有可验证的数字签名"))
	case 0x80096010:
		return fmt.Errorf(i18n.T("文件签名摘要不匹配，文件可能已被修改"))
	case 0x800b010c:
		return fmt.Errorf(i18n.T("文件签名证书已被吊销"))
	default:
		return fmt.Errorf(i18n.T("文件签名未通过 Windows 信任验证（0x%08X）"), code)
	}
}
