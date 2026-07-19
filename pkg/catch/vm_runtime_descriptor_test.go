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
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
	"golang.org/x/sys/unix"
)

func TestVMRuntimeDescriptorAtomicWriteAndRead(t *testing.T) {
	root := vmRuntimeDescriptorTestDir(t)
	path := filepath.Join(root, "vmm-runtime.json")
	descriptor := validVMRuntimeDescriptor()
	deps := defaultVMRuntimeDescriptorFileDeps()
	deps.uid = uint32(os.Geteuid())
	deps.gid = uint32(os.Getegid())
	if err := writeVMRuntimeDescriptorWithDeps(path, descriptor, deps); err != nil {
		t.Fatalf("writeVMRuntimeDescriptorWithDeps: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("descriptor mode = %v, want regular 0600", info.Mode())
	}
	got, err := readVMRuntimeDescriptorWithOwner(path, descriptor.Service, deps.uid, deps.gid)
	if err != nil {
		t.Fatalf("readVMRuntimeDescriptorWithOwner: %v", err)
	}
	if !reflect.DeepEqual(got, descriptor) {
		t.Fatalf("descriptor = %#v, want %#v", got, descriptor)
	}
	descriptor.Configured.ID = "firecracker-v1.16.2-yeet-v1"
	if err := writeVMRuntimeDescriptorWithDeps(path, descriptor, deps); err != nil {
		t.Fatalf("replace descriptor: %v", err)
	}
	got, err = readVMRuntimeDescriptorWithOwner(path, descriptor.Service, deps.uid, deps.gid)
	if err != nil {
		t.Fatalf("read replaced descriptor: %v", err)
	}
	if !reflect.DeepEqual(got, descriptor) {
		t.Fatalf("replaced descriptor = %#v, want %#v", got, descriptor)
	}
}

func TestVMRuntimeDescriptorDoesNotOverwriteConcurrentCreation(t *testing.T) {
	root := vmRuntimeDescriptorTestDir(t)
	path := filepath.Join(root, "vmm-runtime.json")
	deps := defaultVMRuntimeDescriptorFileDeps()
	deps.uid = uint32(os.Geteuid())
	deps.gid = uint32(os.Getegid())
	concurrent := validVMRuntimeDescriptor()
	concurrent.Configured.ID = "concurrent"
	deps.beforePublish = func(*os.File, string) error {
		return writeVMRuntimeDescriptorTestFile(path, concurrent)
	}

	written := validVMRuntimeDescriptor()
	written.Configured.ID = "writer"
	if err := writeVMRuntimeDescriptorWithDeps(path, written, deps); err == nil || !strings.Contains(err.Error(), "concurrent") {
		t.Fatalf("write error = %v, want concurrent creation refusal", err)
	}
	got, err := readVMRuntimeDescriptorWithOwner(path, concurrent.Service, deps.uid, deps.gid)
	if err != nil {
		t.Fatalf("read concurrent descriptor: %v", err)
	}
	if !reflect.DeepEqual(got, concurrent) {
		t.Fatalf("descriptor = %#v, want concurrent %#v", got, concurrent)
	}
}

func TestVMRuntimeDescriptorPreservesConcurrentReplacementForRecovery(t *testing.T) {
	root := vmRuntimeDescriptorTestDir(t)
	path := filepath.Join(root, "vmm-runtime.json")
	deps := defaultVMRuntimeDescriptorFileDeps()
	deps.uid = uint32(os.Geteuid())
	deps.gid = uint32(os.Getegid())
	if err := writeVMRuntimeDescriptorWithDeps(path, validVMRuntimeDescriptor(), deps); err != nil {
		t.Fatal(err)
	}
	concurrent := validVMRuntimeDescriptor()
	concurrent.Configured.ID = "concurrent"
	deps.beforePublish = func(*os.File, string) error {
		temp := filepath.Join(root, "concurrent.json")
		if err := writeVMRuntimeDescriptorTestFile(temp, concurrent); err != nil {
			return err
		}
		return os.Rename(temp, path)
	}

	written := validVMRuntimeDescriptor()
	written.Configured.ID = "writer"
	writeErr := writeVMRuntimeDescriptorWithDeps(path, written, deps)
	if writeErr == nil || !strings.Contains(writeErr.Error(), "concurrent") || !strings.Contains(writeErr.Error(), "recovery") {
		t.Fatalf("write error = %v, want concurrent replacement refusal", writeErr)
	}
	got, err := readVMRuntimeDescriptorWithOwner(path, written.Service, deps.uid, deps.gid)
	if err != nil {
		t.Fatalf("read published descriptor: %v", err)
	}
	if !reflect.DeepEqual(got, written) {
		t.Fatalf("descriptor = %#v, want published %#v", got, written)
	}
	recovery := vmRuntimeDescriptorRecoveryFiles(t, root)
	if len(recovery) != 1 {
		t.Fatalf("recovery files = %v, want one", recovery)
	}
	if !strings.Contains(writeErr.Error(), recovery[0]) {
		t.Fatalf("write error = %v, want retained recovery path %s", writeErr, recovery[0])
	}
	if got := readVMRuntimeDescriptorTestFile(t, recovery[0]); !reflect.DeepEqual(got, concurrent) {
		t.Fatalf("recovery descriptor = %#v, want concurrent %#v", got, concurrent)
	}
}

func TestVMRuntimeDescriptorRetainsPublishedReplacementAndRecoveryOnSyncFailure(t *testing.T) {
	root := vmRuntimeDescriptorTestDir(t)
	path := filepath.Join(root, "vmm-runtime.json")
	deps := defaultVMRuntimeDescriptorFileDeps()
	deps.uid = uint32(os.Geteuid())
	deps.gid = uint32(os.Getegid())
	original := validVMRuntimeDescriptor()
	original.Configured.ID = "original"
	if err := writeVMRuntimeDescriptorWithDeps(path, original, deps); err != nil {
		t.Fatal(err)
	}

	realExchange := deps.exchangeAt
	exchangeCalls := 0
	deps.exchangeAt = func(oldDirFD int, oldName string, newDirFD int, newName string) error {
		exchangeCalls++
		if exchangeCalls > 1 {
			return errors.New("forbidden second exchange")
		}
		return realExchange(oldDirFD, oldName, newDirFD, newName)
	}
	syncErr := errors.New("injected publish sync failure")
	deps.syncDir = func(*os.File) error { return syncErr }

	written := validVMRuntimeDescriptor()
	written.Configured.ID = "writer"
	writeErr := writeVMRuntimeDescriptorWithDeps(path, written, deps)
	if writeErr == nil || !strings.Contains(writeErr.Error(), "injected publish sync failure") || !strings.Contains(writeErr.Error(), "canonical state retained") || !strings.Contains(writeErr.Error(), "recovery") {
		t.Fatalf("write error = %v, want explicit retained publication and recovery state", writeErr)
	}
	if exchangeCalls != 1 {
		t.Fatalf("exchange calls = %d, want exactly the publish exchange", exchangeCalls)
	}
	var retainedErr *vmRuntimeDescriptorPublicationRetainedError
	if !errors.As(writeErr, &retainedErr) {
		t.Fatalf("write error = %v, want typed retained-publication error", writeErr)
	}
	if !errors.Is(writeErr, syncErr) {
		t.Fatalf("write error = %v, want wrapped sync error", writeErr)
	}
	if retainedErr.CanonicalPath() != path {
		t.Fatalf("canonical path = %q, want %q", retainedErr.CanonicalPath(), path)
	}
	got, err := readVMRuntimeDescriptorWithOwner(path, written.Service, deps.uid, deps.gid)
	if err != nil {
		t.Fatalf("read retained publication: %v", err)
	}
	if !reflect.DeepEqual(got, written) {
		t.Fatalf("descriptor = %#v, want retained publication %#v", got, written)
	}
	recovery := vmRuntimeDescriptorRecoveryFiles(t, root)
	if len(recovery) != 1 {
		t.Fatalf("recovery files = %v, want one", recovery)
	}
	if !strings.Contains(writeErr.Error(), recovery[0]) {
		t.Fatalf("write error = %v, want retained recovery path %s", writeErr, recovery[0])
	}
	if retainedErr.RecoveryPath() != recovery[0] {
		t.Fatalf("recovery path = %q, want %q", retainedErr.RecoveryPath(), recovery[0])
	}
	if got := readVMRuntimeDescriptorTestFile(t, recovery[0]); !reflect.DeepEqual(got, original) {
		t.Fatalf("recovery descriptor = %#v, want original %#v", got, original)
	}
}

func TestVMRuntimeDescriptorRetainsPublishedCreationOnSyncFailure(t *testing.T) {
	root := vmRuntimeDescriptorTestDir(t)
	path := filepath.Join(root, "vmm-runtime.json")
	deps := defaultVMRuntimeDescriptorFileDeps()
	deps.uid = uint32(os.Geteuid())
	deps.gid = uint32(os.Getegid())
	syncErr := errors.New("injected publish sync failure")
	deps.syncDir = func(*os.File) error { return syncErr }

	written := validVMRuntimeDescriptor()
	written.Configured.ID = "writer"
	err := writeVMRuntimeDescriptorWithDeps(path, written, deps)
	if err == nil || !strings.Contains(err.Error(), "injected publish sync failure") || !strings.Contains(err.Error(), "canonical state retained") {
		t.Fatalf("write error = %v, want explicit retained publication state", err)
	}
	var retainedErr *vmRuntimeDescriptorPublicationRetainedError
	if !errors.As(err, &retainedErr) {
		t.Fatalf("write error = %v, want typed retained-publication error", err)
	}
	if !errors.Is(err, syncErr) {
		t.Fatalf("write error = %v, want wrapped sync error", err)
	}
	if retainedErr.CanonicalPath() != path || retainedErr.RecoveryPath() != "" {
		t.Fatalf("retained paths = canonical %q recovery %q, want canonical %q without recovery", retainedErr.CanonicalPath(), retainedErr.RecoveryPath(), path)
	}
	got, readErr := readVMRuntimeDescriptorWithOwner(path, written.Service, deps.uid, deps.gid)
	if readErr != nil {
		t.Fatalf("read retained publication: %v", readErr)
	}
	if !reflect.DeepEqual(got, written) {
		t.Fatalf("descriptor = %#v, want retained publication %#v", got, written)
	}
	if recovery := vmRuntimeDescriptorRecoveryFiles(t, root); len(recovery) != 0 {
		t.Fatalf("recovery files = %v, want none for creation", recovery)
	}
}

func TestVMRuntimeDescriptorDoesNotOverwriteConcurrentPostPublishReplacement(t *testing.T) {
	root := vmRuntimeDescriptorTestDir(t)
	path := filepath.Join(root, "vmm-runtime.json")
	deps := defaultVMRuntimeDescriptorFileDeps()
	deps.uid = uint32(os.Geteuid())
	deps.gid = uint32(os.Getegid())
	if err := writeVMRuntimeDescriptorWithDeps(path, validVMRuntimeDescriptor(), deps); err != nil {
		t.Fatal(err)
	}
	concurrent := validVMRuntimeDescriptor()
	concurrent.Configured.ID = "concurrent-after-publish"
	deps.afterPublish = func(*os.File, string) {
		temp := filepath.Join(root, "concurrent-after.json")
		if err := writeVMRuntimeDescriptorTestFile(temp, concurrent); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(temp, path); err != nil {
			t.Fatal(err)
		}
	}

	written := validVMRuntimeDescriptor()
	written.Configured.ID = "writer"
	writeErr := writeVMRuntimeDescriptorWithDeps(path, written, deps)
	if writeErr == nil || !strings.Contains(writeErr.Error(), "changed") || !strings.Contains(writeErr.Error(), "recovery") {
		t.Fatalf("write error = %v, want post-publish replacement refusal", writeErr)
	}
	got, err := readVMRuntimeDescriptorWithOwner(path, concurrent.Service, deps.uid, deps.gid)
	if err != nil {
		t.Fatalf("read concurrent descriptor: %v", err)
	}
	if !reflect.DeepEqual(got, concurrent) {
		t.Fatalf("descriptor = %#v, want concurrent %#v", got, concurrent)
	}
	recovery := vmRuntimeDescriptorRecoveryFiles(t, root)
	if len(recovery) != 1 {
		t.Fatalf("recovery files = %v, want one", recovery)
	}
	if !strings.Contains(writeErr.Error(), recovery[0]) {
		t.Fatalf("write error = %v, want retained recovery path %s", writeErr, recovery[0])
	}
	if got := readVMRuntimeDescriptorTestFile(t, recovery[0]); got.Configured.ID != validVMRuntimeDescriptor().Configured.ID {
		t.Fatalf("recovery descriptor = %#v, want original descriptor", got)
	}
}

func TestVMRuntimeDescriptorRawTransactionPublishesAndRestoresExactAbsentState(t *testing.T) {
	root := filepath.Join(vmRuntimeDescriptorTestDir(t), "custom", "service", "data")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, vmRuntimeDescriptorFileName)
	oldFile := vmRuntimeDescriptorRawTestState(t, path, false, nil)
	newRaw := []byte("exact journal bytes; no rendering\n")
	newFile := vmRuntimeDescriptorRawTestState(t, path, true, newRaw)
	deps := vmRuntimeDescriptorRawTestDeps()
	tx, err := prepareVMRuntimeDescriptorRawTransaction(context.Background(), oldFile, newFile, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Close() })
	if got, err := tx.Classify(context.Background()); err != nil || got != vmRuntimeDescriptorRawOld {
		t.Fatalf("initial classification = %q, %v", got, err)
	}
	if err := tx.PublishAndVerify(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := readVMRuntimeDescriptorRawTestFile(t, path); !reflect.DeepEqual(got, newRaw) {
		t.Fatalf("published bytes = %q, want exact %q", got, newRaw)
	}
	if err := tx.PublishAndVerify(context.Background()); err != nil {
		t.Fatalf("already-applied retry: %v", err)
	}
	if err := tx.RestorePreviousAndVerify(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("descriptor exists after exact absent restoration: %v", err)
	}
	if err := tx.RestorePreviousAndVerify(context.Background()); err != nil {
		t.Fatalf("idempotent absent restoration: %v", err)
	}
}

