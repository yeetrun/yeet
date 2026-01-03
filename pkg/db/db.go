// Package db provides a simple JSON file-backed database.
package db

import (
	"encoding/json"
	"fmt"
	"log"
	"net/netip"
	"os"
	"path/filepath"
	"sync"

	"github.com/shayne/yeet/pkg/fileutil"
	"tailscale.com/tailcfg"
	"tailscale.com/util/mak"
)

//go:generate go run tailscale.com/cmd/viewer -type=Data,Service,Volume,ImageRepo,Artifact,DockerNetwork,DockerEndpoint,TailscaleNetwork,EndpointPort --copyright=false

// Data is the full JSON structure of the database.
type Data struct {
	// DataVersion is the version of the data format. This is used to determine
	// how to parse the data.
	DataVersion int `json:",omitempty"`

	Services map[string]*Service

	Images map[ImageRepoName]*ImageRepo

	Volumes map[string]*Volume

	DockerNetworks map[string]*DockerNetwork
}

type DockerNetwork struct {
	NetworkID string
	NetNS     string

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
)

// Service is the configuration for one service.
type Service struct {
	// Name is the name of the service.
	Name string

	ServiceType ServiceType

	// Generation is the current generation of the service.
	Generation int `json:",omitempty"`

	// LatestGeneration is the latest generation of the service.
	LatestGeneration int `json:",omitempty"`

	// Artifacts are the artifacts generated for this service.
	Artifacts ArtifactStore

	SvcNetwork *SvcNetwork
	Macvlan    *MacvlanNetwork
	TSNet      *TailscaleNetwork
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
			origVersion := s.d.DataVersion
			migrated, err := migrate(s.d)
			if err != nil {
				return DataView{}, fmt.Errorf("migrating data: %v", err)
			}
			if migrated {
				if err := s.backupLocked(origVersion); err != nil {
					return DataView{}, fmt.Errorf("backing up migrated data: %v", err)
				}
				if err := s.saveLocked(); err != nil {
					return DataView{}, fmt.Errorf("saving migrated data: %v", err)
				}
			}
		}
	}
	return s.d.View(), nil
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
	s.d = d.Clone()
	return s.saveLocked()
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
	defer f.Close()
	jd := json.NewDecoder(f)
	d := new(Data)
	if err := jd.Decode(&d); err != nil {
		return false, err
	}
	s.d = d
	return false, nil
}

// saveLocked saves s.d to s.file.
func (s *Store) saveLocked() error {
	if s.d == nil {
		return nil
	}
	dir := filepath.Dir(s.file)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "db.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	jc := json.NewEncoder(tmp)
	jc.SetIndent("", "  ")
	if err := jc.Encode(s.d); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.file)
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
