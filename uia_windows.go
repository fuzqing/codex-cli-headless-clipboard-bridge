//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Minimal Windows SDK COM ABI. No UIA wrapper dependency. All interfaces live on
// one MTA worker; the keyboard-hook thread never calls COM or waits for providers.
type comObject struct{ vtable *[128]uintptr }

type comError struct {
	Method  int
	HRESULT uint32
}

func (e *comError) Error() string {
	return fmt.Sprintf("UIA method %d HRESULT 0x%08x", e.Method, e.HRESULT)
}

//go:uintptrescapes
func comCall(obj *comObject, index int, args ...uintptr) error {
	if obj == nil {
		return errors.New("UIA null interface")
	}
	a := append([]uintptr{uintptr(unsafe.Pointer(obj))}, args...)
	hr, _, _ := syscall.SyscallN(obj.vtable[index], a...)
	runtime.KeepAlive(obj)
	if int32(hr) < 0 {
		return &comError{index, uint32(hr)}
	}
	return nil
}
func comRelease(p *comObject) {
	if p != nil {
		_ = comCall(p, 2)
	}
}

var ole32 = windows.NewLazySystemDLL("ole32.dll")
var oleaut32 = windows.NewLazySystemDLL("oleaut32.dll")

type paneSnapshot struct {
	ID            string      `json:"pane_id"`
	TextAvailable bool        `json:"text_available"`
	Codex         bool        `json:"codex_tui"`
	Evidence      tuiEvidence `json:"evidence"`
}
type inspectRequest struct {
	ctx    context.Context
	target target
	result chan inspectResult
}
type inspectResult struct {
	pane paneSnapshot
	err  error
}
type tuiInspector struct{ requests chan inspectRequest }

func newTUIInspector(ctx context.Context) *tuiInspector {
	i := &tuiInspector{requests: make(chan inspectRequest)}
	go i.run(ctx)
	return i
}
func (i *tuiInspector) read(ctx context.Context, t target) (paneSnapshot, error) {
	r := inspectRequest{ctx: ctx, target: t, result: make(chan inspectResult, 1)}
	select {
	case i.requests <- r:
	case <-ctx.Done():
		return paneSnapshot{}, ctx.Err()
	}
	select {
	case result := <-r.result:
		return result.pane, result.err
	case <-ctx.Done():
		return paneSnapshot{}, ctx.Err()
	}
}
func (i *tuiInspector) run(ctx context.Context) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := ole32.NewProc("CoInitializeEx").Call(0, 0)
	var initErr error
	if int32(hr) < 0 {
		initErr = fmt.Errorf("UIA CoInitializeEx: 0x%08x", uint32(hr))
	} else {
		defer ole32.NewProc("CoUninitialize").Call()
	}
	var uia *comObject
	if initErr == nil {
		clsid, _ := windows.GUIDFromString("{E22AD333-B25F-460C-83D0-0581107395C9}")
		iid, _ := windows.GUIDFromString("{34723AFF-0C9D-49D0-9896-7AB52DF8CD8A}")
		hr, _, _ = ole32.NewProc("CoCreateInstance").Call(uintptr(unsafe.Pointer(&clsid)), 0, 1, uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&uia)))
		if int32(hr) < 0 {
			initErr = fmt.Errorf("UIA CoCreateInstance: 0x%08x", uint32(hr))
		} else {
			defer comRelease(uia)
			// IUIAutomation2 setters: AutoSetFocus, ConnectionTimeout, TransactionTimeout.
			for _, s := range []struct {
				method int
				value  uintptr
			}{{59, 0}, {61, 500}, {63, 500}} {
				if err := comCall(uia, s.method, s.value); err != nil {
					initErr = err
					break
				}
			}
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case r := <-i.requests:
			if r.ctx.Err() != nil {
				r.result <- inspectResult{err: r.ctx.Err()}
				continue
			}
			if initErr != nil {
				r.result <- inspectResult{err: initErr}
				continue
			}
			pane, err := inspectPane(uia, r.target)
			r.result <- inspectResult{pane, err}
		}
	}
}