func TestVMRuntimeDescriptorRawTransactionRestoresExactExistingBytesAndMetadata(t *testing.T) {
	root := vmRuntimeDescriptorTestDir(t)
	path := filepath.Join(root, vmRuntimeDescriptorFileName)
	oldRaw := []byte("historical descriptor bytes\n")
	if err := os.WriteFile(path, oldRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	var wantStat unix.Stat_t
	if err := unix.Lstat(path, &wantStat); err != nil {
		t.Fatal(err)
	}
	oldFile := vmRuntimeDescriptorRawTestStateFromDisk(t, path)
	newFile := vmRuntimeDescriptorRawTestState(t, path, true, []byte("new journal descriptor bytes\n"))
	tx, err := prepareVMRuntimeDescriptorRawTransaction(context.Background(), oldFile, newFile, vmRuntimeDescriptorRawTestDeps())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Close() })
	if err := tx.PublishAndVerify(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := tx.RestorePreviousAndVerify(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := readVMRuntimeDescriptorRawTestFile(t, path); !reflect.DeepEqual(got, oldRaw) {
		t.Fatalf("restored bytes = %q, want %q", got, oldRaw)
	}
	var gotStat unix.Stat_t
	if err := unix.Lstat(path, &gotStat); err != nil {
		t.Fatal(err)
	}
	if uint32(gotStat.Mode) != uint32(wantStat.Mode) || gotStat.Uid != wantStat.Uid || gotStat.Gid != wantStat.Gid {
		t.Fatalf("restored metadata = %o %d:%d, want %o %d:%d", uint32(gotStat.Mode), gotStat.Uid, gotStat.Gid, uint32(wantStat.Mode), wantStat.Uid, wantStat.Gid)
	}
}

func TestVMRuntimeDescriptorRawTransactionFreshRecoveryRecreatesOldDescriptor(t *testing.T) {
	root := vmRuntimeDescriptorTestDir(t)
	path := filepath.Join(root, vmRuntimeDescriptorFileName)
	oldRaw := []byte("exact old journal bytes\n")
	newRaw := []byte("exact new journal bytes\n")
	oldFile := vmRuntimeDescriptorRawTestState(t, path, true, oldRaw)
	newFile := vmRuntimeDescriptorRawTestState(t, path, true, newRaw)
	if err := os.WriteFile(path, newRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	tx, err := prepareVMRuntimeDescriptorRawTransaction(context.Background(), oldFile, newFile, vmRuntimeDescriptorRawTestDeps())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Close() })
	if err := tx.RestorePreviousAndVerify(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := readVMRuntimeDescriptorRawTestFile(t, path); !reflect.DeepEqual(got, oldRaw) {
		t.Fatalf("fresh recovery bytes = %q, want %q", got, oldRaw)
	}
}

func TestVMRuntimeDescriptorRawTransactionAlreadyAppliedRetryPreservesIdentityAndSyncs(t *testing.T) {
	root := vmRuntimeDescriptorTestDir(t)
	path := filepath.Join(root, vmRuntimeDescriptorFileName)
	oldFile := vmRuntimeDescriptorRawTestState(t, path, false, nil)
	newFile := vmRuntimeDescriptorRawTestState(t, path, true, []byte("new\n"))
	if err := os.WriteFile(path, newFile.Contents, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	fileSyncs, dirSyncs := 0, 0
	deps := vmRuntimeDescriptorRawTestDeps()
	deps.syncFile = func(file *os.File) error {
		fileSyncs++
		return file.Sync()
	}
	deps.syncDir = func(dir *os.File) error {
		dirSyncs++
		return dir.Sync()
	}
	tx, err := prepareVMRuntimeDescriptorRawTransaction(context.Background(), oldFile, newFile, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Close() })
	if err := tx.PublishAndVerify(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || fileSyncs == 0 || dirSyncs == 0 {
		t.Fatalf("retry identity/sync = same %v file %d dir %d", os.SameFile(before, after), fileSyncs, dirSyncs)
	}
}

func TestVMRuntimeDescriptorRawTransactionDistinguishesSyncFailures(t *testing.T) {
	t.Run("before visibility", func(t *testing.T) {
		root := vmRuntimeDescriptorTestDir(t)
		path := filepath.Join(root, vmRuntimeDescriptorFileName)
		oldFile := vmRuntimeDescriptorRawTestState(t, path, false, nil)
		newFile := vmRuntimeDescriptorRawTestState(t, path, true, []byte("new\n"))
		syncErr := errors.New("staged file sync failed")
		deps := vmRuntimeDescriptorRawTestDeps()
		deps.syncFile = func(*os.File) error { return syncErr }
		tx, err := prepareVMRuntimeDescriptorRawTransaction(context.Background(), oldFile, newFile, deps)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tx.Close() })
		err = tx.PublishAndVerify(context.Background())
		var post *vmRuntimeDescriptorRawPostPublicationError
		var uncertain *vmRuntimeDescriptorRawUncertainError
		if !errors.Is(err, syncErr) || errors.As(err, &post) || errors.As(err, &uncertain) {
			t.Fatalf("pre-publication error = %v", err)
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("canonical descriptor visible after pre-publication failure: %v", err)
		}
	})

	t.Run("after visibility", func(t *testing.T) {
		root := vmRuntimeDescriptorTestDir(t)
		path := filepath.Join(root, vmRuntimeDescriptorFileName)
		oldFile := vmRuntimeDescriptorRawTestState(t, path, false, nil)
		newFile := vmRuntimeDescriptorRawTestState(t, path, true, []byte("new\n"))
		syncErr := errors.New("directory sync failed")
		fail := true
		deps := vmRuntimeDescriptorRawTestDeps()
		deps.syncDir = func(dir *os.File) error {
			if fail {
				return syncErr
			}
			return dir.Sync()
		}
		tx, err := prepareVMRuntimeDescriptorRawTransaction(context.Background(), oldFile, newFile, deps)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tx.Close() })
		err = tx.PublishAndVerify(context.Background())
		var post *vmRuntimeDescriptorRawPostPublicationError
		if !errors.As(err, &post) || !errors.Is(err, syncErr) || post.Outcome().Classification != vmRuntimeDescriptorRawNew {
			t.Fatalf("post-publication error = %v", err)
		}
		if got := readVMRuntimeDescriptorRawTestFile(t, path); !reflect.DeepEqual(got, newFile.Contents) {
			t.Fatalf("visible bytes = %q", got)
		}
		fail = false
		if err := tx.PublishAndVerify(context.Background()); err != nil {
			t.Fatalf("resync retry: %v", err)
		}
	})
}

