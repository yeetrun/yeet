// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/yeetrun/yeet/pkg/db"
)

func (s *Server) withTailscaleResolverReadyForActivation(
	ctx context.Context,
	serviceName string,
	mutate func() error,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	serviceView, err := s.serviceView(serviceName)
	if err != nil {
		return fmt.Errorf("load service for tailscale resolver readiness: %w", err)
	}
	if !tailscaleResolverPersistedRecord(*serviceView.AsStruct()) {
		if mutate == nil {
			return nil
		}
		return mutate()
	}
	return s.withTailscaleResolverMutationGuard(func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		serviceView, err := s.serviceView(serviceName)
		if err != nil {
			return fmt.Errorf("load service for tailscale resolver readiness: %w", err)
		}
		service := *serviceView.AsStruct()
		service.ServiceRoot = s.serviceRootFromView(serviceView)
		if err := s.checkTailscaleResolverReady(ctx, service); err != nil {
			return err
		}
		if mutate == nil {
			return nil
		}
		return mutate()
	})
}

func (s *Server) checkTailscaleResolverReady(ctx context.Context, service db.Service) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !tailscaleResolverPersistedRecord(service) {
		return nil
	}
	service.ServiceRoot = serviceRootFromConfig(s.cfg, service)
	catchRunner := s.catchRunnerPath()
	catchRunnerLocation, err := tailscaleResolverCatchRunnerLocation(catchRunner)
	if err != nil {
		return fmt.Errorf(
			"tailscale resolver readiness for service %q: %w",
			service.Name,
			err,
		)
	}
	catchRunnerProof, _, err := captureTailscaleResolverManagedFileAt(
		catchRunnerLocation.root,
		catchRunnerLocation.relative,
		catchRunnerLocation.path,
		"Catch runner",
	)
	if err != nil {
		return fmt.Errorf(
			"tailscale resolver readiness for service %q: %w",
			service.Name,
			err,
		)
	}
	plan, err := s.planTailscaleResolverService(ctx, service, catchRunner)
	if err != nil {
		return fmt.Errorf(
			"tailscale resolver readiness for service %q: %w",
			service.Name,
			err,
		)
	}
	for _, file := range plan.Files {
		if !bytes.Equal(file.Original, file.Next) {
			return fmt.Errorf(
				"tailscale resolver readiness for service %q: unit %s requires resolver isolation migration",
				service.Name,
				file.Path,
			)
		}
	}
	if err := validateServiceIdentityPathProofAt(
		catchRunnerLocation.root,
		catchRunnerLocation.relative,
		catchRunnerProof,
	); err != nil {
		return fmt.Errorf(
			"tailscale resolver readiness for service %q changed during verification: Catch runner: %w",
			service.Name,
			err,
		)
	}
	if err := revalidateTailscaleResolverReadiness(ctx, plan); err != nil {
		return fmt.Errorf(
			"tailscale resolver readiness for service %q changed during verification: %w",
			service.Name,
			err,
		)
	}
	return ctx.Err()
}

func revalidateTailscaleResolverReadiness(
	ctx context.Context,
	plan tailscaleResolverServicePlan,
) error {
	if err := revalidateTailscaleResolverProofs(plan); err != nil {
		return err
	}
	return ensureTailscaleResolverUnitHasNoDropIns(ctx, plan.UnitName)
}

// checkTailscaleResolverCanonicalReady proves the migrated/generated record
// before any stable systemd destination is replaced. The full readiness check
// still runs after staging and proves the installed unit independently.
func (s *Server) checkTailscaleResolverCanonicalReady(
	ctx context.Context,
	service db.Service,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !tailscaleResolverPersistedRecord(service) {
		return nil
	}
	service.ServiceRoot = serviceRootFromConfig(s.cfg, service)
	readiness, err := s.captureTailscaleResolverCanonicalReadiness(ctx, service)
	if err != nil {
		return fmt.Errorf("tailscale resolver readiness for service %q: %w", service.Name, err)
	}
	if err := readiness.revalidate(ctx); err != nil {
		return fmt.Errorf(
			"tailscale resolver readiness for service %q changed during verification: %w",
			service.Name,
			err,
		)
	}
	return ctx.Err()
}

type tailscaleResolverCanonicalReadiness struct {
	catchRunnerLocation tailscaleResolverManagedLocation
	catchRunnerProof    serviceIdentityPathProof
	plan                tailscaleResolverServicePlan
}

