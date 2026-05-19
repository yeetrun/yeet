// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestParseComposeImages(t *testing.T) {
	got := parseComposeImages("  nginx:1.27  \n\nredis:7\nnginx:1.27\n")
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

func TestImageRepositoryNameNormalizesDockerHubOfficialImages(t *testing.T) {
	tests := []struct {
		name  string
		image string
		want  string
	}{
		{name: "short", image: "redis:7", want: "docker.io/library/redis"},
		{name: "docker io missing library", image: "docker.io/redis:7", want: "docker.io/library/redis"},
		{name: "index docker io missing library", image: "index.docker.io/redis:7", want: "docker.io/library/redis"},
		{name: "docker io library", image: "docker.io/library/redis:7", want: "docker.io/library/redis"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := imageRepositoryName(tt.image); got != tt.want {
				t.Fatalf("imageRepositoryName(%q) = %q, want %q", tt.image, got, tt.want)
			}
		})
	}
}

func TestImageRepositoryNameKeepsLocalhostRegistryExplicit(t *testing.T) {
	tests := []struct {
		image string
		want  string
	}{
		{image: "localhost/app:tag", want: "localhost/app"},
		{image: "docker.io/localhost/app:tag", want: "docker.io/localhost/app"},
	}
	for _, tt := range tests {
		t.Run(tt.image, func(t *testing.T) {
			if got := imageRepositoryName(tt.image); got != tt.want {
				t.Fatalf("imageRepositoryName(%q) = %q, want %q", tt.image, got, tt.want)
			}
		})
	}
	if imageRepositoryName("localhost/app:tag") == imageRepositoryName("docker.io/localhost/app:tag") {
		t.Fatal("localhost/app and docker.io/localhost/app should remain distinct")
	}
}

func TestSelectRepoDigestForImageMatchesDockerHubEquivalentRefs(t *testing.T) {
	repoDigests := []string{"docker.io/library/redis@sha256:redis"}
	for _, image := range []string{
		"redis:7",
		"docker.io/redis:7",
		"index.docker.io/redis:7",
		"docker.io/library/redis:7",
	} {
		t.Run(image, func(t *testing.T) {
			got := selectRepoDigestForImage(repoDigests, image)
			if got != "sha256:redis" {
				t.Fatalf("digest = %q, want sha256:redis", got)
			}
		})
	}
}

func TestSelectRepoDigestForImageKeepsLocalhostDistinctFromDockerHub(t *testing.T) {
	got := selectRepoDigestForImage([]string{"docker.io/localhost/app@sha256:dockerhub"}, "localhost/app:tag")
	if got != "" {
		t.Fatalf("digest = %q, want empty", got)
	}

	got = selectRepoDigestForImage([]string{"localhost/app@sha256:local"}, "localhost/app:tag")
	if got != "sha256:local" {
		t.Fatalf("digest = %q, want sha256:local", got)
	}
}

func TestDockerOutdatedCompare(t *testing.T) {
	tests := []struct {
		name    string
		row     DockerOutdatedRow
		want    DockerOutdatedStatus
		wantWhy string
	}{
		{name: "update", row: DockerOutdatedRow{RunningDigest: "sha256:old", LatestDigest: "sha256:new", Reason: "stale"}, want: DockerOutdatedUpdateAvailable},
		{name: "current", row: DockerOutdatedRow{RunningDigest: "sha256:same", LatestDigest: "sha256:same", Reason: "stale"}, want: DockerOutdatedCurrent},
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
			if tt.wantWhy == "" && got.Reason != "" {
				t.Fatalf("reason = %q, want empty", got.Reason)
			}
		})
	}
}

func TestCompactDockerOutdatedImageRef(t *testing.T) {
	tests := []struct {
		name  string
		image string
		want  string
	}{
		{name: "empty", image: "", want: "-"},
		{name: "registry stripped", image: "lscr.io/linuxserver/plex:latest", want: "linuxserver/plex:latest"},
		{name: "ghcr stripped", image: "ghcr.io/pocket-id/pocket-id", want: "pocket-id/pocket-id:latest"},
		{name: "docker hub library stripped", image: "docker.io/library/redis:7", want: "redis:7"},
		{name: "digest pinned shortened", image: "ghcr.io/acme/app@sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef", want: "acme/app@sha256:1234567890ab"},
		{name: "long image capped", image: "registry.example.com/very/long/path/to/application/component:2026.05.09-build.123", want: ".../to/application/component:2026.05.09-build.123"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CompactDockerOutdatedImageRef(tt.image); got != tt.want {
				t.Fatalf("CompactDockerOutdatedImageRef(%q) = %q, want %q", tt.image, got, tt.want)
			}
		})
	}
}

