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

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/iso"
)

type isoPoolProbe interface {
	HostPrefixes(context.Context) ([]netip.Prefix, error)
	NamespacePrefixes(context.Context) ([]netip.Prefix, error)
	DockerPrefixes(context.Context) ([]netip.Prefix, error)
}

var automaticISOPoolCandidates = []string{
	iso.PreferredPool, "172.29.0.0/16", "172.28.0.0/16", "172.27.0.0/16",
	"172.26.0.0/16", "172.25.0.0/16", "172.24.0.0/16", "172.23.0.0/16",
	"172.22.0.0/16", "172.21.0.0/16", "172.20.0.0/16", "172.19.0.0/16",
	"172.18.0.0/16", "172.16.0.0/16", "172.17.0.0/16", "172.31.0.0/16",
}

var isoPoolProbeForServer = func(*Server) isoPoolProbe {
	return commandISOPoolProbe{run: runISOPoolCommand}
}

type isoPoolCommandRunner func(context.Context, string, ...string) ([]byte, error)

type commandISOPoolProbe struct {
	run isoPoolCommandRunner
}

func runISOPoolCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("run %s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (p commandISOPoolProbe) HostPrefixes(ctx context.Context) ([]netip.Prefix, error) {
	addresses, err := p.run(ctx, "ip", "-j", "address")
	if err != nil {
		return nil, fmt.Errorf("probe host addresses: %w", err)
	}
	prefixes, err := parseIPJSONAddresses(addresses)
	if err != nil {
		return nil, fmt.Errorf("parse host addresses: %w", err)
	}
	routes, err := p.run(ctx, "ip", "-j", "route", "show", "table", "all")
	if err != nil {
		return nil, fmt.Errorf("probe host routes: %w", err)
	}
	routePrefixes, err := parseIPJSONRoutes(routes)
	if err != nil {
		return nil, fmt.Errorf("parse host routes: %w", err)
	}
	return append(prefixes, routePrefixes...), nil
}

func (p commandISOPoolProbe) NamespacePrefixes(ctx context.Context) ([]netip.Prefix, error) {
	out, err := p.run(ctx, "ip", "netns", "list")
	if err != nil {
		return nil, fmt.Errorf("list network namespaces: %w", err)
	}
	var prefixes []netip.Prefix
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		addresses, runErr := p.run(ctx, "ip", "netns", "exec", name, "ip", "-j", "address")
		if runErr != nil {
			return nil, fmt.Errorf("probe namespace %s addresses: %w", name, runErr)
		}
		addressPrefixes, parseErr := parseIPJSONAddresses(addresses)
		if parseErr != nil {
			return nil, fmt.Errorf("parse namespace %s addresses: %w", name, parseErr)
		}
		routes, runErr := p.run(ctx, "ip", "netns", "exec", name, "ip", "-j", "route", "show", "table", "all")
		if runErr != nil {
			return nil, fmt.Errorf("probe namespace %s routes: %w", name, runErr)
		}
		routePrefixes, parseErr := parseIPJSONRoutes(routes)
		if parseErr != nil {
			return nil, fmt.Errorf("parse namespace %s routes: %w", name, parseErr)
		}
		prefixes = append(prefixes, addressPrefixes...)
		prefixes = append(prefixes, routePrefixes...)
	}
	return prefixes, nil
}

func (p commandISOPoolProbe) DockerPrefixes(ctx context.Context) ([]netip.Prefix, error) {
	out, err := p.run(ctx, "docker", "network", "ls", "--quiet")
	if err != nil {
		return nil, fmt.Errorf("list Docker networks: %w", err)
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return nil, nil
	}
	args := append([]string{"network", "inspect"}, ids...)
	out, err = p.run(ctx, "docker", args...)
	if err != nil {
		return nil, fmt.Errorf("inspect Docker networks: %w", err)
	}
	return parseDockerNetworkPrefixes(out)
}

