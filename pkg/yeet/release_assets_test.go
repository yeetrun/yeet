// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"errors"
	"strings"
	"testing"
)

func TestYeetReleaseAssetNames(t *testing.T) {
	tests := []struct {
		name      string
		goos      string
		goarch    string
		assetName string
		shaName   string
	}{
		{
			name:      "linux amd64",
			goos:      "linux",
			goarch:    "amd64",
			assetName: "yeet-linux-amd64.tar.gz",
			shaName:   "yeet-linux-amd64.tar.gz.sha256",
		},
		{
			name:      "darwin arm64 normalized",
			goos:      " Darwin ",
			goarch:    " ARM64 ",
			assetName: "yeet-darwin-arm64.tar.gz",
			shaName:   "yeet-darwin-arm64.tar.gz.sha256",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assetName, shaName, err := yeetReleaseAssetNames(tt.goos, tt.goarch)
			if err != nil {
				t.Fatalf("yeetReleaseAssetNames failed: %v", err)
			}
			if assetName != tt.assetName {
				t.Fatalf("assetName = %q, want %q", assetName, tt.assetName)
			}
			if shaName != tt.shaName {
				t.Fatalf("shaName = %q, want %q", shaName, tt.shaName)
			}
		})
	}
}

func TestYeetReleaseAssetNamesRejectUnsupported(t *testing.T) {
	if _, _, err := yeetReleaseAssetNames("freebsd", "amd64"); err == nil || !strings.Contains(err.Error(), "unsupported OS") {
		t.Fatalf("expected unsupported OS error, got %v", err)
	}
	if _, _, err := yeetReleaseAssetNames("linux", "386"); err == nil || !strings.Contains(err.Error(), "unsupported arch") {
		t.Fatalf("expected unsupported arch error, got %v", err)
	}
}

func TestResolveYeetReleaseAsset(t *testing.T) {
	originalFetch := fetchGitHubReleaseFn
	t.Cleanup(func() {
		fetchGitHubReleaseFn = originalFetch
	})

	var gotNightly bool
	fetchGitHubReleaseFn = func(nightly bool) (githubRelease, error) {
		gotNightly = nightly
		return githubRelease{
			TagName: "v1.2.3",
			Assets: []githubAsset{
				{Name: "yeet-darwin-arm64.tar.gz", BrowserDownloadURL: "https://example.com/yeet.tar.gz"},
				{Name: "yeet-darwin-arm64.tar.gz.sha256", BrowserDownloadURL: "https://example.com/yeet.tar.gz.sha256"},
			},
		}, nil
	}

	assetName, assetURL, shaURL, tag, err := resolveYeetReleaseAsset("darwin", "arm64", true)
	if err != nil {
		t.Fatalf("resolveYeetReleaseAsset failed: %v", err)
	}
	if !gotNightly {
		t.Fatal("fetchGitHubReleaseFn was not called with nightly=true")
	}
	if assetName != "yeet-darwin-arm64.tar.gz" {
		t.Fatalf("assetName = %q", assetName)
	}
	if assetURL != "https://example.com/yeet.tar.gz" {
		t.Fatalf("assetURL = %q", assetURL)
	}
	if shaURL != "https://example.com/yeet.tar.gz.sha256" {
		t.Fatalf("shaURL = %q", shaURL)
	}
	if tag != "v1.2.3" {
		t.Fatalf("tag = %q", tag)
	}

	fetchGitHubReleaseFn = func(bool) (githubRelease, error) {
		return githubRelease{}, errors.New("boom")
	}
	_, _, _, _, err = resolveYeetReleaseAsset("linux", "amd64", false)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected fetch error, got %v", err)
	}
}
