// Copyright (c) 2026 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"fmt"
	"net/netip"
	"strings"
)

type vmLANLink struct {
	Name        string
	Kind        string
	OperState   string
	Master      string
	HasHardware bool
}

type vmLANRoute struct {
	Default bool
	Iface   string
	Gateway string
	Metric  int
}

type vmLANAddress struct {
	Iface  string
	Prefix string
	Scope  string
}

type vmLANRenderer struct {
	Name      string
	Supported bool
	Reason    string
}

type vmLANBridgePlan struct {
	Ready        bool
	NeedsPrepare bool
	Bridge       string
	Parent       string
	Renderer     vmLANRenderer
	Reason       string
}

type fakeVMLANHostState struct {
	links    []vmLANLink
	routes   []vmLANRoute
	addrs    []vmLANAddress
	renderer vmLANRenderer
}

func planHostLANBridge(state fakeVMLANHostState) (vmLANBridgePlan, error) {
	defaultRoute, ok := chooseDefaultIPv4Route(state.routes)
	if !ok {
		return vmLANBridgePlan{}, fmt.Errorf("no default IPv4 route found for VM LAN bridge planning")
	}
	if hasAmbiguousDefaultIPv4Route(state.routes, defaultRoute.Metric) {
		return vmLANBridgePlan{}, fmt.Errorf("ambiguous default IPv4 routes for VM LAN bridge planning")
	}
	links := indexVMLANLinks(state.links)
	link, ok := links[defaultRoute.Iface]
	if !ok {
		return vmLANBridgePlan{}, fmt.Errorf("default route interface %q was not found", defaultRoute.Iface)
	}
	if plan, ok := existingVMLANBridgePlan(link, links, state.renderer); ok {
		return plan, nil
	}
	if !isSupportedVMLANUplink(link, state.addrs) {
		return vmLANBridgePlan{}, fmt.Errorf("no supported LAN uplink found for VM LAN bridge planning; default route uses %q", link.Name)
	}
	if !state.renderer.Supported {
		return vmLANBridgePlan{}, fmt.Errorf("VM LAN bridge preparation is not supported for %s: %s", state.renderer.Name, state.renderer.Reason)
	}
	if br0, ok := links["br0"]; ok {
		if !canPrepareExistingEmptyVMLANBridge(br0, link.Name, links, state.addrs) {
			return vmLANBridgePlan{}, fmt.Errorf("br0 already exists but is not the LAN bridge for %s", link.Name)
		}
	}
	return vmLANBridgePlan{
		Ready:        false,
		NeedsPrepare: true,
		Bridge:       "br0",
		Parent:       link.Name,
		Renderer:     state.renderer,
		Reason:       "default route is on a supported physical LAN uplink",
	}, nil
}

func existingVMLANBridgePlan(link vmLANLink, links map[string]vmLANLink, renderer vmLANRenderer) (vmLANBridgePlan, bool) {
	if link.Kind == "bridge" {
		return vmLANBridgePlan{Ready: true, Bridge: link.Name, Renderer: renderer, Reason: "default route is already on a bridge"}, true
	}
	if link.Master == "" {
		return vmLANBridgePlan{}, false
	}
	master, ok := links[link.Master]
	if !ok || master.Kind != "bridge" {
		return vmLANBridgePlan{}, false
	}
	return vmLANBridgePlan{Ready: true, Bridge: master.Name, Parent: link.Name, Renderer: renderer, Reason: "default route interface is attached to a bridge"}, true
}

func canPrepareExistingEmptyVMLANBridge(bridge vmLANLink, parent string, links map[string]vmLANLink, addrs []vmLANAddress) bool {
	if bridge.Name != "br0" || bridge.Kind != "bridge" {
		return false
	}
	if vmLANLinkHasGlobalAddress(bridge.Name, addrs) {
		return false
	}
	for _, link := range links {
		if link.Master == bridge.Name && link.Name != parent {
			return false
		}
	}
	return true
}

func chooseDefaultIPv4Route(routes []vmLANRoute) (vmLANRoute, bool) {
	var chosen vmLANRoute
	found := false
	for _, route := range routes {
		if !route.Default || strings.TrimSpace(route.Iface) == "" {
			continue
		}
		if !found || route.Metric < chosen.Metric {
			chosen = route
			found = true
		}
	}
	return chosen, found
}

func hasAmbiguousDefaultIPv4Route(routes []vmLANRoute, metric int) bool {
	seenIface := ""
	for _, route := range routes {
		if !route.Default || route.Metric != metric {
			continue
		}
		iface := strings.TrimSpace(route.Iface)
		if iface == "" {
			continue
		}
		if seenIface == "" {
			seenIface = iface
			continue
		}
		if iface != seenIface {
			return true
		}
	}
	return false
}

func indexVMLANLinks(links []vmLANLink) map[string]vmLANLink {
	out := make(map[string]vmLANLink, len(links))
	for _, link := range links {
		name := strings.TrimSpace(link.Name)
		if name == "" {
			continue
		}
		link.Name = name
		out[name] = link
	}
	return out
}

func isSupportedVMLANUplink(link vmLANLink, addrs []vmLANAddress) bool {
	if strings.TrimSpace(link.Name) == "" || !link.HasHardware {
		return false
	}
	if isRejectedVMLANLink(link.Name) || isRejectedVMLANLink(link.Kind) {
		return false
	}
	return vmLANLinkHasRFC1918Address(link.Name, addrs)
}

func vmLANLinkHasRFC1918Address(name string, addrs []vmLANAddress) bool {
	name = strings.TrimSpace(name)
	for _, addr := range addrs {
		if strings.TrimSpace(addr.Iface) != name {
			continue
		}
		if scope := strings.TrimSpace(addr.Scope); scope != "" && scope != "global" {
			continue
		}
		prefix, err := netip.ParsePrefix(strings.TrimSpace(addr.Prefix))
		if err != nil {
			continue
		}
		if prefix.Addr().Is4() && prefix.Addr().IsPrivate() {
			return true
		}
	}
	return false
}

func vmLANLinkHasGlobalAddress(name string, addrs []vmLANAddress) bool {
	name = strings.TrimSpace(name)
	for _, addr := range addrs {
		if strings.TrimSpace(addr.Iface) != name {
			continue
		}
		if scope := strings.TrimSpace(addr.Scope); scope != "" && scope != "global" {
			continue
		}
		if strings.TrimSpace(addr.Prefix) != "" {
			return true
		}
	}
	return false
}

func isRejectedVMLANLink(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	switch value {
	case "lo", "vlan", "bond":
		return true
	}
	if strings.Contains(value, ".") {
		return true
	}
	for _, prefix := range []string{
		"bond",
		"docker",
		"br-",
		"veth",
		"tap",
		"tun",
		"tailscale",
		"yvm-",
		"cni",
		"virbr",
		"wlan",
		"wl",
	} {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}
