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
  deployJobId: "",
  deployEvents: null,
  terminal: null,
  workload: "",
  workloadOverride: "",
  networkSelections: {},
  zfsRootSeq: 0,
  zfsRootKey: "",
  zfsRootState: null,
  vmDefaultsSeq: 0,
  vmDefaultsKey: "",
  vmDefaultsState: null,
  vmShapeManual: {
    cpus: false,
    memory: false,
    disk: false,
  },
  pickedZFSRoot: null,
  serviceRootManual: false,
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

const workloadDefinitions = {
  auto: {
    payloadKind: "",
    payloadLabel: "Payload",
    payloadHelp: "Let yeet detect whether this is a local image name, image reference, or project file.",
    sourceHint: "Yeet will detect the payload type.",
    placeholder: "alpine",
    networkModes: ["host", "svc", "ts", "lan"],
    defaultModes: [],
  },
  compose: {
    payloadKind: "compose",
    payloadLabel: "Compose file",
    payloadHelp: "A compose.yml or docker-compose.yml file in this project.",
    sourceHint: "Choose a Compose file.",
    placeholder: "compose.yml",
    networkModes: ["host", "svc", "ts", "lan"],
    defaultModes: [],
  },
  vm: {
    payloadKind: "vm",
    payloadLabel: "VM image",
    payloadHelp: "A managed catalog VM image, or a manual vm:// reference under advanced.",
    sourceHint: "Choose a catalog image.",
    placeholder: "vm://ubuntu/26.04",
    networkModes: ["svc", "lan"],
    defaultModes: ["svc"],
  },
  dockerfile: {
    payloadKind: "dockerfile",
    payloadLabel: "Dockerfile",
    payloadHelp: "Dockerfile to build and deploy.",
    sourceHint: "Choose a Dockerfile.",
    placeholder: "Dockerfile",
    networkModes: ["host", "svc", "ts", "lan"],
    defaultModes: [],
  },
  "remote-image": {
    payloadKind: "remote-image",
    payloadLabel: "Image",
    payloadHelp: "Container image reference such as ghcr.io/example/app:latest.",
    sourceHint: "Enter an image reference.",
    placeholder: "ghcr.io/example/app:latest",
    networkModes: ["host", "svc", "ts", "lan"],
    defaultModes: [],
  },
  file: {
    payloadKind: "file",
    payloadLabel: "Binary/script",
    payloadHelp: "Local binary or script to upload and run.",
    sourceHint: "Choose a local executable or script.",
    placeholder: "./run.sh",
    networkModes: ["host", "svc", "ts", "lan"],
    defaultModes: [],
  },
  cron: {
    payloadKind: "cron",
    payloadLabel: "Job file",
    payloadHelp: "Local binary or script to install as a scheduled job.",
    sourceHint: "Choose a job file and schedule.",
    placeholder: "./job.sh",
    networkModes: ["host"],
    defaultModes: [],
  },
};

function selectedWorkload() {
  return state.workloadOverride || document.querySelector("input[name='workload']:checked")?.value || "compose";
}

function workloadDefinition(workload = selectedWorkload()) {
  return workloadDefinitions[workload] || workloadDefinitions.compose;
}

function workloadPayloadKind(workload) {
  return workloadDefinition(workload).payloadKind;
}

function defaultNetworkModesForWorkload(workload) {
  return [...workloadDefinition(workload).defaultModes];
}

function payloadPickerEnabledForWorkload(workload) {
  return workload !== "vm" && workload !== "remote-image";
}

function sourcePayloadForWorkload(workload) {
  if (workload === "vm") {
    const manual = $("manualVMSource").value.trim();
    return manual || $("vmCatalog").value.trim();
  }
  return $("payload").value.trim();
}

function inferWorkloadForPayload(payload) {
  const trimmed = payload.trim();
  const name = trimmed.split("/").pop();
  const lowerName = name.toLowerCase();
  if (!trimmed) return "compose";
  if (isVMPayload(trimmed)) return "vm";
  if (lowerName === "dockerfile" || lowerName.startsWith("dockerfile.")) return "dockerfile";
  if (["compose.yml", "compose.yaml", "docker-compose.yml", "docker-compose.yaml"].includes(lowerName)) {
    return "compose";
  }
  if (trimmed.startsWith("./") || trimmed.startsWith("../") || trimmed.startsWith("/")) return "file";
  if (name.includes(".") && !name.includes(":")) return "file";
  if (looksLikeRemoteImageReference(trimmed)) return "remote-image";
  return "auto";
}

function looksLikeRemoteImageReference(payload) {
  if (payload.includes("\\") || /\s/.test(payload)) return false;
  if (payload.startsWith("http://") || payload.startsWith("https://")) return false;
  if (payload.includes("@")) {
    const parts = payload.split("@", 2);
    return Boolean(parts[0] && parts[1]);
  }
  const lastSlash = payload.lastIndexOf("/");
  const lastColon = payload.lastIndexOf(":");
  return lastColon > lastSlash && payload.slice(lastColon + 1) !== "";
}

