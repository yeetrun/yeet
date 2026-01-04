// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/shayne/yeet/pkg/catchrpc"
	"github.com/shayne/yeet/pkg/cli"
	"github.com/shayne/yeet/pkg/ftdetect"
)

type infoOutput struct {
	Service  string                       `json:"service"`
	Host     string                       `json:"host"`
	HostInfo *serverInfo                  `json:"hostInfo,omitempty"`
	Client   clientInfo                   `json:"client"`
	Server   catchrpc.ServiceInfoResponse `json:"server"`
}

type clientInfo struct {
	Found      bool                `json:"found"`
	Message    string              `json:"message,omitempty"`
	ConfigFile string              `json:"configFile,omitempty"`
	ConfigDir  string              `json:"configDir,omitempty"`
	Entry      *clientServiceEntry `json:"entry,omitempty"`
	Payload    *clientPayloadInfo  `json:"payload,omitempty"`
}

type clientServiceEntry struct {
	Name     string   `json:"name"`
	Host     string   `json:"host"`
	Type     string   `json:"type,omitempty"`
	Payload  string   `json:"payload,omitempty"`
	Schedule string   `json:"schedule,omitempty"`
	Args     []string `json:"args,omitempty"`
}

type clientPayloadInfo struct {
	Stored     string `json:"stored,omitempty"`
	Resolved   string `json:"resolved,omitempty"`
	Kind       string `json:"kind,omitempty"`
	SizeBytes  int64  `json:"sizeBytes,omitempty"`
	Exists     bool   `json:"exists,omitempty"`
	ImageRef   bool   `json:"imageRef,omitempty"`
	DetectErr  string `json:"detectError,omitempty"`
	ResolveErr string `json:"resolveError,omitempty"`
}

func handleInfoCommand(ctx context.Context, args []string, cfgLoc *projectConfigLocation) error {
	flags, _, err := cli.ParseInfo(args)
	if err != nil {
		return err
	}
	format := strings.TrimSpace(flags.Format)
	if format == "" {
		format = "plain"
	}
	switch format {
	case "plain", "text", "json", "json-pretty":
	default:
		return fmt.Errorf("unsupported format %q (expected plain, json, or json-pretty)", format)
	}

	service := getService()
	host := Host()

	var hostInfo serverInfo
	hostInfoErr := newRPCClient(host).Call(ctx, "catch.Info", nil, &hostInfo)

	serverInfoResp, err := newRPCClient(host).ServiceInfo(ctx, service)
	if err != nil {
		return err
	}

	client := buildClientInfo(cfgLoc, service, host, hostInfo, hostInfoErr)

	if format == "json" || format == "json-pretty" {
		out := infoOutput{
			Service: service,
			Host:    host,
			Client:  client,
			Server:  serverInfoResp,
		}
		if hostInfoErr == nil {
			out.HostInfo = &hostInfo
		}
		enc := json.NewEncoder(os.Stdout)
		if format == "json-pretty" {
			enc.SetIndent("", "  ")
		}
		return enc.Encode(out)
	}

	return renderInfoPlain(os.Stdout, service, host, hostInfoErr, hostInfo, client, serverInfoResp)
}

func buildClientInfo(cfgLoc *projectConfigLocation, service, host string, hostInfo serverInfo, hostInfoErr error) clientInfo {
	info := clientInfo{}
	if cfgLoc == nil || cfgLoc.Config == nil {
		info.Message = "no yeet.toml found"
		return info
	}
	info.ConfigFile = cfgLoc.Path
	info.ConfigDir = cfgLoc.Dir
	entry, ok := cfgLoc.Config.ServiceEntry(service, host)
	if !ok {
		info.Message = fmt.Sprintf("no entry for %s@%s", service, host)
		return info
	}
	info.Found = true
	info.Entry = &clientServiceEntry{
		Name:     entry.Name,
		Host:     entry.Host,
		Type:     entry.Type,
		Payload:  entry.Payload,
		Schedule: entry.Schedule,
		Args:     entry.Args,
	}
	info.Payload = inspectPayload(entry.Payload, cfgLoc.Dir, hostInfo, hostInfoErr)
	return info
}

func inspectPayload(payload, configDir string, hostInfo serverInfo, hostInfoErr error) *clientPayloadInfo {
	payload = strings.TrimSpace(payload)
	info := &clientPayloadInfo{Stored: payload}
	if payload == "" {
		info.ResolveErr = "no payload configured"
		return info
	}
	if looksLikeImageRef(payload) {
		info.Kind = "image"
		info.ImageRef = true
		return info
	}
	resolved := resolvePayloadPath(configDir, payload)
	info.Resolved = resolved
	st, err := os.Stat(resolved)
	if err != nil {
		info.ResolveErr = err.Error()
		return info
	}
	info.Exists = true
	info.SizeBytes = st.Size()
	if filepath.Base(resolved) == "Dockerfile" {
		info.Kind = "dockerfile"
		return info
	}
	goos, goarch := hostInfo.GOOS, hostInfo.GOARCH
	if hostInfoErr != nil || goos == "" || goarch == "" {
		goos, goarch = runtime.GOOS, runtime.GOARCH
	}
	ft, err := ftdetect.DetectFile(resolved, goos, goarch)
	if err != nil {
		info.DetectErr = err.Error()
		info.Kind = "unknown"
		return info
	}
	info.Kind = formatFileType(ft)
	return info
}

