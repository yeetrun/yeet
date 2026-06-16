// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
)

func TestVMSetRejectsNonVMService(t *testing.T) {
	server := newTestServer(t)
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		"api": {Name: "api", ServiceType: db.ServiceTypeDockerCompose},
	}}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}

	err := server.updateVMServiceSettings(context.Background(), "api", cli.VMSetFlags{CPUs: 2})
	if err == nil || !strings.Contains(err.Error(), `service "api" is not a VM service`) {
		t.Fatalf("error = %v, want non-VM service", err)
	}
}

func TestVMSetRejectsRunningVM(t *testing.T) {
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", t.TempDir(), vmDiskBackendRaw)
	withServiceSetVMRunningCheck(t, func(*Server, string) (bool, error) { return true, nil })

	err := server.updateVMServiceSettings(context.Background(), "devbox", cli.VMSetFlags{CPUs: 2})
	if err == nil || !strings.Contains(err.Error(), `cannot change VM settings while "devbox" is running`) {
		t.Fatalf("error = %v, want running VM error", err)
	}
}

func TestVMSetUpdatesShapeAndFirecrackerConfig(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", root, vmDiskBackendRaw)
	withServiceSetVMRunningCheck(t, func(*Server, string) (bool, error) { return false, nil })

	if err := server.updateVMServiceSettings(context.Background(), "devbox", cli.VMSetFlags{CPUs: 6, Memory: "6g"}); err != nil {
		t.Fatalf("updateVMServiceSettings: %v", err)
	}

	svc := getTestService(t, server, "devbox")
	if svc.VM.CPUs != 6 || svc.VM.MemoryBytes != 6<<30 {
		t.Fatalf("vm shape = %d/%d, want 6/%d", svc.VM.CPUs, svc.VM.MemoryBytes, int64(6<<30))
	}
	assertFileContains(t, filepath.Join(serviceRunDirForRoot(root), "firecracker.json"), `"vcpu_count": 6`)
	assertFileContains(t, filepath.Join(serviceRunDirForRoot(root), "firecracker.json"), `"mem_size_mib": 6144`)
}

func TestVMCmdSetUpdatesShape(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", root, vmDiskBackendRaw)
	withServiceSetVMRunningCheck(t, func(*Server, string) (bool, error) { return false, nil })
	execer := &ttyExecer{s: server, sn: "devbox", rw: &bytes.Buffer{}}

	if err := execer.vmCmdFunc([]string{"set", "--cpus=6", "--memory=6g"}); err != nil {
		t.Fatalf("vm set: %v", err)
	}
	svc := getTestService(t, server, "devbox")
	if svc.VM.CPUs != 6 || svc.VM.MemoryBytes != 6<<30 {
		t.Fatalf("vm shape = %d/%d, want 6/%d", svc.VM.CPUs, svc.VM.MemoryBytes, int64(6<<30))
	}
}

func TestVMSetGrowsRawDiskAfterCommands(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", root, vmDiskBackendRaw)
	withServiceSetVMRunningCheck(t, func(*Server, string) (bool, error) { return false, nil })
	var commands [][]string
	withServiceSetVMDiskRunner(t, func(_ context.Context, command []string) error {
		commands = append(commands, append([]string(nil), command...))
		return nil
	})

	if err := server.updateVMServiceSettings(context.Background(), "devbox", cli.VMSetFlags{Disk: "32g"}); err != nil {
		t.Fatalf("updateVMServiceSettings: %v", err)
	}

	disk := filepath.Join(serviceDataDirForRoot(root), "rootfs.raw")
	want := [][]string{
		{"qemu-img", "resize", disk, "34359738368"},
		{"e2fsck", "-pf", disk},
		{"resize2fs", disk},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("disk commands = %#v, want %#v", commands, want)
	}
	svc := getTestService(t, server, "devbox")
	if svc.VM.Disk.Bytes != 32<<30 {
		t.Fatalf("disk bytes = %d, want %d", svc.VM.Disk.Bytes, int64(32<<30))
	}
}

func TestVMSetGrowsDiskWithoutMemoryAdmission(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", root, vmDiskBackendRaw)
	withServiceSetVMRunningCheck(t, func(*Server, string) (bool, error) { return false, nil })
	withServiceSetVMHostProfile(t, func(*Server, string, int64) (vmHostProfile, error) {
		t.Fatal("disk-only vm set should not inspect host memory admission")
		return vmHostProfile{}, nil
	})
	withServiceSetVMDiskRunner(t, func(context.Context, []string) error { return nil })

	if err := server.updateVMServiceSettings(context.Background(), "devbox", cli.VMSetFlags{Disk: "32g"}); err != nil {
		t.Fatalf("updateVMServiceSettings: %v", err)
	}
}

