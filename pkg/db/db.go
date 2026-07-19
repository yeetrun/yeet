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
	"golang.org/x/sys/unix"
	"tailscale.com/tailcfg"
	"tailscale.com/util/mak"
)

var (
	renameDBFile    = os.Rename
	syncDBFile      = func(f *os.File) error { return f.Sync() }
	syncDBDirectory = func(f *os.File) error { return f.Sync() }
)

//go:generate go run tailscale.com/cmd/viewer -type=Data,Service,ServiceIdentity,SnapshotPolicy,Volume,ImageRepo,Artifact,DockerNetwork,DockerEndpoint,TailscaleNetwork,EndpointPort,VMConfig,VMImageConfig,VMDiskConfig,VMNetworkConfig,VMSSHConfig,VMConsoleConfig,VMSocketConfig,VMBalloonConfig,VMHostConfig,ISOPool,ISOAllocation,ISOComponent,VMGuestBaseConfig,VMKernelArtifactConfig,VMRuntimeArtifactConfig,VMRuntimeTrialConfig,VMRuntimeLifecycleConfig,VMComponentsConfig --copyright=false

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
	MemoryPolicy        string   `json:",omitempty"`
	RuntimePolicy       string   `json:",omitempty"`
	RuntimeChannel      string   `json:",omitempty"`
	ProtectedRuntimeIDs []string `json:",omitempty"`
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
	Runtime    string
	Image      VMImageConfig
	Components *VMComponentsConfig `json:",omitempty"`
	CPUs       int

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

type VMGuestBaseConfig struct {
	ID               string
	ManifestSHA256   string
	Source           string
	RootFSProvenance string `json:",omitempty"`
}

type VMKernelArtifactConfig struct {
	ID             string
	ManifestSHA256 string
	SHA256         string
	Path           string
	Source         string
}

type VMRuntimeArtifactConfig struct {
	ID                string
	ManifestSHA256    string
	FirecrackerSHA256 string
	JailerSHA256      string
	Firecracker       string
	Jailer            string
	Source            string
}

type VMRuntimeTrialConfig struct {
	State         string
	CandidateID   string
	PreviousID    string
	RecoveryPoint string `json:",omitempty"`
	StartedAt     string
	LastError     string `json:",omitempty"`
}

type VMRuntimeLifecycleConfig struct {
	Policy     string
	Channel    string
	Configured VMRuntimeArtifactConfig
	Staged     *VMRuntimeArtifactConfig `json:",omitempty"`
	Previous   *VMRuntimeArtifactConfig `json:",omitempty"`
	Trial      *VMRuntimeTrialConfig    `json:",omitempty"`
}

type VMComponentsConfig struct {
	GuestBase VMGuestBaseConfig
	Kernel    VMKernelArtifactConfig
	Runtime   VMRuntimeLifecycleConfig
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
	deps        storeFileDeps

	mu sync.Mutex // protects the following
	d  *Data
}

type storeFileDeps struct {
	openLockFile  func(string) (*os.File, error)
	lockFile      func(*os.File) error
	unlockFile    func(*os.File) error
	closeLockFile func(*os.File) error
	mkdirDir      func(string, os.FileMode) error
	syncFile      func(*os.File) error
	rename        func(string, string) error
	openDir       func(string) (*os.File, error)
	syncDir       func(*os.File) error
	closeDir      func(*os.File) error
}

// PostPublicationError reports that a database replacement was atomically
// published, but a later durability or lock-cleanup operation failed. Callers
// must inspect MutationCommitted before deciding whether compensating work is
// safe, and must still surface the operational error.
type PostPublicationError struct {
	Err error

	// MutationCommitted is true when the Set or MutateData replacement requested
	// by the caller was published. It is false when only an automatic database
	// migration was published before the requested mutation could run.
	MutationCommitted bool
}

func (e *PostPublicationError) Error() string {
	return fmt.Sprintf("database update was published, but post-publication work failed: %v", e.Err)
}

func (e *PostPublicationError) Unwrap() error { return e.Err }

// PostFinalizationError reports that database lock cleanup failed after a
// WithLatestDataLocked callback was eligible to run. FinalizerCompleted is true
// only when the callback returned nil before the cleanup failure. Callers that
// use the callback as an irreversible external commit boundary must inspect
// this outcome before deciding whether compensation is safe.
type PostFinalizationError struct {
	Err error

	FinalizerCompleted bool
}

func (e *PostFinalizationError) Error() string {
	if e.FinalizerCompleted {
		return fmt.Sprintf("database finalizer completed, but lock cleanup failed: %v", e.Err)
	}
	return fmt.Sprintf("database finalizer did not complete and lock cleanup failed: %v", e.Err)
}

