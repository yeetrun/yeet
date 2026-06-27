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
	"time"

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

func TestNormalizeExecRequestTargets(t *testing.T) {
	tests := []struct {
		name    string
		req     catchrpc.ExecRequest
		wantErr string
	}{
		{
			name:    "service command default requires service",
			req:     catchrpc.ExecRequest{Args: []string{"status"}},
			wantErr: "missing service",
		},
		{
			name: "service command default with service",
			req:  catchrpc.ExecRequest{Service: "api", Args: []string{"status"}},
		},
		{
			name: "host shell allows empty service",
			req:  catchrpc.ExecRequest{Target: catchrpc.ExecTargetHostShell, Args: []string{"whoami"}},
		},
		{
			name: "host shell clears accidental service",
			req:  catchrpc.ExecRequest{Target: catchrpc.ExecTargetHostShell, Service: "api", Args: []string{"whoami"}},
		},
		{
			name:    "service shell requires service",
			req:     catchrpc.ExecRequest{Target: catchrpc.ExecTargetServiceShell, Args: []string{"pwd"}},
			wantErr: "missing service",
		},
		{
			name: "service shell with service",
			req:  catchrpc.ExecRequest{Target: catchrpc.ExecTargetServiceShell, Service: "api", Args: []string{"pwd"}},
		},
		{
			name:    "unknown target",
			req:     catchrpc.ExecRequest{Target: catchrpc.ExecTarget("bogus"), Service: "api"},
			wantErr: `unknown exec target "bogus"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeExecRequest(tt.req)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("normalizeExecRequest error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeExecRequest: %v", err)
			}
			if tt.req.Target == catchrpc.ExecTargetHostShell && got.Service != "" {
				t.Fatalf("host shell service = %q, want empty", got.Service)
			}
			if got.Target != tt.req.Target {
				t.Fatalf("target = %q, want %q", got.Target, tt.req.Target)
			}
		})
	}
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

func TestRPCServiceInfoPassesRequestContextToVMAgent(t *testing.T) {
	server := newTestServer(t)
	seedLANVMService(t, server)
	if _, _, err := server.cfg.DB.MutateService("devbox", func(_ *cdb.Data, svc *cdb.Service) error {
		svc.VM.Sockets.VsockSocketPath = "/run/devbox/vsock.sock"
		svc.VM.Sockets.VsockGuestCID = vmAgentGuestCID
		return nil
	}); err != nil {
		t.Fatalf("MutateService: %v", err)
	}

	oldQuery := queryVMNetworkStateFn
	sawCanceled := false
	queryVMNetworkStateFn = func(ctx context.Context, _ string) (vmAgentNetworkState, error) {
		sawCanceled = errors.Is(ctx.Err(), context.Canceled)
		return vmAgentNetworkState{}, ctx.Err()
	}
	t.Cleanup(func() { queryVMNetworkStateFn = oldQuery })

	params, err := json.Marshal(catchrpc.ServiceInfoRequest{Service: "devbox"})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	req := catchrpc.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "catch.ServiceInfo",
		Params:  params,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	resp := server.dispatchRPCWithContext(ctx, req)
	if resp.Error != nil {
		t.Fatalf("dispatchRPCWithContext error = %#v", resp.Error)
	}
	if !sawCanceled {
		t.Fatal("VM agent query did not receive canceled RPC context")
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

func TestRPCVMDefaults(t *testing.T) {
	server := newTestServer(t)
	oldProfile := vmDefaultsHostProfileFunc
	defer func() { vmDefaultsHostProfileFunc = oldProfile }()
	vmDefaultsHostProfileFunc = func(_ *Server, req catchrpc.VMDefaultsRequest, _ int64) (vmHostProfile, []string, error) {
		if req.Service != "devbox" || req.ServiceRoot != "flash/yeet/vms/devbox" || !req.ZFS {
			t.Fatalf("request = %#v, want devbox ZFS request", req)
		}
		return vmHostProfile{
			Arch:         "x86_64",
			HasKVM:       true,
			LogicalCPUs:  12,
			MemoryBytes:  31 << 30,
			StorageBytes: 897 << 30,
			StorageZFS:   true,
		}, nil, nil
	}
	ts := httptest.NewServer(server.RPCMux())
	defer ts.Close()

	params, err := json.Marshal(catchrpc.VMDefaultsRequest{
		Service:     "devbox",
		ServiceRoot: "flash/yeet/vms/devbox",
		ZFS:         true,
	})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	req := catchrpc.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "catch.VMDefaults",
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
	var defaults catchrpc.VMDefaultsResponse
	b, _ := json.Marshal(rpcResp.Result)
	if err := json.Unmarshal(b, &defaults); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if defaults.CPUs != 4 || defaults.Memory != "4g" || defaults.Disk != "128g" || defaults.DiskBackend != "zvol" {
		t.Fatalf("defaults = %#v, want 4/4g/128g zvol", defaults)
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

	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 401", resp.StatusCode, body)
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

func TestRPCMethodAuthorization(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		have       permissionSet
		wantStatus int
		wantBody   string
	}{
		{name: "info requires read", method: "catch.Info", have: newPermissionSet(permissionRead), wantStatus: http.StatusOK},
		{name: "info denied without read", method: "catch.Info", have: newPermissionSet(permissionManage), wantStatus: http.StatusUnauthorized, wantBody: `missing yeet permission "read"`},
		{name: "tailscale setup requires full admin permissions", method: "catch.TailscaleSetup", have: newPermissionSet(permissionRead, permissionManage, permissionSSH), wantStatus: http.StatusOK},
		{name: "tailscale setup denied without manage", method: "catch.TailscaleSetup", have: newPermissionSet(permissionRead, permissionSSH), wantStatus: http.StatusUnauthorized, wantBody: `missing yeet permission "manage"`},
		{name: "tailscale setup denied without ssh", method: "catch.TailscaleSetup", have: newPermissionSet(permissionRead, permissionManage), wantStatus: http.StatusUnauthorized, wantBody: `missing yeet permission "ssh"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newAuthzTestServer(t, tt.have)
			ts := httptest.NewServer(server.RPCMux())
			defer ts.Close()

			req := catchrpc.Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: tt.method}
			if tt.method == "catch.TailscaleSetup" {
				params, err := json.Marshal(catchrpc.TailscaleSetupRequest{ClientSecret: "tskey-client-test"})
				if err != nil {
					t.Fatalf("marshal params: %v", err)
				}
				req.Params = params
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
			raw, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d body=%s, want %d", resp.StatusCode, raw, tt.wantStatus)
			}
			if tt.wantBody != "" && !strings.Contains(string(raw), tt.wantBody) {
				t.Fatalf("body = %q, want %q", raw, tt.wantBody)
			}
		})
	}
}

