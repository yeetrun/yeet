// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func netipMustParseAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	addr, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("ParseAddr(%q): %v", s, err)
	}
	return addr
}
