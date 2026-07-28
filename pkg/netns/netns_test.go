// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package netns

import (
	"bytes"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/env"
)

func TestWriteServiceNetNSOrdersBeforeDockerPrereqs(t *testing.T) {
	root := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd returned error: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("Chdir returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldwd); err != nil {
			t.Fatalf("restore Chdir returned error: %v", err)
		}
	})

	binDir := filepath.Join(root, "bin")
	runDir := filepath.Join(root, "run")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("MkdirAll binDir returned error: %v", err)
	}
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatalf("MkdirAll runDir returned error: %v", err)
	}

	artifacts, err := WriteServiceNetNS(binDir, runDir, Service{ServiceName: "media"})
	if err != nil {
		t.Fatalf("WriteServiceNetNS returned error: %v", err)
	}
	raw, err := os.ReadFile(artifacts[db.ArtifactNetNSService])
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	got := string(raw)
	for _, want := range []string{
		"Requires=yeet-ns.service\n",
		"After=yeet-ns.service\n",
		"Before=yeet-docker-prereqs.target docker.service\n",
		"WantedBy=multi-user.target yeet-docker-prereqs.target\n",
		"EnvironmentFile=" + filepath.Join(root, "env", "netns.env") + "\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("unit missing %q:\n%s", want, got)
		}
	}
}

func TestWriteServiceNetNSWaitsForNetworkOnlineForMacvlan(t *testing.T) {
	root := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd returned error: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("Chdir returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldwd); err != nil {
			t.Fatalf("restore Chdir returned error: %v", err)
		}
	})

	binDir := filepath.Join(root, "bin")
	runDir := filepath.Join(root, "run")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("MkdirAll binDir returned error: %v", err)
	}
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatalf("MkdirAll runDir returned error: %v", err)
	}

	artifacts, err := WriteServiceNetNS(binDir, runDir, Service{
		ServiceName:   "media",
		MacvlanParent: "vmbr0",
	})
	if err != nil {
		t.Fatalf("WriteServiceNetNS returned error: %v", err)
	}
	raw, err := os.ReadFile(artifacts[db.ArtifactNetNSService])
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	got := string(raw)
	for _, want := range []string{
		"Wants=network-online.target\n",
		"Requires=yeet-ns.service\n",
		"After=yeet-ns.service network-online.target\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("unit missing %q:\n%s", want, got)
		}
	}
}

func TestWriteServiceNetNSRequiresTailscaleUnit(t *testing.T) {
	root := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd returned error: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("Chdir returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldwd); err != nil {
			t.Fatalf("restore Chdir returned error: %v", err)
		}
	})

	binDir := filepath.Join(root, "bin")
	runDir := filepath.Join(root, "run")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("MkdirAll binDir returned error: %v", err)
	}
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatalf("MkdirAll runDir returned error: %v", err)
	}

	artifacts, err := WriteServiceNetNS(binDir, runDir, Service{
		ServiceName:           "media",
		TailscaleTAPInterface: "tap0",
	})
	if err != nil {
		t.Fatalf("WriteServiceNetNS returned error: %v", err)
	}
	raw, err := os.ReadFile(artifacts[db.ArtifactNetNSService])
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	got := string(raw)
	for _, want := range []string{
		"Requires=yeet-ns.service yeet-media-ts.service\n",
		"After=yeet-ns.service yeet-media-ts.service\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("unit missing %q:\n%s", want, got)
		}
	}
	if svc := (&Service{ServiceName: "media"}).ServiceUnit(); svc != "yeet-media-ns.service" {
		t.Fatalf("ServiceUnit = %q, want yeet-media-ns.service", svc)
	}
}

