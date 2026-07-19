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
	"slices"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestExecuteVMJailerTransitionMarksReadyLast(t *testing.T) {
	var got []string
	deps := vmJailerTransitionDeps{
		validate: func(context.Context, vmJailerTransitionPlan) error {
			got = append(got, "validate")
			return nil
		},
		cleanupJail: func(vmJailPlan) error {
			got = append(got, "cleanup-jail")
			return nil
		},
		delegateStorage: func(string, string, vmRuntimeIdentity) error {
			got = append(got, "delegate-storage")
			return nil
		},
		runNetwork: func([][]string, vmNetworkCommandMode) error {
			got = append(got, "network")
			return nil
		},
		markReady: func(string) error {
			got = append(got, "mark-ready")
			return nil
		},
	}
	if err := executeVMJailerTransition(context.Background(), testVMJailerTransitionPlan(), deps); err != nil {
		t.Fatal(err)
	}
	want := []string{"validate", "cleanup-jail", "delegate-storage", "network", "network", "mark-ready"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("operations = %v, want %v", got, want)
	}
}

func TestExecuteVMJailerTransitionFailureDoesNotMarkReady(t *testing.T) {
	marked := false
	deps := testVMJailerTransitionDeps()
	deps.runNetwork = func([][]string, vmNetworkCommandMode) error { return errors.New("network failed") }
	deps.markReady = func(string) error { marked = true; return nil }
	if err := executeVMJailerTransition(context.Background(), testVMJailerTransitionPlan(), deps); err == nil {
		t.Fatal("transition succeeded")
	}
	if marked {
		t.Fatal("readiness marker was written after failure")
	}
}

func TestExecuteVMJailerTransitionRetryRepeatsSafely(t *testing.T) {
	var got []string
	setupCalls := 0
	deps := vmJailerTransitionDeps{
		validate: func(context.Context, vmJailerTransitionPlan) error {
			got = append(got, "validate")
			return nil
		},
		cleanupJail: func(vmJailPlan) error {
			got = append(got, "cleanup-jail")
			return nil
		},
		delegateStorage: func(string, string, vmRuntimeIdentity) error {
			got = append(got, "delegate-storage")
			return nil
		},
		runNetwork: func(_ [][]string, mode vmNetworkCommandMode) error {
			got = append(got, "network-"+string(mode))
			if mode == vmNetworkCommandModeSetup {
				setupCalls++
				if setupCalls == 1 {
					return errors.New("setup failed")
				}
			}
			return nil
		},
		markReady: func(string) error {
			got = append(got, "mark-ready")
			return nil
		},
	}

	plan := testVMJailerTransitionPlan()
	if err := executeVMJailerTransition(context.Background(), plan, deps); err == nil {
		t.Fatal("first transition succeeded")
	}
	if err := executeVMJailerTransition(context.Background(), plan, deps); err != nil {
		t.Fatalf("retry transition: %v", err)
	}
	want := []string{
		"validate", "cleanup-jail", "delegate-storage", "network-cleanup", "network-setup",
		"validate", "cleanup-jail", "delegate-storage", "network-cleanup", "network-setup", "mark-ready",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("retry operations = %v, want %v", got, want)
	}
}

