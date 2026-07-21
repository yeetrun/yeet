// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
)

const (
	defaultVMGuestBaseCatalogURL = "https://raw.githubusercontent.com/yeetrun/yeet-vm-images/main/guest-catalog.json"
	defaultVMKernelCatalogURL    = "https://raw.githubusercontent.com/yeetrun/yeet-vm-images/main/kernel-catalog.json"
	maxVMComponentCatalogBytes   = 4 << 20
)

var (
	vmGuestBaseIDPattern = regexp.MustCompile(`^guest-(ubuntu|nixos)-([0-9]+\.[0-9]+)-amd64-v[1-9][0-9]*$`)
	vmKernelIDPattern    = regexp.MustCompile(`^kernel-linux-([0-9]+\.[0-9]+(?:\.[0-9]+)*)-yeet-v([1-9][0-9]*)$`)
	vmOSVersionPattern   = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)
)

type vmGuestBaseCatalogRef struct {
	GuestBaseID    string `json:"guest_base_id"`
	OS             string `json:"os"`
	OSVersion      string `json:"os_version"`
	Architecture   string `json:"architecture"`
	ManifestURL    string `json:"manifest_url"`
	ManifestSHA256 string `json:"manifest_sha256"`
}

type vmGuestBaseCatalogIdentity struct {
	GuestBaseID    string `json:"guest_base_id"`
	ManifestSHA256 string `json:"manifest_sha256"`
}

type vmGuestBaseCatalogChannels struct {
	Stable    *vmGuestBaseCatalogIdentity `json:"stable"`
	Candidate *vmGuestBaseCatalogIdentity `json:"candidate"`
}

type vmGuestBaseCatalog struct {
	SchemaVersion int                                   `json:"schema_version"`
	GuestBases    []vmGuestBaseCatalogRef               `json:"guest_bases"`
	Channels      map[string]vmGuestBaseCatalogChannels `json:"channels"`
}

type vmKernelCatalogRef struct {
	KernelID          string `json:"kernel_id"`
	UpstreamVersion   string `json:"upstream_version"`
	PackagingRevision int    `json:"packaging_revision"`
	Architecture      string `json:"architecture"`
	ManifestURL       string `json:"manifest_url"`
	ManifestSHA256    string `json:"manifest_sha256"`
}

type vmKernelCatalogIdentity struct {
	KernelID       string `json:"kernel_id"`
	ManifestSHA256 string `json:"manifest_sha256"`
}

type vmKernelCatalogChannels struct {
	Stable    *vmKernelCatalogIdentity `json:"stable"`
	Candidate *vmKernelCatalogIdentity `json:"candidate"`
}

type vmKernelCatalog struct {
	SchemaVersion int                                `json:"schema_version"`
	Kernels       []vmKernelCatalogRef               `json:"kernels"`
	Channels      map[string]vmKernelCatalogChannels `json:"channels"`
}

type vmComponentCatalogSet struct {
	GuestBases vmGuestBaseCatalog
	Kernels    vmKernelCatalog
	Runtimes   vmRuntimeCatalog
}

func (c vmImageComponentCatalogs) validate(requireTrustedURL bool) error {
	for _, catalog := range []struct {
		name     string
		rawURL   string
		expected string
	}{
		{name: "guest bases", rawURL: c.GuestBases, expected: defaultVMGuestBaseCatalogURL},
		{name: "kernels", rawURL: c.Kernels, expected: defaultVMKernelCatalogURL},
		{name: "runtimes", rawURL: c.Runtimes, expected: defaultVMRuntimeCatalogURL},
	} {
		if strings.TrimSpace(catalog.rawURL) == "" {
			return fmt.Errorf("VM image component catalog %s URL is required", catalog.name)
		}
		if !requireTrustedURL {
			continue
		}
		if err := validateTrustedYeetVMArtifactURL(catalog.rawURL, "component catalog "+catalog.name); err != nil {
			return err
		}
		if catalog.rawURL != catalog.expected {
			return fmt.Errorf("untrusted VM image component catalog %s URL %q", catalog.name, catalog.rawURL)
		}
	}
	return nil
}

func decodeVMGuestBaseCatalog(raw []byte, requireTrustedURL bool) (vmGuestBaseCatalog, error) {
	if err := validateVMGuestBaseCatalogRequiredJSON(raw); err != nil {
		return vmGuestBaseCatalog{}, err
	}
	var catalog vmGuestBaseCatalog
	if err := decodeStrictVMRuntimeJSON(raw, &catalog, "VM guest-base catalog"); err != nil {
		return vmGuestBaseCatalog{}, err
	}
	if err := catalog.validate(requireTrustedURL); err != nil {
		return vmGuestBaseCatalog{}, err
	}
	return catalog, nil
}

