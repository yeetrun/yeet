// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"fmt"
	"net/netip"
	"os/exec"
	"sort"
	"strings"

	"github.com/yeetrun/yeet/pkg/db"
)

var (
	vmNetworkLinkLister         = listVMNetworkLinks
	vmNetworkRouteLister        = listVMNetworkRoutes
	vmNetworkLiveStateCollector = collectVMNetworkLiveState
	vmNetworkReconcileRunner    vmNetworkCommandRunner
)

type vmNetworkRoute struct {
	Destination string
	Device      string
}

type vmNetworkLiveState struct {
	Links  []string
	Routes []vmNetworkRoute
}

type vmNetworkDesiredState struct {
	Plans []vmNetworkPlan
	Owned map[string]bool
}

type vmNetworkCheckReport struct {
	Findings []string
}

func (d vmNetworkDesiredState) Check(live vmNetworkLiveState) vmNetworkCheckReport {
	liveLinks := vmNetworkLiveLinkSet(live.Links)
	findings := vmNetworkMissingLinkFindings(d.Plans, liveLinks)
	findings = append(findings, vmNetworkStaleLinkFindings(live.Links, d.Owned)...)
	findings = append(findings, vmNetworkStaleRouteFindings(live.Routes, d.Owned)...)
	sort.Strings(findings)
	return vmNetworkCheckReport{Findings: findings}
}

func vmNetworkLiveLinkSet(links []string) map[string]bool {
	live := make(map[string]bool, len(links))
	for _, link := range links {
		live[link] = true
	}
	return live
}

func vmNetworkMissingLinkFindings(plans []vmNetworkPlan, live map[string]bool) []string {
	var findings []string
	for _, plan := range plans {
		for name := range vmNetworkDeviceNames(plan) {
			if !live[name] {
				findings = append(findings, "missing link "+name)
			}
		}
	}
	return findings
}

func vmNetworkStaleLinkFindings(links []string, owned map[string]bool) []string {
	var findings []string
	for _, link := range links {
		base, _, ok := vmNetworkLinkBase(link)
		if !ok || owned[base] {
			continue
		}
		findings = append(findings, "stale link "+link)
	}
	return findings
}

func vmNetworkStaleRouteFindings(routes []vmNetworkRoute, owned map[string]bool) []string {
	var findings []string
	for _, route := range routes {
		base, _, ok := vmNetworkLinkBase(route.Device)
		if !ok || owned[base] {
			continue
		}
		findings = append(findings, "stale route "+route.Destination+" dev "+route.Device)
	}
	return findings
}

func (s *Server) reconcileVMNetworks(ctx context.Context) error {
	dv, err := s.getDB()
	if err != nil {
		return err
	}
	desired := vmNetworkDesiredStateFromDB(dv)
	live, err := vmNetworkLiveStateCollector(ctx)
	if err != nil {
		return err
	}
	ensureCmds, err := desired.ensureCommands()
	if err != nil {
		return err
	}
	cleanupCmds := unownedVMNetworkCleanupCommands(live, desired.Owned)
	return runVMNetworkLifecycleCommands(ensureCmds, cleanupCmds, "reconcile VM networks")
}

func (s *Server) EnsureVMNetwork(ctx context.Context, service string) error {
	dv, err := s.getDB()
	if err != nil {
		return err
	}
	return ensureVMNetworkFromDataView(ctx, dv, service)
}

func EnsureVMNetwork(ctx context.Context, cfg *Config, service string) error {
	if cfg == nil || cfg.DB == nil {
		return fmt.Errorf("config DB is required")
	}
	dv, err := cfg.DB.Get()
	if err != nil {
		return fmt.Errorf("failed to get data: %v", err)
	}
	if !dv.Valid() {
		return fmt.Errorf("db is invalid")
	}
	return ensureVMNetworkFromDataView(ctx, &dv, service)
}

func ensureVMNetworkFromDataView(_ context.Context, dv *db.DataView, service string) error {
	plan, err := vmNetworkPlanForVMService(dv, service)
	if err != nil {
		return err
	}
	setupCmds, err := vmNetworkSetupCommands(plan)
	if err != nil {
		return err
	}
	return runVMNetworkLifecycleCommands(setupCmds, nil, fmt.Sprintf("ensure VM network for %q", service))
}

