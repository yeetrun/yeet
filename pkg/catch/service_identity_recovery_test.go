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
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func serviceIdentityTestRuntimeUnits(service *db.Service, name string, primaryActive bool) []serviceIdentityRuntimeUnitState {
	units, _ := serviceIdentityRuntimeUnits(service, name)
	states := make([]serviceIdentityRuntimeUnitState, len(units))
	primary := serviceIdentityPrimaryRuntimeUnit(service, name)
	for index, unit := range units {
		states[index] = serviceIdentityRuntimeUnitState{Unit: unit, Active: primaryActive && unit == primary}
	}
	return states
}

func TestServiceIdentityMigrationRecoveryRollsBackValidUnsealedJournal(t *testing.T) {
	server := newTestServer(t)
	oldRoot := filepath.Join(t.TempDir(), "old")
	if err := ensureDirsForRoot(oldRoot, ""); err != nil {
		t.Fatal(err)
	}
	oldService := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: oldRoot, Generation: 2}
	targetService := oldService.Clone()
	targetService.Identity = &db.ServiceIdentity{RequestedUser: "1000", RequestedGroup: "1000", UID: 1000, GID: 1000}
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, service *db.Service) error {
		*service = *targetService.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(t.TempDir(), "api.service")
	oldUnit := "[Service]\nUser=root\nGroup=root\n"
	if err := os.WriteFile(unitPath, []byte(oldUnit), 0o600); err != nil {
		t.Fatal(err)
	}
	j, err := createServiceIdentityJournal(server.cfg.RootDir, serviceIdentityJournalHeader{
		ID: "tx-unsealed", Service: "api", Root: oldRoot,
		TargetIdentity: *targetService.Identity, PreviousService: oldService, TargetService: targetService,
		PreviousUnit: oldUnit, PreviousUnitPresent: true, PreviousUnitPath: unitPath, PreviousUnitMode: 0o600,
		PreviousRoot: oldRoot, TargetRoot: oldRoot, WasRunning: true,
		PreviousRuntimeUnits: serviceIdentityTestRuntimeUnits(oldService, "api", true),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := j.AppendPhase(serviceIdentityPhaseRecord{Phase: serviceIdentityPhaseStop}); err != nil {
		t.Fatal(err)
	}
	journalPath := j.Path()
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	running := false
	ops := serviceIdentityMigrationOps{
		unitPath:  func(string) string { return unitPath },
		isRunning: func(context.Context, string) (bool, error) { return running, nil },
		start:     func(context.Context, string) error { running = true; return nil },
		stop:      func(context.Context, string) error { running = false; return nil },
		reload:    func(context.Context) error { return nil },
	}
	if err := server.recoverServiceIdentityMigrationsWithOps(context.Background(), &ops); err != nil {
		t.Fatalf("recoverServiceIdentityMigrationsWithOps: %v", err)
	}
	assertRecoveredServiceIdentityState(t, server, unitPath, oldUnit, 0o600, oldService, true)
	if _, err := os.Stat(journalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal still exists: %v", err)
	}
}

func TestServiceIdentityMigrationRecoveryTreatsDurableCompleteAsCleanupOnly(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "root")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	oldService := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root}
	targetService := oldService.Clone()
	targetService.Identity = &db.ServiceIdentity{RequestedUser: "1000", RequestedGroup: "1000", UID: 1000, GID: 1000}
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, service *db.Service) error {
		*service = *targetService.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(t.TempDir(), "api.service")
	if err := os.WriteFile(unitPath, []byte("target"), 0o644); err != nil {
		t.Fatal(err)
	}
	j, err := createServiceIdentityJournal(server.cfg.RootDir, serviceIdentityJournalHeader{
		ID: "tx-complete", Service: "api", Root: root,
		TargetIdentity: *targetService.Identity, PreviousService: oldService, TargetService: targetService,
		PreviousUnit: "old", PreviousUnitPresent: true, PreviousUnitPath: unitPath, PreviousUnitMode: 0o600,
		PreviousRoot: root, TargetRoot: root,
		PreviousRuntimeUnits: serviceIdentityTestRuntimeUnits(oldService, "api", false),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Seal(); err != nil {
		t.Fatal(err)
	}
	if err := j.AppendPhase(serviceIdentityPhaseRecord{Phase: serviceIdentityPhaseDBCommit}); err != nil {
		t.Fatal(err)
	}
	if err := j.AppendPhase(serviceIdentityPhaseRecord{Phase: serviceIdentityPhaseComplete}); err != nil {
		t.Fatal(err)
	}
	journalPath := j.Path()
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	restored := false
	ops := serviceIdentityMigrationOps{
		unitPath: func(string) string { return unitPath },
		restore:  func(string) error { restored = true; return errors.New("must not restore") },
	}
	if err := server.recoverServiceIdentityMigrationsWithOps(context.Background(), &ops); err != nil {
		t.Fatal(err)
	}
	if restored {
		t.Fatal("complete migration was rolled back")
	}
	service, err := server.serviceView("api")
	if err != nil || !reflect.DeepEqual(service.AsStruct(), targetService) {
		t.Fatalf("service = %#v, %v, want target", service.AsStruct(), err)
	}
	raw, _ := os.ReadFile(unitPath)
	if string(raw) != "target" {
		t.Fatalf("unit = %q, want target", raw)
	}
	if _, err := os.Stat(journalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("complete journal still exists: %v", err)
	}
}

func TestServiceIdentityMigrationRecoveryTreatsDatabaseCommitAsProvisional(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "root")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	oldService := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root}
	targetService := oldService.Clone()
	targetService.Identity = &db.ServiceIdentity{RequestedUser: "1000", RequestedGroup: "1000", UID: 1000, GID: 1000}
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, service *db.Service) error {
		*service = *targetService.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(t.TempDir(), "api.service")
	if err := os.WriteFile(unitPath, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	j, err := createServiceIdentityJournal(server.cfg.RootDir, serviceIdentityJournalHeader{
		ID: "tx-provisional", Service: "api", Root: root,
		TargetIdentity: *targetService.Identity, PreviousService: oldService, TargetService: targetService,
		PreviousUnit: "old", PreviousUnitPresent: true, PreviousUnitPath: unitPath, PreviousUnitMode: 0o600,
		PreviousRoot: root, TargetRoot: root,
		PreviousRuntimeUnits: serviceIdentityTestRuntimeUnits(oldService, "api", false),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Seal(); err != nil {
		t.Fatal(err)
	}
	if err := j.AppendPhase(serviceIdentityPhaseRecord{Phase: serviceIdentityPhaseDBCommit}); err != nil {
		t.Fatal(err)
	}
	journalPath := j.Path()
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	ops := serviceIdentityMigrationOps{
		unitPath:  func(string) string { return unitPath },
		restore:   func(string) error { return nil },
		reload:    func(context.Context) error { return nil },
		isRunning: func(context.Context, string) (bool, error) { return false, nil },
	}
	if err := server.recoverServiceIdentityMigrationsWithOps(context.Background(), &ops); err != nil {
		t.Fatal(err)
	}
	assertRecoveredServiceIdentityState(t, server, unitPath, "old", 0o600, oldService, false)
	if _, err := os.Stat(journalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("provisional journal still exists: %v", err)
	}
}

func TestServiceIdentityMigrationRecoveryDeletesAbsentPredecessorExactly(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "root")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	observed := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root}
	target := observed.Clone()
	target.Identity = &db.ServiceIdentity{RequestedUser: "1000", RequestedGroup: "1000", UID: 1000, GID: 1000}
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, service *db.Service) error {
		*service = *target.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(t.TempDir(), "api.service")
	if err := os.WriteFile(unitPath, []byte("replacement"), 0o644); err != nil {
		t.Fatal(err)
	}
	previousUnitProof := serviceIdentityPathProof{Path: unitPath}
	replacementProof, err := captureServiceIdentityPathProof(unitPath)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := createServiceIdentityJournal(server.cfg.RootDir, serviceIdentityJournalHeader{
		ID: "tx-first-install", Service: "api", Root: root,
		TargetIdentity: *target.Identity, ObservedService: observed, TargetService: target,
		PreviousUnitPath: unitPath, PreviousRoot: root, TargetRoot: root,
		PreviousUnitProof:    previousUnitProof,
		PreviousRuntimeUnits: serviceIdentityTestRuntimeUnits(observed, "api", false),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Seal(); err != nil {
		t.Fatal(err)
	}
	if err := journal.AppendPhase(serviceIdentityPhaseRecord{
		Phase: serviceIdentityPhaseUnitWriteIntent, PrimaryUnit: previousUnitProof,
		PrimaryUnitIntent: serviceIdentityStateFromProof(replacementProof),
	}); err != nil {
		t.Fatal(err)
	}
	if err := journal.AppendPhase(serviceIdentityPhaseRecord{Phase: serviceIdentityPhaseUnitWrite, PrimaryUnit: replacementProof}); err != nil {
		t.Fatal(err)
	}
	journalPath := journal.Path()
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	ops := serviceIdentityMigrationOps{
		unitPath:  func(string) string { return unitPath },
		isRunning: func(context.Context, string) (bool, error) { return false, nil },
		reload:    func(context.Context) error { return nil },
	}
	if err := server.recoverServiceIdentityMigrationsWithOps(context.Background(), &ops); err != nil {
		t.Fatal(err)
	}
	if _, err := server.serviceView("api"); !errors.Is(err, errServiceNotFound) {
		t.Fatalf("recovery left absent predecessor in database: %v", err)
	}
	for _, path := range []string{unitPath, journalPath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("recovery left %s: %v", path, err)
		}
	}
}

func TestServiceIdentityMigrationRecoveryDuplicateJournalsBlockOnlyService(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "root")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	service := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root}
	target := service.Clone()
	target.Identity = &db.ServiceIdentity{RequestedUser: "1000", RequestedGroup: "1000", UID: 1000, GID: 1000}
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, current *db.Service) error {
		*current = *target.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"tx-one", "tx-two"} {
		j, err := createServiceIdentityJournal(server.cfg.RootDir, serviceIdentityJournalHeader{
			ID: id, Service: "api", Root: root,
			TargetIdentity:  *target.Identity,
			PreviousService: service, TargetService: target, PreviousRoot: root, TargetRoot: root,
			PreviousRuntimeUnits: serviceIdentityTestRuntimeUnits(service, "api", false),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := j.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := server.recoverServiceIdentityMigrationsWithOps(context.Background(), &serviceIdentityMigrationOps{}); err == nil || !strings.Contains(err.Error(), "duplicate journals") {
		t.Fatalf("recovery error = %v, want duplicate journal refusal", err)
	}
	if err := server.checkServiceIdentityMutationAllowed("api"); !errors.Is(err, errServiceIdentityRecoveryBlocked) {
		t.Fatalf("api mutation block = %v", err)
	}
	if err := server.checkServiceIdentityMutationAllowed("worker"); err != nil {
		t.Fatalf("unrelated service blocked: %v", err)
	}
}

func TestServiceIdentityMigrationRecoveryGroupsInvalidAndValidSiblingBeforeValidation(t *testing.T) {
	for _, tt := range []struct {
		name      string
		invalidID string
		validID   string
	}{
		{name: "invalid filename sorts first", invalidID: "aaa-invalid", validID: "zzz-valid"},
		{name: "valid filename sorts first", invalidID: "zzz-invalid", validID: "aaa-valid"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(t)
			root := filepath.Join(t.TempDir(), "root")
			if err := ensureDirsForRoot(root, ""); err != nil {
				t.Fatal(err)
			}
			previous := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root}
			target := previous.Clone()
			target.Identity = &db.ServiceIdentity{RequestedUser: "1000", RequestedGroup: "1000", UID: 1000, GID: 1000}
			if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, service *db.Service) error {
				*service = *target.Clone()
				return nil
			}); err != nil {
				t.Fatal(err)
			}

			unitPath := filepath.Join(t.TempDir(), "api.service")
			journal, err := createServiceIdentityJournal(server.cfg.RootDir, serviceIdentityJournalHeader{
				ID: tt.validID, Service: "api", Root: root,
				TargetIdentity: *target.Identity, PreviousService: previous, TargetService: target,
				PreviousUnitPath: unitPath, PreviousRoot: root, TargetRoot: root,
				PreviousRuntimeUnits: serviceIdentityTestRuntimeUnits(previous, "api", false),
			})
			if err != nil {
				t.Fatal(err)
			}
			validPath := journal.Path()
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}
			invalidPath := serviceIdentityJournalPath(server.cfg.RootDir, "api", tt.invalidID)
			if err := os.WriteFile(invalidPath, []byte("not-json\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			ops := serviceIdentityMigrationOps{
				unitPath:  func(string) string { return unitPath },
				isRunning: func(context.Context, string) (bool, error) { return false, nil },
				reload:    func(context.Context) error { return nil },
			}
			err = server.recoverServiceIdentityMigrationsWithOps(context.Background(), &ops)
			if err == nil || !strings.Contains(err.Error(), "duplicate journals") {
				t.Fatalf("recovery error = %v, want duplicate journal refusal", err)
			}
			for _, path := range []string{invalidPath, validPath} {
				if _, statErr := os.Stat(path); statErr != nil {
					t.Fatalf("blocked sibling journal %s changed: %v", path, statErr)
				}
			}
			service, err := server.serviceView("api")
			if err != nil || !reflect.DeepEqual(service.AsStruct(), target) {
				t.Fatalf("database service = %#v, %v, want untouched target %#v", service.AsStruct(), err, target)
			}
		})
	}
}

