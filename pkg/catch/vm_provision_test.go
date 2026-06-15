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
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
)

func TestRunVMStagesDBAfterArtifacts(t *testing.T) {
	server := newTestServer(t)
	execer, serviceRoot, _, _ := newVMProvisionTestExecer(t, server, "svc")
	vmImageEnsureFunc = func(context.Context, vmImageCache, string, ProgressUI) (vmImageAsset, error) {
		return vmImageAsset{}, fmt.Errorf("image manifest missing kernel")
	}

	err := execer.runVM(cli.RunFlags{Net: "svc", CPUs: 2, Memory: "2g", Disk: "16g", Restart: false}, vmUbuntu2604Payload)
	if err == nil || !strings.Contains(err.Error(), "image manifest") {
		t.Fatalf("runVM error = %v, want image manifest failure", err)
	}
	assertNoReadyVM(t, server, "svc")
	if _, statErr := os.Stat(serviceRoot); !os.IsNotExist(statErr) {
		t.Fatalf("service root stat after failed VM inputs = %v, want not exists", statErr)
	}
}

func TestRunVMDoesNotCommitReadyOnArtifactFailure(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "svc")
	vmProvisionDiskRunner = func(context.Context, []string) error {
		return errors.New("disk failed")
	}

	err := execer.runVM(cli.RunFlags{Net: "svc", CPUs: 2, Memory: "2g", Disk: "16g"}, vmUbuntu2604Payload)
	if err == nil || !strings.Contains(err.Error(), "disk failed") {
		t.Fatalf("runVM error = %v, want disk failure", err)
	}
	assertNoReadyVM(t, server, "svc")
}

func TestRunVMRemovesNewServiceRootOnArtifactFailure(t *testing.T) {
	server := newTestServer(t)
	execer, serviceRoot, _, _ := newVMProvisionTestExecer(t, server, "svc")
	vmProvisionDiskRunner = func(context.Context, []string) error {
		if err := os.WriteFile(filepath.Join(serviceDataDirForRoot(serviceRoot), "partial-rootfs.raw"), []byte("partial"), 0o644); err != nil {
			t.Fatalf("write partial disk: %v", err)
		}
		return errors.New("disk failed")
	}

	err := execer.runVM(cli.RunFlags{Net: "svc", CPUs: 2, Memory: "2g", Disk: "16g"}, vmUbuntu2604Payload)
	if err == nil || !strings.Contains(err.Error(), "disk failed") {
		t.Fatalf("runVM error = %v, want disk failure", err)
	}
	assertNoReadyVM(t, server, "svc")
	if _, statErr := os.Stat(serviceRoot); !os.IsNotExist(statErr) {
		t.Fatalf("service root stat after failed new VM = %v, want not exists", statErr)
	}
}

func TestRunVMKeepsExistingServiceRootOnArtifactFailure(t *testing.T) {
	server := newTestServer(t)
	execer, serviceRoot, _, _ := newVMProvisionTestExecer(t, server, "svc")
	if err := os.MkdirAll(serviceDataDirForRoot(serviceRoot), 0o755); err != nil {
		t.Fatalf("mkdir service data: %v", err)
	}
	marker := filepath.Join(serviceDataDirForRoot(serviceRoot), "existing")
	if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	addTestServices(t, server, db.Service{
		Name:        "svc",
		ServiceType: db.ServiceTypeVM,
		VM:          &db.VMConfig{SetupState: "ready"},
	})
	vmProvisionDiskRunner = func(context.Context, []string) error {
		return errors.New("disk failed")
	}

	err := execer.runVM(cli.RunFlags{Net: "svc", CPUs: 2, Memory: "2g", Disk: "16g"}, vmUbuntu2604Payload)
	if err == nil || !strings.Contains(err.Error(), "disk failed") {
		t.Fatalf("runVM error = %v, want disk failure", err)
	}
	if got, readErr := os.ReadFile(marker); readErr != nil || string(got) != "keep" {
		t.Fatalf("existing marker after failed VM update = %q, %v; want preserved", got, readErr)
	}
}

func TestRunVMRejectsInvalidNetworkBeforeImageSelection(t *testing.T) {
	server := newTestServer(t)
	execer, serviceRoot, _, _ := newVMProvisionTestExecer(t, server, "svc")

	var profiled bool
	vmProvisionHostProfileFunc = func(_ *ttyExecer, _ resolvedServiceRoot, _ int64) (vmHostProfile, error) {
		profiled = true
		return vmHostProfile{
			Arch:         "x86_64",
			HasKVM:       true,
			LogicalCPUs:  8,
			MemoryBytes:  16 << 30,
			StorageBytes: 128 << 30,
		}, nil
	}
	var inspected bool
	vmImageInspectFunc = func(context.Context, vmImageCache, string) (vmImageCacheState, vmImageManifest, error) {
		inspected = true
		return vmImageCacheState{
			Payload:       vmUbuntu2604Payload,
			LatestVersion: defaultVMImageVersion,
			State:         vmImageCacheMissing,
		}, vmImageManifest{Version: defaultVMImageVersion}, nil
	}
	var ensured bool
	vmImageEnsureFunc = func(context.Context, vmImageCache, string, ProgressUI) (vmImageAsset, error) {
		ensured = true
		return fakeVMImageAsset(t)
	}

	err := execer.runVM(cli.RunFlags{Net: "ts"}, vmUbuntu2604Payload)
	if err == nil || !strings.Contains(err.Error(), `unsupported VM network mode "ts"`) {
		t.Fatalf("runVM error = %v, want unsupported network mode", err)
	}
	if profiled || inspected || ensured {
		t.Fatalf("invalid network performed work: profiled=%v inspected=%v ensured=%v", profiled, inspected, ensured)
	}
	if _, statErr := os.Stat(serviceRoot); !os.IsNotExist(statErr) {
		t.Fatalf("service root stat after invalid network = %v, want not exists", statErr)
	}
	assertNoReadyVM(t, server, "svc")
}

func TestRunVMRejectsMacvlanFlagsWithoutLANBeforeImageSelection(t *testing.T) {
	server := newTestServer(t)
	execer, serviceRoot, _, _ := newVMProvisionTestExecer(t, server, "svc")

	var profiled bool
	vmProvisionHostProfileFunc = func(_ *ttyExecer, _ resolvedServiceRoot, _ int64) (vmHostProfile, error) {
		profiled = true
		return vmHostProfile{
			Arch:         "x86_64",
			HasKVM:       true,
			LogicalCPUs:  8,
			MemoryBytes:  16 << 30,
			StorageBytes: 128 << 30,
		}, nil
	}
	var inspected bool
	vmImageInspectFunc = func(context.Context, vmImageCache, string) (vmImageCacheState, vmImageManifest, error) {
		inspected = true
		return vmImageCacheState{
			Payload:       vmUbuntu2604Payload,
			LatestVersion: defaultVMImageVersion,
			State:         vmImageCacheMissing,
		}, vmImageManifest{Version: defaultVMImageVersion}, nil
	}
	var ensured bool
	vmImageEnsureFunc = func(context.Context, vmImageCache, string, ProgressUI) (vmImageAsset, error) {
		ensured = true
		return fakeVMImageAsset(t)
	}

	err := execer.runVM(cli.RunFlags{MacvlanParent: "vmbr0"}, vmUbuntu2604Payload)
	if err == nil || !strings.Contains(err.Error(), `--macvlan-* settings require VM LAN networking`) {
		t.Fatalf("runVM error = %v, want macvlan LAN requirement", err)
	}
	if profiled || inspected || ensured {
		t.Fatalf("invalid macvlan flags performed work: profiled=%v inspected=%v ensured=%v", profiled, inspected, ensured)
	}
	if _, statErr := os.Stat(serviceRoot); !os.IsNotExist(statErr) {
		t.Fatalf("service root stat after invalid macvlan flags = %v, want not exists", statErr)
	}
	assertNoReadyVM(t, server, "svc")
}

