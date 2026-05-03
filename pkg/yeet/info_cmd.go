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

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/ftdetect"
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
	EnvFile  string   `json:"envFile,omitempty"`
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
	format, err := normalizeInfoFormat(flags.Format)
	if err != nil {
		return err
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

	if isInfoJSONFormat(format) {
		out := newInfoOutput(service, host, hostInfo, hostInfoErr, client, serverInfoResp)
		return encodeInfoOutput(os.Stdout, format, out)
	}

	return renderInfoPlain(os.Stdout, service, host, hostInfoErr, hostInfo, client, serverInfoResp)
}

func normalizeInfoFormat(format string) (string, error) {
	format = strings.TrimSpace(format)
	if format == "" {
		return "plain", nil
	}
	switch format {
	case "plain", "text", "json", "json-pretty":
		return format, nil
	default:
		return "", fmt.Errorf("unsupported format %q (expected plain, json, or json-pretty)", format)
	}
}

func isInfoJSONFormat(format string) bool {
	return format == "json" || format == "json-pretty"
}

func newInfoOutput(service, host string, hostInfo serverInfo, hostInfoErr error, client clientInfo, server catchrpc.ServiceInfoResponse) infoOutput {
	out := infoOutput{
		Service: service,
		Host:    host,
		Client:  client,
		Server:  server,
	}
	if hostInfoErr == nil {
		out.HostInfo = &hostInfo
	}
	return out
}

