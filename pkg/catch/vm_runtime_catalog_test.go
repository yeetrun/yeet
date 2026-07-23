// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestVMRuntimeCatalogAcceptsStrictSchemaAndResolvesEntries(t *testing.T) {
	catalog := validVMRuntimeCatalog()
	got, err := decodeVMRuntimeCatalog(marshalVMRuntimeTestJSON(t, catalog), true)
	if err != nil {
		t.Fatalf("decodeVMRuntimeCatalog: %v", err)
	}
	exact, ok := got.RuntimeByID("amd64", catalog.Architectures["amd64"].Runtimes[0].RuntimeID)
	if !ok || exact.ManifestSHA != strings.Repeat("1", 64) {
		t.Fatalf("exact runtime = %#v, %v", exact, ok)
	}
	stable, ok := got.RuntimeForChannel("amd64", "stable")
	if !ok || stable.RuntimeID != exact.RuntimeID || stable.ManifestSHA != exact.ManifestSHA {
		t.Fatalf("stable runtime = %#v, %v", stable, ok)
	}
}

func TestVMRuntimeCatalogResolvesNewestNonRevokedPackagingForUpstreamVersion(t *testing.T) {
	catalog := validVMRuntimeCatalog()
	base := catalog.Architectures["amd64"]
	legacy := base.Runtimes[0]
	legacy.RuntimeID = "firecracker-v1.14.3-yeet-v1"
	legacy.UpstreamVersion = "v1.14.3"
	legacy.Support = "eol"
	repacked := legacy
	repacked.RuntimeID = "firecracker-v1.14.3-yeet-v2"
	revoked := legacy
	revoked.RuntimeID = "firecracker-v1.14.3-yeet-v3"
	revoked.Support = "revoked"
	base.Runtimes = append(base.Runtimes, legacy, repacked, revoked)
	catalog.Architectures["amd64"] = base

	got, ok := catalog.RuntimeForUpstreamVersion("amd64", "v1.14.3")
	if !ok || got.RuntimeID != repacked.RuntimeID {
		t.Fatalf("runtime = %#v, %v; want %s", got, ok, repacked.RuntimeID)
	}
	if _, ok := catalog.RuntimeForUpstreamVersion("amd64", "v1.13.0"); ok {
		t.Fatal("unknown upstream version resolved")
	}
}

func TestVMRuntimeCatalogFetchUsesExplicitTestTrustOverride(t *testing.T) {
	raw := marshalVMRuntimeTestJSON(t, validVMRuntimeCatalog())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(raw)
	}))
	t.Cleanup(server.Close)
	cache := vmRuntimeCache{CatalogURL: server.URL + "/runtime-catalog.json", Client: server.Client()}
	catalog, err := cache.fetchCatalog(context.Background(), false)
	if err != nil {
		t.Fatalf("fetchCatalog: %v", err)
	}
	if _, ok := catalog.RuntimeForChannel("amd64", "stable"); !ok {
		t.Fatal("stable runtime not resolved")
	}
}

func TestVMRuntimeCatalogRejectsUnknownFields(t *testing.T) {
	raw := addVMRuntimeTestJSONField(t, marshalVMRuntimeTestJSON(t, validVMRuntimeCatalog()), "unexpected", true)
	if _, err := decodeVMRuntimeCatalog(raw, true); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("decodeVMRuntimeCatalog error = %v, want unknown field", err)
	}
}

func TestVMRuntimeCatalogRejectsUnsupportedSchema(t *testing.T) {
	catalog := validVMRuntimeCatalog()
	catalog.SchemaVersion = 2
	if _, err := decodeVMRuntimeCatalog(marshalVMRuntimeTestJSON(t, catalog), true); err == nil || !strings.Contains(err.Error(), "schema_version") {
		t.Fatalf("decodeVMRuntimeCatalog error = %v, want schema_version", err)
	}
}

func TestVMRuntimeCatalogRejectsMissingRequiredField(t *testing.T) {
	raw := marshalVMRuntimeTestJSON(t, validVMRuntimeCatalog())
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	delete(object, "revocations")
	if _, err := decodeVMRuntimeCatalog(marshalVMRuntimeTestJSON(t, object), true); err == nil || !strings.Contains(err.Error(), "missing required field") {
		t.Fatalf("decodeVMRuntimeCatalog error = %v, want missing required field", err)
	}
}

