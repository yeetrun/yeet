// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/serviceid"
	"golang.org/x/sys/unix"
)

const (
	vmRuntimeTrialResultSchemaVersion = 1
	vmRuntimeTrialResultMaxSize       = 64 << 10
)

type vmRuntimeTrialOutcome string

const (
	vmRuntimeTrialHealthy          vmRuntimeTrialOutcome = "healthy"
	vmRuntimeTrialFailedRolledBack vmRuntimeTrialOutcome = "failed-rolled-back"
	vmRuntimeTrialFailedNoFallback vmRuntimeTrialOutcome = "failed-no-fallback"
)

type vmRuntimeTrialArtifactIdentity struct {
	ID                string `json:"id"`
	ManifestSHA256    string `json:"manifestSHA256,omitempty"`
	FirecrackerSHA256 string `json:"firecrackerSHA256"`
	JailerSHA256      string `json:"jailerSHA256"`
}

type vmRuntimeTrialResult struct {
	SchemaVersion    int                             `json:"schemaVersion"`
	Service          string                          `json:"service"`
	DescriptorSHA256 string                          `json:"descriptorSHA256"`
	LaunchID         string                          `json:"launchId"`
	Candidate        vmRuntimeTrialArtifactIdentity  `json:"candidate"`
	Configured       vmRuntimeTrialArtifactIdentity  `json:"configured"`
	Running          *vmRuntimeTrialArtifactIdentity `json:"running,omitempty"`
	Outcome          vmRuntimeTrialOutcome           `json:"outcome"`
	RunnerPID        int                             `json:"runnerPid"`
	ChildPID         int                             `json:"childPid"`
	StartedAt        string                          `json:"startedAt"`
	CompletedAt      string                          `json:"completedAt"`
	Error            string                          `json:"error,omitempty"`
}

type vmRuntimeControlFileDeps struct {
	uid     uint32
	gid     uint32
	now     func() time.Time
	rename  func(int, string, int, string) error
	unlink  func(int, string, int) error
	syncDir func(*os.File) error
}

func defaultVMRuntimeControlFileDeps() vmRuntimeControlFileDeps {
	return vmRuntimeControlFileDeps{
		uid: 0,
		gid: 0,
		now: time.Now,
		rename: func(oldDir int, oldName string, newDir int, newName string) error {
			return unix.Renameat(oldDir, oldName, newDir, newName)
		},
		unlink:  unix.Unlinkat,
		syncDir: func(dir *os.File) error { return dir.Sync() },
	}
}

func completeVMRuntimeControlFileDeps(deps vmRuntimeControlFileDeps) vmRuntimeControlFileDeps {
	defaults := defaultVMRuntimeControlFileDeps()
	if deps.now == nil {
		deps.now = defaults.now
	}
	if deps.rename == nil {
		deps.rename = defaults.rename
	}
	if deps.unlink == nil {
		deps.unlink = defaults.unlink
	}
	if deps.syncDir == nil {
		deps.syncDir = defaults.syncDir
	}
	return deps
}

func vmRuntimeTrialIdentityForArtifact(artifact db.VMRuntimeArtifactConfig) vmRuntimeTrialArtifactIdentity {
	return vmRuntimeTrialArtifactIdentity{
		ID: artifact.ID, ManifestSHA256: artifact.ManifestSHA256,
		FirecrackerSHA256: artifact.FirecrackerSHA256, JailerSHA256: artifact.JailerSHA256,
	}
}

func (identity vmRuntimeTrialArtifactIdentity) matches(artifact db.VMRuntimeArtifactConfig) bool {
	return identity == vmRuntimeTrialIdentityForArtifact(artifact)
}