func TestWriteNetNSScriptsWritesScriptsAndSkipsIdenticalFiles(t *testing.T) {
	chdirTemp(t)

	changed, err := writeNetNSScripts()
	if err != nil {
		t.Fatalf("writeNetNSScripts() returned error: %v", err)
	}
	if !changed {
		t.Fatal("writeNetNSScripts() changed = false, want true on first write")
	}
	for _, name := range []string{"service-ns", "yeet-ns"} {
		want, err := netnsScripts.ReadFile("netns-scripts/" + name)
		if err != nil {
			t.Fatalf("ReadFile embedded %s returned error: %v", name, err)
		}
		got, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("ReadFile %s returned error: %v", name, err)
		}
		if string(got) != string(want) {
			t.Fatalf("%s content mismatch", name)
		}
		info, err := os.Stat(name)
		if err != nil {
			t.Fatalf("Stat %s returned error: %v", name, err)
		}
		if gotMode := info.Mode().Perm(); gotMode != 0755 {
			t.Fatalf("%s mode = %v, want 0755", name, gotMode)
		}
	}
	if _, err := os.Stat("dhclient-enter-hook-yeet-netns-resolv"); !os.IsNotExist(err) {
		t.Fatalf("dhclient hook stat error = %v, want missing from working directory", err)
	}

	changed, err = writeNetNSScripts()
	if err != nil {
		t.Fatalf("second writeNetNSScripts() returned error: %v", err)
	}
	if changed {
		t.Fatal("second writeNetNSScripts() changed = true, want false for identical files")
	}
}

func TestServiceNSScriptUsesPerServiceDhclientLeaseFileAndNetNSResolverHook(t *testing.T) {
	raw, err := netnsScripts.ReadFile("netns-scripts/service-ns")
	if err != nil {
		t.Fatalf("ReadFile embedded service-ns returned error: %v", err)
	}
	got := string(raw)

	for _, want := range []string{
		`DHCP_LEASEFILE="/var/lib/dhcp/dhclient-${SERVICE_NAME}-${MACVLAN_INTERFACE}.leases"`,
		`DHCP_RELEASE="dhcpcd --nohook resolv.conf -k"`,
		`DHCP="dhclient -e YEET_NETNS_NAME=${NS_NAME} ${DHCP_RESOLV_CONF_ENV} -pf ${DHCP_PIDFILE} -lf ${DHCP_LEASEFILE}"`,
		`DHCP_RELEASE="dhclient -e YEET_NETNS_NAME=${NS_NAME} ${DHCP_RESOLV_CONF_ENV} -r -pf ${DHCP_PIDFILE} -lf ${DHCP_LEASEFILE}"`,
		`NETNS_ETC_DIR="${NETNS_ETC_DIR:-/etc/netns}"`,
		`refresh_netns_resolv_conf() {`,
		`mkdir -p "$NETNS_ETC_DIR/$NS_NAME"`,
		`cp /etc/resolv.conf "$NETNS_ETC_DIR/$NS_NAME/resolv.conf"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("service-ns missing %q:\n%s", want, got)
		}
	}
}

func TestServiceNSScriptPassesExplicitResolverToDhclientHook(t *testing.T) {
	raw, err := netnsScripts.ReadFile("netns-scripts/service-ns")
	if err != nil {
		t.Fatalf("ReadFile embedded service-ns returned error: %v", err)
	}
	got := string(raw)

	for _, want := range []string{
		`DHCP_RESOLV_CONF_ENV=""`,
		`DHCP_RESOLV_CONF_ENV="-e YEET_NETNS_RESOLV_CONF=${RESOLV_CONF}"`,
		`DHCP="dhclient -e YEET_NETNS_NAME=${NS_NAME} ${DHCP_RESOLV_CONF_ENV} -pf ${DHCP_PIDFILE} -lf ${DHCP_LEASEFILE}"`,
		`DHCP_RELEASE="dhclient -e YEET_NETNS_NAME=${NS_NAME} ${DHCP_RESOLV_CONF_ENV} -r -pf ${DHCP_PIDFILE} -lf ${DHCP_LEASEFILE}"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("service-ns missing %q:\n%s", want, got)
		}
	}
}

