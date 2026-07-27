// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

const fs = require("node:fs");
const path = require("node:path");
const { test, expect } = require("@playwright/test");

const repoRoot = path.resolve(__dirname, "..");
const assetRoot = path.join(repoRoot, "pkg", "yeet", "web_run_assets");

if (process.env.YEET_PLAYWRIGHT_EXECUTABLE_PATH) {
  test.use({
    launchOptions: {
      executablePath: process.env.YEET_PLAYWRIGHT_EXECUTABLE_PATH,
    },
  });
}

function readAsset(name) {
  return fs.readFileSync(path.join(assetRoot, name), "utf8");
}

function readAssetBytes(name) {
  return fs.readFileSync(path.join(assetRoot, name));
}

function mockRuntimeScript(options = {}) {
  const prefill = options.prefill || { service: "nginx", payload: "nginx:latest" };
  const delayedHostStorageServices = options.delayedHostStorageServices || [];
  const manualEvents = Boolean(options.manualEvents);
  const statusResponse = options.statusResponse || { state: "succeeded" };
  const ackStatuses = options.ackStatuses || [204];
  const delayedStatus = Boolean(options.delayedStatus);
  const delayedFiles = Boolean(options.delayedFiles);
  const lifecycleAware = Boolean(options.lifecycleAware);
  const autoSubmitOnEnable = Boolean(options.autoSubmitOnEnable);
  const activeJob = options.activeJob || "";
  return `
    const delayedHostStorageServices = new Set(${JSON.stringify(delayedHostStorageServices)});
    const manualEvents = ${JSON.stringify(manualEvents)};
    const statusResponse = ${JSON.stringify(statusResponse)};
    const ackStatuses = ${JSON.stringify(ackStatuses)};
    const delayedStatus = ${JSON.stringify(delayedStatus)};
    const delayedFiles = ${JSON.stringify(delayedFiles)};
    const lifecycleAware = ${JSON.stringify(lifecycleAware)};
    const autoSubmitOnEnable = ${JSON.stringify(autoSubmitOnEnable)};
    const activeJob = ${JSON.stringify(activeJob)};
    if (activeJob) sessionStorage.setItem("yeet.run.activeJob", activeJob);
    function json(data, status = 200) {
      return new Response(JSON.stringify(data), {
        status,
        headers: { "Content-Type": "application/json" },
      });
    }
    function text(data, status = 200) {
      return new Response(data, { status, headers: { "Content-Type": "text/plain" } });
    }
    function noContent() {
      return new Response(null, { status: 204 });
    }
    function base64(value) {
      const bytes = new TextEncoder().encode(value);
      let binary = "";
      for (const byte of bytes) binary += String.fromCharCode(byte);
      return btoa(binary);
    }
    function hostStorageResponse(service, data) {
      if (!delayedHostStorageServices.has(service)) return json(data);
      window.__pendingHostStorage = window.__pendingHostStorage || {};
      return new Promise((resolve) => {
        window.__pendingHostStorage[service] = () => resolve(json(data));
      });
    }
    const nativeFetch = window.fetch.bind(window);
    window.__ackRequests = 0;
    window.__deployRequests = 0;
    window.__copiedTerminal = null;
    window.__deployButtonEverEnabled = false;
    window.__autoSubmittedDeploy = false;
    window.addEventListener("DOMContentLoaded", () => {
      const button = document.querySelector("#deployButton");
      const observe = () => {
        if (!button || button.disabled) return;
        window.__deployButtonEverEnabled = true;
        const shouldSubmit = autoSubmitOnEnable ||
          sessionStorage.getItem("yeet.test.autoSubmitOnEnable") === "true";
        if (shouldSubmit && !window.__autoSubmittedDeploy) {
          window.__autoSubmittedDeploy = true;
          document.querySelector("#deployForm").requestSubmit();
        }
      };
      observe();
      if (button) {
        new MutationObserver(observe).observe(button, {
          attributes: true,
          attributeFilter: ["disabled"],
        });
      }
    });
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: {
        async writeText(value) {
          window.__copiedTerminal = value;
        },
      },
    });
    window.fetch = async (url, options = {}) => {
      const target = String(url);
      if (new URL(target, window.location.href).pathname === "/ghostty-vt.wasm") {
        return nativeFetch(url, options);
      }
      if (target === "/api/bootstrap") {
        return json({
          cwd: "fixture",
          configPath: "yeet.toml",
          selectedHost: "catch-lab",
          hosts: ["catch-lab"],
          prefill: ${JSON.stringify(prefill)},
          options: { networkModes: ["svc", "ts", "lan"], snapshotModes: ["inherit", "on", "off"] },
        });
      }
      if (target.startsWith("/api/host-storage")) {
        const request = new URL(target, "http://127.0.0.1");
        const service = request.searchParams.get("service") || "";
        const storage = { dataDir: "/flash/yeet/data", servicesRoot: "/flash/yeet/services" };
        if (!service) {
          return hostStorageResponse(service, {
            state: "available",
            storage,
            defaults: {
              serviceRootPlaceholder: "flash/yeet/services/<service>",
              serviceRootZfs: "flash/yeet/services",
              zfs: true,
            },
          });
        }
        const serviceRoot = "flash/yeet/services/" + service;
        return hostStorageResponse(service, {
          state: "available",
          storage,
          defaults: { serviceRoot, serviceRootZfs: serviceRoot, zfs: true },
        });
      }
      if (target.startsWith("/api/zfs-roots")) {
        const request = new URL(target, "http://127.0.0.1");
        const service = request.searchParams.get("service") || "";
        const suggestedDataset = service ? "flash/yeet/services/" + service : "flash/yeet/services";
        return json({
          state: "available",
          candidates: [{
            dataset: "flash/yeet/services",
            mountpoint: "/flash/yeet/services",
            suggestedDataset,
          }],
        });
      }
      if (target.startsWith("/api/files")) {
        window.__filesRequested = true;
        if (delayedFiles) {
          return new Promise((resolve) => {
            window.__releaseFiles = () => {
              window.__filesLoaded = true;
              resolve(json({ dir: ".", entries: [] }));
            };
          });
        }
        window.__filesLoaded = true;
        return json({ dir: ".", entries: [] });
      }
      if (target === "/api/validate") {
        window.__validateRequests = (window.__validateRequests || 0) + 1;
        return json({ validation: { ok: true, errors: [], warnings: [] } });
      }
      if (target === "/api/deploy") {
        window.__deployRequests += 1;
        return json({ jobId: "job-1" });
      }
      if (target === "/api/deploy/job-1/status") {
        if (lifecycleAware && sessionStorage.getItem("yeet.test.serverReleased") === "true") {
          return text("server completed", 503);
        }
        if (delayedStatus) {
          window.__statusPending = true;
          return new Promise((resolve) => {
            window.__releaseStatus = () => {
              window.__statusPending = false;
              resolve(json(statusResponse));
            };
          });
        }
        return json(statusResponse);
      }
      if (target === "/api/deploy/job-1/ack") {
        const index = Math.min(window.__ackRequests, ackStatuses.length - 1);
        const status = ackStatuses[index] || 204;
        window.__ackRequests += 1;
        if (lifecycleAware) {
          const total = Number(sessionStorage.getItem("yeet.test.ackTotal") || "0");
          sessionStorage.setItem("yeet.test.ackTotal", String(total + 1));
        }
        return status === 204 ? noContent() : text("ack failed", status);
      }
      if (target.startsWith("/api/session/closed")) {
        if (lifecycleAware) {
          const total = Number(sessionStorage.getItem("yeet.test.sessionCloseTotal") || "0");
          sessionStorage.setItem("yeet.test.sessionCloseTotal", String(total + 1));
          if (sessionStorage.getItem("yeet.test.serverSucceeded") === "true") {
            sessionStorage.setItem("yeet.test.serverReleased", "true");
          }
        }
        return noContent();
      }
      return text("unexpected fetch " + target, 404);
    };
    class MockEventSource {
      constructor(url) {
        this.url = String(url);
        this.listeners = {};
        this.closeCount = 0;
        this.closed = false;
        window.__eventSources.push(this);
        if (!manualEvents) {
          setTimeout(() => {
            this.dispatch("open");
            this.dispatch("terminal", {
              tty: true,
              cols: 80,
              rows: 24,
              term: "xterm-256color",
              scrollback: 1000,
            }, "1");
            const output = [
              "[+] yeet run nginx@catch-lab",
              "✔ Upload payload (103.00 B @ 285.57 B/s)",
              "✔ Detect payload (Docker Compose)",
              "[+] up 2/2",
              " ✔ Network catch-nginx_default   Created 0.0s",
              " ✔ Container catch-nginx-nginx-1 Started 0.4s",
              "✔ Install service",
            ].join("\\r\\n") + "\\r\\n";
            this.dispatch("output", { encoding: "base64", chunk: base64(output) }, "2");
            this.dispatch("status", { state: "succeeded" }, "3");
          }, 0);
        }
      }
      addEventListener(type, listener) {
        if (!this.listeners[type]) this.listeners[type] = [];
        this.listeners[type].push(listener);
      }
      close() {
        this.closeCount += 1;
        this.closed = true;
      }
      dispatch(type, data, lastEventId = "") {
        if (this.closed) return;
        const event = type === "open" || type === "error"
          ? {}
          : { data: JSON.stringify(data), lastEventId: String(lastEventId) };
        for (const listener of this.listeners[type] || []) listener(event);
      }
    }
    window.__eventSources = [];
    window.__dispatchSSE = (type, data, lastEventId, index = window.__eventSources.length - 1) => {
      const source = window.__eventSources[index];
      if (!source) throw new Error("missing EventSource " + index);
      source.dispatch(type, data, lastEventId);
    };
    window.EventSource = MockEventSource;
  `;
}

