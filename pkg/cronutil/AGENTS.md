# pkg/cronutil Agent Notes

This package converts cron expressions into calendar-style strings used by
service scheduling output.

## Local Rules

- Preserve the exported `CronToCalender` spelling unless you intentionally
  migrate all callers.
- Treat cron parsing as input parsing: table-test malformed fields, names,
  ranges, and defaults.
- Keep day-of-week and month normalization deterministic.

## Tests

- Run `go test ./pkg/cronutil -count=1` after schedule parsing changes.

## Related Context

- Codebase map: `docs/agent/codebase-map.md`
- Quality skill: `.codex/skills/yeet-quality/SKILL.md`