func (s *Server) reconcileOrphanedVMServiceNetworks(ctx context.Context) error {
	dv, err := s.getDB()
	if err != nil {
		return err
	}
	links, err := vmNetworkLinkLister(ctx)
	if err != nil {
		return err
	}
	desired := vmNetworkDesiredState{Owned: ownedVMServiceNetworkBases(dv)}
	cmds := unownedVMNetworkLinkCleanupCommands(links, desired.Owned)
	if len(cmds) == 0 {
		return nil
	}
	runner := vmNetworkReconcileRunner
	if runner == nil {
		runner = execVMNetworkCommand
	}
	if err := runVMNetworkCommands(runner, cmds, vmNetworkCommandModeCleanup); err != nil {
		return fmt.Errorf("clean up orphaned VM service networks: %w", err)
	}
	return nil
}

func vmNetworkDesiredStateFromDB(dv *db.DataView) vmNetworkDesiredState {
	desired := vmNetworkDesiredState{Owned: map[string]bool{}}
	if dv == nil {
		return desired
	}
	for _, sv := range dv.Services().All() {
		if sv.ServiceType() != db.ServiceTypeVM {
			continue
		}
		vm := sv.VM()
		if !vm.Valid() {
			continue
		}
		name := sv.Name()
		plan := vmNetworkPlanFromDB(name, vm.Networks().AsSlice())
		desired.Plans = append(desired.Plans, plan)
		for base := range vmNetworkOwnedBases(plan) {
			desired.Owned[base] = true
		}
	}
	return desired
}

func vmNetworkPlanForVMService(dv *db.DataView, service string) (vmNetworkPlan, error) {
	if dv == nil {
		return vmNetworkPlan{}, fmt.Errorf("service %q not found", service)
	}
	sv, ok := dv.Services().GetOk(service)
	if !ok {
		return vmNetworkPlan{}, fmt.Errorf("service %q not found", service)
	}
	if sv.ServiceType() != db.ServiceTypeVM {
		return vmNetworkPlan{}, fmt.Errorf("service %q is not a VM service", service)
	}
	vm := sv.VM()
	if !vm.Valid() {
		return vmNetworkPlan{}, fmt.Errorf("service %q is not a VM service", service)
	}
	return vmNetworkPlanFromDB(service, vm.Networks().AsSlice()), nil
}

func vmNetworkOwnedBases(plan vmNetworkPlan) map[string]bool {
	owned := map[string]bool{}
	for _, iface := range plan.Interfaces {
		for _, name := range []string{iface.Tap, iface.Bridge, iface.VLANDevice} {
			if base, _, ok := vmNetworkLinkBase(name); ok {
				owned[base] = true
			}
		}
	}
	return owned
}

func vmNetworkDeviceNames(plan vmNetworkPlan) map[string]bool {
	names := make(map[string]bool)
	for _, iface := range plan.Interfaces {
		for _, name := range []string{iface.Tap, iface.Bridge, iface.VLANDevice} {
			if strings.HasPrefix(name, "yvm-") {
				names[name] = true
			}
		}
	}
	for _, command := range plan.SetupCommands() {
		for _, arg := range command {
			if strings.HasPrefix(arg, "yvm-") {
				names[arg] = true
			}
		}
	}
	return names
}

func (d vmNetworkDesiredState) ensureCommands() ([][]string, error) {
	var cmds [][]string
	for _, plan := range d.Plans {
		setupCmds, err := vmNetworkSetupCommands(plan)
		if err != nil {
			return nil, err
		}
		cmds = append(cmds, setupCmds...)
	}
	return cmds, nil
}

func vmNetworkSetupCommands(plan vmNetworkPlan) ([][]string, error) {
	if err := plan.validateExecutable(); err != nil {
		return nil, err
	}
	return plan.SetupCommands(), nil
}