func decodeVMKernelCatalog(raw []byte, requireTrustedURL bool) (vmKernelCatalog, error) {
	if err := validateVMKernelCatalogRequiredJSON(raw); err != nil {
		return vmKernelCatalog{}, err
	}
	var catalog vmKernelCatalog
	if err := decodeStrictVMRuntimeJSON(raw, &catalog, "VM kernel catalog"); err != nil {
		return vmKernelCatalog{}, err
	}
	if err := catalog.validate(requireTrustedURL); err != nil {
		return vmKernelCatalog{}, err
	}
	return catalog, nil
}

func validateVMGuestBaseCatalogRequiredJSON(raw []byte) error {
	top, err := decodeVMRuntimeJSONObject(raw, "VM guest-base catalog")
	if err != nil {
		return err
	}
	if err := requireVMRuntimeJSONFields(top, "VM guest-base catalog", "schema_version", "guest_bases", "channels"); err != nil {
		return err
	}
	entries, err := decodeVMRuntimeJSONArray(top["guest_bases"], "VM guest-base catalog guest_bases")
	if err != nil {
		return err
	}
	for i, entry := range entries {
		if err := requireVMComponentJSONFields(entry, fmt.Sprintf("VM guest-base catalog entry %d", i), "guest_base_id", "os", "os_version", "architecture", "manifest_url", "manifest_sha256"); err != nil {
			return err
		}
	}
	return validateVMComponentChannelsRequiredJSON(top["channels"], "VM guest-base catalog channels", "guest_base_id")
}

func validateVMKernelCatalogRequiredJSON(raw []byte) error {
	top, err := decodeVMRuntimeJSONObject(raw, "VM kernel catalog")
	if err != nil {
		return err
	}
	if err := requireVMRuntimeJSONFields(top, "VM kernel catalog", "schema_version", "kernels", "channels"); err != nil {
		return err
	}
	entries, err := decodeVMRuntimeJSONArray(top["kernels"], "VM kernel catalog kernels")
	if err != nil {
		return err
	}
	for i, entry := range entries {
		if err := requireVMComponentJSONFields(entry, fmt.Sprintf("VM kernel catalog entry %d", i), "kernel_id", "upstream_version", "packaging_revision", "architecture", "manifest_url", "manifest_sha256"); err != nil {
			return err
		}
	}
	return validateVMComponentChannelsRequiredJSON(top["channels"], "VM kernel catalog channels", "kernel_id")
}

func validateVMComponentChannelsRequiredJSON(raw []byte, label, identityField string) error {
	channels, err := decodeVMRuntimeJSONObject(raw, label)
	if err != nil {
		return err
	}
	for family, familyRaw := range channels {
		familyChannels, err := decodeVMRuntimeJSONObject(familyRaw, label+" "+family)
		if err != nil {
			return err
		}
		if err := requireVMRuntimeJSONFields(familyChannels, label+" "+family, "stable", "candidate"); err != nil {
			return err
		}
		for _, channel := range []string{"stable", "candidate"} {
			identityRaw := bytes.TrimSpace(familyChannels[channel])
			if bytes.Equal(identityRaw, []byte("null")) {
				continue
			}
			if err := requireVMComponentJSONFields(identityRaw, label+" "+family+" "+channel, identityField, "manifest_sha256"); err != nil {
				return err
			}
		}
	}
	return nil
}

func requireVMComponentJSONFields(raw []byte, label string, fields ...string) error {
	object, err := decodeVMRuntimeJSONObject(raw, label)
	if err != nil {
		return err
	}
	return requireVMRuntimeJSONFields(object, label, fields...)
}

func (c vmGuestBaseCatalog) validate(requireTrustedURL bool) error {
	if c.SchemaVersion != 1 {
		return fmt.Errorf("unsupported VM guest-base catalog schema_version %d", c.SchemaVersion)
	}
	byID := make(map[string]vmGuestBaseCatalogRef, len(c.GuestBases))
	for _, ref := range c.GuestBases {
		if err := ref.validate(requireTrustedURL); err != nil {
			return err
		}
		if _, exists := byID[ref.GuestBaseID]; exists {
			return fmt.Errorf("duplicate VM guest-base catalog ID %q", ref.GuestBaseID)
		}
		byID[ref.GuestBaseID] = ref
	}
	wantFamilies := []string{"nixos-26.05-amd64", "ubuntu-26.04-amd64"}
	if !sameVMComponentKeys(c.Channels, wantFamilies) {
		return fmt.Errorf("VM guest-base catalog channels must contain exactly %s", strings.Join(wantFamilies, ", "))
	}
	for family, channels := range c.Channels {
		for channel, identity := range map[string]*vmGuestBaseCatalogIdentity{
			"stable": channels.Stable, "candidate": channels.Candidate,
		} {
			if identity == nil {
				continue
			}
			ref, ok := byID[identity.GuestBaseID]
			if !ok || ref.ManifestSHA256 != identity.ManifestSHA256 {
				return fmt.Errorf("VM guest-base catalog %s %s channel does not resolve to one immutable entry", family, channel)
			}
			if family != ref.OS+"-"+ref.OSVersion+"-"+ref.Architecture {
				return fmt.Errorf("VM guest-base catalog %s %s channel resolves to a different family", family, channel)
			}
		}
	}
	return nil
}

