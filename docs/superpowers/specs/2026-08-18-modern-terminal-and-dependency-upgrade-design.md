# Modern Terminal and Dependency Upgrade Design

## Status

Approved on 2026-08-18.

This design is an umbrella for three independently testable projects:

1. Go 1.26.6 alignment and Charm v2 terminal modernization.
2. Behavior-preserving Go 1.26 source modernization.
3. A compatibility-tested refresh of the remaining direct Go dependencies.

Each project has its own implementation plan so it can be reviewed, landed,
and rolled back independently.

## Context

Yeet currently uses `github.com/charmbracelet/huh v1.0.0`. That dependency
selects the legacy GitHub-hosted Bubble Tea v1, Bubbles v1, and Lip Gloss v1
module paths. The client only imports Huh directly, while `pkg/tui` implements
its own streaming spinner and raw ANSI color wrapper for local `yeet init` and
Catch-side run progress.

The spinner is not an ordinary single-process animation. Catch writes progress
through RPC and PTY streams, temporarily suspends progress around interactive
prompts and raw subprocess output, and must always restore the cursor. Those
properties make the existing streaming primitive valuable even though its
styling layer is dated.

After updating from `origin/main`, `.mise.toml` already pins Go 1.26.6 and a
fresh `mise run vuln` reports no reachable vulnerabilities. `go.mod` still
declares `go 1.26.3`, so consumers can build with older 1.26 patch releases.
The implementation will align the module floor with the repository and release
toolchain at Go 1.26.6.

The focused baseline is green:

- `mise exec -- go test ./pkg/tui ./pkg/yeet ./pkg/catch -count=1`
- `mise run vuln`

The current `modernize` linter reports 50 findings. A direct-module scan also
reports 14 same-major updates beyond Charm, including a substantial Tailscale
upgrade. Mixing those changes into the terminal migration would obscure
regressions and make rollback unnecessarily broad.

## Upstream Baseline

The approved stable versions as of 2026-08-18 are:

| Component | Approved version | Upstream guidance |
| --- | --- | --- |
| Go | 1.26.6 | Latest stable; Go 1.27 remains unreleased draft material. |
| Huh | v2.0.3 | Latest stable v2. |
| Bubble Tea | v2.0.8 | Latest stable v2. |
| Bubbles | v2.1.1 | Latest stable v2. |
| Lip Gloss | v2.0.6 | Latest stable v2. |
| golangci-lint | v2.12.2 | Already pinned at the latest stable release. |

Huh's v2 guide explicitly requires upgrading Huh, Bubble Tea, Bubbles, and Lip
Gloss together and moving imports to the `charm.land/.../v2` module paths.

Primary references:

- <https://go.dev/dl/>
- <https://go.dev/doc/devel/release>
- <https://go.dev/doc/go1.27>
- <https://github.com/charmbracelet/huh/releases/tag/v2.0.3>
- <https://github.com/charmbracelet/huh/blob/main/UPGRADE_GUIDE_V2.md>
- <https://github.com/charmbracelet/bubbletea/releases/tag/v2.0.8>
- <https://github.com/charmbracelet/bubbletea/blob/main/UPGRADE_GUIDE_V2.md>
- <https://github.com/charmbracelet/bubbles/releases/tag/v2.1.1>
- <https://github.com/charmbracelet/bubbles/blob/main/UPGRADE_GUIDE_V2.md>
- <https://github.com/charmbracelet/lipgloss/releases/tag/v2.0.6>
- <https://github.com/charmbracelet/lipgloss/blob/main/UPGRADE_GUIDE_V2.md>

## Goals

- Require the secure Go 1.26.6 patch release everywhere the repository declares
  or provisions Go.
- Select the complete latest stable Charm v2 stack without retaining the legacy
  Charm core module paths.
- Give Yeet and Catch a small semantic style system for accent, success,
  warning, error, muted, and heading text.
- Modernize prompts and interactive TTY output while preserving all streaming,
  PTY, plain, quiet, JSON, and pipe behavior.
