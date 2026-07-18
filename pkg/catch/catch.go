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
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/dnet"
	"github.com/yeetrun/yeet/pkg/netns"
	"github.com/yeetrun/yeet/pkg/registry"
	"github.com/yeetrun/yeet/pkg/svc"
	"tailscale.com/client/local"
	"tailscale.com/client/tailscale/apitype"
	tsapi "tailscale.com/client/tailscale/v2"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/util/set"
)

const (
	// SystemService is the name of the system meta-service that manages the server.
	SystemService = "sys"
	// CatchService is the name of the self-service that manages the server.
	CatchService = "catch"
)

var zfsDestroyRetryDelay = 2 * time.Second
var zfsDestroyMaxAttempts = 6
var zfsDestroySettleFunc = settleZFSBlockDevicesForDestroy

var DockerStatusesUnknown = svc.DockerComposeStatus{}
var serverVMStatusFunc = func(name string) (svc.Status, error) {
	runner := &vmRunner{name: name}
	return runner.Status()
}

var installYeetNSService = netns.InstallYeetNSService
var installYeetDNSServiceForServer = installYeetDNSService
var reconcileDockerNetNSPortForwards = dnet.ReconcilePortForwards

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

	newDockerComposeService  func(sv db.ServiceView) (dockerNetNSReconciler, error)
	serviceRootDirFunc       func(string) (string, error)
	zfsRunner                zfsCommandRunner
	hostStorageMutationMu    sync.Mutex
	hostStorageMutationBlock error
	hostStorageRecovery      *hostStorageTransaction
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
	InstallHome          string
	InstallHost          string
	RootDir              string
	ServicesRoot         string
	TSNetHost            string
	MountsRoot           string
	InternalRegistryAddr string
	ExternalRegistryAddr string
	RegistryRoot         string
	ContainerdSocket     string
	RegistryStorage      registry.Storage
	LocalClient          *local.Client
	StatusFunc           func(ctx context.Context) (*ipnstate.Status, error)                          `json:"-"`
	WhoIsFunc            func(ctx context.Context, remoteAddr string) (*apitype.WhoIsResponse, error) `json:"-"`
	AuthorizeFunc        func(ctx context.Context, remoteAddr string) error                           `json:"-"`
}

// NewUnstartedServer creates a new Server instance with the provided
// configuration but does not start it.
func NewUnstartedServer(config *Config) *Server {
	s := &Server{
		cfg: *config,
	}
	if tx, err := findHostStorageStartupRecoveryForConfig(context.Background(), *config); err != nil {
		s.hostStorageMutationBlock = err
		s.hostStorageRecovery = tx
	}
	s.registry = s.newRegistry()
	s.newDockerComposeService = func(sv db.ServiceView) (dockerNetNSReconciler, error) {
		root := s.serviceRootFromView(sv)
		return svc.NewDockerComposeService(s.cfg.DB, sv, serviceDataDirForRoot(root), serviceRunDirForRoot(root))
	}
	return s
}

func findHostStorageStartupRecoveryForConfig(ctx context.Context, config Config) (*hostStorageTransaction, error) {
	for {
		tx, err := findHostStorageStartupRecovery(ctx, config.RootDir)
		if err == nil || tx == nil || tx.Phase != hostStoragePhaseSourceRestartPending ||
			!hostStorageConfigMatchesState(config, tx.Plan.Current) {
			return tx, err
		}
		if completeErr := advanceHostStorageTransactionState(tx, hostStoragePhaseRolledBack, hostStorageCatchAuthoritySource); completeErr != nil {
			return tx, errors.Join(err, fmt.Errorf("complete source catch restart handoff: %w", completeErr))
		}
	}
}

func hostStorageConfigMatchesState(config Config, state catchrpc.HostStorageState) bool {
	return hostStoragePathsEqual(config.RootDir, state.DataDir) &&
		hostStoragePathsEqual(config.ServicesRoot, state.ServicesRoot)
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
	s.waitGroup.Go(func() {
		s.runVMBalloonController(s.ctx)
	})
	if err := installYeetNSService(); err != nil {
		log.Fatalf("Failed to install bridge service: %v", err)
	}
	if err := installYeetDNSServiceForServer(s.cfg.RootDir); err != nil {
		log.Fatalf("Failed to install DNS service: %v", err)
	}
	if err := installDockerPrereqs(s); err != nil {
		log.Fatalf("Failed to install Docker prerequisites: %v", err)
	}
	s.waitGroup.Go(s.reconcileRuntimeState)
}

