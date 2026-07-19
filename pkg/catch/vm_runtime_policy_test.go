// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
)

func TestReconcileVMRuntimePolicyStagesPromotedRuntimeWithoutServiceActivity(t *testing.T) {
	for _, tc := range []struct {
		name       string
		hostMode   string
		hostChan   string
		vmMode     string
		vmChan     string
		wantID     string
		wantFetch  bool
		configured string
	}{
		{name: "inherited manual", hostMode: cli.VMRuntimePolicyManual},
		{name: "explicit manual", hostMode: cli.VMRuntimePolicyStageOnRestart, vmMode: cli.VMRuntimePolicyManual},
		{name: "inherited stable", hostMode: cli.VMRuntimePolicyStageOnRestart, wantID: "stable", wantFetch: true},
		{name: "inherited candidate", hostMode: cli.VMRuntimePolicyStageOnRestart, hostChan: cli.VMRuntimeChannelCandidate, wantID: "candidate", wantFetch: true},
		{name: "VM candidate override", hostMode: cli.VMRuntimePolicyManual, vmMode: cli.VMRuntimePolicyStageOnRestart, vmChan: cli.VMRuntimeChannelCandidate, wantID: "candidate", wantFetch: true},
		{name: "already current", hostMode: cli.VMRuntimePolicyStageOnRestart, configured: "stable", wantFetch: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(t)
			catalog, stable, candidate := vmRuntimeTargetTestCatalog()
			configured := stable
			if tc.configured == "" {
				configured = stable
				configured.RuntimeID = "firecracker-v1.15.0-yeet-v1"
				configured.UpstreamVersion = "v1.15.0"
				configured.ManifestSHA = strings.Repeat("5", 64)
			}
			seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{
				Policy: tc.vmMode, Channel: tc.vmChan, Configured: vmRuntimeCommandArtifact(configured, "official"),
			})
			if _, err := server.cfg.DB.MutateData(func(data *db.Data) error {
				data.VMHost = &db.VMHostConfig{RuntimePolicy: tc.hostMode, RuntimeChannel: tc.hostChan}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			fetches := 0
			var staged vmRuntimeResolvedTarget
			server.vmRuntimeCommandDeps = &vmRuntimeCommandDeps{
				fetchCatalog: func(context.Context) (vmRuntimeCatalog, error) {
					fetches++
					return catalog, nil
				},
				ensureRuntime: func(_ context.Context, ref vmRuntimeCatalogRef) (db.VMRuntimeArtifactConfig, error) {
					return vmRuntimeCommandArtifact(ref, "official"), nil
				},
				stageRuntime: func(_ context.Context, _ *Config, _ string, target vmRuntimeResolvedTarget) (vmRuntimeTransitionResult, error) {
					staged = target
					return vmRuntimeTransitionResult{Service: "devbox", Staged: cloneVMRuntimeArtifact(&target.Artifact)}, nil
				},
				serviceActive: func(string) (bool, error) {
					t.Fatal("runtime policy inspected or changed service activity")
					return false, nil
				},
			}
			if _, err := server.reconcileVMRuntimePolicy(context.Background(), "devbox"); err != nil {
				t.Fatal(err)
			}
			if got := fetches > 0; got != tc.wantFetch {
				t.Fatalf("catalog fetched=%t, want %t", got, tc.wantFetch)
			}
			wantID := ""
			if tc.wantID == "stable" {
				wantID = stable.RuntimeID
			} else if tc.wantID == "candidate" {
				wantID = candidate.RuntimeID
			}
			if staged.Artifact.ID != wantID {
				t.Fatalf("staged runtime = %q, want %q", staged.Artifact.ID, wantID)
			}
		})
	}
}

