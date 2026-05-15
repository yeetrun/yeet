# pkg/codecutil Agent Notes

This package contains zstd file compression and decompression helpers.

## Local Rules

- Preserve source and destination path handling; callers expect file-to-file
  helpers, not streaming APIs.
- Keep deferred close error capture intact so close failures are returned when
  no earlier error occurred.
- Add tests for corrupt input, missing files, and round trips when touching
  codec behavior.

## Tests

- Run `go test ./pkg/codecutil -count=1` after codec changes.

## Related Context

- Codebase map: `docs/agent/codebase-map.md`
- Quality skill: `.codex/skills/yeet-quality/SKILL.md`
