// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catchrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func splitHostPort(t *testing.T, rawURL string) (string, int) {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	host, portStr, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatalf("split host:port: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	return host, port
}

func FuzzJSONRPCMessages(f *testing.F) {
	for _, seed := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"catch.Info"}`,
		`{"jsonrpc":"2.0","id":"abc","method":"catch.ServiceInfo","params":{"service":"api"}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"found":true}}`,
		`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found","data":"bad"}}`,
		`{`,
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 4096 {
			t.Skip()
		}

		var req Request
		if err := json.Unmarshal([]byte(raw), &req); err == nil {
			assertRawJSONValid(t, "request id", req.ID)
			assertRawJSONValid(t, "request params", req.Params)
			if _, err := json.Marshal(req); err != nil {
				t.Fatalf("marshal request: %v", err)
			}
		}

		var resp Response
		if err := json.Unmarshal([]byte(raw), &resp); err == nil {
			assertRawJSONValid(t, "response id", resp.ID)
			if _, err := json.Marshal(resp); err != nil {
				t.Fatalf("marshal response: %v", err)
			}
		}
	})
}

func assertRawJSONValid(t *testing.T, name string, raw json.RawMessage) {
	t.Helper()
	if len(raw) == 0 {
		return
	}
	if !json.Valid(raw) {
		t.Fatalf("%s raw JSON is invalid: %q", name, string(raw))
	}
}

func TestBuildRPCRequestPayloadEncodesParams(t *testing.T) {
	payload, err := buildRPCRequestPayload("test.echo", 42, map[string]string{"msg": "hi"})
	if err != nil {
		t.Fatalf("buildRPCRequestPayload failed: %v", err)
	}

	var req Request
	if err := json.Unmarshal(payload, &req); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if req.JSONRPC != "2.0" || req.Method != "test.echo" || string(req.ID) != "42" {
		t.Fatalf("unexpected request metadata: %#v", req)
	}

	var params map[string]string
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatalf("decode params: %v", err)
	}
	if params["msg"] != "hi" {
		t.Fatalf("unexpected params: %#v", params)
	}
}

func TestBuildRPCRequestPayloadOmitsNilParams(t *testing.T) {
	payload, err := buildRPCRequestPayload("test.ping", 7, nil)
	if err != nil {
		t.Fatalf("buildRPCRequestPayload failed: %v", err)
	}

	var req Request
	if err := json.Unmarshal(payload, &req); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(req.Params) != 0 {
		t.Fatalf("expected params to be omitted, got %q", string(req.Params))
	}
}

func TestBuildRPCRequestPayloadRejectsUnmarshalableParams(t *testing.T) {
	if _, err := buildRPCRequestPayload("test.bad", 1, map[string]any{"bad": make(chan int)}); err == nil {
		t.Fatalf("buildRPCRequestPayload succeeded for unmarshalable params")
	}
}

func TestDecodeRPCResponseDecodesResult(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(bytes.NewBufferString(`{
			"jsonrpc":"2.0",
			"id":1,
			"result":{"msg":"hi"}
		}`)),
	}

	var out map[string]string
	if err := decodeRPCResponse(resp, &out); err != nil {
		t.Fatalf("decodeRPCResponse failed: %v", err)
	}
	if out["msg"] != "hi" {
		t.Fatalf("unexpected result: %#v", out)
	}
}

func TestDecodeRPCResponseReturnsTrimmedHTTPStatusError(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Body:       io.NopCloser(bytes.NewBufferString("  upstream down\n")),
	}

	err := decodeRPCResponse(resp, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if got, want := err.Error(), "rpc status 502: upstream down"; got != want {
		t.Fatalf("unexpected error: %q", got)
	}
}

func TestDecodeRPCResponseAndResultErrorPaths(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewBufferString(`{`)),
	}
	if err := decodeRPCResponse(resp, nil); err == nil {
		t.Fatalf("decodeRPCResponse succeeded for malformed JSON")
	}

	err := decodeRPCResult(Response{Result: map[string]any{"bad": make(chan int)}}, &map[string]string{})
	if err == nil {
		t.Fatalf("decodeRPCResult succeeded for unmarshalable result")
	}
	if err := decodeRPCResult(Response{Result: map[string]string{"ok": "yes"}}, nil); err != nil {
		t.Fatalf("decodeRPCResult nil out returned error: %v", err)
	}
}

func TestClientCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		var params map[string]string
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatalf("decode params: %v", err)
		}
		resp := Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  params,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	host, port := splitHostPort(t, srv.URL)
	client := NewClient(host, port)

	var out map[string]string
	if err := client.Call(context.Background(), "test.echo", map[string]string{"msg": "hi"}, &out); err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if out["msg"] != "hi" {
		t.Fatalf("unexpected response: %#v", out)
	}
}

func TestClientCallError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		resp := Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &Error{
				Code:    ErrMethodNotFound,
				Message: "nope",
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	host, port := splitHostPort(t, srv.URL)
	client := NewClient(host, port)

	var out map[string]string
	err := client.Call(context.Background(), "test.nope", nil, &out)
	if err == nil {
		t.Fatal("expected error")
	}
	if got, want := err.Error(), "rpc error -32601: nope"; got != want {
		t.Fatalf("Client.Call error = %q, want %q", got, want)
	}
}

func TestClientCallErrorIncludesData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		resp := Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &Error{
				Code:    ErrInternal,
				Message: "failed to apply host storage",
				Data:    "reinstall catch unit: service root parent missing",
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	host, port := splitHostPort(t, srv.URL)
	client := NewClient(host, port)

	err := client.Call(context.Background(), "test.nope", nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	want := "rpc error -32603: failed to apply host storage: reinstall catch unit: service root parent missing"
	if got := err.Error(); got != want {
		t.Fatalf("Client.Call error = %q, want %q", got, want)
	}
}

func TestClientCallBuildRequestAndTransportErrors(t *testing.T) {
	client := NewClient("127.0.0.1", 1)
	if err := client.Call(context.Background(), "test.bad", map[string]any{"bad": make(chan int)}, nil); err == nil {
		t.Fatalf("Client.Call succeeded for bad params")
	}

	client = NewClient("127.0.0.1", 1)
	client.baseURL = "http://[::1"
	if err := client.Call(context.Background(), "test.bad-url", nil, nil); err == nil {
		t.Fatalf("Client.Call succeeded for bad base URL")
	}

	wantErr := errors.New("transport failed")
	client = NewClient("127.0.0.1", 1)
	client.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, wantErr
	})}
	if err := client.Call(context.Background(), "test.transport", nil, nil); !errors.Is(err, wantErr) {
		t.Fatalf("Client.Call transport error = %v, want %v", err, wantErr)
	}
}

func TestClientCallUsesDefaultTimeoutContext(t *testing.T) {
	client := NewClient("127.0.0.1", 1)
	client.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		deadline, ok := req.Context().Deadline()
		if !ok {
			t.Fatalf("request context has no deadline")
		}
		remaining := time.Until(deadline)
		if remaining <= 0 || remaining > defaultRPCTimeout+time.Second {
			t.Fatalf("request deadline remaining = %s, want default timeout near %s", remaining, defaultRPCTimeout)
		}
		return rpcTestHTTPResponse(req, nil), nil
	})}

	if err := client.Call(context.Background(), "test.timeout", nil, nil); err != nil {
		t.Fatalf("Client.Call returned error: %v", err)
	}
}

func TestHostStorageApplyUsesExtendedTimeoutContext(t *testing.T) {
	client := NewClient("127.0.0.1", 1)
	client.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		deadline, ok := req.Context().Deadline()
		if !ok {
			t.Fatalf("request context has no deadline")
		}
		remaining := time.Until(deadline)
		if remaining < 10*time.Minute {
			t.Fatalf("request deadline remaining = %s, want extended host storage apply timeout", remaining)
		}
		return rpcTestHTTPResponse(req, HostStorageApplyResult{Restarted: true}), nil
	})}

	got, err := client.HostStorageApply(context.Background(), HostStorageApplyRequest{Yes: true})
	if err != nil {
		t.Fatalf("HostStorageApply returned error: %v", err)
	}
	if !got.Restarted {
		t.Fatalf("HostStorageApply = %#v, want restarted result", got)
	}
}

