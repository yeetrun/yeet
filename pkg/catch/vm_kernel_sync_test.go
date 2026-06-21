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
	withVMKernelSyncRunner(t, func(context.Context, []string) error { return errors.New("mount failed") })
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
