// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
)

var errVMConsoleDetached = errors.New("VM console detached")

var runVMCmdFunc = func(e *ttyExecer, flags cli.RunFlags, payload string) error {
	return e.runVM(flags, payload)
}

var vmConsoleJournalFunc = func(e *ttyExecer) error {
	return e.vmConsoleJournalCmdFunc()
}

func isVMImagePayload(payload string) bool {
	return strings.HasPrefix(strings.TrimSpace(payload), vmImagePayloadPrefix)
}

func (e *ttyExecer) runVM(flags cli.RunFlags, payload string) error {
	return e.provisionVM(flags, payload)
}

func (e *ttyExecer) vmCmdFunc(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("vm requires a subcommand")
	}
	switch args[0] {
	case "console":
		return e.vmConsoleRemoteCmdFunc(args[1:])
	case "set":
		return e.vmSetCmdFunc(args[1:])
	case "images":
		return e.vmImagesRemoteCmdFunc(args[1:])
	case "kernel":
		return e.vmKernelRemoteCmdFunc(args[1:])
	case "memory":
		return e.vmMemoryRemoteCmdFunc(args[1:])
	case "runtime":
		return e.vmRuntimeRemoteCmdFunc(args[1:])
	default:
		return fmt.Errorf("unknown vm command %q", args[0])
	}
}

func (e *ttyExecer) vmRuntimeRemoteCmdFunc(args []string) error {
	logicalArgs := vmRuntimeLogicalArgs(e.sn, args)
	flags, remaining, err := cli.ParseVMRuntime(logicalArgs)
	if err != nil {
		return err
	}
	return e.vmRuntimeCmdFunc(flags, remaining)
}

func vmRuntimeLogicalArgs(service string, args []string) []string {
	if service == "" || service == SystemService || len(args) == 0 {
		return append([]string(nil), args...)
	}
	action, actionIndex, ok := cli.FindVMRuntimeAction(args)
	if !ok {
		return append([]string(nil), args...)
	}
	insertAt := actionIndex + 1
	switch action {
	case cli.VMRuntimeActionStatus, cli.VMRuntimeActionUpgrade, cli.VMRuntimeActionRollback:
	case cli.VMRuntimeActionPolicy:
		// Runtime policy defaults are always routed to the system service, so a
		// non-system target is necessarily the per-VM form with its VM removed.
	default:
		insertAt = -1
	}
	if insertAt < 0 {
		return append([]string(nil), args...)
	}
	result := make([]string, 0, len(args)+1)
	result = append(result, args[:insertAt]...)
	result = append(result, service)
	result = append(result, args[insertAt:]...)
	return result
}

func (e *ttyExecer) vmRuntimeCmdFunc(flags cli.VMRuntimeFlags, remaining []string) error {
	action, err := vmRuntimeCommandAction(remaining)
	if err != nil {
		return err
	}
	switch action {
	case cli.VMRuntimeActionStatus:
		return e.vmRuntimeStatusCmd(flags)
	case cli.VMRuntimeActionUpdate:
		return e.s.updateVMRuntimes(e.ctx, e.rw)
	case cli.VMRuntimeActionImport:
		return e.vmRuntimeImportCmd(remaining)
	case cli.VMRuntimeActionPolicy:
		return e.vmRuntimePolicyCmd(flags, remaining)
	case cli.VMRuntimeActionProtect:
		return e.s.setVMRuntimeProtection(e.ctx, e.rw, remaining[1], true)
	case cli.VMRuntimeActionUnprotect:
		return e.s.setVMRuntimeProtection(e.ctx, e.rw, remaining[1], false)
	default:
		return e.vmRuntimeLifecycleCmd(action, flags, remaining)
	}
}

