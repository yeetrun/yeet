// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
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

var fetchVMImageCatalogFunc = fetchVMImageCatalog

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
	if err := catalog.validate(true); err != nil {
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
	seenPayloads := map[string]struct{}{}
	seenVersionPrefixes := map[string]struct{}{}
	defaults := 0
	for _, image := range c.Images {
		if err := image.validate(requireTrustedURL); err != nil {
			return err
		}
		payload := strings.TrimSpace(image.Payload)
		if _, ok := seenPayloads[payload]; ok {
			return fmt.Errorf("duplicate VM image payload %q in catalog", payload)
		}
		seenPayloads[payload] = struct{}{}
		versionPrefix := strings.TrimSpace(image.VersionPrefix)
		if _, ok := seenVersionPrefixes[versionPrefix]; ok {
			return fmt.Errorf("duplicate VM image version_prefix %q in catalog", versionPrefix)
		}
		seenVersionPrefixes[versionPrefix] = struct{}{}
		if image.Default {
			defaults++
		}
	}
	if defaults > 1 {
		return fmt.Errorf("multiple default VM images in catalog: %s", vmImageCatalogPayloadsForError(c))
	}
	if defaults == 0 {
		return fmt.Errorf("no default VM image in catalog: %s", vmImageCatalogPayloadsForError(c))
	}
	return nil
}

func (i vmImageCatalogImage) validate(requireTrustedURL bool) error {
	payload, err := i.validateIdentity()
	if err != nil {
		return err
	}
	if err := i.validateManifestURL(requireTrustedURL); err != nil {
		return err
	}
	if err := i.validateVersionPrefix(payload); err != nil {
		return err
	}
	return i.validateRuntimeMetadata(payload)
}

func (i vmImageCatalogImage) validateIdentity() (string, error) {
	payload := strings.TrimSpace(i.Payload)
	if !strings.HasPrefix(payload, vmImagePayloadPrefix) || strings.TrimPrefix(payload, vmImagePayloadPrefix) == "" {
		return "", fmt.Errorf("invalid VM image catalog payload %q", i.Payload)
	}
	if strings.TrimSpace(i.Name) == "" {
		return "", fmt.Errorf("VM image catalog entry %s missing name", payload)
	}
	if strings.TrimSpace(i.Architecture) != "amd64" {
		return "", fmt.Errorf("VM image catalog entry %s has unsupported architecture %q", payload, i.Architecture)
	}
	return payload, nil
}

func (i vmImageCatalogImage) validateManifestURL(requireTrustedURL bool) error {
	if requireTrustedURL {
		if err := validateTrustedVMImageRepoURL(i.ManifestURL, "manifest"); err != nil {
			return err
		}
	}
	return nil
}

func (i vmImageCatalogImage) validateVersionPrefix(payload string) error {
	prefix := strings.TrimSpace(i.VersionPrefix)
	if prefix == "" || strings.ContainsAny(prefix, `/\`) {
		return fmt.Errorf("VM image catalog entry %s has invalid version_prefix %q", payload, i.VersionPrefix)
	}
	return nil
}

func (i vmImageCatalogImage) validateRuntimeMetadata(payload string) error {
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
	cleanPath, err := trustedVMImageRepoPath(u)
	if err != nil {
		return fmt.Errorf("untrusted VM image %s URL %q: %w", kind, rawURL, err)
	}
	switch u.Host {
	case "raw.githubusercontent.com":
		if !strings.HasPrefix(cleanPath, "/yeetrun/yeet-vm-images/") {
			return fmt.Errorf("untrusted VM image %s URL %q", kind, rawURL)
		}
	case "github.com":
		if !strings.HasPrefix(cleanPath, "/yeetrun/yeet-vm-images/") {
			return fmt.Errorf("untrusted VM image %s URL %q", kind, rawURL)
		}
	default:
		return fmt.Errorf("untrusted VM image %s URL %q", kind, rawURL)
	}
	return nil
}

func trustedVMImageRepoPath(u *url.URL) (string, error) {
	decodedPath, err := url.PathUnescape(u.EscapedPath())
	if err != nil {
		return "", err
	}
	return path.Clean("/" + strings.TrimPrefix(decodedPath, "/")), nil
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
	return vmImageCatalogImage{}, false
}

func (i vmImageCatalogImage) matchesVersion(version string) bool {
	version = strings.TrimSpace(version)
	prefix := strings.TrimSpace(i.VersionPrefix)
	if !strings.HasPrefix(version, prefix) {
		return false
	}
	suffix := strings.TrimPrefix(version, prefix)
	return isNumericVersionSuffix(suffix) || isHybridKernelVersionSuffix(suffix)
}

func isHybridKernelVersionSuffix(version string) bool {
	const prefix = "kernel-"
	if !strings.HasPrefix(version, prefix) {
		return false
	}
	kernel, revision, ok := strings.Cut(strings.TrimPrefix(version, prefix), "-")
	return ok && validUpstreamKernelVersion(kernel) && isNumericVersionSuffix(revision)
}

func validUpstreamKernelVersion(kernel string) bool {
	components := strings.Split(kernel, ".")
	if len(components) < 2 {
		return false
	}
	for _, component := range components {
		if component == "" {
			return false
		}
		for _, r := range component {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
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
