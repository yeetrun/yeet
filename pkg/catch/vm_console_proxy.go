// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"encoding/json"
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

type vmFullRestoreRequest struct {
	StatePath  string `json:"statePath"`
	MemoryPath string `json:"memoryPath"`
	Resume     bool   `json:"resume"`
}

type vmFullRestoreResult struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type vmSnapshotLoader interface {
	LoadSnapshot(context.Context, string, string, string, bool) error
}

type vmSnapshotLoaderFunc func(context.Context, string, string, string, bool) error

func (f vmSnapshotLoaderFunc) LoadSnapshot(ctx context.Context, socket, statePath, memoryPath string, resume bool) error {
	return f(ctx, socket, statePath, memoryPath, resume)
}

var (
	ErrVMGuestReboot       = errors.New("VM guest requested reboot")
	ErrVMRestoreLoadFailed = errors.New("VM full restore load failed")

	vmConsoleSnapshotLoader   vmSnapshotLoader = firecrackerSnapshotAPI{}
	vmConsoleWaitForAPISocket                  = waitForUnixSocket
)

const (
	VMGuestRebootExitCode       = 75
	VMRestoreLoadFailedExitCode = 76
	vmAPISocketWaitTimeout      = 10 * time.Second

	vmFullRestoreStatusSuccess = "success"
	vmFullRestoreStatusFailed  = "failed"
)

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

	requestPath := vmFullRestoreRequestPath(cfg.APISocket)
	resultPath := vmFullRestoreResultPath(cfg.APISocket)
	restoreRequest, restoreMode, err := readVMFullRestoreRequest(requestPath)
	if err != nil {
		return failVMRestoreLoadBeforeStart(resultPath, err)
	}
	cmd := vmFirecrackerCommand(ctx, cfg, restoreMode)
	console, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("start Firecracker console PTY: %w", err)
	}
	defer func() { _ = console.Close() }()

	guestStopped := make(chan vmGuestStopKind, 1)
	broker := newVMConsoleBroker(console, os.Stdout, guestStopped)
	go broker.accept(listener)
	go broker.copyOutput()
	if restoreMode {
		if err := completeVMFullRestoreStartup(ctx, cmd, cfg.APISocket, requestPath, resultPath, restoreRequest); err != nil {
			return err
		}
	}
	return waitVMConsoleProcess(cmd, guestStopped)
}

func failVMRestoreLoadBeforeStart(resultPath string, err error) error {
	_ = writeVMFullRestoreResult(resultPath, vmFullRestoreResult{Status: vmFullRestoreStatusFailed, Error: err.Error()})
	return fmt.Errorf("%w: %v", ErrVMRestoreLoadFailed, err)
}

func completeVMFullRestoreStartup(ctx context.Context, cmd *exec.Cmd, apiSocket, requestPath, resultPath string, request vmFullRestoreRequest) error {
	if err := loadFullVMSnapshot(ctx, apiSocket, request); err != nil {
		return failRunningVMRestoreLoad(cmd, resultPath, "", err)
	}
	if err := os.Remove(requestPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return failRunningVMRestoreLoad(cmd, resultPath, "consume full restore request", err)
	}
	if err := writeVMFullRestoreResult(resultPath, vmFullRestoreResult{Status: vmFullRestoreStatusSuccess}); err != nil {
		return failRunningVMRestoreLoad(cmd, resultPath, "write full restore result", err)
	}
	return nil
}

func failRunningVMRestoreLoad(cmd *exec.Cmd, resultPath, context string, err error) error {
	_ = writeVMFullRestoreResult(resultPath, vmFullRestoreResult{Status: vmFullRestoreStatusFailed, Error: err.Error()})
	stopVMConsoleProcess(cmd)
	if context != "" {
		return fmt.Errorf("%w: %s: %v", ErrVMRestoreLoadFailed, context, err)
	}
	return fmt.Errorf("%w: %v", ErrVMRestoreLoadFailed, err)
}

func stopVMConsoleProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()
}