func TestVMRuntimeDescriptorRawTransactionClassifiesErrorAfterRenameVisible(t *testing.T) {
	root := vmRuntimeDescriptorTestDir(t)
	path := filepath.Join(root, vmRuntimeDescriptorFileName)
	oldFile := vmRuntimeDescriptorRawTestState(t, path, false, nil)
	newFile := vmRuntimeDescriptorRawTestState(t, path, true, []byte("new\n"))
	renameErr := errors.New("rename reported failure")
	deps := vmRuntimeDescriptorRawTestDeps()
	realRename := deps.renameNoReplaceAt
	deps.renameNoReplaceAt = func(oldDir int, oldName string, newDir int, newName string) error {
		if err := realRename(oldDir, oldName, newDir, newName); err != nil {
			return err
		}
		return renameErr
	}
	tx, err := prepareVMRuntimeDescriptorRawTransaction(context.Background(), oldFile, newFile, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Close() })
	err = tx.PublishAndVerify(context.Background())
	var post *vmRuntimeDescriptorRawPostPublicationError
	if !errors.As(err, &post) || !errors.Is(err, renameErr) || post.Outcome().Classification != vmRuntimeDescriptorRawNew {
		t.Fatalf("rename outcome error = %v", err)
	}
}

func TestVMRuntimeDescriptorRawTransactionClassifiesErrorAfterExchangeVisible(t *testing.T) {
	root := vmRuntimeDescriptorTestDir(t)
	path := filepath.Join(root, vmRuntimeDescriptorFileName)
	oldRaw := []byte("old\n")
	newRaw := []byte("new\n")
	if err := os.WriteFile(path, oldRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	oldFile := vmRuntimeDescriptorRawTestStateFromDisk(t, path)
	newFile := vmRuntimeDescriptorRawTestState(t, path, true, newRaw)
	exchangeErr := errors.New("exchange reported failure")
	deps := vmRuntimeDescriptorRawTestDeps()
	realExchange := deps.exchangeAt
	exchanges := 0
	deps.exchangeAt = func(oldDir int, oldName string, newDir int, newName string) error {
		exchanges++
		if err := realExchange(oldDir, oldName, newDir, newName); err != nil {
			return err
		}
		if exchanges == 1 {
			return exchangeErr
		}
		return nil
	}
	tx, err := prepareVMRuntimeDescriptorRawTransaction(context.Background(), oldFile, newFile, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Close() })
	err = tx.PublishAndVerify(context.Background())
	var post *vmRuntimeDescriptorRawPostPublicationError
	if !errors.As(err, &post) || !errors.Is(err, exchangeErr) || post.Outcome().Classification != vmRuntimeDescriptorRawNew || len(post.Outcome().RecoveryPaths) != 1 {
		t.Fatalf("exchange outcome error = %v", err)
	}
	if got := readVMRuntimeDescriptorRawTestFile(t, path); !reflect.DeepEqual(got, newRaw) {
		t.Fatalf("visible bytes = %q, want %q", got, newRaw)
	}
	if err := tx.RestorePreviousAndVerify(context.Background()); err != nil {
		t.Fatalf("restore retained old descriptor: %v", err)
	}
	if got := readVMRuntimeDescriptorRawTestFile(t, path); !reflect.DeepEqual(got, oldRaw) {
		t.Fatalf("restored bytes = %q, want %q", got, oldRaw)
	}
}

