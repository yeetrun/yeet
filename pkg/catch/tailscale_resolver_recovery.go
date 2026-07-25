// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/serviceid"
	"golang.org/x/sys/unix"
)

var errTailscaleResolverRecoveryBlocked = errors.New(
	"tailscale resolver recovery is incomplete; service mutations are blocked",
)

type tailscaleResolverRecoveryState struct {
	mu             sync.RWMutex
	block          error
	afterBlockLock func()
}

func (s *Server) recoverTailscaleResolverIsolation(ctx context.Context) error {
	paths, discoveryErr := discoverTailscaleResolverJournals(s.cfg.RootDir)
	if len(paths) == 0 {
		if discoveryErr != nil {
			return s.blockTailscaleResolverRecovery(discoveryErr)
		}
		s.clearTailscaleResolverMutationBlock()
		return nil
	}

	services, names, scanErr := scanTailscaleResolverRecoveryJournals(paths)
	scanErr = errors.Join(discoveryErr, scanErr)
	release := s.serviceOperationLocks.Lock(names...)
	defer release()

	if len(paths) != 1 || scanErr != nil {
		stopErr := s.stopTailscaleResolverJournalServices(services)
		return s.blockTailscaleResolverRecovery(errors.Join(
			scanErr,
			stopErr,
			fmt.Errorf(
				"found %d tailscale resolver journals; recovery requires exactly one valid journal",
				len(paths),
			),
		))
	}

	path := paths[0]
	contents, err := loadTailscaleResolverJournal(path)
	if err != nil {
		s.stopSalvageableTailscaleResolverJournalServices(path)
		return s.blockTailscaleResolverRecovery(err)
	}
	if err := s.validateTailscaleResolverRecoveryRecords(contents.Header.Services); err != nil {
		stopErr := s.stopTailscaleResolverJournalServices(contents.Header.Services)
		return s.blockTailscaleResolverRecovery(errors.Join(err, stopErr))
	}
	if tailscaleResolverJournalCommitted(contents) {
		if err := removeTailscaleResolverJournal(path); err != nil {
			return s.blockTailscaleResolverRecovery(err)
		}
		s.clearTailscaleResolverMutationBlock()
		return nil
	}
	if err := s.rollbackTailscaleResolverJournal(
		context.WithoutCancel(ctx),
		path,
		contents.Header,
	); err != nil {
		return s.blockTailscaleResolverRecovery(err)
	}
	s.clearTailscaleResolverMutationBlock()
	return nil
}

func scanTailscaleResolverRecoveryJournals(
	paths []string,
) ([]tailscaleResolverJournalService, []string, error) {
	servicesByUnit := make(map[string]tailscaleResolverJournalService)
	names := make(map[string]struct{})
	var scanErrs []error
	for _, path := range paths {
		contents, err := loadTailscaleResolverJournal(path)
		services := contents.Header.Services
		if err != nil {
			scanErrs = append(scanErrs, fmt.Errorf("load %s: %w", path, err))
			services, err = salvageTailscaleResolverJournalServices(path)
			if err != nil {
				scanErrs = append(scanErrs, fmt.Errorf("salvage %s: %w", path, err))
				continue
			}
		}
		for _, service := range services {
			servicesByUnit[service.UnitName] = service
			names[service.ServiceName] = struct{}{}
		}
	}
	sortedServices := make([]tailscaleResolverJournalService, 0, len(servicesByUnit))
	for _, service := range servicesByUnit {
		sortedServices = append(sortedServices, service)
	}
	sort.Slice(sortedServices, func(i, j int) bool {
		return sortedServices[i].ServiceName < sortedServices[j].ServiceName
	})
	sortedNames := make([]string, 0, len(names))
	for name := range names {
		sortedNames = append(sortedNames, name)
	}
	sort.Strings(sortedNames)
	return sortedServices, sortedNames, errors.Join(scanErrs...)
}

