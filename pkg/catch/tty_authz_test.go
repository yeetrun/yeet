// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

func TestExecRequestPermissionsForShellTargets(t *testing.T) {
	tests := []struct {
		name string
		req  catchrpc.ExecRequest
		want yeetPermission
	}{
		{name: "host shell", req: catchrpc.ExecRequest{Target: catchrpc.ExecTargetHostShell}, want: permissionSSH},
		{name: "service shell", req: catchrpc.ExecRequest{Target: catchrpc.ExecTargetServiceShell, Service: "svc"}, want: permissionSSH},
		{name: "VM SSH proxy", req: catchrpc.ExecRequest{Target: catchrpc.ExecTargetVMSSHProxy, Service: "devbox"}, want: permissionSSH},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := execRequestPermissions(tt.req)
			if err != nil {
				t.Fatalf("execRequestPermissions: %v", err)
			}
			if !got.has(tt.want) {
				t.Fatalf("permissions = %#v, want %q", got, tt.want)
			}
		})
	}
}

func TestTTYCommandPermissions(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want yeetPermission
	}{
		{name: "events", args: []string{"events", "--all"}, want: permissionRead},
		{name: "logs", args: []string{"logs"}, want: permissionRead},
		{name: "status", args: []string{"status"}, want: permissionRead},
		{name: "version", args: []string{"version"}, want: permissionRead},
		{name: "ip", args: []string{"ip"}, want: permissionRead},
		{name: "docker outdated", args: []string{"docker", "outdated"}, want: permissionRead},
		{name: "docker update", args: []string{"docker", "update"}, want: permissionManage},
		{name: "snapshots list", args: []string{"snapshots", "list"}, want: permissionRead},
		{name: "snapshots defaults show", args: []string{"snapshots", "defaults", "show"}, want: permissionRead},
		{name: "snapshots defaults set", args: []string{"snapshots", "defaults", "set", "--enabled=true"}, want: permissionManage},
		{name: "snapshots restore", args: []string{"snapshots", "restore", "svc", "snap"}, want: permissionManage},
		{name: "service generations", args: []string{"service", "generations"}, want: permissionRead},
		{name: "service set", args: []string{"service", "set", "--copy"}, want: permissionManage},
		{name: "tailscale status", args: []string{"tailscale", "status"}, want: permissionRead},
		{name: "tailscale update", args: []string{"tailscale", "update"}, want: permissionManage},
		{name: "vm images ls", args: []string{"vm", "images", "ls"}, want: permissionRead},
		{name: "vm memory status", args: []string{"vm", "memory"}, want: permissionRead},
		{name: "vm console", args: []string{"vm", "console"}, want: permissionManage},
		{name: "run", args: []string{"run", "ghcr.io/example/app:latest"}, want: permissionManage},
		{name: "remove", args: []string{"remove", "--clean"}, want: permissionManage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ttyCommandPermissions(tt.args)
			if err != nil {
				t.Fatalf("ttyCommandPermissions: %v", err)
			}
			if !got.has(tt.want) {
				t.Fatalf("permissions = %#v, want %q", got, tt.want)
			}
		})
	}
}

func TestTTYCommandPermissionsFailClosed(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"unknown"},
		{"docker", "system"},
		{"snapshots", "unknown"},
		{"service", "unknown"},
		{"vm", "unknown"},
		{"vm", "images", "unknown"},
		{"tailscale"},
	} {
		_, err := ttyCommandPermissions(args)
		if err == nil {
			t.Fatalf("ttyCommandPermissions(%#v) error = nil, want fail closed", args)
		}
		if !strings.Contains(err.Error(), "unclassified") {
			t.Fatalf("ttyCommandPermissions(%#v) error = %v, want unclassified", args, err)
		}
	}
}
