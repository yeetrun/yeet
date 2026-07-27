# Web Terminal Container Fit Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fit the `yeet run --web` Ghostty grid to the deploy panel so short and live output remains visible without an oversized outer scroll viewport.

**Architecture:** Keep the streamed native profile as a validated bootstrap, then use Ghostty's bundled `FitAddon` measurements to derive browser-owned rows and columns from the panel content box. Remove DOM scrolling, retain Ghostty's internal 1,000-line scrollback and follow controller, and refit through one owned `ResizeObserver` for expand, collapse, responsive layout, and queued source-resize barriers.

**Tech Stack:** Go 1.26 via `mise`, vanilla ES modules and CSS, vendored `ghostty-web` 0.4.0 with `FitAddon`, WebAssembly, `ResizeObserver`, and Playwright 1.51.1.

## Global Constraints

- The browser panel owns browser terminal rows and columns after Ghostty opens.
- The streamed profile remains required, validated, and used only as bootstrap geometry.
- Keep the read-only Ghostty adapter configured with exactly `scrollback: 1000`.
- Render the same ordered terminal byte stream; keep 64 KiB batching, FIFO write, resize, and clear barriers, drain behavior, copying, and disposal.
- Native resize events remain ordered barriers but must not impose native rows or columns on the browser renderer.
- The terminal canvas must stay within the panel content box with no DOM horizontal or vertical scrolling.
- Ghostty's internal viewport is the only scroll layer.
- Manual Ghostty scrollback must remain anchored across writes and panel refits; returning to live output resumes following.
- Expand, collapse, and browser width changes must refit the grid.
- Canvas and partial-cell remainder areas must share the terminal background.
- Retrying in the same form must remove the prior canvas and transcript before new output.
- Clearing resets screen, scrollback, selection, and follow mode while retaining fitted geometry.
- Keep the vendored Ghostty JavaScript and WASM; add no runtime dependency or network request.
- Change no CLI syntax, RPC permission mapping, README, or public manual behavior.
- Preserve unrelated branches and working-tree changes.
- Do not push or land without a separate explicit user request.

---

## File Map

- `pkg/yeet/web_run_assets/terminal.js`: own fitted geometry, single-layer follow state, source-resize barriers, layout observation, and fit failure handling.
- `pkg/yeet/web_run_assets/styles.css`: provide one shared terminal background and prevent DOM overflow.
- `pkg/yeet/web_run_assets/app.js`: verify only; its existing retry path disposes the old adapter and clears the output container before the second request.
- `pkg/yeet/web_run_assets_test.go`: lock the embedded asset contract to `FitAddon`, the 1,000-line scrollback limit, and single-layer overflow styling.
- `tools/web-run-terminal.spec.cjs`: exercise real Ghostty/WASM fitting, expand/collapse, source resize barriers, scrollback, retry, errors, and lifecycle behavior in Chrome.
- `docs/superpowers/specs/2026-07-26-web-terminal-container-fit-design.md`: approved design.
- `docs/superpowers/plans/2026-07-26-web-terminal-container-fit.md`: this plan.

---

### Task 1: Fit the Initial Ghostty Grid and Remove the Outer Scroll Layer

**Files:**

- Modify: `pkg/yeet/web_run_assets/terminal.js:7-267`
- Modify: `pkg/yeet/web_run_assets/styles.css:808-827`
- Modify: `pkg/yeet/web_run_assets_test.go:35-125`
- Modify: `tools/web-run-terminal.spec.cjs:773-1079`

**Interfaces:**

- Consumes: `FitAddon.proposeDimensions(): { cols: number, rows: number } | undefined`.
- Consumes: `Terminal.resize(cols: number, rows: number)`, `Terminal.getViewportY()`, and `Terminal.scrollToBottom()`.
- Produces: private `fitTerminal(): void`, which fits only when the panel has usable dimensions.
- Produces: CSS custom property `--terminal-background` with exact value `#101216`.
- Preserves: public adapter getters `cols` and `rows`, now reporting fitted browser geometry.

- [ ] **Step 1: Install the pinned real-browser test runner outside the repository**

Run:

```bash
browser_test_root=$(mktemp -d -t yeet-playwright.XXXXXX)
npm install --prefix "$browser_test_root" @playwright/test@1.51.1
export NODE_PATH="$browser_test_root/node_modules"
export YEET_PLAYWRIGHT_EXECUTABLE_PATH="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
```

