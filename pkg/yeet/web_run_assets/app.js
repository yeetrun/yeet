/**
 * Copyright (c) 2025 AUTHORS All rights reserved.
 * Use of this source code is governed by a BSD-style
 * license that can be found in the LICENSE file.
 */

const params = new URLSearchParams(window.location.search);
const token = params.get("token") || "";
const state = {
  bootstrap: null,
  currentDir: ".",
  validateSeq: 0,
  validateTimer: null,
};

const $ = (id) => document.getElementById(id);

function api(path, options = {}) {
  return fetch(path, {
    ...options,
    headers: {
      "Content-Type": "application/json",
      "X-Yeet-Run-Token": token,
      ...(options.headers || {}),
    },
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
  return {
    service: $("service").value.trim(),
    host: $("host").value.trim(),
    payload: $("payload").value.trim(),
    envFile: $("envFile").value.trim(),
    payloadArgs: splitArgs($("payloadArgs").value),
    network: {
      modes: [...document.querySelectorAll("input[name='net']:checked")].map((el) => el.value),
      tsTags: splitCSV($("tsTags").value),
      tsAuthKey: $("tsAuthKey").value,
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
      events: splitCSV($("snapshotEvents").value),
    },
  };
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
  for (const tag of draft.network.tsTags) parts.push(`--ts-tags=${shell(tag)}`);
  if (draft.network.tsAuthKey) parts.push("--ts-auth-key=<hidden>");
  for (const port of draft.network.publish) parts.push(`--publish=${shell(port)}`);
  if (draft.storage.serviceRoot) parts.push(`--service-root=${shell(draft.storage.serviceRoot)}`);
  if (draft.storage.zfs) parts.push("--zfs");
  if (draft.snapshots.mode) parts.push(`--snapshots=${shell(draft.snapshots.mode)}`);
  if (draft.snapshots.keepLast) parts.push(`--snapshot-keep-last=${draft.snapshots.keepLast}`);
  if (draft.snapshots.maxAge) parts.push(`--snapshot-max-age=${shell(draft.snapshots.maxAge)}`);
  for (const event of draft.snapshots.events) parts.push(`--snapshot-events=${shell(event)}`);
  if (draft.payloadArgs.length) parts.push("--", ...draft.payloadArgs.map(shell));
  $("commandPreview").textContent = parts.join(" ");
}

function setStatus(message, tone = "") {
  $("formStatus").textContent = message;
  if (tone) $("formStatus").dataset.tone = tone;
  else delete $("formStatus").dataset.tone;
}

function renderNetworkModes(modes) {
  const rows = modes.map((mode) => {
    const label = document.createElement("label");
    label.className = "check-pill";
    const input = document.createElement("input");
    input.type = "checkbox";
    input.name = "net";
    input.value = mode;
    const span = document.createElement("span");
    span.textContent = mode;
    label.append(input, span);
    return label;
  });
  $("networkModes").replaceChildren(...rows);
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
      if (entry.likelyEnv) $("envFile").value = entry.path;
      else $("payload").value = entry.path;
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
  renderNetworkModes(state.bootstrap.options?.networkModes || ["host", "svc", "ts", "lan", "macvlan"]);
  renderSnapshotModes(state.bootstrap.options?.snapshotModes || ["inherit", "on", "off"]);
  await loadFiles(".");
  $("service").focus();
  if ($("service").value && !$("payload").value) $("payload").focus();
  update();
}

function update() {
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

async function validate(draft) {
  const seq = ++state.validateSeq;
  setStatus("Validating");
  try {
    const res = await api("/api/validate", {
      method: "POST",
      body: JSON.stringify(draft),
    });
    if (seq !== state.validateSeq) return;
    if (!res.ok) {
      setStatus(await res.text(), "error");
      $("hostStatus").textContent = "Validation failed";
      return;
    }
    const data = await res.json();
    const ok = Boolean(data.validation?.ok);
    $("deployButton").disabled = !ok;
    $("hostStatus").textContent = ok ? "Host ready" : "Needs attention";
    if (ok) setStatus("Ready", "ready");
    else setStatus(firstValidationMessage(data.validation) || "Invalid", "error");
  } catch (err) {
    if (seq !== state.validateSeq) return;
    setStatus(String(err), "error");
    $("hostStatus").textContent = "Validation failed";
  }
}

async function deploy(event) {
  event.preventDefault();
  const draft = buildDraft();
  $("deployButton").disabled = true;
  setStatus("Deploying");
  try {
    const res = await api("/api/deploy", {
      method: "POST",
      body: JSON.stringify(draft),
    });
    if (res.ok) {
      setStatus("Done. Close this tab and return to the terminal.", "done");
      $("hostStatus").textContent = "Deployed";
      return;
    }
    const contentType = res.headers.get("Content-Type") || "";
    if (contentType.includes("application/json")) {
      const data = await res.json();
      setStatus(firstValidationMessage(data.validation) || "Deploy failed", "error");
    } else {
      setStatus(await res.text(), "error");
    }
    $("deployButton").disabled = false;
  } catch (err) {
    setStatus(String(err), "error");
    $("deployButton").disabled = false;
  }
}

function showTooltip(target) {
  const tip = $("tooltip");
  tip.textContent = target.dataset.help || "";
  const rect = target.getBoundingClientRect();
  const left = Math.min(rect.left, window.innerWidth - 332);
  tip.style.left = `${Math.max(12, left)}px`;
  tip.style.top = `${Math.min(window.innerHeight - 72, rect.bottom + 8)}px`;
  tip.hidden = false;
}

function hideTooltip() {
  $("tooltip").hidden = true;
}

document.addEventListener("input", (event) => {
  if (event.target.closest("#deployForm")) update();
});
$("deployForm").addEventListener("submit", deploy);
$("upButton").addEventListener("click", () => loadFiles(parentDir(state.currentDir)));
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
