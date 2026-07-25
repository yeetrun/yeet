// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/iso"
	"github.com/yeetrun/yeet/pkg/netns"
	"tailscale.com/ipn"
)

func withTailscaleResolverCatchPath(t *testing.T, path string) {
	t.Helper()
	previous := catchExecutablePath
	catchExecutablePath = func() (string, error) { return path, nil }
	t.Cleanup(func() { catchExecutablePath = previous })
}

func TestNewTailscaleDownloadSelectsTrackAndURL(t *testing.T) {
	tests := []struct {
		name      string
		version   string
		wantTrack string
		wantURL   string
	}{
		{
			name:      "stable",
			version:   "1.92.3",
			wantTrack: "stable",
			wantURL:   "https://pkgs.tailscale.com/stable/tailscale_1.92.3_amd64.tgz",
		},
		{
			name:      "unstable",
			version:   "1.93.0",
			wantTrack: "unstable",
			wantURL:   "https://pkgs.tailscale.com/unstable/tailscale_1.93.0_amd64.tgz",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := newTailscaleDownload(tt.version, "linux", "amd64")
			if err != nil {
				t.Fatalf("newTailscaleDownload returned error: %v", err)
			}
			if got.track != tt.wantTrack {
				t.Fatalf("track = %q, want %q", got.track, tt.wantTrack)
			}
			if got.url != tt.wantURL {
				t.Fatalf("url = %q, want %q", got.url, tt.wantURL)
			}
		})
	}
}

func TestNewTailscaleDownloadRejectsUnsupportedInputs(t *testing.T) {
	if _, err := newTailscaleDownload("not-semver", "linux", "amd64"); err == nil {
		t.Fatal("expected invalid version error")
	}
	_, err := newTailscaleDownload("1.92.3", "darwin", "arm64")
	if err == nil || !strings.Contains(err.Error(), "unsupported OS: darwin") {
		t.Fatalf("error = %v, want unsupported OS", err)
	}
}

func TestExtractTailscaleBinariesWritesExpectedArtifacts(t *testing.T) {
	dstDir := t.TempDir()
	archive := makeTailscaleArchive(t, map[string]string{
		"tailscale_1.92.3_amd64/tailscaled": "daemon",
		"tailscale_1.92.3_amd64/tailscale":  "client",
		"tailscale_1.92.3_amd64/README":     "ignore me",
	})

	if err := extractTailscaleBinaries(bytes.NewReader(archive), dstDir, "1.92.3"); err != nil {
		t.Fatalf("extractTailscaleBinaries returned error: %v", err)
	}

	assertFileContent(t, filepath.Join(dstDir, "tailscaled-1.92.3"), "daemon")
	assertFileContent(t, filepath.Join(dstDir, "tailscale-1.92.3"), "client")
	if _, err := os.Stat(filepath.Join(dstDir, "README-1.92.3")); !os.IsNotExist(err) {
		t.Fatalf("README artifact exists, stat err: %v", err)
	}
	assertExecutable(t, filepath.Join(dstDir, "tailscaled-1.92.3"))
	assertExecutable(t, filepath.Join(dstDir, "tailscale-1.92.3"))
}

func TestExtractTailscaleBinariesRequiresBothArtifacts(t *testing.T) {
	archive := makeTailscaleArchive(t, map[string]string{
		"tailscale_1.92.3_amd64/tailscaled": "daemon",
	})

	err := extractTailscaleBinaries(bytes.NewReader(archive), t.TempDir(), "1.92.3")
	if err == nil || !strings.Contains(err.Error(), "expected 2 binaries, got 1") {
		t.Fatalf("error = %v, want missing binary count", err)
	}
}

func TestDownloadTailscaleArchiveUsesHTTPClientAndClosesBody(t *testing.T) {
	var gotURL string
	closed := false
	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			gotURL = req.URL.String()
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: &trackingReadCloser{
					Reader: bytes.NewReader(makeTailscaleArchive(t, map[string]string{
						"tailscaled": "daemon",
						"tailscale":  "client",
					})),
					closed: &closed,
				},
				Header: make(http.Header),
			}, nil
		}),
	}

	dstDir := t.TempDir()
	if err := downloadTailscaleArchive(client, "https://example.test/tailscale.tgz", dstDir, "1.92.3"); err != nil {
		t.Fatalf("downloadTailscaleArchive returned error: %v", err)
	}
	if gotURL != "https://example.test/tailscale.tgz" {
		t.Fatalf("got URL %q", gotURL)
	}
	if !closed {
		t.Fatal("response body was not closed")
	}
	assertFileContent(t, filepath.Join(dstDir, "tailscale-1.92.3"), "client")
}