func TestServiceNSScriptRefreshesSeededNetNSResolver(t *testing.T) {
	raw, err := netnsScripts.ReadFile("netns-scripts/service-ns")
	if err != nil {
		t.Fatalf("ReadFile embedded service-ns returned error: %v", err)
	}
	got := string(raw)

	if strings.Contains(got, `[ ! -s "/etc/netns/$NS_NAME/resolv.conf" ]`) {
		t.Fatalf("service-ns skips non-empty stale netns resolver:\n%s", got)
	}
	if want := `if [ -r /etc/resolv.conf ]; then`; !strings.Contains(got, want) {
		t.Fatalf("service-ns missing %q:\n%s", want, got)
	}
	if want := `cp /etc/resolv.conf "$NETNS_ETC_DIR/$NS_NAME/resolv.conf"`; !strings.Contains(got, want) {
		t.Fatalf("service-ns missing %q:\n%s", want, got)
	}
	if strings.Count(got, `refresh_netns_resolv_conf`) < 3 {
		t.Fatalf("service-ns should define and call refresh_netns_resolv_conf for setup and cleanup:\n%s", got)
	}
}

func TestServiceNSScriptCleanupRemovesResolverDirectory(t *testing.T) {
	resolverDir := runServiceNSScriptCleanup(t, map[string]string{
		"resolv.conf": "nameserver 192.0.2.53\n",
	})

	if _, err := os.Stat(resolverDir); !os.IsNotExist(err) {
		t.Fatalf("resolver directory stat error = %v, want not exist", err)
	}
}

func TestServiceNSScriptCleanupPreservesUnexpectedFiles(t *testing.T) {
	resolverDir := runServiceNSScriptCleanup(t, map[string]string{
		"keep":        "operator state\n",
		"resolv.conf": "nameserver 192.0.2.53\n",
	})

	if _, err := os.Stat(filepath.Join(resolverDir, "resolv.conf")); !os.IsNotExist(err) {
		t.Fatalf("resolv.conf stat error = %v, want not exist", err)
	}
	got, err := os.ReadFile(filepath.Join(resolverDir, "keep"))
	if err != nil {
		t.Fatalf("ReadFile keep returned error: %v", err)
	}
	if string(got) != "operator state\n" {
		t.Fatalf("keep content = %q, want %q", got, "operator state\n")
	}
}

