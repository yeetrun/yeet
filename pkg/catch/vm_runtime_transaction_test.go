// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestStageVMRuntimePreservesRunningAndStoppedVMState(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		name := "running"
		if stopped {
			name = "stopped"
		}
		t.Run(name, func(t *testing.T) {
			fixture, deps, systemctlCalls := newVMRuntimeAdoptionTransactionFixture(t, stopped)
			adoptVMRuntimeTransitionFixture(t, fixture, deps)
			*systemctlCalls = nil
			diskBefore := readVMRuntimeAdoptionTestFile(t, fixture.disk)
			before := readLatestVMRuntimeAdoptionData(t, fixture.store).Services["devbox"].VM.Components.Clone()
			target, resolved := newVMRuntimeTransitionTarget(t, fixture)

			result, err := stageVMRuntimeWithDeps(context.Background(), &fixture.cfg, "devbox", resolved, deps)
			if err != nil {
				t.Fatal(err)
			}
			if result.Service != "devbox" || result.RunningUnchanged != !stopped || result.Configured != before.Runtime.Configured || result.Staged == nil || *result.Staged != target {
				t.Fatalf("transition result = %#v", result)
			}
			latest := readLatestVMRuntimeAdoptionData(t, fixture.store).Services["devbox"].VM.Components
			if latest.Runtime.Configured != before.Runtime.Configured || latest.Runtime.Staged == nil || *latest.Runtime.Staged != target || !equalVMRuntimeArtifactPointers(latest.Runtime.Previous, before.Runtime.Previous) {
				t.Fatalf("runtime state = %#v, before %#v", latest.Runtime, before.Runtime)
			}
			if latest.Kernel != before.Kernel || latest.GuestBase != before.GuestBase {
				t.Fatalf("unrelated VM components changed: %#v", latest)
			}
			descriptor, err := readVMRuntimeDescriptorWithOwner(
				filepath.Join(serviceDataDirForRoot(fixture.serviceRoot), vmRuntimeDescriptorFileName),
				"devbox", uint32(os.Geteuid()), uint32(os.Getegid()),
			)
			if err != nil {
				t.Fatal(err)
			}
			if !descriptor.Trial || descriptor.Staged == nil || *descriptor.Staged != target || descriptor.Configured != before.Runtime.Configured {
				t.Fatalf("trial descriptor = %#v", descriptor)
			}
			if !bytes.Equal(readVMRuntimeAdoptionTestFile(t, fixture.disk), diskBefore) {
				t.Fatal("active VM disk changed while staging runtime")
			}
			for _, call := range *systemctlCalls {
				if !slices.Equal(call, []string{"daemon-reload"}) {
					t.Fatalf("systemctl call = %v; staging must not start, stop, or restart a VM", call)
				}
			}
		})
	}
}

func TestStageVMRuntimeCommitsPolicyMutationWithCandidateAtomically(t *testing.T) {
	fixture, deps, _ := newVMRuntimeAdoptionTransactionFixture(t, false)
	adoptVMRuntimeTransitionFixture(t, fixture, deps)
	_, resolved := newVMRuntimeTransitionTarget(t, fixture)
	resolved.PolicyMutation = &vmRuntimePolicyMutation{
		ExpectedPolicy: "manual", ExpectedChannel: "stable",
		Policy: "stage-on-restart", Channel: "candidate",
	}
	if _, err := stageVMRuntimeWithDeps(context.Background(), &fixture.cfg, "devbox", resolved, deps); err != nil {
		t.Fatal(err)
	}
	runtimeState := readLatestVMRuntimeAdoptionData(t, fixture.store).Services["devbox"].VM.Components.Runtime
	if runtimeState.Policy != "stage-on-restart" || runtimeState.Channel != "candidate" || runtimeState.Staged == nil || *runtimeState.Staged != resolved.Artifact {
		t.Fatalf("policy transition = %#v", runtimeState)
	}
}

