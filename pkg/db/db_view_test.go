// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package db

import (
	"encoding/json"
	"net/netip"
	"reflect"
	"testing"

	"tailscale.com/tailcfg"
)

func TestViewAccessorsExposeDataWithoutMutableStructs(t *testing.T) {
	data := sampleViewData(t)
	view := data.View()

	if !view.Valid() {
		t.Fatal("DataView is invalid")
	}
	if got := view.DataVersion(); got != CurrentDataVersion {
		t.Fatalf("DataVersion = %d, want %d", got, CurrentDataVersion)
	}

	services := view.Services()
	if services.Len() != 1 || !services.Contains("svc") {
		t.Fatalf("Services view missing svc: len=%d contains=%v", services.Len(), services.Contains("svc"))
	}
	svc := services.Get("svc")
	if !svc.Valid() {
		t.Fatal("ServiceView is invalid")
	}
	if svc.Name() != "svc" ||
		svc.ServiceType() != ServiceTypeDockerCompose ||
		svc.Generation() != 2 ||
		svc.LatestGeneration() != 3 {
		t.Fatalf("service scalar accessors returned unexpected values: %#v", svc.AsStruct())
	}
	artifacts := svc.Artifacts()
	if got := artifacts.Get(ArtifactBinary).Refs().Get("latest"); got != "/srv/svc/latest" {
		t.Fatalf("artifact latest ref = %q, want /srv/svc/latest", got)
	}
	if got, ok := svc.SvcNetwork().GetOk(); !ok || got.IPv4 != mustAddr(t, "10.0.0.20") {
		t.Fatalf("SvcNetwork = %#v, %v; want 10.0.0.20, true", got, ok)
	}
	if got := svc.Macvlan().Get(); got.VLAN != 42 || got.Parent != "eth0" {
		t.Fatalf("Macvlan = %#v, want VLAN 42 parent eth0", got)
	}
	ts := svc.TSNet()
	if ts.Interface() != "tailscale0" ||
		ts.Version() != "1.2.3" ||
		ts.ExitNode() != "exit-node" ||
		ts.StableID() != tailcfg.StableNodeID("stable-id") {
		t.Fatalf("tailscale accessors returned unexpected values: %#v", ts.AsStruct())
	}
	if ts.Tags().Len() != 2 || ts.Tags().At(0) != "tag:svc" || ts.Tags().At(1) != "tag:prod" {
		t.Fatalf("tailscale tags = %#v, want tag:svc/tag:prod", ts.Tags())
	}

	images := view.Images()
	image := images.Get("repo")
	if got := image.Refs().Get("latest").BlobHash; got != "sha256:old" {
		t.Fatalf("image latest blob = %q, want sha256:old", got)
	}

	volume := view.Volumes().Get("data")
	if volume.Name() != "data" ||
		volume.Src() != "host:/export" ||
		volume.Path() != "/data" ||
		volume.Type() != "nfs" ||
		volume.Opts() != "defaults" ||
		volume.Deps() != "network-online.target" {
		t.Fatalf("volume accessors returned unexpected values: %#v", volume.AsStruct())
	}

	network := view.DockerNetworks().Get("net")
	if network.NetworkID() != "network-id" ||
		network.NetNS() != "netns" ||
		network.IPv4Gateway() != mustPrefix(t, "10.1.0.1/24") ||
		network.IPv4Range() != mustPrefix(t, "10.1.0.0/24") {
		t.Fatalf("network scalar accessors returned unexpected values: %#v", network.AsStruct())
	}
	endpoint := network.Endpoints().Get("endpoint-id")
	if endpoint.EndpointID() != "endpoint-id" || endpoint.IPv4() != mustPrefix(t, "10.1.0.2/32") {
		t.Fatalf("endpoint accessors returned unexpected values: %#v", endpoint.AsStruct())
	}
	if got := network.EndpointAddrs().Get("legacy"); got != mustPrefix(t, "10.1.0.3/32") {
		t.Fatalf("legacy endpoint addr = %s, want 10.1.0.3/32", got)
	}
	port := network.PortMap().Get("6/80")
	if port.EndpointID() != "endpoint-id" || port.Port() != 80 {
		t.Fatalf("port accessors returned unexpected values: %#v", port.AsStruct())
	}
}

