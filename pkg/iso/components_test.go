// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package iso

import (
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"testing"
)

func TestPlanComponentsPreservesAndRetiresAddresses(t *testing.T) {
	project := netip.MustParsePrefix("172.30.128.0/27")
	current := map[string]netip.Addr{
		"api": netip.MustParseAddr("172.30.128.2"),
		"old": netip.MustParseAddr("172.30.128.3"),
	}
	got, err := PlanComponents(project, current, []string{"api", "worker"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]netip.Addr{
		"api":    netip.MustParseAddr("172.30.128.2"),
		"worker": netip.MustParseAddr("172.30.128.4"),
	}
	if !reflect.DeepEqual(got.Desired, want) {
		t.Fatalf("Desired = %#v, want %#v", got.Desired, want)
	}
	if got.Retired["old"] != netip.MustParseAddr("172.30.128.3") {
		t.Fatalf("Retired = %#v", got.Retired)
	}
}

func TestPlanComponentsEnforcesCapacity(t *testing.T) {
	names := make([]string, MaxComponents+1)
	for i := range names {
		names[i] = fmt.Sprintf("component-%02d", i)
	}
	_, err := PlanComponents(netip.MustParsePrefix("172.30.128.0/27"), nil, names)
	if !errors.Is(err, ErrComponentCapacity) {
		t.Fatalf("error = %v, want ErrComponentCapacity", err)
	}
}
