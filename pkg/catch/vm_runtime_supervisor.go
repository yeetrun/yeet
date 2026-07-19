// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/yeetrun/yeet/pkg/db"
)

const VMRuntimeNoFallbackExitCode = 76

var ErrVMRuntimeNoFallback = errors.New("VM runtime candidate and configured fallback both failed")

type vmRuntimeProcessWaiter struct {
	done chan struct{}
	err  error
}

func newVMRuntimeProcessWaiter(cmd *exec.Cmd) *vmRuntimeProcessWaiter {
	waiter := &vmRuntimeProcessWaiter{done: make(chan struct{})}
	go func() {
		waiter.err = cmd.Wait()
		close(waiter.done)
	}()
	return waiter
}

type vmRuntimeLaunchAttempt struct {
	artifact db.VMRuntimeArtifactConfig
	version  string
	cmd      *exec.Cmd
	console  *os.File
	broker   *vmConsoleBroker
	waiter   *vmRuntimeProcessWaiter
	markerID vmJailerFileIdentity
	marker   vmRuntimeRunningMarker
	cleanup  func()
	stopOnce sync.Once
}

type vmRuntimeSupervisorDeps struct {
	lock       func(context.Context, VMConsoleProxyConfig, func() error) error
	selectRun  func(VMConsoleProxyConfig) (vmRuntimeLaunchSelection, error)
	confirm    func(VMConsoleProxyConfig, vmRuntimeLaunchSelection) error
	preflight  func(context.Context, db.VMRuntimeArtifactConfig) error
	launch     func(context.Context, VMConsoleProxyConfig, db.VMRuntimeArtifactConfig, string, string) (*vmRuntimeLaunchAttempt, error)
	ready      func(context.Context, VMConsoleProxyConfig, *vmRuntimeLaunchAttempt) error
	stop       func(VMConsoleProxyConfig, *vmRuntimeLaunchAttempt)
	supervise  func(context.Context, VMConsoleProxyConfig, net.Listener, *vmRuntimeLaunchAttempt) error
	write      func(string, vmRuntimeTrialResult) error
	random     io.Reader
	now        func() time.Time
	retryDelay time.Duration
	retryLimit int
}

func defaultVMRuntimeSupervisorDeps(constructProcess vmConsoleProcessConstructor) vmRuntimeSupervisorDeps {
	launchDeps := defaultVMRuntimeLaunchDeps()
	controlDeps := defaultVMRuntimeControlFileDeps()
	deps := vmRuntimeSupervisorDeps{
		random:     rand.Reader,
		now:        time.Now,
		retryDelay: 100 * time.Millisecond,
		retryLimit: 50,
	}
	deps.lock = func(ctx context.Context, cfg VMConsoleProxyConfig, fn func() error) error {
		if strings.TrimSpace(cfg.RuntimeDataRoot) == "" {
			return fn()
		}
		return WithVMRuntimeRootLock(ctx, cfg.RuntimeDataRoot, fn)
	}
	deps.selectRun = func(cfg VMConsoleProxyConfig) (vmRuntimeLaunchSelection, error) {
		return selectVMRuntimeLaunch(cfg.RuntimeDescriptor, cfg.Service, 0, 0, launchDeps)
	}
	deps.confirm = func(cfg VMConsoleProxyConfig, selection vmRuntimeLaunchSelection) error {
		snapshot, err := readVMRuntimeDescriptorSnapshotWithOwner(cfg.RuntimeDescriptor, cfg.Service, 0, 0)
		if err != nil {
			return err
		}
		if snapshot.SHA256 != selection.DescriptorSHA256 {
			return fmt.Errorf("VM runtime descriptor changed before launch")
		}
		if selection.Trial {
			if snapshot.Descriptor.Staged == nil || *snapshot.Descriptor.Staged != selection.Selected || selection.Fallback == nil || snapshot.Descriptor.Configured != *selection.Fallback {
				return fmt.Errorf("VM runtime trial selection changed before launch")
			}
		} else if snapshot.Descriptor.Configured != selection.Selected {
			return fmt.Errorf("configured VM runtime selection changed before launch")
		}
		return nil
	}
	deps.preflight = func(ctx context.Context, artifact db.VMRuntimeArtifactConfig) error {
		_, err := verifyVMRuntimeLaunchArtifact(ctx, artifact, launchDeps)
		return err
	}
	deps.launch = func(ctx context.Context, cfg VMConsoleProxyConfig, artifact db.VMRuntimeArtifactConfig, descriptorSHA256, launchID string) (*vmRuntimeLaunchAttempt, error) {
		return launchVMRuntimeAttempt(ctx, cfg, artifact, descriptorSHA256, launchID, constructProcess, launchDeps, controlDeps)
	}
	deps.ready = waitVMRuntimeHostReady
	deps.stop = func(cfg VMConsoleProxyConfig, attempt *vmRuntimeLaunchAttempt) {
		stopVMRuntimeAttempt(cfg, attempt, controlDeps)
	}
	deps.supervise = superviseVMRuntimeAttempt
	deps.write = func(path string, result vmRuntimeTrialResult) error {
		_, err := writeVMRuntimeTrialResult(path, result, controlDeps)
		return err
	}
	return deps
}