func TestNewVMJailerTransitionPlanUsesDatabasePaths(t *testing.T) {
	fixture := newVMJailerTransitionFixture(t)
	identity := vmRuntimeIdentity{UID: 812, GID: 813}

	plan, err := newVMJailerTransitionPlan(fixture.Data, vmJailerTransitionInput{
		DataRoot:    fixture.DataRoot,
		Service:     fixture.Service,
		ServiceRoot: fixture.ServiceRoot,
	}, identity)
	if err != nil {
		t.Fatalf("newVMJailerTransitionPlan: %v", err)
	}

	if plan.Service != fixture.Service || plan.Root != fixture.ServiceRoot || plan.Disk != fixture.Disk {
		t.Fatalf("plan service/root/disk = %q/%q/%q", plan.Service, plan.Root, plan.Disk)
	}
	wantRuntime := VMConsoleProxyConfig{
		Firecracker:   filepath.Join(fixture.ImageDir, "firecracker"),
		Jailer:        filepath.Join(fixture.ImageDir, "jailer"),
		JailerBase:    vmJailerBaseForDataRoot(fixture.DataRoot),
		APISocket:     fixture.APISocket,
		ConfigFile:    fixture.ConfigFile,
		ConsoleSocket: fixture.ConsoleSocket,
		Service:       fixture.Service,
		ServiceRoot:   fixture.ServiceRoot,
		DiskPath:      fixture.Disk,
	}
	if !reflect.DeepEqual(plan.Runtime, wantRuntime) {
		t.Fatalf("runtime = %#v, want %#v", plan.Runtime, wantRuntime)
	}
	if plan.Identity != identity || plan.Network.TapOwner != identity {
		t.Fatalf("plan identity/network owner = %#v/%#v, want %#v", plan.Identity, plan.Network.TapOwner, identity)
	}
	wantJailRoot := filepath.Join(fixture.DataRoot, "vm-jailer", "firecracker", vmJailerID(fixture.Service), "root")
	if plan.Jail.JailRoot != wantJailRoot {
		t.Fatalf("jail root = %q, want %q", plan.Jail.JailRoot, wantJailRoot)
	}
	for _, socket := range []string{fixture.APISocket, fixture.VsockSocket} {
		if !hasVMJailSocketLink(plan.Jail, socket) {
			t.Fatalf("jail socket links = %#v, missing stored socket %q", plan.Jail.SocketLinks, socket)
		}
	}
}

func TestNewVMJailerTransitionPlanUsesImageRootFSFallback(t *testing.T) {
	fixture := newVMJailerTransitionFixture(t)
	service := fixture.Data.Services().Get(fixture.Service).AsStruct()
	service.VM.Disk.Path = ""
	config := mustRenderFirecrackerConfig(t, fixture.Kernel, fixture.RootFS, fixture.VsockSocket)
	if err := os.WriteFile(fixture.ConfigFile, config, 0o644); err != nil {
		t.Fatal(err)
	}
	store := db.NewStore(filepath.Join(fixture.DataRoot, "fallback-db.json"), filepath.Join(fixture.DataRoot, "services"))
	if err := store.Set(&db.Data{Services: map[string]*db.Service{fixture.Service: service}}); err != nil {
		t.Fatal(err)
	}
	dv, err := store.Get()
	if err != nil {
		t.Fatal(err)
	}

	plan, err := newVMJailerTransitionPlan(&dv, vmJailerTransitionInput{
		DataRoot:    fixture.DataRoot,
		Service:     fixture.Service,
		ServiceRoot: fixture.ServiceRoot,
	}, vmRuntimeIdentity{UID: 812, GID: 813})
	if err != nil {
		t.Fatalf("newVMJailerTransitionPlan: %v", err)
	}
	if plan.Disk != fixture.RootFS || plan.Runtime.DiskPath != fixture.RootFS {
		t.Fatalf("fallback disk = %q/%q, want %q", plan.Disk, plan.Runtime.DiskPath, fixture.RootFS)
	}
}

func TestNewVMJailerTransitionPlanRejectsCheckpointSymlink(t *testing.T) {
	fixture := newVMJailerTransitionFixture(t)
	checkpointDir := filepath.Join(serviceDataDirForRoot(fixture.ServiceRoot), "checkpoints", "full")
	if err := os.MkdirAll(checkpointDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fixture.Disk, filepath.Join(checkpointDir, "memory.bin")); err != nil {
		t.Fatal(err)
	}
	oldChown := vmJailStorageChown
	mutated := false
	vmJailStorageChown = func(*os.File, int, int) error {
		mutated = true
		return nil
	}
	t.Cleanup(func() { vmJailStorageChown = oldChown })

	_, err := newVMJailerTransitionPlan(fixture.Data, vmJailerTransitionInput{
		DataRoot:    fixture.DataRoot,
		Service:     fixture.Service,
		ServiceRoot: fixture.ServiceRoot,
	}, vmRuntimeIdentity{UID: 812, GID: 813})
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("newVMJailerTransitionPlan error = %v, want checkpoint symlink rejection", err)
	}
	if mutated {
		t.Fatal("checkpoint validation mutated storage")
	}
}

