# cmd/yeet Agent Notes

This directory contains the client CLI entrypoint and user-facing command
routing. Keep command-line behavior predictable and covered by tests.

## Local Rules

- `yeet.go` wires global flags, runtime overrides, subcommand handlers, and
  yargs groups.
- `cli.go` owns top-level help metadata for local groups and global flags.
- `cli_bridge.go` resolves service arguments such as `<svc>` and `<svc>@<host>`
  before forwarding commands to `pkg/yeet`.
- Keep parsing side effects small. Prefer shared parser definitions in
  `pkg/cli` over ad hoc local parsing.
- When user-facing syntax changes, update CLI help tests, README examples, and
  website docs in the same work session.

## Tests

- Run `go test ./cmd/yeet -count=1` after changing this directory.
- Run `go test ./pkg/cli ./cmd/yeet ./pkg/yeet -count=1` when command routing
  or bridge behavior changes.

## Related Context

- Codebase map: `docs/agent/codebase-map.md`
- CLI operations skill: `.codex/skills/yeet-cli/SKILL.md`

Planned for later agent-context tasks:

- Docs skill: `.codex/skills/yeet-docs/SKILL.md`