func TestVMRuntimeDescriptorRawTransactionCancellationAfterExchangeRetainsCorrectOldIdentity(t *testing.T) {
	root := vmRuntimeDescriptorTestDir(t)
	path := filepath.Join(root, vmRuntimeDescriptorFileName)
	oldRaw := []byte("old\n")
	newRaw := []byte("new\n")
	if err := os.WriteFile(path, oldRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	oldFile := vmRuntimeDescriptorRawTestStateFromDisk(t, path)
	newFile := vmRuntimeDescriptorRawTestState(t, path, true, newRaw)
	ctx, cancel := context.WithCancel(context.Background())
	deps := vmRuntimeDescriptorRawTestDeps()
	realExchange := deps.exchangeAt
	deps.exchangeAt = func(oldDir int, oldName string, newDir int, newName string) error {
		if err := realExchange(oldDir, oldName, newDir, newName); err != nil {
			return err
		}
		cancel()
		return nil
	}
	tx, err := prepareVMRuntimeDescriptorRawTransaction(ctx, oldFile, newFile, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Close() })
	err = tx.PublishAndVerify(ctx)
	var post *vmRuntimeDescriptorRawPostPublicationError
	if !errors.As(err, &post) || !errors.Is(err, context.Canceled) || post.Outcome().Classification != vmRuntimeDescriptorRawNew || len(post.Outcome().RecoveryPaths) != 1 {
		t.Fatalf("publish error = %v, want canceled exact-new outcome with retained old path", err)
	}
	if err := tx.RestorePreviousAndVerify(context.Background()); err != nil {
		t.Fatalf("restore after cancellation: %v", err)
	}
	if got := readVMRuntimeDescriptorRawTestFile(t, path); !reflect.DeepEqual(got, oldRaw) {
		t.Fatalf("restored bytes = %q, want %q", got, oldRaw)
	}
}

