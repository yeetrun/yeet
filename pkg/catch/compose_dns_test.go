// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

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

func TestComposeDNSServicesDetectsMergedCustomResolver(t *testing.T) {
	raw := []byte(`
x-common: &common
  dns:
    - 1.1.1.1
services:
  api:
    <<: *common
    image: nginx
  worker:
    <<:
      - dns_search:
          - lan
    image: busybox
  web:
    image: caddy
`)
	services, err := composeDNSServices(raw)
	if err != nil {
		t.Fatalf("composeDNSServices: %v", err)
	}
	want := []composeDNSService{
		{Name: "api", CustomResolver: true},
		{Name: "worker", CustomResolver: true},
		{Name: "web"},
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

func netipPrefixForTest(t *testing.T, value string) netip.Prefix {
	t.Helper()
	prefix, err := netip.ParsePrefix(value)
	if err != nil {
		t.Fatalf("ParsePrefix(%q): %v", value, err)
	}
	return prefix
}