func TestVMSetRejectsDiskShrinkWithoutDBChange(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", root, vmDiskBackendRaw)
	withServiceSetVMRunningCheck(t, func(*Server, string) (bool, error) { return false, nil })

	err := server.updateVMServiceSettings(context.Background(), "devbox", cli.VMSetFlags{Disk: "8g"})
	if err == nil || !strings.Contains(err.Error(), "VM disk shrink is not supported") {
		t.Fatalf("error = %v, want shrink rejection", err)
	}
	svc := getTestService(t, server, "devbox")
	if svc.VM.Disk.Bytes != 16<<30 {
		t.Fatalf("disk bytes changed to %d, want %d", svc.VM.Disk.Bytes, int64(16<<30))
	}
}

func TestVMSetReplacesNetworkAndMetadata(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", root, vmDiskBackendRaw)
	withServiceSetVMRunningCheck(t, func(*Server, string) (bool, error) { return false, nil })
	var networkCommands [][]string
	withServiceSetVMNetworkRunner(t, func(command []string) error {
		networkCommands = append(networkCommands, append([]string(nil), command...))
		return nil
	})
	var injectedDisk string
	var injectedMetadata vmMetadataConfig
	withServiceSetVMMetadataInjector(t, func(_ context.Context, disk string, metadata vmMetadataConfig) error {
		injectedDisk = disk
		injectedMetadata = metadata
		return nil
	})

	if err := server.updateVMServiceSettings(context.Background(), "devbox", cli.VMSetFlags{
		Net:           "lan",
		NetworkChange: true,
		MacvlanParent: "vmbr0",
		MacvlanMac:    "02:fc:00:00:00:44",
	}); err != nil {
		t.Fatalf("updateVMServiceSettings: %v", err)
	}

	if !containsCommandPrefix(networkCommands, []string{"ip", "link", "del"}) {
		t.Fatalf("network cleanup command missing: %#v", networkCommands)
	}
	if !containsCommand(networkCommands, []string{"ip", "tuntap", "add", "yvm-d-ea1055-l0", "mode", "tap"}) {
		t.Fatalf("lan tap setup missing: %#v", networkCommands)
	}
	svc := getTestService(t, server, "devbox")
	if len(svc.VM.Networks) != 1 || svc.VM.Networks[0].Mode != "lan" || svc.VM.Networks[0].Parent != "vmbr0" {
		t.Fatalf("networks = %#v, want lan on vmbr0", svc.VM.Networks)
	}
	if injectedDisk != svc.VM.Disk.Path {
		t.Fatalf("injected disk = %q, want %q", injectedDisk, svc.VM.Disk.Path)
	}
	if len(injectedMetadata.Networks) != 1 || !injectedMetadata.Networks[0].DHCP {
		t.Fatalf("injected metadata networks = %#v, want DHCP lan", injectedMetadata.Networks)
	}
	assertFileContains(t, filepath.Join(serviceRunDirForRoot(root), "firecracker.json"), `"host_dev_name": "yvm-d-ea1055-l0"`)
}

func TestVMSetRejectsMacvlanFlagsWithoutLAN(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", root, vmDiskBackendRaw)
	withServiceSetVMRunningCheck(t, func(*Server, string) (bool, error) { return false, nil })

	err := server.updateVMServiceSettings(context.Background(), "devbox", cli.VMSetFlags{MacvlanParent: "vmbr0"})
	if err == nil || !strings.Contains(err.Error(), `--macvlan-* settings require VM LAN networking`) {
		t.Fatalf("error = %v, want macvlan LAN requirement", err)
	}
	svc := getTestService(t, server, "devbox")
	if len(svc.VM.Networks) != 1 || svc.VM.Networks[0].Mode != "svc" {
		t.Fatalf("networks changed to %#v, want original svc network", svc.VM.Networks)
	}
}