func TestVMRuntimeDescriptorRawTransactionHonorsCancellationBeforePublication(t *testing.T) {
	root := vmRuntimeDescriptorTestDir(t)
	path := filepath.Join(root, vmRuntimeDescriptorFileName)
	oldFile := vmRuntimeDescriptorRawTestState(t, path, false, nil)
	newFile := vmRuntimeDescriptorRawTestState(t, path, true, []byte("new\n"))
	ctx, cancel := context.WithCancel(context.Background())
	deps := vmRuntimeDescriptorRawTestDeps()
	realSync := deps.syncFile
	deps.syncFile = func(file *os.File) error {
		err := realSync(file)
		cancel()
		return err
	}
	tx, err := prepareVMRuntimeDescriptorRawTransaction(ctx, oldFile, newFile, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Close() })
	if err := tx.PublishAndVerify(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("publish error = %v, want cancellation", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("descriptor published after cancellation: %v", err)
	}
}

func TestVMRuntimeDescriptorRawTransactionHonorsCancellationWhileWaitingForLock(t *testing.T) {
	root := vmRuntimeDescriptorTestDir(t)
	path := filepath.Join(root, vmRuntimeDescriptorFileName)
	oldFile := vmRuntimeDescriptorRawTestState(t, path, false, nil)
	newFile := vmRuntimeDescriptorRawTestState(t, path, true, []byte("new\n"))
	first, err := prepareVMRuntimeDescriptorRawTransaction(context.Background(), oldFile, newFile, vmRuntimeDescriptorRawTestDeps())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	second, err := prepareVMRuntimeDescriptorRawTransaction(ctx, oldFile, newFile, vmRuntimeDescriptorRawTestDeps())
	if second != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second transaction = %#v, %v, want canceled lock wait", second, err)
	}
}

