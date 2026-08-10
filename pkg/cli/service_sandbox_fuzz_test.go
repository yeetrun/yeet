// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cli

import "testing"

func FuzzParseSandboxExposure(f *testing.F) {
	for _, seed := range []struct {
		raw        string
		allowReset bool
	}{
		{raw: "/srv/shared"},
		{raw: "/srv/shared:/opt/input"},
		{raw: "reset", allowReset: true},
		{raw: "/srv/with space:/opt/with space"},
		{raw: "/srv/shared:/opt/../input"},
		{raw: "/srv/shared:/opt/input:copy"},
		{raw: ""},
		{raw: "/"},
		{raw: string([]byte{'/', 0xff})},
	} {
		f.Add(seed.raw, seed.allowReset)
	}

	f.Fuzz(func(t *testing.T, raw string, allowReset bool) {
		exposure, reset, err := ParseSandboxExposure(raw, allowReset)
		if err != nil || reset {
			return
		}
		formatted := FormatSandboxExposure(exposure)
		got, gotReset, err := ParseSandboxExposure(formatted, allowReset)
		if err != nil {
			t.Fatalf("reparse %q: %v", formatted, err)
		}
		if gotReset || got != exposure {
			t.Fatalf("reparse %q = %#v reset=%v, want %#v reset=false", formatted, got, gotReset, exposure)
		}
	})
}

func FuzzParseServiceSetSandbox(f *testing.F) {
	for _, seed := range []string{
		"--sandbox=on",
		"--sandbox-ro=/srv/shared",
		"--sandbox-rw=/srv/cache:/cache",
		"--sandbox-ro=reset",
		"--sandbox-ro=reset --sandbox-ro=/srv/with-space",
		"--sandbox-rw=/srv/../cache",
		"--sandbox-ro=/srv/cache:/cache:copy",
		"--sandbox-ro=",
		"--sandbox-ro=/",
		string([]byte{'-', '-', 's', 'a', 'n', 'd', 'b', 'o', 'x', '-', 'r', 'o', '=', '/', 0xff}),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		args := append([]string{"api"}, fuzzArgs(raw)...)
		flags, _, err := ParseServiceSet(args)
		if err != nil {
			return
		}
		for _, exposure := range append(flags.Sandbox.ReadOnly, flags.Sandbox.Writable...) {
			if exposure.Source == "reset" || exposure.Destination == "reset" {
				t.Fatalf("ParseServiceSet(%#v) retained reset as a path: %#v", args, flags.Sandbox)
			}
		}
	})
}
