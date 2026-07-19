// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
	"golang.org/x/sys/unix"
)

func TestStageVMJailerUnitCreatesOwnedRegularFile(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "yeet-vm-alpha.service")
	if err := os.WriteFile(live, []byte("old-alpha"), 0o600); err != nil {
		t.Fatalf("WriteFile live unit: %v", err)
	}
	deps := defaultVMJailerUpgradeDeps()
	deps.unitUID = uint32(os.Geteuid())
	deps.unitGID = uint32(os.Getegid())
	replacement, err := stageVMJailerUnit(vmJailerUpgradeVM{
		Service: "alpha", UnitPath: live, UnitContent: []byte("new-alpha"),
	}, deps)
	if err != nil {
		t.Fatalf("stageVMJailerUnit: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(replacement.Staged) })
	if !replacement.Existed || string(replacement.Previous) != "old-alpha" {
		t.Fatalf("replacement previous state = %#v", replacement)
	}
	var stat unix.Stat_t
	if err := unix.Lstat(replacement.Staged, &stat); err != nil {
		t.Fatalf("Lstat staged unit: %v", err)
	}
	if uint32(stat.Mode)&unix.S_IFMT != unix.S_IFREG {
		t.Fatalf("staged mode = %o, want regular file", stat.Mode)
	}
	if uint32(stat.Mode)&0o777 != 0o644 {
		t.Fatalf("staged permissions = %o, want 0644", uint32(stat.Mode)&0o777)
	}
	if stat.Uid != deps.unitUID || stat.Gid != deps.unitGID {
		t.Fatalf("staged owner = %d:%d, want %d:%d", stat.Uid, stat.Gid, deps.unitUID, deps.unitGID)
	}
	if raw, err := os.ReadFile(replacement.Staged); err != nil || string(raw) != "new-alpha" {
		t.Fatalf("staged content = %q, %v; want new-alpha", raw, err)
	}
}

func TestStageVMJailerUnitRejectsSymlinkUnit(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim.service")
	live := filepath.Join(dir, "yeet-vm-alpha.service")
	if err := os.WriteFile(victim, []byte("do-not-read"), 0o644); err != nil {
		t.Fatalf("WriteFile victim: %v", err)
	}
	if err := os.Symlink(victim, live); err != nil {
		t.Fatalf("Symlink live unit: %v", err)
	}
	deps := defaultVMJailerUpgradeDeps()
	deps.unitUID = uint32(os.Geteuid())
	deps.unitGID = uint32(os.Getegid())

	_, err := stageVMJailerUnit(vmJailerUpgradeVM{
		Service: "alpha", UnitPath: live, UnitContent: []byte("new-alpha"),
	}, deps)
	if err == nil || !strings.Contains(err.Error(), "without following symlinks") {
		t.Fatalf("stageVMJailerUnit error = %v, want symlink refusal", err)
	}
	matches, globErr := filepath.Glob(filepath.Join(dir, ".yeet-vm-alpha.service.jailer-*"))
	if globErr != nil {
		t.Fatalf("Glob staged units: %v", globErr)
	}
	if len(matches) != 0 {
		t.Fatalf("staged files after symlink refusal = %v", matches)
	}
}

func TestStageVMJailerUnitRejectsSymlinkParentDirectory(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real-systemd")
	linkedDir := filepath.Join(root, "linked-systemd")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatalf("Mkdir real systemd dir: %v", err)
	}
	if err := os.Symlink(realDir, linkedDir); err != nil {
		t.Fatalf("Symlink systemd dir: %v", err)
	}
	deps := defaultVMJailerUpgradeDeps()
	deps.unitUID = uint32(os.Geteuid())
	deps.unitGID = uint32(os.Getegid())

	_, err := stageVMJailerUnit(vmJailerUpgradeVM{
		Service: "alpha", UnitPath: filepath.Join(linkedDir, "yeet-vm-alpha.service"), UnitContent: []byte("new-alpha"),
	}, deps)
	if err == nil || !strings.Contains(err.Error(), "unit directory without following symlinks") {
		t.Fatalf("stageVMJailerUnit error = %v, want symlinked parent refusal", err)
	}
	entries, readErr := os.ReadDir(realDir)
	if readErr != nil {
		t.Fatalf("ReadDir real systemd dir: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("files created through symlinked parent = %v", entries)
	}
}

func TestPrepareVMJailerUpgradeResolvesEveryVMBeforeStaging(t *testing.T) {
	dataRoot := t.TempDir()
	servicesRoot := filepath.Join(dataRoot, "services")
	systemdDir := filepath.Join(dataRoot, "systemd")
	if err := os.MkdirAll(systemdDir, 0o700); err != nil {
		t.Fatalf("MkdirAll systemd dir: %v", err)
	}
	store := db.NewStore(filepath.Join(dataRoot, "db.json"), servicesRoot)
	cfg := &Config{RootDir: dataRoot, ServicesRoot: servicesRoot, DB: store}
	for _, service := range []string{"alpha", "beta"} {
		version := service + "-v1"
		rootFS := writeVMJailerUpgradeTargetBundle(t, filepath.Join(dataRoot, "vm-images", version), version, "amd64")
		addTestServices(t, &Server{cfg: *cfg}, db.Service{
			Name: service, ServiceType: db.ServiceTypeVM,
			VM: &db.VMConfig{
				Runtime: vmRuntimeFirecracker,
				Image:   db.VMImageConfig{Payload: "vm://custom/" + service, Version: version, RootFS: rootFS},
			},
		})
	}
	var resolved []string
	resolveErr := errors.New("beta jailer unavailable")
	deps := defaultVMJailerUpgradeDeps()
	deps.sibling = func(_ context.Context, vm vmJailerUpgradeVM) (string, bool, error) {
		resolved = append(resolved, vm.Service)
		if vm.Service == "beta" {
			return "", false, resolveErr
		}
		return vm.Jailer, true, nil
	}
	deps.readiness = func(string) (vmJailerReadiness, error) { return vmJailerPendingRestart, nil }
	deps.isRunning = func(*Server, string) (bool, error) { return false, nil }
	deps.renderUnit = func(cfg vmSystemdConfig) (string, error) { return cfg.Service, nil }
	deps.unitUID = uint32(os.Geteuid())
	deps.unitGID = uint32(os.Getegid())
	oldSystemdDir := vmSystemdSystemDir
	vmSystemdSystemDir = systemdDir
	t.Cleanup(func() { vmSystemdSystemDir = oldSystemdDir })

	_, err := prepareVMJailerUpgradeWithDeps(context.Background(), cfg, deps)
	if !errors.Is(err, resolveErr) {
		t.Fatalf("prepareVMJailerUpgradeWithDeps error = %v, want %v", err, resolveErr)
	}
	if !reflect.DeepEqual(resolved, []string{"alpha", "beta"}) {
		t.Fatalf("resolved VMs = %v, want alpha then beta", resolved)
	}
	entries, readErr := os.ReadDir(systemdDir)
	if readErr != nil {
		t.Fatalf("ReadDir systemd: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("staged units before all jailers resolved = %v", entries)
	}
}

func TestPrepareVMJailerUpgradeEnsuresRuntimeIdentityBeforeStaging(t *testing.T) {
	fixture := newVMJailerUpgradeIdentityFixture(t)
	identityCalls := 0
	fixture.deps.ensureRuntimeIdentity = func() (vmRuntimeIdentity, error) {
		identityCalls++
		entries, err := os.ReadDir(fixture.systemdDir)
		if err != nil {
			t.Fatalf("ReadDir systemd during identity preflight: %v", err)
		}
		if got := vmJailerUpgradeEntryNames(entries); !reflect.DeepEqual(got, []string{filepath.Base(fixture.unitPath)}) {
			t.Fatalf("systemd entries during identity preflight = %v, want live unit only", got)
		}
		if raw, err := os.ReadFile(fixture.unitPath); err != nil || string(raw) != "old-alpha" {
			t.Fatalf("live unit during identity preflight = %q, %v; want old-alpha", raw, err)
		}
		return vmRuntimeIdentity{UID: 812, GID: 813}, nil
	}

	tx, err := prepareVMJailerUpgradeWithDeps(context.Background(), fixture.cfg, fixture.deps)
	if err != nil {
		t.Fatalf("prepareVMJailerUpgradeWithDeps: %v", err)
	}
	t.Cleanup(func() { _ = tx.Close() })
	if identityCalls != 1 {
		t.Fatalf("runtime identity calls = %d, want 1", identityCalls)
	}
	if len(tx.units) != 1 {
		t.Fatalf("staged unit count = %d, want 1", len(tx.units))
	}
	if _, err := os.Lstat(tx.units[0].Staged); err != nil {
		t.Fatalf("Lstat staged unit after identity preflight: %v", err)
	}
}

func TestPrepareVMJailerUpgradeIdentityFailureLeavesUnitsUntouched(t *testing.T) {
	fixture := newVMJailerUpgradeIdentityFixture(t)
	identityErr := errors.New("create yeet-vm account")
	identityCalls := 0
	fixture.deps.ensureRuntimeIdentity = func() (vmRuntimeIdentity, error) {
		identityCalls++
		return vmRuntimeIdentity{}, identityErr
	}

	tx, err := prepareVMJailerUpgradeWithDeps(context.Background(), fixture.cfg, fixture.deps)
	if tx != nil {
		_ = tx.Close()
		t.Fatal("prepareVMJailerUpgradeWithDeps returned a transaction after identity failure")
	}
	if !errors.Is(err, identityErr) {
		t.Fatalf("prepareVMJailerUpgradeWithDeps error = %v, want %v", err, identityErr)
	}
	if identityCalls != 1 {
		t.Fatalf("runtime identity calls = %d, want 1", identityCalls)
	}
	if raw, readErr := os.ReadFile(fixture.unitPath); readErr != nil || string(raw) != "old-alpha" {
		t.Fatalf("live unit after identity failure = %q, %v; want old-alpha", raw, readErr)
	}
	entries, readErr := os.ReadDir(fixture.systemdDir)
	if readErr != nil {
		t.Fatalf("ReadDir systemd after identity failure: %v", readErr)
	}
	if got := vmJailerUpgradeEntryNames(entries); !reflect.DeepEqual(got, []string{filepath.Base(fixture.unitPath)}) {
		t.Fatalf("systemd entries after identity failure = %v, want live unit only", got)
	}
}

func TestPrepareVMJailerUpgradeWithoutVMsDoesNotEnsureRuntimeIdentity(t *testing.T) {
	dataRoot := t.TempDir()
	servicesRoot := filepath.Join(dataRoot, "services")
	cfg := &Config{
		RootDir:      dataRoot,
		ServicesRoot: servicesRoot,
		DB:           db.NewStore(filepath.Join(dataRoot, "db.json"), servicesRoot),
	}
	identityCalls := 0
	deps := defaultVMJailerUpgradeDeps()
	deps.ensureRuntimeIdentity = func() (vmRuntimeIdentity, error) {
		identityCalls++
		return vmRuntimeIdentity{}, errors.New("identity must not be created without VMs")
	}

	tx, err := prepareVMJailerUpgradeWithDeps(context.Background(), cfg, deps)
	if err != nil {
		t.Fatalf("prepareVMJailerUpgradeWithDeps: %v", err)
	}
	t.Cleanup(func() { _ = tx.Close() })
	if identityCalls != 0 {
		t.Fatalf("runtime identity calls = %d, want 0", identityCalls)
	}
	if len(tx.units) != 0 {
		t.Fatalf("staged unit count = %d, want 0", len(tx.units))
	}
}

func TestDefaultVMJailerUpgradeStagesRootOwnedUnits(t *testing.T) {
	deps := defaultVMJailerUpgradeDeps()
	if deps.unitUID != 0 || deps.unitGID != 0 {
		t.Fatalf("default staged unit owner = %d:%d, want root", deps.unitUID, deps.unitGID)
	}
}

func TestVMJailerUpgradeCommitNeverRestartsVMs(t *testing.T) {
	tx, calls := newVMJailerUnitTransaction(t, "alpha", "beta", "gamma")

	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !reflect.DeepEqual(*calls, [][]string{{"daemon-reload"}}) {
		t.Fatalf("systemctl calls = %v, want daemon-reload only", *calls)
	}
	for _, unit := range tx.units {
		raw, err := os.ReadFile(unit.Path)
		if err != nil {
			t.Fatalf("ReadFile(%q): %v", unit.Path, err)
		}
		if want := "new-" + unit.Service; string(raw) != want {
			t.Fatalf("%s content = %q, want %q", unit.Service, raw, want)
		}
	}
}

func TestVMJailerUpgradeCommitRejectsReplacedStagedSymlink(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "yeet-vm-alpha.service")
	victim := filepath.Join(dir, "victim.service")
	if err := os.WriteFile(live, []byte("old-alpha"), 0o644); err != nil {
		t.Fatalf("WriteFile live unit: %v", err)
	}
	if err := os.WriteFile(victim, []byte("victim"), 0o644); err != nil {
		t.Fatalf("WriteFile victim: %v", err)
	}
	deps := defaultVMJailerUpgradeDeps()
	deps.unitUID = uint32(os.Geteuid())
	deps.unitGID = uint32(os.Getegid())
	var calls [][]string
	deps.systemctl = func(args ...string) error {
		calls = append(calls, append([]string(nil), args...))
		return nil
	}
	tx, err := prepareVMJailerUnitTransaction(context.Background(), []vmJailerUpgradeVM{{
		Service: "alpha", UnitPath: live, UnitContent: []byte("new-alpha"),
	}}, VMJailerUpgradeSummary{}, deps)
	if err != nil {
		t.Fatalf("prepare transaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Close() })
	replacement := &tx.units[0]
	if err := os.Remove(replacement.Staged); err != nil {
		t.Fatalf("Remove staged unit: %v", err)
	}
	if err := os.Symlink(victim, replacement.Staged); err != nil {
		t.Fatalf("replace staged unit with symlink: %v", err)
	}
	err = tx.Commit()
	if err == nil || !strings.Contains(err.Error(), "staged VM unit") {
		t.Fatalf("Commit error = %v, want replaced staged unit refusal", err)
	}
	info, statErr := os.Lstat(live)
	if statErr != nil {
		t.Fatalf("Lstat live unit: %v", statErr)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("live unit became a symlink")
	}
	if raw, readErr := os.ReadFile(live); readErr != nil || string(raw) != "old-alpha" {
		t.Fatalf("live content = %q, %v; want old-alpha", raw, readErr)
	}
	if !reflect.DeepEqual(calls, [][]string{{"daemon-reload"}}) {
		t.Fatalf("systemctl calls = %v, want restoration daemon-reload only", calls)
	}
}

func TestPrepareVMJailerUnitTransactionSerializesAndHonorsCancellation(t *testing.T) {
	dir := t.TempDir()
	vm := vmJailerUpgradeVM{
		Service: "alpha", UnitPath: filepath.Join(dir, "yeet-vm-alpha.service"), UnitContent: []byte("new-alpha"),
	}
	if err := os.WriteFile(vm.UnitPath, []byte("old-alpha"), 0o644); err != nil {
		t.Fatalf("WriteFile live unit: %v", err)
	}
	deps := defaultVMJailerUpgradeDeps()
	deps.unitUID = uint32(os.Geteuid())
	deps.unitGID = uint32(os.Getegid())
	first, err := prepareVMJailerUnitTransaction(context.Background(), []vmJailerUpgradeVM{vm}, VMJailerUpgradeSummary{}, deps)
	if err != nil {
		t.Fatalf("prepare first transaction: %v", err)
	}
	defer func() { _ = first.Close() }()

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := prepareVMJailerUnitTransaction(canceled, []vmJailerUpgradeVM{vm}, VMJailerUpgradeSummary{}, deps); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled transaction error = %v, want context canceled", err)
	}

	result := make(chan *VMJailerUpgrade, 1)
	errs := make(chan error, 1)
	go func() {
		tx, err := prepareVMJailerUnitTransaction(context.Background(), []vmJailerUpgradeVM{vm}, VMJailerUpgradeSummary{}, deps)
		if err != nil {
			errs <- err
			return
		}
		result <- tx
	}()
	select {
	case tx := <-result:
		_ = tx.Close()
		t.Fatal("second transaction prepared while first held unit-directory lock")
	case err := <-errs:
		t.Fatalf("second transaction failed while waiting: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first transaction: %v", err)
	}
	select {
	case tx := <-result:
		if err := tx.Close(); err != nil {
			t.Fatalf("close second transaction: %v", err)
		}
	case err := <-errs:
		t.Fatalf("second transaction failed after release: %v", err)
	case <-time.After(time.Second):
		t.Fatal("second transaction did not prepare after first released lock")
	}
}

func TestVMJailerUpgradeCommitRejectsReplacedLiveUnit(t *testing.T) {
	tx, _ := newVMJailerUnitTransaction(t, "alpha")
	t.Cleanup(func() { _ = tx.Close() })
	live := tx.units[0].Path
	if err := os.Remove(live); err != nil {
		t.Fatalf("Remove live unit: %v", err)
	}
	if err := os.WriteFile(live, []byte("concurrent-root-replacement"), 0o644); err != nil {
		t.Fatalf("replace live unit: %v", err)
	}

	err := tx.Commit()
	if err == nil || !strings.Contains(err.Error(), "live VM unit") || !strings.Contains(err.Error(), "changed before commit") {
		t.Fatalf("Commit error = %v, want live replacement refusal", err)
	}
	if raw, readErr := os.ReadFile(live); readErr != nil || string(raw) != "concurrent-root-replacement" {
		t.Fatalf("live content = %q, %v; want concurrent replacement preserved", raw, readErr)
	}
}

func TestVMJailerUpgradeRollbackRefusesReplacedInstalledUnit(t *testing.T) {
	tx, _ := newVMJailerUnitTransaction(t, "alpha")
	t.Cleanup(func() { _ = tx.Close() })
	live := tx.units[0].Path
	reloadErr := errors.New("reload failed")
	reloads := 0
	tx.deps.systemctl = func(...string) error {
		reloads++
		if reloads != 1 {
			return nil
		}
		if err := os.Remove(live); err != nil {
			t.Fatalf("Remove installed unit: %v", err)
		}
		if err := os.WriteFile(live, []byte("concurrent-root-replacement"), 0o644); err != nil {
			t.Fatalf("replace installed unit: %v", err)
		}
		return reloadErr
	}

	err := tx.Commit()
	if !errors.Is(err, reloadErr) || !strings.Contains(err.Error(), "changed after install") {
		t.Fatalf("Commit error = %v, want reload failure and rollback replacement refusal", err)
	}
	if raw, readErr := os.ReadFile(live); readErr != nil || string(raw) != "concurrent-root-replacement" {
		t.Fatalf("live content = %q, %v; want concurrent replacement preserved", raw, readErr)
	}
	if reloads != 2 {
		t.Fatalf("daemon-reload calls = %d, want failed commit reload and restoration reload", reloads)
	}
}

func TestVMJailerUpgradeRollbackExistingUnitPreservesReplacementAtExchange(t *testing.T) {
	tx, _ := newVMJailerUnitTransaction(t, "alpha")
	live := tx.units[0].Path
	reloadErr := errors.New("reload failed")
	reloads := 0
	tx.deps.systemctl = func(...string) error {
		reloads++
		if reloads == 1 {
			return reloadErr
		}
		return nil
	}
	originalExchange := tx.deps.exchangeAt
	exchanges := 0
	tx.deps.exchangeAt = func(oldDir int, oldName string, newDir int, newName string) error {
		exchanges++
		if err := originalExchange(oldDir, oldName, newDir, newName); err != nil {
			return err
		}
		if exchanges == 1 {
			if err := os.Remove(live); err != nil {
				t.Fatalf("remove restored unit after exchange: %v", err)
			}
			if err := os.WriteFile(live, []byte("concurrent-root-replacement"), 0o644); err != nil {
				t.Fatalf("replace restored unit after exchange: %v", err)
			}
		}
		return nil
	}

	err := tx.Commit()
	if !errors.Is(err, reloadErr) {
		t.Fatalf("Commit error = %v, want reload failure", err)
	}
	if raw, readErr := os.ReadFile(live); readErr != nil || string(raw) != "concurrent-root-replacement" {
		t.Fatalf("live content = %q, %v; want concurrent replacement preserved", raw, readErr)
	}
	if exchanges != 1 {
		t.Fatalf("exchange calls = %d, want one atomic restoration exchange", exchanges)
	}
}

func TestVMJailerUpgradeRollbackNewUnitPreservesReplacementAtQuarantine(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "yeet-vm-alpha.service")
	reloadErr := errors.New("reload failed")
	reloads := 0
	deps := defaultVMJailerUpgradeDeps()
	deps.unitUID = uint32(os.Geteuid())
	deps.unitGID = uint32(os.Getegid())
	deps.systemctl = func(...string) error {
		reloads++
		if reloads == 1 {
			return reloadErr
		}
		return nil
	}
	tx, err := prepareVMJailerUnitTransaction(context.Background(), []vmJailerUpgradeVM{{
		Service: "alpha", UnitPath: live, UnitContent: []byte("new-alpha"),
	}}, VMJailerUpgradeSummary{}, deps)
	if err != nil {
		t.Fatalf("prepare transaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Close() })
	originalNoReplace := tx.deps.renameNoReplaceAt
	renames := 0
	tx.deps.renameNoReplaceAt = func(oldDir int, oldName string, newDir int, newName string) error {
		renames++
		if err := originalNoReplace(oldDir, oldName, newDir, newName); err != nil {
			return err
		}
		if renames == 1 {
			if err := os.WriteFile(live, []byte("concurrent-root-replacement"), 0o644); err != nil {
				t.Fatalf("replace quarantined unit after no-replace rename: %v", err)
			}
		}
		return nil
	}

	err = tx.Commit()
	if !errors.Is(err, reloadErr) {
		t.Fatalf("Commit error = %v, want reload failure", err)
	}
	if raw, readErr := os.ReadFile(live); readErr != nil || string(raw) != "concurrent-root-replacement" {
		t.Fatalf("live content = %q, %v; want concurrent replacement preserved", raw, readErr)
	}
	if renames != 1 {
		t.Fatalf("no-replace rename calls = %d, want one quarantine rename", renames)
	}
}

func TestVMJailerUpgradeRollbackNewUnitRestoresMismatchedQuarantine(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "yeet-vm-alpha.service")
	reloadErr := errors.New("reload failed")
	reloads := 0
	deps := defaultVMJailerUpgradeDeps()
	deps.unitUID = uint32(os.Geteuid())
	deps.unitGID = uint32(os.Getegid())
	deps.systemctl = func(...string) error {
		reloads++
		if reloads != 1 {
			return nil
		}
		if err := os.Remove(live); err != nil {
			t.Fatalf("remove installed unit before rollback: %v", err)
		}
		if err := os.WriteFile(live, []byte("concurrent-root-replacement"), 0o644); err != nil {
			t.Fatalf("replace installed unit before rollback: %v", err)
		}
		return reloadErr
	}
	tx, err := prepareVMJailerUnitTransaction(context.Background(), []vmJailerUpgradeVM{{
		Service: "alpha", UnitPath: live, UnitContent: []byte("new-alpha"),
	}}, VMJailerUpgradeSummary{}, deps)
	if err != nil {
		t.Fatalf("prepare transaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Close() })

	err = tx.Commit()
	if !errors.Is(err, reloadErr) || !strings.Contains(err.Error(), "changed after install") {
		t.Fatalf("Commit error = %v, want reload failure and mismatched quarantine refusal", err)
	}
	if raw, readErr := os.ReadFile(live); readErr != nil || string(raw) != "concurrent-root-replacement" {
		t.Fatalf("live content = %q, %v; want quarantined replacement restored", raw, readErr)
	}
}

func TestVMJailerUpgradeCommitUsesBoundUnitDirectory(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "systemd")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("Mkdir systemd dir: %v", err)
	}
	live := filepath.Join(dir, "yeet-vm-alpha.service")
	if err := os.WriteFile(live, []byte("old-alpha"), 0o644); err != nil {
		t.Fatalf("WriteFile live unit: %v", err)
	}
	deps := defaultVMJailerUpgradeDeps()
	deps.unitUID = uint32(os.Geteuid())
	deps.unitGID = uint32(os.Getegid())
	deps.systemctl = func(...string) error { return nil }
	tx, err := prepareVMJailerUnitTransaction(context.Background(), []vmJailerUpgradeVM{{
		Service: "alpha", UnitPath: live, UnitContent: []byte("new-alpha"),
	}}, VMJailerUpgradeSummary{}, deps)
	if err != nil {
		t.Fatalf("prepare transaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Close() })

	boundDir := filepath.Join(base, "bound-systemd")
	if err := os.Rename(dir, boundDir); err != nil {
		t.Fatalf("rename bound directory: %v", err)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("create path replacement directory: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if raw, readErr := os.ReadFile(filepath.Join(boundDir, filepath.Base(live))); readErr != nil || string(raw) != "new-alpha" {
		t.Fatalf("bound live content = %q, %v; want new-alpha", raw, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, filepath.Base(live))); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("replacement path was mutated: %v", statErr)
	}
}

func TestVMJailerUpgradeCommitRestoresEarlierUnitsAfterSecondRenameFailure(t *testing.T) {
	tx, calls := newVMJailerUnitTransaction(t, "alpha", "beta", "gamma")
	renameErr := errors.New("second rename failed")
	renames := 0
	tx.deps.renameAt = func(oldDir int, oldName string, newDir int, newName string) error {
		renames++
		if renames == 2 {
			return renameErr
		}
		return unix.Renameat(oldDir, oldName, newDir, newName)
	}

	err := tx.Commit()
	if !errors.Is(err, renameErr) {
		t.Fatalf("Commit error = %v, want %v", err, renameErr)
	}
	for _, unit := range tx.units {
		raw, readErr := os.ReadFile(unit.Path)
		if readErr != nil {
			t.Fatalf("ReadFile(%q): %v", unit.Path, readErr)
		}
		if want := "old-" + unit.Service; string(raw) != want {
			t.Fatalf("%s content = %q, want restored %q", unit.Service, raw, want)
		}
	}
	if !reflect.DeepEqual(*calls, [][]string{{"daemon-reload"}}) {
		t.Fatalf("systemctl calls = %v, want one daemon-reload after restoration", *calls)
	}
}

func TestVMJailerUpgradeCommitRestoresAllUnitsAfterDaemonReloadFailure(t *testing.T) {
	tx, calls := newVMJailerUnitTransaction(t, "alpha", "beta")
	reloadErr := errors.New("daemon reload failed")
	reloads := 0
	tx.deps.systemctl = func(args ...string) error {
		*calls = append(*calls, append([]string(nil), args...))
		reloads++
		if reloads == 1 {
			return reloadErr
		}
		return nil
	}

	err := tx.Commit()
	if !errors.Is(err, reloadErr) {
		t.Fatalf("Commit error = %v, want %v", err, reloadErr)
	}
	for _, unit := range tx.units {
		raw, readErr := os.ReadFile(unit.Path)
		if readErr != nil {
			t.Fatalf("ReadFile(%q): %v", unit.Path, readErr)
		}
		if want := "old-" + unit.Service; string(raw) != want {
			t.Fatalf("%s content = %q, want restored %q", unit.Service, raw, want)
		}
	}
	if !reflect.DeepEqual(*calls, [][]string{{"daemon-reload"}, {"daemon-reload"}}) {
		t.Fatalf("systemctl calls = %v, want failed reload followed by restoration reload", *calls)
	}
}

func TestVMJailerUpgradeCommitRemovesNewUnitAfterDaemonReloadFailure(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "yeet-vm-alpha.service")
	reloadErr := errors.New("daemon reload failed")
	reloads := 0
	deps := defaultVMJailerUpgradeDeps()
	deps.unitUID = uint32(os.Geteuid())
	deps.unitGID = uint32(os.Getegid())
	deps.systemctl = func(...string) error {
		reloads++
		if reloads == 1 {
			return reloadErr
		}
		return nil
	}
	tx, err := prepareVMJailerUnitTransaction(context.Background(), []vmJailerUpgradeVM{{
		Service: "alpha", UnitPath: live, UnitContent: []byte("new-alpha"),
	}}, VMJailerUpgradeSummary{}, deps)
	if err != nil {
		t.Fatalf("prepare transaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Close() })

	if err := tx.Commit(); !errors.Is(err, reloadErr) {
		t.Fatalf("Commit error = %v, want %v", err, reloadErr)
	}
	if _, err := os.Stat(live); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new live unit remains after rollback: %v", err)
	}
	if reloads != 2 {
		t.Fatalf("daemon-reload calls = %d, want failed commit reload and restoration reload", reloads)
	}
}

func TestVMJailerUpgradeCommitJoinsOriginalAndRollbackErrors(t *testing.T) {
	tx, _ := newVMJailerUnitTransaction(t, "alpha", "beta")
	renameErr := errors.New("replace beta")
	restoreErr := errors.New("restore alpha")
	reloadErr := errors.New("reload restored units")
	renames := 0
	tx.deps.renameAt = func(oldDir int, oldName string, newDir int, newName string) error {
		renames++
		if renames == 2 {
			return renameErr
		}
		return unix.Renameat(oldDir, oldName, newDir, newName)
	}
	tx.deps.restoreUnitAt = func(*os.File, string, vmJailerFileIdentity, []byte, os.FileMode, uint32, uint32, func(int, string, int, string) error, func(int, string, int) error) error {
		return restoreErr
	}
	tx.deps.systemctl = func(...string) error { return reloadErr }

	err := tx.Commit()
	if !errors.Is(err, renameErr) || !errors.Is(err, restoreErr) || !errors.Is(err, reloadErr) {
		t.Fatalf("Commit error = %v, want original, restoration, and reload errors", err)
	}
}

func TestVMJailerUpgradeCommitAndCloseAreIdempotent(t *testing.T) {
	tx, calls := newVMJailerUnitTransaction(t, "alpha")
	if err := tx.Commit(); err != nil {
		t.Fatalf("first Commit: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("second Commit: %v", err)
	}
	if err := tx.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := tx.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if !reflect.DeepEqual(*calls, [][]string{{"daemon-reload"}}) {
		t.Fatalf("systemctl calls = %v, want one daemon-reload", *calls)
	}
}

func TestVMJailerUpgradeCommitFailureIsIdempotent(t *testing.T) {
	tests := []struct {
		name             string
		configure        func(*VMJailerUpgrade, *int, *int, *int)
		wantRenameCalls  int
		wantReloadCalls  int
		wantRestoreCalls int
	}{
		{
			name: "rename failure",
			configure: func(tx *VMJailerUpgrade, renameCalls, _, _ *int) {
				tx.deps.renameAt = func(int, string, int, string) error {
					*renameCalls++
					return errors.New("rename failed")
				}
			},
			wantRenameCalls: 1,
			wantReloadCalls: 1,
		},
		{
			name: "daemon reload failure",
			configure: func(tx *VMJailerUpgrade, _, reloadCalls, restoreCalls *int) {
				tx.deps.systemctl = func(...string) error {
					*reloadCalls++
					if *reloadCalls == 1 {
						return errors.New("reload failed")
					}
					return nil
				}
				originalRestore := tx.deps.restoreUnitAt
				tx.deps.restoreUnitAt = func(dir *os.File, name string, installedID vmJailerFileIdentity, raw []byte, mode os.FileMode, uid, gid uint32, exchangeAt func(int, string, int, string) error, unlinkAt func(int, string, int) error) error {
					*restoreCalls++
					return originalRestore(dir, name, installedID, raw, mode, uid, gid, exchangeAt, unlinkAt)
				}
			},
			wantRenameCalls:  1,
			wantReloadCalls:  2,
			wantRestoreCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx, _ := newVMJailerUnitTransaction(t, "alpha")
			renameCalls := 0
			reloadCalls := 0
			restoreCalls := 0
			originalRename := tx.deps.renameAt
			tx.deps.renameAt = func(oldDir int, oldName string, newDir int, newName string) error {
				renameCalls++
				return originalRename(oldDir, oldName, newDir, newName)
			}
			originalSystemctl := tx.deps.systemctl
			tx.deps.systemctl = func(args ...string) error {
				reloadCalls++
				return originalSystemctl(args...)
			}
			tt.configure(tx, &renameCalls, &reloadCalls, &restoreCalls)

			first := tx.Commit()
			if first == nil {
				t.Fatal("first Commit returned nil, want failure")
			}
			second := tx.Commit()
			if second != first {
				t.Fatalf("second Commit error = %v (%p), want identical first error %v (%p)", second, second, first, first)
			}
			if renameCalls != tt.wantRenameCalls {
				t.Fatalf("rename calls = %d, want %d", renameCalls, tt.wantRenameCalls)
			}
			if reloadCalls != tt.wantReloadCalls {
				t.Fatalf("daemon-reload calls = %d, want %d", reloadCalls, tt.wantReloadCalls)
			}
			if restoreCalls != tt.wantRestoreCalls {
				t.Fatalf("restore calls = %d, want %d", restoreCalls, tt.wantRestoreCalls)
			}
		})
	}
}

func TestVMJailerUpgradeEmptyTransactionDoesNotReloadSystemd(t *testing.T) {
	called := false
	tx := &VMJailerUpgrade{deps: vmJailerUpgradeDeps{systemctl: func(...string) error {
		called = true
		return nil
	}}}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := tx.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if called {
		t.Fatal("empty transaction called systemctl")
	}
}

func TestVMJailerUpgradeCloseRemovesStagedSymlinkWithoutFollowingIt(t *testing.T) {
	tx, _ := newVMJailerUnitTransaction(t, "alpha")
	dir := filepath.Dir(tx.units[0].Path)
	victim := filepath.Join(dir, "victim")
	staged := tx.units[0].Staged
	if err := os.WriteFile(victim, []byte("keep"), 0o644); err != nil {
		t.Fatalf("WriteFile victim: %v", err)
	}
	if err := os.Remove(staged); err != nil {
		t.Fatalf("Remove staged unit: %v", err)
	}
	if err := os.Symlink(victim, staged); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if err := tx.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Lstat(staged); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged symlink still exists: %v", err)
	}
	raw, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("ReadFile victim: %v", err)
	}
	if string(raw) != "keep" {
		t.Fatalf("victim content = %q, want keep", raw)
	}
}

func TestVMJailerUpgradeCloseJoinsCleanupErrors(t *testing.T) {
	firstErr := errors.New("remove first")
	secondErr := errors.New("remove second")
	tx, _ := newVMJailerUnitTransaction(t, "alpha", "beta")
	firstName := tx.units[0].stagedName
	tx.deps.unlinkAt = func(_ int, name string, _ int) error {
		if name == firstName {
			return firstErr
		}
		return secondErr
	}

	err := tx.Close()
	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("Close error = %v, want joined cleanup errors", err)
	}
	if err := tx.Close(); err != nil {
		t.Fatalf("second Close = %v, want nil", err)
	}
}

func TestVMJailerUpgradeSummaryReturnsCopies(t *testing.T) {
	tx := &VMJailerUpgrade{summary: VMJailerUpgradeSummary{
		Ready:          []string{"alpha"},
		PendingRestart: []string{"beta"},
	}}
	got := tx.Summary()
	got.Ready[0] = "changed"
	got.PendingRestart[0] = "changed"

	want := VMJailerUpgradeSummary{Ready: []string{"alpha"}, PendingRestart: []string{"beta"}}
	if next := tx.Summary(); !reflect.DeepEqual(next, want) {
		t.Fatalf("Summary after caller mutation = %#v, want %#v", next, want)
	}
}

func newVMJailerUnitTransaction(t *testing.T, services ...string) (*VMJailerUpgrade, *[][]string) {
	t.Helper()
	dir := t.TempDir()
	vms := make([]vmJailerUpgradeVM, 0, len(services))
	for _, service := range services {
		live := filepath.Join(dir, "yeet-vm-"+service+".service")
		previous := []byte("old-" + service)
		if err := os.WriteFile(live, previous, 0o644); err != nil {
			t.Fatalf("WriteFile live unit: %v", err)
		}
		vms = append(vms, vmJailerUpgradeVM{
			Service: service, UnitPath: live, UnitContent: []byte("new-" + service),
		})
	}
	var calls [][]string
	deps := defaultVMJailerUpgradeDeps()
	deps.unitUID = uint32(os.Geteuid())
	deps.unitGID = uint32(os.Getegid())
	deps.systemctl = func(args ...string) error {
		calls = append(calls, append([]string(nil), args...))
		return nil
	}
	tx, err := prepareVMJailerUnitTransaction(context.Background(), vms, VMJailerUpgradeSummary{}, deps)
	if err != nil {
		t.Fatalf("prepare VM jailer unit transaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Close() })
	return tx, &calls
}

type vmJailerUpgradeIdentityFixture struct {
	cfg        *Config
	deps       vmJailerUpgradeDeps
	systemdDir string
	unitPath   string
}

func newVMJailerUpgradeIdentityFixture(t *testing.T) vmJailerUpgradeIdentityFixture {
	t.Helper()
	dataRoot := t.TempDir()
	servicesRoot := filepath.Join(dataRoot, "services")
	systemdDir := filepath.Join(dataRoot, "systemd")
	if err := os.Mkdir(systemdDir, 0o700); err != nil {
		t.Fatalf("Mkdir systemd: %v", err)
	}
	store := db.NewStore(filepath.Join(dataRoot, "db.json"), servicesRoot)
	cfg := &Config{RootDir: dataRoot, ServicesRoot: servicesRoot, DB: store}
	rootFS := writeVMJailerUpgradeTargetBundle(t, filepath.Join(dataRoot, "vm-images", "alpha-v1"), "alpha-v1", "amd64")
	addTestServices(t, &Server{cfg: *cfg}, db.Service{
		Name: "alpha", ServiceType: db.ServiceTypeVM,
		VM: &db.VMConfig{
			Runtime: vmRuntimeFirecracker,
			Image:   db.VMImageConfig{Payload: "vm://custom/alpha", Version: "alpha-v1", RootFS: rootFS},
		},
	})
	unitPath := filepath.Join(systemdDir, vmSystemdUnitName("alpha"))
	if err := os.WriteFile(unitPath, []byte("old-alpha"), 0o644); err != nil {
		t.Fatalf("WriteFile live unit: %v", err)
	}
	oldSystemdDir := vmSystemdSystemDir
	vmSystemdSystemDir = systemdDir
	t.Cleanup(func() { vmSystemdSystemDir = oldSystemdDir })

	deps := defaultVMJailerUpgradeDeps()
	deps.sibling = func(_ context.Context, vm vmJailerUpgradeVM) (string, bool, error) {
		return vm.Jailer, true, nil
	}
	deps.readiness = func(string) (vmJailerReadiness, error) { return vmJailerPendingRestart, nil }
	deps.isRunning = func(*Server, string) (bool, error) { return false, nil }
	deps.renderUnit = func(vmSystemdConfig) (string, error) { return "new-alpha", nil }
	deps.unitUID = uint32(os.Geteuid())
	deps.unitGID = uint32(os.Getegid())
	return vmJailerUpgradeIdentityFixture{cfg: cfg, deps: deps, systemdDir: systemdDir, unitPath: unitPath}
}

func vmJailerUpgradeEntryNames(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestResolveVMUpgradeJailer(t *testing.T) {
	tests := []struct {
		name       string
		configure  func(*vmJailerUpgradeDeps)
		wantSource string
		wantErr    string
	}{
		{
			name: "valid sibling",
			configure: func(d *vmJailerUpgradeDeps) {
				d.sibling = func(context.Context, vmJailerUpgradeVM) (string, bool, error) {
					return "/images/v1/jailer", true, nil
				}
			},
			wantSource: "sibling",
		},
		{
			name: "verified managed cache",
			configure: func(d *vmJailerUpgradeDeps) {
				d.cached = func(context.Context, vmJailerUpgradeVM) (vmJailerCandidate, bool, error) {
					return vmJailerCandidate{Path: "/cache/v2/jailer"}, true, nil
				}
			},
			wantSource: "cache",
		},
		{
			name:       "official manifest downloads only jailer",
			configure:  func(*vmJailerUpgradeDeps) {},
			wantSource: "remote",
		},
		{
			name: "mismatched version fails",
			configure: func(d *vmJailerUpgradeDeps) {
				d.sibling = func(context.Context, vmJailerUpgradeVM) (string, bool, error) {
					return "", true, errors.New("jailer version does not match")
				}
			},
			wantErr: "does not match",
		},
		{
			name: "custom image without jailer fails",
			configure: func(d *vmJailerUpgradeDeps) {
				d.localPayload = func(string) bool { return true }
			},
			wantErr: "re-import",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			officialCalls := 0
			installCalls := 0
			vm := vmJailerUpgradeVM{
				Service:      "devbox",
				Payload:      "vm://ubuntu/26.04",
				ImageVersion: "ubuntu-26.04-amd64-v1",
				Architecture: "amd64",
				Firecracker:  "/images/v1/firecracker",
				Jailer:       "/images/v1/jailer",
			}
			deps := vmJailerUpgradeDeps{
				sibling: func(context.Context, vmJailerUpgradeVM) (string, bool, error) {
					return "", false, nil
				},
				cached: func(context.Context, vmJailerUpgradeVM) (vmJailerCandidate, bool, error) {
					return vmJailerCandidate{}, false, nil
				},
				localPayload: func(string) bool { return false },
				official: func(context.Context, vmJailerUpgradeVM) (vmJailerCandidate, error) {
					officialCalls++
					return vmJailerCandidate{Path: "/stage/jailer"}, nil
				},
				install: func(_ context.Context, gotVM vmJailerUpgradeVM, _ vmJailerCandidate) (string, error) {
					installCalls++
					return filepath.Join(filepath.Dir(gotVM.Firecracker), "jailer"), nil
				},
			}
			tt.configure(&deps)

			path, source, err := resolveVMUpgradeJailer(context.Background(), vm, deps)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				if tt.name == "custom image without jailer fails" && (officialCalls != 0 || installCalls != 0) {
					t.Fatalf("custom payload called official=%d install=%d, want neither", officialCalls, installCalls)
				}
				return
			}
			wantPath := filepath.Join(filepath.Dir(vm.Firecracker), "jailer")
			if err != nil || source != tt.wantSource || path != wantPath {
				t.Fatalf("path, source, error = %q, %q, %v; want %q, %q, nil", path, source, err, wantPath, tt.wantSource)
			}
		})
	}
}

func TestCachedVMUpgradeJailerNormalizedArchitectureMismatchIsNonCandidate(t *testing.T) {
	err := classifyCachedVMUpgradeJailerArchitecture("arm64", "amd64")
	if !errors.Is(err, errVMJailerUpgradeIncompatibleCacheCandidate) {
		t.Fatalf("architecture classification error = %v, want incompatible cache candidate", err)
	}
}

func TestResolveVMUpgradeJailerFallsBackToOfficialForCachedRuntimeVersionMismatch(t *testing.T) {
	cacheRoot := t.TempDir()
	writeVMJailerUpgradeManagedBundle(t, cacheRoot, "ubuntu-26.04-amd64-v2", "amd64")
	assertVMJailerUpgradeUsesOfficialCandidate(t, cacheRoot, func(_ context.Context, firecracker, _ string) error {
		if firecracker == "/images/v1/firecracker" {
			return validateVMJailerPairVersion("Firecracker v1.7.0", "jailer v1.8.0")
		}
		return nil
	})
}

func assertVMJailerUpgradeUsesOfficialCandidate(t *testing.T, cacheRoot string, validatePair func(context.Context, string, string) error) {
	t.Helper()
	vm := vmJailerUpgradeVM{
		Service:      "devbox",
		Payload:      testUbuntuVMPayload,
		ImageVersion: "ubuntu-26.04-amd64-v1",
		Architecture: "amd64",
		Firecracker:  "/images/v1/firecracker",
	}
	officialCandidate := vmJailerCandidate{
		Path:         "/official/jailer",
		ArtifactName: "jailer",
		SHA256:       testSHA256Hex([]byte("official jailer")),
		Architecture: "amd64",
	}
	officialCalls := 0
	var installed vmJailerCandidate
	deps := vmJailerUpgradeDeps{
		sibling: func(context.Context, vmJailerUpgradeVM) (string, bool, error) {
			return "", false, nil
		},
		cached: func(ctx context.Context, got vmJailerUpgradeVM) (vmJailerCandidate, bool, error) {
			return cachedVMUpgradeJailerCandidate(ctx, got, cacheRoot, validatePair)
		},
		localPayload: func(string) bool { return false },
		official: func(context.Context, vmJailerUpgradeVM) (vmJailerCandidate, error) {
			officialCalls++
			return officialCandidate, nil
		},
		install: func(_ context.Context, _ vmJailerUpgradeVM, candidate vmJailerCandidate) (string, error) {
			installed = candidate
			return "/images/v1/jailer", nil
		},
	}

	path, source, err := resolveVMUpgradeJailer(context.Background(), vm, deps)
	if err != nil {
		t.Fatalf("resolveVMUpgradeJailer: %v", err)
	}
	if path != "/images/v1/jailer" || source != "remote" {
		t.Fatalf("path, source = %q, %q; want official install", path, source)
	}
	if officialCalls != 1 || !reflect.DeepEqual(installed, officialCandidate) {
		t.Fatalf("official calls, installed candidate = %d, %#v; want 1, %#v", officialCalls, installed, officialCandidate)
	}
}

func TestResolveVMUpgradeJailerRejectsInvalidCachedCandidateWithoutOfficialFallback(t *testing.T) {
	tests := []struct {
		name                  string
		candidateVersion      string
		candidateArchitecture string
		tamper                bool
		validate              func(int) error
		wantErr               string
	}{
		{
			name:     "corrupt checksum",
			tamper:   true,
			validate: func(int) error { return nil },
			wantErr:  "checksum mismatch",
		},
		{
			name: "unsafe path",
			validate: func(call int) error {
				if call == 1 {
					return errors.New("trusted VM runtime input contains a symbolic link")
				}
				return nil
			},
			wantErr: "symbolic link",
		},
		{
			name: "version probe failure",
			validate: func(call int) error {
				if call == 1 {
					return errors.New("read VM runtime version: permission denied")
				}
				return nil
			},
			wantErr: "permission denied",
		},
		{
			name: "unparseable target version",
			validate: func(call int) error {
				if call == 2 {
					return validateVMJailerPairVersion("not a version", "jailer v1.8.0")
				}
				return nil
			},
			wantErr: "read Firecracker/jailer version",
		},
		{
			name:                  "invalid architecture",
			candidateArchitecture: "not-an-architecture",
			validate:              func(int) error { return nil },
			wantErr:               "unsupported VM image architecture",
		},
		{
			name:             "target identity runtime mismatch",
			candidateVersion: "ubuntu-26.04-amd64-v1",
			validate: func(call int) error {
				if call == 2 {
					return validateVMJailerPairVersion("Firecracker v1.7.0", "jailer v1.8.0")
				}
				return nil
			},
			wantErr: "does not match",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cacheRoot := t.TempDir()
			candidateVersion := tt.candidateVersion
			if candidateVersion == "" {
				candidateVersion = "ubuntu-26.04-amd64-v2"
			}
			candidateArchitecture := tt.candidateArchitecture
			if candidateArchitecture == "" {
				candidateArchitecture = "amd64"
			}
			_, jailer, _ := writeVMJailerUpgradeManagedBundle(t, cacheRoot, candidateVersion, candidateArchitecture)
			if tt.tamper {
				if err := os.WriteFile(jailer, []byte("tampered"), 0o755); err != nil {
					t.Fatalf("tamper jailer: %v", err)
				}
			}
			vm := vmJailerUpgradeVM{
				Service:      "devbox",
				Payload:      testUbuntuVMPayload,
				ImageVersion: "ubuntu-26.04-amd64-v1",
				Architecture: "amd64",
				Firecracker:  "/images/v1/firecracker",
			}
			pairCalls := 0
			officialCalls := 0
			deps := vmJailerUpgradeDeps{
				sibling: func(context.Context, vmJailerUpgradeVM) (string, bool, error) {
					return "", false, nil
				},
				cached: func(ctx context.Context, got vmJailerUpgradeVM) (vmJailerCandidate, bool, error) {
					return cachedVMUpgradeJailerCandidate(ctx, got, cacheRoot, func(context.Context, string, string) error {
						pairCalls++
						return tt.validate(pairCalls)
					})
				},
				localPayload: func(string) bool { return false },
				official: func(context.Context, vmJailerUpgradeVM) (vmJailerCandidate, error) {
					officialCalls++
					return vmJailerCandidate{Path: "/official/jailer"}, nil
				},
				install: func(context.Context, vmJailerUpgradeVM, vmJailerCandidate) (string, error) {
					t.Fatal("invalid cached candidate reached install")
					return "", nil
				},
			}

			_, _, err := resolveVMUpgradeJailer(context.Background(), vm, deps)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
			if officialCalls != 0 {
				t.Fatalf("official resolver calls = %d, want 0", officialCalls)
			}
		})
	}
}

func TestPlanVMJailerUpgradeInventory(t *testing.T) {
	dataRoot := t.TempDir()
	servicesRoot := filepath.Join(dataRoot, "configured-services")
	store := db.NewStore(filepath.Join(dataRoot, "db.json"), servicesRoot)
	cfg := &Config{DB: store, RootDir: dataRoot, ServicesRoot: servicesRoot}

	defaultRoot := filepath.Join(servicesRoot, "zeta")
	customRoot := filepath.Join(dataRoot, "custom", "alpha")
	zfsRoot := filepath.Join(dataRoot, "mounted-zfs", "middle")
	zetaRootFS := writeVMJailerUpgradeTargetBundle(t, filepath.Join(dataRoot, "vm-images", "zeta-v1"), "zeta-v1", "amd64")
	alphaRootFS := writeVMJailerUpgradeTargetBundle(t, filepath.Join(dataRoot, "vm-images", "alpha-v1"), "alpha-v1", "x86_64")
	middleRootFS := writeVMJailerUpgradeTargetBundle(t, filepath.Join(dataRoot, "vm-images", "middle-v1"), "middle-v1", "amd64")

	vmService := func(name, payload, version, rootFS, root, rootZFS, disk string) db.Service {
		effectiveRoot := root
		if effectiveRoot == "" {
			effectiveRoot = filepath.Join(servicesRoot, name)
		}
		runDir := serviceRunDirForRoot(effectiveRoot)
		return db.Service{
			Name:           name,
			ServiceType:    db.ServiceTypeVM,
			ServiceRoot:    root,
			ServiceRootZFS: rootZFS,
			VM: &db.VMConfig{
				Runtime: vmRuntimeFirecracker,
				Image: db.VMImageConfig{
					Payload: payload,
					Version: version,
					RootFS:  rootFS,
				},
				Disk:    db.VMDiskConfig{Backend: vmDiskBackendRaw, Path: disk},
				Console: db.VMConsoleConfig{SocketPath: filepath.Join(runDir, "stored-serial.sock")},
				Sockets: db.VMSocketConfig{
					APISocketPath:   filepath.Join(runDir, "stored-api.sock"),
					VsockSocketPath: filepath.Join(runDir, "stored-vsock.sock"),
				},
			},
		}
	}
	services := []db.Service{
		vmService("zeta", "vm://ubuntu/26.04", "zeta-v1", zetaRootFS, "", "", ""),
		vmService("alpha", "vm://custom/alpha", "alpha-v1", alphaRootFS, customRoot, "", filepath.Join(customRoot, "data", "alpha.raw")),
		vmService("middle", "vm://ubuntu/26.04", "middle-v1", middleRootFS, zfsRoot, "tank/vms/middle", filepath.Join(zfsRoot, "data", "middle.raw")),
		{Name: "not-a-vm", ServiceType: db.ServiceTypeSystemd},
		{Name: "invalid-vm", ServiceType: db.ServiceTypeVM},
		{Name: CatchService, ServiceType: db.ServiceTypeSystemd, ServiceRoot: filepath.Join(dataRoot, "custom-catch")},
	}
	addTestServices(t, &Server{cfg: *cfg}, services...)

	readiness := map[string]vmJailerReadiness{
		defaultRoot: vmJailerPendingRestart,
		customRoot:  vmJailerReady,
		zfsRoot:     vmJailerPendingRestart,
	}
	running := map[string]bool{"alpha": true, "middle": false, "zeta": true}
	var rendered []vmSystemdConfig
	deps := vmJailerUpgradeDeps{
		sibling: func(_ context.Context, vm vmJailerUpgradeVM) (string, bool, error) {
			return vm.Jailer, true, nil
		},
		cached: func(context.Context, vmJailerUpgradeVM) (vmJailerCandidate, bool, error) {
			return vmJailerCandidate{}, false, nil
		},
		localPayload: func(string) bool { return false },
		official: func(context.Context, vmJailerUpgradeVM) (vmJailerCandidate, error) {
			return vmJailerCandidate{}, errors.New("unexpected official fetch")
		},
		install: func(context.Context, vmJailerUpgradeVM, vmJailerCandidate) (string, error) {
			return "", errors.New("unexpected install")
		},
		readiness: func(root string) (vmJailerReadiness, error) {
			got, ok := readiness[root]
			if !ok {
				return "", errors.New("unexpected service root " + root)
			}
			return got, nil
		},
		isRunning: func(_ *Server, service string) (bool, error) {
			got, ok := running[service]
			if !ok {
				return false, errors.New("unexpected running check " + service)
			}
			return got, nil
		},
		renderUnit: func(cfg vmSystemdConfig) (string, error) {
			rendered = append(rendered, cfg)
			return renderVMSystemdUnit(cfg)
		},
	}

	plan, err := planVMJailerUpgrade(context.Background(), cfg, deps)
	if err != nil {
		t.Fatalf("planVMJailerUpgrade: %v", err)
	}
	if got := vmJailerUpgradeServiceNames(plan.VMs); !reflect.DeepEqual(got, []string{"alpha", "middle", "zeta"}) {
		t.Fatalf("service order = %v", got)
	}
	if !reflect.DeepEqual(plan.Summary.Ready, []string{"alpha"}) || !reflect.DeepEqual(plan.Summary.PendingRestart, []string{"middle", "zeta"}) {
		t.Fatalf("summary = %#v", plan.Summary)
	}
	if len(rendered) != 3 {
		t.Fatalf("render calls = %d, want 3", len(rendered))
	}

	byName := map[string]vmJailerUpgradeVM{}
	for _, vm := range plan.VMs {
		byName[vm.Service] = vm
		unit := string(vm.UnitContent)
		wantNetworkEnsure := "-data-dir " + dataRoot + " -services-root " + servicesRoot + " vm-network-ensure " + vm.Service
		if !strings.Contains(unit, wantNetworkEnsure) {
			t.Fatalf("%s unit missing configured services root %q:\n%s", vm.Service, wantNetworkEnsure, unit)
		}
		if wantServiceRoot := "--service-root " + vm.ServiceRoot; !strings.Contains(unit, wantServiceRoot) {
			t.Fatalf("%s unit missing effective per-VM service root %q:\n%s", vm.Service, wantServiceRoot, unit)
		}
		if vm.UnitPath != filepath.Join(vmSystemdSystemDir, vmSystemdUnitName(vm.Service)) {
			t.Fatalf("%s unit path = %q", vm.Service, vm.UnitPath)
		}
	}
	if got := byName["zeta"]; got.ServiceRoot != defaultRoot || got.Disk != zetaRootFS || got.Architecture != "amd64" || !got.Running || got.Readiness != vmJailerPendingRestart {
		t.Fatalf("default-root VM = %#v", got)
	}
	if got := byName["alpha"]; got.ServiceRoot != customRoot || got.Disk != filepath.Join(customRoot, "data", "alpha.raw") || got.Architecture != "amd64" || !got.Running || got.Readiness != vmJailerReady {
		t.Fatalf("custom-root VM = %#v", got)
	}
	if got := byName["middle"]; got.ServiceRoot != zfsRoot || got.Disk != filepath.Join(zfsRoot, "data", "middle.raw") || got.Running || got.Readiness != vmJailerPendingRestart {
		t.Fatalf("ZFS-root VM = %#v", got)
	}

	renderedByName := map[string]vmSystemdConfig{}
	for _, unit := range rendered {
		renderedByName[unit.Service] = unit
	}
	for name, vm := range byName {
		unit := renderedByName[name]
		runDir := serviceRunDirForRoot(vm.ServiceRoot)
		if unit.Runner != filepath.Join(dataRoot, "custom-catch", "run", "catch") || unit.DataDir != dataRoot || unit.ServicesRoot != servicesRoot || unit.ServiceRoot != vm.ServiceRoot || unit.DiskPath != vm.Disk {
			t.Fatalf("%s rendered roots = %#v", name, unit)
		}
		if unit.Firecracker != vm.Firecracker || unit.Jailer != vm.Jailer || unit.JailerBase != vmJailerBaseForDataRoot(dataRoot) {
			t.Fatalf("%s rendered runtime = %#v", name, unit)
		}
		if unit.ConfigPath != filepath.Join(runDir, "firecracker.json") || unit.APISocket != filepath.Join(runDir, "stored-api.sock") || unit.ConsoleSocket != filepath.Join(runDir, "stored-serial.sock") || unit.VsockSocket != filepath.Join(runDir, "stored-vsock.sock") {
			t.Fatalf("%s rendered sockets = %#v", name, unit)
		}
	}
}

func TestInspectVMJailerUpgradeRuntimeSupportsLegacyBundleWithoutManifest(t *testing.T) {
	dir := t.TempDir()
	firecracker := filepath.Join(dir, "firecracker")
	if err := os.WriteFile(firecracker, []byte("legacy firecracker"), 0o755); err != nil {
		t.Fatalf("write legacy Firecracker: %v", err)
	}
	rootFS := filepath.Join(dir, "rootfs.ext4")
	service := db.Service{VM: &db.VMConfig{
		Image: db.VMImageConfig{Version: "ubuntu-26.04-amd64-v11", RootFS: rootFS},
		Disk:  db.VMDiskConfig{Path: "/dev/zvol/tank/vms/devbox/root"},
	}}

	got, err := inspectVMJailerUpgradeRuntime(service, "amd64")
	if err != nil {
		t.Fatalf("inspectVMJailerUpgradeRuntime: %v", err)
	}
	if got.firecracker != firecracker || got.disk != service.VM.Disk.Path {
		t.Fatalf("runtime paths = %#v", got)
	}
	if got.manifest.Version != service.VM.Image.Version || got.manifest.Architecture != "amd64" || got.manifest.Firecracker != "firecracker" {
		t.Fatalf("legacy runtime identity = %#v", got.manifest)
	}
}

func TestInspectVMJailerUpgradeRuntimeRejectsMissingLegacyBundleInputs(t *testing.T) {
	tests := []struct {
		name    string
		rootFS  func(string) string
		wantErr string
	}{
		{
			name: "missing bundle directory",
			rootFS: func(root string) string {
				return filepath.Join(root, "missing", "rootfs.ext4")
			},
			wantErr: "inspect legacy VM image bundle",
		},
		{
			name: "missing Firecracker",
			rootFS: func(root string) string {
				dir := filepath.Join(root, "bundle")
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatalf("mkdir bundle: %v", err)
				}
				return filepath.Join(dir, "rootfs.ext4")
			},
			wantErr: "inspect legacy VM image Firecracker",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := db.Service{VM: &db.VMConfig{Image: db.VMImageConfig{
				Version: "ubuntu-26.04-amd64-v11",
				RootFS:  tt.rootFS(t.TempDir()),
			}}}
			_, err := inspectVMJailerUpgradeRuntime(service, "amd64")
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestInspectVMJailerUpgradeRuntimeDoesNotTreatInvalidManifestAsLegacy(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "firecracker"), []byte("legacy firecracker"), 0o755); err != nil {
		t.Fatalf("write Firecracker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte("not json"), 0o644); err != nil {
		t.Fatalf("write invalid manifest: %v", err)
	}
	service := db.Service{VM: &db.VMConfig{Image: db.VMImageConfig{
		Version: "ubuntu-26.04-amd64-v11",
		RootFS:  filepath.Join(dir, "rootfs.ext4"),
	}}}

	_, err := inspectVMJailerUpgradeRuntime(service, "amd64")
	if err == nil || !strings.Contains(err.Error(), "decode VM image runtime manifest") {
		t.Fatalf("error = %v, want invalid manifest failure", err)
	}
}

func TestInspectVMJailerUpgradeRuntimeDoesNotTreatMissingDeclaredFirecrackerAsLegacy(t *testing.T) {
	dir := t.TempDir()
	manifest := vmImageTestManifest("ubuntu-26.04-amd64-v11", vmImageTestContents())
	if err := writeManifestFile(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	service := db.Service{VM: &db.VMConfig{Image: db.VMImageConfig{
		Version: manifest.Version,
		RootFS:  filepath.Join(dir, manifest.RootFS),
	}}}

	_, err := inspectVMJailerUpgradeRuntime(service, "amd64")
	if err == nil || !strings.Contains(err.Error(), "verify VM image runtime Firecracker") {
		t.Fatalf("error = %v, want missing declared Firecracker failure", err)
	}
}

func TestCachedVMUpgradeJailerCandidateVerifiesManifestAndRuntimePair(t *testing.T) {
	cacheRoot := t.TempDir()
	candidateFirecracker, candidateJailer, candidateChecksum := writeVMJailerUpgradeManagedBundle(t, cacheRoot, "ubuntu-26.04-amd64-v2", "amd64")
	targetFirecracker := filepath.Join(t.TempDir(), "firecracker")
	vm := vmJailerUpgradeVM{
		Service:      "devbox",
		ImageVersion: "ubuntu-26.04-amd64-v1",
		Architecture: "amd64",
		Firecracker:  targetFirecracker,
	}
	var pairs [][2]string
	validatePair := func(_ context.Context, firecracker, jailer string) error {
		pairs = append(pairs, [2]string{firecracker, jailer})
		return nil
	}

	got, ok, err := cachedVMUpgradeJailerCandidate(context.Background(), vm, cacheRoot, validatePair)
	if err != nil {
		t.Fatalf("cachedVMUpgradeJailerCandidate: %v", err)
	}
	if !ok {
		t.Fatal("cachedVMUpgradeJailerCandidate did not find candidate")
	}
	if got.Path != candidateJailer || got.ArtifactName != "jailer" || got.SHA256 != candidateChecksum || got.Architecture != "amd64" {
		t.Fatalf("candidate = %#v", got)
	}
	wantPairs := [][2]string{{candidateFirecracker, candidateJailer}, {targetFirecracker, candidateJailer}}
	if !reflect.DeepEqual(pairs, wantPairs) {
		t.Fatalf("validated pairs = %#v, want %#v", pairs, wantPairs)
	}
}

func TestCachedVMUpgradeJailerCandidateRejectsUntrustedCandidates(t *testing.T) {
	tests := []struct {
		name         string
		architecture string
		tamper       bool
		validate     func(context.Context, string, string) error
		wantErr      string
	}{
		{
			name:         "checksum mismatch",
			architecture: "amd64",
			tamper:       true,
			validate:     func(context.Context, string, string) error { return nil },
			wantErr:      "checksum mismatch",
		},
		{
			name:         "runtime version mismatch",
			architecture: "amd64",
			validate: func(_ context.Context, firecracker, _ string) error {
				if strings.Contains(firecracker, "target") {
					return errors.New("firecracker version 1.7.0 does not match jailer version 1.8.0")
				}
				return nil
			},
			wantErr: "does not match",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cacheRoot := t.TempDir()
			_, jailer, _ := writeVMJailerUpgradeManagedBundle(t, cacheRoot, "ubuntu-26.04-amd64-v2", tt.architecture)
			if tt.tamper {
				if err := os.WriteFile(jailer, []byte("tampered"), 0o755); err != nil {
					t.Fatalf("tamper jailer: %v", err)
				}
			}
			vm := vmJailerUpgradeVM{Service: "devbox", Architecture: "amd64", Firecracker: filepath.Join(t.TempDir(), "target-firecracker")}
			_, _, err := cachedVMUpgradeJailerCandidate(context.Background(), vm, cacheRoot, tt.validate)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestFetchOfficialVMUpgradeJailerDownloadsOnlyDeclaredJailer(t *testing.T) {
	fixture := newVMJailerUpgradeHTTPFixture(t, testUbuntuVMPayload, "amd64", false, false)
	cache := vmImageCache{Root: t.TempDir(), ManifestURL: "https://evil.example/override.json", Client: fixture.client}
	vm := vmJailerUpgradeVM{
		Service:      "devbox",
		Payload:      testUbuntuVMPayload,
		ImageVersion: "ubuntu-26.04-amd64-v1",
		Architecture: "amd64",
		Firecracker:  "/images/v1/firecracker",
	}
	var validated [][2]string
	candidate, err := fetchOfficialVMUpgradeJailer(context.Background(), vm, cache, func(_ context.Context, firecracker, jailer string) error {
		validated = append(validated, [2]string{firecracker, jailer})
		return nil
	})
	if err != nil {
		t.Fatalf("fetchOfficialVMUpgradeJailer: %v", err)
	}
	if candidate.ArtifactName != "jailer" || candidate.Architecture != "amd64" || candidate.SHA256 != fixture.jailerChecksum {
		t.Fatalf("candidate = %#v", candidate)
	}
	if got, err := os.ReadFile(candidate.Path); err != nil || !bytes.Equal(got, fixture.jailer) {
		t.Fatalf("downloaded jailer = %q, %v", got, err)
	}
	if !reflect.DeepEqual(validated, [][2]string{{vm.Firecracker, candidate.Path}}) {
		t.Fatalf("validated pairs = %#v", validated)
	}
	wantURLs := []string{defaultVMImageCatalogURL, testDefaultVMImageManifest, strings.TrimSuffix(testDefaultVMImageManifest, "manifest.json") + "jailer"}
	if !reflect.DeepEqual(fixture.urls, wantURLs) {
		t.Fatalf("requested URLs = %#v, want %#v", fixture.urls, wantURLs)
	}
	for _, rawURL := range fixture.urls {
		if strings.Contains(rawURL, "rootfs") || strings.Contains(rawURL, "vmlinux") || strings.HasSuffix(rawURL, "/firecracker") {
			t.Fatalf("official jailer fetch requested unrelated artifact %q", rawURL)
		}
	}
}

func TestFetchOfficialVMUpgradeJailerRejectsChecksumAndArchitecture(t *testing.T) {
	tests := []struct {
		name         string
		architecture string
		badChecksum  bool
		wantErr      string
	}{
		{name: "checksum mismatch", architecture: "amd64", badChecksum: true, wantErr: "checksum mismatch"},
		{name: "architecture mismatch", architecture: "arm64", wantErr: "architecture"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newVMJailerUpgradeHTTPFixture(t, testUbuntuVMPayload, tt.architecture, tt.badChecksum, false)
			cacheRoot := t.TempDir()
			vm := vmJailerUpgradeVM{Service: "devbox", Payload: testUbuntuVMPayload, Architecture: "amd64", Firecracker: "/images/v1/firecracker"}
			_, err := fetchOfficialVMUpgradeJailer(context.Background(), vm, vmImageCache{Root: cacheRoot, Client: fixture.client}, func(context.Context, string, string) error { return nil })
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
			if entries, readErr := os.ReadDir(filepath.Join(cacheRoot, "upgrade-jailers")); readErr == nil && len(entries) != 0 {
				t.Fatalf("partial upgrade artifacts remain: %#v", entries)
			}
		})
	}
}

func TestResolveVMUpgradeJailerUnknownCatalogPayloadRequiresReimportWithoutArtifactAccess(t *testing.T) {
	fixture := newVMJailerUpgradeHTTPFixture(t, testUbuntuVMPayload, "amd64", false, false)
	cache := vmImageCache{Root: t.TempDir(), Client: fixture.client}
	vm := vmJailerUpgradeVM{Service: "custom", Payload: "vm://custom/image", ImageVersion: "custom-v1", Architecture: "amd64", Firecracker: "/images/custom/firecracker"}
	deps := vmJailerUpgradeDeps{
		sibling: func(context.Context, vmJailerUpgradeVM) (string, bool, error) { return "", false, nil },
		cached: func(context.Context, vmJailerUpgradeVM) (vmJailerCandidate, bool, error) {
			return vmJailerCandidate{}, false, nil
		},
		localPayload: func(string) bool { return false },
		official: func(ctx context.Context, got vmJailerUpgradeVM) (vmJailerCandidate, error) {
			return fetchOfficialVMUpgradeJailer(ctx, got, cache, func(context.Context, string, string) error { return nil })
		},
		install: func(context.Context, vmJailerUpgradeVM, vmJailerCandidate) (string, error) {
			t.Fatal("install called for unknown catalog payload")
			return "", nil
		},
	}
	_, _, err := resolveVMUpgradeJailer(context.Background(), vm, deps)
	if err == nil || !strings.Contains(err.Error(), "re-import") {
		t.Fatalf("error = %v, want re-import guidance", err)
	}
	if !reflect.DeepEqual(fixture.urls, []string{defaultVMImageCatalogURL}) {
		t.Fatalf("requested URLs = %#v, want catalog only", fixture.urls)
	}
}

func TestFetchOfficialVMUpgradeJailerRejectsUntrustedManifestURL(t *testing.T) {
	fixture := newVMJailerUpgradeHTTPFixture(t, testUbuntuVMPayload, "amd64", false, true)
	vm := vmJailerUpgradeVM{Service: "devbox", Payload: testUbuntuVMPayload, Architecture: "amd64", Firecracker: "/images/v1/firecracker"}
	_, err := fetchOfficialVMUpgradeJailer(context.Background(), vm, vmImageCache{Root: t.TempDir(), Client: fixture.client}, func(context.Context, string, string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "untrusted VM image manifest URL") {
		t.Fatalf("error = %v, want untrusted manifest URL", err)
	}
	if !reflect.DeepEqual(fixture.urls, []string{defaultVMImageCatalogURL}) {
		t.Fatalf("requested URLs = %#v, want catalog only", fixture.urls)
	}
}

func TestInstallUpgradeJailer(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "staged-jailer")
	contents := []byte("matching jailer")
	if err := os.WriteFile(source, contents, 0o755); err != nil {
		t.Fatalf("write source: %v", err)
	}
	targetDir := filepath.Join(root, "target")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	vm := vmJailerUpgradeVM{Service: "devbox", Architecture: "amd64", Firecracker: filepath.Join(targetDir, "firecracker"), Jailer: filepath.Join(targetDir, "jailer")}
	candidate := vmJailerCandidate{Path: source, ArtifactName: "jailer", SHA256: testSHA256Hex(contents), Architecture: "x86_64"}
	ops := defaultVMJailerUpgradeInstallOps()
	ops.trustedDirUID = uint32(os.Geteuid())
	var chowns [][2]int
	ops.fchown = func(_ *os.File, uid, gid int) error {
		chowns = append(chowns, [2]int{uid, gid})
		return nil
	}
	var validated [][2]string
	ops.validatePair = func(_ context.Context, firecracker, jailer string) error {
		validated = append(validated, [2]string{firecracker, jailer})
		return nil
	}

	got, err := installUpgradeJailerWithOps(context.Background(), vm, candidate, ops)
	if err != nil {
		t.Fatalf("installUpgradeJailerWithOps: %v", err)
	}
	if got != vm.Jailer {
		t.Fatalf("installed path = %q, want %q", got, vm.Jailer)
	}
	if installed, err := os.ReadFile(got); err != nil || !bytes.Equal(installed, contents) {
		t.Fatalf("installed contents = %q, %v", installed, err)
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat installed jailer: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("installed mode = %o, want 755", info.Mode().Perm())
	}
	if !reflect.DeepEqual(chowns, [][2]int{{0, 0}}) {
		t.Fatalf("chown calls = %#v", chowns)
	}
	wantValidated := [][2]string{{vm.Firecracker, source}, {vm.Firecracker, vm.Jailer}}
	if !reflect.DeepEqual(validated, wantValidated) {
		t.Fatalf("validated pairs = %#v, want %#v", validated, wantValidated)
	}
}

func TestInstallUpgradeJailerPreservesExistingSiblingUntilCandidateVerifies(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "staged-jailer")
	contents := []byte("untrusted replacement")
	if err := os.WriteFile(source, contents, 0o755); err != nil {
		t.Fatalf("write source: %v", err)
	}
	targetDir := filepath.Join(root, "target")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	vm := vmJailerUpgradeVM{
		Service:      "devbox",
		Architecture: "amd64",
		Firecracker:  filepath.Join(targetDir, "firecracker"),
		Jailer:       filepath.Join(targetDir, "jailer"),
	}
	existing := []byte("known-good sibling")
	if err := os.WriteFile(vm.Jailer, existing, 0o755); err != nil {
		t.Fatalf("write existing jailer: %v", err)
	}
	candidate := vmJailerCandidate{
		Path:         source,
		ArtifactName: "jailer",
		SHA256:       testSHA256Hex([]byte("different replacement")),
		Architecture: "amd64",
	}
	ops := defaultVMJailerUpgradeInstallOps()
	ops.fchown = func(*os.File, int, int) error { return nil }
	ops.validatePair = func(context.Context, string, string) error {
		t.Fatal("candidate with invalid checksum reached version validation")
		return nil
	}

	_, err := installUpgradeJailerWithOps(context.Background(), vm, candidate, ops)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("error = %v, want checksum mismatch", err)
	}
	got, readErr := os.ReadFile(vm.Jailer)
	if readErr != nil {
		t.Fatalf("read existing jailer: %v", readErr)
	}
	if !bytes.Equal(got, existing) {
		t.Fatalf("existing sibling contents = %q, want %q", got, existing)
	}
}

func TestInstallUpgradeJailerPublishConflictPreservesExistingTarget(t *testing.T) {
	fixture := newVMJailerInstallFixture(t)
	existing := []byte("concurrent known-good jailer")
	if err := os.WriteFile(fixture.vm.Jailer, existing, 0o755); err != nil {
		t.Fatalf("write existing jailer: %v", err)
	}
	ops := fixture.ops()

	_, err := installUpgradeJailerWithOps(context.Background(), fixture.vm, fixture.candidate, ops)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %v, want no-replace conflict", err)
	}
	got, readErr := os.ReadFile(fixture.vm.Jailer)
	if readErr != nil {
		t.Fatalf("read existing jailer: %v", readErr)
	}
	if !bytes.Equal(got, existing) {
		t.Fatalf("existing target contents = %q, want %q", got, existing)
	}
}

func TestInstallUpgradeJailerSerializesDirectoryMutation(t *testing.T) {
	fixture := newVMJailerInstallFixture(t)
	firstEnteredPublish := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstOps := fixture.ops()
	firstOps.beforePublish = func(*os.File, string) error {
		close(firstEnteredPublish)
		<-releaseFirst
		return nil
	}
	firstResult := make(chan error, 1)
	go func() {
		_, err := installUpgradeJailerWithOps(context.Background(), fixture.vm, fixture.candidate, firstOps)
		firstResult <- err
	}()
	<-firstEnteredPublish

	secondContents := []byte("second installer jailer")
	secondSource := filepath.Join(fixture.root, "second-staged-jailer")
	if err := os.WriteFile(secondSource, secondContents, 0o755); err != nil {
		close(releaseFirst)
		t.Fatalf("write second candidate: %v", err)
	}
	secondCandidate := fixture.candidate
	secondCandidate.Path = secondSource
	secondCandidate.SHA256 = testSHA256Hex(secondContents)
	secondCopied := make(chan struct{}, 1)
	secondOps := fixture.ops()
	secondOps.copy = func(dst io.Writer, src io.Reader) (int64, error) {
		secondCopied <- struct{}{}
		return io.Copy(dst, src)
	}
	secondResult := make(chan error, 1)
	go func() {
		_, err := installUpgradeJailerWithOps(context.Background(), fixture.vm, secondCandidate, secondOps)
		secondResult <- err
	}()

	select {
	case <-secondCopied:
		close(releaseFirst)
		<-firstResult
		<-secondResult
		t.Fatal("second installer mutated the directory while the first held the lock")
	case <-time.After(150 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstResult; err != nil {
		t.Fatalf("first install: %v", err)
	}
	secondErr := <-secondResult
	if secondErr == nil || !strings.Contains(secondErr.Error(), "already exists") {
		t.Fatalf("second install error = %v, want no-replace conflict after lock release", secondErr)
	}
	got, err := os.ReadFile(fixture.vm.Jailer)
	if err != nil {
		t.Fatalf("read installed jailer: %v", err)
	}
	want, err := os.ReadFile(fixture.candidate.Path)
	if err != nil {
		t.Fatalf("read first candidate: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("installed jailer = %q, want first installer contents %q", got, want)
	}
}

func TestAcquireVMJailerUpgradeDirLockHonorsCancellation(t *testing.T) {
	targetDir := t.TempDir()
	first, err := os.Open(targetDir)
	if err != nil {
		t.Fatalf("open first directory handle: %v", err)
	}
	defer func() { _ = first.Close() }()
	second, err := os.Open(targetDir)
	if err != nil {
		t.Fatalf("open second directory handle: %v", err)
	}
	defer func() { _ = second.Close() }()
	if err := acquireVMJailerUpgradeDirLock(context.Background(), first); err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := acquireVMJailerUpgradeDirLock(ctx, second); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled lock error = %v, want context canceled", err)
	}
	if err := releaseVMJailerUpgradeDirLock(first); err != nil {
		t.Fatalf("release first lock: %v", err)
	}
	if err := acquireVMJailerUpgradeDirLock(context.Background(), second); err != nil {
		t.Fatalf("acquire second lock after cancellation: %v", err)
	}
	if err := releaseVMJailerUpgradeDirLock(second); err != nil {
		t.Fatalf("release second lock: %v", err)
	}
}

func TestInstallUpgradeJailerValidatesBoundTargetDirectoryOwnerAndMode(t *testing.T) {
	tests := []struct {
		name      string
		configure func(vmJailerInstallFixture, *vmJailerUpgradeInstallOps) error
		wantErr   string
	}{
		{
			name: "unexpected owner",
			configure: func(_ vmJailerInstallFixture, ops *vmJailerUpgradeInstallOps) error {
				ops.trustedDirUID = uint32(os.Geteuid() + 1)
				return nil
			},
			wantErr: "target directory owner",
		},
		{
			name: "group writable",
			configure: func(fixture vmJailerInstallFixture, _ *vmJailerUpgradeInstallOps) error {
				return os.Chmod(fixture.targetDir, 0o775)
			},
			wantErr: "writable by group or others",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newVMJailerInstallFixture(t)
			ops := fixture.ops()
			if err := tt.configure(fixture, &ops); err != nil {
				t.Fatalf("configure: %v", err)
			}

			_, err := installUpgradeJailerWithOps(context.Background(), fixture.vm, fixture.candidate, ops)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
			if _, statErr := os.Stat(fixture.vm.Jailer); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("unexpected published jailer: %v", statErr)
			}
		})
	}
}

func TestInstallUpgradeJailerRejectsSwappedTargetDirectory(t *testing.T) {
	fixture := newVMJailerInstallFixture(t)
	otherDir := filepath.Join(fixture.root, "attacker-dir")
	if err := os.Mkdir(otherDir, 0o755); err != nil {
		t.Fatalf("mkdir attacker dir: %v", err)
	}
	replacement := []byte("attacker replacement")
	if err := os.WriteFile(filepath.Join(otherDir, "jailer"), replacement, 0o755); err != nil {
		t.Fatalf("write attacker jailer: %v", err)
	}
	movedDir := filepath.Join(fixture.root, "opened-target-dir")
	ops := fixture.ops()
	pairCalls := 0
	ops.validatePair = func(context.Context, string, string) error {
		pairCalls++
		return nil
	}
	ops.afterPublish = func(*os.File) error {
		if err := os.Rename(fixture.targetDir, movedDir); err != nil {
			return err
		}
		return os.Symlink(otherDir, fixture.targetDir)
	}

	_, err := installUpgradeJailerWithOps(context.Background(), fixture.vm, fixture.candidate, ops)
	if err == nil || !strings.Contains(err.Error(), "target directory changed") {
		t.Fatalf("error = %v, want target directory changed", err)
	}
	if pairCalls != 1 {
		t.Fatalf("pair validation calls = %d, want preinstall only", pairCalls)
	}
	if _, statErr := os.Stat(filepath.Join(movedDir, "jailer")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("installed inode remains in opened directory: %v", statErr)
	}
	got, readErr := os.ReadFile(filepath.Join(fixture.targetDir, "jailer"))
	if readErr != nil || !bytes.Equal(got, replacement) {
		t.Fatalf("replacement target = %q, %v", got, readErr)
	}
}

func TestInstallUpgradeJailerRejectsReplacedTempName(t *testing.T) {
	fixture := newVMJailerInstallFixture(t)
	replacement := []byte("replacement temp inode")
	var tempPath string
	ops := fixture.ops()
	ops.beforePublish = func(_ *os.File, tempName string) error {
		tempPath = filepath.Join(fixture.targetDir, tempName)
		if err := os.Remove(tempPath); err != nil {
			return err
		}
		return os.WriteFile(tempPath, replacement, 0o600)
	}

	_, err := installUpgradeJailerWithOps(context.Background(), fixture.vm, fixture.candidate, ops)
	if err == nil || !strings.Contains(err.Error(), "temporary VM jailer inode changed") {
		t.Fatalf("error = %v, want temporary inode change", err)
	}
	if _, statErr := os.Stat(fixture.vm.Jailer); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unexpected published jailer: %v", statErr)
	}
	got, readErr := os.ReadFile(tempPath)
	if readErr != nil || !bytes.Equal(got, replacement) {
		t.Fatalf("replacement temp = %q, %v", got, readErr)
	}
}

func TestInstallUpgradeJailerLateFailuresPreserveReplacementTarget(t *testing.T) {
	tests := []struct {
		name          string
		configureFail func(*vmJailerUpgradeInstallOps, func() error)
		wantErr       string
	}{
		{
			name: "checksum",
			configureFail: func(ops *vmJailerUpgradeInstallOps, swap func() error) {
				verify := ops.verifyFileChecksum
				calls := 0
				ops.verifyFileChecksum = func(file *os.File, artifactName, checksum string) error {
					calls++
					if calls == 2 {
						if err := swap(); err != nil {
							return err
						}
						return errors.New("injected late checksum failure")
					}
					return verify(file, artifactName, checksum)
				}
			},
			wantErr: "late checksum failure",
		},
		{
			name: "runtime pair",
			configureFail: func(ops *vmJailerUpgradeInstallOps, swap func() error) {
				calls := 0
				ops.validatePair = func(context.Context, string, string) error {
					calls++
					if calls == 2 {
						if err := swap(); err != nil {
							return err
						}
						return errors.New("injected late pair failure")
					}
					return nil
				}
			},
			wantErr: "late pair failure",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newVMJailerInstallFixture(t)
			replacement := []byte("concurrent replacement")
			movedInstalled := filepath.Join(fixture.targetDir, "moved-installed-jailer")
			swap := func() error {
				if err := os.Rename(fixture.vm.Jailer, movedInstalled); err != nil {
					return err
				}
				return os.WriteFile(fixture.vm.Jailer, replacement, 0o755)
			}
			ops := fixture.ops()
			tt.configureFail(&ops, swap)

			_, err := installUpgradeJailerWithOps(context.Background(), fixture.vm, fixture.candidate, ops)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
			got, readErr := os.ReadFile(fixture.vm.Jailer)
			if readErr != nil || !bytes.Equal(got, replacement) {
				t.Fatalf("replacement target = %q, %v", got, readErr)
			}
		})
	}
}

