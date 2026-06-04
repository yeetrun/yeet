// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"

	"github.com/yeetrun/yeet/pkg/cmdutil"
)

type vmSetupDeps struct {
	commandExists func(string) bool
	pathExists    func(string) bool
	confirm       func(io.Reader, io.Writer, string) (bool, error)
	stdin         io.Reader
	stderr        io.Writer
	runCommand    func(string, ...string) error
	goarch        string
}

type vmHostCommandRequirement struct {
	Command string
	Package string
}

type vmHostPrereqReport struct {
	MissingCommands []vmHostCommandRequirement
	MissingDevices  []string
	UnsupportedArch string
	ZFSAvailable    bool
	MissingUdevadm  bool
}

var requiredVMHostCommands = []vmHostCommandRequirement{
	{Command: "qemu-img", Package: "qemu-utils"},
	{Command: "zstd", Package: "zstd"},
	{Command: "e2fsck", Package: "e2fsprogs"},
	{Command: "resize2fs", Package: "e2fsprogs"},
	{Command: "mount", Package: "util-linux"},
	{Command: "umount", Package: "util-linux"},
	{Command: "ip", Package: "iproute2"},
}

var requiredVMHostDevices = []string{"/dev/kvm", "/dev/net/tun"}

func setupVMHost() error {
	return setupVMHostWith(defaultVMSetupDeps())
}

func defaultVMSetupDeps() vmSetupDeps {
	return vmSetupDeps{
		commandExists: commandExists,
		pathExists:    pathExists,
		confirm:       cmdutil.Confirm,
		stdin:         os.Stdin,
		stderr:        os.Stderr,
		runCommand:    runVMHostSetupCommand,
		goarch:        runtime.GOARCH,
	}
}

func setupVMHostWith(deps vmSetupDeps) error {
	deps = normalizeVMSetupDeps(deps)
	report := inspectVMHostPrereqs(deps)
	warnVMHostCapabilities(deps.stderr, report)
	if len(report.MissingCommands) == 0 {
		return nil
	}
	packages := missingVMHostPackages(report)
	warnMissingVMHostCommands(deps.stderr, report.MissingCommands, packages)
	if !deps.commandExists("apt-get") {
		return nil
	}
	ok, err := deps.confirm(deps.stdin, deps.stderr, "Would you like to install VM host packages with apt-get?")
	if err != nil {
		warnVMHostConfirmError(deps.stderr, err, packages)
		return nil
	}
	if !ok {
		return nil
	}
	if err := deps.runCommand("apt-get", "update"); err != nil {
		return fmt.Errorf("failed to update apt package index for VM tooling: %w", err)
	}
	args := append([]string{"install", "-y"}, packages...)
	if err := deps.runCommand("apt-get", args...); err != nil {
		return fmt.Errorf("failed to install VM host packages: %w", err)
	}
	return nil
}

func normalizeVMSetupDeps(deps vmSetupDeps) vmSetupDeps {
	defaults := defaultVMSetupDeps()
	if deps.commandExists == nil {
		deps.commandExists = defaults.commandExists
	}
	if deps.pathExists == nil {
		deps.pathExists = defaults.pathExists
	}
	if deps.confirm == nil {
		deps.confirm = defaults.confirm
	}
	if deps.stdin == nil {
		deps.stdin = defaults.stdin
	}
	if deps.stderr == nil {
		deps.stderr = defaults.stderr
	}
	if deps.runCommand == nil {
		deps.runCommand = defaults.runCommand
	}
	if deps.goarch == "" {
		deps.goarch = defaults.goarch
	}
	return deps
}

func inspectVMHostPrereqs(deps vmSetupDeps) vmHostPrereqReport {
	deps = normalizeVMSetupDeps(deps)
	report := vmHostPrereqReport{}
	if deps.goarch != "amd64" && deps.goarch != "x86_64" {
		report.UnsupportedArch = deps.goarch
	}
	for _, device := range requiredVMHostDevices {
		if !deps.pathExists(device) {
			report.MissingDevices = append(report.MissingDevices, device)
		}
	}
	for _, req := range requiredVMHostCommands {
		if !deps.commandExists(req.Command) {
			report.MissingCommands = append(report.MissingCommands, req)
		}
	}
	report.ZFSAvailable = deps.commandExists("zfs")
	report.MissingUdevadm = report.ZFSAvailable && !deps.commandExists("udevadm")
	return report
}

func warnVMHostCapabilities(out io.Writer, report vmHostPrereqReport) {
	if report.UnsupportedArch != "" {
		_, _ = fmt.Fprintf(out, "Warning: VM payloads require x86_64/amd64 hosts in this release; detected %s\n", report.UnsupportedArch)
	}
	for _, device := range report.MissingDevices {
		switch device {
		case "/dev/kvm":
			_, _ = fmt.Fprintln(out, "Warning: VM payloads require KVM; /dev/kvm is missing")
		case "/dev/net/tun":
			_, _ = fmt.Fprintln(out, "Warning: VM networking requires TUN/TAP; /dev/net/tun is missing")
		default:
			_, _ = fmt.Fprintf(out, "Warning: VM payloads require %s; it is missing\n", device)
		}
	}
	if report.MissingUdevadm {
		_, _ = fmt.Fprintln(out, "Warning: ZFS-backed VM disks require udevadm; raw VM disks still work")
	}
}

func warnMissingVMHostCommands(out io.Writer, missing []vmHostCommandRequirement, packages []string) {
	commands := make([]string, 0, len(missing))
	for _, req := range missing {
		commands = append(commands, req.Command)
	}
	_, _ = fmt.Fprintf(
		out,
		"Warning: VM tooling is missing required commands: %s. Install packages: %s\n",
		strings.Join(commands, ", "),
		strings.Join(packages, ", "),
	)
}

func warnVMHostConfirmError(out io.Writer, err error, packages []string) {
	_, _ = fmt.Fprintf(
		out,
		"Warning: could not confirm VM package install (%v). To enable VM payloads, install: %s\n",
		err,
		strings.Join(packages, ", "),
	)
}

func missingVMHostPackages(report vmHostPrereqReport) []string {
	var packages []string
	for _, req := range report.MissingCommands {
		if req.Package != "" {
			packages = append(packages, req.Package)
		}
	}
	return sortedUniqueStrings(packages)
}

func sortedUniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	values = append([]string(nil), values...)
	sort.Strings(values)
	out := values[:0]
	for _, value := range values {
		if value == "" {
			continue
		}
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func runVMHostSetupCommand(name string, args ...string) error {
	cmd := cmdutil.NewStdCmd(name, args...)
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	return cmd.Run()
}
