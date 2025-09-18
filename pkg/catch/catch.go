// Copyright 2025 AUTHORS
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package catch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/netns"
	"github.com/yeetrun/yeet/pkg/svc"
	"golang.org/x/crypto/ssh"
	"tailscale.com/client/tailscale"
	gssh "tailscale.com/tempfork/gliderlabs/ssh"
	"tailscale.com/util/set"
)

const (
	// SystemService is the name of the system meta-service that manages the server.
	SystemService = "sys"
	// CatchService is the name of the self-service that manages the server.
	CatchService = "catch"
)

var DockerStatusesUnknown = svc.DockerComposeStatus{}

// Server is an SSH server that handles exec commands and SFTP subsystem
// requests. It listens on a specified address and uses the provided SSH server
// configuration. The server can be configured with a CmdHandlerFunc to handle
// exec commands and a PutHandlerFunc to handle SFTP PUT requests.
type Server struct {
	cfg       Config
	registry  *containerRegistry
	waitGroup sync.WaitGroup

	ctx    context.Context
	cancel context.CancelFunc

	eventListeners struct {
		mu sync.Mutex
		s  set.HandleSet[*EventListener]
	}

	serviceStatus struct {
		mu sync.Mutex
		m  map[string]map[string]ComponentStatus // serviceName -> componentName -> ComponentStatus
	}
}

type EventListener struct {
	ch     chan<- Event
	filter func(Event) bool
}

type EventType string

const (
	EventTypeUnknown              EventType = "Unknown"
	EventTypeHeartbeat            EventType = "Heartbeat"
	EventTypeServiceStatusChanged EventType = "ServiceStatusChanged"
	EventTypeServiceDeleted       EventType = "ServiceDeleted"
	EventTypeServiceCreated       EventType = "ServiceCreated"
	EventTypeServiceConfigChanged EventType = "ServiceConfigChanged"
	EventTypeServiceConfigStaged  EventType = "ServiceConfigStaged"
)

type EventData struct {
	Data any
}

// MarshalJSON returns m as the JSON encoding of m.
func (m EventData) MarshalJSON() ([]byte, error) {
	if m.Data == nil {
		return []byte("null"), nil
	}
	return json.Marshal(m.Data)
}

type Event struct {
	// Time is the time the event was created in milliseconds since the epoch.
	Time        int64     `json:"time"`
	ServiceName string    `json:"serviceName"`
	Type        EventType `json:"type"`
	Data        EventData `json:"data,omitempty"`
}

func (s *Server) PublishEvent(event Event) {
	event.Time = time.Now().UnixMilli()
	els := &s.eventListeners
	els.mu.Lock()
	defer els.mu.Unlock()
	for _, el := range els.s {
		if el.filter != nil && !el.filter(event) {
			continue
		}
		el.ch <- event
	}
}

func (s *Server) AddEventListener(ch chan<- Event, filter func(Event) bool) set.Handle {
	els := &s.eventListeners
	els.mu.Lock()
	defer els.mu.Unlock()
	return els.s.Add(&EventListener{ch: ch, filter: filter})
}

func (s *Server) RemoveEventListener(h set.Handle) {
	els := &s.eventListeners
	els.mu.Lock()
	defer els.mu.Unlock()
	delete(els.s, h)
}

// Config consists of an `Address` (ip:port) and an `SSHConfig` that is used to
// configure the SSH server.
type Config struct {
	Signer               ssh.Signer
	DB                   *db.Store
	DefaultUser          string
	RootDir              string
	ServicesRoot         string
	MountsRoot           string
	InternalRegistryAddr string
	ExternalRegistryAddr string
	RegistryRoot         string
	LocalClient          *tailscale.LocalClient
}

// NewUnstartedServer creates a new Server instance with the provided
// configuration but does not start it.
func NewUnstartedServer(config *Config) *Server {
	s := &Server{
		cfg: *config,
	}
	s.registry = s.newRegistry()
	return s
}

// NewServer creates a new Server instance with the provided configuration.
func NewServer(config *Config) *Server {
	s := NewUnstartedServer(config)
	s.Start()
	return s
}

// Start starts the server. It panics if the server is already started.
func (s *Server) Start() {
	if s.cancel != nil {
		panic("server already started")
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.waitGroup.Go(s.monitorSystemd)
	s.waitGroup.Go(s.monitorDocker)
	s.waitGroup.Go(s.heartbeat)
	if err := netns.InstallYeetNSService(); err != nil {
		log.Fatalf("Failed to install bridge service: %v", err)
	}
}

func (s *Server) Shutdown() {
	s.cancel()
	s.waitGroup.Wait()
}

func (s *Server) heartbeat() {
	ctx := s.ctx
	// Publish a heartbeat event every second.
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.PublishEvent(Event{
				Type:        EventTypeHeartbeat,
				ServiceName: "sys",
			})
		}
	}
}

