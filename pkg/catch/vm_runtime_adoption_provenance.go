// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	vmLegacyCompositionSchema        = "yeet.vm.legacy-composition"
	vmLegacyCompositionSchemaVersion = 1
	vmLegacyProvenanceDirName        = "vm-component-provenance"
	vmLegacyProvenanceDigestDirName  = "sha256"
	vmLegacyProvenanceMaxRecordBytes = 64 << 10
	vmRuntimeAdoptionMaxFileBytes    = 1 << 40
)

// vmLegacyCompositionRecord is deliberately a fixed tree of structs. It is a
// content identity for immutable components, so it contains no service name,
// active disk, or installation path.
type vmLegacyCompositionRecord struct {
	Schema        string                    `json:"schema"`
	SchemaVersion int                       `json:"schemaVersion"`
	Architecture  string                    `json:"architecture"`
	GuestBase     vmLegacyGuestBaseIdentity `json:"guestBase"`
	Kernel        vmLegacyKernelIdentity    `json:"kernel"`
	Runtime       vmLegacyRuntimeIdentity   `json:"runtime"`
}

type vmLegacyGuestBaseIdentity struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	RootFSSHA256 string `json:"rootfsSHA256"`
}

type vmLegacyKernelIdentity struct {
	Version      string `json:"version"`
	KernelSHA256 string `json:"kernelSHA256"`
	ConfigSHA256 string `json:"configSHA256"`
	InitrdSHA256 string `json:"initrdSHA256"`
}

type vmLegacyRuntimeIdentity struct {
	Version           string `json:"version"`
	FirecrackerSHA256 string `json:"firecrackerSHA256"`
	JailerSHA256      string `json:"jailerSHA256"`
}

type vmLegacyCompositionInput struct {
	Architecture       string
	GuestName          string
	GuestVersion       string
	KernelVersion      string
	FirecrackerVersion string
	RootFS             vmRuntimeAdoptionFileEvidence
	Kernel             vmRuntimeAdoptionFileEvidence
	KernelConfig       vmRuntimeAdoptionFileEvidence
	Initrd             vmRuntimeAdoptionFileEvidence
	Firecracker        vmRuntimeAdoptionFileEvidence
	Jailer             vmRuntimeAdoptionFileEvidence
}

// vmRuntimeAdoptionFileEvidence belongs to a service precondition, not the
// composition identity. Its absolute path and inode metadata intentionally
// detect relocation or replacement between inventory and publication.
type vmRuntimeAdoptionFileEvidence struct {
	Path    string `json:"path"`
	Exists  bool   `json:"exists"`
	Device  uint64 `json:"device"`
	Inode   uint64 `json:"inode"`
	Size    int64  `json:"size"`
	Mode    uint32 `json:"mode"`
	UID     uint32 `json:"uid"`
	GID     uint32 `json:"gid"`
	MTimeNS int64  `json:"mtimeNS"`
	SHA256  string `json:"sha256"`
}

// vmRuntimeAdoptionActiveDiskEvidence records only filesystem identity and
// declared capacity. A mutable disk must never gain a content digest.
type vmRuntimeAdoptionActiveDiskEvidence struct {
	Path         string `json:"path"`
	Backend      string `json:"backend"`
	Bytes        int64  `json:"bytes"`
	Dataset      string `json:"dataset"`
	ResolvedPath string `json:"resolvedPath"`
	LinkDevice   uint64 `json:"linkDevice"`
	LinkInode    uint64 `json:"linkInode"`
	LinkMode     uint32 `json:"linkMode"`
	LinkUID      uint32 `json:"linkUID"`
	LinkGID      uint32 `json:"linkGID"`
	Device       uint64 `json:"device"`
	Inode        uint64 `json:"inode"`
	RDevice      uint64 `json:"rdevice"`
	Size         int64  `json:"size"`
	Mode         uint32 `json:"mode"`
	UID          uint32 `json:"uid"`
	GID          uint32 `json:"gid"`
	MTimeNS      int64  `json:"mtimeNS"`
}

type vmRuntimeAdoptionPreconditionEvidence struct {
	Service     string                              `json:"service"`
	ServiceRoot string                              `json:"serviceRoot"`
	Files       []vmRuntimeAdoptionFileEvidence     `json:"files"`
	ActiveDisk  vmRuntimeAdoptionActiveDiskEvidence `json:"activeDisk"`
}

type vmRuntimeAdoptionEvidenceDeps struct {
	trustedUID   uint32
	maxFileBytes int64
	copy         func(io.Writer, io.Reader) (int64, error)
	resolveZVOL  func(string) (vmRuntimeAdoptionZVOLResolution, error)
}

type vmRuntimeAdoptionZVOLResolution struct {
	Dataset      string
	ResolvedPath string
	LinkDevice   uint64
	LinkInode    uint64
	LinkMode     uint32
	LinkUID      uint32
	LinkGID      uint32
	Metadata     vmRuntimeAdoptionFileMetadata
}

type vmLegacyCompositionStoreDeps struct {
	trustedUID        uint32
	random            io.Reader
	renameNoReplaceAt func(int, string, int, string) error
	unlinkAt          func(int, string, int) error
	syncFile          func(*os.File) error
	syncDir           func(*os.File) error
}

type vmLegacyCompositionPublication struct {
	Path   string
	SHA256 string
	Bytes  []byte
}

// vmLegacyCompositionPostPublicationError means the canonical digest name
// became visible before the operation failed. Callers must not treat it as an
// ordinary pre-publication failure; retrying the same record verifies and
// resyncs the retained name.
type vmLegacyCompositionPostPublicationError struct {
	cause       error
	publication vmLegacyCompositionPublication
}

// vmLegacyCompositionPublicationUncertainError means publication did not
// resolve to either a verified canonical record or a verified intact staging
// record. Recovery must inspect the named paths instead of retrying as though
// no filesystem change occurred.
type vmLegacyCompositionPublicationUncertainError struct {
	cause       error
	publication vmLegacyCompositionPublication
}

func (err *vmLegacyCompositionPublicationUncertainError) Error() string {
	return fmt.Sprintf("%v; legacy VM composition publication outcome is uncertain at %s", err.cause, err.publication.Path)
}

func (err *vmLegacyCompositionPublicationUncertainError) Unwrap() error {
	return err.cause
}

func (err *vmLegacyCompositionPublicationUncertainError) Publication() vmLegacyCompositionPublication {
	publication := err.publication
	publication.Bytes = append([]byte(nil), publication.Bytes...)
	return publication
}

func (err *vmLegacyCompositionPostPublicationError) Error() string {
	return fmt.Sprintf("%v; legacy VM composition publication retained at %s", err.cause, err.publication.Path)
}

func (err *vmLegacyCompositionPostPublicationError) Unwrap() error {
	return err.cause
}

func (err *vmLegacyCompositionPostPublicationError) Publication() vmLegacyCompositionPublication {
	publication := err.publication
	publication.Bytes = append([]byte(nil), publication.Bytes...)
	return publication
}

