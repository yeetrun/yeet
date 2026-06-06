# Fresh VPS E2E Friction Test Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run a live, sanitized, pre-release E2E validation pass from `main` against a fresh Ubuntu VPS and produce a friction log plus cleanup evidence.

**Architecture:** The pass runs from an isolated local `.tmp/` project directory so repo-local `yeet.toml` is not touched. The local `main` client installs catch from source, then tests one representative workflow per payload family and records environment-limited skips separately from yeet failures. Raw logs and host details stay in untracked local files; committed docs remain sanitized.

**Tech Stack:** Go/mise for building yeet from `main`, SSH for fresh-host inspection, yeet/catch RPC over tsnet, Docker/systemd on the VPS, optional Firecracker VM checks when KVM is available.

---

## File Structure

- Create local untracked run directory: `$YEET_E2E_ROOT/`
  - `bin/yeet-main`: built client from local `main`.
  - `project/`: isolated working directory for generated test payloads and `yeet.toml`.
  - `logs/`: full command logs.
  - `friction-log.md`: structured findings.
  - `report.md`: final pass/fail summary.
- Do not modify source files during the test pass unless a confirmed fix is split into a follow-up implementation task.
- Do not commit raw run notes, the VPS SSH target, public IP, auth material, or generated `yeet.toml` files.

## Task 1: Prepare Isolated Local E2E Workspace

**Files:**
- Create: `$YEET_E2E_ROOT/`
- Create: `$YEET_E2E_PROJECT/compose.yml`
- Create: `$YEET_E2E_PROJECT/hello-service.sh`
- Create: `$YEET_E2E_PROJECT/cron-job.sh`
- Create: `$YEET_E2E_ROOT/friction-log.md`

- [ ] **Step 1: Require execution-time target variables without committing them**

Run from the repository root:

```bash
export YEET_E2E_SSH_TARGET="${YEET_E2E_SSH_TARGET:?set YEET_E2E_SSH_TARGET in the shell before executing this plan}"
export YEET_E2E_CATCH_HOST="${YEET_E2E_CATCH_HOST:-catch}"
export YEET_E2E_PUBLIC_HOST="${YEET_E2E_PUBLIC_HOST:-${YEET_E2E_SSH_TARGET#*@}}"
```

Expected: shell exits immediately if `YEET_E2E_SSH_TARGET` is not set. No target value is written to a committed file.

- [ ] **Step 2: Create an isolated run directory**

```bash
export YEET_E2E_ROOT="$(pwd)/.tmp/fresh-vps-e2e-$(date +%Y%m%d-%H%M%S)"
export YEET_E2E_PROJECT="$YEET_E2E_ROOT/project"
export YEET_E2E_LOG_DIR="$YEET_E2E_ROOT/logs"
export YEET_E2E_YEET="$YEET_E2E_ROOT/bin/yeet-main"
export YEET_E2E_PREFIX="e2e$(date +%H%M%S)"
mkdir -p "$YEET_E2E_ROOT/bin" "$YEET_E2E_PROJECT" "$YEET_E2E_LOG_DIR"
```

Expected: the directories exist under `.tmp/`; `git status --short` does not show them because `.tmp/` is ignored.

- [ ] **Step 3: Initialize friction log and final report files**

```bash
{
  printf '# Fresh VPS E2E Friction Log\n\n'
  printf 'Run root: `%s`\n\n' "$YEET_E2E_ROOT"
  printf '## Findings\n\n'
} > "$YEET_E2E_ROOT/friction-log.md"
{
  printf '# Fresh VPS E2E Report\n\n'
  printf 'Run root: `%s`\n\n' "$YEET_E2E_ROOT"
  printf '## Summary\n\n'
  printf 'Report is populated during Task 9.\n'
} > "$YEET_E2E_ROOT/report.md"
```

Expected: both files exist under the untracked run directory.

- [ ] **Step 4: Build the yeet client from current `main`**

```bash
mise exec -- go build -o "$YEET_E2E_YEET" ./cmd/yeet
"$YEET_E2E_YEET" --help >/dev/null
```

Expected: both commands exit 0. The binary is local to the run directory.

- [ ] **Step 5: Create local test payloads**

