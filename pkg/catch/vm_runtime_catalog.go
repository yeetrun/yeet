// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const (
	defaultVMRuntimeCatalogURL = "https://raw.githubusercontent.com/yeetrun/yeet-vm-images/main/runtime-catalog.json"
	maxVMRuntimeCatalogBytes   = 4 << 20
)

type vmRuntimeCatalogRef struct {
	RuntimeID       string `json:"runtime_id"`
	ManifestURL     string `json:"manifest_url"`
	ManifestSHA     string `json:"manifest_sha256"`
	UpstreamVersion string `json:"upstream_version"`
	Support         string `json:"support"`
	IntegrationURL  string `json:"integration_attestation_url"`
	IntegrationSHA  string `json:"integration_attestation_sha256"`
	CanaryURL       string `json:"canary_attestation_url,omitempty"`
	CanarySHA       string `json:"canary_attestation_sha256,omitempty"`
}

type vmRuntimeCatalogIdentity struct {
	RuntimeID   string `json:"runtime_id"`
	ManifestSHA string `json:"manifest_sha256"`
}

type vmRuntimeCatalogArchitecture struct {
	Runtimes []vmRuntimeCatalogRef                `json:"runtimes"`
	Channels map[string]*vmRuntimeCatalogIdentity `json:"channels"`
}

type vmRuntimeRevocation struct {
	RuntimeID   string `json:"runtime_id"`
	ManifestSHA string `json:"manifest_sha256"`
	Reason      string `json:"reason"`
	RecordedAt  string `json:"recorded_at"`
}

type vmRuntimeCatalog struct {
	SchemaVersion int                                     `json:"schema_version"`
	Architectures map[string]vmRuntimeCatalogArchitecture `json:"architectures"`
	Revocations   []vmRuntimeRevocation                   `json:"revocations"`
}

func decodeVMRuntimeCatalog(raw []byte, requireTrustedURL bool) (vmRuntimeCatalog, error) {
	if err := validateVMRuntimeCatalogRequiredJSON(raw); err != nil {
		return vmRuntimeCatalog{}, err
	}
	var catalog vmRuntimeCatalog
	if err := decodeStrictVMRuntimeJSON(raw, &catalog, "VM runtime catalog"); err != nil {
		return vmRuntimeCatalog{}, err
	}
	if err := catalog.normalizeArchitectures(); err != nil {
		return vmRuntimeCatalog{}, err
	}
	if err := catalog.validate(requireTrustedURL); err != nil {
		return vmRuntimeCatalog{}, err
	}
	return catalog, nil
}

func validateVMRuntimeCatalogRequiredJSON(raw []byte) error {
	top, err := decodeVMRuntimeJSONObject(raw, "VM runtime catalog")
	if err != nil {
		return err
	}
	if err := requireVMRuntimeJSONFields(top, "VM runtime catalog", "schema_version", "architectures", "revocations"); err != nil {
		return err
	}
	architectures, err := decodeVMRuntimeJSONObject(top["architectures"], "VM runtime catalog architectures")
	if err != nil {
		return err
	}
	for name, rawArchitecture := range architectures {
		if err := validateVMRuntimeArchitectureRequiredJSON(name, rawArchitecture); err != nil {
			return err
		}
	}
	return validateVMRuntimeRevocationsRequiredJSON(top["revocations"])
}

func validateVMRuntimeArchitectureRequiredJSON(name string, raw []byte) error {
	label := "VM runtime catalog architecture " + name
	architecture, err := decodeVMRuntimeJSONObject(raw, label)
	if err != nil {
		return err
	}
	if err := requireVMRuntimeJSONFields(architecture, label, "runtimes", "channels"); err != nil {
		return err
	}
	if err := validateVMRuntimeEntriesRequiredJSON(name, architecture["runtimes"]); err != nil {
		return err
	}
	return validateVMRuntimeChannelsRequiredJSON(name, architecture["channels"])
}