func TestExecuteVMJailerTransitionRevalidatesConfigDiskBeforeMutation(t *testing.T) {
	fixture := newVMJailerTransitionFixture(t)
	identity := vmRuntimeIdentity{UID: 812, GID: 813}
	plan, err := newVMJailerTransitionPlan(fixture.Data, vmJailerTransitionInput{
		DataRoot:    fixture.DataRoot,
		Service:     fixture.Service,
		ServiceRoot: fixture.ServiceRoot,
	}, identity)
	if err != nil {
		t.Fatalf("newVMJailerTransitionPlan: %v", err)
	}

	otherDisk := filepath.Join(serviceDataDirForRoot(fixture.ServiceRoot), "other.raw")
	if err := os.WriteFile(otherDisk, []byte("other disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeVMJailerTransitionFirecrackerConfig(t, fixture, []firecrackerDrive{{
		DriveID:      "rootfs",
		PathOnHost:   otherDisk,
		IsRootDevice: true,
	}})

	restore := stubVMJailerTransitionValidation(t)
	defer restore()
	var mutations []string
	deps := defaultVMJailerTransitionDeps()
	deps.cleanupJail = func(vmJailPlan) error { mutations = append(mutations, "cleanup"); return nil }
	deps.delegateStorage = func(string, string, vmRuntimeIdentity) error {
		mutations = append(mutations, "ownership")
		return nil
	}
	deps.runNetwork = func([][]string, vmNetworkCommandMode) error {
		mutations = append(mutations, "network")
		return nil
	}
	deps.markReady = func(string) error { mutations = append(mutations, "readiness"); return nil }

	err = executeVMJailerTransition(context.Background(), plan, deps)
	if err == nil || !strings.Contains(err.Error(), "configured VM disk") {
		t.Fatalf("executeVMJailerTransition error = %v, want configured disk mismatch", err)
	}
	if len(mutations) != 0 {
		t.Fatalf("transition mutated state after config disk mismatch: %v", mutations)
	}
}

func TestVMJailerTransitionRejectsExtraWritableConfigDriveWithoutMutation(t *testing.T) {
	fixture := newVMJailerTransitionFixture(t)
	extraDisk := filepath.Join(serviceDataDirForRoot(fixture.ServiceRoot), "extra.raw")
	if err := os.WriteFile(extraDisk, []byte("extra disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeVMJailerTransitionFirecrackerConfig(t, fixture, []firecrackerDrive{
		{DriveID: "rootfs", PathOnHost: fixture.Disk, IsRootDevice: true},
		{DriveID: "extra", PathOnHost: extraDisk},
	})

	assertVMJailerTransitionEnsureRejectedWithoutMutation(t, fixture, fixture.Data, "extra writable")
}

