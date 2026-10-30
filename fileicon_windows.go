//go:build windows

package main

import (
	"image"
	"path/filepath"
	"syscall"
	"unsafe"

	"github.com/egoist/mygo"
)

const (
	fileIconsAvailable = true
	fileIconMaxSize    = 256

	shgfiIcon              = 0x100
	shgfiSysIconIndex      = 0x4000
	shgfiUseFileAttributes = 0x10
	fileAttributeNormal    = 0x80

	shilJumbo      = 4 // 256x256, fixed regardless of DPI
	ildTransparent = 0x1

	// IImageList slots, from commctrl.h behind IUnknown's three.
	ilGetIconSlot  = 10
	comReleaseSlot = 2
)

var (
	fileIconShell32 = syscall.NewLazyDLL("shell32.dll")
	fileIconShcore  = syscall.NewLazyDLL("shcore.dll")
	fileIconUser32  = syscall.NewLazyDLL("user32.dll")
	fileIconGDI32   = syscall.NewLazyDLL("gdi32.dll")
	fileIconOle32   = syscall.NewLazyDLL("ole32.dll")

	procSHGetFileInfoW     = fileIconShell32.NewProc("SHGetFileInfoW")
	procSHGetImageList     = fileIconShell32.NewProc("SHGetImageList")
	procExtractIconExW     = fileIconShell32.NewProc("ExtractIconExW")
	procDestroyIcon        = fileIconUser32.NewProc("DestroyIcon")
	procDrawIconEx         = fileIconUser32.NewProc("DrawIconEx")
	procCreateDIBSection   = fileIconGDI32.NewProc("CreateDIBSection")
	procCreateCompatibleDC = fileIconGDI32.NewProc("CreateCompatibleDC")
	procSelectObject       = fileIconGDI32.NewProc("SelectObject")
	procDeleteObject       = fileIconGDI32.NewProc("DeleteObject")
	procDeleteDC           = fileIconGDI32.NewProc("DeleteDC")
	procGdiFlush           = fileIconGDI32.NewProc("GdiFlush")
	procGetDC              = fileIconUser32.NewProc("GetDC")
	procReleaseDC          = fileIconUser32.NewProc("ReleaseDC")
	procGetDeviceCaps      = fileIconGDI32.NewProc("GetDeviceCaps")
	procGetDpiForMonitor   = fileIconShcore.NewProc("GetDpiForMonitor") // shcore, not gdi32
	procMonitorFromWindow  = fileIconUser32.NewProc("MonitorFromWindow")
	procCoInitializeEx     = fileIconOle32.NewProc("CoInitializeEx")
	procCoUninitialize     = fileIconOle32.NewProc("CoUninitialize")
)

type shFileInfoW struct {
	hIcon         uintptr
	iIcon         int32
	dwAttributes  uint32
	szDisplayName [260]uint16
	szTypeName    [80]uint16
}

// IID_IImageList, from commctrl.h; SHGetImageList hands back this interface.
type fileIconGUID struct {
	data1 uint32
	data2 uint16
	data3 uint16
	data4 [8]byte
}

var iidFileIconImageList = fileIconGUID{
	data1: 0x46EB5926,
	data2: 0x582E,
	data3: 0x4017,
	data4: [8]byte{0x9F, 0xDF, 0xE8, 0x99, 0x8D, 0xAA, 0x09, 0x50},
}

// fileIconComCall calls a COM method through its vtable slot. The image list
// is reached this way instead of comctl32's ImageList_* macros so the call
// works no matter which comctl32 the process is bound to.
func fileIconComCall(obj uintptr, slot int, args ...uintptr) uintptr {
	vtable := *(*uintptr)(unsafe.Pointer(obj))
	fn := *(*uintptr)(unsafe.Pointer(vtable + uintptr(slot)*unsafe.Sizeof(uintptr(0))))
	ret, _, _ := syscall.SyscallN(fn, append([]uintptr{obj}, args...)...)
	return ret
}

type fileIconBitmapInfo struct {
	size                         uint32
	width, height                int32
	planes, bits                 uint16
	compression, sizeImage       uint32
	xPelsPerMeter, yPelsPerMeter int32
	clrUsed, clrImportant        uint32
	colors                       [1]uint32
}