- Remove the duplicate `fatih/color` dependency after its remaining uses move
  to the shared terminal styling layer or Lip Gloss v2.
- Apply Go 1.26 language and standard-library modernizations without changing
  wire formats, persisted formats, or command behavior.
- Refresh remaining direct dependencies in compatibility-focused batches with
  release-note-specific tests.
- Keep every delivery unit small enough to review, bisect, and roll back.

## Non-Goals

- Adopting an unreleased Go 1.27 toolchain or its draft APIs.
- Replacing Yeet with a full-screen alternate-screen Bubble Tea application.
- Replacing the streaming spinner with the Bubbles spinner model.
- Adding mouse input, hover state, a resident dashboard, or a second command
  navigation model.
- Changing `--progress=auto|tty|plain|quiet`, JSON schemas, RPC methods, command
  routing, or permission mappings.
- Styling redirected output or embedding ANSI sequences in machine-readable
  output.
- Upgrading every transitive module independently of the versions selected by
  direct dependencies.
- Migrating from `github.com/miekg/dns` v1 to the separately hosted v2 major.
- Deploying or replacing Catch on a live host, cutting a release, or pushing to
  `main` without separate authorization.

## Architecture

### 1. Go Patch Alignment

`.mise.toml` remains pinned to Go 1.26.6. `go.mod` moves to `go 1.26.6` so the
declared module floor cannot select a toolchain with the standard-library
vulnerabilities fixed in 1.26.6.

This is the first implementation commit. Its acceptance gate is a clean full
test suite and `mise run vuln` reporting zero reachable vulnerabilities.

### 2. Charm v2 Module Migration

`pkg/yeet/prompts_huh.go` changes its import to `charm.land/huh/v2`. The field
construction and `.Run()` calls stay intact because Huh v2 retains those APIs.
Huh v2's default Charm theme is used instead of introducing a Yeet-specific
fork of the prompt style.

The module graph explicitly selects:

- `charm.land/huh/v2@v2.0.3`
- `charm.land/bubbletea/v2@v2.0.8`
- `charm.land/bubbles/v2@v2.1.1`
- `charm.land/lipgloss/v2@v2.0.6`

`go mod tidy` may mark Bubble Tea or Bubbles indirect, but the selected module
versions must remain the approved versions. Verification must prove that the
legacy `github.com/charmbracelet/huh`, `bubbletea`, `bubbles`, and `lipgloss`
modules are no longer in the selected build list.

Huh's form-level accessibility policy remains authoritative. `TERM=dumb`
continues to select accessible prompting automatically. This project does not
add a new global accessibility flag.

### 3. Semantic Terminal Styles

Replace `pkg/tui/color.go`'s exported raw color constants with a small semantic
API:

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

`NewStyles` disables styling when the caller disables it, `NO_COLOR` is set,
or `TERM` is empty or `dumb`. `Render` returns the original text byte-for-byte
when disabled. When enabled, it uses Lip Gloss v2 and conservative terminal
colors: accent/warning yellow, success green, error red, muted bright black,
and heading cyan plus bold.

The API deliberately returns styled strings because the existing progress
renderers assemble cursor-control output around text. Raw ANSI remains only for
terminal control operations such as carriage return, line clearing, and cursor
visibility; Lip Gloss owns presentation styling.

`pkg/tui.Spinner` keeps its concurrency and lifecycle. Its style option becomes:

```go
func WithStyle(styles Styles, role Role) SpinnerOption
```

The spinner frame is rendered through `Styles.Render`. No Bubble Tea program,
tick command, alternate screen, or global renderer is introduced.

### 4. Yeet and Catch Progress Rendering

`pkg/yeet/init_ui.go` and `pkg/catch/run_ui.go` replace color-code calls with
semantic roles:

- active spinner: `RoleWarning`
- completed check: `RoleSuccess`
- failed check: `RoleError`
- secondary detail: `RoleMuted`

All existing progress modes remain exact:

- `tty` may redraw, clear lines, hide the cursor, and emit styles;
- `plain` emits stable newline-delimited records with no escape sequences;
- `quiet` suppresses progress;
- `auto` chooses from the existing TTY and PTY rules.

Catch's suspend/resume behavior around interactive prompts, Docker Compose
output, and PTY forwarding is unchanged.

The remaining `fatih/color` uses move as follows:

- the root-install warning in `pkg/yeet/init.go` uses `RoleError` only when
  stderr is a color-enabled terminal;
- `pkg/yeet/skirt.go` uses local Lip Gloss styles for its intentional rainbow
  animation and respects the shared enabled decision.

After those migrations, `github.com/fatih/color` is removed by `go mod tidy`.

### 5. Restrained TTY-Only Read Output

Only presentation-only headings are styled in this project:

- the completed header line of `yeet status` table output;
- section titles in `yeet info` and host info output.

Rows continue to use `text/tabwriter`. Styling is applied after layout or
outside the tabwriter so ANSI bytes cannot distort column widths. Status cell
contents, service names, host names, paths, and values remain unchanged.

Styling is enabled only when the destination implements `Fd() uintptr`, the
existing `isTerminalFn` reports that descriptor is a terminal, and the shared
style policy permits color. Buffers, pipes, files, JSON, and JSON-pretty output
remain byte-for-byte plain.

### 6. Go 1.26 Source Modernization

The source modernization is a separate branch and plan. It uses the current
`modernize` diagnostics as the bounded target rather than performing an open-
ended refactor.

The mechanical categories are:

- `any`, `min`/`max`, range-over-int, and removal of redundant loop copies;
- `maps.Copy`, `slices.Contains`, `slices.ContainsFunc`, and
  `slices.Backward`;
- `reflect.TypeFor`, typed atomics, and `fmt.Appendf`;
- Go 1.26 `new(expr)` and removal of one-line pointer helpers.

Three `omitzero` findings describe currently ineffective `omitempty` options
in `pkg/catchrpc` wire types. To preserve serialized output, the implementation
removes only the ineffective option rather than changing field presence. The
existing effective `omitzero` behavior remains in place. Characterization
tests lock the existing JSON field behavior before the tag cleanup.

Once all findings are resolved, `modernize` is enabled in `.golangci.yml` so
new outdated patterns fail the normal quality gate.

### 7. Direct Dependency Compatibility Refresh

The remaining updates are landed in subsystem batches. Exact versions are
reconfirmed immediately before implementation; the approved snapshot is:

| Module | Current | Target |
| --- | --- | --- |
| `github.com/BurntSushi/toml` | pseudo-version before v1.5 | v1.6.0 |
| `github.com/Masterminds/semver/v3` | v3.3.0 | v3.5.0 |
| `github.com/containerd/containerd/v2` | v2.3.2 | v2.3.4 |
| `github.com/go-json-experiment/json` | 2025 pseudo-version | `v0.0.0-20260623181947-01eb4420fa68` |
| `github.com/hugomd/ascii-live` | 2023 pseudo-version | `v0.0.0-20250503202505-9695c975e852` |
| `github.com/klauspost/compress` | v1.18.5 | v1.19.2 |
| `github.com/miekg/dns` | v1.1.58 | v1.1.72 |
| `github.com/tailscale/depaware` | 2025 pseudo-version | `v0.0.0-20260720165112-f20f66241ec6` |
| `golang.org/x/sync` | v0.21.0 | v0.22.0 |
| `golang.org/x/sys` | v0.46.0 | v0.47.0 |
| `golang.org/x/term` | v0.44.0 | v0.45.0 |
| `tailscale.com` | v1.88.2 | v1.102.2 |
| `tailscale.com/client/tailscale/v2` | v2.5.0 | v2.10.1 |

`github.com/fatih/color` is removed by the terminal project and therefore does
not appear in the dependency-refresh project.

Release-note consequences that shape the batches:

