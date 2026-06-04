// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/db"
)

func TestVMConsoleConnectsToUnixSocket(t *testing.T) {
	socketPath := filepath.Join(shortUnixSocketDirForTest(t), "console.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	defer listener.Close()

	served := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			served <- err
			return
		}
		defer conn.Close()
		_, err = conn.Write([]byte("login: "))
		served <- err
	}()

	server := newTestServer(t)
	if _, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, s *db.Service) error {
		s.ServiceType = db.ServiceTypeVM
		s.VM = &db.VMConfig{
			Console: db.VMConsoleConfig{SocketPath: socketPath},
		}
		return nil
	}); err != nil {
		t.Fatalf("seed VM service: %v", err)
	}

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx:      context.Background(),
		s:        server,
		sn:       "devbox",
		rw:       readWriter{Reader: strings.NewReader(""), Writer: &out},
		progress: catchrpc.ProgressQuiet,
	}
	if err := execer.vmConsoleCmdFunc(); err != nil {
		t.Fatalf("vmConsoleCmdFunc: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "login: ") {
		t.Fatalf("output = %q, want login prompt", got)
	}
	if got := out.String(); !strings.Contains(got, "Escape: press Enter, then type ~.") {
		t.Fatalf("output = %q, want escape hint", got)
	}
	if err := <-served; err != nil {
		t.Fatalf("console server write: %v", err)
	}
}

func TestVMConsoleFallsBackToJournalWhenSocketMissing(t *testing.T) {
	oldJournal := vmConsoleJournalFunc
	defer func() { vmConsoleJournalFunc = oldJournal }()

	called := false
	vmConsoleJournalFunc = func(e *ttyExecer) error {
		called = true
		if e.sn != "devbox" {
			t.Fatalf("journal service = %q, want devbox", e.sn)
		}
		return nil
	}

	server := newTestServer(t)
	if _, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, s *db.Service) error {
		s.ServiceType = db.ServiceTypeVM
		s.VM = &db.VMConfig{Console: db.VMConsoleConfig{SocketPath: filepath.Join(shortUnixSocketDirForTest(t), "missing.sock")}}
		return nil
	}); err != nil {
		t.Fatalf("seed VM service: %v", err)
	}

	execer := &ttyExecer{
		ctx:      context.Background(),
		s:        server,
		sn:       "devbox",
		rw:       &bytes.Buffer{},
		progress: catchrpc.ProgressQuiet,
	}
	if err := execer.vmConsoleCmdFunc(); err != nil {
		t.Fatalf("vmConsoleCmdFunc: %v", err)
	}
	if !called {
		t.Fatal("journal fallback was not called")
	}
}

func TestVMConsoleStopsWhenContextCanceled(t *testing.T) {
	socketPath := filepath.Join(shortUnixSocketDirForTest(t), "console.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	defer listener.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()

	server := newTestServer(t)
	if _, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, s *db.Service) error {
		s.ServiceType = db.ServiceTypeVM
		s.VM = &db.VMConfig{Console: db.VMConsoleConfig{SocketPath: socketPath}}
		return nil
	}); err != nil {
		t.Fatalf("seed VM service: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (&ttyExecer{ctx: ctx, s: server, sn: "devbox", rw: &bytes.Buffer{}}).vmConsoleCmdFunc()
	}()

	conn := <-accepted
	defer conn.Close()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("vmConsoleCmdFunc after cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("vmConsoleCmdFunc did not return after context cancellation")
	}
}

func TestCopyVMConsoleInputStopsOnEscapeSequence(t *testing.T) {
	var out bytes.Buffer
	err := copyVMConsoleInput(&out, strings.NewReader("echo hi\n~.\nignored"))
	if !errors.Is(err, errVMConsoleDetached) {
		t.Fatalf("copyVMConsoleInput error = %v, want detach", err)
	}
	if out.String() != "echo hi\n" {
		t.Fatalf("copied input = %q, want content before escape", out.String())
	}
}

func TestRunVMConsoleProxyBridgesPTYToSocket(t *testing.T) {
	dir := shortUnixSocketDirForTest(t)
	fakeFirecracker := filepath.Join(dir, "firecracker")
	script := `#!/bin/sh
printf 'fake-ready\n'
while IFS= read -r line; do
	printf 'got:%s\n' "$line"
	if [ "$line" = "quit" ]; then
		exit 0
	fi
done
`
	if err := os.WriteFile(fakeFirecracker, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake firecracker: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	socketPath := filepath.Join(dir, "serial.sock")
	done := make(chan error, 1)
	go func() {
		done <- RunVMConsoleProxy(ctx, VMConsoleProxyConfig{
			Firecracker:   fakeFirecracker,
			APISocket:     filepath.Join(dir, "firecracker.sock"),
			ConfigFile:    filepath.Join(dir, "firecracker.json"),
			ConsoleSocket: socketPath,
		})
	}()

	conn := dialUnixSocketForTest(t, socketPath)
	defer conn.Close()
	if _, err := conn.Write([]byte("hello\nquit\n")); err != nil {
		t.Fatalf("write console input: %v", err)
	}
	got := readUntilForTest(t, conn, "got:hello")
	if !strings.Contains(got, "got:hello") {
		t.Fatalf("console output = %q, want command response", got)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunVMConsoleProxy: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunVMConsoleProxy did not return after fake Firecracker exited")
	}
}

func TestRunVMConsoleProxyStopsWhenGuestHalts(t *testing.T) {
	dir := shortUnixSocketDirForTest(t)
	fakeFirecracker := filepath.Join(dir, "firecracker")
	script := `#!/bin/sh
printf '[ 1.0] reboot: System halted\n'
sleep 30
`
	if err := os.WriteFile(fakeFirecracker, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake firecracker: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := RunVMConsoleProxy(ctx, VMConsoleProxyConfig{
		Firecracker:   fakeFirecracker,
		APISocket:     filepath.Join(dir, "firecracker.sock"),
		ConfigFile:    filepath.Join(dir, "firecracker.json"),
		ConsoleSocket: filepath.Join(dir, "serial.sock"),
	})
	if err != nil {
		t.Fatalf("RunVMConsoleProxy: %v", err)
	}
}

func dialUnixSocketForTest(t *testing.T, socketPath string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", socketPath)
		if err == nil {
			return conn
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("dial unix socket %s: %v", socketPath, lastErr)
	return nil
}

func shortUnixSocketDirForTest(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "yeet-vm-console-*")
	if err != nil {
		t.Fatalf("create short unix socket dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})
	return dir
}

func readUntilForTest(t *testing.T, r io.Reader, want string) string {
	t.Helper()
	buf := make([]byte, 1024)
	var out strings.Builder
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if conn, ok := r.(interface{ SetReadDeadline(time.Time) error }); ok {
			_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		}
		n, err := r.Read(buf)
		if n > 0 {
			out.Write(buf[:n])
			if strings.Contains(out.String(), want) {
				return out.String()
			}
		}
		if err == nil {
			continue
		}
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			continue
		}
		t.Fatalf("read console output: %v; output so far %q", err, out.String())
	}
	t.Fatalf("timed out waiting for %q; output so far %q", want, out.String())
	return out.String()
}
