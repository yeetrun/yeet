// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"os/user"
	"strconv"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestResolveServiceIdentity(t *testing.T) {
	stubServiceIdentityLookups(t, map[string]*user.User{
		"app":  {Username: "app", Uid: "1002", Gid: "1003"},
		"root": {Username: "root", Uid: "0", Gid: "0"},
	}, map[string]*user.Group{
		"app":     {Name: "app", Gid: "1003"},
		"root":    {Name: "root", Gid: "0"},
		"workers": {Name: "workers", Gid: "1010"},
	})

	tests := []struct {
		spec    string
		wantUID uint32
		wantGID uint32
		wantErr string
	}{
		{spec: "app", wantUID: 1002, wantGID: 1003},
		{spec: "app:workers", wantUID: 1002, wantGID: 1010},
		{spec: "1002", wantUID: 1002, wantGID: 1003},
		{spec: "70000:70001", wantUID: 70000, wantGID: 70001},
		{spec: "root", wantUID: 0, wantGID: 0},
		{spec: "1009", wantErr: "numeric UID without an account requires a numeric GID"},
		{spec: "1009:workers", wantErr: "numeric UID without an account requires a numeric GID"},
		{spec: "missing", wantErr: "user does not exist"},
		{spec: "app:missing", wantErr: "group does not exist"},
		{spec: "app:", wantErr: "group must not be empty"},
		{spec: ":app", wantErr: "user must not be empty"},
	}
	for _, tt := range tests {
		t.Run(tt.spec, func(t *testing.T) {
			got, err := resolveServiceIdentity(tt.spec)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("resolveServiceIdentity(%q) error = %v, want %q", tt.spec, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveServiceIdentity(%q): %v", tt.spec, err)
			}
			if got.Persisted.UID != tt.wantUID || got.Persisted.GID != tt.wantGID {
				t.Fatalf("resolveServiceIdentity(%q) IDs = %d:%d, want %d:%d", tt.spec, got.Persisted.UID, got.Persisted.GID, tt.wantUID, tt.wantGID)
			}
		})
	}
}

func TestResolveServiceIdentityPreservesRequestedForms(t *testing.T) {
	stubServiceIdentityLookups(t, map[string]*user.User{
		"app": {Username: "app", Uid: "1002", Gid: "1003"},
	}, map[string]*user.Group{
		"app":     {Name: "app", Gid: "1003"},
		"workers": {Name: "workers", Gid: "1010"},
	})

	named, err := resolveServiceIdentity("app:workers")
	if err != nil {
		t.Fatal(err)
	}
	if named.Persisted.RequestedUser != "app" || named.Persisted.RequestedGroup != "workers" || named.UserName != "app" || named.GroupName != "workers" {
		t.Fatalf("named identity = %#v", named)
	}

	numeric, err := resolveServiceIdentity("70000:70001")
	if err != nil {
		t.Fatal(err)
	}
	if numeric.Persisted.RequestedUser != "70000" || numeric.Persisted.RequestedGroup != "70001" || numeric.UserName != "" || numeric.GroupName != "" {
		t.Fatalf("numeric identity = %#v", numeric)
	}
}

