# Upgrade Nightly Flag Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `yeet upgrade --nightly` so stable, nightly, and forced upgrade flows can explicitly target the moving GitHub nightly release.

**Architecture:** Parse `--nightly` as an upgrade target-channel override, carry that target through report building, and make install paths choose nightly release assets independently from the currently installed local binary channel. Keep the existing stable and pinned-version paths unchanged, and route catch nightly installs through the existing `yeet init --from-github --nightly --no-workspace` machinery with next-step output suppressed.

**Tech Stack:** Go CLI/parser code, existing GitHub release asset resolver, existing init/catch installer path, Markdown docs, GitButler for root repo commits.

## Global Constraints

- `yeet upgrade --nightly`, `yeet upgrade check --nightly`, `yeet upgrade --nightly --force`, and `yeet upgrade --host=<catch-host> --nightly` must all be accepted.
- `--nightly` conflicts with `--version`; return a parser error before building an upgrade report.
- When `--nightly` is set, fetch the GitHub `nightly` release even when the local binary is a stable release.
- When catch is upgraded with `--nightly`, use init options equivalent to `yeet init --from-github --nightly --no-workspace`, and keep `suppressNextSteps` true.
- Do not semver-compare against the moving `nightly` tag; compare current version text to the target tag and mark different release builds as updateable.
- Current versions that are empty or `unknown` remain non-actionable unless `--force` is set.
- Keep output generic and free of private hostnames, local paths, or private service names.
- Public docs use `yeetrun.com`, not `yeet.run`.
- Do not update `website/docs/changelog.mdx` for this feature until a tagged release is being prepared.
- Use `mise exec -- go ...` for Go commands and do not set or manage `GOCACHE`.
- Use GitButler for root repo version-control writes; commit and push website submodule changes inside `website/` before committing the root gitlink.

---

## File Structure

- Modify `pkg/cli/cli.go`: add `UpgradeFlags.Nightly`, parse `--nightly`, and reject `--nightly` with `--version`.
- Modify `pkg/cli/cli_test.go`: cover accepted nightly forms and the conflict with `--version`.
- Modify `cmd/yeet/cli.go`: expose `--nightly` in upgrade usage/examples.
- Modify `cmd/yeet/cli_test.go`: verify upgrade help mentions nightly.
- Modify `pkg/yeet/update_cache.go`: mark cached release entries with the target channel, so install code can distinguish stable target from nightly target.
- Modify `pkg/yeet/upgrade_check.go`: propagate nightly target selection, fetch nightly releases from stable installs, and classify nightly targets without semver.
- Modify `pkg/yeet/upgrade_check_test.go`: cover nightly target fetching and classification.
- Modify `pkg/yeet/upgrade_cmd.go`: render `TARGET` for nightly reports, pass nightly target state into local and catch install paths, and avoid passing `releaseVersion` for nightly catch init.
- Modify `pkg/yeet/upgrade_cmd_test.go`: cover report rendering and catch init options for nightly.
- Modify `pkg/yeet/upgrade_install.go`: make local yeet self-upgrade resolve nightly assets when the target report is nightly, even if the current local binary is stable.
- Modify `pkg/yeet/upgrade_install_test.go`: cover stable local binary upgrading from nightly assets.
- Modify `README.md`: document nightly upgrade usage.
- Modify `website/docs/getting-started/host-setup.mdx`: document nightly upgrade usage in the host setup guide.
- Modify `website/docs/cli/yeet-cli.mdx`: document the CLI example for `--nightly`.
- Regenerate `.codex/skills/yeet-cli/references/yeet-help-agent.md`: keep agent-facing help synchronized with CLI metadata.

---

### Task 1: Parse and Advertise `upgrade --nightly`

**Files:**
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`
- Modify: `cmd/yeet/cli.go`
- Modify: `cmd/yeet/cli_test.go`

**Interfaces:**
- Produces: `cli.UpgradeFlags.Nightly bool`
- Produces: `upgradeFlagsParsed.Nightly bool` with `flag:"nightly"`
- Produces: `ParseUpgrade(args []string) (UpgradeFlags, []string, error)` error text containing `--nightly` and `--version` when both are set.

- [ ] **Step 1: Write failing parser tests**

Replace `TestParseUpgrade` in `pkg/cli/cli_test.go` with this version and add `TestParseUpgradeRejectsNightlyWithVersion` immediately after `TestParseUpgradeRejectsAllFlag`:

```go
func TestParseUpgrade(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    UpgradeFlags
		wantPos []string
	}{
		{name: "check json", args: []string{"check", "--json"}, want: UpgradeFlags{JSON: true}, wantPos: []string{"check"}},
		{name: "host yes", args: []string{"--host", "edge-a", "--yes"}, want: UpgradeFlags{Host: "edge-a", Yes: true}},
		{name: "check flag alias", args: []string{"--check"}, want: UpgradeFlags{Check: true}},
		{name: "force specific version", args: []string{"--force", "--version", "v0.6.1"}, want: UpgradeFlags{Force: true, Version: "v0.6.1"}},
		{name: "nightly", args: []string{"--nightly"}, want: UpgradeFlags{Nightly: true}},
		{name: "nightly check", args: []string{"check", "--nightly", "--json"}, want: UpgradeFlags{Nightly: true, JSON: true}, wantPos: []string{"check"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, pos, err := ParseUpgrade(tt.args)
			if err != nil {
				t.Fatalf("ParseUpgrade error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("flags = %#v, want %#v", got, tt.want)
			}
			if strings.Join(pos, ",") != strings.Join(tt.wantPos, ",") {
				t.Fatalf("pos = %#v, want %#v", pos, tt.wantPos)
			}
		})
	}
}

