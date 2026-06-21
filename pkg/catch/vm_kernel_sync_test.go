// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/cli"
)

func TestSyncGuestSelectedKernelCopiesVerifiedKernel(t *testing.T) {
	root := t.TempDir()
	mountRoot := filepath.Join(root, "mnt")
	if err := os.MkdirAll(filepath.Join(mountRoot, "etc/yeet-vm/kernel"), 0o755); err != nil {
		t.Fatalf("mkdir selector: %v", err)
	}
	kernelDir := filepath.Join(mountRoot, "usr/lib/yeet-vm/kernels/linux-7.1.1-yeet")
	if err := os.MkdirAll(kernelDir, 0o755); err != nil {
		t.Fatalf("mkdir kernel: %v", err)
	}
	kernelBytes := "kernel"
	configBytes := "config"
	if err := os.WriteFile(filepath.Join(kernelDir, "vmlinux"), []byte(kernelBytes), 0o644); err != nil {
		t.Fatalf("write kernel: %v", err)
	}
	if err := os.WriteFile(filepath.Join(kernelDir, "kernel.config"), []byte(configBytes), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	selector := `{
		"schema_version":1,
		"version":"linux-7.1.1-yeet",
		"kernel":"/usr/lib/yeet-vm/kernels/linux-7.1.1-yeet/vmlinux",
		"kernel_config":"/usr/lib/yeet-vm/kernels/linux-7.1.1-yeet/kernel.config",
		"sha256":{
			"vmlinux":"` + sha256Hex(kernelBytes) + `",
			"kernel.config":"` + sha256Hex(configBytes) + `"
		}
	}`
	if err := os.WriteFile(filepath.Join(mountRoot, "etc/yeet-vm/kernel/selected.json"), []byte(selector), 0o644); err != nil {
		t.Fatalf("write selector: %v", err)
	}

	out, err := syncGuestSelectedKernelFromMountedRoot(context.Background(), root, "devbox", mountRoot)
	if err != nil {
		t.Fatalf("syncGuestSelectedKernelFromMountedRoot: %v", err)
	}
	if out.Version != "linux-7.1.1-yeet" {
		t.Fatalf("version = %q, want linux-7.1.1-yeet", out.Version)
	}
	if !strings.HasSuffix(out.HostKernelPath, "/run/kernels/devbox/linux-7.1.1-yeet/vmlinux") {
		t.Fatalf("host kernel path = %q", out.HostKernelPath)
	}
	if _, err := os.Stat(out.HostKernelPath); err != nil {
		t.Fatalf("stat copied kernel: %v", err)
	}
}

func TestSyncGuestSelectedKernelReadsSelectorThroughGuestAbsoluteSymlink(t *testing.T) {
	root := t.TempDir()
	mountRoot := filepath.Join(root, "mnt")
	if err := os.MkdirAll(filepath.Join(mountRoot, "etc/yeet-vm/kernel"), 0o755); err != nil {
		t.Fatalf("mkdir selector dir: %v", err)
	}
	staticDir := filepath.Join(mountRoot, "nix/store/hash-etc/etc")
	staticSelectorDir := filepath.Join(staticDir, "yeet-vm/kernel")
	if err := os.MkdirAll(staticSelectorDir, 0o755); err != nil {
		t.Fatalf("mkdir static selector dir: %v", err)
	}
	if err := os.Symlink("/nix/store/hash-etc/etc", filepath.Join(mountRoot, "etc/static")); err != nil {
		t.Fatalf("symlink /etc/static: %v", err)
	}
	if err := os.Symlink("/etc/static/yeet-vm/kernel/selected.json", filepath.Join(mountRoot, "etc/yeet-vm/kernel/selected.json")); err != nil {
		t.Fatalf("symlink selector: %v", err)
	}

	kernelDir := filepath.Join(mountRoot, "nix/store/hash-yeet-vm-kernel-7.1.1/lib/yeet-vm/kernels/linux-7.1.1-yeet")
	if err := os.MkdirAll(kernelDir, 0o755); err != nil {
		t.Fatalf("mkdir kernel: %v", err)
	}
	kernelBytes := "kernel"
	configBytes := "config"
	if err := os.WriteFile(filepath.Join(kernelDir, "vmlinux"), []byte(kernelBytes), 0o644); err != nil {
		t.Fatalf("write kernel: %v", err)
	}
	if err := os.WriteFile(filepath.Join(kernelDir, "kernel.config"), []byte(configBytes), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	selector := `{
		"schema_version":1,
		"version":"linux-7.1.1-yeet",
		"kernel":"/nix/store/hash-yeet-vm-kernel-7.1.1/lib/yeet-vm/kernels/linux-7.1.1-yeet/vmlinux",
		"kernel_config":"/nix/store/hash-yeet-vm-kernel-7.1.1/lib/yeet-vm/kernels/linux-7.1.1-yeet/kernel.config",
		"sha256":{
			"vmlinux":"` + sha256Hex(kernelBytes) + `",
			"kernel.config":"` + sha256Hex(configBytes) + `"
		}
	}`
	if err := os.WriteFile(filepath.Join(staticSelectorDir, "selected.json"), []byte(selector), 0o644); err != nil {
		t.Fatalf("write selector: %v", err)
	}

	out, err := syncGuestSelectedKernelFromMountedRoot(context.Background(), root, "devbox", mountRoot)
	if err != nil {
		t.Fatalf("syncGuestSelectedKernelFromMountedRoot: %v", err)
	}
	if out.Version != "linux-7.1.1-yeet" {
		t.Fatalf("version = %q, want linux-7.1.1-yeet", out.Version)
	}
	if _, err := os.Stat(out.HostKernelPath); err != nil {
		t.Fatalf("stat copied kernel: %v", err)
	}
}

func TestVMKernelSyncRejectsRunningVMWithoutRestart(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", root, vmDiskBackendRaw)
	withVMKernelSyncRunningCheck(t, func(*Server, string) (bool, error) { return true, nil })

	err := server.syncVMGuestKernel(context.Background(), "devbox", cli.VMKernelFlags{})
	if err == nil || !strings.Contains(err.Error(), `cannot sync VM kernel while "devbox" is running`) {
		t.Fatalf("sync error = %v, want running VM error", err)
	}
}

func TestVMKernelSyncUpdatesFirecrackerConfigAndDB(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", root, vmDiskBackendRaw)
	withVMKernelSyncRunningCheck(t, func(*Server, string) (bool, error) { return false, nil })
	withVMKernelSyncRunner(t, mountedGuestKernelRunner(t))
	var systemctlCalls [][]string
	withVMKernelSyncSystemctl(t, func(args ...string) error {
		systemctlCalls = append(systemctlCalls, append([]string(nil), args...))
		return nil
	})

	if err := server.syncVMGuestKernel(context.Background(), "devbox", cli.VMKernelFlags{}); err != nil {
		t.Fatalf("syncVMGuestKernel: %v", err)
	}

	wantKernelSuffix := "/run/kernels/devbox/linux-7.1.1-yeet/vmlinux"
	svc := getTestService(t, server, "devbox")
	if !strings.HasSuffix(svc.VM.Image.Kernel, wantKernelSuffix) {
		t.Fatalf("DB kernel = %q, want suffix %q", svc.VM.Image.Kernel, wantKernelSuffix)
	}
	firecrackerPath := filepath.Join(serviceRunDirForRoot(root), "firecracker.json")
	assertFileContains(t, firecrackerPath, svc.VM.Image.Kernel)
	assertFileNotContains(t, firecrackerPath, "initrd.img")
	if len(systemctlCalls) != 0 {
		t.Fatalf("systemctl calls = %#v, want none", systemctlCalls)
	}
}

func TestVMKernelSyncReplaysJournalBeforeReadOnlyMount(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", root, vmDiskBackendRaw)
	withVMKernelSyncRunningCheck(t, func(*Server, string) (bool, error) { return false, nil })
	var commands [][]string
	withVMKernelSyncRunner(t, func(_ context.Context, command []string) error {
		commands = append(commands, append([]string(nil), command...))
		switch {
		case len(command) > 0 && command[0] == "sh":
			return nil
		case len(command) > 0 && command[0] == "mount":
			writeMountedGuestKernel(t, command[len(command)-1])
			return nil
		case len(command) == 2 && command[0] == "umount":
			return nil
		default:
			return errors.New("unexpected kernel sync command: " + strings.Join(command, " "))
		}
	})

	if err := server.syncVMGuestKernel(context.Background(), "devbox", cli.VMKernelFlags{}); err != nil {
		t.Fatalf("syncVMGuestKernel: %v", err)
	}
	if len(commands) != 3 {
		t.Fatalf("commands = %#v, want journal replay, read-only mount, unmount", commands)
	}
	if commands[0][0] != "sh" || !strings.Contains(strings.Join(commands[0], " "), "mount -o") {
		t.Fatalf("first command = %#v, want shell journal replay mount", commands[0])
	}
	if commands[1][0] != "mount" {
		t.Fatalf("second command = %#v, want read-only mount", commands[1])
	}
	if commands[2][0] != "umount" {
		t.Fatalf("third command = %#v, want unmount", commands[2])
	}
}

func TestVMKernelSyncRestartsRunningVM(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", root, vmDiskBackendRaw)
	withVMKernelSyncRunningCheck(t, func(*Server, string) (bool, error) { return true, nil })
	withVMKernelSyncRunner(t, mountedGuestKernelRunner(t))
	var systemctlCalls [][]string
	withVMKernelSyncSystemctl(t, func(args ...string) error {
		systemctlCalls = append(systemctlCalls, append([]string(nil), args...))
		return nil
	})

	if err := server.syncVMGuestKernel(context.Background(), "devbox", cli.VMKernelFlags{Restart: true}); err != nil {
		t.Fatalf("syncVMGuestKernel: %v", err)
	}

	want := [][]string{
		{"stop", vmSystemdUnitName("devbox")},
		{"restart", vmSystemdUnitName("devbox")},
	}
	if !reflect.DeepEqual(systemctlCalls, want) {
		t.Fatalf("systemctl calls = %#v, want %#v", systemctlCalls, want)
	}
}

func TestVMKernelSyncRestartsRunningVMOnSyncError(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", root, vmDiskBackendRaw)
	withVMKernelSyncRunningCheck(t, func(*Server, string) (bool, error) { return true, nil })
	withVMKernelSyncRunner(t, func(_ context.Context, command []string) error {
		if len(command) > 0 && command[0] == "sh" {
			return nil
		}
		return errors.New("mount failed")
	})
	var systemctlCalls [][]string
	withVMKernelSyncSystemctl(t, func(args ...string) error {
		systemctlCalls = append(systemctlCalls, append([]string(nil), args...))
		return nil
	})

	err := server.syncVMGuestKernel(context.Background(), "devbox", cli.VMKernelFlags{Restart: true})
	if err == nil || !strings.Contains(err.Error(), "mount VM rootfs") {
		t.Fatalf("syncVMGuestKernel error = %v, want mount failure", err)
	}
	want := [][]string{
		{"stop", vmSystemdUnitName("devbox")},
		{"start", vmSystemdUnitName("devbox")},
	}
	if !reflect.DeepEqual(systemctlCalls, want) {
		t.Fatalf("systemctl calls = %#v, want %#v", systemctlCalls, want)
	}
}

func TestVMRootFSReadOnlyMountCommandUsesNoJournalReplay(t *testing.T) {
	tests := []struct {
		name string
		disk string
		want []string
	}{
		{
			name: "file image",
			disk: "/srv/vms/devbox/rootfs.raw",
			want: []string{"mount", "-o", "loop,ro,noload", "/srv/vms/devbox/rootfs.raw", "/mnt/root"},
		},
		{
			name: "block device",
			disk: "/dev/zvol/tank/vms/devbox/root",
			want: []string{"mount", "-o", "ro,noload", "/dev/zvol/tank/vms/devbox/root", "/mnt/root"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := vmRootFSReadOnlyMountCommand(tt.disk, "/mnt/root")
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("mount command = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestVMRootFSJournalReplayCommandMountsWritable(t *testing.T) {
	tests := []struct {
		name string
		disk string
		want string
	}{
		{
			name: "file image",
			disk: "/srv/vms/devbox/rootfs.raw",
			want: "loop,rw",
		},
		{
			name: "block device",
			disk: "/dev/zvol/tank/vms/devbox/root",
			want: "rw",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := vmRootFSJournalReplayCommand(tt.disk, "/mnt/root")
			if len(got) < 7 || got[0] != "sh" || got[1] != "-c" {
				t.Fatalf("journal replay command = %#v, want shell command", got)
			}
			if !strings.Contains(got[2], "mount -o") || !strings.Contains(got[2], "umount") {
				t.Fatalf("journal replay script = %q, want mount and umount", got[2])
			}
			if got[4] != tt.want || got[5] != tt.disk || got[6] != "/mnt/root" {
				t.Fatalf("journal replay args = %#v, want options %q disk %q root /mnt/root", got[4:], tt.want, tt.disk)
			}
		})
	}
}

func TestVMCmdKernelSyncRoutesToServer(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", root, vmDiskBackendRaw)
	var gotName string
	var gotFlags cli.VMKernelFlags
	withVMKernelSyncFunc(t, func(_ context.Context, _ *Server, name string, flags cli.VMKernelFlags) error {
		gotName = name
		gotFlags = flags
		return nil
	})
	execer := &ttyExecer{s: server, sn: "devbox", rw: &bytes.Buffer{}}

	if err := execer.vmCmdFunc([]string{"kernel", "sync", "--restart"}); err != nil {
		t.Fatalf("vm kernel sync: %v", err)
	}
	if gotName != "devbox" {
		t.Fatalf("service = %q, want devbox", gotName)
	}
	if !gotFlags.Restart {
		t.Fatal("Restart = false, want true")
	}
}

func mountedGuestKernelRunner(t *testing.T) vmCommandRunner {
	t.Helper()
	return func(_ context.Context, command []string) error {
		switch {
		case len(command) > 0 && command[0] == "sh":
			return nil
		case len(command) > 0 && command[0] == "mount":
			writeMountedGuestKernel(t, command[len(command)-1])
			return nil
		case len(command) == 2 && command[0] == "umount":
			return nil
		default:
			return errors.New("unexpected kernel sync command: " + strings.Join(command, " "))
		}
	}
}

func writeMountedGuestKernel(t *testing.T, mountRoot string) {
	t.Helper()
	kernelDir := filepath.Join(mountRoot, "usr/lib/yeet-vm/kernels/linux-7.1.1-yeet")
	if err := os.MkdirAll(kernelDir, 0o755); err != nil {
		t.Fatalf("mkdir kernel: %v", err)
	}
	kernelBytes := "kernel"
	configBytes := "config"
	if err := os.WriteFile(filepath.Join(kernelDir, "vmlinux"), []byte(kernelBytes), 0o644); err != nil {
		t.Fatalf("write kernel: %v", err)
	}
	if err := os.WriteFile(filepath.Join(kernelDir, "kernel.config"), []byte(configBytes), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	selectorDir := filepath.Join(mountRoot, "etc/yeet-vm/kernel")
	if err := os.MkdirAll(selectorDir, 0o755); err != nil {
		t.Fatalf("mkdir selector: %v", err)
	}
	selector := `{
		"schema_version":1,
		"version":"linux-7.1.1-yeet",
		"kernel":"/usr/lib/yeet-vm/kernels/linux-7.1.1-yeet/vmlinux",
		"kernel_config":"/usr/lib/yeet-vm/kernels/linux-7.1.1-yeet/kernel.config",
		"sha256":{
			"vmlinux":"` + sha256Hex(kernelBytes) + `",
			"kernel.config":"` + sha256Hex(configBytes) + `"
		}
	}`
	if err := os.WriteFile(filepath.Join(selectorDir, "selected.json"), []byte(selector), 0o644); err != nil {
		t.Fatalf("write selector: %v", err)
	}
}

func withVMKernelSyncRunningCheck(t *testing.T, fn func(*Server, string) (bool, error)) {
	t.Helper()
	old := isServiceRunningForVMKernelSync
	isServiceRunningForVMKernelSync = fn
	t.Cleanup(func() { isServiceRunningForVMKernelSync = old })
}

func withVMKernelSyncRunner(t *testing.T, fn vmCommandRunner) {
	t.Helper()
	old := vmKernelSyncRunner
	vmKernelSyncRunner = fn
	t.Cleanup(func() { vmKernelSyncRunner = old })
}

func withVMKernelSyncSystemctl(t *testing.T, fn func(args ...string) error) {
	t.Helper()
	old := vmKernelSyncSystemctlFunc
	vmKernelSyncSystemctlFunc = fn
	t.Cleanup(func() { vmKernelSyncSystemctlFunc = old })
}

func withVMKernelSyncFunc(t *testing.T, fn func(context.Context, *Server, string, cli.VMKernelFlags) error) {
	t.Helper()
	old := syncVMGuestKernelFunc
	syncVMGuestKernelFunc = fn
	t.Cleanup(func() { syncVMGuestKernelFunc = old })
}