func inspectPane(uia *comObject, t target) (paneSnapshot, error) {
	var pane paneSnapshot
	var element *comObject
	if err := comCall(uia, 8, uintptr(unsafe.Pointer(&element))); err != nil {
		return pane, err
	}
	defer comRelease(element)
	var pid int32
	if err := comCall(element, 20, uintptr(unsafe.Pointer(&pid))); err != nil {
		return pane, err
	}
	if uint32(pid) != t.PID {
		return pane, errors.New("UIA focus belongs to another process")
	}
	// Match the ancestor HWND as well as PID: one Terminal process can own several windows.
	var walker *comObject
	if err := comCall(uia, 16, uintptr(unsafe.Pointer(&walker))); err != nil {
		return pane, err
	}
	defer comRelease(walker)
	node := element
	matched := false
	for depth := 0; depth < 24 && node != nil; depth++ {
		var hwnd int32
		if err := comCall(node, 36, uintptr(unsafe.Pointer(&hwnd))); err == nil && uintptr(uint32(hwnd)) == t.HWND {
			matched = true
			if node != element {
				comRelease(node)
			}
			break
		}
		var parent *comObject
		err := comCall(walker, 3, uintptr(unsafe.Pointer(node)), uintptr(unsafe.Pointer(&parent)))
		if node != element {
			comRelease(node)
		}
		if err != nil {
			return pane, err
		}
		node = parent
		if depth == 23 {
			comRelease(node)
		}
	}
	if !matched {
		return pane, errors.New("UIA focused pane is outside target Terminal window")
	}
	id, err := runtimeID(element)
	if err != nil {
		return pane, err
	}
	pane.ID = id
	var pattern *comObject
	if err = comCall(element, 16, 10014, uintptr(unsafe.Pointer(&pattern))); err != nil {
		var code *comError
		if !errors.As(err, &code) || (code.HRESULT != 0x80040204 && code.HRESULT != 0x80004002) {
			return pane, err
		}
		return pane, nil
	}
	if pattern == nil {
		// A focused tab/menu/button is a valid non-TUI target. No clipboard access.
		return pane, nil
	}
	defer comRelease(pattern)
	var ranges *comObject
	if err = comCall(pattern, 6, uintptr(unsafe.Pointer(&ranges))); err != nil {
		return pane, err
	}
	defer comRelease(ranges)
	var n int32
	if err = comCall(ranges, 3, uintptr(unsafe.Pointer(&n))); err != nil {
		return pane, err
	}
	if n < 1 || n > 4 {
		return pane, errors.New("UIA visible ranges unavailable or ambiguous")
	}
	var text strings.Builder
	for index := int32(0); index < n; index++ {
		var span *comObject
		if err = comCall(ranges, 4, uintptr(index), uintptr(unsafe.Pointer(&span))); err != nil {
			return pane, err
		}
		var bstr uintptr
		err = comCall(span, 12, 65536, uintptr(unsafe.Pointer(&bstr)))
		comRelease(span)
		if err != nil {
			return pane, err
		}
		if bstr != 0 {
			length, _, _ := oleaut32.NewProc("SysStringLen").Call(bstr)
			if length >= 65536 {
				oleaut32.NewProc("SysFreeString").Call(bstr)
				return pane, errors.New("UIA viewport exceeds bounded text limit")
			}
			text.WriteString(string(utf16.Decode(unsafe.Slice((*uint16)(nativePointer(bstr)), int(length)))))
			oleaut32.NewProc("SysFreeString").Call(bstr)
		}
	}
	pane.TextAvailable = true
	pane.Evidence = recognizeCodex(text.String())
	pane.Codex = pane.Evidence.matches()
	return pane, nil
}

func runtimeID(element *comObject) (string, error) {
	var array uintptr
	if err := comCall(element, 4, uintptr(unsafe.Pointer(&array))); err != nil {
		return "", err
	}
	if array == 0 {
		return "", errors.New("UIA runtime identity missing")
	}
	defer oleaut32.NewProc("SafeArrayDestroy").Call(array)
	dim, _, _ := oleaut32.NewProc("SafeArrayGetDim").Call(array)
	if dim != 1 {
		return "", errors.New("UIA invalid runtime identity")
	}
	var lo, hi int32
	for _, p := range []struct {
		name string
		out  *int32
	}{{"SafeArrayGetLBound", &lo}, {"SafeArrayGetUBound", &hi}} {
		hr, _, _ := oleaut32.NewProc(p.name).Call(array, 1, uintptr(unsafe.Pointer(p.out)))
		if int32(hr) < 0 {
			return "", errors.New("UIA runtime identity bounds failed")
		}
	}
	if hi < lo || int64(hi)-int64(lo) > 63 {
		return "", errors.New("UIA runtime identity too long")
	}
	var id strings.Builder
	for index := lo; index <= hi; index++ {
		var value int32
		hr, _, _ := oleaut32.NewProc("SafeArrayGetElement").Call(array, uintptr(unsafe.Pointer(&index)), uintptr(unsafe.Pointer(&value)))
		if int32(hr) < 0 {
			return "", errors.New("UIA runtime identity read failed")
		}
		fmt.Fprintf(&id, "%d/", value)
	}
	return id.String(), nil
}