async function openFixture(page, options = {}) {
  await page.addInitScript({ content: mockRuntimeScript(options) });
  await page.route("http://yeet.test/**", async (route) => {
    const pathname = new URL(route.request().url()).pathname;
    if (pathname === "/") {
      await route.fulfill({
        status: 200,
        contentType: "text/html; charset=utf-8",
        body: readAsset("index.html").replace("__YEET_SESSION_SCRIPT__", ""),
      });
      return;
    }
    if (pathname === "/ghostty-vt.wasm" && options.runtimeGate) {
      await options.runtimeGate;
    }
    if (pathname === "/ghostty-vt.wasm" && options.runtimeInitFailure) {
      await route.fulfill({ status: 500, contentType: "text/plain", body: "WASM unavailable" });
      return;
    }
    const assets = {
      "/app.js": ["app.js", "text/javascript; charset=utf-8"],
      "/terminal.js": ["terminal.js", "text/javascript; charset=utf-8"],
      "/ghostty-web.js": ["ghostty-web.js", "text/javascript; charset=utf-8"],
      "/styles.css": ["styles.css", "text/css; charset=utf-8"],
      "/ghostty-vt.wasm": ["ghostty-vt.wasm", "application/wasm"],
      "/yeet-mark.svg": ["yeet-mark.svg", "image/svg+xml"],
    };
    const asset = assets[pathname];
    if (!asset || !fs.existsSync(path.join(assetRoot, asset[0]))) {
      await route.fulfill({ status: 404, contentType: "text/plain", body: "not found" });
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: asset[1],
      body: readAssetBytes(asset[0]),
    });
  });
  await page.goto("http://yeet.test/", { waitUntil: "domcontentloaded" });
}

async function waitForDeployReady(page) {
  await page.waitForFunction(() => {
    const button = document.querySelector("#deployButton");
    return button && !button.disabled;
  });
}

async function startManualDeploy(page) {
  await waitForDeployReady(page);
  await page.click("#deployButton");
  await page.waitForFunction(() => window.__eventSources?.length === 1);
}

async function dispatchSSE(page, type, data, lastEventId) {
  await page.evaluate(
    ({ type: eventType, data: eventData, lastEventId: eventID }) => {
      window.__dispatchSSE(eventType, eventData, eventID);
    },
    { type, data, lastEventId: String(lastEventId) },
  );
}

async function dispatchTerminalProfile(page, lastEventId = 1, profile = {}) {
  await dispatchSSE(page, "terminal", {
    tty: true,
    cols: 80,
    rows: 24,
    term: "xterm-256color",
    scrollback: 1000,
    ...profile,
  }, lastEventId);
}

async function dispatchOutput(page, value, lastEventId) {
  await dispatchSSE(page, "output", {
    encoding: "base64",
    chunk: Buffer.from(value).toString("base64"),
  }, lastEventId);
}

async function copyTerminalText(page) {
  await page.click("#terminalCopy");
  await page.waitForFunction(() => window.__copiedTerminal !== null);
  return page.evaluate(() => window.__copiedTerminal);
}

test("web run terminal renders CRLF TTY output", async ({ page }) => {
  await openFixture(page);
  await waitForDeployReady(page);
  await expect(page.locator("#commandPreview")).toContainText("yeet run nginx@catch-lab nginx:latest");
  await page.click("#deployButton");
  await page.waitForFunction(() => document.querySelector("#terminalStatus")?.textContent === "Deployed");

  await expect(page.locator("#terminalSheet")).toHaveCSS("overflow", "hidden");
  const output = await copyTerminalText(page);

  expect(output).toContain("[+] yeet run nginx@catch-lab");
  expect(output).toContain("✔ Upload payload");
  expect(output).toContain("✔ Install service");
});

test("web run reconnects natively, ignores replay duplicates, and acknowledges only after drain", async ({ page }) => {
  await openFixture(page, { manualEvents: true });
  await page.evaluate(async () => {
    const { Terminal } = await import("/ghostty-web.js");
    const nativeWrite = Terminal.prototype.write;
    window.__pendingTerminalCallbacks = [];
    window.__holdTerminalWrites = true;
    Terminal.prototype.write = function write(bytes, callback) {
      return nativeWrite.call(this, bytes, () => {
        if (window.__holdTerminalWrites) window.__pendingTerminalCallbacks.push(callback);
        else callback();
      });
    };
    window.__releaseTerminalWrites = () => {
      window.__holdTerminalWrites = false;
      for (const callback of window.__pendingTerminalCallbacks.splice(0)) callback();
    };
  });
  await startManualDeploy(page);
  await dispatchSSE(page, "open");
  await dispatchTerminalProfile(page, 1);
  await page.evaluate(() => {
    for (let id = 2; id <= 257; id += 1) {
      window.__dispatchSSE("output", { encoding: "base64", chunk: btoa("x") }, String(id));
    }
  });

  await dispatchSSE(page, "error");
  await expect(page.locator("#terminalStatus")).toHaveText("Reconnecting");
  expect(await page.locator("#service").isDisabled()).toBe(true);
  expect(await page.locator("#deployButton").isDisabled()).toBe(true);
  expect(await page.evaluate(() => document.body.dataset.phase)).toBe("deploying");
  expect(await page.evaluate(() => ({
    count: window.__eventSources.length,
    closes: window.__eventSources[0].closeCount,
  }))).toEqual({ count: 1, closes: 0 });

  await dispatchSSE(page, "open");
  await expect(page.locator("#terminalStatus")).toHaveText("Streaming");
  await page.evaluate(() => {
    for (let id = 250; id <= 257; id += 1) {
      window.__dispatchSSE("output", { encoding: "base64", chunk: btoa("x") }, String(id));
    }
  });
  await dispatchOutput(page, "y", 258);
  await dispatchSSE(page, "status", { state: "succeeded" }, 259);

  await page.waitForFunction(() => window.__pendingTerminalCallbacks?.length > 0);
  expect(await page.evaluate(() => window.__ackRequests)).toBe(0);
  await expect(page.locator("#terminalStatus")).not.toHaveText("Deployed");

  await page.evaluate(() => window.__releaseTerminalWrites());
  await expect(page.locator("#terminalStatus")).toHaveText("Deployed");
  await page.waitForFunction(() => window.__ackRequests === 1);
  const copied = await copyTerminalText(page);
  expect((copied.match(/x/g) || []).length).toBe(256);
  expect((copied.match(/y/g) || []).length).toBe(1);
  expect(await page.evaluate(() => ({
    sources: window.__eventSources.length,
    closes: window.__eventSources[0].closeCount,
    activeJob: sessionStorage.getItem("yeet.run.activeJob"),
  }))).toEqual({ sources: 1, closes: 1, activeJob: null });
});

