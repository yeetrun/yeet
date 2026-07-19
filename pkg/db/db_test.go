// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package db

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDataCloneDeepCopiesTopLevelCollections(t *testing.T) {
	src := &Data{
		DataVersion: CurrentDataVersion,
		Services: map[string]*Service{
			"nil": nil,
			"svc": {
				Name: "svc",
				Artifacts: ArtifactStore{
					ArtifactBinary: {Refs: map[ArtifactRef]string{"latest": "/srv/svc/bin"}},
				},
				TSNet: &TailscaleNetwork{Tags: []string{"tag:prod"}},
			},
		},
		Images: map[ImageRepoName]*ImageRepo{
			"nil": nil,
			"repo": {Refs: map[ImageRef]ImageManifest{
				"latest": {ContentType: "application/vnd.oci.image.manifest.v1+json", BlobHash: "sha256:old"},
			}},
		},
		Volumes: map[string]*Volume{
			"nil":  nil,
			"data": {Name: "data", Path: "/data"},
		},
		DockerNetworks: map[string]*DockerNetwork{
			"nil": nil,
			"net": {
				NetworkID: "network-id",
				Endpoints: map[string]*DockerEndpoint{
					"svc": {EndpointID: "endpoint-id", IPv4: mustPrefix(t, "10.0.0.2/32")},
				},
			},
		},
	}

	clone := src.Clone()
	if !reflect.DeepEqual(clone, src) {
		t.Fatalf("clone differs from source:\nclone=%#v\nsource=%#v", clone, src)
	}
	if clone.Services["nil"] != nil {
		t.Fatal("nil service entry was not preserved")
	}
	if clone.Images["nil"] != nil {
		t.Fatal("nil image entry was not preserved")
	}
	if clone.Volumes["nil"] != nil {
		t.Fatal("nil volume entry was not preserved")
	}
	if clone.DockerNetworks["nil"] != nil {
		t.Fatal("nil docker network entry was not preserved")
	}
	requireDistinctPtr(t, "service", clone.Services["svc"], src.Services["svc"])
	requireDistinctPtr(t, "image repo", clone.Images["repo"], src.Images["repo"])
	requireDistinctPtr(t, "volume", clone.Volumes["data"], src.Volumes["data"])
	requireDistinctPtr(t, "docker network", clone.DockerNetworks["net"], src.DockerNetworks["net"])

	clone.Services["svc"].Artifacts[ArtifactBinary].Refs["latest"] = "/srv/clone/bin"
	clone.Services["svc"].TSNet.Tags[0] = "tag:clone"
	clone.Images["repo"].Refs["latest"] = ImageManifest{ContentType: "application/vnd.oci.image.manifest.v1+json", BlobHash: "sha256:clone"}
	clone.Volumes["data"].Path = "/clone"
	clone.DockerNetworks["net"].Endpoints["svc"].EndpointID = "clone-endpoint"
	clone.Services["new"] = &Service{Name: "new"}
	clone.Images["new"] = &ImageRepo{}
	clone.Volumes["new"] = &Volume{Name: "new"}
	clone.DockerNetworks["new"] = &DockerNetwork{NetworkID: "new"}

	if got := src.Services["svc"].Artifacts[ArtifactBinary].Refs["latest"]; got != "/srv/svc/bin" {
		t.Fatalf("source service artifact was mutated through clone: %q", got)
	}
	if got := src.Services["svc"].TSNet.Tags[0]; got != "tag:prod" {
		t.Fatalf("source tailscale tags were mutated through clone: %q", got)
	}
	if got := src.Images["repo"].Refs["latest"].BlobHash; got != "sha256:old" {
		t.Fatalf("source image repo was mutated through clone: %q", got)
	}
	if got := src.Volumes["data"].Path; got != "/data" {
		t.Fatalf("source volume was mutated through clone: %q", got)
	}
	if got := src.DockerNetworks["net"].Endpoints["svc"].EndpointID; got != "endpoint-id" {
		t.Fatalf("source docker endpoint was mutated through clone: %q", got)
	}
	if _, ok := src.Services["new"]; ok {
		t.Fatal("source services map was mutated through clone")
	}
	if _, ok := src.Images["new"]; ok {
		t.Fatal("source images map was mutated through clone")
	}
	if _, ok := src.Volumes["new"]; ok {
		t.Fatal("source volumes map was mutated through clone")
	}
	if _, ok := src.DockerNetworks["new"]; ok {
		t.Fatal("source docker networks map was mutated through clone")
	}
}

func TestMigrateV11AddsISOState(t *testing.T) {
	d := &Data{DataVersion: 11, Services: map[string]*Service{"app": {Name: "app"}}}
	migrated, err := migrate(d)
	if err != nil {
		t.Fatal(err)
	}
	if !migrated || d.DataVersion != CurrentDataVersion {
		t.Fatalf("migrated=%v version=%d", migrated, d.DataVersion)
	}
}

func TestMigrateVersion12LeavesOldServiceIdentityNil(t *testing.T) {
	d := &Data{DataVersion: 12, Services: map[string]*Service{
		"api": {Name: "api", ServiceType: ServiceTypeSystemd},
	}}
	if _, err := migrate(d); err != nil {
		t.Fatal(err)
	}
	if d.DataVersion != CurrentDataVersion || d.Services["api"].Identity != nil {
		t.Fatalf("migrated data = %#v", d)
	}
}

func TestISOStateCloneIsDeep(t *testing.T) {
	d := &Data{
		ISOPool: &ISOPool{Prefix: netip.MustParsePrefix("172.30.0.0/16")},
		Services: map[string]*Service{
			"app": {
				Name: "app",
				ISO: &ISOAllocation{
					Components: map[string]ISOComponent{
						"api": {Address: netip.MustParseAddr("172.30.128.2")},
					},
					RetiredComponents: map[string]ISOComponent{
						"old": {Address: netip.MustParseAddr("172.30.128.3")},
					},
					DesiredModes: []string{"iso", "ts"},
				},
			},
		},
	}

	clone := d.Clone()
	clone.ISOPool.Source = "clone"
	clone.Services["app"].ISO.Components["api"] = ISOComponent{Address: netip.MustParseAddr("172.30.128.4")}
	clone.Services["app"].ISO.RetiredComponents["old"] = ISOComponent{Address: netip.MustParseAddr("172.30.128.5")}
	clone.Services["app"].ISO.DesiredModes[0] = "clone"

	if d.ISOPool.Source != "" {
		t.Fatalf("source pool mutated to %q", d.ISOPool.Source)
	}
	if got := d.Services["app"].ISO.Components["api"].Address.String(); got != "172.30.128.2" {
		t.Fatalf("source component mutated to %s", got)
	}
	if got := d.Services["app"].ISO.RetiredComponents["old"].Address.String(); got != "172.30.128.3" {
		t.Fatalf("source retired component mutated to %s", got)
	}
	if got := d.Services["app"].ISO.DesiredModes[0]; got != "iso" {
		t.Fatalf("source desired mode mutated to %q", got)
	}
}

func TestISOStateViewExposesPersistedFields(t *testing.T) {
	d := &Data{
		ISOPool: &ISOPool{Prefix: netip.MustParsePrefix("172.30.0.0/16"), Source: "preferred"},
		Services: map[string]*Service{
			"app": {
				Name: "app",
				ISO: &ISOAllocation{
					State:        "ready",
					DesiredModes: []string{"iso"},
					Components: map[string]ISOComponent{
						"api": {Address: netip.MustParseAddr("172.30.128.2"), State: "reserved"},
					},
				},
			},
		},
		DockerNetworks: map[string]*DockerNetwork{"app": {Mode: "iso"}},
	}

	view := d.View()
	if got := view.ISOPool().Prefix(); got != d.ISOPool.Prefix {
		t.Fatalf("pool prefix = %v, want %v", got, d.ISOPool.Prefix)
	}
	service, ok := view.Services().GetOk("app")
	if !ok {
		t.Fatal("service view is missing app")
	}
	if got := service.ISO().State(); got != "ready" {
		t.Fatalf("allocation state = %q, want ready", got)
	}
	component, ok := service.ISO().Components().GetOk("api")
	if !ok || component.State != "reserved" {
		t.Fatalf("component view = %#v, present=%v", component, ok)
	}
	network, ok := view.DockerNetworks().GetOk("app")
	if !ok || network.Mode() != "iso" {
		t.Fatalf("docker network view mode = %q, present=%v", network.Mode(), ok)
	}
}

func TestServiceCloneDeepCopiesArtifactsNetworksAndTags(t *testing.T) {
	src := &Service{
		Name:             "svc",
		ServiceType:      ServiceTypeDockerCompose,
		Generation:       2,
		LatestGeneration: 3,
		Artifacts: ArtifactStore{
			ArtifactBinary:  {Refs: map[ArtifactRef]string{Gen(2): "/srv/svc/gen-2", "latest": "/srv/svc/latest"}},
			ArtifactEnvFile: nil,
		},
		SvcNetwork: &SvcNetwork{
			IPv4: mustAddr(t, "10.0.0.20"),
		},
		Macvlan: &MacvlanNetwork{
			Interface: "eth0.42",
			Mac:       "02:00:00:00:00:42",
			Parent:    "eth0",
			VLAN:      42,
		},
		TSNet: &TailscaleNetwork{
			Interface: "tailscale0",
			Version:   "1.2.3",
			ExitNode:  "exit-node",
			Tags:      []string{"tag:svc", "tag:prod"},
		},
	}

	clone := src.Clone()
	if !reflect.DeepEqual(clone, src) {
		t.Fatalf("clone differs from source:\nclone=%#v\nsource=%#v", clone, src)
	}
	if clone.Artifacts[ArtifactEnvFile] != nil {
		t.Fatal("nil artifact entry was not preserved")
	}
	requireDistinctPtr(t, "artifact", clone.Artifacts[ArtifactBinary], src.Artifacts[ArtifactBinary])
	requireDistinctPtr(t, "svc network", clone.SvcNetwork, src.SvcNetwork)
	requireDistinctPtr(t, "macvlan", clone.Macvlan, src.Macvlan)
	requireDistinctPtr(t, "tailscale network", clone.TSNet, src.TSNet)
	if &clone.TSNet.Tags[0] == &src.TSNet.Tags[0] {
		t.Fatal("tailscale tags slice aliases source")
	}

	clone.Artifacts[ArtifactBinary].Refs["latest"] = "/srv/clone/latest"
	clone.SvcNetwork.IPv4 = mustAddr(t, "10.0.0.99")
	clone.Macvlan.VLAN = 99
	clone.TSNet.Tags[0] = "tag:clone"
	clone.Artifacts["new"] = &Artifact{}

	if got := src.Artifacts[ArtifactBinary].Refs["latest"]; got != "/srv/svc/latest" {
		t.Fatalf("source artifact refs were mutated through clone: %q", got)
	}
	if got := src.SvcNetwork.IPv4; got != mustAddr(t, "10.0.0.20") {
		t.Fatalf("source service network was mutated through clone: %s", got)
	}
	if got := src.Macvlan.VLAN; got != 42 {
		t.Fatalf("source macvlan was mutated through clone: %d", got)
	}
	if got := src.TSNet.Tags[0]; got != "tag:svc" {
		t.Fatalf("source tailscale tags were mutated through clone: %q", got)
	}
	if _, ok := src.Artifacts["new"]; ok {
		t.Fatal("source artifacts map was mutated through clone")
	}
}

func TestVMServiceClonePreservesVMConfig(t *testing.T) {
	svc := &Service{
		Name:        "devbox",
		ServiceType: ServiceTypeVM,
		VM: &VMConfig{
			Runtime:     "firecracker",
			Image:       VMImageConfig{Payload: "vm://ubuntu/26.04", Version: "ubuntu-26.04-amd64-v1"},
			CPUs:        4,
			MemoryBytes: 4 << 30,
			Disk:        VMDiskConfig{Backend: "zvol", Bytes: 128 << 30, Path: "flash/yeet/vms/devbox/root"},
			SSH:         VMSSHConfig{User: "ubuntu"},
			Console:     VMConsoleConfig{SocketPath: "/run/yeet/devbox/serial.sock"},
			SetupState:  "ready",
		},
	}
	cloned := svc.Clone()
	if cloned.VM == nil || cloned.VM.Image.Payload != "vm://ubuntu/26.04" || cloned.VM.CPUs != 4 {
		t.Fatalf("cloned VM = %#v", cloned.VM)
	}
	cloned.VM.CPUs = 2
	if svc.VM.CPUs != 4 {
		t.Fatalf("source mutated, cpus = %d", svc.VM.CPUs)
	}
}

func TestDBRoundTripsVMHostAndBalloonConfig(t *testing.T) {
	data := &Data{
		DataVersion: CurrentDataVersion,
		VMHost: &VMHostConfig{
			MemoryPolicy: "balanced",
		},
		Services: map[string]*Service{
			"devbox": {
				Name:        "devbox",
				ServiceType: ServiceTypeVM,
				VM: &VMConfig{
					Runtime:     "firecracker",
					CPUs:        2,
					MemoryBytes: 4 << 30,
					Balloon: VMBalloonConfig{
						Mode:                 "auto",
						MinBytes:             1 << 30,
						StatsIntervalSeconds: 5,
						LastTargetBytes:      512 << 20,
					},
				},
			},
		},
	}

	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got Data
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.VMHost == nil || got.VMHost.MemoryPolicy != "balanced" {
		t.Fatalf("VMHost = %#v, want balanced policy", got.VMHost)
	}
	vm := got.Services["devbox"].VM
	if vm.Balloon.Mode != "auto" || vm.Balloon.MinBytes != 1<<30 || vm.Balloon.StatsIntervalSeconds != 5 || vm.Balloon.LastTargetBytes != 512<<20 {
		t.Fatalf("Balloon = %#v, want persisted config", vm.Balloon)
	}
}

