# Svc-Scoped DNS Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make yeet DNS available only to workloads that opt into `svc`, while keeping Docker daemon DNS untouched and preserving normal LAN, Tailscale, and Docker resolver behavior outside `svc`.

**Architecture:** Keep `yeet-dns` on `192.168.100.1`, but deliver that resolver only through service-network configuration. Compose workloads get a generated overlay with per-service `dns` and `dns_search` only when `svc` is enabled, because Docker containers do not inherit `/etc/netns/<name>/resolv.conf`. Non-compose network namespaces write a resolver file only for `svc` or Tailscale TAP mode, and mixed `svc,lan` namespaces keep explicit routes back to the service subnet after LAN DHCP.

**Tech Stack:** Go, `gopkg.in/yaml.v3`, Docker Compose overlays, systemd unit generation, bash netns scripts, GitButler (`but`), website manual submodule.

---

## Execution Notes

- Start from a clean view based on current `origin/main`. The current workspace may have unrelated applied GitButler branches, including VM agent work and a website submodule change. Do not include those changes in this DNS branch.
- Read `AGENTS.md`, `AGENTS.local.md`, `cmd/catch/AGENTS.md`, `pkg/catch/AGENTS.md`, `pkg/netns/AGENTS.md`, and `website/AGENTS.md` before editing the listed areas.
- Use `but status -fv` before each commit. GitButler change IDs are runtime-generated; commit only the IDs for the files named in each task. The commit steps name the exact files to include; copy the current IDs for those files from `but status -fv`.
- Use `mise exec -- go ...` for all Go commands.
- Commit after each coherent task when the tests for that task pass. Do not push until the user asks.

## File Structure

- Modify `cmd/catch/catch.go`: remove install-time Docker daemon DNS writes and make catch install ensure only `features.containerd-snapshotter=true`.
- Modify `cmd/catch/catch_test.go`: replace daemon-DNS tests with tests proving existing Docker DNS config is preserved and no yeet DNS keys are written.
- Create `pkg/catch/compose_dns.go`: parse compose services and render a service-network DNS overlay for compose payloads.
- Create `pkg/catch/compose_dns_test.go`: unit-test compose service detection, custom resolver preservation, malformed compose errors, and overlay rendering.
- Modify `pkg/catch/installer_file.go`: scope netns resolver files to `svc` or Tailscale TAP mode, carry resolver presence through `networkConfig`, and call the compose DNS overlay renderer.
- Modify `pkg/catch/installer_file_test.go`: cover resolver scoping and staged compose network overlays for `svc`, `svc,lan`, and `lan`.
- Modify `pkg/netns/netns-scripts/service-ns`: add explicit routes to the service subnet and DNS host over the `svc` interface.
- Modify `pkg/netns/netns_test.go`: assert the service namespace script pins the service subnet and DNS host routes.
- Modify `website/docs/concepts/dns.mdx`: document that compose DNS is generated per `svc` workload, not configured at Docker daemon scope.
- Modify `website/docs/payloads/containers.mdx`: remove the host-requirement claim that `yeet init` configures Docker daemon DNS defaults.
- Review `README.md`, `website/docs/concepts/networking.mdx`, and `website/docs/operations/troubleshooting.mdx`: update only if they still imply daemon-wide Docker DNS.

### Task 1: Remove Docker Daemon DNS Defaults

**Files:**
- Modify: `cmd/catch/catch.go`
- Modify: `cmd/catch/catch_test.go`

- [ ] **Step 1: Write the failing test**

In `cmd/catch/catch_test.go`, delete `TestWriteDockerInstallConfigCreatesDNSDefaults`, `TestWriteDockerInstallConfigPreservesExistingSettings`, and `assertDockerConfigList`. Add this test next to `TestWriteContainerdSnapshotterConfigCreatesAndPreservesDockerConfig`:

```go
func TestWriteContainerdSnapshotterConfigDoesNotSetDockerDNS(t *testing.T) {
	root := t.TempDir()
	cfgPath := filepath.Join(root, "daemon.json")
	if err := os.WriteFile(cfgPath, []byte(`{"log-driver":"journald","features":{"buildkit":true},"dns":["8.8.8.8"],"dns-search":["lan"]}`), 0o600); err != nil {
		t.Fatalf("write existing config: %v", err)
	}

	changed, err := writeContainerdSnapshotterConfig(cfgPath)
	if err != nil {
		t.Fatalf("writeContainerdSnapshotterConfig: %v", err)
	}
	if !changed {
		t.Fatal("writeContainerdSnapshotterConfig changed=false, want true")
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read docker config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("docker config json: %v", err)
	}
	if cfg["log-driver"] != "journald" {
		t.Fatalf("log-driver = %#v, want journald", cfg["log-driver"])
	}
	if got := fmt.Sprint(cfg["dns"]); got != "[8.8.8.8]" {
		t.Fatalf("dns = %#v, want existing daemon DNS preserved", cfg["dns"])
	}
	if got := fmt.Sprint(cfg["dns-search"]); got != "[lan]" {
		t.Fatalf("dns-search = %#v, want existing daemon search preserved", cfg["dns-search"])
	}
	features := cfg["features"].(map[string]any)
	if features["buildkit"] != true || features["containerd-snapshotter"] != true {
		t.Fatalf("features = %#v, want buildkit and containerd-snapshotter", features)
	}
}

func TestWriteContainerdSnapshotterConfigCreatesOnlySnapshotterFeature(t *testing.T) {
	root := t.TempDir()
	cfgPath := filepath.Join(root, "docker", "daemon.json")

	changed, err := writeContainerdSnapshotterConfig(cfgPath)
	if err != nil {
		t.Fatalf("writeContainerdSnapshotterConfig: %v", err)
	}
	if !changed {
		t.Fatal("writeContainerdSnapshotterConfig changed=false, want true")
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read docker config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("docker config json: %v", err)
	}
	if _, ok := cfg["dns"]; ok {
		t.Fatalf("dns = %#v, want no daemon DNS default", cfg["dns"])
	}
	if _, ok := cfg["dns-search"]; ok {
		t.Fatalf("dns-search = %#v, want no daemon search default", cfg["dns-search"])
	}
	features := cfg["features"].(map[string]any)
	if features["containerd-snapshotter"] != true {
		t.Fatalf("features = %#v, want containerd-snapshotter=true", features)
	}
}
```

- [ ] **Step 2: Run the failing test**

Run:

```bash
mise exec -- go test ./cmd/catch -run 'TestWriteContainerdSnapshotterConfig(DoesNotSetDockerDNS|CreatesOnlySnapshotterFeature)' -count=1
```

Expected: the first test may pass, and the second test fails on `origin/main` while `writeDockerInstallConfig` still adds `dns` and `dns-search`.

- [ ] **Step 3: Remove daemon DNS writes**

In `cmd/catch/catch.go`, make `ensureContainerdSnapshotterForInstall` call only `writeContainerdSnapshotterConfig`:

```go
func ensureContainerdSnapshotterForInstall(dockerConfigPath string) error {
	changed, err := writeContainerdSnapshotterConfig(dockerConfigPath)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	return restartDocker()
}
```

Delete `writeDockerInstallConfig`, `setDockerConfigStringList`, `dockerConfigStringList`, `dockerInstallDNS`, and `dockerInstallDNSSearch` if present and unused.

- [ ] **Step 4: Run the targeted tests**

Run:

```bash
mise exec -- go test ./cmd/catch -count=1
```

Expected: all `cmd/catch` tests pass.

- [ ] **Step 5: Commit**

Run `but status -fv`, identify only the current change IDs for `cmd/catch/catch.go` and `cmd/catch/catch_test.go`, then run `but commit codex/svc-dns-design -m "catch: stop writing daemon DNS defaults"` with `--changes` set to exactly those comma-separated IDs.

### Task 2: Add Compose DNS Overlay Helpers

**Files:**
- Create: `pkg/catch/compose_dns.go`
- Create: `pkg/catch/compose_dns_test.go`

- [ ] **Step 1: Write the failing parser and renderer tests**

Create `pkg/catch/compose_dns_test.go` with these tests:

```go
package catch

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/netns"
	"gopkg.in/yaml.v3"
)

func TestComposeDNSServicesDetectsServicesAndCustomResolvers(t *testing.T) {
	raw := []byte(`
services:
  api:
    image: nginx
  db:
    image: postgres
    dns:
      - 1.1.1.1
  worker:
    image: busybox
    dns_search:
      - lan