```bash
cat > "$YEET_E2E_PROJECT/compose.yml" <<'YAML'
services:
  web:
    image: nginx:alpine
    ports:
      - "18081:80"
YAML

cat > "$YEET_E2E_PROJECT/hello-service.sh" <<'SH'
#!/bin/sh
set -eu
while true; do
  date -u +"hello-service %Y-%m-%dT%H:%M:%SZ"
  sleep 5
done
SH
chmod +x "$YEET_E2E_PROJECT/hello-service.sh"

cat > "$YEET_E2E_PROJECT/cron-job.sh" <<'SH'
#!/bin/sh
set -eu
echo "cron-job $(date -u +%Y-%m-%dT%H:%M:%SZ)"
SH
chmod +x "$YEET_E2E_PROJECT/cron-job.sh"
```

Expected: the compose file and scripts exist only under the isolated project directory.

- [ ] **Step 6: Define a local helper for manual finding entries**

```bash
cat >> "$YEET_E2E_ROOT/friction-log.md" <<'MD'
### Finding Template

- Phase:
- Command:
- Expected:
- Actual:
- Severity:
- Classification:
- Reproduction:
- Evidence:
- Cleanup action:
- Proposed next step:

MD
```

Expected: the friction log contains the template.

## Task 2: Capture Fresh Host Baseline

**Files:**
- Write: `$YEET_E2E_LOG_DIR/01-host-baseline.log`
- Write: `$YEET_E2E_ROOT/friction-log.md`

- [ ] **Step 1: Verify raw SSH access and record first-contact friction**

```bash
ssh "$YEET_E2E_SSH_TARGET" 'printf "ssh-ok\n"' 2>&1 | tee "$YEET_E2E_LOG_DIR/00-ssh-first-contact.log"
```

Expected: output includes `ssh-ok`. If this prompts for host-key trust, record whether the prompt is clear. If it fails, add a blocker finding and stop before changing the host.

- [ ] **Step 2: Capture OS, package, resource, network, and virtualization baseline**

```bash
ssh "$YEET_E2E_SSH_TARGET" 'set -u
printf "== os ==\n"
cat /etc/os-release || true
printf "== kernel ==\n"
uname -a
printf "== arch ==\n"
uname -m
printf "== systemd ==\n"
systemctl is-system-running || true
printf "== resources ==\n"
nproc || true
free -h || true
df -h / || true
printf "== network ==\n"
ip -brief addr || true
ip route || true
printf "== tools ==\n"
for tool in docker tailscale zfs zpool qemu-img zstd e2fsck resize2fs mount umount ip apt-get curl; do
  if command -v "$tool" >/dev/null 2>&1; then
    printf "%s: present %s\n" "$tool" "$(command -v "$tool")"
  else
    printf "%s: missing\n" "$tool"
  fi
done
printf "== devices ==\n"
test -e /dev/kvm && printf "/dev/kvm: present\n" || printf "/dev/kvm: missing\n"
test -e /dev/net/tun && printf "/dev/net/tun: present\n" || printf "/dev/net/tun: missing\n"
' 2>&1 | tee "$YEET_E2E_LOG_DIR/01-host-baseline.log"
```

Expected: baseline log clearly identifies Ubuntu version, WAN-only networking shape, and whether KVM, TUN/TAP, Docker, VM tools, and ZFS are available.

- [ ] **Step 3: Record environment-limited test decisions**

Append to `$YEET_E2E_ROOT/friction-log.md`:

```bash
{
  printf '## Host Capability Notes\n\n'
  grep -E '/dev/kvm:|/dev/net/tun:|docker:|zfs:|zpool:' "$YEET_E2E_LOG_DIR/01-host-baseline.log" || true
  printf '\n'
} >> "$YEET_E2E_ROOT/friction-log.md"
```

Expected: the friction log has explicit capability notes before feature testing starts.

## Task 3: Bootstrap Catch From `main`

**Files:**
- Write: `$YEET_E2E_LOG_DIR/02-init.log`
- Write: `$YEET_E2E_LOG_DIR/03-catch-status.log`
- Write: `$YEET_E2E_ROOT/friction-log.md`

- [ ] **Step 1: Install catch from the local source checkout**

Run from the repository root:

```bash
"$YEET_E2E_YEET" --progress=plain init "$YEET_E2E_SSH_TARGET" 2>&1 | tee "$YEET_E2E_LOG_DIR/02-init.log"
```

