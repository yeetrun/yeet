// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCatchReleaseAssetNames(t *testing.T) {
	assetName, shaName, err := catchReleaseAssetNames("Linux", "ARM64")
	if err != nil {
		t.Fatalf("catchReleaseAssetNames failed: %v", err)
	}
	if assetName != "catch-linux-arm64.tar.gz" {
		t.Fatalf("assetName = %q", assetName)
	}
	if shaName != "catch-linux-arm64.tar.gz.sha256" {
		t.Fatalf("shaName = %q", shaName)
	}

	if _, _, err := catchReleaseAssetNames("Darwin", "arm64"); err == nil {
		t.Fatal("expected non-linux error")
	}
	if _, _, err := catchReleaseAssetNames("Linux", "386"); err == nil {
		t.Fatal("expected unsupported arch error")
	}
}

func TestResolveReleaseAssetURLs(t *testing.T) {
	assets := []githubAsset{
		{Name: "catch-linux-amd64.tar.gz", BrowserDownloadURL: "https://example.com/catch.tgz"},
		{Name: "catch-linux-amd64.tar.gz.sha256", BrowserDownloadURL: "https://example.com/catch.sha256"},
	}

	assetURL, shaURL, err := resolveReleaseAssetURLs(assets, "catch-linux-amd64.tar.gz", "catch-linux-amd64.tar.gz.sha256")
	if err != nil {
		t.Fatalf("resolveReleaseAssetURLs failed: %v", err)
	}
	if assetURL != "https://example.com/catch.tgz" || shaURL != "https://example.com/catch.sha256" {
		t.Fatalf("urls = %q %q", assetURL, shaURL)
	}

	_, _, err = resolveReleaseAssetURLs(assets, "catch-linux-arm64.tar.gz", "catch-linux-arm64.tar.gz.sha256")
	if err == nil || !strings.Contains(err.Error(), "asset not found") {
		t.Fatalf("expected missing asset error, got %v", err)
	}
}

func TestFetchGitHubReleaseFromURL(t *testing.T) {
	var gotAccept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v1.2.3","assets":[{"name":"catch-linux-amd64.tar.gz","browser_download_url":"https://example.com/catch.tgz"}]}`))
	}))
	defer server.Close()

	rel, err := fetchGitHubReleaseFromURL(server.URL, server.Client())
	if err != nil {
		t.Fatalf("fetchGitHubReleaseFromURL failed: %v", err)
	}
	if rel.TagName != "v1.2.3" {
		t.Fatalf("TagName = %q", rel.TagName)
	}
	if gotAccept != "application/vnd.github+json" {
		t.Fatalf("Accept = %q", gotAccept)
	}
}

func TestFetchGitHubReleaseFromURLRequiresTag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"assets":[]}`))
	}))
	defer server.Close()

	_, err := fetchGitHubReleaseFromURL(server.URL, server.Client())
	if err == nil || !strings.Contains(err.Error(), "missing release tag") {
		t.Fatalf("expected missing tag error, got %v", err)
	}
}