func TestRPCUnknownMethodFailsClosedBeforeDispatch(t *testing.T) {
	server := newAuthzTestServer(t, newPermissionSet(permissionRead, permissionManage, permissionSSH))
	ts := httptest.NewServer(server.RPCMux())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/rpc", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"catch.Future"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 401", resp.StatusCode, body)
	}
}

func TestRPCExecAuthorizesAfterReadingExecRequest(t *testing.T) {
	tests := []struct {
		name string
		req  catchrpc.ExecRequest
		have permissionSet
		want string
	}{
		{
			name: "host shell requires ssh",
			req:  catchrpc.ExecRequest{Target: catchrpc.ExecTargetHostShell, Args: []string{"sh", "-c", "echo host"}},
			have: newPermissionSet(permissionRead, permissionManage),
			want: `missing yeet permission "ssh"`,
		},
		{
			name: "service command requires mapped manage",
			req:  catchrpc.ExecRequest{Service: "svc", Args: []string{"remove", "--yes"}},
			have: newPermissionSet(permissionRead),
			want: `missing yeet permission "manage"`,
		},
		{
			name: "service command requires mapped read",
			req:  catchrpc.ExecRequest{Service: "svc", Args: []string{"logs"}},
			have: newPermissionSet(permissionManage),
			want: `missing yeet permission "read"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newAuthzTestServer(t, tt.have)
			ts := httptest.NewServer(server.RPCMux())
			defer ts.Close()

			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/rpc/exec", nil)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer conn.Close()
			if err := conn.WriteJSON(tt.req); err != nil {
				t.Fatalf("write request: %v", err)
			}
			var output bytes.Buffer
			var exitErr string
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			for {
				mt, data, err := conn.ReadMessage()
				if err != nil {
					t.Fatalf("read message: %v output=%q", err, output.String())
				}
				if mt == websocket.BinaryMessage {
					output.Write(data)
					continue
				}
				var msg catchrpc.ExecMessage
				if err := json.Unmarshal(data, &msg); err != nil {
					t.Fatalf("decode exec message: %v", err)
				}
				if msg.Type == catchrpc.ExecMsgExit {
					if msg.Code == 0 {
						t.Fatal("exec exit code = 0, want denied")
					}
					exitErr = msg.Error
					break
				}
			}
			combined := output.String() + exitErr
			if !strings.Contains(combined, tt.want) {
				t.Fatalf("combined output = %q, want %q", combined, tt.want)
			}
		})
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

func TestRPCZFSServiceRootCandidates(t *testing.T) {
	server := newTestServer(t)
	server.zfsRunner = fakeZFSListRunner(strings.Join([]string{
		"flash\tfilesystem\t/flash\t1000\t400\t100\t-\ton\toff\tyes",
		"flash/yeet\tfilesystem\t/flash/yeet\t1000\t300\t1\t-\ton\toff\tyes",
		"flash/yeet/vms\tfilesystem\t/flash/yeet/vms\t1000\t30\t1\t-\ton\toff\tyes",
		"flash/yeet/vms/devbox\tfilesystem\t/flash/yeet/vms/devbox\t1000\t10\t1\t-\ton\toff\tyes",
		"flash/yeet/vms/devbox/root\tvolume\t-\t1000\t10\t10\tflash/yeet/vm-images/ubuntu/root@snap\t-\toff\t-",
	}, "\n")+"\n", "", nil)

	params, err := json.Marshal(catchrpc.ZFSServiceRootCandidatesRequest{
		Workload: "vm",
		Service:  "devbox",
	})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	resp := server.dispatchRPC(catchrpc.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "catch.ZFSServiceRootCandidates",
		Params:  params,
	})
	if resp.Error != nil {
		t.Fatalf("unexpected rpc error: %+v", resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var roots catchrpc.ZFSServiceRootCandidatesResponse
	if err := json.Unmarshal(raw, &roots); err != nil {
		t.Fatalf("unmarshal roots: %v", err)
	}
	if roots.State != catchrpc.ZFSRootDiscoveryAvailable {
		t.Fatalf("state = %q, want available", roots.State)
	}
	if len(roots.Candidates) == 0 || roots.Candidates[0].SuggestedDataset != "flash/yeet/vms/devbox" {
		t.Fatalf("roots = %#v", roots)
	}
}

func TestRPCZFSServiceRootCandidatesCancelsRunnerWithRequestContext(t *testing.T) {
	server := newTestServer(t)
	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	server.zfsRunner = func(ctx context.Context, args ...string) (string, string, error) {
		close(started)
		select {
		case <-ctx.Done():
			close(canceled)
			return "", "", ctx.Err()
		case <-release:
			return "", "", errZFSCommandFailed
		}
	}
	ts := httptest.NewServer(server.RPCMux())
	defer ts.Close()

	params, err := json.Marshal(catchrpc.ZFSServiceRootCandidatesRequest{Workload: "vm"})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	body, err := json.Marshal(catchrpc.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "catch.ZFSServiceRootCandidates",
		Params:  params,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/rpc", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for zfs runner to start")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(500 * time.Millisecond):
		close(release)
		<-done
		t.Fatal("zfs runner did not observe request cancellation")
	}
	<-done
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
