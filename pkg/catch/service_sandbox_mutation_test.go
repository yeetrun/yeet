// Copyright (c) 2026 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
)

func TestServiceSandboxMutationRejectsIneligibleBeforeDependencyWork(t *testing.T) {
	server := newTestServer(t)
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		CatchService:  {Name: CatchService, ServiceType: db.ServiceTypeSystemd},
		SystemService: {Name: SystemService, ServiceType: db.ServiceTypeSystemd},
		"compose":     {Name: "compose", ServiceType: db.ServiceTypeDockerCompose},
		"vm":          {Name: "vm", ServiceType: db.ServiceTypeVM},
	}}); err != nil {
		t.Fatal(err)
	}
	oldEnsure := ensureBubblewrapForServiceSandboxMutation
	t.Cleanup(func() { ensureBubblewrapForServiceSandboxMutation = oldEnsure })
	ensureBubblewrapForServiceSandboxMutation = func(context.Context) error {
		t.Fatal("ineligible sandbox mutation reached Bubblewrap dependency work")
		return nil
	}

	for _, name := range []string{"missing", CatchService, SystemService, "compose", "vm"} {
		t.Run(name, func(t *testing.T) {
			if _, err := server.planServiceSandboxMutation(context.Background(), name, cli.SandboxOptions{State: "on", StateSet: true}); err == nil {
				t.Fatalf("planServiceSandboxMutation(%q) error = nil", name)
			}
		})
	}

	t.Run("inconsistent active artifact", func(t *testing.T) {
		server, service := newServiceSandboxMutationFixture(t, serviceSandboxPolicy{State: "off"})
		service.Artifacts[db.ArtifactNetNSResolv] = &db.Artifact{Refs: map[db.ArtifactRef]string{
			"latest": filepath.Join(t.TempDir(), "resolv.conf"),
		}}
		if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{service.Name: service}}); err != nil {
			t.Fatal(err)
		}
		injected := errors.New("dependency work reached")
		ensureBubblewrapForServiceSandboxMutation = func(context.Context) error { return injected }
		_, err := server.planServiceSandboxMutation(context.Background(), service.Name, cli.SandboxOptions{State: "on", StateSet: true})
		if err == nil || errors.Is(err, injected) || !strings.Contains(err.Error(), "no exact") {
			t.Fatalf("inconsistent artifact error = %v, want pre-dependency exact-ref rejection", err)
		}
	})

	for _, tt := range []struct {
		name       string
		artifact   db.ArtifactName
		unreadable bool
	}{
		{name: "non-regular unit", artifact: db.ArtifactSystemdUnit},
		{name: "non-regular payload", artifact: db.ArtifactBinary},
		{name: "unreadable unit", artifact: db.ArtifactSystemdUnit, unreadable: true},
		{name: "unreadable payload", artifact: db.ArtifactBinary, unreadable: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.unreadable && os.Geteuid() == 0 {
				t.Skip("root can read mode-000 regular files")
			}
			server, service := newServiceSandboxMutationFixture(t, serviceSandboxPolicy{State: "off"})
			path := t.TempDir()
			if tt.unreadable {
				path = filepath.Join(path, "unreadable")
				if err := os.WriteFile(path, []byte("unreadable"), 0o000); err != nil {
					t.Fatal(err)
				}
			}
			service.Artifacts[tt.artifact].Refs[db.Gen(service.Generation)] = path
			if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{service.Name: service}}); err != nil {
				t.Fatal(err)
			}
			ensureBubblewrapForServiceSandboxMutation = func(context.Context) error {
				t.Fatal("invalid active artifact reached dependency work")
				return nil
			}
			if _, err := server.planServiceSandboxMutation(context.Background(), service.Name, cli.SandboxOptions{State: "on", StateSet: true}); err == nil {
				t.Fatal("invalid active artifact was accepted")
			}
		})
	}
}

func TestServiceSandboxMutationRejectsNilExactSandboxPolicyBeforeSideEffects(t *testing.T) {
	server, service := newServiceSandboxMutationFixture(t, serviceSandboxPolicy{State: "off"})
	service.Sandbox.Refs[db.Gen(service.Generation)] = nil
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{service.Name: service}}); err != nil {
		t.Fatal(err)
	}
	beforeFiles := serviceSandboxMutationUnitFiles(t, service)
	oldEnsure := ensureBubblewrapForServiceSandboxMutation
	oldMigrate := migrateServiceSandboxGeneration
	t.Cleanup(func() {
		ensureBubblewrapForServiceSandboxMutation = oldEnsure
		migrateServiceSandboxGeneration = oldMigrate
	})
	ensureBubblewrapForServiceSandboxMutation = func(context.Context) error {
		t.Fatal("nil exact sandbox policy reached dependency work")
		return nil
	}
	migrateServiceSandboxGeneration = func(context.Context, *Server, serviceIdentityMigrationRequest, io.Writer) (serviceIdentityMigrationResult, error) {
		t.Fatal("nil exact sandbox policy reached journaled migration")
		return serviceIdentityMigrationResult{}, nil
	}
	err := server.updateServiceSandboxLocked(context.Background(), service.Name, cli.SandboxOptions{State: "on", StateSet: true}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "nil exact sandbox policy") {
		t.Fatalf("nil exact sandbox policy error = %v", err)
	}
	if afterFiles := serviceSandboxMutationUnitFiles(t, service); !reflect.DeepEqual(afterFiles, beforeFiles) {
		t.Fatalf("nil-policy unit files = %v, want %v", afterFiles, beforeFiles)
	}
	got, viewErr := server.serviceView(service.Name)
	if viewErr != nil || got.AsStruct().Sandbox.Refs[db.Gen(service.Generation)] != nil {
		t.Fatalf("nil-policy record changed: %#v, %v", got.AsStruct(), viewErr)
	}
}

