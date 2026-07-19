// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestSelectVMRuntimeLaunchUsesStagedCandidateAndConfiguredFallback(t *testing.T) {
	configured := vmRuntimeLaunchTestArtifact("v1.16.1", "/configured")
	staged := vmRuntimeLaunchTestArtifact("v1.17.0", "/staged")
	descriptor := vmRuntimeDescriptor{
		SchemaVersion: vmRuntimeDescriptorSchemaVersion, Service: "devbox",
		Configured: configured, Staged: &staged, Trial: true,
	}
	selection, err := selectVMRuntimeLaunch("/srv/devbox/data/vmm-runtime.json", "devbox", 0, 0, vmRuntimeLaunchDeps{
		readDescriptor: func(path, service string, uid, gid uint32) (vmRuntimeDescriptorSnapshot, error) {
			if path != "/srv/devbox/data/vmm-runtime.json" || service != "devbox" || uid != 0 || gid != 0 {
				t.Fatalf("descriptor read = %q %q %d:%d", path, service, uid, gid)
			}
			return vmRuntimeDescriptorSnapshot{Descriptor: descriptor, SHA256: strings.Repeat("d", 64)}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !selection.Trial || selection.Selected != staged || selection.Fallback == nil || *selection.Fallback != configured || !isLowerSHA256(selection.DescriptorSHA256) {
		t.Fatalf("launch selection = %#v", selection)
	}
}

func TestSelectVMRuntimeLaunchUsesConfiguredWithoutTrial(t *testing.T) {
	configured := vmRuntimeLaunchTestArtifact("v1.16.1", "/configured")
	selection, err := selectVMRuntimeLaunch("/descriptor", "devbox", 0, 0, vmRuntimeLaunchDeps{
		readDescriptor: func(string, string, uint32, uint32) (vmRuntimeDescriptorSnapshot, error) {
			return vmRuntimeDescriptorSnapshot{Descriptor: vmRuntimeDescriptor{SchemaVersion: 1, Service: "devbox", Configured: configured}, SHA256: strings.Repeat("d", 64)}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Trial || selection.Selected != configured || selection.Fallback != nil {
		t.Fatalf("normal launch selection = %#v", selection)
	}
}

func TestVerifyVMRuntimeLaunchRejectsDigestReplacementAfterDescriptorRead(t *testing.T) {
	artifact, deps := newVMRuntimeLaunchVerificationFixture(t)
	if err := os.WriteFile(artifact.Firecracker, []byte("replaced"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := verifyVMRuntimeLaunchArtifact(context.Background(), artifact, deps)
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("launch verification error = %v", err)
	}
}

func TestVerifyVMRuntimeLaunchRejectsPairMismatch(t *testing.T) {
	artifact, deps := newVMRuntimeLaunchVerificationFixture(t)
	mismatch := errors.New("version mismatch")
	deps.runtimePair = func(context.Context, string, string) (string, error) { return "", mismatch }
	_, err := verifyVMRuntimeLaunchArtifact(context.Background(), artifact, deps)
	if !errors.Is(err, mismatch) {
		t.Fatalf("launch verification error = %v, want %v", err, mismatch)
	}
}

func TestVerifyVMRuntimeLaunchRechecksEvidenceAfterVersionProbe(t *testing.T) {
	artifact, deps := newVMRuntimeLaunchVerificationFixture(t)
	calls := 0
	deps.architecture = func(string) (string, error) {
		calls++
		if calls == 2 {
			if err := os.WriteFile(artifact.Firecracker, []byte("changed-during-verification"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		return "amd64", nil
	}
	_, err := verifyVMRuntimeLaunchArtifact(context.Background(), artifact, deps)
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("launch verification error = %v, want replacement refusal", err)
	}
}

func TestVerifyVMRuntimeLaunchAcceptsExactTrustedPair(t *testing.T) {
	artifact, deps := newVMRuntimeLaunchVerificationFixture(t)
	if version, err := verifyVMRuntimeLaunchArtifact(context.Background(), artifact, deps); err != nil {
		t.Fatal(err)
	} else if version != "1.17.0" {
		t.Fatalf("verified version = %q", version)
	}
}

func TestProbeMatchingVMRuntimePair(t *testing.T) {
	oldValidate, oldProbe := vmJailValidateTrustedInput, vmJailProbeVersion
	t.Cleanup(func() { vmJailValidateTrustedInput, vmJailProbeVersion = oldValidate, oldProbe })
	vmJailValidateTrustedInput = func(path string) error {
		if path == "invalid" {
			return errors.New("invalid path")
		}
		return nil
	}
	vmJailProbeVersion = func(_ context.Context, path string) (string, error) {
		switch path {
		case "firecracker", "jailer":
			return "Firecracker v1.17.0", nil
		case "probe-error":
			return "", errors.New("probe failed")
		default:
			return "unrecognized", nil
		}
	}
	if got, err := probeMatchingVMRuntimePair(context.Background(), "firecracker", "jailer"); err != nil || got != "1.17.0" {
		t.Fatalf("matching pair = %q, %v", got, err)
	}
	for _, paths := range [][2]string{{"invalid", "jailer"}, {"probe-error", "jailer"}, {"firecracker", "probe-error"}, {"unrecognized", "unrecognized"}} {
		if _, err := probeMatchingVMRuntimePair(context.Background(), paths[0], paths[1]); err == nil {
			t.Fatalf("invalid pair %q accepted", paths)
		}
	}
}

func newVMRuntimeLaunchVerificationFixture(t *testing.T) (db.VMRuntimeArtifactConfig, vmRuntimeLaunchDeps) {
	t.Helper()
	root, err := os.MkdirTemp(".", ".vm-runtime-launch-test-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	firecracker := filepath.Join(root, "firecracker")
	jailer := filepath.Join(root, "jailer")
	if err := os.WriteFile(firecracker, []byte("firecracker"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jailer, []byte("jailer"), 0o755); err != nil {
		t.Fatal(err)
	}
	artifact := db.VMRuntimeArtifactConfig{
		ID: "firecracker-v1.17.0-yeet-v1", Source: "official",
		ManifestSHA256:    strings.Repeat("a", 64),
		FirecrackerSHA256: vmRuntimeSHA256Bytes([]byte("firecracker")),
		JailerSHA256:      vmRuntimeSHA256Bytes([]byte("jailer")),
		Firecracker:       firecracker, Jailer: jailer,
	}
	deps := defaultVMRuntimeLaunchDeps()
	deps.evidence.trustedUID = uint32(os.Geteuid())
	deps.architecture = func(string) (string, error) { return "amd64", nil }
	deps.runtimePair = func(context.Context, string, string) (string, error) { return "1.17.0", nil }
	return artifact, deps
}

func vmRuntimeLaunchTestArtifact(version, root string) db.VMRuntimeArtifactConfig {
	return db.VMRuntimeArtifactConfig{
		ID: "firecracker-" + version + "-yeet-v1", Source: "official",
		ManifestSHA256: strings.Repeat("a", 64), FirecrackerSHA256: strings.Repeat("b", 64), JailerSHA256: strings.Repeat("c", 64),
		Firecracker: filepath.Join(root, "firecracker"), Jailer: filepath.Join(root, "jailer"),
	}
}