func TestValidateVMNetworkOptionsRejectsDuplicateModesAndInvalidVLANs(t *testing.T) {
	tests := []struct {
		name    string
		modes   []string
		vlan    int
		wantErr string
	}{
		{
			name:    "duplicate svc",
			modes:   []string{"svc,svc"},
			wantErr: `duplicate VM network mode "svc"`,
		},
		{
			name:    "duplicate lan",
			modes:   []string{"svc,lan,lan"},
			wantErr: `duplicate VM network mode "lan"`,
		},
		{
			name:    "empty mode list",
			modes:   []string{","},
			wantErr: "VM network mode must not be empty",
		},
		{
			name:    "trailing empty mode",
			modes:   []string{"svc,"},
			wantErr: "VM network mode must not be empty",
		},
		{
			name:    "negative vlan",
			modes:   []string{"lan"},
			vlan:    -1,
			wantErr: "--macvlan-vlan must be between 1 and 4094",
		},
		{
			name:    "too large vlan",
			modes:   []string{"lan"},
			vlan:    4095,
			wantErr: "--macvlan-vlan must be between 1 and 4094",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateVMNetworkOptions(tt.modes, "", tt.vlan, "")
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateVMNetworkOptions error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestRunVMProvisionSuccessWritesArtifactsAndDB(t *testing.T) {
	server := newTestServer(t)
	execer, serviceRoot, systemdDir, systemctlCalls := newVMProvisionTestExecer(t, server, "svc")
	fastImageVersion := "ubuntu-26.04-amd64-v8"
	vmImageEnsureFunc = func(context.Context, vmImageCache, string, ProgressUI) (vmImageAsset, error) {
		asset, err := fakeVMImageAssetVersion(t, fastImageVersion)
		if err != nil {
			return vmImageAsset{}, err
		}
		asset.Manifest.GuestInit = vmGuestInitPath
		return asset, nil
	}

	var diskCommands [][]string
	vmProvisionDiskRunner = func(_ context.Context, cmd []string) error {
		diskCommands = append(diskCommands, append([]string(nil), cmd...))
		return nil
	}
	var networkCommands [][]string
	vmProvisionNetworkRunner = func(cmd []string) error {
		networkCommands = append(networkCommands, append([]string(nil), cmd...))
		return nil
	}
	var injectedDisk string
	var injectedMetadata vmMetadataConfig
	vmProvisionMetadataInjector = func(_ context.Context, disk string, cfg vmMetadataConfig) error {
		injectedDisk = disk
		injectedMetadata = cfg
		return nil
	}

	if err := execer.runVM(cli.RunFlags{Net: "svc", CPUs: 2, Memory: "2g", Disk: "16g", Restart: false}, vmUbuntu2604Payload); err != nil {
		t.Fatalf("runVM: %v", err)
	}

	svc := getTestService(t, server, "svc")
	if svc.ServiceType != db.ServiceTypeVM {
		t.Fatalf("ServiceType = %q, want vm", svc.ServiceType)
	}
	if svc.VM == nil {
		t.Fatal("VM config is nil")
	}
	vm := svc.VM
	if vm.SetupState != "ready" {
		t.Fatalf("SetupState = %q, want ready", vm.SetupState)
	}
	if vm.Runtime != vmRuntimeFirecracker || vm.Image.Payload != vmUbuntu2604Payload || vm.Image.Version != fastImageVersion {
		t.Fatalf("VM image/runtime = %#v", vm)
	}
	if vm.CPUs != 2 || vm.MemoryBytes != 2<<30 || vm.Disk.Bytes != 16<<30 || vm.Disk.Backend != vmDiskBackendRaw {
		t.Fatalf("VM shape = CPUs %d memory %d disk %#v", vm.CPUs, vm.MemoryBytes, vm.Disk)
	}
	if vm.Disk.Path == "" || vm.Console.SocketPath == "" || vm.Console.LogPath == "" || vm.Sockets.APISocketPath == "" || vm.PIDFile == "" {
		t.Fatalf("VM paths not populated: %#v", vm)
	}
	if len(vm.Networks) != 1 || vm.Networks[0].Mode != "svc" || vm.Networks[0].Tap == "" || !vm.Networks[0].IP.IsValid() {
		t.Fatalf("VM networks = %#v", vm.Networks)
	}
	if svc.SvcNetwork == nil || !svc.SvcNetwork.IPv4.IsValid() {
		t.Fatalf("SvcNetwork = %#v, want assigned service IP", svc.SvcNetwork)
	}
	if vm.SSH.User != "ubuntu" {
		t.Fatalf("SSH user = %q, want ubuntu", vm.SSH.User)
	}
	if len(diskCommands) == 0 {
		t.Fatal("disk runner was not called")
	}
	if len(networkCommands) == 0 {
		t.Fatal("network runner was not called")
	}
	if injectedDisk != vm.Disk.Path {
		t.Fatalf("metadata injected into %q, want VM disk %q", injectedDisk, vm.Disk.Path)
	}
	if injectedMetadata.SSHKey != "ssh-ed25519 AAAATEST user@example" {
		t.Fatalf("metadata SSH key = %q", injectedMetadata.SSHKey)
	}
	if !injectedMetadata.FastBoot {
		t.Fatal("metadata FastBoot = false, want true for guest_init image")
	}

	assertFileContains(t, filepath.Join(serviceRunDirForRoot(serviceRoot), "firecracker.json"), `"kernel_image_path"`)
	assertFileContains(t, filepath.Join(serviceRunDirForRoot(serviceRoot), "firecracker.json"), vm.Disk.Path)
	assertFileContains(t, filepath.Join(serviceRunDirForRoot(serviceRoot), "firecracker.json"), "init=/usr/local/lib/yeet-vm/yeet-init")
	assertFileContains(t, filepath.Join(serviceRunDirForRoot(serviceRoot), "firecracker.json"), "ip=192.168.100.")
	assertFileContains(t, filepath.Join(serviceRunDirForRoot(serviceRoot), "firecracker.json"), "yeet.hostname=svc")
	assertFileContains(t, filepath.Join(serviceRoot, "metadata", "hostname"), "svc")
	assertFileContains(t, filepath.Join(serviceBinDirForRoot(serviceRoot), vmSystemdUnitName("svc")), "ExecStart=")
	assertFileContains(t, filepath.Join(systemdDir, vmSystemdUnitName("svc")), "--api-sock")

	wantSystemctl := [][]string{
		{"daemon-reload"},
		{"enable", vmSystemdUnitName("svc")},
	}
	if !reflect.DeepEqual(*systemctlCalls, wantSystemctl) {
		t.Fatalf("systemctl calls = %#v, want %#v", *systemctlCalls, wantSystemctl)
	}
}

func TestRunVMConfiguresVsockRuntimeMetadata(t *testing.T) {
	server := newTestServer(t)
	execer, serviceRoot, _, _ := newVMProvisionTestExecer(t, server, "devbox")

	if err := execer.runVM(cli.RunFlags{Net: "svc", Restart: false}, vmUbuntu2604Payload); err != nil {
		t.Fatalf("runVM: %v", err)
	}

	svc := getTestService(t, server, "devbox")
	if svc.VM == nil {
		t.Fatal("VM missing after run")
	}
	if svc.VM.Sockets.VsockSocketPath == "" {
		t.Fatalf("vsock socket path is empty: %#v", svc.VM.Sockets)
	}
	if !strings.HasSuffix(svc.VM.Sockets.VsockSocketPath, "/run/vsock.sock") {
		t.Fatalf("vsock socket path = %q, want run/vsock.sock suffix", svc.VM.Sockets.VsockSocketPath)
	}
	if svc.VM.Sockets.VsockGuestCID != vmAgentGuestCID {
		t.Fatalf("vsock guest CID = %d, want %d", svc.VM.Sockets.VsockGuestCID, vmAgentGuestCID)
	}

	raw, err := os.ReadFile(filepath.Join(serviceRunDirForRoot(serviceRoot), "firecracker.json"))
	if err != nil {
		t.Fatalf("read firecracker config: %v", err)
	}
	for _, want := range []string{
		`"vsock"`,
		`"vsock_id": "yeet-agent"`,
		`"guest_cid": 3`,
		`"uds_path": "` + svc.VM.Sockets.VsockSocketPath + `"`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("firecracker config missing %q:\n%s", want, string(raw))
		}
	}
	assertFileContains(t, filepath.Join(serviceBinDirForRoot(serviceRoot), vmSystemdUnitName("devbox")), svc.VM.Sockets.VsockSocketPath)
	assertFileContains(t, filepath.Join(serviceBinDirForRoot(serviceRoot), vmSystemdUnitName("devbox")), "ExecStartPre=/bin/rm -f")
}

func TestRunVMPersistsSnapshotPolicyFlags(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "svc")

	if err := execer.runVM(cli.RunFlags{
		Net:              "svc",
		Restart:          false,
		Snapshots:        "on",
		SnapshotKeepLast: "3",
		SnapshotMaxAge:   "72h",
		SnapshotChange:   true,
	}, vmUbuntu2604Payload); err != nil {
		t.Fatalf("runVM: %v", err)
	}

	policy := getTestService(t, server, "svc").SnapshotPolicy
	if policy == nil {
		t.Fatal("SnapshotPolicy is nil, want valid policy")
	}
	if policy.Enabled == nil || !*policy.Enabled {
		t.Fatalf("SnapshotPolicy.Enabled = %v, want true", policy.Enabled)
	}
	if policy.KeepLast == nil || *policy.KeepLast != 3 {
		t.Fatalf("SnapshotPolicy.KeepLast = %v, want 3", policy.KeepLast)
	}
	if policy.MaxAge != "72h" {
		t.Fatalf("SnapshotPolicy.MaxAge = %q, want 72h", policy.MaxAge)
	}
}

func TestRunVMProvisionUsesManifestDefaultUser(t *testing.T) {
	server := newTestServer(t)
	execer, serviceRoot, _, _ := newVMProvisionTestExecer(t, server, "svc")
	vmImageEnsureFunc = func(context.Context, vmImageCache, string, ProgressUI) (vmImageAsset, error) {
		asset, err := fakeVMImageAssetVersion(t, "nixos-26.05-amd64-v1")
		if err != nil {
			return vmImageAsset{}, err
		}
		asset.Manifest.Name = "yeet-nixos-26.05"
		asset.Manifest.DefaultUser = "nixos"
		asset.Manifest.GuestInit = vmGuestInitPath
		asset.Manifest.GuestSystemInit = "/run/current-system/init"
		return asset, nil
	}

	var injectedMetadata vmMetadataConfig
	vmProvisionMetadataInjector = func(_ context.Context, _ string, cfg vmMetadataConfig) error {
		injectedMetadata = cfg
		return nil
	}

	if err := execer.runVM(cli.RunFlags{Net: "svc", Restart: false}, vmNixOS2605Payload); err != nil {
		t.Fatalf("runVM: %v", err)
	}

	vm := getTestService(t, server, "svc").VM
	if vm.SSH.User != "nixos" {
		t.Fatalf("SSH user = %q, want nixos", vm.SSH.User)
	}
	if injectedMetadata.User != "nixos" {
		t.Fatalf("metadata user = %q, want nixos", injectedMetadata.User)
	}
	if injectedMetadata.MetadataDriver != "nixos" {
		t.Fatalf("metadata driver = %q, want nixos", injectedMetadata.MetadataDriver)
	}
	assertFileContains(t, filepath.Join(serviceRunDirForRoot(serviceRoot), "firecracker.json"), "yeet.system_init=/run/current-system/init")
}

func TestRunVMProvisionUsesLegacyBootAndMetadataWithoutGuestInit(t *testing.T) {
	server := newTestServer(t)
	execer, serviceRoot, _, _ := newVMProvisionTestExecer(t, server, "svc")

	var injectedMetadata vmMetadataConfig
	vmProvisionMetadataInjector = func(_ context.Context, _ string, cfg vmMetadataConfig) error {
		injectedMetadata = cfg
		return nil
	}

	if err := execer.runVM(cli.RunFlags{Net: "svc", CPUs: 2, Memory: "2g", Disk: "16g", Restart: false}, vmUbuntu2604Payload); err != nil {
		t.Fatalf("runVM: %v", err)
	}

	firecrackerConfig := filepath.Join(serviceRunDirForRoot(serviceRoot), "firecracker.json")
	assertFileContains(t, firecrackerConfig, "console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda rw")
	assertFileNotContains(t, firecrackerConfig, "init=/usr/local/lib/yeet-vm/yeet-init")
	assertFileNotContains(t, firecrackerConfig, "ip=192.168.100.")
	assertFileNotContains(t, firecrackerConfig, "yeet.hostname=svc")
	assertFileNotContains(t, firecrackerConfig, "yeet.iface=eth0")
	if injectedMetadata.FastBoot {
		t.Fatal("metadata FastBoot = true, want false for image without guest_init")
	}
}

func TestRunVMProvisionIncludesInitrdPathWhenImageHasInitrd(t *testing.T) {
	server := newTestServer(t)
	execer, serviceRoot, _, _ := newVMProvisionTestExecer(t, server, "svc")
	vmImageEnsureFunc = func(context.Context, vmImageCache, string, ProgressUI) (vmImageAsset, error) {
		asset, err := fakeVMImageAsset(t)
		if err != nil {
			return vmImageAsset{}, err
		}
		asset.Paths.InitrdPath = filepath.Join(asset.Paths.Dir, "initrd.img")
		asset.Manifest.Initrd = "initrd.img"
		return asset, nil
	}

	if err := execer.runVM(cli.RunFlags{Net: "svc", Restart: false}, vmUbuntu2604Payload); err != nil {
		t.Fatalf("runVM: %v", err)
	}

	assertFileContains(t, filepath.Join(serviceRunDirForRoot(serviceRoot), "firecracker.json"), `"initrd_path": "`)
	assertFileContains(t, filepath.Join(serviceRunDirForRoot(serviceRoot), "firecracker.json"), "initrd.img")
}

func TestRunVMPrintsProgressAndNextCommands(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "devbox")
	var out bytes.Buffer
	execer.rw = &out

	if err := execer.runVM(cli.RunFlags{Net: "svc", CPUs: 2, Memory: "2g", Disk: "16g", Restart: true}, vmUbuntu2604Payload); err != nil {
		t.Fatalf("runVM: %v", err)
	}

	text := out.String()
	for _, want := range []string{
		"VM devbox",
		"Image: vm://ubuntu/26.04",
		"Shape: 2 vCPU, 2.0 GB memory, 16.0 GB disk",
		"Network: svc",
		"Preparing disk",
		"Injecting guest metadata",
		"Starting VM",
		"SSH: yeet ssh devbox",
		"Console: yeet vm console devbox",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s", want, text)
		}
	}
}

