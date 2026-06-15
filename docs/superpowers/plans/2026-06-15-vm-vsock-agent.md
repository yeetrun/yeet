# VM Vsock Agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an observe-only `yeet-agent` guest service over Firecracker vsock so `yeet ip <vm>` and `yeet ssh <vm>` query current guest network state live.

**Architecture:** catch renders a Firecracker `vsock` device for new VMs and stores its runtime socket metadata. A new Rust `guest/yeet-agent` binary listens on vsock port `7788` and returns newline-delimited JSON responses for `hello`, `ping`, and `network_state`. catch prefers live agent network state for VM IP and SSH host selection, and keeps existing journal and neighbor discovery only as compatibility fallbacks for old VMs.

**Tech Stack:** Go 1.26 via `mise exec -- go`, Rust stable via `mise exec -- cargo`, Firecracker JSON config, Linux AF_VSOCK via `libc`, newline-delimited JSON, existing `pkg/db`, `pkg/catch`, and `yeet-vm-images` build scripts.

---

## Scope Check

This plan spans two repositories because the feature requires host-side support in `/Users/shayne/code/yeet` and guest-image support in `/Users/shayne/code/yeet-vm-images`. The work is still one deployable feature: new VMs need both the Firecracker vsock device and an image that contains `yeet-agent`.

V1 intentionally does not add guest command execution, file copy, package management, persistent observed IP caches, or IPv6 routing decisions.

## File Structure

### `/Users/shayne/code/yeet`

- Modify `.mise.toml`: add `guest:agent:test` and `guest:agent:build` tasks.
- Create `guest/yeet-agent/Cargo.toml`: Rust crate manifest for the guest agent.
- Create `guest/yeet-agent/Cargo.lock`: locked Rust dependencies for reproducible agent builds.
- Create `guest/yeet-agent/src/main.rs`: thin binary entrypoint.
- Create `guest/yeet-agent/src/lib.rs`: protocol types, network-state collection, vsock listener, and request handler.
- Modify `pkg/db/db.go`: add vsock fields to `VMSocketConfig`.
- Modify `pkg/db/migrate.go`: bump `CurrentDataVersion` from `9` to `10` and add a no-op migrator from `9`.
- Regenerate `pkg/db/db_view.go` and `pkg/db/db_clone.go`: generated accessors for the new DB fields.
- Modify `pkg/db/db_view_test.go`: cover JSON round trip for `VMSocketConfig`.
- Modify `pkg/catch/vm_firecracker.go`: add the Firecracker `vsock` object.
- Modify `pkg/catch/vm_firecracker_test.go`: cover vsock JSON rendering.
- Modify `pkg/catch/vm_provision.go`: assign vsock socket path and guest CID during VM provision.
- Modify `pkg/catch/vm_provision_test.go`: cover persisted vsock metadata and rendered Firecracker JSON.
- Create `pkg/catch/vm_agent.go`: host-side vsock protocol client, response validation, and network-state normalization.
- Create `pkg/catch/vm_agent_test.go`: fake Firecracker vsock server and protocol/error tests.
- Modify `pkg/catch/service_info.go`: prefer live agent IPs for VM info, `yeet ip`, and VM SSH host selection.
- Modify `pkg/catch/vm_lan_test.go`: cover agent-preferred live IP behavior and fail-closed behavior.
- Modify `pkg/catchrpc/types.go`: add optional source fields for JSON diagnostics.
- Modify `pkg/catch/service_info_test.go`: verify JSON-facing source fields where service info is rendered.

### `/Users/shayne/code/yeet-vm-images`

- Modify `scripts/build-linux-kernel.sh`: require virtio-vsock guest kernel support.
- Modify `scripts/build-ubuntu-26.04.sh`: require `YEET_VM_AGENT_PATH`, install `yeet-agent`, and enable its systemd unit.
- Modify `flake.nix`: build `yeet-agent` and include it in the NixOS rootfs.
- Modify `nixos/yeet-vm.nix`: define and enable the `yeet-agent` systemd service.
- Modify `scripts/verify-nixos-26.05.sh`: verify `yeet-agent` and vsock support.
- Modify `README.md`: document the guest agent contract and local build commands.

## Task 1: Add DB and Firecracker Vsock Metadata

**Files:**
- Modify: `/Users/shayne/code/yeet/pkg/db/db.go`
- Modify: `/Users/shayne/code/yeet/pkg/db/migrate.go`
- Modify: `/Users/shayne/code/yeet/pkg/db/db_view_test.go`
- Regenerate: `/Users/shayne/code/yeet/pkg/db/db_view.go`
- Regenerate: `/Users/shayne/code/yeet/pkg/db/db_clone.go`
- Modify: `/Users/shayne/code/yeet/pkg/catch/vm_firecracker.go`
- Modify: `/Users/shayne/code/yeet/pkg/catch/vm_firecracker_test.go`

- [ ] **Step 1: Write the failing DB view test**

Add this case to `TestViewJSONRoundTripAndInitializationRules` in `/Users/shayne/code/yeet/pkg/db/db_view_test.go`:

```go
{
	name:    "vm socket config",
	newView: func() jsonView { return &VMSocketConfigView{} },
	validView: func() jsonView {
		v := (&VMSocketConfig{
			APISocketPath:    "/run/devbox/firecracker.sock",
			VsockSocketPath: "/run/devbox/vsock.sock",
			VsockGuestCID:   3,
		}).View()
		return &v
	},
	json: `{"APISocketPath":"/run/devbox/firecracker.sock","VsockSocketPath":"/run/devbox/vsock.sock","VsockGuestCID":3}`,
},
```

- [ ] **Step 2: Run the DB test to verify it fails**

Run:

```bash
mise exec -- go test ./pkg/db -run TestViewJSONRoundTripAndInitializationRules -count=1
```

Expected: FAIL because `VMSocketConfig` has no `VsockSocketPath` or `VsockGuestCID` fields.

- [ ] **Step 3: Add vsock fields and migrate version**

In `/Users/shayne/code/yeet/pkg/db/db.go`, change `VMSocketConfig` to:

```go
type VMSocketConfig struct {
	APISocketPath   string
	VsockSocketPath string `json:",omitempty"`
	VsockGuestCID   uint32 `json:",omitempty"`
}
```

In `/Users/shayne/code/yeet/pkg/db/migrate.go`, change:

```go
const CurrentDataVersion = 10
```

and add the version `9` migrator:

```go
var migrators = map[int]func(*Data) error{ // Start DataVersion -> NextStep
	3: reinit,
	4: addDockerEndpoints,
	5: addServiceRoot,
	6: addServiceRootZFS,
	7: addSnapshotPolicy,
	8: addVMServiceConfig,
	9: addVMVsockConfig,
}
```

Add this no-op migrator near `addVMServiceConfig`:

```go
func addVMVsockConfig(d *Data) error {
	return nil
}
```

- [ ] **Step 4: Regenerate DB clone and view helpers**

Run:

```bash
mise exec -- go generate ./pkg/db
```

Expected: `pkg/db/db_view.go` gains `VsockSocketPath()` and `VsockGuestCID()` accessors, and `pkg/db/db_clone.go` regenerates cleanly.

- [ ] **Step 5: Verify DB tests pass**

Run:

```bash
mise exec -- go test ./pkg/db -count=1
```

Expected: PASS.

- [ ] **Step 6: Write the failing Firecracker vsock config test**

