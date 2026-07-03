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

type ExecTarget string

const (
	ExecTargetServiceCommand ExecTarget = ""
	ExecTargetHostShell      ExecTarget = "host-shell"
	ExecTargetServiceShell   ExecTarget = "service-shell"
	ExecTargetVMSSHProxy     ExecTarget = "vm-ssh-proxy"
)

type ExecRequest struct {
	Target      ExecTarget   `json:"target,omitempty"`
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
	Trace       bool         `json:"trace,omitempty"`
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

type ZFSRootDiscoveryState string

const (
	ZFSRootDiscoveryAvailable       ZFSRootDiscoveryState = "available"
	ZFSRootDiscoveryHostUnreachable ZFSRootDiscoveryState = "host-unreachable"
	ZFSRootDiscoveryUnsupportedRPC  ZFSRootDiscoveryState = "unsupported-rpc"
	ZFSRootDiscoveryZFSMissing      ZFSRootDiscoveryState = "zfs-missing"
	ZFSRootDiscoveryNoFilesystems   ZFSRootDiscoveryState = "no-filesystems"
	ZFSRootDiscoveryError           ZFSRootDiscoveryState = "error"
)

type ZFSServiceRootCandidatesRequest struct {
	Workload string `json:"workload,omitempty"`
	Service  string `json:"service,omitempty"`
}

type ZFSServiceRootCandidate struct {
	Dataset           string `json:"dataset"`
	Mountpoint        string `json:"mountpoint,omitempty"`
	FreeBytes         int64  `json:"freeBytes,omitempty"`
	ChildCount        int    `json:"childCount,omitempty"`
	VMChildCount      int    `json:"vmChildCount,omitempty"`
	ServiceChildCount int    `json:"serviceChildCount,omitempty"`
	SuggestedDataset  string `json:"suggestedDataset,omitempty"`
	Label             string `json:"label,omitempty"`
	Rank              int    `json:"rank,omitempty"`
}

type ZFSServiceRootCandidatesResponse struct {
	State      ZFSRootDiscoveryState     `json:"state"`
	Candidates []ZFSServiceRootCandidate `json:"candidates,omitempty"`
	Warnings   []string                  `json:"warnings,omitempty"`
}

type VMDefaultsRequest struct {
	Service     string `json:"service,omitempty"`
	ServiceRoot string `json:"serviceRoot,omitempty"`
	ZFS         bool   `json:"zfs,omitempty"`
}

type VMDefaultsResponse struct {
	CPUs        int      `json:"cpus,omitempty"`
	Memory      string   `json:"memory,omitempty"`
	MemoryBytes int64    `json:"memoryBytes,omitempty"`
	Disk        string   `json:"disk,omitempty"`
	DiskBytes   int64    `json:"diskBytes,omitempty"`
	DiskBackend string   `json:"diskBackend,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
}

const (
	RPCMethodHostStoragePlan  = "catch.HostStoragePlan"
	RPCMethodHostStorageApply = "catch.HostStorageApply"
)

type HostStorageMigrateServices string

const (
	HostStorageMigratePrompt HostStorageMigrateServices = ""
	HostStorageMigrateAll    HostStorageMigrateServices = "all"
	HostStorageMigrateNone   HostStorageMigrateServices = "none"
)

type HostStorageTarget struct {
	Value string `json:"value"`
	ZFS   bool   `json:"zfs"`
}

type HostStorageSetRequest struct {
	DataDir         *HostStorageTarget         `json:"dataDir,omitempty"`
	ServicesRoot    *HostStorageTarget         `json:"servicesRoot,omitempty"`
	MigrateServices HostStorageMigrateServices `json:"migrateServices,omitempty"`
	Yes             bool                       `json:"yes,omitempty"`
}

type HostStoragePlanRequest struct {
	Set HostStorageSetRequest `json:"set"`
}

type HostStorageApplyRequest struct {
	Plan HostStoragePlan `json:"plan"`
	Yes  bool            `json:"yes,omitempty"`
}

type HostStoragePlan struct {
	Current             HostStorageState          `json:"current"`
	Desired             HostStorageState          `json:"desired"`
	DataDirAction       HostStorageDataDirAction  `json:"dataDirAction"`
	ServicesAction      HostStorageServicesAction `json:"servicesAction"`
	CatchAction         HostStorageCatchAction    `json:"catchAction"`
	RepairAction        HostStorageRepairAction   `json:"repairAction,omitempty"`
	ZFSDatasetsToCreate []string                  `json:"zfsDatasetsToCreate,omitempty"`
	Warnings            []string                  `json:"warnings,omitempty"`
	RequiresRestart     bool                      `json:"requiresRestart,omitempty"`
}

type HostStorageRepairAction struct {
	References      int      `json:"references,omitempty"`
	DatabaseRefs    int      `json:"databaseRefs,omitempty"`
	SystemdRefs     int      `json:"systemdRefs,omitempty"`
	ArtifactRefs    int      `json:"artifactRefs,omitempty"`
	RegenerateUnits []string `json:"regenerateUnits,omitempty"`
	RestartServices []string `json:"restartServices,omitempty"`
	ValidationRoots []string `json:"validationRoots,omitempty"`
}

type HostStorageState struct {
	DataDir      string `json:"dataDir"`
	DataDirZFS   bool   `json:"dataDirZfs,omitempty"`
	ServicesRoot string `json:"servicesRoot"`
	ServicesZFS  bool   `json:"servicesZfs,omitempty"`
}

type HostStorageDataDirAction struct {
	Move bool   `json:"move"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

type HostStorageServicesAction struct {
	Mode             HostStorageMigrateServices `json:"mode"`
	From             string                     `json:"from,omitempty"`
	To               string                     `json:"to,omitempty"`
	AffectedServices []HostStorageServiceMove   `json:"affectedServices,omitempty"`
}

type HostStorageCatchAction struct {
	Move  bool   `json:"move,omitempty"`
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
	ToZFS string `json:"toZfs,omitempty"`
}

type HostStorageServiceMove struct {
	Name       string `json:"name"`
	From       string `json:"from"`
	To         string `json:"to"`
	ToZFS      string `json:"toZfs,omitempty"`
	WasRunning bool   `json:"wasRunning"`
}

type HostStorageApplyResult struct {
	MigratedServices []HostStorageServiceMove `json:"migratedServices,omitempty"`
	Restarted        bool                     `json:"restarted,omitempty"`
	RestartScheduled bool                     `json:"restartScheduled,omitempty"`
	Validation       HostStorageValidation    `json:"validation,omitempty"`
}

type HostStorageValidation struct {
	ActiveRefs   int `json:"activeRefs,omitempty"`
	DatabaseRefs int `json:"databaseRefs,omitempty"`
	SystemdRefs  int `json:"systemdRefs,omitempty"`
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
	Balloon      ServiceVMBalloon   `json:"balloon,omitempty"`
	DiskBytes    int64              `json:"diskBytes,omitempty"`
	DiskBackend  string             `json:"diskBackend,omitempty"`
	DiskPath     string             `json:"diskPath,omitempty"`
	SSH          *ServiceVMSSH      `json:"ssh,omitempty"`
	Console      *ServiceVMConsole  `json:"console,omitempty"`
	Networks     []ServiceVMNetwork `json:"networks,omitempty"`
	SetupState   string             `json:"setupState,omitempty"`
}

type ServiceVMBalloon struct {
	Mode       string `json:"mode,omitempty"`
	MinBytes   int64  `json:"minBytes,omitempty"`
	MinMemory  string `json:"minMemory,omitempty"`
	LastTarget int64  `json:"lastTargetBytes,omitempty"`
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
	Source    string `json:"source,omitempty"`
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
	SvcIP      string            `json:"svcIp,omitempty"`
	IPs        []ServiceIP       `json:"ips,omitempty"`
	RuntimeIPs []ServiceIP       `json:"runtimeIps,omitempty"`
	IPError    string            `json:"ipError,omitempty"`
	IPWarning  string            `json:"ipWarning,omitempty"`
	Ports      []ServicePort     `json:"ports,omitempty"`
	Macvlan    *ServiceMacvlan   `json:"macvlan,omitempty"`
	Tailscale  *ServiceTailscale `json:"tailscale,omitempty"`

	PortsPresent bool `json:"-"`
}

type serviceNetworkJSON struct {
	SvcIP      string            `json:"svcIp,omitempty"`
	IPs        []ServiceIP       `json:"ips,omitempty"`
	RuntimeIPs []ServiceIP       `json:"runtimeIps,omitempty"`
	IPError    string            `json:"ipError,omitempty"`
	IPWarning  string            `json:"ipWarning,omitempty"`
	Ports      []ServicePort     `json:"ports,omitempty"`
	Macvlan    *ServiceMacvlan   `json:"macvlan,omitempty"`
	Tailscale  *ServiceTailscale `json:"tailscale,omitempty"`
}

type serviceNetworkJSONWithPorts struct {
	SvcIP      string            `json:"svcIp,omitempty"`
	IPs        []ServiceIP       `json:"ips,omitempty"`
	RuntimeIPs []ServiceIP       `json:"runtimeIps,omitempty"`
	IPError    string            `json:"ipError,omitempty"`
	IPWarning  string            `json:"ipWarning,omitempty"`
	Ports      []ServicePort     `json:"ports"`
	Macvlan    *ServiceMacvlan   `json:"macvlan,omitempty"`
	Tailscale  *ServiceTailscale `json:"tailscale,omitempty"`
}

func (n ServiceNetwork) MarshalJSON() ([]byte, error) {
	if n.PortsPresent || len(n.Ports) != 0 {
		ports := n.Ports
		if ports == nil {
			ports = []ServicePort{}
		}
		return json.Marshal(serviceNetworkJSONWithPorts{
			SvcIP:      n.SvcIP,
			IPs:        n.IPs,
			RuntimeIPs: n.RuntimeIPs,
			IPError:    n.IPError,
			IPWarning:  n.IPWarning,
			Ports:      ports,
			Macvlan:    n.Macvlan,
			Tailscale:  n.Tailscale,
		})
	}
	return json.Marshal(serviceNetworkJSON{
		SvcIP:      n.SvcIP,
		IPs:        n.IPs,
		RuntimeIPs: n.RuntimeIPs,
		IPError:    n.IPError,
		IPWarning:  n.IPWarning,
		Macvlan:    n.Macvlan,
		Tailscale:  n.Tailscale,
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
		RuntimeIPs:   decoded.RuntimeIPs,
		IPError:      decoded.IPError,
		IPWarning:    decoded.IPWarning,
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
	Source    string `json:"source,omitempty"`
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
