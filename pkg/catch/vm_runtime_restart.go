// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
)

type vmRuntimeRestartDeps struct {
	restart      func(string) error
	snapshot     func(context.Context, *db.Service, io.Writer) (string, error)
	consume      func(context.Context, string) (vmRuntimeTrialOutcome, error)
	unprotect    func(context.Context, string) error
	processAlive func(int) bool
	now          func() time.Time
	wait         time.Duration
}

func (s *Server) runtimeRestartDependencies() vmRuntimeRestartDeps {
	deps := vmRuntimeRestartDeps{
		restart: func(service string) error { return (&vmRunner{name: service}).Restart() },
		snapshot: func(ctx context.Context, service *db.Service, w io.Writer) (string, error) {
			return s.createVMRuntimeUpgradeRecoveryPoint(ctx, service, w)
		},
		consume: func(ctx context.Context, service string) (vmRuntimeTrialOutcome, error) {
			return consumeVMRuntimeTrialResult(ctx, &s.cfg, service, s.runtimeTrialConsumerDependencies())
		},
		unprotect: func(ctx context.Context, snapshot string) error {
			return setSnapshotProperty(ctx, s.zfsRunner, snapshot, "com.yeetrun:protected", "false")
		},
		processAlive: vmRuntimeProcessAlive,
		now:          time.Now,
		wait:         200 * time.Millisecond,
	}
	if s.vmRuntimeRestartDeps == nil {
		return deps
	}
	override := *s.vmRuntimeRestartDeps
	if override.restart != nil {
		deps.restart = override.restart
	}
	if override.snapshot != nil {
		deps.snapshot = override.snapshot
	}
	if override.consume != nil {
		deps.consume = override.consume
	}
	if override.unprotect != nil {
		deps.unprotect = override.unprotect
	}
	if override.processAlive != nil {
		deps.processAlive = override.processAlive
	}
	if override.now != nil {
		deps.now = override.now
	}
	if override.wait > 0 {
		deps.wait = override.wait
	}
	return deps
}

func (s *Server) restartStagedVMRuntime(ctx context.Context, w io.Writer, serviceName string) error {
	lock := s.vmRuntimeRestartLock(serviceName)
	lock.Lock()
	defer lock.Unlock()
	deps := s.runtimeRestartDependencies()
	if err := s.reconcileHealthyVMRuntimeRecoveryPoint(ctx, serviceName, deps.unprotect); err != nil {
		return fmt.Errorf("reconcile prior healthy VM runtime recovery point before starting a new trial: %w", err)
	}
	recoveryPoint, shouldRestart, err := s.prepareVMRuntimeRestart(ctx, w, serviceName, deps)
	if err != nil {
		return err
	}
	// Deliberately outside the VM runtime root lock: the unit's ExecStartPre
	// takes that same lock while it reconciles the VM network.
	if shouldRestart {
		if err := deps.restart(serviceName); err != nil {
			return fmt.Errorf("restart VM %s for runtime trial: %w", serviceName, err)
		}
	}
	return s.waitVMRuntimeRestartTrial(ctx, w, serviceName, recoveryPoint, deps)
}

