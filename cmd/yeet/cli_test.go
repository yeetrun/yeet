// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"reflect"
	"testing"

	"github.com/shayne/yargs"
	"github.com/yeetrun/yeet/pkg/cli"
)

func TestParseGlobalFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantVal string
		wantSvc string
		wantOut []string
	}{
		{
			name:    "consumes separate value",
			args:    []string{"--host", "catch", "status"},
			wantVal: "catch",
			wantSvc: "",
			wantOut: []string{"status"},
		},
		{
			name:    "consumes equals value",
			args:    []string{"status", "--host=catch"},
			wantVal: "catch",
			wantSvc: "",
			wantOut: []string{"status"},
		},
		{
			name:    "last value wins",
			args:    []string{"--host", "one", "--host", "two", "status"},
			wantVal: "two",
			wantSvc: "",
			wantOut: []string{"status"},
		},
		{
			name:    "stops at double dash",
			args:    []string{"--host", "catch", "--", "--host", "ignored"},
			wantVal: "catch",
			wantSvc: "",
			wantOut: []string{"--", "--host", "ignored"},
		},
		{
			name:    "unknown flags are preserved",
			args:    []string{"--unknown", "x", "--host", "catch"},
			wantVal: "catch",
			wantSvc: "",
			wantOut: []string{"--unknown", "x"},
		},
		{
			name:    "service flag parsed",
			args:    []string{"--service", "svc-a", "status"},
			wantVal: "",
			wantSvc: "svc-a",
			wantOut: []string{"status"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flags, out, err := parseGlobalFlags(tt.args)
			if err != nil {
				t.Fatalf("parseGlobalFlags error: %v", err)
			}
			if flags.Host != tt.wantVal {
				t.Fatalf("Host = %q, want %q", flags.Host, tt.wantVal)
			}
			if flags.Service != tt.wantSvc {
				t.Fatalf("Service = %q, want %q", flags.Service, tt.wantSvc)
			}
			if !reflect.DeepEqual(out, tt.wantOut) {
				t.Fatalf("out = %#v, want %#v", out, tt.wantOut)
			}
		})
	}
}

func TestResolveGlobalOverrides(t *testing.T) {
	tests := []struct {
		name     string
		flags    globalFlagsParsed
		wantHost string
		wantSvc  string
	}{
		{
			name:     "host only",
			flags:    globalFlagsParsed{Host: "catch-a"},
			wantHost: "catch-a",
		},
		{
			name:    "service only",
			flags:   globalFlagsParsed{Service: "svc-a"},
			wantSvc: "svc-a",
		},
		{
			name:     "qualified service overrides host",
			flags:    globalFlagsParsed{Host: "catch-a", Service: "svc-a@catch-b"},
			wantHost: "catch-b",
			wantSvc:  "svc-a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveGlobalOverrides(tt.flags)
			if got.host != tt.wantHost {
				t.Fatalf("host = %q, want %q", got.host, tt.wantHost)
			}
			if got.service != tt.wantSvc {
				t.Fatalf("service = %q, want %q", got.service, tt.wantSvc)
			}
		})
	}
}

