// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
)

func TestVMBalloonControllerPlansNoTargetsWhenMemoryHealthy(t *testing.T) {
	plan := planVMBalloonTargets(vmBalloonControllerInput{
		HostBytes:         16 << 30,
		MemAvailable:      8 << 30,
		CurrentPolicyName: vmHostMemoryPolicyBalanced,
		Candidates: []vmBalloonReclaimCandidate{
			{Service: "a", MaxBytes: 4 << 30, MinBytes: 1 << 30, FreeBytes: 2 << 30},
		},
	})
	if len(plan.Targets) != 0 {
		t.Fatalf("targets = %#v, want none", plan.Targets)
	}
}

func TestVMBalloonControllerReclaimsUnderPressureWithoutPassingFloor(t *testing.T) {
	plan := planVMBalloonTargets(vmBalloonControllerInput{
		HostBytes:         16 << 30,
		MemAvailable:      1 << 30,
		CurrentPolicyName: vmHostMemoryPolicyBalanced,
		Candidates: []vmBalloonReclaimCandidate{
			{Service: "a", MaxBytes: 4 << 30, MinBytes: 1 << 30, FreeBytes: 3 << 30},
		},
	})
	if len(plan.Targets) != 1 {
		t.Fatalf("targets = %#v, want one target", plan.Targets)
	}
	if plan.Targets["a"] > 3<<30 {
		t.Fatalf("target = %d, exceeds max-floor", plan.Targets["a"])
	}
}

func TestVMBalloonControllerCapsReclaimAtGuestFreeBytes(t *testing.T) {
	plan := planVMBalloonTargets(vmBalloonControllerInput{
		HostBytes:         16 << 30,
		MemAvailable:      512 << 20,
		CurrentPolicyName: vmHostMemoryPolicyBalanced,
		Candidates: []vmBalloonReclaimCandidate{
			{Service: "a", MaxBytes: 4 << 30, MinBytes: 1 << 30, FreeBytes: 256 << 20},
		},
	})
	if got := plan.Targets["a"]; got != 256<<20 {
		t.Fatalf("target = %d, want guest free cap %d", got, int64(256<<20))
	}
}

func TestVMBalloonControllerDoesNotReclaimWhenGuestReportsNoFreeMemory(t *testing.T) {
	plan := planVMBalloonTargets(vmBalloonControllerInput{
		HostBytes:         16 << 30,
		MemAvailable:      512 << 20,
		CurrentPolicyName: vmHostMemoryPolicyBalanced,
		Candidates: []vmBalloonReclaimCandidate{
			{Service: "a", MaxBytes: 4 << 30, MinBytes: 1 << 30, FreeBytes: 0},
		},
	})
	if len(plan.Targets) != 0 {
		t.Fatalf("targets = %#v, want none when guest reports no free memory", plan.Targets)
	}
}

func TestVMBalloonControllerReconcileSetsTargetsAndPersistsLastTarget(t *testing.T) {
	server := newTestServer(t)
	socketPath := "/run/firecracker/devbox.sock"
	if _, err := server.cfg.DB.MutateData(func(d *db.Data) error {
		d.Services = map[string]*db.Service{
			"devbox": {
				Name:        "devbox",
				ServiceType: db.ServiceTypeVM,
				VM: &db.VMConfig{
					MemoryBytes: 4 << 30,
					Balloon: db.VMBalloonConfig{
						Mode:                 vmBalloonModeAuto,
						MinBytes:             1 << 30,
						StatsIntervalSeconds: vmBalloonDefaultStatsIntervalSeconds,
					},
					Sockets: db.VMSocketConfig{APISocketPath: socketPath},
				},
			},
		}
		return nil
	}); err != nil {
		t.Fatalf("seed db: %v", err)
	}
	oldVMStatus := serverVMStatusFunc
	serverVMStatusFunc = func(name string) (svc.Status, error) {
		if name == "devbox" {
			return svc.StatusRunning, nil
		}
		return svc.StatusStopped, nil
	}
	t.Cleanup(func() { serverVMStatusFunc = oldVMStatus })
	api := &recordingVMBalloonAPI{
		stats: map[string]vmBalloonStats{
			socketPath: {FreeMemoryBytes: 3 << 30},
		},
	}
	oldMemTotal := linuxMemTotalBytesFunc
	oldMemAvailable := linuxMemAvailableBytesFunc
	linuxMemTotalBytesFunc = func() int64 { return 16 << 30 }
	linuxMemAvailableBytesFunc = func() int64 { return 1 << 30 }
	t.Cleanup(func() {
		linuxMemTotalBytesFunc = oldMemTotal
		linuxMemAvailableBytesFunc = oldMemAvailable
	})

	if err := server.reconcileVMBalloons(context.Background(), api); err != nil {
		t.Fatalf("reconcileVMBalloons: %v", err)
	}
	if len(api.targets) != 1 || api.targets[socketPath] == 0 {
		t.Fatalf("targets = %#v, want one non-zero target", api.targets)
	}
	dv, err := server.getDB()
	if err != nil {
		t.Fatalf("getDB: %v", err)
	}
	got := dv.Services().Get("devbox").VM().Balloon().LastTargetBytes
	if got != api.targets[socketPath] {
		t.Fatalf("LastTargetBytes = %d, want %d", got, api.targets[socketPath])
	}
}

type recordingVMBalloonAPI struct {
	stats   map[string]vmBalloonStats
	targets map[string]int64
}

func (a *recordingVMBalloonAPI) SetTarget(_ context.Context, socket string, targetBytes int64) error {
	if a.targets == nil {
		a.targets = map[string]int64{}
	}
	a.targets[socket] = targetBytes
	return nil
}

func (a *recordingVMBalloonAPI) Stats(_ context.Context, socket string) (vmBalloonStats, error) {
	return a.stats[socket], nil
}
