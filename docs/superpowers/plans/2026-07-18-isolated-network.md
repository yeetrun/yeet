# Isolated Network Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a fail-closed `--net=iso` mode that gives supported VMs and container projects stable RFC1918 IPv4 addresses, Catch-originated ingress, and public-IPv4-only egress without access to Catch, private networks, Yeet discovery, or other ISO projects.

**Architecture:** A pure `pkg/iso` package owns mode validation, address partitioning, component allocation, and public-address classification. Catch persists pool and allocation state, installs one backend-neutral host policy plus per-workload routed links, and admits the exact canonical Compose model before starting containers; VM and container adapters consume the same persisted allocation and policy. Every start is ordered behind policy verification, while failed cleanup leaves a non-reusable tombstone.

**Tech Stack:** Go 1.25 via `mise`, `net/netip`, JSON-backed `pkg/db`, Docker Compose canonical JSON, Docker remote network driver, Linux network namespaces/veth/TAP, systemd, nftables, iptables-nft, iptables-legacy with ipset, `miekg/dns`, Firecracker, Tailscale, GitButler, MDX documentation.

## Global Constraints

- The approved design at `docs/superpowers/specs/2026-07-18-isolated-network-design.md` is authoritative; do not weaken its threat model to make an implementation step easier.
- Supported payloads are VM, prebuilt/local image, client-built Dockerfile runtime, generated Python/TypeScript containers, and admitted Compose. Native binary, script, and cron/timer payloads must reject ISO while they run as host root.
- Valid combinations are `iso` for VMs and `iso` or `iso,ts` for non-VM container payloads. `iso` never combines with `svc` or `lan`.
- `-p`, `--publish`, `--publish-reset`, and resolved Compose `ports` are errors for ISO.
- Prefer `172.30.0.0/16`; select only an RFC1918 IPv4 `/16` after checking live host routes/addresses, named namespaces, Docker IPAM, persisted Yeet state, and ISO tombstones. Avoid `172.17.0.0/16` and `172.31.0.0/16` until the end of the curated fallback list.
- The lower `/17` yields 8,192 `/30` links. The upper `/17` yields 1,024 `/27` project networks with one gateway and at most 29 stable component addresses.
- ISO is IPv4-only. Disable generated IPv6 and drop IPv6 on every ISO attachment.
- Catch-local traffic may initiate to every ISO endpoint on every protocol and port. Forwarded LAN, `svc`, Docker, tailnet, and other ISO traffic may not borrow that privilege.
- Workload-initiated traffic may use the dedicated ISO resolver, same-project connectivity, explicit `iso,ts` tailnet routing, and otherwise only globally reachable public IPv4 through NAT.
- Invalid and source-spoofed packets drop silently. Policy-denied destinations reject promptly. Direct public TCP/UDP 53 and TCP/UDP 853 are rejected; DNS-over-HTTPS remains outside the claim.
- Use one vendored special-purpose IPv4 table for all firewall backends and DNS filtering. Do not fetch IANA data at runtime.
- Compose admission happens before pull/build/network creation, uses `docker compose config --format json` before and after the Yeet overlay, rejects unknown service fields, and inspects the created runtime before declaring success.
- Compose `build`, providers, host networking/namespaces, privileges/capabilities/devices, alternate/external networks, custom DNS, host-control mounts, out-of-root binds, cross-project `volumes_from`, scaling, and more than 29 active services must reject.
- Ordinary stop/start/update/restart preserves addresses. Clone allocates fresh state. Removal releases addresses only after verified cleanup; failures leave tombstones.
- Reconciliation and every start fail closed on policy, route, DNS, attachment, Docker runtime, Tailscale, pool, or source-validation drift.
- `manage` covers ISO service mutation, repair, cleanup, and host pool plan/apply. `read` covers pool/service inspection, component addresses, and VM connection metadata. VM guest login remains key-authorized and does not require Catch `ssh`.
- Keep `svc`, `lan`, and existing `ts` behavior unchanged unless a test in this plan explicitly says otherwise.
- Use `mise exec -- go ...`; do not set `GOCACHE`.
- Preserve unrelated dirty `.codex/skills/gitbutler` files. For each commit, derive only the listed file IDs from `but diff --format json` and pass those IDs to `but commit codex/iso-network-design --changes`.
- Do not push, land, tag, or release during plan execution unless the user separately authorizes publication.

## File and Interface Map

- `pkg/iso/layout.go`: pure `/16` partition and `/30`/`/27` address math.
- `pkg/iso/components.go`: stable component assignment and retired-address protection.
- `pkg/iso/modes.go`: shared payload/mode/publish compatibility contract.
- `pkg/iso/ranges.go`: vendored public/special IPv4 classification.
- `pkg/db/db.go`, generated views/clones, and `pkg/db/migrate.go`: persisted pool, allocation, lifecycle, component, and Docker-driver mode state.
- `pkg/catch/iso_allocator.go`: atomic allocation/reservation/tombstone operations over `db.Store`.
- `pkg/catch/iso_pool.go`: live collision probes and automatic/explicit pool plans.
- `pkg/catch/iso_compose.go`: canonical Compose safe-profile admission and post-overlay validation.
- `pkg/catch/iso_runtime.go`: per-service topology ensure, verify, quarantine, and cleanup orchestration.
- `pkg/netns/iso_firewall.go`: backend-neutral policy input and nftables/iptables rendering/application.
- `pkg/netns/iso_topology.go`: Linux veth, namespace, route, blackhole, sysctl, and TAP helpers.
- `pkg/catch/iso_dns.go`, `pkg/catch/iso_dns_server.go`, `pkg/catch/iso_dns_service.go`: public-only TCP/UDP resolver on the Catch host.
- `pkg/dnet/dnet.go`: ISO-aware Docker driver behavior, with no driver-local NAT or port forwarding.
- `pkg/catch/vm_network.go` and reconciliation/provisioning files: dedicated `/30` VM adapter.
- `pkg/catchrpc`: typed ISO pool and observability RPC contracts.
- `pkg/yeet`: host-set, info, IP, SSH-proxy, and run-draft client behavior.
- `README.md` and `website/docs`: the supported matrix, threat boundary, DNS/Tailscale behavior, operations, and limitations.

---

### Task 1: Pure ISO Contract, Address Layout, and Public-IPv4 Classification

**Files:**
- Create: `pkg/iso/layout.go`
- Create: `pkg/iso/components.go`
- Create: `pkg/iso/modes.go`
- Create: `pkg/iso/ranges.go`
- Create: `pkg/iso/layout_test.go`
- Create: `pkg/iso/components_test.go`
- Create: `pkg/iso/modes_test.go`
- Create: `pkg/iso/ranges_test.go`
- Create: `pkg/iso/iso_fuzz_test.go`

**Interfaces:**
- Consumes: only Go standard-library `net/netip`, `sort`, `strings`, `encoding/binary`, `errors`, and `fmt`.
- Produces: `iso.NewLayout(netip.Prefix) (iso.Layout, error)`, `Layout.Link(int) (netip.Prefix, error)`, `Layout.Project(int) (netip.Prefix, error)`, `iso.PlanComponents(netip.Prefix, map[string]netip.Addr, []string) (iso.ComponentPlan, error)`, `iso.NormalizeModes([]string) ([]string, error)`, `iso.ValidateNetwork(iso.NetworkRequest) error`, and `iso.IsPublicIPv4(netip.Addr, netip.Prefix) bool`.

- [ ] **Step 1: Write failing layout and component tests**

```go
package iso

import (
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"testing"
)

func TestLayoutPartitionsPreferredPool(t *testing.T) {
	layout, err := NewLayout(netip.MustParsePrefix("172.30.0.0/16"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := layout.Links, netip.MustParsePrefix("172.30.0.0/17"); got != want {
		t.Fatalf("Links = %v, want %v", got, want)
	}
	if got, want := layout.Projects, netip.MustParsePrefix("172.30.128.0/17"); got != want {
		t.Fatalf("Projects = %v, want %v", got, want)
	}
	link, _ := layout.Link(8191)
	if want := netip.MustParsePrefix("172.30.127.252/30"); link != want {
		t.Fatalf("last link = %v, want %v", link, want)
	}
	project, _ := layout.Project(1023)
	if want := netip.MustParsePrefix("172.30.255.224/27"); project != want {
		t.Fatalf("last project = %v, want %v", project, want)
	}
}

func TestPlanComponentsPreservesAndRetiresAddresses(t *testing.T) {
	project := netip.MustParsePrefix("172.30.128.0/27")
	current := map[string]netip.Addr{
		"api": netip.MustParseAddr("172.30.128.2"),
		"old": netip.MustParseAddr("172.30.128.3"),
	}
	got, err := PlanComponents(project, current, []string{"api", "worker"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]netip.Addr{
		"api":    netip.MustParseAddr("172.30.128.2"),
		"worker": netip.MustParseAddr("172.30.128.4"),
	}
	if !reflect.DeepEqual(got.Desired, want) {
		t.Fatalf("Desired = %#v, want %#v", got.Desired, want)
	}
	if got.Retired["old"] != netip.MustParseAddr("172.30.128.3") {
		t.Fatalf("Retired = %#v", got.Retired)
	}
}

func TestPlanComponentsEnforcesCapacity(t *testing.T) {
	names := make([]string, MaxComponents+1)
	for i := range names {
		names[i] = fmt.Sprintf("component-%02d", i)
	}
	_, err := PlanComponents(netip.MustParsePrefix("172.30.128.0/27"), nil, names)
	if !errors.Is(err, ErrComponentCapacity) {
		t.Fatalf("error = %v, want ErrComponentCapacity", err)
	}
}
```

- [ ] **Step 2: Run the focused tests and confirm the package is absent**

Run: `mise exec -- go test ./pkg/iso -run 'TestLayout|TestPlanComponents' -count=1`

Expected: FAIL because `pkg/iso` and its symbols do not exist.

- [ ] **Step 3: Implement deterministic layout and stable component allocation**

```go
package iso

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"sort"
)

const (
	PreferredPool    = "172.30.0.0/16"
	AllocatorVersion = 1
	PolicyVersion    = 1
	MaxLinks         = 8192
	MaxProjects      = 1024
	MaxComponents    = 29
)

var (
	ErrLinkCapacity      = errors.New("ISO link capacity exhausted")
	ErrProjectCapacity   = errors.New("ISO project capacity exhausted")
	ErrComponentCapacity = errors.New("ISO project supports at most 29 active components")
)

type Layout struct {
	Pool     netip.Prefix
	Links    netip.Prefix
	Projects netip.Prefix
}

func NewLayout(pool netip.Prefix) (Layout, error) {
	pool = pool.Masked()
	if !pool.IsValid() || !pool.Addr().Is4() || pool.Bits() != 16 {
		return Layout{}, fmt.Errorf("ISO pool must be an IPv4 /16: %v", pool)
	}
	projectBase, err := addIPv4(pool.Addr(), 1<<15)
	if err != nil {
		return Layout{}, err
	}
	return Layout{
		Pool:     pool,
		Links:    netip.PrefixFrom(pool.Addr(), 17),
		Projects: netip.PrefixFrom(projectBase, 17),
	}, nil
}

func (l Layout) Link(index int) (netip.Prefix, error) {
	if index < 0 || index >= MaxLinks {
		return netip.Prefix{}, ErrLinkCapacity
	}
	addr, err := addIPv4(l.Links.Addr(), uint32(index*4))
	return netip.PrefixFrom(addr, 30), err
}

func (l Layout) Project(index int) (netip.Prefix, error) {
	if index < 0 || index >= MaxProjects {
		return netip.Prefix{}, ErrProjectCapacity
	}
	addr, err := addIPv4(l.Projects.Addr(), uint32(index*32))
	return netip.PrefixFrom(addr, 27), err
}

func addIPv4(addr netip.Addr, offset uint32) (netip.Addr, error) {
	if !addr.Is4() {
		return netip.Addr{}, fmt.Errorf("address is not IPv4: %v", addr)
	}
	raw := addr.As4()
	base := binary.BigEndian.Uint32(raw[:])
	if ^uint32(0)-base < offset {
		return netip.Addr{}, fmt.Errorf("IPv4 address overflow from %v", addr)
	}
	var out [4]byte
	binary.BigEndian.PutUint32(out[:], base+offset)
	return netip.AddrFrom4(out), nil
}

type ComponentPlan struct {
	Desired map[string]netip.Addr
	Retired map[string]netip.Addr
}

func PlanComponents(project netip.Prefix, current map[string]netip.Addr, desired []string) (ComponentPlan, error) {
	project = project.Masked()
	if !project.IsValid() || !project.Addr().Is4() || project.Bits() != 27 {
		return ComponentPlan{}, fmt.Errorf("ISO project must be an IPv4 /27: %v", project)
	}
	names := append([]string(nil), desired...)
	sort.Strings(names)
	for i, name := range names {
		if name == "" || i > 0 && name == names[i-1] {
			return ComponentPlan{}, fmt.Errorf("ISO component names must be non-empty and unique")
		}
	}
	if len(names) > MaxComponents {
		return ComponentPlan{}, ErrComponentCapacity
	}
	used := map[netip.Addr]bool{}
	for name, addr := range current {
		if name == "" || !project.Contains(addr) {
			return ComponentPlan{}, fmt.Errorf("current ISO component %q has address %v outside %v", name, addr, project)
		}
		offset := uint32(addr.As4()[3] - project.Addr().As4()[3])
		if offset < 2 || offset > 30 {
			return ComponentPlan{}, fmt.Errorf("current ISO component %q uses reserved address %v", name, addr)
		}
		if used[addr] {
			return ComponentPlan{}, fmt.Errorf("current ISO component address %v is duplicated", addr)
		}
		used[addr] = true
	}
	out := ComponentPlan{Desired: map[string]netip.Addr{}, Retired: map[string]netip.Addr{}}
	wanted := map[string]bool{}
	for _, name := range names {
		wanted[name] = true
		if addr, ok := current[name]; ok {
			out.Desired[name] = addr
		}
	}
	for name, addr := range current {
		if !wanted[name] {
			out.Retired[name] = addr
		}
	}
	for _, name := range names {
		if _, ok := out.Desired[name]; ok {
			continue
		}
		for host := uint32(2); host <= 30; host++ {
			addr, err := addIPv4(project.Addr(), host)
			if err != nil {
				return ComponentPlan{}, err
			}
			if !used[addr] {
				out.Desired[name] = addr
				used[addr] = true
				break
			}
		}
		if _, ok := out.Desired[name]; !ok {
			return ComponentPlan{}, ErrComponentCapacity
		}
	}
	return out, nil
}
```

Place `Layout` and `addIPv4` in `layout.go`; place `ComponentPlan` and `PlanComponents` in `components.go`.

- [ ] **Step 4: Write failing network-contract and address-classification tests**

```go
package iso

import (
	"net/netip"
	"strings"
	"testing"
)

func TestValidateNetworkMatrix(t *testing.T) {
	tests := []struct {
		name    string
		req     NetworkRequest
		wantErr string
	}{
		{name: "vm", req: NetworkRequest{Payload: PayloadVM, Modes: []string{"iso"}}},
		{name: "compose tailscale", req: NetworkRequest{Payload: PayloadCompose, Modes: []string{"iso", "ts"}}},
		{name: "svc conflict", req: NetworkRequest{Payload: PayloadCompose, Modes: []string{"iso", "svc"}}, wantErr: "cannot combine"},
		{name: "lan conflict", req: NetworkRequest{Payload: PayloadContainer, Modes: []string{"iso", "lan"}}, wantErr: "cannot combine"},
		{name: "vm tailscale", req: NetworkRequest{Payload: PayloadVM, Modes: []string{"iso", "ts"}}, wantErr: "VMs support only iso"},
		{name: "native", req: NetworkRequest{Payload: PayloadNative, Modes: []string{"iso"}}, wantErr: "native"},
		{name: "cron", req: NetworkRequest{Payload: PayloadCron, Modes: []string{"iso"}}, wantErr: "cron"},
		{name: "publish", req: NetworkRequest{Payload: PayloadContainer, Modes: []string{"iso"}, Published: true}, wantErr: "published ports"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateNetwork(tt.req)
			if tt.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestIsPublicIPv4(t *testing.T) {
	pool := netip.MustParsePrefix("172.30.0.0/16")
	for raw, want := range map[string]bool{
		"1.1.1.1": true,
		"8.8.8.8": true,
		"10.0.0.1": false,
		"100.100.100.100": false,
		"169.254.169.254": false,
		"172.30.0.2": false,
		"192.0.0.8": false,
		"192.0.0.9": true,
		"192.0.0.10": true,
		"192.31.196.1": true,
		"192.52.193.1": true,
		"192.175.48.1": true,
		"192.0.2.1": false,
		"198.18.0.1": false,
		"224.0.0.1": false,
		"2001:4860:4860::8888": false,
	} {
		if got := IsPublicIPv4(netip.MustParseAddr(raw), pool); got != want {
			t.Errorf("IsPublicIPv4(%s) = %v, want %v", raw, got, want)
		}
	}
}
```

- [ ] **Step 5: Implement the shared mode matrix and vendored range table**

```go
package iso

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

type PayloadKind string

const (
	PayloadVM        PayloadKind = "vm"
	PayloadCompose   PayloadKind = "compose"
	PayloadContainer PayloadKind = "container"
	PayloadNative    PayloadKind = "native"
	PayloadCron      PayloadKind = "cron"
)

type NetworkRequest struct {
	Payload   PayloadKind
	Modes     []string
	Published bool
}

func NormalizeModes(modes []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(modes))
	for _, raw := range modes {
		mode := strings.ToLower(strings.TrimSpace(raw))
		if mode == "" {
			return nil, fmt.Errorf("network mode cannot be empty")
		}
		if mode != "svc" && mode != "lan" && mode != "ts" && mode != "iso" {
			return nil, fmt.Errorf("unsupported network mode %q", raw)
		}
		if !seen[mode] {
			seen[mode] = true
			out = append(out, mode)
		}
	}
	sort.Strings(out)
	return out, nil
}

func ValidateNetwork(req NetworkRequest) error {
	modes, err := NormalizeModes(req.Modes)
	if err != nil {
		return err
	}
	has := func(want string) bool {
		for _, mode := range modes {
			if mode == want {
				return true
			}
		}
		return false
	}
	if !has("iso") {
		return nil
	}
	if has("svc") || has("lan") {
		return fmt.Errorf("iso cannot combine with svc or lan")
	}
	if req.Published {
		return fmt.Errorf("iso does not support published ports")
	}
	switch req.Payload {
	case PayloadVM:
		if len(modes) != 1 {
			return fmt.Errorf("VMs support only iso as a Yeet-managed isolated mode")
		}
	case PayloadCompose, PayloadContainer:
		if len(modes) > 2 || len(modes) == 2 && !has("ts") {
			return fmt.Errorf("container ISO modes must be iso or iso,ts")
		}
	case PayloadNative:
		return fmt.Errorf("native root services do not support iso")
	case PayloadCron:
		return fmt.Errorf("cron root services do not support iso")
	default:
		return fmt.Errorf("payload kind %q does not support iso", req.Payload)
	}
	return nil
}
```

```go
package iso

import "net/netip"

// nonPublicIPv4 is the deny representation of the IANA IPv4 Special-Purpose
// Address Registry snapshot last updated 2025-10-09, plus all IPv4 multicast.
// Source: https://www.iana.org/assignments/iana-ipv4-special-registry/
// The split of 192.0.0.0/24 preserves its globally reachable .9 and .10
// anycast exceptions. Update this reviewed table explicitly when IANA changes.
var nonPublicIPv4 = mustPrefixes(
	"0.0.0.0/8",
	"10.0.0.0/8",
	"100.64.0.0/10",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"172.16.0.0/12",
	"192.0.0.0/29",
	"192.0.0.8/32",
	"192.0.0.11/32",
	"192.0.0.12/30",
	"192.0.0.16/28",
	"192.0.0.32/27",
	"192.0.0.64/26",
	"192.0.0.128/25",
	"192.0.2.0/24",
	"192.88.99.0/24",
	"192.168.0.0/16",
	"198.18.0.0/15",
	"198.51.100.0/24",
	"203.0.113.0/24",
	"224.0.0.0/4",
	"240.0.0.0/4",
)

func IsPublicIPv4(addr netip.Addr, isoPool netip.Prefix) bool {
	if !addr.IsValid() || !addr.Is4() || addr.IsUnspecified() {
		return false
	}
	if isoPool.IsValid() && isoPool.Contains(addr) {
		return false
	}
	for _, prefix := range nonPublicIPv4 {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

func NonPublicIPv4Prefixes(isoPool netip.Prefix) []netip.Prefix {
	out := append([]netip.Prefix(nil), nonPublicIPv4...)
	if isoPool.IsValid() {
		out = append(out, isoPool.Masked())
	}
	return out
}

func mustPrefixes(raw ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(raw))
	for i, value := range raw {
		out[i] = netip.MustParsePrefix(value)
	}
	return out
}
```

