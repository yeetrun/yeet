/**
 * Copyright (c) 2025 AUTHORS All rights reserved.
 * Use of this source code is governed by a BSD-style
 * license that can be found in the LICENSE file.
 */

const params = new URLSearchParams(window.location.search);
const token = params.get("token") || "";
const csrfToken = window.__YEET_CSRF_TOKEN__ || "";
const state = {
  bootstrap: null,
  currentDir: ".",
  validateSeq: 0,
  validateTimer: null,
  phase: "editing",
  activePicker: "",
};

const $ = (id) => document.getElementById(id);

function api(path, options = {}) {
  const headers = {
    "Content-Type": "application/json",
    ...(options.headers || {}),
  };
  if (token) headers["X-Yeet-Run-Token"] = token;
  if (csrfToken) headers["X-Yeet-Run-CSRF"] = csrfToken;
  return fetch(path, {
    ...options,
    headers,
  });
}

function splitCSV(raw) {
  return raw
    .split(",")
    .map((part) => part.trim())
    .filter(Boolean);
}

function splitArgs(raw) {
  const input = raw.trim();
  if (!input) return [];
  const args = [];
  let current = "";
  let quote = "";
  let escaped = false;
  for (const char of input) {
    if (escaped) {
      current += char;
      escaped = false;
      continue;
    }
    if (char === "\\") {
      escaped = true;
      continue;
    }
    if (quote) {
      if (char === quote) quote = "";
      else current += char;
      continue;
    }
    if (char === "'" || char === "\"") {
      quote = char;
      continue;
    }
    if (/\s/.test(char)) {
      if (current) {
        args.push(current);
        current = "";
      }
      continue;
    }
    current += char;
  }
  if (current) args.push(current);
  return args;
}

function buildDraft() {
  const restart = true;
  // Future redeploy support can add pull/force once the web flow allows existing services.
  const snapshotRequired = snapshotRequiredValue();
  const modes = selectedNetworkModes();
  const hasTailscale = modes.includes("ts");
  const hasLAN = modes.includes("lan");
  return {
    service: $("service").value.trim(),
    host: $("host").value.trim(),
    payload: $("payload").value.trim(),
    envFile: $("envFile").value.trim(),
    payloadArgs: splitArgs($("payloadArgs").value),
    network: {
      modes,
      tsVersion: hasTailscale ? $("tsVersion").value.trim() : "",
      tsExitNode: hasTailscale ? $("tsExitNode").value.trim() : "",
      tsTags: hasTailscale ? splitCSV($("tsTags").value) : [],
      tsAuthKey: hasTailscale ? $("tsAuthKey").value : "",
      macvlanMac: hasLAN ? $("macvlanMac").value.trim() : "",
      macvlanVlan: hasLAN ? Number.parseInt($("macvlanVlan").value, 10) || 0 : 0,
      macvlanParent: hasLAN ? $("macvlanParent").value.trim() : "",
      publish: splitCSV($("publish").value),
      restart,
    },
    storage: {
      serviceRoot: $("serviceRoot").value.trim(),
      zfs: $("zfs").checked,
    },
    snapshots: {
      mode: $("snapshots").value,
      keepLast: Number.parseInt($("snapshotKeepLast").value, 10) || 0,
      maxAge: $("snapshotMaxAge").value.trim(),
      required: snapshotRequired,
      events: splitCSV($("snapshotEvents").value),
    },
  };
}

function selectedNetworkModes() {
  return [...document.querySelectorAll("input[name='net']:checked")].map((el) => el.value);
}

function snapshotRequiredValue() {
  const value = $("snapshotRequired").value;
  if (value === "true") return true;
  if (value === "false") return false;
  return undefined;
}

function shell(value) {
  if (/^[A-Za-z0-9_./:@=-]+$/.test(value)) return value;
  return JSON.stringify(value);
}