func TestVMRuntimeCatalogRequiresNullableCanaryFields(t *testing.T) {
	for _, field := range []string{"canary_attestation_url", "canary_attestation_sha256"} {
		t.Run(field, func(t *testing.T) {
			object := vmRuntimeCatalogTestObject(t)
			delete(vmRuntimeCatalogTestRuntime(t, object), field)
			if _, err := decodeVMRuntimeCatalog(marshalVMRuntimeTestJSON(t, object), true); err == nil || !strings.Contains(err.Error(), field) {
				t.Fatalf("decodeVMRuntimeCatalog error = %v, want missing %s", err, field)
			}
		})
	}
}

func TestVMRuntimeCatalogAcceptsNullCanaryFields(t *testing.T) {
	object := vmRuntimeCatalogTestObject(t)
	runtime := vmRuntimeCatalogTestRuntime(t, object)
	runtime["canary_attestation_url"] = nil
	runtime["canary_attestation_sha256"] = nil
	channels := object["architectures"].(map[string]any)["amd64"].(map[string]any)["channels"].(map[string]any)
	channels["stable"] = nil
	if _, err := decodeVMRuntimeCatalog(marshalVMRuntimeTestJSON(t, object), true); err != nil {
		t.Fatalf("decodeVMRuntimeCatalog: %v", err)
	}
}

func TestValidateVMRuntimeRevocation(t *testing.T) {
	runtimeID := "firecracker-v1.16.1-yeet-v1"
	manifestSHA := strings.Repeat("1", 64)
	revocation := vmRuntimeRevocation{
		RuntimeID: runtimeID, ManifestSHA: manifestSHA,
		Reason: "withdrawn after validation", RecordedAt: "2026-07-19T12:00:00Z",
	}
	entries := map[string]vmRuntimeCatalogRef{
		vmRuntimeCatalogKey(runtimeID, manifestSHA): {
			RuntimeID: runtimeID, ManifestSHA: manifestSHA, Support: "revoked",
		},
	}
	if err := validateVMRuntimeRevocation(revocation, entries, map[string]*vmRuntimeCatalogIdentity{}); err != nil {
		t.Fatalf("valid revocation: %v", err)
	}

	tests := []struct {
		name     string
		mutate   func(*vmRuntimeRevocation)
		entries  map[string]vmRuntimeCatalogRef
		channels map[string]*vmRuntimeCatalogIdentity
	}{
		{name: "runtime ID", mutate: func(r *vmRuntimeRevocation) { r.RuntimeID = "invalid" }, entries: entries},
		{name: "manifest digest", mutate: func(r *vmRuntimeRevocation) { r.ManifestSHA = "invalid" }, entries: entries},
		{name: "reason", mutate: func(r *vmRuntimeRevocation) { r.Reason = " " }, entries: entries},
		{name: "recorded at", mutate: func(r *vmRuntimeRevocation) { r.RecordedAt = "yesterday" }, entries: entries},
		{name: "missing entry", mutate: func(*vmRuntimeRevocation) {}, entries: map[string]vmRuntimeCatalogRef{}},
		{
			name: "still promoted", mutate: func(*vmRuntimeRevocation) {}, entries: entries,
			channels: map[string]*vmRuntimeCatalogIdentity{"stable": {RuntimeID: runtimeID, ManifestSHA: manifestSHA}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := revocation
			tt.mutate(&got)
			if err := validateVMRuntimeRevocation(got, tt.entries, tt.channels); err == nil {
				t.Fatal("invalid revocation accepted")
			}
		})
	}
}

func TestVMRuntimeCatalogRejectsInvalidCollectionShapes(t *testing.T) {
	tests := []struct {
		name   string
		want   string
		mutate func(map[string]any)
	}{
		{
			name: "null architectures",
			want: "JSON object",
			mutate: func(object map[string]any) {
				object["architectures"] = nil
			},
		},
		{
			name: "null runtimes",
			want: "JSON array",
			mutate: func(object map[string]any) {
				object["architectures"].(map[string]any)["amd64"].(map[string]any)["runtimes"] = nil
			},
		},
		{
			name: "object runtimes",
			want: "JSON array",
			mutate: func(object map[string]any) {
				object["architectures"].(map[string]any)["amd64"].(map[string]any)["runtimes"] = map[string]any{}
			},
		},
		{
			name: "null channels",
			want: "JSON object",
			mutate: func(object map[string]any) {
				object["architectures"].(map[string]any)["amd64"].(map[string]any)["channels"] = nil
			},
		},
		{
			name: "null revocations",
			want: "JSON array",
			mutate: func(object map[string]any) {
				object["revocations"] = nil
			},
		},
		{
			name: "object revocations",
			want: "JSON array",
			mutate: func(object map[string]any) {
				object["revocations"] = map[string]any{}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			object := vmRuntimeCatalogTestObject(t)
			tt.mutate(object)
			if _, err := decodeVMRuntimeCatalog(marshalVMRuntimeTestJSON(t, object), true); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("decodeVMRuntimeCatalog error = %v, want %s", err, tt.want)
			}
		})
	}
}