func TestStageVMRuntimePolicyCohortRollsBackHostAndEveryVMOnDerivedFailure(t *testing.T) {
	fixture, deps, systemctlCalls := newVMRuntimeAdoptionTransactionFixture(t, false)
	adoptVMRuntimeTransitionFixture(t, fixture, deps)
	deps = addVMRuntimeTransitionFixtureService(t, fixture, deps, "worker")
	_, resolved := newVMRuntimeTransitionTarget(t, fixture)
	targets := []vmRuntimeNamedTarget{{Service: "devbox", Target: resolved}, {Service: "worker", Target: resolved}}
	newHost := &db.VMHostConfig{RuntimePolicy: "stage-on-restart", RuntimeChannel: "stable"}

	before := readLatestVMRuntimeAdoptionData(t, fixture.store)
	oldFiles := make(map[string][]byte)
	for _, service := range []string{"devbox", "worker"} {
		root := before.Services[service].ServiceRoot
		for _, path := range []string{
			filepath.Join(serviceDataDirForRoot(root), vmRuntimeDescriptorFileName),
			filepath.Join(vmSystemdSystemDir, vmSystemdUnitName(service)),
		} {
			oldFiles[path] = readVMRuntimeAdoptionTestFile(t, path)
		}
	}
	deps.afterTransition = func(state string) error {
		if state == "unit-published" {
			return errors.New("stop after fleet derived publication")
		}
		return nil
	}
	*systemctlCalls = nil
	err := stageVMRuntimePolicyCohort(context.Background(), &fixture.cfg, nil, newHost, snapshotVMRuntimePolicyFleet(before), targets, deps)
	if err == nil || !strings.Contains(err.Error(), "stop after fleet derived publication") {
		t.Fatalf("cohort error = %v", err)
	}
	after := readLatestVMRuntimeAdoptionData(t, fixture.store)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("failed cohort changed database\nbefore=%#v\nafter=%#v", before, after)
	}
	for path, want := range oldFiles {
		if got := readVMRuntimeAdoptionTestFile(t, path); !bytes.Equal(got, want) {
			t.Fatalf("failed cohort changed %s", path)
		}
	}
	for _, call := range *systemctlCalls {
		if !slices.Equal(call, []string{"daemon-reload"}) {
			t.Fatalf("policy cohort service activity = %v", call)
		}
	}
}