func (s *Server) reconcileRuntimeState() {
	logRuntimeReconcileError("tailscale DNS config reconciliation failed", s.reconcileTailscaleDNSConfigs(s.ctx))
	logRuntimeReconcileError("tailscale resolver isolation reconciliation failed", s.reconcileTailscaleResolverIsolation(s.ctx))
	logRuntimeReconcileError("netns reconciliation failed", s.reconcileNetNSBackedDockerServices(s.ctx))
	logRuntimeReconcileError("docker netns NAT reconciliation failed", reconcileDockerNetNSPortForwards(s.cfg.DB))
	logRuntimeReconcileError("VM network reconciliation failed", s.reconcileVMNetworks(s.ctx))
}

func logRuntimeReconcileError(message string, err error) {
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("%s: %v", message, err)
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

var errUnauthorized = fmt.Errorf("unauthorized connection")

func validateCatchNodeIdentity(serverTags []string) error {
	if len(serverTags) == 0 {
		return fmt.Errorf("%w: catch tsnet node must be tagged", errUnauthorized)
	}
	return nil
}

// verifyCaller checks the base caller identity. Operation-specific callers
// should use authorizeCaller with required yeet permissions.
func (s *Server) verifyCaller(ctx context.Context, remoteAddr string) error {
	return s.authorizeCaller(ctx, remoteAddr)
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
	root := s.serviceRootFromView(sv)
	service, err := svc.NewDockerComposeService(s.cfg.DB, sv, serviceDataDirForRoot(root), serviceRunDirForRoot(root))
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
	root := s.serviceRootFromView(sv)
	service, err := svc.NewSystemdService(s.cfg.DB, sv, serviceRunDirForRoot(root))
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
	// ServiceRoot is either an absolute filesystem root or, when ServiceRootZFS
	// is true, a ZFS dataset name to resolve on the catch host.
	ServiceRoot string
	// ServiceRootZFS treats ServiceRoot as a ZFS dataset name.
	ServiceRootZFS bool
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

func (s *Server) defaultServiceRootDir(sn string) string {
	return filepath.Join(s.cfg.ServicesRoot, sn)
}

func (s *Server) serviceRootFromView(sv db.ServiceView) string {
	if !sv.Valid() {
		return s.defaultServiceRootDir("")
	}
	if sv.ServiceRoot() != "" {
		return sv.ServiceRoot()
	}
	return s.defaultServiceRootDir(sv.Name())
}

// serviceRootDir returns the effective root directory for the given service
// name. Missing services use the legacy default location.
func (s *Server) serviceRootDir(sn string) (string, error) {
	if s.serviceRootDirFunc != nil {
		return s.serviceRootDirFunc(sn)
	}
	d, err := s.getDB()
	if err != nil {
		return "", err
	}
	sv, ok := d.Services().GetOk(sn)
	if !ok {
		return s.defaultServiceRootDir(sn), nil
	}
	return s.serviceRootFromView(sv), nil
}

func (s *Server) prepareServiceRootForInstall(sn, requested string, requestedZFS bool) (resolvedServiceRoot, error) {
	sv, err := s.serviceView(sn)
	if err != nil && !errors.Is(err, errServiceNotFound) {
		return resolvedServiceRoot{}, err
	}
	if requestedZFS {
		return s.prepareZFSServiceRootForInstall(sn, sv, requested)
	}
	return s.prepareFilesystemServiceRootForInstall(sn, sv, requested)
}

func (s *Server) prepareZFSServiceRootForInstall(sn string, sv db.ServiceView, requested string) (resolvedServiceRoot, error) {
	requestedDataset := strings.TrimSpace(requested)
	if requestedDataset == "" {
		return resolvedServiceRoot{}, fmt.Errorf("--service-root is required when --zfs is set")
	}
	if !sv.Valid() {
		return resolveZFSServiceRoot(context.Background(), s.zfsRunner, requestedDataset, zfsServiceRootTarget)
	}
	if sv.ServiceRootZFS() == requestedDataset {
		return resolveZFSServiceRoot(context.Background(), s.zfsRunner, requestedDataset, zfsServiceRootExisting)
	}
	return resolvedServiceRoot{}, fmt.Errorf(
		"service %q already uses service root %q; change it with: yeet service set %s --service-root=%s --zfs",
		sn,
		s.serviceRootFromView(sv),
		sn,
		requestedDataset,
	)
}

func (s *Server) prepareFilesystemServiceRootForInstall(sn string, sv db.ServiceView, requested string) (resolvedServiceRoot, error) {
	if sv.Valid() {
		effective := s.serviceRootFromView(sv)
		if requested == "" {
			return resolvedServiceRoot{Root: effective, Dataset: sv.ServiceRootZFS(), ZFS: sv.ServiceRootZFS() != ""}, nil
		}
		cleaned, err := cleanRequestedServiceRoot(requested)
		if err != nil {
			return resolvedServiceRoot{}, err
		}
		if sv.ServiceRootZFS() != "" {
			return resolvedServiceRoot{}, fmt.Errorf(
				"service %q already uses ZFS service root %q; change it with: yeet service set %s --service-root=%s",
				sn,
				sv.ServiceRootZFS(),
				sn,
				cleaned,
			)
		}
		if cleaned == filepath.Clean(effective) {
			return resolvedServiceRoot{Root: cleaned}, nil
		}
		return resolvedServiceRoot{}, fmt.Errorf(
			"service %q already uses service root %q; change it with: yeet service set %s --service-root=%s",
			sn,
			effective,
			sn,
			cleaned,
		)
	}
	if requested == "" {
		resolved, ok, err := resolveDefaultZFSServiceRoot(context.Background(), s.zfsRunner, s.cfg.ServicesRoot, sn)
		if err != nil {
			return resolvedServiceRoot{}, err
		}
		if ok {
			return resolved, nil
		}
		return resolvedServiceRoot{Root: s.defaultServiceRootDir(sn)}, nil
	}
	root, err := validateRequestedServiceRoot(requested)
	if err != nil {
		return resolvedServiceRoot{}, err
	}
	return resolvedServiceRoot{Root: root}, nil
}

func validateRequestedServiceRoot(root string) (string, error) {
	cleaned, err := cleanRequestedServiceRoot(root)
	if err != nil || cleaned == "" {
		return cleaned, err
	}
	parent := filepath.Dir(cleaned)
	parentInfo, err := os.Stat(parent)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("service root parent %q does not exist", parent)
		}
		return "", fmt.Errorf("failed to stat service root parent %q: %w", parent, err)
	}
	if !parentInfo.IsDir() {
		return "", fmt.Errorf("service root parent %q is not a directory", parent)
	}
	empty, err := rootIsMissingOrEmpty(cleaned)
	if err != nil {
		return "", err
	}
	if !empty {
		return "", fmt.Errorf("service root %q must be empty", cleaned)
	}
	return cleaned, nil
}

