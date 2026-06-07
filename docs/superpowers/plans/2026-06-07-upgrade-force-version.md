# Upgrade Force And Version Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `yeet upgrade --force` and `yeet upgrade --version <tag>` so users can reinstall or switch yeet/catch to a selected public release.

**Architecture:** Keep `upgrade check` as the planner and `upgrade` as the executor. Generalize GitHub release resolution so both latest and explicit tag releases use the same asset and checksum paths. Treat force as an actionable reinstall status for local yeet and remote catch rows, while preserving non-forced behavior for current, newer, and dev builds.

**Tech Stack:** Go CLI parser, GitHub release API, existing tarball/checksum installer, website Markdown docs, repo quality hooks.

---

## File Structure

- `pkg/cli/cli.go`: add `Force` and `Version` to upgrade flags and parser metadata.
- `pkg/cli/cli_test.go`: cover parsing `--force` and `--version`.
- `pkg/yeet/init_release.go`: add a release-by-tag URL helper and fetch function.
- `pkg/yeet/init_download.go`: let yeet/catch asset resolution target either latest/nightly or a specific release tag.
- `pkg/yeet/init.go`: carry an internal `releaseVersion` through `initCatch` when upgrade reinstalls catch from a selected tag.
- `pkg/yeet/upgrade_check.go`: add force/selected-version planning and statuses.
- `pkg/yeet/upgrade_cmd.go`: render `TARGET` when appropriate, include force reinstalls in confirmation, and execute forced local/catch installs.
- `pkg/yeet/upgrade_install.go`: let local upgrade replace with the target release when forced.
- Tests under `pkg/yeet/*_test.go`: cover explicit version, force reinstall, and non-forced no-downgrade behavior.
- `cmd/yeet/cli.go`, `website/docs/cli/yeet-cli.mdx`, `website/docs/getting-started/installation.mdx`, `README.md`, `.codex/skills/yeet-cli/references/yeet-help-llm.md`: document the new flags.

## Tasks

### Task 1: Parser And Help Surface

- [ ] Add `Force bool` and `Version string` to `UpgradeFlags` and `upgradeFlagsParsed`.
- [ ] Update `ParseUpgrade` to populate those fields.
- [ ] Add parser test:

```go
{
	name: "force specific version",
	args: []string{"--all", "--force", "--version", "v0.6.1"},
	want: UpgradeFlags{All: true, Force: true, Version: "v0.6.1"},
}
```

- [ ] Update top-level upgrade help examples to include `yeet upgrade --all --force` and `yeet upgrade --all --version v0.6.1 --force`.
- [ ] Run `mise exec -- go test ./pkg/cli ./cmd/yeet -count=1`.

### Task 2: Release Resolution By Tag

- [ ] Add `githubReleaseTagURL(tag string)` returning `/repos/yeetrun/yeet/releases/tags/<tag>`.
- [ ] Add `fetchGitHubReleaseForVersion(nightly bool, version string)`:

```go
if strings.TrimSpace(version) != "" {
	return fetchGitHubReleaseFromURL(githubReleaseTagURL(version), &http.Client{Timeout: 30 * time.Second})
}
return fetchGitHubRelease(nightly)
```

- [ ] Update `resolveCatchReleaseAsset` and `resolveYeetReleaseAsset` to accept a `version string` and call the new helper.
- [ ] Update existing callers with `""` except version-aware upgrade paths.
- [ ] Add tests for explicit tag URL and explicit-version asset resolution.
- [ ] Run `mise exec -- go test ./pkg/yeet -run 'Test.*Release|Test.*Asset|Test.*Init' -count=1`.

### Task 3: Upgrade Planning Semantics

- [ ] Extend `upgradeCheckRequest` and `upgradeReport` with `Force bool` and `TargetVersion string`.
- [ ] Fetch `TargetVersion` directly when set; otherwise fetch latest stable/nightly as today.
- [ ] Add statuses:

```go
upgradeStatusReinstall upgradeStatus = "reinstall release"
upgradeStatusAhead     upgradeStatus = "newer than target"
```

- [ ] Classification rules:
  - If target is unknown, status remains `unknown`.
  - If `Force` is true, reachable rows become `reinstall release`.
  - Without force, dev/source rows stay `dev build`.
  - Without force, older release rows become `update available`.
  - Without force, equal release rows become `current`.
  - Without force, newer release rows become `newer than target`.
- [ ] Add tests for forced dev rows, forced current release rows, explicit older target without force, and explicit upgrade target.
- [ ] Run `mise exec -- go test ./pkg/yeet -run 'TestBuildUpgradeReport|TestFetchUpgrade|TestRenderUpgrade|TestConfirmUpgrade' -count=1`.

### Task 4: Upgrade Execution

- [ ] Treat `update available` and `reinstall release` as actionable rows.
- [ ] Render `TARGET` instead of `LATEST` when force or explicit version is active.
- [ ] Confirmation plan includes `(reinstall release)` for forced rows.
- [ ] Change `upgradeLocalBinary` and `localUpgradePlan` to accept `force bool`; force skips release-version comparison and allows dev/source builds to be replaced.
- [ ] Change catch reinstall to call `initCatchFn(target, initOptions{fromGithub: true, releaseVersion: report.Latest.Tag})`.
- [ ] Add tests that forced catch rows call init with `fromGithub=true` and `releaseVersion` set.
- [ ] Add tests that forced local rows call replacement even when current equals target or current is dev.
- [ ] Run `mise exec -- go test ./pkg/yeet -run 'TestRunUpgrade|TestLocalUpgrade|TestConfirmUpgrade|TestCatchInstallTarget' -count=1`.

### Task 5: Docs And Generated Help

- [ ] Update README and website upgrade sections with:

```bash
yeet upgrade --all --force
yeet upgrade --all --version v0.6.1 --force
```

- [ ] Explain that `--version` selects a public release and `--force` reinstalls the selected release even over equal, newer, or dev builds.
- [ ] Run `tools/generate-yeet-help-llm.sh`.
- [ ] Run docs checks:

```bash
git -C website diff --check
rg -n "private[-]host|/User[s]/" README.md website/docs .codex/skills
```

### Task 6: Final Verification

- [ ] Run targeted tests:

```bash
mise exec -- go test ./pkg/cli ./cmd/yeet ./pkg/yeet -count=1
```

- [ ] Run full tests:

```bash
mise exec -- go test ./... -count=1
```

- [ ] Run pre-commit:

```bash
mise exec -- pre-commit run --all-files
```

- [ ] Report that changes are implemented but uncommitted unless the user authorizes commit/push.

## Self-Review

- Spec coverage: force latest, explicit version selection, local yeet, remote catch, check output, confirmation, docs, and tests all have tasks.
- Placeholder scan: no TBD/TODO placeholders.
- Type consistency: `Force`, `Version`, `TargetVersion`, `releaseVersion`, and `upgradeStatusReinstall` are consistently named.
