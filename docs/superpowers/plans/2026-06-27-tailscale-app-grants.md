# Tailscale App Grants Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enforce yeet `read`, `manage`, and `ssh` permissions from Tailscale app grants at the catch boundary, with clear user-facing errors and docs.

**Architecture:** Add a small catch authorization layer that decodes `yeetrun.com/app/yeet` app capabilities from Tailscale `WhoIs`, maps each RPC/WebSocket/registry/TTY operation to required permissions, and fails closed. Keep clients as UX helpers only: normal RPC, WebSocket exec/events, web deploy, and docs surface catch's actionable missing-permission errors.

**Tech Stack:** Go, catch JSON-RPC/WebSocket handlers, Tailscale LocalAPI `WhoIs`, `tailcfg.PeerCapMap`, GitButler, MDX website docs.

---

## Current State Notes

- The active GitButler branch already contains the approved spec commit `78eb046a`.
- Root working tree currently shows `website` modified because the website submodule has pre-existing docs edits in:
  - `website/docs/concepts/tailscale.mdx`
  - `website/docs/faq.mdx`
  - `website/docs/getting-started/quick-start.mdx`
- Preserve those website edits. Do not reset, overwrite, or omit them when editing the website docs.

## File Structure

- Create `pkg/catch/authz.go`: yeet permission types, Tailscale cap decoding, missing-permission errors, server caller authorization helpers, and RPC/registry operation mapping.
- Create `pkg/catch/authz_test.go`: focused tests for cap decoding, permission union, missing/malformed cap denial, friendly error text, and caller authorization.
- Create `pkg/catch/tty_authz.go`: normal remote TTY command permission classification for service command execution over `/rpc/exec`.
- Create `pkg/catch/tty_authz_test.go`: table tests for command permission mapping and fail-closed unknown commands.
- Modify `pkg/catch/catch.go`: add testable `StatusFunc` and `WhoIsFunc` hooks to `Config`, and keep the old `AuthorizeFunc` override behavior for existing tests.
- Modify `pkg/catch/rpc.go`: move `/rpc` to method-level authorization, authorize `/rpc/events` as `read`, authorize `/rpc/exec` after reading `ExecRequest`, and write friendly auth errors to exec output before exit.
- Modify `pkg/catch/rpc_test.go` and `pkg/catch/rpc_events_test.go`: cover method-level auth, exec target auth, exec TTY command auth, and events auth.
- Modify `pkg/catch/registry.go` and `pkg/catch/registry_test.go`: require `manage` for tailnet `/v2/` access while preserving loopback read-only access.
- Modify `pkg/catchrpc/client.go` and `pkg/catchrpc/client_test.go`: preserve HTTP and WebSocket auth response bodies in client errors, especially for `/rpc/events`.
- Modify `pkg/yeet/run_web_api_test.go` or `pkg/yeet/run_web_job_test.go`: pin web deploy error propagation for missing-permission errors.
- Create `website/docs/security/tailscale-access-grants.mdx`: dedicated public docs page.
- Modify `website/docs/nav.json`: add a Security section or link for the dedicated page.
- Modify `website/docs/concepts/tailscale.mdx`, `website/docs/getting-started/quick-start.mdx`, and `website/docs/cli/yeet-cli.mdx`: link to the new page and preserve the existing local-Tailscale wording.
- Modify `website/docs/changelog.mdx` during release preparation, not during the first implementation pass unless the implementation is being released in the same session.

## Task 1: Add Permission Types and Cap Decoding

**Files:**
- Create: `pkg/catch/authz.go`
- Create: `pkg/catch/authz_test.go`

- [ ] **Step 1: Write failing cap decoding tests**

Add `pkg/catch/authz_test.go`:

```go
package catch

import (
	"context"
	"errors"
	"strings"
	"testing"

	"tailscale.com/tailcfg"
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
	perms := permissionSet{permissionManage: {}}
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
```

- [ ] **Step 2: Run tests and confirm they fail**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestPermissionsFromCapMap|TestMissingPermissionError|TestRequirePermissions|TestAuthorizeCallerUsesAuthorizeFuncOverride' -count=1
```

Expected: FAIL with undefined identifiers such as `permissionsFromCapMap`, `yeetAppCapability`, `permissionRead`, and `authorizeCaller`.

- [ ] **Step 3: Implement permission decoding and errors**

Add `pkg/catch/authz.go`:

```go
package catch

import (
	"context"
	"fmt"
	"slices"

	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
)

