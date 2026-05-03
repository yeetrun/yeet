// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
	"tailscale.com/tailcfg"
)

func TestStatusesServiceNamesByTypeFiltersAndSorts(t *testing.T) {
	services := map[string]*db.Service{
		"worker": {Name: "worker", ServiceType: db.ServiceTypeSystemd},
		"api":    {Name: "api", ServiceType: db.ServiceTypeDockerCompose},
		"cache":  {Name: "cache", ServiceType: db.ServiceTypeDockerCompose},
		"unset":  {Name: "unset"},
	}

	got := serviceNamesByType(services, db.ServiceTypeDockerCompose)
	want := []string{"api", "cache"}
	if !slices.Equal(got, want) {
		t.Fatalf("serviceNamesByType = %v, want %v", got, want)
	}
}

func TestStatusesDockerComposeRunningDecision(t *testing.T) {
	if !dockerComposeStatusRunning(svc.DockerComposeStatus{
		"api":    svc.StatusStopped,
		"worker": svc.StatusRunning,
	}) {
		t.Fatalf("expected one running component to make the service running")
	}
	if dockerComposeStatusRunning(svc.DockerComposeStatus{
		"api":    svc.StatusStopped,
		"worker": svc.StatusUnknown,
	}) {
		t.Fatalf("expected no running components to make the service stopped")
	}
}