func TestVMRuntimeDescriptorRawTransactionNeverFollowsCanonicalSymlink(t *testing.T) {
	root := vmRuntimeDescriptorTestDir(t)
	path := filepath.Join(root, vmRuntimeDescriptorFileName)
	target := filepath.Join(root, "target")
	oldRaw := []byte("old\n")
	if err := os.WriteFile(target, oldRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	oldFile := vmRuntimeDescriptorRawTestState(t, path, true, oldRaw)
	newFile := vmRuntimeDescriptorRawTestState(t, path, true, []byte("new\n"))
	tx, err := prepareVMRuntimeDescriptorRawTransaction(context.Background(), oldFile, newFile, vmRuntimeDescriptorRawTestDeps())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Close() })
	err = tx.PublishAndVerify(context.Background())
	var uncertain *vmRuntimeDescriptorRawUncertainError
	if !errors.As(err, &uncertain) || !strings.Contains(strings.ToLower(err.Error()), "symlink") {
		t.Fatalf("publish error = %v, want typed no-follow uncertainty", err)
	}
	if got := readVMRuntimeDescriptorRawTestFile(t, target); !reflect.DeepEqual(got, oldRaw) {
		t.Fatalf("symlink target changed: %q", got)
	}
}

func TestVMRuntimeDescriptorRawTransactionRestoreRefusesConcurrentReplacement(t *testing.T) {
	root := vmRuntimeDescriptorTestDir(t)
	path := filepath.Join(root, vmRuntimeDescriptorFileName)
	oldFile := vmRuntimeDescriptorRawTestState(t, path, false, nil)
	newFile := vmRuntimeDescriptorRawTestState(t, path, true, []byte("new\n"))
	tx, err := prepareVMRuntimeDescriptorRawTransaction(context.Background(), oldFile, newFile, vmRuntimeDescriptorRawTestDeps())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Close() })
	if err := tx.PublishAndVerify(context.Background()); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(root, "replacement")
	if err := os.WriteFile(replacement, []byte("concurrent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	err = tx.RestorePreviousAndVerify(context.Background())
	var uncertain *vmRuntimeDescriptorRawUncertainError
	if !errors.As(err, &uncertain) {
		t.Fatalf("restore error = %v, want typed uncertainty", err)
	}
	if got := readVMRuntimeDescriptorRawTestFile(t, path); string(got) != "concurrent\n" {
		t.Fatalf("concurrent descriptor overwritten: %q", got)
	}
}

func TestVMRuntimeDescriptorRawTransactionAbsentRestoreDetectsReplacementAfterQuarantine(t *testing.T) {
	root := vmRuntimeDescriptorTestDir(t)
	path := filepath.Join(root, vmRuntimeDescriptorFileName)
	oldFile := vmRuntimeDescriptorRawTestState(t, path, false, nil)
	newFile := vmRuntimeDescriptorRawTestState(t, path, true, []byte("new\n"))
	deps := vmRuntimeDescriptorRawTestDeps()
	realRename := deps.renameNoReplaceAt
	renames := 0
	deps.renameNoReplaceAt = func(oldDir int, oldName string, newDir int, newName string) error {
		renames++
		if err := realRename(oldDir, oldName, newDir, newName); err != nil {
			return err
		}
		if renames == 2 {
			return os.WriteFile(path, []byte("concurrent-after-quarantine\n"), 0o600)
		}
		return nil
	}
	tx, err := prepareVMRuntimeDescriptorRawTransaction(context.Background(), oldFile, newFile, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Close() })
	if err := tx.PublishAndVerify(context.Background()); err != nil {
		t.Fatal(err)
	}
	err = tx.RestorePreviousAndVerify(context.Background())
	var uncertain *vmRuntimeDescriptorRawUncertainError
	var post *vmRuntimeDescriptorRawPostPublicationError
	if !errors.As(err, &uncertain) || errors.As(err, &post) {
		t.Fatalf("restore error = %v, want conservative typed uncertainty", err)
	}
	if got := string(readVMRuntimeDescriptorRawTestFile(t, path)); got != "concurrent-after-quarantine\n" {
		t.Fatalf("concurrent descriptor overwritten: %q", got)
	}
}

func TestVMRuntimeDescriptorRawTransactionAbsentRestoreSyncFailureIsRetryable(t *testing.T) {
	root := vmRuntimeDescriptorTestDir(t)
	path := filepath.Join(root, vmRuntimeDescriptorFileName)
	oldFile := vmRuntimeDescriptorRawTestState(t, path, false, nil)
	newFile := vmRuntimeDescriptorRawTestState(t, path, true, []byte("new\n"))
	fail := false
	syncErr := errors.New("restore directory sync failed")
	deps := vmRuntimeDescriptorRawTestDeps()
	deps.syncDir = func(dir *os.File) error {
		if fail {
			return syncErr
		}
		return dir.Sync()
	}
	tx, err := prepareVMRuntimeDescriptorRawTransaction(context.Background(), oldFile, newFile, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Close() })
	if err := tx.PublishAndVerify(context.Background()); err != nil {
		t.Fatal(err)
	}
	fail = true
	err = tx.RestorePreviousAndVerify(context.Background())
	var post *vmRuntimeDescriptorRawPostPublicationError
	if !errors.As(err, &post) || !errors.Is(err, syncErr) || post.Outcome().Classification != vmRuntimeDescriptorRawOld {
		t.Fatalf("restore error = %v, want typed exact-old post-publication result", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("descriptor still canonical after absent restore: %v", err)
	}
	fail = false
	if err := tx.RestorePreviousAndVerify(context.Background()); err != nil {
		t.Fatalf("restore retry: %v", err)
	}
}

