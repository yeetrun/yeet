// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package iso

import (
	"net/netip"
	"testing"
)

func TestLayoutPartitionsPreferredPool(t *testing.T) {
	layout, err := NewLayout(netip.MustParsePrefix("172.30.0.0/16"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := layout.Links, netip.MustParsePrefix("172.30.0.0/17"); got != want {
		t.Fatalf("Links = %v, want %v", got, want)
	}
	if got, want := layout.Projects, netip.MustParsePrefix("172.30.128.0/17"); got != want {
		t.Fatalf("Projects = %v, want %v", got, want)
	}
	link, _ := layout.Link(8191)
	if want := netip.MustParsePrefix("172.30.127.252/30"); link != want {
		t.Fatalf("last link = %v, want %v", link, want)
	}
	project, _ := layout.Project(1023)
	if want := netip.MustParsePrefix("172.30.255.224/27"); project != want {
		t.Fatalf("last project = %v, want %v", project, want)
	}
}