func cleanRequestedServiceRoot(root string) (string, error) {
	if root == "" {
		return "", nil
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("service root %q must be absolute", root)
	}
	return filepath.Clean(root), nil
}

func rootIsMissingOrEmpty(root string) (bool, error) {
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, fmt.Errorf("failed to stat service root %q: %w", root, err)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("service root %q is a file", root)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return false, fmt.Errorf("failed to read service root %q: %w", root, err)
	}
	if len(entries) == 0 {
		return true, nil
	}
	return rootIsRetrySafeServiceRootSkeleton(root, entries)
}

func rootIsRetrySafeServiceRootSkeleton(root string, entries []os.DirEntry) (bool, error) {
	allowed := map[string]struct{}{
		"bin":  {},
		"data": {},
		"env":  {},
		"run":  {},
	}
	if len(entries) != len(allowed) {
		return false, nil
	}
	for _, entry := range entries {
		if _, ok := allowed[entry.Name()]; !ok {
			return false, nil
		}
		if !entry.IsDir() {
			return false, nil
		}
		children, err := os.ReadDir(filepath.Join(root, entry.Name()))
		if err != nil {
			return false, fmt.Errorf("failed to read service root child %q: %w", filepath.Join(root, entry.Name()), err)
		}
		if len(children) != 0 {
			return false, nil
		}
		delete(allowed, entry.Name())
	}
	return len(allowed) == 0, nil
}