Add this test to `/Users/shayne/code/yeet/pkg/catch/vm_firecracker_test.go`:

```go
func TestRenderFirecrackerConfigIncludesVsock(t *testing.T) {
	raw, err := renderFirecrackerConfig(firecrackerConfig{
		BootSource: firecrackerBootSource{
			KernelImagePath: "/srv/images/vmlinux",
			BootArgs:        "console=ttyS0",
		},
		Drives: []firecrackerDrive{{
			DriveID:      "rootfs",
			PathOnHost:   "/srv/vms/devbox/rootfs.raw",
			IsRootDevice: true,
		}},
		MachineConfig: firecrackerMachineConfig{VCPUCount: 2, MemSizeMib: 2048},
		Vsock: &firecrackerVsock{
			VsockID:  "agent",
			GuestCID: 3,
			UDSPath:  "/srv/vms/devbox/run/vsock.sock",
		},
	})
	if err != nil {
		t.Fatalf("renderFirecrackerConfig: %v", err)
	}
	text := string(raw)
	for _, want := range []string{
		`"vsock"`,
		`"vsock_id": "agent"`,
		`"guest_cid": 3`,
		`"uds_path": "/srv/vms/devbox/run/vsock.sock"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("config missing %q:\n%s", want, text)
		}
	}
}
```

- [ ] **Step 7: Run the Firecracker config test to verify it fails**

Run:

```bash
mise exec -- go test ./pkg/catch -run TestRenderFirecrackerConfigIncludesVsock -count=1
```

Expected: FAIL because `firecrackerConfig` has no `Vsock` field and `firecrackerVsock` does not exist.

- [ ] **Step 8: Add the Firecracker vsock config type**

In `/Users/shayne/code/yeet/pkg/catch/vm_firecracker.go`, change `firecrackerConfig` to:

```go
type firecrackerConfig struct {
	BootSource        firecrackerBootSource         `json:"boot-source"`
	Drives            []firecrackerDrive            `json:"drives"`
	NetworkInterfaces []firecrackerNetworkInterface `json:"network-interfaces"`
	MachineConfig     firecrackerMachineConfig      `json:"machine-config"`
	Vsock             *firecrackerVsock             `json:"vsock,omitempty"`
}
```

Add the vsock type near the other Firecracker config types:

```go
type firecrackerVsock struct {
	VsockID  string `json:"vsock_id"`
	GuestCID uint32 `json:"guest_cid"`
	UDSPath  string `json:"uds_path"`
}
```

- [ ] **Step 9: Verify Task 1 passes**

Run:

```bash
mise exec -- go test ./pkg/db ./pkg/catch -run 'TestViewJSONRoundTripAndInitializationRules|TestRenderFirecrackerConfigIncludesVsock' -count=1
```

Expected: PASS.

- [ ] **Step 10: Commit Task 1**

Run:

```bash
git add pkg/db/db.go pkg/db/migrate.go pkg/db/db_view.go pkg/db/db_clone.go pkg/db/db_view_test.go pkg/catch/vm_firecracker.go pkg/catch/vm_firecracker_test.go
git commit -m "vm: add vsock config metadata"
```

If this workspace is still on `gitbutler/workspace`, commit the same listed files with GitButler and keep unrelated dirty files out of the commit.

## Task 2: Persist Vsock Runtime Metadata During VM Provision

**Files:**
- Modify: `/Users/shayne/code/yeet/pkg/catch/vm_provision.go`
- Modify: `/Users/shayne/code/yeet/pkg/catch/vm_provision_test.go`

- [ ] **Step 1: Write the failing provision test**

Add this test to `/Users/shayne/code/yeet/pkg/catch/vm_provision_test.go`:

```go
func TestRunVMConfiguresVsockRuntimeMetadata(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "devbox")

	if err := execer.runVM(cli.RunFlags{Net: "svc", Restart: false}, vmUbuntu2604Payload); err != nil {
		t.Fatalf("runVM: %v", err)
	}

	svc := getTestService(t, server, "devbox")
	if svc.VM == nil {
		t.Fatal("VM missing after run")
	}
	if svc.VM.Sockets.VsockSocketPath == "" {
		t.Fatalf("vsock socket path is empty: %#v", svc.VM.Sockets)
	}
	if !strings.HasSuffix(svc.VM.Sockets.VsockSocketPath, "/run/vsock.sock") {
		t.Fatalf("vsock socket path = %q, want run/vsock.sock suffix", svc.VM.Sockets.VsockSocketPath)
	}
	if svc.VM.Sockets.VsockGuestCID != vmAgentGuestCID {
		t.Fatalf("vsock guest CID = %d, want %d", svc.VM.Sockets.VsockGuestCID, vmAgentGuestCID)
	}

	raw, err := os.ReadFile(filepath.Join(serviceRunDirForRoot(svc.ServiceRoot), "firecracker.json"))
	if err != nil {
		t.Fatalf("read firecracker config: %v", err)
	}
	for _, want := range []string{
		`"vsock"`,
		`"vsock_id": "yeet-agent"`,
		`"guest_cid": 3`,
		`"uds_path": "` + svc.VM.Sockets.VsockSocketPath + `"`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("firecracker config missing %q:\n%s", want, string(raw))
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run:

```bash
mise exec -- go test ./pkg/catch -run TestRunVMConfiguresVsockRuntimeMetadata -count=1
```

Expected: FAIL because `vmAgentGuestCID` is undefined and VM provisioning does not set vsock fields.

- [ ] **Step 3: Add VM agent constants**

Create `/Users/shayne/code/yeet/pkg/catch/vm_agent.go` with this initial content:

```go
package catch

const (
	vmAgentProtocolVersion = 1
	vmAgentPort            = 7788
	vmAgentGuestCID        = 3
	vmAgentVsockID         = "yeet-agent"
)
```

- [ ] **Step 4: Wire vsock into the provision plan**

In `/Users/shayne/code/yeet/pkg/catch/vm_provision.go`, in `newVMProvisionPlan`, add:

```go
vsockSocket := filepath.Join(runDir, "vsock.sock")
```

near the existing `apiSocket` and `unitName` locals.

When building `firecrackerConfig`, add:

```go
		Vsock: &firecrackerVsock{
			VsockID:  vmAgentVsockID,
			GuestCID: vmAgentGuestCID,
			UDSPath:  vsockSocket,
		},
```

When returning `vmProvisionPlan`, keep the existing fields and add:

```go
		VsockSocket: vsockSocket,
```

Add `VsockSocket string` to the `vmProvisionPlan` struct near `APISocket`.

In `commitVMProvision`, change the sockets assignment to:

```go
			Sockets: db.VMSocketConfig{
				APISocketPath:   plan.APISocket,
				VsockSocketPath: plan.VsockSocket,
				VsockGuestCID:   vmAgentGuestCID,
			},
```

- [ ] **Step 5: Verify Task 2 passes**

Run:

```bash
mise exec -- go test ./pkg/catch -run TestRunVMConfiguresVsockRuntimeMetadata -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit Task 2**

Run:

```bash
git add pkg/catch/vm_agent.go pkg/catch/vm_provision.go pkg/catch/vm_provision_test.go
git commit -m "vm: persist agent vsock metadata"
```

If using GitButler, commit the same listed files only.

## Task 3: Implement Host-Side Agent Protocol Client

**Files:**
- Modify: `/Users/shayne/code/yeet/pkg/catch/vm_agent.go`
- Create: `/Users/shayne/code/yeet/pkg/catch/vm_agent_test.go`

- [ ] **Step 1: Write the fake Firecracker vsock test server**

Create `/Users/shayne/code/yeet/pkg/catch/vm_agent_test.go` with this content:

```go
package catch

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func startFakeVsockAgent(t *testing.T, response string) string {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "vsock.sock")
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	t.Cleanup(func() {
		_ = ln.Close()
		_ = os.Remove(socketPath)
	})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		if strings.TrimSpace(line) != "CONNECT 7788" {
			return
		}
		_, _ = conn.Write([]byte("OK 1024\n"))
		var req vmAgentRequest
		if err := json.NewDecoder(r).Decode(&req); err != nil {
			return
		}
		_, _ = conn.Write([]byte(response + "\n"))
	}()
	return socketPath
}
```

- [ ] **Step 2: Add the passing-shape protocol tests**

Append these tests to `/Users/shayne/code/yeet/pkg/catch/vm_agent_test.go`:

```go
func TestQueryVMNetworkStateUsesVsockConnectAndJSONProtocol(t *testing.T) {
	socketPath := startFakeVsockAgent(t, `{"protocol":1,"type":"network_state","request_id":"test","interfaces":[{"name":"eth0","mac":"02:fc:00:00:00:12","up":true,"ips":["10.0.4.183"]}]}`)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	got, err := queryVMNetworkState(ctx, socketPath)
	if err != nil {
		t.Fatalf("queryVMNetworkState: %v", err)
	}
	if len(got.Interfaces) != 1 {
		t.Fatalf("interfaces = %#v, want one", got.Interfaces)
	}
	if got.Interfaces[0].Name != "eth0" || got.Interfaces[0].IPs[0] != "10.0.4.183" {
		t.Fatalf("network state = %#v, want eth0 10.0.4.183", got)
	}
}

