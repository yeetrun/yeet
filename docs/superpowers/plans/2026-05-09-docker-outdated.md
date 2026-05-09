# Docker Outdated Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `yeet docker outdated` as a read-only, status-like report for Docker compose services with upstream image updates.

**Architecture:** The local `yeet` command handles no-service multi-host fan-out and host-aware rendering, matching `yeet status`. Catch owns `docker outdated` parsing and Docker compose inspection over the existing `catchrpc.Exec` path. Docker inspection lives in `pkg/svc` behind injectable command execution so tests can prove the check never pulls, updates, or recreates containers.

**Tech Stack:** Go, `pkg/yargs` CLI parsing, catch RPC exec, Docker CLI, OCI image-spec types, tabwriter table rendering, Go table-driven tests.

---

## File Structure

- Modify `pkg/cli/cli.go`: add `DockerOutdatedFlags`, parser, flag specs, and remote group metadata.
- Modify `pkg/cli/cli_test.go`: cover parsing and metadata.
- Modify `cmd/yeet/cli.go`: route `docker outdated` through the Docker group handler.
- Modify `cmd/yeet/cli_bridge_test.go`: cover scoped and unscoped Docker outdated bridging.
- Modify `cmd/yeet/cli_test.go`: cover `docker@host outdated <svc>` route.
- Create `pkg/svc/docker_outdated.go`: data model, pure parsers, digest comparison, and read-only compose inspection.
- Create `pkg/svc/docker_outdated_test.go`: parser, comparison, and read-only command tests.
- Modify `pkg/catch/tty_ops.go`: add catch-side `docker outdated` dispatch and rendering.
- Modify `pkg/catch/tty_ops_test.go`: cover catch dispatch, scoped non-Docker error, and rendering.
- Modify `pkg/yeet/svc_cmd.go`: intercept local `docker outdated` for table/no-service fan-out.
- Create `pkg/yeet/docker_outdated.go`: host fan-out fetcher and host-aware renderer.
- Create `pkg/yeet/docker_outdated_test.go`: fetch, render, JSON, and error tests.
- Modify `README.md`: mention `yeet docker outdated`.
- Modify `website/docs/cli/yeet-cli.mdx`: add CLI docs for `docker outdated`.
- Modify `website/docs/operations/workflows.mdx`: add Docker update-check workflow note.
- Modify `website/docs/cli/catch-cli.mdx`: list `docker outdated` among catch-supported commands.

---

### Task 1: CLI Metadata and Routing

**Files:**
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`
- Modify: `cmd/yeet/cli.go`
- Modify: `cmd/yeet/cli_bridge_test.go`
- Modify: `cmd/yeet/cli_test.go`

- [ ] **Step 1: Write failing CLI parser and metadata tests**

Add this subtest to the parse test group in `pkg/cli/cli_test.go` near the existing `status` subtest:

```go
t.Run("docker outdated", func(t *testing.T) {
	flags, args, err := ParseDockerOutdated([]string{"--format=json", "svc"})
	if err != nil {
		t.Fatalf("ParseDockerOutdated: %v", err)
	}
	if flags.Format != "json" {
		t.Fatalf("docker outdated format = %q, want json", flags.Format)
	}
	if got := strings.Join(args, " "); got != "svc" {
		t.Fatalf("ParseDockerOutdated args = %q, want svc", got)
	}

	flags, args, err = ParseDockerOutdated(nil)
	if err != nil {
		t.Fatalf("ParseDockerOutdated default: %v", err)
	}
	if flags.Format != "table" || len(args) != 0 {
		t.Fatalf("ParseDockerOutdated default = %#v args=%v, want table no args", flags, args)
	}
})
```

Extend `TestRemoteRegistryMetadata` in `pkg/cli/cli_test.go`:

```go
if reg.Groups["docker"].Commands["outdated"].Info.Name != "outdated" {
	t.Fatalf("registry docker outdated command = %#v", reg.Groups["docker"].Commands["outdated"])
}
if !RemoteGroupFlagSpecs()["docker"]["outdated"]["--format"].ConsumesValue {
	t.Fatal("docker outdated --format should consume a value")
}
```

- [ ] **Step 2: Run parser tests and verify they fail**

Run:

```bash
go test ./pkg/cli -run 'Test(ParseFlags|RemoteRegistryMetadata)' -count=1
```

Expected failure:

```text
undefined: ParseDockerOutdated
```

- [ ] **Step 3: Implement CLI flags and metadata**

In `pkg/cli/cli.go`, add the public and parsed flag types near `StatusFlags`:

```go
type DockerOutdatedFlags struct {
	Format string
}
```

```go
type dockerOutdatedFlagsParsed struct {
	Format string `flag:"format" default:"table"`
}
```

Add `docker outdated` to `remoteGroupInfos["docker"].Commands` without an `ArgsSchema`, because the service argument is optional:

```go
"outdated": {
	Name:        "outdated",
	Description: "Show Docker compose containers with upstream image updates",
	Usage:       "docker outdated [SVC] [--format=table|json|json-pretty]",
	Examples: []string{
		"yeet docker outdated",
		"yeet docker outdated <svc>",
		"yeet docker outdated --format=json",
	},
},
```

Add the flag specs:

```go
"outdated": flagSpecsFromStruct(dockerOutdatedFlagsParsed{}),
```

Add the parser near `ParseStatus`:

```go
func ParseDockerOutdated(args []string) (DockerOutdatedFlags, []string, error) {
	parseArgs, extraArgs := splitArgsAtDoubleDash(args)
	parsed, err := parseFlags[dockerOutdatedFlagsParsed](parseArgs)
	if err != nil {
		return DockerOutdatedFlags{}, nil, err
	}
	flags := DockerOutdatedFlags{Format: parsed.Flags.Format}
	argsOut := append(parsed.Args, extraArgs...)
	return flags, argsOut, nil
}
```

- [ ] **Step 4: Run parser tests and verify they pass**

Run:

```bash
go test ./pkg/cli -run 'Test(ParseFlags|RemoteRegistryMetadata)' -count=1
```

Expected: `ok github.com/yeetrun/yeet/pkg/cli`.

- [ ] **Step 5: Write failing yeet routing tests**

Add tests to `cmd/yeet/cli_bridge_test.go`:

```go
func TestBridgeServiceArgsDockerOutdatedScoped(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	args := []string{"docker", "outdated", "--format=json", "svc-a"}
	service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
	if !ok {
		t.Fatalf("expected to recognize docker outdated group command")
	}
	if service != "svc-a" {
		t.Fatalf("service = %q, want svc-a", service)
	}
	if host != "" {
		t.Fatalf("host = %q, want empty", host)
	}
	if got := strings.Join(bridged, " "); got != "docker outdated --format=json" {
		t.Fatalf("bridged = %q, want docker outdated --format=json", got)
	}
}