func runServiceNSScriptCleanup(t *testing.T, resolverFiles map[string]string) string {
	t.Helper()

	raw, err := netnsScripts.ReadFile("netns-scripts/service-ns")
	if err != nil {
		t.Fatalf("ReadFile embedded service-ns returned error: %v", err)
	}
	root := t.TempDir()
	scriptPath := filepath.Join(root, "service-ns")
	if err := os.WriteFile(scriptPath, raw, 0755); err != nil {
		t.Fatalf("WriteFile service-ns returned error: %v", err)
	}
	fakeBinDir := filepath.Join(root, "bin")
	if err := os.Mkdir(fakeBinDir, 0755); err != nil {
		t.Fatalf("Mkdir fake bin returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fakeBinDir, "ip"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("WriteFile fake ip returned error: %v", err)
	}

	netNSEtcDir := filepath.Join(root, "etc-netns")
	resolverDir := filepath.Join(netNSEtcDir, "yeet-cleanup-test-ns")
	if err := os.MkdirAll(resolverDir, 0755); err != nil {
		t.Fatalf("MkdirAll resolver directory returned error: %v", err)
	}
	for name, content := range resolverFiles {
		if err := os.WriteFile(filepath.Join(resolverDir, name), []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile resolver file %q returned error: %v", name, err)
		}
	}

	cmd := exec.Command(scriptPath, "cleanup")
	cmd.Env = append(os.Environ(),
		"SERVICE_NAME=cleanup-test",
		"NETNS_ETC_DIR="+netNSEtcDir,
		"PATH="+fakeBinDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("service-ns cleanup returned error: %v\n%s", err, output)
	}
	return resolverDir
}

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
		`tmp="$(mktemp "${target_dir}/resolv.conf.XXXXXX")"`,
		`if [ -n "${YEET_NETNS_RESOLV_CONF:-}" ]; then`,
		`cp "$YEET_NETNS_RESOLV_CONF" "$tmp"`,
		`chmod 0644 "$tmp"`,
		`if [ -z "${new_domain_name_servers:-}" ]; then`,
		`return 0`,
		`printf 'nameserver %s\n' "$server"`,
		`mv "$tmp" "$target"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("dhclient hook missing %q:\n%s", want, got)
		}
	}
}

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

func TestWriteDhclientEnterHookRepairsExistingHookMode(t *testing.T) {
	dir := t.TempDir()
	oldPath := dhclientEnterHookPath
	oldMkdirAll := mkdirAll
	dhclientEnterHookPath = filepath.Join(dir, "hooks", "yeet-netns-resolv")
	mkdirAll = os.MkdirAll
	t.Cleanup(func() {
		dhclientEnterHookPath = oldPath
		mkdirAll = oldMkdirAll
	})

	raw, err := netnsScripts.ReadFile("netns-scripts/dhclient-enter-hook-yeet-netns-resolv")
	if err != nil {
		t.Fatalf("ReadFile embedded hook returned error: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(dhclientEnterHookPath), 0o755); err != nil {
		t.Fatalf("MkdirAll hook dir returned error: %v", err)
	}
	if err := os.WriteFile(dhclientEnterHookPath, raw, 0o600); err != nil {
		t.Fatalf("WriteFile existing hook returned error: %v", err)
	}

	changed, err := writeDhclientEnterHook()
	if err != nil {
		t.Fatalf("writeDhclientEnterHook returned error: %v", err)
	}
	if !changed {
		t.Fatal("writeDhclientEnterHook changed = false, want true for mode repair")
	}
	info, err := os.Stat(dhclientEnterHookPath)
	if err != nil {
		t.Fatalf("Stat hook returned error: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("hook mode = %v, want 0644", got)
	}
}

func TestServiceNSScriptPinsServiceNetworkRoutes(t *testing.T) {
	raw, err := netnsScripts.ReadFile("netns-scripts/service-ns")
	if err != nil {
		t.Fatalf("ReadFile embedded service-ns returned error: %v", err)
	}
	got := string(raw)

	for _, want := range []string{
		`ip netns exec $NS_NAME ip route replace "$RANGE" dev "$IF_IN_NS_NAME"`,
		`ip netns exec $NS_NAME ip route replace "$HOST_IP/32" dev "$IF_IN_NS_NAME"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("service-ns missing %q:\n%s", want, got)
		}
	}
}

func TestWriteYeetNSEnvWritesAndSkipsIdenticalFiles(t *testing.T) {
	chdirTemp(t)
	ye := defaultYeetNSEnv(BackendNFT, "/usr/local/bin/catch")

	changed, err := writeYeetNSEnv(ye)
	if err != nil {
		t.Fatalf("writeYeetNSEnv returned error: %v", err)
	}
	if !changed {
		t.Fatalf("writeYeetNSEnv changed=false, want true on first write")
	}
	raw, err := os.ReadFile("yeet-ns.env")
	if err != nil {
		t.Fatalf("ReadFile env returned error: %v", err)
	}
	if got := string(raw); !strings.Contains(got, "FIREWALL_BACKEND=nft") || !strings.Contains(got, "CATCH_BIN=/usr/local/bin/catch") {
		t.Fatalf("env file missing expected values:\n%s", got)
	}

	changed, err = writeYeetNSEnv(ye)
	if err != nil {
		t.Fatalf("second writeYeetNSEnv returned error: %v", err)
	}
	if changed {
		t.Fatalf("second writeYeetNSEnv changed=true, want false for identical env")
	}
	if _, err := os.Stat("yeet-ns.env.tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp env file stat error = %v, want missing", err)
	}
}

