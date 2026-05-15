# pkg/copyutil Agent Notes

This package contains tar streaming, extraction, file metadata, and tree-move
helpers for copying artifacts.

## Local Rules

- Preserve archive traversal protection. Extraction must not write outside the
  requested destination.
- Keep symlink, hardlink, directory, and special-file handling covered by tests.
- Respect platform-specific behavior in `special_unix.go` and
  `special_windows.go`.
- Do not drop metadata handling unless callers explicitly do not need
  permissions or timestamps.

## Tests

- Run `go test ./pkg/copyutil -count=1` after copy, tar, extraction, or move
  changes.
- Run `go test ./pkg/copyutil ./pkg/yeet ./pkg/catch -count=1` when changing
  artifact copy semantics used by client/server flows.

## Related Context

- Codebase map: `docs/agent/codebase-map.md`
- Quality skill: `.codex/skills/yeet-quality/SKILL.md`