func newVMLegacyCompositionRecord(input vmLegacyCompositionInput) (vmLegacyCompositionRecord, error) {
	segments, err := vmLegacyCompositionSegmentsForInput(input)
	if err != nil {
		return vmLegacyCompositionRecord{}, err
	}
	digests, err := vmLegacyCompositionDigestsForInput(input)
	if err != nil {
		return vmLegacyCompositionRecord{}, err
	}

	return vmLegacyCompositionRecord{
		Schema:        vmLegacyCompositionSchema,
		SchemaVersion: vmLegacyCompositionSchemaVersion,
		Architecture:  segments.architecture,
		GuestBase: vmLegacyGuestBaseIdentity{
			Name: segments.guestName, Version: segments.guestVersion, RootFSSHA256: digests.rootFS,
		},
		Kernel: vmLegacyKernelIdentity{
			Version: segments.kernelVersion, KernelSHA256: digests.kernel, ConfigSHA256: digests.config, InitrdSHA256: digests.initrd,
		},
		Runtime: vmLegacyRuntimeIdentity{
			Version: segments.runtimeVersion, FirecrackerSHA256: digests.firecracker, JailerSHA256: digests.jailer,
		},
	}, nil
}

type vmLegacyCompositionSegments struct {
	architecture   string
	guestName      string
	guestVersion   string
	kernelVersion  string
	runtimeVersion string
}

func vmLegacyCompositionSegmentsForInput(input vmLegacyCompositionInput) (vmLegacyCompositionSegments, error) {
	values := []struct {
		label string
		value string
		set   func(*vmLegacyCompositionSegments, string)
	}{
		{label: "guest name", value: input.GuestName, set: func(result *vmLegacyCompositionSegments, value string) { result.guestName = value }},
		{label: "guest version", value: input.GuestVersion, set: func(result *vmLegacyCompositionSegments, value string) { result.guestVersion = value }},
		{label: "kernel version", value: input.KernelVersion, set: func(result *vmLegacyCompositionSegments, value string) { result.kernelVersion = value }},
		{label: "Firecracker version", value: input.FirecrackerVersion, set: func(result *vmLegacyCompositionSegments, value string) { result.runtimeVersion = value }},
	}
	architecture, err := normalizeVMLegacyArchitecture(input.Architecture)
	if err != nil {
		return vmLegacyCompositionSegments{}, err
	}
	result := vmLegacyCompositionSegments{architecture: architecture}
	for _, item := range values {
		value, err := requiredVMLegacyIDSegment(item.label, item.value)
		if err != nil {
			return vmLegacyCompositionSegments{}, err
		}
		item.set(&result, value)
	}
	return result, nil
}

type vmLegacyCompositionDigests struct {
	rootFS      string
	kernel      string
	config      string
	initrd      string
	firecracker string
	jailer      string
}

func vmLegacyCompositionDigestsForInput(input vmLegacyCompositionInput) (vmLegacyCompositionDigests, error) {
	result := vmLegacyCompositionDigests{}
	values := []struct {
		label    string
		evidence vmRuntimeAdoptionFileEvidence
		required bool
		set      func(string)
	}{
		{label: "immutable rootfs", evidence: input.RootFS, required: true, set: func(value string) { result.rootFS = value }},
		{label: "kernel", evidence: input.Kernel, required: true, set: func(value string) { result.kernel = value }},
		{label: "kernel config", evidence: input.KernelConfig, set: func(value string) { result.config = value }},
		{label: "initrd", evidence: input.Initrd, set: func(value string) { result.initrd = value }},
		{label: "Firecracker", evidence: input.Firecracker, required: true, set: func(value string) { result.firecracker = value }},
		{label: "jailer", evidence: input.Jailer, required: true, set: func(value string) { result.jailer = value }},
	}
	for _, item := range values {
		digest, err := vmLegacyEvidenceDigest(item.label, item.evidence, item.required)
		if err != nil {
			return vmLegacyCompositionDigests{}, err
		}
		item.set(digest)
	}
	return result, nil
}

func normalizeVMLegacyArchitecture(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "amd64", "x86_64":
		return "amd64", nil
	default:
		return "", fmt.Errorf("unsupported legacy VM architecture %q", value)
	}
}

func normalizeVMLegacyIDSegment(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var normalized strings.Builder
	normalized.Grow(len(value))
	separator := false
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			if separator && normalized.Len() > 0 {
				normalized.WriteByte('-')
			}
			normalized.WriteByte(c)
			separator = false
			continue
		}
		separator = true
	}
	return normalized.String()
}

func requiredVMLegacyIDSegment(label, value string) (string, error) {
	normalized := normalizeVMLegacyIDSegment(value)
	if normalized == "" {
		return "", fmt.Errorf("%s has no usable ID characters", label)
	}
	return normalized, nil
}

func vmLegacyEvidenceDigest(label string, evidence vmRuntimeAdoptionFileEvidence, required bool) (string, error) {
	if !evidence.Exists {
		if required {
			return "", fmt.Errorf("%s evidence is absent", label)
		}
		if evidence.SHA256 != "" {
			return "", fmt.Errorf("absent %s evidence has a SHA-256", label)
		}
		return "", nil
	}
	if !isLowerSHA256(evidence.SHA256) {
		return "", fmt.Errorf("%s evidence must have an exact lowercase SHA-256", label)
	}
	return evidence.SHA256, nil
}

func canonicalVMLegacyComposition(record vmLegacyCompositionRecord) ([]byte, string, error) {
	if err := validateVMLegacyCompositionRecord(record); err != nil {
		return nil, "", err
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, "", fmt.Errorf("encode legacy VM composition: %w", err)
	}
	if len(raw) > vmLegacyProvenanceMaxRecordBytes {
		return nil, "", fmt.Errorf("legacy VM composition exceeds %d bytes", vmLegacyProvenanceMaxRecordBytes)
	}
	return raw, vmLegacySHA256Bytes(raw), nil
}

func validateVMLegacyCompositionRecord(record vmLegacyCompositionRecord) error {
	if record.Schema != vmLegacyCompositionSchema || record.SchemaVersion != vmLegacyCompositionSchemaVersion {
		return fmt.Errorf("unsupported legacy VM composition schema")
	}
	if record.Architecture != "amd64" {
		return fmt.Errorf("legacy VM composition architecture must be amd64")
	}
	if err := validateVMLegacyCompositionSegments(record); err != nil {
		return err
	}
	return validateVMLegacyCompositionDigests(record)
}

func validateVMLegacyCompositionSegments(record vmLegacyCompositionRecord) error {
	for _, segment := range []struct {
		label string
		value string
	}{
		{label: "guest name", value: record.GuestBase.Name},
		{label: "guest version", value: record.GuestBase.Version},
		{label: "kernel version", value: record.Kernel.Version},
		{label: "Firecracker version", value: record.Runtime.Version},
	} {
		if segment.value == "" || normalizeVMLegacyIDSegment(segment.value) != segment.value {
			return fmt.Errorf("legacy VM composition %s is not normalized", segment.label)
		}
	}
	return nil
}