`)
	services, err := composeDNSServices(raw)
	if err != nil {
		t.Fatalf("composeDNSServices: %v", err)
	}
	want := []composeDNSService{
		{Name: "api"},
		{Name: "db", CustomResolver: true},
		{Name: "worker", CustomResolver: true},
	}
	if len(services) != len(want) {
		t.Fatalf("services = %#v, want %#v", services, want)
	}
	for i := range want {
		if services[i] != want[i] {
			t.Fatalf("services[%d] = %#v, want %#v", i, services[i], want[i])
		}
	}
}

func TestComposeDNSServicesRejectsMalformedCompose(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "missing services", raw: `name: demo`, want: "missing services"},
		{name: "services list", raw: `services: []`, want: "compose services are not a map"},
		{name: "service scalar", raw: "services:\n  api: nginx\n", want: `compose service "api" is malformed`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := composeDNSServices([]byte(tt.raw))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("composeDNSServices error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestRenderDockerComposeNetworkAddsDNSOnlyForSvcServicesWithoutCustomResolvers(t *testing.T) {
	overlay, err := renderDockerComposeNetwork(netns.Service{
		ServiceName: "client",
		ServiceIP:   netipPrefixForTest(t, "192.168.100.3/32"),
	}, []composeDNSService{
		{Name: "api"},
		{Name: "db", CustomResolver: true},
	})
	if err != nil {
		t.Fatalf("renderDockerComposeNetwork: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(overlay), &doc); err != nil {
		t.Fatalf("unmarshal overlay: %v\n%s", err, overlay)
	}
	services := doc["services"].(map[string]any)
	api := services["api"].(map[string]any)
	if got := api["dns"].([]any)[0]; got != "192.168.100.1" {
		t.Fatalf("api dns = %#v, want 192.168.100.1", api["dns"])
	}
	if got := api["dns_search"].([]any)[0]; got != "yeet.internal" {
		t.Fatalf("api dns_search = %#v, want yeet.internal", api["dns_search"])
	}
	if _, ok := services["db"]; ok {
		t.Fatalf("custom resolver service was included in overlay: %#v", services["db"])
	}
	networks := doc["networks"].(map[string]any)
	def := networks["default"].(map[string]any)
	if def["driver"] != "yeet" {
		t.Fatalf("network driver = %#v, want yeet", def["driver"])
	}
}

func TestRenderDockerComposeNetworkOmitsDNSWithoutSvc(t *testing.T) {
	overlay, err := renderDockerComposeNetwork(netns.Service{ServiceName: "client"}, []composeDNSService{{Name: "api"}})
	if err != nil {
		t.Fatalf("renderDockerComposeNetwork: %v", err)
	}
	if strings.Contains(overlay, "dns:") || strings.Contains(overlay, "dns_search:") {
		t.Fatalf("overlay contains DNS without svc:\n%s", overlay)
	}
}
```

Add this helper at the bottom of `pkg/catch/compose_dns_test.go`:

```go
func netipPrefixForTest(t *testing.T, value string) netip.Prefix {
	t.Helper()
	prefix, err := netip.ParsePrefix(value)
	if err != nil {
		t.Fatalf("ParsePrefix(%q): %v", value, err)
	}
	return prefix
}
```

- [ ] **Step 2: Run the failing tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestComposeDNSServices|TestRenderDockerComposeNetwork' -count=1
```

Expected: build fails because `composeDNSServices`, `composeDNSService`, and `renderDockerComposeNetwork` do not exist.

- [ ] **Step 3: Add the helper implementation**

Create `pkg/catch/compose_dns.go`:

```go
package catch

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/yeetrun/yeet/pkg/netns"
	"gopkg.in/yaml.v3"
)

type composeDNSService struct {
	Name           string
	CustomResolver bool
}

type composeDNSOverlayService struct {
	DNS       []string `yaml:"dns,omitempty"`
	DNSSearch []string `yaml:"dns_search,omitempty"`
}

type composeNetworkOverlay struct {
	Services map[string]composeDNSOverlayService `yaml:"services,omitempty"`
	Networks map[string]composeOverlayNetwork    `yaml:"networks"`
}

type composeOverlayNetwork struct {
	Driver     string            `yaml:"driver"`
	DriverOpts map[string]string `yaml:"driver_opts"`
}

func composeDNSServices(raw []byte) ([]composeDNSService, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse compose yaml: %w", err)
	}
	root := yamlDocumentMapping(&doc)
	if root == nil {
		return nil, fmt.Errorf("compose file root is not a map")
	}
	services := yamlMappingValue(root, "services")
	if services == nil {
		return nil, fmt.Errorf("compose file missing services")
	}
	if services.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("compose services are not a map")
	}
	out := make([]composeDNSService, 0, len(services.Content)/2)
	for idx := 0; idx+1 < len(services.Content); idx += 2 {
		key := services.Content[idx]
		value := services.Content[idx+1]
		if key.Value == "" {
			return nil, fmt.Errorf("compose service name is empty")
		}
		if value.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("compose service %q is malformed", key.Value)
		}
		out = append(out, composeDNSService{
			Name:           key.Value,
			CustomResolver: yamlMappingValue(value, "dns") != nil || yamlMappingValue(value, "dns_search") != nil,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("compose file has no services")
	}
	return out, nil
}

func yamlDocumentMapping(doc *yaml.Node) *yaml.Node {
	if doc == nil {
		return nil
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) == 1 {
		doc = doc.Content[0]
	}
	if doc.Kind != yaml.MappingNode {
		return nil
	}
	return doc
}

func yamlMappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for idx := 0; idx+1 < len(node.Content); idx += 2 {
		if node.Content[idx].Value == key {
			return node.Content[idx+1]
		}
	}
	return nil
}