- [ ] **Step 6: Add fuzz coverage for prefixes, modes, and component names**

```go
package iso

import (
	"net/netip"
	"strings"
	"testing"
)

func FuzzISOInputs(f *testing.F) {
	f.Add("172.30.0.0/16", "api", "iso,ts")
	f.Fuzz(func(t *testing.T, rawPrefix, component, rawModes string) {
		prefix, err := netip.ParsePrefix(rawPrefix)
		if err == nil {
			_, _ = NewLayout(prefix)
			_, _ = PlanComponents(prefix, nil, []string{component})
		}
		_, _ = NormalizeModes(strings.Split(rawModes, ","))
	})
}
```

- [ ] **Step 7: Run the package tests and fuzz smoke test**

Run: `mise exec -- go test ./pkg/iso -count=1 && mise exec -- go test ./pkg/iso -run '^$' -fuzz FuzzISOInputs -fuzztime=10s`

Expected: PASS; the fuzz run completes without a crash or newly written failing corpus.

- [ ] **Step 8: Commit only the new ISO package**

Run:

```bash
IDS=$(but diff --format json | jq -r --args '$ARGS.positional as $paths | [.changes[] | select(.path as $path | $paths | index($path)) | .id] | join(",")' -- pkg/iso/layout.go pkg/iso/components.go pkg/iso/modes.go pkg/iso/ranges.go pkg/iso/layout_test.go pkg/iso/components_test.go pkg/iso/modes_test.go pkg/iso/ranges_test.go pkg/iso/iso_fuzz_test.go)
test -n "$IDS"
but commit codex/iso-network-design -m "iso: add network contract and address model" --changes "$IDS"
```

Expected: GitButler creates one commit on `codex/iso-network-design`; unrelated `.codex/skills/gitbutler` changes remain uncommitted.

### Task 2: Persisted Pool and Allocation State

**Files:**
- Modify: `pkg/db/db.go`
- Modify: `pkg/db/migrate.go`
- Modify: `pkg/db/db_test.go`
- Regenerate: `pkg/db/db_view.go`
- Regenerate: `pkg/db/db_clone.go`
- Create: `pkg/catch/iso_allocator.go`
- Create: `pkg/catch/iso_allocator_test.go`

**Interfaces:**
- Consumes: `iso.Layout`, `iso.PlanComponents`, `iso.AllocatorVersion`, `iso.PolicyVersion`, and `db.Store.MutateData(func(*db.Data) error)`.
- Produces: persisted `db.ISOPool`, `db.ISOAllocation`, `db.ISOComponent`, `db.Data.ISOPool`, `db.Service.ISO`, `db.DockerNetwork.Mode`; `Server.reserveISOAllocation(context.Context, string, isoReservationRequest) (*db.ISOAllocation, error)`; `Server.markISOState(string, string, error) error`; and `Server.releaseISOAllocation(string) error`.

- [ ] **Step 1: Write migration, clone, stability, exhaustion, and tombstone tests**

```go
func TestMigrateV11AddsISOState(t *testing.T) {
	d := &Data{DataVersion: 11, Services: map[string]*Service{"app": {Name: "app"}}}
	migrated, err := migrate(d)
	if err != nil {
		t.Fatal(err)
	}
	if !migrated || d.DataVersion != 12 {
		t.Fatalf("migrated=%v version=%d", migrated, d.DataVersion)
	}
}

func TestISOStateCloneIsDeep(t *testing.T) {
	d := &Data{ISOPool: &ISOPool{Prefix: netip.MustParsePrefix("172.30.0.0/16")}, Services: map[string]*Service{
		"app": {Name: "app", ISO: &ISOAllocation{Components: map[string]ISOComponent{
			"api": {Address: netip.MustParseAddr("172.30.128.2")},
		}}},
	}}
	clone := d.Clone()
	clone.Services["app"].ISO.Components["api"] = ISOComponent{Address: netip.MustParseAddr("172.30.128.3")}
	if got := d.Services["app"].ISO.Components["api"].Address.String(); got != "172.30.128.2" {
		t.Fatalf("source mutated to %s", got)
	}
}
```

```go
func TestReserveISOAllocationStableAcrossRetries(t *testing.T) {
	server := newISOAllocatorTestServer(t, "172.30.0.0/16")
	req := isoReservationRequest{Kind: iso.PayloadCompose, Modes: []string{"iso"}, Components: []string{"api", "worker"}}
	first, err := server.reserveISOAllocation(context.Background(), "app", req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := server.reserveISOAllocation(context.Background(), "app", req)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(first, second); diff != "" {
		t.Fatalf("allocation changed (-first +second):\n%s", diff)
	}
}

func TestReserveISOAllocationDoesNotReuseTombstone(t *testing.T) {
	server := newISOAllocatorTestServer(t, "172.30.0.0/16")
	first, _ := server.reserveISOAllocation(context.Background(), "old", isoReservationRequest{Kind: iso.PayloadVM, Modes: []string{"iso"}})
	if err := server.markISOState("old", string(iso.StateTombstoned), errors.New("link still present")); err != nil {
		t.Fatal(err)
	}
	second, err := server.reserveISOAllocation(context.Background(), "new", isoReservationRequest{Kind: iso.PayloadVM, Modes: []string{"iso"}})
	if err != nil {
		t.Fatal(err)
	}
	if second.Link == first.Link {
		t.Fatalf("reused tombstoned link %v", first.Link)
	}
}
```

- [ ] **Step 2: Run the DB and allocator tests to verify failure**

Run: `mise exec -- go test ./pkg/db ./pkg/catch -run 'TestMigrateV11AddsISOState|TestISOStateCloneIsDeep|TestReserveISOAllocation' -count=1`

Expected: FAIL because the ISO persistence types and allocator do not exist.

- [ ] **Step 3: Add versioned DB records and Docker network mode**

```go
type ISOPool struct {
	Prefix              netip.Prefix
	Source              string
	AllocatorVersion    int
	PolicyVersion       int
	AggregateRouteState string `json:",omitempty"`
	LastConflict        string `json:",omitempty"`
}

type ISOComponent struct {
	Address netip.Addr
	State   string
}

type ISOAllocation struct {
	Kind              string
	State             string
	Link              netip.Prefix
	HostIP            netip.Addr
	PeerIP            netip.Addr
	Project           netip.Prefix `json:",omitempty"`
	Gateway           netip.Addr `json:",omitempty"`
	Interface         string
	PeerInterface     string
	NetNS             string `json:",omitempty"`
	Bridge            string `json:",omitempty"`
	Components        map[string]ISOComponent `json:",omitempty"`
	RetiredComponents map[string]ISOComponent `json:",omitempty"`
	DesiredModes      []string
	AllocatorVersion  int
	PolicyVersion     int
	RemoveRequested   bool `json:",omitempty"`
	CleanupVerified   bool `json:",omitempty"`
	LastError         string `json:",omitempty"`
}
```

Add `ISOPool *ISOPool` to `db.Data`, `ISO *ISOAllocation` to `db.Service`, and `Mode string` to `db.DockerNetwork`. Add `ISOPool,ISOAllocation,ISOComponent` to the `//go:generate viewer` type list. Set `CurrentDataVersion = 12`, add `11: addISOState`, and define:

```go
func addISOState(d *Data) error {
	return nil
}
```

- [ ] **Step 4: Generate views/clones and verify generated state compiles**

Run: `mise exec -- go generate ./pkg/db && mise exec -- go test ./pkg/db -run 'TestMigrateV11AddsISOState|TestISOStateCloneIsDeep' -count=1`

Expected: PASS; generated `ISOPoolView`, `ISOAllocationView`, and `ISOComponentView` types are present.

- [ ] **Step 5: Implement atomic reservation and lifecycle mutations**

```go
package catch

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/iso"
)

type isoReservationRequest struct {
	Kind       iso.PayloadKind
	Modes      []string
	Components []string
}

func (s *Server) reserveISOAllocation(ctx context.Context, name string, req isoReservationRequest) (*db.ISOAllocation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var result *db.ISOAllocation
	_, _, err := s.cfg.DB.MutateService(name, func(data *db.Data, service *db.Service) error {
		if data.ISOPool == nil {
			return fmt.Errorf("ISO pool is not configured")
		}
		layout, err := iso.NewLayout(data.ISOPool.Prefix)
		if err != nil {
			return err
		}
		if service.ISO == nil {
			link, err := firstFreeISOLink(layout, data.Services)
			if err != nil {
				return err
			}
			service.ISO = newDBISOAllocation(name, req, link)
			if req.Kind != iso.PayloadVM {
				project, err := firstFreeISOProject(layout, data.Services)
				if err != nil {
					return err
				}
				service.ISO.Project = project
				service.ISO.Gateway = project.Addr().Next()
			}
		}
		if service.ISO.State == string(iso.StateTombstoned) {
			return fmt.Errorf("service %q has an ISO cleanup tombstone: %s", name, service.ISO.LastError)
		}
		current := map[string]netip.Addr{}
		for component, state := range service.ISO.Components {
			current[component] = state.Address
		}
		if service.ISO.Project.IsValid() {
			plan, err := iso.PlanComponents(service.ISO.Project, current, req.Components)
			if err != nil {
				return err
			}
			service.ISO.Components = componentStates(plan.Desired, "reserved")
			service.ISO.RetiredComponents = componentStates(plan.Retired, "retiring")
		}
		service.ISO.DesiredModes = append([]string(nil), req.Modes...)
		service.ISO.State = string(iso.StateReserved)
		service.ISO.LastError = ""
		copy := *service.ISO
		result = &copy
		return nil
	})
	return result, err
}

func (s *Server) markISOState(name, state string, cause error) error {
	_, _, err := s.cfg.DB.MutateService(name, func(_ *db.Data, service *db.Service) error {
		if service.ISO == nil {
			return fmt.Errorf("service %q has no ISO allocation", name)
		}
		service.ISO.State = state
		service.ISO.LastError = ""
		if cause != nil {
			service.ISO.LastError = cause.Error()
		}
		return nil
	})
	return err
}
```

Define in `pkg/iso/components.go`:

```go
type AllocationState string

const (
	StateReserved    AllocationState = "reserved"
	StateReady       AllocationState = "ready"
	StateStopped     AllocationState = "stopped"
	StateDegraded    AllocationState = "degraded"
	StateRemoving    AllocationState = "removing"
	StateQuarantined AllocationState = "quarantined"
	StateTombstoned  AllocationState = "tombstoned"
)
```

Use deterministic ascending scans over all services whose `ISO` is non-nil, regardless of lifecycle state. The helper implementations are:

```go
func newDBISOAllocation(name string, req isoReservationRequest, link netip.Prefix) *db.ISOAllocation {
	token := isoNameToken(name)
	allocation := &db.ISOAllocation{
		Kind:             string(req.Kind),
		State:            string(iso.StateReserved),
		Link:             link,
		HostIP:           link.Addr().Next(),
		PeerIP:           link.Addr().Next().Next(),
		Interface:        "yi-" + token,
		PeerInterface:    "yo-" + token,
		DesiredModes:     append([]string(nil), req.Modes...),
		AllocatorVersion: iso.AllocatorVersion,
		PolicyVersion:    iso.PolicyVersion,
	}
	if req.Kind != iso.PayloadVM {
		allocation.NetNS = "yeet-" + token + "-ns"
	}
	return allocation
}

func isoNameToken(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:5])
}

func componentStates(addrs map[string]netip.Addr, state string) map[string]db.ISOComponent {
	if len(addrs) == 0 {
		return nil
	}
	out := make(map[string]db.ISOComponent, len(addrs))
	for name, addr := range addrs {
		out[name] = db.ISOComponent{Address: addr, State: state}
	}
	return out
}

func firstFreeISOLink(layout iso.Layout, services map[string]*db.Service) (netip.Prefix, error) {
	used := map[netip.Prefix]bool{}
	for _, service := range services {
		if service != nil && service.ISO != nil && service.ISO.Link.IsValid() {
			used[service.ISO.Link.Masked()] = true
		}
	}
	for index := 0; index < iso.MaxLinks; index++ {
		candidate, err := layout.Link(index)
		if err != nil {
			return netip.Prefix{}, err
		}
		if !used[candidate] {
			return candidate, nil
		}
	}
	return netip.Prefix{}, iso.ErrLinkCapacity
}

func firstFreeISOProject(layout iso.Layout, services map[string]*db.Service) (netip.Prefix, error) {
	used := map[netip.Prefix]bool{}
	for _, service := range services {
		if service != nil && service.ISO != nil && service.ISO.Project.IsValid() {
			used[service.ISO.Project.Masked()] = true
		}
	}
	for index := 0; index < iso.MaxProjects; index++ {
		candidate, err := layout.Project(index)
		if err != nil {
			return netip.Prefix{}, err
		}
		if !used[candidate] {
			return candidate, nil
		}
	}
	return netip.Prefix{}, iso.ErrProjectCapacity
}

func (s *Server) releaseISOAllocation(name string) error {
	_, _, err := s.cfg.DB.MutateService(name, func(_ *db.Data, service *db.Service) error {
		service.ISO = nil
		return nil
	})
	return err
}
```

Import `context`, `crypto/sha256`, and `encoding/hex`; the `yi-`/`yo-` names are 13 characters, under Linux's 15-character interface limit. Only call `releaseISOAllocation` after Task 9's live cleanup verifier succeeds.

- [ ] **Step 6: Run DB and allocator tests including the race detector**

Run: `mise exec -- go test ./pkg/db ./pkg/catch -run 'TestMigrateV11AddsISOState|TestISOStateCloneIsDeep|TestReserveISOAllocation' -race -count=1`

Expected: PASS with no race report.

- [ ] **Step 7: Commit persistence and allocator state**

Run:

```bash
IDS=$(but diff --format json | jq -r --args '$ARGS.positional as $paths | [.changes[] | select(.path as $path | $paths | index($path)) | .id] | join(",")' -- pkg/db/db.go pkg/db/migrate.go pkg/db/db_test.go pkg/db/db_view.go pkg/db/db_clone.go pkg/catch/iso_allocator.go pkg/catch/iso_allocator_test.go pkg/iso/components.go)
test -n "$IDS"
but commit codex/iso-network-design -m "iso: persist stable network allocations" --changes "$IDS"
```

Expected: one commit; unrelated dirty files remain uncommitted.

### Task 3: Network-Mode Validation and Payload Boundary

**Files:**
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`
- Modify: `pkg/yeet/run_draft_validate.go`
- Modify: `pkg/yeet/run_draft_validate_test.go`
- Modify: `pkg/yeet/run_web.go`
- Modify: `pkg/yeet/run_web_test.go`
- Modify: `pkg/catch/installer_file.go`
- Modify: `pkg/catch/installer_file_test.go`
- Modify: `pkg/catch/installer_service.go`

**Interfaces:**
- Consumes: `iso.NormalizeModes` and `iso.ValidateNetwork` from Task 1; current `NetworkOpts`, `RunDraft`, and payload classification.
- Produces: client and Catch validation that agree on the ISO compatibility matrix; `NetworkOpts.ISO bool`; `NetworkOpts.Modes []string`; a Catch-side `networkPayloadKind() iso.PayloadKind` mapping.

- [ ] **Step 1: Add table-driven client and Catch rejection tests**

```go
func TestRunDraftISOCompatibility(t *testing.T) {
	tests := []struct {
		name    string
		draft   RunDraft
		wantErr string
	}{
		{name: "VM ISO", draft: RunDraft{Payload: RunDraftPayload{Kind: "vm"}, Network: RunDraftNetwork{Modes: []string{"iso"}}}},
		{name: "container ISO TS", draft: RunDraft{Payload: RunDraftPayload{Kind: "image"}, Network: RunDraftNetwork{Modes: []string{"iso", "ts"}}}},
		{name: "ISO SVC", draft: RunDraft{Payload: RunDraftPayload{Kind: "compose"}, Network: RunDraftNetwork{Modes: []string{"iso", "svc"}}}, wantErr: "cannot combine"},
		{name: "VM ISO TS", draft: RunDraft{Payload: RunDraftPayload{Kind: "vm"}, Network: RunDraftNetwork{Modes: []string{"iso", "ts"}}}, wantErr: "VMs support only iso"},
		{name: "cron ISO", draft: RunDraft{Payload: RunDraftPayload{Kind: "cron"}, Network: RunDraftNetwork{Modes: []string{"iso"}}}, wantErr: "cron root services"},
		{name: "ISO publish", draft: RunDraft{Payload: RunDraftPayload{Kind: "image"}, Network: RunDraftNetwork{Modes: []string{"iso"}, Publish: []string{"8080:80"}}}, wantErr: "published ports"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ValidateRunDraft(tt.draft)
			assertValidationContains(t, result, tt.wantErr)
		})
	}
}
```

```go
func TestParseNetworkISO(t *testing.T) {
	for _, tt := range []struct {
		raw     string
		kind    iso.PayloadKind
		wantISO bool
		wantErr string
	}{
		{raw: "iso", kind: iso.PayloadContainer, wantISO: true},
		{raw: "iso,ts", kind: iso.PayloadCompose, wantISO: true},
		{raw: "iso,svc", kind: iso.PayloadCompose, wantErr: "cannot combine"},
		{raw: "iso", kind: iso.PayloadNative, wantErr: "native root"},
	} {
		opts, err := parseNetworkForPayload(NetworkOpts{Interfaces: tt.raw}, tt.kind, false)
		if tt.wantErr == "" && (err != nil || opts.ISO != tt.wantISO) {
			t.Fatalf("parseNetworkForPayload(%q) = %#v, %v", tt.raw, opts, err)
		}
		if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
			t.Fatalf("error = %v, want %q", err, tt.wantErr)
		}
	}
}
```

- [ ] **Step 2: Run focused validation tests and confirm failure**

Run: `mise exec -- go test ./pkg/cli ./pkg/yeet ./pkg/catch -run 'TestRunDraftISOCompatibility|TestParseNetworkISO|TestRun.*Net' -count=1`

Expected: FAIL because `iso` is unknown or accepted without the required constraints.

- [ ] **Step 3: Route all client validation through the shared contract**

Use a single payload mapping and call the shared validator after existing normalization:

```go
func validateRunDraftISO(draft RunDraft, result *RunDraftValidationResult) {
	kind := runDraftISOPayloadKind(draft.Payload.Kind)
	err := iso.ValidateNetwork(iso.NetworkRequest{
		Payload:   kind,
		Modes:     draft.Network.Modes,
		Published: len(draft.Network.Publish) != 0 || draft.Network.PublishReset,
	})
	if err != nil {
		result.Add("network.modes", err.Error())
	}
}

func runDraftISOPayloadKind(kind string) iso.PayloadKind {
	switch kind {
	case "vm":
		return iso.PayloadVM
	case "compose":
		return iso.PayloadCompose
	case "image", "dockerfile", "python", "typescript":
		return iso.PayloadContainer
	case "cron":
		return iso.PayloadCron
	default:
		return iso.PayloadNative
	}
}
```

Add `iso` to CLI help and web-draft network enums, but do not yet advertise runtime readiness in prose docs. Keep validation errors field-specific (`network.modes` or `network.publish`).

- [ ] **Step 4: Enforce the same contract after Catch resolves the payload type**

```go
type NetworkOpts struct {
	Interfaces string
	Tailscale  TailscaleOpts
	Macvlan    MacvlanOpts
	Modes      []string
	ISO        bool
}