func TestServiceInfoCallsRPC(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Method != "catch.ServiceInfo" {
			t.Fatalf("method = %q, want catch.ServiceInfo", req.Method)
		}
		var params ServiceInfoRequest
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatalf("decode params: %v", err)
		}
		if params.Service != "svc-a" {
			t.Fatalf("service param = %q, want svc-a", params.Service)
		}
		_ = json.NewEncoder(w).Encode(Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  ServiceInfoResponse{Info: ServiceInfo{Name: "svc-a"}},
		})
	}))
	defer srv.Close()

	host, port := splitHostPort(t, srv.URL)
	got, err := NewClient(host, port).ServiceInfo(context.Background(), "svc-a")
	if err != nil {
		t.Fatalf("ServiceInfo returned error: %v", err)
	}
	if got.Info.Name != "svc-a" {
		t.Fatalf("ServiceInfo info = %#v, want svc-a", got.Info)
	}
}

func TestZFSServiceRootCandidatesCallsRPC(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Method != "catch.ZFSServiceRootCandidates" {
			t.Fatalf("method = %q, want catch.ZFSServiceRootCandidates", req.Method)
		}
		var params ZFSServiceRootCandidatesRequest
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatalf("decode params: %v", err)
		}
		if params.Workload != "vm" || params.Service != "devbox" {
			t.Fatalf("params = %#v, want vm/devbox", params)
		}
		_ = json.NewEncoder(w).Encode(Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: ZFSServiceRootCandidatesResponse{
				State: ZFSRootDiscoveryAvailable,
				Candidates: []ZFSServiceRootCandidate{{
					Dataset:          "flash/yeet/vms",
					Mountpoint:       "/flash/yeet/vms",
					FreeBytes:        1024,
					ChildCount:       4,
					VMChildCount:     4,
					SuggestedDataset: "flash/yeet/vms/devbox",
					Label:            "VM services root",
					Rank:             100,
				}},
			},
		})
	}))
	defer srv.Close()

	host, port := splitHostPort(t, srv.URL)
	got, err := NewClient(host, port).ZFSServiceRootCandidates(context.Background(), ZFSServiceRootCandidatesRequest{
		Workload: "vm",
		Service:  "devbox",
	})
	if err != nil {
		t.Fatalf("ZFSServiceRootCandidates returned error: %v", err)
	}
	if got.State != ZFSRootDiscoveryAvailable || len(got.Candidates) != 1 {
		t.Fatalf("response = %#v, want one available candidate", got)
	}
	if got.Candidates[0].SuggestedDataset != "flash/yeet/vms/devbox" {
		t.Fatalf("suggested dataset = %q", got.Candidates[0].SuggestedDataset)
	}
}

func TestVMDefaultsCallsRPC(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Method != "catch.VMDefaults" {
			t.Fatalf("method = %q, want catch.VMDefaults", req.Method)
		}
		var params VMDefaultsRequest
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatalf("decode params: %v", err)
		}
		if params.Service != "devbox" || params.ServiceRoot != "flash/yeet/vms/devbox" || !params.ZFS {
			t.Fatalf("params = %#v, want devbox ZFS request", params)
		}
		_ = json.NewEncoder(w).Encode(Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: VMDefaultsResponse{
				CPUs:        4,
				Memory:      "4g",
				MemoryBytes: 4 << 30,
				Disk:        "128g",
				DiskBytes:   128 << 30,
				DiskBackend: "zvol",
			},
		})
	}))
	defer srv.Close()

	host, port := splitHostPort(t, srv.URL)
	got, err := NewClient(host, port).VMDefaults(context.Background(), VMDefaultsRequest{
		Service:     "devbox",
		ServiceRoot: "flash/yeet/vms/devbox",
		ZFS:         true,
	})
	if err != nil {
		t.Fatalf("VMDefaults returned error: %v", err)
	}
	if got.CPUs != 4 || got.Memory != "4g" || got.Disk != "128g" || got.DiskBackend != "zvol" {
		t.Fatalf("VMDefaults = %#v, want 4/4g/128g zvol", got)
	}
}

