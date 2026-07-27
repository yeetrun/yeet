// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

func TestRunWebAPITokenRequired(t *testing.T) {
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: t.TempDir()})
	req := httptest.NewRequest(http.MethodGet, "/api/bootstrap", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestRunWebAPITokenQueryAllowedButEmptyConfiguredTokenRejected(t *testing.T) {
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: t.TempDir()})
	req := httptest.NewRequest(http.MethodGet, "/api/bootstrap?token=secret", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("query token status = %d, want 200", rec.Code)
	}

	s = newRunWebServer(runWebServerConfig{Root: t.TempDir()})
	req = httptest.NewRequest(http.MethodGet, "/api/bootstrap?token=", nil)
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("empty configured token status = %d, want 401", rec.Code)
	}
}

func TestRunWebAPIBootstrapAndFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	s := newRunWebServer(runWebServerConfig{
		Token:     "secret",
		Root:      root,
		Bootstrap: runWebBootstrap{SelectedHost: "host-a", Hosts: []string{"host-a"}},
	})
	rec := runWebAPIRequest(t, s, http.MethodGet, "/api/bootstrap", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap status = %d", rec.Code)
	}
	rec = runWebAPIRequest(t, s, http.MethodGet, "/api/files?dir=.", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("files status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "compose.yml") {
		t.Fatalf("files body = %s, want compose.yml", rec.Body.String())
	}
}

func TestRunWebAPIFilesSearchQuery(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "apps", "searxng"), 0o755); err != nil {
		t.Fatalf("mkdir app: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "apps", "searxng", "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	s := newRunWebServer(runWebServerConfig{
		Token: "secret",
		Root:  root,
	})

	rec := runWebAPIRequest(t, s, http.MethodGet, "/api/files?q=searx+compose&field=payload", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("files search status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body runWebFileList
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Entries) != 1 || body.Entries[0].Path != "apps/searxng/docker-compose.yml" {
		t.Fatalf("body = %#v, want recursive compose match", body)
	}
}

func TestRunWebAPIZFSRootsUsesSelectedHost(t *testing.T) {
	oldFetch := fetchRunWebZFSRootCandidatesFn
	defer func() { fetchRunWebZFSRootCandidatesFn = oldFetch }()

	var gotHost string
	var gotReq catchrpc.ZFSServiceRootCandidatesRequest
	fetchRunWebZFSRootCandidatesFn = func(ctx context.Context, host string, req catchrpc.ZFSServiceRootCandidatesRequest) (catchrpc.ZFSServiceRootCandidatesResponse, error) {
		gotHost = host
		gotReq = req
		return catchrpc.ZFSServiceRootCandidatesResponse{
			State: catchrpc.ZFSRootDiscoveryAvailable,
			Candidates: []catchrpc.ZFSServiceRootCandidate{{
				Dataset:          "flash/yeet/vms",
				SuggestedDataset: "flash/yeet/vms/devbox",
			}},
		}, nil
	}
	s := newRunWebServer(runWebServerConfig{
		Token:     "secret",
		Root:      t.TempDir(),
		Bootstrap: runWebBootstrap{SelectedHost: "yeet-lab"},
	})
	rec := runWebAPIRequest(t, s, http.MethodGet, "/api/zfs-roots?workload=vm&service=devbox", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("zfs roots status = %d body=%s", rec.Code, rec.Body.String())
	}
	if gotHost != "yeet-lab" {
		t.Fatalf("host = %q, want yeet-lab", gotHost)
	}
	if gotReq.Workload != "vm" || gotReq.Service != "devbox" {
		t.Fatalf("request = %#v", gotReq)
	}
	var body catchrpc.ZFSServiceRootCandidatesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.State != catchrpc.ZFSRootDiscoveryAvailable || len(body.Candidates) != 1 {
		t.Fatalf("response = %#v", body)
	}

	rec = runWebAPIRequest(t, s, http.MethodGet, "/api/zfs-roots?host=yeet-storage&workload=compose", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("zfs roots explicit host status = %d body=%s", rec.Code, rec.Body.String())
	}
	if gotHost != "yeet-storage" {
		t.Fatalf("explicit host = %q, want yeet-storage", gotHost)
	}
}

func TestRunWebAPIZFSRootsMapsErrorsToStates(t *testing.T) {
	oldFetch := fetchRunWebZFSRootCandidatesFn
	defer func() { fetchRunWebZFSRootCandidatesFn = oldFetch }()

	tests := []struct {
		name string
		err  error
		want catchrpc.ZFSRootDiscoveryState
	}{
		{name: "unsupported rpc", err: errors.New("rpc error -32601: method not found"), want: catchrpc.ZFSRootDiscoveryUnsupportedRPC},
		{name: "host unreachable", err: syscall.ECONNREFUSED, want: catchrpc.ZFSRootDiscoveryHostUnreachable},
		{name: "other error", err: errors.New("permission denied"), want: catchrpc.ZFSRootDiscoveryError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fetchRunWebZFSRootCandidatesFn = func(ctx context.Context, host string, req catchrpc.ZFSServiceRootCandidatesRequest) (catchrpc.ZFSServiceRootCandidatesResponse, error) {
				return catchrpc.ZFSServiceRootCandidatesResponse{}, tt.err
			}
			s := newRunWebServer(runWebServerConfig{
				Token:     "secret",
				Root:      t.TempDir(),
				Bootstrap: runWebBootstrap{SelectedHost: "yeet-lab"},
			})
			rec := runWebAPIRequest(t, s, http.MethodGet, "/api/zfs-roots?workload=vm", nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("zfs roots status = %d body=%s", rec.Code, rec.Body.String())
			}
			var body catchrpc.ZFSServiceRootCandidatesResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.State != tt.want {
				t.Fatalf("state = %q, want %q body=%s", body.State, tt.want, rec.Body.String())
			}
		})
	}
}

func TestRunWebAPIZFSRootsRejectsBadMethods(t *testing.T) {
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: t.TempDir()})
	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/zfs-roots", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405 body=%s", rec.Code, rec.Body.String())
	}
}

func TestRunWebAPIHostStorageUsesSelectedHost(t *testing.T) {
	oldFetch := fetchRunWebHostStorageInfoFn
	oldFetchDefaults := fetchRunWebServiceRootDefaultsFn
	defer func() {
		fetchRunWebHostStorageInfoFn = oldFetch
		fetchRunWebServiceRootDefaultsFn = oldFetchDefaults
	}()

	var gotHost string
	var gotDefaultHost string
	var gotDefaultReq catchrpc.ServiceRootDefaultsRequest
	fetchRunWebHostStorageInfoFn = func(ctx context.Context, host string) (serverInfo, error) {
		gotHost = host
		return serverInfo{
			RootDir:     "/flash/yeet/data",
			ServicesDir: "/flash/yeet/services",
		}, nil
	}
	fetchRunWebServiceRootDefaultsFn = func(ctx context.Context, host string, req catchrpc.ServiceRootDefaultsRequest) (catchrpc.ServiceRootDefaultsResponse, error) {
		gotDefaultHost = host
		gotDefaultReq = req
		if strings.TrimSpace(req.Service) == "" {
			return catchrpc.ServiceRootDefaultsResponse{
				ServiceRoot:    "flash/yeet/services/",
				ServiceRootZFS: "flash/yeet/services/",
				ZFS:            true,
			}, nil
		}
		return catchrpc.ServiceRootDefaultsResponse{
			ServiceRoot:    "flash/yeet/services/nginx",
			ServiceRootZFS: "flash/yeet/services/nginx",
			ZFS:            true,
		}, nil
	}
	s := newRunWebServer(runWebServerConfig{
		Token:     "secret",
		Root:      t.TempDir(),
		Bootstrap: runWebBootstrap{SelectedHost: "yeet-lab"},
	})
	rec := runWebAPIRequest(t, s, http.MethodGet, "/api/host-storage?service=nginx&workload=compose", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("host storage status = %d body=%s", rec.Code, rec.Body.String())
	}
	if gotHost != "yeet-lab" {
		t.Fatalf("host = %q, want yeet-lab", gotHost)
	}
	if gotDefaultHost != "yeet-lab" || gotDefaultReq.Service != "nginx" {
		t.Fatalf("default request = host %q req %#v, want nginx on yeet-lab", gotDefaultHost, gotDefaultReq)
	}
	var body runWebHostStorageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.State != "available" || body.Storage.DataDir != "/flash/yeet/data" || body.Storage.ServicesRoot != "/flash/yeet/services" {
		t.Fatalf("response = %#v, want available flash storage", body)
	}
	if body.Defaults.ServiceRoot != "flash/yeet/services/nginx" || !body.Defaults.ZFS {
		t.Fatalf("defaults = %#v, want ZFS nginx service root", body.Defaults)
	}

	rec = runWebAPIRequest(t, s, http.MethodGet, "/api/host-storage?host=yeet-storage", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("host storage explicit host status = %d body=%s", rec.Code, rec.Body.String())
	}
	if gotHost != "yeet-storage" {
		t.Fatalf("explicit host = %q, want yeet-storage", gotHost)
	}
}

func TestRunWebAPIHostStorageUsesZFSPlaceholderBeforeServiceName(t *testing.T) {
	oldFetch := fetchRunWebHostStorageInfoFn
	oldFetchDefaults := fetchRunWebServiceRootDefaultsFn
	defer func() {
		fetchRunWebHostStorageInfoFn = oldFetch
		fetchRunWebServiceRootDefaultsFn = oldFetchDefaults
	}()

	var gotDefaultReq catchrpc.ServiceRootDefaultsRequest
	fetchRunWebHostStorageInfoFn = func(ctx context.Context, host string) (serverInfo, error) {
		return serverInfo{
			RootDir:     "/flash/yeet/data",
			ServicesDir: "/flash/yeet/services",
		}, nil
	}
	fetchRunWebServiceRootDefaultsFn = func(ctx context.Context, host string, req catchrpc.ServiceRootDefaultsRequest) (catchrpc.ServiceRootDefaultsResponse, error) {
		gotDefaultReq = req
		return catchrpc.ServiceRootDefaultsResponse{
			ServiceRoot:    "flash/yeet/services/",
			ServiceRootZFS: "flash/yeet/services/",
			ZFS:            true,
		}, nil
	}
	s := newRunWebServer(runWebServerConfig{
		Token:     "secret",
		Root:      t.TempDir(),
		Bootstrap: runWebBootstrap{SelectedHost: "yeet-lab"},
	})
	rec := runWebAPIRequest(t, s, http.MethodGet, "/api/host-storage?workload=compose", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("host storage status = %d body=%s", rec.Code, rec.Body.String())
	}
	if gotDefaultReq.Service != "" {
		t.Fatalf("default request service = %q, want blank", gotDefaultReq.Service)
	}
	var body runWebHostStorageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Defaults.ServiceRoot != "" || body.Defaults.ServiceRootZFS != "flash/yeet/services" || !body.Defaults.ZFS {
		t.Fatalf("defaults = %#v, want blank value with ZFS services dataset", body.Defaults)
	}
	if body.Defaults.ServiceRootPlaceholder != "flash/yeet/services/<service>" {
		t.Fatalf("placeholder = %q, want flash/yeet/services/<service>", body.Defaults.ServiceRootPlaceholder)
	}
}

