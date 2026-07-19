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
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
)

func TestServiceIdentityMigrationContractSupportsIdentityAndCombinedRoot(t *testing.T) {
	target := resolvedServiceIdentity{Persisted: db.ServiceIdentity{
		RequestedUser: "app", RequestedGroup: "app", UID: 1001, GID: 1001,
	}}
	requests := []serviceIdentityMigrationRequest{
		{Service: "api", Requested: "app", Target: target},
		{
			Service: "api", Requested: "app", Target: target,
			RootPlan: &serviceRootMigrationPlan{
				ServiceName: "api",
				OldRoot:     filepath.Join(t.TempDir(), "old"),
				NewRoot:     filepath.Join(t.TempDir(), "new"),
				Mode:        serviceRootMigrationCopy,
			},
		},
	}
	server := newTestServer(t)
	for _, req := range requests {
		_, _ = server.migrateServiceIdentity(context.Background(), req, io.Discard)
	}
}

func TestCleanupIncompleteServiceIdentityBackupsRemovesRandomizedCopyTemp(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.WriteFile(source, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	original, err := captureServiceIdentityPathProof(source)
	if err != nil {
		t.Fatal(err)
	}
	backupDir := filepath.Join(root, "transaction", "generation")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(backupDir, "000")
	tempPath := filepath.Join(backupDir, ".000.yeet-identity-interrupted")
	if err := os.WriteFile(tempPath, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	backups := []serviceIdentityGenerationBackup{{Path: source, BackupPath: backupPath, Present: true, Original: original}}
	if err := cleanupIncompleteServiceIdentityBackups(backups); err != nil {
		t.Fatalf("cleanupIncompleteServiceIdentityBackups: %v", err)
	}
	if _, err := os.Lstat(backupDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup dir remains after cleanup: %v", err)
	}
}

func TestCleanupIncompleteServiceIdentityBackupsRejectsUnexpectedEntry(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.WriteFile(source, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	original, err := captureServiceIdentityPathProof(source)
	if err != nil {
		t.Fatal(err)
	}
	backupDir := filepath.Join(root, "transaction", "generation")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	unexpected := filepath.Join(backupDir, "operator-data")
	if err := os.WriteFile(unexpected, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	backups := []serviceIdentityGenerationBackup{{Path: source, BackupPath: filepath.Join(backupDir, "000"), Present: true, Original: original}}
	err = cleanupIncompleteServiceIdentityBackups(backups)
	if err == nil || !strings.Contains(err.Error(), "unexpected incomplete transaction backup") {
		t.Fatalf("cleanup error = %v, want unexpected entry refusal", err)
	}
	if got, readErr := os.ReadFile(unexpected); readErr != nil || string(got) != "keep" {
		t.Fatalf("unexpected entry changed: %q, %v", got, readErr)
	}
}

func TestServiceIdentityMigrationStartDiagnosticIsBoundedAndRedacted(t *testing.T) {
	oldJournal := serviceIdentityStartJournal
	serviceIdentityStartJournal = func(context.Context, string) ([]byte, error) {
		return []byte(strings.Repeat("permission denied TOKEN=super-secret ", 600)), nil
	}
	t.Cleanup(func() { serviceIdentityStartJournal = oldJournal })

	identity := db.ServiceIdentity{RequestedUser: "app", RequestedGroup: "workers", UID: 1002, GID: 1003}
	server := newTestServer(t)
	migration := &serviceIdentityMigration{
		server:   server,
		req:      serviceIdentityMigrationRequest{Service: "api", Requested: "app:workers", Target: resolvedServiceIdentity{Persisted: identity}},
		previous: &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: "/var/lib/yeet/services/api"},
		result: serviceIdentityMigrationResult{Previous: resolvedServiceIdentity{Persisted: db.ServiceIdentity{
			RequestedUser: "root", RequestedGroup: "root",
		}}},
		journalPath: "/var/lib/yeet/migrations/service-identity/api.jsonl",
	}
	err := migration.migrationError(fmt.Errorf("%s: systemctl failed", serviceIdentityPhaseStart), nil)
	text := err.Error()
	for _, want := range []string{
		"service api failed to start as app:workers (1002:1003)",
		"systemd:", "permission denied", "TOKEN=<redacted>",
		"check service data permissions, privileged ports, devices, and absolute host paths",
		"rollback restored the old unit",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("diagnostic missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "super-secret") {
		t.Fatalf("diagnostic leaked environment value: %s", text)
	}
	if len(text) > 12*1024 {
		t.Fatalf("diagnostic length = %d, want bounded output", len(text))
	}
}

func TestServiceIdentityMigrationRechecksRecoveryInsideServiceLock(t *testing.T) {
	server := newTestServer(t)
	release := server.serviceOperationLocks.Lock("api")
	done := make(chan error, 1)
	go func() {
		_, err := server.migrateServiceIdentity(context.Background(), serviceIdentityMigrationRequest{
			Service: "api",
		}, io.Discard)
		done <- err
	}()
	select {
	case err := <-done:
		release()
		t.Fatalf("identity migration bypassed service lock: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	server.setServiceIdentityMutationBlock("api", errors.New("recovery required"))
	release()

	select {
	case err := <-done:
		if !errors.Is(err, errServiceIdentityRecoveryBlocked) {
			t.Fatalf("migrateServiceIdentity error = %v, want identity recovery block", err)
		}
	case <-time.After(time.Second):
		t.Fatal("identity migration did not resume after service lock release")
	}
}

func TestFailedFirstNativeIdentityInstallRestoresAbsenceAndRetrySelectsManagedIdentity(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "api")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	observed := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": observed.Clone()}}); err != nil {
		t.Fatal(err)
	}
	identity := db.ServiceIdentity{
		RequestedUser: strconv.Itoa(os.Geteuid()), RequestedGroup: strconv.Itoa(os.Getegid()),
		UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()),
	}
	target := observed.Clone()
	target.Identity = &identity
	unitPath := filepath.Join(t.TempDir(), "api.service")
	running := false
	ops := serviceIdentityMigrationOps{
		unitPath:  func(string) string { return unitPath },
		isRunning: func(context.Context, string) (bool, error) { return running, nil },
		start:     func(context.Context, string) error { running = true; return nil },
		stop:      func(context.Context, string) error { running = false; return nil },
		inspect: func(context.Context, serviceIdentityInspectionRequest) (serviceIdentityInspection, error) {
			return serviceIdentityInspection{}, nil
		},
		apply:  func(serviceIdentityInspection, *serviceIdentityJournal) error { return nil },
		reload: func(context.Context) error { return nil },
		verify: func(context.Context, serviceIdentityMigrationVerification) error {
			return errors.New("injected first-install health failure")
		},
	}
	_, err := server.migrateServiceIdentity(context.Background(), serviceIdentityMigrationRequest{
		Service: "api", Requested: identity.RequestedUser + ":" + identity.RequestedGroup,
		Target: resolvedServiceIdentity{Persisted: identity}, TargetService: target,
		ReplacementUnit: "[Service]\nUser=" + identity.RequestedUser + "\nGroup=" + identity.RequestedGroup + "\n",
		StartNew:        true, PredecessorAbsent: true, ops: &ops,
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "rollback restored") {
		t.Fatalf("migration error = %v, want fully rolled-back first install", err)
	}
	if _, err := server.serviceView("api"); !errors.Is(err, errServiceNotFound) {
		t.Fatalf("failed first install left a database service: %v", err)
	}
	if _, err := os.Stat(unitPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed first install left a primary unit: %v", err)
	}

	want := resolvedServiceIdentity{Persisted: db.ServiceIdentity{
		RequestedUser: managedServiceUser, RequestedGroup: managedServiceUser, UID: 991, GID: 992,
	}}
	retry := &FileInstaller{
		s: server, existingService: First(server.serviceView("api")),
		ensureManagedServiceAccount: func() (resolvedServiceIdentity, error) { return want, nil },
	}
	if err := retry.resolveNativeInstallIdentity(); err != nil {
		t.Fatal(err)
	}
	if !retry.newNativeIdentity || retry.resolvedIdentity != want {
		t.Fatalf("retry identity = %#v, new=%t, want managed %#v", retry.resolvedIdentity, retry.newNativeIdentity, want)
	}
}

func TestServiceIdentityMigrationSuccessAndRollbackMatrix(t *testing.T) {
	phases := []string{
		serviceIdentityPhaseJournal,
		serviceIdentityPhaseStop,
		serviceIdentityPhaseSnapshot,
		serviceIdentityPhaseMaterializeIntent,
		serviceIdentityPhaseMaterialize,
		serviceIdentityPhaseInventorySeal,
		serviceIdentityPhaseOwnership,
		serviceIdentityPhaseUnitWrite,
		serviceIdentityPhaseDaemonReload,
		serviceIdentityPhaseGeneration,
		serviceIdentityPhaseStart,
		serviceIdentityPhaseVerify,
		serviceIdentityPhaseDBCommit,
		serviceIdentityPhaseComplete,
	}
	for _, failPhase := range append([]string{""}, phases...) {
		name := failPhase
		if name == "" {
			name = "success"
		}
		t.Run(name, func(t *testing.T) {
			server := newTestServer(t)
			oldRoot := filepath.Join(t.TempDir(), "old")
			newRoot := filepath.Join(t.TempDir(), "new")
			if err := ensureDirsForRoot(oldRoot, ""); err != nil {
				t.Fatal(err)
			}
			oldService := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, Generation: 2, LatestGeneration: 2}
			if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, service *db.Service) error {
				*service = *oldService.Clone()
				service.ServiceRoot = oldRoot
				return nil
			}); err != nil {
				t.Fatal(err)
			}

			unitPath := filepath.Join(t.TempDir(), "api.service")
			oldUnit := "[Service]\nUser=root\nGroup=root\nWorkingDirectory=" + oldRoot + "\n"
			requestedUser := strconv.Itoa(os.Geteuid())
			requestedGroup := strconv.Itoa(os.Getegid())
			newUnit := "[Service]\nUser=" + requestedUser + "\nGroup=" + requestedGroup + "\nWorkingDirectory=" + filepath.Join(newRoot, "data") + "\n"
			if err := os.WriteFile(unitPath, []byte(oldUnit), 0o600); err != nil {
				t.Fatal(err)
			}

			running := true
			ownershipChanged := false
			materialized := false
			ops := serviceIdentityMigrationOps{
				phase: func(phase string) error {
					if phase == failPhase {
						return errors.New("injected " + phase)
					}
					return nil
				},
				unitPath:  func(string) string { return unitPath },
				isRunning: func(context.Context, string) (bool, error) { return running, nil },
				stop:      func(context.Context, string) error { running = false; return nil },
				start:     func(context.Context, string) error { running = true; return nil },
				snapshot: func(_ context.Context, service *db.Service) (string, error) {
					if !materialized {
						return "", errors.New("snapshot ran before target materialization")
					}
					if service.ServiceRootZFS != "tank/services/api-new" || filepath.Clean(service.ServiceRoot) != filepath.Clean(newRoot) {
						return "", errors.New("snapshot did not target the materialized root")
					}
					return "tank/services/api-new@identity", nil
				},
				materialize: func(ctx context.Context, plan serviceRootMigrationPlan, w io.Writer) (bool, error) {
					materialized = true
					return false, ensureDirsForRoot(plan.NewRoot, "")
				},
				discardRoot: func(context.Context, serviceRootMigrationPlan, bool) error {
					materialized = false
					return os.RemoveAll(newRoot)
				},
				inspect: func(context.Context, serviceIdentityInspectionRequest) (serviceIdentityInspection, error) {
					return serviceIdentityInspection{}, nil
				},
				apply: func(serviceIdentityInspection, *serviceIdentityJournal) error {
					ownershipChanged = true
					return nil
				},
				restore: func(string) error { ownershipChanged = false; return nil },
				reload:  func(context.Context) error { return nil },
				verify:  func(context.Context, serviceIdentityMigrationVerification) error { return nil },
			}
			target := resolvedServiceIdentity{Persisted: db.ServiceIdentity{
				RequestedUser: requestedUser, RequestedGroup: requestedGroup, UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()),
			}}
			plan := &serviceRootMigrationPlan{
				ServiceName: "api", OldRoot: oldRoot, NewRoot: newRoot,
				NewRootZFS: "tank/services/api-new", Mode: serviceRootMigrationCopy,
			}
			targetService := oldService.Clone()
			targetService.ServiceRoot = oldRoot
			targetService.Generation = 3
			targetService.LatestGeneration = 3
			generationInstalled := false
			result, err := server.migrateServiceIdentity(context.Background(), serviceIdentityMigrationRequest{
				Service: "api", Requested: requestedUser + ":" + requestedGroup, Target: target, RootPlan: plan,
				ReplacementUnit: newUnit, TargetService: targetService,
				InstallGeneration: func(context.Context) error {
					generationInstalled = true
					return nil
				},
				ops: &ops,
			}, io.Discard)

			if failPhase == "" {
				if err != nil {
					t.Fatalf("migrateServiceIdentity: %v", err)
				}
				if result.Previous.Persisted.UID != 0 || result.Current.Persisted != target.Persisted || !result.Restarted || result.Root != newRoot {
					t.Fatalf("result = %#v", result)
				}
				assertServiceIdentityMigrationState(t, server, unitPath, newUnit, target.Persisted, newRoot, 3)
				if !ownershipChanged || !materialized || !generationInstalled {
					t.Fatalf("success state ownership=%t materialized=%t generation=%t", ownershipChanged, materialized, generationInstalled)
				}
				return
			}

			if err == nil || !strings.Contains(err.Error(), failPhase) {
				t.Fatalf("migrateServiceIdentity error = %v, want phase %q", err, failPhase)
			}
			assertServiceIdentityMigrationState(t, server, unitPath, oldUnit, db.ServiceIdentity{}, oldRoot, 2)
			info, statErr := os.Stat(unitPath)
			if statErr != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("rolled-back unit mode = %v, %v, want 0600", info, statErr)
			}
			if ownershipChanged || materialized {
				t.Fatalf("rollback state ownership=%t materialized=%t", ownershipChanged, materialized)
			}
		})
	}
}