func initFileIconThread() (func(), bool) {
	// MyGo enables per-monitor DPI awareness before the window opens, so the
	// device caps below report physical pixels rather than logical ones.
	hr, _, _ := procCoInitializeEx.Call(0, 2) // COINIT_APARTMENTTHREADED
	if int32(hr) < 0 {
		return nil, false
	}
	return func() { procCoUninitialize.Call() }, true
}

func extractFileIcon(path string, actual bool) image.Image {
	if path == "" {
		path = "file"
	}
	if actual {
		if img := extractFileIconHandle(path, true); img != nil {
			return img
		}
		path = filepath.Ext(path)
	}
	if img := extractFileIconHandle(path, false); img != nil {
		return img
	}
	if img := extractLargeFileIcon(path); img != nil {
		return img
	}
	return nil
}

// shellJumboIcon returns the shell's jumbo icon for path, or 0 when the
// system image list has none. actual picks the file's own icon over the one
// registered for its extension. The caller must DestroyIcon the result.
func shellJumboIcon(path string, actual bool) uintptr {
	ptr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0
	}
	flags := uintptr(shgfiSysIconIndex)
	if !actual {
		flags |= shgfiUseFileAttributes
	}
	var info shFileInfoW
	// SHGFI_SYSICONINDEX returns the system image list handle and copies the
	// icon's index into iIcon; the index is shared by every size of the list.
	ret, _, _ := procSHGetFileInfoW.Call(uintptr(unsafe.Pointer(ptr)), fileAttributeNormal,
		uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info), flags)
	if ret == 0 || info.iIcon < 0 {
		return 0
	}
	var list uintptr
	hr, _, _ := procSHGetImageList.Call(shilJumbo,
		uintptr(unsafe.Pointer(&iidFileIconImageList)), uintptr(unsafe.Pointer(&list)))
	if int32(hr) < 0 || list == 0 {
		return 0
	}
	// System image lists are process-wide and read-only: release the
	// reference, never destroy the list itself.
	defer fileIconComCall(list, comReleaseSlot)
	var icon uintptr
	if int32(fileIconComCall(list, ilGetIconSlot, uintptr(info.iIcon), ildTransparent,
		uintptr(unsafe.Pointer(&icon)))) < 0 {
		return 0
	}
	return icon
}

func extractFileIconHandle(path string, actual bool) image.Image {
	// Prefer the jumbo image list: SHGetFileInfoW's own handle is only the
	// 32px large icon, which upscales soft in the 24px table slot.
	if icon := shellJumboIcon(path, actual); icon != 0 {
		defer procDestroyIcon.Call(icon)
		if img := renderFileIcon(icon); img != nil {
			return img
		}
	}
	ptr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil
	}
	flags := uintptr(shgfiIcon)
	if !actual {
		flags |= shgfiUseFileAttributes
	}
	var info shFileInfoW
	ret, _, _ := procSHGetFileInfoW.Call(uintptr(unsafe.Pointer(ptr)), fileAttributeNormal,
		uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info), flags)
	if info.hIcon != 0 {
		defer procDestroyIcon.Call(info.hIcon)
	}
	if ret == 0 || info.hIcon == 0 {
		return nil
	}
	return renderFileIcon(info.hIcon)
}

func fileIconWindowHandle() uintptr {
	if mygo.App == nil || !mygo.App.IsReady() {
		return 0
	}
	// Any live window works: the handle only picks the monitor for DPI.
	var hwnd uintptr
	mygo.RunOnMain(func() {
		for _, w := range mygo.Windows() {
			if w != nil && !w.IsDestroyed() {
				hwnd = w.NativeHandle()
				return
			}
		}
	})
	return hwnd
}