func TestRunWebAPIHostStorageDerivesServicesRootFromDataDir(t *testing.T) {
	oldFetch := fetchRunWebHostStorageInfoFn
	oldFetchDefaults := fetchRunWebServiceRootDefaultsFn
	defer func() {
		fetchRunWebHostStorageInfoFn = oldFetch
		fetchRunWebServiceRootDefaultsFn = oldFetchDefaults
	}()

	fetchRunWebHostStorageInfoFn = func(ctx context.Context, host string) (serverInfo, error) {
		return serverInfo{RootDir: "/srv/yeet-data"}, nil
	}
	fetchRunWebServiceRootDefaultsFn = func(ctx context.Context, host string, req catchrpc.ServiceRootDefaultsRequest) (catchrpc.ServiceRootDefaultsResponse, error) {
		return catchrpc.ServiceRootDefaultsResponse{}, nil
	}
	s := newRunWebServer(runWebServerConfig{
		Token:     "secret",
		Root:      t.TempDir(),
		Bootstrap: runWebBootstrap{SelectedHost: "yeet-lab"},
	})
	rec := runWebAPIRequest(t, s, http.MethodGet, "/api/host-storage", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("host storage status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body runWebHostStorageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.State != "available" || body.Storage.ServicesRoot != "/srv/yeet-data/services" {
		t.Fatalf("response = %#v, want services root derived from data dir", body)
	}
	if body.Defaults.ServiceRoot != "" || body.Defaults.ServiceRootPlaceholder != "/srv/yeet-data/services/<service>" || body.Defaults.ZFS {
		t.Fatalf("defaults = %#v, want filesystem placeholder", body.Defaults)
	}
}

func TestRunWebAPIHostStorageInfersLegacyZFSDefaultFromServicesRootCandidate(t *testing.T) {
	oldFetch := fetchRunWebHostStorageInfoFn
	oldFetchDefaults := fetchRunWebServiceRootDefaultsFn
	oldFetchZFS := fetchRunWebZFSRootCandidatesFn
	defer func() {
		fetchRunWebHostStorageInfoFn = oldFetch
		fetchRunWebServiceRootDefaultsFn = oldFetchDefaults
		fetchRunWebZFSRootCandidatesFn = oldFetchZFS
	}()

	fetchRunWebHostStorageInfoFn = func(ctx context.Context, host string) (serverInfo, error) {
		return serverInfo{
			RootDir:     "/flash/yeet/data",
			ServicesDir: "/flash/yeet/services",
		}, nil
	}
	fetchRunWebServiceRootDefaultsFn = func(ctx context.Context, host string, req catchrpc.ServiceRootDefaultsRequest) (catchrpc.ServiceRootDefaultsResponse, error) {
		return catchrpc.ServiceRootDefaultsResponse{}, errors.New("rpc error -32601: method not found")
	}
	fetchRunWebZFSRootCandidatesFn = func(ctx context.Context, host string, req catchrpc.ZFSServiceRootCandidatesRequest) (catchrpc.ZFSServiceRootCandidatesResponse, error) {
		if req.Service != "nginx" {
			t.Fatalf("zfs request = %#v, want nginx", req)
		}
		return catchrpc.ZFSServiceRootCandidatesResponse{
			State: catchrpc.ZFSRootDiscoveryAvailable,
			Candidates: []catchrpc.ZFSServiceRootCandidate{{
				Dataset:          "flash/yeet/services",
				Mountpoint:       "/flash/yeet/services",
				SuggestedDataset: "flash/yeet/services/nginx",
			}},
		}, nil
	}
	s := newRunWebServer(runWebServerConfig{
		Token:     "secret",
		Root:      t.TempDir(),
		Bootstrap: runWebBootstrap{SelectedHost: "yeet-lab"},
	})
	rec := runWebAPIRequest(t, s, http.MethodGet, "/api/host-storage?service=nginx", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("host storage status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body runWebHostStorageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Defaults.ServiceRoot != "flash/yeet/services/nginx" || !body.Defaults.ZFS {
		t.Fatalf("defaults = %#v, want legacy inferred ZFS root", body.Defaults)
	}
}

func TestRunWebAPIHostStorageTreatsUnclassifiedDefaultsRPCAsLegacy(t *testing.T) {
	oldFetch := fetchRunWebHostStorageInfoFn
	oldFetchDefaults := fetchRunWebServiceRootDefaultsFn
	oldFetchZFS := fetchRunWebZFSRootCandidatesFn
	defer func() {
		fetchRunWebHostStorageInfoFn = oldFetch
		fetchRunWebServiceRootDefaultsFn = oldFetchDefaults
		fetchRunWebZFSRootCandidatesFn = oldFetchZFS
	}()

	fetchRunWebHostStorageInfoFn = func(ctx context.Context, host string) (serverInfo, error) {
		return serverInfo{
			RootDir:     "/flash/yeet/data",
			ServicesDir: "/flash/yeet/services",
		}, nil
	}
	fetchRunWebServiceRootDefaultsFn = func(ctx context.Context, host string, req catchrpc.ServiceRootDefaultsRequest) (catchrpc.ServiceRootDefaultsResponse, error) {
		return catchrpc.ServiceRootDefaultsResponse{}, errors.New(`unauthorized connection: unclassified RPC method "catch.ServiceRootDefaults"`)
	}
	fetchRunWebZFSRootCandidatesFn = func(ctx context.Context, host string, req catchrpc.ZFSServiceRootCandidatesRequest) (catchrpc.ZFSServiceRootCandidatesResponse, error) {
		if req.Service != "nginx" {
			t.Fatalf("zfs request = %#v, want nginx", req)
		}
		return catchrpc.ZFSServiceRootCandidatesResponse{
			State: catchrpc.ZFSRootDiscoveryAvailable,
			Candidates: []catchrpc.ZFSServiceRootCandidate{{
				Dataset:          "flash/yeet/services",
				Mountpoint:       "/flash/yeet/services",
				SuggestedDataset: "flash/yeet/services/nginx",
			}},
		}, nil
	}
	s := newRunWebServer(runWebServerConfig{
		Token:     "secret",
		Root:      t.TempDir(),
		Bootstrap: runWebBootstrap{SelectedHost: "yeet-lab"},
	})
	rec := runWebAPIRequest(t, s, http.MethodGet, "/api/host-storage?service=nginx", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("host storage status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body runWebHostStorageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Defaults.ServiceRoot != "flash/yeet/services/nginx" || !body.Defaults.ZFS || len(body.Warnings) != 0 {
		t.Fatalf("defaults = %#v warnings=%v, want legacy ZFS root without warnings", body.Defaults, body.Warnings)
	}
}

func TestRunWebAPIHostStorageInfersLegacyZFSPlaceholderBeforeServiceName(t *testing.T) {
	oldFetch := fetchRunWebHostStorageInfoFn
	oldFetchDefaults := fetchRunWebServiceRootDefaultsFn
	oldFetchZFS := fetchRunWebZFSRootCandidatesFn
	defer func() {
		fetchRunWebHostStorageInfoFn = oldFetch
		fetchRunWebServiceRootDefaultsFn = oldFetchDefaults
		fetchRunWebZFSRootCandidatesFn = oldFetchZFS
	}()

	fetchRunWebHostStorageInfoFn = func(ctx context.Context, host string) (serverInfo, error) {
		return serverInfo{
			RootDir:     "/flash/yeet/data",
			ServicesDir: "/flash/yeet/services",
		}, nil
	}
	fetchRunWebServiceRootDefaultsFn = func(ctx context.Context, host string, req catchrpc.ServiceRootDefaultsRequest) (catchrpc.ServiceRootDefaultsResponse, error) {
		return catchrpc.ServiceRootDefaultsResponse{}, errors.New(`unauthorized connection: unclassified RPC method "catch.ServiceRootDefaults"`)
	}
	fetchRunWebZFSRootCandidatesFn = func(ctx context.Context, host string, req catchrpc.ZFSServiceRootCandidatesRequest) (catchrpc.ZFSServiceRootCandidatesResponse, error) {
		if req.Service != "" {
			t.Fatalf("zfs request = %#v, want blank service", req)
		}
		return catchrpc.ZFSServiceRootCandidatesResponse{
			State: catchrpc.ZFSRootDiscoveryAvailable,
			Candidates: []catchrpc.ZFSServiceRootCandidate{{
				Dataset:          "flash/yeet/services",
				Mountpoint:       "/flash/yeet/services",
				SuggestedDataset: "flash/yeet/services/",
			}},
		}, nil
	}
	s := newRunWebServer(runWebServerConfig{
		Token:     "secret",
		Root:      t.TempDir(),
		Bootstrap: runWebBootstrap{SelectedHost: "yeet-lab"},
	})
	rec := runWebAPIRequest(t, s, http.MethodGet, "/api/host-storage", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("host storage status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body runWebHostStorageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Defaults.ServiceRoot != "" || body.Defaults.ServiceRootZFS != "flash/yeet/services" || !body.Defaults.ZFS || len(body.Warnings) != 0 {
		t.Fatalf("defaults = %#v warnings=%v, want blank legacy ZFS placeholder default", body.Defaults, body.Warnings)
	}
	if body.Defaults.ServiceRootPlaceholder != "flash/yeet/services/<service>" {
		t.Fatalf("placeholder = %q, want flash/yeet/services/<service>", body.Defaults.ServiceRootPlaceholder)
	}
}

func TestRunWebAPIHostStorageMapsErrorsToStates(t *testing.T) {
	oldFetch := fetchRunWebHostStorageInfoFn
	defer func() { fetchRunWebHostStorageInfoFn = oldFetch }()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "host unreachable", err: syscall.ECONNREFUSED, want: "host-unreachable"},
		{name: "other error", err: errors.New("permission denied"), want: "error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fetchRunWebHostStorageInfoFn = func(ctx context.Context, host string) (serverInfo, error) {
				return serverInfo{}, tt.err
			}
			s := newRunWebServer(runWebServerConfig{
				Token:     "secret",
				Root:      t.TempDir(),
				Bootstrap: runWebBootstrap{SelectedHost: "yeet-lab"},
			})
			rec := runWebAPIRequest(t, s, http.MethodGet, "/api/host-storage", nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			var body runWebHostStorageResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.State != tt.want {
				t.Fatalf("state = %q, want %q body=%#v", body.State, tt.want, body)
			}
		})
	}
}

func TestRunWebAPIHostStorageRejectsBadMethods(t *testing.T) {
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: t.TempDir()})
	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/host-storage", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405 body=%s", rec.Code, rec.Body.String())
	}
}

