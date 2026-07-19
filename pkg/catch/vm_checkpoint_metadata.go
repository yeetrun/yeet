// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
)

var (
	vmCheckpointFirecrackerPathFunc    = vmCheckpointFirecrackerPathFromSystemd
	vmCheckpointFirecrackerVersionFunc = firecrackerBinaryVersion
)

const (
	firecrackerVersionProbeTimeout   = 2 * time.Second
	firecrackerVersionProbeWaitDelay = 500 * time.Millisecond
)

type vmCheckpointMetadata struct {
	Service            string `json:"service"`
	Comment            string `json:"comment,omitempty"`
	ZVOLSnapshot       string `json:"zvolSnapshot"`
	FirecrackerState   string `json:"firecrackerState"`
	FirecrackerMemory  string `json:"firecrackerMemory"`
	CreatedBy          string `json:"createdBy"`
	CreatedAt          string `json:"createdAt"`
	Mode               string `json:"mode,omitempty"`
	FirecrackerVersion string `json:"firecrackerVersion,omitempty"`
	FirecrackerSha256  string `json:"firecrackerSha256,omitempty"`
	MachineConfigHash  string `json:"machineConfigHash,omitempty"`
	NetworkConfigHash  string `json:"networkConfigHash,omitempty"`
	BalloonConfigHash  string `json:"balloonConfigHash,omitempty"`
	DiskPath           string `json:"diskPath,omitempty"`
	VCPU               int    `json:"vcpu,omitempty"`
	MemoryMiB          int    `json:"memoryMiB,omitempty"`
	VMConfigHash       string `json:"vmConfigHash,omitempty"`
}

type vmCheckpointCompatibility struct {
	FirecrackerVersion string
	FirecrackerSha256  string
	MachineConfigHash  string
	NetworkConfigHash  string
	BalloonConfigHash  string
	DiskPath           string
	VCPU               int
	MemoryMiB          int
	VMConfigHash       string
}

func (s *Server) marshalVMCheckpointMetadataWithCompatibility(service *db.Service, compat vmCheckpointCompatibility, comment string, zvolSnapshot string, result vmSnapshotResult, created time.Time) ([]byte, error) {
	metadata := vmCheckpointMetadata{
		Service:            service.Name,
		Comment:            strings.TrimSpace(comment),
		ZVOLSnapshot:       zvolSnapshot,
		FirecrackerState:   result.StatePath,
		FirecrackerMemory:  result.MemoryPath,
		CreatedBy:          "catch",
		CreatedAt:          created.UTC().Format(time.RFC3339Nano),
		Mode:               recoveryModeFull,
		FirecrackerVersion: compat.FirecrackerVersion,
		FirecrackerSha256:  compat.FirecrackerSha256,
		MachineConfigHash:  compat.MachineConfigHash,
		NetworkConfigHash:  compat.NetworkConfigHash,
		BalloonConfigHash:  compat.BalloonConfigHash,
		DiskPath:           compat.DiskPath,
		VCPU:               compat.VCPU,
		MemoryMiB:          compat.MemoryMiB,
		VMConfigHash:       compat.VMConfigHash,
	}
	raw, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return nil, err
	}
	raw = append(raw, '\n')
	return raw, nil
}

func (s *Server) vmCheckpointCompatibility(service *db.Service, vm db.VMConfig) (vmCheckpointCompatibility, error) {
	if service == nil {
		return vmCheckpointCompatibility{}, fmt.Errorf("VM checkpoint service is required")
	}
	memoryMiB := int(vm.MemoryBytes >> 20)
	machine, network, err := s.vmCheckpointFirecrackerCompatibility(service, vm, memoryMiB)
	if err != nil {
		return vmCheckpointCompatibility{}, err
	}
	machineHash, err := canonicalJSONSHA256(machine)
	if err != nil {
		return vmCheckpointCompatibility{}, err
	}
	networkHash, err := canonicalJSONSHA256(network)
	if err != nil {
		return vmCheckpointCompatibility{}, err
	}
	balloonHash, vmHash, err := vmCheckpointConfigHashes(vm)
	if err != nil {
		return vmCheckpointCompatibility{}, err
	}
	compat := vmCheckpointCompatibility{
		MachineConfigHash: machineHash,
		NetworkConfigHash: networkHash,
		BalloonConfigHash: balloonHash,
		DiskPath:          strings.TrimSpace(vm.Disk.Path),
		VCPU:              vm.CPUs,
		MemoryMiB:         memoryMiB,
		VMConfigHash:      vmHash,
	}
	if firecrackerPath := strings.TrimSpace(vmCheckpointFirecrackerPathFunc(service, vm)); firecrackerPath != "" {
		sha, err := fileSHA256(firecrackerPath)
		if err != nil {
			return vmCheckpointCompatibility{}, fmt.Errorf("hash Firecracker binary %s: %w", firecrackerPath, err)
		}
		version, err := vmCheckpointFirecrackerVersionFunc(firecrackerPath)
		if err != nil {
			return vmCheckpointCompatibility{}, err
		}
		compat.FirecrackerSha256 = sha
		compat.FirecrackerVersion = version
	}
	return compat, nil
}

