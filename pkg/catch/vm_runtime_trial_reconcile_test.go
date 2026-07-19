// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestWatchVMRuntimeTrialResultsPromotesHealthyCandidateAtomically(t *testing.T) {
	fixture, deps, _ := newVMRuntimeAdoptionTransactionFixture(t, false)
	adoptVMRuntimeTransitionFixture(t, fixture, deps)
	target, resolved := newVMRuntimeTransitionTarget(t, fixture)
	if _, err := stageVMRuntimeWithDeps(context.Background(), &fixture.cfg, "devbox", resolved, deps); err != nil {
		t.Fatal(err)
	}
	configured := readLatestVMRuntimeAdoptionData(t, fixture.store).Services["devbox"].VM.Components.Runtime.Configured
	consumerDeps, resultPath := writeVMRuntimeTrialReconcileFixture(t, fixture, deps, vmRuntimeTrialHealthy, target, configured, "")

	outcome, err := consumeVMRuntimeTrialResult(context.Background(), &fixture.cfg, "devbox", consumerDeps)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != vmRuntimeTrialHealthy {
		t.Fatalf("outcome = %q", outcome)
	}
	latest := readLatestVMRuntimeAdoptionData(t, fixture.store).Services["devbox"].VM.Components.Runtime
	if latest.Configured != target || latest.Staged != nil || latest.Previous == nil || *latest.Previous != configured || latest.Trial == nil || latest.Trial.State != string(vmRuntimeTrialHealthy) {
		t.Fatalf("promoted runtime state = %#v", latest)
	}
	descriptor, err := readVMRuntimeDescriptorWithOwner(filepath.Join(serviceDataDirForRoot(fixture.serviceRoot), vmRuntimeDescriptorFileName), "devbox", uint32(os.Geteuid()), uint32(os.Getegid()))
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Configured != target || descriptor.Staged != nil || descriptor.Trial {
		t.Fatalf("promoted descriptor = %#v", descriptor)
	}
	if _, err := os.Lstat(resultPath); !os.IsNotExist(err) {
		t.Fatalf("consumed trial result remains: %v", err)
	}
}

func TestWatchVMRuntimeTrialResultsClearsFailedCandidateWithoutRotatingPrevious(t *testing.T) {
	fixture, deps, _ := newVMRuntimeAdoptionTransactionFixture(t, false)
	adoptVMRuntimeTransitionFixture(t, fixture, deps)
	target, resolved := newVMRuntimeTransitionTarget(t, fixture)
	if _, err := stageVMRuntimeWithDeps(context.Background(), &fixture.cfg, "devbox", resolved, deps); err != nil {
		t.Fatal(err)
	}
	before := readLatestVMRuntimeAdoptionData(t, fixture.store).Services["devbox"].VM.Components.Runtime
	consumerDeps, _ := writeVMRuntimeTrialReconcileFixture(t, fixture, deps, vmRuntimeTrialFailedRolledBack, target, before.Configured, "candidate failed host readiness")

	if _, err := consumeVMRuntimeTrialResult(context.Background(), &fixture.cfg, "devbox", consumerDeps); err != nil {
		t.Fatal(err)
	}
	latest := readLatestVMRuntimeAdoptionData(t, fixture.store).Services["devbox"].VM.Components.Runtime
	if latest.Configured != before.Configured || latest.Staged != nil || !equalVMRuntimeArtifactPointers(latest.Previous, before.Previous) || latest.Trial == nil || latest.Trial.LastError == "" {
		t.Fatalf("rolled-back runtime state = %#v, before %#v", latest, before)
	}
}