func TestRunVMUsesExecRequestSSHKey(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "svc")
	execer.vmSSHAuthorizedKey = "ssh-ed25519 AAAALOCAL local@example"

	var injectedMetadata vmMetadataConfig
	vmProvisionMetadataInjector = func(_ context.Context, _ string, cfg vmMetadataConfig) error {
		injectedMetadata = cfg
		return nil
	}

	if err := execer.runVM(cli.RunFlags{Net: "svc", CPUs: 2, Memory: "2g", Disk: "16g", Restart: false}, vmUbuntu2604Payload); err != nil {
		t.Fatalf("runVM: %v", err)
	}
	if injectedMetadata.SSHKey != "ssh-ed25519 AAAALOCAL local@example" {
		t.Fatalf("metadata SSH key = %q, want exec request key", injectedMetadata.SSHKey)
	}
}

func TestVMZVOLBaseDatasetUsesServiceRootPool(t *testing.T) {
	root := resolvedServiceRoot{
		Root:    "/flash/yeet/vms/devbox",
		Dataset: "flash/yeet/vms/devbox",
		ZFS:     true,
	}

	got := vmZVOLBaseDataset(root, "ubuntu-26.04-amd64-v1")
	want := "flash/yeet/vm-images/ubuntu-26.04-amd64-v1/root"
	if got != want {
		t.Fatalf("vmZVOLBaseDataset = %q, want %q", got, want)
	}
}

func TestVMZVOLBaseDatasetUsesTargetPoolForDifferentServiceRoot(t *testing.T) {
	root := resolvedServiceRoot{
		Root:    "/tank/apps/devbox",
		Dataset: "tank/apps/devbox",
		ZFS:     true,
	}

	got := vmZVOLBaseDataset(root, "ubuntu-26.04-amd64-v3")
	want := "tank/yeet/vm-images/ubuntu-26.04-amd64-v3/root"
	if got != want {
		t.Fatalf("vmZVOLBaseDataset = %q, want %q", got, want)
	}
}

func TestCleanupFailedNewVMProvisionRootKeepsExistingZFSDatasetContents(t *testing.T) {
	server := newTestServer(t)
	execer := &ttyExecer{s: server}
	root := t.TempDir()
	existingFile := filepath.Join(root, "preexisting")
	if err := os.WriteFile(existingFile, []byte("owned by caller"), 0o644); err != nil {
		t.Fatalf("write existing file: %v", err)
	}
	var destroyCalled bool
	server.zfsRunner = func(context.Context, ...string) (string, string, error) {
		destroyCalled = true
		return "", "destroy should not be called for existing datasets", errZFSCommandFailed
	}

	err := execer.cleanupFailedNewVMProvisionRoot(resolvedServiceRoot{
		Root:    root,
		Dataset: "tank/apps/svc",
		ZFS:     true,
		Created: false,
	})
	if err != nil {
		t.Fatalf("cleanupFailedNewVMProvisionRoot: %v", err)
	}
	if destroyCalled {
		t.Fatal("cleanup destroyed an existing ZFS service root")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("existing ZFS root stat: %v", err)
	}
	if _, err := os.Stat(existingFile); err != nil {
		t.Fatalf("existing ZFS content stat: %v", err)
	}
}

func TestCleanupFailedNewVMProvisionRootDestroysCreatedZFSDataset(t *testing.T) {
	server := newTestServer(t)
	execer := &ttyExecer{s: server}
	var got []string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		got = append([]string(nil), args...)
		return "", "", nil
	}

	err := execer.cleanupFailedNewVMProvisionRoot(resolvedServiceRoot{
		Root:    t.TempDir(),
		Dataset: "tank/apps/svc",
		ZFS:     true,
		Created: true,
	})
	if err != nil {
		t.Fatalf("cleanupFailedNewVMProvisionRoot: %v", err)
	}
	want := []string{"destroy", "-R", "tank/apps/svc"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("zfs command = %#v, want %#v", got, want)
	}
}

