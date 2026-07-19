// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
	"golang.org/x/sys/unix"
)

func TestVMRuntimeJournalStoreLocksPersistentRoot(t *testing.T) {
	root := t.TempDir()
	first := openTestVMRuntimeJournalStore(t, root)
	defer func() { _ = first.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	if _, err := openVMRuntimeJournalStore(ctx, root, testVMRuntimeJournalStoreDeps()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second open error = %v, want context deadline", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}
	second := openTestVMRuntimeJournalStore(t, root)
	if err := second.Close(); err != nil {
		t.Fatalf("close second store: %v", err)
	}

	lockInfo, err := os.Lstat(filepath.Join(root, vmRuntimeJournalLockFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm() != 0o600 {
		t.Fatalf("lock mode = %v, want regular 0600", lockInfo.Mode())
	}
	journalInfo, err := os.Lstat(filepath.Join(root, vmRuntimeJournalDirName))
	if err != nil {
		t.Fatal(err)
	}
	if !journalInfo.IsDir() || journalInfo.Mode().Perm() != 0o700 {
		t.Fatalf("journal directory mode = %v, want directory 0700", journalInfo.Mode())
	}
}

func TestVMRuntimeJournalLockAcceptsInheritedGroupOnly(t *testing.T) {
	uid := uint32(os.Geteuid())
	if err := validateVMRuntimeJournalLockMetadata(vmRuntimeJournalFileMetadata{
		mode: unix.S_IFREG | 0o600,
		uid:  uid,
		gid:  uint32(os.Getegid()) + 1,
	}, uid); err != nil {
		t.Fatalf("validate inherited group: %v", err)
	}
	for _, metadata := range []vmRuntimeJournalFileMetadata{
		{mode: unix.S_IFLNK | 0o600, uid: uid},
		{mode: unix.S_IFREG | 0o640, uid: uid},
		{mode: unix.S_IFREG | 0o600, uid: uid + 1},
	} {
		if err := validateVMRuntimeJournalLockMetadata(metadata, uid); err == nil {
			t.Fatalf("metadata %#v unexpectedly accepted", metadata)
		}
	}
}

func TestVMRuntimeJournalStoreRejectsUntrustedDirectory(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		target := t.TempDir()
		if err := os.Symlink(target, filepath.Join(root, vmRuntimeJournalDirName)); err != nil {
			t.Fatal(err)
		}
		if _, err := openVMRuntimeJournalStore(context.Background(), root, testVMRuntimeJournalStoreDeps()); err == nil {
			t.Fatal("open unexpectedly followed journal directory symlink")
		}
	})
	t.Run("permissions", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, vmRuntimeJournalDirName)
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := openVMRuntimeJournalStore(context.Background(), root, testVMRuntimeJournalStoreDeps()); err == nil || !strings.Contains(err.Error(), "0700") {
			t.Fatalf("open error = %v, want exact directory-mode refusal", err)
		}
	})
}

func TestVMRuntimeJournalStoreRejectsWhitespaceAliasedRoot(t *testing.T) {
	root := t.TempDir()
	if _, err := openVMRuntimeJournalStore(context.Background(), root+" ", testVMRuntimeJournalStoreDeps()); err == nil || !strings.Contains(err.Error(), "clean and absolute") {
		t.Fatalf("open error = %v, want whitespace-alias refusal", err)
	}
	if _, err := os.Lstat(filepath.Join(root, vmRuntimeJournalDirName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("trimmed root was mutated despite refusal: %v", err)
	}
}

func TestVMRuntimeJournalStoreSupportsSetgidCustomRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "setgid-zfs-root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Chmod(root, 0o2700); err != nil {
		t.Fatal(err)
	}
	var rootStat unix.Stat_t
	if err := unix.Stat(root, &rootStat); err != nil {
		t.Fatal(err)
	}
	if uint32(rootStat.Mode)&unix.S_ISGID == 0 {
		t.Skip("filesystem does not preserve setgid on the test directory")
	}
	store := openTestVMRuntimeJournalStore(t, root)
	records := validVMRuntimeJournalCohort(root, "alpha", "beta")
	if err := store.Prepare(records); err != nil {
		t.Fatalf("Prepare on setgid custom root: %v", err)
	}
	var journalStat unix.Stat_t
	if err := unix.Stat(filepath.Join(root, vmRuntimeJournalDirName), &journalStat); err != nil {
		t.Fatal(err)
	}
	journalMode := uint32(journalStat.Mode) & 0o7777
	if journalMode != 0o700 && journalMode != 0o2700 || journalStat.Gid != rootStat.Gid {
		t.Fatalf("journal mode/gid = %o/%d, want 0700 or inherited-setgid 02700 and gid %d", journalMode, journalStat.Gid, rootStat.Gid)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openTestVMRuntimeJournalStore(t, root)
	defer func() { _ = store.Close() }()
	assertVMRuntimeJournalStableState(t, store, vmRuntimeJournalStatePrepared)
}

func TestVMRuntimeJournalPreparePublishesCompleteSortedCohort(t *testing.T) {
	root := t.TempDir()
	store := openTestVMRuntimeJournalStore(t, root)
	defer func() { _ = store.Close() }()
	records := validVMRuntimeJournalCohort(root, "alpha", "beta")

	if err := store.Prepare(records); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	groups, err := store.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(groups))
	}
	got := groups[0]
	if got.TransactionID != records[0].TransactionID || !reflect.DeepEqual(got.Members, []string{"alpha", "beta"}) {
		t.Fatalf("group = %#v", got)
	}
	if len(got.Records) != 2 || got.Records[0].Service != "alpha" || got.Records[1].Service != "beta" {
		t.Fatalf("records = %#v, want service order alpha,beta", got.Records)
	}
	for _, record := range got.Records {
		want := records[0]
		if record.Service == "beta" {
			want = records[1]
		}
		if !reflect.DeepEqual(record, want) {
			t.Fatalf("record %s = %#v, want %#v", record.Service, record, want)
		}
		info, err := os.Lstat(filepath.Join(root, vmRuntimeJournalDirName, record.Service+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("journal %s mode = %v, want regular 0600", record.Service, info.Mode())
		}
	}
	realSyncFile := store.deps.syncFile
	syncedFiles := 0
	store.deps.syncFile = func(file *os.File) error {
		syncedFiles++
		return realSyncFile(file)
	}
	if err := store.Prepare(records); err != nil {
		t.Fatalf("idempotent Prepare: %v", err)
	}
	if syncedFiles != 3 {
		t.Fatalf("idempotent Prepare synced %d files, want two records and marker", syncedFiles)
	}
	groups, err = store.LoadAll()
	if err != nil || groups[0].Phase != vmRuntimeJournalPhaseStable {
		t.Fatalf("stable group after retry = %#v, error %v", groups, err)
	}
}

func TestVMRuntimeJournalPreparePreservesAbsentOldDescriptor(t *testing.T) {
	root := t.TempDir()
	store := openTestVMRuntimeJournalStore(t, root)
	defer func() { _ = store.Close() }()
	records := validVMRuntimeJournalCohort(root, "alpha")
	records[0].OldDescriptor = vmRuntimeJournalFile{Path: records[0].NewDescriptor.Path}
	if err := store.Prepare(records); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	groups, err := store.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if got := groups[0].Records[0].OldDescriptor; got.Exists || len(got.Contents) != 0 || got.Mode != 0 || got.SHA256 != "" {
		t.Fatalf("old descriptor = %#v, want exact absence", got)
	}
}

func TestVMRuntimeJournalPrepareRejectsContradictoryCohort(t *testing.T) {
	root := t.TempDir()
	store := openTestVMRuntimeJournalStore(t, root)
	defer func() { _ = store.Close() }()
	records := validVMRuntimeJournalCohort(root, "alpha", "beta")
	records[1].UpdatedAt = records[1].UpdatedAt.Add(time.Second)
	if err := store.Prepare(records); err == nil || !strings.Contains(err.Error(), "contradictory") {
		t.Fatalf("Prepare error = %v, want contradictory cohort refusal", err)
	}
	records = validVMRuntimeJournalCohort(root, "alpha", "beta")
	records[0].Members = []string{"beta", "alpha"}
	if err := store.Prepare(records); err == nil || !strings.Contains(err.Error(), "sorted") {
		t.Fatalf("Prepare error = %v, want unsorted-member refusal", err)
	}
}

