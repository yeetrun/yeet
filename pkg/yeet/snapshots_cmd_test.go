// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestSnapshotsDefaultsRoutesToSystemService(t *testing.T) {
	preserveSvcCommandGlobals(t)
	var gotService string
	var gotArgs []string
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		gotService = service
		gotArgs = append([]string{}, args...)
		return nil
	}
	req := svcCommandRequest{
		Command: svcCommand{
			Name:    "snapshots",
			Args:    []string{"defaults", "show"},
			RawArgs: []string{"snapshots", "defaults", "show"},
		},
	}
	if err := handleSvcSnapshots(context.Background(), req); err != nil {
		t.Fatalf("handleSvcSnapshots: %v", err)
	}
	if gotService != systemServiceName {
		t.Fatalf("service = %q, want %s", gotService, systemServiceName)
	}
	if !reflect.DeepEqual(gotArgs, []string{"snapshots", "defaults", "show"}) {
		t.Fatalf("args = %#v", gotArgs)
	}
}

func TestSnapshotsDefaultsSetRejectsUnexpectedArgs(t *testing.T) {
	preserveSvcCommandGlobals(t)
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		t.Fatalf("unexpected remote exec: service=%q args=%v", service, args)
		return nil
	}
	req := svcCommandRequest{
		Command: svcCommand{
			Name:    "snapshots",
			Args:    []string{"defaults", "set", "--enabled=false", "extra"},
			RawArgs: []string{"snapshots", "defaults", "set", "--enabled=false", "extra"},
		},
	}
	err := handleSvcSnapshots(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "unexpected snapshots defaults args: extra") {
		t.Fatalf("handleSvcSnapshots error = %v, want unexpected args", err)
	}
}

func TestSnapshotsLifecycleRoutesToSystemService(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantTTY bool
	}{
		{name: "list", args: []string{"list", "svc-a", "--format=json"}},
		{name: "inspect", args: []string{"inspect", "svc-a", "yeet-abc", "--format=json-pretty"}},
		{name: "create", args: []string{"create", "svc-a", "--comment", "before upgrade", "--full"}},
		{name: "clone", args: []string{"clone", "svc-a", "yeet-abc", "svc-copy", "--start"}},
		{name: "restore", args: []string{"restore", "svc-a", "yeet-abc", "--stop", "--start", "--yes", "--mode=full", "--generation=snapshot"}},
		{name: "restore prompt", args: []string{"restore", "svc-a", "yeet-abc", "--stop"}, wantTTY: true},
		{name: "rm", args: []string{"rm", "svc-a", "yeet-abc", "--yes"}},
		{name: "rm prompt", args: []string{"rm", "svc-a", "yeet-abc"}, wantTTY: true},
		{name: "protect", args: []string{"protect", "svc-a", "yeet-abc"}},
		{name: "unprotect", args: []string{"unprotect", "svc-a", "yeet-abc"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preserveSvcCommandGlobals(t)
			rawArgs := append([]string{"snapshots"}, tt.args...)
			var gotService string
			var gotArgs []string
			var gotTTY bool
			execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
				gotService = service
				gotArgs = append([]string{}, args...)
				gotTTY = tty
				return nil
			}
			req := svcCommandRequest{
				Service: "svc-a",
				Command: svcCommand{
					Name:    "snapshots",
					Args:    tt.args,
					RawArgs: rawArgs,
				},
			}
			if err := handleSvcSnapshots(context.Background(), req); err != nil {
				t.Fatalf("handleSvcSnapshots: %v", err)
			}
			if gotService != systemServiceName {
				t.Fatalf("service = %q, want %s", gotService, systemServiceName)
			}
			if !reflect.DeepEqual(gotArgs, rawArgs) {
				t.Fatalf("args = %#v, want %#v", gotArgs, rawArgs)
			}
			if gotTTY != tt.wantTTY {
				t.Fatalf("tty = %t, want %t", gotTTY, tt.wantTTY)
			}
		})
	}
}

func TestSnapshotsLifecycleRejectsBadInput(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "missing command", args: nil, wantErr: "snapshots requires a command"},
		{name: "unknown command", args: []string{"bogus"}, wantErr: `unknown snapshots command "bogus"`},
		{name: "bad list format", args: []string{"list", "svc-a", "--format=yaml"}, wantErr: "--format must be table, json, or json-pretty"},
		{name: "inspect missing snapshot", args: []string{"inspect", "svc-a"}, wantErr: "snapshots inspect requires service and snapshot"},
		{name: "create missing service", args: []string{"create"}, wantErr: "snapshots create requires a service"},
		{name: "clone missing new service", args: []string{"clone", "svc-a", "yeet-abc"}, wantErr: "snapshots clone requires service, snapshot, and new service"},
		{name: "restore missing snapshot", args: []string{"restore", "svc-a"}, wantErr: "snapshots restore requires service and snapshot"},
		{name: "restore invalid mode", args: []string{"restore", "svc-a", "yeet-abc", "--mode=bogus"}, wantErr: "--mode must be disk or full"},
		{name: "restore missing mode value", args: []string{"restore", "svc-a", "yeet-abc", "--mode"}, wantErr: "--mode must be disk or full"},
		{name: "restore empty generation value", args: []string{"restore", "svc-a", "yeet-abc", "--generation="}, wantErr: "--generation must be current or snapshot"},
		{name: "restore invalid generation", args: []string{"restore", "svc-a", "yeet-abc", "--generation=bogus"}, wantErr: "--generation must be current or snapshot"},
		{name: "rm missing snapshot", args: []string{"rm", "svc-a"}, wantErr: "snapshots rm requires service and snapshot"},
		{name: "protect missing snapshot", args: []string{"protect", "svc-a"}, wantErr: "snapshots protect requires service and snapshot"},
		{name: "unprotect missing snapshot", args: []string{"unprotect", "svc-a"}, wantErr: "snapshots unprotect requires service and snapshot"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preserveSvcCommandGlobals(t)
			execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
				t.Fatalf("unexpected remote exec: service=%q args=%v", service, args)
				return nil
			}
			req := svcCommandRequest{
				Command: svcCommand{
					Name:    "snapshots",
					Args:    tt.args,
					RawArgs: append([]string{"snapshots"}, tt.args...),
				},
			}
			err := handleSvcSnapshots(context.Background(), req)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("handleSvcSnapshots error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