func TestInstallUpgradeJailerLateFailureRemovesOnlyInstalledInode(t *testing.T) {
	fixture := newVMJailerInstallFixture(t)
	ops := fixture.ops()
	verify := ops.verifyFileChecksum
	calls := 0
	ops.verifyFileChecksum = func(file *os.File, artifactName, checksum string) error {
		calls++
		if calls == 2 {
			return errors.New("injected late checksum failure")
		}
		return verify(file, artifactName, checksum)
	}

	_, err := installUpgradeJailerWithOps(context.Background(), fixture.vm, fixture.candidate, ops)
	if err == nil || !strings.Contains(err.Error(), "late checksum failure") {
		t.Fatalf("error = %v, want late checksum failure", err)
	}
	if _, statErr := os.Stat(fixture.vm.Jailer); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("installed target remains after late failure: %v", statErr)
	}
	retryCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := installUpgradeJailerWithOps(retryCtx, fixture.vm, fixture.candidate, fixture.ops()); err != nil {
		t.Fatalf("retry after validation failure did not reacquire released lock: %v", err)
	}
}

func TestInstallUpgradeJailerRejectsInvalidInputAndCleansPartialInstall(t *testing.T) {
	tests := []struct {
		name         string
		candidate    func(string, []byte) vmJailerCandidate
		validate     func(int) error
		copyError    bool
		chownError   bool
		chmodError   bool
		publishError bool
		wantErr      string
	}{
		{
			name: "checksum mismatch",
			candidate: func(path string, contents []byte) vmJailerCandidate {
				return vmJailerCandidate{Path: path, ArtifactName: "jailer", SHA256: testSHA256Hex(append(contents, '!')), Architecture: "amd64"}
			},
			validate: func(int) error { return nil },
			wantErr:  "checksum mismatch",
		},
		{
			name: "architecture mismatch",
			candidate: func(path string, contents []byte) vmJailerCandidate {
				return vmJailerCandidate{Path: path, ArtifactName: "jailer", SHA256: testSHA256Hex(contents), Architecture: "arm64"}
			},
			validate: func(int) error { return nil },
			wantErr:  "architecture",
		},
		{
			name: "copy cleanup",
			candidate: func(path string, contents []byte) vmJailerCandidate {
				return vmJailerCandidate{Path: path, ArtifactName: "jailer", SHA256: testSHA256Hex(contents), Architecture: "amd64"}
			},
			validate:  func(int) error { return nil },
			copyError: true,
			wantErr:   "copy VM jailer",
		},
		{
			name: "chown cleanup",
			candidate: func(path string, contents []byte) vmJailerCandidate {
				return vmJailerCandidate{Path: path, ArtifactName: "jailer", SHA256: testSHA256Hex(contents), Architecture: "amd64"}
			},
			validate:   func(int) error { return nil },
			chownError: true,
			wantErr:    "chown temporary VM jailer",
		},
		{
			name: "chmod cleanup",
			candidate: func(path string, contents []byte) vmJailerCandidate {
				return vmJailerCandidate{Path: path, ArtifactName: "jailer", SHA256: testSHA256Hex(contents), Architecture: "amd64"}
			},
			validate:   func(int) error { return nil },
			chmodError: true,
			wantErr:    "chmod temporary VM jailer",
		},
		{
			name: "preinstall version mismatch",
			candidate: func(path string, contents []byte) vmJailerCandidate {
				return vmJailerCandidate{Path: path, ArtifactName: "jailer", SHA256: testSHA256Hex(contents), Architecture: "amd64"}
			},
			validate: func(int) error { return errors.New("firecracker version does not match jailer version") },
			wantErr:  "does not match",
		},
		{
			name: "postinstall validation cleanup",
			candidate: func(path string, contents []byte) vmJailerCandidate {
				return vmJailerCandidate{Path: path, ArtifactName: "jailer", SHA256: testSHA256Hex(contents), Architecture: "amd64"}
			},
			validate: func(call int) error {
				if call == 2 {
					return errors.New("installed jailer version does not match")
				}
				return nil
			},
			wantErr: "does not match",
		},
		{
			name: "atomic publish cleanup",
			candidate: func(path string, contents []byte) vmJailerCandidate {
				return vmJailerCandidate{Path: path, ArtifactName: "jailer", SHA256: testSHA256Hex(contents), Architecture: "amd64"}
			},
			validate:     func(int) error { return nil },
			publishError: true,
			wantErr:      "publish VM jailer",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source")
			contents := []byte("jailer")
			if err := os.WriteFile(source, contents, 0o755); err != nil {
				t.Fatalf("write source: %v", err)
			}
			targetDir := filepath.Join(root, "target")
			if err := os.MkdirAll(targetDir, 0o755); err != nil {
				t.Fatalf("mkdir target: %v", err)
			}
			vm := vmJailerUpgradeVM{Service: "devbox", Architecture: "amd64", Firecracker: filepath.Join(targetDir, "firecracker"), Jailer: filepath.Join(targetDir, "jailer")}
			candidate := tt.candidate(source, contents)
			ops := defaultVMJailerUpgradeInstallOps()
			ops.trustedDirUID = uint32(os.Geteuid())
			ops.fchown = func(*os.File, int, int) error { return nil }
			if tt.copyError {
				ops.copy = func(io.Writer, io.Reader) (int64, error) { return 0, errors.New("copy denied") }
			}
			if tt.chownError {
				ops.fchown = func(*os.File, int, int) error { return errors.New("chown denied") }
			}
			if tt.chmodError {
				ops.fchmod = func(*os.File, os.FileMode) error { return errors.New("chmod denied") }
			}
			validationCalls := 0
			ops.validatePair = func(context.Context, string, string) error {
				validationCalls++
				return tt.validate(validationCalls)
			}
			if tt.publishError {
				ops.publishNoReplace = func(*os.File, *os.File, string, string) error { return errors.New("publish denied") }
			}
			_, err := installUpgradeJailerWithOps(context.Background(), vm, candidate, ops)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
			if _, statErr := os.Stat(vm.Jailer); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("partial target remains: %v", statErr)
			}
			entries, readErr := os.ReadDir(targetDir)
			if readErr != nil {
				t.Fatalf("read target dir: %v", readErr)
			}
			for _, entry := range entries {
				if strings.Contains(entry.Name(), ".jailer.tmp-") {
					t.Fatalf("partial temp file remains: %s", entry.Name())
				}
			}
		})
	}
}