func TestNewTailscaleSystemdUnitPlansTapAndGuardedNetNSModes(t *testing.T) {
	tap, err := newTailscaleSystemdUnit(tailscaleInstallPlan{
		service:       "demo",
		runDir:        "/srv/demo/run",
		serviceTSDir:  "/srv/demo/tailscale",
		interfaceName: "ts0",
	})
	if err != nil {
		t.Fatalf("newTailscaleSystemdUnit TAP: %v", err)
	}
	if got := strings.Join(tap.Arguments, " "); !strings.Contains(got, "--tun=tap:ts0") {
		t.Fatalf("tap args = %q", got)
	}
	if tap.NetNS != "" || tap.Wants != "" || len(tap.ExecStartPre) != 0 {
		t.Fatalf("tap unit has netns fields: %+v", tap)
	}
	if tap.Executable != "/srv/demo/bin/tailscaled" || tap.ConditionExecutable != "" || tap.EnvFile != "/srv/demo/env/tailscaled.env" {
		t.Fatalf("tap managed paths = executable %q condition %q env %q", tap.Executable, tap.ConditionExecutable, tap.EnvFile)
	}
	if got := strings.Join(tap.Arguments, " "); strings.Contains(got, "tailscale-resolver-exec") || !strings.Contains(got, "--config=/srv/demo/env/tailscaled.json") || !strings.Contains(got, "--socket=/srv/demo/run/tailscaled.sock") {
		t.Fatalf("tap args use wrong stable/runtime paths: %q", got)
	}

	netns, err := newTailscaleSystemdUnit(tailscaleInstallPlan{
		service:       "demo",
		runDir:        "/srv/demo/run",
		serviceTSDir:  "/srv/demo/tailscale",
		runInNetNS:    "yeet-demo-net",
		interfaceName: "ts0",
		resolvConf:    "/etc/netns/yeet-demo-net/resolv.conf",
		catchBin:      "/srv/catch/run/catch",
	})
	if err != nil {
		t.Fatalf("newTailscaleSystemdUnit netns: %v", err)
	}
	if netns.Executable != "/srv/catch/run/catch" || netns.ConditionExecutable != "/srv/demo/bin/tailscaled" {
		t.Fatalf("netns executable and condition = %q, %q", netns.Executable, netns.ConditionExecutable)
	}
	if got, want := strings.Join(netns.Arguments, " "), "tailscale-resolver-exec --source /etc/netns/yeet-demo-net/resolv.conf -- /srv/demo/bin/tailscaled --statedir=. --socket=/srv/demo/run/tailscaled.sock --config=/srv/demo/env/tailscaled.json --tun=ts0"; got != want {
		t.Fatalf("netns args = %q, want %q", got, want)
	}
	if netns.Wants != "yeet-demo-net.service" || netns.After != "yeet-demo-net.service" {
		t.Fatalf("netns deps = wants %q after %q", netns.Wants, netns.After)
	}
	if netns.NetNS != "yeet-demo-net" || netns.ResolvConf != "" || !netns.PrivateMounts {
		t.Fatalf("netns fields = netns %q resolv %q private mounts %t", netns.NetNS, netns.ResolvConf, netns.PrivateMounts)
	}
	if len(netns.ExecStartPre) != 1 || netns.ExecStartPre[0] != "/bin/systemctl is-active --quiet yeet-demo-net.service" {
		t.Fatalf("ExecStartPre = %#v", netns.ExecStartPre)
	}
	if netns.EnvFile != "/srv/demo/env/tailscaled.env" || netns.WorkingDirectory != "/srv/demo/tailscale" {
		t.Fatalf("netns environment paths = env %q working directory %q", netns.EnvFile, netns.WorkingDirectory)
	}
	paths, err := netns.WriteOutUnitFiles(t.TempDir())
	if err != nil {
		t.Fatalf("WriteOutUnitFiles netns: %v", err)
	}
	raw, err := os.ReadFile(paths[db.ArtifactSystemdUnit])
	if err != nil {
		t.Fatalf("ReadFile netns unit: %v", err)
	}
	for _, want := range []string{
		"PrivateMounts=yes\n",
		"NetworkNamespacePath=/var/run/netns/yeet-demo-net\n",
		"ExecStart=/srv/catch/run/catch tailscale-resolver-exec --source /etc/netns/yeet-demo-net/resolv.conf -- /srv/demo/bin/tailscaled --statedir=. --socket=/srv/demo/run/tailscaled.sock --config=/srv/demo/env/tailscaled.json --tun=ts0\n",
	} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("netns unit missing %q:\n%s", want, raw)
		}
	}
	if strings.Contains(string(raw), "BindReadOnlyPaths=") {
		t.Fatalf("netns Tailscale unit retained replaceable resolver bind:\n%s", raw)
	}
}

