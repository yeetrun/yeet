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
		"--env-file", "prod.env",
		"-p", "8000:8000",
		"-p", "9000:9000",
		"--force",
		"--pull",
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
	if flags.EnvFile != "prod.env" {
		t.Errorf("EnvFile = %q, want %q", flags.EnvFile, "prod.env")
	}
	if !flags.Pull {
		t.Errorf("Pull = false, want true")
	}
	if !flags.Force {
		t.Errorf("Force = false, want true")
	}
	wantTags := []string{"tag:a", "tag:b"}
	if !reflect.DeepEqual(flags.TsTags, wantTags) {
		t.Errorf("TsTags = %v, want %v", flags.TsTags, wantTags)
	}
	wantPublish := []string{"8000:8000", "9000:9000"}
	if !reflect.DeepEqual(flags.Publish, wantPublish) {
		t.Errorf("Publish = %v, want %v", flags.Publish, wantPublish)
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

func TestParseStagePullFlag(t *testing.T) {
	args := []string{
		"--pull",
		"commit",
	}
	flags, subcmd, outArgs, err := ParseStage(args)
	if err != nil {
		t.Fatalf("ParseStage failed: %v", err)
	}
	if !flags.Pull {
		t.Fatalf("Pull = false, want true")
	}
	if subcmd != "commit" {
		t.Fatalf("subcmd = %q, want %q", subcmd, "commit")
	}
	if len(outArgs) != 0 {
		t.Fatalf("expected no args, got %v", outArgs)
	}
}

func TestParseEnvShowFlags(t *testing.T) {
	flags, outArgs, err := ParseEnvShow([]string{"--staged"})
	if err != nil {
		t.Fatalf("ParseEnvShow failed: %v", err)
	}
	if !flags.Staged {
		t.Fatalf("Staged = false, want true")
	}
	if len(outArgs) != 0 {
		t.Fatalf("expected no args, got %v", outArgs)
	}
}

func TestParseRemoveFlags(t *testing.T) {
	flags, outArgs, err := ParseRemove([]string{"-y", "--clean-config"})
	if err != nil {
		t.Fatalf("ParseRemove failed: %v", err)
	}
	if !flags.Yes {
		t.Fatalf("Yes = false, want true")
	}
	if !flags.CleanConfig {
		t.Fatalf("CleanConfig = false, want true")
	}
	if len(outArgs) != 0 {
		t.Fatalf("expected no args, got %v", outArgs)
	}
}

func TestParseInfoFlags(t *testing.T) {
	flags, outArgs, err := ParseInfo([]string{"--format=json"})
	if err != nil {
		t.Fatalf("ParseInfo failed: %v", err)
	}
	if flags.Format != "json" {
		t.Fatalf("Format = %q, want %q", flags.Format, "json")
	}
	if len(outArgs) != 0 {
		t.Fatalf("expected no args, got %v", outArgs)
	}

	flags, outArgs, err = ParseInfo(nil)
	if err != nil {
		t.Fatalf("ParseInfo (default) failed: %v", err)
	}
	if flags.Format != "plain" {
		t.Fatalf("Format = %q, want %q", flags.Format, "plain")
	}
	if len(outArgs) != 0 {
		t.Fatalf("expected no args, got %v", outArgs)
	}
}
