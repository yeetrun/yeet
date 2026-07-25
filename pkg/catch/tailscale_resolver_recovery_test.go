// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestRecoverTailscaleResolverIsolationFromEveryNonTerminalPhase(t *testing.T) {
	for _, phase := range []string{
		tailscaleResolverPhasePrepared,
		tailscaleResolverPhaseFilesWritten,
		tailscaleResolverPhaseDaemonReloaded,
		tailscaleResolverPhaseServicesVerified,
	} {
		t.Run(phase, func(t *testing.T) {
			fixture := newTailscaleResolverFleetTransactionFixture(t)
			restore := stubTailscaleResolverFleetLifecycle(t, fixture.active)
			defer restore()
			createTailscaleResolverRecoveryJournal(t, fixture, phase)

			recovered := NewUnstartedServer(&fixture.server.cfg)
			if err := recovered.checkTailscaleResolverMutationAllowed(); err != nil {
				t.Fatalf("startup recovery mutation block = %v", err)
			}
			fixture.server = recovered
			fixture.assertOriginalFleet(t)
			if err := recovered.recoverTailscaleResolverIsolation(context.Background()); err != nil {
				t.Fatalf("idempotent second recovery: %v", err)
			}
			fixture.assertOriginalFleet(t)
		})
	}
}

func TestRecoverTailscaleResolverIsolationRemovesCommittedJournal(t *testing.T) {
	fixture := newTailscaleResolverFleetTransactionFixture(t)
	restore := stubTailscaleResolverFleetLifecycle(t, fixture.active)
	defer restore()
	createTailscaleResolverRecoveryJournal(t, fixture, tailscaleResolverPhaseCommitted)

	if err := fixture.server.recoverTailscaleResolverIsolation(context.Background()); err != nil {
		t.Fatalf("recoverTailscaleResolverIsolation: %v", err)
	}
	if journals := tailscaleResolverJournalPaths(t, fixture.server); len(journals) != 0 {
		t.Fatalf("committed recovery retained journals: %q", journals)
	}
	for _, service := range fixture.plan.Services {
		for _, file := range service.Files {
			raw, err := os.ReadFile(file.Path)
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) != string(file.Next) {
				t.Fatalf("committed recovery rolled back %s", file.Path)
			}
		}
	}
}

func TestRecoverTailscaleResolverIsolationRejectsCorruptDuplicateOrSymlinkJournal(t *testing.T) {
	tests := []struct {
		name  string
		write func(*testing.T, *tailscaleResolverFleetTransactionFixture, string)
	}{
		{
			name: "corrupt",
			write: func(t *testing.T, _ *tailscaleResolverFleetTransactionFixture, dir string) {
				t.Helper()
				writePrivateTailscaleResolverJournal(t, filepath.Join(dir, "corrupt.jsonl"), []byte("{not-json}\n"))
			},
		},
		{
			name: "duplicate managed path",
			write: func(t *testing.T, fixture *tailscaleResolverFleetTransactionFixture, dir string) {
				t.Helper()
				header := mustNewTailscaleResolverJournalHeader(t, fixture.plan)
				header.ID = "duplicate"
				header.Files = append(header.Files, header.Files[0])
				raw, err := json.Marshal(header)
				if err != nil {
					t.Fatal(err)
				}
				writePrivateTailscaleResolverJournal(
					t,
					filepath.Join(dir, header.ID+".jsonl"),
					append(raw, '\n'),
				)
			},
		},
		{
			name: "symlink",
			write: func(t *testing.T, _ *tailscaleResolverFleetTransactionFixture, dir string) {
				t.Helper()
				target := filepath.Join(t.TempDir(), "target.jsonl")
				writePrivateTailscaleResolverJournal(t, target, []byte("{}\n"))
				if err := os.Symlink(target, filepath.Join(dir, "linked.jsonl")); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newTailscaleResolverFleetTransactionFixture(t)
			dir, err := ensureTailscaleResolverJournalDir(fixture.server.cfg.RootDir)
			if err != nil {
				t.Fatal(err)
			}
			tt.write(t, fixture, dir)

			err = fixture.server.recoverTailscaleResolverIsolation(context.Background())
			if err == nil || !errors.Is(err, errTailscaleResolverRecoveryBlocked) {
				t.Fatalf("recoverTailscaleResolverIsolation error = %v, want recovery block", err)
			}
			if allowed := fixture.server.checkTailscaleResolverMutationAllowed(); !errors.Is(allowed, errTailscaleResolverRecoveryBlocked) {
				t.Fatalf("mutation block = %v, want resolver recovery block", allowed)
			}
		})
	}
}