func TestPrepareCommandRoute(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		service     string
		wantArgs    []string
		wantHost    string
		wantService string
		wantBridged []string
	}{
		{
			name:        "rewrites env set shorthand",
			args:        []string{"env", "svc-a", "FOO=bar"},
			wantArgs:    []string{"env", "set", "FOO=bar"},
			wantService: "svc-a",
			wantBridged: []string{"env", "set", "FOO=bar"},
		},
		{
			name:        "splits host from command",
			args:        []string{"status@catch-a", "svc-a"},
			wantArgs:    []string{"status"},
			wantHost:    "catch-a",
			wantService: "svc-a",
			wantBridged: []string{"status"},
		},
		{
			name:        "events host defaults all services",
			args:        []string{"events@catch-a"},
			wantArgs:    []string{"events", "--all"},
			wantHost:    "catch-a",
			wantService: "",
			wantBridged: nil,
		},
		{
			name:        "honors existing service override",
			args:        []string{"status", "--format", "json"},
			service:     "svc-override",
			wantArgs:    []string{"status", "--format", "json"},
			wantService: "svc-override",
			wantBridged: []string{"status", "--format", "json"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := prepareCommandRoute(tt.args, tt.service)
			if !reflect.DeepEqual(got.args, tt.wantArgs) {
				t.Fatalf("args = %#v, want %#v", got.args, tt.wantArgs)
			}
			if got.host != tt.wantHost {
				t.Fatalf("host = %q, want %q", got.host, tt.wantHost)
			}
			if got.service != tt.wantService {
				t.Fatalf("service = %q, want %q", got.service, tt.wantService)
			}
			if !reflect.DeepEqual(got.bridgedArgs, tt.wantBridged) {
				t.Fatalf("bridgedArgs = %#v, want %#v", got.bridgedArgs, tt.wantBridged)
			}
		})
	}
}

func TestParseListHostsFlags(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantTags []string
	}{
		{
			name:     "default tags",
			args:     []string{},
			wantTags: []string{"tag:catch"},
		},
		{
			name:     "comma-separated tags",
			args:     []string{"list-hosts", "--tags", "tag:a,tag:b"},
			wantTags: []string{"tag:a", "tag:b"},
		},
		{
			name:     "repeated tags",
			args:     []string{"list-hosts", "--tags", "tag:a", "--tags", "tag:b"},
			wantTags: []string{"tag:a", "tag:b"},
		},
		{
			name:     "ignores unknown flags",
			args:     []string{"list-hosts", "--tags", "tag:a", "--unknown", "x"},
			wantTags: []string{"tag:a"},
		},
		{
			name:     "stops at double dash",
			args:     []string{"list-hosts", "--tags", "tag:a", "--", "--tags", "tag:b"},
			wantTags: []string{"tag:a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flags, err := parseListHostsFlags(tt.args)
			if err != nil {
				t.Fatalf("parseListHostsFlags error: %v", err)
			}
			if !reflect.DeepEqual(flags.Tags, tt.wantTags) {
				t.Fatalf("Tags = %#v, want %#v", flags.Tags, tt.wantTags)
			}
		})
	}
}

func TestGroupHandlersCoverRemoteGroupInfos(t *testing.T) {
	groups := buildGroupHandlers()
	for groupName, info := range cli.RemoteGroupInfos() {
		group, ok := groups[groupName]
		if !ok {
			t.Fatalf("missing group handler for %q", groupName)
		}
		for cmdName := range info.Commands {
			if _, ok := group.Commands[cmdName]; !ok {
				t.Fatalf("missing handler for group %q command %q", groupName, cmdName)
			}
		}
	}
}

func TestEnvCopyAlias(t *testing.T) {
	helpConfig := buildHelpConfig()
	args := yargs.ApplyAliases([]string{"env", "cp", "svc", "file"}, helpConfig)
	if len(args) < 2 || args[1] != "copy" {
		t.Fatalf("expected alias to resolve to copy, got %v", args)
	}
}

func TestCopyAlias(t *testing.T) {
	helpConfig := buildHelpConfig()
	args := yargs.ApplyAliases([]string{"cp", "src", "dst"}, helpConfig)
	if len(args) == 0 || args[0] != "copy" {
		t.Fatalf("expected alias to resolve to copy, got %v", args)
	}
}

func TestRewriteEnvSetArgs(t *testing.T) {
	args := rewriteEnvSetArgs([]string{"env", "svc-a", "FOO="})
	want := []string{"env", "set", "svc-a", "FOO="}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("unexpected args: %v", args)
	}

	args = rewriteEnvSetArgs([]string{"env", "show", "svc-a"})
	want = []string{"env", "show", "svc-a"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("unexpected args: %v", args)
	}
}
