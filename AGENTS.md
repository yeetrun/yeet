# Repository Guidelines

If `AGENTS.local.md` exists, read it and merge its instructions with this file.

## Project Structure & Module Organization
- `cmd/yeet`: Client CLI entrypoint and user-facing commands.
- `cmd/catch`: Server/daemon entrypoint run on remote hosts.
- `pkg/`: Core libraries shared by client/server (CLI parsing, services, installer, etc.).
- `pkg/catchrpc`: JSON-RPC + WebSocket types/client shared by yeet/catch.
- `example/`: Sample services and artifacts for demos.
- `bin/`: Built binaries (local outputs).
- `tools/`, `tempfork/`: Supporting tooling and forked dependencies.
- Tests live alongside code as `*_test.go` files (primarily under `pkg/` and `cmd/`).

## Agent Navigation

- Start with `docs/agent/codebase-map.md` when choosing where to read or edit.
- Subdirectories may contain their own `AGENTS.md`; read and follow the local
  file before editing there.
- Use `.codex/skills` for task workflows such as releases, docs, RPC, Docker,
  and quality gates.
- Keep this root file focused on repo-wide policy. Put subsystem-specific rules
  in the subsystem `AGENTS.md`.

## Version Control

- Use GitButler (`but`) for normal agent version-control write operations,
  including branching, committing, branch pushes, and history edits.
- Assume multiple agents may be working in this repository. Do not move, amend,
  squash, discard, commit, push, or otherwise modify another agent's work unless
  the user asks.
- Use a dedicated GitButler branch for each agent session, unless the user asks
  for a different branch structure. Commit only changes that belong to that
  session.
- Do not push or open pull requests unless the user asks. Pull requests are not
  the default workflow.
- When the user asks to finish or integrate a session, the default outcome is
  that the session's work lands on both local `main` and `origin/main` without a
  pull request, unless the user asks for a different integration path.
- This repo normally targets `origin/main` in GitButler. Do not use `but merge`
  as the default finish command here: it is for `gb-local` targets and creates a
  merge commit, which is not the desired no-PR squash-to-main workflow.
- For a finish-to-main request, first use `but` to make the session branch a
  single commit when needed, then verify the commit is based on current
  `origin/main` and contains only this session's work. The final direct update
  of local `main` and `origin/main` is the only allowed raw `git` write
  exception, and it still requires explicit user authorization.
- Keep commit messages and any explicitly requested pull request descriptions
  succinct: explain what changed, why it changed, and any important decision.

## Build, Test, and Development Commands
- `go build ./cmd/yeet` — build the client CLI.
- `go build ./cmd/catch` — build the server binary.
- `go test ./...` — run the full test suite.
- `go test ./pkg/svc` — run service-layer tests only.
- `gofmt -w path/to/file.go` — format Go source files.
- `make helloworld` / `make hellotimer` — build example binaries.
- `make all` — installs `yeet` and runs `yeet init` (use with care).
- Note: don’t set or manage `GOCACHE` here; just run tests normally and ignore cache artifacts.

## Coding Style & Naming Conventions
- Go code is formatted with `gofmt` and follows standard Go conventions.
- Package names are lowercase; exported identifiers use `PascalCase`.
- CLI flags use kebab-case in tags (e.g., `flag:"ts-auth-key"`).
- Keep functions small and explicit; avoid hidden side effects in CLI parsing.
- Avoid magic strings; use constants or shared registries for command names/keywords.

## Testing Guidelines
- Use Go’s `testing` package; name tests `TestXxx`.
- Prefer table-driven tests for flag parsing and CLI routing.
- Add tests for command bridging, parsing edge cases, and service behavior.
- Run targeted tests for packages you touch, plus `go test ./...` before
  integration or release.

## Quality Standard
- Treat `main` as release-grade at all times: no known broken tests, red checks, reachable vulnerabilities, private-info leaks, or unreviewed quality regressions.
- Pre-commit is the deterministic local gate. Run `pre-commit run --all-files` before commits that change code, tooling, docs examples, or release surfaces.
- `mise run quality` must stay clean: private-info scan, coverage, CRAP, golangci, depaware, and hotspot reporting are the normal ratchet.
- `mise run quality:goal` is the heavy destination gate. Use it before releases and after meaningful quality-tooling, parser, RPC, concurrency, or service-orchestration changes.
- Current destination goals: at least 80% total coverage, zero CRAP hotspots, zero golangci findings, race detector clean, at least four active fuzz targets, and at least 80% mutation score on the bounded mutation target set.
- Do not lower goals, refresh baselines, or mark findings acceptable just to get green. Burn down the issue, add focused tests, or document a technical reason in the relevant review/commit context.
- Fuzz every parser, normalizer, RPC codec, config reader, path handler, and network-input surface when touched. Commit minimized fuzz corpus files for bugs found by fuzzing.
- Race detector findings are bugs until proven otherwise. Fix test harness races too; they hide real concurrency failures.
- Use hotspot ranking to choose quality work: high churn plus low coverage or complexity risk should move to the front of the burn-down queue.
- Keep public repo content free of private infrastructure details, local machine paths, usernames, hostnames, and private service names unless the user explicitly approves publishing them.

## Commit Guidelines
- Commit messages typically follow `area: summary` (e.g., `cmd/yeet: add yargs CLI`).
- Commit only the changes that belong to the current session branch.
- Summaries for integration, release, or an explicitly requested pull request
  should include the tests run and any user-facing behavior or CLI impacts.

## Release & Tagging Process
- Find the latest `vX.Y.Z` tag and bump the patch version.
- Update `website/docs/changelog.mdx` with a new date section and 1-3 user-facing bullets for the release.
- Scope release notes to the commits between the previous published release tag
  and the tag being prepared. Do not summarize the entire minor series unless
  the user explicitly asks for a roll-up release note.
- Before writing release notes, inspect the actual commit range (for example,
  `git log <previous-tag>..HEAD`) and translate only user-visible behavior,
  compatibility, migration, reliability, or operational changes from that range.
- Commit and push the changelog update inside `website/`, then commit the updated submodule pointer in this repo.
- Create an annotated tag with message equal to the version only (example: `git tag -a v0.1.2 -m "v0.1.2"`).
- Push commits and the new tag (`git push` then `git push origin v0.1.2`).
- Require explicit user authorization before any commit, tag, or push operations.

## Website Docs (User Manual)
- The user manual lives in the `website/` submodule.
- When you make user-facing changes (CLI commands, flags, workflows, behavior), update the website docs in the same work session.
- Keep CLI-facing documentation (README quickstart/examples and CLI help text) in sync with those changes.
- To get the latest website content: `git submodule update --init --recursive`.
- Edit markdown files inside `website/`, commit and push **within that repo**, then commit the updated submodule pointer in this repo.

## Website Changelog Styleguide
- Date-first sections, then version headings, then 1-3 short bullets per release.
- Write for public yeet users and operators, not maintainers. Focus on what
  changed for someone installing, upgrading, deploying, managing, or debugging
  services.
- Use plain, user-facing language focused on behavior changes, new capabilities,
  reliability fixes, compatibility notes, and required user action. Avoid
  internal refactors, tests, implementation details, commit chronology, and
  developer-only wording.
- Keep tense consistent (past or present), keep lines concise, and avoid emojis.
- Include only releases/tags; don’t list every commit.

## Configuration & Environment
- `CATCH_HOST`: overrides the default remote host for the client.
- Keep local config in `~/.yeet/prefs.json` (managed via `yeet prefs`).
