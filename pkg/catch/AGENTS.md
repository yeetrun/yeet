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
- RPC flow skill: `.codex/skills/yeet-rpc/SKILL.md`

Planned for later agent-context tasks:

- Docker workflow skill: `.codex/skills/yeet-docker/SKILL.md`
