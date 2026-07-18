// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/yeetrun/yeet/pkg/db"
)

func TestAdmitISOCompose(t *testing.T) {
	safe := `{"services":{"api":{"image":"nginx:alpine","command":["nginx","-g","daemon off;"],"environment":{"A":"B"}}}}`
	model, err := admitImplicitISOTest(t, safe, ISOComposeAdmissionOptions{ServiceRoot: t.TempDir(), MaxComponents: 29})
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
		{name: "external volume", raw: `{"services":{"api":{"image":"x","volumes":[{"type":"volume","source":"shared","target":"/data"}] }},"volumes":{"shared":{"external":true}}}`, path: "volumes.shared.external"},
		{name: "outside secret", raw: `{"services":{"api":{"image":"x","secrets":[{"source":"token","target":"token"}]}},"secrets":{"token":{"file":"/etc/shadow"}}}`, path: "secrets.token.file"},
		{name: "unknown field", raw: `{"services":{"api":{"image":"x","future_escape":true}}}`, path: "services.api.future_escape"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := admitImplicitISOTest(t, tt.raw, ISOComposeAdmissionOptions{ServiceRoot: t.TempDir(), MaxComponents: 29})
			assertISOAdmissionPath(t, err, tt.path)
		})
	}
}

func TestAdmitISOComposeRejectsForbiddenServiceFields(t *testing.T) {
	fields := []string{
		"build", "cap_add", "cgroup", "cgroup_parent", "cpu_rt_period", "cpu_rt_runtime",
		"devices", "device_cgroup_rules", "dns", "dns_opt", "dns_search", "domainname",
		"external_links", "ipc", "links", "network_mode", "pid", "ports", "privileged",
		"provider", "runtime", "security_opt", "storage_opt", "uts", "volumes_from",
		"annotations", "develop", "extends",
	}
	for _, field := range fields {
		t.Run(field, func(t *testing.T) {
			raw := fmt.Sprintf(`{"services":{"api":{"image":"x",%q:true}}}`, field)
			_, err := admitImplicitISOTest(t, raw, ISOComposeAdmissionOptions{ServiceRoot: t.TempDir()})
			assertISOAdmissionPath(t, err, "services.api."+field)
		})
	}
}

func TestAdmitISOComposeAllowsOnlyCanonicalImplicitDefault(t *testing.T) {
	raw := `{"name":"catch-app","networks":{"default":{"name":"catch-app_default","ipam":{}}},"services":{"api":{"image":"nginx","networks":{"default":null}}}}`
	if _, err := AdmitISOCompose([]byte(raw), ISOComposeAdmissionOptions{ProjectName: "catch-app", ServiceRoot: t.TempDir(), MaxComponents: 29}); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name string
		raw  string
		path string
	}{
		{name: "external network", raw: `{"name":"catch-app","networks":{"outside":{"name":"shared","external":true}},"services":{"api":{"image":"nginx","networks":{"outside":null}}}}`, path: "networks.outside"},
		{name: "custom default name", raw: `{"name":"catch-app","networks":{"default":{"name":"shared","ipam":{}}},"services":{"api":{"image":"nginx","networks":{"default":null}}}}`, path: "networks.default.name"},
		{name: "custom driver", raw: `{"name":"catch-app","networks":{"default":{"name":"catch-app_default","driver":"bridge","ipam":{}}},"services":{"api":{"image":"nginx","networks":{"default":null}}}}`, path: "networks.default.driver"},
		{name: "alternate attachment", raw: `{"name":"catch-app","networks":{"default":{"name":"catch-app_default","ipam":{}}},"services":{"api":{"image":"nginx","networks":{"outside":null}}}}`, path: "services.api.networks.outside"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := AdmitISOCompose([]byte(tt.raw), ISOComposeAdmissionOptions{ProjectName: "catch-app", ServiceRoot: t.TempDir()})
			assertISOAdmissionPath(t, err, tt.path)
		})
	}
}

func TestAdmitISOComposeRequiresExactCanonicalImplicitDefault(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
		path string
	}{
		{
			name: "missing top-level default",
			raw:  `{"name":"catch-app","services":{"api":{"image":"nginx","networks":{"default":null}}}}`,
			path: "networks.default",
		},
		{
			name: "missing service attachment",
			raw:  `{"name":"catch-app","networks":{"default":{"name":"catch-app_default","ipam":{}}},"services":{"api":{"image":"nginx"}}}`,
			path: "services.api.networks",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := AdmitISOCompose([]byte(tt.raw), ISOComposeAdmissionOptions{ProjectName: "catch-app", ServiceRoot: t.TempDir()})
			assertISOAdmissionPath(t, err, tt.path)
		})
	}
}