function buildDraft() {
  const restart = true;
  // Future redeploy support can add pull/force once the web flow allows existing services.
  const workload = selectedWorkload();
  const payload = sourcePayloadForWorkload(workload);
  const payloadKind = workloadPayloadKind(workload);
  const vmPayload = payloadKind === "vm";
  const cronPayload = payloadKind === "cron";
  const envFile = vmPayload || cronPayload ? "" : $("envFile").value.trim();
  const publish = vmPayload || cronPayload ? [] : splitCSV($("publish").value);
  const snapshotRequired = snapshotRequiredValue();
  const modes = selectedNetworkModes();
  const hasTailscale = modes.includes("ts");
  const hasLAN = modes.includes("lan");
  return {
    service: $("service").value.trim(),
    host: $("host").value.trim(),
    payload,
    payloadKind,
    envFile,
    payloadArgs: vmPayload ? [] : splitArgs($("payloadArgs").value),
    cron: cronPayload ? {
      schedule: $("cronSchedule").value.trim(),
    } : {},
    vm: vmPayload ? {
      cpus: Number.parseInt($("vmCPUs").value, 10) || 0,
      memory: $("vmMemory").value.trim(),
      disk: $("vmDisk").value.trim(),
    } : {},
    network: cronPayload ? {} : {
      modes,
      tsVersion: hasTailscale ? $("tsVersion").value.trim() : "",
      tsExitNode: hasTailscale ? $("tsExitNode").value.trim() : "",
      tsTags: hasTailscale ? splitCSV($("tsTags").value) : [],
      tsAuthKey: hasTailscale ? $("tsAuthKey").value : "",
      macvlanMac: hasLAN ? $("macvlanMac").value.trim() : "",
      macvlanVlan: hasLAN ? Number.parseInt($("macvlanVlan").value, 10) || 0 : 0,
      macvlanParent: hasLAN ? $("macvlanParent").value.trim() : "",
      publish,
      restart,
    },
    storage: cronPayload ? {} : {
      serviceRoot: $("serviceRoot").value.trim(),
      zfs: $("zfs").checked,
    },
    snapshots: cronPayload ? {} : {
      mode: $("snapshots").value,
      keepLast: Number.parseInt($("snapshotKeepLast").value, 10) || 0,
      maxAge: $("snapshotMaxAge").value.trim(),
      required: snapshotRequired,
      events: splitCSV($("snapshotEvents").value),
    },
  };
}

function isVMPayload(payload) {
  return payload.trim().startsWith("vm://");
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
  const parts = draft.payloadKind === "cron" ? ["yeet", "cron"] : ["yeet", "run"];
  const serviceTarget = draft.service && draft.host && !draft.service.includes("@")
    ? `${draft.service}@${draft.host}`
    : draft.service;
  if (serviceTarget) parts.push(shell(serviceTarget));
  if (draft.payload) parts.push(shell(draft.payload));
  if (draft.payloadKind === "cron") {
    if (draft.cron?.schedule) parts.push(shell(draft.cron.schedule));
    if (draft.payloadArgs.length) parts.push("--", ...draft.payloadArgs.map(shell));
    $("commandPreview").textContent = parts.join(" ");
    return;
  }
  const network = draft.network || {};
  const storage = draft.storage || {};
  const snapshots = draft.snapshots || {};
  if (draft.envFile) parts.push(`--env-file=${shell(draft.envFile)}`);
  for (const mode of network.modes || []) parts.push(`--net=${shell(mode)}`);
  if (network.tsVersion) parts.push(`--ts-ver=${shell(network.tsVersion)}`);
  if (network.tsExitNode) parts.push(`--ts-exit=${shell(network.tsExitNode)}`);
  for (const tag of network.tsTags || []) parts.push(`--ts-tags=${shell(tag)}`);
  if (network.tsAuthKey) parts.push("--ts-auth-key=<hidden>");
  if (network.macvlanParent) parts.push(`--macvlan-parent=${shell(network.macvlanParent)}`);
  if (network.macvlanVlan) parts.push(`--macvlan-vlan=${network.macvlanVlan}`);
  if (network.macvlanMac) parts.push(`--macvlan-mac=${shell(network.macvlanMac)}`);
  for (const port of network.publish || []) parts.push(`--publish=${shell(port)}`);
  if (storage.serviceRoot) parts.push(`--service-root=${shell(storage.serviceRoot)}`);
  if (storage.zfs) parts.push("--zfs");
  if (draft.vm?.cpus) parts.push(`--cpus=${draft.vm.cpus}`);
  if (draft.vm?.memory) parts.push(`--memory=${shell(draft.vm.memory)}`);
  if (draft.vm?.disk) parts.push(`--disk=${shell(draft.vm.disk)}`);
  if (snapshots.mode) parts.push(`--snapshots=${shell(snapshots.mode)}`);
  if (snapshots.keepLast) parts.push(`--snapshot-keep-last=${snapshots.keepLast}`);
  if (snapshots.maxAge) parts.push(`--snapshot-max-age=${shell(snapshots.maxAge)}`);
  if (snapshots.required !== undefined) parts.push(`--snapshot-required=${snapshots.required}`);
  for (const event of snapshots.events || []) parts.push(`--snapshot-events=${shell(event)}`);
  if (draft.payloadArgs.length) parts.push("--", ...draft.payloadArgs.map(shell));
  $("commandPreview").textContent = parts.join(" ");
}

function setStatus(message, tone = "") {
  $("formStatus").textContent = message;
  if (tone) $("formStatus").dataset.tone = tone;
  else delete $("formStatus").dataset.tone;
}