func TestParseUpgradeRejectsNightlyWithVersion(t *testing.T) {
	_, _, err := ParseUpgrade([]string{"--nightly", "--version", "v0.6.1"})
	if err == nil || !strings.Contains(err.Error(), "--nightly") || !strings.Contains(err.Error(), "--version") {
		t.Fatalf("ParseUpgrade nightly/version error = %v, want conflict", err)
	}
}
```

- [ ] **Step 2: Write failing help test**

Add this test to `cmd/yeet/cli_test.go` near the other upgrade help tests:

```go
func TestRunUpgradeHelpShowsNightly(t *testing.T) {
	oldArgs := os.Args
	oldHandleSvcCmdFn := handleSvcCmdFn
	oldStdout := os.Stdout
	oldBridgedArgs := bridgedArgs
	oldRawArgs := rawArgs
	t.Cleanup(func() {
		os.Args = oldArgs
		handleSvcCmdFn = oldHandleSvcCmdFn
		os.Stdout = oldStdout
		bridgedArgs = oldBridgedArgs
		rawArgs = oldRawArgs
	})

	stdoutFile, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatalf("create stdout temp file: %v", err)
	}
	os.Stdout = stdoutFile
	os.Args = []string{"yeet", "upgrade", "--help"}
	handleSvcCmdFn = func(args []string) error {
		t.Fatalf("upgrade help should not call service handler with args %v", args)
		return nil
	}

	if got := run(); got != 0 {
		t.Fatalf("run exit code = %d, want 0", got)
	}
	if _, err := stdoutFile.Seek(0, 0); err != nil {
		t.Fatalf("seek stdout: %v", err)
	}
	rawStdout, err := os.ReadFile(stdoutFile.Name())
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	stdout := string(rawStdout)
	for _, want := range []string{
		"[--nightly]",
		"yeet upgrade --nightly",
		"yeet upgrade check --nightly",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
}
```

- [ ] **Step 3: Run tests and verify they fail**

Run:

```bash
mise exec -- go test ./pkg/cli ./cmd/yeet -run 'TestParseUpgrade|TestRunUpgradeHelpShowsNightly' -count=1
```

Expected: FAIL because `UpgradeFlags.Nightly` is undefined and the upgrade help metadata does not mention `--nightly`.

- [ ] **Step 4: Implement parser fields and conflict validation**

In `pkg/cli/cli.go`, update the upgrade flag structs and `ParseUpgrade`:

```go
type UpgradeFlags struct {
	Host    string
	JSON    bool
	Yes     bool
	Check   bool
	Force   bool
	Nightly bool
	Version string
}

type upgradeFlagsParsed struct {
	Host    string `flag:"host"`
	JSON    bool   `flag:"json"`
	Yes     bool   `flag:"yes"`
	Check   bool   `flag:"check"`
	Force   bool   `flag:"force"`
	Nightly bool   `flag:"nightly"`
	Version string `flag:"version"`
}
```

```go
func ParseUpgrade(args []string) (UpgradeFlags, []string, error) {
	parseArgs, extraArgs := splitArgsAtDoubleDash(args)
	parsed, err := parseFlags[upgradeFlagsParsed](parseArgs)
	if err != nil {
		return UpgradeFlags{}, nil, err
	}
	flags := UpgradeFlags{
		Host:    parsed.Flags.Host,
		JSON:    parsed.Flags.JSON,
		Yes:     parsed.Flags.Yes,
		Check:   parsed.Flags.Check,
		Force:   parsed.Flags.Force,
		Nightly: parsed.Flags.Nightly,
		Version: parsed.Flags.Version,
	}
	if flags.Nightly && strings.TrimSpace(flags.Version) != "" {
		return UpgradeFlags{}, nil, fmt.Errorf("--nightly cannot be used with --version")
	}
	argsOut := append(parsed.Args, extraArgs...)
	return flags, argsOut, nil
}
```

- [ ] **Step 5: Implement command help metadata**

In `cmd/yeet/cli.go`, update the `upgrade` command metadata:

```go
subcommands["upgrade"] = yargs.SubCommandInfo{
	Name:        "upgrade",
	Description: "Check for and install yeet/catch updates",
	Usage:       "[check] [--host=catch-a] [--json] [--yes] [--force] [--nightly] [--version=vX.Y.Z]",
	Examples: []string{
		"yeet upgrade check",
		"yeet upgrade",
		"yeet upgrade --host=catch-a",
		"yeet upgrade --force",
		"yeet upgrade --nightly",
		"yeet upgrade check --nightly",
		"yeet upgrade --version v0.6.1 --force",
	},
}
```

- [ ] **Step 6: Run task tests and verify they pass**

Run:

```bash
mise exec -- go test ./pkg/cli ./cmd/yeet -run 'TestParseUpgrade|TestRunUpgradeHelpShowsNightly' -count=1
```

Expected: PASS for `./pkg/cli` and `./cmd/yeet`.

- [ ] **Step 7: Commit parser/help changes**

Run:

```bash
but diff
but commit codex/upgrade-nightly-flag-design -m "cli: parse upgrade nightly flag"
```

Expected: GitButler creates a commit on `codex/upgrade-nightly-flag-design` containing only `pkg/cli/cli.go`, `pkg/cli/cli_test.go`, `cmd/yeet/cli.go`, and `cmd/yeet/cli_test.go`.

---

### Task 2: Build Nightly Upgrade Reports

**Files:**
- Modify: `pkg/yeet/update_cache.go`
- Modify: `pkg/yeet/upgrade_check.go`
- Modify: `pkg/yeet/upgrade_check_test.go`

**Interfaces:**
- Consumes: `cli.UpgradeFlags.Nightly bool` from Task 1 through the caller in Task 3.
- Produces: `upgradeCheckRequest.Nightly bool`
- Produces: `upgradeReport.Nightly bool`
- Produces: `releaseCacheEntry.Nightly bool`
- Produces: `upgradeTargetChannel(local buildinfo.Info, nightly bool) buildinfo.Channel`
- Produces: `classifyUpgradeVersion(current string, latest releaseCacheEntry) upgradeStatus`

- [ ] **Step 1: Write failing report tests**

Add these tests to `pkg/yeet/upgrade_check_test.go`:

```go
func TestBuildUpgradeReportNightlyFetchesNightlyFromStable(t *testing.T) {
	oldLatest := fetchUpgradeLatestFn
	oldInfo := fetchUpgradeCatchInfoFn
	t.Cleanup(func() {
		fetchUpgradeLatestFn = oldLatest
		fetchUpgradeCatchInfoFn = oldInfo
	})
	var gotChannel buildinfo.Channel
	fetchUpgradeLatestFn = func(_ context.Context, channel buildinfo.Channel, _ time.Time) (releaseCacheEntry, error) {
		gotChannel = channel
		return releaseCacheEntry{Tag: "nightly", Nightly: true}, nil
	}
	fetchUpgradeCatchInfoFn = func(ctx context.Context, host string) (serverInfo, error) {
		return serverInfo{Version: "v0.9.5", InstallUser: "root", InstallHost: host}, nil
	}

	report := buildUpgradeReport(context.Background(), upgradeCheckRequest{
		Local:   buildinfo.Info{Version: "v0.9.5", Channel: buildinfo.ChannelStable},
		Hosts:   []string{"edge"},
		Now:     time.Unix(100, 0),
		Nightly: true,
	})
	if gotChannel != buildinfo.ChannelNightly {
		t.Fatalf("channel = %q, want nightly", gotChannel)
	}
	if !report.Nightly || !report.Latest.Nightly || report.Latest.Tag != "nightly" {
		t.Fatalf("report = %#v, want nightly target", report)
	}
	if report.Local.Status != upgradeStatusUpdateAvailable {
		t.Fatalf("local status = %q, want update available", report.Local.Status)
	}
	if report.Catch[0].Status != upgradeStatusUpdateAvailable {
		t.Fatalf("catch row = %#v, want update available", report.Catch[0])
	}
}