func (r vmGuestBaseCatalogRef) validate(requireTrustedURL bool) error {
	match := vmGuestBaseIDPattern.FindStringSubmatch(r.GuestBaseID)
	if match == nil {
		return fmt.Errorf("invalid VM guest-base ID %q", r.GuestBaseID)
	}
	if r.OS != match[1] || r.OSVersion != match[2] || !vmOSVersionPattern.MatchString(r.OSVersion) {
		return fmt.Errorf("VM guest-base %s identity fields do not match", r.GuestBaseID)
	}
	if r.Architecture != "amd64" {
		return fmt.Errorf("VM guest-base %s has unsupported architecture %q", r.GuestBaseID, r.Architecture)
	}
	if !vmRuntimeSHA256Pattern.MatchString(r.ManifestSHA256) {
		return fmt.Errorf("VM guest-base %s has invalid manifest_sha256", r.GuestBaseID)
	}
	if requireTrustedURL {
		want := "https://github.com/yeetrun/yeet-vm-images/releases/download/" + r.GuestBaseID + "/guest-manifest.json"
		if err := validateTrustedYeetVMArtifactURL(r.ManifestURL, "guest-base manifest"); err != nil {
			return err
		}
		if r.ManifestURL != want {
			return fmt.Errorf("untrusted VM guest-base manifest URL %q", r.ManifestURL)
		}
	}
	return nil
}

func (c vmKernelCatalog) validate(requireTrustedURL bool) error {
	if c.SchemaVersion != 1 {
		return fmt.Errorf("unsupported VM kernel catalog schema_version %d", c.SchemaVersion)
	}
	byID := make(map[string]vmKernelCatalogRef, len(c.Kernels))
	for _, ref := range c.Kernels {
		if err := ref.validate(requireTrustedURL); err != nil {
			return err
		}
		if _, exists := byID[ref.KernelID]; exists {
			return fmt.Errorf("duplicate VM kernel catalog ID %q", ref.KernelID)
		}
		byID[ref.KernelID] = ref
	}
	if !sameVMComponentKeys(c.Channels, []string{"amd64"}) {
		return fmt.Errorf("VM kernel catalog channels must contain exactly amd64")
	}
	for architecture, channels := range c.Channels {
		for channel, identity := range map[string]*vmKernelCatalogIdentity{
			"stable": channels.Stable, "candidate": channels.Candidate,
		} {
			if identity == nil {
				continue
			}
			ref, ok := byID[identity.KernelID]
			if !ok || ref.ManifestSHA256 != identity.ManifestSHA256 {
				return fmt.Errorf("VM kernel catalog %s %s channel does not resolve to one immutable entry", architecture, channel)
			}
			if ref.Architecture != architecture {
				return fmt.Errorf("VM kernel catalog %s %s channel resolves to a different architecture", architecture, channel)
			}
		}
	}
	return nil
}

func (r vmKernelCatalogRef) validate(requireTrustedURL bool) error {
	match := vmKernelIDPattern.FindStringSubmatch(r.KernelID)
	if match == nil {
		return fmt.Errorf("invalid VM kernel ID %q", r.KernelID)
	}
	if r.UpstreamVersion != match[1] {
		return fmt.Errorf("VM kernel %s upstream_version does not match its ID", r.KernelID)
	}
	var revision int
	if _, err := fmt.Sscan(match[2], &revision); err != nil || revision != r.PackagingRevision {
		return fmt.Errorf("VM kernel %s packaging_revision does not match its ID", r.KernelID)
	}
	if r.Architecture != "amd64" {
		return fmt.Errorf("VM kernel %s has unsupported architecture %q", r.KernelID, r.Architecture)
	}
	if !vmRuntimeSHA256Pattern.MatchString(r.ManifestSHA256) {
		return fmt.Errorf("VM kernel %s has invalid manifest_sha256", r.KernelID)
	}
	if requireTrustedURL {
		want := "https://github.com/yeetrun/yeet-vm-images/releases/download/" + r.KernelID + "/kernel-manifest.json"
		if err := validateTrustedYeetVMArtifactURL(r.ManifestURL, "kernel manifest"); err != nil {
			return err
		}
		if r.ManifestURL != want {
			return fmt.Errorf("untrusted VM kernel manifest URL %q", r.ManifestURL)
		}
	}
	return nil
}