func TestServiceIdentityDrift(t *testing.T) {
	stubServiceIdentityLookups(t, map[string]*user.User{
		"app": {Username: "app", Uid: "1002", Gid: "1003"},
	}, map[string]*user.Group{
		"workers": {Name: "workers", Gid: "1010"},
	})

	tests := []struct {
		name    string
		id      db.ServiceIdentity
		wantErr string
	}{
		{
			name: "named identity matches",
			id:   db.ServiceIdentity{RequestedUser: "app", RequestedGroup: "workers", UID: 1002, GID: 1010},
		},
		{
			name:    "named user changed UID",
			id:      db.ServiceIdentity{RequestedUser: "app", RequestedGroup: "workers", UID: 1009, GID: 1010},
			wantErr: "UID",
		},
		{
			name:    "named group changed GID",
			id:      db.ServiceIdentity{RequestedUser: "app", RequestedGroup: "workers", UID: 1002, GID: 1011},
			wantErr: "GID",
		},
		{
			name: "unregistered numeric identity remains valid",
			id:   db.ServiceIdentity{RequestedUser: "70000", RequestedGroup: "70001", UID: 70000, GID: 70001},
		},
		{
			name:    "numeric request differs from persisted ID",
			id:      db.ServiceIdentity{RequestedUser: "70000", RequestedGroup: "70001", UID: 70002, GID: 70001},
			wantErr: "UID",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateServiceIdentityDrift(tt.id)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateServiceIdentityDrift: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateServiceIdentityDrift error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestServiceIdentityDriftRejectsMissingNamedAccount(t *testing.T) {
	stubServiceIdentityLookups(t, nil, nil)
	err := validateServiceIdentityDrift(db.ServiceIdentity{
		RequestedUser: "missing", RequestedGroup: "missing", UID: 1002, GID: 1003,
	})
	if err == nil || !strings.Contains(err.Error(), "user does not exist") {
		t.Fatalf("validateServiceIdentityDrift error = %v, want missing-user error", err)
	}
}

func TestServiceIdentityEffectiveAndClass(t *testing.T) {
	legacy := effectiveServiceIdentity(db.ServiceView{})
	if legacy.Persisted != (db.ServiceIdentity{RequestedUser: "root", RequestedGroup: "root", UID: 0, GID: 0}) || legacy.UserName != "root" || legacy.GroupName != "root" {
		t.Fatalf("invalid service effective identity = %#v", legacy)
	}
	withoutIdentity := effectiveServiceIdentity((&db.Service{Name: "legacy"}).View())
	if withoutIdentity != legacy {
		t.Fatalf("legacy effective identity = %#v, want %#v", withoutIdentity, legacy)
	}

	persisted := &db.ServiceIdentity{RequestedUser: "app", RequestedGroup: "workers", UID: 1002, GID: 1010}
	got := effectiveServiceIdentity((&db.Service{Name: "api", Identity: persisted}).View())
	if got.Persisted != *persisted || got.UserName != "app" || got.GroupName != "workers" {
		t.Fatalf("persisted effective identity = %#v", got)
	}

	tests := []struct {
		id   *db.ServiceIdentity
		want string
	}{
		{want: "legacy-root"},
		{id: &db.ServiceIdentity{RequestedUser: managedServiceUser, RequestedGroup: managedServiceUser, UID: 991, GID: 992}, want: "managed"},
		{id: &db.ServiceIdentity{RequestedUser: "app", RequestedGroup: "workers", UID: 1002, GID: 1010}, want: "operator"},
		{id: &db.ServiceIdentity{RequestedUser: "root", RequestedGroup: "root", UID: 0, GID: 0}, want: "explicit-root"},
		{id: &db.ServiceIdentity{RequestedUser: "0", RequestedGroup: "0", UID: 0, GID: 0}, want: "explicit-root"},
		{id: &db.ServiceIdentity{RequestedUser: "root", RequestedGroup: "staff", UID: 0, GID: 20}, want: "explicit-root"},
	}
	for _, tt := range tests {
		if got := serviceIdentityClass(tt.id); got != tt.want {
			t.Fatalf("serviceIdentityClass(%#v) = %q, want %q", tt.id, got, tt.want)
		}
	}
}

func stubServiceIdentityLookups(t *testing.T, users map[string]*user.User, groups map[string]*user.Group) {
	t.Helper()
	oldUserLookup := serviceUserLookup
	oldUserLookupID := serviceUserLookupID
	oldGroupLookup := serviceGroupLookup
	oldGroupLookupID := serviceGroupLookupID
	t.Cleanup(func() {
		serviceUserLookup = oldUserLookup
		serviceUserLookupID = oldUserLookupID
		serviceGroupLookup = oldGroupLookup
		serviceGroupLookupID = oldGroupLookupID
	})
	serviceUserLookup = func(name string) (*user.User, error) {
		if account := users[name]; account != nil {
			copy := *account
			return &copy, nil
		}
		return nil, user.UnknownUserError(name)
	}
	serviceUserLookupID = func(id string) (*user.User, error) {
		for _, account := range users {
			if account.Uid == id {
				copy := *account
				return &copy, nil
			}
		}
		value, _ := strconv.Atoi(id)
		return nil, user.UnknownUserIdError(value)
	}
	serviceGroupLookup = func(name string) (*user.Group, error) {
		if group := groups[name]; group != nil {
			copy := *group
			return &copy, nil
		}
		return nil, user.UnknownGroupError(name)
	}
	serviceGroupLookupID = func(id string) (*user.Group, error) {
		for _, group := range groups {
			if group.Gid == id {
				copy := *group
				return &copy, nil
			}
		}
		return nil, user.UnknownGroupIdError(id)
	}
}
