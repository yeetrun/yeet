// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shayne/yeet/pkg/catchrpc"
)

var rpcUpgrader = websocket.Upgrader{
	ReadBufferSize:  16 * 1024,
	WriteBufferSize: 16 * 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// RPCMux returns the RPC handler that serves JSON-RPC and streaming endpoints.
func (s *Server) RPCMux() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/rpc", s.authZ(http.HandlerFunc(s.handleRPC)))
	mux.Handle("/rpc/exec", s.authZ(http.HandlerFunc(s.handleExecWS)))
	mux.Handle("/rpc/events", s.authZ(http.HandlerFunc(s.handleEventsWS)))
	mux.Handle("/v2/", s.registry)
	return mux
}

func (s *Server) authZ(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := s.verifyCaller(r.Context(), r.RemoteAddr); err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeRPCResponse(w http.ResponseWriter, resp catchrpc.Response) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, msg string, data any) {
	resp := catchrpc.Response{
		JSONRPC: "2.0",
		ID:      id,
		Error: &catchrpc.Error{
			Code:    code,
			Message: msg,
			Data:    data,
		},
	}
	writeRPCResponse(w, resp)
}

func (s *Server) handleRPC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Body == nil {
		writeRPCError(w, []byte("null"), catchrpc.ErrInvalidRequest, "empty body", nil)
		return
	}
	defer r.Body.Close()

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var req catchrpc.Request
	if err := dec.Decode(&req); err != nil {
		writeRPCError(w, []byte("null"), catchrpc.ErrParseError, "parse error", err.Error())
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		writeRPCError(w, req.ID, catchrpc.ErrInvalidRequest, "invalid request", nil)
		return
	}
	if len(req.ID) == 0 {
		return // notification
	}

	switch req.Method {
	case "catch.Info":
		writeRPCResponse(w, catchrpc.Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  GetInfo(),
		})
	case "catch.ServiceInfo":
		var params catchrpc.ServiceInfoRequest
		if len(req.Params) == 0 {
			writeRPCError(w, req.ID, catchrpc.ErrInvalidParams, "missing params", nil)
			return
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			writeRPCError(w, req.ID, catchrpc.ErrInvalidParams, "invalid params", err.Error())
			return
		}
		params.Service = strings.TrimSpace(params.Service)
		if params.Service == "" {
			writeRPCError(w, req.ID, catchrpc.ErrInvalidParams, "missing service", nil)
			return
		}
		resp, err := s.serviceInfo(params.Service)
		if err != nil {
			writeRPCError(w, req.ID, catchrpc.ErrInternal, "failed to get service info", err.Error())
			return
		}
		writeRPCResponse(w, catchrpc.Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  resp,
		})
	case "catch.ServicesList":
		list, err := s.listServices()
		if err != nil {
			writeRPCError(w, req.ID, catchrpc.ErrInternal, "failed to list services", err.Error())
			return
		}
		writeRPCResponse(w, catchrpc.Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  list,
		})
	default:
		writeRPCError(w, req.ID, catchrpc.ErrMethodNotFound, "method not found", req.Method)
	}
}

type serviceInfo struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func (s *Server) listServices() ([]serviceInfo, error) {
	dv, err := s.getDB()
	if err != nil {
		return nil, err
	}
	var out []serviceInfo
	for name, sv := range dv.Services().All() {
		out = append(out, serviceInfo{
			Name: string(name),
			Type: string(sv.ServiceType()),
		})
	}
	return out, nil
}

type readWriter struct {
	io.Reader
	io.Writer
}

type wsBinaryWriter struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (w *wsBinaryWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.conn.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *wsBinaryWriter) CloseWrite() error {
	return nil
}

type wsCloser struct {
	conn *websocket.Conn
}

func (c wsCloser) Close() error {
	return c.conn.Close()
}

func (s *Server) handleExecWS(w http.ResponseWriter, r *http.Request) {
	conn, err := rpcUpgrader.Upgrade(w, r, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer conn.Close()

	_, data, err := conn.ReadMessage()
	if err != nil {
		return
	}
	var req catchrpc.ExecRequest
	if err := json.Unmarshal(data, &req); err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"exit","code":1,"error":"invalid exec request"}`))
		return
	}
	if req.Service == "" {
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"exit","code":1,"error":"missing service"}`))
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	pr, pw := io.Pipe()
	writer := &wsBinaryWriter{conn: conn}
	rw := readWriter{Reader: pr, Writer: writer}

	execer := &ttyExecer{
		ctx:         ctx,
		s:           s,
		args:        req.Args,
		sn:          req.Service,
		hostLabel:   req.Host,
		user:        req.User,
		payloadName: req.PayloadName,
		progress:    req.Progress,
		rawRW:       rw,
		rawCloser:   wsCloser{conn: conn},
		isPty:       req.TTY,
		ptyReq: PtySpec{
			Term: req.Term,
			Window: PtyWindow{
				Width:  req.Cols,
				Height: req.Rows,
			},
		},
	}

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					_ = pw.Close()
					cancel()
					return
				}
				_ = pw.CloseWithError(err)
				cancel()
				return
			}
			switch mt {
			case websocket.BinaryMessage:
				if _, err := pw.Write(msg); err != nil {
					cancel()
					return
				}
			case websocket.TextMessage:
				var ctrl catchrpc.ExecMessage
				if err := json.Unmarshal(msg, &ctrl); err != nil {
					continue
				}
				switch ctrl.Type {
				case catchrpc.ExecMsgResize:
					execer.ResizeTTY(ctrl.Cols, ctrl.Rows)
				case catchrpc.ExecMsgStdinClose:
					_ = pw.Close()
				}
			}
		}
	}()

	err = execer.run()
	code := 0
	if err != nil {
		code = 1
	}
	resp := catchrpc.ExecMessage{Type: catchrpc.ExecMsgExit, Code: code}
	if err != nil {
		resp.Error = err.Error()
	}
	payload, _ := json.Marshal(resp)
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_ = conn.WriteMessage(websocket.TextMessage, payload)
	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(5*time.Second))
	_ = conn.Close()
	<-readDone
}

func (s *Server) handleEventsWS(w http.ResponseWriter, r *http.Request) {
	conn, err := rpcUpgrader.Upgrade(w, r, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer conn.Close()

	var sub catchrpc.EventsRequest
	_, msg, err := conn.ReadMessage()
	if err == nil && len(msg) > 0 {
		_ = json.Unmarshal(msg, &sub)
	}

	ch := make(chan Event)
	all := sub.All
	service := sub.Service
	h := s.AddEventListener(ch, func(et Event) bool {
		if all {
			return true
		}
		if service == "" {
			return false
		}
		return et.ServiceName == service
	})
	defer s.RemoveEventListener(h)

	for {
		select {
		case event := <-ch:
			if err := conn.WriteJSON(event); err != nil {
				if !errors.Is(err, websocket.ErrCloseSent) {
					log.Printf("event write failed: %v", err)
				}
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}