function updatePreview(draft) {
  const parts = ["yeet", "run"];
  if (draft.service) parts.push(shell(draft.service));
  if (draft.payload) parts.push(shell(draft.payload));
  if (draft.envFile) parts.push(`--env-file=${shell(draft.envFile)}`);
  for (const mode of draft.network.modes) parts.push(`--net=${shell(mode)}`);
  if (draft.network.tsVersion) parts.push(`--ts-ver=${shell(draft.network.tsVersion)}`);
  if (draft.network.tsExitNode) parts.push(`--ts-exit=${shell(draft.network.tsExitNode)}`);
  for (const tag of draft.network.tsTags) parts.push(`--ts-tags=${shell(tag)}`);
  if (draft.network.tsAuthKey) parts.push("--ts-auth-key=<hidden>");
  if (draft.network.macvlanParent) parts.push(`--macvlan-parent=${shell(draft.network.macvlanParent)}`);
  if (draft.network.macvlanVlan) parts.push(`--macvlan-vlan=${draft.network.macvlanVlan}`);
  if (draft.network.macvlanMac) parts.push(`--macvlan-mac=${shell(draft.network.macvlanMac)}`);
  for (const port of draft.network.publish) parts.push(`--publish=${shell(port)}`);
  if (draft.storage.serviceRoot) parts.push(`--service-root=${shell(draft.storage.serviceRoot)}`);
  if (draft.storage.zfs) parts.push("--zfs");
  if (draft.snapshots.mode) parts.push(`--snapshots=${shell(draft.snapshots.mode)}`);
  if (draft.snapshots.keepLast) parts.push(`--snapshot-keep-last=${draft.snapshots.keepLast}`);
  if (draft.snapshots.maxAge) parts.push(`--snapshot-max-age=${shell(draft.snapshots.maxAge)}`);
  if (draft.snapshots.required !== undefined) parts.push(`--snapshot-required=${draft.snapshots.required}`);
  for (const event of draft.snapshots.events) parts.push(`--snapshot-events=${shell(event)}`);
  if (draft.payloadArgs.length) parts.push("--", ...draft.payloadArgs.map(shell));
  $("commandPreview").textContent = parts.join(" ");
}

function setStatus(message, tone = "") {
  $("formStatus").textContent = message;
  if (tone) $("formStatus").dataset.tone = tone;
  else delete $("formStatus").dataset.tone;
}

const networkModeLabels = {
  svc: "Service",
  ts: "Tailscale",
  lan: "LAN",
};

function renderNetworkModes(modes) {
  const rows = modes.map((mode) => {
    const label = document.createElement("label");
    label.className = "check-pill";
    const input = document.createElement("input");
    input.type = "checkbox";
    input.name = "net";
    input.value = mode;
    const span = document.createElement("span");
    span.textContent = networkModeLabels[mode] || mode;
    label.append(input, span);
    return label;
  });
  $("networkModes").replaceChildren(...rows);
}

function syncNetworkUI() {
  const modes = selectedNetworkModes();
  $("hostDefault").checked = modes.length === 0;
  $("tsOptions").hidden = !modes.includes("ts");
  $("lanOptions").hidden = !modes.includes("lan");
}

function updateServiceRootPlaceholder() {
  const service = $("service").value.trim() || "<service>";
  $("serviceRoot").placeholder = $("zfs").checked ? `tank/apps/${service}` : `/root/data/services/${service}`;
}

function renderSnapshotModes(modes) {
  const select = $("snapshots");
  const options = modes.length ? modes : ["inherit", "on", "off"];
  select.replaceChildren(...options.map((mode) => {
    const option = document.createElement("option");
    option.value = mode === "inherit" ? "" : mode;
    option.textContent = mode;
    return option;
  }));
}

function renderSnapshotRequiredOptions() {
  $("snapshotRequired").replaceChildren(
    option("inherit", ""),
    option("required", "true"),
    option("optional", "false"),
  );
}

function option(label, value) {
  const item = document.createElement("option");
  item.textContent = label;
  item.value = value;
  return item;
}

function parentDir(dir) {
  if (!dir || dir === ".") return ".";
  const parts = dir.split("/").filter(Boolean);
  parts.pop();
  return parts.length ? parts.join("/") : ".";
}