type vmJailerInstallFixture struct {
	root      string
	targetDir string
	vm        vmJailerUpgradeVM
	candidate vmJailerCandidate
}

func newVMJailerInstallFixture(t *testing.T) vmJailerInstallFixture {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "staged-jailer")
	contents := []byte("matching jailer")
	if err := os.WriteFile(source, contents, 0o755); err != nil {
		t.Fatalf("write staged jailer: %v", err)
	}
	targetDir := filepath.Join(root, "target")
	if err := os.Mkdir(targetDir, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	vm := vmJailerUpgradeVM{
		Service:      "devbox",
		Architecture: "amd64",
		Firecracker:  filepath.Join(targetDir, "firecracker"),
		Jailer:       filepath.Join(targetDir, "jailer"),
	}
	return vmJailerInstallFixture{
		root:      root,
		targetDir: targetDir,
		vm:        vm,
		candidate: vmJailerCandidate{
			Path:         source,
			ArtifactName: "jailer",
			SHA256:       testSHA256Hex(contents),
			Architecture: "amd64",
		},
	}
}

func (f vmJailerInstallFixture) ops() vmJailerUpgradeInstallOps {
	ops := defaultVMJailerUpgradeInstallOps()
	ops.trustedDirUID = uint32(os.Geteuid())
	ops.fchown = func(*os.File, int, int) error { return nil }
	ops.validatePair = func(context.Context, string, string) error { return nil }
	return ops
}

