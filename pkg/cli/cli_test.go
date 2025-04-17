// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cli

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseRunFlagsAndArgs(t *testing.T) {
	args := []string{
		"--net", "ts",
		"--ts-ver", "1.2.3",
		"--ts-exit", "exit-node",
		"--ts-tags", "tag:a",
		"--ts-tags", "tag:b",
		"--ts-auth-key", "tskey-abc",
		"--macvlan-mac", "00:11:22:33:44:55",
		"--macvlan-vlan", "12",
		"--macvlan-parent", "eth0",
		"arg1", "arg2",
	}

	flags, outArgs, err := ParseRun(args)
	if err != nil {
		t.Fatalf("ParseRun failed: %v", err)
	}
	if flags.Net != "ts" {
		t.Errorf("Net = %q, want %q", flags.Net, "ts")
	}
	if flags.TsVer != "1.2.3" {
		t.Errorf("TsVer = %q, want %q", flags.TsVer, "1.2.3")
	}
	if flags.TsExit != "exit-node" {
		t.Errorf("TsExit = %q, want %q", flags.TsExit, "exit-node")
	}
	if flags.TsAuthKey != "tskey-abc" {
		t.Errorf("TsAuthKey = %q, want %q", flags.TsAuthKey, "tskey-abc")
	}
	if flags.MacvlanMac != "00:11:22:33:44:55" {
		t.Errorf("MacvlanMac = %q, want %q", flags.MacvlanMac, "00:11:22:33:44:55")
	}
	if flags.MacvlanVlan != 12 {
		t.Errorf("MacvlanVlan = %d, want %d", flags.MacvlanVlan, 12)
	}
	if flags.MacvlanParent != "eth0" {
		t.Errorf("MacvlanParent = %q, want %q", flags.MacvlanParent, "eth0")
	}
	wantTags := []string{"tag:a", "tag:b"}
	if !reflect.DeepEqual(flags.TsTags, wantTags) {
		t.Errorf("TsTags = %v, want %v", flags.TsTags, wantTags)
	}
	if got := strings.Join(outArgs, " "); got != "arg1 arg2" {
		t.Errorf("args = %q, want %q", got, "arg1 arg2")
	}
}

func TestParseRunStopsAtUnknownFlag(t *testing.T) {
	args := []string{
		"--net", "ts",
		"--ts-tags", "tag:a",
		"--unknown", "value",
		"arg1",
	}

	flags, outArgs, err := ParseRun(args)
	if err != nil {
		t.Fatalf("ParseRun failed: %v", err)
	}
	if flags.Net != "ts" {
		t.Errorf("Net = %q, want %q", flags.Net, "ts")
	}
	wantTags := []string{"tag:a"}
	if !reflect.DeepEqual(flags.TsTags, wantTags) {
		t.Errorf("TsTags = %v, want %v", flags.TsTags, wantTags)
	}
	if got := strings.Join(outArgs, " "); got != "--unknown value arg1" {
		t.Errorf("args = %q, want %q", got, "--unknown value arg1")
	}
}
