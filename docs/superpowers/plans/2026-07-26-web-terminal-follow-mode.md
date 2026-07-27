# Web Terminal Follow Mode Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep `yeet run --web` deploy output pinned to the latest terminal rows while preserving standard pause-and-resume behavior for manual scrollback.

**Architecture:** Add one follow-mode controller inside the Yeet-owned Ghostty adapter. It combines Ghostty's internal viewport position with the outer DOM viewport position, restores paused internal scrollback after Ghostty writes, and re-aligns followed output after writes and element resizes without changing PTY geometry.

**Tech Stack:** Go 1.26 via `mise`, vanilla ES modules, vendored `ghostty-web` 0.4.0, WebAssembly, DOM `scroll` and `ResizeObserver`, Playwright 1.51.1.

## Global Constraints

- Preserve the invoking terminal's exact rows and columns; browser layout must not resize the remote PTY.
- Keep the read-only Ghostty adapter configured with exactly `scrollback: 1000`.
- Start at live output, pause bottom-follow when either terminal viewport scrolls up, and resume only when both return to the bottom.
- Preserve queued byte order, 64 KiB write batching, resize barriers, clear barriers, drain semantics, copy behavior, and disposal behavior.
- Clearing for a retry must reset both viewports to followed live output.
- Starting a second deploy from the same form must clear the first attempt before the second stream can render output.
- The browser must continue using the vendored Ghostty JS and WASM with no new runtime dependency or network request.
- Restore behavior already promised by the web terminal design; do not change CLI help, README, or website documentation.
- Preserve all unrelated branches and working-tree changes.
- Do not push or land the branch without a separate explicit user request.

---

## File Map

- `go.mod`: security baseline only; current `main` already requires `golang.org/x/text v0.39.0`.
- `go.sum`: security baseline only; current `main` already contains the `v0.39.0` checksums.
- `pkg/yeet/web_run_assets/terminal.js`: own follow state, coordinate Ghostty and DOM scrolling, preserve paused scrollback across writes, observe layout resizing, and clean up listeners.
- `pkg/yeet/web_run_assets/app.js`: existing retry boundary to verify; it disposes the prior adapter and clears the output container before starting another deploy.
- `tools/web-run-terminal.spec.cjs`: exercise the real vendored terminal and canvas through bottom-follow, manual scrollback, resizing, clear, and disposal.
- `docs/superpowers/specs/2026-07-26-web-terminal-follow-mode-design.md`: approved design.
- `docs/superpowers/plans/2026-07-26-web-terminal-follow-mode.md`: this implementation plan.

---

### Task 1: Establish a clean current-main security baseline

**Files:**

- Verify: `go.mod`
- Verify: `go.sum`
- Modify only if the current-main checkout has regressed: `go.mod`, `go.sum`

**Interfaces:**

- Consumes: current `origin/main` and the repository-managed Go toolchain.
- Produces: a workspace where `golang.org/x/text` resolves to `v0.39.0` and `govulncheck` does not report `GO-2026-5970`.

- [ ] **Step 1: Work from an isolated GitButler project based on current main**

The existing shared workspace has unrelated Git history and cannot merge with
current `main`. Create a separate checkout:

```bash
terminal_follow_checkout=$(mktemp -d -t yeet-terminal-follow.XXXXXX)
git clone --branch main git@github.com:yeetrun/yeet.git "$terminal_follow_checkout"
cd "$terminal_follow_checkout"
but setup --format agent
but branch new codex/web-terminal-follow-bottom --format agent
```

Use `apply_patch` to add the approved spec and this plan at the paths in the
file map before the first commit. Do not copy or apply any unrelated file from
the original shared workspace.

- [ ] **Step 2: Verify the resolved dependency version**

Run:

```bash
mise exec -- go list -m -f '{{.Version}}' golang.org/x/text
```

Expected: exactly `v0.39.0`.

- [ ] **Step 3: Run the vulnerability gate before terminal work**

Run:

```bash
mise exec -- govulncheck ./...
```

Expected: exit 0 and no reachable `GO-2026-5970` result.

- [ ] **Step 4: Repair the dependency only if Step 2 or Step 3 disproves the current-main baseline**

Run:

```bash
mise exec -- go get golang.org/x/text@v0.39.0
mise exec -- go mod tidy
mise exec -- go list -m -f '{{.Version}}' golang.org/x/text
mise exec -- govulncheck ./...
```

Expected: the module prints `v0.39.0`, `govulncheck` exits 0, and only
`go.mod`/`go.sum` change. If this repair is needed, commit it as
`deps: update x/text for GO-2026-5970` before continuing. If the baseline is
already correct, make no dependency commit.

- [ ] **Step 5: Commit the approved design and implementation plan**

With only the two documentation files uncommitted, run:

```bash
but commit codex/web-terminal-follow-bottom -m "docs: plan web terminal follow mode"
```

Expected: one local commit containing only the approved spec and this plan.

---

### Task 2: Follow the outer terminal viewport and preserve DOM scrollback

**Files:**