func TestQueryVMNetworkStateRejectsProtocolMismatch(t *testing.T) {
	socketPath := startFakeVsockAgent(t, `{"protocol":2,"type":"network_state","request_id":"test","interfaces":[]}`)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := queryVMNetworkState(ctx, socketPath)
	if err == nil || !strings.Contains(err.Error(), "protocol version") {
		t.Fatalf("queryVMNetworkState error = %v, want protocol version error", err)
	}
}

func TestQueryVMNetworkStateRejectsNoUsableAddresses(t *testing.T) {
	socketPath := startFakeVsockAgent(t, `{"protocol":1,"type":"network_state","request_id":"test","interfaces":[{"name":"lo","up":true,"ips":["127.0.0.1"]}]}`)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := queryVMNetworkState(ctx, socketPath)
	if err == nil || !strings.Contains(err.Error(), "no usable addresses") {
		t.Fatalf("queryVMNetworkState error = %v, want no usable addresses", err)
	}
}
```

- [ ] **Step 3: Run the protocol tests to verify they fail**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestQueryVMNetworkState' -count=1
```

Expected: FAIL because `vmAgentRequest`, `queryVMNetworkState`, and response types are not implemented.

- [ ] **Step 4: Implement the host-side agent client**

Replace `/Users/shayne/code/yeet/pkg/catch/vm_agent.go` with:

```go
package catch

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"
)

const (
	vmAgentProtocolVersion = 1
	vmAgentPort            = 7788
	vmAgentGuestCID        = 3
	vmAgentVsockID         = "yeet-agent"
	vmAgentRequestID       = "test"
	vmAgentQueryTimeout    = 1500 * time.Millisecond
)

type vmAgentRequest struct {
	Protocol  int    `json:"protocol"`
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
}

type vmAgentError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

type vmAgentResponse struct {
	Protocol   int                `json:"protocol"`
	Type       string             `json:"type"`
	RequestID  string             `json:"request_id"`
	Interfaces []vmAgentInterface `json:"interfaces,omitempty"`
	Error      *vmAgentError      `json:"error,omitempty"`
}

type vmAgentInterface struct {
	Name string   `json:"name"`
	MAC  string   `json:"mac,omitempty"`
	Up   bool     `json:"up"`
	IPs  []string `json:"ips,omitempty"`
}

type vmAgentNetworkState struct {
	Interfaces []vmAgentInterface
}

func queryVMNetworkState(ctx context.Context, socketPath string) (vmAgentNetworkState, error) {
	socketPath = strings.TrimSpace(socketPath)
	if socketPath == "" {
		return vmAgentNetworkState{}, fmt.Errorf("VM agent vsock socket path is empty")
	}
	queryCtx, cancel := context.WithTimeout(ctx, vmAgentQueryTimeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(queryCtx, "unix", socketPath)
	if err != nil {
		return vmAgentNetworkState{}, fmt.Errorf("connect VM agent vsock: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(vmAgentQueryTimeout))

	r := bufio.NewReader(conn)
	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", vmAgentPort); err != nil {
		return vmAgentNetworkState{}, fmt.Errorf("send VM agent connect: %w", err)
	}
	ack, err := r.ReadString('\n')
	if err != nil {
		return vmAgentNetworkState{}, fmt.Errorf("read VM agent connect ack: %w", err)
	}
	if !strings.HasPrefix(ack, "OK ") {
		return vmAgentNetworkState{}, fmt.Errorf("VM agent connect rejected: %s", strings.TrimSpace(ack))
	}
	req := vmAgentRequest{
		Protocol:  vmAgentProtocolVersion,
		Type:      "network_state",
		RequestID: vmAgentRequestID,
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return vmAgentNetworkState{}, fmt.Errorf("send VM agent request: %w", err)
	}
	var resp vmAgentResponse
	if err := json.NewDecoder(r).Decode(&resp); err != nil {
		return vmAgentNetworkState{}, fmt.Errorf("read VM agent response: %w", err)
	}
	if err := validateVMAgentNetworkStateResponse(resp, req); err != nil {
		return vmAgentNetworkState{}, err
	}
	return vmAgentNetworkState{Interfaces: usableVMAgentInterfaces(resp.Interfaces)}, nil
}

func validateVMAgentNetworkStateResponse(resp vmAgentResponse, req vmAgentRequest) error {
	if resp.Protocol != vmAgentProtocolVersion {
		return fmt.Errorf("VM agent protocol version = %d, want %d", resp.Protocol, vmAgentProtocolVersion)
	}
	if resp.Type != req.Type {
		return fmt.Errorf("VM agent response type = %q, want %q", resp.Type, req.Type)
	}
	if resp.RequestID != req.RequestID {
		return fmt.Errorf("VM agent response request_id = %q, want %q", resp.RequestID, req.RequestID)
	}
	if resp.Error != nil {
		return fmt.Errorf("VM agent error %s: %s", resp.Error.Code, resp.Error.Message)
	}
	if len(usableVMAgentInterfaces(resp.Interfaces)) == 0 {
		return fmt.Errorf("VM agent returned no usable addresses")
	}
	return nil
}

func usableVMAgentInterfaces(in []vmAgentInterface) []vmAgentInterface {
	out := make([]vmAgentInterface, 0, len(in))
	for _, iface := range in {
		if strings.TrimSpace(iface.Name) == "" || strings.TrimSpace(iface.Name) == "lo" || !iface.Up {
			continue
		}
		usableIPs := make([]string, 0, len(iface.IPs))
		for _, raw := range iface.IPs {
			ip, err := netip.ParseAddr(strings.TrimSpace(raw))
			if err != nil || !ip.Is4() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			usableIPs = append(usableIPs, ip.String())
		}
		if len(usableIPs) == 0 {
			continue
		}
		iface.IPs = usableIPs
		out = append(out, iface)
	}
	return out
}

func vmAgentNetworkStateIPs(state vmAgentNetworkState) map[string]string {
	out := map[string]string{}
	for _, iface := range state.Interfaces {
		if len(iface.IPs) == 0 {
			continue
		}
		out[strings.TrimSpace(iface.Name)] = iface.IPs[0]
	}
	return out
}
```

