# pkg/yeet Agent Notes

This package contains client-side orchestration for the `yeet` CLI: service and
host resolution, project config, local handling, remote exec, init, copy, SSH,
and status rendering.

## Local Rules

- Preserve the RPC CLI flow: `cmd/yeet` resolves global routing, `pkg/yeet`
  decides local vs remote handling, and `catch` remains authoritative for remote
  command parsing.
- Prefer forwarding existing command shapes through `catchrpc.Exec`; add
  structured RPCs only when the command cannot reasonably be represented as
  remote CLI execution.
- Be careful with global host/service state in tests. Preserve and restore
  package globals around tests that mutate preferences or overrides.
- User-facing behavior changes usually require README and website docs updates.
- Live host commands can affect real services. Use `AGENTS.local.md` and the
  `yeet-cli` skill before running them.

## Tests

- Run `go test ./pkg/yeet -count=1` after client orchestration changes.
- Run `go test ./pkg/cli ./cmd/yeet ./pkg/yeet -count=1` after command routing
  changes.
- Run `go test ./... -count=1` before broad merges or releases.

## Related Context

- Codebase map: `docs/agent/codebase-map.md`
- CLI operations skill: `.codex/skills/yeet-cli/SKILL.md`

Planned for later agent-context tasks:

- RPC flow skill: `.codex/skills/yeet-rpc/SKILL.md`