func TestBuildUpgradeReportNightlyCurrentWhenVersionMatchesTag(t *testing.T) {
	oldLatest := fetchUpgradeLatestFn
	oldInfo := fetchUpgradeCatchInfoFn
	t.Cleanup(func() {
		fetchUpgradeLatestFn = oldLatest
		fetchUpgradeCatchInfoFn = oldInfo
	})
	fetchUpgradeLatestFn = func(_ context.Context, channel buildinfo.Channel, _ time.Time) (releaseCacheEntry, error) {
		if channel != buildinfo.ChannelNightly {
			t.Fatalf("channel = %q, want nightly", channel)
		}
		return releaseCacheEntry{Tag: "nightly", Nightly: true}, nil
	}
	fetchUpgradeCatchInfoFn = func(ctx context.Context, host string) (serverInfo, error) {
		return serverInfo{Version: "nightly", InstallUser: "root", InstallHost: host}, nil
	}

	report := buildUpgradeReport(context.Background(), upgradeCheckRequest{
		Local:   buildinfo.Info{Version: "nightly", Channel: buildinfo.ChannelNightly},
		Hosts:   []string{"edge"},
		Now:     time.Unix(100, 0),
		Nightly: true,
	})
	if report.Local.Status != upgradeStatusCurrent {
		t.Fatalf("local status = %q, want current", report.Local.Status)
	}
	if report.Catch[0].Status != upgradeStatusCurrent {
		t.Fatalf("catch row = %#v, want current", report.Catch[0])
	}
}

func TestBuildUpgradeReportUnknownCurrentVersionIsNotActionable(t *testing.T) {
	oldLatest := fetchUpgradeLatestFn
	oldInfo := fetchUpgradeCatchInfoFn
	t.Cleanup(func() {
		fetchUpgradeLatestFn = oldLatest
		fetchUpgradeCatchInfoFn = oldInfo
	})
	fetchUpgradeLatestFn = func(context.Context, buildinfo.Channel, time.Time) (releaseCacheEntry, error) {
		return releaseCacheEntry{Tag: "nightly", Nightly: true}, nil
	}
	fetchUpgradeCatchInfoFn = func(ctx context.Context, host string) (serverInfo, error) {
		return serverInfo{Version: "unknown", InstallUser: "root", InstallHost: host}, nil
	}

	report := buildUpgradeReport(context.Background(), upgradeCheckRequest{
		Local:   buildinfo.Info{Version: "unknown", Channel: buildinfo.ChannelUnknown},
		Hosts:   []string{"edge"},
		Now:     time.Unix(100, 0),
		Nightly: true,
	})
	if report.Local.Status != upgradeStatusUnknown {
		t.Fatalf("local status = %q, want unknown", report.Local.Status)
	}
	if report.Catch[0].Status != upgradeStatusUnknown {
		t.Fatalf("catch row = %#v, want unknown", report.Catch[0])
	}
}
```

- [ ] **Step 2: Write failing cache test**

Add this test to `pkg/yeet/upgrade_check_test.go` near the other `fetchUpgradeLatest` tests:

```go
func TestFetchUpgradeLatestMarksNightlyTargets(t *testing.T) {
	restoreCache := stubUpdateCheckCacheFile(t)
	defer restoreCache()
	oldFetch := fetchGitHubReleaseFn
	t.Cleanup(func() { fetchGitHubReleaseFn = oldFetch })
	fetchGitHubReleaseFn = func(nightly bool) (githubRelease, error) {
		if !nightly {
			t.Fatal("nightly channel should fetch nightly release")
		}
		return githubRelease{TagName: "nightly", PublishedAt: "2026-07-09T00:00:00Z"}, nil
	}

	now := time.Unix(500, 0)
	got, err := fetchUpgradeLatest(context.Background(), buildinfo.ChannelNightly, now)
	if err != nil {
		t.Fatalf("fetch latest: %v", err)
	}
	if got.Tag != "nightly" || !got.Nightly {
		t.Fatalf("latest = %#v, want nightly target", got)
	}
	cache := readUpdateCheckCache(updateCheckCacheFile)
	if cache.LatestNightly.Tag != "nightly" || !cache.LatestNightly.Nightly {
		t.Fatalf("cache latest = %#v, want nightly target", cache.LatestNightly)
	}
}
```

- [ ] **Step 3: Run tests and verify they fail**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestBuildUpgradeReportNightly|TestBuildUpgradeReportUnknownCurrentVersionIsNotActionable|TestFetchUpgradeLatestMarksNightlyTargets' -count=1
```

