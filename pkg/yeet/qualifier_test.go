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
	SetServiceOverride("media@yeet-edge-a")

	if got := getService(); got != "media" {
		t.Fatalf("service = %q, want media", got)
	}
	if host, ok := HostOverride(); !ok || host != "yeet-edge-a" {
		t.Fatalf("host override = %q ok=%v, want yeet-edge-a", host, ok)
	}
	if loadedPrefs.DefaultHost != "yeet-edge-a" {
		t.Fatalf("host = %q, want yeet-edge-a", loadedPrefs.DefaultHost)
	}
}