func TestAdmitISOComposeRequiresExactOverlay(t *testing.T) {
	allocation := &db.ISOAllocation{
		Project: netip.MustParsePrefix("172.30.128.0/27"),
		Gateway: netip.MustParseAddr("172.30.128.1"),
		NetNS:   "yeet-a172cedcae-ns",
		Components: map[string]db.ISOComponent{
			"api": {Address: netip.MustParseAddr("172.30.128.2")},
		},
	}
	raw := `{
		"name":"catch-app",
		"networks":{"default":{"name":"catch-app_default","driver":"yeet","driver_opts":{"dev.catchit.mode":"iso","dev.catchit.netns":"/var/run/netns/yeet-a172cedcae-ns"},"enable_ipv6":false,"ipam":{"config":[{"subnet":"172.30.128.0/27","gateway":"172.30.128.1"}]}}},
		"services":{"api":{"image":"nginx","dns":["172.30.128.1"],"networks":{"default":{"ipv4_address":"172.30.128.2"}}}}
	}`
	opts := ISOComposeAdmissionOptions{ProjectName: "catch-app", ServiceRoot: t.TempDir(), RequireISOOverlay: allocation}
	if _, err := AdmitISOCompose([]byte(raw), opts); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name string
		from string
		to   string
		path string
	}{
		{name: "wrong driver", from: `"driver":"yeet"`, to: `"driver":"bridge"`, path: "networks.default.driver"},
		{name: "wrong mode", from: `"dev.catchit.mode":"iso"`, to: `"dev.catchit.mode":"nat"`, path: "networks.default.driver_opts.dev.catchit.mode"},
		{name: "wrong namespace", from: `"dev.catchit.netns":"/var/run/netns/yeet-a172cedcae-ns"`, to: `"dev.catchit.netns":"/var/run/netns/other"`, path: "networks.default.driver_opts.dev.catchit.netns"},
		{name: "wrong subnet", from: `"subnet":"172.30.128.0/27"`, to: `"subnet":"172.30.129.0/27"`, path: "networks.default.ipam.config[0].subnet"},
		{name: "wrong gateway", from: `"gateway":"172.30.128.1"`, to: `"gateway":"172.30.128.3"`, path: "networks.default.ipam.config[0].gateway"},
		{name: "IPv6 enabled", from: `"enable_ipv6":false`, to: `"enable_ipv6":true`, path: "networks.default.enable_ipv6"},
		{name: "wrong component IP", from: `"ipv4_address":"172.30.128.2"`, to: `"ipv4_address":"172.30.128.3"`, path: "services.api.networks.default.ipv4_address"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := AdmitISOCompose([]byte(strings.Replace(raw, tt.from, tt.to, 1)), opts)
			assertISOAdmissionPath(t, err, tt.path)
		})
	}

	missingAttachment := strings.Replace(raw, `,"networks":{"default":{"ipv4_address":"172.30.128.2"}}`, "", 1)
	_, err := AdmitISOCompose([]byte(missingAttachment), opts)
	assertISOAdmissionPath(t, err, "services.api.networks")
}