func TestVMZVOLBaseDatasetFallbackForMissingDataset(t *testing.T) {
	root := resolvedServiceRoot{Root: "/srv/yeet/services/devbox"}

	got := vmZVOLBaseDataset(root, "ubuntu-26.04-amd64-v1")
	want := "yeet/vm-images/ubuntu-26.04-amd64-v1/root"
	if got != want {
		t.Fatalf("vmZVOLBaseDataset = %q, want %q", got, want)
	}
}

func TestRunVMZVOLProvisionUsesDevicePathForFirecracker(t *testing.T) {
	server := newTestServer(t)
	execer, serviceRoot, _, _ := newVMProvisionTestExecer(t, server, "svc")
	serviceDataset := "flash/yeet/services/svc"
	if err := os.MkdirAll(serviceRoot, 0o755); err != nil {
		t.Fatalf("mkdir service root: %v", err)
	}
	server.zfsRunner = fakeZFSRunner(map[string]fakeZFSDataset{
		serviceDataset: {Mountpoint: serviceRoot, Exists: true},
	}).Run
	var diskCommands [][]string
	vmProvisionDiskRunner = func(_ context.Context, cmd []string) error {
		diskCommands = append(diskCommands, append([]string(nil), cmd...))
		if len(cmd) >= 2 && cmd[0] == "zfs" && cmd[1] == "list" {
			if strings.Contains(strings.Join(cmd, " "), "@") {
				return errors.New("snapshot missing")
			}
			return errors.New("base missing")
		}
		return nil
	}

	if err := execer.runVM(cli.RunFlags{Net: "svc", ZFS: true, ServiceRoot: serviceDataset}, vmUbuntu2604Payload); err != nil {
		t.Fatalf("runVM: %v", err)
	}
	svc := getTestService(t, server, "svc")
	if svc.VM == nil || svc.VM.Disk.Backend != vmDiskBackendZVOL {
		t.Fatalf("VM disk = %#v", svc.VM)
	}
	wantDataset := serviceDataset + "/root"
	wantDevice := "/dev/zvol/" + wantDataset
	wantBase := "flash/yeet/vm-images/" + defaultVMImageVersion + "/root"
	wantSnapshot := wantBase + "@" + defaultVMImageVersion
	if strings.HasPrefix(wantBase, serviceDataset+"/") {
		t.Fatalf("shared base %q must not be under service root dataset %q", wantBase, serviceDataset)
	}
	if svc.VM.Disk.Path != wantDevice {
		t.Fatalf("db disk path = %q, want %q", svc.VM.Disk.Path, wantDevice)
	}
	assertFileContains(t, filepath.Join(serviceRunDirForRoot(serviceRoot), "firecracker.json"), `"path_on_host": "`+wantDevice+`"`)
	foundClone := false
	for _, command := range diskCommands {
		if reflect.DeepEqual(command, []string{"zfs", "clone", "-o", "volsize=68719476736", wantSnapshot, wantDataset}) {
			foundClone = true
		}
		if len(command) >= 3 && strings.Contains(strings.Join(command, " "), serviceDataset+"/base/") {
			t.Fatalf("disk command used legacy per-service base: %#v", command)
		}
	}
	if !foundClone {
		t.Fatalf("clone command from %q to %q not found in %#v", wantSnapshot, wantDataset, diskCommands)
	}
}

func TestRunVMZVOLProvisionPrintsDiskSubsteps(t *testing.T) {
	server := newTestServer(t)
	execer, serviceRoot, _, _ := newVMProvisionTestExecer(t, server, "devbox")
	var out bytes.Buffer
	execer.rw = &out
	serviceDataset := "flash/yeet/vms/devbox"
	if err := os.MkdirAll(serviceRoot, 0o755); err != nil {
		t.Fatalf("mkdir service root: %v", err)
	}
	server.zfsRunner = fakeZFSRunner(map[string]fakeZFSDataset{
		serviceDataset: {Mountpoint: serviceRoot, Exists: true},
	}).Run
	var diskCommands [][]string
	vmProvisionDiskRunner = func(_ context.Context, cmd []string) error {
		diskCommands = append(diskCommands, append([]string(nil), cmd...))
		if len(cmd) >= 2 && cmd[0] == "zfs" && cmd[1] == "list" {
			if strings.Contains(strings.Join(cmd, " "), "@") {
				return errors.New("snapshot missing")
			}
			return errors.New("base missing")
		}
		return nil
	}

	if err := execer.runVM(cli.RunFlags{Net: "svc", ZFS: true, ServiceRoot: serviceDataset}, vmUbuntu2604Payload); err != nil {
		t.Fatalf("runVM: %v", err)
	}

	text := out.String()
	for _, want := range []string{
		"Preparing ZFS image base",
		"Writing image to ZFS base",
		"Cloning VM disk",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s\ndisk commands: %#v", want, text, diskCommands)
		}
	}
}

func TestRunVMUsesPreparedRootFSForDiskProvisioning(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "svc")

	var diskCommands [][]string
	vmProvisionDiskRunner = func(_ context.Context, cmd []string) error {
		diskCommands = append(diskCommands, append([]string(nil), cmd...))
		return nil
	}

	if err := execer.runVM(cli.RunFlags{Net: "svc", Disk: "16g"}, vmUbuntu2604Payload); err != nil {
		t.Fatalf("runVM: %v", err)
	}
	if len(diskCommands) < 2 {
		t.Fatalf("disk commands = %#v", diskCommands)
	}
	if got := diskCommands[1][len(diskCommands[1])-2]; strings.HasSuffix(got, ".zst") || filepath.Base(got) != "rootfs.ext4" {
		t.Fatalf("disk base rootfs = %q, want prepared rootfs.ext4", got)
	}
}

func TestRunVMRestartFlagControlsSystemctlRestart(t *testing.T) {
	tests := []struct {
		name        string
		restart     bool
		wantRestart bool
	}{
		{name: "restart false", restart: false},
		{name: "restart true", restart: true, wantRestart: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(t)
			execer, _, _, systemctlCalls := newVMProvisionTestExecer(t, server, "svc")

			if err := execer.runVM(cli.RunFlags{Net: "svc", Restart: tt.restart}, vmUbuntu2604Payload); err != nil {
				t.Fatalf("runVM: %v", err)
			}
			if got := vmTestSystemctlCalled(*systemctlCalls, "restart", vmSystemdUnitName("svc")); got != tt.wantRestart {
				t.Fatalf("restart called = %v, want %v; calls %#v", got, tt.wantRestart, *systemctlCalls)
			}
		})
	}
}

func TestRunVMWaitsForGuestReadinessBeforeNextCommands(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "devbox")
	var out bytes.Buffer
	execer.rw = &out
	var captured bool
	var waited bool
	vmProvisionGuestReadyBoundaryFunc = func(ctx context.Context, service string) (vmGuestReadyBoundary, error) {
		if service != "devbox" {
			t.Fatalf("boundary service = %q, want devbox", service)
		}
		captured = true
		return vmGuestReadyBoundary{Cursor: "s/abc"}, nil
	}
	vmProvisionGuestReadyWaitFunc = func(ctx context.Context, service string, network vmNetworkPlan, boundary vmGuestReadyBoundary) (vmGuestReadyReport, error) {
		waited = true
		if !captured {
			t.Fatal("wait called before boundary capture")
		}
		if boundary.Cursor != "s/abc" {
			t.Fatalf("boundary = %#v, want cursor", boundary)
		}
		return vmGuestReadyReport{Interface: "eth0", IP: netip.MustParseAddr("192.168.100.4")}, nil
	}

	if err := execer.runVM(cli.RunFlags{Net: "svc", Restart: true}, vmUbuntu2604Payload); err != nil {
		t.Fatalf("runVM: %v", err)
	}
	if !captured || !waited {
		t.Fatalf("captured=%v waited=%v, want both true", captured, waited)
	}
	text := out.String()
	waitIdx := strings.Index(text, "Waiting for guest readiness")
	runIdx := strings.Index(text, "VM devbox is running")
	if waitIdx < 0 || runIdx < 0 || waitIdx > runIdx {
		t.Fatalf("output order wrong:\n%s", text)
	}
}

func TestRunVMSkipsGuestReadinessWhenRestartFalse(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "devbox")
	vmProvisionGuestReadyBoundaryFunc = func(context.Context, string) (vmGuestReadyBoundary, error) {
		t.Fatal("boundary should not be captured when restart=false")
		return vmGuestReadyBoundary{}, nil
	}
	vmProvisionGuestReadyWaitFunc = func(context.Context, string, vmNetworkPlan, vmGuestReadyBoundary) (vmGuestReadyReport, error) {
		t.Fatal("readiness should not be waited when restart=false")
		return vmGuestReadyReport{}, nil
	}

	if err := execer.runVM(cli.RunFlags{Net: "svc", Restart: false}, vmUbuntu2604Payload); err != nil {
		t.Fatalf("runVM: %v", err)
	}
}

