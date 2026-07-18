// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestRenderISOComposeOverlayAssignsOnlyISOAddresses(t *testing.T) {
	allocation := testISOComposeOverlayAllocation()
	allocation.Components["worker"] = db.ISOComponent{Address: netip.MustParseAddr("172.30.128.3")}

	raw, err := renderISOComposeOverlay(allocation, ISOComposeModel{Components: []string{"api", "worker"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"driver: yeet",
		"dev.catchit.mode: iso",
		"dev.catchit.netns: /var/run/netns/yeet-a172cedcae-ns",
		"enable_ipv6: false",
		"subnet: 172.30.128.0/27",
		"gateway: 172.30.128.1",
		"ipv4_address: 172.30.128.2",
		"ipv4_address: 172.30.128.3",
		"dns:",
		"- 172.30.128.1",
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("overlay missing %q:\n%s", want, raw)
		}
	}
	if strings.Contains(raw, "192.168.100.1") || strings.Contains(raw, "yeet.internal") {
		t.Fatalf("ISO overlay contains the svc DNS overlay:\n%s", raw)
	}
}

func TestRenderISOComposeOverlayUsesQuad100ForTailscale(t *testing.T) {
	allocation := testISOComposeOverlayAllocation()
	allocation.DesiredModes = []string{"iso", "ts"}
	raw, err := renderISOComposeOverlay(allocation, ISOComposeModel{Components: []string{"api"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "- 100.100.100.100") {
		t.Fatalf("overlay does not use Tailscale DNS:\n%s", raw)
	}
	if strings.Contains(raw, "- 172.30.128.1") {
		t.Fatalf("Tailscale overlay retained gateway DNS:\n%s", raw)
	}
}

func TestRenderISOComposeOverlayRejectsIncompleteAllocation(t *testing.T) {
	tests := []struct {
		name       string
		allocation *db.ISOAllocation
	}{
		{name: "nil allocation"},
		{name: "invalid project", allocation: &db.ISOAllocation{Gateway: netip.MustParseAddr("172.30.128.1")}},
		{name: "invalid gateway", allocation: &db.ISOAllocation{Project: netip.MustParsePrefix("172.30.128.0/27")}},
		{name: "invalid namespace", allocation: &db.ISOAllocation{
			Project: netip.MustParsePrefix("172.30.128.0/27"), Gateway: netip.MustParseAddr("172.30.128.1"), NetNS: "../host",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := renderISOComposeOverlay(tt.allocation, ISOComposeModel{}); err == nil {
				t.Fatal("renderISOComposeOverlay returned nil error")
			}
		})
	}
}

func TestRenderISOComposeOverlayRejectsUnallocatedComponent(t *testing.T) {
	_, err := renderISOComposeOverlay(testISOComposeOverlayAllocation(), ISOComposeModel{Components: []string{"missing"}})
	if err == nil || !strings.Contains(err.Error(), `ISO component "missing" has no reserved address`) {
		t.Fatalf("error = %v, want missing component allocation", err)
	}
}

func testISOComposeOverlayAllocation() *db.ISOAllocation {
	return &db.ISOAllocation{
		Project:      netip.MustParsePrefix("172.30.128.0/27"),
		Gateway:      netip.MustParseAddr("172.30.128.1"),
		NetNS:        "yeet-a172cedcae-ns",
		DesiredModes: []string{"iso"},
		Components: map[string]db.ISOComponent{
			"api": {Address: netip.MustParseAddr("172.30.128.2")},
		},
	}
}
