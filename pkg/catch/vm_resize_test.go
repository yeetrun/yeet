// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/iso"
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

func TestVMSetResolvesRuntimeIdentityOnce(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", root, vmDiskBackendRaw)
	withServiceSetVMRunningCheck(t, func(*Server, string) (bool, error) { return false, nil })
	identityCalls := 0
	withServiceSetVMRuntimeIdentity(t, func() (vmRuntimeIdentity, error) {
		identityCalls++
		return vmRuntimeIdentity{UID: 812, GID: 813}, nil
	})

	if err := server.updateVMServiceSettings(context.Background(), "devbox", cli.VMSetFlags{CPUs: 6}); err != nil {
		t.Fatalf("updateVMServiceSettings: %v", err)
	}
	if identityCalls != 1 {
		t.Fatalf("runtime identity calls = %d, want 1", identityCalls)
	}
}

func TestVMSetUpdatesBalloonConfigAndFirecrackerConfig(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", root, vmDiskBackendRaw)
	withServiceSetVMRunningCheck(t, func(*Server, string) (bool, error) { return false, nil })

	if err := server.updateVMServiceSettings(context.Background(), "devbox", cli.VMSetFlags{MemoryMin: "2g", Balloon: "auto"}); err != nil {
		t.Fatalf("updateVMServiceSettings: %v", err)
	}
	svc := getTestService(t, server, "devbox")
	if svc.VM.Balloon.Mode != vmBalloonModeAuto || svc.VM.Balloon.MinBytes != 2<<30 {
		t.Fatalf("Balloon = %#v, want auto 2GiB floor", svc.VM.Balloon)
	}
	raw, err := os.ReadFile(filepath.Join(serviceRunDirForRoot(root), "firecracker.json"))
	if err != nil {
		t.Fatalf("read Firecracker config: %v", err)
	}
	if !strings.Contains(string(raw), `"balloon"`) {
		t.Fatalf("Firecracker config missing balloon:\n%s", raw)
	}
}

func TestRunningVMMemoryTreatsLegacyMissingBalloonConfigAsReserved(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t)
	seedVMForResize(t, server, "legacy", root, vmDiskBackendRaw)

	maxBytes, minBytes, err := server.runningVMMemoryExcluding("")
	if err != nil {
		t.Fatalf("runningVMMemoryExcluding: %v", err)
	}
	if maxBytes != 4<<30 || minBytes != 4<<30 {
		t.Fatalf("running memory = max %d min %d, want both 4GiB", maxBytes, minBytes)
	}
}

func TestVMSetDiskOnlyPreservesLegacyBalloonOff(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", root, vmDiskBackendRaw)
	withServiceSetVMRunningCheck(t, func(*Server, string) (bool, error) { return false, nil })
	withServiceSetVMDiskRunner(t, func(context.Context, []string) error { return nil })

	if err := server.updateVMServiceSettings(context.Background(), "devbox", cli.VMSetFlags{Disk: "32g"}); err != nil {
		t.Fatalf("updateVMServiceSettings: %v", err)
	}
	svc := getTestService(t, server, "devbox")
	if svc.VM.Balloon.Mode != vmBalloonModeOff || svc.VM.Balloon.MinBytes != 4<<30 {
		t.Fatalf("Balloon = %#v, want legacy VM to remain fully reserved", svc.VM.Balloon)
	}
	raw, err := os.ReadFile(filepath.Join(serviceRunDirForRoot(root), "firecracker.json"))
	if err != nil {
		t.Fatalf("read Firecracker config: %v", err)
	}
	if strings.Contains(string(raw), `"balloon"`) {
		t.Fatalf("Firecracker config includes balloon for legacy disk-only update:\n%s", raw)
	}
}