func runVMNetworkLifecycleCommands(ensureCmds, cleanupCmds [][]string, label string) error {
	if len(ensureCmds) == 0 && len(cleanupCmds) == 0 {
		return nil
	}
	runner := vmNetworkReconcileRunner
	if runner == nil {
		runner = execVMNetworkCommand
	}
	if err := runVMNetworkCommands(runner, ensureCmds, vmNetworkCommandModeSetup); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	if err := runVMNetworkCommands(runner, cleanupCmds, vmNetworkCommandModeCleanup); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}

func ownedVMServiceNetworkBases(dv *db.DataView) map[string]bool {
	owned := map[string]bool{}
	if dv == nil {
		return owned
	}
	for _, sv := range dv.Services().All() {
		if sv.ServiceType() != db.ServiceTypeVM {
			continue
		}
		vm := sv.VM()
		if !vm.Valid() {
			continue
		}
		plan := vmNetworkPlanFromDB(sv.Name(), vm.Networks().AsSlice())
		for base := range vmNetworkOwnedBases(plan) {
			owned[base] = true
		}
	}
	return owned
}

func collectVMNetworkLiveState(ctx context.Context) (vmNetworkLiveState, error) {
	links, err := vmNetworkLinkLister(ctx)
	if err != nil {
		return vmNetworkLiveState{}, err
	}
	routes, err := vmNetworkRouteLister(ctx)
	if err != nil {
		return vmNetworkLiveState{}, err
	}
	return vmNetworkLiveState{Links: links, Routes: routes}, nil
}

func unownedVMNetworkCleanupCommands(live vmNetworkLiveState, owned map[string]bool) [][]string {
	cmds := unownedVMNetworkRouteCleanupCommands(live.Routes, owned)
	cmds = append(cmds, unownedVMNetworkLinkCleanupCommands(live.Links, owned)...)
	return cmds
}

func unownedVMNetworkRouteCleanupCommands(routes []vmNetworkRoute, owned map[string]bool) [][]string {
	var cmds [][]string
	for _, route := range routes {
		base, ok := vmNetworkRouteCleanupBase(route)
		if !ok || owned[base] {
			continue
		}
		cmds = append(cmds, []string{"ip", "route", "del", route.Destination, "dev", route.Device})
	}
	return cmds
}

func vmNetworkRouteCleanupBase(route vmNetworkRoute) (string, bool) {
	base, suffix, ok := vmNetworkLinkBase(route.Device)
	if !ok || suffix[0] != 'b' {
		return "", false
	}
	if !vmNetworkRouteDestinationIsIPv4Host(route.Destination) {
		return "", false
	}
	return base, true
}

func vmNetworkRouteDestinationIsIPv4Host(dest string) bool {
	pfx, err := netip.ParsePrefix(dest)
	return err == nil && pfx.Addr().Is4() && pfx.Bits() == 32
}

func unownedVMNetworkLinkCleanupCommands(links []string, owned map[string]bool) [][]string {
	byBase := unownedVMNetworkLinksByBase(links, owned)
	bases := make([]string, 0, len(byBase))
	for base := range byBase {
		bases = append(bases, base)
	}
	sort.Strings(bases)

	var cmds [][]string
	for _, base := range bases {
		cmds = append(cmds, unownedVMNetworkBaseCleanupCommands(base, byBase[base])...)
	}
	return cmds
}

func unownedVMNetworkLinksByBase(links []string, owned map[string]bool) map[string]map[string]bool {
	byBase := map[string]map[string]bool{}
	for _, link := range links {
		base, suffix, ok := vmNetworkLinkBase(link)
		if !ok || owned[base] {
			continue
		}
		if byBase[base] == nil {
			byBase[base] = map[string]bool{}
		}
		byBase[base][suffix] = true
	}
	return byBase
}

func unownedVMNetworkBaseCleanupCommands(base string, suffixes map[string]bool) [][]string {
	var cmds [][]string
	for _, idx := range vmNetworkLinkIndexes(suffixes) {
		for _, kind := range []byte{'v', 'n', 's', 'b', 'l'} {
			if cmd := vmNetworkLinkCleanupCommand(base, kind, idx, suffixes); len(cmd) > 0 {
				cmds = append(cmds, cmd)
			}
		}
	}
	return cmds
}