func parseNetworkForPayload(opts NetworkOpts, payload iso.PayloadKind, published bool) (NetworkOpts, error) {
	modes, err := iso.NormalizeModes(strings.Split(opts.Interfaces, ","))
	if err != nil {
		return NetworkOpts{}, err
	}
	if err := iso.ValidateNetwork(iso.NetworkRequest{Payload: payload, Modes: modes, Published: published}); err != nil {
		return NetworkOpts{}, err
	}
	opts.Interfaces = strings.Join(modes, ",")
	opts.Modes = modes
	opts.ISO = slices.Contains(modes, "iso")
	return opts, nil
}
```

Preserve the incoming `TailscaleOpts` and `MacvlanOpts` when applying the normalized result to `i.cfg.Network`; `parseNetworkPart("iso", dv)` records ISO intent but does not allocate until Compose/VM components are known. Call `parseNetworkForPayload` only after the installer has classified the materialized payload, and before artifact rendering, image pull, VM setup, or service installation. Add this exact comment above the native systemd boundary in both `newSystemdUnit` and the native payload validation branch:

```go
// ISO intentionally rejects native root services. Host root can reconfigure or
// leave a network namespace, so non-root systemd sandboxing is a prerequisite
// for adding native ISO support without making a false security claim.
```

- [ ] **Step 5: Run all parser and draft tests**

Run: `mise exec -- go test ./pkg/cli ./pkg/yeet ./pkg/catch -run 'Network|ISO|RunDraft|ParseNetwork' -count=1`

Expected: PASS; existing `svc`, `lan`, and `ts` cases remain green.

- [ ] **Step 6: Commit the shared mode boundary**

Run:

```bash
IDS=$(but diff --format json | jq -r --args '$ARGS.positional as $paths | [.changes[] | select(.path as $path | $paths | index($path)) | .id] | join(",")' -- pkg/cli/cli.go pkg/cli/cli_test.go pkg/yeet/run_draft_validate.go pkg/yeet/run_draft_validate_test.go pkg/yeet/run_web.go pkg/yeet/run_web_test.go pkg/catch/installer_file.go pkg/catch/installer_file_test.go pkg/catch/installer_service.go)
test -n "$IDS"
but commit codex/iso-network-design -m "iso: enforce payload and mode compatibility" --changes "$IDS"
```

Expected: one commit with validation only.

### Task 4: Pool Selection, Host Plan/Apply RPC, and Catch Summary

**Files:**
- Create: `pkg/catch/iso_pool.go`
- Create: `pkg/catch/iso_pool_test.go`
- Modify: `pkg/catch/iso_allocator.go`
- Modify: `pkg/catchrpc/types.go`
- Modify: `pkg/catchrpc/types_test.go`
- Modify: `pkg/catchrpc/client.go`
- Modify: `pkg/catch/rpc.go`
- Modify: `pkg/catch/rpc_test.go`
- Modify: `pkg/catch/authz.go`
- Modify: `pkg/catch/authz_test.go`
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`
- Modify: `pkg/yeet/host_set.go`
- Modify: `pkg/yeet/host_set_test.go`
- Modify: `pkg/catch/info.go`
- Modify: `pkg/yeet/rpc_types.go`
- Modify: `pkg/yeet/info_cmd.go`
- Modify: `pkg/yeet/info_cmd_test.go`

**Interfaces:**
- Consumes: persisted `db.ISOPool`, ISO allocations, `iso.NewLayout`, `iso.PreferredPool`, host command execution, Docker network inspection, and existing structured host-storage plan/apply patterns.
- Produces: `catchrpc.ISOPoolPlanRequest`, `catchrpc.ISOPoolPlan`, `catchrpc.ISOPoolApplyRequest`, `catchrpc.ISOPoolApplyResult`; client methods `ISOPoolPlan` and `ISOPoolApply`; Catch methods `PlanISOPool(context.Context, catchrpc.ISOPoolPlanRequest) (catchrpc.ISOPoolPlan, error)` and `ApplyISOPool(context.Context, catchrpc.ISOPoolApplyRequest) (catchrpc.ISOPoolApplyResult, error)`; `HostSetFlags.ISOPool string`; and `ServerInfo.ISO catchrpc.ISOPoolSummary`.

- [ ] **Step 1: Write collision-selection and immutable-override tests**

```go
func TestSelectISOPoolPrefers17230(t *testing.T) {
	probe := fakeISOPoolProbe{}
	got, err := selectISOPool(context.Background(), probe, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != netip.MustParsePrefix("172.30.0.0/16") {
		t.Fatalf("pool = %v", got)
	}
}

func TestSelectISOPoolFallsBackAroundCollisions(t *testing.T) {
	probe := fakeISOPoolProbe{occupied: []netip.Prefix{
		netip.MustParsePrefix("172.30.0.0/16"),
		netip.MustParsePrefix("172.29.0.0/16"),
	}}
	got, err := selectISOPool(context.Background(), probe, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != netip.MustParsePrefix("172.28.0.0/16") {
		t.Fatalf("pool = %v, want 172.28.0.0/16", got)
	}
}

func TestPlanISOPoolRejectsChangeWithTombstone(t *testing.T) {
	server := newISOPoolTestServer(t)
	seedISOAllocation(t, server, "stuck", iso.StateTombstoned)
	_, err := server.PlanISOPool(context.Background(), catchrpc.ISOPoolPlanRequest{Prefix: "172.28.0.0/16"})
	if err == nil || !strings.Contains(err.Error(), "stuck") {
		t.Fatalf("error = %v, want blocking service", err)
	}
}
```

- [ ] **Step 2: Run focused pool tests and confirm failure**

Run: `mise exec -- go test ./pkg/catch -run 'TestSelectISOPool|TestPlanISOPool' -count=1`

Expected: FAIL because pool probing and planning do not exist.

- [ ] **Step 3: Implement live collision probes and curated candidates**

```go
type isoPoolProbe interface {
	HostPrefixes(context.Context) ([]netip.Prefix, error)
	NamespacePrefixes(context.Context) ([]netip.Prefix, error)
	DockerPrefixes(context.Context) ([]netip.Prefix, error)
}

var automaticISOPoolCandidates = []string{
	"172.30.0.0/16", "172.29.0.0/16", "172.28.0.0/16", "172.27.0.0/16",
	"172.26.0.0/16", "172.25.0.0/16", "172.24.0.0/16", "172.23.0.0/16",
	"172.22.0.0/16", "172.21.0.0/16", "172.20.0.0/16", "172.19.0.0/16",
	"172.18.0.0/16", "172.16.0.0/16", "172.17.0.0/16", "172.31.0.0/16",
}

func selectISOPool(ctx context.Context, probe isoPoolProbe, persisted []netip.Prefix) (netip.Prefix, error) {
	occupied := append([]netip.Prefix(nil), persisted...)
	for _, load := range []func(context.Context) ([]netip.Prefix, error){probe.HostPrefixes, probe.NamespacePrefixes, probe.DockerPrefixes} {
		prefixes, err := load(ctx)
		if err != nil {
			return netip.Prefix{}, err
		}
		occupied = append(occupied, prefixes...)
	}
	for _, raw := range automaticISOPoolCandidates {
		candidate := netip.MustParsePrefix(raw)
		if !overlapsAny(candidate, occupied) {
			return candidate, nil
		}
	}
	return netip.Prefix{}, fmt.Errorf("no collision-free ISO /16 is available in 172.16.0.0/12")
}
```

Add automatic first-use persistence before Task 2's reservation mutation. `ensureISOPool` must probe outside the DB lock, then use `MutateData` to keep an already-selected concurrent value or persist the probed value with source `automatic`. Re-probe the chosen prefix immediately before persistence. Modify `reserveISOAllocation` to call it before `MutateService`:

```go
func (s *Server) ensureISOPool(ctx context.Context) error {
	dv, err := s.cfg.DB.Get()
	if err != nil {
		return err
	}
	if pool := dv.ISOPool(); pool.Valid() {
		return nil
	}
	prefix, err := selectISOPool(ctx, s.isoPoolProbe(), persistedNetworkPrefixes(dv))
	if err != nil {
		return err
	}
	if err := verifyISOPoolCandidate(ctx, s.isoPoolProbe(), prefix, persistedNetworkPrefixes(dv)); err != nil {
		return err
	}
	_, err = s.cfg.DB.MutateData(func(data *db.Data) error {
		if data.ISOPool == nil {
			data.ISOPool = &db.ISOPool{Prefix: prefix, Source: "automatic", AllocatorVersion: iso.AllocatorVersion, PolicyVersion: iso.PolicyVersion}
		}
		return nil
	})
	return err
}
```

At the start of `reserveISOAllocation`, call `ensureISOPool(ctx)` after checking `ctx.Err()`. This guarantees the selected `/16` is durable before the first `/30` or `/27` is written.

`HostPrefixes` must parse `ip -j address` and `ip -j route show table all`; `NamespacePrefixes` must enumerate `ip netns list` and run `ip -j address` plus routes inside each namespace; `DockerPrefixes` must parse `docker network inspect` IPAM configs. Any command or JSON parse failure is fatal, not an empty result. Include persisted `SvcNetwork`, VM, Docker, current ISO, reserved, quarantined, and tombstoned prefixes in `persisted`.

- [ ] **Step 4: Define typed RPCs and manage authorization**

```go
type ISOPoolPlanRequest struct {
	Prefix string `json:"prefix"`
}

type ISOPoolPlan struct {
	CurrentPrefix string   `json:"currentPrefix,omitempty"`
	DesiredPrefix string   `json:"desiredPrefix"`
	Source        string   `json:"source"`
	Changed       bool     `json:"changed"`
	Blockers      []string `json:"blockers,omitempty"`
	Conflicts     []string `json:"conflicts,omitempty"`
}

type ISOPoolApplyRequest struct {
	Plan ISOPoolPlan `json:"plan"`
}

type ISOPoolApplyResult struct {
	Prefix string `json:"prefix"`
	Source string `json:"source"`
}

type ISOPoolSummary struct {
	Prefix       string `json:"prefix,omitempty"`
	Source       string `json:"source,omitempty"`
	Allocator    int    `json:"allocatorVersion,omitempty"`
	Policy       int    `json:"policyVersion,omitempty"`
	LinksUsed    int    `json:"linksUsed,omitempty"`
	ProjectsUsed int    `json:"projectsUsed,omitempty"`
	Reserved     int    `json:"reserved,omitempty"`
	Active       int    `json:"active,omitempty"`
	Quarantined  int    `json:"quarantined,omitempty"`
	Tombstoned   int    `json:"tombstoned,omitempty"`
	Conflict     string `json:"conflict,omitempty"`
}
```

Map both new RPC methods to `manage` in `pkg/catch/authz.go`, with one positive manage test and one negative read-only test for each method. `ApplyISOPool` must recompute the plan immediately before mutation so a stale client plan cannot race a newly created route or allocation.

- [ ] **Step 5: Add `yeet host set --iso-pool` and render the plan**

```go
type HostSetFlags struct {
	DataDir         string `flag:"data-dir"`
	ServicesRoot    string `flag:"services-root"`
	MigrateServices string `flag:"migrate-services"`
	RestartCatch    bool   `flag:"restart-catch"`
	ISOPool         string `flag:"iso-pool" help:"Set the ISO network RFC1918 IPv4 /16 before any ISO allocation exists."`
	Yes             bool   `flag:"yes" short:"y"`
}
```

In `runHostSet`, reject mixing `ISOPool` with storage flags so v1 cannot partially apply two independent host mutations. Call `ISOPoolPlan`, print current/desired/source/conflicts/blockers, require the existing confirmation unless `--yes`, then call `ISOPoolApply`. Validate an explicit value with:

```go
func validateExplicitISOPool(raw string) (netip.Prefix, error) {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
	if err != nil || !prefix.Addr().Is4() || prefix.Bits() != 16 || prefix != prefix.Masked() {
		return netip.Prefix{}, fmt.Errorf("--iso-pool must be a canonical RFC1918 IPv4 /16")
	}
	private := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
	}
	for _, allowed := range private {
		if allowed.Contains(prefix.Addr()) {
			return prefix, nil
		}
	}
	return netip.Prefix{}, fmt.Errorf("--iso-pool must be contained by RFC1918 space")
}
```

- [ ] **Step 6: Add the ISO pool summary to `info catch` JSON and text**

Copy `catchrpc.ISOPoolSummary` into both server-info representations and render `ISO pool`, `source`, version, capacity, state counts, and conflict only when configured. Tests must assert JSON backward compatibility when `ISO` is zero and readable text when populated.

- [ ] **Step 7: Run pool, RPC, auth, CLI, and info tests**

Run: `mise exec -- go test ./pkg/catchrpc ./pkg/catch ./pkg/cli ./pkg/yeet -run 'ISOPool|HostSet|Authz|InfoCatch' -count=1`

Expected: PASS; read-only credentials are denied for plan and apply, and explicit pool changes with any allocation state are rejected.

- [ ] **Step 8: Commit pool control and summary**

Run:

```bash
IDS=$(but diff --format json | jq -r --args '$ARGS.positional as $paths | [.changes[] | select(.path as $path | $paths | index($path)) | .id] | join(",")' -- pkg/catch/iso_pool.go pkg/catch/iso_pool_test.go pkg/catch/iso_allocator.go pkg/catchrpc/types.go pkg/catchrpc/types_test.go pkg/catchrpc/client.go pkg/catch/rpc.go pkg/catch/rpc_test.go pkg/catch/authz.go pkg/catch/authz_test.go pkg/cli/cli.go pkg/cli/cli_test.go pkg/yeet/host_set.go pkg/yeet/host_set_test.go pkg/catch/info.go pkg/yeet/rpc_types.go pkg/yeet/info_cmd.go pkg/yeet/info_cmd_test.go)
test -n "$IDS"
but commit codex/iso-network-design -m "iso: add host pool planning and inspection" --changes "$IDS"
```

Expected: one commit with `manage`-protected pool mutation and `read`-visible summary.

### Task 5: Canonical Compose Resolution and Safe-Profile Admission

**Files:**
- Create: `pkg/svc/compose_config.go`
- Create: `pkg/svc/compose_config_test.go`
- Create: `pkg/catch/iso_compose.go`
- Create: `pkg/catch/iso_compose_test.go`
- Create: `pkg/catch/iso_compose_fuzz_test.go`

**Interfaces:**
- Consumes: existing Docker command construction and service root; Docker Compose v2 `config --format json` output.
- Produces: `svc.ResolveComposeJSON(context.Context, svc.ComposeResolveOptions) ([]byte, error)`, `catch.AdmitISOCompose([]byte, ISOComposeAdmissionOptions) (ISOComposeModel, error)`, `ISOComposeModel.Components []string`, and field-path errors such as `services.api.network_mode: ISO does not allow host network mode`.

- [ ] **Step 1: Write command-construction and safe-profile tests**

```go
func TestResolveComposeJSONUsesExactFiles(t *testing.T) {
	var got []string
	opts := ComposeResolveOptions{
		ProjectName: "catch-app",
		ProjectDir:  "/srv/app/data",
		Files:       []string{"/srv/app/run/compose.yml", "/srv/app/run/compose.network"},
		NewCmd: func(_ context.Context, name string, args ...string) *exec.Cmd {
			got = append([]string{name}, args...)
			return helperCommand(t, `{"services":{"api":{"image":"nginx"}}}`)
		},
	}
	if _, err := ResolveComposeJSON(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	want := []string{"docker", "compose", "--project-name", "catch-app", "--project-directory", "/srv/app/data", "--file", "/srv/app/run/compose.yml", "--file", "/srv/app/run/compose.network", "config", "--format", "json"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("command mismatch (-want +got):\n%s", diff)
	}
}
```

```go
func TestAdmitISOCompose(t *testing.T) {
	safe := `{"services":{"api":{"image":"nginx:alpine","command":["nginx","-g","daemon off;"],"environment":{"A":"B"}}}}`
	model, err := AdmitISOCompose([]byte(safe), ISOComposeAdmissionOptions{ServiceRoot: "/srv/app", MaxComponents: 29})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"api"}, model.Components); diff != "" {
		t.Fatal(diff)
	}
	for _, tt := range []struct {
		name string
		raw  string
		path string
	}{
		{name: "ports", raw: `{"services":{"api":{"image":"x","ports":[{"target":80,"published":"8080"}]}}}`, path: "services.api.ports"},
		{name: "host network", raw: `{"services":{"api":{"image":"x","network_mode":"host"}}}`, path: "services.api.network_mode"},
		{name: "privileged", raw: `{"services":{"api":{"image":"x","privileged":true}}}`, path: "services.api.privileged"},
		{name: "build", raw: `{"services":{"api":{"build":{"context":"."}}}}`, path: "services.api.build"},
		{name: "custom DNS", raw: `{"services":{"api":{"image":"x","dns":["8.8.8.8"]}}}`, path: "services.api.dns"},
		{name: "daemon socket", raw: `{"services":{"api":{"image":"x","volumes":[{"type":"bind","source":"/var/run/docker.sock","target":"/docker.sock"}]}}}`, path: "services.api.volumes[0].source"},
		{name: "outside bind", raw: `{"services":{"api":{"image":"x","volumes":[{"type":"bind","source":"/etc","target":"/host"}]}}}`, path: "services.api.volumes[0].source"},
		{name: "external volume", raw: `{"services":{"api":{"image":"x","volumes":[{"type":"volume","source":"shared","target":"/data"}]}},"volumes":{"shared":{"external":true}}}`, path: "volumes.shared.external"},
		{name: "outside secret", raw: `{"services":{"api":{"image":"x","secrets":[{"source":"token","target":"token"}]}},"secrets":{"token":{"file":"/etc/shadow"}}}`, path: "secrets.token.file"},
		{name: "unknown field", raw: `{"services":{"api":{"image":"x","future_escape":true}}}`, path: "services.api.future_escape"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := AdmitISOCompose([]byte(tt.raw), ISOComposeAdmissionOptions{ServiceRoot: "/srv/app", MaxComponents: 29})
			if err == nil || !strings.Contains(err.Error(), tt.path) {
				t.Fatalf("error = %v, want path %q", err, tt.path)
			}
		})
	}
}

func TestAdmitISOComposeAllowsOnlyCanonicalImplicitDefault(t *testing.T) {
	raw := `{"name":"catch-app","networks":{"default":{"name":"catch-app_default","ipam":{}}},"services":{"api":{"image":"nginx","networks":{"default":null}}}}`
	if _, err := AdmitISOCompose([]byte(raw), ISOComposeAdmissionOptions{ProjectName: "catch-app", ServiceRoot: "/srv/app", MaxComponents: 29}); err != nil {
		t.Fatal(err)
	}
	unsafe := `{"name":"catch-app","networks":{"outside":{"name":"shared","external":true}},"services":{"api":{"image":"nginx","networks":{"outside":null}}}}`
	_, err := AdmitISOCompose([]byte(unsafe), ISOComposeAdmissionOptions{ProjectName: "catch-app", ServiceRoot: "/srv/app", MaxComponents: 29})
	if err == nil || !strings.Contains(err.Error(), "networks.outside") {
		t.Fatalf("error = %v, want networks.outside path", err)
	}
}
```

- [ ] **Step 2: Run focused Compose tests and confirm failure**

Run: `mise exec -- go test ./pkg/svc ./pkg/catch -run 'TestResolveComposeJSON|TestAdmitISOCompose' -count=1`

Expected: FAIL because canonical resolution and admission do not exist.

- [ ] **Step 3: Implement reusable canonical resolution**

```go
package svc

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type ComposeResolveOptions struct {
	ProjectName string
	ProjectDir  string
	Files       []string
	NewCmd      func(context.Context, string, ...string) *exec.Cmd
}

func ResolveComposeJSON(ctx context.Context, opts ComposeResolveOptions) ([]byte, error) {
	args := []string{"compose", "--project-name", opts.ProjectName, "--project-directory", opts.ProjectDir}
	for _, file := range opts.Files {
		args = append(args, "--file", file)
	}
	args = append(args, "config", "--format", "json")
	docker, err := DockerCmd()
	if err != nil {
		return nil, err
	}
	newCmd := opts.NewCmd
	if newCmd == nil {
		newCmd = func(ctx context.Context, name string, args ...string) *exec.Cmd {
			return exec.CommandContext(ctx, name, args...)
		}
	}
	cmd := newCmd(ctx, docker, args...)
	cmd.Dir = opts.ProjectDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("resolve Docker Compose application model: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return out, nil
}
```

- [ ] **Step 4: Implement a versioned, fail-closed service-field decoder**

