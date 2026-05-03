// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/codecutil"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/ftdetect"
)

func TestHostDefaultRouteInterfaceFromProcRoute(t *testing.T) {
	routeTable := strings.Join([]string{
		"Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT",
		"docker0\t000011AC\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0",
		"vmbr0\t00000000\t0104000A\t0003\t0\t0\t0\t00000000\t0\t0\t0",
	}, "\n")

	iface, err := hostDefaultRouteInterfaceFromProcRoute(strings.NewReader(routeTable))
	if err != nil {
		t.Fatalf("hostDefaultRouteInterfaceFromProcRoute returned error: %v", err)
	}
	if iface != "vmbr0" {
		t.Fatalf("interface = %q, want %q", iface, "vmbr0")
	}
}

func TestParseNetworkLANUsesHostDefaultRoute(t *testing.T) {
	oldHostDefaultRouteInterfaceFn := hostDefaultRouteInterfaceFn
	defer func() {
		hostDefaultRouteInterfaceFn = oldHostDefaultRouteInterfaceFn
	}()
	hostDefaultRouteInterfaceFn = func() (string, error) {
		return "vmbr0", nil
	}

	installer := &FileInstaller{
		s: newTestServer(t),
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{
				ServiceName: "svc-lan",
			},
			Network: NetworkOpts{
				Interfaces: "lan",
			},
		},
	}

	if err := installer.parseNetwork(); err != nil {
		t.Fatalf("parseNetwork returned error: %v", err)
	}
	if installer.macvlan == nil {
		t.Fatalf("expected macvlan config to be created")
	}
	if installer.macvlan.Parent != "vmbr0" {
		t.Fatalf("macvlan parent = %q, want %q", installer.macvlan.Parent, "vmbr0")
	}
}

func TestParseNetworkLANExplicitParentOverridesHostDefaultRoute(t *testing.T) {
	oldHostDefaultRouteInterfaceFn := hostDefaultRouteInterfaceFn
	defer func() {
		hostDefaultRouteInterfaceFn = oldHostDefaultRouteInterfaceFn
	}()
	hostDefaultRouteInterfaceFn = func() (string, error) {
		return "vmbr0", nil
	}

	installer := &FileInstaller{
		s: newTestServer(t),
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{
				ServiceName: "svc-lan",
			},
			Network: NetworkOpts{
				Interfaces: "lan",
				Macvlan: MacvlanOpts{
					Parent: "eno1",
				},
			},
		},
	}

	if err := installer.parseNetwork(); err != nil {
		t.Fatalf("parseNetwork returned error: %v", err)
	}
	if installer.macvlan == nil {
		t.Fatalf("expected macvlan config to be created")
	}
	if installer.macvlan.Parent != "eno1" {
		t.Fatalf("macvlan parent = %q, want %q", installer.macvlan.Parent, "eno1")
	}
}

func TestParseNetworkAppliesCombinedNetworkOptions(t *testing.T) {
	oldHostDefaultRouteInterfaceFn := hostDefaultRouteInterfaceFn
	defer func() {
		hostDefaultRouteInterfaceFn = oldHostDefaultRouteInterfaceFn
	}()
	hostDefaultRouteInterfaceFn = func() (string, error) {
		return "vmbr0", nil
	}

	server := newTestServer(t)
	addTestServices(t, server, db.Service{
		Name:       "existing-svc",
		SvcNetwork: &db.SvcNetwork{IPv4: netipMustParseAddr(t, "192.168.100.3")},
	})

	installer := &FileInstaller{
		s: server,
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{
				ServiceName: "svc-combined",
			},
			Network: NetworkOpts{
				Interfaces: "ts,svc,lan",
				Tailscale: TailscaleOpts{
					Version:  "1.2.3",
					ExitNode: "100.64.0.1",
					Tags:     []string{"tag:yeet"},
					AuthKey:  "tskey-auth",
				},
				Macvlan: MacvlanOpts{
					Parent: "eno1",
					VLAN:   42,
					Mac:    "02:00:00:00:00:42",
				},
			},
		},
	}

	if err := installer.parseNetwork(); err != nil {
		t.Fatalf("parseNetwork returned error: %v", err)
	}
	if installer.tsNet == nil {
		t.Fatal("expected tailscale config")
	}
	if !strings.HasPrefix(installer.tsNet.Interface, "yts-") {
		t.Fatalf("tailscale interface = %q, want yts-*", installer.tsNet.Interface)
	}
	if installer.tsNet.Version != "1.2.3" {
		t.Fatalf("tailscale version = %q, want %q", installer.tsNet.Version, "1.2.3")
	}
	if installer.tsNet.ExitNode != "100.64.0.1" {
		t.Fatalf("tailscale exit node = %q, want %q", installer.tsNet.ExitNode, "100.64.0.1")
	}
	if len(installer.tsNet.Tags) != 1 || installer.tsNet.Tags[0] != "tag:yeet" {
		t.Fatalf("tailscale tags = %#v, want [tag:yeet]", installer.tsNet.Tags)
	}
	if installer.tsAuthKey != "tskey-auth" {
		t.Fatalf("tailscale auth key = %q, want %q", installer.tsAuthKey, "tskey-auth")
	}
	if installer.svcNet == nil {
		t.Fatal("expected svc network config")
	}
	if got := installer.svcNet.IPv4.String(); got != "192.168.100.4" {
		t.Fatalf("svc ip = %q, want %q", got, "192.168.100.4")
	}
	if installer.macvlan == nil {
		t.Fatal("expected macvlan config")
	}
	if !strings.HasPrefix(installer.macvlan.Interface, "ymv-") {
		t.Fatalf("macvlan interface = %q, want ymv-*", installer.macvlan.Interface)
	}
	if installer.macvlan.Parent != "eno1" {
		t.Fatalf("macvlan parent = %q, want %q", installer.macvlan.Parent, "eno1")
	}
	if installer.macvlan.VLAN != 42 {
		t.Fatalf("macvlan vlan = %d, want %d", installer.macvlan.VLAN, 42)
	}
	if installer.macvlan.Mac != "02:00:00:00:00:42" {
		t.Fatalf("macvlan mac = %q, want %q", installer.macvlan.Mac, "02:00:00:00:00:42")
	}
}

