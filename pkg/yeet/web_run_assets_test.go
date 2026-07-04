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
		`id="vmMemoryMin"`,
		`id="vmBalloon"`,
		`id="vmDisk"`,
		`id="snapshotDetails"`,
		`id="snapshotSummaryText"`,
		`id="snapshotModeLabel"`,
		`id="snapshotRequiredField"`,
		`id="snapshotEventsField"`,
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
		`<summary>Payload args`,
		`placeholder="tag:app"`,
		`id="tsTagsError"`,
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
		"memoryMin:",
		"balloon:",
		"disk:",
		"--vcpus",
		"--memory",
		"--memory-min",
		"--balloon",
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
		"function snapshotDraftForPayloadKind(payloadKind)",
		"function syncSnapshotUI(payloadKind)",
		`snapshotEventsField`,
		`snapshotRequiredField`,
		`VM snapshots`,
		`VM snapshot policy does not use events`,
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
		"function syncTailscaleTagRequirement",
		`"network.tsTags": "tsTags"`,
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
	if strings.Contains(string(app), "snapshots: snapshotDraftForPayloadKind(payloadKind),") {
		t.Fatal("cron drafts must omit the snapshots field instead of sending an empty object")
	}
	if !strings.Contains(string(app), "const snapshots = snapshotDraftForPayloadKind(payloadKind);") ||
		!strings.Contains(string(app), "...(cronPayload ? {} : { snapshots }),") {
		t.Fatal("buildDraft must include snapshots only for non-cron payloads")
	}
	if strings.Contains(string(app), "required: vmPayload ? undefined") ||
		strings.Contains(string(app), "events: vmPayload ? []") {
		t.Fatal("VM snapshot drafts must omit required and events fields entirely")
	}
	if !strings.Contains(string(app), "if (vmPayload) return") ||
		!strings.Contains(string(app), "required: snapshotRequiredValue(),") ||
		!strings.Contains(string(app), `events: splitCSV($("snapshotEvents").value),`) {
		t.Fatal("snapshot draft helper must branch VM retention-only fields from non-VM required/events fields")
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

func TestWebRunZFSRootPickerOpensFromServiceRootField(t *testing.T) {
	app, err := fs.ReadFile(webRunAssets, "web_run_assets/app.js")
	if err != nil {
		t.Fatalf("read app: %v", err)
	}
	source := string(app)

	for _, snippet := range []string{
		`$("serviceRoot").addEventListener("focus", showZFSRootPicker)`,
		`$("serviceRoot").addEventListener("click", showZFSRootPicker)`,
		`if (event.target.closest("#zfsRootPicker") || event.target.closest(".zfs-root-field")) return`,
	} {
		if !strings.Contains(source, snippet) {
			t.Fatalf("app missing service-root picker trigger %s", snippet)
		}
	}
}

func TestWebRunServiceRootPlaceholderUsesHostStorageDefaults(t *testing.T) {
	app, err := fs.ReadFile(webRunAssets, "web_run_assets/app.js")
	if err != nil {
		t.Fatalf("read app: %v", err)
	}
	source := string(app)

	for _, snippet := range []string{
		"hostStorageSeq",
		"hostStorageKey",
		"hostStorageState",
		"hostStorageDefaultsKey",
		"function syncHostStorage()",
		"async function loadHostStorage(key)",
		"/api/host-storage?",
		"function applyHostStorageDefaults()",
		`$("serviceRoot").value = defaults.serviceRoot`,
		`$("zfs").checked = defaults.zfs`,
		"function hostStorageAutoDefaults()",
		`return { serviceRoot: "", zfs: null };`,
		"function defaultServicesRoot()",
		"function defaultServiceRootPlaceholder()",
		"function serviceRootHelpText()",
		"Leave empty to use",
		"selected catch host's default services root",
	} {
		if !strings.Contains(source, snippet) {
			t.Fatalf("app missing host storage default behavior %s", snippet)
		}
	}
	for _, forbidden := range []string{
		"`/root/data/services/${service}`",
		`"/root/data/services/"`,
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("app still hardcodes legacy services root placeholder %s", forbidden)
		}
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
		"function ensureVMNetworkSelection(clearedMode = \"\")",
		`if (payloadKind !== "vm" || selectedNetworkModes().length) return`,
		`const fallbackValue = clearedMode === "svc" ? "lan" : "svc"`,
		`fallback.checked = true`,
		`input.addEventListener("input", () => ensureVMNetworkSelection(input.value))`,
		`$("hostDefault").closest("label").hidden = vmPayload`,
		`$("hostDefault").checked = !vmPayload && modes.length === 0`,
		"ensureVMNetworkSelection();",
	} {
		if !strings.Contains(source, snippet) {
			t.Fatalf("app missing VM network selection behavior %s", snippet)
		}
	}
}

func TestWebRunPayloadPickerSupportsFuzzyKeyboardFiltering(t *testing.T) {
	app, err := fs.ReadFile(webRunAssets, "web_run_assets/app.js")
	if err != nil {
		t.Fatalf("read app: %v", err)
	}
	source := string(app)

	for _, snippet := range []string{
		"fileSearchSeq",
		"filePickerActiveIndex",
		"function filePickerInputForActiveField()",
		"async function loadFileMatches(query)",
		"new URLSearchParams({ q: query, field: state.activePicker })",
		"function renderFilePickerEntries(entries, emptyMessage)",
		"function setFilePickerActiveIndex(index)",
		`setAttribute("aria-activedescendant"`,
		"function handlePickerKeydown(event)",
		`event.key === "ArrowDown"`,
		`event.key === "ArrowUp"`,
		`event.key === "Enter"`,
		`event.key === "Escape"`,
		"function handlePayloadFilterInput()",
		`$("payload").addEventListener("input", handlePayloadFilterInput)`,
		`No matches`,
	} {
		if !strings.Contains(source, snippet) {
			t.Fatalf("app missing fuzzy payload picker behavior %s", snippet)
		}
	}
}