func writeVMRuntimeRunningMarker(
	path, service string,
	artifact db.VMRuntimeArtifactConfig,
	descriptorSHA256, launchID string,
	runnerPID, childPID int,
	deps vmRuntimeControlFileDeps,
) (vmJailerFileIdentity, vmRuntimeRunningMarker, error) {
	deps = completeVMRuntimeControlFileDeps(deps)
	marker := vmRuntimeRunningMarker{
		SchemaVersion: vmRuntimeRunningMarkerSchemaVersion,
		Service:       service, DescriptorSHA256: descriptorSHA256, LaunchID: launchID,
		RuntimeID: artifact.ID, ManifestSHA256: artifact.ManifestSHA256,
		FirecrackerSHA256: artifact.FirecrackerSHA256, JailerSHA256: artifact.JailerSHA256,
		RunnerPID: runnerPID, ChildPID: childPID, StartedAt: deps.now().UTC().Format(time.RFC3339Nano),
	}
	raw, err := json.Marshal(marker)
	if err != nil {
		return vmJailerFileIdentity{}, vmRuntimeRunningMarker{}, err
	}
	if _, err := decodeVMRuntimeRunningMarker(raw, service); err != nil {
		return vmJailerFileIdentity{}, vmRuntimeRunningMarker{}, fmt.Errorf("validate VM runtime marker before write: %w", err)
	}
	id, err := writeTrustedVMRuntimeControlFile(path, append(raw, '\n'), deps)
	if err != nil {
		return vmJailerFileIdentity{}, vmRuntimeRunningMarker{}, fmt.Errorf("write VM runtime marker: %w", err)
	}
	return id, marker, nil
}

func removeVMRuntimeRunningMarker(
	path, service string,
	wantID vmJailerFileIdentity,
	runnerPID, childPID int,
	deps vmRuntimeControlFileDeps,
) error {
	deps = completeVMRuntimeControlFileDeps(deps)
	raw, id, _, dir, err := readTrustedVMRuntimeControlFile(path, deps, vmRuntimeRunningMarkerMaxSize)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	marker, err := decodeVMRuntimeRunningMarker(raw, service)
	if err != nil {
		return err
	}
	if id != wantID || marker.RunnerPID != runnerPID || marker.ChildPID != childPID {
		return nil
	}
	if err := unlinkVMJailerNameIfIdentityWith(dir, filepath.Base(path), wantID, deps.unlink); err != nil {
		return fmt.Errorf("remove exact VM runtime marker: %w", err)
	}
	return deps.syncDir(dir)
}

func writeVMRuntimeTrialResult(path string, result vmRuntimeTrialResult, deps vmRuntimeControlFileDeps) (vmJailerFileIdentity, error) {
	deps = completeVMRuntimeControlFileDeps(deps)
	raw, err := json.Marshal(result)
	if err != nil {
		return vmJailerFileIdentity{}, err
	}
	if _, err := decodeVMRuntimeTrialResult(raw, result.Service); err != nil {
		return vmJailerFileIdentity{}, fmt.Errorf("validate VM runtime trial result before write: %w", err)
	}
	id, err := writeTrustedVMRuntimeControlFile(path, append(raw, '\n'), deps)
	if err != nil {
		return vmJailerFileIdentity{}, fmt.Errorf("write VM runtime trial result: %w", err)
	}
	return id, nil
}

func readTrustedVMRuntimeTrialResult(path, service string, deps vmRuntimeControlFileDeps) (vmRuntimeTrialResult, vmJailerFileIdentity, error) {
	deps = completeVMRuntimeControlFileDeps(deps)
	raw, id, _, dir, err := readTrustedVMRuntimeControlFile(path, deps, vmRuntimeTrialResultMaxSize)
	if err != nil {
		return vmRuntimeTrialResult{}, vmJailerFileIdentity{}, err
	}
	if err := dir.Close(); err != nil {
		return vmRuntimeTrialResult{}, vmJailerFileIdentity{}, err
	}
	result, err := decodeVMRuntimeTrialResult(raw, service)
	return result, id, err
}

func removeVMRuntimeTrialResult(path string, wantID vmJailerFileIdentity, deps vmRuntimeControlFileDeps) error {
	deps = completeVMRuntimeControlFileDeps(deps)
	dir, err := openValidatedVMRuntimeDescriptorDir(filepath.Dir(path), deps.uid)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	if err := unlinkVMJailerNameIfIdentityWith(dir, filepath.Base(path), wantID, deps.unlink); err != nil {
		return fmt.Errorf("remove exact VM runtime trial result: %w", err)
	}
	return deps.syncDir(dir)
}