func TestVMRuntimeDescriptorRejectsInvalidContract(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{name: "unknown field", mutate: func(object map[string]any) { object["extra"] = true }, want: "unknown field"},
		{name: "missing trial", mutate: func(object map[string]any) { delete(object, "trial") }, want: "missing required field"},
		{name: "unsupported schema", mutate: func(object map[string]any) { object["schemaVersion"] = 2 }, want: "schemaVersion"},
		{name: "service mismatch", mutate: func(object map[string]any) { object["service"] = "other" }, want: "service"},
		{name: "null configured", mutate: func(object map[string]any) { object["configured"] = nil }, want: "configured"},
		{name: "null trial", mutate: func(object map[string]any) { object["trial"] = nil }, want: "trial"},
		{name: "wrong trial type", mutate: func(object map[string]any) { object["trial"] = "false" }, want: "trial"},
		{name: "null manifest digest", mutate: func(object map[string]any) { object["configured"].(map[string]any)["ManifestSHA256"] = nil }, want: "ManifestSHA256"},
		{name: "wrong manifest digest type", mutate: func(object map[string]any) { object["configured"].(map[string]any)["ManifestSHA256"] = true }, want: "ManifestSHA256"},
		{name: "missing official manifest digest", mutate: func(object map[string]any) { object["configured"].(map[string]any)["ManifestSHA256"] = "" }, want: "ManifestSHA256"},
		{name: "missing local manifest digest", mutate: func(object map[string]any) {
			artifact := object["configured"].(map[string]any)
			artifact["Source"] = "local:lab"
			artifact["ManifestSHA256"] = ""
		}, want: "ManifestSHA256"},
		{name: "missing custom manifest digest", mutate: func(object map[string]any) {
			artifact := object["configured"].(map[string]any)
			artifact["Source"] = "custom"
			artifact["ManifestSHA256"] = ""
		}, want: "ManifestSHA256"},
		{name: "uppercase official manifest digest", mutate: func(object map[string]any) {
			object["configured"].(map[string]any)["ManifestSHA256"] = strings.Repeat("A", 64)
		}, want: "ManifestSHA256"},
		{name: "uppercase local manifest digest", mutate: func(object map[string]any) {
			artifact := object["configured"].(map[string]any)
			artifact["Source"] = "local:lab"
			artifact["ManifestSHA256"] = strings.Repeat("A", 64)
		}, want: "ManifestSHA256"},
		{name: "bad digest", mutate: func(object map[string]any) { object["configured"].(map[string]any)["FirecrackerSHA256"] = "bad" }, want: "FirecrackerSHA256"},
		{name: "relative firecracker", mutate: func(object map[string]any) { object["configured"].(map[string]any)["Firecracker"] = "firecracker" }, want: "absolute"},
		{name: "trial without staged", mutate: func(object map[string]any) { object["trial"] = true }, want: "staged"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			object := vmRuntimeDescriptorTestObject(t)
			tt.mutate(object)
			raw, err := json.Marshal(object)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeVMRuntimeDescriptor(raw, "devbox"); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("decodeVMRuntimeDescriptor error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestVMRuntimeDescriptorAllowsManifestlessExplicitLegacySources(t *testing.T) {
	for _, source := range []string{"official-legacy", "custom-legacy", "local-legacy"} {
		t.Run(source, func(t *testing.T) {
			descriptor := validVMRuntimeDescriptor()
			descriptor.Configured.Source = source
			descriptor.Configured.ManifestSHA256 = ""
			raw, err := json.Marshal(descriptor)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeVMRuntimeDescriptor(raw, descriptor.Service); err != nil {
				t.Fatalf("decode legacy descriptor: %v", err)
			}
		})
	}
}

func TestVMRuntimeDescriptorRejectsUntrustedFilePaths(t *testing.T) {
	descriptor := validVMRuntimeDescriptor()
	raw, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
	t.Run("symlink", func(t *testing.T) {
		dir := vmRuntimeDescriptorTestDir(t)
		target := filepath.Join(dir, "target.json")
		if err := os.WriteFile(target, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "vmm-runtime.json")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := readVMRuntimeDescriptorWithOwner(link, descriptor.Service, uid, gid); err == nil || !strings.Contains(err.Error(), "without following symlinks") {
			t.Fatalf("read error = %v, want symlink refusal", err)
		}
	})
	t.Run("writable parent", func(t *testing.T) {
		dir := vmRuntimeDescriptorTestDir(t)
		if err := os.Chmod(dir, 0o770); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "vmm-runtime.json")
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readVMRuntimeDescriptorWithOwner(path, descriptor.Service, uid, gid); err == nil || !strings.Contains(err.Error(), "writable") {
			t.Fatalf("read error = %v, want writable parent refusal", err)
		}
	})
	t.Run("wrong mode", func(t *testing.T) {
		dir := vmRuntimeDescriptorTestDir(t)
		path := filepath.Join(dir, "vmm-runtime.json")
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := readVMRuntimeDescriptorWithOwner(path, descriptor.Service, uid, gid); err == nil || !strings.Contains(err.Error(), "0600") {
			t.Fatalf("read error = %v, want mode refusal", err)
		}
	})
	t.Run("ancestor symlink", func(t *testing.T) {
		root := vmRuntimeDescriptorTestDir(t)
		realDir := filepath.Join(root, "real")
		if err := os.Mkdir(realDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(realDir, "vmm-runtime.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		linkedDir := filepath.Join(root, "linked")
		if err := os.Symlink(realDir, linkedDir); err != nil {
			t.Fatal(err)
		}
		if _, err := readVMRuntimeDescriptorWithOwner(filepath.Join(linkedDir, "vmm-runtime.json"), descriptor.Service, uid, gid); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("read error = %v, want ancestor symlink refusal", err)
		}
		deps := defaultVMRuntimeDescriptorFileDeps()
		deps.uid, deps.gid = uid, gid
		if err := writeVMRuntimeDescriptorWithDeps(filepath.Join(linkedDir, "vmm-runtime.json"), descriptor, deps); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("write error = %v, want ancestor symlink refusal", err)
		}
	})
}

func TestValidateVMRuntimeLaunchPaths(t *testing.T) {
	root := "/srv/vm roots/devbox"
	descriptor := filepath.Join(root, "data", "vmm-runtime.json")
	running := filepath.Join(root, "run", "vmm-runtime-running.json")
	trial := filepath.Join(root, "run", "vmm-runtime-trial-result.json")
	if err := ValidateVMRuntimeLaunchPaths(root, descriptor, running, trial); err != nil {
		t.Fatalf("ValidateVMRuntimeLaunchPaths: %v", err)
	}
	for _, tt := range []struct {
		name                         string
		root, descriptor, run, trial string
	}{
		{name: "relative root", root: "srv/devbox", descriptor: descriptor, run: running, trial: trial},
		{name: "unclean root", root: root + "/../devbox", descriptor: descriptor, run: running, trial: trial},
		{name: "descriptor", root: root, descriptor: filepath.Join(root, "data", "other.json"), run: running, trial: trial},
		{name: "running", root: root, descriptor: descriptor, run: filepath.Join(root, "run", "other.json"), trial: trial},
		{name: "trial", root: root, descriptor: descriptor, run: running, trial: filepath.Join(root, "run", "other.json")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateVMRuntimeLaunchPaths(tt.root, tt.descriptor, tt.run, tt.trial); err == nil {
				t.Fatal("ValidateVMRuntimeLaunchPaths accepted invalid paths")
			}
		})
	}
}