func TestReconcileVMRuntimePolicySkipsCustomRuntimeAndExistingStage(t *testing.T) {
	server := newTestServer(t)
	_, stable, candidate := vmRuntimeTargetTestCatalog()
	custom := vmRuntimeCommandArtifact(stable, "custom-legacy")
	custom.ID = "legacy-firecracker-v1-16-1-" + strings.Repeat("a", 64) + "-jailer-" + strings.Repeat("b", 64)
	seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{
		Policy: cli.VMRuntimePolicyStageOnRestart, Configured: custom,
	})
	server.vmRuntimeCommandDeps = &vmRuntimeCommandDeps{
		fetchCatalog: func(context.Context) (vmRuntimeCatalog, error) {
			t.Fatal("custom runtime triggered official discovery")
			return vmRuntimeCatalog{}, nil
		},
		stageRuntime: func(context.Context, *Config, string, vmRuntimeResolvedTarget) (vmRuntimeTransitionResult, error) {
			t.Fatal("custom runtime was staged")
			return vmRuntimeTransitionResult{}, nil
		},
	}
	if _, err := server.reconcileVMRuntimePolicy(context.Background(), "devbox"); err != nil {
		t.Fatal(err)
	}

	seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{
		Policy:     cli.VMRuntimePolicyStageOnRestart,
		Configured: vmRuntimeCommandArtifact(stable, "official"),
		Staged:     vmRuntimeArtifactPtr(vmRuntimeCommandArtifact(candidate, "official")),
	})
	if _, err := server.reconcileVMRuntimePolicy(context.Background(), "devbox"); err != nil {
		t.Fatal(err)
	}
}

func TestApplyVMRuntimeHostPolicy(t *testing.T) {
	runtime := vmRuntimeCommandArtifact(vmRuntimeTargetTestCatalogStable(t), "official")
	data := &db.Data{
		VMHost: &db.VMHostConfig{RuntimePolicy: cli.VMRuntimePolicyManual, RuntimeChannel: cli.VMRuntimeChannelStable},
		Services: map[string]*db.Service{
			"devbox": {
				Name: "devbox", ServiceType: db.ServiceTypeVM,
				VM: &db.VMConfig{Components: &db.VMComponentsConfig{Runtime: db.VMRuntimeLifecycleConfig{Configured: runtime}}},
			},
		},
	}
	fleet := snapshotVMRuntimePolicyFleet(data)
	if err := applyVMRuntimeHostPolicy(
		data, fleet,
		cli.VMRuntimePolicyManual, cli.VMRuntimeChannelStable,
		cli.VMRuntimePolicyStageOnRestart, cli.VMRuntimeChannelCandidate,
	); err != nil {
		t.Fatal(err)
	}
	if data.VMHost.RuntimePolicy != cli.VMRuntimePolicyStageOnRestart || data.VMHost.RuntimeChannel != cli.VMRuntimeChannelCandidate {
		t.Fatalf("host policy = %#v", data.VMHost)
	}
	if err := applyVMRuntimeHostPolicy(data, fleet, cli.VMRuntimePolicyManual, cli.VMRuntimeChannelStable, cli.VMRuntimePolicyManual, cli.VMRuntimeChannelStable); err == nil {
		t.Fatal("stale host policy precondition accepted")
	}
	data.Services["devbox"].VM.Components.Runtime.Channel = cli.VMRuntimeChannelCandidate
	if err := applyVMRuntimeHostPolicy(data, fleet, cli.VMRuntimePolicyStageOnRestart, cli.VMRuntimeChannelCandidate, cli.VMRuntimePolicyManual, cli.VMRuntimeChannelStable); err == nil {
		t.Fatal("stale fleet precondition accepted")
	}

	data = &db.Data{}
	if err := applyVMRuntimeHostPolicy(data, nil, "", "", cli.VMRuntimePolicyManual, cli.VMRuntimeChannelStable); err != nil {
		t.Fatal(err)
	}
	if data.VMHost == nil {
		t.Fatal("host policy was not initialized")
	}
}

func vmRuntimeTargetTestCatalogStable(t *testing.T) vmRuntimeCatalogRef {
	t.Helper()
	_, stable, _ := vmRuntimeTargetTestCatalog()
	return stable
}