func TestServiceSandboxMutationApplyUsesJournaledGenerationRequest(t *testing.T) {
	server, service := newServiceSandboxMutationFixture(t, serviceSandboxPolicy{State: "off"})
	restore := installServiceSandboxMutationTestDeps(t)
	defer restore()
	verifyGeneratedSystemdUnitForSandboxMutation = func(context.Context, string) error { return nil }
	oldMigrate := migrateServiceSandboxGeneration
	t.Cleanup(func() { migrateServiceSandboxGeneration = oldMigrate })
	var captured serviceIdentityMigrationRequest
	migrateServiceSandboxGeneration = func(_ context.Context, got *Server, req serviceIdentityMigrationRequest, out io.Writer) (serviceIdentityMigrationResult, error) {
		if got != server || out == nil {
			t.Fatalf("migration callback = server %p out %v", got, out)
		}
		captured = req
		return serviceIdentityMigrationResult{Restarted: true}, nil
	}
	var out strings.Builder
	err := server.updateServiceSandboxLocked(context.Background(), service.Name, cli.SandboxOptions{
		State: "off", StateSet: true, ReadOnlySet: true, ReadOnlyReset: true,
		ReadOnly:    []cli.SandboxExposure{{Source: "/source with space", Destination: "/operator read only"}},
		WritableSet: true, WritableReset: true,
		Writable: []cli.SandboxExposure{{Source: "/writable source", Destination: "/operator writable"}},
	}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if captured.Service != service.Name || captured.TargetService == nil || captured.TargetService.Generation != 2 ||
		captured.StageGeneration == nil || len(captured.GenerationPaths) == 0 || len(captured.GenerationUnits) == 0 ||
		!captured.PreserveTargetServiceIdentity {
		t.Fatalf("migration request = %#v", captured)
	}
	if captured.Requested != "root:root" || captured.Target.Persisted != *service.Identity {
		t.Fatalf("migration identity = %q %#v, want preserved %#v", captured.Requested, captured.Target.Persisted, *service.Identity)
	}
	if captured.GenerationDiagnostic.Mutation != "sandbox service generation mutation" ||
		!strings.Contains(captured.GenerationDiagnostic.Retry, "yeet service set api --sandbox=off") {
		t.Fatalf("sandbox generation diagnostic = %#v", captured.GenerationDiagnostic)
	}
	flags, rest := replayServiceSandboxMutationGuidance(t, captured.GenerationDiagnostic.Retry)
	if !reflect.DeepEqual(rest, []string{service.Name}) {
		t.Fatalf("sandbox generation retry positional args = %q, want [%q]", rest, service.Name)
	}
	if !flags.Sandbox.ReadOnlyReset || !flags.Sandbox.WritableReset {
		t.Fatalf("sandbox generation retry resets = ro:%t rw:%t, want both", flags.Sandbox.ReadOnlyReset, flags.Sandbox.WritableReset)
	}
	replayed, replayErr := applyServiceSandboxPolicyPatch(service.Name, serviceSandboxPolicy{State: "off"}, false, flags.Sandbox)
	if replayErr != nil {
		t.Fatalf("replay sandbox generation retry %q: %v", captured.GenerationDiagnostic.Retry, replayErr)
	}
	wantPolicy := mustServiceSandboxPolicyForExactGeneration(t, captured.TargetService, captured.TargetService.Generation)
	if !reflect.DeepEqual(replayed, wantPolicy) {
		t.Fatalf("replayed sandbox generation retry policy = %#v, want %#v", replayed, wantPolicy)
	}
	if !strings.Contains(out.String(), "sandbox") || !strings.Contains(out.String(), "restarted") {
		t.Fatalf("mutation output = %q", out.String())
	}
}

func TestServiceSandboxMutationCancellationAtMigrationBoundaryCleansStagedUnit(t *testing.T) {
	server, service := newServiceSandboxMutationFixture(t, serviceSandboxPolicy{State: "off"})
	before := serviceSandboxMutationUnitFiles(t, service)
	restore := installServiceSandboxMutationTestDeps(t)
	defer restore()
	ctx, cancel := context.WithCancel(context.Background())
	verifyGeneratedSystemdUnitForSandboxMutation = func(context.Context, string) error {
		cancel()
		return nil
	}
	oldMigrate := migrateServiceSandboxGeneration
	t.Cleanup(func() { migrateServiceSandboxGeneration = oldMigrate })
	migrateServiceSandboxGeneration = func(context.Context, *Server, serviceIdentityMigrationRequest, io.Writer) (serviceIdentityMigrationResult, error) {
		t.Fatal("canceled mutation reached journaled migration")
		return serviceIdentityMigrationResult{}, nil
	}
	err := server.updateServiceSandboxLocked(ctx, service.Name, cli.SandboxOptions{State: "off", StateSet: true, ReadOnlySet: true, ReadOnlyReset: true,
		ReadOnly: []cli.SandboxExposure{{Source: "/source", Destination: "/operator"}}}, io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled mutation error = %v", err)
	}
	if after := serviceSandboxMutationUnitFiles(t, service); !reflect.DeepEqual(after, before) {
		t.Fatalf("canceled mutation files = %v, want %v", after, before)
	}
}

func TestServiceSandboxMutationMigrationFailureKeepsExactPreviousRecordAndCleansUnit(t *testing.T) {
	server, service := newServiceSandboxMutationFixture(t, serviceSandboxPolicy{State: "off"})
	before := service.Clone()
	beforeFiles := serviceSandboxMutationUnitFiles(t, service)
	restore := installServiceSandboxMutationTestDeps(t)
	defer restore()
	verifyGeneratedSystemdUnitForSandboxMutation = func(context.Context, string) error { return nil }
	oldMigrate := migrateServiceSandboxGeneration
	t.Cleanup(func() { migrateServiceSandboxGeneration = oldMigrate })
	injected := errors.New("injected journaled failure")
	migrateServiceSandboxGeneration = func(context.Context, *Server, serviceIdentityMigrationRequest, io.Writer) (serviceIdentityMigrationResult, error) {
		return serviceIdentityMigrationResult{}, injected
	}
	err := server.updateServiceSandboxLocked(context.Background(), service.Name, cli.SandboxOptions{State: "off", StateSet: true, ReadOnlySet: true, ReadOnlyReset: true,
		ReadOnly: []cli.SandboxExposure{{Source: "/source", Destination: "/operator"}}}, io.Discard)
	if !errors.Is(err, injected) {
		t.Fatalf("migration error = %v", err)
	}
	got, viewErr := server.serviceView(service.Name)
	if viewErr != nil || !reflect.DeepEqual(got.AsStruct(), before) {
		t.Fatalf("record after failed migration = %#v, %v, want %#v", got.AsStruct(), viewErr, before)
	}
	if after := serviceSandboxMutationUnitFiles(t, service); !reflect.DeepEqual(after, beforeFiles) {
		t.Fatalf("failed migration files = %v, want %v", after, beforeFiles)
	}
}

func TestServiceSandboxMutationCleanupRequiresOwnedStagedUnitProvenance(t *testing.T) {
	for _, stage := range []string{"preflight", "request", "migration"} {
		for _, externalReplacement := range []bool{false, true} {
			name := stage + "/owned"
			if externalReplacement {
				name = stage + "/external replacement"
			}
			t.Run(name, func(t *testing.T) {
				server, service := newServiceSandboxMutationFixture(t, serviceSandboxPolicy{State: "off"})
				before := service.Clone()
				restore := installServiceSandboxMutationTestDeps(t)
				defer restore()
				oldMigrate := migrateServiceSandboxGeneration
				t.Cleanup(func() { migrateServiceSandboxGeneration = oldMigrate })
				injected := errors.New("injected " + stage + " failure")
				var staged string
				const sentinel = "external staged-unit replacement\n"
				replaceStaged := func() {
					t.Helper()
					owned := staged + ".owned"
					if err := os.Rename(staged, owned); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(staged, []byte(sentinel), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				verifyGeneratedSystemdUnitForSandboxMutation = func(_ context.Context, path string) error {
					staged = path
					if stage == "preflight" {
						if externalReplacement {
							replaceStaged()
						}
						return injected
					}
					return nil
				}
				captureServiceSandboxEnablementForMutation = func(context.Context, *db.Service, []string) ([]serviceIdentityUnitEnablement, error) {
					if stage != "request" {
						return []serviceIdentityUnitEnablement{{Unit: "api.service", Enabled: true, TargetEnabled: true}}, nil
					}
					if externalReplacement {
						replaceStaged()
					}
					return nil, injected
				}
				migrateServiceSandboxGeneration = func(_ context.Context, _ *Server, req serviceIdentityMigrationRequest, _ io.Writer) (serviceIdentityMigrationResult, error) {
					if stage != "migration" {
						t.Fatal("unexpected journaled migration")
					}
					staged = req.TargetService.Artifacts[db.ArtifactSystemdUnit].Refs[db.Gen(req.TargetService.Generation)]
					if externalReplacement {
						replaceStaged()
					}
					return serviceIdentityMigrationResult{}, injected
				}
				err := server.updateServiceSandboxLocked(context.Background(), service.Name, cli.SandboxOptions{
					State: "off", StateSet: true, ReadOnlySet: true, ReadOnlyReset: true,
					ReadOnly: []cli.SandboxExposure{{Source: "/source", Destination: "/operator"}},
				}, io.Discard)
				if !errors.Is(err, injected) {
					t.Fatalf("sandbox %s failure = %v", stage, err)
				}
				if externalReplacement {
					if !strings.Contains(err.Error(), "durable provenance") {
						t.Fatalf("sandbox %s cleanup error = %v, want ownership divergence", stage, err)
					}
					raw, readErr := os.ReadFile(staged)
					if readErr != nil || string(raw) != sentinel {
						t.Fatalf("external replacement = %q, %v, want preserved %q", raw, readErr, sentinel)
					}
				} else if _, statErr := os.Stat(staged); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("owned staged unit %q still exists: %v", staged, statErr)
				}
				got, viewErr := server.serviceView(service.Name)
				if viewErr != nil || !reflect.DeepEqual(got.AsStruct(), before) {
					t.Fatalf("sandbox %s failure record = %#v, %v, want %#v", stage, got.AsStruct(), viewErr, before)
				}
			})
		}
	}
}

func TestServiceSandboxMutationCleanupPreservesReplacementImmediatelyAfterQuarantineRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "staged.service")
	if err := os.WriteFile(path, []byte("owned staged unit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	proof, err := captureServiceIdentityPathProof(path)
	if err != nil {
		t.Fatal(err)
	}
	const sentinel = "external replacement after proof\n"
	oldAfterRename := afterServiceSandboxMutationCleanupRename
	t.Cleanup(func() { afterServiceSandboxMutationCleanupRename = oldAfterRename })
	afterServiceSandboxMutationCleanupRename = func(original, quarantine string) {
		if original != path || filepath.Dir(quarantine) == dir {
			t.Fatalf("cleanup rename = %q -> %q, want original and private quarantine", original, quarantine)
		}
		if err := os.WriteFile(original, []byte(sentinel), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cleanupErr := removeServiceSandboxMutationUnit(proof)
	if cleanupErr == nil || !strings.Contains(cleanupErr.Error(), "durable provenance") {
		t.Fatalf("cleanup error = %v, want ownership divergence", cleanupErr)
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil || string(raw) != sentinel {
		t.Fatalf("external replacement was removed: cleanup error=%v read error=%v raw=%q", cleanupErr, readErr, raw)
	}
}

func TestServiceSandboxMutationCleanupQuarantineOutcomes(t *testing.T) {
	for _, scenario := range []string{"owned", "missing", "divergent restored"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "staged.service")
			if err := os.WriteFile(path, []byte("owned staged unit\n"), 0o640); err != nil {
				t.Fatal(err)
			}
			proof, err := captureServiceIdentityPathProof(path)
			if err != nil {
				t.Fatal(err)
			}
			const sentinel = "external replacement before quarantine\n"
			switch scenario {
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "divergent restored":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(sentinel), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			cleanupErr := removeServiceSandboxMutationUnit(proof)
			switch scenario {
			case "owned", "missing":
				if cleanupErr != nil {
					t.Fatalf("%s cleanup error = %v", scenario, cleanupErr)
				}
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("%s cleanup path still exists: %v", scenario, err)
				}
			case "divergent restored":
				if cleanupErr == nil || !strings.Contains(cleanupErr.Error(), "durable provenance") {
					t.Fatalf("divergent cleanup error = %v, want ownership divergence", cleanupErr)
				}
				raw, err := os.ReadFile(path)
				if err != nil || string(raw) != sentinel {
					t.Fatalf("restored divergent sentinel = %q, %v, want %q", raw, err, sentinel)
				}
			}
			if artifacts := serviceSandboxCleanupQuarantineDirs(t, dir); len(artifacts) != 0 {
				t.Fatalf("%s cleanup artifacts = %q, want none", scenario, artifacts)
			}
		})
	}
}

func TestServiceSandboxMutationCleanupPreservesDivergentQuarantineWhenOriginalOccupied(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "staged.service")
	if err := os.WriteFile(path, []byte("owned staged unit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ownedProof, err := captureServiceIdentityPathProof(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	const divergent = "divergent sentinel moved to recovery\n"
	if err := os.WriteFile(path, []byte(divergent), 0o644); err != nil {
		t.Fatal(err)
	}
	const occupant = "external sentinel at original path\n"
	oldAfterRename := afterServiceSandboxMutationCleanupRename
	t.Cleanup(func() { afterServiceSandboxMutationCleanupRename = oldAfterRename })
	afterServiceSandboxMutationCleanupRename = func(original, quarantine string) {
		if original != path || filepath.Dir(quarantine) == dir {
			t.Fatalf("cleanup rename = %q -> %q, want original and private quarantine", original, quarantine)
		}
		if err := os.WriteFile(original, []byte(occupant), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cleanupErr := removeServiceSandboxMutationUnit(ownedProof)
	if cleanupErr == nil || !strings.Contains(cleanupErr.Error(), "durable provenance") {
		t.Fatalf("occupied divergent cleanup error = %v, want ownership divergence", cleanupErr)
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil || string(raw) != occupant {
		t.Fatalf("original external sentinel = %q, %v, want preserved %q", raw, readErr, occupant)
	}
	artifacts := serviceSandboxCleanupQuarantineDirs(t, dir)
	if len(artifacts) != 1 {
		t.Fatalf("divergent recovery artifacts = %q, want one", artifacts)
	}
	recovery := filepath.Join(artifacts[0], filepath.Base(path))
	if !strings.Contains(cleanupErr.Error(), recovery) {
		t.Fatalf("cleanup error = %v, want recovery location %q", cleanupErr, recovery)
	}
	raw, readErr = os.ReadFile(recovery)
	if readErr != nil || string(raw) != divergent {
		t.Fatalf("divergent recovery sentinel = %q, %v, want %q", raw, readErr, divergent)
	}
}

func serviceSandboxCleanupQuarantineDirs(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".staged.service.sandbox-cleanup-*"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func TestServiceSandboxMutationUncommittedFailureJoinsCleanupError(t *testing.T) {
	server, service := newServiceSandboxMutationFixture(t, serviceSandboxPolicy{State: "off"})
	restore := installServiceSandboxMutationTestDeps(t)
	defer restore()
	verifyGeneratedSystemdUnitForSandboxMutation = func(context.Context, string) error { return nil }
	oldMigrate := migrateServiceSandboxGeneration
	oldRemove := removeServiceSandboxMutationUnitForUpdate
	t.Cleanup(func() {
		migrateServiceSandboxGeneration = oldMigrate
		removeServiceSandboxMutationUnitForUpdate = oldRemove
	})
	migrationErr := errors.New("injected uncommitted migration failure")
	cleanupErr := errors.New("injected staged-unit cleanup failure")
	migrateServiceSandboxGeneration = func(context.Context, *Server, serviceIdentityMigrationRequest, io.Writer) (serviceIdentityMigrationResult, error) {
		return serviceIdentityMigrationResult{}, migrationErr
	}
	removeCalls := 0
	removeServiceSandboxMutationUnitForUpdate = func(serviceIdentityPathProof) error {
		removeCalls++
		return cleanupErr
	}
	err := server.updateServiceSandboxLocked(context.Background(), service.Name, cli.SandboxOptions{
		State: "off", StateSet: true, ReadOnlySet: true, ReadOnlyReset: true,
		ReadOnly: []cli.SandboxExposure{{Source: "/source", Destination: "/operator"}},
	}, io.Discard)
	if !errors.Is(err, migrationErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("uncommitted failure = %v, want migration and cleanup errors", err)
	}
	if removeCalls != 1 {
		t.Fatalf("staged-unit cleanup calls = %d, want 1", removeCalls)
	}
}

func TestServiceSandboxMutationPostCommitCleanupFailureKeepsLiveUnit(t *testing.T) {
	server, service := newServiceSandboxMutationFixture(t, serviceSandboxPolicy{State: "off"})
	restore := installServiceSandboxMutationTestDeps(t)
	defer restore()
	verifyGeneratedSystemdUnitForSandboxMutation = func(context.Context, string) error { return nil }
	oldMigrate := migrateServiceSandboxGeneration
	oldRemove := removeServiceSandboxMutationUnitForUpdate
	t.Cleanup(func() {
		migrateServiceSandboxGeneration = oldMigrate
		removeServiceSandboxMutationUnitForUpdate = oldRemove
	})
	injected := errors.New("injected post-commit cleanup failure")
	var staged string
	migrateServiceSandboxGeneration = func(_ context.Context, _ *Server, req serviceIdentityMigrationRequest, _ io.Writer) (serviceIdentityMigrationResult, error) {
		staged = req.TargetService.Artifacts[db.ArtifactSystemdUnit].Refs[db.Gen(req.TargetService.Generation)]
		return serviceIdentityMigrationResult{Committed: true}, injected
	}
	removeServiceSandboxMutationUnitForUpdate = func(serviceIdentityPathProof) error {
		t.Fatal("committed sandbox mutation attempted staged-unit cleanup")
		return nil
	}
	err := server.updateServiceSandboxLocked(context.Background(), service.Name, cli.SandboxOptions{
		State: "off", StateSet: true, ReadOnlySet: true, ReadOnlyReset: true,
		ReadOnly: []cli.SandboxExposure{{Source: "/source", Destination: "/operator"}},
	}, io.Discard)
	if !errors.Is(err, injected) {
		t.Fatalf("post-commit error = %v", err)
	}
	if _, statErr := os.Stat(staged); statErr != nil {
		t.Fatalf("live staged unit %q removed after committed migration: %v", staged, statErr)
	}
}

func TestServiceSandboxMutationJournalRollbackFailureMatrix(t *testing.T) {
	for _, failure := range []string{"unit install", "daemon reload", "enable", "start", "target verification", "rollback restoration"} {
		t.Run(failure, func(t *testing.T) {
			fixture := newServiceSandboxJournalFixture(t, failure)
			_, err := fixture.server.migrateServiceIdentityLocked(context.Background(), fixture.request, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "injected "+failure) {
				t.Fatalf("migration error = %v, want injected %s", err, failure)
			}
			got, viewErr := fixture.server.serviceView(fixture.previous.Name)
			if viewErr != nil || !reflect.DeepEqual(got.AsStruct(), fixture.previous) {
				t.Fatalf("rolled-back record = %#v, %v, want %#v", got.AsStruct(), viewErr, fixture.previous)
			}
			for path, want := range map[string]string{fixture.primary: fixture.oldPrimary, fixture.auxiliary: fixture.oldAuxiliary} {
				raw, readErr := os.ReadFile(path)
				if readErr != nil || string(raw) != want {
					t.Fatalf("rolled-back %s = %q, %v, want %q", path, raw, readErr, want)
				}
			}
			wantRuntime := []serviceIdentityRuntimeUnitState{{Unit: "api.service", Active: true}}
			if !reflect.DeepEqual(fixture.state.runtime, wantRuntime) {
				t.Fatalf("rolled-back runtime = %#v, want %#v", fixture.state.runtime, wantRuntime)
			}
			if !fixture.state.enabled {
				t.Fatal("rolled-back unit enablement is disabled")
			}
			if policy := mustServiceSandboxPolicyForExactGeneration(t, got.AsStruct(), got.Generation()); policy.State != "off" || len(policy.ReadOnly) != 0 {
				t.Fatalf("rolled-back sandbox generation = %#v", policy)
			}
		})
	}
}

func TestServiceSandboxMutationActualTimerRequestPreservesRuntimeOnSuccessAndRollback(t *testing.T) {
	for _, failVerify := range []bool{false, true} {
		name := "success"
		if failVerify {
			name = "rollback"
		}
		t.Run(name, func(t *testing.T) {
			server, service := newServiceSandboxMutationFixture(t, serviceSandboxPolicy{State: "off"})
			timerArtifact := filepath.Join(filepath.Dir(service.Artifacts[db.ArtifactSystemdUnit].Refs[db.Gen(1)]), "api-v1.timer")
			oldTimer := "[Timer]\nOnCalendar=*-*-* 00:00:00\nUnit=api.service\n[Install]\nWantedBy=timers.target\n"
			if err := os.WriteFile(timerArtifact, []byte(oldTimer), 0o644); err != nil {
				t.Fatal(err)
			}
			service.Artifacts[db.ArtifactSystemdTimerFile] = &db.Artifact{Refs: map[db.ArtifactRef]string{
				db.Gen(1): timerArtifact, "latest": timerArtifact,
			}}
			if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{service.Name: service.Clone()}}); err != nil {
				t.Fatal(err)
			}

			oldSystemdDir := systemdSystemDir
			systemdSystemDir = t.TempDir()
			t.Cleanup(func() { systemdSystemDir = oldSystemdDir })
			restore := installServiceSandboxMutationTestDeps(t)
			defer restore()
			verifyGeneratedSystemdUnitForSandboxMutation = func(context.Context, string) error { return nil }
			captureServiceSandboxEnablementForMutation = func(_ context.Context, previous *db.Service, units []string) ([]serviceIdentityUnitEnablement, error) {
				if !serviceIdentityUsesTimer(previous) {
					t.Fatal("timer-backed request lost timer identity")
				}
				states := make([]serviceIdentityUnitEnablement, len(units))
				for index, unit := range units {
					enabled := unit == "api.timer"
					states[index] = serviceIdentityUnitEnablement{Unit: unit, Enabled: enabled, TargetEnabled: enabled}
				}
				return states, nil
			}
			plan, err := server.planServiceSandboxMutation(context.Background(), service.Name, cli.SandboxOptions{
				State: "off", StateSet: true, ReadOnlySet: true, ReadOnlyReset: true,
				ReadOnly: []cli.SandboxExposure{{Source: "/source", Destination: "/operator"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			request, err := serviceSandboxMutationRequest(context.Background(), plan)
			if err != nil {
				t.Fatal(err)
			}
			if request.GenerationEnablement == nil || !serviceSandboxTimerEnabled(*request.GenerationEnablement) {
				t.Fatalf("timer enablement intent = %#v", request.GenerationEnablement)
			}

			stableTimer := filepath.Join(systemdSystemDir, "api.timer")
			stableService := filepath.Join(systemdSystemDir, "api.service")
			oldService, err := os.ReadFile(service.Artifacts[db.ArtifactSystemdUnit].Refs[db.Gen(1)])
			if err != nil {
				t.Fatal(err)
			}
			for path, content := range map[string][]byte{stableTimer: []byte(oldTimer), stableService: oldService} {
				if err := os.WriteFile(path, content, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			previous := service.Clone()
			active := map[string]bool{"api.timer": true, "api.service": false}
			enabled := map[string]bool{"api.timer": true, "api.service": false}
			counter := filepath.Join(t.TempDir(), "payload-counter")
			oldSystemctl, oldActive := catchSystemctl, catchSystemdUnitActive
			catchSystemdUnitActive = func(unit string) bool { return active[unit] }
			catchSystemctl = func(args ...string) error {
				if len(args) != 2 {
					return nil
				}
				unit := args[1]
				switch args[0] {
				case "stop":
					active[unit] = false
				case "start":
					active[unit] = true
					if unit == "api.service" {
						return os.WriteFile(counter, []byte("executed"), 0o600)
					}
				}
				return nil
			}
			t.Cleanup(func() { catchSystemctl, catchSystemdUnitActive = oldSystemctl, oldActive })
			verified := false
			injected := errors.New("injected target verification")
			ops := serviceIdentityMigrationOps{
				unitPath: func(string) string { return stableService },
				snapshot: func(context.Context, *db.Service) (string, error) { return "", nil },
				inspect: func(context.Context, serviceIdentityInspectionRequest) (serviceIdentityInspection, error) {
					return serviceIdentityInspection{}, nil
				},
				apply: func(serviceIdentityInspection, *serviceIdentityJournal) error { return nil }, restore: func(string) error { return nil },
				reload: func(context.Context) error { return nil },
				verify: func(_ context.Context, check serviceIdentityMigrationVerification) error {
					verified = true
					if check.ExpectProcess {
						t.Fatal("timer target verification expected a payload process")
					}
					if failVerify {
						return injected
					}
					return nil
				},
				isEnabled: func(_ context.Context, unit string) (bool, error) { return enabled[unit], nil },
				enable:    func(_ context.Context, unit string) error { enabled[unit] = true; return nil },
				disable:   func(_ context.Context, unit string) error { enabled[unit] = false; return nil },
			}
			request.ops = &ops
			result, err := server.migrateServiceIdentityLocked(context.Background(), request, io.Discard)
			if failVerify {
				if !errors.Is(err, injected) {
					t.Fatalf("rollback error = %v", err)
				}
			} else if err != nil || !result.Committed {
				t.Fatalf("successful timer migration = %#v, %v", result, err)
			}
			if !verified || !active["api.timer"] || active["api.service"] || !enabled["api.timer"] || enabled["api.service"] {
				t.Fatalf("timer state verified=%t active=%#v enabled=%#v", verified, active, enabled)
			}
			if _, err := os.Stat(counter); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("far-future timer payload executed: %v", err)
			}
			got, err := server.serviceView(service.Name)
			if err != nil {
				t.Fatal(err)
			}
			if failVerify {
				if !reflect.DeepEqual(got.AsStruct(), previous) {
					t.Fatalf("rolled-back timer record = %#v, want %#v", got.AsStruct(), previous)
				}
				for path, want := range map[string][]byte{stableTimer: []byte(oldTimer), stableService: oldService} {
					raw, readErr := os.ReadFile(path)
					if readErr != nil || !bytes.Equal(raw, want) {
						t.Fatalf("rolled-back timer path %s = %q, %v, want %q", path, raw, readErr, want)
					}
				}
			} else if got.Generation() != 2 || got.LatestGeneration() != 2 {
				t.Fatalf("successful timer generations = %d/%d, want 2/2", got.Generation(), got.LatestGeneration())
			}
		})
	}
}

func serviceSandboxTimerEnabled(states []serviceIdentityUnitEnablement) bool {
	for _, state := range states {
		if state.Unit == "api.timer" {
			return state.Enabled && state.TargetEnabled
		}
	}
	return false
}

type serviceSandboxJournalFixture struct {
	server                   *Server
	previous                 *db.Service
	request                  serviceIdentityMigrationRequest
	primary, auxiliary       string
	oldPrimary, oldAuxiliary string
	state                    *serviceSandboxJournalState
}

type serviceSandboxJournalState struct {
	runtime []serviceIdentityRuntimeUnitState
	enabled bool
}

func newServiceSandboxJournalFixture(t *testing.T, failure string) *serviceSandboxJournalFixture {
	t.Helper()
	server, service := newServiceSandboxMutationFixture(t, serviceSandboxPolicy{State: "off"})
	root := t.TempDir()
	primary := filepath.Join(root, "systemd", "api.service")
	auxiliary := filepath.Join(root, "systemd", "api.env")
	if err := os.MkdirAll(filepath.Dir(primary), 0o755); err != nil {
		t.Fatal(err)
	}
	oldPrimary := "[Service]\nExecStart=" + service.Artifacts[db.ArtifactBinary].Refs[db.Gen(1)] + "\nWorkingDirectory=" + server.serviceDataDir("api") + "\n"
	newPrimary := oldPrimary + "Environment=SANDBOX=off\n"
	oldAuxiliary, newAuxiliary := "old-env\n", "new-env\n"
	for path, content := range map[string]string{primary: oldPrimary, auxiliary: oldAuxiliary} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	previous := service.Clone()
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{previous.Name: previous.Clone()}}); err != nil {
		t.Fatal(err)
	}
	target := previous.Clone()
	target.Generation, target.LatestGeneration = 2, 2
	target.Sandbox.Refs[db.Gen(2)] = serviceSandboxPolicyToDB(serviceSandboxPolicy{
		State: "off", ReadOnly: []serviceSandboxExposure{{Source: "/source", Destination: "/operator"}},
	})
	target.Sandbox.Refs["latest"] = target.Sandbox.Refs[db.Gen(2)].Clone()
	for _, artifact := range target.Artifacts {
		artifact.Refs[db.Gen(2)] = artifact.Refs[db.Gen(1)]
		artifact.Refs["latest"] = artifact.Refs[db.Gen(2)]
	}
	state := &serviceSandboxJournalState{
		runtime: []serviceIdentityRuntimeUnitState{{Unit: "api.service", Active: true}},
		enabled: true,
	}
	reloadCalls, enableCalls, startCalls, restoreCalls := 0, 0, 0, 0
	injected := func(at string) error {
		if failure == at {
			return errors.New("injected " + at)
		}
		return nil
	}
	ops := serviceIdentityMigrationOps{
		unitPath: func(string) string { return primary },
		isPreviousRunning: func(context.Context, string) (bool, error) {
			return state.runtime[0].Active, nil
		},
		isTargetRunning: func(context.Context, string) (bool, error) {
			return state.runtime[0].Active, nil
		},
		isReplacementRunning: func(context.Context, string) (bool, error) {
			return state.runtime[0].Active, nil
		},
		captureRuntime: func(context.Context, string) ([]serviceIdentityRuntimeUnitState, error) {
			return append([]serviceIdentityRuntimeUnitState(nil), state.runtime...), nil
		},
		restoreRuntime: func(_ context.Context, _ string, desired []serviceIdentityRuntimeUnitState) error {
			restoreCalls++
			state.runtime = append([]serviceIdentityRuntimeUnitState(nil), desired...)
			if failure == "rollback restoration" && restoreCalls == 1 {
				return errors.New("injected rollback restoration")
			}
			return nil
		},
		stop: func(context.Context, string) error {
			state.runtime[0].Active = false
			return nil
		},
		stopReplacement: func(context.Context, string) error { state.runtime[0].Active = false; return nil },
		start: func(context.Context, string) error {
			startCalls++
			if failure == "start" && startCalls == 1 {
				return errors.New("injected start")
			}
			state.runtime[0].Active = true
			return nil
		},
		startPrevious: func(context.Context, string) error { state.runtime[0].Active = true; return nil },
		stopPrevious:  func(context.Context, string) error { state.runtime[0].Active = false; return nil },
		snapshot:      func(context.Context, *db.Service) (string, error) { return "", nil },
		inspect: func(context.Context, serviceIdentityInspectionRequest) (serviceIdentityInspection, error) {
			return serviceIdentityInspection{}, nil
		},
		apply:   func(serviceIdentityInspection, *serviceIdentityJournal) error { return nil },
		restore: func(string) error { return nil },
		reload: func(context.Context) error {
			reloadCalls++
			if failure == "daemon reload" && reloadCalls == 1 {
				return errors.New("injected daemon reload")
			}
			return nil
		},
		verify: func(_ context.Context, check serviceIdentityMigrationVerification) error {
			if check.ExpectProcess != true {
				return fmt.Errorf("ExpectProcess = %t, want true", check.ExpectProcess)
			}
			if failure == "rollback restoration" {
				return errors.New("force rollback")
			}
			return injected("target verification")
		},
		isEnabled: func(context.Context, string) (bool, error) { return state.enabled, nil },
		enable: func(context.Context, string) error {
			enableCalls++
			if failure == "enable" && enableCalls == 1 {
				return errors.New("injected enable")
			}
			state.enabled = true
			return nil
		},
		disable: func(context.Context, string) error { state.enabled = false; return nil },
	}
	desiredAuxiliary := serviceIdentityDesiredFileState(auxiliary, []byte(newAuxiliary), 0o644, uint32(os.Geteuid()), uint32(os.Getegid()))
	enablement := []serviceIdentityUnitEnablement{{Unit: "api.service", Enabled: true, TargetEnabled: true}}
	request := serviceIdentityMigrationRequest{
		Service: previous.Name, Requested: "root:root", Target: effectiveServiceIdentity(previous.View()),
		TargetService: target, ReplacementUnit: newPrimary,
		StageGeneration: func(context.Context) error {
			if err := os.WriteFile(auxiliary, []byte(newAuxiliary), 0o644); err != nil {
				return err
			}
			if failure == "unit install" {
				return errors.New("injected unit install")
			}
			if failure == "enable" {
				state.enabled = false
			}
			return nil
		},
		GenerationPaths: []string{primary, auxiliary}, GenerationIntents: []serviceIdentityPathState{desiredAuxiliary},
		GenerationUnits: []string{"api.service"}, GenerationEnablement: &enablement,
		PreserveTargetServiceIdentity: true, ops: &ops,
	}
	return &serviceSandboxJournalFixture{
		server: server, previous: previous, request: request, primary: primary, auxiliary: auxiliary,
		oldPrimary: oldPrimary, oldAuxiliary: oldAuxiliary, state: state,
	}
}

func TestServiceSandboxMutationBuildsIndependentActiveGeneration(t *testing.T) {
	server, service := newServiceSandboxMutationFixture(t, serviceSandboxPolicy{State: "off"})
	latestUnit := filepath.Join(filepath.Dir(service.Artifacts[db.ArtifactSystemdUnit].Refs[db.Gen(1)]), "api-latest.service")
	latestPayload := filepath.Join(filepath.Dir(service.Artifacts[db.ArtifactBinary].Refs[db.Gen(1)]), "api-latest")
	service.LatestGeneration = 4
	service.Artifacts[db.ArtifactSystemdUnit].Refs[db.Gen(4)] = latestUnit
	service.Artifacts[db.ArtifactSystemdUnit].Refs["latest"] = latestUnit
	service.Artifacts[db.ArtifactSystemdUnit].Refs["staged"] = "/unrelated/staged.service"
	service.Artifacts[db.ArtifactBinary].Refs[db.Gen(4)] = latestPayload
	service.Artifacts[db.ArtifactBinary].Refs["latest"] = latestPayload
	service.Artifacts[db.ArtifactBinary].Refs["staged"] = "/unrelated/staged-payload"
	service.Sandbox.Refs[db.Gen(4)] = serviceSandboxPolicyToDB(serviceSandboxPolicy{State: "on"})
	service.Sandbox.Refs["latest"] = serviceSandboxPolicyToDB(serviceSandboxPolicy{State: "on"})
	service.Sandbox.Refs["staged"] = serviceSandboxPolicyToDB(serviceSandboxPolicy{State: "on", Writable: []serviceSandboxExposure{{Source: "/staged", Destination: "/staged"}}})
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{service.Name: service.Clone()}}); err != nil {
		t.Fatal(err)
	}
	oldVerify := verifyGeneratedSystemdUnitForSandboxMutation
	t.Cleanup(func() { verifyGeneratedSystemdUnitForSandboxMutation = oldVerify })
	verifyGeneratedSystemdUnitForSandboxMutation = func(context.Context, string) error { return nil }

	plan, err := server.planServiceSandboxMutation(context.Background(), service.Name, cli.SandboxOptions{
		State: "off", StateSet: true, ReadOnlySet: true, ReadOnlyReset: true,
		ReadOnly: []cli.SandboxExposure{{Source: "/active", Destination: "/operator"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.target.Generation != 5 || plan.target.LatestGeneration != 5 {
		t.Fatalf("target generations = %d/%d, want 5/5", plan.target.Generation, plan.target.LatestGeneration)
	}
	if got := plan.target.Artifacts[db.ArtifactBinary].Refs[db.Gen(5)]; got != service.Artifacts[db.ArtifactBinary].Refs[db.Gen(1)] {
		t.Fatalf("target payload = %q, want active generation payload", got)
	}
	if got := plan.target.Artifacts[db.ArtifactSystemdUnit].Refs[db.Gen(5)]; got != plan.stagedUnit || got == service.Artifacts[db.ArtifactSystemdUnit].Refs[db.Gen(1)] {
		t.Fatalf("target unit = %q, staged = %q, active = %q", got, plan.stagedUnit, service.Artifacts[db.ArtifactSystemdUnit].Refs[db.Gen(1)])
	}
	if plan.target.Sandbox.Refs[db.Gen(5)] == plan.target.Sandbox.Refs["latest"] ||
		plan.target.Sandbox.Refs[db.Gen(5)] == service.Sandbox.Refs[db.Gen(1)] {
		t.Fatal("target sandbox generation aliases latest or previous policy")
	}
	plan.target.Sandbox.Refs[db.Gen(5)].ReadOnly[0].Destination = "/mutated"
	plan.target.Artifacts[db.ArtifactBinary].Refs[db.Gen(5)] = "/mutated"
	if got := service.Sandbox.Refs[db.Gen(1)].ReadOnly; len(got) != 0 {
		t.Fatalf("previous sandbox changed through target alias: %#v", got)
	}
	if got := service.Artifacts[db.ArtifactBinary].Refs[db.Gen(1)]; got == "/mutated" {
		t.Fatal("previous artifact refs changed through target alias")
	}
}

func TestServiceSandboxMutationCanonicalizesLegacyRuntimePathsBeforeSandboxRender(t *testing.T) {
	server, service := newServiceSandboxMutationFixture(t, serviceSandboxPolicy{State: "legacy"})
	root := server.defaultServiceRootDir(service.Name)
	runDir := serviceRunDirForRoot(root)
	payload := filepath.Join(serviceBinDirForRoot(root), "api-v1")
	if err := os.MkdirAll(filepath.Dir(payload), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := filepath.Join(root, "env", "env-v1")
	if err := os.MkdirAll(filepath.Dir(env), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env, []byte("KEY=value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unit := service.Artifacts[db.ArtifactSystemdUnit].Refs[db.Gen(service.Generation)]
	legacyPayload := filepath.Join(runDir, service.Name)
	legacyEnv := filepath.Join(runDir, "env")
	raw := "[Unit]\nConditionFileIsExecutable=" + legacyPayload + "\n" +
		"[Service]\nExecStart=" + legacyPayload + " --serve\n" +
		"WorkingDirectory=" + serviceDataDirForRoot(root) + "\n" +
		"EnvironmentFile=" + legacyEnv + "\nUser=legacy\nGroup=legacy\n"
	if err := os.WriteFile(unit, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	service.Artifacts[db.ArtifactBinary].Refs[db.Gen(service.Generation)] = payload
	service.Artifacts[db.ArtifactBinary].Refs["latest"] = payload
	service.Artifacts[db.ArtifactEnvFile] = &db.Artifact{Refs: map[db.ArtifactRef]string{
		db.Gen(service.Generation): env,
		"latest":                   env,
	}}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{service.Name: service.Clone()}}); err != nil {
		t.Fatal(err)
	}

	restore := installServiceSandboxMutationTestDeps(t)
	defer restore()
	ensureBubblewrapForServiceSandboxMutation = func(context.Context) error { return nil }
	probeServiceSandboxForMutation = func(context.Context, serviceSandboxPlan, uint32, uint32) error { return nil }
	verifyGeneratedSystemdUnitForSandboxMutation = func(context.Context, string) error { return nil }

	plan, err := server.planServiceSandboxMutation(context.Background(), service.Name, cli.SandboxOptions{State: "on", StateSet: true})
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := os.ReadFile(plan.stagedUnit)
	if err != nil {
		t.Fatal(err)
	}
	installedEnv := filepath.Join(root, "env", "env")
	for _, want := range []string{bubblewrapPath, payload, "EnvironmentFile=" + installedEnv, "User=root", "Group=root"} {
		if !strings.Contains(string(rendered), want) {
			t.Fatalf("rendered sandbox unit missing %q:\n%s", want, rendered)
		}
	}
	for _, stale := range []string{
		"ConditionFileIsExecutable=" + legacyPayload,
		"ExecStart=" + legacyPayload,
		"EnvironmentFile=" + legacyEnv,
		"User=legacy",
		"Group=legacy",
	} {
		if strings.Contains(string(rendered), stale) {
			t.Fatalf("rendered sandbox unit retains legacy directive %q:\n%s", stale, rendered)
		}
	}
}

func TestServiceSandboxMutationPreflightOrderCleanupAndCancellation(t *testing.T) {
	t.Run("ordered success", func(t *testing.T) {
		server, service := newServiceSandboxMutationFixture(t, serviceSandboxPolicy{State: "off"})
		var order []string
		restore := installServiceSandboxMutationTestDeps(t)
		defer restore()
		ensureBubblewrapForServiceSandboxMutation = func(context.Context) error { order = append(order, "ensure"); return nil }
		validateServiceSandboxPolicyForMutation = func(req serviceSandboxPlanRequest, active bool) (serviceSandboxPolicy, error) {
			order = append(order, "validate")
			return sandboxMutationTestValidate(req, active)
		}
		renderNativeSandboxUnitForServiceSandboxMutation = func(raw string, req nativeSandboxUnitRequest, plan *serviceSandboxPlan) (string, *serviceSandboxPlan, error) {
			order = append(order, "render")
			return renderNativeSandboxUnitWithPlan(raw, req, plan)
		}
		probeServiceSandboxForMutation = func(context.Context, serviceSandboxPlan, uint32, uint32) error {
			order = append(order, "probe")
			return nil
		}
		verifyGeneratedSystemdUnitForSandboxMutation = func(context.Context, string) error { order = append(order, "verify"); return nil }

		plan, err := server.planServiceSandboxMutation(context.Background(), service.Name, cli.SandboxOptions{State: "on", StateSet: true})
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"ensure", "validate", "render", "probe", "verify"}; !reflect.DeepEqual(order, want) {
			t.Fatalf("preflight order = %v, want %v", order, want)
		}
		if plan.stagedUnit == "" {
			t.Fatal("successful preflight has no staged versioned unit")
		}
	})

	t.Run("cancel after dependency", func(t *testing.T) {
		server, service := newServiceSandboxMutationFixture(t, serviceSandboxPolicy{State: "off"})
		before := serviceSandboxMutationUnitFiles(t, service)
		ctx, cancel := context.WithCancel(context.Background())
		restore := installServiceSandboxMutationTestDeps(t)
		defer restore()
		var order []string
		ensureBubblewrapForServiceSandboxMutation = func(context.Context) error {
			order = append(order, "ensure")
			cancel()
			return nil
		}
		validateServiceSandboxPolicyForMutation = func(req serviceSandboxPlanRequest, active bool) (serviceSandboxPolicy, error) {
			order = append(order, "validate")
			return sandboxMutationTestValidate(req, active)
		}
		_, err := server.planServiceSandboxMutation(ctx, service.Name, cli.SandboxOptions{State: "on", StateSet: true})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled preflight error = %v", err)
		}
		if want := []string{"ensure"}; !reflect.DeepEqual(order, want) {
			t.Fatalf("canceled preflight order = %v, want %v", order, want)
		}
		if after := serviceSandboxMutationUnitFiles(t, service); !reflect.DeepEqual(after, before) {
			t.Fatalf("canceled preflight files = %v, want %v", after, before)
		}
	})

	for _, failure := range []string{"ensure", "validate", "probe", "verify"} {
		t.Run("cleanup "+failure, func(t *testing.T) {
			server, service := newServiceSandboxMutationFixture(t, serviceSandboxPolicy{State: "off"})
			before := serviceSandboxMutationUnitFiles(t, service)
			restore := installServiceSandboxMutationTestDeps(t)
			defer restore()
			injected := errors.New("injected " + failure)
			ensureBubblewrapForServiceSandboxMutation = func(context.Context) error {
				if failure == "ensure" {
					return injected
				}
				return nil
			}
			validateServiceSandboxPolicyForMutation = func(req serviceSandboxPlanRequest, active bool) (serviceSandboxPolicy, error) {
				if failure == "validate" {
					return serviceSandboxPolicy{}, injected
				}
				return sandboxMutationTestValidate(req, active)
			}
			probeServiceSandboxForMutation = func(context.Context, serviceSandboxPlan, uint32, uint32) error {
				if failure == "probe" {
					return injected
				}
				return nil
			}
			verifyGeneratedSystemdUnitForSandboxMutation = func(context.Context, string) error {
				if failure == "verify" {
					return injected
				}
				return nil
			}
			_, err := server.planServiceSandboxMutation(context.Background(), service.Name, cli.SandboxOptions{State: "on", StateSet: true})
			if !errors.Is(err, injected) {
				t.Fatalf("preflight error = %v, want %v", err, injected)
			}
			if after := serviceSandboxMutationUnitFiles(t, service); !reflect.DeepEqual(after, before) {
				t.Fatalf("failed preflight files = %v, want %v", after, before)
			}
		})
	}
}

func installServiceSandboxMutationTestDeps(t *testing.T) func() {
	t.Helper()
	oldEnsure, oldValidate := ensureBubblewrapForServiceSandboxMutation, validateServiceSandboxPolicyForMutation
	oldProbe, oldVerify := probeServiceSandboxForMutation, verifyGeneratedSystemdUnitForSandboxMutation
	oldRender := renderNativeSandboxUnitForServiceSandboxMutation
	oldEnablement := captureServiceSandboxEnablementForMutation
	captureServiceSandboxEnablementForMutation = func(_ context.Context, previous *db.Service, units []string) ([]serviceIdentityUnitEnablement, error) {
		plan := serviceIdentityGenerationUnitPlan(previous, previous.Name, units)
		states := make([]serviceIdentityUnitEnablement, len(plan))
		for index, unit := range plan {
			states[index] = serviceIdentityUnitEnablement{Unit: unit, Enabled: true, TargetEnabled: true}
		}
		return states, nil
	}
	return func() {
		ensureBubblewrapForServiceSandboxMutation = oldEnsure
		validateServiceSandboxPolicyForMutation = oldValidate
		probeServiceSandboxForMutation = oldProbe
		verifyGeneratedSystemdUnitForSandboxMutation = oldVerify
		renderNativeSandboxUnitForServiceSandboxMutation = oldRender
		captureServiceSandboxEnablementForMutation = oldEnablement
	}
}

func sandboxMutationTestValidate(req serviceSandboxPlanRequest, active bool) (serviceSandboxPolicy, error) {
	deps := defaultServiceSandboxValidationDeps()
	deps.checkAccess = func(string, []string, uint32, uint32) error { return nil }
	return validateServiceSandboxPolicyWith(req, active, deps)
}

func serviceSandboxMutationUnitFiles(t *testing.T, service *db.Service) []string {
	t.Helper()
	unit := service.Artifacts[db.ArtifactSystemdUnit].Refs[db.Gen(service.Generation)]
	files, err := filepath.Glob(filepath.Join(filepath.Dir(unit), "*.service"))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestServiceSandboxMutationDormantAndStateTransitions(t *testing.T) {
	source := t.TempDir()
	tests := []struct {
		name       string
		current    serviceSandboxPolicy
		options    cli.SandboxOptions
		want       serviceSandboxPolicy
		wantEnsure int
		wantProbe  int
	}{
		{
			name:    "off to off dormant edit",
			current: serviceSandboxPolicy{State: "off"},
			options: cli.SandboxOptions{State: "off", StateSet: true, ReadOnlySet: true, ReadOnlyReset: true,
				ReadOnly: []cli.SandboxExposure{{Source: source, Destination: "/operator"}}},
			want: serviceSandboxPolicy{State: "off", ReadOnly: []serviceSandboxExposure{{Source: source, Destination: "/operator"}}},
		},
		{
			name:       "off to on",
			current:    serviceSandboxPolicy{State: "off", ReadOnly: []serviceSandboxExposure{{Source: source, Destination: "/operator"}}},
			options:    cli.SandboxOptions{State: "on", StateSet: true},
			want:       serviceSandboxPolicy{State: "on", ReadOnly: []serviceSandboxExposure{{Source: source, Destination: "/operator"}}},
			wantEnsure: 1,
			wantProbe:  1,
		},
		{
			name:    "on to off preserves dormant lists",
			current: serviceSandboxPolicy{State: "on", Writable: []serviceSandboxExposure{{Source: source, Destination: "/operator"}}},
			options: cli.SandboxOptions{State: "off", StateSet: true},
			want:    serviceSandboxPolicy{State: "off", Writable: []serviceSandboxExposure{{Source: source, Destination: "/operator"}}},
		},
		{
			name:    "legacy to off",
			current: serviceSandboxPolicy{State: "legacy"},
			options: cli.SandboxOptions{State: "off", StateSet: true},
			want:    serviceSandboxPolicy{State: "off"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, service := newServiceSandboxMutationFixture(t, tt.current)
			oldEnsure, oldValidate := ensureBubblewrapForServiceSandboxMutation, validateServiceSandboxPolicyForMutation
			oldProbe, oldVerify := probeServiceSandboxForMutation, verifyGeneratedSystemdUnitForSandboxMutation
			t.Cleanup(func() {
				ensureBubblewrapForServiceSandboxMutation = oldEnsure
				validateServiceSandboxPolicyForMutation = oldValidate
				probeServiceSandboxForMutation = oldProbe
				verifyGeneratedSystemdUnitForSandboxMutation = oldVerify
			})
			ensureCalls := 0
			ensureBubblewrapForServiceSandboxMutation = func(context.Context) error { ensureCalls++; return nil }
			validateServiceSandboxPolicyForMutation = func(req serviceSandboxPlanRequest, active bool) (serviceSandboxPolicy, error) {
				deps := defaultServiceSandboxValidationDeps()
				deps.checkAccess = func(string, []string, uint32, uint32) error { return nil }
				return validateServiceSandboxPolicyWith(req, active, deps)
			}
			probeCalls := 0
			probeServiceSandboxForMutation = func(_ context.Context, plan serviceSandboxPlan, uid, gid uint32) error {
				probeCalls++
				if uid != 0 || gid != 0 || len(plan.Arguments) == 0 || plan.Arguments[len(plan.Arguments)-1] != "--" {
					t.Fatalf("probe inputs = UID %d GID %d argv %#v", uid, gid, plan.Arguments)
				}
				return nil
			}
			verifyGeneratedSystemdUnitForSandboxMutation = func(context.Context, string) error { return nil }

			plan, err := server.planServiceSandboxMutation(context.Background(), service.Name, tt.options)
			if err != nil {
				t.Fatalf("planServiceSandboxMutation: %v", err)
			}
			if plan.noOp || plan.target.Generation != 2 || plan.target.LatestGeneration != 2 {
				t.Fatalf("plan generations = active %d latest %d no-op %t, want 2/2/false", plan.target.Generation, plan.target.LatestGeneration, plan.noOp)
			}
			got := mustServiceSandboxPolicyForExactGeneration(t, plan.target, 2)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("target policy = %#v, want %#v", got, tt.want)
			}
			if ensureCalls != tt.wantEnsure {
				t.Fatalf("EnsureBubblewrap calls = %d, want %d", ensureCalls, tt.wantEnsure)
			}
			if probeCalls != tt.wantProbe {
				t.Fatalf("probe calls = %d, want %d", probeCalls, tt.wantProbe)
			}
			previousPolicy := mustServiceSandboxPolicyForExactGeneration(t, plan.previous, 1)
			if !reflect.DeepEqual(previousPolicy, tt.current) {
				t.Fatalf("previous policy changed to %#v", previousPolicy)
			}
		})
	}
}

func TestServiceSandboxMutationGuidanceReplaysThroughRealParserAndPatch(t *testing.T) {
	current := serviceSandboxPolicy{State: "off", ReadOnly: []serviceSandboxExposure{
		{Source: "/srv/read one", Destination: "/read one"},
		{Source: "/srv/read two", Destination: "/read two"},
	}, Writable: []serviceSandboxExposure{
		{Source: "/srv/write one", Destination: "/write one"},
		{Source: "/srv/write two", Destination: "/write two"},
	}}
	requested := cli.SandboxOptions{
		State: "off", StateSet: true,
		ReadOnlySet: true, ReadOnly: []cli.SandboxExposure{{Source: "/srv/read one", Destination: "/read one"}},
		WritableSet: true, Writable: []cli.SandboxExposure{{Source: "/srv/write one", Destination: "/write one"}},
	}
	_, err := applyServiceSandboxPolicyPatch("api", current, false, requested)
	if err == nil {
		t.Fatal("omitting an existing exposure succeeded")
	}
	lines := strings.Split(err.Error(), "\n")
	if len(lines) != 4 {
		t.Fatalf("guidance lines = %q", lines)
	}
	for index, command := range []string{lines[1], lines[3]} {
		flags, rest := replayServiceSandboxMutationGuidance(t, command)
		got, patchErr := applyServiceSandboxPolicyPatch("api", current, false, flags.Sandbox)
		if patchErr != nil {
			t.Fatalf("replay guidance %q: %v", command, patchErr)
		}
		wantCount := 2 - index
		if len(got.ReadOnly) != wantCount || len(got.Writable) != wantCount {
			t.Fatalf("replayed guidance %q produced %#v, want %d entries in both classes", command, got, wantCount)
		}
		if len(rest) != 1 || rest[0] != "api" {
			t.Fatalf("replayed guidance %q positional args = %q", command, rest)
		}
		if index == 1 && (!flags.Sandbox.ReadOnlyReset || !flags.Sandbox.WritableReset) {
			t.Fatalf("replacement guidance resets = ro:%t rw:%t, want both", flags.Sandbox.ReadOnlyReset, flags.Sandbox.WritableReset)
		}
	}
}

func replayServiceSandboxMutationGuidance(t *testing.T, command string) (cli.ServiceSetFlags, []string) {
	t.Helper()
	script := "set -f\nyeet() { printf '%s\\000' \"$@\"; }\n" + command + "\n"
	process := exec.Command("/bin/sh", "-c", script) //nolint:gosec // Fixed shell harness validates generated recovery commands.
	process.Env = []string{"PATH=/usr/bin:/bin"}
	output, err := process.Output()
	if err != nil {
		t.Fatalf("execute guidance command %q: %v", command, err)
	}
	raw := bytes.Split(output, []byte{0})
	if len(raw) > 0 && len(raw[len(raw)-1]) == 0 {
		raw = raw[:len(raw)-1]
	}
	arguments := make([]string, len(raw))
	for index := range raw {
		arguments[index] = string(raw[index])
	}
	if len(arguments) < 3 || arguments[0] != "service" || arguments[1] != "set" {
		t.Fatalf("guidance command %q produced argv %q", command, arguments)
	}
	flags, rest, err := cli.ParseServiceSet(arguments[2:])
	if err != nil {
		t.Fatalf("ParseServiceSet(%q): %v", arguments, err)
	}
	return flags, rest
}

func TestServiceSandboxMutationNoOpAndPatchGuardPrecedeDependencyWork(t *testing.T) {
	server, service := newServiceSandboxMutationFixture(t, serviceSandboxPolicy{
		State: "off",
		ReadOnly: []serviceSandboxExposure{
			{Source: "/srv/one", Destination: "/one"},
			{Source: "/srv/two", Destination: "/two"},
		},
	})
	oldEnsure := ensureBubblewrapForServiceSandboxMutation
	t.Cleanup(func() { ensureBubblewrapForServiceSandboxMutation = oldEnsure })
	ensureCalls := 0
	ensureBubblewrapForServiceSandboxMutation = func(context.Context) error { ensureCalls++; return nil }

	noOp, err := server.planServiceSandboxMutation(context.Background(), service.Name, cli.SandboxOptions{State: "off", StateSet: true})
	if err != nil {
		t.Fatalf("no-op plan: %v", err)
	}
	if !noOp.noOp || ensureCalls != 0 {
		t.Fatalf("no-op plan = %#v, ensure calls = %d", noOp, ensureCalls)
	}

	_, err = server.planServiceSandboxMutation(context.Background(), service.Name, cli.SandboxOptions{
		ReadOnly: []cli.SandboxExposure{{Source: "/srv/one", Destination: "/one"}}, ReadOnlySet: true,
	})
	if err == nil || !strings.Contains(err.Error(), "preserve them with:") || !strings.Contains(err.Error(), "replace them with:") {
		t.Fatalf("omission error = %v, want preservation and replacement guidance", err)
	}
	if ensureCalls != 0 {
		t.Fatalf("omission guard made %d Bubblewrap ensure calls", ensureCalls)
	}

	legacyServer, legacy := newServiceSandboxMutationFixture(t, serviceSandboxPolicy{State: "legacy"})
	_, err = legacyServer.planServiceSandboxMutation(context.Background(), legacy.Name, cli.SandboxOptions{
		ReadOnlySet: true, ReadOnly: []cli.SandboxExposure{{Source: "/srv/one", Destination: "/one"}},
	})
	if err == nil || !strings.Contains(err.Error(), "requires explicit --sandbox=on or --sandbox=off") {
		t.Fatalf("legacy exposure-only error = %v", err)
	}
	if ensureCalls != 0 {
		t.Fatalf("legacy exposure-only guard made %d Bubblewrap ensure calls", ensureCalls)
	}
}

func TestServiceSandboxMutationNoOpHasNoObservableEffects(t *testing.T) {
	tests := []struct {
		name    string
		policy  serviceSandboxPolicy
		options cli.SandboxOptions
	}{
		{name: "off", policy: serviceSandboxPolicy{State: "off"}, options: cli.SandboxOptions{State: "off", StateSet: true}},
		{name: "on", policy: serviceSandboxPolicy{State: "on"}, options: cli.SandboxOptions{State: "on", StateSet: true}},
		{
			name: "lists", policy: serviceSandboxPolicy{State: "on", ReadOnly: []serviceSandboxExposure{{Source: "/srv/data", Destination: "/data"}}},
			options: cli.SandboxOptions{ReadOnlySet: true, ReadOnly: []cli.SandboxExposure{{Source: "/srv/data", Destination: "/data"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, service := newServiceSandboxMutationFixture(t, tt.policy)
			before := service.Clone()
			beforeFiles := serviceSandboxMutationUnitFiles(t, service)
			restore := installServiceSandboxMutationTestDeps(t)
			defer restore()
			ensureBubblewrapForServiceSandboxMutation = func(context.Context) error {
				t.Fatal("semantic no-op reached dependency work")
				return nil
			}
			oldMigrate := migrateServiceSandboxGeneration
			t.Cleanup(func() { migrateServiceSandboxGeneration = oldMigrate })
			migrateServiceSandboxGeneration = func(context.Context, *Server, serviceIdentityMigrationRequest, io.Writer) (serviceIdentityMigrationResult, error) {
				t.Fatal("semantic no-op reached journaled migration")
				return serviceIdentityMigrationResult{}, nil
			}
			var out strings.Builder
			if err := server.updateServiceSandboxLocked(context.Background(), service.Name, tt.options, &out); err != nil {
				t.Fatal(err)
			}
			got, err := server.serviceView(service.Name)
			if err != nil || !reflect.DeepEqual(got.AsStruct(), before) {
				t.Fatalf("no-op record = %#v, %v, want %#v", got.AsStruct(), err, before)
			}
			if files := serviceSandboxMutationUnitFiles(t, service); !reflect.DeepEqual(files, beforeFiles) {
				t.Fatalf("no-op unit files = %v, want %v", files, beforeFiles)
			}
			if out.Len() != 0 {
				t.Fatalf("no-op output = %q, want empty", out.String())
			}
		})
	}
}

func newServiceSandboxMutationFixture(t *testing.T, policy serviceSandboxPolicy) (*Server, *db.Service) {
	t.Helper()
	server := newTestServer(t)
	root := server.defaultServiceRootDir("api")
	dataDir := serviceDataDirForRoot(root)
	binDir := serviceBinDirForRoot(root)
	for _, dir := range []string{dataDir, serviceRunDirForRoot(root)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	payload := filepath.Join(binDir, "api-v1")
	if err := os.MkdirAll(filepath.Dir(payload), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(binDir, "api-v1.service")
	execStart := payload
	workingDirectory := dataDir
	if policy.State == "on" {
		execStart = bubblewrapPath + " -- " + payload
		workingDirectory = "/"
	}
	if err := os.WriteFile(unit, []byte("[Service]\nExecStart="+execStart+"\nWorkingDirectory="+workingDirectory+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := &db.Service{
		Name: "api", ServiceType: db.ServiceTypeSystemd, Generation: 1, LatestGeneration: 1,
		Identity: &db.ServiceIdentity{RequestedUser: "root", RequestedGroup: "root"},
		Artifacts: db.ArtifactStore{
			db.ArtifactBinary:      {Refs: map[db.ArtifactRef]string{db.Gen(1): payload, "latest": payload}},
			db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{db.Gen(1): unit, "latest": unit}},
		},
	}
	if policy.State != "legacy" {
		service.Sandbox = &db.ServiceSandboxStore{Refs: map[db.ArtifactRef]*db.ServiceSandboxPolicy{
			db.Gen(1): serviceSandboxPolicyToDB(policy), "latest": serviceSandboxPolicyToDB(policy),
		}}
	}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{service.Name: service.Clone()}}); err != nil {
		t.Fatal(err)
	}
	return server, service
}