func renderDockerComposeNetwork(env netns.Service, services []composeDNSService) (string, error) {
	overlay := composeNetworkOverlay{
		Networks: map[string]composeOverlayNetwork{
			"default": {
				Driver: "yeet",
				DriverOpts: map[string]string{
					"dev.catchit.netns": filepath.Join("/var/run/netns", env.NetNS()),
				},
			},
		},
	}
	if env.ServiceIP.IsValid() {
		for _, service := range services {
			if service.CustomResolver {
				continue
			}
			if overlay.Services == nil {
				overlay.Services = map[string]composeDNSOverlayService{}
			}
			overlay.Services[service.Name] = composeDNSOverlayService{
				DNS:       []string{yeetDNSHostIP},
				DNSSearch: []string{strings.TrimSuffix(yeetDNSDomain, ".")},
			}
		}
	}
	raw, err := yaml.Marshal(overlay)
	if err != nil {
		return "", fmt.Errorf("marshal compose network overlay: %w", err)
	}
	return string(raw), nil
}
```

- [ ] **Step 4: Run gofmt and targeted tests**

Run:

```bash
mise exec -- gofmt -w pkg/catch/compose_dns.go pkg/catch/compose_dns_test.go
mise exec -- go test ./pkg/catch -run 'TestComposeDNSServices|TestRenderDockerComposeNetwork' -count=1
```

Expected: tests pass.

- [ ] **Step 5: Commit**

Run `but status -fv`, identify only the current change IDs for `pkg/catch/compose_dns.go` and `pkg/catch/compose_dns_test.go`, then run `but commit codex/svc-dns-design -m "catch: add compose DNS overlay helpers"` with `--changes` set to exactly those comma-separated IDs.

### Task 3: Wire Compose and Netns Resolver Scoping

**Files:**
- Modify: `pkg/catch/installer_file.go`
- Modify: `pkg/catch/installer_file_test.go`

- [ ] **Step 1: Write failing resolver scoping tests**

In `pkg/catch/installer_file_test.go`, replace `TestDefaultNetNSResolvConfUsesYeetDNS` and `TestDefaultNetNSResolvConfExplicitNameserverOptsOut` with:

```go
func TestSvcNetNSResolvConfUsesYeetDNS(t *testing.T) {
	t.Setenv("DEFAULT_NS", "")
	t.Setenv("DEFAULT_SEARCH_DOMAINS", "")

	got := defaultSvcNetNSResolvConf()
	want := "nameserver 192.168.100.1\nsearch yeet.internal\n"
	if got != want {
		t.Fatalf("resolv.conf = %q, want %q", got, want)
	}
}

func TestSvcNetNSResolvConfExplicitNameserverOptsOut(t *testing.T) {
	t.Setenv("DEFAULT_NS", "1.1.1.1")
	t.Setenv("DEFAULT_SEARCH_DOMAINS", "")

	got := defaultSvcNetNSResolvConf()
	want := "nameserver 1.1.1.1\n"
	if got != want {
		t.Fatalf("resolv.conf = %q, want %q", got, want)
	}
}

func TestNetNSResolvConfForScopesDNSByNetworkMode(t *testing.T) {
	svcEnv := netns.Service{ServiceName: "svc"}
	applySvcNetwork(&svcEnv, &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.3")})
	if got := netNSResolvConfFor(&svcEnv, ""); got != "nameserver 192.168.100.1\nsearch yeet.internal\n" {
		t.Fatalf("svc resolv.conf = %q", got)
	}

	lanEnv := netns.Service{ServiceName: "lan", MacvlanParent: "vmbr0"}
	if got := netNSResolvConfFor(&lanEnv, ""); got != "" {
		t.Fatalf("lan resolv.conf = %q, want empty", got)
	}

	tsEnv := netns.Service{ServiceName: "ts", TailscaleTAPInterface: "yts0"}
	if got := netNSResolvConfFor(&tsEnv, tailscaledResolvConf); got != tailscaledResolvConf {
		t.Fatalf("tailscale tap resolv.conf = %q, want %q", got, tailscaledResolvConf)
	}
}
```

- [ ] **Step 2: Write failing installer integration tests**

Add these tests near the existing network installer tests in `pkg/catch/installer_file_test.go`:

```go
func TestInstallerCloseStagesComposeSvcDNSOverlay(t *testing.T) {
	server := newTestServer(t)
	installer, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{ServiceName: "compose-dns"},
		Network:      NetworkOpts{Interfaces: "svc"},
		StageOnly:    true,
		PayloadName:  "compose.yml",
	})
	if err != nil {
		t.Fatalf("NewFileInstaller returned error: %v", err)
	}
	payload := "services:\n  app:\n    image: busybox\n  custom:\n    image: busybox\n    dns:\n      - 1.1.1.1\n"
	if _, err := installer.Write([]byte(payload)); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if err := installer.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	service := testService(t, server, "compose-dns")
	networkPath := stagedArtifactPath(t, service, db.ArtifactDockerComposeNetwork)
	raw, err := os.ReadFile(networkPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", networkPath, err)
	}
	network := string(raw)
	for _, want := range []string{
		"services:",
		"app:",
		"192.168.100.1",
		"yeet.internal",
		"driver: yeet",
		`dev.catchit.netns: /var/run/netns/yeet-compose-dns-ns`,
	} {
		if !strings.Contains(network, want) {
			t.Fatalf("compose network missing %q:\n%s", want, network)
		}
	}
	if strings.Contains(network, "custom:") {
		t.Fatalf("custom DNS service should not receive generated resolver stanza:\n%s", network)
	}
}

