# Agentic Repo Context Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make yeet easier for Codex agents to navigate and edit by adding layered repo instructions, a codebase map, focused skills, and maintenance guidance.

**Architecture:** Keep root `AGENTS.md` broad and lean, move subsystem invariants into local `AGENTS.md` files, and move repeatable workflows into `.codex/skills`. Add `docs/agent/codebase-map.md` as the starting index and keep hooks deterministic without adding new hook behavior.

**Tech Stack:** Markdown, Codex `AGENTS.md`, Codex repo skills under `.codex/skills`, existing pre-commit quality gates.

---

## File Structure

- Modify: `AGENTS.md`  
  Keep repo-wide policy, add pointers to the codebase map and skills, and remove duplicated subsystem details after local `AGENTS.md` files exist.
- Create: `docs/agent/codebase-map.md`  
  Repository navigation index for Codex agents.
- Create: `docs/agent/maintenance.md`  
  Periodic review checklist for agent context.
- Create: `cmd/yeet/AGENTS.md`  
  Local rules for the client CLI entrypoint and bridge behavior.
- Create: `cmd/catch/AGENTS.md`  
  Local rules for the catch command entrypoint.
- Create: `pkg/cli/AGENTS.md`  
  Parser, registry, help, and CLI test guidance.
- Create: `pkg/yeet/AGENTS.md`  
  Client orchestration, host/service resolution, project config, and remote exec guidance.
- Create: `pkg/catch/AGENTS.md`  
  Catch-side RPC, TTY, Docker/systemd, and live side-effect guidance.
- Create: `pkg/svc/AGENTS.md`  
  Service-domain helper guidance for Docker/systemd/networking behavior.
- Create: `website/AGENTS.md`  
  User manual, changelog, and submodule workflow guidance.
- Modify: `.codex/skills/yeet-cli/SKILL.md`  
  Keep the existing CLI operations skill, but link it to the new map and local docs.
- Create: `.codex/skills/yeet-release/SKILL.md`  
  Release workflow skill.
- Create: `.codex/skills/yeet-docs/SKILL.md`  
  Docs/manual/changelog workflow skill.
- Create: `.codex/skills/yeet-rpc/SKILL.md`  
  Client-to-catch RPC workflow skill.
- Create: `.codex/skills/yeet-docker/SKILL.md`  
  Docker compose/registry/outdated workflow skill.
- Create: `.codex/skills/yeet-quality/SKILL.md`  
  Quality gate and empirical testing workflow skill.
- Modify: `README.md`  
  Add a short pointer to the agent-context map under the development quality section.

## Task 1: Add Agent Navigation Docs

**Files:**
- Create: `docs/agent/codebase-map.md`
- Create: `docs/agent/maintenance.md`

- [ ] **Step 1: Create the agent docs directory**

Run:

```bash
mkdir -p docs/agent
```

Expected: command exits `0`.

- [ ] **Step 2: Create `docs/agent/codebase-map.md`**

Create `docs/agent/codebase-map.md` with this content:

```markdown
# Yeet Codebase Map

This map is a starting index for Codex agents. It is not a full architecture
manual. Use it to choose the first files to read, then inspect the code before
editing.

## Top-Level Layout

- `cmd/yeet/`: local CLI entrypoint, global flags, yargs group handlers, and
  service-argument bridging.
- `cmd/catch/`: catch daemon entrypoint. Most server behavior lives in
  `pkg/catch/`.
- `pkg/cli/`: command registries, flag structs, parser helpers, and CLI help
  metadata shared by client and catch.
- `pkg/yeet/`: client-side orchestration, project config, service and host
  resolution, local command handling, remote exec, init, copy, SSH, and status
  rendering.
- `pkg/catch/`: catch-side RPC, TTY command execution, service state, Docker
  compose operations, systemd integration, networking, registry, and install
  behavior.
- `pkg/svc/`: service-domain helpers for Docker, Docker compose, systemd,
  network namespaces, and Docker outdated digest checks.
- `pkg/catchrpc/`: JSON-RPC and WebSocket types/client shared by yeet and catch.
- `tools/`: repo quality tools, pre-commit hook implementations, private scan,
  hotspot, and quality gate logic.
- `website/`: user manual submodule. Commit and push inside this repo before
  committing the updated submodule pointer in the root repo.
- `docs/superpowers/`: design specs and implementation plans for agentic work.
- `.codex/`: repo-local Codex hooks and skills.

## Common Starting Points

- New or changed CLI syntax: start in `pkg/cli/cli.go`, then inspect
  `cmd/yeet/cli.go`, `cmd/yeet/cli_bridge.go`, and the relevant handler in
  `pkg/yeet/`.
- Service command routing: start in `pkg/yeet/svc_cmd.go`.
- Client remote execution: start in `pkg/yeet/exec_remote.go`.
- Catch RPC behavior: start in `pkg/catch/rpc.go` and
  `pkg/catch/tty_exec.go`.
- Catch-side Docker commands: start in `pkg/catch/tty_ops.go` and
  `pkg/svc/docker.go`.
- Docker outdated/update behavior: start in `pkg/svc/docker_outdated.go`,
  `pkg/yeet/docker_outdated.go`, and `pkg/catch/tty_ops.go`.
- Docker compose deploy/update behavior: start in `pkg/svc/docker.go`,
  `pkg/yeet/svc_cmd.go`, and `pkg/catch/tty_ops.go`.
- Systemd service behavior: start in `pkg/svc/systemd.go` and
  `pkg/catch/systemd.go`.
- Network namespace and port reconciliation: start in `pkg/catch/netns.go`,
  `pkg/catch/netns_reconcile.go`, and `pkg/catch/compose_ports.go`.
- Registry behavior: start in `pkg/registry/` and `pkg/catch/registry.go`.
- Release work: use `.codex/skills/yeet-release/SKILL.md` and
  `website/AGENTS.md`.
- User-facing docs: use `.codex/skills/yeet-docs/SKILL.md`.

## Targeted Test Commands

- CLI parser and metadata: `go test ./pkg/cli -count=1`
- CLI bridge and global routing: `go test ./cmd/yeet -count=1`
- Client orchestration: `go test ./pkg/yeet -count=1`
- Catch RPC and TTY commands: `go test ./pkg/catch -count=1`
- Service-domain helpers: `go test ./pkg/svc -count=1`
- Registry: `go test ./pkg/registry -count=1`
- Full Go suite: `go test ./... -count=1`
- Commit gate: `pre-commit run --all-files`
- Normal quality ratchet: `mise run quality`
- Heavy release-quality goal: `mise run quality:goal`

## Local Agent Instructions

- Root policy: `AGENTS.md`
- CLI entrypoint: `cmd/yeet/AGENTS.md`
- Catch entrypoint: `cmd/catch/AGENTS.md`
- CLI parser registry: `pkg/cli/AGENTS.md`
- Client orchestration: `pkg/yeet/AGENTS.md`
- Catch server behavior: `pkg/catch/AGENTS.md`
- Service helpers: `pkg/svc/AGENTS.md`
- Website docs: `website/AGENTS.md`

## Repo Skills

- CLI operations: `.codex/skills/yeet-cli/SKILL.md`
- Releases: `.codex/skills/yeet-release/SKILL.md`
- Docs: `.codex/skills/yeet-docs/SKILL.md`
- RPC flow: `.codex/skills/yeet-rpc/SKILL.md`
- Docker workflows: `.codex/skills/yeet-docker/SKILL.md`
- Quality gates: `.codex/skills/yeet-quality/SKILL.md`

## Navigation Rules

- Read the local `AGENTS.md` before editing inside a subsystem.
- Prefer `rg` and targeted tests before opening broad file sets.
- Do not treat this map as authoritative over code. If code and map disagree,
  follow the code and update the map in the same task when useful.
```

- [ ] **Step 3: Create `docs/agent/maintenance.md`**

Create `docs/agent/maintenance.md` with this content:

```markdown
# Agent Context Maintenance

Review the repo-local Codex setup every three to six months and after major
Codex/tooling changes.

## Checklist

- Root `AGENTS.md` is still lean and repo-wide.
- Subdirectory `AGENTS.md` files describe local invariants, not broad policy.
- Skills trigger for the tasks they describe and stay compact.
- The Stop hook catches real final-state drift without frequent false positives.
- `docs/agent/codebase-map.md` points to the current starting files.
- `.codex/skills/yeet-cli/references/yeet-help-llm.md` matches current help
  output for changed commands.
- Release, docs, and quality workflows still match `AGENTS.md`.
- New Codex capabilities have not made local guidance obsolete.

## When To Update

- Add or update local `AGENTS.md` when agents repeatedly miss subsystem-specific
  rules.
- Add or update a skill when a workflow is useful but too specific for
  always-loaded instructions.
- Update the codebase map when a common task has a better starting point.
- Tune hooks only with a replayable sample message that proves the false
  positive or false negative.

## Verification

Run these checks after agent-context changes:

```bash
python3 -m json.tool .codex/hooks.json >/dev/null
python3 -m py_compile .codex/hooks/stop_repo_state.py
rm -rf .codex/hooks/__pycache__
pre-commit run --all-files
```
```

- [ ] **Step 4: Verify the new docs have no placeholders**

Run:

```bash
rg -n "TBD|TODO|FIXME|placeholder|\\?\\?" docs/agent
```

Expected: no matches.

- [ ] **Step 5: Commit**

Run:

```bash
git add docs/agent/codebase-map.md docs/agent/maintenance.md
git commit -m "docs: add agent codebase map"
```

Expected: commit succeeds.

## Task 2: Add Local AGENTS For Entrypoints And Parsers

**Files:**
- Create: `cmd/yeet/AGENTS.md`
- Create: `cmd/catch/AGENTS.md`
- Create: `pkg/cli/AGENTS.md`

- [ ] **Step 1: Create `cmd/yeet/AGENTS.md`**

Create `cmd/yeet/AGENTS.md` with this content:

```markdown
# cmd/yeet Agent Notes

This directory contains the client CLI entrypoint and user-facing command
routing. Keep command-line behavior predictable and covered by tests.

## Local Rules

- `yeet.go` wires global flags, runtime overrides, subcommand handlers, and
  yargs groups.
- `cli.go` owns top-level help metadata for local groups and global flags.
- `cli_bridge.go` resolves service arguments such as `<svc>` and `<svc>@<host>`
  before forwarding commands to `pkg/yeet`.
- Keep parsing side effects small. Prefer shared parser definitions in
  `pkg/cli` over ad hoc local parsing.
- When user-facing syntax changes, update CLI help tests, README examples, and
  website docs in the same work session.

## Tests

- Run `go test ./cmd/yeet -count=1` after changing this directory.
- Run `go test ./pkg/cli ./cmd/yeet ./pkg/yeet -count=1` when command routing
  or bridge behavior changes.

## Related Context

- Codebase map: `docs/agent/codebase-map.md`
- CLI operations skill: `.codex/skills/yeet-cli/SKILL.md`
- Docs skill: `.codex/skills/yeet-docs/SKILL.md`
```

- [ ] **Step 2: Create `cmd/catch/AGENTS.md`**

Create `cmd/catch/AGENTS.md` with this content:

```markdown
# cmd/catch Agent Notes

This directory contains the catch daemon entrypoint. Most behavior should live
in packages, especially `pkg/catch`, so it can be tested without starting the
daemon command.

## Local Rules

- Keep command setup, process wiring, and startup concerns here.
- Put RPC, TTY, service, Docker, systemd, registry, and networking behavior in
  `pkg/catch` or lower-level packages.
- Avoid adding tests that need a real daemon when package-level tests can cover
  the behavior.

## Tests

- Run `go test ./cmd/catch -count=1` after changing this directory.
- Run targeted package tests for behavior moved into `pkg/catch`.

## Related Context

- Catch server notes: `pkg/catch/AGENTS.md`
- Codebase map: `docs/agent/codebase-map.md`
```

- [ ] **Step 3: Create `pkg/cli/AGENTS.md`**

Create `pkg/cli/AGENTS.md` with this content:

```markdown
# pkg/cli Agent Notes

This package defines shared command metadata, flag structs, parser helpers, and
help text used by both yeet and catch.

## Local Rules

- Keep CLI flags in struct tags and use kebab-case names.
- Register command names and group commands in the shared registries instead of
  scattering magic strings.
- Prefer table-driven tests for parser behavior, invalid values, repeated
  flags, positional arguments, and default values.
- Parser changes often require updates in `cmd/yeet` bridge tests and
  `pkg/yeet` command handling tests.
- Do not make `pkg/cli` depend on client or server packages.

## Tests

- Run `go test ./pkg/cli -count=1` after parser or help metadata changes.
- Run `go test ./pkg/cli ./cmd/yeet ./pkg/yeet -count=1` when parser changes
  affect service-argument bridging or client routing.

## Related Context

- Codebase map: `docs/agent/codebase-map.md`
- RPC flow skill: `.codex/skills/yeet-rpc/SKILL.md`
- Docs skill: `.codex/skills/yeet-docs/SKILL.md`
```

