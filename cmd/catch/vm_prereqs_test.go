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
)

func TestSetupVMHostWithReadyHostSkipsPromptAndInstall(t *testing.T) {
	var prompted bool
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
			"tic":       true,
			"apt-get":   true,
		}),
		pathExists: fakeVMPathExists(map[string]bool{
			"/dev/kvm":     true,
			"/dev/net/tun": true,
		}),
		confirm: func(io.Reader, io.Writer, string) (bool, error) {
			prompted = true
			return true, nil
		},
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
	if prompted {
		t.Fatal("setupVMHostWith prompted even though VM host was ready")
	}
	if ran {
		t.Fatal("setupVMHostWith ran installer even though VM host was ready")
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestSetupVMHostWithMissingPackagesPromptsAndInstallsAPT(t *testing.T) {
	var stderr bytes.Buffer
	var prompt string
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
		confirm: func(_ io.Reader, _ io.Writer, msg string) (bool, error) {
			prompt = msg
			return true, nil
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
	if prompt != "Would you like to install VM host packages with apt-get?" {
		t.Fatalf("prompt = %q", prompt)
	}
	want := [][]string{
		{"apt-get", "update"},
		{"apt-get", "install", "-y", "e2fsprogs", "ncurses-bin", "qemu-utils", "zstd"},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
	got := stderr.String()
	if !strings.Contains(got, "Warning: VM tooling is missing required commands: qemu-img, zstd, e2fsck, resize2fs, tic") {
		t.Fatalf("stderr = %q, want missing command warning", got)
	}
	if !strings.Contains(got, "Install packages: e2fsprogs, ncurses-bin, qemu-utils, zstd") {
		t.Fatalf("stderr = %q, want package list", got)
	}
}

func TestSetupVMHostWithMissingPackagesWithoutAPTWarnsOnly(t *testing.T) {
	var stderr bytes.Buffer
	var prompted bool
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
		confirm: func(io.Reader, io.Writer, string) (bool, error) {
			prompted = true
			return true, nil
		},
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
	if prompted {
		t.Fatal("setupVMHostWith prompted without apt-get")
	}
	if ran {
		t.Fatal("setupVMHostWith ran installer without apt-get")
	}
	if !strings.Contains(stderr.String(), "Warning: VM tooling is missing required commands") {
		t.Fatalf("stderr = %q, want missing tooling warning", stderr.String())
	}
}

func TestSetupVMHostWithConfirmErrorWarnsAndContinues(t *testing.T) {
	confirmErr := errors.New("no stdin")
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
		confirm: func(io.Reader, io.Writer, string) (bool, error) {
			return false, confirmErr
		},
		stderr: &stderr,
		goarch: "amd64",
	})
	if err != nil {
		t.Fatalf("setupVMHostWith returned error: %v", err)
	}
	if !strings.Contains(stderr.String(), "Warning: could not confirm VM package install") {
		t.Fatalf("stderr = %q, want confirm warning", stderr.String())
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
			"tic":       true,
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
		"Warning: VM payloads require x86_64/amd64 hosts in this release; detected arm64",
		"Warning: VM payloads require KVM; /dev/kvm is missing",
		"Warning: VM networking requires TUN/TAP; /dev/net/tun is missing",
		"Warning: ZFS-backed VM disks require udevadm; raw VM disks still work",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("stderr = %q, missing %q", got, want)
		}
	}
}

func TestSetupVMHostWithAPTInstallFailureReturnsError(t *testing.T) {
	installErr := errors.New("apt failed")
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
		confirm: func(io.Reader, io.Writer, string) (bool, error) {
			return true, nil
		},
		runCommand: func(name string, args ...string) error {
			if name == "apt-get" && len(args) > 0 && args[0] == "install" {
				return installErr
			}
			return nil
		},
		stderr: io.Discard,
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