const yeetAccessGrantsDocsURL = "https://yeet.run/docs/security/tailscale-access-grants"

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
	st, err := s.statusWithoutPeers(ctx)
	if err != nil {
		return fmt.Errorf("failed to get local client status: %v", err)
	}
	var selfTags []string
	if st != nil && st.Self != nil && st.Self.IsTagged() {
		selfTags = st.Self.Tags.AsSlice()
	}
	if err := validateCatchNodeIdentity(selfTags); err != nil {
		return err
	}
	if len(required) == 0 {
		return nil
	}
	who, err := s.whoIs(ctx, remoteAddr)
	if err != nil {
		return fmt.Errorf("%w: failed to read Tailscale app grants for caller: %v", errUnauthorized, err)
	}
	perms, err := permissionsFromCapMap(who.CapMap)
	if err != nil {
		return err
	}
	return requirePermissions(perms, required...)
}
```

- [ ] **Step 4: Add test hooks to `Config`**

Modify `pkg/catch/catch.go` imports and `Config`:

```go
import (
	// existing imports

	"tailscale.com/client/local"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn/ipnstate"
)
```

Then add fields next to `LocalClient` and `AuthorizeFunc`:

```go
	LocalClient          *local.Client
	StatusFunc           func(ctx context.Context) (*ipnstate.Status, error)           `json:"-"`
	WhoIsFunc            func(ctx context.Context, remoteAddr string) (*apitype.WhoIsResponse, error) `json:"-"`
	AuthorizeFunc        func(ctx context.Context, remoteAddr string) error           `json:"-"`
```

Replace the old `verifyCaller` body with:

```go
// verifyCaller checks the base caller identity. Operation-specific callers
// should use authorizeCaller with required yeet permissions.
func (s *Server) verifyCaller(ctx context.Context, remoteAddr string) error {
	return s.authorizeCaller(ctx, remoteAddr)
}
```

- [ ] **Step 5: Run focused tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestPermissionsFromCapMap|TestMissingPermissionError|TestRequirePermissions|TestAuthorizeCallerUsesAuthorizeFuncOverride|TestServerVerifyCallerUsesAuthorizeFunc' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit Task 1**

Run `but diff`, select only `pkg/catch/authz.go`, `pkg/catch/authz_test.go`, and `pkg/catch/catch.go`, then commit those selected changes with message:

```bash
but commit codex/tailscale-app-grants-design -m "pkg/catch: decode yeet app grants"
```

Do not include the website submodule changes in this commit.

## Task 2: Classify TTY Command Permissions

**Files:**
- Create: `pkg/catch/tty_authz.go`
- Create: `pkg/catch/tty_authz_test.go`

- [ ] **Step 1: Write failing classifier tests**

Add `pkg/catch/tty_authz_test.go`:

```go
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
```

- [ ] **Step 2: Run tests and confirm they fail**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestExecRequestPermissions|TestTTYCommandPermissions' -count=1
```

Expected: FAIL with undefined `execRequestPermissions` and `ttyCommandPermissions`.

- [ ] **Step 3: Implement TTY permission classification**

Add `pkg/catch/tty_authz.go`:

```go
package catch

import (
	"fmt"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

func execRequestPermissions(req catchrpc.ExecRequest) (permissionSet, error) {
	switch req.Target {
	case catchrpc.ExecTargetHostShell, catchrpc.ExecTargetServiceShell:
		return newPermissionSet(permissionSSH), nil
	case catchrpc.ExecTargetServiceCommand:
		return ttyCommandPermissions(req.Args)
	default:
		return nil, fmt.Errorf("unclassified exec target %q", req.Target)
	}
}

func ttyCommandPermissions(args []string) (permissionSet, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("unclassified empty command")
	}
	switch args[0] {
	case "events", "ip", "logs", "status", "version":
		return newPermissionSet(permissionRead), nil
	case "docker":
		return dockerCommandPermissions(args[1:])
	case "snapshots":
		return snapshotsCommandPermissions(args[1:])
	case "service":
		return serviceCommandPermissions(args[1:])
	case "tailscale", "ts":
		return tailscaleCommandPermissions(args[1:])
	case "vm":
		return vmCommandPermissions(args[1:])
	case "cron", "disable", "edit", "enable", "mount", "umount", "env", "remove", "restart", "run", "copy", "stage", "start", "stop":
		return newPermissionSet(permissionManage), nil
	default:
		return nil, fmt.Errorf("unclassified command %q", args[0])
	}
}

func dockerCommandPermissions(args []string) (permissionSet, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("unclassified docker command")
	}
	switch args[0] {
	case "outdated":
		return newPermissionSet(permissionRead), nil
	case "pull", "update":
		return newPermissionSet(permissionManage), nil
	default:
		return nil, fmt.Errorf("unclassified docker command %q", args[0])
	}
}

func snapshotsCommandPermissions(args []string) (permissionSet, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("unclassified snapshots command")
	}
	if args[0] == "defaults" {
		if len(args) < 2 {
			return nil, fmt.Errorf("unclassified snapshots defaults command")
		}
		switch args[1] {
		case "show":
			return newPermissionSet(permissionRead), nil
		case "set":
			return newPermissionSet(permissionManage), nil
		default:
			return nil, fmt.Errorf("unclassified snapshots defaults command %q", args[1])
		}
	}
	switch args[0] {
	case "list", "inspect":
		return newPermissionSet(permissionRead), nil
	case "create", "clone", "restore", "rm", "protect", "unprotect":
		return newPermissionSet(permissionManage), nil
	default:
		return nil, fmt.Errorf("unclassified snapshots command %q", args[0])
	}
}

func serviceCommandPermissions(args []string) (permissionSet, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("unclassified service command")
	}
	switch args[0] {
	case "generations":
		return newPermissionSet(permissionRead), nil
	case "set", "rollback":
		return newPermissionSet(permissionManage), nil
	default:
		return nil, fmt.Errorf("unclassified service command %q", args[0])
	}
}

func tailscaleCommandPermissions(args []string) (permissionSet, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("unclassified tailscale command")
	}
	switch args[0] {
	case "status":
		return newPermissionSet(permissionRead), nil
	case "update", "serve", "set":
		return newPermissionSet(permissionManage), nil
	case "--":
		if len(args) > 1 && args[1] == "status" {
			return newPermissionSet(permissionRead), nil
		}
		return newPermissionSet(permissionManage), nil
	default:
		return newPermissionSet(permissionManage), nil
	}
}

func vmCommandPermissions(args []string) (permissionSet, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("unclassified vm command")
	}
	switch args[0] {
	case "images":
		return vmImagesCommandPermissions(args[1:])
	case "memory":
		if len(args) == 1 {
			return newPermissionSet(permissionRead), nil
		}
		return newPermissionSet(permissionManage), nil
	case "console", "set", "kernel":
		return newPermissionSet(permissionManage), nil
	default:
		return nil, fmt.Errorf("unclassified vm command %q", args[0])
	}
}

func vmImagesCommandPermissions(args []string) (permissionSet, error) {
	if len(args) == 0 {
		return newPermissionSet(permissionRead), nil
	}
	switch args[0] {
	case "ls", "catalog":
		return newPermissionSet(permissionRead), nil
	case "update", "import", "rm", "prune":
		return newPermissionSet(permissionManage), nil
	default:
		return nil, fmt.Errorf("unclassified vm images command %q", args[0])
	}
}
```

- [ ] **Step 4: Run focused tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestExecRequestPermissions|TestTTYCommandPermissions' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit Task 2**

Run `but diff`, select only `pkg/catch/tty_authz.go` and `pkg/catch/tty_authz_test.go`, then commit with message:

```bash
but commit codex/tailscale-app-grants-design -m "pkg/catch: classify command permissions"
```

Do not include the website submodule changes in this commit.

## Task 3: Enforce Permissions on RPC, Events, Exec, and Registry

**Files:**
- Modify: `pkg/catch/authz.go`
- Modify: `pkg/catch/rpc.go`
- Modify: `pkg/catch/registry.go`
- Modify: `pkg/catch/rpc_test.go`
- Modify: `pkg/catch/rpc_events_test.go`
- Modify: `pkg/catch/registry_test.go`

- [ ] **Step 1: Add failing operation authorization tests**

Append to `pkg/catch/rpc_test.go`:

```go
func TestRPCMethodAuthorization(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		have       permissionSet
		wantStatus int
		wantBody   string
	}{
		{name: "info requires read", method: "catch.Info", have: newPermissionSet(permissionRead), wantStatus: http.StatusOK},
		{name: "info denied without read", method: "catch.Info", have: newPermissionSet(permissionManage), wantStatus: http.StatusUnauthorized, wantBody: `missing yeet permission "read"`},
		{name: "tailscale setup requires manage", method: "catch.TailscaleSetup", have: newPermissionSet(permissionManage), wantStatus: http.StatusOK},
		{name: "tailscale setup denied without manage", method: "catch.TailscaleSetup", have: newPermissionSet(permissionRead), wantStatus: http.StatusUnauthorized, wantBody: `missing yeet permission "manage"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newPermissionTestServer(t, tt.have)
			ts := httptest.NewServer(server.RPCMux())
			defer ts.Close()

			req := catchrpc.Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: tt.method}
			if tt.method == "catch.TailscaleSetup" {
				params, err := json.Marshal(catchrpc.TailscaleSetupRequest{ClientSecret: "secret"})
				if err != nil {
					t.Fatalf("marshal params: %v", err)
				}
				req.Params = params
			}
			body, err := json.Marshal(req)
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			resp, err := http.Post(ts.URL+"/rpc", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatalf("post: %v", err)
			}
			defer resp.Body.Close()
			raw, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d body=%s, want %d", resp.StatusCode, raw, tt.wantStatus)
			}
			if tt.wantBody != "" && !strings.Contains(string(raw), tt.wantBody) {
				t.Fatalf("body = %q, want %q", raw, tt.wantBody)
			}
		})
	}
}