func TestRewriteSystemdUnitContentReplacesOnlyExecStart(t *testing.T) {
	input := strings.Join([]string{
		"[Unit]",
		"Description=old app",
		"",
		"[Service]",
		"Environment=MODE=prod",
		"  ExecStart=/old/app --stale",
		"ExecStartPost=/bin/true",
	}, "\n")

	got := rewriteSystemdUnitContent(input, "/srv/app", []string{"--flag", "value"})
	want := strings.Join([]string{
		"[Unit]",
		"Description=old app",
		"",
		"[Service]",
		"Environment=MODE=prod",
		"ExecStart=/srv/app --flag value",
		"ExecStartPost=/bin/true",
		"",
	}, "\n")
	if got != want {
		t.Fatalf("rewritten unit:\n%s\nwant:\n%s", got, want)
	}
}

func TestBuildNetNSResolvConfIncludesOptionalSearchDomains(t *testing.T) {
	got := buildNetNSResolvConf("1.1.1.1", "svc.local example.com")
	want := "nameserver 1.1.1.1\nsearch svc.local example.com\n"
	if got != want {
		t.Fatalf("resolv.conf = %q, want %q", got, want)
	}
}

func TestInstallerCloseStagesEnvFileAndCleansTemp(t *testing.T) {
	server := newTestServer(t)
	installer, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{ServiceName: "env-svc"},
		EnvFile:      true,
		StageOnly:    true,
	})
	if err != nil {
		t.Fatalf("NewFileInstaller returned error: %v", err)
	}
	tmpDir := installer.tmpDir
	tmpPath := installer.tempFilePath()

	n, err := installer.Write([]byte("A=1\n"))
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if _, err := installer.WriteAt([]byte("B=2\n"), int64(n)); err != nil {
		t.Fatalf("WriteAt returned error: %v", err)
	}
	if got, want := installer.Received(), float64(len("A=1\nB=2\n")); got != want {
		t.Fatalf("Received = %v, want %v", got, want)
	}
	_ = installer.Rate()

	if err := installer.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if err := installer.Wait(); err != nil {
		t.Fatalf("Wait returned error: %v", err)
	}
	if !installer.closed {
		t.Fatal("installer was not marked closed")
	}
	if installer.File != nil {
		t.Fatal("temporary file handle was not cleared")
	}
	if _, err := os.Stat(tmpDir); !os.IsNotExist(err) {
		t.Fatalf("temp dir still exists after Close: %v", err)
	}
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("temp path still exists after Close: %v", err)
	}
	if err := installer.Close(); err != nil {
		t.Fatalf("second Close returned error: %v", err)
	}

	service := testService(t, server, "env-svc")
	envPath := stagedArtifactPath(t, service, db.ArtifactEnvFile)
	raw, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) returned error: %v", envPath, err)
	}
	if string(raw) != "A=1\nB=2\n" {
		t.Fatalf("env file content = %q, want staged payload", string(raw))
	}
}

func TestInstallerCloseFailedCleansTempAndCachesError(t *testing.T) {
	var printed []string
	installer, err := NewFileInstaller(newTestServer(t), FileInstallerCfg{
		InstallerCfg: InstallerCfg{
			ServiceName: "failed-svc",
			Printer: func(format string, args ...any) {
				printed = append(printed, fmt.Sprintf(format, args...))
			},
		},
		EnvFile:   true,
		StageOnly: true,
	})
	if err != nil {
		t.Fatalf("NewFileInstaller returned error: %v", err)
	}
	tmpDir := installer.tmpDir
	installer.Fail()

	err = installer.Close()
	if err == nil || !strings.Contains(err.Error(), "installation failed") {
		t.Fatalf("Close error = %v, want installation failed", err)
	}
	if err := installer.Wait(); err != nil {
		t.Fatalf("Wait returned error: %v", err)
	}
	if _, err := os.Stat(tmpDir); !os.IsNotExist(err) {
		t.Fatalf("temp dir still exists after failed Close: %v", err)
	}
	if len(printed) != 1 || !strings.Contains(printed[0], "Installation of \"failed-svc\" failed") {
		t.Fatalf("printed messages = %#v, want installation failure", printed)
	}
	err = installer.Close()
	if err == nil || !strings.Contains(err.Error(), "installation failed") {
		t.Fatalf("second Close error = %v, want cached installation failed", err)
	}
}

func TestInstallerCloseReturnsTempFileCloseError(t *testing.T) {
	installer, err := NewFileInstaller(newTestServer(t), FileInstallerCfg{
		InstallerCfg: InstallerCfg{ServiceName: "close-error-svc"},
		EnvFile:      true,
		StageOnly:    true,
	})
	if err != nil {
		t.Fatalf("NewFileInstaller returned error: %v", err)
	}
	tmpDir := installer.tmpDir
	if err := installer.File.Close(); err != nil {
		t.Fatalf("manual temp file close returned error: %v", err)
	}

	err = installer.Close()
	if err == nil || !strings.Contains(err.Error(), "failed to close temporary file") {
		t.Fatalf("Close error = %v, want temporary file close error", err)
	}
	if _, err := os.Stat(tmpDir); !os.IsNotExist(err) {
		t.Fatalf("temp dir still exists after close error: %v", err)
	}
	err = installer.Close()
	if err == nil || !strings.Contains(err.Error(), "failed to close temporary file") {
		t.Fatalf("second Close error = %v, want cached close error", err)
	}
}

func TestInstallerCloseWrapsInvalidPayloadInstallError(t *testing.T) {
	var printed []string
	installer, err := NewFileInstaller(newTestServer(t), FileInstallerCfg{
		InstallerCfg: InstallerCfg{
			ServiceName: "invalid-payload-svc",
			Printer: func(format string, args ...any) {
				printed = append(printed, fmt.Sprintf(format, args...))
			},
		},
		StageOnly: true,
	})
	if err != nil {
		t.Fatalf("NewFileInstaller returned error: %v", err)
	}
	if _, err := installer.Write([]byte("plain text without a known payload type")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}

	err = installer.Close()
	if err == nil || !strings.Contains(err.Error(), "failed to install service") {
		t.Fatalf("Close error = %v, want wrapped install error", err)
	}
	if !strings.Contains(err.Error(), "unable to detect file type") {
		t.Fatalf("Close error = %v, want payload detection failure", err)
	}
	if len(printed) != 1 || !strings.Contains(printed[0], "Failed to install service") {
		t.Fatalf("printed messages = %#v, want install failure", printed)
	}
}

