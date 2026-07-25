// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
	"tailscale.com/client/local"
	"tailscale.com/ipn/ipnstate"
)

const tailscaleVerificationSampleInterval = 100 * time.Millisecond

type tailscaleLifecycleDeps struct {
	mainPID     func(string) (int, error)
	stat        func(string) (os.FileInfo, error)
	status      func(context.Context, string) (*ipnstate.Status, error)
	verifyMount func(ResolverMountProbe) error
	wait        func(context.Context, time.Duration) error
}

var tailscaleLifecycleDepsFn = func() tailscaleLifecycleDeps {
	return tailscaleLifecycleDeps{
		mainPID:     systemdServiceMainPID,
		stat:        os.Stat,
		status:      tailscaleStatusWithoutPeers,
		verifyMount: VerifyReadOnlyResolverMount,
		wait:        waitForTailscaleVerificationSample,
	}
}

// StartTailscaleSidecar starts the Tailscale sidecar and waits for its final
// tailscaled process to be ready.
func (s *SystemdService) StartTailscaleSidecar(ctx context.Context) error {
	log.Printf("starting tailscaled for %s", s.Name())
	if err := s.run("start", s.tailscaledServiceUnit()); err != nil {
		return err
	}
	verificationErr := s.VerifyTailscaleSidecar(ctx)
	if verificationErr == nil {
		return nil
	}
	stopErr := s.run("stop", s.tailscaledServiceUnit())
	return errors.Join(verificationErr, stopErr)
}

// RestartTailscaleSidecar restarts the Tailscale sidecar and waits for its
// final tailscaled process to be ready.
func (s *SystemdService) RestartTailscaleSidecar(ctx context.Context) error {
	if err := s.run("restart", s.tailscaledServiceUnit()); err != nil {
		return fmt.Errorf("restart tailscaled: %w", err)
	}
	verificationErr := s.VerifyTailscaleSidecar(ctx)
	if verificationErr == nil {
		return nil
	}
	stopErr := s.run("stop", s.tailscaledServiceUnit())
	return errors.Join(verificationErr, stopErr)
}

// VerifyTailscaleSidecar waits until systemd reports the same, final
// tailscaled process twice in a row. A resolver mount is verified from that
// process's mount namespace when the sidecar is in a network namespace.
func (s *SystemdService) VerifyTailscaleSidecar(ctx context.Context) error {
	return s.verifyTailscaleSidecar(ctx, tailscaleLifecycleDepsFn())
}

func (s *SystemdService) verifyTailscaleSidecar(ctx context.Context, deps tailscaleLifecycleDeps) error {
	var stablePID, successes int
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return tailscaleVerificationContextError(err, lastErr)
		}

		pid, err := s.verifyTailscaleSidecarSample(ctx, deps)
		if err != nil {
			lastErr = err
			stablePID = 0
			successes = 0
		} else if pid == stablePID {
			successes++
		} else {
			stablePID = pid
			successes = 1
		}
		if successes == 2 {
			return nil
		}

		if err := deps.wait(ctx, tailscaleVerificationSampleInterval); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return tailscaleVerificationContextError(ctxErr, lastErr)
			}
			return fmt.Errorf("wait for tailscaled verification sample: %w", err)
		}
	}
}

func (s *SystemdService) verifyTailscaleSidecarSample(ctx context.Context, deps tailscaleLifecycleDeps) (int, error) {
	pid, err := deps.mainPID(s.tailscaledServiceUnit())
	if err != nil {
		return 0, err
	}
	if pid <= 0 {
		return 0, errors.New("tailscaled MainPID is not running")
	}
	if err := s.verifyTailscaledExecutable(pid, deps.stat); err != nil {
		return 0, err
	}
	status, err := deps.status(ctx, filepath.Join(s.runDir, "tailscaled.sock"))
	if err != nil {
		return 0, err
	}
	if status == nil || len(status.TailscaleIPs) == 0 {
		return 0, errors.New("tailscale has no IPs yet")
	}
	if err := s.verifyTailscaleResolverMount(pid, deps.verifyMount); err != nil {
		return 0, err
	}
	return pid, nil
}