func TestVMRuntimeCatalogRejectsChannelDigestMismatch(t *testing.T) {
	catalog := validVMRuntimeCatalog()
	catalog.Architectures["amd64"].Channels["stable"].ManifestSHA = strings.Repeat("9", 64)
	if _, err := decodeVMRuntimeCatalog(marshalVMRuntimeTestJSON(t, catalog), true); err == nil || !strings.Contains(err.Error(), "stable") {
		t.Fatalf("decodeVMRuntimeCatalog error = %v, want stable channel mismatch", err)
	}
}

func TestVMRuntimeCatalogRejectsRevokedChannelRuntime(t *testing.T) {
	catalog := validVMRuntimeCatalog()
	runtimeRef := catalog.Architectures["amd64"].Runtimes[0]
	runtimeRef.Support = "revoked"
	catalog.Architectures["amd64"].Runtimes[0] = runtimeRef
	catalog.Revocations = []vmRuntimeRevocation{{
		RuntimeID: runtimeRef.RuntimeID, ManifestSHA: runtimeRef.ManifestSHA,
		Reason: "security issue", RecordedAt: "2026-07-19T14:00:00Z",
	}}
	if _, err := decodeVMRuntimeCatalog(marshalVMRuntimeTestJSON(t, catalog), true); err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("decodeVMRuntimeCatalog error = %v, want revoked channel runtime", err)
	}
}

func TestVMRuntimeCatalogRejectsUntrustedURLs(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{name: "non HTTPS", url: "http://github.com/yeetrun/yeet-vm-images/releases/download/firecracker-v1.16.1-yeet-v1/runtime-manifest.json"},
		{name: "wrong repository", url: "https://github.com/example/yeet-vm-images/releases/download/firecracker-v1.16.1-yeet-v1/runtime-manifest.json"},
		{name: "wrong path", url: "https://github.com/yeetrun/yeet-vm-images/raw/main/runtime-manifest.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			catalog := validVMRuntimeCatalog()
			entry := catalog.Architectures["amd64"].Runtimes[0]
			entry.ManifestURL = tt.url
			catalog.Architectures["amd64"].Runtimes[0] = entry
			if _, err := decodeVMRuntimeCatalog(marshalVMRuntimeTestJSON(t, catalog), true); err == nil || !strings.Contains(err.Error(), "untrusted") {
				t.Fatalf("decodeVMRuntimeCatalog error = %v, want untrusted", err)
			}
		})
	}
}

func TestValidateTrustedYeetVMArtifactURLCompatibility(t *testing.T) {
	trusted := "https://raw.githubusercontent.com/yeetrun/yeet-vm-images/main/catalog.json"
	if err := validateTrustedYeetVMArtifactURL(trusted, "catalog"); err != nil {
		t.Fatalf("validateTrustedYeetVMArtifactURL: %v", err)
	}
	if err := validateTrustedVMImageRepoURL(trusted, "catalog"); err != nil {
		t.Fatalf("compatibility wrapper: %v", err)
	}
}

func TestVMRuntimeCatalogTrustedRedirectPolicy(t *testing.T) {
	customPolicyCalled := false
	client := trustedVMRuntimeHTTPClient(&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		customPolicyCalled = true
		return nil
	}}, true)
	githubRequest := vmRuntimeRedirectTestRequest(t, "https://github.com/yeetrun/yeet-vm-images/releases/download/runtime/runtime-manifest.json")
	releaseAssetRequest := vmRuntimeRedirectTestRequest(t, "https://release-assets.githubusercontent.com/github-production-release-asset/file?sig=redacted")
	if err := client.CheckRedirect(releaseAssetRequest, []*http.Request{githubRequest}); err != nil {
		t.Fatalf("trusted release redirect: %v", err)
	}
	if !customPolicyCalled {
		t.Fatal("existing redirect policy was not preserved")
	}
	for _, rawURL := range []string{
		"https://example.com/runtime",
		"http://release-assets.githubusercontent.com/runtime",
		"https://release-assets.githubusercontent.com:444/runtime",
	} {
		if err := client.CheckRedirect(vmRuntimeRedirectTestRequest(t, rawURL), []*http.Request{githubRequest}); err == nil {
			t.Fatalf("redirect to %s succeeded", rawURL)
		}
	}

	rawRequest := vmRuntimeRedirectTestRequest(t, defaultVMRuntimeCatalogURL)
	if err := client.CheckRedirect(releaseAssetRequest, []*http.Request{rawRequest}); err == nil {
		t.Fatal("raw catalog redirected to release asset host")
	}
	if got := trustedVMRuntimeHTTPClient(client, false); got != client {
		t.Fatal("test-only untrusted client was wrapped")
	}
}

