//go:build windows

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"time"

	"golang.org/x/sys/windows"
)

func main() {
	if err := run(); err != nil {
		slog.Error("clipbridge stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	path := flag.String("config", "config.json", "JSON configuration")
	probe := flag.Bool("probe", false, "read-only SSH/X11 prerequisite diagnostics")
	selftest := flag.Int("selftest", 0, "upload N generated images, verify, and report latency; replaces remote clipboard")
	metrics := flag.String("metrics", "", "optional JSONL timings file; contains no image bytes")
	logPath := flag.String("log-file", "", "optional diagnostic log file")
	flag.Parse()
	c, err := loadConfig(*path)
	if err != nil {
		return err
	}
	level := slog.LevelInfo
	_ = level.UnmarshalText([]byte(c.LogLevel))
	var out = os.Stderr
	if *logPath != "" {
		f, e := os.OpenFile(*logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		defer f.Close()
		out = f
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(out, &slog.HandlerOptions{Level: level})))
	r, err := newRemote(c)
	if err != nil {
		return err
	}
	defer r.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if *probe {
		p, e := context.WithTimeout(ctx, time.Duration(c.TimeoutMS)*time.Millisecond)
		defer e()
		s, err := r.Probe(p)
		fmt.Print(s)
		return err
	}
	if *selftest > 0 {
		return remoteSelftest(ctx, r, *selftest, *metrics)
	}
	// One bridge per interactive user session, including different config files.
	name, _ := windows.UTF16PtrFromString("Local\\CodexClipboardBridge")
	mutex, e := windows.CreateMutex(nil, false, name)
	if e != nil {
		if mutex != 0 {
			windows.CloseHandle(mutex)
		}
		return fmt.Errorf("another bridge may already be running: %w", e)
	}
	defer windows.CloseHandle(mutex)
	d := newDesktop(c)
	if c.Window.Mode == "codex_tui" {
		d.inspector = newTUIInspector(ctx)
	}
	var mf *os.File
	if *metrics != "" {
		mf, err = os.OpenFile(*metrics, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer mf.Close()
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case t := <-d.requests:
				handlePaste(ctx, c, d, r, t, mf)
			case <-d.ignored:
				slog.Debug("Alt+V passed through: foreground process/class/title did not match configuration")
			}
		}
	}()
	// Keep transport fresh without opening any new listener or clipboard watcher.
	keepDone := make(chan struct{})
	go func() {
		defer close(keepDone)
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if !d.busy.Load() {
					p, stop := context.WithTimeout(ctx, 3*time.Second)
					e := r.Keepalive(p)
					stop()
					if e != nil {
						slog.Debug("SSH keepalive/reconnect pending", "error", e)
					}
				}
			}
		}
	}()
	slog.Info("ready: Alt+V in configured Terminal session; Ctrl+C to exit", "mode", c.Window.Mode, "title_regex", c.Window.TitleRegex)
	err = d.run(ctx)
	cancel()
	r.Close()
	<-done
	<-keepDone
	return err
}

func handlePaste(parent context.Context, c Config, d *desktop, r *Remote, t target, mf *os.File) {
	defer d.busy.Store(false)
	start := t.Triggered
	if start.IsZero() {
		start = time.Now()
	}
	m := Timings{}
	m.QueueMS = ms(start)
	ctx, cancel := context.WithTimeout(parent, time.Duration(c.TimeoutMS)*time.Millisecond)
	defer cancel()
	if c.Window.Mode == "codex_tui" {
		// No Windows clipboard access until this focused pane is recognized.
		tm := time.Now()
		p, err := d.inspect(ctx, t)
		m.DetectMS = ms(tm)
		if err != nil {
			feedback()
			slog.Warn("TUI detection failed; no paste replay", "error", err)
			return
		}
		t.PaneID = p.ID
		slog.Debug("focused Terminal pane inspected", "evidence", p.Evidence, "codex_tui", p.Codex)
		if !p.Codex {
			// Preserve native Alt+V in other Terminal applications without uploading.
			if err = d.waitRelease(ctx, t); err == nil {
				var current paneSnapshot
				current, err = d.inspect(ctx, t)
				if err == nil && (current.ID != p.ID || current.Codex) {
					err = fmt.Errorf("Terminal pane changed before native pass-through")
				}
			}
			if err == nil {
				err = d.paste(ctx, t)
			}
			if err != nil {
				feedback()
				slog.Warn("native Alt+V cancelled", "error", err)
			} else {
				slog.Debug("native Alt+V restored in non-Codex Terminal pane; clipboard not accessed")
			}
			return
		}
	}
	// Cancel remote I/O promptly when a focus/title/interaction event invalidates target.
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		tick := time.NewTicker(15 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-watchDone:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
				if !d.valid(t) {
					cancel()
					return
				}
			}
		}
	}()
	err := func() error {
		if !d.valid(t) {
			return fmt.Errorf("target changed before clipboard read")
		}
		tm := time.Now()
		raw, e := readClipboard(ctx, c)
		m.ReadMS = ms(tm)
		if e != nil {
			return imageError("clipboard", e)
		}
		tm = time.Now()
		png, e := normalize(raw, c)
		m.EncodeMS = ms(tm)
		if e != nil {
			return imageError("PNG", e)
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		if e = r.Upload(ctx, png, &m); e != nil {
			return imageError("upload/verify", e)
		}
		tm = time.Now()
		if e = d.waitRelease(ctx, t); e != nil {
			return e
		}
		m.ReleaseMS = ms(tm)
		// The image may have been replaced while the physical Alt key was held.
		if e = r.Verify(ctx, png, &m); e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		if c.Window.Mode == "codex_tui" {
			tm = time.Now()
			pane, err := d.inspect(ctx, t)
			m.DetectMS += ms(tm)
			if err != nil {
				return err
			}
			if pane.ID != t.PaneID || !pane.Codex {
				return fmt.Errorf("focused pane or Codex TUI changed; paste cancelled")
			}
		}
		tm = time.Now()
		e = d.paste(ctx, t)
		m.SendMS = ms(tm)
		return e
	}()
	m.TotalMS = ms(start)
	if err != nil {
		r.Close()
		feedback()
		slog.Warn("paste failed or cancelled", "error", err, "timings", m)
	} else {
		slog.Info("paste replayed after PNG verification", "timings", m)
	}
	if mf != nil {
		_ = json.NewEncoder(mf).Encode(struct {
			Success bool `json:"success"`
			Timings
		}{err == nil, m})
	}
}

func (d *desktop) inspect(ctx context.Context, t target) (paneSnapshot, error) {
	if d.inspector == nil {
		return paneSnapshot{}, fmt.Errorf("TUI inspector is unavailable")
	}
	if !d.valid(t) {
		return paneSnapshot{}, fmt.Errorf("target changed before TUI detection")
	}
	p, stop := context.WithTimeout(ctx, 2*time.Second)
	defer stop()
	pane, err := d.inspector.read(p, t)
	if err == nil && !d.valid(t) {
		err = fmt.Errorf("target changed during TUI detection")
	}
	return pane, err
}
