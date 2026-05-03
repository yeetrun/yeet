// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/db"
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
		{name: "configured macvlan interface", entry: ifaceIP{Interface: "mac0", IP: "10.0.0.8"}, macIface: "mac0", want: "macvlan"},
		{name: "docker prefix", entry: ifaceIP{Interface: "docker0", IP: "172.17.0.1"}, want: "docker"},
		{name: "bridge prefix", entry: ifaceIP{Interface: "br-abcd", IP: "172.18.0.1"}, want: "docker"},
		{name: "docker compose fallback", entry: ifaceIP{Interface: "eth0", IP: "10.0.0.8"}, serviceType: db.ServiceTypeDockerCompose, want: "docker"},
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
		"10.0.0.8":    "macvlan",
		"10.0.0.9":    "netns",
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

func TestServiceInfoReturnsNotFoundResponse(t *testing.T) {
	resp, err := newTestServer(t).serviceInfo("missing")
	if err != nil {
		t.Fatalf("serviceInfo: %v", err)
	}
	if resp.Found || resp.Message != "service not found" {
		t.Fatalf("response = %#v", resp)
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