func TestInstallerCloseStagesComposeLANOverlayWithoutYeetDNS(t *testing.T) {
	oldHostDefaultRouteInterfaceFn := hostDefaultRouteInterfaceFn
	hostDefaultRouteInterfaceFn = func() (string, error) { return "vmbr0", nil }
	t.Cleanup(func() { hostDefaultRouteInterfaceFn = oldHostDefaultRouteInterfaceFn })

	server := newTestServer(t)
	installer, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{ServiceName: "compose-lan"},
		Network:      NetworkOpts{Interfaces: "lan"},
		StageOnly:    true,
		PayloadName:  "compose.yml",
	})
	if err != nil {
		t.Fatalf("NewFileInstaller returned error: %v", err)
	}
	if _, err := installer.Write([]byte("services:\n  app:\n    image: busybox\n")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if err := installer.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	service := testService(t, server, "compose-lan")
	networkPath := stagedArtifactPath(t, service, db.ArtifactDockerComposeNetwork)
	raw, err := os.ReadFile(networkPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", networkPath, err)
	}
	if strings.Contains(string(raw), "192.168.100.1") || strings.Contains(string(raw), "yeet.internal") {
		t.Fatalf("lan-only compose network should not include yeet DNS:\n%s", raw)
	}
	if _, ok := service.Artifacts[db.ArtifactNetNSResolv]; ok {
		t.Fatalf("lan-only service staged netns resolv artifact: %#v", service.Artifacts[db.ArtifactNetNSResolv])
	}
}
```

- [ ] **Step 3: Run the failing installer tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestSvcNetNSResolvConf|TestNetNSResolvConfFor|TestInstallerCloseStagesCompose.*Overlay' -count=1
```

Expected: build fails until resolver function names and compose integration exist.

- [ ] **Step 4: Implement resolver scoping**

In `pkg/catch/installer_file.go`, update `networkConfig`:

```go
type networkConfig struct {
	NetNS        string
	Deps         []string
	HasResolvConf bool
}
```

In `configureNetworkOnce`, return resolver presence:

```go
	return &networkConfig{
		NetNS:         env.NetNS(),
		Deps:          deps,
		HasResolvConf: env.ResolvConf != "",
	}, nil
```

Replace `writeBaseNetworkConfig`, `writeNetNSResolvConf`, and `defaultNetNSResolvConf` with:

```go
func (i *FileInstaller) writeBaseNetworkConfig(env *netns.Service) error {
	_, tailscaleResolvConf, _ := i.tailscaleNetNSMode(env)
	if resolvConf := netNSResolvConfFor(env, tailscaleResolvConf); resolvConf != "" {
		if err := i.writeNetNSResolvConf(env, resolvConf); err != nil {
			return err
		}
	}
	return i.writeServiceNetNSFiles(*env)
}

func netNSResolvConfFor(env *netns.Service, tailscaleResolvConf string) string {
	if tailscaleResolvConf != "" {
		return tailscaleResolvConf
	}
	if env != nil && env.ServiceIP.IsValid() {
		return defaultSvcNetNSResolvConf()
	}
	return ""
}

func (i *FileInstaller) writeNetNSResolvConf(env *netns.Service, resolvConf string) error {
	fp := filepath.Join(i.serviceBinDir(), fileutil.ApplyVersion("resolv.conf"))
	if err := os.WriteFile(fp, []byte(resolvConf), 0644); err != nil {
		return fmt.Errorf("failed to write resolv.conf: %v", err)
	}
	mak.Set(&i.artifacts, db.ArtifactNetNSResolv, fp)
	env.ResolvConf = fp
	return nil
}

func defaultSvcNetNSResolvConf() string {
	if dns := os.Getenv("DEFAULT_NS"); dns != "" {
		return buildNetNSResolvConf(dns, os.Getenv("DEFAULT_SEARCH_DOMAINS"))
	}
	searchDomains := os.Getenv("DEFAULT_SEARCH_DOMAINS")
	if searchDomains == "" {
		searchDomains = strings.TrimSuffix(yeetDNSDomain, ".")
	}
	return buildNetNSResolvConf(yeetDNSHostIP, searchDomains)
}
```

