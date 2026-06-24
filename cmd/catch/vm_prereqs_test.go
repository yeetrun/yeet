// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSetupVMHostWithReadyHostSkipsPromptAndInstall(t *testing.T) {
	var ran bool
	var stderr bytes.Buffer
	err := setupVMHostWith(vmSetupDeps{
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
	})
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

func TestSetupVMHostWithMissingPackagesWarnsWithoutPromptOrInstall(t *testing.T) {
	var stderr bytes.Buffer
	var commands [][]string
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
			commands = append(commands, append([]string{name}, args...))
			return nil
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
}

func TestSetupVMHostWithMissingPackagesWithoutAPTWarnsOnly(t *testing.T) {
	var stderr bytes.Buffer
	var ran bool
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
		stderr: &stderr,
		goarch: "amd64",
	})
	if err != nil {
		t.Fatalf("setupVMHostWith returned error: %v", err)
	}
	if ran {
		t.Fatal("setupVMHostWith ran installer without apt-get")
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
		stderr: &stderr,
		goarch: "amd64",
	})
	if err != nil {
		t.Fatalf("setupVMHostWith returned error: %v", err)
	}
	if ran {
		t.Fatal("setupVMHostWith ran installer even though KVM is missing")
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
		stderr:     &stderr,
		goarch:     "arm64",
	})
	if err != nil {
		t.Fatalf("setupVMHostWith returned error: %v", err)
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
		stderr: &stderr,
		goarch: "amd64",
	})
	if err == nil || !strings.Contains(err.Error(), "failed to install VM host packages") {
		t.Fatalf("setupVMHostWith error = %v, want install failure", err)
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
