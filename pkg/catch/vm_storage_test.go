// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestVMRawDiskPlanUsesSparseQemuImage(t *testing.T) {
	plan := vmDiskPlan{
		Service:    "devbox",
		Backend:    vmDiskBackendRaw,
		Path:       "/srv/yeet/services/devbox/data/rootfs.raw",
		Bytes:      32 << 30,
		BaseRootFS: "/srv/yeet/images/ubuntu/rootfs.ext4",
	}
	cmds, err := plan.Commands()
	if err != nil {
		t.Fatalf("Commands: %v", err)
	}
	want := [][]string{
		{"qemu-img", "create", "-f", "raw", plan.Path, "34359738368"},
		{"cp", "--reflink=auto", "--sparse=always", plan.BaseRootFS, plan.Path},
		{"truncate", "-s", "34359738368", plan.Path},
		{"e2fsck", "-pf", plan.Path},
		{"resize2fs", plan.Path},
	}
	if !reflect.DeepEqual(cmds, want) {
		t.Fatalf("commands = %#v, want %#v", cmds, want)
	}
}

func TestRunVMCommandTreatsE2FSCKCorrectionsAsSuccess(t *testing.T) {
	fakeCommandInPath(t, "e2fsck", "#!/bin/sh\nexit 1\n")

	if err := runVMCommand(context.Background(), []string{"e2fsck", "-pf", "/srv/devbox/rootfs.raw"}); err != nil {
		t.Fatalf("runVMCommand e2fsck exit 1: %v", err)
	}
}

func TestRunVMCommandRejectsHardE2FSCKFailure(t *testing.T) {
	fakeCommandInPath(t, "e2fsck", "#!/bin/sh\necho broken >&2\nexit 2\n")

	err := runVMCommand(context.Background(), []string{"e2fsck", "-pf", "/srv/devbox/rootfs.raw"})
	if err == nil {
		t.Fatal("runVMCommand e2fsck exit 2 = nil, want error")
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Fatalf("error = %q, want command output", err.Error())
	}
}

