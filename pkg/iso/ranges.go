// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package iso

import "net/netip"

// nonPublicIPv4 is the deny representation of the IANA IPv4 Special-Purpose
// Address Registry snapshot last updated 2025-10-09, plus all IPv4 multicast.
// Source: https://www.iana.org/assignments/iana-ipv4-special-registry/
// The split of 192.0.0.0/24 preserves its globally reachable .9 and .10
// anycast exceptions. Update this reviewed table explicitly when IANA changes.
var nonPublicIPv4 = mustPrefixes(
	"0.0.0.0/8",
	"10.0.0.0/8",
	"100.64.0.0/10",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"172.16.0.0/12",
	"192.0.0.0/29",
	"192.0.0.8/32",
	"192.0.0.11/32",
	"192.0.0.12/30",
	"192.0.0.16/28",
	"192.0.0.32/27",
	"192.0.0.64/26",
	"192.0.0.128/25",
	"192.0.2.0/24",
	"192.88.99.0/24",
	"192.168.0.0/16",
	"198.18.0.0/15",
	"198.51.100.0/24",
	"203.0.113.0/24",
	"224.0.0.0/4",
	"240.0.0.0/4",
)

func IsPublicIPv4(addr netip.Addr, isoPool netip.Prefix) bool {
	if !addr.IsValid() || !addr.Is4() || addr.IsUnspecified() {
		return false
	}
	if isoPool.IsValid() && isoPool.Contains(addr) {
		return false
	}
	for _, prefix := range nonPublicIPv4 {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

func NonPublicIPv4Prefixes(isoPool netip.Prefix) []netip.Prefix {
	out := append([]netip.Prefix(nil), nonPublicIPv4...)
	if isoPool.IsValid() {
		out = append(out, isoPool.Masked())
	}
	return out
}

func mustPrefixes(raw ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(raw))
	for i, value := range raw {
		out[i] = netip.MustParsePrefix(value)
	}
	return out
}