- [ ] **Step 4: Verify local instruction files**

Run:

```bash
rg -n "TBD|TODO|FIXME|placeholder|\\?\\?" cmd/yeet/AGENTS.md cmd/catch/AGENTS.md pkg/cli/AGENTS.md
```

Expected: no matches.

- [ ] **Step 5: Commit**

Run:

```bash
git add cmd/yeet/AGENTS.md cmd/catch/AGENTS.md pkg/cli/AGENTS.md
git commit -m "docs: add cli agent notes"
```

Expected: commit succeeds.

## Task 3: Add Local AGENTS For Client, Server, Services, And Website

**Files:**
- Create: `pkg/yeet/AGENTS.md`
- Create: `pkg/catch/AGENTS.md`
- Create: `pkg/svc/AGENTS.md`
- Create: `website/AGENTS.md`

- [ ] **Step 1: Create `pkg/yeet/AGENTS.md`**

Create `pkg/yeet/AGENTS.md` with this content:

```markdown
# pkg/yeet Agent Notes

This package contains client-side orchestration for the `yeet` CLI: service and
host resolution, project config, local handling, remote exec, init, copy, SSH,
and status rendering.

## Local Rules

- Preserve the RPC CLI flow: `cmd/yeet` resolves global routing, `pkg/yeet`
  decides local vs remote handling, and `catch` remains authoritative for remote
  command parsing.
- Prefer forwarding existing command shapes through `catchrpc.Exec`; add
  structured RPCs only when the command cannot reasonably be represented as
  remote CLI execution.
- Be careful with global host/service state in tests. Preserve and restore
  package globals around tests that mutate preferences or overrides.
- User-facing behavior changes usually require README and website docs updates.
- Live host commands can affect real services. Use `AGENTS.local.md` and the
  `yeet-cli` skill before running them.

## Tests

- Run `go test ./pkg/yeet -count=1` after client orchestration changes.
- Run `go test ./pkg/cli ./cmd/yeet ./pkg/yeet -count=1` after command routing
  changes.
- Run `go test ./... -count=1` before broad merges or releases.

## Related Context

- Codebase map: `docs/agent/codebase-map.md`
- CLI operations skill: `.codex/skills/yeet-cli/SKILL.md`
- RPC flow skill: `.codex/skills/yeet-rpc/SKILL.md`
```

- [ ] **Step 2: Create `pkg/catch/AGENTS.md`**

Create `pkg/catch/AGENTS.md` with this content:

```markdown
# pkg/catch Agent Notes

This package contains catch server behavior: RPC, TTY command execution,
service state, Docker compose operations, registry integration, systemd,
networking, and install helpers.

## Local Rules

- Catch is authoritative for remote command parsing and execution.
- Keep TTY command behavior testable through package-level helpers and stubs.
- Treat Docker, systemd, network namespace, and registry operations as
  side-effectful. Unit-test command construction and state transitions before
  live testing.
- Prefer focused tests near the touched behavior. Avoid daemon-level tests when
  package tests can cover the same path.
- Use `AGENTS.local.md` for live catch testing guidance and target hosts.

## Tests

- Run `go test ./pkg/catch -count=1` after catch-side changes.
- Run `go test ./pkg/catch ./pkg/svc -count=1` after Docker/systemd service
  behavior changes.
- Run live E2E only when behavior depends on real Docker, systemd, networking,
  or RPC streaming.

## Related Context

- Codebase map: `docs/agent/codebase-map.md`
- Docker workflow skill: `.codex/skills/yeet-docker/SKILL.md`
- RPC flow skill: `.codex/skills/yeet-rpc/SKILL.md`
```

- [ ] **Step 3: Create `pkg/svc/AGENTS.md`**

Create `pkg/svc/AGENTS.md` with this content:

```markdown
# pkg/svc Agent Notes

This package contains service-domain helpers for Docker, Docker compose,
systemd, network namespaces, and Docker image update checks.

## Local Rules

- Keep helpers deterministic and unit-testable. Stub command execution where
  possible instead of requiring Docker or systemd in unit tests.
- Docker compose behavior should preserve user-facing output and avoid
  unnecessary restarts.
- Docker outdated logic compares running container state with the compose
  declared upstream image reference. Table output can be compact, but JSON
  should preserve exact digest information.
- Systemd and network helpers must treat race detector findings as real bugs
  unless proven otherwise.

## Tests

- Run `go test ./pkg/svc -count=1` after service helper changes.
- Run `go test ./pkg/svc ./pkg/catch -count=1` when catch command behavior uses
  changed helpers.
- Run race/fuzz/quality gates when touching parser, path, concurrency, or
  network-input surfaces.

## Related Context

- Codebase map: `docs/agent/codebase-map.md`
- Docker workflow skill: `.codex/skills/yeet-docker/SKILL.md`
- Quality skill: `.codex/skills/yeet-quality/SKILL.md`
```

- [ ] **Step 4: Create `website/AGENTS.md` inside the submodule**

Create `website/AGENTS.md` with this content:

```markdown
# Website Agent Notes

This submodule contains the yeet user manual and changelog. Changes here must
be committed and pushed inside the website repository before the root repository
commits the updated submodule pointer.

## Local Rules

- Update docs when user-facing CLI commands, flags, workflows, or behavior
  change.
- Keep changelog entries date-first, release-version second, and limited to
  1-3 user-facing bullets.
- Use plain user-facing language. Avoid internal refactor details in release
  notes.
- Keep examples generic. Do not publish private hostnames, service names, local
  paths, or infrastructure details.

## Tests

- Run `git -C website diff --check` after docs edits.
- Run the website's local checks when changing build or site behavior.
- Before a root release commit, confirm `git -C website status --short --branch`
  is clean and pushed.

## Related Context

- Docs skill: `../.codex/skills/yeet-docs/SKILL.md`
- Release skill: `../.codex/skills/yeet-release/SKILL.md`
- Root release policy: `../AGENTS.md`
```

- [ ] **Step 5: Verify local instruction files**

Run:

```bash
rg -n "TBD|TODO|FIXME|placeholder|\\?\\?" pkg/yeet/AGENTS.md pkg/catch/AGENTS.md pkg/svc/AGENTS.md website/AGENTS.md
git -C website diff --check
```

Expected: no matches from `rg`; `git -C website diff --check` exits `0`.

- [ ] **Step 6: Commit website AGENTS inside the submodule**

Run:

```bash
git -C website add AGENTS.md
git -C website commit -m "docs: add website agent notes"
git -C website push origin main
```

Expected: website commit and push succeed.

- [ ] **Step 7: Commit root AGENTS files and submodule pointer**

Run:

```bash
git add pkg/yeet/AGENTS.md pkg/catch/AGENTS.md pkg/svc/AGENTS.md website
git commit -m "docs: add service agent notes"
```

Expected: root commit succeeds.

## Task 4: Add Release, Docs, And RPC Skills

**Files:**
- Modify: `.codex/skills/yeet-cli/SKILL.md`
- Create: `.codex/skills/yeet-release/SKILL.md`
- Create: `.codex/skills/yeet-docs/SKILL.md`
- Create: `.codex/skills/yeet-rpc/SKILL.md`

- [ ] **Step 1: Update `.codex/skills/yeet-cli/SKILL.md`**

Replace `.codex/skills/yeet-cli/SKILL.md` with this content:

```markdown
---
name: yeet-cli
description: Use for yeet CLI operations like deploying or updating services, restarting/removing services, installing or upgrading catch on a host, checking status/logs/info, and guidance on CATCH_HOST/--host targeting.
---

# Yeet CLI

Use this skill for operating yeet from this repository. Prefer `go run
./cmd/yeet ...` unless the user explicitly asks for an installed binary.

## Start Here

- Read `docs/agent/codebase-map.md` for command ownership.
- Read `AGENTS.local.md` before live host testing.
- For exact syntax, use `go run ./cmd/yeet <command> --help-llm`.
- If help output changes, update
  `.codex/skills/yeet-cli/references/yeet-help-llm.md`.

## Common Commands

```bash
go run ./cmd/yeet status
go run ./cmd/yeet status <svc>
go run ./cmd/yeet logs <svc>
go run ./cmd/yeet info <svc>
go run ./cmd/yeet run <svc> ./compose.yml
go run ./cmd/yeet docker outdated
go run ./cmd/yeet docker update <svc>
```

Use `--host=<catch-host>` or `CATCH_HOST=<catch-host>` when the target host is
not the current default.

## Live Testing

- `yeet init` updates catch on the target host.
- `yeet run`, `yeet remove`, `yeet restart`, and `yeet docker update` can change
  running services.
- Use unique service names for tests and clean them up when finished.
```