func (e *PostFinalizationError) Unwrap() error { return e.Err }

func defaultStoreFileDeps() storeFileDeps {
	return storeFileDeps{
		openLockFile: openPersistentDBLock,
		lockFile: func(file *os.File) error {
			return unix.Flock(int(file.Fd()), unix.LOCK_EX)
		},
		unlockFile: func(file *os.File) error {
			return unix.Flock(int(file.Fd()), unix.LOCK_UN)
		},
		closeLockFile: func(file *os.File) error { return file.Close() },
		mkdirDir:      os.Mkdir,
		syncFile:      func(file *os.File) error { return syncDBFile(file) },
		rename:        func(oldPath, newPath string) error { return renameDBFile(oldPath, newPath) },
		openDir:       os.Open,
		syncDir:       func(dir *os.File) error { return syncDBDirectory(dir) },
		closeDir:      func(dir *os.File) error { return dir.Close() },
	}
}

// NewStore returns a new Store with the given file.
func NewStore(file, serviceRoot string) *Store {
	return &Store{file: file, serviceRoot: serviceRoot, deps: defaultStoreFileDeps()}
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
		var loaded *Data
		published, err := s.withFileLock(func() (bool, error) {
			var err error
			var published bool
			loaded, published, err = s.loadLatestDataLocked()
			return published, err
		})
		if published && loaded != nil {
			s.d = loaded
		}
		if err != nil {
			if published {
				return DataView{}, &PostPublicationError{Err: err}
			}
			return DataView{}, err
		}
		s.d = loaded
	}
	return s.d.View(), nil
}

