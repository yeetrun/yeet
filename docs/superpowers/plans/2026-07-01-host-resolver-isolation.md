# Host Resolver Isolation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prevent yeet-managed service DHCP, service Tailscale sidecars, and catch DNS forwarding from corrupting or looping through the catch host's `/etc/resolv.conf`.

**Architecture:** Treat the host resolver file as host-owned state. Every yeet-created network namespace gets a namespace-local `/etc/netns/<name>/resolv.conf`; service units and Tailscale sidecars bind that file over `/etc/resolv.conf`, DHCP resolver updates are redirected into that file, and `catch dns` refuses to forward through its own listener address.

**Tech Stack:** Go, Bash, systemd unit generation, Linux network namespaces, ISC dhclient hooks, `github.com/miekg/dns`, Go `testing`, GitButler.

---

## File Structure

- Modify `pkg/catch/dns.go`
  - Add resolver filtering so `catch dns` never forwards external queries to `192.168.100.1`, the yeet DNS listener address.
- Modify `pkg/catch/dns_test.go`
  - Add tests proving self-referential upstreams are skipped and all-self upstream lists fail cleanly.
- Modify `pkg/netns/netns-scripts/service-ns`
  - Ensure `/etc/netns/<service-ns>/resolv.conf` exists before DHCP starts.
  - Pass `YEET_NETNS_NAME=<service-ns>` into service `dhclient` invocations.
- Create `pkg/netns/netns-scripts/dhclient-enter-hook-yeet-netns-resolv`
  - Override dhclient's `make_resolv_conf` only for yeet-launched DHCP clients and write DHCP resolver data into `/etc/netns/<service-ns>/resolv.conf`.
- Modify `pkg/netns/netns.go`
  - Install the embedded dhclient enter hook into `/etc/dhcp/dhclient-enter-hooks.d/yeet-netns-resolv` as part of `InstallYeetNSService`.
  - Keep script installation idempotent and testable with package-level path/function variables.
- Modify `pkg/netns/netns_test.go`
  - Add tests for dhclient hook installation and service DHCP command wiring.
- Modify `pkg/catch/installer_file.go`
  - Represent the runtime resolver bind path explicitly in `networkConfig`.
  - Bind `/etc/netns/<service-ns>/resolv.conf` for every non-host network namespace service unit.
  - Pass the same runtime resolver bind path into every non-tap Tailscale sidecar.
- Modify `pkg/catch/installer_file_test.go`
  - Replace the current LAN+TS "no bind" expectation with tests requiring resolver isolation for LAN+TS.
  - Add tests for LAN-only service resolver binding.
- Modify `pkg/catch/tsns_test.go`
  - Keep existing Tailscale sidecar unit tests aligned with the new resolver bind behavior.
- Modify `pkg/catch/netns_reconcile.go`
  - Add a startup reconciliation pass that repairs already-installed Tailscale sidecar units missing resolver isolation and restarts only repaired sidecars.
- Modify `pkg/catch/netns_reconcile_test.go`
  - Add migration tests for missing `BindPaths` / `PrivateMounts`, already-safe units, and non-Tailscale services.
- Modify `pkg/catch/catch.go`
  - Call the resolver isolation reconciliation from `reconcileRuntimeState` before restarting sidecars for stale network namespaces.

## Task 0: Workspace Setup

**Files:**
- Read: `AGENTS.md`
- Read: `AGENTS.local.md`
- Read: `docs/agent/codebase-map.md`

- [ ] **Step 1: Verify the workspace is safe to start**

Run:

```bash
but pull --check
```

Expected: GitButler reports the workspace can be updated cleanly or reports no update is needed. If it reports conflicts or another active branch touching the same files, stop and ask the user before continuing.

- [ ] **Step 2: Inspect current dirty work**

Run:

```bash
but diff
```

Expected: Either no local changes, or only changes that clearly belong to this resolver-isolation work after the plan starts.

## Task 1: Guard `catch dns` Against Self-Referential Upstreams

**Files:**
- Modify: `pkg/catch/dns.go`
- Test: `pkg/catch/dns_test.go`

- [ ] **Step 1: Write the failing DNS loop tests**

Append these tests near the existing `forwardDNSViaResolverConfig` tests in `pkg/catch/dns_test.go`:

```go
func TestForwardDNSViaResolverConfigSkipsYeetDNSHostResolver(t *testing.T) {
	req := newAQuestion("example.com.")
	response := new(dns.Msg)
	response.SetReply(req)
	var addrs []string

	got, err := forwardDNSViaResolverConfig(context.Background(), req, &dns.ClientConfig{
		Servers: []string{yeetDNSHostIP, "192.0.2.53"},
		Port:    "53",
	}, func(_ context.Context, _ *dns.Msg, addr string) (*dns.Msg, error) {
		addrs = append(addrs, addr)
		return response, nil
	})

	if err != nil {
		t.Fatalf("forwardDNSViaResolverConfig returned error: %v", err)
	}
	if got != response {
		t.Fatalf("response = %p, want %p", got, response)
	}
	want := []string{"192.0.2.53:53"}
	if !reflect.DeepEqual(addrs, want) {
		t.Fatalf("addrs = %#v, want %#v", addrs, want)
	}
}

func TestForwardDNSViaResolverConfigRejectsOnlyYeetDNSHostResolver(t *testing.T) {
	req := newAQuestion("example.com.")
	var addrs []string

	_, err := forwardDNSViaResolverConfig(context.Background(), req, &dns.ClientConfig{
		Servers: []string{yeetDNSHostIP},
		Port:    "53",
	}, func(_ context.Context, _ *dns.Msg, addr string) (*dns.Msg, error) {
		addrs = append(addrs, addr)
		return nil, fmt.Errorf("unexpected exchange through %s", addr)
	})

	if err == nil || !strings.Contains(err.Error(), "no usable upstream DNS servers") {
		t.Fatalf("error = %v, want no usable upstream DNS servers", err)
	}
	if len(addrs) != 0 {
		t.Fatalf("addrs = %#v, want no exchange attempts", addrs)
	}
}
```

