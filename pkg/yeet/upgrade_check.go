// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/yeetrun/yeet/pkg/buildinfo"
)

type upgradeStatus string

const (
	upgradeStatusCurrent         upgradeStatus = "current"
	upgradeStatusUpdateAvailable upgradeStatus = "update available"
	upgradeStatusReinstall       upgradeStatus = "reinstall release"
	upgradeStatusAhead           upgradeStatus = "newer than target"
	upgradeStatusUnknown         upgradeStatus = "unknown"
	upgradeStatusUnreachable     upgradeStatus = "unreachable"
	upgradeStatusDev             upgradeStatus = "dev build"
)

type upgradeCheckRequest struct {
	Local         buildinfo.Info
	Hosts         []string
	Now           time.Time
	Force         bool
	TargetVersion string
}

type upgradeReport struct {
	Latest        releaseCacheEntry  `json:"latest"`
	Local         upgradeComponent   `json:"local"`
	Catch         []upgradeComponent `json:"catch,omitempty"`
	Force         bool               `json:"force,omitempty"`
	TargetVersion string             `json:"targetVersion,omitempty"`
}

type upgradeComponent struct {
	Name        string        `json:"name"`
	Host        string        `json:"host,omitempty"`
	Current     string        `json:"current,omitempty"`
	Latest      string        `json:"latest,omitempty"`
	Status      upgradeStatus `json:"status"`
	Reason      string        `json:"reason,omitempty"`
	InstallUser string        `json:"installUser,omitempty"`
	InstallHost string        `json:"installHost,omitempty"`
}

var fetchUpgradeLatestFn = fetchUpgradeLatest
var fetchUpgradeCatchInfoFn = fetchUpgradeCatchInfo

func upgradeKnownHosts(cfgLoc *projectConfigLocation, all bool, hostOverrideSet bool) []string {
	if !all || hostOverrideSet || cfgLoc == nil || cfgLoc.Config == nil {
		return []string{Host()}
	}
	seen := map[string]struct{}{Host(): {}}
	for _, host := range cfgLoc.Config.AllHosts() {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		seen[host] = struct{}{}
	}
	hosts := make([]string, 0, len(seen))
	for host := range seen {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return hosts
}

func buildUpgradeReport(ctx context.Context, req upgradeCheckRequest) upgradeReport {
	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}
	channel := req.Local.ReleaseChannel()
	latest, latestErr := fetchUpgradeTarget(ctx, channel, now, req.TargetVersion)
	report := upgradeReport{Latest: latest, Force: req.Force, TargetVersion: strings.TrimSpace(req.TargetVersion)}
	report.Local = classifyLocalUpgrade(req.Local, latest, latestErr, req.Force)
	for _, host := range req.Hosts {
		report.Catch = append(report.Catch, checkCatchUpgrade(ctx, host, latest, latestErr, req.Force))
	}
	return report
}

func classifyLocalUpgrade(local buildinfo.Info, latest releaseCacheEntry, latestErr error, force bool) upgradeComponent {
	row := upgradeComponent{Name: "yeet", Current: local.Version, Latest: latest.Tag}
	if latestErr != nil || latest.Tag == "" {
		row.Status = upgradeStatusUnknown
		row.Reason = errorString(latestErr)
		return row
	}
	if force {
		row.Status = upgradeStatusReinstall
		return row
	}
	if !local.IsRelease() {
		row.Status = upgradeStatusDev
		row.Reason = "source/dev builds are not self-updated as release binaries"
		return row
	}
	cmp := buildinfo.CompareSemver(local.Version, latest.Tag)
	if cmp < 0 {
		row.Status = upgradeStatusUpdateAvailable
		return row
	}
	if cmp > 0 {
		row.Status = upgradeStatusAhead
		return row
	}
	row.Status = upgradeStatusCurrent
	return row
}

func checkCatchUpgrade(ctx context.Context, host string, latest releaseCacheEntry, latestErr error, force bool) upgradeComponent {
	row := upgradeComponent{Name: "catch", Host: host, Latest: latest.Tag}
	info, err := fetchUpgradeCatchInfoFn(ctx, host)
	if err != nil {
		row.Status = upgradeStatusUnreachable
		row.Reason = err.Error()
		return row
	}
	row.Current = info.Version
	row.InstallUser = info.InstallUser
	row.InstallHost = info.InstallHost
	if latestErr != nil || latest.Tag == "" {
		row.Status = upgradeStatusUnknown
		row.Reason = errorString(latestErr)
		return row
	}
	if force {
		row.Status = upgradeStatusReinstall
		return row
	}
	catchBuild := buildinfo.Info{Version: info.Version}
	if !catchBuild.IsRelease() {
		row.Status = upgradeStatusDev
		row.Reason = "source/dev builds are not self-updated as release binaries"
		return row
	}
	cmp := buildinfo.CompareSemver(info.Version, latest.Tag)
	if cmp < 0 {
		row.Status = upgradeStatusUpdateAvailable
		return row
	}
	if cmp > 0 {
		row.Status = upgradeStatusAhead
		return row
	}
	row.Status = upgradeStatusCurrent
	return row
}

func fetchUpgradeTarget(ctx context.Context, channel buildinfo.Channel, now time.Time, version string) (releaseCacheEntry, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return fetchUpgradeLatestFn(ctx, channel, now)
	}
	if err := ctx.Err(); err != nil {
		return releaseCacheEntry{}, err
	}
	rel, err := fetchGitHubReleaseByTagFn(version)
	if err != nil {
		return releaseCacheEntry{}, err
	}
	return releaseCacheEntry{Tag: rel.TagName, PublishedAt: rel.PublishedAt, CheckedAt: now, Assets: rel.Assets}, nil
}

func fetchUpgradeLatest(ctx context.Context, channel buildinfo.Channel, now time.Time) (releaseCacheEntry, error) {
	cache := readUpdateCheckCache(updateCheckCacheFile)
	nightly := channel == buildinfo.ChannelNightly
	entry := cache.LatestStable
	if nightly {
		entry = cache.LatestNightly
	}
	if err := ctx.Err(); err != nil {
		if entry.Tag != "" {
			return entry, nil
		}
		return releaseCacheEntry{}, err
	}
	rel, err := fetchGitHubReleaseFn(nightly)
	if err != nil {
		if entry.Tag != "" {
			return entry, nil
		}
		return releaseCacheEntry{}, err
	}
	entry = releaseCacheEntry{Tag: rel.TagName, PublishedAt: rel.PublishedAt, CheckedAt: now, Assets: rel.Assets}
	if nightly {
		cache.LatestNightly = entry
	} else {
		cache.LatestStable = entry
	}
	_ = writeUpdateCheckCache(updateCheckCacheFile, cache)
	return entry, nil
}

func fetchUpgradeCatchInfo(ctx context.Context, host string) (serverInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var info serverInfo
	if err := newRPCClient(host).Call(ctx, "catch.Info", nil, &info); err != nil {
		return serverInfo{}, fmt.Errorf("%s: %w", host, err)
	}
	return info, nil
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
