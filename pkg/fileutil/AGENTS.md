# pkg/fileutil Agent Notes

This package contains low-level file copy, versioned filename, and identity
helpers.

## Local Rules

- Keep file updates atomic where the helper already writes through temporary
  paths.
- Preserve permission handling and close-error capture for copied files.
- Version helpers are used in artifact naming; table-test extension and suffix
  cases before changing the regex.
- `Identical` should return errors for directory/file misuse instead of hiding
  invalid comparisons.

## Tests

- Run `go test ./pkg/fileutil -count=1` after file helper changes.
- Run `go test ./pkg/fileutil ./pkg/catch ./pkg/yeet -count=1` when artifact
  paths or versioning behavior changes.

## Related Context

- Codebase map: `docs/agent/codebase-map.md`
- Quality skill: `.codex/skills/yeet-quality/SKILL.md`
