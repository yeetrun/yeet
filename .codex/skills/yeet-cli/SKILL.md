---
name: yeet-cli
description: Use for yeet CLI operations like deploying or updating services, restarting/removing services, installing or upgrading catch on a host, and for any request asking how to use yeet commands/options/groups (run, restart, remove, init, docker/env groups, status/logs/info). Also use when the user asks for yeet CLI help output or guidance on CATCH_HOST/--host targeting.
---

# Yeet CLI

## Overview

Use this skill to translate user requests into `yeet` CLI commands and to fetch or reference `--help-llm` output for exact syntax. Prefer `go run ./cmd/yeet ...` in this repo unless the user explicitly wants the installed `yeet` binary.

## Quick Start

- Read command syntax and examples in `references/yeet-help-llm.md`.
- If any command looks stale, re-run `go run ./cmd/yeet <command> --help-llm` and update the reference file.

## Core Tasks

### Deploy or update a service

Use `yeet run` with a service name and payload. Common payloads include a binary, compose file, image, or Dockerfile.

Examples:

```
go run ./cmd/yeet run <svc> ./bin/<svc> -- --app-flag value
```

```
go run ./cmd/yeet run <svc> ./compose.yml --net=svc,ts --ts-tags=tag:app
```

```
go run ./cmd/yeet run <svc> ghcr.io/org/app:latest
```

If the command requires a host override, use `--host` or set `CATCH_HOST`.

### Restart a service

Use `yeet restart` and pass the service name via `--service` if needed.

```
go run ./cmd/yeet restart --service <svc>
```

### Remove a service

Use `yeet remove` (alias `rm`) and pass the service name via `--service` if needed.

```
go run ./cmd/yeet remove --service <svc>
```

### Install or update catch on a host

Use `yeet init` with the install user and host. This updates the catch server on the target.

```
go run ./cmd/yeet init root@<host>
```

You can also run `yeet init` without arguments if a default host/user is configured.

## Related Commands

- `yeet status`, `yeet info`, `yeet logs`, `yeet events` for observing service state.
- `yeet env` group for env file management.
- `yeet docker` group for compose and registry operations.

Refer to `references/yeet-help-llm.md` for the authoritative usage text and examples.

## Repo Notes

- The repo’s local guidance for live testing lives in `AGENTS.local.md`.
- `CATCH_HOST` overrides the default remote host for the client.