function setTerminalStatus(message, tone = "") {
  $("terminalStatus").textContent = message;
  if (tone) $("terminalStatus").dataset.tone = tone;
  else delete $("terminalStatus").dataset.tone;
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

function renderVMCatalog(images) {
  const rows = images.length ? images : [{ payload: "vm://ubuntu/26.04", label: "Ubuntu 26.04" }];
  $("vmCatalog").replaceChildren(...rows.map((image) => option(image.label, image.payload)));
}

function syncWorkloadUI() {
  const workload = selectedWorkload();
  const def = workloadDefinition(workload);
  const previousWorkload = state.workload;
  const workloadChanged = previousWorkload !== workload;
  if (previousWorkload && workloadChanged) {
    state.networkSelections[previousWorkload] = selectedNetworkModes();
  }
  state.workload = workload;
  const isVM = workload === "vm";
  const isCron = workload === "cron";
  $("sourceHint").textContent = def.sourceHint;
  $("payloadLabel").firstChild.textContent = `${def.payloadLabel} `;
  $("payloadLabel").querySelector(".help").dataset.help = def.payloadHelp;
  $("payload").placeholder = def.placeholder;
  $("payload").closest("label").hidden = isVM;
  const payloadPickerEnabled = payloadPickerEnabledForWorkload(workload);
  $("payloadPicker").hidden = !payloadPickerEnabled;
  if (payloadPickerEnabled) $("payload").setAttribute("aria-haspopup", "listbox");
  else $("payload").removeAttribute("aria-haspopup");
  if (!payloadPickerEnabled && state.activePicker === "payload") hidePicker();
  $("vmCatalogBlock").hidden = !isVM;
  $("cronScheduleField").hidden = !isCron;
  $("envFile").closest("label").hidden = isVM || isCron;
  if ((isVM || isCron) && state.activePicker === "envFile") hidePicker();
  $("publish").closest("label").hidden = isVM || isCron;
  $("serviceRoot").disabled = isCron;
  $("zfs").disabled = isCron;
  $("snapshots").closest("details").hidden = isCron;
  const zfsChecked = $("zfs").checked;
  const storageHelp = $("storageModeLabel").querySelector(".help");
  if (zfsChecked && isVM) {
    $("storageModeLabel").firstChild.textContent = "VM ZVOL parent ";
    storageHelp.dataset.help = "Enter a ZFS dataset name that will contain this VM's zvols, without a leading or trailing slash.";
  } else if (zfsChecked) {
    $("storageModeLabel").firstChild.textContent = "ZFS dataset ";
    storageHelp.dataset.help = "Enter a ZFS dataset name for this service, without a leading or trailing slash.";
  } else {
    $("storageModeLabel").firstChild.textContent = "Service root ";
    storageHelp.dataset.help = "Leave empty to use the catch default root, or enter an absolute filesystem path.";
  }
  $("zfsHelp").textContent = "ZFS";
  if (workloadChanged) {
    renderNetworkModes(def.networkModes.filter((mode) => mode !== "host"));
    applyDefaultNetworkModes(workload);
  }
}

function applyDefaultNetworkModes(workload) {
  const defaults = state.networkSelections[workload] || defaultNetworkModesForWorkload(workload);
  document.querySelectorAll("input[name='net']").forEach((input) => {
    input.checked = defaults.includes(input.value);
  });
}

function renderHostPicker(hosts) {
  const current = $("host").value.trim();
  const rows = hosts.map((host) => {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "host-option";
    button.setAttribute("role", "option");
    button.setAttribute("aria-selected", String(host === current));
    button.textContent = host;
    button.addEventListener("click", () => {
      $("host").value = host;
      hideHostPicker();
      update();
    });
    return button;
  });
  if (!rows.length) {
    const empty = document.createElement("div");
    empty.className = "empty-state";
    empty.textContent = "No hosts found";
    rows.push(empty);
  }
  $("hostPicker").replaceChildren(...rows);
}

function showHostPicker() {
  renderHostPicker(state.bootstrap?.hosts || []);
  $("hostPicker").hidden = false;
  $("host").setAttribute("aria-expanded", "true");
}

function hideHostPicker() {
  $("hostPicker").hidden = true;
  $("host").setAttribute("aria-expanded", "false");
}

function syncNetworkUI() {
  const payloadKind = workloadPayloadKind(selectedWorkload());
  const vmPayload = payloadKind === "vm";
  document.querySelectorAll("input[name='net']").forEach((input) => {
    if (input.value === "ts") {
      input.disabled = vmPayload;
      if (vmPayload) input.checked = false;
    }
  });
  const modes = selectedNetworkModes();
  $("hostDefault").disabled = true;
  $("hostDefault").checked = modes.length === 0;
  $("tsOptions").hidden = !modes.includes("ts");
  $("lanOptions").hidden = !modes.includes("lan");
  $("vmOptions").hidden = !vmPayload;
  $("payloadArgsBlock").hidden = vmPayload;
}

function updateServiceRootPlaceholder() {
  const service = $("service").value.trim() || "<service>";
  $("serviceRoot").placeholder = $("zfs").checked ? `<dataset>/${service}` : `/root/data/services/${service}`;
}

function zfsRootPickerEnabled() {
  return $("zfs").checked && !$("zfs").disabled && selectedWorkload() !== "cron";
}

function zfsRootRequestKey() {
  return [
    $("host").value.trim(),
    selectedWorkload(),
  ].join("\n");
}

function suggestedZFSServiceDataset(root, service) {
  const dataset = root.trim().replace(/\/+$/, "");
  const name = service.trim();
  if (!dataset) return "";
  return name ? `${dataset}/${name}` : `${dataset}/`;
}

function syncPickedZFSRootValue() {
  if (!state.pickedZFSRoot || state.serviceRootManual) return;
  const host = $("host").value.trim();
  const workload = selectedWorkload();
  if (state.pickedZFSRoot.host !== host || state.pickedZFSRoot.workload !== workload) return;
  $("serviceRoot").value = suggestedZFSServiceDataset(state.pickedZFSRoot.dataset, $("service").value);
}

function clearPickedZFSRootForContextChange() {
  if (!state.pickedZFSRoot) return;
  const host = $("host").value.trim();
  const workload = selectedWorkload();
  if (state.pickedZFSRoot.host === host && state.pickedZFSRoot.workload === workload) return;
  state.pickedZFSRoot = null;
}

function syncZFSRootPicker() {
  clearPickedZFSRootForContextChange();
  const button = $("zfsRootPickerButton");
  const input = $("serviceRoot");
  if (!zfsRootPickerEnabled()) {
    button.hidden = true;
    input.removeAttribute("aria-haspopup");
    input.setAttribute("aria-expanded", "false");
    hideZFSRootPicker();
    state.zfsRootKey = "";
    return;
  }
  button.hidden = false;
  input.setAttribute("aria-haspopup", "listbox");
  syncPickedZFSRootValue();
  const key = zfsRootRequestKey();
  if (key === state.zfsRootKey) return;
  state.zfsRootKey = key;
  loadZFSRoots(key);
}

function vmDefaultsEnabled() {
  return workloadPayloadKind(selectedWorkload()) === "vm";
}

function vmDefaultsRequestKey() {
  return [
    $("host").value.trim(),
    $("service").value.trim(),
    $("serviceRoot").value.trim(),
    String($("zfs").checked),
  ].join("\n");
}

function syncVMDefaults() {
  if (!vmDefaultsEnabled()) {
    state.vmDefaultsKey = "";
    state.vmDefaultsState = null;
    return;
  }
  const key = vmDefaultsRequestKey();
  if (key === state.vmDefaultsKey) return;
  state.vmDefaultsKey = key;
  loadVMDefaults(key);
}

async function loadVMDefaults(key) {
  const seq = ++state.vmDefaultsSeq;
  const [host, service, serviceRoot, zfs] = key.split("\n");
  if (!host) {
    state.vmDefaultsState = { state: "error", warnings: ["Choose a host"] };
    return;
  }
  state.vmDefaultsState = { state: "loading" };
  const query = new URLSearchParams({ host, service, serviceRoot, zfs });
  try {
    const res = await api(`/api/vm-defaults?${query}`);
    if (seq !== state.vmDefaultsSeq) return;
    if (!res.ok) throw new Error(await res.text());
    state.vmDefaultsState = await res.json();
    applyVMDefaults(state.vmDefaultsState);
    update();
  } catch (err) {
    if (seq !== state.vmDefaultsSeq) return;
    state.vmDefaultsState = { state: "error", warnings: [String(err)] };
  }
}

function applyVMDefaults(response) {
  if (response?.state !== "available") return;
  const defaults = response.defaults || {};
  if (!state.vmShapeManual.cpus && defaults.cpus) $("vmCPUs").value = String(defaults.cpus);
  if (!state.vmShapeManual.memory && defaults.memory) $("vmMemory").value = defaults.memory;
  if (!state.vmShapeManual.disk && defaults.disk) $("vmDisk").value = defaults.disk;
}

async function loadZFSRoots(key) {
  const seq = ++state.zfsRootSeq;
  const [host, workload] = key.split("\n");
  if (!host) {
    state.zfsRootState = { state: "error", warnings: ["Choose a host"] };
    renderZFSRootCandidates(state.zfsRootState);
    return;
  }
  state.zfsRootState = { state: "loading" };
  renderZFSRootCandidates(state.zfsRootState);
  const service = $("service").value.trim();
  const query = new URLSearchParams({ host, workload, service });
  try {
    const res = await api(`/api/zfs-roots?${query}`);
    if (seq !== state.zfsRootSeq) return;
    if (!res.ok) throw new Error(await res.text());
    state.zfsRootState = await res.json();
  } catch (err) {
    if (seq !== state.zfsRootSeq) return;
    state.zfsRootState = { state: "error", warnings: [String(err)] };
  }
  renderZFSRootCandidates(state.zfsRootState);
}

function renderZFSRootCandidates(response) {
  const list = $("zfsRootList");
  $("zfsRootStatus").textContent = zfsRootStatusText(response);
  if (!response || response.state === "loading") {
    list.replaceChildren(emptyZFSRootState("Checking host"));
    return;
  }
  if (response.state !== "available") {
    list.replaceChildren(emptyZFSRootState(zfsRootDetailText(response)));
    return;
  }
  const candidates = response.candidates || [];
  if (!candidates.length) {
    list.replaceChildren(emptyZFSRootState("No suggested roots"));
    return;
  }
  list.replaceChildren(...candidates.slice(0, 6).map(zfsRootRow));
}

function showZFSRootPicker() {
  if (!zfsRootPickerEnabled()) {
    hideZFSRootPicker();
    return;
  }
  hidePicker();
  syncZFSRootPicker();
  state.activePicker = "zfsRoot";
  const input = $("serviceRoot");
  const picker = $("zfsRootPicker");
  const rect = input.getBoundingClientRect();
  picker.style.left = `${Math.max(12, rect.left)}px`;
  picker.style.top = `${Math.min(window.innerHeight - 320, rect.bottom + 6)}px`;
  picker.style.width = `${Math.max(360, Math.min(520, rect.width))}px`;
  picker.hidden = false;
  input.setAttribute("aria-expanded", "true");
  $("zfsRootPickerButton").setAttribute("aria-expanded", "true");
}

function hideZFSRootPicker() {
  if (state.activePicker === "zfsRoot") state.activePicker = "";
  $("zfsRootPicker").hidden = true;
  $("serviceRoot").setAttribute("aria-expanded", "false");
  $("zfsRootPickerButton").setAttribute("aria-expanded", "false");
}

function zfsRootStatusText(response) {
  switch (response?.state) {
    case "available":
      return "Available";
    case "loading":
      return "Checking";
    case "zfs-missing":
      return "ZFS unavailable";
    case "no-filesystems":
      return "No filesystems";
    case "unsupported-rpc":
      return "Upgrade catch";
    case "host-unreachable":
      return "Host unreachable";
    case "error":
      return "Unavailable";
    default:
      return "";
  }
}

function zfsRootDetailText(response) {
  if (response?.warnings?.length) return response.warnings[0];
  switch (response?.state) {
    case "zfs-missing":
      return "This host does not have ZFS available.";
    case "no-filesystems":
      return "No mounted ZFS filesystems were found.";
    case "unsupported-rpc":
      return "This catch host needs an upgrade for root suggestions.";
    case "host-unreachable":
      return "The selected host is not reachable.";
    case "error":
      return "ZFS root suggestions are unavailable.";
    default:
      return "";
  }
}

function emptyZFSRootState(message) {
  const empty = document.createElement("div");
  empty.className = "zfs-root-empty";
  empty.textContent = message;
  return empty;
}

function zfsRootRow(candidate) {
  const button = document.createElement("button");
  button.type = "button";
  button.className = "zfs-root-row";
  button.setAttribute("role", "option");
  button.setAttribute("aria-selected", String(state.pickedZFSRoot?.dataset === candidate.dataset));

  const main = document.createElement("span");
  main.className = "zfs-root-main";
  main.textContent = zfsRootDisplayDataset(candidate);

  const meta = document.createElement("span");
  meta.className = "zfs-root-meta";
  meta.textContent = zfsRootMeta(candidate);

  button.append(main, meta);
  button.addEventListener("click", () => pickZFSRootCandidate(candidate));
  return button;
}

function zfsRootDisplayDataset(candidate) {
  const dataset = (candidate.dataset || "").trim().replace(/\/+$/, "");
  return dataset ? `${dataset}/` : "";
}

function zfsRootMeta(candidate) {
  const parts = [];
  const mountpoint = zfsRootMountpointMeta(candidate);
  if (mountpoint) parts.push(mountpoint);
  const free = formatBytes(candidate.freeBytes);
  if (free) parts.push(`${free} free`);
  if (candidate.vmChildCount) parts.push(countLabel(candidate.vmChildCount, "VM"));
  else if (candidate.serviceChildCount) parts.push(countLabel(candidate.serviceChildCount, "service"));
  else if (candidate.childCount) parts.push(countLabel(candidate.childCount, "child", "children"));
  return parts.join(", ");
}

function zfsRootMountpointMeta(candidate) {
  const mountpoint = (candidate.mountpoint || "").trim().replace(/\/+$/, "");
  const dataset = (candidate.dataset || "").trim().replace(/\/+$/, "");
  if (!mountpoint) return "";
  if (dataset && mountpoint === `/${dataset}`) return "";
  return mountpoint;
}

function countLabel(count, singular, plural = `${singular}s`) {
  return `${count} ${count === 1 ? singular : plural}`;
}

function formatBytes(bytes) {
  if (!Number.isFinite(bytes) || bytes <= 0) return "";
  const units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  const rounded = value >= 10 || unit === 0 ? Math.round(value) : Math.round(value * 10) / 10;
  return `${rounded} ${units[unit]}`;
}

function pickZFSRootCandidate(candidate) {
  state.pickedZFSRoot = {
    dataset: candidate.dataset,
    host: $("host").value.trim(),
    workload: selectedWorkload(),
  };
  state.serviceRootManual = false;
  $("serviceRoot").value = suggestedZFSServiceDataset(candidate.dataset, $("service").value);
  renderZFSRootCandidates(state.zfsRootState);
  hideZFSRootPicker();
  update();
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
  if (!pickerEnabledForField(field)) {
    if (state.activePicker === field) hidePicker();
    return;
  }
  state.activePicker = field;
  const input = $(field);
  const picker = $("filePicker");
  const rect = input.getBoundingClientRect();
  picker.style.left = `${Math.max(12, rect.left)}px`;
  picker.style.top = `${Math.min(window.innerHeight - 340, rect.bottom + 6)}px`;
  picker.style.width = `${Math.max(360, rect.width)}px`;
  picker.hidden = false;
}

function pickerEnabledForField(field) {
  if (field === "payload") {
    return payloadPickerEnabledForWorkload(selectedWorkload()) && !$("payload").closest("label").hidden;
  }
  if (field === "envFile") {
    return !$("envFile").closest("label").hidden;
  }
  return true;
}

function hidePicker() {
  if (state.activePicker !== "zfsRoot") state.activePicker = "";
  $("filePicker").hidden = true;
}

function createTerminalRenderer(output) {
  const decoder = new TextDecoder();
  let lines = [""];
  let row = 0;
  let col = 0;
  let ansiState = "normal";
  let csi = "";

  function ensureLine() {
    while (lines.length <= row) lines.push("");
  }

  function eraseLine(mode) {
    ensureLine();
    if (mode === 1) {
      lines[row] = `${" ".repeat(Math.min(col, lines[row].length))}${lines[row].slice(col)}`;
      return;
    }
    if (mode === 2) {
      lines[row] = "";
      return;
    }
    lines[row] = lines[row].slice(0, col);
  }

  function eraseDisplay(mode) {
    ensureLine();
    if (mode === 2 || mode === 3) {
      lines = [""];
      row = 0;
      col = 0;
      return;
    }
    if (mode === 1) {
      for (let i = 0; i < row; i += 1) lines[i] = "";
      lines[row] = `${" ".repeat(Math.min(col, lines[row].length))}${lines[row].slice(col)}`;
      return;
    }
    lines[row] = lines[row].slice(0, col);
    lines = lines.slice(0, row + 1);
  }

  function numbers(raw) {
    return raw
      .replace(/^\?/, "")
      .split(";")
      .filter((part) => part !== "")
      .map((part) => Number.parseInt(part, 10))
      .filter((num) => !Number.isNaN(num));
  }

  function firstNumber(raw, fallback) {
    const parsed = numbers(raw);
    return parsed.length ? parsed[0] : fallback;
  }

  function handleCSI(sequence) {
    const command = sequence[sequence.length - 1];
    const raw = sequence.slice(0, -1);
    const parsed = numbers(raw);
    const amount = parsed.length ? parsed[0] : 1;
    switch (command) {
      case "A":
        row = Math.max(0, row - amount);
        break;
      case "B":
        row += amount;
        ensureLine();
        break;
      case "C":
        col += amount;
        break;
      case "D":
        col = Math.max(0, col - amount);
        break;
      case "E":
        row += amount;
        col = 0;
        ensureLine();
        break;
      case "F":
        row = Math.max(0, row - amount);
        col = 0;
        break;
      case "G":
        col = Math.max(0, firstNumber(raw, 1) - 1);
        break;
      case "H":
      case "f":
        row = Math.max(0, (parsed[0] || 1) - 1);
        col = Math.max(0, (parsed[1] || 1) - 1);
        ensureLine();
        break;
      case "J":
        eraseDisplay(parsed.length ? parsed[0] : 0);
        break;
      case "K":
        eraseLine(parsed.length ? parsed[0] : 0);
        break;
      default:
        break;
    }
  }

  function writeChar(char) {
    ensureLine();
    const line = lines[row];
    const padded = col > line.length ? `${line}${" ".repeat(col - line.length)}` : line;
    lines[row] = `${padded.slice(0, col)}${char}${padded.slice(col + 1)}`;
    col += 1;
  }

  function applyChar(char) {
    if (ansiState === "normal") {
      if (char === "\x1B") {
        ansiState = "esc";
        return;
      }
      if (char === "\r") {
        col = 0;
        return;
      }
      if (char === "\n") {
        row += 1;
        col = 0;
        ensureLine();
        return;
      }
      if (char === "\b") {
        col = Math.max(0, col - 1);
        return;
      }
      if (char === "\t") {
        const spaces = 8 - (col % 8);
        for (let i = 0; i < spaces; i += 1) writeChar(" ");
        return;
      }
      if (char >= " ") writeChar(char);
      return;
    }
    if (ansiState === "esc") {
      if (char === "[") {
        csi = "";
        ansiState = "csi";
        return;
      }
      if (char === "]") {
        ansiState = "osc";
        return;
      }
      ansiState = "normal";
      return;
    }
    if (ansiState === "csi") {
      csi += char;
      if (char >= "@" && char <= "~") {
        handleCSI(csi);
        csi = "";
        ansiState = "normal";
      }
      return;
    }
    if (ansiState === "osc") {
      if (char === "\x07") {
        ansiState = "normal";
        return;
      }
      if (char === "\x1B") ansiState = "oscEsc";
      return;
    }
    if (ansiState === "oscEsc") {
      ansiState = char === "\\" ? "normal" : "osc";
    }
  }

  function render(shouldStick) {
    output.textContent = lines.join("\n");
    if (shouldStick) output.scrollTop = output.scrollHeight;
  }

  function applyText(text) {
    for (const char of text) applyChar(char);
  }

  return {
    write(bytes) {
      const shouldStick = output.scrollTop + output.clientHeight >= output.scrollHeight - 8;
      applyText(decoder.decode(bytes, { stream: true }));
      render(shouldStick);
    },
    clear() {
      lines = [""];
      row = 0;
      col = 0;
      ansiState = "normal";
      csi = "";
      output.textContent = "";
      output.scrollTop = 0;
    },
    text() {
      return lines.join("\n");
    },
  };
}

function showTerminal(draft) {
  if (!state.terminal) state.terminal = createTerminalRenderer($("terminalOutput"));
  state.terminal.clear();
  $("terminalSheet").hidden = false;
  document.body.dataset.terminalVisible = "true";
  $("terminalSubtitle").textContent = `${draft.service || "service"} on ${draft.host || "host"}`;
  setTerminalStatus("Connecting", "");
}

function collapseTerminal() {
  const sheet = $("terminalSheet");
  sheet.dataset.expanded = "false";
  $("terminalExpand").textContent = "Expand";
  $("terminalExpand").setAttribute("aria-expanded", "false");
}

function setDeployMode(enabled) {
  document.querySelectorAll("#deployForm input, #deployForm select, #deployForm button").forEach((el) => {
    if (el.id === "deployButton") return;
    el.disabled = enabled;
  });
}

function decodeOutputChunk(data) {
  const payload = JSON.parse(data);
  if (payload.encoding === "base64") {
    const raw = window.atob(payload.chunk || "");
    const bytes = new Uint8Array(raw.length);
    for (let i = 0; i < raw.length; i += 1) bytes[i] = raw.charCodeAt(i);
    return bytes;
  }
  return new TextEncoder().encode(payload.chunk || "");
}

function closeDeployStream() {
  if (!state.deployEvents) return;
  state.deployEvents.close();
  state.deployEvents = null;
}

function finishDeployStream(status) {
  closeDeployStream();
  if (status.state === "succeeded") {
    state.phase = "done";
    setDeployMode(true);
    $("deployButton").disabled = true;
    setTerminalStatus("Deployed", "done");
    setStatus("Deployed. Close this tab and return to the terminal.", "done");
    $("hostStatus").textContent = "Deployed";
    return;
  }
  if (status.state === "failed") {
    state.phase = "editing";
    setDeployMode(false);
    collapseTerminal();
    setTerminalStatus("Failed", "error");
    $("hostStatus").textContent = "";
    update();
    setStatus(status.error || "Deploy failed. Fix the form and retry.", "error");
  }
}

async function checkDeployStatus(jobId) {
  const res = await api(`/api/deploy/${encodeURIComponent(jobId)}/status`);
  if (!res.ok) throw new Error(await res.text());
  return res.json();
}

async function recoverDeployStream(jobId) {
  closeDeployStream();
  setTerminalStatus("Output stream lost", "error");
  try {
    const status = await checkDeployStatus(jobId);
    if (status.state === "succeeded" || status.state === "failed") {
      finishDeployStream(status);
      return;
    }
  } catch {
    // Fall through to unlock the form when recovery status cannot be fetched.
  }
  if (state.phase !== "deploying") return;
  state.phase = "editing";
  setDeployMode(false);
  $("hostStatus").textContent = "";
  update();
  setStatus("Output stream was lost; the terminal deploy may still be running. Edit and retry if needed.", "error");
}

function followDeployStream(jobId) {
  closeDeployStream();
  const events = new EventSource(`/api/deploy/${encodeURIComponent(jobId)}/stream`);
  state.deployEvents = events;
  events.addEventListener("open", () => {
    setTerminalStatus("Streaming", "ready");
  });
  events.addEventListener("output", (event) => {
    try {
      state.terminal.write(decodeOutputChunk(event.data));
    } catch (err) {
      setTerminalStatus(`Output decode failed: ${err}`, "error");
    }
  });
  events.addEventListener("status", (event) => {
    const status = JSON.parse(event.data);
    finishDeployStream(status);
  });
  events.addEventListener("error", () => {
    if (state.phase !== "deploying") return;
    recoverDeployStream(jobId);
  });
}

async function bootstrap() {
  const res = await api("/api/bootstrap");
  if (!res.ok) throw new Error(await res.text());
  state.bootstrap = await res.json();
  $("projectLabel").textContent = state.bootstrap.cwd || "Project";
  $("configLabel").textContent = state.bootstrap.configPath || "yeet.toml recipe";
  $("host").value = state.bootstrap.selectedHost || "";

  renderHostPicker(state.bootstrap.hosts || []);

  $("service").value = state.bootstrap.prefill?.service || "";
  const prefillPayload = state.bootstrap.prefill?.payload || "";
  $("payload").value = prefillPayload;
  renderVMCatalog(state.bootstrap.options?.vmImages || []);
  const inferredWorkload = inferWorkloadForPayload(prefillPayload);
  state.workloadOverride = inferredWorkload === "auto" ? "auto" : "";
  if (state.workloadOverride) {
    document.querySelectorAll("input[name='workload']").forEach((input) => {
      input.checked = false;
    });
  }
  const workloadInput = document.querySelector(`input[name='workload'][value='${inferredWorkload}']`);
  if (workloadInput) workloadInput.checked = true;
  if (inferredWorkload === "vm") {
    const catalogValues = [...$("vmCatalog").options].map((item) => item.value);
    if (catalogValues.includes(prefillPayload)) $("vmCatalog").value = prefillPayload;
    else $("manualVMSource").value = prefillPayload;
  }
  renderNetworkModes(state.bootstrap.options?.networkModes || ["svc", "ts", "lan"]);
  renderSnapshotModes(state.bootstrap.options?.snapshotModes || ["inherit", "on", "off"]);
  renderSnapshotRequiredOptions();
  await loadFiles(".");
  syncWorkloadUI();
  syncNetworkUI();
  updateServiceRootPlaceholder();
  syncZFSRootPicker();
  syncVMDefaults();
  $("service").focus();
  if ($("service").value && !$("payload").closest("label").hidden) $("payload").focus();
  update();
}

function update() {
  if (state.phase !== "editing") return;
  syncWorkloadUI();
  syncNetworkUI();
  updateServiceRootPlaceholder();
  syncZFSRootPicker();
  syncVMDefaults();
  clearValidationErrors();
  const draft = buildDraft();
  updatePreview(draft);
  $("deployButton").disabled = true;
  const seq = ++state.validateSeq;
  window.clearTimeout(state.validateTimer);
  state.validateTimer = window.setTimeout(() => validate(draft, seq), 250);
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
  "cron.schedule": "cronSchedule",
  "network.modes": "hostDefault",
  "vm.cpus": "vmCPUs",
  "vm.memory": "vmMemory",
  "vm.disk": "vmDisk",
  "snapshots.mode": "snapshots",
  "snapshots.keepLast": "snapshotKeepLast",
  "snapshots.maxAge": "snapshotMaxAge",
  "snapshots.required": "snapshotRequired",
  "snapshots.events": "snapshotEvents",
};

function validationFieldID(field) {
  if (field === "payload" && $("vmCatalogBlock") && !$("vmCatalogBlock").hidden) {
    if ($("manualVMSource")?.value.trim()) return "manualVMSource";
    return "vmCatalog";
  }
  return validationFieldIDs[field];
}

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
    const id = validationFieldID(err.field);
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
  if (copy.network?.tsAuthKey) copy.network.tsAuthKey = "<hidden>";
  return copy;
}