func TestRunWebAPIVMDefaultsUsesSelectedHost(t *testing.T) {
	oldFetch := fetchRunWebVMDefaultsFn
	defer func() { fetchRunWebVMDefaultsFn = oldFetch }()

	var gotHost string
	var gotReq catchrpc.VMDefaultsRequest
	fetchRunWebVMDefaultsFn = func(ctx context.Context, host string, req catchrpc.VMDefaultsRequest) (catchrpc.VMDefaultsResponse, error) {
		gotHost = host
		gotReq = req
		return catchrpc.VMDefaultsResponse{
			CPUs:        4,
			Memory:      "4g",
			MemoryBytes: 4 << 30,
			Disk:        "128g",
			DiskBytes:   128 << 30,
			DiskBackend: "zvol",
		}, nil
	}
	s := newRunWebServer(runWebServerConfig{
		Token:     "secret",
		Root:      t.TempDir(),
		Bootstrap: runWebBootstrap{SelectedHost: "yeet-lab"},
	})
	rec := runWebAPIRequest(t, s, http.MethodGet, "/api/vm-defaults?service=devbox&serviceRoot=flash/yeet/vms/devbox&zfs=true", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("vm defaults status = %d body=%s", rec.Code, rec.Body.String())
	}
	if gotHost != "yeet-lab" {
		t.Fatalf("host = %q, want yeet-lab", gotHost)
	}
	if gotReq.Service != "devbox" || gotReq.ServiceRoot != "flash/yeet/vms/devbox" || !gotReq.ZFS {
		t.Fatalf("request = %#v, want devbox ZFS request", gotReq)
	}
	var body runWebVMDefaultsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.State != "available" || body.Defaults.CPUs != 4 || body.Defaults.Memory != "4g" || body.Defaults.Disk != "128g" {
		t.Fatalf("response = %#v, want available 4/4g/128g", body)
	}

	rec = runWebAPIRequest(t, s, http.MethodGet, "/api/vm-defaults?host=yeet-storage&service=devbox", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("vm defaults explicit host status = %d body=%s", rec.Code, rec.Body.String())
	}
	if gotHost != "yeet-storage" {
		t.Fatalf("explicit host = %q, want yeet-storage", gotHost)
	}
}

func TestRunWebAPIVMDefaultsMapsErrorsToStates(t *testing.T) {
	oldFetch := fetchRunWebVMDefaultsFn
	defer func() { fetchRunWebVMDefaultsFn = oldFetch }()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "unsupported rpc", err: errors.New("rpc error -32601: method not found"), want: "unsupported-rpc"},
		{name: "host unreachable", err: syscall.ECONNREFUSED, want: "host-unreachable"},
		{name: "other error", err: errors.New("permission denied"), want: "error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fetchRunWebVMDefaultsFn = func(ctx context.Context, host string, req catchrpc.VMDefaultsRequest) (catchrpc.VMDefaultsResponse, error) {
				return catchrpc.VMDefaultsResponse{}, tt.err
			}
			s := newRunWebServer(runWebServerConfig{
				Token:     "secret",
				Root:      t.TempDir(),
				Bootstrap: runWebBootstrap{SelectedHost: "yeet-lab"},
			})
			rec := runWebAPIRequest(t, s, http.MethodGet, "/api/vm-defaults?service=devbox", nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			var body runWebVMDefaultsResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.State != tt.want {
				t.Fatalf("state = %q, want %q body=%#v", body.State, tt.want, body)
			}
		})
	}
}

func TestRunWebAPIVMDefaultsRejectsBadMethods(t *testing.T) {
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: t.TempDir()})
	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/vm-defaults", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405 body=%s", rec.Code, rec.Body.String())
	}
}

func TestRunWebAPIStaticAssetsRequireAuth(t *testing.T) {
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: t.TempDir()})

	req := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want 401", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/app.js?token=secret", nil)
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authorized status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("Content-Type = %q, want javascript", ct)
	}

	req = httptest.NewRequest(http.MethodGet, "/app.js", nil)
	req.AddCookie(&http.Cookie{Name: runWebTokenCookieName, Value: "secret"})
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("cookie auth status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
}

func TestRunWebAPIStaticIndexDoesNotSpreadTokenToAssets(t *testing.T) {
	s := newRunWebServer(runWebServerConfig{Token: "secret", CSRFToken: "csrf-value", Root: t.TempDir()})

	req := httptest.NewRequest(http.MethodGet, "/?token=secret", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("index status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, forbidden := range []string{"/styles.css?token=", "/app.js?token=", "/yeet-mark.svg?token=", "href=\"/?token="} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("index contains %q; body=%s", forbidden, body)
		}
	}
	if strings.Contains(body, "secret") {
		t.Fatalf("index body leaked token: %s", body)
	}
	if !strings.Contains(body, "history.replaceState") {
		t.Fatalf("index body missing token removal script: %s", body)
	}
	if !strings.Contains(body, "window.__YEET_CSRF_TOKEN__") {
		t.Fatalf("index body missing csrf script: %s", body)
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("Referrer-Policy = %q, want no-referrer", got)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != runWebTokenCookieName || cookies[0].Value != "secret" || !cookies[0].HttpOnly {
		t.Fatalf("cookies = %#v, want http-only token cookie", cookies)
	}

	req = httptest.NewRequest(http.MethodGet, "/styles.css", nil)
	req.AddCookie(cookies[0])
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("cookie asset status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
}

func TestRunWebAPIUnsafeRequestsNeedTokenOrCSRFHeader(t *testing.T) {
	s := newRunWebServer(runWebServerConfig{Token: "secret", CSRFToken: "csrf-value", Root: t.TempDir()})

	req := httptest.NewRequest(http.MethodPost, "/api/validate", strings.NewReader(`{}`))
	req.AddCookie(&http.Cookie{Name: runWebTokenCookieName, Value: "secret"})
	req.Header.Set("Origin", "http://127.0.0.1:9999")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cookie-only unsafe status = %d, want 403 body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/validate", strings.NewReader(`{}`))
	req.AddCookie(&http.Cookie{Name: runWebTokenCookieName, Value: "secret"})
	req.Header.Set("Origin", "http://127.0.0.1:9999")
	req.Header.Set("X-Yeet-Run-CSRF", "csrf-value")
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("csrf unsafe status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/validate?token=secret", strings.NewReader(`{}`))
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("query-token unsafe status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
}

func TestRunWebAPIStaticRejectsBadMethodsAndTraversal(t *testing.T) {
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: t.TempDir()})

	rec := runWebAPIRequest(t, s, http.MethodPost, "/app.js", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("post static status = %d, want 405", rec.Code)
	}

	for _, target := range []string{"/%2e%2e/run_web_api.go", "/assets/%2e%2e/run_web_api.go"} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("X-Yeet-Run-Token", "secret")
		rec = httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404 body=%s", target, rec.Code, rec.Body.String())
		}
	}
}

func TestRunWebAPIRedactsTSAuthKeyInValidationResponses(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	defer func() { fetchRunDraftServiceInfoFn = oldInfo }()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}

	root := t.TempDir()
	payload := filepath.Join(root, "run.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: root})
	draft := RunDraft{
		Service: "svc-a",
		Host:    "host-a",
		Payload: "run.sh",
		Network: RunDraftNetwork{
			TSAuthKey: "tskey-secret",
		},
	}
	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/validate", draft)
	if rec.Code != http.StatusOK {
		t.Fatalf("validate status = %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "tskey-secret") {
		t.Fatalf("validate body leaked ts auth key: %s", rec.Body.String())
	}

	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: true}, nil
	}
	rec = runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", draft)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("deploy status = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "tskey-secret") {
		t.Fatalf("deploy body leaked ts auth key: %s", rec.Body.String())
	}
}

func TestRunWebAPIValidateRejectsInvalidServiceName(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	defer func() { fetchRunDraftServiceInfoFn = oldInfo }()
	called := false
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		called = true
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: t.TempDir()})
	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/validate", RunDraft{
		Service: "bad.name",
		Host:    "host-a",
		Payload: "ghcr.io/example/app:latest",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("validate status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp runWebValidateResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode validate response: %v", err)
	}
	if resp.Validation.OK {
		t.Fatal("validation OK = true, want false")
	}
	if got := resp.Validation.fieldError("service"); !strings.Contains(got, "invalid service name") {
		t.Fatalf("service error = %q, want invalid service name", got)
	}
	if called {
		t.Fatal("service info lookup ran for invalid service name")
	}
}

func TestRunWebAPIValidateReturnsCommandPreview(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	defer func() { fetchRunDraftServiceInfoFn = oldInfo }()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}

	root := t.TempDir()
	payload := filepath.Join(root, "compose.yml")
	if err := os.WriteFile(payload, []byte("services:\n  app:\n    image: nginx\n"), 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: root})
	draft := RunDraft{
		Service: "app",
		Host:    "yeet-lab",
		Payload: "compose.yml",
		Network: RunDraftNetwork{
			Modes: []string{"svc", "lan"},
		},
		Storage: RunDraftStorage{ServiceRoot: "tank/apps/app", ZFS: true},
	}
	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/validate", draft)
	if rec.Code != http.StatusOK {
		t.Fatalf("validate status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for _, want := range []string{"yeet run app@yeet-lab", "--net=svc,lan", "--service-root=tank/apps/app", "--zfs"} {
		if !strings.Contains(body.Command, want) {
			t.Fatalf("command = %q, missing %q", body.Command, want)
		}
	}
}

func TestRunWebAPIValidateAndDeploy(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	var deployed RunDraft
	done := make(chan struct{})
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
		deployed = draft
		close(done)
		return nil
	}
	root := t.TempDir()
	payload := filepath.Join(root, "run.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	envFile := filepath.Join(root, ".env")
	if err := os.WriteFile(envFile, []byte("A=B\n"), 0o644); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	s := newRunWebServer(runWebServerConfig{
		Token: "secret",
		Root:  root,
		Config: &projectConfigLocation{
			Dir:    root,
			Config: &ProjectConfig{Version: projectConfigVersion},
		},
	})
	draft := RunDraft{Service: "svc-a", Host: "host-a", Payload: "run.sh", EnvFile: ".env"}
	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/validate", draft)
	if rec.Code != http.StatusOK {
		t.Fatalf("validate status = %d body=%s", rec.Code, rec.Body.String())
	}
	rec = runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", draft)
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy status = %d body=%s", rec.Code, rec.Body.String())
	}
	jobID := decodeRunWebDeployStarted(t, rec).JobID
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for background deploy")
	}
	if deployed.Service != "svc-a" || deployed.Host != "host-a" || deployed.Payload != payload || !deployed.NewServiceOnly {
		t.Fatalf("deployed = %#v, want normalized new-service draft", deployed)
	}
	if deployed.EnvFile != envFile || !deployed.EnvFileSet || deployed.EnvFileArg != envFile {
		t.Fatalf("deployed env = file:%q set:%v arg:%q, want normalized explicit env", deployed.EnvFile, deployed.EnvFileSet, deployed.EnvFileArg)
	}
	waitRunWebJobState(t, s, jobID, runWebJobSucceeded)
}