- [ ] **Step 5: Verify Task 3 passes**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestQueryVMNetworkState|TestRunVMConfiguresVsockRuntimeMetadata' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit Task 3**

Run:

```bash
git add pkg/catch/vm_agent.go pkg/catch/vm_agent_test.go
git commit -m "vm: add agent protocol client"
```

If using GitButler, commit the same listed files only.

## Task 4: Prefer Live Agent Network State For VM IP and SSH

**Files:**
- Modify: `/Users/shayne/code/yeet/pkg/catch/service_info.go`
- Modify: `/Users/shayne/code/yeet/pkg/catch/vm_lan_test.go`
- Modify: `/Users/shayne/code/yeet/pkg/catchrpc/types.go`
- Modify: `/Users/shayne/code/yeet/pkg/catch/service_info_test.go`

- [ ] **Step 1: Write tests for agent-preferred LAN IPs**

Add this variable near the existing test stubs in `/Users/shayne/code/yeet/pkg/catch/vm_lan_test.go`:

```go
func stubVMNetworkState(t *testing.T, state vmAgentNetworkState, err error) {
	t.Helper()
	old := queryVMNetworkStateFn
	queryVMNetworkStateFn = func(context.Context, string) (vmAgentNetworkState, error) {
		return state, err
	}
	t.Cleanup(func() { queryVMNetworkStateFn = old })
}
```

Add this test:

```go
func TestServiceInfoPrefersLiveAgentLANIP(t *testing.T) {
	oldDiscover := discoverVMLANIPsFn
	defer func() { discoverVMLANIPsFn = oldDiscover }()
	discoverVMLANIPsFn = func(string, db.VMConfigView) map[string]string {
		return map[string]string{"eth0": "10.0.4.50"}
	}
	stubVMNetworkState(t, vmAgentNetworkState{Interfaces: []vmAgentInterface{{
		Name: "eth0",
		MAC:  "46:85:dc:3a:06:34",
		Up:   true,
		IPs:  []string{"10.0.4.183"},
	}}}, nil)

	server := newTestServer(t)
	seedLANVMService(t, server)
	if _, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, svc *db.Service) error {
		svc.VM.Sockets.VsockSocketPath = "/run/devbox/vsock.sock"
		svc.VM.Sockets.VsockGuestCID = vmAgentGuestCID
		return nil
	}); err != nil {
		t.Fatalf("MutateService: %v", err)
	}

	resp, err := server.serviceInfo("devbox")
	if err != nil {
		t.Fatalf("serviceInfo: %v", err)
	}
	if resp.Info.VM == nil || resp.Info.VM.SSH == nil || resp.Info.VM.SSH.Host != "10.0.4.183" {
		t.Fatalf("VM SSH = %#v, want live agent host 10.0.4.183", resp.Info.VM)
	}
	if len(resp.Info.Network.IPs) != 1 || resp.Info.Network.IPs[0].IP != "10.0.4.183" || resp.Info.Network.IPs[0].Source != "agent" {
		t.Fatalf("network IPs = %#v, want agent LAN IP", resp.Info.Network.IPs)
	}
}
```

Add this test for fail-closed `yeet ip` behavior when no source is trustworthy:

```go
func TestIPCmdFuncErrorsWhenVMHasNoTrustedIP(t *testing.T) {
	oldDiscover := discoverVMLANIPsFn
	defer func() { discoverVMLANIPsFn = oldDiscover }()
	discoverVMLANIPsFn = func(string, db.VMConfigView) map[string]string { return nil }
	stubVMNetworkState(t, vmAgentNetworkState{}, errors.New("agent unavailable"))

	server := newTestServer(t)
	seedLANVMService(t, server)
	out := &bytes.Buffer{}
	execer := &ttyExecer{s: server, sn: "devbox", rw: out}

	err := execer.ipCmdFunc()
	if err == nil || !strings.Contains(err.Error(), "no current IP known") {
		t.Fatalf("ipCmdFunc error = %v, want no current IP known", err)
	}
	if out.String() != "" {
		t.Fatalf("ip output = %q, want empty on error", out.String())
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestServiceInfoPrefersLiveAgentLANIP|TestIPCmdFuncErrorsWhenVMHasNoTrustedIP' -count=1
```

Expected: FAIL because `queryVMNetworkStateFn` and `ServiceIP.Source` are not implemented, and `ipCmdFunc` still succeeds with empty output.

- [ ] **Step 3: Add source fields to RPC types**

In `/Users/shayne/code/yeet/pkg/catchrpc/types.go`, change `ServiceVMNetwork` and `ServiceIP` to:

```go
type ServiceVMNetwork struct {
	Mode      string `json:"mode,omitempty"`
	Interface string `json:"interface,omitempty"`
	IP        string `json:"ip,omitempty"`
	Source    string `json:"source,omitempty"`
	MAC       string `json:"mac,omitempty"`
}
```

```go
type ServiceIP struct {
	Label     string `json:"label,omitempty"`
	IP        string `json:"ip,omitempty"`
	Interface string `json:"interface,omitempty"`
	Source    string `json:"source,omitempty"`
}
```

- [ ] **Step 4: Add agent query hook and source-aware VM IP selection**

In `/Users/shayne/code/yeet/pkg/catch/service_info.go`, add a package variable near `discoverVMLANIPsFn`:

```go
var queryVMNetworkStateFn = queryVMNetworkState
```

Add this local type:

```go
type vmDiscoveredIP struct {
	IP     string
	Source string
}
```

Replace `discoverVMLANIPs(service string, vm db.VMConfigView) map[string]string` with a source-aware version:

```go
func discoverVMNetworkIPs(ctx context.Context, service string, vm db.VMConfigView) map[string]vmDiscoveredIP {
	out := map[string]vmDiscoveredIP{}
	sockets := vm.Sockets()
	if socketPath := strings.TrimSpace(sockets.VsockSocketPath()); socketPath != "" {
		if state, err := queryVMNetworkStateFn(ctx, socketPath); err == nil {
			for iface, ip := range vmAgentNetworkStateIPs(state) {
				out[iface] = vmDiscoveredIP{IP: ip, Source: "agent"}
			}
		}
	}
	for iface, ip := range discoverVMLANIPsFn(service, vm) {
		if _, ok := out[iface]; ok {
			continue
		}
		out[iface] = vmDiscoveredIP{IP: ip, Source: "legacy"}
	}
	return out
}
```

Keep `discoverVMLANIPs` as the legacy journal/neighbor helper so old tests and fallback code remain focused.

Update VM service info builders so they call `discoverVMNetworkIPs(context.Background(), sn, vm)` and pass `map[string]vmDiscoveredIP` into `vmSSHHostFromNetworks`, `serviceVMNetworkInfo`, and `serviceVMNetworkIPs`.