func validateVMLegacyCompositionDigests(record vmLegacyCompositionRecord) error {
	for _, digest := range []struct {
		label    string
		value    string
		required bool
	}{
		{label: "rootfs", value: record.GuestBase.RootFSSHA256, required: true},
		{label: "kernel", value: record.Kernel.KernelSHA256, required: true},
		{label: "kernel config", value: record.Kernel.ConfigSHA256},
		{label: "initrd", value: record.Kernel.InitrdSHA256},
		{label: "Firecracker", value: record.Runtime.FirecrackerSHA256, required: true},
		{label: "jailer", value: record.Runtime.JailerSHA256, required: true},
	} {
		if digest.value == "" && !digest.required {
			continue
		}
		if !isLowerSHA256(digest.value) {
			return fmt.Errorf("legacy VM composition %s must be an exact lowercase SHA-256", digest.label)
		}
	}
	return nil
}

func vmLegacyCompositionIDs(record vmLegacyCompositionRecord, provenanceSHA string) (string, string, string, error) {
	_, wantProvenanceSHA, err := canonicalVMLegacyComposition(record)
	if err != nil {
		return "", "", "", err
	}
	if provenanceSHA != wantProvenanceSHA {
		return "", "", "", fmt.Errorf("legacy VM provenance SHA-256 does not match canonical composition")
	}
	// Component IDs are durable keys, so they retain full component digests.
	// The full-composition digest remains a separate synthetic manifest binding;
	// changing one component must not rename either of the other components.
	guestID := "legacy-guest-" + record.GuestBase.Name + "-" + record.GuestBase.Version + "-" + record.GuestBase.RootFSSHA256
	kernelID := "legacy-kernel-" + record.Kernel.Version + "-" + record.Kernel.KernelSHA256
	runtimeID := "legacy-firecracker-" + record.Runtime.Version + "-" + record.Runtime.FirecrackerSHA256 + "-jailer-" + record.Runtime.JailerSHA256
	return guestID, kernelID, runtimeID, nil
}

func vmLegacySHA256Bytes(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func defaultVMRuntimeAdoptionEvidenceDeps() vmRuntimeAdoptionEvidenceDeps {
	return vmRuntimeAdoptionEvidenceDeps{
		trustedUID:   0,
		maxFileBytes: vmRuntimeAdoptionMaxFileBytes,
		copy:         io.Copy,
		resolveZVOL:  resolveVMRuntimeAdoptionZVOL,
	}
}

func completeVMRuntimeAdoptionEvidenceDeps(deps vmRuntimeAdoptionEvidenceDeps) vmRuntimeAdoptionEvidenceDeps {
	defaults := defaultVMRuntimeAdoptionEvidenceDeps()
	if deps.maxFileBytes <= 0 {
		deps.maxFileBytes = defaults.maxFileBytes
	}
	if deps.copy == nil {
		deps.copy = defaults.copy
	}
	if deps.resolveZVOL == nil {
		deps.resolveZVOL = defaults.resolveZVOL
	}
	return deps
}

func collectVMRuntimeAdoptionFileEvidence(path string, required bool, deps vmRuntimeAdoptionEvidenceDeps) (evidence vmRuntimeAdoptionFileEvidence, retErr error) {
	deps = completeVMRuntimeAdoptionEvidenceDeps(deps)
	evidence.Path = path
	if err := validateVMRuntimeAdoptionEvidencePath(path); err != nil {
		return evidence, err
	}
	file, parent, name, err := openVMRuntimeAdoptionPath(path)
	if errors.Is(err, unix.ENOENT) && !required {
		return evidence, nil
	}
	if err != nil {
		return evidence, fmt.Errorf("open immutable VM component evidence: %w", err)
	}
	defer func() {
		retErr = errors.Join(retErr, closeVMRuntimeAdoptionPath(file, parent))
	}()

	before, err := vmRuntimeAdoptionMetadataForFile(file, path)
	if err != nil {
		return evidence, err
	}
	if err := validateVMRuntimeAdoptionEvidenceMetadata(path, before, deps); err != nil {
		return evidence, err
	}
	digest, err := hashVMRuntimeAdoptionEvidence(file, path, before, deps)
	if err != nil {
		return evidence, err
	}
	evidence = vmRuntimeAdoptionFileEvidence{
		Path: path, Exists: true, Device: before.Device, Inode: before.Inode, Size: before.Size,
		Mode: before.Mode, UID: before.UID, GID: before.GID, MTimeNS: before.MTimeNS,
		SHA256: digest,
	}
	if err := revalidateVMRuntimeAdoptionName(parent, name, evidence.metadata()); err != nil {
		return vmRuntimeAdoptionFileEvidence{Path: path}, fmt.Errorf("revalidate immutable VM component %s: %w", path, err)
	}
	return evidence, nil
}

func validateVMRuntimeAdoptionEvidenceMetadata(path string, metadata vmRuntimeAdoptionFileMetadata, deps vmRuntimeAdoptionEvidenceDeps) error {
	if metadata.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("immutable VM component %s must be a regular file", path)
	}
	if metadata.UID != deps.trustedUID {
		return fmt.Errorf("immutable VM component %s owner UID is %d, want %d", path, metadata.UID, deps.trustedUID)
	}
	if metadata.Mode&0o022 != 0 {
		return fmt.Errorf("immutable VM component %s is group or other writable", path)
	}
	if metadata.Size < 0 || metadata.Size > deps.maxFileBytes {
		return fmt.Errorf("immutable VM component %s size %d exceeds %d bytes", path, metadata.Size, deps.maxFileBytes)
	}
	return nil
}