func TestNewVMJailerTransitionPlanAllowsSafeConfigDriveRules(t *testing.T) {
	tests := []struct {
		name   string
		drives func(*testing.T, vmJailerTransitionFixture) []firecrackerDrive
	}{
		{
			name: "single writable drive without root flag",
			drives: func(_ *testing.T, fixture vmJailerTransitionFixture) []firecrackerDrive {
				return []firecrackerDrive{{DriveID: "rootfs", PathOnHost: fixture.Disk}}
			},
		},
		{
			name: "additional read-only drive",
			drives: func(t *testing.T, fixture vmJailerTransitionFixture) []firecrackerDrive {
				readOnly := filepath.Join(fixture.ImageDir, "seed.ext4")
				if err := os.WriteFile(readOnly, []byte("seed"), 0o644); err != nil {
					t.Fatal(err)
				}
				return []firecrackerDrive{
					{DriveID: "rootfs", PathOnHost: fixture.Disk, IsRootDevice: true},
					{DriveID: "seed", PathOnHost: readOnly, IsReadOnly: true},
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newVMJailerTransitionFixture(t)
			writeVMJailerTransitionFirecrackerConfig(t, fixture, tt.drives(t, fixture))
			if _, err := newVMJailerTransitionPlan(fixture.Data, vmJailerTransitionInput{
				DataRoot:    fixture.DataRoot,
				Service:     fixture.Service,
				ServiceRoot: fixture.ServiceRoot,
			}, vmRuntimeIdentity{UID: 812, GID: 813}); err != nil {
				t.Fatalf("newVMJailerTransitionPlan: %v", err)
			}
		})
	}
}

func TestVMJailerTransitionRejectsRawDiskAncestorSymlinkWithoutMutation(t *testing.T) {
	fixture := newVMJailerTransitionFixture(t)
	external := filepath.Join(resolvedVMJailerTransitionTempDir(t), "external-disk")
	if err := os.MkdirAll(external, 0o755); err != nil {
		t.Fatal(err)
	}
	disk := filepath.Join(external, "rootfs.raw")
	if err := os.WriteFile(disk, []byte("disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(serviceDataDirForRoot(fixture.ServiceRoot), "linked")
	if err := os.Symlink(external, link); err != nil {
		t.Fatal(err)
	}
	linkedDisk := filepath.Join(link, "rootfs.raw")
	writeVMJailerTransitionFirecrackerConfig(t, fixture, []firecrackerDrive{{
		DriveID:      "rootfs",
		PathOnHost:   linkedDisk,
		IsRootDevice: true,
	}})
	dv := replaceVMJailerTransitionService(t, fixture, func(service *db.Service) {
		service.VM.Disk.Path = linkedDisk
	})

	assertVMJailerTransitionEnsureRejectedWithoutMutation(t, fixture, dv, "symbolic link")
}

func TestVMJailerTransitionRejectsCheckpointAncestorSymlinkWithoutMutation(t *testing.T) {
	fixture := newVMJailerTransitionFixture(t)
	serviceData := serviceDataDirForRoot(fixture.ServiceRoot)
	if err := os.RemoveAll(serviceData); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(resolvedVMJailerTransitionTempDir(t), "external-data")
	checkpoint := filepath.Join(external, "checkpoints", "full", "memory.bin")
	if err := os.MkdirAll(filepath.Dir(checkpoint), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(checkpoint, []byte("checkpoint"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, serviceData); err != nil {
		t.Fatal(err)
	}
	writeVMJailerTransitionFirecrackerConfig(t, fixture, []firecrackerDrive{{
		DriveID:      "rootfs",
		PathOnHost:   fixture.RootFS,
		IsRootDevice: true,
	}})
	dv := replaceVMJailerTransitionService(t, fixture, func(service *db.Service) {
		service.VM.Disk.Path = ""
	})

	assertVMJailerTransitionEnsureRejectedWithoutMutation(t, fixture, dv, "symbolic link")
}

func TestVMJailerTransitionRejectsUnsafeStoredSocketWithoutMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, vmJailerTransitionFixture, *db.Service)
	}{
		{
			name: "API socket outside service run directory",
			mutate: func(_ *testing.T, fixture vmJailerTransitionFixture, service *db.Service) {
				service.VM.Sockets.APISocketPath = filepath.Join(fixture.DataRoot, "unsafe", "api.sock")
			},
		},
		{
			name: "vsock socket outside service run directory",
			mutate: func(t *testing.T, fixture vmJailerTransitionFixture, service *db.Service) {
				unsafe := filepath.Join(fixture.DataRoot, "unsafe", "vsock.sock")
				service.VM.Sockets.VsockSocketPath = unsafe
				writeVMJailerTransitionFirecrackerConfig(t, fixture, []firecrackerDrive{{
					DriveID:      "rootfs",
					PathOnHost:   fixture.Disk,
					IsRootDevice: true,
				}}, unsafe)
			},
		},
		{
			name: "console socket outside service run directory",
			mutate: func(_ *testing.T, fixture vmJailerTransitionFixture, service *db.Service) {
				service.VM.Console.SocketPath = filepath.Join(fixture.DataRoot, "unsafe", "console.sock")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newVMJailerTransitionFixture(t)
			dv := replaceVMJailerTransitionService(t, fixture, func(service *db.Service) {
				tt.mutate(t, fixture, service)
			})
			assertVMJailerTransitionEnsureRejectedWithoutMutation(t, fixture, dv, "service run directory")
		})
	}
}

func TestVMJailerTransitionStorageDelegation(t *testing.T) {
	oldChown := vmJailStorageChown
	oldChmod := vmJailStorageChmod
	t.Cleanup(func() {
		vmJailStorageChown = oldChown
		vmJailStorageChmod = oldChmod
	})

	tests := []struct {
		name      string
		disk      func(string) string
		wantOwned bool
	}{
		{
			name: "raw disk is delegated",
			disk: func(root string) string {
				path := filepath.Join(serviceDataDirForRoot(root), "rootfs.raw")
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("disk"), 0o600); err != nil {
					t.Fatal(err)
				}
				return path
			},
			wantOwned: true,
		},
		{
			name:      "zvol host node is unchanged",
			disk:      func(string) string { return "/dev/zvol/flash/yeet/vms/devbox/root" },
			wantOwned: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := resolvedVMJailerTransitionTempDir(t)
			disk := tt.disk(root)
			var owned []string
			vmJailStorageChown = func(file *os.File, uid, gid int) error {
				if uid != 812 || gid != 813 {
					t.Fatalf("chown identity = %d:%d", uid, gid)
				}
				owned = append(owned, file.Name())
				return nil
			}
			vmJailStorageChmod = func(*os.File, os.FileMode) error { return nil }

			if err := delegateVMJailStorage(root, disk, vmRuntimeIdentity{UID: 812, GID: 813}); err != nil {
				t.Fatalf("delegateVMJailStorage: %v", err)
			}
			if got := slices.Contains(owned, disk); got != tt.wantOwned {
				t.Fatalf("disk ownership changed = %v, want %v; paths = %#v", got, tt.wantOwned, owned)
			}
			if !tt.wantOwned && len(owned) != 0 {
				t.Fatalf("zvol transition changed host ownership: %#v", owned)
			}
		})
	}
}