func (s *Server) serviceBinDir(sn string) string {
	return serviceBinDirForRoot(s.defaultServiceRootDir(sn))
}

func (s *Server) serviceRunDir(sn string) string {
	return serviceRunDirForRoot(s.defaultServiceRootDir(sn))
}

func (s *Server) catchRunnerPath() string {
	root, err := s.serviceRootDir(CatchService)
	if err != nil {
		root = s.defaultServiceRootDir(CatchService)
	}
	return filepath.Join(serviceRunDirForRoot(root), "catch")
}

func (s *Server) serviceDataDir(sn string) string {
	return serviceDataDirForRoot(s.defaultServiceRootDir(sn))
}

func (s *Server) serviceEnvDir(sn string) string {
	return serviceEnvDirForRoot(s.defaultServiceRootDir(sn))
}

func (s *Server) ensureDirs(sn, uname string) error {
	root, err := s.serviceRootDir(sn)
	if err != nil {
		return err
	}
	return ensureDirsForRoot(root, uname)
}

func ensureDirsForRoot(root, uname string) error {
	for _, dir := range serviceDirectoryPlan(root) {
		if err := ensureServiceDir(dir, uname); err != nil {
			return err
		}
	}
	return nil
}

func serviceDirectoryPlan(serviceRoot string) []string {
	return []string{
		filepath.Join(serviceRoot, "bin"),
		filepath.Join(serviceRoot, "data"),
		filepath.Join(serviceRoot, "env"),
		filepath.Join(serviceRoot, "run"),
	}
}

func serviceBinDirForRoot(root string) string {
	return filepath.Join(root, "bin")
}

func serviceRunDirForRoot(root string) string {
	return filepath.Join(root, "run")
}

func serviceDataDirForRoot(root string) string {
	return filepath.Join(root, "data")
}

func serviceEnvDirForRoot(root string) string {
	return filepath.Join(root, "env")
}

func ensureServiceDir(dir, uname string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create bin directory: %w", err)
	}
	if uname == "" || uname == "root" {
		return nil
	}
	return chownServiceDir(dir, uname)
}

func chownServiceDir(dir, uname string) error {
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
	return nil
}

var errNoServiceConfigured = fmt.Errorf("no service configured")

const dockerComposeOutdatedAllWorkerLimit = 8

type dockerComposeOutdatedServiceScan func(context.Context, string, svc.DockerOutdatedOptions) ([]svc.DockerOutdatedRow, error)

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
	allstatuses := make(map[string]svc.DockerComposeStatus)
	for _, sn := range serviceNamesByType(dv.AsStruct().Services, db.ServiceTypeDockerCompose) {
		statuses, err := s.dockerComposeStatusOrUnknown(sn)
		if err != nil {
			return nil, err
		}
		allstatuses[sn] = statuses
	}
	return allstatuses, nil
}

func (s *Server) DockerComposeOutdated(ctx context.Context, sn string, opts svc.DockerOutdatedOptions) ([]svc.DockerOutdatedRow, error) {
	service, err := s.dockerComposeService(sn)
	if err != nil {
		return nil, fmt.Errorf("failed to get service: %w", err)
	}
	return service.Outdated(ctx, opts)
}

func (s *Server) DockerComposeOutdatedAll(ctx context.Context) ([]svc.DockerOutdatedRow, error) {
	dv, err := s.getDB()
	if err != nil {
		return nil, fmt.Errorf("failed to get db: %v", err)
	}
	serviceNames := serviceNamesByType(dv.AsStruct().Services, db.ServiceTypeDockerCompose)
	return dockerComposeOutdatedAll(ctx, serviceNames, func(ctx context.Context, sn string, opts svc.DockerOutdatedOptions) ([]svc.DockerOutdatedRow, error) {
		return s.DockerComposeOutdated(ctx, sn, opts)
	})
}