func TestServiceIdentityMigrationLegacyRuntimeCleanupIsReversibleUntilComplete(t *testing.T) {
	for _, failUnit := range []bool{false, true} {
		name := "commit"
		if failUnit {
			name = "rollback"
		}
		t.Run(name, func(t *testing.T) {
			server := newTestServer(t)
			root := filepath.Join(t.TempDir(), "root")
			if err := ensureDirsForRoot(root, ""); err != nil {
				t.Fatal(err)
			}
			legacyPath := filepath.Join(serviceRunDirForRoot(root), "api")
			if err := os.WriteFile(legacyPath, []byte("legacy-runtime"), 0o751); err != nil {
				t.Fatal(err)
			}
			catchPath := filepath.Join(serviceRunDirForRoot(root), CatchService)
			if err := os.WriteFile(catchPath, []byte("catch-runner"), 0o700); err != nil {
				t.Fatal(err)
			}
			old := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root}
			if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, current *db.Service) error {
				*current = *old.Clone()
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			unitPath := filepath.Join(t.TempDir(), "api.service")
			oldUnit := "[Service]\nUser=root\nGroup=wheel\nWorkingDirectory=" + serviceDataDirForRoot(root) + "\n"
			if err := os.WriteFile(unitPath, []byte(oldUnit), 0o644); err != nil {
				t.Fatal(err)
			}
			identity := db.ServiceIdentity{
				RequestedUser: strconv.Itoa(os.Geteuid()), RequestedGroup: strconv.Itoa(os.Getegid()),
				UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()),
			}
			ops := serviceIdentityMigrationOps{
				unitPath:  func(string) string { return unitPath },
				isRunning: func(context.Context, string) (bool, error) { return false, nil },
				snapshot:  func(context.Context, *db.Service) (string, error) { return "", nil },
				inspect: func(context.Context, serviceIdentityInspectionRequest) (serviceIdentityInspection, error) {
					return serviceIdentityInspection{}, nil
				},
				apply:   func(serviceIdentityInspection, *serviceIdentityJournal) error { return nil },
				restore: func(string) error { return nil },
				reload:  func(context.Context) error { return nil },
				verify:  func(context.Context, serviceIdentityMigrationVerification) error { return nil },
			}
			if failUnit {
				ops.writeUnit = func(string, []byte, os.FileMode, serviceIdentityPathProof, uint32, uint32) error {
					return errors.New("unit write failed after backup")
				}
			}
			_, err := server.migrateServiceIdentity(context.Background(), serviceIdentityMigrationRequest{
				Service: "api", Requested: identity.RequestedUser,
				Target:          resolvedServiceIdentity{Persisted: identity},
				ReplacementUnit: "[Service]\nUser=" + identity.RequestedUser + "\nGroup=" + identity.RequestedGroup + "\nWorkingDirectory=" + serviceDataDirForRoot(root) + "\n",
				ops:             &ops,
			}, io.Discard)
			if failUnit {
				if err == nil || !strings.Contains(err.Error(), "rollback restored") {
					t.Fatalf("migration error = %v", err)
				}
				raw, readErr := os.ReadFile(legacyPath)
				if readErr != nil || string(raw) != "legacy-runtime" {
					t.Fatalf("restored legacy runtime = %q, %v", raw, readErr)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if _, statErr := os.Stat(legacyPath); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("legacy runtime still present after commit: %v", statErr)
				}
			}
			if raw, readErr := os.ReadFile(catchPath); readErr != nil || string(raw) != "catch-runner" {
				t.Fatalf("catch runner changed = %q, %v", raw, readErr)
			}
			entries, readErr := os.ReadDir(serviceRunDirForRoot(root))
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".yeet-identity-backup-") {
					t.Fatalf("backup directory retained after verified outcome: %s", entry.Name())
				}
			}
		})
	}
}

func assertServiceIdentityMigrationState(t *testing.T, server *Server, unitPath, wantUnit string, wantIdentity db.ServiceIdentity, wantRoot string, wantGeneration int) {
	t.Helper()
	unit, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(unit) != wantUnit {
		t.Fatalf("unit = %q, want %q", unit, wantUnit)
	}
	service, err := server.serviceView("api")
	if err != nil {
		t.Fatal(err)
	}
	gotIdentity := db.ServiceIdentity{}
	if service.Identity().Valid() {
		gotIdentity = *service.Identity().AsStruct()
	}
	if gotIdentity != wantIdentity || filepath.Clean(server.serviceRootFromView(service)) != filepath.Clean(wantRoot) || service.Generation() != wantGeneration {
		t.Fatalf("service identity/root/gen = %#v %q %d, want %#v %q %d", gotIdentity, server.serviceRootFromView(service), service.Generation(), wantIdentity, wantRoot, wantGeneration)
	}
}

func TestServiceIdentityMigrationRollsBackPartialSideEffects(t *testing.T) {
	for _, failure := range []string{"materialize", "ownership", "unit", "start", "database"} {
		t.Run(failure, func(t *testing.T) {
			server := newTestServer(t)
			oldRoot := filepath.Join(t.TempDir(), "old")
			newRoot := filepath.Join(t.TempDir(), "new")
			if err := ensureDirsForRoot(oldRoot, ""); err != nil {
				t.Fatal(err)
			}
			oldService := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: oldRoot, Generation: 2, LatestGeneration: 2}
			if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, service *db.Service) error {
				*service = *oldService.Clone()
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			unitPath := filepath.Join(t.TempDir(), "api.service")
			oldUnit := "[Service]\nUser=root\nGroup=root\nWorkingDirectory=" + filepath.Join(oldRoot, "data") + "\n"
			newUnit := "[Service]\nUser=" + strconv.Itoa(os.Geteuid()) + "\nGroup=" + strconv.Itoa(os.Getegid()) + "\nWorkingDirectory=" + filepath.Join(newRoot, "data") + "\n"
			if err := os.WriteFile(unitPath, []byte(oldUnit), 0o600); err != nil {
				t.Fatal(err)
			}
			running := true
			ownershipChanged := false
			materialized := false
			startCalls := 0
			ops := serviceIdentityMigrationOps{
				unitPath:  func(string) string { return unitPath },
				isRunning: func(context.Context, string) (bool, error) { return running, nil },
				stop:      func(context.Context, string) error { running = false; return nil },
				start: func(context.Context, string) error {
					startCalls++
					running = true
					if failure == "start" && startCalls == 1 {
						return errors.New("partially started")
					}
					return nil
				},
				snapshot: func(context.Context, *db.Service) (string, error) { return "", nil },
				materialize: func(context.Context, serviceRootMigrationPlan, io.Writer) (bool, error) {
					materialized = true
					if err := ensureDirsForRoot(newRoot, ""); err != nil {
						return false, err
					}
					if failure == "materialize" {
						return true, errors.New("partial materialize")
					}
					return false, nil
				},
				discardRoot: func(context.Context, serviceRootMigrationPlan, bool) error {
					materialized = false
					return os.RemoveAll(newRoot)
				},
				inspect: func(context.Context, serviceIdentityInspectionRequest) (serviceIdentityInspection, error) {
					return serviceIdentityInspection{}, nil
				},
				apply: func(serviceIdentityInspection, *serviceIdentityJournal) error {
					ownershipChanged = true
					if failure == "ownership" {
						return errors.New("partial ownership")
					}
					return nil
				},
				restore: func(string) error { ownershipChanged = false; return nil },
				reload:  func(context.Context) error { return nil },
				verify:  func(context.Context, serviceIdentityMigrationVerification) error { return nil },
			}
			if failure == "unit" {
				ops.writeUnit = func(path string, raw []byte, mode os.FileMode, expected serviceIdentityPathProof, uid, gid uint32) error {
					if err := writeServiceIdentityUnitAtomicallyExpected(path, raw, mode, expected, uid, gid); err != nil {
						return err
					}
					return errors.New("unit directory sync failed")
				}
			}
			targetIdentity := db.ServiceIdentity{
				RequestedUser: strconv.Itoa(os.Geteuid()), RequestedGroup: strconv.Itoa(os.Getegid()),
				UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()),
			}
			targetService := oldService.Clone()
			targetService.Generation = 3
			targetService.LatestGeneration = 3
			if failure == "database" {
				ops.commit = func(_, target *db.Service) error {
					_, err := server.cfg.DB.MutateData(func(data *db.Data) error {
						data.Services["api"] = target.Clone()
						return nil
					})
					return errors.Join(err, errors.New("database fsync failed"))
				}
			}
			var rootPlan *serviceRootMigrationPlan
			if failure == "materialize" {
				rootPlan = &serviceRootMigrationPlan{ServiceName: "api", OldRoot: oldRoot, NewRoot: newRoot, Mode: serviceRootMigrationCopy}
			} else {
				newRoot = oldRoot
				newUnit = "[Service]\nUser=" + targetIdentity.RequestedUser + "\nGroup=" + targetIdentity.RequestedGroup + "\nWorkingDirectory=" + filepath.Join(oldRoot, "data") + "\n"
			}
			_, err := server.migrateServiceIdentity(context.Background(), serviceIdentityMigrationRequest{
				Service: "api", Requested: targetIdentity.RequestedUser + ":" + targetIdentity.RequestedGroup,
				Target: resolvedServiceIdentity{Persisted: targetIdentity}, RootPlan: rootPlan,
				ReplacementUnit: newUnit, TargetService: targetService, ops: &ops,
			}, io.Discard)
			if err == nil {
				t.Fatalf("%s failure unexpectedly succeeded", failure)
			}
			assertServiceIdentityMigrationState(t, server, unitPath, oldUnit, db.ServiceIdentity{}, oldRoot, 2)
			if !running || ownershipChanged || materialized {
				t.Fatalf("rollback running=%t ownership=%t materialized=%t", running, ownershipChanged, materialized)
			}
		})
	}
}

func TestServiceIdentityMigrationRollbackObservesAmbiguousSystemdResults(t *testing.T) {
	for _, failure := range []string{"initial-stop", "replacement-start-and-stop"} {
		t.Run(failure, func(t *testing.T) {
			server := newTestServer(t)
			root := filepath.Join(t.TempDir(), "root")
			if err := ensureDirsForRoot(root, ""); err != nil {
				t.Fatal(err)
			}
			old := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root}
			if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, current *db.Service) error {
				*current = *old.Clone()
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			unitPath := filepath.Join(t.TempDir(), "api.service")
			oldUnit := "[Service]\nUser=root\nGroup=wheel\nWorkingDirectory=" + serviceDataDirForRoot(root) + "\n"
			if err := os.WriteFile(unitPath, []byte(oldUnit), 0o644); err != nil {
				t.Fatal(err)
			}
			identity := db.ServiceIdentity{
				RequestedUser: strconv.Itoa(os.Geteuid()), RequestedGroup: strconv.Itoa(os.Getegid()),
				UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()),
			}
			running := true
			stopCalls := 0
			startCalls := 0
			removed := false
			ops := serviceIdentityMigrationOps{
				unitPath:  func(string) string { return unitPath },
				isRunning: func(context.Context, string) (bool, error) { return running, nil },
				stop: func(context.Context, string) error {
					stopCalls++
					running = false
					if failure == "initial-stop" && stopCalls == 1 || failure == "replacement-start-and-stop" && stopCalls == 2 {
						return errors.New("systemctl result lost after stop")
					}
					return nil
				},
				start: func(context.Context, string) error {
					startCalls++
					running = true
					if failure == "replacement-start-and-stop" && startCalls == 1 {
						return errors.New("systemctl result lost after start")
					}
					return nil
				},
				snapshot: func(context.Context, *db.Service) (string, error) { return "", nil },
				inspect: func(context.Context, serviceIdentityInspectionRequest) (serviceIdentityInspection, error) {
					return serviceIdentityInspection{}, nil
				},
				apply:  func(serviceIdentityInspection, *serviceIdentityJournal) error { return nil },
				reload: func(context.Context) error { return nil },
				verify: func(context.Context, serviceIdentityMigrationVerification) error { return nil },
				remove: func(string) error { removed = true; return nil },
			}
			_, err := server.migrateServiceIdentity(context.Background(), serviceIdentityMigrationRequest{
				Service: "api", Requested: identity.RequestedUser,
				Target:          resolvedServiceIdentity{Persisted: identity},
				ReplacementUnit: "[Service]\nUser=" + identity.RequestedUser + "\nGroup=" + identity.RequestedGroup + "\nWorkingDirectory=" + serviceDataDirForRoot(root) + "\n",
				ops:             &ops,
			}, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "rollback restored") {
				t.Fatalf("migration error = %v, want verified rollback", err)
			}
			if !running || !removed {
				t.Fatalf("rollback running=%t journalRemoved=%t", running, removed)
			}
		})
	}
}