func hashVMRuntimeAdoptionEvidence(file *os.File, path string, before vmRuntimeAdoptionFileMetadata, deps vmRuntimeAdoptionEvidenceDeps) (string, error) {
	hasher := sha256.New()
	n, err := deps.copy(hasher, io.LimitReader(file, deps.maxFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("hash immutable VM component %s: %w", path, err)
	}
	if n > deps.maxFileBytes {
		return "", fmt.Errorf("immutable VM component %s exceeds %d bytes", path, deps.maxFileBytes)
	}
	if n != before.Size {
		return "", fmt.Errorf("immutable VM component %s size changed while hashing", path)
	}
	after, err := vmRuntimeAdoptionMetadataForFile(file, path)
	if err != nil {
		return "", err
	}
	if before != after {
		return "", fmt.Errorf("immutable VM component %s changed while hashing", path)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func collectVMRuntimeActiveDiskEvidence(path, backend string, bytes int64, deps vmRuntimeAdoptionEvidenceDeps) (evidence vmRuntimeAdoptionActiveDiskEvidence, retErr error) {
	deps = completeVMRuntimeAdoptionEvidenceDeps(deps)
	if err := validateVMRuntimeAdoptionEvidencePath(path); err != nil {
		return evidence, err
	}
	switch backend {
	case vmDiskBackendRaw, vmDiskBackendZVOL:
	default:
		return evidence, fmt.Errorf("active VM disk backend must be %q or %q", vmDiskBackendRaw, vmDiskBackendZVOL)
	}
	if bytes <= 0 {
		return evidence, fmt.Errorf("active VM disk declared bytes must be positive")
	}
	if backend == vmDiskBackendZVOL {
		return collectVMRuntimeZVOLDiskEvidence(path, backend, bytes, deps)
	}
	return collectVMRuntimeRawDiskEvidence(path, backend, bytes)
}

func collectVMRuntimeZVOLDiskEvidence(path, backend string, bytes int64, deps vmRuntimeAdoptionEvidenceDeps) (vmRuntimeAdoptionActiveDiskEvidence, error) {
	if _, err := vmRuntimeAdoptionZVOLDataset(path); err != nil {
		return vmRuntimeAdoptionActiveDiskEvidence{}, err
	}
	resolution, err := deps.resolveZVOL(path)
	if err != nil {
		return vmRuntimeAdoptionActiveDiskEvidence{}, fmt.Errorf("resolve active VM zvol evidence: %w", err)
	}
	dataset, err := validateVMRuntimeAdoptionZVOLResolution(path, resolution)
	if err != nil {
		return vmRuntimeAdoptionActiveDiskEvidence{}, err
	}
	metadata := resolution.Metadata
	return vmRuntimeAdoptionActiveDiskEvidence{
		Path: path, Backend: backend, Bytes: bytes, Dataset: dataset, ResolvedPath: resolution.ResolvedPath,
		LinkDevice: resolution.LinkDevice, LinkInode: resolution.LinkInode, LinkMode: resolution.LinkMode,
		LinkUID: resolution.LinkUID, LinkGID: resolution.LinkGID,
		Device: metadata.Device, Inode: metadata.Inode, RDevice: metadata.RDevice, Size: metadata.Size,
		Mode: metadata.Mode, UID: metadata.UID, GID: metadata.GID, MTimeNS: metadata.MTimeNS,
	}, nil
}

func collectVMRuntimeRawDiskEvidence(path, backend string, bytes int64) (evidence vmRuntimeAdoptionActiveDiskEvidence, retErr error) {
	file, parent, name, err := openVMRuntimeAdoptionPath(path)
	if err != nil {
		return evidence, fmt.Errorf("open active VM disk metadata: %w", err)
	}
	defer func() {
		retErr = errors.Join(retErr, closeVMRuntimeAdoptionPath(file, parent))
	}()
	metadata, err := vmRuntimeAdoptionMetadataForFile(file, path)
	if err != nil {
		return evidence, err
	}
	if metadata.Mode&unix.S_IFMT != unix.S_IFREG {
		return evidence, fmt.Errorf("active raw VM disk %s must be a regular file", path)
	}
	evidence = vmRuntimeAdoptionActiveDiskEvidence{
		Path: path, Backend: backend, Bytes: bytes, ResolvedPath: path, Device: metadata.Device, Inode: metadata.Inode,
		RDevice: metadata.RDevice, Size: metadata.Size, Mode: metadata.Mode, UID: metadata.UID,
		GID: metadata.GID, MTimeNS: metadata.MTimeNS,
	}
	if err := revalidateVMRuntimeAdoptionName(parent, name, metadata); err != nil {
		return vmRuntimeAdoptionActiveDiskEvidence{}, fmt.Errorf("revalidate active VM disk %s: %w", path, err)
	}
	return evidence, nil
}

func validateVMRuntimeAdoptionZVOLResolution(path string, resolution vmRuntimeAdoptionZVOLResolution) (string, error) {
	dataset, err := vmRuntimeAdoptionZVOLDataset(path)
	if err != nil {
		return "", err
	}
	if resolution.Dataset != dataset {
		return "", fmt.Errorf("active VM zvol resolver dataset is %q, want %q", resolution.Dataset, dataset)
	}
	if err := validateVMRuntimeAdoptionZVOLLink(resolution); err != nil {
		return "", err
	}
	if err := validateVMRuntimeAdoptionZVOLTarget(resolution); err != nil {
		return "", err
	}
	return dataset, nil
}

func validateVMRuntimeAdoptionZVOLLink(resolution vmRuntimeAdoptionZVOLResolution) error {
	if resolution.ResolvedPath == "" || !filepath.IsAbs(resolution.ResolvedPath) || filepath.Clean(resolution.ResolvedPath) != resolution.ResolvedPath || !strings.HasPrefix(resolution.ResolvedPath, "/dev/") {
		return fmt.Errorf("active VM zvol resolved path must be a clean path beneath /dev")
	}
	if resolution.LinkDevice == 0 || resolution.LinkInode == 0 || resolution.LinkMode&unix.S_IFMT != unix.S_IFLNK {
		return fmt.Errorf("active VM zvol link identity is incomplete or is not a symbolic link")
	}
	if resolution.LinkUID != 0 {
		return fmt.Errorf("active VM zvol link owner UID is %d, want 0", resolution.LinkUID)
	}
	return nil
}

func validateVMRuntimeAdoptionZVOLTarget(resolution vmRuntimeAdoptionZVOLResolution) error {
	metadata := resolution.Metadata
	if metadata.Mode&unix.S_IFMT != unix.S_IFBLK {
		return fmt.Errorf("active VM zvol target %s must be a block device", resolution.ResolvedPath)
	}
	if metadata.UID != 0 {
		return fmt.Errorf("active VM zvol target %s owner UID is %d, want 0", resolution.ResolvedPath, metadata.UID)
	}
	if metadata.Device == 0 || metadata.Inode == 0 || metadata.RDevice == 0 {
		return fmt.Errorf("active VM zvol target identity is incomplete")
	}
	return nil
}

func vmRuntimeAdoptionZVOLDataset(path string) (string, error) {
	const prefix = "/dev/zvol/"
	if !strings.HasPrefix(path, prefix) {
		return "", fmt.Errorf("active VM zvol path must start with %s", prefix)
	}
	dataset := strings.TrimPrefix(path, prefix)
	if err := validateZFSName("active dataset", dataset, true); err != nil {
		return "", err
	}
	return dataset, nil
}

type vmRuntimeAdoptionFileMetadata struct {
	Device  uint64
	Inode   uint64
	RDevice uint64
	Size    int64
	Mode    uint32
	UID     uint32
	GID     uint32
	MTimeNS int64
}

func (evidence vmRuntimeAdoptionFileEvidence) metadata() vmRuntimeAdoptionFileMetadata {
	return vmRuntimeAdoptionFileMetadata{
		Device: evidence.Device, Inode: evidence.Inode, Size: evidence.Size, Mode: evidence.Mode,
		UID: evidence.UID, GID: evidence.GID, MTimeNS: evidence.MTimeNS,
	}
}

func vmRuntimeAdoptionMetadataForFile(file *os.File, path string) (vmRuntimeAdoptionFileMetadata, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return vmRuntimeAdoptionFileMetadata{}, fmt.Errorf("inspect VM path %s: %w", path, err)
	}
	info, err := file.Stat()
	if err != nil {
		return vmRuntimeAdoptionFileMetadata{}, fmt.Errorf("inspect VM path timestamp %s: %w", path, err)
	}
	return vmRuntimeAdoptionFileMetadata{
		Device: uint64(stat.Dev), Inode: uint64(stat.Ino), RDevice: uint64(stat.Rdev), Size: stat.Size,
		Mode: uint32(stat.Mode), UID: stat.Uid, GID: stat.Gid, MTimeNS: info.ModTime().UnixNano(),
	}, nil
}

func resolveVMRuntimeAdoptionZVOL(path string) (resolution vmRuntimeAdoptionZVOLResolution, retErr error) {
	dataset, err := vmRuntimeAdoptionZVOLDataset(path)
	if err != nil {
		return resolution, err
	}
	linkDir, ancestor, err := openVMRuntimeAdoptionZVOLLinkParent(path)
	if err != nil {
		return resolution, err
	}
	defer func() { retErr = errors.Join(retErr, closeVMRuntimeAdoptionPath(linkDir, ancestor)) }()
	name, before, linkTarget, resolvedPath, err := inspectVMRuntimeAdoptionZVOLLink(linkDir, path)
	if err != nil {
		return resolution, err
	}
	target, targetParent, metadata, err := openVMRuntimeAdoptionZVOLTarget(resolvedPath)
	if err != nil {
		return resolution, err
	}
	defer func() { retErr = errors.Join(retErr, closeVMRuntimeAdoptionPath(target, targetParent)) }()
	if err := revalidateVMRuntimeAdoptionZVOLLink(linkDir, name, before, linkTarget); err != nil {
		return resolution, err
	}
	return vmRuntimeAdoptionZVOLResolution{
		Dataset: dataset, ResolvedPath: resolvedPath, LinkDevice: uint64(before.Dev), LinkInode: uint64(before.Ino),
		LinkMode: uint32(before.Mode), LinkUID: before.Uid, LinkGID: before.Gid, Metadata: metadata,
	}, nil
}

func openVMRuntimeAdoptionZVOLLinkParent(path string) (*os.File, *os.File, error) {
	parentPath := filepath.Dir(path)
	linkDir, ancestor, _, err := openVMRuntimeAdoptionPath(parentPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open active VM zvol link parent: %w", err)
	}
	metadata, err := vmRuntimeAdoptionMetadataForFile(linkDir, parentPath)
	if err != nil {
		return nil, nil, errors.Join(err, closeVMRuntimeAdoptionPath(linkDir, ancestor))
	}
	if metadata.Mode&unix.S_IFMT != unix.S_IFDIR || metadata.UID != 0 || metadata.Mode&0o022 != 0 {
		return nil, nil, errors.Join(fmt.Errorf("active VM zvol link parent must be a root-controlled directory"), closeVMRuntimeAdoptionPath(linkDir, ancestor))
	}
	return linkDir, ancestor, nil
}

func inspectVMRuntimeAdoptionZVOLLink(linkDir *os.File, path string) (string, unix.Stat_t, string, string, error) {
	name := filepath.Base(path)
	var before unix.Stat_t
	if err := unix.Fstatat(int(linkDir.Fd()), name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return "", before, "", "", fmt.Errorf("inspect active VM zvol link: %w", err)
	}
	if uint32(before.Mode)&unix.S_IFMT != unix.S_IFLNK || before.Uid != 0 {
		return "", before, "", "", fmt.Errorf("active VM zvol path must be a root-owned symbolic link")
	}
	linkTarget, err := readVMRuntimeAdoptionLinkAt(linkDir, name)
	if err != nil {
		return "", before, "", "", err
	}
	resolvedPath := linkTarget
	if !filepath.IsAbs(resolvedPath) {
		resolvedPath = filepath.Join(filepath.Dir(path), resolvedPath)
	}
	resolvedPath = filepath.Clean(resolvedPath)
	if !strings.HasPrefix(resolvedPath, "/dev/") {
		return "", before, "", "", fmt.Errorf("active VM zvol link target must resolve beneath /dev")
	}
	return name, before, linkTarget, resolvedPath, nil
}

func openVMRuntimeAdoptionZVOLTarget(path string) (*os.File, *os.File, vmRuntimeAdoptionFileMetadata, error) {
	target, parent, name, err := openVMRuntimeAdoptionPath(path)
	if err != nil {
		return nil, nil, vmRuntimeAdoptionFileMetadata{}, fmt.Errorf("open active VM zvol block target: %w", err)
	}
	metadata, err := vmRuntimeAdoptionMetadataForFile(target, path)
	if err == nil && (metadata.Mode&unix.S_IFMT != unix.S_IFBLK || metadata.UID != 0) {
		err = fmt.Errorf("active VM zvol target must be a root-owned block device")
	}
	if err == nil {
		err = revalidateVMRuntimeAdoptionName(parent, name, metadata)
		if err != nil {
			err = fmt.Errorf("revalidate active VM zvol target: %w", err)
		}
	}
	if err != nil {
		return nil, nil, vmRuntimeAdoptionFileMetadata{}, errors.Join(err, closeVMRuntimeAdoptionPath(target, parent))
	}
	return target, parent, metadata, nil
}

func revalidateVMRuntimeAdoptionZVOLLink(linkDir *os.File, name string, before unix.Stat_t, linkTarget string) error {
	var after unix.Stat_t
	if err := unix.Fstatat(int(linkDir.Fd()), name, &after, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("revalidate active VM zvol link: %w", err)
	}
	afterTarget, err := readVMRuntimeAdoptionLinkAt(linkDir, name)
	if err != nil {
		return err
	}
	if before.Dev != after.Dev || before.Ino != after.Ino || before.Mode != after.Mode || before.Uid != after.Uid || before.Gid != after.Gid || before.Size != after.Size || linkTarget != afterTarget {
		return fmt.Errorf("active VM zvol link changed while resolving")
	}
	return nil
}

func readVMRuntimeAdoptionLinkAt(parent *os.File, name string) (string, error) {
	for size := 256; size <= 4096; size *= 2 {
		buffer := make([]byte, size)
		n, err := unix.Readlinkat(int(parent.Fd()), name, buffer)
		if err != nil {
			return "", fmt.Errorf("read active VM zvol link: %w", err)
		}
		if n < len(buffer) {
			if n == 0 || bytes.IndexByte(buffer[:n], 0) >= 0 {
				return "", fmt.Errorf("active VM zvol link target is empty or malformed")
			}
			return string(buffer[:n]), nil
		}
	}
	return "", fmt.Errorf("active VM zvol link target exceeds 4095 bytes")
}

func validateVMRuntimeAdoptionEvidencePath(path string) error {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(path) != path || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("VM adoption evidence path must be clean, absolute, and trimmed: %q", path)
	}
	return nil
}

func openVMRuntimeAdoptionPath(path string) (*os.File, *os.File, string, error) {
	if err := validateVMRuntimeAdoptionEvidencePath(path); err != nil {
		return nil, nil, "", err
	}
	current, err := os.Open(string(filepath.Separator))
	if err != nil {
		return nil, nil, "", fmt.Errorf("open filesystem root: %w", err)
	}
	if path == string(filepath.Separator) {
		return current, nil, "", nil
	}
	components := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	currentPath := string(filepath.Separator)
	for index, component := range components {
		childPath := filepath.Join(currentPath, component)
		last := index == len(components)-1
		child, err := openVMRuntimeAdoptionChild(current, component, childPath, last)
		if err != nil {
			_ = current.Close()
			return nil, nil, "", err
		}
		if last {
			return child, current, component, nil
		}
		if err := current.Close(); err != nil {
			_ = child.Close()
			return nil, nil, "", fmt.Errorf("close VM path ancestor %s: %w", currentPath, err)
		}
		current = child
		currentPath = childPath
	}
	_ = current.Close()
	return nil, nil, "", fmt.Errorf("open VM path %s", path)
}

func openVMRuntimeAdoptionChild(parent *os.File, name, path string, last bool) (*os.File, error) {
	var before unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, fmt.Errorf("inspect VM path %s: %w", path, err)
	}
	beforeType := uint32(before.Mode) & unix.S_IFMT
	if err := validateVMRuntimeAdoptionChildType(path, beforeType, last); err != nil {
		return nil, err
	}
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	if last {
		flags |= unix.O_NONBLOCK
	} else {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Openat(int(parent.Fd()), name, flags, 0)
	if err != nil {
		return nil, fmt.Errorf("open VM path %s without following symbolic links: %w", path, err)
	}
	child := os.NewFile(uintptr(fd), path)
	if child == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("bind VM path descriptor %s", path)
	}
	if err := validateOpenedVMRuntimeAdoptionChild(child, before, beforeType, path); err != nil {
		return nil, closeVMJailerFileOnError(child, err)
	}
	return child, nil
}