// WithLatestDataLocked reloads the exact latest on-disk database under the
// stable cross-process lock and invokes finalizer while every Store-based
// writer is excluded. The callback receives an isolated read-only view and
// cannot mutate the Store's cached state. Ordinary success does not rewrite the
// database.
//
// A successful implicit migration may publish before finalizer runs. If that
// publication reports a durability error, finalizer is not invoked. The
// The callback must not synchronously call any Store method targeting this
// database file: this Store's mutex and the stable cross-process lock are both
// held for its duration.
func (s *Store) WithLatestDataLocked(finalizer func(DataView) error) error {
	if finalizer == nil {
		return fmt.Errorf("latest-data finalizer is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var latest *Data
	loaded := false
	finalizerCompleted := false
	published, operationErr, cleanupErr := s.withFileLockOutcome(func() (bool, error) {
		var migrationPublished bool
		var err error
		latest, migrationPublished, err = s.loadLatestDataLocked()
		if err != nil {
			return migrationPublished, fmt.Errorf("failed to get latest data: %w", err)
		}
		loaded = true
		snapshot := latest.Clone()
		if err := finalizer(snapshot.View()); err != nil {
			return migrationPublished, fmt.Errorf("latest-data finalizer: %w", err)
		}
		finalizerCompleted = true
		return migrationPublished, nil
	})
	if latest != nil && (loaded || published) {
		s.d = latest.Clone()
	}

	resultErr := operationErr
	if cleanupErr != nil {
		resultErr = &PostFinalizationError{
			Err:                errors.Join(operationErr, cleanupErr),
			FinalizerCompleted: finalizerCompleted,
		}
	}
	if resultErr != nil && published {
		return &PostPublicationError{Err: resultErr}
	}
	return resultErr
}

func (s *Store) loadLatestDataLocked() (*Data, bool, error) {
	loaded, created, err := s.readDataLocked()
	if err != nil {
		return nil, false, err
	}
	if created {
		loaded.DataVersion = CurrentDataVersion
		return loaded, false, nil
	}
	staged := loaded.Clone()
	origVersion := staged.DataVersion
	migrated, err := migrate(staged)
	if err != nil {
		return nil, false, fmt.Errorf("migrating data: %w", err)
	}
	if !migrated {
		return staged, false, nil
	}
	if err := s.backupLocked(origVersion); err != nil {
		return nil, false, fmt.Errorf("backing up migrated data: %w", err)
	}
	published, err := s.saveDataLocked(staged)
	if err != nil {
		if published {
			return staged, true, fmt.Errorf("saving migrated data: %w", err)
		}
		return nil, false, fmt.Errorf("saving migrated data: %w", err)
	}
	return staged, published, nil
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
	next := d.Clone()
	if next != nil && next.DataVersion == 0 {
		next.DataVersion = CurrentDataVersion
	}
	published, err := s.withFileLock(func() (bool, error) {
		return s.saveDataLocked(next)
	})
	if published {
		s.d = next
	}
	if err != nil {
		if published {
			return &PostPublicationError{Err: err, MutationCommitted: true}
		}
		return err
	}
	s.d = next
	return nil
}

func (s *Store) readDataLocked() (_ *Data, created bool, err error) {
	f, err := os.Open(s.file)
	if os.IsNotExist(err) {
		return new(Data), true, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer func() {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}()
	jd := json.NewDecoder(f)
	d := new(Data)
	if err := jd.Decode(&d); err != nil {
		return nil, false, err
	}
	if d == nil {
		return nil, false, fmt.Errorf("database file %s contains null data", s.file)
	}
	return d, false, nil
}

// saveDataLocked saves d to s.file.
func (s *Store) saveDataLocked(d *Data) (published bool, retErr error) {
	if d == nil {
		return false, nil
	}
	dir := filepath.Dir(s.file)
	if err := s.ensureDurableDir(dir, 0o755); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(dir, "db.json")
	if err != nil {
		return false, err
	}
	tmpOpen := true
	defer func() {
		retErr = closeTemporaryDatabase(tmp, tmpOpen, retErr)
		removeTempFile(tmp.Name())
	}()
	if err := s.writeTemporaryDatabase(tmp, d); err != nil {
		return false, err
	}
	tmpOpen = false
	if err := s.deps.rename(tmp.Name(), s.file); err != nil {
		return false, fmt.Errorf("replace database: %w", err)
	}
	published = true
	if err := s.syncDatabaseParent(dir); err != nil {
		return true, err
	}
	return true, nil
}

func closeTemporaryDatabase(file *os.File, open bool, retErr error) error {
	if open {
		return errors.Join(retErr, file.Close())
	}
	return retErr
}

func (s *Store) writeTemporaryDatabase(file *os.File, data *Data) error {
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(data); err != nil {
		return err
	}
	if err := s.deps.syncFile(file); err != nil {
		return fmt.Errorf("sync temporary database: %w", err)
	}
	return file.Close()
}

func (s *Store) syncDatabaseParent(dir string) error {
	parent, err := s.deps.openDir(dir)
	if err != nil {
		return fmt.Errorf("open database parent for sync: %w", err)
	}
	syncErr := s.deps.syncDir(parent)
	closeErr := s.deps.closeDir(parent)
	if syncErr != nil || closeErr != nil {
		return errors.Join(wrapDBFileError("sync database parent", syncErr), wrapDBFileError("close database parent", closeErr))
	}
	return nil
}

func (s *Store) withFileLock(f func() (bool, error)) (bool, error) {
	published, operationErr, cleanupErr := s.withFileLockOutcome(f)
	return published, errors.Join(operationErr, cleanupErr)
}

func (s *Store) withFileLockOutcome(f func() (bool, error)) (published bool, operationErr error, cleanupErr error) {
	dir := filepath.Dir(s.file)
	if err := s.ensureDurableDir(dir, 0o755); err != nil {
		return false, fmt.Errorf("create database parent: %w", err), nil
	}
	lock, err := s.deps.openLockFile(s.file + ".lock")
	if err != nil {
		return false, fmt.Errorf("open persistent database lock: %w", err), nil
	}
	if err := s.deps.lockFile(lock); err != nil {
		return false, errors.Join(
			fmt.Errorf("lock database: %w", err),
			wrapDBFileError("close database lock", s.deps.closeLockFile(lock)),
		), nil
	}
	defer func() {
		cleanupErr = errors.Join(
			wrapDBFileError("unlock database", s.deps.unlockFile(lock)),
			wrapDBFileError("close database lock", s.deps.closeLockFile(lock)),
		)
	}()
	published, operationErr = f()
	return published, operationErr, nil
}

func (s *Store) ensureDurableDir(dir string, mode os.FileMode) error {
	dir = filepath.Clean(dir)
	existing, missing, err := existingDatabaseDirectoryAncestor(dir)
	if err != nil {
		return err
	}
	// The nearest existing directory may be the result of a previous process
	// crashing or returning after mkdir but before its parent directory was
	// durably synced. Re-sync its link on every attempt so restart recovery does
	// not depend on in-memory bookkeeping.
	if err := s.syncDirectoryLink(existing); err != nil {
		return err
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := s.ensureDurableDatabaseDirectory(missing[i], mode); err != nil {
			return err
		}
	}
	return nil
}

func existingDatabaseDirectoryAncestor(dir string) (string, []string, error) {
	var missing []string
	for current := dir; ; current = filepath.Dir(current) {
		info, err := os.Stat(current)
		if err == nil {
			if !info.IsDir() {
				return "", nil, fmt.Errorf("database parent %s is not a directory", current)
			}
			return current, missing, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", nil, err
		}
		missing = append(missing, current)
		parent := filepath.Dir(current)
		if parent == current {
			return "", nil, fmt.Errorf("no existing ancestor for database parent %s", dir)
		}
	}
}

func (s *Store) ensureDurableDatabaseDirectory(link string, mode os.FileMode) error {
	if err := s.deps.mkdirDir(link, mode); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		info, statErr := os.Stat(link)
		if statErr != nil {
			return statErr
		}
		if !info.IsDir() {
			return fmt.Errorf("database parent %s is not a directory", link)
		}
	}
	return s.syncDirectoryLink(link)
}

func (s *Store) syncDirectoryLink(link string) error {
	parentPath := filepath.Dir(link)
	parent, err := s.deps.openDir(parentPath)
	if err != nil {
		return fmt.Errorf("open created database parent link %s for sync: %w", parentPath, err)
	}
	syncErr := s.deps.syncDir(parent)
	closeErr := s.deps.closeDir(parent)
	if syncErr != nil || closeErr != nil {
		return errors.Join(
			wrapDBFileError("sync created database parent link "+parentPath, syncErr),
			wrapDBFileError("close created database parent link "+parentPath, closeErr),
		)
	}
	return nil
}

type persistentDBLockMetadata struct {
	mode uint32
	uid  uint32
	gid  uint32
}

func validatePersistentDBLockMetadata(metadata persistentDBLockMetadata, expectedUID uint32) error {
	if metadata.mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("persistent database lock is not a regular file")
	}
	if metadata.uid != expectedUID {
		return fmt.Errorf("persistent database lock owner UID is %d, want %d", metadata.uid, expectedUID)
	}
	if metadata.mode&0o777 != 0o600 {
		return fmt.Errorf("persistent database lock permissions are %o, want 0600", metadata.mode&0o777)
	}
	return nil
}

func openPersistentDBLock(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("bind persistent database lock descriptor")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, errors.Join(fmt.Errorf("inspect persistent database lock: %w", err), file.Close())
	}
	metadata := persistentDBLockMetadata{
		mode: uint32(stat.Mode),
		uid:  stat.Uid,
		gid:  stat.Gid,
	}
	if err := validatePersistentDBLockMetadata(metadata, uint32(os.Geteuid())); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func wrapDBFileError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func removeTempFile(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("failed to remove temp database file %s: %v", path, err)
	}
}