func TestServiceIdentityRoundTrip(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "db.json")
	want := &ServiceIdentity{
		RequestedUser:  "root",
		RequestedGroup: "root",
		UID:            0,
		GID:            0,
	}
	store := NewStore(path, filepath.Join(root, "services"))
	if err := store.Set(&Data{
		DataVersion: CurrentDataVersion,
		Services: map[string]*Service{
			"api": {
				Name:                   "api",
				ServiceType:            ServiceTypeSystemd,
				Identity:               want,
				IdentityInstallPending: true,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	onDisk := mustReadData(t, path).Services["api"].Identity
	if onDisk == nil || *onDisk != *want {
		t.Fatalf("on-disk identity = %#v, want %#v", onDisk, want)
	}

	got, err := NewStore(path, filepath.Join(root, "services")).Get()
	if err != nil {
		t.Fatal(err)
	}
	roundTripped := got.AsStruct().Services["api"].Identity
	if roundTripped == nil || *roundTripped != *want {
		t.Fatalf("round-tripped identity = %#v, want %#v", roundTripped, want)
	}
	if !got.AsStruct().Services["api"].IdentityInstallPending {
		t.Fatal("round-tripped IdentityInstallPending = false, want true")
	}
}

func TestVMHostAndBalloonCloneAndView(t *testing.T) {
	data := &Data{
		DataVersion: CurrentDataVersion,
		VMHost: &VMHostConfig{
			MemoryPolicy: "balanced",
		},
		Services: map[string]*Service{
			"devbox": {
				Name:        "devbox",
				ServiceType: ServiceTypeVM,
				VM: &VMConfig{
					Runtime:     "firecracker",
					MemoryBytes: 4 << 30,
					Balloon: VMBalloonConfig{
						Mode:                 "auto",
						MinBytes:             1 << 30,
						StatsIntervalSeconds: 5,
						LastTargetBytes:      512 << 20,
					},
				},
			},
		},
	}

	clone := data.Clone()
	requireDistinctPtr(t, "vm host", clone.VMHost, data.VMHost)
	requireDistinctPtr(t, "vm service", clone.Services["devbox"], data.Services["devbox"])
	requireDistinctPtr(t, "vm config", clone.Services["devbox"].VM, data.Services["devbox"].VM)
	if got := clone.VMHost.MemoryPolicy; got != "balanced" {
		t.Fatalf("clone VMHost MemoryPolicy = %q, want balanced", got)
	}
	if got := clone.Services["devbox"].VM.Balloon; got != data.Services["devbox"].VM.Balloon {
		t.Fatalf("clone VM Balloon = %#v, want %#v", got, data.Services["devbox"].VM.Balloon)
	}
	clone.VMHost.MemoryPolicy = "aggressive"
	clone.Services["devbox"].VM.Balloon.MinBytes = 2 << 30
	if got := data.VMHost.MemoryPolicy; got != "balanced" {
		t.Fatalf("source VMHost mutated through clone: %q", got)
	}
	if got := data.Services["devbox"].VM.Balloon.MinBytes; got != 1<<30 {
		t.Fatalf("source VM Balloon mutated through clone: %d", got)
	}

	view := data.View()
	if got := view.VMHost().MemoryPolicy(); got != "balanced" {
		t.Fatalf("view VMHost MemoryPolicy = %q, want balanced", got)
	}
	svc, ok := view.Services().GetOk("devbox")
	if !ok {
		t.Fatal("view missing VM service")
	}
	if got := svc.VM().Balloon(); got != data.Services["devbox"].VM.Balloon {
		t.Fatalf("view VM Balloon = %#v, want %#v", got, data.Services["devbox"].VM.Balloon)
	}
}

func testVMComponentsConfig() *VMComponentsConfig {
	return &VMComponentsConfig{
		GuestBase: VMGuestBaseConfig{
			ID:               "guest-ubuntu-26.04-amd64-v1",
			ManifestSHA256:   strings.Repeat("a", 64),
			Source:           "official",
			RootFSProvenance: "ubuntu-26.04-server-cloudimg-amd64.img",
		},
		Kernel: VMKernelArtifactConfig{
			ID:             "kernel-linux-7.1.1-yeet-v1",
			ManifestSHA256: strings.Repeat("b", 64),
			SHA256:         strings.Repeat("c", 64),
			Path:           "/var/lib/yeet/vm-kernels/vmlinux",
			Source:         "official",
		},
		Runtime: VMRuntimeLifecycleConfig{
			Policy:  "manual",
			Channel: "stable",
			Configured: VMRuntimeArtifactConfig{
				ID:                "firecracker-v1.16.1-yeet-v1",
				ManifestSHA256:    strings.Repeat("d", 64),
				FirecrackerSHA256: strings.Repeat("e", 64),
				JailerSHA256:      strings.Repeat("f", 64),
				Firecracker:       "/var/lib/yeet/vm-runtimes/amd64/fc/firecracker",
				Jailer:            "/var/lib/yeet/vm-runtimes/amd64/fc/jailer",
				Source:            "official",
			},
		},
	}
}

func TestVMComponentsClone(t *testing.T) {
	components := testVMComponentsConfig()
	components.Runtime.Staged = &VMRuntimeArtifactConfig{
		ID:                "firecracker-v1.17.0-yeet-v1",
		ManifestSHA256:    strings.Repeat("1", 64),
		FirecrackerSHA256: strings.Repeat("2", 64),
		JailerSHA256:      strings.Repeat("3", 64),
		Firecracker:       "/var/lib/yeet/vm-runtimes/amd64/staged/firecracker",
		Jailer:            "/var/lib/yeet/vm-runtimes/amd64/staged/jailer",
		Source:            "official",
	}
	components.Runtime.Previous = &VMRuntimeArtifactConfig{
		ID:                "firecracker-v1.15.0-yeet-v2",
		ManifestSHA256:    strings.Repeat("4", 64),
		FirecrackerSHA256: strings.Repeat("5", 64),
		JailerSHA256:      strings.Repeat("6", 64),
		Firecracker:       "/var/lib/yeet/vm-runtimes/amd64/previous/firecracker",
		Jailer:            "/var/lib/yeet/vm-runtimes/amd64/previous/jailer",
		Source:            "official",
	}
	components.Runtime.Trial = &VMRuntimeTrialConfig{
		State:         "pending",
		CandidateID:   components.Runtime.Staged.ID,
		PreviousID:    components.Runtime.Previous.ID,
		RecoveryPoint: "runtime-trial-before-v1.17.0",
		StartedAt:     "2026-07-19T14:00:00Z",
		LastError:     "previous attempt timed out",
	}

	src := &Data{
		DataVersion: CurrentDataVersion,
		VMHost: &VMHostConfig{
			MemoryPolicy:        "balanced",
			RuntimePolicy:       "stage-on-restart",
			RuntimeChannel:      "candidate",
			ProtectedRuntimeIDs: []string{components.Runtime.Configured.ID, components.Runtime.Previous.ID},
		},
		Services: map[string]*Service{
			"devbox": {
				Name:        "devbox",
				ServiceType: ServiceTypeVM,
				VM: &VMConfig{
					Runtime:    "firecracker",
					Image:      VMImageConfig{Payload: "vm://ubuntu/26.04", Version: "ubuntu-26.04-amd64-v29"},
					Components: components,
				},
			},
		},
	}

	raw, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var roundTrip Data
	if err := json.Unmarshal(raw, &roundTrip); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(&roundTrip, src) {
		t.Fatalf("round-trip differs:\ngot=%#v\nwant=%#v", &roundTrip, src)
	}

	clone := src.Clone()
	if !reflect.DeepEqual(clone, src) {
		t.Fatalf("clone differs:\ngot=%#v\nwant=%#v", clone, src)
	}
	requireDistinctPtr(t, "VM host", clone.VMHost, src.VMHost)
	requireDistinctPtr(t, "VM components", clone.Services["devbox"].VM.Components, components)
	requireDistinctPtr(t, "staged runtime", clone.Services["devbox"].VM.Components.Runtime.Staged, components.Runtime.Staged)
	requireDistinctPtr(t, "previous runtime", clone.Services["devbox"].VM.Components.Runtime.Previous, components.Runtime.Previous)
	requireDistinctPtr(t, "runtime trial", clone.Services["devbox"].VM.Components.Runtime.Trial, components.Runtime.Trial)

	clone.VMHost.ProtectedRuntimeIDs[0] = "firecracker-v9.9.9-yeet-v9"
	clone.Services["devbox"].VM.Components.GuestBase.ID = "guest-clone"
	clone.Services["devbox"].VM.Components.Runtime.Staged.ID = "runtime-staged-clone"
	clone.Services["devbox"].VM.Components.Runtime.Previous.ID = "runtime-previous-clone"
	clone.Services["devbox"].VM.Components.Runtime.Trial.LastError = "clone"
	clone.Services["new"] = &Service{Name: "new"}

	if got := src.VMHost.ProtectedRuntimeIDs[0]; got != components.Runtime.Configured.ID {
		t.Fatalf("source protected runtime IDs mutated through clone: %q", got)
	}
	if got := components.GuestBase.ID; got != "guest-ubuntu-26.04-amd64-v1" {
		t.Fatalf("source guest base mutated through clone: %q", got)
	}
	if got := components.Runtime.Staged.ID; got != "firecracker-v1.17.0-yeet-v1" {
		t.Fatalf("source staged runtime mutated through clone: %q", got)
	}
	if got := components.Runtime.Previous.ID; got != "firecracker-v1.15.0-yeet-v2" {
		t.Fatalf("source previous runtime mutated through clone: %q", got)
	}
	if got := components.Runtime.Trial.LastError; got != "previous attempt timed out" {
		t.Fatalf("source runtime trial mutated through clone: %q", got)
	}
	if _, ok := src.Services["new"]; ok {
		t.Fatal("source services map mutated through clone")
	}
}

func TestServiceRootCloneAndView(t *testing.T) {
	data := &Data{
		DataVersion: CurrentDataVersion,
		Services: map[string]*Service{
			"svc": {
				Name:           "svc",
				ServiceType:    ServiceTypeDockerCompose,
				ServiceRoot:    "/tank/apps/svc",
				ServiceRootZFS: "tank/apps/svc",
			},
		},
	}

	clone := data.Clone()
	if got := clone.Services["svc"].ServiceRoot; got != "/tank/apps/svc" {
		t.Fatalf("Clone ServiceRoot = %q, want /tank/apps/svc", got)
	}
	if got := clone.Services["svc"].ServiceRootZFS; got != "tank/apps/svc" {
		t.Fatalf("Clone ServiceRootZFS = %q, want tank/apps/svc", got)
	}
	clone.Services["svc"].ServiceRoot = "/srv/clone/svc"
	clone.Services["svc"].ServiceRootZFS = "tank/apps/clone"
	if got := data.Services["svc"].ServiceRoot; got != "/tank/apps/svc" {
		t.Fatalf("source ServiceRoot was mutated through clone: %q", got)
	}
	if got := data.Services["svc"].ServiceRootZFS; got != "tank/apps/svc" {
		t.Fatalf("source ServiceRootZFS was mutated through clone: %q", got)
	}

	view := data.View()
	svc, ok := view.Services().GetOk("svc")
	if !ok {
		t.Fatal("view missing service")
	}
	if got := svc.ServiceRoot(); got != "/tank/apps/svc" {
		t.Fatalf("View ServiceRoot = %q, want /tank/apps/svc", got)
	}
	if got := svc.ServiceRootZFS(); got != "tank/apps/svc" {
		t.Fatalf("View ServiceRootZFS = %q, want tank/apps/svc", got)
	}
	if got := view.AsStruct().Services["svc"].ServiceRoot; got != "/tank/apps/svc" {
		t.Fatalf("View AsStruct ServiceRoot = %q, want /tank/apps/svc", got)
	}
	if got := view.AsStruct().Services["svc"].ServiceRootZFS; got != "tank/apps/svc" {
		t.Fatalf("View AsStruct ServiceRootZFS = %q, want tank/apps/svc", got)
	}
}

func TestSnapshotPolicyCloneAndView(t *testing.T) {
	enabled := false
	required := true
	keepLast := 3
	data := &Data{
		DataVersion: CurrentDataVersion,
		SnapshotDefaults: &SnapshotPolicy{
			Enabled:  boolPtr(enabled),
			KeepLast: intPtr(keepLast),
			MaxAge:   "72h",
			Events:   []string{"run", "docker-update"},
			Required: boolPtr(required),
		},
		Services: map[string]*Service{
			"svc": {
				Name: "svc",
				SnapshotPolicy: &SnapshotPolicy{
					Enabled:  boolPtr(true),
					KeepLast: intPtr(2),
					MaxAge:   "24h",
					Events:   []string{"deploy"},
					Required: boolPtr(false),
				},
			},
		},
	}

	clone := data.Clone()
	*clone.SnapshotDefaults.Enabled = true
	*clone.SnapshotDefaults.KeepLast = 9
	clone.SnapshotDefaults.MaxAge = "1h"
	clone.SnapshotDefaults.Events[0] = "manual"
	*clone.SnapshotDefaults.Required = false
	*clone.Services["svc"].SnapshotPolicy.Enabled = false
	*clone.Services["svc"].SnapshotPolicy.KeepLast = 8
	clone.Services["svc"].SnapshotPolicy.MaxAge = "2h"
	clone.Services["svc"].SnapshotPolicy.Events[0] = "manual"
	*clone.Services["svc"].SnapshotPolicy.Required = true
	if got := *data.SnapshotDefaults.Enabled; got != false {
		t.Fatalf("source SnapshotDefaults.Enabled mutated through clone: %v", got)
	}
	if got := *data.SnapshotDefaults.KeepLast; got != 3 {
		t.Fatalf("source SnapshotDefaults.KeepLast mutated through clone: %d", got)
	}
	if got := data.SnapshotDefaults.MaxAge; got != "72h" {
		t.Fatalf("source SnapshotDefaults.MaxAge mutated through clone: %q", got)
	}
	if got := data.SnapshotDefaults.Events[0]; got != "run" {
		t.Fatalf("source SnapshotDefaults.Events mutated through clone: %q", got)
	}
	if got := *data.SnapshotDefaults.Required; got != true {
		t.Fatalf("source SnapshotDefaults.Required mutated through clone: %v", got)
	}
	if got := *data.Services["svc"].SnapshotPolicy.Enabled; got != true {
		t.Fatalf("source service SnapshotPolicy.Enabled mutated through clone: %v", got)
	}
	if got := *data.Services["svc"].SnapshotPolicy.KeepLast; got != 2 {
		t.Fatalf("source service SnapshotPolicy.KeepLast mutated through clone: %d", got)
	}
	if got := data.Services["svc"].SnapshotPolicy.MaxAge; got != "24h" {
		t.Fatalf("source service SnapshotPolicy.MaxAge mutated through clone: %q", got)
	}
	if got := data.Services["svc"].SnapshotPolicy.Events[0]; got != "deploy" {
		t.Fatalf("source service SnapshotPolicy.Events mutated through clone: %q", got)
	}
	if got := *data.Services["svc"].SnapshotPolicy.Required; got != false {
		t.Fatalf("source service SnapshotPolicy.Required mutated through clone: %v", got)
	}

	view := data.View()
	defaults := view.SnapshotDefaults()
	if got := defaults.Enabled().Get(); got != false {
		t.Fatalf("View SnapshotDefaults Enabled = %v, want false", got)
	}
	if got := defaults.KeepLast().Get(); got != 3 {
		t.Fatalf("View SnapshotDefaults KeepLast = %d, want 3", got)
	}
	if got := defaults.MaxAge(); got != "72h" {
		t.Fatalf("View SnapshotDefaults MaxAge = %q, want 72h", got)
	}
	if got := defaults.Events(); got.Len() != 2 {
		t.Fatalf("View SnapshotDefaults Events len = %d, want 2", got.Len())
	} else if got.At(0) != "run" || got.At(1) != "docker-update" {
		t.Fatalf("View SnapshotDefaults Events = [%s %s], want [run docker-update]", got.At(0), got.At(1))
	}
	if got := defaults.Required().Get(); got != true {
		t.Fatalf("View SnapshotDefaults Required = %v, want true", got)
	}
	sv, ok := view.Services().GetOk("svc")
	if !ok {
		t.Fatal("missing service view")
	}
	servicePolicy := sv.SnapshotPolicy()
	if got := servicePolicy.Enabled().Get(); got != true {
		t.Fatalf("View service SnapshotPolicy Enabled = %v, want true", got)
	}
	if got := servicePolicy.KeepLast().Get(); got != 2 {
		t.Fatalf("View service SnapshotPolicy KeepLast = %d, want 2", got)
	}
	if got := servicePolicy.MaxAge(); got != "24h" {
		t.Fatalf("View service SnapshotPolicy MaxAge = %q, want 24h", got)
	}
	if got := servicePolicy.Events(); got.Len() != 1 {
		t.Fatalf("View service SnapshotPolicy Events len = %d, want 1", got.Len())
	} else if got.At(0) != "deploy" {
		t.Fatalf("View service SnapshotPolicy Events = %q, want [deploy]", got.At(0))
	}
	if got := servicePolicy.Required().Get(); got != false {
		t.Fatalf("View service SnapshotPolicy Required = %v, want false", got)
	}

	viewStruct := view.AsStruct()
	*viewStruct.SnapshotDefaults.Enabled = true
	*viewStruct.SnapshotDefaults.KeepLast = 10
	viewStruct.SnapshotDefaults.Events[0] = "view-struct"
	*viewStruct.SnapshotDefaults.Required = false
	if got := *data.SnapshotDefaults.Enabled; got != false {
		t.Fatalf("source SnapshotDefaults.Enabled mutated through view AsStruct: %v", got)
	}
	if got := *data.SnapshotDefaults.KeepLast; got != 3 {
		t.Fatalf("source SnapshotDefaults.KeepLast mutated through view AsStruct: %d", got)
	}
	if got := data.SnapshotDefaults.Events[0]; got != "run" {
		t.Fatalf("source SnapshotDefaults.Events mutated through view AsStruct: %q", got)
	}
	if got := *data.SnapshotDefaults.Required; got != true {
		t.Fatalf("source SnapshotDefaults.Required mutated through view AsStruct: %v", got)
	}

	svStruct := sv.AsStruct()
	*svStruct.SnapshotPolicy.Enabled = false
	*svStruct.SnapshotPolicy.KeepLast = 11
	svStruct.SnapshotPolicy.Events[0] = "service-struct"
	*svStruct.SnapshotPolicy.Required = true
	if got := *data.Services["svc"].SnapshotPolicy.Enabled; got != true {
		t.Fatalf("source service SnapshotPolicy.Enabled mutated through view AsStruct: %v", got)
	}
	if got := *data.Services["svc"].SnapshotPolicy.KeepLast; got != 2 {
		t.Fatalf("source service SnapshotPolicy.KeepLast mutated through view AsStruct: %d", got)
	}
	if got := data.Services["svc"].SnapshotPolicy.Events[0]; got != "deploy" {
		t.Fatalf("source service SnapshotPolicy.Events mutated through view AsStruct: %q", got)
	}
	if got := *data.Services["svc"].SnapshotPolicy.Required; got != false {
		t.Fatalf("source service SnapshotPolicy.Required mutated through view AsStruct: %v", got)
	}
}

func TestDockerNetworkCloneDeepCopiesMapsAndPointers(t *testing.T) {
	src := &DockerNetwork{
		NetworkID:   "network-id",
		NetNS:       "netns",
		IPv4Gateway: mustPrefix(t, "10.1.0.1/24"),
		IPv4Range:   mustPrefix(t, "10.1.0.0/24"),
		Endpoints: map[string]*DockerEndpoint{
			"nil": nil,
			"svc": {EndpointID: "endpoint-id", IPv4: mustPrefix(t, "10.1.0.2/32")},
		},
		EndpointAddrs: map[string]netip.Prefix{
			"legacy": mustPrefix(t, "10.1.0.3/32"),
		},
		PortMap: map[string]*EndpointPort{
			"nil":  nil,
			"6/80": {EndpointID: "endpoint-id", Port: 80},
		},
	}

	clone := src.Clone()
	if !reflect.DeepEqual(clone, src) {
		t.Fatalf("clone differs from source:\nclone=%#v\nsource=%#v", clone, src)
	}
	if clone.Endpoints["nil"] != nil {
		t.Fatal("nil endpoint entry was not preserved")
	}
	if clone.PortMap["nil"] != nil {
		t.Fatal("nil port entry was not preserved")
	}
	requireDistinctPtr(t, "endpoint", clone.Endpoints["svc"], src.Endpoints["svc"])
	requireDistinctPtr(t, "port", clone.PortMap["6/80"], src.PortMap["6/80"])

	clone.Endpoints["svc"].EndpointID = "clone-endpoint"
	clone.EndpointAddrs["legacy"] = mustPrefix(t, "10.1.0.99/32")
	clone.PortMap["6/80"].Port = 8080
	clone.Endpoints["new"] = &DockerEndpoint{EndpointID: "new"}
	clone.EndpointAddrs["new"] = mustPrefix(t, "10.1.0.100/32")
	clone.PortMap["6/443"] = &EndpointPort{EndpointID: "new", Port: 443}

	if got := src.Endpoints["svc"].EndpointID; got != "endpoint-id" {
		t.Fatalf("source endpoint was mutated through clone: %q", got)
	}
	if got := src.EndpointAddrs["legacy"]; got != mustPrefix(t, "10.1.0.3/32") {
		t.Fatalf("source endpoint addrs were mutated through clone: %s", got)
	}
	if got := src.PortMap["6/80"].Port; got != 80 {
		t.Fatalf("source port map was mutated through clone: %d", got)
	}
	if _, ok := src.Endpoints["new"]; ok {
		t.Fatal("source endpoints map was mutated through clone")
	}
	if _, ok := src.EndpointAddrs["new"]; ok {
		t.Fatal("source endpoint addrs map was mutated through clone")
	}
	if _, ok := src.PortMap["6/443"]; ok {
		t.Fatal("source port map was mutated through clone")
	}
}

func TestCloneNilReceiversAndEmptyCollections(t *testing.T) {
	if got := (*Data)(nil).Clone(); got != nil {
		t.Fatalf("nil Data clone = %#v, want nil", got)
	}
	if got := (*Service)(nil).Clone(); got != nil {
		t.Fatalf("nil Service clone = %#v, want nil", got)
	}
	if got := (*DockerNetwork)(nil).Clone(); got != nil {
		t.Fatalf("nil DockerNetwork clone = %#v, want nil", got)
	}

	data := (&Data{
		Services:       map[string]*Service{},
		Images:         map[ImageRepoName]*ImageRepo{},
		Volumes:        map[string]*Volume{},
		DockerNetworks: map[string]*DockerNetwork{},
	}).Clone()
	if data.Services == nil || data.Images == nil || data.Volumes == nil || data.DockerNetworks == nil {
		t.Fatalf("empty Data maps were not preserved: %#v", data)
	}
	data.Services["svc"] = nil
	data.Images["image"] = nil
	data.Volumes["volume"] = nil
	data.DockerNetworks["network"] = nil

	serviceSrc := &Service{
		Artifacts: ArtifactStore{},
		TSNet:     &TailscaleNetwork{Tags: []string{}},
	}
	service := serviceSrc.Clone()
	if service.Artifacts == nil {
		t.Fatal("empty ArtifactStore was not preserved")
	}
	service.Artifacts[ArtifactBinary] = nil
	service.TSNet.Tags = append(service.TSNet.Tags, "tag:new")
	if _, ok := serviceSrc.Artifacts[ArtifactBinary]; ok {
		t.Fatal("source empty ArtifactStore was mutated through clone")
	}
	if len(serviceSrc.TSNet.Tags) != 0 {
		t.Fatalf("source empty tags slice was mutated through clone: %#v", serviceSrc.TSNet.Tags)
	}

	networkSrc := &DockerNetwork{
		Endpoints:     map[string]*DockerEndpoint{},
		EndpointAddrs: map[string]netip.Prefix{},
		PortMap:       map[string]*EndpointPort{},
	}
	network := networkSrc.Clone()
	if network.Endpoints == nil || network.EndpointAddrs == nil || network.PortMap == nil {
		t.Fatalf("empty DockerNetwork maps were not preserved: %#v", network)
	}
	network.Endpoints["endpoint"] = nil
	network.EndpointAddrs["endpoint"] = mustPrefix(t, "10.2.0.2/32")
	network.PortMap["6/80"] = nil
	if _, ok := networkSrc.Endpoints["endpoint"]; ok {
		t.Fatal("source empty endpoints map was mutated through clone")
	}
	if _, ok := networkSrc.EndpointAddrs["endpoint"]; ok {
		t.Fatal("source empty endpoint addrs map was mutated through clone")
	}
	if _, ok := networkSrc.PortMap["6/80"]; ok {
		t.Fatal("source empty port map was mutated through clone")
	}
}

func TestLeafCloneMethods(t *testing.T) {
	if got := (*Volume)(nil).Clone(); got != nil {
		t.Fatalf("nil Volume clone = %#v, want nil", got)
	}
	if got := (*ImageRepo)(nil).Clone(); got != nil {
		t.Fatalf("nil ImageRepo clone = %#v, want nil", got)
	}
	if got := (*Artifact)(nil).Clone(); got != nil {
		t.Fatalf("nil Artifact clone = %#v, want nil", got)
	}
	if got := (*DockerEndpoint)(nil).Clone(); got != nil {
		t.Fatalf("nil DockerEndpoint clone = %#v, want nil", got)
	}
	if got := (*TailscaleNetwork)(nil).Clone(); got != nil {
		t.Fatalf("nil TailscaleNetwork clone = %#v, want nil", got)
	}
	if got := (*EndpointPort)(nil).Clone(); got != nil {
		t.Fatalf("nil EndpointPort clone = %#v, want nil", got)
	}

	volumeSrc := &Volume{Name: "data", Path: "/data"}
	volume := volumeSrc.Clone()
	requireDistinctPtr(t, "volume", volume, volumeSrc)
	volume.Path = "/clone"
	if volumeSrc.Path != "/data" {
		t.Fatalf("source volume was mutated through clone: %q", volumeSrc.Path)
	}

	imageSrc := &ImageRepo{Refs: map[ImageRef]ImageManifest{
		"latest": {BlobHash: "sha256:old"},
	}}
	image := imageSrc.Clone()
	requireDistinctPtr(t, "image repo", image, imageSrc)
	image.Refs["latest"] = ImageManifest{BlobHash: "sha256:clone"}
	image.Refs["new"] = ImageManifest{}
	if got := imageSrc.Refs["latest"].BlobHash; got != "sha256:old" {
		t.Fatalf("source image refs were mutated through clone: %q", got)
	}
	if _, ok := imageSrc.Refs["new"]; ok {
		t.Fatal("source image refs map was mutated through clone")
	}

	artifactSrc := &Artifact{Refs: map[ArtifactRef]string{
		"latest": "/srv/svc/latest",
	}}
	artifact := artifactSrc.Clone()
	requireDistinctPtr(t, "artifact", artifact, artifactSrc)
	artifact.Refs["latest"] = "/srv/clone/latest"
	artifact.Refs["new"] = "/srv/clone/new"
	if got := artifactSrc.Refs["latest"]; got != "/srv/svc/latest" {
		t.Fatalf("source artifact refs were mutated through clone: %q", got)
	}
	if _, ok := artifactSrc.Refs["new"]; ok {
		t.Fatal("source artifact refs map was mutated through clone")
	}

	endpointSrc := &DockerEndpoint{EndpointID: "endpoint-id", IPv4: mustPrefix(t, "10.4.0.2/32")}
	endpoint := endpointSrc.Clone()
	requireDistinctPtr(t, "docker endpoint", endpoint, endpointSrc)
	endpoint.EndpointID = "clone-endpoint"
	if endpointSrc.EndpointID != "endpoint-id" {
		t.Fatalf("source docker endpoint was mutated through clone: %q", endpointSrc.EndpointID)
	}

	tsSrc := &TailscaleNetwork{Interface: "tailscale0", Tags: []string{"tag:one"}}
	ts := tsSrc.Clone()
	requireDistinctPtr(t, "tailscale network", ts, tsSrc)
	if &ts.Tags[0] == &tsSrc.Tags[0] {
		t.Fatal("tailscale tags slice aliases source")
	}
	ts.Tags[0] = "tag:clone"
	if got := tsSrc.Tags[0]; got != "tag:one" {
		t.Fatalf("source tailscale tags were mutated through clone: %q", got)
	}

	portSrc := &EndpointPort{EndpointID: "endpoint-id", Port: 80}
	port := portSrc.Clone()
	requireDistinctPtr(t, "endpoint port", port, portSrc)
	port.Port = 8080
	if portSrc.Port != 80 {
		t.Fatalf("source endpoint port was mutated through clone: %d", portSrc.Port)
	}
}

func TestStoreGetCreatesCurrentVersionForMissingFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nested", "db.json")
	store := NewStore(path, filepath.Join(root, "services"))

	dv, err := store.Get()
	if err != nil {
		t.Fatal(err)
	}
	got := dv.AsStruct()
	if got == nil {
		t.Fatal("Get returned nil data for missing database")
	}
	if got.DataVersion != CurrentDataVersion {
		t.Fatalf("DataVersion = %d, want %d", got.DataVersion, CurrentDataVersion)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("missing database Get created file or returned unexpected stat error: %v", err)
	}
}

func TestStoreSetWritesCloneAndCreatesParentDir(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nested", "db.json")
	store := NewStore(path, filepath.Join(root, "services"))
	input := &Data{
		DataVersion: CurrentDataVersion,
		Services: map[string]*Service{
			"svc": {
				Name:       "svc",
				Generation: 7,
			},
		},
	}

	if err := store.Set(input); err != nil {
		t.Fatal(err)
	}
	input.Services["svc"].Name = "mutated"

	got := mustReadData(t, path)
	if got.Services["svc"].Name != "svc" {
		t.Fatalf("on-disk data aliases Set input: got service name %q", got.Services["svc"].Name)
	}
	dv, err := store.Get()
	if err != nil {
		t.Fatal(err)
	}
	if got := dv.AsStruct().Services["svc"].Name; got != "svc" {
		t.Fatalf("store data aliases Set input: got service name %q", got)
	}
}

func TestStoreSetSyncsTemporaryFileBeforeRenameAndDirectoryAfter(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nested", "db.json")
	store := NewStore(path, filepath.Join(root, "services"))

	oldFileSync := syncDBFile
	oldRename := renameDBFile
	oldDirectorySync := syncDBDirectory
	var events []string
	syncDBFile = func(f *os.File) error {
		events = append(events, "file-sync")
		return oldFileSync(f)
	}
	renameDBFile = func(oldPath, newPath string) error {
		events = append(events, "rename")
		return oldRename(oldPath, newPath)
	}
	syncDBDirectory = func(f *os.File) error {
		events = append(events, "directory-sync")
		return oldDirectorySync(f)
	}
	t.Cleanup(func() {
		syncDBFile = oldFileSync
		renameDBFile = oldRename
		syncDBDirectory = oldDirectorySync
	})

	if err := store.Set(&Data{DataVersion: CurrentDataVersion}); err != nil {
		t.Fatal(err)
	}
	fileSync := slices.Index(events, "file-sync")
	if fileSync < 0 {
		t.Fatalf("durability events = %v, missing temporary file sync", events)
	}
	if got, want := events[fileSync:], []string{"file-sync", "rename", "directory-sync"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("database publication events = %v, want %v (all durability events: %v)", got, want, events)
	}
}

func TestStoreSetPropagatesTemporaryFileSyncFailureBeforeRename(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "db.json")
	writeData(t, path, &Data{
		DataVersion: CurrentDataVersion,
		Services: map[string]*Service{
			"svc": {Name: "svc", Generation: 1},
		},
	})
	store := NewStore(path, filepath.Join(root, "services"))
	if _, err := store.Get(); err != nil {
		t.Fatal(err)
	}

	oldFileSync := syncDBFile
	oldRename := renameDBFile
	wantErr := errors.New("temporary database sync failed")
	renameCalled := false
	syncDBFile = func(*os.File) error { return wantErr }
	renameDBFile = func(oldPath, newPath string) error {
		renameCalled = true
		return oldRename(oldPath, newPath)
	}
	t.Cleanup(func() {
		syncDBFile = oldFileSync
		renameDBFile = oldRename
	})

	err := store.Set(&Data{
		DataVersion: CurrentDataVersion,
		Services: map[string]*Service{
			"svc": {Name: "svc", Generation: 2},
		},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Set error = %v, want %v", err, wantErr)
	}
	if renameCalled {
		t.Fatal("database was renamed after temporary file sync failed")
	}
	if got := mustReadData(t, path).Services["svc"].Generation; got != 1 {
		t.Fatalf("on-disk generation = %d, want 1", got)
	}
	got, err := store.Get()
	if err != nil {
		t.Fatal(err)
	}
	if generation := got.Services().Get("svc").Generation(); generation != 1 {
		t.Fatalf("cached generation = %d, want 1", generation)
	}
}

func TestStoreSetPropagatesDirectorySyncFailureAndExposesRenamedStateForRollback(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "db.json")
	writeData(t, path, &Data{
		DataVersion: CurrentDataVersion,
		Services: map[string]*Service{
			"svc": {Name: "svc", Generation: 1},
		},
	})
	store := NewStore(path, filepath.Join(root, "services"))
	if _, err := store.Get(); err != nil {
		t.Fatal(err)
	}

	oldRename := renameDBFile
	oldDirectorySync := syncDBDirectory
	wantErr := errors.New("database directory sync failed")
	renameCalled := false
	renameDBFile = func(oldPath, newPath string) error {
		renameCalled = true
		return oldRename(oldPath, newPath)
	}
	syncDBDirectory = func(f *os.File) error {
		if renameCalled {
			return wantErr
		}
		return oldDirectorySync(f)
	}
	t.Cleanup(func() {
		renameDBFile = oldRename
		syncDBDirectory = oldDirectorySync
	})

	err := store.Set(&Data{
		DataVersion: CurrentDataVersion,
		Services: map[string]*Service{
			"svc": {Name: "svc", Generation: 2},
		},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Set error = %v, want %v", err, wantErr)
	}
	if !renameCalled {
		t.Fatal("database was not renamed before directory sync")
	}
	got, err := store.Get()
	if err != nil {
		t.Fatal(err)
	}
	if generation := got.Services().Get("svc").Generation(); generation != 2 {
		t.Fatalf("cached generation = %d, want renamed generation 2", generation)
	}
	if generation := mustReadData(t, path).Services["svc"].Generation; generation != 2 {
		t.Fatalf("on-disk generation = %d, want renamed generation 2", generation)
	}

	syncDBDirectory = oldDirectorySync
	_, _, err = store.MutateService("svc", func(_ *Data, svc *Service) error {
		if svc.Generation != 2 {
			t.Fatalf("rollback observed generation = %d, want provisional generation 2", svc.Generation)
		}
		svc.Generation = 1
		return nil
	})
	if err != nil {
		t.Fatalf("rollback mutation: %v", err)
	}
	if generation := mustReadData(t, path).Services["svc"].Generation; generation != 1 {
		t.Fatalf("on-disk generation after rollback = %d, want 1", generation)
	}
}

func TestStoreSetNormalizesZeroDataVersion(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "db.json")
	store := NewStore(path, filepath.Join(root, "services"))

	if err := store.Set(&Data{Services: map[string]*Service{"svc": {Name: "svc"}}}); err != nil {
		t.Fatal(err)
	}
	if got := mustReadData(t, path).DataVersion; got != CurrentDataVersion {
		t.Fatalf("on-disk DataVersion = %d, want %d", got, CurrentDataVersion)
	}
	if _, err := store.MutateData(func(d *Data) error {
		d.Services["svc"].Generation++
		return nil
	}); err != nil {
		t.Fatalf("MutateData after Set: %v", err)
	}
}

func TestStoreSetNilDoesNotCreateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.json")
	store := NewStore(path, filepath.Join(t.TempDir(), "services"))

	if err := store.Set(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Set(nil) created file or returned unexpected stat error: %v", err)
	}
}

