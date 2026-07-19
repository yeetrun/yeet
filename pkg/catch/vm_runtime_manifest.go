// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

const vmRuntimeSupportPolicyURL = "https://github.com/firecracker-microvm/firecracker/blob/main/docs/RELEASE_POLICY.md"

var (
	vmRuntimeIDPattern       = regexp.MustCompile(`^firecracker-(v[0-9]+\.[0-9]+\.[0-9]+)-yeet-v[1-9][0-9]*$`)
	vmRuntimeVersionPattern  = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
	vmRuntimeSHA256Pattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	vmRuntimeCommitPattern   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	vmRuntimeWorkflowPattern = regexp.MustCompile(`^[1-9][0-9]*$`)
	vmRuntimeSignerPattern   = regexp.MustCompile(`^([0-9A-F]{40}|[0-9A-F]{64})$`)
)

type vmRuntimeManifest struct {
	SchemaVersion  int                         `json:"schema_version"`
	RuntimeID      string                      `json:"runtime_id"`
	Architecture   string                      `json:"architecture"`
	Upstream       vmRuntimeManifestUpstream   `json:"upstream"`
	Components     vmRuntimeManifestComponents `json:"components"`
	Classification vmRuntimeManifestClass      `json:"classification"`
	Support        vmRuntimeManifestSupport    `json:"support"`
	Provenance     vmRuntimeManifestProvenance `json:"provenance"`
}

type vmRuntimeManifestUpstream struct {
	Repository    string                        `json:"repository"`
	Version       string                        `json:"version"`
	Tag           string                        `json:"tag"`
	Commit        string                        `json:"commit"`
	ArchiveURL    string                        `json:"archive_url"`
	ArchiveSHA256 string                        `json:"archive_sha256"`
	ChecksumURL   string                        `json:"checksum_url"`
	TagSignature  vmRuntimeManifestTagSignature `json:"tag_signature"`
}

type vmRuntimeManifestTagSignature struct {
	Status      string  `json:"status"`
	Fingerprint *string `json:"fingerprint"`
}

type vmRuntimeManifestComponent struct {
	Path          string `json:"path"`
	SHA256        string `json:"sha256"`
	VersionOutput string `json:"version_output"`
}

type vmRuntimeManifestComponents struct {
	Firecracker vmRuntimeManifestComponent `json:"firecracker"`
	Jailer      vmRuntimeManifestComponent `json:"jailer"`
}

type vmRuntimeManifestClass struct {
	ProductionRelease bool `json:"production_release"`
	DefaultSeccomp    bool `json:"default_seccomp"`
}

type vmRuntimeManifestSupport struct {
	State     string `json:"state"`
	PolicyURL string `json:"policy_url"`
}

type vmRuntimeManifestProvenance struct {
	Repository  string `json:"repository"`
	Commit      string `json:"commit"`
	WorkflowRun string `json:"workflow_run"`
}

func decodeVMRuntimeManifest(raw []byte) (vmRuntimeManifest, error) {
	if err := validateVMRuntimeManifestRequiredJSON(raw); err != nil {
		return vmRuntimeManifest{}, err
	}
	var manifest vmRuntimeManifest
	if err := decodeStrictVMRuntimeJSON(raw, &manifest, "VM runtime manifest"); err != nil {
		return vmRuntimeManifest{}, err
	}
	if err := manifest.validate(); err != nil {
		return vmRuntimeManifest{}, err
	}
	architecture, err := normalizeVMRuntimeArchitecture(manifest.Architecture)
	if err != nil {
		return vmRuntimeManifest{}, err
	}
	manifest.Architecture = architecture
	return manifest, nil
}

func validateVMRuntimeManifestRequiredJSON(raw []byte) error {
	top, err := decodeVMRuntimeJSONObject(raw, "VM runtime manifest")
	if err != nil {
		return err
	}
	if err := requireVMRuntimeJSONFields(top, "VM runtime manifest", "schema_version", "runtime_id", "architecture", "upstream", "components", "classification", "support", "provenance"); err != nil {
		return err
	}
	if err := validateVMRuntimeUpstreamRequiredJSON(top["upstream"]); err != nil {
		return err
	}
	if err := validateVMRuntimeComponentsRequiredJSON(top["components"]); err != nil {
		return err
	}
	for field, required := range map[string][]string{
		"classification": {"production_release", "default_seccomp"},
		"support":        {"state", "policy_url"},
		"provenance":     {"repository", "commit", "workflow_run"},
	} {
		if err := validateVMRuntimeRequiredJSONObject(top[field], "VM runtime manifest "+field, required...); err != nil {
			return err
		}
	}
	return nil
}