func TestInstallerCloseStagesGeneratedPythonComposeWithNetworkArtifacts(t *testing.T) {
	t.Setenv("DEFAULT_NS", "9.9.9.9")
	t.Setenv("DEFAULT_SEARCH_DOMAINS", "svc.local")

	server := newTestServer(t)
	installer, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{ServiceName: "py-svc"},
		Args:         []string{"--port", "8080"},
		Network: NetworkOpts{
			Interfaces: "svc",
		},
		StageOnly:   true,
		Publish:     []string{"8080:8080"},
		PayloadName: "/client/path/main.py",
	})
	if err != nil {
		t.Fatalf("NewFileInstaller returned error: %v", err)
	}
	if !strings.HasSuffix(installer.tempFilePath(), string(filepath.Separator)+"main.py") {
		t.Fatalf("temp file path = %q, want payload basename to be preserved", installer.tempFilePath())
	}
	if _, err := installer.Write([]byte("print('hello')\n")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}

	if err := installer.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	service := testService(t, server, "py-svc")
	if service.ServiceType != db.ServiceTypeDockerCompose {
		t.Fatalf("service type = %q, want docker compose", service.ServiceType)
	}
	if service.SvcNetwork == nil || service.SvcNetwork.IPv4.String() != "192.168.100.3" {
		t.Fatalf("service svc network = %#v, want first service IP", service.SvcNetwork)
	}
	pythonPath := stagedArtifactPath(t, service, db.ArtifactPythonFile)
	assertInstallerFileContent(t, pythonPath, "print('hello')\n")

	composePath := stagedArtifactPath(t, service, db.ArtifactDockerComposeFile)
	composeRaw, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) returned error: %v", composePath, err)
	}
	compose := string(composeRaw)
	for _, want := range []string{
		"ghcr.io/astral-sh/uv:python3.13-bookworm-slim",
		"- uv",
		"- run",
		"- /main.py",
		"- --port",
		"- \"8080\"",
		"- 8080:8080",
		fmt.Sprintf("%s:/data", server.serviceDataDir("py-svc")),
		fmt.Sprintf("%s:/main.py:ro", filepath.Join(server.serviceRunDir("py-svc"), "main.py")),
	} {
		if !strings.Contains(compose, want) {
			t.Fatalf("generated compose missing %q:\n%s", want, compose)
		}
	}

	resolvPath := stagedArtifactPath(t, service, db.ArtifactNetNSResolv)
	assertInstallerFileContent(t, resolvPath, "nameserver 9.9.9.9\nsearch svc.local\n")
	composeNetworkPath := stagedArtifactPath(t, service, db.ArtifactDockerComposeNetwork)
	composeNetworkRaw, err := os.ReadFile(composeNetworkPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) returned error: %v", composeNetworkPath, err)
	}
	for _, want := range []string{
		"driver: yeet",
		`dev.catchit.netns: "/var/run/netns/yeet-py-svc-ns"`,
	} {
		if !strings.Contains(string(composeNetworkRaw), want) {
			t.Fatalf("compose network missing %q:\n%s", want, string(composeNetworkRaw))
		}
	}
	for _, artifact := range []db.ArtifactName{db.ArtifactNetNSService, db.ArtifactNetNSEnv} {
		path := stagedArtifactPath(t, service, artifact)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("stat staged %s %q: %v", artifact, path, err)
		}
	}
}

func TestInstallerCloseStagesScriptPayloadWithSystemdUnit(t *testing.T) {
	server := newTestServer(t)
	installer, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{ServiceName: "script-svc"},
		Args:         []string{"--flag"},
		StageOnly:    true,
		PayloadName:  "run",
	})
	if err != nil {
		t.Fatalf("NewFileInstaller returned error: %v", err)
	}
	if _, err := installer.Write([]byte("#!/bin/sh\necho hi\n")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}

	if err := installer.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	service := testService(t, server, "script-svc")
	if service.ServiceType != db.ServiceTypeSystemd {
		t.Fatalf("service type = %q, want systemd", service.ServiceType)
	}
	binaryPath := stagedArtifactPath(t, service, db.ArtifactBinary)
	assertInstallerFileContent(t, binaryPath, "#!/bin/sh\necho hi\n")
	info, err := os.Stat(binaryPath)
	if err != nil {
		t.Fatalf("Stat(%q) returned error: %v", binaryPath, err)
	}
	if info.Mode().Perm() != 0755 {
		t.Fatalf("binary mode = %v, want 0755", info.Mode().Perm())
	}
	unitPath := stagedArtifactPath(t, service, db.ArtifactSystemdUnit)
	unitRaw, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) returned error: %v", unitPath, err)
	}
	unit := string(unitRaw)
	for _, want := range []string{
		fmt.Sprintf("ExecStart=%s --flag\n", filepath.Join(server.serviceRunDir("script-svc"), "script-svc")),
		fmt.Sprintf("WorkingDirectory=%s\n", server.serviceDataDir("script-svc")),
		fmt.Sprintf("EnvironmentFile=-%s\n", filepath.Join(server.serviceRunDir("script-svc"), "env")),
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("systemd unit missing %q:\n%s", want, unit)
		}
	}
}

func TestInstallerCloseStagesDockerComposePayloadAndPublishPorts(t *testing.T) {
	server := newTestServer(t)
	installer, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{ServiceName: "compose-svc"},
		StageOnly:    true,
		Publish:      []string{"127.0.0.1:8080:80", "  "},
		PayloadName:  "compose.yml",
	})
	if err != nil {
		t.Fatalf("NewFileInstaller returned error: %v", err)
	}
	payload := "services:\n  compose-svc:\n    image: nginx:latest\n"
	if _, err := installer.Write([]byte(payload)); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}

	if err := installer.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	service := testService(t, server, "compose-svc")
	if service.ServiceType != db.ServiceTypeDockerCompose {
		t.Fatalf("service type = %q, want docker compose", service.ServiceType)
	}
	composePath := stagedArtifactPath(t, service, db.ArtifactDockerComposeFile)
	raw, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) returned error: %v", composePath, err)
	}
	got := string(raw)
	for _, want := range []string{
		"image: nginx:latest",
		"ports:",
		"- 127.0.0.1:8080:80",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("staged compose missing %q:\n%s", want, got)
		}
	}
}