Implement source-aware IP selection:

```go
func serviceVMNetworkIP(network db.VMNetworkConfig, discovered map[string]vmDiscoveredIP) vmDiscoveredIP {
	if network.IP.IsValid() {
		return vmDiscoveredIP{IP: network.IP.String(), Source: "config"}
	}
	return discovered[strings.TrimSpace(network.Interface)]
}
```

When filling `catchrpc.ServiceVMNetwork` and `catchrpc.ServiceIP`, set `Source` from the returned `vmDiscoveredIP`.

- [ ] **Step 5: Make `yeet ip` fail closed when no VM IP is known**

Find `ipCmdFunc` in `/Users/shayne/code/yeet/pkg/catch/service_info.go` or the current file that owns it. After collecting VM IPs, return this error when the VM exists and the resulting IP list is empty:

```go
if sv.ServiceType() == db.ServiceTypeVM && len(ips) == 0 {
	return fmt.Errorf("VM %q has no current IP known; live agent unavailable and legacy discovery found no address", e.sn)
}
```

Use the actual local variable names in that function. The behavior must be covered by `TestIPCmdFuncErrorsWhenVMHasNoTrustedIP`.

- [ ] **Step 6: Verify Task 4 targeted tests pass**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestServiceInfoPrefersLiveAgentLANIP|TestIPCmdFuncErrorsWhenVMHasNoTrustedIP|TestServiceInfoIncludesDiscoveredLANIPForVMSSH|TestIPCmdFuncPrintsVMNetworkIPs' -count=1
```

Expected: PASS.

- [ ] **Step 7: Verify RPC type changes do not break service info tests**

Run:

```bash
mise exec -- go test ./pkg/catch ./pkg/catchrpc ./pkg/yeet -run 'Test.*SSH|Test.*ServiceInfo|Test.*IP' -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit Task 4**

Run:

```bash
git add pkg/catch/service_info.go pkg/catch/vm_lan_test.go pkg/catchrpc/types.go pkg/catch/service_info_test.go
git commit -m "vm: prefer live agent network state"
```

If using GitButler, commit the same listed files only.

## Task 5: Add the Rust `yeet-agent` Crate

**Files:**
- Modify: `/Users/shayne/code/yeet/.mise.toml`
- Create: `/Users/shayne/code/yeet/guest/yeet-agent/Cargo.toml`
- Create: `/Users/shayne/code/yeet/guest/yeet-agent/Cargo.lock`
- Create: `/Users/shayne/code/yeet/guest/yeet-agent/src/lib.rs`
- Create: `/Users/shayne/code/yeet/guest/yeet-agent/src/main.rs`

- [ ] **Step 1: Add the agent Rust tasks**

In `/Users/shayne/code/yeet/.mise.toml`, add:

```toml
[tasks."guest:agent:test"]
description = "Run yeet-agent Rust tests"
run = '''
#!/usr/bin/env bash
set -euo pipefail

if [[ "$(uname -s)" == "Darwin" ]] && command -v xcrun >/dev/null 2>&1; then
  case "$(rustc -vV | awk '/^host:/ {print $2}')" in
    aarch64-apple-darwin)
      export CARGO_TARGET_AARCH64_APPLE_DARWIN_LINKER="$(xcrun --find cc)"
      ;;
    x86_64-apple-darwin)
      export CARGO_TARGET_X86_64_APPLE_DARWIN_LINKER="$(xcrun --find cc)"
      ;;
  esac
fi

cargo test --locked --manifest-path guest/yeet-agent/Cargo.toml
'''

[tasks."guest:agent:build"]
description = "Build static yeet-agent for the VM image"
run = '''
#!/usr/bin/env bash
set -euo pipefail

rustup target add x86_64-unknown-linux-musl
export CARGO_TARGET_X86_64_UNKNOWN_LINUX_MUSL_LINKER="${CARGO_TARGET_X86_64_UNKNOWN_LINUX_MUSL_LINKER:-rust-lld}"
cargo build --locked --manifest-path guest/yeet-agent/Cargo.toml --release --target x86_64-unknown-linux-musl

binary=guest/yeet-agent/target/x86_64-unknown-linux-musl/release/yeet-agent
file_output=$(file "$binary")
echo "$file_output"
if [[ "$file_output" != *"ELF 64-bit"* || "$file_output" != *"x86-64"* ]]; then
  echo "expected x86-64 ELF binary: $file_output" >&2
  exit 1
fi
if [[ "$file_output" == *"dynamically linked"* ]]; then
  echo "expected static binary without dynamic interpreter: $file_output" >&2
  exit 1
fi
'''
```

- [ ] **Step 2: Create the Rust manifest**

Create `/Users/shayne/code/yeet/guest/yeet-agent/Cargo.toml`:

```toml
[package]
name = "yeet-agent"
version = "0.1.0"
edition = "2024"
license = "BSD-2-Clause"

[dependencies]
libc = "0.2"
serde = { version = "1", features = ["derive"] }
serde_json = "1"
```

- [ ] **Step 3: Generate the agent lockfile**

Run:

```bash
mise exec -- cargo generate-lockfile --manifest-path guest/yeet-agent/Cargo.toml
```

Expected: `guest/yeet-agent/Cargo.lock` is created.

- [ ] **Step 4: Write protocol and address-filter tests**

Create `/Users/shayne/code/yeet/guest/yeet-agent/src/lib.rs` with these tests and minimal type declarations:

```rust
use serde::{Deserialize, Serialize};
use std::io::{self, BufRead, Write};

pub const PROTOCOL_VERSION: u32 = 1;
pub const AGENT_PORT: u32 = 7788;

#[derive(Debug, Deserialize)]
pub struct AgentRequest {
    pub protocol: u32,
    #[serde(rename = "type")]
    pub request_type: String,
    pub request_id: String,
}

#[derive(Debug, Serialize)]
pub struct AgentResponse {
    pub protocol: u32,
    #[serde(rename = "type")]
    pub response_type: String,
    pub request_id: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub interfaces: Vec<AgentInterface>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<AgentError>,
}

#[derive(Debug, Serialize)]
pub struct AgentError {
    pub code: String,
    pub message: String,
}

#[derive(Debug, Clone, Eq, PartialEq, Serialize)]
pub struct AgentInterface {
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub mac: String,
    pub up: bool,
    pub ips: Vec<String>,
}

pub fn usable_interfaces(input: &[AgentInterface]) -> Vec<AgentInterface> {
    input
        .iter()
        .filter(|iface| iface.up && iface.name != "lo")
        .filter_map(|iface| {
            let ips: Vec<String> = iface
                .ips
                .iter()
                .filter(|ip| ip.as_str() != "127.0.0.1" && !ip.starts_with("169.254."))
                .cloned()
                .collect();
            if ips.is_empty() {
                None
            } else {
                let mut out = iface.clone();
                out.ips = ips;
                Some(out)
            }
        })
        .collect()
}

pub fn handle_one_request<R: BufRead, W: Write, F>(
    mut reader: R,
    mut writer: W,
    mut network_state: F,
) -> io::Result<()>
where
    F: FnMut() -> io::Result<Vec<AgentInterface>>,
{
    let mut line = String::new();
    reader.read_line(&mut line)?;
    let req: AgentRequest = serde_json::from_str(&line)
        .map_err(|err| io::Error::new(io::ErrorKind::InvalidData, err))?;
    let resp = if req.protocol != PROTOCOL_VERSION {
        AgentResponse {
            protocol: PROTOCOL_VERSION,
            response_type: req.request_type,
            request_id: req.request_id,
            interfaces: Vec::new(),
            error: Some(AgentError {
                code: "protocol_mismatch".to_string(),
                message: "unsupported protocol version".to_string(),
            }),
        }
    } else if req.request_type == "network_state" {
        AgentResponse {
            protocol: PROTOCOL_VERSION,
            response_type: "network_state".to_string(),
            request_id: req.request_id,
            interfaces: usable_interfaces(&network_state()?),
            error: None,
        }
    } else if req.request_type == "ping" || req.request_type == "hello" {
        AgentResponse {
            protocol: PROTOCOL_VERSION,
            response_type: req.request_type,
            request_id: req.request_id,
            interfaces: Vec::new(),
            error: None,
        }
    } else {
        AgentResponse {
            protocol: PROTOCOL_VERSION,
            response_type: req.request_type,
            request_id: req.request_id,
            interfaces: Vec::new(),
            error: Some(AgentError {
                code: "unknown_request".to_string(),
                message: "unknown request type".to_string(),
            }),
        }
    };
    serde_json::to_writer(&mut writer, &resp)?;
    writer.write_all(b"\n")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn filters_loopback_and_link_local_addresses() {
        let got = usable_interfaces(&[
            AgentInterface { name: "lo".to_string(), mac: String::new(), up: true, ips: vec!["127.0.0.1".to_string()] },
            AgentInterface { name: "eth0".to_string(), mac: "02:fc:00:00:00:12".to_string(), up: true, ips: vec!["169.254.1.1".to_string(), "10.0.4.183".to_string()] },
            AgentInterface { name: "eth1".to_string(), mac: String::new(), up: false, ips: vec!["192.168.1.2".to_string()] },
        ]);
        assert_eq!(got.len(), 1);
        assert_eq!(got[0].name, "eth0");
        assert_eq!(got[0].ips, vec!["10.0.4.183"]);
    }

    #[test]
    fn handles_network_state_request() {
        let mut out = Vec::new();
        handle_one_request(
            br#"{"protocol":1,"type":"network_state","request_id":"r1"}
"#.as_slice(),
            &mut out,
            || Ok(vec![AgentInterface { name: "eth0".to_string(), mac: "02:fc:00:00:00:12".to_string(), up: true, ips: vec!["10.0.4.183".to_string()] }]),
        ).expect("handle request");
        let text = String::from_utf8(out).expect("utf8");
        assert!(text.contains(r#""type":"network_state""#), "{text}");
        assert!(text.contains(r#""request_id":"r1""#), "{text}");
        assert!(text.contains("10.0.4.183"), "{text}");
    }

    #[test]
    fn rejects_unknown_request_type() {
        let mut out = Vec::new();
        handle_one_request(
            br#"{"protocol":1,"type":"exec","request_id":"r1"}
"#.as_slice(),
            &mut out,
            || Ok(Vec::new()),
        ).expect("handle request");
        let text = String::from_utf8(out).expect("utf8");
        assert!(text.contains("unknown_request"), "{text}");
    }
}
```

- [ ] **Step 5: Run the Rust tests**

Run:

```bash
mise exec -- cargo test --locked --manifest-path guest/yeet-agent/Cargo.toml
```

Expected: PASS for the initial protocol and filtering tests.

- [ ] **Step 6: Implement Linux network collection and vsock listener**

Append this implementation to `/Users/shayne/code/yeet/guest/yeet-agent/src/lib.rs` below the existing request handler:

```rust
use std::fs;
use std::io::{BufReader, BufWriter};
use std::{fs::File, mem};
use std::net::Ipv4Addr;
use std::os::fd::{FromRawFd, RawFd};
use std::process::Command;

#[derive(Debug, Deserialize)]
struct IpAddrInterface {
    ifname: String,
    #[serde(default)]
    address: String,
    #[serde(default)]
    operstate: String,
    #[serde(default)]
    addr_info: Vec<IpAddrInfo>,
}

#[derive(Debug, Deserialize)]
struct IpAddrInfo {
    family: String,
    local: String,
}

pub fn collect_network_state() -> io::Result<Vec<AgentInterface>> {
    let output = Command::new("/usr/bin/ip")
        .args(["-j", "-4", "addr", "show"])
        .output()
        .or_else(|_| Command::new("/run/current-system/sw/bin/ip").args(["-j", "-4", "addr", "show"]).output())
        .or_else(|_| Command::new("/sbin/ip").args(["-j", "-4", "addr", "show"]).output())?;
    if !output.status.success() {
        return Err(io::Error::new(io::ErrorKind::Other, "ip command failed"));
    }
    parse_ip_json(&output.stdout)
}

pub fn parse_ip_json(raw: &[u8]) -> io::Result<Vec<AgentInterface>> {
    let parsed: Vec<IpAddrInterface> = serde_json::from_slice(raw)
        .map_err(|err| io::Error::new(io::ErrorKind::InvalidData, err))?;
    let mut out = Vec::new();
    for iface in parsed {
        let mut ips = Vec::new();
        for addr in iface.addr_info {
            if addr.family != "inet" {
                continue;
            }
            if addr.local.parse::<Ipv4Addr>().is_ok() {
                ips.push(addr.local);
            }
        }
        let mac = if iface.address.contains(':') { iface.address } else { read_mac(&iface.ifname).unwrap_or_default() };
        out.push(AgentInterface {
            name: iface.ifname,
            mac,
            up: iface.operstate == "UP" || !ips.is_empty(),
            ips,
        });
    }
    Ok(usable_interfaces(&out))
}

fn read_mac(name: &str) -> io::Result<String> {
    let raw = fs::read_to_string(format!("/sys/class/net/{name}/address"))?;
    Ok(raw.trim().to_string())
}

pub fn serve_vsock_forever(port: u32) -> io::Result<()> {
    let fd = listen_vsock(port)?;
    loop {
        let conn_fd = unsafe { libc::accept(fd, std::ptr::null_mut(), std::ptr::null_mut()) };
        if conn_fd < 0 {
            continue;
        }
        let _ = handle_stream_fd(conn_fd);
    }
}

fn handle_stream_fd(fd: RawFd) -> io::Result<()> {
    let stream = unsafe { File::from_raw_fd(fd) };
    let reader = BufReader::new(stream.try_clone()?);
    let writer = BufWriter::new(stream);
    handle_one_request(reader, writer, collect_network_state)
}

#[cfg(target_os = "linux")]
fn listen_vsock(port: u32) -> io::Result<RawFd> {
    let fd = unsafe { libc::socket(libc::AF_VSOCK, libc::SOCK_STREAM | libc::SOCK_CLOEXEC, 0) };
    if fd < 0 {
        return Err(io::Error::last_os_error());
    }
    let addr = libc::sockaddr_vm {
        svm_family: libc::AF_VSOCK as libc::sa_family_t,
        svm_reserved1: 0,
        svm_port: port,
        svm_cid: libc::VMADDR_CID_ANY,
        svm_zero: [0; 4],
    };
    let rc = unsafe {
        libc::bind(
            fd,
            &addr as *const libc::sockaddr_vm as *const libc::sockaddr,
            mem::size_of::<libc::sockaddr_vm>() as libc::socklen_t,
        )
    };
    if rc < 0 {
        let err = io::Error::last_os_error();
        unsafe { libc::close(fd) };
        return Err(err);
    }
    if unsafe { libc::listen(fd, 16) } < 0 {
        let err = io::Error::last_os_error();
        unsafe { libc::close(fd) };
        return Err(err);
    }
    Ok(fd)
}

#[cfg(not(target_os = "linux"))]
fn listen_vsock(_port: u32) -> io::Result<RawFd> {
    Err(io::Error::other("vsock requires Linux"))
}
```

