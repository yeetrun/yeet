// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"strings"
	"testing"

	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/types/ptr"
	"tailscale.com/types/views"
)

func TestPermissionsFromCapMapUnionsAllowValues(t *testing.T) {
	caps := tailcfg.PeerCapMap{
		yeetAppCapability: {
			tailcfg.RawMessage(`{"allow":["read","unknown"]}`),
			tailcfg.RawMessage(`{"allow":["manage","ssh","read"]}`),
		},
	}

	perms, err := permissionsFromCapMap(caps)
	if err != nil {
		t.Fatalf("permissionsFromCapMap: %v", err)
	}
	for _, perm := range []yeetPermission{permissionRead, permissionManage, permissionSSH} {
		if !perms.has(perm) {
			t.Fatalf("permissions missing %q: %#v", perm, perms)
		}
	}
	if perms.has(yeetPermission("unknown")) {
		t.Fatalf("unknown permission was retained: %#v", perms)
	}
}

func TestPermissionsFromCapMapMissingIsEmpty(t *testing.T) {
	perms, err := permissionsFromCapMap(tailcfg.PeerCapMap{})
	if err != nil {
		t.Fatalf("permissionsFromCapMap: %v", err)
	}
	if !perms.empty() {
		t.Fatalf("permissions = %#v, want empty", perms)
	}
}

func TestPermissionsFromCapMapRejectsMalformedJSON(t *testing.T) {
	_, err := permissionsFromCapMap(tailcfg.PeerCapMap{
		yeetAppCapability: {tailcfg.RawMessage(`{"allow":"read"}`)},
	})
	if err == nil {
		t.Fatal("permissionsFromCapMap error = nil, want malformed cap error")
	}
}

func TestMissingPermissionErrorIsActionable(t *testing.T) {
	err := missingPermissionError{permission: permissionManage}
	msg := err.Error()
	for _, want := range []string{
		`missing yeet permission "manage"`,
		string(yeetAppCapability),
		yeetAccessGrantsDocsURL,
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q missing %q", msg, want)
		}
	}
	if !errors.Is(err, errUnauthorized) {
		t.Fatalf("missingPermissionError should wrap errUnauthorized")
	}
}

func TestRequirePermissionsReportsFirstMissing(t *testing.T) {
	perms := newPermissionSet(permissionManage)
	err := requirePermissions(perms, permissionRead, permissionManage, permissionSSH)
	if err == nil || !strings.Contains(err.Error(), `"read"`) {
		t.Fatalf("requirePermissions error = %v, want missing read", err)
	}

	perms[permissionRead] = struct{}{}
	err = requirePermissions(perms, permissionRead, permissionManage, permissionSSH)
	if err == nil || !strings.Contains(err.Error(), `"ssh"`) {
		t.Fatalf("requirePermissions error = %v, want missing ssh", err)
	}
}

func TestAuthorizeCallerUsesAuthorizeFuncOverride(t *testing.T) {
	server := newTestServer(t)
	wantErr := errors.New("custom deny")
	var gotRemote string
	server.cfg.AuthorizeFunc = func(ctx context.Context, remoteAddr string) error {
		gotRemote = remoteAddr
		return wantErr
	}

	err := server.authorizeCaller(context.Background(), "100.64.0.1:1234", permissionRead)
	if !errors.Is(err, wantErr) {
		t.Fatalf("authorizeCaller error = %v, want %v", err, wantErr)
	}
	if gotRemote != "100.64.0.1:1234" {
		t.Fatalf("remote = %q", gotRemote)
	}
}

func TestAuthorizeCallerWithoutRequiredPermissionChecksTaggedIdentityOnly(t *testing.T) {
	server := newAuthzTestServer(t, newPermissionSet())

	if err := server.authorizeCaller(context.Background(), "100.64.0.1:1234"); err != nil {
		t.Fatalf("authorizeCaller without required permissions: %v", err)
	}
}

func TestAuthorizeCallerRequiresTaggedCatchNode(t *testing.T) {
	server := newAuthzTestServer(t, newPermissionSet(permissionRead))
	server.cfg.StatusFunc = func(context.Context) (*ipnstate.Status, error) {
		return &ipnstate.Status{Self: &ipnstate.PeerStatus{}}, nil
	}

	err := server.authorizeCaller(context.Background(), "100.64.0.1:1234", permissionRead)
	if err == nil || !strings.Contains(err.Error(), "catch tsnet node must be tagged") {
		t.Fatalf("authorizeCaller error = %v, want tagged-node denial", err)
	}
	if !errors.Is(err, errUnauthorized) {
		t.Fatalf("authorizeCaller error = %v, want errUnauthorized", err)
	}
}