func TestExecuteYeetNSSetupUsesDesiredEnvironment(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "environment")
	script := filepath.Join(root, "yeet-ns")
	content := "#!/bin/sh\nprintf '%s\\n' \"$RANGE\" \"$HOST_IP\" \"$BRIDGE_IP\" \"$YEET_IP\" \"$BRIDGE_IF\" \"$FIREWALL_BACKEND\" \"$CATCH_BIN\" > " + output + "\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("WriteFile script: %v", err)
	}
	environment := yeetNSEnv{
		Range:           "10.0.0.0/24",
		HostIP:          "10.0.0.1/32",
		BridgeIP:        "10.0.0.254/32",
		YeetIP:          "10.0.0.2/32",
		BridgeIf:        "bridge-test0",
		FirewallBackend: "nft",
		CatchBin:        "/flash/yeet/services/catch/run/catch",
	}

	if err := executeYeetNSSetup(script, environment); err != nil {
		t.Fatalf("executeYeetNSSetup: %v", err)
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("ReadFile output: %v", err)
	}
	want := strings.Join([]string{
		environment.Range,
		environment.HostIP,
		environment.BridgeIP,
		environment.YeetIP,
		environment.BridgeIf,
		environment.FirewallBackend,
		environment.CatchBin,
		"",
	}, "\n")
	if got := string(raw); got != want {
		t.Fatalf("command environment = %q, want %q", got, want)
	}
}

func TestYeetNSScriptConnectsHostPeerToServiceBridge(t *testing.T) {
	raw, err := netnsScripts.ReadFile("netns-scripts/yeet-ns")
	if err != nil {
		t.Fatalf("ReadFile yeet-ns script returned error: %v", err)
	}
	script := string(raw)
	for _, want := range []string{
		"if ! ip netns list | grep -q yeet-ns; then",
		"if ! ip netns exec yeet-ns ip link show br0; then",
		"if ! ip link show yeet0; then",
		"ip netns exec yeet-ns ip link set yeet0-peer master br0",
		"ip netns exec yeet-ns ip addr replace ${YEET_IP} dev br0",
		"ip netns exec yeet-ns ip addr replace ${BRIDGE_IP} dev br0",
		"ip netns exec yeet-ns ip route replace ${HOST_IP} dev br0",
		"ip netns exec yeet-ns ip route replace default via ${HOST_IP_BASE} dev br0",
		"ip route replace ${RANGE} dev ${BRIDGE_IF}",
		`"${CATCH_BIN}" netns-firewall ensure`,
		`"${CATCH_BIN}" netns-firewall verify`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("yeet-ns script missing %q:\n%s", want, script)
		}
	}
	for _, bad := range []string{
		"ip netns exec yeet-ns ip addr replace ${YEET_IP} dev yeet0-peer",
		"ip route replace ${RANGE} via ${YEET_IP_BASE} dev ${BRIDGE_IF}",
		"ip route del ${RANGE}",
	} {
		if strings.Contains(script, bad) {
			t.Fatalf("yeet-ns script contains obsolete routed host peer command %q:\n%s", bad, script)
		}
	}
}

func TestInstallYeetNSServiceNoopsWhenArtifactsAreCurrent(t *testing.T) {
	root := chdirTemp(t)
	systemdPath := filepath.Join(root, "systemd", "yeet-ns.service")
	if err := os.MkdirAll(filepath.Dir(systemdPath), 0755); err != nil {
		t.Fatalf("MkdirAll systemd dir returned error: %v", err)
	}

	catchBin := "/usr/local/bin/catch"
	withDetectedFirewallBackend(t, BackendNFT)
	withInstallYeetNSServiceFakes(t, installYeetNSServiceFakes{
		systemdPath: systemdPath,
		newService: func(cfg db.ServiceView, runDir string) (yeetNSServiceInstaller, error) {
			t.Fatalf("new systemd service called for unchanged artifacts")
			return nil, nil
		},
		unitActive: func(unit string) bool {
			t.Fatalf("systemdUnitActive called for unchanged artifacts")
			return false
		},
	})
	writeCurrentYeetNSArtifacts(t, BackendNFT, catchBin, systemdPath)

	if err := InstallYeetNSService(catchBin); err != nil {
		t.Fatalf("InstallYeetNSService() returned error: %v", err)
	}
}