func TestStoreGetMigratesVersion4EndpointAddrs(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "db.json")
	legacyPrefix := mustPrefix(t, "10.3.0.2/32")
	writeData(t, path, &Data{
		DataVersion: 4,
		DockerNetworks: map[string]*DockerNetwork{
			"svc": {
				NetworkID: "network-id",
				EndpointAddrs: map[string]netip.Prefix{
					"endpoint-id": legacyPrefix,
				},
			},
		},
	})

	store := NewStore(path, filepath.Join(root, "services"))
	dv, err := store.Get()
	if err != nil {
		t.Fatal(err)
	}
	got := dv.AsStruct()
	if got.DataVersion != CurrentDataVersion {
		t.Fatalf("DataVersion = %d, want %d", got.DataVersion, CurrentDataVersion)
	}
	gotNetwork := got.DockerNetworks["svc"]
	if gotNetwork.EndpointAddrs != nil {
		t.Fatalf("EndpointAddrs = %#v, want nil after migration", gotNetwork.EndpointAddrs)
	}
	if gotEndpoint := gotNetwork.Endpoints["endpoint-id"]; gotEndpoint == nil {
		t.Fatal("migrated endpoint missing")
	} else if gotEndpoint.EndpointID != "endpoint-id" || gotEndpoint.IPv4 != legacyPrefix {
		t.Fatalf("migrated endpoint = %#v, want endpoint-id/%s", gotEndpoint, legacyPrefix)
	}

	onDisk := mustReadData(t, path)
	if onDisk.DataVersion != CurrentDataVersion {
		t.Fatalf("on-disk DataVersion = %d, want %d", onDisk.DataVersion, CurrentDataVersion)
	}
	if onDisk.DockerNetworks["svc"].EndpointAddrs != nil {
		t.Fatalf("on-disk EndpointAddrs = %#v, want nil", onDisk.DockerNetworks["svc"].EndpointAddrs)
	}
	if gotEndpoint := onDisk.DockerNetworks["svc"].Endpoints["endpoint-id"]; gotEndpoint == nil {
		t.Fatal("on-disk migrated endpoint missing")
	}

	backups, err := filepath.Glob(path + ".v4.*")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("migration backups = %v, want exactly one v4 backup", backups)
	}
	backup := mustReadData(t, backups[0])
	if backup.DataVersion != 4 {
		t.Fatalf("backup DataVersion = %d, want 4", backup.DataVersion)
	}
	if got := backup.DockerNetworks["svc"].EndpointAddrs["endpoint-id"]; got != legacyPrefix {
		t.Fatalf("backup EndpointAddrs = %s, want %s", got, legacyPrefix)
	}
}

