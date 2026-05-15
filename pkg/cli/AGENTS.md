# pkg/cli Agent Notes

This package defines shared command metadata, flag structs, parser helpers, and
help text used by both yeet and catch.

## Local Rules

- Keep CLI flags in struct tags and use kebab-case names.
- Register command names and group commands in the shared registries instead of
  scattering magic strings.
- Prefer table-driven tests for parser behavior, invalid values, repeated
  flags, positional arguments, and default values.
- Parser changes often require updates in `cmd/yeet` bridge tests and
  `pkg/yeet` command handling tests.
- Do not make `pkg/cli` depend on client or server packages.

## Tests

- Run `go test ./pkg/cli -count=1` after parser or help metadata changes.
- Run `go test ./pkg/cli ./cmd/yeet ./pkg/yeet -count=1` when parser changes
  affect service-argument bridging or client routing.

## Related Context

- Codebase map: `docs/agent/codebase-map.md`

Planned for later agent-context tasks:

- RPC flow skill: `.codex/skills/yeet-rpc/SKILL.md`
- Docs skill: `.codex/skills/yeet-docs/SKILL.md`
