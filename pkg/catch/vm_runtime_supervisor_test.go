// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestVMRuntimeTrialHealthyCandidatePublishesBeforeSupervision(t *testing.T) {
	selection := vmRuntimeSupervisorTestSelection()
	supervised := errors.New("supervised candidate")
	var preflighted, launched []string
	var result vmRuntimeTrialResult
	deps := vmRuntimeSupervisorTestDeps()
	deps.preflight = func(_ context.Context, artifact db.VMRuntimeArtifactConfig) error {
		preflighted = append(preflighted, artifact.ID)
		return nil
	}
	deps.launch = func(_ context.Context, _ VMConsoleProxyConfig, artifact db.VMRuntimeArtifactConfig, generation, launchID string) (*vmRuntimeLaunchAttempt, error) {
		launched = append(launched, artifact.ID)
		return vmRuntimeSupervisorFakeAttempt(artifact, generation, launchID, 101, 202), nil
	}
	deps.write = func(_ string, got vmRuntimeTrialResult) error { result = got; return nil }
	deps.supervise = func(context.Context, VMConsoleProxyConfig, net.Listener, *vmRuntimeLaunchAttempt) error {
		return supervised
	}

	err := runVMRuntimeLaunchSequence(context.Background(), vmRuntimeSupervisorTestConfig(), nil, selection, deps)
	if !errors.Is(err, supervised) {
		t.Fatalf("run error = %v", err)
	}
	if !reflect.DeepEqual(preflighted, []string{selection.Fallback.ID}) || !reflect.DeepEqual(launched, []string{selection.Selected.ID}) {
		t.Fatalf("preflighted=%v launched=%v", preflighted, launched)
	}
	if result.Outcome != vmRuntimeTrialHealthy || result.Running == nil || !result.Running.matches(selection.Selected) {
		t.Fatalf("healthy result = %#v", result)
	}
}

func TestVMRuntimeTrialFallsBackInSameSupervisor(t *testing.T) {
	selection := vmRuntimeSupervisorTestSelection()
	candidateFailure := errors.New("candidate exited during stabilization")
	var launched, stopped []string
	var result vmRuntimeTrialResult
	deps := vmRuntimeSupervisorTestDeps()
	deps.launch = func(_ context.Context, _ VMConsoleProxyConfig, artifact db.VMRuntimeArtifactConfig, generation, launchID string) (*vmRuntimeLaunchAttempt, error) {
		launched = append(launched, artifact.ID)
		pid := 202
		if artifact == *selection.Fallback {
			pid = 303
		}
		return vmRuntimeSupervisorFakeAttempt(artifact, generation, launchID, 101, pid), nil
	}
	deps.ready = func(_ context.Context, _ VMConsoleProxyConfig, attempt *vmRuntimeLaunchAttempt) error {
		if attempt.artifact == selection.Selected {
			return candidateFailure
		}
		return nil
	}
	deps.stop = func(_ VMConsoleProxyConfig, attempt *vmRuntimeLaunchAttempt) {
		stopped = append(stopped, attempt.artifact.ID)
	}
	deps.write = func(_ string, got vmRuntimeTrialResult) error { result = got; return nil }

	if err := runVMRuntimeLaunchSequence(context.Background(), vmRuntimeSupervisorTestConfig(), nil, selection, deps); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(launched, []string{selection.Selected.ID, selection.Fallback.ID}) {
		t.Fatalf("launch order = %v", launched)
	}
	if result.Outcome != vmRuntimeTrialFailedRolledBack || result.Running == nil || !result.Running.matches(*selection.Fallback) || result.Error != candidateFailure.Error() {
		t.Fatalf("fallback result = %#v", result)
	}
	if !reflect.DeepEqual(stopped, []string{selection.Selected.ID, selection.Fallback.ID}) {
		t.Fatalf("stopped attempts = %v", stopped)
	}
}