- [ ] **Step 2: Create `.codex/skills/yeet-release/SKILL.md`**

Create `.codex/skills/yeet-release/SKILL.md` with this content:

```markdown
---
name: yeet-release
description: Use when preparing, validating, tagging, or pushing a yeet patch release.
---

# Yeet Release

Use this skill for release work. Follow root `AGENTS.md` exactly.

## Patch Release Flow

1. Find the latest `vX.Y.Z` tag and choose the next patch version.
2. Update `website/docs/changelog.mdx` with a date section, version heading, and
   1-3 user-facing bullets.
3. Commit and push the website changes inside `website/`.
4. Commit the updated `website` submodule pointer in the root repo.
5. Create an annotated tag with message equal to the version:

```bash
git tag -a vX.Y.Z -m "vX.Y.Z"
```

6. Push the root branch, then push the tag:

```bash
git push origin main
git push origin vX.Y.Z
```

## Verification

```bash
git status --short --branch
git -C website status --short --branch
git tag --list 'v*' --sort=-version:refname | sed -n '1,5p'
git ls-remote --tags origin vX.Y.Z
```

Do not commit, tag, or push without explicit user authorization.
```

- [ ] **Step 3: Create `.codex/skills/yeet-docs/SKILL.md`**

Create `.codex/skills/yeet-docs/SKILL.md` with this content:

```markdown
---
name: yeet-docs
description: Use when user-facing CLI behavior, flags, workflows, README examples, website manual pages, or changelog entries need to be updated.
---

# Yeet Docs

Use this skill when a change affects what users run or read.

## Surfaces

- Root `README.md` for quickstart and common examples.
- `website/docs/` for the user manual.
- `website/docs/changelog.mdx` only for tagged releases.
- CLI help metadata in `pkg/cli/cli.go` and `cmd/yeet/cli.go`.
- Help reference:
  `.codex/skills/yeet-cli/references/yeet-help-llm.md`.

## Rules

- Keep examples generic and free of private hostnames or service names.
- Keep changelog bullets user-facing and concise.
- For CLI syntax changes, update parser tests, help text, README, and website
  docs together.
- Commit website changes inside the submodule before committing the root
  submodule pointer.

## Checks

```bash
git -C website diff --check
rg -n "private-host|/Users/" README.md website/docs .codex/skills
```
```

- [ ] **Step 4: Create `.codex/skills/yeet-rpc/SKILL.md`**

Create `.codex/skills/yeet-rpc/SKILL.md` with this content:

```markdown
---
name: yeet-rpc
description: Use when changing yeet client command routing, catch remote execution, catchrpc types, or the boundary between local CLI parsing and catch-side parsing.
---

# Yeet RPC Flow

Use this skill for command-routing and remote-exec work.

## Architecture

- `cmd/yeet` parses global flags and group routing.
- `cmd/yeet/cli_bridge.go` resolves service arguments and `<svc>@<host>`.
- `pkg/yeet` decides whether a command is local or forwarded remotely.
- `catchrpc.Exec` carries command args to catch.
- `pkg/catch` is authoritative for parsing and executing remote commands.

## Rules

- Avoid adding structured RPCs unless command-shaped forwarding cannot support
  the behavior.
- Keep shared parser metadata in `pkg/cli`.
- Test both parser behavior and bridge/routing behavior when command syntax
  changes.

## Tests

```bash
go test ./pkg/cli ./cmd/yeet ./pkg/yeet -count=1
go test ./pkg/catch ./pkg/catchrpc -count=1
```
```

- [ ] **Step 5: Verify skill frontmatter**

Run:

```bash
python3 - <<'PY'
from pathlib import Path
for path in sorted(Path('.codex/skills').glob('yeet-*/SKILL.md')):
    data = path.read_text()
    assert data.startswith('---\n'), path
    head = data.split('---', 2)[1]
    assert 'name:' in head, path
    assert 'description:' in head, path
    print(path)
PY
```