Add imports if they are missing:

```go
import (
	"fmt"
	"reflect"
	"strings"
)
```

- [ ] **Step 2: Run the failing tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestForwardDNSViaResolverConfig(SkipsYeetDNSHostResolver|RejectsOnlyYeetDNSHostResolver)' -count=1
```

Expected: FAIL. The first test attempts `192.168.100.1:53`, and the second test reports an unexpected exchange or does not return the desired error.

- [ ] **Step 3: Implement the resolver filter**

In `pkg/catch/dns.go`, add this helper after `forwardDNSViaResolverConfig`:

```go
func usableHostResolverServers(servers []string) []string {
	out := make([]string, 0, len(servers))
	for _, server := range servers {
		server = strings.TrimSpace(server)
		if server == "" || isYeetDNSSelfResolver(server) {
			continue
		}
		out = append(out, server)
	}
	return out
}

func isYeetDNSSelfResolver(server string) bool {
	server = strings.Trim(server, "[]")
	addr, err := netip.ParseAddr(server)
	if err != nil {
		return server == yeetDNSHostIP
	}
	return addr == netip.MustParseAddr(yeetDNSHostIP)
}
```

Then update `forwardDNSViaResolverConfig` so it filters host resolver servers before forwarding:

```go
func forwardDNSViaResolverConfig(ctx context.Context, req *dns.Msg, cfg *dns.ClientConfig, exchange dnsExchangeFunc) (*dns.Msg, error) {
	if cfg == nil {
		return nil, fmt.Errorf("host resolver config is nil")
	}
	servers := usableHostResolverServers(cfg.Servers)
	port := cfg.Port
	if port == "" {
		port = "53"
	}
	if shouldForwardViaTailscaleDNS(req, cfg.Search) {
		servers = []string{tailscaleDNSIP}
		port = "53"
	}
	if len(servers) == 0 {
		return nil, fmt.Errorf("no usable upstream DNS servers after filtering yeet DNS self resolver %s", yeetDNSHostIP)
	}
	return forwardDNSViaServers(ctx, req, servers, port, exchange)
}
```

- [ ] **Step 4: Run the DNS tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestForwardDNSViaResolverConfig|TestForwardDNSViaServers|TestYeetDNSHandler' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit the DNS guard**

Run:

```bash
but diff
```

Confirm only `pkg/catch/dns.go` and `pkg/catch/dns_test.go` are included in this task's diff, then run:

```bash
but commit codex/host-resolver-isolation -c -m "catch: guard DNS forwarding loops"
```

Expected: GitButler creates `codex/host-resolver-isolation` with one commit.

## Task 2: Redirect Service DHCP Resolver Writes Into `/etc/netns`

**Files:**
- Create: `pkg/netns/netns-scripts/dhclient-enter-hook-yeet-netns-resolv`
- Modify: `pkg/netns/netns-scripts/service-ns`
- Modify: `pkg/netns/netns.go`
- Test: `pkg/netns/netns_test.go`

- [ ] **Step 1: Add failing tests for service DHCP wiring**

Replace `TestServiceNSScriptUsesPerServiceDhclientLeaseFile` in `pkg/netns/netns_test.go` with this stronger version:

```go
func TestServiceNSScriptUsesPerServiceDhclientLeaseFileAndNetNSResolverHook(t *testing.T) {
	raw, err := netnsScripts.ReadFile("netns-scripts/service-ns")
	if err != nil {
		t.Fatalf("ReadFile embedded service-ns returned error: %v", err)
	}
	got := string(raw)

	for _, want := range []string{
		`DHCP_LEASEFILE="/var/lib/dhcp/dhclient-${SERVICE_NAME}-${MACVLAN_INTERFACE}.leases"`,
		`DHCP="dhclient -e YEET_NETNS_NAME=${NS_NAME} -pf ${DHCP_PIDFILE} -lf ${DHCP_LEASEFILE}"`,
		`DHCP_RELEASE="dhclient -e YEET_NETNS_NAME=${NS_NAME} -r -pf ${DHCP_PIDFILE} -lf ${DHCP_LEASEFILE}"`,
		`mkdir -p "/etc/netns/$NS_NAME"`,
		`cp /etc/resolv.conf "/etc/netns/$NS_NAME/resolv.conf"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("service-ns missing %q:\n%s", want, got)
		}
	}
}
```

Add this new test in `pkg/netns/netns_test.go`:

```go
func TestDhclientEnterHookRedirectsResolverWritesForYeetNetNS(t *testing.T) {
	raw, err := netnsScripts.ReadFile("netns-scripts/dhclient-enter-hook-yeet-netns-resolv")
	if err != nil {
		t.Fatalf("ReadFile embedded dhclient hook returned error: %v", err)
	}
	got := string(raw)

	for _, want := range []string{
		`if [ -n "${YEET_NETNS_NAME:-}" ]; then`,
		`make_resolv_conf() {`,
		`target="/etc/netns/${YEET_NETNS_NAME}/resolv.conf"`,
		`printf 'nameserver %s\n' "$server"`,
		`mv "$tmp" "$target"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("dhclient hook missing %q:\n%s", want, got)
		}
	}
}
```

- [ ] **Step 2: Add failing tests for hook installation**

Add this test in `pkg/netns/netns_test.go`:

```go
func TestWriteDhclientEnterHookInstallsHookIdempotently(t *testing.T) {
	dir := t.TempDir()
	oldPath := dhclientEnterHookPath
	oldMkdirAll := mkdirAll
	dhclientEnterHookPath = filepath.Join(dir, "hooks", "yeet-netns-resolv")
	mkdirAll = os.MkdirAll
	t.Cleanup(func() {
		dhclientEnterHookPath = oldPath
		mkdirAll = oldMkdirAll
	})

	changed, err := writeDhclientEnterHook()
	if err != nil {
		t.Fatalf("writeDhclientEnterHook first call returned error: %v", err)
	}
	if !changed {
		t.Fatal("writeDhclientEnterHook first call changed = false, want true")
	}
	raw, err := os.ReadFile(dhclientEnterHookPath)
	if err != nil {
		t.Fatalf("read installed hook: %v", err)
	}
	if !strings.Contains(string(raw), "YEET_NETNS_NAME") {
		t.Fatalf("installed hook missing YEET_NETNS_NAME:\n%s", raw)
	}
	info, err := os.Stat(dhclientEnterHookPath)
	if err != nil {
		t.Fatalf("stat installed hook: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("hook mode = %v, want 0644", got)
	}

	changed, err = writeDhclientEnterHook()
	if err != nil {
		t.Fatalf("writeDhclientEnterHook second call returned error: %v", err)
	}
	if changed {
		t.Fatal("writeDhclientEnterHook second call changed = true, want false")
	}
}
```

Add imports if missing:

```go
import (
	"os"
	"path/filepath"
)
```

- [ ] **Step 3: Run the failing netns tests**

Run:

```bash
mise exec -- go test ./pkg/netns -run 'TestServiceNSScriptUsesPerServiceDhclientLeaseFileAndNetNSResolverHook|TestDhclientEnterHookRedirectsResolverWritesForYeetNetNS|TestWriteDhclientEnterHookInstallsHookIdempotently' -count=1
```

Expected: FAIL because the embedded hook file and installation function do not exist, and `service-ns` does not pass `YEET_NETNS_NAME` to dhclient.

- [ ] **Step 4: Create the dhclient enter hook**

Create `pkg/netns/netns-scripts/dhclient-enter-hook-yeet-netns-resolv`:

```sh
# yeet netns dhclient resolver redirect.
#
# /sbin/dhclient-script sources enter hooks after defining make_resolv_conf()
# and before calling it for BOUND, RENEW, REBIND, and REBOOT events. Only yeet
# service DHCP clients pass YEET_NETNS_NAME, so host DHCP clients keep the
# distro default behavior.
if [ -n "${YEET_NETNS_NAME:-}" ]; then
    make_resolv_conf() {
        target="/etc/netns/${YEET_NETNS_NAME}/resolv.conf"
        target_dir="$(dirname "$target")"
        tmp="${target}.tmp"

        mkdir -p "$target_dir"
        : > "$tmp"

        if [ -n "${new_domain_name:-}" ]; then
            printf 'domain %s\n' "$new_domain_name" >> "$tmp"
        fi

        if [ -n "${new_domain_search:-}" ]; then
            printf 'search %s\n' "$new_domain_search" >> "$tmp"
        elif [ -n "${new_domain_name:-}" ]; then
            printf 'search %s\n' "$new_domain_name" >> "$tmp"
        fi

        for server in ${new_domain_name_servers:-}; do
            printf 'nameserver %s\n' "$server" >> "$tmp"
        done

        mv "$tmp" "$target"
    }
fi
```

- [ ] **Step 5: Update `service-ns` to pass the hook environment**

Modify the dhclient command setup in `pkg/netns/netns-scripts/service-ns`:

```sh
DHCP_AVAILABLE=true
DHCP="dhcpcd --nohook resolv.conf"
DHCP_RELEASE="dhcpcd -k"
DHCP_PIDFILE="/var/run/dhclient-${SERVICE_NAME}-${MACVLAN_INTERFACE}.pid"
DHCP_LEASEFILE="/var/lib/dhcp/dhclient-${SERVICE_NAME}-${MACVLAN_INTERFACE}.leases"

if ! command -v dhcpcd &> /dev/null; then
    if ! command -v dhclient &> /dev/null; then
        DHCP_AVAILABLE=false
    else
        DHCP="dhclient -e YEET_NETNS_NAME=${NS_NAME} -pf ${DHCP_PIDFILE} -lf ${DHCP_LEASEFILE}"
        DHCP_RELEASE="dhclient -e YEET_NETNS_NAME=${NS_NAME} -r -pf ${DHCP_PIDFILE} -lf ${DHCP_LEASEFILE}"
    fi
fi
```

After `ip netns add $NS_NAME`, create the namespace resolver directory before DHCP can run:

```sh
# Prepare the namespace resolver path before DHCP starts. DHCP resolver updates
# are redirected here by yeet's dhclient enter hook. If the host uses dhcpcd,
# --nohook resolv.conf prevents host mutation and this seed gives the namespace
# a resolver until a future dhcpcd-specific redirect is needed.
mkdir -p "/etc/netns/$NS_NAME"
if [ ! -s "/etc/netns/$NS_NAME/resolv.conf" ] && [ -r /etc/resolv.conf ]; then
    cp /etc/resolv.conf "/etc/netns/$NS_NAME/resolv.conf"
fi
```

Keep the existing explicit resolver copy at the end:

```sh
if [ -n "$RESOLV_CONF" ]; then
    mkdir -p /etc/netns/$NS_NAME
    cp $RESOLV_CONF "/etc/netns/$NS_NAME/resolv.conf"
fi
```

- [ ] **Step 6: Install the dhclient hook from `pkg/netns/netns.go`**

In `pkg/netns/netns.go`, add package-level variables beside the existing vars:

```go
var (
	executablePath = os.Executable
	mkdirAll       = os.MkdirAll
	writeFile      = os.WriteFile
	chmodFile      = os.Chmod
	readFile       = os.ReadFile

	dhclientEnterHookPath = "/etc/dhcp/dhclient-enter-hooks.d/yeet-netns-resolv"

	systemdUnitPath = func(unit string) string {
		return filepath.Join("/etc/systemd/system", unit)
	}
	newYeetNSSystemdService = func(cfg db.ServiceView, runDir string) (yeetNSServiceInstaller, error) {
		return svc.NewSystemdService(nil, cfg, runDir)
	}
	systemdUnitActive = func(unit string) bool {
		return exec.Command("systemctl", "is-active", "--quiet", unit).Run() == nil
	}
)
```

Update `writeNetNSScript` to use `readFile`, `writeFile`, and `chmodFile` instead of direct `os.ReadFile`, `os.WriteFile`, and `os.Chmod` calls.

Add this helper:

```go
func writeDhclientEnterHook() (bool, error) {
	raw, err := netnsScripts.ReadFile("netns-scripts/dhclient-enter-hook-yeet-netns-resolv")
	if err != nil {
		return false, fmt.Errorf("failed to read dhclient enter hook: %v", err)
	}
	if err := mkdirAll(filepath.Dir(dhclientEnterHookPath), 0o755); err != nil {
		return false, fmt.Errorf("failed to create dhclient hook dir: %v", err)
	}
	prev, err := readFile(dhclientEnterHookPath)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("failed to read previous dhclient hook: %v", err)
	}
	if err == nil && bytes.Equal(prev, raw) {
		return false, nil
	}
	if err := writeFile(dhclientEnterHookPath, raw, 0o644); err != nil {
		return false, fmt.Errorf("failed to write dhclient hook: %v", err)
	}
	return true, nil
}
```

Update `InstallYeetNSService` to install the hook and include it in the change check:

```go
func InstallYeetNSService() error {
	scriptsChanged, err := writeNetNSScripts()
	if err != nil {
		return fmt.Errorf("failed to write netns scripts: %v", err)
	}
	dhclientHookChanged, err := writeDhclientEnterHook()
	if err != nil {
		return err
	}
	backend, err := DetectFirewallBackend()
	if err != nil {
		return fmt.Errorf("failed to detect firewall backend: %v", err)
	}
	catchBin, err := executablePath()
	if err != nil {
		return fmt.Errorf("failed to resolve catch binary path: %v", err)
	}
	envChanged, err := writeYeetNSEnv(defaultYeetNSEnv(backend, catchBin))
	if err != nil {
		return err
	}

	unitFiles, err := newYeetNSUnit().WriteOutUnitFiles(".")
	if err != nil {
		return fmt.Errorf("failed to write unit files: %v", err)
	}
	defer removeFiles(unitFiles)

	unitChanged, err := yeetNSUnitChanged(unitFiles[db.ArtifactSystemdUnit])
	if err != nil {
		return err
	}
	if !anyChanged(scriptsChanged, dhclientHookChanged, envChanged, unitChanged) {
		return nil
	}
	if err := installYeetNSService(unitFiles); err != nil {
		return err
	}
	return nil
}
```

- [ ] **Step 7: Run the netns tests**

Run:

```bash
mise exec -- go test ./pkg/netns -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit the DHCP resolver redirection**