- Verify: `pkg/yeet/web_run_assets/app.js:1385-1405,1908-1966`
- Modify: `pkg/yeet/web_run_assets/terminal.js:7-223`
- Test: `tools/web-run-terminal.spec.cjs:888-938`

**Interfaces:**

- Consumes: `Terminal.getViewportY()`, `Terminal.scrollToBottom()`, the
  terminal element's `scrollTop`, `scrollHeight`, and `clientHeight`.
- Produces: private adapter functions `elementAtBottom()`,
  `updateFollowState()`, and `alignFollowedViewport()`.
- Produces: private boolean `following`, initially `true`.

- [ ] **Step 1: Install the pinned Playwright runner outside the repository**

Run:

```bash
browser_test_root=$(mktemp -d -t yeet-playwright.XXXXXX)
npm install --prefix "$browser_test_root" @playwright/test@1.51.1
```

Keep `browser_test_root` for all remaining browser test commands.

- [ ] **Step 2: Add an app-level characterization test for clearing before retry**

Add this test near the existing failed-job recovery tests:

```js
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
```

This test catches moving the retry reset after stream startup, retaining the
old adapter, or allowing the first transcript into the second attempt.

- [ ] **Step 3: Run the retry-clear characterization test**

Run:

```bash
NODE_PATH="$browser_test_root/node_modules" \
  "$browser_test_root/node_modules/.bin/playwright" \
  test tools/web-run-terminal.spec.cjs \
  --grep "clears a failed attempt before retry output"
```

Expected: PASS because `showTerminal()` already disposes the prior adapter and
clears `#terminalOutput` synchronously before the deploy request and stream.
No `app.js` change is needed unless this test disproves the inspected behavior.

- [ ] **Step 4: Add a failing real-browser test for DOM follow, pause, resume, resize, and clear**

Add this Playwright test after the existing explicit-geometry test:

```js
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
```

This test catches removal of initial pinning, unconditional write pinning,
failure to resume, resize jumps while paused, and clear failing to reset.

- [ ] **Step 5: Run the new test and verify it fails for the reported bug**

Run:

```bash
NODE_PATH="$browser_test_root/node_modules" \
  "$browser_test_root/node_modules/.bin/playwright" \
  test tools/web-run-terminal.spec.cjs \
  --grep "follows the outer viewport"
```

Expected: FAIL because `initial.scrollTop` remains `0` and
`initial.atBottom` is false.

- [ ] **Step 6: Add minimal outer follow-state management**

In `createTerminalAdapter`, after the current lifecycle state declarations,
add:

```js
const bottomTolerance = 2;
let following = true;
let suppressTerminalScroll = false;

function elementAtBottom() {
  return element.scrollHeight - element.clientHeight - element.scrollTop <= bottomTolerance;
}

function updateFollowState() {
  if (disposed || suppressTerminalScroll) return;
  following = terminal.getViewportY() === 0 && elementAtBottom();
}

function alignFollowedViewport() {
  if (disposed || !following) return;
  suppressTerminalScroll = true;
  try {
    terminal.scrollToBottom();
    element.scrollTop = element.scrollHeight;
  } finally {
    suppressTerminalScroll = false;
  }
}
```

Subscribe after those functions are defined:

```js
const terminalScroll = terminal.onScroll(updateFollowState);
element.addEventListener("scroll", updateFollowState, { passive: true });
const resizeObserver = new ResizeObserver(alignFollowedViewport);
resizeObserver.observe(element);
alignFollowedViewport();
```

In the Ghostty write callback, call `alignFollowedViewport()` before clearing
`writing`. After immediate and queued resizes, call it once the resize
completes.

In the clear barrier, set `following = true` after reset/clear and then call
`alignFollowedViewport()`.

In `dispose()`, remove and dispose the owned resources before disposing
Ghostty:

```js
resizeObserver.disconnect();
terminalScroll.dispose();
element.removeEventListener("scroll", updateFollowState);
```

- [ ] **Step 7: Run the focused test and existing lifecycle tests**

Run:

```bash
NODE_PATH="$browser_test_root/node_modules" \
  "$browser_test_root/node_modules/.bin/playwright" \
  test tools/web-run-terminal.spec.cjs \
  --grep "follows the outer viewport|bounds write batches|preserves resize barriers|clear removes"
```

Expected: all selected tests pass.

- [ ] **Step 8: Commit the outer follow controller**

With only `terminal.js` and `web-run-terminal.spec.cjs` uncommitted, run:

```bash
but commit codex/web-terminal-follow-bottom -m "web: follow deploy terminal output"
```

Expected: one local checkpoint commit containing the new test and minimal
outer follow behavior.

---

### Task 3: Preserve Ghostty scrollback while output arrives

**Files:**

- Modify: `pkg/yeet/web_run_assets/terminal.js:97-138`
- Test: `tools/web-run-terminal.spec.cjs` immediately after the outer follow test

**Interfaces:**

- Consumes: Task 2's `following`, `suppressTerminalScroll`,
  `updateFollowState()`, and `alignFollowedViewport()`.