func TestServiceIdentityRecoveryBlockAllowsReadsAndRejectsMutations(t *testing.T) {
	server := newTestServer(t)
	server.setServiceIdentityMutationBlock("api", errors.New("journal recovery failed"))
	var rw bytes.Buffer
	execer := &ttyExecer{s: server, sn: "api", rw: &rw}
	if err := execer.dispatch([]string{"version"}); err != nil {
		t.Fatalf("read command blocked: %v", err)
	}
	if err := execer.dispatch([]string{"stop"}); !errors.Is(err, errServiceIdentityRecoveryBlocked) {
		t.Fatalf("mutation error = %v, want recovery block", err)
	}
}

func TestServiceIdentityRecoveryBlockRejectsMigrationEngine(t *testing.T) {
	server := newTestServer(t)
	server.setServiceIdentityMutationBlock("api", errors.New("journal recovery failed"))
	_, err := server.migrateServiceIdentity(context.Background(), serviceIdentityMigrationRequest{
		Service: "api",
		Target: resolvedServiceIdentity{Persisted: db.ServiceIdentity{
			RequestedUser: "1000", RequestedGroup: "1000", UID: 1000, GID: 1000,
		}},
	}, nil)
	if !errors.Is(err, errServiceIdentityRecoveryBlocked) {
		t.Fatalf("migration error = %v, want recovery block", err)
	}
}

