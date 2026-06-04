// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

type VMConsoleProxyConfig struct {
	Firecracker   string
	APISocket     string
	ConfigFile    string
	ConsoleSocket string
}

var ErrVMGuestReboot = errors.New("VM guest requested reboot")

const VMGuestRebootExitCode = 75

type vmGuestStopKind int

const (
	vmGuestStopNone vmGuestStopKind = iota
	vmGuestStopHalt
	vmGuestStopReboot
)

func RunVMConsoleProxy(ctx context.Context, cfg VMConsoleProxyConfig) error {
	if err := validateVMConsoleProxyConfig(cfg); err != nil {
		return err
	}
	listener, err := listenVMConsoleSocket(cfg.ConsoleSocket)
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()

	cmd := exec.CommandContext(ctx, cfg.Firecracker, "--api-sock", cfg.APISocket, "--config-file", cfg.ConfigFile)
	console, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("start Firecracker console PTY: %w", err)
	}
	defer func() { _ = console.Close() }()

	guestStopped := make(chan vmGuestStopKind, 1)
	broker := newVMConsoleBroker(console, os.Stdout, guestStopped)
	go broker.accept(listener)
	go broker.copyOutput()
	return waitVMConsoleProcess(cmd, guestStopped)
}

func listenVMConsoleSocket(socketPath string) (net.Listener, error) {
	if err := os.RemoveAll(socketPath); err != nil {
		return nil, fmt.Errorf("remove stale VM console socket: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		return nil, fmt.Errorf("prepare VM console socket directory: %w", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen VM console socket: %w", err)
	}
	if err := os.Chmod(socketPath, 0o755); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("chmod VM console socket: %w", err)
	}
	return listener, nil
}

func waitVMConsoleProcess(cmd *exec.Cmd, guestStopped <-chan vmGuestStopKind) error {
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
	}()

	select {
	case kind := <-guestStopped:
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		err := <-waitDone
		return vmGuestStopError(kind, err)
	case err := <-waitDone:
		select {
		case kind := <-guestStopped:
			return vmGuestStopError(kind, err)
		default:
		}
		if err != nil {
			return fmt.Errorf("wait for Firecracker: %w", err)
		}
		return nil
	}
}

func vmGuestStopError(kind vmGuestStopKind, err error) error {
	switch kind {
	case vmGuestStopReboot:
		return ErrVMGuestReboot
	case vmGuestStopHalt:
		return nil
	}
	if err != nil {
		return fmt.Errorf("wait for Firecracker: %w", err)
	}
	return nil
}

func validateVMConsoleProxyConfig(cfg VMConsoleProxyConfig) error {
	if cfg.Firecracker == "" {
		return fmt.Errorf("firecracker path is required")
	}
	if cfg.APISocket == "" {
		return fmt.Errorf("api socket path is required")
	}
	if cfg.ConfigFile == "" {
		return fmt.Errorf("config file path is required")
	}
	if cfg.ConsoleSocket == "" {
		return fmt.Errorf("console socket path is required")
	}
	return nil
}

type vmConsoleBroker struct {
	pty     *os.File
	output  io.Writer
	mu      sync.Mutex
	clients map[net.Conn]struct{}

	guestStopped  chan vmGuestStopKind
	guestStopOnce sync.Once
	shutdownLog   vmGuestShutdownLog
}

func newVMConsoleBroker(console *os.File, output io.Writer, guestStopped chan vmGuestStopKind) *vmConsoleBroker {
	if output == nil {
		output = io.Discard
	}
	return &vmConsoleBroker{
		pty:          console,
		output:       output,
		clients:      map[net.Conn]struct{}{},
		guestStopped: guestStopped,
	}
}

func (b *vmConsoleBroker) accept(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		b.add(conn)
		go b.copyInput(conn)
	}
}

func (b *vmConsoleBroker) add(conn net.Conn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.clients[conn] = struct{}{}
}

func (b *vmConsoleBroker) remove(conn net.Conn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.clients, conn)
	_ = conn.Close()
}

func (b *vmConsoleBroker) copyInput(conn net.Conn) {
	defer b.remove(conn)
	_, _ = io.Copy(b.pty, conn)
}

func (b *vmConsoleBroker) copyOutput() {
	buf := make([]byte, 32*1024)
	for {
		n, err := b.pty.Read(buf)
		if n > 0 {
			b.write(buf[:n])
		}
		if err != nil {
			b.closeClients()
			return
		}
	}
}

func (b *vmConsoleBroker) write(p []byte) {
	_, _ = b.output.Write(p)
	if kind := b.shutdownLog.observe(p); kind != vmGuestStopNone {
		b.guestStopOnce.Do(func() {
			b.guestStopped <- kind
			close(b.guestStopped)
		})
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for conn := range b.clients {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write(p); err != nil {
			delete(b.clients, conn)
			_ = conn.Close()
		}
	}
}

type vmGuestShutdownLog struct {
	tail string
}

func (l *vmGuestShutdownLog) observe(p []byte) vmGuestStopKind {
	text := l.tail + string(p)
	if kind := vmGuestShutdownKind(text); kind != vmGuestStopNone {
		return kind
	}
	l.tail = vmGuestShutdownTail(text)
	return vmGuestStopNone
}

func vmGuestShutdownKind(text string) vmGuestStopKind {
	text = strings.ToLower(text)
	switch {
	case strings.Contains(text, "reboot: restarting system"):
		return vmGuestStopReboot
	case strings.Contains(text, "reboot: system halted"),
		strings.Contains(text, "reboot: power down"):
		return vmGuestStopHalt
	default:
		return vmGuestStopNone
	}
}

func vmGuestShutdownTail(text string) string {
	const maxTail = 256
	if len(text) <= maxTail {
		return text
	}
	return text[len(text)-maxTail:]
}

func (b *vmConsoleBroker) closeClients() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for conn := range b.clients {
		delete(b.clients, conn)
		_ = conn.Close()
	}
}
