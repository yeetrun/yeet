// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
	"tailscale.com/tailcfg"
)

func TestLabelForIP(t *testing.T) {
	tests := []struct {
		name        string
		entry       ifaceIP
		svcIP       string
		tsIface     string
		macIface    string
		hasNetns    bool
		serviceType db.ServiceType
		want        string
	}{
		{name: "service ip wins", entry: ifaceIP{Interface: "eth0", IP: "10.0.0.5"}, svcIP: "10.0.0.5", serviceType: db.ServiceTypeDockerCompose, want: "service"},
		{name: "configured tailscale interface", entry: ifaceIP{Interface: "ts0", IP: "100.64.0.1"}, tsIface: "ts0", want: "tailscale"},
		{name: "tailscale prefix", entry: ifaceIP{Interface: "tailscale0", IP: "100.64.0.1"}, want: "tailscale"},
		{name: "yeet tailscale prefix", entry: ifaceIP{Interface: "yts-app", IP: "100.64.0.1"}, want: "tailscale"},
		{name: "configured lan interface", entry: ifaceIP{Interface: "mac0", IP: "10.0.0.8"}, macIface: "mac0", want: "lan"},
		{name: "docker prefix", entry: ifaceIP{Interface: "docker0", IP: "172.17.0.1"}, want: "docker"},
		{name: "bridge prefix", entry: ifaceIP{Interface: "br-abcd", IP: "172.18.0.1"}, want: "docker"},
		{name: "docker compose netns fallback", entry: ifaceIP{Interface: "eth0", IP: "10.0.0.8"}, hasNetns: true, serviceType: db.ServiceTypeDockerCompose, want: "docker"},
		{name: "docker compose without netns fallback", entry: ifaceIP{Interface: "eth0", IP: "10.0.0.8"}, serviceType: db.ServiceTypeDockerCompose, want: "host"},
		{name: "netns fallback", entry: ifaceIP{Interface: "eth0", IP: "10.0.0.8"}, hasNetns: true, serviceType: db.ServiceTypeSystemd, want: "netns"},
		{name: "host fallback", entry: ifaceIP{Interface: "eth0", IP: "10.0.0.8"}, serviceType: db.ServiceTypeSystemd, want: "host"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := labelForIP(tt.entry, tt.svcIP, tt.tsIface, tt.macIface, tt.hasNetns, tt.serviceType)
			if got != tt.want {
				t.Fatalf("labelForIP = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestServiceImageInfoFiltersAndSortsServiceImages(t *testing.T) {
	server := newTestServer(t)
	if err := server.cfg.DB.Set(&db.Data{
		Images: map[db.ImageRepoName]*db.ImageRepo{
			"api/app": {
				Refs: map[db.ImageRef]db.ImageManifest{
					"run":    {BlobHash: "sha256:run", ContentType: "application/vnd.oci.image.manifest.v1+json"},
					"staged": {BlobHash: "sha256:staged", ContentType: "application/vnd.oci.image.manifest.v1+json"},
				},
			},
			"api/worker": {
				Refs: map[db.ImageRef]db.ImageManifest{
					"run": {BlobHash: "sha256:worker", ContentType: "application/vnd.oci.image.manifest.v1+json"},
				},
			},
			"other/app": {
				Refs: map[db.ImageRef]db.ImageManifest{
					"run": {BlobHash: "sha256:other", ContentType: "application/vnd.oci.image.manifest.v1+json"},
				},
			},
			"invalid/repo/name": {
				Refs: map[db.ImageRef]db.ImageManifest{
					"run": {BlobHash: "sha256:invalid", ContentType: "application/vnd.oci.image.manifest.v1+json"},
				},
			},
		},
	}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	dv, err := server.getDB()
	if err != nil {
		t.Fatalf("getDB: %v", err)
	}

	got := serviceImageInfo(dv, "api")
	if len(got) != 2 {
		t.Fatalf("serviceImageInfo returned %d images: %#v", len(got), got)
	}
	if got[0].Repo != "api/app" || got[1].Repo != "api/worker" {
		t.Fatalf("repos not filtered and sorted: %#v", got)
	}
	if got[0].Refs["run"].Digest != "sha256:run" || got[0].Refs["staged"].Digest != "sha256:staged" {
		t.Fatalf("refs missing expected digest metadata: %#v", got[0].Refs)
	}
	if serviceImageInfo(nil, "api") != nil {
		t.Fatalf("nil data view should return nil")
	}
	if serviceImageInfo(dv, "") != nil {
		t.Fatalf("empty service should return nil")
	}
}

func TestServiceInfoIncludesSnapshotPolicy(t *testing.T) {
	server := newTestServer(t)
	enabled := false
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		"svc-info": {
			Name:           "svc-info",
			ServiceRoot:    "/tank/apps/svc-info",
			ServiceRootZFS: "tank/apps/svc-info",
			SnapshotPolicy: &db.SnapshotPolicy{Enabled: &enabled, MaxAge: "72h"},
		},
	}}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	resp, err := server.serviceInfo("svc-info")
	if err != nil {
		t.Fatalf("serviceInfo: %v", err)
	}
	if resp.Info.Snapshots == nil || resp.Info.Snapshots.Override == nil || resp.Info.Snapshots.Override.Enabled == nil || *resp.Info.Snapshots.Override.Enabled {
		t.Fatalf("override = %#v", resp.Info.Snapshots)
	}
	if resp.Info.Snapshots.Effective.MaxAge != "72h" {
		t.Fatalf("effective = %#v", resp.Info.Snapshots.Effective)
	}
}

func TestServiceInfoSnapshotDefaultsInherited(t *testing.T) {
	server := newTestServer(t)
	enabled := true
	keep := 7
	required := false
	if err := server.cfg.DB.Set(&db.Data{
		SnapshotDefaults: &db.SnapshotPolicy{
			Enabled:  &enabled,
			KeepLast: &keep,
			MaxAge:   "14d",
			Events:   []string{"run", "docker-update"},
			Required: &required,
		},
		Services: map[string]*db.Service{
			"svc-info": {
				Name:           "svc-info",
				ServiceRoot:    "/tank/apps/svc-info",
				ServiceRootZFS: "tank/apps/svc-info",
			},
		},
	}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	resp, err := server.serviceInfo("svc-info")
	if err != nil {
		t.Fatalf("serviceInfo: %v", err)
	}
	snapshots := resp.Info.Snapshots
	if snapshots == nil {
		t.Fatal("snapshots = nil")
	}
	if snapshots.Override != nil {
		t.Fatalf("override = %#v, want nil", snapshots.Override)
	}
	if !snapshots.Effective.Enabled || snapshots.Effective.KeepLast != 7 || snapshots.Effective.MaxAge != "14d" || snapshots.Effective.Required {
		t.Fatalf("effective = %#v", snapshots.Effective)
	}
	if got := snapshots.Effective.Events; len(got) != 2 || got[0] != "run" || got[1] != "docker-update" {
		t.Fatalf("events = %#v", got)
	}
}

func TestServiceInfoSnapshotServiceOverridePrecedence(t *testing.T) {
	server := newTestServer(t)
	serverEnabled := true
	keep := 8
	required := true
	serviceEnabled := false
	if err := server.cfg.DB.Set(&db.Data{
		SnapshotDefaults: &db.SnapshotPolicy{
			Enabled:  &serverEnabled,
			KeepLast: &keep,
			MaxAge:   "14d",
			Events:   []string{"run", "docker-update"},
			Required: &required,
		},
		Services: map[string]*db.Service{
			"svc-info": {
				Name:           "svc-info",
				ServiceRoot:    "/tank/apps/svc-info",
				ServiceRootZFS: "tank/apps/svc-info",
				SnapshotPolicy: &db.SnapshotPolicy{Enabled: &serviceEnabled, MaxAge: "72h"},
			},
		},
	}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	resp, err := server.serviceInfo("svc-info")
	if err != nil {
		t.Fatalf("serviceInfo: %v", err)
	}
	snapshots := resp.Info.Snapshots
	if snapshots == nil || snapshots.Override == nil || snapshots.Override.Enabled == nil || *snapshots.Override.Enabled {
		t.Fatalf("override = %#v", snapshots)
	}
	if snapshots.Override.KeepLast != nil || snapshots.Override.Required != nil || snapshots.Override.MaxAge != "72h" {
		t.Fatalf("override = %#v", snapshots.Override)
	}
	if snapshots.Effective.Enabled || snapshots.Effective.KeepLast != 8 || snapshots.Effective.MaxAge != "72h" || !snapshots.Effective.Required {
		t.Fatalf("effective = %#v", snapshots.Effective)
	}
	if got := snapshots.Effective.Events; len(got) != 2 || got[0] != "run" || got[1] != "docker-update" {
		t.Fatalf("events = %#v", got)
	}
}

func TestServiceInfoIncludesVMConfig(t *testing.T) {
	server := newTestServer(t)
	serviceRoot := t.TempDir()
	if err := markVMJailerReady(serviceRoot); err != nil {
		t.Fatal(err)
	}
	oldListIPv4Addrs := listIPv4AddrsFn
	defer func() { listIPv4AddrsFn = oldListIPv4Addrs }()
	listIPv4AddrsFn = func(args []string) ([]ifaceIP, error) {
		return nil, nil
	}
	oldServiceVMStatus := serviceVMStatusFn
	defer func() { serviceVMStatusFn = oldServiceVMStatus }()
	serviceVMStatusFn = func(sn string) (svc.Status, error) {
		if sn != "devbox" {
			t.Fatalf("VM status service = %q, want devbox", sn)
		}
		return svc.StatusRunning, nil
	}

	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		"devbox": {
			Name:        "devbox",
			ServiceType: db.ServiceTypeVM,
			ServiceRoot: serviceRoot,
			VM: &db.VMConfig{
				Runtime:     "firecracker",
				Image:       db.VMImageConfig{Payload: testUbuntuVMPayload, Version: "ubuntu-26.04-amd64-v1"},
				CPUs:        4,
				MemoryBytes: 4 << 30,
				Balloon: db.VMBalloonConfig{
					Mode:            "auto",
					MinBytes:        1 << 30,
					LastTargetBytes: 2 << 30,
				},
				Disk: db.VMDiskConfig{Backend: "zvol", Bytes: 128 << 30, Path: "flash/yeet/vms/devbox/root"},
				Networks: []db.VMNetworkConfig{{
					Mode:      "svc",
					Interface: "tap-devbox",
					IP:        netip.MustParseAddr("10.0.0.42"),
					MAC:       "02:00:00:00:00:42",
				}},
				SSH:        db.VMSSHConfig{User: "ubuntu"},
				Console:    db.VMConsoleConfig{SocketPath: "/run/yeet/devbox/serial.sock"},
				SetupState: "ready",
			},
		},
	}}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}

	resp, err := server.serviceInfo("devbox")
	if err != nil {
		t.Fatalf("serviceInfo: %v", err)
	}
	vm := resp.Info.VM
	if vm == nil {
		t.Fatal("VM info = nil")
	}
	if resp.Info.DataType != "vm" {
		t.Fatalf("data type = %q, want vm", resp.Info.DataType)
	}
	if resp.Info.Status.Error != "" {
		t.Fatalf("status error = %q, want empty", resp.Info.Status.Error)
	}
	if len(resp.Info.Status.Components) != 1 || resp.Info.Status.Components[0].Name != "devbox" || resp.Info.Status.Components[0].Status != "running" {
		t.Fatalf("status components = %#v", resp.Info.Status.Components)
	}
	if vm.Runtime != "firecracker" || vm.Image != testUbuntuVMPayload || vm.ImageVersion != "ubuntu-26.04-amd64-v1" {
		t.Fatalf("VM image/runtime = %#v", vm)
	}
	if vm.VMMIsolation != string(vmJailerReady) {
		t.Fatalf("VM VMM isolation = %q, want %q", vm.VMMIsolation, vmJailerReady)
	}
	if vm.CPUs != 4 || vm.MemoryBytes != 4<<30 || vm.DiskBytes != 128<<30 || vm.DiskBackend != "zvol" || vm.DiskPath != "flash/yeet/vms/devbox/root" {
		t.Fatalf("VM resources/disk = %#v", vm)
	}
	if vm.Balloon.Mode != "auto" || vm.Balloon.MinBytes != 1<<30 || vm.Balloon.MinMemory != "1 GB" || vm.Balloon.LastTarget != 2<<30 {
		t.Fatalf("VM balloon = %#v, want persisted auto config with 1 GB floor", vm.Balloon)
	}
	if vm.SSH == nil || vm.SSH.User != "ubuntu" || vm.SSH.Host != "10.0.0.42" {
		t.Fatalf("VM SSH = %#v", vm.SSH)
	}
	if vm.Console == nil || !vm.Console.Available || vm.Console.SocketPath != "/run/yeet/devbox/serial.sock" {
		t.Fatalf("VM console = %#v", vm.Console)
	}
	if len(vm.Networks) != 1 || vm.Networks[0].Mode != "svc" || vm.Networks[0].Interface != "tap-devbox" || vm.Networks[0].IP != "10.0.0.42" || vm.Networks[0].Source != "config" || vm.Networks[0].MAC != "02:00:00:00:00:42" {
		t.Fatalf("VM networks = %#v", vm.Networks)
	}
	if resp.Info.Network.SvcIP != "10.0.0.42" {
		t.Fatalf("generic svc IP = %q, want 10.0.0.42", resp.Info.Network.SvcIP)
	}
	if len(resp.Info.Network.IPs) != 1 || resp.Info.Network.IPs[0].Label != "service" || resp.Info.Network.IPs[0].IP != "10.0.0.42" || resp.Info.Network.IPs[0].Interface != "tap-devbox" || resp.Info.Network.IPs[0].Source != "config" {
		t.Fatalf("generic network IPs = %#v", resp.Info.Network.IPs)
	}
	if vm.SetupState != "ready" {
		t.Fatalf("VM setup state = %q", vm.SetupState)
	}
	if err := os.Remove(vmJailerReadinessMarkerPath(serviceRoot)); err != nil {
		t.Fatal(err)
	}
	pendingResp, err := server.serviceInfo("devbox")
	if err != nil {
		t.Fatal(err)
	}
	if got := pendingResp.Info.VM.VMMIsolation; got != string(vmJailerPendingRestart) {
		t.Fatalf("pending VMM isolation = %q", got)
	}
}

func TestTailscaleHasValues(t *testing.T) {
	tests := []struct {
		name string
		in   *catchrpc.ServiceTailscale
		want bool
	}{
		{name: "nil", in: nil},
		{name: "empty", in: &catchrpc.ServiceTailscale{}},
		{name: "interface", in: &catchrpc.ServiceTailscale{Interface: "ts0"}, want: true},
		{name: "version", in: &catchrpc.ServiceTailscale{Version: "1.2.3"}, want: true},
		{name: "exit node", in: &catchrpc.ServiceTailscale{ExitNode: "100.64.0.1"}, want: true},
		{name: "stable id", in: &catchrpc.ServiceTailscale{StableID: "stable"}, want: true},
		{name: "tags", in: &catchrpc.ServiceTailscale{Tags: []string{"tag:app"}}, want: true},
		{name: "blank strings", in: &catchrpc.ServiceTailscale{Interface: " ", Version: "\t"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tailscaleHasValues(tt.in); got != tt.want {
				t.Fatalf("tailscaleHasValues = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestServiceIPListLabelsInterfacesAndAddsMissingServiceIP(t *testing.T) {
	server := newTestServer(t)
	if _, _, err := server.cfg.DB.MutateService("svc-info", func(_ *db.Data, s *db.Service) error {
		s.ServiceType = db.ServiceTypeSystemd
		s.Generation = 2
		s.SvcNetwork = &db.SvcNetwork{IPv4: netip.MustParseAddr("10.0.0.99")}
		s.Macvlan = &db.MacvlanNetwork{Interface: "mac0"}
		s.TSNet = &db.TailscaleNetwork{Interface: "ts0"}
		s.Artifacts = db.ArtifactStore{
			db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(2): "/tmp/netns.service"}},
		}
		return nil
	}); err != nil {
		t.Fatalf("mutate service: %v", err)
	}
	dv, err := server.getDB()
	if err != nil {
		t.Fatalf("get db: %v", err)
	}
	sv, ok := dv.Services().GetOk("svc-info")
	if !ok {
		t.Fatal("missing service")
	}

	oldListIPv4Addrs := listIPv4AddrsFn
	defer func() { listIPv4AddrsFn = oldListIPv4Addrs }()
	var gotArgs []string
	listIPv4AddrsFn = func(args []string) ([]ifaceIP, error) {
		gotArgs = append([]string(nil), args...)
		return []ifaceIP{
			{Interface: "ts0", IP: "100.64.0.10"},
			{Interface: "mac0", IP: "10.0.0.8"},
			{Interface: "eth0", IP: "10.0.0.9"},
		}, nil
	}

	ips, err := server.serviceIPList("svc-info", sv)
	if err != nil {
		t.Fatalf("serviceIPList: %v", err)
	}
	wantArgs := []string{"netns", "exec", "yeet-svc-info-ns", "ip", "-o", "-4", "addr", "list"}
	if len(gotArgs) != len(wantArgs) {
		t.Fatalf("args = %#v, want %#v", gotArgs, wantArgs)
	}
	for i := range wantArgs {
		if gotArgs[i] != wantArgs[i] {
			t.Fatalf("args = %#v, want %#v", gotArgs, wantArgs)
		}
	}
	want := map[string]string{
		"100.64.0.10": "tailscale",
		"10.0.0.8":    "lan",
		"10.0.0.99":   "service",
	}
	if len(ips) != len(want) {
		t.Fatalf("ips = %#v, want %d entries", ips, len(want))
	}
	for _, ip := range ips {
		if want[ip.IP] != ip.Label {
			t.Fatalf("ip %s label = %q, want %q (all=%#v)", ip.IP, ip.Label, want[ip.IP], ips)
		}
	}
}

func TestServiceInfoSeparatesEndpointAndRuntimeIPs(t *testing.T) {
	server := newTestServer(t)
	if _, _, err := server.cfg.DB.MutateService("jellyfin", func(_ *db.Data, s *db.Service) error {
		s.ServiceType = db.ServiceTypeDockerCompose
		s.Generation = 3
		s.LatestGeneration = 3
		s.Macvlan = &db.MacvlanNetwork{Interface: "ymv-jellyfin"}
		s.TSNet = &db.TailscaleNetwork{Interface: "yts-jellyfin"}
		s.Artifacts = db.ArtifactStore{
			db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(3): "/tmp/netns.service"}},
		}
		return nil
	}); err != nil {
		t.Fatalf("mutate service: %v", err)
	}

	oldListIPv4Addrs := listIPv4AddrsFn
	defer func() { listIPv4AddrsFn = oldListIPv4Addrs }()
	listIPv4AddrsFn = func([]string) ([]ifaceIP, error) {
		return []ifaceIP{
			{Interface: "br0", IP: "192.168.48.1"},
			{Interface: "yts-jellyfin", IP: "100.116.205.120"},
			{Interface: "ymv-jellyfin", IP: "10.0.4.171"},
		}, nil
	}

	resp, err := server.serviceInfo("jellyfin")
	if err != nil {
		t.Fatalf("serviceInfo: %v", err)
	}
	gotEndpoints := serviceIPsByLabel(resp.Info.Network.IPs)
	wantEndpoints := map[string]string{
		"tailscale": "100.116.205.120",
		"lan":       "10.0.4.171",
	}
	if !mapsEqual(gotEndpoints, wantEndpoints) {
		t.Fatalf("endpoint IPs = %#v, want %#v (all=%#v)", gotEndpoints, wantEndpoints, resp.Info.Network.IPs)
	}
	gotRuntime := serviceIPsByLabel(resp.Info.Network.RuntimeIPs)
	wantRuntime := map[string]string{"docker": "192.168.48.1"}
	if !mapsEqual(gotRuntime, wantRuntime) {
		t.Fatalf("runtime IPs = %#v, want %#v (all=%#v)", gotRuntime, wantRuntime, resp.Info.Network.RuntimeIPs)
	}
}

func TestServiceIPListReturnsOnlyRelevantEndpoints(t *testing.T) {
	server := newTestServer(t)
	if _, _, err := server.cfg.DB.MutateService("jellyfin", func(_ *db.Data, s *db.Service) error {
		s.ServiceType = db.ServiceTypeDockerCompose
		s.Generation = 3
		s.LatestGeneration = 3
		s.SvcNetwork = &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.12")}
		s.Macvlan = &db.MacvlanNetwork{Interface: "ymv-jellyfin"}
		s.TSNet = &db.TailscaleNetwork{Interface: "yts-jellyfin"}
		s.Artifacts = db.ArtifactStore{
			db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(3): "/tmp/netns.service"}},
		}
		return nil
	}); err != nil {
		t.Fatalf("mutate service: %v", err)
	}
	dv, err := server.getDB()
	if err != nil {
		t.Fatalf("get db: %v", err)
	}
	sv, ok := dv.Services().GetOk("jellyfin")
	if !ok {
		t.Fatal("missing service")
	}

	oldListIPv4Addrs := listIPv4AddrsFn
	defer func() { listIPv4AddrsFn = oldListIPv4Addrs }()
	listIPv4AddrsFn = func([]string) ([]ifaceIP, error) {
		return []ifaceIP{
			{Interface: "br0", IP: "192.168.48.1"},
			{Interface: "yts-jellyfin", IP: "100.116.205.120"},
			{Interface: "ymv-jellyfin", IP: "10.0.4.171"},
		}, nil
	}

	ips, err := server.serviceIPList("jellyfin", sv)
	if err != nil {
		t.Fatalf("serviceIPList: %v", err)
	}
	got := make([]string, 0, len(ips))
	for _, ip := range ips {
		got = append(got, ip.IP)
	}
	want := []string{"10.0.4.171", "100.116.205.120", "192.168.100.12"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("serviceIPList IPs = %#v, want %#v (all=%#v)", got, want, ips)
	}
}

func serviceIPsByLabel(ips []catchrpc.ServiceIP) map[string]string {
	out := map[string]string{}
	for _, ip := range ips {
		out[ip.Label] = ip.IP
	}
	return out
}

func mapsEqual(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, leftValue := range left {
		if right[key] != leftValue {
			return false
		}
	}
	return true
}

func TestServiceInfoReturnsNotFoundResponse(t *testing.T) {
	resp, err := newTestServer(t).serviceInfo("missing")
	if err != nil {
		t.Fatalf("serviceInfo: %v", err)
	}
	if resp.Found || resp.Message != "service not found" {
		t.Fatalf("response = %#v", resp)
	}
}

func TestServiceInfoPathsIncludeRootIdentity(t *testing.T) {
	server := newTestServer(t)
	customRoot := filepath.Join(t.TempDir(), "custom-info")
	zfsRoot := filepath.Join(t.TempDir(), "zfs-info")
	if err := server.cfg.DB.Set(&db.Data{
		Services: map[string]*db.Service{
			"fs-info": {
				Name:        "fs-info",
				ServiceType: db.ServiceTypeSystemd,
				ServiceRoot: customRoot,
			},
			"zfs-info": {
				Name:           "zfs-info",
				ServiceType:    db.ServiceTypeSystemd,
				ServiceRoot:    zfsRoot,
				ServiceRootZFS: "tank/apps/zfs-info",
			},
			"default-info": {
				Name:        "default-info",
				ServiceType: db.ServiceTypeSystemd,
			},
		},
	}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}

	fsResp, err := server.serviceInfo("fs-info")
	if err != nil {
		t.Fatalf("serviceInfo fs-info: %v", err)
	}
	if fsResp.Info.Paths.Root != customRoot || fsResp.Info.Paths.EffectiveRoot != customRoot {
		t.Fatalf("filesystem effective roots = %#v, want %q", fsResp.Info.Paths, customRoot)
	}
	if fsResp.Info.Paths.ServiceRoot != customRoot {
		t.Fatalf("filesystem ServiceRoot = %q, want %q", fsResp.Info.Paths.ServiceRoot, customRoot)
	}
	if fsResp.Info.Paths.ServiceRootZFS != "" {
		t.Fatalf("filesystem ServiceRootZFS = %q, want empty", fsResp.Info.Paths.ServiceRootZFS)
	}

	zfsResp, err := server.serviceInfo("zfs-info")
	if err != nil {
		t.Fatalf("serviceInfo zfs-info: %v", err)
	}
	if zfsResp.Info.Paths.Root != zfsRoot || zfsResp.Info.Paths.EffectiveRoot != zfsRoot {
		t.Fatalf("zfs effective roots = %#v, want %q", zfsResp.Info.Paths, zfsRoot)
	}
	if zfsResp.Info.Paths.ServiceRoot != zfsRoot {
		t.Fatalf("zfs ServiceRoot = %q, want %q", zfsResp.Info.Paths.ServiceRoot, zfsRoot)
	}
	if zfsResp.Info.Paths.ServiceRootZFS != "tank/apps/zfs-info" {
		t.Fatalf("zfs ServiceRootZFS = %q, want tank/apps/zfs-info", zfsResp.Info.Paths.ServiceRootZFS)
	}

	defaultResp, err := server.serviceInfo("default-info")
	if err != nil {
		t.Fatalf("serviceInfo default-info: %v", err)
	}
	wantDefault := server.defaultServiceRootDir("default-info")
	if defaultResp.Info.Paths.Root != wantDefault || defaultResp.Info.Paths.EffectiveRoot != wantDefault {
		t.Fatalf("default effective roots = %#v, want %q", defaultResp.Info.Paths, wantDefault)
	}
	if defaultResp.Info.Paths.ServiceRoot != "" || defaultResp.Info.Paths.ServiceRootZFS != "" {
		t.Fatalf("default stored roots = %#v, want empty stored root fields", defaultResp.Info.Paths)
	}
}

func TestServicePublishPortInfoUsesDBPublish(t *testing.T) {
	info := servicePublishPortInfo("svc-info", (&db.Service{
		Name:    "svc-info",
		Publish: []string{"80:80", "127.0.0.1:8080:80/udp"},
	}).View())

	if !info.PortsPresent {
		t.Fatalf("PortsPresent = false, want true")
	}
	if len(info.Ports) != 2 {
		t.Fatalf("Ports = %#v, want 2 entries", info.Ports)
	}
	if info.Ports[0].HostPort != 80 || info.Ports[0].ContainerPort != 80 || info.Ports[0].Protocol != "tcp" {
		t.Fatalf("first port = %#v, want 80/tcp", info.Ports[0])
	}
	if info.Ports[1].HostIP != "127.0.0.1" || info.Ports[1].HostPort != 8080 || info.Ports[1].ContainerPort != 80 || info.Ports[1].Protocol != "udp" {
		t.Fatalf("second port = %#v, want host-ip udp mapping", info.Ports[1])
	}
}

func TestServicePublishPortInfoFallsBackToComposeArtifact(t *testing.T) {
	tmp := t.TempDir()
	composePath := filepath.Join(tmp, "compose.yml")
	if err := os.WriteFile(composePath, []byte("services:\n  svc-info:\n    image: nginx\n    ports:\n      - 443:443/tcp\n"), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	info := servicePublishPortInfo("svc-info", (&db.Service{
		Name:             "svc-info",
		Generation:       1,
		LatestGeneration: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{db.Gen(1): composePath}},
		},
	}).View())

	if !info.PortsPresent || len(info.Ports) != 1 {
		t.Fatalf("info = %#v, want fallback compose port", info)
	}
	if info.Ports[0].HostPort != 443 || info.Ports[0].ContainerPort != 443 || info.Ports[0].Protocol != "tcp" {
		t.Fatalf("port = %#v, want 443/tcp", info.Ports[0])
	}
}