test("web run reconnects through VM spinner control sequences without stale progress", async ({ page }) => {
  await openFixture(page, { manualEvents: true });
  await startManualDeploy(page);
  await dispatchSSE(page, "open");
  await dispatchTerminalProfile(page, 1);
  await dispatchOutput(page, "\x1b[?25l", 2);
  await dispatchOutput(page, "⠋ Preparing VM image\r", 3);

  await dispatchSSE(page, "error");
  await expect(page.locator("#terminalStatus")).toHaveText("Reconnecting");
  await dispatchSSE(page, "open");
  await expect(page.locator("#terminalStatus")).toHaveText("Streaming");

  await dispatchOutput(page, "\x1b[?25l", 2);
  await dispatchOutput(page, "⠋ Preparing VM image\r", 3);
  await dispatchOutput(page, "\x1b[2K⠙ Preparing VM image\r", 4);
  await dispatchOutput(page, "\x1b[2K✔ Prepare VM image\r\n", 5);
  await dispatchOutput(page, "  Downloaded 512 MiB / 512 MiB\r\n", 6);
  await dispatchOutput(page, "\x1b[?25h", 7);
  await dispatchSSE(page, "status", { state: "succeeded" }, 8);

  await expect(page.locator("#terminalStatus")).toHaveText("Deployed");
  const copied = await copyTerminalText(page);
  expect((copied.match(/✔ Prepare VM image/g) || []).length).toBe(1);
  expect((copied.match(/Downloaded 512 MiB \/ 512 MiB/g) || []).length).toBe(1);
  expect(copied).not.toContain("⠋ Preparing VM image");
  expect(copied).not.toContain("⠙ Preparing VM image");
});

test("web run refresh reconstructs an active terminal stream from event zero", async ({ page }) => {
  await openFixture(page, {
    manualEvents: true,
    statusResponse: { state: "running" },
  });
  await startManualDeploy(page);
  await dispatchSSE(page, "open");
  await dispatchTerminalProfile(page, 1);
  await dispatchOutput(page, "prefix-", 2);
  await page.waitForFunction(() => sessionStorage.getItem("yeet.run.activeJob") === "job-1");

  await page.reload({ waitUntil: "domcontentloaded" });
  await page.waitForFunction(() => window.__eventSources?.length === 1);
  expect(await page.locator("#service").isDisabled()).toBe(true);
  expect(await page.evaluate(() => document.body.dataset.phase)).toBe("deploying");
  expect(await page.evaluate(() => window.__eventSources[0].url)).toBe("/api/deploy/job-1/stream");

  await dispatchSSE(page, "open");
  await dispatchTerminalProfile(page, 1);
  await dispatchOutput(page, "prefix-suffix", 2);
  await dispatchSSE(page, "status", { state: "succeeded" }, 3);
  await expect(page.locator("#terminalStatus")).toHaveText("Deployed");
  const copied = await copyTerminalText(page);
  expect(copied).toContain("prefix-suffix");
  expect((copied.match(/prefix-/g) || []).length).toBe(1);
});

test("web run refresh replays a failed job before unlocking retry", async ({ page }) => {
  await openFixture(page, {
    manualEvents: true,
    statusResponse: { state: "failed", error: "remote deploy failed" },
  });
  await startManualDeploy(page);
  await page.waitForFunction(() => sessionStorage.getItem("yeet.run.activeJob") === "job-1");

  await page.reload({ waitUntil: "domcontentloaded" });
  await page.waitForFunction(() => window.__eventSources?.length === 1);
  expect(await page.locator("#service").isDisabled()).toBe(true);
  await dispatchSSE(page, "open");
  await dispatchTerminalProfile(page, 1);
  await dispatchOutput(page, "failure detail\r\n", 2);
  await dispatchSSE(page, "status", { state: "failed", error: "remote deploy failed" }, 3);

  await expect(page.locator("#terminalStatus")).toHaveText("Failed");
  expect(await page.locator("#service").isEnabled()).toBe(true);
  expect(await page.evaluate(() => sessionStorage.getItem("yeet.run.activeJob"))).toBe("job-1");
  expect(await copyTerminalText(page)).toContain("failure detail");
});

test("web run clears a failed attempt before retry output", async ({ page }) => {
  await openFixture(page, { manualEvents: true });
  await startManualDeploy(page);
  await dispatchSSE(page, "open");
  await dispatchTerminalProfile(page, 1);
  await dispatchOutput(page, "first attempt output\r\n", 2);
  await dispatchSSE(page, "status", { state: "failed", error: "first attempt failed" }, 3);

  await page.waitForFunction(() => document.body.dataset.phase === "editing");
  expect(await copyTerminalText(page)).toContain("first attempt output");

  await waitForDeployReady(page);
  await page.click("#deployButton");
  await page.waitForFunction(() => window.__eventSources?.length === 2);
  await expect(page.locator("#terminalCopy")).toBeDisabled();
  await expect(page.locator("#terminalOutput canvas")).toHaveCount(0);

  await dispatchSSE(page, "open");
  await dispatchTerminalProfile(page, 1);
  await dispatchOutput(page, "second attempt output\r\n", 2);
  await page.waitForFunction(() => !document.querySelector("#terminalCopy")?.disabled);
  const copied = await copyTerminalText(page);
  expect(copied).toContain("second attempt output");
  expect(copied).not.toContain("first attempt output");
});

test("web run degraded success keeps its warning visible without acknowledging or unlocking", async ({ page }) => {
  await openFixture(page, { manualEvents: true });
  await startManualDeploy(page);
  await dispatchSSE(page, "open");
  await dispatchTerminalProfile(page, 1);
  await dispatchOutput(page, "complete with replay gap\r\n", 2);
  await dispatchSSE(page, "warning", { message: "Browser terminal replay stopped." }, 3);
  await dispatchSSE(page, "status", { state: "succeeded" }, 4);

  await page.waitForFunction(() => document.body.dataset.phase === "done");
  await expect(page.locator("#terminalStatus")).toHaveText("Degraded");
  await expect(page.locator("#terminalWarning")).toHaveText("Browser terminal replay stopped.");
  await expect(page.locator("#terminalWarning")).toBeVisible();
  expect(await page.evaluate(() => window.__ackRequests)).toBe(0);
  expect(await page.locator("#service").isDisabled()).toBe(true);
  expect(await page.locator("#deployButton").isDisabled()).toBe(true);
  expect(await page.evaluate(() => document.body.dataset.phase)).toBe("done");
  expect(await page.evaluate(() => sessionStorage.getItem("yeet.run.activeJob"))).toBe("job-1");
});

test("web run refresh recovers degraded success when the journal cannot store final status", async ({ page }) => {
  await openFixture(page, {
    manualEvents: true,
    activeJob: "job-1",
    statusResponse: { state: "succeeded", degraded: true },
  });
  await page.waitForFunction(() => window.__eventSources?.length === 1);
  await dispatchSSE(page, "open");
  await dispatchTerminalProfile(page, 1);
  await dispatchOutput(page, "output before journal failure\r\n", 2);
  await dispatchSSE(page, "error");

  await page.waitForFunction(() => document.body.dataset.phase === "done");
  await expect(page.locator("#terminalStatus")).toHaveText("Degraded");
  await expect(page.locator("#terminalWarning")).toBeVisible();
  expect(await copyTerminalText(page)).toContain("output before journal failure");
  expect(await page.evaluate(() => window.__ackRequests)).toBe(0);
  expect(await page.locator("#deployButton").isDisabled()).toBe(true);
});

test("web run recovers degraded failure status and unlocks retry when the journal cannot seal", async ({ page }) => {
  await openFixture(page, {
    manualEvents: true,
    statusResponse: { state: "failed", error: "remote deploy failed", degraded: true },
  });
  await startManualDeploy(page);
  await dispatchSSE(page, "open");
  await dispatchTerminalProfile(page, 1);
  await dispatchOutput(page, "failure output before journal failure\r\n", 2);
  await dispatchSSE(page, "error");

  await page.waitForFunction(() => document.body.dataset.phase === "editing");
  await expect(page.locator("#terminalStatus")).toHaveText("Failed");
  await expect(page.locator("#terminalWarning")).toBeVisible();
  await expect(page.locator("#formStatus")).toContainText("remote deploy failed");
  expect(await copyTerminalText(page)).toContain("failure output before journal failure");
  expect(await page.locator("#service").isEnabled()).toBe(true);
  expect(await page.evaluate(() => window.__ackRequests)).toBe(0);
});

test("web run degrades output that arrives before its terminal profile", async ({ page }) => {
  await openFixture(page, { manualEvents: true });
  await startManualDeploy(page);
  await dispatchSSE(page, "open");
  await dispatchOutput(page, "orphaned", 1);
  await dispatchTerminalProfile(page, 2);
  await dispatchSSE(page, "status", { state: "succeeded" }, 3);

  await expect(page.locator("#terminalStatus")).toHaveText("Degraded");
  await expect(page.locator("#terminalWarning")).toContainText("before the terminal profile");
  expect(await page.evaluate(() => window.__ackRequests)).toBe(0);
  expect(await page.locator("#service").isDisabled()).toBe(true);
});

