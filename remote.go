package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

// The same SSH stdin carries exactly N image bytes followed by renewable lease lines.
// A foreground xclip owns CLIPBOARD. EOF, lease expiry, or normal replacement reaps it.
func ownerCommand(c Config, n int) string {
	script := fmt.Sprintf(`set -eu
pid=
cleanup() { if [[ -n "$pid" ]]; then kill "$pid" 2>/dev/null || :; wait "$pid" 2>/dev/null || :; fi; }
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
coproc CB { exec xclip -display %s -selection clipboard -target image/png -in -quiet -loops 0 >/dev/null 2>/dev/null; }
pid=$CB_PID
exec 3>&${CB[1]}
exec {CB[1]}>&-
timeout %ds head -c %d >&3
exec 3>&-
printf 'UPLOADED\n'
while IFS= read -r -t 15 line; do
  [[ "$line" == keepalive ]] || break
  kill -0 "$pid" 2>/dev/null || break
done
`, quote(c.Display), c.TimeoutMS/1000+15, n)
	return "bash -c " + quote(script)
}

func verifyCommand(c Config) string {
	x := "xclip -display " + quote(c.Display) + " -selection clipboard -out -target "
	// pipefail prevents sha256sum(empty) being mistaken for a successful read.
	s := `set -euo pipefail
targets=$(timeout 2s ` + x + `TARGETS)
grep -Fxq image/png <<< "$targets"
timeout 2s ` + x + `image/png | sha256sum
`
	return "bash -c " + quote(s)
}

type owner struct {
	session   *ssh.Session
	in        io.WriteCloser
	done      chan error
	stop      chan struct{}
	heartDone chan struct{}
	mu        sync.Mutex
	once      sync.Once
}

func (o *owner) stopInput() {
	o.once.Do(func() { close(o.stop); o.mu.Lock(); _ = o.in.Close(); o.mu.Unlock() })
}
func (o *owner) heartbeat() {
	defer close(o.heartDone)
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-o.stop:
			return
		case <-t.C:
			o.mu.Lock()
			_, e := io.WriteString(o.in, "keepalive\n")
			o.mu.Unlock()
			if e != nil {
				return
			}
		}
	}
}

type Remote struct {
	c      Config
	auth   *ssh.ClientConfig
	mu     sync.Mutex
	client *ssh.Client
	owner  *owner
}

func newRemote(c Config) (*Remote, error) {
	key, err := os.ReadFile(c.SSH.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	var signer ssh.Signer
	if c.SSH.PassphraseEnv != "" {
		pass := os.Getenv(c.SSH.PassphraseEnv)
		if pass == "" {
			return nil, errors.New("private key passphrase environment variable is empty")
		}
		signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte(pass))
	} else {
		signer, err = ssh.ParsePrivateKey(key)
	}
	clear(key)
	if err != nil {
		return nil, errors.New("cannot parse private key (encrypted key requires passphrase_env)")
	}
	cb, err := knownhosts.New(c.SSH.KnownHosts)
	if err != nil {
		return nil, fmt.Errorf("known_hosts: %w", err)
	}
	return &Remote{c: c, auth: &ssh.ClientConfig{User: c.SSH.User, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: cb, Timeout: 5 * time.Second}}, nil
}

func (r *Remote) connect(ctx context.Context) error {
	if r.client != nil {
		return nil
	}
	addr := net.JoinHostPort(r.c.SSH.Host, strconv.Itoa(r.c.SSH.Port))
	conn, e := (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 10 * time.Second}).DialContext(ctx, "tcp", addr)
	if e != nil {
		return e
	}
	deadline := time.Now().Add(5 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	cc, ch, req, e := ssh.NewClientConn(conn, addr, r.auth)
	stop()
	if e != nil {
		_ = conn.Close()
		return e
	}
	_ = conn.SetDeadline(time.Time{})
	r.client = ssh.NewClient(cc, ch, req)
	return nil
}

// Run one operation with a watchdog which closes the transport to unblock SSH I/O.
// All operations are serialized; a timeout invalidates every channel and never replays input.
func (r *Remote) withClient(ctx context.Context, fn func() error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.connect(ctx); err != nil {
		return err
	}
	client := r.client
	stop := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		select {
		case <-ctx.Done():
			_ = client.Close()
		case <-stop:
		}
	}()
	err := fn()
	close(stop)
	<-finished
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		r.drop()
	}
	return err
}

func (r *Remote) drop() {
	if r.client != nil {
		_ = r.client.Close()
		r.client = nil
	}
	if r.owner != nil {
		r.owner.stopInput()
		_ = r.owner.session.Close()
		r.owner = nil
	}
}
func (r *Remote) Close() { r.mu.Lock(); defer r.mu.Unlock(); r.drop() }