func TestMigrateAddsServiceRootVersion(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "db.json")
	writeData(t, path, &Data{
		DataVersion: 5,
		Services: map[string]*Service{
			"svc": {
				Name:        "svc",
				ServiceType: ServiceTypeDockerCompose,
			},
		},
	})

	store := NewStore(path, filepath.Join(root, "services"))
	dv, err := store.Get()
	if err != nil {
		t.Fatal(err)
	}
	got := dv.AsStruct()
	if got.DataVersion != CurrentDataVersion {
		t.Fatalf("DataVersion = %d, want %d", got.DataVersion, CurrentDataVersion)
	}
	if got := got.Services["svc"].ServiceRoot; got != "" {
		t.Fatalf("migrated ServiceRoot = %q, want empty", got)
	}

	onDisk := mustReadData(t, path)
	if onDisk.DataVersion != CurrentDataVersion {
		t.Fatalf("on-disk DataVersion = %d, want %d", onDisk.DataVersion, CurrentDataVersion)
	}
	if got := onDisk.Services["svc"].ServiceRoot; got != "" {
		t.Fatalf("on-disk ServiceRoot = %q, want empty", got)
	}

	backups, err := filepath.Glob(path + ".v5.*")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("migration backups = %v, want exactly one v5 backup", backups)
	}
	backup := mustReadData(t, backups[0])
	if backup.DataVersion != 5 {
		t.Fatalf("backup DataVersion = %d, want 5", backup.DataVersion)
	}
	if got := backup.Services["svc"].ServiceRoot; got != "" {
		t.Fatalf("backup ServiceRoot = %q, want empty", got)
	}
}

func TestMigrateAddsServiceRootZFSVersion(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "db.json")
	writeData(t, path, &Data{
		DataVersion: 6,
		Services: map[string]*Service{
			"svc": {Name: "svc", ServiceRoot: "/srv/apps/svc"},
		},
	})

	store := NewStore(path, filepath.Join(root, "services"))
	dv, err := store.Get()
	if err != nil {
		t.Fatal(err)
	}
	got := dv.AsStruct()
	if got.DataVersion != CurrentDataVersion {
		t.Fatalf("DataVersion = %d, want %d", got.DataVersion, CurrentDataVersion)
	}
	if got := got.Services["svc"].ServiceRoot; got != "/srv/apps/svc" {
		t.Fatalf("migrated ServiceRoot = %q, want /srv/apps/svc", got)
	}
	if got := got.Services["svc"].ServiceRootZFS; got != "" {
		t.Fatalf("migrated ServiceRootZFS = %q, want empty", got)
	}

	onDisk := mustReadData(t, path)
	if onDisk.DataVersion != CurrentDataVersion {
		t.Fatalf("on-disk DataVersion = %d, want %d", onDisk.DataVersion, CurrentDataVersion)
	}
	if got := onDisk.Services["svc"].ServiceRootZFS; got != "" {
		t.Fatalf("on-disk ServiceRootZFS = %q, want empty", got)
	}

	backups, err := filepath.Glob(path + ".v6.*")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("migration backups = %v, want exactly one v6 backup", backups)
	}
	backup := mustReadData(t, backups[0])
	if backup.DataVersion != 6 {
		t.Fatalf("backup DataVersion = %d, want 6", backup.DataVersion)
	}
}

