// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"errors"
	"path/filepath"
	"slices"
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