Expected: command exits 0 or fails with a concise, actionable bootstrap error. Record every prompt or package-install offer in the friction log.

- [ ] **Step 2: Verify catch systemd service on the host**

```bash
ssh "$YEET_E2E_SSH_TARGET" 'set -u
systemctl status catch --no-pager || true
printf "\n== recent catch logs ==\n"
journalctl -u catch -n 120 --no-pager || true
printf "\n== install metadata ==\n"
cat /root/data/install.json 2>/dev/null || true
' 2>&1 | tee "$YEET_E2E_LOG_DIR/03-catch-status.log"
```

Expected: `catch.service` is active or logs explain why it is not active.

- [ ] **Step 3: Verify RPC reachability through catch host name**

```bash
"$YEET_E2E_YEET" --host="$YEET_E2E_CATCH_HOST" version 2>&1 | tee "$YEET_E2E_LOG_DIR/04-version.log"
```

Expected: output shows the local `main` commit or a clearly related dirty/local build version. If `catch` is not the reachable tsnet hostname, update `YEET_E2E_CATCH_HOST` in the shell, rerun this step, and record hostname discovery friction.

- [ ] **Step 4: Verify host-level SSH convenience command**

```bash
"$YEET_E2E_YEET" --host="$YEET_E2E_CATCH_HOST" ssh -- uname -a 2>&1 | tee "$YEET_E2E_LOG_DIR/05-yeet-ssh-host.log"
```

Expected: output is the remote kernel from the VPS. If it tries the wrong SSH user or host, record the install metadata and command output.

## Task 4: Test Docker Image Service With Published Ports

**Files:**
- Workdir: `$YEET_E2E_PROJECT/`
- Write logs under `$YEET_E2E_LOG_DIR/`
- Mutates isolated `$YEET_E2E_PROJECT/yeet.toml`

- [ ] **Step 1: Deploy an nginx image with a host published port**

```bash
cd "$YEET_E2E_PROJECT"
export YEET_E2E_IMG_SVC="$YEET_E2E_PREFIX-img"
"$YEET_E2E_YEET" --host="$YEET_E2E_CATCH_HOST" --progress=plain run -p 18080:80 "$YEET_E2E_IMG_SVC" nginx:alpine 2>&1 | tee "$YEET_E2E_LOG_DIR/10-image-run.log"
```

Expected: install succeeds. If Docker is missing, catch should offer installation or emit a clear Docker prerequisite error.

- [ ] **Step 2: Verify service inspection and published-port reachability**

```bash
"$YEET_E2E_YEET" --host="$YEET_E2E_CATCH_HOST" status "$YEET_E2E_IMG_SVC" 2>&1 | tee "$YEET_E2E_LOG_DIR/11-image-status.log"
"$YEET_E2E_YEET" --host="$YEET_E2E_CATCH_HOST" info "$YEET_E2E_IMG_SVC" 2>&1 | tee "$YEET_E2E_LOG_DIR/12-image-info.log"
curl -fsS "http://$YEET_E2E_PUBLIC_HOST:18080/" 2>&1 | tee "$YEET_E2E_LOG_DIR/13-image-curl.log"
```

Expected: status/info identify the service and curl returns the nginx welcome page. If the provider firewall blocks the port, classify reachability as host/network limitation only after remote-local curl succeeds from the VPS.

- [ ] **Step 3: Test published-port mutation guardrails**

```bash
"$YEET_E2E_YEET" --host="$YEET_E2E_CATCH_HOST" service set "$YEET_E2E_IMG_SVC" --publish-reset -p 18082:80 2>&1 | tee "$YEET_E2E_LOG_DIR/14-image-service-set-port.log"
curl -fsS "http://$YEET_E2E_PUBLIC_HOST:18082/" 2>&1 | tee "$YEET_E2E_LOG_DIR/15-image-curl-new-port.log"
```

Expected: service port changes to `18082:80`, and curl succeeds on the new port.

- [ ] **Step 4: Sync live service state into isolated config**

```bash
"$YEET_E2E_YEET" --host="$YEET_E2E_CATCH_HOST" service sync "$YEET_E2E_IMG_SVC" --config "$YEET_E2E_PROJECT/yeet.toml" 2>&1 | tee "$YEET_E2E_LOG_DIR/16-image-service-sync.log"
rg -n "$YEET_E2E_IMG_SVC|18082:80" "$YEET_E2E_PROJECT/yeet.toml" 2>&1 | tee "$YEET_E2E_LOG_DIR/17-image-config-check.log"
```