func TestStageVMRuntimePolicyCohortCommitsHostAndFleetTogether(t *testing.T) {
	fixture, deps, _ := newVMRuntimeAdoptionTransactionFixture(t, true)
	adoptVMRuntimeTransitionFixture(t, fixture, deps)
	deps = addVMRuntimeTransitionFixtureService(t, fixture, deps, "worker")
	target, resolved := newVMRuntimeTransitionTarget(t, fixture)
	newHost := &db.VMHostConfig{RuntimePolicy: "stage-on-restart", RuntimeChannel: "candidate"}
	if _, err := fixture.store.MutateData(func(data *db.Data) error {
		data.VMHost = newHost.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fleet := snapshotVMRuntimePolicyFleet(readLatestVMRuntimeAdoptionData(t, fixture.store))
	if err := stageVMRuntimePolicyCohort(context.Background(), &fixture.cfg, newHost, newHost, fleet, []vmRuntimeNamedTarget{
		{Service: "worker", Target: resolved}, {Service: "devbox", Target: resolved},
	}, deps); err != nil {
		t.Fatal(err)
	}
	latest := readLatestVMRuntimeAdoptionData(t, fixture.store)
	if !reflect.DeepEqual(latest.VMHost, newHost) {
		t.Fatalf("host policy = %#v", latest.VMHost)
	}
	for _, service := range []string{"devbox", "worker"} {
		staged := latest.Services[service].VM.Components.Runtime.Staged
		if staged == nil || *staged != target {
			t.Fatalf("%s staged runtime = %#v", service, staged)
		}
	}
}

func TestStageVMRuntimePolicyCohortRecoversFullyNewStateAfterDatabasePublication(t *testing.T) {
	fixture, deps, _ := newVMRuntimeAdoptionTransactionFixture(t, true)
	adoptVMRuntimeTransitionFixture(t, fixture, deps)
	deps = addVMRuntimeTransitionFixtureService(t, fixture, deps, "worker")
	target, resolved := newVMRuntimeTransitionTarget(t, fixture)
	newHost := &db.VMHostConfig{RuntimePolicy: "stage-on-restart", RuntimeChannel: "stable"}
	fleet := snapshotVMRuntimePolicyFleet(readLatestVMRuntimeAdoptionData(t, fixture.store))
	deps.afterTransition = func(state string) error {
		if state == "database-published" {
			return errors.New("interrupt after atomic database publication")
		}
		return nil
	}
	err := stageVMRuntimePolicyCohort(context.Background(), &fixture.cfg, nil, newHost, fleet, []vmRuntimeNamedTarget{
		{Service: "devbox", Target: resolved}, {Service: "worker", Target: resolved},
	}, deps)
	if err == nil || !strings.Contains(err.Error(), "interrupt after atomic database publication") {
		t.Fatalf("cohort error = %v", err)
	}
	latest := readLatestVMRuntimeAdoptionData(t, fixture.store)
	if !reflect.DeepEqual(latest.VMHost, newHost) {
		t.Fatalf("published host policy = %#v", latest.VMHost)
	}
	for _, service := range []string{"devbox", "worker"} {
		staged := latest.Services[service].VM.Components.Runtime.Staged
		if staged == nil || *staged != target {
			t.Fatalf("published %s staged runtime = %#v", service, staged)
		}
	}
	if _, err := fixture.store.MutateData(func(data *db.Data) error {
		data.VMHost.MemoryPolicy = "on"
		data.VMHost.ProtectedRuntimeIDs = []string{"local-operator-runtime"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	deps.afterTransition = nil
	recovery, err := openVMRuntimeJournalStore(context.Background(), fixture.dataRoot, deps.journal)
	if err != nil {
		t.Fatal(err)
	}
	defer recovery.Close()
	if err := recoverVMRuntimeAdoptionsWithStore(context.Background(), &fixture.cfg, recovery, deps); err != nil {
		t.Fatal(err)
	}
	if groups, err := recovery.LoadAll(); err != nil || len(groups) != 0 {
		t.Fatalf("journals after cohort recovery = %#v, %v", groups, err)
	}
	latest = readLatestVMRuntimeAdoptionData(t, fixture.store)
	if latest.VMHost.MemoryPolicy != "on" || !slices.Equal(latest.VMHost.ProtectedRuntimeIDs, []string{"local-operator-runtime"}) {
		t.Fatalf("recovery changed unrelated VM host settings: %#v", latest.VMHost)
	}
}

func TestStageVMRuntimePolicyCohortRejectsStaleSkippedFleetMember(t *testing.T) {
	fixture, deps, _ := newVMRuntimeAdoptionTransactionFixture(t, true)
	adoptVMRuntimeTransitionFixture(t, fixture, deps)
	deps = addVMRuntimeTransitionFixtureService(t, fixture, deps, "worker")
	fleet := snapshotVMRuntimePolicyFleet(readLatestVMRuntimeAdoptionData(t, fixture.store))
	if _, _, err := fixture.store.MutateService("worker", func(_ *db.Data, service *db.Service) error {
		service.VM.Components.Runtime.Channel = "candidate"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, resolved := newVMRuntimeTransitionTarget(t, fixture)
	newHost := &db.VMHostConfig{RuntimePolicy: "stage-on-restart", RuntimeChannel: "stable"}
	err := stageVMRuntimePolicyCohort(context.Background(), &fixture.cfg, nil, newHost, fleet, []vmRuntimeNamedTarget{{Service: "devbox", Target: resolved}}, deps)
	if err == nil || !strings.Contains(err.Error(), "fleet changed") {
		t.Fatalf("stale fleet cohort error = %v", err)
	}
	latest := readLatestVMRuntimeAdoptionData(t, fixture.store)
	if latest.VMHost != nil || latest.Services["devbox"].VM.Components.Runtime.Staged != nil {
		t.Fatalf("stale fleet cohort committed state: host=%#v runtime=%#v", latest.VMHost, latest.Services["devbox"].VM.Components.Runtime)
	}
}

func TestStageVMRuntimeRechecksArtifactImmediatelyBeforeDescriptorPublication(t *testing.T) {
	fixture, deps, _ := newVMRuntimeAdoptionTransactionFixture(t, false)
	adoptVMRuntimeTransitionFixture(t, fixture, deps)
	target, resolved := newVMRuntimeTransitionTarget(t, fixture)
	oldDescriptor := readVMRuntimeAdoptionTestFile(t, filepath.Join(serviceDataDirForRoot(fixture.serviceRoot), vmRuntimeDescriptorFileName))
	deps.afterTransition = func(state string) error {
		if state == "prepared" {
			if err := os.WriteFile(target.Firecracker, []byte("replaced-after-journal"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}

	_, err := stageVMRuntimeWithDeps(context.Background(), &fixture.cfg, "devbox", resolved, deps)
	if err == nil || !strings.Contains(err.Error(), "evidence changed") {
		t.Fatalf("stage error = %v, want evidence drift", err)
	}
	latest := readLatestVMRuntimeAdoptionData(t, fixture.store).Services["devbox"].VM.Components.Runtime
	if latest.Staged != nil {
		t.Fatalf("staged runtime committed after evidence drift: %#v", latest.Staged)
	}
	if got := readVMRuntimeAdoptionTestFile(t, filepath.Join(serviceDataDirForRoot(fixture.serviceRoot), vmRuntimeDescriptorFileName)); !bytes.Equal(got, oldDescriptor) {
		t.Fatal("descriptor changed despite pre-publication evidence failure")
	}
}

func TestStageVMRuntimeAllowsActiveDiskTimestampChanges(t *testing.T) {
	fixture, deps, _ := newVMRuntimeAdoptionTransactionFixture(t, false)
	adoptVMRuntimeTransitionFixture(t, fixture, deps)
	_, resolved := newVMRuntimeTransitionTarget(t, fixture)
	deps.afterTransition = func(state string) error {
		if state == "prepared" {
			future := time.Now().Add(2 * time.Second)
			if err := os.Chtimes(fixture.disk, future, future); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}
	if _, err := stageVMRuntimeWithDeps(context.Background(), &fixture.cfg, "devbox", resolved, deps); err != nil {
		t.Fatalf("stage after active disk timestamp change: %v", err)
	}
}

func TestStageVMRuntimeExplicitChannelDoesNotRequireMatchingPolicy(t *testing.T) {
	fixture := newVMRuntimeAdoptionFixture(t, false)
	_, resolved := newVMRuntimeTransitionTarget(t, fixture)
	resolved.Selection = vmRuntimeTargetSelectionChannel
	resolved.Channel = "candidate"
	resolved.ChannelFromPolicy = false
	configured := resolved.Artifact
	configured.ID = "firecracker-v1.16.1-yeet-v1"
	data := &db.Data{
		VMHost: &db.VMHostConfig{RuntimeChannel: "stable"},
		Services: map[string]*db.Service{"devbox": {
			Name: "devbox", ServiceType: db.ServiceTypeVM,
			VM: &db.VMConfig{Components: &db.VMComponentsConfig{Runtime: db.VMRuntimeLifecycleConfig{Configured: configured}}},
		}},
	}
	if _, err := validateVMRuntimeTransitionTarget(context.Background(), &fixture.cfg, data, "devbox", resolved); err != nil {
		t.Fatalf("explicit candidate channel under stable policy: %v", err)
	}
}

func TestStageVMRuntimePolicyTargetRejectsStaleLifecycle(t *testing.T) {
	fixture := newVMRuntimeAdoptionFixture(t, false)
	_, resolved := newVMRuntimeTransitionTarget(t, fixture)
	configured := resolved.Artifact
	configured.ID = "firecracker-v1.15.0-yeet-v1"
	runtimeState := db.VMRuntimeLifecycleConfig{Configured: configured}
	resolved.ExpectedRuntime = runtimeState.Clone()
	for _, mutate := range []func(*db.VMRuntimeLifecycleConfig){
		func(runtime *db.VMRuntimeLifecycleConfig) { runtime.Configured.ID = "firecracker-v1.17.0-yeet-v1" },
		func(runtime *db.VMRuntimeLifecycleConfig) {
			runtime.Staged = cloneVMRuntimeArtifact(&resolved.Artifact)
		},
	} {
		changed := *runtimeState.Clone()
		mutate(&changed)
		data := &db.Data{Services: map[string]*db.Service{"devbox": {
			Name: "devbox", ServiceType: db.ServiceTypeVM,
			VM: &db.VMConfig{Components: &db.VMComponentsConfig{Runtime: changed}},
		}}}
		if _, err := validateVMRuntimeTransitionTarget(context.Background(), &fixture.cfg, data, "devbox", resolved); err == nil || !strings.Contains(err.Error(), "lifecycle changed") {
			t.Fatalf("stale policy target error = %v", err)
		}
	}
	resolved.Selection = vmRuntimeTargetSelectionChannel
	resolved.Channel = "stable"
	resolved.ChannelFromPolicy = true
	unchanged := *runtimeState.Clone()
	data := &db.Data{
		VMHost: &db.VMHostConfig{RuntimePolicy: "manual", RuntimeChannel: "stable"},
		Services: map[string]*db.Service{"devbox": {
			Name: "devbox", ServiceType: db.ServiceTypeVM,
			VM: &db.VMConfig{Components: &db.VMComponentsConfig{Runtime: unchanged}},
		}},
	}
	if _, err := validateVMRuntimeTransitionTarget(context.Background(), &fixture.cfg, data, "devbox", resolved); err == nil || !strings.Contains(err.Error(), "effective runtime policy changed") {
		t.Fatalf("stale host policy target error = %v", err)
	}
}

func TestStageVMRuntimeIsIdempotentForExactCandidate(t *testing.T) {
	fixture, deps, systemctlCalls := newVMRuntimeAdoptionTransactionFixture(t, true)
	adoptVMRuntimeTransitionFixture(t, fixture, deps)
	_, resolved := newVMRuntimeTransitionTarget(t, fixture)
	if _, err := stageVMRuntimeWithDeps(context.Background(), &fixture.cfg, "devbox", resolved, deps); err != nil {
		t.Fatal(err)
	}
	callCount := len(*systemctlCalls)
	first := readLatestVMRuntimeAdoptionData(t, fixture.store).Services["devbox"].VM.Components.Runtime
	result, err := stageVMRuntimeWithDeps(context.Background(), &fixture.cfg, "devbox", resolved, deps)
	if err != nil {
		t.Fatal(err)
	}
	if result.Staged == nil || *result.Staged != resolved.Artifact || result.RunningUnchanged {
		t.Fatalf("idempotent result = %#v", result)
	}
	second := readLatestVMRuntimeAdoptionData(t, fixture.store).Services["devbox"].VM.Components.Runtime
	if !reflectVMRuntimeLifecycle(first, second) || len(*systemctlCalls) != callCount {
		t.Fatalf("idempotent stage mutated state or systemd: first=%#v second=%#v calls=%v", first, second, *systemctlCalls)
	}
}

func TestStageVMRuntimeRefusesReplacingLiveNaturalRestartTrial(t *testing.T) {
	fixture, deps, _ := newVMRuntimeAdoptionTransactionFixture(t, false)
	adoptVMRuntimeTransitionFixture(t, fixture, deps)
	target, resolved := newVMRuntimeTransitionTarget(t, fixture)
	if _, err := stageVMRuntimeWithDeps(context.Background(), &fixture.cfg, "devbox", resolved, deps); err != nil {
		t.Fatal(err)
	}
	descriptorPath := filepath.Join(serviceDataDirForRoot(fixture.serviceRoot), vmRuntimeDescriptorFileName)
	snapshot, err := readVMRuntimeDescriptorSnapshotWithOwner(descriptorPath, "devbox", uint32(os.Geteuid()), uint32(os.Getegid()))
	if err != nil {
		t.Fatal(err)
	}
	control := defaultVMRuntimeControlFileDeps()
	control.uid = uint32(os.Geteuid())
	control.gid = uint32(os.Getegid())
	if _, _, err := writeVMRuntimeRunningMarker(
		filepath.Join(serviceRunDirForRoot(fixture.serviceRoot), vmRuntimeRunningMarkerFileName), "devbox", target,
		snapshot.SHA256, strings.Repeat("e", 64), os.Getpid(), os.Getpid(), control,
	); err != nil {
		t.Fatal(err)
	}
	replacement := resolved
	replacement.Artifact.ID = "firecracker-v1.18.0-yeet-v1"
	ref := *resolved.CatalogRef
	replacement.CatalogRef = &ref
	replacement.CatalogRef.RuntimeID = replacement.Artifact.ID
	replacement.CatalogRef.UpstreamVersion = "v1.18.0"
	_, err = stageVMRuntimeWithDeps(context.Background(), &fixture.cfg, "devbox", replacement, deps)
	if err == nil || !strings.Contains(err.Error(), "active runtime trial") {
		t.Fatalf("replace live trial error = %v", err)
	}
}

func TestStageVMRuntimeCanReplaceTrustedTerminalFailure(t *testing.T) {
	fixture, deps, _ := newVMRuntimeAdoptionTransactionFixture(t, false)
	adoptVMRuntimeTransitionFixture(t, fixture, deps)
	target, resolved := newVMRuntimeTransitionTarget(t, fixture)
	if _, err := stageVMRuntimeWithDeps(context.Background(), &fixture.cfg, "devbox", resolved, deps); err != nil {
		t.Fatal(err)
	}
	latest := readLatestVMRuntimeAdoptionData(t, fixture.store).Services["devbox"]
	configured := latest.VM.Components.Runtime.Configured
	if _, _, err := fixture.store.MutateService("devbox", func(_ *db.Data, service *db.Service) error {
		service.VM.Components.Runtime.Trial = &db.VMRuntimeTrialConfig{
			State: "pending", CandidateID: target.ID, PreviousID: configured.ID, StartedAt: "2026-07-20T12:00:00Z",
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	descriptorPath := filepath.Join(serviceDataDirForRoot(fixture.serviceRoot), vmRuntimeDescriptorFileName)
	snapshot, err := readVMRuntimeDescriptorSnapshotWithOwner(descriptorPath, "devbox", uint32(os.Geteuid()), uint32(os.Getegid()))
	if err != nil {
		t.Fatal(err)
	}
	control := defaultVMRuntimeControlFileDeps()
	control.uid = uint32(os.Geteuid())
	control.gid = uint32(os.Getegid())
	resultPath := filepath.Join(serviceRunDirForRoot(fixture.serviceRoot), vmRuntimeTrialResultFileName)
	terminalResult := vmRuntimeTrialResult{
		SchemaVersion: vmRuntimeTrialResultSchemaVersion,
		Service:       "devbox", DescriptorSHA256: snapshot.SHA256, LaunchID: strings.Repeat("e", 64),
		Candidate: vmRuntimeTrialIdentityForArtifact(target), Configured: vmRuntimeTrialIdentityForArtifact(configured),
		Outcome: vmRuntimeTrialFailedNoFallback, RunnerPID: os.Getpid(), ChildPID: 0,
		StartedAt: "2026-07-20T12:00:00Z", CompletedAt: "2026-07-20T12:00:01Z", Error: "both launch attempts failed",
	}
	if _, err := writeVMRuntimeTrialResult(resultPath, terminalResult, control); err != nil {
		t.Fatal(err)
	}
	replacement := resolved
	replacement.Artifact.ID = "firecracker-v1.18.0-yeet-v1"
	ref := *resolved.CatalogRef
	replacement.CatalogRef = &ref
	replacement.CatalogRef.RuntimeID = replacement.Artifact.ID
	replacement.CatalogRef.UpstreamVersion = "v1.18.0"
	if _, err := stageVMRuntimeWithDeps(context.Background(), &fixture.cfg, "devbox", replacement, deps); err != nil {
		t.Fatal(err)
	}
	runtimeState := readLatestVMRuntimeAdoptionData(t, fixture.store).Services["devbox"].VM.Components.Runtime
	if runtimeState.Staged == nil || *runtimeState.Staged != replacement.Artifact || runtimeState.Trial != nil {
		t.Fatalf("replacement runtime state = %#v", runtimeState)
	}
	if _, _, err := readTrustedVMRuntimeTrialResult(resultPath, "devbox", control); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prior terminal result survived replacement stage: %v", err)
	}

	// Model a crash after the replacement transaction committed but before its
	// exact result cleanup. The next explicit restart has a new pending lease;
	// its consumer must discard the old generation instead of reporting an
	// immediate terminal failure for the replacement candidate.
	if _, _, err := fixture.store.MutateService("devbox", func(_ *db.Data, service *db.Service) error {
		service.VM.Components.Runtime.Trial = &db.VMRuntimeTrialConfig{
			State: "pending", CandidateID: replacement.Artifact.ID, PreviousID: configured.ID, StartedAt: "2026-07-20T12:01:00Z",
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := writeVMRuntimeTrialResult(resultPath, terminalResult, control); err != nil {
		t.Fatal(err)
	}
	consumerDeps := vmRuntimeTrialConsumerDeps{control: control, coordinator: deps}
	if _, err := consumeVMRuntimeTrialResult(context.Background(), &fixture.cfg, "devbox", consumerDeps); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale terminal result after replacement = %v, want not-exist retry", err)
	}
	if _, _, err := readTrustedVMRuntimeTrialResult(resultPath, "devbox", control); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale terminal result was not removed: %v", err)
	}
}

func TestStageVMRuntimeIdempotentPathRejectsDerivedDrift(t *testing.T) {
	fixture, deps, _ := newVMRuntimeAdoptionTransactionFixture(t, true)
	adoptVMRuntimeTransitionFixture(t, fixture, deps)
	_, resolved := newVMRuntimeTransitionTarget(t, fixture)
	if _, err := stageVMRuntimeWithDeps(context.Background(), &fixture.cfg, "devbox", resolved, deps); err != nil {
		t.Fatal(err)
	}
	descriptorPath := filepath.Join(serviceDataDirForRoot(fixture.serviceRoot), vmRuntimeDescriptorFileName)
	if err := os.WriteFile(descriptorPath, []byte("drifted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := stageVMRuntimeWithDeps(context.Background(), &fixture.cfg, "devbox", resolved, deps)
	if err == nil || !strings.Contains(err.Error(), "does not match staged database state") {
		t.Fatalf("idempotent derived-drift error = %v", err)
	}
}

func TestStageVMRuntimeUsesDurableLocalAliasUnderTransactionLock(t *testing.T) {
	fixture, deps, _ := newVMRuntimeAdoptionTransactionFixture(t, true)
	adoptVMRuntimeTransitionFixture(t, fixture, deps)
	importFixture := newVMRuntimeImportFixture(t)
	artifact, err := importVMRuntime(
		context.Background(), filepath.Join(fixture.dataRoot, "vm-runtimes"), "lab",
		bytes.NewReader(importFixture.archive(t)),
	)
	if err != nil {
		t.Fatal(err)
	}
	resolved := vmRuntimeResolvedTarget{
		Artifact: artifact, Selection: vmRuntimeTargetSelectionLocalAlias, LocalAlias: "lab",
	}
	result, err := stageVMRuntimeWithDeps(context.Background(), &fixture.cfg, "devbox", resolved, deps)
	if err != nil {
		t.Fatal(err)
	}
	if result.Staged == nil || *result.Staged != artifact || result.Staged.Source != "local:lab" {
		t.Fatalf("local transition result = %#v", result)
	}
	latest := readLatestVMRuntimeAdoptionData(t, fixture.store).Services["devbox"].VM.Components.Runtime
	if latest.Staged == nil || *latest.Staged != artifact {
		t.Fatalf("stored local candidate = %#v, want %#v", latest.Staged, artifact)
	}
}

func TestRecoverVMRuntimeTransactionRestoresDerivedStateWhenDatabaseIsOld(t *testing.T) {
	fixture, deps, _ := newVMRuntimeAdoptionTransactionFixture(t, false)
	adoptVMRuntimeTransitionFixture(t, fixture, deps)
	_, resolved := newVMRuntimeTransitionTarget(t, fixture)

	journal, err := openVMRuntimeJournalStore(context.Background(), fixture.dataRoot, deps.journal)
	if err != nil {
		t.Fatal(err)
	}
	view, err := fixture.store.Get()
	if err != nil {
		t.Fatal(err)
	}
	data := view.AsStruct()
	stored, err := validateVMRuntimeTransitionTarget(context.Background(), &fixture.cfg, data, "devbox", resolved)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := prepareVMRuntimeTransitionWithStore(context.Background(), &fixture.cfg, journal, deps, data, stored, resolved)
	if err != nil {
		_ = journal.Close()
		t.Fatal(err)
	}
	oldDescriptor := append([]byte(nil), tx.records[0].OldDescriptor.Contents...)
	if err := tx.publishNewDerived(); err != nil {
		t.Fatal(err)
	}
	// Simulate process loss after derived publication but before DB commit. Do
	// not call tx.Close, because its compensating rollback is the clean path.
	tx.closed = true
	if err := errors.Join(tx.units.Close(), tx.descriptors.Close(), journal.Close()); err != nil {
		t.Fatal(err)
	}

	recovery, err := openVMRuntimeJournalStore(context.Background(), fixture.dataRoot, deps.journal)
	if err != nil {
		t.Fatal(err)
	}
	defer recovery.Close()
	if err := recoverVMRuntimeAdoptionsWithStore(context.Background(), &fixture.cfg, recovery, deps); err != nil {
		t.Fatal(err)
	}
	latest := readLatestVMRuntimeAdoptionData(t, fixture.store).Services["devbox"].VM.Components.Runtime
	if latest.Staged != nil {
		t.Fatalf("recovery committed an uncommitted staged runtime: %#v", latest.Staged)
	}
	if got := readVMRuntimeAdoptionTestFile(t, filepath.Join(serviceDataDirForRoot(fixture.serviceRoot), vmRuntimeDescriptorFileName)); !bytes.Equal(got, oldDescriptor) {
		t.Fatal("recovery did not restore the old runtime descriptor")
	}
	if groups, err := recovery.LoadAll(); err != nil || len(groups) != 0 {
		t.Fatalf("journals after recovery = %#v, %v", groups, err)
	}
}

func adoptVMRuntimeTransitionFixture(t *testing.T, fixture *vmRuntimeAdoptionFixture, deps vmRuntimeAdoptionCoordinatorDeps) {
	t.Helper()
	tx, err := prepareVMRuntimeAdoptionWithDeps(context.Background(), &fixture.cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		_ = tx.Close()
		t.Fatal(err)
	}
	if err := tx.Close(); err != nil {
		t.Fatal(err)
	}
}

func newVMRuntimeTransitionTarget(t *testing.T, fixture *vmRuntimeAdoptionFixture) (db.VMRuntimeArtifactConfig, vmRuntimeResolvedTarget) {
	t.Helper()
	dir := filepath.Join(fixture.dataRoot, "vm-runtimes", "amd64", "firecracker-v1.17.0-yeet-v1", strings.Repeat("a", 64))
	firecracker := filepath.Join(dir, "firecracker")
	jailer := filepath.Join(dir, "jailer")
	manifest := filepath.Join(dir, vmRuntimeManifestFilename)
	writeVMRuntimeAdoptionTestFile(t, firecracker, "candidate-firecracker", 0o755)
	writeVMRuntimeAdoptionTestFile(t, jailer, "candidate-jailer", 0o755)
	writeVMRuntimeAdoptionTestFile(t, manifest, "candidate-manifest", 0o644)
	artifact := db.VMRuntimeArtifactConfig{
		ID: "firecracker-v1.17.0-yeet-v1", Source: "official",
		ManifestSHA256:    vmRuntimeSHA256Bytes([]byte("candidate-manifest")),
		FirecrackerSHA256: vmRuntimeSHA256Bytes([]byte("candidate-firecracker")),
		JailerSHA256:      vmRuntimeSHA256Bytes([]byte("candidate-jailer")),
		Firecracker:       firecracker, Jailer: jailer,
	}
	ref := vmRuntimeCatalogRef{
		RuntimeID: artifact.ID, UpstreamVersion: "v1.17.0", ManifestSHA: artifact.ManifestSHA256,
		ManifestURL: "https://github.com/yeetrun/yeet-vm-images/releases/download/firecracker-v1.17.0-yeet-v1/runtime-manifest.json",
		Support:     "stable",
	}
	return artifact, vmRuntimeResolvedTarget{
		Artifact: artifact, CatalogRef: &ref, Selection: vmRuntimeTargetSelectionOfficialID,
	}
}

func addVMRuntimeTransitionFixtureService(
	t *testing.T,
	fixture *vmRuntimeAdoptionFixture,
	deps vmRuntimeAdoptionCoordinatorDeps,
	name string,
) vmRuntimeAdoptionCoordinatorDeps {
	t.Helper()
	latest := readLatestVMRuntimeAdoptionData(t, fixture.store)
	service := latest.Services["devbox"].Clone()
	service.Name = name
	service.ServiceRoot = filepath.Join(fixture.dataRoot, "zfs-mounts", name)
	service.VM.Disk.Path = filepath.Join(service.ServiceRoot, "data", "rootfs.raw")
	writeVMRuntimeAdoptionTestFile(t, service.VM.Disk.Path, "mutable-worker-disk", 0o600)
	writeVMRuntimeAdoptionTestJSON(t, filepath.Join(serviceRunDirForRoot(service.ServiceRoot), "firecracker.json"), firecrackerConfig{
		BootSource:    firecrackerBootSource{KernelImagePath: service.VM.Image.Kernel, BootArgs: "console=ttyS0"},
		Drives:        []firecrackerDrive{{DriveID: "rootfs", PathOnHost: service.VM.Disk.Path, IsRootDevice: true}},
		MachineConfig: firecrackerMachineConfig{VCPUCount: 2, MemSizeMib: 2048},
	}, 0o644)
	descriptor, err := vmRuntimeDescriptorFromService(service)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeVMRuntimeDescriptorWithDeps(
		filepath.Join(serviceDataDirForRoot(service.ServiceRoot), vmRuntimeDescriptorFileName),
		descriptor,
		deps.descriptor,
	); err != nil {
		t.Fatal(err)
	}
	unitSpec, err := renderVMRuntimeUnitSpec(fixture.cfg, service, filepath.Join(fixture.dataRoot, "run", "catch"))
	if err != nil {
		t.Fatal(err)
	}
	writeVMRuntimeAdoptionTestFile(t, unitSpec.Path, string(unitSpec.Content), 0o644)
	if _, err := fixture.store.MutateData(func(data *db.Data) error {
		data.Services[name] = service
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	deps.inventory.loadUnit = func(_ context.Context, unit string) (vmRuntimeAdoptionLoadedUnit, error) {
		path := filepath.Join(vmSystemdSystemDir, unit)
		raw := readVMRuntimeAdoptionTestFile(t, path)
		withHeader := append([]byte("# "+path+"\n"), raw...)
		argv, paths, err := parseVMRuntimeAdoptionUnit(withHeader)
		if err != nil {
			return vmRuntimeAdoptionLoadedUnit{}, err
		}
		fragments := make([]vmRuntimeAdoptionUnitFragment, len(paths))
		for i, fragmentPath := range paths {
			evidence, err := collectTrustedVMRuntimeAdoptionFileEvidence(fragmentPath, true, deps.inventory.evidence)
			if err != nil {
				return vmRuntimeAdoptionLoadedUnit{}, err
			}
			fragments[i] = vmRuntimeAdoptionUnitFragment{Path: fragmentPath, Evidence: evidence}
		}
		if len(argv) == 0 {
			return vmRuntimeAdoptionLoadedUnit{}, fmt.Errorf("unit %s has no launch command", unit)
		}
		return vmRuntimeAdoptionLoadedUnit{
			Name: unit, ExecStart: argv, Fragments: fragments,
			ActiveState: "inactive", MainPID: 0, NeedDaemonReload: "no",
		}, nil
	}
	return deps
}

func equalVMRuntimeArtifactPointers(left, right *db.VMRuntimeArtifactConfig) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func reflectVMRuntimeLifecycle(left, right db.VMRuntimeLifecycleConfig) bool {
	return left.Policy == right.Policy && left.Channel == right.Channel && left.Configured == right.Configured &&
		equalVMRuntimeArtifactPointers(left.Staged, right.Staged) && equalVMRuntimeArtifactPointers(left.Previous, right.Previous) &&
		left.Trial == nil && right.Trial == nil
}
