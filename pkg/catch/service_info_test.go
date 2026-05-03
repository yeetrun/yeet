// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"net/netip"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/db"
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
