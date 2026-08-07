// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package iso

import (
	"net/netip"
	"strings"
	"testing"
)

func FuzzValidateNetwork(f *testing.F) {
	f.Add("172.30.0.0/16", "api", "iso,ts", "compose", false)
	f.Add("172.30.0.0/16", "root-native", "iso", "native", false)
	f.Add("172.30.0.0/16", "non-root-native", "iso", "native", false)
	f.Add("172.30.0.0/16", "timer", "iso", "cron", false)
	f.Fuzz(func(t *testing.T, rawPrefix, component, rawModes, rawPayload string, published bool) {
		prefix, err := netip.ParsePrefix(rawPrefix)
		if err == nil {
			_, _ = NewLayout(prefix)
			_, _ = PlanComponents(prefix, nil, []string{component})
		}
		modes, normalizeErr := NormalizeModes(strings.Split(rawModes, ","))
		if normalizeErr != nil {
			return
		}
		payload := PayloadKind(rawPayload)
		err = ValidateNetwork(NetworkRequest{Payload: payload, Modes: modes, Published: published})
		if !hasMode(modes, "iso") {
			return
		}
		if published && err == nil {
			t.Fatal("ISO input with published ports passed validation")
		}
		if (hasMode(modes, "svc") || hasMode(modes, "lan") || hasMode(modes, "host")) && err == nil {
			t.Fatal("ISO input with an incompatible topology passed validation")
		}
		if (payload == PayloadNative || payload == PayloadCron) && len(modes) != 1 && err == nil {
			t.Fatal("native or timer ISO input with a second mode passed validation")
		}
	})
}
