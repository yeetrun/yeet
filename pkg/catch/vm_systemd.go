// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type vmSystemdConfig struct {
	Service          string
	Runner           string
	Firecracker      string
	ConfigPath       string
	APISocket        string
	ConsoleSocket    string
	WorkingDirectory string
}

func renderVMSystemdUnit(cfg vmSystemdConfig) string {
	return fmt.Sprintf(`[Unit]
Description=yeet VM %s
After=network-online.target yeet-ns.service
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=%s
ExecStartPre=/bin/rm -f %s %s
ExecStart=%s vm-run --firecracker %s --api-sock %s --config-file %s --console-sock %s
Restart=on-failure
RestartForceExitStatus=75
RestartPreventExitStatus=%d
RestartSec=1
KillMode=mixed
TimeoutStopSec=10

[Install]
WantedBy=multi-user.target
`, cfg.Service, cfg.WorkingDirectory, cfg.APISocket, cfg.ConsoleSocket, cfg.Runner, cfg.Firecracker, cfg.APISocket, cfg.ConfigPath, cfg.ConsoleSocket, VMRestoreLoadFailedExitCode)
}

func ensureVMSystemdRestorePrevent(name string) error {
	unitPath := filepath.Join(vmSystemdSystemDir, vmSystemdUnitName(name))
	raw, err := os.ReadFile(unitPath)
	if err != nil {
		return fmt.Errorf("read VM systemd unit %s: %w", unitPath, err)
	}
	unit := string(raw)
	line := fmt.Sprintf("RestartPreventExitStatus=%d", VMRestoreLoadFailedExitCode)
	if !strings.Contains(unit, line) {
		insertAfter := "RestartForceExitStatus=75\n"
		if !strings.Contains(unit, insertAfter) {
			return fmt.Errorf("VM systemd unit %s does not contain RestartForceExitStatus=75", unitPath)
		}
		unit = strings.Replace(unit, insertAfter, insertAfter+line+"\n", 1)
		if err := writeVMSystemdUnitAtomic(unitPath, []byte(unit), 0o644); err != nil {
			return fmt.Errorf("write VM systemd unit %s: %w", unitPath, err)
		}
	}
	cmd := exec.Command("systemctl", "daemon-reload")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("reload systemd after updating VM unit %s: %w: %s", unitPath, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func writeVMSystemdUnitAtomic(path string, contents []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(contents); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