Expected: FAIL because `Nightly` fields and nightly classification helpers do not exist yet.

- [ ] **Step 4: Mark release cache entries with nightly target state**

In `pkg/yeet/update_cache.go`, update `releaseCacheEntry`:

```go
type releaseCacheEntry struct {
	Tag         string        `json:"tag,omitempty"`
	PublishedAt string        `json:"publishedAt,omitempty"`
	CheckedAt   time.Time     `json:"checkedAt,omitempty"`
	Assets      []githubAsset `json:"assets,omitempty"`
	Nightly     bool          `json:"nightly,omitempty"`
}
```

- [ ] **Step 5: Add nightly request/report fields and target-channel helper**

In `pkg/yeet/upgrade_check.go`, update the structs and add the helper:

```go
type upgradeCheckRequest struct {
	Local         buildinfo.Info
	Hosts         []string
	Now           time.Time
	Force         bool
	Nightly       bool
	TargetVersion string
}

type upgradeReport struct {
	Latest        releaseCacheEntry  `json:"latest"`
	Local         upgradeComponent   `json:"local"`
	Catch         []upgradeComponent `json:"catch,omitempty"`
	Force         bool               `json:"force,omitempty"`
	Nightly       bool               `json:"nightly,omitempty"`
	TargetVersion string             `json:"targetVersion,omitempty"`
}

func upgradeTargetChannel(local buildinfo.Info, nightly bool) buildinfo.Channel {
	if nightly {
		return buildinfo.ChannelNightly
	}
	return local.ReleaseChannel()
}
```

- [ ] **Step 6: Thread the nightly target through report building**

In `pkg/yeet/upgrade_check.go`, replace `buildUpgradeReport` with:

```go
func buildUpgradeReport(ctx context.Context, req upgradeCheckRequest) upgradeReport {
	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}
	channel := upgradeTargetChannel(req.Local, req.Nightly)
	latest, latestErr := fetchUpgradeTarget(ctx, channel, now, req.TargetVersion)
	if req.Nightly {
		latest.Nightly = true
	}
	report := upgradeReport{
		Latest:        latest,
		Force:         req.Force,
		Nightly:       req.Nightly,
		TargetVersion: strings.TrimSpace(req.TargetVersion),
	}
	report.Local = classifyLocalUpgrade(req.Local, latest, latestErr, req.Force)
	for _, host := range req.Hosts {
		report.Catch = append(report.Catch, checkCatchUpgrade(ctx, host, latest, latestErr, req.Force))
	}
	return report
}
```

- [ ] **Step 7: Classify nightly targets without semver and keep unknown current versions non-actionable**

In `pkg/yeet/upgrade_check.go`, add helpers:

```go
func classifyUpgradeVersion(current string, latest releaseCacheEntry) upgradeStatus {
	current = strings.TrimSpace(current)
	target := strings.TrimSpace(latest.Tag)
	if latest.Nightly {
		if current == target {
			return upgradeStatusCurrent
		}
		return upgradeStatusUpdateAvailable
	}
	cmp := buildinfo.CompareSemver(current, target)
	if cmp < 0 {
		return upgradeStatusUpdateAvailable
	}
	if cmp > 0 {
		return upgradeStatusAhead
	}
	return upgradeStatusCurrent
}

func currentVersionUnknown(version string) bool {
	version = strings.TrimSpace(version)
	return version == "" || version == "unknown"
}
```

Then replace the semver blocks in `classifyLocalUpgrade` and `checkCatchUpgrade` with calls to the helpers:

```go
if currentVersionUnknown(local.Version) {
	row.Status = upgradeStatusUnknown
	row.Reason = "current version is unknown"
	return row
}
if !local.IsRelease() {
	row.Status = upgradeStatusDev
	row.Reason = "source/dev builds are not self-updated as release binaries"
	return row
}
row.Status = classifyUpgradeVersion(local.Version, latest)
return row
```

```go
if currentVersionUnknown(info.Version) {
	row.Status = upgradeStatusUnknown
	row.Reason = "current version is unknown"
	return row
}
catchBuild := buildinfo.Info{Version: info.Version}
if !catchBuild.IsRelease() {
	row.Status = upgradeStatusDev
	row.Reason = "source/dev builds are not self-updated as release binaries"
	return row
}
row.Status = classifyUpgradeVersion(info.Version, latest)
return row
```

- [ ] **Step 8: Preserve nightly target state from fresh and stale cache**

In `pkg/yeet/upgrade_check.go`, update `fetchUpgradeLatest` so every return from the nightly cache path has `Nightly: true`:

