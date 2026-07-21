// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

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
				Payload:        "vm://ubuntu/26.04",
				Name:           "Ubuntu 26.04",
				Architecture:   "amd64",
				ManifestURL:    "https://github.com/yeetrun/yeet-vm-images/releases/download/ubuntu-26.04-amd64-latest/manifest.json",
				VersionPrefix:  "ubuntu-26.04-amd64-",
				DefaultUser:    "ubuntu",
				MetadataDriver: "ubuntu",
				Capabilities:   []string{"guest_init", "guest_agent", "rsync"},
				Default:        true,
			},
			{
				Payload:        "vm://nixos/26.05",
				Name:           "NixOS 26.05",
				Architecture:   "amd64",
				ManifestURL:    "https://github.com/yeetrun/yeet-vm-images/releases/download/nixos-26.05-amd64-latest/manifest.json",
				VersionPrefix:  "nixos-26.05-amd64-",
				DefaultUser:    "nixos",
				MetadataDriver: "nixos",
				Capabilities:   []string{"guest_init", "guest_agent", "rsync"},
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

func TestVMImageCatalogLegacySchemaWithoutComponentCatalogsStillValidates(t *testing.T) {
	raw := []byte(`{
		"schema_version": 1,
		"images": [{
			"payload": "vm://ubuntu/26.04",
			"name": "Ubuntu 26.04",
			"architecture": "amd64",
			"manifest_url": "https://github.com/yeetrun/yeet-vm-images/releases/download/ubuntu-26.04-amd64-latest/manifest.json",
			"version_prefix": "ubuntu-26.04-amd64-",
			"default": true
		}]
	}`)
	var catalog vmImageCatalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	if err := catalog.validate(true); err != nil {
		t.Fatalf("legacy catalog validate: %v", err)
	}
	if catalog.ComponentCatalogs != nil {
		t.Fatalf("legacy component catalogs = %#v, want nil", catalog.ComponentCatalogs)
	}
	if _, ok := catalog.ImageByPayload("vm://ubuntu/26.04"); !ok {
		t.Fatal("legacy image lookup failed")
	}
}

func TestVMImageCatalogAdditiveComponentCatalogsRequireTrustedHTTPS(t *testing.T) {
	catalog := vmImageCatalogValidationTestCatalog()
	catalog.ComponentCatalogs = &vmImageComponentCatalogs{
		GuestBases: "https://raw.githubusercontent.com/yeetrun/yeet-vm-images/main/guest-catalog.json",
		Kernels:    "https://raw.githubusercontent.com/yeetrun/yeet-vm-images/main/kernel-catalog.json",
		Runtimes:   "https://raw.githubusercontent.com/yeetrun/yeet-vm-images/main/runtime-catalog.json",
	}
	if err := catalog.validate(true); err != nil {
		t.Fatalf("component catalog validate: %v", err)
	}

	catalog.ComponentCatalogs.Kernels = "http://raw.githubusercontent.com/yeetrun/yeet-vm-images/main/kernel-catalog.json"
	if err := catalog.validate(true); err == nil || !strings.Contains(err.Error(), "scheme must be https") {
		t.Fatalf("insecure component catalog error = %v", err)
	}
}

func TestVMImageCatalogRejectsUntrustedManifestURL(t *testing.T) {
	catalog := vmImageCatalog{
		SchemaVersion: 1,
		Images: []vmImageCatalogImage{{
			Payload:        "vm://ubuntu/26.04",
			Name:           "Ubuntu 26.04",
			Architecture:   "amd64",
			ManifestURL:    "https://example.com/manifest.json",
			VersionPrefix:  "ubuntu-26.04-amd64-",
			DefaultUser:    "ubuntu",
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
		Payload:        "vm://ubuntu/26.04",
		Name:           "Ubuntu 26.04",
		Architecture:   "amd64",
		ManifestURL:    "https://github.com/yeetrun/yeet-vm-images/releases/download/ubuntu-26.04-amd64-latest/manifest.json",
		VersionPrefix:  "ubuntu-26.04-amd64-",
		DefaultUser:    "ubuntu",
		MetadataDriver: "ubuntu",
	}
	catalog := vmImageCatalog{SchemaVersion: 1, Images: []vmImageCatalogImage{image, image}}
	err := catalog.validate(true)
	if err == nil || !strings.Contains(err.Error(), "duplicate VM image payload") {
		t.Fatalf("validate error = %v", err)
	}
}

func TestVMImageCatalogRejectsDuplicateVersionPrefix(t *testing.T) {
	catalog := vmImageCatalog{
		SchemaVersion: 1,
		Images: []vmImageCatalogImage{
			vmImageCatalogTestImage("vm://ubuntu/26.04", "ubuntu-26.04-amd64-", true),
			vmImageCatalogTestImage("vm://ubuntu/rolling", " ubuntu-26.04-amd64- ", false),
		},
	}
	err := catalog.validate(true)
	if err == nil || !strings.Contains(err.Error(), "duplicate VM image version_prefix") {
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

func TestValidateTrustedVMImageRepoURL(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		wantErr string
	}{
		{
			name:   "github release manifest",
			rawURL: "https://github.com/yeetrun/yeet-vm-images/releases/download/foo/manifest.json",
		},
		{
			name:   "raw catalog",
			rawURL: "https://raw.githubusercontent.com/yeetrun/yeet-vm-images/main/catalog.json",
		},
		{
			name:    "wrong scheme",
			rawURL:  "http://github.com/yeetrun/yeet-vm-images/releases/download/foo/manifest.json",
			wantErr: "scheme must be https",
		},
		{
			name:    "wrong host",
			rawURL:  "https://example.com/yeetrun/yeet-vm-images/releases/download/foo/manifest.json",
			wantErr: "untrusted VM image manifest URL",
		},
		{
			name:    "wrong owner repo",
			rawURL:  "https://github.com/other/yeet-vm-images/releases/download/foo/manifest.json",
			wantErr: "untrusted VM image manifest URL",
		},
		{
			name:    "dot segment escapes repo",
			rawURL:  "https://github.com/yeetrun/yeet-vm-images/../other/repo/releases/download/foo/manifest.json",
			wantErr: "untrusted VM image manifest URL",
		},
		{
			name:    "encoded dot segment escapes repo",
			rawURL:  "https://github.com/yeetrun/yeet-vm-images/%2e%2e/other/repo/releases/download/foo/manifest.json",
			wantErr: "untrusted VM image manifest URL",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTrustedVMImageRepoURL(tt.rawURL, "manifest")
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateTrustedVMImageRepoURL() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateTrustedVMImageRepoURL() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestVMImageCatalogDefaultRequiresExactlyOneDefault(t *testing.T) {
	catalog := vmImageCatalog{
		SchemaVersion: 1,
		Images: []vmImageCatalogImage{
			vmImageCatalogTestImage("vm://ubuntu/26.04", "ubuntu-26.04-amd64-", false),
			vmImageCatalogTestImage("vm://nixos/26.05", "nixos-26.05-amd64-", false),
		},
	}
	err := catalog.validate(true)
	if err == nil || !strings.Contains(err.Error(), "no default VM image") {
		t.Fatalf("validate error = %v", err)
	}
}

func TestVMImageCatalogDefaultImageNoDefault(t *testing.T) {
	catalog := vmImageCatalog{
		SchemaVersion: 1,
		Images: []vmImageCatalogImage{
			vmImageCatalogTestImage("vm://ubuntu/26.04", "ubuntu-26.04-amd64-", false),
			vmImageCatalogTestImage("vm://nixos/26.05", "nixos-26.05-amd64-", false),
		},
	}
	got, ok := catalog.DefaultImage()
	if ok {
		t.Fatalf("DefaultImage() = %#v ok=%v, want ok=false", got, ok)
	}
}

func TestFetchVMImageCatalogRejectsNon2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	_, err := fetchVMImageCatalogFromURL(context.Background(), server.Client(), server.URL+"/catalog.json", false)
	if err == nil || !strings.Contains(err.Error(), "503 Service Unavailable") {
		t.Fatalf("fetchVMImageCatalogFromURL error = %v", err)
	}
}

func TestFetchVMImageCatalogRejectsUntrustedManifestURLFromOverride(t *testing.T) {
	catalog := vmImageCatalogValidationTestCatalog()
	catalog.Images[0].ManifestURL = "https://example.com/manifest.json"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewEncoder(w).Encode(catalog); err != nil {
			t.Fatalf("encode catalog: %v", err)
		}
	}))
	defer server.Close()

	_, err := fetchVMImageCatalogFromURL(context.Background(), server.Client(), server.URL+"/catalog.json", false)
	if err == nil || !strings.Contains(err.Error(), "untrusted VM image manifest URL") {
		t.Fatalf("fetchVMImageCatalogFromURL error = %v", err)
	}
}

func TestVMImageCatalogValidationBranches(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*vmImageCatalog)
		wantErr string
	}{
		{
			name:    "unsupported schema",
			mutate:  func(c *vmImageCatalog) { c.SchemaVersion = 2 },
			wantErr: "unsupported VM image catalog schema_version",
		},
		{
			name:    "empty image list",
			mutate:  func(c *vmImageCatalog) { c.Images = nil },
			wantErr: "VM image catalog has no images",
		},
		{
			name:    "invalid payload",
			mutate:  func(c *vmImageCatalog) { c.Images[0].Payload = "ubuntu/26.04" },
			wantErr: "invalid VM image catalog payload",
		},
		{
			name:    "blank name",
			mutate:  func(c *vmImageCatalog) { c.Images[0].Name = " \t" },
			wantErr: "missing name",
		},
		{
			name:    "non amd64 architecture",
			mutate:  func(c *vmImageCatalog) { c.Images[0].Architecture = "arm64" },
			wantErr: "unsupported architecture",
		},
		{
			name:    "invalid version prefix",
			mutate:  func(c *vmImageCatalog) { c.Images[0].VersionPrefix = "ubuntu/26.04-amd64-" },
			wantErr: "invalid version_prefix",
		},
		{
			name:    "invalid default user",
			mutate:  func(c *vmImageCatalog) { c.Images[0].DefaultUser = "bad user" },
			wantErr: "invalid default_user",
		},
		{
			name:    "unsupported metadata driver",
			mutate:  func(c *vmImageCatalog) { c.Images[0].MetadataDriver = "freebsd" },
			wantErr: "unsupported metadata_driver",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			catalog := vmImageCatalogValidationTestCatalog()
			tt.mutate(&catalog)
			err := catalog.validate(true)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validate error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestVMImageCatalogValidationRejectsNonnumericVersionSuffix(t *testing.T) {
	catalog := vmImageCatalogValidationTestCatalog()
	got, ok := catalog.ImageByVersion("ubuntu-26.04-amd64-latest")
	if ok {
		t.Fatalf("ImageByVersion() = %#v ok=%v, want ok=false", got, ok)
	}
}

func TestVMImageCatalogMatchesHybridKernelImageVersion(t *testing.T) {
	catalog := vmImageCatalogValidationTestCatalog()
	tests := []string{
		"ubuntu-26.04-amd64-kernel-6.10-v1",
		"ubuntu-26.04-amd64-kernel-6.10.14-v42",
		"ubuntu-26.04-amd64-kernel-7.1.1-v3",
	}
	for _, version := range tests {
		t.Run(version, func(t *testing.T) {
			got, ok := catalog.ImageByVersion(version)
			if !ok || got.Payload != "vm://ubuntu/26.04" {
				t.Fatalf("ImageByVersion() = %#v ok=%v, want ubuntu image", got, ok)
			}
		})
	}
}

func TestVMImageCatalogRejectsMalformedHybridKernelImageVersion(t *testing.T) {
	catalog := vmImageCatalogValidationTestCatalog()
	tests := []string{
		"ubuntu-26.04-amd64- kernel-6.10-v1",
		"ubuntu-26.04-amd64-kernel--v1",
		"ubuntu-26.04-amd64-kernel-6-v1",
		"ubuntu-26.04-amd64-kernel-.10-v1",
		"ubuntu-26.04-amd64-kernel-6.-v1",
		"ubuntu-26.04-amd64-kernel-6.10.-v1",
		"ubuntu-26.04-amd64-kernel-6.x-v1",
		"ubuntu-26.04-amd64-kernel-6.10-rc1-v1",
		"ubuntu-26.04-amd64-kernel-6.10-v",
		"ubuntu-26.04-amd64-kernel-6.10-vlatest",
		"ubuntu-26.04-amd64-kernel-6.10-1",
	}
	for _, version := range tests {
		t.Run(version, func(t *testing.T) {
			got, ok := catalog.ImageByVersion(version)
			if ok {
				t.Fatalf("ImageByVersion() = %#v ok=%v, want ok=false", got, ok)
			}
		})
	}
}

func TestVMImageCacheFetchCatalogUsesOverrideURL(t *testing.T) {
	catalog := vmImageCatalog{
		SchemaVersion: 1,
		Images: []vmImageCatalogImage{
			vmImageCatalogTestImage("vm://ubuntu/26.04", "ubuntu-26.04-amd64-", true),
		},
	}
	var gotUserAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/custom-catalog.json" {
			t.Fatalf("request path = %q", r.URL.Path)
		}
		gotUserAgent = r.Header.Get("User-Agent")
		if err := json.NewEncoder(w).Encode(catalog); err != nil {
			t.Fatalf("encode catalog: %v", err)
		}
	}))
	defer server.Close()

	cache := vmImageCache{Client: server.Client(), catalogURL: server.URL + "/custom-catalog.json"}
	got, err := cache.FetchCatalog(context.Background())
	if err != nil {
		t.Fatalf("FetchCatalog: %v", err)
	}
	if gotUserAgent != vmImageHTTPUserAgent {
		t.Fatalf("User-Agent = %q, want %q", gotUserAgent, vmImageHTTPUserAgent)
	}
	def, ok := got.DefaultImage()
	if !ok || def.Payload != "vm://ubuntu/26.04" {
		t.Fatalf("default lookup = %#v ok=%v", def, ok)
	}
}