```go
const isoComposeProfileVersion = 1

type ISOComposeAdmissionOptions struct {
	ServiceRoot      string
	ProjectName      string
	MaxComponents    int
	RequireISOOverlay *db.ISOAllocation
}

type ISOComposeModel struct {
	Components []string
}

type isoCanonicalCompose struct {
	Services map[string]json.RawMessage `json:"services"`
	Networks map[string]json.RawMessage `json:"networks"`
	Volumes  map[string]json.RawMessage `json:"volumes"`
	Configs  map[string]json.RawMessage `json:"configs"`
	Secrets  map[string]json.RawMessage `json:"secrets"`
}

var isoAllowedServiceFields = map[string]bool{
	"attach": true, "blkio_config": true, "cap_drop": true, "command": true, "configs": true,
	"container_name": true, "cpu_count": true, "cpu_percent": true, "cpu_period": true,
	"cpu_quota": true, "cpu_rt_period": true, "cpu_rt_runtime": true, "cpu_shares": true,
	"cpus": true, "cpuset": true, "depends_on": true, "deploy": true,
	"entrypoint": true, "env_file": true, "environment": true, "expose": true,
	"extra_hosts": true, "group_add": true, "healthcheck": true,
	"hostname": true, "image": true, "init": true, "labels": true, "logging": true,
	"mac_address": true, "mem_limit": true, "mem_reservation": true, "mem_swappiness": true,
	"memswap_limit": true, "oom_kill_disable": true,
	"oom_score_adj": true, "platform": true, "post_start": true, "pre_stop": true,
	"pids_limit": true, "profiles": true, "pull_policy": true, "read_only": true,
	"restart": true, "scale": true, "secrets": true, "shm_size": true, "stdin_open": true,
	"stop_grace_period": true, "stop_signal": true,
	"sysctls": true, "tmpfs": true, "tty": true, "ulimits": true, "user": true,
	"userns_mode": true, "volumes": true, "working_dir": true,
}

var isoForbiddenServiceFields = map[string]string{
	"build": "Catch-side builds run before the ISO boundary",
	"cap_add": "added capabilities can bypass the runtime boundary",
	"cgroup": "host cgroup namespace sharing is not allowed",
	"cgroup_parent": "host cgroup placement is not allowed",
	"cpu_rt_period": "host realtime scheduling is not allowed",
	"cpu_rt_runtime": "host realtime scheduling is not allowed",
	"devices": "host devices are not allowed",
	"device_cgroup_rules": "device cgroup rules are not allowed",
	"dns": "custom DNS bypasses the ISO resolver",
	"dns_opt": "custom DNS bypasses the ISO resolver",
	"dns_search": "custom DNS search bypasses the ISO resolver",
	"domainname": "custom DNS search is not allowed",
	"external_links": "external links cross the admitted project",
	"ipc": "host or external IPC namespaces are not allowed",
	"links": "links may cross the generated ISO default network",
	"network_mode": "alternate network namespaces are not allowed",
	"pid": "host or external PID namespaces are not allowed",
	"ports": "ISO does not support published ports",
	"privileged": "privileged containers are not allowed",
	"provider": "host-side providers run before the ISO boundary",
	"runtime": "custom OCI runtimes are not allowed",
	"security_opt": "unclassified security options fail closed",
	"storage_opt": "host storage-driver options are not allowed",
	"uts": "host UTS namespace sharing is not allowed",
	"volumes_from": "cross-container volume inheritance is not allowed",
}

func decodeISOService(name string, raw json.RawMessage, opts ISOComposeAdmissionOptions) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("services.%s: %w", name, err)
	}
	for field, value := range fields {
		path := "services." + name + "." + field
		if field == "networks" {
			if err := validateISOServiceNetworks(path, value, name, opts.RequireISOOverlay); err != nil {
				return err
			}
			continue
		}
		if reason, forbidden := isoForbiddenServiceFields[field]; forbidden && !isJSONEmpty(value) {
			return fmt.Errorf("%s: %s", path, reason)
		}
		if !isoAllowedServiceFields[field] && isoForbiddenServiceFields[field] == "" {
			return fmt.Errorf("%s: unknown field in ISO safe profile v%d", path, isoComposeProfileVersion)
		}
	}
	return validateISOAllowedFields(name, fields, opts)
}
```

`AdmitISOCompose` must first decode a `map[string]json.RawMessage` and allow only canonical top-level `name`, `version`, `services`, `networks`, `volumes`, `configs`, and `secrets`; unknown future host-side top-level behavior fails closed. Require canonical top-level `name` to equal `ProjectName`. Then decode `isoCanonicalCompose`, require at least one service, sort names, call `decodeISOService` for each, and reject more than `MaxComponents`. Permit project-scoped named volumes whose canonical `name` is exactly `ProjectName + "_" + volumeKey`; reject `external`, a different custom `name`, `driver`, and `driver_opts`. Permit configs/secrets only when non-external file sources resolve beneath `ServiceRoot`, or when the canonical model carries inline `content`/`environment`; a materialized `name` must follow the same project-scoped rule. With no `RequireISOOverlay`, Compose still materializes one implicit project-scoped `default` network and a `services.NAME.networks.default: null` attachment: allow exactly that shape, with canonical name `ProjectName + "_default"`, no custom driver/options/external/attachable/internal setting, and empty IPAM. With `RequireISOOverlay`, require exactly the `default` top-level network with driver `yeet`, `dev.catchit.mode=iso`, the persisted namespace, the persisted `/27` and gateway, IPv6 disabled, and no external/attachable/internal override; require each service to attach only to `default` with its persisted `ipv4_address`. This is what lets both passes accept Compose's canonical defaults and Yeet's exact overlay without accepting an operator-supplied alternate network.

```go
func AdmitISOCompose(raw []byte, opts ISOComposeAdmissionOptions) (ISOComposeModel, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return ISOComposeModel{}, fmt.Errorf("decode canonical Compose JSON: %w", err)
	}
	allowedTop := map[string]bool{"name": true, "version": true, "services": true, "networks": true, "volumes": true, "configs": true, "secrets": true}
	for field := range top {
		if !allowedTop[field] {
			return ISOComposeModel{}, fmt.Errorf("%s: unknown field in ISO safe profile v%d", field, isoComposeProfileVersion)
		}
	}
	var app isoCanonicalCompose
	if err := json.Unmarshal(raw, &app); err != nil {
		return ISOComposeModel{}, fmt.Errorf("decode canonical Compose JSON: %w", err)
	}
	if len(app.Services) == 0 {
		return ISOComposeModel{}, fmt.Errorf("services: ISO Compose project has no services")
	}
	names := make([]string, 0, len(app.Services))
	for name := range app.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	limit := opts.MaxComponents
	if limit == 0 {
		limit = iso.MaxComponents
	}
	if len(names) > limit {
		return ISOComposeModel{}, fmt.Errorf("services: ISO supports at most %d active components", limit)
	}
	if err := validateISOTopLevelNetworks(app.Networks, opts.ProjectName, opts.RequireISOOverlay); err != nil {
		return ISOComposeModel{}, err
	}
	if err := validateISOProjectVolumes(app.Volumes, opts.ProjectName); err != nil {
		return ISOComposeModel{}, err
	}
	if err := validateISOProjectData("configs", app.Configs, opts.ServiceRoot); err != nil {
		return ISOComposeModel{}, err
	}
	if err := validateISOProjectData("secrets", app.Secrets, opts.ServiceRoot); err != nil {
		return ISOComposeModel{}, err
	}
	for _, name := range names {
		if err := decodeISOService(name, app.Services[name], opts); err != nil {
			return ISOComposeModel{}, err
		}
	}
	return ISOComposeModel{Components: names}, nil
}
```

`validateISOAllowedFields` must parse the allowed-but-constrained fields: reject `deploy.replicas` other than absent/one, `scale` other than absent/one, host user namespace, unconfined seccomp/AppArmor embedded in any accepted representation, host-control socket or namespace-handle bind sources, and bind sources whose `filepath.EvalSymlinks` result is not beneath the resolved service root. Named volumes, tmpfs, configs, and secrets remain allowed. Explicitly validate allowed sysctls against a versioned container-namespace-only allowlist. Reject `annotations`, `develop`, `extends`, custom runtimes, and storage-driver options as unknown or forbidden until individually proven safe.

- [ ] **Step 5: Fuzz canonical JSON decoding and field-path construction**

```go
func FuzzAdmitISOCompose(f *testing.F) {
	f.Add([]byte(`{"services":{"api":{"image":"nginx"}}}`))
	f.Add([]byte(`{"services":{"api":{"network_mode":"host"}}}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = AdmitISOCompose(raw, ISOComposeAdmissionOptions{ServiceRoot: t.TempDir(), MaxComponents: 29})
	})
}
```

- [ ] **Step 6: Run unit and fuzz tests**

Run: `mise exec -- go test ./pkg/svc ./pkg/catch -run 'Compose|ISO' -count=1 && mise exec -- go test ./pkg/catch -run '^$' -fuzz FuzzAdmitISOCompose -fuzztime=10s`

Expected: PASS; malformed JSON never panics, and every rejected service field includes its exact path.

- [ ] **Step 7: Commit canonical Compose admission**

Run:

```bash
IDS=$(but diff --format json | jq -r --args '$ARGS.positional as $paths | [.changes[] | select(.path as $path | $paths | index($path)) | .id] | join(",")' -- pkg/svc/compose_config.go pkg/svc/compose_config_test.go pkg/catch/iso_compose.go pkg/catch/iso_compose_test.go pkg/catch/iso_compose_fuzz_test.go)
test -n "$IDS"
but commit codex/iso-network-design -m "iso: admit a safe canonical Compose profile" --changes "$IDS"
```

Expected: one commit; Compose admission remains unused by normal non-ISO deployments.

### Task 6: ISO Compose Overlay and Docker Network Driver Mode

**Files:**
- Create: `pkg/catch/iso_compose_overlay.go`
- Create: `pkg/catch/iso_compose_overlay_test.go`
- Modify: `pkg/catch/compose_dns.go`
- Modify: `pkg/catch/installer_file.go`
- Modify: `pkg/catch/installer_file_test.go`
- Modify: `pkg/svc/docker.go`
- Modify: `pkg/svc/docker_test.go`
- Modify: `pkg/dnet/dnet.go`
- Modify: `pkg/dnet/dnet_test.go`

**Interfaces:**
- Consumes: admitted `ISOComposeModel`, persisted `db.ISOAllocation`, `svc.ResolveComposeJSON`, existing `ArtifactDockerComposeNetwork`, and Docker generic driver options.
- Produces: `renderISOComposeOverlay(*db.ISOAllocation, ISOComposeModel) (string, error)`; Docker option `dev.catchit.mode=iso`; `db.DockerNetwork.Mode == "iso"`; ISO driver joins without namespace-local NAT/port maps; `svc.ComposeProjectName(string) string`; `DockerComposeService.ResolveConfigJSON(context.Context) ([]byte, error)`.

- [ ] **Step 1: Write overlay and Docker-driver rejection tests**

```go
func TestRenderISOComposeOverlayAssignsOnlyISOAddresses(t *testing.T) {
	allocation := &db.ISOAllocation{
		Project: netip.MustParsePrefix("172.30.128.0/27"),
		Gateway: netip.MustParseAddr("172.30.128.1"),
		NetNS:   "yeet-app-ns",
		DesiredModes: []string{"iso"},
		Components: map[string]db.ISOComponent{
			"api":    {Address: netip.MustParseAddr("172.30.128.2")},
			"worker": {Address: netip.MustParseAddr("172.30.128.3")},
		},
	}
	raw, err := renderISOComposeOverlay(allocation, ISOComposeModel{Components: []string{"api", "worker"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"driver: yeet", "dev.catchit.mode: iso", "dev.catchit.netns: /var/run/netns/yeet-app-ns",
		"subnet: 172.30.128.0/27", "gateway: 172.30.128.1", "ipv4_address: 172.30.128.2",
		"dns:", "- 172.30.128.1",
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("overlay missing %q:\n%s", want, raw)
		}
	}
}

func TestRenderISOComposeOverlayUsesQuad100ForTailscale(t *testing.T) {
	allocation := testISOContainerAllocation()
	allocation.DesiredModes = []string{"iso", "ts"}
	raw, err := renderISOComposeOverlay(allocation, ISOComposeModel{Components: []string{"api"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "- 100.100.100.100") {
		t.Fatalf("overlay does not use Tailscale DNS:\n%s", raw)
	}
}
```

```go
func TestISODriverRejectsPortMapsAndSkipsNamespaceNAT(t *testing.T) {
	p := newTestPlugin(t)
	networkID := createTestNetwork(t, p, `{"com.docker.network.generic":{"dev.catchit.netns":"/var/run/netns/yeet-app-ns","dev.catchit.mode":"iso"}}`)
	endpointID := createTestEndpoint(t, p, networkID, "172.30.128.2/27")
	resp := callJoin(t, p, networkID, endpointID, []portMap{{Proto: 6, Port: 8080}})
	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "ISO network does not support port maps") {
		t.Fatalf("response = %d %s", resp.Code, resp.Body.String())
	}
	resp = callJoin(t, p, networkID, endpointID, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("response = %d %s", resp.Code, resp.Body.String())
	}
	assertCommandNotRun(t, p, "iptables", "-t", "nat")
}
```

- [ ] **Step 2: Run focused overlay and driver tests and confirm failure**

Run: `mise exec -- go test ./pkg/catch ./pkg/svc ./pkg/dnet -run 'TestRenderISOComposeOverlay|TestISODriver' -count=1`

Expected: FAIL because the ISO overlay and driver mode do not exist.

- [ ] **Step 3: Render a complete, exclusive ISO overlay**

```go
type isoComposeOverlay struct {
	Services map[string]isoComposeOverlayService `yaml:"services"`
	Networks map[string]isoComposeOverlayNetwork `yaml:"networks"`
}

type isoComposeOverlayService struct {
	Networks map[string]isoComposeServiceNetwork `yaml:"networks"`
	DNS      []string                            `yaml:"dns"`
}

type isoComposeServiceNetwork struct {
	IPv4Address string `yaml:"ipv4_address"`
}

type isoComposeOverlayNetwork struct {
	Driver     string            `yaml:"driver"`
	DriverOpts map[string]string `yaml:"driver_opts"`
	EnableIPv6 bool              `yaml:"enable_ipv6"`
	IPAM       isoComposeIPAM    `yaml:"ipam"`
}

type isoComposeIPAM struct {
	Config []isoComposeIPAMConfig `yaml:"config"`
}

type isoComposeIPAMConfig struct {
	Subnet  string `yaml:"subnet"`
	Gateway string `yaml:"gateway"`
}

func renderISOComposeOverlay(allocation *db.ISOAllocation, model ISOComposeModel) (string, error) {
	if allocation == nil || !allocation.Project.IsValid() || !allocation.Gateway.IsValid() {
		return "", fmt.Errorf("ISO container allocation is incomplete")
	}
	resolver := allocation.Gateway.String()
	if slices.Contains(allocation.DesiredModes, "ts") {
		resolver = tailscaleDNSIP
	}
	overlay := isoComposeOverlay{
		Services: map[string]isoComposeOverlayService{},
		Networks: map[string]isoComposeOverlayNetwork{
			"default": {
				Driver: "yeet",
				DriverOpts: map[string]string{
					"dev.catchit.netns": filepath.Join("/var/run/netns", allocation.NetNS),
					"dev.catchit.mode":  "iso",
				},
				EnableIPv6: false,
				IPAM: isoComposeIPAM{Config: []isoComposeIPAMConfig{{
					Subnet: allocation.Project.String(), Gateway: allocation.Gateway.String(),
				}}},
			},
		},
	}
	for _, name := range model.Components {
		component, ok := allocation.Components[name]
		if !ok || !component.Address.IsValid() {
			return "", fmt.Errorf("ISO component %q has no reserved address", name)
		}
		overlay.Services[name] = isoComposeOverlayService{
			Networks: map[string]isoComposeServiceNetwork{"default": {IPv4Address: component.Address.String()}},
			DNS:      []string{resolver},
		}
	}
	raw, err := yaml.Marshal(overlay)
	if err != nil {
		return "", fmt.Errorf("marshal ISO Compose overlay: %w", err)
	}
	return string(raw), nil
}
```

ISO rendering must bypass the existing `svc` DNS overlay path: one deployment writes either the ISO overlay or the current `svc`/`lan`/`ts` overlay, never both.

- [ ] **Step 4: Make canonical resolution available on the exact service generation**

```go
func (s *DockerComposeService) ResolveConfigJSON(ctx context.Context) ([]byte, error) {
	args, err := s.composeCommandArgs()
	if err != nil {
		return nil, err
	}
	files := composeFilesFromArgs(args)
	return ResolveComposeJSON(ctx, ComposeResolveOptions{
		ProjectName: s.composeProjectName(),
		ProjectDir:  s.DataDir,
		Files:       files,
		NewCmd:      s.NewCmdContext,
	})
}

func ComposeProjectName(service string) string {
	return dockerContainerNamePrefix + "-" + service
}

func composeFilesFromArgs(args []string) []string {
	var files []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--file" {
			files = append(files, args[i+1])
			i++
		}
	}
	return files
}
```

Change `DockerComposeService.projectName` to delegate to exported `ComposeProjectName` so installer admission and runtime inspection use exactly the same project identity.

- [ ] **Step 5: Persist and enforce Docker driver mode**

Extend `createNetworkRequest.Options.Generic` with:

```go
Mode string `json:"dev.catchit.mode"`
```

Accept only empty mode or `iso`; persist it in `db.DockerNetwork.Mode`. Add `mode string` to `joinNetworkState`. Reject any `CreateEndpoint`, `Join`, `ProgramExternalConnectivity`, or restored DB state that carries a port map when mode is `iso`. In `configureJoinedNetwork`, retain bridge creation/attachment but return immediately after bringing the interface up for ISO:

```go
func (p *plugin) configureJoinedNetwork(join joinNetworkState) error {
	run := p.commandRunner()
	if err := ensureBridgeWithRunner(join.gatewayPrefix, run); err != nil {
		return err
	}
	if err := run("ip", "link", "set", join.ifName, "master", "br0"); err != nil {
		return err
	}
	if err := run("ip", "link", "set", join.ifName, "up"); err != nil {
		return err
	}
	if join.mode == "iso" {
		return nil
	}
	if err := ensurePostroutingChainWithRunner(run); err != nil {
		return err
	}
	desired, err := p.currentPortForwardsForNetNS(join.netns)
	if err != nil {
		return err
	}
	return syncNetNSPortForwards(join.netns, desired, p.natBackend())
}
```

Host-root ISO policy in Task 7 owns NAT; the driver must never masquerade ISO traffic inside the service namespace.

- [ ] **Step 6: Run overlay, service, and driver tests**

Run: `mise exec -- go test ./pkg/catch ./pkg/svc ./pkg/dnet -run 'ComposeOverlay|ResolveConfigJSON|ISODriver|PortMap' -count=1`

Expected: PASS; existing non-ISO dnet NAT and port-forward tests remain unchanged and green.

- [ ] **Step 7: Commit overlay and driver mode**

Run:

```bash
IDS=$(but diff --format json | jq -r --args '$ARGS.positional as $paths | [.changes[] | select(.path as $path | $paths | index($path)) | .id] | join(",")' -- pkg/catch/iso_compose_overlay.go pkg/catch/iso_compose_overlay_test.go pkg/catch/compose_dns.go pkg/catch/installer_file.go pkg/catch/installer_file_test.go pkg/svc/docker.go pkg/svc/docker_test.go pkg/dnet/dnet.go pkg/dnet/dnet_test.go)
test -n "$IDS"
but commit codex/iso-network-design -m "iso: add routed Compose network overlay" --changes "$IDS"
```

Expected: one commit; no workload can use the mode until Task 7 provides the verified host boundary.

### Task 7: Host Firewall Policy and Routed Namespace Topology

**Files:**
- Create: `pkg/netns/iso_firewall.go`
- Create: `pkg/netns/iso_firewall_test.go`
- Create: `pkg/netns/iso_topology.go`
- Create: `pkg/netns/iso_topology_test.go`
- Create: `pkg/catch/iso_runtime.go`
- Create: `pkg/catch/iso_runtime_test.go`
- Modify: `pkg/catch/catch.go`
- Modify: `cmd/catch/catch.go`
- Modify: `cmd/catch/catch_test.go`
- Modify: `pkg/svc/systemd.go`
- Modify: `pkg/svc/systemd_test.go`

**Interfaces:**
- Consumes: `netns.FirewallBackend`, `iso.NonPublicIPv4Prefixes`, persisted pool/allocations, Linux `ip`, `sysctl`, `nft`, `iptables-restore`, `ip6tables-restore`, and `ipset`.
- Produces: `netns.ISOEndpoint`, `netns.ISOPolicySpec`, `netns.RenderISOPolicy`, `netns.EnsureISOPolicy`, `netns.VerifyISOPolicy`, `netns.EnsureISOTopology`, `netns.RemoveISOTopology`; `Server.EnsureISONetwork(context.Context, string) error`, `Server.CleanISONetwork(context.Context, string) error`; package wrappers `EnsureISONetwork(context.Context, *Config, string) error` and `CleanISONetwork(context.Context, *Config, string) error`; local Catch commands `iso-network-ensure` and `iso-network-clean`.

- [ ] **Step 1: Write backend-equivalence, rule-order, and topology-command tests**

```go
func TestRenderISOPolicyOrdersSecurityDecisions(t *testing.T) {
	spec := ISOPolicySpec{
		Pool:    netip.MustParsePrefix("172.30.0.0/16"),
		DNSPort: 5353,
		Endpoints: []ISOEndpoint{{
			Interface: "yi-a1b2c3", Link: netip.MustParsePrefix("172.30.0.0/30"),
			PeerIP: netip.MustParseAddr("172.30.0.2"), Project: netip.MustParsePrefix("172.30.128.0/27"),
		}},
	}
	for _, backend := range []FirewallBackend{BackendNFT, BackendIPTablesNFT, BackendIPTablesLegacy} {
		rules, err := RenderISOPolicy(backend, spec)
		if err != nil {
			t.Fatal(err)
		}
		wantDecisions := []string{
			"drop-invalid", "drop-spoof", "accept-established", "accept-iso-dns",
			"reject-new-host-access", "reject-direct-dns", "reject-non-public",
			"accept-public", "reject-rest", "masquerade-public", "drop-ipv6",
		}
		if diff := cmp.Diff(wantDecisions, rules.Decisions); diff != "" {
			t.Fatalf("%s decision order (-want +got):\n%s", backend, diff)
		}
		assertContainsAll(t, rules.IPv4, "172.30.128.0/27", "100.64.0.0/10", "169.254.0.0/16", "198.18.0.0/15")
		if !strings.Contains(rules.IPv6, "drop") {
			t.Fatalf("%s has no IPv6 drop", backend)
		}
	}
}

