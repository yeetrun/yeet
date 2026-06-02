// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

var vmZFSDatasetComponentPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)

const (
	vmDiskPhaseRaw             = "raw"
	vmDiskPhaseZVOLBasePrepare = "zvol-base-prepare"
	vmDiskPhaseZVOLBaseWrite   = "zvol-base-write"
	vmDiskPhaseZVOLClone       = "zvol-clone"
	vmDiskPhaseZVOLResize      = "zvol-resize"
)

type vmDiskPlan struct {
	Service      string
	Backend      string
	Path         string
	Bytes        int64
	BaseBytes    int64
	BaseRootFS   string
	BaseDataset  string
	ImageVersion string
}

type vmDiskPlanStep struct {
	Phase   string
	Command []string
}

type vmCommandRunner func(context.Context, []string) error

type vmSetupIncompleteError struct {
	DiskPath string
	Phase    string
	Command  []string
	Err      error
}

func (e vmSetupIncompleteError) Error() string {
	command := formatVMCommandArgv(e.Command)
	if e.DiskPath == "" {
		if command == "" {
			return fmt.Sprintf("VM setup incomplete: %v", e.Err)
		}
		return fmt.Sprintf("VM setup incomplete after %s: %v", command, e.Err)
	}
	if command == "" {
		return fmt.Sprintf("VM setup incomplete for disk %s: %v", e.DiskPath, e.Err)
	}
	return fmt.Sprintf("VM setup incomplete for disk %s after %s: %v", e.DiskPath, command, e.Err)
}

func (e vmSetupIncompleteError) Unwrap() error {
	return e.Err
}

func vmDiskProgressLabel(phase string) string {
	switch phase {
	case vmDiskPhaseRaw:
		return "Preparing disk"
	case vmDiskPhaseZVOLBasePrepare:
		return "Preparing ZFS image base"
	case vmDiskPhaseZVOLBaseWrite:
		return "Writing image to ZFS base"
	case vmDiskPhaseZVOLClone:
		return "Cloning VM disk"
	case vmDiskPhaseZVOLResize:
		return "Expanding filesystem"
	default:
		return ""
	}
}

func (p vmDiskPlan) Validate() error {
	if p.Path == "" {
		return fmt.Errorf("VM disk path is required")
	}
	if p.Bytes <= 0 {
		return fmt.Errorf("VM disk size must be positive")
	}
	if p.BaseRootFS == "" {
		return fmt.Errorf("VM base rootfs is required")
	}
	if p.BaseBytes < 0 {
		return fmt.Errorf("VM base disk size must not be negative")
	}
	if p.Backend != vmDiskBackendZVOL {
		return nil
	}
	if err := validateZFSName("base dataset", p.BaseDataset, true); err != nil {
		return err
	}
	if err := validateZFSName("image version", p.ImageVersion, false); err != nil {
		return err
	}
	if err := validateZFSName("target dataset", p.Path, true); err != nil {
		return err
	}
	if p.Path == p.BaseDataset {
		return fmt.Errorf("VM zvol target dataset must differ from base dataset")
	}
	return nil
}

func validateZFSName(label, name string, allowSlash bool) error {
	if err := validateZFSNameText(label, name, allowSlash); err != nil {
		return err
	}
	components := zfsNameComponents(name, allowSlash)
	for _, component := range components {
		if component == "" || component == "." || !vmZFSDatasetComponentPattern.MatchString(component) {
			return fmt.Errorf("VM zvol %s has invalid component %q", label, component)
		}
	}
	return nil
}

func validateZFSNameText(label, name string, allowSlash bool) error {
	switch {
	case name == "":
		return fmt.Errorf("VM zvol %s is required", label)
	case strings.HasPrefix(name, "/"):
		return fmt.Errorf("VM zvol %s must not be absolute", label)
	case strings.ContainsAny(name, "@#"):
		return fmt.Errorf("VM zvol %s must not contain snapshot or bookmark separators", label)
	case strings.Contains(name, ".."):
		return fmt.Errorf("VM zvol %s must not contain parent traversal", label)
	case !allowSlash && strings.Contains(name, "/"):
		return fmt.Errorf("VM zvol %s must not contain slashes", label)
	default:
		return nil
	}
}