func TestServiceIdentityMigrationRollbackDoesNotRestartAfterCriticalRestoreFailure(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "root")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	old := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root}
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, current *db.Service) error {
		*current = *old.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(t.TempDir(), "api.service")
	oldUnit := "[Service]\nUser=root\nGroup=root\nWorkingDirectory=" + serviceDataDirForRoot(root) + "\n"
	if err := os.WriteFile(unitPath, []byte(oldUnit), 0o644); err != nil {
		t.Fatal(err)
	}
	identity := db.ServiceIdentity{
		RequestedUser: strconv.Itoa(os.Geteuid()), RequestedGroup: strconv.Itoa(os.Getegid()),
		UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()),
	}
	running := true
	startCalls := 0
	ops := serviceIdentityMigrationOps{
		unitPath:  func(string) string { return unitPath },
		isRunning: func(context.Context, string) (bool, error) { return running, nil },
		stop:      func(context.Context, string) error { running = false; return nil },
		start: func(context.Context, string) error {
			startCalls++
			running = true
			return nil
		},
		snapshot: func(context.Context, *db.Service) (string, error) { return "", nil },
		inspect: func(context.Context, serviceIdentityInspectionRequest) (serviceIdentityInspection, error) {
			return serviceIdentityInspection{}, nil
		},
		apply:   func(serviceIdentityInspection, *serviceIdentityJournal) error { return nil },
		restore: func(string) error { return errors.New("ownership restore failed") },
		reload:  func(context.Context) error { return nil },
		verify: func(context.Context, serviceIdentityMigrationVerification) error {
			return errors.New("target unhealthy")
		},
	}
	_, err := server.migrateServiceIdentity(context.Background(), serviceIdentityMigrationRequest{
		Service: "api", Requested: identity.RequestedUser + ":" + identity.RequestedGroup,
		Target:          resolvedServiceIdentity{Persisted: identity},
		ReplacementUnit: "[Service]\nUser=" + identity.RequestedUser + "\nGroup=" + identity.RequestedGroup + "\nWorkingDirectory=" + serviceDataDirForRoot(root) + "\n",
		StartNew:        true, ops: &ops,
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "ownership restore failed") {
		t.Fatalf("migration error = %v, want ownership restore failure", err)
	}
	if running {
		t.Fatal("old service restarted after critical rollback restoration failed")
	}
	if startCalls != 1 {
		t.Fatalf("start calls = %d, want only target start", startCalls)
	}
}

func TestServiceIdentityMigrationCompletedCleanupFailureDoesNotRollback(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "root")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	oldService := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root}
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, service *db.Service) error {
		*service = *oldService.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(t.TempDir(), "api.service")
	oldUnit := "[Service]\nUser=root\nGroup=root\nWorkingDirectory=" + filepath.Join(root, "data") + "\n"
	targetIdentity := db.ServiceIdentity{RequestedUser: strconv.Itoa(os.Geteuid()), RequestedGroup: strconv.Itoa(os.Getegid()), UID: uint32(os.Geteuid()), GID: uint32(os.Getegid())}
	newUnit := "[Service]\nUser=" + targetIdentity.RequestedUser + "\nGroup=" + targetIdentity.RequestedGroup + "\nWorkingDirectory=" + filepath.Join(root, "data") + "\n"
	if err := os.WriteFile(unitPath, []byte(oldUnit), 0o644); err != nil {
		t.Fatal(err)
	}
	ops := serviceIdentityMigrationOps{
		unitPath:  func(string) string { return unitPath },
		isRunning: func(context.Context, string) (bool, error) { return false, nil },
		snapshot:  func(context.Context, *db.Service) (string, error) { return "", nil },
		inspect: func(context.Context, serviceIdentityInspectionRequest) (serviceIdentityInspection, error) {
			return serviceIdentityInspection{}, nil
		},
		apply:   func(serviceIdentityInspection, *serviceIdentityJournal) error { return nil },
		restore: func(string) error { return errors.New("must not rollback") },
		reload:  func(context.Context) error { return nil },
		verify:  func(context.Context, serviceIdentityMigrationVerification) error { return nil },
		remove:  func(string) error { return errors.New("journal unlink failed") },
	}
	result, err := server.migrateServiceIdentity(context.Background(), serviceIdentityMigrationRequest{
		Service: "api", Requested: targetIdentity.RequestedUser + ":" + targetIdentity.RequestedGroup,
		Target: resolvedServiceIdentity{Persisted: targetIdentity}, ReplacementUnit: newUnit, ops: &ops,
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "committed") || strings.Contains(err.Error(), "rollback restored") {
		t.Fatalf("cleanup error = %v", err)
	}
	if result.Current.Persisted != targetIdentity {
		t.Fatalf("result = %#v, want committed target", result)
	}
	assertServiceIdentityMigrationState(t, server, unitPath, newUnit, targetIdentity, root, 0)
}

func TestServiceIdentityMigrationIdentityOnlyVariants(t *testing.T) {
	currentNumeric := db.ServiceIdentity{
		RequestedUser: strconv.Itoa(os.Geteuid()), RequestedGroup: strconv.Itoa(os.Getegid()),
		UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()),
	}
	otherNumeric := db.ServiceIdentity{
		RequestedUser: strconv.Itoa(os.Geteuid() + 10000), RequestedGroup: strconv.Itoa(os.Getegid() + 10000),
		UID: uint32(os.Geteuid() + 10000), GID: uint32(os.Getegid() + 10000),
	}
	explicitRootResolved, err := resolveServiceIdentity("root")
	if err != nil {
		t.Fatal(err)
	}
	explicitRoot := explicitRootResolved.Persisted
	tests := []struct {
		name        string
		previous    *db.ServiceIdentity
		target      db.ServiceIdentity
		running     bool
		wantRestart bool
		wantActions bool
	}{
		{name: "legacy root to managed-like numeric", target: currentNumeric, running: true, wantRestart: true, wantActions: true},
		{name: "operator A to B", previous: &otherNumeric, target: currentNumeric, running: true, wantRestart: true, wantActions: true},
		{name: "legacy nil to explicit root persists intent", target: explicitRoot, running: false, wantActions: true},
		{name: "explicit root", previous: &currentNumeric, target: explicitRoot, running: false, wantActions: true},
		{name: "stopped stays stopped", target: currentNumeric, running: false, wantActions: true},
		{name: "same identity no-op", previous: &currentNumeric, target: currentNumeric, running: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(t)
			root := filepath.Join(t.TempDir(), "root")
			if err := ensureDirsForRoot(root, ""); err != nil {
				t.Fatal(err)
			}
			service := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root, Identity: tt.previous}
			if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, current *db.Service) error {
				*current = *service.Clone()
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			unitPath := filepath.Join(t.TempDir(), "api.service")
			previousEffective := effectiveServiceIdentity(service.View()).Persisted
			oldUnit := "[Service]\nUser=" + previousEffective.RequestedUser + "\nGroup=" + previousEffective.RequestedGroup + "\nWorkingDirectory=" + filepath.Join(root, "data") + "\n"
			if err := os.WriteFile(unitPath, []byte(oldUnit), 0o644); err != nil {
				t.Fatal(err)
			}
			running := tt.running
			starts := 0
			ownershipCalls := 0
			ops := serviceIdentityMigrationOps{
				unitPath:  func(string) string { return unitPath },
				isRunning: func(context.Context, string) (bool, error) { return running, nil },
				stop:      func(context.Context, string) error { running = false; return nil },
				start:     func(context.Context, string) error { starts++; running = true; return nil },
				snapshot:  func(context.Context, *db.Service) (string, error) { return "", nil },
				inspect: func(context.Context, serviceIdentityInspectionRequest) (serviceIdentityInspection, error) {
					return serviceIdentityInspection{}, nil
				},
				apply:   func(serviceIdentityInspection, *serviceIdentityJournal) error { ownershipCalls++; return nil },
				restore: func(string) error { return nil },
				reload:  func(context.Context) error { return nil },
				verify:  func(context.Context, serviceIdentityMigrationVerification) error { return nil },
			}
			result, err := server.migrateServiceIdentity(context.Background(), serviceIdentityMigrationRequest{
				Service: "api", Requested: tt.target.RequestedUser + ":" + tt.target.RequestedGroup,
				Target: resolvedServiceIdentity{Persisted: tt.target}, ops: &ops,
			}, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			if result.Restarted != tt.wantRestart || running != tt.running {
				t.Fatalf("result/running = %#v %t", result, running)
			}
			if (ownershipCalls != 0) != tt.wantActions {
				t.Fatalf("ownership calls = %d, want actions %t", ownershipCalls, tt.wantActions)
			}
			if tt.wantRestart && starts != 1 {
				t.Fatalf("starts = %d, want 1", starts)
			}
			view, err := server.serviceView("api")
			if err != nil {
				t.Fatal(err)
			}
			if !view.Identity().Valid() || *view.Identity().AsStruct() != tt.target {
				t.Fatalf("identity = %#v, want %#v", view.Identity().AsStruct(), tt.target)
			}
		})
	}
}