func vmFirecrackerCommand(ctx context.Context, cfg VMConsoleProxyConfig, restoreMode bool) *exec.Cmd {
	if restoreMode {
		return exec.CommandContext(ctx, cfg.Firecracker, "--api-sock", cfg.APISocket)
	}
	return exec.CommandContext(ctx, cfg.Firecracker, "--api-sock", cfg.APISocket, "--config-file", cfg.ConfigFile)
}

func loadFullVMSnapshot(ctx context.Context, apiSocket string, request vmFullRestoreRequest) error {
	if err := vmConsoleWaitForAPISocket(ctx, apiSocket); err != nil {
		return err
	}
	return vmConsoleSnapshotLoader.LoadSnapshot(ctx, apiSocket, request.StatePath, request.MemoryPath, request.Resume)
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

func vmFullRestoreRequestPath(apiSocket string) string {
	return filepath.Join(filepath.Dir(strings.TrimSpace(apiSocket)), "firecracker-restore.json")
}

func vmFullRestoreResultPath(apiSocket string) string {
	return filepath.Join(filepath.Dir(strings.TrimSpace(apiSocket)), "firecracker-restore-result.json")
}

func writeVMFullRestoreRequest(path string, request vmFullRestoreRequest) error {
	if strings.TrimSpace(request.StatePath) == "" {
		return fmt.Errorf("full restore state path is required")
	}
	if strings.TrimSpace(request.MemoryPath) == "" {
		return fmt.Errorf("full restore memory path is required")
	}
	raw, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create full restore request directory: %w", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("set full restore request permissions: %w", err)
	}
	return nil
}

func readVMFullRestoreRequest(path string) (vmFullRestoreRequest, bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return vmFullRestoreRequest{}, false, nil
	}
	if err != nil {
		return vmFullRestoreRequest{}, false, fmt.Errorf("read full restore request: %w", err)
	}
	var request vmFullRestoreRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return vmFullRestoreRequest{}, false, fmt.Errorf("decode full restore request: %w", err)
	}
	if strings.TrimSpace(request.StatePath) == "" {
		return vmFullRestoreRequest{}, false, fmt.Errorf("full restore state path is required")
	}
	if strings.TrimSpace(request.MemoryPath) == "" {
		return vmFullRestoreRequest{}, false, fmt.Errorf("full restore memory path is required")
	}
	return request, true, nil
}

func writeVMFullRestoreResult(path string, result vmFullRestoreResult) error {
	if result.Status != vmFullRestoreStatusSuccess && result.Status != vmFullRestoreStatusFailed {
		return fmt.Errorf("invalid full restore result status %q", result.Status)
	}
	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create full restore result directory: %w", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("set full restore result permissions: %w", err)
	}
	return nil
}

func readVMFullRestoreResult(path string) (vmFullRestoreResult, bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return vmFullRestoreResult{}, false, nil
	}
	if err != nil {
		return vmFullRestoreResult{}, false, fmt.Errorf("read full restore result: %w", err)
	}
	var result vmFullRestoreResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return vmFullRestoreResult{}, true, fmt.Errorf("decode full restore result: %w", err)
	}
	if result.Status != vmFullRestoreStatusSuccess && result.Status != vmFullRestoreStatusFailed {
		return vmFullRestoreResult{}, true, fmt.Errorf("invalid full restore result status %q", result.Status)
	}
	return result, true, nil
}

func removeVMFullRestoreResult(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale full restore result: %w", err)
	}
	return nil
}

func waitForUnixSocket(ctx context.Context, socketPath string) error {
	deadline := time.Now().Add(vmAPISocketWaitTimeout)
	var lastErr error
	for {
		conn, err := net.DialTimeout("unix", socketPath, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("wait for Firecracker API socket %s: %w", socketPath, lastErr)
		}
		time.Sleep(25 * time.Millisecond)
	}
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
		strings.Contains(text, "reboot: power down"),
		strings.Contains(text, "reboot: power off not available: system halted instead"):
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
