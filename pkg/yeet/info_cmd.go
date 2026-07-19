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

type hostInfoOutput struct {
	Host         string                        `json:"host"`
	HostInfo     *serverInfo                   `json:"hostInfo,omitempty"`
	CatchService *catchrpc.ServiceInfoResponse `json:"catchService,omitempty"`
	Inventory    hostInfoInventory             `json:"inventory"`
	Warnings     []string                      `json:"warnings,omitempty"`
}

type hostInfoInventory struct {
	Services hostInventoryCounts `json:"services"`
	VMs      hostInventoryCounts `json:"vms"`
}

type hostInventoryCounts struct {
	Total     int `json:"total"`
	Running   int `json:"running"`
	Stopped   int `json:"stopped"`
	Unhealthy int `json:"unhealthy"`
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
	Name        string   `json:"name"`
	Host        string   `json:"host"`
	Type        string   `json:"type,omitempty"`
	Payload     string   `json:"payload,omitempty"`
	PayloadKind string   `json:"payloadKind,omitempty"`
	EnvFile     string   `json:"envFile,omitempty"`
	Schedule    string   `json:"schedule,omitempty"`
	Args        []string `json:"args,omitempty"`
	Ports       []string `json:"ports,omitempty"`
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

var fetchInfoHostInfoFn = fetchInfoHostInfo
var fetchInfoServiceInfoFn = fetchInfoServiceInfo

func handleInfoCommand(ctx context.Context, args []string, cfgLoc *projectConfigLocation) error {
	flags, remaining, err := cli.ParseInfo(args)
	if err != nil {
		return err
	}
	format, err := normalizeInfoFormat(flags.Format)
	if err != nil {
		return err
	}

	service, err := infoServiceFromArgs(remaining)
	if err != nil {
		return err
	}
	host := Host()
	if service == "" {
		return handleHostInfoCommand(ctx, host, format)
	}

	serverInfoResp, err := fetchInfoServiceInfoFn(ctx, host, service)
	if err != nil {
		return err
	}
	if !serverInfoResp.Found {
		return fmt.Errorf("service %q not found on %s", service, host)
	}

	hostInfo, hostInfoErr := fetchInfoHostInfoFn(ctx, host)

	client := buildClientInfo(cfgLoc, service, host, hostInfo, hostInfoErr)

	if isInfoJSONFormat(format) {
		out := newInfoOutput(service, host, hostInfo, hostInfoErr, client, serverInfoResp)
		return encodeInfoOutput(os.Stdout, format, out)
	}

	return renderInfoPlain(os.Stdout, service, host, hostInfoErr, hostInfo, client, serverInfoResp)
}

func infoServiceFromArgs(args []string) (string, error) {
	args = trimEmptyInfoArgs(args)
	if serviceOverride != "" {
		if len(args) > 0 {
			return "", fmt.Errorf("info accepts one service name")
		}
		return serviceOverride, nil
	}
	if len(args) == 0 {
		return "", nil
	}
	if len(args) > 1 {
		return "", fmt.Errorf("info accepts one service name")
	}
	return args[0], nil
}

func trimEmptyInfoArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg != "" {
			out = append(out, arg)
		}
	}
	return out
}

func handleHostInfoCommand(ctx context.Context, host, format string) error {
	hostInfo, err := fetchInfoHostInfoFn(ctx, host)
	if err != nil {
		return err
	}
	catchService, catchErr := fetchInfoServiceInfoFn(ctx, host, catchServiceName)
	statuses, statusErr := fetchStatusForHostFn(ctx, host, cli.StatusFlags{})

	out := newHostInfoOutput(host, hostInfo, &catchService, statuses)
	out.Warnings = append(out.Warnings, hostInfoWarning("catch service info", catchErr)...)
	out.Warnings = append(out.Warnings, hostInfoWarning("status", statusErr)...)

	if isInfoJSONFormat(format) {
		return encodeInfoOutput(os.Stdout, format, out)
	}
	return renderHostInfoPlain(os.Stdout, out)
}