func TestWatchVMRuntimeTrialResultsRejectsStaleLaunchLease(t *testing.T) {
	fixture, deps, _ := newVMRuntimeAdoptionTransactionFixture(t, false)
	adoptVMRuntimeTransitionFixture(t, fixture, deps)
	target, resolved := newVMRuntimeTransitionTarget(t, fixture)
	if _, err := stageVMRuntimeWithDeps(context.Background(), &fixture.cfg, "devbox", resolved, deps); err != nil {
		t.Fatal(err)
	}
	before := readLatestVMRuntimeAdoptionData(t, fixture.store).Services["devbox"].VM.Components.Runtime
	consumerDeps, _ := writeVMRuntimeTrialReconcileFixture(t, fixture, deps, vmRuntimeTrialHealthy, target, before.Configured, "")
	resultPath := filepath.Join(serviceRunDirForRoot(fixture.serviceRoot), vmRuntimeTrialResultFileName)
	result, _, err := readTrustedVMRuntimeTrialResult(resultPath, "devbox", consumerDeps.control)
	if err != nil {
		t.Fatal(err)
	}
	result.LaunchID = strings.Repeat("f", 64)
	if _, err := writeVMRuntimeTrialResult(resultPath, result, consumerDeps.control); err != nil {
		t.Fatal(err)
	}
	if _, err := consumeVMRuntimeTrialResult(context.Background(), &fixture.cfg, "devbox", consumerDeps); err == nil || !strings.Contains(err.Error(), "lease") {
		t.Fatalf("stale lease error = %v", err)
	}
	latest := readLatestVMRuntimeAdoptionData(t, fixture.store).Services["devbox"].VM.Components.Runtime
	if latest.Staged == nil || *latest.Staged != target || latest.Configured != before.Configured {
		t.Fatalf("stale result changed runtime state: %#v", latest)
	}
}

func writeVMRuntimeTrialReconcileFixture(
	t *testing.T,
	fixture *vmRuntimeAdoptionFixture,
	coordinator vmRuntimeAdoptionCoordinatorDeps,
	outcome vmRuntimeTrialOutcome,
	candidate, configured db.VMRuntimeArtifactConfig,
	errorText string,
) (vmRuntimeTrialConsumerDeps, string) {
	t.Helper()
	control := defaultVMRuntimeControlFileDeps()
	control.uid = uint32(os.Geteuid())
	control.gid = uint32(os.Getegid())
	control.now = func() time.Time { return time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC) }
	descriptorPath := filepath.Join(serviceDataDirForRoot(fixture.serviceRoot), vmRuntimeDescriptorFileName)
	snapshot, err := readVMRuntimeDescriptorSnapshotWithOwner(descriptorPath, "devbox", control.uid, control.gid)
	if err != nil {
		t.Fatal(err)
	}
	launchID := strings.Repeat("e", 64)
	running := candidate
	if outcome == vmRuntimeTrialFailedRolledBack {
		running = configured
	}
	runDir := serviceRunDirForRoot(fixture.serviceRoot)
	markerPath := filepath.Join(runDir, vmRuntimeRunningMarkerFileName)
	if _, _, err := writeVMRuntimeRunningMarker(markerPath, "devbox", running, snapshot.SHA256, launchID, 101, 202, control); err != nil {
		t.Fatal(err)
	}
	runningIdentity := vmRuntimeTrialIdentityForArtifact(running)
	result := vmRuntimeTrialResult{
		SchemaVersion: vmRuntimeTrialResultSchemaVersion,
		Service:       "devbox", DescriptorSHA256: snapshot.SHA256, LaunchID: launchID,
		Candidate: vmRuntimeTrialIdentityForArtifact(candidate), Configured: vmRuntimeTrialIdentityForArtifact(configured),
		Running: &runningIdentity, Outcome: outcome, RunnerPID: 101, ChildPID: 202,
		StartedAt: "2026-07-20T12:00:00Z", CompletedAt: "2026-07-20T12:00:05Z", Error: errorText,
	}
	resultPath := filepath.Join(runDir, vmRuntimeTrialResultFileName)
	if _, err := writeVMRuntimeTrialResult(resultPath, result, control); err != nil {
		t.Fatal(err)
	}
	consumer := vmRuntimeTrialConsumerDeps{
		control: control, coordinator: coordinator,
		unitState: func(context.Context, string) (vmRuntimeUnitState, error) {
			return vmRuntimeUnitState{ActiveState: "active", MainPID: 101}, nil
		},
		processAlive: func(pid int) bool { return pid == 101 || pid == 202 },
		jailerState:  func(string, uint32, uint32) (vmJailerReadiness, error) { return vmJailerReady, nil },
	}
	return consumer, resultPath
}
