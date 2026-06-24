// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestVMCheckpointConfigHashesIgnoreBalloonLastTarget(t *testing.T) {
	vm := db.VMConfig{
		Runtime:     "firecracker",
		CPUs:        2,
		MemoryBytes: 4 << 30,
		Balloon: db.VMBalloonConfig{
			Mode:                 vmBalloonModeAuto,
			MinBytes:             1 << 30,
			StatsIntervalSeconds: vmBalloonDefaultStatsIntervalSeconds,
			LastTargetBytes:      512 << 20,
		},
		Disk: db.VMDiskConfig{Path: "flash/yeet/vms/devbox/root"},
	}
	baseBalloonHash, baseVMHash, err := vmCheckpointConfigHashes(vm)
	if err != nil {
		t.Fatalf("vmCheckpointConfigHashes: %v", err)
	}

	targetChanged := vm
	targetChanged.Balloon.LastTargetBytes = 2 << 30
	targetBalloonHash, targetVMHash, err := vmCheckpointConfigHashes(targetChanged)
	if err != nil {
		t.Fatalf("vmCheckpointConfigHashes targetChanged: %v", err)
	}
	if targetBalloonHash != baseBalloonHash || targetVMHash != baseVMHash {
		t.Fatalf("hashes changed for runtime target: balloon %q/%q VM %q/%q", baseBalloonHash, targetBalloonHash, baseVMHash, targetVMHash)
	}

	floorChanged := vm
	floorChanged.Balloon.MinBytes = 2 << 30
	floorBalloonHash, floorVMHash, err := vmCheckpointConfigHashes(floorChanged)
	if err != nil {
		t.Fatalf("vmCheckpointConfigHashes floorChanged: %v", err)
	}
	if floorBalloonHash == baseBalloonHash || floorVMHash == baseVMHash {
		t.Fatalf("hashes did not change for static floor: balloon %q/%q VM %q/%q", baseBalloonHash, floorBalloonHash, baseVMHash, floorVMHash)
	}
}