func runVMRuntimeConsoleProxy(
	ctx context.Context,
	cfg VMConsoleProxyConfig,
	listener net.Listener,
	constructProcess vmConsoleProcessConstructor,
) error {
	return runVMRuntimeLaunchSequence(ctx, cfg, listener, vmRuntimeLaunchSelection{}, defaultVMRuntimeSupervisorDeps(constructProcess))
}

func runVMRuntimeLaunchSequence(
	ctx context.Context,
	cfg VMConsoleProxyConfig,
	listener net.Listener,
	selection vmRuntimeLaunchSelection,
	deps vmRuntimeSupervisorDeps,
) error {
	if !validVMRuntimeSupervisorDeps(deps) {
		return fmt.Errorf("VM runtime supervisor dependencies are incomplete")
	}
	launchID, err := newVMRuntimeLaunchID(deps.random)
	if err != nil {
		return err
	}
	startedAt := deps.now().UTC()
	selection, attempt, selectionConfirmed, err := selectAndLaunchVMRuntimePrimary(ctx, cfg, selection, launchID, deps)
	if err != nil && !selectionConfirmed {
		return err
	}
	if err == nil {
		err = deps.ready(ctx, cfg, attempt)
	}
	if err == nil {
		return superviseHealthyVMRuntimePrimary(ctx, cfg, listener, selection, attempt, launchID, startedAt, deps)
	}
	return recoverVMRuntimeLaunch(ctx, cfg, listener, selection, attempt, launchID, startedAt, err, deps)
}

func validVMRuntimeSupervisorDeps(deps vmRuntimeSupervisorDeps) bool {
	return deps.random != nil && deps.now != nil && deps.lock != nil && deps.confirm != nil && deps.preflight != nil &&
		deps.launch != nil && deps.ready != nil && deps.stop != nil && deps.supervise != nil && deps.write != nil
}

func selectAndLaunchVMRuntimePrimary(ctx context.Context, cfg VMConsoleProxyConfig, selection vmRuntimeLaunchSelection, launchID string, deps vmRuntimeSupervisorDeps) (vmRuntimeLaunchSelection, *vmRuntimeLaunchAttempt, bool, error) {
	var attempt *vmRuntimeLaunchAttempt
	confirmed := false
	err := deps.lock(ctx, cfg, func() error {
		if deps.selectRun != nil {
			var err error
			selection, err = deps.selectRun(cfg)
			if err != nil {
				return fmt.Errorf("select VM runtime launch: %w", err)
			}
		}
		if err := deps.confirm(cfg, selection); err != nil {
			return fmt.Errorf("confirm VM runtime launch selection: %w", err)
		}
		confirmed = true
		if err := preflightVMRuntimeFallback(ctx, selection, deps); err != nil {
			return err
		}
		var launchErr error
		attempt, launchErr = deps.launch(ctx, cfg, selection.Selected, selection.DescriptorSHA256, launchID)
		return launchErr
	})
	return selection, attempt, confirmed, err
}

func preflightVMRuntimeFallback(ctx context.Context, selection vmRuntimeLaunchSelection, deps vmRuntimeSupervisorDeps) error {
	if !selection.Trial {
		return nil
	}
	if selection.Fallback == nil {
		return fmt.Errorf("VM runtime trial has no configured fallback")
	}
	if err := deps.preflight(ctx, *selection.Fallback); err != nil {
		return fmt.Errorf("verify configured VM runtime fallback before candidate launch: %w", err)
	}
	return nil
}