func TestViewAsStructReturnsDeepCopies(t *testing.T) {
	data := sampleViewData(t)
	view := data.View()

	clone := view.AsStruct()
	if !reflect.DeepEqual(clone, data) {
		t.Fatalf("DataView.AsStruct differs from source:\nclone=%#v\nsource=%#v", clone, data)
	}
	clone.Services["svc"].Artifacts[ArtifactBinary].Refs["latest"] = "/srv/clone/latest"
	clone.Services["svc"].TSNet.Tags[0] = "tag:clone"
	clone.Images["repo"].Refs["latest"] = ImageManifest{BlobHash: "sha256:clone"}
	clone.Volumes["data"].Path = "/clone"
	clone.DockerNetworks["net"].Endpoints["endpoint-id"].EndpointID = "clone-endpoint"
	clone.DockerNetworks["net"].PortMap["6/80"].Port = 8080

	if got := data.Services["svc"].Artifacts[ArtifactBinary].Refs["latest"]; got != "/srv/svc/latest" {
		t.Fatalf("source artifact ref mutated through DataView.AsStruct: %q", got)
	}
	if got := data.Services["svc"].TSNet.Tags[0]; got != "tag:svc" {
		t.Fatalf("source tailscale tags mutated through DataView.AsStruct: %q", got)
	}
	if got := data.Images["repo"].Refs["latest"].BlobHash; got != "sha256:old" {
		t.Fatalf("source image ref mutated through DataView.AsStruct: %q", got)
	}
	if got := data.Volumes["data"].Path; got != "/data" {
		t.Fatalf("source volume mutated through DataView.AsStruct: %q", got)
	}
	if got := data.DockerNetworks["net"].Endpoints["endpoint-id"].EndpointID; got != "endpoint-id" {
		t.Fatalf("source endpoint mutated through DataView.AsStruct: %q", got)
	}
	if got := data.DockerNetworks["net"].PortMap["6/80"].Port; got != 80 {
		t.Fatalf("source port mutated through DataView.AsStruct: %d", got)
	}

	svcClone := view.Services().Get("svc").AsStruct()
	svcClone.Name = "clone"
	if got := data.Services["svc"].Name; got != "svc" {
		t.Fatalf("source service name mutated through ServiceView.AsStruct: %q", got)
	}
	imageClone := view.Images().Get("repo").AsStruct()
	imageClone.Refs["new"] = ImageManifest{BlobHash: "sha256:new"}
	if _, ok := data.Images["repo"].Refs["new"]; ok {
		t.Fatal("source image refs mutated through ImageRepoView.AsStruct")
	}
	volumeClone := view.Volumes().Get("data").AsStruct()
	volumeClone.Name = "clone"
	if got := data.Volumes["data"].Name; got != "data" {
		t.Fatalf("source volume name mutated through VolumeView.AsStruct: %q", got)
	}
	networkClone := view.DockerNetworks().Get("net").AsStruct()
	networkClone.NetworkID = "clone"
	if got := data.DockerNetworks["net"].NetworkID; got != "network-id" {
		t.Fatalf("source network ID mutated through DockerNetworkView.AsStruct: %q", got)
	}
	endpointClone := view.DockerNetworks().Get("net").Endpoints().Get("endpoint-id").AsStruct()
	endpointClone.EndpointID = "clone"
	if got := data.DockerNetworks["net"].Endpoints["endpoint-id"].EndpointID; got != "endpoint-id" {
		t.Fatalf("source endpoint ID mutated through DockerEndpointView.AsStruct: %q", got)
	}
	tsClone := view.Services().Get("svc").TSNet().AsStruct()
	tsClone.Tags[0] = "tag:clone"
	if got := data.Services["svc"].TSNet.Tags[0]; got != "tag:svc" {
		t.Fatalf("source tailscale tag mutated through TailscaleNetworkView.AsStruct: %q", got)
	}
	portClone := view.DockerNetworks().Get("net").PortMap().Get("6/80").AsStruct()
	portClone.Port = 8080
	if got := data.DockerNetworks["net"].PortMap["6/80"].Port; got != 80 {
		t.Fatalf("source endpoint port mutated through EndpointPortView.AsStruct: %d", got)
	}
	artifactClone := view.Services().Get("svc").Artifacts().Get(ArtifactBinary).AsStruct()
	artifactClone.Refs["latest"] = "/srv/clone/latest"
	if got := data.Services["svc"].Artifacts[ArtifactBinary].Refs["latest"]; got != "/srv/svc/latest" {
		t.Fatalf("source artifact mutated through ArtifactView.AsStruct: %q", got)
	}
}

