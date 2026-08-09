# Catch Docker Plugin Cold-Boot Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate Catch's cold-boot dependency cycle by serving the Yeet Docker network plugin before synchronous Catch startup reconciliation can wait for Docker.

**Architecture:** Keep the plugin and Catch server in the command process, but move listener creation and serving behind a narrow startup helper that runs before `catch.NewServer`. Test the exact ordering boundary with a real temporary Unix socket and the existing `/Plugin.Activate` handler, then validate the candidate with a controlled `pve1` reboot.

**Tech Stack:** Go 1.25 via `mise`, Unix sockets, Docker remote network-driver HTTP API, systemd, GitButler, SSH.

## Global Constraints

- Preserve Catch-before-Docker systemd ordering.
- Preserve Docker readiness waits and isolated-network fail-closed behavior.
- Do not automatically clear quarantines or alter database state transitions.
- Keep startup wiring in `cmd/catch`; do not move package behavior into the command.
- Make no user-facing CLI, RPC, permission, configuration, or documentation change.
- Preserve every unrelated GitButler branch and uncommitted file.
- Land one signed commit on `main` and push only after exact-tree gates and live cold-boot verification pass.
- Do not cut a tag or release.

---

## File Structure

- Create `docs/superpowers/specs/2026-08-09-catch-docker-plugin-cold-boot-design.md`: record the incident, approved design, non-goals, and validation contract.
- Create `docs/superpowers/plans/2026-08-09-catch-docker-plugin-cold-boot.md`: provide this executable plan.
- Modify `cmd/catch/catch.go`: bind and serve the Docker plugin before constructing the Catch server and manage the listener lifecycle.
- Modify `cmd/catch/catch_test.go`: reproduce and prevent the startup-order regression with a real Unix socket.

### Task 1: Establish the Parallel GitButler Branch and Plan

**Files:**
- Create: `docs/superpowers/specs/2026-08-09-catch-docker-plugin-cold-boot-design.md`
- Create: `docs/superpowers/plans/2026-08-09-catch-docker-plugin-cold-boot.md`

- [ ] **Step 1: Verify the base and existing workspace ownership**

Run:

```bash
but pull --check
but status -fv
git status --short --branch
```

Expected: `origin/main` is current; unrelated branches and uncommitted changes
are visible and remain untouched.

- [ ] **Step 2: Create the dedicated parallel branch**

Run:

```bash
but branch new codex/catch-docker-plugin-boot
```

Expected: GitButler reports a new unstacked branch based on `origin/main`.

- [ ] **Step 3: Review the design and plan for scope and exactness**

Run:

```bash
git diff --check -- \
  docs/superpowers/specs/2026-08-09-catch-docker-plugin-cold-boot-design.md \
  docs/superpowers/plans/2026-08-09-catch-docker-plugin-cold-boot.md
```

Expected: exit `0`, with no whitespace errors.

### Task 2: Add the Failing Startup-Order Regression Test

**Files:**
- Modify: `cmd/catch/catch_test.go`

**Interfaces:**
- Test target: `startCatchServerWithDockerPlugin(config, socket, constructor)`.
- Behavioral boundary: the real Unix socket must answer `POST /Plugin.Activate`
  before `constructor` is invoked.

- [ ] **Step 1: Write the behavior-first regression test**

Add `TestStartCatchServerWithDockerPluginServesBeforeCatchStartup`. It must:

- create a temporary `cdb.Store` and socket path;
- create an HTTP client whose transport dials that Unix socket;
- call `startCatchServerWithDockerPlugin` with an observing constructor;
- send `POST /Plugin.Activate` from inside the constructor;
- assert HTTP `200` and the exact response `{"Implements":["NetworkDriver"]}`;
- close the returned listener; and
- assert the constructor ran only after the endpoint was available.

Add `TestStartCatchServerWithDockerPluginListenFailureSkipsCatchStartup`. It
must use an invalid socket parent and assert that the constructor is never run.

- [ ] **Step 2: Run the focused test and prove it fails for the missing behavior**

Run:

```bash
mise exec -- go test ./cmd/catch -run 'TestStartCatchServerWithDockerPlugin' -count=1
```

Expected: non-zero with `undefined: startCatchServerWithDockerPlugin` before
production code is changed.

### Task 3: Implement the Minimal Startup-Ordering Fix

**Files:**
- Modify: `cmd/catch/catch.go`

**Interfaces:**
- Add `type catchServerConstructor func(*catch.Config) *catch.Server`.
- Add `catchServerRuntime` to carry the constructed server and bound plugin
  listener without increasing `runServer`'s branch complexity.
- Add `startCatchServerWithDockerPlugin(*catch.Config, string, catchServerConstructor) (catchServerRuntime, error)`.

- [ ] **Step 1: Implement the startup helper**

The helper must call `listenDockerPluginSocket`, start
`serveDockerPlugin` in a goroutine, and only then invoke the constructor. A
listen error must return without invoking the constructor.

- [ ] **Step 2: Wire `runServer` through the helper**

Replace the current `catch.NewServer` followed by `go startDockerPlugin` with a
single helper call. On error, fail startup. On success, defer listener close and
socket removal for normal return before starting registry/RPC servers.

