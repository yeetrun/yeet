# pkg/svc Agent Notes

This package contains service-domain helpers for Docker, Docker compose,
systemd, network namespaces, and Docker image update checks.

## Local Rules

- Keep helpers deterministic and unit-testable. Stub command execution where
  possible instead of requiring Docker or systemd in unit tests.
- Docker compose behavior should preserve user-facing output and avoid
  unnecessary restarts.
- Docker outdated logic compares running container state with the compose
  declared upstream image reference. Table output can be compact, but JSON
  should preserve exact digest information.
- Systemd and network helpers must treat race detector findings as real bugs
  unless proven otherwise.

## Tests

- Run `go test ./pkg/svc -count=1` after service helper changes.
- Run `go test ./pkg/svc ./pkg/catch -count=1` when catch command behavior uses
  changed helpers.
- Run race/fuzz/quality gates when touching parser, path, concurrency, or
  network-input surfaces.

## Related Context

- Codebase map: `docs/agent/codebase-map.md`
- Docker workflow skill: `.codex/skills/yeet-docker/SKILL.md`
- Quality skill: `.codex/skills/yeet-quality/SKILL.md`