func TestNewTailscaleSystemdUnitRejectsUnsafeResolverGuardPath(t *testing.T) {
	plan := tailscaleInstallPlan{
		service:       "demo",
		runDir:        "/srv/demo/run",
		serviceTSDir:  "/srv/demo/tailscale",
		runInNetNS:    "yeet-demo-net",
		interfaceName: "ts0",
		resolvConf:    "/etc/netns/yeet-demo-net/resolv.conf",
	}
	for _, catchBin := range []string{
		"catch",
		"/srv/catch/run/catch-v1",
		"/srv/catch/run/catch.install",
		"/srv/catch/run/../run/catch",
	} {
		t.Run(catchBin, func(t *testing.T) {
			plan.catchBin = catchBin
			if _, err := newTailscaleSystemdUnit(plan); err == nil {
				t.Fatalf("newTailscaleSystemdUnit accepted unsafe Catch path %q", catchBin)
			}
		})
	}
}

func TestNewTailscaleSystemdUnitUsesPersistedISONamespaceAndActualGateUnit(t *testing.T) {
	unit, err := newTailscaleSystemdUnit(tailscaleInstallPlan{
		service:       "demo",
		runDir:        "/srv/demo/run",
		serviceTSDir:  "/srv/demo/tailscale",
		runInNetNS:    "yeet-a172cedcae-ns",
		netNSUnit:     "yeet-demo-ns.service",
		interfaceName: "ts0",
		catchBin:      "/srv/catch/run/catch",
	})
	if err != nil {
		t.Fatalf("newTailscaleSystemdUnit: %v", err)
	}
	if unit.NetNS != "yeet-a172cedcae-ns" {
		t.Fatalf("NetNS = %q, want persisted ISO namespace", unit.NetNS)
	}
	if unit.Wants != "yeet-demo-ns.service" || unit.After != "yeet-demo-ns.service" ||
		len(unit.ExecStartPre) != 1 || unit.ExecStartPre[0] != "/bin/systemctl is-active --quiet yeet-demo-ns.service" {
		t.Fatalf("ISO gate ordering = wants %q after %q pre %#v", unit.Wants, unit.After, unit.ExecStartPre)
	}
}