func TestInstallYeetNSServiceLogsWhenArtifactsAreCurrent(t *testing.T) {
	root := chdirTemp(t)
	systemdPath := filepath.Join(root, "systemd", "yeet-ns.service")
	if err := os.MkdirAll(filepath.Dir(systemdPath), 0755); err != nil {
		t.Fatalf("MkdirAll systemd dir returned error: %v", err)
	}

	catchBin := "/usr/local/bin/catch"
	withDetectedFirewallBackend(t, BackendNFT)
	withInstallYeetNSServiceFakes(t, installYeetNSServiceFakes{
		systemdPath: systemdPath,
		newService: func(cfg db.ServiceView, runDir string) (yeetNSServiceInstaller, error) {
			t.Fatalf("new systemd service called for unchanged artifacts")
			return nil, nil
		},
		unitActive: func(unit string) bool {
			t.Fatalf("systemdUnitActive called for unchanged artifacts")
			return false
		},
	})
	writeCurrentYeetNSArtifacts(t, BackendNFT, catchBin, systemdPath)

	var logs bytes.Buffer
	previousLogOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousLogOutput) })

	if err := InstallYeetNSService(catchBin); err != nil {
		t.Fatalf("InstallYeetNSService() returned error: %v", err)
	}
	if got := logs.String(); !strings.Contains(got, "yeet-ns artifacts unchanged") {
		t.Fatalf("InstallYeetNSService() logs = %q, want unchanged artifact message", got)
	}
}

func TestInstallYeetNSServiceConvergesActiveNamespaceAfterArtifactChange(t *testing.T) {
	chdirTemp(t)
	const stableCatchRunner = "/flash/yeet/services/catch/run/catch"
	var installCalls int
	var startCalls int
	var setupCalls int
	installed := false
	var gotSetupPath string
	var gotSetupEnv yeetNSEnv

	withDetectedFirewallBackend(t, BackendNFT)
	withInstallYeetNSServiceFakes(t, installYeetNSServiceFakes{
		systemdPath: filepath.Join(t.TempDir(), "yeet-ns.service"),
		newService: func(cfg db.ServiceView, runDir string) (yeetNSServiceInstaller, error) {
			if got := cfg.Name(); got != "yeet-ns" {
				t.Fatalf("service name = %q, want yeet-ns", got)
			}
			if runDir != "." {
				t.Fatalf("runDir = %q, want .", runDir)
			}
			return fakeYeetNSSystemdService{
				install: func() error {
					installCalls++
					installed = true
					return nil
				},
				start: func() error {
					startCalls++
					return nil
				},
			}, nil
		},
		unitActive: func(unit string) bool {
			return unit == "yeet-ns.service"
		},
		runSetup: func(path string, environment yeetNSEnv) error {
			if !installed {
				t.Fatal("setup ran before updated artifacts were installed")
			}
			setupCalls++
			gotSetupPath = path
			gotSetupEnv = environment
			return nil
		},
	})
	if err := env.Write("yeet-ns.env", defaultYeetNSEnv(BackendNFT, "/flash/yeet/services/catch/bin/catch-20260727")); err != nil {
		t.Fatalf("seed yeet-ns.env: %v", err)
	}

	if err := InstallYeetNSService(stableCatchRunner); err != nil {
		t.Fatalf("InstallYeetNSService() returned error: %v", err)
	}
	if installCalls != 1 {
		t.Fatalf("install calls = %d, want 1", installCalls)
	}
	if startCalls != 0 {
		t.Fatalf("start calls = %d, want 0 for active namespace", startCalls)
	}
	if setupCalls != 1 {
		t.Fatalf("setup calls = %d, want 1 for active namespace", setupCalls)
	}
	if want := filepath.Join(mustGetwd(t), "yeet-ns"); gotSetupPath != want {
		t.Fatalf("setup path = %q, want %q", gotSetupPath, want)
	}
	if gotSetupEnv.CatchBin != stableCatchRunner {
		t.Fatalf("setup CATCH_BIN = %q, want stable runner %q", gotSetupEnv.CatchBin, stableCatchRunner)
	}
	raw, err := os.ReadFile("yeet-ns.env")
	if err != nil {
		t.Fatalf("ReadFile yeet-ns.env: %v", err)
	}
	if got := string(raw); !strings.Contains(got, "CATCH_BIN="+stableCatchRunner) || strings.Contains(got, "catch-20260727") {
		t.Fatalf("yeet-ns.env = %q, want rewritten stable runner", got)
	}
}