func deviceScale() float64 {
	const (
		dpiTypeEffective = 0
		logPixelsSX      = 88
		logPixelsSY      = 90
	)
	hwnd := fileIconWindowHandle()
	mon, _, _ := procMonitorFromWindow.Call(hwnd, 2)
	// GetDpiForMonitor needs Windows 8.1+. LazyProc.Call panics on a missing
	// export, and this runs on the icon goroutine, so probe first and fall
	// through to the device caps below.
	if mon != 0 && procGetDpiForMonitor.Find() == nil {
		var dpiX, dpiY uint32
		ret, _, _ := procGetDpiForMonitor.Call(mon, dpiTypeEffective,
			uintptr(unsafe.Pointer(&dpiX)), uintptr(unsafe.Pointer(&dpiY)))
		if ret == 0 && dpiX != 0 { // S_OK
			return clampFileIconScale(float64(max(dpiX, dpiY)) / 96)
		}
	}
	hdc, _, _ := procGetDC.Call(hwnd)
	if hdc == 0 {
		return 1
	}
	defer procReleaseDC.Call(hwnd, hdc)
	x, _, _ := procGetDeviceCaps.Call(hdc, logPixelsSX)
	y, _, _ := procGetDeviceCaps.Call(hdc, logPixelsSY)
	return clampFileIconScale(float64(max(x, y)) / 96)
}

func clampFileIconScale(scale float64) float64 {
	if scale < 1 || scale > 4 {
		return 1
	}
	return scale
}

// Some shell icon handlers expose larger PNG images than SHGetFileInfoW's
// generic handle. ExtractIconExW can retrieve those without changing the icon
// source used for normal files and extensions.
func extractLargeFileIcon(path string) image.Image {
	ptr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil
	}
	var icon uintptr
	ret, _, _ := procExtractIconExW.Call(uintptr(unsafe.Pointer(ptr)), 0,
		uintptr(unsafe.Pointer(&icon)), 0, 1)
	if ret == ^uintptr(0) || icon == 0 {
		return nil
	}
	defer procDestroyIcon.Call(icon)
	return renderFileIcon(icon)
}

// Shell icons may use device-dependent bitmaps whose bmBits is nil. Draw into
// our own top-down DIB, preserving premultiplied alpha and legacy AND masks.
func renderFileIcon(icon uintptr) image.Image {
	size := min(fileIconMaxSize, int(64*deviceScale()))
	for current := size; current >= 32; current /= 2 {
		if img := renderFileIconAtSize(icon, current); img != nil {
			return img
		}
		if current == 32 {
			break
		}
	}
	return nil
}

func renderFileIconAtSize(icon uintptr, size int) image.Image {
	if size < 1 || size > fileIconMaxSize {
		return nil
	}
	dc, _, _ := procCreateCompatibleDC.Call(0)
	if dc == 0 {
		return nil
	}
	defer procDeleteDC.Call(dc)
	info := fileIconBitmapInfo{size: 40, width: int32(size), height: -int32(size), planes: 1, bits: 32}
	var pixels unsafe.Pointer
	bmp, _, _ := procCreateDIBSection.Call(dc, uintptr(unsafe.Pointer(&info)), 0,
		uintptr(unsafe.Pointer(&pixels)), 0, 0)
	if bmp == 0 {
		return nil
	}
	defer procDeleteObject.Call(bmp)
	if pixels == nil {
		return nil
	}
	previous, _, _ := procSelectObject.Call(dc, bmp)
	if previous == 0 || previous == ^uintptr(0) {
		return nil
	}
	defer procSelectObject.Call(dc, previous)
	src := unsafe.Slice((*byte)(pixels), size*size*4)
	clear(src)
	ok, _, _ := procDrawIconEx.Call(dc, 0, 0, icon, uintptr(size), uintptr(size), 0, 0, 3) // DI_NORMAL
	procGdiFlush.Call()
	if ok == 0 {
		return nil
	}
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	anyAlpha := false
	for i := 0; i < len(src); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = src[i+2], src[i+1], src[i], src[i+3]
		anyAlpha = anyAlpha || src[i+3] != 0
	}
	if !anyAlpha {
		clear(src)
		ok, _, _ = procDrawIconEx.Call(dc, 0, 0, icon, uintptr(size), uintptr(size), 0, 0, 1) // DI_MASK
		procGdiFlush.Call()
		if ok == 0 {
			return nil
		}
		for i := 0; i < len(src); i += 4 {
			if src[i] == 0 {
				img.Pix[i+3] = 255
			} else {
				img.Pix[i], img.Pix[i+1], img.Pix[i+2] = 0, 0, 0
			}
		}
	}
	return img
}