func TestRPCUnknownMethodFailsClosedBeforeDispatch(t *testing.T) {
	server := newPermissionTestServer(t, newPermissionSet(permissionRead, permissionManage, permissionSSH))
	ts := httptest.NewServer(server.RPCMux())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/rpc", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"catch.Future"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 401", resp.StatusCode, body)
	}
}
```

Append helper code to the same test file:

```go
func newPermissionTestServer(t *testing.T, perms permissionSet) *Server {
	t.Helper()
	server := newTestServer(t)
	server.cfg.AuthorizeFunc = nil
	server.cfg.StatusFunc = func(context.Context) (*ipnstate.Status, error) {
		return &ipnstate.Status{Self: &ipnstate.PeerStatus{Tags: ptr.To(views.SliceOf([]string{"tag:catch"}))}}, nil
	}
	server.cfg.WhoIsFunc = func(context.Context, string) (*apitype.WhoIsResponse, error) {
		return &apitype.WhoIsResponse{CapMap: capMapForPermissions(perms)}, nil
	}
	return server
}

func capMapForPermissions(perms permissionSet) tailcfg.PeerCapMap {
	var allow []string
	for _, perm := range knownYeetPermissions {
		if perms.has(perm) {
			allow = append(allow, string(perm))
		}
	}
	raw, err := json.Marshal(yeetAppCapabilityValue{Allow: allow})
	if err != nil {
		panic(err)
	}
	return tailcfg.PeerCapMap{
		yeetAppCapability: []tailcfg.RawMessage{tailcfg.RawMessage(raw)},
	}
}
```

Add these imports to `pkg/catch/rpc_test.go`:

```go
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/types/ptr"
	"tailscale.com/types/views"