func TestReconcileVMRuntimePolicyStagesNewerRuntimeForOfficialLegacySource(t *testing.T) {
	server := newTestServer(t)
	catalog, stable, _ := vmRuntimeTargetTestCatalog()
	legacy := vmRuntimeCommandArtifact(stable, string(vmRuntimeAdoptionOfficialLegacy))
	legacy.ID = "legacy-firecracker-v1-15-0-" + strings.Repeat("a", 64) + "-jailer-" + strings.Repeat("b", 64)
	seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{
		Policy: cli.VMRuntimePolicyStageOnRestart, Configured: legacy,
	})
	var staged string
	server.vmRuntimeCommandDeps = &vmRuntimeCommandDeps{
		fetchCatalog: func(context.Context) (vmRuntimeCatalog, error) { return catalog, nil },
		ensureRuntime: func(_ context.Context, ref vmRuntimeCatalogRef) (db.VMRuntimeArtifactConfig, error) {
			return vmRuntimeCommandArtifact(ref, "official"), nil
		},
		stageRuntime: func(_ context.Context, _ *Config, _ string, target vmRuntimeResolvedTarget) (vmRuntimeTransitionResult, error) {
			staged = target.Artifact.ID
			return vmRuntimeTransitionResult{Service: "devbox", Staged: cloneVMRuntimeArtifact(&target.Artifact)}, nil
		},
	}
	if _, err := server.reconcileVMRuntimePolicy(context.Background(), "devbox"); err != nil {
		t.Fatal(err)
	}
	if staged != stable.RuntimeID {
		t.Fatalf("official legacy staged %q, want %q", staged, stable.RuntimeID)
	}
}

func TestPromotedVMRuntimePackagingRevisionNeverDowngrades(t *testing.T) {
	_, stable, _ := vmRuntimeTargetTestCatalog()
	stable.RuntimeID = "firecracker-v1.16.1-yeet-v1"
	stable.UpstreamVersion = "v1.16.1"
	configured := vmRuntimeCommandArtifact(stable, "official")
	configured.ID = "firecracker-v1.16.1-yeet-v2"
	newer, err := promotedVMRuntimeIsNewer(stable, configured)
	if err != nil {
		t.Fatal(err)
	}
	if newer {
		t.Fatal("older packaging revision was treated as newer")
	}
	stable.RuntimeID = "firecracker-v1.16.1-yeet-v3"
	newer, err = promotedVMRuntimeIsNewer(stable, configured)
	if err != nil {
		t.Fatal(err)
	}
	if !newer {
		t.Fatal("newer packaging revision was not selected")
	}
}

func TestReconcileVMRuntimePoliciesOnCatchUpgradeUsesOneVerifiedCatalogAndCacheArtifact(t *testing.T) {
	server := newTestServer(t)
	catalog, stable, _ := vmRuntimeTargetTestCatalog()
	old := stable
	old.RuntimeID = "firecracker-v1.15.0-yeet-v1"
	old.UpstreamVersion = "v1.15.0"
	old.ManifestSHA = strings.Repeat("5", 64)
	seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{
		Policy: cli.VMRuntimePolicyStageOnRestart, Configured: vmRuntimeCommandArtifact(old, "official"),
	})
	if _, err := server.cfg.DB.MutateData(func(data *db.Data) error {
		clone := data.Services["devbox"].Clone()
		clone.Name = "worker"
		data.Services["worker"] = clone
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var fetches, ensures int
	var staged []string
	server.vmRuntimeCommandDeps = &vmRuntimeCommandDeps{
		fetchCatalog: func(context.Context) (vmRuntimeCatalog, error) { fetches++; return catalog, nil },
		ensureRuntime: func(_ context.Context, ref vmRuntimeCatalogRef) (db.VMRuntimeArtifactConfig, error) {
			ensures++
			return vmRuntimeCommandArtifact(ref, "official"), nil
		},
		stageRuntime: func(_ context.Context, _ *Config, service string, target vmRuntimeResolvedTarget) (vmRuntimeTransitionResult, error) {
			staged = append(staged, service)
			return vmRuntimeTransitionResult{Service: service, Staged: cloneVMRuntimeArtifact(&target.Artifact)}, nil
		},
	}
	summary, err := server.reconcileVMRuntimePoliciesForCatchUpgrade(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fetches != 1 || ensures != 1 || !reflect.DeepEqual(staged, []string{"devbox", "worker"}) || !reflect.DeepEqual(summary.Staged, staged) {
		t.Fatalf("fetches=%d ensures=%d staged=%v summary=%#v", fetches, ensures, staged, summary)
	}
}

func TestReconcileVMRuntimePoliciesOnCatchUpgradeWarnsWithoutMutationOnDiscoveryFailure(t *testing.T) {
	server := newTestServer(t)
	_, stable, _ := vmRuntimeTargetTestCatalog()
	old := stable
	old.RuntimeID = "firecracker-v1.15.0-yeet-v1"
	old.UpstreamVersion = "v1.15.0"
	old.ManifestSHA = strings.Repeat("5", 64)
	seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{
		Policy: cli.VMRuntimePolicyStageOnRestart, Configured: vmRuntimeCommandArtifact(old, "official"),
	})
	before, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	server.vmRuntimeCommandDeps = &vmRuntimeCommandDeps{
		fetchCatalog: func(context.Context) (vmRuntimeCatalog, error) {
			return vmRuntimeCatalog{}, errors.New("network unavailable")
		},
		stageRuntime: func(context.Context, *Config, string, vmRuntimeResolvedTarget) (vmRuntimeTransitionResult, error) {
			t.Fatal("Catch upgrade staged after discovery failure")
			return vmRuntimeTransitionResult{}, nil
		},
	}
	summary, err := server.reconcileVMRuntimePoliciesForCatchUpgrade(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Warnings) != 1 || !strings.Contains(summary.Warnings[0], "network unavailable") {
		t.Fatalf("warnings = %v", summary.Warnings)
	}
	after, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.AsStruct(), after.AsStruct()) {
		t.Fatal("Catch upgrade discovery warning mutated VM state")
	}
}

