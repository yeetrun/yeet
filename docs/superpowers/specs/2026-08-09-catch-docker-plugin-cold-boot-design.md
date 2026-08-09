# Catch Docker Plugin Cold-Boot Recovery

## Context

`catch.service` owns Yeet's Docker network plugin and is intentionally ordered
before `docker.service`. Docker must not restore Yeet containers until Catch has
created `/run/docker/plugins/yeet.sock` and can answer network-driver requests.

The `pve1` reboot exposed a circular startup dependency. `runServer` called
`catch.NewServer` before starting the Docker plugin. `catch.NewServer`
synchronously reconciles isolated networks and, when container ISO records
exist, waits up to 30 seconds for Docker. Docker was simultaneously waiting for
Catch's systemd prerequisite and therefore could not become ready. After Catch
timed out, Docker started without the plugin socket, failed Yeet container
network restores with `plugin "yeet" not found`, and did not retry those
containers. Catch's fail-closed behavior then quarantined isolated workloads.

## Goals

- Make the Yeet Docker network plugin available before Catch performs any
  startup work that may wait for Docker.
- Preserve Catch-before-Docker systemd ordering and isolated-network
  fail-closed behavior.
- Prove the ordering contract with a focused regression test that exercises the
  real Unix socket and Docker plugin HTTP endpoint.
- Validate the exact candidate revision through a controlled `pve1` cold boot.

## Non-Goals

- Relaxing or bypassing isolated-network fail-closed behavior.
- Removing Catch's Docker readiness wait before container ISO reconciliation.
- Automatically clearing quarantine records after arbitrary startup failures.
- Reworking stale generated per-service systemd artifacts observed during the
  incident; that is a separate lifecycle issue and is not required to break the
  circular dependency.
- Cutting a Yeet release or tag.

## Design

Introduce a small command-layer startup helper that owns this exact sequence:

1. Create and bind the Yeet Docker plugin Unix socket.
2. Start serving the existing `dnet` plugin handler on that listener.
3. Invoke the Catch server constructor, whose synchronous startup reconciliation
   may wait for Docker.
4. Return both the Catch server and listener to `runServer` for normal RPC,
   registry, and process-lifetime management.

`runServer` passes `catch.NewServer` as the constructor. Constructor injection
is deliberately narrow and exists so the regression test can observe the real
plugin endpoint at the exact constructor boundary without starting tsnet,
systemd, Docker, or a full Catch daemon.

The socket remains process-owned. `runServer` closes the listener and removes
the socket during normal return. The plugin serve loop treats a closed listener
as a normal shutdown so the helper can be tested without leaking a goroutine or
terminating the test process.

## Failure Semantics

If the plugin socket cannot be created, Catch does not begin server startup.
This retains the current fail-fast behavior and prevents Docker from being
released with no usable Yeet network plugin.

Once the socket is serving, `catch.NewServer` retains all existing behavior:
Docker readiness is still bounded, isolated-network reconciliation still runs,
and reconciliation failures still fail closed. The change only removes the
ordering cycle by publishing the prerequisite before entering that work.

## Test Strategy

The command-layer regression test creates a temporary database and Unix socket,
then calls the startup helper with an observing constructor. From inside that
constructor it sends `POST /Plugin.Activate` through the Unix socket and records
the response. The test requires a successful response advertising
`NetworkDriver`. Calling the constructor before binding and serving the plugin
would make the same request fail, reproducing the old ordering bug without a
real Docker daemon.

A second assertion covers the fail-fast path: a socket-listen error must prevent
the Catch constructor from running.

Focused verification runs `mise exec -- go test ./cmd/catch -count=1`. Stable
candidate verification runs the full Go suite, pre-commit, and the repository
quality gate from an exact clean candidate tree so unrelated GitButler branches
cannot affect the result.

## Live Validation

Build and install Catch from the exact single-commit candidate in a temporary
clean clone, then perform one controlled reboot of `root@pve1`. After SSH
returns, verify all of the following for the current boot:

- `catch.service` and `docker.service` are active and no systemd units failed;
- Catch did not log a Docker-readiness timeout during startup;
- Docker did not log `plugin "yeet" not found` during container restore;
- the Yeet database contains no quarantined service records;
- every previously healthy Yeet service is running; and
- Uptime Kuma on `root@hetz` reports the `Pseudo prod` group healthy.

If the cold boot fails, stop publication, use the existing database backup and
service evidence to recover the host, and revise the implementation before
landing.

## Completion

The fix is complete when the focused test proves the ordering contract, the
repository gates pass for the exact candidate, `pve1` cold-boots without the
incident signatures, and the single signed GitButler commit is verified on
local `main`, `origin/main`, and the remote `main` ref.