func (s *Server) validateTailscaleResolverRecoveryRecords(
	services []tailscaleResolverJournalService,
) error {
	return s.cfg.DB.WithLatestDataLocked(func(latest db.DataView) error {
		for _, journalService := range services {
			serviceView, ok := latest.Services().GetOk(journalService.ServiceName)
			if !ok {
				return fmt.Errorf(
					"tailscale resolver database record for %q is missing",
					journalService.ServiceName,
				)
			}
			service := *serviceView.AsStruct()
			service.ServiceRoot = s.serviceRootFromView(serviceView)
			record, err := tailscaleResolverRecordProof(service)
			if err != nil {
				return fmt.Errorf(
					"tailscale resolver database record for %q: %w",
					journalService.ServiceName,
					err,
				)
			}
			if record != journalService.Record {
				return fmt.Errorf(
					"tailscale resolver database record for %q changed since journal creation",
					journalService.ServiceName,
				)
			}
		}
		return nil
	})
}

func tailscaleResolverJournalCommitted(contents tailscaleResolverJournalContents) bool {
	return len(contents.Phases) == 5 &&
		contents.Phases[len(contents.Phases)-1] == tailscaleResolverPhaseCommitted
}

func (s *Server) stopSalvageableTailscaleResolverJournalServices(path string) {
	services, err := salvageTailscaleResolverJournalServices(path)
	if err != nil {
		return
	}
	_ = s.stopTailscaleResolverJournalServices(services)
}

func salvageTailscaleResolverJournalServices(
	path string,
) ([]tailscaleResolverJournalService, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	line, err := bufio.NewReaderSize(file, tailscaleResolverJournalMaxLine).ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return nil, errors.New("tailscale resolver journal header is too large")
	}
	if err != nil {
		return nil, err
	}
	line = bytes.TrimSuffix(line, []byte{'\n'})
	var header tailscaleResolverJournalHeader
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&header); err != nil {
		return nil, err
	}
	if len(header.Services) == 0 {
		return nil, errors.New("tailscale resolver journal services are required")
	}
	seen := make(map[string]struct{}, len(header.Services))
	for _, service := range header.Services {
		if err := serviceid.Validate(service.ServiceName); err != nil {
			return nil, fmt.Errorf("unsafe tailscale resolver journal service: %w", err)
		}
		if service.UnitName != "yeet-"+service.ServiceName+"-ts.service" {
			return nil, errors.New("unsafe tailscale resolver journal service")
		}
		if _, duplicate := seen[service.ServiceName]; duplicate {
			return nil, errors.New("duplicate tailscale resolver journal service")
		}
		seen[service.ServiceName] = struct{}{}
	}
	return header.Services, nil
}

func (s *Server) checkTailscaleResolverMutationAllowed() error {
	s.tailscaleResolverRecovery.mu.RLock()
	defer s.tailscaleResolverRecovery.mu.RUnlock()
	return s.tailscaleResolverRecovery.block
}

func (s *Server) withTailscaleResolverMutationGuard(mutate func() error) error {
	s.tailscaleResolverRecovery.mu.RLock()
	defer s.tailscaleResolverRecovery.mu.RUnlock()
	if s.tailscaleResolverRecovery.block != nil {
		return s.tailscaleResolverRecovery.block
	}
	return mutate()
}

func (s *Server) blockTailscaleResolverRecovery(cause error) error {
	if cause == nil {
		cause = errors.New("unknown recovery failure")
	}
	block := cause
	if !errors.Is(block, errTailscaleResolverRecoveryBlocked) {
		block = fmt.Errorf("%w: %v", errTailscaleResolverRecoveryBlocked, cause)
	}
	s.tailscaleResolverRecovery.mu.Lock()
	defer s.tailscaleResolverRecovery.mu.Unlock()
	if s.tailscaleResolverRecovery.afterBlockLock != nil {
		s.tailscaleResolverRecovery.afterBlockLock()
	}
	s.tailscaleResolverRecovery.block = block
	return block
}

func (s *Server) clearTailscaleResolverMutationBlock() {
	s.tailscaleResolverRecovery.mu.Lock()
	s.tailscaleResolverRecovery.block = nil
	s.tailscaleResolverRecovery.mu.Unlock()
}
