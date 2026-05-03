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
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/yeetrun/yeet/pkg/catchrpc"
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

func newRPCResponse(id json.RawMessage, result any) catchrpc.Response {
	return catchrpc.Response{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
}

func newRPCError(id json.RawMessage, code int, msg string, data any) catchrpc.Response {
	return catchrpc.Response{
		JSONRPC: "2.0",
		ID:      id,
		Error: &catchrpc.Error{
			Code:    code,
			Message: msg,
			Data:    data,
		},
	}
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, msg string, data any) {
	writeRPCResponse(w, newRPCError(id, code, msg, data))
}

func closeRequestBody(body io.Closer) {
	if err := body.Close(); err != nil {
		log.Printf("request body close failed: %v", err)
	}
}

func decodeRPCRequest(body io.Reader) (catchrpc.Request, error) {
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()

	var req catchrpc.Request
	err := dec.Decode(&req)
	return req, err
}

func invalidParamsError(msg string, data any) *catchrpc.Error {
	return &catchrpc.Error{
		Code:    catchrpc.ErrInvalidParams,
		Message: msg,
		Data:    data,
	}
}

func decodeRPCParams(raw json.RawMessage, out any) *catchrpc.Error {
	if len(raw) == 0 {
		return invalidParamsError("missing params", nil)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return invalidParamsError("invalid params", err.Error())
	}
	return nil
}

func validateServiceParam(service string) (string, *catchrpc.Error) {
	service = strings.TrimSpace(service)
	if service == "" {
		return "", invalidParamsError("missing service", nil)
	}
	return service, nil
}

func responseFromRPCError(id json.RawMessage, err *catchrpc.Error) catchrpc.Response {
	return newRPCError(id, err.Code, err.Message, err.Data)
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
	defer closeRequestBody(r.Body)

	req, err := decodeRPCRequest(r.Body)
	if err != nil {
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

	writeRPCResponse(w, s.dispatchRPC(req))
}

func (s *Server) dispatchRPC(req catchrpc.Request) catchrpc.Response {
	switch req.Method {
	case "catch.Info":
		return newRPCResponse(req.ID, GetInfoWithConfig(&s.cfg))
	case "catch.ServiceInfo":
		return s.handleRPCServiceInfo(req)
	case "catch.ArtifactHashes":
		return s.handleRPCArtifactHashes(req)
	case "catch.TailscaleSetup":
		return s.handleRPCTailscaleSetup(req)
	case "catch.ServicesList":
		list, err := s.listServices()
		if err != nil {
			return newRPCError(req.ID, catchrpc.ErrInternal, "failed to list services", err.Error())
		}
		return newRPCResponse(req.ID, list)
	default:
		return newRPCError(req.ID, catchrpc.ErrMethodNotFound, "method not found", req.Method)
	}
}

func (s *Server) handleRPCServiceInfo(req catchrpc.Request) catchrpc.Response {
	var params catchrpc.ServiceInfoRequest
	if rpcErr := decodeRPCParams(req.Params, &params); rpcErr != nil {
		return responseFromRPCError(req.ID, rpcErr)
	}
	service, rpcErr := validateServiceParam(params.Service)
	if rpcErr != nil {
		return responseFromRPCError(req.ID, rpcErr)
	}
	resp, err := s.serviceInfo(service)
	if err != nil {
		return newRPCError(req.ID, catchrpc.ErrInternal, "failed to get service info", err.Error())
	}
	return newRPCResponse(req.ID, resp)
}

func (s *Server) handleRPCArtifactHashes(req catchrpc.Request) catchrpc.Response {
	var params catchrpc.ArtifactHashesRequest
	if rpcErr := decodeRPCParams(req.Params, &params); rpcErr != nil {
		return responseFromRPCError(req.ID, rpcErr)
	}
	service, rpcErr := validateServiceParam(params.Service)
	if rpcErr != nil {
		return responseFromRPCError(req.ID, rpcErr)
	}
	resp, err := s.artifactHashes(service)
	if err != nil {
		return newRPCError(req.ID, catchrpc.ErrInternal, "failed to get artifact hashes", err.Error())
	}
	return newRPCResponse(req.ID, resp)
}

func (s *Server) handleRPCTailscaleSetup(req catchrpc.Request) catchrpc.Response {
	var params catchrpc.TailscaleSetupRequest
	if rpcErr := decodeRPCParams(req.Params, &params); rpcErr != nil {
		return responseFromRPCError(req.ID, rpcErr)
	}
	resp, err := s.setupTailscale(params.ClientSecret)
	if err != nil {
		return newRPCError(req.ID, catchrpc.ErrInternal, "failed to set tailscale secret", err.Error())
	}
	return newRPCResponse(req.ID, resp)
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

func closeWebSocketConn(conn *websocket.Conn, label string) {
	if err := conn.Close(); err != nil {
		if errors.Is(err, net.ErrClosed) || errors.Is(err, websocket.ErrCloseSent) {
			return
		}
		if strings.Contains(err.Error(), "use of closed network connection") {
			return
		}
		log.Printf("%s websocket close failed: %v", label, err)
	}
}

func upgradeRPCWebSocket(w http.ResponseWriter, r *http.Request) (*websocket.Conn, bool) {
	conn, err := rpcUpgrader.Upgrade(w, r, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, false
	}
	return conn, true
}

func writeExecExit(conn *websocket.Conn, code int, errText string) {
	resp := catchrpc.ExecMessage{Type: catchrpc.ExecMsgExit, Code: code, Error: errText}
	payload, err := json.Marshal(resp)
	if err != nil {
		log.Printf("marshal exec exit failed: %v", err)
		return
	}
	_ = conn.WriteMessage(websocket.TextMessage, payload)
}

func readExecRequest(conn *websocket.Conn) (catchrpc.ExecRequest, bool) {
	_, data, err := conn.ReadMessage()
	if err != nil {
		return catchrpc.ExecRequest{}, false
	}
	var req catchrpc.ExecRequest
	if err := json.Unmarshal(data, &req); err != nil {
		writeExecExit(conn, 1, "invalid exec request")
		return catchrpc.ExecRequest{}, false
	}
	if req.Service == "" {
		writeExecExit(conn, 1, "missing service")
		return catchrpc.ExecRequest{}, false
	}
	return req, true
}

func (s *Server) handleExecWS(w http.ResponseWriter, r *http.Request) {
	conn, ok := upgradeRPCWebSocket(w, r)
	if !ok {
		return
	}
	defer closeWebSocketConn(conn, "exec")

	req, ok := readExecRequest(conn)
	if !ok {
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

	readDone := pumpExecWebSocketInput(conn, pw, execer, cancel)

	err := execer.run()
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	writeExecRunResult(conn, err)
	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(5*time.Second))
	closeWebSocketConn(conn, "exec")
	<-readDone
}

func pumpExecWebSocketInput(conn *websocket.Conn, pw *io.PipeWriter, execer *ttyExecer, cancel context.CancelFunc) <-chan struct{} {
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				closeExecInput(pw, err)
				cancel()
				return
			}
			if !handleExecInputMessage(mt, msg, pw, execer) {
				cancel()
				return
			}
		}
	}()
	return readDone
}