async function validate(draft, seq) {
  if (state.phase !== "editing") return;
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
    if (data.command) $("commandPreview").textContent = data.command;
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
  setDeployMode(true);
  showTerminal(draft);
  setStatus("Deploying");
  try {
    const res = await api("/api/deploy", {
      method: "POST",
      body: JSON.stringify(draft),
    });
    if (res.ok) {
      const data = await res.json();
      state.deployJobId = data.jobId || "";
      setTerminalStatus("Starting", "");
      followDeployStream(state.deployJobId);
      return;
    }
    const contentType = res.headers.get("Content-Type") || "";
    let validation = null;
    let message = "";
    if (contentType.includes("application/json")) {
      const data = await res.json();
      validation = data.validation;
      message = firstValidationMessage(validation) || "Deploy failed";
    } else {
      message = await res.text();
    }
    state.phase = "editing";
    setDeployMode(false);
    setTerminalStatus("Failed", "error");
    update();
    if (validation) applyValidationErrors(validation);
    setStatus(message, "error");
  } catch (err) {
    state.phase = "editing";
    setDeployMode(false);
    setTerminalStatus("Failed", "error");
    update();
    setStatus(String(err), "error");
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

$("serviceRoot").addEventListener("input", () => {
  if (!$("zfs").checked) return;
  state.serviceRootManual = true;
  state.pickedZFSRoot = null;
  renderZFSRootCandidates(state.zfsRootState);
});
for (const [id, field] of [["vmCPUs", "cpus"], ["vmMemory", "memory"], ["vmDisk", "disk"]]) {
  $(id).addEventListener("input", () => {
    state.vmShapeManual[field] = true;
  });
}
document.addEventListener("input", (event) => {
  if (event.target.closest("#deployForm")) update();
});
document.querySelectorAll("input[name='workload']").forEach((input) => {
  input.addEventListener("change", () => {
    state.workloadOverride = "";
    update();
  });
});
$("deployForm").addEventListener("submit", deploy);
$("upButton").addEventListener("click", () => loadFiles(parentDir(state.currentDir)));
$("host").addEventListener("focus", showHostPicker);
$("host").addEventListener("click", showHostPicker);
$("hostPickerButton").addEventListener("click", showHostPicker);
$("zfsRootPickerButton").addEventListener("click", showZFSRootPicker);
$("terminalExpand").addEventListener("click", () => {
  const sheet = $("terminalSheet");
  const expanded = sheet.dataset.expanded !== "true";
  sheet.dataset.expanded = String(expanded);
  $("terminalExpand").textContent = expanded ? "Collapse" : "Expand";
  $("terminalExpand").setAttribute("aria-expanded", String(expanded));
});
document.querySelectorAll("[data-picker-target]").forEach((el) => {
  el.addEventListener("focus", () => showPicker(el.dataset.pickerTarget));
  el.addEventListener("click", () => showPicker(el.dataset.pickerTarget));
});
document.addEventListener("click", (event) => {
  if (event.target.closest("#filePicker") || event.target.closest("[data-picker-target]")) return;
  hidePicker();
});
document.addEventListener("click", (event) => {
  if (event.target.closest("#zfsRootPicker") || event.target.closest(".zfs-root-field")) return;
  hideZFSRootPicker();
});
document.addEventListener("click", (event) => {
  if (event.target.closest("#hostPicker") || event.target.closest(".host-picker-field")) return;
  hideHostPicker();
});
document.addEventListener("focusin", (event) => {
  if (event.target.closest("#filePicker") || event.target.closest("[data-picker-target]")) return;
  hidePicker();
});
document.addEventListener("focusin", (event) => {
  if (event.target.closest("#zfsRootPicker") || event.target.closest(".zfs-root-field")) return;
  hideZFSRootPicker();
});
document.addEventListener("focusin", (event) => {
  if (event.target.closest("#hostPicker") || event.target.closest(".host-picker-field")) return;
  hideHostPicker();
});
document.addEventListener("keydown", (event) => {
  if (event.key === "Escape") {
    hidePicker();
    hideZFSRootPicker();
    hideHostPicker();
  }
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
window.addEventListener("pagehide", () => {
  if (state.phase === "done") return;
  const url = token ? `/api/session/closed?token=${encodeURIComponent(token)}` : "/api/session/closed";
  if (navigator.sendBeacon && token) {
    navigator.sendBeacon(url);
    return;
  }
  fetch("/api/session/closed", {
    method: "POST",
    keepalive: true,
    headers: {
      ...(token ? { "X-Yeet-Run-Token": token } : {}),
      ...(csrfToken ? { "X-Yeet-Run-CSRF": csrfToken } : {}),
    },
  }).catch(() => {});
});

bootstrap().catch((err) => {
  setStatus(String(err), "error");
});
