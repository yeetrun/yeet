# Charm v2 Terminal Modernization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move Yeet and Catch to Go 1.26.6 and the complete latest stable Charm v2 stack while modernizing interactive terminal presentation without changing streaming, PTY, plain, quiet, or structured-output behavior.

**Architecture:** Keep the existing RPC/PTY-aware progress engine and replace only its raw presentation layer with a small semantic Lip Gloss v2 style API. Huh moves mechanically to v2, while status and info receive terminal-only heading styles that are invisible to pipes and structured formats.

**Tech Stack:** Go 1.26.6, Huh v2.0.3, Bubble Tea v2.0.8, Bubbles v2.1.1, Lip Gloss v2.0.6, `text/tabwriter`, GitButler, pre-commit, govulncheck.

**Spec:** `docs/superpowers/specs/2026-08-18-modern-terminal-and-dependency-upgrade-design.md`

## Global Constraints

- `.mise.toml` and `go.mod` must both select Go 1.26.6.
- Select Huh v2.0.3, Bubble Tea v2.0.8, Bubbles v2.1.1, and Lip Gloss v2.0.6 together.
- Remove the selected legacy `github.com/charmbracelet/{huh,bubbletea,bubbles,lipgloss}` modules.
- Preserve `--progress=auto|tty|plain|quiet` and Catch PTY suspend/resume behavior.
- `NO_COLOR`, `TERM=dumb`, non-TTY writers, JSON, JSON-pretty, plain progress, and quiet progress must emit no presentation ANSI.
- Keep cursor/line-control ANSI in the spinner; Lip Gloss owns presentation color and emphasis only.
- Do not add an alternate screen, a full Bubble Tea program, mouse handling, new CLI flags, RPC changes, or permission changes.
- Do not deploy Catch, cut a release, push, or land on `main` without separate authorization.
- Use `mise exec -- go ...` and commit only this plan's files through GitButler.

---

## File Structure

- Modify `go.mod` and `go.sum` for the Go floor and Charm v2 module graph.
- Modify `pkg/yeet/prompts_huh.go` for the Huh v2 vanity import.
- Replace `pkg/tui/color.go` with `pkg/tui/styles.go`, which owns semantic roles and the enabled policy.
- Modify `pkg/tui/spinner.go` so spinner frames consume semantic roles.
- Modify `pkg/tui/color_test.go` into `pkg/tui/styles_test.go`; retain focused policy tests.
- Modify `pkg/tui/spinner_test.go` for semantic spinner styling and cursor invariants.
- Modify `pkg/yeet/init_ui.go` and tests for semantic progress roles.
- Modify `pkg/catch/run_ui.go` and tests for semantic progress roles.
- Modify `pkg/yeet/init.go`, `pkg/yeet/skirt.go`, and `pkg/yeet/skirt_test.go` to remove `fatih/color`.
- Create `pkg/yeet/output_styles.go` for writer-to-terminal style selection and tabwriter-safe styling.
- Modify `pkg/yeet/svc_cmd.go`, `pkg/yeet/info_cmd.go`, and their render tests for TTY-only headings.
- Modify `website/docs/cli/cli-overview.mdx` to document terminal styling and plain-output guarantees.

### Task 1: Align the module floor and capture the pre-upgrade baseline

**Files:**

- Modify: `go.mod:3`
- Verify: `.mise.toml:1-7`
- Generated locally only: `.tmp/terminal-modernization/before-yeet`
- Generated locally only: `.tmp/terminal-modernization/before-catch`

**Interfaces:**

- Consumes: repository-managed Go 1.26.6 from `.mise.toml`.
- Produces: a Go 1.26.6 module floor and before-upgrade binary sizes used by Task 8.

- [ ] **Step 1: Confirm the secure toolchain and vulnerability baseline**

Run:

```bash
mise exec -- go version
mise run vuln
```

Expected: `go version go1.26.6`; `govulncheck` reports zero reachable vulnerabilities.

