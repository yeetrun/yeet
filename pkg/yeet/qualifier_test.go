// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import "testing"

func TestSetServiceOverrideQualified(t *testing.T) {
	oldService := serviceOverride
	oldPrefs := loadedPrefs
	defer func() {
		serviceOverride = oldService
		loadedPrefs = oldPrefs
		resetHostOverride()
	}()

	loadedPrefs.DefaultHost = "catch"
	SetServiceOverride("plex@yeet-lab")

	if got := getService(); got != "plex" {
		t.Fatalf("service = %q, want plex", got)
	}
	if host, ok := HostOverride(); !ok || host != "yeet-lab" {
		t.Fatalf("host override = %q ok=%v, want yeet-lab", host, ok)
	}
	if loadedPrefs.DefaultHost != "yeet-lab" {
		t.Fatalf("host = %q, want yeet-lab", loadedPrefs.DefaultHost)
	}
}