func TestInstallTSWritesArtifactsWithoutNetworkWhenAuthKeyProvided(t *testing.T) {
	withTailscaleResolverCatchPath(t, "/srv/catch/run/catch")
	server := newTestServer(t)
	const (
		service = "demo"
		version = "1.92.3"
		authKey = "tskey-auth-test"
	)
	tsdDir := filepath.Join(server.cfg.RootDir, "tsd")
	if err := os.MkdirAll(tsdDir, 0o755); err != nil {
		t.Fatalf("mkdir tsd dir: %v", err)
	}
	tsdPath := filepath.Join(tsdDir, "tailscaled-"+version)
	if err := os.WriteFile(tsdPath, []byte("daemon"), 0o755); err != nil {
		t.Fatalf("write tailscaled: %v", err)
	}
	if err := os.MkdirAll(server.serviceBinDir(service), 0o755); err != nil {
		t.Fatalf("mkdir service bin dir: %v", err)
	}

	artifacts, err := server.installTS(service, "yeet-demo-net", &db.TailscaleNetwork{
		Interface: "ts0",
		Version:   version,
		ExitNode:  "exit.example",
	}, authKey, "/srv/demo/resolv.conf")
	if err != nil {
		t.Fatalf("installTS returned error: %v", err)
	}

	if artifacts[db.ArtifactTSBinary] != tsdPath {
		t.Fatalf("ArtifactTSBinary = %q, want %q", artifacts[db.ArtifactTSBinary], tsdPath)
	}
	if _, ok := artifacts[db.ArtifactSystemdUnit]; ok {
		t.Fatalf("unexpected ArtifactSystemdUnit: %#v", artifacts)
	}
	for _, name := range []db.ArtifactName{db.ArtifactTSConfig, db.ArtifactTSService, db.ArtifactTSEnv} {
		if artifacts[name] == "" {
			t.Fatalf("missing artifact %s in %#v", name, artifacts)
		}
	}

	rawCfg, err := os.ReadFile(artifacts[db.ArtifactTSConfig])
	if err != nil {
		t.Fatalf("read tailscaled config: %v", err)
	}
	var cfg ipn.ConfigVAlpha
	if err := json.Unmarshal(rawCfg, &cfg); err != nil {
		t.Fatalf("unmarshal tailscaled config: %v", err)
	}
	if cfg.Hostname == nil || *cfg.Hostname != service {
		t.Fatalf("Hostname = %#v, want %q", cfg.Hostname, service)
	}
	if cfg.AuthKey == nil || *cfg.AuthKey != authKey {
		t.Fatalf("AuthKey = %#v, want %q", cfg.AuthKey, authKey)
	}
	if cfg.ExitNode == nil || *cfg.ExitNode != "exit.example" {
		t.Fatalf("ExitNode = %#v, want exit.example", cfg.ExitNode)
	}
	if !cfg.AcceptDNS.EqualBool(false) {
		t.Fatalf("AcceptDNS = %q, want explicit false", cfg.AcceptDNS)
	}

	unitRaw, err := os.ReadFile(artifacts[db.ArtifactTSService])
	if err != nil {
		t.Fatalf("read tailscale service: %v", err)
	}
	unit := string(unitRaw)
	for _, want := range []string{
		"--tun=ts0",
		"NetworkNamespacePath=/var/run/netns/yeet-demo-net",
		"PrivateMounts=yes",
		"ExecStartPre=/bin/systemctl is-active --quiet yeet-demo-net.service",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit missing %q:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "BindReadOnlyPaths=") {
		t.Fatalf("unit retained replaceable resolver bind:\n%s", unit)
	}
}

