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

	"golang.org/x/sys/unix"
)

func TestVMUnitRestorationUncertainErrorMatchesPublicSentinel(t *testing.T) {
	cause := errors.New("restore prior unit")
	err := &vmUnitRestorationUncertainError{cause: cause, paths: []string{"/etc/systemd/system/yeet-alpha.service"}}

	if !errors.Is(err, ErrVMUnitRestorationUncertain) {
		t.Fatalf("errors.Is(%v, ErrVMUnitRestorationUncertain) = false, want true", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("errors.Is(%v, cause) = false, want true", err)
	}
}

func TestVMUnitTransactionPublishesAllAndReloadsOnce(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "yeet-vm-alpha.service")
	second := filepath.Join(dir, "yeet-vm-beta.service")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("old "+filepath.Base(path)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var calls [][]string
	deps := testVMUnitTransactionDeps(func(args ...string) error {
		calls = append(calls, append([]string(nil), args...))
		return nil
	})
	tx, err := prepareVMUnitTransaction(context.Background(), []vmUnitSpec{
		{Service: "alpha", Path: first, Content: []byte("new alpha")},
		{Service: "beta", Path: second, Content: []byte("new beta")},
	}, deps)
	if err != nil {
		t.Fatalf("prepareVMUnitTransaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Close() })
	if got := readVMUnitTransactionTestFile(t, first); strings.HasPrefix(got, "new") {
		t.Fatalf("first unit changed before commit: %q", got)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := readVMUnitTransactionTestFile(t, first); got != "new alpha" {
		t.Fatalf("first unit = %q", got)
	}
	if got := readVMUnitTransactionTestFile(t, second); got != "new beta" {
		t.Fatalf("second unit = %q", got)
	}
	if len(calls) != 1 || len(calls[0]) != 1 || calls[0][0] != "daemon-reload" {
		t.Fatalf("systemctl calls = %#v", calls)
	}
}

func TestVMUnitTransactionRollsBackEarlierUnitAfterRenameFailure(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "yeet-vm-alpha.service")
	second := filepath.Join(dir, "yeet-vm-beta.service")
	if err := os.WriteFile(first, []byte("old alpha"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("old beta"), 0o600); err != nil {
		t.Fatal(err)
	}
	renameErr := errors.New("rename failed")
	renameCalls := 0
	reloadCalls := 0
	deps := testVMUnitTransactionDeps(func(args ...string) error {
		reloadCalls++
		return nil
	})
	deps.renameAt = func(oldDir int, oldName string, newDir int, newName string) error {
		renameCalls++
		if renameCalls == 2 {
			return renameErr
		}
		return unix.Renameat(oldDir, oldName, newDir, newName)
	}
	tx, err := prepareVMUnitTransaction(context.Background(), []vmUnitSpec{
		{Service: "alpha", Path: first, Content: []byte("new alpha")},
		{Service: "beta", Path: second, Content: []byte("new beta")},
	}, deps)
	if err != nil {
		t.Fatalf("prepareVMUnitTransaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Close() })
	if err := tx.Commit(); !errors.Is(err, renameErr) {
		t.Fatalf("Commit error = %v, want %v", err, renameErr)
	}
	if got := readVMUnitTransactionTestFile(t, first); got != "old alpha" {
		t.Fatalf("first unit after rollback = %q", got)
	}
	if got := readVMUnitTransactionTestFile(t, second); got != "old beta" {
		t.Fatalf("second unit after rollback = %q", got)
	}
	if reloadCalls != 1 {
		t.Fatalf("daemon reload calls = %d, want 1", reloadCalls)
	}
}

func TestVMUnitTransactionRejectsReplacedStagedIdentity(t *testing.T) {
	dir := t.TempDir()
	unitPath := filepath.Join(dir, "yeet-vm-alpha.service")
	if err := os.WriteFile(unitPath, []byte("old alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	deps := testVMUnitTransactionDeps(func(...string) error { return nil })
	tx, err := prepareVMUnitTransaction(context.Background(), []vmUnitSpec{{
		Service: "alpha", Path: unitPath, Content: []byte("new alpha"),
	}}, deps)
	if err != nil {
		t.Fatalf("prepareVMUnitTransaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Close() })
	staged := tx.units[0].Staged
	if err := os.Remove(staged); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(unitPath, staged); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err == nil || !strings.Contains(err.Error(), "staged VM unit") {
		t.Fatalf("Commit error = %v, want staged identity refusal", err)
	}
	if got := readVMUnitTransactionTestFile(t, unitPath); got != "old alpha" {
		t.Fatalf("live unit = %q, want old alpha", got)
	}
}

func TestVMUnitTransactionRestorePreviousAndVerifyIsExactAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "yeet-vm-existing.service")
	absent := filepath.Join(dir, "yeet-vm-absent.service")
	if err := os.WriteFile(existing, []byte("exact old bytes\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	var wantStat unix.Stat_t
	if err := unix.Lstat(existing, &wantStat); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	syncs := 0
	deps := testVMUnitTransactionDeps(func(args ...string) error {
		calls = append(calls, append([]string(nil), args...))
		return nil
	})
	deps.syncDir = func(dir *os.File) error {
		syncs++
		return dir.Sync()
	}
	tx, err := prepareVMUnitTransaction(context.Background(), []vmUnitSpec{
		{Service: "existing", Path: existing, Content: []byte("new existing\n")},
		{Service: "absent", Path: absent, Content: []byte("new absent\n")},
	}, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Close() })
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := tx.RestorePreviousAndVerify(); err != nil {
		t.Fatal(err)
	}
	if err := tx.RestorePreviousAndVerify(); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	if got := readVMUnitTransactionTestFile(t, existing); got != "exact old bytes\n" {
		t.Fatalf("restored bytes = %q", got)
	}
	var gotStat unix.Stat_t
	if err := unix.Lstat(existing, &gotStat); err != nil {
		t.Fatal(err)
	}
	if uint32(gotStat.Mode) != uint32(wantStat.Mode) || gotStat.Uid != wantStat.Uid || gotStat.Gid != wantStat.Gid {
		t.Fatalf("restored metadata = mode %o uid:gid %d:%d, want %o %d:%d", uint32(gotStat.Mode), gotStat.Uid, gotStat.Gid, uint32(wantStat.Mode), wantStat.Uid, wantStat.Gid)
	}
	if _, err := os.Lstat(absent); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("originally absent unit exists after restore: %v", err)
	}
	if syncs < 5 {
		t.Fatalf("directory syncs = %d, want publication and restoration syncs", syncs)
	}
	wantCalls := [][]string{{"daemon-reload"}, {"daemon-reload"}, {"daemon-reload"}}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("systemctl calls = %#v, want daemon-reload only", calls)
	}
}

func TestVMUnitTransactionPublicationSyncFailureRestoresPriorState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "yeet-vm-alpha.service")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	syncErr := errors.New("publish directory sync failed")
	syncCalls := 0
	deps := testVMUnitTransactionDeps(func(...string) error { return nil })
	deps.syncDir = func(dir *os.File) error {
		syncCalls++
		if syncCalls == 1 {
			return syncErr
		}
		return dir.Sync()
	}
	tx, err := prepareVMUnitTransaction(context.Background(), []vmUnitSpec{{Service: "alpha", Path: path, Content: []byte("new")}}, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Close() })
	if err := tx.Commit(); !errors.Is(err, syncErr) {
		t.Fatalf("Commit error = %v, want %v", err, syncErr)
	}
	if got := readVMUnitTransactionTestFile(t, path); got != "old" {
		t.Fatalf("unit after failed durable publication = %q, want old", got)
	}
}

func TestVMUnitTransactionRenameErrorAfterVisibilityRestoresPriorState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "yeet-vm-alpha.service")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	renameErr := errors.New("rename reported failure")
	deps := testVMUnitTransactionDeps(func(...string) error { return nil })
	deps.renameAt = func(oldDir int, oldName string, newDir int, newName string) error {
		if err := unix.Renameat(oldDir, oldName, newDir, newName); err != nil {
			return err
		}
		return renameErr
	}
	tx, err := prepareVMUnitTransaction(context.Background(), []vmUnitSpec{{Service: "alpha", Path: path, Content: []byte("new")}}, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Close() })
	if err := tx.Commit(); !errors.Is(err, renameErr) {
		t.Fatalf("Commit error = %v, want %v", err, renameErr)
	}
	if got := readVMUnitTransactionTestFile(t, path); got != "old" {
		t.Fatalf("unit after error-after-visible = %q, want old", got)
	}
}

func TestVMUnitTransactionRestoreRefusesConcurrentReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "yeet-vm-alpha.service")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	deps := testVMUnitTransactionDeps(func(...string) error { return nil })
	tx, err := prepareVMUnitTransaction(context.Background(), []vmUnitSpec{{Service: "alpha", Path: path, Content: []byte("new")}}, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Close() })
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(dir, "replacement")
	if err := os.WriteFile(replacement, []byte("concurrent"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	err = tx.RestorePreviousAndVerify()
	var uncertain *vmUnitRestorationUncertainError
	if !errors.As(err, &uncertain) || len(uncertain.Paths()) != 1 || uncertain.Paths()[0] != path {
		t.Fatalf("restore error = %v, want typed uncertainty for %s", err, path)
	}
	if got := readVMUnitTransactionTestFile(t, path); got != "concurrent" {
		t.Fatalf("concurrent replacement overwritten: %q", got)
	}
}

func TestVMUnitTransactionRestoreDetectsReplacementDuringClassification(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "yeet-vm-alpha.service")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	tx, err := prepareVMUnitTransaction(context.Background(), []vmUnitSpec{{Service: "alpha", Path: path, Content: []byte("new")}}, testVMUnitTransactionDeps(func(...string) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Close() })
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(dir, "replacement")
	if err := os.WriteFile(replacement, []byte("concurrent-during-read"), 0o644); err != nil {
		t.Fatal(err)
	}
	replaced := false
	tx.deps.afterRead = func(*os.File, string) {
		if replaced {
			return
		}
		replaced = true
		if err := os.Rename(replacement, path); err != nil {
			t.Fatal(err)
		}
	}
	err = tx.RestorePreviousAndVerify()
	var uncertain *vmUnitRestorationUncertainError
	if !errors.As(err, &uncertain) || !strings.Contains(err.Error(), "changed while it was read") {
		t.Fatalf("restore error = %v, want classification replacement uncertainty", err)
	}
	if got := readVMUnitTransactionTestFile(t, path); got != "concurrent-during-read" {
		t.Fatalf("concurrent replacement overwritten: %q", got)
	}
}

