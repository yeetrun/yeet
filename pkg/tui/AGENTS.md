# pkg/tui Agent Notes

This package contains terminal color and spinner helpers.

## Local Rules

- Respect `NO_COLOR`, dumb terminals, and explicit caller enablement for color.
- Spinner output is user-facing; tests should assert escape sequences and
  cursor cleanup when behavior changes.
- Keep writers injectable so tests do not need a real terminal.
- Avoid broad UI policy here; CLI command formatting belongs near the command
  renderer.

## Tests

- Run `go test ./pkg/tui -count=1` after color or spinner changes.

## Related Context

- Codebase map: `docs/agent/codebase-map.md`
- Docs skill: `.codex/skills/yeet-docs/SKILL.md`