func TestRunWebAPIDeployCronDraft(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	var deployed RunDraft
	done := make(chan struct{})
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
		deployed = draft
		close(done)
		return nil
	}

	root := t.TempDir()
	payload := filepath.Join(root, "job.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: root})
	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", RunDraft{
		Service:     "backup",
		Host:        "yeet-lab",
		Payload:     "job.sh",
		PayloadKind: serviceTypeCron,
		Cron:        RunDraftCron{Schedule: "0 3 * * *"},
		PayloadArgs: []string{"--full"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy status = %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for cron deploy")
	}
	if deployed.PayloadKind != serviceTypeCron || deployed.Cron.Schedule != "0 3 * * *" || !reflect.DeepEqual(deployed.PayloadArgs, []string{"--full"}) {
		t.Fatalf("deployed cron draft = %#v", deployed)
	}
}

func TestRunWebAPIDeployStartsJobWithoutWaitingForCompletion(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
		startedOnce.Do(func() { close(started) })
		<-release
		return nil
	}

	root := t.TempDir()
	writeRunWebTestPayload(t, root)
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: root})

	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", RunDraft{Service: "svc-a", Host: "host-a", Payload: "run.sh"})
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy status = %d body=%s", rec.Code, rec.Body.String())
	}
	startedResp := decodeRunWebDeployStarted(t, rec)
	if !startedResp.OK || startedResp.JobID == "" {
		t.Fatalf("deploy response = %#v, want ok with job ID", startedResp)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for background deploy to start")
	}
	close(release)
	waitRunWebJobState(t, s, startedResp.JobID, runWebJobSucceeded)
}

func TestRunWebAPIDeployJournalCreationFailureDoesNotExecute(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	executed := false
	executeRunDraftWithOptionsFn = func(context.Context, RunDraft, *projectConfigLocation, runDraftExecuteOptions) error {
		executed = true
		return nil
	}

	root := t.TempDir()
	writeRunWebTestPayload(t, root)
	want := errors.New("journal creation failed")
	s := newRunWebServer(runWebServerConfig{
		Token: "secret",
		Root:  root,
		NewJournal: func(string, int64) (runWebEventJournal, error) {
			return nil, want
		},
	})

	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", RunDraft{
		Service: "svc-a",
		Host:    "host-a",
		Payload: "run.sh",
	})
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), want.Error()) {
		t.Fatalf("deploy = %d %q, want 500 with journal creation error", rec.Code, rec.Body.String())
	}
	if executed {
		t.Fatal("executor called after journal creation failed")
	}
}

func TestRunWebAPIDeployStreamReplaysOutputAndStatus(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
		_, _ = io.WriteString(opts.Stdout, "deploying\n")
		return nil
	}

	root := t.TempDir()
	writeRunWebTestPayload(t, root)
	var terminal bytes.Buffer
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: root, Out: &terminal})
	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", RunDraft{Service: "svc-a", Host: "host-a", Payload: "run.sh"})
	jobID := decodeRunWebDeployStarted(t, rec).JobID
	waitRunWebJobState(t, s, jobID, runWebJobSucceeded)

	stream := runWebAPIRequest(t, s, http.MethodGet, "/api/deploy/"+jobID+"/stream", nil)
	if stream.Code != http.StatusOK {
		t.Fatalf("stream status = %d body=%s", stream.Code, stream.Body.String())
	}
	if ct := stream.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("stream Content-Type = %q, want text/event-stream", ct)
	}
	events := parseRunWebSSE(t, stream.Body.String())
	if len(events) != 3 {
		t.Fatalf("events = %#v, want terminal, output, and status", events)
	}
	if events[0].Name != "terminal" || events[0].ID == "" ||
		events[0].Data != `{"tty":false,"cols":80,"rows":24,"scrollback":1000}` {
		t.Fatalf("first event = %#v, want stable non-TTY terminal profile with id", events[0])
	}
	if events[1].Name != "output" || events[1].ID == "" {
		t.Fatalf("second event = %#v, want output with id", events[1])
	}
	var output struct {
		Encoding string `json:"encoding"`
		Chunk    string `json:"chunk"`
	}
	if err := json.Unmarshal([]byte(events[1].Data), &output); err != nil {
		t.Fatalf("decode output data: %v", err)
	}
	chunk, err := base64.StdEncoding.DecodeString(output.Chunk)
	if err != nil {
		t.Fatalf("decode output chunk: %v", err)
	}
	if output.Encoding != "base64" || string(chunk) != "deploying\n" {
		t.Fatalf("output event = %#v chunk=%q, want deploying", output, string(chunk))
	}
	if events[2].Name != "status" || !strings.Contains(events[2].Data, `"state":"succeeded"`) {
		t.Fatalf("third event = %#v, want succeeded status", events[2])
	}
	if terminal.String() != "deploying\n" {
		t.Fatalf("terminal output = %q, want deploying", terminal.String())
	}
}

func TestRunWebAPIDeployStreamRetryAndCursorValidation(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	executeRunDraftWithOptionsFn = func(_ context.Context, _ RunDraft, _ *projectConfigLocation, opts runDraftExecuteOptions) error {
		_, _ = io.WriteString(opts.Stdout, "first\n")
		_, _ = io.WriteString(opts.Stdout, "second\n")
		return nil
	}

	root := t.TempDir()
	writeRunWebTestPayload(t, root)
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: root})
	t.Cleanup(func() {
		if err := s.(*runWebServer).close(); err != nil {
			t.Errorf("close server: %v", err)
		}
	})
	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", RunDraft{
		Service: "svc-a",
		Host:    "host-a",
		Payload: "run.sh",
	})
	jobID := decodeRunWebDeployStarted(t, rec).JobID
	waitRunWebJobState(t, s, jobID, runWebJobSucceeded)

	streamPath := "/api/deploy/" + jobID + "/stream"
	stream := runWebAPIRequest(t, s, http.MethodGet, streamPath, nil)
	if stream.Code != http.StatusOK {
		t.Fatalf("stream status = %d body=%s", stream.Code, stream.Body.String())
	}
	if !strings.HasPrefix(stream.Body.String(), "retry: 250\n\n") {
		t.Fatalf("stream prefix = %q, want retry directive before events", stream.Body.String())
	}
	if got := stream.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", got)
	}
	if got := stream.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q, want no", got)
	}
	events := parseRunWebSSE(t, stream.Body.String())
	if len(events) != 3 {
		t.Fatalf("events = %#v, want terminal, combined output, status", events)
	}

	req := httptest.NewRequest(http.MethodGet, streamPath, nil)
	req.Header.Set("X-Yeet-Run-Token", "secret")
	req.Header.Set("Last-Event-ID", events[0].ID)
	replay := httptest.NewRecorder()
	s.ServeHTTP(replay, req)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay status = %d body=%s", replay.Code, replay.Body.String())
	}
	replayed := parseRunWebSSE(t, replay.Body.String())
	if len(replayed) != 2 || replayed[0].Name != "output" || replayed[1].Name != "status" {
		t.Fatalf("replayed events = %#v, want ordered output then status", replayed)
	}

	for _, tc := range []struct {
		name   string
		cursor string
		want   int
	}{
		{name: "malformed", cursor: "wat", want: http.StatusBadRequest},
		{name: "negative", cursor: "-1", want: http.StatusBadRequest},
		{name: "impossible", cursor: "1", want: http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, streamPath, nil)
			req.Header.Set("X-Yeet-Run-Token", "secret")
			req.Header.Set("Last-Event-ID", tc.cursor)
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("cursor %q status = %d, want %d body=%s", tc.cursor, rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestRunWebAPIDeployStreamRealHTTPReconnectPreservesRapidOutput(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}

	const chunkCount = 96
	chunks := make([][]byte, 0, chunkCount)
	var wantOutput bytes.Buffer
	for i := 0; i < chunkCount; i++ {
		chunk := []byte(fmt.Sprintf("chunk-%03d:%c\n", i, rune('a'+i%26)))
		chunks = append(chunks, chunk)
		wantOutput.Write(chunk)
	}
	tail := []byte("unique-tail-after-reader-pause\n")
	wantOutput.Write(tail)

	testCtx, cancelTest := context.WithCancel(context.Background())
	streamReady := make(chan struct{})
	outputBlocked := make(chan struct{})
	releaseOutput := make(chan struct{})
	var outputBlockedOnce sync.Once
	var releaseOutputOnce sync.Once
	releaseOutputGate := func() {
		releaseOutputOnce.Do(func() { close(releaseOutput) })
	}
	burstWritten := make(chan struct{})
	allowTail := make(chan struct{})
	writerCompleted := make(chan struct{})
	executeRunDraftWithOptionsFn = func(ctx context.Context, _ RunDraft, _ *projectConfigLocation, opts runDraftExecuteOptions) error {
		select {
		case <-streamReady:
		case <-ctx.Done():
			return ctx.Err()
		case <-testCtx.Done():
			return testCtx.Err()
		}
		for i, chunk := range chunks {
			if _, err := opts.Stdout.Write(chunk); err != nil {
				return err
			}
			if i == 0 {
				select {
				case <-outputBlocked:
				case <-ctx.Done():
					return ctx.Err()
				case <-testCtx.Done():
					return testCtx.Err()
				}
			}
		}
		close(burstWritten)
		select {
		case <-allowTail:
		case <-ctx.Done():
			return ctx.Err()
		case <-testCtx.Done():
			return testCtx.Err()
		}
		if _, err := opts.Stdout.Write(tail); err != nil {
			return err
		}
		close(writerCompleted)
		return nil
	}

	root := t.TempDir()
	writeRunWebTestPayload(t, root)
	completed := make(chan struct{})
	handler := newRunWebServer(runWebServerConfig{
		Token:      "secret",
		Root:       root,
		Out:        io.Discard,
		OnComplete: func() { close(completed) },
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/stream") && r.Header.Get("Last-Event-ID") == "" {
			w = &runWebOutputGateWriter{
				ResponseWriter: w,
				ctx:            r.Context(),
				cleanup:        testCtx.Done(),
				release:        releaseOutput,
				blocked: func() {
					outputBlockedOnce.Do(func() { close(outputBlocked) })
				},
			}
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		releaseOutputGate()
		cancelTest()
		server.CloseClientConnections()
		server.Close()
		if err := handler.(*runWebServer).close(); err != nil {
			t.Errorf("close web server: %v", err)
		}
	})

	deployBody, err := json.Marshal(RunDraft{Service: "svc-a", Host: "host-a", Payload: "run.sh"})
	if err != nil {
		t.Fatalf("encode deploy request: %v", err)
	}
	deployReq, err := http.NewRequest(http.MethodPost, server.URL+"/api/deploy", bytes.NewReader(deployBody))
	if err != nil {
		t.Fatalf("create deploy request: %v", err)
	}
	deployReq.Header.Set("X-Yeet-Run-Token", "secret")
	deployResp, err := server.Client().Do(deployReq)
	if err != nil {
		t.Fatalf("deploy request: %v", err)
	}
	deployResponseBody, err := io.ReadAll(deployResp.Body)
	_ = deployResp.Body.Close()
	if err != nil {
		t.Fatalf("read deploy response: %v", err)
	}
	if deployResp.StatusCode != http.StatusOK {
		t.Fatalf("deploy status = %d body=%s, want 200", deployResp.StatusCode, deployResponseBody)
	}
	var started runWebDeployStartedResponse
	if err := json.Unmarshal(deployResponseBody, &started); err != nil {
		t.Fatalf("decode deploy response %q: %v", deployResponseBody, err)
	}
	if !started.OK || started.JobID == "" {
		t.Fatalf("deploy response = %#v, want ok job ID", started)
	}

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	streamURL := server.URL + "/api/deploy/" + url.PathEscape(started.JobID) + "/stream"
	firstReq, err := http.NewRequestWithContext(firstCtx, http.MethodGet, streamURL, nil)
	if err != nil {
		t.Fatalf("create first stream request: %v", err)
	}
	firstReq.Header.Set("X-Yeet-Run-Token", "secret")
	firstResp, err := server.Client().Do(firstReq)
	if err != nil {
		t.Fatalf("open first stream: %v", err)
	}
	if firstResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(firstResp.Body)
		_ = firstResp.Body.Close()
		t.Fatalf("first stream status = %d body=%s, want 200", firstResp.StatusCode, body)
	}
	firstEvent := readRunWebSSEEvent(t, bufio.NewReader(firstResp.Body))
	if firstEvent.Name != string(runWebStreamTerminal) || firstEvent.ID == "" {
		t.Fatalf("first stream event = %#v, want terminal profile with event ID", firstEvent)
	}
	close(streamReady)

	select {
	case <-outputBlocked:
	case <-time.After(time.Second):
		t.Fatal("first output delivery did not reach the deterministic server-side gate")
	}
	select {
	case <-burstWritten:
	case <-time.After(2 * time.Second):
		t.Fatal("rapid terminal burst did not finish while the first HTTP reader was paused")
	}
	close(allowTail)
	select {
	case <-writerCompleted:
	case <-time.After(2 * time.Second):
		t.Fatal("terminal writer did not complete independently of the paused HTTP reader")
	}
	select {
	case <-completed:
		t.Fatal("server completed before the browser drained and acknowledged terminal output")
	default:
	}

	cancelFirst()
	_ = firstResp.Body.Close()
	releaseOutputGate()

	replayReq, err := http.NewRequest(http.MethodGet, streamURL, nil)
	if err != nil {
		t.Fatalf("create replay request: %v", err)
	}
	replayReq.Header.Set("X-Yeet-Run-Token", "secret")
	replayReq.Header.Set("Last-Event-ID", firstEvent.ID)
	replayResp, err := server.Client().Do(replayReq)
	if err != nil {
		t.Fatalf("open replay stream: %v", err)
	}
	replayBody, err := io.ReadAll(replayResp.Body)
	_ = replayResp.Body.Close()
	if err != nil {
		t.Fatalf("read replay stream: %v", err)
	}
	if replayResp.StatusCode != http.StatusOK {
		t.Fatalf("replay status = %d body=%s, want 200", replayResp.StatusCode, replayBody)
	}
	events := parseRunWebSSE(t, string(replayBody))
	if got := decodeRunWebOutputText(t, events); got != wantOutput.String() {
		t.Fatalf("reconnected output length = %d, want %d exact once", len(got), wantOutput.Len())
	}
	if len(events) < 2 {
		t.Fatalf("reconnected events = %#v, want output followed by final status", events)
	}
	for i, event := range events[:len(events)-1] {
		if event.Name != string(runWebStreamOutput) {
			t.Fatalf("reconnected event %d = %#v, want output before final status", i, event)
		}
	}
	last := events[len(events)-1]
	if last.Name != string(runWebStreamStatus) || last.Data != `{"state":"succeeded"}` {
		t.Fatalf("final replay event = %#v, want succeeded status", last)
	}
	select {
	case <-completed:
		t.Fatal("server completed before the post-drain acknowledgement")
	default:
	}

	ackReq, err := http.NewRequest(http.MethodPost, server.URL+"/api/deploy/"+url.PathEscape(started.JobID)+"/ack", nil)
	if err != nil {
		t.Fatalf("create acknowledgement request: %v", err)
	}
	ackReq.Header.Set("X-Yeet-Run-Token", "secret")
	ackResp, err := server.Client().Do(ackReq)
	if err != nil {
		t.Fatalf("acknowledge drained stream: %v", err)
	}
	_ = ackResp.Body.Close()
	if ackResp.StatusCode != http.StatusNoContent {
		t.Fatalf("acknowledgement status = %d, want 204", ackResp.StatusCode)
	}
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for completion after post-drain acknowledgement")
	}
}