- [ ] **Step 3: Make listener close a normal plugin-server shutdown**

Update `serveDockerPlugin` so `net.ErrClosed`, like `http.ErrServerClosed`, is
not fatal. Do not suppress unrelated serve errors.

- [ ] **Step 4: Format and run the regression tests**

Run:

```bash
mise exec -- gofmt -w cmd/catch/catch.go cmd/catch/catch_test.go
mise exec -- go test ./cmd/catch -run 'TestStartCatchServerWithDockerPlugin' -count=1
mise exec -- go test ./cmd/catch -count=1
```

Expected: all commands exit `0`.

- [ ] **Step 5: Prove the test is coupled to the fix**

Temporarily reverse the helper order so the constructor runs before the plugin
listener, rerun the focused test, and require it to fail on the Unix request.
Restore the correct implementation, format, and rerun the focused test to green.

### Task 4: Commit and Verify the Exact Candidate

**Files:**
- All four task-owned files only.

- [ ] **Step 1: Inspect and select only this task's changes**

Run:

```bash
but diff
git diff --check -- \
  cmd/catch/catch.go cmd/catch/catch_test.go \
  docs/superpowers/specs/2026-08-09-catch-docker-plugin-cold-boot-design.md \
  docs/superpowers/plans/2026-08-09-catch-docker-plugin-cold-boot.md
```

Expected: the candidate contains only the startup fix, tests, design, and plan.

- [ ] **Step 2: Run the repository gates once at the stable boundary**

Run:

```bash
mise exec -- go test ./...
pre-commit run --all-files
mise run quality
```

Expected: all commands exit `0`. If a shared-workspace gate sees unrelated
changes, rerun it from an exact clean clone of the candidate instead of touching
another branch's work.

- [ ] **Step 3: Create one signed GitButler commit**

Use the change IDs from `but diff`:

```bash
but commit codex/catch-docker-plugin-boot -m 'catch: serve Docker plugin before startup reconciliation' --changes <task-owned-change-ids>
```

Expected: one commit on `codex/catch-docker-plugin-boot`, based directly on
current `origin/main`, containing only the four task-owned files. Verify with:

```bash
but status -fv
git show --stat --oneline <candidate-sha>
git diff --check <candidate-sha>^ <candidate-sha>
git verify-commit <candidate-sha>
```

### Task 5: Deploy the Candidate and Prove a Cold Boot

**Files:**
- No repository changes.

- [ ] **Step 1: Materialize and retest the exact candidate in a clean clone**

Create a temporary directory with `mktemp -d`, clone the local repository, and
check out the candidate SHA detached. Run:

```bash
mise exec -- go test ./...
pre-commit run --all-files
mise run quality
```

Expected: the exact candidate passes independently of the shared workspace.

- [ ] **Step 2: Install the exact candidate on `pve1`**

From the clean clone, run:

```bash
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet init root@pve1
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet version
```

Expected: installation succeeds and the remote Catch revision matches the
candidate revision.

- [ ] **Step 3: Record pre-reboot health and reboot once**

Record Yeet service status, Catch/Docker unit state, failed units, current boot
ID, quarantine count, and Uptime Kuma's `Pseudo prod` state. Then run:

```bash
ssh root@pve1 reboot
```

Poll SSH in short bounded attempts until the boot ID changes and both Catch and
Docker settle.

- [ ] **Step 4: Verify the current boot and external monitor**

Require all services running, zero quarantines, zero failed units, no current-
boot Catch Docker-readiness timeout, no current-boot Docker `plugin "yeet" not
found`, and Uptime Kuma's `Pseudo prod` group healthy on `root@hetz`.

Expected: the host cold-boots directly into a healthy state without manual
database repair or service restarts.

### Task 6: Land, Push, Verify, and Reconcile GitButler

**Files:**
- No additional repository changes.

- [ ] **Step 1: Recheck the publication race and candidate shape**

Run:

```bash
but pull --check
git merge-base --is-ancestor origin/main <candidate-sha>
git diff --name-only origin/main..<candidate-sha>
git verify-commit <candidate-sha>
```

Expected: the base is current, the candidate is a direct single commit, and
only the four task-owned files changed.

- [ ] **Step 2: Land on remote `main`**

Run:

```bash
git push origin <candidate-sha>:refs/heads/main
```

Treat a non-fast-forward rejection as a race: run `but pull`, rebuild/retest the
candidate on the new base, and retry only when clean.

- [ ] **Step 3: Reconcile and clean only this session branch**

Run:

```bash
git fetch origin main
but pull
but clean --dry-run
but clean
```

If local `main` still lags, update only local `main` to `origin/main` under the
repository's authorized finish-to-main exception. Never remove another branch.

- [ ] **Step 4: Verify landed truth**

Run:

```bash
git rev-parse main origin/main
git ls-remote origin refs/heads/main
but status -fv
```

Expected: local `main`, `origin/main`, and remote `main` all equal the signed
candidate; this session branch is integrated/cleaned; all unrelated branches
and uncommitted changes remain present.