func TestMigrateAddsSnapshotPolicyVersion(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "db.json")
	writeData(t, path, &Data{
		DataVersion: 7,
		Services: map[string]*Service{
			"svc": {Name: "svc", ServiceRoot: "/srv/apps/svc", ServiceRootZFS: "tank/apps/svc"},
		},
	})
	store := NewStore(path, filepath.Join(root, "services"))
	got, err := store.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.DataVersion() != CurrentDataVersion {
		t.Fatalf("DataVersion = %d, want %d", got.DataVersion(), CurrentDataVersion)
	}
	sv, ok := got.Services().GetOk("svc")
	if !ok {
		t.Fatal("missing migrated service")
	}
	if sv.SnapshotPolicy().Valid() {
		t.Fatalf("service SnapshotPolicy valid = true, want false for inherited policy")
	}
	if got := sv.ServiceRoot(); got != "/srv/apps/svc" {
		t.Fatalf("migrated ServiceRoot = %q, want /srv/apps/svc", got)
	}
	if got := sv.ServiceRootZFS(); got != "tank/apps/svc" {
		t.Fatalf("migrated ServiceRootZFS = %q, want tank/apps/svc", got)
	}
	onDisk := mustReadData(t, path)
	if onDisk.DataVersion != CurrentDataVersion {
		t.Fatalf("on-disk DataVersion = %d, want %d", onDisk.DataVersion, CurrentDataVersion)
	}
	if got := onDisk.Services["svc"].ServiceRoot; got != "/srv/apps/svc" {
		t.Fatalf("on-disk ServiceRoot = %q, want /srv/apps/svc", got)
	}
	if got := onDisk.Services["svc"].ServiceRootZFS; got != "tank/apps/svc" {
		t.Fatalf("on-disk ServiceRootZFS = %q, want tank/apps/svc", got)
	}
	backups, err := filepath.Glob(path + ".v7.*")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("migration backups = %v, want exactly one v7 backup", backups)
	}
	backup := mustReadData(t, backups[0])
	if backup.DataVersion != 7 {
		t.Fatalf("backup DataVersion = %d, want 7", backup.DataVersion)
	}
	if got := backup.Services["svc"].ServiceRoot; got != "/srv/apps/svc" {
		t.Fatalf("backup ServiceRoot = %q, want /srv/apps/svc", got)
	}
	if got := backup.Services["svc"].ServiceRootZFS; got != "tank/apps/svc" {
		t.Fatalf("backup ServiceRootZFS = %q, want tank/apps/svc", got)
	}
}

func TestMigrateAddsVMServiceConfigVersion(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "db.json")
	writeData(t, path, &Data{
		DataVersion: 8,
		Services: map[string]*Service{
			"svc": {Name: "svc", ServiceType: ServiceTypeSystemd, ServiceRoot: "/srv/apps/svc"},
		},
	})
	store := NewStore(path, filepath.Join(root, "services"))
	got, err := store.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.DataVersion() != CurrentDataVersion {
		t.Fatalf("DataVersion = %d, want %d", got.DataVersion(), CurrentDataVersion)
	}
	sv, ok := got.Services().GetOk("svc")
	if !ok {
		t.Fatal("missing migrated service")
	}
	if sv.VM().Valid() {
		t.Fatalf("service VM valid = true, want false")
	}
	if got := sv.ServiceRoot(); got != "/srv/apps/svc" {
		t.Fatalf("migrated ServiceRoot = %q, want /srv/apps/svc", got)
	}
	onDisk := mustReadData(t, path)
	if onDisk.DataVersion != CurrentDataVersion {
		t.Fatalf("on-disk DataVersion = %d, want %d", onDisk.DataVersion, CurrentDataVersion)
	}
	if got := onDisk.Services["svc"].ServiceRoot; got != "/srv/apps/svc" {
		t.Fatalf("on-disk ServiceRoot = %q, want /srv/apps/svc", got)
	}
	backups, err := filepath.Glob(path + ".v8.*")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("migration backups = %v, want exactly one v8 backup", backups)
	}
	backup := mustReadData(t, backups[0])
	if backup.DataVersion != 8 {
		t.Fatalf("backup DataVersion = %d, want 8", backup.DataVersion)
	}
	if got := backup.Services["svc"].ServiceRoot; got != "/srv/apps/svc" {
		t.Fatalf("backup ServiceRoot = %q, want /srv/apps/svc", got)
	}
}

func TestStoreGetMigratesVersion10VMWithoutBalloonOrHost(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "db.json")
	if err := os.WriteFile(path, []byte(`{
  "DataVersion": 10,
  "Services": {
    "devbox": {
      "Name": "devbox",
      "ServiceType": "vm",
      "VM": {
        "Runtime": "firecracker",
        "CPUs": 2,
        "MemoryBytes": 4294967296,
        "SetupState": "ready"
      }
    }
  }
}
`), 0644); err != nil {
		t.Fatal(err)
	}

	store := NewStore(path, filepath.Join(root, "services"))
	got, err := store.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.DataVersion() != CurrentDataVersion {
		t.Fatalf("DataVersion = %d, want %d", got.DataVersion(), CurrentDataVersion)
	}
	if got.VMHost().Valid() {
		t.Fatalf("VMHost valid = true, want false")
	}
	sv, ok := got.Services().GetOk("devbox")
	if !ok {
		t.Fatal("missing migrated VM service")
	}
	if got := sv.VM().SetupState(); got != "ready" {
		t.Fatalf("migrated VM SetupState = %q, want ready", got)
	}
	if got := sv.VM().Balloon(); got != (VMBalloonConfig{}) {
		t.Fatalf("migrated VM Balloon = %#v, want zero value", got)
	}

	onDisk := mustReadData(t, path)
	if onDisk.DataVersion != CurrentDataVersion {
		t.Fatalf("on-disk DataVersion = %d, want %d", onDisk.DataVersion, CurrentDataVersion)
	}
	if onDisk.VMHost != nil {
		t.Fatalf("on-disk VMHost = %#v, want nil", onDisk.VMHost)
	}
	if got := onDisk.Services["devbox"].VM.Balloon; got != (VMBalloonConfig{}) {
		t.Fatalf("on-disk VM Balloon = %#v, want zero value", got)
	}

	backups, err := filepath.Glob(path + ".v10.*")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("migration backups = %v, want exactly one v10 backup", backups)
	}
	backup := mustReadData(t, backups[0])
	if backup.DataVersion != 10 {
		t.Fatalf("backup DataVersion = %d, want 10", backup.DataVersion)
	}
	if backup.VMHost != nil {
		t.Fatalf("backup VMHost = %#v, want nil", backup.VMHost)
	}
	if got := backup.Services["devbox"].VM.Balloon; got != (VMBalloonConfig{}) {
		t.Fatalf("backup VM Balloon = %#v, want zero value", got)
	}
}

func TestStoreGetMigratesVersion11VMComponents(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "db.json")
	if err := os.WriteFile(path, []byte(`{
  "DataVersion": 11,
  "Services": {
    "devbox": {
      "Name": "devbox",
      "ServiceType": "vm",
      "VM": {
        "Runtime": "firecracker",
        "Image": {
          "Payload": "vm://ubuntu/26.04",
          "Version": "ubuntu-26.04-amd64-v29",
          "Digest": "sha256:legacy-image",
          "Kernel": "/var/lib/yeet/images/v29/vmlinux",
          "RootFS": "/var/lib/yeet/images/v29/rootfs.ext4"
        },
        "CPUs": 2,
        "MemoryBytes": 4294967296,
        "SetupState": "ready"
      }
    }
  }
}
`), 0644); err != nil {
		t.Fatal(err)
	}

	store := NewStore(path, filepath.Join(root, "services"))
	got, err := store.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.DataVersion() != CurrentDataVersion {
		t.Fatalf("DataVersion = %d, CurrentDataVersion = %d; want current", got.DataVersion(), CurrentDataVersion)
	}
	if got.VMHost().Valid() {
		t.Fatal("migration invented a VMHost runtime policy container")
	}
	service, ok := got.Services().GetOk("devbox")
	if !ok {
		t.Fatal("missing migrated VM service")
	}
	vm := service.VM()
	if got := vm.Image(); got.Payload != "vm://ubuntu/26.04" || got.Version != "ubuntu-26.04-amd64-v29" || got.Digest != "sha256:legacy-image" || got.Kernel != "/var/lib/yeet/images/v29/vmlinux" || got.RootFS != "/var/lib/yeet/images/v29/rootfs.ext4" {
		t.Fatalf("migrated VM image = %#v; legacy image identity was not preserved", got)
	}
	if vm.Components().Valid() {
		t.Fatalf("migration invented VM component identity: %#v", vm.Components().AsStruct())
	}

	onDisk := mustReadData(t, path)
	if onDisk.DataVersion != CurrentDataVersion {
		t.Fatalf("on-disk DataVersion = %d, want %d", onDisk.DataVersion, CurrentDataVersion)
	}
	if onDisk.VMHost != nil {
		t.Fatalf("on-disk VMHost = %#v, want nil", onDisk.VMHost)
	}
	if onDisk.Services["devbox"].VM.Components != nil {
		t.Fatalf("on-disk components = %#v, want nil", onDisk.Services["devbox"].VM.Components)
	}
	if got := onDisk.Services["devbox"].VM.Image.Version; got != "ubuntu-26.04-amd64-v29" {
		t.Fatalf("on-disk VM image version = %q, want ubuntu-26.04-amd64-v29", got)
	}

	backups, err := filepath.Glob(path + ".v11.*")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("migration backups = %v, want exactly one v11 backup", backups)
	}
	backup := mustReadData(t, backups[0])
	if backup.DataVersion != 11 {
		t.Fatalf("backup DataVersion = %d, want 11", backup.DataVersion)
	}
	if backup.Services["devbox"].VM.Components != nil {
		t.Fatalf("backup components = %#v, want nil", backup.Services["devbox"].VM.Components)
	}
	if got := backup.Services["devbox"].VM.Image.Version; got != "ubuntu-26.04-amd64-v29" {
		t.Fatalf("backup VM image version = %q, want ubuntu-26.04-amd64-v29", got)
	}
}

func TestStoreGetDoesNotCacheFailedMigrationSave(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "db.json")
	writeData(t, path, &Data{
		DataVersion: 8,
		Services: map[string]*Service{
			"svc": {Name: "svc", ServiceType: ServiceTypeSystemd, ServiceRoot: "/srv/apps/svc"},
		},
	})
	store := NewStore(path, filepath.Join(root, "services"))

	oldRename := renameDBFile
	failRename := true
	renameDBFile = func(oldPath, newPath string) error {
		if failRename {
			return os.ErrPermission
		}
		return oldRename(oldPath, newPath)
	}
	t.Cleanup(func() {
		renameDBFile = oldRename
	})

	if _, err := store.Get(); err == nil {
		t.Fatal("Get succeeded after migration save failure")
	}
	if got := mustReadData(t, path).DataVersion; got != 8 {
		t.Fatalf("on-disk DataVersion after failed migration = %d, want 8", got)
	}

	if got, err := store.Get(); err == nil {
		t.Fatalf("second Get after failed migration save = DataVersion %d, want error", got.DataVersion())
	}

	failRename = false
	got, err := store.Get()
	if err != nil {
		t.Fatalf("Get after restoring rename: %v", err)
	}
	if got.DataVersion() != CurrentDataVersion {
		t.Fatalf("DataVersion after retry = %d, want %d", got.DataVersion(), CurrentDataVersion)
	}
	if got := mustReadData(t, path).DataVersion; got != CurrentDataVersion {
		t.Fatalf("on-disk DataVersion after retry = %d, want %d", got, CurrentDataVersion)
	}
}

func TestStoreGetMigratesVersion12WithBackupAndRetriesFailedSave(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "db.json")
	writeData(t, path, &Data{
		DataVersion: 12,
		Services: map[string]*Service{
			"api": {Name: "api", ServiceType: ServiceTypeSystemd},
		},
	})
	store := NewStore(path, filepath.Join(root, "services"))

	oldRename := renameDBFile
	failRename := true
	renameDBFile = func(oldPath, newPath string) error {
		if failRename {
			return os.ErrPermission
		}
		return oldRename(oldPath, newPath)
	}
	t.Cleanup(func() {
		renameDBFile = oldRename
	})

	if _, err := store.Get(); err == nil {
		t.Fatal("Get succeeded after schema-12 migration save failure")
	}
	onDisk := mustReadData(t, path)
	if onDisk.DataVersion != 12 || onDisk.Services["api"].Identity != nil {
		t.Fatalf("on-disk data after failed migration = %#v", onDisk)
	}
	backups, err := filepath.Glob(path + ".v12.*")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("migration backups = %v, want exactly one v12 backup", backups)
	}
	backup := mustReadData(t, backups[0])
	if backup.DataVersion != 12 || backup.Services["api"].Identity != nil {
		t.Fatalf("backup data = %#v", backup)
	}

	failRename = false
	got, err := store.Get()
	if err != nil {
		t.Fatalf("Get after restoring rename: %v", err)
	}
	if got.DataVersion() != CurrentDataVersion || got.Services().Get("api").Identity().Valid() {
		t.Fatalf("migrated view = %#v", got.AsStruct())
	}
	onDisk = mustReadData(t, path)
	if onDisk.DataVersion != CurrentDataVersion || onDisk.Services["api"].Identity != nil {
		t.Fatalf("on-disk data after retry = %#v", onDisk)
	}
}