In `applyNetworkToSystemdUnit`, bind `/etc/resolv.conf` only when a resolver file was staged:

```go
	su.NetNS = n.NetNS
	su.Requires = strings.Join(n.Deps, " ")
	if n.HasResolvConf {
		su.ResolvConf = fmt.Sprintf("/etc/netns/%s/resolv.conf", su.NetNS)
	}
```

- [ ] **Step 5: Implement compose DNS integration**

In `pkg/catch/installer_file.go`, replace `writeDockerComposeNetwork` with:

```go
func (i *FileInstaller) writeDockerComposeNetwork(env netns.Service) error {
	services, err := i.composeDNSOverlayServices(env)
	if err != nil {
		return err
	}
	dockerNet, err := renderDockerComposeNetwork(env, services)
	if err != nil {
		return err
	}
	dnf := filepath.Join(i.serviceBinDir(), "compose.network")
	if err := os.WriteFile(dnf, []byte(dockerNet), 0644); err != nil {
		return fmt.Errorf("failed to write docker compose network: %v", err)
	}
	mak.Set(&i.artifacts, db.ArtifactDockerComposeNetwork, dnf)
	return nil
}

func (i *FileInstaller) composeDNSOverlayServices(env netns.Service) ([]composeDNSService, error) {
	composePath, ok := i.artifacts[db.ArtifactDockerComposeFile]
	if !ok || !env.ServiceIP.IsValid() {
		return nil, nil
	}
	raw, err := os.ReadFile(composePath)
	if err != nil {
		return nil, fmt.Errorf("read compose file for DNS overlay: %w", err)
	}
	services, err := composeDNSServices(raw)
	if err != nil {
		return nil, fmt.Errorf("compose DNS overlay: %w", err)
	}
	for _, service := range services {
		if service.CustomResolver {
			i.printf("warning: compose service %q defines dns or dns_search; leaving resolver configuration unchanged\n", service.Name)
		}
	}
	return services, nil
}
```

- [ ] **Step 6: Run gofmt and targeted tests**

Run:

```bash
mise exec -- gofmt -w pkg/catch/installer_file.go pkg/catch/installer_file_test.go
mise exec -- go test ./pkg/catch -run 'TestBuildNetNSResolvConf|TestSvcNetNSResolvConf|TestNetNSResolvConfFor|TestInstallerCloseStagesCompose.*Overlay|TestInstallerCloseStagesGeneratedPythonComposeWithNetworkArtifacts|TestInstallerNetworkPlanningCoversTailscaleTapAndMacvlanModes' -count=1
```

Expected: tests pass.

- [ ] **Step 7: Commit**

Run `but status -fv`, identify only the current change IDs for `pkg/catch/installer_file.go` and `pkg/catch/installer_file_test.go`, then run `but commit codex/svc-dns-design -m "catch: scope resolver config to svc network"` with `--changes` set to exactly those comma-separated IDs.

### Task 4: Preserve Service Subnet Routes In Mixed Networks

**Files:**
- Modify: `pkg/netns/netns-scripts/service-ns`
- Modify: `pkg/netns/netns_test.go`

- [ ] **Step 1: Write the failing script assertion**

In `pkg/netns/netns_test.go`, add:

```go
func TestServiceNSScriptPinsServiceNetworkRoutes(t *testing.T) {
	raw, err := netnsScripts.ReadFile("netns-scripts/service-ns")
	if err != nil {
		t.Fatalf("ReadFile embedded service-ns returned error: %v", err)
	}
	got := string(raw)
	for _, want := range []string{
		`ip netns exec $NS_NAME ip route replace "$RANGE" via "$YEET_IP" dev "$IF_IN_NS_NAME"`,
		`ip netns exec $NS_NAME ip route replace "$HOST_IP/32" via "$YEET_IP" dev "$IF_IN_NS_NAME"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("service-ns missing %q:\n%s", want, got)
		}
	}
}
```

- [ ] **Step 2: Run the failing test**

Run:

```bash
mise exec -- go test ./pkg/netns -run TestServiceNSScriptPinsServiceNetworkRoutes -count=1
```

Expected: test fails because the route lines are absent.

- [ ] **Step 3: Add explicit service routes**

In `pkg/netns/netns-scripts/service-ns`, after the two default-route lines inside the `if [ -n "$SERVICE_IP" ]; then` block, add:

```bash
    if [ -n "$RANGE" ] && [ -n "$YEET_IP" ]; then
        ip netns exec $NS_NAME ip route replace "$RANGE" via "$YEET_IP" dev "$IF_IN_NS_NAME"
    fi
    if [ -n "$HOST_IP" ] && [ -n "$YEET_IP" ]; then
        ip netns exec $NS_NAME ip route replace "$HOST_IP/32" via "$YEET_IP" dev "$IF_IN_NS_NAME"
    fi