```go
func fetchUpgradeLatest(ctx context.Context, channel buildinfo.Channel, now time.Time) (releaseCacheEntry, error) {
	cache := readUpdateCheckCache(updateCheckCacheFile)
	nightly := channel == buildinfo.ChannelNightly
	entry := cache.LatestStable
	if nightly {
		entry = cache.LatestNightly
		entry.Nightly = true
	}
	if err := ctx.Err(); err != nil {
		if entry.Tag != "" {
			return entry, nil
		}
		return releaseCacheEntry{}, err
	}
	rel, err := fetchGitHubReleaseFn(nightly)
	if err != nil {
		if entry.Tag != "" {
			return entry, nil
		}
		return releaseCacheEntry{}, err
	}
	entry = releaseCacheEntry{Tag: rel.TagName, PublishedAt: rel.PublishedAt, CheckedAt: now, Assets: rel.Assets, Nightly: nightly}
	if nightly {
		cache.LatestNightly = entry
	} else {
		cache.LatestStable = entry
	}
	_ = writeUpdateCheckCache(updateCheckCacheFile, cache)
	return entry, nil
}
```

- [ ] **Step 9: Run task tests and verify they pass**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestBuildUpgradeReportNightly|TestBuildUpgradeReportUnknownCurrentVersionIsNotActionable|TestFetchUpgradeLatestMarksNightlyTargets' -count=1
```

Expected: PASS for `./pkg/yeet`.

- [ ] **Step 10: Commit report-building changes**

Run:

```bash
but diff
but commit codex/upgrade-nightly-flag-design -m "upgrade: build nightly target reports"
```

Expected: GitButler creates a commit containing only `pkg/yeet/update_cache.go`, `pkg/yeet/upgrade_check.go`, and `pkg/yeet/upgrade_check_test.go`.

---

### Task 3: Install from Nightly Targets

**Files:**
- Modify: `pkg/yeet/upgrade_cmd.go`
- Modify: `pkg/yeet/upgrade_cmd_test.go`
- Modify: `pkg/yeet/upgrade_install.go`
- Modify: `pkg/yeet/upgrade_install_test.go`

**Interfaces:**
- Consumes: `upgradeReport.Nightly bool` and `releaseCacheEntry.Nightly bool` from Task 2.
- Produces: `upgradeReportTargetsNightly(report upgradeReport) bool`
- Produces: `upgradeReportReleaseVersion(report upgradeReport) string`
- Produces: `upgradeLocalFromReport(flags cli.UpgradeFlags, report upgradeReport) error` passing nightly target state in `releaseCacheEntry`.

- [ ] **Step 1: Write failing command tests**

Add these tests to `pkg/yeet/upgrade_cmd_test.go`:

```go
func TestRenderUpgradeReportUsesTargetForNightly(t *testing.T) {
	report := upgradeReport{
		Latest:  releaseCacheEntry{Tag: "nightly", Nightly: true},
		Nightly: true,
		Local: upgradeComponent{
			Name:    "yeet",
			Current: "v0.9.5",
			Latest:  "nightly",
			Status:  upgradeStatusUpdateAvailable,
		},
	}
	var out bytes.Buffer
	if err := renderUpgradeReport(&out, report); err != nil {
		t.Fatalf("renderUpgradeReport: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "TARGET") || strings.Contains(got, "LATEST") {
		t.Fatalf("output should use TARGET header:\n%s", got)
	}
}

func TestHandleUpgradePassesNightlyToReport(t *testing.T) {
	old := buildUpgradeReportFn
	t.Cleanup(func() { buildUpgradeReportFn = old })
	var gotNightly bool
	buildUpgradeReportFn = func(_ context.Context, req upgradeCheckRequest) upgradeReport {
		gotNightly = req.Nightly
		return upgradeReport{Local: upgradeComponent{Name: "yeet", Current: "v0.9.5", Latest: "nightly", Status: upgradeStatusCurrent}}
	}

	if err := handleUpgrade(context.Background(), []string{"check", "--nightly"}, &bytes.Buffer{}, &bytes.Buffer{}, buildinfo.Info{Version: "v0.9.5", Channel: buildinfo.ChannelStable}); err != nil {
		t.Fatalf("handleUpgrade: %v", err)
	}
	if !gotNightly {
		t.Fatal("Nightly = false, want true")
	}
}

func TestRunUpgradeUpdatesCatchFromNightly(t *testing.T) {
	oldInit := initCatchFn
	t.Cleanup(func() { initCatchFn = oldInit })
	var target string
	var nightly bool
	var releaseVersion string
	var noWorkspace bool
	var suppressNextSteps bool
	initCatchFn = func(userAtRemote string, opts initOptions) error {
		target = userAtRemote
		if !opts.fromGithub {
			t.Fatalf("opts = %#v, want from github", opts)
		}
		nightly = opts.nightly
		releaseVersion = opts.releaseVersion
		noWorkspace = opts.noWorkspace
		suppressNextSteps = opts.suppressNextSteps
		return nil
	}
	report := upgradeReport{
		Latest:  releaseCacheEntry{Tag: "nightly", Nightly: true},
		Nightly: true,
		Local:   upgradeComponent{Name: "yeet", Current: "nightly", Latest: "nightly", Status: upgradeStatusCurrent},
		Catch: []upgradeComponent{
			{Name: "catch", Host: "edge-a", Current: "v0.9.5", Latest: "nightly", Status: upgradeStatusUpdateAvailable, InstallUser: "root", InstallHost: "machine-a"},
		},
	}
	if err := runUpgrade(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, cli.UpgradeFlags{Yes: true, Nightly: true}, report); err != nil {
		t.Fatalf("runUpgrade: %v", err)
	}
	if target != "root@machine-a" {
		t.Fatalf("target = %q", target)
	}
	if !nightly {
		t.Fatal("nightly = false, want true")
	}
	if releaseVersion != "" {
		t.Fatalf("releaseVersion = %q, want empty for nightly", releaseVersion)
	}
	if !noWorkspace {
		t.Fatal("noWorkspace = false, want true")
	}
	if !suppressNextSteps {
		t.Fatal("suppressNextSteps = false, want true")
	}
}

func TestUpgradeLocalFromReportPreservesNightlyTarget(t *testing.T) {
	oldUpgrade := upgradeLocalBinaryFn
	t.Cleanup(func() { upgradeLocalBinaryFn = oldUpgrade })
	var gotLatest releaseCacheEntry
	upgradeLocalBinaryFn = func(_ buildinfo.Info, latest releaseCacheEntry, force bool) error {
		gotLatest = latest
		return nil
	}

	report := upgradeReport{
		Latest:  releaseCacheEntry{Tag: "nightly"},
		Nightly: true,
		Local:   upgradeComponent{Name: "yeet", Current: "v0.9.5", Latest: "nightly", Status: upgradeStatusUpdateAvailable},
	}
	if err := upgradeLocalFromReport(cli.UpgradeFlags{Nightly: true}, report); err != nil {
		t.Fatalf("upgradeLocalFromReport: %v", err)
	}
	if gotLatest.Tag != "nightly" || !gotLatest.Nightly {
		t.Fatalf("latest = %#v, want nightly target", gotLatest)
	}
}
```

- [ ] **Step 2: Write failing local install test**

Add this test to `pkg/yeet/upgrade_install_test.go` after `TestUpgradeLocalBinaryDownloadsAndReplaces`:

```go
func TestUpgradeLocalBinaryUsesNightlyTargetAssets(t *testing.T) {
	oldExecutable := currentExecutableFn
	oldResolve := resolveYeetReleaseAssetFn
	oldDownload := downloadFileFn
	oldExtract := extractSingleBinaryFn
	oldReplace := replaceLocalBinaryFn
	t.Cleanup(func() {
		currentExecutableFn = oldExecutable
		resolveYeetReleaseAssetFn = oldResolve
		downloadFileFn = oldDownload
		extractSingleBinaryFn = oldExtract
		replaceLocalBinaryFn = oldReplace
	})

	targetPath := filepath.Join(t.TempDir(), "yeet")
	if err := os.WriteFile(targetPath, []byte("old"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}
	currentExecutableFn = func() (string, error) {
		return targetPath, nil
	}
	var gotNightly bool
	var gotVersion string
	resolveYeetReleaseAssetFn = func(goos, goarch string, nightly bool, version string) (string, string, string, string, error) {
		gotNightly = nightly
		gotVersion = version
		return "yeet-darwin-arm64.tar.gz", "https://example.com/yeet.tgz", "https://example.com/yeet.sha256", "nightly", nil
	}
	downloadFileFn = func(url, path string) error {
		payload := []byte("archive")
		if url == "https://example.com/yeet.sha256" {
			sum := sha256.Sum256(payload)
			return os.WriteFile(path, []byte(fmt.Sprintf("%x  yeet-darwin-arm64.tar.gz\n", sum)), 0o644)
		}
		return os.WriteFile(path, payload, 0o644)
	}
	extractSingleBinaryFn = func(archivePath, dstDir string) (string, error) {
		return filepath.Join(dstDir, "yeet-darwin-arm64"), nil
	}
	replaceLocalBinaryFn = func(gotTarget, gotSource string, sudo bool) error {
		return nil
	}

	err := upgradeLocalBinary(
		buildinfo.Info{Version: "v0.9.5", Channel: buildinfo.ChannelStable},
		releaseCacheEntry{Tag: "nightly", Nightly: true},
		false,
	)
	if err != nil {
		t.Fatalf("upgradeLocalBinary: %v", err)
	}
	if !gotNightly || gotVersion != "nightly" {
		t.Fatalf("resolve nightly=%v version=%q, want nightly=true version=nightly", gotNightly, gotVersion)
	}
}
```

- [ ] **Step 3: Run tests and verify they fail**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestRenderUpgradeReportUsesTargetForNightly|TestHandleUpgradePassesNightlyToReport|TestRunUpgradeUpdatesCatchFromNightly|TestUpgradeLocalFromReportPreservesNightlyTarget|TestUpgradeLocalBinaryUsesNightlyTargetAssets' -count=1
```

Expected: FAIL because `handleUpgrade` does not pass `Nightly`, report rendering does not treat nightly as a target, catch init receives a release version instead of `nightly`, and local install still resolves assets from the local binary channel.

- [ ] **Step 4: Pass nightly through command handling and rendering**

In `pkg/yeet/upgrade_cmd.go`, pass `Nightly` to `buildUpgradeReport`:

```go
report := buildUpgradeReportFn(ctx, upgradeCheckRequest{
	Local:         local,
	Hosts:         hosts,
	Now:           time.Now(),
	Force:         flags.Force,
	Nightly:       flags.Nightly,
	TargetVersion: flags.Version,
})
```

Then replace `upgradeReportUsesTarget` and add a report helper:

```go
func upgradeReportUsesTarget(report upgradeReport) bool {
	return report.Force || upgradeReportTargetsNightly(report) || strings.TrimSpace(report.TargetVersion) != ""
}

func upgradeReportTargetsNightly(report upgradeReport) bool {
	return report.Nightly || report.Latest.Nightly
}
```

- [ ] **Step 5: Preserve nightly target state during local upgrade**

In `pkg/yeet/upgrade_cmd.go`, update `upgradeLocalFromReport`:

```go
func upgradeLocalFromReport(flags cli.UpgradeFlags, report upgradeReport) error {
	if upgradeRowActionable(report.Local) {
		latest := report.Latest
		if flags.Nightly || upgradeReportTargetsNightly(report) {
			latest.Nightly = true
		}
		if err := upgradeLocalBinaryFn(buildinfo.Current(), latest, flags.Force); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 6: Use nightly init options for catch upgrades**

In `pkg/yeet/upgrade_cmd.go`, add:

```go
func upgradeReportReleaseVersion(report upgradeReport) string {
	if upgradeReportTargetsNightly(report) {
		return ""
	}
	return report.Latest.Tag
}
```

Then update the `initCatchFn` call in `upgradeCatchFromReport`:

```go
opts := initOptions{
	fromGithub:        true,
	nightly:           upgradeReportTargetsNightly(report),
	noWorkspace:       true,
	suppressNextSteps: true,
	releaseVersion:    upgradeReportReleaseVersion(report),
}
if err := withTemporaryHost(row.Host, func() error {
	return initCatchFn(target, opts)
}); err != nil {
	return fmt.Errorf("upgrade catch@%s: %w", row.Host, err)
}
```

- [ ] **Step 7: Make local install choose target channel, not current binary channel**

In `pkg/yeet/upgrade_install.go`, update `localUpgradePlan` and `upgradeLocalBinary`:

```go
func localUpgradePlan(local buildinfo.Info, latest releaseCacheEntry, force bool) (localUpgradePlanResult, error) {
	result := localUpgradePlanResult{From: local.Version, To: latest.Tag}
	if latest.Tag == "" {
		result.Action = localUpgradeActionSkip
		result.Reason = "latest release is unknown"
		return result, nil
	}
	if force {
		result.Action = localUpgradeActionUpdate
		return result, nil
	}
	if currentVersionUnknown(local.Version) {
		result.Action = localUpgradeActionSkip
		result.Reason = "current version is unknown"
		return result, nil
	}
	if !local.IsRelease() {
		result.Action = localUpgradeActionSkip
		result.Reason = "source/dev builds are not self-updated as release binaries"
		return result, nil
	}
	if latest.Nightly {
		if strings.TrimSpace(local.Version) == strings.TrimSpace(latest.Tag) {
			result.Action = localUpgradeActionSkip
			result.Reason = "already current"
			return result, nil
		}
		result.Action = localUpgradeActionUpdate
		return result, nil
	}
	if buildinfo.CompareSemver(local.Version, latest.Tag) >= 0 {
		result.Action = localUpgradeActionSkip
		result.Reason = "already current"
		return result, nil
	}
	result.Action = localUpgradeActionUpdate
	return result, nil
}
```

```go
assetName, assetURL, shaURL, _, err := resolveYeetReleaseAssetFn(runtime.GOOS, runtime.GOARCH, latest.Nightly, latest.Tag)
```

- [ ] **Step 8: Run task tests and verify they pass**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestRenderUpgradeReportUsesTargetForNightly|TestHandleUpgradePassesNightlyToReport|TestRunUpgradeUpdatesCatchFromNightly|TestUpgradeLocalFromReportPreservesNightlyTarget|TestUpgradeLocalBinaryUsesNightlyTargetAssets|TestRunUpgradeUpdatesCatchWithRecordedInstallTarget|TestUpgradeLocalBinaryDownloadsAndReplaces' -count=1
```

Expected: PASS for `./pkg/yeet`, including the existing stable install tests.

- [ ] **Step 9: Commit install-flow changes**

Run:

```bash
but diff
but commit codex/upgrade-nightly-flag-design -m "upgrade: install from nightly targets"
```

Expected: GitButler creates a commit containing only `pkg/yeet/upgrade_cmd.go`, `pkg/yeet/upgrade_cmd_test.go`, `pkg/yeet/upgrade_install.go`, and `pkg/yeet/upgrade_install_test.go`.

---

### Task 4: Update User Docs and Generated Help Reference

**Files:**
- Modify: `README.md`
- Modify: `website/docs/getting-started/host-setup.mdx`
- Modify: `website/docs/cli/yeet-cli.mdx`
- Modify: `.codex/skills/yeet-cli/references/yeet-help-agent.md`
- Do not modify: `website/docs/changelog.mdx`

**Interfaces:**
- Consumes: help metadata from Task 1.
- Produces: docs that tell users `yeet upgrade --nightly` targets the nightly GitHub release and cannot be combined with `--version`.

- [ ] **Step 1: Update README upgrade examples**

In `README.md`, replace the upgrade section with:

````markdown
## Upgrades

Check local yeet and catch hosts:

```bash
yeet upgrade check
```

Upgrade from verified GitHub release assets:

```bash
yeet upgrade
```

When run from a service workspace with `yeet.toml`, `yeet upgrade` includes all project catch hosts plus the default catch host.

Upgrade one host:

```bash
yeet upgrade --host=<catch-host>
```

Force reinstall:

```bash
yeet upgrade --force
```

Install the latest nightly release:

```bash
yeet upgrade --nightly
```

Install a specific public release:

```bash
yeet upgrade --version v0.6.1 --force
```

`--nightly` and `--version` select different targets, so use one of them per command.
````

- [ ] **Step 2: Update host setup guide**

In `website/docs/getting-started/host-setup.mdx`, replace the upgrade section with:

````markdown
## Upgrade yeet and catch

Check the local CLI and catch hosts:

```bash
yeet upgrade check
```

Upgrade from verified GitHub release assets:

```bash
yeet upgrade
```

When you run from a service workspace with `yeet.toml`, `yeet upgrade` includes
all project catch hosts plus the default catch host. Use `--host=<catch-host>`
only when you want to upgrade one catch host. Otherwise, the project file is the
source of truth and yeet follows it.

```bash
yeet upgrade --host=<catch-host>
```

To reinstall the latest public release even when a component already looks
current, newer, or locally built:

```bash
yeet upgrade --force
```

To install the latest nightly release:

```bash
yeet upgrade --nightly
```

To install a specific public release, select the tag:

```bash
yeet upgrade --version v0.6.1 --force
```

`--nightly` and `--version` select different targets, so use one of them per command.
````

- [ ] **Step 3: Update CLI manual page**

In `website/docs/cli/yeet-cli.mdx`, replace the `upgrade` command block with:

````markdown
### `upgrade`

Check and install public yeet/catch releases:

```bash
yeet upgrade check
yeet upgrade
yeet upgrade --host=<catch-host>
yeet upgrade --force
yeet upgrade --nightly
yeet upgrade check --nightly
yeet upgrade --version v0.6.1 --force
```

When run from a service workspace with `yeet.toml`, `yeet upgrade` includes all
project catch hosts plus the default catch host. Use `--host=<catch-host>` to
upgrade one catch host.

Use `--nightly` to target the latest nightly release. Use `--version` to target
a specific public release tag; do not use both in the same command.
````

- [ ] **Step 4: Regenerate agent help reference**

Run:

```bash
tools/generate-yeet-help-agent.sh
```

Expected: `.codex/skills/yeet-cli/references/yeet-help-agent.md` changes and the `upgrade` section includes `[--nightly]`, `yeet upgrade --nightly`, and `yeet upgrade check --nightly`.

- [ ] **Step 5: Verify docs content and changelog exclusion**

Run:

```bash
rg -n "yeet upgrade --nightly|--nightly.*--version|--version.*--nightly" README.md website/docs/getting-started/host-setup.mdx website/docs/cli/yeet-cli.mdx .codex/skills/yeet-cli/references/yeet-help-agent.md
git diff -- website/docs/changelog.mdx
git -C website diff --check
rg -n "private[-]host|/User[s]/" README.md website/docs .codex/skills
```

Expected:
- The `rg` command finds the nightly upgrade examples in README, website docs, and generated help reference.
- `git diff -- website/docs/changelog.mdx` prints no diff.
- `git -C website diff --check` exits 0.
- The private-path scan exits 1 because it finds no private hostnames or local `/Users/...` paths in public docs or agent docs.

- [ ] **Step 6: Commit and push website submodule docs**

Run:

```bash
git -C website status --short --branch
git -C website add docs/getting-started/host-setup.mdx docs/cli/yeet-cli.mdx
git -C website commit -m "docs: document nightly upgrades"
git -C website push origin HEAD:main
```

Expected: the website commit is created and pushed to the website repository's `main` branch.

- [ ] **Step 7: Commit root docs and submodule pointer**

Run:

```bash
git diff --submodule=log -- website
but diff
but commit codex/upgrade-nightly-flag-design -m "docs: document upgrade nightly flag"
```

Expected:
- `git diff --submodule=log -- website` shows exactly the website commit from Step 6.
- GitButler creates a root commit containing `README.md`, `.codex/skills/yeet-cli/references/yeet-help-agent.md`, and the `website` gitlink.

---

### Task 5: Final Verification

**Files:**
- Read/verify only; no planned edits.

**Interfaces:**
- Consumes all prior task commits.
- Produces a branch that is locally verified and ready for review, landing, or nightly publication.

- [ ] **Step 1: Run focused tests**

Run:

```bash
mise exec -- go test ./pkg/cli ./cmd/yeet ./pkg/yeet -run 'TestParseUpgrade|TestRunUpgradeHelpShowsNightly|TestBuildUpgradeReportNightly|TestBuildUpgradeReportUnknownCurrentVersionIsNotActionable|TestFetchUpgradeLatestMarksNightlyTargets|TestRenderUpgradeReportUsesTargetForNightly|TestHandleUpgradePassesNightlyToReport|TestRunUpgradeUpdatesCatchFromNightly|TestUpgradeLocalFromReportPreservesNightlyTarget|TestUpgradeLocalBinaryUsesNightlyTargetAssets' -count=1
```

Expected: PASS for `./pkg/cli`, `./cmd/yeet`, and `./pkg/yeet`.

- [ ] **Step 2: Run full Go tests**

Run:

```bash
mise exec -- go test ./... -count=1
```

Expected: PASS for all packages.

- [ ] **Step 3: Run pre-commit**

Run:

```bash
mise exec -- pre-commit run --all-files
```

Expected: PASS for every hook.

- [ ] **Step 4: Smoke-test generated help and parser errors**

Run:

```bash
mise exec -- go run ./cmd/yeet upgrade --help-agent | rg -- '--nightly|yeet upgrade --nightly|yeet upgrade check --nightly'
mise exec -- go run ./cmd/yeet upgrade --nightly --version v0.6.1
```

Expected:
- The first command prints the upgrade usage/examples containing nightly.
- The second command exits non-zero and prints an error containing `--nightly cannot be used with --version`.

- [ ] **Step 5: Check workspace state**

Run:

```bash
but status
git -C website status --short --branch
git diff --submodule=log -- website
```

Expected:
- `but status` shows no uncommitted root changes.
- `git -C website status --short --branch` shows the website repo clean and not ahead of its upstream.
- `git diff --submodule=log -- website` prints no diff because the root commit already pins the pushed website commit.

- [ ] **Step 6: Record nightly-publication caveat**

Do not claim live `yeet upgrade --nightly` was verified from GitHub until this branch is landed and the nightly release workflow has published artifacts containing the new flag. Run the live validation commands from the service workspace used for upgrade smoke tests:

```bash
yeet upgrade check --nightly
yeet upgrade --nightly --force
```

Expected after publication: the command plan targets `nightly`, local yeet uses the nightly `yeet-OS-ARCH.tar.gz` asset, and catch reinstalls run through `yeet init --from-github --nightly --no-workspace` without workspace prompts or next-step setup text.

---

## Self-Review Checklist

- Spec coverage: parser, conflict validation, stable-to-nightly targeting, report JSON target marker, table target header, local asset selection, catch init options, docs/help surfaces, and nightly-publication caveat are covered.
- Placeholder scan: this plan intentionally avoids deferred-work markers, unspecified edge cases, and unstated tests.
- Type consistency: `Nightly bool` is named the same in `cli.UpgradeFlags`, `upgradeCheckRequest`, `upgradeReport`, and `releaseCacheEntry`; helpers are consistently named `upgradeReportTargetsNightly`, `upgradeReportReleaseVersion`, `upgradeTargetChannel`, `classifyUpgradeVersion`, and `currentVersionUnknown`.