func TestServiceIdentityJournalRejectsNonMonotonicDurablePhases(t *testing.T) {
	tests := []struct {
		name     string
		phases   []serviceIdentityPhaseRecord
		line     string
		wantText string
	}{
		{
			name: "duplicate", phases: []serviceIdentityPhaseRecord{{Phase: serviceIdentityPhaseOwnership}},
			line: `{"phase":"ownership-applied"}`, wantText: "duplicates",
		},
		{
			name: "out of order", phases: []serviceIdentityPhaseRecord{{Phase: serviceIdentityPhaseUnitWrite}},
			line: `{"phase":"ownership-applied"}`, wantText: "out of order",
		},
		{
			name: "post complete", phases: []serviceIdentityPhaseRecord{{Phase: serviceIdentityPhaseComplete}},
			line: `{"phase":"cleanup-retry"}`, wantText: "after complete",
		},
		{
			name: "unknown", line: `{"phase":"cleanup-retry"}`, wantText: "unknown phase",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contents := serviceIdentityJournalContents{Sealed: true, Phases: tt.phases}
			err := decodeServiceIdentityJournalLine([]byte(tt.line), len(tt.phases)+2, &contents)
			if err == nil || !strings.Contains(err.Error(), tt.wantText) {
				t.Fatalf("decode phase error = %v, want %q", err, tt.wantText)
			}
		})
	}
}