func TestStoreCachesPublishedMigrationAfterDurabilityWarning(t *testing.T) {
	operations := []struct {
		name string
		run  func(*Store, error) error
	}{
		{name: "Get", run: func(store *Store, _ error) error {
			_, err := store.Get()
			return err
		}},
		{name: "MutateData", run: func(store *Store, callbackErr error) error {
			_, err := store.MutateData(func(*Data) error { return callbackErr })
			return err
		}},
	}
	failures := []struct {
		name        string
		inject      func(*Store, error)
		callbackErr error
	}{
		{name: "parent sync", inject: func(store *Store, injected error) {
			afterSuccessfulDBRename(store, func() {
				store.deps.syncDir = func(*os.File) error { return injected }
			})
		}},
		{name: "unlock", callbackErr: errors.New("stop after migration"), inject: func(store *Store, injected error) {
			realUnlock := store.deps.unlockFile
			store.deps.unlockFile = func(lock *os.File) error {
				return errors.Join(realUnlock(lock), injected)
			}
		}},
	}
	for _, operation := range operations {
		for _, failure := range failures {
			t.Run(operation.name+"/"+failure.name, func(t *testing.T) {
				root := t.TempDir()
				path := filepath.Join(root, "db.json")
				writeData(t, path, &Data{
					DataVersion: 11,
					Services:    map[string]*Service{"svc": {Name: "svc", ServiceType: ServiceTypeSystemd}},
				})
				store := NewStore(path, filepath.Join(root, "services"))
				injected := errors.New("injected migration " + failure.name + " failure")
				failure.inject(store, injected)

				err := operation.run(store, failure.callbackErr)
				if !errors.Is(err, injected) {
					t.Fatalf("%s error = %v, want injected failure", operation.name, err)
				}
				var publishedErr *PostPublicationError
				if !errors.As(err, &publishedErr) {
					t.Fatalf("%s error = %v, want *PostPublicationError", operation.name, err)
				}
				if publishedErr.MutationCommitted {
					t.Fatalf("%s outcome says requested mutation committed after migration-only publication", operation.name)
				}
				if got := mustReadData(t, path).DataVersion; got != CurrentDataVersion {
					t.Fatalf("on-disk DataVersion = %d, want %d", got, CurrentDataVersion)
				}
				cached, getErr := store.Get()
				if getErr != nil {
					t.Fatalf("Get cached migration: %v", getErr)
				}
				if got := cached.DataVersion(); got != CurrentDataVersion {
					t.Fatalf("cached DataVersion = %d, want %d", got, CurrentDataVersion)
				}
			})
		}
	}
}

func TestArtifactStoreRefs(t *testing.T) {
	refs := ArtifactStore{
		ArtifactBinary: {
			Refs: map[ArtifactRef]string{
				Gen(7):   "/srv/svc/gen-7",
				"staged": "/srv/svc/staged",
				"latest": "/srv/svc/latest",
			},
		},
	}

	if got, ok := refs.Gen(ArtifactBinary, 7); !ok || got != "/srv/svc/gen-7" {
		t.Fatalf("Gen() = %q, %v; want /srv/svc/gen-7, true", got, ok)
	}
	if got, ok := refs.Staged(ArtifactBinary); !ok || got != "/srv/svc/staged" {
		t.Fatalf("Staged() = %q, %v; want /srv/svc/staged, true", got, ok)
	}
	if got, ok := refs.Latest(ArtifactBinary); !ok || got != "/srv/svc/latest" {
		t.Fatalf("Latest() = %q, %v; want /srv/svc/latest, true", got, ok)
	}
	if got, ok := refs.Gen(ArtifactEnvFile, 7); ok || got != "" {
		t.Fatalf("missing Gen() = %q, %v; want empty, false", got, ok)
	}
	if got, ok := refs.Staged(ArtifactEnvFile); ok || got != "" {
		t.Fatalf("missing Staged() = %q, %v; want empty, false", got, ok)
	}
	if got, ok := refs.Latest(ArtifactEnvFile); ok || got != "" {
		t.Fatalf("missing Latest() = %q, %v; want empty, false", got, ok)
	}
}

func TestProtoPortParseAndString(t *testing.T) {
	var pp ProtoPort
	if err := pp.Parse("6/443"); err != nil {
		t.Fatal(err)
	}
	if pp.Proto != 6 || pp.Port != 443 {
		t.Fatalf("parsed ProtoPort = %#v, want proto 6 port 443", pp)
	}
	if got := pp.String(); got != "6/443" {
		t.Fatalf("String() = %q, want 6/443", got)
	}
	if err := pp.Parse("invalid"); err == nil {
		t.Fatal("Parse succeeded for invalid proto/port")
	}
}

func TestStoreGetReportsInvalidJSONAndMissingMigrator(t *testing.T) {
	t.Run("invalid JSON", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "db.json")
		if err := os.WriteFile(path, []byte("{"), 0644); err != nil {
			t.Fatal(err)
		}

		store := NewStore(path, filepath.Join(t.TempDir(), "services"))
		if _, err := store.Get(); err == nil {
			t.Fatal("Get succeeded for invalid JSON")
		}
	})

	t.Run("missing migrator", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "db.json")
		writeData(t, path, &Data{DataVersion: 2})

		store := NewStore(path, filepath.Join(t.TempDir(), "services"))
		if _, err := store.Get(); err == nil {
			t.Fatal("Get succeeded for data with no migrator")
		}
	})
}

func TestStoreGetRejectsNullDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.json")
	if err := os.WriteFile(path, []byte("null\n"), 0644); err != nil {
		t.Fatal(err)
	}

	store := NewStore(path, filepath.Join(t.TempDir(), "services"))
	if _, err := store.Get(); err == nil {
		t.Fatal("Get succeeded for a null database")
	}
}

func TestStoreMutateDataLoadsClonesAndPersistsChanges(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "db.json")
	writeData(t, path, &Data{
		DataVersion: CurrentDataVersion,
		Services: map[string]*Service{
			"svc": {Name: "svc", Generation: 1},
		},
	})
	store := NewStore(path, filepath.Join(root, "services"))

	got, err := store.MutateData(func(d *Data) error {
		d.Services["svc"].Generation = 2
		d.Volumes = map[string]*Volume{"data": {Name: "data", Path: "/data"}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Services["svc"].Generation != 2 {
		t.Fatalf("returned generation = %d, want 2", got.Services["svc"].Generation)
	}

	got.Services["svc"].Generation = 99
	onDisk := mustReadData(t, path)
	if onDisk.Services["svc"].Generation != 2 {
		t.Fatalf("on-disk generation = %d, want 2", onDisk.Services["svc"].Generation)
	}
	if onDisk.Volumes["data"].Path != "/data" {
		t.Fatalf("on-disk volume path = %q, want /data", onDisk.Volumes["data"].Path)
	}
	dv, err := store.Get()
	if err != nil {
		t.Fatal(err)
	}
	if got := dv.AsStruct().Services["svc"].Generation; got != 2 {
		t.Fatalf("cached generation = %d, want 2", got)
	}
}

func TestStoreMutateDataWrapsCallbackErrorsWithoutSaving(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "db.json")
	writeData(t, path, &Data{
		DataVersion: CurrentDataVersion,
		Services: map[string]*Service{
			"svc": {Name: "svc", Generation: 1},
		},
	})
	store := NewStore(path, filepath.Join(root, "services"))

	_, err := store.MutateData(func(d *Data) error {
		d.Services["svc"].Generation = 2
		return os.ErrPermission
	})
	if err == nil {
		t.Fatal("MutateData succeeded after callback error")
	}
	if got := mustReadData(t, path).Services["svc"].Generation; got != 1 {
		t.Fatalf("on-disk generation after failed mutation = %d, want 1", got)
	}
}

func TestStoreMutateDataKeepsCachedViewAfterSaveFailure(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "db.json")
	writeData(t, path, &Data{
		DataVersion: CurrentDataVersion,
		Services: map[string]*Service{
			"svc": {Name: "svc", Generation: 1},
		},
	})
	store := NewStore(path, filepath.Join(root, "services"))

	oldRename := renameDBFile
	renameCalled := false
	renameDBFile = func(_, _ string) error {
		renameCalled = true
		return os.ErrPermission
	}
	t.Cleanup(func() {
		renameDBFile = oldRename
	})

	_, err := store.MutateData(func(d *Data) error {
		d.Services["svc"].Generation = 2
		return nil
	})
	if err == nil {
		t.Fatal("MutateData succeeded after save failure")
	}
	if !renameCalled {
		t.Fatal("rename hook was not called")
	}
	dv, err := store.Get()
	if err != nil {
		t.Fatal(err)
	}
	if got := dv.AsStruct().Services["svc"].Generation; got != 1 {
		t.Fatalf("cached generation after failed save = %d, want 1", got)
	}
	if got := mustReadData(t, path).Services["svc"].Generation; got != 1 {
		t.Fatalf("on-disk generation after failed save = %d, want 1", got)
	}
}