func validateVMRuntimeEntriesRequiredJSON(architecture string, raw []byte) error {
	label := fmt.Sprintf("VM runtime catalog architecture %s runtimes", architecture)
	runtimes, err := decodeVMRuntimeJSONArray(raw, label)
	if err != nil {
		return err
	}
	for index, rawRuntime := range runtimes {
		label := fmt.Sprintf("VM runtime catalog architecture %s runtime %d", architecture, index)
		if err := validateVMRuntimeRequiredJSONObject(
			rawRuntime, label,
			"runtime_id", "manifest_url", "manifest_sha256", "upstream_version", "support",
			"integration_attestation_url", "integration_attestation_sha256",
			"canary_attestation_url", "canary_attestation_sha256",
		); err != nil {
			return err
		}
	}
	return nil
}

func validateVMRuntimeChannelsRequiredJSON(architecture string, raw []byte) error {
	channels, err := decodeVMRuntimeJSONObject(raw, "VM runtime catalog architecture "+architecture+" channels")
	if err != nil {
		return err
	}
	for _, channel := range []string{"stable", "candidate"} {
		rawIdentity, ok := channels[channel]
		if !ok || string(rawIdentity) == "null" {
			continue
		}
		if err := validateVMRuntimeRequiredJSONObject(rawIdentity, "VM runtime catalog "+channel+" channel", "runtime_id", "manifest_sha256"); err != nil {
			return err
		}
	}
	return nil
}

func validateVMRuntimeRevocationsRequiredJSON(raw []byte) error {
	revocations, err := decodeVMRuntimeJSONArray(raw, "VM runtime catalog revocations")
	if err != nil {
		return err
	}
	for index, rawRevocation := range revocations {
		label := fmt.Sprintf("VM runtime catalog revocation %d", index)
		if err := validateVMRuntimeRequiredJSONObject(rawRevocation, label, "runtime_id", "manifest_sha256", "reason", "recorded_at"); err != nil {
			return err
		}
	}
	return nil
}

func decodeVMRuntimeJSONArray(raw []byte, label string) ([]json.RawMessage, error) {
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("%s must be a JSON array: %w", label, err)
	}
	if values == nil {
		return nil, fmt.Errorf("%s must be a JSON array", label)
	}
	return values, nil
}

func (c *vmRuntimeCatalog) normalizeArchitectures() error {
	if c.Architectures == nil {
		return fmt.Errorf("VM runtime catalog missing architectures")
	}
	architecture, hasAMD64 := c.Architectures["amd64"]
	alias, hasX8664 := c.Architectures["x86_64"]
	if hasAMD64 && hasX8664 {
		return fmt.Errorf("VM runtime catalog has duplicate amd64/x86_64 architectures")
	}
	if hasX8664 {
		delete(c.Architectures, "x86_64")
		c.Architectures["amd64"] = alias
		architecture = alias
		hasAMD64 = true
	}
	if !hasAMD64 {
		return fmt.Errorf("VM runtime catalog missing amd64 architecture")
	}
	if len(c.Architectures) != 1 {
		for name := range c.Architectures {
			if name != "amd64" {
				return fmt.Errorf("unsupported VM runtime catalog architecture %q", name)
			}
		}
	}
	c.Architectures["amd64"] = architecture
	return nil
}

func (c vmRuntimeCatalog) validate(requireTrustedURL bool) error {
	if c.SchemaVersion != 1 {
		return fmt.Errorf("unsupported VM runtime catalog schema_version %d", c.SchemaVersion)
	}
	architecture, ok := c.Architectures["amd64"]
	if !ok || len(c.Architectures) != 1 {
		return fmt.Errorf("VM runtime catalog requires only amd64 architecture")
	}
	entries := make(map[string]vmRuntimeCatalogRef, len(architecture.Runtimes))
	ids := make(map[string]struct{}, len(architecture.Runtimes))
	for _, ref := range architecture.Runtimes {
		if err := ref.validate(requireTrustedURL); err != nil {
			return err
		}
		key := vmRuntimeCatalogKey(ref.RuntimeID, ref.ManifestSHA)
		if _, exists := entries[key]; exists {
			return fmt.Errorf("duplicate VM runtime catalog entry %s", key)
		}
		if _, exists := ids[ref.RuntimeID]; exists {
			return fmt.Errorf("duplicate VM runtime catalog runtime_id %q", ref.RuntimeID)
		}
		entries[key] = ref
		ids[ref.RuntimeID] = struct{}{}
	}
	if err := validateVMRuntimeChannels(architecture.Channels, entries); err != nil {
		return err
	}
	return validateVMRuntimeRevocations(c.Revocations, entries, architecture.Channels)
}