func FuzzParseVMRuntimeDescriptor(f *testing.F) {
	raw, err := json.Marshal(validVMRuntimeDescriptor())
	if err != nil {
		f.Fatal(err)
	}
	f.Add(raw, "devbox")
	f.Add([]byte(`{"schemaVersion":1,"service":"devbox","configured":null,"trial":false}`), "devbox")
	f.Add([]byte(`not-json`), "devbox")
	f.Fuzz(func(t *testing.T, raw []byte, service string) {
		_, _ = decodeVMRuntimeDescriptor(raw, service)
	})
}

func validVMRuntimeDescriptor() vmRuntimeDescriptor {
	return vmRuntimeDescriptor{
		SchemaVersion: 1,
		Service:       "devbox",
		Configured: db.VMRuntimeArtifactConfig{
			ID:                "firecracker-v1.16.1-yeet-v1",
			ManifestSHA256:    strings.Repeat("a", 64),
			FirecrackerSHA256: strings.Repeat("b", 64),
			JailerSHA256:      strings.Repeat("c", 64),
			Firecracker:       "/var/lib/yeet/vm-runtimes/amd64/firecracker-v1.16.1-yeet-v1/manifest/firecracker",
			Jailer:            "/var/lib/yeet/vm-runtimes/amd64/firecracker-v1.16.1-yeet-v1/manifest/jailer",
			Source:            "official",
		},
	}
}

func vmRuntimeDescriptorTestObject(t testing.TB) map[string]any {
	t.Helper()
	raw, err := json.Marshal(validVMRuntimeDescriptor())
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	return object
}

func vmRuntimeDescriptorTestDir(t testing.TB) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeVMRuntimeDescriptorTestFile(path string, descriptor vmRuntimeDescriptor) error {
	raw, err := json.Marshal(descriptor)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o600)
}

func readVMRuntimeDescriptorTestFile(t testing.TB, path string) vmRuntimeDescriptor {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := decodeVMRuntimeDescriptor(raw, "devbox")
	if err != nil {
		t.Fatal(err)
	}
	return descriptor
}

func vmRuntimeDescriptorRecoveryFiles(t testing.TB, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "." + vmRuntimeDescriptorFileName + ".unit-"
	var paths []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			paths = append(paths, filepath.Join(root, entry.Name()))
		}
	}
	return paths
}

func vmRuntimeDescriptorRawTestDeps() vmRuntimeDescriptorFileDeps {
	deps := defaultVMRuntimeDescriptorFileDeps()
	deps.uid = uint32(os.Geteuid())
	deps.gid = uint32(os.Getegid())
	return deps
}

func vmRuntimeDescriptorRawTestState(t testing.TB, path string, exists bool, raw []byte) vmRuntimeJournalFile {
	t.Helper()
	state := vmRuntimeJournalFile{Path: path, Exists: exists}
	if !exists {
		return state
	}
	digest := sha256.Sum256(raw)
	state.Contents = append([]byte(nil), raw...)
	state.Mode = unix.S_IFREG | 0o600
	state.UID = uint32(os.Geteuid())
	state.GID = uint32(os.Getegid())
	state.SHA256 = hex.EncodeToString(digest[:])
	return state
}

func vmRuntimeDescriptorRawTestStateFromDisk(t testing.TB, path string) vmRuntimeJournalFile {
	t.Helper()
	raw := readVMRuntimeDescriptorRawTestFile(t, path)
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	return vmRuntimeJournalFile{
		Path: path, Exists: true, Contents: raw, Mode: uint32(stat.Mode), UID: stat.Uid, GID: stat.Gid,
		SHA256: hex.EncodeToString(digest[:]),
	}
}

func readVMRuntimeDescriptorRawTestFile(t testing.TB, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
