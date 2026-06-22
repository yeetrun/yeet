// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
)

var listIPv4AddrsFn = listIPv4Addrs
var serviceVMStatusFn = serviceVMStatus
var queryVMNetworkStateFn = queryVMNetworkState
var queryVMGuestReadyFn = queryVMGuestReady

type vmDiscoveredIP struct {
	IP     string
	Source string
}

type vmNetworkDiscovery struct {
	IPs     map[string]vmDiscoveredIP
	Err     error
	Warning string
}

type serviceIPDetails struct {
	Endpoints []catchrpc.ServiceIP
	Runtime   []catchrpc.ServiceIP
}

func serviceVMStatus(sn string) (svc.Status, error) {
	runner := &vmRunner{name: sn}
	return runner.Status()
}

func (s *Server) serviceInfo(sn string) (catchrpc.ServiceInfoResponse, error) {
	return s.serviceInfoWithContext(s.serviceContext(), sn)
}

func (s *Server) serviceInfoWithContext(ctx context.Context, sn string) (catchrpc.ServiceInfoResponse, error) {
	if ctx == nil {
		ctx = s.serviceContext()
	}
	resp := catchrpc.ServiceInfoResponse{}
	dv, err := s.getDB()
	if err != nil {
		return resp, err
	}
	sv, ok := dv.Services().GetOk(sn)
	if !ok {
		resp.Found = false
		resp.Message = "service not found"
		return resp, nil
	}

	effectiveRoot := s.serviceRootFromView(sv)
	info := catchrpc.ServiceInfo{
		Name:             sn,
		ServiceType:      string(sv.ServiceType()),
		DataType:         string(ServiceDataTypeForService(sv)),
		Generation:       sv.Generation(),
		LatestGeneration: sv.LatestGeneration(),
		Paths: catchrpc.ServicePaths{
			Root:           effectiveRoot,
			EffectiveRoot:  effectiveRoot,
			ServiceRoot:    sv.ServiceRoot(),
			ServiceRootZFS: sv.ServiceRootZFS(),
		},
	}

	info.Staged = serviceHasStagedChanges(sv)
	info.Network = serviceNetworkInfo(sv)
	portInfo := servicePublishPortInfo(sn, sv)
	info.Network.Ports = portInfo.Ports
	info.Network.PortsPresent = portInfo.PortsPresent
	if sv.ServiceType() == db.ServiceTypeVM {
		vmNetwork := discoverVMNetworkIPs(ctx, sv.VM())
		info.Network.IPs = serviceVMNetworkIPs(sv.VM(), vmNetwork.IPs)
		if vmNetwork.Err != nil {
			info.Network.IPError = vmNetwork.Err.Error()
		}
		info.Network.IPWarning = vmNetwork.Warning
		info.VM = serviceVMInfo(sv.VM(), vmNetwork.IPs)
	} else {
		details, ipErr := s.serviceIPDetailsWithContext(ctx, sn, sv)
		if ipErr != nil {
			info.Network.IPError = ipErr.Error()
		} else {
			info.Network.IPs = details.Endpoints
			info.Network.RuntimeIPs = details.Runtime
		}
	}
	info.Status = s.serviceStatusInfo(sn, sv)
	info.Images = serviceImageInfo(dv, sn)
	snapshots, err := s.serviceSnapshotInfo(dv, sv)
	if err != nil {
		return resp, err
	}
	info.Snapshots = &snapshots

	resp.Found = true
	resp.Info = info
	return resp, nil
}

func servicePublishPortInfo(serviceName string, sv db.ServiceView) catchrpc.ServiceNetwork {
	out := catchrpc.ServiceNetwork{PortsPresent: true}
	ports := normalizePublish(sv.Publish().AsSlice())
	if len(ports) == 0 {
		composePath, ok := serviceComposePathForPublish(sv.AsStruct())
		if ok {
			if composePorts, err := readComposePorts(composePath, serviceName); err == nil {
				ports = composePorts
			}
		}
	}
	out.Ports = servicePortsFromPublish(ports)
	return out
}

