// Package db provides a simple JSON file-backed database.
package db

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"os"
	"path/filepath"
	"sync"

	"github.com/yeetrun/yeet/pkg/fileutil"
	"tailscale.com/tailcfg"
	"tailscale.com/util/mak"
)

var (
	renameDBFile    = os.Rename
	syncDBFile      = func(f *os.File) error { return f.Sync() }
	syncDBDirectory = func(f *os.File) error { return f.Sync() }
)

//go:generate go run tailscale.com/cmd/viewer -type=Data,Service,ServiceIdentity,SnapshotPolicy,Volume,ImageRepo,Artifact,DockerNetwork,DockerEndpoint,TailscaleNetwork,EndpointPort,VMConfig,VMImageConfig,VMDiskConfig,VMNetworkConfig,VMSSHConfig,VMConsoleConfig,VMSocketConfig,VMBalloonConfig,VMHostConfig,ISOPool,ISOAllocation,ISOComponent --copyright=false

// Data is the full JSON structure of the database.
type Data struct {
	// DataVersion is the version of the data format. This is used to determine
	// how to parse the data.
	DataVersion int `json:",omitempty"`

	SnapshotDefaults *SnapshotPolicy `json:",omitempty"`
	VMHost           *VMHostConfig   `json:",omitempty"`
	ISOPool          *ISOPool        `json:",omitempty"`

	Services map[string]*Service

	Images map[ImageRepoName]*ImageRepo

	Volumes map[string]*Volume

	DockerNetworks map[string]*DockerNetwork
}

type ISOPool struct {
	Prefix              netip.Prefix
	Source              string
	AllocatorVersion    int
	PolicyVersion       int
	AggregateRouteState string `json:",omitempty"`
	LastConflict        string `json:",omitempty"`
}

type ISOComponent struct {
	Address netip.Addr
	State   string
}

type ISOAllocation struct {
	Kind              string
	State             string
	Link              netip.Prefix
	HostIP            netip.Addr
	PeerIP            netip.Addr
	Project           netip.Prefix `json:",omitempty"`
	Gateway           netip.Addr   `json:",omitempty"`
	Interface         string
	PeerInterface     string
	NetNS             string                  `json:",omitempty"`
	Bridge            string                  `json:",omitempty"`
	Components        map[string]ISOComponent `json:",omitempty"`
	RetiredComponents map[string]ISOComponent `json:",omitempty"`
	DesiredModes      []string
	AllocatorVersion  int
	PolicyVersion     int
	RemoveRequested   bool   `json:",omitempty"`
	CleanupVerified   bool   `json:",omitempty"`
	RemoveCleanData   bool   `json:",omitempty"`
	LastError         string `json:",omitempty"`
}

type VMHostConfig struct {
	MemoryPolicy string `json:",omitempty"`
}

type DockerNetwork struct {
	NetworkID string
	NetNS     string
	Mode      string

	IPv4Gateway netip.Prefix
	IPv4Range   netip.Prefix

	Endpoints map[string]*DockerEndpoint

	// Deprecated: use Endpoints instead.
	EndpointAddrs map[string]netip.Prefix

	PortMap map[string]*EndpointPort // key is "proto/hostport"
}

type EndpointPort struct {
	EndpointID string
	Port       uint16
}

type ProtoPort struct {
	Proto int
	Port  uint16
}

func (p ProtoPort) String() string {
	return fmt.Sprintf("%d/%d", p.Proto, p.Port)
}

func (p *ProtoPort) Parse(data string) error {
	_, err := fmt.Sscanf(data, "%d/%d", &p.Proto, &p.Port)
	return err
}

type DockerEndpoint struct {
	EndpointID string
	IPv4       netip.Prefix
}

type ImageRepoName string

// Tag or digest.
type ImageRef string

type ImageRepo struct {
	Refs map[ImageRef]ImageManifest `json:",omitempty"`
}

type ImageManifest struct {
	ContentType string
	BlobHash    string
}

