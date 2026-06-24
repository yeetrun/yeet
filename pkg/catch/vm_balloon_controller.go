// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
)

type vmBalloonControllerInput struct {
	HostBytes         int64
	MemAvailable      int64
	CurrentPolicyName string
	Candidates        []vmBalloonReclaimCandidate
}

type vmBalloonControllerPlan struct {
	Targets map[string]int64
}

func planVMBalloonTargets(input vmBalloonControllerInput) vmBalloonControllerPlan {
	reserve := vmHostMemoryReserve(input.HostBytes)
	startReclaim := reserve + input.HostBytes/10
	stopReclaim := reserve + (input.HostBytes*15)/100
	if input.MemAvailable >= startReclaim {
		return vmBalloonControllerPlan{Targets: nil}
	}
	desired := stopReclaim - input.MemAvailable
	if desired <= 0 {
		return vmBalloonControllerPlan{Targets: nil}
	}
	candidates := append([]vmBalloonReclaimCandidate(nil), input.Candidates...)
	sortVMBalloonReclaimCandidates(candidates)
	targets := map[string]int64{}
	remaining := desired
	for _, candidate := range candidates {
		headroom := candidate.MaxBytes - candidate.MinBytes - candidate.CurrentTarget
		if headroom <= 0 {
			continue
		}
		if candidate.FreeBytes <= 0 {
			continue
		}
		reclaim := headroom
		if candidate.FreeBytes < reclaim {
			reclaim = candidate.FreeBytes
		}
		if reclaim > remaining {
			reclaim = remaining
		}
		if reclaim <= 0 {
			continue
		}
		targets[candidate.Service] = candidate.CurrentTarget + reclaim
		remaining -= reclaim
		if remaining <= 0 {
			break
		}
	}
	return vmBalloonControllerPlan{Targets: targets}
}

func (s *Server) runVMBalloonController(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.reconcileVMBalloons(ctx, firecrackerBalloonAPI{}); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("VM balloon reconciliation failed: %v", err)
			}
		}
	}
}

type vmBalloonReconcileCandidate struct {
	service       string
	socket        string
	currentTarget int64
}

func (s *Server) reconcileVMBalloons(ctx context.Context, api vmBalloonAPI) error {
	dv, err := s.getDB()
	if err != nil {
		return err
	}
	policy, err := s.vmHostMemoryPolicy()
	if err != nil {
		return err
	}
	candidates, reconcileCandidates, errs := s.vmBalloonControllerCandidates(ctx, dv, api)
	hostBytes, memAvailable, memoryErr := currentVMBalloonHostMemory()
	if memoryErr != nil {
		errs = append(errs, memoryErr)
		return errors.Join(errs...)
	}
	plan := planVMBalloonTargets(vmBalloonControllerInput{
		HostBytes:         hostBytes,
		MemAvailable:      memAvailable,
		CurrentPolicyName: policy.Name,
		Candidates:        candidates,
	})
	errs = append(errs, s.applyVMBalloonTargets(ctx, api, plan, reconcileCandidates)...)
	return errors.Join(errs...)
}

func (s *Server) vmBalloonControllerCandidates(ctx context.Context, dv *db.DataView, api vmBalloonAPI) ([]vmBalloonReclaimCandidate, map[string]vmBalloonReconcileCandidate, []error) {
	candidates := []vmBalloonReclaimCandidate{}
	reconcileCandidates := map[string]vmBalloonReconcileCandidate{}
	var errs []error
	for name, serviceView := range dv.Services().All() {
		candidate, reconcileCandidate, ok, err := s.vmBalloonControllerCandidate(ctx, name, serviceView.AsStruct(), api)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !ok {
			continue
		}
		candidates = append(candidates, candidate)
		reconcileCandidates[name] = reconcileCandidate
	}
	return candidates, reconcileCandidates, errs
}

func (s *Server) vmBalloonControllerCandidate(ctx context.Context, name string, service *db.Service, api vmBalloonAPI) (vmBalloonReclaimCandidate, vmBalloonReconcileCandidate, bool, error) {
	if service == nil || service.ServiceType != db.ServiceTypeVM || service.VM == nil {
		return vmBalloonReclaimCandidate{}, vmBalloonReconcileCandidate{}, false, nil
	}
	vm := service.VM
	balloon, err := effectiveExistingVMBalloonConfig(vm.MemoryBytes, vm.Balloon)
	if err != nil {
		return vmBalloonReclaimCandidate{}, vmBalloonReconcileCandidate{}, false, fmt.Errorf("VM %q balloon config: %w", name, err)
	}
	if balloon.Mode == vmBalloonModeOff {
		return vmBalloonReclaimCandidate{}, vmBalloonReconcileCandidate{}, false, nil
	}
	socket := strings.TrimSpace(vm.Sockets.APISocketPath)
	if socket == "" {
		return vmBalloonReclaimCandidate{}, vmBalloonReconcileCandidate{}, false, nil
	}
	running, err := s.isServiceTypeRunning(name, service.ServiceType)
	if err != nil {
		return vmBalloonReclaimCandidate{}, vmBalloonReconcileCandidate{}, false, fmt.Errorf("VM %q status: %w", name, err)
	}
	if !running {
		return vmBalloonReclaimCandidate{}, vmBalloonReconcileCandidate{}, false, nil
	}
	stats, err := api.Stats(ctx, socket)
	if err != nil {
		return vmBalloonReclaimCandidate{}, vmBalloonReconcileCandidate{}, false, fmt.Errorf("VM %q balloon stats: %w", name, err)
	}
	return vmBalloonReclaimCandidate{
			Service:       name,
			MaxBytes:      vm.MemoryBytes,
			MinBytes:      balloon.MinBytes,
			CurrentTarget: stats.TargetBytes,
			FreeBytes:     stats.FreeMemoryBytes,
		}, vmBalloonReconcileCandidate{
			service:       name,
			socket:        socket,
			currentTarget: stats.TargetBytes,
		}, true, nil
}

func currentVMBalloonHostMemory() (int64, int64, error) {
	hostBytes := linuxMemTotalBytes()
	memAvailable := linuxMemAvailableBytes()
	var errs []error
	if hostBytes <= 0 {
		errs = append(errs, fmt.Errorf("host MemTotal unavailable"))
	}
	if memAvailable <= 0 {
		errs = append(errs, fmt.Errorf("host MemAvailable unavailable"))
	}
	return hostBytes, memAvailable, errors.Join(errs...)
}

func (s *Server) applyVMBalloonTargets(ctx context.Context, api vmBalloonAPI, plan vmBalloonControllerPlan, candidates map[string]vmBalloonReconcileCandidate) []error {
	var errs []error
	for service, target := range plan.Targets {
		candidate, ok := candidates[service]
		if !ok || target == candidate.currentTarget {
			continue
		}
		if err := api.SetTarget(ctx, candidate.socket, target); err != nil {
			errs = append(errs, fmt.Errorf("VM %q set balloon target: %w", service, err))
			continue
		}
		if err := s.persistVMBalloonLastTarget(service, target); err != nil {
			errs = append(errs, fmt.Errorf("VM %q persist balloon target: %w", service, err))
		}
	}
	return errs
}

func (s *Server) persistVMBalloonLastTarget(service string, targetBytes int64) error {
	_, err := s.cfg.DB.MutateData(func(d *db.Data) error {
		if d.Services == nil {
			return nil
		}
		svc := d.Services[service]
		if svc == nil || svc.VM == nil {
			return nil
		}
		svc.VM.Balloon.LastTargetBytes = targetBytes
		return nil
	})
	return err
}