Run:

```bash
but diff
```

Confirm only `pkg/netns/netns.go`, `pkg/netns/netns_test.go`, `pkg/netns/netns-scripts/service-ns`, and `pkg/netns/netns-scripts/dhclient-enter-hook-yeet-netns-resolv` are included in this task's diff, then run:

```bash
but commit codex/host-resolver-isolation -m "netns: redirect service DHCP resolver writes"
```

Expected: GitButler appends a second commit to `codex/host-resolver-isolation`.

## Task 3: Bind Namespace Resolver Files For All Non-Host Service Units

**Files:**
- Modify: `pkg/catch/installer_file.go`
- Test: `pkg/catch/installer_file_test.go`
- Test: `pkg/catch/tsns_test.go`

- [ ] **Step 1: Write failing installer tests for LAN resolver isolation**

Replace `TestInstallerCloseStagesComposeLANOverlayWithoutYeetDNS` if it only asserts no DNS overlay for Docker compose; keep its Docker compose assertion but add systemd resolver assertions. Add this focused test in `pkg/catch/installer_file_test.go`:

```go
func TestNewSystemdUnitBindsResolverForLANNetNS(t *testing.T) {
	server := newTestServer(t)
	if err := server.ensureDirs("lan-systemd", ""); err != nil {
		t.Fatalf("ensureDirs returned error: %v", err)
	}
	oldHostDefaultRouteInterfaceFn := hostDefaultRouteInterfaceFn
	hostDefaultRouteInterfaceFn = func() (string, error) {
		return "vmbr0", nil
	}
	t.Cleanup(func() {
		hostDefaultRouteInterfaceFn = oldHostDefaultRouteInterfaceFn
	})

	installer := &FileInstaller{
		s: server,
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{ServiceName: "lan-systemd"},
			Network:      NetworkOpts{Interfaces: "lan"},
		},
	}

	unit, err := installer.newSystemdUnit(filepath.Join(server.serviceRunDir("lan-systemd"), "lan-systemd"))
	if err != nil {
		t.Fatalf("newSystemdUnit returned error: %v", err)
	}
	if unit.NetNS != "yeet-lan-systemd-ns" {
		t.Fatalf("unit NetNS = %q, want yeet-lan-systemd-ns", unit.NetNS)
	}
	if unit.ResolvConf != "/etc/netns/yeet-lan-systemd-ns/resolv.conf" {
		t.Fatalf("unit ResolvConf = %q, want LAN netns resolver bind", unit.ResolvConf)
	}
}
```