Expected: isolated `yeet.toml` contains the service and current published port.

## Task 5: Test Docker Compose Workflow

**Files:**
- Read: `$YEET_E2E_PROJECT/compose.yml`
- Write logs under `$YEET_E2E_LOG_DIR/`

- [ ] **Step 1: Deploy compose service**

```bash
cd "$YEET_E2E_PROJECT"
export YEET_E2E_COMPOSE_SVC="$YEET_E2E_PREFIX-compose"
"$YEET_E2E_YEET" --host="$YEET_E2E_CATCH_HOST" --progress=plain run "$YEET_E2E_COMPOSE_SVC" ./compose.yml 2>&1 | tee "$YEET_E2E_LOG_DIR/20-compose-run.log"
```

Expected: compose service deploys and records into isolated `yeet.toml`.

- [ ] **Step 2: Verify compose inspection, logs, and reachability**

```bash
"$YEET_E2E_YEET" --host="$YEET_E2E_CATCH_HOST" status "$YEET_E2E_COMPOSE_SVC" 2>&1 | tee "$YEET_E2E_LOG_DIR/21-compose-status.log"
"$YEET_E2E_YEET" --host="$YEET_E2E_CATCH_HOST" logs "$YEET_E2E_COMPOSE_SVC" --lines=30 2>&1 | tee "$YEET_E2E_LOG_DIR/22-compose-logs.log"
curl -fsS "http://$YEET_E2E_PUBLIC_HOST:18081/" 2>&1 | tee "$YEET_E2E_LOG_DIR/23-compose-curl.log"
```

Expected: logs stream without hanging and curl returns nginx content.

- [ ] **Step 3: Test idempotent redeploy and Docker maintenance commands**

```bash
"$YEET_E2E_YEET" --host="$YEET_E2E_CATCH_HOST" --progress=plain run "$YEET_E2E_COMPOSE_SVC" ./compose.yml 2>&1 | tee "$YEET_E2E_LOG_DIR/24-compose-redeploy.log"
"$YEET_E2E_YEET" --host="$YEET_E2E_CATCH_HOST" docker outdated "$YEET_E2E_COMPOSE_SVC" 2>&1 | tee "$YEET_E2E_LOG_DIR/25-compose-outdated.log" || true
```

Expected: redeploy is clean and `docker outdated` returns a compact update/no-update/error table. Nonzero `docker outdated` is a finding only when the error is unclear or caused by yeet.

## Task 6: Test Binary Or Script Systemd Workflow

**Files:**
- Read: `$YEET_E2E_PROJECT/hello-service.sh`
- Write logs under `$YEET_E2E_LOG_DIR/`

- [ ] **Step 1: Deploy script payload as a long-running service**

```bash
cd "$YEET_E2E_PROJECT"
export YEET_E2E_SCRIPT_SVC="$YEET_E2E_PREFIX-script"
"$YEET_E2E_YEET" --host="$YEET_E2E_CATCH_HOST" --progress=plain run "$YEET_E2E_SCRIPT_SVC" ./hello-service.sh 2>&1 | tee "$YEET_E2E_LOG_DIR/30-script-run.log"
```

Expected: catch detects the script and installs a systemd service.

- [ ] **Step 2: Verify status, logs, restart, and service info**

```bash
"$YEET_E2E_YEET" --host="$YEET_E2E_CATCH_HOST" status "$YEET_E2E_SCRIPT_SVC" 2>&1 | tee "$YEET_E2E_LOG_DIR/31-script-status.log"
"$YEET_E2E_YEET" --host="$YEET_E2E_CATCH_HOST" logs "$YEET_E2E_SCRIPT_SVC" --lines=20 2>&1 | tee "$YEET_E2E_LOG_DIR/32-script-logs.log"
"$YEET_E2E_YEET" --host="$YEET_E2E_CATCH_HOST" restart "$YEET_E2E_SCRIPT_SVC" 2>&1 | tee "$YEET_E2E_LOG_DIR/33-script-restart.log"
"$YEET_E2E_YEET" --host="$YEET_E2E_CATCH_HOST" info "$YEET_E2E_SCRIPT_SVC" 2>&1 | tee "$YEET_E2E_LOG_DIR/34-script-info.log"
```