func TestBridgeServiceArgsDockerOutdatedNoServiceDoesNotBridge(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	args := []string{"docker", "outdated", "--format=json"}
	service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
	if ok {
		t.Fatalf("expected unscoped command to stay local, got service=%q host=%q bridged=%v", service, host, bridged)
	}
}
```

Extend `TestPrepareCommandRouteShortArgsAndGroupHost` in `cmd/yeet/cli_test.go`:

```go
got = prepareCommandRoute([]string{"docker@catch-a", "outdated", "svc-a"}, "")
if got.host != "catch-a" {
	t.Fatalf("host = %q, want catch-a", got.host)
}
if !reflect.DeepEqual(got.args, []string{"docker", "outdated"}) {
	t.Fatalf("args = %#v, want bridged docker outdated", got.args)
}
if got.service != "svc-a" {
	t.Fatalf("service = %q, want svc-a", got.service)
}
```

Extend `TestGroupHandlersWrapRemoteCommands` in `cmd/yeet/cli_test.go`:

```go
dockerGroup := buildGroupHandlers()["docker"]
if _, ok := dockerGroup.Commands["outdated"]; !ok {
	t.Fatal("docker outdated should be registered in group handlers")
}
```

- [ ] **Step 6: Run routing tests and verify they fail**

Run:

```bash
go test ./cmd/yeet -run 'Test(BridgeServiceArgsDockerOutdated|PrepareCommandRouteShortArgsAndGroupHost|GroupHandlersWrapRemoteCommands)' -count=1
```

Expected failure: `docker outdated` is not present in the Docker group handler or group specs.

- [ ] **Step 7: Implement yeet group routing**

In `cmd/yeet/cli.go`, add `outdated` to the Docker group handler map:

```go
"outdated": handleDockerGroup,
```

No change is needed in `cmd/yeet/cli_bridge.go` because `docker push` remains the only local-only Docker group command.

- [ ] **Step 8: Run routing tests and verify they pass**

Run:

```bash
go test ./cmd/yeet -run 'Test(BridgeServiceArgsDockerOutdated|PrepareCommandRouteShortArgsAndGroupHost|GroupHandlersWrapRemoteCommands)' -count=1
```

Expected: `ok github.com/yeetrun/yeet/cmd/yeet`.

- [ ] **Step 9: Commit CLI routing**

```bash
git add pkg/cli/cli.go pkg/cli/cli_test.go cmd/yeet/cli.go cmd/yeet/cli_bridge_test.go cmd/yeet/cli_test.go
git commit -m "cli: register docker outdated command"
```

---

### Task 2: Docker Outdated Pure Helpers

**Files:**
- Create: `pkg/svc/docker_outdated.go`
- Create: `pkg/svc/docker_outdated_test.go`

- [ ] **Step 1: Write failing parser and comparison tests**

Create `pkg/svc/docker_outdated_test.go`:

```go
package svc

import (
	"strings"
	"testing"
)

func TestParseComposeImages(t *testing.T) {
	got := parseComposeImages("nginx:1.27\n\nredis:7\n")
	want := []string{"nginx:1.27", "redis:7"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("parseComposeImages = %#v, want %#v", got, want)
	}
}

func TestParseComposePSJSONAcceptsArrayAndLines(t *testing.T) {
	arrayInput := []byte(`[
	  {"ID":"c1","Name":"catch-web-app-1","Service":"app","Image":"ghcr.io/acme/app:latest","State":"running"},
	  {"ID":"c2","Name":"catch-web-worker-1","Service":"worker","Image":"ghcr.io/acme/worker:latest","State":"exited"}
	]`)
	got, err := parseComposePSJSON(arrayInput)
	if err != nil {
		t.Fatalf("parse array: %v", err)
	}
	if len(got) != 2 || got[0].ID != "c1" || got[1].Service != "worker" {
		t.Fatalf("array rows = %#v", got)
	}

	lineInput := []byte(`{"ID":"c3","Name":"catch-web-api-1","Service":"api","Image":"ghcr.io/acme/api:latest","State":"running"}` + "\n")
	got, err = parseComposePSJSON(lineInput)
	if err != nil {
		t.Fatalf("parse lines: %v", err)
	}
	if len(got) != 1 || got[0].ID != "c3" {
		t.Fatalf("line rows = %#v", got)
	}
}

func TestSelectRepoDigestForImage(t *testing.T) {
	got := selectRepoDigestForImage([]string{
		"docker.io/library/redis@sha256:redis",
		"ghcr.io/acme/app@sha256:app",
	}, "ghcr.io/acme/app:latest")
	if got != "sha256:app" {
		t.Fatalf("digest = %q, want sha256:app", got)
	}

	if got := selectRepoDigestForImage([]string{"ghcr.io/acme/app@sha256:app"}, "ghcr.io/acme/other:latest"); got != "" {
		t.Fatalf("digest = %q, want empty", got)
	}
}

func TestDockerOutdatedCompare(t *testing.T) {
	tests := []struct {
		name    string
		row     DockerOutdatedRow
		want    DockerOutdatedStatus
		wantWhy string
	}{
		{name: "update", row: DockerOutdatedRow{RunningDigest: "sha256:old", LatestDigest: "sha256:new"}, want: DockerOutdatedUpdateAvailable},
		{name: "current", row: DockerOutdatedRow{RunningDigest: "sha256:same", LatestDigest: "sha256:same"}, want: DockerOutdatedCurrent},
		{name: "missing running", row: DockerOutdatedRow{LatestDigest: "sha256:new"}, want: DockerOutdatedUnknown, wantWhy: "missing running digest"},
		{name: "missing latest", row: DockerOutdatedRow{RunningDigest: "sha256:old"}, want: DockerOutdatedUnknown, wantWhy: "missing latest digest"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compareDockerOutdatedRow(tt.row)
			if got.Status != tt.want {
				t.Fatalf("status = %q, want %q", got.Status, tt.want)
			}
			if tt.wantWhy != "" && got.Reason != tt.wantWhy {
				t.Fatalf("reason = %q, want %q", got.Reason, tt.wantWhy)
			}
		})
	}
}

func TestPlatformDigestFromRawManifest(t *testing.T) {
	const amdDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const armDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	index := []byte(`{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"` + amdDigest + `","platform":{"architecture":"amd64","os":"linux"}},{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"` + armDigest + `","platform":{"architecture":"arm64","os":"linux"}}]}`)
	got, ok, err := platformDigestFromRawManifest(index, "linux", "amd64")
	if err != nil {
		t.Fatalf("platform digest: %v", err)
	}
	if !ok || got != amdDigest {
		t.Fatalf("platform digest = %q ok=%v, want %s true", got, ok, amdDigest)
	}

	manifest := []byte(`{"schemaVersion":2,"config":{"digest":"sha256:config"}}`)
	got, ok, err = platformDigestFromRawManifest(manifest, "linux", "amd64")
	if err != nil {
		t.Fatalf("single manifest digest: %v", err)
	}
	if want := digestFromManifestBytes(manifest); !ok || got != want {
		t.Fatalf("single manifest digest = %q ok=%v, want %q true", got, ok, want)
	}
}

func TestInternalRegistryImage(t *testing.T) {
	if !isInternalRegistryImage("catchit.dev/svc/app:run") {
		t.Fatal("catchit.dev image should be internal")
	}
	if isInternalRegistryImage("ghcr.io/acme/app:latest") {
		t.Fatal("ghcr.io image should not be internal")
	}
}
```

- [ ] **Step 2: Run helper tests and verify they fail**

Run:

```bash
go test ./pkg/svc -run 'Test(ParseComposeImages|ParseComposePSJSON|SelectRepoDigest|DockerOutdatedCompare|PlatformDigest|InternalRegistryImage)' -count=1
```

Expected failure: undefined helper types and functions.

- [ ] **Step 3: Implement data model and pure helpers**

Create `pkg/svc/docker_outdated.go`:

```go
package svc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type DockerOutdatedStatus string

const (
	DockerOutdatedUpdateAvailable DockerOutdatedStatus = "update available"
	DockerOutdatedCurrent         DockerOutdatedStatus = "current"
	DockerOutdatedUnknown         DockerOutdatedStatus = "unknown"
	DockerOutdatedError           DockerOutdatedStatus = "error"
)

type DockerOutdatedOptions struct {
	IncludeInternal bool
}

type DockerOutdatedRow struct {
	ServiceName    string               `json:"serviceName"`
	ContainerID    string               `json:"containerID,omitempty"`
	ContainerName  string               `json:"containerName"`
	Image          string               `json:"image"`
	RunningDigest  string               `json:"runningDigest,omitempty"`
	LatestDigest   string               `json:"latestDigest,omitempty"`
	Status         DockerOutdatedStatus `json:"status"`
	Reason         string               `json:"reason,omitempty"`
}

type dockerComposePSRow struct {
	ID      string `json:"ID"`
	Name    string `json:"Name"`
	Service string `json:"Service"`
	Image   string `json:"Image"`
	State   string `json:"State"`
}

