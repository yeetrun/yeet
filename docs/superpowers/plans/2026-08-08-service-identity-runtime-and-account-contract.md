# Service Identity Runtime and Account Contract Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make identity-only native service migrations restage their persisted generation, migrate the four selected live services to `yeet-svc`, and give `yeet-svc` and `yeet-vm` one explicit `/nonexistent` account-home contract.

**Architecture:** Build the same complete generation-staging request for identity-only `service set` that existing redeploy and network-plus-identity paths already use, while leaving copied-root staging in the transaction engine. Normalize legacy flat runtime references at the shared systemd artifact install boundary so current migration and future rollback use the immutable binary and managed environment layout. Add one small shared static-account policy for the common home and nologin values; keep the existing service- and VM-specific validation code.

**Tech Stack:** Go, systemd, Linux NSS/useradd/usermod, GitButler, Yeet/Catch live deployment.

## Global Constraints

- Keep `service set` permission mapping at `manage`; add no new CLI or RPC surface.
- Preserve the existing transactional rollback and root-copy behavior.
- Both `yeet-svc` and `yeet-vm` must use `/nonexistent`, `--no-create-home`, and `/usr/sbin/nologin`.
- Migrate `rssbot` first and stop the live rollout on the first failure.
- Leave cron payloads and unrelated services unchanged.
- Use `mise exec -- go ...` and GitButler for repository work.

---

### Task 1: Restage identity-only service generations

**Files:**
- Modify: `pkg/svc/systemd_test.go`
- Modify: `pkg/svc/systemd.go`
- Modify: `pkg/catch/tty_service_set_test.go`
- Modify: `pkg/catch/tty_service_set.go`

**Interfaces:**
- Produces: `(*svc.SystemdService).RenderedPrimaryUnit() (string, error)` using the same transformation as normal systemd artifact installation.
- Consumes: `Server.NewInstaller(InstallerCfg)`, `newSystemdInstallService`, `serviceIdentityInstallTargetStates`, and `stagedNativeIdentityGeneration`.
- Produces: `(*ttyExecer).prepareServiceSetIdentityMigrationRequest(cli.ServiceSetFlags, serviceSetChanges, resolvedServiceIdentity, *serviceRootMigrationPlan) (serviceIdentityMigrationRequest, error)`.

- [ ] **Step 1: Write the failing runtime-regeneration test**

  Add a `pkg/svc` test proving systemd installation rewrites a generated legacy unit from `run/api` and `run/env` to the current immutable binary and managed `env/env`, while preserving arguments and applying the requested identity. Extend `TestServiceSetRunAsRoutesNativeToMigrationEngine` with the same persisted generation. In the injected migration boundary, remove legacy `run/api` and `run/env`, require `StageGeneration`, invoke it, and assert the managed environment is recreated, the replacement unit points at the immutable binary and managed environment, and no legacy runtime executable remains. Also assert `TargetService`, `GenerationPaths`, `GenerationIntents`, and `GenerationUnits` are populated.

- [ ] **Step 2: Prove the regression test is red**

  Run:

  ```bash
  mise exec -- go test ./pkg/catch -run '^TestServiceSetRunAsRoutesNativeToMigrationEngine$' -count=1
  ```

  Expected: failure because the current request has no `StageGeneration` callback or rendered primary unit.

- [ ] **Step 3: Build a complete identity-only migration request**

  Add the shared systemd rendering method and use it for ordinary artifact installation and install-intent hashing. It must replace only known Yeet legacy runtime paths for artifacts present in the current generation. Add a helper used by `applyServiceSetIdentityChange`. When `rootPlan == nil`, it must load and clone the persisted native service, copy the resolved identity into the clone, construct a generation installer, render the replacement primary unit, capture non-primary install states, and return:

  ```go
  serviceIdentityMigrationRequest{
      Service: e.sn, Requested: flags.RunAs, Target: target,
      ReplacementUnit: replacement,
      TargetService: service,
      GenerationPaths: generationService.InstallTargetPaths(),
      GenerationIntents: serviceIdentityInstallTargetStates(states),
      GenerationUnits: units,
      StageGeneration: stagedNativeIdentityGeneration(generationService, units),
  }
  ```

  When `rootPlan != nil`, return the existing minimal request so the migration engine retains ownership of copied-root generation setup.

- [ ] **Step 4: Prove the fix is green**

  Run the focused test, then:

  ```bash
  mise exec -- go test ./pkg/catch
  ```

- [ ] **Step 5: Run the implementation gate and checkpoint**

  Run:

  ```bash
  mise exec -- go test ./...
  pre-commit run --all-files
  mise run quality:goal
  but pull --check
  ```

  Commit only the identity migration fix and its tests to `codex/service-identity-accounts` with `but commit`.

### Task 2: Deploy and migrate the selected native services

**Files:**
- Modify through Yeet only: `/Users/shayne/yeet-services/yeet.toml`

