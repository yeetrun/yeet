// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package iso

import (
	"net/netip"
	"testing"
)

func TestIsPublicIPv4(t *testing.T) {
	pool := netip.MustParsePrefix("172.30.0.0/16")
	for raw, want := range map[string]bool{
		"1.1.1.1":              true,
		"8.8.8.8":              true,
		"10.0.0.1":             false,
		"100.100.100.100":      false,
		"169.254.169.254":      false,
		"172.30.0.2":           false,
		"192.0.0.8":            false,
		"192.0.0.9":            true,
		"192.0.0.10":           true,
		"192.31.196.1":         true,
		"192.52.193.1":         true,
		"192.175.48.1":         true,
		"192.0.2.1":            false,
		"198.18.0.1":           false,
		"224.0.0.1":            false,
		"2001:4860:4860::8888": false,
	} {
		if got := IsPublicIPv4(netip.MustParseAddr(raw), pool); got != want {
			t.Errorf("IsPublicIPv4(%s) = %v, want %v", raw, got, want)
		}
	}
}
