// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yeetrun/yeet/pkg/catchrpc"
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
	result := RunDraftValidationResult{OK: true}

	draft = trimRunDraftFields(draft)
	validateRunDraftRequired(draft, &result)
	validateRunDraftService(ctx, draft, &result)
	validateRunDraftPaths(cwd, &draft, &result)
	validateRunDraftStorage(&draft, &result)
	validateRunDraftSnapshots(&draft, &result)

	return draft, result
}

func trimRunDraftFields(draft RunDraft) RunDraft {
	draft.Service = strings.TrimSpace(draft.Service)
	draft.Host = strings.TrimSpace(draft.Host)
	draft.Payload = strings.TrimSpace(draft.Payload)
	draft.EnvFile = strings.TrimSpace(draft.EnvFile)
	draft.Storage.ServiceRoot = strings.TrimSpace(draft.Storage.ServiceRoot)
	draft.Snapshots.Mode = strings.TrimSpace(draft.Snapshots.Mode)
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
	if draft.Payload != "" {
		payload, err := normalizeRunDraftPayload(cwd, draft.Payload)
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
	if draft.Snapshots.Mode != "" {
		mode, err := parseSnapshotModeValue(draft.Snapshots.Mode)
		if err != nil {
			result.addError("snapshots.mode", "%v", err)
		} else {
			draft.Snapshots.Mode = mode
		}
	}
}

func normalizeRunDraftPayload(cwd, payload string) (string, error) {
	payload = strings.TrimSpace(payload)
	if looksLikeImageRef(payload) {
		return payload, nil
	}
	return normalizeExistingRunDraftPath(cwd, payload)
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