func fetchInfoHostInfo(ctx context.Context, host string) (serverInfo, error) {
	var info serverInfo
	err := newRPCClient(host).Call(ctx, "catch.Info", nil, &info)
	return info, err
}

func fetchInfoServiceInfo(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
	return newRPCClient(host).ServiceInfo(ctx, service)
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

func newHostInfoOutput(host string, hostInfo serverInfo, catchService *catchrpc.ServiceInfoResponse, statuses []statusService) hostInfoOutput {
	return hostInfoOutput{
		Host:         host,
		HostInfo:     &hostInfo,
		CatchService: catchService,
		Inventory:    buildHostInventory(statuses),
	}
}

func hostInfoWarning(label string, err error) []string {
	if err == nil {
		return nil
	}
	return []string{fmt.Sprintf("%s unavailable: %v", label, err)}
}

func encodeInfoOutput(w io.Writer, format string, out any) error {
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
		Name:        entry.Name,
		Host:        entry.Host,
		Type:        entry.Type,
		Payload:     entry.Payload,
		PayloadKind: entry.PayloadKind,
		EnvFile:     entry.EnvFile,
		Schedule:    entry.Schedule,
		Args:        entry.Args,
		Ports:       entry.Ports,
	}
	info.Payload = inspectPayloadWithKind(entry.Payload, entry.PayloadKind, cfgLoc.Dir, hostInfo, hostInfoErr)
	return info
}

func inspectPayload(payload, configDir string, hostInfo serverInfo, hostInfoErr error) *clientPayloadInfo {
	return inspectPayloadWithKind(payload, "", configDir, hostInfo, hostInfoErr)
}

func inspectPayloadWithKind(payload, payloadKind, configDir string, hostInfo serverInfo, hostInfoErr error) *clientPayloadInfo {
	payload = strings.TrimSpace(payload)
	info := &clientPayloadInfo{Stored: payload}
	if payload == "" {
		info.ResolveErr = "no payload configured"
		return info
	}
	if classifyFilelessPayload(info, payload, payloadKind) {
		return info
	}
	return inspectLocalPayload(info, payload, configDir, hostInfo, hostInfoErr)
}

func classifyFilelessPayload(info *clientPayloadInfo, payload, payloadKind string) bool {
	switch {
	case strings.TrimSpace(payloadKind) == "local-image":
		info.Kind = "local image"
		info.ImageRef = true
	case strings.TrimSpace(payloadKind) == serviceTypeVM || isVMPayload(payload):
		info.Kind = serviceTypeVM
	case looksLikeImageRef(payload):
		info.Kind = "image"
		info.ImageRef = true
	default:
		return false
	}
	return true
}

func inspectLocalPayload(info *clientPayloadInfo, payload, configDir string, hostInfo serverInfo, hostInfoErr error) *clientPayloadInfo {
	info.Resolved = resolvePayloadPath(configDir, payload)
	st, err := os.Stat(info.Resolved)
	if err != nil {
		info.ResolveErr = err.Error()
		return info
	}
	info.Exists = true
	info.SizeBytes = st.Size()
	if filepath.Base(info.Resolved) == "Dockerfile" {
		info.Kind = "dockerfile"
		return info
	}
	goos, goarch := hostInfo.GOOS, hostInfo.GOARCH
	if hostInfoErr != nil || goos == "" || goarch == "" {
		goos, goarch = runtime.GOOS, runtime.GOARCH
	}
	ft, err := ftdetect.DetectFile(info.Resolved, goos, goarch)
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
		renderVMSection(server),
		renderClientSection(client),
		renderServerSection(server),
		renderNetworkSection(server),
		renderRuntimeSection(service, server),
		renderImagesSection(server),
	}
	return renderInfoSections(w, sections)
}

func renderHostInfoPlain(w io.Writer, out hostInfoOutput) error {
	return renderInfoSections(w, []infoSection{
		renderHostInfoHostSection(out),
		renderHostInfoInventorySection(out.Inventory),
		renderHostInfoWarningsSection(out.Warnings),
	})
}

