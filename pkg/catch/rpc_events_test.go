// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/yeetrun/yeet/pkg/catchrpc"
)

func TestRPCEventsFilter(t *testing.T) {
	server := newTestServer(t)
	ts := newTestHTTPServer(t, server)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/rpc/events"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	sub := catchrpc.EventsRequest{Service: "svc"}
	payload, _ := json.Marshal(sub)
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("send events request: %v", err)
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		server.PublishEvent(Event{ServiceName: "other", Type: EventTypeHeartbeat})
		server.PublishEvent(Event{ServiceName: "svc", Type: EventTypeHeartbeat})
	}()

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read event: %v", err)
	}
	var ev Event
	if err := json.Unmarshal(data, &ev); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if ev.ServiceName != "svc" {
		t.Fatalf("unexpected event service: %#v", ev)
	}
}