- Consumes: `Terminal.getViewportY()`, `Terminal.getScrollbackLength()`, and
  `Terminal.scrollToLine(viewportY)`.
- Produces: private `terminalScrollbackPosition()` and
  `restorePausedTerminalViewport(viewportY, scrollbackPosition)`.

- [ ] **Step 1: Add a failing real-Ghostty test for internal scrollback**

Add:

```js
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
```

This test catches Ghostty's current unconditional `scrollToBottom()` during a
write and verifies that returning to live output resumes normal following.

- [ ] **Step 2: Run the internal scrollback test and verify it fails**

Run:

```bash
NODE_PATH="$browser_test_root/node_modules" \
  "$browser_test_root/node_modules/.bin/playwright" \
  test tools/web-run-terminal.spec.cjs \
  --grep "preserves manual terminal scrollback"
```

Expected: FAIL with `afterPausedWrite` equal to `0`.

- [ ] **Step 3: Restore paused Ghostty viewport state immediately after each write**

Use Ghostty's underlying scrollback position to retain the viewed line even
after the adapter's visible 1,000-line scrollback is full:

```js
function terminalScrollbackPosition() {
  return terminal.wasmTerm?.getNativeScrollbackLength?.() ?? terminal.getScrollbackLength();
}

function restorePausedTerminalViewport(viewportY, scrollbackPosition) {
  if (viewportY <= 0) return;
  const currentScrollbackLength = terminal.getScrollbackLength();
  const growth = Math.max(0, terminalScrollbackPosition() - scrollbackPosition);
  terminal.scrollToLine(Math.min(currentScrollbackLength, viewportY + growth));
}
```

Before `terminal.write`, capture:

```js
const viewportY = terminal.getViewportY();
const scrollbackPosition = terminalScrollbackPosition();
const preservePausedViewport = !following;
```

Wrap the synchronous Ghostty write and restoration so Ghostty's temporary
bottom event cannot change follow state:

```js
suppressTerminalScroll = true;
try {
  terminal.write(batch, () => {
    if (following) alignFollowedViewport();
    writing = false;
    if (disposed) {
      settleDrains();
      return;
    }
    scheduleFlush();
  });
  if (preservePausedViewport) {
    restorePausedTerminalViewport(viewportY, scrollbackPosition);
  }
} finally {
  suppressTerminalScroll = false;
}
```

The callback checks the current `following` value, not the value captured
before the write. A genuine user scroll between the synchronous write and its
render callback therefore pauses follow immediately.

- [ ] **Step 4: Run both follow-mode tests and all terminal-adapter tests**

Run:

```bash
NODE_PATH="$browser_test_root/node_modules" \
  "$browser_test_root/node_modules/.bin/playwright" \
  test tools/web-run-terminal.spec.cjs \
  --grep "Ghostty adapter"
```

Expected: all Ghostty adapter tests pass, including fixed geometry, 1,000-line
scrollback, write batching, resize barriers, clear ordering, errors, disposal,
outer follow, and internal scrollback.

- [ ] **Step 5: Commit internal scrollback preservation**

With only `terminal.js` and `web-run-terminal.spec.cjs` uncommitted, run:

```bash
but commit codex/web-terminal-follow-bottom -m "web: preserve terminal scrollback during output"
```

Expected: one local checkpoint commit containing the failing-first regression
and the Ghostty viewport restoration.

---

### Task 4: Run full regression and quality gates

**Files:**

- Verify: `pkg/yeet/web_run_assets/terminal.js`
- Verify: `tools/web-run-terminal.spec.cjs`
- Verify: repository-wide tests and quality tooling

**Interfaces:**

- Consumes: completed follow-mode controller and regressions.
- Produces: a locally verified GitButler branch ready for review or an
  explicitly authorized finish-to-main operation.

- [ ] **Step 1: Run the entire real-browser regression suite**

Run:

```bash
NODE_PATH="$browser_test_root/node_modules" \
  "$browser_test_root/node_modules/.bin/playwright" \
  test tools/web-run-terminal.spec.cjs
```

Expected: all browser tests pass.

- [ ] **Step 2: Run targeted and full Go tests**

Run:

```bash
mise exec -- go test ./pkg/yeet -count=1
mise exec -- go test ./... -count=1
```

Expected: both commands pass.

- [ ] **Step 3: Run deterministic repository gates**

Run:

```bash
git diff --check
mise exec -- pre-commit run --all-files
mise run quality
```

Expected: all commands pass, including `govulncheck`, private-info scanning,
coverage, golangci, depaware, and hotspot reporting.

- [ ] **Step 4: Review the final diff and branch scope**

Run:

```bash
but diff
but status -fv
```

Expected: no uncommitted changes in the isolated checkout and the session
branch contains only the approved spec, implementation plan, terminal adapter,
and Playwright regression changes. `go.mod` and `go.sum` appear only if Task 1
proved current main had regressed.

- [ ] **Step 5: Stop before publication**

Report the local commit IDs and verification results. Do not push, land, clean,
or update `main` until the user explicitly requests publication.