func TestInstallerCloseNoBinaryRewritesExistingSystemdArtifact(t *testing.T) {
	server := newTestServer(t)
	oldUnit := filepath.Join(server.serviceBinDir("nobin-svc"), "nobin-svc-old.service")
	if err := os.MkdirAll(filepath.Dir(oldUnit), 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(oldUnit, []byte("[Service]\nExecStart=/old/bin --old\n"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	addTestServices(t, server, db.Service{
		Name:        "nobin-svc",
		ServiceType: db.ServiceTypeSystemd,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{"staged": oldUnit}},
		},
	})
	installer, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{ServiceName: "nobin-svc"},
		Args:         []string{"--new"},
		NoBinary:     true,
		StageOnly:    true,
	})
	if err != nil {
		t.Fatalf("NewFileInstaller returned error: %v", err)
	}
	tmpPath := installer.tempFilePath()

	if err := installer.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("no-binary temp payload still exists: %v", err)
	}
	service := testService(t, server, "nobin-svc")
	unitPath := stagedArtifactPath(t, service, db.ArtifactSystemdUnit)
	if unitPath == oldUnit {
		t.Fatalf("rewritten systemd unit reused old path %q", unitPath)
	}
	unitRaw, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) returned error: %v", unitPath, err)
	}
	wantExec := fmt.Sprintf("ExecStart=%s --new\n", filepath.Join(server.serviceRunDir("nobin-svc"), "nobin-svc"))
	if !strings.Contains(string(unitRaw), wantExec) {
		t.Fatalf("rewritten systemd unit missing %q:\n%s", wantExec, string(unitRaw))
	}
}

func TestPayloadDetectionDecompressesZstdPayload(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "main.py")
	compressed := filepath.Join(dir, "payload.py")
	if err := os.WriteFile(src, []byte("print('zstd')\n"), 0644); err != nil {
		t.Fatalf("WriteFile source returned error: %v", err)
	}
	if err := codecutil.ZstdCompress(src, compressed); err != nil {
		t.Fatalf("ZstdCompress returned error: %v", err)
	}

	got, err := detectInstallPayloadType(compressed)
	if err != nil {
		t.Fatalf("detectInstallPayloadType returned error: %v", err)
	}
	if got != ftdetect.Python {
		t.Fatalf("detected type = %v, want Python", got)
	}
	raw, err := os.ReadFile(compressed)
	if err != nil {
		t.Fatalf("ReadFile decompressed payload returned error: %v", err)
	}
	if string(raw) != "print('zstd')\n" {
		t.Fatalf("decompressed payload = %q, want original source", string(raw))
	}
	if _, err := os.Stat(compressed + ".unpack"); !os.IsNotExist(err) {
		t.Fatalf("temporary unpack file still exists: %v", err)
	}
}

func TestPayloadDetectionCleansInvalidZstdUnpackFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "payload.py")
	if err := os.WriteFile(path, []byte{0x28, 0xb5, 0x2f, 0xfd, 0x00}, 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	_, err := detectInstallPayloadType(path)
	if err == nil || !strings.Contains(err.Error(), "failed to decompress file") {
		t.Fatalf("detectInstallPayloadType error = %v, want decompress failure", err)
	}
	if _, err := os.Stat(path + ".unpack"); !os.IsNotExist(err) {
		t.Fatalf("temporary unpack file still exists: %v", err)
	}
}

func TestInstallerNetworkPlanningCoversTailscaleTapAndMacvlanModes(t *testing.T) {
	oldHostDefaultRouteInterfaceFn := hostDefaultRouteInterfaceFn
	defer func() {
		hostDefaultRouteInterfaceFn = oldHostDefaultRouteInterfaceFn
	}()
	hostDefaultRouteInterfaceFn = func() (string, error) {
		return "vmbr0", nil
	}

	tapInstaller := &FileInstaller{
		s: newTestServer(t),
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{ServiceName: "ts-only"},
			Network: NetworkOpts{
				Interfaces: "ts",
				Tailscale:  TailscaleOpts{AuthKey: "tskey-auth"},
			},
		},
	}
	tapEnv, tapRun, tapMode, err := tapInstaller.prepareNetworkConfig()
	if err != nil {
		t.Fatalf("prepareNetworkConfig tap mode returned error: %v", err)
	}
	if !tapMode || tapRun != "" {
		t.Fatalf("tap mode = %v runTSInNetNS = %q, want tap mode with host tailscale", tapMode, tapRun)
	}
	if tapEnv.TailscaleTAPInterface == "" || !strings.HasPrefix(tapEnv.TailscaleTAPInterface, "yts-") {
		t.Fatalf("tap env tailscale interface = %q, want generated yts-*", tapEnv.TailscaleTAPInterface)
	}

	server := newTestServer(t)
	combinedInstaller := &FileInstaller{
		s: server,
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{ServiceName: "ts-netns"},
			Network: NetworkOpts{
				Interfaces: "ts,svc,lan",
				Macvlan: MacvlanOpts{
					Parent: "eno1",
					VLAN:   7,
					Mac:    "02:00:00:00:00:07",
				},
			},
		},
	}
	env, runTSInNetNS, tapMode, err := combinedInstaller.prepareNetworkConfig()
	if err != nil {
		t.Fatalf("prepareNetworkConfig combined mode returned error: %v", err)
	}
	if tapMode {
		t.Fatal("combined network unexpectedly selected tailscale TAP mode")
	}
	if runTSInNetNS != env.NetNS() {
		t.Fatalf("runTSInNetNS = %q, want %q", runTSInNetNS, env.NetNS())
	}
	if got := env.ServiceIP.Addr().String(); got != "192.168.100.3" {
		t.Fatalf("service IP = %q, want 192.168.100.3", got)
	}
	if env.MacvlanParent != "eno1" || env.MacvlanVLAN != "7" || env.MacvlanMac != "02:00:00:00:00:07" {
		t.Fatalf("macvlan env = parent %q vlan %q mac %q", env.MacvlanParent, env.MacvlanVLAN, env.MacvlanMac)
	}
}

