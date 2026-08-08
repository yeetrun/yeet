// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"slices"
	"testing"

	"github.com/BurntSushi/toml"
)

func FuzzClientConfigTOMLMatches(f *testing.F) {
	desired := clientConfig{
		DefaultHost: "yeet-lab",
		Workspaces:  []string{"/srv/services-a", "/srv/services-b"},
	}
	desired.normalize()
	for _, seed := range []string{
		"",
		"default_host = [",
		"default_host = \"yeet-cloud\"\n",
		"default_host = \"yeet-lab\"\nworkspaces = [\"/srv/services-a\", \"/srv/services-b\"]\n",
		"# formatted differently\nworkspaces = [\"/srv/services-b\", \"/srv/services-a\", \"/srv/services-b\"]\ndefault_host = \"YEET-LAB\"\n",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		var decoded clientConfig
		_, err := toml.Decode(raw, &decoded)
		want := false
		if err == nil {
			decoded.normalize()
			want = decoded.DefaultHost == desired.DefaultHost && slices.Equal(decoded.Workspaces, desired.Workspaces)
		}
		if got := clientConfigTOMLMatches([]byte(raw), desired); got != want {
			t.Fatalf("clientConfigTOMLMatches() = %v, want %v for decoded %#v", got, want, decoded)
		}
	})
}