Expected: prints each `SKILL.md` path and exits `0`.

- [ ] **Step 6: Commit**

Run:

```bash
git add .codex/skills/yeet-cli/SKILL.md .codex/skills/yeet-release/SKILL.md .codex/skills/yeet-docs/SKILL.md .codex/skills/yeet-rpc/SKILL.md
git commit -m "codex: add release docs rpc skills"
```

Expected: commit succeeds.

## Task 5: Add Docker And Quality Skills

**Files:**
- Create: `.codex/skills/yeet-docker/SKILL.md`
- Create: `.codex/skills/yeet-quality/SKILL.md`

- [ ] **Step 1: Create `.codex/skills/yeet-docker/SKILL.md`**

Create `.codex/skills/yeet-docker/SKILL.md` with this content:

```markdown
---
name: yeet-docker
description: Use when changing Docker compose deployment, docker pull/update/outdated behavior, internal registry image handling, Docker networking, or digest comparison logic.
---

# Yeet Docker

Use this skill for Docker compose and registry behavior.

## Starting Points

- Client orchestration: `pkg/yeet/docker_outdated.go`, `pkg/yeet/svc_cmd.go`
- Catch TTY commands: `pkg/catch/tty_ops.go`
- Service helpers: `pkg/svc/docker.go`, `pkg/svc/docker_outdated.go`
- Registry: `pkg/registry/`, `pkg/catch/registry.go`

## Rules

- `yeet run` for compose does not pull images by default.
- `yeet docker update <svc>` pulls and recreates one compose service.
- `yeet docker outdated` is read-only and should preserve exact digests in JSON.
- Compact table output should avoid raw digest noise.
- Internal yeet registry images and upstream registry images have different
  semantics; inspect existing tests before changing comparison logic.
- Live Docker update commands affect running containers.

## Tests

```bash
go test ./pkg/svc ./pkg/catch ./pkg/yeet -run 'Test.*Docker' -count=1
go test ./pkg/svc ./pkg/catch ./pkg/yeet -count=1
```
```

- [ ] **Step 2: Create `.codex/skills/yeet-quality/SKILL.md`**

Create `.codex/skills/yeet-quality/SKILL.md` with this content:

```markdown
---
name: yeet-quality
description: Use when choosing verification commands, working on tests, quality tooling, race/fuzz/mutation checks, pre-commit failures, or release-quality gates.
---

# Yeet Quality

Use this skill to choose verification depth.

## Normal Flow

- Run targeted package tests while developing.
- Run `go test ./... -count=1` before broad completion.
- Run `pre-commit run --all-files` before commits that change code, tooling,
  docs examples, release surfaces, or agent context.

## Heavy Checks

```bash
mise run quality
mise run race
mise run fuzz
mise run mutation
mise run quality:goal
```

Use `mise run quality:goal` before releases and after meaningful
quality-tooling, parser, RPC, concurrency, or service-orchestration changes.

## Rules

- Do not set or manage `GOCACHE`.
- Do not refresh baselines just to get green.
- Treat race detector findings as bugs unless proven otherwise.
- Commit minimized fuzz corpus files for fuzz-discovered bugs.
```

- [ ] **Step 3: Verify skill frontmatter**

Run:

```bash
python3 - <<'PY'
from pathlib import Path
for path in sorted(Path('.codex/skills').glob('yeet-*/SKILL.md')):
    data = path.read_text()
    assert data.startswith('---\n'), path
    head = data.split('---', 2)[1]
    assert 'name:' in head, path
    assert 'description:' in head, path
    print(path)
PY
```

Expected: prints each `SKILL.md` path and exits `0`.

- [ ] **Step 4: Commit**

Run:

```bash
git add .codex/skills/yeet-docker/SKILL.md .codex/skills/yeet-quality/SKILL.md
git commit -m "codex: add docker quality skills"
```

Expected: commit succeeds.

## Task 6: Refresh Root Docs

**Files:**
- Modify: `AGENTS.md`
- Modify: `README.md`

- [ ] **Step 1: Update root `AGENTS.md`**

Edit root `AGENTS.md` to add this short section after "Project Structure &
Module Organization":

