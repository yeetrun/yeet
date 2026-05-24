// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catchrpc

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestServiceInfoSnapshotsOmitEmpty(t *testing.T) {
	raw, err := json.Marshal(ServiceInfo{Name: "svc"})
	if err != nil {
		t.Fatalf("Marshal ServiceInfo: %v", err)
	}
	if strings.Contains(string(raw), "snapshots") {
		t.Fatalf("ServiceInfo JSON = %s, want no snapshots field", raw)
	}
}

func TestServiceInfoSnapshotsIncludePopulated(t *testing.T) {
	raw, err := json.Marshal(ServiceInfo{
		Name: "svc",
		Snapshots: &ServiceSnapshots{
			Effective: EffectiveSnapshotPolicy{
				Enabled:  true,
				KeepLast: 3,
				MaxAge:   "72h",
				Required: true,
			},
		},
	})
	if err != nil {
		t.Fatalf("Marshal ServiceInfo: %v", err)
	}
	if !strings.Contains(string(raw), `"snapshots"`) {
		t.Fatalf("ServiceInfo JSON = %s, want snapshots field", raw)
	}
}
