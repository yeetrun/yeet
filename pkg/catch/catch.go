// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

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
	"sync"
	"time"

	"github.com/shayne/yeet/pkg/db"
	"github.com/shayne/yeet/pkg/netns"
	"github.com/shayne/yeet/pkg/registry"
	"github.com/shayne/yeet/pkg/svc"
	"tailscale.com/client/tailscale"
	"tailscale.com/util/set"
)

const (
	// SystemService is the name of the system meta-service that manages the server.
	SystemService = "sys"
	// CatchService is the name of the self-service that manages the server.
	CatchService = "catch"
)

var DockerStatusesUnknown = svc.DockerComposeStatus{}

// Server hosts the RPC handlers that manage services and exec commands.
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

// Config contains the server dependencies and filesystem paths.
type Config struct {
	DB                   *db.Store
	DefaultUser          string
	InstallUser          string
	InstallHost          string
	RootDir              string
	ServicesRoot         string
	MountsRoot           string
	InternalRegistryAddr string
	ExternalRegistryAddr string
	RegistryRoot         string
	ContainerdSocket     string
	RegistryStorage      registry.Storage
	LocalClient          *tailscale.LocalClient
	AuthorizeFunc        func(ctx context.Context, remoteAddr string) error `json:"-"`
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
				ServiceName: SystemService,
			})
		}
	}
}

func (s *Server) ServeInternalRegistry(listener net.Listener) error {
	return http.Serve(listener, s.registry)
}

func (s *Server) RegistryHandler() http.Handler {
	return s.registry
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
	if s.cfg.AuthorizeFunc != nil {
		return s.cfg.AuthorizeFunc(ctx, remoteAddr)
	}
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

func (s *Server) dockerComposeService(sn string) (*svc.DockerComposeService, error) {
	d, err := s.getDB()
	if err != nil {
		return nil, err
	}
	sv, ok := d.Services().GetOk(sn)
	if !ok {
		return nil, errServiceNotFound
	}
	service, err := svc.NewDockerComposeService(s.cfg.DB, sv, s.serviceDataDir(sn), s.serviceRunDir(sn))
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

// InstallerCfg is the configuration for installing a service.
type InstallerCfg struct {
	ServiceName string
	User        string
	// Pull forces docker compose services to pull images on install.
	Pull bool
	// Printer is a function to print messages to the client.
	Printer func(string, ...any) `json:"-"`

	// ClientOut is the writer to send messages to stdout on the client.
	ClientOut io.Writer `json:"-"`

	// UI is used to render user-facing install progress.
	UI ProgressUI `json:"-"`

	// Timer, if set, specifies that the service should be installed as a timer service.
	Timer *svc.TimerConfig `json:"-"`

	// ClientCloser is an io.Closer that closes the client connection.
	ClientCloser io.Closer `json:"-"`
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
			if err == svc.ErrDockerStatusUnknown {
				return false, nil
			}
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

// RemoveService removes the service from the database and attempts to clean up
// related files/devices. It always removes the DB entry if possible, returning
// cleanup warnings separately from fatal errors.
func (s *Server) RemoveService(name string) (*RemoveReport, error) {
	report := &RemoveReport{}
	var tsStableID string

	if running, err := s.IsServiceRunning(name); err != nil {
		report.addWarning(fmt.Errorf("failed to check if service %q is running: %w", name, err))
	} else if running {
		report.addWarning(fmt.Errorf("service %q is still running", name))
	}

	if sv, err := s.serviceView(name); err == nil {
		if sv.TSNet().Valid() && !sv.TSNet().StableID().IsZero() {
			tsStableID = string(sv.TSNet().StableID())
		}
	} else if !errors.Is(err, errServiceNotFound) {
		report.addWarning(fmt.Errorf("failed to load service view for %q: %w", name, err))
	}

	if _, err := s.cfg.DB.MutateData(func(d *db.Data) error {
		delete(d.Services, name)
		return nil
	}); err != nil {
		return report, fmt.Errorf("failed to remove service from db: %w", err)
	}
	s.PublishEvent(Event{
		Type:        EventTypeServiceDeleted,
		ServiceName: name,
	})

	if tsStableID != "" {
		c, err := tsClient(s.ctx)
		if err != nil {
			report.addWarning(fmt.Errorf("failed to get tailscale client: %w", err))
		} else if err := c.DeleteDevice(s.ctx, tsStableID); err != nil {
			var errResp tailscale.ErrResponse
			if errors.As(err, &errResp) && errResp.Status == http.StatusNotFound {
				log.Printf("tailscale device not found: %v", errResp)
			} else {
				report.addWarning(fmt.Errorf("failed to delete tailscale device: %w", err))
			}
		}
	}

	dirs, err := filepath.Glob(filepath.Join(s.cfg.ServicesRoot, name, "*"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		report.addWarning(fmt.Errorf("failed to list service directories: %w", err))
		return report, nil
	}
	for _, dir := range dirs {
		if filepath.Base(dir) == "data" {
			// Skip data directory.
			continue
		}
		log.Printf("removing service directory: %v", dir)
		if err := os.RemoveAll(dir); err != nil {
			report.addWarning(fmt.Errorf("failed to remove service directory %s: %w", dir, err))
		}
	}
	return report, nil
}

type RemoveReport struct {
	Warnings []error
}

func (r *RemoveReport) addWarning(err error) {
	if err != nil {
		r.Warnings = append(r.Warnings, err)
	}
}