func writeTrustedVMRuntimeControlFile(path string, raw []byte, deps vmRuntimeControlFileDeps) (retID vmJailerFileIdentity, retErr error) {
	if len(raw) > vmRuntimeTrialResultMaxSize {
		return vmJailerFileIdentity{}, fmt.Errorf("VM runtime control file exceeds %d bytes", vmRuntimeTrialResultMaxSize)
	}
	dir, err := openValidatedVMRuntimeDescriptorDir(filepath.Dir(path), deps.uid)
	if err != nil {
		return vmJailerFileIdentity{}, err
	}
	defer func() { retErr = errors.Join(retErr, dir.Close()) }()
	temp, tempName, tempID, err := createStagedVMUnitAt(dir, filepath.Base(path))
	if err != nil {
		return vmJailerFileIdentity{}, err
	}
	tempOpen := true
	tempPublished := false
	defer func() {
		retErr = cleanupStagedVMRuntimeControlFile(dir, temp, tempName, tempID, tempOpen, tempPublished, deps, retErr)
	}()
	id, stat, closed, err := stageVMRuntimeControlFile(temp, raw, tempID, deps)
	tempOpen = !closed
	if err != nil {
		return vmJailerFileIdentity{}, err
	}
	if err := validateVMRuntimeDescriptorName(dir, tempName, id, stat, deps.uid, deps.gid); err != nil {
		return vmJailerFileIdentity{}, err
	}
	if err := deps.rename(int(dir.Fd()), tempName, int(dir.Fd()), filepath.Base(path)); err != nil {
		return vmJailerFileIdentity{}, err
	}
	tempPublished = true
	if err := deps.syncDir(dir); err != nil {
		return vmJailerFileIdentity{}, err
	}
	_, publishedStat, err := vmJailerNameIdentityAt(dir, filepath.Base(path))
	if err != nil {
		return vmJailerFileIdentity{}, err
	}
	if err := validateVMRuntimeDescriptorName(dir, filepath.Base(path), id, publishedStat, deps.uid, deps.gid); err != nil {
		return vmJailerFileIdentity{}, err
	}
	return id, nil
}

func cleanupStagedVMRuntimeControlFile(dir, temp *os.File, tempName string, tempID vmJailerFileIdentity, tempOpen, published bool, deps vmRuntimeControlFileDeps, retErr error) error {
	if tempOpen {
		retErr = errors.Join(retErr, temp.Close())
	}
	if !published {
		retErr = errors.Join(retErr, unlinkVMJailerNameIfIdentityWith(dir, tempName, tempID, deps.unlink))
	}
	return retErr
}

func stageVMRuntimeControlFile(temp *os.File, raw []byte, tempID vmJailerFileIdentity, deps vmRuntimeControlFileDeps) (vmJailerFileIdentity, unix.Stat_t, bool, error) {
	if _, err := temp.Write(raw); err != nil {
		return vmJailerFileIdentity{}, unix.Stat_t{}, false, err
	}
	if err := temp.Chown(int(deps.uid), int(deps.gid)); err != nil {
		return vmJailerFileIdentity{}, unix.Stat_t{}, false, err
	}
	if err := temp.Chmod(0o600); err != nil {
		return vmJailerFileIdentity{}, unix.Stat_t{}, false, err
	}
	if err := temp.Sync(); err != nil {
		return vmJailerFileIdentity{}, unix.Stat_t{}, false, err
	}
	id, stat, err := validateOpenVMRuntimeMarker(temp, deps.uid, deps.gid)
	if err != nil {
		return vmJailerFileIdentity{}, unix.Stat_t{}, false, err
	}
	if id != tempID {
		return vmJailerFileIdentity{}, unix.Stat_t{}, false, fmt.Errorf("staged VM runtime control file identity changed")
	}
	if err := temp.Close(); err != nil {
		return vmJailerFileIdentity{}, unix.Stat_t{}, false, err
	}
	return id, stat, true, nil
}