func TestServiceIdentityMigrationStartNewStagesThenStartsAndCommitsOnce(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "root")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	previous := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root}
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, current *db.Service) error {
		*current = *previous.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	identity := db.ServiceIdentity{
		RequestedUser: strconv.Itoa(os.Geteuid()), RequestedGroup: strconv.Itoa(os.Getegid()),
		UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()),
	}
	target := previous.Clone()
	target.Identity = &identity
	target.Generation = 1
	target.LatestGeneration = 1
	unitPath := filepath.Join(t.TempDir(), "api.service")
	running := false
	stopCalls := 0
	startCalls := 0
	stageCalls := 0
	ops := serviceIdentityMigrationOps{
		unitPath:  func(string) string { return unitPath },
		isRunning: func(context.Context, string) (bool, error) { return running, nil },
		stop:      func(context.Context, string) error { stopCalls++; running = false; return nil },
		start:     func(context.Context, string) error { startCalls++; running = true; return nil },
		snapshot:  func(context.Context, *db.Service) (string, error) { return "", nil },
		inspect: func(context.Context, serviceIdentityInspectionRequest) (serviceIdentityInspection, error) {
			return serviceIdentityInspection{}, nil
		},
		apply:  func(serviceIdentityInspection, *serviceIdentityJournal) error { return nil },
		reload: func(context.Context) error { return nil },
		verify: func(_ context.Context, check serviceIdentityMigrationVerification) error {
			if !check.WasRunning || !running {
				return errors.New("new service was not running at verification")
			}
			return nil
		},
	}
	result, err := server.migrateServiceIdentity(context.Background(), serviceIdentityMigrationRequest{
		Service: "api", Requested: identity.RequestedUser, Target: resolvedServiceIdentity{Persisted: identity},
		TargetService: target, StartNew: true,
		ReplacementUnit: "[Service]\nUser=" + identity.RequestedUser + "\nGroup=" + identity.RequestedGroup + "\nWorkingDirectory=" + serviceDataDirForRoot(root) + "\n",
		InstallGeneration: func(context.Context) error {
			stageCalls++
			current, getErr := server.serviceView("api")
			if getErr != nil {
				return getErr
			}
			if !reflect.DeepEqual(current.AsStruct(), previous) {
				return errors.New("database changed before generation stage")
			}
			return nil
		},
		ops: &ops,
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if stopCalls != 0 || startCalls != 1 || stageCalls != 1 || !running || result.Restarted {
		t.Fatalf("start-new calls stop=%d start=%d stage=%d running=%t result=%#v", stopCalls, startCalls, stageCalls, running, result)
	}
	current, err := server.serviceView("api")
	if err != nil || !reflect.DeepEqual(current.AsStruct(), target) {
		t.Fatalf("committed service = %#v, %v, want %#v", current.AsStruct(), err, target)
	}
}

func TestServiceIdentityMigrationRollsBackStagedGenerationBeforeReplacingJournaledInodes(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "root")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	old := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root}
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, current *db.Service) error {
		*current = *old.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stableEnv := filepath.Join(serviceEnvDirForRoot(root), "env")
	if err := os.WriteFile(stableEnv, []byte("old-env"), 0o600); err != nil {
		t.Fatal(err)
	}
	auxUnit := filepath.Join(t.TempDir(), "yeet-api-ns.service")
	unitPath := filepath.Join(t.TempDir(), "api.service")
	oldUnit := "[Service]\nUser=root\nGroup=wheel\nWorkingDirectory=" + serviceDataDirForRoot(root) + "\n"
	if err := os.WriteFile(unitPath, []byte(oldUnit), 0o644); err != nil {
		t.Fatal(err)
	}
	identity := db.ServiceIdentity{
		RequestedUser: strconv.Itoa(os.Geteuid()), RequestedGroup: strconv.Itoa(os.Getegid()),
		UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()),
	}
	running := true
	enabled := map[string]bool{"api.service": false, "yeet-api-ns.service": true}
	restoreSawStagedInode := false
	startCalls := 0
	ops := serviceIdentityMigrationOps{
		unitPath:  func(string) string { return unitPath },
		isRunning: func(context.Context, string) (bool, error) { return running, nil },
		stop:      func(context.Context, string) error { running = false; return nil },
		start: func(context.Context, string) error {
			startCalls++
			running = true
			if startCalls == 1 {
				return errors.New("start failed after side effect")
			}
			return nil
		},
		snapshot: func(context.Context, *db.Service) (string, error) { return "", nil },
		inspect: func(context.Context, serviceIdentityInspectionRequest) (serviceIdentityInspection, error) {
			return serviceIdentityInspection{}, nil
		},
		apply: func(serviceIdentityInspection, *serviceIdentityJournal) error { return nil },
		restore: func(string) error {
			raw, err := os.ReadFile(stableEnv)
			if err != nil {
				return err
			}
			restoreSawStagedInode = string(raw) == "new-env"
			if !restoreSawStagedInode {
				return fmt.Errorf("ownership restore saw %q, want staged inode before backup restoration", raw)
			}
			return nil
		},
		reload:    func(context.Context) error { return nil },
		verify:    func(context.Context, serviceIdentityMigrationVerification) error { return nil },
		isEnabled: func(_ context.Context, unit string) (bool, error) { return enabled[unit], nil },
		enable:    func(_ context.Context, unit string) error { enabled[unit] = true; return nil },
		disable:   func(_ context.Context, unit string) error { enabled[unit] = false; return nil },
	}
	_, err := server.migrateServiceIdentity(context.Background(), serviceIdentityMigrationRequest{
		Service: "api", Requested: identity.RequestedUser, Target: resolvedServiceIdentity{Persisted: identity},
		ReplacementUnit: "[Service]\nUser=" + identity.RequestedUser + "\nGroup=" + identity.RequestedGroup + "\nWorkingDirectory=" + serviceDataDirForRoot(root) + "\n",
		GenerationPaths: []string{stableEnv, auxUnit},
		GenerationUnits: []string{"api.service", "yeet-api-ns.service"},
		StageGeneration: func(context.Context) error {
			if err := os.WriteFile(stableEnv, []byte("new-env"), 0o640); err != nil {
				return err
			}
			return os.WriteFile(auxUnit, []byte("new auxiliary unit"), 0o644)
		},
		ops: &ops,
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "rollback restored") {
		t.Fatalf("migration error = %v", err)
	}
	if !restoreSawStagedInode {
		t.Fatal("ownership rollback did not run before generation backup restoration")
	}
	if raw, readErr := os.ReadFile(stableEnv); readErr != nil || string(raw) != "old-env" {
		t.Fatalf("stable env after rollback = %q, %v", raw, readErr)
	}
	if _, statErr := os.Stat(auxUnit); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("new auxiliary unit survived rollback: %v", statErr)
	}
	if enabled["api.service"] || !enabled["yeet-api-ns.service"] {
		t.Fatalf("enablement after rollback = %#v", enabled)
	}
}

func TestServiceIdentityMigrationPartialGenerationStageFailsClosedWithoutDeletingPaths(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "root")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	old := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root}
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, current *db.Service) error {
		*current = *old.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(t.TempDir(), "api.service")
	if err := os.WriteFile(unitPath, []byte("[Service]\nUser=root\nGroup=root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	partialPath := filepath.Join(serviceEnvDirForRoot(root), "env")
	identity := db.ServiceIdentity{
		RequestedUser: strconv.Itoa(os.Geteuid()), RequestedGroup: strconv.Itoa(os.Getegid()),
		UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()),
	}
	ops := serviceIdentityMigrationOps{
		unitPath:  func(string) string { return unitPath },
		isRunning: func(context.Context, string) (bool, error) { return false, nil },
		snapshot:  func(context.Context, *db.Service) (string, error) { return "", nil },
		reload:    func(context.Context) error { return nil },
	}
	_, err := server.migrateServiceIdentity(context.Background(), serviceIdentityMigrationRequest{
		Service: "api", Requested: identity.RequestedUser, Target: resolvedServiceIdentity{Persisted: identity},
		ReplacementUnit: "[Service]\nUser=" + identity.RequestedUser + "\nGroup=" + identity.RequestedGroup + "\n",
		GenerationPaths: []string{partialPath},
		StageGeneration: func(context.Context) error {
			if err := os.WriteFile(partialPath, []byte("partial stage"), 0o600); err != nil {
				return err
			}
			return errors.New("stage interrupted")
		},
		ops: &ops,
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "matches neither staged nor restored provenance") {
		t.Fatalf("migration error = %v, want fail-closed partial stage", err)
	}
	if raw, readErr := os.ReadFile(partialPath); readErr != nil || string(raw) != "partial stage" {
		t.Fatalf("partial staged path = %q, %v", raw, readErr)
	}
	if blockErr := server.checkServiceIdentityMutationAllowed("api"); !errors.Is(blockErr, errServiceIdentityRecoveryBlocked) {
		t.Fatalf("mutation block = %v, want retained-journal block", blockErr)
	}
}

func TestServiceIdentityMigrationPreflightUsesFinalTargetPorts(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "root")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	old := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root}
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, current *db.Service) error {
		*current = *old.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	target := old.Clone()
	target.Publish = []string{"80:8080"}
	identity := db.ServiceIdentity{
		RequestedUser: strconv.Itoa(os.Geteuid()), RequestedGroup: strconv.Itoa(os.Getegid()),
		UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()),
	}
	phaseCalled := false
	_, err := server.migrateServiceIdentity(context.Background(), serviceIdentityMigrationRequest{
		Service: "api", Requested: identity.RequestedUser, Target: resolvedServiceIdentity{Persisted: identity},
		TargetService: target, ReplacementUnit: "[Service]\n", ops: &serviceIdentityMigrationOps{
			phase: func(string) error { phaseCalled = true; return nil },
		},
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "privileged host port 80") {
		t.Fatalf("migration error = %v, want final target privileged-port rejection", err)
	}
	if phaseCalled {
		t.Fatal("migration entered side-effect phases before final target port preflight")
	}
}

