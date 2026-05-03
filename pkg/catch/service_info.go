// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"fmt"
	"sort"
	"strings"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/db"
)

var listIPv4AddrsFn = listIPv4Addrs

func (s *Server) serviceInfo(sn string) (catchrpc.ServiceInfoResponse, error) {
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

	info := catchrpc.ServiceInfo{
		Name:             sn,
		ServiceType:      string(sv.ServiceType()),
		DataType:         string(ServiceDataTypeForService(sv)),
		Generation:       sv.Generation(),
		LatestGeneration: sv.LatestGeneration(),
		Paths: catchrpc.ServicePaths{
			Root: s.serviceRootDir(sn),
		},
	}

	info.Staged = serviceHasStagedChanges(sv)
	info.Network = serviceNetworkInfo(sv)
	ips, ipErr := s.serviceIPList(sn, sv)
	if ipErr != nil {
		info.Network.IPError = ipErr.Error()
	} else {
		info.Network.IPs = ips
	}
	info.Status = s.serviceStatusInfo(sn, sv)
	info.Images = serviceImageInfo(dv, sn)

	resp.Found = true
	resp.Info = info
	return resp, nil
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

func (s *Server) serviceIPList(sn string, sv db.ServiceView) ([]catchrpc.ServiceIP, error) {
	if sn == CatchService {
		return s.catchServiceIPList()
	}

	args, hasNetns := serviceIPListArgs(sn, sv)
	raw, err := listIPv4AddrsFn(args)
	if err != nil {
		return nil, err
	}
	return serviceIPListFromEntries(raw, serviceIPLabelConfigFromView(sv, hasNetns)), nil
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

func serviceIPListFromEntries(raw []ifaceIP, cfg serviceIPLabelConfig) []catchrpc.ServiceIP {
	out := make([]catchrpc.ServiceIP, 0, len(raw)+1)
	seenSvc := false
	for _, entry := range raw {
		label := labelForIP(entry, cfg.svcIP, cfg.tsIface, cfg.macIface, cfg.hasNetns, cfg.serviceType)
		out = append(out, catchrpc.ServiceIP{
			Label:     label,
			IP:        entry.IP,
			Interface: entry.Interface,
		})
		if entry.IP == cfg.svcIP && cfg.svcIP != "" {
			seenSvc = true
		}
	}
	if cfg.svcIP != "" && !seenSvc {
		out = append(out, catchrpc.ServiceIP{
			Label: "service",
			IP:    cfg.svcIP,
		})
	}
	return out
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
	if serviceType == db.ServiceTypeDockerCompose {
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
		return "macvlan"
	case strings.HasPrefix(iface, "docker"), strings.HasPrefix(iface, "br-"):
		return "docker"
	default:
		return ""
	}
}
