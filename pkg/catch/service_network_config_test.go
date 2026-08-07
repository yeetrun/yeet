// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/iso"
)

func TestServiceNetworkConfigRoundTripAndClone(t *testing.T) {
	want := &db.ServiceNetworkConfig{
		Modes:         []string{"lan", "ts"},
		TSVersion:     "1.101.284",
		TSExitNode:    "100.64.0.1",
		TSTags:        []string{"tag:app", "tag:ops"},
		MacvlanParent: "eno1",
		MacvlanVLAN:   42,
		MacvlanMAC:    "02:00:00:00:00:42",
	}
	raw, err := json.Marshal(&db.Service{Name: "app", Network: want})
	if err != nil {
		t.Fatal(err)
	}
	var got db.Service
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Network, want) {
		t.Fatalf("round trip Network = %#v, want %#v", got.Network, want)
	}
	clone := got.Clone()
	clone.Network.Modes[0] = "svc"
	clone.Network.TSTags[0] = "tag:changed"
	if got.Network.Modes[0] != "lan" || got.Network.TSTags[0] != "tag:app" {
		t.Fatalf("Network clone aliases source: %#v", got.Network)
	}
	viewClone := got.View().Network().AsStruct()
	viewClone.Modes[0] = "ts"
	viewClone.TSTags[0] = "tag:view"
	if got.Network.Modes[0] != "lan" || got.Network.TSTags[0] != "tag:app" {
		t.Fatalf("Network view aliases source: %#v", got.Network)
	}
}