type Timings struct {
	DetectMS       float64 `json:"tui_detect_ms"`
	QueueMS        float64 `json:"queue_ms"`
	ReadMS         float64 `json:"clipboard_read_ms"`
	EncodeMS       float64 `json:"png_encode_ms"`
	ConnectMS      float64 `json:"ssh_connect_ms"`
	OwnerPrepareMS float64 `json:"owner_prepare_ms"`
	TransferMS     float64 `json:"ssh_transfer_ms"`
	WriteMS        float64 `json:"xclip_write_ms"`
	VerifyMS       float64 `json:"clipboard_verify_ms"`
	ReleaseMS      float64 `json:"key_release_ms"`
	SendMS         float64 `json:"send_input_ms"`
	TotalMS        float64 `json:"total_ms"`
}

func ms(t time.Time) float64 { return float64(time.Since(t)) / float64(time.Millisecond) }

func (r *Remote) Upload(ctx context.Context, png []byte, m *Timings) error {
	start := time.Now()
	return r.withClient(ctx, func() error {
		m.ConnectMS = ms(start)
		prepareStart := time.Now()
		if r.owner != nil {
			o := r.owner
			o.stopInput()
			select {
			case <-o.done:
			case <-ctx.Done():
				return ctx.Err()
			}
			_ = o.session.Close()
			r.owner = nil
		}
		s, e := r.client.NewSession()
		if e != nil {
			return e
		}
		in, e := s.StdinPipe()
		if e != nil {
			_ = s.Close()
			return e
		}
		out, e := s.StdoutPipe()
		if e != nil {
			_ = s.Close()
			return e
		}
		s.Stderr = io.Discard
		if e = s.Start(ownerCommand(r.c, len(png))); e != nil {
			_ = s.Close()
			return e
		}
		o := &owner{session: s, in: in, done: make(chan error, 1), stop: make(chan struct{}), heartDone: make(chan struct{})}
		r.owner = o
		go func() { o.done <- s.Wait() }()
		m.OwnerPrepareMS = ms(prepareStart)
		// Heartbeats start only after the length-framed payload and acknowledgment.
		t := time.Now()
		_, e = io.Copy(in, bytes.NewReader(png))
		m.TransferMS = ms(t)
		if e != nil {
			return e
		}
		t = time.Now()
		line, e := bufio.NewReader(io.LimitReader(out, 128)).ReadString('\n')
		m.WriteMS = ms(t)
		if e != nil || line != "UPLOADED\n" {
			return errors.New("xclip upload acknowledgment missing")
		}
		go o.heartbeat()
		return r.verify(ctx, png, m)
	})
}

func (r *Remote) verify(ctx context.Context, png []byte, m *Timings) error {
	t := time.Now()
	defer func() { m.VerifyMS += ms(t) }()
	want := fmt.Sprintf("%x", sha256.Sum256(png))
	for {
		s, e := r.client.NewSession()
		if e != nil {
			return e
		}
		b := &cappedBuffer{max: 4096}
		s.Stdout = b
		s.Stderr = io.Discard
		e = s.Run(verifyCommand(r.c))
		_ = s.Close()
		fields := strings.Fields(b.String())
		if e == nil && len(fields) == 2 && fields[0] == want && fields[1] == "-" {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("clipboard MIME/SHA-256 verification: %w", ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}
func (r *Remote) Verify(ctx context.Context, png []byte, m *Timings) error {
	return r.withClient(ctx, func() error { return r.verify(ctx, png, m) })
}

func (r *Remote) Probe(ctx context.Context) (string, error) {
	var result string
	err := r.withClient(ctx, func() error {
		s, e := r.client.NewSession()
		if e != nil {
			return e
		}
		defer s.Close()
		b := &cappedBuffer{max: 16384}
		s.Stdout = b
		s.Stderr = io.Discard
		e = s.Run("bash -c " + quote("set -eu; command -v bash xclip head timeout sha256sum grep; xclip -version 2>&1; DISPLAY="+quote(r.c.Display)+" timeout 3s xclip -selection clipboard -out -target TARGETS 2>/dev/null || :; ps -u \"$(id -u)\" -o pid=,comm= | awk '$2 == \"xclip\" { print }'"))
		result = b.String()
		return e
	})
	return result, err
}

func (r *Remote) Keepalive(ctx context.Context) error {
	return r.withClient(ctx, func() error { _, _, e := r.client.SendRequest("keepalive@openssh.com", true, nil); return e })
}