Expected: Playwright 1.51.1 installs only under `browser_test_root`; the
repository remains unchanged.

- [ ] **Step 2: Replace the fixed-geometry browser test with a failing panel-fit regression**

Replace `Ghostty adapter bounds scrollback and keeps explicit geometry` with:

```js
test("Ghostty adapter fits native geometry inside the panel without outer scrolling", async ({ page }) => {
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
      element.className = "terminal-output";
      element.style.width = "560px";
      element.style.height = "96px";
      document.body.appendChild(element);

      const adapter = await createTerminalAdapter(element, { cols: 132, rows: 44 });
      adapter.write(new TextEncoder().encode(
        "line 1\r\nline 2\r\nline 3\r\nline 4\r\nline 5\r\n",
      ));
      await adapter.drain();
      const shortText = adapter.copyText();

      const longOutput = Array.from(
        { length: 1101 },
        (_, index) => `line ${String(index).padStart(4, "0")}${
          index === 900 ? " 界 e\u0301" : ""
        }\r\n`,
      ).join("");
      adapter.write(new TextEncoder().encode(longOutput));
      await adapter.drain();
      const boundedText = adapter.copyText();

      const style = getComputedStyle(element);
      const canvas = element.querySelector("canvas");
      const result = {
        profile: [132, 44],
        fitted: [adapter.cols, adapter.rows],
        client: [element.clientWidth, element.clientHeight],
        content: [
          element.clientWidth - parseFloat(style.paddingLeft) - parseFloat(style.paddingRight),
          element.clientHeight - parseFloat(style.paddingTop) - parseFloat(style.paddingBottom),
        ],
        canvas: [parseFloat(canvas.style.width), parseFloat(canvas.style.height)],
        scrollSize: [element.scrollWidth, element.scrollHeight],
        scrollPosition: [element.scrollLeft, element.scrollTop],
        overflow: [style.overflowX, style.overflowY],
        backgroundToken: style.getPropertyValue("--terminal-background").trim(),
        terminalBackground: terminal.options.theme.background,
        shortText,
        boundedText,
      };
      adapter.dispose();
      return result;
    } finally {
      Terminal.prototype.open = originalOpen;
    }
  });

  expect(result.fitted).not.toEqual(result.profile);
  expect(result.fitted[0]).toBeGreaterThan(1);
  expect(result.fitted[1]).toBeGreaterThan(0);
  expect(result.canvas[0]).toBeLessThanOrEqual(result.content[0]);
  expect(result.canvas[1]).toBeLessThanOrEqual(result.content[1]);
  expect(result.scrollSize).toEqual(result.client);
  expect(result.scrollPosition).toEqual([0, 0]);
  expect(result.overflow).toEqual(["hidden", "hidden"]);
  expect(result.backgroundToken).toBe("#101216");
  expect(result.terminalBackground).toBe(result.backgroundToken);
  expect(result.shortText).toContain("line 1");
  expect(result.shortText).toContain("line 5");
  expect(result.boundedText).toContain("line 1100");
  expect(result.boundedText).toContain("line 0900 界  e\u0301");
  expect(result.boundedText).not.toContain("line 0000");
  expect(result.boundedText.split("\n").length).toBeLessThanOrEqual(1002 + result.fitted[1]);
});
```

This catches the reproduced 1320-by-704 canvas, the `scrollTop=578` blank-row
failure, independent canvas/container colors, and either DOM overflow axis
becoming scrollable.

- [ ] **Step 3: Change the embedded-asset contract before production code**

In `TestWebRunAssetsContainTerminalContracts`, require:

```go
`import { FitAddon, Ghostty, Terminal } from "./ghostty-web.js";`,
`scrollback: 1000`,
`new FitAddon()`,
`--terminal-background: #101216`,
`overflow: hidden`,
```

Remove `overflow-x: auto` and `overflow-y: auto` from the required style
snippets. Add them to the forbidden terminal style checks:

```go
for _, forbidden := range []string{
	"overflow-x: auto",
	"overflow-y: auto",
} {
	if strings.Contains(styleSource, forbidden) {
		t.Fatalf("terminal styles retain outer scrolling %q", forbidden)
	}
}
```

- [ ] **Step 4: Run both regressions and verify the RED state**

Run:

```bash
mise exec -- go test ./pkg/yeet -run TestWebRunAssetsContainTerminalContracts -count=1
"$browser_test_root/node_modules/.bin/playwright" \
  test tools/web-run-terminal.spec.cjs \
  --grep "fits native geometry inside the panel"