func (e *ttyExecer) vmRuntimeLifecycleCmd(action string, flags cli.VMRuntimeFlags, remaining []string) error {
	switch action {
	case cli.VMRuntimeActionUpgrade:
		return e.vmRuntimeUpgradeCmd(flags, remaining[1])
	case cli.VMRuntimeActionRollback:
		return e.s.rollbackVMRuntime(e.ctx, e.rw, remaining[1], flags.Restart)
	case cli.VMRuntimeActionPrune:
		return e.s.pruneVMRuntimes(e.ctx, e.rw, flags.DryRun)
	default:
		return fmt.Errorf("unknown vm runtime action %q", action)
	}
}

func vmRuntimeCommandAction(remaining []string) (string, error) {
	if len(remaining) == 0 {
		return "", fmt.Errorf("vm runtime requires an action")
	}
	return remaining[0], nil
}

func (e *ttyExecer) vmRuntimeStatusCmd(flags cli.VMRuntimeFlags) error {
	service := ""
	if e.sn != "" && e.sn != SystemService {
		service = e.sn
	}
	return e.s.printVMRuntimeStatus(e.ctx, e.rw, service, flags.Format)
}

func (e *ttyExecer) vmRuntimeImportCmd(remaining []string) error {
	if remaining[2] != "-" {
		return fmt.Errorf("vm runtime import source must be the authenticated input stream")
	}
	return e.s.importVMRuntime(e.ctx, e.rw, remaining[1], e.payloadReader())
}

func (e *ttyExecer) vmRuntimePolicyCmd(flags cli.VMRuntimeFlags, remaining []string) error {
	if len(remaining) <= 1 || remaining[1] != "defaults" {
		return e.s.setVMRuntimePolicy(e.ctx, e.rw, e.sn, flags.Policy, flags.Channel)
	}
	if remaining[2] == "show" {
		return e.s.printVMRuntimePolicyDefaults(e.rw)
	}
	return e.s.setVMRuntimePolicyDefaults(e.ctx, e.rw, flags.Policy, flags.Channel)
}

func (e *ttyExecer) vmRuntimeUpgradeCmd(flags cli.VMRuntimeFlags, service string) error {
	if err := e.s.upgradeVMRuntime(e.ctx, e.rw, service, flags.To, flags.Channel); err != nil {
		return err
	}
	if flags.Restart {
		return e.s.restartStagedVMRuntime(e.ctx, e.rw, service)
	}
	return nil
}

func (e *ttyExecer) vmConsoleRemoteCmdFunc(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("vm console takes no remote arguments")
	}
	return e.vmConsoleCmdFunc()
}

func (e *ttyExecer) vmSetCmdFunc(args []string) error {
	flags, rest, err := cli.ParseVMSet(args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return fmt.Errorf("unexpected vm set args: %s", strings.Join(rest, " "))
	}
	return e.s.updateVMServiceSettings(e.vmProvisionContext(), e.sn, flags)
}

func (e *ttyExecer) vmImagesRemoteCmdFunc(args []string) error {
	flags, remaining, err := cli.ParseVMImages(args)
	if err != nil {
		return err
	}
	return e.vmImagesCmdFunc(flags, remaining)
}

func (e *ttyExecer) vmKernelRemoteCmdFunc(args []string) error {
	flags, remaining, err := cli.ParseVMKernel(args)
	if err != nil {
		return err
	}
	if len(remaining) != 1 || remaining[0] != "sync" {
		return fmt.Errorf("unexpected vm kernel args: %s", strings.Join(remaining, " "))
	}
	return e.s.syncVMGuestKernel(e.vmProvisionContext(), e.sn, flags)
}

func (e *ttyExecer) vmMemoryRemoteCmdFunc(args []string) error {
	flags, rest, err := cli.ParseVMMemory(args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		if strings.TrimSpace(flags.Policy) != "" {
			return fmt.Errorf("use vm memory set --policy=%s to change host VM memory policy", flags.Policy)
		}
		return e.s.printVMMemoryStatus(e.rw, flags.Format)
	}
	if len(rest) == 1 && rest[0] == "set" {
		return e.s.setVMMemoryPolicy(flags.Policy)
	}
	return fmt.Errorf("unexpected vm memory args: %s", strings.Join(rest, " "))
}