**Interfaces:**
- Consumes: patched `cmd/catch`, installed Yeet v0.10.13 client, Catch alias `yeet-pve1`, machine host `root@pve1`.
- Produces: four healthy native services with persisted `run_as = "yeet-svc"`.

- [ ] **Step 1: Install and identify the patched Catch build**

  Run:

  ```bash
  CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet init root@pve1
  CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet version
  ```

  Verify the remote executable build metadata and that Catch is healthy.

- [ ] **Step 2: Retry `rssbot`**

  From `/Users/shayne/yeet-services`, run:

  ```bash
  yeet --host=yeet-pve1 service set rssbot --run-as=yeet-svc
  ```

  Verify service info, systemd `User`/`Group`, live process UID/GID, stable runtime artifacts, root/bin/env/data/run ownership, recent logs, and `run_as` in `yeet.toml`. Restart once and recheck health.

- [ ] **Step 3: Migrate the remaining services one at a time**

  Repeat the command and the same verification for:

  ```text
  lemonsqueezy-bot
  imap-back-to-google-watch
  tsidp
  ```

  Stop immediately on a failure and verify rollback before any further service.

### Task 3: Unify the static account home contract

**Files:**
- Create: `pkg/catch/system_account.go`
- Modify: `pkg/catch/service_account.go`
- Modify: `pkg/catch/service_account_test.go`
- Modify: `pkg/catch/vm_jailer_readiness.go`
- Modify: `pkg/catch/vm_jailer_readiness_test.go`

**Interfaces:**
- Produces: shared constants `staticSystemAccountHome` and `staticSystemAccountShell`, plus a small `staticSystemUserAddArgs(name string, groupArgs ...string) []string` helper.
- Consumes: the existing service and VM lookup/command seams and their specialized validators.

- [ ] **Step 1: Write failing account contract tests**

  Update the VM creation expectation to require:

  ```text
  --system --gid yeet-vm --home-dir /nonexistent --no-create-home --shell /usr/sbin/nologin yeet-vm
  ```

  Add a VM validator case whose passwd record uses `/home/yeet-vm` and require an unsafe-account error mentioning `/nonexistent`. Keep the service account creation expectation on the same shared values.

- [ ] **Step 2: Prove the account tests are red**

  Run:

  ```bash
  mise exec -- go test ./pkg/catch -run '^(TestVMRuntimeIdentityCreatesMissingAccountOnce|TestVMRuntimeIdentityRejectsWrongHome|TestManagedServiceAccountCreatesAndRelooksUp)$' -count=1
  ```

- [ ] **Step 3: Add the minimal shared policy**

  Define the shared home and shell constants and one useradd argument builder. Use it from both provisioning paths while preserving `yeet-svc`'s explicit pre-created group and `yeet-vm`'s `--user-group` behavior. Add `Home string` to `vmRuntimePasswdRecord`, parse passwd field 5, and require exact equality with `staticSystemAccountHome`. Do not merge the larger validation implementations.

- [ ] **Step 4: Prove account behavior is green**

  Run the focused tests, `mise exec -- go test ./pkg/catch`, `mise exec -- go test ./...`, `pre-commit run --all-files`, and `mise run quality:goal`.

- [ ] **Step 5: Checkpoint the account change**

  Run `but pull --check` and commit the shared account contract and tests to the existing GitButler branch.

### Task 4: Correct the host and verify the final state

**Files:**
- No repository file mutations.

**Interfaces:**
- Consumes: `root@pve1`, `getent`, `id`, `ps`, `usermod`, and the patched Catch build.
- Produces: verified `/nonexistent` passwd homes for both Yeet-owned accounts without UID/GID or workload regressions.

- [ ] **Step 1: Capture the `yeet-vm` mutation preconditions**

  Record its passwd entry, UID/GID, group memberships, old-home absence, `/nonexistent` absence, and current `yeet-vm` processes. Abort if `/home/yeet-vm` contains data or `/nonexistent` exists.

- [ ] **Step 2: Change only the passwd home field**

  Run:

  ```bash
  ssh root@pve1 usermod --home /nonexistent yeet-vm
  ```

  Do not use `--move-home` and do not change UID, GID, shell, groups, or ownership.

- [ ] **Step 3: Deploy the account-validation build**

  Reinstall Catch on `yeet-pve1`, verify its build metadata and health, and exercise VM identity readiness through a read-only or idempotent existing path.

- [ ] **Step 4: Final cross-check**

  Verify both passwd entries use `/nonexistent`; all four selected native services are active as `yeet-svc`; ownership and stable runtime files remain correct; their `yeet.toml` entries persist `run_as = "yeet-svc"`; and any preexisting `yeet-vm` processes/VMs remain healthy.

- [ ] **Step 5: Report repository and publication truth**

  Report local branch commits, uncommitted state, live Catch build, host mutations, service results, and that nothing was pushed or landed on `origin/main` unless separately authorized.