func readTrustedVMRuntimeControlFile(
	path string,
	deps vmRuntimeControlFileDeps,
	maxSize int64,
) ([]byte, vmJailerFileIdentity, unix.Stat_t, *os.File, error) {
	dir, err := openValidatedVMRuntimeDescriptorDir(filepath.Dir(path), deps.uid)
	if err != nil {
		return nil, vmJailerFileIdentity{}, unix.Stat_t{}, nil, err
	}
	name := filepath.Base(path)
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = dir.Close()
		if errors.Is(err, unix.ENOENT) {
			return nil, vmJailerFileIdentity{}, unix.Stat_t{}, nil, os.ErrNotExist
		}
		return nil, vmJailerFileIdentity{}, unix.Stat_t{}, nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		_ = dir.Close()
		return nil, vmJailerFileIdentity{}, unix.Stat_t{}, nil, fmt.Errorf("bind VM runtime control file descriptor")
	}
	id, stat, err := validateOpenVMRuntimeMarker(file, deps.uid, deps.gid)
	if err != nil {
		_ = file.Close()
		_ = dir.Close()
		return nil, vmJailerFileIdentity{}, unix.Stat_t{}, nil, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, maxSize+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		_ = dir.Close()
		return nil, vmJailerFileIdentity{}, unix.Stat_t{}, nil, errors.Join(readErr, closeErr)
	}
	if int64(len(raw)) > maxSize {
		_ = dir.Close()
		return nil, vmJailerFileIdentity{}, unix.Stat_t{}, nil, fmt.Errorf("VM runtime control file exceeds %d bytes", maxSize)
	}
	if err := validateVMRuntimeDescriptorName(dir, name, id, stat, deps.uid, deps.gid); err != nil {
		_ = dir.Close()
		return nil, vmJailerFileIdentity{}, unix.Stat_t{}, nil, err
	}
	return raw, id, stat, dir, nil
}

func decodeVMRuntimeTrialResult(raw []byte, expectedService string) (vmRuntimeTrialResult, error) {
	if err := validateVMRuntimeTrialResultFields(raw); err != nil {
		return vmRuntimeTrialResult{}, err
	}
	result, err := decodeVMRuntimeTrialResultStrict(raw)
	if err != nil {
		return vmRuntimeTrialResult{}, err
	}
	if err := validateVMRuntimeTrialResult(result, expectedService); err != nil {
		return vmRuntimeTrialResult{}, err
	}
	return result, nil
}

func validateVMRuntimeTrialResultFields(raw []byte) error {
	required := []string{
		"schemaVersion", "service", "descriptorSHA256", "launchId", "candidate", "configured",
		"outcome", "runnerPid", "childPid", "startedAt", "completedAt",
	}
	allowed := append(append([]string(nil), required...), "running", "error")
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("decode VM runtime trial result: %w", err)
	}
	for _, name := range required {
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("VM runtime trial result missing required field %q", name)
		}
	}
	for name := range fields {
		if !slices.Contains(allowed, name) {
			return fmt.Errorf("VM runtime trial result contains unknown field %q", name)
		}
	}
	return nil
}