func validateVMRuntimeAdoptionChildType(path string, fileType uint32, last bool) error {
	if fileType == unix.S_IFLNK {
		return fmt.Errorf("refusing symbolic link in VM path %s", path)
	}
	if !last && fileType != unix.S_IFDIR {
		return fmt.Errorf("VM path ancestor %s must be a directory", path)
	}
	return nil
}

func validateOpenedVMRuntimeAdoptionChild(file *os.File, before unix.Stat_t, beforeType uint32, path string) error {
	var opened unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &opened); err != nil {
		return fmt.Errorf("inspect opened VM path %s: %w", path, err)
	}
	if before.Dev != opened.Dev || before.Ino != opened.Ino || beforeType != uint32(opened.Mode)&unix.S_IFMT {
		return fmt.Errorf("VM path %s changed while opening", path)
	}
	return nil
}

func closeVMRuntimeAdoptionPath(file, parent *os.File) error {
	var errs []error
	if file != nil {
		errs = append(errs, file.Close())
	}
	if parent != nil {
		errs = append(errs, parent.Close())
	}
	return errors.Join(errs...)
}

func revalidateVMRuntimeAdoptionName(parent *os.File, name string, want vmRuntimeAdoptionFileMetadata) error {
	if parent == nil || name == "" {
		return fmt.Errorf("VM path has no parent entry")
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return fmt.Errorf("bind revalidation descriptor")
	}
	got, inspectErr := vmRuntimeAdoptionMetadataForFile(file, name)
	closeErr := file.Close()
	if inspectErr != nil || closeErr != nil {
		return errors.Join(inspectErr, closeErr)
	}
	if got != want {
		return fmt.Errorf("VM path metadata changed")
	}
	return nil
}