Add this test to the existing Rust tests:

```rust
#[test]
fn parses_ip_json_output() {
    let raw = br#"[
      {"ifname":"lo","operstate":"UNKNOWN","addr_info":[{"family":"inet","local":"127.0.0.1"}]},
      {"ifname":"eth0","address":"02:fc:00:00:00:12","operstate":"UP","addr_info":[{"family":"inet","local":"10.0.4.183"}]}
    ]"#;
    let got = parse_ip_json(raw).expect("parse");
    assert_eq!(got.len(), 1);
    assert_eq!(got[0].name, "eth0");
    assert_eq!(got[0].mac, "02:fc:00:00:00:12");
    assert_eq!(got[0].ips, vec!["10.0.4.183"]);
}
```

- [ ] **Step 7: Add the binary entrypoint**

Create `/Users/shayne/code/yeet/guest/yeet-agent/src/main.rs`:

```rust
fn main() {
    if let Err(err) = yeet_agent::serve_vsock_forever(yeet_agent::AGENT_PORT) {
        eprintln!("yeet-agent: {err}");
        std::process::exit(1);
    }
}
```

- [ ] **Step 8: Verify Rust tests and static build**

Run:

```bash
mise exec -- cargo test --locked --manifest-path guest/yeet-agent/Cargo.toml
mise run guest:agent:build
```

Expected: tests PASS and `file` reports a static x86-64 ELF binary.

- [ ] **Step 9: Commit Task 5**

Run:

```bash
git add .mise.toml guest/yeet-agent/Cargo.toml guest/yeet-agent/Cargo.lock guest/yeet-agent/src/lib.rs guest/yeet-agent/src/main.rs
git commit -m "guest: add yeet agent"
```

If using GitButler, commit the same listed files only.

## Task 6: Install `yeet-agent` In Official VM Images

**Files:**
- Modify: `/Users/shayne/code/yeet-vm-images/flake.nix`
- Modify: `/Users/shayne/code/yeet-vm-images/nixos/yeet-vm.nix`
- Modify: `/Users/shayne/code/yeet-vm-images/scripts/build-ubuntu-26.04.sh`
- Modify: `/Users/shayne/code/yeet-vm-images/scripts/build-linux-kernel.sh`
- Modify: `/Users/shayne/code/yeet-vm-images/scripts/verify-nixos-26.05.sh`
- Modify: `/Users/shayne/code/yeet-vm-images/README.md`

- [ ] **Step 1: Update the kernel builder verification**

In `/Users/shayne/code/yeet-vm-images/scripts/build-linux-kernel.sh`, add these required configs beside the other `require_config` calls:

```bash
require_config CONFIG_VSOCKETS y
require_config CONFIG_VIRTIO_VSOCKETS y
```

If the script has a `scripts/config` enable list, add:

```bash
--enable VSOCKETS \
--enable VIRTIO_VSOCKETS \
```

- [ ] **Step 2: Update Ubuntu image build inputs and installation**

In `/Users/shayne/code/yeet-vm-images/scripts/build-ubuntu-26.04.sh`, add:

```bash
guest_agent_path="${YEET_VM_AGENT_PATH:-}"
```

near `guest_init_path`.

In the fast-profile input checks, add:

```bash
if [ -z "$guest_agent_path" ]; then
	echo "YEET_VM_AGENT_PATH is required for the fast profile" >&2
	exit 1
fi
if [ ! -x "$guest_agent_path" ]; then
	echo "YEET_VM_AGENT_PATH is not executable: $guest_agent_path" >&2
	exit 1
fi
```

When installing guest files into the mounted rootfs, add:

```bash
install -m 0755 "$guest_agent_path" "$rootfs_mount/usr/local/lib/yeet-vm/yeet-agent"
cat > "$rootfs_mount/etc/systemd/system/yeet-agent.service" <<'UNIT'
[Unit]
Description=yeet VM guest agent
After=network.target
Wants=network.target

[Service]
Type=simple
ExecStart=/usr/local/lib/yeet-vm/yeet-agent
Restart=always
RestartSec=1

[Install]
WantedBy=multi-user.target
UNIT
ln -sf ../yeet-agent.service "$rootfs_mount/etc/systemd/system/multi-user.target.wants/yeet-agent.service"
```

When writing `manifest.json`, include:

```json
"guest_agent": "/usr/local/lib/yeet-vm/yeet-agent"
```

next to `guest_init`.

- [ ] **Step 3: Update NixOS image build**

In `/Users/shayne/code/yeet-vm-images/flake.nix`, add a `yeetAgent` package next to `yeetInit`:

```nix
yeetAgent = pkgs.rustPlatform.buildRustPackage {
  pname = "yeet-agent";
  version = "0.1.0";
  src = "${yeet}/guest/yeet-agent";
  cargoLock.lockFile = "${yeet}/guest/yeet-agent/Cargo.lock";
  doCheck = false;
};
```

Add `yeetAgent` to `storePaths` and symlink it in `populateImageCommands`:

```nix
yeetAgent
```

```bash
ln -s ${yeetAgent}/bin/yeet-agent ./files/usr/local/lib/yeet-vm/yeet-agent
```

Expose the package:

```nix
yeet-agent = yeetAgent;
```

- [ ] **Step 4: Enable the NixOS systemd unit**

In `/Users/shayne/code/yeet-vm-images/nixos/yeet-vm.nix`, add this service under `systemd.services`:

```nix
yeet-agent = {
  description = "yeet VM guest agent";
  wantedBy = [ "multi-user.target" ];
  after = [ "network.target" ];
  wants = [ "network.target" ];
  serviceConfig = {
    Type = "simple";
    ExecStart = "/usr/local/lib/yeet-vm/yeet-agent";
    Restart = "always";
    RestartSec = 1;
  };
};
```

- [ ] **Step 5: Update NixOS verifier**

In `/Users/shayne/code/yeet-vm-images/scripts/verify-nixos-26.05.sh`, add `yeet-agent` to the systemd unit assertions and add a path check for `/usr/local/lib/yeet-vm/yeet-agent`.

Use this check shape:

```bash
systemd_units="$(nix_eval_json "systemd.units" | jq -r 'keys[]')"
printf '%s\n' "$systemd_units" | grep -qx 'yeet-agent.service'
```

If the verifier already has a unit loop, add `"yeet-agent"` to that loop instead of duplicating logic.

- [ ] **Step 6: Update image README build commands**

In `/Users/shayne/code/yeet-vm-images/README.md`, update local Ubuntu build instructions to include:

```bash
cd ../yeet
mise run guest:init:build
mise run guest:agent:build
cd ../yeet-vm-images
sudo YEET_VM_KERNEL_PATH="$PWD/dist/kernel-linux-7.0/vmlinux" \
  YEET_VM_KERNEL_VERSION=linux-7.0-yeet \
  YEET_VM_INIT_PATH="$PWD/../yeet/guest/yeet-init/target/x86_64-unknown-linux-musl/release/yeet-init" \
  YEET_VM_AGENT_PATH="$PWD/../yeet/guest/yeet-agent/target/x86_64-unknown-linux-musl/release/yeet-agent" \
  scripts/build-ubuntu-26.04.sh
```