type Volume struct {
	Name string

	Src  string
	Path string
	Type string
	Opts string
	Deps string
}

type ServiceType string

const (
	ServiceTypeDockerCompose ServiceType = "docker-compose"
	ServiceTypeSystemd       ServiceType = "systemd"
	ServiceTypeVM            ServiceType = "vm"
)

type ServiceIdentity struct {
	RequestedUser  string
	RequestedGroup string
	UID            uint32
	GID            uint32
}

// Service is the configuration for one service.
type Service struct {
	// Name is the name of the service.
	Name string

	ServiceType ServiceType
	Identity    *ServiceIdentity `json:",omitempty"`
	// IdentityInstallPending marks a first native install that has staged its
	// database row but has not yet durably entered the identity journal.
	IdentityInstallPending bool `json:",omitempty"`

	// ServiceRoot is the absolute service root on the catch host.
	// Empty means filepath.Join(Store.serviceRoot, Name).
	ServiceRoot string `json:",omitempty"`

	// ServiceRootZFS is the ZFS dataset name used to resolve ServiceRoot.
	// Empty means ServiceRoot is a normal filesystem path or the default root.
	ServiceRootZFS string `json:",omitempty"`

	// SnapshotPolicy overrides catch snapshot defaults for this service.
	// Nil means all snapshot settings inherit from server defaults.
	SnapshotPolicy *SnapshotPolicy `json:",omitempty"`

	// Generation is the current generation of the service.
	Generation int `json:",omitempty"`

	// LatestGeneration is the latest generation of the service.
	LatestGeneration int `json:",omitempty"`

	// Publish lists Docker Compose short-syntax port mappings for the service.
	Publish []string `json:",omitempty"`

	// Artifacts are the artifacts generated for this service.
	Artifacts ArtifactStore

	SvcNetwork *SvcNetwork
	Macvlan    *MacvlanNetwork
	TSNet      *TailscaleNetwork
	VM         *VMConfig      `json:",omitempty"`
	ISO        *ISOAllocation `json:",omitempty"`
}

type VMConfig struct {
	Runtime string
	Image   VMImageConfig
	CPUs    int

	MemoryBytes int64
	Balloon     VMBalloonConfig
	Disk        VMDiskConfig

	Networks []VMNetworkConfig
	SSH      VMSSHConfig
	Console  VMConsoleConfig
	Sockets  VMSocketConfig

	PIDFile    string `json:",omitempty"`
	SetupState string `json:",omitempty"`
}

type VMBalloonConfig struct {
	Mode                 string
	MinBytes             int64
	StatsIntervalSeconds int   `json:",omitempty"`
	LastTargetBytes      int64 `json:",omitempty"`
}

type VMImageConfig struct {
	Payload         string
	Version         string
	Digest          string
	Kernel          string
	RootFS          string
	Distro          string `json:",omitempty"`
	DistroVersion   string `json:",omitempty"`
	DefaultUser     string `json:",omitempty"`
	GuestSystemInit string `json:",omitempty"`
	MetadataDriver  string `json:",omitempty"`
}

type VMDiskConfig struct {
	Backend string
	Bytes   int64
	Path    string
}

type VMNetworkConfig struct {
	Mode      string
	Interface string
	Tap       string
	MAC       string
	IP        netip.Addr
	Parent    string
	VLAN      int
}

type VMSSHConfig struct {
	User       string
	KeyRef     string `json:",omitempty"`
	KnownHosts string `json:",omitempty"`
}

type VMConsoleConfig struct {
	SocketPath string
	LogPath    string
}

type VMSocketConfig struct {
	APISocketPath   string
	VsockSocketPath string `json:",omitempty"`
	VsockGuestCID   uint32 `json:",omitempty"`
}

// SnapshotPolicy stores either server defaults or per-service overrides.
// Nil pointer fields mean inherit from the next policy layer.
type SnapshotPolicy struct {
	Enabled  *bool    `json:",omitempty"`
	KeepLast *int     `json:",omitempty"`
	MaxAge   string   `json:",omitempty"`
	Events   []string `json:",omitempty"`
	Required *bool    `json:",omitempty"`
}

