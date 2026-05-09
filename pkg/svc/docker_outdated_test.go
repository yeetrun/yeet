// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

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
