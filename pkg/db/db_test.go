// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package db

import (
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"testing"
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