func TestReconcileVMRuntimePolicyRejectsRevokedOrUnverifiedPromotion(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*vmRuntimeCatalogRef)
		ensure func(vmRuntimeCatalogRef) (db.VMRuntimeArtifactConfig, error)
		want   string
	}{
		{name: "revoked", mutate: func(ref *vmRuntimeCatalogRef) { ref.Support = "revoked" }, want: "revoked"},
		{name: "cache failure", ensure: func(vmRuntimeCatalogRef) (db.VMRuntimeArtifactConfig, error) {
			return db.VMRuntimeArtifactConfig{}, errors.New("download unavailable")
		}, want: "download unavailable"},
		{name: "wrong cache identity", ensure: func(ref vmRuntimeCatalogRef) (db.VMRuntimeArtifactConfig, error) {
			artifact := vmRuntimeCommandArtifact(ref, "official")
			artifact.ManifestSHA256 = strings.Repeat("f", 64)
			return artifact, nil
		}, want: "exact catalog identity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(t)
			catalog, stable, candidate := vmRuntimeTargetTestCatalog()
			old := stable
			old.RuntimeID = "firecracker-v1.15.0-yeet-v1"
			old.UpstreamVersion = "v1.15.0"
			old.ManifestSHA = strings.Repeat("5", 64)
			seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{
				Policy: cli.VMRuntimePolicyStageOnRestart, Channel: cli.VMRuntimeChannelCandidate,
				Configured: vmRuntimeCommandArtifact(old, "official"),
			})
			if tc.mutate != nil {
				architecture := catalog.Architectures["amd64"]
				for i := range architecture.Runtimes {
					if architecture.Runtimes[i].RuntimeID == candidate.RuntimeID {
						tc.mutate(&architecture.Runtimes[i])
					}
				}
				catalog.Architectures["amd64"] = architecture
			}
			staged := false
			server.vmRuntimeCommandDeps = &vmRuntimeCommandDeps{
				fetchCatalog: func(context.Context) (vmRuntimeCatalog, error) { return catalog, nil },
				ensureRuntime: func(_ context.Context, ref vmRuntimeCatalogRef) (db.VMRuntimeArtifactConfig, error) {
					if tc.ensure != nil {
						return tc.ensure(ref)
					}
					return vmRuntimeCommandArtifact(ref, "official"), nil
				},
				stageRuntime: func(context.Context, *Config, string, vmRuntimeResolvedTarget) (vmRuntimeTransitionResult, error) {
					staged = true
					return vmRuntimeTransitionResult{}, nil
				},
			}
			if _, err := server.reconcileVMRuntimePolicy(context.Background(), "devbox"); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if staged {
				t.Fatal("untrusted promoted runtime was staged")
			}
		})
	}
}

