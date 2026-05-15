---
name: yeet-docker
description: Use when changing Docker compose deployment, docker pull/update/outdated behavior, internal registry image handling, Docker networking, or digest comparison logic.
---

# Yeet Docker

Use this skill for Docker compose and registry behavior.

## Starting Points

- Client orchestration: `pkg/yeet/docker_outdated.go`, `pkg/yeet/svc_cmd.go`
- Catch TTY commands: `pkg/catch/tty_ops.go`
- Service helpers: `pkg/svc/docker.go`, `pkg/svc/docker_outdated.go`
- Registry: `pkg/registry/`, `pkg/catch/registry.go`

## Rules

- `yeet run` for compose does not pull images by default.
- `yeet docker update <svc>` pulls and recreates one compose service.
- `yeet docker outdated` is read-only and should preserve exact digests in JSON.
- Compact table output should avoid raw digest noise.
- Internal yeet registry images and upstream registry images have different
  semantics; inspect existing tests before changing comparison logic.
- Live Docker update commands affect running containers.

## Tests

```bash
go test ./pkg/svc ./pkg/catch ./pkg/yeet -run 'Test.*Docker' -count=1
go test ./pkg/svc ./pkg/catch ./pkg/yeet -count=1
```