```

- [ ] **Step 2: Add failing events, exec, and registry tests**

Append to `pkg/catch/rpc_events_test.go`:

```go
func TestRPCEventsRequiresReadPermission(t *testing.T) {
	server := newPermissionTestServer(t, newPermissionSet(permissionManage))
	ts := newTestHTTPServer(t, server)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/rpc/events"
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		t.Fatal("events dial succeeded without read permission")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("events status = %#v err=%v, want 401", resp, err)
	}
}
```

Add `net/http` to `pkg/catch/rpc_events_test.go` imports.

Append to `pkg/catch/rpc_test.go`:

```go
func TestRPCExecAuthorizesAfterReadingExecRequest(t *testing.T) {
	tests := []struct {
		name string
		req  catchrpc.ExecRequest
		have permissionSet
		want string
	}{
		{name: "host shell requires ssh", req: catchrpc.ExecRequest{Target: catchrpc.ExecTargetHostShell}, have: newPermissionSet(permissionRead, permissionManage), want: `missing yeet permission "ssh"`},
		{name: "service command requires mapped manage", req: catchrpc.ExecRequest{Service: "svc", Args: []string{"run", "ghcr.io/example/app:latest"}}, have: newPermissionSet(permissionRead), want: `missing yeet permission "manage"`},
		{name: "service command requires mapped read", req: catchrpc.ExecRequest{Service: "svc", Args: []string{"logs"}}, have: newPermissionSet(permissionManage), want: `missing yeet permission "read"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newPermissionTestServer(t, tt.have)
			ts := httptest.NewServer(server.RPCMux())
			defer ts.Close()

			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/rpc/exec", nil)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer conn.Close()
			if err := conn.WriteJSON(tt.req); err != nil {
				t.Fatalf("write request: %v", err)
			}
			var output bytes.Buffer
			deadline := time.Now().Add(2 * time.Second)
			_ = conn.SetReadDeadline(deadline)
			for {
				mt, data, err := conn.ReadMessage()
				if err != nil {
					t.Fatalf("read message: %v output=%q", err, output.String())
				}
				if mt == websocket.BinaryMessage {
					output.Write(data)
					continue
				}
				var msg catchrpc.ExecMessage
				if err := json.Unmarshal(data, &msg); err != nil {
					t.Fatalf("decode exec message: %v", err)
				}
				if msg.Type == catchrpc.ExecMsgExit {
					if msg.Code == 0 {
						t.Fatal("exec exit code = 0, want denied")
					}
					break
				}
			}
			if !strings.Contains(output.String(), tt.want) {
				t.Fatalf("output = %q, want %q", output.String(), tt.want)
			}
		})
	}
}
```

Append to `pkg/catch/registry_test.go`:

```go
func TestRegistryTailnetRequiresManagePermission(t *testing.T) {
	server := newPermissionTestServer(t, newPermissionSet(permissionRead))
	req := httptest.NewRequest(http.MethodHead, "http://example/v2/", nil)
	req.RemoteAddr = "100.64.0.2:1234"
	rr := httptest.NewRecorder()

	server.registry.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body=%s, want 401", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `missing yeet permission "manage"`) {
		t.Fatalf("body = %q, want missing manage", rr.Body.String())
	}
}
```

- [ ] **Step 3: Run tests and confirm they fail**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestRPCMethodAuthorization|TestRPCUnknownMethodFailsClosedBeforeDispatch|TestRPCEventsRequiresReadPermission|TestRPCExecAuthorizesAfterReadingExecRequest|TestRegistryTailnetRequiresManagePermission' -count=1
```

Expected: FAIL because RPC, events, exec, and registry still use broad `verifyCaller`.

- [ ] **Step 4: Add operation mapping helpers**

Append to `pkg/catch/authz.go`:

```go
func rpcMethodPermissions(method string) (permissionSet, error) {
	switch method {
	case "catch.Info", "catch.ServiceInfo", "catch.ArtifactHashes", "catch.ZFSServiceRootCandidates", "catch.VMDefaults", "catch.ServicesList":
		return newPermissionSet(permissionRead), nil
	case "catch.TailscaleSetup":
		return newPermissionSet(permissionManage), nil
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
```

Also add the import in `pkg/catch/authz.go`:

```go
	"github.com/yeetrun/yeet/pkg/catchrpc"
```

- [ ] **Step 5: Enforce RPC and events permissions**

Modify `pkg/catch/rpc.go` `RPCMux`:

```go
func (s *Server) RPCMux() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/rpc", http.HandlerFunc(s.handleRPC))
	mux.Handle("/rpc/exec", http.HandlerFunc(s.handleExecWS))
	mux.Handle("/rpc/events", s.authZ(permissionRead, http.HandlerFunc(s.handleEventsWS)))
	mux.Handle("/v2/", s.registry)
	return mux
}
```

Change `authZ` to take required permissions:

```go
func (s *Server) authZ(required yeetPermission, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := s.authorizeCaller(r.Context(), r.RemoteAddr, required); err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
```

In `handleRPC`, after validating `req.JSONRPC` and `req.Method`, add:

```go
	if err := s.authorizeRPCMethod(r.Context(), r.RemoteAddr, req.Method); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
```

Keep this before the notification check so notifications cannot bypass operation classification.

- [ ] **Step 6: Enforce exec permissions after reading `ExecRequest`**

In `pkg/catch/rpc.go`, add helper:

```go
func writeExecAuthError(conn *websocket.Conn, err error) {
	_ = conn.WriteMessage(websocket.BinaryMessage, []byte("Error: "+err.Error()+"\n"))
	writeExecExit(conn, 1, err.Error())
}
```

Then in `handleExecWS`, immediately after `readExecRequest` succeeds:

```go
	if err := s.authorizeExecRequest(r.Context(), r.RemoteAddr, req); err != nil {
		writeExecAuthError(conn, err)
		return
	}
```

- [ ] **Step 7: Require manage for tailnet registry**

Modify `pkg/catch/registry.go` non-loopback branch:

```go
	} else {
		if err := cr.s.authorizeCaller(r.Context(), r.RemoteAddr, permissionManage); err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
	}
```

Leave loopback read-only behavior unchanged.

- [ ] **Step 8: Run focused tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestRPCMethodAuthorization|TestRPCUnknownMethodFailsClosedBeforeDispatch|TestRPCEventsRequiresReadPermission|TestRPCExecAuthorizesAfterReadingExecRequest|TestRegistryLoopbackWriteRejected|TestRegistryTailnetRequiresManagePermission|TestRPCInfo|TestRPCServicesListDispatch|TestRPCEventsFilter' -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit Task 3**

Run `but diff`, select only `pkg/catch/authz.go`, `pkg/catch/rpc.go`, `pkg/catch/registry.go`, and the catch test files changed in this task, then commit with message:

```bash
but commit codex/tailscale-app-grants-design -m "pkg/catch: enforce operation permissions"
```

Do not include the website submodule changes in this commit.

## Task 4: Preserve Friendly Errors in Clients and Web Deploy

**Files:**
- Modify: `pkg/catchrpc/client.go`
- Modify: `pkg/catchrpc/client_test.go`
- Modify: `pkg/yeet/run_web_api_test.go` or `pkg/yeet/run_web_job_test.go`

- [ ] **Step 1: Add failing WebSocket dial body tests**

Append to `pkg/catchrpc/client_test.go`:

```go
func TestClientEventsDialErrorIncludesHTTPBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `missing yeet permission "read"; update your Tailscale grant`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	host, port := splitHostPort(t, srv.URL)
	err := NewClient(host, port).Events(context.Background(), EventsRequest{}, func(Event) {})
	if err == nil {
		t.Fatal("Events error = nil, want bad handshake")
	}
	if !strings.Contains(err.Error(), `missing yeet permission "read"`) {
		t.Fatalf("Events error = %v, want auth body", err)
	}
}

func TestClientExecDialErrorIncludesHTTPBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `missing yeet permission "ssh"; update your Tailscale grant`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	host, port := splitHostPort(t, srv.URL)
	_, err := NewClient(host, port).Exec(context.Background(), ExecRequest{Target: ExecTargetHostShell}, nil, nil, nil)
	if err == nil {
		t.Fatal("Exec error = nil, want bad handshake")
	}
	if !strings.Contains(err.Error(), `missing yeet permission "ssh"`) {
		t.Fatalf("Exec error = %v, want auth body", err)
	}
}
```

- [ ] **Step 2: Add a web deploy propagation test**

Append to `pkg/yeet/run_web_api_test.go`:

```go
func TestRunWebAPIDeploySurfacesPermissionErrorInJobOutput(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
		return errors.New(`missing yeet permission "manage"; update your Tailscale grant for yeetrun.com/app/yeet:
https://yeet.run/docs/security/tailscale-access-grants`)
	}

	root := t.TempDir()
	writeRunWebTestPayload(t, root)
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: root})
	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", RunDraft{Service: "svc-a", Host: "host-a", Payload: "run.sh"})
	jobID := decodeRunWebDeployStarted(t, rec).JobID
	waitRunWebJobState(t, s, jobID, runWebJobFailed)

	stream := runWebAPIRequest(t, s, http.MethodGet, "/api/deploy/"+jobID+"/stream", nil)
	output := decodeRunWebOutputText(t, parseRunWebSSE(t, stream.Body.String()))
	for _, want := range []string{`missing yeet permission "manage"`, "https://yeet.run/docs/security/tailscale-access-grants"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}