func TestRunWebAPIDeployUsesConfiguredTerminalProfileAndResizeStream(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	resizes := make(chan catchrpc.Resize)
	resizeAppended := make(chan struct{})
	journal := &runWebResizeSignalJournal{
		runWebEventJournal: newRunWebMemoryJournal(),
		appended:           resizeAppended,
	}
	executeRunDraftWithOptionsFn = func(ctx context.Context, _ RunDraft, _ *projectConfigLocation, opts runDraftExecuteOptions) error {
		terminal, ok := remoteExecTerminalStateFromContext(ctx)
		if !ok {
			return errors.New("deploy context did not contain shared terminal state")
		}
		cols, rows, remoteResize, stopResize := terminal.subscribe()
		if stopResize != nil {
			defer stopResize()
		}
		if !terminal.TTY || cols != 120 || rows != 40 || terminal.Term != "xterm-256color" {
			return fmt.Errorf("shared terminal state = %#v, want configured profile", terminal)
		}
		if _, err := io.WriteString(opts.Stdout, "before"); err != nil {
			return err
		}
		resizes <- catchrpc.Resize{Cols: 132, Rows: 44}
		select {
		case resize := <-remoteResize:
			if resize != (catchrpc.Resize{Cols: 132, Rows: 44}) {
				return fmt.Errorf("remote resize = %#v, want 132x44", resize)
			}
		case <-time.After(time.Second):
			return errors.New("timed out waiting for shared remote resize")
		}
		select {
		case <-resizeAppended:
		default:
			return errors.New("remote resize was delivered before its journal event")
		}
		if _, err := io.WriteString(opts.Stdout, "after"); err != nil {
			return err
		}
		return nil
	}

	root := t.TempDir()
	writeRunWebTestPayload(t, root)
	profile := runWebTerminalProfile{
		TTY: true, Cols: 120, Rows: 40, Term: "xterm-256color", Scrollback: 1000,
	}
	var profileCalls, resizeCalls int
	s := newRunWebServer(runWebServerConfig{
		Token: "secret",
		Root:  root,
		TerminalProfile: func() runWebTerminalProfile {
			profileCalls++
			return profile
		},
		TerminalResize: func(ctx context.Context) <-chan catchrpc.Resize {
			if ctx == nil {
				t.Fatal("TerminalResize received nil context")
			}
			resizeCalls++
			return resizes
		},
		NewJournal: func(string, int64) (runWebEventJournal, error) {
			return journal, nil
		},
	})
	t.Cleanup(func() { _ = s.(*runWebServer).close() })

	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", RunDraft{
		Service: "svc-a",
		Host:    "host-a",
		Payload: "run.sh",
	})
	jobID := decodeRunWebDeployStarted(t, rec).JobID
	waitRunWebJobState(t, s, jobID, runWebJobSucceeded)
	stream := runWebAPIRequest(t, s, http.MethodGet, "/api/deploy/"+jobID+"/stream", nil)
	events := parseRunWebSSE(t, stream.Body.String())
	if len(events) != 5 {
		t.Fatalf("events = %#v, want terminal, output, resize, output, status", events)
	}
	if events[0].Name != "terminal" || events[0].Data != `{"tty":true,"cols":120,"rows":40,"term":"xterm-256color","scrollback":1000}` {
		t.Fatalf("terminal event = %#v, want configured profile", events[0])
	}
	if events[1].Name != "output" || decodeRunWebOutputText(t, events[1:2]) != "before" ||
		events[2].Name != "resize" || events[2].Data != `{"cols":132,"rows":44}` ||
		events[3].Name != "output" || decodeRunWebOutputText(t, events[3:4]) != "after" ||
		events[4].Name != "status" {
		t.Fatalf("events = %#v, want exact terminal/output/resize/output/status ordering", events)
	}
	if profileCalls != 1 || resizeCalls != 1 {
		t.Fatalf("profile calls = %d resize calls = %d, want one each", profileCalls, resizeCalls)
	}
}

func TestRunWebAPIDeployStreamMirrorsStderr(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
		if opts.Stderr == nil {
			return errors.New("stderr writer was nil")
		}
		_, _ = io.WriteString(opts.Stderr, "stderr writer line\n")
		return errors.New("deploy failed")
	}

	root := t.TempDir()
	writeRunWebTestPayload(t, root)
	var terminal bytes.Buffer
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: root, Out: &terminal})
	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", RunDraft{Service: "svc-a", Host: "host-a", Payload: "run.sh"})
	jobID := decodeRunWebDeployStarted(t, rec).JobID
	waitRunWebJobState(t, s, jobID, runWebJobFailed)

	stream := runWebAPIRequest(t, s, http.MethodGet, "/api/deploy/"+jobID+"/stream", nil)
	if stream.Code != http.StatusOK {
		t.Fatalf("stream status = %d body=%s", stream.Code, stream.Body.String())
	}
	output := decodeRunWebOutputText(t, parseRunWebSSE(t, stream.Body.String()))
	for _, want := range []string{"stderr writer line\n", "Error: deploy failed\n"} {
		if !strings.Contains(output, want) {
			t.Fatalf("stream output missing %q:\n%s", want, output)
		}
		if !strings.Contains(terminal.String(), want) {
			t.Fatalf("terminal output missing %q:\n%s", want, terminal.String())
		}
	}
}

func TestRunWebAPIDeploySurfacesPermissionErrorInJobOutput(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
		return errors.New(`missing yeet permission "manage"; update your Tailscale grant for yeetrun.com/app/yeet:
https://yeetrun.com/docs/security/tailscale-access-grants`)
	}

	root := t.TempDir()
	writeRunWebTestPayload(t, root)
	var terminal bytes.Buffer
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: root, Out: &terminal})
	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", RunDraft{Service: "svc-a", Host: "host-a", Payload: "run.sh"})
	jobID := decodeRunWebDeployStarted(t, rec).JobID
	waitRunWebJobState(t, s, jobID, runWebJobFailed)

	stream := runWebAPIRequest(t, s, http.MethodGet, "/api/deploy/"+jobID+"/stream", nil)
	output := decodeRunWebOutputText(t, parseRunWebSSE(t, stream.Body.String()))
	for _, want := range []string{`missing yeet permission "manage"`, "https://yeetrun.com/docs/security/tailscale-access-grants"} {
		if !strings.Contains(output, want) {
			t.Fatalf("stream output missing %q:\n%s", want, output)
		}
		if !strings.Contains(terminal.String(), want) {
			t.Fatalf("terminal output missing %q:\n%s", want, terminal.String())
		}
	}
}