func TestVMImageCacheFetchCatalogUsesDefaultHook(t *testing.T) {
	orig := fetchVMImageCatalogFunc
	testClient := &http.Client{}
	called := false
	fetchVMImageCatalogFunc = func(ctx context.Context, client *http.Client) (vmImageCatalog, error) {
		called = true
		if client != testClient {
			t.Fatalf("client = %#v, want test client", client)
		}
		return vmImageCatalogValidationTestCatalog(), nil
	}
	t.Cleanup(func() { fetchVMImageCatalogFunc = orig })

	got, err := (vmImageCache{Client: testClient}).FetchCatalog(context.Background())
	if err != nil {
		t.Fatalf("FetchCatalog: %v", err)
	}
	if !called {
		t.Fatalf("FetchCatalog did not call fetchVMImageCatalogFunc")
	}
	def, ok := got.DefaultImage()
	if !ok || def.Payload != "vm://ubuntu/26.04" {
		t.Fatalf("default lookup = %#v ok=%v", def, ok)
	}
}

func vmImageCatalogValidationTestCatalog() vmImageCatalog {
	return vmImageCatalog{
		SchemaVersion: 1,
		Images: []vmImageCatalogImage{
			vmImageCatalogTestImage("vm://ubuntu/26.04", "ubuntu-26.04-amd64-", true),
		},
	}
}

func vmImageCatalogTestImage(payload, prefix string, def bool) vmImageCatalogImage {
	return vmImageCatalogImage{
		Payload:        payload,
		Name:           payload,
		Architecture:   "amd64",
		ManifestURL:    "https://github.com/yeetrun/yeet-vm-images/releases/download/" + strings.TrimSuffix(prefix, "-") + "-latest/manifest.json",
		VersionPrefix:  prefix,
		DefaultUser:    "ubuntu",
		MetadataDriver: "ubuntu",
		Default:        def,
	}
}
