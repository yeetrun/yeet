// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

func stubRunDraftServiceInfo(t *testing.T, fn func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error)) {
	t.Helper()
	old := fetchRunDraftServiceInfoFn
	fetchRunDraftServiceInfoFn = fn
	t.Cleanup(func() {
		fetchRunDraftServiceInfoFn = old
	})
}

func TestValidateRunDraftRejectsExistingServiceInNewOnlyMode(t *testing.T) {
	stubRunDraftServiceInfo(t, func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: true}, nil
	})

	draft := RunDraft{
		Service:        "svc-a",
		Host:           "host-a",
		Payload:        "ghcr.io/example/app:latest",
		NewServiceOnly: true,
	}
	_, validation := validateRunDraft(context.Background(), draft, t.TempDir())

	if validation.OK {
		t.Fatal("validation OK = true, want false")
	}
	if got := validation.fieldError("service"); !strings.Contains(got, "already exists") {
		t.Fatalf("service error = %q, want already exists", got)
	}
}

func TestValidateRunDraftAcceptsNewServiceAndExistingFilePayload(t *testing.T) {
	stubRunDraftServiceInfo(t, func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	})

	tmp := t.TempDir()
	composePath := filepath.Join(tmp, "compose.yml")
	if err := os.WriteFile(composePath, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	draft := RunDraft{
		Service:        "svc-a",
		Host:           "host-a",
		Payload:        "compose.yml",
		NewServiceOnly: true,
	}
	normalized, validation := validateRunDraft(context.Background(), draft, tmp)

	if !validation.OK {
		t.Fatalf("validation OK = false, errors = %#v", validation.Errors)
	}
	if normalized.Payload != composePath {
		t.Fatalf("payload = %q, want %q", normalized.Payload, composePath)
	}
}

func TestValidateRunDraftRejectsInvalidRootsAndEnvFile(t *testing.T) {
	stubRunDraftServiceInfo(t, func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	})

	draft := RunDraft{
		Service:        "svc-a",
		Host:           "host-a",
		Payload:        "ghcr.io/example/app:latest",
		EnvFile:        "missing.env",
		Storage:        RunDraftStorage{ServiceRoot: "relative/root"},
		NewServiceOnly: true,
	}
	_, validation := validateRunDraft(context.Background(), draft, t.TempDir())

	if validation.OK {
		t.Fatal("validation OK = true, want false")
	}
	if got := validation.fieldError("envFile"); !strings.Contains(got, "does not exist") {
		t.Fatalf("envFile error = %q, want does not exist", got)
	}
	if got := validation.fieldError("serviceRoot"); !strings.Contains(got, "absolute") {
		t.Fatalf("serviceRoot error = %q, want absolute", got)
	}
}

func TestValidateRunDraftReportsHostError(t *testing.T) {
	stubRunDraftServiceInfo(t, func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{}, errors.New("rpc unavailable")
	})

	draft := RunDraft{
		Service: "svc-a",
		Host:    "host-a",
		Payload: "ghcr.io/example/app:latest",
	}
	_, validation := validateRunDraft(context.Background(), draft, t.TempDir())

	if validation.OK {
		t.Fatal("validation OK = true, want false")
	}
	if got := validation.fieldError("host"); !strings.Contains(got, "rpc unavailable") {
		t.Fatalf("host error = %q, want rpc unavailable", got)
	}
}
