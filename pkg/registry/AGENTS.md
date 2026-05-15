# pkg/registry Agent Notes

This package implements the OCI registry, filesystem storage, containerd cache
storage, and registry error responses.

## Local Rules

- Preserve OCI Distribution API behavior for manifests, blobs, uploads, mounts,
  tags, and error response shapes.
- Keep path parsing strict. Add table cases for nested repositories, upload
  URLs, digest paths, and invalid requests when changing routing.
- Compression behavior is delegated through `pkg/compress`; test both packages
  when response or request compression changes.
- Containerd cache storage should keep labels, media types, and digest handling
  compatible with OCI clients.

## Tests

- Run `go test ./pkg/registry -count=1` after registry changes.
- Run `go test ./pkg/registry ./pkg/compress -count=1` when compression or HTTP
  transport behavior changes.
- Run fuzz or table tests for parser changes in registry paths, repository
  names, and digests.

## Related Context

- Codebase map: `docs/agent/codebase-map.md`
- Quality skill: `.codex/skills/yeet-quality/SKILL.md`