test("web run rejects a terminal profile with the wrong scrollback", async ({ page }) => {
  await openFixture(page, { manualEvents: true });
  await startManualDeploy(page);
  await dispatchSSE(page, "open");
  await dispatchTerminalProfile(page, 1, { scrollback: 999 });
  await dispatchSSE(page, "status", { state: "succeeded" }, 2);

  await expect(page.locator("#terminalStatus")).toHaveText("Degraded");
  await expect(page.locator("#terminalWarning")).toContainText("1,000 lines");
  expect(await page.evaluate(() => window.__ackRequests)).toBe(0);
});

test("web run keeps deploy disabled when the embedded terminal runtime cannot initialize", async ({ page }) => {
  await openFixture(page, { runtimeInitFailure: true });
  await expect(page.locator("#formStatus")).toContainText("browser cannot initialize the embedded terminal");
  expect(await page.locator("#deployButton").isDisabled()).toBe(true);
  expect(await page.evaluate(() => window.__deployRequests)).toBe(0);
});

test("web run never enables or submits deploy while delayed terminal startup eventually fails", async ({ page }) => {
  let releaseRuntime;
  const runtimeGate = new Promise((resolve) => {
    releaseRuntime = resolve;
  });
  await openFixture(page, {
    runtimeGate,
    runtimeInitFailure: true,
    autoSubmitOnEnable: true,
  });
  await page.waitForFunction(() => window.__filesLoaded === true);
  await page.waitForTimeout(350);

  expect(await page.evaluate(() => ({
    everEnabled: window.__deployButtonEverEnabled,
    deployRequests: window.__deployRequests,
  }))).toEqual({ everEnabled: false, deployRequests: 0 });

  releaseRuntime();
  await expect(page.locator("#formStatus")).toContainText("browser cannot initialize the embedded terminal");
  expect(await page.locator("#deployButton").isDisabled()).toBe(true);
  expect(await page.evaluate(() => window.__deployRequests)).toBe(0);
});

test("web run locks a stored job before delayed restoration status resolves", async ({ page }) => {
  await openFixture(page, {
    activeJob: "job-1",
    delayedFiles: true,
    delayedStatus: true,
    manualEvents: true,
    statusResponse: { state: "running" },
    autoSubmitOnEnable: true,
  });
  await page.waitForFunction(() => window.__filesRequested === true);
  await page.waitForTimeout(350);

  expect(await page.evaluate(() => ({
    everEnabled: window.__deployButtonEverEnabled,
    deployRequests: window.__deployRequests,
  }))).toEqual({ everEnabled: false, deployRequests: 0 });

  await page.evaluate(() => window.__releaseFiles());
  await page.waitForFunction(() => window.__statusPending === true);
  expect(await page.locator("#service").isDisabled()).toBe(true);
  expect(await page.locator("#deployButton").isDisabled()).toBe(true);
  expect(await page.evaluate(() => document.body.dataset.phase)).toBe("deploying");
  expect(await page.evaluate(() => window.__deployRequests)).toBe(0);

  await page.evaluate(() => window.__releaseStatus());
  await page.waitForFunction(() => window.__eventSources?.length === 1);
});

for (const invalidID of [" 2", "2e0", "+2", "0", "02", "9007199254740992"]) {
  test(`web run rejects non-canonical terminal event ID ${JSON.stringify(invalidID)}`, async ({ page }) => {
    await openFixture(page, { manualEvents: true });
    await startManualDeploy(page);
    await dispatchSSE(page, "open");
    await dispatchTerminalProfile(page, 1);
    await dispatchOutput(page, "must not be acknowledged", invalidID);
    await dispatchSSE(page, "status", { state: "succeeded" }, 3);

    await expect(page.locator("#terminalStatus")).toHaveText("Degraded");
    await expect(page.locator("#terminalWarning")).toContainText("invalid terminal event ID");
    expect(await page.evaluate(() => window.__ackRequests)).toBe(0);
  });
}

for (const [name, payload] of [
  ["null body", null],
  ["array body", []],
  ["string body", "output"],
  ["missing fields", {}],
  ["unsupported encoding", { encoding: "utf8", chunk: "output" }],
  ["missing chunk", { encoding: "base64" }],
  ["non-string chunk", { encoding: "base64", chunk: 42 }],
  ["invalid base64", { encoding: "base64", chunk: "***" }],
]) {
  test(`web run rejects output payload with ${name}`, async ({ page }) => {
    await openFixture(page, { manualEvents: true });
    await startManualDeploy(page);
    await dispatchSSE(page, "open");
    await dispatchTerminalProfile(page, 1);
    await dispatchSSE(page, "output", payload, 2);
    await dispatchSSE(page, "status", { state: "succeeded" }, 3);

    await expect(page.locator("#terminalStatus")).toHaveText("Degraded");
    await expect(page.locator("#terminalWarning")).toContainText("could not apply streamed output");
    expect(await page.evaluate(() => window.__ackRequests)).toBe(0);
  });
}

test("web run refresh during final drain reconstructs and acknowledges without releasing the job", async ({ page }) => {
  await openFixture(page, {
    lifecycleAware: true,
    manualEvents: true,
    statusResponse: { state: "succeeded" },
  });
  await page.evaluate(async () => {
    const { Terminal } = await import("/ghostty-web.js");
    const nativeWrite = Terminal.prototype.write;
    window.__pendingTerminalCallbacks = [];
    Terminal.prototype.write = function write(bytes, callback) {
      return nativeWrite.call(this, bytes, () => {
        window.__pendingTerminalCallbacks.push(callback);
      });
    };
  });
  await startManualDeploy(page);
  await dispatchSSE(page, "open");
  await dispatchTerminalProfile(page, 1);
  await dispatchOutput(page, "old page output\r\n", 2);
  await page.evaluate(() => sessionStorage.setItem("yeet.test.serverSucceeded", "true"));
  await dispatchSSE(page, "status", { state: "succeeded" }, 3);
  await page.waitForFunction(() => window.__pendingTerminalCallbacks?.length > 0);

  await page.reload({ waitUntil: "domcontentloaded" });
  await page.waitForFunction(() =>
    sessionStorage.getItem("yeet.test.sessionCloseTotal") !== null ||
    window.__eventSources?.length === 1,
  );
  expect(await page.evaluate(() => sessionStorage.getItem("yeet.test.sessionCloseTotal") || "0")).toBe("0");
  await page.waitForFunction(() => window.__eventSources?.length === 1);

  await dispatchSSE(page, "open");
  await dispatchTerminalProfile(page, 1);
  await dispatchOutput(page, "replacement output\r\n", 2);
  await dispatchSSE(page, "status", { state: "succeeded" }, 3);
  await expect(page.locator("#terminalStatus")).toHaveText("Deployed");
  await page.waitForFunction(() => sessionStorage.getItem("yeet.test.ackTotal") === "1");
  expect(await page.evaluate(() => ({
    ackTotal: sessionStorage.getItem("yeet.test.ackTotal"),
    sessionCloseTotal: sessionStorage.getItem("yeet.test.sessionCloseTotal") || "0",
    activeJob: sessionStorage.getItem("yeet.run.activeJob"),
  }))).toEqual({ ackTotal: "1", sessionCloseTotal: "0", activeJob: null });
});

test("web run copies through the adapter and expand preserves fixed terminal columns and accessibility", async ({ page }) => {
  await openFixture(page, { manualEvents: true });
  await page.evaluate(async () => {
    const { Terminal } = await import("/ghostty-web.js");
    const nativeResize = Terminal.prototype.resize;
    window.__terminalResizes = [];
    Terminal.prototype.resize = function resize(cols, rows) {
      window.__terminalResizes.push([cols, rows]);
      return nativeResize.call(this, cols, rows);
    };
  });
  await startManualDeploy(page);
  await dispatchSSE(page, "open");
  await dispatchTerminalProfile(page, 1, { cols: 40, rows: 12 });
  await dispatchOutput(page, "copy me\r\n", 2);
  await page.waitForFunction(() => !document.querySelector("#terminalCopy")?.disabled);

  expect(await copyTerminalText(page)).toContain("copy me");
  await page.click("#terminalExpand");
  await expect(page.locator("#terminalExpand")).toHaveAttribute("aria-expanded", "true");
  await expect(page.locator("#terminalExpand")).toHaveText("Collapse");
  await page.click("#terminalExpand");
  await expect(page.locator("#terminalExpand")).toHaveAttribute("aria-expanded", "false");
  expect(await page.evaluate(() => window.__terminalResizes)).toEqual([]);
  await expect(page.locator("#terminalOutput")).toHaveAttribute("role", "region");
  await expect(page.locator("#terminalOutput")).not.toHaveAttribute("aria-live", /.+/);
  await expect(
    page.getByRole("region", { name: "Deploy output" }).and(page.locator("#terminalOutput")),
  ).toBeVisible();
  await expect(page.getByRole("log")).toHaveCount(0);
  await expect(page.locator("#terminalStatus")).toHaveAttribute("role", "status");
});