func fakeCommandInPath(t *testing.T, name, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestVMZVOLPlanCreatesSparseClone(t *testing.T) {
	plan := vmDiskPlan{
		Service:      "devbox",
		Backend:      vmDiskBackendZVOL,
		Path:         "flash/yeet/vms/devbox/root",
		Bytes:        128 << 30,
		BaseBytes:    2 << 30,
		BaseRootFS:   "/srv/yeet/images/ubuntu/rootfs.ext4",
		BaseDataset:  "flash/yeet/base/ubuntu-26.04",
		ImageVersion: "ubuntu-26.04-amd64-v1",
	}
	base, err := plan.ZVOLBaseSteps()
	if err != nil {
		t.Fatalf("ZVOLBaseSteps: %v", err)
	}
	wantBase := []vmDiskPlanStep{
		{Phase: vmDiskPhaseZVOLBasePrepare, Command: []string{"zfs", "create", "-p", "-s", "-V", "2147483648", plan.BaseDataset}},
		{Phase: vmDiskPhaseZVOLBasePrepare, Command: []string{"udevadm", "settle", "--timeout=10"}},
		{Phase: vmDiskPhaseZVOLBaseWrite, Command: []string{"dd", "if=" + plan.BaseRootFS, "of=/dev/zvol/" + plan.BaseDataset, "bs=16M", "status=none"}},
		{Phase: vmDiskPhaseZVOLBaseWrite, Command: []string{"zfs", "snapshot", "flash/yeet/base/ubuntu-26.04@ubuntu-26.04-amd64-v1"}},
	}
	if !reflect.DeepEqual(base, wantBase) {
		t.Fatalf("base steps = %#v, want %#v", base, wantBase)
	}
	clone, err := plan.ZVOLCloneSteps()
	if err != nil {
		t.Fatalf("ZVOLCloneSteps: %v", err)
	}
	wantClone := []vmDiskPlanStep{
		{Phase: vmDiskPhaseZVOLClone, Command: []string{"zfs", "clone", "-o", "volsize=137438953472", "flash/yeet/base/ubuntu-26.04@ubuntu-26.04-amd64-v1", plan.Path}},
		{Phase: vmDiskPhaseZVOLClone, Command: []string{"udevadm", "settle", "--timeout=10"}},
	}
	if !reflect.DeepEqual(clone, wantClone) {
		t.Fatalf("clone steps = %#v, want %#v", clone, wantClone)
	}
	steps, err := plan.Steps()
	if err != nil {
		t.Fatalf("Steps: %v", err)
	}
	wantSteps := append(append([]vmDiskPlanStep(nil), wantBase...), wantClone...)
	if !reflect.DeepEqual(steps, wantSteps) {
		t.Fatalf("steps = %#v, want %#v", steps, wantSteps)
	}
	if cmds, err := plan.Commands(); err == nil || cmds != nil {
		t.Fatalf("zvol Commands() = %#v, %v; want nil commands and phased-plan error", cmds, err)
	}
}

func TestVMZVOLCloneStepsSkipHostFilesystemResize(t *testing.T) {
	plan := vmDiskPlan{
		Service:      "devbox",
		Backend:      vmDiskBackendZVOL,
		Path:         "flash/yeet/vms/devbox/root",
		Bytes:        128 << 30,
		BaseBytes:    2 << 30,
		BaseRootFS:   "/srv/yeet/images/ubuntu/rootfs.ext4",
		BaseDataset:  "flash/yeet/base/ubuntu-26.04",
		ImageVersion: "ubuntu-26.04-amd64-v1",
	}

	clone, err := plan.ZVOLCloneSteps()
	if err != nil {
		t.Fatalf("ZVOLCloneSteps: %v", err)
	}
	for _, step := range clone {
		if len(step.Command) > 0 && step.Command[0] == "resize2fs" {
			t.Fatalf("zvol clone steps must not host-resize filesystem: %#v", clone)
		}
	}
	got := clone[len(clone)-1].Command
	want := vmZVOLSettleCommand()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("last clone command = %#v, want settle %#v", got, want)
	}
}

func TestVMZVOLPlanUsesSingleZFSCreateAndCloneOperations(t *testing.T) {
	plan := vmDiskPlan{
		Service:      "devbox",
		Backend:      vmDiskBackendZVOL,
		Path:         "flash/yeet/vms/devbox/root",
		Bytes:        128 << 30,
		BaseBytes:    2 << 30,
		BaseRootFS:   "/srv/yeet/images/ubuntu/rootfs.ext4",
		BaseDataset:  "flash/yeet/vms/devbox/base/ubuntu-26.04-amd64-v1",
		ImageVersion: "ubuntu-26.04-amd64-v1",
	}
	base, err := plan.ZVOLBaseSteps()
	if err != nil {
		t.Fatalf("ZVOLBaseSteps: %v", err)
	}
	if !reflect.DeepEqual(base[0].Command, []string{"zfs", "create", "-p", "-s", "-V", "2147483648", plan.BaseDataset}) {
		t.Fatalf("first base command = %#v, want combined parent/base zvol create", base[0].Command)
	}
	clone, err := plan.ZVOLCloneSteps()
	if err != nil {
		t.Fatalf("ZVOLCloneSteps: %v", err)
	}
	wantClone := []string{"zfs", "clone", "-o", "volsize=137438953472", plan.ZVOLSnapshotName(), plan.Path}
	if !reflect.DeepEqual(clone[0].Command, wantClone) {
		t.Fatalf("first clone command = %#v, want combined clone+volsize %#v", clone[0].Command, wantClone)
	}
}

