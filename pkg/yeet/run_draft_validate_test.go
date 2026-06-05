// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
	calls := 0
	stubRunDraftServiceInfo(t, func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		calls++
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
	if calls != 1 {
		t.Fatalf("service info calls = %d, want 1", calls)
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

func TestValidateRunDraftRejectsUnsupportedNetworkModes(t *testing.T) {
	draft := RunDraft{
		Service: "svc-a",
		Host:    "host-a",
		Payload: "ghcr.io/example/app:latest",
		Network: RunDraftNetwork{
			Modes: []string{"svc", "host", "macvlan"},
		},
	}
	_, validation := validateRunDraft(context.Background(), draft, t.TempDir())

	if validation.OK {
		t.Fatal("validation OK = true, want false")
	}
	if got := validation.fieldError("network.modes"); !strings.Contains(got, `unsupported network mode "host"`) {
		t.Fatalf("network.modes error = %q, want unsupported host", got)
	}
}

func TestValidateRunDraftNormalizesNetworkFields(t *testing.T) {
	draft := RunDraft{
		Service: "svc-a",
		Host:    "host-a",
		Payload: "ghcr.io/example/app:latest",
		Network: RunDraftNetwork{
			Modes:         []string{" svc ", "ts", "", "lan", "svc"},
			TSVersion:     " 1.2.3 ",
			TSExitNode:    " exit-node ",
			TSTags:        []string{" tag:app ", ""},
			MacvlanMAC:    " 02:00:00:00:00:07 ",
			MacvlanParent: " eno1 ",
			Publish:       []string{" 8080:80 ", ""},
		},
	}
	normalized, validation := validateRunDraft(context.Background(), draft, t.TempDir())

	if !validation.OK {
		t.Fatalf("validation OK = false, errors = %#v", validation.Errors)
	}
	if got, want := normalized.Network.Modes, []string{"svc", "ts", "lan"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("network modes = %#v, want %#v", got, want)
	}
	if normalized.Network.TSVersion != "1.2.3" || normalized.Network.TSExitNode != "exit-node" {
		t.Fatalf("tailscale fields = %q/%q, want trimmed", normalized.Network.TSVersion, normalized.Network.TSExitNode)
	}
	if got, want := normalized.Network.TSTags, []string{"tag:app"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ts tags = %#v, want %#v", got, want)
	}
	if normalized.Network.MacvlanMAC != "02:00:00:00:00:07" || normalized.Network.MacvlanParent != "eno1" {
		t.Fatalf("macvlan fields = %q/%q, want trimmed", normalized.Network.MacvlanMAC, normalized.Network.MacvlanParent)
	}
	if got, want := normalized.Network.Publish, []string{"8080:80"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("publish = %#v, want %#v", got, want)
	}
}

func TestValidateRunDraftAcceptsVMPayload(t *testing.T) {
	for _, payload := range []string{"vm://ubuntu/26.04", "vm://foo/bar"} {
		t.Run(payload, func(t *testing.T) {
			draft := RunDraft{
				Service: "devbox",
				Host:    "yeet-lab",
				Payload: payload,
				VM: RunDraftVM{
					CPUs:   4,
					Memory: "4g",
					Disk:   "128g",
				},
				Network: RunDraftNetwork{Modes: []string{"svc", "lan"}},
			}
			normalized, validation := validateRunDraft(context.Background(), draft, t.TempDir())
			if !validation.OK {
				t.Fatalf("validation OK = false, errors = %#v", validation.Errors)
			}
			if normalized.PayloadKind != "vm" {
				t.Fatalf("PayloadKind = %q, want vm", normalized.PayloadKind)
			}
			if normalized.Payload != payload {
				t.Fatalf("Payload = %q, want %q", normalized.Payload, payload)
			}
		})
	}
}

func TestValidateRunDraftRejectsTailscaleForVM(t *testing.T) {
	draft := RunDraft{
		Service: "devbox",
		Host:    "yeet-lab",
		Payload: "vm://ubuntu/26.04",
		Network: RunDraftNetwork{
			Modes: []string{"svc", "ts"},
		},
	}
	_, validation := validateRunDraft(context.Background(), draft, t.TempDir())
	if validation.OK {
		t.Fatal("validation OK = true, want false")
	}
	if got := validation.fieldError("network.modes"); !strings.Contains(got, `VM network mode "ts"`) {
		t.Fatalf("network.modes error = %q, want VM ts rejection", got)
	}
}

func TestValidateRunDraftRejectsVMFlagsForNonVMPayload(t *testing.T) {
	draft := RunDraft{
		Service: "api",
		Host:    "yeet-lab",
		Payload: "ghcr.io/example/api:latest",
		VM:      RunDraftVM{CPUs: 2},
	}
	_, validation := validateRunDraft(context.Background(), draft, t.TempDir())
	if validation.OK {
		t.Fatal("validation OK = true, want false")
	}
	if got := validation.fieldError("vm.cpus"); !strings.Contains(got, "only valid for VM payloads") {
		t.Fatalf("vm.cpus error = %q", got)
	}
}

func TestValidateRunDraftPayloadKindFileStatsImageLikePayload(t *testing.T) {
	draft := RunDraft{
		Service:     "svc-a",
		Host:        "host-a",
		Payload:     "ghcr.io/example/app:latest",
		PayloadKind: "file",
	}
	_, validation := validateRunDraft(context.Background(), draft, t.TempDir())

	if validation.OK {
		t.Fatal("validation OK = true, want false")
	}
	if got := validation.fieldError("payload"); !strings.Contains(got, "does not exist") {
		t.Fatalf("payload error = %q, want does not exist", got)
	}
}

func TestValidateRunDraftAutoPayloadKindAcceptsUntaggedLocalImageStylePayload(t *testing.T) {
	for _, kind := range []string{"", "auto"} {
		t.Run("kind="+kind, func(t *testing.T) {
			for _, payload := range []string{"alpine", "myapp", "registry.local/team/app", "registry.local:5000/team/app"} {
				t.Run(payload, func(t *testing.T) {
					draft := RunDraft{
						Service:     "svc-a",
						Host:        "host-a",
						Payload:     payload,
						PayloadKind: kind,
					}
					normalized, validation := validateRunDraft(context.Background(), draft, t.TempDir())

					if !validation.OK {
						t.Fatalf("validation OK = false, errors = %#v", validation.Errors)
					}
					if normalized.Payload != payload {
						t.Fatalf("payload = %q, want %q", normalized.Payload, payload)
					}
				})
			}
		})
	}
}

func TestValidateRunDraftRemoteImagePayloadKind(t *testing.T) {
	t.Run("accepts image ref", func(t *testing.T) {
		draft := RunDraft{
			Service:     "svc-a",
			Host:        "host-a",
			Payload:     "ghcr.io/example/app:latest",
			PayloadKind: "remote-image",
		}
		normalized, validation := validateRunDraft(context.Background(), draft, t.TempDir())

		if !validation.OK {
			t.Fatalf("validation OK = false, errors = %#v", validation.Errors)
		}
		if normalized.Payload != draft.Payload {
			t.Fatalf("payload = %q, want %q", normalized.Payload, draft.Payload)
		}
	})

	t.Run("rejects local path", func(t *testing.T) {
		tmp := t.TempDir()
		composePath := filepath.Join(tmp, "compose.yml")
		if err := os.WriteFile(composePath, []byte("services: {}\n"), 0o644); err != nil {
			t.Fatalf("write compose: %v", err)
		}
		draft := RunDraft{
			Service:     "svc-a",
			Host:        "host-a",
			Payload:     "compose.yml",
			PayloadKind: "remote-image",
		}
		_, validation := validateRunDraft(context.Background(), draft, tmp)

		if validation.OK {
			t.Fatal("validation OK = true, want false")
		}
		if got := validation.fieldError("payload"); !strings.Contains(got, "image") {
			t.Fatalf("payload error = %q, want image", got)
		}
	})

	t.Run("rejects absolute local path with image-like tag", func(t *testing.T) {
		tmp := t.TempDir()
		imageLikePath := filepath.Join(tmp, "compose:latest")
		if err := os.WriteFile(imageLikePath, []byte("services: {}\n"), 0o644); err != nil {
			t.Fatalf("write compose: %v", err)
		}
		draft := RunDraft{
			Service:     "svc-a",
			Host:        "host-a",
			Payload:     imageLikePath,
			PayloadKind: "remote-image",
		}
		_, validation := validateRunDraft(context.Background(), draft, tmp)

		if validation.OK {
			t.Fatal("validation OK = true, want false")
		}
		if got := validation.fieldError("payload"); !strings.Contains(got, "image") {
			t.Fatalf("payload error = %q, want image", got)
		}
	})

	t.Run("rejects untagged local image name", func(t *testing.T) {
		draft := RunDraft{
			Service:     "svc-a",
			Host:        "host-a",
			Payload:     "alpine",
			PayloadKind: "remote-image",
		}
		_, validation := validateRunDraft(context.Background(), draft, t.TempDir())

		if validation.OK {
			t.Fatal("validation OK = true, want false")
		}
		if got := validation.fieldError("payload"); !strings.Contains(got, "image") {
			t.Fatalf("payload error = %q, want image", got)
		}
	})

	t.Run("rejects malformed ref", func(t *testing.T) {
		draft := RunDraft{
			Service:     "svc-a",
			Host:        "host-a",
			Payload:     "not-an-image",
			PayloadKind: "remote-image",
		}
		_, validation := validateRunDraft(context.Background(), draft, t.TempDir())

		if validation.OK {
			t.Fatal("validation OK = true, want false")
		}
		if got := validation.fieldError("payload"); !strings.Contains(got, "image") {
			t.Fatalf("payload error = %q, want image", got)
		}
	})
}

func TestValidateRunDraftLocalImagePayloadKindDoesNotStatDockerStylePayload(t *testing.T) {
	for _, payload := range []string{"alpine", "myapp", "repo/svc/app:latest"} {
		t.Run(payload, func(t *testing.T) {
			draft := RunDraft{
				Service:     "svc-a",
				Host:        "host-a",
				Payload:     payload,
				PayloadKind: "local-image",
			}
			normalized, validation := validateRunDraft(context.Background(), draft, t.TempDir())

			if !validation.OK {
				t.Fatalf("validation OK = false, errors = %#v", validation.Errors)
			}
			if normalized.Payload != draft.Payload {
				t.Fatalf("payload = %q, want %q", normalized.Payload, draft.Payload)
			}
		})
	}
}

func TestValidateRunDraftDockerfilePayloadKind(t *testing.T) {
	tmp := t.TempDir()
	notDockerfile := filepath.Join(tmp, "Containerfile")
	if err := os.WriteFile(notDockerfile, []byte("FROM alpine\n"), 0o644); err != nil {
		t.Fatalf("write non-Dockerfile: %v", err)
	}
	dockerfile := filepath.Join(tmp, "Dockerfile")
	if err := os.WriteFile(dockerfile, []byte("FROM alpine\n"), 0o644); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}

	t.Run("rejects non-Dockerfile file", func(t *testing.T) {
		draft := RunDraft{
			Service:     "svc-a",
			Host:        "host-a",
			Payload:     "Containerfile",
			PayloadKind: "dockerfile",
		}
		_, validation := validateRunDraft(context.Background(), draft, tmp)

		if validation.OK {
			t.Fatal("validation OK = true, want false")
		}
		if got := validation.fieldError("payload"); !strings.Contains(got, "Dockerfile") {
			t.Fatalf("payload error = %q, want Dockerfile", got)
		}
	})

	t.Run("accepts Dockerfile", func(t *testing.T) {
		draft := RunDraft{
			Service:     "svc-a",
			Host:        "host-a",
			Payload:     "Dockerfile",
			PayloadKind: "dockerfile",
		}
		normalized, validation := validateRunDraft(context.Background(), draft, tmp)

		if !validation.OK {
			t.Fatalf("validation OK = false, errors = %#v", validation.Errors)
		}
		if normalized.Payload != dockerfile {
			t.Fatalf("payload = %q, want %q", normalized.Payload, dockerfile)
		}
	})
}

func TestValidateRunDraftComposePayloadKind(t *testing.T) {
	tmp := t.TempDir()
	notCompose := filepath.Join(tmp, "app.txt")
	if err := os.WriteFile(notCompose, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write non-compose: %v", err)
	}
	compose := filepath.Join(tmp, "compose.yml")
	if err := os.WriteFile(compose, []byte("services:\n  app:\n    image: alpine\n"), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}

	t.Run("rejects non-compose file", func(t *testing.T) {
		draft := RunDraft{
			Service:     "svc-a",
			Host:        "host-a",
			Payload:     "app.txt",
			PayloadKind: "compose",
		}
		_, validation := validateRunDraft(context.Background(), draft, tmp)

		if validation.OK {
			t.Fatal("validation OK = true, want false")
		}
		if got := validation.fieldError("payload"); !strings.Contains(got, "Docker Compose") {
			t.Fatalf("payload error = %q, want Docker Compose", got)
		}
	})

	t.Run("accepts compose file", func(t *testing.T) {
		draft := RunDraft{
			Service:     "svc-a",
			Host:        "host-a",
			Payload:     "compose.yml",
			PayloadKind: "compose",
		}
		normalized, validation := validateRunDraft(context.Background(), draft, tmp)

		if !validation.OK {
			t.Fatalf("validation OK = false, errors = %#v", validation.Errors)
		}
		if normalized.Payload != compose {
			t.Fatalf("payload = %q, want %q", normalized.Payload, compose)
		}
	})
}

func TestValidateRunDraftRejectsUnknownPayloadKind(t *testing.T) {
	draft := RunDraft{
		Service:     "svc-a",
		Host:        "host-a",
		Payload:     "ghcr.io/example/app:latest",
		PayloadKind: "archive",
	}
	_, validation := validateRunDraft(context.Background(), draft, t.TempDir())

	if validation.OK {
		t.Fatal("validation OK = true, want false")
	}
	if got := validation.fieldError("payloadKind"); !strings.Contains(got, "unknown") {
		t.Fatalf("payloadKind error = %q, want unknown", got)
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
		Service:        "svc-a",
		Host:           "host-a",
		Payload:        "ghcr.io/example/app:latest",
		NewServiceOnly: true,
	}
	_, validation := validateRunDraft(context.Background(), draft, t.TempDir())

	if validation.OK {
		t.Fatal("validation OK = true, want false")
	}
	if got := validation.fieldError("host"); !strings.Contains(got, "rpc unavailable") {
		t.Fatalf("host error = %q, want rpc unavailable", got)
	}
}

func TestValidateRunDraftSkipsServiceInfoWhenLocalValidationFails(t *testing.T) {
	stubRunDraftServiceInfo(t, func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		t.Fatalf("unexpected service info call for host=%q service=%q", host, service)
		return catchrpc.ServiceInfoResponse{}, nil
	})

	draft := RunDraft{
		Service:        "svc-a",
		Host:           "host-a",
		NewServiceOnly: true,
	}
	_, validation := validateRunDraft(context.Background(), draft, t.TempDir())

	if validation.OK {
		t.Fatal("validation OK = true, want false")
	}
	if got := validation.fieldError("payload"); got == "" {
		t.Fatal("payload error = empty, want missing payload error")
	}
}

