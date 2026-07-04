// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

const fs = require("node:fs");
const path = require("node:path");
const { test, expect } = require("@playwright/test");

const repoRoot = path.resolve(__dirname, "..");
const assetRoot = path.join(repoRoot, "pkg", "yeet", "web_run_assets");

function readAsset(name) {
  return fs.readFileSync(path.join(assetRoot, name), "utf8");
}

function mockRuntimeScript(options = {}) {
  const prefill = options.prefill || { service: "nginx", payload: "nginx:latest" };
  const delayedHostStorageServices = options.delayedHostStorageServices || [];
  return `
    const delayedHostStorageServices = new Set(${JSON.stringify(delayedHostStorageServices)});
    function json(data, status = 200) {
      return new Response(JSON.stringify(data), {
        status,
        headers: { "Content-Type": "application/json" },
      });
    }
    function text(data, status = 200) {
      return new Response(data, { status, headers: { "Content-Type": "text/plain" } });
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
    window.fetch = async (url) => {
      const target = String(url);
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
      if (target.startsWith("/api/files")) return json({ dir: ".", entries: [] });
      if (target === "/api/validate") {
        window.__validateRequests = (window.__validateRequests || 0) + 1;
        return json({ validation: { ok: true, errors: [], warnings: [] } });
      }
      if (target === "/api/deploy") return json({ jobId: "job-1" });
      if (target === "/api/deploy/job-1/status") return json({ state: "succeeded" });
      if (target.startsWith("/api/session/closed")) return text("", 204);
      return text("unexpected fetch " + target, 404);
    };
    class MockEventSource {
      constructor() {
        this.listeners = {};
        setTimeout(() => {
          this.dispatch("open", {});
          const output = [
            "[+] yeet run nginx@catch-lab",
            "✔ Upload payload (103.00 B @ 285.57 B/s)",
            "✔ Detect payload (Docker Compose)",
            "[+] up 2/2",
            " ✔ Network catch-nginx_default   Created 0.0s",
            " ✔ Container catch-nginx-nginx-1 Started 0.4s",
            "✔ Install service",
          ].join("\\r\\n") + "\\r\\n";
          this.dispatch("output", { data: JSON.stringify({ encoding: "base64", chunk: base64(output) }) });
          this.dispatch("status", { data: JSON.stringify({ state: "succeeded" }) });
        }, 0);
      }
      addEventListener(type, listener) {
        if (!this.listeners[type]) this.listeners[type] = [];
        this.listeners[type].push(listener);
      }
      close() {}
      dispatch(type, event) {
        for (const listener of this.listeners[type] || []) listener(event);
      }
    }
    window.EventSource = MockEventSource;
  `;
}

function pageHTML(options = {}) {
  return readAsset("index.html")
    .replace('<link rel="stylesheet" href="/styles.css">', `<style>${readAsset("styles.css")}</style>`)
    .replace("__YEET_SESSION_SCRIPT__", "")
    .replace('<script src="/app.js" defer></script>', `<script>${mockRuntimeScript(options)}</script><script>${readAsset("app.js")}</script>`);
}

test("web run terminal renders CRLF TTY output", async ({ page }, testInfo) => {
  await page.setContent(pageHTML(), { waitUntil: "domcontentloaded" });
  await page.waitForFunction(() => {
    const button = document.querySelector("#deployButton");
    return button && !button.disabled;
  });
  await expect(page.locator("#commandPreview")).toContainText("yeet run nginx@catch-lab nginx:latest");
  await page.click("#deployButton");
  await page.waitForFunction(() => document.querySelector("#terminalStatus")?.textContent === "Deployed");

  await expect(page.locator("#terminalSheet")).toHaveCSS("overflow", "hidden");
  await page.screenshot({ path: path.join(testInfo.outputDir, "web-terminal.png"), fullPage: true });
  const output = await page.locator("#terminalOutput").textContent();

  expect(output).toContain("[+] yeet run nginx@catch-lab");
  expect(output).toContain("✔ Upload payload");
  expect(output).toContain("✔ Install service");
});

test("web run clears auto ZFS service root when service is erased", async ({ page }) => {
  await page.setContent(pageHTML(), { waitUntil: "domcontentloaded" });
  await page.waitForFunction(() => document.querySelector("#serviceRoot")?.value === "flash/yeet/services/nginx");
  await expect(page.locator("#zfs")).toBeChecked();

  await page.fill("#service", "n");
  await page.waitForFunction(() => document.querySelector("#serviceRoot")?.value === "flash/yeet/services/n");

  await page.fill("#service", "");
  await page.waitForFunction(() => document.querySelector("#serviceRoot")?.value === "");
  await expect(page.locator("#commandPreview")).not.toContainText("flash/yeet/services/n");
});

test("web run shows ZFS placeholder before service is named", async ({ page }) => {
  await page.setContent(pageHTML({ prefill: { service: "", payload: "nginx:latest" } }), { waitUntil: "domcontentloaded" });
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
  await page.setContent(pageHTML({
    prefill: { service: "", payload: "nginx:latest" },
    delayedHostStorageServices: ["n"],
  }), { waitUntil: "domcontentloaded" });
  await page.waitForFunction(() => document.querySelector("#zfs")?.checked === true);
  await expect(page.locator("#serviceRoot")).toHaveAttribute("placeholder", "flash/yeet/services/<service>");

  await page.fill("#service", "n");

  expect(await page.evaluate(() => Boolean(window.__pendingHostStorage?.n))).toBe(true);
  expect(await page.locator("#serviceRoot").inputValue()).toBe("flash/yeet/services/n");
  expect(await page.locator("#commandPreview").textContent()).toContain("--service-root=flash/yeet/services/n --zfs");
});

test("web run derives ZFS service root from a previous auto default while loading", async ({ page }) => {
  await page.setContent(pageHTML({
    prefill: { service: "nginx", payload: "nginx:latest" },
    delayedHostStorageServices: ["n"],
  }), { waitUntil: "domcontentloaded" });
  await page.waitForFunction(() => document.querySelector("#serviceRoot")?.value === "flash/yeet/services/nginx");

  await page.fill("#service", "n");

  expect(await page.evaluate(() => Boolean(window.__pendingHostStorage?.n))).toBe(true);
  expect(await page.locator("#serviceRoot").inputValue()).toBe("flash/yeet/services/n");
  expect(await page.locator("#commandPreview").textContent()).toContain("--service-root=flash/yeet/services/n --zfs");
});