func closeExecInput(pw *io.PipeWriter, err error) {
	if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
		_ = pw.Close()
		return
	}
	_ = pw.CloseWithError(err)
}

func handleExecInputMessage(mt int, msg []byte, pw *io.PipeWriter, execer *ttyExecer) bool {
	switch mt {
	case websocket.BinaryMessage:
		_, err := pw.Write(msg)
		return err == nil
	case websocket.TextMessage:
		handleExecControlMessage(msg, pw, execer)
	}
	return true
}

func handleExecControlMessage(msg []byte, pw *io.PipeWriter, execer *ttyExecer) {
	var ctrl catchrpc.ExecMessage
	if err := json.Unmarshal(msg, &ctrl); err != nil {
		return
	}
	switch ctrl.Type {
	case catchrpc.ExecMsgResize:
		execer.ResizeTTY(ctrl.Cols, ctrl.Rows)
	case catchrpc.ExecMsgStdinClose:
		_ = pw.Close()
	}
}

func writeExecRunResult(conn *websocket.Conn, err error) {
	if err == nil {
		writeExecExit(conn, 0, "")
		return
	}
	writeExecExit(conn, 1, err.Error())
}

func (s *Server) handleEventsWS(w http.ResponseWriter, r *http.Request) {
	conn, ok := upgradeRPCWebSocket(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	sub := readEventsSubscription(conn)
	closeDone := watchEventsWebSocketClose(conn, cancel)
	defer func() {
		closeWebSocketConn(conn, "events")
		<-closeDone
	}()

	ch := make(chan Event)
	h := s.AddEventListener(ch, eventFilter(sub))
	defer s.RemoveEventListener(h)

	for {
		select {
		case event := <-ch:
			if !writeEventMessage(conn, event) {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func watchEventsWebSocketClose(conn *websocket.Conn, cancel context.CancelFunc) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				cancel()
				return
			}
		}
	}()
	return done
}

func readEventsSubscription(conn *websocket.Conn) catchrpc.EventsRequest {
	var sub catchrpc.EventsRequest
	_, msg, err := conn.ReadMessage()
	if err == nil && len(msg) > 0 {
		_ = json.Unmarshal(msg, &sub)
	}
	return sub
}

func eventFilter(sub catchrpc.EventsRequest) func(Event) bool {
	return func(et Event) bool {
		if sub.All {
			return true
		}
		if sub.Service == "" {
			return false
		}
		return et.ServiceName == sub.Service
	}
}

func writeEventMessage(conn *websocket.Conn, event Event) bool {
	if err := conn.WriteJSON(event); err != nil {
		if !errors.Is(err, websocket.ErrCloseSent) {
			log.Printf("event write failed: %v", err)
		}
		return false
	}
	return true
}