func testVMJailerTransitionPlan() vmJailerTransitionPlan {
	return vmJailerTransitionPlan{
		Service:  "devbox",
		Root:     "/srv/devbox",
		Disk:     "/srv/devbox/data/rootfs.raw",
		Identity: vmRuntimeIdentity{UID: 987, GID: 987},
		Network:  vmNetworkPlan{Service: "devbox", Interfaces: []vmNetworkInterfacePlan{{Mode: "lan", Tap: "yvm-devbox-l0", Bridge: "br0"}}},
		Jail:     vmJailPlan{ID: "yeet-devbox", JailRoot: "/var/lib/yeet/vm-jailer/firecracker/yeet-devbox/root"},
	}
}

func testVMJailerTransitionDeps() vmJailerTransitionDeps {
	return vmJailerTransitionDeps{
		validate:        func(context.Context, vmJailerTransitionPlan) error { return nil },
		cleanupJail:     func(vmJailPlan) error { return nil },
		delegateStorage: func(string, string, vmRuntimeIdentity) error { return nil },
		runNetwork:      func([][]string, vmNetworkCommandMode) error { return nil },
		markReady:       func(string) error { return nil },
	}
}

type vmJailerTransitionFixture struct {
	Data          *db.DataView
	DataRoot      string
	Service       string
	ServiceRoot   string
	ImageDir      string
	Kernel        string
	RootFS        string
	Disk          string
	ConfigFile    string
	APISocket     string
	VsockSocket   string
	ConsoleSocket string
}