func TestAdmitISOComposeAcceptsGeneratedDNSAfterOverlay(t *testing.T) {
	for _, tt := range []struct {
		name     string
		modes    []string
		resolver string
	}{
		{name: "gateway", modes: []string{"iso"}, resolver: "172.30.128.1"},
		{name: "Quad100", modes: []string{"iso", "ts"}, resolver: "100.100.100.100"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			allocation := testISOOverlayDNSAllocation(tt.modes)
			_, err := AdmitISOCompose(testISOOverlayCanonicalWithDNS(t, fmt.Sprintf(`[%q]`, tt.resolver)), ISOComposeAdmissionOptions{
				ProjectName:       "catch-app",
				ServiceRoot:       t.TempDir(),
				RequireISOOverlay: allocation,
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAdmitISOComposeRejectsNonGeneratedDNSAfterOverlay(t *testing.T) {
	for _, tt := range []struct {
		name    string
		dnsJSON string
		want    string
	}{
		{name: "wrong resolver", dnsJSON: `["8.8.8.8"]`, want: "does not match generated ISO resolver"},
		{name: "multiple resolvers", dnsJSON: `["172.30.128.1","8.8.8.8"]`, want: "requires exactly one generated DNS resolver"},
		{name: "empty resolvers", dnsJSON: `[]`, want: "requires exactly one generated DNS resolver"},
		{name: "null", dnsJSON: `null`, want: "invalid canonical DNS representation"},
		{name: "scalar", dnsJSON: `"172.30.128.1"`, want: "invalid canonical DNS representation"},
		{name: "malformed address", dnsJSON: `["not-an-address"]`, want: "invalid canonical DNS address"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := AdmitISOCompose(testISOOverlayCanonicalWithDNS(t, tt.dnsJSON), ISOComposeAdmissionOptions{
				ProjectName:       "catch-app",
				ServiceRoot:       t.TempDir(),
				RequireISOOverlay: testISOOverlayDNSAllocation([]string{"iso"}),
			})
			assertISOAdmissionPath(t, err, "services.api.dns")
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestAdmitISOComposeRejectsAbsentDNSAfterOverlay(t *testing.T) {
	raw := strings.Replace(
		string(testISOOverlayCanonicalWithDNS(t, `["172.30.128.1"]`)),
		`"dns":["172.30.128.1"],`,
		"",
		1,
	)
	_, err := AdmitISOCompose([]byte(raw), ISOComposeAdmissionOptions{
		ProjectName:       "catch-app",
		ServiceRoot:       t.TempDir(),
		RequireISOOverlay: testISOOverlayDNSAllocation([]string{"iso"}),
	})
	assertISOAdmissionPath(t, err, "services.api.dns")
	if !strings.Contains(err.Error(), "requires exactly one generated DNS resolver") {
		t.Fatalf("error = %v, want absent generated DNS rejection", err)
	}
}

func TestAdmitISOComposeRejectsCustomDNSBeforeOverlay(t *testing.T) {
	_, err := admitImplicitISOTest(t, `{"services":{"api":{"image":"nginx","dns":["172.30.128.1"]}}}`, ISOComposeAdmissionOptions{
		ProjectName: "catch-app",
		ServiceRoot: t.TempDir(),
	})
	assertISOAdmissionPath(t, err, "services.api.dns")
	if !strings.Contains(err.Error(), "custom DNS bypasses the ISO resolver") {
		t.Fatalf("error = %v, want pre-overlay custom DNS rejection", err)
	}
}

func testISOOverlayDNSAllocation(modes []string) *db.ISOAllocation {
	return &db.ISOAllocation{
		Project:      netip.MustParsePrefix("172.30.128.0/27"),
		Gateway:      netip.MustParseAddr("172.30.128.1"),
		NetNS:        "yeet-a172cedcae-ns",
		DesiredModes: append([]string(nil), modes...),
		Components: map[string]db.ISOComponent{
			"api": {Address: netip.MustParseAddr("172.30.128.2")},
		},
	}
}

func testISOOverlayCanonicalWithDNS(t *testing.T, dnsJSON string) []byte {
	t.Helper()
	return []byte(fmt.Sprintf(`{
		"name":"catch-app",
		"networks":{"default":{"name":"catch-app_default","driver":"yeet","driver_opts":{"dev.catchit.mode":"iso","dev.catchit.netns":"/var/run/netns/yeet-a172cedcae-ns"},"enable_ipv6":false,"ipam":{"config":[{"subnet":"172.30.128.0/27","gateway":"172.30.128.1"}]}}},
		"services":{"api":{"image":"nginx","dns":%s,"networks":{"default":{"ipv4_address":"172.30.128.2"}}}}
	}`, dnsJSON))
}

func TestAdmitISOComposeRejectsMalformedPersistedNamespaces(t *testing.T) {
	for _, netNS := range []string{
		".",
		"..",
		"yeet-app-ns",
		"yeet-A172CEDCAE-ns",
		"yeet-a172cedca-ns",
		"yeet-a172cedcaef-ns",
	} {
		t.Run(netNS, func(t *testing.T) {
			allocation := &db.ISOAllocation{
				Project: netip.MustParsePrefix("172.30.128.0/27"),
				Gateway: netip.MustParseAddr("172.30.128.1"),
				NetNS:   netNS,
				Components: map[string]db.ISOComponent{
					"api": {Address: netip.MustParseAddr("172.30.128.2")},
				},
			}
			raw := fmt.Sprintf(`{
				"name":"catch-app",
				"networks":{"default":{"name":"catch-app_default","driver":"yeet","driver_opts":{"dev.catchit.mode":"iso","dev.catchit.netns":%q},"enable_ipv6":false,"ipam":{"config":[{"subnet":"172.30.128.0/27","gateway":"172.30.128.1"}]}}},
				"services":{"api":{"image":"nginx","networks":{"default":{"ipv4_address":"172.30.128.2"}}}}
			}`, filepath.Join(isoDockerNetNSRoot, netNS))
			_, err := AdmitISOCompose([]byte(raw), ISOComposeAdmissionOptions{
				ProjectName:       "catch-app",
				ServiceRoot:       t.TempDir(),
				RequireISOOverlay: allocation,
			})
			assertISOAdmissionPath(t, err, "networks.default.driver_opts.dev.catchit.netns")
		})
	}
}

func TestAdmitISOComposePreservesSafeProjectData(t *testing.T) {
	root := t.TempDir()
	bindDir := filepath.Join(root, "bind")
	if err := os.Mkdir(bindDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(root, "app.conf")
	secretFile := filepath.Join(root, "token")
	for _, file := range []string{configFile, secretFile} {
		if err := os.WriteFile(file, []byte("safe"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	raw := fmt.Sprintf(`{
		"name":"catch-app",
		"volumes":{"data":{"name":"catch-app_data"}},
		"configs":{"app":{"name":"catch-app_app","file":%q},"inline":{"name":"catch-app_inline","content":"hello"}},
		"secrets":{"token":{"name":"catch-app_token","file":%q},"from_env":{"name":"catch-app_from_env","environment":"TOKEN"}},
		"services":{"api":{"image":"nginx","scale":1,"deploy":{"replicas":1},"sysctls":{"net.ipv4.ip_unprivileged_port_start":"0"},"volumes":[{"type":"bind","source":%q,"target":"/srv/bind","bind":{"create_host_path":false}},{"type":"volume","source":"data","target":"/srv/data","volume":{"nocopy":true,"subpath":"current"}},{"type":"tmpfs","target":"/tmp","tmpfs":{"size":1048576,"mode":493}}],"configs":[{"source":"app","target":"/etc/app.conf"}],"secrets":[{"source":"token","target":"token"}]}}
	}`, configFile, secretFile, bindDir)
	if _, err := admitImplicitISOTest(t, raw, ISOComposeAdmissionOptions{ProjectName: "catch-app", ServiceRoot: root}); err != nil {
		t.Fatal(err)
	}
}

func TestISOReservedPathsRejectStaticComposeSources(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{
		filepath.Join(root, "run"),
		filepath.Join(root, "bin"),
		filepath.Join(root, "tailscale"),
		filepath.Join(root, "data"),
		filepath.Join(root, "env"),
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{
		filepath.Join(root, "run", "child"),
		filepath.Join(root, "bin", "child"),
		filepath.Join(root, "tailscale", "child"),
		filepath.Join(root, "data", "storage"),
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{
		filepath.Join(root, "run", "control.conf"),
		filepath.Join(root, "bin", "control.conf"),
		filepath.Join(root, "tailscale", "control.conf"),
		filepath.Join(root, "env", "app.conf"),
		filepath.Join(root, "env", "token"),
	} {
		if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for _, tt := range []struct {
		name, kind, source string
	}{
		{name: "bind service root ancestor", kind: "bind", source: root},
		{name: "bind equal run", kind: "bind", source: filepath.Join(root, "run")},
		{name: "bind inside run", kind: "bind", source: filepath.Join(root, "run", "child")},
		{name: "bind equal bin", kind: "bind", source: filepath.Join(root, "bin")},
		{name: "bind inside bin", kind: "bind", source: filepath.Join(root, "bin", "child")},
		{name: "bind equal tailscale", kind: "bind", source: filepath.Join(root, "tailscale")},
		{name: "bind inside tailscale", kind: "bind", source: filepath.Join(root, "tailscale", "child")},
		{name: "config service root ancestor", kind: "config", source: root},
		{name: "config inside run", kind: "config", source: filepath.Join(root, "run", "control.conf")},
		{name: "config inside bin", kind: "config", source: filepath.Join(root, "bin", "control.conf")},
		{name: "config inside tailscale", kind: "config", source: filepath.Join(root, "tailscale", "control.conf")},
		{name: "secret service root ancestor", kind: "secret", source: root},
		{name: "secret inside run", kind: "secret", source: filepath.Join(root, "run", "control.conf")},
		{name: "secret inside bin", kind: "secret", source: filepath.Join(root, "bin", "control.conf")},
		{name: "secret inside tailscale", kind: "secret", source: filepath.Join(root, "tailscale", "control.conf")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw, wantPath := isoReservedPathComposeJSON(tt.kind, tt.source)
			_, err := admitImplicitISOTest(t, raw, ISOComposeAdmissionOptions{ProjectName: "catch-app", ServiceRoot: root})
			assertISOAdmissionPath(t, err, wantPath)
			if err != nil && !strings.Contains(strings.ToLower(err.Error()), "host-managed") {
				t.Fatalf("error = %v, want host-managed reserved-path rejection", err)
			}
		})
	}
}

func TestISOReservedPathsRejectSymlinkResolvedSources(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{
		filepath.Join(root, "run"),
		filepath.Join(root, "bin"),
		filepath.Join(root, "tailscale"),
		filepath.Join(root, "data", "links"),
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, reserved := range []string{"run", "bin", "tailscale"} {
		reservedFile := filepath.Join(root, reserved, "control.conf")
		if err := os.WriteFile(reservedFile, []byte("control"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, kind := range []string{"bind", "config", "secret"} {
			t.Run(kind+"_to_"+reserved, func(t *testing.T) {
				target := filepath.Join(root, reserved)
				if kind != "bind" {
					target = reservedFile
				}
				link := filepath.Join(root, "data", "links", kind+"-"+reserved)
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
				raw, wantPath := isoReservedPathComposeJSON(kind, link)
				_, err := admitImplicitISOTest(t, raw, ISOComposeAdmissionOptions{ProjectName: "catch-app", ServiceRoot: root})
				assertISOAdmissionPath(t, err, wantPath)
			})
		}
	}
}

func TestISOReservedPathsPermitNonOverlappingDataAndEnv(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data", "storage")
	envConfig := filepath.Join(root, "env", "app.conf")
	envSecret := filepath.Join(root, "env", "token")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{envConfig, envSecret} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("safe"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	raw := fmt.Sprintf(`{
		"name":"catch-app",
		"configs":{"app":{"name":"catch-app_app","file":%q}},
		"secrets":{"token":{"name":"catch-app_token","file":%q}},
		"services":{"api":{"image":"nginx","volumes":[{"type":"bind","source":%q,"target":"/data"}],"configs":[{"source":"app","target":"/etc/app.conf"}],"secrets":[{"source":"token","target":"token"}]}}
	}`, envConfig, envSecret, data)
	if _, err := admitImplicitISOTest(t, raw, ISOComposeAdmissionOptions{ProjectName: "catch-app", ServiceRoot: root}); err != nil {
		t.Fatalf("safe data/env sources rejected: %v", err)
	}
}

func isoReservedPathComposeJSON(kind, source string) (raw, wantPath string) {
	switch kind {
	case "bind":
		return fmt.Sprintf(`{"name":"catch-app","services":{"api":{"image":"nginx","volumes":[{"type":"bind","source":%q,"target":"/host"}]}}}`, source), "services.api.volumes[0].source"
	case "config":
		return fmt.Sprintf(`{"name":"catch-app","configs":{"control":{"name":"catch-app_control","file":%q}},"services":{"api":{"image":"nginx","configs":[{"source":"control","target":"/etc/control"}]}}}`, source), "configs.control.file"
	case "secret":
		return fmt.Sprintf(`{"name":"catch-app","secrets":{"control":{"name":"catch-app_control","file":%q}},"services":{"api":{"image":"nginx","secrets":[{"source":"control","target":"control"}]}}}`, source), "secrets.control.file"
	default:
		panic("unknown reserved-path source kind: " + kind)
	}
}

func TestAdmitISOComposeRejectsPathAndConstrainedFieldEscapes(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		raw  string
		path string
	}{
		{name: "symlink bind escape", raw: fmt.Sprintf(`{"services":{"api":{"image":"x","volumes":[{"type":"bind","source":%q,"target":"/host"}]}}}`, link), path: "services.api.volumes[0].source"},
		{name: "namespace handle", raw: `{"services":{"api":{"image":"x","volumes":[{"type":"bind","source":"/proc/1/ns/net","target":"/host"}]}}}`, path: "services.api.volumes[0].source"},
		{name: "scale", raw: `{"services":{"api":{"image":"x","scale":2}}}`, path: "services.api.scale"},
		{name: "replicas", raw: `{"services":{"api":{"image":"x","deploy":{"replicas":2}}}}`, path: "services.api.deploy.replicas"},
		{name: "host userns", raw: `{"services":{"api":{"image":"x","userns_mode":"host"}}}`, path: "services.api.userns_mode"},
		{name: "unconfined seccomp string", raw: `{"services":{"api":{"image":"x","security_opt":"seccomp=unconfined"}}}`, path: "services.api.security_opt"},
		{name: "unconfined apparmor list", raw: `{"services":{"api":{"image":"x","security_opt":["apparmor=unconfined"]}}}`, path: "services.api.security_opt[0]"},
		{name: "host sysctl", raw: `{"services":{"api":{"image":"x","sysctls":{"kernel.hostname":"escape"}}}}`, path: "services.api.sysctls.kernel.hostname"},
		{name: "unknown volume type", raw: `{"services":{"api":{"image":"x","volumes":[{"type":"image","source":"x","target":"/data"}]}}}`, path: "services.api.volumes[0].type"},
		{name: "bind propagation", raw: fmt.Sprintf(`{"services":{"api":{"image":"x","volumes":[{"type":"bind","source":%q,"target":"/data","bind":{"propagation":"rshared"}}]}}}`, root), path: "services.api.volumes[0].bind.propagation"},
		{name: "tmpfs host option", raw: `{"services":{"api":{"image":"x","volumes":[{"type":"tmpfs","target":"/data","tmpfs":{"future":true}}]}}}`, path: "services.api.volumes[0].tmpfs.future"},
		{name: "bind with volume options", raw: fmt.Sprintf(`{"services":{"api":{"image":"x","volumes":[{"type":"bind","source":%q,"target":"/data","volume":{"nocopy":true}}]}}}`, root), path: "services.api.volumes[0].volume"},
		{name: "invalid read only", raw: fmt.Sprintf(`{"services":{"api":{"image":"x","volumes":[{"type":"bind","source":%q,"target":"/data","read_only":"true"}]}}}`, root), path: "services.api.volumes[0].read_only"},
		{name: "privileged lifecycle hook", raw: `{"services":{"api":{"image":"x","post_start":[{"command":["true"],"privileged":true}]}}}`, path: "services.api.post_start[0].privileged"},
		{name: "blkio host device", raw: `{"services":{"api":{"image":"x","blkio_config":{"device_read_bps":[{"path":"/dev/sda","rate":"1mb"}]}}}}`, path: "services.api.blkio_config.device_read_bps"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := admitImplicitISOTest(t, tt.raw, ISOComposeAdmissionOptions{ServiceRoot: root})
			assertISOAdmissionPath(t, err, tt.path)
		})
	}
}

func TestAdmitISOComposeRejectsNonCanonicalScaleValues(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
		path string
	}{
		{name: "zero scale", raw: `{"services":{"api":{"image":"x","scale":0}}}`, path: "services.api.scale"},
		{name: "null scale", raw: `{"services":{"api":{"image":"x","scale":null}}}`, path: "services.api.scale"},
		{name: "zero replicas", raw: `{"services":{"api":{"image":"x","deploy":{"replicas":0}}}}`, path: "services.api.deploy.replicas"},
		{name: "null replicas", raw: `{"services":{"api":{"image":"x","deploy":{"replicas":null}}}}`, path: "services.api.deploy.replicas"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := admitImplicitISOTest(t, tt.raw, ISOComposeAdmissionOptions{ServiceRoot: t.TempDir()})
			assertISOAdmissionPath(t, err, tt.path)
		})
	}
}

func TestAdmitISOComposeRejectsUnreviewedSysctls(t *testing.T) {
	for _, name := range []string{
		"net.ipv4.ip_forward",
		"net.core.somaxconn",
		"fs.mqueue.msg_max",
	} {
		t.Run(name, func(t *testing.T) {
			raw := fmt.Sprintf(`{"services":{"api":{"image":"x","sysctls":{%q:"1"}}}}`, name)
			_, err := admitImplicitISOTest(t, raw, ISOComposeAdmissionOptions{ServiceRoot: t.TempDir()})
			assertISOAdmissionPath(t, err, "services.api.sysctls."+name)
		})
	}
}

func TestAdmitISOComposeRejectsNonObjectMountOptions(t *testing.T) {
	root := t.TempDir()
	for _, tt := range []struct {
		name string
		raw  string
		path string
	}{
		{
			name: "bind false",
			raw:  fmt.Sprintf(`{"services":{"api":{"image":"x","volumes":[{"type":"bind","source":%q,"target":"/data","bind":false}]}}}`, root),
			path: "services.api.volumes[0].bind",
		},
		{
			name: "volume zero",
			raw:  `{"volumes":{"data":{"name":"catch-app_data"}},"services":{"api":{"image":"x","volumes":[{"type":"volume","source":"data","target":"/data","volume":0}]}}}`,
			path: "services.api.volumes[0].volume",
		},
		{
			name: "tmpfs array",
			raw:  `{"services":{"api":{"image":"x","volumes":[{"type":"tmpfs","target":"/data","tmpfs":[]}]}}}`,
			path: "services.api.volumes[0].tmpfs",
		},
		{
			name: "mismatched empty object",
			raw:  fmt.Sprintf(`{"services":{"api":{"image":"x","volumes":[{"type":"bind","source":%q,"target":"/data","volume":{}}]}}}`, root),
			path: "services.api.volumes[0].volume",
		},
		{
			name: "mismatched false",
			raw:  fmt.Sprintf(`{"services":{"api":{"image":"x","volumes":[{"type":"bind","source":%q,"target":"/data","volume":false}]}}}`, root),
			path: "services.api.volumes[0].volume",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := admitImplicitISOTest(t, tt.raw, ISOComposeAdmissionOptions{ServiceRoot: root})
			assertISOAdmissionPath(t, err, tt.path)
		})
	}
}

func TestAdmitISOComposeRejectsSpecialHostResourceTypes(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	fifoLink := filepath.Join(root, "pipe-link")
	if err := os.Symlink(fifo, fifoLink); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name string
		raw  string
		opts ISOComposeAdmissionOptions
		path string
	}{
		{
			name: "character device bind",
			raw:  `{"services":{"api":{"image":"x","volumes":[{"type":"bind","source":"/dev/null","target":"/device"}]}}}`,
			opts: ISOComposeAdmissionOptions{ServiceRoot: string(filepath.Separator)},
			path: "services.api.volumes[0].source",
		},
		{
			name: "fifo bind through symlink",
			raw:  fmt.Sprintf(`{"services":{"api":{"image":"x","volumes":[{"type":"bind","source":%q,"target":"/pipe"}]}}}`, fifoLink),
			opts: ISOComposeAdmissionOptions{ServiceRoot: root},
			path: "services.api.volumes[0].source",
		},
		{
			name: "fifo config through symlink",
			raw: fmt.Sprintf(`{
				"configs":{"pipe":{"name":"catch-app_pipe","file":%q}},
				"services":{"api":{"image":"x","configs":[{"source":"pipe","target":"/pipe"}]}}
			}`, fifoLink),
			opts: ISOComposeAdmissionOptions{ServiceRoot: root},
			path: "configs.pipe.file",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := admitImplicitISOTest(t, tt.raw, tt.opts)
			assertISOAdmissionPath(t, err, tt.path)
		})
	}
}

func TestAdmitISOComposeRejectsNamespaceHandleByFilesystemType(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "ordinary-looking-handle")
	if err := os.WriteFile(source, []byte("handle"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolvedSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	original := inspectISONamespaceHandle
	var inspected string
	inspectISONamespaceHandle = func(path string) (bool, error) {
		inspected = path
		return true, nil
	}
	t.Cleanup(func() { inspectISONamespaceHandle = original })

	raw := fmt.Sprintf(`{"services":{"api":{"image":"x","volumes":[{"type":"bind","source":%q,"target":"/handle"}]}}}`, source)
	_, err = admitImplicitISOTest(t, raw, ISOComposeAdmissionOptions{ServiceRoot: root})
	assertISOAdmissionPath(t, err, "services.api.volumes[0].source")
	if inspected != resolvedSource {
		t.Fatalf("inspected namespace handle = %q, want resolved source %q", inspected, resolvedSource)
	}
}

func TestRejectISOSpecialHostResourceRejectsBlockDeviceMode(t *testing.T) {
	source := filepath.Join(t.TempDir(), "block-device")
	if err := os.WriteFile(source, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	original := statISOHostResource
	statISOHostResource = func(string) (os.FileInfo, error) {
		return isoTestFileInfo{FileInfo: info, mode: os.ModeDevice}, nil
	}
	t.Cleanup(func() { statISOHostResource = original })

	if err := rejectISOSpecialHostResource("services.api.volumes[0].source", source); err == nil || !strings.Contains(err.Error(), "services.api.volumes[0].source") {
		t.Fatalf("error = %v, want block-device path rejection", err)
	}
}

type isoTestFileInfo struct {
	os.FileInfo
	mode os.FileMode
}

func (i isoTestFileInfo) Mode() os.FileMode { return i.mode }

func TestAdmitISOComposeAllowsNullMountOptions(t *testing.T) {
	root := t.TempDir()
	bindSource := filepath.Join(root, "data", "storage")
	if err := os.MkdirAll(bindSource, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf(`{
		"volumes":{"data":{"name":"catch-app_data"}},
		"services":{"api":{"image":"x","volumes":[
			{"type":"bind","source":%q,"target":"/bind","bind":null},
			{"type":"volume","source":"data","target":"/volume","volume":null},
			{"type":"tmpfs","target":"/tmpfs","tmpfs":null}
		]}}
	}`, bindSource)
	if _, err := admitImplicitISOTest(t, raw, ISOComposeAdmissionOptions{ServiceRoot: root}); err != nil {
		t.Fatal(err)
	}
}

func TestAdmitISOComposeRejectsInvalidProjectData(t *testing.T) {
	root := t.TempDir()
	for _, tt := range []struct {
		name string
		raw  string
		path string
	}{
		{name: "custom volume name", raw: `{"name":"catch-app","services":{"api":{"image":"x"}},"volumes":{"data":{"name":"shared"}}}`, path: "volumes.data.name"},
		{name: "volume driver", raw: `{"name":"catch-app","services":{"api":{"image":"x"}},"volumes":{"data":{"name":"catch-app_data","driver":"local"}}}`, path: "volumes.data.driver"},
		{name: "config external", raw: `{"name":"catch-app","services":{"api":{"image":"x"}},"configs":{"app":{"external":true}}}`, path: "configs.app.external"},
		{name: "secret custom name", raw: `{"name":"catch-app","services":{"api":{"image":"x"}},"secrets":{"token":{"name":"shared","content":"x"}}}`, path: "secrets.token.name"},
		{name: "config unknown", raw: `{"name":"catch-app","services":{"api":{"image":"x"}},"configs":{"app":{"content":"x","future":"escape"}}}`, path: "configs.app.future"},
		{name: "invalid external type", raw: `{"name":"catch-app","services":{"api":{"image":"x"}},"volumes":{"data":{"name":"catch-app_data","external":"false"}}}`, path: "volumes.data.external"},
		{name: "missing resource source", raw: `{"name":"catch-app","services":{"api":{"image":"x"}},"configs":{"app":{"name":"catch-app_app"}}}`, path: "configs.app"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := admitImplicitISOTest(t, tt.raw, ISOComposeAdmissionOptions{ProjectName: "catch-app", ServiceRoot: root})
			assertISOAdmissionPath(t, err, tt.path)
		})
	}
}

func TestAdmitISOComposeRejectsMalformedCanonicalShapes(t *testing.T) {
	root := t.TempDir()
	for _, tt := range []struct {
		name string
		raw  string
		path string
	}{
		{name: "numeric project name", raw: `{"name":42,"services":{"api":{"image":"x"}}}`, path: "name:"},
		{name: "service map array", raw: `{"services":[]}`, path: "services:"},
		{name: "invalid network flag", raw: `{"name":"catch-app","networks":{"default":{"name":"catch-app_default","enable_ipv6":"false","ipam":{}}},"services":{"api":{"image":"x","networks":{"default":null}}}}`, path: "networks.default.enable_ipv6"},
		{name: "invalid config target", raw: `{"name":"catch-app","configs":{"app":{"name":"catch-app_app","content":"x"}},"services":{"api":{"image":"x","configs":[{"source":"app","target":42}]}}}`, path: "services.api.configs[0].target"},
		{name: "deploy devices", raw: `{"services":{"api":{"image":"x","deploy":{"resources":{"reservations":{"devices":[{"capabilities":["gpu"]}]}}}}}}`, path: "services.api.deploy.resources.reservations.devices"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			opts := ISOComposeAdmissionOptions{ServiceRoot: root}
			if strings.Contains(tt.raw, `"name":"catch-app"`) {
				opts.ProjectName = "catch-app"
			}
			_, err := admitImplicitISOTest(t, tt.raw, opts)
			assertISOAdmissionPath(t, err, tt.path)
		})
	}
}

func TestAdmitISOComposeRejectsMalformedOrOversizedProjects(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
		path string
	}{
		{name: "malformed", raw: `{`, path: "decode canonical Compose JSON"},
		{name: "unknown top level", raw: `{"services":{"api":{"image":"x"}},"future":"escape"}`, path: "future"},
		{name: "wrong project", raw: `{"name":"other","services":{"api":{"image":"x"}}}`, path: "name"},
		{name: "no services", raw: `{"name":"catch-app","services":{}}`, path: "services"},
		{name: "non-object service", raw: `{"name":"catch-app","services":{"api":true}}`, path: "services.api"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := admitImplicitISOTest(t, tt.raw, ISOComposeAdmissionOptions{ProjectName: "catch-app", ServiceRoot: t.TempDir()})
			assertISOAdmissionPath(t, err, tt.path)
		})
	}

	services := make([]string, 30)
	for i := range services {
		services[i] = fmt.Sprintf(`"svc%d":{"image":"x"}`, i)
	}
	raw := `{"services":{` + strings.Join(services, ",") + `}}`
	_, err := admitImplicitISOTest(t, raw, ISOComposeAdmissionOptions{ServiceRoot: t.TempDir(), MaxComponents: 29})
	assertISOAdmissionPath(t, err, "services")

	_, err = admitImplicitISOTest(t, raw, ISOComposeAdmissionOptions{ServiceRoot: t.TempDir(), MaxComponents: 100})
	assertISOAdmissionPath(t, err, "services")
}

func admitImplicitISOTest(t *testing.T, raw string, opts ISOComposeAdmissionOptions) (ISOComposeModel, error) {
	t.Helper()
	if opts.ProjectName == "" {
		opts.ProjectName = "catch-app"
	}
	canonical := canonicalImplicitISOJSON(t, raw, opts.ProjectName)
	return AdmitISOCompose(canonical, opts)
}

func canonicalImplicitISOJSON(t *testing.T, raw, projectName string) []byte {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &top); err != nil || top == nil {
		return []byte(raw)
	}
	if _, ok := top["name"]; !ok {
		top["name"] = json.RawMessage(fmt.Sprintf("%q", projectName))
	}
	if _, ok := top["networks"]; !ok {
		top["networks"] = json.RawMessage(fmt.Sprintf(`{"default":{"name":%q,"ipam":{}}}`, projectName+"_default"))
	}
	var services map[string]json.RawMessage
	if err := json.Unmarshal(top["services"], &services); err == nil {
		for name, rawService := range services {
			var service map[string]json.RawMessage
			if json.Unmarshal(rawService, &service) != nil || service == nil {
				continue
			}
			if _, ok := service["networks"]; !ok {
				service["networks"] = json.RawMessage(`{"default":null}`)
			}
			encoded, err := json.Marshal(service)
			if err != nil {
				t.Fatal(err)
			}
			services[name] = encoded
		}
		encoded, err := json.Marshal(services)
		if err != nil {
			t.Fatal(err)
		}
		top["services"] = encoded
	}
	encoded, err := json.Marshal(top)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func assertISOAdmissionPath(t *testing.T, err error, path string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("error = %v, want path %q", err, path)
	}
}
