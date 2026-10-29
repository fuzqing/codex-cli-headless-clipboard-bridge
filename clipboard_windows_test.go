//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Use a private noninteractive window station in a subprocess. This exercises
// real Windows clipboard memory without reading or replacing the user's clipboard.
func TestNativeClipboard(t *testing.T) {
	if os.Getenv("CLIPBRIDGE_ISOLATED_CLIPBOARD") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestNativeClipboard$", "-test.v")
		cmd.Env = append(os.Environ(), "CLIPBRIDGE_ISOLATED_CLIPBOARD=1")
		out, e := cmd.CombinedOutput()
		t.Log(string(out))
		if e != nil {
			t.Fatal(e)
		}
		if bytes.Contains(out, []byte("--- SKIP")) {
			t.Skip("private native clipboard station unavailable in this environment")
		}
		return
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	createStation := user32.NewProc("CreateWindowStationW")
	setStation := user32.NewProc("SetProcessWindowStation")
	getStation := user32.NewProc("GetProcessWindowStation")
	closeStation := user32.NewProc("CloseWindowStation")
	createDesktop := user32.NewProc("CreateDesktopW")
	setDesktop := user32.NewProc("SetThreadDesktop")
	getDesktop := user32.NewProc("GetThreadDesktop")
	closeDesktop := user32.NewProc("CloseDesktop")
	oldStation, _, _ := getStation.Call()
	oldDesktop, _, _ := getDesktop.Call(uintptr(windows.GetCurrentThreadId()))
	name, _ := windows.UTF16PtrFromString("clipbridge-test")
	station, _, e := createStation.Call(uintptr(unsafe.Pointer(name)), 0, 0x3f, 0)
	if station == 0 {
		if errors.Is(e, windows.ERROR_ACCESS_DENIED) {
			t.Skip("private window station denied by sandbox; run compiled tests outside sandbox")
		}
		t.Fatalf("private clipboard station: %v", e)
	}
	defer closeStation.Call(station)
	if ok, _, e := setStation.Call(station); ok == 0 {
		t.Fatal(e)
	}
	defer setStation.Call(oldStation)
	desk, _, e := createDesktop.Call(uintptr(unsafe.Pointer(name)), 0, 0, 0, 0x1ff, 0)
	if desk == 0 {
		t.Fatal(e)
	}
	defer closeDesktop.Call(desk)
	if ok, _, e := setDesktop.Call(desk); ok == 0 {
		t.Fatal(e)
	}
	defer setDesktop.Call(oldDesktop)
	class, _ := windows.UTF16PtrFromString("STATIC")
	hwnd, _, e := user32.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(name)), 0, 0, 0, 1, 1, ^uintptr(2), 0, 0, 0)
	if hwnd == 0 {
		t.Fatal(e)
	}
	defer user32.NewProc("DestroyWindow").Call(hwnd)
	pngName, _ := windows.UTF16PtrFromString("PNG")
	pngFormat, _, _ := registerClipboardFormat.Call(uintptr(unsafe.Pointer(pngName)))
	put := func(format uintptr, b []byte) {
		t.Helper()
		if ok, _, e := openClipboard.Call(hwnd); ok == 0 {
			t.Fatal(e)
		}
		defer closeClipboard.Call()
		if ok, _, e := user32.NewProc("EmptyClipboard").Call(); ok == 0 {
			t.Fatal(e)
		}
		if b == nil {
			return
		}
		h, _, e := kernel32.NewProc("GlobalAlloc").Call(2, uintptr(len(b)))
		if h == 0 {
			t.Fatal(e)
		}
		p, _, _ := globalLock.Call(h)
		copy(unsafe.Slice((*byte)(nativePointer(p)), len(b)), b)
		globalUnlock.Call(h)
		if ok, _, e := user32.NewProc("SetClipboardData").Call(format, h); ok == 0 {
			kernel32.NewProc("GlobalFree").Call(h)
			t.Fatal(e)
		}
	}
	c := Config{MaxImageBytes: 4096, MaxPixels: 16}
	put(0, nil)
	if _, e := readClipboard(context.Background(), c); !errors.Is(e, errNoImage) {
		t.Fatal(e)
	}
	put(13, []byte{'t', 0, 0, 0})
	if _, e := readClipboard(context.Background(), c); !errors.Is(e, errNoImage) {
		t.Fatal(e)
	}
	b := dib(2, 2, 24, false)
	binary.LittleEndian.PutUint32(b[40:], 0x00ff0000)
	put(8, b)
	seq := user32.NewProc("GetClipboardSequenceNumber")
	before, _, _ := seq.Call()
	raw, e := readClipboard(context.Background(), c)
	if e != nil || raw.png {
		t.Fatal(e)
	}
	png, e := normalize(raw, c)
	if e != nil {
		t.Fatal(e)
	}
	after, _, _ := seq.Call()
	if before != after {
		t.Fatal("clipboard changed during read")
	}
	put(pngFormat, png)
	raw, e = readClipboard(context.Background(), c)
	if e != nil || !raw.png {
		t.Fatal(e)
	}
	if _, e = normalize(raw, c); e != nil {
		t.Fatal(e)
	}
	put(pngFormat, make([]byte, 4097))
	if _, e = readClipboard(context.Background(), c); e == nil {
		t.Fatal("oversize clipboard accepted")
	}
	t.Log("real Win32 clipboard: empty/text/DIB/PNG/size limit passed; user clipboard isolated")
}
