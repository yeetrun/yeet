// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
)

func TestHostStorageTransactionCreateIsAtomicPrivateAndMirrored(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("MkdirAll source: %v", err)
	}
	databasePath := filepath.Join(source, "db.json")
	databaseRaw := []byte("{\n  \"dataVersion\": 1\n}\n")
	if err := os.WriteFile(databasePath, databaseRaw, 0o644); err != nil {
		t.Fatalf("WriteFile database: %v", err)
	}
	unitPath := filepath.Join(root, "systemd", "api.service")
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatalf("MkdirAll systemd: %v", err)
	}
	unitRaw := []byte("[Service]\nExecStart=/source/api\n")
	if err := os.WriteFile(unitPath, unitRaw, 0o644); err != nil {
		t.Fatalf("WriteFile unit: %v", err)
	}
	plan := testHostStorageTransactionPlan(source, target, "api")

	tx, err := createHostStorageTransaction(context.Background(), plan, databasePath, []string{unitPath}, []string{"api"})
	if err != nil {
		t.Fatalf("createHostStorageTransaction: %v", err)
	}
	if tx.Version != hostStorageTransactionVersion || tx.ID == "" || tx.Phase != hostStoragePhasePrepared {
		t.Fatalf("transaction identity = version %d id %q phase %q", tx.Version, tx.ID, tx.Phase)
	}
	if tx.CatchAuthority != hostStorageCatchAuthoritySource {
		t.Fatalf("CatchAuthority = %q, want source", tx.CatchAuthority)
	}
	if !slices.Equal(tx.PreviouslyRunning, []string{"api"}) {
		t.Fatalf("PreviouslyRunning = %#v, want api", tx.PreviouslyRunning)
	}
	if tx.SourceJournal != hostStorageTransactionPath(source, tx.ID) {
		t.Fatalf("SourceJournal = %q", tx.SourceJournal)
	}
	if tx.TargetJournal != hostStorageTransactionPath(target, tx.ID) {
		t.Fatalf("TargetJournal = %q", tx.TargetJournal)
	}
	assertHostStorageTransactionPathMode(t, filepath.Dir(tx.SourceJournal), 0o700)
	assertHostStorageTransactionPathMode(t, filepath.Dir(tx.TargetJournal), 0o700)
	assertHostStorageTransactionPathMode(t, tx.SourceJournal, 0o600)
	assertHostStorageTransactionPathMode(t, tx.TargetJournal, 0o600)
	assertHostStorageTransactionPathMode(t, tx.DatabaseBackup, 0o600)

	if got, readErr := os.ReadFile(tx.DatabaseBackup); readErr != nil || string(got) != string(databaseRaw) {
		t.Fatalf("database backup = %q, %v", got, readErr)
	}
	unitBackup := tx.UnitBackups[unitPath]
	if got, readErr := os.ReadFile(unitBackup); readErr != nil || string(got) != string(unitRaw) {
		t.Fatalf("unit backup = %q, %v", got, readErr)
	}
	for _, journal := range []string{tx.SourceJournal, tx.TargetJournal} {
		loaded, loadErr := loadHostStorageTransaction(journal)
		if loadErr != nil {
			t.Fatalf("loadHostStorageTransaction(%q): %v", journal, loadErr)
		}
		if loaded.ID != tx.ID || loaded.Phase != hostStoragePhasePrepared || loaded.Plan.Legacy.SourceRoot != plan.Legacy.SourceRoot {
			t.Fatalf("loaded mirrored transaction = %#v", loaded)
		}
		matches, globErr := filepath.Glob(filepath.Join(filepath.Dir(journal), ".transaction-*"))
		if globErr != nil || len(matches) != 0 {
			t.Fatalf("temporary transaction files = %#v, %v", matches, globErr)
		}
	}
}

func TestHostStorageTransactionCreationSyncsEveryJournalParent(t *testing.T) {
	for _, failParent := range []string{"source", "migrations", "host-storage"} {
		t.Run(failParent, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source")
			if err := os.MkdirAll(source, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "db.json"), []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			wantParent := source
			switch failParent {
			case "migrations":
				wantParent = filepath.Join(source, "migrations")
			case "host-storage":
				wantParent = filepath.Join(source, "migrations", "host-storage")
			}
			oldSync := hostStorageSyncRootFn
			var synced []string
			hostStorageSyncRootFn = func(root *os.Root) error {
				synced = append(synced, filepath.Clean(root.Name()))
				if filepath.Clean(root.Name()) == filepath.Clean(wantParent) {
					return errors.New("injected journal parent sync failure")
				}
				return oldSync(root)
			}
			t.Cleanup(func() { hostStorageSyncRootFn = oldSync })

			_, err := createHostStorageTransaction(context.Background(), testHostStorageTransactionPlan(source, source), filepath.Join(source, "db.json"), nil, nil)
			if err == nil || !strings.Contains(err.Error(), "injected journal parent sync failure") {
				t.Fatalf("createHostStorageTransaction error = %v, want parent sync failure at %s; synced %#v", err, wantParent, synced)
			}
		})
	}
}

