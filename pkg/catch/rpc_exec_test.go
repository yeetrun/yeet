// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shayne/yeet/pkg/catchrpc"
)

func TestRPCExecVersionJSON(t *testing.T) {
	server := newTestServer(t)
	ts := newTestHTTPServer(t, server)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/rpc/exec"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	req := catchrpc.ExecRequest{
		Service: "sys",
		Args:    []string{"version", "--json"},
		TTY:     false,
	}
	payload, _ := json.Marshal(req)
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("send exec request: %v", err)
	}

	var out bytes.Buffer
	for {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		mt, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read message: %v", err)
		}
		switch mt {
		case websocket.BinaryMessage:
			out.Write(data)
		case websocket.TextMessage:
			var msg catchrpc.ExecMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				t.Fatalf("decode control: %v", err)
			}
			if msg.Type == catchrpc.ExecMsgExit {
				if msg.Code != 0 {
					t.Fatalf("unexpected exit code: %d", msg.Code)
				}
				if payload := bytes.TrimSpace(out.Bytes()); len(payload) > 0 {
					var info ServerInfo
					if err := json.Unmarshal(payload, &info); err != nil {
						t.Fatalf("decode output: %v", err)
					}
					if info.GOOS == "" || info.GOARCH == "" {
						t.Fatalf("unexpected info: %#v", info)
					}
				}
				return
			}
		}
	}
}

func TestRPCExecMissingService(t *testing.T) {
	server := newTestServer(t)
	ts := newTestHTTPServer(t, server)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/rpc/exec"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	req := catchrpc.ExecRequest{
		Service: "",
		Args:    []string{"status"},
		TTY:     false,
	}
	payload, _ := json.Marshal(req)
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("send exec request: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	mt, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read message: %v", err)
	}
	if mt != websocket.TextMessage {
		t.Fatalf("expected text message, got %d", mt)
	}
	var msg catchrpc.ExecMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("decode control: %v", err)
	}
	if msg.Type != catchrpc.ExecMsgExit || msg.Code != 1 {
		t.Fatalf("unexpected exit message: %#v", msg)
	}
}

func newTestHTTPServer(t *testing.T, server *Server) *httptest.Server {
	t.Helper()
	return httptest.NewServer(server.RPCMux())
}