func TestInstallerNewFileInstallerRejectsReservedNameAndNilWrites(t *testing.T) {
	if _, err := NewFileInstaller(newTestServer(t), FileInstallerCfg{
		InstallerCfg: InstallerCfg{ServiceName: string(db.ArtifactTSBinary)},
	}); err == nil {
		t.Fatal("NewFileInstaller returned nil error for reserved service name")
	}

	installer := &FileInstaller{}
	if _, err := installer.Write([]byte("payload")); err == nil || !strings.Contains(err.Error(), "no temporary file") {
		t.Fatalf("Write error = %v, want no temporary file", err)
	}
	if _, err := installer.WriteAt([]byte("payload"), 0); err == nil || !strings.Contains(err.Error(), "no temporary file") {
		t.Fatalf("WriteAt error = %v, want no temporary file", err)
	}
	if err := installer.Close(); err == nil || !strings.Contains(err.Error(), "no temporary file") {
		t.Fatalf("Close error = %v, want no temporary file", err)
	}
}

func TestInstallerInstallPreparedFileCleanupAndPostActionError(t *testing.T) {
	installer := &FileInstaller{}
	dir := t.TempDir()
	cleanupPath := filepath.Join(dir, "cleanup.tmp")
	if err := os.WriteFile(cleanupPath, []byte("discard"), 0644); err != nil {
		t.Fatalf("WriteFile cleanup payload returned error: %v", err)
	}

	if err := installer.installPreparedFile(cleanupPath, fileInstallPlan{}); err != nil {
		t.Fatalf("installPreparedFile cleanup returned error: %v", err)
	}
	if _, err := os.Stat(cleanupPath); !os.IsNotExist(err) {
		t.Fatalf("cleanup temp file still exists: %v", err)
	}

	src := filepath.Join(dir, "payload")
	dst := filepath.Join(dir, "payload.dst")
	if err := os.WriteFile(src, []byte("payload"), 0644); err != nil {
		t.Fatalf("WriteFile source returned error: %v", err)
	}
	err := installer.installPreparedFile(src, fileInstallPlan{
		dst: dst,
		postRenameActions: []func() error{
			func() error { return fmt.Errorf("post action failed") },
		},
	})
	if err == nil || !strings.Contains(err.Error(), "failed to run post-action") {
		t.Fatalf("installPreparedFile error = %v, want post-action failure", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("renamed payload missing after post-action failure: %v", err)
	}
}

func TestComposeRenderingTypeScriptIncludesArgsPublishAndVolumes(t *testing.T) {
	got, err := typescriptComposeFile("ts-svc", "/run/ts-svc", "/data/ts-svc", []string{"--serve"}, []string{" 3000:3000 ", ""})
	if err != nil {
		t.Fatalf("typescriptComposeFile returned error: %v", err)
	}
	for _, want := range []string{
		"denoland/deno:2.0.0-rc.2",
		"- deno",
		"- run",
		"- --allow-net",
		"- /main.ts",
		"- --serve",
		"- 3000:3000",
		"/data/ts-svc:/data",
		"/run/ts-svc/main.ts:/main.ts:ro",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("typescript compose missing %q:\n%s", want, got)
		}
	}
}

func TestComposeGeneratedFileReportsRenderError(t *testing.T) {
	installer := &FileInstaller{
		s:   newTestServer(t),
		cfg: FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: "render-svc"}},
	}
	_, err := installer.writeGeneratedComposeFile(t.TempDir(), "custom", func(string, string, string, []string, []string) (string, error) {
		return "", fmt.Errorf("render boom")
	})
	if err == nil || !strings.Contains(err.Error(), "failed to render custom compose file") {
		t.Fatalf("writeGeneratedComposeFile error = %v, want render failure", err)
	}
}

func TestValidatePullPayloadType(t *testing.T) {
	for _, ft := range []ftdetect.FileType{ftdetect.DockerCompose, ftdetect.Python, ftdetect.TypeScript} {
		if err := validatePullPayloadType(true, ft); err != nil {
			t.Fatalf("validatePullPayloadType(true, %v) returned error: %v", ft, err)
		}
	}
	if err := validatePullPayloadType(true, ftdetect.Binary); err == nil {
		t.Fatal("validatePullPayloadType(true, Binary) returned nil, want error")
	}
	if err := validatePullPayloadType(false, ftdetect.Binary); err != nil {
		t.Fatalf("validatePullPayloadType(false, Binary) returned error: %v", err)
	}
}

