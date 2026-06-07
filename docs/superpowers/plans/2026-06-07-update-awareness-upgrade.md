# Update Awareness And Upgrade Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add robust public update awareness and an explicit `yeet upgrade` workflow that updates a release-installed local `yeet` binary and one or more catch hosts from verified GitHub release assets.

**Architecture:** Add shared build metadata in `pkg/buildinfo`, keep release/GitHub/cache/upgrade logic in focused `pkg/yeet` files, and wire only thin command/context hooks through `cmd/yeet`. Normal commands use a cached passive advisory that never blocks or changes exit status; explicit `yeet upgrade check` and `yeet upgrade` do the network probing and mutation.

**Tech Stack:** Go, yargs CLI parsing, existing catch RPC `catch.Info`, GitHub Releases API/assets, SHA256 verification, atomic local file replacement, existing `yeet init --from-github` catch install path, website MDX docs.

---

## File Structure

- Create: `pkg/buildinfo/buildinfo.go`
- Create: `pkg/buildinfo/buildinfo_test.go`
- Modify: `pkg/catch/version.go`
- Modify: `.github/workflows/release.yaml`
- Modify: `.github/workflows/nightly-release.yaml`
- Modify: `pkg/yeet/init_release.go`
- Modify: `pkg/yeet/init_download.go`
- Create: `pkg/yeet/release_assets_test.go`
- Create: `pkg/yeet/update_cache.go`
- Create: `pkg/yeet/update_cache_test.go`
- Create: `pkg/yeet/upgrade_check.go`
- Create: `pkg/yeet/upgrade_check_test.go`
- Create: `pkg/yeet/upgrade_cmd.go`
- Create: `pkg/yeet/upgrade_cmd_test.go`
- Create: `pkg/yeet/upgrade_install.go`
- Create: `pkg/yeet/upgrade_install_test.go`
- Create: `pkg/yeet/update_advisory.go`
- Create: `pkg/yeet/update_advisory_test.go`
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`
- Modify: `cmd/yeet/cli.go`
- Modify: `cmd/yeet/yeet.go`
- Modify: `cmd/yeet/cli_test.go`
- Modify: `README.md`
- Modify: `website/docs/getting-started/installation.mdx`
- Modify: `website/docs/cli/yeet-cli.mdx`
- Modify: `website/docs/changelog.mdx`

Commit steps in this plan require explicit user authorization at execution time.

## Task 1: Shared Build Metadata

**Files:**
- Create: `pkg/buildinfo/buildinfo.go`
- Create: `pkg/buildinfo/buildinfo_test.go`
- Modify: `pkg/catch/version.go`
- Modify: `.github/workflows/release.yaml`
- Modify: `.github/workflows/nightly-release.yaml`

- [ ] **Step 1: Write build metadata tests**

Create `pkg/buildinfo/buildinfo_test.go` with tests for stable, nightly, dev, unknown, dirty commit, and semver comparison:

```go
package buildinfo

import "testing"

