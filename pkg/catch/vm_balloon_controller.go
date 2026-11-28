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

const (
	vmBalloonPollInterval = 5 * time.Second
	vmBalloonStallTimeout = 30 * time.Second
	vmBalloonRetryDelay   = time.Minute
	vmBalloonTargetUnit   = 1 << 20 // Firecracker accepts targets in whole MiB.
	vmBalloonReturnStep   = 128 * vmBalloonTargetUnit
)

type vmBalloonControllerInput struct {
	HostBytes    int64
	MemAvailable int64
	Candidates   []vmBalloonReclaimCandidate
}

func planVMBalloonTargets(input vmBalloonControllerInput) map[string]int64 {
	reserve := vmHostMemoryReserve(input.HostBytes)
	startReclaim := reserve + input.HostBytes/10
	startReturn := reserve + (input.HostBytes*15)/100
	candidates := append([]vmBalloonReclaimCandidate(nil), input.Candidates...)
	sortVMBalloonReclaimCandidates(candidates)
	targets := map[string]int64{}
	var inflating, deflating int64
	for _, candidate := range candidates {
		delta := candidate.CurrentTarget - candidate.ActualBytes
		switch {
		case candidate.Stalled:
			// Cancel the unfulfilled part of the target so the guest stops retrying.
			targets[candidate.Service] = roundVMBalloonTarget(candidate.ActualBytes)
		case delta > 0:
			inflating += delta
		case delta < 0:
			deflating -= delta
		}
	}
	switch {
	case input.MemAvailable < startReclaim:
		planVMBalloonInflation(candidates, targets, startReturn-input.MemAvailable-inflating)
	case input.MemAvailable >= startReturn:
		// Keep a gap between the thresholds and cap the total returned per poll.
		budget := min(input.MemAvailable-startReturn, vmBalloonReturnStep) - deflating
		planVMBalloonDeflation(candidates, targets, budget)
	}
	return targets
}

func planVMBalloonInflation(candidates []vmBalloonReclaimCandidate, targets map[string]int64, remaining int64) {
	for _, candidate := range candidates {
		if remaining < vmBalloonTargetUnit {
			break
		}
		if candidate.BackingOff || candidate.CurrentTarget != candidate.ActualBytes {
			continue
		}
		headroom := candidate.MaxBytes - candidate.MinBytes - candidate.CurrentTarget
		reclaim := roundVMBalloonTarget(min(headroom, candidate.FreeBytes, remaining))
		if reclaim == 0 {
			continue
		}
		targets[candidate.Service] = candidate.CurrentTarget + reclaim
		remaining -= reclaim
	}
}

func planVMBalloonDeflation(candidates []vmBalloonReclaimCandidate, targets map[string]int64, remaining int64) {
	// The inflation order starts with the most free memory. Return it to the
	// guests with the least free memory first.
	for i := len(candidates) - 1; i >= 0; i-- {
		candidate := candidates[i]
		// Cancel outstanding inflation as soon as host memory recovers.
		held := roundVMBalloonTarget(min(candidate.CurrentTarget, candidate.ActualBytes))
		release := roundVMBalloonTarget(min(held, remaining))
		if target := held - release; target < candidate.CurrentTarget {
			targets[candidate.Service] = target
		}
		remaining -= release
	}
}

func roundVMBalloonTarget(bytes int64) int64 {
	return max(0, bytes) / vmBalloonTargetUnit * vmBalloonTargetUnit
}

type vmBalloonObservation struct {
	socket       string
	target       int64
	highWater    int64
	lastProgress time.Time
	retryAfter   time.Time
}

func (o vmBalloonObservation) observe(now time.Time, candidate *vmBalloonReclaimCandidate) vmBalloonObservation {
	if o.socket != candidate.Socket {
		o = vmBalloonObservation{socket: candidate.Socket}
	}
	if o.lastProgress.IsZero() || o.target != candidate.CurrentTarget ||
		candidate.ActualBytes >= candidate.CurrentTarget || candidate.ActualBytes-o.highWater >= vmBalloonTargetUnit {
		o.target = candidate.CurrentTarget
		o.highWater = candidate.ActualBytes
		o.lastProgress = now
	}
	candidate.Stalled = candidate.ActualBytes < candidate.CurrentTarget && now.Sub(o.lastProgress) >= vmBalloonStallTimeout
	if candidate.Stalled {
		o.retryAfter = now.Add(vmBalloonRetryDelay)
	}
	candidate.BackingOff = now.Before(o.retryAfter)
	return o
}

func (s *Server) runVMBalloonController(ctx context.Context) {
	ticker := time.NewTicker(vmBalloonPollInterval)
	defer ticker.Stop()
	observations := make(map[string]vmBalloonObservation)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.reconcileVMBalloons(ctx, firecrackerBalloonAPI{}, time.Now(), observations); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("VM balloon reconciliation failed: %v", err)
			}
		}
	}
}

