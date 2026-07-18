// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/iso"
)

type isoReservationRequest struct {
	Kind       iso.PayloadKind
	Modes      []string
	Components []string
}

func (s *Server) reserveISOAllocation(ctx context.Context, name string, req isoReservationRequest) (*db.ISOAllocation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.ensureISOPool(ctx); err != nil {
		return nil, err
	}
	var result *db.ISOAllocation
	_, _, err := s.cfg.DB.MutateService(name, func(data *db.Data, service *db.Service) error {
		allocation, err := reserveISOAllocationInData(name, req, data, service)
		if err != nil {
			return err
		}
		result = allocation
		return nil
	})
	return result, err
}

func reserveISOAllocationInData(name string, req isoReservationRequest, data *db.Data, service *db.Service) (*db.ISOAllocation, error) {
	layout, err := isoLayoutForPool(data.ISOPool)
	if err != nil {
		return nil, err
	}
	if err := ensureISOAllocation(name, req, layout, data.Services, service); err != nil {
		return nil, err
	}
	if service.ISO.RemoveRequested {
		return nil, fmt.Errorf("service %q has ISO removal in progress", name)
	}
	if service.ISO.State == string(iso.StateTombstoned) {
		return nil, fmt.Errorf("service %q has an ISO cleanup tombstone: %s", name, service.ISO.LastError)
	}
	if err := planISOComponents(req.Components, service.ISO); err != nil {
		return nil, err
	}
	service.ISO.DesiredModes = append([]string(nil), req.Modes...)
	service.ISO.State = string(iso.StateReserved)
	service.ISO.LastError = ""
	copy := *service.ISO
	return &copy, nil
}

func isoLayoutForPool(pool *db.ISOPool) (iso.Layout, error) {
	if pool == nil {
		return iso.Layout{}, fmt.Errorf("ISO pool is not configured")
	}
	return iso.NewLayout(pool.Prefix)
}

func ensureISOAllocation(name string, req isoReservationRequest, layout iso.Layout, services map[string]*db.Service, service *db.Service) error {
	if service.ISO != nil {
		return nil
	}
	link, err := firstFreeISOLink(layout, services)
	if err != nil {
		return err
	}
	service.ISO = newDBISOAllocation(name, req, link)
	if req.Kind == iso.PayloadVM {
		return nil
	}
	project, err := firstFreeISOProject(layout, services)
	if err != nil {
		return err
	}
	service.ISO.Project = project
	service.ISO.Gateway = project.Addr().Next()
	return nil
}

func planISOComponents(components []string, allocation *db.ISOAllocation) error {
	if !allocation.Project.IsValid() {
		return nil
	}
	current := make(map[string]netip.Addr, len(allocation.Components)+len(allocation.RetiredComponents))
	for component, state := range allocation.RetiredComponents {
		current[component] = state.Address
	}
	for component, state := range allocation.Components {
		current[component] = state.Address
	}
	plan, err := iso.PlanComponents(allocation.Project, current, components)
	if err != nil {
		return err
	}
	allocation.Components = componentStates(plan.Desired, "reserved")
	allocation.RetiredComponents = componentStates(plan.Retired, "retiring")
	return nil
}

func (s *Server) markISOState(name, state string, cause error) error {
	_, _, err := s.cfg.DB.MutateService(name, func(_ *db.Data, service *db.Service) error {
		if service.ISO == nil {
			return fmt.Errorf("service %q has no ISO allocation", name)
		}
		service.ISO.State = state
		service.ISO.LastError = ""
		if cause != nil {
			service.ISO.LastError = cause.Error()
		}
		return nil
	})
	return err
}

func newDBISOAllocation(name string, req isoReservationRequest, link netip.Prefix) *db.ISOAllocation {
	token := isoNameToken(name)
	allocation := &db.ISOAllocation{
		Kind:             string(req.Kind),
		State:            string(iso.StateReserved),
		Link:             link,
		HostIP:           link.Addr().Next(),
		PeerIP:           link.Addr().Next().Next(),
		Interface:        "yi-" + token,
		PeerInterface:    "yo-" + token,
		DesiredModes:     append([]string(nil), req.Modes...),
		AllocatorVersion: iso.AllocatorVersion,
		PolicyVersion:    iso.PolicyVersion,
	}
	if req.Kind != iso.PayloadVM {
		allocation.NetNS = isoRouterNamespace(name)
	}
	return allocation
}

func isoRouterNamespace(name string) string {
	return "yeet-" + isoNameToken(name) + "-ns"
}

func isoNameToken(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:5])
}

func componentStates(addrs map[string]netip.Addr, state string) map[string]db.ISOComponent {
	if len(addrs) == 0 {
		return nil
	}
	out := make(map[string]db.ISOComponent, len(addrs))
	for name, addr := range addrs {
		out[name] = db.ISOComponent{Address: addr, State: state}
	}
	return out
}

func firstFreeISOLink(layout iso.Layout, services map[string]*db.Service) (netip.Prefix, error) {
	used := map[netip.Prefix]bool{}
	for _, service := range services {
		if service != nil && service.ISO != nil && service.ISO.Link.IsValid() {
			used[service.ISO.Link.Masked()] = true
		}
	}
	for index := 0; index < iso.MaxLinks; index++ {
		candidate, err := layout.Link(index)
		if err != nil {
			return netip.Prefix{}, err
		}
		if !used[candidate] {
			return candidate, nil
		}
	}
	return netip.Prefix{}, iso.ErrLinkCapacity
}

func firstFreeISOProject(layout iso.Layout, services map[string]*db.Service) (netip.Prefix, error) {
	used := map[netip.Prefix]bool{}
	for _, service := range services {
		if service != nil && service.ISO != nil && service.ISO.Project.IsValid() {
			used[service.ISO.Project.Masked()] = true
		}
	}
	for index := 0; index < iso.MaxProjects; index++ {
		candidate, err := layout.Project(index)
		if err != nil {
			return netip.Prefix{}, err
		}
		if !used[candidate] {
			return candidate, nil
		}
	}
	return netip.Prefix{}, iso.ErrProjectCapacity
}

// releaseISOAllocation only clears persisted allocation state. Callers must
// first verify that every corresponding live network resource has been removed.
func (s *Server) releaseISOAllocation(name string) error {
	_, _, err := s.cfg.DB.MutateService(name, func(_ *db.Data, service *db.Service) error {
		service.ISO = nil
		return nil
	})
	return err
}
