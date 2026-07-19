// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
)

func TestVMRuntimeUpdateEnsuresStableAndSelectedOfficialRuntimesWithoutMutation(t *testing.T) {
	server := newTestServer(t)
	catalog := validVMRuntimeCatalog()
	stable := catalog.Architectures["amd64"].Runtimes[0]
	old := stable
	old.RuntimeID = "firecracker-v1.15.0-yeet-v1"
	old.UpstreamVersion = "v1.15.0"
	old.ManifestSHA = strings.Repeat("4", 64)
	catalog.Architectures["amd64"] = vmRuntimeCatalogArchitecture{
		Runtimes: []vmRuntimeCatalogRef{stable, old},
		Channels: catalog.Architectures["amd64"].Channels,
	}
	seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{
		Configured: vmRuntimeCommandArtifact(old, "official"),
		Staged:     vmRuntimeArtifactPtr(vmRuntimeCommandArtifact(stable, "official")),
		Previous:   vmRuntimeArtifactPtr(vmRuntimeCommandArtifact(old, "official-legacy")),
	})
	before, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}

	var ensured []string
	server.vmRuntimeCommandDeps = &vmRuntimeCommandDeps{
		fetchCatalog: func(context.Context) (vmRuntimeCatalog, error) { return catalog, nil },
		ensureRuntime: func(_ context.Context, ref vmRuntimeCatalogRef) (db.VMRuntimeArtifactConfig, error) {
			ensured = append(ensured, ref.RuntimeID)
			return vmRuntimeCommandArtifact(ref, "official"), nil
		},
	}
	var out bytes.Buffer
	if err := server.updateVMRuntimes(context.Background(), &out); err != nil {
		t.Fatalf("updateVMRuntimes: %v", err)
	}
	wantEnsured := []string{old.RuntimeID, stable.RuntimeID}
	if !slices.Equal(ensured, wantEnsured) {
		t.Fatalf("ensured = %v, want %v", ensured, wantEnsured)
	}
	after, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.AsStruct(), after.AsStruct()) {
		t.Fatalf("runtime update mutated DB\nbefore=%#v\nafter=%#v", before.AsStruct(), after.AsStruct())
	}
	if !strings.Contains(out.String(), stable.RuntimeID) || !strings.Contains(out.String(), old.RuntimeID) {
		t.Fatalf("output = %q", out.String())
	}
}

func TestVMRuntimeUpdateRejectsSelectedOfficialRuntimeAbsentFromCatalog(t *testing.T) {
	server := newTestServer(t)
	catalog := validVMRuntimeCatalog()
	missing := vmRuntimeCommandArtifact(catalog.Architectures["amd64"].Runtimes[0], "official")
	missing.ID = "firecracker-v1.14.3-yeet-v1"
	seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{Configured: missing})
	server.vmRuntimeCommandDeps = &vmRuntimeCommandDeps{
		fetchCatalog: func(context.Context) (vmRuntimeCatalog, error) { return catalog, nil },
		ensureRuntime: func(context.Context, vmRuntimeCatalogRef) (db.VMRuntimeArtifactConfig, error) {
			t.Fatal("ensureRuntime called after catalog reference failure")
			return db.VMRuntimeArtifactConfig{}, nil
		},
	}
	err := server.updateVMRuntimes(context.Background(), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "absent from the trusted catalog") {
		t.Fatalf("error = %v", err)
	}
}

