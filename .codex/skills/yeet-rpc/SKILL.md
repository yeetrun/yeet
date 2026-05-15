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