func TestVMZVOLBaseStepsAreSharedAcrossVMClones(t *testing.T) {
	base := vmDiskPlan{
		Service:      "devbox",
		Backend:      vmDiskBackendZVOL,
		Path:         "flash/yeet/vms/devbox/root",
		Bytes:        128 << 30,
		BaseBytes:    2 << 30,
		BaseRootFS:   "/srv/yeet/images/ubuntu/rootfs.ext4",
		BaseDataset:  "flash/yeet/base/ubuntu-26.04",
		ImageVersion: "ubuntu-26.04-amd64-v1",
	}
	other := base
	other.Service = "worker"
	other.Path = "flash/yeet/vms/worker/root"
	other.Bytes = 64 << 30

	baseSteps, err := base.ZVOLBaseSteps()
	if err != nil {
		t.Fatalf("base ZVOLBaseSteps: %v", err)
	}
	otherBaseSteps, err := other.ZVOLBaseSteps()
	if err != nil {
		t.Fatalf("other ZVOLBaseSteps: %v", err)
	}
	if !reflect.DeepEqual(baseSteps, otherBaseSteps) {
		t.Fatalf("base setup differs between VMs:\n%#v\n%#v", baseSteps, otherBaseSteps)
	}

	cloneSteps, err := base.ZVOLCloneSteps()
	if err != nil {
		t.Fatalf("base ZVOLCloneSteps: %v", err)
	}
	otherCloneSteps, err := other.ZVOLCloneSteps()
	if err != nil {
		t.Fatalf("other ZVOLCloneSteps: %v", err)
	}
	if reflect.DeepEqual(cloneSteps, otherCloneSteps) {
		t.Fatalf("clone setup should differ by target path: %#v", cloneSteps)
	}
}

func TestValidateVMDiskPlanRejectsInvalidZVOL(t *testing.T) {
	valid := vmDiskPlan{
		Service:      "devbox",
		Backend:      vmDiskBackendZVOL,
		Path:         "flash/yeet/vms/devbox/root",
		Bytes:        128 << 30,
		BaseRootFS:   "/srv/yeet/images/ubuntu/rootfs.ext4",
		BaseDataset:  "flash/yeet/base/ubuntu-26.04",
		ImageVersion: "ubuntu-26.04-amd64-v1",
	}
	tests := []struct {
		name string
		edit func(*vmDiskPlan)
	}{
		{name: "empty base dataset", edit: func(p *vmDiskPlan) { p.BaseDataset = "" }},
		{name: "empty image version", edit: func(p *vmDiskPlan) { p.ImageVersion = "" }},
		{name: "empty path", edit: func(p *vmDiskPlan) { p.Path = "" }},
		{name: "base dataset snapshot", edit: func(p *vmDiskPlan) { p.BaseDataset = "flash/base@old" }},
		{name: "image version bookmark", edit: func(p *vmDiskPlan) { p.ImageVersion = "ubuntu#old" }},
		{name: "path parent traversal", edit: func(p *vmDiskPlan) { p.Path = "flash/../root" }},
		{name: "path absolute", edit: func(p *vmDiskPlan) { p.Path = "/flash/yeet/vms/devbox/root" }},
		{name: "target matches base", edit: func(p *vmDiskPlan) { p.Path = p.BaseDataset }},
		{name: "target invalid component", edit: func(p *vmDiskPlan) { p.Path = "flash/yeet/vms/dev box/root" }},
		{name: "dot component", edit: func(p *vmDiskPlan) { p.BaseDataset = "flash/./base" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := valid
			tt.edit(&plan)
			if err := plan.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
			if cmds, err := plan.Commands(); err == nil || cmds != nil {
				t.Fatalf("Commands() = %#v, %v; want nil commands and error", cmds, err)
			}
		})
	}
}

