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

type ProgressMode string

const (
	ProgressAuto  ProgressMode = "auto"
	ProgressTTY   ProgressMode = "tty"
	ProgressPlain ProgressMode = "plain"
	ProgressQuiet ProgressMode = "quiet"
)

type ExecRequest struct {
	Service     string       `json:"service"`
	Host        string       `json:"host,omitempty"`
	User        string       `json:"user,omitempty"`
	Args        []string     `json:"args"`
	PayloadName string       `json:"payloadName,omitempty"`
	TTY         bool         `json:"tty"`
	Progress    ProgressMode `json:"progress,omitempty"`
	Term        string       `json:"term,omitempty"`
	Rows        int          `json:"rows,omitempty"`
	Cols        int          `json:"cols,omitempty"`
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

type ServiceInfoRequest struct {
	Service string `json:"service"`
}

type ServiceInfoResponse struct {
	Found   bool        `json:"found"`
	Message string      `json:"message,omitempty"`
	Info    ServiceInfo `json:"info,omitempty"`
}

type TailscaleSetupRequest struct {
	ClientSecret string `json:"clientSecret"`
}

type TailscaleSetupResponse struct {
	Path     string `json:"path"`
	Verified bool   `json:"verified"`
}

type ServiceInfo struct {
	Name             string         `json:"name"`
	ServiceType      string         `json:"serviceType,omitempty"`
	DataType         string         `json:"dataType,omitempty"`
	Generation       int            `json:"generation,omitempty"`
	LatestGeneration int            `json:"latestGeneration,omitempty"`
	Staged           bool           `json:"staged,omitempty"`
	Paths            ServicePaths   `json:"paths,omitempty"`
	Network          ServiceNetwork `json:"network,omitempty"`
	Status           ServiceStatus  `json:"status,omitempty"`
	Images           []ServiceImage `json:"images,omitempty"`
}

type ServicePaths struct {
	Root string `json:"root,omitempty"`
}

type ServiceNetwork struct {
	SvcIP     string            `json:"svcIp,omitempty"`
	IPs       []ServiceIP       `json:"ips,omitempty"`
	IPError   string            `json:"ipError,omitempty"`
	Macvlan   *ServiceMacvlan   `json:"macvlan,omitempty"`
	Tailscale *ServiceTailscale `json:"tailscale,omitempty"`
}

type ServiceIP struct {
	Label     string `json:"label,omitempty"`
	IP        string `json:"ip,omitempty"`
	Interface string `json:"interface,omitempty"`
}

type ServiceMacvlan struct {
	Interface string `json:"interface,omitempty"`
	Parent    string `json:"parent,omitempty"`
	Mac       string `json:"mac,omitempty"`
	VLAN      int    `json:"vlan,omitempty"`
}

type ServiceTailscale struct {
	Interface string   `json:"interface,omitempty"`
	Version   string   `json:"version,omitempty"`
	ExitNode  string   `json:"exitNode,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	StableID  string   `json:"stableId,omitempty"`
}

type ServiceStatus struct {
	Components []ServiceComponentStatus `json:"components,omitempty"`
	Error      string                   `json:"error,omitempty"`
}

type ServiceComponentStatus struct {
	Name   string `json:"name,omitempty"`
	Status string `json:"status,omitempty"`
}

type ServiceImage struct {
	Repo string                     `json:"repo"`
	Refs map[string]ServiceImageRef `json:"refs,omitempty"`
}

type ServiceImageRef struct {
	Digest    string `json:"digest,omitempty"`
	MediaType string `json:"mediaType,omitempty"`
}
