//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var getForegroundWindow = user32.NewProc("GetForegroundWindow")
var getWindowText = user32.NewProc("GetWindowTextW")
var getClassName = user32.NewProc("GetClassNameW")
var getWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
var setWindowsHook = user32.NewProc("SetWindowsHookExW")
var unhookWindowsHook = user32.NewProc("UnhookWindowsHookEx")
var callNextHook = user32.NewProc("CallNextHookEx")
var setWinEventHook = user32.NewProc("SetWinEventHook")
var unhookWinEvent = user32.NewProc("UnhookWinEvent")
var getMessage = user32.NewProc("GetMessageW")
var peekMessage = user32.NewProc("PeekMessageW")
var postThreadMessage = user32.NewProc("PostThreadMessageW")
var getAsyncKeyState = user32.NewProc("GetAsyncKeyState")
var sendInput = user32.NewProc("SendInput")
var messageBeep = user32.NewProc("MessageBeep")

type target struct {
	HWND      uintptr
	PID       uint32
	Title     string
	Epoch     uint64
	Triggered time.Time
	PaneID    string
}
type desktop struct {
	c         Config
	epoch     atomic.Uint64
	busy      atomic.Bool
	physicalV atomic.Bool
	// gate is used only on the message-loop thread, including SendInput.
	gate      keyGate
	requests  chan target
	ignored   chan struct{}
	commits   chan commit
	tid       atomic.Uint32
	inspector *tuiInspector
}
type commit struct {
	target target
	ctx    context.Context
	result chan error
}
type keyGate struct{ down, suppressed bool }

// Pure state machine shared by the real hook and tests. Never queues repeat uploads.
func (g *keyGate) key(down, alt, extra, allowed, busy bool) (swallow, trigger bool) {
	if !down {
		swallow = g.suppressed
		g.down = false
		g.suppressed = false
		return
	}
	if g.down {
		return g.suppressed, false
	}
	g.down = true
	if alt && !extra && allowed {
		g.suppressed = true
		return true, !busy
	}
	return false, false
}

