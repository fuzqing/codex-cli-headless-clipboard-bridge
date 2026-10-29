package main

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"os"
	"testing"
	"time"
)

// Opt-in integration test: changes only the configured remote X11 clipboard.
func TestLiveRemoteLifecycle(t *testing.T) {
	path := os.Getenv("CLIPBRIDGE_REMOTE_CONFIG")
	if path == "" {
		t.Skip("set CLIPBRIDGE_REMOTE_CONFIG for real Debian X11 tests")
	}
	c, e := loadConfig(path)
	if e != nil {
		t.Fatal(e)
	}
	r, e := newRemote(c)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	im := image.NewNRGBA(image.Rect(0, 0, 128, 96))
	im.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 127})
	var b bytes.Buffer
	if e = encodeFixture(&b, im); e != nil {
		t.Fatal(e)
	}
	upload := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if e := r.Upload(ctx, b.Bytes(), &Timings{}); e != nil {
			t.Fatal(e)
		}
	}
	waitZero := func(timeout time.Duration) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		for {
			n, e := r.ownerCount(ctx)
			if e != nil {
				t.Fatal(e)
			}
			if n == 0 {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatalf("xclip not reaped (count %d)", n)
			case <-time.After(30 * time.Millisecond):
			}
		}
	}
	upload()
	// Keep image for longer than a lease, proving heartbeat renewal works.
	deadline := time.NewTimer(16 * time.Second)
	defer deadline.Stop()
	<-deadline.C
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	e = r.Verify(ctx, b.Bytes(), &Timings{})
	cancel()
	if e != nil {
		t.Fatalf("lease not renewed: %v", e)
	}
	t.Log("heartbeat retained verified image beyond 15s lease")
	// Stop renewal without sending EOF: an unresponsive client must lose its owner.
	r.mu.Lock()
	o := r.owner
	o.once.Do(func() { close(o.stop) })
	r.mu.Unlock()
	<-o.heartDone
	waitZero(17 * time.Second)
	t.Log("lease expiry reaped xclip without stdin EOF")
	r.Close()
	upload()
	r.Close()
	waitZero(4 * time.Second)
	t.Log("transport closure reaped xclip")
	upload()
	ctx, cancel = context.WithTimeout(context.Background(), 400*time.Millisecond)
	e = r.Verify(ctx, []byte("wrong hash"), &Timings{})
	cancel()
	if e == nil {
		t.Fatal("wrong readback accepted")
	}
	waitZero(4 * time.Second)
	t.Log("mismatch failed closed and reaped owner")
	r.Close()
}
