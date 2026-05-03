// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"reflect"
	"strings"
	"testing"
)

func FuzzParseIPv4Addrs(f *testing.F) {
	for _, seed := range []string{
		"2: eth0    inet 10.0.0.4/24 brd 10.0.0.255 scope global eth0",
		"3: tun0    inet 100.64.0.7 peer 100.64.0.1/32 scope global tun0",
		"4: bad",
		"5: eth1    inet /24 scope global eth1",
		"6: eth2    inet6 fe80::1/64 scope link",
		"",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 4096 {
			t.Skip()
		}
		for _, entry := range parseIPv4Addrs(raw) {
			if entry.Interface == "" {
				t.Fatalf("empty interface in %#v for raw %q", entry, raw)
			}
			if entry.IP == "" {
				t.Fatalf("empty IP in %#v for raw %q", entry, raw)
			}
			if strings.Contains(entry.IP, "/") {
				t.Fatalf("IP %q still contains CIDR suffix for raw %q", entry.IP, raw)
			}
		}
	})
}

func TestParseIPv4Addrs(t *testing.T) {
	input := `
1: lo    inet 127.0.0.1/8 scope host lo
2: eth0    inet 10.0.0.4/24 brd 10.0.0.255 scope global eth0
3: tun0    inet 100.64.0.7 peer 100.64.0.1/32 scope global tun0
4: bad
5: eth1    inet /24 scope global eth1
6: eth2    inet6 fe80::1/64 scope link

`

	got := parseIPv4Addrs(input)
	want := []ifaceIP{
		{Interface: "lo", IP: "127.0.0.1"},
		{Interface: "eth0", IP: "10.0.0.4"},
		{Interface: "tun0", IP: "100.64.0.7"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseIPv4Addrs() = %#v, want %#v", got, want)
	}
}

func TestParseIPv4AddrsEmpty(t *testing.T) {
	if got := parseIPv4Addrs(" \n\n"); len(got) != 0 {
		t.Fatalf("parseIPv4Addrs() = %#v, want empty", got)
	}
}
