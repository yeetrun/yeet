// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestServiceIdentityJournalRejectsInvalidServiceBeforePathConstruction(t *testing.T) {
	_, err := createServiceIdentityJournal(t.TempDir(), serviceIdentityJournalHeader{
		Version: 1, ID: "tx", Service: "../escape", Root: "/srv/api",
		TargetIdentity: db.ServiceIdentity{UID: 1000, GID: 1000},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid service") {
		t.Fatalf("createServiceIdentityJournal error = %v, want invalid service", err)
	}
}

func TestServiceIdentityJournalValidatesHeaderIdentitiesAtCreate(t *testing.T) {
	tests := []struct {
		name     string
		target   db.ServiceIdentity
		previous *db.ServiceIdentity
		want     string
	}{
		{name: "target", target: db.ServiceIdentity{RequestedUser: "app"}, want: "invalid target identity"},
		{name: "previous", target: db.ServiceIdentity{}, previous: &db.ServiceIdentity{RequestedGroup: "app"}, want: "invalid previous identity"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := createServiceIdentityJournal(t.TempDir(), serviceIdentityJournalHeader{
				Version: 1, ID: "tx", Service: "api", Root: "/srv/api",
				PreviousIdentity: tt.previous, TargetIdentity: tt.target,
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("createServiceIdentityJournal error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestServiceIdentityJournalUsesPrivateExclusiveNoFollowFiles(t *testing.T) {
	stateRoot := t.TempDir()
	header := serviceIdentityJournalHeader{
		Version: 1, ID: "tx", Service: "api", Root: "/srv/api",
		TargetIdentity: db.ServiceIdentity{UID: 1000, GID: 1000},
	}
	j, err := createServiceIdentityJournal(stateRoot, header)
	if err != nil {
		t.Fatal(err)
	}
	path := j.Path()
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("journal mode = %04o, want 0600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("journal directory mode = %04o, want 0700", dirInfo.Mode().Perm())
	}
	if _, err := createServiceIdentityJournal(stateRoot, header); err == nil || !strings.Contains(err.Error(), "file exists") {
		t.Fatalf("duplicate journal error = %v, want exclusive create", err)
	}

	linkHeader := header
	linkHeader.ID = "linked"
	linkPath := serviceIdentityJournalPath(stateRoot, linkHeader.Service, linkHeader.ID)
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, linkPath); err != nil {
		t.Fatal(err)
	}
	if _, err := createServiceIdentityJournal(stateRoot, linkHeader); err == nil {
		t.Fatal("createServiceIdentityJournal followed existing symlink")
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "untouched" {
		t.Fatalf("symlink target = %q, %v", got, err)
	}
}

func TestServiceIdentityJournalFsyncsEveryNewDirectoryEntry(t *testing.T) {
	stateRoot := t.TempDir()
	oldSyncDir := serviceIdentityJournalDirectorySync
	var synced []string
	serviceIdentityJournalDirectorySync = func(path string) error {
		synced = append(synced, filepath.Clean(path))
		return nil
	}
	t.Cleanup(func() { serviceIdentityJournalDirectorySync = oldSyncDir })

	j, err := createServiceIdentityJournal(stateRoot, serviceIdentityJournalHeader{
		Version: 1, ID: "tx", Service: "api", Root: "/srv/api",
		TargetIdentity: db.ServiceIdentity{UID: 1000, GID: 1000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}

	want := []string{
		filepath.Clean(stateRoot),
		filepath.Join(stateRoot, "migrations"),
		filepath.Join(stateRoot, "migrations", "service-identity"),
	}
	if !reflect.DeepEqual(synced, want) {
		t.Fatalf("synced directories = %#v, want %#v", synced, want)
	}
}

func TestServiceIdentityJournalFsyncsHeaderBatchesAndSeal(t *testing.T) {
	oldSync := serviceIdentityJournalSync
	syncs := 0
	serviceIdentityJournalSync = func(*os.File) error { syncs++; return nil }
	t.Cleanup(func() { serviceIdentityJournalSync = oldSync })

	j, err := createServiceIdentityJournal(t.TempDir(), serviceIdentityJournalHeader{
		Version: 1, ID: "tx", Service: "api", Root: "/srv/api",
		TargetIdentity: db.ServiceIdentity{UID: 1000, GID: 1000},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if syncs != 1 {
		t.Fatalf("syncs after header = %d, want 1", syncs)
	}
	for i := range serviceIdentityJournalSyncBatch {
		if err := j.AppendInode(serviceIdentityInodeRecord{Path: filepath.Join("data", "f"+string(rune(i+1))), UID: 0, GID: 0, Mode: 0o600}); err != nil {
			t.Fatal(err)
		}
	}
	if syncs != 2 {
		t.Fatalf("syncs after batch = %d, want 2", syncs)
	}
	if err := j.Seal(); err != nil {
		t.Fatal(err)
	}
	if syncs != 3 {
		t.Fatalf("syncs after seal = %d, want 3", syncs)
	}
}

func TestServiceIdentityJournalAppendPhaseRequiresSealAndDurableWrite(t *testing.T) {
	j, err := createServiceIdentityJournal(t.TempDir(), serviceIdentityJournalHeader{
		Version: 1, ID: "tx", Service: "api", Root: "/srv/api",
		TargetIdentity: db.ServiceIdentity{UID: 1000, GID: 1000},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()

	if err := j.AppendPhase(serviceIdentityPhaseRecord{Phase: serviceIdentityPhaseOwnership}); err == nil || !strings.Contains(err.Error(), "must be sealed") {
		t.Fatalf("AppendPhase before seal error = %v, want sealed refusal", err)
	}
	if err := j.Seal(); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"", " \t", serviceIdentityJournalSealPhase} {
		err := j.AppendPhase(serviceIdentityPhaseRecord{Phase: phase})
		if err == nil {
			t.Fatalf("AppendPhase(%q) unexpectedly succeeded", phase)
		}
	}

	oldSync := serviceIdentityJournalSync
	syncs := 0
	serviceIdentityJournalSync = func(*os.File) error { syncs++; return nil }
	t.Cleanup(func() { serviceIdentityJournalSync = oldSync })
	if err := j.AppendPhase(serviceIdentityPhaseRecord{Phase: serviceIdentityPhaseOwnership}); err != nil {
		t.Fatal(err)
	}
	if syncs != 1 {
		t.Fatalf("phase syncs = %d, want 1", syncs)
	}
}

func TestServiceIdentityJournalAppendPhaseMakesSyncFailureSticky(t *testing.T) {
	j, err := createServiceIdentityJournal(t.TempDir(), serviceIdentityJournalHeader{
		Version: 1, ID: "tx", Service: "api", Root: "/srv/api",
		TargetIdentity: db.ServiceIdentity{UID: 1000, GID: 1000},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if err := j.Seal(); err != nil {
		t.Fatal(err)
	}

	oldSync := serviceIdentityJournalSync
	wantErr := errors.New("sync denied")
	serviceIdentityJournalSync = func(*os.File) error { return wantErr }
	t.Cleanup(func() { serviceIdentityJournalSync = oldSync })
	if err := j.AppendPhase(serviceIdentityPhaseRecord{Phase: serviceIdentityPhaseOwnership}); !errors.Is(err, wantErr) {
		t.Fatalf("AppendPhase sync error = %v, want %v", err, wantErr)
	}
	if err := j.AppendPhase(serviceIdentityPhaseRecord{Phase: "ownership-complete"}); !errors.Is(err, wantErr) {
		t.Fatalf("AppendPhase after sync failure = %v, want sticky %v", err, wantErr)
	}
}

func TestServiceIdentityJournalRequiresSealBeforeMutation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "api")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	j, err := createServiceIdentityJournal(t.TempDir(), serviceIdentityJournalHeader{
		Version: 1, ID: "tx", Service: "api", Root: root,
		TargetIdentity: db.ServiceIdentity{UID: 1000, GID: 1000},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	inspection := serviceIdentityInspection{Mutations: []serviceIdentityMutation{{Path: root, UID: 1000, GID: 1000}}}
	if err := applyServiceIdentityInspection(inspection, j); err == nil || !strings.Contains(err.Error(), "sealed") {
		t.Fatalf("applyServiceIdentityInspection error = %v, want sealed journal", err)
	}
}

func TestServiceIdentityJournalCorruptLineRefusesRestoreBeforeMutation(t *testing.T) {
	root, journalPath := writeTestServiceIdentityJournal(t, false)
	if err := os.WriteFile(journalPath, []byte(`{"version":1,"id":"tx","service":"api","root":"`+root+`","targetIdentity":{"uid":1000,"gid":1000}}`+"\n"+`{"path":"data/file"`), 0o600); err != nil {
		t.Fatal(err)
	}
	oldMutation := serviceIdentityRestoreMutation
	called := false
	serviceIdentityRestoreMutation = func(string, string, serviceIdentityInodeRecord, uint32, uint32, os.FileMode, bool) error {
		called = true
		return nil
	}
	t.Cleanup(func() { serviceIdentityRestoreMutation = oldMutation })

	if err := restoreServiceIdentityJournal(journalPath); err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("restoreServiceIdentityJournal error = %v, want truncated", err)
	}
	if called {
		t.Fatal("restore changed ownership before validating the whole journal")
	}
}

func TestServiceIdentityJournalUnknownRecordRefusesRestore(t *testing.T) {
	root, journalPath := writeTestServiceIdentityJournal(t, false)
	raw := `{"version":1,"id":"tx","service":"api","root":"` + root + `","targetIdentity":{"uid":1000,"gid":1000}}` + "\n" +
		`{"wat":"unknown"}` + "\n"
	if err := os.WriteFile(journalPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := restoreServiceIdentityJournal(journalPath); err == nil || !strings.Contains(err.Error(), "unknown record type") {
		t.Fatalf("restoreServiceIdentityJournal error = %v, want unknown record", err)
	}
}

func TestServiceIdentityJournalRefusesPhaseBeforeSealAndDuplicateInode(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "phase before seal", body: `{"phase":"` + serviceIdentityPhaseOwnership + `"}` + "\n", want: "before journal seal"},
		{name: "duplicate inode", body: `{"path":"data/file","uid":0,"gid":0,"mode":384}` + "\n" +
			`{"path":"data/file","uid":0,"gid":0,"mode":384}` + "\n" + `{"phase":"inventory-sealed"}` + "\n", want: "duplicate inode path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, journalPath := writeTestServiceIdentityJournal(t, false)
			raw := `{"version":1,"id":"tx","service":"api","root":"` + root + `","targetIdentity":{"uid":1000,"gid":1000}}` + "\n" + tt.body
			if err := os.WriteFile(journalPath, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadServiceIdentityJournal(journalPath); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("loadServiceIdentityJournal error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestServiceIdentityJournalPathEscapeRefusesRestoreBeforeMutation(t *testing.T) {
	root, journalPath := writeTestServiceIdentityJournal(t, false)
	raw := `{"version":1,"id":"tx","service":"api","root":"` + root + `","targetIdentity":{"uid":1000,"gid":1000}}` + "\n" +
		`{"path":"../escape","uid":0,"gid":0,"mode":384}` + "\n" +
		`{"phase":"inventory-sealed"}` + "\n"
	if err := os.WriteFile(journalPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	oldMutation := serviceIdentityRestoreMutation
	called := false
	serviceIdentityRestoreMutation = func(string, string, serviceIdentityInodeRecord, uint32, uint32, os.FileMode, bool) error {
		called = true
		return nil
	}
	t.Cleanup(func() { serviceIdentityRestoreMutation = oldMutation })
	if err := restoreServiceIdentityJournal(journalPath); err == nil || !strings.Contains(err.Error(), "invalid service identity journal inode path") {
		t.Fatalf("restoreServiceIdentityJournal error = %v, want path refusal", err)
	}
	if called {
		t.Fatal("path escape journal performed an ownership mutation")
	}
}

func TestServiceIdentityJournalRestoresInReverseAndLchownsSymlink(t *testing.T) {
	root, journalPath := writeTestServiceIdentityJournal(t, true)
	file := filepath.Join(root, "data", "file")
	if err := os.WriteFile(file, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}

	oldMutation := serviceIdentityRestoreMutation
	var calls []string
	serviceIdentityRestoreMutation = func(root, rel string, _ serviceIdentityInodeRecord, _, _ uint32, _ os.FileMode, changeMode bool) error {
		calls = append(calls, "chown:"+filepath.Base(filepath.Join(root, rel)))
		if changeMode {
			calls = append(calls, "chmod:"+filepath.Base(filepath.Join(root, rel)))
		}
		return nil
	}
	t.Cleanup(func() { serviceIdentityRestoreMutation = oldMutation })

	if err := restoreServiceIdentityJournal(journalPath); err != nil {
		t.Fatal(err)
	}
	want := []string{"chown:link", "chown:file", "chmod:file"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("restore calls = %#v, want %#v", calls, want)
	}
	if _, err := os.Stat(journalPath); !os.IsNotExist(err) {
		t.Fatalf("journal stat after complete restore = %v, want removed", err)
	}
}

func TestServiceIdentityJournalRetainsJournalWhenRestoreIncomplete(t *testing.T) {
	root, journalPath := writeTestServiceIdentityJournal(t, true)
	if err := os.WriteFile(filepath.Join(root, "data", "file"), []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	oldMutation := serviceIdentityRestoreMutation
	serviceIdentityRestoreMutation = func(root, rel string, _ serviceIdentityInodeRecord, _, _ uint32, _ os.FileMode, _ bool) error {
		if filepath.Base(filepath.Join(root, rel)) == "file" {
			return errors.New("denied")
		}
		return nil
	}
	t.Cleanup(func() { serviceIdentityRestoreMutation = oldMutation })

	if err := restoreServiceIdentityJournal(journalPath); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("restoreServiceIdentityJournal error = %v, want denied", err)
	}
	if _, err := os.Stat(journalPath); err != nil {
		t.Fatalf("journal must remain after incomplete restore: %v", err)
	}
}

func TestServiceIdentityJournalRestoreRefusesReplacementSymlinkWithoutChangingTarget(t *testing.T) {
	stateRoot := t.TempDir()
	root := filepath.Join(t.TempDir(), "api")
	data := filepath.Join(root, "data")
	if err := os.MkdirAll(data, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(data, "file")
	if err := os.WriteFile(path, []byte("owned"), 0o640); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := serviceIdentityMetadata(info)
	if err != nil {
		t.Fatal(err)
	}
	j, err := createServiceIdentityJournal(stateRoot, serviceIdentityJournalHeader{
		Version: 1, ID: "tx", Service: "api", Root: root,
		TargetIdentity: db.ServiceIdentity{UID: 1000, GID: 1000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := j.AppendInode(serviceIdentityInodeRecord{Path: "data/file", UID: meta.UID, GID: meta.GID, Mode: info.Mode(), Dev: meta.Dev, Ino: meta.Ino}); err != nil {
		t.Fatal(err)
	}
	if err := j.Seal(); err != nil {
		t.Fatal(err)
	}
	journalPath := j.Path()
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "external")
	if err := os.WriteFile(external, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, path); err != nil {
		t.Fatal(err)
	}

	if err := restoreServiceIdentityJournal(journalPath); err == nil || !strings.Contains(err.Error(), "inode identity or type changed") {
		t.Fatalf("restoreServiceIdentityJournal error = %v, want sealed inode refusal", err)
	}
	info, err = os.Stat(external)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("external target mode = %04o, want untouched 0600", info.Mode().Perm())
	}
	if _, err := os.Stat(journalPath); err != nil {
		t.Fatalf("journal must remain after replacement refusal: %v", err)
	}
}

func TestServiceIdentityJournalApplyUsesExactSealedInodeBinding(t *testing.T) {
	root := filepath.Join(t.TempDir(), "api")
	if err := os.MkdirAll(filepath.Join(root, "data"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "data", "file"), []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	inspection, err := inspectServiceIdentityChange(context.Background(), serviceIdentityInspectionRequest{
		Root: root, Target: db.ServiceIdentity{UID: uint32(os.Geteuid() + 1), GID: uint32(os.Getegid() + 1)},
		MountPoints: []string{}, ListXattrs: func(string) ([]string, error) { return nil, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	j, err := createServiceIdentityJournal(t.TempDir(), serviceIdentityJournalHeader{
		Version: 1, ID: "tx", Service: "api", Root: root,
		TargetIdentity: db.ServiceIdentity{UID: 1000, GID: 1000},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	for _, record := range inspection.Records {
		if err := j.AppendInode(record); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.Seal(); err != nil {
		t.Fatal(err)
	}
	inspection.Mutations[0].Dev = 0
	oldMutation := serviceIdentityApplyMutation
	called := false
	serviceIdentityApplyMutation = func(string, string, serviceIdentityInodeRecord, uint32, uint32, os.FileMode, bool) error {
		called = true
		return nil
	}
	t.Cleanup(func() { serviceIdentityApplyMutation = oldMutation })
	if err := applyServiceIdentityInspection(inspection, j); err == nil || !strings.Contains(err.Error(), "inode identity") {
		t.Fatalf("applyServiceIdentityInspection error = %v, want inode binding refusal", err)
	}
	if called {
		t.Fatal("mismatched mutation reached filesystem mutation")
	}
}

func TestServiceIdentityJournalApplyValidatesCompleteSealedInventory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "api")
	record := serviceIdentityInodeRecord{Path: "data/file", UID: 0, GID: 0, Mode: 0o640, Dev: 11, Ino: 22}
	mutation := serviceIdentityMutation{
		Path: filepath.Join(root, record.Path), UID: 1000, GID: 1000, Mode: 0o640,
		Dev: record.Dev, Ino: record.Ino,
	}

	newJournal := func(t *testing.T) *serviceIdentityJournal {
		t.Helper()
		j, err := createServiceIdentityJournal(t.TempDir(), serviceIdentityJournalHeader{
			Version: 1, ID: "tx", Service: "api", Root: root,
			TargetIdentity: db.ServiceIdentity{UID: 1000, GID: 1000},
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = j.Close() })
		if err := j.AppendInode(record); err != nil {
			t.Fatal(err)
		}
		if err := j.Seal(); err != nil {
			t.Fatal(err)
		}
		return j
	}

	tests := []struct {
		name   string
		edit   func(*serviceIdentityInspection, *serviceIdentityJournal)
		want   string
		called bool
	}{
		{name: "failed journal", edit: func(_ *serviceIdentityInspection, j *serviceIdentityJournal) { j.failed = errors.New("disk failure") }, want: "durably sealed"},
		{name: "missing mutation", edit: func(i *serviceIdentityInspection, _ *serviceIdentityJournal) { i.Mutations = nil }, want: "complete mutation inventory"},
		{name: "journal count mismatch", edit: func(_ *serviceIdentityInspection, j *serviceIdentityJournal) { j.inodeCount = 0 }, want: "complete mutation inventory"},
		{name: "record mismatch", edit: func(i *serviceIdentityInspection, _ *serviceIdentityJournal) { i.Records[0].UID++ }, want: "inventory differs"},
		{name: "relative path mismatch", edit: func(i *serviceIdentityInspection, _ *serviceIdentityJournal) {
			i.Mutations[0].Path = filepath.Join(root, "data", "other")
		}, want: "does not match sealed inode"},
		{name: "record path absent", edit: func(_ *serviceIdentityInspection, j *serviceIdentityJournal) { delete(j.recordPaths, record.Path) }, want: "absent from sealed journal"},
		{name: "device mismatch", edit: func(i *serviceIdentityInspection, _ *serviceIdentityJournal) { i.Mutations[0].Dev++ }, want: "inode identity"},
		{name: "inode mismatch", edit: func(i *serviceIdentityInspection, _ *serviceIdentityJournal) { i.Mutations[0].Ino++ }, want: "inode identity"},
		{name: "symlink mismatch", edit: func(i *serviceIdentityInspection, _ *serviceIdentityJournal) { i.Mutations[0].Symlink = true }, want: "inode identity"},
		{name: "mutation failure", edit: func(_ *serviceIdentityInspection, _ *serviceIdentityJournal) {}, want: "mutation denied", called: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := newJournal(t)
			inspection := serviceIdentityInspection{Records: []serviceIdentityInodeRecord{record}, Mutations: []serviceIdentityMutation{mutation}}
			tt.edit(&inspection, j)
			oldMutation := serviceIdentityApplyMutation
			called := false
			serviceIdentityApplyMutation = func(string, string, serviceIdentityInodeRecord, uint32, uint32, os.FileMode, bool) error {
				called = true
				return errors.New("mutation denied")
			}
			t.Cleanup(func() { serviceIdentityApplyMutation = oldMutation })
			err := applyServiceIdentityInspection(inspection, j)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("applyServiceIdentityInspection error = %v, want %q", err, tt.want)
			}
			if called != tt.called {
				t.Fatalf("mutation called = %t, want %t", called, tt.called)
			}
		})
	}
}

func TestServiceIdentityJournalApplyMutatesMatchingInventory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "api")
	record := serviceIdentityInodeRecord{Path: "data/link", UID: 0, GID: 0, Mode: os.ModeSymlink | 0o777, Dev: 11, Ino: 22}
	mutation := serviceIdentityMutation{
		Path: filepath.Join(root, record.Path), UID: 1000, GID: 1001, Mode: record.Mode,
		Dev: record.Dev, Ino: record.Ino, Symlink: true,
	}
	j, err := createServiceIdentityJournal(t.TempDir(), serviceIdentityJournalHeader{
		Version: 1, ID: "tx", Service: "api", Root: root,
		TargetIdentity: db.ServiceIdentity{UID: mutation.UID, GID: mutation.GID},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if err := j.AppendInode(record); err != nil {
		t.Fatal(err)
	}
	if err := j.Seal(); err != nil {
		t.Fatal(err)
	}

	oldMutation := serviceIdentityApplyMutation
	var got struct {
		root, rel  string
		expected   serviceIdentityInodeRecord
		uid, gid   uint32
		mode       os.FileMode
		changeMode bool
	}
	serviceIdentityApplyMutation = func(root, rel string, expected serviceIdentityInodeRecord, uid, gid uint32, mode os.FileMode, changeMode bool) error {
		got.root, got.rel, got.expected = root, rel, expected
		got.uid, got.gid, got.mode, got.changeMode = uid, gid, mode, changeMode
		return nil
	}
	t.Cleanup(func() { serviceIdentityApplyMutation = oldMutation })
	if err := applyServiceIdentityInspection(serviceIdentityInspection{
		Records: []serviceIdentityInodeRecord{record}, Mutations: []serviceIdentityMutation{mutation},
	}, j); err != nil {
		t.Fatal(err)
	}
	want := struct {
		root, rel  string
		expected   serviceIdentityInodeRecord
		uid, gid   uint32
		mode       os.FileMode
		changeMode bool
	}{root: root, rel: record.Path, expected: record, uid: mutation.UID, gid: mutation.GID, mode: mutation.Mode}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mutation arguments = %#v, want %#v", got, want)
	}
}

func TestDecodeServiceIdentityJournalLineValidatesRecordTransitions(t *testing.T) {
	validHeader := `{"version":1,"id":"tx","service":"api","root":"/srv/api","targetIdentity":{"uid":1000,"gid":1000}}`
	tests := []struct {
		name     string
		line     string
		number   int
		contents serviceIdentityJournalContents
		wantErr  string
		wantKind string
	}{
		{name: "invalid JSON", line: `{`, number: 1, wantErr: "decode line 1"},
		{name: "first line is not header", line: `{"path":"data/file"}`, number: 1, wantErr: "immutable header"},
		{name: "unknown header field", line: strings.TrimSuffix(validHeader, `}`) + `,"extra":true}`, number: 1, wantErr: "unknown field"},
		{name: "unsupported version", line: strings.Replace(validHeader, `"version":1`, `"version":2`, 1), number: 1, wantErr: "unsupported version"},
		{name: "invalid service", line: strings.Replace(validHeader, `"api"`, `"../api"`, 1), number: 1, wantErr: "invalid service"},
		{name: "invalid id", line: strings.Replace(validHeader, `"tx"`, `"bad/id"`, 1), number: 1, wantErr: "invalid service identity journal id"},
		{name: "invalid target identity", line: strings.Replace(validHeader, `"uid":1000,"gid":1000`, `"requestedUser":"app"`, 1), number: 1, wantErr: "invalid target identity"},
		{name: "invalid previous identity", line: strings.TrimSuffix(validHeader, `}`) + `,"previousIdentity":{"requestedGroup":"app"}}`, number: 1, wantErr: "invalid previous identity"},
		{name: "unclean root", line: strings.Replace(validHeader, `/srv/api`, `/srv/../api`, 1), number: 1, wantErr: "invalid journal root"},
		{name: "valid header", line: validHeader, number: 1, wantKind: "header"},
		{name: "header rewrite", line: validHeader, number: 2, wantErr: "rewrites immutable header"},
		{name: "unknown inode field", line: `{"path":"data/file","extra":true}`, number: 2, wantErr: "unknown field"},
		{name: "invalid inode path", line: `{"path":"../file"}`, number: 2, wantErr: "invalid service identity journal inode path"},
		{name: "inode after seal", line: `{"path":"data/file"}`, number: 2, contents: serviceIdentityJournalContents{Sealed: true}, wantErr: "after journal seal"},
		{name: "valid inode", line: `{"path":"data/file","uid":1,"gid":2,"mode":384,"dev":3,"ino":4}`, number: 2, wantKind: "inode"},
		{name: "unknown phase field", line: `{"phase":"` + serviceIdentityPhaseOwnership + `","extra":true}`, number: 2, wantErr: "unknown field"},
		{name: "empty phase", line: `{"phase":" "}`, number: 2, wantErr: "empty phase"},
		{name: "phase before seal", line: `{"phase":"` + serviceIdentityPhaseOwnership + `"}`, number: 2, wantErr: "before journal seal"},
		{name: "valid seal", line: `{"phase":"inventory-sealed"}`, number: 2, wantKind: "seal"},
		{name: "duplicate seal", line: `{"phase":"inventory-sealed"}`, number: 3, contents: serviceIdentityJournalContents{Sealed: true}, wantErr: "duplicates journal seal"},
		{name: "valid post-seal phase", line: `{"phase":"` + serviceIdentityPhaseOwnership + `"}`, number: 3, contents: serviceIdentityJournalContents{Sealed: true}, wantKind: "phase"},
		{name: "unknown record", line: `{"wat":true}`, number: 2, wantErr: "unknown record type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contents := tt.contents
			err := decodeServiceIdentityJournalLine([]byte(tt.line), tt.number, &contents)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("decodeServiceIdentityJournalLine error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			switch tt.wantKind {
			case "header":
				if contents.Header.Service != "api" {
					t.Fatalf("header = %#v", contents.Header)
				}
			case "inode":
				if len(contents.Inodes) != 1 || contents.Inodes[0].Path != "data/file" {
					t.Fatalf("inodes = %#v", contents.Inodes)
				}
			case "seal":
				if !contents.Sealed || len(contents.Phases) != 1 {
					t.Fatalf("seal contents = %#v", contents)
				}
			case "phase":
				if len(contents.Phases) != 1 || contents.Phases[0].Phase != serviceIdentityPhaseOwnership {
					t.Fatalf("phase contents = %#v", contents)
				}
			}
		})
	}
}

func TestMutateServiceIdentityPathChangesOnlyBoundInodes(t *testing.T) {
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
	t.Run("regular file", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "data", "file")
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		meta, err := serviceIdentityMetadata(info)
		if err != nil {
			t.Fatal(err)
		}
		expected := serviceIdentityInodeRecord{Path: "data/file", UID: meta.UID, GID: meta.GID, Mode: info.Mode(), Dev: meta.Dev, Ino: meta.Ino, Nlink: meta.Nlink}
		if err := mutateServiceIdentityPath(root, "data/file", expected, uid, gid, 0o640|os.ModeSetgid, true); err != nil {
			t.Fatal(err)
		}
		info, err = os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o640 || info.Mode()&os.ModeSetgid == 0 {
			t.Fatalf("mode = %v, want 02640", info.Mode())
		}
	})

	t.Run("root directory", func(t *testing.T) {
		root := t.TempDir()
		info, err := os.Lstat(root)
		if err != nil {
			t.Fatal(err)
		}
		meta, err := serviceIdentityMetadata(info)
		if err != nil {
			t.Fatal(err)
		}
		expected := serviceIdentityInodeRecord{Path: ".", UID: meta.UID, GID: meta.GID, Mode: info.Mode(), Dev: meta.Dev, Ino: meta.Ino, Nlink: meta.Nlink}
		if err := mutateServiceIdentityPath(root, ".", expected, uid, gid, 0o750, true); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "link")
		if err := os.Symlink("missing-target", path); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		meta, err := serviceIdentityMetadata(info)
		if err != nil {
			t.Fatal(err)
		}
		expected := serviceIdentityInodeRecord{Path: "link", UID: meta.UID, GID: meta.GID, Mode: info.Mode(), Dev: meta.Dev, Ino: meta.Ino, Nlink: meta.Nlink}
		if err := mutateServiceIdentityPath(root, "link", expected, uid, gid, info.Mode(), false); err != nil {
			t.Fatal(err)
		}
	})
}

func TestMutateServiceIdentityPathRejectsUnsafeOrReplacedPaths(t *testing.T) {
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
	tests := []struct {
		name    string
		prepare func(t *testing.T) (root, rel string, dev, ino uint64, symlink bool)
		want    string
	}{
		{name: "invalid relative path", prepare: func(t *testing.T) (string, string, uint64, uint64, bool) {
			return t.TempDir(), "../escape", 0, 0, false
		}, want: "invalid service identity journal inode path"},
		{name: "missing root", prepare: func(t *testing.T) (string, string, uint64, uint64, bool) {
			return filepath.Join(t.TempDir(), "missing"), ".", 0, 0, false
		}, want: "open service identity root"},
		{name: "root cannot be symlink record", prepare: func(t *testing.T) (string, string, uint64, uint64, bool) { return t.TempDir(), ".", 0, 0, true }, want: "root cannot be restored as a symlink"},
		{name: "missing symlink", prepare: func(t *testing.T) (string, string, uint64, uint64, bool) { return t.TempDir(), "missing", 0, 0, true }, want: "open symlink"},
		{name: "regular replaced expected symlink", prepare: func(t *testing.T) (string, string, uint64, uint64, bool) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "path"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			return root, "path", 0, 0, true
		}, want: "path type changed"},
		{name: "symlink replaced expected regular", prepare: func(t *testing.T) (string, string, uint64, uint64, bool) {
			root := t.TempDir()
			if err := os.Symlink("target", filepath.Join(root, "path")); err != nil {
				t.Fatal(err)
			}
			return root, "path", 0, 0, false
		}, want: "without following symlinks"},
		{name: "symlink parent", prepare: func(t *testing.T) (string, string, uint64, uint64, bool) {
			root := t.TempDir()
			if err := os.Symlink(t.TempDir(), filepath.Join(root, "data")); err != nil {
				t.Fatal(err)
			}
			return root, "data/file", 0, 0, false
		}, want: "open service identity parent"},
		{name: "inode changed", prepare: func(t *testing.T) (string, string, uint64, uint64, bool) {
			root := t.TempDir()
			path := filepath.Join(root, "file")
			if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			meta, err := serviceIdentityMetadata(info)
			if err != nil {
				t.Fatal(err)
			}
			return root, "file", meta.Dev, meta.Ino + 1, false
		}, want: "inode changed"},
		{name: "device changed", prepare: func(t *testing.T) (string, string, uint64, uint64, bool) {
			root := t.TempDir()
			path := filepath.Join(root, "file")
			if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			meta, err := serviceIdentityMetadata(info)
			if err != nil {
				t.Fatal(err)
			}
			return root, "file", meta.Dev + 1, meta.Ino, false
		}, want: "inode device changed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, rel, dev, ino, symlink := tt.prepare(t)
			expected := serviceIdentityInodeRecord{Path: rel, UID: uid, GID: gid, Mode: 0o600, Dev: dev, Ino: ino, Nlink: 1}
			if symlink {
				expected.Mode = os.ModeSymlink | 0o777
			}
			err := mutateServiceIdentityPath(root, rel, expected, uid, gid, 0o640, true)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("mutateServiceIdentityPath error = %v, want %q", err, tt.want)
			}
		})
	}
}

func FuzzDecodeServiceIdentityJournalLine(f *testing.F) {
	for _, seed := range []string{
		`{"version":1,"id":"tx","service":"api","root":"/srv/api","targetIdentity":{"requestedUser":"app","requestedGroup":"app","uid":1000,"gid":1000}}`,
		`{"path":"data/file","uid":0,"gid":0,"mode":384,"dev":1,"ino":2}`,
		`{"phase":"inventory-sealed","zfsSnapshot":"tank/api@yeet-snapshot"}`,
		`{"path":"../escape"}`,
		`{"wat":"unknown"}`,
	} {
		f.Add([]byte(seed), uint8(1))
	}
	f.Fuzz(func(t *testing.T, raw []byte, line uint8) {
		if len(raw) > serviceIdentityJournalMaxLine {
			t.Skip()
		}
		var contents serviceIdentityJournalContents
		_ = decodeServiceIdentityJournalLine(raw, int(line%4)+1, &contents)
	})
}

func FuzzValidateServiceIdentityJournalRecordPath(f *testing.F) {
	for _, seed := range []string{".", "data/file", "../escape", "/absolute", "data/../file", ""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, path string) {
		err := validateServiceIdentityJournalRecordPath(path)
		if err != nil {
			return
		}
		if path == "." {
			return
		}
		clean := filepath.Clean(path)
		if clean != path || filepath.IsAbs(path) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			t.Fatalf("unsafe path %q passed validation", path)
		}
	})
}

func writeTestServiceIdentityJournal(t *testing.T, sealed bool) (string, string) {
	t.Helper()
	stateRoot := t.TempDir()
	root := filepath.Join(t.TempDir(), "api")
	if err := os.MkdirAll(filepath.Join(root, "data"), 0o750); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "data", "file")
	link := filepath.Join(root, "data", "link")
	if err := os.WriteFile(file, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file", link); err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Lstat(file)
	if err != nil {
		t.Fatal(err)
	}
	fileMeta, err := serviceIdentityMetadata(fileInfo)
	if err != nil {
		t.Fatal(err)
	}
	linkInfo, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	linkMeta, err := serviceIdentityMetadata(linkInfo)
	if err != nil {
		t.Fatal(err)
	}
	j, err := createServiceIdentityJournal(stateRoot, serviceIdentityJournalHeader{
		Version: 1, ID: "tx", Service: "api", Root: root,
		TargetIdentity: db.ServiceIdentity{UID: fileMeta.UID, GID: fileMeta.GID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := j.AppendInode(serviceIdentityInodeRecord{Path: filepath.Join("data", "file"), UID: fileMeta.UID + 1, GID: fileMeta.GID, Mode: fileInfo.Mode(), Dev: fileMeta.Dev, Ino: fileMeta.Ino, Nlink: fileMeta.Nlink}); err != nil {
		t.Fatal(err)
	}
	if err := j.AppendInode(serviceIdentityInodeRecord{Path: filepath.Join("data", "link"), UID: linkMeta.UID + 1, GID: linkMeta.GID, Mode: linkInfo.Mode(), Dev: linkMeta.Dev, Ino: linkMeta.Ino, Nlink: linkMeta.Nlink}); err != nil {
		t.Fatal(err)
	}
	if sealed {
		if err := j.Seal(); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	return root, j.Path()
}