func validateVMRuntimeUpstreamRequiredJSON(raw []byte) error {
	upstream, err := decodeVMRuntimeJSONObject(raw, "VM runtime manifest upstream")
	if err != nil {
		return err
	}
	if err := requireVMRuntimeJSONFields(upstream, "VM runtime manifest upstream", "repository", "version", "tag", "commit", "archive_url", "archive_sha256", "checksum_url", "tag_signature"); err != nil {
		return err
	}
	signature, err := decodeVMRuntimeJSONObject(upstream["tag_signature"], "VM runtime manifest tag_signature")
	if err != nil {
		return err
	}
	if err := requireVMRuntimeJSONFields(signature, "VM runtime manifest tag_signature", "status", "fingerprint"); err != nil {
		return err
	}
	return nil
}

func validateVMRuntimeComponentsRequiredJSON(raw []byte) error {
	components, err := decodeVMRuntimeJSONObject(raw, "VM runtime manifest components")
	if err != nil {
		return err
	}
	if err := requireVMRuntimeJSONFields(components, "VM runtime manifest components", "firecracker", "jailer"); err != nil {
		return err
	}
	for _, name := range []string{"firecracker", "jailer"} {
		component, err := decodeVMRuntimeJSONObject(components[name], "VM runtime manifest "+name)
		if err != nil {
			return err
		}
		if err := requireVMRuntimeJSONFields(component, "VM runtime manifest "+name, "path", "sha256", "version_output"); err != nil {
			return err
		}
	}
	return nil
}

func validateVMRuntimeRequiredJSONObject(raw []byte, label string, fields ...string) error {
	object, err := decodeVMRuntimeJSONObject(raw, label)
	if err != nil {
		return err
	}
	return requireVMRuntimeJSONFields(object, label, fields...)
}

func decodeStrictVMRuntimeJSON(raw []byte, target any, label string) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", label, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode %s: multiple JSON values", label)
		}
		return fmt.Errorf("decode %s trailing data: %w", label, err)
	}
	return nil
}

func decodeVMRuntimeJSONObject(raw []byte, label string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("decode %s: %w", label, err)
	}
	if object == nil {
		return nil, fmt.Errorf("%s must be a JSON object", label)
	}
	return object, nil
}

func requireVMRuntimeJSONFields(object map[string]json.RawMessage, label string, fields ...string) error {
	for _, field := range fields {
		if _, ok := object[field]; !ok {
			return fmt.Errorf("%s missing required field %q", label, field)
		}
	}
	return nil
}

func (m vmRuntimeManifest) validate() error {
	if m.SchemaVersion != 1 {
		return fmt.Errorf("unsupported VM runtime manifest schema_version %d", m.SchemaVersion)
	}
	version, err := vmRuntimeVersionFromID(m.RuntimeID)
	if err != nil {
		return err
	}
	if _, err := normalizeVMRuntimeArchitecture(m.Architecture); err != nil {
		return err
	}
	if err := m.Upstream.validate(version); err != nil {
		return err
	}
	if err := m.Components.validate(version); err != nil {
		return err
	}
	if err := m.validateClassificationAndSupport(); err != nil {
		return err
	}
	return m.Provenance.validate()
}

func (m vmRuntimeManifest) validateClassificationAndSupport() error {
	if !m.Classification.ProductionRelease || !m.Classification.DefaultSeccomp {
		return fmt.Errorf("VM runtime manifest requires production_release and default_seccomp classification")
	}
	if !validVMRuntimeSupport(m.Support.State) {
		return fmt.Errorf("VM runtime manifest has unsupported support state %q", m.Support.State)
	}
	if m.Support.PolicyURL != vmRuntimeSupportPolicyURL {
		return fmt.Errorf("VM runtime manifest has invalid support policy_url %q", m.Support.PolicyURL)
	}
	return nil
}

