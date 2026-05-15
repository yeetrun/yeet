# pkg/compress Agent Notes

This package contains HTTP request decompression and response compression
helpers used by registry and transfer paths.

## Local Rules

- Preserve content negotiation semantics: honor quality values, reject `q=0`,
  and prefer `zstd > gzip > deflate` when qualities tie.
- Response compression must set `Content-Encoding`, set `Vary:
  Accept-Encoding`, and remove stale `Content-Length`.
- Request decompression must wrap and close bodies without leaking the original
  body.
- Add table cases for wildcard and unsupported encodings when changing
  negotiation.

## Tests

- Run `go test ./pkg/compress -count=1` after compression changes.
- Run `go test ./pkg/compress ./pkg/registry -count=1` when registry behavior
  consumes the changed helper.

## Related Context

- Codebase map: `docs/agent/codebase-map.md`
- Quality skill: `.codex/skills/yeet-quality/SKILL.md`
