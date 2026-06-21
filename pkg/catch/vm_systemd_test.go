// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestRenderVMSystemdUnit(t *testing.T) {
	unit := renderVMSystemdUnit(vmSystemdConfig{
		Service:          "devbox",
		Runner:           "/srv/catch/run/catch",
		DataDir:          "/srv/catch/data",
		ServiceRoot:      "/srv/vms/devbox",
		DiskPath:         "/srv/vms/devbox/rootfs.ext4",
		Firecracker:      "/srv/images/firecracker",
		ConfigPath:       "/srv/vms/devbox/run/firecracker.json",
		APISocket:        "/srv/vms/devbox/run/firecracker.sock",
		ConsoleSocket:    "/srv/vms/devbox/run/serial.sock",
		VsockSocket:      "/srv/vms/devbox/run/vsock.sock",
		WorkingDirectory: "/srv/vms/devbox",
	})
	for _, want := range []string{
		"[Unit]",
		"Description=yeet VM devbox",
		"ExecStartPre=/bin/rm -f /srv/vms/devbox/run/firecracker.sock /srv/vms/devbox/run/serial.sock /srv/vms/devbox/run/vsock.sock",
		"ExecStartPre=/srv/catch/run/catch -data-dir /srv/catch/data vm-network-ensure devbox",
		"ExecStart=/srv/catch/run/catch vm-run --service devbox --service-root /srv/vms/devbox --disk-path /srv/vms/devbox/rootfs.ext4 --firecracker /srv/images/firecracker --api-sock /srv/vms/devbox/run/firecracker.sock --config-file /srv/vms/devbox/run/firecracker.json --console-sock /srv/vms/devbox/run/serial.sock",
		"Restart=on-failure",
		"RestartForceExitStatus=75",
		"RestartPreventExitStatus=76",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit missing %q:\n%s", want, unit)
		}
	}
	assertTextOrder(t, unit,
		"ExecStartPre=/bin/rm -f /srv/vms/devbox/run/firecracker.sock /srv/vms/devbox/run/serial.sock /srv/vms/devbox/run/vsock.sock",
		"ExecStartPre=/srv/catch/run/catch -data-dir /srv/catch/data vm-network-ensure devbox",
		"ExecStart=/srv/catch/run/catch vm-run --service devbox --service-root /srv/vms/devbox --disk-path /srv/vms/devbox/rootfs.ext4 --firecracker /srv/images/firecracker --api-sock /srv/vms/devbox/run/firecracker.sock --config-file /srv/vms/devbox/run/firecracker.json --console-sock /srv/vms/devbox/run/serial.sock",
	)
}

func assertTextOrder(t *testing.T, text string, wants ...string) {
	t.Helper()
	offset := 0
	for _, want := range wants {
		idx := strings.Index(text[offset:], want)
		if idx < 0 {
			t.Fatalf("text missing %q after offset %d:\n%s", want, offset, text)
		}
		offset += idx + len(want)
	}
}

func TestEnsureVMSystemdRestorePreventUpdatesExistingUnit(t *testing.T) {
	dir := t.TempDir()
	oldDir := vmSystemdSystemDir
	vmSystemdSystemDir = dir
	t.Cleanup(func() { vmSystemdSystemDir = oldDir })

	fakeBin := t.TempDir()
	systemctlLog := filepath.Join(fakeBin, "systemctl.log")
	systemctl := filepath.Join(fakeBin, "systemctl")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + strconv.Quote(systemctlLog) + "\n"
	if err := os.WriteFile(systemctl, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake systemctl: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	unitPath := filepath.Join(dir, vmSystemdUnitName("devbox"))
	unit := "[Service]\nRestart=on-failure\nRestartForceExitStatus=75\nRestartSec=1\n"
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		t.Fatalf("write unit: %v", err)
	}
	if err := ensureVMSystemdRestorePrevent("devbox"); err != nil {
		t.Fatalf("ensureVMSystemdRestorePrevent: %v", err)
	}
	raw, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("read unit: %v", err)
	}
	if !strings.Contains(string(raw), "RestartPreventExitStatus=76\nRestartSec=1") {
		t.Fatalf("unit = %q, want restore prevent before RestartSec", string(raw))
	}
	logRaw, err := os.ReadFile(systemctlLog)
	if err != nil {
		t.Fatalf("read systemctl log: %v", err)
	}
	if strings.TrimSpace(string(logRaw)) != "daemon-reload" {
		t.Fatalf("systemctl log = %q, want daemon-reload", string(logRaw))
	}
}