func (s *Server) vmCheckpointFirecrackerCompatibility(service *db.Service, vm db.VMConfig, memoryMiB int) (firecrackerMachineConfig, []firecrackerNetworkInterface, error) {
	machine := firecrackerMachineConfig{VCPUCount: vm.CPUs, MemSizeMib: memoryMiB}
	network := vmNetworkPlanFromDB(service.Name, vm.Networks).FirecrackerInterfaces()
	cfg, ok, err := readVMCheckpointFirecrackerConfig(filepath.Join(serviceRunDirForRoot(s.serviceRootFromService(service)), "firecracker.json"))
	if err != nil {
		return firecrackerMachineConfig{}, nil, err
	}
	if ok {
		machine = cfg.MachineConfig
		network = cfg.NetworkInterfaces
	}
	return machine, network, nil
}

func vmCheckpointConfigHashes(vm db.VMConfig) (string, string, error) {
	balloon, err := effectiveExistingVMBalloonConfig(vm.MemoryBytes, vm.Balloon)
	if err != nil {
		return "", "", fmt.Errorf("VM balloon config: %w", err)
	}
	balloon.LastTargetBytes = 0
	balloonHash, err := canonicalJSONSHA256(balloon)
	if err != nil {
		return "", "", err
	}
	vm.Balloon = balloon
	vmHash, err := canonicalJSONSHA256(vm)
	if err != nil {
		return "", "", err
	}
	return balloonHash, vmHash, nil
}

func readVMCheckpointFirecrackerConfig(path string) (firecrackerConfig, bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return firecrackerConfig{}, false, nil
	}
	if err != nil {
		return firecrackerConfig{}, false, fmt.Errorf("read Firecracker config %s: %w", path, err)
	}
	var cfg firecrackerConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return firecrackerConfig{}, false, fmt.Errorf("decode Firecracker config %s: %w", path, err)
	}
	return cfg, true, nil
}

func canonicalJSONSHA256(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func fileSHA256(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func firecrackerBinaryVersion(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), firecrackerVersionProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.WaitDelay = firecrackerVersionProbeWaitDelay
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return "", fmt.Errorf("read Firecracker version from %s: %w", path, ctx.Err())
	}
	if err != nil {
		return "", fmt.Errorf("read Firecracker version from %s: %w: %s", path, err, strings.TrimSpace(string(out)))
	}
	version := stableFirecrackerVersionLine(string(out))
	if version == "" {
		return "", fmt.Errorf("read Firecracker version from %s: empty output", path)
	}
	return version, nil
}

func stableFirecrackerVersionLine(output string) string {
	var firstNonEmpty string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if firstNonEmpty == "" {
			firstNonEmpty = line
		}
		if strings.HasPrefix(line, "Firecracker ") {
			return line
		}
	}
	return firstNonEmpty
}

// Empty means the current launcher identity cannot be discovered from the
// installed yeet VM systemd unit, so identity comparison is skipped until a
// future launcher can provide a safer source of truth.
func vmCheckpointFirecrackerPathFromSystemd(service *db.Service, _ db.VMConfig) string {
	if service == nil || strings.TrimSpace(service.Name) == "" {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(vmSystemdSystemDir, vmSystemdUnitName(service.Name)))
	if err != nil {
		return ""
	}
	return firecrackerPathFromVMSystemdUnit(string(raw))
}

func firecrackerPathFromVMSystemdUnit(unit string) string {
	for _, line := range strings.Split(unit, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ExecStart=") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "ExecStart="))
		for i, field := range fields {
			if field == "--firecracker" && i+1 < len(fields) {
				return fields[i+1]
			}
		}
	}
	return ""
}

func (m vmCheckpointMetadata) hasFullCompatibilityFields() bool {
	return len(m.missingFullCompatibilityFields()) == 0
}

type vmCheckpointCompatibilityField struct {
	Name    string
	Missing bool
}

func (m vmCheckpointMetadata) missingFullCompatibilityFields() []string {
	fields := []vmCheckpointCompatibilityField{
		{Name: "mode", Missing: strings.TrimSpace(m.Mode) != recoveryModeFull},
		{Name: "firecrackerState", Missing: strings.TrimSpace(m.FirecrackerState) == ""},
		{Name: "firecrackerMemory", Missing: strings.TrimSpace(m.FirecrackerMemory) == ""},
		{Name: "machineConfigHash", Missing: strings.TrimSpace(m.MachineConfigHash) == ""},
		{Name: "networkConfigHash", Missing: strings.TrimSpace(m.NetworkConfigHash) == ""},
		{Name: "balloonConfigHash", Missing: strings.TrimSpace(m.BalloonConfigHash) == ""},
		{Name: "diskPath", Missing: strings.TrimSpace(m.DiskPath) == ""},
		{Name: "vcpu", Missing: m.VCPU <= 0},
		{Name: "memoryMiB", Missing: m.MemoryMiB <= 0},
		{Name: "vmConfigHash", Missing: strings.TrimSpace(m.VMConfigHash) == ""},
	}
	return missingVMCheckpointCompatibilityFields(fields)
}

func missingVMCheckpointCompatibilityFields(fields []vmCheckpointCompatibilityField) []string {
	var missing []string
	for _, field := range fields {
		if field.Missing {
			missing = append(missing, field.Name)
		}
	}
	return missing
}