func zfsNameComponents(name string, allowSlash bool) []string {
	if allowSlash {
		return strings.Split(name, "/")
	}
	return []string{name}
}

func (p vmDiskPlan) Commands() ([][]string, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if p.Backend == vmDiskBackendZVOL {
		return nil, fmt.Errorf("VM zvol command planning requires phased steps")
	}
	return stepsCommands(p.rawSteps()), nil
}

func (p vmDiskPlan) Steps() ([]vmDiskPlanStep, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	switch p.Backend {
	case vmDiskBackendZVOL:
		base, err := p.ZVOLBaseSteps()
		if err != nil {
			return nil, err
		}
		clone, err := p.ZVOLCloneSteps()
		if err != nil {
			return nil, err
		}
		return append(base, clone...), nil
	default:
		return p.rawSteps(), nil
	}
}

func (p vmDiskPlan) rawSteps() []vmDiskPlanStep {
	size := fmt.Sprintf("%d", p.Bytes)
	return []vmDiskPlanStep{
		{Phase: vmDiskPhaseRaw, Command: []string{"qemu-img", "create", "-f", "raw", p.Path, size}},
		{Phase: vmDiskPhaseRaw, Command: []string{"cp", "--reflink=auto", "--sparse=always", p.BaseRootFS, p.Path}},
		{Phase: vmDiskPhaseRaw, Command: []string{"truncate", "-s", size, p.Path}},
		{Phase: vmDiskPhaseRaw, Command: []string{"resize2fs", p.Path}},
	}
}

func (p vmDiskPlan) ZVOLBaseSteps() ([]vmDiskPlanStep, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if p.Backend != vmDiskBackendZVOL {
		return nil, fmt.Errorf("VM disk backend %q does not use zvol base setup", p.Backend)
	}
	snap := p.ZVOLSnapshotName()
	size := fmt.Sprintf("%d", p.zvolBaseBytes())
	return append(zfsParentDatasetSteps(vmDiskPhaseZVOLBasePrepare, p.BaseDataset),
		vmDiskPlanStep{Phase: vmDiskPhaseZVOLBasePrepare, Command: []string{"zfs", "create", "-s", "-V", size, p.BaseDataset}},
		vmDiskPlanStep{Phase: vmDiskPhaseZVOLBasePrepare, Command: vmZVOLSettleCommand()},
		vmDiskPlanStep{Phase: vmDiskPhaseZVOLBaseWrite, Command: []string{"dd", "if=" + p.BaseRootFS, "of=/dev/zvol/" + p.BaseDataset, "bs=16M", "status=none"}},
		vmDiskPlanStep{Phase: vmDiskPhaseZVOLBaseWrite, Command: []string{"zfs", "snapshot", snap}},
	), nil
}

func (p vmDiskPlan) zvolBaseBytes() int64 {
	if p.BaseBytes > 0 {
		return p.BaseBytes
	}
	return p.Bytes
}

func (p vmDiskPlan) ZVOLCloneSteps() ([]vmDiskPlanStep, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if p.Backend != vmDiskBackendZVOL {
		return nil, fmt.Errorf("VM disk backend %q does not use zvol clone setup", p.Backend)
	}
	snap := p.ZVOLSnapshotName()
	size := fmt.Sprintf("%d", p.Bytes)
	return append(zfsParentDatasetSteps(vmDiskPhaseZVOLClone, p.Path),
		vmDiskPlanStep{Phase: vmDiskPhaseZVOLClone, Command: []string{"zfs", "clone", snap, p.Path}},
		vmDiskPlanStep{Phase: vmDiskPhaseZVOLClone, Command: []string{"zfs", "set", "volsize=" + size, p.Path}},
		vmDiskPlanStep{Phase: vmDiskPhaseZVOLClone, Command: vmZVOLSettleCommand()},
		vmDiskPlanStep{Phase: vmDiskPhaseZVOLResize, Command: []string{"resize2fs", vmDiskPathForRuntime(p)}},
	), nil
}

