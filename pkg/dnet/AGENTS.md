# pkg/dnet Agent Notes

This package implements the Docker network plugin and port-forward
reconciliation against catch database state.

## Local Rules

- Treat namespace, bridge, and iptables operations as privileged side effects.
  Unit-test command construction and reconciliation decisions before live
  testing.
- Keep plugin HTTP handlers strict about request decoding and error responses.
- Preserve deterministic ordering and de-duplication for generated port-forward
  rules.
- Use live testing only when behavior depends on real Docker networking or host
  firewall state.

## Tests

- Run `go test ./pkg/dnet -count=1` after Docker network plugin changes.
- Run `go test ./pkg/dnet ./pkg/db ./pkg/catch -count=1` when persisted network
  state or catch reconciliation behavior is involved.

## Related Context

- Codebase map: `docs/agent/codebase-map.md`
- Docker workflow skill: `.codex/skills/yeet-docker/SKILL.md`
- Quality skill: `.codex/skills/yeet-quality/SKILL.md`