func TestCallerValidationRules(t *testing.T) {
	tests := []struct {
		name       string
		serverTags []string
		serverUser tailcfg.UserID
		callerTags []string
		callerUser tailcfg.UserID
		wantErr    bool
	}{
		{
			name:       "tagged caller allowed by overlapping tagged server",
			serverTags: []string{"tag:prod", "tag:web"},
			callerTags: []string{"tag:web"},
		},
		{
			name:       "tagged caller rejected without overlap",
			serverTags: []string{"tag:prod"},
			callerTags: []string{"tag:db"},
			wantErr:    true,
		},
		{
			name:       "untagged caller allowed when server is tagged",
			serverTags: []string{"tag:prod"},
		},
		{
			name:       "untagged caller allowed for same user",
			serverUser: 1,
			callerUser: 1,
		},
		{
			name:       "untagged caller rejected for different user",
			serverUser: 1,
			callerUser: 2,
			wantErr:    true,
		},
		{
			name:       "tagged caller rejected when server is untagged",
			serverUser: 1,
			callerTags: []string{"tag:web"},
			callerUser: 1,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCallerIdentity(tt.serverTags, tt.serverUser, tt.callerTags, tt.callerUser)
			if tt.wantErr {
				if !errors.Is(err, errUnauthorized) {
					t.Fatalf("validateCallerIdentity error = %v, want errUnauthorized", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateCallerIdentity error = %v, want nil", err)
			}
		})
	}
}

func TestEnsureDirsServiceDirectoryPlan(t *testing.T) {
	root := filepath.Join(t.TempDir(), "services")
	got := serviceDirectoryPlan(root, "api")
	want := []string{
		filepath.Join(root, "api", "bin"),
		filepath.Join(root, "api", "data"),
		filepath.Join(root, "api", "env"),
		filepath.Join(root, "api", "run"),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("serviceDirectoryPlan = %v, want %v", got, want)
	}
}

func TestRemoveServiceDirectoryRemovalPlanSkipsData(t *testing.T) {
	root := filepath.Join(t.TempDir(), "svc")
	paths := []string{
		filepath.Join(root, "bin"),
		filepath.Join(root, "data"),
		filepath.Join(root, "env"),
		filepath.Join(root, "run"),
	}

	got := serviceChildDirsToRemove(paths)
	want := []string{
		filepath.Join(root, "bin"),
		filepath.Join(root, "env"),
		filepath.Join(root, "run"),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("serviceChildDirsToRemove = %v, want %v", got, want)
	}
}

func TestRemoveServiceTailscaleStableIDDecision(t *testing.T) {
	stableID := tailcfg.StableNodeID("node-123")
	withID := (&db.Service{
		Name:  "api",
		TSNet: &db.TailscaleNetwork{StableID: stableID},
	}).View()
	if got := tailscaleStableIDForRemoval(withID); got != string(stableID) {
		t.Fatalf("tailscaleStableIDForRemoval = %q, want %q", got, stableID)
	}

	withoutID := (&db.Service{Name: "api"}).View()
	if got := tailscaleStableIDForRemoval(withoutID); got != "" {
		t.Fatalf("tailscaleStableIDForRemoval without TSNet = %q, want empty", got)
	}
}

func TestEventDataMarshalJSON(t *testing.T) {
	raw, err := json.Marshal(EventData{})
	if err != nil {
		t.Fatalf("marshal nil event data: %v", err)
	}
	if string(raw) != "null" {
		t.Fatalf("nil event data json = %s", raw)
	}
	raw, err = json.Marshal(EventData{Data: map[string]string{"k": "v"}})
	if err != nil {
		t.Fatalf("marshal event data: %v", err)
	}
	if string(raw) != `{"k":"v"}` {
		t.Fatalf("event data json = %s", raw)
	}
}

func TestServerVerifyCallerUsesAuthorizeFunc(t *testing.T) {
	server := newTestServer(t)
	wantErr := errors.New("denied")
	var gotRemote string
	server.cfg.AuthorizeFunc = func(ctx context.Context, remoteAddr string) error {
		gotRemote = remoteAddr
		return wantErr
	}

	err := server.verifyCaller(context.Background(), "100.64.0.1:1234")
	if !errors.Is(err, wantErr) {
		t.Fatalf("verifyCaller error = %v, want %v", err, wantErr)
	}
	if gotRemote != "100.64.0.1:1234" {
		t.Fatalf("remote = %q", gotRemote)
	}
}

func TestServerRegistryHandlerAndClosedRegistryListener(t *testing.T) {
	server := newTestServer(t)
	if server.RegistryHandler() != server.registry {
		t.Fatal("RegistryHandler did not return server registry")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("Close listener: %v", err)
	}
	if err := server.ServeInternalRegistry(ln); err == nil {
		t.Fatal("expected serving closed listener to return error")
	}
}

func TestEnsureServiceDirCreatesDirAndSkipsRootChown(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "svc", "bin")
	if err := ensureServiceDir(dir, "root"); err != nil {
		t.Fatalf("ensureServiceDir root: %v", err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("dir stat = %v, %v", info, err)
	}
	if err := ensureServiceDir(filepath.Join(t.TempDir(), "svc", "bin"), "definitely-not-a-local-user"); err == nil {
		t.Fatal("expected lookup error for unknown user")
	}
}

func TestIsServiceTypeRunningRejectsUnknownType(t *testing.T) {
	if _, err := newTestServer(t).isServiceTypeRunning("svc", db.ServiceType("bogus")); err == nil {
		t.Fatal("expected unknown service type error")
	}
}

func TestRemoveServiceRemovesConfigDirsPreservesDataAndPublishesEvent(t *testing.T) {
	server := newTestServer(t)
	serviceRoot := server.serviceRootDir("api")
	for _, dir := range []string{"bin", "data", "env", "run"} {
		if err := os.MkdirAll(filepath.Join(serviceRoot, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := server.cfg.DB.Set(&db.Data{
		Services: map[string]*db.Service{
			"api": {Name: "api", ServiceType: db.ServiceType("unknown")},
		},
	}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	ch := make(chan Event, 1)
	handle := server.AddEventListener(ch, nil)
	defer server.RemoveEventListener(handle)

	report, err := server.RemoveService("api")
	if err != nil {
		t.Fatalf("RemoveService: %v", err)
	}
	if len(report.Warnings) == 0 {
		t.Fatal("expected running-check warning for unknown service type")
	}
	event := <-ch
	if event.Type != EventTypeServiceDeleted || event.ServiceName != "api" {
		t.Fatalf("event = %#v", event)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("DB.Get: %v", err)
	}
	if dv.Services().Contains("api") {
		t.Fatal("service remains in db")
	}
	for _, removed := range []string{"bin", "env", "run"} {
		if _, err := os.Stat(filepath.Join(serviceRoot, removed)); !os.IsNotExist(err) {
			t.Fatalf("%s stat err = %v, want not exist", removed, err)
		}
	}
	if _, err := os.Stat(filepath.Join(serviceRoot, "data")); err != nil {
		t.Fatalf("data dir should remain: %v", err)
	}
}

func TestAddWarningIgnoresNil(t *testing.T) {
	var report RemoveReport
	report.addWarning(nil)
	report.addWarning(errors.New("warn"))
	if len(report.Warnings) != 1 || !strings.Contains(report.Warnings[0].Error(), "warn") {
		t.Fatalf("warnings = %#v", report.Warnings)
	}
}