func renderInfoSections(w io.Writer, sections []infoSection) error {
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

func renderHostInfoHostSection(out hostInfoOutput) infoSection {
	rows := []infoRow{{Label: "Host", Value: out.Host}}
	if out.HostInfo == nil {
		rows = append(rows, infoRow{Label: "Catch", Value: "unknown"})
		return infoSection{Title: "Host", Rows: rows}
	}
	rows = append(rows, infoRow{Label: "Catch", Value: formatCatchInfo(*out.HostInfo)})
	if out.HostInfo.RootDir != "" {
		rows = append(rows, infoRow{Label: "Data dir", Value: out.HostInfo.RootDir})
	}
	if out.HostInfo.ServicesDir != "" {
		rows = append(rows, infoRow{Label: "Services root", Value: out.HostInfo.ServicesDir})
	}
	if root := catchServiceRoot(out.CatchService); root != "" {
		rows = append(rows, infoRow{Label: "Catch root", Value: formatPathWithZFS(root, catchServiceRootZFS(out.CatchService))})
	}
	return infoSection{Title: "Host", Rows: rows}
}

func renderHostSection(host string, hostInfoErr error, hostInfo serverInfo) infoSection {
	rows := []infoRow{{Label: "Host", Value: host}}
	catchInfo := "unknown"
	if hostInfoErr != nil {
		catchInfo = fmt.Sprintf("unavailable (%v)", hostInfoErr)
	} else if hostInfo.Version != "" {
		catchInfo = formatCatchInfo(hostInfo)
	}
	rows = append(rows, infoRow{Label: "Catch", Value: catchInfo})
	return infoSection{Title: "Host", Rows: rows}
}

func formatCatchInfo(hostInfo serverInfo) string {
	if hostInfo.Version == "" {
		return "unknown"
	}
	if hostInfo.GOOS == "" || hostInfo.GOARCH == "" {
		return hostInfo.Version
	}
	return fmt.Sprintf("%s (%s/%s)", hostInfo.Version, hostInfo.GOOS, hostInfo.GOARCH)
}

func catchServiceRoot(resp *catchrpc.ServiceInfoResponse) string {
	if resp == nil || !resp.Found {
		return ""
	}
	paths := resp.Info.Paths
	switch {
	case paths.ServiceRoot != "":
		return paths.ServiceRoot
	case paths.EffectiveRoot != "":
		return paths.EffectiveRoot
	default:
		return paths.Root
	}
}

func catchServiceRootZFS(resp *catchrpc.ServiceInfoResponse) string {
	if resp == nil || !resp.Found {
		return ""
	}
	return resp.Info.Paths.ServiceRootZFS
}

func formatPathWithZFS(path, dataset string) string {
	if path == "" || dataset == "" {
		return path
	}
	return fmt.Sprintf("%s (zfs %s)", path, dataset)
}

func renderHostInfoInventorySection(inventory hostInfoInventory) infoSection {
	return infoSection{
		Title: "Inventory",
		Rows: []infoRow{
			{Label: "Services", Value: formatHostInventoryCounts(inventory.Services)},
			{Label: "VMs", Value: formatHostInventoryCounts(inventory.VMs)},
		},
	}
}

func renderHostInfoWarningsSection(warnings []string) infoSection {
	rows := make([]infoRow, 0, len(warnings))
	for _, warning := range warnings {
		if strings.TrimSpace(warning) != "" {
			rows = append(rows, infoRow{Label: "Warning", Value: warning})
		}
	}
	return infoSection{Title: "Warnings", Rows: rows}
}

func buildHostInventory(statuses []statusService) hostInfoInventory {
	var inventory hostInfoInventory
	for _, status := range statuses {
		state := classifyHostStatusService(status)
		addHostInventoryCount(&inventory.Services, state)
		if strings.TrimSpace(status.ServiceType) == serviceTypeVM {
			addHostInventoryCount(&inventory.VMs, state)
		}
	}
	return inventory
}

func classifyHostStatusService(status statusService) string {
	if len(status.Components) == 0 {
		return "unhealthy"
	}
	running := 0
	stopped := 0
	for _, component := range status.Components {
		switch strings.ToLower(strings.TrimSpace(component.Status)) {
		case "running":
			running++
		case "stopped":
			stopped++
		}
	}
	switch total := len(status.Components); {
	case running == total:
		return "running"
	case stopped == total:
		return "stopped"
	default:
		return "unhealthy"
	}
}

func addHostInventoryCount(counts *hostInventoryCounts, state string) {
	counts.Total++
	switch state {
	case "running":
		counts.Running++
	case "stopped":
		counts.Stopped++
	default:
		counts.Unhealthy++
	}
}

func formatHostInventoryCounts(counts hostInventoryCounts) string {
	return fmt.Sprintf("%d total, %d running, %d stopped, %d unhealthy", counts.Total, counts.Running, counts.Stopped, counts.Unhealthy)
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

func renderVMSection(server catchrpc.ServiceInfoResponse) infoSection {
	if !server.Found || server.Info.VM == nil {
		return infoSection{Title: "VM", Rows: nil}
	}
	vm := server.Info.VM
	rows := vmInfoRows(vm)
	return infoSection{Title: "VM", Rows: rows}
}

func vmInfoRows(vm *catchrpc.ServiceVM) []infoRow {
	candidates := []infoRow{
		{Label: "Runtime", Value: vm.Runtime},
		{Label: "VMM isolation", Value: formatVMMIsolation(vm.VMMIsolation)},
		{Label: "Image", Value: formatVMImage(vm)},
		{Label: "CPU", Value: formatVMCPU(vm.CPUs)},
		{Label: "Memory", Value: formatOptionalBytes(vm.MemoryBytes)},
		{Label: "Balloon", Value: formatVMBalloon(vm.Balloon)},
		{Label: "Disk", Value: formatVMDisk(vm)},
		{Label: "Console", Value: formatOptionalVMConsole(vm.Console)},
		{Label: "SSH", Value: formatVMSSH(vm.SSH)},
		{Label: "Provisioning", Value: vm.SetupState},
	}
	rows := make([]infoRow, 0, len(candidates))
	for _, row := range candidates {
		if row.Value != "" {
			rows = append(rows, row)
		}
	}
	return rows
}

func formatVMMIsolation(value string) string {
	switch strings.TrimSpace(value) {
	case "jailer-pending-restart":
		return "jailer (pending restart)"
	case "jailer":
		return "jailer"
	default:
		return strings.TrimSpace(value)
	}
}

func formatVMCPU(cpus int) string {
	if cpus == 0 {
		return ""
	}
	return fmt.Sprintf("%d", cpus)
}

func formatVMBalloon(balloon catchrpc.ServiceVMBalloon) string {
	mode := strings.TrimSpace(balloon.Mode)
	if mode == "" {
		return ""
	}
	if mode != "auto" {
		return mode
	}
	min := strings.TrimSpace(balloon.MinMemory)
	if min == "" && balloon.MinBytes > 0 {
		min = formatBytesCompact(balloon.MinBytes)
	}
	if min == "" {
		return mode
	}
	return mode + ", floor " + min
}

func formatBytesCompact(bytes int64) string {
	if bytes <= 0 {
		return ""
	}
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	value := float64(bytes)
	unit := "B"
	for _, next := range []string{"KB", "MB", "GB", "TB", "PB"} {
		value /= 1024
		unit = next
		if value < 1024 {
			break
		}
	}
	formatted := fmt.Sprintf("%.1f %s", value, unit)
	return strings.Replace(formatted, ".0 "+unit, " "+unit, 1)
}

func formatOptionalBytes(bytes int64) string {
	if bytes == 0 {
		return ""
	}
	return formatBytes(bytes)
}

func formatOptionalVMConsole(console *catchrpc.ServiceVMConsole) string {
	if console == nil {
		return ""
	}
	return formatVMConsole(console)
}

func formatVMImage(vm *catchrpc.ServiceVM) string {
	if vm == nil {
		return ""
	}
	switch {
	case vm.Image != "" && vm.ImageVersion != "":
		return fmt.Sprintf("%s (%s)", vm.Image, vm.ImageVersion)
	case vm.Image != "":
		return vm.Image
	case vm.ImageVersion != "":
		return vm.ImageVersion
	default:
		return ""
	}
}

func formatVMDisk(vm *catchrpc.ServiceVM) string {
	if vm == nil {
		return ""
	}
	parts := []string{}
	if vm.DiskBytes != 0 {
		parts = append(parts, formatBytes(vm.DiskBytes))
	}
	if vm.DiskBackend != "" {
		parts = append(parts, vm.DiskBackend)
	}
	if vm.DiskPath != "" {
		parts = append(parts, vm.DiskPath)
	}
	return strings.Join(parts, ", ")
}

func formatVMConsole(console *catchrpc.ServiceVMConsole) string {
	if console == nil {
		return ""
	}
	if !console.Available {
		return "unavailable"
	}
	if console.SocketPath != "" {
		return "available (" + console.SocketPath + ")"
	}
	return "available"
}

func formatVMSSH(ssh *catchrpc.ServiceVMSSH) string {
	if ssh == nil {
		return ""
	}
	if ssh.User != "" && ssh.Host != "" {
		return ssh.User + "@" + ssh.Host
	}
	if ssh.Host != "" {
		return ssh.Host
	}
	if ssh.User != "" {
		return ssh.User
	}
	return ""
}

func renderClientSection(client clientInfo) infoSection {
	return infoSection{Title: "Client (yeet.toml)", Rows: clientConfigRows(client)}
}

func clientConfigRows(client clientInfo) []infoRow {
	if !client.Found {
		return nil
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
	if len(entry.Ports) > 0 {
		rows = append(rows, infoRow{Label: "Published ports", Value: strings.Join(entry.Ports, ", ")})
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
	if shouldRenderServiceBackend(info) {
		rows = append(rows, infoRow{Label: "Backend", Value: formatServiceBackend(info.ServiceType)})
	}
	if shouldRenderServiceGeneration(info) {
		rows = append(rows, infoRow{Label: "Generation", Value: formatServiceGeneration(info)})
	}
	if info.Staged {
		rows = append(rows, infoRow{Label: "Staged changes", Value: "yes"})
	}
	if info.Paths.Root != "" {
		rows = append(rows, infoRow{Label: "Root dir", Value: info.Paths.Root})
	}
	return infoSection{Title: "Server (catch)", Rows: rows}
}

func formatServiceBackend(backend string) string {
	switch backend {
	case "docker", "docker-compose":
		return "Docker Compose"
	case "systemd":
		return "systemd"
	case serviceTypeVM:
		return "VM"
	default:
		return backend
	}
}

func shouldRenderServiceBackend(info catchrpc.ServiceInfo) bool {
	if info.ServiceType == "" {
		return false
	}
	switch info.DataType {
	case "python", "typescript":
		return true
	case "", "unknown":
		return true
	default:
		return false
	}
}

func shouldRenderServiceGeneration(info catchrpc.ServiceInfo) bool {
	if info.Generation == 0 && info.LatestGeneration == 0 {
		return false
	}
	if info.LatestGeneration == 0 {
		return true
	}
	return info.Generation != info.LatestGeneration
}

func formatServiceGeneration(info catchrpc.ServiceInfo) string {
	if info.LatestGeneration == 0 {
		return fmt.Sprintf("%d", info.Generation)
	}
	if info.Generation == 0 {
		return fmt.Sprintf("unknown (latest %d)", info.LatestGeneration)
	}
	return fmt.Sprintf("%d (latest %d)", info.Generation, info.LatestGeneration)
}

func renderNetworkSection(server catchrpc.ServiceInfoResponse) infoSection {
	if !server.Found {
		return infoSection{Title: "Network", Rows: nil}
	}
	if serviceInfoIsVM(server.Info) {
		return renderVMNetworkSection(server.Info)
	}
	rows := serviceNetworkRows(server.Info.Network)
	return infoSection{Title: "Network", Rows: rows}
}

func serviceNetworkRows(net catchrpc.ServiceNetwork) []infoRow {
	ipNet, hasIPs := serviceEndpointNetwork(net)
	rows := []infoRow{}
	if hasIPs || ipNet.IPError != "" {
		rows = append(rows, networkIPRows(ipNet)...)
	}
	if net.IPWarning != "" {
		rows = append(rows, infoRow{Label: "IP warning", Value: net.IPWarning})
	}
	rows = append(rows, networkPortRows(net)...)
	if net.Tailscale != nil {
		rows = append(rows, infoRow{Label: "Tailscale", Value: describeTailscale(net.Tailscale)})
	}
	if net.Macvlan != nil {
		rows = append(rows, infoRow{Label: "Macvlan", Value: describeMacvlan(net.Macvlan)})
	}
	return rows
}

func serviceEndpointNetwork(net catchrpc.ServiceNetwork) (catchrpc.ServiceNetwork, bool) {
	out := catchrpc.ServiceNetwork{
		SvcIP:   net.SvcIP,
		IPError: net.IPError,
	}
	for _, ip := range net.IPs {
		if serviceIPVisibleInPlainNetwork(ip, net) {
			out.IPs = append(out.IPs, ip)
		}
	}
	return out, out.SvcIP != "" || len(out.IPs) > 0
}

func serviceIPVisibleInPlainNetwork(ip catchrpc.ServiceIP, net catchrpc.ServiceNetwork) bool {
	switch strings.TrimSpace(ip.Label) {
	case "service":
		return true
	case "tailscale":
		return net.Tailscale != nil
	case "lan":
		return net.Macvlan != nil
	default:
		return false
	}
}

func renderVMNetworkSection(info catchrpc.ServiceInfo) infoSection {
	net := info.Network
	rows := networkIPRows(net)
	if net.IPWarning != "" {
		rows = append(rows, infoRow{Label: "IP warning", Value: net.IPWarning})
	}
	return infoSection{Title: "Network", Rows: rows}
}

func serviceInfoIsVM(info catchrpc.ServiceInfo) bool {
	return info.VM != nil || info.DataType == serviceTypeVM || info.ServiceType == serviceTypeVM
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

func networkPortRows(net catchrpc.ServiceNetwork) []infoRow {
	if len(net.Ports) == 0 {
		return nil
	}
	ports := make([]string, 0, len(net.Ports))
	for _, port := range net.Ports {
		if formatted := formatServicePort(port); formatted != "" {
			ports = append(ports, formatted)
		}
	}
	if len(ports) == 0 {
		return nil
	}
	return []infoRow{{Label: "Ports", Value: strings.Join(ports, ", ")}}
}

func formatServicePort(port catchrpc.ServicePort) string {
	if strings.TrimSpace(port.Raw) != "" {
		return strings.TrimSpace(port.Raw)
	}
	protocol := strings.TrimSpace(port.Protocol)
	if protocol == "" {
		protocol = "tcp"
	}
	host := formatPortEndpoint(port.HostIP, port.HostPort, protocol)
	container := formatPortEndpoint("", port.ContainerPort, protocol)
	if host == "" || container == "" {
		return ""
	}
	return host + " -> " + container
}

func formatPortEndpoint(hostIP string, port uint16, protocol string) string {
	if port == 0 {
		return ""
	}
	value := fmt.Sprintf("%d/%s", port, protocol)
	if strings.TrimSpace(hostIP) != "" {
		return strings.TrimSpace(hostIP) + ":" + value
	}
	return value
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

func renderRuntimeSection(service string, server catchrpc.ServiceInfoResponse) infoSection {
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
		return infoSection{Title: "Runtime", Rows: rows}
	}
	if runtimeComponentsDuplicateServiceStatus(service, status.Components) {
		return infoSection{Title: "Runtime", Rows: nil}
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

func runtimeComponentsDuplicateServiceStatus(service string, components []catchrpc.ServiceComponentStatus) bool {
	if len(components) != 1 {
		return false
	}
	component := components[0]
	return component.Name == service && component.Status != ""
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
	case "vm":
		return "VM"
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