```

- [ ] **Step 4: Run targeted netns tests**

Run:

```bash
mise exec -- go test ./pkg/netns -count=1
```

Expected: tests pass.

- [ ] **Step 5: Commit**

Run `but status -fv`, identify only the current change IDs for `pkg/netns/netns-scripts/service-ns` and `pkg/netns/netns_test.go`, then run `but commit codex/svc-dns-design -m "netns: preserve service routes with lan"` with `--changes` set to exactly those comma-separated IDs.

### Task 5: Update User Documentation

**Files:**
- Modify: `website/docs/concepts/dns.mdx`
- Modify: `website/docs/payloads/containers.mdx`
- Review: `README.md`
- Review: `website/docs/concepts/networking.mdx`
- Review: `website/docs/operations/troubleshooting.mdx`

- [ ] **Step 1: Update the DNS concept page prose**

In `website/docs/concepts/dns.mdx`, replace the Docker Compose paragraph under "Containers and service netns" with:

```md
Docker Compose containers normally still see Docker's embedded resolver at
`127.0.0.11`, so catch generates a compose overlay for workloads that include
`svc`. The overlay adds `dns: [192.168.100.1]` and
`dns_search: [yeet.internal]` for compose services that do not already define
their own resolver settings. If a compose service explicitly sets `dns` or
`dns_search`, that service owns resolver configuration and yeet leaves it
unchanged.

Workloads that use only `lan`, only `ts`, or Docker's default networking do
not receive yeet DNS by default. Add `svc` when the workload should discover
other yeet services; use `svc,lan` when it also needs a LAN address.
```

- [ ] **Step 2: Update container host requirements**

In `website/docs/payloads/containers.mdx`, replace the host-requirements paragraph with:

```md
Docker hosts need Docker, Compose support, and the containerd snapshotter so
pushed images show up locally. `yeet init` can install Docker on fresh
Debian/Ubuntu hosts and configures the snapshotter during catch install. Yeet
service discovery is configured per workload when `svc` networking is used;
Docker daemon DNS defaults stay under the host operator's control. See
[DNS](/docs/concepts/dns) for resolver behavior,
[Installation](/docs/getting-started/installation) for host setup, and
[Workflows](/docs/operations/workflows) for common update flows. ZFS is
optional; see [ZFS](/docs/concepts/zfs) if you want dataset-backed service
roots.
```

- [ ] **Step 3: Search for stale daemon-DNS wording**

Run:

```bash
rg -n "daemon defaults|daemon DNS|dns_search|compose files do not need|192\\.168\\.100\\.1" README.md website/docs
```

Expected: remaining `192.168.100.1` mentions are service-network resolver facts, not Docker daemon defaults. There are no remaining claims that `yeet init` configures Docker daemon DNS.

- [ ] **Step 4: Run docs checks**

Run:

```bash
git -C website diff --check
```

Expected: no whitespace errors.

- [ ] **Step 5: Commit website docs**

Inside `website/`, commit and push the website documentation update according to `website/AGENTS.md`. If GitButler commands from `website/` resolve to the parent workspace, use raw `git -C website status`, `git -C website commit`, and `git -C website push` only for the website repository.

Then return to the root repo and commit the `website` gitlink on the DNS branch with GitButler:

Run `but status -fv`, then run `but commit codex/svc-dns-design -m "docs: scope DNS guidance to svc"` with `--changes` set to only the current GitButler change ID for the root `website` gitlink.

### Task 6: Full Local Verification

**Files:**
- No planned code edits.

- [ ] **Step 1: Run targeted packages**

Run:

```bash
mise exec -- go test ./cmd/catch ./pkg/catch ./pkg/netns ./pkg/svc -count=1
```

Expected: all listed packages pass.

- [ ] **Step 2: Run the full Go suite**

Run:

```bash
mise exec -- go test ./... -count=1
```

Expected: all packages pass.

- [ ] **Step 3: Run the repository quality gate**

Run:

```bash
pre-commit run --all-files
```

Expected: pre-commit completes cleanly. If it rewrites files, inspect, rerun the affected tests, and amend those changes into the relevant task commit.

### Task 7: Live Smoke Test On lab-host And cloud-host

**Files:**
- No planned code edits.

- [ ] **Step 1: Install the updated catch build**

Run:

```bash
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet init root@lab-host
CATCH_HOST=yeet-cloud mise exec -- go run ./cmd/yeet init root@cloud-host
```

Expected: both commands finish without configuring Docker daemon `dns` or `dns-search`.

- [ ] **Step 2: Verify Docker daemon config stays host-owned**

Run:

```bash
ssh root@lab-host 'python3 - <<'"'"'PY'"'"'
import json
cfg=json.load(open("/etc/docker/daemon.json"))
assert cfg.get("features", {}).get("containerd-snapshotter") is True, cfg
assert "dns" not in cfg, cfg
assert "dns-search" not in cfg, cfg
print("lab-host docker config ok")
PY'
ssh root@cloud-host 'python3 - <<'"'"'PY'"'"'
import json
cfg=json.load(open("/etc/docker/daemon.json"))
assert cfg.get("features", {}).get("containerd-snapshotter") is True, cfg
assert "dns" not in cfg, cfg
assert "dns-search" not in cfg, cfg
print("cloud-host docker config ok")
PY'
```

Expected: both commands print `docker config ok`.

- [ ] **Step 3: Deploy disposable svc compose workloads**

Run:

```bash
tmpdir="$(mktemp -d)"
cat > "$tmpdir/codex-dns-target.yml" <<'YAML'
services:
  target:
    image: busybox:latest
    command: ["sh", "-c", "sleep 1d"]