func superviseHealthyVMRuntimePrimary(ctx context.Context, cfg VMConsoleProxyConfig, listener net.Listener, selection vmRuntimeLaunchSelection, attempt *vmRuntimeLaunchAttempt, launchID string, startedAt time.Time, deps vmRuntimeSupervisorDeps) error {
	if selection.Trial {
		running := vmRuntimeTrialIdentityForArtifact(selection.Selected)
		result := vmRuntimeTrialResult{
			SchemaVersion: vmRuntimeTrialResultSchemaVersion,
			Service:       cfg.Service, DescriptorSHA256: selection.DescriptorSHA256, LaunchID: launchID,
			Candidate: vmRuntimeTrialIdentityForArtifact(selection.Selected), Configured: vmRuntimeTrialIdentityForArtifact(*selection.Fallback), Running: &running,
			Outcome: vmRuntimeTrialHealthy, RunnerPID: attempt.marker.RunnerPID, ChildPID: attempt.marker.ChildPID,
			StartedAt: startedAt.Format(time.RFC3339Nano), CompletedAt: deps.now().UTC().Format(time.RFC3339Nano),
		}
		if err := writeVMRuntimeTrialResultWithRetry(ctx, deps, cfg.RuntimeTrialResult, result); err != nil {
			deps.stop(cfg, attempt)
			return fmt.Errorf("publish healthy VM runtime trial result: %w", err)
		}
	}
	defer deps.stop(cfg, attempt)
	return deps.supervise(ctx, cfg, listener, attempt)
}