func newVMJailerTransitionFixture(t *testing.T) vmJailerTransitionFixture {
	t.Helper()
	dataRoot := filepath.Join(resolvedVMJailerTransitionTempDir(t), "configured-data")
	service := "devbox"
	serviceRoot := filepath.Join(resolvedVMJailerTransitionTempDir(t), "custom-zfs-root", service)
	imageDir := filepath.Join(dataRoot, "vm-images", "custom-v1")
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	kernel := filepath.Join(imageDir, "kernel")
	rootFS := filepath.Join(imageDir, "rootfs.ext4")
	for path, mode := range map[string]os.FileMode{
		kernel:                                 0o644,
		rootFS:                                 0o644,
		filepath.Join(imageDir, "firecracker"): 0o755,
		filepath.Join(imageDir, "jailer"):      0o755,
	} {
		if err := os.WriteFile(path, []byte(path), mode); err != nil {
			t.Fatal(err)
		}
	}
	disk := filepath.Join(serviceDataDirForRoot(serviceRoot), "rootfs.raw")
	if err := os.MkdirAll(filepath.Dir(disk), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(disk, []byte("disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	runDir := serviceRunDirForRoot(serviceRoot)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(runDir, "firecracker.json")
	apiSocket := filepath.Join(runDir, "firecracker.sock")
	vsockSocket := filepath.Join(runDir, "vsock.sock")
	consoleSocket := filepath.Join(runDir, "serial.sock")
	if err := os.WriteFile(configFile, mustRenderFirecrackerConfig(t, kernel, disk, vsockSocket), 0o644); err != nil {
		t.Fatal(err)
	}
	network := newVMNetworkPlan(service, []string{"svc"}, vmNetworkInputs{ServiceIP: "192.168.100.12"})
	store := db.NewStore(filepath.Join(dataRoot, "db.json"), filepath.Join(dataRoot, "services"))
	if err := store.Set(&db.Data{Services: map[string]*db.Service{
		service: {
			Name:        service,
			ServiceType: db.ServiceTypeVM,
			ServiceRoot: serviceRoot,
			VM: &db.VMConfig{
				Runtime:  vmRuntimeFirecracker,
				Image:    db.VMImageConfig{Kernel: kernel, RootFS: rootFS},
				Disk:     db.VMDiskConfig{Backend: vmDiskBackendRaw, Path: disk},
				Networks: network.DBNetworks(),
				Console:  db.VMConsoleConfig{SocketPath: consoleSocket},
				Sockets:  db.VMSocketConfig{APISocketPath: apiSocket, VsockSocketPath: vsockSocket},
			},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	dv, err := store.Get()
	if err != nil {
		t.Fatal(err)
	}
	return vmJailerTransitionFixture{
		Data:          &dv,
		DataRoot:      dataRoot,
		Service:       service,
		ServiceRoot:   serviceRoot,
		ImageDir:      imageDir,
		Kernel:        kernel,
		RootFS:        rootFS,
		Disk:          disk,
		ConfigFile:    configFile,
		APISocket:     apiSocket,
		VsockSocket:   vsockSocket,
		ConsoleSocket: consoleSocket,
	}
}

func resolvedVMJailerTransitionTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func mustRenderFirecrackerConfig(t *testing.T, kernel, disk, vsock string) []byte {
	t.Helper()
	raw, err := renderFirecrackerConfig(firecrackerConfig{
		BootSource: firecrackerBootSource{KernelImagePath: kernel},
		Drives: []firecrackerDrive{{
			DriveID:      "rootfs",
			PathOnHost:   disk,
			IsRootDevice: true,
		}},
		Vsock: &firecrackerVsock{VsockID: "agent", GuestCID: vmAgentGuestCID, UDSPath: vsock},
	})
	if err != nil {
		t.Fatalf("renderFirecrackerConfig: %v", err)
	}
	return raw
}

func writeVMJailerTransitionFirecrackerConfig(t *testing.T, fixture vmJailerTransitionFixture, drives []firecrackerDrive, vsock ...string) {
	t.Helper()
	vsockPath := fixture.VsockSocket
	if len(vsock) > 0 {
		vsockPath = vsock[0]
	}
	raw, err := renderFirecrackerConfig(firecrackerConfig{
		BootSource: firecrackerBootSource{KernelImagePath: fixture.Kernel},
		Drives:     drives,
		Vsock:      &firecrackerVsock{VsockID: "agent", GuestCID: vmAgentGuestCID, UDSPath: vsockPath},
	})
	if err != nil {
		t.Fatalf("renderFirecrackerConfig: %v", err)
	}
	if err := os.WriteFile(fixture.ConfigFile, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func replaceVMJailerTransitionService(t *testing.T, fixture vmJailerTransitionFixture, mutate func(*db.Service)) *db.DataView {
	t.Helper()
	service := fixture.Data.Services().Get(fixture.Service).AsStruct()
	mutate(service)
	store := db.NewStore(filepath.Join(resolvedVMJailerTransitionTempDir(t), "db.json"), filepath.Join(resolvedVMJailerTransitionTempDir(t), "services"))
	if err := store.Set(&db.Data{Services: map[string]*db.Service{fixture.Service: service}}); err != nil {
		t.Fatal(err)
	}
	dv, err := store.Get()
	if err != nil {
		t.Fatal(err)
	}
	return &dv
}

func stubVMJailerTransitionValidation(t *testing.T) func() {
	t.Helper()
	oldTrustedInput := vmJailValidateTrustedInput
	oldTrustedReadOnly := vmJailValidateReadOnlyPath
	oldProbeVersion := vmJailProbeVersion
	vmJailValidateTrustedInput = func(string) error { return nil }
	vmJailValidateReadOnlyPath = func(string) error { return nil }
	vmJailProbeVersion = func(context.Context, string) (string, error) { return "Firecracker v1.12.0", nil }
	return func() {
		vmJailValidateTrustedInput = oldTrustedInput
		vmJailValidateReadOnlyPath = oldTrustedReadOnly
		vmJailProbeVersion = oldProbeVersion
	}
}

func assertVMJailerTransitionEnsureRejectedWithoutMutation(t *testing.T, fixture vmJailerTransitionFixture, dv *db.DataView, wantError string) {
	t.Helper()
	service := dv.Services().Get(fixture.Service)
	serviceRoot := service.ServiceRoot()
	instanceRoot := filepath.Join(vmJailerBaseForDataRoot(fixture.DataRoot), "firecracker", vmJailerID(fixture.Service))
	if err := os.MkdirAll(instanceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(instanceRoot, "must-survive")
	if err := os.WriteFile(sentinel, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}

	oldIdentity := vmNetworkEnsureRuntimeIdentity
	oldStorageChown := vmJailStorageChown
	oldStorageChmod := vmJailStorageChmod
	oldRunner := vmNetworkReconcileRunner
	restoreValidation := stubVMJailerTransitionValidation(t)
	mutatedOwnership := false
	mutatedNetwork := false
	vmNetworkEnsureRuntimeIdentity = func() (vmRuntimeIdentity, error) {
		return vmRuntimeIdentity{UID: 812, GID: 813}, nil
	}
	vmJailStorageChown = func(*os.File, int, int) error { mutatedOwnership = true; return nil }
	vmJailStorageChmod = func(*os.File, os.FileMode) error { mutatedOwnership = true; return nil }
	vmNetworkReconcileRunner = func([]string) error { mutatedNetwork = true; return nil }
	t.Cleanup(func() {
		vmNetworkEnsureRuntimeIdentity = oldIdentity
		vmJailStorageChown = oldStorageChown
		vmJailStorageChmod = oldStorageChmod
		vmNetworkReconcileRunner = oldRunner
		restoreValidation()
	})

	err := ensureVMNetworkFromDataView(context.Background(), dv, vmNetworkEnsureInput{
		DataRoot:    fixture.DataRoot,
		Service:     fixture.Service,
		ServiceRoot: serviceRoot,
	})
	if err == nil || !strings.Contains(err.Error(), wantError) {
		t.Fatalf("ensureVMNetworkFromDataView error = %v, want %q", err, wantError)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("stale jail was cleaned before validation: %v", err)
	}
	if mutatedOwnership {
		t.Fatal("storage ownership changed before validation")
	}
	if mutatedNetwork {
		t.Fatal("network state changed before validation")
	}
	readiness, err := vmJailerReadinessForRoot(serviceRoot)
	if err != nil {
		t.Fatal(err)
	}
	if readiness != vmJailerPendingRestart {
		t.Fatalf("readiness = %q, want pending", readiness)
	}
}

func hasVMJailSocketLink(plan vmJailPlan, hostPath string) bool {
	return slices.ContainsFunc(plan.SocketLinks, func(link vmJailSocketLink) bool {
		return link.HostPath == hostPath
	})
}