func TestLegacyServiceNetworkConfigDerivesRuntime(t *testing.T) {
	tests := []struct {
		name string
		svc  *db.Service
		want db.ServiceNetworkConfig
	}{
		{name: "host", svc: &db.Service{Name: "host"}, want: db.ServiceNetworkConfig{Modes: []string{"host"}}},
		{name: "svc", svc: &db.Service{Name: "svc", SvcNetwork: &db.SvcNetwork{}}, want: db.ServiceNetworkConfig{Modes: []string{"svc"}}},
		{name: "lan", svc: &db.Service{Name: "lan", Macvlan: &db.MacvlanNetwork{Parent: "eno1", VLAN: 42, Mac: "02:00:00:00:00:42"}}, want: db.ServiceNetworkConfig{Modes: []string{"lan"}, MacvlanParent: "eno1", MacvlanVLAN: 42, MacvlanMAC: "02:00:00:00:00:42"}},
		{name: "ts", svc: &db.Service{Name: "ts", TSNet: &db.TailscaleNetwork{Version: "1.101.284", ExitNode: "100.64.0.1", Tags: []string{"tag:app"}}}, want: db.ServiceNetworkConfig{Modes: []string{"ts"}, TSVersion: "1.101.284", TSExitNode: "100.64.0.1", TSTags: []string{"tag:app"}}},
		{name: "active iso", svc: &db.Service{Name: "iso", ISO: &db.ISOAllocation{State: string(iso.StateReady), DesiredModes: []string{"ts", "iso"}}, TSNet: &db.TailscaleNetwork{Version: "1.101.284", Tags: []string{"tag:app"}}}, want: db.ServiceNetworkConfig{Modes: []string{"iso", "ts"}, TSVersion: "1.101.284", TSTags: []string{"tag:app"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := desiredServiceNetworkConfig(tt.svc.View()); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("desiredServiceNetworkConfig() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestApplyServiceNetworkPatch(t *testing.T) {
	current := db.ServiceNetworkConfig{
		Modes:         []string{"lan", "ts"},
		TSVersion:     "1.100.0",
		TSExitNode:    "100.64.0.1",
		TSTags:        []string{"tag:app"},
		MacvlanParent: "eno1",
		MacvlanVLAN:   7,
		MacvlanMAC:    "02:00:00:00:00:07",
	}
	tests := []struct {
		name  string
		flags cli.ServiceSetFlags
		want  db.ServiceNetworkConfig
	}{
		{
			name:  "net replaces modes but preserves inactive settings",
			flags: cli.ServiceSetFlags{Net: "svc", NetSet: true},
			want:  db.ServiceNetworkConfig{Modes: []string{"svc"}, TSVersion: "1.100.0", TSExitNode: "100.64.0.1", TSTags: []string{"tag:app"}, MacvlanParent: "eno1", MacvlanVLAN: 7, MacvlanMAC: "02:00:00:00:00:07"},
		},
		{
			name:  "sets individual fields",
			flags: cli.ServiceSetFlags{TsVer: "1.101.284", TsVerSet: true, TsExit: "100.64.0.2", TsExitSet: true, TsTags: []string{"tag:ops"}, TsTagsSet: true, MacvlanParent: "eth0", MacvlanParentSet: true, MacvlanVlan: 42, MacvlanVlanSet: true, MacvlanMac: "02:00:00:00:00:42", MacvlanMacSet: true},
			want:  db.ServiceNetworkConfig{Modes: []string{"lan", "ts"}, TSVersion: "1.101.284", TSExitNode: "100.64.0.2", TSTags: []string{"tag:ops"}, MacvlanParent: "eth0", MacvlanVLAN: 42, MacvlanMAC: "02:00:00:00:00:42"},
		},
		{
			name:  "clears individual fields",
			flags: cli.ServiceSetFlags{Net: "svc", NetSet: true, TsVerSet: true, TsExitSet: true, TsTags: []string{}, TsTagsSet: true, MacvlanParentSet: true, MacvlanVlanSet: true, MacvlanMacSet: true},
			want:  db.ServiceNetworkConfig{Modes: []string{"svc"}, TSTags: []string{}},
		},
		{
			name:  "clears inactive macvlan parent independently",
			flags: cli.ServiceSetFlags{Net: "svc", NetSet: true, MacvlanParentSet: true},
			want:  db.ServiceNetworkConfig{Modes: []string{"svc"}, TSVersion: "1.100.0", TSExitNode: "100.64.0.1", TSTags: []string{"tag:app"}, MacvlanVLAN: 7, MacvlanMAC: "02:00:00:00:00:07"},
		},
		{
			name:  "clears inactive macvlan VLAN independently",
			flags: cli.ServiceSetFlags{Net: "svc", NetSet: true, MacvlanVlanSet: true},
			want:  db.ServiceNetworkConfig{Modes: []string{"svc"}, TSVersion: "1.100.0", TSExitNode: "100.64.0.1", TSTags: []string{"tag:app"}, MacvlanParent: "eno1", MacvlanMAC: "02:00:00:00:00:07"},
		},
		{
			name:  "clears inactive macvlan MAC independently",
			flags: cli.ServiceSetFlags{Net: "svc", NetSet: true, MacvlanMacSet: true},
			want:  db.ServiceNetworkConfig{Modes: []string{"svc"}, TSVersion: "1.100.0", TSExitNode: "100.64.0.1", TSTags: []string{"tag:app"}, MacvlanParent: "eno1", MacvlanVLAN: 7},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := applyServiceNetworkPatch(current, tt.flags)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("applyServiceNetworkPatch() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestApplyServiceNetworkPatchValidatesResult(t *testing.T) {
	withTags := db.ServiceNetworkConfig{Modes: []string{"host"}, TSTags: []string{"tag:app"}}
	tests := []struct {
		name    string
		current db.ServiceNetworkConfig
		flags   cli.ServiceSetFlags
		wantErr string
		want    db.ServiceNetworkConfig
	}{
		{name: "ts needs tags", current: db.ServiceNetworkConfig{Modes: []string{"host"}}, flags: cli.ServiceSetFlags{Net: "ts", NetSet: true}, wantErr: "tailscale tags"},
		{name: "entering ts inherits tags", current: withTags, flags: cli.ServiceSetFlags{Net: "ts", NetSet: true}, want: db.ServiceNetworkConfig{Modes: []string{"ts"}, TSTags: []string{"tag:app"}}},
		{name: "host cannot combine", current: withTags, flags: cli.ServiceSetFlags{Net: "host,ts", NetSet: true}, wantErr: "host cannot combine"},
		{name: "iso cannot combine with svc", current: withTags, flags: cli.ServiceSetFlags{Net: "iso,svc", NetSet: true}, wantErr: "iso cannot combine"},
		{name: "vlan range", current: db.ServiceNetworkConfig{Modes: []string{"lan"}}, flags: cli.ServiceSetFlags{MacvlanVlan: 4095, MacvlanVlanSet: true}, wantErr: "between 1 and 4094"},
		{name: "macvlan patch needs lan", current: db.ServiceNetworkConfig{Modes: []string{"host"}}, flags: cli.ServiceSetFlags{MacvlanParent: "eno1", MacvlanParentSet: true}, wantErr: "require LAN"},
		{name: "inactive macvlan write still needs lan", current: db.ServiceNetworkConfig{Modes: []string{"svc"}, MacvlanParent: "eno1"}, flags: cli.ServiceSetFlags{MacvlanMac: "02:00:00:00:00:07", MacvlanMacSet: true}, wantErr: "require LAN"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := applyServiceNetworkPatch(tt.current, tt.flags)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("applyServiceNetworkPatch() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("applyServiceNetworkPatch() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestServiceNetworkConfigNormalizesDeterministically(t *testing.T) {
	input := db.ServiceNetworkConfig{Modes: []string{" TS ", "lan", "ts"}, TSTags: []string{" tag:app ", "", "tag:ops", "tag:app"}}
	first, err := normalizeServiceNetworkConfig(input)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"lan", "ts"}; !reflect.DeepEqual(first.Modes, want) {
		t.Fatalf("normalized modes = %#v, want %#v", first.Modes, want)
	}
	if want := []string{"tag:app", "tag:ops"}; !reflect.DeepEqual(first.TSTags, want) {
		t.Fatalf("normalized tags = %#v, want %#v", first.TSTags, want)
	}
	second, err := normalizeServiceNetworkConfig(first)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("normalization is not idempotent: second %#v, first %#v", second, first)
	}
}

func TestServiceNetworkConfigRejectsInvalidTailscaleTags(t *testing.T) {
	tests := []struct {
		name    string
		current db.ServiceNetworkConfig
		flags   cli.ServiceSetFlags
		wantErr string
	}{
		{name: "whitespace does not satisfy ts", current: db.ServiceNetworkConfig{Modes: []string{"host"}, TSTags: []string{"   "}}, flags: cli.ServiceSetFlags{Net: "ts", NetSet: true}, wantErr: "tailscale tags"},
		{name: "empty tags do not satisfy ts", current: db.ServiceNetworkConfig{Modes: []string{"ts"}, TSTags: []string{""}}, wantErr: "tailscale tags"},
		{name: "malformed tag rejected", current: db.ServiceNetworkConfig{Modes: []string{"ts"}, TSTags: []string{"tag:bad!"}}, wantErr: "invalid tailscale tag"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := applyServiceNetworkPatch(tt.current, tt.flags)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tt.wantErr) {
				t.Fatalf("applyServiceNetworkPatch() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestServiceNetworkConfigDoesNotPersistAuthKey(t *testing.T) {
	const authKey = "tskey-auth-secret"
	got, err := applyServiceNetworkPatch(db.ServiceNetworkConfig{Modes: []string{"ts"}, TSTags: []string{"tag:app"}}, cli.ServiceSetFlags{TsAuthKey: authKey, TsAuthKeySet: true})
	if err != nil {
		t.Fatalf("applyServiceNetworkPatch returned an error containing no secret: %v", err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), authKey) {
		t.Fatal("desired network configuration persisted an auth key")
	}
	options := networkOptsFromDesired(got, authKey)
	if options.Tailscale.AuthKey != authKey || strings.Contains(options.Interfaces, authKey) {
		t.Fatal("auth key did not remain isolated to installer options")
	}
}
