// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func seedMissingRuntimeMetadata(t *testing.T, f *vmRuntimeAdoptionFixture) vmRuntimeDescriptor {
	t.Helper()
	artifact := f.onlyVM(t).Components.Runtime.Configured
	descriptor := vmRuntimeDescriptor{SchemaVersion: 1, Service: f.service.Name, Configured: artifact, Previous: &artifact}
	path := filepath.Join(serviceDataDirForRoot(f.serviceRoot), vmRuntimeDescriptorFileName)
	writeVMRuntimeAdoptionTestJSON(t, path, descriptor, 0o600)
	args := []string{}
	for i := 0; i < len(f.unitExec); i++ {
		if f.unitExec[i] == "--firecracker" || f.unitExec[i] == "--jailer" {
			i++
			continue
		}
		args = append(args, f.unitExec[i])
	}
	args = append(args, "--runtime-descriptor", path, "--runtime-running-marker", filepath.Join(serviceRunDirForRoot(f.serviceRoot), vmRuntimeRunningMarkerFileName), "--runtime-trial-result", filepath.Join(serviceRunDirForRoot(f.serviceRoot), vmRuntimeTrialResultFileName))
	f.unitExec = args
	writeVMRuntimeAdoptionTestFile(t, f.unitPath, "[Service]\nExecStart="+strings.Join(systemdVMExecArguments(args), " ")+"\n", 0o644)
	return descriptor
}

func TestVMRuntimeMetadataRecoveryPreservesLaunchAndIsIdempotent(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		t.Run(map[bool]string{false: "running", true: "stopped"}[stopped], func(t *testing.T) {
			f, deps, calls := newVMRuntimeAdoptionTransactionFixture(t, stopped)
			descriptor := seedMissingRuntimeMetadata(t, f)
			path := filepath.Join(serviceDataDirForRoot(f.serviceRoot), vmRuntimeDescriptorFileName)
			beforeDescriptor := readVMRuntimeAdoptionTestFile(t, path)
			beforeUnit := readVMRuntimeAdoptionTestFile(t, f.unitPath)
			beforeDisk := readVMRuntimeAdoptionTestFile(t, f.disk)
			tx, err := prepareVMRuntimeAdoptionWithDeps(context.Background(), &f.cfg, deps)
			if err != nil {
				t.Fatal(err)
			}
			if len(tx.Summary().Adopting) != 1 {
				t.Fatalf("summary: %#v", tx.Summary())
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if err := tx.Close(); err != nil {
				t.Fatal(err)
			}
			vm := readLatestVMRuntimeAdoptionData(t, f.store).Services[f.service.Name].VM
			if vm.Components == nil || vm.Components.Runtime.Configured != descriptor.Configured || *vm.Components.Runtime.Previous != *descriptor.Previous {
				t.Fatalf("components: %#v", vm.Components)
			}
			for _, pair := range []struct {
				path   string
				before []byte
			}{{path, beforeDescriptor}, {f.unitPath, beforeUnit}, {f.disk, beforeDisk}} {
				if !bytes.Equal(readVMRuntimeAdoptionTestFile(t, pair.path), pair.before) {
					t.Fatalf("changed %s", pair.path)
				}
			}
			for _, call := range *calls {
				if strings.Join(call, " ") != "daemon-reload" {
					t.Fatalf("unexpected service operation %v", call)
				}
			}
			again, err := prepareVMRuntimeAdoptionWithDeps(context.Background(), &f.cfg, deps)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = again.Close() }()
			if again.Summary().HasChanges {
				t.Fatalf("second repair changed state: %#v", again.Summary())
			}
		})
	}
}