func TestCompactDockerOutdatedStatus(t *testing.T) {
	tests := []struct {
		name   string
		status DockerOutdatedStatus
		reason string
		want   string
	}{
		{name: "update", status: DockerOutdatedUpdateAvailable, want: "update"},
		{name: "current", status: DockerOutdatedCurrent, want: "current"},
		{name: "unknown with reason", status: DockerOutdatedUnknown, reason: "missing running digest", want: "unknown: missing running digest"},
		{name: "long reason capped", status: DockerOutdatedError, reason: "docker compose config failed because the registry returned a long multiline diagnostic", want: "error: docker compose config failed bec..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CompactDockerOutdatedStatus(tt.status, tt.reason); got != tt.want {
				t.Fatalf("CompactDockerOutdatedStatus(%q, %q) = %q, want %q", tt.status, tt.reason, got, tt.want)
			}
		})
	}
}

func TestUpstreamReferenceDigestFromRawManifest(t *testing.T) {
	const amdDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const armDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	index := []byte(`{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"` + amdDigest + `","platform":{"architecture":"amd64","os":"linux"}},{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"` + armDigest + `","platform":{"architecture":"arm64","os":"linux"}}]}`)
	got, ok, err := upstreamReferenceDigestFromRawManifest(index, "linux", "amd64")
	if err != nil {
		t.Fatalf("reference digest: %v", err)
	}
	if want := digestFromManifestBytes(index); !ok || got != want {
		t.Fatalf("reference digest = %q ok=%v, want %q true", got, ok, want)
	}

	manifest := []byte(`{"schemaVersion":2,"config":{"digest":"sha256:config"}}`)
	got, ok, err = upstreamReferenceDigestFromRawManifest(manifest, "linux", "amd64")
	if err != nil {
		t.Fatalf("single manifest digest: %v", err)
	}
	if want := digestFromManifestBytes(manifest); !ok || got != want {
		t.Fatalf("single manifest digest = %q ok=%v, want %q true", got, ok, want)
	}
}

func TestUpstreamReferenceDigestFromRawManifestErrorsOnMatchingEmptyDigest(t *testing.T) {
	index := []byte(`{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"","platform":{"architecture":"amd64","os":"linux"}}]}`)
	got, ok, err := upstreamReferenceDigestFromRawManifest(index, "linux", "amd64")
	if err == nil {
		t.Fatalf("platform digest error = nil, got digest %q ok=%v", got, ok)
	}
	if ok {
		t.Fatalf("ok = true, want false")
	}
}