func TestCombinedRootIdentityPlanningDoesNotRewriteBeforeMaterialization(t *testing.T) {
	server := newTestServer(t)
	oldRoot := filepath.Join(t.TempDir(), "old")
	newRoot := filepath.Join(t.TempDir(), "new")
	if err := ensureDirsForRoot(oldRoot, ""); err != nil {
		t.Fatal(err)
	}
	unitArtifact := filepath.Join(serviceBinDirForRoot(oldRoot), "api.service")
	if err := os.WriteFile(unitArtifact, []byte("[Service]\nWorkingDirectory="+serviceDataDirForRoot(oldRoot)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := &db.Service{
		Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: oldRoot,
		Artifacts: db.ArtifactStore{db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{"latest": unitArtifact}}},
	}
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, current *db.Service) error {
		*current = *old.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rootIdentity, resolveErr := resolveServiceIdentity("root")
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	identity := rootIdentity.Persisted
	_, err := server.migrateServiceIdentity(context.Background(), serviceIdentityMigrationRequest{
		Service: "api", Requested: "root", Target: resolvedServiceIdentity{Persisted: identity},
		RootPlan: &serviceRootMigrationPlan{
			ServiceName: "api", OldRoot: oldRoot, NewRoot: newRoot, Mode: serviceRootMigrationCopy,
		},
		ReplacementUnit: "[Service]\nUser=root\nGroup=root\nWorkingDirectory=" + serviceDataDirForRoot(newRoot) + "\n",
		ops: &serviceIdentityMigrationOps{
			phase: func(phase string) error {
				if phase == serviceIdentityPhaseJournal {
					return errors.New("reached durable journal")
				}
				return nil
			},
			unitPath:  func(string) string { return filepath.Join(t.TempDir(), "api.service") },
			isRunning: func(context.Context, string) (bool, error) { return false, nil },
		},
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "reached durable journal") {
		t.Fatalf("migration error = %v, want planning to reach journal without reading absent target artifacts", err)
	}
	if _, statErr := os.Stat(newRoot); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("target root changed during planning: %v", statErr)
	}
}

type testServiceIdentityGenerationStager struct {
	paths []string
	units []string
	stage func() error
}

func (s *testServiceIdentityGenerationStager) InstallTargetPaths() []string {
	return append([]string(nil), s.paths...)
}

func (s *testServiceIdentityGenerationStager) InstallUnits() []string {
	return append([]string(nil), s.units...)
}

func (s *testServiceIdentityGenerationStager) InstallTargetStatesExcluding(...string) ([]svc.InstallTargetState, error) {
	return nil, nil
}

func (s *testServiceIdentityGenerationStager) StageInstallForReload() ([]string, error) {
	if s.stage != nil {
		if err := s.stage(); err != nil {
			return nil, err
		}
	}
	return s.InstallUnits(), nil
}

func (s *testServiceIdentityGenerationStager) StageInstallForReloadExcluding(...string) ([]string, error) {
	return s.StageInstallForReload()
}

func TestCombinedRootIdentityCopyStagesRewrittenTargetGenerationAfterMaterialization(t *testing.T) {
	server := newTestServer(t)
	parent := t.TempDir()
	oldRoot := filepath.Join(parent, "old")
	newRoot := filepath.Join(parent, "new")
	if err := ensureDirsForRoot(oldRoot, ""); err != nil {
		t.Fatal(err)
	}
	oldUnitArtifact := filepath.Join(serviceBinDirForRoot(oldRoot), "api.service")
	oldAuxArtifact := filepath.Join(serviceBinDirForRoot(oldRoot), "yeet-api-ns.service")
	oldUnit := "[Service]\nExecStart=" + filepath.Join(serviceBinDirForRoot(oldRoot), "api-1") + "\n" +
		"EnvironmentFile=-" + filepath.Join(serviceEnvDirForRoot(oldRoot), "env") + "\n" +
		"WorkingDirectory=" + serviceDataDirForRoot(oldRoot) + "\nUser=root\nGroup=root\n"
	oldAux := "[Service]\nEnvironmentFile=" + filepath.Join(serviceEnvDirForRoot(oldRoot), "netns.env") + "\n"
	if err := os.WriteFile(oldUnitArtifact, []byte(oldUnit), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldAuxArtifact, []byte(oldAux), 0o644); err != nil {
		t.Fatal(err)
	}
	old := &db.Service{
		Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: oldRoot, Generation: 1, LatestGeneration: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit:  {Refs: map[db.ArtifactRef]string{db.Gen(1): oldUnitArtifact}},
			db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(1): oldAuxArtifact}},
		},
	}
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, service *db.Service) error {
		*service = *old.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := buildServiceRootMigrationPlan(context.Background(), server.cfg, *old, serviceRootMigrationRequest{Root: newRoot})
	if err != nil {
		t.Fatal(err)
	}
	plan.Mode = serviceRootMigrationCopy
	identity := db.ServiceIdentity{
		RequestedUser: strconv.Itoa(os.Geteuid()), RequestedGroup: strconv.Itoa(os.Getegid()),
		UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()),
	}
	unitPath := filepath.Join(parent, "systemd", "api.service")
	auxPath := filepath.Join(parent, "systemd", "yeet-api-ns.service")
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte(oldUnit), 0o644); err != nil {
		t.Fatal(err)
	}
	enabled := map[string]bool{"api.service": true, "yeet-api-ns.service": true}
	var stagedTarget *db.Service
	stageCalls := 0
	ops := serviceIdentityMigrationOps{
		unitPath:  func(string) string { return unitPath },
		isRunning: func(context.Context, string) (bool, error) { return false, nil },
		materialize: func(ctx context.Context, rootPlan serviceRootMigrationPlan, w io.Writer) (bool, error) {
			return false, materializeServiceRootMigration(ctx, rootPlan, w)
		},
		discardRoot: func(context.Context, serviceRootMigrationPlan, bool) error { return nil },
		inspect: func(context.Context, serviceIdentityInspectionRequest) (serviceIdentityInspection, error) {
			return serviceIdentityInspection{}, nil
		},
		apply:  func(serviceIdentityInspection, *serviceIdentityJournal) error { return nil },
		reload: func(context.Context) error { return nil },
		verify: func(_ context.Context, verification serviceIdentityMigrationVerification) error {
			raw, readErr := os.ReadFile(verification.UnitPath)
			if readErr != nil {
				return readErr
			}
			if strings.Contains(string(raw), oldRoot) || !strings.Contains(string(raw), newRoot) ||
				!strings.Contains(string(raw), "User="+identity.RequestedUser) || !strings.Contains(string(raw), "Group="+identity.RequestedGroup) {
				return fmt.Errorf("installed primary unit was not rewritten to target root and identity: %s", raw)
			}
			return nil
		},
		isEnabled: func(_ context.Context, unit string) (bool, error) { return enabled[unit], nil },
		enable:    func(_ context.Context, unit string) error { enabled[unit] = true; return nil },
		disable:   func(_ context.Context, unit string) error { enabled[unit] = false; return nil },
		newGenerationStager: func(target *db.Service, root string) (serviceIdentityGenerationStager, error) {
			if filepath.Clean(root) != filepath.Clean(newRoot) {
				return nil, fmt.Errorf("generation root = %s, want %s", root, newRoot)
			}
			stagedTarget = target.Clone()
			return &testServiceIdentityGenerationStager{
				paths: []string{unitPath, auxPath},
				units: []string{"api.service", "yeet-api-ns.service"},
				stage: func() error {
					stageCalls++
					primary, _ := stagedTarget.Artifacts.Gen(db.ArtifactSystemdUnit, stagedTarget.Generation)
					auxiliary, _ := stagedTarget.Artifacts.Gen(db.ArtifactNetNSService, stagedTarget.Generation)
					rawPrimary, readErr := os.ReadFile(primary)
					if readErr != nil {
						return readErr
					}
					if strings.Contains(string(rawPrimary), oldRoot) {
						return fmt.Errorf("copied primary artifact was not rewritten before stage")
					}
					for src, dst := range map[string]string{auxiliary: auxPath} {
						raw, readErr := os.ReadFile(src)
						if readErr != nil {
							return readErr
						}
						if strings.Contains(string(raw), oldRoot) || !strings.Contains(string(raw), newRoot) {
							return fmt.Errorf("copied artifact %s was not rewritten before stage: %s", src, raw)
						}
						if writeErr := os.WriteFile(dst, raw, 0o644); writeErr != nil {
							return writeErr
						}
					}
					return nil
				},
			}, nil
		},
	}
	_, err = server.migrateServiceIdentity(context.Background(), serviceIdentityMigrationRequest{
		Service: "api", Requested: identity.RequestedUser + ":" + identity.RequestedGroup,
		Target: resolvedServiceIdentity{Persisted: identity}, RootPlan: &plan, ops: &ops,
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if stageCalls != 1 {
		t.Fatalf("generation stage calls = %d, want 1", stageCalls)
	}
	service, err := server.serviceView("api")
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range []db.ArtifactName{db.ArtifactSystemdUnit, db.ArtifactNetNSService} {
		path, ok := service.AsStruct().Artifacts.Gen(artifact, 1)
		if !ok || !strings.HasPrefix(filepath.Clean(path), filepath.Clean(newRoot)+string(filepath.Separator)) {
			t.Fatalf("target %s artifact = %q, %t, want under %s", artifact, path, ok, newRoot)
		}
	}
}

func TestCombinedRootIdentityCopyCleansLegacyRuntimeTransactionally(t *testing.T) {
	legacy := map[string]string{
		"api":             "legacy-service",
		"env":             "legacy-env",
		"netns.env":       "legacy-netns",
		"tailscaled":      "legacy-tailscaled",
		"tailscaled.env":  "legacy-tailscaled-env",
		"tailscaled.json": "legacy-tailscaled-json",
	}
	for _, rollback := range []bool{false, true} {
		name := "success"
		if rollback {
			name = "rollback"
		}
		t.Run(name, func(t *testing.T) {
			server := newTestServer(t)
			parent := t.TempDir()
			oldRoot := filepath.Join(parent, "old")
			newRoot := filepath.Join(parent, "new")
			if err := ensureDirsForRoot(oldRoot, ""); err != nil {
				t.Fatal(err)
			}
			for filename, contents := range legacy {
				path := filepath.Join(serviceRunDirForRoot(oldRoot), filename)
				if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			oldUnitArtifact := filepath.Join(serviceBinDirForRoot(oldRoot), "api.service")
			oldUnit := "[Service]\nExecStart=" + filepath.Join(serviceBinDirForRoot(oldRoot), "api-1") + "\n" +
				"WorkingDirectory=" + serviceDataDirForRoot(oldRoot) + "\nUser=root\nGroup=root\n"
			if err := os.WriteFile(oldUnitArtifact, []byte(oldUnit), 0o640); err != nil {
				t.Fatal(err)
			}
			old := &db.Service{
				Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: oldRoot, Generation: 1, LatestGeneration: 1,
				Artifacts: db.ArtifactStore{
					db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{db.Gen(1): oldUnitArtifact}},
				},
			}
			if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, service *db.Service) error {
				*service = *old.Clone()
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			plan, err := buildServiceRootMigrationPlan(context.Background(), server.cfg, *old, serviceRootMigrationRequest{Root: newRoot})
			if err != nil {
				t.Fatal(err)
			}
			plan.Mode = serviceRootMigrationCopy
			identity := db.ServiceIdentity{
				RequestedUser: strconv.Itoa(os.Geteuid()), RequestedGroup: strconv.Itoa(os.Getegid()),
				UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()),
			}
			unitPath := filepath.Join(parent, "systemd", "api.service")
			if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(unitPath, []byte(oldUnit), 0o644); err != nil {
				t.Fatal(err)
			}
			enabled := true
			ops := serviceIdentityMigrationOps{
				unitPath:  func(string) string { return unitPath },
				isRunning: func(context.Context, string) (bool, error) { return false, nil },
				materialize: func(ctx context.Context, rootPlan serviceRootMigrationPlan, w io.Writer) (bool, error) {
					if err := materializeServiceRootMigration(ctx, rootPlan, w); err != nil {
						return false, err
					}
					return false, nil
				},
				inspect: func(context.Context, serviceIdentityInspectionRequest) (serviceIdentityInspection, error) {
					return serviceIdentityInspection{}, nil
				},
				apply:     func(serviceIdentityInspection, *serviceIdentityJournal) error { return nil },
				reload:    func(context.Context) error { return nil },
				verify:    func(context.Context, serviceIdentityMigrationVerification) error { return nil },
				isEnabled: func(context.Context, string) (bool, error) { return enabled, nil },
				enable:    func(context.Context, string) error { enabled = true; return nil },
				disable:   func(context.Context, string) error { enabled = false; return nil },
				newGenerationStager: func(*db.Service, string) (serviceIdentityGenerationStager, error) {
					return &testServiceIdentityGenerationStager{paths: []string{unitPath}, units: []string{"api.service"}}, nil
				},
			}
			if rollback {
				ops.writeUnit = func(string, []byte, os.FileMode, serviceIdentityPathProof, uint32, uint32) error {
					for filename := range legacy {
						if _, statErr := os.Stat(filepath.Join(serviceRunDirForRoot(newRoot), filename)); !errors.Is(statErr, os.ErrNotExist) {
							return fmt.Errorf("legacy runtime artifact %s was not removed before unit write: %v", filename, statErr)
						}
					}
					return errors.New("injected unit write failure")
				}
			}
			_, err = server.migrateServiceIdentity(context.Background(), serviceIdentityMigrationRequest{
				Service: "api", Requested: identity.RequestedUser + ":" + identity.RequestedGroup,
				Target: resolvedServiceIdentity{Persisted: identity}, RootPlan: &plan, ops: &ops,
			}, io.Discard)
			if rollback {
				if err == nil || !strings.Contains(err.Error(), "injected unit write failure") || !strings.Contains(err.Error(), "rollback restored") {
					t.Fatalf("migration error = %v, want fully rolled-back unit failure", err)
				}
				if _, statErr := os.Stat(newRoot); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("target root survived rollback: %v", statErr)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				for filename := range legacy {
					if _, statErr := os.Stat(filepath.Join(serviceRunDirForRoot(newRoot), filename)); !errors.Is(statErr, os.ErrNotExist) {
						t.Fatalf("legacy target runtime artifact %s survived success: %v", filename, statErr)
					}
				}
			}
			for filename, contents := range legacy {
				raw, readErr := os.ReadFile(filepath.Join(serviceRunDirForRoot(oldRoot), filename))
				if readErr != nil || string(raw) != contents {
					t.Fatalf("source runtime artifact %s = %q, %v, want exact original", filename, raw, readErr)
				}
			}
		})
	}
}

func TestCombinedRootIdentityFailureRetryCommandPreservesRootPlan(t *testing.T) {
	server := newTestServer(t)
	old := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: "/srv/old", ServiceRootZFS: "tank/old"}
	migration := &serviceIdentityMigration{
		server: server,
		req: serviceIdentityMigrationRequest{
			Service: "api", Requested: "app:app",
			Target: resolvedServiceIdentity{Persisted: db.ServiceIdentity{RequestedUser: "app", RequestedGroup: "app", UID: 1000, GID: 1000}},
			RootPlan: &serviceRootMigrationPlan{
				ServiceName: "api", OldRoot: "/srv/old", OldRootZFS: "tank/old",
				NewRoot: "/srv/new root", NewRootZFS: "tank/new", Mode: serviceRootMigrationCopy,
			},
		},
		previous: old,
		result: serviceIdentityMigrationResult{Previous: resolvedServiceIdentity{Persisted: db.ServiceIdentity{
			RequestedUser: "root", RequestedGroup: "root",
		}}},
	}
	err := migration.migrationError(errors.New("injected"), nil)
	for _, want := range []string{
		"yeet service set api --run-as=app:app",
		"'--service-root=/srv/new root'",
		"--zfs",
		"--copy",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("migration error missing retry fragment %q: %v", want, err)
		}
	}
}