func TestInstallTSUsesStableConfiguredCatchRunnerWhenExecutableIsVersioned(t *testing.T) {
	server := newTestServer(t)
	customCatchRoot := filepath.Join(t.TempDir(), "custom-catch")
	addTestServices(t, server, db.Service{
		Name:        CatchService,
		ServiceType: db.ServiceTypeSystemd,
		ServiceRoot: customCatchRoot,
	})

	oldCatchExecutablePath := catchExecutablePath
	var executablePathCalls int
	versionedCatch := filepath.Join(customCatchRoot, "bin", "catch-20260725035920")
	catchExecutablePath = func() (string, error) {
		executablePathCalls++
		return versionedCatch, nil
	}
	t.Cleanup(func() { catchExecutablePath = oldCatchExecutablePath })

	const (
		service = "demo-stable-runner"
		version = "1.92.3"
	)
	tsdDir := filepath.Join(server.cfg.RootDir, "tsd")
	if err := os.MkdirAll(tsdDir, 0o755); err != nil {
		t.Fatalf("mkdir tsd dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tsdDir, "tailscaled-"+version), []byte("daemon"), 0o755); err != nil {
		t.Fatalf("write tailscaled: %v", err)
	}
	if err := server.ensureDirs(service, ""); err != nil {
		t.Fatalf("ensure service dirs: %v", err)
	}

	artifacts, err := server.installTS(service, "yeet-demo-stable-runner-ns", &db.TailscaleNetwork{
		Interface: "ts0",
		Version:   version,
	}, "tskey-auth-test", "/etc/netns/yeet-demo-stable-runner-ns/resolv.conf")
	if err != nil {
		t.Fatalf("installTS: %v", err)
	}
	unitRaw, err := os.ReadFile(artifacts[db.ArtifactTSService])
	if err != nil {
		t.Fatalf("read Tailscale unit: %v", err)
	}
	stableCatch := filepath.Join(customCatchRoot, "run", "catch")
	if want := "ExecStart=" + stableCatch + " tailscale-resolver-exec "; !strings.Contains(string(unitRaw), want) {
		t.Fatalf("Tailscale unit missing stable configured Catch runner %q:\n%s", want, unitRaw)
	}
	if strings.Contains(string(unitRaw), versionedCatch) {
		t.Fatalf("Tailscale unit uses versioned Catch executable %q:\n%s", versionedCatch, unitRaw)
	}
	if executablePathCalls != 0 {
		t.Fatalf("Catch executable resolver called %d times, want 0", executablePathCalls)
	}
}

func TestInstallTSRejectsUnsafeResolverGuardBeforeTailscaleDownloadOrArtifactWrites(t *testing.T) {
	server := newTestServer(t)
	addTestServices(t, server, db.Service{
		Name:        CatchService,
		ServiceType: db.ServiceTypeSystemd,
		ServiceRoot: "relative-catch-root",
	})
	const (
		service = "demo"
		version = "1.92.3"
	)
	if err := server.ensureDirs(service, ""); err != nil {
		t.Fatalf("ensureDirs: %v", err)
	}
	oldHTTPClient := tailscaleHTTPClient
	var downloadAttempts int
	tailscaleHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		downloadAttempts++
		return nil, errors.New("tailscale download attempted")
	})}
	t.Cleanup(func() { tailscaleHTTPClient = oldHTTPClient })
	oldGenerateAuthKey := generateTailscaleAuthKeyFn
	var authKeyRequests int
	generateTailscaleAuthKeyFn = func(context.Context, []string) (string, error) {
		authKeyRequests++
		return "tskey-auth-test", nil
	}
	t.Cleanup(func() { generateTailscaleAuthKeyFn = oldGenerateAuthKey })

	artifacts, err := server.installTS(service, "yeet-demo-net", &db.TailscaleNetwork{
		Interface: "ts0",
		Version:   version,
	}, "", "/etc/netns/yeet-demo-net/resolv.conf")
	if err == nil || !strings.Contains(err.Error(), "resolver guard") {
		t.Fatalf("installTS error = %v, want unsafe resolver guard path", err)
	}
	if artifacts != nil {
		t.Fatalf("installTS artifacts = %#v, want nil after guard validation failure", artifacts)
	}
	if authKeyRequests != 0 {
		t.Fatalf("Tailscale auth key requests = %d, want 0 after guard validation failure", authKeyRequests)
	}
	if downloadAttempts != 0 {
		t.Fatalf("Tailscale download attempts = %d, want 0 after guard validation failure", downloadAttempts)
	}
	if _, err := os.Stat(filepath.Join(server.cfg.RootDir, "tsd")); !os.IsNotExist(err) {
		t.Fatalf("Tailscale cache artifacts were written before guard validation: %v", err)
	}
	if _, err := os.Stat(filepath.Join(server.defaultServiceRootDir(service), "tailscale")); !os.IsNotExist(err) {
		t.Fatalf("tailscale artifacts were written before guard validation: %v", err)
	}
}

