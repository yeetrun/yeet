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
- RPC CLI flow: `yeet` parses global/subcommand routing with `pkg/yargs` and forwards args via `catchrpc.Exec`; `catch` is authoritative for command/flag parsing via `pkg/cli.Parse*` (which uses `pkg/yargs`). Avoid adding per-command structured RPCs unless there is a strong need.

## Testing Guidelines
- Use Go’s `testing` package; name tests `TestXxx`.
- Prefer table-driven tests for flag parsing and CLI routing.
- Add tests for command bridging, parsing edge cases, and service behavior.
- Run targeted tests for packages you touch, plus `go test ./...` before PR.

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

## Commit & Pull Request Guidelines
- Commit messages typically follow `area: summary` (e.g., `cmd/yeet: add yargs CLI`).
- PRs should include:
  - A short summary of changes and rationale.
  - Tests run (commands + results).
  - Any user-facing behavior changes or CLI impacts.

## Release & Tagging Process
- Find the latest `vX.Y.Z` tag and bump the patch version.
- Update `website/docs/changelog.mdx` with a new date section and 1-3 user-facing bullets for the release.
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
- Use plain, user-facing language focused on behavior changes; avoid internal refactor notes.
- Keep tense consistent (past or present), keep lines concise, and avoid emojis.
- Include only releases/tags; don’t list every commit.

## Configuration & Environment
- `CATCH_HOST`: overrides the default remote host for the client.
- Keep local config in `~/.yeet/prefs.json` (managed via `yeet prefs`).