func defaultVMLegacyCompositionStoreDeps() vmLegacyCompositionStoreDeps {
	return vmLegacyCompositionStoreDeps{
		trustedUID:        0,
		random:            rand.Reader,
		renameNoReplaceAt: renameVMJailerUnitNameNoReplaceAt,
		unlinkAt:          unix.Unlinkat,
		syncFile:          func(file *os.File) error { return file.Sync() },
		syncDir:           func(dir *os.File) error { return dir.Sync() },
	}
}

func completeVMLegacyCompositionStoreDeps(deps vmLegacyCompositionStoreDeps) vmLegacyCompositionStoreDeps {
	defaults := defaultVMLegacyCompositionStoreDeps()
	if deps.random == nil {
		deps.random = defaults.random
	}
	if deps.renameNoReplaceAt == nil {
		deps.renameNoReplaceAt = defaults.renameNoReplaceAt
	}
	if deps.unlinkAt == nil {
		deps.unlinkAt = defaults.unlinkAt
	}
	if deps.syncFile == nil {
		deps.syncFile = defaults.syncFile
	}
	if deps.syncDir == nil {
		deps.syncDir = defaults.syncDir
	}
	return deps
}

func persistVMLegacyComposition(root string, record vmLegacyCompositionRecord, deps vmLegacyCompositionStoreDeps) (publication vmLegacyCompositionPublication, retErr error) {
	deps = completeVMLegacyCompositionStoreDeps(deps)
	published := false
	defer func() {
		retErr = classifyVMLegacyCompositionPostPublicationError(publication, published, retErr)
	}()
	raw, digest, err := canonicalVMLegacyComposition(record)
	if err != nil {
		return publication, err
	}
	if err := validateVMRuntimeAdoptionEvidencePath(root); err != nil {
		return publication, fmt.Errorf("validate VM component provenance root: %w", err)
	}
	rootDir, rootParent, _, err := openVMRuntimeAdoptionPath(root)
	if err != nil {
		return publication, fmt.Errorf("open VM component provenance root: %w", err)
	}
	defer func() {
		retErr = errors.Join(retErr, closeVMRuntimeAdoptionPath(rootDir, rootParent))
	}()
	if err := validateVMLegacyProvenanceRoot(rootDir, deps.trustedUID); err != nil {
		return publication, err
	}

	provenanceDir, err := ensureVMLegacyProvenanceDir(rootDir, vmLegacyProvenanceDirName, deps)
	if err != nil {
		return publication, err
	}
	defer func() { retErr = errors.Join(retErr, provenanceDir.Close()) }()
	digestDir, err := ensureVMLegacyProvenanceDir(provenanceDir, vmLegacyProvenanceDigestDirName, deps)
	if err != nil {
		return publication, err
	}
	defer func() { retErr = errors.Join(retErr, digestDir.Close()) }()

	name := digest + ".json"
	publication = vmLegacyCompositionPublication{
		Path:   filepath.Join(root, vmLegacyProvenanceDirName, vmLegacyProvenanceDigestDirName, name),
		SHA256: digest,
		Bytes:  append([]byte(nil), raw...),
	}
	stagedName, stagedIdentity, err := stageVMLegacyComposition(digestDir, name, raw, deps)
	if err != nil {
		return vmLegacyCompositionPublication{}, err
	}
	cleanupStaged := true
	defer func() {
		if cleanupStaged {
			retErr = errors.Join(retErr, cleanupVMLegacyCompositionStaged(digestDir, stagedName, stagedIdentity, deps))
		}
	}()

	result, didPublish, shouldCleanup, err := publishVMLegacyCompositionStage(digestDir, name, raw, publication, stagedName, stagedIdentity, deps)
	published = didPublish
	cleanupStaged = shouldCleanup
	return result, err
}

func classifyVMLegacyCompositionPostPublicationError(publication vmLegacyCompositionPublication, published bool, err error) error {
	if !published || err == nil {
		return err
	}
	return &vmLegacyCompositionPostPublicationError{cause: err, publication: publication}
}