func servicePortsFromPublish(ports []string) []catchrpc.ServicePort {
	out := make([]catchrpc.ServicePort, 0, len(ports))
	for _, port := range normalizePublish(ports) {
		out = append(out, servicePortFromPublish(port))
	}
	return out
}

func servicePortFromPublish(port string) catchrpc.ServicePort {
	protocol := "tcp"
	base := port
	if before, after, ok := strings.Cut(port, "/"); ok {
		base = before
		protocol = strings.ToLower(strings.TrimSpace(after))
		if protocol == "" {
			protocol = "tcp"
		}
	}
	parts := strings.Split(base, ":")
	switch len(parts) {
	case 2:
		hostPort, err := parseServicePortNumber(parts[0])
		if err != nil {
			break
		}
		containerPort, err := parseServicePortNumber(parts[1])
		if err != nil {
			break
		}
		return catchrpc.ServicePort{HostPort: hostPort, ContainerPort: containerPort, Protocol: protocol}
	case 3:
		hostPort, err := parseServicePortNumber(parts[1])
		if err != nil {
			break
		}
		containerPort, err := parseServicePortNumber(parts[2])
		if err != nil {
			break
		}
		hostIP := strings.TrimSpace(parts[0])
		if hostIP == "" {
			break
		}
		return catchrpc.ServicePort{HostIP: hostIP, HostPort: hostPort, ContainerPort: containerPort, Protocol: protocol}
	}
	return catchrpc.ServicePort{Raw: port}
}

func parseServicePortNumber(value string) (uint16, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(value), 10, 16)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, fmt.Errorf("port must be positive")
	}
	return uint16(n), nil
}

func (s *Server) serviceSnapshotInfo(dv *db.DataView, sv db.ServiceView) (catchrpc.ServiceSnapshots, error) {
	serverPolicy := snapshotPolicyPtrFromView(dv.SnapshotDefaults())
	servicePolicy := snapshotPolicyPtrFromView(sv.SnapshotPolicy())
	effective, err := effectiveSnapshotPolicy(serverPolicy, servicePolicy)
	if err != nil {
		return catchrpc.ServiceSnapshots{}, err
	}
	return catchrpc.ServiceSnapshots{
		Override:  snapshotPolicyRPC(servicePolicy),
		Effective: effectiveSnapshotPolicyRPCWithPreferred(effective, preferredEffectiveSnapshotMaxAge(serverPolicy, servicePolicy)),
	}, nil
}

func serviceHasStagedChanges(sv db.ServiceView) bool {
	if !sv.Valid() {
		return false
	}
	af := sv.AsStruct().Artifacts
	if af == nil {
		return false
	}
	for name := range af {
		staged, ok := af.Staged(name)
		if !ok || staged == "" {
			continue
		}
		latest, _ := af.Latest(name)
		current, _ := af.Gen(name, sv.Generation())
		if staged != latest && staged != current {
			return true
		}
	}
	return false
}

func serviceNetworkInfo(sv db.ServiceView) catchrpc.ServiceNetwork {
	var out catchrpc.ServiceNetwork
	if svcNet, ok := sv.SvcNetwork().GetOk(); ok && svcNet.IPv4.IsValid() {
		out.SvcIP = svcNet.IPv4.String()
	}
	if out.SvcIP == "" && sv.ServiceType() == db.ServiceTypeVM {
		out.SvcIP = vmSvcIPFromNetworks(sv.VM())
	}
	if macvlan, ok := sv.Macvlan().GetOk(); ok {
		out.Macvlan = &catchrpc.ServiceMacvlan{
			Interface: macvlan.Interface,
			Parent:    macvlan.Parent,
			Mac:       macvlan.Mac,
			VLAN:      macvlan.VLAN,
		}
	}
	if ts := sv.TSNet(); ts.Valid() {
		tags := ts.Tags().AsSlice()
		tsOut := &catchrpc.ServiceTailscale{
			Interface: ts.Interface(),
			Version:   ts.Version(),
			ExitNode:  ts.ExitNode(),
			Tags:      tags,
			StableID:  string(ts.StableID()),
		}
		if !tailscaleHasValues(tsOut) {
			tsOut = nil
		}
		out.Tailscale = tsOut
	}
	return out
}