func encodeInfoOutput(w io.Writer, format string, out infoOutput) error {
	enc := json.NewEncoder(w)
	if format == "json-pretty" {
		enc.SetIndent("", "  ")
	}
	return enc.Encode(out)
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
		EnvFile:  entry.EnvFile,
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
		if _, err := fmt.Fprintln(w, section.Title); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, row := range section.Rows {
			if _, err := fmt.Fprintf(tw, "  %s:\t%s\n", row.Label, row.Value); err != nil {
				return err
			}
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
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
	return infoSection{Title: "Client (yeet.toml)", Rows: clientConfigRows(client)}
}

func clientConfigRows(client clientInfo) []infoRow {
	if !client.Found {
		msg := client.Message
		if msg == "" {
			msg = "no local config"
		}
		return []infoRow{{Label: "Config", Value: msg}}
	}

	rows := clientSavedRows(client.Entry)
	rows = append(rows, clientPayloadRows(client.Payload)...)
	rows = append(rows, clientEntryMetadataRows(client.Entry)...)
	return rows
}

func clientSavedRows(entry *clientServiceEntry) []infoRow {
	if entry == nil {
		return nil
	}
	rows := []infoRow{}
	if entry.Host != "" {
		rows = append(rows, infoRow{Label: "Saved host", Value: entry.Host})
	}
	if entry.Type != "" {
		rows = append(rows, infoRow{Label: "Saved type", Value: entry.Type})
	}
	return rows
}

func clientPayloadRows(payload *clientPayloadInfo) []infoRow {
	if payload == nil {
		return nil
	}
	rows := []infoRow{}
	if payload.Stored != "" {
		rows = append(rows, infoRow{Label: "Payload", Value: payload.Stored})
	}
	if payload.Kind != "" {
		rows = append(rows, infoRow{Label: "Payload type", Value: payload.Kind})
	}
	if payload.Exists {
		rows = append(rows, infoRow{Label: "Payload size", Value: formatBytes(payload.SizeBytes)})
	}
	if row, ok := clientPayloadErrorRow(payload); ok {
		rows = append(rows, row)
	}
	return rows
}

func clientPayloadErrorRow(payload *clientPayloadInfo) (infoRow, bool) {
	if payload.ResolveErr != "" {
		return infoRow{Label: "Payload error", Value: payload.ResolveErr}, true
	}
	if payload.DetectErr != "" {
		return infoRow{Label: "Payload detect", Value: payload.DetectErr}, true
	}
	return infoRow{}, false
}

func clientEntryMetadataRows(entry *clientServiceEntry) []infoRow {
	if entry == nil {
		return nil
	}
	rows := []infoRow{}
	if entry.EnvFile != "" {
		rows = append(rows, infoRow{Label: "Env file", Value: entry.EnvFile})
	}
	if len(entry.Args) > 0 {
		rows = append(rows, infoRow{Label: "Payload args", Value: strings.Join(entry.Args, " ")})
	}
	if entry.Schedule != "" {
		rows = append(rows, infoRow{Label: "Schedule", Value: entry.Schedule})
	}
	return rows
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
	rows := networkIPRows(net)
	rows = append(rows, infoRow{Label: "Tailscale", Value: describeTailscale(net.Tailscale)})
	rows = append(rows, infoRow{Label: "Macvlan", Value: describeMacvlan(net.Macvlan)})
	return infoSection{Title: "Network", Rows: rows}
}

func networkIPRows(net catchrpc.ServiceNetwork) []infoRow {
	if net.IPError != "" {
		return []infoRow{{Label: "IPs", Value: fmt.Sprintf("unavailable (%s)", net.IPError)}}
	}
	if len(net.IPs) == 0 && net.SvcIP == "" {
		return []infoRow{{Label: "IPs", Value: "none"}}
	}

	groups := buildIPGroups(net.IPs, net.SvcIP)
	if len(groups) == 0 {
		return []infoRow{{Label: "IPs", Value: "none"}}
	}

	rows := []infoRow{{Label: "IPs", Value: ""}}
	for _, group := range groups {
		rows = append(rows, infoRow{
			Label: "  " + group.label,
			Value: strings.Join(group.ips, ", "),
		})
	}
	return rows
}

func describeTailscale(ts *catchrpc.ServiceTailscale) string {
	if ts == nil {
		return "disabled"
	}
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
	return desc
}

func describeMacvlan(mv *catchrpc.ServiceMacvlan) string {
	if mv == nil {
		return "disabled"
	}
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
	return strings.Join(parts, ", ")
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
	components := status.Components
	total := len(components)
	switch total {
	case 0:
		return "unknown"
	case 1:
		return componentStatusOrUnknown(components[0].Status)
	}

	counts := countComponentStatuses(components)
	if summary, ok := uniformComponentStatusSummary(counts, total); ok {
		return summary
	}
	if counts["running"] > 0 {
		return fmt.Sprintf("partial (%d/%d)", counts["running"], total)
	}
	return fmt.Sprintf("mixed (%d)", total)
}

func componentStatusOrUnknown(status string) string {
	if status == "" {
		return "unknown"
	}
	return status
}

func countComponentStatuses(components []catchrpc.ServiceComponentStatus) map[string]int {
	counts := make(map[string]int)
	for _, component := range components {
		counts[componentStatusOrUnknown(component.Status)]++
	}
	return counts
}

func uniformComponentStatusSummary(counts map[string]int, total int) (string, bool) {
	for _, status := range []string{"running", "stopped", "starting", "stopping"} {
		if counts[status] == total {
			return fmt.Sprintf("%s (%d)", status, total), true
		}
	}
	return "", false
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

type ipGroupBuilder struct {
	seen    map[string]struct{}
	ordered []ipGroup
	index   map[string]int
}

func newIPGroupBuilder() *ipGroupBuilder {
	return &ipGroupBuilder{
		seen:  make(map[string]struct{}),
		index: make(map[string]int),
	}
}

func buildIPGroups(entries []catchrpc.ServiceIP, svcIP string) []ipGroup {
	if len(entries) == 0 && svcIP == "" {
		return nil
	}

	builder := newIPGroupBuilder()
	for _, entry := range entries {
		label, base := ipGroupLabel(entry)
		builder.add(label, base, entry.IP)
	}
	builder.add("service", "service", svcIP)

	ordered := builder.groups()
	sort.SliceStable(ordered, func(i, j int) bool {
		return ipGroupLess(ordered[i], ordered[j])
	})

	return ordered
}

func (b *ipGroupBuilder) add(label, base, ip string) {
	if ip == "" {
		return
	}
	key := label + ":" + ip
	if _, ok := b.seen[key]; ok {
		return
	}
	b.seen[key] = struct{}{}
	if _, ok := b.index[label]; !ok {
		b.index[label] = len(b.ordered)
		b.ordered = append(b.ordered, ipGroup{label: label, base: base})
	}
	b.ordered[b.index[label]].ips = append(b.ordered[b.index[label]].ips, ip)
}

func (b *ipGroupBuilder) groups() []ipGroup {
	return b.ordered
}

func ipGroupLabel(entry catchrpc.ServiceIP) (string, string) {
	base := strings.TrimSpace(entry.Label)
	if base == "" {
		base = "ip"
	}
	label := base
	if base != "service" && entry.Interface != "" {
		label = fmt.Sprintf("%s (%s)", base, entry.Interface)
	}
	return label, base
}

func ipGroupLess(left, right ipGroup) bool {
	leftPriority := ipGroupPriority(left.base)
	rightPriority := ipGroupPriority(right.base)
	if leftPriority != rightPriority {
		return leftPriority < rightPriority
	}
	return left.label < right.label
}

func ipGroupPriority(base string) int {
	for priority, label := range []string{"service", "tailscale", "macvlan", "docker", "host", "netns"} {
		if base == label {
			return priority
		}
	}
	return 6
}