func (s *SystemdService) verifyTailscaledExecutable(pid int, stat func(string) (os.FileInfo, error)) error {
	binaryPath, err := s.installedTailscaledDaemonPath()
	if err != nil {
		return err
	}
	binaryInfo, err := stat(binaryPath)
	if err != nil {
		return fmt.Errorf("stat installed tailscaled binary %s: %w", binaryPath, err)
	}
	exePath := fmt.Sprintf("/proc/%d/exe", pid)
	exeInfo, err := stat(exePath)
	if err != nil {
		return fmt.Errorf("stat tailscaled process executable %s: %w", exePath, err)
	}
	if !os.SameFile(binaryInfo, exeInfo) {
		return fmt.Errorf("tailscaled process executable %s does not match installed tailscaled binary %s", exePath, binaryPath)
	}
	return nil
}

func (s *SystemdService) installedTailscaledDaemonPath() (string, error) {
	unitPath := s.tailscaledServicePath()
	raw, err := os.ReadFile(unitPath)
	if err != nil {
		return "", fmt.Errorf("read installed tailscaled unit %s: %w", unitPath, err)
	}
	daemon, err := TailscaledDaemonFromUnit(string(raw), filepath.Dir(s.runDir), s.tailscaleGuardRunner)
	if err != nil {
		return "", fmt.Errorf("parse installed tailscaled unit %s: %w", unitPath, err)
	}
	return daemon, nil
}

// TailscaledDaemonFromUnit returns the managed daemon selected by a strict
// direct or resolver-guarded Tailscale systemd unit.
func TailscaledDaemonFromUnit(unit, serviceRoot, expectedCatchRunner string) (string, error) {
	daemon, err := tailscaledDaemonFromUnit(unit, expectedCatchRunner)
	if err != nil {
		return "", err
	}
	if err := ValidateManagedTailscaledDaemonPath(serviceRoot, daemon); err != nil {
		return "", err
	}
	return daemon, nil
}

func tailscaledDaemonFromUnit(unit, expectedCatchRunner string) (string, error) {
	var parser tailscaledUnitParser
	for _, line := range strings.Split(unit, "\n") {
		if err := parser.consumeLine(line); err != nil {
			return "", err
		}
	}
	return parser.finish(expectedCatchRunner)
}

type tailscaledUnitParser struct {
	section    string
	execStarts []string
}

func (p *tailscaledUnitParser) consumeLine(line string) error {
	trimmed := strings.TrimSpace(line)
	switch {
	case trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";"):
		return nil
	case strings.HasSuffix(trimmed, "\\"):
		return errors.New("systemd continuation lines are not supported")
	case strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]"):
		p.section = trimmed
		return nil
	case strings.ContainsAny(trimmed, "\"'"):
		return fmt.Errorf("quoted systemd directive %q is not supported", trimmed)
	case strings.HasPrefix(trimmed, "ExecStart="):
		if p.section != "[Service]" {
			return errors.New("ExecStart outside [Service]")
		}
		p.execStarts = append(p.execStarts, strings.TrimSpace(strings.TrimPrefix(trimmed, "ExecStart=")))
	}
	return nil
}

func (p *tailscaledUnitParser) finish(expectedCatchRunner string) (string, error) {
	if len(p.execStarts) != 1 {
		return "", fmt.Errorf("require exactly one [Service] ExecStart, got %d", len(p.execStarts))
	}
	return tailscaledDaemonFromExecStart(p.execStarts[0], expectedCatchRunner)
}

func tailscaledDaemonFromExecStart(execStart, expectedCatchRunner string) (string, error) {
	args := strings.Fields(execStart)
	if len(args) == 0 {
		return "", fmt.Errorf("ExecStart executable must be an absolute clean path: %q", execStart)
	}
	if len(args) >= 2 && args[1] == "tailscale-resolver-exec" {
		return tailscaledDaemonFromGuardedExecStart(args, execStart, expectedCatchRunner)
	}
	if !cleanAbsoluteTailscalePath(args[0]) {
		return "", fmt.Errorf("ExecStart executable must be an absolute clean path: %q", execStart)
	}
	return args[0], nil
}

func tailscaledDaemonFromGuardedExecStart(args []string, execStart, expectedCatchRunner string) (string, error) {
	if err := validateStableCatchRunner("expected stable Catch runner", expectedCatchRunner); err != nil {
		return "", err
	}
	if err := validateStableCatchRunner("resolver guard launcher", args[0]); err != nil {
		return "", err
	}
	if args[0] != expectedCatchRunner {
		return "", fmt.Errorf("resolver guard launcher %q does not match expected stable Catch runner %q", args[0], expectedCatchRunner)
	}
	if !validGuardedTailscaleExecStart(args) {
		return "", fmt.Errorf("invalid guarded Tailscale ExecStart %q", execStart)
	}
	return args[5], nil
}

