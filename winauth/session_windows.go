//go:build windows

package winauth

import (
	"encoding/binary"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"openDL/i18n"
)

const Available = true

type secHandle struct{ Lower, Upper uintptr }
type secBuffer struct {
	Size, Type uint32
	Data       *byte
}
type secBufferDesc struct {
	Version, Count uint32
	Buffers        *secBuffer
}
type packageInfo struct {
	Capabilities   uint32
	Version, RPCID uint16
	MaxToken       uint32
	Name, Comment  *uint16
}
type negotiationInfo struct {
	Package *packageInfo
	State   uint32
}

type nativeAPI struct {
	acquire, freeCred, initialize, deleteContext, complete, query, freeBuffer, packageInfo *syscall.LazyProc
	exclude, freeIdentity                                                                  *syscall.LazyProc
}

var apiOnce sync.Once
var api nativeAPI
var apiErr error

func loadAPI() (*nativeAPI, error) {
	apiOnce.Do(func() {
		var directory [512]uint16
		getDir := syscall.NewLazyDLL("kernel32.dll").NewProc("GetSystemDirectoryW")
		n, _, err := getDir.Call(uintptr(unsafe.Pointer(&directory[0])), uintptr(len(directory)))
		if n == 0 {
			apiErr = err
			return
		}
		if n >= uintptr(len(directory)) {
			apiErr = syscall.Errno(122)
			return
		}
		root := syscall.UTF16ToString(directory[:])
		security := syscall.NewLazyDLL(filepath.Join(root, "secur32.dll"))
		identities := syscall.NewLazyDLL(filepath.Join(root, "sspicli.dll"))
		api = nativeAPI{
			acquire: security.NewProc("AcquireCredentialsHandleW"), freeCred: security.NewProc("FreeCredentialsHandle"),
			initialize: security.NewProc("InitializeSecurityContextW"), deleteContext: security.NewProc("DeleteSecurityContext"),
			complete: security.NewProc("CompleteAuthToken"), query: security.NewProc("QueryContextAttributesW"),
			freeBuffer: security.NewProc("FreeContextBuffer"), packageInfo: security.NewProc("QuerySecurityPackageInfoW"),
			exclude: identities.NewProc("SspiExcludePackage"), freeIdentity: identities.NewProc("SspiFreeAuthIdentity"),
		}
		for _, proc := range []*syscall.LazyProc{api.acquire, api.freeCred, api.initialize, api.deleteContext, api.complete, api.query, api.freeBuffer, api.packageInfo} {
			if err := proc.Find(); err != nil {
				apiErr = err
				return
			}
		}
	})
	return &api, apiErr
}

type clientSession struct {
	api                                         *nativeAPI
	credential, context                         secHandle
	hasCredential, hasContext, complete, closed bool
	kerberosOnly, rawNTLM                       bool
	target                                      *uint16
	bindings                                    []byte
	maxToken                                    uint32
}

func statusError(stage string, status uintptr) error {
	return fmt.Errorf(i18n.T("Windows 集成认证失败（%s，0x%08X）"), stage, uint32(status))
}