func TestVMSetPreservesNixOSUserAndSystemInit(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", root, vmDiskBackendRaw)
	if _, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, svc *db.Service) error {
		svc.VM.Image.Payload = testNixOSVMPayload
		svc.VM.Image.Version = testNixOSVMImageVersion
		svc.VM.SSH.User = "nixos"
		return nil
	}); err != nil {
		t.Fatalf("MutateService: %v", err)
	}
	firecrackerPath := filepath.Join(serviceRunDirForRoot(root), "firecracker.json")
	raw, err := os.ReadFile(firecrackerPath)
	if err != nil {
		t.Fatalf("read firecracker config: %v", err)
	}
	raw = []byte(strings.Replace(string(raw), "init=/usr/local/lib/yeet-vm/yeet-init", "init=/usr/local/lib/yeet-vm/yeet-init yeet.system_init=/run/current-system/init", 1))
	if err := os.WriteFile(firecrackerPath, raw, 0o644); err != nil {
		t.Fatalf("write firecracker config: %v", err)
	}
	withServiceSetVMRunningCheck(t, func(*Server, string) (bool, error) { return false, nil })
	withServiceSetVMNetworkRunner(t, func([]string) error { return nil })
	var injectedMetadata vmMetadataConfig
	withServiceSetVMMetadataInjector(t, func(_ context.Context, _ string, metadata vmMetadataConfig) error {
		injectedMetadata = metadata
		return nil
	})

	if err := server.updateVMServiceSettings(context.Background(), "devbox", cli.VMSetFlags{
		Net:           "lan",
		NetworkChange: true,
		MacvlanParent: "vmbr0",
	}); err != nil {
		t.Fatalf("updateVMServiceSettings: %v", err)
	}

	svc := getTestService(t, server, "devbox")
	if svc.VM.SSH.User != "nixos" {
		t.Fatalf("SSH user = %q, want nixos", svc.VM.SSH.User)
	}
	if injectedMetadata.User != "nixos" {
		t.Fatalf("metadata user = %q, want nixos", injectedMetadata.User)
	}
	if injectedMetadata.MetadataDriver != "nixos" {
		t.Fatalf("metadata driver = %q, want nixos", injectedMetadata.MetadataDriver)
	}
	assertFileContains(t, firecrackerPath, "yeet.system_init=/run/current-system/init")
}

func TestVMSetPreservesLocalNixOSImageMetadata(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", root, vmDiskBackendRaw)
	if _, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, svc *db.Service) error {
		svc.VM.Image.Payload = "vm://local/nixos"
		svc.VM.Image.Version = "custom-nixos-v1"
		svc.VM.Image.DefaultUser = "nixos"
		svc.VM.Image.GuestSystemInit = "/run/current-system/init"
		svc.VM.Image.MetadataDriver = "nixos"
		svc.VM.SSH.User = ""
		return nil
	}); err != nil {
		t.Fatalf("MutateService: %v", err)
	}
	firecrackerPath := filepath.Join(serviceRunDirForRoot(root), "firecracker.json")
	withServiceSetVMRunningCheck(t, func(*Server, string) (bool, error) { return false, nil })
	withServiceSetVMNetworkRunner(t, func([]string) error { return nil })
	var injectedMetadata vmMetadataConfig
	withServiceSetVMMetadataInjector(t, func(_ context.Context, _ string, metadata vmMetadataConfig) error {
		injectedMetadata = metadata
		return nil
	})

	if err := server.updateVMServiceSettings(context.Background(), "devbox", cli.VMSetFlags{
		Net:           "lan",
		NetworkChange: true,
		MacvlanParent: "vmbr0",
	}); err != nil {
		t.Fatalf("updateVMServiceSettings: %v", err)
	}

	if injectedMetadata.User != "nixos" {
		t.Fatalf("metadata user = %q, want nixos", injectedMetadata.User)
	}
	if injectedMetadata.MetadataDriver != "nixos" {
		t.Fatalf("metadata driver = %q, want nixos", injectedMetadata.MetadataDriver)
	}
	assertFileContains(t, firecrackerPath, "yeet.system_init=/run/current-system/init")
}

