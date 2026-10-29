//go:build windows

package main

import (
	"context"
	"testing"
	"time"
	"unsafe"
)

func TestKeyGate(t *testing.T) {
	g := keyGate{}
	if swallow, fire := g.key(true, true, false, false, false); swallow || fire {
		t.Fatal("other app intercepted")
	}
	g.key(false, false, false, false, false)
	if s, f := g.key(true, true, false, true, false); !s || !f {
		t.Fatal("target did not fire")
	}
	for i := 0; i < 100; i++ {
		if s, f := g.key(true, true, false, true, false); !s || f {
			t.Fatal("autorepeat uploaded")
		}
	}
	if s, f := g.key(false, false, false, false, false); !s || f {
		t.Fatal("matching release not swallowed")
	}
	if s, f := g.key(true, true, false, true, true); !s || f {
		t.Fatal("busy allowed reentry")
	}
	g.key(false, false, false, false, false)
	if s, f := g.key(true, true, true, true, false); s || f {
		t.Fatal("AltGr/extra modifier intercepted")
	}
}
func TestInputLayout(t *testing.T) {
	var i input
	if unsafe.Sizeof(i) != 40 || unsafe.Offsetof(i.VK) != 8 || unsafe.Offsetof(i.Extra) != 24 {
		t.Fatal("wrong INPUT layout")
	}
}
func TestNativeHooksInstallAndExit(t *testing.T) {
	c, e := loadConfig("config.example.json")
	if e != nil {
		t.Fatal(e)
	}
	d := newDesktop(c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { time.Sleep(150 * time.Millisecond); cancel() }()
	if e = d.run(ctx); e != nil {
		t.Fatal(e)
	}
	if d.tid.Load() == 0 {
		t.Fatal("message thread not started")
	}
}
func TestForeignWindowRejected(t *testing.T) {
	c, e := loadConfig("config.example.json")
	if e != nil {
		t.Fatal(e)
	}
	c.Window.Class = "CLIPBRIDGE_TEST_NONEXISTENT_CLASS"
	d := newDesktop(c)
	if _, ok := d.snapshot(); ok {
		t.Fatal("foreign app accepted")
	}
}
