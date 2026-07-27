// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestWebRunAssetsEmbedded(t *testing.T) {
	for _, name := range []string{"index.html", "styles.css", "app.js", "terminal.js", "yeet-mark.svg"} {
		b, err := fs.ReadFile(webRunAssets, "web_run_assets/"+name)
		if err != nil {
			t.Fatalf("read embedded %s: %v", name, err)
		}
		if len(b) == 0 {
			t.Fatalf("embedded %s is empty", name)
		}
	}
}

func TestWebRunAssetsContainTerminalContracts(t *testing.T) {
	index, err := fs.ReadFile(webRunAssets, "web_run_assets/index.html")
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	terminal, err := fs.ReadFile(webRunAssets, "web_run_assets/terminal.js")
	if err != nil {
		t.Fatalf("read terminal adapter: %v", err)
	}
	app, err := fs.ReadFile(webRunAssets, "web_run_assets/app.js")
	if err != nil {
		t.Fatalf("read app: %v", err)
	}
	styles, err := fs.ReadFile(webRunAssets, "web_run_assets/styles.css")
	if err != nil {
		t.Fatalf("read styles: %v", err)
	}

	indexSource := string(index)
	for _, snippet := range []string{
		`<script type="module" src="/app.js"></script>`,
		`<div id="terminalOutput" class="terminal-output" tabindex="0" role="region" aria-label="Deploy output"></div>`,
		`id="terminalStatus" class="terminal-status" role="status"`,
	} {
		if !strings.Contains(indexSource, snippet) {
			t.Fatalf("index missing terminal contract %q", snippet)
		}
	}
	if strings.Contains(indexSource, `id="terminalOutput" tabindex="0" aria-live=`) ||
		strings.Contains(indexSource, `class="terminal-output" tabindex="0" aria-live=`) {
		t.Fatal("raw terminal viewport must not be aria-live")
	}
	if strings.Contains(indexSource, `id="terminalOutput" class="terminal-output" tabindex="0" role="log"`) {
		t.Fatal("raw terminal viewport must not expose an implicit live log role")
	}

	terminalSource := string(terminal)
	for _, snippet := range []string{
		`import { FitAddon, Ghostty, Terminal } from "./ghostty-web.js";`,
		`Ghostty.load(new URL("./ghostty-vt.wasm", import.meta.url).href)`,
		`scrollback: 1000`,
		`new FitAddon()`,
		`disableStdin: true`,
		`convertEol: false`,
		`cursorBlink: false`,
		`resolveTerminalBackground`,
		`getComputedStyle(document.documentElement)`,
		`terminal background must be opaque`,
	} {
		if !strings.Contains(terminalSource, snippet) {
			t.Fatalf("terminal adapter missing %q", snippet)
		}
	}
	if strings.Contains(terminalSource, "handleCSI") {
		t.Fatal("terminal adapter must delegate escape parsing to Ghostty")
	}

	appSource := string(app)
	for _, snippet := range []string{
		`import { createTerminalAdapter, loadTerminalRuntime } from "./terminal.js";`,
		`sessionStorage.setItem("yeet.run.activeJob", jobId)`,
		`state.terminal.write(decodeOutputChunk(event.data))`,
		`state.terminal.resize(resize.cols, resize.rows)`,
		`await state.terminal.drain()`,
		`navigator.clipboard.writeText(state.terminal.copyText())`,
		`setTerminalStatus("Reconnecting", "warning")`,
		`setTerminalStatus("Streaming", "ready")`,
	} {
		if !strings.Contains(appSource, snippet) {
			t.Fatalf("app missing durable terminal contract %q", snippet)
		}
	}
	for _, forbidden := range []string{
		"createTerminalRenderer",
		"handleCSI",
		"recoverDeployStream",
		`state.terminal.text()`,
	} {
		if strings.Contains(appSource, forbidden) {
			t.Fatalf("app still contains custom terminal lifecycle %q", forbidden)
		}
	}

	styleSource := string(styles)
	for _, snippet := range []string{
		".terminal-output canvas",
		".terminal-warning",
		"--terminal-background: oklch(0.14 0.011 165)",
		"overflow: hidden",
		"max-width: none",
	} {
		if !strings.Contains(styleSource, snippet) {
			t.Fatalf("terminal styles missing %q", snippet)
		}
	}
	if got := strings.Count(styleSource, "background: var(--terminal-background);"); got != 2 {
		t.Fatalf("shared terminal background use count = %d, want 2", got)
	}
	for _, forbidden := range []string{
		"overflow-x: auto",
		"overflow-y: auto",
	} {
		if strings.Contains(styleSource, forbidden) {
			t.Fatalf("terminal styles retain outer scrolling %q", forbidden)
		}
	}

	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: t.TempDir()})
	req := httptest.NewRequest(http.MethodGet, "/ghostty-vt.wasm?token=secret", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("WASM status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "application/wasm") {
		t.Fatalf("WASM Content-Type = %q, want application/wasm", got)
	}
}