func TestValidateRunDraftRejectsInvalidSnapshotFields(t *testing.T) {
	tests := []struct {
		name    string
		snap    RunDraftSnapshots
		field   string
		wantErr string
	}{
		{
			name:    "negative keep last",
			snap:    RunDraftSnapshots{KeepLast: -1},
			field:   "snapshots.keepLast",
			wantErr: "negative",
		},
		{
			name:    "keep last with inherit",
			snap:    RunDraftSnapshots{KeepLast: 3, KeepLastInherit: true},
			field:   "snapshots.keepLast",
			wantErr: "inherit",
		},
		{
			name:    "empty event",
			snap:    RunDraftSnapshots{Events: []string{"run", " "}},
			field:   "snapshots.events",
			wantErr: "empty",
		},
		{
			name:    "events with inherit",
			snap:    RunDraftSnapshots{Events: []string{"run"}, EventsInherit: true},
			field:   "snapshots.events",
			wantErr: "inherit",
		},
		{
			name:    "invalid max age",
			snap:    RunDraftSnapshots{MaxAge: "forever"},
			field:   "snapshots.maxAge",
			wantErr: "invalid",
		},
		{
			name:    "non-positive max age",
			snap:    RunDraftSnapshots{MaxAge: "0h"},
			field:   "snapshots.maxAge",
			wantErr: "positive",
		},
		{
			name:    "overflowing negative day max age",
			snap:    RunDraftSnapshots{MaxAge: "-106752d"},
			field:   "snapshots.maxAge",
			wantErr: "positive",
		},
		{
			name:    "overflowing positive day max age",
			snap:    RunDraftSnapshots{MaxAge: "106752d"},
			field:   "snapshots.maxAge",
			wantErr: "invalid",
		},
		{
			name:    "max age with inherit",
			snap:    RunDraftSnapshots{MaxAge: "7d", MaxAgeInherit: true},
			field:   "snapshots.maxAge",
			wantErr: "inherit",
		},
		{
			name:    "required with inherit",
			snap:    RunDraftSnapshots{Required: runDraftTestBool(true), RequiredInherit: true},
			field:   "snapshots.required",
			wantErr: "inherit",
		},
		{
			name:    "invalid event",
			snap:    RunDraftSnapshots{Events: []string{"run", "backup"}},
			field:   "snapshots.events",
			wantErr: "invalid",
		},
		{
			name:    "inherit mode with override",
			snap:    RunDraftSnapshots{Mode: "inherit", MaxAge: "72h"},
			field:   "snapshots.mode",
			wantErr: "inherit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubRunDraftServiceInfo(t, func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
				return catchrpc.ServiceInfoResponse{Found: false}, nil
			})

			draft := RunDraft{
				Service:   "svc-a",
				Host:      "host-a",
				Payload:   "ghcr.io/example/app:latest",
				Snapshots: tt.snap,
			}
			_, validation := validateRunDraft(context.Background(), draft, t.TempDir())

			if validation.OK {
				t.Fatal("validation OK = true, want false")
			}
			if got := validation.fieldError(tt.field); !strings.Contains(got, tt.wantErr) {
				t.Fatalf("%s error = %q, want %q", tt.field, got, tt.wantErr)
			}
		})
	}
}

