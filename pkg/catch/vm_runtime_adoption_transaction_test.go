// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
)

func TestVMRuntimeAdoptionCommitPublishesFleetWithoutRestart(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		name := "running"
		if stopped {
			name = "stopped"
		}
		t.Run(name, func(t *testing.T) {
			fixture, deps, systemctlCalls := newVMRuntimeAdoptionTransactionFixture(t, stopped)
			diskBefore := readVMRuntimeAdoptionTestFile(t, fixture.disk)

			tx, err := prepareVMRuntimeAdoptionWithDeps(context.Background(), &fixture.cfg, deps)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = tx.Close() })
			summary := tx.Summary()
			if !summary.HasChanges || !summary.RequiresRollbackGeneration || !slices.Equal(summary.Adopting, []string{"devbox"}) || !slices.Equal(summary.PendingRestart, []string{"devbox"}) {
				t.Fatalf("summary = %#v", summary)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if tx.CatchRollbackSafe() {
				t.Fatal("successful adoption unexpectedly permits rolling Catch back")
			}
			assertVMRuntimeAdoptionDatabaseGeneration(t, fixture.store, "devbox", true)
			assertVMRuntimeAdoptionDerivedGeneration(t, tx, true)
			if groups, err := tx.journal.LoadAll(); err != nil || len(groups) != 0 {
				t.Fatalf("journals after commit = %#v, %v", groups, err)
			}
			if !bytes.Equal(readVMRuntimeAdoptionTestFile(t, fixture.disk), diskBefore) {
				t.Fatal("active VM disk changed during logical adoption")
			}
			for _, call := range *systemctlCalls {
				if !slices.Equal(call, []string{"daemon-reload"}) {
					t.Fatalf("systemctl call = %v; adoption must not start, stop, or restart a VM", call)
				}
			}
		})
	}
}

func TestAdoptMonolithicVMComponents(t *testing.T) {
	tests := []struct {
		name           string
		version        string
		kernelVersion  string
		runtimeVersion string
		withManifest   bool
		syncedKernel   bool
	}{
		{name: "v11", version: "ubuntu-26.04-amd64-v11", kernelVersion: "linux-7.1.2-yeet", runtimeVersion: "1.14.3", syncedKernel: true},
		{name: "v15", version: "ubuntu-26.04-amd64-v15", kernelVersion: "linux-7.1.3-yeet", runtimeVersion: "1.14.3", syncedKernel: true},
		{name: "v29", version: "ubuntu-26.04-amd64-kernel-7.1.4-v29", kernelVersion: "linux-7.1.4-yeet", runtimeVersion: "1.16.1", withManifest: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture, deps, systemctlCalls := newVMRuntimeAdoptionTransactionFixture(t, false)
			configureVMRuntimeAdoptionBundle(t, fixture, &deps, tt.version, tt.kernelVersion, tt.runtimeVersion, tt.withManifest, tt.syncedKernel)
			before := readLatestVMRuntimeAdoptionData(t, fixture.store).Services[fixture.service.Name]
			diskBefore := readVMRuntimeAdoptionTestFile(t, fixture.disk)

			tx, err := prepareVMRuntimeAdoptionWithDeps(context.Background(), &fixture.cfg, deps)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = tx.Close() })
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}

			after := readLatestVMRuntimeAdoptionData(t, fixture.store).Services[fixture.service.Name]
			if after.VM.Image != before.VM.Image {
				t.Fatalf("image provenance changed during adoption: before %#v after %#v", before.VM.Image, after.VM.Image)
			}
			if after.VM.Disk != before.VM.Disk || !bytes.Equal(readVMRuntimeAdoptionTestFile(t, fixture.disk), diskBefore) {
				t.Fatal("active VM disk changed during adoption")
			}
			components := after.VM.Components
			if components == nil || components.Kernel.Path != fixture.kernel || components.Kernel.SHA256 == "" {
				t.Fatalf("adopted kernel = %#v", components)
			}
			if components.Runtime.Configured.Firecracker != fixture.firecracker || components.Runtime.Configured.Jailer != fixture.jailer || components.Runtime.Configured.FirecrackerSHA256 == "" || components.Runtime.Configured.JailerSHA256 == "" {
				t.Fatalf("adopted runtime = %#v", components.Runtime.Configured)
			}
			if tt.withManifest && components.Runtime.Configured.Source != string(vmRuntimeAdoptionCustomLegacy) {
				t.Fatalf("v29 source = %q, want measured custom legacy without an install receipt", components.Runtime.Configured.Source)
			}
			for _, call := range *systemctlCalls {
				if !slices.Equal(call, []string{"daemon-reload"}) {
					t.Fatalf("systemctl call = %v; adoption must not start, stop, or restart a VM", call)
				}
			}
		})
	}
}