func TestRunVMGuestReadinessFailureKeepsCommittedVM(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "devbox")
	vmProvisionGuestReadyBoundaryFunc = func(context.Context, string) (vmGuestReadyBoundary, error) {
		return vmGuestReadyBoundary{}, nil
	}
	vmProvisionGuestReadyWaitFunc = func(context.Context, string, vmNetworkPlan, vmGuestReadyBoundary) (vmGuestReadyReport, error) {
		return vmGuestReadyReport{}, errors.New("guest readiness timeout")
	}

	err := execer.runVM(cli.RunFlags{Net: "svc", Restart: true}, vmUbuntu2604Payload)
	if err == nil || !strings.Contains(err.Error(), "guest readiness timeout") {
		t.Fatalf("runVM error = %v, want guest readiness timeout", err)
	}
	svc := getTestService(t, server, "devbox")
	if svc.VM == nil || svc.VM.SetupState != "ready" {
		t.Fatalf("VM after readiness failure = %#v, want committed ready VM for console recovery", svc.VM)
	}
}

func TestRunVMKeepsReadyWhenRestartFailsAfterCommit(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, systemctlCalls := newVMProvisionTestExecer(t, server, "svc")
	vmProvisionSystemctlFunc = func(args ...string) error {
		*systemctlCalls = append(*systemctlCalls, append([]string(nil), args...))
		if reflect.DeepEqual(args, []string{"restart", vmSystemdUnitName("svc")}) {
			return errors.New("restart failed")
		}
		return nil
	}

	err := execer.runVM(cli.RunFlags{Net: "svc", Restart: true}, vmUbuntu2604Payload)
	if err == nil || !strings.Contains(err.Error(), "restart failed") {
		t.Fatalf("runVM error = %v, want restart failure", err)
	}
	svc := getTestService(t, server, "svc")
	if svc.VM == nil || svc.VM.SetupState != "ready" {
		t.Fatalf("VM after restart failure = %#v, want ready", svc.VM)
	}
}

func TestRunVMDoesNotCommitReadyWhenSystemdEnableFails(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, systemctlCalls := newVMProvisionTestExecer(t, server, "svc")
	vmProvisionSystemctlFunc = func(args ...string) error {
		*systemctlCalls = append(*systemctlCalls, append([]string(nil), args...))
		if reflect.DeepEqual(args, []string{"enable", vmSystemdUnitName("svc")}) {
			return errors.New("enable failed")
		}
		return nil
	}

	err := execer.runVM(cli.RunFlags{Net: "svc"}, vmUbuntu2604Payload)
	if err == nil || !strings.Contains(err.Error(), "enable failed") {
		t.Fatalf("runVM error = %v, want enable failure", err)
	}
	assertNoReadyVM(t, server, "svc")
}

func TestRunVMRollsBackNewServiceReservationOnProvisionFailure(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "svc")
	vmProvisionMetadataInjector = func(context.Context, string, vmMetadataConfig) error {
		return errors.New("metadata injection failed")
	}

	err := execer.runVM(cli.RunFlags{Net: "svc"}, vmUbuntu2604Payload)
	if err == nil || !strings.Contains(err.Error(), "metadata injection failed") {
		t.Fatalf("runVM error = %v, want metadata failure", err)
	}
	dv, getErr := server.getDB()
	if getErr != nil {
		t.Fatalf("getDB: %v", getErr)
	}
	if _, ok := dv.AsStruct().Services["svc"]; ok {
		t.Fatalf("service reservation was not rolled back: %#v", dv.AsStruct().Services["svc"])
	}
}

func TestRunVMStaleImageDefaultNonInteractiveFailsWithoutEnsure(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "svc")
	stubVMProvisionImageState(t, staleVMProvisionImageState("ubuntu-26.04-amd64-v1", "ubuntu-26.04-amd64-v3"))
	ensureCalled := false
	vmImageEnsureFunc = func(context.Context, vmImageCache, string, ProgressUI) (vmImageAsset, error) {
		ensureCalled = true
		return vmImageAsset{}, fmt.Errorf("ensure should not be called")
	}

	err := execer.runVM(cli.RunFlags{Net: "svc"}, vmUbuntu2604Payload)
	if err == nil {
		t.Fatal("runVM error = nil, want stale image policy error")
	}
	for _, want := range []string{"ubuntu-26.04-amd64-v1", "ubuntu-26.04-amd64-v3", "--image-policy=update", "--image-policy=cached", "yeet vm images update"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("stale image error missing %q: %v", want, err)
		}
	}
	if ensureCalled {
		t.Fatal("ensure was called for non-interactive stale prompt policy")
	}
	assertNoReadyVM(t, server, "svc")
}

func TestRunVMStaleImageUpdatePolicyEnsuresLatest(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "svc")
	stubVMProvisionImageState(t, staleVMProvisionImageState("ubuntu-26.04-amd64-v1", "ubuntu-26.04-amd64-v3"))
	ensureCalled := false
	vmImageEnsureFunc = func(_ context.Context, _ vmImageCache, payload string, ui ProgressUI) (vmImageAsset, error) {
		ensureCalled = true
		if payload != vmUbuntu2604Payload {
			t.Fatalf("ensure payload = %q, want %q", payload, vmUbuntu2604Payload)
		}
		if ui == nil {
			t.Fatal("ensure UI is nil")
		}
		return fakeVMImageAssetVersion(t, "ubuntu-26.04-amd64-v3")
	}

	if err := execer.runVM(cli.RunFlags{Net: "svc", ImagePolicy: "update"}, vmUbuntu2604Payload); err != nil {
		t.Fatalf("runVM: %v", err)
	}
	if !ensureCalled {
		t.Fatal("ensure was not called for update policy")
	}
	assertVMImageVersion(t, server, "svc", "ubuntu-26.04-amd64-v3")
}

func TestRunVMStaleImageUpdatePolicyPrunesOldCacheAfterRefresh(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "svc")
	cacheRoot := filepath.Join(server.cfg.RootDir, "vm-images")
	oldDir := seedCachedVMImage(t, cacheRoot, "ubuntu-26.04-amd64-v1")
	currentDir := seedCachedVMImage(t, cacheRoot, "ubuntu-26.04-amd64-v3")
	stubVMProvisionImageState(t, staleVMProvisionImageState("ubuntu-26.04-amd64-v1", "ubuntu-26.04-amd64-v3"))
	vmImageEnsureFunc = func(context.Context, vmImageCache, string, ProgressUI) (vmImageAsset, error) {
		asset, err := fakeVMImageAssetVersion(t, "ubuntu-26.04-amd64-v3")
		if err != nil {
			return vmImageAsset{}, err
		}
		asset.Paths.Dir = currentDir
		return asset, nil
	}

	if err := execer.runVM(cli.RunFlags{Net: "svc", ImagePolicy: "update"}, vmUbuntu2604Payload); err != nil {
		t.Fatalf("runVM: %v", err)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("old cache dir stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(currentDir); err != nil {
		t.Fatalf("current cache dir should remain: %v", err)
	}
	assertVMImageVersion(t, server, "svc", "ubuntu-26.04-amd64-v3")
}

func TestRunVMCachedImagePolicyUsesCachedStaleVersion(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "svc")
	seedCachedVMProvisionImage(t, server, "ubuntu-26.04-amd64-v1")
	stubVMProvisionImageState(t, staleVMProvisionImageState("ubuntu-26.04-amd64-v1", "ubuntu-26.04-amd64-v3"))
	vmImageEnsureFunc = func(context.Context, vmImageCache, string, ProgressUI) (vmImageAsset, error) {
		return vmImageAsset{}, fmt.Errorf("ensure should not be called")
	}

	if err := execer.runVM(cli.RunFlags{Net: "svc", ImagePolicy: "cached"}, vmUbuntu2604Payload); err != nil {
		t.Fatalf("runVM: %v", err)
	}
	assertVMImageVersion(t, server, "svc", "ubuntu-26.04-amd64-v1")
}

