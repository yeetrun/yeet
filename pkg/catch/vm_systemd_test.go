// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
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

func TestRenderVMSystemdUnitUsesJailerWhenConfigured(t *testing.T) {
	unit := renderVMSystemdUnit(vmSystemdConfig{
		Service:          "devbox",
		Runner:           "/srv/catch/run/catch",
		DataDir:          "/srv/catch/data",
		ServiceRoot:      "/srv/vms/devbox",
		DiskPath:         "/srv/vms/devbox/data/rootfs.raw",
		Firecracker:      "/srv/images/firecracker",
		Jailer:           "/srv/images/jailer",
		JailerBase:       "/run/yeet/vm-jailer",
		ConfigPath:       "/srv/vms/devbox/run/firecracker.json",
		APISocket:        "/srv/vms/devbox/run/firecracker.sock",
		ConsoleSocket:    "/srv/vms/devbox/run/serial.sock",
		VsockSocket:      "/srv/vms/devbox/run/vsock.sock",
		WorkingDirectory: "/srv/vms/devbox",
	})
	want := "--firecracker /srv/images/firecracker --jailer /srv/images/jailer --jailer-base /run/yeet/vm-jailer --api-sock"
	if !strings.Contains(unit, want) {
		t.Fatalf("jailed unit missing %q:\n%s", want, unit)
	}
}

func TestRegenerateHostStorageVMSystemdUnitUsesCurrentRoots(t *testing.T) {
	systemdDir := t.TempDir()
	oldDir := vmSystemdSystemDir
	vmSystemdSystemDir = systemdDir
	t.Cleanup(func() { vmSystemdSystemDir = oldDir })
	oldSystemctl := vmProvisionSystemctlFunc
	var calls [][]string
	vmProvisionSystemctlFunc = func(args ...string) error {
		calls = append(calls, append([]string(nil), args...))
		return nil
	}
	t.Cleanup(func() { vmProvisionSystemctlFunc = oldSystemctl })

	serviceRoot := filepath.Join(t.TempDir(), "services", "devbox")
	service := &db.Service{
		Name:        "devbox",
		ServiceType: db.ServiceTypeVM,
		ServiceRoot: serviceRoot,
		VM: &db.VMConfig{
			Image: db.VMImageConfig{
				RootFS: "/flash/yeet/data/vm-images/ubuntu/rootfs.ext4",
			},
			Disk: db.VMDiskConfig{
				Path: filepath.Join(serviceRoot, "data", "rootfs.raw"),
			},
		},
	}
	cfg := Config{RootDir: "/flash/yeet/data", ServicesRoot: "/flash/yeet/services"}
	if err := writeVMIsolationMode(service.ServiceRoot, vmIsolationJailer); err != nil {
		t.Fatalf("write VM isolation mode: %v", err)
	}

	units, err := regenerateHostStorageVMSystemdUnit(context.Background(), cfg, service, "/flash/yeet/services/catch/run/catch")
	if err != nil {
		t.Fatalf("regenerateHostStorageVMSystemdUnit error: %v", err)
	}
	if !slices.Equal(units, []string{vmSystemdUnitName("devbox")}) {
		t.Fatalf("regenerateHostStorageVMSystemdUnit units = %#v, want VM unit", units)
	}
	raw, err := os.ReadFile(filepath.Join(systemdDir, vmSystemdUnitName("devbox")))
	if err != nil {
		t.Fatal(err)
	}
	unit := string(raw)
	for _, want := range []string{
		"/flash/yeet/data",
		"/flash/yeet/services/catch/run/catch",
		serviceRoot,
		filepath.Join(serviceRoot, "data", "rootfs.raw"),
		"/flash/yeet/data/vm-images/ubuntu/firecracker",
		"--jailer /flash/yeet/data/vm-images/ubuntu/jailer",
		"--jailer-base /flash/yeet/data/vm-jailer",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit missing %s:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "/root/data") {
		t.Fatalf("unit contains old root:\n%s", unit)
	}
	if len(calls) != 0 {
		t.Fatalf("systemctl calls = %#v, want none before batched reload", calls)
	}
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