func TestVMRuntimeTrialTerminalFailureIsDurableAndNonRestarting(t *testing.T) {
	selection := vmRuntimeSupervisorTestSelection()
	var result vmRuntimeTrialResult
	deps := vmRuntimeSupervisorTestDeps()
	deps.launch = func(_ context.Context, _ VMConsoleProxyConfig, artifact db.VMRuntimeArtifactConfig, _, _ string) (*vmRuntimeLaunchAttempt, error) {
		return nil, errors.New("cannot launch " + artifact.ID)
	}
	deps.write = func(_ string, got vmRuntimeTrialResult) error { result = got; return nil }

	err := runVMRuntimeLaunchSequence(context.Background(), vmRuntimeSupervisorTestConfig(), nil, selection, deps)
	if !errors.Is(err, ErrVMRuntimeNoFallback) {
		t.Fatalf("terminal error = %v", err)
	}
	if result.Outcome != vmRuntimeTrialFailedNoFallback || result.Running != nil || result.ChildPID != 0 {
		t.Fatalf("terminal result = %#v", result)
	}
}

func TestVMRuntimeTrialFallbackResultPublicationFailureRestartsInsteadOfWedging(t *testing.T) {
	selection := vmRuntimeSupervisorTestSelection()
	var stopped []string
	deps := vmRuntimeSupervisorTestDeps()
	deps.retryLimit = 2
	deps.launch = func(_ context.Context, _ VMConsoleProxyConfig, artifact db.VMRuntimeArtifactConfig, generation, launchID string) (*vmRuntimeLaunchAttempt, error) {
		return vmRuntimeSupervisorFakeAttempt(artifact, generation, launchID, 101, 202), nil
	}
	deps.ready = func(_ context.Context, _ VMConsoleProxyConfig, attempt *vmRuntimeLaunchAttempt) error {
		if attempt.artifact == selection.Selected {
			return errors.New("candidate failed")
		}
		return nil
	}
	deps.write = func(string, vmRuntimeTrialResult) error { return errors.New("result fsync failed") }
	deps.stop = func(_ VMConsoleProxyConfig, attempt *vmRuntimeLaunchAttempt) {
		stopped = append(stopped, attempt.artifact.ID)
	}
	deps.supervise = func(context.Context, VMConsoleProxyConfig, net.Listener, *vmRuntimeLaunchAttempt) error {
		t.Fatal("fallback supervised without a durable result")
		return nil
	}
	err := runVMRuntimeLaunchSequence(context.Background(), vmRuntimeSupervisorTestConfig(), nil, selection, deps)
	if err == nil || errors.Is(err, ErrVMRuntimeNoFallback) || !strings.Contains(err.Error(), "could not be published") {
		t.Fatalf("publication error = %v", err)
	}
	if !reflect.DeepEqual(stopped, []string{selection.Selected.ID, selection.Fallback.ID}) {
		t.Fatalf("stopped attempts = %v", stopped)
	}
}

func TestVMRuntimeTrialTerminalResultPublicationFailureRemainsRestartable(t *testing.T) {
	selection := vmRuntimeSupervisorTestSelection()
	deps := vmRuntimeSupervisorTestDeps()
	deps.retryLimit = 1
	deps.launch = func(_ context.Context, _ VMConsoleProxyConfig, artifact db.VMRuntimeArtifactConfig, _, _ string) (*vmRuntimeLaunchAttempt, error) {
		return nil, errors.New("cannot launch " + artifact.ID)
	}
	deps.write = func(string, vmRuntimeTrialResult) error { return errors.New("result fsync failed") }
	err := runVMRuntimeLaunchSequence(context.Background(), vmRuntimeSupervisorTestConfig(), nil, selection, deps)
	if err == nil || errors.Is(err, ErrVMRuntimeNoFallback) {
		t.Fatalf("undurable terminal result must use restartable error, got %v", err)
	}
}

func TestVMRuntimeSelectionOccursUnderLaunchLock(t *testing.T) {
	selection := vmRuntimeSupervisorTestSelection()
	deps := vmRuntimeSupervisorTestDeps()
	locked := false
	deps.lock = func(_ context.Context, _ VMConsoleProxyConfig, fn func() error) error {
		locked = true
		defer func() { locked = false }()
		return fn()
	}
	deps.selectRun = func(VMConsoleProxyConfig) (vmRuntimeLaunchSelection, error) {
		if !locked {
			t.Fatal("runtime descriptor selected outside the launch lock")
		}
		return selection, nil
	}
	if err := runVMRuntimeLaunchSequence(context.Background(), vmRuntimeSupervisorTestConfig(), nil, vmRuntimeLaunchSelection{}, deps); err != nil {
		t.Fatal(err)
	}
}