func TestRunWebAPIDeployKeepsTerminalBackedOutputTTY(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	oldIsTerminal := isTerminalFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
		isTerminalFn = oldIsTerminal
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer func() { _ = stdoutR.Close() }()
	defer func() { _ = stdoutW.Close() }()
	isTerminalFn = func(fd int) bool {
		return fd == int(stdoutW.Fd())
	}
	gotTTY := make(chan bool, 1)
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
		gotTTY <- isWriterTerminal(opts.Stdout)
		return nil
	}

	root := t.TempDir()
	writeRunWebTestPayload(t, root)
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: root, Out: stdoutW})
	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", RunDraft{Service: "svc-a", Host: "host-a", Payload: "run.sh"})
	jobID := decodeRunWebDeployStarted(t, rec).JobID
	waitRunWebJobState(t, s, jobID, runWebJobSucceeded)

	select {
	case tty := <-gotTTY:
		if !tty {
			t.Fatal("web deploy stdout was not terminal-backed; catch would render plain progress instead of TTY output")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for deploy execution")
	}
}

func TestRunWebAPISuccessfulJobWaitsForAckAndCompletesExactlyOnce(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
		return nil
	}
	completed := make(chan struct{})
	var completedOnce sync.Once
	var completedMu sync.Mutex
	completedCalls := 0
	root := t.TempDir()
	writeRunWebTestPayload(t, root)
	s := newRunWebServer(runWebServerConfig{
		Token: "secret",
		Root:  root,
		OnComplete: func() {
			completedMu.Lock()
			completedCalls++
			completedMu.Unlock()
			completedOnce.Do(func() { close(completed) })
		},
	})
	t.Cleanup(func() { _ = s.(*runWebServer).close() })
	draft := RunDraft{Service: "svc-a", Host: "host-a", Payload: "run.sh"}

	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", draft)
	jobID := decodeRunWebDeployStarted(t, rec).JobID
	waitRunWebJobState(t, s, jobID, runWebJobSucceeded)
	select {
	case <-completed:
		t.Fatal("OnComplete ran before browser acknowledgement")
	case <-time.After(50 * time.Millisecond):
	}
	for i := 0; i < 2; i++ {
		ack := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy/"+jobID+"/ack", nil)
		if ack.Code != http.StatusNoContent {
			t.Fatalf("ack %d status = %d, want 204 body=%s", i+1, ack.Code, ack.Body.String())
		}
	}
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for OnComplete")
	}
	time.Sleep(25 * time.Millisecond)
	completedMu.Lock()
	if completedCalls != 1 {
		t.Fatalf("OnComplete calls = %d, want exactly 1", completedCalls)
	}
	completedMu.Unlock()
	again := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", draft)
	if again.Code != http.StatusConflict {
		t.Fatalf("second deploy status = %d, want 409 body=%s", again.Code, again.Body.String())
	}
}

func TestRunWebAPICompletionFallbackAndSessionClose(t *testing.T) {
	for _, tc := range []struct {
		name    string
		release func(http.Handler, string)
	}{
		{name: "fallback"},
		{
			name: "session close",
			release: func(s http.Handler, _ string) {
				rec := runWebAPIRequest(t, s, http.MethodPost, "/api/session/closed", nil)
				if rec.Code != http.StatusNoContent {
					t.Fatalf("session close status = %d body=%s", rec.Code, rec.Body.String())
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restore := stubRunWebSuccessfulDeploy(t)
			defer restore()
			completed := make(chan struct{})
			root := t.TempDir()
			writeRunWebTestPayload(t, root)
			cfg := runWebServerConfig{
				Token: "secret",
				Root:  root,
				OnComplete: func() {
					close(completed)
				},
			}
			if tc.release == nil {
				cfg.CompletionAckTimeout = 25 * time.Millisecond
			} else {
				cfg.CompletionAckTimeout = time.Second
			}
			s := newRunWebServer(cfg)
			t.Cleanup(func() { _ = s.(*runWebServer).close() })
			rec := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", RunDraft{
				Service: "svc-a", Host: "host-a", Payload: "run.sh",
			})
			jobID := decodeRunWebDeployStarted(t, rec).JobID
			waitRunWebJobState(t, s, jobID, runWebJobSucceeded)
			if tc.release != nil {
				tc.release(s, jobID)
			}
			select {
			case <-completed:
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for completion release")
			}
		})
	}
}

func TestRunWebAPIDefaultCompletionTimeoutAndNilContext(t *testing.T) {
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: t.TempDir()}).(*runWebServer)
	t.Cleanup(func() { _ = s.close() })
	if s.ctx == nil {
		t.Fatal("server context is nil")
	}
	if got := s.completionAckTimeout(); got != 10*time.Second {
		t.Fatalf("completion acknowledgement timeout = %s, want 10s", got)
	}
}

func TestRunWebAPIDegradedSuccessUsesCompletionFallback(t *testing.T) {
	restore := stubRunWebSuccessfulDeploy(t)
	defer restore()
	completed := make(chan struct{})
	root := t.TempDir()
	writeRunWebTestPayload(t, root)
	s := newRunWebServer(runWebServerConfig{
		Token:                "secret",
		Root:                 root,
		CompletionAckTimeout: 25 * time.Millisecond,
		OnComplete:           func() { close(completed) },
		NewJournal: func(string, int64) (runWebEventJournal, error) {
			return &runWebFailAfterTerminalJournal{
				runWebEventJournal: newRunWebMemoryJournal(),
				err:                errors.New("journal write failed"),
			}, nil
		},
	})
	t.Cleanup(func() { _ = s.(*runWebServer).close() })
	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", RunDraft{
		Service: "svc-a", Host: "host-a", Payload: "run.sh",
	})
	jobID := decodeRunWebDeployStarted(t, rec).JobID
	status := waitRunWebJobState(t, s, jobID, runWebJobSucceeded)
	if !status.Degraded {
		t.Fatalf("status = %#v, want degraded success", status)
	}
	ack := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy/"+jobID+"/ack", nil)
	if ack.Code != http.StatusConflict {
		t.Fatalf("degraded ack status = %d, want 409 body=%s", ack.Code, ack.Body.String())
	}
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for degraded-success completion fallback")
	}
}

func TestRunWebAPIFailedAndCanceledJobsNeverComplete(t *testing.T) {
	tests := []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
		exec func(context.Context) error
	}{
		{
			name: "failed",
			ctx:  func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
			exec: func(context.Context) error {
				return errors.New("deploy failed")
			},
		},
		{
			name: "context cancellation",
			ctx:  func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
			exec: func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldInfo := fetchRunDraftServiceInfoFn
			oldExecDraft := executeRunDraftWithOptionsFn
			defer func() {
				fetchRunDraftServiceInfoFn = oldInfo
				executeRunDraftWithOptionsFn = oldExecDraft
			}()
			fetchRunDraftServiceInfoFn = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
				return catchrpc.ServiceInfoResponse{Found: false}, nil
			}
			executeRunDraftWithOptionsFn = func(ctx context.Context, _ RunDraft, _ *projectConfigLocation, _ runDraftExecuteOptions) error {
				return tc.exec(ctx)
			}
			ctx, cancel := tc.ctx()
			root := t.TempDir()
			writeRunWebTestPayload(t, root)
			completed := make(chan struct{})
			s := newRunWebServer(runWebServerConfig{
				Token:                "secret",
				Root:                 root,
				Context:              ctx,
				CompletionAckTimeout: 25 * time.Millisecond,
				OnComplete:           func() { close(completed) },
			})
			t.Cleanup(func() { _ = s.(*runWebServer).close() })
			rec := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", RunDraft{
				Service: "svc-a", Host: "host-a", Payload: "run.sh",
			})
			jobID := decodeRunWebDeployStarted(t, rec).JobID
			if tc.name == "context cancellation" {
				cancel()
			} else {
				defer cancel()
			}
			waitRunWebJobState(t, s, jobID, runWebJobFailed)
			select {
			case <-completed:
				t.Fatal("OnComplete ran for unsuccessful job")
			case <-time.After(75 * time.Millisecond):
			}
		})
	}
}

func TestRunWebAPISuccessIsNotPublishedBeforeServerIsComplete(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	release := make(chan struct{})
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
		<-release
		return nil
	}

	root := t.TempDir()
	writeRunWebTestPayload(t, root)
	handler := newRunWebServer(runWebServerConfig{Token: "secret", Root: root})
	server := handler.(*runWebServer)
	rec := runWebAPIRequest(t, handler, http.MethodPost, "/api/deploy", RunDraft{Service: "svc-a", Host: "host-a", Payload: "run.sh"})
	jobID := decodeRunWebDeployStarted(t, rec).JobID
	job, ok := server.lookupJob(jobID)
	if !ok {
		t.Fatalf("job %q not found", jobID)
	}

	server.deployMu.Lock()
	close(release)
	select {
	case <-job.done:
		server.deployMu.Unlock()
		t.Fatal("job published terminal success before server completion could be marked")
	case <-time.After(25 * time.Millisecond):
	}
	if status := job.status(); status.State == runWebJobSucceeded {
		server.deployMu.Unlock()
		t.Fatalf("job status = %s while server completion lock is held, want not externally succeeded", status.State)
	}
	server.deployMu.Unlock()

	status := waitRunWebJobState(t, handler, jobID, runWebJobSucceeded)
	if status.State != runWebJobSucceeded {
		t.Fatalf("status = %#v, want succeeded", status)
	}
	server.deployMu.Lock()
	complete := server.complete
	server.deployMu.Unlock()
	if !complete {
		t.Fatal("server complete = false after job succeeded")
	}
}

func TestRunWebAPIFailedJobAllowsRetry(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	var mu sync.Mutex
	calls := 0
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			return errors.New("deploy failed")
		}
		return nil
	}

	root := t.TempDir()
	writeRunWebTestPayload(t, root)
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: root})
	draft := RunDraft{Service: "svc-a", Host: "host-a", Payload: "run.sh"}

	first := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", draft)
	firstID := decodeRunWebDeployStarted(t, first).JobID
	waitRunWebJobState(t, s, firstID, runWebJobFailed)
	second := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", draft)
	secondID := decodeRunWebDeployStarted(t, second).JobID
	if secondID == firstID {
		t.Fatalf("second job id = %q, want new job id", secondID)
	}
	waitRunWebJobState(t, s, secondID, runWebJobSucceeded)
}