Replace `TestInstallerCloseStagesComposeLANOverlayWithoutYeetDNS`'s Tailscale unit expectation at `pkg/catch/installer_file_test.go:1266` with this test:

```go
func TestInstallerCloseStagesComposeLANTailscaleUnitBindsNetNSResolver(t *testing.T) {
	oldHostDefaultRouteInterfaceFn := hostDefaultRouteInterfaceFn
	hostDefaultRouteInterfaceFn = func() (string, error) {
		return "vmbr0", nil
	}
	t.Cleanup(func() {
		hostDefaultRouteInterfaceFn = oldHostDefaultRouteInterfaceFn
	})

	server := newTestServer(t)
	const (
		service = "lan-ts"
		version = "1.92.3"
	)
	if err := server.ensureDirs(service, ""); err != nil {
		t.Fatalf("ensureDirs: %v", err)
	}
	tsdDir := filepath.Join(server.cfg.RootDir, "tsd")
	if err := os.MkdirAll(tsdDir, 0o755); err != nil {
		t.Fatalf("mkdir tsd dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tsdDir, "tailscaled-"+version), []byte("daemon"), 0o755); err != nil {
		t.Fatalf("write tailscaled: %v", err)
	}
	installer := &FileInstaller{
		s:           server,
		serviceRoot: server.defaultServiceRootDir(service),
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{ServiceName: service},
			Network: NetworkOpts{
				Interfaces: "lan,ts",
				Macvlan:    MacvlanOpts{Parent: "vmbr0"},
				Tailscale: TailscaleOpts{
					AuthKey: "tskey-auth-test",
					Version: version,
				},
			},
		},
	}

	if _, err := installer.configureNetwork(); err != nil {
		t.Fatalf("configureNetwork: %v", err)
	}
	tsUnitPath := installer.artifacts[db.ArtifactTSService]
	if tsUnitPath == "" {
		t.Fatalf("tailscale service artifact missing: %#v", installer.artifacts)
	}
	raw, err := os.ReadFile(tsUnitPath)
	if err != nil {
		t.Fatalf("read tailscale unit: %v", err)
	}
	unit := string(raw)
	for _, want := range []string{
		"NetworkNamespacePath=/var/run/netns/yeet-lan-ts-ns",
		"BindPaths=/etc/netns/yeet-lan-ts-ns/resolv.conf:/etc/resolv.conf",
		"PrivateMounts=yes",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("tailscale unit missing %q:\n%s", want, unit)
		}
	}
}
```