```

This test should already pass after current run-web behavior. Keep it as a regression pin.

- [ ] **Step 3: Run tests and confirm the catchrpc tests fail**

Run:

```bash
mise exec -- go test ./pkg/catchrpc -run 'TestClientEventsDialErrorIncludesHTTPBody|TestClientExecDialErrorIncludesHTTPBody' -count=1
```

Expected: FAIL because `Exec` and `Events` currently return `websocket: bad handshake` without the response body.

- [ ] **Step 4: Implement WebSocket dial error body preservation**

Modify `pkg/catchrpc/client.go` imports to include `strings`.

Add helper:

```go
func websocketDialError(err error, resp *http.Response) error {
	if err == nil {
		return nil
	}
	if resp == nil || resp.Body == nil {
		return err
	}
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return err
	}
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, msg)
}
```

Change `Exec` dial:

```go
	conn, resp, err := c.wsDialer.DialContext(ctx, c.wsURL+"/rpc/exec", nil)
	if err != nil {
		return 0, websocketDialError(err, resp)
	}
```

Change `Events` dial:

```go
	conn, resp, err := c.wsDialer.DialContext(ctx, c.wsURL+"/rpc/events", nil)
	if err != nil {
		return websocketDialError(err, resp)
	}
```

- [ ] **Step 5: Run focused tests**

Run:

```bash
mise exec -- go test ./pkg/catchrpc -run 'TestClientEventsDialErrorIncludesHTTPBody|TestClientExecDialErrorIncludesHTTPBody|TestClientEvents|TestClientEventsDialError|TestClientCall' -count=1
mise exec -- go test ./pkg/yeet -run 'TestRunWebAPIDeploySurfacesPermissionErrorInJobOutput|TestRunWebAPIDeployStreamMirrorsStderr' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit Task 4**

Run `but diff`, select only `pkg/catchrpc/client.go`, `pkg/catchrpc/client_test.go`, and the run-web test file changed in this task, then commit with message:

```bash
but commit codex/tailscale-app-grants-design -m "pkg/catchrpc: preserve auth response bodies"
```

Do not include the website submodule changes in this commit.

## Task 5: Document Tailscale Access Grants

**Files:**
- Create: `website/docs/security/tailscale-access-grants.mdx`
- Modify: `website/docs/nav.json`
- Modify: `website/docs/concepts/tailscale.mdx`
- Modify: `website/docs/getting-started/quick-start.mdx`
- Modify: `website/docs/cli/yeet-cli.mdx`

- [ ] **Step 1: Inspect and preserve existing website changes**

Run:

```bash
git -C website status --short --branch
git -C website diff -- docs/concepts/tailscale.mdx docs/faq.mdx docs/getting-started/quick-start.mdx
```