func TestValidateVMDiskPlanRejectsInvalidRaw(t *testing.T) {
	tests := []vmDiskPlan{
		{Backend: vmDiskBackendRaw, Path: "", Bytes: 32 << 30, BaseRootFS: "/srv/yeet/images/ubuntu/rootfs.ext4"},
		{Backend: vmDiskBackendRaw, Path: "/srv/yeet/services/devbox/data/rootfs.raw", Bytes: 0, BaseRootFS: "/srv/yeet/images/ubuntu/rootfs.ext4"},
		{Backend: vmDiskBackendRaw, Path: "/srv/yeet/services/devbox/data/rootfs.raw", Bytes: 32 << 30, BaseRootFS: ""},
	}
	for _, plan := range tests {
		if err := plan.Validate(); err == nil {
			t.Fatalf("expected validation error for %#v", plan)
		}
	}
}

func TestRejectVMDiskShrink(t *testing.T) {
	err := validateVMDiskResize(128<<30, 64<<30)
	if err == nil {
		t.Fatal("expected disk shrink rejection")
	}
}

func TestRawVMDiskResizeStepsGrowFilesystem(t *testing.T) {
	steps, err := rawVMDiskResizeSteps("/srv/devbox/data/rootfs.raw", 16<<30, 32<<30)
	if err != nil {
		t.Fatalf("rawVMDiskResizeSteps: %v", err)
	}
	want := []vmDiskPlanStep{
		{Phase: vmDiskPhaseRawResize, Command: []string{"qemu-img", "resize", "/srv/devbox/data/rootfs.raw", "34359738368"}},
		{Phase: vmDiskPhaseRawResize, Command: []string{"e2fsck", "-pf", "/srv/devbox/data/rootfs.raw"}},
		{Phase: vmDiskPhaseRawResize, Command: []string{"resize2fs", "/srv/devbox/data/rootfs.raw"}},
	}
	if !reflect.DeepEqual(steps, want) {
		t.Fatalf("steps = %#v, want %#v", steps, want)
	}
}

func TestZVOLVMDiskResizeStepsGrowFilesystem(t *testing.T) {
	steps, err := zvolVMDiskResizeSteps("flash/yeet/vms/devbox/vm/d-abc/root", 16<<30, 32<<30)
	if err != nil {
		t.Fatalf("zvolVMDiskResizeSteps: %v", err)
	}
	want := []vmDiskPlanStep{
		{Phase: vmDiskPhaseZVOLResize, Command: []string{"zfs", "set", "volsize=34359738368", "flash/yeet/vms/devbox/vm/d-abc/root"}},
		{Phase: vmDiskPhaseZVOLResize, Command: vmZVOLSettleCommand()},
		{Phase: vmDiskPhaseZVOLResize, Command: []string{"e2fsck", "-pf", "/dev/zvol/flash/yeet/vms/devbox/vm/d-abc/root"}},
		{Phase: vmDiskPhaseZVOLResize, Command: []string{"resize2fs", "/dev/zvol/flash/yeet/vms/devbox/vm/d-abc/root"}},
	}
	if !reflect.DeepEqual(steps, want) {
		t.Fatalf("steps = %#v, want %#v", steps, want)
	}
}

func TestVMDiskResizeStepsRejectShrinkAndNoop(t *testing.T) {
	if _, err := rawVMDiskResizeSteps("/srv/rootfs.raw", 32<<30, 16<<30); err == nil || !strings.Contains(err.Error(), "VM disk shrink is not supported") {
		t.Fatalf("raw shrink error = %v", err)
	}
	steps, err := rawVMDiskResizeSteps("/srv/rootfs.raw", 16<<30, 16<<30)
	if err != nil {
		t.Fatalf("raw noop: %v", err)
	}
	if len(steps) != 0 {
		t.Fatalf("noop steps = %#v, want none", steps)
	}
}