func formatFileType(ft ftdetect.FileType) string {
	switch ft {
	case ftdetect.Binary:
		return "binary"
	case ftdetect.Script:
		return "script"
	case ftdetect.DockerCompose:
		return "docker compose"
	case ftdetect.TypeScript:
		return "typescript"
	case ftdetect.Python:
		return "python"
	case ftdetect.Zstd:
		return "zstd archive"
	default:
		return "unknown"
	}
}

type infoSection struct {
	Title string
	Rows  []infoRow
}

type infoRow struct {
	Label string
	Value string
}

func renderInfoPlain(w io.Writer, service, host string, hostInfoErr error, hostInfo serverInfo, client clientInfo, server catchrpc.ServiceInfoResponse) error {
	sections := []infoSection{
		renderHostSection(host, hostInfoErr, hostInfo),
		renderServiceSection(service, host, client, server),
		renderClientSection(client),
		renderServerSection(server),
		renderNetworkSection(server),
		renderRuntimeSection(server),
		renderImagesSection(server),
	}
	for _, section := range sections {
		if len(section.Rows) == 0 {
			continue
		}
		fmt.Fprintln(w, section.Title)
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, row := range section.Rows {
			fmt.Fprintf(tw, "  %s:\t%s\n", row.Label, row.Value)
		}
		_ = tw.Flush()
		fmt.Fprintln(w)
	}
	return nil
}

func renderHostSection(host string, hostInfoErr error, hostInfo serverInfo) infoSection {
	rows := []infoRow{{Label: "Host", Value: host}}
	catchInfo := "unknown"
	if hostInfoErr != nil {
		catchInfo = fmt.Sprintf("unavailable (%v)", hostInfoErr)
	} else if hostInfo.Version != "" {
		catchInfo = fmt.Sprintf("%s (%s/%s)", hostInfo.Version, hostInfo.GOOS, hostInfo.GOARCH)
	}
	rows = append(rows, infoRow{Label: "Catch", Value: catchInfo})
	return infoSection{Title: "Host", Rows: rows}
}

func renderServiceSection(service, host string, client clientInfo, server catchrpc.ServiceInfoResponse) infoSection {
	rows := []infoRow{
		{Label: "Name", Value: service},
		{Label: "Host", Value: host},
	}
	serviceType := "unknown"
	if server.Found && server.Info.DataType != "" {
		serviceType = formatServiceDataType(server.Info.DataType)
	} else if client.Entry != nil && client.Entry.Type != "" {
		serviceType = formatClientServiceType(client.Entry.Type)
	}
	rows = append(rows, infoRow{Label: "Type", Value: serviceType})
	status := "unknown"
	if server.Found {
		status = summarizeStatus(server.Info)
	} else if server.Message != "" {
		status = fmt.Sprintf("unknown (%s)", server.Message)
	}
	rows = append(rows, infoRow{Label: "Status", Value: status})
	return infoSection{Title: "Service", Rows: rows}
}

func renderClientSection(client clientInfo) infoSection {
	rows := []infoRow{}
	if !client.Found {
		msg := client.Message
		if msg == "" {
			msg = "no local config"
		}
		rows = append(rows, infoRow{Label: "Config", Value: msg})
		return infoSection{Title: "Client (yeet.toml)", Rows: rows}
	}
	if client.Entry != nil && client.Entry.Host != "" {
		rows = append(rows, infoRow{Label: "Saved host", Value: client.Entry.Host})
	}
	if client.Entry != nil && client.Entry.Type != "" {
		rows = append(rows, infoRow{Label: "Saved type", Value: client.Entry.Type})
	}
	if client.Payload != nil {
		if client.Payload.Stored != "" {
			rows = append(rows, infoRow{Label: "Payload", Value: client.Payload.Stored})
		}
		if client.Payload.Kind != "" {
			rows = append(rows, infoRow{Label: "Payload type", Value: client.Payload.Kind})
		}
		if client.Payload.Exists {
			rows = append(rows, infoRow{Label: "Payload size", Value: formatBytes(client.Payload.SizeBytes)})
		}
		if client.Payload.ResolveErr != "" {
			rows = append(rows, infoRow{Label: "Payload error", Value: client.Payload.ResolveErr})
		} else if client.Payload.DetectErr != "" {
			rows = append(rows, infoRow{Label: "Payload detect", Value: client.Payload.DetectErr})
		}
	}
	if client.Entry != nil {
		if len(client.Entry.Args) > 0 {
			rows = append(rows, infoRow{Label: "Payload args", Value: strings.Join(client.Entry.Args, " ")})
		}
		if client.Entry.Schedule != "" {
			rows = append(rows, infoRow{Label: "Schedule", Value: client.Entry.Schedule})
		}
	}
	return infoSection{Title: "Client (yeet.toml)", Rows: rows}
}