func TestApplyInstallServiceType(t *testing.T) {
	tests := []struct {
		name    string
		current db.ServiceType
		plan    fileInstallPlan
		want    db.ServiceType
		wantErr bool
	}{
		{
			name: "sets empty service type",
			plan: fileInstallPlan{
				detectedServiceType: db.ServiceTypeSystemd,
			},
			want: db.ServiceTypeSystemd,
		},
		{
			name:    "keeps matching service type",
			current: db.ServiceTypeDockerCompose,
			plan: fileInstallPlan{
				detectedServiceType: db.ServiceTypeDockerCompose,
			},
			want: db.ServiceTypeDockerCompose,
		},
		{
			name:    "ignores empty detected service type",
			current: db.ServiceTypeSystemd,
			want:    db.ServiceTypeSystemd,
		},
		{
			name:    "allows systemd to generated compose upgrade",
			current: db.ServiceTypeSystemd,
			plan: fileInstallPlan{
				detectedServiceType:     db.ServiceTypeDockerCompose,
				allowServiceTypeUpgrade: true,
			},
			want: db.ServiceTypeDockerCompose,
		},
		{
			name:    "rejects mismatched service type",
			current: db.ServiceTypeDockerCompose,
			plan: fileInstallPlan{
				detectedServiceType: db.ServiceTypeSystemd,
			},
			want:    db.ServiceTypeDockerCompose,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &db.Service{ServiceType: tt.current}
			err := applyInstallServiceType(service, tt.plan)
			if tt.wantErr {
				if err == nil {
					t.Fatal("applyInstallServiceType returned nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("applyInstallServiceType returned error: %v", err)
			}
			if service.ServiceType != tt.want {
				t.Fatalf("service type = %q, want %q", service.ServiceType, tt.want)
			}
		})
	}
}

func TestEnsureSystemdUnitRegeneratesCatchUnitWithDockerOrdering(t *testing.T) {
	server := newTestServer(t)
	oldUnit := filepath.Join(server.serviceBinDir(CatchService), "catch-old.service")
	if err := os.MkdirAll(filepath.Dir(oldUnit), 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(oldUnit, []byte("[Unit]\n\n[Service]\nExecStart=/old/catch\n\n[Install]\nWantedBy=multi-user.target\n"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	addTestServices(t, server, db.Service{
		Name:        CatchService,
		ServiceType: db.ServiceTypeSystemd,
		Generation:  1,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{"staged": oldUnit}},
		},
	})

	installer, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{ServiceName: CatchService},
		Args:         []string{"--data-dir=/root/data", "--tsnet-host=catch"},
	})
	if err != nil {
		t.Fatalf("NewFileInstaller returned error: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Remove(installer.tempFilePath())
	})

	if err := installer.ensureSystemdUnit(); err != nil {
		t.Fatalf("ensureSystemdUnit returned error: %v", err)
	}
	gotPath := installer.artifacts[db.ArtifactSystemdUnit]
	if gotPath == "" {
		t.Fatal("catch systemd unit was not staged")
	}
	if gotPath == oldUnit {
		t.Fatalf("catch systemd unit reused old staged path %q", gotPath)
	}
	raw, err := os.ReadFile(gotPath)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	got := string(raw)
	for _, want := range []string{
		"Wants=containerd.service\n",
		"After=containerd.service\n",
		"Before=yeet-docker-prereqs.target docker.service\n",
		"ExecStartPost=/bin/sh -c 'i=0; while [ \"$i\" -lt 600 ]; do [ -S /run/docker/plugins/yeet.sock ] && exit 0; i=$((i+1)); sleep 0.1; done; exit 1'\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("regenerated catch unit missing %q:\n%s", want, got)
		}
	}
}

func TestHostDefaultRouteInterfaceFromProcRouteReportsErrors(t *testing.T) {
	routeTable := strings.Join([]string{
		"Iface\tDestination\tGateway",
		"malformed",
		"eth0\t000011AC\t00000000",
	}, "\n")
	if _, err := hostDefaultRouteInterfaceFromProcRoute(strings.NewReader(routeTable)); err == nil || !strings.Contains(err.Error(), "default route interface not found") {
		t.Fatalf("hostDefaultRouteInterfaceFromProcRoute error = %v, want default route not found", err)
	}

	if _, err := hostDefaultRouteInterfaceFromProcRoute(errorReader{}); err == nil || !strings.Contains(err.Error(), "read failed") {
		t.Fatalf("hostDefaultRouteInterfaceFromProcRoute error = %v, want reader error", err)
	}
}

func TestParseNetworkReportsUnsupportedAndAllocationErrors(t *testing.T) {
	t.Run("unknown interface", func(t *testing.T) {
		installer := &FileInstaller{
			s: newTestServer(t),
			cfg: FileInstallerCfg{
				InstallerCfg: InstallerCfg{ServiceName: "bad-net"},
				Network:      NetworkOpts{Interfaces: "bad"},
			},
		}
		if err := installer.parseNetwork(); err == nil || !strings.Contains(err.Error(), `unknown network: "bad"`) {
			t.Fatalf("parseNetwork error = %v, want unknown network", err)
		}
	})

	t.Run("default route error", func(t *testing.T) {
		oldHostDefaultRouteInterfaceFn := hostDefaultRouteInterfaceFn
		hostDefaultRouteInterfaceFn = func() (string, error) {
			return "", fmt.Errorf("route lookup failed")
		}
		t.Cleanup(func() {
			hostDefaultRouteInterfaceFn = oldHostDefaultRouteInterfaceFn
		})

		installer := &FileInstaller{
			s: newTestServer(t),
			cfg: FileInstallerCfg{
				InstallerCfg: InstallerCfg{ServiceName: "lan-net"},
				Network:      NetworkOpts{Interfaces: "lan"},
			},
		}
		if err := installer.parseNetwork(); err == nil || !strings.Contains(err.Error(), "failed to get default route interface") {
			t.Fatalf("parseNetwork error = %v, want default route failure", err)
		}
	})

}

func TestConfigureNetworkAndStageInstallSurfaceErrors(t *testing.T) {
	installer := &FileInstaller{
		s: newTestServer(t),
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{ServiceName: "missing-dirs"},
			Network:      NetworkOpts{Interfaces: "svc"},
		},
	}
	if _, err := installer.configureNetwork(); err == nil || !strings.Contains(err.Error(), "failed to write resolv.conf") {
		t.Fatalf("configureNetwork error = %v, want resolv.conf write failure", err)
	}

	installer = &FileInstaller{
		s: newTestServer(t),
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{ServiceName: "bad-stage-net"},
			Network:      NetworkOpts{Interfaces: "bad"},
		},
	}
	if err := installer.configureAndStageInstall(fileInstallPlan{}); err == nil || !strings.Contains(err.Error(), "failed to configure network") {
		t.Fatalf("configureAndStageInstall error = %v, want wrapped network failure", err)
	}
}

