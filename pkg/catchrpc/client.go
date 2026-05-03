// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catchrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

type Client struct {
	baseURL string
	wsURL   string

	httpClient *http.Client
	wsDialer   *websocket.Dialer

	nextID uint64
}

func NewClient(host string, port int) *Client {
	base := fmt.Sprintf("http://%s:%d", host, port)
	ws := fmt.Sprintf("ws://%s:%d", host, port)
	return &Client{
		baseURL: base,
		wsURL:   ws,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		wsDialer: websocket.DefaultDialer,
	}
}

func (c *Client) Call(ctx context.Context, method string, params any, out any) error {
	payload, err := buildRPCRequestPayload(method, atomic.AddUint64(&c.nextID, 1), params)
	if err != nil {
		return err
	}
	httpReq, err := newRPCRequest(ctx, c.baseURL, payload)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer closeIgnoringError(resp.Body)

	return decodeRPCResponse(resp, out)
}

func buildRPCRequestPayload(method string, id uint64, params any) ([]byte, error) {
	req := Request{
		JSONRPC: "2.0",
		Method:  method,
		ID:      []byte(fmt.Sprintf("%d", id)),
	}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		req.Params = b
	}
	return json.Marshal(req)
}

func newRPCRequest(ctx context.Context, baseURL string, payload []byte) (*http.Request, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/rpc", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	return httpReq, nil
}

func decodeRPCResponse(resp *http.Response, out any) error {
	if resp.StatusCode != http.StatusOK {
		return rpcStatusError(resp)
	}
	rpcResp, err := readRPCResponse(resp.Body)
	if err != nil {
		return err
	}
	return decodeRPCResult(rpcResp, out)
}

func rpcStatusError(resp *http.Response) error {
	b, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("rpc status %d: %s", resp.StatusCode, bytes.TrimSpace(b))
}

func readRPCResponse(r io.Reader) (Response, error) {
	var rpcResp Response
	err := json.NewDecoder(r).Decode(&rpcResp)
	return rpcResp, err
}

func decodeRPCResult(rpcResp Response, out any) error {
	if rpcResp.Error != nil {
		return fmt.Errorf("rpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	if out == nil {
		return nil
	}
	b, err := json.Marshal(rpcResp.Result)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

type closer interface {
	Close() error
}

func closeIgnoringError(c closer) {
	_ = c.Close()
}

type Resize struct {
	Rows int
	Cols int
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

func (w *wsBinaryWriter) WriteControl(msg ExecMessage) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conn.WriteMessage(websocket.TextMessage, payload)
}

func writeAllWithRetry(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if n > 0 {
			p = p[n:]
		}
		if err == nil {
			if len(p) == 0 {
				return nil
			}
			if n == 0 {
				return io.ErrShortWrite
			}
			continue
		}
		if errors.Is(err, syscall.EINTR) ||
			errors.Is(err, syscall.EAGAIN) ||
			errors.Is(err, syscall.EWOULDBLOCK) {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		return err
	}
	return nil
}

func (c *Client) Exec(ctx context.Context, req ExecRequest, stdin io.Reader, stdout io.Writer, resizeCh <-chan Resize) (int, error) {
	conn, _, err := c.wsDialer.DialContext(ctx, c.wsURL+"/rpc/exec", nil)
	if err != nil {
		return 0, err
	}
	defer closeIgnoringError(conn)

	if err := writeExecRequest(conn, req); err != nil {
		return 0, err
	}

	writer := &wsBinaryWriter{conn: conn}
	errCh := make(chan error, 2)
	exitCh := make(chan int, 1)

	startExecStdin(writer, stdin, errCh)
	startExecResize(writer, resizeCh)
	go readExecMessages(conn, stdout, exitCh, errCh)

	return waitExecResult(ctx, exitCh, errCh)
}

func writeExecRequest(conn *websocket.Conn, req ExecRequest) error {
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, b)
}

func startExecStdin(writer *wsBinaryWriter, stdin io.Reader, errCh chan<- error) {
	if stdin == nil {
		_ = writer.WriteControl(ExecMessage{Type: ExecMsgStdinClose})
		return
	}
	go copyExecStdin(writer, stdin, errCh)
}

func copyExecStdin(writer *wsBinaryWriter, stdin io.Reader, errCh chan<- error) {
	_, err := io.Copy(writer, stdin)
	if err != nil && !errors.Is(err, io.EOF) {
		errCh <- err
		return
	}
	_ = writer.WriteControl(ExecMessage{Type: ExecMsgStdinClose})
}

func startExecResize(writer *wsBinaryWriter, resizeCh <-chan Resize) {
	if resizeCh == nil {
		return
	}
	go func() {
		for r := range resizeCh {
			_ = writer.WriteControl(ExecMessage{Type: ExecMsgResize, Rows: r.Rows, Cols: r.Cols})
		}
	}()
}

func readExecMessages(conn *websocket.Conn, stdout io.Writer, exitCh chan<- int, errCh chan<- error) {
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			handleExecReadError(err, errCh)
			return
		}
		result, err := handleExecReadMessage(mt, data, stdout)
		if err != nil {
			errCh <- err
			return
		}
		if result.exit {
			exitCh <- result.exitCode
			return
		}
		if result.closed {
			return
		}
	}
}

