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
- User-facing docs: use `.codex/skills/yeet-docs/SKILL.md` and
  `website/AGENTS.md`.

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

Present:

- CLI entrypoint: `cmd/yeet/AGENTS.md`
- Catch entrypoint: `cmd/catch/AGENTS.md`
- CLI parser registry: `pkg/cli/AGENTS.md`
- Client orchestration: `pkg/yeet/AGENTS.md`
- Catch server behavior: `pkg/catch/AGENTS.md`
- Service helpers: `pkg/svc/AGENTS.md`
- RPC client and wire types: `pkg/catchrpc/AGENTS.md`
- Command helpers: `pkg/cmdutil/AGENTS.md`
- Codec helpers: `pkg/codecutil/AGENTS.md`
- HTTP compression helpers: `pkg/compress/AGENTS.md`
- Copy and tar helpers: `pkg/copyutil/AGENTS.md`
- Cron formatting helpers: `pkg/cronutil/AGENTS.md`
- Catch data store: `pkg/db/AGENTS.md`
- Docker network plugin: `pkg/dnet/AGENTS.md`
- Env file writer: `pkg/env/AGENTS.md`
- File helpers: `pkg/fileutil/AGENTS.md`
- File type detection: `pkg/ftdetect/AGENTS.md`
- Network namespace helpers: `pkg/netns/AGENTS.md`
- OCI registry: `pkg/registry/AGENTS.md`
- Tar.gz reader: `pkg/targz/AGENTS.md`
- Terminal UI helpers: `pkg/tui/AGENTS.md`
- Website docs: `website/AGENTS.md`

## Repo Skills

Present:

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
