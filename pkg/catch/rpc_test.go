// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"testing"

	"github.com/shayne/yeet/pkg/catchrpc"
	cdb "github.com/shayne/yeet/pkg/db"
	"github.com/shayne/yeet/pkg/registry"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	cfg := &Config{
		DB:                   cdb.NewStore(filepath.Join(root, "db.json"), filepath.Join(root, "services")),
		DefaultUser:          "root",
		RootDir:              root,
		ServicesRoot:         filepath.Join(root, "services"),
		MountsRoot:           filepath.Join(root, "mounts"),
		RegistryRoot:         filepath.Join(root, "registry"),
		InternalRegistryAddr: "127.0.0.1:0",
		AuthorizeFunc: func(ctx context.Context, remoteAddr string) error {
			return nil
		},
	}
	storage, err := registry.NewFilesystemStorage(cfg.RegistryRoot)
	if err != nil {
		t.Fatalf("NewFilesystemStorage: %v", err)
	}
	cfg.RegistryStorage = storage
	return NewUnstartedServer(cfg)
}

func TestRPCInfo(t *testing.T) {
	server := newTestServer(t)
	ts := httptest.NewServer(server.RPCMux())
	defer ts.Close()

	req := catchrpc.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "catch.Info",
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	resp, err := http.Post(ts.URL+"/rpc", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	var rpcResp catchrpc.Response
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if rpcResp.Error != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcResp.Error)
	}
	var info ServerInfo
	b, _ := json.Marshal(rpcResp.Result)
	if err := json.Unmarshal(b, &info); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if info.GOOS == "" || info.GOARCH == "" {
		t.Fatalf("unexpected info: %#v", info)
	}
}

func TestRPCServiceInfo(t *testing.T) {
	server := newTestServer(t)
	_, _, err := server.cfg.DB.MutateService("svc-info", func(_ *cdb.Data, s *cdb.Service) error {
		s.ServiceType = cdb.ServiceTypeSystemd
		s.Generation = 2
		s.LatestGeneration = 2
		s.SvcNetwork = &cdb.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.5")}
		s.Artifacts = cdb.ArtifactStore{
			cdb.ArtifactSystemdUnit: {Refs: map[cdb.ArtifactRef]string{"latest": "/tmp/svc-info.service"}},
		}
		return nil
	})
	if err != nil {
		t.Fatalf("mutate service: %v", err)
	}

	ts := httptest.NewServer(server.RPCMux())
	defer ts.Close()

	params, err := json.Marshal(catchrpc.ServiceInfoRequest{Service: "svc-info"})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	req := catchrpc.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "catch.ServiceInfo",
		Params:  params,
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	resp, err := http.Post(ts.URL+"/rpc", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	var rpcResp catchrpc.Response
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if rpcResp.Error != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcResp.Error)
	}
	var info catchrpc.ServiceInfoResponse
	b, _ := json.Marshal(rpcResp.Result)
	if err := json.Unmarshal(b, &info); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if !info.Found || info.Info.Name != "svc-info" {
		t.Fatalf("unexpected info: %#v", info)
	}
	if info.Info.Network.SvcIP != "192.168.100.5" {
		t.Fatalf("unexpected svc ip: %#v", info.Info.Network.SvcIP)
	}
}

func TestRPCMethodNotFound(t *testing.T) {
	server := newTestServer(t)
	ts := httptest.NewServer(server.RPCMux())
	defer ts.Close()

	req := catchrpc.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "catch.DoesNotExist",
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	resp, err := http.Post(ts.URL+"/rpc", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	var rpcResp catchrpc.Response
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if rpcResp.Error == nil {
		t.Fatalf("expected rpc error")
	}
	if rpcResp.Error.Code != catchrpc.ErrMethodNotFound {
		t.Fatalf("unexpected error code: %d", rpcResp.Error.Code)
	}
}

func TestRPCInvalidJSON(t *testing.T) {
	server := newTestServer(t)
	ts := httptest.NewServer(server.RPCMux())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/rpc", "application/json", bytes.NewReader([]byte("{")))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	var rpcResp catchrpc.Response
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if rpcResp.Error == nil || rpcResp.Error.Code != catchrpc.ErrParseError {
		t.Fatalf("unexpected error: %+v", rpcResp.Error)
	}
}

func TestRPCInvalidRequest(t *testing.T) {
	server := newTestServer(t)
	ts := httptest.NewServer(server.RPCMux())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/rpc", "application/json", bytes.NewReader([]byte(`{"jsonrpc":"2.0"}`)))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	var rpcResp catchrpc.Response
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if rpcResp.Error == nil || rpcResp.Error.Code != catchrpc.ErrInvalidRequest {
		t.Fatalf("unexpected error: %+v", rpcResp.Error)
	}
}

func TestRPCNotificationNoResponse(t *testing.T) {
	server := newTestServer(t)
	ts := httptest.NewServer(server.RPCMux())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/rpc", "application/json", bytes.NewReader([]byte(`{"jsonrpc":"2.0","method":"catch.Info"}`)))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if len(bytes.TrimSpace(body)) != 0 {
		t.Fatalf("expected empty response, got %q", string(body))
	}
}

func TestRPCMethodNotAllowed(t *testing.T) {
	server := newTestServer(t)
	ts := httptest.NewServer(server.RPCMux())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/rpc")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", resp.StatusCode)
	}
}

func TestRPCAuthorizeDenied(t *testing.T) {
	server := newTestServer(t)
	server.cfg.AuthorizeFunc = func(ctx context.Context, remoteAddr string) error {
		return errors.New("nope")
	}
	ts := httptest.NewServer(server.RPCMux())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/rpc", "application/json", bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":1,"method":"catch.Info"}`)))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}