func TestWebRunAssetsExposeISOCompatibility(t *testing.T) {
	app, err := fs.ReadFile(webRunAssets, "web_run_assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	raw := string(app)
	for _, want := range []string{
		`vm: new Set(["svc", "lan", "iso"])`,
		`compose: new Set(["svc", "lan", "ts", "iso"])`,
		`file: new Set(["svc", "lan", "ts"])`,
		`cron: new Set([])`,
		`function reconcileISOSelection(changedInput)`,
		`if (changedInput.value === "iso")`,
		`["svc", "lan"].includes(input.value)`,
		`publish.value = ""`,
		`publish.disabled = vmPayload || cronPayload || isoSelected`,
		`ISO does not support published ports`,
		`VMs support only iso as a Yeet-managed isolated mode`,
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("app.js missing %q", want)
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
		`id="terminalWarning"`,
		`id="terminalCopy"`,
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
		"setDeployMode",
		"checkDeployStatus",
		"collapseTerminal",
		"createTerminalAdapter",
		"loadTerminalRuntime",
		"terminalCopy",
		"terminalWarning",
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
		"hostStorageBaseState",
		"hostStorageResponseKey",
		"hostStorageDefaultsKey",
		"function syncHostStorage()",
		"async function loadHostStorage(key)",
		"/api/host-storage?",
		"function applyHostStorageDefaults()",
		`$("serviceRoot").value = defaults.serviceRoot`,
		`$("zfs").checked = defaults.zfs`,
		"function hostStorageAutoDefaults()",
		"defaults.serviceRootPlaceholder",
		"function hostStorageDefaultsForCurrentService()",
		"function deriveHostStorageDefaults(defaults, service)",
		"function serviceRootFromPlaceholder(pattern, service)",
		"function rememberHostStorageBase(response, contextKey, service)",
		"function hostStorageBaseStateFromResponse(response, service)",
		"function serviceRootPlaceholderForService(root, service)",
		`return { serviceRoot: "", zfs: null, sourceKey };`,
		"function defaultServicesRoot()",
		"function defaultServiceRootPlaceholder()",
		`if (!draft.service) {`,
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
		`reconcileISOSelection(input);`,
		`ensureVMNetworkSelection(input.value);`,
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

func TestWebRunGhosttyAssetsEmbeddedAndPinned(t *testing.T) {
	for _, name := range []string{
		"ghostty-web.js",
		"ghostty-vt.wasm",
		"ghostty-web.LICENSE",
		"ghostty-web.manifest.json",
	} {
		b, err := fs.ReadFile(webRunAssets, "web_run_assets/"+name)
		if err != nil {
			t.Fatalf("read embedded %s: %v", name, err)
		}
		if len(b) == 0 {
			t.Fatalf("embedded %s is empty", name)
		}
	}

	manifestBytes, err := fs.ReadFile(webRunAssets, "web_run_assets/ghostty-web.manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Package                string `json:"package"`
		Version                string `json:"version"`
		Tarball                string `json:"tarball"`
		Integrity              string `json:"integrity"`
		License                string `json:"license"`
		InlineWasmReplacedWith string `json:"inlineWasmReplacedWith"`
		SelectAllPatch         string `json:"selectAllPatch"`
		ScrollbackPatch        string `json:"scrollbackPatch"`
		ResetPatch             string `json:"resetPatch"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("parse Ghostty manifest: %v", err)
	}
	if want := (struct {
		Package                string
		Version                string
		Tarball                string
		Integrity              string
		License                string
		InlineWasmReplacedWith string
		SelectAllPatch         string
		ScrollbackPatch        string
		ResetPatch             string
	}{
		Package:                "ghostty-web",
		Version:                "0.4.0",
		Tarball:                "https://registry.npmjs.org/ghostty-web/-/ghostty-web-0.4.0.tgz",
		Integrity:              "sha512-0puDBik2qapbD/QQBW9o5ZHfXnZBqZWx/ctBiVtKZ6ZLds4NYb+wZuw1cRLXZk9zYovIQ908z3rvFhexAvc5Hg==",
		License:                "MIT",
		InlineWasmReplacedWith: "./ghostty-vt.wasm",
		SelectAllPatch:         "absolute rows 0 through scrollback plus screen",
		ScrollbackPatch:        "expose newest configured line window",
		ResetPatch:             "failure-atomic owner swap with selection scroll and link reset",
	}); manifest.Package != want.Package ||
		manifest.Version != want.Version ||
		manifest.Tarball != want.Tarball ||
		manifest.Integrity != want.Integrity ||
		manifest.License != want.License ||
		manifest.InlineWasmReplacedWith != want.InlineWasmReplacedWith ||
		manifest.SelectAllPatch != want.SelectAllPatch ||
		manifest.ScrollbackPatch != want.ScrollbackPatch ||
		manifest.ResetPatch != want.ResetPatch {
		t.Fatalf("unexpected Ghostty manifest: %+v", manifest)
	}

	js, err := fs.ReadFile(webRunAssets, "web_run_assets/ghostty-web.js")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(js), "data:application/wasm;base64") {
		t.Fatal("vendored Ghostty JS still embeds the WASM payload")
	}
	if strings.Contains(string(js), "Copyright (c) 2025 AUTHORS") {
		t.Fatal("vendored Ghostty JS must retain its upstream MIT attribution")
	}
	if !strings.Contains(string(js), "./ghostty-vt.wasm") {
		t.Fatal("vendored Ghostty JS does not load the local WASM asset")
	}
	if strings.Contains(string(js), "this.selectionStart = { col: 0, absoluteRow: B }") {
		t.Fatal("vendored Ghostty selectAll still starts at the viewport offset")
	}
	if !strings.Contains(string(js), "this.selectionStart = { col: 0, absoluteRow: 0 }") ||
		!strings.Contains(string(js), "absoluteRow: B + A.rows - 1") {
		t.Fatal("vendored Ghostty selectAll does not span scrollback and the active screen")
	}
	for _, snippet := range []string{
		"this.scrollbackLimit = C ? C.scrollbackLimit ?? 1e4 : 1e4",
		"this.getNativeScrollbackLength() - this.scrollbackLimit",
	} {
		if !strings.Contains(string(js), snippet) {
			t.Fatalf("vendored Ghostty scrollback window missing %q", snippet)
		}
	}
	if got := strings.Count(string(js), "A + this.getScrollbackStart()"); got != 2 {
		t.Fatalf("vendored Ghostty scrollback offset consumers = %d, want line and grapheme accessors", got)
	}
	for _, snippet := range []string{
		"const A = this.buildWasmConfig(), B = this.wasmTerm, g = this.ghostty.createTerminal",
		"this.wasmTerm = g, this.selectionManager && (this.selectionManager.wasmTerm = g)",
		"this.renderer && (this.renderer.currentBuffer = g",
		"this.renderer.currentSelectionCoords = null",
		"this.renderer.hoveredHyperlinkId = 0",
		"this.renderer.previousHoveredHyperlinkId = 0",
		"this.renderer.hoveredLinkRange = null",
		"this.renderer.previousHoveredLinkRange = null",
		"this.linkDetector && this.linkDetector.invalidateCache()",
		"this.currentHoveredLink = void 0",
		`this.element && (this.element.style.cursor = "text")`,
		"this.scrollAnimationFrame && cancelAnimationFrame(this.scrollAnimationFrame)",
		"this.scrollAnimationStartTime = void 0",
		"this.scrollAnimationStartY = void 0",
		"this.targetViewportY = 0",
		"this.viewportY = 0",
		"B && B.free()",
	} {
		if !strings.Contains(string(js), snippet) {
			t.Fatalf("vendored Ghostty failure-atomic reset missing %q", snippet)
		}
	}
}
