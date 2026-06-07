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
  `.codex/skills/yeet-cli/references/yeet-help-llm.md` with
  `tools/generate-yeet-help-llm.sh`.

## Common Commands

```bash
go run ./cmd/yeet status
go run ./cmd/yeet status <svc>
go run ./cmd/yeet logs <svc>
go run ./cmd/yeet info <svc>
go run ./cmd/yeet run <svc> ./compose.yml
go run ./cmd/yeet docker outdated
go run ./cmd/yeet docker update <svc...>
```

Use `--host=<catch-host>` or `CATCH_HOST=<catch-host>` when the target host is
not the current default.

## Live Testing

- `yeet init` updates catch on the target host.
- `yeet run`, `yeet remove`, `yeet restart`, and `yeet docker update` can change
  running services.
- Use unique service names for tests and clean them up when finished.