func (r vmRuntimeCatalogRef) validate(requireTrustedURL bool) error {
	version, err := vmRuntimeVersionFromID(r.RuntimeID)
	if err != nil {
		return err
	}
	if r.UpstreamVersion != version {
		return fmt.Errorf("VM runtime catalog entry %s upstream_version %q does not match %s", r.RuntimeID, r.UpstreamVersion, version)
	}
	if !validVMRuntimeSHA256(r.ManifestSHA) {
		return fmt.Errorf("VM runtime catalog entry %s has invalid manifest_sha256 %q", r.RuntimeID, r.ManifestSHA)
	}
	if !validVMRuntimeSupport(r.Support) {
		return fmt.Errorf("VM runtime catalog entry %s has unsupported support %q", r.RuntimeID, r.Support)
	}
	if err := validateVMRuntimeDownloadURL(r.ManifestURL, "manifest", r.RuntimeID, requireTrustedURL); err != nil {
		return err
	}
	if err := validateVMRuntimeAttestationPair(r.IntegrationURL, r.IntegrationSHA, "integration", r.RuntimeID, requireTrustedURL); err != nil {
		return err
	}
	return validateVMRuntimeAttestationPair(r.CanaryURL, r.CanarySHA, "canary", r.RuntimeID, requireTrustedURL)
}

func validateVMRuntimeChannels(channels map[string]*vmRuntimeCatalogIdentity, entries map[string]vmRuntimeCatalogRef) error {
	if len(channels) != 2 {
		return fmt.Errorf("VM runtime catalog channels must contain stable and candidate")
	}
	for name := range channels {
		if name != "stable" && name != "candidate" {
			return fmt.Errorf("VM runtime catalog has unsupported channel %q", name)
		}
	}
	for _, name := range []string{"stable", "candidate"} {
		if err := validateVMRuntimeChannel(name, channels, entries); err != nil {
			return err
		}
	}
	return nil
}

func validateVMRuntimeChannel(name string, channels map[string]*vmRuntimeCatalogIdentity, entries map[string]vmRuntimeCatalogRef) error {
	identity, present := channels[name]
	if !present {
		return fmt.Errorf("VM runtime catalog missing %s channel", name)
	}
	if identity == nil {
		return nil
	}
	if _, err := vmRuntimeVersionFromID(identity.RuntimeID); err != nil {
		return fmt.Errorf("VM runtime catalog %s channel: %w", name, err)
	}
	if !validVMRuntimeSHA256(identity.ManifestSHA) {
		return fmt.Errorf("VM runtime catalog %s channel has invalid manifest_sha256", name)
	}
	entry, ok := entries[vmRuntimeCatalogKey(identity.RuntimeID, identity.ManifestSHA)]
	if !ok {
		return fmt.Errorf("VM runtime catalog %s channel does not resolve exactly", name)
	}
	return validateVMRuntimeChannelEvidence(name, identity.RuntimeID, entry)
}

func validateVMRuntimeChannelEvidence(name, runtimeID string, entry vmRuntimeCatalogRef) error {
	if entry.Support == "revoked" {
		return fmt.Errorf("VM runtime catalog %s channel references revoked runtime %s", name, runtimeID)
	}
	if entry.IntegrationURL == "" || entry.IntegrationSHA == "" {
		return fmt.Errorf("VM runtime catalog %s channel runtime lacks integration evidence", name)
	}
	if name == "stable" && (entry.CanaryURL == "" || entry.CanarySHA == "") {
		return fmt.Errorf("VM runtime catalog stable channel runtime lacks canary evidence")
	}
	return nil
}