Expected: service is running, logs contain `hello-service`, restart exits 0, and info is readable.

## Task 7: Test Cron Workflow

**Files:**
- Read: `$YEET_E2E_PROJECT/cron-job.sh`
- Write logs under `$YEET_E2E_LOG_DIR/`

- [ ] **Step 1: Install a cron job**

```bash
cd "$YEET_E2E_PROJECT"
export YEET_E2E_CRON_SVC="$YEET_E2E_PREFIX-cron"
"$YEET_E2E_YEET" --host="$YEET_E2E_CATCH_HOST" --progress=plain cron "$YEET_E2E_CRON_SVC" ./cron-job.sh "* * * * *" 2>&1 | tee "$YEET_E2E_LOG_DIR/40-cron-install.log"
```

Expected: catch installs a systemd timer and service for the cron payload.

- [ ] **Step 2: Verify cron status and timer visibility**

```bash
"$YEET_E2E_YEET" --host="$YEET_E2E_CATCH_HOST" status "$YEET_E2E_CRON_SVC" 2>&1 | tee "$YEET_E2E_LOG_DIR/41-cron-status.log"
ssh "$YEET_E2E_SSH_TARGET" "systemctl list-timers --all --no-pager | grep '$YEET_E2E_CRON_SVC' || true" 2>&1 | tee "$YEET_E2E_LOG_DIR/42-cron-timer.log"
```

Expected: yeet shows the cron service type and systemd lists a timer for it.

- [ ] **Step 3: Wait for one timer window and inspect logs**

```bash
sleep 70
"$YEET_E2E_YEET" --host="$YEET_E2E_CATCH_HOST" logs "$YEET_E2E_CRON_SVC" --lines=50 2>&1 | tee "$YEET_E2E_LOG_DIR/43-cron-logs.log"
```

Expected: logs contain at least one `cron-job` line, or timer delay behavior is clear enough to classify.

## Task 8: Test Networking, VM, Snapshot, And Image Surfaces

**Files:**
- Read logs from earlier host baseline.
- Write logs under `$YEET_E2E_LOG_DIR/`
- Write findings to `$YEET_E2E_ROOT/friction-log.md`

- [ ] **Step 1: Verify host-wide status and snapshot defaults surface**

```bash
"$YEET_E2E_YEET" --host="$YEET_E2E_CATCH_HOST" status 2>&1 | tee "$YEET_E2E_LOG_DIR/50-host-status.log"
"$YEET_E2E_YEET" --host="$YEET_E2E_CATCH_HOST" snapshots defaults show 2>&1 | tee "$YEET_E2E_LOG_DIR/51-snapshot-defaults.log"
```

Expected: both commands produce readable output. Snapshot defaults should not require ZFS merely to display policy.

- [ ] **Step 2: Try LAN networking only when the host shape makes it meaningful**

```bash
if grep -q 'default via' "$YEET_E2E_LOG_DIR/01-host-baseline.log"; then
  export YEET_E2E_LAN_SVC="$YEET_E2E_PREFIX-lan"
  "$YEET_E2E_YEET" --host="$YEET_E2E_CATCH_HOST" --progress=plain run "$YEET_E2E_LAN_SVC" nginx:alpine --net=lan 2>&1 | tee "$YEET_E2E_LOG_DIR/52-lan-run.log" || true
else
  printf 'Skipping --net=lan: baseline has no usable default route evidence.\n' | tee "$YEET_E2E_LOG_DIR/52-lan-run.log"
fi
```

Expected: on a WAN-only VPS this may fail or be skipped. Classify failure as host limitation when the message is clear and provider networking does not support LAN DHCP. Classify as yeet friction if the error is confusing, hangs, or leaves stale state.

- [ ] **Step 3: Decide whether VM tests are host-supported**

```bash
if grep -q '/dev/kvm: present' "$YEET_E2E_LOG_DIR/01-host-baseline.log"; then
  printf 'KVM present; VM workflow will run.\n' | tee "$YEET_E2E_LOG_DIR/53-vm-decision.log"
else
  printf 'KVM missing; VM workflow blocked by host capability.\n' | tee "$YEET_E2E_LOG_DIR/53-vm-decision.log"
fi
```

