//go:build windows

package download

import (
	"encoding/binary"
	"strings"
	"syscall"
	"unsafe"
)

// systemProxySettings 是 Windows 用户级 Internet Settings 的代理声明。
type systemProxySettings struct {
	enabled       bool
	server        string
	autoConfigURL string
	override      string
	available     bool
}

// platformSystemProxy 读取 HKCU 的 Internet Settings；读不到时说明原因。
func platformSystemProxy() (systemProxySettings, string) {
	const hkeyCurrentUser = 0x80000001
	const keyRead = 0x20019
	advapi32 := syscall.NewLazyDLL("advapi32.dll")
	regOpenKeyEx := advapi32.NewProc("RegOpenKeyExW")
	regQueryValueEx := advapi32.NewProc("RegQueryValueExW")
	regCloseKey := advapi32.NewProc("RegCloseKey")
	subkey, err := syscall.UTF16PtrFromString("Software\\Microsoft\\Windows\\CurrentVersion\\Internet Settings")
	if err != nil {
		return systemProxySettings{}, "无法读取系统代理设置"
	}
	var hkey uintptr
	status, _, _ := regOpenKeyEx.Call(hkeyCurrentUser, uintptr(unsafe.Pointer(subkey)), 0, keyRead, uintptr(unsafe.Pointer(&hkey)))
	if status != 0 {
		return systemProxySettings{}, "无法读取系统代理设置"
	}
	defer regCloseKey.Call(hkey)
	readValue := func(name string) (uint32, []byte) {
		valueName, err := syscall.UTF16PtrFromString(name)
		if err != nil {
			return 0, nil
		}
		var valueType uintptr
		var size uintptr
		status, _, _ := regQueryValueEx.Call(hkey, uintptr(unsafe.Pointer(valueName)), 0, uintptr(unsafe.Pointer(&valueType)), 0, uintptr(unsafe.Pointer(&size)))
		if status != 0 || size == 0 || size > 1<<16 {
			return 0, nil
		}
		buffer := make([]byte, size)
		status, _, _ = regQueryValueEx.Call(hkey, uintptr(unsafe.Pointer(valueName)), 0, uintptr(unsafe.Pointer(&valueType)), uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)))
		if status != 0 {
			return 0, nil
		}
		return uint32(valueType), buffer[:size]
	}
	regString := func(data []byte) string {
		if len(data)%2 != 0 {
			data = data[:len(data)-1]
		}
		u16 := make([]uint16, len(data)/2)
		for i := range u16 {
			u16[i] = binary.LittleEndian.Uint16(data[i*2:])
		}
		return strings.TrimSpace(syscall.UTF16ToString(u16))
	}
	settings := systemProxySettings{available: true}
	if valueType, data := readValue("ProxyEnable"); valueType == 4 && len(data) >= 4 {
		settings.enabled = binary.LittleEndian.Uint32(data) != 0
	}
	if valueType, data := readValue("ProxyServer"); valueType == 1 {
		settings.server = regString(data)
	}
	if valueType, data := readValue("AutoConfigURL"); valueType == 1 {
		settings.autoConfigURL = regString(data)
	}
	if valueType, data := readValue("ProxyOverride"); valueType == 1 {
		settings.override = regString(data)
	}
	return settings, ""
}
