// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/yeetrun/yeet/pkg/svc"
)

var vmSystemdSystemDir = "/etc/systemd/system"

type vmRunner struct {
	name   string
	newCmd func(string, ...string) *exec.Cmd
}

func (r *vmRunner) SetNewCmd(f func(string, ...string) *exec.Cmd) {
	r.newCmd = f
}

func (r *vmRunner) unit() string {
	return vmSystemdUnitName(r.name)
}

func (r *vmRunner) unitPath() string {
	return filepath.Join(vmSystemdSystemDir, r.unit())
}

func vmSystemdUnitName(name string) string {
	return "yeet-vm-" + name + ".service"
}

func (r *vmRunner) command(name string, args ...string) *exec.Cmd {
	if r.newCmd == nil {
		r.newCmd = exec.Command
	}
	return r.newCmd(name, args...)
}

func (r *vmRunner) systemctl(args ...string) error {
	cmd := r.command("systemctl", args...)
	clearVMCommandOutput(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl %v failed: %w\n%s", args, err, string(out))
	}
	return nil
}

func clearVMCommandOutput(cmd *exec.Cmd) {
	cmd.Stdout = nil
	cmd.Stderr = nil
}

func (r *vmRunner) Start() error {
	return r.systemctl("start", r.unit())
}

func (r *vmRunner) Stop() error {
	return r.systemctl("stop", r.unit())
}

func (r *vmRunner) Restart() error {
	return r.systemctl("restart", r.unit())
}

func (r *vmRunner) Status() (svc.Status, error) {
	cmd := r.command("systemctl", "is-active", "--quiet", r.unit())
	clearVMCommandOutput(cmd)
	if err := cmd.Run(); err != nil {
		return svc.StatusStopped, nil
	}
	return svc.StatusRunning, nil
}

func (r *vmRunner) Enable() error {
	return r.systemctl("enable", r.unit())
}

func (r *vmRunner) Disable() error {
	return r.systemctl("disable", r.unit())
}

func (r *vmRunner) Remove() error {
	disableErr := r.systemctl("disable", "--now", r.unit())
	if vmSystemdUnitMissingError(disableErr, r.unit()) {
		disableErr = nil
	}
	removeErr := os.Remove(r.unitPath())
	if os.IsNotExist(removeErr) {
		removeErr = nil
	}
	if removeErr != nil {
		removeErr = fmt.Errorf("failed to remove VM systemd unit %s: %w", r.unitPath(), removeErr)
	}
	reloadErr := r.systemctl("daemon-reload")
	return errors.Join(disableErr, removeErr, reloadErr)
}

func vmSystemdUnitMissingError(err error, unit string) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, strings.ToLower(unit)) &&
		(strings.Contains(message, "does not exist") || strings.Contains(message, "could not be found"))
}

func (r *vmRunner) Logs(opts *svc.LogOptions) error {
	cmd := r.command("journalctl", systemdLogArgs(r.unit(), opts)...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start journalctl: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("failed to wait for journalctl: %w", err)
	}
	return nil
}