func (s *Server) vmRuntimeRestartLock(serviceName string) *sync.Mutex {
	lock, _ := s.vmRuntimeRestartLocks.LoadOrStore(serviceName, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func (s *Server) prepareVMRuntimeRestart(ctx context.Context, w io.Writer, serviceName string, deps vmRuntimeRestartDeps) (recoveryPoint string, shouldRestart bool, retErr error) {
	err := WithVMRuntimeTransactionLock(ctx, &s.cfg, func() error {
		var err error
		recoveryPoint, shouldRestart, err = s.prepareVMRuntimeRestartLocked(ctx, w, serviceName, deps)
		return err
	})
	if err != nil && recoveryPoint != "" {
		return recoveryPoint, false, fmt.Errorf("prepare VM runtime restart (protected recovery point retained at %s): %w", recoveryPoint, err)
	}
	return recoveryPoint, shouldRestart, err
}

func (s *Server) prepareVMRuntimeRestartLocked(ctx context.Context, w io.Writer, serviceName string, deps vmRuntimeRestartDeps) (string, bool, error) {
	view, err := s.cfg.DB.Get()
	if err != nil {
		return "", false, err
	}
	service, err := adoptedVMRuntimeRestartService(view.AsStruct(), serviceName)
	if err != nil {
		return "", false, err
	}
	runtimeState := service.VM.Components.Runtime
	if runtimeState.Staged == nil {
		return "", false, fmt.Errorf("VM %s has no staged runtime to restart into", serviceName)
	}
	if pendingVMRuntimeTrial(runtimeState) {
		return s.resumePendingVMRuntimeRestart(service, runtimeState, deps)
	}
	return s.beginVMRuntimeRestart(ctx, w, service, runtimeState, deps)
}

func adoptedVMRuntimeRestartService(data *db.Data, serviceName string) (*db.Service, error) {
	service := data.Services[serviceName]
	if service == nil || service.ServiceType != db.ServiceTypeVM || service.VM == nil || service.VM.Components == nil {
		return nil, fmt.Errorf("service %q is not an adopted VM", serviceName)
	}
	return service, nil
}

func (s *Server) resumePendingVMRuntimeRestart(service *db.Service, runtimeState db.VMRuntimeLifecycleConfig, deps vmRuntimeRestartDeps) (string, bool, error) {
	if runtimeState.Trial.CandidateID != runtimeState.Staged.ID || runtimeState.Trial.PreviousID != runtimeState.Configured.ID {
		return "", false, fmt.Errorf("VM %s pending runtime trial does not match current selections", service.Name)
	}
	recoveryPoint := runtimeState.Trial.RecoveryPoint
	root := serviceRootFromConfig(s.cfg, *service)
	retried, err := retryTerminalVMRuntimeTrial(root, service, runtimeState, deps, s.cfg.DB)
	if err != nil || retried {
		return recoveryPoint, retried, err
	}
	started, err := pendingVMRuntimeTrialStarted(root, service, runtimeState, deps)
	return recoveryPoint, !started, err
}

func (s *Server) beginVMRuntimeRestart(ctx context.Context, w io.Writer, service *db.Service, runtimeState db.VMRuntimeLifecycleConfig, deps vmRuntimeRestartDeps) (string, bool, error) {
	recoveryPoint, err := deps.snapshot(ctx, service, w)
	if err != nil {
		return "", false, err
	}
	candidate := *runtimeState.Staged
	configured := runtimeState.Configured
	_, _, err = s.cfg.DB.MutateService(service.Name, func(_ *db.Data, current *db.Service) error {
		return recordPendingVMRuntimeTrial(current, service.Name, candidate, configured, recoveryPoint, deps.now())
	})
	return recoveryPoint, true, err
}

func recordPendingVMRuntimeTrial(current *db.Service, serviceName string, candidate, configured db.VMRuntimeArtifactConfig, recoveryPoint string, now time.Time) error {
	if current.VM == nil || current.VM.Components == nil || current.VM.Components.Runtime.Staged == nil ||
		*current.VM.Components.Runtime.Staged != candidate || current.VM.Components.Runtime.Configured != configured {
		return fmt.Errorf("VM %s runtime state changed while preparing restart", serviceName)
	}
	if current.VM.Components.Runtime.Trial != nil && current.VM.Components.Runtime.Trial.State == "pending" {
		return fmt.Errorf("VM %s already has a pending runtime trial", serviceName)
	}
	current.VM.Components.Runtime.Trial = &db.VMRuntimeTrialConfig{
		State: "pending", CandidateID: candidate.ID, PreviousID: configured.ID,
		RecoveryPoint: recoveryPoint, StartedAt: now.UTC().Format(time.RFC3339Nano),
	}
	return nil
}

func retryTerminalVMRuntimeTrial(root string, service *db.Service, runtimeState db.VMRuntimeLifecycleConfig, deps vmRuntimeRestartDeps, store *db.Store) (bool, error) {
	control := defaultVMRuntimeControlFileDeps()
	control.uid = uint32(os.Geteuid())
	control.gid = uint32(os.Getegid())
	resultPath := filepath.Join(serviceRunDirForRoot(root), vmRuntimeTrialResultFileName)
	result, resultID, err := readTrustedVMRuntimeTrialResult(resultPath, service.Name, control)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if result.Outcome != vmRuntimeTrialFailedNoFallback {
		return false, nil
	}
	if !vmRuntimeTrialResultMatchesSelections(result, runtimeState) {
		return false, fmt.Errorf("terminal VM runtime retry result does not match current selections")
	}
	_, _, err = store.MutateService(service.Name, func(_ *db.Data, current *db.Service) error {
		return resetPendingVMRuntimeTrialForRetry(current, result, deps.now())
	})
	if err != nil {
		return false, err
	}
	if err := removeVMRuntimeTrialResult(resultPath, resultID, control); err != nil {
		return false, err
	}
	return true, nil
}

func vmRuntimeTrialResultMatchesSelections(result vmRuntimeTrialResult, runtimeState db.VMRuntimeLifecycleConfig) bool {
	return runtimeState.Staged != nil && result.Candidate.matches(*runtimeState.Staged) && result.Configured.matches(runtimeState.Configured)
}

func resetPendingVMRuntimeTrialForRetry(current *db.Service, result vmRuntimeTrialResult, now time.Time) error {
	trial := current.VM.Components.Runtime.Trial
	if !pendingVMRuntimeTrial(current.VM.Components.Runtime) || trial.CandidateID != result.Candidate.ID || trial.PreviousID != result.Configured.ID {
		return fmt.Errorf("pending VM runtime trial changed before terminal retry")
	}
	trial.StartedAt = now.UTC().Format(time.RFC3339Nano)
	trial.LastError = ""
	return nil
}

func pendingVMRuntimeTrialStarted(root string, service *db.Service, runtimeState db.VMRuntimeLifecycleConfig, deps vmRuntimeRestartDeps) (bool, error) {
	control := defaultVMRuntimeControlFileDeps()
	control.uid = uint32(os.Geteuid())
	control.gid = uint32(os.Getegid())
	result, _, err := readTrustedVMRuntimeTrialResult(
		filepath.Join(serviceRunDirForRoot(root), vmRuntimeTrialResultFileName), service.Name, control,
	)
	if err == nil {
		if !vmRuntimeTrialResultMatchesSelections(result, runtimeState) {
			return false, fmt.Errorf("pending VM runtime trial result does not match current selections")
		}
		return true, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return pendingVMRuntimeLaunchMarkerStarted(root, service, runtimeState, deps, control)
}

func pendingVMRuntimeLaunchMarkerStarted(root string, service *db.Service, runtimeState db.VMRuntimeLifecycleConfig, deps vmRuntimeRestartDeps, control vmRuntimeControlFileDeps) (bool, error) {
	descriptor, err := readVMRuntimeDescriptorSnapshotWithOwner(filepath.Join(serviceDataDirForRoot(root), vmRuntimeDescriptorFileName), service.Name, control.uid, control.gid)
	if err != nil {
		return false, err
	}
	marker, err := readTrustedVMRuntimeRunningMarker(filepath.Join(serviceRunDirForRoot(root), vmRuntimeRunningMarkerFileName), service.Name, control.uid, control.gid)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	currentLaunch := marker.DescriptorSHA256 == descriptor.SHA256 &&
		(vmRuntimeMarkerMatchesArtifact(marker, *runtimeState.Staged) || vmRuntimeMarkerMatchesArtifact(marker, runtimeState.Configured))
	return currentLaunch && deps.processAlive(marker.RunnerPID) && deps.processAlive(marker.ChildPID), nil
}

func (s *Server) waitVMRuntimeRestartTrial(ctx context.Context, w io.Writer, serviceName, recoveryPoint string, deps vmRuntimeRestartDeps) error {
	ticker := time.NewTicker(deps.wait)
	defer ticker.Stop()
	for {
		state, err := s.vmRuntimeTrialState(serviceName)
		if err != nil {
			return err
		}
		done, err := s.handleVMRuntimeRestartTrialState(ctx, w, serviceName, recoveryPoint, state, deps)
		if done || err != nil {
			return err
		}

		outcome, consumeErr := deps.consume(ctx, serviceName)
		if err := s.handleVMRuntimeRestartTrialOutcome(w, serviceName, recoveryPoint, outcome, consumeErr); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Server) handleVMRuntimeRestartTrialState(ctx context.Context, w io.Writer, serviceName, recoveryPoint string, state db.VMRuntimeTrialConfig, deps vmRuntimeRestartDeps) (bool, error) {
	switch state.State {
	case string(vmRuntimeTrialHealthy):
		if recoveryPoint != "" {
			if err := s.reconcileHealthyVMRuntimeRecoveryPoint(ctx, serviceName, deps.unprotect); err != nil {
				return true, fmt.Errorf("VM runtime trial succeeded, but recovery point %s remains protected: %w", recoveryPoint, err)
			}
		}
		writef(w, "%s\t%s\thealthy\n", serviceName, state.CandidateID)
		return true, nil
	case string(vmRuntimeTrialFailedRolledBack):
		if recoveryPoint != "" {
			writeSnapshotWarning(w, "protected runtime-upgrade recovery point: %s\n", recoveryPoint)
		}
		return true, fmt.Errorf("VM runtime candidate %s failed host readiness; configured runtime %s is running", state.CandidateID, state.PreviousID)
	default:
		return false, nil
	}
}

func (s *Server) handleVMRuntimeRestartTrialOutcome(w io.Writer, serviceName, recoveryPoint string, outcome vmRuntimeTrialOutcome, consumeErr error) error {
	if consumeErr != nil && !errors.Is(consumeErr, os.ErrNotExist) {
		return consumeErr
	}
	if outcome != vmRuntimeTrialFailedNoFallback {
		return nil
	}
	if recoveryPoint != "" {
		writeSnapshotWarning(w, "protected runtime-upgrade recovery point: %s\n", recoveryPoint)
	}
	return s.vmRuntimeTerminalTrialError(serviceName)
}

func (s *Server) vmRuntimeTerminalTrialError(serviceName string) error {
	view, err := s.getDB()
	if err != nil {
		return errors.Join(ErrVMRuntimeNoFallback, err)
	}
	service, ok := view.Services().GetOk(serviceName)
	if !ok {
		return errors.Join(ErrVMRuntimeNoFallback, fmt.Errorf("service %q not found", serviceName))
	}
	root := serviceRootFromConfig(s.cfg, *service.AsStruct())
	result, _, err := readTrustedVMRuntimeTrialResult(
		filepath.Join(serviceRunDirForRoot(root), vmRuntimeTrialResultFileName), serviceName, s.runtimeTrialConsumerDependencies().control,
	)
	if err != nil {
		return errors.Join(ErrVMRuntimeNoFallback, err)
	}
	if result.Outcome != vmRuntimeTrialFailedNoFallback {
		return errors.Join(ErrVMRuntimeNoFallback, fmt.Errorf("unexpected terminal VM runtime outcome %q", result.Outcome))
	}
	return fmt.Errorf("%w: %s", ErrVMRuntimeNoFallback, result.Error)
}

func (s *Server) vmRuntimeTrialState(serviceName string) (db.VMRuntimeTrialConfig, error) {
	view, err := s.getDB()
	if err != nil {
		return db.VMRuntimeTrialConfig{}, err
	}
	service, ok := view.Services().GetOk(serviceName)
	if !ok || !service.VM().Valid() || !service.VM().Components().Valid() {
		return db.VMRuntimeTrialConfig{}, fmt.Errorf("service %q is not an adopted VM", serviceName)
	}
	trial := service.VM().Components().Runtime().Trial()
	if !trial.Valid() {
		return db.VMRuntimeTrialConfig{}, nil
	}
	return *trial.AsStruct(), nil
}

func (s *Server) rollbackVMRuntime(ctx context.Context, w io.Writer, serviceName string, restart bool) error {
	view, err := s.getDB()
	if err != nil {
		return err
	}
	service, ok := view.Services().GetOk(serviceName)
	if !ok || !service.VM().Valid() || !service.VM().Components().Valid() {
		return fmt.Errorf("service %q is not an adopted VM", serviceName)
	}
	previous := service.VM().Components().Runtime().Previous()
	if !previous.Valid() {
		return fmt.Errorf("VM %s has no previous runtime to roll back to", serviceName)
	}
	target := *previous.AsStruct()
	result, err := s.runtimeCommandDependencies().stageRuntime(ctx, &s.cfg, serviceName, vmRuntimeResolvedTarget{
		Artifact: target, Selection: vmRuntimeTargetSelectionPrevious,
	})
	if err != nil {
		return err
	}
	state := "staged-for-next-start"
	if result.RunningUnchanged {
		state = "staged-running-unchanged"
	}
	if _, err := fmt.Fprintf(w, "%s\t%s\t%s\n", result.Service, target.ID, state); err != nil {
		return err
	}
	if restart {
		return s.restartStagedVMRuntime(ctx, w, serviceName)
	}
	return nil
}

func pendingVMRuntimeTrial(runtime db.VMRuntimeLifecycleConfig) bool {
	return runtime.Trial != nil && strings.TrimSpace(runtime.Trial.State) == "pending"
}