func vmSvcIPFromNetworks(vm db.VMConfigView) string {
	if !vm.Valid() {
		return ""
	}
	for _, network := range vm.Networks().AsSlice() {
		if network.Mode == "svc" && network.IP.IsValid() {
			return network.IP.String()
		}
	}
	return ""
}

func serviceVMInfo(vm db.VMConfigView, discovered map[string]vmDiscoveredIP) *catchrpc.ServiceVM {
	if !vm.Valid() {
		return nil
	}
	image := vm.Image()
	disk := vm.Disk()
	ssh := vm.SSH()
	console := vm.Console()
	socketPath := strings.TrimSpace(console.SocketPath)
	out := &catchrpc.ServiceVM{
		Runtime:      vm.Runtime(),
		Image:        image.Payload,
		ImageVersion: image.Version,
		CPUs:         vm.CPUs(),
		MemoryBytes:  vm.MemoryBytes(),
		DiskBytes:    disk.Bytes,
		DiskBackend:  disk.Backend,
		DiskPath:     disk.Path,
		Console: &catchrpc.ServiceVMConsole{
			Available:  socketPath != "",
			SocketPath: socketPath,
		},
		SetupState: vm.SetupState(),
	}
	if strings.TrimSpace(ssh.User) != "" {
		out.SSH = &catchrpc.ServiceVMSSH{User: ssh.User, Host: vmSSHHostFromNetworks(vm, discovered)}
	}
	for _, network := range vm.Networks().AsSlice() {
		out.Networks = append(out.Networks, serviceVMNetworkInfo(network, discovered))
	}
	return out
}

func vmSSHHostFromNetworks(vm db.VMConfigView, discovered map[string]vmDiscoveredIP) string {
	if svcIP := vmSvcIPFromNetworks(vm); svcIP != "" {
		return svcIP
	}
	for _, network := range vm.Networks().AsSlice() {
		if ip := serviceVMNetworkIP(network, discovered); ip.IP != "" {
			return ip.IP
		}
	}
	return ""
}

func serviceVMNetworkInfo(network db.VMNetworkConfig, discovered map[string]vmDiscoveredIP) catchrpc.ServiceVMNetwork {
	out := catchrpc.ServiceVMNetwork{
		Mode:      network.Mode,
		Interface: network.Interface,
		MAC:       network.MAC,
	}
	if ip := serviceVMNetworkIP(network, discovered); ip.IP != "" {
		out.IP = ip.IP
		out.Source = ip.Source
	}
	return out
}

func serviceVMNetworkIP(network db.VMNetworkConfig, discovered map[string]vmDiscoveredIP) vmDiscoveredIP {
	if network.IP.IsValid() {
		return vmDiscoveredIP{IP: network.IP.String(), Source: "config"}
	}
	return discovered[strings.TrimSpace(network.Interface)]
}

func serviceVMNetworkIPs(vm db.VMConfigView, discovered map[string]vmDiscoveredIP) []catchrpc.ServiceIP {
	if !vm.Valid() {
		return nil
	}
	var out []catchrpc.ServiceIP
	for _, network := range vm.Networks().AsSlice() {
		ip := serviceVMNetworkIP(network, discovered)
		if ip.IP == "" {
			continue
		}
		label := "vm"
		switch network.Mode {
		case "svc":
			label = "service"
		case "lan":
			label = "lan"
		}
		out = append(out, catchrpc.ServiceIP{
			Label:     label,
			IP:        ip.IP,
			Interface: network.Interface,
			Source:    ip.Source,
		})
	}
	return out
}

func (s *Server) serviceIPList(sn string, sv db.ServiceView) ([]catchrpc.ServiceIP, error) {
	return s.serviceIPListWithContext(s.serviceContext(), sn, sv)
}

func (s *Server) serviceIPListWithContext(ctx context.Context, sn string, sv db.ServiceView) ([]catchrpc.ServiceIP, error) {
	details, err := s.serviceIPDetailsWithContext(ctx, sn, sv)
	return details.Endpoints, err
}