func (s *Server) captureTailscaleResolverCanonicalReadiness(
	ctx context.Context,
	service db.Service,
) (tailscaleResolverCanonicalReadiness, error) {
	unitName := "yeet-" + service.Name + "-ts.service"
	catchRunner := s.catchRunnerPath()
	catchRunnerLocation, err := tailscaleResolverCatchRunnerLocation(catchRunner)
	if err != nil {
		return tailscaleResolverCanonicalReadiness{}, err
	}
	catchRunnerProof, _, err := captureTailscaleResolverManagedFileAt(
		catchRunnerLocation.root,
		catchRunnerLocation.relative,
		catchRunnerLocation.path,
		"Catch runner",
	)
	if err != nil {
		return tailscaleResolverCanonicalReadiness{}, err
	}
	record, err := tailscaleResolverRecordProof(service)
	if err != nil {
		return tailscaleResolverCanonicalReadiness{}, err
	}
	canonical, proof, raw, generation, err := captureTailscaleResolverCanonicalUnit(
		service,
		record,
		catchRunner,
	)
	if err != nil {
		return tailscaleResolverCanonicalReadiness{}, err
	}
	provenance, err := s.captureTailscaleResolverGenerationProvenance(service, record, generation)
	if err != nil {
		return tailscaleResolverCanonicalReadiness{}, err
	}
	next, _, err := ensureTailscaleUnitResolverIsolation(string(raw), catchRunner)
	if err != nil {
		return tailscaleResolverCanonicalReadiness{}, err
	}
	if !bytes.Equal(raw, []byte(next)) {
		return tailscaleResolverCanonicalReadiness{}, fmt.Errorf(
			"unit %s requires resolver isolation migration",
			canonical.path,
		)
	}
	if err := ensureTailscaleResolverUnitHasNoDropIns(ctx, unitName); err != nil {
		return tailscaleResolverCanonicalReadiness{}, err
	}
	return tailscaleResolverCanonicalReadiness{
		catchRunnerLocation: catchRunnerLocation,
		catchRunnerProof:    catchRunnerProof,
		plan: tailscaleResolverServicePlan{
			ServiceName: service.Name,
			UnitName:    unitName,
			Record:      record,
			Generation:  generation,
			Files: []tailscaleResolverUnitFilePlan{{
				Root: canonical.root, Relative: canonical.relative, Path: canonical.path,
				Original: raw, Next: []byte(next), Proof: proof,
			}},
			Provenance: provenance,
		},
	}, nil
}

func captureTailscaleResolverCanonicalUnit(
	service db.Service,
	record tailscaleResolverServiceRecordProof,
	catchRunner string,
) (
	tailscaleResolverManagedLocation,
	serviceIdentityPathProof,
	[]byte,
	tailscaleResolverGeneration,
	error,
) {
	locations, err := tailscaleResolverUnitLocations(
		service,
		record,
		tailscaleSidecarInstalledUnitPath(service.Name),
	)
	if err != nil {
		return tailscaleResolverManagedLocation{}, serviceIdentityPathProof{}, nil, tailscaleResolverGeneration{}, err
	}
	var canonical tailscaleResolverManagedLocation
	for _, location := range locations {
		if location.path == record.TSServiceArtifact {
			canonical = location
			break
		}
	}
	if canonical.path == "" {
		return tailscaleResolverManagedLocation{}, serviceIdentityPathProof{}, nil, tailscaleResolverGeneration{}, errors.New("canonical unit is missing")
	}
	proof, raw, err := captureTailscaleResolverManagedFileAt(
		canonical.root,
		canonical.relative,
		canonical.path,
		"canonical unit",
	)
	if err != nil {
		return tailscaleResolverManagedLocation{}, serviceIdentityPathProof{}, nil, tailscaleResolverGeneration{}, err
	}
	parsed, err := parseTailscaleResolverUnit(string(raw))
	if err != nil {
		return tailscaleResolverManagedLocation{}, serviceIdentityPathProof{}, nil, tailscaleResolverGeneration{},
			fmt.Errorf("parse canonical unit %s: %w", canonical.path, err)
	}
	generation, err := classifyTailscaleResolverGeneration(service, parsed, catchRunner)
	if err != nil {
		return tailscaleResolverManagedLocation{}, serviceIdentityPathProof{}, nil, tailscaleResolverGeneration{},
			fmt.Errorf("canonical unit: %w", err)
	}
	return canonical, proof, raw, generation, nil
}

func (r tailscaleResolverCanonicalReadiness) revalidate(ctx context.Context) error {
	if err := validateServiceIdentityPathProofAt(
		r.catchRunnerLocation.root,
		r.catchRunnerLocation.relative,
		r.catchRunnerProof,
	); err != nil {
		return fmt.Errorf("catch runner proof: %w", err)
	}
	if err := revalidateTailscaleResolverProofs(r.plan); err != nil {
		return err
	}
	return ensureTailscaleResolverUnitHasNoDropIns(ctx, r.plan.UnitName)
}

func (s *Server) captureTailscaleResolverGenerationProvenance(
	service db.Service,
	record tailscaleResolverServiceRecordProof,
	generation tailscaleResolverGeneration,
) ([]tailscaleResolverManagedPathProof, error) {
	pairs, err := s.tailscaleResolverProvenancePairs(service, record, generation)
	if err != nil {
		return nil, err
	}
	provenance := make([]tailscaleResolverManagedPathProof, 0, len(pairs))
	for _, artifact := range pairs {
		proof, raw, err := captureTailscaleResolverManagedFileAt(
			artifact.generation.root,
			artifact.generation.relative,
			artifact.generation.path,
			artifact.label+" generation artifact",
		)
		if err != nil {
			return nil, err
		}
		if artifact.generation.path == record.TSConfigArtifact {
			if err := validateTailscaleResolverAcceptDNSFalse(raw); err != nil {
				return nil, fmt.Errorf(
					"%s %s: %w",
					artifact.label,
					artifact.generation.path,
					err,
				)
			}
		}
		provenance = append(provenance, tailscaleResolverManagedPathProof{
			Root: artifact.generation.root, Relative: artifact.generation.relative, Proof: proof,
		})
	}
	return provenance, nil
}