func publishVMLegacyCompositionStage(digestDir *os.File, name string, raw []byte, publication vmLegacyCompositionPublication, stagedName string, stagedIdentity vmJailerFileIdentity, deps vmLegacyCompositionStoreDeps) (vmLegacyCompositionPublication, bool, bool, error) {
	err := deps.renameNoReplaceAt(int(digestDir.Fd()), stagedName, int(digestDir.Fd()), name)
	if errors.Is(err, unix.EEXIST) {
		if unlinkErr := unlinkVMJailerNameIfIdentityWith(digestDir, stagedName, stagedIdentity, deps.unlinkAt); unlinkErr != nil {
			return vmLegacyCompositionPublication{}, false, true, unlinkErr
		}
		existing, inspectErr := readVMLegacyCompositionEntry(digestDir, name, deps.trustedUID)
		if inspectErr != nil {
			return vmLegacyCompositionPublication{}, false, false, errors.Join(inspectErr, syncVMLegacyCompositionDir(digestDir, "staged cleanup", deps))
		}
		if !bytes.Equal(existing, raw) {
			return vmLegacyCompositionPublication{}, false, false, errors.Join(
				fmt.Errorf("existing legacy VM composition %s conflicts with its content address", publication.Path),
				syncVMLegacyCompositionDir(digestDir, "staged cleanup", deps),
			)
		}
		if err := syncVMLegacyCompositionDir(digestDir, "verified existing publication", deps); err != nil {
			return publication, true, false, err
		}
		return publication, true, false, nil
	}
	if err != nil {
		result, didPublish, classifyErr := classifyVMLegacyCompositionPublishError(digestDir, name, raw, publication, stagedName, stagedIdentity, err, deps)
		return result, didPublish, true, classifyErr
	}
	if err := validateVMLegacyCompositionPublishedName(digestDir, name, stagedIdentity, deps.trustedUID, raw); err != nil {
		return publication, true, false, err
	}
	if err := syncVMLegacyCompositionDir(digestDir, "publication", deps); err != nil {
		return publication, true, false, err
	}
	return publication, true, false, nil
}

func classifyVMLegacyCompositionPublishError(digestDir *os.File, name string, raw []byte, publication vmLegacyCompositionPublication, stagedName string, stagedIdentity vmJailerFileIdentity, publishErr error, deps vmLegacyCompositionStoreDeps) (vmLegacyCompositionPublication, bool, error) {
	cause := fmt.Errorf("publish legacy VM composition without replacement: %w", publishErr)
	existing, inspectErr := readVMLegacyCompositionEntry(digestDir, name, deps.trustedUID)
	if inspectErr == nil {
		return classifyExistingVMLegacyComposition(raw, existing, publication, digestDir, cause, deps)
	}
	if !errors.Is(inspectErr, unix.ENOENT) {
		return publication, false, &vmLegacyCompositionPublicationUncertainError{
			cause: errors.Join(cause, inspectErr, syncVMLegacyCompositionDir(digestDir, "uncertain publication", deps)), publication: publication,
		}
	}
	stagedIntact, stagedErr := isVMLegacyCompositionStagedIntact(digestDir, stagedName, stagedIdentity, deps.trustedUID, int64(len(raw)))
	if stagedErr == nil && stagedIntact {
		return vmLegacyCompositionPublication{}, false, cause
	}
	return publication, false, &vmLegacyCompositionPublicationUncertainError{
		cause:       errors.Join(cause, stagedErr, fmt.Errorf("canonical name is absent and staging identity is not intact"), syncVMLegacyCompositionDir(digestDir, "uncertain publication", deps)),
		publication: publication,
	}
}

func classifyExistingVMLegacyComposition(raw, existing []byte, publication vmLegacyCompositionPublication, digestDir *os.File, cause error, deps vmLegacyCompositionStoreDeps) (vmLegacyCompositionPublication, bool, error) {
	if bytes.Equal(existing, raw) {
		return publication, true, errors.Join(cause, syncVMLegacyCompositionDir(digestDir, "retained publication", deps))
	}
	return publication, false, &vmLegacyCompositionPublicationUncertainError{
		cause:       errors.Join(cause, fmt.Errorf("canonical legacy VM composition conflicts with expected bytes"), syncVMLegacyCompositionDir(digestDir, "uncertain publication", deps)),
		publication: publication,
	}
}

func validateVMLegacyProvenanceRoot(root *os.File, uid uint32) error {
	metadata, err := vmRuntimeAdoptionMetadataForFile(root, root.Name())
	if err != nil {
		return err
	}
	if metadata.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("VM component provenance root must be a directory")
	}
	if metadata.UID != uid {
		return fmt.Errorf("VM component provenance root owner UID is %d, want %d", metadata.UID, uid)
	}
	if metadata.Mode&0o022 != 0 {
		return fmt.Errorf("VM component provenance root is group or other writable")
	}
	return nil
}

func ensureVMLegacyProvenanceDir(parent *os.File, name string, deps vmLegacyCompositionStoreDeps) (*os.File, error) {
	created := false
	if err := unix.Mkdirat(int(parent.Fd()), name, 0o700); err == nil {
		created = true
	} else if !errors.Is(err, unix.EEXIST) {
		return nil, fmt.Errorf("create VM component provenance directory %s: %w", name, err)
	}
	dir, err := openVMLegacyProvenanceDir(parent, name, deps.trustedUID)
	if err != nil {
		return nil, err
	}
	// Sync even when the name already existed. A prior process may have created
	// it and then failed its parent sync, so EEXIST is not durability evidence.
	if err := deps.syncDir(parent); err != nil {
		action := "opening"
		if created {
			action = "creating"
		}
		return nil, closeVMJailerFileOnError(dir, fmt.Errorf("sync VM component provenance parent after %s %s: %w", action, name, err))
	}
	return dir, nil
}

func openVMLegacyProvenanceDir(parent *os.File, name string, uid uint32) (*os.File, error) {
	var before unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, fmt.Errorf("inspect VM component provenance directory %s: %w", name, err)
	}
	if uint32(before.Mode)&unix.S_IFMT == unix.S_IFLNK {
		return nil, fmt.Errorf("refusing symbolic link VM component provenance directory %s", name)
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open VM component provenance directory %s without following symbolic links: %w", name, err)
	}
	dir := os.NewFile(uintptr(fd), name)
	if dir == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("bind VM component provenance directory %s", name)
	}
	metadata, err := vmRuntimeAdoptionMetadataForFile(dir, name)
	if err != nil {
		return nil, closeVMJailerFileOnError(dir, err)
	}
	if err := validateVMLegacyProvenanceDirMetadata(name, before, metadata, uid); err != nil {
		return nil, closeVMJailerFileOnError(dir, err)
	}
	return dir, nil
}

func validateVMLegacyProvenanceDirMetadata(name string, before unix.Stat_t, metadata vmRuntimeAdoptionFileMetadata, uid uint32) error {
	if uint64(before.Dev) != metadata.Device || uint64(before.Ino) != metadata.Inode {
		return fmt.Errorf("VM component provenance directory %s changed while opening", name)
	}
	mode := metadata.Mode & 0o7777
	if metadata.Mode&unix.S_IFMT != unix.S_IFDIR || metadata.UID != uid || mode != 0o700 && mode != 0o2700 {
		return fmt.Errorf("VM component provenance directory %s must be owned by UID %d with permissions 0700 (setgid inheritance is allowed)", name, uid)
	}
	return nil
}

func stageVMLegacyComposition(dir *os.File, canonical string, raw []byte, deps vmLegacyCompositionStoreDeps) (string, vmJailerFileIdentity, error) {
	for range 128 {
		name, identity, retry, err := stageVMLegacyCompositionAttempt(dir, canonical, raw, deps)
		if retry {
			continue
		}
		if err != nil {
			return "", vmJailerFileIdentity{}, err
		}
		return name, identity, nil
	}
	return "", vmJailerFileIdentity{}, fmt.Errorf("exhausted legacy VM composition staging names")
}

