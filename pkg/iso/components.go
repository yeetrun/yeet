// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package iso

import (
	"fmt"
	"net/netip"
	"sort"
)

type ComponentPlan struct {
	Desired map[string]netip.Addr
	Retired map[string]netip.Addr
}

type AllocationState string

const (
	StateReserved    AllocationState = "reserved"
	StateReady       AllocationState = "ready"
	StateStopped     AllocationState = "stopped"
	StateDegraded    AllocationState = "degraded"
	StateRemoving    AllocationState = "removing"
	StateQuarantined AllocationState = "quarantined"
	StateTombstoned  AllocationState = "tombstoned"
)

func PlanComponents(project netip.Prefix, current map[string]netip.Addr, desired []string) (ComponentPlan, error) {
	project = project.Masked()
	if !project.IsValid() || !project.Addr().Is4() || project.Bits() != 27 {
		return ComponentPlan{}, fmt.Errorf("ISO project must be an IPv4 /27: %v", project)
	}
	names, err := normalizeComponentNames(desired)
	if err != nil {
		return ComponentPlan{}, err
	}
	used, err := validateCurrentComponents(project, current)
	if err != nil {
		return ComponentPlan{}, err
	}
	out := planExistingComponents(current, names)
	if err := allocateComponentAddresses(project, names, used, out.Desired); err != nil {
		return ComponentPlan{}, err
	}
	return out, nil
}

func normalizeComponentNames(desired []string) ([]string, error) {
	names := append([]string(nil), desired...)
	sort.Strings(names)
	for i, name := range names {
		if name == "" || i > 0 && name == names[i-1] {
			return nil, fmt.Errorf("ISO component names must be non-empty and unique")
		}
	}
	if len(names) > MaxComponents {
		return nil, ErrComponentCapacity
	}
	return names, nil
}

func validateCurrentComponents(project netip.Prefix, current map[string]netip.Addr) (map[netip.Addr]bool, error) {
	used := map[netip.Addr]bool{}
	for name, addr := range current {
		if name == "" || !project.Contains(addr) {
			return nil, fmt.Errorf("current ISO component %q has address %v outside %v", name, addr, project)
		}
		offset := uint32(addr.As4()[3] - project.Addr().As4()[3])
		if offset < 2 || offset > 30 {
			return nil, fmt.Errorf("current ISO component %q uses reserved address %v", name, addr)
		}
		if used[addr] {
			return nil, fmt.Errorf("current ISO component address %v is duplicated", addr)
		}
		used[addr] = true
	}
	return used, nil
}

func planExistingComponents(current map[string]netip.Addr, names []string) ComponentPlan {
	out := ComponentPlan{Desired: map[string]netip.Addr{}, Retired: map[string]netip.Addr{}}
	wanted := map[string]bool{}
	for _, name := range names {
		wanted[name] = true
		if addr, ok := current[name]; ok {
			out.Desired[name] = addr
		}
	}
	for name, addr := range current {
		if !wanted[name] {
			out.Retired[name] = addr
		}
	}
	return out
}

func allocateComponentAddresses(project netip.Prefix, names []string, used map[netip.Addr]bool, desired map[string]netip.Addr) error {
	for _, name := range names {
		if _, ok := desired[name]; ok {
			continue
		}
		for host := uint32(2); host <= 30; host++ {
			addr, err := addIPv4(project.Addr(), host)
			if err != nil {
				return err
			}
			if !used[addr] {
				desired[name] = addr
				used[addr] = true
				break
			}
		}
		if _, ok := desired[name]; !ok {
			return ErrComponentCapacity
		}
	}
	return nil
}