func (e *ttyExecer) vmImageCache() vmImageCache {
	return vmImageCache{
		Root: filepath.Join(e.s.cfg.RootDir, "vm-images"),
	}
}

func (e *ttyExecer) vmConsoleCmdFunc() error {
	sv, err := e.s.serviceView(e.sn)
	if err != nil {
		return err
	}
	if sv.ServiceType() != db.ServiceTypeVM {
		return fmt.Errorf("service %q is type %q; vm console requires type \"vm\"", e.sn, sv.ServiceType())
	}
	vm := sv.VM()
	if !vm.Valid() {
		return fmt.Errorf("service %q has no VM console socket", e.sn)
	}
	socketPath := strings.TrimSpace(vm.Console().SocketPath)
	if socketPath == "" {
		return fmt.Errorf("service %q has no VM console socket", e.sn)
	}
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return vmConsoleJournalFunc(e)
		}
		return fmt.Errorf("connect VM console socket %s: %w", socketPath, err)
	}
	defer func() { _ = conn.Close() }()
	if e.ctx != nil {
		go func() {
			<-e.ctx.Done()
			_ = conn.Close()
		}()
	}
	writef(e.rw, "Connected to VM console. Escape: press Enter, then type ~.\r\n")

	go func() {
		_ = copyVMConsoleInput(conn, e.rw)
		if unixConn, ok := conn.(*net.UnixConn); ok {
			_ = unixConn.CloseWrite()
		}
	}()

	_, err = io.Copy(e.rw, conn)
	if isExpectedVMConsoleCopyError(err) {
		return nil
	}
	return err
}

func copyVMConsoleInput(dst io.Writer, src io.Reader) error {
	buf := make([]byte, 1)
	state := newVMConsoleInputState(dst)
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			if err := state.write(buf[0]); err != nil {
				return err
			}
		}
		if readErr != nil {
			return state.finish(readErr)
		}
	}
}

type vmConsoleInputState struct {
	dst          io.Writer
	atLineStart  bool
	pendingTilde bool
}

func newVMConsoleInputState(dst io.Writer) *vmConsoleInputState {
	return &vmConsoleInputState{dst: dst, atLineStart: true}
}

func (s *vmConsoleInputState) write(b byte) error {
	if s.pendingTilde {
		if b == '.' {
			return errVMConsoleDetached
		}
		if err := s.writeRaw('~'); err != nil {
			return err
		}
		s.pendingTilde = false
	}
	if s.atLineStart && b == '~' {
		s.pendingTilde = true
		s.atLineStart = false
		return nil
	}
	if err := s.writeRaw(b); err != nil {
		return err
	}
	s.atLineStart = vmConsoleInputLineBreak(b)
	return nil
}

func (s *vmConsoleInputState) finish(readErr error) error {
	if s.pendingTilde {
		if err := s.writeRaw('~'); err != nil {
			return err
		}
	}
	if errors.Is(readErr, io.EOF) {
		return nil
	}
	return readErr
}

func (s *vmConsoleInputState) writeRaw(b byte) error {
	_, err := s.dst.Write([]byte{b})
	return err
}

func vmConsoleInputLineBreak(b byte) bool {
	return b == '\n' || b == '\r'
}

func (e *ttyExecer) vmConsoleJournalCmdFunc() error {
	cmd := e.newCmd("journalctl", "-u", vmSystemdUnitName(e.sn), "-f", "-o", "cat", "-n", "200")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("stream VM console journal: %w", err)
	}
	return nil
}

func isExpectedVMConsoleCopyError(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrClosed) {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "broken pipe") ||
		strings.Contains(text, "use of closed network connection")
}