func TestRecoverTailscaleResolverIsolationBlocksMutationsWhenRollbackCannotBeProven(t *testing.T) {
	fixture := newTailscaleResolverFleetTransactionFixture(t)
	restore := stubTailscaleResolverFleetLifecycle(t, fixture.active)
	defer restore()
	createTailscaleResolverRecoveryJournal(t, fixture, tailscaleResolverPhasePrepared)
	path := fixture.plan.Services[0].Files[0].Path
	if err := os.WriteFile(path, []byte("operator replacement\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	err := fixture.server.recoverTailscaleResolverIsolation(context.Background())
	if err == nil || !errors.Is(err, errTailscaleResolverRecoveryBlocked) {
		t.Fatalf("recoverTailscaleResolverIsolation error = %v, want recovery block", err)
	}
	if allowed := fixture.server.authorizeCaller(context.Background(), "test", permissionManage); !errors.Is(allowed, errTailscaleResolverRecoveryBlocked) {
		t.Fatalf("central manage boundary = %v, want resolver recovery block", allowed)
	}
	if journals := tailscaleResolverJournalPaths(t, fixture.server); len(journals) != 1 {
		t.Fatalf("unproven recovery journals = %q, want one retained", journals)
	}
}

func TestRecoverTailscaleResolverIsolationStopsUnprovenReplacementSidecars(t *testing.T) {
	fixture := newTailscaleResolverFleetTransactionFixture(t)
	for unit := range fixture.active {
		fixture.active[unit] = true
	}
	restore := stubTailscaleResolverFleetLifecycle(t, fixture.active)
	defer restore()
	createTailscaleResolverRecoveryJournal(t, fixture, tailscaleResolverPhaseDaemonReloaded)
	path := fixture.plan.Services[1].Files[1].Path
	if err := os.WriteFile(path, []byte("unproven replacement\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := fixture.server.recoverTailscaleResolverIsolation(context.Background()); err == nil {
		t.Fatal("recoverTailscaleResolverIsolation succeeded with an unproven replacement")
	}
	for _, service := range fixture.plan.Services {
		if fixture.active[service.UnitName] {
			t.Fatalf("unproven replacement sidecar %s remained active", service.UnitName)
		}
	}
}

func TestRecoverTailscaleResolverIsolationAllowsReadAndExplicitRecoveryWhileBlocked(t *testing.T) {
	fixture := newTailscaleResolverFleetTransactionFixture(t)
	restore := stubTailscaleResolverFleetLifecycle(t, fixture.active)
	defer restore()
	createTailscaleResolverRecoveryJournal(t, fixture, tailscaleResolverPhaseFilesWritten)
	file := fixture.plan.Services[2].Files[0]
	if err := os.WriteFile(file.Path, []byte("unknown\n"), file.Proof.Mode.Perm()); err != nil {
		t.Fatal(err)
	}
	if err := fixture.server.recoverTailscaleResolverIsolation(context.Background()); err == nil {
		t.Fatal("initial recovery unexpectedly succeeded")
	}
	if err := fixture.server.authorizeCaller(context.Background(), "test", permissionRead); err != nil {
		t.Fatalf("read authorization while blocked: %v", err)
	}
	if err := fixture.server.authorizeCaller(context.Background(), "test", permissionManage); !errors.Is(err, errTailscaleResolverRecoveryBlocked) {
		t.Fatalf("manage authorization while blocked = %v", err)
	}
	if err := fixture.server.checkServiceIdentityMutationAllowed("alpha"); !errors.Is(err, errTailscaleResolverRecoveryBlocked) {
		t.Fatalf("per-service internal mutation boundary = %v, want resolver recovery block", err)
	}
	if err := fixture.server.checkServiceIdentityMutationsAllowed([]string{"alpha", "bravo"}); !errors.Is(err, errTailscaleResolverRecoveryBlocked) {
		t.Fatalf("multi-service internal mutation boundary = %v, want resolver recovery block", err)
	}
	for name, mutate := range map[string]func() error{
		"network runtime": func() error {
			return fixture.server.prepareNetworkRuntime(context.Background())
		},
		"tailscale DNS reconciliation": func() error {
			return fixture.server.reconcileTailscaleDNSConfigs(context.Background())
		},
		"docker netns reconciliation": func() error {
			return fixture.server.reconcileNetNSBackedDockerServices(context.Background())
		},
		"tailscale mount reconciliation": func() error {
			return fixture.server.reconcileTailscaleResolverMounts(context.Background())
		},
		"tailscale resolver reconciliation": func() error {
			return fixture.server.reconcileTailscaleResolverIsolation(context.Background())
		},
	} {
		if err := mutate(); !errors.Is(err, errTailscaleResolverRecoveryBlocked) {
			t.Fatalf("%s while blocked = %v, want resolver recovery block", name, err)
		}
	}

	if err := os.WriteFile(file.Path, file.Original, file.Proof.Mode.Perm()); err != nil {
		t.Fatal(err)
	}
	if err := fixture.server.recoverTailscaleResolverIsolation(context.Background()); err != nil {
		t.Fatalf("explicit recovery while blocked: %v", err)
	}
	if err := fixture.server.checkTailscaleResolverMutationAllowed(); err != nil {
		t.Fatalf("mutation block after proven explicit recovery: %v", err)
	}
	fixture.assertOriginalFleet(t)
}

func TestRecoverTailscaleResolverIsolationStopsUnionForMultipleJournalsAndRetainsAll(t *testing.T) {
	fixture := newTailscaleResolverFleetTransactionFixture(t)
	for unit := range fixture.active {
		fixture.active[unit] = true
	}
	restore := stubTailscaleResolverFleetLifecycle(t, fixture.active)
	defer restore()
	for _, id := range []string{"fleet-a", "fleet-b"} {
		header := mustNewTailscaleResolverJournalHeader(t, fixture.plan)
		header.ID = id
		journal, err := createTailscaleResolverJournal(fixture.server.cfg.RootDir, header)
		if err != nil {
			t.Fatal(err)
		}
		if err := journal.Close(); err != nil {
			t.Fatal(err)
		}
	}

	err := fixture.server.recoverTailscaleResolverIsolation(context.Background())
	if !errors.Is(err, errTailscaleResolverRecoveryBlocked) {
		t.Fatalf("multi-journal recovery error = %v, want recovery block", err)
	}
	for unit, active := range fixture.active {
		if active {
			t.Fatalf("multi-journal recovery left %s active", unit)
		}
	}
	if journals := tailscaleResolverJournalPaths(t, fixture.server); len(journals) != 2 {
		t.Fatalf("retained journals = %q, want both", journals)
	}
}

func TestRecoverTailscaleResolverIsolationStopsSafeJournalWithCorruptOrUnexpectedCompanion(t *testing.T) {
	for _, companion := range []string{"corrupt.jsonl", "README"} {
		t.Run(companion, func(t *testing.T) {
			fixture := newTailscaleResolverFleetTransactionFixture(t)
			for unit := range fixture.active {
				fixture.active[unit] = true
			}
			restore := stubTailscaleResolverFleetLifecycle(t, fixture.active)
			defer restore()
			header := mustNewTailscaleResolverJournalHeader(t, fixture.plan)
			header.ID = "safe"
			journal, err := createTailscaleResolverJournal(fixture.server.cfg.RootDir, header)
			if err != nil {
				t.Fatal(err)
			}
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}
			dir := tailscaleResolverJournalDir(fixture.server.cfg.RootDir)
			writePrivateTailscaleResolverJournal(t, filepath.Join(dir, companion), []byte("{not-json}\n"))

			err = fixture.server.recoverTailscaleResolverIsolation(context.Background())
			if !errors.Is(err, errTailscaleResolverRecoveryBlocked) {
				t.Fatalf("recovery with companion error = %v, want recovery block", err)
			}
			for unit, active := range fixture.active {
				if active {
					t.Fatalf("recovery with %s left %s active", companion, unit)
				}
			}
			if _, statErr := os.Lstat(filepath.Join(dir, "safe.jsonl")); statErr != nil {
				t.Fatalf("safe journal was not retained: %v", statErr)
			}
		})
	}
}

func TestRecoverTailscaleResolverIsolationRejectsFreshDatabaseRecordMismatchBeforeWrite(t *testing.T) {
	fixture := newTailscaleResolverFleetTransactionFixture(t)
	for unit := range fixture.active {
		fixture.active[unit] = true
	}
	restore := stubTailscaleResolverFleetLifecycle(t, fixture.active)
	defer restore()
	createTailscaleResolverRecoveryJournal(t, fixture, tailscaleResolverPhasePrepared)
	replacement := filepath.Join(t.TempDir(), "bin", "yeet-alpha-ts-v999.service")
	if err := os.MkdirAll(filepath.Dir(replacement), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacement, []byte("replacement\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.server.cfg.DB.MutateService("alpha", func(_ *db.Data, service *db.Service) error {
		service.Artifacts[db.ArtifactTSService].Refs[db.Gen(service.Generation)] = replacement
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	writes := 0
	previousWrite := tailscaleResolverWriteManagedFile
	tailscaleResolverWriteManagedFile = func(
		root, relative string,
		expected serviceIdentityPathProof,
		content []byte,
	) (serviceIdentityPathProof, error) {
		writes++
		return previousWrite(root, relative, expected, content)
	}
	t.Cleanup(func() { tailscaleResolverWriteManagedFile = previousWrite })

	err := fixture.server.recoverTailscaleResolverIsolation(context.Background())
	if !errors.Is(err, errTailscaleResolverRecoveryBlocked) ||
		!strings.Contains(err.Error(), "database record") {
		t.Fatalf("record-mismatch recovery error = %v, want database-record block", err)
	}
	if writes != 0 {
		t.Fatalf("record-mismatch recovery performed %d managed writes", writes)
	}
	for unit, active := range fixture.active {
		if active {
			t.Fatalf("record-mismatch recovery left %s active", unit)
		}
	}
}

func createTailscaleResolverRecoveryJournal(
	t *testing.T,
	fixture *tailscaleResolverFleetTransactionFixture,
	through string,
) {
	t.Helper()
	header := mustNewTailscaleResolverJournalHeader(t, fixture.plan)
	journal, err := createTailscaleResolverJournal(fixture.server.cfg.RootDir, header)
	if err != nil {
		t.Fatalf("createTailscaleResolverJournal: %v", err)
	}
	defer func() {
		if err := journal.Close(); err != nil {
			t.Fatalf("close journal: %v", err)
		}
	}()
	if err := journal.AppendPhase(tailscaleResolverPhasePrepared); err != nil {
		t.Fatal(err)
	}
	if through == tailscaleResolverPhasePrepared {
		writeRecoveryPlanPrefix(t, fixture, 3)
		return
	}
	writeRecoveryPlanPrefix(t, fixture, len(header.Files))
	if err := journal.AppendPhase(tailscaleResolverPhaseFilesWritten); err != nil {
		t.Fatal(err)
	}
	if through == tailscaleResolverPhaseFilesWritten {
		return
	}
	if err := journal.AppendPhase(tailscaleResolverPhaseDaemonReloaded); err != nil {
		t.Fatal(err)
	}
	if through == tailscaleResolverPhaseDaemonReloaded {
		for unit := range fixture.active {
			fixture.active[unit] = true
		}
		return
	}
	for unit := range fixture.active {
		fixture.active[unit] = true
	}
	if err := journal.AppendPhase(tailscaleResolverPhaseServicesVerified); err != nil {
		t.Fatal(err)
	}
	if through == tailscaleResolverPhaseServicesVerified {
		return
	}
	if through != tailscaleResolverPhaseCommitted {
		t.Fatalf("unknown recovery phase %q", through)
	}
	if err := journal.AppendPhase(tailscaleResolverPhaseCommitted); err != nil {
		t.Fatal(err)
	}
}

func writeRecoveryPlanPrefix(
	t *testing.T,
	fixture *tailscaleResolverFleetTransactionFixture,
	count int,
) {
	t.Helper()
	written := 0
	for _, service := range fixture.plan.Services {
		for _, file := range service.Files {
			if written == count {
				return
			}
			if _, err := tailscaleResolverWriteManagedFile(
				file.Root,
				file.Relative,
				file.Proof,
				file.Next,
			); err != nil {
				t.Fatalf("write recovery fixture %s: %v", file.Path, err)
			}
			written++
		}
	}
	if written != count {
		t.Fatalf("wrote %d recovery files, want %d", written, count)
	}
}

func writePrivateTailscaleResolverJournal(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}