func TestCombinedRootIdentitySnapshotsSourceBeforeAndTargetAfterMaterialization(t *testing.T) {
	server := newTestServer(t)
	oldRoot := filepath.Join(t.TempDir(), "old")
	newRoot := filepath.Join(t.TempDir(), "new")
	if err := ensureDirsForRoot(oldRoot, ""); err != nil {
		t.Fatal(err)
	}
	old := &db.Service{
		Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: oldRoot,
		ServiceRootZFS: "tank/source/api",
	}
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, current *db.Service) error {
		*current = *old.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(t.TempDir(), "api.service")
	if err := os.WriteFile(unitPath, []byte("[Service]\nUser=root\nGroup=wheel\nWorkingDirectory="+serviceDataDirForRoot(oldRoot)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rootIdentity, err := resolveServiceIdentity("root")
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	ops := serviceIdentityMigrationOps{
		unitPath:  func(string) string { return unitPath },
		isRunning: func(context.Context, string) (bool, error) { return false, nil },
		snapshot: func(_ context.Context, service *db.Service) (string, error) {
			order = append(order, "snapshot:"+service.ServiceRootZFS)
			return service.ServiceRootZFS + "@identity", nil
		},
		materialize: func(context.Context, serviceRootMigrationPlan, io.Writer) (bool, error) {
			order = append(order, "materialize")
			return false, ensureDirsForRoot(newRoot, "")
		},
		discardRoot: func(context.Context, serviceRootMigrationPlan, bool) error { return nil },
		inspect: func(context.Context, serviceIdentityInspectionRequest) (serviceIdentityInspection, error) {
			return serviceIdentityInspection{}, nil
		},
		apply:  func(serviceIdentityInspection, *serviceIdentityJournal) error { return nil },
		reload: func(context.Context) error { return nil },
		verify: func(context.Context, serviceIdentityMigrationVerification) error { return nil },
	}
	result, err := server.migrateServiceIdentity(context.Background(), serviceIdentityMigrationRequest{
		Service: "api", Requested: "root", Target: rootIdentity,
		RootPlan: &serviceRootMigrationPlan{
			ServiceName: "api", OldRoot: oldRoot, OldRootZFS: old.ServiceRootZFS,
			NewRoot: newRoot, NewRootZFS: "tank/target/api", Mode: serviceRootMigrationCopy,
		},
		ReplacementUnit: "[Service]\nUser=" + rootIdentity.Persisted.RequestedUser + "\nGroup=" + rootIdentity.Persisted.RequestedGroup + "\nWorkingDirectory=" + serviceDataDirForRoot(newRoot) + "\n",
		ops:             &ops,
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{"snapshot:tank/source/api", "materialize", "snapshot:tank/target/api"}
	if strings.Join(order, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("snapshot/materialize order = %v, want %v", order, wantOrder)
	}
	if result.ZFSSnapshot != "tank/source/api@identity, tank/target/api@identity" {
		t.Fatalf("recovery snapshots = %q", result.ZFSSnapshot)
	}
}

func TestServiceIdentityMaterializationRejectsPreexistingTargetShapes(t *testing.T) {
	for _, skeleton := range []bool{false, true} {
		name := "empty"
		if skeleton {
			name = "retry-skeleton"
		}
		t.Run(name, func(t *testing.T) {
			server := newTestServer(t)
			parent := t.TempDir()
			oldRoot := filepath.Join(parent, "old")
			newRoot := filepath.Join(parent, "new")
			if err := ensureDirsForRoot(oldRoot, ""); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(serviceDataDirForRoot(oldRoot), "payload"), []byte("new-state"), 0o640); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(newRoot, 0o711); err != nil {
				t.Fatal(err)
			}
			if skeleton {
				for index, path := range serviceDirectoryPlan(newRoot) {
					mode := os.FileMode(0o700 + index)
					if err := os.Mkdir(path, mode); err != nil {
						t.Fatal(err)
					}
				}
			}
			service := db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: oldRoot}
			plan, err := buildServiceRootMigrationPlan(context.Background(), server.cfg, service, serviceRootMigrationRequest{Root: newRoot})
			if err != nil {
				t.Fatal(err)
			}
			plan.Mode = serviceRootMigrationCopy
			migration := &serviceIdentityMigration{server: server}
			_, err = migration.materializeRoot(context.Background(), plan, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "newly-created target root") {
				t.Fatalf("materializeRoot error = %v, want preexisting target rejection", err)
			}
		})
	}
}

func TestServiceIdentityMaterializationRejectsChangedExistingTargetProvenance(t *testing.T) {
	server := newTestServer(t)
	parent := t.TempDir()
	oldRoot := filepath.Join(parent, "old")
	targetRoot := filepath.Join(parent, "target")
	if err := ensureDirsForRoot(oldRoot, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(targetRoot, 0o711); err != nil {
		t.Fatal(err)
	}
	service := db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: oldRoot}
	plan, err := buildServiceRootMigrationPlan(context.Background(), server.cfg, service, serviceRootMigrationRequest{Root: targetRoot})
	if err != nil {
		t.Fatal(err)
	}
	plan.Mode = serviceRootMigrationCopy
	if err := os.Chmod(targetRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	migration := &serviceIdentityMigration{server: server}
	_, err = migration.materializeRoot(context.Background(), plan, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "newly-created target root") {
		t.Fatalf("materializeRoot error = %v, want preexisting target rejection", err)
	}
	info, statErr := os.Stat(targetRoot)
	if statErr != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("target mode = %v, %v, want operator change preserved", info, statErr)
	}
}

func TestServiceIdentityRollbackPreservesTargetChangedAfterMaterialization(t *testing.T) {
	server := newTestServer(t)
	parent := t.TempDir()
	oldRoot := filepath.Join(parent, "old")
	targetRoot := filepath.Join(parent, "target")
	if err := ensureDirsForRoot(oldRoot, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(serviceDataDirForRoot(oldRoot), "state"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: oldRoot}
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, service *db.Service) error {
		*service = *old.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(t.TempDir(), "api.service")
	oldUnit := "[Service]\nUser=root\nGroup=root\nWorkingDirectory=" + serviceDataDirForRoot(oldRoot) + "\n"
	if err := os.WriteFile(unitPath, []byte(oldUnit), 0o644); err != nil {
		t.Fatal(err)
	}
	identity := db.ServiceIdentity{
		RequestedUser: strconv.Itoa(os.Geteuid()), RequestedGroup: strconv.Itoa(os.Getegid()),
		UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()),
	}
	marker := filepath.Join(targetRoot, "operator-data")
	ops := serviceIdentityMigrationOps{
		unitPath:  func(string) string { return unitPath },
		isRunning: func(context.Context, string) (bool, error) { return false, nil },
		snapshot:  func(context.Context, *db.Service) (string, error) { return "", nil },
		inspect: func(context.Context, serviceIdentityInspectionRequest) (serviceIdentityInspection, error) {
			return serviceIdentityInspection{}, nil
		},
		apply:  func(serviceIdentityInspection, *serviceIdentityJournal) error { return nil },
		reload: func(context.Context) error { return nil },
		verify: func(context.Context, serviceIdentityMigrationVerification) error {
			if err := os.WriteFile(marker, []byte("preserve"), 0o600); err != nil {
				return err
			}
			return errors.New("target health failed")
		},
	}
	_, err := server.migrateServiceIdentity(context.Background(), serviceIdentityMigrationRequest{
		Service: "api", Requested: identity.RequestedUser + ":" + identity.RequestedGroup,
		Target: resolvedServiceIdentity{Persisted: identity},
		RootPlan: &serviceRootMigrationPlan{
			ServiceName: "api", OldRoot: oldRoot, NewRoot: targetRoot, Mode: serviceRootMigrationCopy,
		},
		ReplacementUnit: "[Service]\nUser=" + identity.RequestedUser + "\nGroup=" + identity.RequestedGroup + "\nWorkingDirectory=" + serviceDataDirForRoot(targetRoot) + "\n",
		ops:             &ops,
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "target root changed after materialization") {
		t.Fatalf("migration error = %v, want changed-target refusal", err)
	}
	raw, readErr := os.ReadFile(marker)
	if readErr != nil || string(raw) != "preserve" {
		t.Fatalf("operator data = %q, %v", raw, readErr)
	}
	if blockErr := server.checkServiceIdentityMutationAllowed("api"); !errors.Is(blockErr, errServiceIdentityRecoveryBlocked) {
		t.Fatalf("mutation block = %v, want retained-journal block", blockErr)
	}
}

func TestServiceIdentityRollbackDetectsInPlaceTargetContentChange(t *testing.T) {
	targetRoot := filepath.Join(t.TempDir(), "target")
	if err := ensureDirsForRoot(targetRoot, ""); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(serviceDataDirForRoot(targetRoot), "state")
	if err := os.WriteFile(state, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, count, meta, err := serviceIdentityTargetInventory(targetRoot)
	if err != nil {
		t.Fatal(err)
	}
	migration := &serviceIdentityMigration{
		server: newTestServer(t),
		req: serviceIdentityMigrationRequest{RootPlan: &serviceRootMigrationPlan{
			ServiceName: "api", NewRoot: targetRoot, Mode: serviceRootMigrationCopy,
		}},
		materialization: serviceIdentityPhaseRecord{
			Phase: serviceIdentityPhaseMaterializeFinal, RootCreated: true,
			RootDev: meta.Dev, RootIno: meta.Ino, InventoryDigest: digest, InventoryCount: count,
		},
	}
	if err := os.WriteFile(state, []byte("after!"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = migration.discardRoot(context.Background(), *migration.req.RootPlan, false)
	if err == nil || !strings.Contains(err.Error(), "target root changed") {
		t.Fatalf("discardRoot error = %v, want in-place content change rejection", err)
	}
	if raw, readErr := os.ReadFile(state); readErr != nil || string(raw) != "after!" {
		t.Fatalf("operator content = %q, %v", raw, readErr)
	}
}

func TestServiceIdentityMigrationCapturesMaterializationAfterArtifactRewrites(t *testing.T) {
	server := newTestServer(t)
	parent := t.TempDir()
	oldRoot := filepath.Join(parent, "old")
	targetRoot := filepath.Join(parent, "target")
	if err := ensureDirsForRoot(oldRoot, ""); err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(serviceBinDirForRoot(oldRoot), "api.service")
	if err := os.WriteFile(artifactPath, []byte("[Service]\nWorkingDirectory="+oldRoot+"/data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := &db.Service{
		Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: oldRoot, Generation: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{db.Gen(1): artifactPath}},
		},
	}
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, service *db.Service) error {
		*service = *old.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := buildServiceRootMigrationPlan(context.Background(), server.cfg, *old, serviceRootMigrationRequest{Root: targetRoot})
	if err != nil {
		t.Fatal(err)
	}
	plan.Mode = serviceRootMigrationCopy
	unitPath := filepath.Join(parent, "api-installed.service")
	if err := os.WriteFile(unitPath, []byte("[Service]\nUser=root\nGroup=root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	identity := db.ServiceIdentity{
		RequestedUser: strconv.Itoa(os.Geteuid()), RequestedGroup: strconv.Itoa(os.Getegid()),
		UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()),
	}
	ops := serviceIdentityMigrationOps{
		phase: func(phase string) error {
			if phase == serviceIdentityPhaseInventorySeal {
				return errors.New("stop after rewritten materialization")
			}
			return nil
		},
		unitPath:  func(string) string { return unitPath },
		isRunning: func(context.Context, string) (bool, error) { return false, nil },
		reload:    func(context.Context) error { return nil },
	}
	_, err = server.migrateServiceIdentity(context.Background(), serviceIdentityMigrationRequest{
		Service: "api", Requested: identity.RequestedUser, Target: resolvedServiceIdentity{Persisted: identity},
		RootPlan: &plan, ReplacementUnit: "[Service]\nUser=" + identity.RequestedUser + "\nGroup=" + identity.RequestedGroup + "\n",
		ops: &ops,
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "rollback restored") {
		t.Fatalf("migration error = %v, want complete rollback", err)
	}
	if _, statErr := os.Stat(targetRoot); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("rewritten target root survived rollback: %v", statErr)
	}
}

func TestServiceIdentityMigrationRollbackPreservesReplacedZFSDataset(t *testing.T) {
	targetRoot := filepath.Join(t.TempDir(), "target")
	if err := ensureDirsForRoot(targetRoot, ""); err != nil {
		t.Fatal(err)
	}
	digest, count, meta, err := serviceIdentityTargetInventory(targetRoot)
	if err != nil {
		t.Fatal(err)
	}
	destroyed := false
	server := newTestServer(t)
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		switch {
		case len(args) == 5 && args[0] == "list" && args[4] == "tank/yeet/api":
			return "tank/yeet/api\n", "", nil
		case len(args) == 6 && args[0] == "get" && args[4] == "guid":
			return "replacement-guid\n", "", nil
		case len(args) == 3 && args[0] == "destroy":
			destroyed = true
			return "", "", nil
		default:
			return "", "", fmt.Errorf("unexpected zfs args: %v", args)
		}
	}
	migration := &serviceIdentityMigration{
		server: server,
		req: serviceIdentityMigrationRequest{RootPlan: &serviceRootMigrationPlan{
			ServiceName: "api", NewRoot: targetRoot, NewRootZFS: "tank/yeet/api", CreateNewRootZFS: true,
		}},
		materialization: serviceIdentityPhaseRecord{
			Phase: serviceIdentityPhaseMaterialize, DatasetCreated: true,
			RootCreated: true, RootDev: meta.Dev, RootIno: meta.Ino,
			InventoryDigest: digest, InventoryCount: count, DatasetGUID: "original-guid",
		},
	}
	err = migration.discardRoot(context.Background(), *migration.req.RootPlan, true)
	if err == nil || !strings.Contains(err.Error(), "GUID changed") {
		t.Fatalf("discard error = %v, want GUID mismatch", err)
	}
	if destroyed {
		t.Fatal("replacement ZFS dataset was destroyed")
	}
}

func TestServiceIdentityRollbackUsesAtomicZFSMarkerBeforeGUIDRecord(t *testing.T) {
	for _, tt := range []struct {
		name      string
		marker    string
		wantError bool
	}{
		{name: "owned", marker: "tx-marker"},
		{name: "replacement", marker: "other", wantError: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			destroyed := false
			server := newTestServer(t)
			server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
				switch {
				case len(args) == 5 && args[0] == "list" && args[4] == "tank/yeet/api":
					return "tank/yeet/api\n", "", nil
				case len(args) == 6 && args[0] == "get" && args[4] == serviceIdentityZFSMarkerProperty:
					return tt.marker + "\n", "", nil
				case len(args) == 3 && args[0] == "destroy":
					destroyed = true
					return "", "", nil
				default:
					return "", "", fmt.Errorf("unexpected zfs args: %v", args)
				}
			}
			migration := &serviceIdentityMigration{server: server, migrationID: "tx-marker"}
			plan := serviceRootMigrationPlan{
				ServiceName: "api", NewRoot: filepath.Join(t.TempDir(), "api"),
				NewRootZFS: "tank/yeet/api", CreateNewRootZFS: true,
			}
			err := migration.discardRoot(context.Background(), plan, true)
			if tt.wantError {
				if err == nil || !strings.Contains(err.Error(), "transaction marker") {
					t.Fatalf("discard error = %v, want marker mismatch", err)
				}
				if destroyed {
					t.Fatal("replacement dataset was destroyed")
				}
				return
			}
			if err != nil {
				t.Fatalf("discardRoot: %v", err)
			}
			if !destroyed {
				t.Fatal("transaction-owned dataset was not destroyed")
			}
		})
	}
}

func TestServiceIdentityRollbackDoesNotTreatMissingMountpointAsZFSAbsence(t *testing.T) {
	for _, tt := range []struct {
		name            string
		materialization serviceIdentityPhaseRecord
	}{
		{name: "before guid proof"},
		{name: "after guid proof", materialization: serviceIdentityPhaseRecord{
			DatasetGUID: "guid-1", RootDev: 1, RootIno: 2,
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(t)
			server.zfsRunner = func(context.Context, ...string) (string, string, error) {
				return "", "pool unavailable", errors.New("zfs unavailable")
			}
			migration := &serviceIdentityMigration{
				server: server, migrationID: "tx-zfs", materialization: tt.materialization,
			}
			plan := serviceRootMigrationPlan{
				NewRoot: filepath.Join(t.TempDir(), "unmounted"), NewRootZFS: "tank/yeet/api", CreateNewRootZFS: true,
			}
			err := migration.discardRoot(context.Background(), plan, true)
			if err == nil || !strings.Contains(err.Error(), "pool unavailable") {
				t.Fatalf("discardRoot error = %v, want authoritative ZFS lookup failure", err)
			}
		})
	}
}

func TestDiscardUnrecordedServiceIdentityStageOnlyRemovesEmptyOwnedStage(t *testing.T) {
	parent := t.TempDir()
	plan := serviceRootMigrationPlan{NewRoot: filepath.Join(parent, "api")}
	stage := filepath.Join(parent, ".yeet-service-root-tx")
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := discardUnrecordedServiceIdentityStage(plan, "tx"); err != nil {
		t.Fatalf("discard empty stage: %v", err)
	}
	if _, err := os.Lstat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty transaction stage remains: %v", err)
	}

	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(stage, "operator-data")
	if err := os.WriteFile(marker, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := discardUnrecordedServiceIdentityStage(plan, "tx"); err == nil || !strings.Contains(err.Error(), "left untouched") {
		t.Fatalf("discard nonempty stage error = %v, want fail closed", err)
	}
	if raw, err := os.ReadFile(marker); err != nil || string(raw) != "preserve" {
		t.Fatalf("nonempty stage marker = %q, %v", raw, err)
	}
}

func TestServiceIdentityRootPublishNeverReplacesConcurrentTarget(t *testing.T) {
	parent := t.TempDir()
	stage := filepath.Join(parent, "stage")
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	stageMarker := filepath.Join(stage, "transaction-data")
	if err := os.WriteFile(stageMarker, []byte("staged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o711); err != nil {
		t.Fatal(err)
	}
	if err := renameServiceIdentityRootNoReplace(stage, target); err == nil {
		t.Fatal("no-replace root publish replaced a concurrent target")
	}
	if info, err := os.Lstat(target); err != nil || info.Mode().Perm() != 0o711 {
		t.Fatalf("concurrent target = %#v, %v, want preserved", info, err)
	}
	if raw, err := os.ReadFile(stageMarker); err != nil || string(raw) != "staged" {
		t.Fatalf("transaction stage = %q, %v, want preserved", raw, err)
	}
}

func TestServiceIdentityDefaultRuntimeOpsUseTimerAndAuxiliaryUnits(t *testing.T) {
	server := newTestServer(t)
	service := &db.Service{
		Name: "api", ServiceType: db.ServiceTypeSystemd, Generation: 2,
		Artifacts: db.ArtifactStore{},
	}
	for _, name := range []db.ArtifactName{db.ArtifactSystemdUnit, db.ArtifactSystemdTimerFile, db.ArtifactNetNSService, db.ArtifactTSService} {
		service.Artifacts[name] = &db.Artifact{Refs: map[db.ArtifactRef]string{db.Gen(2): "/artifact"}}
	}
	migration := &serviceIdentityMigration{server: server, previous: service.Clone(), target: service.Clone()}
	active := map[string]bool{
		"api.timer": true, "api.service": true, "yeet-api-ns.service": true, "yeet-api-ts.service": true,
	}
	var calls []string
	oldSystemctl := catchSystemctl
	oldActive := catchSystemdUnitActive
	catchSystemctl = func(args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		if len(args) == 2 && args[0] == "stop" {
			active[args[1]] = false
		}
		if len(args) == 2 && args[0] == "start" {
			active[args[1]] = true
		}
		return nil
	}
	catchSystemdUnitActive = func(unit string) bool { return active[unit] }
	t.Cleanup(func() {
		catchSystemctl = oldSystemctl
		catchSystemdUnitActive = oldActive
	})

	ops := migration.defaultOps()
	if err := ops.stop(context.Background(), "api"); err != nil {
		t.Fatal(err)
	}
	if err := ops.start(context.Background(), "api"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"stop api.timer", "stop api.service", "stop yeet-api-ts.service", "stop yeet-api-ns.service",
		"start yeet-api-ns.service", "start yeet-api-ts.service", "start api.timer",
	}
	if strings.Join(calls, "|") != strings.Join(want, "|") {
		t.Fatalf("runtime calls = %v, want %v", calls, want)
	}
}

func TestServiceIdentityDefaultRuntimeOpsResolveTargetAfterPreparation(t *testing.T) {
	migration := &serviceIdentityMigration{}
	ops := migration.defaultOps()
	target := &db.Service{
		Name: "cron", ServiceType: db.ServiceTypeSystemd, Generation: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit:      {Refs: map[db.ArtifactRef]string{db.Gen(1): "/service"}},
			db.ArtifactSystemdTimerFile: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/timer"}},
		},
	}
	migration.previous = &db.Service{Name: "cron", ServiceType: db.ServiceTypeSystemd}
	migration.target = target
	active := map[string]bool{}
	var calls []string
	oldSystemctl := catchSystemctl
	oldActive := catchSystemdUnitActive
	catchSystemctl = func(args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		if len(args) == 2 && args[0] == "start" {
			active[args[1]] = true
		}
		return nil
	}
	catchSystemdUnitActive = func(unit string) bool { return active[unit] }
	t.Cleanup(func() {
		catchSystemctl = oldSystemctl
		catchSystemdUnitActive = oldActive
	})

	if err := ops.start(context.Background(), "cron"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"start cron.timer"}) {
		t.Fatalf("runtime calls = %v, want timer start", calls)
	}
}

func TestServiceIdentityRuntimeOpsUseCapturedGenerationsAcrossTimerTransition(t *testing.T) {
	server := newTestServer(t)
	previous := &db.Service{
		Name: "api", ServiceType: db.ServiceTypeSystemd, Generation: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit:      {Refs: map[db.ArtifactRef]string{db.Gen(1): "/service-1", db.Gen(2): "/service-2"}},
			db.ArtifactSystemdTimerFile: {Refs: map[db.ArtifactRef]string{db.Gen(2): "/timer-2", db.ArtifactRef("staged"): "/timer-staged"}},
		},
	}
	target := previous.Clone()
	target.Generation = 2
	migration := &serviceIdentityMigration{server: server, previous: previous, target: target}
	active := map[string]bool{"api.service": true, "api.timer": false}
	var calls []string
	oldSystemctl := catchSystemctl
	oldActive := catchSystemdUnitActive
	catchSystemctl = func(args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		if len(args) == 2 && args[0] == "stop" {
			active[args[1]] = false
		}
		if len(args) == 2 && args[0] == "start" {
			active[args[1]] = true
		}
		return nil
	}
	catchSystemdUnitActive = func(unit string) bool { return active[unit] }
	t.Cleanup(func() {
		catchSystemctl = oldSystemctl
		catchSystemdUnitActive = oldActive
	})
	ops := migration.defaultOps()
	if running, err := ops.isPreviousRunning(context.Background(), "api"); err != nil || !running {
		t.Fatalf("previous running = %t, %v, want true", running, err)
	}
	if running, err := ops.isTargetRunning(context.Background(), "api"); err != nil || running {
		t.Fatalf("target running = %t, %v, want false", running, err)
	}
	if err := ops.stop(context.Background(), "api"); err != nil {
		t.Fatal(err)
	}
	if err := ops.start(context.Background(), "api"); err != nil {
		t.Fatal(err)
	}
	if running, err := ops.isTargetRunning(context.Background(), "api"); err != nil || !running {
		t.Fatalf("target running after start = %t, %v, want true", running, err)
	}
	want := []string{"stop api.service", "start api.timer"}
	if strings.Join(calls, "|") != strings.Join(want, "|") {
		t.Fatalf("runtime calls = %v, want %v", calls, want)
	}
}

