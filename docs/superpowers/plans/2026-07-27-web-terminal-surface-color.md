# Web Terminal Surface Color Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the blue-biased rectangle inside the deploy terminal by giving the terminal sheet, output viewport, and Ghostty canvas one shared green-neutral background.

**Architecture:** Define `--terminal-background` once at the root with the terminal sheet's existing `oklch(0.14 0.011 165)` value. Use that token for both CSS surfaces, rasterize its computed color to an opaque integer `rgb(r, g, b)` value for Ghostty 0.4.0/WASM, and fall back to the same root token without changing terminal geometry, scrolling, or lifecycle behavior.

**Tech Stack:** Vanilla CSS and ES modules, vendored `ghostty-web` 0.4.0, Go embedded-asset contract tests, and Playwright 1.51.1 with system Chrome.

## Global Constraints

- The exact shared terminal surface is `oklch(0.14 0.011 165)`.
- The terminal sheet and output viewport must use that exact CSS surface; Ghostty's parsed RGB default background must be its opaque equivalent.
- Keep an explicit opaque background; do not use transparency.
- Do not change terminal fitting, scrolling, batching, barriers, clearing, copying, failure, or disposal behavior.
- Keep `scrollback: 1000`.
- Add no dependency, network request, CLI syntax, RPC permission, README, or public manual change.
- Preserve unrelated branches and working-tree changes.
- Do not push or land without a separate explicit user request.

---

## File Map

- `pkg/yeet/web_run_assets/styles.css`: define and consume the single terminal background token.
- `pkg/yeet/web_run_assets/terminal.js`: derive Ghostty's opaque RGB background from the CSS token and fall back to the same root token.
- `pkg/yeet/web_run_assets_test.go`: lock the embedded CSS contract to one exact token used by both surfaces.
- `tools/web-run-terminal.spec.cjs`: prove the sheet, viewport, and Ghostty theme resolve to the same color in Chrome.

---

### Task 1: Share the Terminal Sheet Surface with Ghostty

**Files:**

- Modify: `pkg/yeet/web_run_assets/styles.css:8-21,725-820`
- Modify: `pkg/yeet/web_run_assets/terminal.js:18-21`
- Modify: `pkg/yeet/web_run_assets_test.go:105-123`
- Modify: `tools/web-run-terminal.spec.cjs:935-1010`

**Interfaces:**

- Consumes: inherited CSS custom property `--terminal-background`.
- Produces: exact root token `--terminal-background: oklch(0.14 0.011 165)`.
- Preserves: `createTerminalAdapter(element, profile)` and the full adapter API unchanged.

- [ ] **Step 1: Strengthen the static and real-browser color contracts**

In `pkg/yeet/web_run_assets_test.go`, replace the old blue token expectation:

```go
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
```

In `tools/web-run-terminal.spec.cjs`, extend
`Ghostty adapter fits native geometry inside the panel without outer scrolling`
to capture the actual sheet and output colors:

```js
const style = getComputedStyle(element);
const sheetStyle = getComputedStyle(document.querySelector("#terminalSheet"));
const canvas = element.querySelector("canvas");
const result = {
  // Existing geometry, scroll, text, and scrollback fields stay unchanged.
  outputBackground: style.backgroundColor,
  sheetBackground: sheetStyle.backgroundColor,
  backgroundToken: style.getPropertyValue("--terminal-background").trim(),
  terminalBackground: terminal.options.theme.background,
  wasmBackground: terminal.buildWasmConfig().bgColor,
};
```

Replace the old `#101216` assertion with:

```js
expect(result.backgroundToken).toBe("oklch(0.14 0.011 165)");
expect(result.outputBackground).toBe(result.sheetBackground);
expect(result.terminalBackground).toMatch(/^rgb\\(\\d+, \\d+, \\d+\\)$/);
expect(result.wasmBackground).toBe(/* packed CSS RGB value */);
```

- [ ] **Step 2: Run the focused tests to establish RED**

Run:

```bash
mise exec -- go test ./pkg/yeet \
  -run TestWebRunAssetsContainTerminalContracts -count=1
```