test("web run retries a failed acknowledgement while the local server remains reachable", async ({ page }) => {
  await openFixture(page, {
    manualEvents: true,
    statusResponse: { state: "succeeded" },
    ackStatuses: [500, 204],
  });
  await startManualDeploy(page);
  await dispatchSSE(page, "open");
  await dispatchTerminalProfile(page, 1);
  await dispatchOutput(page, "finished\r\n", 2);
  await dispatchSSE(page, "status", { state: "succeeded" }, 3);

  await expect(page.locator("#terminalStatus")).toHaveText("Deployed");
  await page.waitForFunction(() => window.__ackRequests >= 1);
  await expect(page.locator("#formStatus")).toContainText("finishing");
  await page.waitForFunction(() => window.__ackRequests === 2);
  await page.waitForFunction(() => sessionStorage.getItem("yeet.run.activeJob") === null);
  await expect(page.locator("#terminalStatus")).toHaveText("Deployed");
});

test("web run clears auto ZFS service root when service is erased", async ({ page }) => {
  await openFixture(page);
  await page.waitForFunction(() => document.querySelector("#serviceRoot")?.value === "flash/yeet/services/nginx");
  await expect(page.locator("#zfs")).toBeChecked();

  await page.fill("#service", "n");
  await page.waitForFunction(() => document.querySelector("#serviceRoot")?.value === "flash/yeet/services/n");

  await page.fill("#service", "");
  await page.waitForFunction(() => document.querySelector("#serviceRoot")?.value === "");
  await expect(page.locator("#commandPreview")).not.toContainText("flash/yeet/services/n");
});

test("web run shows ZFS placeholder before service is named", async ({ page }) => {
  await openFixture(page, { prefill: { service: "", payload: "nginx:latest" } });
  await page.waitForFunction(() => document.querySelector("#zfs")?.checked === true);

  await expect(page.locator("#serviceRoot")).toHaveValue("");
  await expect(page.locator("#serviceRoot")).toHaveAttribute("placeholder", "flash/yeet/services/<service>");
  await expect(page.locator("#commandPreview")).not.toContainText("--zfs");
  expect(await page.evaluate(() => window.__validateRequests || 0)).toBe(0);

  await page.fill("#service", "nginx");
  await page.waitForFunction(() => document.querySelector("#serviceRoot")?.value === "flash/yeet/services/nginx");
  await expect(page.locator("#zfs")).toBeChecked();
});

test("web run derives ZFS service root while service defaults are loading", async ({ page }) => {
  await openFixture(page, {
    prefill: { service: "", payload: "nginx:latest" },
    delayedHostStorageServices: ["n"],
  });
  await page.waitForFunction(() => document.querySelector("#zfs")?.checked === true);
  await expect(page.locator("#serviceRoot")).toHaveAttribute("placeholder", "flash/yeet/services/<service>");

  await page.fill("#service", "n");

  expect(await page.evaluate(() => Boolean(window.__pendingHostStorage?.n))).toBe(true);
  expect(await page.locator("#serviceRoot").inputValue()).toBe("flash/yeet/services/n");
  expect(await page.locator("#commandPreview").textContent()).toContain("--service-root=flash/yeet/services/n --zfs");
});

test("web run derives ZFS service root from a previous auto default while loading", async ({ page }) => {
  await openFixture(page, {
    prefill: { service: "nginx", payload: "nginx:latest" },
    delayedHostStorageServices: ["n"],
  });
  await page.waitForFunction(() => document.querySelector("#serviceRoot")?.value === "flash/yeet/services/nginx");

  await page.fill("#service", "n");

  expect(await page.evaluate(() => Boolean(window.__pendingHostStorage?.n))).toBe(true);
  expect(await page.locator("#serviceRoot").inputValue()).toBe("flash/yeet/services/n");
  expect(await page.locator("#commandPreview").textContent()).toContain("--service-root=flash/yeet/services/n --zfs");
});

test("Ghostty adapter preserves terminal bytes and rendering semantics", async ({ page }) => {
  await openFixture(page);

  const copied = await page.evaluate(async () => {
    const { createTerminalAdapter } = await import("/terminal.js");
    const element = document.createElement("div");
    element.style.width = "900px";
    element.style.height = "320px";
    document.body.appendChild(element);
    const adapter = await createTerminalAdapter(element, { cols: 80, rows: 12 });
    const bytes = new TextEncoder().encode([
      "phase 1\r",
      "\x1b[2Kphase 2\r\n",
      "\x1b[31mred\x1b[0m ",
      "界 e\u0301\r\n",
    ].join(""));
    adapter.write(bytes);
    bytes.fill("x".charCodeAt(0));
    await adapter.drain();
    const text = adapter.copyText();
    adapter.dispose();
    return text;
  });

  expect(copied).toContain("phase 2");
  expect(copied).toContain("red");
  expect(copied).toContain("界");
  expect(copied).toContain("e\u0301");
  expect(copied).not.toContain("phase 1");
  expect(copied).not.toContain("xxxxxxxx");
});

test("Ghostty adapter bounds scrollback and keeps explicit geometry", async ({ page }) => {
  await openFixture(page);

  const result = await page.evaluate(async () => {
    const { createTerminalAdapter } = await import("/terminal.js");
    const parent = document.createElement("div");
    parent.style.width = "240px";
    parent.style.height = "220px";
    const element = document.createElement("div");
    element.className = "terminal-output";
    parent.appendChild(element);
    document.body.appendChild(parent);

    const adapter = await createTerminalAdapter(element, { cols: 40, rows: 10 });
    const lines = Array.from(
      { length: 1101 },
      (_, index) => `line ${String(index).padStart(4, "0")}${index === 100 ? " 界 e\u0301" : ""}\r\n`,
    ).join("");
    adapter.write(new TextEncoder().encode(lines));
    await adapter.drain();
    const text = adapter.copyText();

    adapter.resize(132, 44);
    await adapter.drain();
    const before = {
      cols: adapter.cols,
      rows: adapter.rows,
      canvasWidth: element.querySelector("canvas").style.width,
      canvasHeight: element.querySelector("canvas").style.height,
    };
    parent.style.width = "1200px";
    await new Promise((resolve) => requestAnimationFrame(resolve));
    const after = {
      cols: adapter.cols,
      rows: adapter.rows,
      canvasWidth: element.querySelector("canvas").style.width,
      canvasHeight: element.querySelector("canvas").style.height,
    };
    adapter.dispose();
    return { text, copiedLineCount: text.split("\n").length, before, after };
  });

  expect(result.text).toContain("line 1100");
  // Ghostty's selection text includes the wide glyph's trailing display cell.
  expect(result.text).toContain("line 0100 界  e\u0301");
  expect(result.text).not.toContain("line 0000");
  expect(result.copiedLineCount).toBe(1010);
  expect(result.before).toEqual(result.after);
  expect(result.after.cols).toBe(132);
  expect(result.after.rows).toBe(44);
});

test("Ghostty adapter follows the outer viewport until the user scrolls up", async ({ page }) => {
  await openFixture(page);

  const result = await page.evaluate(async () => {
    const { createTerminalAdapter } = await import("/terminal.js");
    const frame = () => new Promise((resolve) => requestAnimationFrame(resolve));
    const atBottom = (element) =>
      Math.abs(element.scrollHeight - element.clientHeight - element.scrollTop) <= 2;
    const element = document.createElement("div");
    element.className = "terminal-output";
    element.style.width = "560px";
    element.style.height = "96px";
    document.body.appendChild(element);

    const adapter = await createTerminalAdapter(element, { cols: 80, rows: 24 });
    const initialOutput = Array.from(
      { length: 40 },
      (_, index) => `initial ${String(index).padStart(2, "0")}\r\n`,
    ).join("");
    adapter.write(new TextEncoder().encode(initialOutput));
    await adapter.drain();
    const initial = {
      atBottom: atBottom(element),
      scrollTop: element.scrollTop,
      scrollHeight: element.scrollHeight,
      clientHeight: element.clientHeight,
    };

    element.scrollTop = 0;
    await frame();
    adapter.write(new TextEncoder().encode("paused output\r\n"));
    await adapter.drain();
    const pausedTop = element.scrollTop;

    element.style.height = "72px";
    await frame();
    await frame();
    const pausedAfterResize = element.scrollTop;

    element.scrollTop = element.scrollHeight;
    await frame();
    adapter.write(new TextEncoder().encode("followed output\r\n"));
    await adapter.drain();
    const resumedAtBottom = atBottom(element);

    element.scrollTop = 0;
    await frame();
    adapter.clear();
    await adapter.drain();
    const clearAtBottom = atBottom(element);
    adapter.dispose();

    return { initial, pausedTop, pausedAfterResize, resumedAtBottom, clearAtBottom };
  });

  expect(result.initial.scrollHeight).toBeGreaterThan(result.initial.clientHeight);
  expect(result.initial.scrollTop).toBeGreaterThan(0);
  expect(result.initial.atBottom).toBe(true);
  expect(result.pausedTop).toBe(0);
  expect(result.pausedAfterResize).toBe(0);
  expect(result.resumedAtBottom).toBe(true);
  expect(result.clearAtBottom).toBe(true);
});

