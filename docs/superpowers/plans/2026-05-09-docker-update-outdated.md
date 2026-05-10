# Docker Update Outdated Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `docker outdated` table output compact and add host-wide `yeet docker update --outdated`.

**Architecture:** Keep exact digests in JSON. Share compact table formatting through `pkg/svc`, and implement `--outdated` as local client orchestration that scans each host and invokes existing remote scoped `docker update` once per outdated service.
Batch updates print a short host/service marker before each service, then stream
the same compose output as an individual `yeet docker update <svc>` run.

**Tech Stack:** Go, `pkg/yargs` flag parsing, existing yeet RPC execution, Docker compose service helpers.

---

### Task 1: Compact Docker Outdated Tables

**Files:**
- Modify: `pkg/svc/docker_outdated.go`
- Modify: `pkg/svc/docker_outdated_test.go`
- Modify: `pkg/yeet/docker_outdated.go`
- Modify: `pkg/yeet/docker_outdated_test.go`
- Modify: `pkg/catch/tty_ops.go`
- Modify: `pkg/catch/tty_ops_test.go`

- [x] **Step 1: Write failing tests**

Add tests that assert default table output has no `RUNNING`, `LATEST`, or full
`sha256:` digest columns, and that image references render as compact tags such
as `linuxserver/plex:latest`.

- [x] **Step 2: Verify red**

Run:

```bash
go test ./pkg/svc ./pkg/yeet ./pkg/catch -run 'Test.*DockerOutdated.*Table|TestCompactDockerOutdated' -count=1
```

Expected: FAIL because current table output includes digest columns.

- [x] **Step 3: Implement shared compact display helpers**

Add helpers in `pkg/svc/docker_outdated.go` to compact image refs and statuses
for table rendering while leaving JSON structs unchanged.

- [x] **Step 4: Update local and remote table renderers**

Change `pkg/yeet/docker_outdated.go` and `pkg/catch/tty_ops.go` table renderers
to use compact columns.

- [x] **Step 5: Verify green**

Run:

```bash
go test ./pkg/svc ./pkg/yeet ./pkg/catch -run 'Test.*DockerOutdated.*Table|TestCompactDockerOutdated' -count=1
```

Expected: PASS.

### Task 2: Host-Wide `docker update --outdated`

**Files:**
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`
- Modify: `cmd/yeet/cli_bridge_test.go`
- Modify: `pkg/yeet/svc_cmd.go`
- Modify: `pkg/yeet/docker_outdated.go`
- Modify: `pkg/yeet/docker_outdated_test.go`

- [x] **Step 1: Write failing parser/routing tests**

Add tests for:

- `ParseDockerUpdate([]string{"--outdated"})`
- `docker update --outdated` not requiring a service
- `docker update --outdated svc` rejecting service-scoped usage
- bridge parsing skipping the `--outdated` flag when a service is present

- [x] **Step 2: Verify red**

Run:

```bash
go test ./pkg/cli ./cmd/yeet ./pkg/yeet -run 'Test.*DockerUpdate.*Outdated|TestBridgeServiceArgsDockerUpdate' -count=1
```

Expected: FAIL because the flag and local handler do not exist.

- [x] **Step 3: Implement parsing and local routing**

Add `DockerUpdateFlags{Outdated bool}`, parse `--outdated`, register the flag,
and route `docker update --outdated` to a local host-wide handler.

- [x] **Step 4: Implement batch update orchestration**

Use existing `fetchDockerOutdatedForHostFn` to discover rows, dedupe services
whose status is `update available`, and call a testable update hook that runs
remote scoped `docker update` for each service on its host, streaming the normal
remote command output.

- [x] **Step 5: Verify green**

Run:

```bash
go test ./pkg/cli ./cmd/yeet ./pkg/yeet -run 'Test.*DockerUpdate.*Outdated|TestBridgeServiceArgsDockerUpdate' -count=1
```

Expected: PASS.

### Task 3: Docs And Live Verification

**Files:**
- Modify: `README.md`
- Modify: `website/docs/cli/yeet-cli.mdx`
- Modify: relevant workflow docs under `website/docs/operations/`

- [x] **Step 1: Update docs**

Document compact `docker outdated` output, full JSON digests, and
`yeet docker update --outdated`.

- [x] **Step 2: Run full local gates**

Run:

```bash
go test ./... -count=1
pre-commit run --all-files
```

Expected: PASS.

- [x] **Step 3: Deploy and test live**

Run:

```bash
CATCH_HOST=yeet-pve1 go run ./cmd/yeet --progress plain init root@pve1
CATCH_HOST=yeet-hetz go run ./cmd/yeet --progress plain init root@hetz
CATCH_HOST=yeet-pve1 go run ./cmd/yeet --progress plain docker outdated
CATCH_HOST=yeet-hetz go run ./cmd/yeet --progress plain docker outdated
```

Expected: compact table output on both hosts. Do not run live
`docker update --outdated` without explicit final confirmation because it
changes running services.