func TestRunWebAPIRetryClosesFailedJournalBeforeReplacingJob(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	var execMu sync.Mutex
	execCalls := 0
	executeRunDraftWithOptionsFn = func(context.Context, RunDraft, *projectConfigLocation, runDraftExecuteOptions) error {
		execMu.Lock()
		defer execMu.Unlock()
		execCalls++
		if execCalls == 1 {
			return errors.New("deploy failed")
		}
		return nil
	}
	firstJournal := newRunWebMemoryJournal()
	secondJournal := newRunWebMemoryJournal()
	journals := []runWebEventJournal{firstJournal, secondJournal}
	var journalMu sync.Mutex
	s := newRunWebServer(runWebServerConfig{
		Token: "secret",
		Root:  t.TempDir(),
		NewJournal: func(string, int64) (runWebEventJournal, error) {
			journalMu.Lock()
			defer journalMu.Unlock()
			journal := journals[0]
			journals = journals[1:]
			return journal, nil
		},
	})
	t.Cleanup(func() { _ = s.(*runWebServer).close() })
	root := s.(*runWebServer).cfg.Root
	writeRunWebTestPayload(t, root)
	draft := RunDraft{Service: "svc-a", Host: "host-a", Payload: "run.sh"}

	first := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", draft)
	firstID := decodeRunWebDeployStarted(t, first).JobID
	waitRunWebJobState(t, s, firstID, runWebJobFailed)
	second := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", draft)
	secondID := decodeRunWebDeployStarted(t, second).JobID
	waitRunWebJobState(t, s, secondID, runWebJobSucceeded)

	firstJournal.mu.Lock()
	firstCloseCalls := firstJournal.closeCalls
	firstJournal.mu.Unlock()
	if firstCloseCalls != 1 {
		t.Fatalf("failed journal close calls = %d, want 1 before replacement", firstCloseCalls)
	}
}

func TestRunWebServerCloseAndContextCancellationCleanJournalExactlyOnce(t *testing.T) {
	t.Run("explicit close", func(t *testing.T) {
		journal := newRunWebMemoryJournal()
		server := newRunWebServer(runWebServerConfig{Token: "secret", Root: t.TempDir()}).(*runWebServer)
		job, err := newRunWebJob("1", runWebJobConfig{
			NewJournal: func(string, int64) (runWebEventJournal, error) {
				return journal, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		server.active = job
		if err := server.close(); err != nil {
			t.Fatalf("first close: %v", err)
		}
		if err := server.close(); err != nil {
			t.Fatalf("second close: %v", err)
		}
		journal.mu.Lock()
		defer journal.mu.Unlock()
		if journal.closeCalls != 1 {
			t.Fatalf("journal close calls = %d, want 1", journal.closeCalls)
		}
	})

	t.Run("context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		journal := newRunWebMemoryJournal()
		server := newRunWebServer(runWebServerConfig{Token: "secret", Root: t.TempDir(), Context: ctx}).(*runWebServer)
		job, err := newRunWebJob("1", runWebJobConfig{
			NewJournal: func(string, int64) (runWebEventJournal, error) {
				return journal, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		server.active = job
		cancel()
		deadline := time.Now().Add(time.Second)
		for {
			journal.mu.Lock()
			closeCalls := journal.closeCalls
			journal.mu.Unlock()
			if closeCalls == 1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("journal close calls = %d, want 1 after context cancellation", closeCalls)
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err := server.close(); err != nil {
			t.Fatalf("idempotent close: %v", err)
		}
	})
}

func TestRunWebServerCloseStopsLifecycleWatcherWithLiveParent(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	server := newRunWebServer(runWebServerConfig{
		Token:   "secret",
		Root:    t.TempDir(),
		Context: parent,
	}).(*runWebServer)

	if err := server.close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-server.lifecycleDone:
	case <-time.After(time.Second):
		t.Fatal("lifecycle watcher did not stop after explicit server close")
	}
	if err := parent.Err(); err != nil {
		t.Fatalf("parent context error = %v, want live parent", err)
	}
}

func TestRunWebServerCloseRejectsLateDeployBeforeCreatingJournal(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	defer func() { fetchRunDraftServiceInfoFn = oldInfo }()
	fetchRunDraftServiceInfoFn = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	root := t.TempDir()
	writeRunWebTestPayload(t, root)
	journalCreated := false
	server := newRunWebServer(runWebServerConfig{
		Token: "secret",
		Root:  root,
		NewJournal: func(string, int64) (runWebEventJournal, error) {
			journalCreated = true
			return newRunWebMemoryJournal(), nil
		},
	}).(*runWebServer)
	if err := server.close(); err != nil {
		t.Fatal(err)
	}

	rec := runWebAPIRequest(t, server, http.MethodPost, "/api/deploy", RunDraft{
		Service: "svc-a", Host: "host-a", Payload: "run.sh",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("late deploy status = %d, want 409 body=%s", rec.Code, rec.Body.String())
	}
	if journalCreated {
		t.Fatal("late deploy created a journal after server close")
	}
}

func TestRunWebAPISessionClosedWritesNoticeForFailedIncompleteJob(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
		return errors.New("deploy failed")
	}

	root := t.TempDir()
	writeRunWebTestPayload(t, root)
	var notices bytes.Buffer
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: root, Err: &notices})
	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", RunDraft{Service: "svc-a", Host: "host-a", Payload: "run.sh"})
	jobID := decodeRunWebDeployStarted(t, rec).JobID
	waitRunWebJobState(t, s, jobID, runWebJobFailed)

	for i := 0; i < 2; i++ {
		closed := runWebAPIRequest(t, s, http.MethodPost, "/api/session/closed", nil)
		if closed.Code != http.StatusNoContent {
			t.Fatalf("closed status = %d body=%s", closed.Code, closed.Body.String())
		}
	}
	if got := notices.String(); got != runWebBrowserClosedMessage {
		t.Fatalf("notice output = %q, want exactly one close notice after failed incomplete job", got)
	}
}

func TestRunWebAPISessionClosedWritesNoticeOnceForIncompleteActiveJob(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
		startedOnce.Do(func() { close(started) })
		<-release
		return nil
	}

	root := t.TempDir()
	writeRunWebTestPayload(t, root)
	var notices bytes.Buffer
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: root, Err: &notices})
	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", RunDraft{Service: "svc-a", Host: "host-a", Payload: "run.sh"})
	jobID := decodeRunWebDeployStarted(t, rec).JobID
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for deploy")
	}
	for i := 0; i < 2; i++ {
		closed := runWebAPIRequest(t, s, http.MethodPost, "/api/session/closed", nil)
		if closed.Code != http.StatusNoContent {
			t.Fatalf("closed status = %d body=%s", closed.Code, closed.Body.String())
		}
	}
	if got := notices.String(); got != runWebBrowserClosedMessage {
		t.Fatalf("notice output = %q, want exactly one close notice", got)
	}
	close(release)
	waitRunWebJobState(t, s, jobID, runWebJobSucceeded)
	closed := runWebAPIRequest(t, s, http.MethodPost, "/api/session/closed", nil)
	if closed.Code != http.StatusNoContent {
		t.Fatalf("closed after finish status = %d body=%s", closed.Code, closed.Body.String())
	}
	if got := notices.String(); got != runWebBrowserClosedMessage {
		t.Fatalf("notice after finish = %q, want unchanged", got)
	}
}

func TestRunWebAPIDeployRepeatsValidation(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	defer func() { fetchRunDraftServiceInfoFn = oldInfo }()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: true}, nil
	}
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: t.TempDir()})
	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", RunDraft{Service: "svc-a", Host: "host-a", Payload: "ghcr.io/example/app:latest"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("deploy status = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "already exists") {
		t.Fatalf("deploy body = %s, want already exists", rec.Body.String())
	}
}

func TestRunWebAPIDeployIsSingleUse(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}

	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	var mu sync.Mutex
	execCount := 0
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
		mu.Lock()
		execCount++
		mu.Unlock()
		startOnce.Do(func() { close(started) })
		<-release
		return nil
	}

	root := t.TempDir()
	payload := filepath.Join(root, "run.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: root})
	draft := RunDraft{Service: "svc-a", Host: "host-a", Payload: "run.sh"}

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstDone <- runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", draft)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first deploy to start")
	}

	second := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", draft)
	if second.Code != http.StatusConflict {
		t.Fatalf("second deploy status = %d, want 409 body=%s", second.Code, second.Body.String())
	}
	close(release)
	first := <-firstDone
	if first.Code != http.StatusOK {
		t.Fatalf("first deploy status = %d, want 200 body=%s", first.Code, first.Body.String())
	}
	firstID := decodeRunWebDeployStarted(t, first).JobID
	waitRunWebJobState(t, s, firstID, runWebJobSucceeded)

	third := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", draft)
	if third.Code != http.StatusConflict {
		t.Fatalf("third deploy status = %d, want 409 body=%s", third.Code, third.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if execCount != 1 {
		t.Fatalf("executeRunDraftWithOptionsFn calls = %d, want 1", execCount)
	}
}

func TestRunWebAPIDeployUsesConfiguredContext(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
			return context.DeadlineExceeded
		}
	}

	root := t.TempDir()
	payload := filepath.Join(root, "run.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: root, Context: ctx})

	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", RunDraft{Service: "svc-a", Host: "host-a", Payload: "run.sh"})
	jobID := decodeRunWebDeployStarted(t, rec).JobID
	status := waitRunWebJobState(t, s, jobID, runWebJobFailed)
	if !strings.Contains(status.Error, "context canceled") {
		t.Fatalf("status error = %q, want context canceled", status.Error)
	}
}

func TestRunWebAPIDeployIgnoresCanceledRequestContext(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	done := make(chan struct{})
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
		defer close(done)
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	}

	root := t.TempDir()
	payload := filepath.Join(root, "run.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: root, Context: context.Background()})
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(RunDraft{Service: "svc-a", Host: "host-a", Payload: "run.sh"}); err != nil {
		t.Fatalf("encode draft: %v", err)
	}
	requestCtx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/deploy", &buf).WithContext(requestCtx)
	req.Header.Set("X-Yeet-Run-Token", "secret")
	rec := httptest.NewRecorder()

	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("deploy status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for background deploy")
	}
}

func TestRunWebAPIDeployJobUnknownAndBadMethods(t *testing.T) {
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: t.TempDir()})

	tests := []struct {
		method string
		path   string
		want   int
	}{
		{method: http.MethodGet, path: "/api/deploy/missing/status", want: http.StatusNotFound},
		{method: http.MethodGet, path: "/api/deploy/missing/stream", want: http.StatusNotFound},
		{method: http.MethodPost, path: "/api/deploy/missing/stream", want: http.StatusMethodNotAllowed},
		{method: http.MethodPost, path: "/api/deploy/missing/status", want: http.StatusMethodNotAllowed},
		{method: http.MethodGet, path: "/api/deploy", want: http.StatusMethodNotAllowed},
		{method: http.MethodGet, path: "/api/session/closed", want: http.StatusMethodNotAllowed},
	}
	for _, tt := range tests {
		rec := runWebAPIRequest(t, s, tt.method, tt.path, nil)
		if rec.Code != tt.want {
			t.Fatalf("%s %s status = %d, want %d body=%s", tt.method, tt.path, rec.Code, tt.want, rec.Body.String())
		}
	}
}