Expected: the three pre-existing modified files remain visible. Keep those edits and layer the grants docs on top.

- [ ] **Step 2: Add the dedicated security page**

Create `website/docs/security/tailscale-access-grants.mdx`:

```mdx
---
title: Tailscale Access Grants
description: Control who can read, manage, and open shell access through yeet.
---

Use Tailscale grants to decide who can use yeet against a catch host.
Catch enforces these permissions before it runs yeet operations.

## Start with one admin grant

For first setup, keep the policy simple. Give Tailscale admins all yeet
permissions:

```jsonc
{
  "grants": [
    {
      "src": ["autogroup:admin"],
      "dst": ["tag:catch"],
      "ip": ["tcp:41548"],
      "app": {
        "yeetrun.com/app/yeet": [
          { "allow": ["read", "manage", "ssh"] }
        ]
      }
    }
  ]
}
```

Run `yeet init` after this grant is active. First setup expects all three
permissions so the same admin can validate the host, deploy services, and use
catch-mediated shell access.

## Permissions

`read` allows observation. It covers status, info, service lists, logs, events,
VM defaults, artifact hashes, and VM SSH connection metadata.

`read` can expose service names, service state, event timing, logs, and service
details. Give it to people who should be allowed to inspect the host.

`manage` allows mutation. It covers deploys, updates, removes, `rm --clean`,
service config changes, snapshot and restore operations, VM lifecycle changes,
catch upgrades, Tailscale service changes, and registry push over the tailnet.

`ssh` allows catch-mediated host and service shell access through `yeet ssh`.
It does not control VM guest SSH. For `yeet ssh <vm>`, yeet needs `read` to
fetch VM connection metadata, then normal SSH keys authorize guest login.

## Split access later

After the first host works, you can split access by group:

```jsonc
{
  "grants": [
    {
      "src": ["group:yeet-readers"],
      "dst": ["tag:catch"],
      "ip": ["tcp:41548"],
      "app": {
        "yeetrun.com/app/yeet": [
          { "allow": ["read"] }
        ]
      }
    },
    {
      "src": ["group:yeet-deployers"],
      "dst": ["tag:catch"],
      "ip": ["tcp:41548"],
      "app": {
        "yeetrun.com/app/yeet": [
          { "allow": ["read", "manage"] }
        ]
      }
    },
    {
      "src": ["group:yeet-shell-admins"],
      "dst": ["tag:catch"],
      "ip": ["tcp:41548"],
      "app": {
        "yeetrun.com/app/yeet": [
          { "allow": ["read", "ssh"] }
        ]
      }
    }
  ]
}
```

Keep `read` with `manage` for operators who deploy services. Many deploy flows
need to inspect the existing service before changing it.

## Workstation reachability

The local `yeet` CLI does not embed tsnet. It connects to the catch hostname
over normal HTTP and WebSocket connections.

In the standard setup, install Tailscale on your workstation and connect it to
the same tailnet. The grant controls what yeet can do after it reaches catch.

## Registry access

The tailnet registry endpoint is a manage operation. Docker push uses several
read-shaped HTTP requests while uploading an image, so yeet treats the whole
tailnet registry surface as `manage`.

Catch still keeps its internal loopback registry reads for containerd and local
host operations.

## When access is denied

catch returns the missing permission in the error:

```text
missing yeet permission "manage"; update your Tailscale grant for yeetrun.com/app/yeet:
https://yeet.run/docs/security/tailscale-access-grants
```

Add the missing permission to the grant for the user or group that runs the
command, then retry the yeet command.

## Related pages

- [Tailscale Setup](/docs/concepts/tailscale)
- [Quick Start](/docs/getting-started/quick-start)
- [How Commands Work](/docs/cli/cli-overview)
```

- [ ] **Step 3: Add the page to nav**

Modify `website/docs/nav.json` by adding this folder after Daily Workflows and before Payload Guides:

```json
    {
      "type": "folder",
      "path": "/security",
      "title": "Security",
      "children": [
        { "type": "link", "path": "/tailscale-access-grants", "title": "Tailscale Access Grants" }
      ]
    },
```

Keep the JSON valid and preserve existing item order around the new section.

- [ ] **Step 4: Link from Tailscale setup**

In `website/docs/concepts/tailscale.mdx`, update the minimal policy snippet so the catch grant includes the `app` capability:

```jsonc
  "grants": [
    {
      "src": ["autogroup:admin"],
      "dst": ["tag:catch"],
      "ip": ["tcp:41548"],
      "app": {
        "yeetrun.com/app/yeet": [
          { "allow": ["read", "manage", "ssh"] }
        ]
      }
    },
    { "src": ["tag:catch"], "dst": ["tag:catch"], "ip": ["tcp:41548"] }
  ]
```

