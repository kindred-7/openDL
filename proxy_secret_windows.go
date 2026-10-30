//go:build windows

package main

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

const proxyPasswordStorageAvailable = true

var (
	proxyCrypt32       = syscall.NewLazyDLL("crypt32.dll")
	proxyKernel32      = syscall.NewLazyDLL("kernel32.dll")
	proxyProtectData   = proxyCrypt32.NewProc("CryptProtectData")
	proxyUnprotectData = proxyCrypt32.NewProc("CryptUnprotectData")
	proxyLocalFree     = proxyKernel32.NewProc("LocalFree")
)

type proxyDataBlob struct {
	Size uint32
	Data *byte
}

func encryptProxySecret(data []byte) ([]byte, error) { return proxyDPAPI(data, true) }
func decryptProxySecret(data []byte) ([]byte, error) { return proxyDPAPI(data, false) }

// Use the current user's DPAPI key, with no machine-wide or UI flags. The
// envelope also binds the password to its normalized endpoint and username.
func proxyDPAPI(data []byte, protect bool) ([]byte, error) {
	if len(data) == 0 || len(data) > 128<<10 {
		return nil, fmt.Errorf(tr("凭据长度无效"))
	}
	entropy := []byte("opendl.proxy.credentials.v1")
	input := proxyDataBlob{Size: uint32(len(data)), Data: &data[0]}
	additional := proxyDataBlob{Size: uint32(len(entropy)), Data: &entropy[0]}
	var output proxyDataBlob
	proc := proxyUnprotectData
	if protect {
		proc = proxyProtectData
	}
	const cryptprotectUIForbidden = 1
	result, _, callErr := proc.Call(uintptr(unsafe.Pointer(&input)), 0,
		uintptr(unsafe.Pointer(&additional)), 0, 0, cryptprotectUIForbidden, uintptr(unsafe.Pointer(&output)))
	runtime.KeepAlive(data)
	runtime.KeepAlive(entropy)
	if output.Data != nil {
		defer proxyLocalFree.Call(uintptr(unsafe.Pointer(output.Data)))
	}
	if result == 0 {
		if callErr != syscall.Errno(0) {
			return nil, callErr
		}
		return nil, fmt.Errorf(tr("Windows 凭据保护失败"))
	}
	if output.Data == nil || output.Size == 0 || output.Size > 128<<10 {
		return nil, fmt.Errorf(tr("Windows 凭据数据无效"))
	}
	buffer := unsafe.Slice(output.Data, int(output.Size))
	defer clear(buffer)
	return append([]byte(nil), buffer...), nil
}
