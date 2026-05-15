# pkg/env Agent Notes

This package writes struct values as environment files.

## Local Rules

- Preserve deterministic env-file output so generated artifacts are stable.
- Keep reflection behavior conservative; avoid silently emitting unsupported
  field shapes.
- Add tests for quoting, zero values, and field-tag behavior when the env format
  changes.

## Tests

- Run `go test ./pkg/env -count=1` after env serialization changes.

## Related Context

- Codebase map: `docs/agent/codebase-map.md`
- Docs skill: `.codex/skills/yeet-docs/SKILL.md`