- [ ] **Step 2: Run the failing catch installer tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestNewSystemdUnitBindsResolverForLANNetNS|TestInstallerCloseStagesComposeLANTailscaleUnitBindsNetNSResolver' -count=1
```

Expected: FAIL because LAN service units and LAN+TS sidecar units do not always bind `/etc/netns/<name>/resolv.conf`.

- [ ] **Step 3: Implement explicit runtime resolver paths**

In `pkg/catch/installer_file.go`, replace `networkConfig` with:

```go
type networkConfig struct {
	NetNS      string
	Deps       []string
	ResolvConf string
}
```

Add this helper near `buildNetNSResolvConf`:

```go
func runtimeNetNSResolvConf(netNS string) string {
	netNS = strings.TrimSpace(netNS)
	if netNS == "" {
		return ""
	}
	return fmt.Sprintf("/etc/netns/%s/resolv.conf", netNS)
}
```

Update `configureNetworkOnce`:

```go
return &networkConfig{
	NetNS:      env.NetNS(),
	Deps:       deps,
	ResolvConf: runtimeNetNSResolvConf(env.NetNS()),
}, nil
```

Update `installTailscaleForNetNS`:

```go
func (i *FileInstaller) installTailscaleForNetNS(env netns.Service, runTSInNetNS string, tsTapMode bool) error {
	rc := ""
	if !tsTapMode && strings.TrimSpace(runTSInNetNS) != "" {
		rc = runtimeNetNSResolvConf(runTSInNetNS)
	}
	files, err := i.s.installTSAtRoot(i.effectiveServiceRoot(), i.cfg.ServiceName, runTSInNetNS, i.tsNet, i.tsAuthKey, rc)
	if err != nil {
		return fmt.Errorf("failed to install tailscale: %v", err)
	}
	i.setArtifacts(files)
	return nil
}
```

Update `applyNetworkToSystemdUnit`:

```go
func (i *FileInstaller) applyNetworkToSystemdUnit(su *svc.SystemdUnit) error {
	n, err := i.configureNetwork()
	if err != nil {
		return fmt.Errorf("failed to configure network: %v", err)
	}
	if n == nil {
		return nil
	}
	su.NetNS = n.NetNS
	su.Requires = strings.Join(n.Deps, " ")
	if n.ResolvConf != "" {
		su.ResolvConf = n.ResolvConf
	}
	return nil
}
```

- [ ] **Step 4: Run catch installer and Tailscale unit tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestNewSystemdUnit.*Resolv|TestInstaller.*Tailscale|TestInstallTSWritesArtifactsWithoutNetworkWhenAuthKeyProvided|TestNewTailscaleSystemdUnit' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit resolver binding changes**

Run:

```bash
but diff
```

Confirm only `pkg/catch/installer_file.go`, `pkg/catch/installer_file_test.go`, and `pkg/catch/tsns_test.go` are included in this task's diff, then run:

```bash
but commit codex/host-resolver-isolation -m "catch: bind resolv.conf for network namespace services"
```

Expected: GitButler appends a third commit to `codex/host-resolver-isolation`.

## Task 4: Repair Existing Tailscale Sidecar Units At Runtime

**Files:**
- Modify: `pkg/catch/netns_reconcile.go`
- Modify: `pkg/catch/netns_reconcile_test.go`
- Modify: `pkg/catch/catch.go`

- [ ] **Step 1: Write failing migration tests**

Add this test to `pkg/catch/netns_reconcile_test.go` near the Tailscale DNS config reconciliation tests:

```go
func TestReconcileTailscaleResolverIsolationRepairsMissingBind(t *testing.T) {
	s := newTestServer(t)
	root := filepath.Join(t.TempDir(), "services", "api")
	unitPath := filepath.Join(t.TempDir(), "yeet-api-ts.service")
	if err := os.WriteFile(unitPath, []byte(`[Unit]
After=yeet-api-ns.service

[Service]
ExecStart=/srv/api/run/tailscaled --tun=ts0
NetworkNamespacePath=/var/run/netns/yeet-api-ns

[Install]
WantedBy=multi-user.target
`), 0o644); err != nil {
		t.Fatalf("write unit: %v", err)
	}
	addTestServices(t, s, db.Service{
		Name:             "api",
		ServiceType:      db.ServiceTypeDockerCompose,
		ServiceRoot:      root,
		Generation:       3,
		LatestGeneration: 3,
		Artifacts: db.ArtifactStore{
			db.ArtifactTSService:    {Refs: map[db.ArtifactRef]string{db.Gen(3): unitPath}},
			db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(3): "/tmp/yeet-api-ns.service"}},
		},
	})

	var calls []string
	prevSystemctl := catchSystemctl
	catchSystemctl = func(args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		return nil
	}
	t.Cleanup(func() {
		catchSystemctl = prevSystemctl
	})

	if err := s.reconcileTailscaleResolverIsolation(context.Background()); err != nil {
		t.Fatalf("reconcileTailscaleResolverIsolation returned error: %v", err)
	}
	raw, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("read repaired unit: %v", err)
	}
	unit := string(raw)
	for _, want := range []string{
		"BindPaths=/etc/netns/yeet-api-ns/resolv.conf:/etc/resolv.conf",
		"PrivateMounts=yes",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("repaired unit missing %q:\n%s", want, unit)
		}
	}
	wantCalls := []string{"daemon-reload", "restart yeet-api-ts.service"}
	if diff := cmp.Diff(wantCalls, calls); diff != "" {
		t.Fatalf("systemctl calls (-want +got):\n%s", diff)
	}
}
```

Add this idempotence test:

```go
func TestReconcileTailscaleResolverIsolationSkipsSafeUnit(t *testing.T) {
	s := newTestServer(t)
	root := filepath.Join(t.TempDir(), "services", "api")
	unitPath := filepath.Join(t.TempDir(), "yeet-api-ts.service")
	unitRaw := []byte(`[Unit]
After=yeet-api-ns.service

[Service]
ExecStart=/srv/api/run/tailscaled --tun=ts0
NetworkNamespacePath=/var/run/netns/yeet-api-ns
BindPaths=/etc/netns/yeet-api-ns/resolv.conf:/etc/resolv.conf
PrivateMounts=yes

[Install]
WantedBy=multi-user.target
`)
	if err := os.WriteFile(unitPath, unitRaw, 0o644); err != nil {
		t.Fatalf("write unit: %v", err)
	}
	addTestServices(t, s, db.Service{
		Name:             "api",
		ServiceType:      db.ServiceTypeDockerCompose,
		ServiceRoot:      root,
		Generation:       3,
		LatestGeneration: 3,
		Artifacts: db.ArtifactStore{
			db.ArtifactTSService:    {Refs: map[db.ArtifactRef]string{db.Gen(3): unitPath}},
			db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(3): "/tmp/yeet-api-ns.service"}},
		},
	})

	prevSystemctl := catchSystemctl
	catchSystemctl = func(args ...string) error {
		t.Fatalf("unexpected systemctl call: %v", args)
		return nil
	}
	t.Cleanup(func() {
		catchSystemctl = prevSystemctl
	})

	if err := s.reconcileTailscaleResolverIsolation(context.Background()); err != nil {
		t.Fatalf("reconcileTailscaleResolverIsolation returned error: %v", err)
	}
	raw, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("read unit: %v", err)
	}
	if string(raw) != string(unitRaw) {
		t.Fatalf("safe unit changed:\n%s", raw)
	}
}
```

- [ ] **Step 2: Run the failing migration tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestReconcileTailscaleResolverIsolation' -count=1
```