func TestNilViewsAreInvalidAndAsStructIsNil(t *testing.T) {
	if (DataView{}).Valid() {
		t.Fatal("zero DataView is valid")
	}
	if got := (DataView{}).AsStruct(); got != nil {
		t.Fatalf("zero DataView AsStruct = %#v, want nil", got)
	}
	if (ServiceView{}).Valid() {
		t.Fatal("zero ServiceView is valid")
	}
	if got := (ServiceView{}).AsStruct(); got != nil {
		t.Fatalf("zero ServiceView AsStruct = %#v, want nil", got)
	}
	if (VolumeView{}).Valid() {
		t.Fatal("zero VolumeView is valid")
	}
	if got := (VolumeView{}).AsStruct(); got != nil {
		t.Fatalf("zero VolumeView AsStruct = %#v, want nil", got)
	}
	if (ImageRepoView{}).Valid() {
		t.Fatal("zero ImageRepoView is valid")
	}
	if got := (ImageRepoView{}).AsStruct(); got != nil {
		t.Fatalf("zero ImageRepoView AsStruct = %#v, want nil", got)
	}
	if (ArtifactView{}).Valid() {
		t.Fatal("zero ArtifactView is valid")
	}
	if got := (ArtifactView{}).AsStruct(); got != nil {
		t.Fatalf("zero ArtifactView AsStruct = %#v, want nil", got)
	}
	if (DockerNetworkView{}).Valid() {
		t.Fatal("zero DockerNetworkView is valid")
	}
	if got := (DockerNetworkView{}).AsStruct(); got != nil {
		t.Fatalf("zero DockerNetworkView AsStruct = %#v, want nil", got)
	}
	if (DockerEndpointView{}).Valid() {
		t.Fatal("zero DockerEndpointView is valid")
	}
	if got := (DockerEndpointView{}).AsStruct(); got != nil {
		t.Fatalf("zero DockerEndpointView AsStruct = %#v, want nil", got)
	}
	if (TailscaleNetworkView{}).Valid() {
		t.Fatal("zero TailscaleNetworkView is valid")
	}
	if got := (TailscaleNetworkView{}).AsStruct(); got != nil {
		t.Fatalf("zero TailscaleNetworkView AsStruct = %#v, want nil", got)
	}
	if (EndpointPortView{}).Valid() {
		t.Fatal("zero EndpointPortView is valid")
	}
	if got := (EndpointPortView{}).AsStruct(); got != nil {
		t.Fatalf("zero EndpointPortView AsStruct = %#v, want nil", got)
	}
}