type dockerImageInspectRow struct {
	ID           string   `json:"Id"`
	RepoDigests  []string `json:"RepoDigests"`
	Architecture string   `json:"Architecture"`
	OS           string   `json:"Os"`
}

func parseComposeImages(output string) []string {
	lines := splitNonEmptyLines(output)
	images := make([]string, 0, len(lines))
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		image := strings.TrimSpace(line)
		if image == "" {
			continue
		}
		if _, ok := seen[image]; ok {
			continue
		}
		seen[image] = struct{}{}
		images = append(images, image)
	}
	return images
}

func parseComposePSJSON(output []byte) ([]dockerComposePSRow, error) {
	output = bytes.TrimSpace(output)
	if len(output) == 0 {
		return nil, nil
	}
	if output[0] == '[' {
		var rows []dockerComposePSRow
		if err := json.Unmarshal(output, &rows); err != nil {
			return nil, fmt.Errorf("parse docker compose ps JSON array: %w", err)
		}
		return rows, nil
	}
	dec := json.NewDecoder(bytes.NewReader(output))
	rows := make([]dockerComposePSRow, 0)
	for {
		var row dockerComposePSRow
		if err := dec.Decode(&row); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("parse docker compose ps JSON line: %w", err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func selectRepoDigestForImage(repoDigests []string, image string) string {
	repo := imageRepositoryName(image)
	for _, candidate := range repoDigests {
		candidateRepo, digest, ok := strings.Cut(candidate, "@")
		if !ok {
			continue
		}
		if imageRepositoryName(candidateRepo) == repo {
			return digest
		}
	}
	return ""
}

func imageRepositoryName(image string) string {
	image = strings.TrimSpace(image)
	if image == "" {
		return ""
	}
	if repo, _, ok := strings.Cut(image, "@"); ok {
		image = repo
	}
	lastSlash := strings.LastIndex(image, "/")
	lastColon := strings.LastIndex(image, ":")
	if lastColon > lastSlash {
		image = image[:lastColon]
	}
	if strings.Count(image, "/") == 0 {
		return "docker.io/library/" + image
	}
	if strings.Count(image, "/") == 1 && !strings.ContainsAny(strings.SplitN(image, "/", 2)[0], ".:") {
		return "docker.io/" + image
	}
	return image
}

func isInternalRegistryImage(image string) bool {
	return strings.HasPrefix(imageRepositoryName(image), InternalRegistryHost+"/")
}

func compareDockerOutdatedRow(row DockerOutdatedRow) DockerOutdatedRow {
	if row.RunningDigest == "" {
		row.Status = DockerOutdatedUnknown
		row.Reason = "missing running digest"
		return row
	}
	if row.LatestDigest == "" {
		row.Status = DockerOutdatedUnknown
		row.Reason = "missing latest digest"
		return row
	}
	if row.RunningDigest == row.LatestDigest {
		row.Status = DockerOutdatedCurrent
		return row
	}
	row.Status = DockerOutdatedUpdateAvailable
	return row
}

func digestFromManifestBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func platformDigestFromRawManifest(raw []byte, osName, arch string) (string, bool, error) {
	var index struct {
		MediaType  string              `json:"mediaType"`
		Manifests []ocispec.Descriptor `json:"manifests"`
	}
	if err := json.Unmarshal(raw, &index); err != nil {
		return "", false, fmt.Errorf("parse manifest: %w", err)
	}
	if len(index.Manifests) == 0 {
		return digestFromManifestBytes(raw), true, nil
	}
	for _, desc := range index.Manifests {
		if desc.Platform == nil {
			continue
		}
		if desc.Platform.OS == osName && desc.Platform.Architecture == arch {
			return desc.Digest.String(), true, nil
		}
	}
	return "", false, nil
}

func (s *DockerComposeService) readonlyComposeCommandContext(ctx context.Context, args ...string) (*exec.Cmd, error) {
	dockerPath, err := DockerCmd()
	if err != nil {
		return nil, err
	}
	nargs, err := s.composeCommandArgs()
	if err != nil {
		return nil, err
	}
	args = append(nargs, args...)
	cmd := s.newDockerCommand(ctx, dockerPath, args...)
	cmd.Dir = s.DataDir
	return cmd, nil
}
```

- [ ] **Step 4: Run helper tests and verify they pass**

Run:

```bash
go test ./pkg/svc -run 'Test(ParseComposeImages|ParseComposePSJSON|SelectRepoDigest|DockerOutdatedCompare|PlatformDigest|InternalRegistryImage)' -count=1
```

Expected: `ok github.com/yeetrun/yeet/pkg/svc`.

- [ ] **Step 5: Commit pure helpers**

```bash
git add pkg/svc/docker_outdated.go pkg/svc/docker_outdated_test.go
git commit -m "svc: add docker outdated helpers"
```

---

### Task 3: Read-Only Docker Compose Inspection

**Files:**
- Modify: `pkg/svc/docker_outdated.go`
- Modify: `pkg/svc/docker_outdated_test.go`

- [ ] **Step 1: Write failing read-only inspection tests**

Append these helpers and tests to `pkg/svc/docker_outdated_test.go`:

Update the import block to include every package used by the new tests:

```go
import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)
```

```go
func TestDockerComposeOutdatedUsesReadOnlyCommands(t *testing.T) {
	tmp := t.TempDir()
	compose := writeDockerOutdatedFile(t, tmp, "compose.yml", "services:\n  app:\n    image: ghcr.io/acme/app:2\n")
	fakeBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeBin, "docker"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake docker binary: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	service := &DockerComposeService{
		Name:    "web",
		DataDir: tmp,
		cfg: &testDockerOutdatedServiceConfig{
			composePath: compose,
		}.service(),
	}

	var commands [][]string
	service.NewCmdContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		full := append([]string{name}, args...)
		commands = append(commands, full)
		switch {
		case hasOrderedArgs(args, "compose", "config", "--images"):
			return fakeDockerOutputCmd(t, "ghcr.io/acme/app:2\n")
		case hasOrderedArgs(args, "compose", "ps", "--format=json"):
			return fakeDockerOutputCmd(t, `[{"ID":"cid","Name":"catch-web-app-1","Service":"app","Image":"ghcr.io/acme/app:1","State":"running"}]`)
		case len(args) >= 2 && args[0] == "inspect" && args[1] == "cid":
			return fakeDockerOutputCmd(t, `[{"Image":"sha256:imageid"}]`)
		case len(args) >= 2 && args[0] == "image" && args[1] == "inspect":
			return fakeDockerOutputCmd(t, `[{"Id":"sha256:imageid","RepoDigests":["ghcr.io/acme/app@sha256:old"],"Architecture":"amd64","Os":"linux"}]`)
		case len(args) >= 4 && args[0] == "buildx" && args[1] == "imagetools" && args[2] == "inspect":
			if args[3] != "ghcr.io/acme/app:2" {
				t.Fatalf("upstream image = %q, want compose-declared ghcr.io/acme/app:2", args[3])
			}
			return fakeDockerOutputCmd(t, `{"schemaVersion":2}`)
		default:
			return fakeDockerOutputCmd(t, "")
		}
	}

	rows, err := service.Outdated(context.Background(), DockerOutdatedOptions{})
	if err != nil {
		t.Fatalf("Outdated: %v", err)
	}
	if len(rows) != 1 || rows[0].Status != DockerOutdatedUpdateAvailable {
		t.Fatalf("rows = %#v, want one update row", rows)
	}
	if rows[0].Image != "ghcr.io/acme/app:2" {
		t.Fatalf("row image = %q, want compose-declared ghcr.io/acme/app:2", rows[0].Image)
	}
	for _, command := range commands {
		joined := strings.Join(command, " ")
		for _, forbidden := range []string{" pull", " up", " update"} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("forbidden docker command %q in commands %#v", forbidden, commands)
			}
		}
	}
}

