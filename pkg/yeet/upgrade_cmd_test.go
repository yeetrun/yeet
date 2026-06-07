// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestRunUpgradeUpdatesCatchWithRecordedInstallTarget(t *testing.T) {
	oldInit := initCatchFn
	t.Cleanup(func() { initCatchFn = oldInit })
	var target string
	initCatchFn = func(userAtRemote string, opts initOptions) error {
		target = userAtRemote
		if !opts.fromGithub {
			t.Fatalf("opts = %#v, want from github", opts)
		}
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
}
