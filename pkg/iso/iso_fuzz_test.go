// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package iso

import (
	"net/netip"
	"strings"
	"testing"
)

func FuzzISOInputs(f *testing.F) {
	f.Add("172.30.0.0/16", "api", "iso,ts")
	f.Fuzz(func(t *testing.T, rawPrefix, component, rawModes string) {
		prefix, err := netip.ParsePrefix(rawPrefix)
		if err == nil {
			_, _ = NewLayout(prefix)
			_, _ = PlanComponents(prefix, nil, []string{component})
		}
		_, _ = NormalizeModes(strings.Split(rawModes, ","))
	})
}
