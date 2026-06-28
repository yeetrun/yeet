// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/catch"
)

func TestDefaultVMSetupDepsUsesDataDirForLANBridge(t *testing.T) {
	oldPlan := planVMLANBridgeFn
	oldPrepare := prepareVMLANBridgeFn
	t.Cleanup(func() {
		planVMLANBridgeFn = oldPlan
		prepareVMLANBridgeFn = oldPrepare
	})

	dataDir := t.TempDir()
	var planRoot string
	var prepareRoot string
	planVMLANBridgeFn = func(root string) (vmLANBridgePlan, error) {
		planRoot = root
		return vmLANBridgePlan{NeedsPrepare: true, Bridge: "br0", Parent: "eno1"}, nil
	}
	prepareVMLANBridgeFn = func(root string, yes bool) (catch.VMLANBridgePrepareStatus, error) {
		prepareRoot = root
		return catch.VMLANBridgePrepareStatus{}, nil
	}

	deps := defaultVMSetupDeps(dataDir)
	if _, err := deps.planLANBridge(); err != nil {
		t.Fatalf("planLANBridge: %v", err)
	}
	if err := deps.prepareLANBridge(); err != nil {
		t.Fatalf("prepareLANBridge: %v", err)
	}
	if planRoot != dataDir || prepareRoot != dataDir {
		t.Fatalf("roots = plan %q prepare %q, want %q", planRoot, prepareRoot, dataDir)
	}
}