func TestServiceIdentityMigrationRecoveryFailureBlocksOnlyThatService(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "root")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	oldService := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root}
	targetService := oldService.Clone()
	targetService.Identity = &db.ServiceIdentity{RequestedUser: "1000", RequestedGroup: "1000", UID: 1000, GID: 1000}
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, service *db.Service) error {
		*service = *targetService.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(t.TempDir(), "api.service")
	j, err := createServiceIdentityJournal(server.cfg.RootDir, serviceIdentityJournalHeader{
		ID: "tx-block", Service: "api", Root: root, TargetIdentity: *targetService.Identity,
		PreviousService: oldService, TargetService: targetService, PreviousUnitPath: unitPath,
		PreviousRoot: root, TargetRoot: root, WasRunning: true,
		PreviousRuntimeUnits: serviceIdentityTestRuntimeUnits(oldService, "api", true),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Seal(); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	startCalls := 0
	ops := serviceIdentityMigrationOps{
		unitPath:  func(string) string { return unitPath },
		restore:   func(string) error { return errors.New("restore denied") },
		reload:    func(context.Context) error { return nil },
		isRunning: func(context.Context, string) (bool, error) { return false, nil },
		start:     func(context.Context, string) error { startCalls++; return nil },
	}
	if err := server.recoverServiceIdentityMigrationsWithOps(context.Background(), &ops); err == nil {
		t.Fatal("recovery unexpectedly succeeded")
	}
	if err := server.checkServiceIdentityMutationAllowed("api"); err == nil || !errors.Is(err, errServiceIdentityRecoveryBlocked) {
		t.Fatalf("api mutation block = %v", err)
	}
	if err := server.checkServiceIdentityMutationAllowed("other"); err != nil {
		t.Fatalf("unrelated service blocked: %v", err)
	}
	if startCalls != 0 {
		t.Fatalf("start calls = %d, want service left stopped after recovery failure", startCalls)
	}
}

func TestServiceIdentityMigrationRecoveryRejectsIncoherentUnitPathBeforeMutation(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "root")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	oldService := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root}
	targetService := oldService.Clone()
	targetService.Identity = &db.ServiceIdentity{RequestedUser: "1000", RequestedGroup: "1000", UID: 1000, GID: 1000}
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, service *db.Service) error {
		*service = *targetService.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "operator-data")
	if err := os.WriteFile(victim, []byte("preserve me"), 0o600); err != nil {
		t.Fatal(err)
	}
	j, err := createServiceIdentityJournal(server.cfg.RootDir, serviceIdentityJournalHeader{
		ID: "tx-bad-unit", Service: "api", Root: root,
		TargetIdentity: *targetService.Identity, PreviousService: oldService, TargetService: targetService,
		PreviousUnit: "corrupt overwrite", PreviousUnitPresent: true, PreviousUnitPath: victim,
		PreviousRoot: root, TargetRoot: root,
		PreviousRuntimeUnits: serviceIdentityTestRuntimeUnits(oldService, "api", false),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	mutated := false
	ops := serviceIdentityMigrationOps{
		stop:   func(context.Context, string) error { mutated = true; return nil },
		start:  func(context.Context, string) error { mutated = true; return nil },
		reload: func(context.Context) error { mutated = true; return nil },
	}
	err = server.recoverServiceIdentityMigrationsWithOps(context.Background(), &ops)
	if err == nil || !strings.Contains(err.Error(), "unit path") {
		t.Fatalf("recovery error = %v, want incoherent unit path rejection", err)
	}
	if mutated {
		t.Fatal("recovery invoked runtime mutations before rejecting the journal")
	}
	raw, readErr := os.ReadFile(victim)
	if readErr != nil || string(raw) != "preserve me" {
		t.Fatalf("victim contents = %q, %v", raw, readErr)
	}
}

func TestServiceIdentityMigrationRecoveryRejectsIncoherentGenerationPathBeforeMutation(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "root")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	oldService := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root, Generation: 1}
	targetService := oldService.Clone()
	targetService.Identity = &db.ServiceIdentity{RequestedUser: "1000", RequestedGroup: "1000", UID: 1000, GID: 1000}
	targetService.Artifacts = db.ArtifactStore{
		db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/artifact"}},
	}
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, service *db.Service) error {
		*service = *oldService.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(t.TempDir(), "api.service")
	victim := filepath.Join(t.TempDir(), "operator-data")
	if err := os.WriteFile(victim, []byte("preserve me"), 0o600); err != nil {
		t.Fatal(err)
	}
	const id = "tx-bad-generation"
	j, err := createServiceIdentityJournal(server.cfg.RootDir, serviceIdentityJournalHeader{
		ID: id, Service: "api", Root: root,
		TargetIdentity: *targetService.Identity, PreviousService: oldService, TargetService: targetService,
		PreviousUnitPath: unitPath, PreviousRoot: root, TargetRoot: root,
		GenerationBackups: []serviceIdentityGenerationBackup{{
			Path: victim, BackupPath: victim + ".yeet-identity-" + id,
		}},
		GenerationUnits:      []serviceIdentityUnitEnablement{{Unit: "api.service", TargetEnabled: true}},
		PreviousRuntimeUnits: serviceIdentityTestRuntimeUnits(oldService, "api", false),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := j.AppendPhase(serviceIdentityPhaseRecord{Phase: serviceIdentityPhaseGenerationBackup}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	mutated := false
	ops := serviceIdentityMigrationOps{
		unitPath:  func(string) string { return unitPath },
		isRunning: func(context.Context, string) (bool, error) { return false, nil },
		stop:      func(context.Context, string) error { mutated = true; return nil },
		start:     func(context.Context, string) error { mutated = true; return nil },
		reload:    func(context.Context) error { mutated = true; return nil },
	}
	err = server.recoverServiceIdentityMigrationsWithOps(context.Background(), &ops)
	if err == nil || !strings.Contains(err.Error(), "generation backup") {
		t.Fatalf("recovery error = %v, want incoherent generation backup rejection", err)
	}
	if mutated {
		t.Fatal("recovery invoked runtime mutations before rejecting the journal")
	}
	raw, readErr := os.ReadFile(victim)
	if readErr != nil || string(raw) != "preserve me" {
		t.Fatalf("victim contents = %q, %v", raw, readErr)
	}
}

func TestServiceIdentityMigrationRecoveryRequiresExactGenerationHeaderLists(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "root")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	previous := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root, Generation: 1}
	target := previous.Clone()
	target.Identity = &db.ServiceIdentity{RequestedUser: "1000", RequestedGroup: "1000", UID: 1000, GID: 1000}
	target.Artifacts = db.ArtifactStore{
		db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/artifact/unit"}},
		db.ArtifactEnvFile:     {Refs: map[db.ArtifactRef]string{db.Gen(1): "/artifact/env"}},
	}
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, service *db.Service) error {
		*service = *previous.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(t.TempDir(), "api.service")
	journal, err := createServiceIdentityJournal(server.cfg.RootDir, serviceIdentityJournalHeader{
		ID: "tx-missing-generation", Service: "api", Root: root,
		TargetIdentity: *target.Identity, PreviousService: previous, TargetService: target,
		PreviousUnitPath: unitPath, PreviousRoot: root, TargetRoot: root,
		PreviousRuntimeUnits: serviceIdentityTestRuntimeUnits(previous, "api", false),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.AppendPhase(serviceIdentityPhaseRecord{Phase: serviceIdentityPhaseGenerationBackup}); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	mutated := false
	ops := serviceIdentityMigrationOps{
		unitPath:  func(string) string { return unitPath },
		isRunning: func(context.Context, string) (bool, error) { return false, nil },
		reload:    func(context.Context) error { mutated = true; return nil },
	}
	err = server.recoverServiceIdentityMigrationsWithOps(context.Background(), &ops)
	if err == nil || !strings.Contains(err.Error(), "generation backup paths do not match") {
		t.Fatalf("recovery error = %v, want exact generation header rejection", err)
	}
	if mutated {
		t.Fatal("recovery mutated runtime before rejecting missing generation header lists")
	}
}

func TestServiceIdentityMigrationRecoveryRequiresGenerationPhaseDependencies(t *testing.T) {
	for _, tt := range []struct {
		name   string
		phases []serviceIdentityPhaseRecord
		want   string
	}{
		{
			name:   "stage requires backup",
			phases: []serviceIdentityPhaseRecord{{Phase: serviceIdentityPhaseGenerationStage}},
			want:   "staged generation has no durable completed backup phase",
		},
		{
			name:   "activation requires stage",
			phases: []serviceIdentityPhaseRecord{{Phase: serviceIdentityPhaseGenerationActivate}},
			want:   "generation activation has no durable staged phase",
		},
		{
			name:   "enabled requires activation",
			phases: []serviceIdentityPhaseRecord{{Phase: serviceIdentityPhaseGenerationEnabled}},
			want:   "enabled generation has no durable activation phase",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(t)
			err := server.validateServiceIdentityRecoveryContents(serviceIdentityJournalContents{Phases: tt.phases})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validation error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestServiceIdentityMigrationRecoveryConstrainsRuntimeBackupsToExactAllowedPaths(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "root")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	previous := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root}
	target := previous.Clone()
	target.Identity = &db.ServiceIdentity{RequestedUser: "1000", RequestedGroup: "1000", UID: 1000, GID: 1000}
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, service *db.Service) error {
		*service = *previous.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(t.TempDir(), "api.service")
	newContents := func(id string, paths ...string) serviceIdentityJournalContents {
		t.Helper()
		backups := make([]serviceIdentityGenerationBackup, len(paths))
		for index, path := range paths {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
				if err := os.WriteFile(path, []byte("legacy"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			proof, err := captureServiceIdentityPathProof(path)
			if err != nil {
				t.Fatal(err)
			}
			backups[index] = serviceIdentityGenerationBackup{
				Path: path, BackupPath: filepath.Join(serviceIdentityMigrationBackupDir(server.cfg.RootDir, id), "runtime", fmt.Sprintf("%03d", index)),
				Present: true, Original: proof,
			}
		}
		header := serviceIdentityJournalHeader{
			ID: id, Service: "api", Root: root,
			TargetIdentity: *target.Identity, PreviousService: previous, TargetService: target,
			PreviousUnitPath: unitPath, PreviousRoot: root, TargetRoot: root,
			PreviousRuntimeUnits: serviceIdentityTestRuntimeUnits(previous, "api", false),
		}
		return serviceIdentityJournalContents{Header: header, Phases: []serviceIdentityPhaseRecord{{
			Phase: serviceIdentityPhaseRuntimePlan, RuntimeBackups: backups,
		}}}
	}
	validate := func(contents serviceIdentityJournalContents) error {
		return server.validateServiceIdentityRecoveryHeader(contents, &serviceIdentityMigrationOps{
			unitPath: func(string) string { return unitPath },
		})
	}

	allowed := filepath.Join(serviceRunDirForRoot(root), "env")
	if err := validate(newContents("tx-runtime-allowed", allowed)); err != nil {
		t.Fatalf("allowed runtime backup rejected: %v", err)
	}
	missingRuntime := newContents("tx-runtime-missing", allowed)
	missingRuntime.Header.PreviousRuntimeUnits = nil
	if err := validate(missingRuntime); err == nil || !strings.Contains(err.Error(), "previous runtime units") {
		t.Fatalf("missing exact runtime list error = %v, want previous runtime units rejection", err)
	}
	for _, tt := range []struct {
		name  string
		paths []string
		want  string
	}{
		{name: "nested allowed basename", paths: []string{filepath.Join(serviceRunDirForRoot(root), "nested", "env")}, want: "invalid legacy runtime backup path"},
		{name: "unknown direct artifact", paths: []string{filepath.Join(serviceRunDirForRoot(root), "operator")}, want: "not a managed compatibility artifact"},
		{name: "duplicate direct artifact", paths: []string{allowed, allowed}, want: "duplicate legacy runtime backup path"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := validate(newContents("tx-runtime-"+strings.ReplaceAll(tt.name, " ", "-"), tt.paths...))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validation error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestServiceIdentityMigrationRecoveryRestoresRuntimeBackupRecordedBeforeSeal(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "root")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(serviceRunDirForRoot(root), "env")
	if err := os.WriteFile(legacyPath, []byte("legacy runtime"), 0o640); err != nil {
		t.Fatal(err)
	}
	oldService := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root}
	targetService := oldService.Clone()
	targetService.Identity = &db.ServiceIdentity{RequestedUser: "1000", RequestedGroup: "1000", UID: 1000, GID: 1000}
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, service *db.Service) error {
		*service = *oldService.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(t.TempDir(), "api.service")
	oldUnit := "[Service]\nUser=root\nGroup=root\n"
	if err := os.WriteFile(unitPath, []byte(oldUnit), 0o600); err != nil {
		t.Fatal(err)
	}
	unitProof, err := captureServiceIdentityPathProof(unitPath)
	if err != nil {
		t.Fatal(err)
	}
	const migrationID = "tx-runtime-before-seal"
	backups, err := captureLegacyNativeRuntimeBackups(server.cfg.RootDir, root, "api", migrationID)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := createServiceIdentityJournal(server.cfg.RootDir, serviceIdentityJournalHeader{
		ID: migrationID, Service: "api", Root: root,
		TargetIdentity: *targetService.Identity, PreviousServicePresent: true,
		PreviousService: oldService, ObservedService: oldService, TargetService: targetService,
		PreviousUnit: oldUnit, PreviousUnitPresent: true, PreviousUnitPath: unitPath,
		PreviousUnitMode: 0o600, PreviousUnitProof: unitProof,
		PreviousRoot: root, TargetRoot: root,
		PreviousRuntimeUnits: serviceIdentityTestRuntimeUnits(oldService, "api", false),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.AppendPhase(serviceIdentityPhaseRecord{
		Phase: serviceIdentityPhaseRuntimePlan, RuntimeBackups: backups,
	}); err != nil {
		t.Fatal(err)
	}
	if err := journal.AppendPhase(serviceIdentityPhaseRecord{Phase: serviceIdentityPhaseRuntimeBackup}); err != nil {
		t.Fatal(err)
	}
	backups, err = backupLegacyNativeRuntimeArtifacts(server.cfg.RootDir, root, backups)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.AppendPhase(serviceIdentityPhaseRecord{
		Phase: serviceIdentityPhaseRuntimeBackedUp, RuntimeBackups: backups,
	}); err != nil {
		t.Fatal(err)
	}
	if err := removeLegacyNativeRuntimeArtifacts(root, backups); err != nil {
		t.Fatal(err)
	}
	journalPath := journal.Path()
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	ops := serviceIdentityMigrationOps{
		unitPath:  func(string) string { return unitPath },
		isRunning: func(context.Context, string) (bool, error) { return false, nil },
		reload:    func(context.Context) error { return nil },
	}
	if err := server.recoverServiceIdentityMigrationsWithOps(context.Background(), &ops); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(legacyPath)
	if err != nil || string(raw) != "legacy runtime" {
		t.Fatalf("restored legacy runtime = %q, %v", raw, err)
	}
	info, err := os.Stat(legacyPath)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("restored legacy runtime mode = %v, %v", info, err)
	}
	if _, err := os.Stat(journalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal remains after restart recovery: %v", err)
	}
}

func TestServiceIdentityMigrationRecoveryUsesExactProspectiveUnitIntent(t *testing.T) {
	for _, state := range []string{"old", "yeet-replacement", "external-replacement"} {
		t.Run(state, func(t *testing.T) {
			server := newTestServer(t)
			root := filepath.Join(t.TempDir(), "root")
			if err := ensureDirsForRoot(root, ""); err != nil {
				t.Fatal(err)
			}
			oldService := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root}
			targetService := oldService.Clone()
			targetService.Identity = &db.ServiceIdentity{RequestedUser: "1000", RequestedGroup: "1000", UID: 1000, GID: 1000}
			if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, service *db.Service) error {
				*service = *oldService.Clone()
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			unitPath := filepath.Join(t.TempDir(), "api.service")
			oldUnit := []byte("[Service]\nUser=root\nGroup=root\n")
			replacement := []byte("[Service]\nUser=1000\nGroup=1000\n")
			if err := os.WriteFile(unitPath, oldUnit, 0o600); err != nil {
				t.Fatal(err)
			}
			previousProof, err := captureServiceIdentityPathProof(unitPath)
			if err != nil {
				t.Fatal(err)
			}
			intent := serviceIdentityDesiredFileState(
				unitPath, replacement, 0o644, uint32(os.Geteuid()), uint32(os.Getegid()),
			)
			journal, err := createServiceIdentityJournal(server.cfg.RootDir, serviceIdentityJournalHeader{
				ID: "tx-unit-intent-" + state, Service: "api", Root: root,
				TargetIdentity: *targetService.Identity, PreviousServicePresent: true,
				PreviousService: oldService, ObservedService: oldService, TargetService: targetService,
				PreviousUnit: string(oldUnit), PreviousUnitPresent: true, PreviousUnitPath: unitPath,
				PreviousUnitMode: 0o600, PreviousUnitProof: previousProof,
				PreviousRuntimeUnits: serviceIdentityTestRuntimeUnits(oldService, "api", false),
				PreviousRoot:         root, TargetRoot: root,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := journal.Seal(); err != nil {
				t.Fatal(err)
			}
			if err := journal.AppendPhase(serviceIdentityPhaseRecord{
				Phase: serviceIdentityPhaseUnitWriteIntent, PrimaryUnit: previousProof, PrimaryUnitIntent: intent,
			}); err != nil {
				t.Fatal(err)
			}
			switch state {
			case "yeet-replacement":
				if err := writeServiceIdentityUnitAtomically(unitPath, replacement, 0o644); err != nil {
					t.Fatal(err)
				}
			case "external-replacement":
				if err := writeServiceIdentityUnitAtomically(unitPath, []byte("operator replacement"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			journalPath := journal.Path()
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}
			previousRuntime := serviceIdentityTestRuntimeUnits(oldService, "api", false)
			ops := serviceIdentityMigrationOps{
				unitPath:       func(string) string { return unitPath },
				isRunning:      func(context.Context, string) (bool, error) { return false, nil },
				captureRuntime: func(context.Context, string) ([]serviceIdentityRuntimeUnitState, error) { return previousRuntime, nil },
				restoreRuntime: func(context.Context, string, []serviceIdentityRuntimeUnitState) error { return nil },
				reload:         func(context.Context) error { return nil },
			}
			err = server.recoverServiceIdentityMigrationsWithOps(context.Background(), &ops)
			if state == "external-replacement" {
				if err == nil || !errors.Is(err, errServiceIdentityRecoveryBlocked) || !strings.Contains(err.Error(), "matches neither replacement nor restored provenance") {
					t.Fatalf("recovery error = %v, want external replacement block", err)
				}
				raw, readErr := os.ReadFile(unitPath)
				if readErr != nil || string(raw) != "operator replacement" {
					t.Fatalf("external replacement changed: %q, %v", raw, readErr)
				}
				if _, statErr := os.Stat(journalPath); statErr != nil {
					t.Fatalf("blocked recovery removed journal: %v", statErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, readErr := os.ReadFile(unitPath)
			if readErr != nil || string(raw) != string(oldUnit) {
				t.Fatalf("recovered unit = %q, %v", raw, readErr)
			}
			info, statErr := os.Stat(unitPath)
			if statErr != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("recovered unit mode = %v, %v", info, statErr)
			}
			if _, statErr := os.Stat(journalPath); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("recovered journal remains: %v", statErr)
			}
		})
	}
}

func TestServiceIdentityMigrationRecoveryRejectsIncoherentRootPlanBeforeCleanup(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "root")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "operator-root")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(victim, "preserve")
	if err := os.WriteFile(marker, []byte("operator data"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldService := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root}
	targetService := oldService.Clone()
	targetService.Identity = &db.ServiceIdentity{RequestedUser: "1000", RequestedGroup: "1000", UID: 1000, GID: 1000}
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, service *db.Service) error {
		*service = *targetService.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	j, err := createServiceIdentityJournal(server.cfg.RootDir, serviceIdentityJournalHeader{
		ID: "tx-bad-root", Service: "api", Root: root,
		TargetIdentity: *targetService.Identity, PreviousService: oldService, TargetService: targetService,
		PreviousRoot: root, TargetRoot: root,
		PreviousRuntimeUnits: serviceIdentityTestRuntimeUnits(oldService, "api", false),
		RootPlan: &serviceRootMigrationPlan{
			ServiceName: "api", OldRoot: root, NewRoot: victim, Mode: serviceRootMigrationCopy,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := j.AppendPhase(serviceIdentityPhaseRecord{Phase: serviceIdentityPhaseMaterializeIntent}); err != nil {
		t.Fatal(err)
	}
	if err := j.Seal(); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	err = server.recoverServiceIdentityMigrationsWithOps(context.Background(), &serviceIdentityMigrationOps{})
	if err == nil || !strings.Contains(err.Error(), "root plan") {
		t.Fatalf("recovery error = %v, want incoherent root plan rejection", err)
	}
	raw, readErr := os.ReadFile(marker)
	if readErr != nil || string(raw) != "operator data" {
		t.Fatalf("operator data = %q, %v", raw, readErr)
	}
}

func TestServiceIdentityMigrationRecoveryPreservesTargetWhenCrashPrecedesCreationProof(t *testing.T) {
	server := newTestServer(t)
	oldRoot := filepath.Join(t.TempDir(), "old")
	targetRoot := filepath.Join(t.TempDir(), "target")
	if err := ensureDirsForRoot(oldRoot, ""); err != nil {
		t.Fatal(err)
	}
	oldService := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: oldRoot}
	targetService := oldService.Clone()
	targetService.ServiceRoot = targetRoot
	targetService.Identity = &db.ServiceIdentity{RequestedUser: "1000", RequestedGroup: "1000", UID: 1000, GID: 1000}
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, service *db.Service) error {
		*service = *oldService.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(t.TempDir(), "api.service")
	j, err := createServiceIdentityJournal(server.cfg.RootDir, serviceIdentityJournalHeader{
		ID: "tx-create-race", Service: "api", Root: targetRoot,
		TargetIdentity: *targetService.Identity, PreviousService: oldService, TargetService: targetService,
		PreviousUnitPath: unitPath, PreviousRoot: oldRoot, TargetRoot: targetRoot,
		PreviousRuntimeUnits: serviceIdentityTestRuntimeUnits(oldService, "api", false),
		RootPlan: &serviceRootMigrationPlan{
			ServiceName: "api", OldRoot: oldRoot, NewRoot: targetRoot, Mode: serviceRootMigrationCopy,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := j.AppendPhase(serviceIdentityPhaseRecord{Phase: serviceIdentityPhaseMaterializeIntent}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(targetRoot, "operator-data")
	if err := os.WriteFile(marker, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	ops := serviceIdentityMigrationOps{
		unitPath:  func(string) string { return unitPath },
		isRunning: func(context.Context, string) (bool, error) { return false, nil },
		reload:    func(context.Context) error { return nil },
	}
	err = server.recoverServiceIdentityMigrationsWithOps(context.Background(), &ops)
	if err == nil || !errors.Is(err, errServiceIdentityRecoveryBlocked) {
		t.Fatalf("recovery error = %v, want fail-closed block", err)
	}
	raw, readErr := os.ReadFile(marker)
	if readErr != nil || string(raw) != "preserve" {
		t.Fatalf("target data = %q, %v", raw, readErr)
	}
}

func TestServiceIdentityMigrationRecoveryRestoresStagedGenerationAndEnablement(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "root")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	oldService := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root, Generation: 1}
	targetService := oldService.Clone()
	targetService.Identity = &db.ServiceIdentity{RequestedUser: "1000", RequestedGroup: "1000", UID: 1000, GID: 1000}
	targetService.Artifacts = db.ArtifactStore{}
	for _, artifact := range []db.ArtifactName{db.ArtifactSystemdUnit, db.ArtifactNetNSService, db.ArtifactEnvFile} {
		targetService.Artifacts[artifact] = &db.Artifact{Refs: map[db.ArtifactRef]string{db.Gen(1): "/artifact"}}
	}
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, service *db.Service) error {
		*service = *oldService.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	unitDir := t.TempDir()
	unitPath := filepath.Join(unitDir, "api.service")
	if err := os.WriteFile(unitPath, []byte("old unit"), 0o600); err != nil {
		t.Fatal(err)
	}
	previousUnitProof, err := captureServiceIdentityPathProof(unitPath)
	if err != nil {
		t.Fatal(err)
	}
	expectedPaths, expectedUnits, err := serviceIdentityExpectedGenerationTargets(targetService, root, unitPath)
	if err != nil {
		t.Fatal(err)
	}
	const id = "tx-generation-recovery"
	stableEnv := filepath.Join(serviceEnvDirForRoot(root), "env")
	newAuxUnit := filepath.Join(unitDir, "yeet-api-ns.service")
	backups := make([]serviceIdentityGenerationBackup, 0, len(expectedPaths)-1)
	backupDir := filepath.Join(serviceIdentityMigrationBackupDir(server.cfg.RootDir, id), "generation")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range expectedPaths {
		if path == unitPath {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		original, err := captureServiceIdentityPathProof(path)
		if err != nil {
			t.Fatal(err)
		}
		backup := serviceIdentityGenerationBackup{
			Path: path, BackupPath: filepath.Join(backupDir, fmt.Sprintf("%03d", len(backups))),
			Present: original.Present, Original: original,
		}
		switch path {
		case stableEnv:
			if err := os.WriteFile(path, []byte("old env"), 0o600); err != nil {
				t.Fatal(err)
			}
			original, err = captureServiceIdentityPathProof(path)
			if err != nil {
				t.Fatal(err)
			}
			backup.Present, backup.Original = true, original
			backup.Backup, err = copyServiceIdentityProof(original, backup.BackupPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("staged env"), 0o640); err != nil {
				t.Fatal(err)
			}
		case newAuxUnit:
			if err := os.WriteFile(path, []byte("staged auxiliary unit"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		backups = append(backups, backup)
	}
	stagedProofs := make([]serviceIdentityPathProof, len(expectedPaths))
	for index, path := range expectedPaths {
		proof, err := captureServiceIdentityPathProof(path)
		if err != nil {
			t.Fatal(err)
		}
		stagedProofs[index] = proof
	}
	generationIntents := make([]serviceIdentityPathState, len(backups))
	for index, backup := range backups {
		proof, ok := serviceIdentityProofForPath(stagedProofs, backup.Path)
		if !ok {
			t.Fatalf("missing staged proof for %s", backup.Path)
		}
		generationIntents[index] = serviceIdentityStateFromProof(proof)
	}
	enablement := make([]serviceIdentityUnitEnablement, len(expectedUnits))
	for index, unit := range expectedUnits {
		enablement[index] = serviceIdentityUnitEnablement{Unit: unit, Enabled: index != 0, TargetEnabled: true}
	}
	j, err := createServiceIdentityJournal(server.cfg.RootDir, serviceIdentityJournalHeader{
		ID: id, Service: "api", Root: root,
		TargetIdentity: *targetService.Identity, PreviousService: oldService, TargetService: targetService,
		PreviousUnit: "old unit", PreviousUnitPresent: true, PreviousUnitPath: unitPath, PreviousUnitMode: 0o600,
		PreviousUnitProof: previousUnitProof,
		PreviousRoot:      root, TargetRoot: root, GenerationBackups: backups, GenerationUnits: enablement,
		PreviousRuntimeUnits: serviceIdentityTestRuntimeUnits(oldService, "api", false),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := j.AppendPhase(serviceIdentityPhaseRecord{Phase: serviceIdentityPhaseGenerationBackup}); err != nil {
		t.Fatal(err)
	}
	if err := j.AppendPhase(serviceIdentityPhaseRecord{Phase: serviceIdentityPhaseGenerationBackedUp, GenerationBackups: backups}); err != nil {
		t.Fatal(err)
	}
	if err := j.AppendPhase(serviceIdentityPhaseRecord{Phase: serviceIdentityPhaseGenerationStageIntent, GenerationIntents: generationIntents}); err != nil {
		t.Fatal(err)
	}
	if err := j.AppendPhase(serviceIdentityPhaseRecord{Phase: serviceIdentityPhaseGenerationStage, GenerationPaths: stagedProofs}); err != nil {
		t.Fatal(err)
	}
	if err := j.Seal(); err != nil {
		t.Fatal(err)
	}
	if err := j.AppendPhase(serviceIdentityPhaseRecord{Phase: serviceIdentityPhaseGenerationActivate}); err != nil {
		t.Fatal(err)
	}
	journalPath := j.Path()
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	enabled := make(map[string]bool, len(enablement))
	for _, state := range enablement {
		enabled[state.Unit] = !state.Enabled
	}
	ops := serviceIdentityMigrationOps{
		unitPath:  func(string) string { return unitPath },
		isRunning: func(context.Context, string) (bool, error) { return false, nil },
		reload:    func(context.Context) error { return nil },
		isEnabled: func(_ context.Context, unit string) (bool, error) { return enabled[unit], nil },
		enable:    func(_ context.Context, unit string) error { enabled[unit] = true; return nil },
		disable:   func(_ context.Context, unit string) error { enabled[unit] = false; return nil },
	}
	if err := server.recoverServiceIdentityMigrationsWithOps(context.Background(), &ops); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(stableEnv); err != nil || string(raw) != "old env" {
		t.Fatalf("restored env = %q, %v", raw, err)
	}
	if _, err := os.Stat(newAuxUnit); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new auxiliary unit survived recovery: %v", err)
	}
	for _, state := range enablement {
		if enabled[state.Unit] != state.Enabled {
			t.Fatalf("unit %s enabled=%t, want %t", state.Unit, enabled[state.Unit], state.Enabled)
		}
	}
	if _, err := os.Stat(journalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovered journal still exists: %v", err)
	}
}

func TestServiceIdentityMigrationRecoveryPreservesPathThatAppearsBeforeGenerationStage(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "root")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	oldService := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root, Generation: 1}
	targetService := oldService.Clone()
	targetService.Identity = &db.ServiceIdentity{RequestedUser: "1000", RequestedGroup: "1000", UID: 1000, GID: 1000}
	targetService.Artifacts = db.ArtifactStore{
		db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/artifact"}},
	}
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, service *db.Service) error {
		*service = *oldService.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(t.TempDir(), "api.service")
	expectedPaths, expectedUnits, err := serviceIdentityExpectedGenerationTargets(targetService, root, unitPath)
	if err != nil {
		t.Fatal(err)
	}
	const id = "tx-pre-stage-race"
	backups := make([]serviceIdentityGenerationBackup, 0, len(expectedPaths)-1)
	for _, path := range expectedPaths {
		if path == unitPath {
			continue
		}
		backups = append(backups, serviceIdentityGenerationBackup{Path: path, BackupPath: path + ".yeet-identity-" + id})
	}
	units := make([]serviceIdentityUnitEnablement, len(expectedUnits))
	for index, unit := range expectedUnits {
		units[index].Unit = unit
		units[index].TargetEnabled = true
	}
	j, err := createServiceIdentityJournal(server.cfg.RootDir, serviceIdentityJournalHeader{
		ID: id, Service: "api", Root: root,
		TargetIdentity: *targetService.Identity, PreviousService: oldService, TargetService: targetService,
		PreviousUnitPath: unitPath, PreviousRoot: root, TargetRoot: root,
		GenerationBackups: backups, GenerationUnits: units,
		PreviousRuntimeUnits: serviceIdentityTestRuntimeUnits(oldService, "api", false),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := j.AppendPhase(serviceIdentityPhaseRecord{Phase: serviceIdentityPhaseGenerationBackup}); err != nil {
		t.Fatal(err)
	}
	journalPath := j.Path()
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	victim := backups[0].Path
	if err := os.MkdirAll(filepath.Dir(victim), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(victim, []byte("operator data"), 0o600); err != nil {
		t.Fatal(err)
	}
	ops := serviceIdentityMigrationOps{
		unitPath:  func(string) string { return unitPath },
		isRunning: func(context.Context, string) (bool, error) { return false, nil },
		reload:    func(context.Context) error { return nil },
	}
	err = server.recoverServiceIdentityMigrationsWithOps(context.Background(), &ops)
	if err == nil || !errors.Is(err, errServiceIdentityRecoveryBlocked) {
		t.Fatalf("recovery error = %v, want fail-closed block", err)
	}
	if raw, err := os.ReadFile(victim); err != nil || string(raw) != "operator data" {
		t.Fatalf("operator path = %q, %v", raw, err)
	}
	if _, err := os.Stat(journalPath); err != nil {
		t.Fatalf("blocked recovery removed journal: %v", err)
	}
}

func assertRecoveredServiceIdentityState(t *testing.T, server *Server, unitPath, wantUnit string, wantMode os.FileMode, wantService *db.Service, wantRunning bool) {
	t.Helper()
	raw, err := os.ReadFile(unitPath)
	if err != nil || string(raw) != wantUnit {
		t.Fatalf("unit = %q, %v, want %q", raw, err, wantUnit)
	}
	info, err := os.Stat(unitPath)
	if err != nil || info.Mode().Perm() != wantMode.Perm() {
		t.Fatalf("unit mode = %v, %v, want %v", info, err, wantMode)
	}
	service, err := server.serviceView(wantService.Name)
	if err != nil || !reflect.DeepEqual(service.AsStruct(), wantService) {
		t.Fatalf("service = %#v, %v, want %#v", service.AsStruct(), err, wantService)
	}
	_ = wantRunning
}