func TestNewSystemdUnitAppliesNetworkNamespaceFields(t *testing.T) {
	server := newTestServer(t)
	if err := server.ensureDirs("net-systemd", ""); err != nil {
		t.Fatalf("ensureDirs returned error: %v", err)
	}
	installer := &FileInstaller{
		s: server,
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{ServiceName: "net-systemd"},
			Network:      NetworkOpts{Interfaces: "svc"},
		},
	}

	unit, err := installer.newSystemdUnit(filepath.Join(server.serviceRunDir("net-systemd"), "net-systemd"))
	if err != nil {
		t.Fatalf("newSystemdUnit returned error: %v", err)
	}
	if unit.NetNS != "yeet-net-systemd-ns" {
		t.Fatalf("unit NetNS = %q, want yeet-net-systemd-ns", unit.NetNS)
	}
	if unit.Requires != "yeet-net-systemd-ns.service" {
		t.Fatalf("unit Requires = %q, want netns service dependency", unit.Requires)
	}
	if unit.ResolvConf != "/etc/netns/yeet-net-systemd-ns/resolv.conf" {
		t.Fatalf("unit ResolvConf = %q, want netns resolv.conf", unit.ResolvConf)
	}
}

func TestPrepareNoBinaryInstallVariants(t *testing.T) {
	emptyInstaller := &FileInstaller{}
	plan, err := emptyInstaller.prepareNoBinaryInstall()
	if err != nil {
		t.Fatalf("prepareNoBinaryInstall with no existing service returned error: %v", err)
	}
	if plan.dst != "" || plan.detectedServiceType != "" || plan.allowServiceTypeUpgrade || len(plan.postRenameActions) != 0 {
		t.Fatalf("plan = %#v, want empty plan", plan)
	}

	server := newTestServer(t)
	addTestServices(t, server,
		db.Service{Name: "compose-existing", ServiceType: db.ServiceTypeDockerCompose},
		db.Service{Name: "systemd-existing", ServiceType: db.ServiceTypeSystemd},
	)

	composeInstaller := &FileInstaller{
		s:               server,
		cfg:             FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: "compose-existing"}},
		existingService: First(server.serviceView("compose-existing")),
	}
	plan, err = composeInstaller.prepareNoBinaryInstall()
	if err != nil {
		t.Fatalf("prepareNoBinaryInstall with compose service returned error: %v", err)
	}
	if plan.detectedServiceType != db.ServiceTypeDockerCompose || plan.dst != "" {
		t.Fatalf("compose plan = %#v, want compose type with no file action", plan)
	}

	systemdInstaller := &FileInstaller{
		s: server,
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{ServiceName: "systemd-existing"},
			StageOnly:    true,
		},
		existingService: First(server.serviceView("systemd-existing")),
	}
	plan, err = systemdInstaller.prepareNoBinaryInstall()
	if err != nil {
		t.Fatalf("prepareNoBinaryInstall with systemd service returned error: %v", err)
	}
	if plan.detectedServiceType != db.ServiceTypeSystemd || plan.dst != "" {
		t.Fatalf("systemd plan = %#v, want systemd type with no file action", plan)
	}
}

func TestReuseExistingSystemdUnitBranches(t *testing.T) {
	server := newTestServer(t)
	addTestServices(t, server, db.Service{
		Name:        "reuse-unit",
		ServiceType: db.ServiceTypeSystemd,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{"staged": filepath.Join(t.TempDir(), "unit.service")}},
		},
	})
	installer := &FileInstaller{
		s:               server,
		cfg:             FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: "reuse-unit"}},
		existingService: First(server.serviceView("reuse-unit")),
	}
	reused, err := installer.reuseExistingSystemdUnit("/srv/reuse-unit")
	if err != nil {
		t.Fatalf("reuseExistingSystemdUnit returned error: %v", err)
	}
	if !reused {
		t.Fatal("reuseExistingSystemdUnit returned reused=false, want true")
	}
	if installer.artifacts != nil {
		t.Fatalf("artifacts = %#v, want no rewrite without args", installer.artifacts)
	}

	addTestServices(t, server, db.Service{
		Name:        "missing-unit",
		ServiceType: db.ServiceTypeSystemd,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{"staged": filepath.Join(t.TempDir(), "missing.service")}},
		},
	})
	installer = &FileInstaller{
		s: server,
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{ServiceName: "missing-unit"},
			Args:         []string{"--new"},
		},
		existingService: First(server.serviceView("missing-unit")),
	}
	reused, err = installer.reuseExistingSystemdUnit("/srv/missing-unit")
	if err == nil || !strings.Contains(err.Error(), "failed to rewrite systemd unit") {
		t.Fatalf("reuseExistingSystemdUnit error = %v, want rewrite failure", err)
	}
	if reused {
		t.Fatal("reuseExistingSystemdUnit returned reused=true on rewrite failure")
	}
}

func TestPreparePayloadErrorBranches(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "run")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho hi\n"), 0644); err != nil {
		t.Fatalf("WriteFile script returned error: %v", err)
	}
	installer := &FileInstaller{
		cfg: FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: "pull-script", Pull: true}},
	}
	if _, err := installer.preparePayloadInstall(script); err == nil || !strings.Contains(err.Error(), "--pull is only valid") {
		t.Fatalf("preparePayloadInstall error = %v, want pull validation failure", err)
	}

	if _, err := installer.preparePayloadByType("unused", ftdetect.Unknown); err == nil || !strings.Contains(err.Error(), "unknown file type") {
		t.Fatalf("preparePayloadByType error = %v, want unknown file type", err)
	}

	compose := filepath.Join(dir, "compose.yml")
	if err := os.WriteFile(compose, []byte("services: []\n"), 0644); err != nil {
		t.Fatalf("WriteFile compose returned error: %v", err)
	}
	installer = &FileInstaller{
		s: newTestServer(t),
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{ServiceName: "compose-bad-publish"},
			Publish:      []string{"8080:80"},
		},
	}
	if _, err := installer.prepareDockerComposePayload(compose); err == nil || !strings.Contains(err.Error(), "failed to apply publish ports") {
		t.Fatalf("prepareDockerComposePayload error = %v, want publish ports failure", err)
	}
}