func TestVMUnitTransactionRestoresPOSIXSpecialModeBits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "yeet-vm-alpha.service")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o2644); err != nil {
		t.Fatal(err)
	}
	var before unix.Stat_t
	if err := unix.Lstat(path, &before); err != nil {
		t.Fatal(err)
	}
	if uint32(before.Mode)&unix.S_ISGID == 0 {
		t.Skip("filesystem did not retain setgid on test file")
	}
	tx, err := prepareVMUnitTransaction(context.Background(), []vmUnitSpec{{Service: "alpha", Path: path, Content: []byte("new")}}, testVMUnitTransactionDeps(func(...string) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Close() })
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := tx.RestorePreviousAndVerify(); err != nil {
		t.Fatal(err)
	}
	var after unix.Stat_t
	if err := unix.Lstat(path, &after); err != nil {
		t.Fatal(err)
	}
	if uint32(after.Mode)&0o7777 != uint32(before.Mode)&0o7777 {
		t.Fatalf("restored mode = %o, want %o", uint32(after.Mode)&0o7777, uint32(before.Mode)&0o7777)
	}
}

func TestVMUnitTransactionRestoreSyncFailureCanBeRetried(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "yeet-vm-alpha.service")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	failSync := false
	syncErr := errors.New("restoration sync failed")
	deps := testVMUnitTransactionDeps(func(...string) error { return nil })
	deps.syncDir = func(dir *os.File) error {
		if failSync {
			return syncErr
		}
		return dir.Sync()
	}
	tx, err := prepareVMUnitTransaction(context.Background(), []vmUnitSpec{{Service: "alpha", Path: path, Content: []byte("new")}}, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Close() })
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	failSync = true
	err = tx.RestorePreviousAndVerify()
	var uncertain *vmUnitRestorationUncertainError
	if !errors.As(err, &uncertain) || !errors.Is(err, syncErr) {
		t.Fatalf("restore error = %v, want typed sync uncertainty", err)
	}
	if got := readVMUnitTransactionTestFile(t, path); got != "old" {
		t.Fatalf("visible state after sync failure = %q, want exact old", got)
	}
	failSync = false
	if err := tx.RestorePreviousAndVerify(); err != nil {
		t.Fatalf("retry restoration: %v", err)
	}
}

func testVMUnitTransactionDeps(systemctl func(...string) error) vmUnitTransactionDeps {
	deps := defaultVMUnitTransactionDeps()
	deps.systemctl = systemctl
	deps.unitUID = uint32(os.Geteuid())
	deps.unitGID = uint32(os.Getegid())
	return deps
}

func readVMUnitTransactionTestFile(t testing.TB, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
