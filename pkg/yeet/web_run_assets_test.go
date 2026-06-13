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
		`id="storageOptions"`,
		`id="networkOptions"`,
		`id="storageModeLabel"`,
		`id="zfsHelp"`,
		`id="zfsRootPicker"`,
		`id="zfsRootPickerButton"`,
		`id="zfsRootStatus"`,
		`id="zfsRootList"`,
		`<summary>Tailscale settings`,
		`<summary>LAN settings`,
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
		`placeholder="auto"`,
		`placeholder="2g"`,
		`placeholder="64g"`,
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
		"function payloadPickerEnabledForWorkload(workload)",
		"function sourcePayloadForWorkload(workload)",
		"function inferWorkloadForPayload(payload)",
		"function looksLikeRemoteImageReference(payload)",
		`payload.includes("@")`,
		`payload.startsWith("http://")`,
		"lastColon > lastSlash",
		"function defaultNetworkModesForWorkload(workload)",
		"function renderVMCatalog(images)",
		"data.command",
		"validate(draft, seq)",
		"async function validate(draft, seq)",
		`tsAuthKey = "<hidden>"`,
		"cron: {",
		"schedule:",
		"manualVMSource",
		"vmCatalog",
		"syncNetworkUI",
		"validationFieldID",
		`"cron.schedule": "cronSchedule"`,
		`$("payloadPicker").hidden = !payloadPickerEnabled`,
		`state.activePicker === "payload"`,
		`state.activePicker === "envFile"`,
		"activePicker",
		"showPicker",
		"function pickerEnabledForField(field)",
		"if (!pickerEnabledForField(field))",
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
		"zfsRootSeq",
		"pickedZFSRoot",
		"serviceRootManual",
		"function syncZFSRootPicker()",
		"function showZFSRootPicker()",
		"function hideZFSRootPicker()",
		"function zfsRootDisplayDataset(candidate)",
		"function loadZFSRoots(key)",
		"/api/zfs-roots?",
		"vmShapeManual",
		"function syncVMDefaults()",
		"function loadVMDefaults(key)",
		"/api/vm-defaults?",
		"VM ZVOL parent",
		"will contain this VM's zvols",
		"function pickZFSRootCandidate(candidate)",
		"function syncPickedZFSRootValue()",
		"function renderZFSRootCandidates(response)",
		"const rows = hosts.map((host) => {",
	} {
		if !strings.Contains(string(app), snippet) {
			t.Fatalf("app missing behavior hook %s", snippet)
		}
	}
	if strings.Contains(string(index)+string(app), "hostOptions") {
		t.Fatal("web assets still contain native hostOptions datalist behavior")
	}
	if strings.Contains(string(app), "tank/apps") {
		t.Fatal("ZFS placeholder should not imply a fixed dataset layout")
	}
	if strings.Contains(string(index), "<datalist") {
		t.Fatal("index still contains native datalist markup")
	}
	if regexp.MustCompile(`\slist\s*=`).Match(index) {
		t.Fatal("index still contains native input list attribute")
	}
	if strings.Contains(string(index)+string(app)+string(styles), "zfs-root-suggested") {
		t.Fatal("ZFS root picker should not repeat the selected dataset and suggested path in each row")
	}
	helpButtonRE := regexp.MustCompile(`<button[^>]*class="help"[^>]*>`)
	for _, match := range helpButtonRE.FindAllString(string(index), -1) {
		if !strings.Contains(match, `tabindex="-1"`) {
			t.Fatalf("help button should not interrupt the primary tab order: %s", match)
		}
	}
	vmIndex := strings.Index(string(index), `id="vmOptions"`)
	storageIndex := strings.Index(string(index), `id="storageOptions"`)
	networkIndex := strings.Index(string(index), `id="networkOptions"`)
	if vmIndex < 0 || storageIndex < 0 || networkIndex < 0 {
		t.Fatalf("settings order markers missing: vm=%d storage=%d network=%d", vmIndex, storageIndex, networkIndex)
	}
	if !(vmIndex < storageIndex && storageIndex < networkIndex) {
		t.Fatalf("deploy settings order = vm:%d storage:%d network:%d, want VM shape, storage, network", vmIndex, storageIndex, networkIndex)
	}
	networkHTML := string(index[networkIndex:])
	if !strings.Contains(networkHTML, `id="lanOptions"`) || !strings.Contains(networkHTML, `<summary>LAN settings`) {
		t.Fatal("network settings must contain collapsed LAN settings")
	}
	for _, snippet := range []string{
		".workload-selector",
		".workload-option",
		".workload-option span",
		".source-head",
		".catalog-block",
		".subsection-label",
		".deploy-settings-grid",
		".settings-block",
		".zfs-root-field",
		".zfs-root-trigger",
		".zfs-root-picker",
		".zfs-root-row",
		".zfs-root-meta",
		".zfs-root-status",
		"[hidden]",
		"display: none !important",
		"@media (max-width: 430px)",
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

func TestWebRunZFSRootPickFocusesServiceRootForTyping(t *testing.T) {
	app, err := fs.ReadFile(webRunAssets, "web_run_assets/app.js")
	if err != nil {
		t.Fatalf("read app: %v", err)
	}
	source := string(app)

	for _, snippet := range []string{
		"function focusServiceRootAtEnd()",
		`const position = input.value.length`,
		`input.focus({ preventScroll: true })`,
		`input.setSelectionRange(position, position)`,
	} {
		if !strings.Contains(source, snippet) {
			t.Fatalf("app missing service-root focus behavior %s", snippet)
		}
	}

	pickStart := strings.Index(source, "function pickZFSRootCandidate(candidate)")
	if pickStart < 0 {
		t.Fatal("pickZFSRootCandidate missing")
	}
	nextFunction := strings.Index(source[pickStart+1:], "\nfunction ")
	if nextFunction < 0 {
		t.Fatal("pickZFSRootCandidate block terminator missing")
	}
	pickBlock := source[pickStart : pickStart+1+nextFunction]
	if !strings.Contains(pickBlock, "focusServiceRootAtEnd();") {
		t.Fatal("picking a ZFS root should focus the service-root field for immediate typing")
	}
}

func TestWebRunPayloadArgsOnlyShowForRunnableFilesAndCron(t *testing.T) {
	app, err := fs.ReadFile(webRunAssets, "web_run_assets/app.js")
	if err != nil {
		t.Fatalf("read app: %v", err)
	}
	source := string(app)

	for _, snippet := range []string{
		"function payloadArgsEnabled()",
		`return payloadKind === "file" || payloadKind === "cron"`,
		`payloadArgs: payloadArgsEnabled() ? splitArgs($("payloadArgs").value) : []`,
		`$("payloadArgsBlock").hidden = !payloadArgsEnabled()`,
	} {
		if !strings.Contains(source, snippet) {
			t.Fatalf("app missing payload args visibility behavior %s", snippet)
		}
	}
}

func TestWebRunAdvancedBlocksDoNotGetSectionSeparatorPadding(t *testing.T) {
	styles, err := fs.ReadFile(webRunAssets, "web_run_assets/styles.css")
	if err != nil {
		t.Fatalf("read styles: %v", err)
	}
	source := string(styles)

	if !strings.Contains(source, ".settings-block + .settings-block") {
		t.Fatal("settings blocks should still get section separator styling")
	}
	if strings.Contains(source, ".settings-block + .advanced-block") {
		t.Fatal("advanced blocks should keep their own compact summary padding")
	}
}

func TestWebRunVMNetworkSelectionNeverFallsBackToHost(t *testing.T) {
	app, err := fs.ReadFile(webRunAssets, "web_run_assets/app.js")
	if err != nil {
		t.Fatalf("read app: %v", err)
	}
	source := string(app)

	for _, snippet := range []string{
		"function ensureVMNetworkSelection()",
		`if (payloadKind !== "vm" || selectedNetworkModes().length) return`,
		`fallback.checked = true`,
		"ensureVMNetworkSelection();",
	} {
		if !strings.Contains(source, snippet) {
			t.Fatalf("app missing VM network selection behavior %s", snippet)
		}
	}
}
