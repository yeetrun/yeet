// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"strings"
	"testing"
)

// assertCallOrder is shared by storage and VM lifecycle tests whose fakes
// record heterogeneous operations in one chronological log.
func assertCallOrder(t *testing.T, calls []string, want ...string) {
	t.Helper()
	last := -1
	for _, needle := range want {
		idx := -1
		for i, call := range calls {
			if strings.Contains(call, needle) {
				idx = i
				break
			}
		}
		if idx == -1 {
			t.Fatalf("calls = %#v, missing %q", calls, needle)
		}
		if idx <= last {
			t.Fatalf("calls = %#v, want %q after prior matched calls", calls, needle)
		}
		last = idx
	}
}