func (s *Server) serviceIPDetailsWithContext(ctx context.Context, sn string, sv db.ServiceView) (serviceIPDetails, error) {
	if sn == CatchService {
		ips, err := s.catchServiceIPList()
		return serviceIPDetails{Endpoints: ips}, err
	}
	if sv.ServiceType() == db.ServiceTypeVM {
		vmNetwork := discoverVMNetworkIPs(ctx, sv.VM())
		return serviceIPDetails{Endpoints: serviceVMNetworkIPs(sv.VM(), vmNetwork.IPs)}, vmNetwork.Err
	}

	args, hasNetns := serviceIPListArgs(sn, sv)
	raw, err := listIPv4AddrsFn(args)
	if err != nil {
		return serviceIPDetails{}, err
	}
	return serviceIPDetailsFromEntries(raw, serviceIPLabelConfigFromView(sv, hasNetns)), nil
}

func (s *Server) serviceContext() context.Context {
	if s.ctx != nil {
		return s.ctx
	}
	return context.Background()
}

func discoverVMNetworkIPs(ctx context.Context, vm db.VMConfigView) vmNetworkDiscovery {
	out := vmNetworkDiscovery{IPs: map[string]vmDiscoveredIP{}}
	needsAgentIP := vmNeedsAgentNetworkIP(vm)
	verifiesStaticIP := vmHasStaticNetworkIP(vm)
	if !needsAgentIP && !verifiesStaticIP {
		return out
	}
	sockets := vm.Sockets()
	socketPath := strings.TrimSpace(sockets.VsockSocketPath)
	if socketPath == "" {
		if needsAgentIP {
			out.Err = fmt.Errorf("VM agent vsock socket path is not configured")
		} else {
			out.Warning = "VM agent vsock socket path is not configured; configured VM IPs were not verified"
		}
		return out
	}
	state, err := queryVMNetworkStateFn(ctx, socketPath)
	if err != nil {
		if needsAgentIP {
			out.Err = fmt.Errorf("VM agent unavailable: %w", err)
		} else {
			out.Warning = fmt.Sprintf("VM agent unavailable; configured VM IPs were not verified: %v", err)
		}
		return out
	}
	agentIPs := vmAgentInterfaceIPs(state)
	for iface, ips := range agentIPs {
		if len(ips) == 0 {
			continue
		}
		out.IPs[iface] = vmDiscoveredIP{IP: ips[0], Source: "agent"}
	}
	out.Warning = vmStaticIPVerificationWarning(vm, agentIPs)
	return out
}

func vmNeedsAgentNetworkIP(vm db.VMConfigView) bool {
	for _, network := range vm.Networks().AsSlice() {
		if !network.IP.IsValid() {
			return true
		}
	}
	return false
}

func vmHasStaticNetworkIP(vm db.VMConfigView) bool {
	for _, network := range vm.Networks().AsSlice() {
		if network.IP.IsValid() {
			return true
		}
	}
	return false
}

func vmAgentInterfaceIPs(state vmAgentNetworkState) map[string][]string {
	out := map[string][]string{}
	for _, iface := range state.Interfaces {
		name := strings.TrimSpace(iface.Name)
		if name == "" || len(iface.IPs) == 0 {
			continue
		}
		out[name] = append([]string(nil), iface.IPs...)
	}
	return out
}