func renderServerSection(server catchrpc.ServiceInfoResponse) infoSection {
	rows := []infoRow{}
	if !server.Found {
		msg := server.Message
		if msg == "" {
			msg = "not installed"
		}
		rows = append(rows, infoRow{Label: "Status", Value: msg})
		return infoSection{Title: "Server (catch)", Rows: rows}
	}
	info := server.Info
	if info.ServiceType != "" {
		rows = append(rows, infoRow{Label: "Service type", Value: info.ServiceType})
	}
	if info.Generation != 0 || info.LatestGeneration != 0 {
		rows = append(rows, infoRow{Label: "Generation", Value: fmt.Sprintf("%d (latest %d)", info.Generation, info.LatestGeneration)})
	}
	if info.Staged {
		rows = append(rows, infoRow{Label: "Staged changes", Value: "yes"})
	} else {
		rows = append(rows, infoRow{Label: "Staged changes", Value: "none"})
	}
	if info.Paths.Root != "" {
		rows = append(rows, infoRow{Label: "Root dir", Value: info.Paths.Root})
	}
	return infoSection{Title: "Server (catch)", Rows: rows}
}

func renderNetworkSection(server catchrpc.ServiceInfoResponse) infoSection {
	if !server.Found {
		return infoSection{Title: "Network", Rows: nil}
	}
	net := server.Info.Network
	rows := []infoRow{}
	if net.IPError != "" {
		rows = append(rows, infoRow{Label: "IPs", Value: fmt.Sprintf("unavailable (%s)", net.IPError)})
	} else if len(net.IPs) > 0 || net.SvcIP != "" {
		groups := buildIPGroups(net.IPs, net.SvcIP)
		if len(groups) == 0 {
			rows = append(rows, infoRow{Label: "IPs", Value: "none"})
		} else {
			rows = append(rows, infoRow{Label: "IPs", Value: ""})
			for _, group := range groups {
				rows = append(rows, infoRow{
					Label: "  " + group.label,
					Value: strings.Join(group.ips, ", "),
				})
			}
		}
	} else {
		rows = append(rows, infoRow{Label: "IPs", Value: "none"})
	}
	if net.Tailscale != nil {
		ts := net.Tailscale
		desc := ts.Interface
		if desc == "" {
			desc = "enabled"
		}
		if ts.Version != "" {
			desc = fmt.Sprintf("%s (ver %s)", desc, ts.Version)
		}
		if len(ts.Tags) > 0 {
			desc = fmt.Sprintf("%s, tags: %s", desc, strings.Join(ts.Tags, ", "))
		}
		if ts.ExitNode != "" {
			desc = fmt.Sprintf("%s, exit: %s", desc, ts.ExitNode)
		}
		rows = append(rows, infoRow{Label: "Tailscale", Value: desc})
	} else {
		rows = append(rows, infoRow{Label: "Tailscale", Value: "disabled"})
	}
	if net.Macvlan != nil {
		mv := net.Macvlan
		desc := mv.Interface
		if desc == "" {
			desc = "enabled"
		}
		parts := []string{desc}
		if mv.Parent != "" {
			parts = append(parts, "parent "+mv.Parent)
		}
		if mv.VLAN != 0 {
			parts = append(parts, fmt.Sprintf("vlan %d", mv.VLAN))
		}
		if mv.Mac != "" {
			parts = append(parts, "mac "+mv.Mac)
		}
		rows = append(rows, infoRow{Label: "Macvlan", Value: strings.Join(parts, ", ")})
	} else {
		rows = append(rows, infoRow{Label: "Macvlan", Value: "disabled"})
	}
	return infoSection{Title: "Network", Rows: rows}
}

func renderRuntimeSection(server catchrpc.ServiceInfoResponse) infoSection {
	if !server.Found {
		return infoSection{Title: "Runtime", Rows: nil}
	}
	status := server.Info.Status
	rows := []infoRow{}
	if status.Error != "" {
		rows = append(rows, infoRow{Label: "Status", Value: status.Error})
		return infoSection{Title: "Runtime", Rows: rows}
	}
	if len(status.Components) == 0 {
		rows = append(rows, infoRow{Label: "Status", Value: "unknown"})
		return infoSection{Title: "Runtime", Rows: rows}
	}
	for _, component := range status.Components {
		label := component.Name
		if label == "" {
			label = "component"
		}
		rows = append(rows, infoRow{Label: label, Value: component.Status})
	}
	return infoSection{Title: "Runtime", Rows: rows}
}