test("Ghostty adapter preserves manual terminal scrollback across writes", async ({ page }) => {
  await openFixture(page);

  const result = await page.evaluate(async () => {
    const { Terminal } = await import("/ghostty-web.js");
    const originalOpen = Terminal.prototype.open;
    let terminal;
    Terminal.prototype.open = function open(element) {
      terminal = this;
      return originalOpen.call(this, element);
    };

    try {
      const { createTerminalAdapter } = await import("/terminal.js");
      const element = document.createElement("div");
      element.style.width = "600px";
      element.style.height = "320px";
      document.body.appendChild(element);
      const adapter = await createTerminalAdapter(element, { cols: 80, rows: 6 });
      const initialOutput = Array.from(
        { length: 1101 },
        (_, index) => `line ${String(index).padStart(4, "0")}\r\n`,
      ).join("");
      adapter.write(new TextEncoder().encode(initialOutput));
      await adapter.drain();

      terminal.scrollToLine(10);
      const beforeWrite = terminal.getViewportY();
      adapter.write(new TextEncoder().encode("new 1\r\nnew 2\r\nnew 3\r\n"));
      await adapter.drain();
      const afterPausedWrite = terminal.getViewportY();

      terminal.scrollToBottom();
      adapter.write(new TextEncoder().encode("live again\r\n"));
      await adapter.drain();
      const afterResumedWrite = terminal.getViewportY();
      adapter.dispose();
      return { beforeWrite, afterPausedWrite, afterResumedWrite };
    } finally {
      Terminal.prototype.open = originalOpen;
    }
  });

  expect(result).toEqual({
    beforeWrite: 10,
    afterPausedWrite: 13,
    afterResumedWrite: 0,
  });
});

test("Ghostty adapter bounds write batches and settles lifecycle drains", async ({ page }) => {
  await openFixture(page);

  const result = await page.evaluate(async () => {
    const { Terminal } = await import("/ghostty-web.js");
    const originalWrite = Terminal.prototype.write;
    const pendingCallbacks = [];
    const batchSizes = [];
    Terminal.prototype.write = function write(bytes, callback) {
      batchSizes.push(bytes.length);
      originalWrite.call(this, bytes);
      pendingCallbacks.push(callback);
    };
    const frame = () => new Promise((resolve) => requestAnimationFrame(resolve));

    try {
      const { createTerminalAdapter } = await import("/terminal.js");
      const element = document.createElement("div");
      document.body.appendChild(element);
      const adapter = await createTerminalAdapter(element, { cols: 132, rows: 44 });
      adapter.write(new Uint8Array(64 * 1024).fill("a".charCodeAt(0)));
      adapter.write(new TextEncoder().encode("bcd"));
      let drained = false;
      const drainPromise = adapter.drain().then(() => {
        drained = true;
      });

      await frame();
      const beforeFirstCallback = { sizes: [...batchSizes], drained };
      pendingCallbacks.shift()();
      await frame();
      await frame();
      const beforeFinalCallback = { sizes: [...batchSizes], drained };
      pendingCallbacks.shift()();
      await drainPromise;

      adapter.write(new TextEncoder().encode("pending"));
      const disposeDrain = adapter.drain();
      await frame();
      adapter.dispose();
      await disposeDrain;
      return {
        beforeFirstCallback,
        beforeFinalCallback,
        finalSizes: batchSizes,
      };
    } finally {
      Terminal.prototype.write = originalWrite;
    }
  });

  expect(result.beforeFirstCallback).toEqual({ sizes: [64 * 1024], drained: false });
  expect(result.beforeFinalCallback).toEqual({ sizes: [64 * 1024, 3], drained: false });
  expect(result.finalSizes).toEqual([64 * 1024, 3, 7]);
});

test("Ghostty adapter preserves resize barriers between byte writes", async ({ page }) => {
  await openFixture(page);

  const operations = await page.evaluate(async () => {
    const { Terminal } = await import("/ghostty-web.js");
    const originalWrite = Terminal.prototype.write;
    const originalResize = Terminal.prototype.resize;
    const pendingCallbacks = [];
    const seen = [];
    Terminal.prototype.write = function write(bytes, callback) {
      seen.push({
        type: "write",
        cols: this.cols,
        rows: this.rows,
        text: new TextDecoder().decode(bytes),
      });
      originalWrite.call(this, bytes);
      pendingCallbacks.push(callback);
    };
    Terminal.prototype.resize = function resize(cols, rows) {
      seen.push({ type: "resize", cols, rows });
      originalResize.call(this, cols, rows);
    };
    const frame = () => new Promise((resolve) => requestAnimationFrame(resolve));

    try {
      const { createTerminalAdapter } = await import("/terminal.js");
      const element = document.createElement("div");
      document.body.appendChild(element);
      const adapter = await createTerminalAdapter(element, { cols: 80, rows: 24 });
      adapter.write(new TextEncoder().encode("before"));
      await frame();
      adapter.resize(132, 44);
      adapter.write(new TextEncoder().encode("after"));
      const beforeCallback = [...seen];
      let drained = false;
      const drainPromise = adapter.drain().then(() => {
        drained = true;
      });
      for (let attempts = 0; attempts < 10 && !drained; attempts += 1) {
        await frame();
        while (pendingCallbacks.length) pendingCallbacks.shift()();
      }
      await drainPromise;
      adapter.dispose();
      return { beforeCallback, seen };
    } finally {
      Terminal.prototype.write = originalWrite;
      Terminal.prototype.resize = originalResize;
    }
  });

  expect(operations.beforeCallback).toEqual([
    { type: "write", cols: 80, rows: 24, text: "before" },
  ]);
  expect(operations.seen).toEqual([
    { type: "write", cols: 80, rows: 24, text: "before" },
    { type: "resize", cols: 132, rows: 44 },
    { type: "write", cols: 132, rows: 44, text: "after" },
  ]);
});

test("Ghostty adapter clear removes screen and exposed scrollback in FIFO order", async ({ page }) => {
  await openFixture(page);

  const result = await page.evaluate(async () => {
    const { createTerminalAdapter } = await import("/terminal.js");
    const element = document.createElement("div");
    element.style.width = "600px";
    element.style.height = "160px";
    document.body.appendChild(element);
    const adapter = await createTerminalAdapter(element, { cols: 40, rows: 4 });
    const oldOutput = Array.from(
      { length: 12 },
      (_, index) => `old deploy ${String(index).padStart(2, "0")}\r\n`,
    ).join("");

    adapter.write(new TextEncoder().encode(oldOutput));
    adapter.clear();
    await adapter.drain();
    element.dispatchEvent(new WheelEvent("wheel", {
      bubbles: true,
      cancelable: true,
      deltaY: -1200,
    }));
    await new Promise((resolve) => requestAnimationFrame(resolve));
    const afterClear = adapter.copyText();

    adapter.write(new TextEncoder().encode("new deploy output\r\n"));
    await adapter.drain();
    const afterWrite = adapter.copyText();
    adapter.dispose();
    return { afterClear, afterWrite };
  });

  expect(result.afterClear).not.toContain("old deploy");
  expect(result.afterWrite).toContain("new deploy output");
  expect(result.afterWrite).not.toContain("old deploy");
});