YAML
cat > "$tmpdir/codex-dns-client.yml" <<'YAML'
services:
  client:
    image: busybox:latest
    command: ["sh", "-c", "sleep 1d"]
YAML
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet run codex-dns-target "$tmpdir/codex-dns-target.yml" --net=svc
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet run codex-dns-client "$tmpdir/codex-dns-client.yml" --net=svc
```

Expected: both services install and start.

- [ ] **Step 4: Verify svc service discovery and public forwarding**

Run:

```bash
ssh root@lab-host 'cid=$(docker ps -q --filter label=com.docker.compose.project=catch-codex-dns-client --filter label=com.docker.compose.service=client); docker exec "$cid" getent hosts codex-dns-target; docker exec "$cid" getent hosts codex-dns-target.yeet.internal; docker exec "$cid" getent hosts example.com'
```

Expected: the first two lookups return a `192.168.100.x` address, and `example.com` returns at least one public address.

- [ ] **Step 5: Verify lan-only compose does not depend on yeet DNS**

Run:

```bash
cat > "$tmpdir/codex-dns-lan.yml" <<'YAML'
services:
  client:
    image: busybox:latest
    command: ["sh", "-c", "sleep 1d"]
YAML
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet run codex-dns-lan "$tmpdir/codex-dns-lan.yml" --net=lan
ssh root@lab-host 'cid=$(docker ps -q --filter label=com.docker.compose.project=catch-codex-dns-lan --filter label=com.docker.compose.service=client); docker exec "$cid" cat /etc/resolv.conf; docker exec "$cid" getent hosts example.com'
```

Expected: `/etc/resolv.conf` does not contain `192.168.100.1`, and public DNS still resolves.

- [ ] **Step 6: Verify existing production services still resolve public DNS**

Run:

```bash
ssh root@lab-host 'for project in catch-prowlarr catch-radarr catch-sonarr catch-sabnzbd; do cid=$(docker ps -q --filter label=com.docker.compose.project=$project | head -n1); printf "%s " "$project"; docker exec "$cid" getent hosts google.com >/dev/null && echo ok; done'
```

Expected: all four print `ok`.

- [ ] **Step 7: Clean disposable services**

Run:

```bash
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet remove codex-dns-target --yes --clean-data --clean-config
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet remove codex-dns-client --yes --clean-data --clean-config
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet remove codex-dns-lan --yes --clean-data --clean-config
rm -rf "$tmpdir"
```

Expected: disposable services are gone from `yeet status`, and no test containers remain.

### Task 8: Final Review And Integration Prep

**Files:**
- Review all changed files.

- [ ] **Step 1: Inspect final diff**

Run:

```bash
but status -fv
but diff
```

Expected: only the DNS implementation, docs update, and website gitlink are present on the DNS branch.

- [ ] **Step 2: Check commit shape**

Run:

```bash
but show codex/svc-dns-design
```

Expected: commits are coherent, with no unrelated VM-agent, website-only, or local workspace changes included.

- [ ] **Step 3: Prepare finish summary**

Summarize:

- Docker daemon DNS no longer written by `catch install`.
- Compose workloads with `svc` receive generated per-service resolver config.
- LAN-only and non-`svc` workloads keep normal resolver behavior.
- Mixed `svc,lan` namespaces keep service subnet routes.
- Docs now tell users to opt into service discovery with `svc`.
- List local tests and live smoke test outcomes.
