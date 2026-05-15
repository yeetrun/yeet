# pkg/ftdetect Agent Notes

This package detects service artifact file types from filenames, contents, and
binary headers.

## Local Rules

- Treat detection as parser behavior. Add table cases for each new extension,
  shebang, magic byte, or binary architecture rule.
- Keep `goos` and `goarch` parameters explicit so tests can cover non-host
  targets.
- Prefer conservative `Unknown` errors over guessing when evidence is
  ambiguous.
- Docker compose detection affects deploy routing; update docs/tests when that
  behavior changes.

## Tests

- Run `go test ./pkg/ftdetect -count=1` after detection changes.
- Run `go test ./pkg/ftdetect ./pkg/yeet -count=1` when routing from detected
  file type changes.

## Related Context

- Codebase map: `docs/agent/codebase-map.md`
- Docs skill: `.codex/skills/yeet-docs/SKILL.md`
