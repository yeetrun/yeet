// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestVMRuntimeManifestAcceptsStrictSchema(t *testing.T) {
	raw := marshalVMRuntimeTestJSON(t, validVMRuntimeManifest())
	manifest, err := decodeVMRuntimeManifest(raw)
	if err != nil {
		t.Fatalf("decodeVMRuntimeManifest: %v", err)
	}
	if manifest.RuntimeID != "firecracker-v1.16.1-yeet-v1" || manifest.Architecture != "amd64" {
		t.Fatalf("manifest = %#v", manifest)
	}
}

func TestVMRuntimeManifestRejectsUnknownFields(t *testing.T) {
	raw := addVMRuntimeTestJSONField(t, marshalVMRuntimeTestJSON(t, validVMRuntimeManifest()), "unexpected", true)
	if _, err := decodeVMRuntimeManifest(raw); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("decodeVMRuntimeManifest error = %v, want unknown field", err)
	}
}

func TestVMRuntimeManifestRejectsUnsupportedSchema(t *testing.T) {
	manifest := validVMRuntimeManifest()
	manifest.SchemaVersion = 2
	if _, err := decodeVMRuntimeManifest(marshalVMRuntimeTestJSON(t, manifest)); err == nil || !strings.Contains(err.Error(), "schema_version") {
		t.Fatalf("decodeVMRuntimeManifest error = %v, want schema_version", err)
	}
}

func TestVMRuntimeManifestRejectsMissingRequiredField(t *testing.T) {
	raw := marshalVMRuntimeTestJSON(t, validVMRuntimeManifest())
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	delete(object, "classification")
	if _, err := decodeVMRuntimeManifest(marshalVMRuntimeTestJSON(t, object)); err == nil || !strings.Contains(err.Error(), "missing required field") {
		t.Fatalf("decodeVMRuntimeManifest error = %v, want missing required field", err)
	}
}

func TestVMRuntimeManifestRejectsWrongArchitecture(t *testing.T) {
	manifest := validVMRuntimeManifest()
	manifest.Architecture = "arm64"
	if _, err := decodeVMRuntimeManifest(marshalVMRuntimeTestJSON(t, manifest)); err == nil || !strings.Contains(err.Error(), "architecture") {
		t.Fatalf("decodeVMRuntimeManifest error = %v, want architecture", err)
	}

	manifest.Architecture = "x86_64"
	got, err := decodeVMRuntimeManifest(marshalVMRuntimeTestJSON(t, manifest))
	if err != nil {
		t.Fatalf("decode x86_64 manifest: %v", err)
	}
	if got.Architecture != "amd64" {
		t.Fatalf("architecture = %q, want amd64", got.Architecture)
	}
}

func TestVMRuntimeManifestRejectsPathTraversal(t *testing.T) {
	manifest := validVMRuntimeManifest()
	manifest.Components.Firecracker.Path = "../firecracker"
	if _, err := decodeVMRuntimeManifest(marshalVMRuntimeTestJSON(t, manifest)); err == nil || !strings.Contains(err.Error(), "firecracker path") {
		t.Fatalf("decodeVMRuntimeManifest error = %v, want firecracker path", err)
	}
}

func TestVMRuntimeManifestRejectsInvalidComponentDigest(t *testing.T) {
	manifest := validVMRuntimeManifest()
	manifest.Components.Jailer.SHA256 = strings.Repeat("F", 64)
	if _, err := decodeVMRuntimeManifest(marshalVMRuntimeTestJSON(t, manifest)); err == nil || !strings.Contains(err.Error(), "jailer sha256") {
		t.Fatalf("decodeVMRuntimeManifest error = %v, want jailer sha256", err)
	}
}

func TestVMRuntimeManifestRejectsMismatchedVersions(t *testing.T) {
	manifest := validVMRuntimeManifest()
	manifest.Upstream.Tag = "v1.16.0"
	if _, err := decodeVMRuntimeManifest(marshalVMRuntimeTestJSON(t, manifest)); err == nil || !strings.Contains(err.Error(), "upstream") {
		t.Fatalf("decodeVMRuntimeManifest error = %v, want upstream mismatch", err)
	}
}

func TestTrimmedVMRuntimeVersionOutputUsesStableFirstLine(t *testing.T) {
	output := "Firecracker v1.16.1\n\n2026-07-20T22:13:26Z Firecracker exiting successfully. exit_code=0\n"
	if got := trimmedVMRuntimeVersionOutput(output); got != "Firecracker v1.16.1" {
		t.Fatalf("trimmed output = %q", got)
	}
}

func FuzzParseVMRuntimeManifest(f *testing.F) {
	raw, err := json.Marshal(validVMRuntimeManifest())
	if err != nil {
		f.Fatal(err)
	}
	f.Add(raw)
	f.Add([]byte(`{"schema_version":1}`))
	f.Add([]byte(`not-json`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = decodeVMRuntimeManifest(raw)
	})
}

func validVMRuntimeManifest() vmRuntimeManifest {
	fingerprint := "0123456789ABCDEF0123456789ABCDEF01234567"
	return vmRuntimeManifest{
		SchemaVersion: 1,
		RuntimeID:     "firecracker-v1.16.1-yeet-v1",
		Architecture:  "amd64",
		Upstream: vmRuntimeManifestUpstream{
			Repository:    "firecracker-microvm/firecracker",
			Version:       "v1.16.1",
			Tag:           "v1.16.1",
			Commit:        strings.Repeat("a", 40),
			ArchiveURL:    "https://github.com/firecracker-microvm/firecracker/releases/download/v1.16.1/firecracker-v1.16.1-x86_64.tgz",
			ArchiveSHA256: strings.Repeat("b", 64),
			ChecksumURL:   "https://github.com/firecracker-microvm/firecracker/releases/download/v1.16.1/firecracker-v1.16.1-x86_64.tgz.sha256.txt",
			TagSignature: vmRuntimeManifestTagSignature{
				Status:      "signed",
				Fingerprint: &fingerprint,
			},
		},
		Components: vmRuntimeManifestComponents{
			Firecracker: vmRuntimeManifestComponent{Path: "firecracker", SHA256: strings.Repeat("c", 64), VersionOutput: "Firecracker v1.16.1"},
			Jailer:      vmRuntimeManifestComponent{Path: "jailer", SHA256: strings.Repeat("d", 64), VersionOutput: "Jailer v1.16.1"},
		},
		Classification: vmRuntimeManifestClass{ProductionRelease: true, DefaultSeccomp: true},
		Support:        vmRuntimeManifestSupport{State: "supported", PolicyURL: "https://github.com/firecracker-microvm/firecracker/blob/main/docs/RELEASE_POLICY.md"},
		Provenance:     vmRuntimeManifestProvenance{Repository: "yeetrun/yeet-vm-images", Commit: strings.Repeat("e", 40), WorkflowRun: "123456789"},
	}
}

func marshalVMRuntimeTestJSON(t testing.TB, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func addVMRuntimeTestJSONField(t testing.TB, raw []byte, name string, value any) []byte {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	object[name] = value
	return marshalVMRuntimeTestJSON(t, object)
}
