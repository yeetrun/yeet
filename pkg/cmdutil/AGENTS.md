# pkg/cmdutil Agent Notes

This package contains small command-line helpers for subprocess wiring and
confirmation prompts.

## Local Rules

- Keep helpers dependency-light and reusable from both client and server code.
- Preserve explicit reader/writer injection for prompts so tests do not need
  real stdin or stdout.
- `Confirm` accepts only `y` or `Y`; do not broaden confirmation semantics
  without checking every destructive caller.
- `NewStdCmd*` intentionally wires commands to the process standard streams.

## Tests

- Run `go test ./pkg/cmdutil -count=1` after changing prompt or command helper
  behavior.

## Related Context

- Codebase map: `docs/agent/codebase-map.md`
- Quality skill: `.codex/skills/yeet-quality/SKILL.md`