func (s *Server) reconcileVMBalloons(ctx context.Context, api vmBalloonAPI, now time.Time, observations map[string]vmBalloonObservation) error {
	if err := s.checkTailscaleResolverMutationAllowed(); err != nil {
		return err
	}
	dv, err := s.getDB()
	if err != nil {
		return err
	}
	if _, err := s.vmHostMemoryPolicy(); err != nil {
		return err
	}
	candidates, errs := s.vmBalloonControllerCandidates(ctx, dv, api, now, observations)
	hostBytes, memAvailable, memoryErr := currentVMBalloonHostMemory()
	if memoryErr != nil {
		errs = append(errs, memoryErr)
		return errors.Join(errs...)
	}
	targets := planVMBalloonTargets(vmBalloonControllerInput{
		HostBytes:    hostBytes,
		MemAvailable: memAvailable,
		Candidates:   candidates,
	})
	errs = append(errs, s.applyVMBalloonTargets(ctx, api, targets, candidates)...)
	return errors.Join(errs...)
}

func (s *Server) vmBalloonControllerCandidates(ctx context.Context, dv *db.DataView, api vmBalloonAPI, now time.Time, observations map[string]vmBalloonObservation) ([]vmBalloonReclaimCandidate, []error) {
	var candidates []vmBalloonReclaimCandidate
	var errs []error
	for name, serviceView := range dv.Services().All() {
		candidate, ok, err := s.vmBalloonControllerCandidate(ctx, name, serviceView.AsStruct(), api)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !ok {
			delete(observations, name)
			continue
		}
		observations[name] = observations[name].observe(now, &candidate)
		candidates = append(candidates, candidate)
	}
	for name := range observations {
		if !dv.Services().Get(name).Valid() {
			delete(observations, name)
		}
	}
	return candidates, errs
}

func (s *Server) vmBalloonControllerCandidate(ctx context.Context, name string, service *db.Service, api vmBalloonAPI) (vmBalloonReclaimCandidate, bool, error) {
	if service == nil || service.ServiceType != db.ServiceTypeVM || service.VM == nil {
		return vmBalloonReclaimCandidate{}, false, nil
	}
	vm := service.VM
	balloon, err := effectiveExistingVMBalloonConfig(vm.MemoryBytes, vm.Balloon)
	if err != nil {
		return vmBalloonReclaimCandidate{}, false, fmt.Errorf("VM %q balloon config: %w", name, err)
	}
	if balloon.Mode == vmBalloonModeOff {
		return vmBalloonReclaimCandidate{}, false, nil
	}
	socket := strings.TrimSpace(vm.Sockets.APISocketPath)
	if socket == "" {
		return vmBalloonReclaimCandidate{}, false, nil
	}
	running, err := s.isServiceTypeRunning(name, service.ServiceType)
	if err != nil {
		return vmBalloonReclaimCandidate{}, false, fmt.Errorf("VM %q status: %w", name, err)
	}
	if !running {
		return vmBalloonReclaimCandidate{}, false, nil
	}
	stats, err := api.Stats(ctx, socket)
	if err != nil {
		return vmBalloonReclaimCandidate{}, false, fmt.Errorf("VM %q balloon stats: %w", name, err)
	}
	return vmBalloonReclaimCandidate{
		Service:       name,
		Socket:        socket,
		MaxBytes:      vm.MemoryBytes,
		MinBytes:      balloon.MinBytes,
		CurrentTarget: stats.TargetBytes,
		ActualBytes:   stats.ActualBytes,
		FreeBytes:     stats.FreeMemoryBytes,
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

func (s *Server) applyVMBalloonTargets(ctx context.Context, api vmBalloonAPI, targets map[string]int64, candidates []vmBalloonReclaimCandidate) []error {
	var errs []error
	for _, candidate := range candidates {
		service := candidate.Service
		target, ok := targets[service]
		if !ok || target == candidate.CurrentTarget {
			continue
		}
		if err := s.checkTailscaleResolverMutationAllowed(); err != nil {
			errs = append(errs, err)
			break
		}
		if err := api.SetTarget(ctx, candidate.Socket, target); err != nil {
			errs = append(errs, fmt.Errorf("VM %q set balloon target: %w", service, err))
			continue
		}
		if err := s.checkTailscaleResolverMutationAllowed(); err != nil {
			errs = append(errs, err)
			break
		}
		if err := s.persistVMBalloonLastTarget(service, target); err != nil {
			errs = append(errs, fmt.Errorf("VM %q persist balloon target: %w", service, err))
		}
	}
	return errs
}

func (s *Server) persistVMBalloonLastTarget(service string, targetBytes int64) error {
	if err := s.checkTailscaleResolverMutationAllowed(); err != nil {
		return err
	}
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