// ServeSSH starts the server and listens for incoming connections. It blocks
// until an error occurs.
func (s *Server) ServeSSH(listener net.Listener) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return fmt.Errorf("failed to accept incoming connection: %w", err)
		}
		go s.handleSSHConnection(conn)
	}
}

func (s *Server) ServeInternalRegistry(listener net.Listener) error {
	return http.Serve(listener, s.registry)
}

func overlaps(a, b []string) bool {
	for _, x := range a {
		if slices.Contains(b, x) {
			return true
		}
	}
	return false
}

var errUnauthorized = fmt.Errorf("unauthorized connection")

// verifyCaller checks if the caller is authorized to connect to the server.
//
// - If the server is tagged and the caller is tagged, it checks if the tags
// overlap.
// - If the server is tagged and the caller is not tagged, it allows the
// connection.
// - If the server is not tagged, it checks if the caller is the same user as the
// server.
func (s *Server) verifyCaller(ctx context.Context, remoteAddr string) error {
	lc := s.cfg.LocalClient
	st, err := lc.StatusWithoutPeers(ctx)
	if err != nil {
		return fmt.Errorf("failed to get local client status: %v", err)
	}
	who, err := lc.WhoIs(ctx, remoteAddr)
	if err != nil {
		return fmt.Errorf("failed to get whois: %v", err)
	}
	if who.Node.IsTagged() {
		if st.Self.IsTagged() && overlaps(who.Node.Tags, st.Self.Tags.AsSlice()) {
			return nil
		}
		return errUnauthorized
	}
	if st.Self.IsTagged() {
		return nil
	}
	if st.Self.UserID == who.Node.User {
		return nil
	}
	return errUnauthorized
}

// handleSSHConnection should be called in a goroutine to handle an incoming SSH
// connection. It will accept the connection and handle incoming channels.
func (s *Server) handleSSHConnection(nConn net.Conn) {
	var spac ssh.ServerPreAuthConn
	ss := &gssh.Server{
		Version: "catch",
		ServerConfigCallback: func(ctx gssh.Context) *ssh.ServerConfig {
			return &ssh.ServerConfig{
				NoClientAuth: true,
				PreAuthConnCallback: func(c ssh.ServerPreAuthConn) {
					spac = c
				},
			}
		},
		HostSigners: []gssh.Signer{s.cfg.Signer},

		NoClientAuthHandler: func(ctx gssh.Context) error {
			if err := s.verifyCaller(ctx, ctx.RemoteAddr().String()); err != nil {
				spac.SendAuthBanner("This machine is not authorized.\r\n")
				return fmt.Errorf("unauthorized connection: %v", err)
			}
			return nil
		},
		Handler: s.handleSession,
		SubsystemHandlers: map[string]gssh.SubsystemHandler{
			"sftp": s.handleSession,
		},
		ChannelHandlers: map[string]gssh.ChannelHandler{},
		RequestHandlers: map[string]gssh.RequestHandler{},
	}
	defer ss.Close()
	for k, v := range gssh.DefaultRequestHandlers {
		ss.RequestHandlers[k] = v
	}
	for k, v := range gssh.DefaultChannelHandlers {
		ss.ChannelHandlers[k] = v
	}
	for k, v := range gssh.DefaultSubsystemHandlers {
		ss.SubsystemHandlers[k] = v
	}

	ss.HandleConn(nConn)
}

type noCloseSession struct {
	gssh.Session
}

func (n noCloseSession) Close() error {
	return nil
}

func (s *Server) handleSession(session gssh.Session) {
	if session.Subsystem() == "sftp" {
		if err := newSFTPHandler(s, session).serve(); err != nil {
			log.Printf("SFTP server error: %v", err)
		}
		return
	}
	s.SSHHandler(session)
}

func (s *Server) dockerComposeService(sn string) (*svc.DockerComposeService, error) {
	d, err := s.getDB()
	if err != nil {
		return nil, err
	}
	sv, ok := d.Services().GetOk(sn)
	if !ok {
		return nil, errServiceNotFound
	}
	service, err := svc.NewDockerComposeService(s.cfg.DB, sv, s.cfg.InternalRegistryAddr, d.AsStruct().Images, s.serviceDataDir(sn), s.serviceRunDir(sn))
	if err != nil {
		return nil, fmt.Errorf("failed to load service: %v", err)
	}
	return service, nil
}

// systemdService returns the service and its configuration for the given service name.
func (s *Server) systemdService(sn string) (*svc.SystemdService, error) {
	sv, err := s.serviceView(sn)
	if err != nil {
		return nil, fmt.Errorf("failed to get service view: %v", err)
	}
	service, err := svc.NewSystemdService(s.cfg.DB, sv, s.serviceRunDir(sn))
	if err != nil {
		return nil, fmt.Errorf("failed to load service: %v", err)
	}
	return service, nil
}

