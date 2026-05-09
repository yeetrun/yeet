// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
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

func TestPlatformDigestFromRawManifestErrorsOnMatchingEmptyDigest(t *testing.T) {
	index := []byte(`{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"","platform":{"architecture":"amd64","os":"linux"}}]}`)
	got, ok, err := platformDigestFromRawManifest(index, "linux", "amd64")
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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
