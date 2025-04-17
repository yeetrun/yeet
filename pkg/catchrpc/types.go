// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catchrpc

import "encoding/json"

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

const (
	ErrParseError     = -32700
	ErrInvalidRequest = -32600
	ErrMethodNotFound = -32601
	ErrInvalidParams  = -32602
	ErrInternal       = -32603
)

type ExecRequest struct {
	Service     string   `json:"service"`
	User        string   `json:"user,omitempty"`
	Args        []string `json:"args"`
	PayloadName string   `json:"payloadName,omitempty"`
	TTY         bool     `json:"tty"`
	Term        string   `json:"term,omitempty"`
	Rows        int      `json:"rows,omitempty"`
	Cols        int      `json:"cols,omitempty"`
}

type ExecMessage struct {
	Type  string `json:"type"`
	Rows  int    `json:"rows,omitempty"`
	Cols  int    `json:"cols,omitempty"`
	Code  int    `json:"code,omitempty"`
	Error string `json:"error,omitempty"`
}

const (
	ExecMsgResize     = "resize"
	ExecMsgStdinClose = "stdin-close"
	ExecMsgExit       = "exit"
)

type EventsRequest struct {
	Service string `json:"service,omitempty"`
	All     bool   `json:"all,omitempty"`
}

type Event struct {
	Time        int64  `json:"time"`
	ServiceName string `json:"serviceName"`
	Type        string `json:"type"`
	Data        any    `json:"data,omitempty"`
}