func TestVMRuntimeUpgradeStagesExactTargetWithoutServiceActivity(t *testing.T) {
	server := newTestServer(t)
	catalog, stable, candidate := vmRuntimeTargetTestCatalog()
	seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{Configured: vmRuntimeCommandArtifact(stable, "official")})
	serviceActivity := 0
	server.vmRuntimeCommandDeps = &vmRuntimeCommandDeps{
		fetchCatalog: func(context.Context) (vmRuntimeCatalog, error) { return catalog, nil },
		ensureRuntime: func(_ context.Context, ref vmRuntimeCatalogRef) (db.VMRuntimeArtifactConfig, error) {
			return vmRuntimeCommandArtifact(ref, "official"), nil
		},
		stageRuntime: func(_ context.Context, cfg *Config, service string, target vmRuntimeResolvedTarget) (vmRuntimeTransitionResult, error) {
			if cfg != &server.cfg || service != "devbox" || target.Artifact.ID != candidate.RuntimeID || target.Selection != vmRuntimeTargetSelectionOfficialID {
				t.Fatalf("stage arguments = cfg %p service %q target %#v", cfg, service, target)
			}
			return vmRuntimeTransitionResult{Service: service, RunningUnchanged: true, Staged: cloneVMRuntimeArtifact(&target.Artifact)}, nil
		},
		serviceActive: func(string) (bool, error) {
			serviceActivity++
			return true, nil
		},
	}
	var out bytes.Buffer
	if err := server.upgradeVMRuntime(context.Background(), &out, "devbox", candidate.RuntimeID, ""); err != nil {
		t.Fatal(err)
	}
	if serviceActivity != 0 {
		t.Fatalf("service activity checks = %d, want none", serviceActivity)
	}
	if got := out.String(); !strings.Contains(got, candidate.RuntimeID) || !strings.Contains(got, "staged-running-unchanged") {
		t.Fatalf("upgrade output = %q", got)
	}
}

func TestVMRuntimePolicyDefaultsAndPerVMInheritance(t *testing.T) {
	server := newTestServer(t)
	catalog, stable, _ := vmRuntimeTargetTestCatalog()
	seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{
		Configured: vmRuntimeCommandArtifact(stable, "official"),
	})
	server.vmRuntimeCommandDeps = &vmRuntimeCommandDeps{
		fetchCatalog: func(context.Context) (vmRuntimeCatalog, error) { return catalog, nil },
		ensureRuntime: func(_ context.Context, ref vmRuntimeCatalogRef) (db.VMRuntimeArtifactConfig, error) {
			return vmRuntimeCommandArtifact(ref, "official"), nil
		},
		stageRuntime: func(_ context.Context, _ *Config, serviceName string, target vmRuntimeResolvedTarget) (vmRuntimeTransitionResult, error) {
			_, _, err := server.cfg.DB.MutateService(serviceName, func(_ *db.Data, service *db.Service) error {
				runtimeState := &service.VM.Components.Runtime
				if target.PolicyMutation != nil {
					if err := applyVMRuntimePolicyMutation(runtimeState, *target.PolicyMutation); err != nil {
						return err
					}
				}
				runtimeState.Staged = cloneVMRuntimeArtifact(&target.Artifact)
				return nil
			})
			return vmRuntimeTransitionResult{Service: serviceName, Staged: cloneVMRuntimeArtifact(&target.Artifact)}, err
		},
		stagePolicyCohort: func(_ context.Context, _ *Config, _ *db.VMHostConfig, newHost *db.VMHostConfig, _ []vmRuntimePolicyFleetPrecondition, targets []vmRuntimeNamedTarget) error {
			_, err := server.cfg.DB.MutateData(func(data *db.Data) error {
				data.VMHost = newHost.Clone()
				for _, named := range targets {
					data.Services[named.Service].VM.Components.Runtime.Staged = cloneVMRuntimeArtifact(&named.Target.Artifact)
				}
				return nil
			})
			return err
		},
	}

	var out bytes.Buffer
	if err := server.printVMRuntimePolicyDefaults(&out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "manual\tstable") {
		t.Fatalf("default output = %q", got)
	}
	out.Reset()
	if err := server.setVMRuntimePolicyDefaults(context.Background(), &out, cli.VMRuntimePolicyStageOnRestart, cli.VMRuntimeChannelCandidate); err != nil {
		t.Fatal(err)
	}
	if err := server.setVMRuntimePolicy(context.Background(), &out, "devbox", cli.VMRuntimePolicyManual, ""); err != nil {
		t.Fatal(err)
	}
	assertRuntimeCommandPolicy(t, server, "devbox", cli.VMRuntimePolicyManual, cli.VMRuntimeChannelCandidate, cli.VMRuntimePolicyManual, "")
	if err := server.setVMRuntimePolicy(context.Background(), &out, "devbox", cli.VMRuntimePolicyStageOnRestart, cli.VMRuntimeChannelStable); err != nil {
		t.Fatal(err)
	}
	assertRuntimeCommandPolicy(t, server, "devbox", cli.VMRuntimePolicyStageOnRestart, cli.VMRuntimeChannelStable, cli.VMRuntimePolicyStageOnRestart, cli.VMRuntimeChannelStable)
	if err := server.setVMRuntimePolicy(context.Background(), &out, "devbox", cli.VMRuntimePolicyInherit, cli.VMRuntimeChannelCandidate); err != nil {
		t.Fatal(err)
	}
	assertRuntimeCommandPolicy(t, server, "devbox", cli.VMRuntimePolicyStageOnRestart, cli.VMRuntimeChannelCandidate, "", "")
}