func TestVMCmdSetUpdatesShape(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", root, vmDiskBackendRaw)
	withServiceSetVMRunningCheck(t, func(*Server, string) (bool, error) { return false, nil })
	execer := &ttyExecer{s: server, sn: "devbox", rw: &bytes.Buffer{}}

	if err := execer.vmCmdFunc([]string{"set", "--vcpus=6", "--memory=6g"}); err != nil {
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
	if !containsCommand(networkCommands, vmSetOwnedTapCommand("yvm-d-ea1055-l0")) {
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

func TestVMSetMigratesStoppedVMToISOBehindVerifiedPolicy(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", root, vmDiskBackendRaw)
	withServiceSetVMRunningCheck(t, func(*Server, string) (bool, error) { return false, nil })
	_, err := server.cfg.DB.MutateData(func(data *db.Data) error {
		data.ISOPool = &db.ISOPool{
			Prefix:           netip.MustParsePrefix("172.30.0.0/16"),
			Source:           "test",
			AllocatorVersion: iso.AllocatorVersion,
			PolicyVersion:    iso.PolicyVersion,
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var events []string
	withServiceSetVMISOBoundary(t, func(_ context.Context, got *Server, service string) error {
		allocation, loadErr := got.persistedISOAllocationForService(service)
		if loadErr != nil {
			return loadErr
		}
		if allocation.Kind != string(iso.PayloadVM) || allocation.Link.Bits() != 30 {
			t.Fatalf("allocation at policy gate = %#v", allocation)
		}
		events = append(events, "verify-policy")
		return nil
	})
	withServiceSetVMNetworkRunner(t, func(command []string) error {
		switch {
		case reflect.DeepEqual(command, []string{"ip", "link", "del", "yvm-d-ea1055-b0"}):
			events = append(events, "cleanup-old")
		case len(command) >= 4 && reflect.DeepEqual(command[:4], []string{"ip", "tuntap", "add", "yi-2ff8ddd61f"}):
			events = append(events, "create-tap")
		}
		return nil
	})
	withServiceSetVMNetworkVerifier(t, func(_ context.Context, plan vmNetworkPlan) error {
		if !plan.hasNetworkMode("iso") {
			t.Fatalf("verified plan = %#v, want ISO", plan)
		}
		events = append(events, "verify-tap")
		return nil
	})
	withServiceSetVMMetadataInjector(t, func(_ context.Context, _ string, metadata vmMetadataConfig) error {
		if len(metadata.Networks) != 1 || metadata.Networks[0].Mode != "iso" {
			t.Fatalf("metadata networks = %#v, want ISO", metadata.Networks)
		}
		events = append(events, "metadata")
		return nil
	})

	if err := server.updateVMServiceSettings(context.Background(), "devbox", cli.VMSetFlags{
		Net:           "iso",
		NetworkChange: true,
	}); err != nil {
		t.Fatalf("updateVMServiceSettings: %v", err)
	}

	wantEvents := []string{"verify-policy", "cleanup-old", "create-tap", "verify-tap", "metadata"}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("events = %#v, want %#v", events, wantEvents)
	}
	service := getTestService(t, server, "devbox")
	if service.ISO == nil || service.ISO.State != string(iso.StateStopped) {
		t.Fatalf("ISO allocation = %#v, want stopped until the VM actually starts", service.ISO)
	}
	if service.SvcNetwork != nil {
		t.Fatalf("SvcNetwork = %#v, want nil", service.SvcNetwork)
	}
	if len(service.VM.Networks) != 1 || service.VM.Networks[0].Mode != "iso" || service.VM.Networks[0].IP != service.ISO.PeerIP {
		t.Fatalf("VM networks = %#v, want ISO guest %s", service.VM.Networks, service.ISO.PeerIP)
	}
}

func TestVMSetTransitionsAwayFromISOOnlyAfterVerifiedTapCleanup(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", root, vmDiskBackendRaw)
	withServiceSetVMRunningCheck(t, func(*Server, string) (bool, error) { return false, nil })
	allocation := seedISOForVMSetTransition(t, server, "devbox")

	var events []string
	withServiceSetVMNetworkRunner(t, func(command []string) error {
		switch {
		case reflect.DeepEqual(command, []string{"ip", "link", "del", allocation.Interface}):
			events = append(events, "cleanup-iso")
		case len(command) >= 4 && command[0] == "ip" && command[1] == "tuntap" && command[2] == "add" && strings.HasPrefix(command[3], "yvm-"):
			events = append(events, "create-replacement")
		}
		return nil
	})
	withServiceSetVMISOAbsenceVerifier(t, func(context.Context, vmNetworkPlan) error {
		events = append(events, "verify-iso-absent")
		return nil
	})
	withServiceSetVMMetadataInjector(t, func(_ context.Context, _ string, metadata vmMetadataConfig) error {
		if len(metadata.Networks) != 1 || metadata.Networks[0].Mode != "svc" {
			t.Fatalf("metadata networks = %#v, want svc", metadata.Networks)
		}
		events = append(events, "metadata")
		return nil
	})
	withServiceSetVMTransitionPolicy(t, func(_ context.Context, got *Server) error {
		if service := getTestService(t, got, "devbox"); service.ISO != nil {
			t.Fatalf("policy rendered before ISO allocation release: %#v", service.ISO)
		}
		events = append(events, "verify-policy-without-iso")
		return nil
	})

	if err := server.updateVMServiceSettings(context.Background(), "devbox", cli.VMSetFlags{Net: "svc", NetworkChange: true}); err != nil {
		t.Fatalf("updateVMServiceSettings: %v", err)
	}
	want := []string{"cleanup-iso", "verify-iso-absent", "metadata", "verify-policy-without-iso", "create-replacement"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
	service := getTestService(t, server, "devbox")
	if service.ISO != nil || service.SvcNetwork == nil || len(service.VM.Networks) != 1 || service.VM.Networks[0].Mode != "svc" {
		t.Fatalf("replacement service = %#v", service)
	}
}

func TestVMSetTransitionAwayFromISORetainsTombstoneWhenTapAbsenceIsUnproven(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", root, vmDiskBackendRaw)
	withServiceSetVMRunningCheck(t, func(*Server, string) (bool, error) { return false, nil })
	seedISOForVMSetTransition(t, server, "devbox")
	withServiceSetVMNetworkRunner(t, func([]string) error { return nil })
	withServiceSetVMISOAbsenceVerifier(t, func(context.Context, vmNetworkPlan) error {
		return errors.New("tap still exists")
	})

	err := server.updateVMServiceSettings(context.Background(), "devbox", cli.VMSetFlags{Net: "svc", NetworkChange: true})
	if err == nil || !strings.Contains(err.Error(), "tap still exists") {
		t.Fatalf("updateVMServiceSettings error = %v", err)
	}
	service := getTestService(t, server, "devbox")
	if service.ISO == nil || service.ISO.State != string(iso.StateTombstoned) || len(service.VM.Networks) != 1 || service.VM.Networks[0].Mode != "iso" {
		t.Fatalf("failed transition service = %#v", service)
	}
}

func seedISOForVMSetTransition(t *testing.T, server *Server, name string) *db.ISOAllocation {
	t.Helper()
	layout, err := iso.NewLayout(netip.MustParsePrefix("172.30.0.0/16"))
	if err != nil {
		t.Fatal(err)
	}
	link, err := layout.Link(0)
	if err != nil {
		t.Fatal(err)
	}
	allocation := newDBISOAllocation(name, isoReservationRequest{Kind: iso.PayloadVM, Modes: []string{"iso"}}, link)
	allocation.State = string(iso.StateReady)
	_, err = server.cfg.DB.MutateData(func(data *db.Data) error {
		data.ISOPool = &db.ISOPool{
			Prefix: link.Masked(), Source: "test", AllocatorVersion: iso.AllocatorVersion, PolicyVersion: iso.PolicyVersion,
		}
		data.ISOPool.Prefix = netip.MustParsePrefix("172.30.0.0/16")
		service := data.Services[name]
		service.ISO = cloneVMISOAllocation(allocation)
		service.SvcNetwork = nil
		service.VM.Networks = newVMNetworkPlan(name, []string{"iso"}, vmNetworkInputs{
			ISOHostIP: allocation.HostIP, ISOGuestIP: allocation.PeerIP, ISOLink: allocation.Link, ISOTap: allocation.Interface,
		}).DBNetworks()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return allocation
}

func TestVMSetRestoresOldNetworkWhenMetadataRewriteFails(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", root, vmDiskBackendRaw)
	withServiceSetVMRunningCheck(t, func(*Server, string) (bool, error) { return false, nil })
	var networkCommands [][]string
	withServiceSetVMNetworkRunner(t, func(command []string) error {
		networkCommands = append(networkCommands, append([]string(nil), command...))
		return nil
	})
	var injected []vmMetadataConfig
	withServiceSetVMMetadataInjector(t, func(_ context.Context, _ string, metadata vmMetadataConfig) error {
		injected = append(injected, metadata)
		if vmMetadataHasNetworkMode(metadata, "lan") {
			return errors.New("metadata rewrite failed")
		}
		return nil
	})

	err := server.updateVMServiceSettings(context.Background(), "devbox", cli.VMSetFlags{
		Net:           "lan",
		NetworkChange: true,
		MacvlanParent: "vmbr0",
		MacvlanMac:    "02:fc:00:00:00:44",
	})
	if err == nil || !strings.Contains(err.Error(), "metadata rewrite failed") {
		t.Fatalf("updateVMServiceSettings error = %v, want metadata failure", err)
	}
	assertVMSetNetworkRolledBack(t, server, root, networkCommands, true)
	assertVMMetadataInjectionModes(t, injected, []string{"lan", "svc"})
}

func TestVMSetRestoresOldNetworkWhenFirecrackerConfigWriteFails(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", root, vmDiskBackendRaw)
	withServiceSetVMRunningCheck(t, func(*Server, string) (bool, error) { return false, nil })
	var networkCommands [][]string
	withServiceSetVMNetworkRunner(t, func(command []string) error {
		networkCommands = append(networkCommands, append([]string(nil), command...))
		return nil
	})
	var injected []vmMetadataConfig
	withServiceSetVMMetadataInjector(t, func(_ context.Context, _ string, metadata vmMetadataConfig) error {
		injected = append(injected, metadata)
		if vmMetadataHasNetworkMode(metadata, "lan") {
			firecrackerPath := filepath.Join(serviceRunDirForRoot(root), "firecracker.json")
			if err := os.Remove(firecrackerPath); err != nil {
				return err
			}
			return os.Mkdir(firecrackerPath, 0o755)
		}
		return nil
	})

	err := server.updateVMServiceSettings(context.Background(), "devbox", cli.VMSetFlags{
		Net:           "lan",
		NetworkChange: true,
		MacvlanParent: "vmbr0",
		MacvlanMac:    "02:fc:00:00:00:44",
	})
	if err == nil || !strings.Contains(err.Error(), "firecracker.json") {
		t.Fatalf("updateVMServiceSettings error = %v, want firecracker config write failure", err)
	}
	assertVMSetNetworkRolledBack(t, server, root, networkCommands, true)
	assertVMMetadataInjectionModes(t, injected, []string{"lan", "svc"})
}

func TestVMSetRestoresOldNetworkWhenDBCommitFails(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", root, vmDiskBackendRaw)
	withServiceSetVMRunningCheck(t, func(*Server, string) (bool, error) { return false, nil })
	var networkCommands [][]string
	withServiceSetVMNetworkRunner(t, func(command []string) error {
		networkCommands = append(networkCommands, append([]string(nil), command...))
		return nil
	})
	var injected []vmMetadataConfig
	withServiceSetVMMetadataInjector(t, func(_ context.Context, _ string, metadata vmMetadataConfig) error {
		injected = append(injected, metadata)
		if err := os.Chmod(server.cfg.RootDir, 0o555); err != nil {
			return err
		}
		return nil
	})
	t.Cleanup(func() {
		if err := os.Chmod(server.cfg.RootDir, 0o755); err != nil {
			t.Logf("restore DB dir permissions: %v", err)
		}
	})

	err := server.updateVMServiceSettings(context.Background(), "devbox", cli.VMSetFlags{
		Net:           "lan",
		NetworkChange: true,
		MacvlanParent: "vmbr0",
		MacvlanMac:    "02:fc:00:00:00:44",
	})
	if err == nil || !strings.Contains(err.Error(), "failed to save data") {
		t.Fatalf("updateVMServiceSettings error = %v, want DB commit failure", err)
	}
	assertVMSetNetworkRolledBack(t, server, root, networkCommands, true)
	assertVMMetadataInjectionModes(t, injected, []string{"lan", "svc"})
}

func TestVMSetCleansPartialNewNetworkWhenSetupFails(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", root, vmDiskBackendRaw)
	withServiceSetVMRunningCheck(t, func(*Server, string) (bool, error) { return false, nil })
	setupFailure := errors.New("tap attach failed")
	var networkCommands [][]string
	newTap := "yvm-d-ea1055-l0"
	withServiceSetVMNetworkRunner(t, func(command []string) error {
		networkCommands = append(networkCommands, append([]string(nil), command...))
		if containsCommand([][]string{command}, []string{"ip", "link", "set", newTap, "master", "vmbr0"}) {
			return setupFailure
		}
		return nil
	})
	withServiceSetVMMetadataInjector(t, func(context.Context, string, vmMetadataConfig) error {
		t.Fatal("metadata should not be rewritten after network setup fails")
		return nil
	})

	err := server.updateVMServiceSettings(context.Background(), "devbox", cli.VMSetFlags{
		Net:           "lan",
		NetworkChange: true,
		MacvlanParent: "vmbr0",
		MacvlanMac:    "02:fc:00:00:00:44",
	})
	if !errors.Is(err, setupFailure) {
		t.Fatalf("updateVMServiceSettings error = %v, want setup failure", err)
	}
	assertVMSetNetworkRolledBack(t, server, root, networkCommands, false)
}

func TestVMSetRestoresOldNetworkWhenOldCleanupFails(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", root, vmDiskBackendRaw)
	withServiceSetVMRunningCheck(t, func(*Server, string) (bool, error) { return false, nil })
	cleanupFailure := errors.New("old tap cleanup failed")
	var networkCommands [][]string
	oldTap := "yvm-d-ea1055-s0"
	newTap := "yvm-d-ea1055-l0"
	failingCleanup := []string{"ip", "link", "del", oldTap}
	withServiceSetVMNetworkRunner(t, func(command []string) error {
		networkCommands = append(networkCommands, append([]string(nil), command...))
		if containsCommand([][]string{command}, failingCleanup) {
			return cleanupFailure
		}
		return nil
	})
	withServiceSetVMMetadataInjector(t, func(context.Context, string, vmMetadataConfig) error {
		t.Fatal("metadata should not be rewritten after old network cleanup fails")
		return nil
	})

	err := server.updateVMServiceSettings(context.Background(), "devbox", cli.VMSetFlags{
		Net:           "lan",
		NetworkChange: true,
		MacvlanParent: "vmbr0",
		MacvlanMac:    "02:fc:00:00:00:44",
	})
	if !errors.Is(err, cleanupFailure) {
		t.Fatalf("updateVMServiceSettings error = %v, want cleanup failure", err)
	}
	svc := getTestService(t, server, "devbox")
	if len(svc.VM.Networks) != 1 || svc.VM.Networks[0].Mode != "svc" {
		t.Fatalf("DB networks = %#v, want original svc network", svc.VM.Networks)
	}
	if containsCommand(networkCommands, vmSetOwnedTapCommand(newTap)) {
		t.Fatalf("new network setup should not run after old cleanup failure: %#v", networkCommands)
	}
	assertCommandSequence(t, networkCommands,
		failingCleanup,
		vmSetOwnedTapCommand(oldTap),
	)
}

func assertVMSetNetworkRolledBack(t *testing.T, server *Server, root string, networkCommands [][]string, metadataTouched bool) {
	t.Helper()
	svc := getTestService(t, server, "devbox")
	if len(svc.VM.Networks) != 1 || svc.VM.Networks[0].Mode != "svc" {
		t.Fatalf("DB networks = %#v, want original svc network", svc.VM.Networks)
	}
	if svc.SvcNetwork == nil || svc.SvcNetwork.IPv4 != netip.MustParseAddr("192.168.100.12") {
		t.Fatalf("svc network = %#v, want original service IP", svc.SvcNetwork)
	}
	oldTap := "yvm-d-ea1055-s0"
	newTap := "yvm-d-ea1055-l0"
	if !containsCommand(networkCommands, vmSetOwnedTapCommand(newTap)) {
		t.Fatalf("new network setup missing: %#v", networkCommands)
	}
	if !containsCommand(networkCommands, []string{"ip", "link", "del", newTap}) {
		t.Fatalf("new network rollback cleanup missing: %#v", networkCommands)
	}
	if !containsCommand(networkCommands, vmSetOwnedTapCommand(oldTap)) {
		t.Fatalf("old network restore missing: %#v", networkCommands)
	}
	assertCommandSequence(t, networkCommands,
		vmSetOwnedTapCommand(newTap),
		[]string{"ip", "link", "del", newTap},
		vmSetOwnedTapCommand(oldTap),
	)
	assertFileContains(t, filepath.Join(serviceRunDirForRoot(root), "firecracker.json"), `"host_dev_name": "yvm-d-ea1055-s0"`)
	if metadataTouched {
		assertFileContains(t, filepath.Join(root, "metadata", "network.yaml"), "192.168.100.12")
	}
}

func assertCommandSequence(t *testing.T, commands [][]string, wants ...[]string) {
	t.Helper()
	offset := 0
	for _, want := range wants {
		found := false
		for i := offset; i < len(commands); i++ {
			if reflect.DeepEqual(commands[i], want) {
				offset = i + 1
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("command sequence missing %v after index %d in %#v", want, offset, commands)
		}
	}
}

func assertVMMetadataInjectionModes(t *testing.T, injected []vmMetadataConfig, want []string) {
	t.Helper()
	var got []string
	for _, metadata := range injected {
		if len(metadata.Networks) == 0 {
			got = append(got, "")
			continue
		}
		got = append(got, metadata.Networks[0].Mode)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("metadata injection modes = %#v, want %#v", got, want)
	}
}

func vmMetadataHasNetworkMode(metadata vmMetadataConfig, mode string) bool {
	for _, network := range metadata.Networks {
		if network.Mode == mode {
			return true
		}
	}
	return false
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
		svc.VM.Image.MetadataDriver = "nixos"
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

func TestVMMetadataDriverForExistingVMFallsBackToUbuntuWithoutStoredMetadata(t *testing.T) {
	vm := db.VMConfig{
		Image: db.VMImageConfig{
			Payload: testNixOSVMPayload,
			Version: testNixOSVMImageVersion,
		},
	}
	if got := vmMetadataDriverForExistingVM(vm); got != "ubuntu" {
		t.Fatalf("metadata driver = %q, want ubuntu", got)
	}
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
	withServiceSetVMRuntimeIdentity(t, func() (vmRuntimeIdentity, error) {
		return vmRuntimeIdentity{UID: 812, GID: 813}, nil
	})
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
	imageDir := filepath.Join(root, "images")
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		t.Fatalf("mkdir images: %v", err)
	}
	kernelPath := filepath.Join(imageDir, "kernel")
	initrdPath := filepath.Join(imageDir, "initrd.img")
	rootFSPath := filepath.Join(imageDir, "rootfs.ext4")
	for path, mode := range map[string]os.FileMode{
		kernelPath:                             0o644,
		initrdPath:                             0o644,
		rootFSPath:                             0o644,
		filepath.Join(imageDir, "firecracker"): 0o755,
		filepath.Join(imageDir, "jailer"):      0o755,
	} {
		if err := os.WriteFile(path, []byte(path), mode); err != nil {
			t.Fatalf("write image artifact: %v", err)
		}
	}
	diskPath := filepath.Join(serviceDataDirForRoot(root), "rootfs.raw")
	diskDBPath := diskPath
	if backend == vmDiskBackendZVOL {
		diskDBPath = "/dev/zvol/flash/yeet/vms/devbox/vm/d-abc/root"
	} else if err := os.WriteFile(diskPath, []byte("disk"), 0o600); err != nil {
		t.Fatalf("write raw disk: %v", err)
	}
	network := newVMNetworkPlan(name, []string{"svc"}, vmNetworkInputs{ServiceIP: "192.168.100.12"})
	firecrackerPath := filepath.Join(serviceRunDirForRoot(root), "firecracker.json")
	firecracker, err := renderFirecrackerConfig(firecrackerConfig{
		BootSource: firecrackerBootSource{
			KernelImagePath: kernelPath,
			InitrdPath:      initrdPath,
			BootArgs:        "console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda rw init=/usr/local/lib/yeet-vm/yeet-init ip=192.168.100.12::192.168.100.1:255.255.255.0:devbox:eth0:none yeet.hostname=devbox yeet.iface=eth0",
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
					Kernel:  kernelPath,
					RootFS:  rootFSPath,
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

func withServiceSetVMRuntimeIdentity(t *testing.T, fn func() (vmRuntimeIdentity, error)) {
	t.Helper()
	old := vmServiceSetEnsureRuntimeIdentity
	vmServiceSetEnsureRuntimeIdentity = fn
	t.Cleanup(func() { vmServiceSetEnsureRuntimeIdentity = old })
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

func withServiceSetVMISOBoundary(t *testing.T, fn func(context.Context, *Server, string) error) {
	t.Helper()
	old := ensureVMISOBoundaryForSettings
	ensureVMISOBoundaryForSettings = fn
	t.Cleanup(func() { ensureVMISOBoundaryForSettings = old })
}

func withServiceSetVMNetworkVerifier(t *testing.T, fn func(context.Context, vmNetworkPlan) error) {
	t.Helper()
	old := verifyVMNetworkPlanForSettings
	verifyVMNetworkPlanForSettings = fn
	t.Cleanup(func() { verifyVMNetworkPlanForSettings = old })
}

func withServiceSetVMISOAbsenceVerifier(t *testing.T, fn func(context.Context, vmNetworkPlan) error) {
	t.Helper()
	old := verifyVMISONetworkAbsentForSettings
	verifyVMISONetworkAbsentForSettings = fn
	t.Cleanup(func() { verifyVMISONetworkAbsentForSettings = old })
}

func withServiceSetVMTransitionPolicy(t *testing.T, fn func(context.Context, *Server) error) {
	t.Helper()
	old := installVMISOPolicyAfterTransitionForSet
	installVMISOPolicyAfterTransitionForSet = fn
	t.Cleanup(func() { installVMISOPolicyAfterTransitionForSet = old })
}

func containsCommand(commands [][]string, want []string) bool {
	for _, command := range commands {
		if reflect.DeepEqual(command, want) {
			return true
		}
	}
	return false
}

func vmSetOwnedTapCommand(tap string) []string {
	return []string{"ip", "tuntap", "add", tap, "mode", "tap", "user", "812", "group", "813"}
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