test("Ghostty adapter permanently fails when a queued resize throws", async ({ page }) => {
  await openFixture(page);

  const result = await page.evaluate(async () => {
    const { Terminal } = await import("/ghostty-web.js");
    const originalWrite = Terminal.prototype.write;
    const originalResize = Terminal.prototype.resize;
    const pendingCallbacks = [];
    Terminal.prototype.write = function write(bytes, callback) {
      originalWrite.call(this, bytes);
      pendingCallbacks.push(callback);
    };
    Terminal.prototype.resize = function resize(cols, rows) {
      if (cols === 132 && rows === 44) throw new Error("resize failed");
      return originalResize.call(this, cols, rows);
    };
    const frame = () => new Promise((resolve) => requestAnimationFrame(resolve));

    let adapter;
    try {
      const { createTerminalAdapter } = await import("/terminal.js");
      const element = document.createElement("div");
      document.body.appendChild(element);
      adapter = await createTerminalAdapter(element, { cols: 80, rows: 24 });
      adapter.write(new TextEncoder().encode("before"));
      await frame();
      adapter.resize(132, 44);
      adapter.write(new TextEncoder().encode("after"));
      const drainOutcome = adapter.drain().then(
        () => ({ state: "resolved" }),
        (error) => ({ state: "rejected", error }),
      );

      pendingCallbacks.shift()();
      await frame();
      await frame();
      const first = await Promise.race([
        drainOutcome,
        new Promise((resolve) => setTimeout(() => resolve({ state: "pending" }), 100)),
      ]);

      const sameFailure = {};
      for (const [name, operation] of Object.entries({
        write: () => adapter.write(new Uint8Array([1])),
        resize: () => adapter.resize(81, 25),
        clear: () => adapter.clear(),
        copyText: () => adapter.copyText(),
      })) {
        try {
          operation();
          sameFailure[name] = false;
        } catch (error) {
          sameFailure[name] = error === first.error;
        }
      }
      const laterDrain = await adapter.drain().then(
        () => false,
        (error) => error === first.error,
      );
      return {
        state: first.state,
        message: first.error?.message,
        sameFailure: { ...sameFailure, drain: laterDrain },
      };
    } finally {
      if (adapter) adapter.dispose();
      Terminal.prototype.write = originalWrite;
      Terminal.prototype.resize = originalResize;
    }
  });

  expect(result).toEqual({
    state: "rejected",
    message: "resize failed",
    sameFailure: {
      write: true,
      resize: true,
      clear: true,
      copyText: true,
      drain: true,
    },
  });
});

test("Ghostty reset creation failure keeps the old native terminal valid", async ({ page }) => {
  await openFixture(page);

  const result = await page.evaluate(async () => {
    const { Ghostty, GhosttyTerminal, Terminal } = await import("/ghostty-web.js");
    const originalOpen = Terminal.prototype.open;
    const originalCreateTerminal = Ghostty.prototype.createTerminal;
    const originalMethods = new Map();
    const logicallyFreed = new WeakSet();
    const freeCalls = new WeakMap();
    const staleAccesses = [];
    let terminal;

    Terminal.prototype.open = function open(element) {
      terminal = this;
      return originalOpen.call(this, element);
    };
    for (const name of Object.getOwnPropertyNames(GhosttyTerminal.prototype)) {
      const method = GhosttyTerminal.prototype[name];
      if (name === "constructor" || typeof method !== "function") continue;
      originalMethods.set(name, method);
      if (name === "free") {
        GhosttyTerminal.prototype.free = function free() {
          freeCalls.set(this, (freeCalls.get(this) || 0) + 1);
          logicallyFreed.add(this);
        };
      } else {
        GhosttyTerminal.prototype[name] = function guardedMethod(...args) {
          if (logicallyFreed.has(this)) staleAccesses.push(name);
          return method.apply(this, args);
        };
      }
    }
    const frame = () => new Promise((resolve) => requestAnimationFrame(resolve));

    let adapter;
    try {
      const { createTerminalAdapter } = await import("/terminal.js");
      const element = document.createElement("div");
      document.body.appendChild(element);
      adapter = await createTerminalAdapter(element, { cols: 40, rows: 4 });
      adapter.write(new TextEncoder().encode("still valid\r\n"));
      await adapter.drain();
      const oldWasmTerm = terminal.wasmTerm;
      Ghostty.prototype.createTerminal = function createTerminal() {
        throw new Error("replacement creation failed");
      };

      adapter.clear();
      const first = await adapter.drain().then(
        () => ({ state: "resolved" }),
        (error) => ({ state: "rejected", error }),
      );
      await frame();
      await frame();

      const sameFailure = {};
      for (const [name, operation] of Object.entries({
        write: () => adapter.write(new Uint8Array([1])),
        resize: () => adapter.resize(41, 5),
        clear: () => adapter.clear(),
        copyText: () => adapter.copyText(),
      })) {
        try {
          operation();
          sameFailure[name] = false;
        } catch (error) {
          sameFailure[name] = error === first.error;
        }
      }
      sameFailure.drain = await adapter.drain().then(
        () => false,
        (error) => error === first.error,
      );

      const ownersBeforeDispose = {
        terminal: terminal.wasmTerm === oldWasmTerm,
        selection: terminal.selectionManager?.wasmTerm === oldWasmTerm,
      };
      const freesBeforeDispose = freeCalls.get(oldWasmTerm) || 0;
      adapter.dispose();
      adapter = null;
      return {
        state: first.state,
        message: first.error?.message,
        sameFailure,
        ownersBeforeDispose,
        freesBeforeDispose,
        freesAfterDispose: freeCalls.get(oldWasmTerm) || 0,
        staleAccesses,
      };
    } finally {
      if (adapter) adapter.dispose();
      Terminal.prototype.open = originalOpen;
      Ghostty.prototype.createTerminal = originalCreateTerminal;
      for (const [name, method] of originalMethods) {
        GhosttyTerminal.prototype[name] = method;
      }
    }
  });

  expect(result).toEqual({
    state: "rejected",
    message: "replacement creation failed",
    sameFailure: {
      write: true,
      resize: true,
      clear: true,
      copyText: true,
      drain: true,
    },
    ownersBeforeDispose: {
      terminal: true,
      selection: true,
    },
    freesBeforeDispose: 0,
    freesAfterDispose: 1,
    staleAccesses: [],
  });
});

test("Ghostty successful reset rebinds every native terminal owner before free", async ({ page }) => {
  await openFixture(page);

  const result = await page.evaluate(async () => {
    const { GhosttyTerminal, Terminal } = await import("/ghostty-web.js");
    const originalOpen = Terminal.prototype.open;
    const originalReset = Terminal.prototype.reset;
    const originalMethods = new Map();
    const logicallyFreed = new WeakSet();
    const freeCalls = new WeakMap();
    const staleAccesses = [];
    let terminal;
    let ownersDuringReset;

    Terminal.prototype.open = function open(element) {
      terminal = this;
      return originalOpen.call(this, element);
    };
    for (const name of Object.getOwnPropertyNames(GhosttyTerminal.prototype)) {
      const method = GhosttyTerminal.prototype[name];
      if (name === "constructor" || typeof method !== "function") continue;
      originalMethods.set(name, method);
      if (name === "free") {
        GhosttyTerminal.prototype.free = function free() {
          freeCalls.set(this, (freeCalls.get(this) || 0) + 1);
          logicallyFreed.add(this);
        };
      } else {
        GhosttyTerminal.prototype[name] = function guardedMethod(...args) {
          if (logicallyFreed.has(this)) staleAccesses.push(name);
          return method.apply(this, args);
        };
      }
    }
    Terminal.prototype.reset = function reset() {
      const oldWasmTerm = this.wasmTerm;
      const rendererOwnedOldBefore = this.renderer?.currentBuffer === oldWasmTerm;
      originalReset.call(this);
      ownersDuringReset = {
        rendererOwnedOldBefore,
        oldFreed: logicallyFreed.has(oldWasmTerm),
        terminal: this.wasmTerm === oldWasmTerm ? "old" : "replacement",
        selection: this.selectionManager?.wasmTerm === this.wasmTerm,
        renderer: this.renderer?.currentBuffer === this.wasmTerm,
        rendererSelectionCleared: this.renderer?.currentSelectionCoords === null,
      };
    };

    let adapter;
    try {
      const { createTerminalAdapter } = await import("/terminal.js");
      const element = document.createElement("div");
      document.body.appendChild(element);
      adapter = await createTerminalAdapter(element, { cols: 40, rows: 4 });
      adapter.write(new TextEncoder().encode("old e\u0301 output\r\n"));
      await adapter.drain();

      const oldWasmTerm = terminal.wasmTerm;
      terminal.select(0, 0, 3);
      terminal.renderer.render(oldWasmTerm, true, 0, terminal, 1);
      const selectionCachedBefore = terminal.renderer.currentSelectionCoords !== null;
      cancelAnimationFrame(terminal.animationFrameId);
      terminal.animationFrameId = undefined;

      adapter.clear();
      await adapter.drain();
      const ownersAtDrain = {
        terminal: terminal.wasmTerm === oldWasmTerm ? "old" : "replacement",
        selection: terminal.selectionManager?.wasmTerm === terminal.wasmTerm,
        renderer: terminal.renderer?.currentBuffer === terminal.wasmTerm,
        rendererSelectionCleared: terminal.renderer?.currentSelectionCoords === null,
      };
      const freesAtDrain = freeCalls.get(oldWasmTerm) || 0;
      adapter.dispose();
      adapter = null;
      return {
        selectionCachedBefore,
        ownersDuringReset,
        ownersAtDrain,
        freesAtDrain,
        staleAccesses,
      };
    } finally {
      if (adapter) adapter.dispose();
      Terminal.prototype.open = originalOpen;
      Terminal.prototype.reset = originalReset;
      for (const [name, method] of originalMethods) {
        GhosttyTerminal.prototype[name] = method;
      }
    }
  });

  expect(result).toEqual({
    selectionCachedBefore: true,
    ownersDuringReset: {
      rendererOwnedOldBefore: true,
      oldFreed: true,
      terminal: "replacement",
      selection: true,
      renderer: true,
      rendererSelectionCleared: true,
    },
    ownersAtDrain: {
      terminal: "replacement",
      selection: true,
      renderer: true,
      rendererSelectionCleared: true,
    },
    freesAtDrain: 1,
    staleAccesses: [],
  });
});

