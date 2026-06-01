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
	VMSSHKey    string       `json:"vmSshKey,omitempty"`
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

type ArtifactHashesRequest struct {
	Service string `json:"service"`
}

type ArtifactHash struct {
	Kind   string `json:"kind,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

type ArtifactHashesResponse struct {
	Found   bool          `json:"found"`
	Message string        `json:"message,omitempty"`
	Payload *ArtifactHash `json:"payload,omitempty"`
	Env     *ArtifactHash `json:"env,omitempty"`
}

type SnapshotPolicy struct {
	Enabled  *bool    `json:"enabled,omitempty"`
	KeepLast *int     `json:"keepLast,omitempty"`
	MaxAge   string   `json:"maxAge,omitempty"`
	Events   []string `json:"events,omitempty"`
	Required *bool    `json:"required,omitempty"`
}

type EffectiveSnapshotPolicy struct {
	Enabled  bool     `json:"enabled"`
	KeepLast int      `json:"keepLast"`
	MaxAge   string   `json:"maxAge"`
	Events   []string `json:"events,omitempty"`
	Required bool     `json:"required"`
}

type ServiceSnapshots struct {
	Override  *SnapshotPolicy         `json:"override,omitempty"`
	Effective EffectiveSnapshotPolicy `json:"effective,omitempty"`
}

type SnapshotDefaultsResponse struct {
	Defaults  SnapshotPolicy          `json:"defaults,omitempty"`
	Effective EffectiveSnapshotPolicy `json:"effective,omitempty"`
}

type ServiceInfo struct {
	Name             string            `json:"name"`
	ServiceType      string            `json:"serviceType,omitempty"`
	DataType         string            `json:"dataType,omitempty"`
	Generation       int               `json:"generation,omitempty"`
	LatestGeneration int               `json:"latestGeneration,omitempty"`
	Staged           bool              `json:"staged,omitempty"`
	Paths            ServicePaths      `json:"paths,omitempty"`
	Network          ServiceNetwork    `json:"network,omitempty"`
	Status           ServiceStatus     `json:"status,omitempty"`
	Images           []ServiceImage    `json:"images,omitempty"`
	VM               *ServiceVM        `json:"vm,omitempty"`
	Snapshots        *ServiceSnapshots `json:"snapshots,omitempty"`
}

type ServiceVM struct {
	Runtime      string             `json:"runtime,omitempty"`
	Image        string             `json:"image,omitempty"`
	ImageVersion string             `json:"imageVersion,omitempty"`
	CPUs         int                `json:"cpus,omitempty"`
	MemoryBytes  int64              `json:"memoryBytes,omitempty"`
	DiskBytes    int64              `json:"diskBytes,omitempty"`
	DiskBackend  string             `json:"diskBackend,omitempty"`
	DiskPath     string             `json:"diskPath,omitempty"`
	SSH          *ServiceVMSSH      `json:"ssh,omitempty"`
	Console      *ServiceVMConsole  `json:"console,omitempty"`
	Networks     []ServiceVMNetwork `json:"networks,omitempty"`
	SetupState   string             `json:"setupState,omitempty"`
}

type ServiceVMSSH struct {
	User string `json:"user,omitempty"`
	Host string `json:"host,omitempty"`
}

type ServiceVMConsole struct {
	Available  bool   `json:"available"`
	SocketPath string `json:"socketPath,omitempty"`
}

type ServiceVMNetwork struct {
	Mode      string `json:"mode,omitempty"`
	Interface string `json:"interface,omitempty"`
	IP        string `json:"ip,omitempty"`
	MAC       string `json:"mac,omitempty"`
}

type ServicePaths struct {
	// Root is the effective filesystem root. It is kept for existing clients.
	Root           string `json:"root,omitempty"`
	EffectiveRoot  string `json:"effectiveRoot,omitempty"`
	ServiceRoot    string `json:"serviceRoot,omitempty"`
	ServiceRootZFS string `json:"serviceRootZfs,omitempty"`
}

type ServiceNetwork struct {
	SvcIP     string            `json:"svcIp,omitempty"`
	IPs       []ServiceIP       `json:"ips,omitempty"`
	IPError   string            `json:"ipError,omitempty"`
	Ports     []ServicePort     `json:"ports,omitempty"`
	Macvlan   *ServiceMacvlan   `json:"macvlan,omitempty"`
	Tailscale *ServiceTailscale `json:"tailscale,omitempty"`

	PortsPresent bool `json:"-"`
}

type serviceNetworkJSON struct {
	SvcIP     string            `json:"svcIp,omitempty"`
	IPs       []ServiceIP       `json:"ips,omitempty"`
	IPError   string            `json:"ipError,omitempty"`
	Ports     []ServicePort     `json:"ports,omitempty"`
	Macvlan   *ServiceMacvlan   `json:"macvlan,omitempty"`
	Tailscale *ServiceTailscale `json:"tailscale,omitempty"`
}

type serviceNetworkJSONWithPorts struct {
	SvcIP     string            `json:"svcIp,omitempty"`
	IPs       []ServiceIP       `json:"ips,omitempty"`
	IPError   string            `json:"ipError,omitempty"`
	Ports     []ServicePort     `json:"ports"`
	Macvlan   *ServiceMacvlan   `json:"macvlan,omitempty"`
	Tailscale *ServiceTailscale `json:"tailscale,omitempty"`
}

func (n ServiceNetwork) MarshalJSON() ([]byte, error) {
	if n.PortsPresent || len(n.Ports) != 0 {
		ports := n.Ports
		if ports == nil {
			ports = []ServicePort{}
		}
		return json.Marshal(serviceNetworkJSONWithPorts{
			SvcIP:     n.SvcIP,
			IPs:       n.IPs,
			IPError:   n.IPError,
			Ports:     ports,
			Macvlan:   n.Macvlan,
			Tailscale: n.Tailscale,
		})
	}
	return json.Marshal(serviceNetworkJSON{
		SvcIP:     n.SvcIP,
		IPs:       n.IPs,
		IPError:   n.IPError,
		Macvlan:   n.Macvlan,
		Tailscale: n.Tailscale,
	})
}

func (n *ServiceNetwork) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var decoded serviceNetworkJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*n = ServiceNetwork{
		SvcIP:        decoded.SvcIP,
		IPs:          decoded.IPs,
		IPError:      decoded.IPError,
		Ports:        decoded.Ports,
		Macvlan:      decoded.Macvlan,
		Tailscale:    decoded.Tailscale,
		PortsPresent: false,
	}
	if _, ok := raw["ports"]; ok {
		n.PortsPresent = true
		if n.Ports == nil {
			n.Ports = []ServicePort{}
		}
	}
	return nil
}

type ServicePort struct {
	HostIP        string `json:"hostIp,omitempty"`
	HostPort      uint16 `json:"hostPort"`
	ContainerPort uint16 `json:"containerPort"`
	Protocol      string `json:"protocol,omitempty"`
	Raw           string `json:"raw,omitempty"`
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