func validateVMRuntimeRevocations(revocations []vmRuntimeRevocation, entries map[string]vmRuntimeCatalogRef, channels map[string]*vmRuntimeCatalogIdentity) error {
	seen := make(map[string]struct{}, len(revocations))
	for _, revocation := range revocations {
		if _, exists := seen[revocation.RuntimeID]; exists {
			return fmt.Errorf("duplicate VM runtime revocation %s", revocation.RuntimeID)
		}
		if err := validateVMRuntimeRevocation(revocation, entries, channels); err != nil {
			return err
		}
		seen[revocation.RuntimeID] = struct{}{}
	}
	return validateVMRuntimeRevokedEntries(entries, seen)
}

func validateVMRuntimeRevocation(revocation vmRuntimeRevocation, entries map[string]vmRuntimeCatalogRef, channels map[string]*vmRuntimeCatalogIdentity) error {
	if _, err := vmRuntimeVersionFromID(revocation.RuntimeID); err != nil {
		return fmt.Errorf("VM runtime revocation: %w", err)
	}
	if !validVMRuntimeSHA256(revocation.ManifestSHA) {
		return fmt.Errorf("VM runtime revocation %s has invalid manifest_sha256", revocation.RuntimeID)
	}
	if strings.TrimSpace(revocation.Reason) == "" || revocation.Reason != strings.TrimSpace(revocation.Reason) {
		return fmt.Errorf("VM runtime revocation %s has invalid reason", revocation.RuntimeID)
	}
	if _, err := time.Parse(time.RFC3339, revocation.RecordedAt); err != nil {
		return fmt.Errorf("VM runtime revocation %s has invalid recorded_at: %w", revocation.RuntimeID, err)
	}
	entry, ok := entries[vmRuntimeCatalogKey(revocation.RuntimeID, revocation.ManifestSHA)]
	if !ok || entry.Support != "revoked" {
		return fmt.Errorf("VM runtime revocation %s does not resolve to a revoked entry", revocation.RuntimeID)
	}
	return validateVMRuntimeRevocationChannels(revocation, channels)
}

func validateVMRuntimeRevocationChannels(revocation vmRuntimeRevocation, channels map[string]*vmRuntimeCatalogIdentity) error {
	for channel, identity := range channels {
		if identity != nil && identity.RuntimeID == revocation.RuntimeID && identity.ManifestSHA == revocation.ManifestSHA {
			return fmt.Errorf("VM runtime revocation %s remains on %s channel", revocation.RuntimeID, channel)
		}
	}
	return nil
}

func validateVMRuntimeRevokedEntries(entries map[string]vmRuntimeCatalogRef, seen map[string]struct{}) error {
	for key, entry := range entries {
		if entry.Support == "revoked" {
			if _, ok := seen[entry.RuntimeID]; !ok {
				return fmt.Errorf("revoked VM runtime catalog entry %s lacks revocation", key)
			}
		}
	}
	return nil
}

func validateVMRuntimeAttestationPair(rawURL, digest, kind, runtimeID string, requireTrustedURL bool) error {
	if (rawURL == "") != (digest == "") {
		return fmt.Errorf("VM runtime catalog entry %s must provide both %s attestation URL and digest", runtimeID, kind)
	}
	if rawURL == "" {
		return nil
	}
	if !validVMRuntimeSHA256(digest) {
		return fmt.Errorf("VM runtime catalog entry %s has invalid %s attestation digest", runtimeID, kind)
	}
	return validateVMRuntimeDownloadURL(rawURL, kind+" attestation", runtimeID, requireTrustedURL)
}

func validateVMRuntimeDownloadURL(rawURL, kind, runtimeID string, requireTrustedURL bool) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("invalid VM runtime %s URL %q", kind, rawURL)
	}
	if !requireTrustedURL {
		return validateVMRuntimeTestDownloadURL(u, kind, rawURL)
	}
	if err := validateTrustedYeetVMArtifactURL(rawURL, "runtime "+kind); err != nil {
		return err
	}
	return validateTrustedVMRuntimeDownloadPath(u, rawURL, kind, runtimeID)
}