func TestSetupVMHostWithReadyHostSkipsPromptAndInstall(t *testing.T) {
	var ran bool
	var stderr bytes.Buffer
	err := setupVMHostWith(withReadyVMLANBridge(vmSetupDeps{
		commandExists: fakeVMCommandExists(map[string]bool{
			"qemu-img":  true,
			"zstd":      true,
			"e2fsck":    true,
			"resize2fs": true,
			"mount":     true,
			"umount":    true,
			"ip":        true,
			"apt-get":   true,
		}),
		pathExists: fakeVMPathExists(map[string]bool{
			"/dev/kvm":     true,
			"/dev/net/tun": true,
		}),
		runCommand: func(string, ...string) error {
			ran = true
			return nil
		},
		stderr: &stderr,
		goarch: "amd64",
	}))
	if err != nil {
		t.Fatalf("setupVMHostWith returned error: %v", err)
	}
	if ran {
		t.Fatal("setupVMHostWith ran installer even though VM host was ready")
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestSetupVMHostPromptsForLANBridgeWhenVMToolsReady(t *testing.T) {
	var prompted bool
	var prepared bool
	err := setupVMHostWith(vmSetupDeps{
		commandExists: func(string) bool { return true },
		pathExists: func(path string) bool {
			return path == "/dev/kvm" || path == "/dev/net/tun"
		},
		stderr: io.Discard,
		getenv: func(string) string { return "" },
		confirm: func(_ io.Reader, _ io.Writer, msg string) (bool, error) {
			prompted = strings.Contains(msg, "Prepare br0 for VM LAN networking")
			return true, nil
		},
		planLANBridge: func() (vmLANBridgePlan, error) {
			return vmLANBridgePlan{NeedsPrepare: true, Bridge: "br0", Parent: "eno1", Renderer: vmLANRenderer{Name: "netplan-networkd", Supported: true}}, nil
		},
		prepareLANBridge: func() error {
			prepared = true
			return nil
		},
		goarch: "amd64",
	})
	if err != nil {
		t.Fatalf("setupVMHostWith: %v", err)
	}
	if !prompted || !prepared {
		t.Fatalf("prompted=%v prepared=%v, want both true", prompted, prepared)
	}
}

func TestSetupVMHostUsesDefaultConfirmForLANBridge(t *testing.T) {
	var stderr bytes.Buffer
	var prepared bool
	err := setupVMHostWith(vmSetupDeps{
		commandExists: func(string) bool { return true },
		pathExists: func(path string) bool {
			return path == "/dev/kvm" || path == "/dev/net/tun"
		},
		stdin:  strings.NewReader("y\n"),
		stderr: &stderr,
		getenv: func(string) string { return "" },
		planLANBridge: func() (vmLANBridgePlan, error) {
			return vmLANBridgePlan{NeedsPrepare: true, Bridge: "br0", Parent: "eno1"}, nil
		},
		prepareLANBridge: func() error {
			prepared = true
			return nil
		},
		goarch: "amd64",
	})
	if err != nil {
		t.Fatalf("setupVMHostWith: %v", err)
	}
	if !prepared {
		t.Fatal("prepareLANBridge did not run after default confirmation")
	}
	if !strings.Contains(stderr.String(), "Prepare br0 for VM LAN networking?") {
		t.Fatalf("stderr = %q, want VM LAN bridge prompt", stderr.String())
	}
}

func TestSetupVMHostSkipsLANBridgeWhenOperatorDeclines(t *testing.T) {
	var prepared bool
	err := setupVMHostWith(vmSetupDeps{
		commandExists: func(string) bool { return true },
		pathExists: func(path string) bool {
			return path == "/dev/kvm" || path == "/dev/net/tun"
		},
		stderr:  io.Discard,
		getenv:  func(string) string { return "" },
		confirm: func(_ io.Reader, _ io.Writer, _ string) (bool, error) { return false, nil },
		planLANBridge: func() (vmLANBridgePlan, error) {
			return vmLANBridgePlan{NeedsPrepare: true, Bridge: "br0", Parent: "eno1", Renderer: vmLANRenderer{Name: "netplan-networkd", Supported: true}}, nil
		},
		prepareLANBridge: func() error {
			prepared = true
			return nil
		},
		goarch: "amd64",
	})
	if err != nil {
		t.Fatalf("setupVMHostWith: %v", err)
	}
	if prepared {
		t.Fatal("prepareLANBridge ran after decline")
	}
}

func TestSetupVMHostSkipsLANBridgeWhenEnvSkips(t *testing.T) {
	var planned bool
	var prepared bool
	err := setupVMHostWith(vmSetupDeps{
		commandExists: func(string) bool { return true },
		pathExists: func(path string) bool {
			return path == "/dev/kvm" || path == "/dev/net/tun"
		},
		stderr: io.Discard,
		getenv: func(name string) string {
			if name == "CATCH_SKIP_VM_LAN_BRIDGE" {
				return "1"
			}
			return ""
		},
		planLANBridge: func() (vmLANBridgePlan, error) {
			planned = true
			return vmLANBridgePlan{}, nil
		},
		prepareLANBridge: func() error {
			prepared = true
			return nil
		},
		goarch: "amd64",
	})
	if err != nil {
		t.Fatalf("setupVMHostWith: %v", err)
	}
	if planned || prepared {
		t.Fatalf("planned=%v prepared=%v, want both false", planned, prepared)
	}
}

func TestSetupVMHostPreparesLANBridgeWhenEnvSet(t *testing.T) {
	var confirmed bool
	var prepared bool
	err := setupVMHostWith(vmSetupDeps{
		commandExists: func(string) bool { return true },
		pathExists: func(path string) bool {
			return path == "/dev/kvm" || path == "/dev/net/tun"
		},
		stderr: io.Discard,
		getenv: func(name string) string {
			if name == "CATCH_PREPARE_VM_LAN_BRIDGE" {
				return "1"
			}
			return ""
		},
		confirm: func(io.Reader, io.Writer, string) (bool, error) {
			confirmed = true
			return false, nil
		},
		planLANBridge: func() (vmLANBridgePlan, error) {
			return vmLANBridgePlan{NeedsPrepare: true, Bridge: "br0", Parent: "eno1"}, nil
		},
		prepareLANBridge: func() error {
			prepared = true
			return nil
		},
		goarch: "amd64",
	})
	if err != nil {
		t.Fatalf("setupVMHostWith: %v", err)
	}
	if confirmed || !prepared {
		t.Fatalf("confirmed=%v prepared=%v, want no prompt and prepare", confirmed, prepared)
	}
}

func TestSetupVMHostWarnsAndContinuesWhenLANBridgePlanningFails(t *testing.T) {
	var stderr bytes.Buffer
	err := setupVMHostWith(vmSetupDeps{
		commandExists: func(string) bool { return true },
		pathExists: func(path string) bool {
			return path == "/dev/kvm" || path == "/dev/net/tun"
		},
		stderr: &stderr,
		getenv: func(string) string { return "" },
		planLANBridge: func() (vmLANBridgePlan, error) {
			return vmLANBridgePlan{}, errors.New("unsupported renderer")
		},
		prepareLANBridge: func() error {
			t.Fatal("prepareLANBridge should not run after planning failure")
			return nil
		},
		goarch: "amd64",
	})
	if err != nil {
		t.Fatalf("setupVMHostWith: %v", err)
	}
	if !strings.Contains(stderr.String(), "VM LAN bridge planning is unavailable during init: unsupported renderer") {
		t.Fatalf("stderr = %q, want planning warning", stderr.String())
	}
}

func TestSetupVMHostPropagatesLANBridgePrepareError(t *testing.T) {
	prepareErr := errors.New("prepare failed")
	err := setupVMHostWith(vmSetupDeps{
		commandExists: func(string) bool { return true },
		pathExists: func(path string) bool {
			return path == "/dev/kvm" || path == "/dev/net/tun"
		},
		stderr: io.Discard,
		getenv: func(name string) string {
			if name == "CATCH_PREPARE_VM_LAN_BRIDGE" {
				return "1"
			}
			return ""
		},
		planLANBridge: func() (vmLANBridgePlan, error) {
			return vmLANBridgePlan{NeedsPrepare: true, Bridge: "br0", Parent: "eno1"}, nil
		},
		prepareLANBridge: func() error {
			return prepareErr
		},
		goarch: "amd64",
	})
	if !errors.Is(err, prepareErr) {
		t.Fatalf("setupVMHostWith error = %v, want %v", err, prepareErr)
	}
}

func TestSetupVMHostWithMissingPackagesWarnsWithoutPromptOrInstall(t *testing.T) {
	var stderr bytes.Buffer
	var commands [][]string
	var planned bool
	err := setupVMHostWith(vmSetupDeps{
		commandExists: fakeVMCommandExists(map[string]bool{
			"mount":   true,
			"umount":  true,
			"ip":      true,
			"apt-get": true,
		}),
		pathExists: fakeVMPathExists(map[string]bool{
			"/dev/kvm":     true,
			"/dev/net/tun": true,
		}),
		getenv: func(string) string {
			return ""
		},
		runCommand: func(name string, args ...string) error {
			commands = append(commands, append([]string{name}, args...))
			return nil
		},
		planLANBridge: func() (vmLANBridgePlan, error) {
			planned = true
			return vmLANBridgePlan{Ready: true, Bridge: "br0"}, nil
		},
		stderr: &stderr,
		goarch: "amd64",
	})
	if err != nil {
		t.Fatalf("setupVMHostWith returned error: %v", err)
	}
	if strings.Contains(stderr.String(), "Would you like to install VM host packages") {
		t.Fatalf("stderr = %q, want no install prompt", stderr.String())
	}
	if len(commands) != 0 {
		t.Fatalf("commands = %#v, want no installer commands", commands)
	}
	if planned {
		t.Fatal("setupVMHostWith planned VM LAN bridge when VM tools were missing and not installed")
	}
	got := stderr.String()
	if !strings.Contains(got, "Warning: VM tools are incomplete: missing qemu-img, zstd, e2fsck, resize2fs") {
		t.Fatalf("stderr = %q, want missing command warning", got)
	}
	if strings.Contains(got, "tic") || strings.Contains(got, "ncurses-bin") {
		t.Fatalf("stderr = %q, want no host-side terminfo requirement", got)
	}
	if !strings.Contains(got, "Install packages: e2fsprogs, qemu-utils, zstd") {
		t.Fatalf("stderr = %q, want package list", got)
	}
	if !strings.Contains(got, "https://yeetrun.com/docs/getting-started/installation#host-requirements") {
		t.Fatalf("stderr = %q, want VM requirements docs link", got)
	}
}

func TestSetupVMHostWithInstallEnvInstallsAPTWithoutPrompt(t *testing.T) {
	var stderr bytes.Buffer
	var commands [][]string
	var planned bool
	err := setupVMHostWith(withReadyVMLANBridge(vmSetupDeps{
		commandExists: fakeVMCommandExists(map[string]bool{
			"mount":   true,
			"umount":  true,
			"ip":      true,
			"apt-get": true,
		}),
		pathExists: fakeVMPathExists(map[string]bool{
			"/dev/kvm":     true,
			"/dev/net/tun": true,
		}),
		getenv: func(key string) string {
			if key == "CATCH_INSTALL_VM_TOOLS" {
				return "1"
			}
			return ""
		},
		runCommand: func(name string, args ...string) error {
			commands = append(commands, append([]string{name}, args...))
			return nil
		},
		planLANBridge: func() (vmLANBridgePlan, error) {
			planned = true
			return vmLANBridgePlan{Ready: true, Bridge: "br0"}, nil
		},
		stderr: &stderr,
		goarch: "amd64",
	}))
	if err != nil {
		t.Fatalf("setupVMHostWith returned error: %v", err)
	}
	if strings.Contains(stderr.String(), "Would you like to install VM host packages") {
		t.Fatalf("stderr = %q, want no install prompt", stderr.String())
	}
	want := [][]string{
		{"apt-get", "update"},
		{"apt-get", "install", "-y", "e2fsprogs", "qemu-utils", "zstd"},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
	if !strings.Contains(stderr.String(), "Installing VM host packages because CATCH_INSTALL_VM_TOOLS=1") {
		t.Fatalf("stderr = %q, want install env note", stderr.String())
	}
	if strings.Contains(stderr.String(), "Warning: VM tools are incomplete") {
		t.Fatalf("stderr = %q, want no missing tooling warning after explicit install request", stderr.String())
	}
	if !planned {
		t.Fatal("setupVMHostWith did not plan VM LAN bridge after VM tools install")
	}
}

func TestSetupVMHostWithMissingPackagesWithoutAPTWarnsOnly(t *testing.T) {
	var stderr bytes.Buffer
	var ran bool
	var planned bool
	err := setupVMHostWith(vmSetupDeps{
		commandExists: fakeVMCommandExists(map[string]bool{
			"mount":  true,
			"umount": true,
			"ip":     true,
		}),
		pathExists: fakeVMPathExists(map[string]bool{
			"/dev/kvm":     true,
			"/dev/net/tun": true,
		}),
		runCommand: func(string, ...string) error {
			ran = true
			return nil
		},
		planLANBridge: func() (vmLANBridgePlan, error) {
			planned = true
			return vmLANBridgePlan{Ready: true, Bridge: "br0"}, nil
		},
		stderr: &stderr,
		goarch: "amd64",
	})
	if err != nil {
		t.Fatalf("setupVMHostWith returned error: %v", err)
	}
	if ran {
		t.Fatal("setupVMHostWith ran installer without apt-get")
	}
	if planned {
		t.Fatal("setupVMHostWith planned VM LAN bridge when apt-get was unavailable and tools were missing")
	}
	if !strings.Contains(stderr.String(), "Warning: VM tools are incomplete") {
		t.Fatalf("stderr = %q, want missing tooling warning", stderr.String())
	}
	if !strings.Contains(stderr.String(), "https://yeetrun.com/docs/getting-started/installation#host-requirements") {
		t.Fatalf("stderr = %q, want VM requirements docs link", stderr.String())
	}
}

func TestSetupVMHostWithMissingKVMDoesNotPromptForPackages(t *testing.T) {
	var stderr bytes.Buffer
	var ran bool
	var planned bool
	err := setupVMHostWith(vmSetupDeps{
		commandExists: fakeVMCommandExists(map[string]bool{
			"mount":   true,
			"umount":  true,
			"ip":      true,
			"apt-get": true,
		}),
		pathExists: fakeVMPathExists(map[string]bool{
			"/dev/net/tun": true,
		}),
		runCommand: func(string, ...string) error {
			ran = true
			return nil
		},
		planLANBridge: func() (vmLANBridgePlan, error) {
			planned = true
			return vmLANBridgePlan{Ready: true, Bridge: "br0"}, nil
		},
		stderr: &stderr,
		goarch: "amd64",
	})
	if err != nil {
		t.Fatalf("setupVMHostWith returned error: %v", err)
	}
	if ran {
		t.Fatal("setupVMHostWith ran installer even though KVM is missing")
	}
	if planned {
		t.Fatal("setupVMHostWith planned VM LAN bridge when KVM was missing")
	}
	got := stderr.String()
	if !strings.Contains(got, "Warning: VM support is unavailable on this host: /dev/kvm is missing") {
		t.Fatalf("stderr = %q, want missing KVM warning", got)
	}
	if !strings.Contains(got, "Warning: VM tools are incomplete") {
		t.Fatalf("stderr = %q, want missing tooling warning", got)
	}
	if !strings.Contains(got, "Containers, binaries, and cron jobs still work") {
		t.Fatalf("stderr = %q, want non-VM payload guidance", got)
	}
	if !strings.Contains(got, "https://yeetrun.com/docs/getting-started/installation#host-requirements") {
		t.Fatalf("stderr = %q, want VM requirements docs link", got)
	}
	if strings.Contains(got, "could not confirm VM package install") {
		t.Fatalf("stderr = %q, want no confirm warning", got)
	}
}

func TestSetupVMHostWithMissingCapabilitiesWarnsButDoesNotFail(t *testing.T) {
	var stderr bytes.Buffer
	var planned bool
	err := setupVMHostWith(vmSetupDeps{
		commandExists: fakeVMCommandExists(map[string]bool{
			"qemu-img":  true,
			"zstd":      true,
			"e2fsck":    true,
			"resize2fs": true,
			"mount":     true,
			"umount":    true,
			"ip":        true,
			"zfs":       true,
		}),
		pathExists: fakeVMPathExists(map[string]bool{}),
		planLANBridge: func() (vmLANBridgePlan, error) {
			planned = true
			return vmLANBridgePlan{Ready: true, Bridge: "br0"}, nil
		},
		stderr: &stderr,
		goarch: "arm64",
	})
	if err != nil {
		t.Fatalf("setupVMHostWith returned error: %v", err)
	}
	if planned {
		t.Fatal("setupVMHostWith planned VM LAN bridge when VM capabilities were missing")
	}
	got := stderr.String()
	for _, want := range []string{
		"Warning: VM support is unavailable on this host: yeet VM payloads require x86_64/amd64 hosts in this release; detected arm64",
		"Warning: VM support is unavailable on this host: /dev/kvm is missing",
		"Warning: VM networking is unavailable on this host: /dev/net/tun is missing",
		"Warning: ZFS-backed VM disks require udevadm; raw VM disks still work",
		"https://yeetrun.com/docs/getting-started/installation#host-requirements",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("stderr = %q, missing %q", got, want)
		}
	}
}

func TestSetupVMHostWithAPTInstallFailureReturnsError(t *testing.T) {
	installErr := errors.New("apt failed")
	var stderr bytes.Buffer
	var planned bool
	err := setupVMHostWith(vmSetupDeps{
		commandExists: fakeVMCommandExists(map[string]bool{
			"mount":   true,
			"umount":  true,
			"ip":      true,
			"apt-get": true,
		}),
		pathExists: fakeVMPathExists(map[string]bool{
			"/dev/kvm":     true,
			"/dev/net/tun": true,
		}),
		getenv: func(key string) string {
			if key == "CATCH_INSTALL_VM_TOOLS" {
				return "1"
			}
			return ""
		},
		runCommand: func(name string, args ...string) error {
			if name == "apt-get" && len(args) > 0 && args[0] == "install" {
				return installErr
			}
			return nil
		},
		planLANBridge: func() (vmLANBridgePlan, error) {
			planned = true
			return vmLANBridgePlan{Ready: true, Bridge: "br0"}, nil
		},
		stderr: &stderr,
		goarch: "amd64",
	})
	if err == nil || !strings.Contains(err.Error(), "failed to install VM host packages") {
		t.Fatalf("setupVMHostWith error = %v, want install failure", err)
	}
	if planned {
		t.Fatal("setupVMHostWith planned VM LAN bridge after failed VM tools install")
	}
}

func fakeVMCommandExists(commands map[string]bool) func(string) bool {
	return func(name string) bool {
		return commands[name]
	}
}

func fakeVMPathExists(paths map[string]bool) func(string) bool {
	return func(path string) bool {
		return paths[path]
	}
}

func withReadyVMLANBridge(deps vmSetupDeps) vmSetupDeps {
	if deps.planLANBridge == nil {
		deps.planLANBridge = func() (vmLANBridgePlan, error) {
			return vmLANBridgePlan{Ready: true, Bridge: "br0"}, nil
		}
	}
	if deps.prepareLANBridge == nil {
		deps.prepareLANBridge = func() error {
			panic("prepareLANBridge should not run for ready VM LAN bridge")
		}
	}
	return deps
}
