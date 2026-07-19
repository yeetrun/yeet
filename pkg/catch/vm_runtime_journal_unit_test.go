// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestVMRuntimeJournalUnitReconcilerExactGenerationsAreIdempotent(t *testing.T) {
	dir := useVMRuntimeJournalUnitTestDir(t)
	records := vmRuntimeJournalUnitTestRecords(t, dir, []string{"alpha", "beta"})
	writeVMRuntimeJournalUnitTestGeneration(t, records, vmRuntimeJournalUnitOld)
	deps, calls := vmRuntimeJournalUnitTestDeps()

	reconciler, err := prepareVMRuntimeJournalUnitReconciler(context.Background(), records, deps)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reconciler.Close(); err != nil {
			t.Error(err)
		}
	}()

	assertVMRuntimeJournalUnitClassification(t, reconciler, vmRuntimeJournalUnitOld)
	if err := reconciler.VerifyOld(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.ReconcileNew(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertVMRuntimeJournalUnitClassification(t, reconciler, vmRuntimeJournalUnitNew)
	assertVMRuntimeJournalUnitGeneration(t, records, vmRuntimeJournalUnitNew)
	if err := reconciler.ReconcileNew(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.ReconcileOld(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertVMRuntimeJournalUnitGeneration(t, records, vmRuntimeJournalUnitOld)

	wantCalls := [][]string{{"daemon-reload"}, {"daemon-reload"}, {"daemon-reload"}}
	if !reflect.DeepEqual(*calls, wantCalls) {
		t.Fatalf("systemctl calls = %v, want %v", *calls, wantCalls)
	}
}

func TestVMRuntimeJournalUnitReconcilerCompletesMixedCohort(t *testing.T) {
	dir := useVMRuntimeJournalUnitTestDir(t)
	records := vmRuntimeJournalUnitTestRecords(t, dir, []string{"alpha", "beta"})
	writeVMRuntimeJournalUnitTestFile(t, records[0].OldUnit)
	writeVMRuntimeJournalUnitTestFile(t, records[1].NewUnit)
	deps, calls := vmRuntimeJournalUnitTestDeps()

	reconciler, err := prepareVMRuntimeJournalUnitReconciler(context.Background(), records, deps)
	if err != nil {
		t.Fatal(err)
	}
	defer reconciler.Close()
	assertVMRuntimeJournalUnitClassification(t, reconciler, vmRuntimeJournalUnitMixed)
	if err := reconciler.ReconcileNew(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertVMRuntimeJournalUnitGeneration(t, records, vmRuntimeJournalUnitNew)
	if want := [][]string{{"daemon-reload"}}; !reflect.DeepEqual(*calls, want) {
		t.Fatalf("systemctl calls = %v, want %v", *calls, want)
	}
}

func TestVMRuntimeJournalUnitReconcilerRefusesContradictoryState(t *testing.T) {
	dir := useVMRuntimeJournalUnitTestDir(t)
	records := vmRuntimeJournalUnitTestRecords(t, dir, []string{"alpha"})
	contradictory := cloneVMRuntimeJournalFile(records[0].OldUnit)
	contradictory.Contents = []byte("not a journal generation\n")
	writeVMRuntimeJournalUnitTestFile(t, contradictory)
	deps, calls := vmRuntimeJournalUnitTestDeps()

	reconciler, err := prepareVMRuntimeJournalUnitReconciler(context.Background(), records, deps)
	if err != nil {
		t.Fatal(err)
	}
	defer reconciler.Close()
	assertVMRuntimeJournalUnitClassification(t, reconciler, vmRuntimeJournalUnitNeither)
	if err := reconciler.ReconcileNew(context.Background()); err == nil || !strings.Contains(err.Error(), "outside its exact journal generations") {
		t.Fatalf("ReconcileNew error = %v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("systemctl calls = %v, want none", *calls)
	}
	assertVMRuntimeJournalUnitBytes(t, contradictory.Path, contradictory.Contents)
}

func TestVMRuntimeJournalUnitReconcilerRejectsSameLengthInPlaceRewriteAfterRead(t *testing.T) {
	dir := useVMRuntimeJournalUnitTestDir(t)
	records := vmRuntimeJournalUnitTestRecords(t, dir, []string{"alpha"})
	writeVMRuntimeJournalUnitTestGeneration(t, records, vmRuntimeJournalUnitOld)
	deps, _ := vmRuntimeJournalUnitTestDeps()
	replacement := bytes.Repeat([]byte{'x'}, len(records[0].OldUnit.Contents))
	rewritten := false
	deps.afterRead = func(dir *os.File, name string) {
		if rewritten {
			return
		}
		rewritten = true
		file, err := os.OpenFile(filepath.Join(dir.Name(), name), os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteAt(replacement, 0); err != nil {
			t.Fatal(err)
		}
		if err := file.Sync(); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	reconciler, err := prepareVMRuntimeJournalUnitReconciler(context.Background(), records, deps)
	if err != nil {
		t.Fatal(err)
	}
	defer reconciler.Close()

	if _, err := reconciler.Classify(context.Background()); err == nil || !strings.Contains(err.Error(), "changed between stable reads") {
		t.Fatalf("Classify error = %v, want stable-read rejection", err)
	}
	assertVMRuntimeJournalUnitBytes(t, records[0].OldUnit.Path, replacement)
}

func TestVMRuntimeJournalUnitReconcilerCreatesAndRestoresAbsence(t *testing.T) {
	dir := useVMRuntimeJournalUnitTestDir(t)
	records := vmRuntimeJournalUnitTestRecords(t, dir, []string{"alpha"})
	records[0].OldUnit = vmRuntimeJournalFile{Path: records[0].NewUnit.Path}
	deps, calls := vmRuntimeJournalUnitTestDeps()

	reconciler, err := prepareVMRuntimeJournalUnitReconciler(context.Background(), records, deps)
	if err != nil {
		t.Fatal(err)
	}
	defer reconciler.Close()
	if err := reconciler.ReconcileNew(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertVMRuntimeJournalUnitGeneration(t, records, vmRuntimeJournalUnitNew)
	if err := reconciler.ReconcileOld(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertVMRuntimeJournalUnitGeneration(t, records, vmRuntimeJournalUnitOld)
	if got := len(*calls); got != 2 {
		t.Fatalf("daemon-reload calls = %d, want 2", got)
	}
}

func TestVMRuntimeJournalUnitReconcilerCreateRetainsLateCanonicalOutcomes(t *testing.T) {
	for _, test := range []struct {
		name        string
		lateChange  func(testing.TB, string)
		wantContent []byte
	}{
		{
			name: "delete",
			lateChange: func(t testing.TB, live string) {
				if err := os.Remove(live); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:        "replacement",
			wantContent: []byte("late replacement\n"),
			lateChange: func(t testing.TB, live string) {
				replacement := live + ".late"
				if err := os.WriteFile(replacement, []byte("late replacement\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(replacement, live); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := useVMRuntimeJournalUnitTestDir(t)
			records := vmRuntimeJournalUnitTestRecords(t, dir, []string{"alpha"})
			records[0].OldUnit = vmRuntimeJournalFile{Path: records[0].NewUnit.Path}
			deps, calls := vmRuntimeJournalUnitTestDeps()
			syncs := 0
			realSync := deps.syncDir
			deps.syncDir = func(dir *os.File) error {
				syncs++
				return realSync(dir)
			}
			deps.afterPublish = func(dir *os.File, canonicalName, _ string) {
				test.lateChange(t, filepath.Join(dir.Name(), canonicalName))
			}
			reconciler, err := prepareVMRuntimeJournalUnitReconciler(context.Background(), records, deps)
			if err != nil {
				t.Fatal(err)
			}
			defer reconciler.Close()

			err = reconciler.ReconcileNew(context.Background())
			assertVMRuntimeJournalUnitUncertain(t, err, records[0].NewUnit.Path)
			if syncs == 0 {
				t.Fatal("post-publication uncertainty did not sync the bound unit directory")
			}
			if want := [][]string{{"daemon-reload"}}; !reflect.DeepEqual(*calls, want) {
				t.Fatalf("systemctl calls = %v, want %v", *calls, want)
			}
			if test.wantContent == nil {
				if _, err := os.Lstat(records[0].NewUnit.Path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("Lstat after late delete = %v, want absence", err)
				}
			} else {
				assertVMRuntimeJournalUnitBytes(t, records[0].NewUnit.Path, test.wantContent)
			}
		})
	}
}

func TestVMRuntimeJournalUnitReconcilerCreateRetainsFailedStagedCleanup(t *testing.T) {
	dir := useVMRuntimeJournalUnitTestDir(t)
	records := vmRuntimeJournalUnitTestRecords(t, dir, []string{"alpha"})
	records[0].OldUnit = vmRuntimeJournalFile{Path: records[0].NewUnit.Path}
	deps, calls := vmRuntimeJournalUnitTestDeps()
	syncs := 0
	realSync := deps.syncDir
	deps.syncDir = func(dir *os.File) error {
		syncs++
		return realSync(dir)
	}
	deps.renameNoReplaceAt = func(int, string, int, string) error {
		return errors.New("create publication unavailable")
	}
	realUnlink := deps.unlinkAt
	deps.unlinkAt = func(dir int, name string, flags int) error {
		if strings.Contains(name, ".unit-") {
			return errors.New("staged cleanup unavailable")
		}
		return realUnlink(dir, name, flags)
	}
	reconciler, err := prepareVMRuntimeJournalUnitReconciler(context.Background(), records, deps)
	if err != nil {
		t.Fatal(err)
	}

	err = reconciler.ReconcileNew(context.Background())
	paths := assertVMRuntimeJournalUnitUncertain(t, err, records[0].NewUnit.Path)
	if syncs == 0 {
		t.Fatal("retained staged create did not sync the bound unit directory")
	}
	if want := [][]string{{"daemon-reload"}}; !reflect.DeepEqual(*calls, want) {
		t.Fatalf("systemctl calls = %v, want %v", *calls, want)
	}
	if _, err := os.Lstat(records[0].NewUnit.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canonical after failed create = %v, want unchanged absence", err)
	}
	assertVMRuntimeJournalUnitBytes(t, paths[1], records[0].NewUnit.Contents)
	if err := reconciler.Close(); err != nil {
		t.Fatal(err)
	}
	assertVMRuntimeJournalUnitBytes(t, paths[1], records[0].NewUnit.Contents)
}

func TestVMRuntimeJournalUnitReconcilerExchangeRetainsFailedStagedCleanup(t *testing.T) {
	dir := useVMRuntimeJournalUnitTestDir(t)
	records := vmRuntimeJournalUnitTestRecords(t, dir, []string{"alpha"})
	writeVMRuntimeJournalUnitTestGeneration(t, records, vmRuntimeJournalUnitOld)
	deps, calls := vmRuntimeJournalUnitTestDeps()
	syncs := 0
	realSync := deps.syncDir
	deps.syncDir = func(dir *os.File) error {
		syncs++
		return realSync(dir)
	}
	deps.exchangeAt = func(int, string, int, string) error {
		return errors.New("exchange publication unavailable")
	}
	realUnlink := deps.unlinkAt
	deps.unlinkAt = func(dir int, name string, flags int) error {
		if strings.Contains(name, ".unit-") {
			return errors.New("staged cleanup unavailable")
		}
		return realUnlink(dir, name, flags)
	}
	reconciler, err := prepareVMRuntimeJournalUnitReconciler(context.Background(), records, deps)
	if err != nil {
		t.Fatal(err)
	}

	err = reconciler.ReconcileNew(context.Background())
	paths := assertVMRuntimeJournalUnitUncertain(t, err, records[0].NewUnit.Path)
	if syncs == 0 {
		t.Fatal("retained staged exchange did not sync the bound unit directory")
	}
	if want := [][]string{{"daemon-reload"}}; !reflect.DeepEqual(*calls, want) {
		t.Fatalf("systemctl calls = %v, want %v", *calls, want)
	}
	assertVMRuntimeJournalUnitGeneration(t, records, vmRuntimeJournalUnitOld)
	assertVMRuntimeJournalUnitBytes(t, paths[1], records[0].NewUnit.Contents)
	if err := reconciler.Close(); err != nil {
		t.Fatal(err)
	}
	assertVMRuntimeJournalUnitGeneration(t, records, vmRuntimeJournalUnitOld)
	assertVMRuntimeJournalUnitBytes(t, paths[1], records[0].NewUnit.Contents)
}

func TestVMRuntimeJournalUnitReconcilerQuarantineRetainsLateDelete(t *testing.T) {
	dir := useVMRuntimeJournalUnitTestDir(t)
	records := vmRuntimeJournalUnitTestRecords(t, dir, []string{"alpha"})
	records[0].OldUnit = vmRuntimeJournalFile{Path: records[0].NewUnit.Path}
	writeVMRuntimeJournalUnitTestGeneration(t, records, vmRuntimeJournalUnitNew)
	deps, calls := vmRuntimeJournalUnitTestDeps()
	syncs := 0
	realSync := deps.syncDir
	deps.syncDir = func(dir *os.File) error {
		syncs++
		return realSync(dir)
	}
	deps.afterPublish = func(dir *os.File, _, recoveryName string) {
		if err := os.Remove(filepath.Join(dir.Name(), recoveryName)); err != nil {
			t.Fatal(err)
		}
	}
	reconciler, err := prepareVMRuntimeJournalUnitReconciler(context.Background(), records, deps)
	if err != nil {
		t.Fatal(err)
	}
	defer reconciler.Close()

	err = reconciler.ReconcileOld(context.Background())
	assertVMRuntimeJournalUnitUncertain(t, err, records[0].NewUnit.Path)
	if syncs == 0 {
		t.Fatal("quarantine uncertainty did not sync the bound unit directory")
	}
	if want := [][]string{{"daemon-reload"}}; !reflect.DeepEqual(*calls, want) {
		t.Fatalf("systemctl calls = %v, want %v", *calls, want)
	}
	if _, err := os.Lstat(records[0].NewUnit.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Lstat after late quarantine delete = %v, want absence", err)
	}
}

func TestVMRuntimeJournalUnitReconcilerQuarantineRetainsLateCanonicalReplacement(t *testing.T) {
	dir := useVMRuntimeJournalUnitTestDir(t)
	records := vmRuntimeJournalUnitTestRecords(t, dir, []string{"alpha"})
	records[0].OldUnit = vmRuntimeJournalFile{Path: records[0].NewUnit.Path}
	writeVMRuntimeJournalUnitTestGeneration(t, records, vmRuntimeJournalUnitNew)
	deps, calls := vmRuntimeJournalUnitTestDeps()
	replacement := []byte("late canonical replacement\n")
	deps.afterPublish = func(dir *os.File, canonicalName, _ string) {
		if err := os.WriteFile(filepath.Join(dir.Name(), canonicalName), replacement, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reconciler, err := prepareVMRuntimeJournalUnitReconciler(context.Background(), records, deps)
	if err != nil {
		t.Fatal(err)
	}

	err = reconciler.ReconcileOld(context.Background())
	paths := assertVMRuntimeJournalUnitUncertain(t, err, records[0].NewUnit.Path)
	if want := [][]string{{"daemon-reload"}}; !reflect.DeepEqual(*calls, want) {
		t.Fatalf("systemctl calls = %v, want %v", *calls, want)
	}
	assertVMRuntimeJournalUnitBytes(t, records[0].NewUnit.Path, replacement)
	assertVMRuntimeJournalUnitBytes(t, paths[1], records[0].NewUnit.Contents)
	if err := reconciler.Close(); err != nil {
		t.Fatal(err)
	}
	assertVMRuntimeJournalUnitBytes(t, paths[1], records[0].NewUnit.Contents)
}

func TestVMRuntimeJournalUnitReconcilerHonorsCanceledContextBeforeMutation(t *testing.T) {
	dir := useVMRuntimeJournalUnitTestDir(t)
	records := vmRuntimeJournalUnitTestRecords(t, dir, []string{"alpha"})
	writeVMRuntimeJournalUnitTestGeneration(t, records, vmRuntimeJournalUnitOld)
	deps, calls := vmRuntimeJournalUnitTestDeps()
	reconciler, err := prepareVMRuntimeJournalUnitReconciler(context.Background(), records, deps)
	if err != nil {
		t.Fatal(err)
	}
	defer reconciler.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := reconciler.ReconcileNew(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReconcileNew error = %v, want context cancellation", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("systemctl calls = %v, want none", *calls)
	}
	assertVMRuntimeJournalUnitGeneration(t, records, vmRuntimeJournalUnitOld)
}

func TestVMRuntimeJournalUnitReconcilerRestoresReplacementCaughtByAtomicExchange(t *testing.T) {
	dir := useVMRuntimeJournalUnitTestDir(t)
	records := vmRuntimeJournalUnitTestRecords(t, dir, []string{"alpha"})
	writeVMRuntimeJournalUnitTestGeneration(t, records, vmRuntimeJournalUnitOld)
	deps, calls := vmRuntimeJournalUnitTestDeps()
	changed := false
	deps.afterSourceCheck = func(dir *os.File, name string) {
		if changed {
			return
		}
		changed = true
		live := filepath.Join(dir.Name(), name)
		replacement := live + ".replacement"
		if err := os.WriteFile(replacement, []byte("concurrent exact-size change\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, live); err != nil {
			t.Fatal(err)
		}
	}
	reconciler, err := prepareVMRuntimeJournalUnitReconciler(context.Background(), records, deps)
	if err != nil {
		t.Fatal(err)
	}
	defer reconciler.Close()

	if err := reconciler.ReconcileNew(context.Background()); err == nil || !strings.Contains(err.Error(), "atomic exchange was restored") {
		t.Fatalf("ReconcileNew error = %v", err)
	}
	if want := [][]string{{"daemon-reload"}}; !reflect.DeepEqual(*calls, want) {
		t.Fatalf("systemctl calls = %v, want %v", *calls, want)
	}
	assertVMRuntimeJournalUnitBytes(t, records[0].NewUnit.Path, []byte("concurrent exact-size change\n"))
}

func TestVMRuntimeJournalUnitReconcilerRetainsBothPathsWhenExchangeRestorationFails(t *testing.T) {
	dir := useVMRuntimeJournalUnitTestDir(t)
	records := vmRuntimeJournalUnitTestRecords(t, dir, []string{"alpha"})
	writeVMRuntimeJournalUnitTestGeneration(t, records, vmRuntimeJournalUnitOld)
	deps, calls := vmRuntimeJournalUnitTestDeps()
	concurrent := []byte("replacement retained after failed restore\n")
	deps.afterSourceCheck = func(dir *os.File, name string) {
		live := filepath.Join(dir.Name(), name)
		replacement := live + ".replacement"
		if err := os.WriteFile(replacement, concurrent, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, live); err != nil {
			t.Fatal(err)
		}
	}
	realExchange := deps.exchangeAt
	exchanges := 0
	deps.exchangeAt = func(oldDir int, oldName string, newDir int, newName string) error {
		exchanges++
		if exchanges == 2 {
			return errors.New("restore exchange unavailable")
		}
		return realExchange(oldDir, oldName, newDir, newName)
	}
	reconciler, err := prepareVMRuntimeJournalUnitReconciler(context.Background(), records, deps)
	if err != nil {
		t.Fatal(err)
	}
	defer reconciler.Close()

	err = reconciler.ReconcileNew(context.Background())
	var uncertain *vmRuntimeJournalUnitUncertainError
	if !errors.As(err, &uncertain) {
		t.Fatalf("ReconcileNew error = %v, want typed uncertainty", err)
	}
	paths := uncertain.Paths()
	if len(paths) != 2 || paths[0] != records[0].NewUnit.Path {
		t.Fatalf("uncertain paths = %v", paths)
	}
	assertVMRuntimeJournalUnitBytes(t, paths[0], records[0].NewUnit.Contents)
	assertVMRuntimeJournalUnitBytes(t, paths[1], concurrent)
	if want := [][]string{{"daemon-reload"}}; !reflect.DeepEqual(*calls, want) {
		t.Fatalf("systemctl calls = %v, want %v", *calls, want)
	}
}

func TestVMRuntimeJournalUnitReconcilerRestoresReplacementCaughtByQuarantine(t *testing.T) {
	dir := useVMRuntimeJournalUnitTestDir(t)
	records := vmRuntimeJournalUnitTestRecords(t, dir, []string{"alpha"})
	records[0].OldUnit = vmRuntimeJournalFile{Path: records[0].NewUnit.Path}
	writeVMRuntimeJournalUnitTestGeneration(t, records, vmRuntimeJournalUnitNew)
	deps, calls := vmRuntimeJournalUnitTestDeps()
	concurrent := []byte("concurrent unit before quarantine\n")
	changed := false
	deps.afterSourceCheck = func(dir *os.File, name string) {
		if changed {
			return
		}
		changed = true
		live := filepath.Join(dir.Name(), name)
		replacement := live + ".replacement"
		if err := os.WriteFile(replacement, concurrent, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, live); err != nil {
			t.Fatal(err)
		}
	}
	reconciler, err := prepareVMRuntimeJournalUnitReconciler(context.Background(), records, deps)
	if err != nil {
		t.Fatal(err)
	}
	defer reconciler.Close()

	if err := reconciler.ReconcileOld(context.Background()); err == nil || !strings.Contains(err.Error(), "quarantined replacement was restored") {
		t.Fatalf("ReconcileOld error = %v", err)
	}
	if want := [][]string{{"daemon-reload"}}; !reflect.DeepEqual(*calls, want) {
		t.Fatalf("systemctl calls = %v, want %v", *calls, want)
	}
	assertVMRuntimeJournalUnitBytes(t, records[0].NewUnit.Path, concurrent)
}

func TestVMRuntimeJournalUnitReconcilerReloadFailureIsRetryable(t *testing.T) {
	dir := useVMRuntimeJournalUnitTestDir(t)
	records := vmRuntimeJournalUnitTestRecords(t, dir, []string{"alpha"})
	writeVMRuntimeJournalUnitTestGeneration(t, records, vmRuntimeJournalUnitOld)
	deps, calls := vmRuntimeJournalUnitTestDeps()
	deps.systemctl = func(args ...string) error {
		*calls = append(*calls, append([]string(nil), args...))
		if len(*calls) == 1 {
			return errors.New("reload unavailable")
		}
		return nil
	}
	reconciler, err := prepareVMRuntimeJournalUnitReconciler(context.Background(), records, deps)
	if err != nil {
		t.Fatal(err)
	}
	defer reconciler.Close()

	if err := reconciler.ReconcileNew(context.Background()); err == nil || !strings.Contains(err.Error(), "reload unavailable") {
		t.Fatalf("first ReconcileNew error = %v", err)
	}
	if err := reconciler.VerifyNew(context.Background()); err != nil {
		t.Fatalf("new exact generation not visible after reload failure: %v", err)
	}
	if err := reconciler.ReconcileNew(context.Background()); err != nil {
		t.Fatalf("retry ReconcileNew: %v", err)
	}
	if got := len(*calls); got != 2 {
		t.Fatalf("daemon-reload calls = %d, want retry after failure", got)
	}
}

func TestVMRuntimeJournalUnitReconcilerRejectsSymlinkAndReleasesLock(t *testing.T) {
	dir := useVMRuntimeJournalUnitTestDir(t)
	records := vmRuntimeJournalUnitTestRecords(t, dir, []string{"alpha"})
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, records[0].OldUnit.Contents, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, records[0].OldUnit.Path); err != nil {
		t.Fatal(err)
	}
	deps, _ := vmRuntimeJournalUnitTestDeps()
	reconciler, err := prepareVMRuntimeJournalUnitReconciler(context.Background(), records, deps)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Classify(context.Background()); err == nil || !strings.Contains(err.Error(), "without following symlinks") {
		t.Fatalf("Classify error = %v", err)
	}
	if err := reconciler.Close(); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := (*vmRuntimeJournalUnitReconciler)(nil).Close(); err != nil {
		t.Fatalf("nil Close: %v", err)
	}
	if _, err := reconciler.Classify(context.Background()); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("Classify after Close error = %v", err)
	}

	if err := os.Remove(records[0].OldUnit.Path); err != nil {
		t.Fatal(err)
	}
	writeVMRuntimeJournalUnitTestGeneration(t, records, vmRuntimeJournalUnitOld)
	second, err := prepareVMRuntimeJournalUnitReconciler(context.Background(), records, deps)
	if err != nil {
		t.Fatalf("prepare after lock release: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestVMRuntimeJournalUnitReconcilerLockWaitHonorsContext(t *testing.T) {
	dir := useVMRuntimeJournalUnitTestDir(t)
	records := vmRuntimeJournalUnitTestRecords(t, dir, []string{"alpha"})
	writeVMRuntimeJournalUnitTestGeneration(t, records, vmRuntimeJournalUnitOld)
	deps, _ := vmRuntimeJournalUnitTestDeps()
	first, err := prepareVMRuntimeJournalUnitReconciler(context.Background(), records, deps)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := prepareVMRuntimeJournalUnitReconciler(ctx, records, deps); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second prepare error = %v, want deadline", err)
	}
}

func useVMRuntimeJournalUnitTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := vmSystemdSystemDir
	vmSystemdSystemDir = dir
	t.Cleanup(func() { vmSystemdSystemDir = old })
	return dir
}

func vmRuntimeJournalUnitTestRecords(t *testing.T, root string, services []string) []vmRuntimeJournalRecord {
	t.Helper()
	records := validVMRuntimeJournalCohort(root, services...)
	uid, gid := uint32(os.Getuid()), uint32(os.Getgid())
	for i := range records {
		oldRaw := []byte("old exact unit " + records[i].Service + "\n")
		newRaw := []byte("new exact unit " + records[i].Service + "\n")
		path := records[i].NewUnit.Path
		records[i].OldUnit = vmRuntimeJournalFileFromBytes(path, true, oldRaw, unix.S_IFREG|0o600, uid, gid)
		records[i].NewUnit = vmRuntimeJournalFileFromBytes(path, true, newRaw, unix.S_IFREG|0o644, uid, gid)
	}
	return records
}

func vmRuntimeJournalUnitTestDeps() (vmRuntimeJournalUnitDeps, *[][]string) {
	deps := defaultVMRuntimeJournalUnitDeps()
	deps.uid = uint32(os.Getuid())
	deps.gid = uint32(os.Getgid())
	calls := &[][]string{}
	deps.systemctl = func(args ...string) error {
		*calls = append(*calls, append([]string(nil), args...))
		return nil
	}
	return deps, calls
}

func writeVMRuntimeJournalUnitTestGeneration(t *testing.T, records []vmRuntimeJournalRecord, generation vmRuntimeJournalUnitClassification) {
	t.Helper()
	for _, record := range records {
		state := record.OldUnit
		if generation == vmRuntimeJournalUnitNew {
			state = record.NewUnit
		}
		writeVMRuntimeJournalUnitTestFile(t, state)
	}
}

func writeVMRuntimeJournalUnitTestFile(t *testing.T, state vmRuntimeJournalFile) {
	t.Helper()
	if !state.Exists {
		if err := os.Remove(state.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		return
	}
	if err := os.WriteFile(state.Path, state.Contents, os.FileMode(state.Mode&0o7777)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(state.Path, vmUnitFileMode(state.Mode)); err != nil {
		t.Fatal(err)
	}
}

func assertVMRuntimeJournalUnitClassification(t *testing.T, reconciler *vmRuntimeJournalUnitReconciler, want vmRuntimeJournalUnitClassification) {
	t.Helper()
	got, err := reconciler.Classify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("classification = %s, want %s", got, want)
	}
}

func assertVMRuntimeJournalUnitGeneration(t *testing.T, records []vmRuntimeJournalRecord, generation vmRuntimeJournalUnitClassification) {
	t.Helper()
	for _, record := range records {
		state := record.OldUnit
		if generation == vmRuntimeJournalUnitNew {
			state = record.NewUnit
		}
		if !state.Exists {
			if _, err := os.Lstat(state.Path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("Lstat(%s) error = %v, want absence", state.Path, err)
			}
			continue
		}
		assertVMRuntimeJournalUnitBytes(t, state.Path, state.Contents)
		var stat unix.Stat_t
		if err := unix.Lstat(state.Path, &stat); err != nil {
			t.Fatal(err)
		}
		if uint32(stat.Mode) != state.Mode || stat.Uid != state.UID || stat.Gid != state.GID {
			t.Fatalf("state for %s = mode %o owner %d:%d, want mode %o owner %d:%d", state.Path, uint32(stat.Mode), stat.Uid, stat.Gid, state.Mode, state.UID, state.GID)
		}
	}
}

func assertVMRuntimeJournalUnitBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("contents of %s = %q, want %q", path, got, want)
	}
}

func assertVMRuntimeJournalUnitUncertain(t *testing.T, err error, canonical string) []string {
	t.Helper()
	var uncertain *vmRuntimeJournalUnitUncertainError
	if !errors.As(err, &uncertain) {
		t.Fatalf("error = %v, want typed VM unit uncertainty", err)
	}
	paths := uncertain.Paths()
	if len(paths) != 2 || paths[0] != canonical || filepath.Dir(paths[1]) != filepath.Dir(canonical) {
		t.Fatalf("uncertain paths = %v, want canonical and bound recovery path", paths)
	}
	return paths
}