func vmJailerUpgradeServiceNames(vms []vmJailerUpgradeVM) []string {
	names := make([]string, 0, len(vms))
	for _, vm := range vms {
		names = append(names, vm.Service)
	}
	return names
}

func writeVMJailerUpgradeTargetBundle(t *testing.T, dir, version, architecture string) string {
	t.Helper()
	contents := vmImageTestContents()
	manifest := vmImageTestManifest(version, contents)
	manifest.Architecture = architecture
	if err := writeManifestFile(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		t.Fatalf("write target manifest: %v", err)
	}
	for name, content := range contents {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o755); err != nil {
			t.Fatalf("write target artifact %s: %v", name, err)
		}
	}
	return filepath.Join(dir, manifest.RootFS)
}

func writeVMJailerUpgradeManagedBundle(t *testing.T, cacheRoot, version, architecture string) (string, string, string) {
	t.Helper()
	contents := vmImageTestContents()
	contents["jailer"] = []byte("jailer-" + version)
	manifest := vmImageTestManifest(version, contents)
	manifest.Architecture = architecture
	manifest.Jailer = "jailer"
	manifest.Checksums[manifest.Jailer] = testSHA256Hex(contents[manifest.Jailer])
	dir := filepath.Join(cacheRoot, version)
	if err := writeManifestFile(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		t.Fatalf("write managed manifest: %v", err)
	}
	for name, content := range contents {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o755); err != nil {
			t.Fatalf("write managed artifact %s: %v", name, err)
		}
	}
	return filepath.Join(dir, manifest.Firecracker), filepath.Join(dir, manifest.Jailer), manifest.Checksums[manifest.Jailer]
}