func TestRunVMCachedImagePolicyUsesRequestedOfficialFamily(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "svc")
	contents := vmImageTestContents()
	cacheRoot := filepath.Join(server.cfg.RootDir, "vm-images")
	ubuntuDir := writeCachedVMImageManifest(t, cacheRoot, vmImageTestManifest("ubuntu-26.04-amd64-v99", contents))
	writeCachedVMImageArtifacts(t, ubuntuDir, contents)
	nixosCached := vmImageTestManifest("nixos-26.05-amd64-v1", contents)
	nixosCached.Name = "yeet-nixos-26.05"
	nixosDir := writeCachedVMImageManifest(t, cacheRoot, nixosCached)
	writeCachedVMImageArtifacts(t, nixosDir, contents)
	nixosLatest := vmImageTestManifest("nixos-26.05-amd64-v2", contents)
	nixosLatest.Name = "yeet-nixos-26.05"
	manifestServer := newVMImageArtifactTestServer(t, nixosLatest, contents)
	defer manifestServer.Close()

	vmImageInspectFunc = func(ctx context.Context, cache vmImageCache, payload string) (vmImageCacheState, vmImageManifest, error) {
		if payload != vmNixOS2605Payload {
			t.Fatalf("inspect payload = %q, want %q", payload, vmNixOS2605Payload)
		}
		cache.ManifestURL = manifestServer.URL + "/manifest.json"
		return cache.Inspect(ctx, payload)
	}
	vmImageEnsureFunc = func(context.Context, vmImageCache, string, ProgressUI) (vmImageAsset, error) {
		return vmImageAsset{}, fmt.Errorf("ensure should not be called")
	}

	if err := execer.runVM(cli.RunFlags{Net: "svc", ImagePolicy: "cached"}, vmNixOS2605Payload); err != nil {
		t.Fatalf("runVM: %v", err)
	}
	assertVMImageVersion(t, server, "svc", "nixos-26.05-amd64-v1")
}

func TestRunVMStaleImageTTYPromptYesEnsuresLatest(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "svc")
	stubVMProvisionImageState(t, staleVMProvisionImageState("ubuntu-26.04-amd64-v1", "ubuntu-26.04-amd64-v3"))
	var out bytes.Buffer
	execer.rw = readWriter{Reader: strings.NewReader("y\n"), Writer: &out}
	execer.isPty = true
	ensureCalled := false
	vmImageEnsureFunc = func(context.Context, vmImageCache, string, ProgressUI) (vmImageAsset, error) {
		ensureCalled = true
		return fakeVMImageAssetVersion(t, "ubuntu-26.04-amd64-v3")
	}

	if err := execer.runVM(cli.RunFlags{Net: "svc"}, vmUbuntu2604Payload); err != nil {
		t.Fatalf("runVM: %v", err)
	}
	if !ensureCalled {
		t.Fatal("ensure was not called after prompt confirmation")
	}
	if !strings.Contains(out.String(), "Update VM image") {
		t.Fatalf("prompt output = %q, want update prompt", out.String())
	}
	assertVMImageVersion(t, server, "svc", "ubuntu-26.04-amd64-v3")
}

func TestRunVMStaleImageTTYPromptReadsRawInputWhenPtyInputBypassed(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "svc")
	stubVMProvisionImageState(t, staleVMProvisionImageState("ubuntu-26.04-amd64-v1", "ubuntu-26.04-amd64-v3"))
	var ptyOut bytes.Buffer
	var rawOut bytes.Buffer
	execer.rw = readWriter{Reader: strings.NewReader(""), Writer: &ptyOut}
	execer.rawRW = readWriter{Reader: strings.NewReader("y\n"), Writer: &rawOut}
	execer.isPty = true
	execer.bypassPtyInput = true
	ensureCalled := false
	vmImageEnsureFunc = func(context.Context, vmImageCache, string, ProgressUI) (vmImageAsset, error) {
		ensureCalled = true
		return fakeVMImageAssetVersion(t, "ubuntu-26.04-amd64-v3")
	}

	if err := execer.runVM(cli.RunFlags{Net: "svc"}, vmUbuntu2604Payload); err != nil {
		t.Fatalf("runVM: %v", err)
	}
	if !ensureCalled {
		t.Fatal("ensure was not called after raw input confirmation")
	}
	if !strings.Contains(ptyOut.String(), "[y/N]: y\n") {
		t.Fatalf("pty output = %q, want echoed raw answer", ptyOut.String())
	}
	if rawOut.Len() != 0 {
		t.Fatalf("raw output = %q, want prompt written to pty output only", rawOut.String())
	}
	assertVMImageVersion(t, server, "svc", "ubuntu-26.04-amd64-v3")
}

func TestRunVMStaleImageTTYPromptEchoesRawAnswerWhenPtyInputBypassed(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "svc")
	stubVMProvisionImageState(t, staleVMProvisionImageState("ubuntu-26.04-amd64-v1", "ubuntu-26.04-amd64-v3"))
	var ptyOut bytes.Buffer
	var rawOut bytes.Buffer
	execer.rw = readWriter{Reader: strings.NewReader(""), Writer: &ptyOut}
	execer.rawRW = readWriter{Reader: strings.NewReader("y\r"), Writer: &rawOut}
	execer.isPty = true
	execer.bypassPtyInput = true
	vmImageEnsureFunc = func(context.Context, vmImageCache, string, ProgressUI) (vmImageAsset, error) {
		return fakeVMImageAssetVersion(t, "ubuntu-26.04-amd64-v3")
	}

	if err := execer.runVM(cli.RunFlags{Net: "svc"}, vmUbuntu2604Payload); err != nil {
		t.Fatalf("runVM: %v", err)
	}
	if !strings.Contains(ptyOut.String(), "[y/N]: y\n") {
		t.Fatalf("pty output = %q, want echoed raw answer", ptyOut.String())
	}
	if rawOut.Len() != 0 {
		t.Fatalf("raw output = %q, want prompt written to pty output only", rawOut.String())
	}
	assertVMImageVersion(t, server, "svc", "ubuntu-26.04-amd64-v3")
}

func TestRunVMStaleImageTTYPromptRawEnterUsesCachedWhenPtyInputBypassed(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "svc")
	seedCachedVMProvisionImage(t, server, "ubuntu-26.04-amd64-v1")
	stubVMProvisionImageState(t, staleVMProvisionImageState("ubuntu-26.04-amd64-v1", "ubuntu-26.04-amd64-v3"))
	var ptyOut bytes.Buffer
	execer.rw = readWriter{Reader: strings.NewReader(""), Writer: &ptyOut}
	execer.rawRW = readWriter{Reader: strings.NewReader("\r"), Writer: io.Discard}
	execer.isPty = true
	execer.bypassPtyInput = true
	vmImageEnsureFunc = func(context.Context, vmImageCache, string, ProgressUI) (vmImageAsset, error) {
		return vmImageAsset{}, fmt.Errorf("ensure should not be called")
	}

	if err := execer.runVM(cli.RunFlags{Net: "svc"}, vmUbuntu2604Payload); err != nil {
		t.Fatalf("runVM: %v", err)
	}
	if !strings.Contains(ptyOut.String(), "[y/N]: \n") {
		t.Fatalf("pty output = %q, want default answer newline", ptyOut.String())
	}
	assertVMImageVersion(t, server, "svc", "ubuntu-26.04-amd64-v1")
}

func TestRunVMStaleImageTTYPromptRawInterruptAbortsWhenPtyInputBypassed(t *testing.T) {
	for _, tc := range []struct {
		name      string
		input     string
		wantEcho  string
		wantError string
	}{
		{name: "ctrl-c", input: "\x03", wantEcho: "^C\n", wantError: "interrupted"},
		{name: "ctrl-backslash", input: "\x1c", wantEcho: "^\\\n", wantError: "quit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(t)
			execer, _, _, _ := newVMProvisionTestExecer(t, server, "svc")
			seedCachedVMProvisionImage(t, server, "ubuntu-26.04-amd64-v1")
			stubVMProvisionImageState(t, staleVMProvisionImageState("ubuntu-26.04-amd64-v1", "ubuntu-26.04-amd64-v3"))
			var ptyOut bytes.Buffer
			execer.rw = readWriter{Reader: strings.NewReader(""), Writer: &ptyOut}
			execer.rawRW = readWriter{Reader: strings.NewReader(tc.input), Writer: io.Discard}
			execer.isPty = true
			execer.bypassPtyInput = true
			vmImageEnsureFunc = func(context.Context, vmImageCache, string, ProgressUI) (vmImageAsset, error) {
				return vmImageAsset{}, fmt.Errorf("ensure should not be called")
			}

			err := execer.runVM(cli.RunFlags{Net: "svc"}, vmUbuntu2604Payload)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("runVM error = %v, want %q", err, tc.wantError)
			}
			if !strings.Contains(ptyOut.String(), tc.wantEcho) {
				t.Fatalf("pty output = %q, want interrupt echo %q", ptyOut.String(), tc.wantEcho)
			}
			assertNoReadyVM(t, server, "svc")
		})
	}
}

func TestRunVMStaleImageTTYPromptNoUsesCachedVersion(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "svc")
	seedCachedVMProvisionImage(t, server, "ubuntu-26.04-amd64-v1")
	stubVMProvisionImageState(t, staleVMProvisionImageState("ubuntu-26.04-amd64-v1", "ubuntu-26.04-amd64-v3"))
	var out bytes.Buffer
	execer.rw = readWriter{Reader: strings.NewReader("n\n"), Writer: &out}
	execer.isPty = true
	vmImageEnsureFunc = func(context.Context, vmImageCache, string, ProgressUI) (vmImageAsset, error) {
		return vmImageAsset{}, fmt.Errorf("ensure should not be called")
	}

	if err := execer.runVM(cli.RunFlags{Net: "svc"}, vmUbuntu2604Payload); err != nil {
		t.Fatalf("runVM: %v", err)
	}
	if !strings.Contains(out.String(), "latest v3") {
		t.Fatalf("prompt output = %q, want latest version", out.String())
	}
	assertVMImageVersion(t, server, "svc", "ubuntu-26.04-amd64-v1")
}