func (s *Server) getDB() (*db.DataView, error) {
	dv, err := s.cfg.DB.Get()
	if err != nil {
		return nil, fmt.Errorf("failed to get data: %v", err)
	}
	if !dv.Valid() {
		return nil, fmt.Errorf("db is invalid")
	}
	return &dv, nil
}

var errServiceNotFound = fmt.Errorf("service not found")

func (s *Server) serviceView(sn string) (db.ServiceView, error) {
	d, err := s.getDB()
	if err != nil {
		return db.ServiceView{}, err
	}
	sv, ok := d.Services().GetOk(sn)
	if !ok {
		return db.ServiceView{}, errServiceNotFound
	}
	return sv, nil
}

func (s *Server) serviceAndUser(conn gssh.Session) (service, user string, _ error) {
	if conn.User() == "" {
		return "", "", fmt.Errorf("empty user")
	}
	// The user@service syntax is used to specify the user and service name.
	user, service, ok := strings.Cut(conn.User(), "@")
	if !ok {
		// In this case the user is the service name as in `service@host`.
		return conn.User(), "", nil
	}
	if strings.Contains(service, "@") {
		return "", "", fmt.Errorf("invalid user: %q", conn.User())
	}
	return service, user, nil
}

// InstallerCfg is the configuration for installing a service.
type InstallerCfg struct {
	ServiceName string
	User        string
	// Printer is a function to print messages to the client.
	Printer func(string, ...any) `json:"-"`

	// ClientOut is the writer to send messages to stdout on the client.
	ClientOut io.Writer `json:"-"`

	// Timer, if set, specifies that the service should be installed as a timer service.
	Timer *svc.TimerConfig `json:"-"`

	// SSHSessionCloser is an io.Closer that closes the SSH session.
	SSHSessionCloser io.Closer `json:"-"`
}

// serviceRootDir returns the root directory for the given service name.
func (s *Server) serviceRootDir(sn string) string {
	return filepath.Join(s.cfg.ServicesRoot, sn)
}

func (s *Server) serviceBinDir(sn string) string {
	return filepath.Join(s.serviceRootDir(sn), "bin")
}

func (s *Server) serviceRunDir(sn string) string {
	return filepath.Join(s.serviceRootDir(sn), "run")
}

func (s *Server) serviceDataDir(sn string) string {
	return filepath.Join(s.serviceRootDir(sn), "data")
}

func (s *Server) serviceEnvDir(sn string) string {
	return filepath.Join(s.serviceRootDir(sn), "env")
}

func (s *Server) ensureDirs(sn, uname string) error {
	// Ensure bin and data directories exist.
	for _, dir := range []string{
		s.serviceBinDir(sn),
		s.serviceDataDir(sn),
		s.serviceEnvDir(sn),
		s.serviceRunDir(sn),
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create bin directory: %w", err)
		}
		if uname != "" && uname != "root" {
			u, err := user.Lookup(uname)
			if err != nil {
				return fmt.Errorf("failed to lookup user: %w", err)
			}
			uid, err := strconv.Atoi(u.Uid)
			if err != nil {
				return fmt.Errorf("failed to convert uid to int: %w", err)
			}
			gid, err := strconv.Atoi(u.Gid)
			if err != nil {
				return fmt.Errorf("failed to convert gid to int: %w", err)
			}
			if err := os.Chown(dir, uid, gid); err != nil {
				return fmt.Errorf("failed to chown directory: %w", err)
			}
		}
	}
	return nil
}

var errNoServiceConfigured = fmt.Errorf("no service configured")

// serviceType returns the type of service for the given service name.
func (s *Server) serviceType(sn string) (db.ServiceType, error) {
	sv, err := s.serviceView(sn)
	if err != nil {
		return "", err
	}
	return sv.ServiceType(), nil
}

// DockerComposeStatus returns the statuses of the containers for the given service.
func (s *Server) DockerComposeStatus(ns string) (svc.DockerComposeStatus, error) {
	service, err := s.dockerComposeService(ns)
	if err != nil {
		return nil, fmt.Errorf("failed to get service: %w", err)
	}
	return service.Statuses()
}

