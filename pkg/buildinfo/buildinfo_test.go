// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package buildinfo

import "testing"

func TestReleaseChannelClassifiesKnownVersions(t *testing.T) {
	tests := []struct {
		name string
		info Info
		want Channel
	}{
		{name: "stable with v prefix", info: Info{Version: "v0.5.13"}, want: ChannelStable},
		{name: "stable without v prefix", info: Info{Version: "0.5.13"}, want: ChannelStable},
		{name: "nightly", info: Info{Version: "nightly-abc1234"}, want: ChannelNightly},
		{name: "dev commit", info: Info{Version: "abc123456"}, want: ChannelDev},
		{name: "dev fallback", info: Info{Version: "dev"}, want: ChannelDev},
		{name: "unknown", info: Info{Version: "unknown"}, want: ChannelUnknown},
		{name: "empty", info: Info{}, want: ChannelUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.info.ReleaseChannel(); got != tt.want {
				t.Fatalf("ReleaseChannel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsRelease(t *testing.T) {
	tests := []struct {
		name string
		info Info
		want bool
	}{
		{name: "stable", info: Info{Version: "v0.5.13"}, want: true},
		{name: "nightly", info: Info{Version: "nightly-abc1234"}, want: true},
		{name: "dev", info: Info{Version: "dev"}, want: false},
		{name: "unknown", info: Info{Version: "unknown"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.info.IsRelease(); got != tt.want {
				t.Fatalf("IsRelease() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestCompareSemver(t *testing.T) {
	tests := []struct {
		a    string
		b    string
		want int
	}{
		{a: "v0.5.13", b: "v0.5.13", want: 0},
		{a: "v0.5.12", b: "v0.5.13", want: -1},
		{a: "v0.5.14", b: "v0.5.13", want: 1},
		{a: "v0.4.99", b: "v0.5.0", want: -1},
		{a: "v1.0.0", b: "v0.99.99", want: 1},
		{a: "0.5.13", b: "v0.5.13", want: 0},
		{a: "dev", b: "v0.5.13", want: 0},
		{a: "v0.5", b: "v0.5.13", want: 0},
		{a: "v0.5.13", b: "nightly-abc1234", want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.a+"_"+tt.b, func(t *testing.T) {
			if got := CompareSemver(tt.a, tt.b); got != tt.want {
				t.Fatalf("CompareSemver(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestCommitVersionFromSettings(t *testing.T) {
	tests := []struct {
		name     string
		settings []buildSetting
		want     string
	}{
		{
			name: "shortens long commit",
			settings: []buildSetting{
				{Key: "vcs.revision", Value: "123456789abcdef"},
			},
			want: "123456789",
		},
		{
			name: "marks dirty",
			settings: []buildSetting{
				{Key: "vcs.revision", Value: "123456789abcdef"},
				{Key: "vcs.modified", Value: "true"},
			},
			want: "123456789+dirty",
		},
		{
			name: "keeps short commit",
			settings: []buildSetting{
				{Key: "vcs.revision", Value: "abc1234"},
			},
			want: "abc1234",
		},
		{
			name:     "dev without commit",
			settings: nil,
			want:     "dev",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := commitVersionFromSettings(tt.settings); got != tt.want {
				t.Fatalf("commitVersionFromSettings() = %q, want %q", got, tt.want)
			}
		})
	}
}