func recoverVMRuntimeLaunch(ctx context.Context, cfg VMConsoleProxyConfig, listener net.Listener, selection vmRuntimeLaunchSelection, attempt *vmRuntimeLaunchAttempt, launchID string, startedAt time.Time, primaryErr error, deps vmRuntimeSupervisorDeps) error {
	if handled, err := handleUnrecoverableVMRuntimeLaunch(ctx, cfg, selection, attempt, primaryErr, deps); handled {
		return err
	}
	fallback, fallbackSelectionConfirmed, fallbackErr := launchVMRuntimeFallback(ctx, cfg, selection, attempt, launchID, deps)
	if fallbackErr != nil && !fallbackSelectionConfirmed {
		return fallbackErr
	}
	if fallbackErr == nil {
		fallbackErr = deps.ready(ctx, cfg, fallback)
	}
	if fallbackErr == nil {
		return superviseHealthyVMRuntimeFallback(ctx, cfg, listener, selection, fallback, launchID, startedAt, primaryErr, deps)
	}
	if fallback != nil {
		deps.stop(cfg, fallback)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return publishTerminalVMRuntimeTrial(ctx, cfg, selection, attempt, fallback, launchID, startedAt, primaryErr, fallbackErr, deps)
}

func handleUnrecoverableVMRuntimeLaunch(ctx context.Context, cfg VMConsoleProxyConfig, selection vmRuntimeLaunchSelection, attempt *vmRuntimeLaunchAttempt, primaryErr error, deps vmRuntimeSupervisorDeps) (bool, error) {
	if ctx.Err() != nil {
		if attempt != nil {
			deps.stop(cfg, attempt)
		}
		return true, ctx.Err()
	}
	if selection.Trial {
		return false, nil
	}
	if attempt != nil {
		deps.stop(cfg, attempt)
	}
	return true, fmt.Errorf("VM runtime failed host readiness: %w", primaryErr)
}

func launchVMRuntimeFallback(ctx context.Context, cfg VMConsoleProxyConfig, selection vmRuntimeLaunchSelection, attempt *vmRuntimeLaunchAttempt, launchID string, deps vmRuntimeSupervisorDeps) (*vmRuntimeLaunchAttempt, bool, error) {
	var fallback *vmRuntimeLaunchAttempt
	confirmed := false
	err := deps.lock(ctx, cfg, func() error {
		if attempt != nil {
			deps.stop(cfg, attempt)
		}
		if err := deps.confirm(cfg, selection); err != nil {
			return fmt.Errorf("confirm VM runtime fallback selection: %w", err)
		}
		confirmed = true
		var launchErr error
		fallback, launchErr = deps.launch(ctx, cfg, *selection.Fallback, selection.DescriptorSHA256, launchID)
		return launchErr
	})
	return fallback, confirmed, err
}

func superviseHealthyVMRuntimeFallback(ctx context.Context, cfg VMConsoleProxyConfig, listener net.Listener, selection vmRuntimeLaunchSelection, fallback *vmRuntimeLaunchAttempt, launchID string, startedAt time.Time, primaryErr error, deps vmRuntimeSupervisorDeps) error {
	running := vmRuntimeTrialIdentityForArtifact(*selection.Fallback)
	result := vmRuntimeTrialResult{
		SchemaVersion: vmRuntimeTrialResultSchemaVersion,
		Service:       cfg.Service, DescriptorSHA256: selection.DescriptorSHA256, LaunchID: launchID,
		Candidate: vmRuntimeTrialIdentityForArtifact(selection.Selected), Configured: vmRuntimeTrialIdentityForArtifact(*selection.Fallback), Running: &running,
		Outcome: vmRuntimeTrialFailedRolledBack, RunnerPID: fallback.marker.RunnerPID, ChildPID: fallback.marker.ChildPID,
		StartedAt: startedAt.Format(time.RFC3339Nano), CompletedAt: deps.now().UTC().Format(time.RFC3339Nano), Error: boundedVMRuntimeTrialError(primaryErr),
	}
	if err := writeVMRuntimeTrialResultWithRetry(ctx, deps, cfg.RuntimeTrialResult, result); err != nil {
		deps.stop(cfg, fallback)
		return fmt.Errorf("configured VM runtime fallback was healthy, but its trial result could not be published: %w", err)
	}
	defer deps.stop(cfg, fallback)
	return deps.supervise(ctx, cfg, listener, fallback)
}

func publishTerminalVMRuntimeTrial(ctx context.Context, cfg VMConsoleProxyConfig, selection vmRuntimeLaunchSelection, attempt, fallback *vmRuntimeLaunchAttempt, launchID string, startedAt time.Time, primaryErr, fallbackErr error, deps vmRuntimeSupervisorDeps) error {
	childPID := vmRuntimeFailedTrialChildPID(attempt, fallback)
	result := vmRuntimeTrialResult{
		SchemaVersion: vmRuntimeTrialResultSchemaVersion,
		Service:       cfg.Service, DescriptorSHA256: selection.DescriptorSHA256, LaunchID: launchID,
		Candidate:  vmRuntimeTrialIdentityForArtifact(selection.Selected),
		Configured: vmRuntimeTrialIdentityForArtifact(*selection.Fallback),
		Outcome:    vmRuntimeTrialFailedNoFallback, RunnerPID: os.Getpid(), ChildPID: childPID,
		StartedAt: startedAt.Format(time.RFC3339Nano), CompletedAt: deps.now().UTC().Format(time.RFC3339Nano),
		Error: boundedVMRuntimeTrialError(errors.Join(primaryErr, fallbackErr)),
	}
	if writeErr := writeVMRuntimeTrialResultWithRetry(ctx, deps, cfg.RuntimeTrialResult, result); writeErr != nil {
		return errors.Join(primaryErr, fallbackErr, fmt.Errorf("publish terminal VM runtime trial result: %w", writeErr))
	}
	return errors.Join(ErrVMRuntimeNoFallback, primaryErr, fallbackErr)
}

func vmRuntimeFailedTrialChildPID(attempt, fallback *vmRuntimeLaunchAttempt) int {
	if fallback != nil {
		return fallback.marker.ChildPID
	}
	if attempt != nil {
		return attempt.marker.ChildPID
	}
	return 0
}

func writeVMRuntimeTrialResultWithRetry(ctx context.Context, deps vmRuntimeSupervisorDeps, path string, result vmRuntimeTrialResult) error {
	limit := deps.retryLimit
	if limit <= 0 {
		limit = 1
	}
	var last error
	for attempt := 0; attempt < limit; attempt++ {
		if err := deps.write(path, result); err == nil {
			return nil
		} else {
			last = err
		}
		if attempt+1 == limit || deps.retryDelay <= 0 {
			continue
		}
		timer := time.NewTimer(deps.retryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(last, ctx.Err())
		case <-timer.C:
		}
	}
	return last
}

func newVMRuntimeLaunchID(random io.Reader) (string, error) {
	var raw [32]byte
	if _, err := io.ReadFull(random, raw[:]); err != nil {
		return "", fmt.Errorf("generate VM runtime launch ID: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func boundedVMRuntimeTrialError(err error) string {
	const max = 4096
	text := strings.TrimSpace(err.Error())
	if len(text) > max {
		text = text[:max]
	}
	return text
}

func launchVMRuntimeAttempt(
	ctx context.Context,
	cfg VMConsoleProxyConfig,
	artifact db.VMRuntimeArtifactConfig,
	descriptorSHA256, launchID string,
	constructProcess vmConsoleProcessConstructor,
	launchDeps vmRuntimeLaunchDeps,
	controlDeps vmRuntimeControlFileDeps,
) (*vmRuntimeLaunchAttempt, error) {
	version, err := verifyVMRuntimeLaunchArtifact(ctx, artifact, launchDeps)
	if err != nil {
		return nil, err
	}
	attemptCfg := cfg
	attemptCfg.Firecracker = artifact.Firecracker
	attemptCfg.Jailer = artifact.Jailer
	cmd, cleanupProcess, err := constructProcess(ctx, attemptCfg)
	if err != nil {
		return nil, err
	}
	cleanupOnError := true
	defer func() {
		if cleanupOnError {
			cleanupProcess()
		}
	}()
	if _, err := collectVMRuntimeLaunchEvidence(artifact, launchDeps.evidence); err != nil {
		return nil, fmt.Errorf("revalidate VM runtime immediately before launch: %w", err)
	}
	console, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("start Firecracker console PTY: %w", err)
	}
	waiter := newVMRuntimeProcessWaiter(cmd)
	markerID, marker, err := writeVMRuntimeRunningMarker(
		cfg.RuntimeRunningMarker, cfg.Service, artifact, descriptorSHA256, launchID,
		os.Getpid(), cmd.Process.Pid, controlDeps,
	)
	if err != nil {
		_ = cmd.Process.Kill()
		<-waiter.done
		_ = console.Close()
		return nil, err
	}
	guestStopped := make(chan vmGuestStopKind, 1)
	broker := newVMConsoleBroker(console, os.Stdout, guestStopped)
	go broker.copyOutput()
	cleanupOnError = false
	return &vmRuntimeLaunchAttempt{
		artifact: artifact, version: version, cmd: cmd, console: console, broker: broker,
		waiter: waiter, markerID: markerID, marker: marker, cleanup: cleanupProcess,
	}, nil
}

func stopVMRuntimeAttempt(cfg VMConsoleProxyConfig, attempt *vmRuntimeLaunchAttempt, deps vmRuntimeControlFileDeps) {
	if attempt == nil {
		return
	}
	attempt.stopOnce.Do(func() {
		if attempt.cmd != nil && attempt.cmd.Process != nil {
			_ = attempt.cmd.Process.Kill()
		}
		if attempt.waiter != nil {
			<-attempt.waiter.done
		}
		if attempt.console != nil {
			_ = attempt.console.Close()
		}
		if err := removeVMRuntimeRunningMarker(
			cfg.RuntimeRunningMarker, cfg.Service, attempt.markerID,
			attempt.marker.RunnerPID, attempt.marker.ChildPID, deps,
		); err != nil {
			fmt.Fprintf(os.Stderr, "warning: clean up VM runtime marker: %v\n", err)
		}
		if attempt.cleanup != nil {
			attempt.cleanup()
		}
	})
}

func superviseVMRuntimeAttempt(ctx context.Context, cfg VMConsoleProxyConfig, listener net.Listener, attempt *vmRuntimeLaunchAttempt) error {
	go attempt.broker.accept(listener)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case kind := <-attempt.broker.guestStopped:
		return handleVMRuntimeGuestStopped(ctx, cfg, attempt, kind)
	case <-attempt.waiter.done:
		return handleVMRuntimeProcessStopped(ctx, cfg, attempt)
	}
}

func handleVMRuntimeGuestStopped(ctx context.Context, cfg VMConsoleProxyConfig, attempt *vmRuntimeLaunchAttempt, kind vmGuestStopKind) error {
	if attempt.cmd != nil && attempt.cmd.Process != nil {
		_ = attempt.cmd.Process.Kill()
	}
	<-attempt.waiter.done
	return handleVMRuntimeGuestStopKind(ctx, cfg, kind)
}

func handleVMRuntimeProcessStopped(ctx context.Context, cfg VMConsoleProxyConfig, attempt *vmRuntimeLaunchAttempt) error {
	select {
	case kind := <-attempt.broker.guestStopped:
		return handleVMRuntimeGuestStopKind(ctx, cfg, kind)
	default:
	}
	if attempt.waiter.err != nil {
		return fmt.Errorf("wait for Firecracker: %w", attempt.waiter.err)
	}
	return nil
}

func handleVMRuntimeGuestStopKind(ctx context.Context, cfg VMConsoleProxyConfig, kind vmGuestStopKind) error {
	if kind != vmGuestStopReboot {
		return nil
	}
	runVMGuestRebootHook(ctx, cfg)
	return ErrVMGuestReboot
}

func waitVMRuntimeHostReady(ctx context.Context, cfg VMConsoleProxyConfig, attempt *vmRuntimeLaunchAttempt) error {
	readyDeadline := time.NewTimer(25 * time.Second)
	defer readyDeadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	if err := waitVMRuntimeInitialHostReady(ctx, cfg, attempt, readyDeadline, ticker); err != nil {
		return err
	}
	return waitVMRuntimeHostStabilization(ctx, cfg, attempt, ticker)
}

func waitVMRuntimeInitialHostReady(ctx context.Context, cfg VMConsoleProxyConfig, attempt *vmRuntimeLaunchAttempt, deadline *time.Timer, ticker *time.Ticker) error {
	var lastErr error
	for {
		if err := probeVMRuntimeHostReady(ctx, cfg, attempt); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-attempt.waiter.done:
			return fmt.Errorf("VM runtime exited before host readiness: %w", attempt.waiter.err)
		case <-deadline.C:
			return fmt.Errorf("VM runtime host readiness timed out: %w", lastErr)
		case <-ticker.C:
		}
	}
}