func TestServiceIdentityGenerationActivationReplacesPreviousPrimaryUnit(t *testing.T) {
	server := newTestServer(t)
	previous := &db.Service{
		Name: "api", ServiceType: db.ServiceTypeSystemd, Generation: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/service-1"}},
		},
	}
	target := previous.Clone()
	target.Generation = 2
	target.Artifacts[db.ArtifactSystemdUnit].Refs[db.Gen(2)] = "/service-2"
	target.Artifacts[db.ArtifactSystemdTimerFile] = &db.Artifact{Refs: map[db.ArtifactRef]string{db.Gen(2): "/timer-2"}}
	enabled := map[string]bool{"api.service": true, "api.timer": false}
	migration := &serviceIdentityMigration{
		server: server, previous: previous, target: target,
		req: serviceIdentityMigrationRequest{Service: "api", GenerationUnits: []string{"api.timer"}},
		ops: serviceIdentityMigrationOps{
			phase:     func(string) error { return nil },
			isEnabled: func(_ context.Context, unit string) (bool, error) { return enabled[unit], nil },
			enable:    func(_ context.Context, unit string) error { enabled[unit] = true; return nil },
			disable:   func(_ context.Context, unit string) error { enabled[unit] = false; return nil },
		},
	}
	states, err := migration.captureGenerationUnitEnablement(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 || states[0].Unit != "api.service" || states[0].TargetEnabled || states[1].Unit != "api.timer" || !states[1].TargetEnabled {
		t.Fatalf("enablement plan = %#v", states)
	}
	j, err := createServiceIdentityJournal(server.cfg.RootDir, serviceIdentityJournalHeader{
		ID: "tx-enable-transition", Service: "api", Root: t.TempDir(),
		TargetIdentity: db.ServiceIdentity{UID: 1000, GID: 1000},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	if err := j.Seal(); err != nil {
		t.Fatal(err)
	}
	migration.journal = j
	migration.generationUnits = states
	if err := migration.activateGenerationUnits(context.Background()); err != nil {
		t.Fatal(err)
	}
	if enabled["api.service"] || !enabled["api.timer"] {
		t.Fatalf("target enablement = %#v", enabled)
	}
	if err := restoreServiceIdentityUnitEnablement(context.Background(), migration.ops, states); err != nil {
		t.Fatal(err)
	}
	if !enabled["api.service"] || enabled["api.timer"] {
		t.Fatalf("restored enablement = %#v", enabled)
	}
}

func TestServiceIdentityMigrationPreservesExternalReplacementAfterDurableGenerationStage(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "root")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	old := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root}
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, current *db.Service) error {
		*current = *old.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stable := filepath.Join(serviceEnvDirForRoot(root), "env")
	if err := os.WriteFile(stable, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(t.TempDir(), "api.service")
	if err := os.WriteFile(unitPath, []byte("[Service]\nUser=root\nGroup=root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	identity := db.ServiceIdentity{
		RequestedUser: strconv.Itoa(os.Geteuid()), RequestedGroup: strconv.Itoa(os.Getegid()),
		UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()),
	}
	ops := serviceIdentityMigrationOps{
		unitPath:  func(string) string { return unitPath },
		isRunning: func(context.Context, string) (bool, error) { return false, nil },
		phase: func(phase string) error {
			if phase != serviceIdentityPhaseInventorySeal {
				return nil
			}
			if err := os.WriteFile(unitPath, []byte("operator replacement"), 0o600); err != nil {
				return err
			}
			return errors.New("fail after external replacement")
		},
		snapshot: func(context.Context, *db.Service) (string, error) { return "", nil },
		reload:   func(context.Context) error { return nil },
	}
	_, err := server.migrateServiceIdentity(context.Background(), serviceIdentityMigrationRequest{
		Service: "api", Requested: identity.RequestedUser, Target: resolvedServiceIdentity{Persisted: identity},
		ReplacementUnit: "[Service]\nUser=" + identity.RequestedUser + "\nGroup=" + identity.RequestedGroup + "\n",
		GenerationPaths: []string{unitPath, stable},
		StageGeneration: func(context.Context) error {
			return os.WriteFile(stable, []byte("staged"), 0o640)
		},
		ops: &ops,
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "matches neither replacement nor restored provenance") {
		t.Fatalf("migration error = %v, want exact staged-provenance failure", err)
	}
	if raw, readErr := os.ReadFile(unitPath); readErr != nil || string(raw) != "operator replacement" {
		t.Fatalf("external replacement = %q, %v", raw, readErr)
	}
	if blockErr := server.checkServiceIdentityMutationAllowed("api"); !errors.Is(blockErr, errServiceIdentityRecoveryBlocked) {
		t.Fatalf("mutation block = %v, want retained journal", blockErr)
	}
}

func TestServiceIdentityMigrationMissingRuntimeBackupFailsClosed(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "root")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(serviceRunDirForRoot(root), "api")
	if err := os.WriteFile(legacy, []byte("legacy"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root}
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, current *db.Service) error {
		*current = *old.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(t.TempDir(), "api.service")
	if err := os.WriteFile(unitPath, []byte("[Service]\nUser=root\nGroup=root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	identity := db.ServiceIdentity{
		RequestedUser: strconv.Itoa(os.Geteuid()), RequestedGroup: strconv.Itoa(os.Getegid()),
		UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()),
	}
	ops := serviceIdentityMigrationOps{
		unitPath:  func(string) string { return unitPath },
		isRunning: func(context.Context, string) (bool, error) { return false, nil },
		phase: func(phase string) error {
			if phase != serviceIdentityPhaseUnitWrite {
				return nil
			}
			matches, globErr := filepath.Glob(filepath.Join(server.cfg.RootDir, "migrations", "service-identity", "backups", "*", "runtime", "000"))
			if globErr != nil || len(matches) != 1 {
				return fmt.Errorf("runtime backup matches %v: %w", matches, globErr)
			}
			if removeErr := os.Remove(matches[0]); removeErr != nil {
				return removeErr
			}
			return errors.New("fail after backup deletion")
		},
		snapshot: func(context.Context, *db.Service) (string, error) { return "", nil },
		inspect: func(context.Context, serviceIdentityInspectionRequest) (serviceIdentityInspection, error) {
			return serviceIdentityInspection{}, nil
		},
		apply:  func(serviceIdentityInspection, *serviceIdentityJournal) error { return nil },
		reload: func(context.Context) error { return nil },
	}
	_, err := server.migrateServiceIdentity(context.Background(), serviceIdentityMigrationRequest{
		Service: "api", Requested: identity.RequestedUser, Target: resolvedServiceIdentity{Persisted: identity},
		ReplacementUnit: "[Service]\nUser=" + identity.RequestedUser + "\nGroup=" + identity.RequestedGroup + "\n",
		ops:             &ops,
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "durable provenance") {
		t.Fatalf("migration error = %v, want missing backup provenance failure", err)
	}
	if _, statErr := os.Stat(legacy); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unrestorable legacy path unexpectedly changed: %v", statErr)
	}
}

func TestServiceIdentityMigrationStopsAndRestoresActiveAuxiliaryUnits(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "root")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	old := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root, Generation: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/artifact/ns"}},
			db.ArtifactTSService:    {Refs: map[db.ArtifactRef]string{db.Gen(1): "/artifact/ts"}},
		},
	}
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, current *db.Service) error {
		*current = *old.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(t.TempDir(), "api.service")
	if err := os.WriteFile(unitPath, []byte("[Service]\nUser=root\nGroup=root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wantRuntime := []serviceIdentityRuntimeUnitState{
		{Unit: "api.service"}, {Unit: "yeet-api-ts.service", Active: true}, {Unit: "yeet-api-ns.service", Active: true},
	}
	currentRuntime := append([]serviceIdentityRuntimeUnitState(nil), wantRuntime...)
	stopCalls, restoreCalls := 0, 0
	identity := db.ServiceIdentity{
		RequestedUser: strconv.Itoa(os.Geteuid()), RequestedGroup: strconv.Itoa(os.Getegid()),
		UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()),
	}
	ops := serviceIdentityMigrationOps{
		unitPath: func(string) string { return unitPath },
		captureRuntime: func(context.Context, string) ([]serviceIdentityRuntimeUnitState, error) {
			return append([]serviceIdentityRuntimeUnitState(nil), currentRuntime...), nil
		},
		restoreRuntime: func(_ context.Context, _ string, state []serviceIdentityRuntimeUnitState) error {
			restoreCalls++
			currentRuntime = append([]serviceIdentityRuntimeUnitState(nil), state...)
			return nil
		},
		stop: func(context.Context, string) error {
			stopCalls++
			for index := range currentRuntime {
				currentRuntime[index].Active = false
			}
			return nil
		},
		snapshot: func(context.Context, *db.Service) (string, error) { return "", nil },
		inspect: func(context.Context, serviceIdentityInspectionRequest) (serviceIdentityInspection, error) {
			return serviceIdentityInspection{}, nil
		},
		apply: func(serviceIdentityInspection, *serviceIdentityJournal) error { return nil },
		writeUnit: func(string, []byte, os.FileMode, serviceIdentityPathProof, uint32, uint32) error {
			return errors.New("force rollback after auxiliary stop")
		},
		reload: func(context.Context) error { return nil },
	}
	_, err := server.migrateServiceIdentity(context.Background(), serviceIdentityMigrationRequest{
		Service: "api", Requested: identity.RequestedUser, Target: resolvedServiceIdentity{Persisted: identity},
		ReplacementUnit: "[Service]\nUser=" + identity.RequestedUser + "\nGroup=" + identity.RequestedGroup + "\n",
		ops:             &ops,
	}, io.Discard)
	if err == nil || stopCalls != 1 || restoreCalls != 1 {
		t.Fatalf("migration error=%v stop=%d restore=%d", err, stopCalls, restoreCalls)
	}
	if !reflect.DeepEqual(currentRuntime, wantRuntime) {
		t.Fatalf("runtime state = %#v, want %#v", currentRuntime, wantRuntime)
	}
}

