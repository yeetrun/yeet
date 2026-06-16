# Remote VM Image Catalog Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make official VM image families remotely discoverable from `yeet-vm-images` so image releases and new families do not require `yeet` code changes.

**Architecture:** `yeet-vm-images` publishes a trusted `catalog.json` at a stable raw GitHub URL. `catch` fetches and validates that catalog, resolves `vm://...` payloads to catalog families, then fetches each family's stable latest manifest exactly as it already does for image assets. Compiled image-version truth and compiled official-family arrays are removed from `pkg/catch`.

**Tech Stack:** Go, `net/http`, JSON schema-by-struct validation, existing VM manifest/cache code, Bash + `jq` for image-repo validation, GitHub release assets, GitButler (`but`) for `yeet` commits.

---

## File Structure

`/Users/shayne/code/yeet-vm-images/catalog.json`
: New source of truth for official image families.

`/Users/shayne/code/yeet-vm-images/scripts/verify-catalog.sh`
: New validation script for catalog shape, trusted manifest URLs, reachable latest manifests, prefix matching, and required agent metadata.

`/Users/shayne/code/yeet-vm-images/.github/workflows/build-ubuntu-26.04.yml`
`/Users/shayne/code/yeet-vm-images/.github/workflows/build-nixos-26.05.yml`
: Remove stale immutable version defaults and run catalog validation.

`/Users/shayne/code/yeet-vm-images/README.md`
: Document that family metadata lives in `catalog.json`, while latest versions live in stable per-family manifests.

`/Users/shayne/code/yeet/pkg/catch/vm_image_catalog.go`
: New catalog structs, fetch, validation, trusted URL checks, payload/version/default lookup helpers.

`/Users/shayne/code/yeet/pkg/catch/vm_image_catalog_test.go`
: New focused catalog tests with local HTTP fixtures.

`/Users/shayne/code/yeet/pkg/catch/vm_image_registry.go`
: Reduce to payload prefix plus catalog URL compatibility helpers, or remove once references are gone.

`/Users/shayne/code/yeet/pkg/catch/vm_image.go`
: Resolve remote images through the fetched catalog family instead of compiled `officialVMImage`.

`/Users/shayne/code/yeet/pkg/catch/vm_images_cmd.go`
: Load catalog once per VM images command and use it for list, catalog, update, and import default image selection.

`/Users/shayne/code/yeet/pkg/catch/vm_images_prune.go`
: Use catalog version prefixes to classify managed cache entries and ZFS bases.

`/Users/shayne/code/yeet/pkg/catch/*_test.go`
: Replace `defaultVMImageVersion`, `vmUbuntu2604Payload`, `vmNixOS2605Payload`, and `officialVMImages` expectations with catalog fixtures.

`/Users/shayne/code/yeet/tools/vm-image/README.md`
`/Users/shayne/code/yeet/README.md`
: Update operator/developer docs to describe dynamic catalog discovery.

---

### Task 1: Add The Remote Catalog To `yeet-vm-images`

**Files:**
- Create: `/Users/shayne/code/yeet-vm-images/catalog.json`
- Create: `/Users/shayne/code/yeet-vm-images/scripts/verify-catalog.sh`
- Modify: `/Users/shayne/code/yeet-vm-images/.github/workflows/build-ubuntu-26.04.yml`
- Modify: `/Users/shayne/code/yeet-vm-images/.github/workflows/build-nixos-26.05.yml`
- Modify: `/Users/shayne/code/yeet-vm-images/README.md`

- [ ] **Step 1: Create `catalog.json`**

Create `/Users/shayne/code/yeet-vm-images/catalog.json`:

```json
{
  "schema_version": 1,
  "images": [
    {
      "payload": "vm://ubuntu/26.04",
      "name": "Ubuntu 26.04",
      "architecture": "amd64",
      "manifest_url": "https://github.com/yeetrun/yeet-vm-images/releases/download/ubuntu-26.04-amd64-latest/manifest.json",
      "version_prefix": "ubuntu-26.04-amd64-",
      "default_user": "ubuntu",
      "metadata_driver": "ubuntu",
      "capabilities": ["guest_init", "guest_agent", "rsync"],
      "default": true
    },
    {
      "payload": "vm://nixos/26.05",
      "name": "NixOS 26.05",
      "architecture": "amd64",
      "manifest_url": "https://github.com/yeetrun/yeet-vm-images/releases/download/nixos-26.05-amd64-latest/manifest.json",
      "version_prefix": "nixos-26.05-amd64-",
      "default_user": "nixos",
      "metadata_driver": "nixos",
      "capabilities": ["guest_init", "guest_agent", "rsync"]
    }
  ]
}
```

- [ ] **Step 2: Create the catalog verifier**

Create `/Users/shayne/code/yeet-vm-images/scripts/verify-catalog.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

script_source="${BASH_SOURCE[0]}"
script_dir="${script_source%/*}"
if [ "$script_dir" = "$script_source" ]; then
	script_dir="."
fi
repo_root="$(cd "$script_dir/.." && pwd)"
catalog="$repo_root/catalog.json"

require() {
	if ! command -v "$1" >/dev/null 2>&1; then
		echo "missing required command: $1" >&2
		exit 1
	fi
}

for cmd in curl jq; do
	require "$cmd"
done

jq -e '
  .schema_version == 1 and
  (.images | type == "array") and
  (.images | length > 0) and
  all(.images[]; (
    (.payload | type == "string" and startswith("vm://")) and
    (.name | type == "string" and length > 0) and
    (.architecture == "amd64") and
    (.manifest_url | type == "string" and startswith("https://github.com/yeetrun/yeet-vm-images/releases/download/")) and
    (.version_prefix | type == "string" and length > 0 and (contains("/") | not)) and
    (.default_user | type == "string" and test("^[A-Za-z_][A-Za-z0-9_-]*$")) and
    (.metadata_driver == "ubuntu" or .metadata_driver == "nixos") and
    (.capabilities | type == "array")
  ))
' "$catalog" >/dev/null

duplicates="$(
	jq -r '.images[].payload' "$catalog" | sort | uniq -d
)"
if [ -n "$duplicates" ]; then
	echo "duplicate catalog payload(s): $duplicates" >&2
	exit 1
fi

default_count="$(jq '[.images[] | select(.default == true)] | length' "$catalog")"
if [ "$default_count" -gt 1 ]; then
	echo "at most one catalog image may be marked default" >&2
	exit 1
fi

tmp_dir="$(mktemp -d)"
cleanup() {
	rm -rf "$tmp_dir"
}
trap cleanup EXIT

jq -c '.images[]' "$catalog" | while IFS= read -r image; do
	payload="$(jq -r '.payload' <<<"$image")"
	manifest_url="$(jq -r '.manifest_url' <<<"$image")"
	version_prefix="$(jq -r '.version_prefix' <<<"$image")"
	manifest="$tmp_dir/$(tr '/:' '__' <<<"$payload").json"
	curl -fsSL "$manifest_url" -o "$manifest"
	version="$(jq -r '.version // empty' "$manifest")"
	case "$version" in
		"$version_prefix"v[0-9]*)
			;;
		*)
			echo "$payload manifest version $version does not match prefix $version_prefix" >&2
			exit 1
			;;
	esac
	jq -e '
	  (.guest_init == "/usr/local/lib/yeet-vm/yeet-init") and
	  (.guest_agent == "/usr/local/lib/yeet-vm/yeet-agent") and
	  (.guest_agent_sha256 | test("^[0-9a-f]{64}$")) and
	  (.checksums | type == "object")
	' "$manifest" >/dev/null
done
```