func TestRunVMDiskPlanPreservesDiskOnSetupError(t *testing.T) {
	plan := vmDiskPlan{
		Service:    "devbox",
		Backend:    vmDiskBackendRaw,
		Path:       "/srv/yeet/services/devbox/data/rootfs.raw",
		Bytes:      32 << 30,
		BaseRootFS: "/srv/yeet/images/ubuntu/rootfs.ext4",
	}
	boom := errors.New("boom")
	err := runVMDiskPlanWithRunner(context.Background(), plan, func(context.Context, []string) error {
		return boom
	})
	var incomplete vmSetupIncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("error = %T %v, want vmSetupIncompleteError", err, err)
	}
	if incomplete.DiskPath != plan.Path {
		t.Fatalf("disk path = %q, want %q", incomplete.DiskPath, plan.Path)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("error does not wrap original error: %v", err)
	}
	if !strings.Contains(err.Error(), "qemu-img create -f raw /srv/yeet/services/devbox/data/rootfs.raw 34359738368") {
		t.Fatalf("error missing failed command: %v", err)
	}
}

func TestRunVMProvisionDiskPlanSkipsExistingZVOLBaseSnapshot(t *testing.T) {
	plan := vmDiskPlan{
		Service:      "devbox",
		Backend:      vmDiskBackendZVOL,
		Path:         "flash/yeet/vms/devbox/root",
		Bytes:        128 << 30,
		BaseBytes:    2 << 30,
		BaseRootFS:   "/srv/yeet/images/ubuntu/rootfs.ext4",
		BaseDataset:  "flash/yeet/base/ubuntu-26.04",
		ImageVersion: "ubuntu-26.04-amd64-v1",
	}
	var commands [][]string
	err := runVMProvisionDiskPlan(context.Background(), plan, func(_ context.Context, command []string) error {
		commands = append(commands, append([]string(nil), command...))
		return nil
	})
	if err != nil {
		t.Fatalf("runVMProvisionDiskPlan: %v", err)
	}
	wantPrefix := []string{"zfs", "list", "-H", "-o", "name", "flash/yeet/base/ubuntu-26.04@ubuntu-26.04-amd64-v1"}
	if !reflect.DeepEqual(commands[0], wantPrefix) {
		t.Fatalf("first command = %#v, want %#v", commands[0], wantPrefix)
	}
	for _, command := range commands[1:] {
		if len(command) > 3 && command[0] == "zfs" && command[1] == "create" && command[2] == "-s" {
			t.Fatalf("zvol base create should be skipped when snapshot exists: %#v", commands)
		}
		if len(command) > 0 && command[0] == "dd" {
			t.Fatalf("zvol base dd should be skipped when snapshot exists: %#v", commands)
		}
	}
	if gotLast := commands[len(commands)-1]; !reflect.DeepEqual(gotLast, vmZVOLSettleCommand()) {
		t.Fatalf("last command = %#v, want zvol settle", gotLast)
	}
}