func vmStaticIPVerificationWarning(vm db.VMConfigView, agentIPs map[string][]string) string {
	var warnings []string
	for _, network := range vm.Networks().AsSlice() {
		if !network.IP.IsValid() {
			continue
		}
		iface := strings.TrimSpace(network.Interface)
		want := network.IP.String()
		got := agentIPs[iface]
		if len(got) == 0 {
			warnings = append(warnings, fmt.Sprintf("VM agent did not report interface %s for configured IP %s", iface, want))
			continue
		}
		if !stringSliceContains(got, want) {
			warnings = append(warnings, fmt.Sprintf("VM agent reported %s IP %s, configured IP is %s", iface, strings.Join(got, ", "), want))
		}
	}
	return strings.Join(warnings, "; ")
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (s *Server) catchServiceIPList() ([]catchrpc.ServiceIP, error) {
	if s.cfg.LocalClient == nil {
		return nil, fmt.Errorf("tailscale client unavailable")
	}
	st, err := s.cfg.LocalClient.StatusWithoutPeers(s.ctx)
	if err != nil {
		return nil, err
	}
	out := make([]catchrpc.ServiceIP, 0, len(st.TailscaleIPs))
	for _, ip := range st.TailscaleIPs {
		out = append(out, catchrpc.ServiceIP{
			Label: "tailscale",
			IP:    ip.String(),
		})
	}
	return out, nil
}

func serviceIPListArgs(sn string, sv db.ServiceView) ([]string, bool) {
	args := []string{"-o", "-4", "addr", "list"}
	if sn == SystemService {
		return args, false
	}
	if _, ok := sv.AsStruct().Artifacts.Gen(db.ArtifactNetNSService, sv.Generation()); !ok {
		return args, false
	}
	netns := fmt.Sprintf("yeet-%s-ns", sn)
	return append([]string{"netns", "exec", netns, "ip"}, args...), true
}

type serviceIPLabelConfig struct {
	svcIP       string
	tsIface     string
	macIface    string
	hasNetns    bool
	serviceType db.ServiceType
}

func serviceIPLabelConfigFromView(sv db.ServiceView, hasNetns bool) serviceIPLabelConfig {
	svcIP := ""
	if svcNet, ok := sv.SvcNetwork().GetOk(); ok && svcNet.IPv4.IsValid() {
		svcIP = svcNet.IPv4.String()
	}
	cfg := serviceIPLabelConfig{
		svcIP:       svcIP,
		hasNetns:    hasNetns,
		serviceType: sv.ServiceType(),
	}
	if ts := sv.TSNet(); ts.Valid() {
		cfg.tsIface = strings.TrimSpace(ts.Interface())
	}
	if mac, ok := sv.Macvlan().GetOk(); ok {
		cfg.macIface = strings.TrimSpace(mac.Interface)
	}
	return cfg
}

func serviceIPDetailsFromEntries(raw []ifaceIP, cfg serviceIPLabelConfig) serviceIPDetails {
	var out serviceIPDetails
	seenSvc := false
	for _, entry := range raw {
		label := labelForIP(entry, cfg.svcIP, cfg.tsIface, cfg.macIface, cfg.hasNetns, cfg.serviceType)
		ip := catchrpc.ServiceIP{
			Label:     label,
			IP:        entry.IP,
			Interface: entry.Interface,
		}
		if serviceIPLabelIsEndpoint(label) {
			out.Endpoints = append(out.Endpoints, ip)
		} else {
			out.Runtime = append(out.Runtime, ip)
		}
		if entry.IP == cfg.svcIP && cfg.svcIP != "" {
			seenSvc = true
		}
	}
	if cfg.svcIP != "" && !seenSvc {
		out.Endpoints = append(out.Endpoints, catchrpc.ServiceIP{
			Label: "service",
			IP:    cfg.svcIP,
		})
	}
	sortServiceIPsByEndpointPriority(out.Endpoints)
	return out
}

func serviceIPLabelIsEndpoint(label string) bool {
	switch label {
	case "lan", "tailscale", "service", "host":
		return true
	default:
		return false
	}
}

func sortServiceIPsByEndpointPriority(ips []catchrpc.ServiceIP) {
	sort.SliceStable(ips, func(i, j int) bool {
		left := serviceIPEndpointPriority(ips[i].Label)
		right := serviceIPEndpointPriority(ips[j].Label)
		if left != right {
			return left < right
		}
		if ips[i].Label != ips[j].Label {
			return ips[i].Label < ips[j].Label
		}
		if ips[i].Interface != ips[j].Interface {
			return ips[i].Interface < ips[j].Interface
		}
		return ips[i].IP < ips[j].IP
	})
}

func serviceIPEndpointPriority(label string) int {
	switch label {
	case "lan":
		return 0
	case "tailscale":
		return 1
	case "service":
		return 2
	case "host":
		return 3
	default:
		return 4
	}
}

func (s *Server) serviceStatusInfo(sn string, sv db.ServiceView) catchrpc.ServiceStatus {
	var out catchrpc.ServiceStatus
	switch sv.ServiceType() {
	case db.ServiceTypeDockerCompose:
		statuses, err := s.DockerComposeStatus(sn)
		if err != nil {
			out.Error = err.Error()
			return out
		}
		names := make([]string, 0, len(statuses))
		for name := range statuses {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			out.Components = append(out.Components, catchrpc.ServiceComponentStatus{
				Name:   name,
				Status: string(ComponentStatusFromServiceStatus(statuses[name])),
			})
		}
	case db.ServiceTypeSystemd:
		status, err := s.SystemdStatus(sn)
		if err != nil {
			out.Error = err.Error()
			return out
		}
		out.Components = append(out.Components, catchrpc.ServiceComponentStatus{
			Name:   sn,
			Status: string(ComponentStatusFromServiceStatus(status)),
		})
	case db.ServiceTypeVM:
		status, err := serviceVMStatusFn(sn)
		if err != nil {
			out.Error = err.Error()
			return out
		}
		out.Components = append(out.Components, catchrpc.ServiceComponentStatus{
			Name:   sn,
			Status: string(ComponentStatusFromServiceStatus(status)),
		})
	default:
		out.Error = fmt.Sprintf("unknown service type %q", sv.ServiceType())
	}
	return out
}

func serviceImageInfo(dv *db.DataView, service string) []catchrpc.ServiceImage {
	if dv == nil || service == "" {
		return nil
	}
	var images []catchrpc.ServiceImage
	for repoName, repoView := range dv.Images().All() {
		repo := string(repoName)
		svcName, err := parseRepo(repo)
		if err != nil || svcName != service {
			continue
		}
		refs := make(map[string]catchrpc.ServiceImageRef)
		repoStruct := repoView.AsStruct()
		if repoStruct != nil {
			for ref, manifest := range repoStruct.Refs {
				refs[string(ref)] = catchrpc.ServiceImageRef{
					Digest:    manifest.BlobHash,
					MediaType: manifest.ContentType,
				}
			}
		}
		images = append(images, catchrpc.ServiceImage{
			Repo: repo,
			Refs: refs,
		})
	}
	sort.Slice(images, func(i, j int) bool {
		return images[i].Repo < images[j].Repo
	})
	return images
}

func tailscaleHasValues(t *catchrpc.ServiceTailscale) bool {
	if t == nil {
		return false
	}
	if strings.TrimSpace(t.Interface) != "" {
		return true
	}
	if strings.TrimSpace(t.Version) != "" {
		return true
	}
	if strings.TrimSpace(t.ExitNode) != "" {
		return true
	}
	if strings.TrimSpace(t.StableID) != "" {
		return true
	}
	return len(t.Tags) > 0
}

func labelForIP(entry ifaceIP, svcIP, tsIface, macIface string, hasNetns bool, serviceType db.ServiceType) string {
	if svcIP != "" && entry.IP == svcIP {
		return "service"
	}
	if label := labelForInterface(entry.Interface, tsIface, macIface); label != "" {
		return label
	}
	if serviceType == db.ServiceTypeDockerCompose && hasNetns {
		return "docker"
	}
	if hasNetns {
		return "netns"
	}
	return "host"
}

func labelForInterface(iface, tsIface, macIface string) string {
	switch {
	case iface == "":
		return ""
	case tsIface != "" && iface == tsIface:
		return "tailscale"
	case strings.HasPrefix(iface, "yts-"), strings.HasPrefix(iface, "tailscale"):
		return "tailscale"
	case macIface != "" && iface == macIface:
		return "lan"
	case strings.HasPrefix(iface, "docker"), strings.HasPrefix(iface, "br-"):
		return "docker"
	default:
		return ""
	}
}