func TestInstallYeetNSServiceStartsInactiveNamespace(t *testing.T) {
	chdirTemp(t)
	var installCalls int
	var startCalls int

	withDetectedFirewallBackend(t, BackendNFT)
	withInstallYeetNSServiceFakes(t, installYeetNSServiceFakes{
		systemdPath: filepath.Join(t.TempDir(), "yeet-ns.service"),
		newService: func(cfg db.ServiceView, runDir string) (yeetNSServiceInstaller, error) {
			return fakeYeetNSSystemdService{
				install: func() error {
					installCalls++
					return nil
				},
				start: func() error {
					startCalls++
					return nil
				},
			}, nil
		},
		unitActive: func(unit string) bool {
			return false
		},
	})

	if err := InstallYeetNSService("/flash/yeet/services/catch/run/catch"); err != nil {
		t.Fatalf("InstallYeetNSService() returned error: %v", err)
	}
	if installCalls != 1 {
		t.Fatalf("install calls = %d, want 1", installCalls)
	}
	if startCalls != 1 {
		t.Fatalf("start calls = %d, want 1 for inactive namespace", startCalls)
	}
}

func TestInstallYeetNSServicePropagatesInstallerErrors(t *testing.T) {
	tests := []struct {
		name    string
		service fakeYeetNSSystemdService
		factory func() (yeetNSServiceInstaller, error)
		want    string
	}{
		{
			name: "create service",
			factory: func() (yeetNSServiceInstaller, error) {
				return nil, errors.New("create failed")
			},
			want: "failed to create service",
		},
		{
			name: "install service",
			service: fakeYeetNSSystemdService{
				install: func() error { return errors.New("install failed") },
				start:   func() error { return nil },
			},
			want: "failed to install service",
		},
		{
			name: "start service",
			service: fakeYeetNSSystemdService{
				install: func() error { return nil },
				start:   func() error { return errors.New("start failed") },
			},
			want: "failed to start yeet-ns service",
		},
		{
			name: "converge active namespace",
			service: fakeYeetNSSystemdService{
				install: func() error { return nil },
				start:   func() error { t.Fatal("Start called after active setup failure"); return nil },
			},
			want: "failed to converge active yeet-ns service in place",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chdirTemp(t)
			withDetectedFirewallBackend(t, BackendNFT)
			withInstallYeetNSServiceFakes(t, installYeetNSServiceFakes{
				systemdPath: filepath.Join(t.TempDir(), "yeet-ns.service"),
				newService: func(db.ServiceView, string) (yeetNSServiceInstaller, error) {
					if tt.factory != nil {
						return tt.factory()
					}
					return tt.service, nil
				},
				unitActive: func(string) bool { return tt.name == "converge active namespace" },
				runSetup: func(string, yeetNSEnv) error {
					if tt.name == "converge active namespace" {
						return errors.New("setup failed")
					}
					return nil
				},
			})

			err := InstallYeetNSService("/flash/yeet/services/catch/run/catch")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("InstallYeetNSService error = %v, want %q", err, tt.want)
			}
		})
	}
}

type installYeetNSServiceFakes struct {
	systemdPath string
	newService  func(db.ServiceView, string) (yeetNSServiceInstaller, error)
	unitActive  func(string) bool
	runSetup    func(string, yeetNSEnv) error
}

