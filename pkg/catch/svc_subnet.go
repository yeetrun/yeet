// Copyright (c) 2026 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os/exec"
	"runtime"
	"strings"

	"github.com/yeetrun/yeet/pkg/netns"
)

var checkSvcSubnetAvailableFn = checkSvcSubnetAvailable

func checkSvcSubnetAvailable() error {
	if runtime.GOOS != "linux" {
		return nil
	}
	return checkSvcSubnetAvailableWith(func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).Output()
	})
}

func checkSvcSubnetAvailableWith(run func(string, ...string) ([]byte, error)) error {
	required := netip.MustParsePrefix(netns.ServiceSubnetCIDR)
	addrRaw, err := run("ip", "-j", "addr", "show")
	if err != nil {
		return fmt.Errorf("inspect host addresses for required service subnet %s: %w", required, err)
	}
	addrConflict, err := firstSvcSubnetAddressConflict(required, addrRaw)
	if err != nil {
		return err
	}
	if addrConflict != nil {
		return fmt.Errorf("required service subnet %s conflicts with existing host address %s on %s; yeet v1 requires exclusive use of this subnet", required, addrConflict.Text, addrConflict.Interface)
	}

	routeRaw, err := run("ip", "-j", "route", "show", "table", "main")
	if err != nil {
		return fmt.Errorf("inspect host routes for required service subnet %s: %w", required, err)
	}
	routeConflict, err := firstSvcSubnetRouteConflict(required, routeRaw)
	if err != nil {
		return err
	}
	if routeConflict != nil {
		return fmt.Errorf("required service subnet %s conflicts with existing host route %s on %s; yeet v1 requires exclusive use of this subnet", required, routeConflict.Text, routeConflict.Interface)
	}
	return nil
}

type svcSubnetConflict struct {
	Prefix    netip.Prefix
	Text      string
	Interface string
}

type ipAddrJSON struct {
	IfName   string           `json:"ifname"`
	AddrInfo []ipAddrInfoJSON `json:"addr_info"`
}

type ipAddrInfoJSON struct {
	Family    string `json:"family"`
	Local     string `json:"local"`
	PrefixLen int    `json:"prefixlen"`
}

func firstSvcSubnetAddressConflict(required netip.Prefix, raw []byte) (*svcSubnetConflict, error) {
	var entries []ipAddrJSON
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("parse host addresses for required service subnet %s: %w", required, err)
	}
	for _, entry := range entries {
		if conflict := svcSubnetAddressConflictForEntry(required, entry); conflict != nil {
			return conflict, nil
		}
	}
	return nil, nil
}

func svcSubnetAddressConflictForEntry(required netip.Prefix, entry ipAddrJSON) *svcSubnetConflict {
	if isYeetSvcInterface(entry.IfName) {
		return nil
	}
	for _, addrInfo := range entry.AddrInfo {
		addr, prefix, ok := ipv4AddrInfoPrefix(addrInfo)
		if !ok || !prefixesOverlap(required, prefix) {
			continue
		}
		return &svcSubnetConflict{
			Prefix:    prefix,
			Text:      fmt.Sprintf("%s/%d", addr, addrInfo.PrefixLen),
			Interface: entry.IfName,
		}
	}
	return nil
}

func ipv4AddrInfoPrefix(addrInfo ipAddrInfoJSON) (netip.Addr, netip.Prefix, bool) {
	if addrInfo.Family != "inet" || addrInfo.PrefixLen < 0 || addrInfo.PrefixLen > 32 {
		return netip.Addr{}, netip.Prefix{}, false
	}
	addr, err := netip.ParseAddr(addrInfo.Local)
	if err != nil || !addr.Is4() {
		return netip.Addr{}, netip.Prefix{}, false
	}
	return addr, netip.PrefixFrom(addr, addrInfo.PrefixLen).Masked(), true
}

type ipRouteJSON struct {
	Dst string `json:"dst"`
	Dev string `json:"dev"`
}

func firstSvcSubnetRouteConflict(required netip.Prefix, raw []byte) (*svcSubnetConflict, error) {
	var entries []ipRouteJSON
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("parse host routes for required service subnet %s: %w", required, err)
	}
	for _, entry := range entries {
		if entry.Dst == "" || entry.Dst == "default" || isYeetSvcInterface(entry.Dev) {
			continue
		}
		prefix, ok := parseIPv4PrefixOrAddr(entry.Dst)
		if !ok {
			continue
		}
		if prefixesOverlap(required, prefix) {
			return &svcSubnetConflict{Prefix: prefix, Text: prefix.String(), Interface: entry.Dev}, nil
		}
	}
	return nil, nil
}

func parseIPv4PrefixOrAddr(value string) (netip.Prefix, bool) {
	prefix, err := netip.ParsePrefix(value)
	if err == nil && prefix.Addr().Is4() {
		return prefix.Masked(), true
	}
	addr, err := netip.ParseAddr(value)
	if err == nil && addr.Is4() {
		return netip.PrefixFrom(addr, addr.BitLen()), true
	}
	return netip.Prefix{}, false
}

func prefixesOverlap(a, b netip.Prefix) bool {
	a = a.Masked()
	b = b.Masked()
	return a.Contains(b.Addr()) || b.Contains(a.Addr())
}

func isYeetSvcInterface(name string) bool {
	return name == "yeet0" || strings.HasPrefix(name, "yvm-")
}
