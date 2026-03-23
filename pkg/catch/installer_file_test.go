// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"strings"
	"testing"
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