Add a short contract bullet:

```markdown
- installs `/usr/local/lib/yeet-vm/yeet-agent` and enables `yeet-agent.service`
  so catch can query current guest network state over Firecracker vsock.
```

- [ ] **Step 7: Verify image-repo tests**

Run from `/Users/shayne/code/yeet-vm-images`:

```bash
mise run lint
scripts/verify-nixos-26.05.sh
```

Expected: PASS. If `verify-nixos-26.05.sh` requires Linux-only Nix features not available on this machine, record the exact failure and run the NixOS verification on a Linux builder before publishing an image.

- [ ] **Step 8: Commit Task 6 in the image repo**

Run from `/Users/shayne/code/yeet-vm-images`:

```bash
git add flake.nix nixos/yeet-vm.nix scripts/build-ubuntu-26.04.sh scripts/build-linux-kernel.sh scripts/verify-nixos-26.05.sh README.md
git commit -m "images: install yeet guest agent"
```

If using GitButler in the image repo, commit the same listed files only.

## Task 7: Run Cross-Package Verification In `yeet`

**Files:**
- No source edits unless tests reveal a defect from earlier tasks.

- [ ] **Step 1: Format Go and Rust code**

Run from `/Users/shayne/code/yeet`:

```bash
mise exec -- gofmt -w pkg/db/db.go pkg/db/migrate.go pkg/catch/vm_firecracker.go pkg/catch/vm_firecracker_test.go pkg/catch/vm_provision.go pkg/catch/vm_provision_test.go pkg/catch/vm_agent.go pkg/catch/vm_agent_test.go pkg/catch/service_info.go pkg/catch/vm_lan_test.go pkg/catchrpc/types.go pkg/catch/service_info_test.go
mise exec -- cargo fmt --manifest-path guest/yeet-agent/Cargo.toml
```

Expected: no output from `gofmt`; Rust files are formatted.

- [ ] **Step 2: Run focused Go tests**

Run:

```bash
mise exec -- go test ./pkg/db ./pkg/catch ./pkg/catchrpc ./pkg/yeet -count=1
```

Expected: PASS.

- [ ] **Step 3: Run Rust guest tests**

Run:

```bash
mise run guest:init:test
mise run guest:agent:test
```

Expected: PASS.

- [ ] **Step 4: Build guest binaries**

Run:

```bash
mise run guest:init:build
mise run guest:agent:build
```

Expected: both binaries are static x86-64 ELF outputs.

- [ ] **Step 5: Run the full Go suite**

Run:

```bash
mise exec -- go test ./...
```

Expected: PASS.

- [ ] **Step 6: Record verification state**

Run:

```bash
git status --short
```

Expected: no unexpected files are present. If an earlier task required a verification fix, return to that task's commit step and commit the exact file set listed there.

## Task 8: Deploy Catch and Run lab-host Live Smoke Test

**Files:**
- No source edits unless live testing reveals a defect.

- [ ] **Step 1: Install updated catch correctly on lab-host**

Run from `/Users/shayne/code/yeet`:

```bash
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet init root@lab-host
```

Expected: catch installs successfully and keeps the real service flags, including `--data-dir=/root/data --tsnet-host=yeet-lab`.

- [ ] **Step 2: Verify catch service state**

Run:

```bash
ssh root@lab-host 'systemctl is-active catch && systemctl cat catch | sed -n "/^ExecStart=/p"'
```

Expected: output includes `active` and an `ExecStart=` line with `--data-dir=/root/data --tsnet-host=yeet-lab`.

- [ ] **Step 3: Create a disposable VM from an agent-capable image**

Use a timestamped service name:

```bash
svc="codex-vsock-agent-$(date +%Y%m%d%H%M%S)"
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet run "$svc" vm://ubuntu/26.04 --net=lan --image-policy=cached --cpus=1 --memory=1g --disk=8g
```

Expected: VM provisions and starts. If the cached image does not contain `yeet-agent`, rebuild or publish an agent-capable image before treating this smoke test as complete.

- [ ] **Step 4: Verify `yeet ip` uses live state**

Run:

```bash
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet ip "$svc"
```

Expected: output is one IPv4 address from the disposable VM.

- [ ] **Step 5: Verify `yeet info` JSON includes agent-source IP data**

Run:

```bash
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet info "$svc" --format=json-pretty | jq '.network.ips, .vm.networks'
```

Expected: at least one IP entry has `"source": "agent"`.

- [ ] **Step 6: Verify SSH host selection**

Run:

```bash
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet --no-tty ssh "$svc" -- hostname
```

Expected: command prints the disposable VM hostname.

- [ ] **Step 7: Remove neighbor evidence without using a hardcoded DB IP**

Capture the live IP and flush only that neighbor entry:

```bash
ip="$(CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet ip "$svc" | head -n1)"
ssh root@lab-host "ip -4 neigh del '$ip' dev vmbr0 2>/dev/null || true"
```

Expected: command completes. It must not edit `/root/data/db.json`.

- [ ] **Step 8: Re-check IP after neighbor flush**

Run:

```bash
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet ip "$svc"
```

Expected: same current IP is returned through the live vsock agent even after the host neighbor entry was removed.

- [ ] **Step 9: Clean up disposable VM**

Run:

```bash
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet remove "$svc" --yes --clean-data --clean-config
```

Expected: VM service is removed. If remove flags have changed, run `CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet remove --help-llm` and use the documented equivalent.

## Task 9: Final Verification and Docs Check

**Files:**
- Modify docs only if verification reveals inaccurate docs.

- [ ] **Step 1: Run final repository verification**

Run from `/Users/shayne/code/yeet`:

```bash
mise exec -- go test ./...
mise run guest:init:test
mise run guest:agent:test
```

Expected: PASS.

- [ ] **Step 2: Check dirty worktree scope**

Run:

```bash
git status --short
```

Expected: only intentional VM agent changes remain. Existing unrelated worktree changes from before this plan must not be reverted or committed accidentally.

- [ ] **Step 3: Summarize live behavior**

Record these facts in the final handoff:

```text
- catch version deployed to lab-host through yeet init
- disposable VM service name
- yeet ip output before neighbor flush
- yeet info source field showing agent
- yeet ssh hostname output
- yeet ip output after neighbor flush
- cleanup command result
```

Expected: final handoff proves the agent path works and did not rely on a hardcoded DB IP.

## Plan Self-Review Notes

- Spec coverage: the plan covers Firecracker vsock config, separate `yeet-agent`, host-initiated JSONL protocol, port `7788`, IPv4-only v1, live source priority for `ip` and `ssh`, legacy fallbacks, no stale DHCP authority, image integration, Rust/Go tests, and lab-host smoke verification.
- Placeholder scan: generic Rust type parameters and command examples such as `yeet ip <vm>` are intentional; there are no unfinished marker or fill-in sections.
- Type consistency: Go protocol names use `vmAgentRequest`, `vmAgentResponse`, `vmAgentInterface`, and `vmAgentNetworkState`; Rust protocol names use `AgentRequest`, `AgentResponse`, and `AgentInterface`; the shared wire fields are `protocol`, `type`, `request_id`, `interfaces`, and `error`.