func TestInstallTSTAPKeepsDirectTailscaledExecution(t *testing.T) {
	oldCatchExecutablePath := catchExecutablePath
	catchExecutablePath = func() (string, error) {
		t.Fatal("TAP install resolved Catch executable")
		return "", errors.New("unreachable")
	}
	t.Cleanup(func() { catchExecutablePath = oldCatchExecutablePath })

	server := newTestServer(t)
	const (
		service = "tap-demo"
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

	artifacts, err := server.installTS(service, "", &db.TailscaleNetwork{
		Interface: "ts0",
		Version:   version,
	}, "tskey-auth-test", "")
	if err != nil {
		t.Fatalf("installTS TAP: %v", err)
	}
	raw, err := os.ReadFile(artifacts[db.ArtifactTSService])
	if err != nil {
		t.Fatalf("read TAP Tailscale unit: %v", err)
	}
	unit := string(raw)
	daemon := filepath.Join(server.serviceBinDir(service), "tailscaled")
	for _, want := range []string{
		"ConditionFileIsExecutable=" + daemon + "\n",
		"ExecStart=" + daemon + " --statedir=. --socket=" + filepath.Join(server.serviceRunDir(service), "tailscaled.sock") + " --config=" + filepath.Join(server.serviceEnvDir(service), "tailscaled.json") + " --tun=tap:ts0\n",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("TAP Tailscale unit missing %q:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "tailscale-resolver-exec") {
		t.Fatalf("TAP Tailscale unit contains resolver guard:\n%s", unit)
	}
}

func TestISOTailscaleConfigKeepsAcceptDNSDisabled(t *testing.T) {
	path, err := writeTailscaleConfig(t.TempDir(), "iso-app", "tskey-auth-test", "")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg ipn.ConfigVAlpha
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.AcceptDNS.EqualBool(false) {
		t.Fatalf("ISO tailscale AcceptDNS = %q, want explicit false", cfg.AcceptDNS)
	}
}

func TestISOTailscaleTopologyKeepsPublicDefaultAndDelegatesTailnetToTS0(t *testing.T) {
	oldDetect := detectISOFirewallBackendForRuntime
	detectISOFirewallBackendForRuntime = func() (netns.FirewallBackend, error) { return netns.BackendNFT, nil }
	t.Cleanup(func() { detectISOFirewallBackendForRuntime = oldDetect })
	allocation := testISORuntimeAllocation("app", iso.StateReserved)
	allocation.DesiredModes = []string{"iso", "ts"}
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{"app": allocation})
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	spec, err := server.isoRuntimeSpec(dv, "app")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Topology.TailscaleInterface != "ts0" {
		t.Fatalf("TailscaleInterface = %q, want ts0", spec.Topology.TailscaleInterface)
	}
	commands, err := netns.ISOTopologyEnsureCommands(spec.Topology)
	if err != nil {
		t.Fatal(err)
	}
	var rendered strings.Builder
	for _, command := range commands {
		rendered.WriteString(command.Name)
		rendered.WriteByte(' ')
		rendered.WriteString(strings.Join(command.Args, " "))
		rendered.WriteByte('\n')
		rendered.WriteString(command.Input)
	}
	text := rendered.String()
	wantDefault := "ip netns exec " + allocation.NetNS + " ip route replace default via " + allocation.HostIP.String() + " dev " + allocation.PeerInterface
	if !strings.Contains(text, wantDefault) {
		t.Fatalf("topology missing ISO public default %q:\n%s", wantDefault, text)
	}
	if strings.Contains(text, "default dev ts0") || strings.Contains(text, "default via ts0") {
		t.Fatalf("topology moved ordinary default to ts0:\n%s", text)
	}
	if !strings.Contains(text, `oifname "ts0" accept`) || !strings.Contains(text, `iifname "ts0"`) {
		t.Fatalf("topology does not admit tailnet traffic exclusively through ts0:\n%s", text)
	}
	if strings.Contains(text, "100.64.0.0/10") {
		t.Fatalf("topology added a root/router CGNAT route or exception instead of relying on ts0 routes:\n%s", text)
	}
	if !netip.MustParsePrefix("100.64.0.0/10").Contains(netip.MustParseAddr("100.100.100.100")) {
		t.Fatal("test precondition: Quad100 must be in CGNAT space")
	}
}

func TestTailscaleBinaryGettersUseExistingFiles(t *testing.T) {
	server := newTestServer(t)
	tsdDir := filepath.Join(server.cfg.RootDir, "tsd")
	if err := os.MkdirAll(tsdDir, 0o755); err != nil {
		t.Fatalf("mkdir tsd: %v", err)
	}
	for _, name := range []string{"tailscale-1.92.3", "tailscaled-1.92.3"} {
		if err := os.WriteFile(filepath.Join(tsdDir, name), []byte(name), 0o755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	ts, err := server.getTailscaleBinary("1.92.3")
	if err != nil {
		t.Fatalf("getTailscaleBinary: %v", err)
	}
	if ts != filepath.Join(tsdDir, "tailscale-1.92.3") {
		t.Fatalf("tailscale path = %q", ts)
	}
	tsd, err := server.getTailscaledBinary("1.92.3")
	if err != nil {
		t.Fatalf("getTailscaledBinary: %v", err)
	}
	if tsd != filepath.Join(tsdDir, "tailscaled-1.92.3") {
		t.Fatalf("tailscaled path = %q", tsd)
	}
}

func TestDownloadTailscaleArchiveReturnsHTTPClientError(t *testing.T) {
	wantErr := errors.New("dial failed")
	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, wantErr
		}),
	}
	err := downloadTailscaleArchive(client, "https://example.test/tailscale.tgz", t.TempDir(), "1.92.3")
	if !errors.Is(err, wantErr) {
		t.Fatalf("download error = %v, want %v", err, wantErr)
	}
}