func TestEnsureVMSystemdRestorePreventRetriesReloadWhenLineAlreadyPresent(t *testing.T) {
	dir := t.TempDir()
	oldDir := vmSystemdSystemDir
	vmSystemdSystemDir = dir
	t.Cleanup(func() { vmSystemdSystemDir = oldDir })

	fakeBin := t.TempDir()
	systemctlLog := filepath.Join(fakeBin, "systemctl.log")
	systemctlMarker := filepath.Join(fakeBin, "systemctl.failed")
	systemctl := filepath.Join(fakeBin, "systemctl")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + strconv.Quote(systemctlLog) + "\n" +
		"if [ ! -e " + strconv.Quote(systemctlMarker) + " ]; then\n" +
		"  touch " + strconv.Quote(systemctlMarker) + "\n" +
		"  printf 'reload failed\\n' >&2\n" +
		"  exit 1\n" +
		"fi\n"
	if err := os.WriteFile(systemctl, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake systemctl: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	unitPath := filepath.Join(dir, vmSystemdUnitName("devbox"))
	unit := "[Service]\nRestart=on-failure\nRestartForceExitStatus=75\nRestartSec=1\n"
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		t.Fatalf("write unit: %v", err)
	}

	if err := ensureVMSystemdRestorePrevent("devbox"); err == nil {
		t.Fatal("first ensureVMSystemdRestorePrevent error = nil, want daemon-reload failure")
	}
	if err := ensureVMSystemdRestorePrevent("devbox"); err != nil {
		t.Fatalf("second ensureVMSystemdRestorePrevent: %v", err)
	}

	raw, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("read unit: %v", err)
	}
	if got := strings.Count(string(raw), "RestartPreventExitStatus=76"); got != 1 {
		t.Fatalf("restore prevent line count = %d, want 1 in %q", got, string(raw))
	}
	logRaw, err := os.ReadFile(systemctlLog)
	if err != nil {
		t.Fatalf("read systemctl log: %v", err)
	}
	if got := strings.TrimSpace(string(logRaw)); got != "daemon-reload\ndaemon-reload" {
		t.Fatalf("systemctl log = %q, want two daemon-reload calls", string(logRaw))
	}
}

func TestEnsureVMSystemdRestorePreventDoesNotMutateUnitWhenAtomicWriteCannotStart(t *testing.T) {
	dir := t.TempDir()
	oldDir := vmSystemdSystemDir
	vmSystemdSystemDir = dir
	t.Cleanup(func() { vmSystemdSystemDir = oldDir })
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	fakeBin := t.TempDir()
	systemctlLog := filepath.Join(fakeBin, "systemctl.log")
	systemctl := filepath.Join(fakeBin, "systemctl")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + strconv.Quote(systemctlLog) + "\n"
	if err := os.WriteFile(systemctl, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake systemctl: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	unitPath := filepath.Join(dir, vmSystemdUnitName("devbox"))
	unit := "[Service]\nRestart=on-failure\nRestartForceExitStatus=75\nRestartSec=1\n"
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		t.Fatalf("write unit: %v", err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("chmod unit dir: %v", err)
	}

	err := ensureVMSystemdRestorePrevent("devbox")
	if err == nil {
		t.Fatal("ensureVMSystemdRestorePrevent error = nil, want atomic write preparation failure")
	}
	raw, readErr := os.ReadFile(unitPath)
	if readErr != nil {
		t.Fatalf("read unit: %v", readErr)
	}
	if string(raw) != unit {
		t.Fatalf("unit mutated after failed atomic write:\n%s", string(raw))
	}
	if _, err := os.Stat(systemctlLog); !os.IsNotExist(err) {
		t.Fatalf("systemctl should not run after failed atomic write, stat error = %v", err)
	}
}