func New(spn string, kerberosOnly, rawNTLM bool, channelBinding []byte) (Session, error) {
	if kerberosOnly && rawNTLM {
		return nil, i18n.NewError("仅 Kerberos 策略禁止 NTLM 回退")
	}
	a, err := loadAPI()
	if err != nil {
		return nil, err
	}
	target, err := syscall.UTF16PtrFromString(spn)
	if err != nil {
		return nil, err
	}
	name := "Negotiate"
	if rawNTLM {
		name = "NTLM"
	}
	provider, _ := syscall.UTF16PtrFromString(name)
	var info *packageInfo
	status, _, _ := a.packageInfo.Call(uintptr(unsafe.Pointer(provider)), uintptr(unsafe.Pointer(&info)))
	if uint32(status) != 0 {
		return nil, statusError("QuerySecurityPackageInfo", status)
	}
	if info == nil {
		return nil, i18n.NewError("Windows 认证包信息无效")
	}
	maxToken := info.MaxToken
	a.freeBuffer.Call(uintptr(unsafe.Pointer(info)))
	if maxToken == 0 || maxToken > 1<<20 {
		return nil, i18n.NewError("Windows 认证令牌大小无效")
	}
	var identity uintptr
	if kerberosOnly {
		// These functions are only needed by the strict policy. Their absence
		// must not prevent regular Negotiate or NTLM authentication.
		if err := a.exclude.Find(); err != nil {
			return nil, err
		}
		if err := a.freeIdentity.Find(); err != nil {
			return nil, err
		}
		// The SDK declares the source identity optional. NULL means default
		// credentials; exclude NTLM before any context or outgoing token exists.
		ntlm, _ := syscall.UTF16PtrFromString("NTLM")
		status, _, _ = a.exclude.Call(0, uintptr(unsafe.Pointer(ntlm)), uintptr(unsafe.Pointer(&identity)))
		if uint32(status) != 0 {
			return nil, statusError("SspiExcludePackage", status)
		}
		if identity == 0 {
			return nil, i18n.NewError("无法应用仅 Kerberos 认证策略")
		}
		defer a.freeIdentity.Call(identity)
	}
	// SEC_CHANNEL_BINDINGS uses offsets within this buffer, unlike the native
	// GSSAPI structure; the shared layer passes only application data.
	var bindings []byte
	if len(channelBinding) > 0 {
		if len(channelBinding) > 1<<20 {
			return nil, i18n.NewError("Windows 认证通道绑定数据过大")
		}
		bindings = make([]byte, 32+len(channelBinding))
		binary.LittleEndian.PutUint32(bindings[24:28], uint32(len(channelBinding)))
		binary.LittleEndian.PutUint32(bindings[28:32], 32)
		copy(bindings[32:], channelBinding)
	}
	s := &clientSession{api: a, target: target, bindings: bindings, maxToken: maxToken, kerberosOnly: kerberosOnly, rawNTLM: rawNTLM}
	var expiry int64
	status, _, _ = a.acquire.Call(0, uintptr(unsafe.Pointer(provider)), 2, 0, identity, 0, 0, uintptr(unsafe.Pointer(&s.credential)), uintptr(unsafe.Pointer(&expiry)))
	runtime.KeepAlive(provider)
	if uint32(status) != 0 {
		return nil, statusError("AcquireCredentialsHandle", status)
	}
	s.hasCredential = true
	return s, nil
}