async function loadFiles(dir) {
  const res = await api(`/api/files?dir=${encodeURIComponent(dir)}`);
  if (!res.ok) {
    setStatus(await res.text(), "error");
    return;
  }
  const data = await res.json();
  state.currentDir = data.dir || ".";
  $("browserDir").textContent = state.currentDir;
  $("upButton").disabled = state.currentDir === ".";

  const rows = (data.entries || []).map((entry) => {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "file-row";

    const name = document.createElement("span");
    name.className = "file-name";
    name.textContent = entry.dir ? `${entry.name}/` : entry.name;

    const badge = document.createElement("span");
    badge.className = "badge";
    badge.textContent = entry.likelyEnv ? "env" : entry.likelyPayload ? "payload" : "";

    button.append(name, badge);
    button.addEventListener("click", () => {
      if (entry.dir) {
        loadFiles(entry.path);
        return;
      }
      if (state.activePicker === "envFile") $("envFile").value = entry.path;
      else $("payload").value = entry.path;
      hidePicker();
      update();
    });
    return button;
  });

  if (!rows.length) {
    const empty = document.createElement("div");
    empty.className = "empty-state";
    empty.textContent = "No files in this directory";
    rows.push(empty);
  }
  $("fileBrowser").replaceChildren(...rows);
}

function showPicker(field) {
  state.activePicker = field;
  const input = $(field);
  const picker = $("filePicker");
  const rect = input.getBoundingClientRect();
  picker.style.left = `${Math.max(12, rect.left)}px`;
  picker.style.top = `${Math.min(window.innerHeight - 340, rect.bottom + 6)}px`;
  picker.style.width = `${Math.max(360, rect.width)}px`;
  picker.hidden = false;
}

function hidePicker() {
  state.activePicker = "";
  $("filePicker").hidden = true;
}

async function bootstrap() {
  const res = await api("/api/bootstrap");
  if (!res.ok) throw new Error(await res.text());
  state.bootstrap = await res.json();
  $("projectLabel").textContent = state.bootstrap.cwd || "Project";
  $("configLabel").textContent = state.bootstrap.configPath || "yeet.toml recipe";
  $("host").value = state.bootstrap.selectedHost || "";

  const hostOptions = (state.bootstrap.hosts || []).map((host) => {
    const option = document.createElement("option");
    option.value = host;
    return option;
  });
  $("hostOptions").replaceChildren(...hostOptions);

  $("service").value = state.bootstrap.prefill?.service || "";
  $("payload").value = state.bootstrap.prefill?.payload || "";
  renderNetworkModes(state.bootstrap.options?.networkModes || ["svc", "ts", "lan"]);
  renderSnapshotModes(state.bootstrap.options?.snapshotModes || ["inherit", "on", "off"]);
  renderSnapshotRequiredOptions();
  await loadFiles(".");
  syncNetworkUI();
  updateServiceRootPlaceholder();
  $("service").focus();
  if ($("service").value) $("payload").focus();
  update();
}

function update() {
  if (state.phase !== "editing") return;
  syncNetworkUI();
  updateServiceRootPlaceholder();
  clearValidationErrors();
  const draft = buildDraft();
  updatePreview(draft);
  $("deployButton").disabled = true;
  window.clearTimeout(state.validateTimer);
  state.validateTimer = window.setTimeout(() => validate(draft), 250);
}

function firstValidationMessage(validation) {
  if (validation?.errors?.length) return validation.errors[0].message;
  if (validation?.warnings?.length) return validation.warnings[0].message;
  return "";
}

const validationFieldIDs = {
  service: "service",
  host: "host",
  payload: "payload",
  envFile: "envFile",
  serviceRoot: "serviceRoot",
  "network.modes": "hostDefault",
  "snapshots.mode": "snapshots",
  "snapshots.keepLast": "snapshotKeepLast",
  "snapshots.maxAge": "snapshotMaxAge",
  "snapshots.required": "snapshotRequired",
  "snapshots.events": "snapshotEvents",
};

function clearValidationErrors() {
  document.querySelectorAll("[data-invalid='true']").forEach((el) => {
    delete el.dataset.invalid;
    el.removeAttribute("aria-invalid");
  });
  document.querySelectorAll(".field-error").forEach((el) => {
    el.textContent = "";
  });
}

function applyValidationErrors(validation) {
  clearValidationErrors();
  for (const err of validation?.errors || []) {
    const id = validationFieldIDs[err.field];
    if (!id) continue;
    const input = $(id);
    if (!input) continue;
    input.dataset.invalid = "true";
    input.setAttribute("aria-invalid", "true");
    const message = $(`${id}Error`);
    if (message) message.textContent = err.message;
  }
}

function redactValidationDraft(draft) {
  const copy = JSON.parse(JSON.stringify(draft));
  if (copy.network) delete copy.network.tsAuthKey;
  return copy;
}