```markdown
## Agent Navigation

- Start with `docs/agent/codebase-map.md` when choosing where to read or edit.
- Subdirectories may contain their own `AGENTS.md`; read and follow the local
  file before editing there.
- Use `.codex/skills` for task workflows such as releases, docs, RPC, Docker,
  and quality gates.
- Keep this root file focused on repo-wide policy. Put subsystem-specific rules
  in the subsystem `AGENTS.md`.
```

If any new local `AGENTS.md` duplicates a root bullet verbatim, remove or
shorten the duplicate in the local file. Do not remove release, quality, privacy,
or website submodule policy from root `AGENTS.md`.

- [ ] **Step 2: Update `README.md`**

Under the existing Codex hooks paragraph in `README.md`, add:

```markdown
Agent navigation docs live in `docs/agent/`. Start with
`docs/agent/codebase-map.md` when orienting a Codex session to the repository.
Task-specific workflows live in `.codex/skills/`.
```

- [ ] **Step 3: Verify docs references**

Run:

```bash
rg -n "docs/agent/codebase-map.md|.codex/skills|subdirectory.*AGENTS" AGENTS.md README.md
rg -n "TBD|TODO|FIXME|placeholder|\\?\\?" AGENTS.md README.md docs/agent .codex/skills
```

Expected: first command shows the new references; second command has no matches.

- [ ] **Step 4: Commit**

Run:

```bash
git add AGENTS.md README.md
git commit -m "docs: point agents to repo context"
```

Expected: commit succeeds.

## Task 7: Final Verification

**Files:**
- Verify: `AGENTS.md`
- Verify: `cmd/yeet/AGENTS.md`
- Verify: `cmd/catch/AGENTS.md`
- Verify: `pkg/cli/AGENTS.md`
- Verify: `pkg/yeet/AGENTS.md`
- Verify: `pkg/catch/AGENTS.md`
- Verify: `pkg/svc/AGENTS.md`
- Verify: `website/AGENTS.md`
- Verify: `.codex/skills/*/SKILL.md`
- Verify: `docs/agent/*.md`
- Verify: `.codex/hooks.json`
- Verify: `.codex/hooks/stop_repo_state.py`

- [ ] **Step 1: Validate hook JSON and Python**

Run:

```bash
python3 -m json.tool .codex/hooks.json >/dev/null
python3 -m py_compile .codex/hooks/stop_repo_state.py
rm -rf .codex/hooks/__pycache__
```

Expected: commands exit `0`; no `__pycache__` remains.

- [ ] **Step 2: Validate skill frontmatter**

Run:

```bash
python3 - <<'PY'
from pathlib import Path
for path in sorted(Path('.codex/skills').glob('yeet-*/SKILL.md')):
    data = path.read_text()
    assert data.startswith('---\n'), path
    head = data.split('---', 2)[1]
    assert 'name:' in head, path
    assert 'description:' in head, path
    print(path)
PY
```

Expected: prints all yeet skill files and exits `0`.

- [ ] **Step 3: Smoke-test the Stop hook**

Run:

```bash
printf '%s' '{"cwd":"'"$PWD"'","hook_event_name":"Stop","last_assistant_message":"Changes are staged; no commit made."}' | /usr/bin/python3 .codex/hooks/stop_repo_state.py
printf '%s' '{"cwd":"'"$PWD"'","hook_event_name":"Stop","last_assistant_message":"Everything is committed and pushed. Tagged v99.0.0."}' | /usr/bin/python3 .codex/hooks/stop_repo_state.py
```

Expected: first command prints nothing; second command prints JSON containing
`"decision": "block"`.

- [ ] **Step 4: Run full pre-commit**

Run:

```bash
pre-commit run --all-files
```

Expected: all hooks pass.

- [ ] **Step 5: Check git state**

Run:

```bash
git status --short --branch
git -C website status --short --branch
```

Expected: root is ahead only by the planned local commits or clean after push;
website is clean at the committed pointer or clean on its branch before the root
submodule pointer commit.

- [ ] **Step 6: Commit any final verification-only adjustments**

If verification required edits, commit them:

```bash
git add AGENTS.md README.md docs/agent .codex/skills .codex/hooks.json .codex/hooks/stop_repo_state.py
git commit -m "docs: finalize agent context"
```

Expected: commit succeeds only if there are final edits. If there are no edits,
skip this step.