func TestHostStorageTransactionAdvancePersistsBothCopiesBeforeReturning(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(source, "db.json")
	if err := os.WriteFile(databasePath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tx, err := createHostStorageTransaction(context.Background(), testHostStorageTransactionPlan(source, target), databasePath, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	oldSync := hostStorageFsyncParentFn
	var synced []string
	hostStorageFsyncParentFn = func(path string) error {
		synced = append(synced, filepath.Dir(path))
		return oldSync(path)
	}
	t.Cleanup(func() { hostStorageFsyncParentFn = oldSync })

	if err := advanceHostStorageTransaction(tx, hostStoragePhaseServicesStopped); err != nil {
		t.Fatalf("advanceHostStorageTransaction: %v", err)
	}
	for _, journal := range []string{tx.SourceJournal, tx.TargetJournal} {
		loaded, loadErr := loadHostStorageTransaction(journal)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if loaded.Phase != hostStoragePhaseServicesStopped {
			t.Fatalf("%s phase = %q, want services-stopped", journal, loaded.Phase)
		}
		if !slices.Contains(synced, filepath.Dir(journal)) {
			t.Fatalf("fsynced parents = %#v, want %q", synced, filepath.Dir(journal))
		}
	}
}

func TestHostStorageCatchSwitchIntentIsDurableBeforeRestartScheduling(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	applier, ops := newTestHostStorageApplier(t, Config{
		RootDir:      source,
		ServicesRoot: filepath.Join(source, "services"),
	}, nil)
	plan := testHostStorageTransactionPlan(source, target)
	plan.RequiresRestart = true

	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	databaseRaw, err := os.ReadFile(filepath.Join(source, "db.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "db.json"), databaseRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	tx, err := createHostStorageTransaction(context.Background(), plan, filepath.Join(source, "db.json"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := advanceHostStorageTransaction(tx, hostStoragePhaseTargetReady); err != nil {
		t.Fatal(err)
	}

	lastDurable, err := os.ReadFile(tx.SourceJournal)
	if err != nil {
		t.Fatal(err)
	}
	crashErr := errors.New("simulated crash before post-switch journal durability")
	scheduled := false
	oldSync := hostStorageFsyncParentFn
	hostStorageFsyncParentFn = func(path string) error {
		if filepath.Clean(path) != filepath.Clean(tx.SourceJournal) {
			return oldSync(path)
		}
		if !scheduled {
			if err := oldSync(path); err != nil {
				return err
			}
			lastDurable, err = os.ReadFile(path)
			return err
		}
		if err := os.WriteFile(path, lastDurable, 0o600); err != nil {
			return err
		}
		return crashErr
	}
	t.Cleanup(func() { hostStorageFsyncParentFn = oldSync })
	applier.ops.restartCatch = func(_ context.Context, _ hostStorageInstallRequest, _ io.Writer) error {
		ops.calls = append(ops.calls, "restart-catch")
		scheduled = true
		return errHostStorageCatchRestartScheduled
	}

	var result catchrpc.HostStorageApplyResult
	err = applier.finishHostStorageApply(context.Background(), plan, nil, tx, nil, &result)
	if err == nil || !strings.Contains(err.Error(), crashErr.Error()) {
		t.Fatalf("finishHostStorageApply error = %v, want simulated durability failure", err)
	}
	hostStorageFsyncParentFn = oldSync

	loaded, err := loadHostStorageTransaction(tx.SourceJournal)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Phase != hostStorageTransactionPhase("catch-switching") || loaded.CatchAuthority != hostStorageCatchAuthorityTarget {
		t.Fatalf("durable switch intent = phase %q authority %q, want catch-switching target", loaded.Phase, loaded.CatchAuthority)
	}
	if tx.Phase != loaded.Phase || tx.CatchAuthority != loaded.CatchAuthority {
		t.Fatalf("in-memory transaction = phase %q authority %q, durable = phase %q authority %q", tx.Phase, tx.CatchAuthority, loaded.Phase, loaded.CatchAuthority)
	}

	applier.ops.restartCatch = ops.restartCatch
	err = applier.rollbackHostStorageTransaction(context.Background(), loaded, errors.New("resume interrupted catch switch"), nil)
	if err == nil || !strings.Contains(err.Error(), "rollback completed") {
		t.Fatalf("rollback error = %v, want completed rollback", err)
	}
	sourceState := source + ":" + filepath.Join(source, "services")
	if !slices.Contains(ops.calls, "install-catch:"+sourceState) ||
		slices.Contains(ops.calls, "verify-info:"+sourceState) {
		t.Fatalf("rollback calls = %#v, want source catch install and current-process proof without config-derived verification", ops.calls)
	}
}

func TestHostStorageTransactionLoadRejectsCorruptAndUnsupportedRecords(t *testing.T) {
	tests := []struct {
		name string
		id   string
		raw  string
		want string
	}{
		{name: "corrupt", id: "corrupt", raw: "{not-json", want: "decode"},
		{name: "unsupported version", id: "old", raw: `{"version":99,"id":"old","phase":"prepared"}`, want: "unsupported host storage transaction version"},
		{name: "unknown phase", id: "bad-phase", raw: `{"version":1,"id":"bad-phase","phase":"future"}`, want: "unknown host storage transaction phase"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := hostStorageTransactionPath(t.TempDir(), tt.id)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tt.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadHostStorageTransaction(path); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("load error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestHostStorageTransactionLoadRejectsMismatchedJournalLocation(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(source, "db.json")
	if err := os.WriteFile(databasePath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tx, err := createHostStorageTransaction(context.Background(), testHostStorageTransactionPlan(source, target), databasePath, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	tx.ID = "different-id"
	if err := writeHostStorageTransactionFile(tx.SourceJournal, tx); err != nil {
		t.Fatal(err)
	}
	if _, err := loadHostStorageTransaction(tx.SourceJournal); err == nil || !strings.Contains(err.Error(), "journal location") {
		t.Fatalf("load error = %v, want mismatched journal location rejection", err)
	}
}

func TestHostStorageTransactionRejectsSymlinkedJournalTree(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "db.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(source, "migrations")); err != nil {
		t.Fatal(err)
	}

	_, err := createHostStorageTransaction(context.Background(), testHostStorageTransactionPlan(source, target), filepath.Join(source, "db.json"), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("createHostStorageTransaction error = %v, want symlink rejection", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "host-storage")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("redirected journal tree exists outside source: %v", err)
	}
}

func TestHostStorageTransactionLoadRejectsJournalSymlinkAndHardlink(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source")
			if err := os.MkdirAll(source, 0o700); err != nil {
				t.Fatal(err)
			}
			databasePath := filepath.Join(source, "db.json")
			if err := os.WriteFile(databasePath, []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			tx, err := createHostStorageTransaction(context.Background(), testHostStorageTransactionPlan(source, source), databasePath, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			extra := filepath.Join(filepath.Dir(tx.SourceJournal), "transaction.extra")
			switch kind {
			case "symlink":
				if err := os.Rename(tx.SourceJournal, extra); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Base(extra), tx.SourceJournal); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(tx.SourceJournal, extra); err != nil {
					t.Fatal(err)
				}
			}

			if _, err := loadHostStorageTransaction(tx.SourceJournal); err == nil || !strings.Contains(err.Error(), kind) {
				t.Fatalf("loadHostStorageTransaction error = %v, want %s rejection", err, kind)
			}
		})
	}
}

func TestHostStorageTransactionLoadRejectsInsecureJournalPermissions(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "db.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tx, err := createHostStorageTransaction(context.Background(), testHostStorageTransactionPlan(root, root), filepath.Join(root, "db.json"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(tx.SourceJournal, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := loadHostStorageTransaction(tx.SourceJournal); err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatalf("loadHostStorageTransaction error = %v, want insecure permissions rejection", err)
	}
}

func TestHostStorageTransactionJournalExistsStrictlyValidatesFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "db.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tx, err := createHostStorageTransaction(context.Background(), testHostStorageTransactionPlan(root, root), filepath.Join(root, "db.json"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	exists, err := hostStorageTransactionJournalExists(tx.SourceJournal)
	if err != nil || !exists {
		t.Fatalf("valid journal exists = %t, %v; want true", exists, err)
	}
	missing := hostStorageTransactionPath(filepath.Join(root, "missing"), "missing")
	exists, err = hostStorageTransactionJournalExists(missing)
	if err != nil || exists {
		t.Fatalf("missing journal exists = %t, %v; want false", exists, err)
	}
	if err := os.Link(tx.SourceJournal, filepath.Join(filepath.Dir(tx.SourceJournal), "transaction.alias")); err != nil {
		t.Fatal(err)
	}
	exists, err = hostStorageTransactionJournalExists(tx.SourceJournal)
	if err == nil || exists || !strings.Contains(err.Error(), "hardlink") {
		t.Fatalf("hardlinked journal exists = %t, %v; want strict hardlink rejection", exists, err)
	}
}

func TestHostStorageTransactionDetectsTransactionDirectoryReplacement(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(source, "db.json")
	if err := os.WriteFile(databasePath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tx, err := createHostStorageTransaction(context.Background(), testHostStorageTransactionPlan(source, source), databasePath, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	txDir := filepath.Dir(tx.SourceJournal)
	displaced := txDir + ".displaced"
	oldSync := hostStorageFsyncParentFn
	replaced := false
	hostStorageFsyncParentFn = func(path string) error {
		if !replaced && filepath.Clean(path) == filepath.Clean(tx.SourceJournal) {
			replaced = true
			if err := os.Rename(txDir, displaced); err != nil {
				return err
			}
			return os.Symlink(displaced, txDir)
		}
		return oldSync(path)
	}
	t.Cleanup(func() { hostStorageFsyncParentFn = oldSync })

	err = advanceHostStorageTransaction(tx, hostStoragePhaseServicesStopped)
	if err == nil || (!strings.Contains(err.Error(), "replaced") && !strings.Contains(err.Error(), "symlink")) {
		t.Fatalf("advanceHostStorageTransaction error = %v, want transaction-directory replacement rejection", err)
	}
}

func TestHostStorageTransactionRejectsHardlinkedBackupInput(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(source, "db.json")
	if err := os.WriteFile(databasePath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(databasePath, filepath.Join(source, "db.alias")); err != nil {
		t.Fatal(err)
	}

	_, err := createHostStorageTransaction(context.Background(), testHostStorageTransactionPlan(source, source), databasePath, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "hardlink") {
		t.Fatalf("createHostStorageTransaction error = %v, want hardlink rejection", err)
	}
}

func TestHostStorageTransactionRejectsBackupParentReplacementDuringOpen(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	displaced := filepath.Join(root, "source.displaced")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "db.json"), []byte("{\"original\":true}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldOpen := hostStorageOpenBackupRootFn
	replaced := false
	hostStorageOpenBackupRootFn = func(path string) (*os.Root, error) {
		if !replaced && filepath.Clean(path) == filepath.Clean(source) {
			replaced = true
			if err := os.Rename(source, displaced); err != nil {
				return nil, err
			}
			if err := os.MkdirAll(source, 0o700); err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(source, "db.json"), []byte("{\"forged\":true}\n"), 0o600); err != nil {
				return nil, err
			}
		}
		return oldOpen(path)
	}
	t.Cleanup(func() { hostStorageOpenBackupRootFn = oldOpen })

	_, err := createHostStorageTransaction(context.Background(), testHostStorageTransactionPlan(source, source), filepath.Join(source, "db.json"), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "parent") || !strings.Contains(err.Error(), "replaced") {
		t.Fatalf("createHostStorageTransaction error = %v, want backup parent replacement rejection", err)
	}
}

func TestHostStorageRollbackRejectsForgedBackupSymlinkAndHardlink(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source")
			applier, _ := newTestHostStorageApplier(t, Config{RootDir: source, ServicesRoot: filepath.Join(source, "services")}, nil)
			databasePath := filepath.Join(source, "db.json")
			original, err := os.ReadFile(databasePath)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := createHostStorageTransaction(context.Background(), testHostStorageTransactionPlan(source, source), databasePath, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			forged := filepath.Join(root, "forged-db.json")
			if err := os.WriteFile(forged, []byte("{\"dataVersion\":1,\"services\":{}}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "symlink":
				if err := os.Remove(tx.DatabaseBackup); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(forged, tx.DatabaseBackup); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Remove(tx.DatabaseBackup); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(forged, tx.DatabaseBackup); err != nil {
					t.Fatal(err)
				}
			}

			err = applier.rollbackHostStorageTransaction(context.Background(), tx, errors.New("force rollback"), nil)
			if err == nil || !strings.Contains(err.Error(), kind) {
				t.Fatalf("rollback error = %v, want forged %s backup rejection", err, kind)
			}
			if got, readErr := os.ReadFile(databasePath); readErr != nil || string(got) != string(original) {
				t.Fatalf("database changed after forged backup rejection: %q, %v", got, readErr)
			}
		})
	}
}

func TestHostStorageRollbackRejectsForgedUnitBackupSymlinkAndHardlink(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source")
			unitPath := filepath.Join(root, "systemd", "api.service")
			if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(unitPath, []byte("original unit\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			applier, _ := newTestHostStorageApplier(t, Config{RootDir: source, ServicesRoot: filepath.Join(source, "services")}, nil)
			tx, err := createHostStorageTransaction(context.Background(), testHostStorageTransactionPlan(source, source), filepath.Join(source, "db.json"), []string{unitPath}, nil)
			if err != nil {
				t.Fatal(err)
			}
			backup := tx.UnitBackups[unitPath]
			forged := filepath.Join(root, "forged.unit")
			if err := os.WriteFile(forged, []byte("forged unit\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(backup); err != nil {
				t.Fatal(err)
			}
			if kind == "symlink" {
				err = os.Symlink(forged, backup)
			} else {
				err = os.Link(forged, backup)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(unitPath, []byte("replacement unit\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			err = applier.rollbackHostStorageTransaction(context.Background(), tx, errors.New("force rollback"), nil)
			if err == nil || !strings.Contains(err.Error(), kind) {
				t.Fatalf("rollback error = %v, want forged %s unit backup rejection", err, kind)
			}
			if got, readErr := os.ReadFile(unitPath); readErr != nil || string(got) != "replacement unit\n" {
				t.Fatalf("unit changed after forged backup rejection: %q, %v", got, readErr)
			}
		})
	}
}

func TestHostStorageTransactionMirrorFailureLeavesTerminalSourceRecord(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target-file")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "db.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := createHostStorageTransaction(context.Background(), testHostStorageTransactionPlan(source, target), filepath.Join(source, "db.json"), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "mirror prepared") {
		t.Fatalf("create error = %v, want mirror failure", err)
	}
	paths, globErr := hostStorageTransactionJournalPaths(source)
	if globErr != nil || len(paths) != 1 {
		t.Fatalf("source journals = %#v, %v, want one terminal record", paths, globErr)
	}
	tx, loadErr := loadHostStorageTransaction(paths[0])
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if tx.Phase != hostStoragePhaseRolledBack || !strings.Contains(tx.LastError, "mirror prepared") {
		t.Fatalf("source transaction = %#v, want rolled-back mirror failure", tx)
	}
}

func TestHostStorageRollbackRestoresJournaledDatabaseUnitsAndRunningSet(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	systemdDir := filepath.Join(root, "systemd")
	oldSystemdDir := systemdSystemDir
	systemdSystemDir = systemdDir
	t.Cleanup(func() { systemdSystemDir = oldSystemdDir })
	if err := os.MkdirAll(systemdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(systemdDir, "api.service")
	originalUnit := []byte("[Service]\nExecStart=/source/api\n")
	if err := os.WriteFile(unitPath, originalUnit, 0o644); err != nil {
		t.Fatal(err)
	}
	applier, ops := newTestHostStorageApplier(t, Config{RootDir: source, ServicesRoot: filepath.Join(source, "services")}, map[string]*db.Service{
		"api":    {Name: "api", ServiceType: db.ServiceTypeSystemd},
		"worker": {Name: "worker", ServiceType: db.ServiceTypeSystemd},
	})
	originalDatabase, err := os.ReadFile(filepath.Join(source, "db.json"))
	if err != nil {
		t.Fatal(err)
	}
	plan := testHostStorageTransactionPlan(source, filepath.Join(root, "target"), "api", "worker")
	tx, err := createHostStorageTransaction(context.Background(), plan, filepath.Join(source, "db.json"), []string{unitPath}, []string{"api"})
	if err != nil {
		t.Fatal(err)
	}
	tx.PreviouslyRunning = []string{"api"}
	tx.StoppedServices = []string{"api"}
	tx.RestartedServices = []string{"api"}
	tx.CatchAuthority = hostStorageCatchAuthorityTarget
	applier.runningCatchState = plan.Desired
	applier.runningCatchStateKnown = true
	if err := advanceHostStorageTransaction(tx, hostStoragePhaseCatchSwitched); err != nil {
		t.Fatal(err)
	}
	if _, err := applier.store.MutateData(func(data *db.Data) error {
		data.Services["replacement"] = &db.Service{Name: "replacement", ServiceType: db.ServiceTypeSystemd}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte("replacement unit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ops.running["api"] = true
	ops.running["worker"] = false

	cause := errors.New("rewrite target failed")
	err = applier.rollbackHostStorageTransaction(context.Background(), tx, cause, nil)
	if err == nil || !strings.Contains(err.Error(), cause.Error()) || !strings.Contains(err.Error(), "rollback completed") {
		t.Fatalf("rollback error = %v, want original cause and completed status", err)
	}
	if got, readErr := os.ReadFile(filepath.Join(source, "db.json")); readErr != nil || string(got) != string(originalDatabase) {
		t.Fatalf("restored database = %q, %v; want exact original", got, readErr)
	}
	if got, readErr := os.ReadFile(unitPath); readErr != nil || string(got) != string(originalUnit) {
		t.Fatalf("restored unit = %q, %v; want exact original", got, readErr)
	}
	if !ops.running["api"] || ops.running["worker"] {
		t.Fatalf("running state = %#v, want only api running", ops.running)
	}
	assertCallOrder(t, ops.calls, "stop:api", "daemon-reload", "start:api", "install-catch:"+source+":"+filepath.Join(source, "services"), "restart-catch")
	loaded, err := loadHostStorageTransaction(tx.SourceJournal)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Phase != hostStoragePhaseRolledBack || loaded.CatchAuthority != hostStorageCatchAuthoritySource || loaded.LastError != cause.Error() {
		t.Fatalf("rolled back journal = %#v", loaded)
	}
}

func TestHostStorageRollbackCombinesOriginalAndRollbackFailures(t *testing.T) {
	root := t.TempDir()
	applier, ops := newTestHostStorageApplier(t, Config{RootDir: root}, map[string]*db.Service{
		"api": {Name: "api", ServiceType: db.ServiceTypeSystemd},
	})
	tx, err := createHostStorageTransaction(context.Background(), testHostStorageTransactionPlan(root, root, "api"), filepath.Join(root, "db.json"), nil, []string{"api"})
	if err != nil {
		t.Fatal(err)
	}
	tx.StoppedServices = []string{"api"}
	ops.startErr["api"] = errors.New("start old api failed")

	err = applier.rollbackHostStorageTransaction(context.Background(), tx, errors.New("original apply failure"), nil)
	if err == nil || !strings.Contains(err.Error(), "original apply failure") || !strings.Contains(err.Error(), "start old api failed") {
		t.Fatalf("rollback error = %v, want joined original and rollback failures", err)
	}
	loaded, loadErr := loadHostStorageTransaction(tx.SourceJournal)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if loaded.Phase == hostStoragePhaseRolledBack {
		t.Fatalf("phase = rolled-back after incomplete rollback")
	}
}

func TestHostStorageResumeOnlyAutomaticallyAcceptsCleanupPending(t *testing.T) {
	phases := []hostStorageTransactionPhase{
		hostStoragePhasePrepared,
		hostStoragePhaseServicesStopped,
		hostStoragePhaseTargetReady,
		hostStoragePhaseCatchSwitching,
		hostStoragePhaseSourceRestartPending,
		hostStoragePhaseCatchSwitched,
		hostStoragePhaseValidated,
		hostStoragePhaseCleanupPending,
		hostStoragePhaseComplete,
		hostStoragePhaseRolledBack,
	}
	for _, phase := range phases {
		t.Run(string(phase), func(t *testing.T) {
			tx := &hostStorageTransaction{Version: hostStorageTransactionVersion, ID: "tx", Phase: phase, SourceJournal: "/source/transaction.json"}
			err := resumeHostStorageTransaction(context.Background(), tx)
			if phase == hostStoragePhaseCleanupPending || phase == hostStoragePhaseComplete || phase == hostStoragePhaseRolledBack {
				if err != nil {
					t.Fatalf("resumeHostStorageTransaction: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tx.SourceJournal) || !strings.Contains(err.Error(), "yeet host set") {
				t.Fatalf("resume error = %v, want journal path and recovery command", err)
			}
		})
	}
}

func TestHostStorageResumeAfterCrashAtEveryJournalPhase(t *testing.T) {
	phases := []hostStorageTransactionPhase{
		hostStoragePhasePrepared,
		hostStoragePhaseServicesStopped,
		hostStoragePhaseTargetReady,
		hostStoragePhaseCatchSwitching,
		hostStoragePhaseSourceRestartPending,
		hostStoragePhaseCatchSwitched,
		hostStoragePhaseValidated,
		hostStoragePhaseCleanupPending,
		hostStoragePhaseComplete,
		hostStoragePhaseRolledBack,
	}
	for _, phase := range phases {
		t.Run(string(phase), func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source")
			target := filepath.Join(root, "target")
			if err := os.MkdirAll(source, 0o700); err != nil {
				t.Fatal(err)
			}
			databasePath := filepath.Join(source, "db.json")
			if err := os.WriteFile(databasePath, []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			tx, err := createHostStorageTransaction(context.Background(), testHostStorageTransactionPlan(source, target), databasePath, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := advanceHostStorageTransaction(tx, phase); err != nil {
				t.Fatal(err)
			}
			_, err = findHostStorageStartupRecovery(context.Background(), target)
			if phase == hostStoragePhaseCleanupPending || phase == hostStoragePhaseComplete || phase == hostStoragePhaseRolledBack {
				if err != nil {
					t.Fatalf("startup recovery: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tx.SourceJournal) {
				t.Fatalf("startup recovery error = %v, want authoritative source journal", err)
			}
		})
	}
}

func TestHostStorageResumeUsesTerminalSourceWhenTargetMirrorIsStale(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(source, "db.json")
	if err := os.WriteFile(databasePath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tx, err := createHostStorageTransaction(context.Background(), testHostStorageTransactionPlan(source, target), databasePath, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	staleTarget, err := os.ReadFile(tx.TargetJournal)
	if err != nil {
		t.Fatal(err)
	}
	if err := advanceHostStorageTransaction(tx, hostStoragePhaseRolledBack); err != nil {
		t.Fatal(err)
	}
	if err := writeHostStorageTransactionBytes(tx.TargetJournal, staleTarget, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := findHostStorageStartupRecovery(context.Background(), target); err != nil {
		t.Fatalf("startup recovery trusted stale target over terminal source: %v", err)
	}
}

func TestHostStorageTargetMirrorCannotReplaceMissingAuthoritativeSource(t *testing.T) {
	phases := []hostStorageTransactionPhase{
		hostStoragePhasePrepared,
		hostStoragePhaseServicesStopped,
		hostStoragePhaseTargetReady,
		hostStoragePhaseCatchSwitching,
		hostStoragePhaseSourceRestartPending,
		hostStoragePhaseCatchSwitched,
		hostStoragePhaseValidated,
		hostStoragePhaseCleanupPending,
		hostStoragePhaseComplete,
		hostStoragePhaseRolledBack,
	}
	for _, phase := range phases {
		for _, sourceState := range []string{"missing", "corrupt"} {
			t.Run(string(phase)+"/"+sourceState, func(t *testing.T) {
				root := t.TempDir()
				source := filepath.Join(root, "source")
				target := filepath.Join(root, "target")
				if err := os.MkdirAll(source, 0o700); err != nil {
					t.Fatal(err)
				}
				databasePath := filepath.Join(source, "db.json")
				if err := os.WriteFile(databasePath, []byte("{}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				tx, err := createHostStorageTransaction(context.Background(), testHostStorageTransactionPlan(source, target), databasePath, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				if err := advanceHostStorageTransaction(tx, phase); err != nil {
					t.Fatal(err)
				}
				switch sourceState {
				case "missing":
					if err := os.Remove(tx.SourceJournal); err != nil {
						t.Fatal(err)
					}
				case "corrupt":
					if err := writeHostStorageTransactionBytes(tx.SourceJournal, []byte("{corrupt\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}

				loaded, _, err := loadAuthoritativeHostStorageTransaction(tx.TargetJournal)
				if err == nil || !strings.Contains(err.Error(), "authoritative") || !strings.Contains(err.Error(), tx.SourceJournal) {
					t.Fatalf("loadAuthoritativeHostStorageTransaction = %#v, %v; want %s authoritative source error naming %s", loaded, err, sourceState, tx.SourceJournal)
				}
			})
		}
	}
}

func TestHostStorageRecoveryCommandRoundTripsQuotedAndMixedTargets(t *testing.T) {
	filesystemPath := "/srv/yeet data/quote'and;$dollar"
	tests := map[string]catchrpc.HostStoragePlan{
		"filesystem data in mixed state": {
			Current: catchrpc.HostStorageState{DataDir: "/old-data", ServicesRoot: "/tank/services", ServicesZFS: true},
			Desired: catchrpc.HostStorageState{DataDir: filesystemPath, ServicesRoot: "/tank/services", ServicesZFS: true},
		},
		"zfs data in mixed state": {
			Current: catchrpc.HostStorageState{DataDir: "tank/old-data", DataDirZFS: true, ServicesRoot: "/srv/services"},
			Desired: catchrpc.HostStorageState{DataDir: "tank/new data;$literal", DataDirZFS: true, ServicesRoot: "/srv/services"},
		},
		"filesystem services in mixed state": {
			Current: catchrpc.HostStorageState{DataDir: "tank/data", DataDirZFS: true, ServicesRoot: "/old-services"},
			Desired: catchrpc.HostStorageState{DataDir: "tank/data", DataDirZFS: true, ServicesRoot: filesystemPath},
		},
		"both zfs targets": {
			Current: catchrpc.HostStorageState{DataDir: "tank/old-data", DataDirZFS: true, ServicesRoot: "tank/old-services", ServicesZFS: true},
			Desired: catchrpc.HostStorageState{DataDir: "tank/new-data", DataDirZFS: true, ServicesRoot: "tank/new-services", ServicesZFS: true},
		},
	}
	for name, plan := range tests {
		t.Run(name, func(t *testing.T) {
			command := hostStorageTransactionRecoveryCommand(plan)
			args := parseHostStorageRecoveryShellCommand(t, command)
			if len(args) < 3 || !slices.Equal(args[:3], []string{"yeet", "host", "set"}) {
				t.Fatalf("recovery command argv = %#v, want yeet host set", args)
			}
			flags, remaining, err := cli.ParseHostSet(args[3:])
			if err != nil || len(remaining) != 0 {
				t.Fatalf("ParseHostSet(%#v) = %#v, %#v, %v", args[3:], flags, remaining, err)
			}
			if !flags.Yes {
				t.Fatalf("recovery flags = %#v, want --yes", flags)
			}
			req := catchrpc.HostStoragePlanRequest{Set: catchrpc.HostStorageSetRequest{
				MigrateServices: catchrpc.HostStorageMigrateServices(flags.MigrateServices),
			}}
			if flags.DataDir != "" {
				req.Set.DataDir = &catchrpc.HostStorageTarget{Value: flags.DataDir, ZFS: flags.ZFS}
			}
			if flags.ServicesRoot != "" {
				req.Set.ServicesRoot = &catchrpc.HostStorageTarget{Value: flags.ServicesRoot, ZFS: flags.ZFS}
			}
			if !hostStorageRecoveryRequestMatches(req, plan) {
				t.Fatalf("recovery command %q parsed to %#v, which does not reproduce desired state %#v", command, req, plan.Desired)
			}
		})
	}
}

func parseHostStorageRecoveryShellCommand(t *testing.T, command string) []string {
	t.Helper()
	script := "set -- " + command + "\nprintf '%s\\000' \"$@\""
	out, err := exec.Command("sh", "-c", script).Output()
	if err != nil {
		t.Fatalf("parse recovery command %q: %v", command, err)
	}
	args := strings.Split(string(out), "\x00")
	return args[:len(args)-1]
}

func testHostStorageTransactionPlan(source, target string, services ...string) catchrpc.HostStoragePlan {
	moves := make([]catchrpc.HostStorageServiceMove, 0, len(services))
	for _, name := range services {
		moves = append(moves, catchrpc.HostStorageServiceMove{Name: name, From: filepath.Join(source, "services", name), To: filepath.Join(target, "services", name)})
	}
	return catchrpc.HostStoragePlan{
		Current:       catchrpc.HostStorageState{DataDir: source, ServicesRoot: filepath.Join(source, "services")},
		Desired:       catchrpc.HostStorageState{DataDir: target, ServicesRoot: filepath.Join(target, "services")},
		DataDirAction: catchrpc.HostStorageDataDirAction{Move: source != target, From: source, To: target},
		ServicesAction: catchrpc.HostStorageServicesAction{
			Mode:             catchrpc.HostStorageMigrateAll,
			From:             filepath.Join(source, "services"),
			To:               filepath.Join(target, "services"),
			AffectedServices: moves,
		},
		Legacy: catchrpc.HostStorageLegacyPlan{Eligible: true, SourceRoot: source, TargetRoot: target, CleanupAllowed: true},
	}
}

func assertHostStorageTransactionPathMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q): %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}

func TestHostStorageTransactionJSONHasNoImplicitRecoveryFields(t *testing.T) {
	tx := hostStorageTransaction{
		Version:           hostStorageTransactionVersion,
		ID:                "tx",
		Phase:             hostStoragePhasePrepared,
		Plan:              catchrpc.HostStoragePlan{},
		SourceJournal:     "/source/transaction.json",
		DatabaseBackup:    "/source/db.before.json",
		UnitBackups:       map[string]string{},
		PreviouslyRunning: []string{},
		CatchAuthority:    hostStorageCatchAuthoritySource,
	}
	raw, err := json.Marshal(tx)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"version"`, `"id"`, `"phase"`, `"plan"`, `"sourceJournal"`, `"databaseBackup"`, `"unitBackups"`, `"previouslyRunning"`, `"catchAuthority"`} {
		if !strings.Contains(string(raw), field) {
			t.Fatalf("journal JSON %s missing %s", raw, field)
		}
	}
}