func TestVMRuntimeJournalPrepareRetainsTypedPublishedCohortAfterSyncFailure(t *testing.T) {
	root := t.TempDir()
	store := openTestVMRuntimeJournalStore(t, root)
	syncErr := errors.New("injected journal directory sync failure")
	store.deps.syncDir = func(*os.File) error { return syncErr }

	err := store.Prepare(validVMRuntimeJournalCohort(root, "alpha", "beta"))
	var retained *vmRuntimeJournalPublicationRetainedError
	if !errors.As(err, &retained) || !errors.Is(err, syncErr) {
		t.Fatalf("Prepare error = %v, want typed retained publication wrapping sync error", err)
	}
	if got := retained.CanonicalPaths(); len(got) != 1 || !strings.Contains(got[0], "transaction-") {
		t.Fatalf("retained paths = %v, want verified canonical marker", got)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openTestVMRuntimeJournalStore(t, root)
	defer func() { _ = store.Close() }()
	groups, loadErr := store.LoadAll()
	if loadErr != nil || len(groups) != 1 || groups[0].Phase != vmRuntimeJournalPhasePublishing {
		t.Fatalf("retained cohort = %#v, error %v", groups, loadErr)
	}
	if err := store.Resume(validVMRuntimeJournalCohort(root, "alpha", "beta")[0].TransactionID); err != nil {
		t.Fatalf("resume prepared cohort: %v", err)
	}
	groups, loadErr = store.LoadAll()
	if loadErr != nil || len(groups) != 1 || len(groups[0].Records) != 2 || groups[0].Phase != vmRuntimeJournalPhaseStable {
		t.Fatalf("resumed cohort = %#v, error %v", groups, loadErr)
	}
}

func TestVMRuntimeJournalPrepareClassifiesMarkerRenameAmbiguity(t *testing.T) {
	t.Run("failure before marker publication is ordinary and clean", func(t *testing.T) {
		root := t.TempDir()
		store := openTestVMRuntimeJournalStore(t, root)
		defer func() { _ = store.Close() }()
		renameErr := errors.New("injected marker rename failure before publication")
		store.deps.renameNoReplaceAt = func(int, string, int, string) error { return renameErr }
		err := store.Prepare(validVMRuntimeJournalCohort(root, "alpha", "beta"))
		var retained *vmRuntimeJournalPublicationRetainedError
		if !errors.Is(err, renameErr) || errors.As(err, &retained) {
			t.Fatalf("Prepare error = %v, want ordinary clean pre-publication failure", err)
		}
		groups, loadErr := store.LoadAll()
		if loadErr != nil || len(groups) != 0 {
			t.Fatalf("groups after clean marker failure = %#v, error %v", groups, loadErr)
		}
	})
	t.Run("failure after exact marker publication is typed and resumable", func(t *testing.T) {
		root := t.TempDir()
		store := openTestVMRuntimeJournalStore(t, root)
		records := validVMRuntimeJournalCohort(root, "alpha", "beta")
		realRename := store.deps.renameNoReplaceAt
		renameErr := errors.New("injected ambiguous marker rename result")
		store.deps.renameNoReplaceAt = func(oldDir int, oldName string, newDir int, newName string) error {
			if err := realRename(oldDir, oldName, newDir, newName); err != nil {
				return err
			}
			return renameErr
		}
		err := store.Prepare(records)
		var retained *vmRuntimeJournalPublicationRetainedError
		if !errors.As(err, &retained) || !errors.Is(err, renameErr) {
			t.Fatalf("Prepare error = %v, want typed visible marker", err)
		}
		if got := retained.CanonicalPaths(); len(got) != 1 || !strings.Contains(got[0], "transaction-") {
			t.Fatalf("canonical inventory = %v, want exact marker", got)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		store = openTestVMRuntimeJournalStore(t, root)
		defer func() { _ = store.Close() }()
		groups, err := store.LoadAll()
		if err != nil || len(groups) != 1 || groups[0].Phase != vmRuntimeJournalPhasePublishing {
			t.Fatalf("reopened marker intent = %#v, error %v", groups, err)
		}
		memberRenames := 0
		realMemberRename := store.deps.renameNoReplaceAt
		store.deps.renameNoReplaceAt = func(oldDir int, oldName string, newDir int, newName string) error {
			memberRenames++
			return realMemberRename(oldDir, oldName, newDir, newName)
		}
		markerSyncErr := errors.New("injected recovered marker sync failure")
		store.deps.syncDir = func(*os.File) error { return markerSyncErr }
		if err := store.Resume(records[0].TransactionID); !errors.Is(err, markerSyncErr) {
			t.Fatalf("Resume marker sync error = %v", err)
		}
		if memberRenames != 0 {
			t.Fatalf("Resume performed %d member renames before marker sync", memberRenames)
		}
		store.deps.syncDir = func(dir *os.File) error { return dir.Sync() }
		if err := store.Resume(records[0].TransactionID); err != nil {
			t.Fatalf("Resume: %v", err)
		}
		assertVMRuntimeJournalStableState(t, store, vmRuntimeJournalStatePrepared)
	})
	t.Run("contradictory marker destination is retained and uncertain", func(t *testing.T) {
		root := t.TempDir()
		store := openTestVMRuntimeJournalStore(t, root)
		defer func() { _ = store.Close() }()
		realRename := store.deps.renameNoReplaceAt
		renameErr := errors.New("injected contradictory marker rename result")
		store.deps.renameNoReplaceAt = func(oldDir int, oldName string, newDir int, newName string) error {
			if err := realRename(oldDir, oldName, newDir, newName); err != nil {
				return err
			}
			if err := unix.Unlinkat(newDir, newName, 0); err != nil {
				return err
			}
			fd, err := unix.Openat(newDir, newName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
			if err != nil {
				return err
			}
			if _, err := unix.Write(fd, []byte("{}\n")); err != nil {
				_ = unix.Close(fd)
				return err
			}
			if err := unix.Close(fd); err != nil {
				return err
			}
			return renameErr
		}
		err := store.Prepare(validVMRuntimeJournalCohort(root, "alpha", "beta"))
		var retained *vmRuntimeJournalPublicationRetainedError
		if !errors.As(err, &retained) || !errors.Is(err, renameErr) || len(retained.UncertainPaths()) == 0 {
			t.Fatalf("Prepare error = %v, uncertain = %v", err, retained)
		}
	})
}

func TestVMRuntimeJournalPrepareResumesPartialNoReplaceCohortAfterReopen(t *testing.T) {
	root := t.TempDir()
	store := openTestVMRuntimeJournalStore(t, root)
	realRename := store.deps.renameNoReplaceAt
	renames := 0
	renameErr := errors.New("injected second journal rename failure")
	store.deps.renameNoReplaceAt = func(oldDir int, oldName string, newDir int, newName string) error {
		renames++
		if renames == 2 {
			return renameErr
		}
		return realRename(oldDir, oldName, newDir, newName)
	}
	err := store.Prepare(validVMRuntimeJournalCohort(root, "alpha", "beta"))
	if !errors.Is(err, renameErr) {
		t.Fatalf("Prepare error = %v, want injected rename failure", err)
	}
	var retained *vmRuntimeJournalPublicationRetainedError
	if !errors.As(err, &retained) {
		t.Fatalf("Prepare error = %v, want durable recoverable outcome", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openTestVMRuntimeJournalStore(t, root)
	defer func() { _ = store.Close() }()
	groups, loadErr := store.LoadAll()
	if loadErr != nil || len(groups) != 1 || groups[0].Phase != vmRuntimeJournalPhasePublishing {
		t.Fatalf("recoverable group = %#v, error %v", groups, loadErr)
	}
	if err := store.Resume(strings.Repeat("a", 64)); err != nil {
		t.Fatalf("resume partial cohort: %v", err)
	}
	groups, loadErr = store.LoadAll()
	if loadErr != nil || len(groups) != 1 || len(groups[0].Records) != 2 || groups[0].Phase != vmRuntimeJournalPhaseStable {
		t.Fatalf("resumed group = %#v, error %v", groups, loadErr)
	}
}

func TestVMRuntimeJournalPrepareRecoversEveryTwoMemberPublicationBoundary(t *testing.T) {
	tests := []struct {
		name        string
		renameCall  int
		syncCall    int
		afterRename bool
	}{
		{name: "alpha bound-stage rename before", renameCall: 2},
		{name: "alpha bound-stage rename ambiguous", renameCall: 2, afterRename: true},
		{name: "alpha canonical rename before", renameCall: 3},
		{name: "alpha canonical rename ambiguous", renameCall: 3, afterRename: true},
		{name: "beta bound-stage rename before", renameCall: 4},
		{name: "beta bound-stage rename ambiguous", renameCall: 4, afterRename: true},
		{name: "beta canonical rename before", renameCall: 5},
		{name: "beta canonical rename ambiguous", renameCall: 5, afterRename: true},
		{name: "recovery marker sync", syncCall: 2},
		{name: "alpha stage sync", syncCall: 3},
		{name: "alpha canonical sync", syncCall: 4},
		{name: "beta stage sync", syncCall: 5},
		{name: "beta canonical sync", syncCall: 6},
		{name: "stable marker sync", syncCall: 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			store := openTestVMRuntimeJournalStore(t, root)
			records := validVMRuntimeJournalCohort(root, "alpha", "beta")
			injected := errors.New("injected prepare publication boundary failure")
			if tt.renameCall != 0 {
				realRename := store.deps.renameNoReplaceAt
				calls := 0
				store.deps.renameNoReplaceAt = func(oldDir int, oldName string, newDir int, newName string) error {
					calls++
					if calls != tt.renameCall {
						return realRename(oldDir, oldName, newDir, newName)
					}
					if tt.afterRename {
						if err := realRename(oldDir, oldName, newDir, newName); err != nil {
							return err
						}
					}
					return injected
				}
			}
			if tt.syncCall != 0 {
				realSync := store.deps.syncDir
				calls := 0
				store.deps.syncDir = func(dir *os.File) error {
					calls++
					if calls == tt.syncCall {
						return injected
					}
					return realSync(dir)
				}
			}
			err := store.Prepare(records)
			var retained *vmRuntimeJournalPublicationRetainedError
			inlineRecoveredBoundStage := tt.afterRename && (tt.renameCall == 2 || tt.renameCall == 4)
			if inlineRecoveredBoundStage && err != nil {
				t.Fatalf("Prepare recovered exact visible bound stage with error: %v", err)
			}
			if !inlineRecoveredBoundStage && (!errors.As(err, &retained) || !errors.Is(err, injected)) {
				t.Fatalf("Prepare error = %v, want typed boundary failure", err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store = openTestVMRuntimeJournalStore(t, root)
			defer func() { _ = store.Close() }()
			groups, err := store.LoadAll()
			if err != nil || len(groups) != 1 {
				t.Fatalf("reopened group = %#v, error %v", groups, err)
			}
			if groups[0].Phase != vmRuntimeJournalPhasePublishing && groups[0].Phase != vmRuntimeJournalPhaseStable {
				t.Fatalf("reopened phase = %s", groups[0].Phase)
			}
			if err := store.Resume(records[0].TransactionID); err != nil {
				t.Fatalf("Resume: %v", err)
			}
			assertVMRuntimeJournalStableState(t, store, vmRuntimeJournalStatePrepared)
		})
	}
}

func TestVMRuntimeJournalPrepareRepairsEmptyReferencedStageAfterAbruptExit(t *testing.T) {
	root := t.TempDir()
	store := openTestVMRuntimeJournalStore(t, root)
	records := validVMRuntimeJournalCohort(root, "alpha", "beta")
	realRename := store.deps.renameNoReplaceAt
	calls := 0
	abrupt := errors.New("simulate exit before first bound-stage rename")
	store.deps.renameNoReplaceAt = func(oldDir int, oldName string, newDir int, newName string) error {
		calls++
		if calls == 2 {
			return abrupt
		}
		return realRename(oldDir, oldName, newDir, newName)
	}
	if err := store.Prepare(records); !errors.Is(err, abrupt) {
		t.Fatalf("Prepare error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	marker := readVMRuntimeJournalTestMarker(t, root, records[0].TransactionID)
	plan, ok := vmRuntimeJournalPlanForService(marker, "alpha")
	if !ok {
		t.Fatal("alpha marker plan missing")
	}
	if err := os.WriteFile(filepath.Join(root, vmRuntimeJournalDirName, plan.StageName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	store = openTestVMRuntimeJournalStore(t, root)
	defer func() { _ = store.Close() }()
	groups, err := store.LoadAll()
	if err != nil || len(groups) != 1 || groups[0].Phase != vmRuntimeJournalPhasePublishing {
		t.Fatalf("reopened group with partial stage = %#v, error %v", groups, err)
	}
	if err := store.Resume(records[0].TransactionID); err != nil {
		t.Fatalf("Resume repaired stage: %v", err)
	}
	assertVMRuntimeJournalStableState(t, store, vmRuntimeJournalStatePrepared)
}

func TestVMRuntimeJournalTransitionsAreMonotonicAndIdempotent(t *testing.T) {
	root := t.TempDir()
	store := openTestVMRuntimeJournalStore(t, root)
	defer func() { _ = store.Close() }()
	records := validVMRuntimeJournalCohort(root, "alpha", "beta")
	if err := store.Prepare(records); err != nil {
		t.Fatal(err)
	}
	derivedAt := time.Date(2026, 7, 20, 12, 1, 0, 0, time.UTC)
	if err := store.Transition(records[0].TransactionID, vmRuntimeJournalStateDerivedPublished, derivedAt); err != nil {
		t.Fatalf("transition to derived-published: %v", err)
	}
	if err := store.Transition(records[0].TransactionID, vmRuntimeJournalStateDerivedPublished, derivedAt.Add(time.Minute)); err != nil {
		t.Fatalf("idempotent transition: %v", err)
	}
	committedAt := derivedAt.Add(2 * time.Minute)
	if err := store.Transition(records[0].TransactionID, vmRuntimeJournalStateDatabaseCommitted, committedAt); err != nil {
		t.Fatalf("transition to database-committed: %v", err)
	}
	if err := store.Transition(records[0].TransactionID, vmRuntimeJournalStateDerivedPublished, committedAt.Add(time.Minute)); err == nil {
		t.Fatal("reverse transition unexpectedly succeeded")
	}
	groups, err := store.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range groups[0].Records {
		if record.State != vmRuntimeJournalStateDatabaseCommitted || !record.UpdatedAt.Equal(committedAt) {
			t.Fatalf("record %s state/time = %s/%s", record.Service, record.State, record.UpdatedAt)
		}
	}

	otherRoot := t.TempDir()
	other := openTestVMRuntimeJournalStore(t, otherRoot)
	defer func() { _ = other.Close() }()
	otherRecords := validVMRuntimeJournalCohort(otherRoot, "gamma")
	if err := other.Prepare(otherRecords); err != nil {
		t.Fatal(err)
	}
	if err := other.Transition(otherRecords[0].TransactionID, vmRuntimeJournalStateDatabaseCommitted, derivedAt); err == nil {
		t.Fatal("skipped transition unexpectedly succeeded")
	}
}

func TestVMRuntimeJournalTransitionReturnsTypedOutcomeAfterRename(t *testing.T) {
	root := t.TempDir()
	store := openTestVMRuntimeJournalStore(t, root)
	records := validVMRuntimeJournalCohort(root, "alpha", "beta")
	if err := store.Prepare(records); err != nil {
		t.Fatal(err)
	}
	syncErr := errors.New("injected transition sync failure")
	store.deps.syncDir = func(*os.File) error { return syncErr }
	err := store.Transition(records[0].TransactionID, vmRuntimeJournalStateDerivedPublished, records[0].UpdatedAt.Add(time.Minute))
	var retained *vmRuntimeJournalPublicationRetainedError
	if !errors.As(err, &retained) || !errors.Is(err, syncErr) {
		t.Fatalf("Transition error = %v, want typed retained publication", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openTestVMRuntimeJournalStore(t, root)
	defer func() { _ = store.Close() }()
	groups, loadErr := store.LoadAll()
	if loadErr != nil || groups[0].Phase != vmRuntimeJournalPhaseTransitioning {
		t.Fatalf("retained transition = %#v, error %v", groups, loadErr)
	}
	if err := store.Resume(records[0].TransactionID); err != nil {
		t.Fatalf("resume transition: %v", err)
	}
	groups, loadErr = store.LoadAll()
	if loadErr != nil || groups[0].Phase != vmRuntimeJournalPhaseStable {
		t.Fatalf("resumed transition = %#v, error %v", groups, loadErr)
	}
	for _, record := range groups[0].Records {
		if record.State != vmRuntimeJournalStateDerivedPublished {
			t.Fatalf("record %s state = %s", record.Service, record.State)
		}
	}
}

func TestVMRuntimeJournalTransitionClassifiesMarkerReplacementAmbiguity(t *testing.T) {
	for _, afterRename := range []bool{false, true} {
		name := "failure before replacement"
		if afterRename {
			name = "failure after visible replacement"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			store := openTestVMRuntimeJournalStore(t, root)
			records := validVMRuntimeJournalCohort(root, "alpha", "beta")
			if err := store.Prepare(records); err != nil {
				t.Fatal(err)
			}
			realRename := store.deps.renameAt
			renameErr := errors.New("injected marker replacement ambiguity")
			store.deps.renameAt = func(oldDir int, oldName string, newDir int, newName string) error {
				if afterRename {
					if err := realRename(oldDir, oldName, newDir, newName); err != nil {
						return err
					}
				}
				return renameErr
			}
			at := records[0].UpdatedAt.Add(time.Minute)
			err := store.Transition(records[0].TransactionID, vmRuntimeJournalStateDerivedPublished, at)
			var retained *vmRuntimeJournalPublicationRetainedError
			if !errors.As(err, &retained) || !errors.Is(err, renameErr) {
				t.Fatalf("Transition error = %v, want typed marker replacement ambiguity", err)
			}
			if got := retained.CanonicalPaths(); len(got) != 3 {
				t.Fatalf("canonical inventory = %v, want exact marker and two records", got)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store = openTestVMRuntimeJournalStore(t, root)
			defer func() { _ = store.Close() }()
			groups, err := store.LoadAll()
			if err != nil || len(groups) != 1 {
				t.Fatalf("reopened marker state = %#v, error %v", groups, err)
			}
			if afterRename {
				if groups[0].Phase != vmRuntimeJournalPhaseTransitioning {
					t.Fatalf("phase = %s, want transitioning", groups[0].Phase)
				}
				memberRenames := 0
				realMemberRename := store.deps.renameAt
				store.deps.renameAt = func(oldDir int, oldName string, newDir int, newName string) error {
					memberRenames++
					return realMemberRename(oldDir, oldName, newDir, newName)
				}
				markerSyncErr := errors.New("injected recovered transition marker sync failure")
				store.deps.syncDir = func(*os.File) error { return markerSyncErr }
				if err := store.Resume(records[0].TransactionID); !errors.Is(err, markerSyncErr) {
					t.Fatalf("Resume marker sync error = %v", err)
				}
				if memberRenames != 0 {
					t.Fatalf("Resume performed %d member renames before marker sync", memberRenames)
				}
				store.deps.syncDir = func(dir *os.File) error { return dir.Sync() }
				if err := store.Resume(records[0].TransactionID); err != nil {
					t.Fatalf("Resume: %v", err)
				}
			} else {
				if groups[0].Phase != vmRuntimeJournalPhaseStable {
					t.Fatalf("phase = %s, want stable", groups[0].Phase)
				}
				if err := store.Transition(records[0].TransactionID, vmRuntimeJournalStateDerivedPublished, at); err != nil {
					t.Fatalf("retry Transition: %v", err)
				}
			}
			assertVMRuntimeJournalStableState(t, store, vmRuntimeJournalStateDerivedPublished)
		})
	}
}

func TestVMRuntimeJournalTransitionRecoversEveryTwoMemberBoundary(t *testing.T) {
	tests := []struct {
		name        string
		renameCall  int
		syncCall    int
		afterRename bool
	}{
		{name: "alpha rename before", renameCall: 2},
		{name: "alpha rename ambiguous", renameCall: 2, afterRename: true},
		{name: "beta rename before", renameCall: 3},
		{name: "beta rename ambiguous", renameCall: 3, afterRename: true},
		{name: "recovery marker sync", syncCall: 2},
		{name: "alpha stage sync", syncCall: 3},
		{name: "alpha canonical sync", syncCall: 4},
		{name: "beta stage sync", syncCall: 5},
		{name: "beta canonical sync", syncCall: 6},
		{name: "stable marker sync", syncCall: 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			store := openTestVMRuntimeJournalStore(t, root)
			records := validVMRuntimeJournalCohort(root, "alpha", "beta")
			if err := store.Prepare(records); err != nil {
				t.Fatal(err)
			}
			injected := errors.New("injected transition boundary failure")
			if tt.renameCall != 0 {
				realRename := store.deps.renameAt
				calls := 0
				store.deps.renameAt = func(oldDir int, oldName string, newDir int, newName string) error {
					calls++
					if calls != tt.renameCall {
						return realRename(oldDir, oldName, newDir, newName)
					}
					if tt.afterRename {
						if err := realRename(oldDir, oldName, newDir, newName); err != nil {
							return err
						}
					}
					return injected
				}
			}
			if tt.syncCall != 0 {
				realSync := store.deps.syncDir
				calls := 0
				store.deps.syncDir = func(dir *os.File) error {
					calls++
					if calls == tt.syncCall {
						return injected
					}
					return realSync(dir)
				}
			}
			at := records[0].UpdatedAt.Add(time.Minute)
			err := store.Transition(records[0].TransactionID, vmRuntimeJournalStateDerivedPublished, at)
			var retained *vmRuntimeJournalPublicationRetainedError
			if !errors.As(err, &retained) || !errors.Is(err, injected) {
				t.Fatalf("Transition error = %v, want typed boundary failure", err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store = openTestVMRuntimeJournalStore(t, root)
			defer func() { _ = store.Close() }()
			groups, err := store.LoadAll()
			if err != nil || len(groups) != 1 {
				t.Fatalf("reopened group = %#v, error %v", groups, err)
			}
			if groups[0].Phase != vmRuntimeJournalPhaseTransitioning && groups[0].Phase != vmRuntimeJournalPhaseStable {
				t.Fatalf("reopened phase = %s", groups[0].Phase)
			}
			if err := store.Resume(records[0].TransactionID); err != nil {
				t.Fatalf("Resume: %v", err)
			}
			assertVMRuntimeJournalStableState(t, store, vmRuntimeJournalStateDerivedPublished)
		})
	}
}

func TestVMRuntimeJournalTransitionRepairsTruncatedReferencedStageAfterAbruptExit(t *testing.T) {
	root := t.TempDir()
	store := openTestVMRuntimeJournalStore(t, root)
	records := validVMRuntimeJournalCohort(root, "alpha", "beta")
	if err := store.Prepare(records); err != nil {
		t.Fatal(err)
	}
	abrupt := errors.New("simulate exit before transition bound-stage rename")
	store.deps.renameNoReplaceAt = func(int, string, int, string) error { return abrupt }
	at := records[0].UpdatedAt.Add(time.Minute)
	if err := store.Transition(records[0].TransactionID, vmRuntimeJournalStateDerivedPublished, at); !errors.Is(err, abrupt) {
		t.Fatalf("Transition error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	marker := readVMRuntimeJournalTestMarker(t, root, records[0].TransactionID)
	plan, ok := vmRuntimeJournalPlanForService(marker, "alpha")
	if !ok {
		t.Fatal("alpha marker plan missing")
	}
	if err := os.WriteFile(filepath.Join(root, vmRuntimeJournalDirName, plan.StageName), []byte(`{"schema":`), 0o600); err != nil {
		t.Fatal(err)
	}
	store = openTestVMRuntimeJournalStore(t, root)
	defer func() { _ = store.Close() }()
	groups, err := store.LoadAll()
	if err != nil || len(groups) != 1 || groups[0].Phase != vmRuntimeJournalPhaseTransitioning {
		t.Fatalf("reopened group with truncated stage = %#v, error %v", groups, err)
	}
	if err := store.Resume(records[0].TransactionID); err != nil {
		t.Fatalf("Resume repaired transition stage: %v", err)
	}
	assertVMRuntimeJournalStableState(t, store, vmRuntimeJournalStateDerivedPublished)
}

func TestVMRuntimeJournalRemoveUsesIdentityCheckedTombstones(t *testing.T) {
	root := t.TempDir()
	store := openTestVMRuntimeJournalStore(t, root)
	defer func() { _ = store.Close() }()
	records := validVMRuntimeJournalCohort(root, "alpha", "beta")
	if err := store.Prepare(records); err != nil {
		t.Fatal(err)
	}
	var pending *vmRuntimeJournalRemovalPendingError
	if err := store.BeginRemoval(records[0].TransactionID); !errors.As(err, &pending) {
		t.Fatalf("BeginRemoval = %v, want durable tombstone confirmation", err)
	}
	groups, err := store.LoadAll()
	if err != nil || len(groups) != 1 || groups[0].Phase != vmRuntimeJournalPhaseTombstoned || len(groups[0].Tombstones) != 2 {
		t.Fatalf("tombstoned group = %#v, error %v", groups, err)
	}
	if err := store.Resume(records[0].TransactionID); !errors.As(err, &pending) {
		t.Fatalf("Resume removal = %v, want explicit agreement recheck requirement", err)
	}
	markerPath := filepath.Join(root, vmRuntimeJournalDirName, vmRuntimeJournalMarkerName(records[0].TransactionID))
	markerBefore, err := os.Lstat(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BeginRemoval(records[0].TransactionID); !errors.As(err, &pending) {
		t.Fatalf("repeated BeginRemoval = %v, want pending", err)
	}
	markerAfter, err := os.Lstat(markerPath)
	if err != nil || !os.SameFile(markerBefore, markerAfter) {
		t.Fatalf("repeated BeginRemoval changed marker: before %v after %v error %v", markerBefore, markerAfter, err)
	}
	if err := store.CommitRemoval(records[0].TransactionID); err != nil {
		t.Fatalf("CommitRemoval: %v", err)
	}
	groups, err = store.LoadAll()
	if err != nil || len(groups) != 0 {
		t.Fatalf("groups after remove = %#v, error %v", groups, err)
	}

	second := validVMRuntimeJournalCohort(root, "gamma")
	if err := store.Prepare(second); err != nil {
		t.Fatal(err)
	}
	realRename := store.deps.renameNoReplaceAt
	store.deps.renameNoReplaceAt = func(oldDir int, oldName string, newDir int, newName string) error {
		if err := realRename(oldDir, oldName, newDir, newName); err != nil {
			return err
		}
		if strings.Contains(newName, ".tombstone-") {
			if err := unix.Unlinkat(newDir, newName, 0); err != nil {
				return err
			}
			fd, err := unix.Openat(newDir, newName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
			if err != nil {
				return err
			}
			return unix.Close(fd)
		}
		return nil
	}
	err = store.BeginRemoval(second[0].TransactionID)
	var retained *vmRuntimeJournalPublicationRetainedError
	if !errors.As(err, &retained) || !strings.Contains(strings.ToLower(err.Error()), "decode") {
		t.Fatalf("Remove error = %v, want retained exact-content refusal", err)
	}
	if len(retained.UncertainPaths()) == 0 {
		t.Fatalf("retained inventory = %#v, want uncertain replacement path", retained)
	}
	for _, path := range retained.UncertainPaths() {
		if strings.Contains(path, ".tombstone-") {
			if _, statErr := os.Lstat(path); statErr != nil {
				t.Fatalf("replacement tombstone was removed: %v", statErr)
			}
			return
		}
	}
	t.Fatal("uncertain inventory did not include replacement tombstone")
}

func TestVMRuntimeJournalBeginRemovalSyncsAmbiguousVisibleMarkerBeforeTombstones(t *testing.T) {
	root := t.TempDir()
	store := openTestVMRuntimeJournalStore(t, root)
	records := validVMRuntimeJournalCohort(root, "alpha", "beta")
	if err := store.Prepare(records); err != nil {
		t.Fatal(err)
	}
	realRename := store.deps.renameAt
	renameErr := errors.New("injected visible removal marker replacement result")
	store.deps.renameAt = func(oldDir int, oldName string, newDir int, newName string) error {
		if err := realRename(oldDir, oldName, newDir, newName); err != nil {
			return err
		}
		return renameErr
	}
	err := store.BeginRemoval(records[0].TransactionID)
	var retained *vmRuntimeJournalPublicationRetainedError
	if !errors.As(err, &retained) || !errors.Is(err, renameErr) {
		t.Fatalf("BeginRemoval error = %v, want typed visible marker ambiguity", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openTestVMRuntimeJournalStore(t, root)
	defer func() { _ = store.Close() }()
	groups, err := store.LoadAll()
	if err != nil || len(groups) != 1 || groups[0].Phase != vmRuntimeJournalPhaseRemoving {
		t.Fatalf("reopened removal marker = %#v, error %v", groups, err)
	}
	tombstoneRenames := 0
	realTombstoneRename := store.deps.renameNoReplaceAt
	store.deps.renameNoReplaceAt = func(oldDir int, oldName string, newDir int, newName string) error {
		tombstoneRenames++
		return realTombstoneRename(oldDir, oldName, newDir, newName)
	}
	markerSyncErr := errors.New("injected recovered removal marker sync failure")
	store.deps.syncDir = func(*os.File) error { return markerSyncErr }
	if err := store.BeginRemoval(records[0].TransactionID); !errors.Is(err, markerSyncErr) {
		t.Fatalf("BeginRemoval marker sync error = %v", err)
	}
	if tombstoneRenames != 0 {
		t.Fatalf("BeginRemoval performed %d tombstone renames before marker sync", tombstoneRenames)
	}
	store.deps.syncDir = func(dir *os.File) error { return dir.Sync() }
	var pending *vmRuntimeJournalRemovalPendingError
	if err := store.BeginRemoval(records[0].TransactionID); !errors.As(err, &pending) {
		t.Fatalf("resumed BeginRemoval = %v, want pending", err)
	}
}

func TestVMRuntimeJournalRemoveRetainsCompleteTombstonesUntilDirectorySync(t *testing.T) {
	root := t.TempDir()
	store := openTestVMRuntimeJournalStore(t, root)
	defer func() { _ = store.Close() }()
	records := validVMRuntimeJournalCohort(root, "alpha", "beta")
	if err := store.Prepare(records); err != nil {
		t.Fatal(err)
	}
	syncErr := errors.New("injected tombstone sync failure")
	realSync := store.deps.syncDir
	syncs := 0
	store.deps.syncDir = func(dir *os.File) error {
		syncs++
		if syncs == 4 {
			return syncErr
		}
		return realSync(dir)
	}
	err := store.BeginRemoval(records[0].TransactionID)
	var retained *vmRuntimeJournalPublicationRetainedError
	if !errors.As(err, &retained) || !errors.Is(err, syncErr) || len(retained.TombstonePaths()) != 2 {
		t.Fatalf("Remove error = %v, tombstones %v; want typed retention of complete cohort", err, retained)
	}
	for _, path := range retained.TombstonePaths() {
		if info, statErr := os.Lstat(path); statErr != nil || !info.Mode().IsRegular() {
			t.Fatalf("retained tombstone %s: info %v, error %v", path, info, statErr)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openTestVMRuntimeJournalStore(t, root)
	defer func() { _ = store.Close() }()
	groups, loadErr := store.LoadAll()
	if loadErr != nil || len(groups) != 1 || groups[0].Phase != vmRuntimeJournalPhaseRemoving || len(groups[0].Tombstones) != 2 {
		t.Fatalf("reopened partial removal = %#v, error %v", groups, loadErr)
	}
	var pending *vmRuntimeJournalRemovalPendingError
	if err := store.BeginRemoval(records[0].TransactionID); !errors.As(err, &pending) {
		t.Fatalf("resumed removal = %v, want confirmation boundary", err)
	}
}

func TestVMRuntimeJournalRemoveRecoversEveryTwoMemberTombstoneBoundary(t *testing.T) {
	tests := []struct {
		name        string
		renameCall  int
		syncCall    int
		afterRename bool
	}{
		{name: "removing marker sync", syncCall: 1},
		{name: "alpha rename before", renameCall: 1},
		{name: "alpha rename ambiguous", renameCall: 1, afterRename: true},
		{name: "beta rename before", renameCall: 2},
		{name: "beta rename ambiguous", renameCall: 2, afterRename: true},
		{name: "recovery marker sync", syncCall: 2},
		{name: "alpha tombstone sync", syncCall: 3},
		{name: "beta tombstone sync", syncCall: 4},
		{name: "tombstoned marker sync", syncCall: 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			store := openTestVMRuntimeJournalStore(t, root)
			records := validVMRuntimeJournalCohort(root, "alpha", "beta")
			if err := store.Prepare(records); err != nil {
				t.Fatal(err)
			}
			injected := errors.New("injected removal tombstone boundary failure")
			if tt.renameCall != 0 {
				realRename := store.deps.renameNoReplaceAt
				calls := 0
				store.deps.renameNoReplaceAt = func(oldDir int, oldName string, newDir int, newName string) error {
					calls++
					if calls != tt.renameCall {
						return realRename(oldDir, oldName, newDir, newName)
					}
					if tt.afterRename {
						if err := realRename(oldDir, oldName, newDir, newName); err != nil {
							return err
						}
					}
					return injected
				}
			}
			if tt.syncCall != 0 {
				realSync := store.deps.syncDir
				calls := 0
				store.deps.syncDir = func(dir *os.File) error {
					calls++
					if calls == tt.syncCall {
						return injected
					}
					return realSync(dir)
				}
			}
			err := store.BeginRemoval(records[0].TransactionID)
			var retained *vmRuntimeJournalPublicationRetainedError
			if !errors.As(err, &retained) || !errors.Is(err, injected) {
				t.Fatalf("Remove error = %v, want typed boundary failure", err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store = openTestVMRuntimeJournalStore(t, root)
			defer func() { _ = store.Close() }()
			groups, err := store.LoadAll()
			if err != nil || len(groups) != 1 {
				t.Fatalf("reopened group = %#v, error %v", groups, err)
			}
			var pending *vmRuntimeJournalRemovalPendingError
			switch groups[0].Phase {
			case vmRuntimeJournalPhaseRemoving:
				if err := store.BeginRemoval(records[0].TransactionID); !errors.As(err, &pending) {
					t.Fatalf("resumed tombstoning = %v, want confirmation boundary", err)
				}
				groups, err = store.LoadAll()
				if err != nil || len(groups) != 1 || groups[0].Phase != vmRuntimeJournalPhaseTombstoned || len(groups[0].Tombstones) != 2 {
					t.Fatalf("confirmed tombstoned group = %#v, error %v", groups, err)
				}
			case vmRuntimeJournalPhaseTombstoned:
				if err := store.Resume(records[0].TransactionID); !errors.As(err, &pending) {
					t.Fatalf("Resume tombstoned removal = %v, want agreement recheck requirement", err)
				}
			default:
				t.Fatalf("reopened removal phase = %s", groups[0].Phase)
			}
			if err := store.CommitRemoval(records[0].TransactionID); err != nil {
				t.Fatalf("confirmed removal: %v", err)
			}
			groups, err = store.LoadAll()
			if err != nil || len(groups) != 0 {
				t.Fatalf("groups after confirmed removal = %#v, error %v", groups, err)
			}
		})
	}
}

func TestVMRuntimeJournalRemoveRecoversMarkerCommitAndTombstoneCleanupFailures(t *testing.T) {
	tests := []struct {
		name        string
		unlinkCall  int
		syncCall    int
		afterUnlink bool
	}{
		{name: "marker unlink before", unlinkCall: 1},
		{name: "marker unlink ambiguous", unlinkCall: 1, afterUnlink: true},
		{name: "marker commit sync", syncCall: 1},
		{name: "alpha tombstone unlink", unlinkCall: 2},
		{name: "alpha tombstone cleanup sync", syncCall: 2},
		{name: "beta tombstone unlink", unlinkCall: 3},
		{name: "beta tombstone cleanup sync", syncCall: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			store := openTestVMRuntimeJournalStore(t, root)
			records := validVMRuntimeJournalCohort(root, "alpha", "beta")
			if err := store.Prepare(records); err != nil {
				t.Fatal(err)
			}
			var pending *vmRuntimeJournalRemovalPendingError
			if err := store.BeginRemoval(records[0].TransactionID); !errors.As(err, &pending) {
				t.Fatalf("first Remove = %v", err)
			}
			injected := errors.New("injected removal commit/cleanup boundary failure")
			if tt.unlinkCall != 0 {
				realUnlink := store.deps.unlinkAt
				calls := 0
				store.deps.unlinkAt = func(dir int, name string, flags int) error {
					calls++
					if calls != tt.unlinkCall {
						return realUnlink(dir, name, flags)
					}
					if tt.afterUnlink {
						if err := realUnlink(dir, name, flags); err != nil {
							return err
						}
					}
					return injected
				}
			}
			if tt.syncCall != 0 {
				realSync := store.deps.syncDir
				calls := 0
				store.deps.syncDir = func(dir *os.File) error {
					calls++
					if calls == tt.syncCall {
						return injected
					}
					return realSync(dir)
				}
			}
			err := store.CommitRemoval(records[0].TransactionID)
			var retained *vmRuntimeJournalPublicationRetainedError
			if !errors.As(err, &retained) || !errors.Is(err, injected) {
				t.Fatalf("confirmed Remove error = %v, want typed retained cleanup", err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store = openTestVMRuntimeJournalStore(t, root)
			defer func() { _ = store.Close() }()
			groups, err := store.LoadAll()
			if err != nil {
				t.Fatalf("LoadAll after cleanup failure: %v", err)
			}
			if len(groups) == 1 {
				if groups[0].Phase != vmRuntimeJournalPhaseTombstoned {
					t.Fatalf("retained phase = %s", groups[0].Phase)
				}
			} else if len(groups) != 0 {
				t.Fatalf("groups = %#v", groups)
			}
			if err := store.CommitRemoval(records[0].TransactionID); err != nil {
				t.Fatalf("retry removal: %v", err)
			}
			groups, err = store.LoadAll()
			if err != nil || len(groups) != 0 {
				t.Fatalf("groups after retry = %#v, error %v", groups, err)
			}
		})
	}
}

func TestVMRuntimeJournalCommitRemovalRefusesUnconfirmedPhases(t *testing.T) {
	t.Run("stable", func(t *testing.T) {
		root := t.TempDir()
		store := openTestVMRuntimeJournalStore(t, root)
		defer func() { _ = store.Close() }()
		records := validVMRuntimeJournalCohort(root, "alpha", "beta")
		if err := store.Prepare(records); err != nil {
			t.Fatal(err)
		}
		if err := store.CommitRemoval(records[0].TransactionID); err == nil || !strings.Contains(err.Error(), "not durably tombstoned") {
			t.Fatalf("CommitRemoval error = %v", err)
		}
		assertVMRuntimeJournalStableState(t, store, vmRuntimeJournalStatePrepared)
	})
	t.Run("removing", func(t *testing.T) {
		root := t.TempDir()
		store := openTestVMRuntimeJournalStore(t, root)
		records := validVMRuntimeJournalCohort(root, "alpha", "beta")
		if err := store.Prepare(records); err != nil {
			t.Fatal(err)
		}
		stop := errors.New("stop before first tombstone")
		store.deps.renameNoReplaceAt = func(int, string, int, string) error { return stop }
		if err := store.BeginRemoval(records[0].TransactionID); !errors.Is(err, stop) {
			t.Fatalf("BeginRemoval error = %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		store = openTestVMRuntimeJournalStore(t, root)
		defer func() { _ = store.Close() }()
		groups, err := store.LoadAll()
		if err != nil || len(groups) != 1 || groups[0].Phase != vmRuntimeJournalPhaseRemoving {
			t.Fatalf("removing group = %#v, error %v", groups, err)
		}
		if err := store.CommitRemoval(records[0].TransactionID); err == nil || !strings.Contains(err.Error(), "not durably tombstoned") {
			t.Fatalf("CommitRemoval error = %v", err)
		}
	})
	t.Run("transitioning", func(t *testing.T) {
		root := t.TempDir()
		store := openTestVMRuntimeJournalStore(t, root)
		records := validVMRuntimeJournalCohort(root, "alpha", "beta")
		if err := store.Prepare(records); err != nil {
			t.Fatal(err)
		}
		stop := errors.New("stop before transition stage")
		store.deps.renameNoReplaceAt = func(int, string, int, string) error { return stop }
		if err := store.Transition(records[0].TransactionID, vmRuntimeJournalStateDerivedPublished, records[0].UpdatedAt.Add(time.Minute)); !errors.Is(err, stop) {
			t.Fatalf("Transition error = %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		store = openTestVMRuntimeJournalStore(t, root)
		defer func() { _ = store.Close() }()
		if err := store.CommitRemoval(records[0].TransactionID); err == nil || !strings.Contains(err.Error(), "not durably tombstoned") {
			t.Fatalf("CommitRemoval error = %v", err)
		}
	})
}

func TestVMRuntimeJournalLoadAllRejectsMalformedIncompleteAndContradictoryCohorts(t *testing.T) {
	tests := []struct {
		name  string
		write func(*testing.T, string)
		want  string
	}{
		{
			name: "unknown field",
			write: func(t *testing.T, root string) {
				record := validVMRuntimeJournalCohort(root, "alpha")[0]
				raw := marshalVMRuntimeJournalTestRecord(t, record)
				raw = append(raw[:len(raw)-1], []byte(`,"surprise":true}`)...)
				writeVMRuntimeJournalTestRecord(t, root, "alpha", raw)
			},
			want: "unknown field",
		},
		{
			name: "duplicate field",
			write: func(t *testing.T, root string) {
				record := validVMRuntimeJournalCohort(root, "alpha")[0]
				raw := marshalVMRuntimeJournalTestRecord(t, record)
				raw = append(raw[:len(raw)-1], []byte(`,"state":"prepared"}`)...)
				writeVMRuntimeJournalTestRecord(t, root, "alpha", raw)
			},
			want: "duplicate field",
		},
		{
			name: "incomplete VM host projection",
			write: func(t *testing.T, root string) {
				record := validVMRuntimeJournalCohort(root, "alpha")[0]
				record.VMHostProjection = true
				record.OldVMHost = &vmRuntimeJournalVMHostProjection{RuntimePolicy: "manual", RuntimeChannel: "stable"}
				record.NewVMHost = &vmRuntimeJournalVMHostProjection{RuntimePolicy: "stage-on-restart", RuntimeChannel: "stable"}
				var object map[string]any
				if err := json.Unmarshal(marshalVMRuntimeJournalTestRecord(t, record), &object); err != nil {
					t.Fatal(err)
				}
				delete(object["oldVMHost"].(map[string]any), "runtimeChannel")
				raw, err := json.Marshal(object)
				if err != nil {
					t.Fatal(err)
				}
				writeVMRuntimeJournalTestRecord(t, root, "alpha", raw)
			},
			want: "missing required field",
		},
		{
			name: "incomplete cohort",
			write: func(t *testing.T, root string) {
				records := validVMRuntimeJournalCohort(root, "alpha", "beta")
				writeVMRuntimeJournalTestStableMarker(t, root, records)
				writeVMRuntimeJournalTestRecord(t, root, "alpha", marshalVMRuntimeJournalTestRecord(t, records[0]))
			},
			want: "missing",
		},
		{
			name: "contradictory states",
			write: func(t *testing.T, root string) {
				records := validVMRuntimeJournalCohort(root, "alpha", "beta")
				writeVMRuntimeJournalTestStableMarker(t, root, records)
				records[1].State = vmRuntimeJournalStateDerivedPublished
				for _, record := range records {
					writeVMRuntimeJournalTestRecord(t, root, record.Service, marshalVMRuntimeJournalTestRecord(t, record))
				}
			},
			want: "contradictory",
		},
		{
			name: "filename mismatch",
			write: func(t *testing.T, root string) {
				record := validVMRuntimeJournalCohort(root, "alpha")[0]
				writeVMRuntimeJournalTestRecord(t, root, "beta", marshalVMRuntimeJournalTestRecord(t, record))
			},
			want: "filename",
		},
		{
			name: "bad digest",
			write: func(t *testing.T, root string) {
				record := validVMRuntimeJournalCohort(root, "alpha")[0]
				record.NewUnit.SHA256 = strings.Repeat("0", 64)
				writeVMRuntimeJournalTestRecord(t, root, "alpha", marshalVMRuntimeJournalTestRecord(t, record))
			},
			want: "digest",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			store := openTestVMRuntimeJournalStore(t, root)
			defer func() { _ = store.Close() }()
			tt.write(t, root)
			if _, err := store.LoadAll(); err == nil || !strings.Contains(strings.ToLower(err.Error()), tt.want) {
				t.Fatalf("LoadAll error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestVMRuntimeJournalLoadAllDoesNotFollowCanonicalSymlink(t *testing.T) {
	root := t.TempDir()
	store := openTestVMRuntimeJournalStore(t, root)
	defer func() { _ = store.Close() }()
	target := filepath.Join(root, "target.json")
	if err := os.WriteFile(target, marshalVMRuntimeJournalTestRecord(t, validVMRuntimeJournalCohort(root, "alpha")[0]), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, vmRuntimeJournalDirName, "alpha.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadAll(); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("LoadAll error = %v, want no-follow refusal", err)
	}
}

func TestVMRuntimeJournalStoreRejectsAncestorSymlinkAndAcceptsCustomRoot(t *testing.T) {
	base := t.TempDir()
	realRoot := filepath.Join(base, "zfs-dataset")
	customRoot := filepath.Join(realRoot, "custom-data-root")
	if err := os.Mkdir(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(customRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	store := openTestVMRuntimeJournalStore(t, customRoot)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "dataset-alias")
	if err := os.Symlink(realRoot, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := openVMRuntimeJournalStore(context.Background(), filepath.Join(alias, "custom-data-root"), testVMRuntimeJournalStoreDeps()); err == nil || !strings.Contains(strings.ToLower(err.Error()), "symbolic link") {
		t.Fatalf("ancestor-symlink open error = %v, want component-level refusal", err)
	}
}

func TestVMRuntimeJournalPrepareBindsExactServiceRootAndUnitTargets(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]vmRuntimeJournalRecord)
		want   string
	}{
		{
			name: "descriptor outside effective service root",
			mutate: func(records []vmRuntimeJournalRecord) {
				records[0].OldDescriptor.Path = filepath.Join(filepath.Dir(records[0].ServiceRoot), "other", vmRuntimeDescriptorFileName)
				records[0].NewDescriptor.Path = records[0].OldDescriptor.Path
			},
			want: "descriptor path must be exactly",
		},
		{
			name: "descriptor under transient run directory",
			mutate: func(records []vmRuntimeJournalRecord) {
				records[0].OldDescriptor.Path = filepath.Join(serviceRunDirForRoot(records[0].ServiceRoot), vmRuntimeDescriptorFileName)
				records[0].NewDescriptor.Path = records[0].OldDescriptor.Path
			},
			want: "descriptor path must be exactly",
		},
		{
			name: "unit outside exact system target",
			mutate: func(records []vmRuntimeJournalRecord) {
				records[0].OldUnit.Path += ".other"
				records[0].NewUnit.Path = records[0].OldUnit.Path
			},
			want: "unit path must be exactly",
		},
		{
			name: "duplicate descriptor target",
			mutate: func(records []vmRuntimeJournalRecord) {
				records[1].ServiceRoot = records[0].ServiceRoot
				records[1].OldDescriptor.Path = records[0].OldDescriptor.Path
				records[1].NewDescriptor.Path = records[0].NewDescriptor.Path
			},
			want: "duplicate vm runtime descriptor target",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			store := openTestVMRuntimeJournalStore(t, root)
			defer func() { _ = store.Close() }()
			records := validVMRuntimeJournalCohort(root, "alpha", "beta")
			tt.mutate(records)
			if err := store.Prepare(records); err == nil || !strings.Contains(strings.ToLower(err.Error()), tt.want) {
				t.Fatalf("Prepare error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestVMRuntimeJournalLoadAllRejectsUnknownHiddenEntry(t *testing.T) {
	root := t.TempDir()
	store := openTestVMRuntimeJournalStore(t, root)
	defer func() { _ = store.Close() }()
	if err := os.WriteFile(filepath.Join(root, vmRuntimeJournalDirName, ".not-journal-debris"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadAll(); err == nil || !strings.Contains(err.Error(), "unknown hidden") {
		t.Fatalf("LoadAll error = %v, want unknown hidden entry refusal", err)
	}
}

func TestVMRuntimeJournalLoadAllRejectsMalformedUnreferencedBoundStage(t *testing.T) {
	root := t.TempDir()
	store := openTestVMRuntimeJournalStore(t, root)
	defer func() { _ = store.Close() }()
	name := ".alpha.json.stage-" + strings.Repeat("d", 64)
	if err := os.WriteFile(filepath.Join(root, vmRuntimeJournalDirName, name), []byte(`{"schema":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadAll(); err == nil || !strings.Contains(err.Error(), "decode VM runtime journal stage") {
		t.Fatalf("LoadAll error = %v, want malformed unreferenced stage refusal", err)
	}
}

func TestVMRuntimeJournalLoadAllBoundsEntriesRawAndDecodedBytes(t *testing.T) {
	t.Run("entries", func(t *testing.T) {
		root := t.TempDir()
		store := openTestVMRuntimeJournalStore(t, root)
		defer func() { _ = store.Close() }()
		store.deps.maxEntries = 2
		for i := range 3 {
			name := fmt.Sprintf(".alpha-%d.json.staged-%024x", i, i+1)
			if err := os.WriteFile(filepath.Join(root, vmRuntimeJournalDirName, name), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.LoadAll(); err == nil || !strings.Contains(err.Error(), "limit 2") {
			t.Fatalf("LoadAll error = %v, want entry limit", err)
		}
	})
	t.Run("aggregate raw", func(t *testing.T) {
		root := t.TempDir()
		store := openTestVMRuntimeJournalStore(t, root)
		defer func() { _ = store.Close() }()
		store.deps.maxAggregateRaw = 10
		name := ".alpha.json.staged-000000000000000000000001"
		if err := os.WriteFile(filepath.Join(root, vmRuntimeJournalDirName, name), []byte("1234567890\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LoadAll(); err == nil || !strings.Contains(err.Error(), "raw bytes") {
			t.Fatalf("LoadAll error = %v, want aggregate raw limit", err)
		}
	})
	t.Run("aggregate decoded", func(t *testing.T) {
		root := t.TempDir()
		store := openTestVMRuntimeJournalStore(t, root)
		defer func() { _ = store.Close() }()
		if err := store.Prepare(validVMRuntimeJournalCohort(root, "alpha", "beta")); err != nil {
			t.Fatal(err)
		}
		store.deps.maxAggregateDecoded = 1
		if _, err := store.LoadAll(); err == nil || !strings.Contains(err.Error(), "decoded payload") {
			t.Fatalf("LoadAll error = %v, want aggregate decoded limit", err)
		}
	})
}

func TestVMRuntimeJournalEncodingLimitIncludesNewline(t *testing.T) {
	record := validVMRuntimeJournalCohort("/var/lib/yeet", "alpha")[0]
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := encodeVMRuntimeJournalRecordWithLimit(record, len(raw)); err == nil || !strings.Contains(err.Error(), "including newline") {
		t.Fatalf("encode error = %v, want newline-inclusive limit", err)
	}
	encoded, err := encodeVMRuntimeJournalRecordWithLimit(record, len(raw)+1)
	if err != nil || len(encoded) != len(raw)+1 || encoded[len(encoded)-1] != '\n' {
		t.Fatalf("encoded length/tail = %d/%q, error %v", len(encoded), encoded[len(encoded)-1:], err)
	}
}

func TestVMRuntimeJournalCleanupRetainsUnverifiedIdentityAndReportsSyncError(t *testing.T) {
	root := t.TempDir()
	store := openTestVMRuntimeJournalStore(t, root)
	defer func() { _ = store.Close() }()
	name := ".alpha.json.staged-000000000000000000000001"
	path := filepath.Join(root, vmRuntimeJournalDirName, name)
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	unlinks := 0
	store.deps.unlinkAt = func(int, string, int) error {
		unlinks++
		return nil
	}
	syncErr := errors.New("injected retained cleanup sync failure")
	store.deps.syncDir = func(*os.File) error { return syncErr }
	err := store.cleanupClosedStaged(name, vmJailerFileIdentity{})
	if !errors.Is(err, syncErr) || !strings.Contains(err.Error(), "identity could not be verified") {
		t.Fatalf("cleanup error = %v", err)
	}
	if unlinks != 0 {
		t.Fatalf("unverified cleanup called unlink %d times", unlinks)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("unverified staged file was removed: %v", err)
	}
}

func TestVMRuntimeJournalPrepareJoinsCleanupFailureAndInventoriesBoundStage(t *testing.T) {
	root := t.TempDir()
	store := openTestVMRuntimeJournalStore(t, root)
	records := validVMRuntimeJournalCohort(root, "alpha", "beta")
	realRename := store.deps.renameNoReplaceAt
	renames := 0
	renameErr := errors.New("injected member rename failure")
	store.deps.renameNoReplaceAt = func(oldDir int, oldName string, newDir int, newName string) error {
		renames++
		if renames == 3 {
			return renameErr
		}
		return realRename(oldDir, oldName, newDir, newName)
	}
	realUnlink := store.deps.unlinkAt
	cleanupErr := errors.New("injected staged cleanup failure")
	store.deps.unlinkAt = func(dir int, name string, flags int) error {
		if strings.Contains(name, ".stage-") {
			return cleanupErr
		}
		return realUnlink(dir, name, flags)
	}
	err := store.Prepare(records)
	var retained *vmRuntimeJournalPublicationRetainedError
	if !errors.As(err, &retained) || !errors.Is(err, renameErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("Prepare error = %v, want joined rename and cleanup failures", err)
	}
	foundStage := false
	for _, path := range retained.UncertainPaths() {
		foundStage = foundStage || strings.Contains(path, ".stage-")
	}
	if !foundStage {
		t.Fatalf("uncertain paths = %v, want retained bound stage", retained.UncertainPaths())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openTestVMRuntimeJournalStore(t, root)
	defer func() { _ = store.Close() }()
	if err := store.Resume(records[0].TransactionID); err != nil {
		t.Fatalf("Resume retained stage: %v", err)
	}
	assertVMRuntimeJournalStableState(t, store, vmRuntimeJournalStatePrepared)
}

func TestVMRuntimeJournalClassifiesOwnedDBProjection(t *testing.T) {
	root := t.TempDir()
	store := openTestVMRuntimeJournalStore(t, root)
	defer func() { _ = store.Close() }()
	records := validVMRuntimeJournalCohort(root, "alpha", "beta")
	if err := store.Prepare(records); err != nil {
		t.Fatal(err)
	}
	groups, err := store.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	group := groups[0]
	data := &db.Data{Services: map[string]*db.Service{}}
	for _, record := range records {
		data.Services[record.Service] = serviceForVMRuntimeJournalProjection(record.OldDB)
	}
	data.Services["alpha"].VM.CPUs = 99
	if got := group.ClassifyDB(data); got != vmRuntimeJournalDBOld {
		t.Fatalf("old classification = %q", got)
	}
	for _, record := range records {
		data.Services[record.Service] = serviceForVMRuntimeJournalProjection(record.NewDB)
	}
	if got := group.ClassifyDB(data); got != vmRuntimeJournalDBNew {
		t.Fatalf("new classification = %q", got)
	}
	data.Services["alpha"] = serviceForVMRuntimeJournalProjection(records[0].OldDB)
	if got := group.ClassifyDB(data); got != vmRuntimeJournalDBMixed {
		t.Fatalf("mixed classification = %q", got)
	}
	data.Services["beta"].VM.Image.Kernel = "/unexpected/kernel"
	if got := group.ClassifyDB(data); got != vmRuntimeJournalDBNeither {
		t.Fatalf("neither classification = %q", got)
	}
}

func TestVMRuntimeJournalLockedFinalizerExcludesConcurrentDatabaseWriter(t *testing.T) {
	root := t.TempDir()
	store := openTestVMRuntimeJournalStore(t, root)
	defer func() { _ = store.Close() }()
	records := validVMRuntimeJournalCohort(root, "alpha")
	if err := store.Prepare(records); err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(records[0].TransactionID, vmRuntimeJournalStateDerivedPublished, records[0].UpdatedAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(records[0].TransactionID, vmRuntimeJournalStateDatabaseCommitted, records[0].UpdatedAt.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginRemoval(records[0].TransactionID); err == nil {
		t.Fatal("BeginRemoval did not stop at the fresh-agreement boundary")
	} else {
		var pending *vmRuntimeJournalRemovalPendingError
		if !errors.As(err, &pending) {
			t.Fatal(err)
		}
	}

	databasePath := filepath.Join(root, "db.json")
	database := db.NewStore(databasePath, filepath.Join(root, "services"))
	if err := database.Set(&db.Data{Services: map[string]*db.Service{
		"alpha": serviceForVMRuntimeJournalProjection(records[0].NewDB),
	}}); err != nil {
		t.Fatal(err)
	}
	other := db.NewStore(databasePath, filepath.Join(root, "services"))
	verifyEntered := make(chan struct{})
	releaseVerify := make(chan struct{})
	finalized := make(chan error, 1)
	go func() {
		finalized <- store.FinalizeRemovalWithLatestDataLocked(
			records[0].TransactionID, database, vmRuntimeJournalDBNew,
			func(got []vmRuntimeJournalRecord) error {
				if len(got) != 1 || got[0].Service != "alpha" {
					return fmt.Errorf("unexpected finalizer records: %#v", got)
				}
				close(verifyEntered)
				<-releaseVerify
				return nil
			},
		)
	}()
	<-verifyEntered
	writerDone := make(chan error, 1)
	go func() {
		_, err := other.MutateData(func(data *db.Data) error {
			data.Services["alpha"].VM.CPUs = 8
			return nil
		})
		writerDone <- err
	}()
	select {
	case err := <-writerDone:
		t.Fatalf("concurrent writer crossed locked finalization: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseVerify)
	if err := <-finalized; err != nil {
		t.Fatal(err)
	}
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}
	groups, err := store.LoadAll()
	if err != nil || len(groups) != 0 {
		t.Fatalf("groups after locked finalization = %#v, %v", groups, err)
	}
}

func TestVMRuntimeJournalLockedFinalizerRetainsMarkerOnAgreementFailure(t *testing.T) {
	root := t.TempDir()
	store := openTestVMRuntimeJournalStore(t, root)
	defer func() { _ = store.Close() }()
	records := validVMRuntimeJournalCohort(root, "alpha")
	if err := store.Prepare(records); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginRemoval(records[0].TransactionID); err == nil {
		t.Fatal("BeginRemoval did not return removal-pending")
	}
	database := db.NewStore(filepath.Join(root, "db.json"), filepath.Join(root, "services"))
	if err := database.Set(&db.Data{Services: map[string]*db.Service{
		"alpha": serviceForVMRuntimeJournalProjection(records[0].OldDB),
	}}); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("derived state drifted")
	err := store.FinalizeRemovalWithLatestDataLocked(records[0].TransactionID, database, vmRuntimeJournalDBOld, func([]vmRuntimeJournalRecord) error {
		return injected
	})
	if !errors.Is(err, injected) {
		t.Fatalf("Finalize error = %v, want verifier failure", err)
	}
	groups, loadErr := store.LoadAll()
	if loadErr != nil || len(groups) != 1 || groups[0].Phase != vmRuntimeJournalPhaseTombstoned {
		t.Fatalf("retained group = %#v, %v", groups, loadErr)
	}
}

func TestVMRuntimeJournalCleansTombstonesAfterCommittedMarkerAbsence(t *testing.T) {
	root := t.TempDir()
	store := openTestVMRuntimeJournalStore(t, root)
	defer func() { _ = store.Close() }()
	records := validVMRuntimeJournalCohort(root, "alpha")
	if err := store.Prepare(records); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginRemoval(records[0].TransactionID); err == nil {
		t.Fatal("BeginRemoval did not return removal-pending")
	}
	marker := readVMRuntimeJournalTestMarker(t, root, records[0].TransactionID)
	if err := store.commitMarkerRemoval(marker); err != nil {
		t.Fatal(err)
	}
	if err := store.CleanupCommittedTombstones(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, vmRuntimeJournalDirName))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if isVMRuntimeJournalRecordTombstoneName(entry.Name()) {
			t.Fatalf("orphan tombstone remains: %s", entry.Name())
		}
	}
}

func openTestVMRuntimeJournalStore(t testing.TB, root string) *vmRuntimeJournalStore {
	t.Helper()
	store, err := openVMRuntimeJournalStore(context.Background(), root, testVMRuntimeJournalStoreDeps())
	if err != nil {
		t.Fatalf("openVMRuntimeJournalStore: %v", err)
	}
	return store
}

func assertVMRuntimeJournalStableState(t testing.TB, store *vmRuntimeJournalStore, want vmRuntimeJournalState) {
	t.Helper()
	groups, err := store.LoadAll()
	if err != nil || len(groups) != 1 || groups[0].Phase != vmRuntimeJournalPhaseStable || len(groups[0].Records) != 2 {
		t.Fatalf("stable group = %#v, error %v", groups, err)
	}
	for _, record := range groups[0].Records {
		if record.State != want {
			t.Fatalf("record %s state = %s, want %s", record.Service, record.State, want)
		}
	}
}

func testVMRuntimeJournalStoreDeps() vmRuntimeJournalStoreDeps {
	deps := defaultVMRuntimeJournalStoreDeps()
	deps.uid = uint32(os.Geteuid())
	return deps
}

func validVMRuntimeJournalCohort(root string, services ...string) []vmRuntimeJournalRecord {
	members := append([]string(nil), services...)
	records := make([]vmRuntimeJournalRecord, 0, len(services))
	preparedAt := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	transactionID := strings.Repeat("a", 64)
	for i, service := range services {
		serviceRoot := filepath.Join(root, service)
		descriptorPath := filepath.Join(serviceDataDirForRoot(serviceRoot), vmRuntimeDescriptorFileName)
		unitPath := filepath.Join(vmSystemdSystemDir, vmSystemdUnitName(service))
		oldDescriptor := []byte("old descriptor " + service + "\x00")
		newDescriptor := []byte("new descriptor " + service + "\n")
		oldUnit := []byte("old unit " + service + "\n")
		newUnit := []byte("new unit " + service + "\n")
		records = append(records, vmRuntimeJournalRecord{
			Schema:             vmRuntimeJournalSchema,
			SchemaVersion:      vmRuntimeJournalSchemaVersion,
			TransactionID:      transactionID,
			Members:            append([]string(nil), members...),
			Service:            service,
			ServiceRoot:        serviceRoot,
			State:              vmRuntimeJournalStatePrepared,
			PreparedAt:         preparedAt,
			UpdatedAt:          preparedAt,
			PreconditionSHA256: strings.Repeat(string(rune('b'+i)), 64),
			OldDB: vmRuntimeJournalDBProjection{
				Components:  &db.VMComponentsConfig{GuestBase: db.VMGuestBaseConfig{ID: "old-" + service}},
				ImageKernel: "/kernels/old-" + service,
			},
			NewDB: vmRuntimeJournalDBProjection{
				Components:  &db.VMComponentsConfig{GuestBase: db.VMGuestBaseConfig{ID: "new-" + service}},
				ImageKernel: "/kernels/new-" + service,
			},
			OldDescriptor: vmRuntimeJournalFileFromBytes(descriptorPath, true, oldDescriptor, unix.S_IFREG|0o600, 0, 0),
			NewDescriptor: vmRuntimeJournalFileFromBytes(descriptorPath, true, newDescriptor, unix.S_IFREG|0o600, 0, 0),
			OldUnit:       vmRuntimeJournalFileFromBytes(unitPath, true, oldUnit, unix.S_IFREG|0o644, 0, 0),
			NewUnit:       vmRuntimeJournalFileFromBytes(unitPath, true, newUnit, unix.S_IFREG|0o644, 0, 0),
		})
	}
	return records
}

func vmRuntimeJournalFileFromBytes(path string, exists bool, contents []byte, mode, uid, gid uint32) vmRuntimeJournalFile {
	if !exists {
		return vmRuntimeJournalFile{Path: path}
	}
	digest := sha256.Sum256(contents)
	return vmRuntimeJournalFile{
		Path: path, Exists: true, Contents: append([]byte(nil), contents...), Mode: mode, UID: uid, GID: gid,
		SHA256: hex.EncodeToString(digest[:]),
	}
}

func marshalVMRuntimeJournalTestRecord(t testing.TB, record vmRuntimeJournalRecord) []byte {
	t.Helper()
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func writeVMRuntimeJournalTestRecord(t testing.TB, root, filenameService string, raw []byte) {
	t.Helper()
	path := filepath.Join(root, vmRuntimeJournalDirName, filenameService+".json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeVMRuntimeJournalTestStableMarker(t testing.TB, root string, records []vmRuntimeJournalRecord) {
	t.Helper()
	operationID := strings.Repeat("c", 64)
	plans, err := buildVMRuntimeJournalPlans(operationID, vmRuntimeJournalOperationStable, records, records)
	if err != nil {
		t.Fatal(err)
	}
	marker := vmRuntimeJournalMarker{
		Schema: vmRuntimeJournalMarkerSchema, SchemaVersion: vmRuntimeJournalMarkerSchemaVersion,
		TransactionID: records[0].TransactionID, OperationID: operationID,
		Members: append([]string(nil), records[0].Members...), Plans: plans,
		Operation: vmRuntimeJournalOperationStable, Phase: vmRuntimeJournalPhaseStable,
		FromState: records[0].State, TargetState: records[0].State, UpdatedAt: records[0].UpdatedAt,
		Desired: cloneVMRuntimeJournalRecords(records),
	}
	raw, err := encodeVMRuntimeJournalMarker(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, vmRuntimeJournalDirName, vmRuntimeJournalMarkerName(marker.TransactionID)), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readVMRuntimeJournalTestMarker(t testing.TB, root, transactionID string) vmRuntimeJournalMarker {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, vmRuntimeJournalDirName, vmRuntimeJournalMarkerName(transactionID)))
	if err != nil {
		t.Fatal(err)
	}
	marker, err := decodeVMRuntimeJournalMarker(raw)
	if err != nil {
		t.Fatal(err)
	}
	return marker
}

func serviceForVMRuntimeJournalProjection(projection vmRuntimeJournalDBProjection) *db.Service {
	return &db.Service{VM: &db.VMConfig{
		Components: projection.Components.Clone(),
		Image:      db.VMImageConfig{Kernel: projection.ImageKernel},
	}}
}

func FuzzVMRuntimeJournalDecode(f *testing.F) {
	record := validVMRuntimeJournalCohort("/var/lib/yeet", "alpha")[0]
	f.Add(marshalVMRuntimeJournalTestRecord(f, record))
	f.Add([]byte(`{"schema":"yeet.vm.runtime-transaction"}`))
	f.Add([]byte(`{"schema":"x","schema":"y"}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = decodeVMRuntimeJournalRecord(raw)
	})
}

func FuzzVMRuntimeJournalMarkerDecode(f *testing.F) {
	records := validVMRuntimeJournalCohort("/var/lib/yeet", "alpha", "beta")
	operationID := strings.Repeat("c", 64)
	plans, err := buildVMRuntimeJournalPlans(operationID, vmRuntimeJournalOperationStable, records, records)
	if err != nil {
		f.Fatal(err)
	}
	marker := vmRuntimeJournalMarker{
		Schema: vmRuntimeJournalMarkerSchema, SchemaVersion: vmRuntimeJournalMarkerSchemaVersion,
		TransactionID: records[0].TransactionID, OperationID: operationID,
		Members: append([]string(nil), records[0].Members...), Plans: plans,
		Operation: vmRuntimeJournalOperationStable, Phase: vmRuntimeJournalPhaseStable,
		FromState: records[0].State, TargetState: records[0].State, UpdatedAt: records[0].UpdatedAt,
		Desired: cloneVMRuntimeJournalRecords(records),
	}
	raw, err := encodeVMRuntimeJournalMarker(marker)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(raw)
	f.Add([]byte(`{"schema":"yeet.vm.runtime-transaction-marker"}`))
	f.Add([]byte(`{"schema":"x","schema":"y"}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = decodeVMRuntimeJournalMarker(raw)
	})
}