func TestVMRuntimeMetadataRecoveryBlocksUnverifiedInputs(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*testing.T, *vmRuntimeAdoptionFixture, vmRuntimeDescriptor)
		want   string
	}{
		{"digest", func(t *testing.T, f *vmRuntimeAdoptionFixture, _ vmRuntimeDescriptor) {
			writeVMRuntimeAdoptionTestFile(t, f.firecracker, "changed", 0o755)
		}, "digest mismatch"},
		{"trial", func(t *testing.T, f *vmRuntimeAdoptionFixture, d vmRuntimeDescriptor) {
			d.Staged = cloneVMRuntimeArtifact(&d.Configured)
			d.Staged.ID = "candidate"
			d.Trial = true
			writeVMRuntimeAdoptionTestJSON(t, filepath.Join(serviceDataDirForRoot(f.serviceRoot), vmRuntimeDescriptorFileName), d, 0o600)
		}, "staged trial"},
		{"wrong service", func(t *testing.T, f *vmRuntimeAdoptionFixture, d vmRuntimeDescriptor) {
			d.Service = "other"
			writeVMRuntimeAdoptionTestJSON(t, filepath.Join(serviceDataDirForRoot(f.serviceRoot), vmRuntimeDescriptorFileName), d, 0o600)
		}, "service"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newVMRuntimeAdoptionFixture(t, false)
			d := seedMissingRuntimeMetadata(t, f)
			test.change(t, f, d)
			vm := f.onlyVM(t)
			if vm.Components != nil || !strings.Contains(vm.BlockedReason, test.want) {
				t.Fatalf("preparation: %#v", vm)
			}
		})
	}
}

func TestVMRuntimeStatusIncludesMissingMetadata(t *testing.T) {
	s := newTestServer(t)
	_, _, err := s.cfg.DB.MutateService("devbox", func(_ *db.Data, service *db.Service) error {
		service.ServiceType = db.ServiceTypeVM
		service.VM = &db.VMConfig{}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s.vmRuntimeCommandDeps = vmRuntimeStatusTestDeps(validVMRuntimeCatalog(), vmRuntimeUnitState{}, func(int) bool { return false })
	for _, selected := range []string{"", "devbox"} {
		rows, err := s.vmRuntimeStatusRows(context.Background(), selected)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].State != "metadata-missing" || rows[0].RecommendedAction == "" {
			t.Fatalf("rows: %#v", rows)
		}
	}
}

func TestVMRuntimeMetadataRecoverySurvivesInterruptedCommit(t *testing.T) {
	for _, point := range []string{"derived-published", "database-published"} {
		t.Run(point, func(t *testing.T) {
			f, deps, _ := newVMRuntimeAdoptionTransactionFixture(t, false)
			seedMissingRuntimeMetadata(t, f)
			path := filepath.Join(serviceDataDirForRoot(f.serviceRoot), vmRuntimeDescriptorFileName)
			before := readVMRuntimeAdoptionTestFile(t, path)
			injected := errors.New("interrupted repair")
			deps.afterTransition = func(state string) error {
				if state == point {
					return injected
				}
				return nil
			}
			tx, err := prepareVMRuntimeAdoptionWithDeps(context.Background(), &f.cfg, deps)
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); !errors.Is(err, injected) {
				t.Fatalf("commit: %v", err)
			}
			if err := tx.Close(); err != nil {
				t.Fatal(err)
			}
			deps.afterTransition = nil
			again, err := prepareVMRuntimeAdoptionWithDeps(context.Background(), &f.cfg, deps)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = again.Close() }()
			if err := again.Commit(); err != nil {
				t.Fatal(err)
			}
			assertVMRuntimeAdoptionDatabaseGeneration(t, f.store, f.service.Name, true)
			if !bytes.Equal(before, readVMRuntimeAdoptionTestFile(t, path)) {
				t.Fatal("descriptor changed")
			}
		})
	}
}

func TestVMRuntimeMetadataRecoveryRejectsDescriptorDrift(t *testing.T) {
	f, deps, _ := newVMRuntimeAdoptionTransactionFixture(t, false)
	seedMissingRuntimeMetadata(t, f)
	tx, err := prepareVMRuntimeAdoptionWithDeps(context.Background(), &f.cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Close() }()
	path := filepath.Join(serviceDataDirForRoot(f.serviceRoot), vmRuntimeDescriptorFileName)
	writeVMRuntimeAdoptionTestFile(t, path, "changed", 0o600)
	if err := tx.Commit(); err == nil {
		t.Fatal("repair accepted descriptor drift")
	}
	assertVMRuntimeAdoptionDatabaseGeneration(t, f.store, f.service.Name, false)
}