func vmNetworkLinkCleanupCommand(base string, kind byte, idx string, suffixes map[string]bool) []string {
	suffix := string(kind) + idx
	if !suffixes[suffix] {
		return nil
	}
	name := base + "-" + suffix
	if kind == 'n' {
		return []string{"ip", "netns", "exec", vmSvcNetNS, "ip", "link", "del", name}
	}
	return []string{"ip", "link", "del", name}
}

func vmNetworkLinkIndexes(suffixes map[string]bool) []string {
	seen := map[string]bool{}
	for suffix := range suffixes {
		if len(suffix) > 1 {
			seen[suffix[1:]] = true
		}
	}
	indexes := make([]string, 0, len(seen))
	for idx := range seen {
		indexes = append(indexes, idx)
	}
	sort.Strings(indexes)
	return indexes
}

func listVMNetworkLinks(ctx context.Context) ([]string, error) {
	rootLinks, err := listVMNetworkLinksWith(ctx, "ip", "-o", "link", "show")
	if err != nil {
		return nil, err
	}
	nsLinks, err := listVMNetworkLinksWith(ctx, "ip", "netns", "exec", vmSvcNetNS, "ip", "-o", "link", "show")
	if err != nil {
		return nil, err
	}
	return append(rootLinks, nsLinks...), nil
}

func listVMNetworkLinksWith(ctx context.Context, name string, args ...string) ([]string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(out), "\n")
	links := make([]string, 0, len(lines))
	for _, line := range lines {
		if link := vmNetworkLinkNameFromIPLine(line); link != "" {
			links = append(links, link)
		}
	}
	return links, nil
}

func vmNetworkLinkNameFromIPLine(line string) string {
	parts := strings.SplitN(line, ":", 3)
	if len(parts) < 2 {
		return ""
	}
	name := strings.TrimSpace(parts[1])
	name = strings.SplitN(name, "@", 2)[0]
	return strings.TrimSpace(name)
}

func listVMNetworkRoutes(ctx context.Context) ([]vmNetworkRoute, error) {
	return listVMNetworkRoutesWith(ctx, "ip", "route", "show")
}

func listVMNetworkRoutesWith(ctx context.Context, name string, args ...string) ([]vmNetworkRoute, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return nil, err
	}
	return vmNetworkRoutesFromIPRouteOutput(out), nil
}

func vmNetworkRoutesFromIPRouteOutput(out []byte) []vmNetworkRoute {
	lines := strings.Split(string(out), "\n")
	routes := make([]vmNetworkRoute, 0, len(lines))
	for _, line := range lines {
		if route, ok := vmNetworkRouteFromIPRouteLine(line); ok {
			routes = append(routes, route)
		}
	}
	return routes
}

func vmNetworkRouteFromIPRouteLine(line string) (vmNetworkRoute, bool) {
	fields := strings.Fields(line)
	if len(fields) < 3 || fields[1] != "dev" {
		return vmNetworkRoute{}, false
	}
	dev := fields[2]
	if !vmNetworkRouteDeviceIsVM(dev) {
		return vmNetworkRoute{}, false
	}
	dest, ok := vmNetworkRouteDestinationFromIPRoute(fields[0])
	if !ok {
		return vmNetworkRoute{}, false
	}
	return vmNetworkRoute{Destination: dest, Device: dev}, true
}

func vmNetworkRouteDeviceIsVM(dev string) bool {
	base, _, ok := vmNetworkLinkBase(dev)
	return ok && base != ""
}

func vmNetworkRouteDestinationFromIPRoute(dest string) (string, bool) {
	if strings.Contains(dest, "/") {
		pfx, err := netip.ParsePrefix(dest)
		if err != nil {
			return "", false
		}
		return pfx.String(), true
	}
	addr, err := netip.ParseAddr(dest)
	if err != nil {
		return "", false
	}
	bits := 128
	if addr.Is4() {
		bits = 32
	}
	return netip.PrefixFrom(addr, bits).String(), true
}