```

Expected:

- the Go test fails because `terminal.js` does not import or instantiate
  `FitAddon` and CSS still uses two auto overflow axes;
- the browser test fails because fitted geometry remains `[132, 44]`,
  `scrollTop` is positive, and the canvas exceeds the content box.

- [ ] **Step 5: Add the shared terminal surface and suppress DOM scrolling**

Change the terminal styles to:

```css
.terminal-output {
  --terminal-background: #101216;
  position: relative;
  height: 150px;
  max-height: 42vh;
  margin: 0;
  padding: 12px;
  color: oklch(0.89 0.018 155);
  background: var(--terminal-background);
  overflow: hidden;
}
```

Keep the existing canvas block rule and expanded height rule unchanged.

- [ ] **Step 6: Add minimal fitted geometry to the adapter**

Change the import:

```js
import { FitAddon, Ghostty, Terminal } from "./ghostty-web.js";
```

Before constructing `Terminal`, read the shared background:

```js
const terminalBackground =
  getComputedStyle(element).getPropertyValue("--terminal-background").trim() || "#101216";
```

Add it to terminal options without changing the existing options:

```js
theme: { background: terminalBackground },
```

Load `FitAddon` before opening:

```js
const fitAddon = new FitAddon();
terminal.loadAddon(fitAddon);
terminal.open(element);
restoreViewportSemantics();
```

Replace the dual-layer follow helpers with Ghostty-only follow state:

```js
function updateFollowState() {
  if (disposed || failure || suppressTerminalScroll) return;
  following = terminal.getViewportY() === 0;
}

function alignFollowedViewport() {
  if (disposed || failure || !following) return;
  suppressTerminalScroll = true;
  try {
    terminal.scrollToBottom();
  } finally {
    suppressTerminalScroll = false;
  }
}

function fitTerminal() {
  if (disposed || failure) return;
  const dimensions = fitAddon.proposeDimensions();
  if (!dimensions) return;
  if (terminal.cols !== dimensions.cols || terminal.rows !== dimensions.rows) {
    terminal.resize(dimensions.cols, dimensions.rows);
  }
  alignFollowedViewport();
}
```

Remove `elementAtBottom()`, `element.scrollTop = element.scrollHeight`, the
element's DOM `scroll` subscription, and the old `ResizeObserver` that only
calls `alignFollowedViewport()`. Retain the Ghostty `terminal.onScroll`
subscription, invoke `fitTerminal()` once, and return the adapter. Task 2 adds
the replacement layout observer.

Remove the matching outer-scroll listener and old resize-observer cleanup from
`dispose()`; retain `terminalScroll.dispose()`.

- [ ] **Step 7: Run the focused GREEN tests**

Run:

```bash
mise exec -- go test ./pkg/yeet -run TestWebRunAssetsContainTerminalContracts -count=1
"$browser_test_root/node_modules/.bin/playwright" \
  test tools/web-run-terminal.spec.cjs \
  --grep "fits native geometry inside the panel|preserves terminal bytes"
