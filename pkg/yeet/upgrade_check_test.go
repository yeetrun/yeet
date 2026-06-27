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

func TestUpgradeKnownHostsUsesProjectHostsByDefault(t *testing.T) {
	restore := stubPrefsState(t, prefs{DefaultHost: "current"})
	defer restore()

	loc := &projectConfigLocation{Config: &ProjectConfig{
		Hosts:    []string{"b"},
		Services: []ServiceEntry{{Name: "svc", Host: "a"}},
	}}
	got := upgradeKnownHosts(loc, false)
	if strings.Join(got, ",") != "a,b,current" {
		t.Fatalf("hosts = %#v", got)
	}
}

func TestUpgradeKnownHostsUsesCurrentHostWithoutProjectConfig(t *testing.T) {
	restore := stubPrefsState(t, prefs{DefaultHost: "current"})
	defer restore()

	got := upgradeKnownHosts(nil, false)
	if strings.Join(got, ",") != "current" {
		t.Fatalf("hosts = %#v", got)
	}
}

func TestUpgradeKnownHostsUsesCurrentHostWithHostOverride(t *testing.T) {
	restore := stubPrefsState(t, prefs{DefaultHost: "override"})
	defer restore()

	loc := &projectConfigLocation{Config: &ProjectConfig{Hosts: []string{"a", "b"}}}
	got := upgradeKnownHosts(loc, true)
	if strings.Join(got, ",") != "override" {
		t.Fatalf("hosts = %#v", got)
	}
}

func TestFetchUpgradeLatestRefreshesFreshCache(t *testing.T) {
	restoreCache := stubUpdateCheckCacheFile(t)
	defer restoreCache()
	oldFetch := fetchGitHubReleaseFn
	t.Cleanup(func() { fetchGitHubReleaseFn = oldFetch })
	fetchGitHubReleaseFn = func(nightly bool) (githubRelease, error) {
		return githubRelease{TagName: "v0.6.0", PublishedAt: "2026-06-07T12:24:07Z"}, nil
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
	if got.Tag != "v0.6.0" {
		t.Fatalf("latest tag = %q, want v0.6.0", got.Tag)
	}
	cache := readUpdateCheckCache(updateCheckCacheFile)
	if cache.LatestStable.Tag != "v0.6.0" || !cache.LatestStable.CheckedAt.Equal(now) {
		t.Fatalf("cache latest = %#v, want refreshed stable", cache.LatestStable)
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

func TestFetchUpgradeTargetUsesSpecificVersion(t *testing.T) {
	oldFetchByTag := fetchGitHubReleaseByTagFn
	oldFetchLatest := fetchUpgradeLatestFn
	t.Cleanup(func() {
		fetchGitHubReleaseByTagFn = oldFetchByTag
		fetchUpgradeLatestFn = oldFetchLatest
	})
	fetchUpgradeLatestFn = func(context.Context, buildinfo.Channel, time.Time) (releaseCacheEntry, error) {
		t.Fatal("specific version should not fetch latest")
		return releaseCacheEntry{}, nil
	}
	var gotTag string
	fetchGitHubReleaseByTagFn = func(tag string) (githubRelease, error) {
		gotTag = tag
		return githubRelease{TagName: "v0.6.1", PublishedAt: "2026-06-07T00:00:00Z"}, nil
	}

	got, err := fetchUpgradeTarget(context.Background(), buildinfo.ChannelStable, time.Unix(400, 0), " v0.6.1 ")
	if err != nil {
		t.Fatalf("fetchUpgradeTarget: %v", err)
	}
	if gotTag != "v0.6.1" || got.Tag != "v0.6.1" {
		t.Fatalf("gotTag=%q target=%#v", gotTag, got)
	}
}

func TestBuildUpgradeReportForceReinstallsDevAndCurrent(t *testing.T) {
	oldLatest := fetchUpgradeLatestFn
	oldInfo := fetchUpgradeCatchInfoFn
	t.Cleanup(func() {
		fetchUpgradeLatestFn = oldLatest
		fetchUpgradeCatchInfoFn = oldInfo
	})
	fetchUpgradeLatestFn = func(context.Context, buildinfo.Channel, time.Time) (releaseCacheEntry, error) {
		return releaseCacheEntry{Tag: "v0.6.2"}, nil
	}
	fetchUpgradeCatchInfoFn = func(ctx context.Context, host string) (serverInfo, error) {
		return serverInfo{Version: "v0.6.2", InstallUser: "root", InstallHost: host}, nil
	}

	report := buildUpgradeReport(context.Background(), upgradeCheckRequest{
		Local: buildinfo.Info{Version: "f6aeae51f+dirty", Channel: buildinfo.ChannelDev},
		Hosts: []string{"edge"},
		Now:   time.Unix(100, 0),
		Force: true,
	})
	if report.Local.Status != upgradeStatusReinstall {
		t.Fatalf("local status = %q, want reinstall", report.Local.Status)
	}
	if report.Catch[0].Status != upgradeStatusReinstall {
		t.Fatalf("catch row = %#v, want reinstall", report.Catch[0])
	}
}

func TestBuildUpgradeReportSpecificVersionDoesNotDowngradeWithoutForce(t *testing.T) {
	oldFetchByTag := fetchGitHubReleaseByTagFn
	oldInfo := fetchUpgradeCatchInfoFn
	t.Cleanup(func() {
		fetchGitHubReleaseByTagFn = oldFetchByTag
		fetchUpgradeCatchInfoFn = oldInfo
	})
	fetchGitHubReleaseByTagFn = func(tag string) (githubRelease, error) {
		return githubRelease{TagName: tag}, nil
	}
	fetchUpgradeCatchInfoFn = func(ctx context.Context, host string) (serverInfo, error) {
		return serverInfo{Version: "v0.6.0", InstallUser: "root", InstallHost: host}, nil
	}

	report := buildUpgradeReport(context.Background(), upgradeCheckRequest{
		Local:         buildinfo.Info{Version: "v0.6.2", Channel: buildinfo.ChannelStable},
		Hosts:         []string{"edge"},
		Now:           time.Unix(100, 0),
		TargetVersion: "v0.6.1",
	})
	if report.Local.Status != upgradeStatusAhead {
		t.Fatalf("local status = %q, want ahead", report.Local.Status)
	}
	if report.Catch[0].Status != upgradeStatusUpdateAvailable {
		t.Fatalf("catch row = %#v, want update available", report.Catch[0])
	}
}