type TailscaleNetwork struct {
	Interface string
	Version   string
	ExitNode  string `json:",omitempty"`
	Tags      []string
	StableID  tailcfg.StableNodeID
}

type MacvlanNetwork struct {
	Interface string
	Mac       string
	Parent    string
	VLAN      int
}

type SvcNetwork struct {
	IPv4 netip.Addr
}

func Gen(gen int) ArtifactRef {
	return ArtifactRef(fmt.Sprintf("gen-%d", gen))
}

type ArtifactStore map[ArtifactName]*Artifact

func (as ArtifactStore) Gen(name ArtifactName, gen int) (string, bool) {
	a, ok := as[name]
	if !ok {
		return "", false
	}
	r, ok := a.Refs[Gen(gen)]
	return r, ok
}

func (as ArtifactStore) Staged(name ArtifactName) (string, bool) {
	a, ok := as[name]
	if !ok {
		return "", false
	}
	r, ok := a.Refs["staged"]
	return r, ok
}

func (as ArtifactStore) Latest(name ArtifactName) (string, bool) {
	a, ok := as[name]
	if !ok {
		return "", false
	}
	r, ok := a.Refs["latest"]
	return r, ok
}

type Artifact struct {
	Refs map[ArtifactRef]string // path on disk
}

type ArtifactName string

const (
	ArtifactBinary  ArtifactName = "binary"
	ArtifactEnvFile ArtifactName = "env"

	ArtifactDockerComposeFile    ArtifactName = "compose.yml"
	ArtifactDockerComposeNetwork ArtifactName = "compose.network"
	ArtifactTypeScriptFile       ArtifactName = "main.ts"
	ArtifactPythonFile           ArtifactName = "main.py"
	ArtifactSystemdUnit          ArtifactName = "systemd.service"
	ArtifactSystemdTimerFile     ArtifactName = "systemd.timer"

	ArtifactNetNSService ArtifactName = "netns.service"
	ArtifactNetNSEnv     ArtifactName = "netns.env"
	ArtifactTSService    ArtifactName = "tailscale.service"
	ArtifactTSEnv        ArtifactName = "tailscale.env"
	ArtifactTSBinary     ArtifactName = "tailscaled"
	ArtifactTSConfig     ArtifactName = "tailscaled.json"
	ArtifactNetNSResolv  ArtifactName = "resolv.conf"
)

// ArtifactRef is a reference to an artifact.
//
// It's either "latest", "staged", or a generation number like "gen-23".
type ArtifactRef string

type Store struct {
	file        string
	serviceRoot string

	mu sync.Mutex // protects the following
	d  *Data
}

// NewStore returns a new Store with the given file.
func NewStore(file, serviceRoot string) *Store {
	return &Store{file: file, serviceRoot: serviceRoot}
}

func migrate(d *Data) (migrated bool, _ error) {
	for d.DataVersion < CurrentDataVersion {
		migrator, ok := migrators[d.DataVersion]
		if !ok {
			return false, fmt.Errorf("no migrator for version %d", d.DataVersion)
		}
		if err := migrator(d); err != nil {
			return false, fmt.Errorf("migrating version %d: %v", d.DataVersion, err)
		}
		d.DataVersion++
		migrated = true
	}
	return migrated, nil
}

// Get returns a DataView of s.d.
// If s.d is nil, it reads s.file into s.d.
func (s *Store) Get() (DataView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getLocked()
}

func (s *Store) getLocked() (DataView, error) {
	if s.d == nil {
		created, err := s.readLocked()
		if err != nil {
			return DataView{}, err
		}
		if created {
			s.d.DataVersion = CurrentDataVersion
		} else {
			if err := s.migrateLoadedDataLocked(); err != nil {
				return DataView{}, err
			}
		}
	}
	return s.d.View(), nil
}