// DockerComposeStatuses returns the status of all Docker services. The keys are the
// service names and the values are the statuses. Possible statuses are
// svc.StatusRunning, svc.StatusStopped, and svc.StatusUnknown.
func (s *Server) DockerComposeStatuses() (map[string]svc.DockerComposeStatus, error) {
	dv, err := s.getDB()
	if err != nil {
		return nil, fmt.Errorf("failed to get db: %v", err)
	}
	d := dv.AsStruct()
	allstatuses := make(map[string]svc.DockerComposeStatus)
	for sn := range d.Services {
		stype, err := s.serviceType(sn)
		if err != nil {
			if errors.Is(err, errNoServiceConfigured) {
				continue
			}
			log.Printf("failed to get service type: %v", err)
			allstatuses[sn] = DockerStatusesUnknown
			continue
		}
		if stype != db.ServiceTypeDockerCompose {
			continue
		}
		statuses, err := s.DockerComposeStatus(sn)
		if err != nil && err == svc.ErrDockerStatusUnknown {
			allstatuses[sn] = DockerStatusesUnknown
		} else if err != nil {
			return nil, err
		}
		allstatuses[sn] = statuses
	}
	return allstatuses, nil
}

// SystemdStatus returns the status of the service with the given name.
// Possible statuses are svc.StatusRunning, svc.StatusStopped, and svc.StatusUnknown.
func (s *Server) SystemdStatus(ns string) (svc.Status, error) {
	service, err := s.systemdService(ns)
	if err != nil {
		return svc.StatusUnknown, fmt.Errorf("failed to get service: %w", err)
	}
	return service.Status()
}

// SystemdStatuses returns the status of all systemd services. The keys are the
// service names and the values are the statuses. Possible statuses are
// svc.StatusRunning, svc.StatusStopped, and svc.StatusUnknown.
func (s *Server) SystemdStatuses() (map[string]svc.Status, error) {
	dv, err := s.getDB()
	if err != nil {
		return nil, fmt.Errorf("failed to get db: %w", err)
	}
	d := dv.AsStruct()
	statuses := make(map[string]svc.Status)
	for name := range d.Services {
		stype, err := s.serviceType(name)
		if err != nil {
			log.Printf("failed to get service type: %v", err)
			statuses[name] = svc.StatusUnknown
			continue
		}
		if stype != db.ServiceTypeSystemd {
			continue
		}
		status, err := s.SystemdStatus(name)
		if err != nil {
			statuses[name] = svc.StatusUnknown
		} else {
			statuses[name] = status
		}
	}
	return statuses, nil
}

// IsServiceRunning returns whether the service with the given name is running.
// If this is a Docker service, it will return true if any of the containers are
// running.
func (s *Server) IsServiceRunning(name string) (bool, error) {
	st, err := s.serviceType(name)
	if err != nil {
		if errors.Is(err, errNoServiceConfigured) {
			return false, nil
		}
		return false, fmt.Errorf("failed to get service type: %w", err)
	}
	switch st {
	case db.ServiceTypeDockerCompose:
		sts, err := s.DockerComposeStatus(name)
		if err != nil {
			return false, err
		}
		for _, status := range sts {
			if status == svc.StatusRunning {
				return true, nil
			}
		}
		return false, nil // No containers are running.
	case db.ServiceTypeSystemd:
		st, err := s.SystemdStatus(name)
		if err != nil {
			return false, err
		}
		return st == svc.StatusRunning, nil
	}
	return false, fmt.Errorf("unknown service type")
}

// RemoveService checks if service is stopped, removes the service directory
// from the filesystem, and removes the service from the database.
func (s *Server) RemoveService(name string) error {
	// Check if service is still running, and if so, return an error. Do not
	// remove the service if it is still running.
	if running, err := s.IsServiceRunning(name); err != nil {
		log.Printf("failed to check if service is running: %v", err)
	} else if running {
		return fmt.Errorf("service is not stopped")
	}

	dirs, err := filepath.Glob(filepath.Join(s.cfg.ServicesRoot, name, "*"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to list service directories: %w", err)
	}
	for _, dir := range dirs {
		if filepath.Base(dir) == "data" {
			// Skip data directory.
			continue
		}
		log.Printf("removing service directory: %v", dir)
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("failed to remove service directory: %w", err)
		}
	}
	if sv, err := s.serviceView(name); err == nil {
		if sv.TSNet().Valid() && !sv.TSNet().StableID().IsZero() {
			c, err := tsClient(s.ctx)
			if err != nil {
				return fmt.Errorf("failed to get tailscale client: %w", err)
			}
			if err := c.DeleteDevice(s.ctx, string(sv.TSNet().StableID())); err != nil {
				var errResp tailscale.ErrResponse
				if errors.As(err, &errResp) && errResp.Status == http.StatusNotFound {
					log.Printf("tailscale device not found: %v", errResp)
				} else {
					return fmt.Errorf("failed to delete tailscale device: %w", err)
				}
			}
		}
	}

	_, err = s.cfg.DB.MutateData(func(d *db.Data) error {
		delete(d.Services, name)
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to remove service from db: %w", err)
	}
	s.PublishEvent(Event{
		Type:        EventTypeServiceDeleted,
		ServiceName: name,
	})
	return nil
}
