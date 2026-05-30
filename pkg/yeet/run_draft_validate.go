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
	runDraftLocalImageNameRE     = regexp.MustCompile(`^[a-z0-9]+([_-][a-z0-9]+)*(/[a-z0-9]+([_-][a-z0-9]+)*)*$`)
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
	validateRunDraftPaths(cwd, &draft, &result)
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
	draft.Storage.ServiceRoot = strings.TrimSpace(draft.Storage.ServiceRoot)
	draft.Snapshots.Mode = strings.TrimSpace(draft.Snapshots.Mode)
	draft.Snapshots.MaxAge = strings.TrimSpace(draft.Snapshots.MaxAge)
	return draft
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
		payload, err := normalizeRunDraftPayload(cwd, draft.Payload, draft.PayloadKind)
		if err != nil {
			result.addError("payload", "%v", err)
		} else {
			draft.Payload = payload
		}
	}

	if draft.EnvFile != "" {
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
		if draft.Storage.ServiceRoot == "" {
			result.addError("serviceRoot", "service root or ZFS dataset is required when zfs is enabled")
		} else if filepath.IsAbs(draft.Storage.ServiceRoot) {
			result.addError("serviceRoot", "zfs service root expects a dataset name, not an absolute path")
		} else if err := validateRunDraftZFSDatasetName(draft.Storage.ServiceRoot); err != nil {
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

func validateRunDraftSnapshots(draft *RunDraft, result *RunDraftValidationResult) {
	mode := validateRunDraftSnapshotMode(draft, result)
	validateRunDraftSnapshotKeepLast(draft.Snapshots, result)
	validateRunDraftSnapshotMaxAge(draft.Snapshots, result)
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
		if days > int(math.MaxInt64/(24*time.Hour)) {
			return fmt.Errorf("invalid snapshot max age %q", raw)
		}
		d := time.Duration(days) * 24 * time.Hour
		if d <= 0 {
			return fmt.Errorf("snapshot max age must be positive")
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

func normalizeRunDraftPayload(cwd, payload, kind string) (string, error) {
	payload = strings.TrimSpace(payload)
	switch kind {
	case "", "auto":
		return normalizeAutoRunDraftPayload(cwd, payload)
	case "file":
		return normalizeExistingRunDraftPath(cwd, payload)
	case "compose":
		return normalizeRunDraftComposePayload(cwd, payload)
	case "dockerfile":
		return normalizeRunDraftDockerfilePayload(cwd, payload)
	case "remote-image":
		if !looksLikeRunDraftImageRef(payload) {
			return "", fmt.Errorf("payload must be a Docker image reference for payloadKind %q", kind)
		}
		return payload, nil
	case "local-image":
		if !looksLikeRunDraftImageRef(payload) && !looksLikeRunDraftLocalImageName(payload) {
			return "", fmt.Errorf("payload must be a Docker image reference or local image name for payloadKind %q", kind)
		}
		return payload, nil
	}
	return "", fmt.Errorf("unknown payload kind %q", kind)
}

func normalizeAutoRunDraftPayload(cwd, payload string) (string, error) {
	if looksLikeRunDraftImageRef(payload) {
		return payload, nil
	}
	normalized, err := normalizeExistingRunDraftPath(cwd, payload)
	if err == nil {
		return normalized, nil
	}
	if looksLikeRunDraftLocalImageName(payload) {
		return payload, nil
	}
	return "", err
}

func normalizeRunDraftDockerfilePayload(cwd, payload string) (string, error) {
	normalized, err := normalizeExistingRunDraftPath(cwd, payload)
	if err != nil {
		return "", err
	}
	if filepath.Base(normalized) != "Dockerfile" {
		return "", fmt.Errorf("payloadKind %q requires a file named Dockerfile", "dockerfile")
	}
	return normalized, nil
}

func normalizeRunDraftComposePayload(cwd, payload string) (string, error) {
	normalized, err := normalizeExistingRunDraftPath(cwd, payload)
	if err != nil {
		return "", err
	}
	ft, err := ftdetect.DetectFile(normalized, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", fmt.Errorf("payloadKind %q requires a Docker Compose file: %w", "compose", err)
	}
	if ft != ftdetect.DockerCompose {
		return "", fmt.Errorf("payloadKind %q requires a Docker Compose file", "compose")
	}
	return normalized, nil
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
	if strings.ContainsAny(payload, " \t\n\r:@\\") {
		return false
	}
	if strings.HasPrefix(payload, "http://") || strings.HasPrefix(payload, "https://") {
		return false
	}
	return runDraftLocalImageNameRE.MatchString(payload)
}

func unknownPayloadKind(kind string) bool {
	switch kind {
	case "", "auto", "file", "compose", "dockerfile", "remote-image", "local-image":
		return false
	default:
		return true
	}
}

func validateRunDraftZFSDatasetName(dataset string) error {
	if strings.HasPrefix(dataset, "/") || strings.HasSuffix(dataset, "/") {
		return fmt.Errorf("zfs service root expects a dataset name without leading or trailing slash")
	}
	if strings.ContainsAny(dataset, "@#") {
		return fmt.Errorf("zfs service root expects a dataset name, not snapshot or bookmark syntax")
	}
	for _, part := range strings.Split(dataset, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("zfs service root contains invalid dataset component %q", part)
		}
		if !runDraftZFSDatasetPartRE.MatchString(part) {
			return fmt.Errorf("zfs service root contains malformed dataset component %q", part)
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