func seedVMForResize(t *testing.T, server *Server, name, root, backend string) {
	t.Helper()
	withServiceSetVMHostProfile(t, func(*Server, string, int64) (vmHostProfile, error) {
		return vmHostProfile{
			Arch:           "x86_64",
			HasKVM:         true,
			LogicalCPUs:    16,
			MemoryBytes:    64 << 30,
			StorageBytes:   512 << 30,
			StorageZFS:     backend == vmDiskBackendZVOL,
			RunningVMBytes: 0,
		}, nil
	})
	if err := os.MkdirAll(serviceDataDirForRoot(root), 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	if err := os.MkdirAll(serviceRunDirForRoot(root), 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	diskPath := filepath.Join(serviceDataDirForRoot(root), "rootfs.raw")
	diskDBPath := diskPath
	if backend == vmDiskBackendZVOL {
		diskDBPath = "/dev/zvol/flash/yeet/vms/devbox/vm/d-abc/root"
	}
	network := newVMNetworkPlan(name, []string{"svc"}, vmNetworkInputs{ServiceIP: "192.168.100.12"})
	firecrackerPath := filepath.Join(serviceRunDirForRoot(root), "firecracker.json")
	firecracker, err := renderFirecrackerConfig(firecrackerConfig{
		BootSource: firecrackerBootSource{
			KernelImagePath: "/srv/yeet/images/kernel",
			InitrdPath:      "/srv/yeet/images/initrd.img",
			BootArgs:        "console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda rw init=/usr/local/lib/yeet-vm/yeet-init ip=192.168.100.12::192.168.100.254:255.255.255.0:devbox:eth0:none yeet.hostname=devbox yeet.iface=eth0",
		},
		Drives: []firecrackerDrive{{
			DriveID:      "rootfs",
			PathOnHost:   diskDBPath,
			IsRootDevice: true,
			IsReadOnly:   false,
		}},
		NetworkInterfaces: network.FirecrackerInterfaces(),
		MachineConfig:     firecrackerMachineConfig{VCPUCount: 4, MemSizeMib: 4096},
	})
	if err != nil {
		t.Fatalf("render firecracker config: %v", err)
	}
	if err := os.WriteFile(firecrackerPath, firecracker, 0o644); err != nil {
		t.Fatalf("write firecracker config: %v", err)
	}
	metadataDir := filepath.Join(root, "metadata")
	if err := os.MkdirAll(metadataDir, 0o755); err != nil {
		t.Fatalf("mkdir metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(metadataDir, "authorized_keys"), []byte("ssh-ed25519 AAAATEST user@example\n"), 0o600); err != nil {
		t.Fatalf("write authorized keys: %v", err)
	}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		name: {
			Name:        name,
			ServiceType: db.ServiceTypeVM,
			ServiceRoot: root,
			SvcNetwork:  &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.12")},
			VM: &db.VMConfig{
				Runtime: vmRuntimeFirecracker,
				Image: db.VMImageConfig{
					Payload: testUbuntuVMPayload,
					Version: testUbuntuVMImageVersion,
					Kernel:  "/srv/yeet/images/kernel",
					RootFS:  "/srv/yeet/images/rootfs.ext4",
				},
				CPUs:        4,
				MemoryBytes: 4 << 30,
				Disk:        db.VMDiskConfig{Backend: backend, Bytes: 16 << 30, Path: diskDBPath},
				Networks:    network.DBNetworks(),
				SSH:         db.VMSSHConfig{User: "ubuntu"},
				Console:     db.VMConsoleConfig{SocketPath: filepath.Join(serviceRunDirForRoot(root), "serial.sock"), LogPath: filepath.Join(serviceRunDirForRoot(root), "serial.log")},
				Sockets:     db.VMSocketConfig{APISocketPath: filepath.Join(serviceRunDirForRoot(root), "firecracker.sock")},
				PIDFile:     filepath.Join(serviceRunDirForRoot(root), "firecracker.pid"),
				SetupState:  "ready",
			},
		},
	}}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
}

func withServiceSetVMHostProfile(t *testing.T, fn func(*Server, string, int64) (vmHostProfile, error)) {
	t.Helper()
	old := vmServiceSetHostProfileFunc
	vmServiceSetHostProfileFunc = fn
	t.Cleanup(func() { vmServiceSetHostProfileFunc = old })
}

func withServiceSetVMRunningCheck(t *testing.T, fn func(*Server, string) (bool, error)) {
	t.Helper()
	old := isServiceRunningForVMSettings
	isServiceRunningForVMSettings = fn
	t.Cleanup(func() { isServiceRunningForVMSettings = old })
}

func withServiceSetVMDiskRunner(t *testing.T, fn vmCommandRunner) {
	t.Helper()
	old := vmServiceSetDiskRunner
	vmServiceSetDiskRunner = fn
	t.Cleanup(func() { vmServiceSetDiskRunner = old })
}

func withServiceSetVMNetworkRunner(t *testing.T, fn vmNetworkCommandRunner) {
	t.Helper()
	old := vmServiceSetNetworkRunner
	vmServiceSetNetworkRunner = fn
	t.Cleanup(func() { vmServiceSetNetworkRunner = old })
}

func withServiceSetVMMetadataInjector(t *testing.T, fn func(context.Context, string, vmMetadataConfig) error) {
	t.Helper()
	old := vmServiceSetMetadataInjector
	vmServiceSetMetadataInjector = fn
	t.Cleanup(func() { vmServiceSetMetadataInjector = old })
}

func containsCommand(commands [][]string, want []string) bool {
	for _, command := range commands {
		if reflect.DeepEqual(command, want) {
			return true
		}
	}
	return false
}

func containsCommandPrefix(commands [][]string, prefix []string) bool {
	for _, command := range commands {
		if len(command) < len(prefix) {
			continue
		}
		if reflect.DeepEqual(command[:len(prefix)], prefix) {
			return true
		}
	}
	return false
}