test("Ghostty clear invalidates links and hover state before new output", async ({ page }) => {
  await openFixture(page);

  const result = await page.evaluate(async () => {
    const { Terminal } = await import("/ghostty-web.js");
    const originalOpen = Terminal.prototype.open;
    const originalWindowOpen = window.open;
    const activated = [];
    let terminal;
    Terminal.prototype.open = function open(element) {
      terminal = this;
      return originalOpen.call(this, element);
    };
    window.open = (url) => {
      activated.push(String(url));
      return null;
    };

    let adapter;
    try {
      const { createTerminalAdapter } = await import("/terminal.js");
      const element = document.createElement("div");
      element.style.width = "600px";
      element.style.height = "160px";
      document.body.appendChild(element);
      adapter = await createTerminalAdapter(element, { cols: 40, rows: 4 });
      adapter.write(new TextEncoder().encode("https://example.com/old"));
      await adapter.drain();

      const oldRow = terminal.wasmTerm.getScrollbackLength();
      const cachedBefore = await terminal.linkDetector.getLinkAt(0, oldRow);
      terminal.currentHoveredLink = cachedBefore;
      terminal.renderer.hoveredHyperlinkId = 7;
      terminal.renderer.previousHoveredHyperlinkId = 7;
      terminal.renderer.hoveredLinkRange = {
        startX: 0,
        startY: 0,
        endX: 22,
        endY: 0,
      };
      terminal.renderer.previousHoveredLinkRange = {
        startX: 0,
        startY: 0,
        endX: 22,
        endY: 0,
      };
      element.style.cursor = "pointer";

      adapter.clear();
      await adapter.drain();
      const stateAfterClear = {
        linkCacheSize: terminal.linkDetector.linkCache.size,
        scannedRowsSize: terminal.linkDetector.scannedRows.size,
        currentHoveredLink: terminal.currentHoveredLink?.text || null,
        hoveredHyperlinkId: terminal.renderer.hoveredHyperlinkId,
        previousHoveredHyperlinkId: terminal.renderer.previousHoveredHyperlinkId,
        hoveredLinkRange: terminal.renderer.hoveredLinkRange,
        previousHoveredLinkRange: terminal.renderer.previousHoveredLinkRange,
        cursor: element.style.cursor,
      };
      const cachedAfter = await terminal.linkDetector.getLinkAt(0, 0);

      const rect = terminal.canvas.getBoundingClientRect();
      element.dispatchEvent(new MouseEvent("click", {
        bubbles: true,
        cancelable: true,
        ctrlKey: true,
        clientX: rect.left + terminal.renderer.charWidth / 2,
        clientY: rect.top + terminal.renderer.charHeight / 2,
      }));
      await new Promise((resolve) => setTimeout(resolve, 0));
      adapter.dispose();
      adapter = null;
      return {
        cachedBefore: cachedBefore?.text || null,
        stateAfterClear,
        cachedAfter: cachedAfter?.text || null,
        activated,
      };
    } finally {
      if (adapter) adapter.dispose();
      Terminal.prototype.open = originalOpen;
      window.open = originalWindowOpen;
    }
  });

  expect(result).toEqual({
    cachedBefore: "https://example.com/old",
    stateAfterClear: {
      linkCacheSize: 0,
      scannedRowsSize: 0,
      currentHoveredLink: null,
      hoveredHyperlinkId: 0,
      previousHoveredHyperlinkId: 0,
      hoveredLinkRange: null,
      previousHoveredLinkRange: null,
      cursor: "text",
    },
    cachedAfter: null,
    activated: [],
  });
});

test("Ghostty clear cancels an active smooth scroll", async ({ page }) => {
  await openFixture(page);

  const result = await page.evaluate(async () => {
    const { Terminal } = await import("/ghostty-web.js");
    const originalOpen = Terminal.prototype.open;
    let terminal;
    Terminal.prototype.open = function open(element) {
      terminal = this;
      return originalOpen.call(this, element);
    };

    try {
      const { createTerminalAdapter } = await import("/terminal.js");
      const element = document.createElement("div");
      element.style.width = "600px";
      element.style.height = "160px";
      document.body.appendChild(element);
      const adapter = await createTerminalAdapter(element, { cols: 40, rows: 4 });
      const oldOutput = Array.from(
        { length: 30 },
        (_, index) => `old scroll ${String(index).padStart(2, "0")}\r\n`,
      ).join("");
      adapter.write(new TextEncoder().encode(oldOutput));
      await adapter.drain();

      element.dispatchEvent(new WheelEvent("wheel", {
        bubbles: true,
        cancelable: true,
        deltaY: -1200,
      }));
      const viewportDuringScroll = terminal.getViewportY();
      adapter.clear();
      adapter.write(new TextEncoder().encode("fresh bottom\r\n"));
      await adapter.drain();
      await new Promise((resolve) => setTimeout(resolve, 150));
      const text = adapter.copyText();
      const viewportAfterWait = terminal.getViewportY();
      adapter.dispose();
      return { viewportDuringScroll, viewportAfterWait, text };
    } finally {
      Terminal.prototype.open = originalOpen;
    }
  });

  expect(result.viewportDuringScroll).toBeGreaterThan(0);
  expect(result.viewportAfterWait).toBe(0);
  expect(result.text).toContain("fresh bottom");
  expect(result.text).not.toContain("old scroll");
});

test("Ghostty adapter disposal rethrows after restoring and settling", async ({ page }) => {
  await openFixture(page);

  const result = await page.evaluate(async () => {
    const { Terminal } = await import("/ghostty-web.js");
    const originalWrite = Terminal.prototype.write;
    const originalDispose = Terminal.prototype.dispose;
    Terminal.prototype.write = function write(bytes) {
      originalWrite.call(this, bytes);
    };
    Terminal.prototype.dispose = function dispose() {
      this.element?.removeAttribute("tabindex");
      this.element?.removeAttribute("role");
      this.element?.removeAttribute("aria-label");
      throw new Error("dispose failed");
    };
    const frame = () => new Promise((resolve) => requestAnimationFrame(resolve));

    try {
      const { createTerminalAdapter } = await import("/terminal.js");
      const element = document.createElement("div");
      document.body.appendChild(element);
      const adapter = await createTerminalAdapter(element, { cols: 80, rows: 24 });
      adapter.write(new TextEncoder().encode("pending"));
      const drainOutcome = adapter.drain().then(() => "resolved", () => "rejected");
      await frame();

      let disposeMessage = "";
      try {
        adapter.dispose();
      } catch (error) {
        disposeMessage = error.message;
      }
      const settled = await Promise.race([
        drainOutcome,
        new Promise((resolve) => setTimeout(() => resolve("pending"), 100)),
      ]);
      let secondDisposeThrew = false;
      try {
        adapter.dispose();
      } catch {
        secondDisposeThrew = true;
      }
      return {
        disposeMessage,
        settled,
        secondDisposeThrew,
        tabindex: element.getAttribute("tabindex"),
        role: element.getAttribute("role"),
        label: element.getAttribute("aria-label"),
      };
    } finally {
      Terminal.prototype.write = originalWrite;
      Terminal.prototype.dispose = originalDispose;
    }
  });

  expect(result).toEqual({
    disposeMessage: "dispose failed",
    settled: "resolved",
    secondDisposeThrew: false,
    tabindex: "0",
    role: "log",
    label: "Deploy output",
  });
});