Expected: clear decision log.

- [ ] **Step 4: Run VM workflow only when KVM is present**

```bash
if grep -q '/dev/kvm: present' "$YEET_E2E_LOG_DIR/01-host-baseline.log"; then
  export YEET_E2E_VM_SVC="$YEET_E2E_PREFIX-vm"
  "$YEET_E2E_YEET" --host="$YEET_E2E_CATCH_HOST" --progress=plain run "$YEET_E2E_VM_SVC" vm://ubuntu/26.04 --image-policy=update --disk=8g 2>&1 | tee "$YEET_E2E_LOG_DIR/54-vm-run.log"
  "$YEET_E2E_YEET" --host="$YEET_E2E_CATCH_HOST" ssh "$YEET_E2E_VM_SVC" -- uname -a 2>&1 | tee "$YEET_E2E_LOG_DIR/55-vm-ssh.log"
  "$YEET_E2E_YEET" --host="$YEET_E2E_CATCH_HOST" stop "$YEET_E2E_VM_SVC" 2>&1 | tee "$YEET_E2E_LOG_DIR/56-vm-stop.log"
  "$YEET_E2E_YEET" --host="$YEET_E2E_CATCH_HOST" service set "$YEET_E2E_VM_SVC" --cpus=2 --memory=2g --disk=10g 2>&1 | tee "$YEET_E2E_LOG_DIR/57-vm-resize.log"
  "$YEET_E2E_YEET" --host="$YEET_E2E_CATCH_HOST" start "$YEET_E2E_VM_SVC" 2>&1 | tee "$YEET_E2E_LOG_DIR/58-vm-start.log"
else
  printf 'VM create skipped because /dev/kvm is not available on this VPS.\n' | tee "$YEET_E2E_LOG_DIR/54-vm-run.log"
fi
```

Expected when KVM is present: VM creates, SSH readiness works, stopped resize works, and restart works. Expected when KVM is absent: skip is recorded as host limitation.

- [ ] **Step 5: Inspect VM image cache commands**

```bash
"$YEET_E2E_YEET" --host="$YEET_E2E_CATCH_HOST" vm images 2>&1 | tee "$YEET_E2E_LOG_DIR/59-vm-images.log" || true
"$YEET_E2E_YEET" --host="$YEET_E2E_CATCH_HOST" vm images prune --dry-run 2>&1 | tee "$YEET_E2E_LOG_DIR/60-vm-images-prune-dry-run.log" || true
```

Expected: commands render clear cache state. On non-KVM hosts, image commands should still be readable if catch is installed.

## Task 9: Cleanup And Evidence Collection

**Files:**
- Write logs under `$YEET_E2E_LOG_DIR/`
- Write: `$YEET_E2E_ROOT/report.md`

- [ ] **Step 1: Remove every service created by the pass**

```bash
cd "$YEET_E2E_PROJECT"
for svc in \
  "${YEET_E2E_IMG_SVC:-}" \
  "${YEET_E2E_COMPOSE_SVC:-}" \
  "${YEET_E2E_SCRIPT_SVC:-}" \
  "${YEET_E2E_CRON_SVC:-}" \
  "${YEET_E2E_LAN_SVC:-}" \
  "${YEET_E2E_VM_SVC:-}"; do
  if [ -n "$svc" ]; then
    "$YEET_E2E_YEET" --host="$YEET_E2E_CATCH_HOST" rm "$svc" --clean-data --yes 2>&1 | tee "$YEET_E2E_LOG_DIR/cleanup-$svc.log" || true
  fi
done
```

Expected: removals exit 0 or emit clear already-gone messages. Any dataset busy, unit left behind, or config cleanup failure is a cleanup issue finding.

- [ ] **Step 2: Verify remote cleanup**

```bash
ssh "$YEET_E2E_SSH_TARGET" "set -u
printf '== matching systemd units ==\n'
systemctl list-units --all --no-legend | grep '$YEET_E2E_PREFIX' || true
printf '== matching systemd unit files ==\n'
find /etc/systemd/system -maxdepth 1 -name '*$YEET_E2E_PREFIX*' -print || true
printf '== matching containers ==\n'
docker ps -a --format '{{.Names}}' 2>/dev/null | grep '$YEET_E2E_PREFIX' || true
printf '== matching service roots ==\n'
find /root/data/services -maxdepth 2 -name '*$YEET_E2E_PREFIX*' -print 2>/dev/null || true
" 2>&1 | tee "$YEET_E2E_LOG_DIR/90-remote-cleanup-check.log"
```

