// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/buildinfo"
	"github.com/yeetrun/yeet/pkg/cli"
)

func TestRenderUpgradeReportTable(t *testing.T) {
	report := upgradeReport{
		Local: upgradeComponent{Name: "yeet", Current: "v0.5.10", Latest: "v0.5.13", Status: upgradeStatusUpdateAvailable},
		Catch: []upgradeComponent{
			{Name: "catch", Host: "edge-a", Current: "v0.5.13", Latest: "v0.5.13", Status: upgradeStatusCurrent},
			{Name: "catch", Host: "edge-b", Current: "v0.5.8", Latest: "v0.5.13", Status: upgradeStatusUpdateAvailable},
		},
	}
	var out bytes.Buffer
	if err := renderUpgradeReport(&out, report); err != nil {
		t.Fatalf("renderUpgradeReport: %v", err)
	}
	got := out.String()
	for _, want := range []string{"COMPONENT", "yeet", "catch@edge-b", "update available"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestRenderUpgradeReportTableKeepsDevRowsCompact(t *testing.T) {
	report := upgradeReport{
		Local: upgradeComponent{
			Name:    "yeet",
			Current: "abaf5aaa1+dirty",
			Latest:  "v0.6.0",
			Status:  upgradeStatusDev,
			Reason:  "source/dev builds are not self-updated as release binaries",
		},
		Catch: []upgradeComponent{
			{
				Name:    "catch",
				Host:    "edge-a",
				Current: "47ee0875a+dirty",
				Latest:  "v0.6.0",
				Status:  upgradeStatusDev,
				Reason:  "source/dev builds are not self-updated as release binaries",
			},
		},
	}
	var out bytes.Buffer
	if err := renderUpgradeReport(&out, report); err != nil {
		t.Fatalf("renderUpgradeReport: %v", err)
	}
	got := out.String()
	for _, unwanted := range []string{"source/dev builds"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("output contains %q:\n%s", unwanted, got)
		}
	}
	for _, want := range []string{"yeet", "catch@edge-a", "abaf5aaa1+dirty", "47ee0875a+dirty", "dev build", "v0.6.0"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestRenderUpgradeReportUsesTargetForForcedVersion(t *testing.T) {
	report := upgradeReport{
		Latest:        releaseCacheEntry{Tag: "v0.6.1"},
		Force:         true,
		TargetVersion: "v0.6.1",
		Local: upgradeComponent{
			Name:    "yeet",
			Current: "f6aeae51f+dirty",
			Latest:  "v0.6.1",
			Status:  upgradeStatusReinstall,
		},
	}
	var out bytes.Buffer
	if err := renderUpgradeReport(&out, report); err != nil {
		t.Fatalf("renderUpgradeReport: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "TARGET") || strings.Contains(got, "LATEST") {
		t.Fatalf("output should use TARGET header:\n%s", got)
	}
	if !strings.Contains(got, "reinstall release") {
		t.Fatalf("output missing reinstall status:\n%s", got)
	}
}

func TestHandleUpgradeCheckJSON(t *testing.T) {
	old := buildUpgradeReportFn
	t.Cleanup(func() { buildUpgradeReportFn = old })
	buildUpgradeReportFn = func(context.Context, upgradeCheckRequest) upgradeReport {
		return upgradeReport{Local: upgradeComponent{Name: "yeet", Current: "v0.5.10", Latest: "v0.5.13", Status: upgradeStatusUpdateAvailable}}
	}

	var out bytes.Buffer
	if err := handleUpgrade(context.Background(), []string{"check", "--json"}, &out, &bytes.Buffer{}, buildinfo.Info{Version: "v0.5.10", Channel: buildinfo.ChannelStable}); err != nil {
		t.Fatalf("handleUpgrade: %v", err)
	}
	var decoded upgradeReport
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("decode json: %v\n%s", err, out.String())
	}
	if decoded.Local.Status != upgradeStatusUpdateAvailable {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestHandleUpgradeCheckUsesProjectHostsByDefault(t *testing.T) {
	restore := stubPrefsState(t, prefs{DefaultHost: "current"})
	defer restore()

	dir := t.TempDir()
	cfg := &projectConfigLocation{
		Path: filepath.Join(dir, projectConfigName),
		Dir:  dir,
		Config: &ProjectConfig{
			Version: projectConfigVersion,
			Hosts:   []string{"catch-b"},
			Services: []ServiceEntry{
				{Name: "uptime-kuma", Host: "catch-a"},
			},
		},
	}
	if err := saveProjectConfig(cfg); err != nil {
		t.Fatalf("saveProjectConfig: %v", err)
	}
	t.Chdir(dir)

	old := buildUpgradeReportFn
	t.Cleanup(func() { buildUpgradeReportFn = old })
	var gotHosts []string
	buildUpgradeReportFn = func(_ context.Context, req upgradeCheckRequest) upgradeReport {
		gotHosts = append([]string(nil), req.Hosts...)
		return upgradeReport{Local: upgradeComponent{Name: "yeet", Current: "v0.6.0", Latest: "v0.6.0", Status: upgradeStatusCurrent}}
	}

	if err := handleUpgrade(context.Background(), []string{"check"}, &bytes.Buffer{}, &bytes.Buffer{}, buildinfo.Info{Version: "v0.6.0", Channel: buildinfo.ChannelStable}); err != nil {
		t.Fatalf("handleUpgrade: %v", err)
	}
	if strings.Join(gotHosts, ",") != "catch-a,catch-b,current" {
		t.Fatalf("hosts = %#v", gotHosts)
	}
}

func TestRunUpgradeRequiresInstallMetadataForStaleCatch(t *testing.T) {
	report := upgradeReport{
		Latest: releaseCacheEntry{Tag: "v0.5.13"},
		Local:  upgradeComponent{Name: "yeet", Current: "v0.5.13", Latest: "v0.5.13", Status: upgradeStatusCurrent},
		Catch: []upgradeComponent{
			{Name: "catch", Host: "edge-a", Current: "v0.5.10", Latest: "v0.5.13", Status: upgradeStatusUpdateAvailable},
		},
	}
	err := runUpgrade(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, cli.UpgradeFlags{Yes: true}, report)
	if err == nil || !strings.Contains(err.Error(), "missing install host metadata") {
		t.Fatalf("runUpgrade error = %v", err)
	}
}

func TestConfirmUpgradePlanRendersUpdates(t *testing.T) {
	report := upgradeReport{
		Local: upgradeComponent{Name: "yeet", Current: "v0.5.10", Latest: "v0.5.13", Status: upgradeStatusUpdateAvailable},
		Catch: []upgradeComponent{
			{Name: "catch", Host: "edge-a", Current: "v0.5.10", Latest: "v0.5.13", Status: upgradeStatusUpdateAvailable},
		},
	}
	var out bytes.Buffer
	ok, err := confirmUpgradePlan(strings.NewReader("y\n"), &out, report)
	if err != nil {
		t.Fatalf("confirmUpgradePlan: %v", err)
	}
	if !ok {
		t.Fatal("confirmUpgradePlan = false, want true")
	}
	got := out.String()
	for _, want := range []string{"Upgrade plan:", "yeet: v0.5.10 -> v0.5.13", "catch@edge-a: v0.5.10 -> v0.5.13", "Proceed? [y/N]:"} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt missing %q:\n%s", want, got)
		}
	}
}

func TestConfirmUpgradeIfNeededSkipsEmptyPlan(t *testing.T) {
	report := upgradeReport{
		Local: upgradeComponent{Name: "yeet", Current: "v0.6.0", Latest: "v0.6.0", Status: upgradeStatusCurrent},
		Catch: []upgradeComponent{
			{Name: "catch", Host: "edge-a", Current: "47ee0875a+dirty", Latest: "v0.6.0", Status: upgradeStatusDev},
		},
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	proceed, err := confirmUpgradeIfNeeded(strings.NewReader(""), &stdout, &stderr, cli.UpgradeFlags{}, report)
	if err != nil {
		t.Fatalf("confirmUpgradeIfNeeded: %v", err)
	}
	if proceed {
		t.Fatal("confirmUpgradeIfNeeded = true, want false")
	}
	if strings.Contains(stdout.String(), "Proceed?") || strings.Contains(stdout.String(), "Upgrade plan:") {
		t.Fatalf("empty plan should not prompt:\nstdout=%q\nstderr=%q", stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "No upgrades available.") {
		t.Fatalf("stdout = %q, want no-upgrades message", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestConfirmUpgradeIfNeededShowsForcedReinstallPlan(t *testing.T) {
	report := upgradeReport{
		Latest: releaseCacheEntry{Tag: "v0.6.2"},
		Force:  true,
		Local:  upgradeComponent{Name: "yeet", Current: "f6aeae51f+dirty", Latest: "v0.6.2", Status: upgradeStatusReinstall},
		Catch: []upgradeComponent{
			{Name: "catch", Host: "edge-a", Current: "47ee0875a+dirty", Latest: "v0.6.2", Status: upgradeStatusReinstall},
		},
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	proceed, err := confirmUpgradeIfNeeded(strings.NewReader("y\n"), &stdout, &stderr, cli.UpgradeFlags{}, report)
	if err != nil {
		t.Fatalf("confirmUpgradeIfNeeded: %v", err)
	}
	if !proceed {
		t.Fatal("confirmUpgradeIfNeeded = false, want true")
	}
	got := stdout.String()
	for _, want := range []string{
		"Upgrade plan:",
		"yeet: f6aeae51f+dirty -> v0.6.2 (reinstall release)",
		"catch@edge-a: 47ee0875a+dirty -> v0.6.2 (reinstall release)",
		"Proceed?",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("stdout missing %q:\n%s", want, got)
		}
	}
}

func TestRunUpgradeUpdatesCatchWithRecordedInstallTarget(t *testing.T) {
	oldInit := initCatchFn
	t.Cleanup(func() { initCatchFn = oldInit })
	var target string
	var releaseVersion string
	var noWorkspace bool
	var suppressNextSteps bool
	initCatchFn = func(userAtRemote string, opts initOptions) error {
		target = userAtRemote
		if !opts.fromGithub {
			t.Fatalf("opts = %#v, want from github", opts)
		}
		releaseVersion = opts.releaseVersion
		noWorkspace = opts.noWorkspace
		suppressNextSteps = opts.suppressNextSteps
		return nil
	}
	report := upgradeReport{
		Latest: releaseCacheEntry{Tag: "v0.5.13"},
		Local:  upgradeComponent{Name: "yeet", Current: "v0.5.13", Latest: "v0.5.13", Status: upgradeStatusCurrent},
		Catch: []upgradeComponent{
			{Name: "catch", Host: "edge-a", Current: "v0.5.10", Latest: "v0.5.13", Status: upgradeStatusUpdateAvailable, InstallUser: "root", InstallHost: "machine-a"},
		},
	}
	if err := runUpgrade(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, cli.UpgradeFlags{Yes: true}, report); err != nil {
		t.Fatalf("runUpgrade: %v", err)
	}
	if target != "root@machine-a" {
		t.Fatalf("target = %q", target)
	}
	if releaseVersion != "v0.5.13" {
		t.Fatalf("releaseVersion = %q, want v0.5.13", releaseVersion)
	}
	if !noWorkspace {
		t.Fatal("noWorkspace = false, want true for catch upgrade reinstall")
	}
	if !suppressNextSteps {
		t.Fatal("suppressNextSteps = false, want true for catch upgrade reinstall")
	}
}

func TestRunUpgradeInstallsCatchWithRowHostOverSoftOverride(t *testing.T) {
	restore := stubPrefsState(t, prefs{DefaultHost: "yeet-lab"})
	defer restore()
	SetHost("yeet-lab")
	oldInit := initCatchFn
	t.Cleanup(func() { initCatchFn = oldInit })
	var gotHostDuringInstall string
	initCatchFn = func(userAtRemote string, opts initOptions) error {
		gotHostDuringInstall = Host()
		return nil
	}
	report := upgradeReport{
		Latest: releaseCacheEntry{Tag: "v0.5.13"},
		Local:  upgradeComponent{Name: "yeet", Current: "v0.5.13", Latest: "v0.5.13", Status: upgradeStatusCurrent},
		Catch: []upgradeComponent{
			{Name: "catch", Host: "yeet-cloud", Current: "v0.5.10", Latest: "v0.5.13", Status: upgradeStatusUpdateAvailable, InstallUser: "root", InstallHost: "cloud-host"},
		},
	}
	if err := runUpgrade(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, cli.UpgradeFlags{Yes: true}, report); err != nil {
		t.Fatalf("runUpgrade: %v", err)
	}
	if gotHostDuringInstall != "yeet-cloud" {
		t.Fatalf("Host during install = %q, want yeet-cloud", gotHostDuringInstall)
	}
	if got := Host(); got != "yeet-lab" {
		t.Fatalf("Host after install = %q, want original soft override", got)
	}
}

func TestUpgradeLocalFromReportPassesForce(t *testing.T) {
	oldUpgrade := upgradeLocalBinaryFn
	t.Cleanup(func() { upgradeLocalBinaryFn = oldUpgrade })
	var gotForce bool
	var gotLatest releaseCacheEntry
	upgradeLocalBinaryFn = func(_ buildinfo.Info, latest releaseCacheEntry, force bool) error {
		gotLatest = latest
		gotForce = force
		return nil
	}

	report := upgradeReport{
		Latest: releaseCacheEntry{Tag: "v0.6.2"},
		Local:  upgradeComponent{Name: "yeet", Current: "dev", Latest: "v0.6.2", Status: upgradeStatusReinstall},
	}
	if err := upgradeLocalFromReport(cli.UpgradeFlags{Force: true}, report); err != nil {
		t.Fatalf("upgradeLocalFromReport: %v", err)
	}
	if !gotForce || gotLatest.Tag != "v0.6.2" {
		t.Fatalf("gotForce=%v gotLatest=%#v", gotForce, gotLatest)
	}
}