func TestVMRuntimePolicySetterDiscoveryFailureLeavesPolicyAndSelectionsIntact(t *testing.T) {
	for _, defaults := range []bool{false, true} {
		name := "per VM"
		if defaults {
			name = "defaults"
		}
		t.Run(name, func(t *testing.T) {
			server := newTestServer(t)
			_, stable, _ := vmRuntimeTargetTestCatalog()
			old := stable
			old.RuntimeID = "firecracker-v1.15.0-yeet-v1"
			old.UpstreamVersion = "v1.15.0"
			old.ManifestSHA = strings.Repeat("5", 64)
			seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{Configured: vmRuntimeCommandArtifact(old, "official")})
			before, err := server.cfg.DB.Get()
			if err != nil {
				t.Fatal(err)
			}
			server.vmRuntimeCommandDeps = &vmRuntimeCommandDeps{
				fetchCatalog: func(context.Context) (vmRuntimeCatalog, error) {
					return vmRuntimeCatalog{}, errors.New("catalog offline")
				},
			}
			if defaults {
				err = server.setVMRuntimePolicyDefaults(context.Background(), &bytes.Buffer{}, cli.VMRuntimePolicyStageOnRestart, cli.VMRuntimeChannelStable)
			} else {
				err = server.setVMRuntimePolicy(context.Background(), &bytes.Buffer{}, "devbox", cli.VMRuntimePolicyStageOnRestart, cli.VMRuntimeChannelStable)
			}
			if err == nil || !strings.Contains(err.Error(), "catalog offline") {
				t.Fatalf("setter error = %v", err)
			}
			after, err := server.cfg.DB.Get()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before.AsStruct(), after.AsStruct()) {
				t.Fatalf("failed policy setter mutated state\nbefore=%#v\nafter=%#v", before.AsStruct(), after.AsStruct())
			}
		})
	}
}

func TestVMRuntimePolicySetterStageFailureRestoresPolicyAndSelections(t *testing.T) {
	for _, defaults := range []bool{false, true} {
		name := "per VM"
		if defaults {
			name = "defaults"
		}
		t.Run(name, func(t *testing.T) {
			server := newTestServer(t)
			catalog, stable, _ := vmRuntimeTargetTestCatalog()
			old := stable
			old.RuntimeID = "firecracker-v1.15.0-yeet-v1"
			old.UpstreamVersion = "v1.15.0"
			old.ManifestSHA = strings.Repeat("5", 64)
			seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{Configured: vmRuntimeCommandArtifact(old, "official")})
			before, err := server.cfg.DB.Get()
			if err != nil {
				t.Fatal(err)
			}
			server.vmRuntimeCommandDeps = &vmRuntimeCommandDeps{
				fetchCatalog: func(context.Context) (vmRuntimeCatalog, error) { return catalog, nil },
				ensureRuntime: func(_ context.Context, ref vmRuntimeCatalogRef) (db.VMRuntimeArtifactConfig, error) {
					return vmRuntimeCommandArtifact(ref, "official"), nil
				},
				stageRuntime: func(context.Context, *Config, string, vmRuntimeResolvedTarget) (vmRuntimeTransitionResult, error) {
					return vmRuntimeTransitionResult{}, errors.New("descriptor transaction failed")
				},
				stagePolicyCohort: func(context.Context, *Config, *db.VMHostConfig, *db.VMHostConfig, []vmRuntimePolicyFleetPrecondition, []vmRuntimeNamedTarget) error {
					return errors.New("descriptor transaction failed")
				},
			}
			if defaults {
				err = server.setVMRuntimePolicyDefaults(context.Background(), &bytes.Buffer{}, cli.VMRuntimePolicyStageOnRestart, cli.VMRuntimeChannelStable)
			} else {
				err = server.setVMRuntimePolicy(context.Background(), &bytes.Buffer{}, "devbox", cli.VMRuntimePolicyStageOnRestart, cli.VMRuntimeChannelStable)
			}
			if err == nil || !strings.Contains(err.Error(), "descriptor transaction failed") {
				t.Fatalf("setter error = %v", err)
			}
			after, err := server.cfg.DB.Get()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before.AsStruct(), after.AsStruct()) {
				t.Fatalf("failed policy stage mutated state\nbefore=%#v\nafter=%#v", before.AsStruct(), after.AsStruct())
			}
		})
	}
}