func TestServiceIdentityCopyRejectsSourceBoundaryBeforeTargetCreation(t *testing.T) {
	server := newTestServer(t)
	oldRoot := filepath.Join(t.TempDir(), "old")
	targetRoot := filepath.Join(t.TempDir(), "target")
	nested := filepath.Join(oldRoot, "data", "mounted")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	old := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: oldRoot}
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, current *db.Service) error {
		*current = *old.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	oldMounts, oldDatasets := serviceIdentityMountPointsFn, serviceIdentityDatasetBoundariesFn
	serviceIdentityMountPointsFn = func() ([]string, error) { return []string{nested}, nil }
	serviceIdentityDatasetBoundariesFn = func(context.Context, zfsCommandRunner, string) ([]serviceIdentityDatasetBoundary, error) {
		return nil, nil
	}
	t.Cleanup(func() {
		serviceIdentityMountPointsFn, serviceIdentityDatasetBoundariesFn = oldMounts, oldDatasets
	})
	identity := db.ServiceIdentity{
		RequestedUser: strconv.Itoa(os.Geteuid()), RequestedGroup: strconv.Itoa(os.Getegid()),
		UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()),
	}
	_, err := server.migrateServiceIdentity(context.Background(), serviceIdentityMigrationRequest{
		Service: "api", Requested: identity.RequestedUser, Target: resolvedServiceIdentity{Persisted: identity},
		RootPlan: &serviceRootMigrationPlan{ServiceName: "api", OldRoot: oldRoot, NewRoot: targetRoot, Mode: serviceRootMigrationCopy},
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "mount boundary") {
		t.Fatalf("migration error = %v, want source boundary rejection", err)
	}
	if _, statErr := os.Stat(targetRoot); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("target root was created before source preflight: %v", statErr)
	}
}

func TestServiceIdentityMaterializeCopyRechecksSourceBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name     string
		mount    bool
		dataset  bool
		wantText string
	}{
		{name: "mount", mount: true, wantText: "mount boundary"},
		{name: "dataset", dataset: true, wantText: "nested ZFS dataset"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(t)
			oldRoot := filepath.Join(t.TempDir(), "old")
			targetRoot := filepath.Join(t.TempDir(), "target")
			nested := filepath.Join(oldRoot, "data", "boundary")
			if err := os.MkdirAll(nested, 0o755); err != nil {
				t.Fatal(err)
			}
			oldMounts, oldDatasets := serviceIdentityMountPointsFn, serviceIdentityDatasetBoundariesFn
			serviceIdentityMountPointsFn = func() ([]string, error) {
				if tt.mount {
					return []string{nested}, nil
				}
				return nil, nil
			}
			serviceIdentityDatasetBoundariesFn = func(context.Context, zfsCommandRunner, string) ([]serviceIdentityDatasetBoundary, error) {
				if tt.dataset {
					return []serviceIdentityDatasetBoundary{{Dataset: "tank/api/nested", MountPoint: nested}}, nil
				}
				return nil, nil
			}
			t.Cleanup(func() {
				serviceIdentityMountPointsFn, serviceIdentityDatasetBoundariesFn = oldMounts, oldDatasets
			})
			migration := &serviceIdentityMigration{server: server}
			_, err := migration.materializeRoot(context.Background(), serviceRootMigrationPlan{
				ServiceName: "api", OldRoot: oldRoot, OldRootZFS: "tank/api", NewRoot: targetRoot, Mode: serviceRootMigrationCopy,
			}, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tt.wantText) {
				t.Fatalf("materializeRoot error = %v, want %q", err, tt.wantText)
			}
			if _, statErr := os.Stat(targetRoot); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("target root was created despite guarded copy failure: %v", statErr)
			}
		})
	}
}

func TestNewNativeServicePreflightFailureRemovesExactProvisionalRecord(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "root")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	provisional := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root, Publish: []string{"80:8080"}}
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, current *db.Service) error {
		*current = *provisional.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	identity := db.ServiceIdentity{RequestedUser: "1000", RequestedGroup: "1000", UID: 1000, GID: 1000}
	_, err := server.migrateServiceIdentity(context.Background(), serviceIdentityMigrationRequest{
		Service: "api", Requested: "1000", Target: resolvedServiceIdentity{Persisted: identity},
		TargetService: provisional, StartNew: true, PredecessorAbsent: true,
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "privileged host port 80") {
		t.Fatalf("migration error = %v, want privileged port rejection", err)
	}
	if _, viewErr := server.serviceView("api"); !errors.Is(viewErr, errServiceNotFound) {
		t.Fatalf("provisional service survived failed preflight: %v", viewErr)
	}
}

func TestRewriteServiceIdentityUnitAcceptsGeneratedInstallSection(t *testing.T) {
	root := t.TempDir()
	unit := &svc.SystemdUnit{
		Name:             "api",
		Executable:       "/srv/api/bin/api",
		WorkingDirectory: "/srv/api/data",
		User:             "root",
		Group:            "root",
	}
	artifacts, err := unit.WriteOutUnitFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(artifacts[db.ArtifactSystemdUnit])
	if err != nil {
		t.Fatal(err)
	}

	identity := db.ServiceIdentity{RequestedUser: "app", RequestedGroup: "app", UID: 1001, GID: 1001}
	rewritten, err := rewriteServiceIdentityUnit(string(raw), identity, "/srv/new-api")
	if err != nil {
		t.Fatalf("rewrite generated unit: %v", err)
	}
	directives := serviceIdentityUnitDirectives(rewritten)
	if directives["User"] != "app" || directives["Group"] != "app" || directives["WorkingDirectory"] != "/srv/new-api/data" {
		t.Fatalf("rewritten directives = %#v", directives)
	}
	if !strings.Contains(rewritten, "Environment=HOME=/srv/new-api/data USER=app LOGNAME=app SHELL=/bin/sh\n") {
		t.Fatalf("rewritten unit has wrong identity environment:\n%s", rewritten)
	}
	if !strings.Contains(rewritten, "[Install]\n") {
		t.Fatalf("rewritten unit dropped generated [Install] section:\n%s", rewritten)
	}
}