func newDesktop(c Config) *desktop {
	return &desktop{c: c, requests: make(chan target, 1), ignored: make(chan struct{}, 1), commits: make(chan commit, 1)}
}
func (d *desktop) snapshot() (target, bool) {
	h, _, _ := getForegroundWindow.Call()
	if h == 0 {
		return target{}, false
	}
	var pid uint32
	getWindowThreadProcessID.Call(h, uintptr(unsafe.Pointer(&pid)))
	var title, class [512]uint16
	n, _, _ := getWindowText.Call(h, uintptr(unsafe.Pointer(&title[0])), uintptr(len(title)))
	getClassName.Call(h, uintptr(unsafe.Pointer(&class[0])), uintptr(len(class)))
	t := target{HWND: h, PID: pid, Title: windows.UTF16ToString(title[:]), Epoch: d.epoch.Load()}
	if windows.UTF16ToString(class[:]) != d.c.Window.Class {
		return t, false
	}
	if d.c.Window.Mode == "title" && (n >= uintptr(len(title)-1) || d.c.title == nil || !d.c.title.MatchString(t.Title)) {
		return t, false
	}
	p, e := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if e != nil {
		return t, false
	}
	defer windows.CloseHandle(p)
	var path [1024]uint16
	size := uint32(len(path))
	if windows.QueryFullProcessImageName(p, 0, &path[0], &size) != nil {
		return t, false
	}
	return t, strings.EqualFold(filepath.Base(windows.UTF16ToString(path[:size])), d.c.Window.Process)
}
func (d *desktop) valid(t target) bool {
	cur, ok := d.snapshot()
	return ok && cur.HWND == t.HWND && cur.PID == t.PID && (d.c.Window.Mode != "title" || cur.Title == t.Title) && cur.Epoch == t.Epoch
}
func keyDown(v uintptr) bool { r, _, _ := getAsyncKeyState.Call(v); return r&0x8000 != 0 }
func keysReleased() bool {
	for _, v := range []uintptr{0x12, 0x56, 0x10, 0x11, 0x5b, 0x5c} {
		if keyDown(v) {
			return false
		}
	}
	return true
}
func (d *desktop) waitRelease(ctx context.Context, t target) error {
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if !d.valid(t) {
			return errors.New("target window/tab/focus changed; paste cancelled")
		}
		if keysReleased() && !d.physicalV.Load() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

type keyboardEvent struct {
	VK, Scan, Flags, Time uint32
	Extra                 uintptr
}
type message struct {
	HWND           uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	X, Y           int32
	Private        uint32
}

// amd64 INPUT is 40 bytes. Its union begins at offset 8; KEYBDINPUT.dwExtraInfo at 24.
type input struct {
	Type        uint32
	Pad         uint32
	VK, Scan    uint16
	Flags, Time uint32
	Pad2        uint32
	Extra       uintptr
	Tail        [8]byte
}

const bridgeMarker = 0x43425056

func (d *desktop) inject(t target) error {
	if !d.valid(t) || !keysReleased() || d.physicalV.Load() {
		return errors.New("target changed or keys still held")
	}
	events := []input{{Type: 1, VK: 0x12, Extra: bridgeMarker}, {Type: 1, VK: 0x56, Extra: bridgeMarker}, {Type: 1, VK: 0x56, Flags: 2, Extra: bridgeMarker}, {Type: 1, VK: 0x12, Flags: 2, Extra: bridgeMarker}}
	n, _, e := sendInput.Call(uintptr(len(events)), uintptr(unsafe.Pointer(&events[0])), unsafe.Sizeof(events[0]))
	runtime.KeepAlive(events)
	if n != 4 {
		// Release only modifiers which could have been inserted by a partial batch.
		if n > 0 {
			up := []input{{Type: 1, VK: 0x56, Flags: 2, Extra: bridgeMarker}, {Type: 1, VK: 0x12, Flags: 2, Extra: bridgeMarker}}
			sendInput.Call(2, uintptr(unsafe.Pointer(&up[0])), unsafe.Sizeof(up[0]))
		}
		return fmt.Errorf("SendInput inserted %d/4 events (UIPI may block): %v", n, e)
	}
	return nil
}
func (d *desktop) paste(ctx context.Context, t target) error {
	r := commit{target: t, ctx: ctx, result: make(chan error, 1)}
	select {
	case d.commits <- r:
	case <-ctx.Done():
		return ctx.Err()
	}
	// WM_APP+1: serialize final check/injection with hook and window events.
	ok, _, e := postThreadMessage.Call(uintptr(d.tid.Load()), 0x8001, 0, 0)
	if ok == 0 {
		return e
	}
	// Once queued, await the actual result; ctx is checked by caller before queueing.
	select {
	case err := <-r.result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *desktop) run(ctx context.Context) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if runtime.GOARCH != "amd64" {
		return errors.New("only Windows amd64 is supported")
	}
	d.tid.Store(windows.GetCurrentThreadId())
	var msg message
	peekMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0, 0)
	callback := windows.NewCallback(func(code int32, wp, lp uintptr) uintptr {
		next := func() uintptr { v, _, _ := callNextHook.Call(0, uintptr(code), wp, lp); return v }
		if code < 0 {
			return next()
		}
		k := (*keyboardEvent)(nativePointer(lp))
		if k.Flags&0x10 != 0 {
			return next()
		} // Synthetic replay never re-enters the bridge.
		down := wp == 0x100 || wp == 0x104
		if k.VK != 0x56 {
			if down && d.busy.Load() && k.VK != 0xa4 && k.VK != 0xa5 && k.VK != 0x12 {
				d.epoch.Add(1)
			}
			return next()
		}
		d.physicalV.Store(down)
		triggered := time.Now()
		allowed := false
		var t target
		if down && !d.gate.down && k.Flags&0x20 != 0 {
			t, allowed = d.snapshot()
			if !allowed {
				select {
				case d.ignored <- struct{}{}:
				default:
				}
			}
		}
		extra := keyDown(0x11) || keyDown(0x10) || keyDown(0x5b) || keyDown(0x5c)
		swallow, trigger := d.gate.key(down, k.Flags&0x20 != 0, extra, allowed, d.busy.Load())
		if trigger && d.busy.CompareAndSwap(false, true) {
			t.Triggered = triggered
			select {
			case d.requests <- t:
			default:
				d.busy.Store(false)
			}
		}
		if swallow {
			return 1
		}
		return next()
	})
	h, _, e := setWindowsHook.Call(13, callback, 0, 0)
	if h == 0 {
		return fmt.Errorf("SetWindowsHookEx: %v", e)
	}
	defer unhookWindowsHook.Call(h)
	eventCB := windows.NewCallback(func(hook uintptr, event uint32, hwnd uintptr, obj, child int32, thread, stamp uint32) uintptr {
		if !d.busy.Load() {
			return 0
		}
		if event == 3 || event == 0x8005 {
			d.epoch.Add(1)
		} else if event == 0x800c && d.c.Window.Mode == "title" {
			fg, _, _ := getForegroundWindow.Call()
			if hwnd == fg && obj == 0 {
				d.epoch.Add(1)
			}
		}
		return 0
	})
	for _, ev := range []uint32{3, 0x8005, 0x800c} {
		hook, _, err := setWinEventHook.Call(uintptr(ev), uintptr(ev), 0, eventCB, 0, 0, 2)
		if hook == 0 {
			return fmt.Errorf("SetWinEventHook: %v", err)
		}
		defer unhookWinEvent.Call(hook)
	}
	// Mouse activity during upload conservatively cancels even same-title tab switches.
	mouseCB := windows.NewCallback(func(code int32, wp, lp uintptr) uintptr {
		if code >= 0 && d.busy.Load() && (wp == 0x201 || wp == 0x204 || wp == 0x207 || wp == 0x20b || wp == 0x20a) {
			d.epoch.Add(1)
		}
		v, _, _ := callNextHook.Call(0, uintptr(code), wp, lp)
		return v
	})
	mh, _, e := setWindowsHook.Call(14, mouseCB, 0, 0)
	if mh == 0 {
		return fmt.Errorf("mouse cancellation hook: %v", e)
	}
	defer unhookWindowsHook.Call(mh)
	exited := make(chan struct{})
	defer close(exited)
	go func() {
		select {
		case <-ctx.Done():
			postThreadMessage.Call(uintptr(d.tid.Load()), 0x12, 0, 0)
		case <-exited:
		}
	}()
	for {
		r, _, err := getMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) == -1 {
			return err
		}
		if r == 0 {
			return nil
		}
		if msg.Message == 0x8001 {
			select {
			case req := <-d.commits:
				if req.ctx.Err() != nil {
					req.result <- req.ctx.Err()
				} else {
					req.result <- d.inject(req.target)
				}
			default:
			}
		}
	}
}

func feedback() { messageBeep.Call(0x30) }