func parseIPJSONAddresses(raw []byte) ([]netip.Prefix, error) {
	var links []struct {
		Addresses []struct {
			Family    string `json:"family"`
			Local     string `json:"local"`
			PrefixLen int    `json:"prefixlen"`
		} `json:"addr_info"`
	}
	if err := json.Unmarshal(raw, &links); err != nil {
		return nil, err
	}
	var prefixes []netip.Prefix
	for _, link := range links {
		for _, address := range link.Addresses {
			if address.Family != "inet" {
				continue
			}
			addr, err := netip.ParseAddr(address.Local)
			if err != nil || !addr.Is4() || address.PrefixLen < 0 || address.PrefixLen > 32 {
				return nil, fmt.Errorf("invalid IPv4 address %q/%d", address.Local, address.PrefixLen)
			}
			prefixes = append(prefixes, netip.PrefixFrom(addr, address.PrefixLen).Masked())
		}
	}
	return prefixes, nil
}

func parseIPJSONRoutes(raw []byte) ([]netip.Prefix, error) {
	var routes []struct {
		Destination string `json:"dst"`
	}
	if err := json.Unmarshal(raw, &routes); err != nil {
		return nil, err
	}
	var prefixes []netip.Prefix
	for _, route := range routes {
		destination := strings.TrimSpace(route.Destination)
		if destination == "" || destination == "default" {
			continue
		}
		prefix, err := netip.ParsePrefix(destination)
		if err != nil {
			addr, addrErr := netip.ParseAddr(destination)
			if addrErr != nil {
				return nil, fmt.Errorf("invalid IPv4 route destination %q", destination)
			}
			if !addr.Is4() {
				continue
			}
			prefix = netip.PrefixFrom(addr, 32)
		}
		if prefix.Addr().Is4() {
			prefixes = append(prefixes, prefix.Masked())
		}
	}
	return prefixes, nil
}

func parseDockerNetworkPrefixes(raw []byte) ([]netip.Prefix, error) {
	var networks []struct {
		IPAM struct {
			Config []struct {
				Subnet  string `json:"Subnet"`
				Gateway string `json:"Gateway"`
				IPRange string `json:"IPRange"`
			} `json:"Config"`
		} `json:"IPAM"`
	}
	if err := json.Unmarshal(raw, &networks); err != nil {
		return nil, fmt.Errorf("parse Docker network inspection: %w", err)
	}
	var prefixes []netip.Prefix
	for _, network := range networks {
		for _, config := range network.IPAM.Config {
			var err error
			prefixes, err = appendDockerIPAMPrefixes(prefixes, config.Subnet, config.IPRange, config.Gateway)
			if err != nil {
				return nil, err
			}
		}
	}
	return prefixes, nil
}

func appendDockerIPAMPrefixes(prefixes []netip.Prefix, subnet, ipRange, gatewayRaw string) ([]netip.Prefix, error) {
	for _, rawPrefix := range []string{subnet, ipRange} {
		if strings.TrimSpace(rawPrefix) == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(rawPrefix)
		if err != nil {
			return nil, fmt.Errorf("invalid Docker IPv4 prefix %q", rawPrefix)
		}
		if prefix.Addr().Is4() {
			prefixes = append(prefixes, prefix.Masked())
		}
	}
	if strings.TrimSpace(gatewayRaw) == "" {
		return prefixes, nil
	}
	gateway, err := netip.ParseAddr(gatewayRaw)
	if err != nil {
		return nil, fmt.Errorf("invalid Docker gateway %q", gatewayRaw)
	}
	if gateway.Is4() {
		prefixes = append(prefixes, netip.PrefixFrom(gateway, 32))
	}
	return prefixes, nil
}

func selectISOPool(ctx context.Context, probe isoPoolProbe, persisted []netip.Prefix) (netip.Prefix, error) {
	occupied := append([]netip.Prefix(nil), persisted...)
	for _, load := range []func(context.Context) ([]netip.Prefix, error){probe.HostPrefixes, probe.NamespacePrefixes, probe.DockerPrefixes} {
		prefixes, err := load(ctx)
		if err != nil {
			return netip.Prefix{}, err
		}
		occupied = append(occupied, prefixes...)
	}
	for _, raw := range automaticISOPoolCandidates {
		candidate := netip.MustParsePrefix(raw)
		if !overlapsAny(candidate, occupied) {
			return candidate, nil
		}
	}
	return netip.Prefix{}, fmt.Errorf("no collision-free ISO /16 is available in 172.16.0.0/12")
}

func overlapsAny(candidate netip.Prefix, occupied []netip.Prefix) bool {
	for _, prefix := range occupied {
		if prefix.IsValid() && candidate.Overlaps(prefix.Masked()) {
			return true
		}
	}
	return false
}

type isoPoolConflictError struct {
	candidate netip.Prefix
	occupied  netip.Prefix
}