- BurntSushi TOML v1.6 enables TOML 1.1 by default and tightens duplicate-key
  handling, so preference and project-config parsing need direct and fuzz tests.
- containerd v2.3.4 is a patch release but changes the default for experimental
  checkpoint restore. Yeet uses the client/storage APIs, not CRI restore, so
  registry tests must prove that assumption.
- klauspost/compress v1.19 adds concurrent Zstandard work and v1.19.2 includes
  decoder, dictionary, ARM64, and race fixes, so codec round trips and race
  tests are required.
- Tailscale v1.102.2 includes networking, Serve/Funnel, DNS, userspace-mode,
  memory-leak, and security fixes. The core and client/v2 modules must move
  together and receive the broadest package test matrix.
- miekg/dns v1 is now maintenance-only; this project takes the latest v1 patch
  but does not silently migrate to the separately hosted v2 major.

Primary dependency references:

- <https://github.com/BurntSushi/toml/releases/tag/v1.6.0>
- <https://github.com/Masterminds/semver/releases/tag/v3.5.0>
- <https://github.com/containerd/containerd/releases/tag/v2.3.4>
- <https://github.com/klauspost/compress/releases/tag/v1.19.2>
- <https://github.com/miekg/dns/releases/tag/v1.1.72>
- <https://tailscale.com/changelog>
- <https://github.com/tailscale/tailscale/releases/tag/v1.102.2>

## Compatibility Contracts

The following are hard invariants across all three projects:

- `--progress=plain` and `--progress=quiet` never emit ANSI.
- Redirected table/plain output never emits ANSI.
- JSON and JSON-pretty bytes and schemas do not change because of styling.
- Cursor visibility is restored on success, error, cancellation, and suspended
  progress paths.
- Catch remains authoritative for remote command parsing and PTY behavior.
- No RPC or permission mapping changes are introduced.
- Existing preferences, project TOML, database views, service records, and
  Catch journals continue to decode.
- Dependency batches do not change generated artifacts or refresh quality
  baselines merely to obtain green checks.

## Verification Strategy

Each plan defines focused RED/GREEN cycles. The final candidate for each project
also runs:

```bash
mise exec -- go test ./... -count=1
mise exec -- pre-commit run --all-files
mise run quality
mise run vuln
```

The terminal project additionally runs focused race tests for `pkg/tui`,
`pkg/yeet`, and `pkg/catch`, verifies both binaries, compares binary sizes, and
checks the selected module graph.

The source-modernization project requires zero findings from:

```bash
mise exec -- golangci-lint run --config .golangci.yml --enable-only modernize ./...
```

The dependency project runs subsystem race/fuzz tests where parsers, codecs,
DNS, or concurrency-sensitive modules changed, then ends with `go list -m -u`
to report remaining direct drift without automatically chasing it.

No live Catch installation is part of these plans. A local interactive prompt
and TTY smoke test is allowed. Replacing Catch on `yeet-pve1` or `yeet-hetz`
requires separate user authorization and the `yeet-cli` workflow.

## Documentation and Release Surface

The terminal project updates the CLI overview to state that interactive output
is styled only on terminals, while `NO_COLOR`, pipes, explicit plain progress,
and structured formats remain unstyled. No command syntax changes.

Source and dependency modernization are internal unless implementation uncovers
a user-visible compatibility change. Changelog and release preparation remain
outside this design and require explicit release authorization.

## Delivery Order

1. Go 1.26.6 and Charm v2 terminal modernization.
2. Go 1.26 source modernization and permanent `modernize` linting.
3. Direct dependency compatibility refresh.

Later projects begin from landed, green predecessors. They do not stack broad
unpublished rewrites on top of one another.

## Completion

The program is complete when all three projects have landed independently, all
normal quality gates pass, the selected module graph contains the approved
Charm v2 stack with no legacy Charm core paths, the modernize linter is clean
and enabled, remaining direct dependency drift is explicitly documented, and
plain/quiet/JSON/PTY compatibility tests remain green.