func TestAuthorizeCallerReportsMissingPermission(t *testing.T) {
	server := newAuthzTestServer(t, newPermissionSet(permissionRead))

	err := server.authorizeCaller(context.Background(), "100.64.0.1:1234", permissionManage)
	if err == nil || !strings.Contains(err.Error(), `missing yeet permission "manage"`) {
		t.Fatalf("authorizeCaller error = %v, want missing manage", err)
	}
	if !strings.Contains(err.Error(), yeetAccessGrantsDocsURL) {
		t.Fatalf("authorizeCaller error = %v, want docs URL", err)
	}
}

func TestAuthorizeCallerRejectsMalformedGrant(t *testing.T) {
	server := newAuthzTestServer(t, newPermissionSet(permissionRead))
	server.cfg.WhoIsFunc = func(context.Context, string) (*apitype.WhoIsResponse, error) {
		return &apitype.WhoIsResponse{CapMap: tailcfg.PeerCapMap{
			yeetAppCapability: {tailcfg.RawMessage(`{"allow":"read"}`)},
		}}, nil
	}

	err := server.authorizeCaller(context.Background(), "100.64.0.1:1234", permissionRead)
	if err == nil || !strings.Contains(err.Error(), "invalid yeetrun.com/app/yeet app capability") {
		t.Fatalf("authorizeCaller error = %v, want malformed grant", err)
	}
	if !errors.Is(err, errUnauthorized) {
		t.Fatalf("authorizeCaller error = %v, want errUnauthorized", err)
	}
}

func TestAuthorizeCallerRejectsMissingWhoIs(t *testing.T) {
	server := newAuthzTestServer(t, newPermissionSet(permissionRead))
	server.cfg.WhoIsFunc = func(context.Context, string) (*apitype.WhoIsResponse, error) {
		return nil, nil
	}

	err := server.authorizeCaller(context.Background(), "100.64.0.1:1234", permissionRead)
	if err == nil || !strings.Contains(err.Error(), "missing Tailscale caller identity") {
		t.Fatalf("authorizeCaller error = %v, want missing identity", err)
	}
	if !errors.Is(err, errUnauthorized) {
		t.Fatalf("authorizeCaller error = %v, want errUnauthorized", err)
	}
}

func TestAuthorizeCallerAllowsGrantedPermissions(t *testing.T) {
	server := newAuthzTestServer(t, newPermissionSet(permissionRead, permissionManage, permissionSSH))

	err := server.authorizeCaller(context.Background(), "100.64.0.1:1234", permissionRead, permissionManage, permissionSSH)
	if err != nil {
		t.Fatalf("authorizeCaller with full grant: %v", err)
	}
}

func newAuthzTestServer(t *testing.T, perms permissionSet) *Server {
	t.Helper()
	server := newTestServer(t)
	server.cfg.AuthorizeFunc = nil
	server.cfg.StatusFunc = func(context.Context) (*ipnstate.Status, error) {
		return &ipnstate.Status{
			Self: &ipnstate.PeerStatus{
				Tags: ptr.To(views.SliceOf([]string{"tag:catch"})),
			},
		}, nil
	}
	server.cfg.WhoIsFunc = func(context.Context, string) (*apitype.WhoIsResponse, error) {
		return &apitype.WhoIsResponse{CapMap: capMapForAuthzTest(perms)}, nil
	}
	return server
}

func capMapForAuthzTest(perms permissionSet) tailcfg.PeerCapMap {
	var allow []string
	for _, perm := range knownYeetPermissions {
		if perms.has(perm) {
			allow = append(allow, string(perm))
		}
	}
	raw := `{"allow":[]}`
	if len(allow) > 0 {
		raw = `{"allow":["` + strings.Join(allow, `","`) + `"]}`
	}
	return tailcfg.PeerCapMap{
		yeetAppCapability: []tailcfg.RawMessage{tailcfg.RawMessage(raw)},
	}
}