Expected: FAIL because `reconcileTailscaleResolverIsolation` does not exist.

- [ ] **Step 3: Implement unit repair helpers**

In `pkg/catch/netns_reconcile.go`, add these helpers after `reconcileTailscaleDNSConfigs`:

```go
func (s *Server) reconcileTailscaleResolverIsolation(ctx context.Context) error {
	dv, err := s.getDB()
	if err != nil {
		return err
	}

	var errs []error
	var repaired bool
	for name, sv := range dv.Services().All() {
		if err := ctx.Err(); err != nil {
			return err
		}
		changed, err := reconcileTailscaleResolverIsolationForService(sv.AsStruct())
		if err != nil {
			log.Printf("tailscale resolver isolation reconciliation failed for service %q: %v", name, err)
			errs = append(errs, err)
			continue
		}
		if changed {
			repaired = true
			if err := restartTailscaleSidecarForService(name); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if repaired {
		if err := catchSystemctl("daemon-reload"); err != nil {
			errs = append(errs, fmt.Errorf("systemctl daemon-reload: %w", err))
		}
	}
	return errors.Join(errs...)
}

func reconcileTailscaleResolverIsolationForService(service db.Service) (bool, error) {
	if _, ok := service.Artifacts.Gen(db.ArtifactTSService, service.Generation); !ok {
		return false, nil
	}
	if _, ok := service.Artifacts.Gen(db.ArtifactNetNSService, service.Generation); !ok {
		return false, nil
	}
	unitPath, ok := service.Artifacts.Gen(db.ArtifactTSService, service.Generation)
	if !ok || strings.TrimSpace(unitPath) == "" {
		return false, nil
	}
	raw, err := os.ReadFile(unitPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read tailscale unit %s: %w", unitPath, err)
	}
	next, changed := ensureTailscaleUnitResolverIsolation(string(raw), "yeet-"+service.Name+"-ns")
	if !changed {
		return false, nil
	}
	if err := os.WriteFile(unitPath, []byte(next), 0o644); err != nil {
		return false, fmt.Errorf("write tailscale unit %s: %w", unitPath, err)
	}
	return true, nil
}

func ensureTailscaleUnitResolverIsolation(unit, netNS string) (string, bool) {
	bind := fmt.Sprintf("BindPaths=/etc/netns/%s/resolv.conf:/etc/resolv.conf", netNS)
	hasBind := strings.Contains(unit, bind)
	hasPrivateMounts := strings.Contains(unit, "PrivateMounts=yes")
	if hasBind && hasPrivateMounts {
		return unit, false
	}

	lines := strings.Split(unit, "\n")
	var out []string
	inserted := false
	for _, line := range lines {
		if line == "[Install]" && !inserted {
			if !hasBind {
				out = append(out, bind)
			}
			if !hasPrivateMounts {
				out = append(out, "PrivateMounts=yes")
			}
			out = append(out, "")
			inserted = true
		}
		out = append(out, line)
	}
	if !inserted {
		if len(out) > 0 && out[len(out)-1] != "" {
			out = append(out, "")
		}
		if !hasBind {
			out = append(out, bind)
		}
		if !hasPrivateMounts {
			out = append(out, "PrivateMounts=yes")
		}
	}
	return strings.Join(out, "\n"), true
}
```