type fakeYeetNSSystemdService struct {
	install func() error
	start   func() error
}

func (s fakeYeetNSSystemdService) Install() error {
	return s.install()
}

func (s fakeYeetNSSystemdService) Start() error {
	return s.start()
}

func chdirTemp(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd returned error: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("Chdir returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldwd); err != nil {
			t.Fatalf("restore Chdir returned error: %v", err)
		}
	})
	return root
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	return wd
}

func withDetectedFirewallBackend(t *testing.T, backend FirewallBackend) {
	t.Helper()

	oldLookPath := lookPath
	oldRunCombinedOutput := runCombinedOutput
	lookPath = func(name string) (string, error) {
		if backend == BackendNFT && name == "nft" {
			return "/usr/sbin/nft", nil
		}
		return "", os.ErrNotExist
	}
	runCombinedOutput = func(name string, args ...string) ([]byte, error) {
		if backend == BackendNFT && name == "nft" && strings.Join(args, " ") == "--version" {
			return []byte("nftables v1.0.9"), nil
		}
		return nil, os.ErrNotExist
	}
	t.Cleanup(func() {
		lookPath = oldLookPath
		runCombinedOutput = oldRunCombinedOutput
	})
}

func withInstallYeetNSServiceFakes(t *testing.T, fakes installYeetNSServiceFakes) {
	t.Helper()

	oldDhclientEnterHookPath := dhclientEnterHookPath
	oldSystemdUnitPath := systemdUnitPath
	oldNewSystemdService := newYeetNSSystemdService
	oldSystemdUnitActive := systemdUnitActive
	oldRunSetup := runYeetNSSetup
	dhclientEnterHookPath = filepath.Join(t.TempDir(), "dhclient-enter-hooks.d", "yeet-netns-resolv")
	systemdUnitPath = func(unit string) string {
		if unit != "yeet-ns.service" {
			t.Fatalf("systemdUnitPath(%q), want yeet-ns.service", unit)
		}
		return fakes.systemdPath
	}
	newYeetNSSystemdService = fakes.newService
	systemdUnitActive = fakes.unitActive
	runYeetNSSetup = fakes.runSetup
	t.Cleanup(func() {
		dhclientEnterHookPath = oldDhclientEnterHookPath
		systemdUnitPath = oldSystemdUnitPath
		newYeetNSSystemdService = oldNewSystemdService
		systemdUnitActive = oldSystemdUnitActive
		runYeetNSSetup = oldRunSetup
	})
}

func writeCurrentYeetNSArtifacts(t *testing.T, backend FirewallBackend, catchBin, systemdPath string) {
	t.Helper()

	if changed, err := writeNetNSScripts(); err != nil {
		t.Fatalf("writeNetNSScripts() returned error: %v", err)
	} else if !changed {
		t.Fatal("writeNetNSScripts() changed = false, want true during setup")
	}
	if changed, err := writeDhclientEnterHook(); err != nil {
		t.Fatalf("writeDhclientEnterHook() returned error: %v", err)
	} else if !changed {
		t.Fatal("writeDhclientEnterHook() changed = false, want true during setup")
	}
	if err := env.Write("yeet-ns.env", defaultYeetNSEnv(backend, catchBin)); err != nil {
		t.Fatalf("env.Write returned error: %v", err)
	}
	unitFiles, err := newYeetNSUnit().WriteOutUnitFiles(".")
	if err != nil {
		t.Fatalf("WriteOutUnitFiles returned error: %v", err)
	}
	raw, err := os.ReadFile(unitFiles[db.ArtifactSystemdUnit])
	if err != nil {
		t.Fatalf("ReadFile generated unit returned error: %v", err)
	}
	if err := os.WriteFile(systemdPath, raw, 0644); err != nil {
		t.Fatalf("WriteFile systemd unit returned error: %v", err)
	}
	for _, path := range unitFiles {
		if err := os.Remove(path); err != nil {
			t.Fatalf("Remove generated unit returned error: %v", err)
		}
	}
}
