---
name: yeet-cli
description: Use for yeet CLI operations like deploying or updating services, restarting/removing services, installing or upgrading catch on a host, checking status/logs/info, and guidance on CATCH_HOST/--host targeting.
---

# Yeet CLI

Use this skill for operating yeet from this repository. Prefer
`mise exec -- go run ./cmd/yeet ...` unless the user explicitly asks for an
installed binary.

## Start Here

- Read `docs/agent/codebase-map.md` for command ownership.
- Read `AGENTS.local.md` before live host testing.
- For exact syntax, use
  `mise exec -- go run ./cmd/yeet <command> --help-agent` or read
  `.codex/skills/yeet-cli/references/yeet-help-agent.md`.
- If help output changes, update
  `.codex/skills/yeet-cli/references/yeet-help-agent.md` with
  `tools/generate-yeet-help-agent.sh`.

## Common Commands

```bash
mise exec -- go run ./cmd/yeet status
mise exec -- go run ./cmd/yeet status <svc>
mise exec -- go run ./cmd/yeet logs <svc>
mise exec -- go run ./cmd/yeet info <svc>
mise exec -- go run ./cmd/yeet run <svc> ./compose.yml
mise exec -- go run ./cmd/yeet run <svc> vm://ubuntu/26.04 --net=lan
mise exec -- go run ./cmd/yeet docker outdated
mise exec -- go run ./cmd/yeet docker update <svc...>
```

Use `--host=<catch-host>` or `CATCH_HOST=<catch-host>` when the target host is
not the current default.
Run service commands from the intended service workspace because `yeet.toml` is
read and written in the current directory.

## Live Testing

- `yeet init` updates catch on the target host.
- `yeet run`, `yeet remove`, `yeet restart`, and `yeet docker update` can change
  running services.
- Use unique service names for tests and clean them up when finished.