func TestUpstreamReferenceDigestFromRawManifestErrorsOnMatchingInvalidDigest(t *testing.T) {
	index := []byte(`{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:bad","platform":{"architecture":"amd64","os":"linux"}}]}`)
	got, ok, err := upstreamReferenceDigestFromRawManifest(index, "linux", "amd64")
	if err == nil {
		t.Fatalf("platform digest error = nil, got digest %q ok=%v", got, ok)
	}
	if ok {
		t.Fatalf("ok = true, want false")
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

func TestDockerImageInspectRowParsesInspectJSON(t *testing.T) {
	raw := []byte(`[{"Id":"sha256:image","RepoDigests":["docker.io/library/redis@sha256:redis"],"Architecture":"amd64","Os":"linux"}]`)
	var rows []dockerImageInspectRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("unmarshal inspect JSON: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "sha256:image" || rows[0].RepoDigests[0] != "docker.io/library/redis@sha256:redis" || rows[0].Architecture != "amd64" || rows[0].OS != "linux" {
		t.Fatalf("inspect rows = %#v", rows)
	}
}

func TestCaptureCommandOutputIgnoresPrewiredSessionOutput(t *testing.T) {
	cmd := fakeDockerOutputCmd(t, "captured")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	out, err := captureCommandOutput(cmd)
	if err != nil {
		t.Fatalf("captureCommandOutput: %v", err)
	}
	if string(out) != "captured" {
		t.Fatalf("output = %q, want captured", out)
	}
}

func TestCaptureCommandOutputIncludesStderrOnFailure(t *testing.T) {
	cmd := fakeDockerErrorCmd(t, "registry DNS failed\n", 23)

	_, err := captureCommandOutput(cmd)
	if err == nil {
		t.Fatal("captureCommandOutput error = nil, want command failure")
	}
	for _, want := range []string{"exit status 23", "registry DNS failed"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want %q", err.Error(), want)
		}
	}
}

func TestReadonlyComposeCommandContextBuildsComposeCommand(t *testing.T) {
	calls := []cmdCall{}
	svc := newTestDockerComposeService(t, "services:\n  app:\n    image: redis:7\n", recordCmd(t, &calls))

	cmd, err := svc.readonlyComposeCommandContext(context.Background(), "ps", "--format", "json")
	if err != nil {
		t.Fatalf("readonlyComposeCommandContext returned error: %v", err)
	}
	if cmd.Dir != svc.DataDir {
		t.Fatalf("cmd.Dir = %q, want %q", cmd.Dir, svc.DataDir)
	}
	if len(calls) != 1 {
		t.Fatalf("recorded %d calls, want 1: %#v", len(calls), calls)
	}
	for _, want := range []string{"compose", "--project-name", "catch-svc-a", "--project-directory", svc.DataDir, "--file", "ps", "--format", "json"} {
		if !containsString(calls[0].args, want) {
			t.Fatalf("command args missing %q: %#v", want, calls[0].args)
		}
	}
}

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
		cfg: testDockerOutdatedServiceConfig{
			composePath: compose,
		}.service(),
	}

	var commands [][]string
	service.NewCmdContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		full := append([]string{name}, args...)
		commands = append(commands, full)
		switch {
		case hasOrderedArgs(args, "compose", "config", "--format", "json"):
			return fakeDockerOutputCmd(t, `{"services":{"app":{"image":"ghcr.io/acme/app:2"}}}`)
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
			t.Fatalf("unexpected docker command: docker %v", args)
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

func TestDockerComposeOutdatedOmitsCurrentMultiPlatformRepositoryDigest(t *testing.T) {
	tmp := t.TempDir()
	compose := writeDockerOutdatedFile(t, tmp, "compose.yml", "services:\n  app:\n    image: ghcr.io/acme/app:latest\n")
	fakeBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeBin, "docker"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake docker binary: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	service := &DockerComposeService{
		Name:    "web",
		DataDir: tmp,
		cfg: testDockerOutdatedServiceConfig{
			composePath: compose,
		}.service(),
	}

	const platformDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	rawIndex := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"` + platformDigest + `","platform":{"architecture":"amd64","os":"linux"}}]}`)
	repositoryDigest := digestFromManifestBytes(rawIndex)
	service.NewCmdContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		switch {
		case hasOrderedArgs(args, "compose", "config", "--format", "json"):
			return fakeDockerOutputCmd(t, `{"services":{"app":{"image":"ghcr.io/acme/app:latest"}}}`)
		case hasOrderedArgs(args, "compose", "ps", "--format=json"):
			return fakeDockerOutputCmd(t, `[{"ID":"cid","Name":"catch-web-app-1","Service":"app","Image":"ghcr.io/acme/app:latest","State":"running"}]`)
		case len(args) >= 2 && args[0] == "inspect" && args[1] == "cid":
			return fakeDockerOutputCmd(t, `[{"Image":"sha256:imageid"}]`)
		case len(args) >= 2 && args[0] == "image" && args[1] == "inspect":
			return fakeDockerOutputCmd(t, `[{"Id":"sha256:imageid","RepoDigests":["ghcr.io/acme/app@`+repositoryDigest+`"],"Architecture":"amd64","Os":"linux"}]`)
		case len(args) >= 4 && args[0] == "buildx" && args[1] == "imagetools" && args[2] == "inspect":
			if args[3] != "ghcr.io/acme/app:latest" {
				t.Fatalf("upstream image = %q, want ghcr.io/acme/app:latest", args[3])
			}
			return fakeDockerOutputCmd(t, string(rawIndex))
		default:
			t.Fatalf("unexpected docker command: docker %v", args)
			return fakeDockerOutputCmd(t, "")
		}
	}

	rows, err := service.Outdated(context.Background(), DockerOutdatedOptions{})
	if err != nil {
		t.Fatalf("Outdated: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %#v, want current multi-platform image omitted", rows)
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
		cfg: testDockerOutdatedServiceConfig{
			composePath: compose,
		}.service(),
	}
	service.NewCmdContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		switch {
		case hasOrderedArgs(args, "compose", "config", "--format", "json"):
			return fakeDockerOutputCmd(t, `{"services":{"app":{"image":"`+InternalRegistryHost+`/web/app:run"}}}`)
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

func TestDockerComposeOutdatedUsesServiceSpecificDeclaredImages(t *testing.T) {
	tmp := t.TempDir()
	compose := writeDockerOutdatedFile(t, tmp, "compose.yml", "services:\n  one:\n    image: ghcr.io/acme/app:1\n  two:\n    image: ghcr.io/acme/app:2\n")
	fakeBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeBin, "docker"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake docker binary: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	service := &DockerComposeService{
		Name:    "web",
		DataDir: tmp,
		cfg: testDockerOutdatedServiceConfig{
			composePath: compose,
		}.service(),
	}

	upstream := map[string]int{}
	service.NewCmdContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		switch {
		case hasOrderedArgs(args, "compose", "config", "--format", "json"):
			return fakeDockerOutputCmd(t, `{"services":{"one":{"image":"ghcr.io/acme/app:1"},"two":{"image":"ghcr.io/acme/app:2"}}}`)
		case hasOrderedArgs(args, "compose", "ps", "--format=json"):
			return fakeDockerOutputCmd(t, `[{"ID":"onecid","Name":"catch-web-one-1","Service":"one","Image":"ghcr.io/acme/app:old","State":"running"},{"ID":"twocid","Name":"catch-web-two-1","Service":"two","Image":"ghcr.io/acme/app:old","State":"running"}]`)
		case len(args) >= 2 && args[0] == "inspect" && args[1] == "onecid":
			return fakeDockerOutputCmd(t, `[{"Image":"sha256:one"}]`)
		case len(args) >= 2 && args[0] == "inspect" && args[1] == "twocid":
			return fakeDockerOutputCmd(t, `[{"Image":"sha256:two"}]`)
		case len(args) >= 3 && args[0] == "image" && args[1] == "inspect" && args[2] == "sha256:one":
			return fakeDockerOutputCmd(t, `[{"Id":"sha256:one","RepoDigests":["ghcr.io/acme/app@sha256:oldone"],"Architecture":"amd64","Os":"linux"}]`)
		case len(args) >= 3 && args[0] == "image" && args[1] == "inspect" && args[2] == "sha256:two":
			return fakeDockerOutputCmd(t, `[{"Id":"sha256:two","RepoDigests":["ghcr.io/acme/app@sha256:oldtwo"],"Architecture":"amd64","Os":"linux"}]`)
		case len(args) >= 4 && args[0] == "buildx" && args[1] == "imagetools" && args[2] == "inspect":
			upstream[args[3]]++
			return fakeDockerOutputCmd(t, `{"schemaVersion":2,"subject":"`+args[3]+`"}`)
		default:
			t.Fatalf("unexpected docker command: docker %v", args)
			return fakeDockerOutputCmd(t, "")
		}
	}

	rows, err := service.Outdated(context.Background(), DockerOutdatedOptions{})
	if err != nil {
		t.Fatalf("Outdated: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %#v, want two update rows", rows)
	}
	if rows[0].ContainerName != "one" || rows[0].Image != "ghcr.io/acme/app:1" {
		t.Fatalf("first row = %#v, want service one declared image tag 1", rows[0])
	}
	if rows[1].ContainerName != "two" || rows[1].Image != "ghcr.io/acme/app:2" {
		t.Fatalf("second row = %#v, want service two declared image tag 2", rows[1])
	}
	if upstream["ghcr.io/acme/app:1"] != 1 || upstream["ghcr.io/acme/app:2"] != 1 {
		t.Fatalf("upstream lookups = %#v, want one lookup per service-specific declared image", upstream)
	}
}

func TestDockerComposeOutdatedSkipsDeclaredInternalImageBeforeRegistry(t *testing.T) {
	tmp := t.TempDir()
	internalImage := InternalRegistryHost + "/web/app:run"
	compose := writeDockerOutdatedFile(t, tmp, "compose.yml", "services:\n  app:\n    image: "+internalImage+"\n")
	fakeBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeBin, "docker"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake docker binary: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	service := &DockerComposeService{
		Name:    "web",
		DataDir: tmp,
		cfg: testDockerOutdatedServiceConfig{
			composePath: compose,
		}.service(),
	}
	service.NewCmdContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		switch {
		case hasOrderedArgs(args, "compose", "config", "--format", "json"):
			return fakeDockerOutputCmd(t, `{"services":{"app":{"image":"`+internalImage+`"}}}`)
		case hasOrderedArgs(args, "compose", "ps", "--format=json"):
			return fakeDockerOutputCmd(t, `[{"ID":"cid","Name":"catch-web-app-1","Service":"app","Image":"ghcr.io/acme/app:latest","State":"running"}]`)
		default:
			t.Fatalf("declared internal image should not trigger docker inspect or registry lookup: docker %v", args)
			return fakeDockerOutputCmd(t, "")
		}
	}

	rows, err := service.Outdated(context.Background(), DockerOutdatedOptions{})
	if err != nil {
		t.Fatalf("Outdated host-wide: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("host-wide declared internal rows = %#v, want none", rows)
	}

	rows, err = service.Outdated(context.Background(), DockerOutdatedOptions{IncludeInternal: true})
	if err != nil {
		t.Fatalf("Outdated scoped: %v", err)
	}
	if len(rows) != 1 || rows[0].Image != internalImage || rows[0].Status != DockerOutdatedUnknown || rows[0].Reason != "internal image" {
		t.Fatalf("scoped declared internal rows = %#v, want one unknown internal image", rows)
	}
}

func TestDockerComposeOutdatedInvalidPinnedDigestIsRowError(t *testing.T) {
	tmp := t.TempDir()
	pinnedImage := "ghcr.io/acme/app@sha256:bad"
	compose := writeDockerOutdatedFile(t, tmp, "compose.yml", "services:\n  app:\n    image: "+pinnedImage+"\n")
	fakeBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeBin, "docker"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake docker binary: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	service := &DockerComposeService{
		Name:    "web",
		DataDir: tmp,
		cfg: testDockerOutdatedServiceConfig{
			composePath: compose,
		}.service(),
	}
	service.NewCmdContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		switch {
		case hasOrderedArgs(args, "compose", "config", "--format", "json"):
			return fakeDockerOutputCmd(t, `{"services":{"app":{"image":"`+pinnedImage+`"}}}`)
		case hasOrderedArgs(args, "compose", "ps", "--format=json"):
			return fakeDockerOutputCmd(t, `[{"ID":"cid","Name":"catch-web-app-1","Service":"app","Image":"ghcr.io/acme/app:latest","State":"running"}]`)
		case len(args) >= 2 && args[0] == "inspect" && args[1] == "cid":
			return fakeDockerOutputCmd(t, `[{"Image":"sha256:imageid"}]`)
		case len(args) >= 2 && args[0] == "image" && args[1] == "inspect":
			return fakeDockerOutputCmd(t, `[{"Id":"sha256:imageid","RepoDigests":["ghcr.io/acme/app@sha256:old"],"Architecture":"amd64","Os":"linux"}]`)
		case len(args) >= 3 && args[0] == "buildx" && args[1] == "imagetools" && args[2] == "inspect":
			t.Fatalf("invalid pinned image should not trigger registry lookup: docker %v", args)
			return fakeDockerOutputCmd(t, "")
		default:
			t.Fatalf("unexpected docker command: docker %v", args)
			return fakeDockerOutputCmd(t, "")
		}
	}

	rows, err := service.Outdated(context.Background(), DockerOutdatedOptions{})
	if err != nil {
		t.Fatalf("Outdated: %v", err)
	}
	if len(rows) != 1 || rows[0].Image != pinnedImage || rows[0].Status != DockerOutdatedError || !strings.Contains(rows[0].Reason, "invalid pinned image digest") {
		t.Fatalf("rows = %#v, want one row-level invalid digest error", rows)
	}
}

type testDockerOutdatedServiceConfig struct {
	composePath string
}

func (c testDockerOutdatedServiceConfig) service() *db.Service {
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

func fakeDockerErrorCmd(t *testing.T, stderr string, exitCode int) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestDockerOutdatedFakeCommand", "--", "")
	cmd.Env = append(os.Environ(),
		"GO_WANT_HELPER_PROCESS=1",
		"GO_WANT_HELPER_STDERR="+stderr,
		fmt.Sprintf("GO_WANT_HELPER_EXIT=%d", exitCode),
	)
	return cmd
}

func TestDockerOutdatedFakeCommand(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	if stderr := os.Getenv("GO_WANT_HELPER_STDERR"); stderr != "" {
		fmt.Fprint(os.Stderr, stderr)
	}
	if rawExit := os.Getenv("GO_WANT_HELPER_EXIT"); rawExit != "" {
		var exitCode int
		if _, err := fmt.Sscanf(rawExit, "%d", &exitCode); err != nil {
			fmt.Fprintf(os.Stderr, "invalid helper exit %q: %v", rawExit, err)
			os.Exit(2)
		}
		os.Exit(exitCode)
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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