func (e isoPoolConflictError) Error() string {
	return fmt.Sprintf("ISO pool %s conflicts with %s", e.candidate, e.occupied)
}

func (s *Server) PlanISOPool(ctx context.Context, req catchrpc.ISOPoolPlanRequest) (catchrpc.ISOPoolPlan, error) {
	desired, err := validateServerISOPool(req.Prefix)
	if err != nil {
		return catchrpc.ISOPoolPlan{}, err
	}
	dv, err := s.cfg.DB.Get()
	if err != nil {
		return catchrpc.ISOPoolPlan{}, err
	}
	plan := catchrpc.ISOPoolPlan{DesiredPrefix: desired.String(), Source: "explicit"}
	if pool := dv.ISOPool(); pool.Valid() {
		plan.CurrentPrefix = pool.Prefix().String()
		if pool.Prefix().Masked() == desired {
			plan.Source = pool.Source()
			return plan, nil
		}
	}
	plan.Changed = true
	plan.Blockers = isoPoolAllocationBlockers(dv.AsStruct().Services)
	if len(plan.Blockers) != 0 {
		return plan, nil
	}
	if err := verifyISOPoolCandidate(ctx, isoPoolProbeForServer(s), desired, persistedNetworkPrefixes(dv)); err != nil {
		var conflict isoPoolConflictError
		if errors.As(err, &conflict) {
			plan.Conflicts = []string{conflict.Error()}
			return plan, nil
		}
		return plan, err
	}
	return plan, nil
}

func (s *Server) ApplyISOPool(ctx context.Context, req catchrpc.ISOPoolApplyRequest) (catchrpc.ISOPoolApplyResult, error) {
	plan, err := s.PlanISOPool(ctx, catchrpc.ISOPoolPlanRequest{Prefix: req.Plan.DesiredPrefix})
	if err != nil {
		return catchrpc.ISOPoolApplyResult{}, err
	}
	if err := blockedISOPoolPlanError(plan); err != nil {
		return catchrpc.ISOPoolApplyResult{}, err
	}
	desired := netip.MustParsePrefix(plan.DesiredPrefix)
	var result catchrpc.ISOPoolApplyResult
	_, err = s.cfg.DB.MutateData(func(data *db.Data) error {
		currentPrefix := ""
		currentSource := ""
		if data.ISOPool != nil {
			currentPrefix = data.ISOPool.Prefix.String()
			currentSource = data.ISOPool.Source
		}
		if currentPrefix != plan.CurrentPrefix {
			return fmt.Errorf("ISO pool changed while applying; plan again")
		}
		if currentPrefix == desired.String() {
			result = catchrpc.ISOPoolApplyResult{Prefix: currentPrefix, Source: currentSource}
			return nil
		}
		if blockers := isoPoolAllocationBlockers(data.Services); len(blockers) != 0 {
			return fmt.Errorf("ISO pool change is blocked by allocations: %s", strings.Join(blockers, ", "))
		}
		data.ISOPool = &db.ISOPool{
			Prefix:           desired,
			Source:           "explicit",
			AllocatorVersion: iso.AllocatorVersion,
			PolicyVersion:    iso.PolicyVersion,
		}
		result = catchrpc.ISOPoolApplyResult{Prefix: desired.String(), Source: "explicit"}
		return nil
	})
	return result, err
}

func blockedISOPoolPlanError(plan catchrpc.ISOPoolPlan) error {
	if len(plan.Blockers) != 0 {
		return fmt.Errorf("ISO pool change is blocked by allocations: %s", strings.Join(plan.Blockers, ", "))
	}
	if len(plan.Conflicts) != 0 {
		return fmt.Errorf("ISO pool change has conflicts: %s", strings.Join(plan.Conflicts, ", "))
	}
	return nil
}

func validateServerISOPool(raw string) (netip.Prefix, error) {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
	if err != nil || !prefix.Addr().Is4() || prefix.Bits() != 16 || prefix != prefix.Masked() {
		return netip.Prefix{}, fmt.Errorf("ISO pool must be a canonical RFC1918 IPv4 /16")
	}
	private := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
	}
	for _, allowed := range private {
		if allowed.Contains(prefix.Addr()) {
			return prefix, nil
		}
	}
	return netip.Prefix{}, fmt.Errorf("ISO pool must be contained by RFC1918 space")
}