func waitVMRuntimeHostStabilization(ctx context.Context, cfg VMConsoleProxyConfig, attempt *vmRuntimeLaunchAttempt, ticker *time.Ticker) error {
	stable := time.NewTimer(5 * time.Second)
	defer stable.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-attempt.waiter.done:
			return fmt.Errorf("VM runtime exited during host stabilization: %w", attempt.waiter.err)
		case <-ticker.C:
			if err := probeVMRuntimeHostReady(ctx, cfg, attempt); err != nil {
				return fmt.Errorf("VM runtime lost host readiness during stabilization: %w", err)
			}
		case <-stable.C:
			return probeVMRuntimeHostReady(ctx, cfg, attempt)
		}
	}
}

func probeVMRuntimeHostReady(ctx context.Context, cfg VMConsoleProxyConfig, attempt *vmRuntimeLaunchAttempt) error {
	if !vmRuntimeProcessAlive(attempt.marker.RunnerPID) || !vmRuntimeProcessAlive(attempt.marker.ChildPID) {
		return fmt.Errorf("VM runtime runner or child is not active")
	}
	marker, err := readTrustedVMRuntimeRunningMarker(cfg.RuntimeRunningMarker, cfg.Service, 0, 0)
	if err != nil {
		return err
	}
	if marker != attempt.marker {
		return fmt.Errorf("VM runtime running marker does not match this launch")
	}
	readiness, err := vmJailerReadinessForRoot(cfg.ServiceRoot)
	if err != nil {
		return err
	}
	if readiness != vmJailerReady {
		return fmt.Errorf("VM jailer isolation is %s", readiness)
	}
	return probeFirecrackerInstance(ctx, cfg.APISocket, vmJailerID(cfg.Service), attempt.version)
}

func probeFirecrackerInstance(ctx context.Context, socket, expectedID, expectedVersion string) error {
	probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, "http://firecracker/", nil)
	if err != nil {
		return err
	}
	response, err := (&http.Client{Transport: transport}).Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("firecracker instance API returned %s", response.Status)
	}
	var state struct {
		ID         string `json:"id"`
		State      string `json:"state"`
		VMMVersion string `json:"vmm_version"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<10))
	if err := decoder.Decode(&state); err != nil {
		return fmt.Errorf("decode Firecracker instance state: %w", err)
	}
	if state.ID != expectedID || state.State != "Running" || state.VMMVersion != expectedVersion {
		return fmt.Errorf("firecracker instance state is id=%q state=%q version=%q, want id=%q state=Running version=%q", state.ID, state.State, state.VMMVersion, expectedID, expectedVersion)
	}
	return nil
}