type vmJailerUpgradeHTTPFixture struct {
	client         *http.Client
	urls           []string
	jailer         []byte
	jailerChecksum string
}

func newVMJailerUpgradeHTTPFixture(t *testing.T, catalogPayload, architecture string, badChecksum, untrustedManifestURL bool) *vmJailerUpgradeHTTPFixture {
	t.Helper()
	fixture := &vmJailerUpgradeHTTPFixture{jailer: []byte("official-jailer")}
	fixture.jailerChecksum = testSHA256Hex(fixture.jailer)
	contents := vmImageTestContents()
	contents["jailer"] = fixture.jailer
	manifest := vmImageTestManifest("ubuntu-26.04-amd64-v2", contents)
	manifest.Architecture = architecture
	manifest.Jailer = "jailer"
	manifest.Checksums[manifest.Jailer] = fixture.jailerChecksum
	if badChecksum {
		manifest.Checksums[manifest.Jailer] = testSHA256Hex([]byte("different-jailer"))
	}
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	manifestURL := testDefaultVMImageManifest
	if untrustedManifestURL {
		manifestURL = "https://evil.example/manifest.json"
	}
	catalog := vmImageCatalog{
		SchemaVersion: 1,
		Images: []vmImageCatalogImage{{
			Payload:       catalogPayload,
			Name:          "Ubuntu 26.04",
			Architecture:  "amd64",
			ManifestURL:   manifestURL,
			VersionPrefix: "ubuntu-26.04-amd64-",
			Default:       true,
		}},
	}
	catalogRaw, err := json.Marshal(catalog)
	if err != nil {
		t.Fatalf("marshal catalog: %v", err)
	}
	jailerURL := strings.TrimSuffix(testDefaultVMImageManifest, "manifest.json") + "jailer"
	fixture.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		fixture.urls = append(fixture.urls, req.URL.String())
		var body []byte
		switch req.URL.String() {
		case defaultVMImageCatalogURL:
			body = catalogRaw
		case testDefaultVMImageManifest:
			body = manifestRaw
		case jailerURL:
			body = fixture.jailer
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found", Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header), Request: req}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)), Header: make(http.Header), Request: req}, nil
	})}
	return fixture
}
