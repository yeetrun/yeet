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

	"github.com/yeetrun/yeet/pkg/catch"
	"github.com/yeetrun/yeet/pkg/cmdutil"
)

type vmSetupDeps struct {
	commandExists    func(string) bool
	pathExists       func(string) bool
	stdin            io.Reader
	stderr           io.Writer
	runCommand       func(string, ...string) error
	getenv           func(string) string
	confirm          func(io.Reader, io.Writer, string) (bool, error)
	planLANBridge    func() (vmLANBridgePlan, error)
	prepareLANBridge func() error
	goarch           string
}

type vmLANRenderer = catch.VMLANRenderer
type vmLANBridgePlan = catch.VMLANBridgePlan

var (
	planVMLANBridgeFn    = catch.PlanVMLANBridge
	prepareVMLANBridgeFn = catch.PrepareVMLANBridge
)

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

const vmHostRequirementsDocsURL = "https://yeetrun.com/docs/getting-started/installation#host-requirements"

func setupVMHost(dataDir string) error {
	return setupVMHostWith(defaultVMSetupDeps(dataDir))
}

func defaultVMSetupDeps(dataDir string) vmSetupDeps {
	return vmSetupDeps{
		commandExists: commandExists,
		pathExists:    pathExists,
		stdin:         os.Stdin,
		stderr:        os.Stderr,
		runCommand:    runVMHostSetupCommand,
		getenv:        os.Getenv,
		confirm:       cmdutil.Confirm,
		planLANBridge: func() (vmLANBridgePlan, error) {
			return planVMLANBridgeFn(dataDir)
		},
		prepareLANBridge: func() error {
			_, err := prepareVMLANBridgeFn(dataDir, true)
			return err
		},
		goarch: runtime.GOARCH,
	}
}

func setupVMHostWith(deps vmSetupDeps) error {
	deps = normalizeVMSetupDeps(deps)
	report := inspectVMHostPrereqs(deps)
	warnVMHostCapabilities(deps.stderr, report)
	if len(report.MissingCommands) == 0 {
		if vmHostPrereqsReady(report) {
			return maybePrepareVMLANBridgeDuringSetup(deps)
		}
		return nil
	}
	packages := missingVMHostPackages(report)
	if !canInstallVMHostPackages(report) {
		warnMissingVMHostCommands(deps.stderr, report.MissingCommands, packages)
		return nil
	}
	if !deps.commandExists("apt-get") {
		warnMissingVMHostCommands(deps.stderr, report.MissingCommands, packages)
		return nil
	}
	if deps.getenv("CATCH_INSTALL_VM_TOOLS") == "1" {
		if _, err := fmt.Fprintf(deps.stderr, "Installing VM host packages because CATCH_INSTALL_VM_TOOLS=1: %s\n", strings.Join(packages, ", ")); err != nil {
			return err
		}
		if err := installVMHostPackages(deps, packages); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(deps.stderr, "Installed VM host packages: %s\n", strings.Join(packages, ", ")); err != nil {
			return err
		}
		return maybePrepareVMLANBridgeDuringSetup(deps)
	}
	warnMissingVMHostCommands(deps.stderr, report.MissingCommands, packages)
	return nil
}

func maybePrepareVMLANBridgeDuringSetup(deps vmSetupDeps) error {
	if deps.getenv("CATCH_SKIP_VM_LAN_BRIDGE") == "1" {
		return nil
	}
	plan, err := deps.planLANBridge()
	if err != nil {
		_, _ = fmt.Fprintf(deps.stderr, "Warning: VM LAN bridge planning is unavailable during init: %v\n", err)
		return nil
	}
	if plan.Ready || !plan.NeedsPrepare {
		return nil
	}
	if deps.getenv("CATCH_PREPARE_VM_LAN_BRIDGE") == "1" {
		return deps.prepareLANBridge()
	}
	if deps.confirm == nil {
		_, _ = fmt.Fprintf(deps.stderr, "Warning: VM LAN bridge %s needs preparation for VM LAN networking; set CATCH_PREPARE_VM_LAN_BRIDGE=1 to prepare it during init or run catch vm-lan-bridge-prepare --yes later.\n", plan.Bridge)
		return nil
	}
	ok, err := deps.confirm(deps.stdin, deps.stderr, fmt.Sprintf("Prepare %s for VM LAN networking?", plan.Bridge))
	if err != nil {
		_, _ = fmt.Fprintf(deps.stderr, "Warning: could not confirm VM LAN bridge preparation: %v\n", err)
		return nil
	}
	if !ok {
		return nil
	}
	return deps.prepareLANBridge()
}