func renderImagesSection(server catchrpc.ServiceInfoResponse) infoSection {
	if !server.Found {
		return infoSection{Title: "Images", Rows: nil}
	}
	if len(server.Info.Images) == 0 {
		return infoSection{Title: "Images", Rows: nil}
	}
	rows := []infoRow{}
	for _, image := range server.Info.Images {
		refKeys := sortedStringKeys(image.Refs)
		refs := make([]string, 0, len(refKeys))
		for _, key := range refKeys {
			ref := image.Refs[key]
			if ref.Digest != "" {
				refs = append(refs, fmt.Sprintf("%s=%s", key, ref.Digest))
			} else {
				refs = append(refs, key)
			}
		}
		value := strings.Join(refs, ", ")
		rows = append(rows, infoRow{Label: image.Repo, Value: value})
	}
	return infoSection{Title: "Images", Rows: rows}
}

func sortedStringKeys[T any](m map[string]T) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func summarizeStatus(info catchrpc.ServiceInfo) string {
	status := info.Status
	if status.Error != "" {
		return fmt.Sprintf("unknown (%s)", status.Error)
	}
	total := len(status.Components)
	if total == 0 {
		return "unknown"
	}
	if total == 1 {
		if status.Components[0].Status != "" {
			return status.Components[0].Status
		}
		return "unknown"
	}
	counts := make(map[string]int)
	for _, component := range status.Components {
		if component.Status == "" {
			counts["unknown"]++
			continue
		}
		counts[component.Status]++
	}
	if counts["running"] == total {
		return fmt.Sprintf("running (%d)", total)
	}
	if counts["stopped"] == total {
		return fmt.Sprintf("stopped (%d)", total)
	}
	if counts["starting"] == total {
		return fmt.Sprintf("starting (%d)", total)
	}
	if counts["stopping"] == total {
		return fmt.Sprintf("stopping (%d)", total)
	}
	if counts["running"] > 0 {
		return fmt.Sprintf("partial (%d/%d)", counts["running"], total)
	}
	return fmt.Sprintf("mixed (%d)", total)
}

func formatServiceDataType(dt string) string {
	switch dt {
	case "docker":
		return "docker compose service"
	case "service":
		return "systemd service"
	case "cron":
		return "cron service"
	case "binary":
		return "systemd binary service"
	case "typescript":
		return "typescript service"
	case "python":
		return "python service"
	default:
		return dt
	}
}

func formatClientServiceType(t string) string {
	switch t {
	case serviceTypeCron:
		return "cron service (local config)"
	case serviceTypeRun:
		return "run service (local config)"
	default:
		return t
	}
}

func formatBytes(n int64) string {
	if n < 0 {
		return "unknown"
	}
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	value := float64(n)
	for _, unit := range units {
		value /= 1024
		if value < 1024 {
			return fmt.Sprintf("%.1f %s", value, unit)
		}
	}
	return fmt.Sprintf("%.1f %s", value, units[len(units)-1])
}

type ipGroup struct {
	label string
	base  string
	ips   []string
}

func buildIPGroups(entries []catchrpc.ServiceIP, svcIP string) []ipGroup {
	if len(entries) == 0 && svcIP == "" {
		return nil
	}
	seen := make(map[string]struct{})
	ordered := []ipGroup{}
	index := make(map[string]int)
	addGroup := func(label, base, ip string) {
		if ip == "" {
			return
		}
		key := label + ":" + ip
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		if _, ok := index[label]; !ok {
			index[label] = len(ordered)
			ordered = append(ordered, ipGroup{label: label, base: base})
		}
		ordered[index[label]].ips = append(ordered[index[label]].ips, ip)
	}

	for _, entry := range entries {
		base := strings.TrimSpace(entry.Label)
		if base == "" {
			base = "ip"
		}
		display := base
		if base != "service" && entry.Interface != "" {
			display = fmt.Sprintf("%s (%s)", base, entry.Interface)
		}
		addGroup(display, base, entry.IP)
	}
	if svcIP != "" {
		addGroup("service", "service", svcIP)
	}

	sort.SliceStable(ordered, func(i, j int) bool {
		priority := func(label string) int {
			switch label {
			case "service":
				return 0
			case "tailscale":
				return 1
			case "macvlan":
				return 2
			case "docker":
				return 3
			case "host":
				return 4
			case "netns":
				return 5
			default:
				return 6
			}
		}
		pi := priority(ordered[i].base)
		pj := priority(ordered[j].base)
		if pi != pj {
			return pi < pj
		}
		return ordered[i].label < ordered[j].label
	})

	return ordered
}