func TestHostStoragePlanCallsRPC(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Method != RPCMethodHostStoragePlan {
			t.Fatalf("method = %q, want %s", req.Method, RPCMethodHostStoragePlan)
		}
		var params HostStoragePlanRequest
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatalf("decode params: %v", err)
		}
		if params.Set.DataDir == nil || params.Set.DataDir.Value != "flash/yeet/data" || !params.Set.DataDir.ZFS {
			t.Fatalf("params = %#v, want ZFS data dir", params)
		}
		if params.Set.MigrateServices != HostStorageMigrateAll {
			t.Fatalf("migrate services = %q, want all", params.Set.MigrateServices)
		}
		_ = json.NewEncoder(w).Encode(Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: HostStoragePlan{
				Current: HostStorageState{DataDir: "/home/me/yeet-data", ServicesRoot: "/home/me/yeet-data/services"},
				Desired: HostStorageState{DataDir: "/flash/yeet/data", DataDirZFS: true, ServicesRoot: "/flash/yeet/services", ServicesZFS: true},
				ServicesAction: HostStorageServicesAction{
					Mode: HostStorageMigrateAll,
					AffectedServices: []HostStorageServiceMove{{
						Name:       "web",
						From:       "/home/me/yeet-data/services/web",
						To:         "/flash/yeet/services/web",
						WasRunning: true,
					}},
				},
				RequiresRestart: true,
			},
		})
	}))
	defer srv.Close()

	host, port := splitHostPort(t, srv.URL)
	got, err := NewClient(host, port).HostStoragePlan(context.Background(), HostStoragePlanRequest{
		Set: HostStorageSetRequest{
			DataDir:         &HostStorageTarget{Value: "flash/yeet/data", ZFS: true},
			MigrateServices: HostStorageMigrateAll,
		},
	})
	if err != nil {
		t.Fatalf("HostStoragePlan returned error: %v", err)
	}
	if !got.RequiresRestart || got.Desired.DataDir != "/flash/yeet/data" || len(got.ServicesAction.AffectedServices) != 1 {
		t.Fatalf("HostStoragePlan = %#v, want restart with one affected service", got)
	}
}

func TestHostStorageApplyCallsRPC(t *testing.T) {
	plan := HostStoragePlan{
		Current: HostStorageState{DataDir: "/home/me/yeet-data", ServicesRoot: "/home/me/yeet-data/services"},
		Desired: HostStorageState{DataDir: "/flash/yeet/data", ServicesRoot: "/flash/yeet/services"},
		ServicesAction: HostStorageServicesAction{
			Mode: HostStorageMigrateAll,
			AffectedServices: []HostStorageServiceMove{{
				Name:       "web",
				From:       "/home/me/yeet-data/services/web",
				To:         "/flash/yeet/services/web",
				WasRunning: true,
			}},
		},
		RequiresRestart: true,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Method != RPCMethodHostStorageApply {
			t.Fatalf("method = %q, want %s", req.Method, RPCMethodHostStorageApply)
		}
		var params HostStorageApplyRequest
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatalf("decode params: %v", err)
		}
		if !params.Yes || !params.Plan.RequiresRestart || params.Plan.Desired.ServicesRoot != "/flash/yeet/services" {
			t.Fatalf("params = %#v, want confirmed restart plan", params)
		}
		_ = json.NewEncoder(w).Encode(Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: HostStorageApplyResult{
				MigratedServices: []HostStorageServiceMove{plan.ServicesAction.AffectedServices[0]},
				Restarted:        true,
			},
		})
	}))
	defer srv.Close()

	host, port := splitHostPort(t, srv.URL)
	got, err := NewClient(host, port).HostStorageApply(context.Background(), HostStorageApplyRequest{
		Plan: plan,
		Yes:  true,
	})
	if err != nil {
		t.Fatalf("HostStorageApply returned error: %v", err)
	}
	if !got.Restarted || len(got.MigratedServices) != 1 || got.MigratedServices[0].Name != "web" {
		t.Fatalf("HostStorageApply = %#v, want restarted web migration", got)
	}
}

