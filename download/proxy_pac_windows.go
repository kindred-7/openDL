//go:build windows

package download

import (
	"syscall"
	"unsafe"
)

// platformPACAvailable 报告当前平台是否支持 PAC 求值。
func platformPACAvailable() bool { return true }

// platformPACProxy 用系统 WinHTTP 求值 PAC 自动配置；返回代理声明，直连时
// 返回空声明。
func platformPACProxy(pacURL, target string) (string, error) {
	winhttp := syscall.NewLazyDLL("winhttp.dll")
	winHttpOpen := winhttp.NewProc("WinHttpOpen")
	winHttpGetProxyForUrl := winhttp.NewProc("WinHttpGetProxyForUrl")
	winHttpCloseHandle := winhttp.NewProc("WinHttpCloseHandle")
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	globalFree := kernel32.NewProc("GlobalFree")

	agent, err := syscall.UTF16PtrFromString("OpenDL")
	if err != nil {
		return "", err
	}
	session, _, _ := winHttpOpen.Call(uintptr(unsafe.Pointer(agent)), 1, 0, 0, 0)
	if session == 0 {
		return "", errPACUnavailable
	}
	defer winHttpCloseHandle.Call(session)

	configURL, err := syscall.UTF16PtrFromString(pacURL)
	if err != nil {
		return "", err
	}
	address, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return "", err
	}
	options := winHTTPAutoProxyOptions{
		flags:       0x00000002, // WINHTTP_AUTOPROXY_CONFIG_URL
		autoConfig:  configURL,
		detectFlags: 0x00000003, // DHCP 与 DNS 自动探测
		autoLogon:   1,
	}
	info := winHTTPProxyInfo{}
	ok, _, _ := winHttpGetProxyForUrl.Call(session, uintptr(unsafe.Pointer(address)), uintptr(unsafe.Pointer(&options)), uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		return "", errPACUnavailable
	}
	if info.proxy != nil {
		defer globalFree.Call(uintptr(unsafe.Pointer(info.proxy)))
	}
	if info.bypass != nil {
		defer globalFree.Call(uintptr(unsafe.Pointer(info.bypass)))
	}
	if info.proxy == nil {
		return "", nil
	}
	return syscall.UTF16ToString((*[1 << 12]uint16)(unsafe.Pointer(info.proxy))[:]), nil
}

type winHTTPAutoProxyOptions struct {
	flags       uint32
	autoConfig  *uint16
	detectFlags uint32
	proxy       *uint16
	proxyBypass *uint16
	autoLogon   uint32
}

type winHTTPProxyInfo struct {
	accessType uint32
	proxy      *uint16
	bypass     *uint16
}
