// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"fmt"
	"slices"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
)

const yeetAccessGrantsDocsURL = "https://yeetrun.com/docs/security/tailscale-access-grants"

const yeetAppCapability tailcfg.PeerCapability = "yeetrun.com/app/yeet"

type yeetPermission string

const (
	permissionRead   yeetPermission = "read"
	permissionManage yeetPermission = "manage"
	permissionSSH    yeetPermission = "ssh"
)

var knownYeetPermissions = []yeetPermission{
	permissionRead,
	permissionManage,
	permissionSSH,
}

type permissionSet map[yeetPermission]struct{}

type yeetAppCapabilityValue struct {
	Allow []string `json:"allow"`
}

type missingPermissionError struct {
	permission yeetPermission
}

func (e missingPermissionError) Error() string {
	return fmt.Sprintf(
		"missing yeet permission %q; update your Tailscale grant for %s:\n%s",
		e.permission,
		yeetAppCapability,
		yeetAccessGrantsDocsURL,
	)
}

func (e missingPermissionError) Unwrap() error {
	return errUnauthorized
}

func newPermissionSet(perms ...yeetPermission) permissionSet {
	out := make(permissionSet, len(perms))
	for _, perm := range perms {
		out[perm] = struct{}{}
	}
	return out
}

func (s permissionSet) has(perm yeetPermission) bool {
	_, ok := s[perm]
	return ok
}

func (s permissionSet) empty() bool {
	return len(s) == 0
}

func permissionsFromCapMap(caps tailcfg.PeerCapMap) (permissionSet, error) {
	values, err := tailcfg.UnmarshalCapJSON[yeetAppCapabilityValue](caps, yeetAppCapability)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid %s app capability: %v", errUnauthorized, yeetAppCapability, err)
	}
	out := make(permissionSet)
	for _, value := range values {
		for _, raw := range value.Allow {
			perm := yeetPermission(raw)
			if slices.Contains(knownYeetPermissions, perm) {
				out[perm] = struct{}{}
			}
		}
	}
	return out, nil
}

func requirePermissions(have permissionSet, required ...yeetPermission) error {
	for _, perm := range required {
		if !have.has(perm) {
			return missingPermissionError{permission: perm}
		}
	}
	return nil
}

func (s *Server) statusWithoutPeers(ctx context.Context) (*ipnstate.Status, error) {
	if s.cfg.StatusFunc != nil {
		return s.cfg.StatusFunc(ctx)
	}
	if s.cfg.LocalClient == nil {
		return nil, fmt.Errorf("%w: tailscale local client is not configured", errUnauthorized)
	}
	return s.cfg.LocalClient.StatusWithoutPeers(ctx)
}

func (s *Server) whoIs(ctx context.Context, remoteAddr string) (*apitype.WhoIsResponse, error) {
	if s.cfg.WhoIsFunc != nil {
		return s.cfg.WhoIsFunc(ctx, remoteAddr)
	}
	if s.cfg.LocalClient == nil {
		return nil, fmt.Errorf("%w: tailscale local client is not configured", errUnauthorized)
	}
	return s.cfg.LocalClient.WhoIs(ctx, remoteAddr)
}

func (s *Server) authorizeCaller(ctx context.Context, remoteAddr string, required ...yeetPermission) error {
	if s.cfg.AuthorizeFunc != nil {
		return s.cfg.AuthorizeFunc(ctx, remoteAddr)
	}
	if err := s.authorizeCatchNodeIdentity(ctx); err != nil {
		return err
	}
	if len(required) == 0 {
		return nil
	}
	perms, err := s.callerPermissions(ctx, remoteAddr)
	if err != nil {
		return err
	}
	return requirePermissions(perms, required...)
}

func (s *Server) authorizeCatchNodeIdentity(ctx context.Context) error {
	st, err := s.statusWithoutPeers(ctx)
	if err != nil {
		return fmt.Errorf("failed to get local client status: %v", err)
	}
	return validateCatchNodeIdentity(statusSelfTags(st))
}

func statusSelfTags(st *ipnstate.Status) []string {
	var selfTags []string
	if st != nil && st.Self != nil && st.Self.IsTagged() {
		selfTags = st.Self.Tags.AsSlice()
	}
	return selfTags
}

func (s *Server) callerPermissions(ctx context.Context, remoteAddr string) (permissionSet, error) {
	who, err := s.whoIs(ctx, remoteAddr)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to read Tailscale app grants for caller: %v", errUnauthorized, err)
	}
	if who == nil {
		return nil, fmt.Errorf("%w: missing Tailscale caller identity", errUnauthorized)
	}
	perms, err := permissionsFromCapMap(who.CapMap)
	if err != nil {
		return nil, err
	}
	return perms, nil
}

func rpcMethodPermissions(method string) (permissionSet, error) {
	switch method {
	case "catch.Info", "catch.ServiceInfo", "catch.ArtifactHashes", "catch.ZFSServiceRootCandidates", "catch.VMDefaults", "catch.ServicesList":
		return newPermissionSet(permissionRead), nil
	case catchrpc.RPCMethodHostStoragePlan, catchrpc.RPCMethodHostStorageApply:
		return newPermissionSet(permissionManage), nil
	case "catch.TailscaleSetup":
		return newPermissionSet(permissionRead, permissionManage, permissionSSH), nil
	default:
		return nil, fmt.Errorf("%w: unclassified RPC method %q", errUnauthorized, method)
	}
}

func (s *Server) authorizeRPCMethod(ctx context.Context, remoteAddr string, method string) error {
	required, err := rpcMethodPermissions(method)
	if err != nil {
		return err
	}
	return s.authorizePermissionSet(ctx, remoteAddr, required)
}

func (s *Server) authorizeExecRequest(ctx context.Context, remoteAddr string, req catchrpc.ExecRequest) error {
	required, err := execRequestPermissions(req)
	if err != nil {
		return fmt.Errorf("%w: %v", errUnauthorized, err)
	}
	return s.authorizePermissionSet(ctx, remoteAddr, required)
}

func (s *Server) authorizePermissionSet(ctx context.Context, remoteAddr string, required permissionSet) error {
	var ordered []yeetPermission
	for _, perm := range knownYeetPermissions {
		if required.has(perm) {
			ordered = append(ordered, perm)
		}
	}
	return s.authorizeCaller(ctx, remoteAddr, ordered...)
}