func validGuardedTailscaleExecStart(args []string) bool {
	return len(args) >= 6 &&
		args[2] == "--source" &&
		args[4] == "--" &&
		cleanAbsoluteTailscalePath(args[3]) &&
		cleanAbsoluteTailscalePath(args[5])
}

func validateStableCatchRunner(label, path string) error {
	if !cleanAbsoluteTailscalePath(path) {
		return fmt.Errorf("%s must be an absolute clean path: %q", label, path)
	}
	if filepath.Base(path) != "catch" {
		return fmt.Errorf("%s must have basename catch: %q", label, path)
	}
	for _, component := range strings.Split(filepath.Clean(path), string(filepath.Separator)) {
		if component == ".install" {
			return fmt.Errorf("%s must not use an .install staging path: %q", label, path)
		}
	}
	return nil
}

// ValidateManagedTailscaledDaemonPath accepts the current and historical
// Yeet-managed Tailscale daemon layouts for a service root.
func ValidateManagedTailscaledDaemonPath(serviceRoot, daemon string) error {
	current := filepath.Join(serviceRoot, "bin", "tailscaled")
	historical := filepath.Join(serviceRoot, "run", "tailscaled")
	if !cleanAbsoluteTailscalePath(daemon) || (daemon != current && daemon != historical) {
		return fmt.Errorf("tailscaled path = %q, want %q or %q", daemon, current, historical)
	}
	return nil
}

func cleanAbsoluteTailscalePath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path
}

func (s *SystemdService) verifyTailscaleResolverMount(pid int, verify func(ResolverMountProbe) error) error {
	source, err := tailscaleResolverBindSourceFromFile(s.tailscaledServicePath())
	if err != nil {
		return err
	}
	if !s.hasArtifact(db.ArtifactNetNSService) {
		return nil
	}
	if source == "" {
		return errors.New("tailscaled network namespace has no resolver source")
	}
	processRoot := fmt.Sprintf("/proc/%d/root", pid)
	return verify(ResolverMountProbe{
		SourcePath:    processRoot + source,
		TargetPath:    processRoot + "/etc/resolv.conf",
		MountInfoPath: fmt.Sprintf("/proc/%d/mountinfo", pid),
		MountPoint:    "/etc/resolv.conf",
	})
}

func tailscaleVerificationContextError(ctxErr, lastErr error) error {
	if lastErr == nil {
		return ctxErr
	}
	return fmt.Errorf("tailscaled sidecar verification ended: %w", errors.Join(ctxErr, lastErr))
}

func waitForTailscaleVerificationSample(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func tailscaleStatusWithoutPeers(ctx context.Context, sock string) (*ipnstate.Status, error) {
	lc := local.Client{Socket: sock, UseSocketOnly: true}
	return lc.StatusWithoutPeers(ctx)
}

func tailscaleResolverBindSourceFromFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read tailscaled unit for resolver mount check: %w", err)
	}
	return tailscaleResolverBindSource(string(raw)), nil
}

func tailscaleResolverBindSource(unit string) string {
	if source := tailscaleResolverGuardSource(unit); source != "" {
		return source
	}
	return tailscaleResolverLegacyBindSource(unit)
}

func tailscaleResolverGuardSource(unit string) string {
	for _, line := range strings.Split(unit, "\n") {
		value, ok := strings.CutPrefix(strings.TrimSpace(line), "ExecStart=")
		if ok {
			args := strings.Fields(value)
			if len(args) >= 2 &&
				args[1] == "tailscale-resolver-exec" &&
				validGuardedTailscaleExecStart(args) {
				return args[3]
			}
		}
	}
	return ""
}

func tailscaleResolverLegacyBindSource(unit string) string {
	for _, line := range strings.Split(unit, "\n") {
		value, ok := strings.CutPrefix(strings.TrimSpace(line), "BindReadOnlyPaths=")
		if !ok {
			continue
		}
		source, target, ok := strings.Cut(value, ":")
		if ok && target == "/etc/resolv.conf" {
			return source
		}
	}
	return ""
}

func systemdServiceMainPID(unit string) (int, error) {
	output, err := exec.Command("systemctl", "show", "-p", "MainPID", "--value", unit).Output()
	if err != nil {
		return 0, fmt.Errorf("systemctl show MainPID for %s: %w", unit, err)
	}
	text := strings.TrimSpace(string(output))
	if text == "" || text == "0" {
		return 0, nil
	}
	pid, err := strconv.Atoi(text)
	if err != nil {
		return 0, fmt.Errorf("parse MainPID for %s: %w", unit, err)
	}
	return pid, nil
}