func TestMixedMonolithicAndComponentFleet(t *testing.T) {
	fixture, deps, systemctlCalls := newVMRuntimeAdoptionTransactionFixture(t, false)
	configured := &db.VMComponentsConfig{
		GuestBase: db.VMGuestBaseConfig{ID: "guest-existing", ManifestSHA256: strings.Repeat("a", 64)},
		Kernel: db.VMKernelArtifactConfig{
			ID: "kernel-existing", ManifestSHA256: strings.Repeat("b", 64), SHA256: strings.Repeat("c", 64),
			Path: "/var/lib/yeet/vm-kernels/amd64/kernel-existing/vmlinux",
		},
		Runtime: db.VMRuntimeLifecycleConfig{Policy: "manual", Channel: "stable", Configured: db.VMRuntimeArtifactConfig{
			ID: "runtime-existing", ManifestSHA256: strings.Repeat("d", 64), FirecrackerSHA256: strings.Repeat("e", 64), JailerSHA256: strings.Repeat("f", 64),
			Firecracker: "/var/lib/yeet/vm-runtimes/amd64/runtime-existing/firecracker", Jailer: "/var/lib/yeet/vm-runtimes/amd64/runtime-existing/jailer",
		}},
	}
	already := &db.Service{
		Name: "already", ServiceType: db.ServiceTypeVM, ServiceRoot: filepath.Join(fixture.dataRoot, "zfs-mounts", "already"),
		VM: &db.VMConfig{Runtime: vmRuntimeFirecracker, Image: db.VMImageConfig{Version: "component-v1"}, Components: configured.Clone()},
	}
	if err := fixture.store.Set(&db.Data{Services: map[string]*db.Service{fixture.service.Name: fixture.service, already.Name: already}}); err != nil {
		t.Fatal(err)
	}
	deps.inventory.readiness = func(string) (vmJailerReadiness, error) { return vmJailerReady, nil }

	tx, err := prepareVMRuntimeAdoptionWithDeps(context.Background(), &fixture.cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Close() })
	if summary := tx.Summary(); !slices.Equal(summary.AlreadyAdopted, []string{"already"}) || !slices.Equal(summary.Adopting, []string{"devbox"}) {
		t.Fatalf("mixed fleet summary = %#v", summary)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	latest := readLatestVMRuntimeAdoptionData(t, fixture.store)
	if latest.Services[fixture.service.Name].VM.Components == nil {
		t.Fatal("monolithic VM was not adopted")
	}
	if got := latest.Services[already.Name].VM.Components; !reflect.DeepEqual(got, configured) {
		t.Fatalf("existing component VM changed: got %#v want %#v", got, configured)
	}
	for _, call := range *systemctlCalls {
		if !slices.Equal(call, []string{"daemon-reload"}) {
			t.Fatalf("systemctl call = %v; mixed-fleet adoption must not start, stop, or restart a VM", call)
		}
	}
}