- [ ] **Step 3: Make verifier executable**

Run:

```bash
chmod +x /Users/shayne/code/yeet-vm-images/scripts/verify-catalog.sh
```

Expected: command exits with code 0.

- [ ] **Step 4: Add catalog validation to workflows**

In both `/Users/shayne/code/yeet-vm-images/.github/workflows/build-ubuntu-26.04.yml` and `/Users/shayne/code/yeet-vm-images/.github/workflows/build-nixos-26.05.yml`, remove the `default:` line under the `version` input. For Ubuntu, change:

```yaml
      version:
        description: Release/image version to publish.
        required: true
        default: ubuntu-26.04-amd64-v14
```

to:

```yaml
      version:
        description: Release/image version to publish, for example ubuntu-26.04-amd64-v16.
        required: true
```

For NixOS, change:

```yaml
      version:
        description: Release/image version to publish.
        required: true
        default: nixos-26.05-amd64-v13
```

to:

```yaml
      version:
        description: Release/image version to publish, for example nixos-26.05-amd64-v14.
        required: true
```

In the Ubuntu workflow, add `curl` to the dependency install block if it is not already present, then add a step after `Install build dependencies`:

```yaml
      - name: Verify catalog
        run: scripts/verify-catalog.sh
```

In the NixOS workflow, extend the existing `Check Nix definitions` step from:

```yaml
          scripts/verify-nixos-26.05.sh
```

to:

```yaml
          scripts/verify-nixos-26.05.sh
          scripts/verify-catalog.sh
```

- [ ] **Step 5: Document catalog ownership**

In `/Users/shayne/code/yeet-vm-images/README.md`, add this paragraph near the existing official image payload URLs:

```markdown
`catalog.json` is the source of truth for official VM image families. It maps
payloads such as `vm://ubuntu/26.04` to stable latest manifest URLs. Publishing
a new image version only updates the immutable release and the matching
`*-latest` release; edit `catalog.json` only when adding or changing a family.
```

- [ ] **Step 6: Run image repo checks**

Run:

```bash
cd /Users/shayne/code/yeet-vm-images
bash -n scripts/*.sh
scripts/verify-catalog.sh
YEET_SOURCE_PATH=/Users/shayne/code/yeet scripts/verify-nixos-26.05.sh
mise run lint
git status --short --branch
```

Expected:

```text
NixOS 26.05 yeet microVM profile verified
0 / 3 would have been reformatted
## main...origin/main
```

The status output should show only intentional image-repo changes before the commit.

- [ ] **Step 7: Commit image repo changes**

Run:

```bash
cd /Users/shayne/code/yeet-vm-images
git add catalog.json scripts/verify-catalog.sh .github/workflows/build-ubuntu-26.04.yml .github/workflows/build-nixos-26.05.yml README.md
git commit -m "images: publish remote VM catalog"
```

Expected: one image-repo commit containing only catalog, verifier, workflow, and README changes.

---

### Task 2: Add Catalog Fetch And Validation In `yeet`

**Files:**
- Create: `/Users/shayne/code/yeet/pkg/catch/vm_image_catalog.go`
- Create: `/Users/shayne/code/yeet/pkg/catch/vm_image_catalog_test.go`
- Modify: `/Users/shayne/code/yeet/pkg/catch/vm_image.go`
- Modify: `/Users/shayne/code/yeet/pkg/catch/vm_image_registry.go`

- [ ] **Step 1: Write catalog validation tests**

Create `/Users/shayne/code/yeet/pkg/catch/vm_image_catalog_test.go` with tests for fetch, duplicate payloads, untrusted URLs, default lookup, payload lookup, and prefix lookup:

```go
package catch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchVMImageCatalogValidatesAndFindsImages(t *testing.T) {
	catalog := vmImageCatalog{
		SchemaVersion: 1,
		Images: []vmImageCatalogImage{
			{
				Payload:      "vm://ubuntu/26.04",
				Name:         "Ubuntu 26.04",
				Architecture: "amd64",
				ManifestURL:  "https://github.com/yeetrun/yeet-vm-images/releases/download/ubuntu-26.04-amd64-latest/manifest.json",
				VersionPrefix: "ubuntu-26.04-amd64-",
				DefaultUser:  "ubuntu",
				MetadataDriver: "ubuntu",
				Capabilities: []string{"guest_init", "guest_agent", "rsync"},
				Default:      true,
			},
			{
				Payload:      "vm://nixos/26.05",
				Name:         "NixOS 26.05",
				Architecture: "amd64",
				ManifestURL:  "https://github.com/yeetrun/yeet-vm-images/releases/download/nixos-26.05-amd64-latest/manifest.json",
				VersionPrefix: "nixos-26.05-amd64-",
				DefaultUser:  "nixos",
				MetadataDriver: "nixos",
				Capabilities: []string{"guest_init", "guest_agent", "rsync"},
			},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewEncoder(w).Encode(catalog); err != nil {
			t.Fatalf("encode catalog: %v", err)
		}
	}))
	defer server.Close()

	got, err := fetchVMImageCatalogFromURL(context.Background(), server.Client(), server.URL+"/catalog.json", false)
	if err != nil {
		t.Fatalf("fetchVMImageCatalogFromURL: %v", err)
	}
	ubuntu, ok := got.ImageByPayload(" vm://ubuntu/26.04 ")
	if !ok || ubuntu.ManifestURL != catalog.Images[0].ManifestURL {
		t.Fatalf("ubuntu lookup = %#v ok=%v", ubuntu, ok)
	}
	byVersion, ok := got.ImageByVersion("ubuntu-26.04-amd64-v15")
	if !ok || byVersion.Payload != "vm://ubuntu/26.04" {
		t.Fatalf("version lookup = %#v ok=%v", byVersion, ok)
	}
	def, ok := got.DefaultImage()
	if !ok || def.Payload != "vm://ubuntu/26.04" {
		t.Fatalf("default lookup = %#v ok=%v", def, ok)
	}
}

func TestVMImageCatalogRejectsUntrustedManifestURL(t *testing.T) {
	catalog := vmImageCatalog{
		SchemaVersion: 1,
		Images: []vmImageCatalogImage{{
			Payload:      "vm://ubuntu/26.04",
			Name:         "Ubuntu 26.04",
			Architecture: "amd64",
			ManifestURL:  "https://example.com/manifest.json",
			VersionPrefix: "ubuntu-26.04-amd64-",
			DefaultUser:  "ubuntu",
			MetadataDriver: "ubuntu",
		}},
	}
	err := catalog.validate(true)
	if err == nil || !strings.Contains(err.Error(), "untrusted VM image manifest URL") {
		t.Fatalf("validate error = %v", err)
	}
}

func TestVMImageCatalogRejectsDuplicatePayload(t *testing.T) {
	image := vmImageCatalogImage{
		Payload:      "vm://ubuntu/26.04",
		Name:         "Ubuntu 26.04",
		Architecture: "amd64",
		ManifestURL:  "https://github.com/yeetrun/yeet-vm-images/releases/download/ubuntu-26.04-amd64-latest/manifest.json",
		VersionPrefix: "ubuntu-26.04-amd64-",
		DefaultUser:  "ubuntu",
		MetadataDriver: "ubuntu",
	}
	catalog := vmImageCatalog{SchemaVersion: 1, Images: []vmImageCatalogImage{image, image}}
	err := catalog.validate(true)
	if err == nil || !strings.Contains(err.Error(), "duplicate VM image payload") {
		t.Fatalf("validate error = %v", err)
	}
}

func TestVMImageCatalogRejectsMultipleDefaults(t *testing.T) {
	catalog := vmImageCatalog{
		SchemaVersion: 1,
		Images: []vmImageCatalogImage{
			vmImageCatalogTestImage("vm://ubuntu/26.04", "ubuntu-26.04-amd64-", true),
			vmImageCatalogTestImage("vm://nixos/26.05", "nixos-26.05-amd64-", true),
		},
	}
	err := catalog.validate(true)
	if err == nil || !strings.Contains(err.Error(), "multiple default VM images") {
		t.Fatalf("validate error = %v", err)
	}
}

func vmImageCatalogTestImage(payload, prefix string, def bool) vmImageCatalogImage {
	return vmImageCatalogImage{
		Payload:      payload,
		Name:         payload,
		Architecture: "amd64",
		ManifestURL:  "https://github.com/yeetrun/yeet-vm-images/releases/download/" + strings.TrimSuffix(prefix, "-") + "-latest/manifest.json",
		VersionPrefix: prefix,
		DefaultUser:  "ubuntu",
		MetadataDriver: "ubuntu",
		Default:      def,
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
cd /Users/shayne/code/yeet
mise exec -- go test ./pkg/catch -run 'TestFetchVMImageCatalog|TestVMImageCatalogRejects' -count=1
```

Expected: FAIL because `vmImageCatalog`, `vmImageCatalogImage`, and `fetchVMImageCatalogFromURL` are undefined.

- [ ] **Step 3: Implement catalog types and validation**

Create `/Users/shayne/code/yeet/pkg/catch/vm_image_catalog.go`:

```go
package catch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

const defaultVMImageCatalogURL = "https://raw.githubusercontent.com/yeetrun/yeet-vm-images/main/catalog.json"

type vmImageCatalog struct {
	SchemaVersion int                   `json:"schema_version"`
	Images        []vmImageCatalogImage `json:"images"`
}

type vmImageCatalogImage struct {
	Payload        string   `json:"payload"`
	Name           string   `json:"name"`
	Architecture   string   `json:"architecture"`
	ManifestURL    string   `json:"manifest_url"`
	VersionPrefix  string   `json:"version_prefix"`
	DefaultUser    string   `json:"default_user,omitempty"`
	MetadataDriver string   `json:"metadata_driver,omitempty"`
	Capabilities   []string `json:"capabilities,omitempty"`
	Default        bool     `json:"default,omitempty"`
}

func fetchVMImageCatalog(ctx context.Context, client *http.Client) (vmImageCatalog, error) {
	return fetchVMImageCatalogFromURL(ctx, client, defaultVMImageCatalogURL, true)
}

func fetchVMImageCatalogFromURL(ctx context.Context, client *http.Client, rawURL string, requireTrustedURL bool) (vmImageCatalog, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if requireTrustedURL {
		if err := validateTrustedVMImageRepoURL(rawURL, "catalog"); err != nil {
			return vmImageCatalog{}, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(rawURL), nil)
	if err != nil {
		return vmImageCatalog{}, fmt.Errorf("create VM image catalog request: %w", err)
	}
	req.Header.Set("User-Agent", vmImageHTTPUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return vmImageCatalog{}, fmt.Errorf("fetch VM image catalog: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return vmImageCatalog{}, fmt.Errorf("fetch VM image catalog: %s", resp.Status)
	}
	var catalog vmImageCatalog
	if err := json.NewDecoder(resp.Body).Decode(&catalog); err != nil {
		return vmImageCatalog{}, fmt.Errorf("decode VM image catalog: %w", err)
	}
	if err := catalog.validate(requireTrustedURL); err != nil {
		return vmImageCatalog{}, err
	}
	return catalog, nil
}

func (c vmImageCatalog) validate(requireTrustedURL bool) error {
	if c.SchemaVersion != 1 {
		return fmt.Errorf("unsupported VM image catalog schema_version %d", c.SchemaVersion)
	}
	if len(c.Images) == 0 {
		return fmt.Errorf("VM image catalog has no images")
	}
	seen := map[string]struct{}{}
	defaults := 0
	for _, image := range c.Images {
		if err := image.validate(requireTrustedURL); err != nil {
			return err
		}
		payload := strings.TrimSpace(image.Payload)
		if _, ok := seen[payload]; ok {
			return fmt.Errorf("duplicate VM image payload %q in catalog", payload)
		}
		seen[payload] = struct{}{}
		if image.Default {
			defaults++
		}
	}
	if defaults > 1 {
		return fmt.Errorf("multiple default VM images in catalog")
	}
	return nil
}

func (i vmImageCatalogImage) validate(requireTrustedURL bool) error {
	payload := strings.TrimSpace(i.Payload)
	if !strings.HasPrefix(payload, vmImagePayloadPrefix) || strings.TrimPrefix(payload, vmImagePayloadPrefix) == "" {
		return fmt.Errorf("invalid VM image catalog payload %q", i.Payload)
	}
	if strings.TrimSpace(i.Name) == "" {
		return fmt.Errorf("VM image catalog entry %s missing name", payload)
	}
	if strings.TrimSpace(i.Architecture) != "amd64" {
		return fmt.Errorf("VM image catalog entry %s has unsupported architecture %q", payload, i.Architecture)
	}
	if requireTrustedURL {
		if err := validateTrustedVMImageRepoURL(i.ManifestURL, "manifest"); err != nil {
			return err
		}
	}
	prefix := strings.TrimSpace(i.VersionPrefix)
	if prefix == "" || strings.ContainsAny(prefix, `/\`) {
		return fmt.Errorf("VM image catalog entry %s has invalid version_prefix %q", payload, i.VersionPrefix)
	}
	if strings.TrimSpace(i.DefaultUser) != "" && !vmUserPattern.MatchString(strings.TrimSpace(i.DefaultUser)) {
		return fmt.Errorf("VM image catalog entry %s has invalid default_user %q", payload, i.DefaultUser)
	}
	switch strings.TrimSpace(i.MetadataDriver) {
	case "", "ubuntu", "nixos":
	default:
		return fmt.Errorf("VM image catalog entry %s has unsupported metadata_driver %q", payload, i.MetadataDriver)
	}
	return nil
}

func validateTrustedVMImageRepoURL(rawURL, kind string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("parse VM image %s URL: %w", kind, err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("untrusted VM image %s URL %q: scheme must be https", kind, rawURL)
	}
	switch u.Host {
	case "raw.githubusercontent.com":
		if !strings.HasPrefix(strings.TrimPrefix(u.Path, "/"), "yeetrun/yeet-vm-images/") {
			return fmt.Errorf("untrusted VM image %s URL %q", kind, rawURL)
		}
	case "github.com":
		if !strings.HasPrefix(strings.TrimPrefix(u.Path, "/"), "yeetrun/yeet-vm-images/") {
			return fmt.Errorf("untrusted VM image %s URL %q", kind, rawURL)
		}
	default:
		return fmt.Errorf("untrusted VM image %s URL %q", kind, rawURL)
	}
	return nil
}

func (c vmImageCatalog) ImageByPayload(payload string) (vmImageCatalogImage, bool) {
	payload = strings.TrimSpace(payload)
	for _, image := range c.Images {
		if strings.TrimSpace(image.Payload) == payload {
			return image.normalized(), true
		}
	}
	return vmImageCatalogImage{}, false
}

func (c vmImageCatalog) ImageByVersion(version string) (vmImageCatalogImage, bool) {
	version = strings.TrimSpace(version)
	for _, image := range c.Images {
		if image.matchesVersion(version) {
			return image.normalized(), true
		}
	}
	return vmImageCatalogImage{}, false
}

func (c vmImageCatalog) DefaultImage() (vmImageCatalogImage, bool) {
	for _, image := range c.Images {
		if image.Default {
			return image.normalized(), true
		}
	}
	if len(c.Images) == 0 {
		return vmImageCatalogImage{}, false
	}
	images := append([]vmImageCatalogImage(nil), c.Images...)
	sort.SliceStable(images, func(i, j int) bool {
		return strings.TrimSpace(images[i].Payload) < strings.TrimSpace(images[j].Payload)
	})
	return images[0].normalized(), true
}

func (i vmImageCatalogImage) matchesVersion(version string) bool {
	version = strings.TrimSpace(version)
	prefix := strings.TrimSpace(i.VersionPrefix)
	return strings.HasPrefix(version, prefix) && isNumericVersionSuffix(strings.TrimPrefix(version, prefix))
}

func (i vmImageCatalogImage) normalized() vmImageCatalogImage {
	i.Payload = strings.TrimSpace(i.Payload)
	i.Name = strings.TrimSpace(i.Name)
	i.Architecture = strings.TrimSpace(i.Architecture)
	i.ManifestURL = strings.TrimSpace(i.ManifestURL)
	i.VersionPrefix = strings.TrimSpace(i.VersionPrefix)
	i.DefaultUser = strings.TrimSpace(i.DefaultUser)
	i.MetadataDriver = strings.TrimSpace(i.MetadataDriver)
	return i
}

func vmImageCatalogPayloadsForError(catalog vmImageCatalog) string {
	payloads := make([]string, 0, len(catalog.Images))
	for _, image := range catalog.Images {
		payloads = append(payloads, strings.TrimSpace(image.Payload))
	}
	sort.Strings(payloads)
	return strings.Join(payloads, ", ")
}
```

- [ ] **Step 4: Run catalog tests**

Run:

```bash
cd /Users/shayne/code/yeet
mise exec -- go test ./pkg/catch -run 'TestFetchVMImageCatalog|TestVMImageCatalogRejects' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit catalog layer**

Run:

```bash
cd /Users/shayne/code/yeet
but status -fv
but commit codex/remote-vm-image-catalog-design -m "catch: add remote VM image catalog client"
```

Expected: `but status -fv` shows only `pkg/catch/vm_image_catalog.go` and
`pkg/catch/vm_image_catalog_test.go` as unassigned changes before the commit,
then one commit on `codex/remote-vm-image-catalog-design`.

---

### Task 3: Resolve VM Payloads Through The Catalog

**Files:**
- Modify: `/Users/shayne/code/yeet/pkg/catch/vm_image.go`
- Modify: `/Users/shayne/code/yeet/pkg/catch/vm_image_registry.go`
- Modify: `/Users/shayne/code/yeet/pkg/catch/vm_image_test.go`

- [ ] **Step 1: Write payload-resolution tests**

Add tests to `/Users/shayne/code/yeet/pkg/catch/vm_image_test.go`:

```go
func TestResolveVMImagePayloadUsesCatalog(t *testing.T) {
	catalog := vmImageCatalog{SchemaVersion: 1, Images: []vmImageCatalogImage{
		{
			Payload:      "vm://debian/13",
			Name:         "Debian 13",
			Architecture: "amd64",
			ManifestURL:  "https://github.com/yeetrun/yeet-vm-images/releases/download/debian-13-amd64-latest/manifest.json",
			VersionPrefix: "debian-13-amd64-",
			DefaultUser:  "debian",
			MetadataDriver: "ubuntu",
		},
	}}
	source, err := resolveVMImagePayloadFromCatalog(" vm://debian/13 ", catalog)
	if err != nil {
		t.Fatalf("resolveVMImagePayloadFromCatalog: %v", err)
	}
	if source.Kind != vmImageSourceRemote || source.ManifestURL != catalog.Images[0].ManifestURL || source.Family.Payload != "vm://debian/13" {
		t.Fatalf("source = %#v", source)
	}
}

func TestResolveVMImagePayloadRejectsUnknownRemotePayloadWithCatalogList(t *testing.T) {
	catalog := vmImageCatalog{SchemaVersion: 1, Images: []vmImageCatalogImage{
		{Payload: "vm://ubuntu/26.04", Name: "Ubuntu", Architecture: "amd64", ManifestURL: "https://github.com/yeetrun/yeet-vm-images/releases/download/ubuntu-26.04-amd64-latest/manifest.json", VersionPrefix: "ubuntu-26.04-amd64-", DefaultUser: "ubuntu", MetadataDriver: "ubuntu"},
	}}
	_, err := resolveVMImagePayloadFromCatalog("vm://unknown/1", catalog)
	if err == nil || !strings.Contains(err.Error(), "supported: vm://ubuntu/26.04 or imported vm://<name>") {
		t.Fatalf("resolve error = %v", err)
	}
}

func TestInspectRemoteRejectsManifestOutsideCatalogFamily(t *testing.T) {
	contents := vmImageTestContents()
	manifest := vmImageTestManifest("debian-13-amd64-v1", contents)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(manifest)
	}))
	defer server.Close()
	cache := vmImageCache{Root: t.TempDir(), ManifestURL: server.URL + "/manifest.json", Client: server.Client()}
	family := vmImageCatalogImage{Payload: "vm://ubuntu/26.04", ManifestURL: server.URL + "/manifest.json", VersionPrefix: "ubuntu-26.04-amd64-"}
	_, _, err := cache.inspectRemote(context.Background(), "vm://ubuntu/26.04", family)
	if err == nil || !strings.Contains(err.Error(), "does not match catalog version prefix") {
		t.Fatalf("inspect error = %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
cd /Users/shayne/code/yeet
mise exec -- go test ./pkg/catch -run 'TestResolveVMImagePayload|TestInspectRemoteRejects' -count=1
```

Expected: FAIL because `resolveVMImagePayloadFromCatalog` and `source.Family` do not exist, and `inspectRemote` still accepts `officialVMImage`.

- [ ] **Step 3: Update source type and resolver**

In `/Users/shayne/code/yeet/pkg/catch/vm_image.go`, change `vmImageSource` to:

```go
type vmImageSource struct {
	Kind        vmImageSourceKind
	ManifestURL string
	LocalName   string
	Family      vmImageCatalogImage
}
```

Replace `resolveVMImagePayload` with:

```go
func resolveVMImagePayloadFromCatalog(payload string, catalog vmImageCatalog) (vmImageSource, error) {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return vmImageSource{}, fmt.Errorf("VM image payload is required")
	}
	if image, ok := catalog.ImageByPayload(payload); ok {
		return vmImageSource{Kind: vmImageSourceRemote, ManifestURL: image.ManifestURL, Family: image}, nil
	}
	if strings.HasPrefix(payload, vmImagePayloadPrefix) {
		name := strings.TrimPrefix(payload, vmImagePayloadPrefix)
		if err := validateLocalVMImageName(name); err != nil {
			return vmImageSource{}, fmt.Errorf("invalid local VM image name %q: %w", name, err)
		}
		return vmImageSource{Kind: vmImageSourceLocal, LocalName: name}, nil
	}
	return vmImageSource{}, fmt.Errorf("unsupported VM image payload %q (supported: %s or imported vm://<name>)", payload, vmImageCatalogPayloadsForError(catalog))
}
```

Add a catalog fetch helper near the resolver:

```go
func (c vmImageCache) FetchCatalog(ctx context.Context) (vmImageCatalog, error) {
	if strings.TrimSpace(c.CatalogURL) != "" {
		return fetchVMImageCatalogFromURL(ctx, c.httpClient(), c.CatalogURL, false)
	}
	return fetchVMImageCatalog(ctx, c.httpClient())
}
```

Add `CatalogURL` to `vmImageCache`:

```go
type vmImageCache struct {
	Root        string
	ManifestURL string
	CatalogURL  string
	Client      *http.Client
}
```

- [ ] **Step 4: Rewire `ensureVMImageAssetWithProgress` and `Inspect`**

In `/Users/shayne/code/yeet/pkg/catch/vm_image.go`, replace the first lines of `ensureVMImageAssetWithProgress` with:

```go
catalog, err := cache.FetchCatalog(ctx)
if err != nil {
	return vmImageAsset{}, err
}
source, err := resolveVMImagePayloadFromCatalog(payload, catalog)
if err != nil {
	return vmImageAsset{}, err
}
```

Replace `Inspect` with:

```go
func (c vmImageCache) Inspect(ctx context.Context, payload string) (vmImageCacheState, vmImageManifest, error) {
	payload = strings.TrimSpace(payload)
	catalog, err := c.FetchCatalog(ctx)
	if err != nil {
		return vmImageCacheState{}, vmImageManifest{}, err
	}
	source, err := resolveVMImagePayloadFromCatalog(payload, catalog)
	if err != nil {
		return vmImageCacheState{}, vmImageManifest{}, err
	}
	if source.Kind == vmImageSourceLocal {
		return c.inspectLocal(ctx, payload, source.LocalName)
	}
	return c.withManifestURL(source.ManifestURL).inspectRemote(ctx, payload, source.Family)
}
```

Change `inspectRemote` signature and add prefix validation:

```go
func (c vmImageCache) inspectRemote(ctx context.Context, payload string, family vmImageCatalogImage) (vmImageCacheState, vmImageManifest, error) {
	manifestURL := c.manifestURL()
	latestManifest, err := c.fetchManifest(ctx)
	if err != nil {
		return vmImageCacheState{}, vmImageManifest{}, err
	}
	if err := latestManifest.validate(); err != nil {
		return vmImageCacheState{}, vmImageManifest{}, err
	}
	if !family.matchesVersion(latestManifest.Version) {
		return vmImageCacheState{}, vmImageManifest{}, fmt.Errorf("VM image manifest version %q does not match catalog version prefix %q for %s", latestManifest.Version, family.VersionPrefix, payload)
	}
	// keep the existing root/state/cache logic after this point
```

Within the same function, keep the existing `latestCachedVMImageManifest(root, family)` call after changing that helper to accept `vmImageCatalogImage`.

- [ ] **Step 5: Reduce registry constants**

In `/Users/shayne/code/yeet/pkg/catch/vm_image_registry.go`, remove `vmUbuntu2604Payload`, `vmNixOS2605Payload`, `defaultVMImageVersion`, `defaultVMImageManifestURL`, `nixos2605VMImageManifestURL`, `officialVMImage`, `officialVMImages`, `officialVMImageByPayload`, `officialVMImageByVersion`, and `officialVMImagePayloadsForError`.

Keep only:

```go
package catch

import "strings"

const vmImagePayloadPrefix = "vm://"

func reservedVMImageLocalPrefixFromCatalog(name string, catalog vmImageCatalog) bool {
	name = strings.TrimSpace(name)
	for _, image := range catalog.Images {
		officialName := strings.TrimPrefix(strings.TrimSpace(image.Payload), vmImagePayloadPrefix)
		prefix := strings.SplitN(officialName, "/", 2)[0] + "/"
		if prefix != "/" && strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 6: Run focused tests**

Run:

```bash
cd /Users/shayne/code/yeet
mise exec -- go test ./pkg/catch -run 'TestFetchVMImageCatalog|TestVMImageCatalogRejects|TestResolveVMImagePayload|TestInspectRemoteRejects' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit payload resolution**

Run:

```bash
cd /Users/shayne/code/yeet
but status -fv
but commit codex/remote-vm-image-catalog-design -m "catch: resolve VM payloads from remote catalog"
```

Expected: `but status -fv` shows only the Task 3 files as unassigned changes
before the commit, then one commit on `codex/remote-vm-image-catalog-design`.

---

### Task 4: Rewire VM Image Commands To Use Catalog Families

**Files:**
- Modify: `/Users/shayne/code/yeet/pkg/catch/vm_images_cmd.go`
- Modify: `/Users/shayne/code/yeet/pkg/catch/vm_images_cmd_test.go`
- Modify: `/Users/shayne/code/yeet/pkg/catch/vm_images_local.go`
- Modify: `/Users/shayne/code/yeet/pkg/catch/vm_images_local_test.go`

- [ ] **Step 1: Add command tests for dynamic family discovery**

In `/Users/shayne/code/yeet/pkg/catch/vm_images_cmd_test.go`, add a helper:

```go
func stubVMImageCatalog(t *testing.T, catalog vmImageCatalog) {
	t.Helper()
	orig := fetchVMImageCatalogFunc
	fetchVMImageCatalogFunc = func(context.Context, *http.Client) (vmImageCatalog, error) {
		return catalog, nil
	}
	t.Cleanup(func() { fetchVMImageCatalogFunc = orig })
}
```

If Task 2 did not add `fetchVMImageCatalogFunc`, add it in `vm_image_catalog.go`:

```go
var fetchVMImageCatalogFunc = fetchVMImageCatalog
```

Then update `vmImageCache.FetchCatalog` to call `fetchVMImageCatalogFunc`.

Add tests:

```go
func TestVMImagesCmdCatalogUsesRemoteCatalogFamilies(t *testing.T) {
	server := newTestServer(t)
	stubVMImageCatalog(t, vmImageCatalog{SchemaVersion: 1, Images: []vmImageCatalogImage{
		{Payload: "vm://debian/13", Name: "Debian 13", Architecture: "amd64", ManifestURL: "https://github.com/yeetrun/yeet-vm-images/releases/download/debian-13-amd64-latest/manifest.json", VersionPrefix: "debian-13-amd64-", DefaultUser: "debian", MetadataDriver: "ubuntu"},
	}})
	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "json"}, []string{"catalog"}); err != nil {
		t.Fatalf("vmImagesCmdFunc catalog: %v", err)
	}
	var rows []vmImageCatalogRowJSON
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("decode rows: %v", err)
	}
	if len(rows) != 1 || rows[0].Payload != "vm://debian/13" || rows[0].VersionPrefix != "debian-13-amd64-" {
		t.Fatalf("catalog rows = %#v", rows)
	}
}

func TestVMImagesUpdateWithoutArgsUsesCatalogPayloads(t *testing.T) {
	server := newTestServer(t)
	stubVMImageCatalog(t, vmImageCatalog{SchemaVersion: 1, Images: []vmImageCatalogImage{
		{Payload: "vm://debian/13", Name: "Debian 13", Architecture: "amd64", ManifestURL: "https://github.com/yeetrun/yeet-vm-images/releases/download/debian-13-amd64-latest/manifest.json", VersionPrefix: "debian-13-amd64-", DefaultUser: "debian", MetadataDriver: "ubuntu"},
	}})
	seen := []string{}
	origEnsure := vmImageEnsureFunc
	vmImageEnsureFunc = func(ctx context.Context, cache vmImageCache, payload string, ui ProgressUI) (vmImageAsset, error) {
		seen = append(seen, payload)
		return fakeVMImageAssetVersion(t, "debian-13-amd64-v1")
	}
	t.Cleanup(func() { vmImageEnsureFunc = origEnsure })
	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "json"}, []string{"update"}); err != nil {
		t.Fatalf("vmImagesCmdFunc update: %v", err)
	}
	if !reflect.DeepEqual(seen, []string{"vm://debian/13"}) {
		t.Fatalf("updated payloads = %#v", seen)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
cd /Users/shayne/code/yeet
mise exec -- go test ./pkg/catch -run 'TestVMImagesCmdCatalogUsesRemoteCatalogFamilies|TestVMImagesUpdateWithoutArgsUsesCatalogPayloads' -count=1
```

Expected: FAIL until command paths use catalog fixtures.

- [ ] **Step 3: Rewire command paths**

In `/Users/shayne/code/yeet/pkg/catch/vm_images_cmd.go`, update `vmImagesListCmdFunc`:

```go
catalog, err := cache.FetchCatalog(e.vmImagesContext())
if err != nil {
	return err
}
for _, image := range catalog.Images {
	state, _, err := vmImageInspectFunc(e.vmImagesContext(), cache, image.Payload)
	if err != nil {
		return err
	}
	rows = append(rows, vmImageListRowFromCacheState(state))
}
```

Update `vmImagesCatalogCmdFunc`:

```go
catalog, err := e.vmImageCache().FetchCatalog(e.vmImagesContext())
if err != nil {
	return err
}
rows := make([]vmImageCatalogRow, 0, len(catalog.Images))
for _, image := range catalog.Images {
	rows = append(rows, vmImageCatalogRowFromCatalogImage(image))
}
```

Replace `vmImagesUpdatePayloads` with a catalog-aware version:

```go
func vmImagesUpdatePayloads(args []string, catalog vmImageCatalog) ([]string, error) {
	if len(args) == 0 {
		payloads := make([]string, 0, len(catalog.Images))
		for _, image := range catalog.Images {
			payloads = append(payloads, strings.TrimSpace(image.Payload))
		}
		sort.Strings(payloads)
		return payloads, nil
	}
	if len(args) != 1 {
		return nil, fmt.Errorf("%s", vmImagesUsage)
	}
	source, err := resolveVMImagePayloadFromCatalog(args[0], catalog)
	if err != nil {
		return nil, err
	}
	if source.Kind != vmImageSourceRemote {
		return nil, fmt.Errorf("VM image update only supports catalog images: %s", vmImageCatalogPayloadsForError(catalog))
	}
	return []string{strings.TrimSpace(args[0])}, nil
}
```

Update `vmImagesUpdateCmdFunc` to fetch the catalog first and pass it to `vmImagesUpdatePayloads`.

Replace `vmImageCatalogRowFromOfficial` with:

```go
func vmImageCatalogRowFromCatalogImage(image vmImageCatalogImage) vmImageCatalogRow {
	return vmImageCatalogRow{
		Payload:       image.Payload,
		Kind:          "builtin",
		Name:          image.Name,
		DefaultUser:   image.DefaultUser,
		VersionPrefix: image.VersionPrefix,
	}
}
```

Update `vmImageListRowFromCacheState` and `vmImageListRowFromPruneRow` so they do not default blank payloads to Ubuntu. Use the row/state payload as-is, and let tests catch missing payloads.

- [ ] **Step 4: Rewire local image import default managed asset**

In `vmImagesImportCmdFunc`, fetch the catalog and choose the default image:

```go
catalog, err := cache.FetchCatalog(e.vmImagesContext())
if err != nil {
	return err
}
defaultImage, ok := catalog.DefaultImage()
if !ok {
	return fmt.Errorf("VM image catalog has no default image for local import")
}
importer := localVMImageImporter{
	CacheRoot: cache.Root,
	EnsureManagedAsset: func(ctx context.Context) (vmImageAsset, error) {
		return vmImageEnsureFunc(ctx, cache, defaultImage.Payload, e.vmImagesProgressUI(flags))
	},
}
```

Update local image tests to stub a default catalog image instead of expecting Ubuntu constants.

- [ ] **Step 5: Run command tests**

Run:

```bash
cd /Users/shayne/code/yeet
mise exec -- go test ./pkg/catch -run 'TestVMImagesCmd|TestVMImagesUpdate|TestVMImagesImport|TestVMImageCatalog' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit command rewiring**

Run:

```bash
cd /Users/shayne/code/yeet
but status -fv
but commit codex/remote-vm-image-catalog-design -m "catch: use remote catalog for VM image commands"
```

Expected: `but status -fv` shows only the Task 4 files as unassigned changes
before the commit, then one commit on `codex/remote-vm-image-catalog-design`.

---

### Task 5: Rewire Prune And Provision Tests Away From Compiled Versions

**Files:**
- Modify: `/Users/shayne/code/yeet/pkg/catch/vm_images_prune.go`
- Modify: `/Users/shayne/code/yeet/pkg/catch/vm_images_cmd_test.go`
- Modify: `/Users/shayne/code/yeet/pkg/catch/vm_provision_test.go`
- Modify: `/Users/shayne/code/yeet/pkg/catch/vm_resize_test.go`
- Modify: `/Users/shayne/code/yeet/pkg/catch/catch_test.go`

- [ ] **Step 1: Add prune tests for catalog prefixes**

In `/Users/shayne/code/yeet/pkg/catch/vm_images_cmd_test.go`, add:

```go
func TestVMImagesCmdPruneClassifiesCatalogVersionPrefixes(t *testing.T) {
	server := newTestServer(t)
	cacheRoot := filepath.Join(server.cfg.RootDir, "vm-images")
	stubVMImageCatalog(t, vmImageCatalog{SchemaVersion: 1, Images: []vmImageCatalogImage{
		{Payload: "vm://debian/13", Name: "Debian 13", Architecture: "amd64", ManifestURL: "https://github.com/yeetrun/yeet-vm-images/releases/download/debian-13-amd64-latest/manifest.json", VersionPrefix: "debian-13-amd64-", DefaultUser: "debian", MetadataDriver: "ubuntu"},
	}})
	oldDebian := seedCachedVMImage(t, cacheRoot, "debian-13-amd64-v1")
	currentDebian := seedCachedVMImage(t, cacheRoot, "debian-13-amd64-v2")
	seedCachedVMImage(t, cacheRoot, "custom-local-v1")

	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.vmImagesCmdFunc(cli.VMImagesFlags{Format: "json", DryRun: true}, []string{"prune"}); err != nil {
		t.Fatalf("vmImagesCmdFunc prune dry-run: %v", err)
	}
	rows := decodeVMImagePruneRows(t, out.Bytes())
	assertPruneRow(t, rows, "cache", "debian-13-amd64-v1", "prunable", oldDebian)
	assertPruneRow(t, rows, "cache", "debian-13-amd64-v2", "current", currentDebian)
	assertPruneRowPayload(t, rows, "debian-13-amd64-v2", "vm://debian/13")
	for _, row := range rows {
		if row.Version == "custom-local-v1" {
			t.Fatalf("local custom image should not be managed prune row: %#v", row)
		}
	}
}
```

- [ ] **Step 2: Run prune test to verify it fails**

Run:

```bash
cd /Users/shayne/code/yeet
mise exec -- go test ./pkg/catch -run TestVMImagesCmdPruneClassifiesCatalogVersionPrefixes -count=1
```

Expected: FAIL until prune uses catalog prefixes.

- [ ] **Step 3: Rewire prune functions**

In `/Users/shayne/code/yeet/pkg/catch/vm_images_prune.go`, change `planVMImagePrune` to fetch the catalog once:

```go
catalog, err := cache.FetchCatalog(ctx)
if err != nil {
	return nil, err
}
cacheEntries, err := listCachedVMImagePruneEntries(cache.Root, catalog)
```

Change helper signatures:

```go
func listCachedVMImagePruneEntries(root string, catalog vmImageCatalog) ([]cachedVMImagePruneEntry, error)
func parseVMImageZFSBaseDataset(name string, catalog vmImageCatalog) (vmImageZFSBase, bool)
func isManagedVMImagePruneVersion(version string, catalog vmImageCatalog) bool
func currentVMImagePruneVersions(cacheEntries []cachedVMImagePruneEntry, zfsBases []vmImageZFSBase, catalog vmImageCatalog) map[string]string
func vmImageVersionIsCurrent(currentVersions map[string]string, version string, catalog vmImageCatalog) bool
func classifyVMImagePruneRow(kind, version, path string, currentVersions map[string]string, inUse map[string]struct{}, hasClones bool, cloneErr error, catalog vmImageCatalog) vmImagePruneRow
```

Replace every `officialVMImageByVersion(version)` call with `catalog.ImageByVersion(version)`.

Update `destroyVMImageZFSBase` to parse by safe path shape only, not catalog membership:

```go
base, ok := parseVMImageZFSBaseDatasetPath(row.Path)
```

Add:

```go
func parseVMImageZFSBaseDatasetPath(name string) (vmImageZFSBase, bool) {
	const marker = "/yeet/vm-images/"
	if name == "" || !strings.HasSuffix(name, "/root") {
		return vmImageZFSBase{}, false
	}
	idx := strings.Index(name, marker)
	if idx <= 0 {
		return vmImageZFSBase{}, false
	}
	version := strings.TrimSuffix(name[idx+len(marker):], "/root")
	if err := validateVMImageCacheDirName(version); err != nil {
		return vmImageZFSBase{}, false
	}
	parent := strings.TrimSuffix(name, "/root")
	return vmImageZFSBase{Version: version, Dataset: name, Snapshot: name + "@" + version, ParentDataset: parent}, true
}
```

Use catalog membership only when planning rows.

- [ ] **Step 4: Replace test constants**

Run:

```bash
cd /Users/shayne/code/yeet
rg -n 'defaultVMImageVersion|vmUbuntu2604Payload|vmNixOS2605Payload|officialVMImages|officialVMImageBy' pkg/catch
```

For tests that need a concrete official version, use local constants inside the test file:

```go
const testUbuntuVMImageVersion = "ubuntu-26.04-amd64-v99"
const testUbuntuVMPayload = "vm://ubuntu/26.04"
```

For tests that need dynamic catalog behavior, stub a catalog with `stubVMImageCatalog`.

- [ ] **Step 5: Run package tests**

Run:

```bash
cd /Users/shayne/code/yeet
mise exec -- go test ./pkg/catch -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit prune and test cleanup**

Run:

```bash
cd /Users/shayne/code/yeet
but status -fv
but commit codex/remote-vm-image-catalog-design -m "catch: prune VM images using catalog prefixes"
```

Expected: `but status -fv` shows only the Task 5 files as unassigned changes
before the commit, then one commit on `codex/remote-vm-image-catalog-design`.

---

### Task 6: Update Docs And Remove Stale Release Guidance

**Files:**
- Modify: `/Users/shayne/code/yeet/README.md`
- Modify: `/Users/shayne/code/yeet/tools/vm-image/README.md`
- Modify: `/Users/shayne/code/yeet/website/docs/**` only when the current user manual mentions static VM image families or version-bump workflow
- Modify: `/Users/shayne/code/yeet/docs/superpowers/specs/2026-06-16-remote-vm-image-catalog-design.md` only if implementation intentionally diverged.

- [ ] **Step 1: Update `tools/vm-image/README.md`**

Replace the static manifest URL wording with:

```markdown
Official VM image families are discovered from:

`https://raw.githubusercontent.com/yeetrun/yeet-vm-images/main/catalog.json`

Each catalog entry points at a stable latest manifest release. Publishing a new
image version updates the immutable release and the matching `*-latest` release;
`yeet` does not need a code change for version bumps.
```

- [ ] **Step 2: Update root README catalog section**

In `/Users/shayne/code/yeet/README.md`, near the `yeet vm images catalog` examples, add:

```markdown
The official VM catalog is loaded from `yeet-vm-images` at runtime. The catalog
defines supported `vm://...` families, and each family points at a stable latest
manifest. New image versions are picked up through that manifest rather than a
yeet release.
```

- [ ] **Step 3: Search for stale version-bump instructions**

Run:

```bash
cd /Users/shayne/code/yeet
rg -n 'default.*image version|bump.*image version|ubuntu-26\\.04-amd64-v14|nixos-26\\.05-amd64-v13|yeet code change' README.md tools docs website -g '*.md' -g '*.mdx'
```

Update only user-facing or maintainer docs that now contradict remote catalog behavior. Do not edit old historical specs/plans unless they are linked as current guidance.

- [ ] **Step 4: Run docs checks**

Run:

```bash
cd /Users/shayne/code/yeet
git diff --check
```

Expected: no whitespace errors.

- [ ] **Step 5: Commit docs**

Run:

```bash
cd /Users/shayne/code/yeet
but status -fv
but commit codex/remote-vm-image-catalog-design -m "docs: document remote VM image catalog"
```

Expected: `but status -fv` shows only docs changed in Task 6 as unassigned
changes before the commit, then one commit on
`codex/remote-vm-image-catalog-design`.

---

### Task 7: Full Verification, Publish, And Live Smoke

**Files:**
- No new source files expected.

- [ ] **Step 1: Run full yeet tests**

Run:

```bash
cd /Users/shayne/code/yeet
mise exec -- go test ./... -count=1
```

Expected: PASS for all packages.

- [ ] **Step 2: Run quality gate**

Run:

```bash
cd /Users/shayne/code/yeet
mise run quality
```

Expected:

```text
total: ... 81.0% or higher
crap baseline: current=0 baseline=0
0 issues.
```

- [ ] **Step 3: Verify no stale compiled image release truth remains**

Run:

```bash
cd /Users/shayne/code/yeet
rg -n 'defaultVMImageVersion|officialVMImages|officialVMImageByPayload|officialVMImageByVersion|vmUbuntu2604Payload|vmNixOS2605Payload|ubuntu-26\\.04-amd64-v14|nixos-26\\.05-amd64-v13' pkg/catch README.md tools/vm-image docs/agent
```

Expected: no matches in current implementation or current docs. Historical design/plan files under `docs/superpowers` may still mention old releases and do not need rewriting.

- [ ] **Step 4: Push image repo**

Run:

```bash
cd /Users/shayne/code/yeet-vm-images
git status --short --branch
git push origin main
git fetch origin main
git rev-parse main origin/main
```

Expected: `main` and `origin/main` match and include `images: publish remote VM catalog`.

- [ ] **Step 5: Verify remote catalog is reachable**

Run:

```bash
curl -fsSL https://raw.githubusercontent.com/yeetrun/yeet-vm-images/main/catalog.json | jq '.images[].payload'
```

Expected output includes:

```text
"vm://ubuntu/26.04"
"vm://nixos/26.05"
```

- [ ] **Step 6: Install local yeet and catch on pve1**

Run:

```bash
cd /Users/shayne/code/yeet
mise exec -- go install ./cmd/yeet
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet init root@pve1
```

Expected: `yeet init` completes and installs the updated `catch` binary.

- [ ] **Step 7: Validate remote catalog behavior on pve1**

Run:

```bash
cd /Users/shayne/code/yeet
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet vm images catalog --format=json-pretty
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet vm images update vm://ubuntu/26.04 --format=json-pretty
```

Expected:

```json
{
  "payload": "vm://ubuntu/26.04",
  "state": "current",
  "latestVersion": "ubuntu-26.04-amd64-v15"
}
```

The exact JSON may include additional fields, but payload must come from the remote catalog and latest version must come from the latest manifest.

- [ ] **Step 8: Provision a disposable Ubuntu VM and verify agent SSH**

Run with a unique service name:

```bash
cd /Users/shayne/yeet-services
CATCH_HOST=yeet-pve1 yeet run codex-agent-ubuntu vm://ubuntu/26.04 --service-root=flash/yeet/vms/codex-agent-ubuntu --zfs --disk=16g --net=lan --image-policy=update
CATCH_HOST=yeet-pve1 yeet ip codex-agent-ubuntu
CATCH_HOST=yeet-pve1 yeet ssh codex-agent-ubuntu -- hostname
CATCH_HOST=yeet-pve1 yeet rm codex-agent-ubuntu --yes --clean-data --clean-config
```

Expected:

```text
VM codex-agent-ubuntu is running.
one line containing a non-loopback LAN IP
codex-agent-ubuntu
```

Cleanup should remove the disposable service.

- [ ] **Step 9: Commit live-validation fixes**

If live validation required no source changes, skip this step. If it revealed a
source bug, first add or update the focused unit test that reproduces the bug,
run that test to see it fail, apply the smallest source fix, rerun the focused
test, and commit only those test/source files:

```bash
cd /Users/shayne/code/yeet
but status -fv
but commit codex/remote-vm-image-catalog-design -m "catch: fix remote VM catalog validation issue"
```

Expected: `but status -fv` shows only the live-validation test and source fix
as unassigned changes before the commit, then no unassigned changes afterward.

- [ ] **Step 10: Final status**

Run:

```bash
cd /Users/shayne/code/yeet
but status -fv
git status --short --branch
cd /Users/shayne/code/yeet-vm-images
git status --short --branch
```

Expected:

- `yeet` has only the intended GitButler branch active and no unassigned changes.
- `yeet-vm-images` is clean and matches `origin/main`.
