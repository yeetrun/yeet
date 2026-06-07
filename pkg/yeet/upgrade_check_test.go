// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/buildinfo"
)

func TestUpgradeKnownHostsUsesCurrentHostWithoutAll(t *testing.T) {
	restore := stubPrefsState(t, prefs{DefaultHost: "current"})
	defer restore()

	loc := &projectConfigLocation{Config: &ProjectConfig{Hosts: []string{"a", "b"}}}
	got := upgradeKnownHosts(loc, false, false)
	if strings.Join(got, ",") != "current" {
		t.Fatalf("hosts = %#v", got)
	}
}

func TestFetchUpgradeLatestUsesFreshCache(t *testing.T) {
	restoreCache := stubUpdateCheckCacheFile(t)
	defer restoreCache()
	oldFetch := fetchGitHubReleaseFn
	t.Cleanup(func() { fetchGitHubReleaseFn = oldFetch })
	fetchGitHubReleaseFn = func(nightly bool) (githubRelease, error) {
		t.Fatal("fresh cache should avoid github fetch")
		return githubRelease{}, nil
	}

	now := time.Unix(100, 0)
	if err := writeUpdateCheckCache(updateCheckCacheFile, updateCheckCache{
		LatestStable: releaseCacheEntry{Tag: "v0.5.13", CheckedAt: now.Add(-time.Hour)},
	}); err != nil {
		t.Fatalf("write cache: %v", err)
	}

	got, err := fetchUpgradeLatest(context.Background(), buildinfo.ChannelStable, now)
	if err != nil {
		t.Fatalf("fetch latest: %v", err)
	}
	if got.Tag != "v0.5.13" {
		t.Fatalf("latest tag = %q, want v0.5.13", got.Tag)
	}
}

func TestFetchUpgradeLatestFetchesAndStoresRelease(t *testing.T) {
	restoreCache := stubUpdateCheckCacheFile(t)
	defer restoreCache()
	oldFetch := fetchGitHubReleaseFn
	t.Cleanup(func() { fetchGitHubReleaseFn = oldFetch })
	fetchGitHubReleaseFn = func(nightly bool) (githubRelease, error) {
		if nightly {
			t.Fatal("stable channel should not fetch nightly")
		}
		return githubRelease{TagName: "v0.5.14", PublishedAt: "2026-06-07T00:00:00Z"}, nil
	}

	now := time.Unix(200, 0)
	got, err := fetchUpgradeLatest(context.Background(), buildinfo.ChannelStable, now)
	if err != nil {
		t.Fatalf("fetch latest: %v", err)
	}
	if got.Tag != "v0.5.14" {
		t.Fatalf("latest tag = %q, want v0.5.14", got.Tag)
	}
	cache := readUpdateCheckCache(updateCheckCacheFile)
	if cache.LatestStable.Tag != "v0.5.14" || !cache.LatestStable.CheckedAt.Equal(now) {
		t.Fatalf("cache latest = %#v, want fetched stable", cache.LatestStable)
	}
}

func TestFetchUpgradeLatestFallsBackToStaleCacheOnFetchError(t *testing.T) {
	restoreCache := stubUpdateCheckCacheFile(t)
	defer restoreCache()
	oldFetch := fetchGitHubReleaseFn
	t.Cleanup(func() { fetchGitHubReleaseFn = oldFetch })
	fetchGitHubReleaseFn = func(nightly bool) (githubRelease, error) {
		return githubRelease{}, errors.New("github unavailable")
	}

	now := time.Unix(300, 0)
	if err := writeUpdateCheckCache(updateCheckCacheFile, updateCheckCache{
		LatestStable: releaseCacheEntry{Tag: "v0.5.12", CheckedAt: now.Add(-2 * updateCheckCacheTTL)},
	}); err != nil {
		t.Fatalf("write cache: %v", err)
	}

	got, err := fetchUpgradeLatest(context.Background(), buildinfo.ChannelStable, now)
	if err != nil {
		t.Fatalf("fetch latest: %v", err)
	}
	if got.Tag != "v0.5.12" {
		t.Fatalf("latest tag = %q, want stale cache tag", got.Tag)
	}
}

