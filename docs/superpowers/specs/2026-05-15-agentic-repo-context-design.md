# Agentic Repo Context Design

## Background

The Claude Code article on large-codebase best practices argues that agentic
search works best when the repository gives the agent good starting context:
lean root instructions, layered local instructions, task-specific skills,
deterministic hooks, and lightweight maps for navigation. This repo already has
some of those pieces: root `AGENTS.md`, a repo-local Codex Stop hook, and a
`yeet-cli` skill.

Codex uses different filenames and extension points than Claude Code, so this
design translates the article's guidance into portable repository files that
every developer gets when they clone this repo.

Source: https://claude.com/blog/how-claude-code-works-in-large-codebases-best-practices-and-where-to-start

## Goals

- Help Codex find the right subsystem faster without rebuilding context from
  scratch every session.
- Improve editing correctness by putting local invariants near the code they
  govern.
- Keep always-loaded context lean enough that it does not become noise.
- Make the setup portable through committed repository files only.
- Preserve deterministic quality gates through existing pre-commit tooling.

## Non-Goals

- Do not add user-level or machine-local Codex config.
- Do not add MCP servers, plugins, LSP setup, or embedding/index generation.
- Do not make hooks auto-edit instructions or docs.
- Do not duplicate pre-commit checks in Codex hooks.
- Do not refactor application code as part of the agent-context rollout.

## Context Architecture

Use a balanced layered-context approach:

- Root `AGENTS.md` remains the repo-wide contract: project structure, quality
  gates, release process, docs policy, privacy rules, and broad CLI/RPC
  architecture.
- Subdirectory `AGENTS.md` files carry local invariants that should load only
  when Codex works in that area.
- `.codex/skills` holds task workflows that should load on demand.
- `docs/agent/codebase-map.md` gives Codex a stable table of contents for the
  repository.
- Hooks remain deterministic state checks rather than advisory reminders.

Root `AGENTS.md` should be trimmed if new local files make sections redundant.
The point is layering, not copying the same guidance into several places.

## Subdirectory Instructions

Add focused `AGENTS.md` files in these locations:

- `cmd/yeet/AGENTS.md`: client entrypoint, yargs group routing, service-argument
  bridging, and user-facing help expectations.
- `cmd/catch/AGENTS.md`: daemon entrypoint expectations and the rule that most
  server behavior belongs in `pkg/catch`.
- `pkg/cli/AGENTS.md`: parser and registry patterns, kebab-case flags,
  table-driven parser tests, and CLI help synchronization.
- `pkg/yeet/AGENTS.md`: client orchestration, host/service resolution, project
  config, remote exec boundaries, and when commands should stay local.
- `pkg/catch/AGENTS.md`: catch as the authoritative remote parser/executor,
  TTY behavior, RPC command handling, Docker/systemd side effects, and live-test
  cautions.
- `pkg/svc/AGENTS.md`: service-domain helpers for Docker, systemd, networking,
  image digest/outdated semantics, and focused package tests.
- `website/AGENTS.md`: user manual, changelog style, and the submodule commit
  flow.

Each file should be short: local purpose, local gotchas, common tests, and links
to the relevant skills or codebase-map sections. Avoid broad policy that already
belongs in root `AGENTS.md`.

## Skills And Workflows

Keep `AGENTS.md` files for invariants. Use `.codex/skills` for repeatable
workflows:

- `yeet-cli`: existing CLI operations skill. It should stay focused on `go run
  ./cmd/yeet ...`, host targeting, live catch testing, and help output refreshes.
- `yeet-release`: patch release checklist, website submodule commit/push order,
  annotated tags, and remote verification.
- `yeet-docs`: user-facing CLI/docs sync, website manual updates, README
  examples, and changelog style.
- `yeet-rpc`: client-to-catch command flow, `pkg/yargs`, `catchrpc.Exec`, and
  when not to add structured RPCs.
- `yeet-docker`: compose lifecycle, Docker outdated/update behavior, internal
  registry behavior, and image digest semantics.
