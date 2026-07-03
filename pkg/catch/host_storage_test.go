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
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/registry"
	"github.com/yeetrun/yeet/pkg/svc"
)

func TestHostStoragePlanNoop(t *testing.T) {
	root := t.TempDir()
	servicesRoot := filepath.Join(root, "services")
	planner := newTestHostStoragePlanner(t, Config{
		RootDir:      root,
		ServicesRoot: servicesRoot,
	}, nil)

	plan, err := planner.Plan(context.Background(), catchrpc.HostStoragePlanRequest{
		Set: catchrpc.HostStorageSetRequest{
			DataDir:      &catchrpc.HostStorageTarget{Value: root},
			ServicesRoot: &catchrpc.HostStorageTarget{Value: servicesRoot},
		},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.DataDirAction.Move {
		t.Fatalf("DataDirAction.Move = true, want false")
	}
	if len(plan.ServicesAction.AffectedServices) != 0 {
		t.Fatalf("AffectedServices = %#v, want none", plan.ServicesAction.AffectedServices)
	}
	if plan.RequiresRestart {
		t.Fatalf("RequiresRestart = true, want false")
	}
}

func TestHostStoragePlanCleanEquivalentDataDirNoop(t *testing.T) {
	root := filepath.Join(t.TempDir(), "yeet")
	planner := newTestHostStoragePlanner(t, Config{
		RootDir:      root + string(filepath.Separator),
		ServicesRoot: filepath.Join(root, "services"),
	}, nil)

	plan, err := planner.Plan(context.Background(), catchrpc.HostStoragePlanRequest{
		Set: catchrpc.HostStorageSetRequest{
			DataDir: &catchrpc.HostStorageTarget{Value: root},
		},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.DataDirAction.Move {
		t.Fatalf("DataDirAction.Move = true, want false for clean-equivalent data dirs")
	}
	if plan.RequiresRestart {
		t.Fatalf("RequiresRestart = true, want false for clean-equivalent data dirs")
	}
}

func TestHostStoragePlanCleanEquivalentServicesRootNoop(t *testing.T) {
	root := filepath.Join(t.TempDir(), "yeet")
	servicesRoot := filepath.Join(root, "services")
	planner := newTestHostStoragePlanner(t, Config{
		RootDir:      root,
		ServicesRoot: servicesRoot + string(filepath.Separator),
	}, nil)

	plan, err := planner.Plan(context.Background(), catchrpc.HostStoragePlanRequest{
		Set: catchrpc.HostStorageSetRequest{
			ServicesRoot: &catchrpc.HostStorageTarget{Value: servicesRoot},
		},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.ServicesAction.AffectedServices) != 0 {
		t.Fatalf("AffectedServices = %#v, want none for clean-equivalent services roots", plan.ServicesAction.AffectedServices)
	}
	if plan.RequiresRestart {
		t.Fatalf("RequiresRestart = true, want false for clean-equivalent services roots")
	}
}

func TestHostStoragePlanDetectsRepairOnlyOldRootRefs(t *testing.T) {
	systemdDir := t.TempDir()
	oldSystemdDir := systemdSystemDir
	systemdSystemDir = systemdDir
	t.Cleanup(func() { systemdSystemDir = oldSystemdDir })
	store := db.NewStore(filepath.Join(t.TempDir(), "db.json"), "/flash/yeet/services")
	if err := store.Set(&db.Data{
		DataVersion: db.CurrentDataVersion,
		Services: map[string]*db.Service{
			CatchService: {
				Name:        CatchService,
				ServiceType: db.ServiceTypeSystemd,
				Generation:  2,
				Artifacts: db.ArtifactStore{
					db.ArtifactBinary: {Refs: map[db.ArtifactRef]string{
						db.Gen(2): "/root/data/services/catch/bin/catch",
					}},
				},
			},
		},
	}); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	storage, err := registry.NewFilesystemStorage(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystemStorage: %v", err)
	}
	server := NewUnstartedServer(&Config{
		RootDir:         "/flash/yeet/data",
		ServicesRoot:    "/flash/yeet/services",
		DB:              store,
		RegistryStorage: storage,
	})

	plan, err := server.PlanHostStorage(context.Background(), catchrpc.HostStoragePlanRequest{
		Set: catchrpc.HostStorageSetRequest{
			ServicesRoot:    &catchrpc.HostStorageTarget{Value: "/flash/yeet/services"},
			MigrateServices: catchrpc.HostStorageMigrateAll,
		},
	})
	if err != nil {
		t.Fatalf("PlanHostStorage error: %v", err)
	}
	if plan.RepairAction.References == 0 {
		t.Fatalf("RepairAction.References = 0, want repair refs")
	}
	if plan.RepairAction.DatabaseRefs == 0 {
		t.Fatalf("RepairAction.DatabaseRefs = 0, want database repair refs")
	}
	if len(plan.RepairAction.RestartServices) != 0 {
		t.Fatalf("RepairAction.RestartServices = %#v, want no restart target for catch DB refs", plan.RepairAction.RestartServices)
	}
	if !slices.Contains(plan.RepairAction.ValidationRoots, "/root/data") {
		t.Fatalf("RepairAction.ValidationRoots = %#v, want /root/data", plan.RepairAction.ValidationRoots)
	}
	if !plan.RequiresRestart {
		t.Fatalf("RequiresRestart = false, want true for repair")
	}
}

func TestHostStoragePlanDetectsRepairOnlyInstallUserDefaultRefs(t *testing.T) {
	systemdDir := t.TempDir()
	oldSystemdDir := systemdSystemDir
	systemdSystemDir = systemdDir
	t.Cleanup(func() { systemdSystemDir = oldSystemdDir })
	store := db.NewStore(filepath.Join(t.TempDir(), "db.json"), "/flash/yeet/services")
	if err := store.Set(&db.Data{
		DataVersion: db.CurrentDataVersion,
		Services: map[string]*db.Service{
			"api": {
				Name:        "api",
				ServiceType: db.ServiceTypeSystemd,
				Generation:  1,
				Artifacts: db.ArtifactStore{
					db.ArtifactBinary: {Refs: map[db.ArtifactRef]string{
						db.Gen(1): "/home/ubuntu/data/services/api/bin/api",
					}},
				},
			},
			CatchService: {
				Name:        CatchService,
				ServiceType: db.ServiceTypeSystemd,
			},
		},
	}); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	storage, err := registry.NewFilesystemStorage(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystemStorage: %v", err)
	}
	server := NewUnstartedServer(&Config{
		RootDir:         "/flash/yeet/data",
		ServicesRoot:    "/flash/yeet/services",
		InstallUser:     "ubuntu",
		DB:              store,
		RegistryStorage: storage,
	})

	plan, err := server.PlanHostStorage(context.Background(), catchrpc.HostStoragePlanRequest{
		Set: catchrpc.HostStorageSetRequest{
			ServicesRoot:    &catchrpc.HostStorageTarget{Value: "/flash/yeet/services"},
			MigrateServices: catchrpc.HostStorageMigrateAll,
		},
	})
	if err != nil {
		t.Fatalf("PlanHostStorage error: %v", err)
	}
	if plan.RepairAction.DatabaseRefs == 0 {
		t.Fatalf("RepairAction.DatabaseRefs = 0, want install-user default repair refs")
	}
	if !slices.Contains(plan.RepairAction.ValidationRoots, "/home/ubuntu/data") {
		t.Fatalf("RepairAction.ValidationRoots = %#v, want /home/ubuntu/data", plan.RepairAction.ValidationRoots)
	}
	if !slices.Contains(plan.RepairAction.RestartServices, "api") {
		t.Fatalf("RepairAction.RestartServices = %#v, want api", plan.RepairAction.RestartServices)
	}
}

func TestHostStoragePlanDetectsRepairOnlySystemdRefs(t *testing.T) {
	systemdDir := t.TempDir()
	oldSystemdDir := systemdSystemDir
	systemdSystemDir = systemdDir
	t.Cleanup(func() { systemdSystemDir = oldSystemdDir })
	for _, unit := range []string{"api.service", "catch.service", "sys.service", "yeet-sys-ns.service"} {
		if err := os.WriteFile(
			filepath.Join(systemdDir, unit),
			[]byte("[Service]\nExecStart=/root/data/services/api/bin/api\n"),
			0o644,
		); err != nil {
			t.Fatalf("WriteFile systemd unit %s: %v", unit, err)
		}
	}
	store := db.NewStore(filepath.Join(t.TempDir(), "db.json"), "/flash/yeet/services")
	if err := store.Set(&db.Data{
		DataVersion: db.CurrentDataVersion,
		Services: map[string]*db.Service{
			CatchService: {
				Name:        CatchService,
				ServiceType: db.ServiceTypeSystemd,
			},
		},
	}); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	storage, err := registry.NewFilesystemStorage(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystemStorage: %v", err)
	}
	server := NewUnstartedServer(&Config{
		RootDir:         "/flash/yeet/data",
		ServicesRoot:    "/flash/yeet/services",
		DB:              store,
		RegistryStorage: storage,
	})

	plan, err := server.PlanHostStorage(context.Background(), catchrpc.HostStoragePlanRequest{
		Set: catchrpc.HostStorageSetRequest{
			ServicesRoot:    &catchrpc.HostStorageTarget{Value: "/flash/yeet/services"},
			MigrateServices: catchrpc.HostStorageMigrateAll,
		},
	})
	if err != nil {
		t.Fatalf("PlanHostStorage error: %v", err)
	}
	if plan.RepairAction.SystemdRefs != 4 {
		t.Fatalf("RepairAction.SystemdRefs = %d, want 4", plan.RepairAction.SystemdRefs)
	}
	if !slices.Contains(plan.RepairAction.RegenerateUnits, "api.service") {
		t.Fatalf("RepairAction.RegenerateUnits = %#v, want api.service", plan.RepairAction.RegenerateUnits)
	}
	if !slices.Contains(plan.RepairAction.RestartServices, "api") {
		t.Fatalf("RepairAction.RestartServices = %#v, want api", plan.RepairAction.RestartServices)
	}
	for _, denied := range []string{CatchService, SystemService} {
		if slices.Contains(plan.RepairAction.RestartServices, denied) {
			t.Fatalf("RepairAction.RestartServices = %#v, want no self-managed service %q", plan.RepairAction.RestartServices, denied)
		}
	}
	if !plan.RequiresRestart {
		t.Fatalf("RequiresRestart = false, want true for systemd repair")
	}
}

func TestHostStoragePlanDetectsRepairOnlyGeneratedArtifactRefs(t *testing.T) {
	systemdDir := t.TempDir()
	oldSystemdDir := systemdSystemDir
	systemdSystemDir = systemdDir
	t.Cleanup(func() { systemdSystemDir = oldSystemdDir })
	artifactPath := filepath.Join(t.TempDir(), "yeet-api-ns.service")
	if err := os.WriteFile(artifactPath, []byte("ExecStart=/root/data/services/catch/data/service-ns\n"), 0o644); err != nil {
		t.Fatalf("WriteFile generated artifact: %v", err)
	}
	store := db.NewStore(filepath.Join(t.TempDir(), "db.json"), "/flash/yeet/services")
	if err := store.Set(&db.Data{
		DataVersion: db.CurrentDataVersion,
		Services: map[string]*db.Service{
			CatchService: {
				Name:        CatchService,
				ServiceType: db.ServiceTypeSystemd,
			},
			"api": {
				Name:        "api",
				ServiceType: db.ServiceTypeDockerCompose,
				Generation:  2,
				Artifacts: db.ArtifactStore{
					db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{
						db.Gen(1): artifactPath,
						db.Gen(2): artifactPath,
						"latest":  artifactPath,
					}},
				},
			},
		},
	}); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	storage, err := registry.NewFilesystemStorage(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystemStorage: %v", err)
	}
	server := NewUnstartedServer(&Config{
		RootDir:         "/flash/yeet/data",
		ServicesRoot:    "/flash/yeet/services",
		DB:              store,
		RegistryStorage: storage,
	})

	plan, err := server.PlanHostStorage(context.Background(), catchrpc.HostStoragePlanRequest{
		Set: catchrpc.HostStorageSetRequest{
			ServicesRoot:    &catchrpc.HostStorageTarget{Value: "/flash/yeet/services"},
			MigrateServices: catchrpc.HostStorageMigrateAll,
		},
	})
	if err != nil {
		t.Fatalf("PlanHostStorage error: %v", err)
	}
	if plan.RepairAction.ArtifactRefs != 1 {
		t.Fatalf("RepairAction.ArtifactRefs = %d, want 1", plan.RepairAction.ArtifactRefs)
	}
	if plan.RepairAction.DatabaseRefs != 0 || plan.RepairAction.SystemdRefs != 0 {
		t.Fatalf("RepairAction DB/Systemd refs = %d/%d, want 0/0", plan.RepairAction.DatabaseRefs, plan.RepairAction.SystemdRefs)
	}
	if !slices.Contains(plan.RepairAction.RestartServices, "api") {
		t.Fatalf("RepairAction.RestartServices = %#v, want api", plan.RepairAction.RestartServices)
	}
	if !slices.Contains(plan.RepairAction.ValidationRoots, "/root/data") {
		t.Fatalf("RepairAction.ValidationRoots = %#v, want /root/data", plan.RepairAction.ValidationRoots)
	}
	if !plan.RequiresRestart {
		t.Fatalf("RequiresRestart = false, want true for generated artifact repair")
	}
}

func TestHostStoragePlanMigrateAllMovesServicesUnderOldRoot(t *testing.T) {
	root := t.TempDir()
	oldServicesRoot := filepath.Join(root, "services")
	newServicesRoot := filepath.Join(root, "services2")
	customRoot := filepath.Join(t.TempDir(), "custom-db")
	planner := newTestHostStoragePlanner(t, Config{
		RootDir:      root,
		ServicesRoot: oldServicesRoot,
	}, map[string]*db.Service{
		"api":    {Name: "api"},
		"worker": {Name: "worker", ServiceRoot: filepath.Join(oldServicesRoot, "worker")},
		"db":     {Name: "db", ServiceRoot: customRoot},
	})

	plan, err := planner.Plan(context.Background(), catchrpc.HostStoragePlanRequest{
		Set: catchrpc.HostStorageSetRequest{
			ServicesRoot:    &catchrpc.HostStorageTarget{Value: newServicesRoot},
			MigrateServices: catchrpc.HostStorageMigrateAll,
		},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	got := plan.ServicesAction.AffectedServices
	want := []catchrpc.HostStorageServiceMove{
		{Name: "api", From: filepath.Join(oldServicesRoot, "api"), To: filepath.Join(newServicesRoot, "api")},
		{Name: "worker", From: filepath.Join(oldServicesRoot, "worker"), To: filepath.Join(newServicesRoot, "worker")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AffectedServices = %#v, want %#v", got, want)
	}
	if plan.ServicesAction.Mode != catchrpc.HostStorageMigrateAll {
		t.Fatalf("Mode = %q, want %q", plan.ServicesAction.Mode, catchrpc.HostStorageMigrateAll)
	}
}

func TestHostStoragePlanMigrateAllSkipsSelfManagedServices(t *testing.T) {
	root := t.TempDir()
	oldServicesRoot := filepath.Join(root, "services")
	newServicesRoot := filepath.Join(root, "services2")
	planner := newTestHostStoragePlanner(t, Config{
		RootDir:      root,
		ServicesRoot: oldServicesRoot,
	}, map[string]*db.Service{
		"api":        {Name: "api"},
		CatchService: {Name: CatchService, ServiceType: db.ServiceTypeSystemd},
		SystemService: {
			Name:        SystemService,
			ServiceType: db.ServiceTypeSystemd,
		},
	})

	plan, err := planner.Plan(context.Background(), catchrpc.HostStoragePlanRequest{
		Set: catchrpc.HostStorageSetRequest{
			ServicesRoot:    &catchrpc.HostStorageTarget{Value: newServicesRoot},
			MigrateServices: catchrpc.HostStorageMigrateAll,
		},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	got := plan.ServicesAction.AffectedServices
	want := []catchrpc.HostStorageServiceMove{
		{Name: "api", From: filepath.Join(oldServicesRoot, "api"), To: filepath.Join(newServicesRoot, "api")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AffectedServices = %#v, want %#v", got, want)
	}
}

func TestHostStoragePlanMigrateAllSkipsSiblingPrefixCustomRoot(t *testing.T) {
	root := t.TempDir()
	oldServicesRoot := filepath.Join(root, "apps")
	newServicesRoot := filepath.Join(root, "apps-new")
	planner := newTestHostStoragePlanner(t, Config{
		RootDir:      root,
		ServicesRoot: oldServicesRoot,
	}, map[string]*db.Service{
		"api": {Name: "api"},
		"db":  {Name: "db", ServiceRoot: filepath.Join(root, "apps2", "db")},
	})

	plan, err := planner.Plan(context.Background(), catchrpc.HostStoragePlanRequest{
		Set: catchrpc.HostStorageSetRequest{
			ServicesRoot:    &catchrpc.HostStorageTarget{Value: newServicesRoot},
			MigrateServices: catchrpc.HostStorageMigrateAll,
		},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	got := plan.ServicesAction.AffectedServices
	want := []catchrpc.HostStorageServiceMove{
		{Name: "api", From: filepath.Join(oldServicesRoot, "api"), To: filepath.Join(newServicesRoot, "api")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AffectedServices = %#v, want %#v", got, want)
	}
}

func TestHostStoragePlanMigrateAllMovesFlatSiblingZFSDatasetsWhenServicesRootAlreadyDesired(t *testing.T) {
	root := t.TempDir()
	servicesRoot := filepath.Join(root, "services")
	for _, dir := range []string{
		servicesRoot,
		filepath.Join(root, "plex"),
		filepath.Join(root, "radarr"),
		filepath.Join(root, "vms", "router"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", dir, err)
		}
	}
	planner := newTestHostStoragePlanner(t, Config{
		RootDir:      filepath.Join(root, "data"),
		ServicesRoot: servicesRoot,
	}, map[string]*db.Service{
		"custom": {Name: "custom", ServiceRoot: "/flash/other/custom", ServiceRootZFS: "flash/other/custom"},
		"newt":   {Name: "newt", ServiceRoot: filepath.Join(servicesRoot, "newt"), ServiceRootZFS: "tank/yeet/services/newt"},
		"plex":   {Name: "plex", ServiceRoot: filepath.Join(root, "plex"), ServiceRootZFS: "tank/yeet/plex"},
		"radarr": {Name: "radarr", ServiceRoot: filepath.Join(root, "radarr"), ServiceRootZFS: "tank/yeet/radarr"},
		"router": {Name: "router", ServiceRoot: filepath.Join(root, "vms", "router"), ServiceRootZFS: "tank/yeet/vms/router"},
	})
	planner.zfs = hostStorageTestZFSRunner(map[string]string{
		"tank/yeet/services": servicesRoot,
	})

	plan, err := planner.Plan(context.Background(), catchrpc.HostStoragePlanRequest{
		Set: catchrpc.HostStorageSetRequest{
			ServicesRoot:    &catchrpc.HostStorageTarget{Value: "tank/yeet/services", ZFS: true},
			MigrateServices: catchrpc.HostStorageMigrateAll,
		},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	got := plan.ServicesAction.AffectedServices
	want := []catchrpc.HostStorageServiceMove{
		{Name: "plex", From: filepath.Join(root, "plex"), To: filepath.Join(servicesRoot, "plex"), ToZFS: "tank/yeet/services/plex"},
		{Name: "radarr", From: filepath.Join(root, "radarr"), To: filepath.Join(servicesRoot, "radarr"), ToZFS: "tank/yeet/services/radarr"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AffectedServices = %#v, want %#v", got, want)
	}
}

func TestHostStoragePlanMigrateNoneReturnsPersistenceActions(t *testing.T) {
	root := t.TempDir()
	oldServicesRoot := filepath.Join(root, "services")
	newServicesRoot := filepath.Join(root, "services2")
	customRoot := filepath.Join(t.TempDir(), "custom-db")
	planner := newTestHostStoragePlanner(t, Config{
		RootDir:      root,
		ServicesRoot: oldServicesRoot,
	}, map[string]*db.Service{
		"api": {Name: "api"},
		"db":  {Name: "db", ServiceRoot: customRoot},
	})

	plan, err := planner.Plan(context.Background(), catchrpc.HostStoragePlanRequest{
		Set: catchrpc.HostStorageSetRequest{
			ServicesRoot:    &catchrpc.HostStorageTarget{Value: newServicesRoot},
			MigrateServices: catchrpc.HostStorageMigrateNone,
		},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	got := plan.ServicesAction.AffectedServices
	want := []catchrpc.HostStorageServiceMove{
		{Name: "api", From: filepath.Join(oldServicesRoot, "api"), To: filepath.Join(oldServicesRoot, "api")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AffectedServices = %#v, want %#v", got, want)
	}
	if plan.ServicesAction.Mode != catchrpc.HostStorageMigrateNone {
		t.Fatalf("Mode = %q, want %q", plan.ServicesAction.Mode, catchrpc.HostStorageMigrateNone)
	}
}

func TestPlanServicesRootBatchAllMovesAffectedServices(t *testing.T) {
	root := t.TempDir()
	oldServicesRoot := filepath.Join(root, "services")
	newServicesRoot := filepath.Join(root, "services2")
	customRoot := filepath.Join(t.TempDir(), "custom-db")

	plan, err := planServicesRootBatch(context.Background(), Config{ServicesRoot: oldServicesRoot}, []db.Service{
		{Name: "api"},
		{Name: "worker", ServiceRoot: filepath.Join(oldServicesRoot, "worker")},
		{Name: "nested", ServiceRoot: filepath.Join(oldServicesRoot, "team", "nested")},
		{Name: "db", ServiceRoot: customRoot},
	}, oldServicesRoot, newServicesRoot, catchrpc.HostStorageMigrateAll)
	if err != nil {
		t.Fatalf("planServicesRootBatch: %v", err)
	}
	want := []serviceRootMigrationPlan{
		{ServiceName: "api", OldRoot: filepath.Join(oldServicesRoot, "api"), NewRoot: filepath.Join(newServicesRoot, "api")},
		{ServiceName: "nested", OldRoot: filepath.Join(oldServicesRoot, "team", "nested"), NewRoot: filepath.Join(newServicesRoot, "nested")},
		{ServiceName: "worker", OldRoot: filepath.Join(oldServicesRoot, "worker"), NewRoot: filepath.Join(newServicesRoot, "worker")},
	}
	if !reflect.DeepEqual(plan.Moves, want) {
		t.Fatalf("Moves = %#v, want %#v", plan.Moves, want)
	}
}

func TestPlanServicesRootBatchNonePinsAffectedServicesAtOldRoots(t *testing.T) {
	root := t.TempDir()
	oldServicesRoot := filepath.Join(root, "services")
	newServicesRoot := filepath.Join(root, "services2")
	customRoot := filepath.Join(t.TempDir(), "custom-db")

	plan, err := planServicesRootBatch(context.Background(), Config{ServicesRoot: oldServicesRoot}, []db.Service{
		{Name: "api"},
		{Name: "worker", ServiceRoot: filepath.Join(oldServicesRoot, "worker")},
		{Name: "db", ServiceRoot: customRoot},
	}, oldServicesRoot, newServicesRoot, catchrpc.HostStorageMigrateNone)
	if err != nil {
		t.Fatalf("planServicesRootBatch: %v", err)
	}
	want := []serviceRootMigrationPlan{
		{ServiceName: "api", OldRoot: filepath.Join(oldServicesRoot, "api"), NewRoot: filepath.Join(oldServicesRoot, "api")},
		{ServiceName: "worker", OldRoot: filepath.Join(oldServicesRoot, "worker"), NewRoot: filepath.Join(oldServicesRoot, "worker")},
	}
	if !reflect.DeepEqual(plan.Moves, want) {
		t.Fatalf("Moves = %#v, want %#v", plan.Moves, want)
	}
}

func TestPlanServicesRootBatchRejectsNoopAndNestedAllMoves(t *testing.T) {
	root := t.TempDir()
	oldServicesRoot := filepath.Join(root, "services")
	services := []db.Service{{Name: "api"}}

	_, err := planServicesRootBatch(context.Background(), Config{ServicesRoot: oldServicesRoot}, services, oldServicesRoot, oldServicesRoot, catchrpc.HostStorageMigrateAll)
	if err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("planServicesRootBatch noop error = %v, want already-current rejection", err)
	}

	_, err = planServicesRootBatch(context.Background(), Config{ServicesRoot: oldServicesRoot}, services, oldServicesRoot, filepath.Join(oldServicesRoot, "nested"), catchrpc.HostStorageMigrateAll)
	if err == nil || !strings.Contains(err.Error(), "nested") {
		t.Fatalf("planServicesRootBatch nested error = %v, want nested root rejection", err)
	}
}

func TestHostStoragePlanRejectsPathLikeZFSTargets(t *testing.T) {
	root := t.TempDir()
	planner := newTestHostStoragePlanner(t, Config{
		RootDir:      root,
		ServicesRoot: filepath.Join(root, "services"),
	}, nil)

	tests := []string{
		"/tank/yeet/data",
		"./tank/yeet/data",
		"../tank/yeet/data",
		"tank//yeet/data",
		"tank/../yeet/data",
	}
	for _, value := range tests {
		t.Run(value, func(t *testing.T) {
			_, err := planner.Plan(context.Background(), catchrpc.HostStoragePlanRequest{
				Set: catchrpc.HostStorageSetRequest{
					DataDir: &catchrpc.HostStorageTarget{Value: value, ZFS: true},
				},
			})
			if err == nil || !strings.Contains(err.Error(), "ZFS storage target must be a dataset name") {
				t.Fatalf("Plan error = %v, want dataset-name validation", err)
			}
		})
	}
}

func TestHostStoragePlanRejectsMixedFilesystemAndZFSTargets(t *testing.T) {
	root := t.TempDir()
	planner := newTestHostStoragePlanner(t, Config{
		RootDir:      root,
		ServicesRoot: filepath.Join(root, "services"),
	}, nil)

	_, err := planner.Plan(context.Background(), catchrpc.HostStoragePlanRequest{
		Set: catchrpc.HostStorageSetRequest{
			DataDir:      &catchrpc.HostStorageTarget{Value: "tank/yeet/data", ZFS: true},
			ServicesRoot: &catchrpc.HostStorageTarget{Value: filepath.Join(root, "services2")},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "mixed filesystem and ZFS storage changes must be run separately") {
		t.Fatalf("Plan error = %v, want mixed storage error", err)
	}
}

func TestHostStoragePlanRejectsMissingZFSDatasetWithoutResolvableParent(t *testing.T) {
	root := t.TempDir()
	runner := &recordingHostStorageZFS{datasets: map[string]fakeZFSDataset{}}
	planner := newTestHostStoragePlanner(t, Config{
		RootDir:      root,
		ServicesRoot: filepath.Join(root, "services"),
	}, nil)
	planner.zfs = runner.Run

	_, err := planner.Plan(context.Background(), catchrpc.HostStoragePlanRequest{
		Set: catchrpc.HostStorageSetRequest{
			DataDir: &catchrpc.HostStorageTarget{Value: "tank/yeet/data", ZFS: true},
		},
	})
	if err == nil || !strings.Contains(err.Error(), `cannot resolve mountpoint for missing ZFS dataset "tank/yeet/data"`) {
		t.Fatalf("Plan error = %v, want missing-parent mountpoint resolution error", err)
	}
	for _, call := range runner.calls {
		if len(call) > 0 && call[0] == "create" {
			t.Fatalf("Plan called zfs create: %#v", runner.calls)
		}
	}
}

func TestHostStoragePlanMarksMissingZFSDatasetsForCreation(t *testing.T) {
	root := t.TempDir()
	parentMountpoint := filepath.Join(root, "tank", "yeet")
	if err := os.MkdirAll(parentMountpoint, 0o755); err != nil {
		t.Fatalf("MkdirAll parent mountpoint: %v", err)
	}
	runner := &recordingHostStorageZFS{
		datasets: map[string]fakeZFSDataset{
			"tank/yeet": {Mountpoint: parentMountpoint, Exists: true},
		},
	}
	planner := newTestHostStoragePlanner(t, Config{
		RootDir:      root,
		ServicesRoot: filepath.Join(root, "services"),
	}, nil)
	planner.zfs = runner.Run

	plan, err := planner.Plan(context.Background(), catchrpc.HostStoragePlanRequest{
		Set: catchrpc.HostStorageSetRequest{
			DataDir:         &catchrpc.HostStorageTarget{Value: "tank/yeet/data", ZFS: true},
			ServicesRoot:    &catchrpc.HostStorageTarget{Value: "tank/yeet/services", ZFS: true},
			MigrateServices: catchrpc.HostStorageMigrateNone,
		},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	got := slices.Clone(plan.ZFSDatasetsToCreate)
	slices.Sort(got)
	want := []string{"tank/yeet/data", "tank/yeet/services"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ZFSDatasetsToCreate = %#v, want %#v", got, want)
	}
	for _, call := range runner.calls {
		if len(call) > 0 && call[0] == "create" {
			t.Fatalf("Plan called zfs create: %#v", runner.calls)
		}
	}
	if plan.Desired.DataDir != filepath.Join(root, "tank", "yeet", "data") {
		t.Fatalf("Desired.DataDir = %q, want inferred mountpoint", plan.Desired.DataDir)
	}
	if !plan.DataDirAction.Move || plan.DataDirAction.From != root || plan.DataDirAction.To != plan.Desired.DataDir {
		t.Fatalf("DataDirAction = %#v, want move from %q to %q", plan.DataDirAction, root, plan.Desired.DataDir)
	}
	if plan.Desired.ServicesRoot != filepath.Join(root, "tank", "yeet", "services") {
		t.Fatalf("Desired.ServicesRoot = %q, want inferred mountpoint", plan.Desired.ServicesRoot)
	}
}

func TestHostStoragePlanZFSServicesRootCreatesPerServiceDatasets(t *testing.T) {
	root := t.TempDir()
	parentMountpoint := filepath.Join(root, "tank", "yeet")
	if err := os.MkdirAll(parentMountpoint, 0o755); err != nil {
		t.Fatalf("MkdirAll parent mountpoint: %v", err)
	}
	runner := &recordingHostStorageZFS{
		datasets: map[string]fakeZFSDataset{
			"tank/yeet": {Mountpoint: parentMountpoint, Exists: true},
		},
	}
	oldServicesRoot := filepath.Join(root, "services")
	planner := newTestHostStoragePlanner(t, Config{
		RootDir:      root,
		ServicesRoot: oldServicesRoot,
	}, map[string]*db.Service{
		"api":        {Name: "api"},
		"worker":     {Name: "worker"},
		CatchService: {Name: CatchService, ServiceType: db.ServiceTypeSystemd},
	})
	planner.zfs = runner.Run

	plan, err := planner.Plan(context.Background(), catchrpc.HostStoragePlanRequest{
		Set: catchrpc.HostStorageSetRequest{
			ServicesRoot:    &catchrpc.HostStorageTarget{Value: "tank/yeet/services", ZFS: true},
			MigrateServices: catchrpc.HostStorageMigrateAll,
		},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	got := slices.Clone(plan.ZFSDatasetsToCreate)
	slices.Sort(got)
	wantDatasets := []string{
		"tank/yeet/services",
		"tank/yeet/services/api",
		"tank/yeet/services/catch",
		"tank/yeet/services/worker",
	}
	if !reflect.DeepEqual(got, wantDatasets) {
		t.Fatalf("ZFSDatasetsToCreate = %#v, want %#v", got, wantDatasets)
	}
	wantRoot := filepath.Join(parentMountpoint, "services")
	wantMoves := []catchrpc.HostStorageServiceMove{
		{Name: "api", From: filepath.Join(oldServicesRoot, "api"), To: filepath.Join(wantRoot, "api"), ToZFS: "tank/yeet/services/api"},
		{Name: "worker", From: filepath.Join(oldServicesRoot, "worker"), To: filepath.Join(wantRoot, "worker"), ToZFS: "tank/yeet/services/worker"},
	}
	if !reflect.DeepEqual(plan.ServicesAction.AffectedServices, wantMoves) {
		t.Fatalf("AffectedServices = %#v, want %#v", plan.ServicesAction.AffectedServices, wantMoves)
	}
	if plan.CatchAction.ToZFS != "tank/yeet/services/catch" {
		t.Fatalf("CatchAction.ToZFS = %q, want catch child dataset", plan.CatchAction.ToZFS)
	}
}

func TestHostStoragePlanSamePathZFSServicesRootCreatesMissingChildDatasets(t *testing.T) {
	root := t.TempDir()
	servicesRoot := filepath.Join(root, "tank", "yeet", "services")
	if err := os.MkdirAll(servicesRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll services root: %v", err)
	}
	runner := &recordingHostStorageZFS{
		datasets: map[string]fakeZFSDataset{
			"tank/yeet/services": {Mountpoint: servicesRoot, Exists: true},
		},
	}
	planner := newTestHostStoragePlanner(t, Config{
		RootDir:      root,
		ServicesRoot: servicesRoot,
	}, map[string]*db.Service{
		"api":        {Name: "api"},
		"worker":     {Name: "worker", ServiceRootZFS: "tank/yeet/services/worker"},
		CatchService: {Name: CatchService, ServiceType: db.ServiceTypeSystemd},
	})
	planner.zfs = runner.Run

	plan, err := planner.Plan(context.Background(), catchrpc.HostStoragePlanRequest{
		Set: catchrpc.HostStorageSetRequest{
			ServicesRoot:    &catchrpc.HostStorageTarget{Value: "tank/yeet/services", ZFS: true},
			MigrateServices: catchrpc.HostStorageMigrateAll,
		},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	got := slices.Clone(plan.ZFSDatasetsToCreate)
	slices.Sort(got)
	wantDatasets := []string{
		"tank/yeet/services/api",
		"tank/yeet/services/catch",
	}
	if !reflect.DeepEqual(got, wantDatasets) {
		t.Fatalf("ZFSDatasetsToCreate = %#v, want %#v", got, wantDatasets)
	}
	wantMoves := []catchrpc.HostStorageServiceMove{
		{Name: "api", From: filepath.Join(servicesRoot, "api"), To: filepath.Join(servicesRoot, "api"), ToZFS: "tank/yeet/services/api"},
	}
	if !reflect.DeepEqual(plan.ServicesAction.AffectedServices, wantMoves) {
		t.Fatalf("AffectedServices = %#v, want %#v", plan.ServicesAction.AffectedServices, wantMoves)
	}
	if !plan.CatchAction.Move || plan.CatchAction.From != filepath.Join(servicesRoot, CatchService) || plan.CatchAction.To != filepath.Join(servicesRoot, CatchService) || plan.CatchAction.ToZFS != "tank/yeet/services/catch" {
		t.Fatalf("CatchAction = %#v, want same-path catch child dataset move", plan.CatchAction)
	}
	if !plan.RequiresRestart {
		t.Fatalf("RequiresRestart = false, want restart for same-path child dataset conversion")
	}
}

func TestHostStorageApplyMigrateAllStopsRunningServicesBeforeMoves(t *testing.T) {
	root := t.TempDir()
	oldServicesRoot := filepath.Join(root, "services")
	newServicesRoot := filepath.Join(root, "services2")
	applier, ops := newTestHostStorageApplier(t, Config{
		RootDir:      root,
		ServicesRoot: oldServicesRoot,
	}, map[string]*db.Service{
		"api":    {Name: "api", ServiceType: db.ServiceTypeSystemd},
		"cache":  {Name: "cache", ServiceType: db.ServiceTypeSystemd},
		"worker": {Name: "worker", ServiceType: db.ServiceTypeSystemd},
	})
	ops.running["api"] = true
	ops.running["worker"] = true
	plan := testHostStorageApplyServicesPlan(root, oldServicesRoot, newServicesRoot, catchrpc.HostStorageMigrateAll, "api", "cache", "worker")

	result, err := applier.Apply(context.Background(), plan, true, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	firstMove := firstCallIndexWithPrefix(ops.calls, "move:")
	if firstMove < 0 {
		t.Fatalf("calls = %#v, want service root moves", ops.calls)
	}
	for _, want := range []string{"stop:api", "stop:worker"} {
		idx := slices.Index(ops.calls, want)
		if idx < 0 {
			t.Fatalf("calls = %#v, missing %s", ops.calls, want)
		}
		if idx > firstMove {
			t.Fatalf("%s happened after first move: calls = %#v", want, ops.calls)
		}
	}
	if slices.Contains(ops.calls, "stop:cache") {
		t.Fatalf("calls = %#v, stopped cache even though it was not running", ops.calls)
	}
	wantMoves := []catchrpc.HostStorageServiceMove{
		{Name: "api", From: filepath.Join(oldServicesRoot, "api"), To: filepath.Join(newServicesRoot, "api"), WasRunning: true},
		{Name: "cache", From: filepath.Join(oldServicesRoot, "cache"), To: filepath.Join(newServicesRoot, "cache")},
		{Name: "worker", From: filepath.Join(oldServicesRoot, "worker"), To: filepath.Join(newServicesRoot, "worker"), WasRunning: true},
	}
	if !reflect.DeepEqual(result.MigratedServices, wantMoves) {
		t.Fatalf("MigratedServices = %#v, want %#v", result.MigratedServices, wantMoves)
	}
}

func TestHostStorageApplyFailedStopPreventsMovesAndRestarts(t *testing.T) {
	root := t.TempDir()
	oldServicesRoot := filepath.Join(root, "services")
	newServicesRoot := filepath.Join(root, "services2")
	applier, ops := newTestHostStorageApplier(t, Config{
		RootDir:      root,
		ServicesRoot: oldServicesRoot,
	}, map[string]*db.Service{
		"api":    {Name: "api", ServiceType: db.ServiceTypeSystemd},
		"worker": {Name: "worker", ServiceType: db.ServiceTypeSystemd},
	})
	ops.running["api"] = true
	ops.running["worker"] = true
	ops.stopErr["api"] = errors.New("systemd refused")
	plan := testHostStorageApplyServicesPlan(root, oldServicesRoot, newServicesRoot, catchrpc.HostStorageMigrateAll, "api", "worker")

	_, err := applier.Apply(context.Background(), plan, true, nil)
	if err == nil || !strings.Contains(err.Error(), `stop service "api"`) {
		t.Fatalf("Apply error = %v, want stop failure for api", err)
	}
	for _, call := range ops.calls {
		if strings.HasPrefix(call, "move:") || strings.HasPrefix(call, "start:") || call == "restart-catch" {
			t.Fatalf("calls = %#v, want no moves or restarts after stop failure", ops.calls)
		}
	}
}

func TestHostStorageApplyFailedMoveLeavesServicesStoppedWithRecoveryText(t *testing.T) {
	root := t.TempDir()
	oldServicesRoot := filepath.Join(root, "services")
	newServicesRoot := filepath.Join(root, "services2")
	applier, ops := newTestHostStorageApplier(t, Config{
		RootDir:      root,
		ServicesRoot: oldServicesRoot,
	}, map[string]*db.Service{
		"api":    {Name: "api", ServiceType: db.ServiceTypeSystemd},
		"cache":  {Name: "cache", ServiceType: db.ServiceTypeSystemd},
		"worker": {Name: "worker", ServiceType: db.ServiceTypeSystemd},
	})
	ops.running["api"] = true
	ops.running["worker"] = true
	ops.moveErr["worker"] = errors.New("disk full")
	plan := testHostStorageApplyServicesPlan(root, oldServicesRoot, newServicesRoot, catchrpc.HostStorageMigrateAll, "api", "worker", "cache")

	_, err := applier.Apply(context.Background(), plan, true, nil)
	workerFrom := filepath.Join(oldServicesRoot, "worker")
	workerTo := filepath.Join(newServicesRoot, "worker")
	wantErr := fmt.Sprintf(`move service root for "worker" from %q to %q failed: disk full. Services were left stopped; repair the failed root and retry host storage apply or start services manually`, workerFrom, workerTo)
	if err == nil || err.Error() != wantErr {
		t.Fatalf("Apply error = %v, want exact recovery text %q", err, wantErr)
	}
	if ops.running["api"] || ops.running["worker"] {
		t.Fatalf("running state = %#v, want previously running services left stopped", ops.running)
	}
	if slices.Contains(ops.calls, "move:cache") {
		t.Fatalf("calls = %#v, moved later service after worker failed", ops.calls)
	}
	for _, call := range ops.calls {
		if strings.HasPrefix(call, "start:") {
			t.Fatalf("calls = %#v, want no service restarts after move failure", ops.calls)
		}
	}
}

func TestHostStorageApplySuccessfulMigrationRestartsOnlyPreviouslyRunningServices(t *testing.T) {
	root := t.TempDir()
	oldServicesRoot := filepath.Join(root, "services")
	newServicesRoot := filepath.Join(root, "services2")
	applier, ops := newTestHostStorageApplier(t, Config{
		RootDir:      root,
		ServicesRoot: oldServicesRoot,
	}, map[string]*db.Service{
		"api":    {Name: "api", ServiceType: db.ServiceTypeSystemd},
		"cache":  {Name: "cache", ServiceType: db.ServiceTypeSystemd},
		"worker": {Name: "worker", ServiceType: db.ServiceTypeSystemd},
	})
	ops.running["api"] = true
	ops.running["cache"] = true
	plan := testHostStorageApplyServicesPlan(root, oldServicesRoot, newServicesRoot, catchrpc.HostStorageMigrateAll, "api", "cache", "worker")

	if _, err := applier.Apply(context.Background(), plan, true, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !reflect.DeepEqual(ops.callsWithPrefix("start:"), []string{"start:api", "start:cache"}) {
		t.Fatalf("calls = %#v, want starts only for previously running api and cache", ops.calls)
	}
}

func TestHostStorageApplyPersistsPerServiceZFSDatasets(t *testing.T) {
	root := t.TempDir()
	oldServicesRoot := filepath.Join(root, "services")
	newServicesRoot := filepath.Join(root, "services2")
	runner := &recordingHostStorageZFS{datasets: map[string]fakeZFSDataset{}}
	applier, _ := newTestHostStorageApplier(t, Config{
		RootDir:      root,
		ServicesRoot: oldServicesRoot,
	}, map[string]*db.Service{
		"api": {Name: "api", ServiceType: db.ServiceTypeSystemd},
	})
	applier.zfs = runner.Run
	plan := testHostStorageApplyServicesPlan(root, oldServicesRoot, newServicesRoot, catchrpc.HostStorageMigrateAll, "api")
	plan.ServicesAction.AffectedServices[0].ToZFS = "tank/yeet/services/api"
	plan.CatchAction = catchrpc.HostStorageCatchAction{
		Move:  true,
		From:  filepath.Join(oldServicesRoot, CatchService),
		To:    filepath.Join(newServicesRoot, CatchService),
		ToZFS: "tank/yeet/services/catch",
	}
	plan.ZFSDatasetsToCreate = []string{"tank/yeet/services/api", "tank/yeet/services/catch"}

	if _, err := applier.Apply(context.Background(), plan, true, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	dv, err := applier.store.Get()
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	api := dv.Services().Get("api").AsStruct()
	if api == nil {
		t.Fatalf("api service missing after apply")
	}
	if api.ServiceRoot != filepath.Join(newServicesRoot, "api") || api.ServiceRootZFS != "tank/yeet/services/api" {
		t.Fatalf("api service root = %q zfs = %q, want %q and child dataset", api.ServiceRoot, api.ServiceRootZFS, filepath.Join(newServicesRoot, "api"))
	}
	catchService := dv.Services().Get(CatchService).AsStruct()
	if catchService == nil {
		t.Fatalf("catch service missing after apply")
	}
	if catchService.ServiceRoot != filepath.Join(newServicesRoot, CatchService) || catchService.ServiceRootZFS != "tank/yeet/services/catch" {
		t.Fatalf("catch service root = %q zfs = %q, want %q and child dataset", catchService.ServiceRoot, catchService.ServiceRootZFS, filepath.Join(newServicesRoot, CatchService))
	}
}

func TestHostStorageApplySamePathZFSStagesPopulatedRootsBeforeCreate(t *testing.T) {
	root := t.TempDir()
	servicesRoot := filepath.Join(root, "services")
	apiRoot := filepath.Join(servicesRoot, "api")
	catchRoot := filepath.Join(servicesRoot, CatchService)
	for _, dir := range []string{apiRoot, catchRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "marker"), []byte(filepath.Base(dir)), 0o644); err != nil {
			t.Fatalf("WriteFile marker %s: %v", dir, err)
		}
	}
	datasets := map[string]fakeZFSDataset{
		"tank/yeet/services/api":   {Mountpoint: apiRoot},
		"tank/yeet/services/catch": {Mountpoint: catchRoot},
	}
	var createCalls []string
	runner := func(ctx context.Context, args ...string) (string, string, error) {
		if len(args) == 2 && args[0] == "create" {
			dataset := args[1]
			createCalls = append(createCalls, dataset)
			mountpoint := datasets[dataset].Mountpoint
			if _, err := os.Stat(filepath.Join(mountpoint, "marker")); err == nil {
				return "", "mountpoint still populated before zfs create", errZFSCommandFailed
			}
		}
		return fakeZFSRunner(datasets).Run(ctx, args...)
	}
	applier, _ := newTestHostStorageApplier(t, Config{
		RootDir:      root,
		ServicesRoot: servicesRoot,
	}, map[string]*db.Service{
		"api": {Name: "api", ServiceType: db.ServiceTypeSystemd},
	})
	applier.zfs = runner
	applier.ops.materializeServiceRootMigration = nil
	plan := catchrpc.HostStoragePlan{
		Current: catchrpc.HostStorageState{DataDir: root, ServicesRoot: servicesRoot},
		Desired: catchrpc.HostStorageState{DataDir: root, ServicesRoot: servicesRoot, ServicesZFS: true},
		ServicesAction: catchrpc.HostStorageServicesAction{
			Mode: catchrpc.HostStorageMigrateAll,
			From: servicesRoot,
			To:   servicesRoot,
			AffectedServices: []catchrpc.HostStorageServiceMove{{
				Name:  "api",
				From:  apiRoot,
				To:    apiRoot,
				ToZFS: "tank/yeet/services/api",
			}},
		},
		CatchAction: catchrpc.HostStorageCatchAction{
			Move:  true,
			From:  catchRoot,
			To:    catchRoot,
			ToZFS: "tank/yeet/services/catch",
		},
		ZFSDatasetsToCreate: []string{"tank/yeet/services/api", "tank/yeet/services/catch"},
		RequiresRestart:     true,
	}

	if _, err := applier.Apply(context.Background(), plan, true, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(apiRoot, "marker")); err != nil || string(got) != "api" {
		t.Fatalf("api marker = %q, %v; want api", string(got), err)
	}
	if got, err := os.ReadFile(filepath.Join(catchRoot, "marker")); err != nil || string(got) != "catch" {
		t.Fatalf("catch marker = %q, %v; want catch", string(got), err)
	}
	if !reflect.DeepEqual(createCalls, []string{"tank/yeet/services/api", "tank/yeet/services/catch"}) {
		t.Fatalf("zfs create calls = %#v, want child datasets", createCalls)
	}
	dv, err := applier.store.Get()
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	api := dv.Services().Get("api").AsStruct()
	if api == nil || api.ServiceRootZFS != "tank/yeet/services/api" {
		t.Fatalf("api after apply = %#v, want child dataset", api)
	}
	catchService := dv.Services().Get(CatchService).AsStruct()
	if catchService == nil || catchService.ServiceRootZFS != "tank/yeet/services/catch" {
		t.Fatalf("catch after apply = %#v, want child dataset", catchService)
	}
}

func TestHostStorageApplySamePathZFSRecoversStagedRootAfterCreate(t *testing.T) {
	root := t.TempDir()
	servicesRoot := filepath.Join(root, "services")
	apiRoot := filepath.Join(servicesRoot, "api")
	stage := filepath.Join(servicesRoot, ".yeet-service-root-stage-123")
	if err := os.MkdirAll(filepath.Join(apiRoot, "bin"), 0o755); err != nil {
		t.Fatalf("MkdirAll partial root: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(stage, "bin"), 0o755); err != nil {
		t.Fatalf("MkdirAll stage: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stage, "bin", "marker"), []byte("api"), 0o644); err != nil {
		t.Fatalf("WriteFile stage marker: %v", err)
	}
	runner := &recordingHostStorageZFS{
		datasets: map[string]fakeZFSDataset{
			"tank/yeet/services/api": {Mountpoint: apiRoot, Exists: true},
		},
	}
	applier, _ := newTestHostStorageApplier(t, Config{
		RootDir:      root,
		ServicesRoot: servicesRoot,
	}, map[string]*db.Service{
		"api": {Name: "api", ServiceType: db.ServiceTypeSystemd},
	})
	applier.zfs = runner.Run
	applier.ops.materializeServiceRootMigration = nil
	applier.ops.isServiceRunning = func(_ context.Context, name string) (bool, error) {
		if name != "api" {
			t.Fatalf("isServiceRunning(%q), want api", name)
		}
		return false, fmt.Errorf("failed to run docker command: chdir %s: no such file or directory", filepath.Join(apiRoot, "data"))
	}
	plan := catchrpc.HostStoragePlan{
		Current: catchrpc.HostStorageState{DataDir: root, ServicesRoot: servicesRoot},
		Desired: catchrpc.HostStorageState{DataDir: root, ServicesRoot: servicesRoot, ServicesZFS: true},
		ServicesAction: catchrpc.HostStorageServicesAction{
			Mode: catchrpc.HostStorageMigrateAll,
			From: servicesRoot,
			To:   servicesRoot,
			AffectedServices: []catchrpc.HostStorageServiceMove{{
				Name:  "api",
				From:  apiRoot,
				To:    apiRoot,
				ToZFS: "tank/yeet/services/api",
			}},
		},
	}

	if _, err := applier.Apply(context.Background(), plan, true, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(apiRoot, "bin", "marker")); err != nil || string(got) != "api" {
		t.Fatalf("restored marker = %q, %v; want api", string(got), err)
	}
	if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stage stat err = %v, want removed stage", err)
	}
	dv, err := applier.store.Get()
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	api := dv.Services().Get("api").AsStruct()
	if api == nil || api.ServiceRootZFS != "tank/yeet/services/api" {
		t.Fatalf("api after apply = %#v, want child dataset", api)
	}
}

func TestHostStorageApplyMigrateNonePinsOldRootsWithoutMovingDirectories(t *testing.T) {
	root := t.TempDir()
	oldServicesRoot := filepath.Join(root, "services")
	newServicesRoot := filepath.Join(root, "services2")
	applier, ops := newTestHostStorageApplier(t, Config{
		RootDir:      root,
		ServicesRoot: oldServicesRoot,
	}, map[string]*db.Service{
		"api": {
			Name:        "api",
			ServiceType: db.ServiceTypeSystemd,
			Artifacts: db.ArtifactStore{
				db.ArtifactBinary: &db.Artifact{
					Refs: map[db.ArtifactRef]string{
						db.Gen(1): filepath.Join(oldServicesRoot, "api", "bin", "api"),
					},
				},
			},
		},
	})
	plan := testHostStorageApplyServicesPlan(root, oldServicesRoot, newServicesRoot, catchrpc.HostStorageMigrateNone, "api")

	if _, err := applier.Apply(context.Background(), plan, true, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for _, call := range ops.calls {
		if strings.HasPrefix(call, "move:") {
			t.Fatalf("calls = %#v, want no service root moves for migrate none", ops.calls)
		}
	}
	dv, err := applier.store.Get()
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	got := dv.Services().Get("api").ServiceRoot()
	want := filepath.Join(oldServicesRoot, "api")
	if got != want {
		t.Fatalf("api ServiceRoot = %q, want explicit old root %q", got, want)
	}
	updated := dv.Services().Get("api").AsStruct()
	if updated == nil || len(updated.Artifacts) == 0 {
		t.Fatalf("api artifacts were cleared for migrate none: %#v", updated)
	}
}

func TestHostStorageApplyRejectsServiceAddedAfterPlanBeforeSideEffects(t *testing.T) {
	root := t.TempDir()
	oldServicesRoot := filepath.Join(root, "services")
	newServicesRoot := filepath.Join(root, "services2")
	runner := &recordingHostStorageZFS{}
	applier, ops := newTestHostStorageApplier(t, Config{
		RootDir:      root,
		ServicesRoot: oldServicesRoot,
	}, nil)
	applier.zfs = runner.Run
	plan := testHostStorageApplyServicesPlan(root, oldServicesRoot, newServicesRoot, catchrpc.HostStorageMigrateAll)
	plan.ZFSDatasetsToCreate = []string{"tank/yeet/services2"}
	_, mutateErr := applier.store.MutateData(func(d *db.Data) error {
		d.Services["late"] = &db.Service{Name: "late", ServiceType: db.ServiceTypeSystemd}
		return nil
	})
	if mutateErr != nil {
		t.Fatalf("MutateData: %v", mutateErr)
	}

	_, err := applier.Apply(context.Background(), plan, true, nil)
	if err == nil || !strings.Contains(err.Error(), "affected services changed during host storage planning") {
		t.Fatalf("Apply error = %v, want stale affected services", err)
	}
	if len(ops.calls) != 0 {
		t.Fatalf("calls = %#v, want no service side effects before stale plan rejection", ops.calls)
	}
	for _, call := range runner.calls {
		if len(call) > 0 && call[0] == "create" {
			t.Fatalf("zfs calls = %#v, want no create before stale plan rejection", runner.calls)
		}
	}
}

func TestHostStorageApplyDataDirChangeCopiesStateReinstallsRestartsAndLeavesOldDir(t *testing.T) {
	root := t.TempDir()
	oldDataDir := filepath.Join(root, "old-data")
	newDataDir := filepath.Join(root, "new-data")
	servicesRoot := filepath.Join(oldDataDir, "services")
	if err := os.MkdirAll(filepath.Join(oldDataDir, "registry", "blobs"), 0o755); err != nil {
		t.Fatalf("MkdirAll old registry: %v", err)
	}
	marker := filepath.Join(oldDataDir, "registry", "blobs", "marker")
	if err := os.WriteFile(marker, []byte("state"), 0o644); err != nil {
		t.Fatalf("WriteFile marker: %v", err)
	}
	applier, ops := newTestHostStorageApplier(t, Config{
		RootDir:      oldDataDir,
		ServicesRoot: servicesRoot,
	}, nil)
	applier.ops.copyDataDir = nil
	plan := catchrpc.HostStoragePlan{
		Current: catchrpc.HostStorageState{DataDir: oldDataDir, ServicesRoot: servicesRoot},
		Desired: catchrpc.HostStorageState{DataDir: newDataDir, ServicesRoot: servicesRoot},
		DataDirAction: catchrpc.HostStorageDataDirAction{
			Move: true,
			From: oldDataDir,
			To:   newDataDir,
		},
		RequiresRestart: true,
	}

	result, err := applier.Apply(context.Background(), plan, true, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("old data marker stat: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(newDataDir, "registry", "blobs", "marker")); err != nil || string(got) != "state" {
		t.Fatalf("copied marker = %q, %v; want state", string(got), err)
	}
	wantCalls := []string{
		"install-catch:" + newDataDir + ":" + servicesRoot,
		"restart-catch",
		"verify-info:" + newDataDir + ":" + servicesRoot,
	}
	if !reflect.DeepEqual(ops.calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", ops.calls, wantCalls)
	}
	if !result.Restarted {
		t.Fatalf("Restarted = false, want true")
	}
}

func TestHostStorageApplyDataDirChangeRewritesCopiedTargetDB(t *testing.T) {
	root := t.TempDir()
	oldDataDir := filepath.Join(root, "old-data")
	newDataDir := filepath.Join(root, "new-data")
	servicesRoot := filepath.Join(oldDataDir, "services")
	applier, _ := newTestHostStorageApplier(t, Config{
		RootDir:      oldDataDir,
		ServicesRoot: servicesRoot,
	}, nil)
	applier.ops.copyDataDir = func(_ context.Context, from, to string, _ hostStorageDataDirCopyOptions) error {
		if from != oldDataDir || to != newDataDir {
			t.Fatalf("copyDataDir from/to = %q/%q, want %q/%q", from, to, oldDataDir, newDataDir)
		}
		targetStore := db.NewStore(filepath.Join(to, "db.json"), servicesRoot)
		return targetStore.Set(&db.Data{
			DataVersion: db.CurrentDataVersion,
			Services: map[string]*db.Service{
				CatchService: {
					Name:        CatchService,
					ServiceType: db.ServiceTypeSystemd,
				},
				"api": {
					Name:       "api",
					Generation: 3,
					Artifacts: db.ArtifactStore{
						db.ArtifactBinary: {Refs: map[db.ArtifactRef]string{
							db.Gen(3): filepath.Join(oldDataDir, "services", "api", "bin", "api"),
						}},
					},
					VM: &db.VMConfig{
						Image: db.VMImageConfig{
							RootFS: filepath.Join(oldDataDir, "vm-images", "ubuntu", "rootfs.ext4"),
						},
						Disk: db.VMDiskConfig{
							Path: filepath.Join(oldDataDir, "services", "api", "data", "rootfs.raw"),
						},
					},
				},
			},
		})
	}
	plan := catchrpc.HostStoragePlan{
		Current: catchrpc.HostStorageState{DataDir: oldDataDir, ServicesRoot: servicesRoot},
		Desired: catchrpc.HostStorageState{DataDir: newDataDir, ServicesRoot: servicesRoot},
		DataDirAction: catchrpc.HostStorageDataDirAction{
			Move: true,
			From: oldDataDir,
			To:   newDataDir,
		},
		RequiresRestart: true,
	}

	var out strings.Builder
	if _, err := applier.Apply(context.Background(), plan, true, &out); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	targetStore := db.NewStore(filepath.Join(newDataDir, "db.json"), servicesRoot)
	dv, err := targetStore.Get()
	if err != nil {
		t.Fatalf("target store Get: %v", err)
	}
	api := dv.Services().Get("api").AsStruct()
	if api == nil {
		t.Fatal("api service missing from copied target DB")
	}
	if got := api.Artifacts[db.ArtifactBinary].Refs[db.Gen(3)]; got != filepath.Join(oldDataDir, "services", "api", "bin", "api") {
		t.Fatalf("copied artifact ref = %q", got)
	}
	if got := api.VM.Disk.Path; got != filepath.Join(oldDataDir, "services", "api", "data", "rootfs.raw") {
		t.Fatalf("copied VM disk = %q", got)
	}
	if got := api.VM.Image.RootFS; got != filepath.Join(newDataDir, "vm-images", "ubuntu", "rootfs.ext4") {
		t.Fatalf("copied VM rootfs = %q", got)
	}
	if !strings.Contains(out.String(), "Rewrote 1 host storage database reference.") {
		t.Fatalf("output = %q, want DB rewrite summary", out.String())
	}
}

func TestHostStorageApplyDataDirRewriteFailureNamesStoppedServices(t *testing.T) {
	root := t.TempDir()
	oldDataDir := filepath.Join(root, "old-data")
	newDataDir := filepath.Join(root, "new-data")
	oldServicesRoot := filepath.Join(oldDataDir, "services")
	newServicesRoot := filepath.Join(newDataDir, "services")
	applier, ops := newTestHostStorageApplier(t, Config{
		RootDir:      oldDataDir,
		ServicesRoot: oldServicesRoot,
	}, map[string]*db.Service{
		"api": {Name: "api", ServiceType: db.ServiceTypeSystemd},
	})
	ops.running["api"] = true
	applier.ops.copyDataDir = func(_ context.Context, _, to string, _ hostStorageDataDirCopyOptions) error {
		if err := os.MkdirAll(to, 0o700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(to, "db.json"), []byte("{not-json"), 0o600)
	}
	plan := catchrpc.HostStoragePlan{
		Current: catchrpc.HostStorageState{DataDir: oldDataDir, ServicesRoot: oldServicesRoot},
		Desired: catchrpc.HostStorageState{DataDir: newDataDir, ServicesRoot: newServicesRoot},
		DataDirAction: catchrpc.HostStorageDataDirAction{
			Move: true,
			From: oldDataDir,
			To:   newDataDir,
		},
		ServicesAction: catchrpc.HostStorageServicesAction{
			Mode: catchrpc.HostStorageMigrateAll,
			From: oldServicesRoot,
			To:   newServicesRoot,
			AffectedServices: []catchrpc.HostStorageServiceMove{
				{Name: "api", From: filepath.Join(oldServicesRoot, "api"), To: filepath.Join(newServicesRoot, "api")},
			},
		},
		RequiresRestart: true,
	}

	_, err := applier.Apply(context.Background(), plan, true, nil)
	if err == nil {
		t.Fatal("Apply error = nil, want target DB rewrite failure")
	}
	if !strings.Contains(err.Error(), "rewrite target db") {
		t.Fatalf("Apply error = %v, want target DB rewrite context", err)
	}
	if !strings.Contains(err.Error(), "Services were left stopped: api") {
		t.Fatalf("Apply error = %v, want stopped service recovery context", err)
	}
}

func TestHostStorageApplyRepairOnlyRewritesCurrentDBRefs(t *testing.T) {
	systemdDir := t.TempDir()
	oldSystemdDir := systemdSystemDir
	systemdSystemDir = systemdDir
	t.Cleanup(func() { systemdSystemDir = oldSystemdDir })
	currentDataDir := filepath.Join(t.TempDir(), "flash", "yeet", "data")
	currentServicesRoot := filepath.Join(t.TempDir(), "flash", "yeet", "services")

	applier, ops := newTestHostStorageApplier(t, Config{
		RootDir:      currentDataDir,
		ServicesRoot: currentServicesRoot,
	}, map[string]*db.Service{
		CatchService: {
			Name:        CatchService,
			ServiceType: db.ServiceTypeSystemd,
		},
		"api": {
			Name:       "api",
			Generation: 3,
			Artifacts: db.ArtifactStore{
				db.ArtifactBinary: {Refs: map[db.ArtifactRef]string{
					db.Gen(3): "/root/data/services/api/bin/api",
				}},
			},
			VM: &db.VMConfig{
				Image: db.VMImageConfig{
					RootFS: "/root/data/vm-images/ubuntu/rootfs.ext4",
				},
			},
		},
	})
	applier.ops.copyDataDir = func(context.Context, string, string, hostStorageDataDirCopyOptions) error {
		t.Fatal("copyDataDir called for repair-only plan")
		return nil
	}
	plan := catchrpc.HostStoragePlan{
		Current: catchrpc.HostStorageState{DataDir: currentDataDir, ServicesRoot: currentServicesRoot},
		Desired: catchrpc.HostStorageState{DataDir: currentDataDir, ServicesRoot: currentServicesRoot},
		RepairAction: catchrpc.HostStorageRepairAction{
			References:      2,
			DatabaseRefs:    2,
			ValidationRoots: []string{"/root/data"},
		},
	}

	result, err := applier.Apply(context.Background(), plan, true, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	dv, err := applier.store.Get()
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	api := dv.Services().Get("api").AsStruct()
	if api == nil {
		t.Fatal("api service missing")
	}
	if got := api.Artifacts[db.ArtifactBinary].Refs[db.Gen(3)]; got != filepath.Join(currentServicesRoot, "api", "bin", "api") {
		t.Fatalf("api binary ref = %q, want repaired services root", got)
	}
	if got := api.VM.Image.RootFS; got != filepath.Join(currentDataDir, "vm-images", "ubuntu", "rootfs.ext4") {
		t.Fatalf("api VM rootfs = %q, want repaired data dir", got)
	}
	if !result.Restarted {
		t.Fatalf("Restarted = false, want catch restart for repair")
	}
	if result.Validation.ActiveRefs != 0 {
		t.Fatalf("Validation.ActiveRefs = %d, want 0", result.Validation.ActiveRefs)
	}
	for _, call := range ops.calls {
		if strings.HasPrefix(call, "move:") {
			t.Fatalf("calls = %#v, want no service root moves for repair-only plan", ops.calls)
		}
	}
}

func TestHostStorageApplyRepairOnlyRewritesGeneratedArtifactsAndReinstallsUnits(t *testing.T) {
	systemdDir := t.TempDir()
	oldSystemdDir := systemdSystemDir
	systemdSystemDir = systemdDir
	t.Cleanup(func() { systemdSystemDir = oldSystemdDir })
	currentDataDir := filepath.Join(t.TempDir(), "flash", "yeet", "data")
	currentServicesRoot := filepath.Join(t.TempDir(), "flash", "yeet", "services")

	root := t.TempDir()
	artifactPath := filepath.Join(root, "yeet-api-ns.service")
	if err := os.WriteFile(artifactPath, []byte("ExecStart=/root/data/services/catch/data/service-ns\n"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	applier, ops := newTestHostStorageApplier(t, Config{
		RootDir:      currentDataDir,
		ServicesRoot: currentServicesRoot,
	}, map[string]*db.Service{
		CatchService: {
			Name:        CatchService,
			ServiceType: db.ServiceTypeSystemd,
		},
		"api": {
			Name:        "api",
			ServiceType: db.ServiceTypeSystemd,
			Generation:  1,
			Artifacts: db.ArtifactStore{
				db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(1): artifactPath}},
			},
		},
	})
	ops.running["api"] = true
	applier.ops.reinstallServiceUnits = func(_ context.Context, _ Config, service *db.Service) ([]string, error) {
		ops.calls = append(ops.calls, "reinstall:"+service.Name)
		return []string{service.Name + ".service"}, nil
	}
	applier.ops.reloadSystemd = func(context.Context) error {
		ops.calls = append(ops.calls, "daemon-reload")
		return nil
	}
	applier.ops.enableSystemdUnits = func(_ context.Context, units []string) error {
		for _, unit := range units {
			ops.calls = append(ops.calls, "enable:"+unit)
		}
		return nil
	}
	plan := catchrpc.HostStoragePlan{
		Current: catchrpc.HostStorageState{DataDir: currentDataDir, ServicesRoot: currentServicesRoot},
		Desired: catchrpc.HostStorageState{DataDir: currentDataDir, ServicesRoot: currentServicesRoot},
		RepairAction: catchrpc.HostStorageRepairAction{
			References:      1,
			SystemdRefs:     1,
			RegenerateUnits: []string{"yeet-api-ns.service"},
			RestartServices: []string{"api"},
			ValidationRoots: []string{"/root/data"},
		},
	}

	if _, err := applier.Apply(context.Background(), plan, true, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	raw, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if strings.Contains(string(raw), "/root/data") {
		t.Fatalf("artifact still contains old root: %s", raw)
	}
	assertCallOrder(t, ops.calls, "stop:api", "reinstall:api", "start:api")
}

func TestHostStorageApplyDataDirMoveKeepsGeneratedArtifactRefsUnderUnchangedServicesRoot(t *testing.T) {
	systemdDir := t.TempDir()
	oldSystemdDir := systemdSystemDir
	systemdSystemDir = systemdDir
	t.Cleanup(func() { systemdSystemDir = oldSystemdDir })
	root := t.TempDir()
	oldDataDir := filepath.Join(root, "old-data")
	newDataDir := filepath.Join(root, "new-data")
	servicesRoot := filepath.Join(oldDataDir, "services")
	artifactPath := filepath.Join(servicesRoot, "api", "compose.yml")
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		t.Fatalf("MkdirAll artifact dir: %v", err)
	}
	oldBind := filepath.Join(servicesRoot, "api", "data")
	if err := os.WriteFile(artifactPath, []byte("services:\n  app:\n    volumes:\n      - "+oldBind+":/data\n"), 0o644); err != nil {
		t.Fatalf("write compose artifact: %v", err)
	}
	applier, _ := newTestHostStorageApplier(t, Config{
		RootDir:      oldDataDir,
		ServicesRoot: servicesRoot,
	}, map[string]*db.Service{
		CatchService: {
			Name:        CatchService,
			ServiceType: db.ServiceTypeSystemd,
		},
		"api": {
			Name:       "api",
			Generation: 1,
			Artifacts: db.ArtifactStore{
				db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{db.Gen(1): artifactPath}},
			},
		},
	})
	applier.ops.copyDataDir = func(_ context.Context, from, to string, _ hostStorageDataDirCopyOptions) error {
		if err := os.MkdirAll(to, 0o755); err != nil {
			return err
		}
		raw, err := os.ReadFile(filepath.Join(from, "db.json"))
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(to, "db.json"), raw, 0o600)
	}
	plan := catchrpc.HostStoragePlan{
		Current: catchrpc.HostStorageState{DataDir: oldDataDir, ServicesRoot: servicesRoot},
		Desired: catchrpc.HostStorageState{DataDir: newDataDir, ServicesRoot: servicesRoot},
		DataDirAction: catchrpc.HostStorageDataDirAction{
			Move: true,
			From: oldDataDir,
			To:   newDataDir,
		},
		RequiresRestart: true,
	}

	if _, err := applier.Apply(context.Background(), plan, true, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	raw, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("read compose artifact: %v", err)
	}
	if !strings.Contains(string(raw), oldBind) {
		t.Fatalf("compose artifact = %q, want unchanged services-root bind %q", raw, oldBind)
	}
	if rewritten := filepath.Join(newDataDir, "services", "api", "data"); strings.Contains(string(raw), rewritten) {
		t.Fatalf("compose artifact = %q, want no data-dir rewrite to %q", raw, rewritten)
	}
}

func TestHostStorageApplyDataDirMoveRewritesGeneratedArtifactDataDirOnly(t *testing.T) {
	systemdDir := t.TempDir()
	oldSystemdDir := systemdSystemDir
	systemdSystemDir = systemdDir
	t.Cleanup(func() { systemdSystemDir = oldSystemdDir })
	root := t.TempDir()
	oldDataDir := filepath.Join(root, "old-data")
	newDataDir := filepath.Join(root, "new-data")
	servicesRoot := filepath.Join(oldDataDir, "services")
	serviceRootPath := filepath.Join(servicesRoot, "api", "data")
	artifactPath := filepath.Join(servicesRoot, "api", "yeet-api-ns.service")
	installedPath := filepath.Join(systemdDir, "yeet-api-ns.service")
	unitText := "ExecStart=/usr/local/bin/catch -data-dir " + oldDataDir + " service-ns --service-root " + serviceRootPath + "\n"
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		t.Fatalf("MkdirAll artifact dir: %v", err)
	}
	if err := os.WriteFile(artifactPath, []byte(unitText), 0o644); err != nil {
		t.Fatalf("write generated unit: %v", err)
	}
	if err := os.WriteFile(installedPath, []byte(unitText), 0o644); err != nil {
		t.Fatalf("write installed unit: %v", err)
	}
	applier, ops := newTestHostStorageApplier(t, Config{
		RootDir:      oldDataDir,
		ServicesRoot: servicesRoot,
	}, map[string]*db.Service{
		CatchService: {
			Name:        CatchService,
			ServiceType: db.ServiceTypeSystemd,
		},
		"api": {
			Name:        "api",
			ServiceType: db.ServiceTypeSystemd,
			Generation:  1,
			Artifacts: db.ArtifactStore{
				db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(1): artifactPath}},
			},
		},
	})
	applier.ops.copyDataDir = func(_ context.Context, from, to string, _ hostStorageDataDirCopyOptions) error {
		if err := os.MkdirAll(to, 0o755); err != nil {
			return err
		}
		raw, err := os.ReadFile(filepath.Join(from, "db.json"))
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(to, "db.json"), raw, 0o600)
	}
	applier.ops.reinstallServiceUnits = func(_ context.Context, _ Config, service *db.Service) ([]string, error) {
		ops.calls = append(ops.calls, "reinstall:"+service.Name)
		raw, err := os.ReadFile(artifactPath)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(installedPath, raw, 0o644); err != nil {
			return nil, err
		}
		return []string{"yeet-api-ns.service"}, nil
	}
	applier.ops.reloadSystemd = func(context.Context) error {
		ops.calls = append(ops.calls, "daemon-reload")
		return nil
	}
	applier.ops.enableSystemdUnits = func(_ context.Context, units []string) error {
		for _, unit := range units {
			ops.calls = append(ops.calls, "enable:"+unit)
		}
		return nil
	}
	plan := catchrpc.HostStoragePlan{
		Current: catchrpc.HostStorageState{DataDir: oldDataDir, ServicesRoot: servicesRoot},
		Desired: catchrpc.HostStorageState{DataDir: newDataDir, ServicesRoot: servicesRoot},
		DataDirAction: catchrpc.HostStorageDataDirAction{
			Move: true,
			From: oldDataDir,
			To:   newDataDir,
		},
		RequiresRestart: true,
	}

	result, err := applier.Apply(context.Background(), plan, true, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Validation.ActiveRefs != 0 {
		t.Fatalf("Validation.ActiveRefs = %d, want 0", result.Validation.ActiveRefs)
	}
	for _, path := range []string{artifactPath, installedPath} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(raw)
		if strings.Contains(text, "-data-dir "+oldDataDir) {
			t.Fatalf("%s still contains old data dir: %s", path, text)
		}
		if !strings.Contains(text, "-data-dir "+newDataDir) {
			t.Fatalf("%s missing new data dir: %s", path, text)
		}
		if !strings.Contains(text, serviceRootPath) {
			t.Fatalf("%s missing unchanged service-root path %q: %s", path, serviceRootPath, text)
		}
		if rewrittenServiceRoot := filepath.Join(newDataDir, "services", "api", "data"); strings.Contains(text, rewrittenServiceRoot) {
			t.Fatalf("%s rewrote unchanged service-root path to %q: %s", path, rewrittenServiceRoot, text)
		}
	}
	assertCallOrder(t, ops.calls, "reinstall:api", "daemon-reload", "enable:yeet-api-ns.service")
}

func TestHostStorageApplyRepairOnlyDataDirMappingKeepsGeneratedArtifactRefsUnderUnchangedServicesRoot(t *testing.T) {
	systemdDir := t.TempDir()
	oldSystemdDir := systemdSystemDir
	systemdSystemDir = systemdDir
	t.Cleanup(func() { systemdSystemDir = oldSystemdDir })
	newDataDir := filepath.Join(t.TempDir(), "flash", "yeet", "data")
	servicesRoot := "/root/data/services"
	serviceRootPath := filepath.Join(servicesRoot, "api", "data")
	artifactPath := filepath.Join(t.TempDir(), "yeet-api-ns.service")
	installedPath := filepath.Join(systemdDir, "yeet-api-ns.service")
	unitText := "ExecStart=/usr/local/bin/catch -data-dir /root/data service-ns --service-root " + serviceRootPath + "\n"
	if err := os.WriteFile(artifactPath, []byte(unitText), 0o644); err != nil {
		t.Fatalf("write generated unit: %v", err)
	}
	if err := os.WriteFile(installedPath, []byte(unitText), 0o644); err != nil {
		t.Fatalf("write installed unit: %v", err)
	}
	applier, ops := newTestHostStorageApplier(t, Config{
		RootDir:      newDataDir,
		ServicesRoot: servicesRoot,
	}, map[string]*db.Service{
		CatchService: {
			Name:        CatchService,
			ServiceType: db.ServiceTypeSystemd,
		},
		"api": {
			Name:        "api",
			ServiceType: db.ServiceTypeSystemd,
			Generation:  1,
			Artifacts: db.ArtifactStore{
				db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(1): artifactPath}},
			},
		},
	})
	applier.ops.reinstallServiceUnits = func(_ context.Context, _ Config, service *db.Service) ([]string, error) {
		ops.calls = append(ops.calls, "reinstall:"+service.Name)
		raw, err := os.ReadFile(artifactPath)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(installedPath, raw, 0o644); err != nil {
			return nil, err
		}
		return []string{"yeet-api-ns.service"}, nil
	}
	applier.ops.reloadSystemd = func(context.Context) error {
		ops.calls = append(ops.calls, "daemon-reload")
		return nil
	}
	applier.ops.enableSystemdUnits = func(_ context.Context, units []string) error {
		for _, unit := range units {
			ops.calls = append(ops.calls, "enable:"+unit)
		}
		return nil
	}
	plan := catchrpc.HostStoragePlan{
		Current: catchrpc.HostStorageState{DataDir: newDataDir, ServicesRoot: servicesRoot},
		Desired: catchrpc.HostStorageState{DataDir: newDataDir, ServicesRoot: servicesRoot},
		RepairAction: catchrpc.HostStorageRepairAction{
			References:      1,
			SystemdRefs:     1,
			RegenerateUnits: []string{"yeet-api-ns.service"},
			ValidationRoots: []string{"/root/data"},
		},
	}

	result, err := applier.Apply(context.Background(), plan, true, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Validation.ActiveRefs != 0 {
		t.Fatalf("Validation.ActiveRefs = %d, want 0", result.Validation.ActiveRefs)
	}
	for _, path := range []string{artifactPath, installedPath} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(raw)
		if strings.Contains(text, "-data-dir /root/data ") {
			t.Fatalf("%s still contains old data dir: %s", path, text)
		}
		if !strings.Contains(text, "-data-dir "+newDataDir) {
			t.Fatalf("%s missing new data dir: %s", path, text)
		}
		if !strings.Contains(text, serviceRootPath) {
			t.Fatalf("%s missing unchanged service-root path %q: %s", path, serviceRootPath, text)
		}
		if rewrittenServiceRoot := filepath.Join(newDataDir, "services", "api", "data"); strings.Contains(text, rewrittenServiceRoot) {
			t.Fatalf("%s rewrote unchanged service-root path to %q: %s", path, rewrittenServiceRoot, text)
		}
	}
	assertCallOrder(t, ops.calls, "reinstall:api", "daemon-reload", "enable:yeet-api-ns.service")
}

func TestHostStorageActiveRepairMappingsUseInstallUserLegacyDefaultDataDir(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "current-data")
	servicesRoot := filepath.Join(root, "current-services")
	services := map[string]*db.Service{
		"api": {
			Name: "api",
			Artifacts: db.ArtifactStore{
				db.ArtifactBinary: {Refs: map[db.ArtifactRef]string{
					db.Gen(1): "/home/ubuntu/data/services/api/bin/api",
				}},
			},
		},
	}
	applier, _ := newTestHostStorageApplier(t, Config{
		RootDir:      dataDir,
		ServicesRoot: servicesRoot,
		InstallUser:  "ubuntu",
	}, services)
	plan := catchrpc.HostStoragePlan{
		Current:      catchrpc.HostStorageState{DataDir: dataDir, ServicesRoot: servicesRoot},
		Desired:      catchrpc.HostStorageState{DataDir: dataDir, ServicesRoot: servicesRoot},
		RepairAction: catchrpc.HostStorageRepairAction{References: 1},
	}

	mappings, err := applier.activeHostStorageMappings(context.Background(), plan)
	if err != nil {
		t.Fatalf("activeHostStorageMappings: %v", err)
	}
	for _, want := range []hostStoragePathMapping{
		{From: "/home/ubuntu/data/services", To: servicesRoot, Reason: hostStoragePathReasonServicesDir},
		{From: "/home/ubuntu/data", To: dataDir, Reason: hostStoragePathReasonDataDir},
	} {
		if !slices.Contains(mappings, want) {
			t.Fatalf("mappings = %#v, missing %#v", mappings, want)
		}
	}
}

func TestHostStorageApplyRepairBatchesSystemdReloadForMultipleUnitRepairs(t *testing.T) {
	systemdDir := t.TempDir()
	oldSystemdDir := systemdSystemDir
	systemdSystemDir = systemdDir
	t.Cleanup(func() { systemdSystemDir = oldSystemdDir })
	currentDataDir := filepath.Join(t.TempDir(), "flash", "yeet", "data")
	currentServicesRoot := filepath.Join(t.TempDir(), "flash", "yeet", "services")
	root := t.TempDir()
	artifactPath := filepath.Join(root, "yeet-api-ns.service")
	if err := os.WriteFile(artifactPath, []byte("ExecStart=/root/data/services/catch/data/service-ns\n"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	applier, ops := newTestHostStorageApplier(t, Config{
		RootDir:      currentDataDir,
		ServicesRoot: currentServicesRoot,
	}, map[string]*db.Service{
		CatchService: {
			Name:        CatchService,
			ServiceType: db.ServiceTypeSystemd,
		},
		"api": {
			Name:        "api",
			ServiceType: db.ServiceTypeSystemd,
			Generation:  1,
			Artifacts: db.ArtifactStore{
				db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(1): artifactPath}},
			},
		},
		"devbox": {
			Name:        "devbox",
			ServiceType: db.ServiceTypeVM,
			VM: &db.VMConfig{
				Image: db.VMImageConfig{RootFS: filepath.Join(currentDataDir, "vm-images", "ubuntu", "rootfs.ext4")},
			},
		},
	})
	ops.running["api"] = true
	ops.running["devbox"] = true
	applier.ops.reinstallServiceUnits = func(_ context.Context, _ Config, service *db.Service) ([]string, error) {
		ops.calls = append(ops.calls, "write-unit:"+service.Name)
		return []string{service.Name + ".service"}, nil
	}
	applier.ops.regenerateVMUnit = func(_ context.Context, _ Config, service *db.Service, _ string) ([]string, error) {
		ops.calls = append(ops.calls, "write-vm:"+service.Name)
		return []string{vmSystemdUnitName(service.Name)}, nil
	}
	applier.ops.reloadSystemd = func(context.Context) error {
		ops.calls = append(ops.calls, "daemon-reload")
		return nil
	}
	applier.ops.enableSystemdUnits = func(_ context.Context, units []string) error {
		for _, unit := range units {
			ops.calls = append(ops.calls, "enable:"+unit)
		}
		return nil
	}
	plan := catchrpc.HostStoragePlan{
		Current: catchrpc.HostStorageState{DataDir: currentDataDir, ServicesRoot: currentServicesRoot},
		Desired: catchrpc.HostStorageState{DataDir: currentDataDir, ServicesRoot: currentServicesRoot},
		RepairAction: catchrpc.HostStorageRepairAction{
			References:      2,
			SystemdRefs:     2,
			RegenerateUnits: []string{"yeet-api-ns.service", vmSystemdUnitName("devbox")},
			RestartServices: []string{"api", "devbox"},
			ValidationRoots: []string{"/root/data"},
		},
	}

	if _, err := applier.Apply(context.Background(), plan, true, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := countHostStorageCalls(ops.calls, "daemon-reload"); got != 1 {
		t.Fatalf("daemon-reload calls = %d, want 1; calls %#v", got, ops.calls)
	}
	reload := slices.Index(ops.calls, "daemon-reload")
	for _, prefix := range []string{"write-unit:api", "write-vm:devbox"} {
		idx := firstCallIndexWithPrefix(ops.calls, prefix)
		if idx < 0 || idx > reload {
			t.Fatalf("%s index = %d, reload index = %d; calls %#v", prefix, idx, reload, ops.calls)
		}
	}
	for _, prefix := range []string{"enable:api.service", "enable:" + vmSystemdUnitName("devbox")} {
		idx := firstCallIndexWithPrefix(ops.calls, prefix)
		if idx < reload {
			t.Fatalf("%s index = %d before reload index %d; calls %#v", prefix, idx, reload, ops.calls)
		}
	}
}

func TestHostStorageValidationApplyFailsWithSystemdRef(t *testing.T) {
	systemdDir := t.TempDir()
	oldSystemdDir := systemdSystemDir
	systemdSystemDir = systemdDir
	t.Cleanup(func() { systemdSystemDir = oldSystemdDir })
	unitPath := filepath.Join(systemdDir, "yeet-api-ns.service")
	if err := os.WriteFile(unitPath, []byte("[Service]\nExecStart=/root/data/services/api/bin/api\n"), 0o644); err != nil {
		t.Fatalf("write unit: %v", err)
	}
	currentDataDir := filepath.Join(t.TempDir(), "flash", "yeet", "data")
	currentServicesRoot := filepath.Join(t.TempDir(), "flash", "yeet", "services")
	applier, _ := newTestHostStorageApplier(t, Config{
		RootDir:      currentDataDir,
		ServicesRoot: currentServicesRoot,
	}, map[string]*db.Service{
		CatchService: {
			Name:        CatchService,
			ServiceType: db.ServiceTypeSystemd,
		},
		"api": {
			Name:        "api",
			ServiceType: db.ServiceTypeSystemd,
		},
	})
	plan := catchrpc.HostStoragePlan{
		Current: catchrpc.HostStorageState{DataDir: currentDataDir, ServicesRoot: currentServicesRoot},
		Desired: catchrpc.HostStorageState{DataDir: currentDataDir, ServicesRoot: currentServicesRoot},
		RepairAction: catchrpc.HostStorageRepairAction{
			References:      1,
			SystemdRefs:     1,
			ValidationRoots: []string{"/root/data"},
		},
	}

	_, err := applier.Apply(context.Background(), plan, true, nil)
	if err == nil {
		t.Fatal("Apply error = nil, want validation failure")
	}
	if !strings.Contains(err.Error(), "yeet-api-ns.service:2") || !strings.Contains(err.Error(), "/root/data/services/api/bin/api") {
		t.Fatalf("Apply error = %v, want unit and old path", err)
	}
}

func TestHostStorageApplyRepairFiltersSelfManagedRestartServices(t *testing.T) {
	systemdDir := t.TempDir()
	oldSystemdDir := systemdSystemDir
	systemdSystemDir = systemdDir
	t.Cleanup(func() { systemdSystemDir = oldSystemdDir })
	currentDataDir := filepath.Join(t.TempDir(), "flash", "yeet", "data")
	currentServicesRoot := filepath.Join(t.TempDir(), "flash", "yeet", "services")

	applier, ops := newTestHostStorageApplier(t, Config{
		RootDir:      currentDataDir,
		ServicesRoot: currentServicesRoot,
	}, map[string]*db.Service{
		CatchService: {
			Name:        CatchService,
			ServiceType: db.ServiceTypeSystemd,
		},
		SystemService: {
			Name:        SystemService,
			ServiceType: db.ServiceTypeSystemd,
		},
		"api": {
			Name:        "api",
			ServiceType: db.ServiceTypeSystemd,
		},
	})
	ops.running["api"] = true
	plan := catchrpc.HostStoragePlan{
		Current: catchrpc.HostStorageState{DataDir: currentDataDir, ServicesRoot: currentServicesRoot},
		Desired: catchrpc.HostStorageState{DataDir: currentDataDir, ServicesRoot: currentServicesRoot},
		RepairAction: catchrpc.HostStorageRepairAction{
			References:      1,
			RestartServices: []string{CatchService, SystemService, "api"},
			ValidationRoots: []string{"/root/data"},
		},
	}

	result, err := applier.Apply(context.Background(), plan, true, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !result.Restarted {
		t.Fatalf("Restarted = false, want catch restart path for repair")
	}
	for _, denied := range []string{CatchService, SystemService} {
		for _, call := range ops.calls {
			if strings.HasSuffix(call, ":"+denied) {
				t.Fatalf("calls = %#v, want no ordinary service operation for %q", ops.calls, denied)
			}
		}
	}
	assertCallOrder(t, ops.calls, "stop:api", "start:api", "install-catch:")
}

func TestHostStorageApplyDefaultServerDataDirChangeCopiesAndRestartsCatch(t *testing.T) {
	root := t.TempDir()
	oldDataDir := filepath.Join(root, "old-data")
	newDataDir := filepath.Join(root, "new-data")
	servicesRoot := filepath.Join(oldDataDir, "services")
	if err := os.MkdirAll(filepath.Join(oldDataDir, "registry", "blobs"), 0o755); err != nil {
		t.Fatalf("MkdirAll old registry: %v", err)
	}
	marker := filepath.Join(oldDataDir, "registry", "blobs", "marker")
	if err := os.WriteFile(marker, []byte("state"), 0o644); err != nil {
		t.Fatalf("WriteFile marker: %v", err)
	}
	server := newTestHostStorageServer(t, Config{
		RootDir:      oldDataDir,
		ServicesRoot: servicesRoot,
	}, map[string]*db.Service{
		CatchService: {
			Name:        CatchService,
			ServiceType: db.ServiceTypeSystemd,
		},
	})
	ops := &recordingHostStorageDefaultCatchOps{}
	withHostStorageDefaultCatchOps(t, ops)
	plan := catchrpc.HostStoragePlan{
		Current: catchrpc.HostStorageState{DataDir: oldDataDir, ServicesRoot: servicesRoot},
		Desired: catchrpc.HostStorageState{DataDir: newDataDir, ServicesRoot: servicesRoot},
		DataDirAction: catchrpc.HostStorageDataDirAction{
			Move: true,
			From: oldDataDir,
			To:   newDataDir,
		},
		RequiresRestart: true,
	}

	result, err := server.ApplyHostStoragePlan(context.Background(), plan, true, nil)
	if err != nil {
		t.Fatalf("ApplyHostStoragePlan: %v", err)
	}
	if !result.Restarted {
		t.Fatalf("Restarted = false, want true")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("old data marker stat: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(newDataDir, "registry", "blobs", "marker")); err != nil || string(got) != "state" {
		t.Fatalf("copied marker = %q, %v; want state", string(got), err)
	}
	wantCalls := []string{
		"install-catch:" + newDataDir + ":" + servicesRoot,
		"restart-catch:" + newDataDir + ":" + servicesRoot,
		"verify-info:" + newDataDir + ":" + servicesRoot,
	}
	if !reflect.DeepEqual(ops.calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", ops.calls, wantCalls)
	}
}

func TestHostStorageApplyDefaultServerMovesDataDirAndServicesRootTogether(t *testing.T) {
	root := t.TempDir()
	oldDataDir := filepath.Join(root, "old-data")
	newDataDir := filepath.Join(root, "new-data")
	oldServicesRoot := filepath.Join(oldDataDir, "services")
	newServicesRoot := filepath.Join(newDataDir, "services")
	oldAPI := filepath.Join(oldServicesRoot, "api")
	newAPI := filepath.Join(newServicesRoot, "api")
	oldCatch := filepath.Join(oldServicesRoot, CatchService)
	if err := os.MkdirAll(filepath.Join(oldDataDir, "registry", "blobs"), 0o755); err != nil {
		t.Fatalf("MkdirAll old registry: %v", err)
	}
	if err := os.MkdirAll(oldAPI, 0o755); err != nil {
		t.Fatalf("MkdirAll old api root: %v", err)
	}
	if err := os.MkdirAll(oldCatch, 0o755); err != nil {
		t.Fatalf("MkdirAll old catch root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldDataDir, "registry", "blobs", "marker"), []byte("state"), 0o644); err != nil {
		t.Fatalf("WriteFile registry marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldAPI, "marker"), []byte("service state"), 0o644); err != nil {
		t.Fatalf("WriteFile service marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldCatch, "marker"), []byte("catch state"), 0o644); err != nil {
		t.Fatalf("WriteFile catch marker: %v", err)
	}
	server := newTestHostStorageServer(t, Config{
		RootDir:      oldDataDir,
		ServicesRoot: oldServicesRoot,
	}, map[string]*db.Service{
		"api": {
			Name:        "api",
			ServiceType: db.ServiceTypeVM,
		},
		CatchService: {
			Name:        CatchService,
			ServiceType: db.ServiceTypeSystemd,
		},
	})
	oldVMStatus := serverVMStatusFunc
	serverVMStatusFunc = func(string) (svc.Status, error) {
		return svc.StatusStopped, nil
	}
	t.Cleanup(func() { serverVMStatusFunc = oldVMStatus })
	ops := &recordingHostStorageDefaultCatchOps{}
	withHostStorageDefaultCatchOps(t, ops)
	plan, err := server.PlanHostStorage(context.Background(), catchrpc.HostStoragePlanRequest{
		Set: catchrpc.HostStorageSetRequest{
			DataDir:         &catchrpc.HostStorageTarget{Value: newDataDir},
			ServicesRoot:    &catchrpc.HostStorageTarget{Value: newServicesRoot},
			MigrateServices: catchrpc.HostStorageMigrateAll,
		},
	})
	if err != nil {
		t.Fatalf("PlanHostStorage: %v", err)
	}

	result, err := server.ApplyHostStoragePlan(context.Background(), plan, true, nil)
	if err != nil {
		t.Fatalf("ApplyHostStoragePlan: %v", err)
	}
	if !result.Restarted {
		t.Fatalf("Restarted = false, want true")
	}
	if got, err := os.ReadFile(filepath.Join(newDataDir, "registry", "blobs", "marker")); err != nil || string(got) != "state" {
		t.Fatalf("copied registry marker = %q, %v; want state", string(got), err)
	}
	if got, err := os.ReadFile(filepath.Join(newAPI, "marker")); err != nil || string(got) != "service state" {
		t.Fatalf("moved api marker = %q, %v; want service state", string(got), err)
	}
	dv, storeErr := server.cfg.DB.Get()
	if storeErr != nil {
		t.Fatalf("store.Get: %v", storeErr)
	}
	if got := dv.Services().Get("api").ServiceRoot(); got != newAPI {
		t.Fatalf("api ServiceRoot = %q, want explicit new root %q", got, newAPI)
	}
	wantCalls := []string{
		"install-catch:" + newDataDir + ":" + newServicesRoot,
		"restart-catch:" + newDataDir + ":" + newServicesRoot,
		"verify-info:" + newDataDir + ":" + newServicesRoot,
	}
	if !reflect.DeepEqual(ops.calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", ops.calls, wantCalls)
	}
}

func TestHostStorageApplyDataDirMoveExcludesMigratedServicesRootOutsideTargetDataDir(t *testing.T) {
	root := t.TempDir()
	oldDataDir := filepath.Join(root, "old-data")
	newDataDir := filepath.Join(root, "new-data")
	oldServicesRoot := filepath.Join(oldDataDir, "services")
	newServicesRoot := filepath.Join(root, "services-root")
	oldAPI := filepath.Join(oldServicesRoot, "api")
	oldCatch := filepath.Join(oldServicesRoot, CatchService)
	if err := os.MkdirAll(filepath.Join(oldDataDir, "registry", "blobs"), 0o755); err != nil {
		t.Fatalf("MkdirAll old registry: %v", err)
	}
	if err := os.MkdirAll(oldAPI, 0o755); err != nil {
		t.Fatalf("MkdirAll old api root: %v", err)
	}
	if err := os.MkdirAll(oldCatch, 0o755); err != nil {
		t.Fatalf("MkdirAll old catch root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldDataDir, "registry", "blobs", "marker"), []byte("state"), 0o644); err != nil {
		t.Fatalf("WriteFile registry marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldAPI, "marker"), []byte("service state"), 0o644); err != nil {
		t.Fatalf("WriteFile service marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldCatch, "marker"), []byte("catch state"), 0o644); err != nil {
		t.Fatalf("WriteFile catch marker: %v", err)
	}
	server := newTestHostStorageServer(t, Config{
		RootDir:      oldDataDir,
		ServicesRoot: oldServicesRoot,
	}, map[string]*db.Service{
		"api": {
			Name:        "api",
			ServiceType: db.ServiceTypeVM,
		},
		CatchService: {
			Name:        CatchService,
			ServiceType: db.ServiceTypeSystemd,
		},
	})
	oldVMStatus := serverVMStatusFunc
	serverVMStatusFunc = func(string) (svc.Status, error) {
		return svc.StatusStopped, nil
	}
	t.Cleanup(func() { serverVMStatusFunc = oldVMStatus })
	ops := &recordingHostStorageDefaultCatchOps{}
	withHostStorageDefaultCatchOps(t, ops)
	plan, err := server.PlanHostStorage(context.Background(), catchrpc.HostStoragePlanRequest{
		Set: catchrpc.HostStorageSetRequest{
			DataDir:         &catchrpc.HostStorageTarget{Value: newDataDir},
			ServicesRoot:    &catchrpc.HostStorageTarget{Value: newServicesRoot},
			MigrateServices: catchrpc.HostStorageMigrateAll,
		},
	})
	if err != nil {
		t.Fatalf("PlanHostStorage: %v", err)
	}

	if _, err := server.ApplyHostStoragePlan(context.Background(), plan, true, nil); err != nil {
		t.Fatalf("ApplyHostStoragePlan: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(newDataDir, "registry", "blobs", "marker")); err != nil || string(got) != "state" {
		t.Fatalf("copied registry marker = %q, %v; want state", string(got), err)
	}
	if _, err := os.Stat(filepath.Join(newDataDir, "services")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new data dir services stat error = %v, want missing migrated service-root copy", err)
	}
	if got, err := os.ReadFile(filepath.Join(newServicesRoot, "api", "marker")); err != nil || string(got) != "service state" {
		t.Fatalf("moved api marker = %q, %v; want service state", string(got), err)
	}
	if got, err := os.ReadFile(filepath.Join(newServicesRoot, CatchService, "marker")); err != nil || string(got) != "catch state" {
		t.Fatalf("moved catch marker = %q, %v; want catch state", string(got), err)
	}
	wantCalls := []string{
		"install-catch:" + newDataDir + ":" + newServicesRoot,
		"restart-catch:" + newDataDir + ":" + newServicesRoot,
		"verify-info:" + newDataDir + ":" + newServicesRoot,
	}
	if !reflect.DeepEqual(ops.calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", ops.calls, wantCalls)
	}
}

func TestCopyHostStorageDataDirCreatesPrivateTarget(t *testing.T) {
	root := t.TempDir()
	from := filepath.Join(root, "old-data")
	to := filepath.Join(root, "new-data")
	if err := os.MkdirAll(filepath.Join(from, "registry"), 0o700); err != nil {
		t.Fatalf("MkdirAll registry: %v", err)
	}
	if err := os.WriteFile(filepath.Join(from, "db.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("WriteFile db.json: %v", err)
	}

	if err := copyHostStorageDataDir(context.Background(), from, to); err != nil {
		t.Fatalf("copyHostStorageDataDir: %v", err)
	}
	info, err := os.Stat(to)
	if err != nil {
		t.Fatalf("Stat target: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("target mode = %o, want 700", got)
	}
}

func TestCopyHostStorageDataDirTightensExistingEmptyTarget(t *testing.T) {
	root := t.TempDir()
	from := filepath.Join(root, "old-data")
	to := filepath.Join(root, "new-data")
	if err := os.MkdirAll(filepath.Join(from, "registry"), 0o700); err != nil {
		t.Fatalf("MkdirAll registry: %v", err)
	}
	if err := os.WriteFile(filepath.Join(from, "db.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("WriteFile db.json: %v", err)
	}
	if err := os.MkdirAll(to, 0o755); err != nil {
		t.Fatalf("MkdirAll target: %v", err)
	}

	if err := copyHostStorageDataDir(context.Background(), from, to); err != nil {
		t.Fatalf("copyHostStorageDataDir: %v", err)
	}
	info, err := os.Stat(to)
	if err != nil {
		t.Fatalf("Stat target: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("target mode = %o, want 700", got)
	}
}

func TestHostStorageVerifyCatchInfoUsesFinalConfig(t *testing.T) {
	got, err := verifyHostStorageCatchInfo(context.Background(), catchrpc.HostStorageState{}, Config{
		RootDir:      "/srv/yeet-data",
		ServicesRoot: "/srv/yeet-data/services",
	})
	if err != nil {
		t.Fatalf("verifyHostStorageCatchInfo: %v", err)
	}
	if got.RootDir != "/srv/yeet-data" || got.ServicesDir != "/srv/yeet-data/services" {
		t.Fatalf("verifyHostStorageCatchInfo root/services = %q/%q, want final config paths", got.RootDir, got.ServicesDir)
	}
}

func TestHostStorageCatchUnitArgsPreserveTSNetHost(t *testing.T) {
	got := hostStorageCatchUnitArgs(hostStorageInstallRequest{
		DataDir:      "/srv/yeet-data",
		ServicesRoot: "/srv/yeet-services",
		Config:       Config{TSNetHost: ""},
	})
	want := []string{"--data-dir=/srv/yeet-data", "--services-root=/srv/yeet-services", "--tsnet-host="}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("hostStorageCatchUnitArgs disabled tsnet = %#v, want %#v", got, want)
	}

	got = hostStorageCatchUnitArgs(hostStorageInstallRequest{
		DataDir:      "/srv/yeet-data",
		ServicesRoot: "/srv/yeet-services",
		Config:       Config{TSNetHost: "catch-custom"},
	})
	want = []string{"--data-dir=/srv/yeet-data", "--services-root=/srv/yeet-services", "--tsnet-host=catch-custom"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("hostStorageCatchUnitArgs custom tsnet = %#v, want %#v", got, want)
	}
}

func TestRestartHostStorageCatchSchedulesSystemdRestart(t *testing.T) {
	var gotName string
	var gotArgs []string
	oldRun := hostStorageRunCommand
	hostStorageRunCommand = func(_ context.Context, name string, args ...string) error {
		gotName = name
		gotArgs = slices.Clone(args)
		return nil
	}
	t.Cleanup(func() { hostStorageRunCommand = oldRun })

	var out strings.Builder
	err := restartHostStorageCatch(context.Background(), hostStorageInstallRequest{}, &out)
	if !errors.Is(err, errHostStorageCatchRestartScheduled) {
		t.Fatalf("restartHostStorageCatch error = %v, want restart scheduled sentinel", err)
	}
	if gotName != "systemd-run" {
		t.Fatalf("command = %q, want systemd-run", gotName)
	}
	if len(gotArgs) != 6 {
		t.Fatalf("args = %#v, want 6 args", gotArgs)
	}
	if !strings.HasPrefix(gotArgs[0], "--unit=yeet-catch-restart-") {
		t.Fatalf("unit arg = %q, want yeet-catch-restart prefix", gotArgs[0])
	}
	wantTail := []string{"--collect", "--on-active=1s", "systemctl", "restart", "catch.service"}
	if !reflect.DeepEqual(gotArgs[1:], wantTail) {
		t.Fatalf("args tail = %#v, want %#v", gotArgs[1:], wantTail)
	}
	if !strings.Contains(out.String(), "Scheduled catch restart") {
		t.Fatalf("output = %q, want scheduled restart message", out.String())
	}
}

func TestHostStorageApplyDefaultServerRejectsMissingCatchServiceBeforeDataDirCopy(t *testing.T) {
	root := t.TempDir()
	oldDataDir := filepath.Join(root, "old-data")
	newDataDir := filepath.Join(root, "new-data")
	servicesRoot := filepath.Join(oldDataDir, "services")
	if err := os.MkdirAll(filepath.Join(oldDataDir, "registry", "blobs"), 0o755); err != nil {
		t.Fatalf("MkdirAll old registry: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldDataDir, "registry", "blobs", "marker"), []byte("state"), 0o644); err != nil {
		t.Fatalf("WriteFile marker: %v", err)
	}
	server := newTestHostStorageServer(t, Config{
		RootDir:      oldDataDir,
		ServicesRoot: servicesRoot,
	}, nil)
	ops := &recordingHostStorageDefaultCatchOps{}
	withHostStorageDefaultCatchOps(t, ops)
	plan := catchrpc.HostStoragePlan{
		Current: catchrpc.HostStorageState{DataDir: oldDataDir, ServicesRoot: servicesRoot},
		Desired: catchrpc.HostStorageState{DataDir: newDataDir, ServicesRoot: servicesRoot},
		DataDirAction: catchrpc.HostStorageDataDirAction{
			Move: true,
			From: oldDataDir,
			To:   newDataDir,
		},
		RequiresRestart: true,
	}

	_, err := server.ApplyHostStoragePlan(context.Background(), plan, true, nil)
	if err == nil || !strings.Contains(err.Error(), "catch service is not configured") {
		t.Fatalf("ApplyHostStoragePlan error = %v, want missing catch service preflight", err)
	}
	if len(ops.calls) != 0 {
		t.Fatalf("calls = %#v, want no catch install/restart after preflight failure", ops.calls)
	}
	if _, statErr := os.Stat(newDataDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("new data dir stat error = %v, want target not copied", statErr)
	}
}

func TestHostStorageApplyDefaultServerServicesRootChangeMovesUsersAndCatchRoot(t *testing.T) {
	root := t.TempDir()
	oldServicesRoot := filepath.Join(root, "services")
	newServicesRoot := filepath.Join(root, "services2")
	oldAPI := filepath.Join(oldServicesRoot, "api")
	newAPI := filepath.Join(newServicesRoot, "api")
	oldCatch := filepath.Join(oldServicesRoot, CatchService)
	newCatch := filepath.Join(newServicesRoot, CatchService)
	if err := os.MkdirAll(oldAPI, 0o755); err != nil {
		t.Fatalf("MkdirAll old api root: %v", err)
	}
	if err := os.MkdirAll(oldCatch, 0o755); err != nil {
		t.Fatalf("MkdirAll old catch root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldAPI, "marker"), []byte("service state"), 0o644); err != nil {
		t.Fatalf("WriteFile marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldCatch, "marker"), []byte("catch state"), 0o644); err != nil {
		t.Fatalf("WriteFile catch marker: %v", err)
	}
	server := newTestHostStorageServer(t, Config{
		RootDir:      root,
		ServicesRoot: oldServicesRoot,
	}, map[string]*db.Service{
		"api": {
			Name:        "api",
			ServiceType: db.ServiceTypeVM,
			ServiceRoot: oldAPI,
		},
		CatchService: {
			Name:        CatchService,
			ServiceType: db.ServiceTypeSystemd,
		},
	})
	oldVMStatus := serverVMStatusFunc
	serverVMStatusFunc = func(string) (svc.Status, error) {
		return svc.StatusStopped, nil
	}
	t.Cleanup(func() { serverVMStatusFunc = oldVMStatus })
	ops := &recordingHostStorageDefaultCatchOps{}
	withHostStorageDefaultCatchOps(t, ops)
	plan, err := server.PlanHostStorage(context.Background(), catchrpc.HostStoragePlanRequest{
		Set: catchrpc.HostStorageSetRequest{
			ServicesRoot:    &catchrpc.HostStorageTarget{Value: newServicesRoot},
			MigrateServices: catchrpc.HostStorageMigrateAll,
		},
	})
	if err != nil {
		t.Fatalf("PlanHostStorage: %v", err)
	}
	if slices.ContainsFunc(plan.ServicesAction.AffectedServices, func(move catchrpc.HostStorageServiceMove) bool {
		return move.Name == CatchService || move.Name == SystemService
	}) {
		t.Fatalf("AffectedServices = %#v, want no self-managed services", plan.ServicesAction.AffectedServices)
	}
	if !plan.CatchAction.Move || plan.CatchAction.From != oldCatch || plan.CatchAction.To != newCatch {
		t.Fatalf("CatchAction = %#v, want catch root move", plan.CatchAction)
	}

	result, err := server.ApplyHostStoragePlan(context.Background(), plan, true, nil)
	if err != nil {
		t.Fatalf("ApplyHostStoragePlan: %v", err)
	}
	if !result.Restarted {
		t.Fatalf("Restarted = false, want true")
	}
	if got, err := os.ReadFile(filepath.Join(newAPI, "marker")); err != nil || string(got) != "service state" {
		t.Fatalf("moved api marker = %q, %v; want service state", string(got), err)
	}
	if got, err := os.ReadFile(filepath.Join(newCatch, "marker")); err != nil || string(got) != "catch state" {
		t.Fatalf("moved catch marker = %q, %v; want catch state", string(got), err)
	}
	dv, storeErr := server.cfg.DB.Get()
	if storeErr != nil {
		t.Fatalf("store.Get: %v", storeErr)
	}
	if got := dv.Services().Get("api").ServiceRoot(); got != newAPI {
		t.Fatalf("api ServiceRoot = %q, want explicit new root %q", got, newAPI)
	}
	catchService := dv.Services().Get(CatchService).AsStruct()
	if catchService == nil {
		t.Fatalf("catch service missing after apply")
	}
	if got := catchService.ServiceRoot; got != "" {
		t.Fatalf("catch ServiceRoot = %q, want default root", got)
	}
	if len(ops.installRequests) != 1 {
		t.Fatalf("installRequests = %#v, want one", ops.installRequests)
	}
	req := ops.installRequests[0]
	if req.PinCatchServiceRoot {
		t.Fatalf("PinCatchServiceRoot = true, want default catch root")
	}
	wantCalls := []string{
		"install-catch:" + root + ":" + newServicesRoot,
		"restart-catch:" + root + ":" + newServicesRoot,
		"verify-info:" + root + ":" + newServicesRoot,
	}
	if !reflect.DeepEqual(ops.calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", ops.calls, wantCalls)
	}
}

func TestHostStorageApplyDefaultServerMovesPinnedCatchRootWhenServicesRootAlreadyDesired(t *testing.T) {
	root := t.TempDir()
	oldServicesRoot := filepath.Join(root, "old-services")
	servicesRoot := filepath.Join(root, "services")
	oldCatch := filepath.Join(oldServicesRoot, CatchService)
	newCatch := filepath.Join(servicesRoot, CatchService)
	if err := os.MkdirAll(oldCatch, 0o755); err != nil {
		t.Fatalf("MkdirAll old catch root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldCatch, "marker"), []byte("catch state"), 0o644); err != nil {
		t.Fatalf("WriteFile catch marker: %v", err)
	}
	server := newTestHostStorageServer(t, Config{
		RootDir:      root,
		ServicesRoot: servicesRoot,
	}, map[string]*db.Service{
		CatchService: {
			Name:        CatchService,
			ServiceType: db.ServiceTypeSystemd,
			ServiceRoot: oldCatch,
		},
	})
	ops := &recordingHostStorageDefaultCatchOps{}
	withHostStorageDefaultCatchOps(t, ops)
	plan, err := server.PlanHostStorage(context.Background(), catchrpc.HostStoragePlanRequest{
		Set: catchrpc.HostStorageSetRequest{
			ServicesRoot:    &catchrpc.HostStorageTarget{Value: servicesRoot},
			MigrateServices: catchrpc.HostStorageMigrateAll,
		},
	})
	if err != nil {
		t.Fatalf("PlanHostStorage: %v", err)
	}
	if len(plan.ServicesAction.AffectedServices) != 0 {
		t.Fatalf("AffectedServices = %#v, want no user service moves", plan.ServicesAction.AffectedServices)
	}
	if !plan.RequiresRestart {
		t.Fatalf("RequiresRestart = false, want catch restart")
	}
	if !plan.CatchAction.Move || plan.CatchAction.From != oldCatch || plan.CatchAction.To != newCatch {
		t.Fatalf("CatchAction = %#v, want catch root move", plan.CatchAction)
	}

	result, err := server.ApplyHostStoragePlan(context.Background(), plan, true, nil)
	if err != nil {
		t.Fatalf("ApplyHostStoragePlan: %v", err)
	}
	if !result.Restarted {
		t.Fatalf("Restarted = false, want true")
	}
	if got, err := os.ReadFile(filepath.Join(newCatch, "marker")); err != nil || string(got) != "catch state" {
		t.Fatalf("moved catch marker = %q, %v; want catch state", string(got), err)
	}
	dv, storeErr := server.cfg.DB.Get()
	if storeErr != nil {
		t.Fatalf("store.Get: %v", storeErr)
	}
	catchService := dv.Services().Get(CatchService).AsStruct()
	if catchService == nil {
		t.Fatalf("catch service missing after apply")
	}
	if got := catchService.ServiceRoot; got != "" {
		t.Fatalf("catch ServiceRoot = %q, want default root", got)
	}
}

func TestHostStorageApplyBuildsServiceMovesBeforeCreatingZFSDatasets(t *testing.T) {
	root := t.TempDir()
	oldServicesRoot := filepath.Join(root, "services")
	newServicesRoot := filepath.Join(root, "services2")
	runner := &recordingHostStorageZFS{}
	applier, _ := newTestHostStorageApplier(t, Config{
		RootDir:      root,
		ServicesRoot: oldServicesRoot,
	}, map[string]*db.Service{
		"api": {Name: "api", ServiceType: db.ServiceTypeSystemd},
	})
	applier.zfs = runner.Run
	plan := testHostStorageApplyServicesPlan(root, oldServicesRoot, newServicesRoot, catchrpc.HostStorageMigrateAll, "api", "missing")
	plan.ZFSDatasetsToCreate = []string{"tank/yeet/services2"}

	_, err := applier.Apply(context.Background(), plan, true, nil)
	if err == nil || !strings.Contains(err.Error(), `service "missing" not found`) {
		t.Fatalf("Apply error = %v, want invalid service before zfs create", err)
	}
	for _, call := range runner.calls {
		if len(call) > 0 && call[0] == "create" {
			t.Fatalf("zfs calls = %#v, want no create before service move validation", runner.calls)
		}
	}
}

func TestHostStorageApplyDataDirTargetCompatibility(t *testing.T) {
	root := t.TempDir()

	t.Run("missing", func(t *testing.T) {
		if err := ensureHostStorageDataDirTargetCompatible(filepath.Join(root, "missing")); err != nil {
			t.Fatalf("ensureHostStorageDataDirTargetCompatible missing: %v", err)
		}
	})

	t.Run("empty", func(t *testing.T) {
		target := filepath.Join(root, "empty")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatalf("MkdirAll target: %v", err)
		}
		if err := ensureHostStorageDataDirTargetCompatible(target); err != nil {
			t.Fatalf("ensureHostStorageDataDirTargetCompatible empty: %v", err)
		}
	})

	t.Run("compatible catch layout", func(t *testing.T) {
		target := filepath.Join(root, "compatible")
		for _, dir := range []string{"backups", "mounts", "registry", "services", "tsd", "tsnet", "vm-images"} {
			if err := os.MkdirAll(filepath.Join(target, dir), 0o755); err != nil {
				t.Fatalf("MkdirAll %s: %v", dir, err)
			}
		}
		if err := os.WriteFile(filepath.Join(target, "install.json"), []byte("{}"), 0o600); err != nil {
			t.Fatalf("WriteFile install.json: %v", err)
		}
		if err := os.WriteFile(filepath.Join(target, "db.json"), []byte("{}"), 0o644); err != nil {
			t.Fatalf("WriteFile db.json: %v", err)
		}
		if err := os.WriteFile(filepath.Join(target, "db.json.bak"), []byte("{}"), 0o644); err != nil {
			t.Fatalf("WriteFile db.json.bak: %v", err)
		}
		if err := os.WriteFile(filepath.Join(target, "catch.lock"), nil, 0o600); err != nil {
			t.Fatalf("WriteFile catch.lock: %v", err)
		}
		if err := os.WriteFile(filepath.Join(target, "id_ed25519"), []byte("key"), 0o600); err != nil {
			t.Fatalf("WriteFile id_ed25519: %v", err)
		}
		if err := ensureHostStorageDataDirTargetCompatible(target); err != nil {
			t.Fatalf("ensureHostStorageDataDirTargetCompatible compatible: %v", err)
		}
	})

	t.Run("non-empty allowlisted but missing db or registry", func(t *testing.T) {
		target := filepath.Join(root, "missing-anchors")
		if err := os.MkdirAll(filepath.Join(target, "tsnet"), 0o755); err != nil {
			t.Fatalf("MkdirAll tsnet: %v", err)
		}
		err := ensureHostStorageDataDirTargetCompatible(target)
		if err == nil || !strings.Contains(err.Error(), "does not look like a compatible catch data directory") {
			t.Fatalf("ensureHostStorageDataDirTargetCompatible error = %v, want missing anchors rejection", err)
		}
	})

	t.Run("incompatible non-empty", func(t *testing.T) {
		target := filepath.Join(root, "incompatible")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatalf("MkdirAll target: %v", err)
		}
		if err := os.WriteFile(filepath.Join(target, "notes.txt"), []byte("not catch state"), 0o644); err != nil {
			t.Fatalf("WriteFile notes: %v", err)
		}
		err := ensureHostStorageDataDirTargetCompatible(target)
		if err == nil || !strings.Contains(err.Error(), "does not look like a compatible catch data directory") {
			t.Fatalf("ensureHostStorageDataDirTargetCompatible error = %v, want incompatible target", err)
		}
	})

	t.Run("file target", func(t *testing.T) {
		target := filepath.Join(root, "file")
		if err := os.WriteFile(target, []byte("not a dir"), 0o644); err != nil {
			t.Fatalf("WriteFile target: %v", err)
		}
		err := ensureHostStorageDataDirTargetCompatible(target)
		if err == nil || !strings.Contains(err.Error(), "is not a directory") {
			t.Fatalf("ensureHostStorageDataDirTargetCompatible error = %v, want file target rejection", err)
		}
	})
}

func newTestHostStoragePlanner(t *testing.T, config Config, services map[string]*db.Service) *hostStoragePlanner {
	t.Helper()
	store := db.NewStore(filepath.Join(t.TempDir(), "db.json"), config.ServicesRoot)
	if services == nil {
		services = map[string]*db.Service{}
	}
	if err := store.Set(&db.Data{DataVersion: db.CurrentDataVersion, Services: services}); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	config.DB = store
	return &hostStoragePlanner{config: config, store: store}
}

func hostStorageTestZFSRunner(mountpoints map[string]string) zfsCommandRunner {
	return func(ctx context.Context, args ...string) (string, string, error) {
		if err := ctx.Err(); err != nil {
			return "", "", err
		}
		if len(args) == 5 && args[0] == "list" && args[1] == "-H" && args[2] == "-o" && args[3] == "name" {
			dataset := args[4]
			if _, ok := mountpoints[dataset]; ok {
				return dataset + "\n", "", nil
			}
			return "", "dataset does not exist", errors.New("dataset does not exist")
		}
		if len(args) == 6 && args[0] == "get" && args[1] == "-H" && args[2] == "-o" && args[3] == "value" && args[4] == "mountpoint" {
			dataset := args[5]
			mountpoint, ok := mountpoints[dataset]
			if !ok {
				return "", "dataset does not exist", errors.New("dataset does not exist")
			}
			return mountpoint + "\n", "", nil
		}
		return "", "", fmt.Errorf("unexpected zfs args: %v", args)
	}
}

func newTestHostStorageServer(t *testing.T, config Config, services map[string]*db.Service) *Server {
	t.Helper()
	if config.RootDir == "" {
		config.RootDir = t.TempDir()
	}
	if config.ServicesRoot == "" {
		config.ServicesRoot = filepath.Join(config.RootDir, "services")
	}
	if config.MountsRoot == "" {
		config.MountsRoot = filepath.Join(config.RootDir, "mounts")
	}
	if config.RegistryRoot == "" {
		config.RegistryRoot = filepath.Join(config.RootDir, "registry")
	}
	if config.RegistryStorage == nil {
		storage, err := registry.NewFilesystemStorage(config.RegistryRoot)
		if err != nil {
			t.Fatalf("NewFilesystemStorage: %v", err)
		}
		config.RegistryStorage = storage
	}
	if services == nil {
		services = map[string]*db.Service{}
	}
	store := db.NewStore(filepath.Join(config.RootDir, "db.json"), config.ServicesRoot)
	if err := store.Set(&db.Data{DataVersion: db.CurrentDataVersion, Services: services}); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	config.DB = store
	return NewUnstartedServer(&config)
}

func newTestHostStorageApplier(t *testing.T, config Config, services map[string]*db.Service) (*hostStorageApplier, *recordingHostStorageApplyOps) {
	t.Helper()
	if config.RootDir == "" {
		config.RootDir = t.TempDir()
	}
	if config.ServicesRoot == "" {
		config.ServicesRoot = filepath.Join(config.RootDir, "services")
	}
	if services == nil {
		services = map[string]*db.Service{}
	}
	if _, ok := services[CatchService]; !ok {
		services[CatchService] = &db.Service{
			Name:        CatchService,
			ServiceType: db.ServiceTypeSystemd,
		}
	}
	store := db.NewStore(filepath.Join(config.RootDir, "db.json"), config.ServicesRoot)
	if err := store.Set(&db.Data{DataVersion: db.CurrentDataVersion, Services: services}); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	config.DB = store
	ops := &recordingHostStorageApplyOps{
		running:  make(map[string]bool),
		stopErr:  make(map[string]error),
		startErr: make(map[string]error),
		moveErr:  make(map[string]error),
	}
	applier := &hostStorageApplier{
		config: config,
		store:  store,
		ops: hostStorageApplyOperations{
			isServiceRunning:                        ops.isServiceRunning,
			runnerForService:                        ops.runnerForService,
			materializeServiceRootMigration:         ops.materializeServiceRootMigration,
			applyServiceRootMigrationRuntimeChanges: ops.applyServiceRootMigrationRuntimeChanges,
			reinstallCatchUnit:                      ops.reinstallCatchUnit,
			restartCatch:                            ops.restartCatch,
			verifyCatchInfo:                         ops.verifyCatchInfo,
		},
	}
	return applier, ops
}

func testHostStorageApplyServicesPlan(dataDir, oldServicesRoot, newServicesRoot string, mode catchrpc.HostStorageMigrateServices, services ...string) catchrpc.HostStoragePlan {
	moves := make([]catchrpc.HostStorageServiceMove, 0, len(services))
	for _, service := range services {
		from := filepath.Join(oldServicesRoot, service)
		to := filepath.Join(newServicesRoot, service)
		if mode == catchrpc.HostStorageMigrateNone {
			to = from
		}
		moves = append(moves, catchrpc.HostStorageServiceMove{
			Name: service,
			From: from,
			To:   to,
		})
	}
	return catchrpc.HostStoragePlan{
		Current: catchrpc.HostStorageState{DataDir: dataDir, ServicesRoot: oldServicesRoot},
		Desired: catchrpc.HostStorageState{DataDir: dataDir, ServicesRoot: newServicesRoot},
		ServicesAction: catchrpc.HostStorageServicesAction{
			Mode:             mode,
			From:             oldServicesRoot,
			To:               newServicesRoot,
			AffectedServices: moves,
		},
		RequiresRestart: true,
	}
}

type recordingHostStorageApplyOps struct {
	running  map[string]bool
	stopErr  map[string]error
	startErr map[string]error
	moveErr  map[string]error
	calls    []string
}

func (o *recordingHostStorageApplyOps) isServiceRunning(_ context.Context, name string) (bool, error) {
	o.calls = append(o.calls, "running:"+name)
	return o.running[name], nil
}

func (o *recordingHostStorageApplyOps) runnerForService(_ context.Context, name string) (ServiceRunner, error) {
	return hostStorageRecordingServiceRunner{name: name, ops: o}, nil
}

func (o *recordingHostStorageApplyOps) materializeServiceRootMigration(_ context.Context, plan serviceRootMigrationPlan, _ io.Writer) error {
	o.calls = append(o.calls, "move:"+plan.ServiceName)
	return o.moveErr[plan.ServiceName]
}

func (o *recordingHostStorageApplyOps) applyServiceRootMigrationRuntimeChanges(_ context.Context, _ Config, before db.Service, _ db.Service, _ io.Writer) error {
	o.calls = append(o.calls, "runtime:"+before.Name)
	return nil
}

func (o *recordingHostStorageApplyOps) reinstallCatchUnit(_ context.Context, req hostStorageInstallRequest, _ io.Writer) error {
	o.calls = append(o.calls, "install-catch:"+req.DataDir+":"+req.ServicesRoot)
	return nil
}

func (o *recordingHostStorageApplyOps) restartCatch(_ context.Context, _ hostStorageInstallRequest, _ io.Writer) error {
	o.calls = append(o.calls, "restart-catch")
	return nil
}

func (o *recordingHostStorageApplyOps) verifyCatchInfo(_ context.Context, desired catchrpc.HostStorageState, _ Config) (ServerInfo, error) {
	o.calls = append(o.calls, "verify-info:"+desired.DataDir+":"+desired.ServicesRoot)
	return ServerInfo{RootDir: desired.DataDir, ServicesDir: desired.ServicesRoot}, nil
}

func (o *recordingHostStorageApplyOps) callsWithPrefix(prefix string) []string {
	var out []string
	for _, call := range o.calls {
		if strings.HasPrefix(call, prefix) {
			out = append(out, call)
		}
	}
	return out
}

type hostStorageRecordingServiceRunner struct {
	name string
	ops  *recordingHostStorageApplyOps
}

func (r hostStorageRecordingServiceRunner) SetNewCmd(func(string, ...string) *exec.Cmd) {}

func (r hostStorageRecordingServiceRunner) Start() error {
	r.ops.calls = append(r.ops.calls, "start:"+r.name)
	if err := r.ops.startErr[r.name]; err != nil {
		return err
	}
	r.ops.running[r.name] = true
	return nil
}

func (r hostStorageRecordingServiceRunner) Stop() error {
	r.ops.calls = append(r.ops.calls, "stop:"+r.name)
	if err := r.ops.stopErr[r.name]; err != nil {
		return err
	}
	r.ops.running[r.name] = false
	return nil
}

func (r hostStorageRecordingServiceRunner) Restart() error {
	return nil
}

func (r hostStorageRecordingServiceRunner) Logs(*svc.LogOptions) error {
	return nil
}

func (r hostStorageRecordingServiceRunner) Remove() error {
	return nil
}

type recordingHostStorageDefaultCatchOps struct {
	calls           []string
	installRequests []hostStorageInstallRequest
}

func withHostStorageDefaultCatchOps(t *testing.T, ops *recordingHostStorageDefaultCatchOps) {
	t.Helper()
	oldInstall := hostStorageInstallCatchUnit
	oldRestart := hostStorageRestartCatch
	oldVerify := hostStorageVerifyCatchInfo
	hostStorageInstallCatchUnit = ops.installCatchUnit
	hostStorageRestartCatch = ops.restartCatch
	hostStorageVerifyCatchInfo = ops.verifyCatchInfo
	t.Cleanup(func() {
		hostStorageInstallCatchUnit = oldInstall
		hostStorageRestartCatch = oldRestart
		hostStorageVerifyCatchInfo = oldVerify
	})
}

func (o *recordingHostStorageDefaultCatchOps) installCatchUnit(_ context.Context, req hostStorageInstallRequest, _ io.Writer) error {
	o.calls = append(o.calls, "install-catch:"+req.DataDir+":"+req.ServicesRoot)
	o.installRequests = append(o.installRequests, req)
	return nil
}

func (o *recordingHostStorageDefaultCatchOps) restartCatch(_ context.Context, req hostStorageInstallRequest, _ io.Writer) error {
	o.calls = append(o.calls, "restart-catch:"+req.DataDir+":"+req.ServicesRoot)
	return nil
}

func (o *recordingHostStorageDefaultCatchOps) verifyCatchInfo(_ context.Context, desired catchrpc.HostStorageState, _ Config) (ServerInfo, error) {
	o.calls = append(o.calls, "verify-info:"+desired.DataDir+":"+desired.ServicesRoot)
	return ServerInfo{RootDir: desired.DataDir, ServicesDir: desired.ServicesRoot}, nil
}

func firstCallIndexWithPrefix(calls []string, prefix string) int {
	for idx, call := range calls {
		if strings.HasPrefix(call, prefix) {
			return idx
		}
	}
	return -1
}

func countHostStorageCalls(calls []string, want string) int {
	count := 0
	for _, call := range calls {
		if call == want {
			count++
		}
	}
	return count
}

type recordingHostStorageZFS struct {
	datasets map[string]fakeZFSDataset
	calls    [][]string
}

func (r *recordingHostStorageZFS) Run(ctx context.Context, args ...string) (string, string, error) {
	r.calls = append(r.calls, slices.Clone(args))
	return fakeZFSRunner(r.datasets).Run(ctx, args...)
}