func TestViewJSONRoundTripAndInitializationRules(t *testing.T) {
	type jsonView interface {
		Valid() bool
		MarshalJSON() ([]byte, error)
		UnmarshalJSON([]byte) error
	}

	tests := []struct {
		name      string
		newView   func() jsonView
		validView func() jsonView
		json      string
	}{
		{
			name:      "data",
			newView:   func() jsonView { return &DataView{} },
			validView: func() jsonView { v := (&Data{DataVersion: CurrentDataVersion}).View(); return &v },
			json:      `{"DataVersion":5}`,
		},
		{
			name:      "service",
			newView:   func() jsonView { return &ServiceView{} },
			validView: func() jsonView { v := (&Service{Name: "svc"}).View(); return &v },
			json:      `{"Name":"svc"}`,
		},
		{
			name:      "volume",
			newView:   func() jsonView { return &VolumeView{} },
			validView: func() jsonView { v := (&Volume{Name: "data"}).View(); return &v },
			json:      `{"Name":"data"}`,
		},
		{
			name:    "image repo",
			newView: func() jsonView { return &ImageRepoView{} },
			validView: func() jsonView {
				v := (&ImageRepo{Refs: map[ImageRef]ImageManifest{"latest": {BlobHash: "sha256:old"}}}).View()
				return &v
			},
			json: `{"Refs":{"latest":{"BlobHash":"sha256:old"}}}`,
		},
		{
			name:    "artifact",
			newView: func() jsonView { return &ArtifactView{} },
			validView: func() jsonView {
				v := (&Artifact{Refs: map[ArtifactRef]string{"latest": "/srv/svc/latest"}}).View()
				return &v
			},
			json: `{"Refs":{"latest":"/srv/svc/latest"}}`,
		},
		{
			name:      "docker network",
			newView:   func() jsonView { return &DockerNetworkView{} },
			validView: func() jsonView { v := (&DockerNetwork{NetworkID: "network-id"}).View(); return &v },
			json:      `{"NetworkID":"network-id"}`,
		},
		{
			name:      "docker endpoint",
			newView:   func() jsonView { return &DockerEndpointView{} },
			validView: func() jsonView { v := (&DockerEndpoint{EndpointID: "endpoint-id"}).View(); return &v },
			json:      `{"EndpointID":"endpoint-id"}`,
		},
		{
			name:    "tailscale network",
			newView: func() jsonView { return &TailscaleNetworkView{} },
			validView: func() jsonView {
				v := (&TailscaleNetwork{Interface: "tailscale0", Tags: []string{"tag:svc"}}).View()
				return &v
			},
			json: `{"Interface":"tailscale0","Tags":["tag:svc"]}`,
		},
		{
			name:      "endpoint port",
			newView:   func() jsonView { return &EndpointPortView{} },
			validView: func() jsonView { v := (&EndpointPort{EndpointID: "endpoint-id", Port: 80}).View(); return &v },
			json:      `{"EndpointID":"endpoint-id","Port":80}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			empty := tt.newView()
			if err := empty.UnmarshalJSON(nil); err != nil {
				t.Fatalf("UnmarshalJSON(nil): %v", err)
			}
			if empty.Valid() {
				t.Fatal("empty JSON initialized view")
			}
			if err := empty.UnmarshalJSON([]byte(tt.json)); err != nil {
				t.Fatalf("UnmarshalJSON(valid): %v", err)
			}
			if !empty.Valid() {
				t.Fatal("valid JSON did not initialize view")
			}
			got, err := empty.MarshalJSON()
			if err != nil {
				t.Fatalf("MarshalJSON after unmarshal: %v", err)
			}
			if !json.Valid(got) {
				t.Fatalf("MarshalJSON returned invalid JSON: %q", string(got))
			}
			if err := tt.newView().UnmarshalJSON([]byte("{")); err == nil {
				t.Fatal("UnmarshalJSON succeeded for invalid JSON")
			}
			if err := tt.validView().UnmarshalJSON([]byte(tt.json)); err == nil {
				t.Fatal("UnmarshalJSON succeeded on initialized view")
			}
			if got, err := tt.validView().MarshalJSON(); err != nil {
				t.Fatalf("MarshalJSON(valid view): %v", err)
			} else if !json.Valid(got) {
				t.Fatalf("MarshalJSON(valid view) returned invalid JSON: %q", string(got))
			}
		})
	}
}

func sampleViewData(t *testing.T) *Data {
	t.Helper()
	return &Data{
		DataVersion: CurrentDataVersion,
		Services: map[string]*Service{
			"svc": {
				Name:             "svc",
				ServiceType:      ServiceTypeDockerCompose,
				Generation:       2,
				LatestGeneration: 3,
				Artifacts: ArtifactStore{
					ArtifactBinary: {Refs: map[ArtifactRef]string{"latest": "/srv/svc/latest"}},
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
					StableID:  tailcfg.StableNodeID("stable-id"),
				},
			},
		},
		Images: map[ImageRepoName]*ImageRepo{
			"repo": {
				Refs: map[ImageRef]ImageManifest{
					"latest": {ContentType: "application/vnd.oci.image.manifest.v1+json", BlobHash: "sha256:old"},
				},
			},
		},
		Volumes: map[string]*Volume{
			"data": {
				Name: "data",
				Src:  "host:/export",
				Path: "/data",
				Type: "nfs",
				Opts: "defaults",
				Deps: "network-online.target",
			},
		},
		DockerNetworks: map[string]*DockerNetwork{
			"net": {
				NetworkID:   "network-id",
				NetNS:       "netns",
				IPv4Gateway: mustPrefix(t, "10.1.0.1/24"),
				IPv4Range:   mustPrefix(t, "10.1.0.0/24"),
				Endpoints: map[string]*DockerEndpoint{
					"endpoint-id": {EndpointID: "endpoint-id", IPv4: mustPrefix(t, "10.1.0.2/32")},
				},
				EndpointAddrs: map[string]netip.Prefix{
					"legacy": mustPrefix(t, "10.1.0.3/32"),
				},
				PortMap: map[string]*EndpointPort{
					"6/80": {EndpointID: "endpoint-id", Port: 80},
				},
			},
		},
	}
}