func (p vmRuntimeManifestProvenance) validate() error {
	if p.Repository != "yeetrun/yeet-vm-images" {
		return fmt.Errorf("VM runtime manifest has invalid provenance repository %q", p.Repository)
	}
	if !vmRuntimeCommitPattern.MatchString(p.Commit) {
		return fmt.Errorf("VM runtime manifest has invalid provenance commit %q", p.Commit)
	}
	if !vmRuntimeWorkflowPattern.MatchString(p.WorkflowRun) {
		return fmt.Errorf("VM runtime manifest has invalid provenance workflow_run %q", p.WorkflowRun)
	}
	return nil
}

func (u vmRuntimeManifestUpstream) validate(version string) error {
	if u.Repository != "firecracker-microvm/firecracker" {
		return fmt.Errorf("VM runtime manifest has invalid upstream repository %q", u.Repository)
	}
	if !vmRuntimeVersionPattern.MatchString(u.Version) || u.Version != version || u.Tag != version {
		return fmt.Errorf("VM runtime manifest upstream version/tag %q/%q does not match runtime %s", u.Version, u.Tag, version)
	}
	if !vmRuntimeCommitPattern.MatchString(u.Commit) {
		return fmt.Errorf("VM runtime manifest has invalid upstream commit %q", u.Commit)
	}
	wantArchive := "https://github.com/firecracker-microvm/firecracker/releases/download/" + version + "/firecracker-" + version + "-x86_64.tgz"
	if u.ArchiveURL != wantArchive {
		return fmt.Errorf("VM runtime manifest has invalid upstream archive_url %q", u.ArchiveURL)
	}
	if !validVMRuntimeSHA256(u.ArchiveSHA256) {
		return fmt.Errorf("VM runtime manifest has invalid upstream archive_sha256 %q", u.ArchiveSHA256)
	}
	if u.ChecksumURL != wantArchive+".sha256.txt" {
		return fmt.Errorf("VM runtime manifest has invalid upstream checksum_url %q", u.ChecksumURL)
	}
	return u.TagSignature.validate()
}

func (s vmRuntimeManifestTagSignature) validate() error {
	switch s.Status {
	case "unsigned-approved":
		if s.Fingerprint != nil {
			return fmt.Errorf("VM runtime manifest unsigned tag signature must have null fingerprint")
		}
	case "signed", "signer-rotation-approved":
		if s.Fingerprint == nil || !vmRuntimeSignerPattern.MatchString(*s.Fingerprint) {
			return fmt.Errorf("VM runtime manifest has invalid tag signature fingerprint")
		}
	default:
		return fmt.Errorf("VM runtime manifest has unsupported tag signature status %q", s.Status)
	}
	return nil
}

func (c vmRuntimeManifestComponents) validate(version string) error {
	if err := c.Firecracker.validate("firecracker", "Firecracker "+version); err != nil {
		return err
	}
	return c.Jailer.validate("jailer", "Jailer "+version)
}

func (c vmRuntimeManifestComponent) validate(name, versionOutput string) error {
	if c.Path != name {
		return fmt.Errorf("VM runtime manifest %s path %q must be %q", name, c.Path, name)
	}
	if !validVMRuntimeSHA256(c.SHA256) {
		return fmt.Errorf("VM runtime manifest has invalid %s sha256 %q", name, c.SHA256)
	}
	if c.VersionOutput != versionOutput {
		return fmt.Errorf("VM runtime manifest %s version_output %q must be %q", name, c.VersionOutput, versionOutput)
	}
	return nil
}

func vmRuntimeVersionFromID(runtimeID string) (string, error) {
	match := vmRuntimeIDPattern.FindStringSubmatch(runtimeID)
	if len(match) != 2 {
		return "", fmt.Errorf("invalid VM runtime ID %q", runtimeID)
	}
	return match[1], nil
}

func normalizeVMRuntimeArchitecture(architecture string) (string, error) {
	switch architecture {
	case "amd64":
		return architecture, nil
	case "x86_64":
		return "amd64", nil
	default:
		return "", fmt.Errorf("unsupported VM runtime architecture %q", architecture)
	}
}

func validVMRuntimeSHA256(digest string) bool {
	return vmRuntimeSHA256Pattern.MatchString(digest)
}

func validVMRuntimeSupport(support string) bool {
	switch support {
	case "supported", "deprecated", "eol", "revoked":
		return true
	default:
		return false
	}
}

func trimmedVMRuntimeVersionOutput(output string) string {
	return strings.TrimSpace(output)
}