func stubUpdateCheckCacheFile(t *testing.T) func() {
	t.Helper()
	old := updateCheckCacheFile
	updateCheckCacheFile = t.TempDir() + "/update-check.json"
	return func() {
		updateCheckCacheFile = old
	}
}

func TestUpgradeKnownHostsAllUsesProjectHosts(t *testing.T) {
	restore := stubPrefsState(t, prefs{DefaultHost: "current"})
	defer restore()

	loc := &projectConfigLocation{Config: &ProjectConfig{
		Hosts:    []string{"b"},
		Services: []ServiceEntry{{Name: "svc", Host: "a"}},
	}}
	got := upgradeKnownHosts(loc, true, false)
	if strings.Join(got, ",") != "a,b,current" {
		t.Fatalf("hosts = %#v", got)
	}
}

func TestBuildUpgradeReportClassifiesStaleAndUnreachable(t *testing.T) {
	oldLatest := fetchUpgradeLatestFn
	oldInfo := fetchUpgradeCatchInfoFn
	t.Cleanup(func() {
		fetchUpgradeLatestFn = oldLatest
		fetchUpgradeCatchInfoFn = oldInfo
	})
	fetchUpgradeLatestFn = func(context.Context, buildinfo.Channel, time.Time) (releaseCacheEntry, error) {
		return releaseCacheEntry{Tag: "v0.5.13"}, nil
	}
	fetchUpgradeCatchInfoFn = func(ctx context.Context, host string) (serverInfo, error) {
		if host == "bad" {
			return serverInfo{}, errors.New("dial timeout")
		}
		return serverInfo{Version: "v0.5.12", InstallUser: "root", InstallHost: host}, nil
	}

	report := buildUpgradeReport(context.Background(), upgradeCheckRequest{
		Local: buildinfo.Info{Version: "v0.5.10", Channel: buildinfo.ChannelStable},
		Hosts: []string{"edge", "bad"},
		Now:   time.Unix(100, 0),
	})
	if report.Local.Status != upgradeStatusUpdateAvailable {
		t.Fatalf("local status = %q", report.Local.Status)
	}
	if report.Catch[0].Status != upgradeStatusUpdateAvailable || report.Catch[1].Status != upgradeStatusUnreachable {
		t.Fatalf("catch rows = %#v", report.Catch)
	}
}

func TestBuildUpgradeReportClassifiesDevCatch(t *testing.T) {
	oldLatest := fetchUpgradeLatestFn
	oldInfo := fetchUpgradeCatchInfoFn
	t.Cleanup(func() {
		fetchUpgradeLatestFn = oldLatest
		fetchUpgradeCatchInfoFn = oldInfo
	})
	fetchUpgradeLatestFn = func(context.Context, buildinfo.Channel, time.Time) (releaseCacheEntry, error) {
		return releaseCacheEntry{Tag: "v0.5.13"}, nil
	}
	fetchUpgradeCatchInfoFn = func(ctx context.Context, host string) (serverInfo, error) {
		return serverInfo{Version: "47ee0875a+dirty", InstallUser: "root", InstallHost: host}, nil
	}

	report := buildUpgradeReport(context.Background(), upgradeCheckRequest{
		Local: buildinfo.Info{Version: "v0.5.13", Channel: buildinfo.ChannelStable},
		Hosts: []string{"edge"},
		Now:   time.Unix(100, 0),
	})
	if report.Catch[0].Status != upgradeStatusDev || !strings.Contains(report.Catch[0].Reason, "source/dev") {
		t.Fatalf("catch row = %#v, want dev status", report.Catch[0])
	}
}