func TestISOTopologyCommandsRouteProjectThroughPeer(t *testing.T) {
	spec := ISOTopologySpec{
		Pool: netip.MustParsePrefix("172.30.0.0/16"),
		Allocation: db.ISOAllocation{
			Link: netip.MustParsePrefix("172.30.0.0/30"), HostIP: netip.MustParseAddr("172.30.0.1"),
			PeerIP: netip.MustParseAddr("172.30.0.2"), Project: netip.MustParsePrefix("172.30.128.0/27"),
			Interface: "yi-a1b2c3", PeerInterface: "yo-a1b2c3", NetNS: "yeet-app-ns",
		},
	}
	commands, err := ISOTopologyEnsureCommands(spec)
	if err != nil {
		t.Fatal(err)
	}
	assertCommand(t, commands, "ip", "route", "replace", "blackhole", "172.30.0.0/16", "metric", "42760")
	assertCommand(t, commands, "ip", "route", "replace", "172.30.128.0/27", "via", "172.30.0.2", "dev", "yi-a1b2c3")
	assertNetNSCommand(t, commands, "yeet-app-ns", "ip", "route", "replace", "default", "via", "172.30.0.1", "dev", "yo-a1b2c3")
}
```

- [ ] **Step 2: Run policy/topology tests and confirm failure**

Run: `mise exec -- go test ./pkg/netns ./pkg/catch -run 'TestRenderISOPolicy|TestISOTopology|TestEnsureISONetwork' -count=1`

Expected: FAIL because the policy and topology APIs do not exist.

- [ ] **Step 3: Define one backend-neutral policy specification**

```go
type ISOEndpoint struct {
	Interface string
	Link      netip.Prefix
	PeerIP    netip.Addr
	Project   netip.Prefix
	Tailscale bool
}

type ISOPolicySpec struct {
	Pool      netip.Prefix
	DNSPort   uint16
	Endpoints []ISOEndpoint
}

type ISOPolicyRules struct {
	Backend FirewallBackend
	IPv4   string
	IPv6   string
	IPSet  string
	Decisions []string
	Digest string
}

func RenderISOPolicy(backend FirewallBackend, spec ISOPolicySpec) (ISOPolicyRules, error) {
	if !spec.Pool.IsValid() || !spec.Pool.Addr().Is4() || spec.Pool.Bits() != 16 {
		return ISOPolicyRules{}, fmt.Errorf("ISO policy requires an IPv4 /16")
	}
	if spec.DNSPort == 0 {
		return ISOPolicyRules{}, fmt.Errorf("ISO DNS port is required")
	}
	sort.Slice(spec.Endpoints, func(i, j int) bool { return spec.Endpoints[i].Interface < spec.Endpoints[j].Interface })
	var rules ISOPolicyRules
	var err error
	switch backend {
	case BackendNFT:
		rules, err = renderNFTISOPolicy(spec)
	case BackendIPTablesNFT, BackendIPTablesLegacy:
		rules, err = renderIPTablesISOPolicy(backend, spec)
	default:
		err = fmt.Errorf("unsupported ISO firewall backend %q", backend)
	}
	if err != nil {
		return ISOPolicyRules{}, err
	}
	rules.Backend = backend
	rules.Decisions = []string{
		"drop-invalid", "drop-spoof", "accept-established", "accept-iso-dns",
		"reject-new-host-access", "reject-direct-dns", "reject-non-public",
		"accept-public", "reject-rest", "masquerade-public", "drop-ipv6",
	}
	rules.Digest = digestISOPolicy(rules)
	return rules, nil
}
```

Both renderers must derive from `spec` and `iso.NonPublicIPv4Prefixes(spec.Pool)`. Render these semantics in this exact order:

1. raw/mangle anti-spoof checks bind each interface to its peer `/32` and optional project `/27`, then silently drop invalid sources;
2. INPUT accepts only `ESTABLISHED,RELATED` and redirected ISO DNS from ISO interfaces, then rejects new host access;
3. FORWARD from an ISO interface accepts `ESTABLISHED,RELATED`, rejects direct ports 53 and 853, rejects every non-public destination, accepts the remaining IPv4, then rejects all other ISO forwarding;
4. FORWARD toward an ISO interface accepts only `ESTABLISHED,RELATED`, preventing LAN/`svc`/other forwarded ingress;
5. locally generated OUTPUT to live ISO prefixes is unrestricted;
6. POSTROUTING masquerades only ISO-pool sources whose destination is not in the vendored non-public set;
7. IPv6 INPUT/FORWARD from ISO interfaces drops;
8. PREROUTING redirects ISO-interface TCP/UDP destination port 53 to local port 5353 before INPUT.

Use native nft interval sets/maps. For both iptables backends, require `ipset` and populate `hash:net,iface` source sets plus a `hash:net` destination set before atomic `iptables-restore`/`ip6tables-restore`. A missing `ipset`, failed restore, or digest mismatch is fatal.

- [ ] **Step 4: Implement idempotent policy apply and verification**

```go
func EnsureISOPolicy(ctx context.Context, rules ISOPolicyRules) error {
	if err := applyISOIPSets(ctx, rules); err != nil {
		return err
	}
	if err := applyISOIPv4(ctx, rules); err != nil {
		return err
	}
	if err := applyISOIPv6(ctx, rules); err != nil {
		return err
	}
	return VerifyISOPolicy(ctx, rules)
}

func VerifyISOPolicy(ctx context.Context, want ISOPolicyRules) error {
	live, err := readLiveISOPolicy(ctx, want.Backend)
	if err != nil {
		return err
	}
	if got := digestISOPolicy(live); got != want.Digest {
		return fmt.Errorf("ISO firewall policy digest mismatch: got %s want %s", got, want.Digest)
	}
	return nil
}
```

Install into dedicated, Yeet-owned chains/table names so ordinary host firewall state is not flushed. Use atomic table/chain replacement and clean stale ISO interface entries by rendering desired state from persisted allocations every time.

- [ ] **Step 5: Implement namespace, veth, route, and sysctl topology helpers**

```go
type ISOTopologySpec struct {
	Pool       netip.Prefix
	Allocation db.ISOAllocation
	TailscaleInterface string
}

func EnsureISOTopology(ctx context.Context, spec ISOTopologySpec) error {
	commands, err := ISOTopologyEnsureCommands(spec)
	if err != nil {
		return err
	}
	for _, command := range commands {
		if err := command.Run(ctx); err != nil {
			return fmt.Errorf("ensure ISO topology: %w", err)
		}
	}
	return VerifyISOTopology(ctx, spec)
}

func RemoveISOTopology(ctx context.Context, spec ISOTopologySpec) error {
	for _, command := range ISOTopologyRemoveCommands(spec) {
		if err := command.Run(ctx); err != nil && !command.NotFound(err) {
			return fmt.Errorf("remove ISO topology: %w", err)
		}
	}
	return VerifyISOTopologyAbsent(ctx, spec)
}
```

The ensure command model must: replace the aggregate blackhole route; create the named namespace; create the root/peer veth pair; assign host `.1/30` and peer `.2/30`; enable IPv4 forwarding only in the service router; disable IPv6 on root/peer/bridge paths; install the root `/27` route through `.2`; install the namespace default route through `.1`; enable strict reverse-path/source checks compatible with the routed `/27`; and add namespace DNAT from project-gateway TCP/UDP 53 to host-link `.1:53`. Docker creates `br0` with the project gateway later. VM allocations skip the namespace and project route and use the TAP adapter in Task 10.

Install and verify a small policy inside the router namespace too: accept established replies; accept root-peer traffic forwarded to the project; accept project traffic forwarded to the outer peer; reject new traffic to the router namespace itself except the DNS DNAT; bind `br0` sources to the project `/27`; and drop all IPv6. When `TailscaleInterface` is non-empty, also accept project-to-`ts0` and `ts0`-to-project forwarding. Do not add a CGNAT exception on the outer peer—the Tailscale route must keep that traffic on `ts0`, and a routing failure must hit the root policy's CGNAT rejection.

- [ ] **Step 6: Order policy verification before link attachment in Catch**

```go
func (s *Server) EnsureISONetwork(ctx context.Context, service string) error {
	dv, err := s.cfg.DB.Get()
	if err != nil {
		return err
	}
	spec, err := s.isoRuntimeSpec(dv, service)
	if err != nil {
		return err
	}
	policy, err := netns.RenderISOPolicy(spec.Backend, spec.Policy)
	if err != nil {
		return err
	}
	if err := netns.EnsureISOPolicy(ctx, policy); err != nil {
		_ = s.markISOState(service, string(iso.StateQuarantined), err)
		return err
	}
	if !spec.VM {
		if err := netns.EnsureISOTopology(ctx, spec.Topology); err != nil {
			_ = s.markISOState(service, string(iso.StateQuarantined), err)
			return err
		}
	}
	if _, err := s.cfg.DB.MutateData(func(data *db.Data) error {
		if data.ISOPool != nil {
			data.ISOPool.AggregateRouteState = "ready"
			data.ISOPool.LastConflict = ""
		}
		return nil
	}); err != nil {
		_ = s.markISOState(service, string(iso.StateQuarantined), err)
		return err
	}
	return s.markISOState(service, string(iso.StateReady), nil)
}

func EnsureISONetwork(ctx context.Context, cfg *Config, service string) error {
	if cfg == nil || cfg.DB == nil {
		return fmt.Errorf("ISO network requires a config DB")
	}
	server := &Server{cfg: *cfg}
	return server.EnsureISONetwork(ctx, service)
}
```

`isoRuntimeSpec` must render global policy from every reserved/ready/stopped/removing/quarantined/tombstoned allocation so a single service ensure cannot remove another service's entries. The sole exclusion is an allocation with both `RemoveRequested` and `CleanupVerified`, after topology absence is already durable. Do not create the veth until the global policy verifies successfully. On aggregate route or pool-conflict failure, persist `AggregateRouteState="conflict"` and the exact `LastConflict` while quarantining affected services; clear those fields only after the aggregate and every more-specific route verify.

- [ ] **Step 7: Add local-only Catch helpers and systemd dependency**

Add hidden local command handlers in `cmd/catch`:

```go
case "iso-network-ensure":
	return catch.EnsureISONetwork(ctx, cfg, requireSingleServiceArg(args))
case "iso-network-clean":
	return catch.CleanISONetwork(ctx, cfg, requireSingleServiceArg(args))
```

Render the container auxiliary unit with:

```ini
[Unit]
Description=yeet ISO network for SERVICE
Before=SERVICE.service
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=CATCH -data-dir DATA_DIR iso-network-ensure SERVICE
ExecStop=CATCH -data-dir DATA_DIR iso-network-clean SERVICE
```

Use existing `ArtifactNetNSService` and `ArtifactNetNSEnv` lifecycle plumbing. These local helpers are not registered as remote TTY/RPC operations; service install/start/remove remains the `manage` boundary.

- [ ] **Step 8: Run policy, topology, command-routing, and systemd tests**

Run: `mise exec -- go test ./pkg/netns ./pkg/catch ./pkg/svc ./cmd/catch -run 'ISO|NetNS|Systemd' -count=1`

Expected: PASS; failure injection proves topology commands are never called when policy application or verification fails.

- [ ] **Step 9: Commit the fail-closed host boundary**

Run:

```bash
IDS=$(but diff --format json | jq -r --args '$ARGS.positional as $paths | [.changes[] | select(.path as $path | $paths | index($path)) | .id] | join(",")' -- pkg/netns/iso_firewall.go pkg/netns/iso_firewall_test.go pkg/netns/iso_topology.go pkg/netns/iso_topology_test.go pkg/catch/iso_runtime.go pkg/catch/iso_runtime_test.go pkg/catch/catch.go cmd/catch/catch.go cmd/catch/catch_test.go pkg/svc/systemd.go pkg/svc/systemd_test.go)
test -n "$IDS"
but commit codex/iso-network-design -m "iso: install verified host network policy" --changes "$IDS"
```

Expected: one commit with backend-equivalent renderers and policy-before-attachment ordering.

### Task 8: Dedicated ISO DNS and `iso,ts` Resolver Routing

**Files:**
- Create: `pkg/catch/iso_dns.go`
- Create: `pkg/catch/iso_dns_test.go`
- Create: `pkg/catch/iso_dns_server.go`
- Create: `pkg/catch/iso_dns_server_test.go`
- Create: `pkg/catch/iso_dns_service.go`
- Create: `pkg/catch/iso_dns_service_test.go`
- Modify: `pkg/catch/catch.go`
- Modify: `pkg/catch/tsns.go`
- Modify: `pkg/catch/tsns_test.go`
- Modify: `pkg/catch/installer_file.go`
- Modify: `pkg/catch/installer_file_test.go`

**Interfaces:**
- Consumes: `miekg/dns`, host resolver forwarding, `iso.IsPublicIPv4`, ISO pool state, systemd unit patterns, and existing service-owned tailscaled namespace behavior.
- Produces: `newISODNSHandler(isoDNSPoolStore, dnsForwardFunc) dns.Handler`; `RunISODNSServer(context.Context, *Config) error`; a `yeet-iso-dns.service` listener on TCP and UDP `0.0.0.0:5353`; `tailscaleNetNSMode` support for ISO router namespaces.

- [ ] **Step 1: Write public-only DNS filtering and refusal tests**

```go
func TestISODNSRefusesLocalAndFiltersAddresses(t *testing.T) {
	pool := netip.MustParsePrefix("172.30.0.0/16")
	h := newISODNSHandler(staticISODNSPoolStore{pool: pool}, func(_ context.Context, req *dns.Msg) (*dns.Msg, error) {
		resp := new(dns.Msg)
		resp.SetReply(req)
		resp.Answer = []dns.RR{
			&dns.A{Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET}, A: net.ParseIP("1.1.1.1")},
			&dns.A{Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET}, A: net.ParseIP("10.0.0.1")},
			&dns.AAAA{Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET}, AAAA: net.ParseIP("2606:4700:4700::1111")},
		}
		return resp, nil
	})
	for _, name := range []string{"api", "api.yeet.internal.", "2.0.0.30.172.in-addr.arpa."} {
		resp := serveDNSMessage(t, h, name, dns.TypeA)
		if resp.Rcode != dns.RcodeRefused {
			t.Fatalf("%s rcode = %s", name, dns.RcodeToString[resp.Rcode])
		}
	}
	resp := serveDNSMessage(t, h, "example.com.", dns.TypeA)
	if len(resp.Answer) != 1 || resp.Answer[0].(*dns.A).A.String() != "1.1.1.1" {
		t.Fatalf("answers = %#v", resp.Answer)
	}
}

func TestISODNSFiltersSVCBAndHTTPSHints(t *testing.T) {
	rr, err := dns.NewRR("example.com. 60 IN HTTPS 1 . ipv4hint=1.1.1.1,10.0.0.1 ipv6hint=2606:4700:4700::1111")
	if err != nil {
		t.Fatal(err)
	}
	filtered := filterISODNSRecords([]dns.RR{rr}, netip.MustParsePrefix("172.30.0.0/16"))
	raw := filtered[0].String()
	if !strings.Contains(raw, "ipv4hint=1.1.1.1") || strings.Contains(raw, "10.0.0.1") || strings.Contains(raw, "ipv6hint") {
		t.Fatalf("filtered HTTPS = %s", raw)
	}
}
```

- [ ] **Step 2: Run DNS tests and confirm failure**

Run: `mise exec -- go test ./pkg/catch -run 'TestISODNS' -count=1`

Expected: FAIL because the ISO handler and service do not exist.

- [ ] **Step 3: Implement the public-only handler**

```go
type isoDNSPoolStore interface {
	ISOPool(context.Context) (netip.Prefix, error)
}

type isoDNSHandler struct {
	store   isoDNSPoolStore
	forward dnsForwardFunc
}

func newISODNSHandler(store isoDNSPoolStore, forward dnsForwardFunc) dns.Handler {
	if forward == nil {
		forward = forwardDNSViaHostResolver
	}
	return &isoDNSHandler{store: store, forward: forward}
}

func (h *isoDNSHandler) responseFor(req *dns.Msg) *dns.Msg {
	if len(req.Question) != 1 {
		resp := new(dns.Msg)
		resp.SetRcode(req, dns.RcodeFormatError)
		return resp
	}
	q := req.Question[0]
	if isYeetInternalDNSName(q.Name) || isShortYeetDNSCandidate(q.Name) || isISOReverseName(q.Name) {
		resp := new(dns.Msg)
		resp.SetRcode(req, dns.RcodeRefused)
		return resp
	}
	pool, err := h.store.ISOPool(context.Background())
	if err != nil {
		resp := new(dns.Msg)
		resp.SetRcode(req, dns.RcodeServerFailure)
		return resp
	}
	resp, err := h.forward(context.Background(), req)
	if err != nil || resp == nil {
		out := new(dns.Msg)
		out.SetRcode(req, dns.RcodeServerFailure)
		return out
	}
	resp.Answer = filterISODNSRecords(resp.Answer, pool)
	resp.Ns = filterISODNSRecords(resp.Ns, pool)
	resp.Extra = filterISODNSRecords(resp.Extra, pool)
	return resp
}
```

`filterISODNSRecords` must clone records before mutation, remove all AAAA records, remove A records for which `iso.IsPublicIPv4` is false, remove IPv6 SVCB/HTTPS hints, and retain only public IPv4 hints. Do not strip CNAME, TXT, MX, NS, SOA, or DNSSEC records merely because they contain names; the packet policy remains authoritative for eventual addresses.

- [ ] **Step 4: Run TCP and UDP listeners in one systemd service**

```go
const isoDNSListenAddr = "0.0.0.0:5353"

