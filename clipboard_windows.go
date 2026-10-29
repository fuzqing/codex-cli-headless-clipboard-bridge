//go:build windows

package main

import (
	"context"
	"errors"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var user32 = windows.NewLazySystemDLL("user32.dll")
var kernel32 = windows.NewLazySystemDLL("kernel32.dll")
var openClipboard = user32.NewProc("OpenClipboard")
var closeClipboard = user32.NewProc("CloseClipboard")
var getClipboardData = user32.NewProc("GetClipboardData")
var registerClipboardFormat = user32.NewProc("RegisterClipboardFormatW")
var globalLock = kernel32.NewProc("GlobalLock")
var globalUnlock = kernel32.NewProc("GlobalUnlock")
var globalSize = kernel32.NewProc("GlobalSize")

func readClipboard(ctx context.Context, c Config) (rawImage, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	deadline := time.NewTimer(600 * time.Millisecond)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		ok, _, _ := openClipboard.Call(0)
		if ok != 0 {
			break
		}
		select {
		case <-ctx.Done():
			return rawImage{}, ctx.Err()
		case <-deadline.C:
			return rawImage{}, errors.New("clipboard busy for 600ms")
		case <-tick.C:
		}
	}
	defer closeClipboard.Call()
	name, _ := windows.UTF16PtrFromString("PNG")
	pf, _, _ := registerClipboardFormat.Call(uintptr(unsafe.Pointer(name)))
	for _, f := range []uintptr{pf, 17, 8} {
		if f == 0 {
			continue
		}
		handle, _, _ := getClipboardData.Call(f)
		if handle == 0 {
			continue
		}
		size, _, _ := globalSize.Call(handle)
		limit := uint64(c.MaxImageBytes)
		if f != pf {
			limit = uint64(c.MaxPixels)*4 + 4096
		}
		if size == 0 || uint64(size) > limit {
			return rawImage{}, errors.New("clipboard image exceeds configured memory/size limit")
		}
		ptr, _, _ := globalLock.Call(handle)
		if ptr == 0 {
			return rawImage{}, errors.New("GlobalLock failed")
		}
		b := append([]byte(nil), unsafe.Slice((*byte)(nativePointer(ptr)), int(size))...)
		globalUnlock.Call(handle)
		return rawImage{data: b, png: f == pf}, nil
	}
	return rawImage{}, errNoImage
}