func (s *Store) MutateData(f func(*Data) error) (*Data, error) {
	return s.mutateData(func(d *Data) (func() error, error) {
		return nil, f(d)
	})
}

// MutateDataWithPrePublicationCompensation mutates the latest database view
// and saves it under the cross-process lock. If the requested replacement does
// not publish, compensate runs before that lock is released. Compensation is
// never run after the requested replacement crosses the atomic rename.
func (s *Store) MutateDataWithPrePublicationCompensation(f func(*Data) (compensate func() error, err error)) (*Data, error) {
	return s.mutateData(f)
}

func (s *Store) mutateData(f func(*Data) (compensate func() error, err error)) (*Data, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := &dataMutationState{}
	published, err := s.withFileLock(func() (bool, error) {
		return s.runDataMutationLocked(f, state)
	})
	if published && state.publishedView != nil {
		s.d = state.publishedView.Clone()
	}
	if err != nil {
		if published {
			var result *Data
			if state.mutationCommitted {
				result = state.next
			}
			return result, &PostPublicationError{Err: err, MutationCommitted: state.mutationCommitted}
		}
		return nil, err
	}
	s.d = state.next.Clone()
	return state.next, nil
}

type dataMutationState struct {
	next              *Data
	publishedView     *Data
	mutationCommitted bool
}

func (s *Store) runDataMutationLocked(f func(*Data) (func() error, error), state *dataMutationState) (bool, error) {
	latest, migrationPublished, err := s.loadLatestDataLocked()
	if migrationPublished && latest != nil {
		state.publishedView = latest
	}
	if err != nil {
		return migrationPublished, fmt.Errorf("failed to get data: %w", err)
	}
	state.next = latest.Clone()
	compensate, err := f(state.next)
	if err != nil {
		return migrationPublished, fmt.Errorf("failed to mutate data: %w", runDataMutationCompensation(err, compensate))
	}
	mutationPublished, err := s.saveDataLocked(state.next)
	if mutationPublished {
		state.mutationCommitted = true
		state.publishedView = state.next
	}
	if err != nil {
		if !mutationPublished {
			err = runDataMutationCompensation(err, compensate)
		}
		return migrationPublished || mutationPublished, fmt.Errorf("failed to save data: %w", err)
	}
	return migrationPublished || mutationPublished, nil
}

func runDataMutationCompensation(err error, compensate func() error) error {
	if compensate == nil {
		return err
	}
	return errors.Join(err, wrapDBFileError("pre-publication compensation", compensate()))
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
		return nil, nil, fmt.Errorf("failed to mutate data: %w", err)
	}
	return d, svc, nil
}