func vmRuntimeRedirectTestRequest(t testing.TB, rawURL string) *http.Request {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Request{URL: parsed}
}

func FuzzParseVMRuntimeCatalog(f *testing.F) {
	raw, err := json.Marshal(validVMRuntimeCatalog())
	if err != nil {
		f.Fatal(err)
	}
	f.Add(raw)
	f.Add([]byte(`{"schema_version":1,"architectures":{},"revocations":[]}`))
	f.Add([]byte(`not-json`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = decodeVMRuntimeCatalog(raw, false)
	})
}

func FuzzValidateTrustedVMRuntimeURL(f *testing.F) {
	f.Add(defaultVMRuntimeCatalogURL)
	f.Add("https://github.com/yeetrun/yeet-vm-images/releases/download/firecracker-v1.16.1-yeet-v1/runtime-manifest.json")
	f.Add("http://127.0.0.1/runtime-manifest.json")
	f.Fuzz(func(t *testing.T, rawURL string) {
		_ = validateTrustedYeetVMArtifactURL(rawURL, "runtime artifact")
		_ = validateVMRuntimeDownloadURL(rawURL, "manifest", "firecracker-v1.16.1-yeet-v1", true)
	})
}

func validVMRuntimeCatalog() vmRuntimeCatalog {
	runtimeID := "firecracker-v1.16.1-yeet-v1"
	manifestSHA := strings.Repeat("1", 64)
	ref := vmRuntimeCatalogRef{
		RuntimeID:       runtimeID,
		ManifestURL:     "https://github.com/yeetrun/yeet-vm-images/releases/download/" + runtimeID + "/runtime-manifest.json",
		ManifestSHA:     manifestSHA,
		UpstreamVersion: "v1.16.1",
		Support:         "supported",
		IntegrationURL:  "https://github.com/yeetrun/yeet-vm-images/releases/download/" + runtimeID + "-integration-123/runtime-attestation.json",
		IntegrationSHA:  strings.Repeat("2", 64),
		CanaryURL:       "https://github.com/yeetrun/yeet-vm-images/releases/download/" + runtimeID + "-canary-124/runtime-attestation.json",
		CanarySHA:       strings.Repeat("3", 64),
	}
	return vmRuntimeCatalog{
		SchemaVersion: 1,
		Architectures: map[string]vmRuntimeCatalogArchitecture{
			"amd64": {
				Runtimes: []vmRuntimeCatalogRef{ref},
				Channels: map[string]*vmRuntimeCatalogIdentity{
					"stable":    {RuntimeID: runtimeID, ManifestSHA: manifestSHA},
					"candidate": {RuntimeID: runtimeID, ManifestSHA: manifestSHA},
				},
			},
		},
		Revocations: []vmRuntimeRevocation{},
	}
}

func vmRuntimeCatalogTestObject(t testing.TB) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(marshalVMRuntimeTestJSON(t, validVMRuntimeCatalog()), &object); err != nil {
		t.Fatal(err)
	}
	return object
}

func vmRuntimeCatalogTestRuntime(t testing.TB, object map[string]any) map[string]any {
	t.Helper()
	architectures, ok := object["architectures"].(map[string]any)
	if !ok {
		t.Fatal("catalog architectures fixture is not an object")
	}
	architecture, ok := architectures["amd64"].(map[string]any)
	if !ok {
		t.Fatal("catalog amd64 fixture is not an object")
	}
	runtimes, ok := architecture["runtimes"].([]any)
	if !ok || len(runtimes) != 1 {
		t.Fatalf("catalog runtimes fixture = %#v", architecture["runtimes"])
	}
	runtime, ok := runtimes[0].(map[string]any)
	if !ok {
		t.Fatal("catalog runtime fixture is not an object")
	}
	return runtime
}