func TestStoreMutateServiceCreatesAndUpdatesServices(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "db.json")
	store := NewStore(path, filepath.Join(root, "services"))

	data, svc, err := store.MutateService("svc", func(d *Data, svc *Service) error {
		if d.Services["svc"] != svc {
			t.Fatal("callback service is not stored in data map")
		}
		svc.ServiceType = ServiceTypeSystemd
		svc.Generation = 1
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if svc.Name != "svc" || svc.ServiceType != ServiceTypeSystemd || svc.Generation != 1 {
		t.Fatalf("created service = %#v, want systemd generation 1", svc)
	}
	if data.Services["svc"] != svc {
		t.Fatal("returned data does not include returned service pointer")
	}

	_, svc, err = store.MutateService("svc", func(_ *Data, svc *Service) error {
		svc.Generation++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if svc.Generation != 2 {
		t.Fatalf("updated service generation = %d, want 2", svc.Generation)
	}
	if got := mustReadData(t, path).Services["svc"].Generation; got != 2 {
		t.Fatalf("on-disk generation = %d, want 2", got)
	}
}

func TestStoreMutateDataReloadsLatestAcrossStores(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "db.json")
	servicesRoot := filepath.Join(root, "services")
	storeA := NewStore(path, servicesRoot)
	storeB := NewStore(path, servicesRoot)
	if err := storeA.Set(&Data{
		DataVersion: CurrentDataVersion,
		Services: map[string]*Service{
			"svc": {Name: "svc", Generation: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := storeA.Get(); err != nil {
		t.Fatal(err)
	}
	if _, err := storeB.MutateData(func(d *Data) error {
		d.Volumes = map[string]*Volume{"other": {Name: "other", Path: "/other"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := storeA.MutateData(func(d *Data) error {
		d.Services["svc"].Generation = 2
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	got := mustReadData(t, path)
	if got.Services["svc"].Generation != 2 {
		t.Fatalf("generation = %d, want 2", got.Services["svc"].Generation)
	}
	if got.Volumes["other"] == nil || got.Volumes["other"].Path != "/other" {
		t.Fatalf("unrelated volume was lost: %#v", got.Volumes)
	}
}

func TestStoreMutateDataSerializesAcrossStores(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "db.json")
	servicesRoot := filepath.Join(root, "services")
	storeA := NewStore(path, servicesRoot)
	storeB := NewStore(path, servicesRoot)
	if err := storeA.Set(&Data{DataVersion: CurrentDataVersion}); err != nil {
		t.Fatal(err)
	}

	enteredA := make(chan struct{})
	releaseA := make(chan struct{})
	doneA := make(chan error, 1)
	go func() {
		_, err := storeA.MutateData(func(d *Data) error {
			close(enteredA)
			<-releaseA
			d.Volumes = map[string]*Volume{"a": {Name: "a", Path: "/a"}}
			return nil
		})
		doneA <- err
	}()
	<-enteredA

	enteredB := make(chan struct{})
	doneB := make(chan error, 1)
	attemptedBLock := make(chan struct{})
	realLockB := storeB.deps.lockFile
	storeB.deps.lockFile = func(file *os.File) error {
		close(attemptedBLock)
		return realLockB(file)
	}
	go func() {
		_, err := storeB.MutateData(func(d *Data) error {
			close(enteredB)
			if d.Volumes["a"] == nil {
				return errors.New("second mutation did not observe first mutation")
			}
			d.Volumes["b"] = &Volume{Name: "b", Path: "/b"}
			return nil
		})
		doneB <- err
	}()
	<-attemptedBLock
	select {
	case <-enteredB:
		t.Fatal("second Store entered mutation callback while first held the database lock")
	default:
	}
	close(releaseA)
	if err := <-doneA; err != nil {
		t.Fatal(err)
	}
	if err := <-doneB; err != nil {
		t.Fatal(err)
	}
	got := mustReadData(t, path)
	if got.Volumes["a"] == nil || got.Volumes["b"] == nil {
		t.Fatalf("serialized volumes = %#v, want a and b", got.Volumes)
	}
}

func TestStoreWithLatestDataLockedReloadsAndBlocksWriterAcrossStores(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "db.json")
	servicesRoot := filepath.Join(root, "services")
	storeA := NewStore(path, servicesRoot)
	storeB := NewStore(path, servicesRoot)
	if err := storeA.Set(&Data{
		DataVersion: CurrentDataVersion,
		Services: map[string]*Service{
			"svc": {Name: "svc", Generation: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := storeA.Get(); err != nil {
		t.Fatal(err)
	}
	if _, err := storeB.MutateData(func(d *Data) error {
		d.Services["svc"].Generation = 2
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	enteredFinalizer := make(chan struct{})
	releaseFinalizer := make(chan struct{})
	observedGeneration := make(chan int, 1)
	finalizerDone := make(chan error, 1)
	go func() {
		finalizerDone <- storeA.WithLatestDataLocked(func(dv DataView) error {
			observedGeneration <- dv.AsStruct().Services["svc"].Generation
			close(enteredFinalizer)
			<-releaseFinalizer
			return nil
		})
	}()
	<-enteredFinalizer
	if got := <-observedGeneration; got != 2 {
		t.Fatalf("finalizer generation = %d, want fresh on-disk generation 2", got)
	}

	attemptedWriterLock := make(chan struct{})
	realLock := storeB.deps.lockFile
	storeB.deps.lockFile = func(file *os.File) error {
		close(attemptedWriterLock)
		return realLock(file)
	}
	enteredWriter := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		_, err := storeB.MutateData(func(d *Data) error {
			close(enteredWriter)
			d.Services["svc"].Generation = 3
			return nil
		})
		writerDone <- err
	}()
	<-attemptedWriterLock
	select {
	case <-enteredWriter:
		t.Fatal("writer entered while latest-data finalizer held the database lock")
	default:
	}

	close(releaseFinalizer)
	if err := <-finalizerDone; err != nil {
		t.Fatal(err)
	}
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}
	if got := mustReadData(t, path).Services["svc"].Generation; got != 3 {
		t.Fatalf("on-disk generation = %d, want serialized writer generation 3", got)
	}
}

func TestStoreWithLatestDataLockedDoesNotRewriteDatabase(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "db.json")
	store := NewStore(path, filepath.Join(root, "services"))
	if err := store.Set(&Data{
		DataVersion: CurrentDataVersion,
		Services:    map[string]*Service{"svc": {Name: "svc", Generation: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.WithLatestDataLocked(func(dv DataView) error {
		if got := dv.AsStruct().Services["svc"].Generation; got != 1 {
			t.Fatalf("finalizer generation = %d, want 1", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	afterRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(beforeInfo, afterInfo) {
		t.Fatal("WithLatestDataLocked replaced db.json on ordinary success")
	}
	if !bytes.Equal(beforeRaw, afterRaw) {
		t.Fatal("WithLatestDataLocked changed db.json contents on ordinary success")
	}
}

func TestStoreWithLatestDataLockedIsolatesCallbackMutation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "db.json")
	store := NewStore(path, filepath.Join(root, "services"))
	if err := store.Set(&Data{
		DataVersion: CurrentDataVersion,
		Services:    map[string]*Service{"svc": {Name: "svc", Generation: 1}},
	}); err != nil {
		t.Fatal(err)
	}

	if err := store.WithLatestDataLocked(func(dv DataView) error {
		isolated := dv.AsStruct()
		isolated.Services["svc"].Generation = 99
		isolated.Services["new"] = &Service{Name: "new"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	cached, err := store.Get()
	if err != nil {
		t.Fatal(err)
	}
	if got := cached.AsStruct().Services["svc"].Generation; got != 1 {
		t.Fatalf("cached generation = %d, want isolated generation 1", got)
	}
	if _, ok := cached.AsStruct().Services["new"]; ok {
		t.Fatal("callback mutation leaked into cached data")
	}
	if got := mustReadData(t, path).Services["svc"].Generation; got != 1 {
		t.Fatalf("on-disk generation = %d, want isolated generation 1", got)
	}
}

func TestStoreWithLatestDataLockedReturnsCallbackError(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "db.json")
	store := NewStore(path, filepath.Join(root, "services"))
	if err := store.Set(&Data{DataVersion: CurrentDataVersion}); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("finalizer rejected latest data")

	err := store.WithLatestDataLocked(func(DataView) error { return injected })
	if !errors.Is(err, injected) {
		t.Fatalf("WithLatestDataLocked error = %v, want callback error", err)
	}
	var postFinalization *PostFinalizationError
	if errors.As(err, &postFinalization) {
		t.Fatalf("WithLatestDataLocked error = %v, want ordinary callback error", err)
	}
}

func TestStoreWithLatestDataLockedReportsCleanupOutcome(t *testing.T) {
	cleanupFailures := []struct {
		name   string
		inject func(*Store, error)
	}{
		{name: "unlock", inject: func(store *Store, injected error) {
			realUnlock := store.deps.unlockFile
			store.deps.unlockFile = func(file *os.File) error {
				return errors.Join(realUnlock(file), injected)
			}
		}},
		{name: "close", inject: func(store *Store, injected error) {
			realClose := store.deps.closeLockFile
			store.deps.closeLockFile = func(file *os.File) error {
				return errors.Join(realClose(file), injected)
			}
		}},
	}
	callbackOutcomes := []struct {
		name      string
		err       error
		completed bool
	}{
		{name: "completed", completed: true},
		{name: "failed", err: errors.New("finalizer failed")},
	}
	for _, cleanup := range cleanupFailures {
		for _, callback := range callbackOutcomes {
			t.Run(cleanup.name+"/"+callback.name, func(t *testing.T) {
				root := t.TempDir()
				path := filepath.Join(root, "db.json")
				store := NewStore(path, filepath.Join(root, "services"))
				if err := store.Set(&Data{DataVersion: CurrentDataVersion}); err != nil {
					t.Fatal(err)
				}
				cleanupErr := errors.New(cleanup.name + " failed")
				cleanup.inject(store, cleanupErr)

				err := store.WithLatestDataLocked(func(DataView) error { return callback.err })
				if !errors.Is(err, cleanupErr) {
					t.Fatalf("WithLatestDataLocked error = %v, want cleanup error", err)
				}
				if callback.err != nil && !errors.Is(err, callback.err) {
					t.Fatalf("WithLatestDataLocked error = %v, want callback error", err)
				}
				var outcome *PostFinalizationError
				if !errors.As(err, &outcome) {
					t.Fatalf("WithLatestDataLocked error = %v, want *PostFinalizationError", err)
				}
				if outcome.FinalizerCompleted != callback.completed {
					t.Fatalf("FinalizerCompleted = %t, want %t", outcome.FinalizerCompleted, callback.completed)
				}
			})
		}
	}
}

func TestStoreWithLatestDataLockedReleasesLockAfterNonLocalExit(t *testing.T) {
	nonLocalExits := []struct {
		name string
		run  func(*testing.T, *Store)
	}{
		{name: "panic", run: func(t *testing.T, store *Store) {
			const panicValue = "finalizer panic"
			func() {
				defer func() {
					if got := recover(); got != panicValue {
						t.Errorf("recovered value = %#v, want %q", got, panicValue)
					}
				}()
				if err := store.WithLatestDataLocked(func(DataView) error {
					panic(panicValue)
				}); err != nil {
					t.Errorf("WithLatestDataLocked returned error instead of panicking: %v", err)
				}
				t.Error("WithLatestDataLocked returned instead of panicking")
			}()
		}},
		{name: "goexit", run: func(t *testing.T, store *Store) {
			done := make(chan struct{})
			go func() {
				defer close(done)
				if err := store.WithLatestDataLocked(func(DataView) error {
					runtime.Goexit()
					return nil
				}); err != nil {
					t.Errorf("WithLatestDataLocked returned error after Goexit: %v", err)
				}
				t.Error("WithLatestDataLocked returned after Goexit")
			}()
			<-done
		}},
	}

	for _, exit := range nonLocalExits {
		t.Run(exit.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "db.json")
			servicesRoot := filepath.Join(root, "services")
			store := NewStore(path, servicesRoot)
			if err := store.Set(&Data{
				DataVersion: CurrentDataVersion,
				Services:    map[string]*Service{"svc": {Name: "svc", Generation: 1}},
			}); err != nil {
				t.Fatal(err)
			}

			unlocked := make(chan struct{}, 1)
			closed := make(chan struct{}, 1)
			realUnlock := store.deps.unlockFile
			store.deps.unlockFile = func(file *os.File) error {
				err := realUnlock(file)
				unlocked <- struct{}{}
				return err
			}
			realClose := store.deps.closeLockFile
			store.deps.closeLockFile = func(file *os.File) error {
				err := realClose(file)
				closed <- struct{}{}
				return err
			}

			exit.run(t, store)
			for name, signal := range map[string]<-chan struct{}{
				"unlock": unlocked,
				"close":  closed,
			} {
				select {
				case <-signal:
				default:
					t.Fatalf("%s was not called after %s", name, exit.name)
				}
			}

			if _, err := store.MutateData(func(d *Data) error {
				d.Services["svc"].Generation = 2
				return nil
			}); err != nil {
				t.Fatalf("same-Store writer after %s: %v", exit.name, err)
			}
			otherStore := NewStore(path, servicesRoot)
			if _, err := otherStore.MutateData(func(d *Data) error {
				d.Services["svc"].Generation = 3
				return nil
			}); err != nil {
				t.Fatalf("cross-Store writer after %s: %v", exit.name, err)
			}
			if got := mustReadData(t, path).Services["svc"].Generation; got != 3 {
				t.Fatalf("on-disk generation after %s = %d, want 3", exit.name, got)
			}
		})
	}
}

func TestStoreWithLatestDataLockedDoesNotFinalizeAfterMigrationDurabilityError(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "db.json")
	writeData(t, path, &Data{
		DataVersion: 11,
		Services:    map[string]*Service{"svc": {Name: "svc", ServiceType: ServiceTypeSystemd}},
	})
	store := NewStore(path, filepath.Join(root, "services"))
	injected := errors.New("migration parent sync failed")
	afterSuccessfulDBRename(store, func() {
		store.deps.syncDir = func(*os.File) error { return injected }
	})
	finalizerCalled := false

	err := store.WithLatestDataLocked(func(DataView) error {
		finalizerCalled = true
		return nil
	})
	if !errors.Is(err, injected) {
		t.Fatalf("WithLatestDataLocked error = %v, want migration durability error", err)
	}
	var publishedErr *PostPublicationError
	if !errors.As(err, &publishedErr) || publishedErr.MutationCommitted {
		t.Fatalf("WithLatestDataLocked error = %v, want migration-only *PostPublicationError", err)
	}
	if finalizerCalled {
		t.Fatal("finalizer ran after ambiguous migration publication")
	}
	if got := mustReadData(t, path).DataVersion; got != CurrentDataVersion {
		t.Fatalf("on-disk DataVersion = %d, want published migration %d", got, CurrentDataVersion)
	}
	cached, getErr := store.Get()
	if getErr != nil {
		t.Fatal(getErr)
	}
	if got := cached.DataVersion(); got != CurrentDataVersion {
		t.Fatalf("cached DataVersion = %d, want %d", got, CurrentDataVersion)
	}
}

func TestStoreDurablyCreatesNestedDatabaseParents(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "custom")
	second := filepath.Join(first, "data")
	path := filepath.Join(second, "db.json")
	store := NewStore(path, filepath.Join(root, "services"))
	realOpen := store.deps.openDir
	realSync := store.deps.syncDir
	var synced []string
	store.deps.openDir = func(path string) (*os.File, error) {
		return realOpen(path)
	}
	store.deps.syncDir = func(dir *os.File) error {
		synced = append(synced, filepath.Clean(dir.Name()))
		return realSync(dir)
	}

	if err := store.Set(&Data{DataVersion: CurrentDataVersion}); err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Dir(filepath.Clean(root)), filepath.Clean(root), first, first, second}
	if !reflect.DeepEqual(synced, want) {
		t.Fatalf("synced database parent links = %#v, want %#v", synced, want)
	}
}

func TestStoreStopsBeforePublicationWhenCreatedParentSyncFails(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "custom", "data", "db.json")
	store := NewStore(path, filepath.Join(root, "services"))
	injected := errors.New("sync created parent")
	realSync := store.deps.syncDir
	store.deps.syncDir = func(dir *os.File) error {
		if filepath.Clean(dir.Name()) == filepath.Clean(root) {
			return injected
		}
		return realSync(dir)
	}

	err := store.Set(&Data{DataVersion: CurrentDataVersion})
	if !errors.Is(err, injected) {
		t.Fatalf("Set error = %v, want created-parent sync failure", err)
	}
	var publishedErr *PostPublicationError
	if errors.As(err, &publishedErr) {
		t.Fatalf("Set error = %v, want pre-publication failure", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("database file stat error = %v, want not exist", statErr)
	}
}

func TestStoreRetriesSyncForExistingParentAfterCreatedLinkSyncFailure(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "custom")
	path := filepath.Join(first, "data", "db.json")
	store := NewStore(path, filepath.Join(root, "services"))
	injected := errors.New("sync created parent")
	realSync := store.deps.syncDir
	failFirst := true
	store.deps.syncDir = func(dir *os.File) error {
		if failFirst && filepath.Clean(dir.Name()) == filepath.Clean(root) {
			return injected
		}
		return realSync(dir)
	}
	if err := store.Set(&Data{DataVersion: CurrentDataVersion}); !errors.Is(err, injected) {
		t.Fatalf("first Set error = %v, want created-link sync failure", err)
	}
	if info, err := os.Stat(first); err != nil || !info.IsDir() {
		t.Fatalf("created first parent stat = %v, %#v", err, info)
	}

	failFirst = false
	retryStore := NewStore(path, filepath.Join(root, "services"))
	var synced []string
	retryStore.deps.syncDir = func(dir *os.File) error {
		synced = append(synced, filepath.Clean(dir.Name()))
		return realSync(dir)
	}
	if err := retryStore.Set(&Data{DataVersion: CurrentDataVersion}); err != nil {
		t.Fatalf("retry Set: %v", err)
	}
	if !slices.Contains(synced, filepath.Clean(root)) {
		t.Fatalf("retry synced directories = %#v, want parent link %s", synced, root)
	}
}

func TestStoreSyncsParentLinkAfterConcurrentMkdir(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "custom")
	path := filepath.Join(first, "db.json")
	store := NewStore(path, filepath.Join(root, "services"))
	realMkdir := store.deps.mkdirDir
	store.deps.mkdirDir = func(path string, mode os.FileMode) error {
		if filepath.Clean(path) == first {
			if err := realMkdir(path, mode); err != nil {
				return err
			}
			return os.ErrExist
		}
		return realMkdir(path, mode)
	}
	realSync := store.deps.syncDir
	var synced []string
	store.deps.syncDir = func(dir *os.File) error {
		synced = append(synced, filepath.Clean(dir.Name()))
		return realSync(dir)
	}

	if err := store.Set(&Data{DataVersion: CurrentDataVersion}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(synced, filepath.Clean(root)) {
		t.Fatalf("synced directories = %#v, want concurrent parent link %s", synced, root)
	}
}

func TestStorePrePublicationCompensationRunsWhileFileLockHeld(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "db.json")
	storeA := NewStore(path, filepath.Join(root, "services"))
	storeB := NewStore(path, filepath.Join(root, "services"))
	if err := storeA.Set(&Data{DataVersion: CurrentDataVersion, Services: map[string]*Service{"svc": {Name: "svc", Generation: 1}}}); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("temporary database sync failed")
	storeA.deps.syncFile = func(*os.File) error { return injected }
	compensationEntered := make(chan struct{})
	releaseCompensation := make(chan struct{})
	mutationDone := make(chan error, 1)
	go func() {
		_, err := storeA.MutateDataWithPrePublicationCompensation(func(d *Data) (func() error, error) {
			d.Services["svc"].Generation = 2
			return func() error {
				close(compensationEntered)
				<-releaseCompensation
				return nil
			}, nil
		})
		mutationDone <- err
	}()
	<-compensationEntered

	attemptedBLock := make(chan struct{})
	realLockB := storeB.deps.lockFile
	storeB.deps.lockFile = func(file *os.File) error {
		close(attemptedBLock)
		return realLockB(file)
	}
	enteredB := make(chan struct{})
	doneB := make(chan error, 1)
	go func() {
		_, err := storeB.MutateData(func(d *Data) error {
			close(enteredB)
			d.Services["svc"].Generation = 3
			return nil
		})
		doneB <- err
	}()
	<-attemptedBLock
	select {
	case <-enteredB:
		t.Fatal("second Store entered while pre-publication compensation held the database lock")
	default:
	}
	close(releaseCompensation)
	if err := <-mutationDone; !errors.Is(err, injected) {
		t.Fatalf("compensated mutation error = %v, want save failure", err)
	}
	if err := <-doneB; err != nil {
		t.Fatal(err)
	}
	if got := mustReadData(t, path).Services["svc"].Generation; got != 3 {
		t.Fatalf("on-disk generation = %d, want serialized generation 3", got)
	}
}

func TestStorePrePublicationCompensationFailureIsJoined(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "db.json")
	store := NewStore(path, filepath.Join(root, "services"))
	if err := store.Set(&Data{DataVersion: CurrentDataVersion, Services: map[string]*Service{"svc": {Name: "svc", Generation: 1}}}); err != nil {
		t.Fatal(err)
	}
	saveErr := errors.New("replace database failed")
	compensationErr := errors.New("compensation failed")
	store.deps.rename = func(string, string) error { return saveErr }
	compensationCalls := 0

	_, err := store.MutateDataWithPrePublicationCompensation(func(d *Data) (func() error, error) {
		d.Services["svc"].Generation = 2
		return func() error {
			compensationCalls++
			return compensationErr
		}, nil
	})
	if !errors.Is(err, saveErr) || !errors.Is(err, compensationErr) {
		t.Fatalf("mutation error = %v, want save and compensation failures", err)
	}
	var publishedErr *PostPublicationError
	if errors.As(err, &publishedErr) {
		t.Fatalf("mutation error = %v, want pre-publication outcome", err)
	}
	if compensationCalls != 1 {
		t.Fatalf("compensation calls = %d, want 1", compensationCalls)
	}
	if got := mustReadData(t, path).Services["svc"].Generation; got != 1 {
		t.Fatalf("on-disk generation = %d, want 1", got)
	}
}

func TestStoreCommittedMutationDoesNotRunPrePublicationCompensation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "db.json")
	store := NewStore(path, filepath.Join(root, "services"))
	if err := store.Set(&Data{DataVersion: CurrentDataVersion, Services: map[string]*Service{"svc": {Name: "svc", Generation: 1}}}); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("database parent sync failed")
	realSync := store.deps.syncDir
	store.deps.syncDir = func(dir *os.File) error {
		if filepath.Clean(dir.Name()) == filepath.Clean(root) {
			return injected
		}
		return realSync(dir)
	}
	compensationCalls := 0

	_, err := store.MutateDataWithPrePublicationCompensation(func(d *Data) (func() error, error) {
		d.Services["svc"].Generation = 2
		return func() error {
			compensationCalls++
			return nil
		}, nil
	})
	var publishedErr *PostPublicationError
	if !errors.As(err, &publishedErr) || !publishedErr.MutationCommitted {
		t.Fatalf("mutation error = %v, want committed *PostPublicationError", err)
	}
	if compensationCalls != 0 {
		t.Fatalf("compensation calls = %d, want 0", compensationCalls)
	}
	if got := mustReadData(t, path).Services["svc"].Generation; got != 2 {
		t.Fatalf("on-disk generation = %d, want 2", got)
	}
}

func TestStoreMutationFailureInjectionPreservesCache(t *testing.T) {
	tests := []struct {
		name   string
		inject func(*Store, error)
	}{
		{name: "lock", inject: func(store *Store, injected error) {
			store.deps.lockFile = func(*os.File) error { return injected }
		}},
		{name: "file sync", inject: func(store *Store, injected error) {
			store.deps.syncFile = func(*os.File) error { return injected }
		}},
		{name: "rename", inject: func(store *Store, injected error) {
			store.deps.rename = func(string, string) error { return injected }
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "db.json")
			store := NewStore(path, filepath.Join(root, "services"))
			if err := store.Set(&Data{DataVersion: CurrentDataVersion, Services: map[string]*Service{"svc": {Name: "svc", Generation: 1}}}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Get(); err != nil {
				t.Fatal(err)
			}
			injected := errors.New("injected " + tt.name + " failure")
			tt.inject(store, injected)
			callbackCalled := false
			_, err := store.MutateData(func(d *Data) error {
				callbackCalled = true
				d.Services["svc"].Generation = 2
				return nil
			})
			if !errors.Is(err, injected) {
				t.Fatalf("MutateData error = %v, want injected failure", err)
			}
			var publishedErr *PostPublicationError
			if errors.As(err, &publishedErr) {
				t.Fatalf("MutateData error = %v, want pre-publication failure", err)
			}
			if tt.name == "lock" && callbackCalled {
				t.Fatal("callback ran after lock failure")
			}
			dv, getErr := store.Get()
			if getErr != nil {
				t.Fatal(getErr)
			}
			if got := dv.AsStruct().Services["svc"].Generation; got != 1 {
				t.Fatalf("cached generation = %d, want 1 after failed mutation", got)
			}
			if got := mustReadData(t, path).Services["svc"].Generation; got != 1 {
				t.Fatalf("on-disk generation = %d, want 1 after failed mutation", got)
			}
		})
	}
}

func TestStorePostPublicationFailuresPublishCacheAndDisk(t *testing.T) {
	operations := []struct {
		name string
		run  func(*Store) (*Data, error)
	}{
		{name: "Set", run: func(store *Store) (*Data, error) {
			next := &Data{
				DataVersion: CurrentDataVersion,
				Services:    map[string]*Service{"svc": {Name: "svc", Generation: 2}},
			}
			return nil, store.Set(next)
		}},
		{name: "MutateData", run: func(store *Store) (*Data, error) {
			return store.MutateData(func(d *Data) error {
				d.Services["svc"].Generation = 2
				return nil
			})
		}},
	}
	failures := []struct {
		name   string
		inject func(*Store, error)
	}{
		{name: "parent open", inject: func(store *Store, injected error) {
			afterSuccessfulDBRename(store, func() {
				store.deps.openDir = func(string) (*os.File, error) { return nil, injected }
			})
		}},
		{name: "parent sync", inject: func(store *Store, injected error) {
			afterSuccessfulDBRename(store, func() {
				store.deps.syncDir = func(*os.File) error { return injected }
			})
		}},
		{name: "parent close", inject: func(store *Store, injected error) {
			realClose := store.deps.closeDir
			afterSuccessfulDBRename(store, func() {
				store.deps.closeDir = func(dir *os.File) error {
					return errors.Join(realClose(dir), injected)
				}
			})
		}},
		{name: "unlock", inject: func(store *Store, injected error) {
			realUnlock := store.deps.unlockFile
			store.deps.unlockFile = func(lock *os.File) error {
				return errors.Join(realUnlock(lock), injected)
			}
		}},
		{name: "lock close", inject: func(store *Store, injected error) {
			realClose := store.deps.closeLockFile
			store.deps.closeLockFile = func(lock *os.File) error {
				return errors.Join(realClose(lock), injected)
			}
		}},
	}
	for _, operation := range operations {
		for _, failure := range failures {
			t.Run(operation.name+"/"+failure.name, func(t *testing.T) {
				root := t.TempDir()
				path := filepath.Join(root, "db.json")
				store := NewStore(path, filepath.Join(root, "services"))
				if err := store.Set(&Data{
					DataVersion: CurrentDataVersion,
					Services:    map[string]*Service{"svc": {Name: "svc", Generation: 1}},
				}); err != nil {
					t.Fatal(err)
				}
				if _, err := store.Get(); err != nil {
					t.Fatal(err)
				}
				injected := errors.New("injected " + failure.name + " failure")
				failure.inject(store, injected)

				updated, err := operation.run(store)
				if !errors.Is(err, injected) {
					t.Fatalf("%s error = %v, want injected failure", operation.name, err)
				}
				var publishedErr *PostPublicationError
				if !errors.As(err, &publishedErr) {
					t.Fatalf("%s error = %v, want *PostPublicationError", operation.name, err)
				}
				if !publishedErr.MutationCommitted {
					t.Fatalf("%s outcome says requested mutation was not committed", operation.name)
				}
				if operation.name == "MutateData" {
					if updated == nil || updated.Services["svc"].Generation != 2 {
						t.Fatalf("MutateData result = %#v, want published generation 2", updated)
					}
				}
				dv, getErr := store.Get()
				if getErr != nil {
					t.Fatal(getErr)
				}
				if got := dv.AsStruct().Services["svc"].Generation; got != 2 {
					t.Fatalf("cached generation = %d, want published generation 2", got)
				}
				if got := mustReadData(t, path).Services["svc"].Generation; got != 2 {
					t.Fatalf("on-disk generation = %d, want published generation 2", got)
				}
			})
		}
	}
}

func afterSuccessfulDBRename(store *Store, f func()) {
	realRename := store.deps.rename
	store.deps.rename = func(oldPath, newPath string) error {
		err := realRename(oldPath, newPath)
		if err == nil {
			f()
		}
		return err
	}
}

func TestStoreMutationJoinsUnlockAndCloseFailures(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "db.json")
	store := NewStore(path, filepath.Join(root, "services"))
	if err := store.Set(&Data{DataVersion: CurrentDataVersion}); err != nil {
		t.Fatal(err)
	}
	unlockErr := errors.New("unlock failed")
	closeErr := errors.New("close failed")
	realUnlock := store.deps.unlockFile
	realClose := store.deps.closeLockFile
	store.deps.unlockFile = func(file *os.File) error {
		return errors.Join(realUnlock(file), unlockErr)
	}
	store.deps.closeLockFile = func(file *os.File) error {
		return errors.Join(realClose(file), closeErr)
	}

	_, err := store.MutateData(func(d *Data) error {
		d.Volumes = map[string]*Volume{"data": {Name: "data", Path: "/data"}}
		return nil
	})
	if !errors.Is(err, unlockErr) || !errors.Is(err, closeErr) {
		t.Fatalf("MutateData error = %v, want joined unlock and close failures", err)
	}
	var publishedErr *PostPublicationError
	if !errors.As(err, &publishedErr) {
		t.Fatalf("MutateData error = %v, want *PostPublicationError", err)
	}
}

func TestValidatePersistentDBLockMetadataAllowsInheritedGID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.json.lock")
	file, err := openPersistentDBLock(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		t.Fatal(err)
	}
	metadata := persistentDBLockMetadata{
		mode: uint32(stat.Mode),
		uid:  stat.Uid,
		gid:  stat.Gid + 1,
	}
	if err := validatePersistentDBLockMetadata(metadata, stat.Uid); err != nil {
		t.Fatalf("validate inherited setgid group: %v", err)
	}
}

func TestStoreRejectsUntrustedPersistentLock(t *testing.T) {
	for _, tt := range []struct {
		name  string
		setup func(testing.TB, string)
	}{
		{name: "symlink", setup: func(t testing.TB, path string) {
			t.Helper()
			target := path + ".target"
			if err := os.WriteFile(target, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "permissive mode", setup: func(t testing.TB, path string) {
			t.Helper()
			if err := os.WriteFile(path, nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "not regular", setup: func(t testing.TB, path string) {
			t.Helper()
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "db.json")
			lockPath := path + ".lock"
			tt.setup(t, lockPath)
			store := NewStore(path, filepath.Join(root, "services"))
			if err := store.Set(&Data{DataVersion: CurrentDataVersion}); err == nil {
				t.Fatal("Set accepted untrusted persistent lock")
			}
		})
	}
}

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	addr, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatal(err)
	}
	return addr
}

func mustPrefix(t *testing.T, s string) netip.Prefix {
	t.Helper()
	prefix, err := netip.ParsePrefix(s)
	if err != nil {
		t.Fatal(err)
	}
	return prefix
}

func requireDistinctPtr[T any](t *testing.T, name string, got, src *T) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s clone is nil", name)
	}
	if got == src {
		t.Fatalf("%s clone aliases source", name)
	}
}

func boolPtr(v bool) *bool { return &v }
func intPtr(v int) *int    { return &v }

func writeData(t *testing.T, path string, d *Data) {
	t.Helper()
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
}

func mustReadData(t *testing.T, path string) *Data {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var d Data
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	return &d
}