```

Expected: all selected tests pass; short output remains in Ghostty's fitted
screen and the element has no DOM scroll offset.

- [ ] **Step 8: Commit the initial fit behavior**

Verify that only the four Task 1 files are uncommitted with `but diff`, then:

```bash
but commit codex/web-terminal-panel-fit -m "web: fit deploy terminal to its panel"
```

Expected: one checkpoint commit containing the failing-first regressions and
initial container fit.

---

### Task 2: Refit on Layout Changes and Keep Native Resize Events as Barriers

**Files:**

- Modify: `pkg/yeet/web_run_assets/terminal.js:34-267`
- Modify: `tools/web-run-terminal.spec.cjs:773-830,1136-1315`

**Interfaces:**

- Consumes: Task 1's `fitAddon`, `fitTerminal()`, `following`, and existing FIFO queue.
- Produces: private `scheduleFit(): void`.
- Produces: one owned `ResizeObserver` and at most one scheduled animation frame.
- Preserves: `adapter.resize(cols, rows): void`; arguments remain validated by `app.js`, but browser rendering refits to the panel.

- [ ] **Step 1: Replace the expand fixed-geometry assertion with a failing refit regression**

Rename the existing app-level test to:

```js
test("web run copies through the adapter and expand refits terminal geometry", async ({ page }) => {
```

Keep its copy and accessibility assertions. Capture the last fitted dimensions
after the terminal initializes:

```js
const initial = await page.evaluate(() => window.__terminalResizes.at(-1));
```

After clicking Expand, wait for a new resize with more rows:

```js
await page.click("#terminalExpand");
await expect(page.locator("#terminalExpand")).toHaveAttribute("aria-expanded", "true");
await page.waitForFunction(
  ([cols, rows]) => {
    const [nextCols, nextRows] = window.__terminalResizes.at(-1);
    return nextCols === cols && nextRows > rows;
  },
  initial,
);
const expanded = await page.evaluate(() => window.__terminalResizes.at(-1));
```

After clicking Collapse, wait until rows decrease:

```js
await page.click("#terminalExpand");
await expect(page.locator("#terminalExpand")).toHaveAttribute("aria-expanded", "false");
await page.waitForFunction(
  ([cols, rows]) => {
    const [nextCols, nextRows] = window.__terminalResizes.at(-1);
    return nextCols === cols && nextRows < rows;
  },
  expanded,
);
const collapsed = await page.evaluate(() => window.__terminalResizes.at(-1));

expect(expanded[0]).toBe(initial[0]);
expect(expanded[1]).toBeGreaterThan(initial[1]);
expect(collapsed[0]).toBe(initial[0]);
expect(collapsed[1]).toBeLessThan(expanded[1]);
```

- [ ] **Step 2: Rewrite the queued resize test to require fitted geometry on both sides**

In `Ghostty adapter preserves resize barriers between byte writes`, give the
element explicit panel dimensions:

```js
element.className = "terminal-output";
element.style.width = "600px";
element.style.height = "160px";
```

After adapter creation, capture fitted geometry and clear instrumentation from
the bootstrap fit:

```js
const fitted = { cols: adapter.cols, rows: adapter.rows };
seen.length = 0;
```

Keep the write, delayed callback, source resize, second write, and drain flow.
Return `fitted` with `beforeCallback` and `seen`. Assert:

```js
expect(operations.beforeCallback).toEqual([
  { type: "write", ...operations.fitted, text: "before" },
]);
expect(operations.seen).toEqual([
  { type: "write", ...operations.fitted, text: "before" },
  { type: "write", ...operations.fitted, text: "after" },
]);
```

This proves the source resize waits behind the first write but cannot restore
132-by-44 browser geometry.

- [ ] **Step 3: Add a zero-sized panel activation regression**

Add:

```js
test("Ghostty adapter fits when a zero-sized panel becomes usable", async ({ page }) => {
  await openFixture(page);

  const result = await page.evaluate(async () => {
    const { createTerminalAdapter } = await import("/terminal.js");
    const element = document.createElement("div");
    element.className = "terminal-output";
    element.style.display = "none";
    element.style.width = "560px";
    element.style.height = "96px";
    document.body.appendChild(element);

    const adapter = await createTerminalAdapter(element, { cols: 132, rows: 44 });
    const hidden = [adapter.cols, adapter.rows];
    element.style.display = "block";
    for (
      let attempts = 0;
      attempts < 60 && adapter.cols === hidden[0] && adapter.rows === hidden[1];
      attempts += 1
    ) {
      await new Promise((resolve) => requestAnimationFrame(resolve));
    }
    const visible = [adapter.cols, adapter.rows];
    const canvas = element.querySelector("canvas");
    const style = getComputedStyle(element);
    const content = [
      element.clientWidth - parseFloat(style.paddingLeft) - parseFloat(style.paddingRight),
      element.clientHeight - parseFloat(style.paddingTop) - parseFloat(style.paddingBottom),
    ];
    const canvasSize = [parseFloat(canvas.style.width), parseFloat(canvas.style.height)];
    adapter.dispose();
    return { hidden, visible, content, canvasSize };
  });

  expect(result.hidden).toEqual([132, 44]);
  expect(result.visible).not.toEqual(result.hidden);
  expect(result.canvasSize[0]).toBeLessThanOrEqual(result.content[0]);
  expect(result.canvasSize[1]).toBeLessThanOrEqual(result.content[1]);
});
```

- [ ] **Step 4: Run all three tests and verify the RED state**

Run:

```bash
"$browser_test_root/node_modules/.bin/playwright" \
  test tools/web-run-terminal.spec.cjs \
  --grep "expand refits terminal geometry|preserves resize barriers|zero-sized panel"
```

Expected:

- expand times out because no layout observer refits Ghostty;
- the resize-barrier test observes `{ cols: 132, rows: 44 }` before the second
  write;
- the zero-sized terminal remains at bootstrap dimensions after becoming
  visible.

- [ ] **Step 5: Add one scheduled layout-fit path**

Add adapter state:

```js
let scheduledFit = null;
```

Add:

```js
function scheduleFit() {
  if (disposed || failure || scheduledFit !== null) return;
  scheduledFit = requestAnimationFrame(() => {
    scheduledFit = null;
    try {
      fitTerminal();
    } catch (error) {
      fail(error);
    }
  });
}
```

Create and observe after the helper definitions:

```js
const resizeObserver = new ResizeObserver(scheduleFit);
resizeObserver.observe(element);
fitTerminal();
```

For both immediate and queued `adapter.resize(cols, rows)` handling, replace:

```js
terminal.resize(cols, rows);
```

with:

```js
fitTerminal();
```

Keep resize queue objects and FIFO placement unchanged.

In `dispose()`, disconnect the observer and cancel the fit frame:

```js
resizeObserver.disconnect();
if (scheduledFit !== null) {
  cancelAnimationFrame(scheduledFit);
  scheduledFit = null;
}
```

- [ ] **Step 6: Run expand, zero-size, resize-barrier, queue, and disposal tests**

Run:

```bash
"$browser_test_root/node_modules/.bin/playwright" \
  test tools/web-run-terminal.spec.cjs \
  --grep "expand refits terminal geometry|zero-sized panel|preserves resize barriers|bounds write batches|disposal"
```

Expected: all selected tests pass with fitted dimensions before and after
source resize barriers.

- [ ] **Step 7: Commit responsive fitting**

Verify that only the Task 2 terminal and browser-test files are uncommitted
with `but diff`, then:

```bash
but commit codex/web-terminal-panel-fit -m "web: refit deploy terminal with its panel"
```

---

### Task 3: Preserve Paused Scrollback Across Refits and Route Fit Failures

**Files:**

- Modify: `pkg/yeet/web_run_assets/terminal.js:34-267`
- Modify: `tools/web-run-terminal.spec.cjs:966-1079,1236-1315`

**Interfaces:**

- Consumes: existing `terminalScrollbackPosition()` and `restorePausedTerminalViewport(viewportY, scrollbackPosition)`.
- Consumes: Task 2's `fitTerminal()` and permanent `fail(error)` path.
- Preserves: one logical paused Ghostty scrollback anchor across writes and panel refits.
- Produces: permanent adapter failure when `FitAddon.proposeDimensions()` or fitted `Terminal.resize()` throws.

- [ ] **Step 1: Replace the outer-scroll test with a failing paused-refit test**

Replace `Ghostty adapter follows the outer viewport until the user scrolls up`
with:

```js
test("Ghostty adapter preserves manual scrollback while the panel refits", async ({ page }) => {
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
      element.className = "terminal-output";
      element.style.width = "600px";
      element.style.height = "160px";
      document.body.appendChild(element);
      const adapter = await createTerminalAdapter(element, { cols: 132, rows: 44 });
      const output = Array.from(
        { length: 120 },
        (_, index) => `line ${String(index).padStart(3, "0")}\r\n`,
      ).join("");
      adapter.write(new TextEncoder().encode(output));
      await adapter.drain();

      terminal.scrollToLine(10);
      const before = {
        cols: adapter.cols,
        rows: adapter.rows,
        viewport: terminal.getViewportY(),
      };

      element.style.width = "420px";
      element.style.height = "320px";
      for (
        let attempts = 0;
        attempts < 60 && adapter.cols === before.cols && adapter.rows === before.rows;
        attempts += 1
      ) {
        await new Promise((resolve) => requestAnimationFrame(resolve));
      }
      const afterFit = {
        cols: adapter.cols,
        rows: adapter.rows,
        viewport: terminal.getViewportY(),
      };

      adapter.write(new TextEncoder().encode("new 1\r\nnew 2\r\nnew 3\r\n"));
      await adapter.drain();
      const afterWrite = terminal.getViewportY();

      terminal.scrollToBottom();
      adapter.write(new TextEncoder().encode("live again\r\n"));
      await adapter.drain();
      const afterResume = terminal.getViewportY();
      adapter.dispose();
      return { before, afterFit, afterWrite, afterResume };
    } finally {
      Terminal.prototype.open = originalOpen;
    }
  });

  expect(result.before.viewport).toBe(10);
  expect(result.afterFit.cols).toBeLessThan(result.before.cols);
  expect(result.afterFit.rows).toBeGreaterThan(result.before.rows);
  expect(result.afterFit.viewport).toBe(10);
  expect(result.afterWrite).toBe(13);
  expect(result.afterResume).toBe(0);
});
```

- [ ] **Step 2: Replace the source-dimension failure test with a fit failure test**

Rename the test to:

```js
test("Ghostty adapter permanently fails when a queued panel fit throws", async ({ page }) => {
```

Patch `FitAddon.prototype.proposeDimensions` instead of throwing from
`Terminal.resize`. Replace the test body with:

```js
test("Ghostty adapter permanently fails when a queued panel fit throws", async ({ page }) => {
  await openFixture(page);

  const result = await page.evaluate(async () => {
    const { FitAddon, Terminal } = await import("/ghostty-web.js");
    const originalProposeDimensions = FitAddon.prototype.proposeDimensions;
    const originalWrite = Terminal.prototype.write;
    const OriginalResizeObserver = window.ResizeObserver;
    const pendingCallbacks = [];
    let throwOnFit = false;
    FitAddon.prototype.proposeDimensions = function proposeDimensions() {
      if (throwOnFit) throw new Error("fit failed");
      return originalProposeDimensions.call(this);
    };
    Terminal.prototype.write = function write(bytes, callback) {
      originalWrite.call(this, bytes);
      pendingCallbacks.push(callback);
    };
    window.ResizeObserver = class ResizeObserver {
      observe() {}
      disconnect() {}
    };

    let adapter;
    try {
      const { createTerminalAdapter } = await import("/terminal.js");
      const element = document.createElement("div");
      element.className = "terminal-output";
      element.style.width = "600px";
      element.style.height = "160px";
      document.body.appendChild(element);
      adapter = await createTerminalAdapter(element, { cols: 80, rows: 24 });
      adapter.write(new TextEncoder().encode("before"));
      await new Promise((resolve) => requestAnimationFrame(resolve));

      throwOnFit = true;
      adapter.resize(132, 44);
      adapter.write(new TextEncoder().encode("after"));
      const drainOutcome = adapter.drain().then(
        () => ({ state: "resolved" }),
        (error) => ({ state: "rejected", error }),
      );

      pendingCallbacks.shift()();
      for (let attempts = 0; attempts < 10; attempts += 1) {
        await new Promise((resolve) => requestAnimationFrame(resolve));
      }
      const first = await drainOutcome;

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
      FitAddon.prototype.proposeDimensions = originalProposeDimensions;
      Terminal.prototype.write = originalWrite;
      window.ResizeObserver = OriginalResizeObserver;
    }
  });

  expect(result).toEqual({
    state: "rejected",
    message: "fit failed",
    sameFailure: {
      write: true,
      resize: true,
      clear: true,
      copyText: true,
      drain: true,
    },
  });
});
```

- [ ] **Step 3: Run both tests and verify the RED state**

Run:

```bash
"$browser_test_root/node_modules/.bin/playwright" \
  test tools/web-run-terminal.spec.cjs \
  --grep "preserves manual scrollback while the panel refits|queued panel fit throws"
```

Expected:

- the paused-refit test fails if layout fitting jumps to live output or loses
  the paused viewport;
- the fit-failure test fails until all fitted geometry calls route exceptions
  through `fail(error)`.

If the paused-refit test already passes because Ghostty preserves its viewport
through `Terminal.resize`, retain the characterization test and make no
production change for that behavior.

- [ ] **Step 4: Preserve a paused viewport around fitted resizing only if RED proves it is needed**

If Step 3 shows the viewport changes during fit, wrap fitted resizing with the
same logical anchor model used for writes:

```js
const viewportY = terminal.getViewportY();
const scrollbackPosition = terminalScrollbackPosition();
const preservePausedViewport = !following;
terminal.resize(dimensions.cols, dimensions.rows);
if (preservePausedViewport) {
  restorePausedTerminalViewport(viewportY, scrollbackPosition);
}
```

Keep terminal scroll events suppressed around resize and restoration so the
programmatic resize cannot re-enable follow mode.

If the characterization is already green, do not add this wrapper.

- [ ] **Step 5: Ensure every fit call uses the permanent adapter error path**

Keep synchronous calls inside existing `try/catch` blocks:

- initial `fitTerminal()` during adapter creation must throw to the caller;
- queued source resize calls are caught by `flush()` and passed to `fail`;
- immediate `adapter.resize()` calls catch and pass to `fail`;
- observed layout fits are caught inside `scheduleFit()` and passed to `fail`.

Do not swallow a fit error or continue processing queued bytes after failure.

- [ ] **Step 6: Run every Ghostty adapter and retry regression**

Run:

```bash
"$browser_test_root/node_modules/.bin/playwright" \
  test tools/web-run-terminal.spec.cjs \
  --grep "Ghostty adapter|clears a failed attempt before retry output|expand refits terminal geometry"
```

Expected: all selected tests pass, including 1,000-line scrollback, paused
writes, fitted panel changes, queued source barriers, clear, copying, errors,
and disposal.

- [ ] **Step 7: Commit scrollback and fit failure hardening**

Run `but diff`. If production code changed in Step 4 or Step 5, commit it with
the browser regressions:

```bash
but commit codex/web-terminal-panel-fit -m "web: preserve terminal scrollback while fitting"
```

If Ghostty already preserves paused scrollback and Task 2 already routes every
fit error, commit only the characterization and failure regressions:

```bash
but commit codex/web-terminal-panel-fit -m "test: cover fitted terminal scrollback"
```

---

### Task 4: Run Full Browser, Go, Security, and Quality Gates

**Files:**

- Verify: `pkg/yeet/web_run_assets/terminal.js`
- Verify: `pkg/yeet/web_run_assets/styles.css`
- Verify: `pkg/yeet/web_run_assets_test.go`
- Verify: `tools/web-run-terminal.spec.cjs`
- Verify: repository-wide tests and quality tooling

**Interfaces:**

- Consumes: completed container-fit adapter and regressions.
- Produces: a clean local GitButler branch ready for the user's chosen
  integration path.

- [ ] **Step 1: Run the complete real-browser regression suite**

Run:

```bash
"$browser_test_root/node_modules/.bin/playwright" \
  test tools/web-run-terminal.spec.cjs
```

Expected: every browser test passes.

- [ ] **Step 2: Run targeted and full Go tests**

Run:

```bash
mise exec -- go test ./pkg/yeet -count=1
mise exec -- go test ./... -count=1
```

Expected: both commands pass.

- [ ] **Step 3: Run the vulnerability gate explicitly**

Run:

```bash
mise exec -- govulncheck ./...
```

Expected: exit 0 with no reachable vulnerabilities.

- [ ] **Step 4: Run deterministic pre-commit and quality gates with an isolated lint cache**

Run:

```bash
golangci_cache_root=$(mktemp -d -t yeet-golangci.XXXXXX)
GOLANGCI_LINT_CACHE="$golangci_cache_root" mise exec -- pre-commit run --all-files
GOLANGCI_LINT_CACHE="$golangci_cache_root" mise run quality
```

Expected: every pre-commit hook passes; coverage, CRAP, golangci, private-info,
depaware, and hotspot checks remain clean. Do not refresh a baseline or remove
the shared global cache to obtain green output.

- [ ] **Step 5: Verify diff integrity and branch scope**

Run:

```bash
git diff --check origin/main...codex/web-terminal-panel-fit
git diff --stat origin/main...codex/web-terminal-panel-fit
git log --oneline origin/main..codex/web-terminal-panel-fit
but status -fv
```

Expected:

- no whitespace errors or uncommitted changes;
- only the approved spec, this plan, terminal adapter, terminal CSS, embedded
  asset contract test, and browser regressions differ from `origin/main`;
- the branch is based directly on current `origin/main`.

- [ ] **Step 6: Stop before publication**

Report the local commits and exact verification results. Do not push, land,
clean, or update `main` until the user explicitly requests publication.
