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
	"os/exec"
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
	inputReader, inputWriter := io.Pipe()
	t.Cleanup(func() { _ = inputReader.Close() })
	t.Cleanup(func() { _ = inputWriter.Close() })
	rw := readWriter{Reader: inputReader, Writer: io.Discard}
	done := make(chan error, 1)
	go func() {
		done <- (&ttyExecer{ctx: ctx, s: server, sn: "devbox", rw: rw}).vmConsoleCmdFunc()
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
	fakeJailer := filepath.Join(dir, "jailer")
	script := `#!/bin/sh
printf 'fake-ready\n'
while IFS= read -r line; do
	printf 'got:%s\n' "$line"
	if [ "$line" = "quit" ]; then
		exit 0
	fi
done
`
	if err := os.WriteFile(fakeJailer, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake jailer: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg := vmConsoleProxyConfigForTest(t, dir, fakeJailer)
	socketPath := cfg.ConsoleSocket
	launchedViaJailer := false
	processConstructor := func(ctx context.Context, cfg VMConsoleProxyConfig) (*exec.Cmd, func(), error) {
		launchedViaJailer = true
		if cfg.Jailer != fakeJailer || cfg.JailerBase == "" {
			return nil, func() {}, errors.New("console process missing mandatory jailer inputs")
		}
		return vmConsoleProcessForTest(ctx, cfg)
	}
	done := make(chan error, 1)
	go func() {
		done <- runVMConsoleProxyWithProcessConstructor(ctx, cfg, processConstructor)
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
		t.Fatal("RunVMConsoleProxy did not return after fake jailer exited")
	}
	if !launchedViaJailer {
		t.Fatal("VM console process was not launched through the jailer fixture")
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
	err := runVMConsoleProxyWithProcessConstructor(ctx, vmConsoleProxyConfigForTest(t, dir, fakeFirecracker), vmConsoleProcessForTest)
	if err != nil {
		t.Fatalf("RunVMConsoleProxy: %v", err)
	}
}

func TestVMGuestShutdownLogClassifiesShutdownKinds(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  vmGuestStopKind
	}{
		{name: "halt", input: []string{"[ 1.0] reboot: System halted\n"}, want: vmGuestStopHalt},
		{name: "power down", input: []string{"[ 1.0] reboot: Power down\n"}, want: vmGuestStopHalt},
		{name: "x86 firecracker poweroff halt", input: []string{"[ 1.0] reboot: Power off not available: System halted instead\n"}, want: vmGuestStopHalt},
		{name: "reboot", input: []string{"[ 1.0] reboot: Restarting system\n"}, want: vmGuestStopReboot},
		{name: "chunked reboot", input: []string{"[ 1.0] reboot: Restart", "ing system\n"}, want: vmGuestStopReboot},
		{name: "ordinary output", input: []string{"Welcome to Ubuntu\n"}, want: vmGuestStopNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var log vmGuestShutdownLog
			got := vmGuestStopNone
			for _, chunk := range tt.input {
				got = log.observe([]byte(chunk))
			}
			if got != tt.want {
				t.Fatalf("shutdown kind = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRunVMConsoleProxyReturnsRebootErrorWhenGuestRestarts(t *testing.T) {
	dir := shortUnixSocketDirForTest(t)
	fakeFirecracker := filepath.Join(dir, "firecracker")
	script := `#!/bin/sh
printf '[ 1.0] reboot: Restarting system\n'
sleep 30
`
	if err := os.WriteFile(fakeFirecracker, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake firecracker: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := runVMConsoleProxyWithProcessConstructor(ctx, vmConsoleProxyConfigForTest(t, dir, fakeFirecracker), vmConsoleProcessForTest)
	if !errors.Is(err, ErrVMGuestReboot) {
		t.Fatalf("RunVMConsoleProxy error = %v, want ErrVMGuestReboot", err)
	}
}

func TestRunVMConsoleProxyRunsRebootHookBeforeReturningReboot(t *testing.T) {
	dir := shortUnixSocketDirForTest(t)
	fakeFirecracker := filepath.Join(dir, "firecracker")
	script := `#!/bin/sh
printf '[ 1.0] reboot: Restarting system\n'
sleep 30
`
	if err := os.WriteFile(fakeFirecracker, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake firecracker: %v", err)
	}

	var hookCalled bool
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg := vmConsoleProxyConfigForTest(t, dir, fakeFirecracker)
	cfg.OnGuestReboot = func(_ context.Context, cfg VMConsoleProxyConfig) error {
		hookCalled = true
		if cfg.Service != "devbox" || cfg.ServiceRoot == "" || cfg.DiskPath == "" {
			t.Fatalf("reboot hook cfg = %#v, want service/root/disk", cfg)
		}
		return nil
	}
	err := runVMConsoleProxyWithProcessConstructor(ctx, cfg, vmConsoleProcessForTest)
	if !errors.Is(err, ErrVMGuestReboot) {
		t.Fatalf("RunVMConsoleProxy error = %v, want ErrVMGuestReboot", err)
	}
	if !hookCalled {
		t.Fatal("reboot hook was not called")
	}
}

func TestRunVMConsoleProxyStillReturnsRebootWhenRebootHookFails(t *testing.T) {
	dir := shortUnixSocketDirForTest(t)
	fakeFirecracker := filepath.Join(dir, "firecracker")
	script := `#!/bin/sh
printf '[ 1.0] reboot: Restarting system\n'
sleep 30
`
	if err := os.WriteFile(fakeFirecracker, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake firecracker: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg := vmConsoleProxyConfigForTest(t, dir, fakeFirecracker)
	cfg.OnGuestReboot = func(context.Context, VMConsoleProxyConfig) error {
		return errors.New("kernel sync failed")
	}
	err := runVMConsoleProxyWithProcessConstructor(ctx, cfg, vmConsoleProcessForTest)
	if !errors.Is(err, ErrVMGuestReboot) {
		t.Fatalf("RunVMConsoleProxy error = %v, want ErrVMGuestReboot despite hook failure", err)
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

func vmConsoleProxyConfigForTest(t *testing.T, dir, launcher string) VMConsoleProxyConfig {
	t.Helper()
	configPath := filepath.Join(dir, "firecracker.json")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write Firecracker config: %v", err)
	}
	serviceRoot := filepath.Join(dir, "service")
	return VMConsoleProxyConfig{
		Service:       "devbox",
		ServiceRoot:   serviceRoot,
		DiskPath:      filepath.Join(serviceRoot, "data", "rootfs.raw"),
		Firecracker:   launcher,
		Jailer:        launcher,
		JailerBase:    filepath.Join(dir, "jails"),
		APISocket:     filepath.Join(dir, "firecracker.sock"),
		ConfigFile:    configPath,
		ConsoleSocket: filepath.Join(dir, "serial.sock"),
	}
}

func vmConsoleProcessForTest(ctx context.Context, cfg VMConsoleProxyConfig) (*exec.Cmd, func(), error) {
	return exec.CommandContext(ctx, cfg.Jailer, "--api-sock", cfg.APISocket, "--config-file", cfg.ConfigFile), func() {}, nil
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
