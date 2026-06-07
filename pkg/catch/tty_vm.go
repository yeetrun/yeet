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
		if len(args) != 1 {
			return fmt.Errorf("vm console takes no remote arguments")
		}
		return e.vmConsoleCmdFunc()
	case "images":
		flags, remaining, err := cli.ParseVMImages(args[1:])
		if err != nil {
			return err
		}
		return e.vmImagesCmdFunc(flags, remaining)
	default:
		return fmt.Errorf("unknown vm command %q", args[0])
	}
}

func (e *ttyExecer) vmImageCache() vmImageCache {
	return vmImageCache{
		Root:        filepath.Join(e.s.cfg.RootDir, "vm-images"),
		ManifestURL: defaultVMImageManifestURL,
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
