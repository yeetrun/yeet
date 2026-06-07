// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yeetrun/yeet/pkg/buildinfo"
)

func TestLocalUpgradePlanRejectsDevBuild(t *testing.T) {
	plan, err := localUpgradePlan(buildinfo.Info{Version: "abc123456", Channel: buildinfo.ChannelDev}, releaseCacheEntry{Tag: "v0.5.13"}, false)
	if err != nil {
		t.Fatalf("localUpgradePlan error: %v", err)
	}
	if plan.Action != localUpgradeActionSkip || plan.Reason == "" {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestLocalUpgradePlanForceUpdatesDevBuild(t *testing.T) {
	plan, err := localUpgradePlan(buildinfo.Info{Version: "abc123456", Channel: buildinfo.ChannelDev}, releaseCacheEntry{Tag: "v0.5.13"}, true)
	if err != nil {
		t.Fatalf("localUpgradePlan error: %v", err)
	}
	if plan.Action != localUpgradeActionUpdate {
		t.Fatalf("plan = %#v, want update", plan)
	}
}

func TestLocalUpgradePlanUpdatesStableBehind(t *testing.T) {
	plan, err := localUpgradePlan(buildinfo.Info{Version: "v0.5.10", Channel: buildinfo.ChannelStable}, releaseCacheEntry{Tag: "v0.5.13"}, false)
	if err != nil {
		t.Fatalf("localUpgradePlan error: %v", err)
	}
	if plan.Action != localUpgradeActionUpdate {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestUpgradeLocalBinaryDownloadsAndReplaces(t *testing.T) {
	oldExecutable := currentExecutableFn
	oldResolve := resolveYeetReleaseAssetFn
	oldDownload := downloadFileFn
	oldExtract := extractSingleBinaryFn
	oldReplace := replaceLocalBinaryFn
	t.Cleanup(func() {
		currentExecutableFn = oldExecutable
		resolveYeetReleaseAssetFn = oldResolve
		downloadFileFn = oldDownload
		extractSingleBinaryFn = oldExtract
		replaceLocalBinaryFn = oldReplace
	})

	targetPath := filepath.Join(t.TempDir(), "yeet")
	if err := os.WriteFile(targetPath, []byte("old"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}
	currentExecutableFn = func() (string, error) {
		return targetPath, nil
	}
	resolveYeetReleaseAssetFn = func(goos, goarch string, nightly bool, version string) (string, string, string, string, error) {
		if nightly {
			t.Fatal("stable release should not resolve nightly asset")
		}
		if version != "v0.5.13" {
			t.Fatalf("version = %q, want v0.5.13", version)
		}
		return "yeet-darwin-arm64.tar.gz", "https://example.com/yeet.tgz", "https://example.com/yeet.sha256", "v0.5.13", nil
	}
	var downloads []string
	downloadFileFn = func(url, path string) error {
		downloads = append(downloads, url)
		payload := []byte("archive")
		if url == "https://example.com/yeet.sha256" {
			sum := sha256.Sum256(payload)
			return os.WriteFile(path, []byte(fmt.Sprintf("%x  yeet-darwin-arm64.tar.gz\n", sum)), 0o644)
		}
		if url == "https://example.com/yeet.tgz" {
			return os.WriteFile(path, payload, 0o644)
		}
		return nil
	}
	extractSingleBinaryFn = func(archivePath, dstDir string) (string, error) {
		return filepath.Join(dstDir, "yeet-darwin-arm64"), nil
	}
	var target, source string
	replaceLocalBinaryFn = func(gotTarget, gotSource string, sudo bool) error {
		target = gotTarget
		source = gotSource
		if sudo {
			t.Fatal("writable test binary should not use sudo")
		}
		return nil
	}

	err := upgradeLocalBinary(buildinfo.Info{Version: "v0.5.10", Channel: buildinfo.ChannelStable}, releaseCacheEntry{Tag: "v0.5.13"}, false)
	if err != nil {
		t.Fatalf("upgradeLocalBinary: %v", err)
	}
	if !reflect.DeepEqual(downloads, []string{"https://example.com/yeet.tgz", "https://example.com/yeet.sha256"}) {
		t.Fatalf("downloads = %#v", downloads)
	}
	if target != targetPath || filepath.Base(source) != "yeet-darwin-arm64" {
		t.Fatalf("replace target=%q source=%q", target, source)
	}
}

func TestReplaceLocalBinaryAtomic(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "yeet")
	next := filepath.Join(dir, "next")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.WriteFile(next, []byte("new"), 0o755); err != nil {
		t.Fatalf("write next: %v", err)
	}
	if err := replaceLocalBinary(target, next, false); err != nil {
		t.Fatalf("replaceLocalBinary: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != "new" {
		t.Fatalf("target = %q", got)
	}
}

func TestDownloadFileWritesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("payload"))
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "download")
	if err := downloadFile(server.URL, path); err != nil {
		t.Fatalf("downloadFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read download: %v", err)
	}
	if string(got) != "payload" {
		t.Fatalf("download = %q", got)
	}
}

func TestVerifySHA256File(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "asset")
	payload := []byte("payload")
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatalf("write asset: %v", err)
	}
	sum := sha256.Sum256(payload)
	shaPath := path + ".sha256"
	if err := os.WriteFile(shaPath, []byte(fmt.Sprintf("%x  asset\n", sum)), 0o644); err != nil {
		t.Fatalf("write checksum: %v", err)
	}
	if err := verifySHA256File(path, shaPath); err != nil {
		t.Fatalf("verifySHA256File: %v", err)
	}
}

func TestExtractSingleBinary(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "yeet.tar.gz")
	writeUpgradeTarGz(t, archivePath, "bin/yeet-darwin-arm64", "new-binary")

	got, err := extractSingleBinary(archivePath, dir)
	if err != nil {
		t.Fatalf("extractSingleBinary: %v", err)
	}
	raw, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("read extracted: %v", err)
	}
	if filepath.Base(got) != "yeet-darwin-arm64" || string(raw) != "new-binary" {
		t.Fatalf("extracted %q = %q", got, raw)
	}
}

func writeUpgradeTarGz(t *testing.T, path, name, body string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body))}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatalf("write body: %v", err)
	}
}