- [ ] **Step 2: Record current binary sizes**

Run:

```bash
mkdir -p .tmp/terminal-modernization
mise exec -- go build -o .tmp/terminal-modernization/before-yeet ./cmd/yeet
mise exec -- go build -o .tmp/terminal-modernization/before-catch ./cmd/catch
stat -f '%N %z' .tmp/terminal-modernization/before-yeet .tmp/terminal-modernization/before-catch
```

Expected: both binaries build and their byte sizes are printed.

- [ ] **Step 3: Raise the module floor**

Change the directive to:

```go
go 1.26.6
```

Then run:

```bash
mise exec -- go mod tidy
```

- [ ] **Step 4: Verify the floor change**

Run:

```bash
mise exec -- go test ./... -count=1
mise run vuln
```

Expected: all tests pass and zero reachable vulnerabilities are reported.

- [ ] **Step 5: Commit the isolated floor change**

Run `but diff`, select only `go.mod` and `go.sum` if it changed, and commit those IDs to `codex/charm-v2-terminal-modernization` with message:

```text
build: require Go 1.26.6
```

### Task 2: Migrate Huh and select the complete Charm v2 graph

**Files:**

- Modify: `pkg/yeet/prompts_huh.go:7-12`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**

- Consumes: the existing `yeetPrompter` interface and Huh field `.Run()` behavior.
- Produces: the same `huhPrompter` methods backed by `charm.land/huh/v2` and the approved selected Charm versions.

- [ ] **Step 1: Prove the prompt package compiles before migration**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'Test.*Prompt|TestSelectedDefaultHost' -count=1
```

Expected: the current prompt-related tests pass; record any package test names selected by the regular expression.

- [ ] **Step 2: Change the Huh import path**

Replace:

```go
import "github.com/charmbracelet/huh"
```

with:

```go
import "charm.land/huh/v2"
```

Do not change titles, defaults, placeholders, option order, password echo mode,
or `.Run()` calls.

- [ ] **Step 3: Select all four approved Charm modules together**

Run:

```bash
mise exec -- go get charm.land/huh/v2@v2.0.3 charm.land/bubbletea/v2@v2.0.8 charm.land/bubbles/v2@v2.1.1 charm.land/lipgloss/v2@v2.0.6
mise exec -- go mod tidy
```

Expected: Huh and Lip Gloss are direct requirements as used by source; Bubble
Tea and Bubbles may be indirect but must select the exact approved versions.

- [ ] **Step 4: Verify prompt compatibility and the selected graph**

Run:

```bash
mise exec -- go test ./pkg/yeet -count=1
mise exec -- go list -m all | rg '^(charm.land/(huh|bubbletea|bubbles|lipgloss)/v2) '
mise exec -- go list -m all | rg '^github.com/charmbracelet/(huh|bubbletea|bubbles|lipgloss) ' && exit 1 || true
```

Expected: package tests pass; the first graph command prints exactly the four
approved versions; the legacy-module check prints nothing.

- [ ] **Step 5: Commit the Huh/module migration**

Run `but diff`, select `pkg/yeet/prompts_huh.go`, `go.mod`, and `go.sum`, and
commit them on `codex/charm-v2-terminal-modernization` with message:

```text
deps: upgrade to Charm v2
```

### Task 3: Introduce semantic Lip Gloss styles

**Files:**

- Create: `pkg/tui/styles.go`
- Create: `pkg/tui/styles_test.go`
- Delete: `pkg/tui/color.go`
- Delete: `pkg/tui/color_test.go`

**Interfaces:**

- Consumes: caller-provided enabled state plus `NO_COLOR` and `TERM`.
- Produces: `Role`, `Styles`, `NewStyles(bool) Styles`, `Styles.Enabled() bool`, and `Styles.Render(Role, string) string`.

- [ ] **Step 1: Write failing semantic-style tests**

Create tests covering exact disabled output, environment policy, and each role:

```go
func TestStylesDisabledRenderIsExact(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	if got := NewStyles(false).Render(RoleSuccess, "ready"); got != "ready" {
		t.Fatalf("Render() = %q, want plain text", got)
	}
}

func TestStylesRespectEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name, noColor, term string
	}{
		{name: "no color", noColor: "1", term: "xterm-256color"},
		{name: "dumb", term: "dumb"},
		{name: "missing term"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", tc.noColor)
			t.Setenv("TERM", tc.term)
			if NewStyles(true).Enabled() {
				t.Fatal("styles unexpectedly enabled")
			}
		})
	}
}

func TestStylesRenderEveryRole(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	styles := NewStyles(true)
	for _, role := range []Role{RoleAccent, RoleSuccess, RoleWarning, RoleError, RoleMuted, RoleHeading} {
		got := styles.Render(role, "text")
		if !strings.Contains(got, "\x1b[") || !strings.Contains(got, "text") {
			t.Fatalf("role %v rendered %q", role, got)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify RED**

Run:

```bash
mise exec -- go test ./pkg/tui -run '^TestStyles' -count=1
```

Expected: compile failure because `NewStyles`, `Role`, and the role constants do not exist.

- [ ] **Step 3: Implement the semantic API**

Implement the exact public surface:

```go
type Role uint8

const (
	RoleAccent Role = iota
	RoleSuccess
	RoleWarning
	RoleError
	RoleMuted
	RoleHeading
)

type Styles struct {
	enabled bool
}

func NewStyles(enabled bool) Styles
func (s Styles) Enabled() bool
func (s Styles) Render(role Role, text string) string
```

Use `charm.land/lipgloss/v2`. Return `text` unchanged when disabled. Map roles
to Lip Gloss v2 named colors and make only `RoleHeading` bold. Unknown roles
must return `text` unchanged rather than guessing a presentation policy.

- [ ] **Step 4: Run the focused and package tests**

Run:

```bash
mise exec -- go test ./pkg/tui -count=1
```

Expected: all style tests pass.

- [ ] **Step 5: Commit the semantic style layer**

Run `but diff`, select only the replacement style files, and commit them with:

```text
pkg/tui: add semantic Charm styles
```

### Task 4: Adapt the streaming spinner without changing its lifecycle

**Files:**

- Modify: `pkg/tui/spinner.go`
- Modify: `pkg/tui/spinner_test.go`

**Interfaces:**

- Consumes: `Styles.Render` and `Role` from Task 3.
- Produces: `WithStyle(styles Styles, role Role) SpinnerOption`; preserves all existing `Spinner` lifecycle methods.

- [ ] **Step 1: Replace the color-option test with a failing role test**

Update the styled-frame assertion to use:

```go
styles := NewStyles(true)
spinner := NewSpinner(
	&out,
	WithFrames([]string{"-"}),
	WithInterval(time.Hour),
	WithHideCursor(true),
	WithStyle(styles, RoleSuccess),
)
```

Assert that output begins with cursor hiding plus:

```go
"\r\033[K" + styles.Render(RoleSuccess, "-")
```

Retain assertions for line clear, newline/clear choice, cursor restoration,
updates, repeated start, and repeated stop.

- [ ] **Step 2: Run the spinner test to verify RED**

Run:

```bash
mise exec -- go test ./pkg/tui -run '^TestSpinner' -count=1
```

Expected: compile failure because `WithStyle` does not exist and the old color symbols were removed.

- [ ] **Step 3: Implement `WithStyle`**

Replace the spinner's `Colorizer` and raw frame color fields with:

```go
styles Styles
role   Role
```

Render only the frame through `s.styles.Render(s.role, frame)`. Do not change
the goroutine, locks, channels, timing, carriage returns, line clearing, or
cursor visibility sequences.

- [ ] **Step 4: Verify spinner behavior and race safety**

Run:

```bash
mise exec -- go test ./pkg/tui -count=1
mise exec -- go test -race ./pkg/tui -count=1
```

Expected: both commands pass.

- [ ] **Step 5: Commit the spinner adaptation**

Commit the two spinner files with:

```text
pkg/tui: style streaming spinner by role
```

### Task 5: Migrate Yeet and Catch progress output to semantic roles

**Files:**

- Modify: `pkg/yeet/init_ui.go`
- Modify: `pkg/yeet/init_ui_test.go`
- Modify: `pkg/catch/run_ui.go`
- Modify: relevant `pkg/catch/*run_ui*_test.go` files selected by `rg -l 'RunUI|runUI|newRunUI' pkg/catch --glob '*_test.go'`

**Interfaces:**

- Consumes: `tui.NewStyles`, `tui.Role*`, and `tui.WithStyle`.
- Produces: unchanged init/run UI methods and output modes with semantic TTY styling.

- [ ] **Step 1: Update tests to express semantic output**

Replace expected raw constants with values built from an enabled test style set:

```go
styles := tui.NewStyles(true)
wantSuccess := styles.Render(tui.RoleSuccess, "✔")
wantFailure := styles.Render(tui.RoleError, "✖")
```

Add or retain explicit assertions that plain and quiet modes contain no `\x1b[`.
Keep exact cursor-restoration assertions for TTY mode.

- [ ] **Step 2: Run focused tests to verify RED**

Run:

```bash
mise exec -- go test ./pkg/yeet -run '^TestInitUI' -count=1
mise exec -- go test ./pkg/catch -run 'TestRunUI' -count=1
```

Expected: compile or assertion failures while the production code still uses `Colorizer` and `WithColor`.

- [ ] **Step 3: Migrate the two UI structs**

Store `tui.Styles` instead of `tui.Colorizer`. Use:

```go
tui.NewStyles(enabled)
tui.RoleWarning
tui.RoleSuccess
tui.RoleError
tui.RoleMuted
```

Do not change progress-mode selection, messages, elapsed-time formatting,
suspend/resume logic, writer routing, or the order of emitted lines.

- [ ] **Step 4: Verify focused, package, and race tests**

Run:

```bash
mise exec -- go test ./pkg/tui ./pkg/yeet ./pkg/catch -count=1
mise exec -- go test -race ./pkg/tui ./pkg/yeet ./pkg/catch -run 'Test(Spinner|InitUI|RunUI)' -count=1
```

Expected: all commands pass.

- [ ] **Step 5: Commit the progress migration**

Commit the selected Yeet and Catch UI files with:

```text
tui: apply semantic progress styles
```

### Task 6: Remove `fatih/color` from warning and animation output

**Files:**

- Modify: `pkg/yeet/init.go`
- Modify: `pkg/yeet/init_test.go`
- Modify: `pkg/yeet/skirt.go`
- Modify: `pkg/yeet/skirt_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**

- Consumes: `tui.NewStyles` for enabled policy and Lip Gloss v2 named colors for the intentionally multicolor skirt animation.
- Produces: identical warning text and animation timing without `github.com/fatih/color`.

- [ ] **Step 1: Add warning and `NO_COLOR` characterization tests**

Capture the root-install warning with terminal detection forced on and assert
that it contains `RoleError` styling. Repeat with `NO_COLOR=1` and assert the
same warning text contains no `\x1b[`.

For skirt output, keep the cancellation test and add a helper-level assertion
that a disabled style decision returns the frame text unchanged.

- [ ] **Step 2: Run the focused tests before implementation**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'Test.*(Root.*Warning|Skirt)' -count=1
```

Expected: the new environment-policy assertion fails while `fatih/color` independently decides whether to emit color.

- [ ] **Step 3: Migrate the root warning and skirt animation**

Use shared `tui.Styles` for the warning. In `skirt.go`, use local Lip Gloss v2
styles built from `lipgloss.Red`, `Green`, `Yellow`, `Blue`, `Magenta`, `Cyan`,
and `White`, but render them only when `tui.NewStyles(true).Enabled()` is true.

Keep the home/clear control sequence, frame order, context cancellation, and
sleep duration unchanged.

- [ ] **Step 4: Remove the old dependency and verify**

Run:

```bash
mise exec -- go mod tidy
mise exec -- go test ./pkg/yeet -count=1
mise exec -- go list -m all | rg '^github.com/fatih/color ' && exit 1 || true
```

Expected: tests pass and the module check prints nothing.

- [ ] **Step 5: Commit the dependency removal**

Commit the Yeet files plus `go.mod` and `go.sum` with:

```text
pkg/yeet: unify terminal color handling
```

### Task 7: Add restrained TTY-only status and info headings

**Files:**

- Create: `pkg/yeet/output_styles.go`
- Create: `pkg/yeet/output_styles_test.go`
- Modify: `pkg/yeet/svc_cmd.go`
- Modify: `pkg/yeet/status_render_test.go`
- Modify: `pkg/yeet/info_cmd.go`
- Modify: `pkg/yeet/info_cmd_test.go`
- Modify: `pkg/yeet/svc_cmd_branch_test.go` only if existing error-writer assertions require updated setup.

**Interfaces:**

- Consumes: `tui.NewStyles`, `isTerminalFn`, `io.Writer`, and `tabwriter.Escape`.
- Produces: `outputStyles(io.Writer) tui.Styles` and `tabwriterStyled(string) string`; status/info function signatures remain unchanged.

- [ ] **Step 1: Write writer-policy tests**

Use a fake descriptor-bearing buffer:

```go
type fdBuffer struct {
	bytes.Buffer
	fd uintptr
}

func (w *fdBuffer) Fd() uintptr { return w.fd }
```

Test these cases:

- ordinary `bytes.Buffer` produces disabled styles;
- `fdBuffer` plus `isTerminalFn=true` produces enabled styles;
- `fdBuffer` plus `isTerminalFn=false` produces disabled styles;
- `NO_COLOR=1` disables an otherwise interactive writer.

- [ ] **Step 2: Write failing render tests**

Add a TTY status test that expects the header cells to contain heading ANSI
while the row text remains unchanged. Add a TTY info test that expects section
titles to be styled. Retain the existing buffer tests as exact assertions for
plain bytes and the JSON tests as exact structured-output assertions.

- [ ] **Step 3: Run focused tests to verify RED**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'Test(OutputStyles|RenderStatus|RenderInfo|EncodeInfoOutputFormatsJSON)' -count=1
```

Expected: the new TTY style assertions fail.

- [ ] **Step 4: Implement writer policy and tabwriter-safe headings**

`outputStyles` enables styles only for writers implementing `Fd() uintptr`
whose descriptor passes `isTerminalFn`. `tabwriterStyled` brackets already-
styled text with `tabwriter.Escape`. Initialize status's tabwriter with
`tabwriter.StripEscape` so escape delimiters are removed while ANSI sequences
have zero display width.

Style only status header cells and info section titles with `RoleHeading`.
Do not style status values, labels inside tabwriter columns, service names,
paths, JSON, or JSON-pretty output.

- [ ] **Step 5: Verify plain, styled, error, and structured output**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'Test(OutputStyles|RenderStatus|RenderInfo|EncodeInfoOutputFormatsJSON|RenderStatusTables.*Error)' -count=1
mise exec -- go test ./pkg/yeet -count=1
```

Expected: buffer output remains byte-for-byte plain, TTY tests contain only the
expected heading styles, error writers still return their injected errors, and
all package tests pass.

- [ ] **Step 6: Commit the read-output polish**

Commit the output-style helper and render changes with:

```text
pkg/yeet: polish interactive status output
```

### Task 8: Document guarantees and run final verification

**Files:**

- Modify in the `website/` submodule: `website/docs/cli/cli-overview.mdx`
- Modify in the root repository: `website` gitlink
- Generated locally only: `.tmp/terminal-modernization/after-yeet`
- Generated locally only: `.tmp/terminal-modernization/after-catch`

**Interfaces:**

- Consumes: completed Tasks 1-7.
- Produces: documented styling behavior and a fully verified terminal-modernization candidate.

- [ ] **Step 1: Document current behavior**

Add one concise paragraph near the TTY-command guidance stating:

```text
Yeet styles prompts and progress only when writing to an interactive terminal.
NO_COLOR, redirected output, --progress=plain, --progress=quiet, and structured
JSON formats remain unstyled for scripts and logs.
```

Keep the manual evergreen; do not call the behavior new.

- [ ] **Step 2: Verify module selection and source imports**

Run:

```bash
mise exec -- go list -m all | rg '^(charm.land/(huh|bubbletea|bubbles|lipgloss)/v2) '
mise exec -- go list -m all | rg '^github.com/charmbracelet/(huh|bubbletea|bubbles|lipgloss) ' && exit 1 || true
rg -n 'github.com/charmbracelet/(huh|bubbletea|bubbles|lipgloss)|github.com/fatih/color' --glob '*.go' --glob 'go.mod'
```

Expected: exact approved Charm v2 versions; no legacy core modules or
`fatih/color` imports.

- [ ] **Step 3: Build after-upgrade binaries and compare sizes**

Run:

```bash
mise exec -- go build -o .tmp/terminal-modernization/after-yeet ./cmd/yeet
mise exec -- go build -o .tmp/terminal-modernization/after-catch ./cmd/catch
stat -f '%N %z' .tmp/terminal-modernization/before-yeet .tmp/terminal-modernization/after-yeet .tmp/terminal-modernization/before-catch .tmp/terminal-modernization/after-catch
```

Expected: all four sizes print. Record the deltas in the final implementation
summary; investigate unexpected multi-megabyte growth before landing.

- [ ] **Step 4: Run final repository gates**

Run once on the stable candidate:

```bash
mise exec -- go test ./... -count=1
mise exec -- go test -race ./pkg/tui ./pkg/yeet ./pkg/catch -count=1
mise exec -- pre-commit run --all-files
mise run quality
mise run vuln
```

Expected: every command passes; coverage and quality ratchets are unchanged or
improved; govulncheck reports zero reachable vulnerabilities.

- [ ] **Step 5: Perform local interactive smoke tests**

In a real terminal, run safe read-only or cancellable commands that cover:

```bash
mise exec -- go run ./cmd/yeet status
mise exec -- go run ./cmd/yeet info
TERM=dumb mise exec -- go run ./cmd/yeet status
NO_COLOR=1 mise exec -- go run ./cmd/yeet status
```

Pipe status to a file or `sed -n '1,3p'` and verify no `\x1b[` bytes. Exercise
one local Huh confirmation path that can be cancelled without remote mutation.
Do not install or restart Catch.

- [ ] **Step 6: Prepare the website commit and root gitlink without publishing**

Follow the repository's website-submodule workflow. Commit the documentation
inside `website/` with message:

```text
docs: explain terminal output styling
```

If GitButler resolves the submodule back to the parent workspace, use the
allowed narrow `git -C website commit` exception. Do not push the website
commit without user authorization. Verify the root diff shows exactly that one
website commit, then commit the `website` gitlink and any test-only final
corrections on `codex/charm-v2-terminal-modernization`.

Until the website commit is advertised by the website remote, the root branch
is local-only and must not be landed or tagged. At an authorized publication
boundary, push the website commit first, verify the advertised SHA, and only
then land the root gitlink.

- [ ] **Step 7: Review branch state without publishing**

Run `but status` and `but show codex/charm-v2-terminal-modernization`. Confirm
the branch contains only this project, is based on current `origin/main`, and
has no uncommitted changes. Also record whether the website commit is local or
advertised. Do not push or land it without user authorization.