Expected: no matching services, unit files, containers, or service roots remain.

- [ ] **Step 3: Verify isolated local config cleanup**

```bash
if [ -f "$YEET_E2E_PROJECT/yeet.toml" ]; then
  rg -n "$YEET_E2E_PREFIX" "$YEET_E2E_PROJECT/yeet.toml" 2>&1 | tee "$YEET_E2E_LOG_DIR/91-local-config-cleanup-check.log" || true
else
  printf 'No isolated yeet.toml present.\n' | tee "$YEET_E2E_LOG_DIR/91-local-config-cleanup-check.log"
fi
```

Expected: no matching entries remain after `yeet rm --clean-data --yes`.

- [ ] **Step 4: Capture final catch state**

```bash
ssh "$YEET_E2E_SSH_TARGET" 'set -u
systemctl status catch --no-pager || true
journalctl -u catch -n 200 --no-pager || true
' 2>&1 | tee "$YEET_E2E_LOG_DIR/92-final-catch-status.log"
```

Expected: catch remains healthy after all tests and cleanup.

- [ ] **Step 5: Write final report skeleton with command-derived evidence**

```bash
{
  printf '# Fresh VPS E2E Report\n\n'
  printf 'Run root: `%s`\n\n' "$YEET_E2E_ROOT"
  printf '## Pass Matrix\n\n'
  printf '| Area | Evidence log | Result |\n'
  printf '| --- | --- | --- |\n'
  printf '| SSH baseline | `00-ssh-first-contact.log`, `01-host-baseline.log` | not evaluated yet |\n'
  printf '| catch init | `02-init.log`, `03-catch-status.log`, `04-version.log` | not evaluated yet |\n'
  printf '| Docker image | `10-image-run.log` through `17-image-config-check.log` | not evaluated yet |\n'
  printf '| Compose | `20-compose-run.log` through `25-compose-outdated.log` | not evaluated yet |\n'
  printf '| Script service | `30-script-run.log` through `34-script-info.log` | not evaluated yet |\n'
  printf '| Cron | `40-cron-install.log` through `43-cron-logs.log` | not evaluated yet |\n'
  printf '| Networking | `50-host-status.log` through `52-lan-run.log` | not evaluated yet |\n'
  printf '| VM and images | `53-vm-decision.log` through `60-vm-images-prune-dry-run.log` | not evaluated yet |\n'
  printf '| Cleanup | `90-remote-cleanup-check.log`, `91-local-config-cleanup-check.log`, `92-final-catch-status.log` | not evaluated yet |\n'
  printf '\n## Friction Findings\n\n'
  sed -n '/## Findings/,$p' "$YEET_E2E_ROOT/friction-log.md"
} > "$YEET_E2E_ROOT/report.md"
```

Expected: report exists and points to the evidence logs. Update `not evaluated yet` values during execution before presenting the final report.

## Task 10: Decide Follow-Up Fix Path

**Files:**
- Read: `$YEET_E2E_ROOT/report.md`
- Read: `$YEET_E2E_ROOT/friction-log.md`
- Potential follow-up files depend on findings and are not part of this plan.

- [ ] **Step 1: Classify every finding**

Use the friction-log classifications:

- `yeet bug`: code behavior is wrong or cleanup incomplete.
- `docs bug`: docs or examples caused confusion.
- `main regression`: behavior likely works in release but fails on `main`.
- `host limitation`: VPS lacks KVM, LAN DHCP, ZFS, firewall access, or a provider feature.
- `external dependency`: package repo, Docker registry, or Tailscale service problem.
- `unknown`: needs debugging before assigning owner.

Expected: no finding remains unclassified.

- [ ] **Step 2: Recommend next action before patch release**

Use this decision table:

- If any blocker or main regression exists, stop and create a fix plan before release.
- If only doc gaps exist, update website docs before release.
- If only host limitations exist, include them in the report and do not block release.
- If cleanup issues exist, prioritize them as release blockers unless they are confirmed host-only.

Expected: final response gives a concise release-readiness recommendation with evidence.