func handleExecReadError(err error, errCh chan<- error) {
	if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
		return
	}
	errCh <- err
}

type execReadResult struct {
	exit     bool
	exitCode int
	closed   bool
}

func handleExecReadMessage(mt int, data []byte, stdout io.Writer) (execReadResult, error) {
	switch mt {
	case websocket.BinaryMessage:
		return execReadResult{}, writeExecOutput(stdout, data)
	case websocket.TextMessage:
		return handleExecTextMessage(data)
	case websocket.CloseMessage:
		return execReadResult{closed: true}, nil
	default:
		return execReadResult{}, nil
	}
}

func writeExecOutput(stdout io.Writer, data []byte) error {
	if stdout == nil {
		return nil
	}
	return writeAllWithRetry(stdout, data)
}

func handleExecTextMessage(data []byte) (execReadResult, error) {
	var msg ExecMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return execReadResult{}, err
	}
	if msg.Type != ExecMsgExit {
		return execReadResult{}, nil
	}
	return execReadResult{exit: true, exitCode: msg.Code}, nil
}

func waitExecResult(ctx context.Context, exitCh <-chan int, errCh <-chan error) (int, error) {
	select {
	case code := <-exitCh:
		return code, nil
	case err := <-errCh:
		return 0, err
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (c *Client) Events(ctx context.Context, req EventsRequest, onEvent func(Event)) error {
	conn, _, err := c.wsDialer.DialContext(ctx, c.wsURL+"/rpc/events", nil)
	if err != nil {
		return err
	}
	defer closeIgnoringError(conn)

	if err := writeEventsRequest(conn, req); err != nil {
		return err
	}

	stopClosing := closeWebsocketOnContext(ctx, conn)
	defer stopClosing()

	for {
		if err := readEvent(conn, onEvent); err != nil {
			return handleEventsReadError(ctx, err)
		}
	}
}

func writeEventsRequest(conn *websocket.Conn, req EventsRequest) error {
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, b)
}

func closeWebsocketOnContext(ctx context.Context, conn *websocket.Conn) func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			writeNormalWebsocketClose(conn)
			closeIgnoringError(conn)
		case <-done:
		}
	}()
	return func() {
		close(done)
	}
}

func writeNormalWebsocketClose(conn *websocket.Conn) {
	_ = conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(2*time.Second),
	)
}

func readEvent(conn *websocket.Conn, onEvent func(Event)) error {
	_, data, err := conn.ReadMessage()
	if err != nil {
		return err
	}
	return dispatchEvent(data, onEvent)
}

func dispatchEvent(data []byte, onEvent func(Event)) error {
	var ev Event
	if err := json.Unmarshal(data, &ev); err != nil {
		return err
	}
	onEvent(ev)
	return nil
}

func handleEventsReadError(ctx context.Context, err error) error {
	if isExpectedEventsClose(err) {
		return nil
	}
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func isExpectedEventsClose(err error) bool {
	return websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) ||
		errors.Is(err, websocket.ErrCloseSent)
}

func (c *Client) ServiceInfo(ctx context.Context, service string) (ServiceInfoResponse, error) {
	var resp ServiceInfoResponse
	err := c.Call(ctx, "catch.ServiceInfo", ServiceInfoRequest{Service: service}, &resp)
	return resp, err
}