func TestRunVMMissingImageAutomaticallyEnsures(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "svc")
	stubVMProvisionImageState(t, vmImageCacheState{
		Payload:       vmUbuntu2604Payload,
		LatestVersion: defaultVMImageVersion,
		State:         vmImageCacheMissing,
		CachePath:     filepath.Join(server.cfg.RootDir, "vm-images", defaultVMImageVersion),
		ManifestURL:   defaultVMImageManifestURL,
	})
	ensureCalled := false
	vmImageEnsureFunc = func(context.Context, vmImageCache, string, ProgressUI) (vmImageAsset, error) {
		ensureCalled = true
		return fakeVMImageAssetVersion(t, defaultVMImageVersion)
	}

	if err := execer.runVM(cli.RunFlags{Net: "svc"}, vmUbuntu2604Payload); err != nil {
		t.Fatalf("runVM: %v", err)
	}
	if !ensureCalled {
		t.Fatal("ensure was not called for missing image cache")
	}
	assertVMImageVersion(t, server, "svc", defaultVMImageVersion)
}

func TestRunVMCurrentImageUsesCachedAssetWithoutEnsuring(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "svc")
	contents := vmImageTestContents()
	manifest := vmImageTestManifest(defaultVMImageVersion, contents)
	dir := writeCachedVMImageManifest(t, filepath.Join(server.cfg.RootDir, "vm-images"), manifest)
	writeCachedVMImageArtifacts(t, dir, contents)
	oldPrepareRootFS := prepareVMRootFSFunc
	t.Cleanup(func() { prepareVMRootFSFunc = oldPrepareRootFS })
	prepareVMRootFSFunc = func(_ context.Context, source string) (string, error) {
		return source, nil
	}
	stubVMProvisionImageState(t, vmImageCacheState{
		Payload:       vmUbuntu2604Payload,
		CachedVersion: defaultVMImageVersion,
		LatestVersion: defaultVMImageVersion,
		State:         vmImageCacheCurrent,
		CachePath:     filepath.Join(server.cfg.RootDir, "vm-images", defaultVMImageVersion),
		ManifestURL:   defaultVMImageManifestURL,
	})
	vmImageEnsureFunc = func(context.Context, vmImageCache, string, ProgressUI) (vmImageAsset, error) {
		t.Fatal("vmImageEnsureFunc called for current VM image cache")
		return vmImageAsset{}, nil
	}

	if err := execer.runVM(cli.RunFlags{Net: "svc"}, vmUbuntu2604Payload); err != nil {
		t.Fatalf("runVM: %v", err)
	}
	assertVMImageVersion(t, server, "svc", defaultVMImageVersion)
}

func TestRunVMCurrentImageDoesNotPrintDownloadProgress(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "devbox")
	var out bytes.Buffer
	execer.rw = &out
	seedCachedVMProvisionImage(t, server, defaultVMImageVersion)
	stubVMProvisionImageState(t, vmImageCacheState{
		Payload:       vmUbuntu2604Payload,
		CachedVersion: defaultVMImageVersion,
		LatestVersion: defaultVMImageVersion,
		State:         vmImageCacheCurrent,
		CachePath:     filepath.Join(server.cfg.RootDir, "vm-images", defaultVMImageVersion),
		ManifestURL:   defaultVMImageManifestURL,
	})
	vmImageEnsureFunc = func(context.Context, vmImageCache, string, ProgressUI) (vmImageAsset, error) {
		t.Fatal("vmImageEnsureFunc called for current VM image cache")
		return vmImageAsset{}, nil
	}

	if err := execer.runVM(cli.RunFlags{Net: "svc", Restart: false}, vmUbuntu2604Payload); err != nil {
		t.Fatalf("runVM: %v", err)
	}
	if strings.Contains(out.String(), "Download VM image") {
		t.Fatalf("current cache printed download progress:\n%s", out.String())
	}
}

func TestRunVMUnknownImageCacheStateErrors(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "svc")
	stubVMProvisionImageState(t, vmImageCacheState{
		Payload:       vmUbuntu2604Payload,
		CachedVersion: "ubuntu-26.04-amd64-v1",
		LatestVersion: "ubuntu-26.04-amd64-v3",
		State:         "confused",
		CachePath:     filepath.Join(server.cfg.RootDir, "vm-images", "ubuntu-26.04-amd64-v1"),
		ManifestURL:   defaultVMImageManifestURL,
	})
	vmImageEnsureFunc = func(context.Context, vmImageCache, string, ProgressUI) (vmImageAsset, error) {
		return vmImageAsset{}, fmt.Errorf("ensure should not be called")
	}

	err := execer.runVM(cli.RunFlags{Net: "svc"}, vmUbuntu2604Payload)
	if err == nil || !strings.Contains(err.Error(), `unknown VM image cache state "confused"`) {
		t.Fatalf("runVM error = %v, want unknown image cache state", err)
	}
	assertNoReadyVM(t, server, "svc")
}

func TestReserveVMServiceNetworkAllocatesInsideMutation(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "svc")
	if _, _, err := server.cfg.DB.MutateService("other", func(_ *db.Data, s *db.Service) error {
		s.SvcNetwork = &db.SvcNetwork{IPv4: netipMustParseAddr(t, "192.168.100.3")}
		return nil
	}); err != nil {
		t.Fatalf("seed service: %v", err)
	}
	net, err := execer.reserveVMServiceNetwork(cli.RunFlags{Net: "svc"})
	if err != nil {
		t.Fatalf("reserveVMServiceNetwork: %v", err)
	}
	if net == nil || net.IPv4.String() != "192.168.100.4" {
		t.Fatalf("reserved network = %#v, want 192.168.100.4", net)
	}
	svc := getTestService(t, server, "svc")
	if svc.SvcNetwork == nil || svc.SvcNetwork.IPv4.String() != "192.168.100.4" {
		t.Fatalf("stored reserved network = %#v", svc.SvcNetwork)
	}
}

func TestRunVMRejectsUnsupportedPayload(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "svc")
	vmImageInspectFunc = func(ctx context.Context, cache vmImageCache, payload string) (vmImageCacheState, vmImageManifest, error) {
		return cache.Inspect(ctx, payload)
	}
	vmImageEnsureFunc = func(ctx context.Context, cache vmImageCache, payload string, ui ProgressUI) (vmImageAsset, error) {
		return ensureVMImageAssetWithProgress(ctx, cache, payload, ui)
	}

	err := execer.runVM(cli.RunFlags{Net: "svc"}, "oci://debian/13")
	if err == nil || !strings.Contains(err.Error(), "unsupported VM image payload") {
		t.Fatalf("runVM error = %v, want unsupported payload", err)
	}
	assertNoReadyVM(t, server, "svc")
}

func TestRunVMReportsUnknownLocalImage(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "svc")
	vmImageInspectFunc = func(ctx context.Context, cache vmImageCache, payload string) (vmImageCacheState, vmImageManifest, error) {
		return cache.Inspect(ctx, payload)
	}
	vmImageEnsureFunc = func(ctx context.Context, cache vmImageCache, payload string, ui ProgressUI) (vmImageAsset, error) {
		return ensureVMImageAssetWithProgress(ctx, cache, payload, ui)
	}

	err := execer.runVM(cli.RunFlags{Net: "svc"}, "vm://debian/13")
	if err == nil || !strings.Contains(err.Error(), "import it with `yeet vm images import debian/13`") {
		t.Fatalf("runVM error = %v, want local import hint", err)
	}
	assertNoReadyVM(t, server, "svc")
}