func TestVMRuntimeSelectionDriftRemainsRestartableWithoutTerminalResult(t *testing.T) {
	selection := vmRuntimeSupervisorTestSelection()
	deps := vmRuntimeSupervisorTestDeps()
	deps.confirm = func(VMConsoleProxyConfig, vmRuntimeLaunchSelection) error {
		return errors.New("descriptor generation changed")
	}
	launched := false
	deps.launch = func(context.Context, VMConsoleProxyConfig, db.VMRuntimeArtifactConfig, string, string) (*vmRuntimeLaunchAttempt, error) {
		launched = true
		return nil, nil
	}
	written := false
	deps.write = func(string, vmRuntimeTrialResult) error {
		written = true
		return nil
	}
	err := runVMRuntimeLaunchSequence(context.Background(), vmRuntimeSupervisorTestConfig(), nil, selection, deps)
	if err == nil || errors.Is(err, ErrVMRuntimeNoFallback) || !strings.Contains(err.Error(), "descriptor generation changed") {
		t.Fatalf("selection drift error = %v", err)
	}
	if launched || written {
		t.Fatalf("selection drift launched=%t wrote-terminal-result=%t", launched, written)
	}
}

func TestVMRuntimeFallbackSelectionDriftRemainsRestartableWithoutTerminalResult(t *testing.T) {
	selection := vmRuntimeSupervisorTestSelection()
	deps := vmRuntimeSupervisorTestDeps()
	confirmCalls := 0
	deps.confirm = func(VMConsoleProxyConfig, vmRuntimeLaunchSelection) error {
		confirmCalls++
		if confirmCalls == 2 {
			return errors.New("descriptor changed before fallback")
		}
		return nil
	}
	launches := 0
	deps.launch = func(_ context.Context, _ VMConsoleProxyConfig, artifact db.VMRuntimeArtifactConfig, generation, launchID string) (*vmRuntimeLaunchAttempt, error) {
		launches++
		return vmRuntimeSupervisorFakeAttempt(artifact, generation, launchID, 101, 202), nil
	}
	deps.ready = func(context.Context, VMConsoleProxyConfig, *vmRuntimeLaunchAttempt) error {
		return errors.New("candidate failed readiness")
	}
	written := false
	deps.write = func(string, vmRuntimeTrialResult) error {
		written = true
		return nil
	}
	err := runVMRuntimeLaunchSequence(context.Background(), vmRuntimeSupervisorTestConfig(), nil, selection, deps)
	if err == nil || errors.Is(err, ErrVMRuntimeNoFallback) || !strings.Contains(err.Error(), "descriptor changed before fallback") {
		t.Fatalf("fallback selection drift error = %v", err)
	}
	if launches != 1 || written {
		t.Fatalf("fallback selection drift launches=%d wrote-terminal-result=%t", launches, written)
	}
}

func TestVMRuntimeTrialCancellationDoesNotLaunchFallback(t *testing.T) {
	selection := vmRuntimeSupervisorTestSelection()
	ctx, cancel := context.WithCancel(context.Background())
	var launches int
	deps := vmRuntimeSupervisorTestDeps()
	deps.launch = func(_ context.Context, _ VMConsoleProxyConfig, artifact db.VMRuntimeArtifactConfig, generation, launchID string) (*vmRuntimeLaunchAttempt, error) {
		launches++
		return vmRuntimeSupervisorFakeAttempt(artifact, generation, launchID, 101, 202), nil
	}
	deps.ready = func(context.Context, VMConsoleProxyConfig, *vmRuntimeLaunchAttempt) error {
		cancel()
		return context.Canceled
	}
	if err := runVMRuntimeLaunchSequence(ctx, vmRuntimeSupervisorTestConfig(), nil, selection, deps); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v", err)
	}
	if launches != 1 {
		t.Fatalf("launches after cancellation = %d", launches)
	}
}