func TestVMRuntimePolicyDefaultsReconcileNewPromotionWhenPolicyIsUnchanged(t *testing.T) {
	server := newTestServer(t)
	catalog, stable, _ := vmRuntimeTargetTestCatalog()
	old := stable
	old.RuntimeID = "firecracker-v1.15.0-yeet-v1"
	old.UpstreamVersion = "v1.15.0"
	old.ManifestSHA = strings.Repeat("5", 64)
	seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{Configured: vmRuntimeCommandArtifact(old, "official")})
	if _, err := server.cfg.DB.MutateData(func(data *db.Data) error {
		data.VMHost = &db.VMHostConfig{RuntimePolicy: cli.VMRuntimePolicyStageOnRestart, RuntimeChannel: cli.VMRuntimeChannelStable}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cohorts := 0
	server.vmRuntimeCommandDeps = &vmRuntimeCommandDeps{
		fetchCatalog: func(context.Context) (vmRuntimeCatalog, error) { return catalog, nil },
		ensureRuntime: func(_ context.Context, ref vmRuntimeCatalogRef) (db.VMRuntimeArtifactConfig, error) {
			return vmRuntimeCommandArtifact(ref, "official"), nil
		},
		stagePolicyCohort: func(_ context.Context, _ *Config, oldHost, newHost *db.VMHostConfig, _ []vmRuntimePolicyFleetPrecondition, targets []vmRuntimeNamedTarget) error {
			cohorts++
			if !reflect.DeepEqual(oldHost, newHost) || len(targets) != 1 || targets[0].Target.Artifact.ID != stable.RuntimeID {
				t.Fatalf("same-policy cohort old=%#v new=%#v targets=%#v", oldHost, newHost, targets)
			}
			_, err := server.cfg.DB.MutateData(func(data *db.Data) error {
				data.Services[targets[0].Service].VM.Components.Runtime.Staged = cloneVMRuntimeArtifact(&targets[0].Target.Artifact)
				return nil
			})
			return err
		},
	}
	if err := server.setVMRuntimePolicyDefaults(context.Background(), &bytes.Buffer{}, cli.VMRuntimePolicyStageOnRestart, cli.VMRuntimeChannelStable); err != nil {
		t.Fatal(err)
	}
	latest, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	staged := latest.AsStruct().Services["devbox"].VM.Components.Runtime.Staged
	if cohorts != 1 || staged == nil || staged.ID != stable.RuntimeID {
		t.Fatalf("cohorts=%d staged=%#v", cohorts, staged)
	}
}

func TestVMRuntimePolicyNoTargetMutationRejectsLifecycleDrift(t *testing.T) {
	server := newTestServer(t)
	_, stable, _ := vmRuntimeTargetTestCatalog()
	seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{Configured: vmRuntimeCommandArtifact(stable, "official")})
	view, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	oldRuntime := view.AsStruct().Services["devbox"].VM.Components.Runtime
	if _, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, service *db.Service) error {
		service.VM.Components.Runtime.Configured.ID = "firecracker-v1.16.1-yeet-v2"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.mutateStoredVMRuntimePolicy(context.Background(), "devbox", oldRuntime, cli.VMRuntimePolicyManual, cli.VMRuntimeChannelStable); err == nil || !strings.Contains(err.Error(), "lifecycle changed") {
		t.Fatalf("stale per-VM mutation error = %v", err)
	}
	latest, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	if latest.AsStruct().Services["devbox"].VM.Components.Runtime.Policy != "" {
		t.Fatal("stale per-VM policy mutation committed")
	}
}

func TestVMRuntimePolicyFleetPreconditionRejectsNewOrChangedVM(t *testing.T) {
	server := newTestServer(t)
	_, stable, _ := vmRuntimeTargetTestCatalog()
	seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{Configured: vmRuntimeCommandArtifact(stable, "official")})
	view, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	expected := snapshotVMRuntimePolicyFleet(view.AsStruct())
	for _, mutate := range []func(*db.Data){
		func(data *db.Data) {
			data.Services["devbox"].VM.Components.Runtime.Configured.ID = "firecracker-v1.15.0-yeet-v1"
		},
		func(data *db.Data) {
			worker := data.Services["devbox"].Clone()
			worker.Name = "worker"
			data.Services["worker"] = worker
		},
	} {
		changed := view.AsStruct()
		mutate(changed)
		if err := validateVMRuntimePolicyFleetPrecondition(changed, expected); err == nil {
			t.Fatal("stale fleet precondition accepted")
		}
	}
}
