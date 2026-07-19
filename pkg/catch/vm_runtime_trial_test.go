// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVMRuntimeRunningMarkerRoundTripAndExactCleanup(t *testing.T) {
	path, deps := newVMRuntimeControlFileFixture(t, vmRuntimeRunningMarkerFileName)
	artifact := vmRuntimeLaunchTestArtifact("v1.17.0", "/runtime")
	generation := strings.Repeat("d", 64)
	firstID, first, err := writeVMRuntimeRunningMarker(path, "devbox", artifact, generation, strings.Repeat("e", 64), 101, 202, deps)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readTrustedVMRuntimeRunningMarker(path, "devbox", deps.uid, deps.gid)
	if err != nil {
		t.Fatal(err)
	}
	if got != first {
		t.Fatalf("running marker = %#v, want %#v", got, first)
	}

	secondID, second, err := writeVMRuntimeRunningMarker(path, "devbox", artifact, generation, strings.Repeat("f", 64), 303, 404, deps)
	if err != nil {
		t.Fatal(err)
	}
	if firstID == secondID {
		t.Fatal("replacement marker reused inode identity")
	}
	if err := removeVMRuntimeRunningMarker(path, "devbox", firstID, 101, 202, deps); err != nil {
		t.Fatal(err)
	}
	got, err = readTrustedVMRuntimeRunningMarker(path, "devbox", deps.uid, deps.gid)
	if err != nil {
		t.Fatal(err)
	}
	if got != second {
		t.Fatalf("stale cleanup removed replacement marker: %#v", got)
	}
	if err := removeVMRuntimeRunningMarker(path, "devbox", secondID, 303, 404, deps); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("marker after exact cleanup: %v", err)
	}
}

func TestVMRuntimeTrialResultRoundTripAndExactConsumption(t *testing.T) {
	path, deps := newVMRuntimeControlFileFixture(t, vmRuntimeTrialResultFileName)
	configured := vmRuntimeLaunchTestArtifact("v1.16.1", "/configured")
	candidate := vmRuntimeLaunchTestArtifact("v1.17.0", "/candidate")
	result := vmRuntimeTrialResult{
		SchemaVersion: vmRuntimeTrialResultSchemaVersion,
		Service:       "devbox", DescriptorSHA256: strings.Repeat("d", 64), LaunchID: strings.Repeat("e", 64),
		Candidate:  vmRuntimeTrialIdentityForArtifact(candidate),
		Configured: vmRuntimeTrialIdentityForArtifact(configured),
		Outcome:    vmRuntimeTrialHealthy, RunnerPID: 101, ChildPID: 202,
		StartedAt: "2026-07-20T12:00:00Z", CompletedAt: "2026-07-20T12:00:05Z",
	}
	running := result.Candidate
	result.Running = &running
	wantID, err := writeVMRuntimeTrialResult(path, result, deps)
	if err != nil {
		t.Fatal(err)
	}
	got, gotID, err := readTrustedVMRuntimeTrialResult(path, "devbox", deps)
	if err != nil {
		t.Fatal(err)
	}
	if gotID != wantID || !equalVMRuntimeTrialResults(got, result) {
		t.Fatalf("trial result = %#v id=%#v, want %#v id=%#v", got, gotID, result, wantID)
	}
	if err := removeVMRuntimeTrialResult(path, wantID, deps); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("trial result after consumption: %v", err)
	}
}

func TestDecodeVMRuntimeTrialResultRejectsUnknownAndInconsistentFields(t *testing.T) {
	configured := vmRuntimeLaunchTestArtifact("v1.16.1", "/configured")
	candidate := vmRuntimeLaunchTestArtifact("v1.17.0", "/candidate")
	result := vmRuntimeTrialResult{
		SchemaVersion: vmRuntimeTrialResultSchemaVersion,
		Service:       "devbox", DescriptorSHA256: strings.Repeat("d", 64), LaunchID: strings.Repeat("e", 64),
		Candidate: vmRuntimeTrialIdentityForArtifact(candidate), Configured: vmRuntimeTrialIdentityForArtifact(configured),
		Outcome: vmRuntimeTrialHealthy, RunnerPID: 101, ChildPID: 202,
		StartedAt: "2026-07-20T12:00:00Z", CompletedAt: "2026-07-20T12:00:05Z",
	}
	running := result.Candidate
	result.Running = &running
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw[:len(raw)-1], []byte(`,"guestReady":true}`)...)
	if _, err := decodeVMRuntimeTrialResult(raw, "devbox"); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown-field error = %v", err)
	}

	result.Error = "guest-controlled readiness must not influence the host trial"
	raw, err = json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeVMRuntimeTrialResult(raw, "devbox"); err == nil || !strings.Contains(err.Error(), "inconsistent") {
		t.Fatalf("healthy-with-error error = %v", err)
	}
}

func TestDecodeVMRuntimeTrialResultAcceptsTerminalFailureWithoutRunningProcess(t *testing.T) {
	configured := vmRuntimeLaunchTestArtifact("v1.16.1", "/configured")
	candidate := vmRuntimeLaunchTestArtifact("v1.17.0", "/candidate")
	result := vmRuntimeTrialResult{
		SchemaVersion: vmRuntimeTrialResultSchemaVersion,
		Service:       "devbox", DescriptorSHA256: strings.Repeat("d", 64), LaunchID: strings.Repeat("e", 64),
		Candidate: vmRuntimeTrialIdentityForArtifact(candidate), Configured: vmRuntimeTrialIdentityForArtifact(configured),
		Outcome: vmRuntimeTrialFailedNoFallback, RunnerPID: 101, ChildPID: 202,
		StartedAt: "2026-07-20T12:00:00Z", CompletedAt: "2026-07-20T12:00:01Z", Error: "both host launch attempts failed",
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeVMRuntimeTrialResult(raw, "devbox"); err != nil {
		t.Fatal(err)
	}
}

func newVMRuntimeControlFileFixture(t *testing.T, name string) (string, vmRuntimeControlFileDeps) {
	t.Helper()
	dir := t.TempDir()
	deps := defaultVMRuntimeControlFileDeps()
	deps.uid = uint32(os.Geteuid())
	deps.gid = uint32(os.Getegid())
	deps.now = func() time.Time { return time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC) }
	return filepath.Join(dir, name), deps
}

func equalVMRuntimeTrialResults(left, right vmRuntimeTrialResult) bool {
	leftRaw, leftErr := json.Marshal(left)
	rightRaw, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftRaw) == string(rightRaw)
}