func installVMHostPackages(deps vmSetupDeps, packages []string) error {
	if err := deps.runCommand("apt-get", "update"); err != nil {
		return fmt.Errorf("failed to update apt package index for VM tooling: %w", err)
	}
	args := append([]string{"install", "-y"}, packages...)
	if err := deps.runCommand("apt-get", args...); err != nil {
		return fmt.Errorf("failed to install VM host packages: %w", err)
	}
	return nil
}

func vmHostPrereqsReady(report vmHostPrereqReport) bool {
	return report.UnsupportedArch == "" && len(report.MissingDevices) == 0 && len(report.MissingCommands) == 0
}

func canInstallVMHostPackages(report vmHostPrereqReport) bool {
	if report.UnsupportedArch != "" {
		return false
	}
	return !missingVMHostDevice(report, "/dev/kvm") && !missingVMHostDevice(report, "/dev/net/tun")
}

func missingVMHostDevice(report vmHostPrereqReport, device string) bool {
	for _, missing := range report.MissingDevices {
		if missing == device {
			return true
		}
	}
	return false
}

func normalizeVMSetupDeps(deps vmSetupDeps) vmSetupDeps {
	defaults := defaultVMSetupDeps("")
	if deps.commandExists == nil {
		deps.commandExists = defaults.commandExists
	}
	if deps.pathExists == nil {
		deps.pathExists = defaults.pathExists
	}
	if deps.stderr == nil {
		deps.stderr = defaults.stderr
	}
	if deps.runCommand == nil {
		deps.runCommand = defaults.runCommand
	}
	if deps.getenv == nil {
		deps.getenv = defaults.getenv
	}
	deps = normalizeVMSetupPromptDeps(deps, defaults)
	if deps.goarch == "" {
		deps.goarch = defaults.goarch
	}
	return deps
}

func normalizeVMSetupPromptDeps(deps vmSetupDeps, defaults vmSetupDeps) vmSetupDeps {
	if deps.stdin == nil {
		deps.stdin = defaults.stdin
	}
	if deps.confirm == nil {
		deps.confirm = defaults.confirm
	}
	if deps.planLANBridge == nil {
		deps.planLANBridge = defaults.planLANBridge
	}
	if deps.prepareLANBridge == nil {
		deps.prepareLANBridge = defaults.prepareLANBridge
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
		_, _ = fmt.Fprintf(out, "Warning: VM support is unavailable on this host: yeet VM payloads require x86_64/amd64 hosts in this release; detected %s. See %s\n", report.UnsupportedArch, vmHostRequirementsDocsURL)
	}
	for _, device := range report.MissingDevices {
		switch device {
		case "/dev/kvm":
			_, _ = fmt.Fprintf(out, "Warning: VM support is unavailable on this host: /dev/kvm is missing. Containers, binaries, and cron jobs still work. See %s\n", vmHostRequirementsDocsURL)
		case "/dev/net/tun":
			_, _ = fmt.Fprintf(out, "Warning: VM networking is unavailable on this host: /dev/net/tun is missing. See %s\n", vmHostRequirementsDocsURL)
		default:
			_, _ = fmt.Fprintf(out, "Warning: VM support is unavailable on this host: %s is missing. See %s\n", device, vmHostRequirementsDocsURL)
		}
	}
	if report.MissingUdevadm {
		_, _ = fmt.Fprintf(out, "Warning: ZFS-backed VM disks require udevadm; raw VM disks still work. See %s\n", vmHostRequirementsDocsURL)
	}
}

func warnMissingVMHostCommands(out io.Writer, missing []vmHostCommandRequirement, packages []string) {
	commands := make([]string, 0, len(missing))
	for _, req := range missing {
		commands = append(commands, req.Command)
	}
	_, _ = fmt.Fprintf(
		out,
		"Warning: VM tools are incomplete: missing %s. Install packages: %s. See %s\n",
		strings.Join(commands, ", "),
		strings.Join(packages, ", "),
		vmHostRequirementsDocsURL,
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
