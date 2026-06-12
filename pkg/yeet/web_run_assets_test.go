// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

func TestWebRunAssetsEmbedded(t *testing.T) {
	for _, name := range []string{"index.html", "styles.css", "app.js", "yeet-mark.svg"} {
		b, err := fs.ReadFile(webRunAssets, "web_run_assets/"+name)
		if err != nil {
			t.Fatalf("read embedded %s: %v", name, err)
		}
		if len(b) == 0 {
			t.Fatalf("embedded %s is empty", name)
		}
	}
}

func TestWebRunAssetsExposeFirstDeployFields(t *testing.T) {
	index, err := fs.ReadFile(webRunAssets, "web_run_assets/index.html")
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	app, err := fs.ReadFile(webRunAssets, "web_run_assets/app.js")
	if err != nil {
		t.Fatalf("read app: %v", err)
	}
	styles, err := fs.ReadFile(webRunAssets, "web_run_assets/styles.css")
	if err != nil {
		t.Fatalf("read styles: %v", err)
	}

	for _, id := range []string{
		`id="hostDefault"`,
		`id="hostPicker"`,
		`id="hostPickerButton"`,
		`class="host-picker-chev"`,
		`id="workloadSelector"`,
		`name="workload"`,
		`value="compose"`,
		`value="vm"`,
		`value="dockerfile"`,
		`value="remote-image"`,
		`value="file"`,
		`value="cron"`,
		`id="sourceTitle"`,
		`id="vmCatalog"`,
		`id="manualVMSource"`,
		`id="manualVMSourceError"`,
		`id="cronSchedule"`,
		`id="tsVersion"`,
		`id="tsExitNode"`,
		`id="macvlanParent"`,
		`id="macvlanVlan"`,
		`id="macvlanMac"`,
		`id="vmOptions"`,
		`id="vmCPUs"`,
		`id="vmMemory"`,
		`id="vmDisk"`,
		`id="snapshotRequired"`,
		`id="terminalSheet"`,
		`id="terminalOutput"`,
		`id="terminalStatus"`,
		`id="terminalExpand"`,
		`id="terminalSubtitle"`,
		`id="payloadPicker"`,
		`id="envFilePicker"`,
		`id="filePicker"`,
		`id="deploySettingsTitle"`,
		`class="deploy-settings-grid"`,
		`id="storageModeLabel"`,
		`id="zfsHelp"`,
		`<summary>Tailscale settings`,
		`<summary>Snapshots`,
		`<summary>Payload args`,
		`placeholder="tag:app"`,
	} {
		if !strings.Contains(string(index), id) {
			t.Fatalf("index missing %s", id)
		}
	}
	for _, snippet := range []string{
		"tsVersion:",
		"tsExitNode:",
		"macvlanParent:",
		"macvlanVlan:",
		"macvlanMac:",
		"payloadKind:",
		"vm:",
		"cpus:",
		"memory:",
		"disk:",
		"--cpus",
		"--memory",
		"--disk",
		"required:",
		"--ts-ver",
		"--ts-exit",
		"--macvlan-parent",
		"--macvlan-vlan",
		"--macvlan-mac",
		"--snapshot-required",
	} {
		if !strings.Contains(string(app), snippet) {
			t.Fatalf("app missing %s", snippet)
		}
	}
	for _, forbidden := range []string{
		"Needs attention",
		`id="terminalCopy"`,
		"terminalCopy",
		`<div class="file-browser" id="fileBrowser"`,
		`<summary>VM settings`,
		`<summary>LAN settings`,
	} {
		if strings.Contains(string(index)+string(app), forbidden) {
			t.Fatalf("web assets still contain %q", forbidden)
		}
	}
	for _, snippet := range []string{
		"const workloadDefinitions =",
		"function selectedWorkload()",
		"workloadOverride",
		"function syncWorkloadUI()",
		"function workloadPayloadKind(workload)",
		"function sourcePayloadForWorkload(workload)",
		"function inferWorkloadForPayload(payload)",
		"function looksLikeRemoteImageReference(payload)",
		`payload.includes("@")`,
		`payload.startsWith("http://")`,
		"lastColon > lastSlash",
		"function defaultNetworkModesForWorkload(workload)",
		"function renderVMCatalog(images)",
		"data.command",
		`tsAuthKey = "<hidden>"`,
		"cron: {",
		"schedule:",
		"manualVMSource",
		"vmCatalog",
		"syncNetworkUI",
		"validationFieldID",
		`"cron.schedule": "cronSchedule"`,
		"activePicker",
		"showPicker",
		"hidePicker",
		"EventSource",
		"/api/session/closed",
		"TextDecoder",
		"setDeployMode",
		"checkDeployStatus",
		"recoverDeployStream",
		"collapseTerminal",
		"createTerminalRenderer",
		"handleCSI",
		"showHostPicker",
		"hideHostPicker",
		"updateServiceRootPlaceholder",
		"const rows = hosts.map((host) => {",
	} {
		if !strings.Contains(string(app), snippet) {
			t.Fatalf("app missing behavior hook %s", snippet)
		}
	}
	if strings.Contains(string(index)+string(app), "hostOptions") {
		t.Fatal("web assets still contain native hostOptions datalist behavior")
	}
	if strings.Contains(string(index), "<datalist") {
		t.Fatal("index still contains native datalist markup")
	}
	if regexp.MustCompile(`\slist\s*=`).Match(index) {
		t.Fatal("index still contains native input list attribute")
	}
	for _, snippet := range []string{
		".workload-selector",
		".workload-option",
		".source-head",
		".catalog-block",
		".subsection-label",
		".deploy-settings-grid",
	} {
		if !strings.Contains(string(styles), snippet) {
			t.Fatalf("styles missing %s", snippet)
		}
	}
}

func TestWebRunAssetsRecognizeAllVMPayloads(t *testing.T) {
	index, err := fs.ReadFile(webRunAssets, "web_run_assets/index.html")
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	app, err := fs.ReadFile(webRunAssets, "web_run_assets/app.js")
	if err != nil {
		t.Fatalf("read app: %v", err)
	}

	if !strings.Contains(string(app), `payload.trim().startsWith("vm://")`) {
		t.Fatal("web run VM detection must recognize all vm:// catalog payloads")
	}
	if strings.Contains(string(app), `payload.trim() === "vm://ubuntu/26.04"`) {
		t.Fatal("web run VM detection is still hard-coded to Ubuntu")
	}
	if !strings.Contains(string(index), "vm:// payloads") {
		t.Fatal("VM settings help copy should describe all vm:// payloads")
	}
}