Expected: FAIL because the assets still define `--terminal-background:
#101216` and only the output viewport uses it.

Install the pinned browser runner outside the repository when no compatible
runner is already available:

```bash
browser_test_root=$(mktemp -d -t yeet-playwright.XXXXXX)
npm install --prefix "$browser_test_root" @playwright/test@1.51.1
export NODE_PATH="$browser_test_root/node_modules"
export YEET_PLAYWRIGHT_EXECUTABLE_PATH="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
```

Run:

```bash
"$browser_test_root/node_modules/.bin/playwright" \
  test tools/web-run-terminal.spec.cjs \
  --grep "Ghostty adapter fits native geometry inside the panel"
```

Expected: FAIL because the sheet uses the green-neutral surface while the
output and Ghostty theme still use the blue-biased code surface.

- [ ] **Step 3: Implement the single shared surface token**

Add the approved token to `:root` in
`pkg/yeet/web_run_assets/styles.css`:

```css
--terminal-background: oklch(0.14 0.011 165);
```

Use it for the terminal sheet:

```css
.terminal-sheet {
  /* Existing declarations stay unchanged. */
  background: var(--terminal-background);
}
```

Remove the local `--terminal-background: #101216` declaration from
`.terminal-output` while retaining:

```css
.terminal-output {
  /* Existing geometry, spacing, and color declarations stay unchanged. */
  background: var(--terminal-background);
}
```

Resolve the CSS token through a one-pixel canvas before constructing Ghostty.
The canvas supplies Ghostty's supported opaque integer RGB syntax while keeping
the CSS token authoritative; if the output token is unset, read the same token
from `document.documentElement` rather than duplicating a color constant:

```js
const [red, green, blue, alpha] = context.getImageData(0, 0, 1, 1).data;
if (alpha !== 255) throw new Error("terminal background must be opaque");
return `rgb(${red}, ${green}, ${blue})`;
```

- [ ] **Step 4: Run the focused tests to establish GREEN**

Run:

```bash
mise exec -- go test ./pkg/yeet \
  -run TestWebRunAssetsContainTerminalContracts -count=1
"$browser_test_root/node_modules/.bin/playwright" \
  test tools/web-run-terminal.spec.cjs \
  --grep "Ghostty adapter fits native geometry inside the panel"
```

Expected: both commands PASS. The browser result reports identical CSS sheet
and output backgrounds; Ghostty receives their opaque parsed RGB equivalent,
including through its WASM default-background and inverse-video path.

- [ ] **Step 5: Verify the rendered deploy terminal visually**

Run:

```bash
"$browser_test_root/node_modules/.bin/playwright" \
  test tools/web-run-terminal.spec.cjs \
  --grep "web run terminal renders CRLF TTY output"
find test-results -name web-terminal.png -print
```

Inspect the generated `web-terminal.png`. Expected: no blue rectangle or
color seam appears between the Ghostty canvas, viewport padding, and terminal
sheet; text contrast remains clear.

- [ ] **Step 6: Run the complete verification gates**

Run:

```bash
"$browser_test_root/node_modules/.bin/playwright" \
  test tools/web-run-terminal.spec.cjs
mise exec -- go test ./pkg/yeet -count=1
mise exec -- go test ./... -count=1
mise exec -- govulncheck ./...
lint_cache=$(mktemp -d -t yeet-terminal-surface-lint.XXXXXX)
GOLANGCI_LINT_CACHE="$lint_cache" mise exec -- pre-commit run --all-files
git diff --check
```

Expected: 50 Playwright tests pass; targeted and full Go suites pass;
govulncheck reports zero reachable vulnerabilities; all pre-commit hooks pass;
the diff is whitespace-clean.

- [ ] **Step 7: Commit only the focused follow-up**

Run `but diff`, copy the four file IDs for the CSS, adapter fallback, Go
contract test, and Playwright regression, then pass those exact
comma-separated IDs to one `but commit codex/web-terminal-surface` command
with message `web: blend terminal with its panel`.

Expected: one new local implementation commit on
`codex/web-terminal-surface`, no remaining uncommitted changes, and no push.