func TestFileActionErrorBranches(t *testing.T) {
	logs := captureLogs(t)
	closeAndLog(errorCloser{}, "closer")
	if !strings.Contains(logs.String(), "failed to close closer") {
		t.Fatalf("logs = %q, want close failure", logs.String())
	}

	dir := t.TempDir()
	nonEmptyDir := filepath.Join(dir, "non-empty")
	if err := os.Mkdir(nonEmptyDir, 0755); err != nil {
		t.Fatalf("Mkdir returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nonEmptyDir, "child"), []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile child returned error: %v", err)
	}
	removeFileIfExists(nonEmptyDir)
	if !strings.Contains(logs.String(), "failed to remove") {
		t.Fatalf("logs = %q, want remove failure", logs.String())
	}

	installer := &FileInstaller{}
	if err := installer.installPreparedFile(filepath.Join(dir, "missing"), fileInstallPlan{dst: filepath.Join(dir, "dst")}); err == nil || !strings.Contains(err.Error(), "failed to move file in place") {
		t.Fatalf("installPreparedFile error = %v, want rename failure", err)
	}

	binDirFile := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(binDirFile, []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile binDirFile returned error: %v", err)
	}
	installer = &FileInstaller{
		s:   newTestServer(t),
		cfg: FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: "compose-write-error"}},
	}
	if _, err := installer.writeGeneratedComposeFile(binDirFile, "python", pythonComposeFile); err == nil || !strings.Contains(err.Error(), "failed to write python compose file") {
		t.Fatalf("writeGeneratedComposeFile error = %v, want write failure", err)
	}

	if err := chmodExecutableAction(filepath.Join(dir, "missing-executable"))(); err == nil || !strings.Contains(err.Error(), "failed to make binary executable") {
		t.Fatalf("chmodExecutableAction error = %v, want chmod failure", err)
	}
}

func TestStageInstallPlanMismatchAndNetworkApplication(t *testing.T) {
	server := newTestServer(t)
	addTestServices(t, server, db.Service{
		Name:        "mismatch",
		ServiceType: db.ServiceTypeDockerCompose,
	})
	installer := &FileInstaller{
		s:   server,
		cfg: FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: "mismatch"}},
	}
	if err := installer.stageInstallPlan(fileInstallPlan{detectedServiceType: db.ServiceTypeSystemd}); err == nil || !strings.Contains(err.Error(), "failed to update service") {
		t.Fatalf("stageInstallPlan error = %v, want service update failure", err)
	}

	service := &db.Service{}
	applyInstallNetworks(
		service,
		&db.MacvlanNetwork{Interface: "ymv-test", Parent: "eno1"},
		nil,
		&db.TailscaleNetwork{Interface: "yts-test", Version: "1.77.33"},
	)
	if service.Macvlan == nil || service.Macvlan.Interface != "ymv-test" {
		t.Fatalf("service macvlan = %#v, want applied macvlan", service.Macvlan)
	}
	if service.TSNet == nil || service.TSNet.Interface != "yts-test" {
		t.Fatalf("service tailscale = %#v, want applied tailscale", service.TSNet)
	}
}

func TestTempFilePathInitAndCleanupBranches(t *testing.T) {
	server := newTestServer(t)
	installer := &FileInstaller{
		s:   server,
		cfg: FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: "fallback-temp"}},
	}
	path := installer.tempFilePath()
	if !strings.HasPrefix(path, server.serviceBinDir("fallback-temp")+string(filepath.Separator)) || !strings.HasSuffix(path, ".tmp") {
		t.Fatalf("tempFilePath = %q, want service bin temp path", path)
	}
	installer.cleanupTemp()

	existingPath := filepath.Join(t.TempDir(), "existing.tmp")
	installer = &FileInstaller{tmpPath: existingPath}
	if err := installer.initTempFile(); err != nil {
		t.Fatalf("initTempFile with existing tmpPath returned error: %v", err)
	}
	if installer.tmpPath != existingPath {
		t.Fatalf("tmpPath = %q, want preserved %q", installer.tmpPath, existingPath)
	}

	badServer := newTestServer(t)
	if err := os.MkdirAll(badServer.serviceRootDir("bad-temp"), 0755); err != nil {
		t.Fatalf("MkdirAll service root returned error: %v", err)
	}
	if err := os.WriteFile(badServer.serviceBinDir("bad-temp"), []byte("not a dir"), 0644); err != nil {
		t.Fatalf("WriteFile service bin returned error: %v", err)
	}
	installer = &FileInstaller{
		s:   badServer,
		cfg: FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: "bad-temp"}},
	}
	if err := installer.initTempFile(); err == nil || !strings.Contains(err.Error(), "failed to create temp dir") {
		t.Fatalf("initTempFile error = %v, want temp dir failure", err)
	}
}

func TestNewFileInstallerReportsEnsureDirsError(t *testing.T) {
	server := newTestServer(t)
	servicesRoot := filepath.Join(t.TempDir(), "services")
	if err := os.WriteFile(servicesRoot, []byte("not a directory"), 0644); err != nil {
		t.Fatalf("WriteFile services root returned error: %v", err)
	}
	server.cfg.ServicesRoot = servicesRoot

	_, err := NewFileInstaller(server, FileInstallerCfg{
		InstallerCfg: InstallerCfg{ServiceName: "dir-error"},
	})
	if err == nil || !strings.Contains(err.Error(), "failed to ensure directories") {
		t.Fatalf("NewFileInstaller error = %v, want ensure dirs failure", err)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, fmt.Errorf("read failed")
}

type errorCloser struct{}

func (errorCloser) Close() error {
	return fmt.Errorf("close failed")
}

func netipMustParseAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	addr, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("ParseAddr(%q): %v", s, err)
	}
	return addr
}

func testService(t *testing.T, server *Server, name string) *db.Service {
	t.Helper()
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("DB.Get returned error: %v", err)
	}
	service := dv.AsStruct().Services[name]
	if service == nil {
		t.Fatalf("service %q not found", name)
	}
	return service
}

func stagedArtifactPath(t *testing.T, service *db.Service, name db.ArtifactName) string {
	t.Helper()
	artifact := service.Artifacts[name]
	if artifact == nil {
		t.Fatalf("artifact %s not staged; artifacts = %#v", name, service.Artifacts)
	}
	path := artifact.Refs[db.ArtifactRef("staged")]
	if path == "" {
		t.Fatalf("artifact %s has no staged ref; refs = %#v", name, artifact.Refs)
	}
	return path
}

func assertInstallerFileContent(t *testing.T, path, want string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) returned error: %v", path, err)
	}
	if string(raw) != want {
		t.Fatalf("file %q content = %q, want %q", path, string(raw), want)
	}
}