- `yeet-quality`: targeted tests, full pre-commit, `mise run quality`, and when
  to run race/fuzz/mutation/goal checks.

Skills should use strong trigger descriptions and compact workflows. They can
point to `docs/agent/codebase-map.md` and local `AGENTS.md` files instead of
duplicating package-level rules.

## Codebase Map

Add `docs/agent/codebase-map.md` as a navigation aid. It should include:

- One-line descriptions of top-level directories.
- Subsystem boundaries for CLI parsing, client orchestration, catch RPC,
  catch-side TTY commands, service-domain helpers, quality tooling, and website
  docs.
- "If changing X, start in Y" entry points for common tasks.
- Targeted test commands for each subsystem.
- Pointers to local `AGENTS.md` and relevant skills.

The map is not an exhaustive architecture document. It is a starting index that
helps Codex choose the first files to read.

## Hooks

Keep the existing repo-local Stop hook as a deterministic final-state guard. It
checks final answers that claim clean, committed, pushed, tagged, or released
state against git, the website submodule, changelog, local tags, and remote
tags.

Do not add new hook behavior in the first implementation pass. A future
follow-up may add a Stop hook mode that suggests agent-doc updates after
repeated confusion, but it must not auto-edit files.

Hooks should not inject broad advice that Codex already gets from `AGENTS.md`.

## Data Flow

For a normal coding task:

1. Codex loads root `AGENTS.md`.
2. When it reads or edits a subsystem, Codex also sees that subdirectory's
   `AGENTS.md`.
3. If the user request matches a skill trigger, Codex loads the relevant skill.
4. The codebase map helps Codex decide where to search first.
5. Existing tests and pre-commit remain the source of truth for verification.
6. The Stop hook checks final-state claims before the turn ends.

For a release task:

1. Root `AGENTS.md` gives the release policy.
2. `yeet-release` gives the workflow.
3. `website/AGENTS.md` gives submodule-specific docs rules.
4. The Stop hook verifies clean/pushed/tagged/released claims.

## Error Handling And Drift

The agent-context system should fail softly:

- If a skill is stale, update that skill rather than adding broad root
  instructions.
- If a local `AGENTS.md` conflicts with root policy, root policy wins and the
  local file should be fixed.
- If the Stop hook produces a false positive, tune the hook with a replayable
  sample final message.
- If Codex repeatedly searches the wrong subsystem, update the codebase map or
  the relevant skill trigger.

## Maintenance

Add `docs/agent/maintenance.md` with a periodic review checklist:

- Are root and subdirectory `AGENTS.md` files still lean?
- Are skills triggering for the tasks they describe?
- Are hooks producing false positives or missing real final-state drift?
- Did new Codex capabilities make any guidance obsolete?
- Are codebase-map entries and help references stale?

Review this setup every three to six months, and after major Codex/tooling
changes.

## Testing

The first implementation pass should verify:

- JSON/TOML/YAML-frontmatter syntax where applicable.
- Markdown files have no obvious placeholders or broken local references.
- The Stop hook still passes honest staged/uncommitted final answers.
- The Stop hook still blocks false pushed/tagged release claims.
- `pre-commit run --all-files` passes.

## Rollout

1. Commit this design spec.
2. Implement the first pass:
   - codebase map
   - subdirectory `AGENTS.md`
   - skill split/expansion
   - maintenance document
   - no new hook behavior unless a critical gap is discovered
3. Use the setup on several real repo tasks.
4. Tune based on observed friction instead of speculative completeness.

## Acceptance Criteria

- A new Codex session can identify likely starting files for common yeet tasks
  without first scanning the whole repository.
- Local subsystem rules are documented close to the code they govern.
- Root `AGENTS.md` remains short enough to be useful in every session.
- Task-specific guidance lives in skills and is not loaded unless relevant.
- Release/docs/quality workflows are easier to follow and harder to overstate in
  final answers.