func TestRunWebAPIDeployAckAuthorizationAndEligibility(t *testing.T) {
	tests := []struct {
		name    string
		method  string
		path    string
		prepare func(*runWebServer)
		want    int
	}{
		{
			name:   "unknown job",
			method: http.MethodPost,
			path:   "/api/deploy/missing/ack",
			want:   http.StatusNotFound,
		},
		{
			name:   "wrong method",
			method: http.MethodGet,
			path:   "/api/deploy/1/ack",
			prepare: func(s *runWebServer) {
				s.active = mustNewRunWebJob(t, runWebJobConfig{})
				s.active.id = "1"
			},
			want: http.StatusMethodNotAllowed,
		},
		{
			name:   "running job",
			method: http.MethodPost,
			path:   "/api/deploy/1/ack",
			prepare: func(s *runWebServer) {
				s.active = mustNewRunWebJob(t, runWebJobConfig{})
				s.active.id = "1"
			},
			want: http.StatusConflict,
		},
		{
			name:   "failed job",
			method: http.MethodPost,
			path:   "/api/deploy/1/ack",
			prepare: func(s *runWebServer) {
				s.active = mustNewRunWebJob(t, runWebJobConfig{})
				s.active.id = "1"
				s.active.finish(errors.New("deploy failed"))
			},
			want: http.StatusConflict,
		},
		{
			name:   "degraded success",
			method: http.MethodPost,
			path:   "/api/deploy/1/ack",
			prepare: func(s *runWebServer) {
				s.active = mustNewRunWebJob(t, runWebJobConfig{})
				s.active.id = "1"
				s.active.mu.Lock()
				s.active.degraded = true
				s.active.mu.Unlock()
				s.active.finish(nil)
			},
			want: http.StatusConflict,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newRunWebServer(runWebServerConfig{Token: "secret", Root: t.TempDir()}).(*runWebServer)
			if tc.prepare != nil {
				tc.prepare(s)
			}
			rec := runWebAPIRequest(t, s, tc.method, tc.path, nil)
			if rec.Code != tc.want {
				t.Fatalf("%s %s status = %d, want %d body=%s", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestRunWebAPIDeployAckCookieRequiresCSRFAndEligibleDuplicatesSucceed(t *testing.T) {
	s := newRunWebServer(runWebServerConfig{
		Token:     "secret",
		CSRFToken: "csrf-value",
		Root:      t.TempDir(),
	}).(*runWebServer)
	s.active = mustNewRunWebJob(t, runWebJobConfig{})
	s.active.finish(nil)

	request := func(csrf string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/deploy/job-a/ack", nil)
		req.AddCookie(&http.Cookie{Name: runWebTokenCookieName, Value: "secret"})
		if csrf != "" {
			req.Header.Set("X-Yeet-Run-CSRF", csrf)
		}
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		return rec
	}

	if rec := request(""); rec.Code != http.StatusForbidden {
		t.Fatalf("cookie-only ack status = %d, want 403 body=%s", rec.Code, rec.Body.String())
	}
	for i := 0; i < 2; i++ {
		if rec := request("csrf-value"); rec.Code != http.StatusNoContent {
			t.Fatalf("eligible ack %d status = %d, want 204 body=%s", i+1, rec.Code, rec.Body.String())
		}
	}
}

func TestRunWebAPICompletionReleaseFollowsFlushedNoContentResponse(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "deploy acknowledgement", path: "/api/deploy/job-a/ack"},
		{name: "session close", path: "/api/session/closed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			callback := make(chan runWebResponseOrderSnapshot, 1)
			var writer *runWebResponseOrderWriter
			server := newRunWebServer(runWebServerConfig{
				Token:                "secret",
				Root:                 t.TempDir(),
				CompletionAckTimeout: time.Second,
				OnComplete: func() {
					callback <- writer.snapshot()
				},
			}).(*runWebServer)
			t.Cleanup(func() { _ = server.close() })
			job := mustNewRunWebJob(t, runWebJobConfig{})
			job.finish(nil)
			server.active = job
			go server.awaitSuccessfulRender(job)

			writer = newRunWebResponseOrderWriter(job)
			req := httptest.NewRequest(http.MethodPost, tc.path, nil)
			req.Header.Set("X-Yeet-Run-Token", "secret")
			server.ServeHTTP(writer, req)

			response := writer.snapshot()
			if response.status != http.StatusNoContent {
				t.Fatalf("response status = %d, want 204", response.status)
			}
			if !response.flushed {
				t.Fatal("204 response was not flushed before handler returned")
			}
			if response.releasedBeforeCommit {
				t.Fatal("completion acknowledgement was released before the 204 response was committed")
			}
			select {
			case completed := <-callback:
				if completed.status != http.StatusNoContent || !completed.flushed {
					t.Fatalf("OnComplete observed response = %#v, want committed and flushed 204", completed)
				}
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for OnComplete")
			}
		})
	}
}

func stubRunWebSuccessfulDeploy(t *testing.T) func() {
	t.Helper()
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	fetchRunDraftServiceInfoFn = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	executeRunDraftWithOptionsFn = func(context.Context, RunDraft, *projectConfigLocation, runDraftExecuteOptions) error {
		return nil
	}
	return func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}
}

type runWebResizeSignalJournal struct {
	runWebEventJournal
	appended chan struct{}
	once     sync.Once
}

type runWebResponseOrderSnapshot struct {
	status               int
	flushed              bool
	releasedBeforeCommit bool
}

type runWebResponseOrderWriter struct {
	job    *runWebJob
	header http.Header

	mu                   sync.Mutex
	status               int
	flushed              bool
	releasedBeforeCommit bool
}

func newRunWebResponseOrderWriter(job *runWebJob) *runWebResponseOrderWriter {
	return &runWebResponseOrderWriter{job: job, header: make(http.Header)}
}

func (w *runWebResponseOrderWriter) Header() http.Header {
	return w.header
}

func (w *runWebResponseOrderWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return len(p), nil
}

func (w *runWebResponseOrderWriter) WriteHeader(status int) {
	released := false
	select {
	case <-w.job.acknowledged():
		released = true
	default:
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.status = status
	w.releasedBeforeCommit = released
}

func (w *runWebResponseOrderWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.flushed = true
}

func (w *runWebResponseOrderWriter) snapshot() runWebResponseOrderSnapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	return runWebResponseOrderSnapshot{
		status:               w.status,
		flushed:              w.flushed,
		releasedBeforeCommit: w.releasedBeforeCommit,
	}
}

func (j *runWebResizeSignalJournal) append(ev runWebStreamEvent, control bool) (int64, error) {
	id, err := j.runWebEventJournal.append(ev, control)
	if err == nil && ev.Type == runWebStreamResize {
		j.once.Do(func() { close(j.appended) })
	}
	return id, err
}

func runWebAPIRequest(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != nil {
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
		r = &buf
	}
	req := httptest.NewRequest(method, path, r)
	req.Header.Set("X-Yeet-Run-Token", "secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

type runWebDeployStartedResponse struct {
	OK    bool   `json:"ok"`
	JobID string `json:"jobId"`
}

func decodeRunWebDeployStarted(t *testing.T, rec *httptest.ResponseRecorder) runWebDeployStartedResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	var response runWebDeployStartedResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode deploy response %q: %v", rec.Body.String(), err)
	}
	if !response.OK || response.JobID == "" {
		t.Fatalf("deploy response = %#v, want ok job ID", response)
	}
	return response
}

func waitRunWebJobState(t *testing.T, handler http.Handler, jobID string, want runWebJobState) runWebJobStatus {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	var last runWebJobStatus
	for time.Now().Before(deadline) {
		rec := runWebAPIRequest(t, handler, http.MethodGet, "/api/deploy/"+jobID+"/status", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &last); err != nil {
			t.Fatalf("decode status %q: %v", rec.Body.String(), err)
		}
		if last.State == want {
			return last
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for job %s state %s, last=%#v", jobID, want, last)
	return runWebJobStatus{}
}

type runWebSSETestEvent struct {
	Name string
	ID   string
	Data string
}

func parseRunWebSSE(t *testing.T, body string) []runWebSSETestEvent {
	t.Helper()
	blocks := strings.Split(strings.TrimSpace(body), "\n\n")
	events := make([]runWebSSETestEvent, 0, len(blocks))
	for _, block := range blocks {
		if strings.TrimSpace(block) == "" {
			continue
		}
		var ev runWebSSETestEvent
		for _, line := range strings.Split(block, "\n") {
			key, value, ok := strings.Cut(line, ": ")
			if !ok {
				continue
			}
			switch key {
			case "event":
				ev.Name = value
			case "id":
				ev.ID = value
				if _, err := strconv.ParseInt(value, 10, 64); err != nil {
					t.Fatalf("event id %q is not int64: %v", value, err)
				}
			case "data":
				ev.Data = value
			}
		}
		if ev.Name != "" || ev.ID != "" || ev.Data != "" {
			events = append(events, ev)
		}
	}
	return events
}

func readRunWebSSEEvent(t *testing.T, reader *bufio.Reader) runWebSSETestEvent {
	t.Helper()
	for {
		var event runWebSSETestEvent
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatalf("read SSE event: %v", err)
			}
			line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			if line == "" {
				break
			}
			key, value, ok := strings.Cut(line, ": ")
			if !ok {
				continue
			}
			switch key {
			case "event":
				event.Name = value
			case "id":
				event.ID = value
			case "data":
				event.Data = value
			}
		}
		if event.Name != "" || event.ID != "" || event.Data != "" {
			return event
		}
	}
}

type runWebOutputGateWriter struct {
	http.ResponseWriter
	ctx     context.Context
	cleanup <-chan struct{}
	release <-chan struct{}
	blocked func()
}

func (w *runWebOutputGateWriter) Write(p []byte) (int, error) {
	if bytes.HasPrefix(p, []byte("event: output\n")) {
		if w.blocked != nil {
			w.blocked()
		}
		select {
		case <-w.release:
		case <-w.ctx.Done():
			return 0, w.ctx.Err()
		case <-w.cleanup:
			return 0, context.Canceled
		}
	}
	return w.ResponseWriter.Write(p)
}

func (w *runWebOutputGateWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func decodeRunWebOutputText(t *testing.T, events []runWebSSETestEvent) string {
	t.Helper()
	var out strings.Builder
	for _, ev := range events {
		if ev.Name != string(runWebStreamOutput) {
			continue
		}
		var output struct {
			Encoding string `json:"encoding"`
			Chunk    string `json:"chunk"`
		}
		if err := json.Unmarshal([]byte(ev.Data), &output); err != nil {
			t.Fatalf("decode output event %q: %v", ev.Data, err)
		}
		if output.Encoding != "base64" {
			t.Fatalf("output encoding = %q, want base64", output.Encoding)
		}
		chunk, err := base64.StdEncoding.DecodeString(output.Chunk)
		if err != nil {
			t.Fatalf("decode output chunk %q: %v", output.Chunk, err)
		}
		out.Write(chunk)
	}
	return out.String()
}

func writeRunWebTestPayload(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write payload: %v", err)
	}
}