func sameVMComponentKeys[V any](values map[string]V, want []string) bool {
	got := make([]string, 0, len(values))
	for key := range values {
		got = append(got, key)
	}
	slices.Sort(got)
	return slices.Equal(got, want)
}

func (c vmGuestBaseCatalog) GuestBaseByID(id string) (vmGuestBaseCatalogRef, bool) {
	for _, ref := range c.GuestBases {
		if ref.GuestBaseID == id {
			return ref, true
		}
	}
	return vmGuestBaseCatalogRef{}, false
}

func (c vmGuestBaseCatalog) GuestBaseForChannel(family, channel string) (vmGuestBaseCatalogRef, bool) {
	channels, ok := c.Channels[family]
	if !ok {
		return vmGuestBaseCatalogRef{}, false
	}
	var identity *vmGuestBaseCatalogIdentity
	switch channel {
	case "stable":
		identity = channels.Stable
	case "candidate":
		identity = channels.Candidate
	default:
		return vmGuestBaseCatalogRef{}, false
	}
	if identity == nil {
		return vmGuestBaseCatalogRef{}, false
	}
	ref, ok := c.GuestBaseByID(identity.GuestBaseID)
	return ref, ok && ref.ManifestSHA256 == identity.ManifestSHA256
}

func (c vmKernelCatalog) KernelByID(id string) (vmKernelCatalogRef, bool) {
	for _, ref := range c.Kernels {
		if ref.KernelID == id {
			return ref, true
		}
	}
	return vmKernelCatalogRef{}, false
}

func (c vmKernelCatalog) KernelForChannel(architecture, channel string) (vmKernelCatalogRef, bool) {
	channels, ok := c.Channels[architecture]
	if !ok {
		return vmKernelCatalogRef{}, false
	}
	var identity *vmKernelCatalogIdentity
	switch channel {
	case "stable":
		identity = channels.Stable
	case "candidate":
		identity = channels.Candidate
	default:
		return vmKernelCatalogRef{}, false
	}
	if identity == nil {
		return vmKernelCatalogRef{}, false
	}
	ref, ok := c.KernelByID(identity.KernelID)
	return ref, ok && ref.ManifestSHA256 == identity.ManifestSHA256
}

func fetchVMComponentCatalogs(ctx context.Context, client *http.Client, refs vmImageComponentCatalogs, requireTrustedURL bool) (vmComponentCatalogSet, error) {
	if err := refs.validate(requireTrustedURL); err != nil {
		return vmComponentCatalogSet{}, err
	}
	guestRaw, err := fetchVMComponentCatalogRaw(ctx, client, refs.GuestBases, "guest-base", requireTrustedURL)
	if err != nil {
		return vmComponentCatalogSet{}, err
	}
	guest, err := decodeVMGuestBaseCatalog(guestRaw, requireTrustedURL)
	if err != nil {
		return vmComponentCatalogSet{}, err
	}
	kernelRaw, err := fetchVMComponentCatalogRaw(ctx, client, refs.Kernels, "kernel", requireTrustedURL)
	if err != nil {
		return vmComponentCatalogSet{}, err
	}
	kernel, err := decodeVMKernelCatalog(kernelRaw, requireTrustedURL)
	if err != nil {
		return vmComponentCatalogSet{}, err
	}
	runtimes, err := fetchVMRuntimeCatalogFromURL(ctx, client, refs.Runtimes, requireTrustedURL)
	if err != nil {
		return vmComponentCatalogSet{}, err
	}
	return vmComponentCatalogSet{GuestBases: guest, Kernels: kernel, Runtimes: runtimes}, nil
}

func fetchVMComponentCatalogRaw(ctx context.Context, client *http.Client, rawURL, label string, requireTrustedURL bool) ([]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create VM %s catalog request: %w", label, err)
	}
	req.Header.Set("User-Agent", vmImageHTTPUserAgent)
	resp, err := trustedVMRuntimeHTTPClient(client, requireTrustedURL).Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch VM %s catalog: %w", label, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch VM %s catalog: %s", label, resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxVMComponentCatalogBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read VM %s catalog: %w", label, err)
	}
	if len(raw) > maxVMComponentCatalogBytes {
		return nil, fmt.Errorf("VM %s catalog exceeds %d byte limit", label, maxVMComponentCatalogBytes)
	}
	return bytes.Clone(raw), nil
}