func validateVMRuntimeTestDownloadURL(u *url.URL, kind, rawURL string) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("invalid VM runtime %s URL %q", kind, rawURL)
	}
	return nil
}

func validateTrustedVMRuntimeDownloadPath(u *url.URL, rawURL, kind, runtimeID string) error {
	cleanPath, err := trustedVMImageRepoPath(u)
	if err != nil {
		return fmt.Errorf("untrusted VM runtime %s URL %q: %w", kind, rawURL, err)
	}
	release, asset, attestation := vmRuntimeReleaseAsset(kind, runtimeID)
	prefix := "/yeetrun/yeet-vm-images/releases/download/" + release
	if u.Host != "github.com" || u.RawQuery != "" || !strings.HasPrefix(cleanPath, prefix) {
		return fmt.Errorf("untrusted VM runtime %s URL %q", kind, rawURL)
	}
	if !attestation && cleanPath == prefix+"/"+asset {
		return nil
	}
	if attestation && validVMRuntimeAttestationPath(strings.TrimPrefix(cleanPath, prefix), asset) {
		return nil
	}
	return fmt.Errorf("untrusted VM runtime %s URL %q", kind, rawURL)
}

func vmRuntimeReleaseAsset(kind, runtimeID string) (release, asset string, attestation bool) {
	switch {
	case strings.HasPrefix(kind, "integration"):
		return runtimeID + "-integration-", "runtime-attestation.json", true
	case strings.HasPrefix(kind, "canary"):
		return runtimeID + "-canary-", "runtime-attestation.json", true
	default:
		return runtimeID, "runtime-manifest.json", false
	}
}

func validVMRuntimeAttestationPath(suffix, asset string) bool {
	runID, rest, ok := strings.Cut(suffix, "/")
	return ok && vmRuntimeWorkflowPattern.MatchString(runID) && rest == asset
}

func (c vmRuntimeCatalog) RuntimeByID(architecture, runtimeID string) (vmRuntimeCatalogRef, bool) {
	architecture, err := normalizeVMRuntimeArchitecture(architecture)
	if err != nil {
		return vmRuntimeCatalogRef{}, false
	}
	entries, ok := c.Architectures[architecture]
	if !ok {
		return vmRuntimeCatalogRef{}, false
	}
	var found vmRuntimeCatalogRef
	matches := 0
	for _, ref := range entries.Runtimes {
		if ref.RuntimeID == runtimeID {
			found = ref
			matches++
		}
	}
	return found, matches == 1
}

func (c vmRuntimeCatalog) RuntimeForUpstreamVersion(architecture, upstreamVersion string) (vmRuntimeCatalogRef, bool) {
	architecture, err := normalizeVMRuntimeArchitecture(architecture)
	if err != nil {
		return vmRuntimeCatalogRef{}, false
	}
	entries, ok := c.Architectures[architecture]
	if !ok {
		return vmRuntimeCatalogRef{}, false
	}
	var found vmRuntimeCatalogRef
	foundRevision := 0
	for _, ref := range entries.Runtimes {
		if ref.UpstreamVersion != upstreamVersion || ref.Support == "revoked" {
			continue
		}
		revision, err := vmRuntimePolicyPackagingRevision(ref.RuntimeID)
		if err != nil {
			continue
		}
		if revision > foundRevision {
			found = ref
			foundRevision = revision
		}
	}
	return found, foundRevision != 0
}

func (c vmRuntimeCatalog) RuntimeForChannel(architecture, channel string) (vmRuntimeCatalogRef, bool) {
	architecture, err := normalizeVMRuntimeArchitecture(architecture)
	if err != nil {
		return vmRuntimeCatalogRef{}, false
	}
	entries, ok := c.Architectures[architecture]
	if !ok {
		return vmRuntimeCatalogRef{}, false
	}
	identity, ok := entries.Channels[channel]
	if !ok || identity == nil {
		return vmRuntimeCatalogRef{}, false
	}
	for _, ref := range entries.Runtimes {
		if ref.RuntimeID == identity.RuntimeID && ref.ManifestSHA == identity.ManifestSHA {
			return ref, true
		}
	}
	return vmRuntimeCatalogRef{}, false
}

