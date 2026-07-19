// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package db

import (
	"bytes"
	"encoding/json"
	"net/netip"
	"reflect"
	"testing"

	"github.com/go-json-experiment/json/jsontext"
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
	if svc.Publish().Len() != 2 || svc.Publish().At(0) != "80:80" || svc.Publish().At(1) != "443:443" {
		t.Fatalf("service publish ports = %#v, want 80:80/443:443", svc.Publish())
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
	clone.Services["svc"].Publish[0] = "8080:80"
	clone.Services["svc"].TSNet.Tags[0] = "tag:clone"
	clone.Images["repo"].Refs["latest"] = ImageManifest{BlobHash: "sha256:clone"}
	clone.Volumes["data"].Path = "/clone"
	clone.DockerNetworks["net"].Endpoints["endpoint-id"].EndpointID = "clone-endpoint"
	clone.DockerNetworks["net"].PortMap["6/80"].Port = 8080

	if got := data.Services["svc"].Artifacts[ArtifactBinary].Refs["latest"]; got != "/srv/svc/latest" {
		t.Fatalf("source artifact ref mutated through DataView.AsStruct: %q", got)
	}
	if got := data.Services["svc"].Publish[0]; got != "80:80" {
		t.Fatalf("source publish port mutated through DataView.AsStruct: %q", got)
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
	svcClone.Publish[0] = "8080:80"
	if got := data.Services["svc"].Name; got != "svc" {
		t.Fatalf("source service name mutated through ServiceView.AsStruct: %q", got)
	}
	if got := data.Services["svc"].Publish[0]; got != "80:80" {
		t.Fatalf("source publish port mutated through ServiceView.AsStruct: %q", got)
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

func TestVMComponentsView(t *testing.T) {
	components := testVMComponentsConfig()
	components.Runtime.Staged = &VMRuntimeArtifactConfig{ID: "firecracker-v1.17.0-yeet-v1", Source: "official"}
	components.Runtime.Previous = &VMRuntimeArtifactConfig{ID: "firecracker-v1.15.0-yeet-v2", Source: "official"}
	components.Runtime.Trial = &VMRuntimeTrialConfig{
		State:       "pending",
		CandidateID: components.Runtime.Staged.ID,
		PreviousID:  components.Runtime.Previous.ID,
		StartedAt:   "2026-07-19T14:00:00Z",
	}
	data := &Data{
		DataVersion: CurrentDataVersion,
		VMHost: &VMHostConfig{
			RuntimePolicy:       "stage-on-restart",
			RuntimeChannel:      "candidate",
			ProtectedRuntimeIDs: []string{components.Runtime.Configured.ID},
		},
		Services: map[string]*Service{
			"devbox": {
				Name:        "devbox",
				ServiceType: ServiceTypeVM,
				VM:          &VMConfig{Runtime: "firecracker", Components: components},
			},
		},
	}

	host := data.View().VMHost()
	if host.RuntimePolicy() != "stage-on-restart" || host.RuntimeChannel() != "candidate" {
		t.Fatalf("host runtime defaults = %q/%q", host.RuntimePolicy(), host.RuntimeChannel())
	}
	if got := host.ProtectedRuntimeIDs(); got.Len() != 1 || got.At(0) != components.Runtime.Configured.ID {
		t.Fatalf("protected runtime IDs = %#v", got)
	}

	componentView := data.View().Services().Get("devbox").VM().Components()
	if !componentView.Valid() {
		t.Fatal("VM components view is invalid")
	}
	if got := componentView.GuestBase(); got.ID != components.GuestBase.ID || got.RootFSProvenance != components.GuestBase.RootFSProvenance {
		t.Fatalf("guest base view = %#v", got)
	}
	if got := componentView.Kernel(); got.ID != components.Kernel.ID || got.SHA256 != components.Kernel.SHA256 {
		t.Fatalf("kernel view = %#v", got)
	}
	runtimeView := componentView.Runtime()
	if runtimeView.Policy() != "manual" || runtimeView.Channel() != "stable" || runtimeView.Configured().ID != components.Runtime.Configured.ID {
		t.Fatalf("runtime lifecycle view = %#v", runtimeView.AsStruct())
	}
	if got := runtimeView.Staged(); !got.Valid() || got.ID() != components.Runtime.Staged.ID {
		t.Fatalf("staged runtime view = %#v", got.AsStruct())
	}
	if got := runtimeView.Previous(); !got.Valid() || got.ID() != components.Runtime.Previous.ID {
		t.Fatalf("previous runtime view = %#v", got.AsStruct())
	}
	if got := runtimeView.Trial(); !got.Valid() || got.State() != "pending" || got.CandidateID() != components.Runtime.Staged.ID || got.PreviousID() != components.Runtime.Previous.ID || got.StartedAt() != "2026-07-19T14:00:00Z" {
		t.Fatalf("runtime trial view = %#v", got.AsStruct())
	}

	componentClone := componentView.AsStruct()
	componentClone.Runtime.Staged.ID = "mutated"
	componentClone.Runtime.Trial.State = "failed"
	hostClone := host.AsStruct()
	hostClone.ProtectedRuntimeIDs[0] = "mutated"
	if components.Runtime.Staged.ID != "firecracker-v1.17.0-yeet-v1" || components.Runtime.Trial.State != "pending" {
		t.Fatalf("component view clone mutated source: %#v", components.Runtime)
	}
	if data.VMHost.ProtectedRuntimeIDs[0] != components.Runtime.Configured.ID {
		t.Fatalf("host view clone mutated source slice: %#v", data.VMHost.ProtectedRuntimeIDs)
	}
}

func TestServicePublishViewAndClone(t *testing.T) {
	service := &Service{
		Name:    "svc",
		Publish: []string{"80:80", "443:443"},
	}

	view := service.View()
	if got := view.Publish(); got.Len() != 2 || got.At(0) != "80:80" || got.At(1) != "443:443" {
		t.Fatalf("ServiceView.Publish = %#v, want 80:80/443:443", got)
	}

	clone := service.Clone()
	clone.Publish[0] = "8080:80"
	if got := service.Publish[0]; got != "80:80" {
		t.Fatalf("source publish port mutated through Service.Clone: %q", got)
	}

	viewClone := view.AsStruct()
	viewClone.Publish[1] = "8443:443"
	if got := service.Publish[1]; got != "443:443" {
		t.Fatalf("source publish port mutated through ServiceView.AsStruct: %q", got)
	}
}

func TestServiceIdentityCloneAndView(t *testing.T) {
	want := &ServiceIdentity{RequestedUser: "app", RequestedGroup: "app", UID: 1002, GID: 1003}
	svc := &Service{Name: "api", Identity: want}
	clone := svc.Clone()
	if clone.Identity == want || *clone.Identity != *want {
		t.Fatalf("clone identity = %#v", clone.Identity)
	}
	view := svc.View()
	identityView := view.Identity()
	if !identityView.Valid() || identityView.RequestedUser() != "app" || identityView.RequestedGroup() != "app" || identityView.UID() != 1002 || identityView.GID() != 1003 {
		t.Fatalf("view identity = %#v", view.Identity())
	}
	if clone := identityView.AsStruct(); clone == want || *clone != *want {
		t.Fatalf("identity view clone = %#v", clone)
	}
	raw, err := identityView.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal identity view: %v", err)
	}
	var decoded ServiceIdentityView
	if err := decoded.UnmarshalJSON(raw); err != nil || !reflect.DeepEqual(decoded.AsStruct(), want) {
		t.Fatalf("unmarshal identity view = %#v, %v", decoded.AsStruct(), err)
	}
	if err := decoded.UnmarshalJSON(raw); err == nil {
		t.Fatal("identity view JSON accepted reinitialization")
	}
	var empty ServiceIdentityView
	if err := empty.UnmarshalJSON(nil); err != nil || empty.Valid() {
		t.Fatalf("empty identity view = %#v, %v", empty, err)
	}
	if err := empty.UnmarshalJSON([]byte("{")); err == nil {
		t.Fatal("identity view accepted invalid JSON")
	}
	var buffer bytes.Buffer
	if err := identityView.MarshalJSONTo(jsontext.NewEncoder(&buffer)); err != nil {
		t.Fatalf("marshal identity view with jsonv2: %v", err)
	}
	var decodedV2 ServiceIdentityView
	if err := decodedV2.UnmarshalJSONFrom(jsontext.NewDecoder(bytes.NewReader(buffer.Bytes()))); err != nil || !reflect.DeepEqual(decodedV2.AsStruct(), want) {
		t.Fatalf("unmarshal identity view with jsonv2 = %#v, %v", decodedV2.AsStruct(), err)
	}
	if err := decodedV2.UnmarshalJSONFrom(jsontext.NewDecoder(bytes.NewReader(buffer.Bytes()))); err == nil {
		t.Fatal("identity view jsonv2 accepted reinitialization")
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
		MarshalJSONTo(*jsontext.Encoder) error
		UnmarshalJSON([]byte) error
		UnmarshalJSONFrom(*jsontext.Decoder) error
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
		{
			name:    "vm socket config",
			newView: func() jsonView { return &VMSocketConfigView{} },
			validView: func() jsonView {
				v := (&VMSocketConfig{
					APISocketPath:   "/run/devbox/firecracker.sock",
					VsockSocketPath: "/run/devbox/vsock.sock",
					VsockGuestCID:   3,
				}).View()
				return &v
			},
			json: `{"APISocketPath":"/run/devbox/firecracker.sock","VsockSocketPath":"/run/devbox/vsock.sock","VsockGuestCID":3}`,
		},
		{
			name:      "vm config",
			newView:   func() jsonView { return &VMConfigView{} },
			validView: func() jsonView { v := (&VMConfig{Runtime: "firecracker"}).View(); return &v },
			json:      `{"Runtime":"firecracker"}`,
		},
		{
			name:      "vm image config",
			newView:   func() jsonView { return &VMImageConfigView{} },
			validView: func() jsonView { v := (&VMImageConfig{Payload: "vm://ubuntu/26.04"}).View(); return &v },
			json:      `{"Payload":"vm://ubuntu/26.04"}`,
		},
		{
			name:      "vm disk config",
			newView:   func() jsonView { return &VMDiskConfigView{} },
			validView: func() jsonView { v := (&VMDiskConfig{Backend: "raw"}).View(); return &v },
			json:      `{"Backend":"raw"}`,
		},
		{
			name:      "vm network config",
			newView:   func() jsonView { return &VMNetworkConfigView{} },
			validView: func() jsonView { v := (&VMNetworkConfig{Mode: "dhcp"}).View(); return &v },
			json:      `{"Mode":"dhcp"}`,
		},
		{
			name:      "vm ssh config",
			newView:   func() jsonView { return &VMSSHConfigView{} },
			validView: func() jsonView { v := (&VMSSHConfig{User: "ubuntu"}).View(); return &v },
			json:      `{"User":"ubuntu"}`,
		},
		{
			name:      "vm console config",
			newView:   func() jsonView { return &VMConsoleConfigView{} },
			validView: func() jsonView { v := (&VMConsoleConfig{SocketPath: "/run/devbox/console.sock"}).View(); return &v },
			json:      `{"SocketPath":"/run/devbox/console.sock"}`,
		},
		{
			name:      "vm balloon config",
			newView:   func() jsonView { return &VMBalloonConfigView{} },
			validView: func() jsonView { v := (&VMBalloonConfig{Mode: "auto"}).View(); return &v },
			json:      `{"Mode":"auto"}`,
		},
		{
			name:      "vm host config",
			newView:   func() jsonView { return &VMHostConfigView{} },
			validView: func() jsonView { v := (&VMHostConfig{RuntimePolicy: "manual"}).View(); return &v },
			json:      `{"RuntimePolicy":"manual"}`,
		},
		{
			name:      "vm guest base config",
			newView:   func() jsonView { return &VMGuestBaseConfigView{} },
			validView: func() jsonView { v := (&VMGuestBaseConfig{ID: "guest-v1"}).View(); return &v },
			json:      `{"ID":"guest-v1"}`,
		},
		{
			name:      "vm kernel artifact config",
			newView:   func() jsonView { return &VMKernelArtifactConfigView{} },
			validView: func() jsonView { v := (&VMKernelArtifactConfig{ID: "kernel-v1"}).View(); return &v },
			json:      `{"ID":"kernel-v1"}`,
		},
		{
			name:      "vm runtime artifact config",
			newView:   func() jsonView { return &VMRuntimeArtifactConfigView{} },
			validView: func() jsonView { v := (&VMRuntimeArtifactConfig{ID: "runtime-v1"}).View(); return &v },
			json:      `{"ID":"runtime-v1"}`,
		},
		{
			name:      "vm runtime trial config",
			newView:   func() jsonView { return &VMRuntimeTrialConfigView{} },
			validView: func() jsonView { v := (&VMRuntimeTrialConfig{State: "pending"}).View(); return &v },
			json:      `{"State":"pending"}`,
		},
		{
			name:      "vm runtime lifecycle config",
			newView:   func() jsonView { return &VMRuntimeLifecycleConfigView{} },
			validView: func() jsonView { v := (&VMRuntimeLifecycleConfig{Policy: "manual"}).View(); return &v },
			json:      `{"Policy":"manual"}`,
		},
		{
			name:      "vm components config",
			newView:   func() jsonView { return &VMComponentsConfigView{} },
			validView: func() jsonView { v := testVMComponentsConfig().View(); return &v },
			json:      `{"GuestBase":{"ID":"guest-v1"}}`,
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

			var encoded bytes.Buffer
			if err := tt.validView().MarshalJSONTo(jsontext.NewEncoder(&encoded)); err != nil {
				t.Fatalf("MarshalJSONTo(valid view): %v", err)
			}
			if !json.Valid(encoded.Bytes()) {
				t.Fatalf("MarshalJSONTo(valid view) returned invalid JSON: %q", encoded.String())
			}
			decoded := tt.newView()
			if err := decoded.UnmarshalJSONFrom(jsontext.NewDecoder(bytes.NewBufferString(tt.json))); err != nil {
				t.Fatalf("UnmarshalJSONFrom(valid): %v", err)
			}
			if !decoded.Valid() {
				t.Fatal("valid JSON did not initialize v2 view")
			}
			if err := decoded.UnmarshalJSONFrom(jsontext.NewDecoder(bytes.NewBufferString(tt.json))); err == nil {
				t.Fatal("UnmarshalJSONFrom succeeded on initialized view")
			}
			if err := tt.newView().UnmarshalJSONFrom(jsontext.NewDecoder(bytes.NewBufferString("{"))); err == nil {
				t.Fatal("UnmarshalJSONFrom succeeded for invalid JSON")
			}
		})
	}
}

func TestVMGeneratedViewAccessorsAndClones(t *testing.T) {
	components := testVMComponentsConfig()
	components.Runtime.Staged = &VMRuntimeArtifactConfig{ID: "runtime-staged", Source: "official"}
	components.Runtime.Previous = &VMRuntimeArtifactConfig{ID: "runtime-previous", Source: "official"}
	components.Runtime.Trial = &VMRuntimeTrialConfig{
		State:         "pending",
		CandidateID:   "runtime-staged",
		PreviousID:    "runtime-previous",
		RecoveryPoint: "before-runtime-staged",
		StartedAt:     "2026-07-19T14:00:00Z",
		LastError:     "none",
	}
	host := &VMHostConfig{
		MemoryPolicy:        "balanced",
		RuntimePolicy:       "stage-on-restart",
		RuntimeChannel:      "candidate",
		ProtectedRuntimeIDs: []string{"runtime-previous"},
	}
	vm := &VMConfig{
		Runtime: "firecracker",
		Image: VMImageConfig{
			Payload:         "vm://ubuntu/26.04",
			Version:         "v29",
			Digest:          "sha256:image",
			Kernel:          "/images/v29/vmlinux",
			RootFS:          "/images/v29/rootfs.ext4",
			Distro:          "ubuntu",
			DistroVersion:   "26.04",
			DefaultUser:     "ubuntu",
			GuestSystemInit: "cloud-init",
			MetadataDriver:  "mmio",
		},
		Components:  components,
		CPUs:        4,
		MemoryBytes: 4 << 30,
		Balloon: VMBalloonConfig{
			Mode:                 "auto",
			MinBytes:             1 << 30,
			StatsIntervalSeconds: 5,
			LastTargetBytes:      2 << 30,
		},
		Disk: VMDiskConfig{Backend: "raw", Bytes: 32 << 30, Path: "/srv/devbox/rootfs.ext4"},
		Networks: []VMNetworkConfig{{
			Mode:      "macvtap",
			Interface: "eth0",
			Tap:       "tap0",
			MAC:       "02:00:00:00:00:01",
			IP:        mustAddr(t, "10.0.0.20"),
			Parent:    "br0",
			VLAN:      42,
		}},
		SSH:        VMSSHConfig{User: "ubuntu", KeyRef: "default", KnownHosts: "/srv/devbox/known_hosts"},
		Console:    VMConsoleConfig{SocketPath: "/run/devbox/console.sock", LogPath: "/var/log/devbox/console.log"},
		Sockets:    VMSocketConfig{APISocketPath: "/run/devbox/firecracker.sock", VsockSocketPath: "/run/devbox/vsock.sock", VsockGuestCID: 3},
		PIDFile:    "/run/devbox/firecracker.pid",
		SetupState: "ready",
	}

	assertViewClone(t, "vm", vm, vm.View().AsStruct())
	assertViewClone(t, "image", &vm.Image, vm.Image.View().AsStruct())
	assertViewClone(t, "disk", &vm.Disk, vm.Disk.View().AsStruct())
	assertViewClone(t, "network", &vm.Networks[0], vm.Networks[0].View().AsStruct())
	assertViewClone(t, "ssh", &vm.SSH, vm.SSH.View().AsStruct())
	assertViewClone(t, "console", &vm.Console, vm.Console.View().AsStruct())
	assertViewClone(t, "socket", &vm.Sockets, vm.Sockets.View().AsStruct())
	assertViewClone(t, "balloon", &vm.Balloon, vm.Balloon.View().AsStruct())
	assertViewClone(t, "host", host, host.View().AsStruct())
	assertViewClone(t, "guest base", &components.GuestBase, components.GuestBase.View().AsStruct())
	assertViewClone(t, "kernel", &components.Kernel, components.Kernel.View().AsStruct())
	assertViewClone(t, "configured runtime", &components.Runtime.Configured, components.Runtime.Configured.View().AsStruct())
	assertViewClone(t, "trial", components.Runtime.Trial, components.Runtime.Trial.View().AsStruct())
	assertViewClone(t, "runtime lifecycle", &components.Runtime, components.Runtime.View().AsStruct())
	assertViewClone(t, "components", components, components.View().AsStruct())

	visitVMConfigView(vm.View())
	visitVMHostView(host.View())
	visitVMComponentsView(components.View())
}

func assertViewClone(t *testing.T, name string, want, got any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s view clone = %#v, want %#v", name, got, want)
	}
}

func visitVMConfigView(view VMConfigView) {
	_ = view.Runtime()
	imageConfig := view.Image()
	image := imageConfig.View()
	_ = image.Payload()
	_ = image.Version()
	_ = image.Digest()
	_ = image.Kernel()
	_ = image.RootFS()
	_ = image.Distro()
	_ = image.DistroVersion()
	_ = image.DefaultUser()
	_ = image.GuestSystemInit()
	_ = image.MetadataDriver()
	_ = view.Components()
	_ = view.CPUs()
	_ = view.MemoryBytes()
	balloonConfig := view.Balloon()
	balloon := balloonConfig.View()
	_ = balloon.Mode()
	_ = balloon.MinBytes()
	_ = balloon.StatsIntervalSeconds()
	_ = balloon.LastTargetBytes()
	diskConfig := view.Disk()
	disk := diskConfig.View()
	_ = disk.Backend()
	_ = disk.Bytes()
	_ = disk.Path()
	networkConfig := view.Networks().At(0)
	network := networkConfig.View()
	_ = network.Mode()
	_ = network.Interface()
	_ = network.Tap()
	_ = network.MAC()
	_ = network.IP()
	_ = network.Parent()
	_ = network.VLAN()
	sshConfig := view.SSH()
	ssh := sshConfig.View()
	_ = ssh.User()
	_ = ssh.KeyRef()
	_ = ssh.KnownHosts()
	consoleConfig := view.Console()
	console := consoleConfig.View()
	_ = console.SocketPath()
	_ = console.LogPath()
	socketConfig := view.Sockets()
	sockets := socketConfig.View()
	_ = sockets.APISocketPath()
	_ = sockets.VsockSocketPath()
	_ = sockets.VsockGuestCID()
	_ = view.PIDFile()
	_ = view.SetupState()
}

func visitVMHostView(view VMHostConfigView) {
	_ = view.MemoryPolicy()
	_ = view.RuntimePolicy()
	_ = view.RuntimeChannel()
	_ = view.ProtectedRuntimeIDs()
}

func visitVMComponentsView(view VMComponentsConfigView) {
	guestConfig := view.GuestBase()
	guest := guestConfig.View()
	_ = guest.ID()
	_ = guest.ManifestSHA256()
	_ = guest.Source()
	_ = guest.RootFSProvenance()
	kernelConfig := view.Kernel()
	kernel := kernelConfig.View()
	_ = kernel.ID()
	_ = kernel.ManifestSHA256()
	_ = kernel.SHA256()
	_ = kernel.Path()
	_ = kernel.Source()
	runtime := view.Runtime()
	_ = runtime.Policy()
	_ = runtime.Channel()
	configuredConfig := runtime.Configured()
	configured := configuredConfig.View()
	visitVMRuntimeArtifactView(configured)
	visitVMRuntimeArtifactView(runtime.Staged())
	visitVMRuntimeArtifactView(runtime.Previous())
	trial := runtime.Trial()
	_ = trial.State()
	_ = trial.CandidateID()
	_ = trial.PreviousID()
	_ = trial.RecoveryPoint()
	_ = trial.StartedAt()
	_ = trial.LastError()
}

func visitVMRuntimeArtifactView(view VMRuntimeArtifactConfigView) {
	_ = view.ID()
	_ = view.ManifestSHA256()
	_ = view.FirecrackerSHA256()
	_ = view.JailerSHA256()
	_ = view.Firecracker()
	_ = view.Jailer()
	_ = view.Source()
}

func TestVMGeneratedNilViewsAndClones(t *testing.T) {
	tests := []struct {
		name     string
		valid    func() bool
		asStruct func() any
		nilClone func() any
	}{
		{"vm", func() bool { return (VMConfigView{}).Valid() }, func() any { return (VMConfigView{}).AsStruct() }, func() any { return (*VMConfig)(nil).Clone() }},
		{"image", func() bool { return (VMImageConfigView{}).Valid() }, func() any { return (VMImageConfigView{}).AsStruct() }, func() any { return (*VMImageConfig)(nil).Clone() }},
		{"disk", func() bool { return (VMDiskConfigView{}).Valid() }, func() any { return (VMDiskConfigView{}).AsStruct() }, func() any { return (*VMDiskConfig)(nil).Clone() }},
		{"network", func() bool { return (VMNetworkConfigView{}).Valid() }, func() any { return (VMNetworkConfigView{}).AsStruct() }, func() any { return (*VMNetworkConfig)(nil).Clone() }},
		{"ssh", func() bool { return (VMSSHConfigView{}).Valid() }, func() any { return (VMSSHConfigView{}).AsStruct() }, func() any { return (*VMSSHConfig)(nil).Clone() }},
		{"console", func() bool { return (VMConsoleConfigView{}).Valid() }, func() any { return (VMConsoleConfigView{}).AsStruct() }, func() any { return (*VMConsoleConfig)(nil).Clone() }},
		{"socket", func() bool { return (VMSocketConfigView{}).Valid() }, func() any { return (VMSocketConfigView{}).AsStruct() }, func() any { return (*VMSocketConfig)(nil).Clone() }},
		{"balloon", func() bool { return (VMBalloonConfigView{}).Valid() }, func() any { return (VMBalloonConfigView{}).AsStruct() }, func() any { return (*VMBalloonConfig)(nil).Clone() }},
		{"host", func() bool { return (VMHostConfigView{}).Valid() }, func() any { return (VMHostConfigView{}).AsStruct() }, func() any { return (*VMHostConfig)(nil).Clone() }},
		{"guest base", func() bool { return (VMGuestBaseConfigView{}).Valid() }, func() any { return (VMGuestBaseConfigView{}).AsStruct() }, func() any { return (*VMGuestBaseConfig)(nil).Clone() }},
		{"kernel", func() bool { return (VMKernelArtifactConfigView{}).Valid() }, func() any { return (VMKernelArtifactConfigView{}).AsStruct() }, func() any { return (*VMKernelArtifactConfig)(nil).Clone() }},
		{"runtime artifact", func() bool { return (VMRuntimeArtifactConfigView{}).Valid() }, func() any { return (VMRuntimeArtifactConfigView{}).AsStruct() }, func() any { return (*VMRuntimeArtifactConfig)(nil).Clone() }},
		{"runtime trial", func() bool { return (VMRuntimeTrialConfigView{}).Valid() }, func() any { return (VMRuntimeTrialConfigView{}).AsStruct() }, func() any { return (*VMRuntimeTrialConfig)(nil).Clone() }},
		{"runtime lifecycle", func() bool { return (VMRuntimeLifecycleConfigView{}).Valid() }, func() any { return (VMRuntimeLifecycleConfigView{}).AsStruct() }, func() any { return (*VMRuntimeLifecycleConfig)(nil).Clone() }},
		{"components", func() bool { return (VMComponentsConfigView{}).Valid() }, func() any { return (VMComponentsConfigView{}).AsStruct() }, func() any { return (*VMComponentsConfig)(nil).Clone() }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.valid() {
				t.Fatal("zero view is valid")
			}
			if got := tt.asStruct(); !isNilValue(got) {
				t.Fatalf("zero view AsStruct = %#v, want nil", got)
			}
			if got := tt.nilClone(); !isNilValue(got) {
				t.Fatalf("nil Clone = %#v, want nil", got)
			}
		})
	}
}

func isNilValue(v any) bool {
	return v == nil || reflect.ValueOf(v).IsNil()
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
				Publish:          []string{"80:80", "443:443"},
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
