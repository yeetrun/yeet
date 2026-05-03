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
	if err := client.Call(context.Background(), "test.nope", nil, &out); err == nil {
		t.Fatal("expected error")
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
