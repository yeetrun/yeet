// Copyright (c) 2026 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"syscall"
)

type serviceSandboxCommand struct {
	Path       string
	Arguments  []string
	Credential *syscall.Credential
}

type serviceSandboxCommandRunner func(context.Context, serviceSandboxCommand) ([]byte, error)

//lint:ignore U1000 Task 6 consumes this workload-probe interface.
func probeServiceSandbox(ctx context.Context, plan serviceSandboxPlan, uid, gid uint32) error {
	return probeServiceSandboxWith(ctx, plan, uid, gid, runServiceSandboxCommand)
}

func probeServiceSandboxWith(ctx context.Context, plan serviceSandboxPlan, uid, gid uint32, run serviceSandboxCommandRunner) error {
	if len(plan.Arguments) == 0 || plan.Arguments[len(plan.Arguments)-1] != "--" {
		return fmt.Errorf("service sandbox plan must end with terminal -- before workload probe")
	}
	args := append(append([]string(nil), plan.Arguments...), "/usr/bin/true")
	diagnostic, err := run(ctx, serviceSandboxCommand{
		Path:       bubblewrapPath,
		Arguments:  args,
		Credential: &syscall.Credential{Uid: uid, Gid: gid},
	})
	if err != nil {
		return fmt.Errorf("service sandbox workload probe failed for UID %d GID %d; inspect source access and host user-namespace or AppArmor policy; stderr: %s: %w", uid, gid, string(diagnostic), err)
	}
	return nil
}

//lint:ignore U1000 Task 6 consumes this generated-unit verification interface.
func verifyGeneratedSystemdUnit(ctx context.Context, path string) error {
	return verifyGeneratedSystemdUnitWith(ctx, path, runServiceSandboxCommand)
}

func verifyGeneratedSystemdUnitWith(ctx context.Context, path string, run serviceSandboxCommandRunner) error {
	diagnostic, err := run(ctx, serviceSandboxCommand{
		Path:      "/usr/bin/systemd-analyze",
		Arguments: []string{"verify", path},
	})
	if err != nil {
		return fmt.Errorf("verify generated systemd unit %s; diagnostic: %s: %w", path, string(diagnostic), err)
	}
	return nil
}

//lint:ignore U1000 This runner backs the Task 6 probe and unit-verification interfaces.
func runServiceSandboxCommand(ctx context.Context, request serviceSandboxCommand) ([]byte, error) {
	command := exec.CommandContext(ctx, request.Path, request.Arguments...)
	if request.Credential != nil {
		credential := *request.Credential
		command.SysProcAttr = &syscall.SysProcAttr{Credential: &credential}
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	err := command.Run()
	return stderr.Bytes(), err
}