func TestVMRuntimePolicyRejectsNonAdoptedServiceWithoutMutation(t *testing.T) {
	server := newTestServer(t)
	if _, err := server.cfg.DB.MutateData(func(data *db.Data) error {
		data.Services = map[string]*db.Service{"legacy": {Name: "legacy", ServiceType: db.ServiceTypeVM, VM: &db.VMConfig{}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	err := server.setVMRuntimePolicy(context.Background(), &bytes.Buffer{}, "legacy", cli.VMRuntimePolicyManual, "")
	if err == nil || !strings.Contains(err.Error(), "not an adopted VM") {
		t.Fatalf("error = %v", err)
	}
}

func TestVMRuntimeProtectionIsSortedAndIdempotent(t *testing.T) {
	server := newTestServer(t)
	first := "firecracker-v1.16.1-yeet-v1"
	second := "firecracker-v1.15.0-yeet-v1"
	for _, id := range []string{first, second, first} {
		if err := server.setVMRuntimeProtection(context.Background(), &bytes.Buffer{}, id, true); err != nil {
			t.Fatal(err)
		}
	}
	assertRuntimeCommandProtections(t, server, []string{second, first})
	for range 2 {
		if err := server.setVMRuntimeProtection(context.Background(), &bytes.Buffer{}, second, false); err != nil {
			t.Fatal(err)
		}
	}
	assertRuntimeCommandProtections(t, server, []string{first})
}

func TestVMRuntimePruneCommandRoutesDryRunThroughHostLifecycle(t *testing.T) {
	server := newTestServer(t)
	server.vmRuntimePruneDeps = &vmRuntimePruneDeps{
		fetchCatalog: func(context.Context) (vmRuntimeCatalog, error) { return validVMRuntimeCatalog(), nil },
		unitState:    func(context.Context, string) (vmRuntimeUnitState, error) { return vmRuntimeUnitState{}, nil },
		processAlive: func(int) bool { return false },
	}
	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, sn: SystemService, rw: readWriter{Writer: &out}}
	if err := execer.vmRuntimeCmdFunc(cli.VMRuntimeFlags{DryRun: true}, []string{cli.VMRuntimeActionPrune}); err != nil {
		t.Fatalf("vm runtime prune --dry-run: %v", err)
	}
	if got := out.String(); got != "No VM runtimes to prune.\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestEffectiveVMRuntimePolicyRejectsCorruptStoredValues(t *testing.T) {
	if _, err := effectiveVMRuntimePolicyFor(&db.VMHostConfig{RuntimePolicy: "automatic"}, nil); err == nil {
		t.Fatal("invalid host policy accepted")
	}
	if _, err := effectiveVMRuntimePolicyFor(nil, &db.VMRuntimeLifecycleConfig{Channel: "nightly"}); err == nil {
		t.Fatal("invalid VM channel accepted")
	}
}

func TestVMRuntimePolicyValidationFailureDoesNotCommit(t *testing.T) {
	server := newTestServer(t)
	catalog := validVMRuntimeCatalog()
	seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{
		Configured: vmRuntimeCommandArtifact(catalog.Architectures["amd64"].Runtimes[0], "official"),
		Channel:    "nightly",
	})
	if _, err := server.cfg.DB.MutateData(func(data *db.Data) error {
		data.VMHost = &db.VMHostConfig{RuntimeChannel: "nightly"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	if err := server.setVMRuntimePolicyDefaults(context.Background(), &bytes.Buffer{}, cli.VMRuntimePolicyManual, ""); err == nil {
		t.Fatal("corrupt retained host channel was accepted")
	}
	after, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.AsStruct(), after.AsStruct()) {
		t.Fatalf("failed host policy validation committed state\nbefore=%#v\nafter=%#v", before.AsStruct(), after.AsStruct())
	}
	if _, err := server.cfg.DB.MutateData(func(data *db.Data) error {
		data.VMHost.RuntimeChannel = cli.VMRuntimeChannelStable
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err = server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	if err := server.setVMRuntimePolicy(context.Background(), &bytes.Buffer{}, "devbox", cli.VMRuntimePolicyManual, ""); err == nil {
		t.Fatal("corrupt retained VM channel was accepted")
	}
	after, err = server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.AsStruct(), after.AsStruct()) {
		t.Fatalf("failed VM policy validation committed state\nbefore=%#v\nafter=%#v", before.AsStruct(), after.AsStruct())
	}
}

func seedRuntimeCommandVM(t *testing.T, server *Server, runtime db.VMRuntimeLifecycleConfig) {
	t.Helper()
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		"devbox": {
			Name:        "devbox",
			ServiceType: db.ServiceTypeVM,
			ServiceRoot: server.defaultServiceRootDir("devbox"),
			VM: &db.VMConfig{Runtime: vmRuntimeFirecracker, Components: &db.VMComponentsConfig{
				Runtime: runtime,
			}},
		},
	}}); err != nil {
		t.Fatalf("seed runtime command VM: %v", err)
	}
}

func vmRuntimeCommandArtifact(ref vmRuntimeCatalogRef, source string) db.VMRuntimeArtifactConfig {
	return db.VMRuntimeArtifactConfig{
		ID:                ref.RuntimeID,
		ManifestSHA256:    ref.ManifestSHA,
		FirecrackerSHA256: strings.Repeat("a", 64),
		JailerSHA256:      strings.Repeat("b", 64),
		Firecracker:       "/var/lib/yeet/vm-runtimes/amd64/" + ref.RuntimeID + "/" + ref.ManifestSHA + "/firecracker",
		Jailer:            "/var/lib/yeet/vm-runtimes/amd64/" + ref.RuntimeID + "/" + ref.ManifestSHA + "/jailer",
		Source:            source,
	}
}

func vmRuntimeArtifactPtr(artifact db.VMRuntimeArtifactConfig) *db.VMRuntimeArtifactConfig {
	return &artifact
}

func assertRuntimeCommandPolicy(t *testing.T, server *Server, service, effectiveMode, effectiveChannel, storedMode, storedChannel string) {
	t.Helper()
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	host := dv.VMHost().AsStruct()
	runtime := dv.AsStruct().Services[service].VM.Components.Runtime
	policy, err := effectiveVMRuntimePolicyFor(host, &runtime)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Mode != effectiveMode || policy.Channel != effectiveChannel || runtime.Policy != storedMode || runtime.Channel != storedChannel {
		t.Fatalf("policy = %#v stored = %q/%q", policy, runtime.Policy, runtime.Channel)
	}
}

func assertRuntimeCommandProtections(t *testing.T, server *Server, want []string) {
	t.Helper()
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	got := dv.VMHost().ProtectedRuntimeIDs().AsSlice()
	if !slices.Equal(got, want) {
		t.Fatalf("protections = %v, want %v", got, want)
	}
}
