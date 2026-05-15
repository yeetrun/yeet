# pkg/targz Agent Notes

This package provides a small tar.gz reader wrapper and per-entry callback
helper.

## Local Rules

- Preserve callback semantics: `ReadFile` calls the callback once per tar entry
  and stops on callback errors.
- Keep close-error capture intact so gzip close failures are not hidden.
- Do not expand this package into extraction logic; `pkg/copyutil` owns richer
  tar extraction behavior.

## Tests

- Run `go test ./pkg/targz -count=1` after tar.gz reader changes.

## Related Context

- Codebase map: `docs/agent/codebase-map.md`
- Quality skill: `.codex/skills/yeet-quality/SKILL.md`