func TestRunVMProvisionDiskPlanCreatesMissingZVOLBase(t *testing.T) {
	plan := vmDiskPlan{
		Service:      "devbox",
		Backend:      vmDiskBackendZVOL,
		Path:         "flash/yeet/vms/devbox/root",
		Bytes:        128 << 30,
		BaseBytes:    2 << 30,
		BaseRootFS:   "/srv/yeet/images/ubuntu/rootfs.ext4",
		BaseDataset:  "flash/yeet/base/ubuntu-26.04",
		ImageVersion: "ubuntu-26.04-amd64-v1",
	}
	var commands [][]string
	err := runVMProvisionDiskPlan(context.Background(), plan, func(_ context.Context, command []string) error {
		commands = append(commands, append([]string(nil), command...))
		if isZFSListSnapshotCommand(command, plan) {
			return errors.New("snapshot missing")
		}
		if isZFSListBaseDatasetCommand(command, plan) {
			return errors.New("base missing")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("runVMProvisionDiskPlan: %v", err)
	}
	if len(commands) < 6 {
		t.Fatalf("commands = %#v, want base and clone steps", commands)
	}
	createIndex := firstCommandIndex(commands, func(command []string) bool {
		return isZVOLBaseCreateCommand(command, plan)
	})
	if createIndex < 0 {
		t.Fatalf("base create command not found in %#v", commands)
	}
}

func TestRunVMProvisionDiskPlanRecreatesOrphanedZVOLBase(t *testing.T) {
	plan := testZVOLProgressDiskPlan()
	var commands [][]string
	destroyed := false

	err := runVMProvisionDiskPlanWithProgress(context.Background(), plan, func(_ context.Context, command []string) error {
		commands = append(commands, append([]string(nil), command...))
		switch {
		case isZFSListSnapshotCommand(command, plan):
			return errors.New("snapshot missing")
		case isZFSListBaseDatasetCommand(command, plan):
			return nil
		case reflect.DeepEqual(command, []string{"zfs", "destroy", "-f", plan.BaseDataset}):
			destroyed = true
			return nil
		case isZVOLBaseCreateCommand(command, plan):
			if !destroyed {
				return errors.New("dataset already exists")
			}
		}
		return nil
	}, nil)
	if err != nil {
		t.Fatalf("runVMProvisionDiskPlanWithProgress: %v\ncommands = %#v", err, commands)
	}

	destroyIndex := commandIndex(commands, []string{"zfs", "destroy", "-f", plan.BaseDataset})
	createIndex := firstCommandIndex(commands, func(command []string) bool {
		return isZVOLBaseCreateCommand(command, plan)
	})
	if destroyIndex < 0 {
		t.Fatalf("orphaned base destroy command not found in %#v", commands)
	}
	if createIndex < 0 {
		t.Fatalf("base create command not found in %#v", commands)
	}
	if destroyIndex > createIndex {
		t.Fatalf("orphaned base destroyed after recreate: %#v", commands)
	}
}

func TestRunVMProvisionDiskPlanSerializesConcurrentZVOLBaseCreation(t *testing.T) {
	plan := testZVOLProgressDiskPlan()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var mu sync.Mutex
	snapshotChecks := 0
	snapshotCreated := false
	baseCreates := 0
	baseWrites := 0
	bothColdChecks := make(chan struct{})

	runner := func(ctx context.Context, command []string) error {
		if isZFSListSnapshotCommand(command, plan) {
			mu.Lock()
			if snapshotCreated {
				mu.Unlock()
				return nil
			}
			snapshotChecks++
			if snapshotChecks == 2 {
				close(bothColdChecks)
			}
			mu.Unlock()

			select {
			case <-bothColdChecks:
				return errors.New("snapshot missing")
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if isZFSListBaseDatasetCommand(command, plan) {
			return errors.New("base missing")
		}
		if isZVOLBaseCreateCommand(command, plan) {
			mu.Lock()
			defer mu.Unlock()
			baseCreates++
			if baseCreates > 1 {
				return errors.New("base already created")
			}
			return nil
		}
		if len(command) > 0 && command[0] == "dd" {
			mu.Lock()
			baseWrites++
			mu.Unlock()
		}
		if reflect.DeepEqual(command, []string{"zfs", "snapshot", plan.ZVOLSnapshotName()}) {
			mu.Lock()
			snapshotCreated = true
			mu.Unlock()
		}
		return nil
	}

	errs := make(chan error, 2)
	for range 2 {
		go func() {
			errs <- runVMProvisionDiskPlanWithProgress(ctx, plan, runner, nil)
		}()
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("runVMProvisionDiskPlanWithProgress: %v", err)
		}
	}

	mu.Lock()
	gotCreates := baseCreates
	gotWrites := baseWrites
	mu.Unlock()
	if gotCreates != 1 {
		t.Fatalf("base creates = %d, want 1", gotCreates)
	}
	if gotWrites != 1 {
		t.Fatalf("base writes = %d, want 1", gotWrites)
	}
}

func TestRunVMProvisionDiskPlanReportsOnlyCloneProgressWhenBaseExists(t *testing.T) {
	plan := testZVOLProgressDiskPlan()
	var labels []string
	var commands [][]string

	err := runVMProvisionDiskPlanWithProgress(context.Background(), plan, func(_ context.Context, command []string) error {
		commands = append(commands, append([]string(nil), command...))
		return nil
	}, func(label string) {
		labels = append(labels, label)
	})
	if err != nil {
		t.Fatalf("runVMProvisionDiskPlanWithProgress: %v", err)
	}

	wantLabels := []string{"Cloning VM disk"}
	if !reflect.DeepEqual(labels, wantLabels) {
		t.Fatalf("labels = %#v, want %#v", labels, wantLabels)
	}
	for _, command := range commands {
		if len(command) > 0 && command[0] == "dd" {
			t.Fatalf("base image write should be skipped when snapshot exists: %#v", commands)
		}
	}
}

func TestRunVMProvisionDiskPlanReportsBaseProgressWhenSnapshotMissing(t *testing.T) {
	plan := testZVOLProgressDiskPlan()
	var labels []string
	var commands [][]string

	err := runVMProvisionDiskPlanWithProgress(context.Background(), plan, func(_ context.Context, command []string) error {
		commands = append(commands, append([]string(nil), command...))
		if isZFSListSnapshotCommand(command, plan) {
			return errors.New("snapshot missing")
		}
		if isZFSListBaseDatasetCommand(command, plan) {
			return errors.New("base missing")
		}
		return nil
	}, func(label string) {
		labels = append(labels, label)
	})
	if err != nil {
		t.Fatalf("runVMProvisionDiskPlanWithProgress: %v", err)
	}

	wantLabels := []string{
		"Preparing ZFS image base",
		"Writing image to ZFS base",
		"Cloning VM disk",
	}
	if !reflect.DeepEqual(labels, wantLabels) {
		t.Fatalf("labels = %#v, want %#v", labels, wantLabels)
	}
}

func TestRunVMProvisionDiskPlanIncludesPhaseInSetupError(t *testing.T) {
	plan := testZVOLProgressDiskPlan()
	wantErr := errors.New("write failed")

	err := runVMProvisionDiskPlanWithProgress(context.Background(), plan, func(_ context.Context, command []string) error {
		if len(command) > 0 && command[0] == "dd" {
			return wantErr
		}
		if len(command) == 6 && command[0] == "zfs" && command[1] == "list" {
			return errors.New("snapshot missing")
		}
		return nil
	}, nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want wrapped write failure", err)
	}
	if !strings.Contains(err.Error(), "Writing image to ZFS base") {
		t.Fatalf("error missing phase label: %v", err)
	}
}

func testZVOLProgressDiskPlan() vmDiskPlan {
	return vmDiskPlan{
		Service:      "devbox",
		Backend:      vmDiskBackendZVOL,
		Path:         "flash/yeet/vms/devbox/root",
		Bytes:        128 << 30,
		BaseBytes:    2 << 30,
		BaseRootFS:   "/srv/yeet/images/ubuntu/rootfs.ext4",
		BaseDataset:  "flash/yeet/vm-images/ubuntu-26.04-amd64-v1/root",
		ImageVersion: "ubuntu-26.04-amd64-v1",
	}
}

func isZFSListSnapshotCommand(command []string, plan vmDiskPlan) bool {
	return reflect.DeepEqual(command, []string{"zfs", "list", "-H", "-o", "name", plan.ZVOLSnapshotName()})
}

func isZFSListBaseDatasetCommand(command []string, plan vmDiskPlan) bool {
	return reflect.DeepEqual(command, []string{"zfs", "list", "-H", "-o", "name", plan.BaseDataset})
}

func isZVOLBaseCreateCommand(command []string, plan vmDiskPlan) bool {
	return reflect.DeepEqual(command, []string{"zfs", "create", "-p", "-s", "-V", "2147483648", plan.BaseDataset})
}

func commandIndex(commands [][]string, want []string) int {
	return firstCommandIndex(commands, func(command []string) bool {
		return reflect.DeepEqual(command, want)
	})
}

func firstCommandIndex(commands [][]string, match func([]string) bool) int {
	for idx, command := range commands {
		if match(command) {
			return idx
		}
	}
	return -1
}