func TestVMRuntimeAdoptionSummaryKeepsJailerReadinessSeparateFromAdoption(t *testing.T) {
	inventory := vmRuntimeAdoptionInventory{
		VMs: []vmRuntimeAdoptionPreparation{
			{Service: "already", ServiceRoot: "/srv/already"},
			{Service: "blocked", ServiceRoot: "/srv/blocked"},
			{Service: "adopting", ServiceRoot: "/srv/adopting"},
		},
		Summary: vmRuntimeAdoptionSummary{
			AlreadyAdopted: []string{"already"}, Adoptable: []string{"adopting"}, Blocked: []string{"blocked"},
			BlockedReasons: map[string]string{"blocked": "legacy metadata incomplete"},
		},
	}
	readiness := map[string]vmJailerReadiness{
		"/srv/already": vmJailerReady, "/srv/adopting": vmJailerReady, "/srv/blocked": vmJailerPendingRestart,
	}
	summary, err := vmRuntimeAdoptionPublicSummary(inventory, func(root string) (vmJailerReadiness, error) {
		return readiness[root], nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(summary.Ready, []string{"adopting", "already"}) || !slices.Equal(summary.PendingRestart, []string{"blocked"}) {
		t.Fatalf("jailer readiness = ready %v pending %v", summary.Ready, summary.PendingRestart)
	}
	if !slices.Equal(summary.AlreadyAdopted, []string{"already"}) || !slices.Equal(summary.Adopting, []string{"adopting"}) || !slices.Equal(summary.Blocked, []string{"blocked"}) {
		t.Fatalf("adoption classification = %#v", summary)
	}
}

func TestVMRuntimeAdoptionAlreadyAdoptedReadinessUsesCustomServiceRoot(t *testing.T) {
	dataRoot := t.TempDir()
	customRoot := filepath.Join(t.TempDir(), "zfs-vm-root")
	service := db.Service{
		Name: "adopted", ServiceType: db.ServiceTypeVM, ServiceRoot: customRoot, ServiceRootZFS: "tank/vms/adopted",
		VM: &db.VMConfig{Components: &db.VMComponentsConfig{GuestBase: db.VMGuestBaseConfig{ID: "guest"}}},
	}
	preparation := inventoryVMRuntimeAdoptionService(context.Background(), &Config{
		RootDir: dataRoot, ServicesRoot: filepath.Join(dataRoot, "services"),
	}, service, defaultVMRuntimeAdoptionInventoryDeps())
	if preparation.Classification != vmRuntimeAdoptionAlreadyAdopted || preparation.ServiceRoot != customRoot {
		t.Fatalf("already-adopted preparation = %#v", preparation)
	}
	seenRoot := ""
	summary, err := vmRuntimeAdoptionPublicSummary(vmRuntimeAdoptionInventory{
		VMs:     []vmRuntimeAdoptionPreparation{preparation},
		Summary: vmRuntimeAdoptionSummary{AlreadyAdopted: []string{"adopted"}},
	}, func(root string) (vmJailerReadiness, error) {
		seenRoot = root
		return vmJailerReady, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seenRoot != customRoot || !slices.Equal(summary.Ready, []string{"adopted"}) {
		t.Fatalf("readiness root/summary = %q/%#v", seenRoot, summary)
	}
}

func TestVMRuntimeAdoptionSummaryKeepsUnresolvedVMBlockedWithoutFailingFleet(t *testing.T) {
	readinessCalls := 0
	summary, err := vmRuntimeAdoptionPublicSummary(vmRuntimeAdoptionInventory{
		VMs: []vmRuntimeAdoptionPreparation{
			{Service: "adopting", ServiceRoot: "/srv/adopting"},
			{Service: "unresolved", Classification: vmRuntimeAdoptionBlocked},
		},
		Summary: vmRuntimeAdoptionSummary{
			Adoptable: []string{"adopting"}, Blocked: []string{"unresolved"},
			BlockedReasons: map[string]string{"unresolved": "service root is unavailable"},
		},
	}, func(root string) (vmJailerReadiness, error) {
		readinessCalls++
		if root != "/srv/adopting" {
			t.Fatalf("readiness called for unresolved root %q", root)
		}
		return vmJailerReady, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if readinessCalls != 1 || !slices.Equal(summary.Ready, []string{"adopting"}) || !slices.Equal(summary.Blocked, []string{"unresolved"}) {
		t.Fatalf("summary = %#v, readiness calls %d", summary, readinessCalls)
	}
}

func TestVMRuntimeAdoptionPreDatabaseFailuresRestoreExactOldGeneration(t *testing.T) {
	for _, failurePoint := range []string{"descriptor-published:devbox", "unit-published", "derived-published"} {
		t.Run(failurePoint, func(t *testing.T) {
			fixture, deps, _ := newVMRuntimeAdoptionTransactionFixture(t, false)
			oldUnit := readVMRuntimeAdoptionTestFile(t, fixture.unitPath)
			injected := errors.New("injected transition failure")
			deps.afterTransition = func(state string) error {
				if state == failurePoint {
					return injected
				}
				return nil
			}
			tx, err := prepareVMRuntimeAdoptionWithDeps(context.Background(), &fixture.cfg, deps)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Close()
			if err := tx.Commit(); !errors.Is(err, injected) {
				t.Fatalf("Commit error = %v, want injected failure", err)
			}
			if !tx.CatchRollbackSafe() {
				t.Fatal("verified exact-old compensation did not permit Catch rollback")
			}
			assertVMRuntimeAdoptionDatabaseGeneration(t, fixture.store, "devbox", false)
			assertVMRuntimeAdoptionDerivedGeneration(t, tx, false)
			if got := readVMRuntimeAdoptionTestFile(t, fixture.unitPath); !bytes.Equal(got, oldUnit) {
				t.Fatal("old unit bytes were not restored exactly")
			}
			if groups, err := tx.journal.LoadAll(); err != nil || len(groups) != 0 {
				t.Fatalf("active journal remained after rollback-safe compensation: %#v, %v", groups, err)
			}
		})
	}
}

func TestVMRuntimeAdoptionPreparedFailureFinalizesOldJournal(t *testing.T) {
	fixture, deps, _ := newVMRuntimeAdoptionTransactionFixture(t, false)
	injected := errors.New("injected after journal preparation")
	deps.afterTransition = func(state string) error {
		if state == "prepared" {
			return injected
		}
		return nil
	}
	if tx, err := prepareVMRuntimeAdoptionWithDeps(context.Background(), &fixture.cfg, deps); !errors.Is(err, injected) || tx != nil {
		t.Fatalf("Prepare = %#v, %v; want injected failure", tx, err)
	}

	store, err := openVMRuntimeJournalStore(context.Background(), fixture.dataRoot, deps.journal)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if groups, err := store.LoadAll(); err != nil || len(groups) != 0 {
		t.Fatalf("journals after prepared failure = %#v, %v", groups, err)
	}
}

func TestVMRuntimeAdoptionPreparePublicationFailureResumesAndFinalizesOldJournal(t *testing.T) {
	fixture, deps, _ := newVMRuntimeAdoptionTransactionFixture(t, false)
	syncErr := errors.New("injected first journal directory sync failure")
	realSync := deps.journal.syncDir
	syncCalls := 0
	deps.journal.syncDir = func(dir *os.File) error {
		syncCalls++
		if syncCalls == 1 {
			return syncErr
		}
		return realSync(dir)
	}
	tx, err := prepareVMRuntimeAdoptionWithDeps(context.Background(), &fixture.cfg, deps)
	if tx != nil || !errors.Is(err, syncErr) {
		t.Fatalf("Prepare = %#v, %v; want resumed publication failure", tx, err)
	}
	store, err := openVMRuntimeJournalStore(context.Background(), fixture.dataRoot, deps.journal)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if groups, err := store.LoadAll(); err != nil || len(groups) != 0 {
		t.Fatalf("journals after recoverable prepare failure = %#v, %v", groups, err)
	}
}

func TestVMRuntimeAdoptionCloseWithoutCommitFinalizesOldJournal(t *testing.T) {
	fixture, deps, _ := newVMRuntimeAdoptionTransactionFixture(t, false)
	tx, err := prepareVMRuntimeAdoptionWithDeps(context.Background(), &fixture.cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Close(); err != nil {
		t.Fatal(err)
	}
	if !tx.CatchRollbackSafe() {
		t.Fatal("clean pre-commit close did not prove the old Catch generation safe")
	}
	store, err := openVMRuntimeJournalStore(context.Background(), fixture.dataRoot, deps.journal)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if groups, err := store.LoadAll(); err != nil || len(groups) != 0 {
		t.Fatalf("journals after clean pre-commit close = %#v, %v", groups, err)
	}
}

func TestVMRuntimeAdoptionPreservesUnrelatedConcurrentDatabaseMutation(t *testing.T) {
	fixture, deps, _ := newVMRuntimeAdoptionTransactionFixture(t, false)
	otherStore := db.NewStore(filepath.Join(fixture.dataRoot, "db.json"), fixture.servicesRoot)
	deps.afterTransition = func(state string) error {
		if state != "derived-published" {
			return nil
		}
		_, err := otherStore.MutateData(func(data *db.Data) error {
			data.Services["devbox"].VM.Balloon.LastTargetBytes = 123456
			data.Services["unrelated"] = &db.Service{Name: "unrelated", ServiceType: db.ServiceTypeSystemd}
			return nil
		})
		return err
	}
	tx, err := prepareVMRuntimeAdoptionWithDeps(context.Background(), &fixture.cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	latest := readLatestVMRuntimeAdoptionData(t, otherStore)
	if latest.Services["devbox"].VM.Balloon.LastTargetBytes != 123456 || latest.Services["unrelated"] == nil || latest.Services["devbox"].VM.Components == nil {
		t.Fatalf("latest database did not preserve unrelated mutation: %#v", latest.Services)
	}
}

func TestVMRuntimeAdoptionRelevantDriftFailsClosedAndRestoresOld(t *testing.T) {
	tests := []struct {
		name             string
		mutate           func(*db.VMConfig)
		check            func(*db.VMConfig) bool
		wantComponents   bool
		wantRollbackSafe bool
		wantProjection   bool
	}{
		{
			name: "memory",
			mutate: func(vm *db.VMConfig) {
				vm.MemoryBytes = 987654321
			},
			check:            func(vm *db.VMConfig) bool { return vm.MemoryBytes == 987654321 },
			wantRollbackSafe: true,
		},
		{
			name: "cpus",
			mutate: func(vm *db.VMConfig) {
				vm.CPUs = 19
			},
			check:            func(vm *db.VMConfig) bool { return vm.CPUs == 19 },
			wantRollbackSafe: true,
		},
		{
			name: "network",
			mutate: func(vm *db.VMConfig) {
				vm.Networks = append(vm.Networks, db.VMNetworkConfig{Mode: "changed-after-preparation"})
			},
			check: func(vm *db.VMConfig) bool {
				return len(vm.Networks) != 0 && vm.Networks[len(vm.Networks)-1].Mode == "changed-after-preparation"
			},
			wantRollbackSafe: true,
		},
		{
			name: "components",
			mutate: func(vm *db.VMConfig) {
				vm.Components = &db.VMComponentsConfig{GuestBase: db.VMGuestBaseConfig{ID: "changed-after-preparation"}}
			},
			check: func(vm *db.VMConfig) bool {
				return vm.Components != nil && vm.Components.GuestBase.ID == "changed-after-preparation"
			},
			wantComponents: true,
			wantProjection: true,
		},
		{
			name: "image kernel",
			mutate: func(vm *db.VMConfig) {
				vm.Image.Kernel = "/changed-after-preparation/vmlinux"
			},
			check: func(vm *db.VMConfig) bool {
				return vm.Image.Kernel == "/changed-after-preparation/vmlinux"
			},
			wantProjection: true,
		},
		{
			name: "image version",
			mutate: func(vm *db.VMConfig) {
				vm.Image.Version = "changed-after-preparation"
			},
			check:            func(vm *db.VMConfig) bool { return vm.Image.Version == "changed-after-preparation" },
			wantRollbackSafe: true,
		},
		{
			name: "vsock path",
			mutate: func(vm *db.VMConfig) {
				vm.Sockets.VsockSocketPath = "/run/changed-after-preparation.sock"
			},
			check: func(vm *db.VMConfig) bool {
				return vm.Sockets.VsockSocketPath == "/run/changed-after-preparation.sock"
			},
			wantRollbackSafe: true,
		},
		{
			name: "console socket",
			mutate: func(vm *db.VMConfig) {
				vm.Console.SocketPath = "/run/changed-after-preparation.console"
			},
			check: func(vm *db.VMConfig) bool {
				return vm.Console.SocketPath == "/run/changed-after-preparation.console"
			},
			wantRollbackSafe: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, deps, _ := newVMRuntimeAdoptionTransactionFixture(t, false)
			otherStore := db.NewStore(filepath.Join(fixture.dataRoot, "db.json"), fixture.servicesRoot)
			deps.afterTransition = func(state string) error {
				if state != "derived-published" {
					return nil
				}
				_, err := otherStore.MutateData(func(data *db.Data) error {
					test.mutate(data.Services["devbox"].VM)
					return nil
				})
				return err
			}
			tx, err := prepareVMRuntimeAdoptionWithDeps(context.Background(), &fixture.cfg, deps)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Close()
			commitErr := tx.Commit()
			wantError := "precondition changed"
			if test.wantProjection {
				wantError = "database projection changed"
			}
			if commitErr == nil || !strings.Contains(commitErr.Error(), wantError) {
				t.Fatalf("Commit error = %v, want %q", commitErr, wantError)
			}
			if tx.CatchRollbackSafe() != test.wantRollbackSafe {
				t.Fatalf("CatchRollbackSafe = %v, want %v", tx.CatchRollbackSafe(), test.wantRollbackSafe)
			}
			assertVMRuntimeAdoptionDerivedGeneration(t, tx, false)
			latest := readLatestVMRuntimeAdoptionData(t, otherStore)
			if !test.check(latest.Services["devbox"].VM) || (latest.Services["devbox"].VM.Components != nil) != test.wantComponents {
				t.Fatalf("drift was overwritten: %#v", latest.Services["devbox"].VM)
			}
		})
	}
}

func TestVMRuntimeAdoptionPostDatabaseFailureRetainsNewForRecovery(t *testing.T) {
	for _, failurePoint := range []string{"database-published", "database-committed", "removal-begun"} {
		t.Run(failurePoint, func(t *testing.T) {
			fixture, deps, _ := newVMRuntimeAdoptionTransactionFixture(t, false)
			injected := errors.New("injected after database publication")
			deps.afterTransition = func(state string) error {
				if state == failurePoint {
					return injected
				}
				return nil
			}
			tx, err := prepareVMRuntimeAdoptionWithDeps(context.Background(), &fixture.cfg, deps)
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); !errors.Is(err, injected) {
				t.Fatalf("Commit error = %v, want injected failure", err)
			}
			if tx.CatchRollbackSafe() {
				t.Fatal("post-database failure incorrectly permits Catch rollback")
			}
			assertVMRuntimeAdoptionDatabaseGeneration(t, fixture.store, "devbox", true)
			assertVMRuntimeAdoptionDerivedGeneration(t, tx, true)
			if groups, err := tx.journal.LoadAll(); err != nil || len(groups) != 1 {
				t.Fatalf("retained journals = %#v, %v", groups, err)
			}
			if err := tx.Close(); err != nil {
				t.Fatal(err)
			}

			store, err := openVMRuntimeJournalStore(context.Background(), fixture.dataRoot, deps.journal)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := recoverVMRuntimeAdoptionsWithStore(context.Background(), &fixture.cfg, store, deps); err != nil {
				t.Fatal(err)
			}
			if groups, err := store.LoadAll(); err != nil || len(groups) != 0 {
				t.Fatalf("journals after recovery = %#v, %v", groups, err)
			}
		})
	}
}

func TestVMRuntimeAdoptionFinalizationRejectsDropInEvidenceDrift(t *testing.T) {
	fixture, deps, systemctlCalls := newVMRuntimeAdoptionTransactionFixture(t, false)
	dropIn := filepath.Join(filepath.Dir(fixture.unitPath), fixture.unitName+".d", "override.conf")
	writeVMRuntimeAdoptionTestFile(t, dropIn, "[Service]\nEnvironment=UNCHANGED_COMMAND=1\n", 0o644)
	loadUnit := deps.inventory.loadUnit
	deps.inventory.loadUnit = func(ctx context.Context, unit string) (vmRuntimeAdoptionLoadedUnit, error) {
		loaded, err := loadUnit(ctx, unit)
		if err != nil {
			return vmRuntimeAdoptionLoadedUnit{}, err
		}
		evidence, err := collectTrustedVMRuntimeAdoptionFileEvidence(dropIn, true, deps.inventory.evidence)
		if err != nil {
			return vmRuntimeAdoptionLoadedUnit{}, err
		}
		loaded.Fragments = append(loaded.Fragments, vmRuntimeAdoptionUnitFragment{Path: dropIn, Evidence: evidence})
		return loaded, nil
	}
	deps.afterTransition = func(state string) error {
		if state == "database-published" {
			writeVMRuntimeAdoptionTestFile(t, dropIn, "[Service]\nEnvironment=UNCHANGED_COMMAND=2\n", 0o644)
		}
		return nil
	}

	tx, err := prepareVMRuntimeAdoptionWithDeps(context.Background(), &fixture.cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	err = tx.Commit()
	if err == nil || !strings.Contains(err.Error(), "fragment evidence changed after database publication") {
		t.Fatalf("Commit error = %v, want final fragment evidence refusal", err)
	}
	if tx.CatchRollbackSafe() {
		t.Fatal("post-database fragment drift incorrectly permits Catch rollback")
	}
	assertVMRuntimeAdoptionDatabaseGeneration(t, fixture.store, "devbox", true)
	assertVMRuntimeAdoptionDerivedGeneration(t, tx, true)
	if groups, err := tx.journal.LoadAll(); err != nil || len(groups) != 1 {
		t.Fatalf("journal after finalization refusal = %#v, %v", groups, err)
	}
	for _, call := range *systemctlCalls {
		if !slices.Equal(call, []string{"daemon-reload"}) {
			t.Fatalf("systemctl call = %v; drop-in validation must not restart a VM", call)
		}
	}
}

func TestVMRuntimeAdoptionCommittedMutationJoinsJournalTransitionFailure(t *testing.T) {
	fixture, deps, _ := newVMRuntimeAdoptionTransactionFixture(t, false)
	tx, err := prepareVMRuntimeAdoptionWithDeps(context.Background(), &fixture.cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	if err := tx.journal.Close(); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("confirmed database publication failed durability follow-up")
	err = tx.handleDatabaseMutationError(&db.PostPublicationError{Err: injected, MutationCommitted: true})
	if !errors.Is(err, injected) || !strings.Contains(err.Error(), "record committed VM runtime adoption database in journal") {
		t.Fatalf("committed mutation error = %v, want publication and journal transition failures", err)
	}
	if tx.CatchRollbackSafe() {
		t.Fatal("confirmed database publication incorrectly permits Catch rollback")
	}
}

func TestVMRuntimeAdoptionRecoveryDBNeitherWritesNothing(t *testing.T) {
	fixture, deps, _ := newVMRuntimeAdoptionTransactionFixture(t, false)
	injected := errors.New("leave database-new journal")
	deps.afterTransition = func(state string) error {
		if state == "database-published" {
			return injected
		}
		return nil
	}
	tx, err := prepareVMRuntimeAdoptionWithDeps(context.Background(), &fixture.cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	newUnit := readVMRuntimeAdoptionTestFile(t, fixture.unitPath)
	newDescriptor := readVMRuntimeAdoptionTestFile(t, tx.records[0].NewDescriptor.Path)
	thirdStore := db.NewStore(filepath.Join(fixture.dataRoot, "db.json"), fixture.servicesRoot)
	if _, err := thirdStore.MutateData(func(data *db.Data) error {
		data.Services["devbox"].VM.Components.GuestBase.ID = "third-generation"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	abandonPreparedVMRuntimeAdoption(t, tx)

	store, err := openVMRuntimeJournalStore(context.Background(), fixture.dataRoot, deps.journal)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	err = recoverVMRuntimeAdoptionsWithStore(context.Background(), &fixture.cfg, store, deps)
	if err == nil || !strings.Contains(err.Error(), "refusing derived-state recovery") {
		t.Fatalf("recovery error = %v, want DB-neither refusal", err)
	}
	if got := readVMRuntimeAdoptionTestFile(t, fixture.unitPath); !bytes.Equal(got, newUnit) {
		t.Fatal("DB-neither recovery changed unit")
	}
	if got := readVMRuntimeAdoptionTestFile(t, tx.records[0].NewDescriptor.Path); !bytes.Equal(got, newDescriptor) {
		t.Fatal("DB-neither recovery changed descriptor")
	}
	if groups, loadErr := store.LoadAll(); loadErr != nil || len(groups) != 1 {
		t.Fatalf("DB-neither recovery did not retain journal: %#v, %v", groups, loadErr)
	}
}

func TestVMRuntimeAdoptionRecoveryDBOldRestoresAndFinalizes(t *testing.T) {
	fixture, deps, _ := newVMRuntimeAdoptionTransactionFixture(t, false)
	injected := errors.New("leave database-old journal")
	deps.afterTransition = func(state string) error {
		if state == "derived-published" {
			return injected
		}
		return nil
	}
	tx, err := prepareVMRuntimeAdoptionWithDeps(context.Background(), &fixture.cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); !errors.Is(err, injected) {
		t.Fatalf("Commit error = %v, want injected failure", err)
	}
	assertVMRuntimeAdoptionDatabaseGeneration(t, fixture.store, "devbox", false)
	assertVMRuntimeAdoptionDerivedGeneration(t, tx, false)
	abandonPreparedVMRuntimeAdoption(t, tx)

	store, err := openVMRuntimeJournalStore(context.Background(), fixture.dataRoot, deps.journal)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := recoverVMRuntimeAdoptionsWithStore(context.Background(), &fixture.cfg, store, deps); err != nil {
		t.Fatal(err)
	}
	if groups, err := store.LoadAll(); err != nil || len(groups) != 0 {
		t.Fatalf("journals after DB-old recovery = %#v, %v", groups, err)
	}
}

func TestWithVMRuntimeTransactionLockReleasesOnPanic(t *testing.T) {
	fixture := newVMRuntimeAdoptionFixture(t, false)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("callback did not panic")
			}
		}()
		_ = WithVMRuntimeTransactionLock(context.Background(), &fixture.cfg, func() error { panic("boom") })
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := WithVMRuntimeTransactionLock(ctx, &fixture.cfg, func() error { return nil }); err != nil {
		t.Fatalf("lock remained held after panic: %v", err)
	}
}

func TestWithVMRuntimeTransactionLockRefusesPendingRecovery(t *testing.T) {
	fixture, deps, _ := newVMRuntimeAdoptionTransactionFixture(t, false)
	tx, err := prepareVMRuntimeAdoptionWithDeps(context.Background(), &fixture.cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	abandonPreparedVMRuntimeAdoption(t, tx)

	called := false
	err = WithVMRuntimeTransactionLock(context.Background(), &fixture.cfg, func() error {
		called = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "VM runtime recovery is pending") {
		t.Fatalf("WithVMRuntimeTransactionLock error = %v, want pending recovery", err)
	}
	if called {
		t.Fatal("callback ran while VM runtime recovery was pending")
	}
}

func TestWithVMRuntimeTransactionLockDoesNotCallBackAfterCancellation(t *testing.T) {
	fixture := newVMRuntimeAdoptionFixture(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	called := false
	err := WithVMRuntimeTransactionLock(ctx, &fixture.cfg, func() error {
		called = true
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("WithVMRuntimeTransactionLock error = %v, want context cancellation", err)
	}
	if called {
		t.Fatal("callback ran after context cancellation")
	}
}

func TestRemoveServiceRefusesPendingVMRuntimeRecoveryBeforeSideEffects(t *testing.T) {
	fixture, deps, _ := newVMRuntimeAdoptionTransactionFixture(t, false)
	tx, err := prepareVMRuntimeAdoptionWithDeps(context.Background(), &fixture.cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	abandonPreparedVMRuntimeAdoption(t, tx)

	server := &Server{cfg: fixture.cfg}
	report, err := server.RemoveService("devbox")
	if err == nil || !strings.Contains(err.Error(), "VM runtime recovery is pending") {
		t.Fatalf("RemoveService error = %v, want pending recovery", err)
	}
	if report != nil {
		t.Fatalf("RemoveService report = %#v, want nil before side effects", report)
	}
	data, err := fixture.store.Get()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := data.Services().GetOk("devbox"); !ok {
		t.Fatal("service was removed while VM runtime recovery was pending")
	}
}

func TestRemoveCmdRefusesPendingVMRuntimeRecoveryBeforeRunnerRemoval(t *testing.T) {
	fixture, deps, _ := newVMRuntimeAdoptionTransactionFixture(t, false)
	tx, err := prepareVMRuntimeAdoptionWithDeps(context.Background(), &fixture.cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	abandonPreparedVMRuntimeAdoption(t, tx)

	runner := &recordingServiceRunner{}
	execer := &ttyExecer{
		s:  &Server{cfg: fixture.cfg},
		sn: "devbox",
		serviceRunnerFn: func() (ServiceRunner, error) {
			return runner, nil
		},
	}
	err = execer.removeCmdFunc(cli.RemoveFlags{Yes: true})
	if err == nil || !strings.Contains(err.Error(), "VM runtime recovery is pending") {
		t.Fatalf("removeCmdFunc error = %v, want pending recovery", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner calls = %#v, want none before recovery", runner.calls)
	}
	data, err := fixture.store.Get()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := data.Services().GetOk("devbox"); !ok {
		t.Fatal("service was removed while VM runtime recovery was pending")
	}
}

func TestEnsureVMNetworkRefusesPendingVMRuntimeRecoveryBeforePlanning(t *testing.T) {
	fixture, deps, _ := newVMRuntimeAdoptionTransactionFixture(t, false)
	tx, err := prepareVMRuntimeAdoptionWithDeps(context.Background(), &fixture.cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	abandonPreparedVMRuntimeAdoption(t, tx)

	originalIdentity := vmNetworkEnsureRuntimeIdentity
	planningCalls := 0
	vmNetworkEnsureRuntimeIdentity = func() (vmRuntimeIdentity, error) {
		planningCalls++
		return vmRuntimeIdentity{}, nil
	}
	t.Cleanup(func() { vmNetworkEnsureRuntimeIdentity = originalIdentity })
	server := &Server{cfg: fixture.cfg}
	err = server.EnsureVMNetwork(context.Background(), "devbox")
	if err == nil || !strings.Contains(err.Error(), "VM runtime recovery is pending") {
		t.Fatalf("EnsureVMNetwork error = %v, want pending recovery", err)
	}
	if planningCalls != 0 {
		t.Fatalf("VM network planning calls = %d, want 0 before recovery", planningCalls)
	}
}

func TestVMRuntimeRecoveryWaitsOnJournalLockAndHonorsCancellation(t *testing.T) {
	fixture := newVMRuntimeAdoptionFixture(t, false)
	entered := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- WithVMRuntimeTransactionLock(context.Background(), &fixture.cfg, func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("journal lock holder did not start")
	}

	cancelled, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := RecoverVMRuntimeAdoptions(cancelled, &fixture.cfg); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("recovery while lock held = %v, want context deadline", err)
	}

	close(release)
	if err := <-holderDone; err != nil {
		t.Fatal(err)
	}
	ctx, cancelSuccess := context.WithTimeout(context.Background(), time.Second)
	defer cancelSuccess()
	if err := RecoverVMRuntimeAdoptions(ctx, &fixture.cfg); err != nil {
		t.Fatalf("recovery after lock release: %v", err)
	}
}

func TestServerStartupRecoveryWaitsForInstallerAndRecoversItsAbandonedJournal(t *testing.T) {
	fixture, deps, _ := newVMRuntimeAdoptionTransactionFixture(t, false)
	installLock, err := AcquireCatchInstallTransactionLock(context.Background(), fixture.dataRoot, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	installLockClosed := false
	t.Cleanup(func() {
		if !installLockClosed {
			_ = installLock.Close()
		}
	})

	server := &Server{cfg: fixture.cfg}
	lockAttempted := make(chan struct{})
	server.acquireCatchInstallLock = func(ctx context.Context, dataRoot string) (io.Closer, error) {
		close(lockAttempted)
		return AcquireCatchInstallTransactionLock(ctx, dataRoot, uint32(os.Geteuid()))
	}
	server.recoverVMRuntimeState = func(ctx context.Context, cfg *Config) error {
		journal, err := openVMRuntimeJournalStore(ctx, cfg.RootDir, deps.journal)
		if err != nil {
			return err
		}
		defer journal.Close()
		return recoverVMRuntimeAdoptionsWithStore(ctx, cfg, journal, deps)
	}

	recoveryDone := make(chan error, 1)
	go func() {
		recoveryDone <- server.recoverVMRuntimeStateAfterInstall(context.Background())
	}()
	select {
	case <-lockAttempted:
	case <-time.After(time.Second):
		t.Fatal("startup recovery did not wait on the install transaction")
	}
	select {
	case err := <-recoveryDone:
		t.Fatalf("startup recovery completed while installer was active: %v", err)
	default:
	}

	tx, err := prepareVMRuntimeAdoptionWithDeps(context.Background(), &fixture.cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	abandonPreparedVMRuntimeAdoption(t, tx)
	if err := installLock.Close(); err != nil {
		t.Fatal(err)
	}
	installLockClosed = true

	select {
	case err := <-recoveryDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("startup did not recover the abandoned journal after installer exit")
	}
	journal, err := openVMRuntimeJournalStore(context.Background(), fixture.dataRoot, deps.journal)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if groups, err := journal.LoadAll(); err != nil || len(groups) != 0 {
		t.Fatalf("journals after running-server recovery = %#v, %v", groups, err)
	}
}

func abandonPreparedVMRuntimeAdoption(t *testing.T, tx *VMRuntimeAdoption) {
	t.Helper()
	tx.closed = true
	if err := errors.Join(tx.units.Close(), tx.descriptors.Close(), tx.journal.Close()); err != nil {
		t.Fatalf("abandon prepared VM runtime adoption: %v", err)
	}
}

func newVMRuntimeAdoptionTransactionFixture(t *testing.T, stopped bool) (*vmRuntimeAdoptionFixture, vmRuntimeAdoptionCoordinatorDeps, *[][]string) {
	t.Helper()
	fixture := newVMRuntimeAdoptionFixture(t, false)
	oldSystemdDir := vmSystemdSystemDir
	vmSystemdSystemDir = filepath.Dir(fixture.unitPath)
	t.Cleanup(func() { vmSystemdSystemDir = oldSystemdDir })

	oldUnit := "[Service]\nExecStart=" + strings.Join(systemdVMExecArguments(fixture.unitExec), " ") + "\n"
	writeVMRuntimeAdoptionTestFile(t, fixture.unitPath, oldUnit, 0o644)
	activeState, mainPID := "active", 4242
	if stopped {
		activeState, mainPID = "inactive", 0
	}
	fixture.deps.loadUnit = func(_ context.Context, unit string) (vmRuntimeAdoptionLoadedUnit, error) {
		if unit != fixture.unitName {
			return vmRuntimeAdoptionLoadedUnit{}, fmt.Errorf("unexpected unit %s", unit)
		}
		raw := readVMRuntimeAdoptionTestFile(t, fixture.unitPath)
		withHeader := append([]byte("# "+fixture.unitPath+"\n"), raw...)
		argv, paths, err := parseVMRuntimeAdoptionUnit(withHeader)
		if err != nil {
			return vmRuntimeAdoptionLoadedUnit{}, err
		}
		evidence, err := collectTrustedVMRuntimeAdoptionFileEvidence(fixture.unitPath, true, fixture.deps.evidence)
		if err != nil {
			return vmRuntimeAdoptionLoadedUnit{}, err
		}
		fragments := make([]vmRuntimeAdoptionUnitFragment, len(paths))
		for i, path := range paths {
			fragments[i] = vmRuntimeAdoptionUnitFragment{Path: path, Evidence: evidence}
		}
		return vmRuntimeAdoptionLoadedUnit{
			Name: unit, ExecStart: argv, Fragments: fragments,
			ActiveState: activeState, MainPID: mainPID, NeedDaemonReload: "no",
		}, nil
	}

	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
	deps := defaultVMRuntimeAdoptionCoordinatorDeps()
	deps.inventory = fixture.deps
	deps.journal.uid = uid
	deps.descriptor.uid, deps.descriptor.gid = uid, gid
	deps.unit.uid, deps.unit.gid = uid, gid
	deps.provenance.trustedUID = uid
	systemctlCalls := &[][]string{}
	deps.unit.systemctl = func(args ...string) error {
		*systemctlCalls = append(*systemctlCalls, append([]string(nil), args...))
		return nil
	}
	return fixture, deps, systemctlCalls
}

func configureVMRuntimeAdoptionBundle(
	t *testing.T,
	fixture *vmRuntimeAdoptionFixture,
	deps *vmRuntimeAdoptionCoordinatorDeps,
	version, kernelVersion, runtimeVersion string,
	withManifest, syncedKernel bool,
) {
	t.Helper()
	newImageDir := filepath.Join(filepath.Dir(fixture.imageDir), version)
	if err := os.Rename(fixture.imageDir, newImageDir); err != nil {
		t.Fatal(err)
	}
	fixture.imageDir = newImageDir
	fixture.rootFS = filepath.Join(newImageDir, "rootfs.ext4")
	fixture.firecracker = filepath.Join(newImageDir, "firecracker")
	fixture.jailer = filepath.Join(newImageDir, "jailer")
	fixture.kernel = filepath.Join(newImageDir, "vmlinux")
	if syncedKernel {
		syncedDir := filepath.Join(serviceRunDirForRoot(fixture.serviceRoot), "kernels", fixture.service.Name, kernelVersion)
		syncedKernelPath := filepath.Join(syncedDir, "vmlinux")
		if err := os.MkdirAll(syncedDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(fixture.kernel, syncedKernelPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(newImageDir, "kernel.config"), filepath.Join(syncedDir, "kernel.config")); err != nil {
			t.Fatal(err)
		}
		fixture.kernel = syncedKernelPath
	}
	fixture.service.VM.Image.Version = version
	fixture.service.VM.Image.RootFS = fixture.rootFS
	fixture.service.VM.Image.Kernel = fixture.kernel
	fixture.writeFirecrackerConfig(t)
	fixture.unitExec = fixture.execStart()
	writeVMRuntimeAdoptionTestFile(t, fixture.unitPath, "[Service]\nExecStart="+strings.Join(systemdVMExecArguments(fixture.unitExec), " ")+"\n", 0o644)
	if withManifest {
		writeVMRuntimeAdoptionTestJSON(t, filepath.Join(newImageDir, "manifest.json"), fixture.newManifest(t), 0o644)
	}
	fixture.deps.runtimePair = func(context.Context, string, string) (string, error) { return runtimeVersion, nil }
	deps.inventory.runtimePair = fixture.deps.runtimePair
	fixture.persist(t)
}

func assertVMRuntimeAdoptionDatabaseGeneration(t *testing.T, store *db.Store, service string, wantNew bool) {
	t.Helper()
	latest := readLatestVMRuntimeAdoptionData(t, store)
	components := latest.Services[service].VM.Components
	if (components != nil) != wantNew {
		t.Fatalf("database Components present = %v, want %v", components != nil, wantNew)
	}
}

func readLatestVMRuntimeAdoptionData(t *testing.T, store *db.Store) *db.Data {
	t.Helper()
	var latest *db.Data
	if err := store.WithLatestDataLocked(func(view db.DataView) error {
		latest = view.AsStruct()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return latest
}

func assertVMRuntimeAdoptionDerivedGeneration(t *testing.T, tx *VMRuntimeAdoption, wantNew bool) {
	t.Helper()
	if wantNew {
		if err := tx.verifyNewDerived(context.Background()); err != nil {
			t.Fatalf("verify new derived generation: %v", err)
		}
		return
	}
	if err := tx.verifyOldDerived(context.Background()); err != nil {
		t.Fatalf("verify old derived generation: %v", err)
	}
}
