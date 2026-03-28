// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package netns

import (
	"strings"
	"testing"
)

func TestDetectFirewallBackendFromProbe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		probe probeResult
		want  FirewallBackend
	}{
		{
			name: "prefers nft when available",
			probe: probeResult{
				HasNFT:          true,
				IPTablesVersion: "v1.8.11 (nf_tables)",
			},
			want: BackendNFT,
		},
		{
			name: "falls back to iptables nft when nft is unavailable",
			probe: probeResult{
				HasNFT:          false,
				IPTablesVersion: "v1.8.11 (nf_tables)",
			},
			want: BackendIPTablesNFT,
		},
		{
			name: "falls back to legacy iptables when nft is unavailable",
			probe: probeResult{
				HasNFT:          false,
				IPTablesVersion: "v1.8.11 (legacy)",
			},
			want: BackendIPTablesLegacy,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := DetectFirewallBackendFromProbe(tt.probe)
			if got != tt.want {
				t.Fatalf("DetectFirewallBackendFromProbe(%+v) = %v, want %v", tt.probe, got, tt.want)
			}
		})
	}
}

func TestRenderRuleset(t *testing.T) {
	t.Parallel()

	spec := FirewallSpec{
		SubnetCIDR: "192.168.100.0/24",
		BridgeIf:   "yeet0",
	}

	tests := []struct {
		name      string
		backend   FirewallBackend
		wantParts []string
	}{
		{
			name:    "nft backend renders native yeet table",
			backend: BackendNFT,
			wantParts: []string{
				"table ip yeet",
			},
		},
		{
			name:    "iptables nft backend renders owned chains",
			backend: BackendIPTablesNFT,
			wantParts: []string{
				"YEET_FORWARD",
				"YEET_POSTROUTING",
			},
		},
		{
			name:    "iptables legacy backend renders owned chains",
			backend: BackendIPTablesLegacy,
			wantParts: []string{
				"YEET_FORWARD",
				"YEET_POSTROUTING",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := RenderFirewallRules(tt.backend, spec)
			for _, wantPart := range tt.wantParts {
				if !strings.Contains(got, wantPart) {
					t.Fatalf("RenderFirewallRules(%v, %+v) missing %q in output:\n%s", tt.backend, spec, wantPart, got)
				}
			}
		})
	}
}