func TestDockerComposeOutdatedInternalImageFiltering(t *testing.T) {
	row := DockerOutdatedRow{ServiceName: "web", ContainerName: "app", Image: InternalRegistryHost + "/web/app:run"}
	if got := filterDockerOutdatedRow(row, DockerOutdatedOptions{}); got != nil {
		t.Fatalf("internal image should be skipped for host-wide output: %#v", got)
	}
	got := filterDockerOutdatedRow(row, DockerOutdatedOptions{IncludeInternal: true})
	if got == nil || got.Status != DockerOutdatedUnknown || got.Reason != "internal image" {
		t.Fatalf("scoped internal image row = %#v, want unknown internal image", got)
	}
}

func TestDockerComposeOutdatedSkipsInternalImagesBeforeRegistry(t *testing.T) {
	tmp := t.TempDir()
	compose := writeDockerOutdatedFile(t, tmp, "compose.yml", "services:\n  app:\n    image: "+InternalRegistryHost+"/web/app:run\n")
	fakeBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeBin, "docker"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake docker binary: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	service := &DockerComposeService{
		Name:    "web",
		DataDir: tmp,
		cfg: &testDockerOutdatedServiceConfig{
			composePath: compose,
		}.service(),
	}
	service.NewCmdContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		switch {
		case hasOrderedArgs(args, "compose", "config", "--images"):
			return fakeDockerOutputCmd(t, InternalRegistryHost+"/web/app:run\n")
		case hasOrderedArgs(args, "compose", "ps", "--format=json"):
			return fakeDockerOutputCmd(t, `[{"ID":"cid","Name":"catch-web-app-1","Service":"app","Image":"`+InternalRegistryHost+`/web/app:run","State":"running"}]`)
		default:
			t.Fatalf("internal image should not trigger docker inspect or registry lookup: docker %v", args)
			return fakeDockerOutputCmd(t, "")
		}
	}

	rows, err := service.Outdated(context.Background(), DockerOutdatedOptions{})
	if err != nil {
		t.Fatalf("Outdated host-wide: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("host-wide internal rows = %#v, want none", rows)
	}

	rows, err = service.Outdated(context.Background(), DockerOutdatedOptions{IncludeInternal: true})
	if err != nil {
		t.Fatalf("Outdated scoped: %v", err)
	}
	if len(rows) != 1 || rows[0].Status != DockerOutdatedUnknown || rows[0].Reason != "internal image" {
		t.Fatalf("scoped internal rows = %#v, want one unknown internal image", rows)
	}
}
```

Add test helpers in the same file:

```go
type testDockerOutdatedServiceConfig struct {
	composePath string
}

func (c *testDockerOutdatedServiceConfig) service() *db.Service {
	return &db.Service{
		Name:             "web",
		ServiceType:      db.ServiceTypeDockerCompose,
		Generation:       1,
		LatestGeneration: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactDockerComposeFile: {
				Refs: map[db.ArtifactRef]string{db.Gen(1): c.composePath},
			},
		},
	}
}

func writeDockerOutdatedFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func fakeDockerOutputCmd(t *testing.T, output string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestDockerOutdatedFakeCommand", "--", output)
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
	return cmd
}

func TestDockerOutdatedFakeCommand(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	for i, arg := range args {
		if arg == "--" && i+1 < len(args) {
			fmt.Print(args[i+1])
			os.Exit(0)
		}
	}
	os.Exit(0)
}