func TestClientExec(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	var gotReq ExecRequest
	var gotInput bytes.Buffer

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade: %v", err)
		}
		defer conn.Close()

		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read exec request: %v", err)
		}
		if err := json.Unmarshal(data, &gotReq); err != nil {
			t.Fatalf("unmarshal exec request: %v", err)
		}

		readDone := make(chan struct{})
		go func() {
			defer close(readDone)
			for {
				mt, msg, err := conn.ReadMessage()
				if err != nil {
					return
				}
				switch mt {
				case websocket.BinaryMessage:
					_, _ = gotInput.Write(msg)
				case websocket.TextMessage:
					var ctrl ExecMessage
					if json.Unmarshal(msg, &ctrl) != nil {
						continue
					}
					if ctrl.Type == ExecMsgStdinClose {
						return
					}
				}
			}
		}()

		_ = conn.WriteMessage(websocket.BinaryMessage, []byte("output"))

		select {
		case <-readDone:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for stdin close")
		}

		exit := ExecMessage{Type: ExecMsgExit, Code: 7}
		payload, _ := json.Marshal(exit)
		_ = conn.WriteMessage(websocket.TextMessage, payload)
	}))
	defer srv.Close()

	host, port := splitHostPort(t, srv.URL)
	client := NewClient(host, port)

	var stdout bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	code, err := client.Exec(ctx, ExecRequest{Service: "svc", Args: []string{"status"}}, bytes.NewBufferString("input"), &stdout, nil)
	if err != nil {
		t.Fatalf("exec failed: %v", err)
	}
	if code != 7 {
		t.Fatalf("unexpected exit code: %d", code)
	}
	if stdout.String() != "output" {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
	if gotReq.Service != "svc" || len(gotReq.Args) != 1 || gotReq.Args[0] != "status" {
		t.Fatalf("unexpected exec request: %#v", gotReq)
	}
	if gotInput.String() != "input" {
		t.Fatalf("unexpected stdin: %q", gotInput.String())
	}
}

func TestClientExecClosesStdinWhenNil(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	var gotReq ExecRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade: %v", err)
		}
		defer conn.Close()

		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read exec request: %v", err)
		}
		if err := json.Unmarshal(data, &gotReq); err != nil {
			t.Fatalf("unmarshal exec request: %v", err)
		}

		closeSeen := make(chan struct{})
		go func() {
			defer close(closeSeen)
			for {
				mt, msg, err := conn.ReadMessage()
				if err != nil {
					return
				}
				if mt != websocket.TextMessage {
					continue
				}
				var ctrl ExecMessage
				if json.Unmarshal(msg, &ctrl) != nil {
					continue
				}
				if ctrl.Type == ExecMsgStdinClose {
					return
				}
			}
		}()

		select {
		case <-closeSeen:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for stdin close")
		}

		exit := ExecMessage{Type: ExecMsgExit, Code: 0}
		payload, _ := json.Marshal(exit)
		_ = conn.WriteMessage(websocket.TextMessage, payload)
	}))
	defer srv.Close()

	host, port := splitHostPort(t, srv.URL)
	client := NewClient(host, port)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := client.Exec(ctx, ExecRequest{Service: "svc", Args: []string{"status"}}, nil, nil, nil); err != nil {
		t.Fatalf("exec failed: %v", err)
	}
	if gotReq.Service != "svc" || len(gotReq.Args) != 1 || gotReq.Args[0] != "status" {
		t.Fatalf("unexpected exec request: %#v", gotReq)
	}
}

func TestClientExecDialError(t *testing.T) {
	wantErr := errors.New("dial failed")
	client := NewClient("127.0.0.1", 1)
	client.wsDialer = &websocket.Dialer{
		NetDialContext: func(context.Context, string, string) (net.Conn, error) {
			return nil, wantErr
		},
	}
	if _, err := client.Exec(context.Background(), ExecRequest{}, nil, nil, nil); !errors.Is(err, wantErr) {
		t.Fatalf("Exec dial error = %v, want %v", err, wantErr)
	}
}

func TestClientExecDialErrorIncludesHTTPBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `missing yeet permission "ssh"; update your Tailscale grant`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	host, port := splitHostPort(t, srv.URL)
	_, err := NewClient(host, port).Exec(context.Background(), ExecRequest{Target: ExecTargetHostShell}, nil, nil, nil)
	if err == nil {
		t.Fatal("Exec error = nil, want bad handshake")
	}
	if !strings.Contains(err.Error(), `missing yeet permission "ssh"`) {
		t.Fatalf("Exec error = %v, want auth body", err)
	}
}

func TestClientExecSendsResizeMessages(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	resizeSeen := make(chan ExecMessage, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade: %v", err)
		}
		defer conn.Close()

		if _, _, err := conn.ReadMessage(); err != nil {
			t.Fatalf("read exec request: %v", err)
		}
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				t.Fatalf("read control message: %v", err)
			}
			if mt != websocket.TextMessage {
				continue
			}
			var msg ExecMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				t.Fatalf("decode control message: %v", err)
			}
			if msg.Type == ExecMsgResize {
				resizeSeen <- msg
				break
			}
		}
		exit := ExecMessage{Type: ExecMsgExit, Code: 0}
		payload, _ := json.Marshal(exit)
		_ = conn.WriteMessage(websocket.TextMessage, payload)
	}))
	defer srv.Close()

	host, port := splitHostPort(t, srv.URL)
	client := NewClient(host, port)
	resizeCh := make(chan Resize, 1)
	resizeCh <- Resize{Rows: 40, Cols: 120}
	close(resizeCh)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := client.Exec(ctx, ExecRequest{Service: "svc"}, nil, nil, resizeCh); err != nil {
		t.Fatalf("Exec returned error: %v", err)
	}
	select {
	case got := <-resizeSeen:
		if got.Rows != 40 || got.Cols != 120 {
			t.Fatalf("resize message = %#v, want 40x120", got)
		}
	default:
		t.Fatalf("server did not observe resize message")
	}
}

type partialWriter struct {
	maxChunk int
	buf      bytes.Buffer
}

func (w *partialWriter) Write(p []byte) (int, error) {
	if len(p) > w.maxChunk {
		_, _ = w.buf.Write(p[:w.maxChunk])
		return w.maxChunk, nil
	}
	return w.buf.Write(p)
}

type retryWriter struct {
	buf    bytes.Buffer
	calls  int
	failOn int
}

func (w *retryWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls == w.failOn {
		return 0, syscall.EAGAIN
	}
	return w.buf.Write(p)
}

func TestWriteAllWithRetryHandlesShortWrites(t *testing.T) {
	w := &partialWriter{maxChunk: 2}
	if err := writeAllWithRetry(w, []byte("output")); err != nil {
		t.Fatalf("writeAllWithRetry failed: %v", err)
	}
	if got := w.buf.String(); got != "output" {
		t.Fatalf("unexpected buffer contents: %q", got)
	}
}

func TestWriteAllWithRetryRetriesTemporaryErrors(t *testing.T) {
	w := &retryWriter{failOn: 1}
	if err := writeAllWithRetry(w, []byte("output")); err != nil {
		t.Fatalf("writeAllWithRetry failed: %v", err)
	}
	if got := w.buf.String(); got != "output" {
		t.Fatalf("unexpected buffer contents: %q", got)
	}
	if w.calls < 2 {
		t.Fatalf("expected retry after temporary error, got %d calls", w.calls)
	}
}

func TestWriteAllWithRetryReturnsPermanentErrors(t *testing.T) {
	want := errors.New("boom")
	err := writeAllWithRetry(writerFunc(func([]byte) (int, error) {
		return 0, want
	}), []byte("output"))
	if !errors.Is(err, want) {
		t.Fatalf("expected %v, got %v", want, err)
	}
}

func TestWriteAllWithRetryReturnsShortWriteWhenWriterMakesNoProgress(t *testing.T) {
	err := writeAllWithRetry(writerFunc(func([]byte) (int, error) {
		return 0, nil
	}), []byte("output"))
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("writeAllWithRetry error = %v, want %v", err, io.ErrShortWrite)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) {
	return f(p)
}