func (s *Store) migrateLoadedDataLocked() error {
	staged := s.d.Clone()
	origVersion := staged.DataVersion
	migrated, err := migrate(staged)
	if err != nil {
		s.d = nil
		return fmt.Errorf("migrating data: %v", err)
	}
	if !migrated {
		return nil
	}
	if err := s.backupLocked(origVersion); err != nil {
		s.d = nil
		return fmt.Errorf("backing up migrated data: %v", err)
	}
	renamed, err := s.saveDataLocked(staged)
	if err != nil {
		if renamed {
			s.d = staged
		} else {
			s.d = nil
		}
		return fmt.Errorf("saving migrated data: %v", err)
	}
	s.d = staged
	return nil
}

func (s *Store) backupLocked(ver int) error {
	backup := s.file + fmt.Sprintf(".v%d.%v", ver, fileutil.Version())
	if err := fileutil.CopyFile(s.file, backup); err != nil {
		return err
	}
	log.Printf("backed up %s to %s", s.file, backup)
	return nil
}

// Set sets s.d to a clone of d.
func (s *Store) Set(d *Data) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setLocked(d)
}

func (s *Store) setLocked(d *Data) error {
	next := d.Clone()
	renamed, err := s.saveDataLocked(next)
	if err == nil || renamed {
		s.d = next
	}
	return err
}

// readLocked reads s.file into s.d.
func (s *Store) readLocked() (created bool, err error) {
	f, err := os.Open(s.file)
	if os.IsNotExist(err) {
		s.d = new(Data)
		return true, nil
	}
	if err != nil {
		return false, err
	}
	defer func() {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}()
	jd := json.NewDecoder(f)
	d := new(Data)
	if err := jd.Decode(&d); err != nil {
		return false, err
	}
	if d == nil {
		return false, fmt.Errorf("database file %s contains null data", s.file)
	}
	s.d = d
	return false, nil
}

// saveDataLocked saves d to s.file.
func (s *Store) saveDataLocked(d *Data) (bool, error) {
	if d == nil {
		return false, nil
	}
	dir := filepath.Dir(s.file)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(dir, "db.json")
	if err != nil {
		return false, err
	}
	defer removeTempFile(tmp.Name())
	jc := json.NewEncoder(tmp)
	jc.SetIndent("", "  ")
	if err := jc.Encode(d); err != nil {
		return false, err
	}
	if err := syncAndCloseDBFile(tmp); err != nil {
		return false, err
	}
	if err := renameDBFile(tmp.Name(), s.file); err != nil {
		return false, err
	}
	return true, syncDBParentDirectory(s.file)
}

func syncAndCloseDBFile(f *os.File) error {
	syncErr := syncDBFile(f)
	closeErr := f.Close()
	return errors.Join(syncErr, closeErr)
}

func syncDBParentDirectory(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	syncErr := syncDBDirectory(dir)
	closeErr := dir.Close()
	return errors.Join(syncErr, closeErr)
}

func removeTempFile(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("failed to remove temp database file %s: %v", path, err)
	}
}

func (s *Store) MutateData(f func(*Data) error) (*Data, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dv, err := s.getLocked()
	if err != nil {
		return nil, fmt.Errorf("failed to get data: %v", err)
	}
	d := dv.AsStruct()
	if d == nil {
		d = new(Data)
	}
	if err := f(d); err != nil {
		return nil, fmt.Errorf("failed to mutate data: %v", err)
	}
	if err := s.setLocked(d); err != nil {
		return nil, fmt.Errorf("failed to save data: %v", err)
	}
	return d, nil
}

func (s *Store) MutateService(name string, f func(*Data, *Service) error) (*Data, *Service, error) {
	var svc *Service
	d, err := s.MutateData(func(d *Data) error {
		var ok bool
		svc, ok = d.Services[name]
		if !ok {
			svc = &Service{
				Name: name,
			}
			mak.Set(&d.Services, name, svc)
		}
		return f(d, svc)
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to mutate data: %v", err)
	}
	return d, svc, nil
}