func fetchVMRuntimeCatalogFromURL(ctx context.Context, client *http.Client, rawURL string, requireTrustedURL bool) (vmRuntimeCatalog, error) {
	if requireTrustedURL {
		if err := validateTrustedVMRuntimeCatalogURL(rawURL); err != nil {
			return vmRuntimeCatalog{}, err
		}
	}
	if client == nil {
		client = http.DefaultClient
	}
	client = trustedVMRuntimeHTTPClient(client, requireTrustedURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return vmRuntimeCatalog{}, fmt.Errorf("create VM runtime catalog request: %w", err)
	}
	req.Header.Set("User-Agent", vmImageHTTPUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return vmRuntimeCatalog{}, fmt.Errorf("fetch VM runtime catalog: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return vmRuntimeCatalog{}, fmt.Errorf("fetch VM runtime catalog: %s", resp.Status)
	}
	raw, err := readLimitedVMRuntimeResponse(resp.Body, maxVMRuntimeCatalogBytes, "VM runtime catalog")
	if err != nil {
		return vmRuntimeCatalog{}, err
	}
	return decodeVMRuntimeCatalog(raw, requireTrustedURL)
}

func trustedVMRuntimeHTTPClient(client *http.Client, requireTrustedURL bool) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	if !requireTrustedURL {
		return client
	}
	trustedClient := *client
	previousPolicy := client.CheckRedirect
	trustedClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := validateTrustedVMRuntimeRedirect(req, via); err != nil {
			return err
		}
		if previousPolicy != nil {
			return previousPolicy(req, via)
		}
		return nil
	}
	return &trustedClient
}

func validateTrustedVMRuntimeRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 || len(via) >= 5 {
		return fmt.Errorf("reject VM runtime redirect chain")
	}
	if req.URL.Scheme != "https" || req.URL.User != nil || req.URL.Port() != "" || req.URL.Fragment != "" {
		return fmt.Errorf("reject untrusted VM runtime redirect to %s", req.URL.Redacted())
	}
	initialHost := via[0].URL.Hostname()
	targetHost := req.URL.Hostname()
	if trustedVMRuntimeRedirectHost(initialHost, targetHost, req.URL.String()) {
		return nil
	}
	return fmt.Errorf("reject untrusted VM runtime redirect from %s to %s", initialHost, targetHost)
}

func trustedVMRuntimeRedirectHost(initialHost, targetHost, rawURL string) bool {
	switch initialHost {
	case "raw.githubusercontent.com":
		return targetHost == initialHost && validateTrustedYeetVMArtifactURL(rawURL, "runtime redirect") == nil
	case "github.com":
		return targetHost == "release-assets.githubusercontent.com" ||
			targetHost == initialHost && validateTrustedYeetVMArtifactURL(rawURL, "runtime redirect") == nil
	default:
		return false
	}
}

func validateTrustedVMRuntimeCatalogURL(rawURL string) error {
	if err := validateTrustedYeetVMArtifactURL(rawURL, "runtime catalog"); err != nil {
		return err
	}
	if rawURL != defaultVMRuntimeCatalogURL {
		return fmt.Errorf("untrusted VM runtime catalog URL %q", rawURL)
	}
	return nil
}

func readLimitedVMRuntimeResponse(reader io.Reader, limit int64, label string) ([]byte, error) {
	limited := io.LimitReader(reader, limit+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", label, err)
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("%s exceeds %d byte limit", label, limit)
	}
	return raw, nil
}

func vmRuntimeCatalogKey(runtimeID, manifestSHA string) string {
	return runtimeID + "/" + manifestSHA
}

func vmRuntimeArtifactURL(manifestURL, artifactName string) (string, error) {
	u, err := url.Parse(manifestURL)
	if err != nil {
		return "", fmt.Errorf("parse VM runtime manifest URL: %w", err)
	}
	u.Path = path.Join(path.Dir(u.Path), url.PathEscape(artifactName))
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}
