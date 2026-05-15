# pkg/catchrpc Agent Notes

This package contains JSON-RPC types plus HTTP/WebSocket client helpers shared
by `yeet` and `catch`.

## Local Rules

- Keep wire structs backward-compatible unless the matching catch handler and
  client call sites are updated together.
- Preserve JSON-RPC raw-message behavior for request IDs, params, results, and
  errors.
- Treat WebSocket exec and event streaming as cancellation-sensitive; test
  context cancellation, close frames, and partial writes when touched.
- Do not import `pkg/yeet` or `pkg/catch` from this package.

## Tests

- Run `go test ./pkg/catchrpc -count=1` after RPC type or client changes.
- Run `go test ./pkg/catchrpc ./pkg/catch ./pkg/yeet -count=1` when changing a
  request or response shape used by both sides.
- Fuzz or table-test JSON codecs when accepting new network input.

## Related Context

- Codebase map: `docs/agent/codebase-map.md`
- RPC flow skill: `.codex/skills/yeet-rpc/SKILL.md`