func TestRunVMLocalImportedImage(t *testing.T) {
	server := newTestServer(t)
	service := "svc"
	execer, _, _, _ := newVMProvisionTestExecer(t, server, service)
	vmImageInspectFunc = func(ctx context.Context, cache vmImageCache, payload string) (vmImageCacheState, vmImageManifest, error) {
		return cache.Inspect(ctx, payload)
	}
	vmImageEnsureFunc = func(ctx context.Context, cache vmImageCache, payload string, ui ProgressUI) (vmImageAsset, error) {
		return ensureVMImageAssetWithProgress(ctx, cache, payload, ui)
	}

	importer := localVMImageImporter{
		CacheRoot: filepath.Join(server.cfg.RootDir, "vm-images"),
		EnsureManagedAsset: func(context.Context) (vmImageAsset, error) {
			return fakeManagedVMImageAsset(t), nil
		},
	}
	ref, err := importer.Import(context.Background(), localVMImageImportRequest{
		Name:   "foo/bar",
		Reader: localVMImageBundleTar(t, map[string][]byte{"rootfs.ext4": []byte("rootfs")}),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	if err := execer.runVM(cli.RunFlags{Net: "svc"}, "vm://foo/bar"); err != nil {
		t.Fatalf("runVM: %v", err)
	}
	assertVMImageVersion(t, server, service, ref.Version)
}

func newVMProvisionTestExecer(t *testing.T, server *Server, service string) (*ttyExecer, string, string, *[][]string) {
	t.Helper()
	serviceRoot := filepath.Join(t.TempDir(), "services", service)
	systemdDir := filepath.Join(t.TempDir(), "systemd")
	if err := os.MkdirAll(filepath.Dir(serviceRoot), 0o755); err != nil {
		t.Fatalf("mkdir service root parent: %v", err)
	}
	execer := &ttyExecer{s: server, sn: service, ctx: context.Background()}
	server.cfg.ServicesRoot = filepath.Dir(serviceRoot)
	oldHostProfile := vmProvisionHostProfileFunc
	oldImageInspect := vmImageInspectFunc
	oldImageEnsure := vmImageEnsureFunc
	oldDiskRunner := vmProvisionDiskRunner
	oldNetworkRunner := vmProvisionNetworkRunner
	oldMetadataInjector := vmProvisionMetadataInjector
	oldSSHKey := vmProvisionSSHKeyFunc
	oldSystemdDir := vmProvisionSystemdDir
	oldSystemctl := vmProvisionSystemctlFunc
	oldPrepareRootFS := prepareVMRootFSFunc
	oldGuestReadyBoundary := vmProvisionGuestReadyBoundaryFunc
	oldGuestReadyWait := vmProvisionGuestReadyWaitFunc
	t.Cleanup(func() {
		vmProvisionHostProfileFunc = oldHostProfile
		vmImageInspectFunc = oldImageInspect
		vmImageEnsureFunc = oldImageEnsure
		vmProvisionDiskRunner = oldDiskRunner
		vmProvisionNetworkRunner = oldNetworkRunner
		vmProvisionMetadataInjector = oldMetadataInjector
		vmProvisionSSHKeyFunc = oldSSHKey
		vmProvisionSystemdDir = oldSystemdDir
		vmProvisionSystemctlFunc = oldSystemctl
		prepareVMRootFSFunc = oldPrepareRootFS
		vmProvisionGuestReadyBoundaryFunc = oldGuestReadyBoundary
		vmProvisionGuestReadyWaitFunc = oldGuestReadyWait
	})
	vmProvisionHostProfileFunc = func(_ *ttyExecer, resolved resolvedServiceRoot, runningVMBytes int64) (vmHostProfile, error) {
		if resolved.Root != serviceRoot {
			t.Fatalf("resolved service root = %q, want %q", resolved.Root, serviceRoot)
		}
		return vmHostProfile{
			Arch:           "x86_64",
			HasKVM:         true,
			LogicalCPUs:    8,
			MemoryBytes:    16 << 30,
			StorageBytes:   128 << 30,
			StorageZFS:     resolved.ZFS,
			RunningVMBytes: runningVMBytes,
		}, nil
	}
	vmImageInspectFunc = func(context.Context, vmImageCache, string) (vmImageCacheState, vmImageManifest, error) {
		return vmImageCacheState{
			Payload:       vmUbuntu2604Payload,
			LatestVersion: defaultVMImageVersion,
			State:         vmImageCacheMissing,
			CachePath:     filepath.Join(server.cfg.RootDir, "vm-images", defaultVMImageVersion),
			ManifestURL:   defaultVMImageManifestURL,
		}, vmImageManifest{Version: defaultVMImageVersion}, nil
	}
	vmImageEnsureFunc = func(context.Context, vmImageCache, string, ProgressUI) (vmImageAsset, error) {
		return fakeVMImageAsset(t)
	}
	prepareVMRootFSFunc = func(_ context.Context, source string) (string, error) {
		return strings.TrimSuffix(source, ".zst"), nil
	}
	vmProvisionDiskRunner = func(context.Context, []string) error { return nil }
	vmProvisionNetworkRunner = func([]string) error { return nil }
	vmProvisionMetadataInjector = func(context.Context, string, vmMetadataConfig) error { return nil }
	vmProvisionSSHKeyFunc = func() (string, error) {
		return "ssh-ed25519 AAAATEST user@example", nil
	}
	vmProvisionSystemdDir = systemdDir
	systemctlCalls := [][]string{}
	vmProvisionSystemctlFunc = func(args ...string) error {
		systemctlCalls = append(systemctlCalls, append([]string(nil), args...))
		return nil
	}
	vmProvisionGuestReadyBoundaryFunc = func(context.Context, string) (vmGuestReadyBoundary, error) {
		return vmGuestReadyBoundary{}, nil
	}
	vmProvisionGuestReadyWaitFunc = func(context.Context, string, vmNetworkPlan, vmGuestReadyBoundary) (vmGuestReadyReport, error) {
		return vmGuestReadyReport{}, nil
	}
	return execer, serviceRoot, systemdDir, &systemctlCalls
}

func fakeVMImageAsset(t *testing.T) (vmImageAsset, error) {
	t.Helper()
	return fakeVMImageAssetVersion(t, defaultVMImageVersion)
}

func fakeVMImageAssetVersion(t *testing.T, version string) (vmImageAsset, error) {
	t.Helper()
	dir := t.TempDir()
	paths := vmImagePaths{
		Manifest:        filepath.Join(dir, "manifest.json"),
		Dir:             dir,
		KernelPath:      filepath.Join(dir, "vmlinux"),
		RootFSPath:      filepath.Join(dir, "rootfs.ext4.zst"),
		FirecrackerPath: filepath.Join(dir, "firecracker"),
	}
	return vmImageAsset{
		Paths:              paths,
		PreparedRootFSPath: filepath.Join(dir, "rootfs.ext4"),
		Manifest: vmImageManifest{
			Name:         "ubuntu",
			Version:      version,
			Architecture: "x86_64",
			Kernel:       "vmlinux",
			RootFS:       "rootfs.ext4.zst",
			Firecracker:  "firecracker",
			RootFSSize:   2 << 30,
		},
	}, nil
}

func staleVMProvisionImageState(cachedVersion, latestVersion string) vmImageCacheState {
	return vmImageCacheState{
		Payload:       vmUbuntu2604Payload,
		CachedVersion: cachedVersion,
		LatestVersion: latestVersion,
		State:         vmImageCacheStale,
		CachePath:     filepath.Join("vm-images", cachedVersion),
		ManifestURL:   defaultVMImageManifestURL,
	}
}

func stubVMProvisionImageState(t *testing.T, state vmImageCacheState) {
	t.Helper()
	vmImageInspectFunc = func(_ context.Context, cache vmImageCache, payload string) (vmImageCacheState, vmImageManifest, error) {
		if payload != vmUbuntu2604Payload {
			t.Fatalf("inspect payload = %q, want %q", payload, vmUbuntu2604Payload)
		}
		if strings.TrimSpace(cache.Root) == "" {
			t.Fatal("inspect cache root is empty")
		}
		return state, vmImageManifest{Version: state.LatestVersion}, nil
	}
}

func seedCachedVMProvisionImage(t *testing.T, server *Server, version string) {
	t.Helper()
	contents := vmImageTestContents()
	manifest := vmImageTestManifest(version, contents)
	root := filepath.Join(server.cfg.RootDir, "vm-images")
	dir := writeCachedVMImageManifest(t, root, manifest)
	writeCachedVMImageArtifacts(t, dir, contents)
}

func assertVMImageVersion(t *testing.T, server *Server, service, version string) {
	t.Helper()
	svc := getTestService(t, server, service)
	if svc.VM == nil {
		t.Fatal("VM config is nil")
	}
	if svc.VM.Image.Version != version {
		t.Fatalf("VM image version = %q, want %q", svc.VM.Image.Version, version)
	}
}

func assertNoReadyVM(t *testing.T, server *Server, service string) {
	t.Helper()
	dv, err := server.getDB()
	if err != nil {
		t.Fatalf("getDB: %v", err)
	}
	svc, ok := dv.AsStruct().Services[service]
	if !ok || svc.VM == nil {
		return
	}
	if svc.VM.SetupState == "ready" {
		t.Fatalf("VM was committed ready after failure: %#v", svc.VM)
	}
}

func getTestService(t *testing.T, server *Server, service string) *db.Service {
	t.Helper()
	dv, err := server.getDB()
	if err != nil {
		t.Fatalf("getDB: %v", err)
	}
	svc, ok := dv.AsStruct().Services[service]
	if !ok {
		t.Fatalf("service %q missing from DB", service)
	}
	return svc
}

func vmTestSystemctlCalled(calls [][]string, args ...string) bool {
	for _, call := range calls {
		if reflect.DeepEqual(call, args) {
			return true
		}
	}
	return false
}