Add this sentence immediately after the snippet:

```md
See [Tailscale Access Grants](/docs/security/tailscale-access-grants) for the yeet permissions attached to that grant.
```

- [ ] **Step 5: Link from Quick Start**

In `website/docs/getting-started/quick-start.mdx`, under "Prepare Tailscale access", replace the sentence that points only to Tailscale setup with:

```md
For the minimal policy snippet, see
[Tailscale Setup](/docs/concepts/tailscale#first-time-host-setup). For the
`read`, `manage`, and `ssh` permissions in that snippet, see
[Tailscale Access Grants](/docs/security/tailscale-access-grants).
```

- [ ] **Step 6: Link from CLI reference**

In `website/docs/cli/yeet-cli.mdx`, find the `yeet ssh`, `yeet run`, or access/setup section. Add this note near the first relevant command group:

```md
When catch denies a command with `missing yeet permission`, update the matching
Tailscale grant. See
[Tailscale Access Grants](/docs/security/tailscale-access-grants).
```

- [ ] **Step 7: Run docs checks**

Run:

```bash
git -C website diff --check
npm --prefix website run generate:content-audit
```

Expected: both commands pass.

- [ ] **Step 8: Commit website docs inside the submodule**

Run:

```bash
git -C website status --short --branch
git -C website diff --stat
git -C website add docs/security/tailscale-access-grants.mdx docs/nav.json docs/concepts/tailscale.mdx docs/getting-started/quick-start.mdx docs/cli/yeet-cli.mdx docs/faq.mdx
git -C website commit -m "docs: explain tailscale access grants"
git -C website push origin main
```

Include `docs/faq.mdx` to preserve and publish the pre-existing local-Tailscale correction. Do not stage unrelated website files.

- [ ] **Step 9: Commit root website gitlink**

From `/Users/shayne/code/yeet`, run:

```bash
git diff --submodule=log -- website
but diff
```

Verify the root diff shows only the intended website gitlink update. Commit the selected gitlink change with message:

```bash
but commit codex/tailscale-app-grants-design -m "docs: publish access grants manual"
```

## Task 6: Run Broad Verification

**Files:**
- No planned source edits unless verification finds a defect.

- [ ] **Step 1: Run package tests touched by the feature**

Run:

```bash
mise exec -- go test ./pkg/catch ./pkg/catchrpc ./pkg/yeet -count=1
```

Expected: PASS.

- [ ] **Step 2: Run full Go tests**

Run:

```bash
mise exec -- go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 3: Run full pre-commit**

Run:

```bash
mise exec -- pre-commit run --all-files
```

Expected: PASS.

- [ ] **Step 4: Run quality gate**

Run:

```bash
mise run quality
```

Expected: PASS.

- [ ] **Step 5: Optional live smoke after ACLs are updated**

Only run this after the operator has updated the Tailscale grants on the tailnet.

```bash
CATCH_HOST=yeet-cloud mise exec -- go run ./cmd/yeet version
CATCH_HOST=yeet-cloud mise exec -- go run ./cmd/yeet status
CATCH_HOST=yeet-cloud mise exec -- go run ./cmd/yeet ssh -- whoami
```

Expected: all commands succeed for a caller with `read`, `manage`, and `ssh`.

- [ ] **Step 6: Commit verification fixes if any**

If verification required code or docs fixes, run `but diff`, select only those files, and commit with a focused message. If no fixes were needed, do not create a commit.

## Task 7: Prepare Release Notes When Releasing

**Files:**
- Modify during release: `website/docs/changelog.mdx`

- [ ] **Step 1: Inspect the release commit range**

Before writing changelog text, run:

```bash
git fetch --tags origin
git describe --tags --abbrev=0
git log --oneline "$(git describe --tags --abbrev=0)"..HEAD
```

Expected: the range contains this access-grants change and any other release-bound commits.

- [ ] **Step 2: Add a user-facing changelog entry**

Add a date-first entry to `website/docs/changelog.mdx` with a version heading chosen by the release process. Use this bullet shape:

```md
- catch now requires Tailscale app grants for yeet access. Add the documented
  `yeetrun.com/app/yeet` grant with `read`, `manage`, and `ssh` before
  upgrading catch.
```

Keep the latest entry standalone and avoid internal implementation details.

- [ ] **Step 3: Run changelog checks**

Run:

```bash
git -C website diff --check
npm --prefix website run generate:content-audit
```

Expected: PASS.

- [ ] **Step 4: Commit release docs through the normal release flow**

Use the repo release process and GitButler workflow. Commit inside `website/`, push the website repo, then commit the root website gitlink only after confirming:

```bash
git -C website status --short --branch
git diff --submodule=log -- website
```

Expected: website is clean and pushed; root gitlink shows exactly the intended website commit.