func TestValidateRunDraftAcceptsValidSnapshotFields(t *testing.T) {
	draft := RunDraft{
		Service: "svc-a",
		Host:    "host-a",
		Payload: "ghcr.io/example/app:latest",
		Snapshots: RunDraftSnapshots{
			Mode:     "on",
			MaxAge:   "7d",
			Events:   []string{"run", "docker-update", "service-root-migration"},
			Required: runDraftTestBool(false),
		},
	}
	_, validation := validateRunDraft(context.Background(), draft, t.TempDir())

	if !validation.OK {
		t.Fatalf("validation OK = false, errors = %#v", validation.Errors)
	}
}

func TestValidateRunDraftRejectsInvalidZFSDatasetNames(t *testing.T) {
	tests := []string{
		"/tank/apps/svc-a",
		"tank/apps/",
		"/tank/apps",
		"tank//apps",
		"tank/./apps",
		"tank/../apps",
		"tank/apps/svc@snap",
		"tank/apps/svc#bookmark",
		"tank/apps/bad name",
		strings.Repeat("a", 256),
	}

	for _, serviceRoot := range tests {
		t.Run(serviceRoot, func(t *testing.T) {
			draft := RunDraft{
				Service: "svc-a",
				Host:    "host-a",
				Payload: "ghcr.io/example/app:latest",
				Storage: RunDraftStorage{
					ServiceRoot: serviceRoot,
					ZFS:         true,
				},
			}
			_, validation := validateRunDraft(context.Background(), draft, t.TempDir())

			if validation.OK {
				t.Fatal("validation OK = true, want false")
			}
			if got := validation.fieldError("serviceRoot"); !strings.Contains(got, "dataset") {
				t.Fatalf("serviceRoot error = %q, want dataset", got)
			}
		})
	}
}

func TestValidateRunDraftAcceptsZFSDatasetName(t *testing.T) {
	draft := RunDraft{
		Service: "svc-a",
		Host:    "host-a",
		Payload: "ghcr.io/example/app:latest",
		Storage: RunDraftStorage{
			ServiceRoot: "tank/apps/vaultwarden",
			ZFS:         true,
		},
	}
	_, validation := validateRunDraft(context.Background(), draft, t.TempDir())

	if !validation.OK {
		t.Fatalf("validation OK = false, errors = %#v", validation.Errors)
	}
}
