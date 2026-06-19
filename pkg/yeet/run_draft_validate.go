// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/ftdetect"
)

var (
	runDraftSnapshotMaxAgeDaysRE = regexp.MustCompile(`^(-?[0-9]+)d$`)
	runDraftZFSDatasetPartRE     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*$`)
	runDraftLocalImageNameRE     = regexp.MustCompile(`^[a-z0-9]+([._-][a-z0-9]+)*(:[0-9]+)?(/[a-z0-9]+([._-][a-z0-9]+)*)*$`)
)

type RunDraftValidationResult struct {
	OK       bool                        `json:"ok"`
	Errors   []RunDraftValidationError   `json:"errors,omitempty"`
	Warnings []RunDraftValidationWarning `json:"warnings,omitempty"`
}

type RunDraftValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

type RunDraftValidationWarning struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (r RunDraftValidationResult) fieldError(field string) string {
	for _, err := range r.Errors {
		if err.Field == field {
			return err.Message
		}
	}
	return ""
}

func (r *RunDraftValidationResult) addError(field, format string, args ...any) {
	r.OK = false
	r.Errors = append(r.Errors, RunDraftValidationError{
		Field:   field,
		Message: fmt.Sprintf(format, args...),
	})
}

var fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
	return newRPCClient(host).ServiceInfo(ctx, service)
}

func validateRunDraft(ctx context.Context, draft RunDraft, cwd string) (RunDraft, RunDraftValidationResult) {
	draft, result := validateRunDraftLocal(draft, cwd)
	if result.OK && draft.NewServiceOnly {
		validateRunDraftService(ctx, draft, &result)
	}
	return draft, result
}

func validateRunDraftLocal(draft RunDraft, cwd string) (RunDraft, RunDraftValidationResult) {
	result := RunDraftValidationResult{OK: true}

	draft = trimRunDraftFields(draft)
	validateRunDraftRequired(draft, &result)
	validateRunDraftCron(&draft, &result)
	validateRunDraftPaths(cwd, &draft, &result)
	validateRunDraftVM(&draft, &result)
	if draft.PayloadKind == serviceTypeCron {
		return draft, result
	}
	validateRunDraftNetwork(&draft, &result)
	validateRunDraftStorage(&draft, &result)
	validateRunDraftSnapshots(&draft, &result)

	return draft, result
}

func trimRunDraftFields(draft RunDraft) RunDraft {
	draft.Service = strings.TrimSpace(draft.Service)
	draft.Host = strings.TrimSpace(draft.Host)
	draft.Payload = strings.TrimSpace(draft.Payload)
	draft.PayloadKind = strings.ToLower(strings.TrimSpace(draft.PayloadKind))
	draft.EnvFile = strings.TrimSpace(draft.EnvFile)
	draft.VM.Memory = strings.TrimSpace(draft.VM.Memory)
	draft.VM.Disk = strings.TrimSpace(draft.VM.Disk)
	draft.Storage.ServiceRoot = strings.TrimSpace(draft.Storage.ServiceRoot)
	draft.Cron.Schedule = strings.Join(strings.Fields(draft.Cron.Schedule), " ")
	draft.Snapshots.Mode = strings.TrimSpace(draft.Snapshots.Mode)
	draft.Snapshots.MaxAge = strings.TrimSpace(draft.Snapshots.MaxAge)
	return draft
}

func validateRunDraftNetwork(draft *RunDraft, result *RunDraftValidationResult) {
	networkModesProvided := runDraftNetworkModesProvided(draft.Network.Modes)
	draft.Network.Modes = normalizeRunDraftNetworkModes(draft.Network.Modes, result)
	draft.Network.TSVersion = strings.TrimSpace(draft.Network.TSVersion)
	draft.Network.TSExitNode = strings.TrimSpace(draft.Network.TSExitNode)
	draft.Network.TSTags = trimNonEmptyStrings(draft.Network.TSTags)
	draft.Network.TSAuthKey = strings.TrimSpace(draft.Network.TSAuthKey)
	draft.Network.MacvlanMAC = strings.TrimSpace(draft.Network.MacvlanMAC)
	draft.Network.MacvlanParent = strings.TrimSpace(draft.Network.MacvlanParent)
	draft.Network.Publish = trimNonEmptyStrings(draft.Network.Publish)
	validateRunDraftMacvlanVLAN(draft.Network.MacvlanVLAN, result)
	validateRunDraftMacvlanLAN(draft.Network, result)
	validateRunDraftTailscaleTags(draft.Network, result)
	if draft.PayloadKind == serviceTypeVM {
		defaultRunDraftVMNetworkModes(draft, networkModesProvided)
		validateRunDraftVMNetworkModes(draft.Network.Modes, result)
	}
}

func runDraftNetworkModesProvided(modes []string) bool {
	for _, mode := range modes {
		if strings.TrimSpace(mode) != "" {
			return true
		}
	}
	return false
}

func defaultRunDraftVMNetworkModes(draft *RunDraft, provided bool) {
	if provided || len(draft.Network.Modes) != 0 {
		return
	}
	draft.Network.Modes = []string{"svc"}
}

func validateRunDraftMacvlanVLAN(vlan int, result *RunDraftValidationResult) {
	if vlan < 0 {
		result.addError("network.macvlanVlan", "--macvlan-vlan must not be negative")
		return
	}
	if vlan > 4094 {
		result.addError("network.macvlanVlan", "--macvlan-vlan must be between 1 and 4094")
	}
}

func validateRunDraftMacvlanLAN(network RunDraftNetwork, result *RunDraftValidationResult) {
	if !runDraftMacvlanFieldsSet(network) || runDraftNetworkModeSet(network.Modes, "lan") {
		return
	}
	result.addError("network.modes", "--macvlan-* settings require LAN networking; use --net=lan or --net=svc,lan")
}

func runDraftMacvlanFieldsSet(network RunDraftNetwork) bool {
	return strings.TrimSpace(network.MacvlanParent) != "" || network.MacvlanVLAN != 0 || strings.TrimSpace(network.MacvlanMAC) != ""
}

func validateRunDraftTailscaleTags(network RunDraftNetwork, result *RunDraftValidationResult) {
	if !runDraftNetworkModeSet(network.Modes, "ts") || strings.TrimSpace(network.TSAuthKey) != "" || len(network.TSTags) != 0 {
		return
	}
	result.addError("network.tsTags", "Tailscale tags are required for OAuth enrollment; add a service tag such as tag:app or provide a service auth key")
}

func runDraftNetworkModeSet(modes []string, want string) bool {
	for _, mode := range modes {
		if strings.TrimSpace(mode) == want {
			return true
		}
	}
	return false
}

func validateRunDraftVMNetworkModes(modes []string, result *RunDraftValidationResult) {
	if len(modes) == 0 {
		result.addError("network.modes", "VM networking requires svc, lan, or svc,lan")
		return
	}
	for _, mode := range modes {
		switch mode {
		case "svc", "lan":
		default:
			result.addError("network.modes", "VM network mode %q is unsupported; supported modes: svc, lan", mode)
		}
	}
}

func validateRunDraftVM(draft *RunDraft, result *RunDraftValidationResult) {
	if draft.VM.CPUs < 0 {
		result.addError("vm.cpus", "vm cpus must be a positive value")
	}
	if draft.PayloadKind == serviceTypeVM {
		return
	}
	if draft.VM.CPUs != 0 {
		result.addError("vm.cpus", "--cpus is only valid for VM payloads")
	}
	if draft.VM.Memory != "" {
		result.addError("vm.memory", "--memory is only valid for VM payloads")
	}
	if draft.VM.Disk != "" {
		result.addError("vm.disk", "--disk is only valid for VM payloads")
	}
}

func normalizeRunDraftNetworkModes(modes []string, result *RunDraftValidationResult) []string {
	if len(modes) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(modes))
	out := make([]string, 0, len(modes))
	for _, raw := range modes {
		mode := strings.TrimSpace(raw)
		if mode == "" {
			continue
		}
		if !validRunDraftNetworkMode(mode) {
			result.addError("network.modes", "unsupported network mode %q", mode)
			continue
		}
		if seen[mode] {
			continue
		}
		seen[mode] = true
		out = append(out, mode)
	}
	return out
}

func validRunDraftNetworkMode(mode string) bool {
	switch mode {
	case "svc", "ts", "lan":
		return true
	default:
		return false
	}
}

func trimNonEmptyStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func validateRunDraftRequired(draft RunDraft, result *RunDraftValidationResult) {
	if draft.Service == "" {
		result.addError("service", "service is required")
	}
	if draft.Host == "" {
		result.addError("host", "host is required")
	}
	if draft.Payload == "" {
		result.addError("payload", "payload is required")
	}
}

func validateRunDraftService(ctx context.Context, draft RunDraft, result *RunDraftValidationResult) {
	if draft.Host != "" && draft.Service != "" {
		resp, err := fetchRunDraftServiceInfoFn(ctx, draft.Host, draft.Service)
		if err != nil {
			result.addError("host", "failed to reach catch on %q: %v", draft.Host, err)
		} else if draft.NewServiceOnly && resp.Found {
			result.addError("service", "service %q already exists on %q; web deploy currently supports new services only", draft.Service, draft.Host)
		}
	}
}

func validateRunDraftPaths(cwd string, draft *RunDraft, result *RunDraftValidationResult) {
	payloadKindOK := true
	if unknownPayloadKind(draft.PayloadKind) {
		payloadKindOK = false
		result.addError("payloadKind", "unknown payload kind %q", draft.PayloadKind)
	}
	if draft.Payload != "" && payloadKindOK {
		payload, payloadKind, err := normalizeRunDraftPayload(cwd, draft.Payload, draft.PayloadKind)
		if err != nil {
			result.addError("payload", "%v", err)
		} else {
			draft.Payload = payload
			draft.PayloadKind = payloadKind
		}
	}

	if draft.EnvFile != "" && draft.PayloadKind != serviceTypeCron {
		envFile, err := normalizeExistingRunDraftPath(cwd, draft.EnvFile)
		if err != nil {
			result.addError("envFile", "%v", err)
		} else {
			draft.EnvFile = envFile
		}
	}
}

func validateRunDraftStorage(draft *RunDraft, result *RunDraftValidationResult) {
	if draft.Storage.ZFS {
		zfsLabel := runDraftZFSStorageLabel(*draft)
		if draft.Storage.ServiceRoot == "" {
			result.addError("serviceRoot", "%s dataset is required when zfs is enabled", zfsLabel)
		} else if filepath.IsAbs(draft.Storage.ServiceRoot) {
			result.addError("serviceRoot", "%s expects a dataset name, not an absolute path", zfsLabel)
		} else if err := validateRunDraftZFSDatasetName(draft.Storage.ServiceRoot, zfsLabel); err != nil {
			result.addError("serviceRoot", "%v", err)
		}
	} else if draft.Storage.ServiceRoot != "" {
		if !filepath.IsAbs(draft.Storage.ServiceRoot) {
			result.addError("serviceRoot", "service root must be absolute unless zfs is enabled")
		} else {
			draft.Storage.ServiceRoot = filepath.Clean(draft.Storage.ServiceRoot)
		}
	}
}

func runDraftZFSStorageLabel(draft RunDraft) string {
	if draft.PayloadKind == serviceTypeVM {
		return "VM ZVOL parent"
	}
	return "zfs service root"
}

func validateRunDraftCron(draft *RunDraft, result *RunDraftValidationResult) {
	if draft.PayloadKind != serviceTypeCron {
		if strings.TrimSpace(draft.Cron.Schedule) != "" {
			result.addError("cron.schedule", "cron schedule is only valid for scheduled jobs")
		}
		return
	}
	validateRunDraftCronSchedule(draft, result)
	validateRunDraftCronUnsupportedFields(*draft, result)
}

func validateRunDraftCronSchedule(draft *RunDraft, result *RunDraftValidationResult) {
	fields, err := parseCronSchedule(draft.Cron.Schedule)
	if err != nil {
		result.addError("cron.schedule", "%v", err)
	} else {
		draft.Cron.Schedule = strings.Join(fields, " ")
	}
}

func validateRunDraftCronUnsupportedFields(draft RunDraft, result *RunDraftValidationResult) {
	if len(trimNonEmptyStrings(draft.Network.Modes)) != 0 {
		result.addError("network.modes", "network modes are not supported for scheduled jobs during web deploy")
	}
	validateRunDraftCronNetworkFields(draft.Network, result)
	if strings.TrimSpace(draft.EnvFile) != "" {
		result.addError("envFile", "env file is not supported for scheduled jobs during web deploy")
	}
	if draft.Pull {
		result.addError("pull", "pull is not supported for scheduled jobs during web deploy")
	}
	if draft.ForceDeploy {
		result.addError("forceDeploy", "force deploy is not supported for scheduled jobs during web deploy")
	}
	if draft.Storage.ServiceRoot != "" || draft.Storage.ZFS {
		result.addError("serviceRoot", "service root is not supported for scheduled jobs during web deploy")
	}
	if runDraftSnapshotsHasFieldOverrides(draft.Snapshots) || strings.TrimSpace(draft.Snapshots.Mode) != "" {
		result.addError("snapshots.mode", "snapshot overrides are not supported for scheduled jobs during web deploy")
	}
}

func validateRunDraftCronNetworkFields(network RunDraftNetwork, result *RunDraftValidationResult) {
	const message = "network settings are not supported for scheduled jobs during web deploy"
	if strings.TrimSpace(network.TSVersion) != "" {
		result.addError("network.tsVersion", message)
	}
	if strings.TrimSpace(network.TSExitNode) != "" {
		result.addError("network.tsExitNode", message)
	}
	if len(trimNonEmptyStrings(network.TSTags)) != 0 {
		result.addError("network.tsTags", message)
	}
	if strings.TrimSpace(network.TSAuthKey) != "" {
		result.addError("network.tsAuthKey", message)
	}
	if strings.TrimSpace(network.MacvlanMAC) != "" {
		result.addError("network.macvlanMac", message)
	}
	if network.MacvlanVLAN != 0 {
		result.addError("network.macvlanVlan", message)
	}
	if strings.TrimSpace(network.MacvlanParent) != "" {
		result.addError("network.macvlanParent", message)
	}
	if network.Restart != nil {
		result.addError("network.restart", message)
	}
	if len(trimNonEmptyStrings(network.Publish)) != 0 {
		result.addError("network.publish", message)
	}
}

func validateRunDraftSnapshots(draft *RunDraft, result *RunDraftValidationResult) {
	mode := validateRunDraftSnapshotMode(draft, result)
	validateRunDraftSnapshotKeepLast(draft.Snapshots, result)
	validateRunDraftSnapshotMaxAge(draft.Snapshots, result)
	validateRunDraftVMSnapshots(draft, result)
	validateRunDraftSnapshotRequired(draft.Snapshots, result)
	validateRunDraftSnapshotEvents(draft, result)
	if mode == "inherit" && runDraftSnapshotsHasFieldOverrides(draft.Snapshots) {
		result.addError("snapshots.mode", "snapshots inherit cannot be combined with field-level snapshot overrides")
	}
}

func validateRunDraftSnapshotKeepLast(snapshots RunDraftSnapshots, result *RunDraftValidationResult) {
	if snapshots.KeepLast < 0 {
		result.addError("snapshots.keepLast", "snapshot keep last cannot be negative")
	}
	if snapshots.KeepLast != 0 && snapshots.KeepLastInherit {
		result.addError("snapshots.keepLast", "snapshot keep last cannot be combined with inherit")
	}
}

func validateRunDraftSnapshotMaxAge(snapshots RunDraftSnapshots, result *RunDraftValidationResult) {
	if snapshots.MaxAge != "" && snapshots.MaxAgeInherit {
		result.addError("snapshots.maxAge", "snapshot max age cannot be combined with inherit")
	}
	if snapshots.MaxAge != "" {
		if err := validateRunDraftSnapshotMaxAgeValue(snapshots.MaxAge); err != nil {
			result.addError("snapshots.maxAge", "%v", err)
		}
	}
}

func validateRunDraftSnapshotRequired(snapshots RunDraftSnapshots, result *RunDraftValidationResult) {
	if snapshots.Required != nil && snapshots.RequiredInherit {
		result.addError("snapshots.required", "snapshot required cannot be combined with inherit")
	}
}

func validateRunDraftSnapshotEvents(draft *RunDraft, result *RunDraftValidationResult) {
	if draft.Snapshots.EventsInherit && len(draft.Snapshots.Events) != 0 {
		result.addError("snapshots.events", "snapshot events cannot be combined with inherit")
	}
	draft.Snapshots.Events = trimRunDraftSnapshotEvents(draft.Snapshots.Events, result)
}

func validateRunDraftVMSnapshots(draft *RunDraft, result *RunDraftValidationResult) {
	if draft.PayloadKind != serviceTypeVM {
		return
	}
	if draft.Snapshots.Required != nil || draft.Snapshots.RequiredInherit {
		result.addError("snapshots.required", "VM snapshot policy does not use required; manual VM snapshots fail or succeed directly")
	}
	if len(draft.Snapshots.Events) != 0 || draft.Snapshots.EventsInherit {
		result.addError("snapshots.events", "VM snapshot policy does not use events; use mode, keep last, and max age for manual VM snapshots")
		draft.Snapshots.Events = nil
	}
}

func validateRunDraftSnapshotMode(draft *RunDraft, result *RunDraftValidationResult) string {
	if draft.Snapshots.Mode == "" {
		return ""
	}
	mode, err := parseSnapshotModeValue(draft.Snapshots.Mode)
	if err != nil {
		result.addError("snapshots.mode", "%v", err)
		return ""
	}
	draft.Snapshots.Mode = mode
	return mode
}

func runDraftSnapshotsHasFieldOverrides(snapshots RunDraftSnapshots) bool {
	return snapshots.KeepLast != 0 ||
		snapshots.KeepLastInherit ||
		snapshots.MaxAge != "" ||
		snapshots.MaxAgeInherit ||
		snapshots.Required != nil ||
		snapshots.RequiredInherit ||
		len(snapshots.Events) != 0 ||
		snapshots.EventsInherit
}

func trimRunDraftSnapshotEvents(events []string, result *RunDraftValidationResult) []string {
	if len(events) == 0 {
		return nil
	}
	out := make([]string, 0, len(events))
	for i, event := range events {
		event = strings.TrimSpace(event)
		if event == "" {
			result.addError("snapshots.events", "snapshot event at index %d must not be empty", i)
			continue
		}
		if !validRunDraftSnapshotEvent(event) {
			result.addError("snapshots.events", "invalid snapshot event %q", event)
			continue
		}
		out = append(out, event)
	}
	return out
}

func validRunDraftSnapshotEvent(event string) bool {
	switch event {
	case "run", "docker-update", "service-root-migration":
		return true
	default:
		return false
	}
}

func validateRunDraftSnapshotMaxAgeValue(raw string) error {
	if match := runDraftSnapshotMaxAgeDaysRE.FindStringSubmatch(raw); match != nil {
		days, err := strconv.Atoi(match[1])
		if err != nil {
			return fmt.Errorf("invalid snapshot max age %q", raw)
		}
		if days <= 0 {
			return fmt.Errorf("snapshot max age must be positive")
		}
		if days > int(math.MaxInt64/(24*time.Hour)) {
			return fmt.Errorf("invalid snapshot max age %q", raw)
		}
		return nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("invalid snapshot max age %q", raw)
	}
	if d <= 0 {
		return fmt.Errorf("snapshot max age must be positive")
	}
	return nil
}

func normalizeRunDraftPayload(cwd, payload, kind string) (string, string, error) {
	payload = strings.TrimSpace(payload)
	if isVMPayload(payload) || kind == serviceTypeVM {
		return normalizeVMRunDraftPayload(payload, kind)
	}
	normalizer, ok := runDraftPayloadNormalizer(kind)
	if !ok {
		return "", kind, fmt.Errorf("unknown payload kind %q", kind)
	}
	return normalizer(cwd, payload, kind)
}

type runDraftPayloadNormalizerFunc func(cwd, payload, kind string) (string, string, error)

func runDraftPayloadNormalizer(kind string) (runDraftPayloadNormalizerFunc, bool) {
	normalizers := map[string]runDraftPayloadNormalizerFunc{
		"":              normalizeAutoRunDraftPayloadKind,
		"auto":          normalizeAutoRunDraftPayloadKind,
		"file":          normalizeFileRunDraftPayloadKind,
		serviceTypeCron: normalizeFileRunDraftPayloadKind,
		"compose":       normalizeComposeRunDraftPayloadKind,
		"dockerfile":    normalizeDockerfileRunDraftPayloadKind,
		"remote-image":  normalizeRemoteImageRunDraftPayloadKind,
		"local-image":   normalizeLocalImageRunDraftPayloadKind,
	}
	normalizer, ok := normalizers[kind]
	return normalizer, ok
}

func normalizeAutoRunDraftPayloadKind(cwd, payload, _ string) (string, string, error) {
	return normalizeAutoRunDraftPayload(cwd, payload)
}

func normalizeFileRunDraftPayloadKind(cwd, payload, kind string) (string, string, error) {
	normalized, err := normalizeExistingRunDraftPath(cwd, payload)
	return normalized, kind, err
}

func normalizeComposeRunDraftPayloadKind(cwd, payload, _ string) (string, string, error) {
	return normalizeRunDraftComposePayload(cwd, payload)
}

func normalizeDockerfileRunDraftPayloadKind(cwd, payload, _ string) (string, string, error) {
	return normalizeRunDraftDockerfilePayload(cwd, payload)
}

func normalizeRemoteImageRunDraftPayloadKind(_ string, payload, kind string) (string, string, error) {
	if !looksLikeRunDraftImageRef(payload) {
		return "", kind, fmt.Errorf("payload must be a Docker image reference for payloadKind %q", kind)
	}
	return payload, kind, nil
}

func normalizeLocalImageRunDraftPayloadKind(_ string, payload, kind string) (string, string, error) {
	if !looksLikeRunDraftImageRef(payload) && !looksLikeRunDraftLocalImageName(payload) {
		return "", kind, fmt.Errorf("payload must be a Docker image reference or local image name for payloadKind %q", kind)
	}
	return payload, kind, nil
}

func normalizeVMRunDraftPayload(payload, kind string) (string, string, error) {
	switch {
	case kind == serviceTypeVM && !isVMPayload(payload):
		return "", kind, fmt.Errorf("payloadKind %q requires a vm:// payload", kind)
	case kind == serviceTypeVM:
		return payload, serviceTypeVM, nil
	case kind == "" || kind == "auto":
		return payload, serviceTypeVM, nil
	default:
		return "", kind, fmt.Errorf("payload %q requires payloadKind %q", payload, serviceTypeVM)
	}
}

func isVMPayload(payload string) bool {
	return strings.HasPrefix(strings.TrimSpace(payload), "vm://")
}

func normalizeAutoRunDraftPayload(cwd, payload string) (string, string, error) {
	if isVMPayload(payload) {
		return strings.TrimSpace(payload), serviceTypeVM, nil
	}
	if looksLikeRunDraftImageRef(payload) {
		return payload, "", nil
	}
	normalized, err := normalizeExistingRunDraftPath(cwd, payload)
	if err == nil {
		return normalized, "", nil
	}
	if looksLikeRunDraftLocalImageName(payload) {
		return payload, "local-image", nil
	}
	return "", "", err
}

func normalizeRunDraftDockerfilePayload(cwd, payload string) (string, string, error) {
	normalized, err := normalizeExistingRunDraftPath(cwd, payload)
	if err != nil {
		return "", "dockerfile", err
	}
	if filepath.Base(normalized) != "Dockerfile" {
		return "", "dockerfile", fmt.Errorf("payloadKind %q requires a file named Dockerfile", "dockerfile")
	}
	return normalized, "dockerfile", nil
}

func normalizeRunDraftComposePayload(cwd, payload string) (string, string, error) {
	normalized, err := normalizeExistingRunDraftPath(cwd, payload)
	if err != nil {
		return "", "compose", err
	}
	ft, err := ftdetect.DetectFile(normalized, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", "compose", fmt.Errorf("payloadKind %q requires a Docker Compose file: %w", "compose", err)
	}
	if ft != ftdetect.DockerCompose {
		return "", "compose", fmt.Errorf("payloadKind %q requires a Docker Compose file", "compose")
	}
	return normalized, "compose", nil
}

func looksLikeRunDraftImageRef(payload string) bool {
	if filepath.IsAbs(payload) || strings.HasPrefix(payload, "./") || strings.HasPrefix(payload, "../") {
		return false
	}
	return looksLikeImageRef(payload)
}

func looksLikeRunDraftLocalImageName(payload string) bool {
	if payload == "" {
		return false
	}
	if filepath.IsAbs(payload) || strings.HasPrefix(payload, "./") || strings.HasPrefix(payload, "../") {
		return false
	}
	if strings.ContainsAny(payload, " \t\n\r@\\") {
		return false
	}
	if strings.HasPrefix(payload, "http://") || strings.HasPrefix(payload, "https://") {
		return false
	}
	return runDraftLocalImageNameRE.MatchString(payload)
}

func unknownPayloadKind(kind string) bool {
	switch kind {
	case "", "auto", "file", "compose", "dockerfile", "remote-image", "local-image", serviceTypeVM, serviceTypeCron:
		return false
	default:
		return true
	}
}

func validateRunDraftZFSDatasetName(dataset, label string) error {
	if len(dataset) > 255 {
		return fmt.Errorf("%s dataset name must be 255 characters or fewer", label)
	}
	if strings.HasPrefix(dataset, "/") || strings.HasSuffix(dataset, "/") {
		return fmt.Errorf("%s expects a dataset name without leading or trailing slash", label)
	}
	if strings.ContainsAny(dataset, "@#") {
		return fmt.Errorf("%s expects a dataset name, not snapshot or bookmark syntax", label)
	}
	for _, part := range strings.Split(dataset, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("%s contains invalid dataset component %q", label, part)
		}
		if !runDraftZFSDatasetPartRE.MatchString(part) {
			return fmt.Errorf("%s contains malformed dataset component %q", label, part)
		}
	}
	return nil
}

func normalizeExistingRunDraftPath(cwd, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	if !filepath.IsAbs(path) {
		base := strings.TrimSpace(cwd)
		if base == "" {
			base = "."
		}
		absBase, err := filepath.Abs(base)
		if err != nil {
			return "", fmt.Errorf("resolve working directory %q: %w", cwd, err)
		}
		path = filepath.Join(absBase, path)
	}
	path = filepath.Clean(path)

	st, err := os.Stat(path)
	if os.IsNotExist(err) {
		return "", fmt.Errorf("%q does not exist", path)
	}
	if err != nil {
		return "", fmt.Errorf("stat %q: %w", path, err)
	}
	if st.IsDir() {
		return "", fmt.Errorf("%q is a directory", path)
	}
	return path, nil
}
