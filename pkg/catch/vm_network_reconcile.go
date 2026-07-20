// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"sort"
	"strings"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/iso"
)

var vmNetworkEnsureRuntimeIdentity = ensureVMRuntimeIdentity

var (
	vmNetworkLinkLister             = listVMNetworkLinks
	vmNetworkRouteLister            = listVMNetworkRoutes
	vmNetworkLiveStateCollector     = collectVMNetworkLiveState
	vmNetworkReconcileRunner        vmNetworkCommandRunner
	vmNetworkVerifyCommand          = runVMNetworkVerifyCommand
	verifyVMNetworkPlanForReconcile = verifyVMNetworkPlan
	ensureVMISONetworkForReconcile  = ensureVMISONetworkAttachment
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

type vmNetworkEnsureInput struct {
	Server      *Server
	DataRoot    string
	Service     string
	ServiceRoot string
	MarkReady   bool
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

//nolint:cyclop // Reconciliation keeps ensure-before-cleanup ordering explicit across ordinary and ISO plans.
func (s *Server) reconcileVMNetworks(ctx context.Context) error {
	dv, err := s.getDB()
	if err != nil {
		return err
	}
	desired, err := s.vmNetworkDesiredStateFromDB(dv)
	if err != nil {
		return err
	}
	live, err := vmNetworkLiveStateCollector(ctx)
	if err != nil {
		return err
	}
	ordinary := vmNetworkDesiredState{Owned: desired.Owned}
	var isoServices []string
	for _, plan := range desired.Plans {
		if plan.hasNetworkMode("iso") {
			if _, err := vmNetworkSetupCommands(plan); err != nil {
				return err
			}
			isoServices = append(isoServices, plan.Service)
			continue
		}
		ordinary.Plans = append(ordinary.Plans, plan)
	}
	ensureCmds, err := ordinary.ensureCommands()
	if err != nil {
		return err
	}
	cleanupCmds := unownedVMNetworkCleanupCommands(live, desired.Owned)
	if err := runVMNetworkLifecycleCommands(ensureCmds, nil, "reconcile VM networks"); err != nil {
		return err
	}
	sort.Strings(isoServices)
	for _, service := range isoServices {
		sv := dv.Services().Get(service)
		if err := ensureVMNetworkFromDataView(ctx, dv, vmNetworkEnsureInput{
			Server: s, DataRoot: s.cfg.RootDir, Service: service,
			ServiceRoot: s.serviceRootFromService(sv.AsStruct()),
		}); err != nil {
			return fmt.Errorf("reconcile VM ISO network %q: %w", service, err)
		}
	}
	return runVMNetworkLifecycleCommands(nil, cleanupCmds, "reconcile VM networks")
}

func (s *Server) EnsureVMNetwork(ctx context.Context, service string) error {
	dv, err := s.getDB()
	if err != nil {
		return err
	}
	sv, ok := dv.Services().GetOk(service)
	if !ok {
		return fmt.Errorf("service %q not found", service)
	}
	return ensureVMNetworkFromDataView(ctx, dv, vmNetworkEnsureInput{
		Server:      s,
		DataRoot:    s.cfg.RootDir,
		Service:     service,
		ServiceRoot: s.serviceRootFromService(sv.AsStruct()),
		MarkReady:   true,
	})
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
	sv, ok := dv.Services().GetOk(service)
	if !ok {
		return fmt.Errorf("service %q not found", service)
	}
	server := &Server{cfg: *cfg}
	return ensureVMNetworkFromDataView(ctx, &dv, vmNetworkEnsureInput{
		Server:      server,
		DataRoot:    cfg.RootDir,
		Service:     service,
		ServiceRoot: serviceRootFromConfig(*cfg, *sv.AsStruct()),
		MarkReady:   true,
	})
}

//nolint:cyclop // Jailer transition, attachment, verification, and cleanup order is security-sensitive.
func ensureVMNetworkFromDataView(ctx context.Context, dv *db.DataView, input vmNetworkEnsureInput) error {
	identity, err := vmNetworkEnsureRuntimeIdentity()
	if err != nil {
		return err
	}
	plan, allocation, err := vmNetworkPlanAndISOForService(dv, input.Service)
	if err != nil {
		return err
	}
	attach := func(currentDV *db.DataView, currentPlan vmNetworkPlan, currentAllocation *db.ISOAllocation) error {
		currentPlan = currentPlan.WithTapOwner(identity)
		readiness, err := vmJailerReadinessForRoot(input.ServiceRoot)
		if err != nil {
			return err
		}
		if readiness == vmJailerPendingRestart {
			transition, err := newVMJailerTransitionPlan(currentDV, vmJailerTransitionInput{
				DataRoot: input.DataRoot, Service: input.Service, ServiceRoot: input.ServiceRoot,
			}, identity)
			if err != nil {
				return err
			}
			if err := executeVMJailerTransition(ctx, transition, defaultVMJailerTransitionDeps()); err != nil {
				return err
			}
		} else {
			if err := ensureOwnedVMNetwork(currentPlan, input.Service); err != nil {
				if currentAllocation == nil {
					return err
				}
				return errors.Join(err, cleanupVMISONetworkPlan(currentPlan))
			}
		}
		if err := verifyVMNetworkPlanForReconcile(ctx, currentPlan); err != nil {
			if currentAllocation == nil {
				return err
			}
			return errors.Join(err, cleanupVMISONetworkPlan(currentPlan))
		}
		return nil
	}
	if allocation == nil {
		return attach(dv, plan, nil)
	}
	if input.Server == nil {
		return fmt.Errorf("VM ISO network requires a server")
	}
	return ensureVMISONetworkForReconcile(ctx, input.Server, input.Service, input.MarkReady, attach)
}

func cleanupVMISONetworkPlan(plan vmNetworkPlan) error {
	runner := vmNetworkReconcileRunner
	if runner == nil {
		runner = execVMNetworkCommand
	}
	if err := plan.ExecuteCleanup(runner); err != nil {
		return fmt.Errorf("clean up partial VM ISO network: %w", err)
	}
	return nil
}

//nolint:cyclop // The lock, boundary, attachment, DNS, and ready transitions must remain visibly ordered.
func ensureVMISONetworkAttachment(ctx context.Context, server *Server, service string, markReady bool, attach func(*db.DataView, vmNetworkPlan, *db.ISOAllocation) error) error {
	return server.withISOOperationLock(ctx, func() error {
		current, err := server.getDB()
		if err != nil {
			return err
		}
		plan, allocation, err := vmNetworkPlanAndISOForService(current, service)
		if err != nil {
			return err
		}
		if allocation == nil {
			return fmt.Errorf("VM %q lost its ISO allocation before attachment", service)
		}
		if markReady {
			if err := validateVMISOStartState(current, service); err != nil {
				return err
			}
		}
		if err := server.ensureISONetworkBoundaryLocked(ctx, service); err != nil {
			return err
		}
		if err := attach(current, plan, allocation); err != nil {
			return server.failISORuntime(err)
		}
		if err := verifyISODNSReadyForVM(ctx); err != nil {
			return server.failISORuntime(err)
		}
		if markReady {
			if err := server.markISOReady(service); err != nil {
				return server.failISORuntime(err)
			}
		}
		return nil
	})
}

func validateVMISOStartState(dv *db.DataView, service string) error {
	if dv == nil {
		return fmt.Errorf("VM %q provisioning state is unavailable", service)
	}
	sv, ok := dv.Services().GetOk(service)
	if !ok || !sv.VM().Valid() || sv.VM().SetupState() != "ready" {
		return fmt.Errorf("VM %q provisioning is incomplete", service)
	}
	allocation := sv.ISO()
	if !allocation.Valid() {
		return fmt.Errorf("VM %q has no ISO allocation", service)
	}
	if allocation.RemoveRequested() || allocation.CleanupVerified() {
		return fmt.Errorf("VM %q ISO removal or cleanup is in progress", service)
	}
	switch iso.AllocationState(allocation.State()) {
	case iso.StateRemoving, iso.StateTombstoned, iso.StateQuarantined:
		return fmt.Errorf("VM %q ISO lifecycle state %q cannot start", service, allocation.State())
	}
	return nil
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

func (s *Server) vmNetworkDesiredStateFromDB(dv *db.DataView) (vmNetworkDesiredState, error) {
	var runtimeIdentity *vmRuntimeIdentity
	return vmNetworkDesiredStateFromDBWithTransform(dv, func(_ db.ServiceView, plan vmNetworkPlan) (vmNetworkPlan, error) {
		if runtimeIdentity == nil {
			identity, err := vmNetworkEnsureRuntimeIdentity()
			if err != nil {
				return vmNetworkPlan{}, err
			}
			runtimeIdentity = &identity
		}
		return plan.WithTapOwner(*runtimeIdentity), nil
	})
}

type vmNetworkPlanTransform func(db.ServiceView, vmNetworkPlan) (vmNetworkPlan, error)

func vmNetworkDesiredStateFromDBWithTransform(dv *db.DataView, transform vmNetworkPlanTransform) (vmNetworkDesiredState, error) {
	desired := vmNetworkDesiredState{Owned: map[string]bool{}}
	if dv == nil {
		return desired, nil
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
		plan := vmNetworkPlanFromDB(name, vm.Networks().AsSlice(), sv.ISO().AsStruct())
		if allocation := sv.ISO().AsStruct(); allocation != nil && !vmNetworkMatchesISOAllocation(plan, allocation) {
			return vmNetworkDesiredState{}, fmt.Errorf("service %q VM ISO network does not match its allocation", name)
		}
		if transform != nil {
			var err error
			plan, err = transform(sv, plan)
			if err != nil {
				return vmNetworkDesiredState{}, err
			}
		}
		desired.Plans = append(desired.Plans, plan)
		for base := range vmNetworkOwnedBases(plan) {
			desired.Owned[base] = true
		}
	}
	return desired, nil
}

func vmNetworkPlanForVMService(dv *db.DataView, service string) (vmNetworkPlan, error) {
	plan, _, err := vmNetworkPlanAndISOForService(dv, service)
	return plan, err
}

func vmNetworkPlanAndISOForService(dv *db.DataView, service string) (vmNetworkPlan, *db.ISOAllocation, error) {
	if dv == nil {
		return vmNetworkPlan{}, nil, fmt.Errorf("service %q not found", service)
	}
	sv, ok := dv.Services().GetOk(service)
	if !ok {
		return vmNetworkPlan{}, nil, fmt.Errorf("service %q not found", service)
	}
	if sv.ServiceType() != db.ServiceTypeVM {
		return vmNetworkPlan{}, nil, fmt.Errorf("service %q is not a VM service", service)
	}
	vm := sv.VM()
	if !vm.Valid() {
		return vmNetworkPlan{}, nil, fmt.Errorf("service %q is not a VM service", service)
	}
	allocation := sv.ISO().AsStruct()
	plan := vmNetworkPlanFromDB(service, vm.Networks().AsSlice(), allocation)
	if allocation != nil && !vmNetworkMatchesISOAllocation(plan, allocation) {
		return vmNetworkPlan{}, nil, fmt.Errorf("service %q VM ISO network does not match its allocation", service)
	}
	if allocation == nil && plan.hasNetworkMode("iso") {
		return vmNetworkPlan{}, nil, fmt.Errorf("service %q has a VM ISO network but no ISO allocation", service)
	}
	return plan, allocation, nil
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
		if iface.Mode == "iso" && iface.Tap != "" {
			names[iface.Tap] = true
		}
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

func ensureOwnedVMNetwork(plan vmNetworkPlan, service string) error {
	setup, err := vmNetworkSetupCommands(plan)
	if err != nil {
		return err
	}
	return runVMNetworkLifecycleCommands(setup, nil, fmt.Sprintf("ensure VM network for %q", service))
}

func ensureRunningVMISOSecuritySysctls(plan vmNetworkPlan, service string) error {
	return runVMNetworkLifecycleCommands(plan.isoInterfaceSysctlCommands(), nil, fmt.Sprintf("repair VM ISO security sysctls for %q", service))
}

func verifyVMNetworkPlan(ctx context.Context, plan vmNetworkPlan) error {
	for _, iface := range plan.Interfaces {
		if iface.Mode != "iso" {
			continue
		}
		if err := verifyVMISOInterface(ctx, iface); err != nil {
			return err
		}
	}
	return nil
}

type vmISOAddressEvidence struct {
	IfName   string          `json:"ifname"`
	Master   json.RawMessage `json:"master"`
	AddrInfo []struct {
		Family    string `json:"family"`
		Local     string `json:"local"`
		PrefixLen int    `json:"prefixlen"`
	} `json:"addr_info"`
}

func verifyVMISOInterface(ctx context.Context, iface vmNetworkInterfacePlan) error {
	link, err := loadVMISOAddressEvidence(ctx, iface.Tap)
	if err != nil {
		return err
	}
	if err := verifyVMISOAddressEvidence(iface, link); err != nil {
		return err
	}
	return verifyVMISOSysctls(ctx, iface.Tap)
}

func loadVMISOAddressEvidence(ctx context.Context, tap string) (vmISOAddressEvidence, error) {
	output, err := vmNetworkVerifyCommand(ctx, "ip", "-j", "address", "show", "dev", tap)
	if err != nil {
		return vmISOAddressEvidence{}, fmt.Errorf("verify VM ISO TAP %s: %w", tap, err)
	}
	var links []vmISOAddressEvidence
	if err := json.Unmarshal(output, &links); err != nil || len(links) != 1 || links[0].IfName != tap {
		return vmISOAddressEvidence{}, fmt.Errorf("verify VM ISO TAP %s: invalid ip address evidence", tap)
	}
	return links[0], nil
}

func verifyVMISOAddressEvidence(iface vmNetworkInterfacePlan, link vmISOAddressEvidence) error {
	if master := strings.TrimSpace(string(link.Master)); master != "" && master != "null" && master != "0" {
		return fmt.Errorf("verify VM ISO TAP %s: unexpectedly attached to a bridge", iface.Tap)
	}
	want, err := netip.ParsePrefix(vmISOHostPrefix(iface))
	if err != nil {
		return fmt.Errorf("verify VM ISO TAP %s: invalid desired host address: %w", iface.Tap, err)
	}
	return verifyVMISOAddresses(iface.Tap, want, link)
}

func verifyVMISOAddresses(tap string, want netip.Prefix, link vmISOAddressEvidence) error {
	matched := false
	for _, address := range link.AddrInfo {
		if address.Family == "inet6" {
			return fmt.Errorf("verify VM ISO TAP %s: IPv6 address is present", tap)
		}
		if address.Family != "inet" {
			continue
		}
		got, err := netip.ParseAddr(address.Local)
		if err != nil || got != want.Addr() || address.PrefixLen != want.Bits() || matched {
			return fmt.Errorf("verify VM ISO TAP %s: unexpected IPv4 address evidence", tap)
		}
		matched = true
	}
	if !matched {
		return fmt.Errorf("verify VM ISO TAP %s: host address is missing", tap)
	}
	return nil
}

func verifyVMISOSysctls(ctx context.Context, tap string) error {
	rpFilter := "net.ipv4.conf." + tap + ".rp_filter"
	value, err := vmNetworkVerifyCommand(ctx, "sysctl", "-n", rpFilter)
	if err != nil {
		return fmt.Errorf("verify VM ISO TAP %s: read %s: %w", tap, rpFilter, err)
	}
	mode := strings.TrimSpace(string(value))
	if mode != "1" && mode != "2" {
		return fmt.Errorf("verify VM ISO TAP %s: source validation is disabled or invalid (%s=%q)", tap, rpFilter, mode)
	}
	disableIPv6 := "net.ipv6.conf." + tap + ".disable_ipv6"
	value, err = vmNetworkVerifyCommand(ctx, "sysctl", "-n", disableIPv6)
	if err != nil || strings.TrimSpace(string(value)) != "1" {
		return fmt.Errorf("verify VM ISO TAP %s: %s is not 1", tap, disableIPv6)
	}
	return nil
}

func runVMNetworkVerifyCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("run %s: %w: %s", strings.Join(append([]string{name}, args...), " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
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
		plan := vmNetworkPlanFromDB(sv.Name(), vm.Networks().AsSlice(), sv.ISO().AsStruct())
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