func (p vmDiskPlan) ZVOLSnapshotName() string {
	return p.BaseDataset + "@" + p.ImageVersion
}

func zfsParentDatasetSteps(phase, dataset string) []vmDiskPlanStep {
	parent := zfsParentDataset(dataset)
	if parent == "" {
		return nil
	}
	return []vmDiskPlanStep{{Phase: phase, Command: []string{"zfs", "create", "-p", parent}}}
}

func zfsParentDataset(dataset string) string {
	idx := strings.LastIndex(dataset, "/")
	if idx <= 0 {
		return ""
	}
	return dataset[:idx]
}

func vmZVOLSettleCommand() []string {
	return []string{"udevadm", "settle", "--timeout=10"}
}

func stepsCommands(steps []vmDiskPlanStep) [][]string {
	commands := make([][]string, 0, len(steps))
	for _, step := range steps {
		commands = append(commands, append([]string(nil), step.Command...))
	}
	return commands
}

func validateVMDiskResize(currentBytes, requestedBytes int64) error {
	if currentBytes == 0 || requestedBytes == 0 || requestedBytes == currentBytes {
		return nil
	}
	if requestedBytes < currentBytes {
		return fmt.Errorf("VM disk shrink is not supported")
	}
	return nil
}

func runVMDiskPlanWithRunner(ctx context.Context, plan vmDiskPlan, runner vmCommandRunner) error {
	if runner == nil {
		runner = runVMCommand
	}
	steps, err := plan.Steps()
	if err != nil {
		return err
	}
	return runVMDiskStepsWithRunner(ctx, plan, steps, runner, nil)
}

func runVMProvisionDiskPlan(ctx context.Context, plan vmDiskPlan, runner vmCommandRunner) error {
	return runVMProvisionDiskPlanWithProgress(ctx, plan, runner, nil)
}

func runVMProvisionDiskPlanWithProgress(ctx context.Context, plan vmDiskPlan, runner vmCommandRunner, progress func(string)) error {
	if runner == nil {
		runner = runVMCommand
	}
	if plan.Backend != vmDiskBackendZVOL {
		steps, err := plan.Steps()
		if err != nil {
			return err
		}
		return runVMDiskStepsWithRunner(ctx, plan, steps, runner, progress)
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	check := []string{"zfs", "list", "-H", "-o", "name", plan.ZVOLSnapshotName()}
	baseExists := runner(ctx, check) == nil
	var steps []vmDiskPlanStep
	var err error
	if !baseExists {
		steps, err = plan.ZVOLBaseSteps()
		if err != nil {
			return err
		}
	}
	clone, err := plan.ZVOLCloneSteps()
	if err != nil {
		return err
	}
	steps = append(steps, clone...)
	return runVMDiskStepsWithRunner(ctx, plan, steps, runner, progress)
}

func runVMDiskStepsWithRunner(ctx context.Context, plan vmDiskPlan, steps []vmDiskPlanStep, runner vmCommandRunner, progress func(string)) error {
	lastLabel := ""
	for _, step := range steps {
		label := vmDiskProgressLabel(step.Phase)
		if progress != nil && label != "" && label != lastLabel {
			progress(label)
			lastLabel = label
		}
		command := step.Command
		if err := runner(ctx, command); err != nil {
			return vmSetupIncompleteError{DiskPath: plan.Path, Phase: step.Phase, Command: append([]string(nil), command...), Err: err}
		}
	}
	return nil
}

func runVMCommand(ctx context.Context, command []string) error {
	if len(command) == 0 {
		return fmt.Errorf("empty VM setup command")
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	stderr := strings.TrimSpace(string(output))
	if stderr == "" {
		return fmt.Errorf("run %s: %w", formatVMCommandArgv(command), err)
	}
	return fmt.Errorf("run %s: %w: %s", formatVMCommandArgv(command), err, stderr)
}

func formatVMCommandArgv(command []string) string {
	return strings.Join(command, " ")
}