func RunISODNSServer(ctx context.Context, cfg *Config) error {
	handler := newISODNSHandler(newConfigISODNSPoolStore(cfg), nil)
	servers := []*dns.Server{
		{Addr: isoDNSListenAddr, Net: "udp", Handler: handler, ReadTimeout: yeetDNSReadTimeout, WriteTimeout: yeetDNSWriteTimeout},
		{Addr: isoDNSListenAddr, Net: "tcp", Handler: handler, ReadTimeout: yeetDNSReadTimeout, WriteTimeout: yeetDNSWriteTimeout},
	}
	errCh := make(chan error, len(servers))
	for _, server := range servers {
		go func(server *dns.Server) { errCh <- server.ListenAndServe() }(server)
	}
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return errors.Join(servers[0].ShutdownContext(shutdownCtx), servers[1].ShutdownContext(shutdownCtx))
	case err := <-errCh:
		return err
	}
}
```

Install `yeet-iso-dns.service` beside the existing Yeet DNS service. The unit runs `catch -data-dir DATA_DIR iso-dns`; Catch startup must install/start it before an ISO policy can verify. The listener may bind wildcard port 5353 because firewall INPUT permits it only after the port-53 ISO redirect and rejects direct workload access to other host ports.

- [ ] **Step 5: Route `iso,ts` through the service-owned namespace**

Extend `FileInstaller.tailscaleNetNSMode` so an ISO allocation returns its persisted router namespace. Preserve `AcceptDNS=false` for the namespace daemon, but set the ISO Compose overlay resolver to `100.100.100.100`; traffic to Quad100 then follows that service namespace's `ts0` identity. Add tests proving ordinary default remains the ISO peer route, tailnet/CGNAT routes use `ts0`, and VM `iso,ts` remains rejected.

```go
func (i *FileInstaller) tailscaleNetNSMode(env *netns.Service) (runTSInNetNS string, netnsResolvConf string, tapMode bool) {
	if i.tsNet == nil {
		return "", "", false
	}
	if i.isoAllocation != nil {
		return i.isoAllocation.NetNS, "", false
	}
	tapMode = i.svcNet == nil && i.macvlan == nil
	if tapMode {
		env.TailscaleTAPInterface = i.tsNet.Interface
		return "", tailscaledResolvConf, true
	}
	return env.NetNS(), "", false
}
```

- [ ] **Step 6: Add DNS fuzz coverage**

```go
func FuzzFilterISODNSMessage(f *testing.F) {
	seed := new(dns.Msg)
	seed.SetQuestion("example.com.", dns.TypeA)
	raw, _ := seed.Pack()
	f.Add(raw)
	f.Fuzz(func(t *testing.T, raw []byte) {
		var msg dns.Msg
		if err := msg.Unpack(raw); err != nil {
			return
		}
		msg.Answer = filterISODNSRecords(msg.Answer, netip.MustParsePrefix("172.30.0.0/16"))
		_, _ = msg.Pack()
	})
}
```

- [ ] **Step 7: Run DNS, Tailscale, and fuzz tests**

Run: `mise exec -- go test ./pkg/catch -run 'ISODNS|ISO.*Tailscale|Tailscale.*ISO' -count=1 && mise exec -- go test ./pkg/catch -run '^$' -fuzz FuzzFilterISODNSMessage -fuzztime=10s`

Expected: PASS; no private/IPv6 address survives the tested answer forms, and `iso,ts` keeps public default egress on ISO.

- [ ] **Step 8: Commit DNS and Tailscale routing**

Run:

```bash
IDS=$(but diff --format json | jq -r --args '$ARGS.positional as $paths | [.changes[] | select(.path as $path | $paths | index($path)) | .id] | join(",")' -- pkg/catch/iso_dns.go pkg/catch/iso_dns_test.go pkg/catch/iso_dns_server.go pkg/catch/iso_dns_server_test.go pkg/catch/iso_dns_service.go pkg/catch/iso_dns_service_test.go pkg/catch/catch.go pkg/catch/tsns.go pkg/catch/tsns_test.go pkg/catch/installer_file.go pkg/catch/installer_file_test.go)
test -n "$IDS"
but commit codex/iso-network-design -m "iso: add public-only DNS and tailscale routing" --changes "$IDS"
```

Expected: one commit with the dedicated resolver and explicit Tailscale exception.

### Task 9: Container Admission, Start Verification, Reconciliation, and Cleanup

**Files:**
- Modify: `pkg/catch/installer_file.go`
- Modify: `pkg/catch/installer_file_test.go`
- Modify: `pkg/catch/installer_service.go`
- Modify: `pkg/catch/catch.go`
- Modify: `pkg/catch/catch_test.go`
- Modify: `pkg/catch/remove_test.go`
- Modify: `pkg/catch/recovery_vm.go`
- Modify: `pkg/catch/recovery_vm_test.go`
- Create: `pkg/svc/iso_inspect.go`
- Create: `pkg/svc/iso_inspect_test.go`
- Modify: `pkg/svc/docker.go`
- Modify: `pkg/svc/docker_test.go`
- Modify: `pkg/catch/iso_runtime.go`
- Modify: `pkg/catch/iso_runtime_test.go`

**Interfaces:**
- Consumes: Tasks 2, 5, 6, 7, and 8; current installer staging/generation paths; Docker Compose lifecycle; Catch startup reconciliation and remove flow.
- Produces: `svc.InspectISOProject(context.Context, svc.ISOInspectOptions) (svc.ISOInspection, error)`; `Server.reconcileISONetworks(context.Context) error`; two-pass admission integrated before pull/start; verified cleanup before DB deletion; fresh clone allocation.

- [ ] **Step 1: Write ordering, drift quarantine, and cleanup tombstone tests**

```go
func TestISOInstallOrdersAdmissionPolicyAndWorkload(t *testing.T) {
	recorder := newISOInstallRecorder(t)
	installer := recorder.Installer()
	if err := installer.InstallISO(context.Background(), testISOComposeRequest()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"resolve-base", "admit-base", "reserve", "render-overlay", "resolve-merged", "admit-merged",
		"install-dns", "ensure-policy", "verify-policy", "attach", "compose-up", "inspect-runtime", "mark-ready",
	}
	if diff := cmp.Diff(want, recorder.Events()); diff != "" {
		t.Fatalf("order mismatch (-want +got):\n%s", diff)
	}
}

func TestISOInstallNeverStartsAfterPolicyFailure(t *testing.T) {
	recorder := newISOInstallRecorder(t)
	recorder.PolicyError = errors.New("nft verify failed")
	err := recorder.Installer().InstallISO(context.Background(), testISOComposeRequest())
	if err == nil {
		t.Fatal("expected failure")
	}
	if slices.Contains(recorder.Events(), "compose-up") {
		t.Fatalf("workload started after failure: %#v", recorder.Events())
	}
	assertPersistedISOState(t, recorder.Server(), "app", iso.StateQuarantined)
}

func TestRemoveISOLeavesTombstoneWhenCleanupCannotVerify(t *testing.T) {
	server := newISORemoveTestServer(t)
	server.isoCleanup = func(context.Context, string) error { return errors.New("endpoint still attached") }
	err := server.RemoveServiceWithOptions(context.Background(), "app", RemoveServiceOptions{})
	if err == nil {
		t.Fatal("expected cleanup error")
	}
	service := mustService(t, server, "app")
	if service.ISO.State != string(iso.StateTombstoned) || !strings.Contains(service.ISO.LastError, "endpoint still attached") {
		t.Fatalf("ISO state = %#v", service.ISO)
	}
}

func TestTransitionAwayFromISOReleasesOnlyAfterCleanup(t *testing.T) {
	recorder := newISOTransitionRecorder(t)
	if err := recorder.Server.transitionFromISO(context.Background(), "app", []string{"svc"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"prepare-svc", "stop-iso", "clean-iso", "verify-iso-absent", "commit-svc", "release-iso", "start-svc"}
	if diff := cmp.Diff(want, recorder.Events); diff != "" {
		t.Fatalf("transition order (-want +got):\n%s", diff)
	}
}
```

- [ ] **Step 2: Run lifecycle tests and confirm failure**

Run: `mise exec -- go test ./pkg/catch ./pkg/svc -run 'TestISOInstall|TestRemoveISO|TestInspectISO|TestClone.*ISO' -count=1`

Expected: FAIL because the end-to-end orchestration and runtime inspection do not exist.

- [ ] **Step 3: Integrate two-pass admission before any Docker side effect**

Add one explicit orchestrator in the installer:

```go
func (i *FileInstaller) prepareISOCompose(ctx context.Context, composePath string) (*db.ISOAllocation, error) {
	projectName := svc.ComposeProjectName(i.cfg.ServiceName)
	baseJSON, err := svc.ResolveComposeJSON(ctx, svc.ComposeResolveOptions{
		ProjectName: projectName, ProjectDir: i.serviceDataDir(), Files: []string{composePath},
	})
	if err != nil {
		return nil, err
	}
	baseModel, err := AdmitISOCompose(baseJSON, ISOComposeAdmissionOptions{ServiceRoot: i.effectiveServiceRoot(), ProjectName: projectName, MaxComponents: iso.MaxComponents})
	if err != nil {
		return nil, err
	}
	allocation, err := i.s.reserveISOAllocation(ctx, i.cfg.ServiceName, isoReservationRequest{
		Kind: iso.PayloadCompose, Modes: i.cfg.Network.Modes, Components: baseModel.Components,
	})
	if err != nil {
		return nil, err
	}
	overlay, err := renderISOComposeOverlay(allocation, baseModel)
	if err != nil {
		return nil, err
	}
	overlayPath, err := i.stageISOComposeOverlay(overlay)
	if err != nil {
		return nil, err
	}
	mergedJSON, err := svc.ResolveComposeJSON(ctx, svc.ComposeResolveOptions{
		ProjectName: projectName, ProjectDir: i.serviceDataDir(), Files: []string{composePath, overlayPath},
	})
	if err != nil {
		return nil, err
	}
	mergedModel, err := AdmitISOCompose(mergedJSON, ISOComposeAdmissionOptions{ServiceRoot: i.effectiveServiceRoot(), ProjectName: projectName, MaxComponents: iso.MaxComponents, RequireISOOverlay: allocation})
	if err != nil {
		return nil, err
	}
	if diff := cmp.Diff(baseModel.Components, mergedModel.Components); diff != "" {
		return nil, fmt.Errorf("ISO overlay changed Compose components (-base +merged):\n%s", diff)
	}
	return allocation, nil
}

func (i *FileInstaller) stageISOComposeOverlay(content string) (string, error) {
	path := filepath.Join(i.serviceBinDir(), fmt.Sprintf("docker-compose.network.%s.yml", i.version()))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write ISO Compose overlay: %w", err)
	}
	mak.Set(&i.artifacts, db.ArtifactDockerComposeNetwork, path)
	return path, nil
}
```

Call this before `PrePullIfRunning`, `compose pull`, `compose up`, network creation, or auxiliary-unit start. Generated image/Dockerfile/Python/TypeScript payloads flow through the same canonical generated Compose file and single-component admission.

- [ ] **Step 4: Inspect the created runtime against the admitted model**

```go
type ISOInspectOptions struct {
	ProjectName string
	NetworkName string
	Components  map[string]netip.Addr
}

type ISOInspection struct {
	Containers []string
	Addresses  map[string]netip.Addr
	Findings   []string
}

func (r ISOInspection) Verify() error {
	if len(r.Findings) == 0 {
		return nil
	}
	return fmt.Errorf("ISO runtime differs from admitted model: %s", strings.Join(r.Findings, "; "))
}
```

`InspectISOProject` must use `docker compose ps --format json` plus `docker inspect` and verify: exactly one running container per admitted component; the expected static IPv4; attachment only to the generated default network; no host/other namespace mode; `Privileged=false`; no added capabilities/devices; no host control sockets or out-of-root binds; no published ports; and IPv6 absent. On any finding, run `compose down --remove-orphans`, mark quarantined, and return the finding.

- [ ] **Step 5: Add component retirement as a stop-clean-reserve transition**

When `reserveISOAllocation` returns retired components, do not start with both old and new ownership. Stop/down the project, inspect that all old endpoints and dnet endpoint records are gone, remove only the verified retired mappings in one `MutateService`, then re-run reservation and overlay rendering. If the old runtime cannot be proven absent, keep the old mapping reserved, quarantine the service, and return an error. This makes a 29-component replacement fail closed until the old address is actually free.

```go
func (s *Server) finalizeISORetirements(ctx context.Context, service string) error {
	if err := s.verifyISOProjectStopped(ctx, service); err != nil {
		return err
	}
	_, _, err := s.cfg.DB.MutateService(service, func(_ *db.Data, record *db.Service) error {
		if record.ISO == nil {
			return fmt.Errorf("service %q has no ISO allocation", service)
		}
		record.ISO.RetiredComponents = nil
		return nil
	})
	return err
}
```

- [ ] **Step 6: Reconcile before startup and quarantine drift**

Call `reconcileISONetworks` synchronously in Catch startup after Docker/firewall/DNS prerequisites and before the existing asynchronous general reconciliation. For each ISO record: validate pool overlap, ensure/verify global policy, ensure/verify topology for non-VM services, inspect running Docker state, and verify Tailscale when requested. Stop a running service before marking quarantined; only restart after the entire boundary verifies.

```go
func (s *Server) reconcileISONetworks(ctx context.Context) error {
	dv, err := s.cfg.DB.Get()
	if err != nil {
		return err
	}
	for _, service := range sortedISOServiceNames(dv) {
		if err := s.reconcileISONetwork(ctx, service); err != nil {
			_ = s.stopUntrustedISOService(ctx, service)
			_ = s.markISOState(service, string(iso.StateQuarantined), err)
		}
	}
	return s.ensureGlobalISOPolicy(ctx)
}
```

- [ ] **Step 7: Make stop and network transitions preserve or release state safely**

An ordinary stop marks the allocation `stopped` but retains every address, route identity, and component mapping. A restart returns through `EnsureISONetwork` before execution. For a requested transition away from ISO, validate and prepare the new mode first without starting it; stop the ISO workload; clean and verify the ISO attachment; atomically commit the new network record and clear ISO; then start the new mode. If cleanup fails, retain the ISO allocation as quarantined/tombstoned and do not start the new mode. If the new-mode commit/start fails after verified ISO cleanup, keep enough staged artifacts to retry; never silently reassign the former ISO addresses until the DB release commits.

```go
type isoReplacementNetwork struct {
	Modes      []string
	SvcNetwork *db.SvcNetwork
	Macvlan    *db.MacvlanNetwork
	Tailscale  *db.TailscaleNetwork
	Artifacts  db.ArtifactStore
}

func (s *Server) transitionFromISO(ctx context.Context, service string, desired []string) error {
	prepared, err := s.prepareReplacementNetwork(ctx, service, desired)
	if err != nil {
		return err
	}
	if err := s.stopUntrustedISOService(ctx, service); err != nil {
		return err
	}
	if err := s.CleanISONetwork(ctx, service); err != nil {
		_ = s.markISOState(service, string(iso.StateTombstoned), err)
		return err
	}
	if err := s.verifyISONetworkAbsent(ctx, service); err != nil {
		_ = s.markISOState(service, string(iso.StateTombstoned), err)
		return err
	}
	if err := s.commitReplacementNetwork(service, prepared); err != nil {
		return err
	}
	return s.startPreparedReplacement(ctx, service, prepared)
}
```

`prepareReplacementNetwork` returns `isoReplacementNetwork` by running the existing non-ISO parser and staging its generated artifacts without mutating the active service record. `commitReplacementNetwork` uses one `MutateService` to set `SvcNetwork`, `Macvlan`, `TSNet`, and staged artifacts from that value while clearing `ISO`; `startPreparedReplacement` invokes the existing service start path, which applies the new mode's normal verification.

- [ ] **Step 8: Make removal cleanup authoritative and clone allocation fresh**

In `RemoveServiceWithOptions`, first persist `RemoveRequested=true`, `CleanupVerified=false`, and state `removing`; then stop the workload and Tailscale, detach Docker endpoints, and call `CleanISONetwork`. After topology absence verifies, persist `CleanupVerified=true`, re-render global policy without that allocation, verify no interface/route/firewall/Docker record uses it, and only then delete the service DB record. On any cleanup failure, set `tombstoned`, retain the service record/allocation and restrictive policy, and return a detailed error. Reconciliation retries remove-requested tombstones but never starts them; a crash after `CleanupVerified=true` finishes policy removal and DB deletion. In clone/recovery, clear `ISO`, ISO-generated network artifacts, and component mappings before creating the destination; its next install reserves a fresh link/project.

```go
func clearClonedISOState(service *db.Service) {
	service.ISO = nil
	delete(service.Artifacts, db.ArtifactNetNSService)
	delete(service.Artifacts, db.ArtifactNetNSEnv)
	delete(service.Artifacts, db.ArtifactDockerComposeNetwork)
}
```

- [ ] **Step 9: Run lifecycle tests with failure injection and race detection**

Run: `mise exec -- go test ./pkg/catch ./pkg/svc -run 'ISO|Clone|Remove|Reconcile' -race -count=1`

Expected: PASS; injected failure after every ordered phase leaves the workload stopped and allocation reserved, quarantined, or tombstoned as appropriate.

- [ ] **Step 10: Commit container lifecycle integration**

Run:

```bash
IDS=$(but diff --format json | jq -r --args '$ARGS.positional as $paths | [.changes[] | select(.path as $path | $paths | index($path)) | .id] | join(",")' -- pkg/catch/installer_file.go pkg/catch/installer_file_test.go pkg/catch/installer_service.go pkg/catch/catch.go pkg/catch/catch_test.go pkg/catch/remove_test.go pkg/catch/recovery_vm.go pkg/catch/recovery_vm_test.go pkg/svc/iso_inspect.go pkg/svc/iso_inspect_test.go pkg/svc/docker.go pkg/svc/docker_test.go pkg/catch/iso_runtime.go pkg/catch/iso_runtime_test.go)
test -n "$IDS"
but commit codex/iso-network-design -m "iso: enforce container lifecycle isolation" --changes "$IDS"
```

Expected: one commit; ISO container starts are now admitted, policy-gated, inspected, and recoverable.

### Task 10: Dedicated VM `/30`, Guest Metadata, Reconciliation, and SSH Proxy

**Files:**
- Modify: `pkg/catch/vm_network.go`
- Modify: `pkg/catch/vm_network_test.go`
- Modify: `pkg/catch/vm_metadata.go`
- Modify: `pkg/catch/vm_metadata_test.go`
- Modify: `pkg/catch/vm_provision.go`
- Modify: `pkg/catch/vm_provision_test.go`
- Modify: `pkg/catch/vm_network_reconcile.go`
- Modify: `pkg/catch/vm_network_reconcile_test.go`
- Modify: `pkg/catch/vm_systemd.go`
- Modify: `pkg/catch/vm_systemd_test.go`
- Modify: `pkg/catch/vm_ssh_proxy.go`
- Modify: `pkg/catch/vm_ssh_proxy_test.go`
- Modify: `pkg/yeet/ssh_cmd.go`
- Modify: `pkg/yeet/ssh_cmd_test.go`
- Modify: `pkg/catch/tty_authz.go`
- Modify: `pkg/catch/tty_authz_test.go`

**Interfaces:**
- Consumes: persisted VM `db.ISOAllocation`, Task 7 policy verification, existing VM provisioning/metadata/Firecracker/TAP paths, existing Catch VM SSH proxy.
- Produces: ISO fields in `vmNetworkInputs`; an `iso` `vmNetworkInterfacePlan`; direct TAP `/30` setup without shared bridge; guest address/gateway/DNS metadata; ISO-aware startup reconciliation; automatic proxy selection; `ExecTargetVMSSHProxy` mapped to `read`.

- [ ] **Step 1: Write VM plan, metadata, setup-order, and SSH tests**

```go
func TestVMISONetworkPlanUsesDedicatedTap(t *testing.T) {
	plan := newVMNetworkPlan("devbox", []string{"iso"}, vmNetworkInputs{
		ISOHostIP:  netip.MustParseAddr("172.30.0.1"),
		ISOGuestIP: netip.MustParseAddr("172.30.0.2"),
		ISOLink:    netip.MustParsePrefix("172.30.0.0/30"),
		ISOTap:     "yi-devbox",
	})
	if len(plan.Interfaces) != 1 {
		t.Fatalf("interfaces = %#v", plan.Interfaces)
	}
	got := plan.Interfaces[0]
	if got.Mode != "iso" || got.Tap != "yi-devbox" || got.Bridge != "" || got.GuestIP != "172.30.0.2/30" || got.Gateway != "172.30.0.1" {
		t.Fatalf("ISO interface = %#v", got)
	}
	metadata := plan.MetadataNetworks()[0]
	if diff := cmp.Diff([]string{"172.30.0.1"}, metadata.Nameservers); diff != "" {
		t.Fatalf("nameservers (-want +got):\n%s", diff)
	}
	if len(metadata.SearchDomains) != 0 {
		t.Fatalf("search domains = %#v", metadata.SearchDomains)
	}
}

