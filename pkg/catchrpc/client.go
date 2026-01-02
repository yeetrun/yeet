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
	req := Request{
		JSONRPC: "2.0",
		Method:  method,
		ID:      []byte(fmt.Sprintf("%d", atomic.AddUint64(&c.nextID, 1))),
	}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		req.Params = b
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/rpc", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("rpc status %d: %s", resp.StatusCode, bytes.TrimSpace(b))
	}
	var rpcResp Response
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return err
	}
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

func (c *Client) Exec(ctx context.Context, req ExecRequest, stdin io.Reader, stdout io.Writer, resizeCh <-chan Resize) (int, error) {
	conn, _, err := c.wsDialer.DialContext(ctx, c.wsURL+"/rpc/exec", nil)
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	b, err := json.Marshal(req)
	if err != nil {
		return 0, err
	}
	if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
		return 0, err
	}

	writer := &wsBinaryWriter{conn: conn}
	writeControl := func(msg ExecMessage) error {
		payload, err := json.Marshal(msg)
		if err != nil {
			return err
		}
		writer.mu.Lock()
		defer writer.mu.Unlock()
		return conn.WriteMessage(websocket.TextMessage, payload)
	}

	errCh := make(chan error, 2)
	exitCh := make(chan int, 1)

	if stdin != nil {
		go func() {
			_, err := io.Copy(writer, stdin)
			if err != nil && !errors.Is(err, io.EOF) {
				errCh <- err
				return
			}
			_ = writeControl(ExecMessage{Type: ExecMsgStdinClose})
		}()
	} else {
		_ = writeControl(ExecMessage{Type: ExecMsgStdinClose})
	}

	if resizeCh != nil {
		go func() {
			for r := range resizeCh {
				_ = writeControl(ExecMessage{Type: ExecMsgResize, Rows: r.Rows, Cols: r.Cols})
			}
		}()
	}

	go func() {
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					return
				}
				errCh <- err
				return
			}
			switch mt {
			case websocket.BinaryMessage:
				if stdout != nil {
					if _, err := stdout.Write(data); err != nil {
						errCh <- err
						return
					}
				}
			case websocket.TextMessage:
				var msg ExecMessage
				if err := json.Unmarshal(data, &msg); err != nil {
					errCh <- err
					return
				}
				if msg.Type == ExecMsgExit {
					exitCh <- msg.Code
					return
				}
			case websocket.CloseMessage:
				return
			}
		}
	}()

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
	defer conn.Close()

	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
		return err
	}

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(2*time.Second))
			_ = conn.Close()
		case <-done:
		}
	}()
	defer close(done)

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) ||
				errors.Is(err, websocket.ErrCloseSent) {
				return nil
			}
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		var ev Event
		if err := json.Unmarshal(data, &ev); err != nil {
			return err
		}
		onEvent(ev)
	}
}