func TestClassifyVersion(t *testing.T) {
	tests := []struct {
		name string
		info Info
		want Channel
	}{
		{name: "stable", info: Info{Version: "v0.5.13"}, want: ChannelStable},
		{name: "nightly", info: Info{Version: "nightly-abc1234"}, want: ChannelNightly},
		{name: "dev", info: Info{Version: "abc123456"}, want: ChannelDev},
		{name: "unknown", info: Info{Version: "unknown"}, want: ChannelUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.info.ReleaseChannel(); got != tt.want {
				t.Fatalf("ReleaseChannel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCompareSemver(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"v0.5.13", "v0.5.13", 0},
		{"v0.5.12", "v0.5.13", -1},
		{"v0.5.14", "v0.5.13", 1},
		{"0.5.13", "v0.5.13", 0},
		{"dev", "v0.5.13", 0},
	}
	for _, tt := range tests {
		if got := CompareSemver(tt.a, tt.b); got != tt.want {
			t.Fatalf("CompareSemver(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestCommitVersionShortensAndMarksDirty(t *testing.T) {
	got := commitVersionFromSettings([]buildSetting{
		{Key: "vcs.revision", Value: "123456789abcdef"},
		{Key: "vcs.modified", Value: "true"},
	})
	if got != "123456789+dirty" {
		t.Fatalf("commit version = %q", got)
	}
}
```

- [ ] **Step 2: Run the failing buildinfo tests**

Run:

```bash
go test ./pkg/buildinfo -count=1
```

Expected: FAIL because `pkg/buildinfo` does not exist yet.

- [ ] **Step 3: Implement `pkg/buildinfo`**

Create `pkg/buildinfo/buildinfo.go`:

```go
package buildinfo

import (
	"runtime/debug"
	"strconv"
	"strings"
)

type Channel string

const (
	ChannelStable  Channel = "stable"
	ChannelNightly Channel = "nightly"
	ChannelDev     Channel = "dev"
	ChannelUnknown Channel = "unknown"
)

// BuildVersion is injected by release workflows with -ldflags.
var BuildVersion string

type Info struct {
	Version string  `json:"version"`
	Commit  string  `json:"commit,omitempty"`
	Dirty   bool    `json:"dirty,omitempty"`
	Channel Channel `json:"channel"`
}

type buildSetting struct {
	Key   string
	Value string
}

func Current() Info {
	commit, dirty := readCommit()
	version := strings.TrimSpace(BuildVersion)
	if version == "" {
		version = commitVersion(commit, dirty)
	}
	info := Info{Version: version, Commit: commit, Dirty: dirty}
	info.Channel = info.ReleaseChannel()
	return info
}

func Version() string {
	return Current().Version
}

func CommitVersion() string {
	commit, dirty := readCommit()
	return commitVersion(commit, dirty)
}

func (i Info) ReleaseChannel() Channel {
	if i.Channel != "" {
		return i.Channel
	}
	v := strings.TrimSpace(i.Version)
	switch {
	case isStableVersion(v):
		return ChannelStable
	case strings.HasPrefix(v, "nightly-"):
		return ChannelNightly
	case v == "" || v == "unknown":
		return ChannelUnknown
	default:
		return ChannelDev
	}
}

func (i Info) IsRelease() bool {
	ch := i.ReleaseChannel()
	return ch == ChannelStable || ch == ChannelNightly
}

func CompareSemver(a, b string) int {
	av, aok := parseSemver(a)
	bv, bok := parseSemver(b)
	if !aok || !bok {
		return 0
	}
	for i := range av {
		if av[i] < bv[i] {
			return -1
		}
		if av[i] > bv[i] {
			return 1
		}
	}
	return 0
}

func isStableVersion(v string) bool {
	_, ok := parseSemver(v)
	return ok
}

func parseSemver(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func readCommit() (string, bool) {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}
	settings := make([]buildSetting, 0, len(bi.Settings))
	for _, s := range bi.Settings {
		settings = append(settings, buildSetting{Key: s.Key, Value: s.Value})
	}
	version := commitVersionFromSettings(settings)
	dirty := strings.HasSuffix(version, "+dirty")
	return strings.TrimSuffix(version, "+dirty"), dirty
}

func commitVersionFromSettings(settings []buildSetting) string {
	var commit string
	var dirty bool
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			commit = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	return commitVersion(commit, dirty)
}

func commitVersion(commit string, dirty bool) string {
	if commit == "" {
		return "dev"
	}
	if len(commit) >= 9 {
		commit = commit[:9]
	}
	if dirty {
		commit += "+dirty"
	}
	return commit
}
```

- [ ] **Step 4: Replace catch-local version logic**

Modify `pkg/catch/version.go` so it delegates to `pkg/buildinfo`:

```go
package catch

import "github.com/yeetrun/yeet/pkg/buildinfo"

func Version() string {
	return buildinfo.Version()
}

func VersionCommit() string {
	return buildinfo.CommitVersion()
}
```

- [ ] **Step 5: Stamp both yeet and catch in release workflows**

In `.github/workflows/release.yaml`, add the same ldflags to the yeet build and change catch to use `pkg/buildinfo.BuildVersion`:

```yaml
VERSION=${{ github.ref_name }}
GOOS=${{ matrix.goos }} GOARCH=${{ matrix.goarch }} \
  go build -ldflags "-X github.com/yeetrun/yeet/pkg/buildinfo.BuildVersion=${VERSION}" \
  -o dist/${{ matrix.asset }} ./cmd/yeet
```

```yaml
VERSION=${{ github.ref_name }}
GOOS=${{ matrix.goos }} GOARCH=${{ matrix.goarch }} \
  go build -ldflags "-X github.com/yeetrun/yeet/pkg/buildinfo.BuildVersion=${VERSION}" \
  -o dist/${{ matrix.asset }} ./cmd/catch
```

In `.github/workflows/nightly-release.yaml`, use:

```yaml
VERSION=nightly-${GITHUB_SHA::7}
GOOS=${{ matrix.goos }} GOARCH=${{ matrix.goarch }} \
  go build -ldflags "-X github.com/yeetrun/yeet/pkg/buildinfo.BuildVersion=${VERSION}" \
  -o dist/${{ matrix.asset }} ./cmd/yeet
```

and the same `pkg/buildinfo.BuildVersion` path for catch.

- [ ] **Step 6: Verify the build metadata task**

Run:

```bash
gofmt -w pkg/buildinfo/buildinfo.go pkg/buildinfo/buildinfo_test.go pkg/catch/version.go
go test ./pkg/buildinfo ./pkg/catch -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

If commits are authorized for execution:

```bash
git add pkg/buildinfo pkg/catch/version.go .github/workflows/release.yaml .github/workflows/nightly-release.yaml
git commit -m "build: stamp yeet release versions"
```

## Task 2: Generalize Release Asset Resolution

**Files:**
- Modify: `pkg/yeet/init_release.go`
- Modify: `pkg/yeet/init_download.go`
- Create: `pkg/yeet/release_assets_test.go`

- [ ] **Step 1: Write release asset tests**

Create `pkg/yeet/release_assets_test.go`:

```go
package yeet

import "testing"

func TestYeetReleaseAssetNames(t *testing.T) {
	asset, sha, err := yeetReleaseAssetNames("darwin", "arm64")
	if err != nil {
		t.Fatalf("yeetReleaseAssetNames error: %v", err)
	}
	if asset != "yeet-darwin-arm64.tar.gz" || sha != "yeet-darwin-arm64.tar.gz.sha256" {
		t.Fatalf("asset=%q sha=%q", asset, sha)
	}
}

func TestYeetReleaseAssetNamesRejectUnsupported(t *testing.T) {
	if _, _, err := yeetReleaseAssetNames("windows", "amd64"); err == nil {
		t.Fatal("expected unsupported OS error")
	}
	if _, _, err := yeetReleaseAssetNames("linux", "386"); err == nil {
		t.Fatal("expected unsupported arch error")
	}
}

func TestResolveYeetReleaseAsset(t *testing.T) {
	old := fetchGitHubReleaseFn
	t.Cleanup(func() { fetchGitHubReleaseFn = old })
	fetchGitHubReleaseFn = func(nightly bool) (githubRelease, error) {
		if nightly {
			t.Fatal("nightly should be false")
		}
		return githubRelease{
			TagName: "v0.5.13",
			Assets: []githubAsset{
				{Name: "yeet-linux-amd64.tar.gz", BrowserDownloadURL: "https://example.com/yeet.tgz"},
				{Name: "yeet-linux-amd64.tar.gz.sha256", BrowserDownloadURL: "https://example.com/yeet.sha256"},
			},
		}, nil
	}
	asset, url, shaURL, tag, err := resolveYeetReleaseAsset("linux", "amd64", false)
	if err != nil {
		t.Fatalf("resolveYeetReleaseAsset error: %v", err)
	}
	if asset != "yeet-linux-amd64.tar.gz" || url == "" || shaURL == "" || tag != "v0.5.13" {
		t.Fatalf("asset=%q url=%q sha=%q tag=%q", asset, url, shaURL, tag)
	}
}
```

- [ ] **Step 2: Run failing release asset tests**

Run:

```bash
go test ./pkg/yeet -run 'TestYeetReleaseAsset|TestResolveYeetReleaseAsset' -count=1
```

Expected: FAIL because the yeet asset helpers and injectable fetch function do not exist.

- [ ] **Step 3: Add injectable release fetch and yeet asset helpers**

Modify `pkg/yeet/init_release.go`:

```go
var fetchGitHubReleaseFn = fetchGitHubRelease
```

Modify `pkg/yeet/init_download.go` so `resolveCatchReleaseAsset` calls `fetchGitHubReleaseFn(nightly)` instead of `fetchGitHubRelease(nightly)`, then add:

```go
func resolveYeetReleaseAsset(goos, goarch string, nightly bool) (assetName, assetURL, shaURL, tag string, err error) {
	assetName, shaName, err := yeetReleaseAssetNames(goos, goarch)
	if err != nil {
		return "", "", "", "", err
	}
	rel, err := fetchGitHubReleaseFn(nightly)
	if err != nil {
		return "", "", "", "", err
	}
	assetURL, shaURL, err = resolveReleaseAssetURLs(rel.Assets, assetName, shaName)
	if err != nil {
		return "", "", "", "", err
	}
	return assetName, assetURL, shaURL, rel.TagName, nil
}

func yeetReleaseAssetNames(goos, goarch string) (assetName, shaName string, err error) {
	goos = strings.ToLower(strings.TrimSpace(goos))
	goarch = strings.ToLower(strings.TrimSpace(goarch))
	switch goos {
	case "linux", "darwin":
	default:
		return "", "", fmt.Errorf("local system has unsupported OS: %s", goos)
	}
	switch goarch {
	case "amd64", "arm64":
	default:
		return "", "", fmt.Errorf("local system has unsupported arch: %s", goarch)
	}
	assetName = fmt.Sprintf("yeet-%s-%s.tar.gz", goos, goarch)
	return assetName, assetName + ".sha256", nil
}
```

- [ ] **Step 4: Verify release asset helpers**

Run:

```bash
gofmt -w pkg/yeet/init_release.go pkg/yeet/init_download.go pkg/yeet/release_assets_test.go
go test ./pkg/yeet -run 'Test.*ReleaseAsset|TestResolveReleaseAssetURLs|TestGitHubReleaseURL' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

If commits are authorized for execution:

```bash
git add pkg/yeet/init_release.go pkg/yeet/init_download.go pkg/yeet/release_assets_test.go
git commit -m "yeet: share release asset resolution"
```

## Task 3: Update Cache And Advisory State

**Files:**
- Create: `pkg/yeet/update_cache.go`
- Create: `pkg/yeet/update_cache_test.go`

- [ ] **Step 1: Write cache tests**

Create `pkg/yeet/update_cache_test.go`:

```go
package yeet

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUpdateCheckCacheRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-check.json")
	cache := updateCheckCache{
		LatestStable: releaseCacheEntry{Tag: "v0.5.13", CheckedAt: time.Unix(10, 0)},
		CatchHosts: map[string]catchObservation{
			"edge-a": {Version: "v0.5.12", ObservedAt: time.Unix(11, 0)},
		},
		LastAdvisory: map[string]time.Time{"v0.5.13": time.Unix(12, 0)},
	}
	if err := writeUpdateCheckCache(path, cache); err != nil {
		t.Fatalf("write cache: %v", err)
	}
	got := readUpdateCheckCache(path)
	if got.LatestStable.Tag != "v0.5.13" || got.CatchHosts["edge-a"].Version != "v0.5.12" {
		t.Fatalf("cache round trip = %#v", got)
	}
}

func TestReadUpdateCheckCacheIgnoresCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-check.json")
	if err := os.WriteFile(path, []byte("{bad"), 0o600); err != nil {
		t.Fatalf("write corrupt cache: %v", err)
	}
	got := readUpdateCheckCache(path)
	if got.LatestStable.Tag != "" || len(got.CatchHosts) != 0 {
		t.Fatalf("corrupt cache = %#v, want empty")
	}
}

func TestCacheReleaseFreshness(t *testing.T) {
	now := time.Unix(1000, 0)
	fresh := releaseCacheEntry{Tag: "v0.5.13", CheckedAt: now.Add(-time.Hour)}
	stale := releaseCacheEntry{Tag: "v0.5.12", CheckedAt: now.Add(-48 * time.Hour)}
	if !fresh.fresh(now, 24*time.Hour) {
		t.Fatal("fresh release entry reported stale")
	}
	if stale.fresh(now, 24*time.Hour) {
		t.Fatal("stale release entry reported fresh")
	}
}
```

- [ ] **Step 2: Run failing cache tests**

Run:

```bash
go test ./pkg/yeet -run 'TestUpdateCheckCache|TestReadUpdateCheckCache|TestCacheReleaseFreshness' -count=1
```

Expected: FAIL because cache types/functions do not exist.

- [ ] **Step 3: Implement cache read/write**

Create `pkg/yeet/update_cache.go`:

```go
package yeet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const updateCheckCacheTTL = 24 * time.Hour

var updateCheckCacheFile = filepath.Join(os.Getenv("HOME"), ".yeet", "update-check.json")

type updateCheckCache struct {
	LatestStable releaseCacheEntry       `json:"latestStable,omitempty"`
	LatestNightly releaseCacheEntry      `json:"latestNightly,omitempty"`
	CatchHosts   map[string]catchObservation `json:"catchHosts,omitempty"`
	LastAdvisory map[string]time.Time    `json:"lastAdvisory,omitempty"`
}

type releaseCacheEntry struct {
	Tag         string        `json:"tag,omitempty"`
	PublishedAt string       `json:"publishedAt,omitempty"`
	CheckedAt   time.Time    `json:"checkedAt,omitempty"`
	Assets      []githubAsset `json:"assets,omitempty"`
}

type catchObservation struct {
	Version    string    `json:"version,omitempty"`
	ObservedAt time.Time `json:"observedAt,omitempty"`
	Error      string    `json:"error,omitempty"`
}

func (e releaseCacheEntry) fresh(now time.Time, ttl time.Duration) bool {
	return e.Tag != "" && !e.CheckedAt.IsZero() && now.Sub(e.CheckedAt) >= 0 && now.Sub(e.CheckedAt) < ttl
}

func readUpdateCheckCache(path string) updateCheckCache {
	raw, err := os.ReadFile(path)
	if err != nil {
		return newUpdateCheckCache()
	}
	var cache updateCheckCache
	if err := json.Unmarshal(raw, &cache); err != nil {
		return newUpdateCheckCache()
	}
	if cache.CatchHosts == nil {
		cache.CatchHosts = map[string]catchObservation{}
	}
	if cache.LastAdvisory == nil {
		cache.LastAdvisory = map[string]time.Time{}
	}
	return cache
}

func newUpdateCheckCache() updateCheckCache {
	return updateCheckCache{
		CatchHosts:   map[string]catchObservation{},
		LastAdvisory: map[string]time.Time{},
	}
}

func writeUpdateCheckCache(path string, cache updateCheckCache) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
```

- [ ] **Step 4: Verify cache**

Run:

```bash
gofmt -w pkg/yeet/update_cache.go pkg/yeet/update_cache_test.go
go test ./pkg/yeet -run 'TestUpdateCheckCache|TestReadUpdateCheckCache|TestCacheReleaseFreshness' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

If commits are authorized for execution:

```bash
git add pkg/yeet/update_cache.go pkg/yeet/update_cache_test.go
git commit -m "yeet: add update check cache"
```

## Task 4: Upgrade Check Model And Host Discovery

**Files:**
- Create: `pkg/yeet/upgrade_check.go`
- Create: `pkg/yeet/upgrade_check_test.go`

- [ ] **Step 1: Write upgrade model tests**

Create `pkg/yeet/upgrade_check_test.go` with focused tests:

```go
package yeet

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/buildinfo"
)

func TestUpgradeKnownHostsUsesCurrentHostWithoutAll(t *testing.T) {
	restore := stubPrefsState(t, prefs{DefaultHost: "current"})
	defer restore()
	loc := &projectConfigLocation{Config: &ProjectConfig{Hosts: []string{"a", "b"}}}
	got := upgradeKnownHosts(loc, false, false)
	if strings.Join(got, ",") != "current" {
		t.Fatalf("hosts = %#v", got)
	}
}

func TestUpgradeKnownHostsAllUsesProjectHosts(t *testing.T) {
	restore := stubPrefsState(t, prefs{DefaultHost: "current"})
	defer restore()
	loc := &projectConfigLocation{Config: &ProjectConfig{Hosts: []string{"b"}, Services: []ServiceEntry{{Name: "svc", Host: "a"}}}}
	got := upgradeKnownHosts(loc, true, false)
	if strings.Join(got, ",") != "a,b,current" {
		t.Fatalf("hosts = %#v", got)
	}
}

func TestBuildUpgradeReportClassifiesStaleAndUnreachable(t *testing.T) {
	oldLatest := fetchUpgradeLatestFn
	oldInfo := fetchUpgradeCatchInfoFn
	t.Cleanup(func() {
		fetchUpgradeLatestFn = oldLatest
		fetchUpgradeCatchInfoFn = oldInfo
	})
	fetchUpgradeLatestFn = func(context.Context, buildinfo.Channel, time.Time) (releaseCacheEntry, error) {
		return releaseCacheEntry{Tag: "v0.5.13"}, nil
	}
	fetchUpgradeCatchInfoFn = func(ctx context.Context, host string) (serverInfo, error) {
		if host == "bad" {
			return serverInfo{}, errors.New("dial timeout")
		}
		return serverInfo{Version: "v0.5.12", InstallUser: "root", InstallHost: host}, nil
	}
	report := buildUpgradeReport(context.Background(), upgradeCheckRequest{
		Local: buildinfo.Info{Version: "v0.5.10", Channel: buildinfo.ChannelStable},
		Hosts: []string{"edge", "bad"},
		Now: time.Unix(100, 0),
	})
	if report.Local.Status != upgradeStatusUpdateAvailable {
		t.Fatalf("local status = %q", report.Local.Status)
	}
	if report.Catch[0].Status != upgradeStatusUpdateAvailable || report.Catch[1].Status != upgradeStatusUnreachable {
		t.Fatalf("catch rows = %#v", report.Catch)
	}
}
```

- [ ] **Step 2: Run failing model tests**

Run:

```bash
go test ./pkg/yeet -run 'TestUpgradeKnownHosts|TestBuildUpgradeReport' -count=1
```

Expected: FAIL because upgrade model functions do not exist.

- [ ] **Step 3: Implement model and discovery**

Create `pkg/yeet/upgrade_check.go` with these exported and package-private shapes:

```go
package yeet

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/yeetrun/yeet/pkg/buildinfo"
)

type upgradeStatus string

const (
	upgradeStatusCurrent         upgradeStatus = "current"
	upgradeStatusUpdateAvailable upgradeStatus = "update available"
	upgradeStatusUnknown         upgradeStatus = "unknown"
	upgradeStatusUnreachable     upgradeStatus = "unreachable"
	upgradeStatusDev             upgradeStatus = "dev build"
)

type upgradeCheckRequest struct {
	Local buildinfo.Info
	Hosts []string
	Now   time.Time
}

type upgradeReport struct {
	Latest releaseCacheEntry `json:"latest"`
	Local  upgradeComponent  `json:"local"`
	Catch  []upgradeComponent `json:"catch,omitempty"`
}

type upgradeComponent struct {
	Name        string        `json:"name"`
	Host        string        `json:"host,omitempty"`
	Current     string        `json:"current,omitempty"`
	Latest      string        `json:"latest,omitempty"`
	Status      upgradeStatus `json:"status"`
	Reason      string        `json:"reason,omitempty"`
	InstallUser string        `json:"installUser,omitempty"`
	InstallHost string        `json:"installHost,omitempty"`
}

var fetchUpgradeLatestFn = fetchUpgradeLatest
var fetchUpgradeCatchInfoFn = fetchUpgradeCatchInfo

func upgradeKnownHosts(cfgLoc *projectConfigLocation, all bool, hostOverrideSet bool) []string {
	if !all || hostOverrideSet || cfgLoc == nil || cfgLoc.Config == nil {
		return []string{Host()}
	}
	seen := map[string]struct{}{Host(): {}}
	for _, host := range cfgLoc.Config.AllHosts() {
		host = strings.TrimSpace(host)
		if host != "" {
			seen[host] = struct{}{}
		}
	}
	hosts := make([]string, 0, len(seen))
	for host := range seen {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return hosts
}

func buildUpgradeReport(ctx context.Context, req upgradeCheckRequest) upgradeReport {
	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}
	latest, latestErr := fetchUpgradeLatestFn(ctx, req.Local.Channel, now)
	report := upgradeReport{Latest: latest}
	report.Local = classifyLocalUpgrade(req.Local, latest, latestErr)
	for _, host := range req.Hosts {
		report.Catch = append(report.Catch, checkCatchUpgrade(ctx, host, latest, latestErr, now))
	}
	return report
}

func classifyLocalUpgrade(local buildinfo.Info, latest releaseCacheEntry, latestErr error) upgradeComponent {
	row := upgradeComponent{Name: "yeet", Current: local.Version, Latest: latest.Tag}
	if latestErr != nil || latest.Tag == "" {
		row.Status = upgradeStatusUnknown
		row.Reason = errorString(latestErr)
		return row
	}
	if !local.IsRelease() {
		row.Status = upgradeStatusDev
		row.Reason = "source/dev builds are not self-updated as release binaries"
		return row
	}
	if buildinfo.CompareSemver(local.Version, latest.Tag) < 0 {
		row.Status = upgradeStatusUpdateAvailable
		return row
	}
	row.Status = upgradeStatusCurrent
	return row
}

func checkCatchUpgrade(ctx context.Context, host string, latest releaseCacheEntry, latestErr error, now time.Time) upgradeComponent {
	row := upgradeComponent{Name: "catch", Host: host, Latest: latest.Tag}
	info, err := fetchUpgradeCatchInfoFn(ctx, host)
	if err != nil {
		row.Status = upgradeStatusUnreachable
		row.Reason = err.Error()
		return row
	}
	row.Current = info.Version
	row.InstallUser = info.InstallUser
	row.InstallHost = info.InstallHost
	if latestErr != nil || latest.Tag == "" {
		row.Status = upgradeStatusUnknown
		row.Reason = errorString(latestErr)
		return row
	}
	if buildinfo.CompareSemver(info.Version, latest.Tag) < 0 {
		row.Status = upgradeStatusUpdateAvailable
		return row
	}
	row.Status = upgradeStatusCurrent
	return row
}

func fetchUpgradeLatest(ctx context.Context, channel buildinfo.Channel, now time.Time) (releaseCacheEntry, error) {
	cache := readUpdateCheckCache(updateCheckCacheFile)
	nightly := channel == buildinfo.ChannelNightly
	entry := cache.LatestStable
	if nightly {
		entry = cache.LatestNightly
	}
	if entry.fresh(now, updateCheckCacheTTL) {
		return entry, nil
	}
	rel, err := fetchGitHubReleaseFn(nightly)
	if err != nil {
		if entry.Tag != "" {
			return entry, nil
		}
		return releaseCacheEntry{}, err
	}
	entry = releaseCacheEntry{Tag: rel.TagName, PublishedAt: rel.PublishedAt, CheckedAt: now, Assets: rel.Assets}
	if nightly {
		cache.LatestNightly = entry
	} else {
		cache.LatestStable = entry
	}
	_ = writeUpdateCheckCache(updateCheckCacheFile, cache)
	return entry, nil
}

func fetchUpgradeCatchInfo(ctx context.Context, host string) (serverInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var info serverInfo
	if err := newRPCClient(host).Call(ctx, "catch.Info", nil, &info); err != nil {
		return serverInfo{}, fmt.Errorf("%s: %w", host, err)
	}
	return info, nil
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
```

- [ ] **Step 4: Verify model and discovery**

Run:

```bash
gofmt -w pkg/yeet/upgrade_check.go pkg/yeet/upgrade_check_test.go
go test ./pkg/yeet -run 'TestUpgradeKnownHosts|TestBuildUpgradeReport' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

If commits are authorized for execution:

```bash
git add pkg/yeet/upgrade_check.go pkg/yeet/upgrade_check_test.go
git commit -m "yeet: model upgrade checks"
```

## Task 5: CLI Parser And Command Wiring

**Files:**
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`
- Modify: `cmd/yeet/cli.go`
- Modify: `cmd/yeet/yeet.go`
- Modify: `cmd/yeet/cli_test.go`

- [ ] **Step 1: Add parser tests for upgrade syntax**

In `pkg/cli/cli_test.go`, add:

```go
func TestParseUpgrade(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want UpgradeFlags
		wantPos []string
	}{
		{name: "check all json", args: []string{"check", "--all", "--json"}, want: UpgradeFlags{All: true, JSON: true}, wantPos: []string{"check"}},
		{name: "host yes", args: []string{"--host", "edge-a", "--yes"}, want: UpgradeFlags{Host: "edge-a", Yes: true}},
		{name: "check flag alias", args: []string{"--check"}, want: UpgradeFlags{Check: true}},
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
```

- [ ] **Step 2: Run failing parser tests**

Run:

```bash
go test ./pkg/cli -run TestParseUpgrade -count=1
```

Expected: FAIL because `UpgradeFlags` and `ParseUpgrade` do not exist.

- [ ] **Step 3: Add parser types and command metadata**

In `pkg/cli/cli.go`, add:

```go
type UpgradeFlags struct {
	All   bool
	Host  string
	JSON  bool
	Yes   bool
	Check bool
}

type upgradeFlagsParsed struct {
	All   bool   `flag:"all"`
	Host  string `flag:"host"`
	JSON  bool   `flag:"json"`
	Yes   bool   `flag:"yes"`
	Check bool   `flag:"check"`
}
```

Add a command info entry:

```go
"upgrade": {
	Name: "upgrade",
	Description: "Check for and install yeet/catch updates",
	Usage: "[check] [--all] [--host=catch-a] [--json] [--yes]",
	Examples: []string{
		"yeet upgrade check",
		"yeet upgrade check --all",
		"yeet upgrade",
		"yeet upgrade --all --yes",
	},
},
```

Add parser:

```go
func ParseUpgrade(args []string) (UpgradeFlags, []string, error) {
	parseArgs, extraArgs := splitArgsAtDoubleDash(args)
	parsed, err := parseFlags[upgradeFlagsParsed](parseArgs)
	if err != nil {
		return UpgradeFlags{}, nil, err
	}
	flags := UpgradeFlags{
		All:   parsed.Flags.All,
		Host:  parsed.Flags.Host,
		JSON:  parsed.Flags.JSON,
		Yes:   parsed.Flags.Yes,
		Check: parsed.Flags.Check,
	}
	argsOut := append(parsed.Args, extraArgs...)
	return flags, argsOut, nil
}
```

- [ ] **Step 4: Wire local command help and handler**

In `cmd/yeet/yeet.go`, add:

```go
handlers["upgrade"] = yeet.HandleUpgrade
```

In `cmd/yeet/cli.go`, add local subcommand help if the shared metadata is not already copied into top-level help:

```go
subcommands["upgrade"] = yargs.SubCommandInfo{
	Name:        "upgrade",
	Description: "Check for and install yeet/catch updates",
	Usage:       "[check] [--all] [--host=catch-a] [--json] [--yes]",
	Examples: []string{
		"yeet upgrade check",
		"yeet upgrade check --all",
		"yeet upgrade --all",
	},
}
```

- [ ] **Step 5: Add CLI wiring tests**

In `cmd/yeet/cli_test.go`, add a help test that asserts `upgrade` appears in `yeet --help`, and a handler-routing test that stubs `yeet.HandleUpgrade` through a package-level handler variable if needed. If `handlers["upgrade"] = yeet.HandleUpgrade` cannot be stubbed directly, introduce:

```go
var handleUpgradeFn = yeet.HandleUpgrade
```

and wire `handlers["upgrade"] = handleUpgradeFn`.

- [ ] **Step 6: Verify parser and CLI wiring**

Run:

```bash
gofmt -w pkg/cli/cli.go pkg/cli/cli_test.go cmd/yeet/cli.go cmd/yeet/yeet.go cmd/yeet/cli_test.go
go test ./pkg/cli ./cmd/yeet -run 'TestParseUpgrade|Upgrade|Help' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

If commits are authorized for execution:

```bash
git add pkg/cli/cli.go pkg/cli/cli_test.go cmd/yeet/cli.go cmd/yeet/yeet.go cmd/yeet/cli_test.go
git commit -m "cmd/yeet: add upgrade command"
```

## Task 6: `yeet upgrade check` Rendering

**Files:**
- Create: `pkg/yeet/upgrade_cmd.go`
- Create: `pkg/yeet/upgrade_cmd_test.go`

- [ ] **Step 1: Write command rendering tests**

Create `pkg/yeet/upgrade_cmd_test.go`:

```go
package yeet

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/buildinfo"
)

func TestRenderUpgradeReportTable(t *testing.T) {
	report := upgradeReport{
		Local: upgradeComponent{Name: "yeet", Current: "v0.5.10", Latest: "v0.5.13", Status: upgradeStatusUpdateAvailable},
		Catch: []upgradeComponent{
			{Name: "catch", Host: "edge-a", Current: "v0.5.13", Latest: "v0.5.13", Status: upgradeStatusCurrent},
			{Name: "catch", Host: "edge-b", Current: "v0.5.8", Latest: "v0.5.13", Status: upgradeStatusUpdateAvailable},
		},
	}
	var out bytes.Buffer
	if err := renderUpgradeReport(&out, report); err != nil {
		t.Fatalf("renderUpgradeReport: %v", err)
	}
	got := out.String()
	for _, want := range []string{"COMPONENT", "yeet", "catch@edge-b", "update available"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestHandleUpgradeCheckJSON(t *testing.T) {
	old := buildUpgradeReportFn
	t.Cleanup(func() { buildUpgradeReportFn = old })
	buildUpgradeReportFn = func(context.Context, upgradeCheckRequest) upgradeReport {
		return upgradeReport{Local: upgradeComponent{Name: "yeet", Current: "v0.5.10", Latest: "v0.5.13", Status: upgradeStatusUpdateAvailable}}
	}
	var out bytes.Buffer
	if err := handleUpgrade(context.Background(), []string{"check", "--json"}, &out, &bytes.Buffer{}, buildinfo.Info{Version: "v0.5.10", Channel: buildinfo.ChannelStable}); err != nil {
		t.Fatalf("handleUpgrade: %v", err)
	}
	var decoded upgradeReport
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("decode json: %v\n%s", err, out.String())
	}
	if decoded.Local.Status != upgradeStatusUpdateAvailable {
		t.Fatalf("decoded = %#v", decoded)
	}
}
```

- [ ] **Step 2: Run failing rendering tests**

Run:

```bash
go test ./pkg/yeet -run 'TestRenderUpgradeReport|TestHandleUpgradeCheckJSON' -count=1
```

Expected: FAIL because rendering/handler functions do not exist.

- [ ] **Step 3: Implement check command path**

Create `pkg/yeet/upgrade_cmd.go` with:

```go
package yeet

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/yeetrun/yeet/pkg/buildinfo"
	"github.com/yeetrun/yeet/pkg/cli"
)

var buildUpgradeReportFn = buildUpgradeReport

func HandleUpgrade(ctx context.Context, args []string) error {
	return handleUpgrade(ctx, args, os.Stdout, os.Stderr, buildinfo.Current())
}

func handleUpgrade(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer, local buildinfo.Info) error {
	if len(args) > 0 && args[0] == "upgrade" {
		args = args[1:]
	}
	flags, pos, err := cli.ParseUpgrade(args)
	if err != nil {
		return err
	}
	if flags.Host != "" {
		SetHostOverride(flags.Host)
	}
	checkOnly := flags.Check || len(pos) > 0 && pos[0] == "check"
	cfgLoc, _ := loadProjectConfigFromCwd()
	_, hostOverrideSet := HostOverride()
	hosts := upgradeKnownHosts(cfgLoc, flags.All, hostOverrideSet)
	report := buildUpgradeReportFn(ctx, upgradeCheckRequest{Local: local, Hosts: hosts, Now: time.Now()})
	if checkOnly {
		if flags.JSON {
			return json.NewEncoder(stdout).Encode(report)
		}
		return renderUpgradeReport(stdout, report)
	}
	return runUpgrade(ctx, stdout, stderr, flags, report)
}

func renderUpgradeReport(w io.Writer, report upgradeReport) error {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(tw, "COMPONENT\tCURRENT\tLATEST\tSTATUS"); err != nil {
		return err
	}
	if err := renderUpgradeRow(tw, report.Local); err != nil {
		return err
	}
	for _, row := range report.Catch {
		if err := renderUpgradeRow(tw, row); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func renderUpgradeRow(w io.Writer, row upgradeComponent) error {
	component := row.Name
	if row.Host != "" {
		component += "@" + row.Host
	}
	status := string(row.Status)
	if row.Reason != "" {
		status += ": " + row.Reason
	}
	_, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", component, row.Current, row.Latest, status)
	return err
}
```

For Task 6 only, define `runUpgrade` as a compile-only guard:

```go
func runUpgrade(context.Context, io.Writer, io.Writer, cli.UpgradeFlags, upgradeReport) error {
	return fmt.Errorf("upgrade apply path is unavailable in this build")
}
```

Task 8 replaces this guard before any broad test or release validation.

- [ ] **Step 4: Verify check command path**

Run:

```bash
gofmt -w pkg/yeet/upgrade_cmd.go pkg/yeet/upgrade_cmd_test.go
go test ./pkg/yeet -run 'TestRenderUpgradeReport|TestHandleUpgradeCheckJSON' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

If commits are authorized for execution:

```bash
git add pkg/yeet/upgrade_cmd.go pkg/yeet/upgrade_cmd_test.go
git commit -m "yeet: render upgrade checks"
```

## Task 7: Local Self-Upgrade Installer

**Files:**
- Create: `pkg/yeet/upgrade_install.go`
- Create: `pkg/yeet/upgrade_install_test.go`

- [ ] **Step 1: Write local install tests**

Create tests that use temp files and fake download/extract/install functions:

```go
package yeet

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yeetrun/yeet/pkg/buildinfo"
)

func TestLocalUpgradePlanRejectsDevBuild(t *testing.T) {
	plan, err := localUpgradePlan(buildinfo.Info{Version: "abc123456", Channel: buildinfo.ChannelDev}, releaseCacheEntry{Tag: "v0.5.13"})
	if err != nil {
		t.Fatalf("localUpgradePlan error: %v", err)
	}
	if plan.Action != localUpgradeActionSkip || plan.Reason == "" {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestLocalUpgradePlanUpdatesStableBehind(t *testing.T) {
	plan, err := localUpgradePlan(buildinfo.Info{Version: "v0.5.10", Channel: buildinfo.ChannelStable}, releaseCacheEntry{Tag: "v0.5.13"})
	if err != nil {
		t.Fatalf("localUpgradePlan error: %v", err)
	}
	if plan.Action != localUpgradeActionUpdate {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestReplaceLocalBinaryAtomic(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "yeet")
	next := filepath.Join(dir, "next")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.WriteFile(next, []byte("new"), 0o755); err != nil {
		t.Fatalf("write next: %v", err)
	}
	if err := replaceLocalBinary(target, next, false); err != nil {
		t.Fatalf("replaceLocalBinary: %v")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "new" {
		t.Fatalf("target = %q", got)
	}
}
```

- [ ] **Step 2: Run failing local installer tests**

Run:

```bash
go test ./pkg/yeet -run 'TestLocalUpgradePlan|TestReplaceLocalBinaryAtomic' -count=1
```

Expected: FAIL because local installer types/functions do not exist.

- [ ] **Step 3: Implement local self-upgrade primitives**

Create `pkg/yeet/upgrade_install.go` with:

```go
package yeet

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/yeetrun/yeet/pkg/buildinfo"
)

type localUpgradeAction string

const (
	localUpgradeActionSkip   localUpgradeAction = "skip"
	localUpgradeActionUpdate localUpgradeAction = "update"
)

type localUpgradePlanResult struct {
	Action localUpgradeAction
	From   string
	To     string
	Reason string
}

func localUpgradePlan(local buildinfo.Info, latest releaseCacheEntry) (localUpgradePlanResult, error) {
	result := localUpgradePlanResult{From: local.Version, To: latest.Tag}
	if latest.Tag == "" {
		result.Action = localUpgradeActionSkip
		result.Reason = "latest release is unknown"
		return result, nil
	}
	if !local.IsRelease() {
		result.Action = localUpgradeActionSkip
		result.Reason = "source/dev builds are not self-updated as release binaries"
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

func upgradeLocalBinary(local buildinfo.Info, latest releaseCacheEntry, yes bool) error {
	plan, err := localUpgradePlan(local, latest)
	if err != nil || plan.Action != localUpgradeActionUpdate {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate current yeet binary: %w", err)
	}
	assetName, assetURL, shaURL, _, err := resolveYeetReleaseAsset(runtime.GOOS, runtime.GOARCH, local.ReleaseChannel() == buildinfo.ChannelNightly)
	if err != nil {
		return err
	}
	tmpDir, err := os.MkdirTemp("", "yeet-upgrade-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	archivePath := filepath.Join(tmpDir, assetName)
	shaPath := archivePath + ".sha256"
	if err := downloadFile(assetURL, archivePath); err != nil {
		return err
	}
	if err := downloadFile(shaURL, shaPath); err != nil {
		return err
	}
	if err := verifySHA256File(archivePath, shaPath); err != nil {
		return err
	}
	bin, err := extractSingleBinary(archivePath, tmpDir)
	if err != nil {
		return err
	}
	return replaceLocalBinary(exe, bin, !fileWritable(exe))
}

func downloadFile(url, path string) error {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, resp.Body)
	return err
}

func verifySHA256File(path, shaPath string) error {
	raw, err := os.ReadFile(shaPath)
	if err != nil {
		return err
	}
	expected := strings.Fields(string(raw))
	if len(expected) == 0 {
		return fmt.Errorf("empty checksum file")
	}
	h := sha256.New()
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != expected[0] {
		return fmt.Errorf("checksum mismatch")
	}
	return nil
}

func extractSingleBinary(archivePath, dstDir string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if hdr.FileInfo().IsDir() {
			continue
		}
		name := filepath.Base(hdr.Name)
		if !strings.HasPrefix(name, "yeet-") {
			continue
		}
		outPath := filepath.Join(dstDir, name)
		out, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(out, tr); err != nil {
			_ = out.Close()
			return "", err
		}
		if err := out.Close(); err != nil {
			return "", err
		}
		return outPath, nil
	}
	return "", fmt.Errorf("archive did not contain yeet binary")
}

func replaceLocalBinary(target, source string, sudo bool) error {
	tmp := filepath.Join(filepath.Dir(target), ".yeet.upgrade.tmp")
	if sudo {
		if err := exec.Command("sudo", "install", "-m", "0755", source, tmp).Run(); err != nil {
			return fmt.Errorf("sudo install replacement: %w", err)
		}
		return exec.Command("sudo", "mv", "-f", tmp, target).Run()
	}
	if err := copyFileMode(source, tmp, 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}

func copyFileMode(source, target string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func fileWritable(path string) bool {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}
```

- [ ] **Step 4: Verify local installer**

Run:

```bash
gofmt -w pkg/yeet/upgrade_install.go pkg/yeet/upgrade_install_test.go
go test ./pkg/yeet -run 'TestLocalUpgradePlan|TestReplaceLocalBinaryAtomic' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

If commits are authorized for execution:

```bash
git add pkg/yeet/upgrade_install.go pkg/yeet/upgrade_install_test.go
git commit -m "yeet: add local self-upgrade installer"
```

## Task 8: Mutating Catch Upgrade Flow

**Files:**
- Modify: `pkg/yeet/upgrade_cmd.go`
- Modify: `pkg/yeet/upgrade_cmd_test.go`

- [ ] **Step 1: Write upgrade execution tests**

Add tests in `pkg/yeet/upgrade_cmd_test.go`:

```go
func TestRunUpgradeRequiresInstallMetadataForStaleCatch(t *testing.T) {
	report := upgradeReport{
		Latest: releaseCacheEntry{Tag: "v0.5.13"},
		Local: upgradeComponent{Name: "yeet", Current: "v0.5.13", Latest: "v0.5.13", Status: upgradeStatusCurrent},
		Catch: []upgradeComponent{{Name: "catch", Host: "edge-a", Current: "v0.5.10", Latest: "v0.5.13", Status: upgradeStatusUpdateAvailable}},
	}
	err := runUpgrade(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, cli.UpgradeFlags{Yes: true}, report)
	if err == nil || !strings.Contains(err.Error(), "missing install host metadata") {
		t.Fatalf("runUpgrade error = %v", err)
	}
}

func TestRunUpgradeUpdatesCatchWithRecordedInstallTarget(t *testing.T) {
	oldInit := initCatchFn
	t.Cleanup(func() { initCatchFn = oldInit })
	var target string
	initCatchFn = func(userAtRemote string, opts initOptions) error {
		target = userAtRemote
		if !opts.fromGithub {
			t.Fatalf("opts = %#v, want from github", opts)
		}
		return nil
	}
	report := upgradeReport{
		Latest: releaseCacheEntry{Tag: "v0.5.13"},
		Local: upgradeComponent{Name: "yeet", Current: "v0.5.13", Latest: "v0.5.13", Status: upgradeStatusCurrent},
		Catch: []upgradeComponent{{Name: "catch", Host: "edge-a", Current: "v0.5.10", Latest: "v0.5.13", Status: upgradeStatusUpdateAvailable, InstallUser: "root", InstallHost: "machine-a"}},
	}
	if err := runUpgrade(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, cli.UpgradeFlags{Yes: true}, report); err != nil {
		t.Fatalf("runUpgrade: %v", err)
	}
	if target != "root@machine-a" {
		t.Fatalf("target = %q", target)
	}
}
```

- [ ] **Step 2: Run failing upgrade execution tests**

Run:

```bash
go test ./pkg/yeet -run 'TestRunUpgrade' -count=1
```

Expected: FAIL until `runUpgrade`, `initCatchFn`, and catch update helpers are implemented.

- [ ] **Step 3: Make `initCatch` injectable**

In `pkg/yeet/init.go`, add:

```go
var initCatchFn = initCatch
```

Change `HandleInit` and `updateCatch` call sites from `initCatch(...)` to `initCatchFn(...)`.

- [ ] **Step 4: Implement `runUpgrade` and catch target resolution**

In `pkg/yeet/upgrade_cmd.go`, replace the temporary `runUpgrade` with:

```go
var upgradeLocalBinaryFn = upgradeLocalBinary

func runUpgrade(ctx context.Context, stdout io.Writer, stderr io.Writer, flags cli.UpgradeFlags, report upgradeReport) error {
	if !flags.Yes {
		ok, err := confirmUpgradePlan(os.Stdin, stdout, report)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(stderr, "Upgrade cancelled")
			return nil
		}
	}
	if report.Local.Status == upgradeStatusUpdateAvailable {
		if err := upgradeLocalBinaryFn(buildinfo.Current(), report.Latest, flags.Yes); err != nil {
			return err
		}
	}
	for _, row := range report.Catch {
		if row.Status != upgradeStatusUpdateAvailable {
			continue
		}
		target, err := catchInstallTarget(row)
		if err != nil {
			return err
		}
		if err := withTemporaryHost(row.Host, func() error {
			return initCatchFn(target, initOptions{fromGithub: true})
		}); err != nil {
			return fmt.Errorf("upgrade catch@%s: %w", row.Host, err)
		}
	}
	return nil
}

func confirmUpgradePlan(stdin io.Reader, stdout io.Writer, report upgradeReport) (bool, error) {
	fmt.Fprintln(stdout, "Upgrade plan:")
	if report.Local.Status == upgradeStatusUpdateAvailable {
		fmt.Fprintf(stdout, "  yeet: %s -> %s\n", report.Local.Current, report.Local.Latest)
	}
	for _, row := range report.Catch {
		if row.Status == upgradeStatusUpdateAvailable {
			fmt.Fprintf(stdout, "  catch@%s: %s -> %s\n", row.Host, row.Current, row.Latest)
		}
	}
	return cmdutil.Confirm(stdin, stdout, "Proceed?")
}

func catchInstallTarget(row upgradeComponent) (string, error) {
	host := strings.TrimSpace(row.InstallHost)
	user := strings.TrimSpace(row.InstallUser)
	if host == "" {
		return "", fmt.Errorf("catch@%s missing install host metadata; run yeet init root@the-ssh-machine-host --from-github", row.Host)
	}
	if strings.Contains(host, "@") {
		return host, nil
	}
	if user == "" {
		return "", fmt.Errorf("catch@%s missing install user metadata; run yeet init root@%s --from-github", row.Host, host)
	}
	return user + "@" + host, nil
}
```

Add imports for `strings`, `github.com/yeetrun/yeet/pkg/buildinfo`, and `github.com/yeetrun/yeet/pkg/cmdutil`.

- [ ] **Step 5: Verify mutating upgrade flow**

Run:

```bash
gofmt -w pkg/yeet/init.go pkg/yeet/upgrade_cmd.go pkg/yeet/upgrade_cmd_test.go
go test ./pkg/yeet -run 'TestRunUpgrade|TestHandleUpgrade|TestRenderUpgradeReport' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

If commits are authorized for execution:

```bash
git add pkg/yeet/init.go pkg/yeet/upgrade_cmd.go pkg/yeet/upgrade_cmd_test.go
git commit -m "yeet: upgrade catch hosts from release assets"
```

## Task 9: Passive Update Advisory

**Files:**
- Create: `pkg/yeet/update_advisory.go`
- Create: `pkg/yeet/update_advisory_test.go`
- Modify: `cmd/yeet/yeet.go`
- Modify: `cmd/yeet/cli_test.go`

- [ ] **Step 1: Write advisory decision tests**

Create `pkg/yeet/update_advisory_test.go`:

```go
package yeet

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/buildinfo"
)

func TestMaybePrintUpdateAdvisorySuppressesJSONAndUpgrade(t *testing.T) {
	cache := newUpdateCheckCache()
	cache.LatestStable = releaseCacheEntry{Tag: "v0.5.13", CheckedAt: time.Unix(100, 0)}
	for _, args := range [][]string{{"upgrade", "check"}, {"status", "--format=json"}, {"version"}} {
		var out bytes.Buffer
		printed := maybePrintUpdateAdvisory(&out, updateAdvisoryRequest{
			Args: args, ExitCode: 0, StdoutTTY: true, StderrTTY: true,
			Local: buildinfo.Info{Version: "v0.5.10", Channel: buildinfo.ChannelStable},
			Cache: cache, Now: time.Unix(100, 0),
		})
		if printed || out.Len() != 0 {
			t.Fatalf("args %#v printed advisory %q", args, out.String())
		}
	}
}

func TestMaybePrintUpdateAdvisoryPrintsLocalUpdate(t *testing.T) {
	cache := newUpdateCheckCache()
	cache.LatestStable = releaseCacheEntry{Tag: "v0.5.13", CheckedAt: time.Unix(100, 0)}
	var out bytes.Buffer
	printed := maybePrintUpdateAdvisory(&out, updateAdvisoryRequest{
		Args: []string{"status"}, ExitCode: 0, StdoutTTY: true, StderrTTY: true,
		Local: buildinfo.Info{Version: "v0.5.10", Channel: buildinfo.ChannelStable},
		Cache: cache, Now: time.Unix(100, 0),
		ProjectHostCount: 3,
	})
	if !printed || !strings.Contains(out.String(), "Update available") || !strings.Contains(out.String(), "yeet upgrade check --all") {
		t.Fatalf("advisory = %q", out.String())
	}
}
```

- [ ] **Step 2: Run failing advisory tests**

Run:

```bash
go test ./pkg/yeet -run TestMaybePrintUpdateAdvisory -count=1
```

Expected: FAIL because advisory logic does not exist.

- [ ] **Step 3: Implement advisory logic**

Create `pkg/yeet/update_advisory.go`:

```go
package yeet

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/yeetrun/yeet/pkg/buildinfo"
)

type updateAdvisoryRequest struct {
	Args             []string
	ExitCode         int
	StdoutTTY        bool
	StderrTTY        bool
	Local            buildinfo.Info
	Cache            updateCheckCache
	Now              time.Time
	ProjectHostCount int
}

func MaybePrintUpdateAdvisory(w io.Writer, args []string, exitCode int, stdoutTTY bool, stderrTTY bool, projectHostCount int) {
	cache := readUpdateCheckCache(updateCheckCacheFile)
	req := updateAdvisoryRequest{
		Args: args, ExitCode: exitCode, StdoutTTY: stdoutTTY, StderrTTY: stderrTTY,
		Local: buildinfo.Current(), Cache: cache, Now: time.Now(), ProjectHostCount: projectHostCount,
	}
	if maybePrintUpdateAdvisory(w, req) {
		latest := cache.LatestStable.Tag
		if req.Local.Channel == buildinfo.ChannelNightly {
			latest = cache.LatestNightly.Tag
		}
		if latest != "" {
			cache.LastAdvisory[latest] = req.Now
			_ = writeUpdateCheckCache(updateCheckCacheFile, cache)
		}
	}
}

func maybePrintUpdateAdvisory(w io.Writer, req updateAdvisoryRequest) bool {
	if suppressUpdateAdvisory(req) {
		return false
	}
	latest := req.Cache.LatestStable.Tag
	if req.Local.Channel == buildinfo.ChannelNightly {
		latest = req.Cache.LatestNightly.Tag
	}
	if latest == "" || !req.Local.IsRelease() || buildinfo.CompareSemver(req.Local.Version, latest) >= 0 {
		return false
	}
	if last := req.Cache.LastAdvisory[latest]; !last.IsZero() && req.Now.Sub(last) < 24*time.Hour {
		return false
	}
	fmt.Fprintf(w, "Update available: yeet %s -> %s.\n", req.Local.Version, latest)
	if req.ProjectHostCount > 1 {
		fmt.Fprintf(w, "Run: yeet upgrade check --all to scan %d project catch hosts.\n", req.ProjectHostCount)
	} else {
		fmt.Fprintln(w, "Run: yeet upgrade check")
	}
	return true
}

func suppressUpdateAdvisory(req updateAdvisoryRequest) bool {
	if req.ExitCode != 0 || !req.StdoutTTY || !req.StderrTTY || os.Getenv("YEET_NO_UPDATE_CHECK") != "" {
		return true
	}
	if len(req.Args) == 0 {
		return true
	}
	switch req.Args[0] {
	case "upgrade", "version", "help", "init", "skirt":
		return true
	}
	for _, arg := range req.Args {
		if arg == "--help" || arg == "-h" || arg == "--help-llm" || arg == "--json" || strings.Contains(arg, "json") {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Hook advisory after command dispatch**

In `cmd/yeet/yeet.go`, wrap the yargs result so the hook runs before return:

```go
exitCode := 0
if err := yargs.RunSubcommandsWithGroups(context.Background(), args, helpConfig, globalFlagsParsed{}, handlers, groups); err != nil {
	yeet.PrintCLIError(os.Stderr, err)
	exitCode = 1
}
projectHostCount := yeet.ProjectHostCountForAdvisory()
yeet.MaybePrintUpdateAdvisory(os.Stderr, args, exitCode, isTerminalFn(int(os.Stdout.Fd())), isTerminalFn(int(os.Stderr.Fd())), projectHostCount)
return exitCode
```

Add `ProjectHostCountForAdvisory` in `pkg/yeet/update_advisory.go`:

```go
func ProjectHostCountForAdvisory() int {
	cfgLoc, err := loadProjectConfigFromCwd()
	if err != nil || cfgLoc == nil || cfgLoc.Config == nil {
		return 1
	}
	if n := len(cfgLoc.Config.AllHosts()); n > 0 {
		return n
	}
	return 1
}
```

- [ ] **Step 5: Verify advisory**

Run:

```bash
gofmt -w pkg/yeet/update_advisory.go pkg/yeet/update_advisory_test.go cmd/yeet/yeet.go cmd/yeet/cli_test.go
go test ./pkg/yeet ./cmd/yeet -run 'TestMaybePrintUpdateAdvisory|Advisory|Run' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

If commits are authorized for execution:

```bash
git add pkg/yeet/update_advisory.go pkg/yeet/update_advisory_test.go cmd/yeet/yeet.go cmd/yeet/cli_test.go
git commit -m "yeet: show passive update advisories"
```

## Task 10: Docs, Quality, And Live Validation

**Files:**
- Modify: `README.md`
- Modify: `website/docs/getting-started/installation.mdx`
- Modify: `website/docs/cli/yeet-cli.mdx`
- Modify: `website/docs/changelog.mdx`

- [ ] **Step 1: Update README upgrade section**

Add a short "Upgrade" section after install:

````md
## Upgrade

Check the local CLI and the selected catch host:

```bash
yeet upgrade check
```

Upgrade both from verified GitHub release assets:

```bash
yeet upgrade
```

In a project with multiple catch hosts in `yeet.toml`, scan and upgrade all of
them explicitly:

```bash
yeet upgrade check --all
yeet upgrade --all
```
````

- [ ] **Step 2: Update website installation docs**

In `website/docs/getting-started/installation.mdx`, add an "Upgrade yeet and catch" section with the same commands and explain:

- passive notices are advisory;
- `yeet upgrade check --all` scans hosts from the current project;
- source builds should use `git pull`/`go build` instead of release self-update.

- [ ] **Step 3: Update website CLI reference**

In `website/docs/cli/yeet-cli.mdx`, add:

````md
## `yeet upgrade`

Check for updates:

```bash
yeet upgrade check
yeet upgrade check --all
yeet upgrade check --json
```

Upgrade:

```bash
yeet upgrade
yeet upgrade --all
yeet upgrade --host catch-a
yeet upgrade --yes
```

`--all` uses the current project's `yeet.toml` plus the default host. It does
not scan the entire tailnet.
````

- [ ] **Step 4: Add changelog entry**

Add the next patch entry in `website/docs/changelog.mdx` with user-facing language:

```md
### v0.5.14

- Added `yeet upgrade check` and `yeet upgrade` so release installs can see and apply yeet/catch updates from verified GitHub assets.
- Interactive commands now show a short update notice when a newer public release is available, without making normal commands depend on GitHub.
```

Before editing the changelog, run `git describe --tags --abbrev=0` and use the next patch version after that tag.

- [ ] **Step 5: Run targeted tests**

Run:

```bash
go test ./pkg/buildinfo ./pkg/cli ./cmd/yeet ./pkg/yeet ./pkg/catch -count=1
```

Expected: PASS.

- [ ] **Step 6: Run full quality gate**

Run:

```bash
go test ./... -count=1
pre-commit run --all-files
```

Expected: both PASS. If this is headed straight into a patch release, also run:

```bash
mise run quality:goal
```

- [ ] **Step 7: Live validation on a disposable host or existing test catch**

Use sanitized environment variables; do not commit secrets or host identifiers:

```bash
export CATCH_HOST="${YEET_UPGRADE_TEST_CATCH_HOST:?set YEET_UPGRADE_TEST_CATCH_HOST}"
go run ./cmd/yeet upgrade check
go run ./cmd/yeet version
```

For a release-style local binary test, install an older release in `.tmp/` and point PATH at it:

```bash
mkdir -p .tmp/upgrade-smoke/bin
YEET_INSTALL_DIR="$(pwd)/.tmp/upgrade-smoke/bin" sh ./install.sh
.tmp/upgrade-smoke/bin/yeet upgrade check
```

If using disposable cloud-provider VMs, pass the token only through `HCLOUD_TOKEN` in the shell and destroy the instance after validation.

- [ ] **Step 8: Commit docs**

Commit website changes inside the submodule first, then root changes. If commits are authorized:

```bash
(cd website && git add docs/getting-started/installation.mdx docs/cli/yeet-cli.mdx docs/changelog.mdx && git commit -m "docs: document yeet upgrades")
git add README.md website
git commit -m "docs: add upgrade workflow"
```

## Final Verification Checklist

- [ ] `go test ./pkg/buildinfo ./pkg/cli ./cmd/yeet ./pkg/yeet ./pkg/catch -count=1` passes.
- [ ] `go test ./... -count=1` passes.
- [ ] `pre-commit run --all-files` passes.
- [ ] `mise run quality:goal` passes before release.
- [ ] `go run ./cmd/yeet upgrade check` reports local/latest/catch state.
- [ ] `go run ./cmd/yeet upgrade check --all` scans project hosts with per-host failures.
- [ ] Passive advisory appears only for interactive, non-JSON, successful commands.
- [ ] Public docs explain upgrade workflow without internal-process language.