func TestEnsureVMISONetworkVerifiesPolicyBeforeTap(t *testing.T) {
	recorder := newVMISONetworkRecorder(t)
	if err := recorder.Server.EnsureVMNetwork(context.Background(), "devbox"); err != nil {
		t.Fatal(err)
	}
	want := []string{"ensure-iso-policy", "verify-iso-policy", "create-tap", "assign-host-ip", "verify-tap"}
	if diff := cmp.Diff(want, recorder.Events); diff != "" {
		t.Fatalf("order (-want +got):\n%s", diff)
	}
}

func TestVMISOSSHAlwaysUsesCatchProxy(t *testing.T) {
	resp := catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{
		ServiceType: "vm",
		Network: catchrpc.ServiceNetwork{ISO: &catchrpc.ServiceISO{Modes: []string{"iso"}}},
		VM: &catchrpc.ServiceVM{SSH: &catchrpc.ServiceVMSSH{User: "ubuntu", Host: "172.30.0.2"}},
	}}
	target := vmSSHTarget(resp, false)
	if !target.Proxy || target.Host != "172.30.0.2" {
		t.Fatalf("target = %#v", target)
	}
}
```

- [ ] **Step 2: Run focused VM tests and confirm failure**

Run: `mise exec -- go test ./pkg/catch ./pkg/yeet -run 'VMISO|ISO.*VM|VM.*ISO|VMSSHProxy.*Permission' -count=1`

Expected: FAIL because VM planning has no ISO adapter and the proxy still requires `ssh`.

- [ ] **Step 3: Add ISO inputs and a direct-TAP interface plan**

```go
type vmNetworkInputs struct {
	ServiceIP         string
	LANParent         string
	LANParentIsBridge bool
	LANBridge         string
	LANVLAN           int
	LANMAC            string
	ISOHostIP         netip.Addr
	ISOGuestIP        netip.Addr
	ISOLink           netip.Prefix
	ISOTap            string
}

func configureVMISONetworkInterface(iface *vmNetworkInterfacePlan, idx int, in vmNetworkInputs) {
	iface.Tap = in.ISOTap
	iface.GuestIP = netip.PrefixFrom(in.ISOGuestIP, in.ISOLink.Bits()).String()
	iface.Gateway = in.ISOHostIP.String()
	iface.DHCP = false
}
```

Add `case "iso"` to `newVMNetworkInterfacePlan`. Do not set `Bridge`, `Parent`, or `VLANDevice`: Firecracker attaches to the dedicated TAP directly, so no unrelated VM shares a broadcast domain. Persist `VMNetworkConfig.IP` as the guest address while the complete link remains in `Service.ISO`.

- [ ] **Step 4: Render ISO guest DNS without Yeet search**

In `MetadataNetworks`, fill `Nameservers` and `SearchDomains` explicitly for ISO:

```go
if iface.Mode == "iso" {
	network.Nameservers = []string{iface.Gateway}
	network.SearchDomains = []string{}
	defaultRoute := true
	network.DNSDefaultRoute = &defaultRoute
}
```

Update `vmGuestNetworkNameservers` and `vmGuestNetworkSearchDomains` so explicit empty slices remain empty instead of falling back to `192.168.100.1` and `yeet.internal`. Render IPv6 disabled in both supported guest metadata formats.

- [ ] **Step 5: Add dedicated TAP setup and cleanup commands**

```go
func vmISONetworkSetupCommands(iface vmNetworkInterfacePlan, hostIP netip.Addr, link netip.Prefix) []vmNetworkCommand {
	return []vmNetworkCommand{
		rootVMCommand("ip", "tuntap", "add", "dev", iface.Tap, "mode", "tap"),
		rootVMCommand("ip", "link", "set", iface.Tap, "up"),
		rootVMCommand("ip", "-4", "addr", "replace", netip.PrefixFrom(hostIP, link.Bits()).String(), "dev", iface.Tap),
		rootVMCommand("sysctl", "-w", "net.ipv4.conf."+iface.Tap+".rp_filter=1"),
		rootVMCommand("sysctl", "-w", "net.ipv6.conf."+iface.Tap+".disable_ipv6=1"),
	}
}
```

Cleanup deletes only the persisted dedicated TAP and verifies its absence. Existing `svc` bridge and LAN setup commands remain unchanged.

- [ ] **Step 6: Reserve before provisioning and ensure policy before TAP**

In `newVMProvisionPlan`, reserve the VM allocation before rendering metadata, Firecracker config, or systemd. Populate `vmNetworkInputs` from it. In `ensureVMNetworkFromDataView`, detect the VM ISO allocation and call `EnsureISONetwork` before `vmNetworkSetupCommands`; then verify policy and TAP together before returning. The existing VM unit's `ExecStartPre=catch ... vm-network-ensure SERVICE` remains the start gate.

```go
func ensureVMNetworkFromDataView(ctx context.Context, cfg *Config, dv *db.DataView, service string) error {
	plan, allocation, err := vmNetworkPlanAndISOForService(dv, service)
	if err != nil {
		return err
	}
	if allocation != nil {
		if err := EnsureISONetwork(ctx, cfg, service); err != nil {
			return err
		}
	}
	setup, err := vmNetworkSetupCommands(plan)
	if err != nil {
		return err
	}
	if err := runVMNetworkLifecycleCommands(setup, nil, fmt.Sprintf("ensure VM network for %q", service)); err != nil {
		return err
	}
	return verifyVMNetworkPlan(ctx, plan)
}
```

- [ ] **Step 7: Force ISO VM SSH through Catch and correct its permission**

Treat `ServiceNetwork.ISO != nil` as `Proxy=true` regardless of workstation route probing. Keep a user-provided ProxyCommand/ProxyJump authoritative. Include the persisted ISO guest address in `vmSSHProxyAllowedHosts`. Change `execRequestPermissions` so host/service shells require `ssh`, while `ExecTargetVMSSHProxy` requires `read`:

```go
switch req.Target {
case catchrpc.ExecTargetHostShell, catchrpc.ExecTargetServiceShell:
	return newPermissionSet(permissionSSH), nil
case catchrpc.ExecTargetVMSSHProxy:
	return newPermissionSet(permissionRead), nil
}
```

Add positive `read` and negative no-read authorization tests. The guest SSH key still decides login after Catch opens the TCP stream.

- [ ] **Step 8: Run VM planning, provisioning, reconciliation, metadata, SSH, and auth tests**

Run: `mise exec -- go test ./pkg/catch ./pkg/yeet -run 'VMNetwork|VMProvision|VMMetadata|VMSSH|ISO|ExecRequestPermissions' -count=1`

Expected: PASS; existing `svc`, `lan`, and `svc,lan` VM behavior remains green.

- [ ] **Step 9: Commit VM ISO support**

Run:

```bash
IDS=$(but diff --format json | jq -r --args '$ARGS.positional as $paths | [.changes[] | select(.path as $path | $paths | index($path)) | .id] | join(",")' -- pkg/catch/vm_network.go pkg/catch/vm_network_test.go pkg/catch/vm_metadata.go pkg/catch/vm_metadata_test.go pkg/catch/vm_provision.go pkg/catch/vm_provision_test.go pkg/catch/vm_network_reconcile.go pkg/catch/vm_network_reconcile_test.go pkg/catch/vm_systemd.go pkg/catch/vm_systemd_test.go pkg/catch/vm_ssh_proxy.go pkg/catch/vm_ssh_proxy_test.go pkg/yeet/ssh_cmd.go pkg/yeet/ssh_cmd_test.go pkg/catch/tty_authz.go pkg/catch/tty_authz_test.go)
test -n "$IDS"
but commit codex/iso-network-design -m "iso: add dedicated VM network attachments" --changes "$IDS"
```

Expected: one commit; VM ISO uses the common policy but a dedicated TAP adapter.

### Task 11: Service Observability, Endpoint Listings, and Permission Regression Tests

**Files:**
- Modify: `pkg/catchrpc/types.go`
- Modify: `pkg/catchrpc/types_test.go`
- Modify: `pkg/catch/service_info.go`
- Modify: `pkg/catch/service_info_test.go`
- Modify: `pkg/catch/tty_ops.go`
- Modify: `pkg/catch/tty_ops_test.go`
- Modify: `pkg/yeet/info_cmd.go`
- Modify: `pkg/yeet/info_cmd_test.go`
- Modify: `pkg/catch/authz_test.go`
- Modify: `pkg/catch/tty_authz_test.go`

**Interfaces:**
- Consumes: persisted ISO state and existing `ServiceInfo`, `ServiceNetwork`, `ServiceIP`, `info`, and `ip` paths.
- Produces: `catchrpc.ServiceISO`, `catchrpc.ServiceISOComponent`, `ServiceNetwork.ISO`; stable endpoint labels from persisted state; human-readable policy/status/errors; complete permission regression matrix.

- [ ] **Step 1: Write JSON, info rendering, and IP endpoint tests**

```go
func TestServiceISOJSONRoundTrip(t *testing.T) {
	want := ServiceISO{
		Modes: []string{"iso", "ts"}, State: "ready", PublicEgress: true,
		DNS: "tailscale", Link: "172.30.0.0/30", Project: "172.30.128.0/27",
		Components: []ServiceISOComponent{{Name: "api", IP: "172.30.128.2"}},
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got ServiceISO
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatal(diff)
	}
}

func TestServiceInfoRendersISOStateAndComponents(t *testing.T) {
	info := catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{
		Network: catchrpc.ServiceNetwork{ISO: &catchrpc.ServiceISO{
			Modes: []string{"iso"}, State: "quarantined", PublicEgress: true, DNS: "public-only",
			Components: []catchrpc.ServiceISOComponent{{Name: "api", IP: "172.30.128.2"}},
			LastError: "firewall digest mismatch",
		}},
	}}
	raw := renderServiceInfoForTest(t, info)
	for _, want := range []string{"ISO", "quarantined", "public IPv4 via NAT", "public-only", "api", "172.30.128.2", "firewall digest mismatch"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("output missing %q:\n%s", want, raw)
		}
	}
}

func TestServiceIPListUsesPersistedISOEndpointsOnly(t *testing.T) {
	server := newServiceInfoTestServer(t, &db.Service{ISO: &db.ISOAllocation{
		Link: netip.MustParsePrefix("172.30.0.0/30"), HostIP: netip.MustParseAddr("172.30.0.1"), PeerIP: netip.MustParseAddr("172.30.0.2"),
		Components: map[string]db.ISOComponent{"api": {Address: netip.MustParseAddr("172.30.128.2")}},
	}})
	ips, err := server.serviceIPListWithContext(context.Background(), "app", mustServiceView(t, server, "app"))
	if err != nil {
		t.Fatal(err)
	}
	want := []catchrpc.ServiceIP{{Label: "api", IP: "172.30.128.2"}}
	if diff := cmp.Diff(want, ips); diff != "" {
		t.Fatalf("endpoints (-want +got):\n%s", diff)
	}
}
```

- [ ] **Step 2: Run observability tests and confirm failure**

Run: `mise exec -- go test ./pkg/catchrpc ./pkg/catch ./pkg/yeet -run 'ServiceISO|Info.*ISO|IPList.*ISO|Permission.*ISO' -count=1`

Expected: FAIL because service-level ISO RPC state is absent.

- [ ] **Step 3: Add backward-compatible RPC types**

```go
type ServiceISO struct {
	Modes        []string              `json:"modes,omitempty"`
	State        string                `json:"state,omitempty"`
	PublicEgress bool                  `json:"publicEgress"`
	DNS          string                `json:"dns,omitempty"`
	Link         string                `json:"link,omitempty"`
	Project      string                `json:"project,omitempty"`
	Gateway      string                `json:"gateway,omitempty"`
	Components   []ServiceISOComponent `json:"components,omitempty"`
	LastError    string                `json:"lastError,omitempty"`
}

type ServiceISOComponent struct {
	Name  string `json:"name"`
	IP    string `json:"ip"`
	State string `json:"state,omitempty"`
}
```

Add `ISO *ServiceISO` to `ServiceNetwork`, `serviceNetworkJSON`, and `serviceNetworkJSONWithPorts`. Preserve custom port-presence marshaling exactly. Old JSON without `iso` must round-trip unchanged.

- [ ] **Step 4: Populate service info from persisted truth**

```go
func serviceISOInfo(view db.ISOAllocationView) *catchrpc.ServiceISO {
	if !view.Valid() {
		return nil
	}
	record := view.AsStruct()
	out := &catchrpc.ServiceISO{
		Modes: append([]string(nil), record.DesiredModes...), State: record.State,
		PublicEgress: true, DNS: "public-only", Link: record.Link.String(), LastError: record.LastError,
	}
	if record.Project.IsValid() {
		out.Project = record.Project.String()
	}
	if record.Gateway.IsValid() {
		out.Gateway = record.Gateway.String()
	}
	if slices.Contains(record.DesiredModes, "ts") {
		out.DNS = "tailscale"
	}
	for _, name := range sortedISOComponentNames(record.Components) {
		component := record.Components[name]
		out.Components = append(out.Components, catchrpc.ServiceISOComponent{Name: name, IP: component.Address.String(), State: component.State})
	}
	return out
}
```

For VM ISO, report one component named `vm` with `PeerIP`; for container projects, report sorted component mappings. Include link/project/gateway only in detailed info/JSON, not the default `yeet ip` endpoint list.

- [ ] **Step 5: Render human status and stable `yeet ip` labels**

Add ISO rows before ordinary port/Tailscale rows: modes, state, egress (`public IPv4 via NAT`), DNS (`public-only` or `Tailscale/MagicDNS`), components, and last error. Use `degraded` only for a non-security auxiliary fault while the verified boundary remains intact; any uncertainty about policy, topology, DNS enforcement, source validation, or runtime admission is `quarantined`. `yeet ip` prints `vm` for the ISO VM address and the admitted Compose service name for each component address. Do not print host link, peer router, project gateway, Docker bridge, or runtime-discovered duplicates.

```go
func serviceISOEndpointIPs(iso *catchrpc.ServiceISO) []catchrpc.ServiceIP {
	if iso == nil {
		return nil
	}
	out := make([]catchrpc.ServiceIP, 0, len(iso.Components))
	for _, component := range iso.Components {
		if component.IP != "" {
			out = append(out, catchrpc.ServiceIP{Label: component.Name, IP: component.IP})
		}
	}
	return out
}
```

- [ ] **Step 6: Lock the permission matrix with positive and negative tests**

Assert `read` for server/service info, `ip`, VM connection metadata, and VM SSH proxy. Assert `manage` for run/update/restart/stop/remove, ISO pool plan/apply, cleanup repair, and network transition. Assert host/service shell still needs `ssh`. Assert no remote RPC/TTY registry entry exists for local `iso-network-ensure`, `iso-network-clean`, or `iso-dns` helpers.

```go
func TestISOPermissionMatrix(t *testing.T) {
	assertRPCPermission(t, "ISOPoolPlan", permissionManage)
	assertRPCPermission(t, "ISOPoolApply", permissionManage)
	assertExecPermission(t, catchrpc.ExecRequest{Target: catchrpc.ExecTargetServiceCommand, Args: []string{"ip"}}, permissionRead)
	assertExecPermission(t, catchrpc.ExecRequest{Target: catchrpc.ExecTargetVMSSHProxy, Service: "vm"}, permissionRead)
	assertExecPermission(t, catchrpc.ExecRequest{Target: catchrpc.ExecTargetServiceCommand, Args: []string{"run", "image"}}, permissionManage)
	assertExecPermission(t, catchrpc.ExecRequest{Target: catchrpc.ExecTargetServiceShell, Service: "app"}, permissionSSH)
}
```

- [ ] **Step 7: Run RPC, info, IP, and authorization tests**

Run: `mise exec -- go test ./pkg/catchrpc ./pkg/catch ./pkg/yeet -run 'ServiceNetwork|ServiceISO|Info|IP|Permission|Authz' -count=1`

Expected: PASS, including legacy ServiceNetwork JSON cases.

- [ ] **Step 8: Commit observability and permission coverage**

Run:

```bash
IDS=$(but diff --format json | jq -r --args '$ARGS.positional as $paths | [.changes[] | select(.path as $path | $paths | index($path)) | .id] | join(",")' -- pkg/catchrpc/types.go pkg/catchrpc/types_test.go pkg/catch/service_info.go pkg/catch/service_info_test.go pkg/catch/tty_ops.go pkg/catch/tty_ops_test.go pkg/yeet/info_cmd.go pkg/yeet/info_cmd_test.go pkg/catch/authz_test.go pkg/catch/tty_authz_test.go)
test -n "$IDS"
but commit codex/iso-network-design -m "iso: expose isolation state and endpoints" --changes "$IDS"
```

Expected: one commit; inspection is `read`, mutation is `manage`, and host/service shell remains `ssh`.

### Task 12: Privileged Linux Packet-Policy Integration Suite

**Files:**
- Create: `pkg/netns/iso_integration_linux_test.go`
- Create: `pkg/netns/testdata/iso-endpoint/main.go`
- Create: `tools/test-iso-network.sh`
- Modify: `mise.toml`

**Interfaces:**
- Consumes: `netns.EnsureISOPolicy`, `netns.EnsureISOTopology`, the available firewall backend, root Linux namespaces/veth/routes, and a tiny Go endpoint helper.
- Produces: opt-in `linux && integration` tests and `mise run test:iso-network`; concrete proof for Catch ingress, public egress, DNS, same-project traffic, private/peer isolation, spoofing, stateful replies, direct-DNS rejection, and IPv6 denial.

- [ ] **Step 1: Write the failing privileged test harness**

```go
//go:build linux && integration

package netns

func TestISOPacketPolicy(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("ISO integration test requires root")
	}
	if os.Getenv("YEET_ISO_INTEGRATION") != "1" {
		t.Skip("set YEET_ISO_INTEGRATION=1")
	}
	for _, backend := range availableISOIntegrationBackends(t) {
		t.Run(string(backend), func(t *testing.T) {
			lab := newISOIntegrationLab(t, backend)
			lab.StartPublicEndpoint("1.1.1.10", 18080)
			lab.StartISODNS(map[string]string{"public.test.": "1.1.1.10", "private.test.": "10.0.0.10"})
			lab.EnsureProject("a", "172.30.0.0/30", "172.30.128.0/27", []string{"172.30.128.2", "172.30.128.3"})
			lab.EnsureProject("b", "172.30.0.4/30", "172.30.128.32/27", []string{"172.30.128.34"})

			lab.AssertHostConnects("172.30.128.2", 18080)
			lab.AssertEndpointConnects("a", "172.30.128.2", "1.1.1.10", 18080)
			lab.AssertEndpointResolves("a", "172.30.128.2", "public.test.", "1.1.1.10")
			lab.AssertEndpointConnects("a", "172.30.128.2", "172.30.128.3", 18080)
			lab.AssertEndpointRejected("a", "172.30.128.2", "172.30.0.1", 22)
			lab.AssertEndpointRejected("a", "172.30.128.2", "192.168.100.1", 53)
			lab.AssertEndpointRejected("a", "172.30.128.2", "169.254.169.254", 80)
			lab.AssertEndpointRejected("a", "172.30.128.2", "100.100.100.100", 53)
			lab.AssertEndpointRejected("a", "172.30.128.2", "172.30.128.34", 18080)
			lab.AssertEndpointRejected("a", "172.30.128.2", "1.1.1.10", 53)
			lab.AssertSpoofedSourceDropped("a", "172.30.128.2", "172.30.128.34")
			lab.AssertIPv6Unavailable("a", "172.30.128.2")
			lab.AssertStatefulReplySucceeds("172.30.128.2", 18080)
		})
	}
}
```

- [ ] **Step 2: Run the integration target and confirm the harness is incomplete**

Run: `sudo env YEET_ISO_INTEGRATION=1 PATH="$PATH" mise exec -- go test -tags=integration ./pkg/netns -run TestISOPacketPolicy -count=1 -v`

Expected: FAIL because the lab helper and endpoint binary are not implemented.

- [ ] **Step 3: Implement an isolated, self-cleaning namespace lab**

Use a random `yi-test-XXXXXXXX` suffix, a dedicated test root namespace topology, and `t.Cleanup` to remove only exact interfaces/namespaces/tables created by that test. Never flush host tables. The synthetic upstream namespace owns `1.1.1.10/32` only inside the lab so the real production classifier treats the destination as public; the test namespace has no route to the external internet.

```go
type ISOIntegrationLab struct {
	t        *testing.T
	backend  FirewallBackend
	suffix   string
	commands [][]string
}