func TestHandleExecReadMessageWritesBinaryOutput(t *testing.T) {
	var stdout bytes.Buffer
	result, err := handleExecReadMessage(websocket.BinaryMessage, []byte("output"), &stdout)
	if err != nil {
		t.Fatalf("handleExecReadMessage failed: %v", err)
	}
	if result.exit || result.closed {
		t.Fatalf("unexpected terminal result: %#v", result)
	}
	if got := stdout.String(); got != "output" {
		t.Fatalf("unexpected stdout: %q", got)
	}
}

func TestHandleExecReadMessageReturnsExitCode(t *testing.T) {
	payload, err := json.Marshal(ExecMessage{Type: ExecMsgExit, Code: 23})
	if err != nil {
		t.Fatalf("marshal exit message: %v", err)
	}

	result, err := handleExecReadMessage(websocket.TextMessage, payload, nil)
	if err != nil {
		t.Fatalf("handleExecReadMessage failed: %v", err)
	}
	if !result.exit || result.exitCode != 23 {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestHandleExecReadMessageRejectsInvalidText(t *testing.T) {
	_, err := handleExecReadMessage(websocket.TextMessage, []byte("{"), nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleExecReadMessageNonTerminalCases(t *testing.T) {
	result, err := handleExecReadMessage(websocket.BinaryMessage, []byte("discarded"), nil)
	if err != nil {
		t.Fatalf("binary nil stdout error: %v", err)
	}
	if result.exit || result.closed {
		t.Fatalf("binary nil stdout result = %#v, want non-terminal", result)
	}

	result, err = handleExecReadMessage(websocket.CloseMessage, nil, nil)
	if err != nil {
		t.Fatalf("close message error: %v", err)
	}
	if !result.closed {
		t.Fatalf("close message result = %#v, want closed", result)
	}

	payload, _ := json.Marshal(ExecMessage{Type: "stdout"})
	result, err = handleExecReadMessage(websocket.TextMessage, payload, nil)
	if err != nil {
		t.Fatalf("non-exit text error: %v", err)
	}
	if result.exit || result.closed {
		t.Fatalf("non-exit text result = %#v, want non-terminal", result)
	}

	result, err = handleExecReadMessage(websocket.PingMessage, []byte("ping"), nil)
	if err != nil {
		t.Fatalf("ping message error: %v", err)
	}
	if result.exit || result.closed {
		t.Fatalf("ping result = %#v, want non-terminal", result)
	}
}

func TestHandleExecReadErrorAndWaitResultCases(t *testing.T) {
	errCh := make(chan error, 1)
	handleExecReadError(&websocket.CloseError{Code: websocket.CloseNormalClosure}, errCh)
	if len(errCh) != 0 {
		t.Fatalf("normal close queued unexpected error")
	}

	wantErr := errors.New("read failed")
	handleExecReadError(wantErr, errCh)
	if got := <-errCh; !errors.Is(got, wantErr) {
		t.Fatalf("queued read error = %v, want %v", got, wantErr)
	}

	exitCh := make(chan int)
	errCh = make(chan error, 1)
	errCh <- wantErr
	if _, err := waitExecResult(context.Background(), exitCh, errCh); !errors.Is(err, wantErr) {
		t.Fatalf("waitExecResult error = %v, want %v", err, wantErr)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := waitExecResult(ctx, exitCh, make(chan error)); !errors.Is(err, context.Canceled) {
		t.Fatalf("waitExecResult canceled error = %v, want context.Canceled", err)
	}
}

func TestWSBinaryWriterCloseWriteNoops(t *testing.T) {
	if err := (&wsBinaryWriter{}).CloseWrite(); err != nil {
		t.Fatalf("CloseWrite returned error: %v", err)
	}
}

func TestDispatchEventDecodesAndCallsHandler(t *testing.T) {
	var got Event
	err := dispatchEvent([]byte(`{"time":12,"serviceName":"svc","type":"started"}`), func(ev Event) {
		got = ev
	})
	if err != nil {
		t.Fatalf("dispatchEvent failed: %v", err)
	}
	if got.Time != 12 || got.ServiceName != "svc" || got.Type != "started" {
		t.Fatalf("unexpected event: %#v", got)
	}
}

func TestHandleEventsReadErrorIgnoresExpectedClose(t *testing.T) {
	err := handleEventsReadError(context.Background(), &websocket.CloseError{Code: websocket.CloseNormalClosure})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestHandleEventsReadErrorIgnoresCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := handleEventsReadError(ctx, errors.New("read failed"))
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestDispatchEventAndEventsReadErrorFailures(t *testing.T) {
	if err := dispatchEvent([]byte("{"), func(Event) {}); err == nil {
		t.Fatalf("dispatchEvent succeeded for invalid JSON")
	}
	wantErr := errors.New("read failed")
	if err := handleEventsReadError(context.Background(), wantErr); !errors.Is(err, wantErr) {
		t.Fatalf("handleEventsReadError = %v, want %v", err, wantErr)
	}
	if err := handleEventsReadError(context.Background(), websocket.ErrCloseSent); err != nil {
		t.Fatalf("handleEventsReadError close sent = %v, want nil", err)
	}
}

func TestClientEvents(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade: %v", err)
		}
		defer conn.Close()

		_, _, err = conn.ReadMessage()
		if err != nil {
			t.Fatalf("read events request: %v", err)
		}

		ev := Event{
			Time:        time.Now().Unix(),
			ServiceName: "svc",
			Type:        "started",
		}
		if err := conn.WriteJSON(ev); err != nil {
			t.Fatalf("write event: %v", err)
		}
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	}))
	defer srv.Close()

	host, port := splitHostPort(t, srv.URL)
	client := NewClient(host, port)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var got Event
	if err := client.Events(ctx, EventsRequest{Service: "svc"}, func(ev Event) {
		got = ev
	}); err != nil {
		t.Fatalf("events failed: %v", err)
	}
	if got.ServiceName != "svc" || got.Type != "started" {
		t.Fatalf("unexpected event: %#v", got)
	}
}

func TestClientEventsDialError(t *testing.T) {
	wantErr := errors.New("dial failed")
	client := NewClient("127.0.0.1", 1)
	client.wsDialer = &websocket.Dialer{
		NetDialContext: func(context.Context, string, string) (net.Conn, error) {
			return nil, wantErr
		},
	}
	if err := client.Events(context.Background(), EventsRequest{}, func(Event) {}); !errors.Is(err, wantErr) {
		t.Fatalf("Events dial error = %v, want %v", err, wantErr)
	}
}

func TestClientEventsDialErrorIncludesHTTPBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `missing yeet permission "read"; update your Tailscale grant`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	host, port := splitHostPort(t, srv.URL)
	err := NewClient(host, port).Events(context.Background(), EventsRequest{}, func(Event) {})
	if err == nil {
		t.Fatal("Events error = nil, want bad handshake")
	}
	if !strings.Contains(err.Error(), `missing yeet permission "read"`) {
		t.Fatalf("Events error = %v, want auth body", err)
	}
}

func TestClientEventsInvalidEventJSON(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade: %v", err)
		}
		defer conn.Close()
		if _, _, err := conn.ReadMessage(); err != nil {
			t.Fatalf("read events request: %v", err)
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte("{"))
	}))
	defer srv.Close()

	host, port := splitHostPort(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := NewClient(host, port).Events(ctx, EventsRequest{}, func(Event) {
		t.Fatalf("event handler should not be called for invalid JSON")
	})
	if err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("Events invalid JSON error = %v, want JSON error", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func rpcTestHTTPResponse(req *http.Request, result any) *http.Response {
	var id json.RawMessage = []byte("1")
	var rpcReq Request
	if err := json.NewDecoder(req.Body).Decode(&rpcReq); err == nil && len(rpcReq.ID) > 0 {
		id = rpcReq.ID
	}
	var body bytes.Buffer
	_ = json.NewEncoder(&body).Encode(Response{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	})
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(&body),
	}
}