func stageVMLegacyCompositionAttempt(dir *os.File, canonical string, raw []byte, deps vmLegacyCompositionStoreDeps) (string, vmJailerFileIdentity, bool, error) {
	var suffix [12]byte
	if _, err := io.ReadFull(deps.random, suffix[:]); err != nil {
		return "", vmJailerFileIdentity{}, false, fmt.Errorf("generate legacy VM composition staging name: %w", err)
	}
	name := "." + canonical + ".staged-" + hex.EncodeToString(suffix[:])
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if errors.Is(err, unix.EEXIST) {
		return "", vmJailerFileIdentity{}, true, nil
	}
	if err != nil {
		return "", vmJailerFileIdentity{}, false, fmt.Errorf("create staged legacy VM composition: %w", err)
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return "", vmJailerFileIdentity{}, false, fmt.Errorf("bind staged legacy VM composition descriptor")
	}
	identity, _, inspectErr := vmJailerFileIdentityForFile(file)
	if inspectErr != nil {
		return "", vmJailerFileIdentity{}, false, errors.Join(inspectErr, file.Close(), unix.Unlinkat(int(dir.Fd()), name, 0))
	}
	if err := writeVMLegacyCompositionStagedFile(dir, file, name, identity, raw, deps); err != nil {
		return "", vmJailerFileIdentity{}, false, err
	}
	return name, identity, false, nil
}

func writeVMLegacyCompositionStagedFile(dir, file *os.File, name string, identity vmJailerFileIdentity, raw []byte, deps vmLegacyCompositionStoreDeps) error {
	n, err := file.Write(raw)
	if err == nil && n != len(raw) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = file.Chmod(0o600)
	}
	if err == nil {
		err = deps.syncFile(file)
	}
	if err == nil {
		err = validateOpenVMLegacyCompositionFile(file, identity, deps.trustedUID, int64(len(raw)))
	}
	if err == nil {
		err = file.Close()
		if err == nil {
			return nil
		}
	} else {
		err = errors.Join(err, file.Close())
	}
	return errors.Join(err, unlinkVMJailerNameIfIdentityWith(dir, name, identity, deps.unlinkAt))
}

func validateOpenVMLegacyCompositionFile(file *os.File, want vmJailerFileIdentity, uid uint32, size int64) error {
	got, stat, err := vmJailerFileIdentityForFile(file)
	if err != nil {
		return err
	}
	if got != want || uint32(stat.Mode)&unix.S_IFMT != unix.S_IFREG || stat.Uid != uid || uint32(stat.Mode)&0o7777 != 0o600 || stat.Size != size {
		return fmt.Errorf("legacy VM composition identity, owner, size, or 0600 permissions changed")
	}
	return nil
}

func isVMLegacyCompositionStagedIntact(dir *os.File, name string, want vmJailerFileIdentity, uid uint32, size int64) (bool, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect staged legacy VM composition after publication error: %w", err)
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return false, fmt.Errorf("bind staged legacy VM composition after publication error")
	}
	validateErr := validateOpenVMLegacyCompositionFile(file, want, uid, size)
	closeErr := file.Close()
	if validateErr != nil || closeErr != nil {
		return false, errors.Join(validateErr, closeErr)
	}
	return true, nil
}

func cleanupVMLegacyCompositionStaged(dir *os.File, name string, identity vmJailerFileIdentity, deps vmLegacyCompositionStoreDeps) error {
	unlinkErr := unlinkVMJailerNameIfIdentityWith(dir, name, identity, deps.unlinkAt)
	if unlinkErr != nil {
		return unlinkErr
	}
	return syncVMLegacyCompositionDir(dir, "staged cleanup", deps)
}

func syncVMLegacyCompositionDir(dir *os.File, action string, deps vmLegacyCompositionStoreDeps) error {
	if err := deps.syncDir(dir); err != nil {
		return fmt.Errorf("sync legacy VM composition %s: %w", action, err)
	}
	return nil
}

func readVMLegacyCompositionEntry(dir *os.File, name string, uid uint32) ([]byte, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open existing legacy VM composition without following symbolic links: %w", err)
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("bind existing legacy VM composition descriptor")
	}
	return readOpenVMLegacyCompositionEntry(dir, file, name, uid)
}

func readOpenVMLegacyCompositionEntry(dir, file *os.File, name string, uid uint32) ([]byte, error) {
	identity, stat, err := vmJailerFileIdentityForFile(file)
	if err != nil {
		return nil, closeVMJailerFileOnError(file, err)
	}
	if err := validateExistingVMLegacyCompositionMetadata(stat, uid); err != nil {
		return nil, closeVMJailerFileOnError(file, err)
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, vmLegacyProvenanceMaxRecordBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	if len(raw) > vmLegacyProvenanceMaxRecordBytes || int64(len(raw)) != stat.Size {
		return nil, fmt.Errorf("existing legacy VM composition has an invalid or changing size")
	}
	if err := revalidateVMLegacyCompositionEntry(dir, name, identity, stat); err != nil {
		return nil, err
	}
	return raw, nil
}

func validateExistingVMLegacyCompositionMetadata(stat unix.Stat_t, uid uint32) error {
	if uint32(stat.Mode)&unix.S_IFMT != unix.S_IFREG || stat.Uid != uid || uint32(stat.Mode)&0o7777 != 0o600 || stat.Size < 0 || stat.Size > vmLegacyProvenanceMaxRecordBytes {
		return fmt.Errorf("existing legacy VM composition must be a UID %d regular file with permissions 0600 and bounded size", uid)
	}
	return nil
}

func revalidateVMLegacyCompositionEntry(dir *os.File, name string, identity vmJailerFileIdentity, stat unix.Stat_t) error {
	got, gotStat, err := vmJailerNameIdentityAt(dir, name)
	if err != nil {
		return fmt.Errorf("revalidate existing legacy VM composition: %w", err)
	}
	if got != identity || uint32(gotStat.Mode) != uint32(stat.Mode) || gotStat.Uid != stat.Uid || gotStat.Gid != stat.Gid || gotStat.Size != stat.Size {
		return fmt.Errorf("existing legacy VM composition changed while reading")
	}
	return nil
}

func validateVMLegacyCompositionPublishedName(dir *os.File, name string, want vmJailerFileIdentity, uid uint32, raw []byte) error {
	got, stat, err := vmJailerNameIdentityAt(dir, name)
	if err != nil {
		return fmt.Errorf("inspect published legacy VM composition: %w", err)
	}
	if got != want || uint32(stat.Mode)&unix.S_IFMT != unix.S_IFREG || stat.Uid != uid || uint32(stat.Mode)&0o7777 != 0o600 || stat.Size != int64(len(raw)) {
		return fmt.Errorf("published legacy VM composition identity, owner, size, or permissions changed")
	}
	published, err := readVMLegacyCompositionEntry(dir, name, uid)
	if err != nil {
		return err
	}
	if !bytes.Equal(published, raw) {
		return fmt.Errorf("published legacy VM composition content conflicts with its content address")
	}
	return nil
}