func TestExtractTailscaleBinaryIgnoresNonBinaryEntries(t *testing.T) {
	err := extractTailscaleBinary(&tar.Header{Name: "README.md"}, strings.NewReader("readme"), t.TempDir(), "1.92.3")
	if err != nil {
		t.Fatalf("extract non-binary: %v", err)
	}
}

func TestExtractOauthID(t *testing.T) {
	id, ok := extractOauthID("tskey-client-abc123-secret")
	if !ok || id != "abc123" {
		t.Fatalf("extractOauthID = %q, %v", id, ok)
	}
	if id, ok := extractOauthID("tskey-auth-abc123"); ok || id != "" {
		t.Fatalf("invalid extractOauthID = %q, %v", id, ok)
	}
}

func TestTSClientRejectsInvalidSecret(t *testing.T) {
	dir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWD)
	})
	if err := os.WriteFile("tailscale.key", []byte("not-an-oauth-secret"), 0o600); err != nil {
		t.Fatalf("write tailscale.key: %v", err)
	}

	if _, err := tsClient(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid tailscale oauth secret") {
		t.Fatalf("tsClient error = %v", err)
	}
}

func TestGenerateTailscaleAuthKeyFromSecretRejectsInvalidSecret(t *testing.T) {
	_, err := GenerateTailscaleAuthKeyFromSecret(context.Background(), "not-an-oauth-secret", []string{"tag:catch"})
	if err == nil || !strings.Contains(err.Error(), "invalid tailscale oauth secret") {
		t.Fatalf("GenerateTailscaleAuthKeyFromSecret error = %v, want invalid oauth secret", err)
	}
}

func TestResolveTailscaleAuthKeyUsesProvidedKey(t *testing.T) {
	server := newTestServer(t)
	key, err := server.resolveTailscaleAuthKey(&db.TailscaleNetwork{Tags: []string{"tag:svc"}}, "tskey-auth-provided")
	if err != nil {
		t.Fatalf("resolveTailscaleAuthKey: %v", err)
	}
	if key != "tskey-auth-provided" {
		t.Fatalf("auth key = %q", key)
	}
}

func TestResolveTailscaleAuthKeyAddsPolicyGuidance(t *testing.T) {
	old := generateTailscaleAuthKeyFn
	generateTailscaleAuthKeyFn = func(context.Context, []string) (string, error) {
		return "", errors.New("tailscale api: tag not owned")
	}
	t.Cleanup(func() {
		generateTailscaleAuthKeyFn = old
	})

	server := newTestServer(t)
	_, err := server.resolveTailscaleAuthKey(&db.TailscaleNetwork{Tags: []string{"tag:app"}}, "")
	if err == nil {
		t.Fatal("resolveTailscaleAuthKey error = nil, want policy guidance")
	}
	msg := err.Error()
	for _, want := range []string{
		"tagOwners",
		"tag:app",
		"yeet tailscale --setup",
		"https://yeetrun.com/docs/concepts/tailscale",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("policy guidance missing %q:\n%s", want, msg)
		}
	}
}

func TestWriteTailscaleConfigWithoutExitNode(t *testing.T) {
	path, err := writeTailscaleConfig(t.TempDir(), "svc", "tskey-auth-test", "")
	if err != nil {
		t.Fatalf("writeTailscaleConfig: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg ipn.ConfigVAlpha
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if cfg.ExitNode != nil {
		t.Fatalf("ExitNode = %#v, want nil", cfg.ExitNode)
	}
	if cfg.Hostname == nil || *cfg.Hostname != "svc" {
		t.Fatalf("Hostname = %#v", cfg.Hostname)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type trackingReadCloser struct {
	io.Reader
	closed *bool
}

func (r *trackingReadCloser) Close() error {
	*r.closed = true
	return nil
}

func makeTailscaleArchive(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range entries {
		b := []byte(content)
		if err := tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0o755,
			Size: int64(len(b)),
		}); err != nil {
			t.Fatalf("write tar header: %v", err)
		}
		if _, err := tw.Write(b); err != nil {
			t.Fatalf("write tar content: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func assertExecutable(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("%s mode = %v, want 0755", path, info.Mode().Perm())
	}
}