func TestServiceHasStagedChanges(t *testing.T) {
	tests := []struct {
		name string
		svc  *db.Service
		want bool
	}{
		{name: "invalid", svc: nil},
		{name: "no artifacts", svc: &db.Service{Name: "svc"}},
		{name: "staged matches latest", svc: &db.Service{Name: "svc", Generation: 2, Artifacts: db.ArtifactStore{
			db.ArtifactBinary: {Refs: map[db.ArtifactRef]string{"staged": "/tmp/bin", "latest": "/tmp/bin", db.Gen(2): "/tmp/bin"}},
		}}},
		{name: "staged differs", svc: &db.Service{Name: "svc", Generation: 2, Artifacts: db.ArtifactStore{
			db.ArtifactBinary: {Refs: map[db.ArtifactRef]string{"staged": "/tmp/new", "latest": "/tmp/old", db.Gen(2): "/tmp/old"}},
		}}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sv db.ServiceView
			if tt.svc != nil {
				sv = tt.svc.View()
			}
			if got := serviceHasStagedChanges(sv); got != tt.want {
				t.Fatalf("serviceHasStagedChanges = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestServiceNetworkInfoIncludesConfiguredNetworks(t *testing.T) {
	stableID := tailcfg.StableNodeID("node-123")
	info := serviceNetworkInfo((&db.Service{
		Name:       "svc",
		SvcNetwork: &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.7")},
		Macvlan: &db.MacvlanNetwork{
			Interface: "mac0",
			Parent:    "eth0",
			Mac:       "02:00:00:00:00:01",
			VLAN:      20,
		},
		TSNet: &db.TailscaleNetwork{
			Interface: "ts0",
			Version:   "1.92.3",
			ExitNode:  "100.64.0.1",
			Tags:      []string{"tag:svc"},
			StableID:  stableID,
		},
	}).View())

	if info.SvcIP != "192.168.100.7" {
		t.Fatalf("SvcIP = %q", info.SvcIP)
	}
	if info.Macvlan == nil || info.Macvlan.Interface != "mac0" || info.Macvlan.VLAN != 20 {
		t.Fatalf("macvlan = %#v", info.Macvlan)
	}
	if info.Tailscale == nil || info.Tailscale.Interface != "ts0" || info.Tailscale.StableID != string(stableID) {
		t.Fatalf("tailscale = %#v", info.Tailscale)
	}
}

func TestServiceIPListReturnsListErrorAndDoesNotAppendSeenServiceIP(t *testing.T) {
	server := newTestServer(t)
	sv := (&db.Service{
		Name:        "svc",
		ServiceType: db.ServiceTypeSystemd,
		SvcNetwork:  &db.SvcNetwork{IPv4: netip.MustParseAddr("10.0.0.5")},
	}).View()

	oldListIPv4Addrs := listIPv4AddrsFn
	defer func() { listIPv4AddrsFn = oldListIPv4Addrs }()
	wantErr := errors.New("ip failed")
	listIPv4AddrsFn = func(args []string) ([]ifaceIP, error) {
		return nil, wantErr
	}
	if _, err := server.serviceIPList("svc", sv); !errors.Is(err, wantErr) {
		t.Fatalf("serviceIPList error = %v, want %v", err, wantErr)
	}

	listIPv4AddrsFn = func(args []string) ([]ifaceIP, error) {
		return []ifaceIP{{Interface: "eth0", IP: "10.0.0.5"}}, nil
	}
	ips, err := server.serviceIPList("svc", sv)
	if err != nil {
		t.Fatalf("serviceIPList: %v", err)
	}
	if len(ips) != 1 || ips[0].Label != "service" || ips[0].IP != "10.0.0.5" {
		t.Fatalf("ips = %#v", ips)
	}
}

func TestCatchServiceIPListRequiresLocalClient(t *testing.T) {
	_, err := newTestServer(t).catchServiceIPList()
	if err == nil || !strings.Contains(err.Error(), "tailscale client unavailable") {
		t.Fatalf("catchServiceIPList error = %v", err)
	}
}

func TestServiceStatusInfoReportsErrorsAndUnknownTypes(t *testing.T) {
	server := newTestServer(t)
	systemd := server.serviceStatusInfo("svc", (&db.Service{Name: "svc", ServiceType: db.ServiceTypeSystemd}).View())
	if systemd.Error == "" {
		t.Fatalf("expected systemd status error")
	}
	docker := server.serviceStatusInfo("svc", (&db.Service{Name: "svc", ServiceType: db.ServiceTypeDockerCompose}).View())
	if docker.Error == "" {
		t.Fatalf("expected docker status error")
	}
	unknown := server.serviceStatusInfo("svc", (&db.Service{Name: "svc", ServiceType: db.ServiceType("other")}).View())
	if unknown.Error == "" || !strings.Contains(unknown.Error, "unknown service type") {
		t.Fatalf("unknown status = %#v", unknown)
	}
}