func (s *clientSession) Next(input []byte) ([]byte, bool, error) {
	if s.closed || (s.complete && len(input) == 0) {
		return nil, false, i18n.NewError("Windows 认证上下文状态无效")
	}
	if len(input) > 1<<20 {
		return nil, false, i18n.NewError("Windows 认证令牌超过大小限制")
	}
	var inputs []secBuffer
	if len(input) > 0 {
		inputs = append(inputs, secBuffer{Size: uint32(len(input)), Type: 2, Data: &input[0]})
	}
	if len(s.bindings) > 0 {
		inputs = append(inputs, secBuffer{Size: uint32(len(s.bindings)), Type: 14, Data: &s.bindings[0]})
	}
	var inputPtr uintptr
	var inDesc secBufferDesc
	if len(inputs) > 0 {
		inDesc = secBufferDesc{Count: uint32(len(inputs)), Buffers: &inputs[0]}
		inputPtr = uintptr(unsafe.Pointer(&inDesc))
	}
	output := make([]byte, s.maxToken)
	out := secBuffer{Size: uint32(len(output)), Type: 2, Data: &output[0]}
	outDesc := secBufferDesc{Count: 1, Buffers: &out}
	var contextPtr uintptr
	if s.hasContext {
		contextPtr = uintptr(unsafe.Pointer(&s.context))
	}
	flags := uintptr(0x800) // ISC_REQ_CONNECTION, no delegation or impersonation
	if !s.rawNTLM {
		flags |= 2
	} // Request Kerberos mutual authentication.
	var attributes uint32
	var expiry int64
	status, _, _ := s.api.initialize.Call(uintptr(unsafe.Pointer(&s.credential)), contextPtr, uintptr(unsafe.Pointer(s.target)), flags, 0, 0x10, inputPtr, 0, uintptr(unsafe.Pointer(&s.context)), uintptr(unsafe.Pointer(&outDesc)), uintptr(unsafe.Pointer(&attributes)), uintptr(unsafe.Pointer(&expiry)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(inputs)
	runtime.KeepAlive(inDesc)
	runtime.KeepAlive(s.bindings)
	runtime.KeepAlive(s.target)
	code := uint32(status)
	if code != 0 && code != 0x00090312 && code != 0x00090313 && code != 0x00090314 {
		if s.context.Lower != 0 || s.context.Upper != 0 {
			s.hasContext = true
		}
		return nil, false, statusError("InitializeSecurityContext", status)
	}
	s.hasContext = true
	if code == 0x00090313 || code == 0x00090314 {
		result, _, _ := s.api.complete.Call(uintptr(unsafe.Pointer(&s.context)), uintptr(unsafe.Pointer(&outDesc)))
		if uint32(result) != 0 {
			return nil, false, statusError("CompleteAuthToken", result)
		}
	}
	if (out.Size > 0 && out.Data != &output[0]) || out.Size > uint32(len(output)) {
		return nil, false, i18n.NewError("Windows 认证输出令牌无效")
	}
	s.complete = code == 0 || code == 0x00090313
	return append([]byte(nil), output[:out.Size]...), s.complete, nil
}

func nativeString(ptr *uint16) string {
	if ptr == nil {
		return ""
	}
	text := make([]uint16, 0, 64)
	for i := 0; i < 256; i++ {
		c := *(*uint16)(unsafe.Add(unsafe.Pointer(ptr), i*2))
		if c == 0 {
			return string(utf16.Decode(text))
		}
		text = append(text, c)
	}
	return ""
}

func (s *clientSession) Package() (string, error) {
	if !s.complete || s.closed {
		return "", i18n.NewError("Windows 集成认证尚未完成")
	}
	if s.rawNTLM {
		return "NTLM", nil
	}
	var info negotiationInfo
	status, _, _ := s.api.query.Call(uintptr(unsafe.Pointer(&s.context)), 12, uintptr(unsafe.Pointer(&info)))
	if uint32(status) != 0 {
		return "", statusError("QueryContextAttributes", status)
	}
	if info.Package == nil {
		return "", i18n.NewError("Windows 认证包信息无效")
	}
	defer s.api.freeBuffer.Call(uintptr(unsafe.Pointer(info.Package)))
	// OPTIMISTIC and IN_PROGRESS are explicitly incomplete. DIRECT is also
	// used by Negotiate for a directly selected package on some systems.
	if info.State != 0 && info.State != 3 {
		return "", i18n.NewError("Windows 集成认证协商尚未完成")
	}
	name := nativeString(info.Package.Name)
	if !strings.EqualFold(name, "Kerberos") && !strings.EqualFold(name, "NTLM") {
		return "", i18n.NewError("Windows 协商选择了不支持的认证协议")
	}
	if s.kerberosOnly && !strings.EqualFold(name, "Kerberos") {
		return "", i18n.NewError("仅 Kerberos 策略禁止 NTLM 回退")
	}
	if strings.EqualFold(name, "Kerberos") {
		// SECPKG_ATTR_FLAGS reports the negotiated context, rather than only
		// the requested capabilities from InitializeSecurityContext.
		var negotiatedFlags uint32
		status, _, _ = s.api.query.Call(uintptr(unsafe.Pointer(&s.context)), 14, uintptr(unsafe.Pointer(&negotiatedFlags)))
		if uint32(status) != 0 {
			return "", statusError("QueryContextAttributes flags", status)
		}
		if negotiatedFlags&2 == 0 {
			return "", i18n.NewError("Kerberos 未完成服务端身份验证")
		}
	}
	if strings.EqualFold(name, "Kerberos") {
		return "Kerberos", nil
	}
	return "NTLM", nil
}

func (s *clientSession) Close() {
	if s.closed {
		return
	}
	s.closed = true
	if s.hasContext {
		s.api.deleteContext.Call(uintptr(unsafe.Pointer(&s.context)))
	}
	if s.hasCredential {
		s.api.freeCred.Call(uintptr(unsafe.Pointer(&s.credential)))
	}
	clear(s.bindings)
}