Then adjust the daemon reload ordering so systemd reloads before restarts:

```go
func (s *Server) reconcileTailscaleResolverIsolation(ctx context.Context) error {
	dv, err := s.getDB()
	if err != nil {
		return err
	}

	var errs []error
	var repaired []string
	for name, sv := range dv.Services().All() {
		if err := ctx.Err(); err != nil {
			return err
		}
		changed, err := reconcileTailscaleResolverIsolationForService(sv.AsStruct())
		if err != nil {
			log.Printf("tailscale resolver isolation reconciliation failed for service %q: %v", name, err)
			errs = append(errs, err)
			continue
		}
		if changed {
			repaired = append(repaired, name)
		}
	}
	if len(repaired) == 0 {
		return errors.Join(errs...)
	}
	if err := catchSystemctl("daemon-reload"); err != nil {
		errs = append(errs, fmt.Errorf("systemctl daemon-reload: %w", err))
		return errors.Join(errs...)
	}
	for _, name := range repaired {
		if err := restartTailscaleSidecarForService(name); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
```

- [ ] **Step 4: Call resolver isolation reconciliation from startup**

In `pkg/catch/catch.go`, update `reconcileRuntimeState`:

```go
func (s *Server) reconcileRuntimeState() {
	if err := s.reconcileTailscaleDNSConfigs(s.ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("tailscale DNS config reconciliation failed: %v", err)
	}
	if err := s.reconcileTailscaleResolverIsolation(s.ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("tailscale resolver isolation reconciliation failed: %v", err)
	}
	if err := s.reconcileNetNSBackedDockerServices(s.ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("netns reconciliation failed: %v", err)
	}
	if err := reconcileDockerNetNSPortForwards(s.cfg.DB); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("docker netns NAT reconciliation failed: %v", err)
	}
	if err := s.reconcileVMNetworks(s.ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("VM network reconciliation failed: %v", err)
	}
}
```