func dockerComposeOutdatedAll(ctx context.Context, serviceNames []string, scan dockerComposeOutdatedServiceScan) ([]svc.DockerOutdatedRow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(serviceNames) == 0 {
		return nil, nil
	}

	workers := dockerComposeOutdatedAllWorkerLimit
	if workers < 1 {
		workers = 1
	}
	if workers > len(serviceNames) {
		workers = len(serviceNames)
	}

	jobs := make(chan string)
	results := make(chan []svc.DockerOutdatedRow, len(serviceNames))
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go dockerComposeOutdatedAllWorker(ctx, &wg, scan, jobs, results)
	}

	sendErr := dockerComposeOutdatedAllSendJobs(ctx, jobs, serviceNames)
	close(jobs)
	wg.Wait()
	close(results)

	if sendErr != nil {
		return nil, sendErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return dockerComposeOutdatedAllCollectRows(results), nil
}

func dockerComposeOutdatedAllWorker(ctx context.Context, wg *sync.WaitGroup, scan dockerComposeOutdatedServiceScan, jobs <-chan string, results chan<- []svc.DockerOutdatedRow) {
	defer wg.Done()
	for sn := range jobs {
		if err := ctx.Err(); err != nil {
			return
		}
		serviceRows, err := scan(ctx, sn, svc.DockerOutdatedOptions{})
		if err != nil {
			serviceRows = []svc.DockerOutdatedRow{{
				ServiceName: sn,
				Status:      svc.DockerOutdatedError,
				Reason:      err.Error(),
			}}
		}
		select {
		case results <- serviceRows:
		case <-ctx.Done():
			return
		}
	}
}