async function validate(draft) {
  if (state.phase !== "editing") return;
  const seq = ++state.validateSeq;
  setStatus("Validating");
  try {
    const res = await api("/api/validate", {
      method: "POST",
      body: JSON.stringify(redactValidationDraft(draft)),
    });
    if (seq !== state.validateSeq) return;
    if (state.phase !== "editing") return;
    if (!res.ok) {
      setStatus(await res.text(), "error");
      $("hostStatus").textContent = "";
      return;
    }
    const data = await res.json();
    const ok = Boolean(data.validation?.ok);
    $("deployButton").disabled = !ok;
    $("hostStatus").textContent = ok ? "Ready" : "";
    applyValidationErrors(data.validation);
    if (ok) setStatus("Ready", "ready");
    else setStatus(firstValidationMessage(data.validation) || "Invalid", "error");
  } catch (err) {
    if (seq !== state.validateSeq) return;
    if (state.phase !== "editing") return;
    setStatus(String(err), "error");
    $("hostStatus").textContent = "";
  }
}

async function deploy(event) {
  event.preventDefault();
  if (state.phase !== "editing") return;
  const draft = buildDraft();
  state.phase = "deploying";
  state.validateSeq += 1;
  window.clearTimeout(state.validateTimer);
  $("deployButton").disabled = true;
  setStatus("Deploying");
  try {
    const res = await api("/api/deploy", {
      method: "POST",
      body: JSON.stringify(draft),
    });
    if (res.ok) {
      state.phase = "done";
      setStatus("Done. Close this tab and return to the terminal.", "done");
      $("hostStatus").textContent = "Deployed";
      return;
    }
    const contentType = res.headers.get("Content-Type") || "";
    if (contentType.includes("application/json")) {
      const data = await res.json();
      applyValidationErrors(data.validation);
      setStatus(firstValidationMessage(data.validation) || "Deploy failed", "error");
    } else {
      setStatus(await res.text(), "error");
    }
    state.phase = "editing";
    $("deployButton").disabled = false;
  } catch (err) {
    state.phase = "editing";
    setStatus(String(err), "error");
    $("deployButton").disabled = false;
  }
}

function showTooltip(target) {
  const tip = $("tooltip");
  tip.textContent = target.dataset.help || "";
  target.setAttribute("aria-describedby", "tooltip");
  const rect = target.getBoundingClientRect();
  const left = Math.min(rect.left, window.innerWidth - 332);
  tip.style.left = `${Math.max(12, left)}px`;
  tip.style.top = `${Math.min(window.innerHeight - 72, rect.bottom + 8)}px`;
  tip.hidden = false;
}

function hideTooltip() {
  document.querySelectorAll(".help[aria-describedby='tooltip']").forEach((button) => {
    button.removeAttribute("aria-describedby");
  });
  $("tooltip").hidden = true;
}

document.addEventListener("input", (event) => {
  if (event.target.closest("#deployForm")) update();
});
$("deployForm").addEventListener("submit", deploy);
$("upButton").addEventListener("click", () => loadFiles(parentDir(state.currentDir)));
document.querySelectorAll("[data-picker-target]").forEach((el) => {
  el.addEventListener("focus", () => showPicker(el.dataset.pickerTarget));
  el.addEventListener("click", () => showPicker(el.dataset.pickerTarget));
});
document.addEventListener("click", (event) => {
  if (event.target.closest("#filePicker") || event.target.closest("[data-picker-target]")) return;
  hidePicker();
});
document.addEventListener("focusin", (event) => {
  if (event.target.closest("#filePicker") || event.target.closest("[data-picker-target]")) return;
  hidePicker();
});
document.addEventListener("keydown", (event) => {
  if (event.key === "Escape") hidePicker();
});
document.addEventListener("mouseover", (event) => {
  const target = event.target.closest(".help");
  if (target) showTooltip(target);
});
document.addEventListener("mouseout", (event) => {
  if (event.target.closest(".help")) hideTooltip();
});
document.addEventListener("focusin", (event) => {
  const target = event.target.closest(".help");
  if (target) showTooltip(target);
});
document.addEventListener("focusout", (event) => {
  if (event.target.closest(".help")) hideTooltip();
});

bootstrap().catch((err) => {
  setStatus(String(err), "error");
});