- [ ] **Step 5: Run reconciliation tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestReconcileTailscaleResolverIsolation|TestReconcileTailscaleDNSConfigs|TestReconcileNetNSBackedDockerServices' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit runtime repair**

Run:

```bash
but diff
```

Confirm only `pkg/catch/netns_reconcile.go`, `pkg/catch/netns_reconcile_test.go`, and `pkg/catch/catch.go` are included in this task's diff, then run:

```bash
but commit codex/host-resolver-isolation -m "catch: repair tailscale resolver isolation"
```

Expected: GitButler appends a fourth commit to `codex/host-resolver-isolation`.

## Task 5: Verification And Live Host Validation

**Files:**
- Verify: `pkg/catch/...`
- Verify: `pkg/netns/...`
- Verify: `pkg/svc/...`

- [ ] **Step 1: Run focused package tests**

Run:

```bash
mise exec -- go test ./pkg/catch ./pkg/netns ./pkg/svc -count=1
```

Expected: PASS.

- [ ] **Step 2: Run the full Go suite**

Run:

```bash
mise exec -- go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 3: Run pre-commit**

Run:

```bash
mise exec -- pre-commit run --all-files
```

Expected: PASS.

- [ ] **Step 4: Run the service-orchestration quality gate**

Run:

```bash
mise run quality:goal
```

Expected: PASS. This change touches service orchestration, networking, and runtime reconciliation, so the heavy goal gate is justified before calling the branch release-grade.

- [ ] **Step 5: Commit any verification-driven fixes**

If a verification command forced code changes, run:

```bash
but diff
```

Then run:

```bash
but commit codex/host-resolver-isolation -m "catch: finish resolver isolation fixes"
```

Expected: GitButler appends a final fix commit only if verification found real issues.

- [ ] **Step 6: Live validation on `root@lab-host` only after user authorization**

After the user explicitly approves updating the live catch server, run:

```bash
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet init root@lab-host
```

Expected: catch installs successfully on `root@lab-host`.

Then run:

```bash
ssh root@lab-host 'bash -lc '"'"'
set -e
echo RESOLV_BEFORE
cat /etc/resolv.conf
echo CHECK_UNSAFE_TS_UNITS
for unit in /etc/systemd/system/yeet-*-ts.service; do
  [ -f "$unit" ] || continue
  if grep -q "NetworkNamespacePath=/var/run/netns/" "$unit"; then
    grep -q "BindPaths=/etc/netns/.*resolv.conf:/etc/resolv.conf" "$unit" || echo "missing BindPaths: $unit"
    grep -q "PrivateMounts=yes" "$unit" || echo "missing PrivateMounts: $unit"
  fi
done
echo CATCH_DNS_SELF_QUERY
timeout 5 dig +time=2 +tries=1 @192.168.100.1 github.com A | sed -n "1,20p"
echo RESOLV_AFTER
cat /etc/resolv.conf
'"'"''
```

Expected:
- `RESOLV_BEFORE` and `RESOLV_AFTER` match exactly.
- `CHECK_UNSAFE_TS_UNITS` prints no `missing ...` lines.
- `CATCH_DNS_SELF_QUERY` returns either a public answer or a controlled SERVFAIL when no non-self upstream is configured; it must not hang in a recursive self-forward loop.

- [ ] **Step 7: Final branch status**

Run:

```bash
but status -fv
```

Expected: `codex/host-resolver-isolation` contains the resolver isolation commits, and there are no unrelated uncommitted changes assigned to this branch.

## Self-Review

Spec coverage:
- Host `/etc/resolv.conf` clobbered by service DHCP: Task 2 redirects dhclient resolver writes into `/etc/netns/<service-ns>/resolv.conf`.
- Host `/etc/resolv.conf` clobbered by service Tailscale sidecars: Task 3 binds namespace resolver files for new units, and Task 4 repairs existing installed units.
- `catch dns` self-forward loop through `192.168.100.1`: Task 1 filters the yeet DNS listener from host resolver upstreams.
- Existing `root@lab-host` services: Task 4 adds runtime reconciliation and Task 5 validates the live host after explicit authorization.

Placeholder scan:
- The plan contains concrete file paths, test code, implementation code, commands, and expected outcomes.
- Dynamic GitButler commit IDs are avoided by requiring each task to run in a focused worktree/branch and using `but commit` without raw `git` writes.

Type consistency:
- `runtimeNetNSResolvConf` is introduced in Task 3 and used consistently by service unit and Tailscale sidecar generation.
- `reconcileTailscaleResolverIsolation` is introduced in Task 4 and called from `reconcileRuntimeState` with the same signature.
- `YEET_NETNS_NAME` is passed by `service-ns` and consumed by the dhclient enter hook with the same environment variable name.
