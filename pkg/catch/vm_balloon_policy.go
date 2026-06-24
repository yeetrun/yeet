// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"fmt"
	"sort"
	"strings"

	"github.com/yeetrun/yeet/pkg/db"
)

const (
	vmBalloonDefaultStatsIntervalSeconds = 5
	vmBalloonDefaultFloorFraction        = 4
)

type vmMemoryPolicy struct {
	Name             string
	RatioNumerator   int64
	RatioDenominator int64
}

type vmMemoryAdmissionInput struct {
	Policy          vmMemoryPolicy
	HostBytes       int64
	RunningMaxBytes int64
	RunningMinBytes int64
	RequestMaxBytes int64
	RequestMinBytes int64
}

type vmBalloonReclaimCandidate struct {
	Service       string
	MaxBytes      int64
	MinBytes      int64
	CurrentTarget int64
	FreeBytes     int64
}

func normalizeVMBalloonMode(raw string) (string, error) {
	mode := strings.TrimSpace(strings.ToLower(raw))
	if mode == "" {
		return vmBalloonModeAuto, nil
	}
	switch mode {
	case vmBalloonModeAuto, vmBalloonModeOff:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported VM balloon mode %q; use auto or off", raw)
	}
}

func normalizeVMHostMemoryPolicy(raw string) (vmMemoryPolicy, error) {
	name := strings.TrimSpace(strings.ToLower(raw))
	if name == "" {
		name = vmHostMemoryPolicySafe
	}
	switch name {
	case vmHostMemoryPolicySafe:
		return vmMemoryPolicy{Name: name, RatioNumerator: 1, RatioDenominator: 1}, nil
	case vmHostMemoryPolicyBalanced:
		return vmMemoryPolicy{Name: name, RatioNumerator: 3, RatioDenominator: 2}, nil
	case vmHostMemoryPolicyAggressive:
		return vmMemoryPolicy{Name: name, RatioNumerator: 2, RatioDenominator: 1}, nil
	default:
		return vmMemoryPolicy{}, fmt.Errorf("unsupported VM memory policy %q; use safe, balanced, or aggressive", raw)
	}
}

func defaultVMBalloonConfig(memoryBytes int64) db.VMBalloonConfig {
	return db.VMBalloonConfig{
		Mode:                 vmBalloonModeAuto,
		MinBytes:             defaultVMBalloonMinBytes(memoryBytes),
		StatsIntervalSeconds: vmBalloonDefaultStatsIntervalSeconds,
	}
}

func effectiveVMBalloonConfig(memoryBytes int64, cfg db.VMBalloonConfig) (db.VMBalloonConfig, error) {
	mode, err := normalizeVMBalloonMode(cfg.Mode)
	if err != nil {
		return db.VMBalloonConfig{}, err
	}
	out := cfg
	out.Mode = mode
	if out.StatsIntervalSeconds <= 0 {
		out.StatsIntervalSeconds = vmBalloonDefaultStatsIntervalSeconds
	}
	if mode == vmBalloonModeOff {
		out.MinBytes = memoryBytes
		out.LastTargetBytes = 0
		return out, nil
	}
	if out.MinBytes == 0 {
		out.MinBytes = defaultVMBalloonMinBytes(memoryBytes)
	}
	if out.MinBytes < 0 {
		return db.VMBalloonConfig{}, fmt.Errorf("VM minimum memory must not be negative")
	}
	if out.MinBytes > memoryBytes {
		return db.VMBalloonConfig{}, fmt.Errorf("VM minimum memory %s exceeds maximum memory %s", formatBytesInt(out.MinBytes), formatBytesInt(memoryBytes))
	}
	return out, nil
}

func effectiveExistingVMBalloonConfig(memoryBytes int64, cfg db.VMBalloonConfig) (db.VMBalloonConfig, error) {
	if !hasPersistedVMBalloonConfig(cfg) {
		return effectiveVMBalloonConfig(memoryBytes, db.VMBalloonConfig{Mode: vmBalloonModeOff})
	}
	return effectiveVMBalloonConfig(memoryBytes, cfg)
}

func hasPersistedVMBalloonConfig(cfg db.VMBalloonConfig) bool {
	return strings.TrimSpace(cfg.Mode) != "" ||
		cfg.MinBytes != 0 ||
		cfg.StatsIntervalSeconds != 0 ||
		cfg.LastTargetBytes != 0
}

func defaultVMBalloonMinBytes(memoryBytes int64) int64 {
	if memoryBytes <= 0 {
		return 0
	}
	floor := memoryBytes / vmBalloonDefaultFloorFraction
	if floor < 512<<20 {
		floor = 512 << 20
	}
	if floor > memoryBytes {
		return memoryBytes
	}
	return floor
}

func vmHostMemoryBudget(hostBytes int64) int64 {
	reserve := vmBalloonHostMemoryReserve(hostBytes)
	budget := hostBytes - reserve
	if budget < 0 {
		return 0
	}
	return budget
}

func vmBalloonHostMemoryReserve(total int64) int64 {
	const gib = int64(1 << 30)
	tenPercent := total / 10
	if tenPercent < 2*gib {
		return 2 * gib
	}
	if tenPercent > 8*gib {
		return 8 * gib
	}
	return tenPercent
}

func admitVMMemoryWithPolicy(input vmMemoryAdmissionInput) error {
	policy, err := normalizeVMHostMemoryPolicy(input.Policy.Name)
	if err != nil {
		return err
	}
	budget := vmHostMemoryBudget(input.HostBytes)
	if budget <= 0 {
		return fmt.Errorf("not enough memory to start VM: host budget is 0")
	}
	committable := budget * policy.RatioNumerator / policy.RatioDenominator
	maxTotal := input.RunningMaxBytes + input.RequestMaxBytes
	minTotal := input.RunningMinBytes + input.RequestMinBytes
	if maxTotal > committable {
		return fmt.Errorf("not enough memory to start VM: requested max commit %s, available max commit %s under %s policy", formatBytesInt(maxTotal), formatBytesInt(committable), policy.Name)
	}
	if minTotal > budget {
		return fmt.Errorf("not enough memory to start VM: requested minimum commit %s, available budget %s", formatBytesInt(minTotal), formatBytesInt(budget))
	}
	return nil
}

func vmBalloonTargetForPressure(maxBytes, minBytes, desiredReclaimBytes int64) int64 {
	limit := maxBytes - minBytes
	if limit <= 0 || desiredReclaimBytes <= 0 {
		return 0
	}
	if desiredReclaimBytes > limit {
		return limit
	}
	return desiredReclaimBytes
}

func sortVMBalloonReclaimCandidates(candidates []vmBalloonReclaimCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		iHeadroom := candidates[i].MaxBytes - candidates[i].MinBytes - candidates[i].CurrentTarget
		jHeadroom := candidates[j].MaxBytes - candidates[j].MinBytes - candidates[j].CurrentTarget
		if candidates[i].FreeBytes != candidates[j].FreeBytes {
			return candidates[i].FreeBytes > candidates[j].FreeBytes
		}
		if iHeadroom != jHeadroom {
			return iHeadroom > jHeadroom
		}
		if candidates[i].MaxBytes != candidates[j].MaxBytes {
			return candidates[i].MaxBytes > candidates[j].MaxBytes
		}
		return candidates[i].Service < candidates[j].Service
	})
}
