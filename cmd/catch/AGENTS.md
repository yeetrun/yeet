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

- Codebase map: `docs/agent/codebase-map.md`

Planned for later agent-context tasks:

- Catch server notes: `pkg/catch/AGENTS.md`