func dockerComposeOutdatedAllSendJobs(ctx context.Context, jobs chan<- string, serviceNames []string) error {
	for _, sn := range serviceNames {
		select {
		case jobs <- sn:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func dockerComposeOutdatedAllCollectRows(results <-chan []svc.DockerOutdatedRow) []svc.DockerOutdatedRow {
	rows := make([]svc.DockerOutdatedRow, 0)
	for serviceRows := range results {
		rows = append(rows, serviceRows...)
	}
	sortDockerOutdatedRows(rows)
	return rows
}

func (s *Server) dockerComposeStatusOrUnknown(sn string) (svc.DockerComposeStatus, error) {
	statuses, err := s.DockerComposeStatus(sn)
	if err == nil {
		return statuses, nil
	}
	if err == svc.ErrDockerStatusUnknown {
		return DockerStatusesUnknown, nil
	}
	return nil, err
}

func serviceNamesByType(services map[string]*db.Service, serviceType db.ServiceType) []string {
	names := make([]string, 0, len(services))
	for name, service := range services {
		if service.ServiceType == serviceType {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
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
	statuses := make(map[string]svc.Status)
	for _, name := range serviceNamesByType(dv.AsStruct().Services, db.ServiceTypeSystemd) {
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
	return s.isServiceTypeRunning(name, st)
}

func (s *Server) isServiceTypeRunning(name string, serviceType db.ServiceType) (bool, error) {
	switch serviceType {
	case db.ServiceTypeDockerCompose:
		return s.isDockerComposeServiceRunning(name)
	case db.ServiceTypeSystemd:
		return s.isSystemdServiceRunning(name)
	case db.ServiceTypeVM:
		return s.isVMServiceRunning(name)
	}
	return false, fmt.Errorf("unknown service type")
}

func (s *Server) isDockerComposeServiceRunning(name string) (bool, error) {
	sts, err := s.DockerComposeStatus(name)
	if err != nil {
		if err == svc.ErrDockerStatusUnknown {
			return false, nil
		}
		return false, err
	}
	return dockerComposeStatusRunning(sts), nil
}

func dockerComposeStatusRunning(statuses svc.DockerComposeStatus) bool {
	for _, status := range statuses {
		if status == svc.StatusRunning {
			return true
		}
	}
	return false
}

func (s *Server) isSystemdServiceRunning(name string) (bool, error) {
	st, err := s.SystemdStatus(name)
	if err != nil {
		return false, err
	}
	return st == svc.StatusRunning, nil
}

func (s *Server) isVMServiceRunning(name string) (bool, error) {
	st, err := serverVMStatusFunc(name)
	if err != nil {
		return false, err
	}
	return st == svc.StatusRunning, nil
}

type RemoveOptions struct {
	CleanData bool
	Trace     func(string, ...any)
}

var (
	vmRemovalNetworkRunner vmNetworkCommandRunner
	vmRemovalJailCleanup   = cleanupVMJail
)

// RemoveService removes the service from the database and attempts to clean up
// related files/devices. Cleanup warnings are reported separately from fatal
// errors. A requested ZFS clean-data destroy must succeed before the DB entry is
// removed, otherwise users can lose the normal recovery path for retrying
// cleanup.
func (s *Server) RemoveService(name string) (*RemoveReport, error) {
	return s.RemoveServiceWithOptions(name, RemoveOptions{})
}

// RemoveServiceWithOptions removes the service with optional cleanup behavior.
func (s *Server) RemoveServiceWithOptions(name string, opts RemoveOptions) (*RemoveReport, error) {
	doneRemove := removeTraceBlock(opts, "remove service")
	defer doneRemove()
	report := &RemoveReport{}
	doneRunning := removeTraceBlock(opts, "remove running warning")
	s.addRunningServiceWarning(report, name)
	doneRunning()
	doneStableID := removeTraceBlock(opts, "remove tailscale stable id")
	tsStableID := s.tailscaleStableIDForService(report, name)
	doneStableID()
	doneZFSRoot := removeTraceBlock(opts, "remove service zfs lookup")
	serviceRootZFS := s.serviceRootZFSForRemoval(report, name)
	doneZFSRoot()
	doneServiceRoot := removeTraceBlock(opts, "remove service root lookup")
	serviceRoot, err := s.serviceRootDir(name)
	doneServiceRoot()
	removeDirs := true
	if err != nil {
		report.addWarning(fmt.Errorf("failed to resolve service root for %q: %w", name, err))
		removeDirs = false
	}
	doneVMNetwork := removeTraceBlock(opts, "remove vm network")
	s.cleanupVMNetworkForRemoval(report, name)
	doneVMNetwork()
	doneVMJail := removeTraceBlock(opts, "remove vm jail")
	s.cleanupVMJailForRemoval(report, name)
	doneVMJail()
	if removeDirs && opts.CleanData {
		removeTrace(opts, "remove zfs dataset=%s", serviceRootZFS)
		doneZFS := removeTraceBlock(opts, "remove zfs destroy")
		if err := s.destroyServiceRootZFS(serviceRootZFS); err != nil {
			doneZFS()
			return report, err
		}
		doneZFS()
	}
	doneDB := removeTraceBlock(opts, "remove db")
	if err := s.removeServiceFromDB(name); err != nil {
		doneDB()
		return report, fmt.Errorf("failed to remove service from db: %w", err)
	}
	doneDB()
	doneEvent := removeTraceBlock(opts, "remove publish event")
	s.publishServiceDeleted(name)
	doneEvent()
	doneTailscale := removeTraceBlock(opts, "remove tailscale delete")
	s.deleteTailscaleDevice(report, tsStableID)
	doneTailscale()
	if removeDirs {
		doneDirs := removeTraceBlock(opts, "remove service dirs")
		s.removeServiceDirs(report, serviceRoot, opts.CleanData)
		doneDirs()
	}
	return report, nil
}

func (s *Server) cleanupVMNetworkForRemoval(report *RemoveReport, name string) {
	sv, err := s.serviceView(name)
	if err != nil {
		if !errors.Is(err, errServiceNotFound) {
			report.addWarning(fmt.Errorf("failed to resolve VM network for %q: %w", name, err))
		}
		return
	}
	service := sv.AsStruct()
	if service.ServiceType != db.ServiceTypeVM || service.VM == nil {
		return
	}
	plan := vmNetworkPlanFromDB(name, service.VM.Networks)
	if len(plan.Interfaces) == 0 {
		return
	}
	runner := vmRemovalNetworkRunner
	if runner == nil {
		runner = execVMNetworkCommand
	}
	if err := plan.ExecuteCleanup(runner); err != nil {
		report.addWarning(fmt.Errorf("failed to clean up VM network for %q: %w", name, err))
	}
}

func (s *Server) cleanupVMJailForRemoval(report *RemoveReport, name string) {
	sv, err := s.serviceView(name)
	if err != nil {
		if !errors.Is(err, errServiceNotFound) {
			report.addWarning(fmt.Errorf("failed to resolve VM jail for %q: %w", name, err))
		}
		return
	}
	service := sv.AsStruct()
	if service.ServiceType != db.ServiceTypeVM || service.VM == nil {
		return
	}
	rootFS := strings.TrimSpace(service.VM.Image.RootFS)
	if !filepath.IsAbs(rootFS) {
		return
	}
	plan := newVMJailCleanupPlan(name, filepath.Join(filepath.Dir(rootFS), "firecracker"), vmJailerBaseForDataRoot(s.cfg.RootDir), []string{
		service.VM.Sockets.APISocketPath,
		service.VM.Sockets.VsockSocketPath,
	})
	if err := vmRemovalJailCleanup(plan); err != nil {
		report.addWarning(fmt.Errorf("failed to clean up VM jail for %q: %w", name, err))
	}
}

func removeTrace(opts RemoveOptions, format string, args ...any) {
	if opts.Trace != nil {
		opts.Trace(format, args...)
	}
}

func removeTraceBlock(opts RemoveOptions, label string) func() {
	if opts.Trace == nil {
		return func() {}
	}
	start := time.Now()
	opts.Trace("%s start", label)
	return func() {
		opts.Trace("%s done duration=%s", label, time.Since(start).Round(time.Millisecond))
	}
}

func (s *Server) addRunningServiceWarning(report *RemoveReport, name string) {
	if s.serviceIsStagedOnlyEnvPlaceholder(name) {
		return
	}
	running, err := s.IsServiceRunning(name)
	if err != nil {
		report.addWarning(fmt.Errorf("failed to check if service %q is running: %w", name, err))
		return
	}
	if running {
		report.addWarning(fmt.Errorf("service %q is still running", name))
	}
}

func (s *Server) serviceIsStagedOnlyEnvPlaceholder(name string) bool {
	sv, err := s.serviceView(name)
	if err != nil {
		return false
	}
	service := sv.AsStruct()
	return serviceHasNoRuntimeDefinition(service) && artifactStoreHasOnlyStagedEnv(service.Artifacts)
}

func serviceHasNoRuntimeDefinition(service *db.Service) bool {
	if service.ServiceType != "" || service.Generation != 0 || service.LatestGeneration != 0 {
		return false
	}
	if len(service.Publish) != 0 || service.SvcNetwork != nil || service.Macvlan != nil || service.TSNet != nil || service.VM != nil {
		return false
	}
	return true
}

func artifactStoreHasOnlyStagedEnv(artifacts db.ArtifactStore) bool {
	if len(artifacts) != 1 {
		return false
	}
	artifact, ok := artifacts[db.ArtifactEnvFile]
	if !ok {
		return false
	}
	return artifactHasOnlyStagedRef(artifact)
}

func artifactHasOnlyStagedRef(artifact *db.Artifact) bool {
	if artifact == nil || len(artifact.Refs) != 1 {
		return false
	}
	_, ok := artifact.Refs[db.ArtifactRef("staged")]
	return ok
}

func (s *Server) tailscaleStableIDForService(report *RemoveReport, name string) string {
	sv, err := s.serviceView(name)
	if err == nil {
		return tailscaleStableIDForRemoval(sv)
	}
	if !errors.Is(err, errServiceNotFound) {
		report.addWarning(fmt.Errorf("failed to load service view for %q: %w", name, err))
	}
	return ""
}

func tailscaleStableIDForRemoval(sv db.ServiceView) string {
	tsnet := sv.TSNet()
	if !tsnet.Valid() || tsnet.StableID().IsZero() {
		return ""
	}
	return string(tsnet.StableID())
}

func (s *Server) serviceRootZFSForRemoval(report *RemoveReport, name string) string {
	sv, err := s.serviceView(name)
	if err == nil {
		return sv.ServiceRootZFS()
	}
	if !errors.Is(err, errServiceNotFound) {
		report.addWarning(fmt.Errorf("failed to load service ZFS root for %q: %w", name, err))
	}
	return ""
}

func (s *Server) removeServiceFromDB(name string) error {
	_, err := s.cfg.DB.MutateData(func(d *db.Data) error {
		delete(d.Services, name)
		return nil
	})
	return err
}

func (s *Server) publishServiceDeleted(name string) {
	s.PublishEvent(Event{
		Type:        EventTypeServiceDeleted,
		ServiceName: name,
	})
}

func (s *Server) deleteTailscaleDevice(report *RemoveReport, tsStableID string) {
	if tsStableID == "" {
		return
	}
	c, err := tsClient(s.ctx)
	if err != nil {
		report.addWarning(fmt.Errorf("failed to get tailscale client: %w", err))
		return
	}
	if err := c.Devices().Delete(s.ctx, tsStableID); err != nil {
		if tsapi.IsNotFound(err) {
			log.Printf("tailscale device not found: %v", err)
			return
		}
		report.addWarning(fmt.Errorf("failed to delete tailscale device: %w", err))
	}
}

func (s *Server) destroyServiceRootZFS(dataset string) error {
	if strings.TrimSpace(dataset) == "" {
		return nil
	}
	runner := s.zfsRunner
	if runner == nil {
		runner = runZFSCommand
	}
	ctx := context.Background()
	var lastErr error
	attempts := zfsDestroyMaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	for attempt := 0; attempt < attempts; attempt++ {
		_, stderr, err := runner(ctx, "destroy", "-R", dataset)
		if err == nil {
			return nil
		}
		if zfsDestroyDatasetMissing(stderr) {
			return nil
		}
		lastErr = formatZFSCommandError("zfs destroy -R "+dataset, stderr, err)
		if !zfsDestroyDatasetBusy(stderr) {
			break
		}
		if zfsDestroySettleFunc != nil {
			zfsDestroySettleFunc(ctx)
		}
		if zfsDestroyRetryDelay > 0 {
			time.Sleep(zfsDestroyRetryDelay)
		}
	}
	return lastErr
}

func zfsDestroyDatasetBusy(stderr string) bool {
	return strings.Contains(strings.ToLower(stderr), "dataset is busy")
}

func zfsDestroyDatasetMissing(stderr string) bool {
	return strings.Contains(strings.ToLower(stderr), "dataset does not exist")
}

func settleZFSBlockDevicesForDestroy(ctx context.Context) {
	cmd := exec.CommandContext(ctx, "udevadm", "settle", "--timeout=10")
	_ = cmd.Run()
}

func (s *Server) removeServiceDirs(report *RemoveReport, serviceRoot string, cleanData bool) {
	dirs, err := filepath.Glob(filepath.Join(serviceRoot, "*"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		report.addWarning(fmt.Errorf("failed to list service directories: %w", err))
		return
	}
	for _, dir := range serviceChildDirsToRemove(dirs, cleanData) {
		log.Printf("removing service directory: %v", dir)
		if err := os.RemoveAll(dir); err != nil {
			report.addWarning(fmt.Errorf("failed to remove service directory %s: %w", dir, err))
		}
	}
	if cleanData {
		log.Printf("removing service root directory: %v", serviceRoot)
		if err := os.Remove(serviceRoot); err != nil && !errors.Is(err, os.ErrNotExist) {
			report.addWarning(fmt.Errorf("failed to remove service root directory %s: %w", serviceRoot, err))
		}
	}
}

func serviceChildDirsToRemove(dirs []string, cleanData bool) []string {
	filtered := dirs[:0]
	for _, dir := range dirs {
		if cleanData || filepath.Base(dir) != "data" {
			filtered = append(filtered, dir)
		}
	}
	return filtered
}

type RemoveReport struct {
	Warnings []error
}

func (r *RemoveReport) addWarning(err error) {
	if err != nil {
		r.Warnings = append(r.Warnings, err)
	}
}
