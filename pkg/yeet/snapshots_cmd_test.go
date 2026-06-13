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
		name string
		args []string
	}{
		{name: "list", args: []string{"list", "svc-a", "--format=json"}},
		{name: "inspect", args: []string{"inspect", "svc-a", "yeet-abc", "--format=json-pretty"}},
		{name: "create", args: []string{"create", "svc-a", "--comment", "before upgrade", "--full"}},
		{name: "rm", args: []string{"rm", "svc-a", "yeet-abc", "--yes"}},
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
			if gotTTY {
				t.Fatal("tty = true, want false")
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
