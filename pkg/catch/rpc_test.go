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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/yeetrun/yeet/pkg/catchrpc"
	cdb "github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/registry"
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
	if info.RootDir == "" || info.ServicesDir == "" {
		t.Fatalf("expected root and services dirs in info: %#v", info)
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

func TestRPCTailscaleSetup(t *testing.T) {
	server := newTestServer(t)
	ts := httptest.NewServer(server.RPCMux())
	defer ts.Close()

	secret := "tskey-client-abc-123"
	params, err := json.Marshal(catchrpc.TailscaleSetupRequest{ClientSecret: secret})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	req := catchrpc.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "catch.TailscaleSetup",
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
	var setupResp catchrpc.TailscaleSetupResponse
	b, _ := json.Marshal(rpcResp.Result)
	if err := json.Unmarshal(b, &setupResp); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if !setupResp.Verified {
		t.Fatalf("expected verified response: %#v", setupResp)
	}
	path := filepath.Join(server.serviceDataDir(CatchService), "tailscale.key")
	if setupResp.Path != path {
		t.Fatalf("unexpected path: %q", setupResp.Path)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read tailscale.key: %v", err)
	}
	if strings.TrimSpace(string(contents)) != secret {
		t.Fatalf("unexpected tailscale.key contents: %q", contents)
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

func TestRPCUnknownFieldReturnsParseError(t *testing.T) {
	server := newTestServer(t)
	ts := httptest.NewServer(server.RPCMux())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/rpc", "application/json", bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":1,"method":"catch.Info","extra":true}`)))
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

func TestRPCServiceInfoInvalidParams(t *testing.T) {
	server := newTestServer(t)
	ts := httptest.NewServer(server.RPCMux())
	defer ts.Close()

	req := catchrpc.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "catch.ServiceInfo",
		Params:  json.RawMessage(`{"service":123}`),
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
	if rpcResp.Error == nil || rpcResp.Error.Code != catchrpc.ErrInvalidParams {
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

func TestRPCServicesListDispatch(t *testing.T) {
	server := newTestServer(t)
	if err := server.cfg.DB.Set(&cdb.Data{
		Services: map[string]*cdb.Service{
			"api":    {Name: "api", ServiceType: cdb.ServiceTypeSystemd},
			"worker": {Name: "worker", ServiceType: cdb.ServiceTypeDockerCompose},
		},
	}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}

	resp := server.dispatchRPC(catchrpc.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "catch.ServicesList",
	})
	if resp.Error != nil {
		t.Fatalf("unexpected rpc error: %+v", resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var list []serviceInfo
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("unmarshal services list: %v", err)
	}
	got := map[string]string{}
	for _, service := range list {
		got[service.Name] = service.Type
	}
	if got["api"] != string(cdb.ServiceTypeSystemd) || got["worker"] != string(cdb.ServiceTypeDockerCompose) {
		t.Fatalf("services list = %#v", list)
	}
}

func TestRPCArtifactHashesMissingService(t *testing.T) {
	server := newTestServer(t)
	params, err := json.Marshal(catchrpc.ArtifactHashesRequest{Service: "missing"})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	resp := server.handleRPCArtifactHashes(catchrpc.Request{
		ID:     json.RawMessage("1"),
		Params: params,
	})
	if resp.Error != nil {
		t.Fatalf("unexpected rpc error: %+v", resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var hashes catchrpc.ArtifactHashesResponse
	if err := json.Unmarshal(raw, &hashes); err != nil {
		t.Fatalf("unmarshal hashes: %v", err)
	}
	if hashes.Found || hashes.Message != "service not found" {
		t.Fatalf("hashes response = %#v", hashes)
	}
}

func TestDecodeRPCParamsReportsMissingParams(t *testing.T) {
	var params catchrpc.ServiceInfoRequest
	err := decodeRPCParams(nil, &params)
	if err == nil || err.Code != catchrpc.ErrInvalidParams || err.Message != "missing params" {
		t.Fatalf("decodeRPCParams error = %#v", err)
	}
}

func TestCloseExecInputHandlesNormalAndErrorClose(t *testing.T) {
	pr, pw := io.Pipe()
	closeExecInput(pw, &websocket.CloseError{Code: websocket.CloseNormalClosure})
	if _, err := pr.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("normal close read error = %v, want EOF", err)
	}

	pr, pw = io.Pipe()
	closeExecInput(pw, errors.New("network failed"))
	if _, err := pr.Read(make([]byte, 1)); err == nil || !strings.Contains(err.Error(), "network failed") {
		t.Fatalf("error close read error = %v", err)
	}
}

func TestHandleExecInputMessageWritesBinaryAndHandlesControl(t *testing.T) {
	pr, pw := io.Pipe()
	readDone := make(chan string, 1)
	go func() {
		buf := make([]byte, len("stdin"))
		n, err := io.ReadFull(pr, buf)
		if err != nil {
			readDone <- err.Error()
			return
		}
		readDone <- string(buf[:n])
	}()
	if !handleExecInputMessage(websocket.BinaryMessage, []byte("stdin"), pw, &ttyExecer{}) {
		t.Fatal("binary input returned false")
	}
	if got := <-readDone; got != "stdin" {
		t.Fatalf("pipe read = %q, want stdin", got)
	}
	_ = pw.Close()

	pr, pw = io.Pipe()
	msg, err := json.Marshal(catchrpc.ExecMessage{Type: catchrpc.ExecMsgStdinClose})
	if err != nil {
		t.Fatalf("marshal control: %v", err)
	}
	if !handleExecInputMessage(websocket.TextMessage, msg, pw, &ttyExecer{}) {
		t.Fatal("stdin close control returned false")
	}
	if _, err := pr.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("stdin close read error = %v, want EOF", err)
	}

	resize, err := json.Marshal(catchrpc.ExecMessage{Type: catchrpc.ExecMsgResize, Cols: 120, Rows: 40})
	if err != nil {
		t.Fatalf("marshal resize: %v", err)
	}
	if !handleExecInputMessage(websocket.TextMessage, resize, pw, &ttyExecer{}) {
		t.Fatal("resize control returned false")
	}
	if !handleExecInputMessage(websocket.PingMessage, nil, pw, &ttyExecer{}) {
		t.Fatal("non-input websocket message should be ignored")
	}
}