func decodeVMRuntimeTrialResultStrict(raw []byte) (vmRuntimeTrialResult, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var result vmRuntimeTrialResult
	if err := decoder.Decode(&result); err != nil {
		return vmRuntimeTrialResult{}, fmt.Errorf("decode VM runtime trial result: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return vmRuntimeTrialResult{}, err
	}
	return result, nil
}

func validateVMRuntimeTrialResult(result vmRuntimeTrialResult, expectedService string) error {
	if result.SchemaVersion != vmRuntimeTrialResultSchemaVersion {
		return fmt.Errorf("unsupported VM runtime trial result schemaVersion %d", result.SchemaVersion)
	}
	if err := serviceid.Validate(result.Service); err != nil || result.Service != expectedService {
		return fmt.Errorf("VM runtime trial result service %q does not match %q", result.Service, expectedService)
	}
	if !isLowerSHA256(result.DescriptorSHA256) || !isLowerSHA256(result.LaunchID) {
		return fmt.Errorf("VM runtime trial generation or launch ID is invalid")
	}
	if err := validateVMRuntimeTrialResultIdentities(result); err != nil {
		return err
	}
	if err := validateVMRuntimeTrialResultProcessAndTime(result); err != nil {
		return err
	}
	return validateVMRuntimeTrialResultOutcome(result)
}

func validateVMRuntimeTrialResultIdentities(result vmRuntimeTrialResult) error {
	if err := validateVMRuntimeTrialIdentity(result.Candidate, "candidate"); err != nil {
		return err
	}
	if err := validateVMRuntimeTrialIdentity(result.Configured, "configured"); err != nil {
		return err
	}
	if result.Running != nil {
		if err := validateVMRuntimeTrialIdentity(*result.Running, "running"); err != nil {
			return err
		}
	}
	return nil
}

func validateVMRuntimeTrialResultProcessAndTime(result vmRuntimeTrialResult) error {
	if result.RunnerPID <= 0 || result.ChildPID < 0 {
		return fmt.Errorf("VM runtime trial result process IDs are invalid")
	}
	started, err := time.Parse(time.RFC3339, result.StartedAt)
	if err != nil {
		return fmt.Errorf("VM runtime trial startedAt is invalid: %w", err)
	}
	completed, err := time.Parse(time.RFC3339, result.CompletedAt)
	if err != nil || completed.Before(started) {
		return fmt.Errorf("VM runtime trial completedAt is invalid")
	}
	return nil
}

func validateVMRuntimeTrialResultOutcome(result vmRuntimeTrialResult) error {
	switch result.Outcome {
	case vmRuntimeTrialHealthy:
		return validateHealthyVMRuntimeTrialResult(result)
	case vmRuntimeTrialFailedRolledBack:
		return validateRolledBackVMRuntimeTrialResult(result)
	case vmRuntimeTrialFailedNoFallback:
		return validateNoFallbackVMRuntimeTrialResult(result)
	default:
		return fmt.Errorf("unsupported VM runtime trial outcome %q", result.Outcome)
	}
}

func validateHealthyVMRuntimeTrialResult(result vmRuntimeTrialResult) error {
	if result.ChildPID <= 0 || result.Running == nil || *result.Running != result.Candidate || strings.TrimSpace(result.Error) != "" {
		return fmt.Errorf("healthy VM runtime trial result is inconsistent")
	}
	return nil
}

func validateRolledBackVMRuntimeTrialResult(result vmRuntimeTrialResult) error {
	if result.ChildPID <= 0 || result.Running == nil || *result.Running != result.Configured || strings.TrimSpace(result.Error) == "" {
		return fmt.Errorf("rolled-back VM runtime trial result is inconsistent")
	}
	return nil
}

func validateNoFallbackVMRuntimeTrialResult(result vmRuntimeTrialResult) error {
	if result.Running != nil || strings.TrimSpace(result.Error) == "" {
		return fmt.Errorf("failed VM runtime trial result is inconsistent")
	}
	return nil
}

func validateVMRuntimeTrialIdentity(identity vmRuntimeTrialArtifactIdentity, role string) error {
	if strings.TrimSpace(identity.ID) == "" || identity.ID != strings.TrimSpace(identity.ID) {
		return fmt.Errorf("VM runtime trial %s runtime ID is invalid", role)
	}
	if identity.ManifestSHA256 != "" && !isLowerSHA256(identity.ManifestSHA256) {
		return fmt.Errorf("VM runtime trial %s manifest SHA-256 is invalid", role)
	}
	if !isLowerSHA256(identity.FirecrackerSHA256) || !isLowerSHA256(identity.JailerSHA256) {
		return fmt.Errorf("VM runtime trial %s component digest is invalid", role)
	}
	return nil
}