func hasOrderedArgs(args []string, want ...string) bool {
	if len(want) == 0 {
		return true
	}
	next := 0
	for _, arg := range args {
		if arg != want[next] {
			continue
		}
		next++
		if next == len(want) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run read-only tests and verify they fail**

Run:

```bash
go test ./pkg/svc -run 'TestDockerComposeOutdated' -count=1
```

Expected failure: `DockerComposeService.Outdated` and `filterDockerOutdatedRow` are undefined.

- [ ] **Step 3: Implement read-only inspection**

Extend `pkg/svc/docker_outdated.go` with command helpers:

```go
func (s *DockerComposeService) Outdated(ctx context.Context, opts DockerOutdatedOptions) ([]DockerOutdatedRow, error) {
	images, err := s.composeDeclaredImages(ctx)
	if err != nil {
		return nil, err
	}
	declared := make(map[string]string, len(images))
	for _, image := range images {
		repo := imageRepositoryName(image)
		if _, ok := declared[repo]; !ok {
			declared[repo] = image
		}
	}

	containers, err := s.composeContainers(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]DockerOutdatedRow, 0, len(containers))
	for _, container := range containers {
		if container.State != "" && container.State != "running" {
			continue
		}
		base := dockerOutdatedRowForComposeContainer(s.Name, container)
		if isInternalRegistryImage(base.Image) {
			filtered := filterDockerOutdatedRow(base, opts)
			if filtered != nil {
				rows = append(rows, *filtered)
			}
			continue
		}
		row := s.outdatedRowForContainer(ctx, container, declared)
		filtered := filterDockerOutdatedRow(row, opts)
		if filtered != nil {
			rows = append(rows, *filtered)
		}
	}
	return rows, nil
}

func (s *DockerComposeService) composeDeclaredImages(ctx context.Context) ([]string, error) {
	out, err := s.readonlyComposeOutput(ctx, "config", "--images")
	if err != nil {
		return nil, fmt.Errorf("docker compose config --images: %w", err)
	}
	return parseComposeImages(string(out)), nil
}

func (s *DockerComposeService) composeContainers(ctx context.Context) ([]dockerComposePSRow, error) {
	out, err := s.readonlyComposeOutput(ctx, "ps", "--format=json")
	if err != nil {
		return nil, fmt.Errorf("docker compose ps --format=json: %w", err)
	}
	return parseComposePSJSON(out)
}

func (s *DockerComposeService) readonlyComposeOutput(ctx context.Context, args ...string) ([]byte, error) {
	cmd, err := s.readonlyComposeCommandContext(ctx, args...)
	if err != nil {
		return nil, err
	}
	return cmd.Output()
}

func (s *DockerComposeService) dockerOutput(ctx context.Context, args ...string) ([]byte, error) {
	dockerPath, err := DockerCmd()
	if err != nil {
		return nil, err
	}
	cmd := s.newDockerCommand(ctx, dockerPath, args...)
	cmd.Dir = s.DataDir
	return cmd.Output()
}
```

Add row resolution:

```go
func (s *DockerComposeService) outdatedRowForContainer(ctx context.Context, container dockerComposePSRow, declared map[string]string) DockerOutdatedRow {
	row := dockerOutdatedRowForComposeContainer(s.Name, container)
	declaredImage, ok := declared[imageRepositoryName(container.Image)]
	if !ok {
		row.Status = DockerOutdatedUnknown
		row.Reason = "image not declared by compose config"
		return row
	}
	row.Image = declaredImage
	inspect, err := s.inspectContainerImage(ctx, container.ID, declaredImage)
	if err != nil {
		row.Status = DockerOutdatedError
		row.Reason = err.Error()
		return row
	}
	row.RunningDigest = inspect.runningDigest
	latest, err := s.latestImageDigest(ctx, declaredImage, inspect.os, inspect.architecture)
	if err != nil {
		row.Status = DockerOutdatedError
		row.Reason = err.Error()
		return row
	}
	row.LatestDigest = latest
	return compareDockerOutdatedRow(row)
}

func dockerOutdatedRowForComposeContainer(serviceName string, container dockerComposePSRow) DockerOutdatedRow {
	row := DockerOutdatedRow{
		ServiceName:   serviceName,
		ContainerID:   container.ID,
		ContainerName: container.Service,
		Image:         container.Image,
	}
	if row.ContainerName == "" {
		row.ContainerName = container.Name
	}
	return row
}
```

Add the inspect data and filter helpers:

```go
type runningImageInspect struct {
	runningDigest string
	os            string
	architecture  string
}

func filterDockerOutdatedRow(row DockerOutdatedRow, opts DockerOutdatedOptions) *DockerOutdatedRow {
	if isInternalRegistryImage(row.Image) {
		if !opts.IncludeInternal {
			return nil
		}
		row.Status = DockerOutdatedUnknown
		row.Reason = "internal image"
		return &row
	}
	if row.Status == "" {
		row = compareDockerOutdatedRow(row)
	}
	if row.Status == DockerOutdatedCurrent {
		return nil
	}
	return &row
}
```

Implement `inspectContainerImage` and `latestImageDigest` using the existing `dockerOutput` helper:

```go
func (s *DockerComposeService) inspectContainerImage(ctx context.Context, containerID, image string) (runningImageInspect, error) {
	out, err := s.dockerOutput(ctx, "inspect", containerID)
	if err != nil {
		return runningImageInspect{}, fmt.Errorf("docker inspect container: %w", err)
	}
	var containers []struct {
		Image string `json:"Image"`
	}
	if err := json.Unmarshal(out, &containers); err != nil {
		return runningImageInspect{}, fmt.Errorf("parse docker inspect container: %w", err)
	}
	if len(containers) == 0 {
		return runningImageInspect{}, fmt.Errorf("parse docker inspect container: empty result")
	}
	imageOut, err := s.dockerOutput(ctx, "image", "inspect", containers[0].Image)
	if err != nil {
		return runningImageInspect{}, fmt.Errorf("docker image inspect: %w", err)
	}
	var images []dockerImageInspectRow
	if err := json.Unmarshal(imageOut, &images); err != nil {
		return runningImageInspect{}, fmt.Errorf("parse docker image inspect: %w", err)
	}
	if len(images) == 0 {
		return runningImageInspect{}, fmt.Errorf("parse docker image inspect: empty result")
	}
	return runningImageInspect{
		runningDigest: selectRepoDigestForImage(images[0].RepoDigests, image),
		os:            images[0].OS,
		architecture:  images[0].Architecture,
	}, nil
}

func (s *DockerComposeService) latestImageDigest(ctx context.Context, image, osName, arch string) (string, error) {
	if pinned, digest, ok := strings.Cut(image, "@"); ok && strings.TrimSpace(pinned) != "" && strings.HasPrefix(digest, "sha256:") {
		return digest, nil
	}
	raw, err := s.dockerOutput(ctx, "buildx", "imagetools", "inspect", image, "--raw")
	if err != nil {
		return "", fmt.Errorf("inspect upstream image: %w", err)
	}
	digest, ok, err := platformDigestFromRawManifest(raw, osName, arch)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("no upstream digest for platform %s/%s", osName, arch)
	}
	return digest, nil
}
```

- [ ] **Step 4: Run read-only tests and verify they pass**

Run:

```bash
go test ./pkg/svc -run 'TestDockerComposeOutdated' -count=1
```

Expected: `ok github.com/yeetrun/yeet/pkg/svc`.

- [ ] **Step 5: Run all service tests**

Run:

```bash
go test ./pkg/svc -count=1
```

Expected: `ok github.com/yeetrun/yeet/pkg/svc`.

- [ ] **Step 6: Commit read-only inspection**

```bash
git add pkg/svc/docker_outdated.go pkg/svc/docker_outdated_test.go
git commit -m "svc: inspect outdated docker images"
```

---

### Task 4: Catch-Side `docker outdated`

**Files:**
- Modify: `pkg/catch/tty_exec.go`
- Modify: `pkg/catch/tty_ops.go`
- Modify: `pkg/catch/tty_ops_test.go`
- Modify: `pkg/catch/catch.go`

- [ ] **Step 1: Write failing catch command tests**

In `pkg/catch/tty_ops_test.go`, add these imports to the existing import block:

```go
"encoding/json"

"github.com/yeetrun/yeet/pkg/svc"
```

Add these tests to `pkg/catch/tty_ops_test.go`:

```go
func TestDockerCmdFuncOutdatedParsesFormat(t *testing.T) {
	server := newTestServer(t)
	addTestService(t, server, "web", db.ServiceTypeDockerCompose)
	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		sn:  "web",
		rw:  &out,
		dockerOutdatedFunc: func(ctx context.Context, service string, opts svc.DockerOutdatedOptions) ([]svc.DockerOutdatedRow, error) {
			if service != "web" {
				t.Fatalf("service = %q, want web", service)
			}
			if !opts.IncludeInternal {
				t.Fatal("scoped docker outdated should include internal-image unknown rows")
			}
			return []svc.DockerOutdatedRow{{
				ServiceName:   "web",
				ContainerName: "app",
				Image:         "ghcr.io/acme/app:latest",
				RunningDigest: "sha256:old",
				LatestDigest:  "sha256:new",
				Status:        svc.DockerOutdatedUpdateAvailable,
			}}, nil
		},
	}
	if err := execer.dockerCmdFunc([]string{"outdated", "--format=json"}); err != nil {
		t.Fatalf("dockerCmdFunc outdated: %v", err)
	}
	var rows []svc.DockerOutdatedRow
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("outdated JSON invalid: %v\n%s", err, out.String())
	}
	if len(rows) != 1 || rows[0].ServiceName != "web" {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestDockerOutdatedCmdFuncRejectsNonDockerScopedService(t *testing.T) {
	server := newTestServer(t)
	addTestService(t, server, "worker", db.ServiceTypeSystemd)
	execer := &ttyExecer{ctx: context.Background(), s: server, sn: "worker", rw: &bytes.Buffer{}}
	err := execer.dockerCmdFunc([]string{"outdated"})
	if err == nil || !strings.Contains(err.Error(), `service "worker" is not a docker compose service`) {
		t.Fatalf("docker outdated non-docker error = %v", err)
	}
}

func TestRenderDockerOutdatedRowsTableAndJSON(t *testing.T) {
	rows := []svc.DockerOutdatedRow{
		{ServiceName: "web", ContainerName: "app", Image: "ghcr.io/acme/app:latest", RunningDigest: "sha256:old", LatestDigest: "sha256:new", Status: svc.DockerOutdatedUpdateAvailable},
	}
	var out bytes.Buffer
	if err := renderDockerOutdatedRows(&out, "table", rows); err != nil {
		t.Fatalf("render table: %v", err)
	}
	if !strings.Contains(out.String(), "SERVICE") || !strings.Contains(out.String(), "update available") {
		t.Fatalf("table output = %q", out.String())
	}

	out.Reset()
	if err := renderDockerOutdatedRows(&out, "json", rows); err != nil {
		t.Fatalf("render json: %v", err)
	}
	var decoded []svc.DockerOutdatedRow
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("json output invalid: %v", err)
	}
	if len(decoded) != 1 || decoded[0].ServiceName != "web" {
		t.Fatalf("decoded rows = %#v", decoded)
	}
}
```

- [ ] **Step 2: Run catch tests and verify they fail**

Run:

```bash
go test ./pkg/catch -run 'Test(DockerCmdFuncOutdated|RenderDockerOutdatedRows)' -count=1
```

Expected failure: `ttyExecer.dockerOutdatedFunc`, `renderDockerOutdatedRows`, or `docker outdated` is undefined.

- [ ] **Step 3: Add server methods for scoped and host-wide checks**

In `pkg/catch/catch.go`, add methods near `DockerComposeStatuses`:

```go
func (s *Server) DockerComposeOutdated(ctx context.Context, sn string, opts svc.DockerOutdatedOptions) ([]svc.DockerOutdatedRow, error) {
	service, err := s.dockerComposeService(sn)
	if err != nil {
		return nil, fmt.Errorf("failed to get service: %w", err)
	}
	return service.Outdated(ctx, opts)
}

func (s *Server) DockerComposeOutdatedAll(ctx context.Context) ([]svc.DockerOutdatedRow, error) {
	dv, err := s.getDB()
	if err != nil {
		return nil, fmt.Errorf("failed to get db: %v", err)
	}
	rows := make([]svc.DockerOutdatedRow, 0)
	for _, sn := range serviceNamesByType(dv.AsStruct().Services, db.ServiceTypeDockerCompose) {
		serviceRows, err := s.DockerComposeOutdated(ctx, sn, svc.DockerOutdatedOptions{})
		if err != nil {
			rows = append(rows, svc.DockerOutdatedRow{
				ServiceName: sn,
				Status:      svc.DockerOutdatedError,
				Reason:      err.Error(),
			})
			continue
		}
		rows = append(rows, serviceRows...)
	}
	sortDockerOutdatedRows(rows)
	return rows, nil
}
```

In `pkg/catch/tty_ops.go`, add this standard-library import:

```go
"slices"
```

Add the sorter in `pkg/catch/tty_ops.go`:

```go
func sortDockerOutdatedRows(rows []svc.DockerOutdatedRow) {
	slices.SortFunc(rows, func(a, b svc.DockerOutdatedRow) int {
		if a.ServiceName != b.ServiceName {
			return strings.Compare(a.ServiceName, b.ServiceName)
		}
		if a.ContainerName != b.ContainerName {
			return strings.Compare(a.ContainerName, b.ContainerName)
		}
		return strings.Compare(a.Image, b.Image)
	})
}
```

- [ ] **Step 4: Implement catch command dispatch and rendering**

In `pkg/catch/tty_exec.go`, add test hooks to `ttyExecer`:

```go
dockerOutdatedFunc    func(context.Context, string, svc.DockerOutdatedOptions) ([]svc.DockerOutdatedRow, error)
dockerOutdatedAllFunc func(context.Context) ([]svc.DockerOutdatedRow, error)
```

In `pkg/catch/tty_ops.go`, change `dockerCmdFunc` so only `pull` and `update` reject extra args before dispatch:

```go
func (e *ttyExecer) dockerCmdFunc(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("docker requires a subcommand")
	}
	subcmd := args[0]
	args = args[1:]
	switch subcmd {
	case "pull":
		if len(args) > 0 {
			return fmt.Errorf("docker pull takes no arguments")
		}
		return e.dockerPullCmdFunc()
	case "update":
		if len(args) > 0 {
			return fmt.Errorf("docker update takes no arguments")
		}
		return e.dockerUpdateCmdFunc()
	case "outdated":
		flags, remaining, err := cli.ParseDockerOutdated(args)
		if err != nil {
			return err
		}
		if len(remaining) > 0 {
			return fmt.Errorf("docker outdated takes no remote arguments")
		}
		return e.dockerOutdatedCmdFunc(flags)
	default:
		return fmt.Errorf("unknown docker command %q", subcmd)
	}
}
```

Add the command and renderer:

```go
func (e *ttyExecer) dockerOutdatedCmdFunc(flags cli.DockerOutdatedFlags) error {
	var rows []svc.DockerOutdatedRow
	var err error
	if e.sn == SystemService {
		if e.dockerOutdatedAllFunc != nil {
			rows, err = e.dockerOutdatedAllFunc(e.ctx)
		} else {
			rows, err = e.s.DockerComposeOutdatedAll(e.ctx)
		}
	} else {
		st, typeErr := e.s.serviceType(e.sn)
		if typeErr != nil {
			return fmt.Errorf("failed to get service type: %w", typeErr)
		}
		if st != db.ServiceTypeDockerCompose {
			return fmt.Errorf("service %q is not a docker compose service", e.sn)
		}
		if e.dockerOutdatedFunc != nil {
			rows, err = e.dockerOutdatedFunc(e.ctx, e.sn, svc.DockerOutdatedOptions{IncludeInternal: true})
		} else {
			rows, err = e.s.DockerComposeOutdated(e.ctx, e.sn, svc.DockerOutdatedOptions{IncludeInternal: true})
		}
	}
	if err != nil {
		return err
	}
	sortDockerOutdatedRows(rows)
	return renderDockerOutdatedRows(e.rw, flags.Format, rows)
}

func renderDockerOutdatedRows(w io.Writer, formatOut string, rows []svc.DockerOutdatedRow) error {
	switch strings.TrimSpace(formatOut) {
	case "json":
		return json.NewEncoder(w).Encode(rows)
	case "json-pretty":
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(rows)
	case "", "table":
		return renderDockerOutdatedTable(w, rows)
	default:
		return fmt.Errorf("unsupported docker outdated format %q", formatOut)
	}
}

func renderDockerOutdatedTable(w io.Writer, rows []svc.DockerOutdatedRow) error {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(tw, "SERVICE\tCONTAINER\tIMAGE\tRUNNING\tLATEST\tSTATUS"); err != nil {
		return err
	}
	for _, row := range rows {
		status := string(row.Status)
		if row.Reason != "" {
			status += ": " + row.Reason
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			row.ServiceName,
			dash(row.ContainerName),
			row.Image,
			dash(row.RunningDigest),
			dash(row.LatestDigest),
			status,
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func dash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}
```

- [ ] **Step 5: Run catch tests and verify they pass**

Run:

```bash
go test ./pkg/catch -run 'Test(DockerCmdFuncOutdated|RenderDockerOutdatedRows|DockerCmdFuncRejectsInvalidForms|DockerUpdateCmdFuncFailsBeforeDockerForNonComposeService)' -count=1
```

Expected: `ok github.com/yeetrun/yeet/pkg/catch`.

- [ ] **Step 6: Commit catch command**

```bash
git add pkg/catch/catch.go pkg/catch/tty_exec.go pkg/catch/tty_ops.go pkg/catch/tty_ops_test.go
git commit -m "catch: add docker outdated command"
```

---

### Task 5: Local Multi-Host Fetch and Rendering

**Files:**
- Modify: `pkg/yeet/svc_cmd.go`
- Create: `pkg/yeet/docker_outdated.go`
- Create: `pkg/yeet/docker_outdated_test.go`

- [ ] **Step 1: Write failing local rendering and fetch tests**

Create `pkg/yeet/docker_outdated_test.go`:

```go
package yeet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/cli"
)

func preserveDockerOutdatedGlobals(t *testing.T) {
	t.Helper()
	preserveSvcCommandGlobals(t)
	oldFetchDockerOutdated := fetchDockerOutdatedForHostFn
	t.Cleanup(func() {
		fetchDockerOutdatedForHostFn = oldFetchDockerOutdated
	})
}

func TestFetchDockerOutdatedForHost(t *testing.T) {
	preserveDockerOutdatedGlobals(t)
	execRemoteOutputFn = func(ctx context.Context, host string, service string, args []string, stdin io.Reader) ([]byte, error) {
		if host != "host-a" || service != systemServiceName || !reflect.DeepEqual(args, []string{"docker", "outdated", "--format=json"}) {
			t.Fatalf("execRemoteOutputFn = (%q, %q, %#v)", host, service, args)
		}
		return []byte(`[{"serviceName":"web","containerName":"app","image":"ghcr.io/acme/app:latest","runningDigest":"sha256:old","latestDigest":"sha256:new","status":"update available"}]`), nil
	}
	rows, err := fetchDockerOutdatedForHost(context.Background(), "host-a", "", cli.DockerOutdatedFlags{})
	if err != nil {
		t.Fatalf("fetchDockerOutdatedForHost: %v", err)
	}
	if len(rows) != 1 || rows[0].ServiceName != "web" {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestDockerOutdatedMultiHostJSON(t *testing.T) {
	preserveDockerOutdatedGlobals(t)
	fetchDockerOutdatedForHostFn = func(ctx context.Context, host string, service string, flags cli.DockerOutdatedFlags) ([]dockerOutdatedRow, error) {
		return []dockerOutdatedRow{{ServiceName: "svc-" + host, ContainerName: "app", Status: "update available"}}, nil
	}
	out, err := captureSvcStdout(t, func() error {
		return dockerOutdatedMultiHost(context.Background(), []string{"host-b", "host-a"}, "", cli.DockerOutdatedFlags{Format: "json-pretty"})
	})
	if err != nil {
		t.Fatalf("dockerOutdatedMultiHost: %v", err)
	}
	var decoded []dockerOutdatedHostData
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if len(decoded) != 2 || decoded[0].Host != "host-a" || decoded[1].Host != "host-b" {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestRenderDockerOutdatedTables(t *testing.T) {
	results := []dockerOutdatedHostData{{
		Host: "host-a",
		Rows: []dockerOutdatedRow{{
			ServiceName:   "web",
			ContainerName: "app",
			Image:         "ghcr.io/acme/app:latest",
			RunningDigest: "sha256:old",
			LatestDigest:  "sha256:new",
			Status:        "update available",
		}},
	}}
	var out bytes.Buffer
	if err := renderDockerOutdatedTables(&out, results); err != nil {
		t.Fatalf("render: %v", err)
	}
	got := out.String()
	for _, want := range []string{"SERVICE", "HOST", "web", "host-a", "update available"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestDockerOutdatedMultiHostReturnsFetchError(t *testing.T) {
	preserveDockerOutdatedGlobals(t)
	fetchDockerOutdatedForHostFn = func(ctx context.Context, host string, service string, flags cli.DockerOutdatedFlags) ([]dockerOutdatedRow, error) {
		return nil, errors.New("host failed")
	}
	if err := dockerOutdatedMultiHost(context.Background(), []string{"host-a"}, "", cli.DockerOutdatedFlags{}); err == nil || !strings.Contains(err.Error(), "host failed") {
		t.Fatalf("error = %v, want host failed", err)
	}
}

func TestDockerOutdatedMultiHostRejectsInvalidFormat(t *testing.T) {
	preserveDockerOutdatedGlobals(t)
	fetchDockerOutdatedForHostFn = func(ctx context.Context, host string, service string, flags cli.DockerOutdatedFlags) ([]dockerOutdatedRow, error) {
		t.Fatalf("invalid format should fail before fetching host %q", host)
		return nil, nil
	}
	err := dockerOutdatedMultiHost(context.Background(), []string{"host-a"}, "", cli.DockerOutdatedFlags{Format: "xml"})
	if err == nil || !strings.Contains(err.Error(), `unsupported docker outdated format "xml"`) {
		t.Fatalf("invalid format error = %v", err)
	}
}

func TestHandleSvcCommandDockerOutdatedInterceptsLocalTable(t *testing.T) {
	preserveDockerOutdatedGlobals(t)
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		t.Fatalf("docker outdated table should be handled locally, got remote exec service=%q args=%v", service, args)
		return nil
	}
	called := false
	fetchDockerOutdatedForHostFn = func(ctx context.Context, host string, service string, flags cli.DockerOutdatedFlags) ([]dockerOutdatedRow, error) {
		called = true
		if service != "" {
			t.Fatalf("unscoped docker outdated service = %q, want empty", service)
		}
		return []dockerOutdatedRow{{ServiceName: "web", ContainerName: "app", Status: "update available"}}, nil
	}
	_, err := captureSvcStdout(t, func() error {
		return handleSvcCommand(context.Background(), svcCommandRequest{
			Command: svcCommand{Name: "docker", Args: []string{"outdated"}, RawArgs: []string{"docker", "outdated"}},
			Config:  nil,
		})
	})
	if err != nil {
		t.Fatalf("handleSvcCommand docker outdated: %v", err)
	}
	if !called {
		t.Fatal("fetchDockerOutdatedForHostFn was not called")
	}
}
```

- [ ] **Step 2: Run local tests and verify they fail**

Run:

```bash
go test ./pkg/yeet -run 'Test(FetchDockerOutdated|DockerOutdatedMultiHost|RenderDockerOutdated|HandleSvcCommandDockerOutdated)' -count=1
```

Expected failure: local Docker outdated types and functions are undefined.

- [ ] **Step 3: Implement local fetch and rendering**

Create `pkg/yeet/docker_outdated.go`:

```go
package yeet

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/yeetrun/yeet/pkg/cli"
)

type dockerOutdatedHostData struct {
	Host string              `json:"host"`
	Rows []dockerOutdatedRow `json:"containers"`
}

type dockerOutdatedRow struct {
	ServiceName   string `json:"serviceName"`
	ContainerID   string `json:"containerID,omitempty"`
	ContainerName string `json:"containerName"`
	Image         string `json:"image"`
	RunningDigest string `json:"runningDigest,omitempty"`
	LatestDigest  string `json:"latestDigest,omitempty"`
	Status        string `json:"status"`
	Reason        string `json:"reason,omitempty"`
}

var fetchDockerOutdatedForHostFn = fetchDockerOutdatedForHost

func handleDockerOutdatedCommand(ctx context.Context, args []string, cfgLoc *projectConfigLocation, hostOverrideSet bool) error {
	if len(args) > 0 && args[0] == "outdated" {
		args = args[1:]
	}
	flags, remaining, err := cli.ParseDockerOutdated(args)
	if err != nil {
		return err
	}
	if len(remaining) > 0 {
		return fmt.Errorf("docker outdated takes at most one service argument")
	}
	format, err := dockerOutdatedFormat(flags.Format)
	if err != nil {
		return err
	}
	if serviceOverride != "" && format == "table" {
		return dockerOutdatedMultiHost(ctx, []string{Host()}, serviceOverride, flags)
	}
	if serviceOverride == "" {
		return dockerOutdatedMultiHost(ctx, statusHosts(cfgLoc, hostOverrideSet), "", flags)
	}
	return execRemoteFn(ctx, serviceOverride, append([]string{"docker", "outdated"}, args...), nil, true)
}

func dockerOutdatedFormat(format string) (string, error) {
	format = strings.TrimSpace(format)
	if format == "" {
		format = "table"
	}
	switch format {
	case "table", "json", "json-pretty":
		return format, nil
	default:
		return "", fmt.Errorf("unsupported docker outdated format %q", format)
	}
}

func dockerOutdatedMultiHost(ctx context.Context, hosts []string, service string, flags cli.DockerOutdatedFlags) error {
	format, err := dockerOutdatedFormat(flags.Format)
	if err != nil {
		return err
	}
	type hostResult struct {
		host string
		rows []dockerOutdatedRow
		err  error
	}
	ch := make(chan hostResult, len(hosts))
	for _, host := range hosts {
		host := host
		go func() {
			rows, err := fetchDockerOutdatedForHostFn(ctx, host, service, flags)
			ch <- hostResult{host: host, rows: rows, err: err}
		}()
	}
	results := make([]dockerOutdatedHostData, 0, len(hosts))
	for range hosts {
		res := <-ch
		if res.err != nil {
			return res.err
		}
		results = append(results, dockerOutdatedHostData{Host: res.host, Rows: res.rows})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Host < results[j].Host })
	if format == "json" || format == "json-pretty" {
		enc := json.NewEncoder(os.Stdout)
		if format == "json-pretty" {
			enc.SetIndent("", "  ")
		}
		return enc.Encode(results)
	}
	return renderDockerOutdatedTables(os.Stdout, results)
}

func fetchDockerOutdatedForHost(ctx context.Context, host string, service string, _ cli.DockerOutdatedFlags) ([]dockerOutdatedRow, error) {
	targetService := service
	if targetService == "" {
		targetService = systemServiceName
	}
	payload, err := execRemoteOutputFn(ctx, host, targetService, []string{"docker", "outdated", "--format=json"}, nil)
	if err != nil {
		return nil, fmt.Errorf("docker outdated on %s: %w", host, err)
	}
	var rows []dockerOutdatedRow
	if err := json.Unmarshal(payload, &rows); err != nil {
		return nil, fmt.Errorf("docker outdated on %s returned invalid JSON: %w", host, err)
	}
	return rows, nil
}

func renderDockerOutdatedTables(w io.Writer, results []dockerOutdatedHostData) error {
	rows := flattenDockerOutdatedRows(results)
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(tw, "SERVICE\tHOST\tCONTAINER\tIMAGE\tRUNNING\tLATEST\tSTATUS"); err != nil {
		return err
	}
	for _, row := range rows {
		status := row.Status
		if row.Reason != "" {
			status += ": " + row.Reason
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			row.ServiceName,
			row.Host,
			dash(row.ContainerName),
			row.Image,
			dash(row.RunningDigest),
			dash(row.LatestDigest),
			status,
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

type dockerOutdatedRenderRow struct {
	Host string
	dockerOutdatedRow
}

func flattenDockerOutdatedRows(results []dockerOutdatedHostData) []dockerOutdatedRenderRow {
	rows := make([]dockerOutdatedRenderRow, 0)
	for _, result := range results {
		for _, row := range result.Rows {
			rows = append(rows, dockerOutdatedRenderRow{Host: result.Host, dockerOutdatedRow: row})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].ServiceName != rows[j].ServiceName {
			return rows[i].ServiceName < rows[j].ServiceName
		}
		if rows[i].Host != rows[j].Host {
			return rows[i].Host < rows[j].Host
		}
		return rows[i].ContainerName < rows[j].ContainerName
	})
	return rows
}
```

Add this local helper at the end of `pkg/yeet/docker_outdated.go`:

```go
func dash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}
```

- [ ] **Step 4: Route `docker outdated` through local handling**

In `pkg/yeet/svc_cmd.go`, add a `docker` case before the default remote handler:

```go
case "docker":
	if len(req.Command.Args) > 0 && req.Command.Args[0] == "outdated" {
		return handleDockerOutdatedCommand(ctx, req.Command.Args, req.Config, req.HostOverrideSet)
	}
	return handleSvcRemote(ctx, req)
```

- [ ] **Step 5: Run local tests and verify they pass**

Run:

```bash
go test ./pkg/yeet -run 'Test(FetchDockerOutdated|DockerOutdatedMultiHost|RenderDockerOutdated|HandleSvcCommandDockerOutdated)' -count=1
```

Expected: `ok github.com/yeetrun/yeet/pkg/yeet`.

- [ ] **Step 6: Run existing status/routing tests for regression coverage**

Run:

```bash
go test ./pkg/yeet -run 'TestSvc(Status|HandleStatus|DockerOutdated)|TestDockerOutdated' -count=1
go test ./cmd/yeet -run 'Test(BridgeServiceArgsDocker|PrepareCommandRoute|GroupHandlersWrapRemoteCommands)' -count=1
```

Expected: both packages pass.

- [ ] **Step 7: Commit local fan-out**

```bash
git add pkg/yeet/svc_cmd.go pkg/yeet/docker_outdated.go pkg/yeet/docker_outdated_test.go
git commit -m "yeet: render docker outdated across hosts"
```

---

### Task 6: Documentation

**Files:**
- Modify: `README.md`
- Modify: `website/docs/cli/yeet-cli.mdx`
- Modify: `website/docs/operations/workflows.mdx`
- Modify: `website/docs/cli/catch-cli.mdx`

- [ ] **Step 1: Initialize website submodule**

Run:

```bash
git submodule update --init --recursive website
```

Expected: the `website/` working tree is present and ready for docs edits.

- [ ] **Step 2: Update README Docker quickstart**

In `README.md`, update the compose refresh note to mention read-only checks:

```md
Note: `yeet run` for compose does not pull new images by default. To check for
available upstream image updates without changing containers, use
`yeet docker outdated`. To refresh images, use
`yeet run --pull <svc> ./compose.yml` or `yeet docker update <svc>`.
```

- [ ] **Step 3: Update yeet CLI docs**

In `website/docs/cli/yeet-cli.mdx`, add this section near the existing Docker group sections:

````md
### docker outdated

Checks Docker compose services for upstream image updates without pulling images,
recreating containers, or restarting services.

```bash
yeet docker outdated
yeet docker outdated <svc>
yeet docker outdated <svc>@<host>
yeet docker outdated --format=json
```

Default output shows only update, unknown, and error rows. Up-to-date containers
are omitted.
````

- [ ] **Step 4: Update workflows docs**

In `website/docs/operations/workflows.mdx`, update the Docker operation block:

```md
yeet docker outdated       # check for image updates without changing containers
yeet docker pull <svc>     # prefetch images without restarting
yeet docker update <svc>   # pull + recreate containers (restart)
```

- [ ] **Step 5: Update catch CLI docs**

In `website/docs/cli/catch-cli.mdx`, update the command list from:

```md
- `copy`, `cron`, `docker pull`, `docker update`, `enable`, `disable`
```

to:

```md
- `copy`, `cron`, `docker outdated`, `docker pull`, `docker update`, `enable`, `disable`
```

- [ ] **Step 6: Verify docs text**

Run:

```bash
rg -n "docker outdated|docker update|docker pull" README.md website/docs
```

Expected: README, yeet CLI docs, catch CLI docs, and workflows docs mention `docker outdated`.

- [ ] **Step 7: Commit docs**

Commit website docs inside the website submodule first:

```bash
cd website
git add docs/cli/yeet-cli.mdx docs/operations/workflows.mdx docs/cli/catch-cli.mdx
git commit -m "docs: add docker outdated command"
```

Then commit the README and submodule pointer in the root repo:

```bash
cd ..
git add README.md website
git commit -m "docs: document docker outdated"
```

---

### Task 7: Full Verification

**Files:**
- No planned file edits.

- [ ] **Step 1: Run targeted tests**

Run:

```bash
go test ./pkg/cli ./cmd/yeet ./pkg/svc ./pkg/catch ./pkg/yeet -count=1
```

Expected: all five packages pass.

- [ ] **Step 2: Run the full Go test suite**

Run:

```bash
go test ./... -count=1
```

Expected: every package reports `ok`.

- [ ] **Step 3: Run the pre-commit gate**

Run:

```bash
pre-commit run --all-files
```

Expected: all hooks pass, including license, gofmt, vet, go mod tidy, staticcheck, govulncheck, private-info scan, quality, and depaware.

- [ ] **Step 4: Run live smoke test when a Docker-capable catch host is available**

Only run this if a suitable Docker-capable catch host is available:

```bash
CATCH_HOST=<host> go run ./cmd/yeet docker outdated
CATCH_HOST=<host> go run ./cmd/yeet docker outdated --format=json
CATCH_HOST=<host> go run ./cmd/yeet docker outdated <svc>
```

Expected:

- commands do not pull images or restart containers
- table output includes a header
- JSON output decodes as either host-grouped rows for unscoped mode or row arrays for scoped mode
- scoped non-Docker service returns `service "<svc>" is not a docker compose service`

- [ ] **Step 5: Final status check**

Run:

```bash
git status --short
git log --oneline -8
```

Expected:

- no uncommitted root changes except any intentionally uncommitted local files
- recent commits show CLI, service, catch, yeet, docs, and verification-related commits

---

## Self-Review Notes

Spec coverage:

- Multi-host fan-out: Task 5.
- Scoped service and host routing: Task 1 and Task 5.
- Read-only Docker behavior: Task 3 tests command plan and uses read-only command helpers.
- Running digest vs upstream registry digest for the compose-declared image reference: Task 2 and Task 3.
- Interesting-row filtering: Task 3, Task 4, and Task 5.
- Internal registry handling: Task 3.
- JSON/table output: Task 4 and Task 5.
- Docs: Task 6.
- Verification: Task 7.

The plan intentionally keeps the first implementation on Docker CLI-backed registry inspection. It does not add an external updater dependency or a new JSON-RPC method.