func newISOIntegrationLab(t *testing.T, backend FirewallBackend) *ISOIntegrationLab {
	t.Helper()
	lab := &ISOIntegrationLab{t: t, backend: backend, suffix: fmt.Sprintf("%08x", rand.Uint32())}
	lab.mustRun("ip", "netns", "add", "yi-up-"+lab.suffix)
	t.Cleanup(func() {
		_ = exec.Command("ip", "netns", "del", "yi-up-"+lab.suffix).Run()
		lab.removeProjects()
		lab.removePolicy()
	})
	return lab
}

func (l *ISOIntegrationLab) mustRun(name string, args ...string) {
	l.t.Helper()
	cmd := exec.Command(name, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		l.t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	l.commands = append(l.commands, append([]string{name}, args...))
}
```

Build `pkg/netns/testdata/iso-endpoint/main.go` into `t.TempDir()` with `mise exec -- go build`; the helper must support `listen`, `connect`, `dns`, and `spoof` subcommands with explicit timeouts and machine-readable exit codes. Run endpoint processes using `ip netns exec` and terminate them in `t.Cleanup`.

- [ ] **Step 4: Add the opt-in mise target**

```toml
[tasks."test:iso-network"]
description = "Run privileged ISO namespace and firewall integration tests"
run = "sudo env YEET_ISO_INTEGRATION=1 PATH=\"$PATH\" mise exec -- go test -tags=integration ./pkg/netns -run TestISOPacketPolicy -count=1 -v"
```

`tools/test-iso-network.sh` must contain `set -euo pipefail`, resolve the repo root relative to the script, and execute the same command. The default `go test ./...` must compile without the integration tag and must not require root.

- [ ] **Step 5: Run each available backend plus the default suite**

Run: `mise run test:iso-network && mise exec -- go test ./pkg/netns -count=1`

Expected: PASS for nftables and each installed iptables backend; unavailable backends report SKIP with the missing binary/backend reason. The ordinary package test passes without root.

- [ ] **Step 6: Commit the privileged policy proof**

Run:

```bash
IDS=$(but diff --format json | jq -r --args '$ARGS.positional as $paths | [.changes[] | select(.path as $path | $paths | index($path)) | .id] | join(",")' -- pkg/netns/iso_integration_linux_test.go pkg/netns/testdata/iso-endpoint/main.go tools/test-iso-network.sh mise.toml)
test -n "$IDS"
but commit codex/iso-network-design -m "test: prove isolated network packet policy" --changes "$IDS"
```

Expected: one commit with an opt-in root test and no host-wide destructive cleanup.

### Task 13: Web UI, README, and Website Manual

**Files:**
- Modify: `pkg/yeet/web_run_assets/app.js`
- Modify: `pkg/yeet/web_run_assets_test.go`
- Modify: `README.md`
- Modify: `website/docs/concepts/networking.mdx`
- Modify: `website/docs/concepts/dns.mdx`
- Modify: `website/docs/concepts/tailscale.mdx`
- Modify: `website/docs/payloads/containers.mdx`
- Modify: `website/docs/payloads/vms.mdx`
- Modify: `website/docs/payloads/binaries.mdx`
- Modify: `website/docs/payloads/cron-jobs.mdx`
- Modify: `website/docs/security/tailscale-access-grants.mdx`
- Modify: `website/docs/operations/troubleshooting.mdx`
- Modify: `website/docs/cli/yeet-cli.mdx`

**Interfaces:**
- Consumes: the completed behavior and exact errors from Tasks 3-11, website's existing MDX style, and the `yeetrun.com` public domain.
- Produces: web-run ISO choices/validation and one consistent public explanation of the trust boundary, supported payload matrix, addressing, ingress/egress, Compose admission, DNS, Tailscale, permissions, operations, and limitations.

- [ ] **Step 1: Write failing web-run compatibility tests**

```go
func TestWebRunAssetsExposeISOCompatibility(t *testing.T) {
	app, err := fs.ReadFile(webRunAssets, "web_run_assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	raw := string(app)
	for _, want := range []string{
		`vm: new Set(["svc", "lan", "iso"])`,
		`compose: new Set(["svc", "lan", "ts", "iso"])`,
		`binary: new Set(["svc", "lan", "ts"])`,
		"iso cannot combine with svc or lan",
		"ISO does not support published ports",
		"VMs support only iso",
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("app.js missing %q", want)
		}
	}
}
```

- [ ] **Step 2: Run web asset tests and confirm failure**

Run: `mise exec -- go test ./pkg/yeet -run 'WebRun|RunWeb' -count=1`

Expected: FAIL because web choices and messages do not expose ISO consistently.

- [ ] **Step 3: Add the web choice and disable incompatible controls**

```javascript
const NETWORK_COMPATIBILITY = Object.freeze({
  vm: new Set(["svc", "lan", "iso"]),
  compose: new Set(["svc", "lan", "ts", "iso"]),
  image: new Set(["svc", "lan", "ts", "iso"]),
  dockerfile: new Set(["svc", "lan", "ts", "iso"]),
  python: new Set(["svc", "lan", "ts", "iso"]),
  typescript: new Set(["svc", "lan", "ts", "iso"]),
  binary: new Set(["svc", "lan", "ts"]),
  cron: new Set([]),
});

function isoSelectionError(payload, modes, publish) {
  if (!modes.includes("iso")) return "";
  if (modes.includes("svc") || modes.includes("lan")) return "iso cannot combine with svc or lan";
  if (payload === "vm" && modes.length !== 1) return "VMs support only iso as a Yeet-managed isolated mode";
  if ((payload === "binary" || payload === "cron")) return `${payload} root services do not support iso`;
  if (publish.length !== 0) return "ISO does not support published ports";
  return "";
}
```

Selecting ISO disables/clears publish controls and incompatible `svc`/`lan`; selecting VM ISO disables Yeet-managed Tailscale. Still submit the draft through server validation—UI state is convenience, not enforcement.

- [ ] **Step 4: Update README with the concise contract and matrix**

Add this mechanism-first summary near the network-mode table:

```markdown
`--net=iso` is for untrusted container workloads and VMs that need the public
internet but should not be able to initiate connections to Catch, your LAN,
the Yeet service network, the tailnet, or another ISO project. Catch can still
connect to every stable ISO endpoint, so administration and proxied VM SSH keep
working without published ports.

ISO is IPv4-only. It permits public IPv4 egress through Catch NAT, uses a
public-only DNS view, blocks direct DNS and DNS-over-TLS, and cannot identify
DNS-over-HTTPS inside otherwise permitted HTTPS. `iso,ts` adds a service-owned
Tailscale identity for non-VM containers. Native binaries, scripts, and cron
jobs remain unsupported until Yeet can run them non-root inside a real process
sandbox.
```

Document `yeet host set --iso-pool=172.30.0.0/16`, the no-renumbering rule, rejected port publishing, and links to the full networking manual on `https://yeetrun.com`.

- [ ] **Step 5: Update the website manual as one consistent model**

Use the approved matrix verbatim in `networking.mdx`, then adapt each page:

- `networking.mdx`: asymmetric boundary, `/16` selection, `/30` VM links, routed `/27` projects, Catch-only ingress, same-project communication, restart stability, and unsupported combinations.
- `dns.mdx`: no `yeet.internal`/search for ISO, public-only filtering, direct 53/853 block, IPv4-only behavior, and DNS-over-HTTPS limitation.
- `tailscale.mdx`: `iso,ts` for non-VM services, service identity/MagicDNS/tailnet routes through `ts0`, public default via ISO, and guest-owned Tailscale for VMs.
- `containers.mdx`: canonical two-pass Compose admission, exact rejected capabilities, 29-component/no-scaling limit, client-side Dockerfile build distinction, and no ports.
- `vms.mdx`: stable `.2/30` guest, Catch proxy SSH, no Yeet-managed `iso,ts`, and guest-owned overlays.
- `binaries.mdx` and `cron-jobs.mdx`: explicit rejection because host-root can leave a namespace; non-root sandboxing is the future prerequisite.
- `tailscale-access-grants.mdx`: `manage` mutation, `read` inspection and VM guest proxy metadata, guest key authorization, and unchanged `ssh` for Catch/service shells.
- `troubleshooting.mdx`: pool conflicts, quarantine, tombstones, canonical Compose field-path errors, backend/ipset prerequisites, and `info`/`ip` commands.
- `yeet-cli.mdx`: `--net=iso`, `--iso-pool`, rejected publish flags, examples, and exact compatibility errors.

Do not add a changelog entry; this plan does not publish a release.

- [ ] **Step 6: Run docs and web checks**

Run: `mise exec -- go test ./pkg/yeet -run 'WebRun|RunWeb' -count=1 && mise exec -- pre-commit run --files README.md pkg/yeet/web_run_assets/app.js pkg/yeet/web_run_assets_test.go website/docs/concepts/networking.mdx website/docs/concepts/dns.mdx website/docs/concepts/tailscale.mdx website/docs/payloads/containers.mdx website/docs/payloads/vms.mdx website/docs/payloads/binaries.mdx website/docs/payloads/cron-jobs.mdx website/docs/security/tailscale-access-grants.mdx website/docs/operations/troubleshooting.mdx website/docs/cli/yeet-cli.mdx`

Expected: PASS with no private hostnames, local paths, or `yeet.run` references.

- [ ] **Step 7: Commit the website repository locally**

Run:

```bash
git -C website add docs/concepts/networking.mdx docs/concepts/dns.mdx docs/concepts/tailscale.mdx docs/payloads/containers.mdx docs/payloads/vms.mdx docs/payloads/binaries.mdx docs/payloads/cron-jobs.mdx docs/security/tailscale-access-grants.mdx docs/operations/troubleshooting.mdx docs/cli/yeet-cli.mdx
git -C website commit -m "docs: explain isolated network mode"
```

Expected: one local website commit. Do not push it without user authorization.

- [ ] **Step 8: Commit root UI, README, and website gitlink**

Run:

```bash
IDS=$(but diff --format json | jq -r --args '$ARGS.positional as $paths | [.changes[] | select(.path as $path | $paths | index($path)) | .id] | join(",")' -- pkg/yeet/web_run_assets/app.js pkg/yeet/web_run_assets_test.go README.md website)
test -n "$IDS"
but commit codex/iso-network-design -m "docs: document isolated network mode" --changes "$IDS"
```

Expected: one root commit pointing at the local website documentation commit. The implementation handoff must state that neither repository has been pushed.

### Task 14: Full Verification and Live Acceptance

**Files:**
- Create: `example/iso-acceptance/compose.yml`
- Create: `example/iso-acceptance/README.md`
- Modify only if a gate exposes a defect: the exact implementation/test/doc file responsible for that defect.

**Interfaces:**
- Consumes: the complete feature, repo quality gates, a Linux Catch host selected through normal `CATCH_HOST`/`--host` targeting, Docker, and VM image support.
- Produces: release-grade local verification evidence, a repeatable live acceptance fixture, and a concise list of live addresses/results. It does not publish the branch or release.

- [ ] **Step 1: Add a two-component live acceptance Compose fixture**

```yaml
services:
  web:
    image: nginx:1.29-alpine
  probe:
    image: alpine:3.22
    command:
      - sh
      - -ec
      - apk add --no-cache bind-tools curl iproute2 netcat-openbsd; sleep infinity
    volumes:
      - probe-data:/data

volumes:
  probe-data: {}
```

Keep the fixture inside its service root and do not publish ports. `README.md` records the commands below and explains that the synthetic names are disposable.

- [ ] **Step 2: Run targeted packages with race detection**

Run:

```bash
mise exec -- go test ./pkg/iso ./pkg/db ./pkg/netns ./pkg/dnet ./pkg/svc ./pkg/catchrpc ./pkg/catch ./pkg/cli ./pkg/yeet ./cmd/catch ./cmd/yeet -race -count=1
```

Expected: PASS with no race findings.

- [ ] **Step 3: Run parser/network fuzz smoke tests**

Run:

```bash
mise exec -- go test ./pkg/iso -run '^$' -fuzz FuzzISOInputs -fuzztime=30s
mise exec -- go test ./pkg/catch -run '^$' -fuzz FuzzAdmitISOCompose -fuzztime=30s
mise exec -- go test ./pkg/catch -run '^$' -fuzz FuzzFilterISODNSMessage -fuzztime=30s
```

Expected: PASS with no crash. Commit any minimized corpus created by a discovered bug together with the fix before continuing.

- [ ] **Step 4: Run the privileged packet-policy suite**

Run: `mise run test:iso-network`

Expected: PASS for every installed backend; unavailable alternate backends are explicit skips, not silent omissions.

- [ ] **Step 5: Run the full deterministic gates**

Run:

```bash
mise exec -- go test ./... -count=1
mise exec -- pre-commit run --all-files
mise run quality:goal
```

Expected: all three commands exit 0; coverage stays at or above 80%, CRAP hotspots and golangci findings remain zero, race checks are clean, at least four fuzz targets remain active, and the bounded mutation score stays at or above 80%.

- [ ] **Step 6: Check the session against current upstream before live deployment**

Run: `but pull --check`

Expected: clean. If the base changed and `but pull` affects only `codex/iso-network-design`, run `but pull`, repeat Steps 2-5, and continue. Stop if the pull conflicts with another active branch.

- [ ] **Step 7: Deploy two ISO projects, one `iso,ts` service, and one ISO VM to the selected Catch host**

Run:

```bash
mise exec -- go run ./cmd/yeet run iso-accept-a ./example/iso-acceptance/compose.yml --net=iso
mise exec -- go run ./cmd/yeet run iso-accept-b ./example/iso-acceptance/compose.yml --net=iso
mise exec -- go run ./cmd/yeet run iso-accept-ts alpine:3.22 --net=iso,ts
mise exec -- go run ./cmd/yeet run iso-accept-vm vm://ubuntu/26.04 --net=iso
mise exec -- go run ./cmd/yeet info catch
mise exec -- go run ./cmd/yeet info iso-accept-a
mise exec -- go run ./cmd/yeet ip iso-accept-a
mise exec -- go run ./cmd/yeet ip iso-accept-b
mise exec -- go run ./cmd/yeet ip iso-accept-vm
```

Expected: all deployments become ready; each endpoint has a stable `172.x` address from the chosen `/16`; the two projects have different `/27`s; the VM has its own `/30`; no published ports appear.

- [ ] **Step 8: Prove Catch-originated ingress and proxied VM SSH**

From `yeet ip`, record the `web`, `probe`, and VM addresses. Run Catch-side curls/pings through the existing host shell, and run:

```bash
mise exec -- go run ./cmd/yeet ssh iso-accept-vm -- uname -a
```

Expected: Catch reaches every component test listener on its private address; VM SSH reports that it is proxying through Catch and succeeds with the guest key. A LAN workstation without the proxy cannot route directly to the addresses.

- [ ] **Step 9: Prove permitted and denied workload egress**

Run the following inside `iso-accept-a`'s `probe` container using Catch's host shell and Docker's Compose project/service labels:

```bash
mise exec -- go run ./cmd/yeet ssh -- sh -lc 'cid=$(docker ps -q --filter label=com.docker.compose.project=catch-iso-accept-a --filter label=com.docker.compose.service=probe); docker exec "$cid" sh -ec "curl -4fsS https://example.com >/dev/null; nslookup example.com >/dev/null"'
mise exec -- go run ./cmd/yeet ssh -- sh -lc 'cid=$(docker ps -q --filter label=com.docker.compose.project=catch-iso-accept-a --filter label=com.docker.compose.service=probe); docker exec "$cid" sh -ec "! nc -zvw2 192.168.100.1 53; ! nc -zvw2 169.254.169.254 80; ! nc -zvw2 100.100.100.100 53; ! nc -zvw2 8.8.8.8 53; ! nc -zvw2 1.1.1.1 853"'
```

Also test the recorded Catch-side `/30` address, one address from `iso-accept-b`, and one real LAN RFC1918 address known to the operator. Expected: HTTPS and ISO DNS succeed; Catch, `svc`, metadata, CGNAT, other ISO, LAN, direct DNS, and DoT fail promptly. `ip -6 route` shows no usable ISO IPv6 default and IPv6 connection attempts fail.

- [ ] **Step 10: Prove same-project and Tailscale exceptions**

From `iso-accept-a/probe`, connect to `iso-accept-a/web` on port 80 by its stable component address; expect success. Connect to `iso-accept-b/web`; expect failure. From `iso-accept-ts`, resolve a MagicDNS name and reach an authorized tailnet target; then query a public IP service and verify ordinary egress still uses Catch's ISO NAT rather than a Tailscale exit node. Confirm the tailnet connection is attributed to `iso-accept-ts`, not Catch.

- [ ] **Step 11: Prove restart stability, drift quarantine, and cleanup tombstones**

Record all addresses, restart the workloads, Docker, and Catch one at a time, and confirm addresses remain unchanged after each restart. Deliberately remove one Yeet-owned ISO firewall entry; restart the workload and expect quarantine until reconciliation repairs and verifies the policy. Inject a cleanup failure by holding one test endpoint, remove the service, and expect a visible tombstone with no address reuse; release the endpoint, rerun removal/reconciliation, and expect the tombstone to clear.

- [ ] **Step 12: Verify unsafe Compose fails before container creation**

Copy the fixture to a disposable test definition and, one at a time, add `network_mode: host`, `privileged: true`, `ports`, `/var/run/docker.sock`, an out-of-root bind, custom DNS, `build`, `deploy.replicas: 2`, and an unknown service key. Run each as `--net=iso`. Expected: a field-path admission error before `docker ps -a` shows any container or ISO Docker network for that project.

- [ ] **Step 13: Remove all live acceptance services and verify release**

Run:

```bash
mise exec -- go run ./cmd/yeet rm iso-accept-a iso-accept-b iso-accept-ts iso-accept-vm
mise exec -- go run ./cmd/yeet info catch
```

Expected: the services are absent, active/reserved/tombstoned counts return to their pre-test values, and no test interface, route, namespace, Docker network, firewall member, or Tailscale state remains.

- [ ] **Step 14: Commit the acceptance fixture and any gate-driven fixes**

Run:

```bash
IDS=$(but diff --format json | jq -r --args '$ARGS.positional as $paths | [.changes[] | select(.path as $path | $paths | index($path)) | .id] | join(",")' -- example/iso-acceptance/compose.yml example/iso-acceptance/README.md)
test -n "$IDS"
but commit codex/iso-network-design -m "test: add isolated network acceptance fixture" --changes "$IDS"
```

If a gate required implementation changes, commit each coherent fix with its regression test before the fixture commit, selecting only that fix's files through the same JSON-ID method. Expected final state: `but status` shows the isolated-network stack and only the unrelated `.codex/skills/gitbutler` edits as uncommitted; nothing is pushed.

## Completion Evidence

Before reporting implementation complete, capture:

- the commit list for `codex/iso-network-design` and confirmation that every task commit contains only its declared files;
- targeted, race, fuzz, privileged integration, full test, pre-commit, and `quality:goal` exit results;
- the selected live pool and endpoint map with private host/service names omitted from public artifacts;
- the live allow/deny matrix, restart-stability results, quarantine repair, and tombstone cleanup result;
- local website commit and root gitlink state, explicitly marked unpushed;
- `origin/main` unchanged unless the user separately authorized landing/pushing.