func TestVMRuntimeTrialDoesNotUseGuestAgentAsAuthority(t *testing.T) {
	selection := vmRuntimeSupervisorTestSelection()
	deps := vmRuntimeSupervisorTestDeps()
	// The supervisor dependency surface deliberately contains no guest-agent or
	// vsock readiness function. Host readiness alone completes this trial.
	if err := runVMRuntimeLaunchSequence(context.Background(), vmRuntimeSupervisorTestConfig(), nil, selection, deps); err != nil {
		t.Fatal(err)
	}
}

func TestProbeFirecrackerInstanceRequiresExactHostState(t *testing.T) {
	dir := shortUnixSocketDirForTest(t)
	socket := filepath.Join(dir, "fc.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"devbox","state":"Running","vmm_version":"1.17.0"}`))
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	if err := probeFirecrackerInstance(context.Background(), socket, "devbox", "1.17.0"); err != nil {
		t.Fatal(err)
	}
	if err := probeFirecrackerInstance(context.Background(), socket, "devbox", "1.16.1"); err == nil {
		t.Fatal("version mismatch accepted")
	}
}

func TestDefaultVMRuntimeSupervisorDepsAndAttemptHelpers(t *testing.T) {
	deps := defaultVMRuntimeSupervisorDeps(func(context.Context, VMConsoleProxyConfig) (*exec.Cmd, func(), error) {
		return nil, func() {}, errors.New("constructor failed")
	})
	if !validVMRuntimeSupervisorDeps(deps) || deps.selectRun == nil {
		t.Fatal("default supervisor dependencies are incomplete")
	}
	called := false
	if err := deps.lock(context.Background(), VMConsoleProxyConfig{}, func() error { called = true; return nil }); err != nil || !called {
		t.Fatalf("unrooted launch lock = called %t, error %v", called, err)
	}

	artifact, launchDeps := newVMRuntimeLaunchVerificationFixture(t)
	_, err := launchVMRuntimeAttempt(
		context.Background(), VMConsoleProxyConfig{}, artifact,
		strings.Repeat("d", 64), strings.Repeat("e", 64),
		func(context.Context, VMConsoleProxyConfig) (*exec.Cmd, func(), error) {
			return nil, func() {}, errors.New("constructor failed")
		},
		launchDeps, defaultVMRuntimeControlFileDeps(),
	)
	if err == nil || !strings.Contains(err.Error(), "constructor failed") {
		t.Fatalf("launch constructor error = %v", err)
	}

	cleaned := 0
	stopVMRuntimeAttempt(
		VMConsoleProxyConfig{Service: "devbox", RuntimeRunningMarker: filepath.Join(t.TempDir(), "missing.json")},
		&vmRuntimeLaunchAttempt{cleanup: func() { cleaned++ }},
		vmRuntimeControlFileDeps{uid: uint32(os.Geteuid()), gid: uint32(os.Getegid())},
	)
	if cleaned != 1 {
		t.Fatalf("cleanup calls = %d, want 1", cleaned)
	}
}

func TestVMRuntimeHostReadinessCancellationAndExit(t *testing.T) {
	waiting := &vmRuntimeProcessWaiter{done: make(chan struct{})}
	attempt := &vmRuntimeLaunchAttempt{waiter: waiting}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	deadline := time.NewTimer(time.Hour)
	ticker := time.NewTicker(time.Hour)
	if err := waitVMRuntimeInitialHostReady(canceled, VMConsoleProxyConfig{}, attempt, deadline, ticker); !errors.Is(err, context.Canceled) {
		t.Fatalf("initial readiness cancellation = %v", err)
	}
	deadline.Stop()
	ticker.Stop()

	close(waiting.done)
	deadline = time.NewTimer(time.Hour)
	ticker = time.NewTicker(time.Hour)
	if err := waitVMRuntimeInitialHostReady(context.Background(), VMConsoleProxyConfig{}, attempt, deadline, ticker); err == nil || !strings.Contains(err.Error(), "exited before host readiness") {
		t.Fatalf("initial readiness process exit = %v", err)
	}
	deadline.Stop()
	ticker.Stop()

	waiting = &vmRuntimeProcessWaiter{done: make(chan struct{})}
	attempt.waiter = waiting
	ticker = time.NewTicker(time.Hour)
	if err := waitVMRuntimeHostStabilization(canceled, VMConsoleProxyConfig{}, attempt, ticker); !errors.Is(err, context.Canceled) {
		t.Fatalf("stabilization cancellation = %v", err)
	}
	ticker.Stop()

	close(waiting.done)
	ticker = time.NewTicker(time.Hour)
	if err := waitVMRuntimeHostStabilization(context.Background(), VMConsoleProxyConfig{}, attempt, ticker); err == nil || !strings.Contains(err.Error(), "exited during host stabilization") {
		t.Fatalf("stabilization process exit = %v", err)
	}
	ticker.Stop()

	attempt.marker = vmRuntimeRunningMarker{RunnerPID: os.Getpid(), ChildPID: os.Getpid()}
	if err := probeVMRuntimeHostReady(context.Background(), VMConsoleProxyConfig{
		Service: "devbox", RuntimeRunningMarker: filepath.Join(t.TempDir(), "missing.json"),
	}, attempt); err == nil {
		t.Fatal("missing trusted running marker accepted")
	}
}

func vmRuntimeSupervisorTestSelection() vmRuntimeLaunchSelection {
	configured := vmRuntimeLaunchTestArtifact("v1.16.1", "/configured")
	staged := vmRuntimeLaunchTestArtifact("v1.17.0", "/staged")
	return vmRuntimeLaunchSelection{
		Service: "devbox", DescriptorSHA256: strings.Repeat("d", 64),
		Selected: staged, Fallback: &configured, Trial: true,
	}
}

func vmRuntimeSupervisorTestConfig() VMConsoleProxyConfig {
	return VMConsoleProxyConfig{
		Service: "devbox", RuntimeRunningMarker: "/run/devbox/running.json",
		RuntimeTrialResult: "/run/devbox/trial.json",
	}
}

func vmRuntimeSupervisorTestDeps() vmRuntimeSupervisorDeps {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	deps := vmRuntimeSupervisorDeps{
		random:  bytes.NewReader(make([]byte, 32)),
		now:     func() time.Time { return now },
		lock:    func(_ context.Context, _ VMConsoleProxyConfig, fn func() error) error { return fn() },
		confirm: func(VMConsoleProxyConfig, vmRuntimeLaunchSelection) error { return nil },
		preflight: func(context.Context, db.VMRuntimeArtifactConfig) error {
			return nil
		},
		launch: func(_ context.Context, _ VMConsoleProxyConfig, artifact db.VMRuntimeArtifactConfig, generation, launchID string) (*vmRuntimeLaunchAttempt, error) {
			return vmRuntimeSupervisorFakeAttempt(artifact, generation, launchID, 101, 202), nil
		},
		ready: func(context.Context, VMConsoleProxyConfig, *vmRuntimeLaunchAttempt) error { return nil },
		stop:  func(VMConsoleProxyConfig, *vmRuntimeLaunchAttempt) {},
		supervise: func(context.Context, VMConsoleProxyConfig, net.Listener, *vmRuntimeLaunchAttempt) error {
			return nil
		},
		write: func(string, vmRuntimeTrialResult) error { return nil },
	}
	return deps
}

func vmRuntimeSupervisorFakeAttempt(artifact db.VMRuntimeArtifactConfig, generation, launchID string, runnerPID, childPID int) *vmRuntimeLaunchAttempt {
	return &vmRuntimeLaunchAttempt{
		artifact: artifact,
		marker: vmRuntimeRunningMarker{
			SchemaVersion: vmRuntimeRunningMarkerSchemaVersion,
			Service:       "devbox", DescriptorSHA256: generation, LaunchID: launchID,
			RuntimeID: artifact.ID, ManifestSHA256: artifact.ManifestSHA256,
			FirecrackerSHA256: artifact.FirecrackerSHA256, JailerSHA256: artifact.JailerSHA256,
			RunnerPID: runnerPID, ChildPID: childPID, StartedAt: "2026-07-20T12:00:00Z",
		},
	}
}
