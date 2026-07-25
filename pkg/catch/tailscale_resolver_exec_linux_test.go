// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/sys/unix"
)

const (
	tailscaleResolverExecPrivilegedProbeStage = "probe"
	tailscaleResolverExecPrivilegedSource     = "/etc/netns/yeet-codex-guard-ns/resolv.conf"
	tailscaleResolverExecPrivilegedTarget     = "/etc/resolv.conf"
)

func TestTailscaleResolverExecPrivilegedHelper(t *testing.T) {
	switch os.Getenv(tailscaleResolverExecPrivilegedStageEnv) {
	case "":
		return
	case tailscaleResolverExecPrivilegedSetupStage:
		tailscaleResolverExecPrivilegedSetup(t)
	case tailscaleResolverExecPrivilegedProbeStage:
		tailscaleResolverExecPrivilegedProbe(t)
	default:
		t.Fatalf("unknown privileged resolver launcher stage %q", os.Getenv(tailscaleResolverExecPrivilegedStageEnv))
	}
}

func tailscaleResolverExecPrivilegedSetup(t *testing.T) {
	t.Helper()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if info, err := os.Stat("/etc/netns"); err != nil {
		t.Fatalf("stat existing /etc/netns mountpoint: %v", err)
	} else if !info.IsDir() {
		t.Fatalf("/etc/netns is not a directory")
	}
	if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
		t.Fatalf("unshare mount namespace: %v", err)
	}
	if err := unix.Mount("/", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatalf("make mount namespace private: %v", err)
	}
	if err := unix.Mount("tmpfs", "/etc/netns", "tmpfs", 0, "mode=0755"); err != nil {
		t.Fatalf("mount private /etc/netns tmpfs: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(tailscaleResolverExecPrivilegedSource), 0o755); err != nil {
		t.Fatalf("make resolver source directory: %v", err)
	}
	if err := os.WriteFile(tailscaleResolverExecPrivilegedSource, []byte("nameserver 100.100.100.100\n"), 0o644); err != nil {
		t.Fatalf("write resolver source: %v", err)
	}
	serviceRoot, err := os.MkdirTemp("", "yeet-tailscale-resolver-exec-")
	if err != nil {
		t.Fatalf("make temporary service root: %v", err)
	}
	defer os.RemoveAll(serviceRoot)
	executableDir := filepath.Join(serviceRoot, "bin")
	if err := os.MkdirAll(executableDir, 0o755); err != nil {
		t.Fatalf("make temporary tailscaled directory: %v", err)
	}
	executable := filepath.Join(executableDir, "tailscaled")
	if err := os.Symlink(os.Args[0], executable); err != nil {
		t.Fatalf("symlink test binary as tailscaled: %v", err)
	}
	if err := os.Setenv(tailscaleResolverExecPrivilegedStageEnv, tailscaleResolverExecPrivilegedProbeStage); err != nil {
		t.Fatalf("set probe stage: %v", err)
	}
	if err := os.Setenv(tailscaleResolverExecPrivilegedExecDirEnv, serviceRoot); err != nil {
		t.Fatalf("set temporary service root: %v", err)
	}
	if err := RunTailscaleResolverExec([]string{
		"--source", tailscaleResolverExecPrivilegedSource,
		"--", executable, "-test.run=^TestTailscaleResolverExecPrivilegedHelper$",
	}); err != nil {
		t.Fatalf("RunTailscaleResolverExec: %v", err)
	}
	t.Fatal("RunTailscaleResolverExec returned without execing tailscaled")
}

func tailscaleResolverExecPrivilegedProbe(t *testing.T) {
	t.Helper()
	serviceRoot := os.Getenv(tailscaleResolverExecPrivilegedExecDirEnv)
	if serviceRoot == "" {
		t.Fatal("missing temporary service root")
	}
	t.Cleanup(func() { _ = os.RemoveAll(serviceRoot) })
	sourceInfo, err := os.Stat(tailscaleResolverExecPrivilegedSource)
	if err != nil {
		t.Fatalf("stat resolver source: %v", err)
	}
	targetInfo, err := os.Stat(tailscaleResolverExecPrivilegedTarget)
	if err != nil {
		t.Fatalf("stat resolver target: %v", err)
	}
	if !os.SameFile(sourceInfo, targetInfo) {
		t.Fatal("resolver target is not the private resolver source")
	}
	file, err := os.OpenFile(tailscaleResolverExecPrivilegedTarget, os.O_WRONLY, 0)
	if err == nil {
		_ = file.Close()
		t.Fatal("open resolver target for writing succeeded, want EROFS")
	}
	if !errors.Is(err, unix.EROFS) {
		t.Fatalf("open resolver target for writing: %v, want EROFS", err)
	}
	resultPath := os.Getenv(tailscaleResolverExecPrivilegedResultEnv)
	if resultPath == "" {
		t.Fatal("missing result path")
	}
	if err := os.WriteFile(resultPath, []byte("pass\n"), 0o600); err != nil {
		t.Fatalf("write probe result: %v", err)
	}
}