func verifyISOPoolCandidate(ctx context.Context, probe isoPoolProbe, candidate netip.Prefix, persisted []netip.Prefix) error {
	occupied := append([]netip.Prefix(nil), persisted...)
	for _, load := range []func(context.Context) ([]netip.Prefix, error){probe.HostPrefixes, probe.NamespacePrefixes, probe.DockerPrefixes} {
		prefixes, err := load(ctx)
		if err != nil {
			return err
		}
		occupied = append(occupied, prefixes...)
	}
	for _, prefix := range occupied {
		if prefix.IsValid() && candidate.Overlaps(prefix.Masked()) {
			return isoPoolConflictError{candidate: candidate, occupied: prefix.Masked()}
		}
	}
	return nil
}

func isoPoolAllocationBlockers(services map[string]*db.Service) []string {
	var blockers []string
	for name, service := range services {
		if service != nil && service.ISO != nil {
			blockers = append(blockers, name)
		}
	}
	sort.Strings(blockers)
	return blockers
}

func persistedNetworkPrefixes(dv db.DataView) []netip.Prefix {
	data := dv.AsStruct()
	if data == nil {
		return nil
	}
	var prefixes []netip.Prefix
	if data.ISOPool != nil && data.ISOPool.Prefix.IsValid() {
		prefixes = append(prefixes, data.ISOPool.Prefix.Masked())
	}
	for _, service := range data.Services {
		prefixes = appendServiceNetworkPrefixes(prefixes, service)
	}
	for _, network := range data.DockerNetworks {
		prefixes = appendDockerNetworkPrefixes(prefixes, network)
	}
	return prefixes
}

func appendServiceNetworkPrefixes(prefixes []netip.Prefix, service *db.Service) []netip.Prefix {
	if service == nil {
		return prefixes
	}
	if service.SvcNetwork != nil {
		prefixes = appendIPv4AddressPrefix(prefixes, service.SvcNetwork.IPv4)
	}
	if service.VM != nil {
		for _, network := range service.VM.Networks {
			prefixes = appendIPv4AddressPrefix(prefixes, network.IP)
		}
	}
	if service.ISO != nil {
		prefixes = appendValidIPv4Prefix(prefixes, service.ISO.Link)
		prefixes = appendValidIPv4Prefix(prefixes, service.ISO.Project)
	}
	return prefixes
}

func appendDockerNetworkPrefixes(prefixes []netip.Prefix, network *db.DockerNetwork) []netip.Prefix {
	if network == nil {
		return prefixes
	}
	prefixes = appendValidIPv4Prefix(prefixes, network.IPv4Gateway)
	prefixes = appendValidIPv4Prefix(prefixes, network.IPv4Range)
	for _, endpoint := range network.Endpoints {
		if endpoint != nil {
			prefixes = appendValidIPv4Prefix(prefixes, endpoint.IPv4)
		}
	}
	return prefixes
}

func appendIPv4AddressPrefix(prefixes []netip.Prefix, addr netip.Addr) []netip.Prefix {
	if addr.IsValid() && addr.Is4() {
		return append(prefixes, netip.PrefixFrom(addr, 32))
	}
	return prefixes
}

func appendValidIPv4Prefix(prefixes []netip.Prefix, prefix netip.Prefix) []netip.Prefix {
	if prefix.IsValid() && prefix.Addr().Is4() {
		return append(prefixes, prefix.Masked())
	}
	return prefixes
}

func (s *Server) ensureISOPool(ctx context.Context) error {
	dv, err := s.cfg.DB.Get()
	if err != nil {
		return err
	}
	if pool := dv.ISOPool(); pool.Valid() {
		return nil
	}
	persisted := persistedNetworkPrefixes(dv)
	prefix, err := selectISOPool(ctx, isoPoolProbeForServer(s), persisted)
	if err != nil {
		return err
	}
	if err := verifyISOPoolCandidate(ctx, isoPoolProbeForServer(s), prefix, persisted); err != nil {
		return err
	}
	_, err = s.cfg.DB.MutateData(func(data *db.Data) error {
		if data.ISOPool == nil {
			data.ISOPool = &db.ISOPool{
				Prefix:           prefix,
				Source:           "automatic",
				AllocatorVersion: iso.AllocatorVersion,
				PolicyVersion:    iso.PolicyVersion,
			}
		}
		return nil
	})
	return err
}
