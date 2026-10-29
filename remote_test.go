package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Real SSH transport, key exchange and channels; fake X selection endpoint permits
// deterministic authentication, corrupted readback, reconnect and timeout tests.
type sshFixture struct {
	mu                        sync.Mutex
	image                     []byte
	owners, peak, connections atomic.Int32
	corrupt, hang             atomic.Bool
}

func setupSSH(t *testing.T) (Config, *sshFixture) {
	t.Helper()
	pub, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	signer, e := ssh.NewSignerFromKey(key)
	if e != nil {
		t.Fatal(e)
	}
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = ln.Close() })
	sc := &ssh.ServerConfig{PublicKeyCallback: func(meta ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		want, _ := ssh.NewPublicKey(pub)
		if string(k.Marshal()) != string(want.Marshal()) {
			return nil, fmt.Errorf("denied")
		}
		return nil, nil
	}}
	sc.AddHostKey(signer)
	f := &sshFixture{}
	go func() {
		for {
			conn, e := ln.Accept()
			if e != nil {
				return
			}
			go func() {
				s, chans, reqs, e := ssh.NewServerConn(conn, sc)
				if e != nil {
					_ = conn.Close()
					return
				}
				defer s.Close()
				f.connections.Add(1)
				go func() {
					for r := range reqs {
						_ = r.Reply(true, nil)
					}
				}()
				for ch := range chans {
					if ch.ChannelType() != "session" {
						_ = ch.Reject(ssh.UnknownChannelType, "unsupported")
						continue
					}
					c, rs, e := ch.Accept()
					if e != nil {
						continue
					}
					go f.serve(c, rs)
				}
			}()
		}
	}()
	dir := t.TempDir()
	der, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		t.Fatal(e)
	}
	keyPath := filepath.Join(dir, "key")
	if e = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600); e != nil {
		t.Fatal(e)
	}
	kh := filepath.Join(dir, "known_hosts")
	if e = os.WriteFile(kh, []byte(knownhosts.Line([]string{ln.Addr().String()}, signer.PublicKey())+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	h, p, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(p)
	c := Config{Display: ":99", MaxImageBytes: 1 << 20, MaxPixels: 10000, TimeoutMS: 2000}
	c.SSH.Host = h
	c.SSH.Port = port
	c.SSH.User = "test"
	c.SSH.PrivateKey = keyPath
	c.SSH.KnownHosts = kh
	return c, f
}
func (f *sshFixture) serve(c ssh.Channel, rs <-chan *ssh.Request) {
	defer c.Close()
	for r := range rs {
		if r.Type != "exec" {
			_ = r.Reply(false, nil)
			continue
		}
		var cmd struct{ Command string }
		_ = ssh.Unmarshal(r.Payload, &cmd)
		_ = r.Reply(true, nil)
		status := uint32(0)
		if strings.Contains(cmd.Command, "coproc CB") {
			ns := regexp.MustCompile(`head -c (\d+)`).FindStringSubmatch(cmd.Command)
			if len(ns) != 2 {
				return
			}
			n, _ := strconv.Atoi(ns[1])
			if n > 1<<20 {
				return
			}
			b := make([]byte, n)
			if _, e := io.ReadFull(c, b); e != nil {
				return
			}
			f.mu.Lock()
			f.image = b
			f.mu.Unlock()
			active := f.owners.Add(1)
			for {
				peak := f.peak.Load()
				if active <= peak || f.peak.CompareAndSwap(peak, active) {
					break
				}
			}
			defer f.owners.Add(-1)
			_, _ = io.WriteString(c, "UPLOADED\n")
			scanner := bufio.NewScanner(c)
			for scanner.Scan() {
			}
		} else if strings.Contains(cmd.Command, "sha256sum") {
			if f.hang.Load() {
				_, _ = io.Copy(io.Discard, c)
				return
			}
			f.mu.Lock()
			hash := sha256.Sum256(f.image)
			f.mu.Unlock()
			if f.corrupt.Load() {
				hash[0] ^= 255
			}
			_, _ = fmt.Fprintf(c, "%x  -\n", hash)
		}
		_, _ = c.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
		return
	}
}
func TestSSHReuseReplacementAndReconnect(t *testing.T) {
	c, f := setupSSH(t)
	r, e := newRemote(c)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	for i := 0; i < 20; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		e = r.Upload(ctx, []byte(fmt.Sprintf("unique PNG fixture %d", i)), &Timings{})
		cancel()
		if e != nil {
			t.Fatal(e)
		}
	}
	if f.connections.Load() != 1 || f.peak.Load() != 1 {
		t.Fatalf("connections=%d peak=%d", f.connections.Load(), f.peak.Load())
	}
	r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if e = r.Upload(ctx, []byte("after reconnect"), &Timings{}); e != nil {
		t.Fatal(e)
	}
	if f.connections.Load() != 2 {
		t.Fatal("no reconnect")
	}
}
func TestSSHCorruptAndTimeoutFailClosed(t *testing.T) {
	for _, hang := range []bool{false, true} {
		t.Run(fmt.Sprint(hang), func(t *testing.T) {
			c, f := setupSSH(t)
			r, e := newRemote(c)
			if e != nil {
				t.Fatal(e)
			}
			defer r.Close()
			f.corrupt.Store(!hang)
			f.hang.Store(hang)
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			if e = r.Upload(ctx, []byte("PNG"), &Timings{}); e == nil {
				t.Fatal("unverified image accepted")
			}
			if r.client != nil || r.owner != nil {
				t.Fatal("failed transport retained")
			}
		})
	}
}
func TestSSHHostKeyMismatch(t *testing.T) {
	c, _ := setupSSH(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := ssh.NewPublicKey(pub)
	addr := net.JoinHostPort(c.SSH.Host, strconv.Itoa(c.SSH.Port))
	if e := os.WriteFile(c.SSH.KnownHosts, []byte(knownhosts.Line([]string{addr}, key)+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	r, e := newRemote(c)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if e = r.Keepalive(ctx); e == nil || !strings.Contains(e.Error(), "key mismatch") {
		t.Fatalf("unexpected error %v", e)
	}
}
