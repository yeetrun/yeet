// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/serviceid"
	"golang.org/x/sys/unix"
)

const (
	vmRuntimeJournalSchema              = "yeet.vm.runtime-transaction"
	vmRuntimeJournalSchemaVersion       = 1
	vmRuntimeJournalMarkerSchema        = "yeet.vm.runtime-transaction-marker"
	vmRuntimeJournalMarkerSchemaVersion = 1
	vmRuntimeJournalDirName             = "vm-runtime-transactions"
	vmRuntimeJournalLockFileName        = "vm-runtime-transactions.lock"

	vmRuntimeJournalMaxMembers          = 1024
	vmRuntimeJournalMaxEntries          = 4096
	vmRuntimeJournalMaxFileSize         = 6 << 20
	vmRuntimeJournalMaxMarkerSize       = 96 << 20
	vmRuntimeJournalMaxPayload          = 1 << 20
	vmRuntimeJournalMaxAggregateRaw     = 128 << 20
	vmRuntimeJournalMaxAggregateDecoded = 96 << 20
)

type vmRuntimeJournalState string

const (
	vmRuntimeJournalStatePrepared          vmRuntimeJournalState = "prepared"
	vmRuntimeJournalStateDerivedPublished  vmRuntimeJournalState = "derived-published"
	vmRuntimeJournalStateDatabaseCommitted vmRuntimeJournalState = "database-committed"
)

type vmRuntimeJournalPhase string

const (
	vmRuntimeJournalPhaseStable        vmRuntimeJournalPhase = "stable"
	vmRuntimeJournalPhasePublishing    vmRuntimeJournalPhase = "publishing"
	vmRuntimeJournalPhaseTransitioning vmRuntimeJournalPhase = "transitioning"
	vmRuntimeJournalPhaseRemoving      vmRuntimeJournalPhase = "removing"
	vmRuntimeJournalPhaseTombstoned    vmRuntimeJournalPhase = "tombstoned"
)

type vmRuntimeJournalOperation string

const (
	vmRuntimeJournalOperationStable     vmRuntimeJournalOperation = "stable"
	vmRuntimeJournalOperationPrepare    vmRuntimeJournalOperation = "prepare"
	vmRuntimeJournalOperationTransition vmRuntimeJournalOperation = "transition"
	vmRuntimeJournalOperationRemove     vmRuntimeJournalOperation = "remove"
)

type vmRuntimeJournalDBProjection struct {
	Components  *db.VMComponentsConfig `json:"components"`
	ImageKernel string                 `json:"imageKernel"`
}

type vmRuntimeJournalVMHostProjection struct {
	RuntimePolicy  string `json:"runtimePolicy"`
	RuntimeChannel string `json:"runtimeChannel"`
}

type vmRuntimeJournalFile struct {
	Path     string `json:"path"`
	Exists   bool   `json:"exists"`
	Contents []byte `json:"contents"`
	Mode     uint32 `json:"mode"`
	UID      uint32 `json:"uid"`
	GID      uint32 `json:"gid"`
	SHA256   string `json:"sha256"`
}

type vmRuntimeJournalRecord struct {
	Schema             string                            `json:"schema"`
	SchemaVersion      int                               `json:"schemaVersion"`
	TransactionID      string                            `json:"transactionID"`
	Members            []string                          `json:"members"`
	Service            string                            `json:"service"`
	ServiceRoot        string                            `json:"serviceRoot"`
	State              vmRuntimeJournalState             `json:"state"`
	PreparedAt         time.Time                         `json:"preparedAt"`
	UpdatedAt          time.Time                         `json:"updatedAt"`
	PreconditionSHA256 string                            `json:"preconditionSHA256"`
	OldDB              vmRuntimeJournalDBProjection      `json:"oldDB"`
	NewDB              vmRuntimeJournalDBProjection      `json:"newDB"`
	VMHostProjection   bool                              `json:"vmHostProjection,omitempty"`
	OldVMHost          *vmRuntimeJournalVMHostProjection `json:"oldVMHost,omitempty"`
	NewVMHost          *vmRuntimeJournalVMHostProjection `json:"newVMHost,omitempty"`
	OldDescriptor      vmRuntimeJournalFile              `json:"oldDescriptor"`
	NewDescriptor      vmRuntimeJournalFile              `json:"newDescriptor"`
	OldUnit            vmRuntimeJournalFile              `json:"oldUnit"`
	NewUnit            vmRuntimeJournalFile              `json:"newUnit"`
}

type vmRuntimeJournalMarker struct {
	Schema        string                    `json:"schema"`
	SchemaVersion int                       `json:"schemaVersion"`
	TransactionID string                    `json:"transactionID"`
	OperationID   string                    `json:"operationID"`
	Members       []string                  `json:"members"`
	Plans         []vmRuntimeJournalPlan    `json:"plans"`
	Operation     vmRuntimeJournalOperation `json:"operation"`
	Phase         vmRuntimeJournalPhase     `json:"phase"`
	FromState     vmRuntimeJournalState     `json:"fromState"`
	TargetState   vmRuntimeJournalState     `json:"targetState"`
	UpdatedAt     time.Time                 `json:"updatedAt"`
	Previous      []vmRuntimeJournalRecord  `json:"previous"`
	Desired       []vmRuntimeJournalRecord  `json:"desired"`
}

type vmRuntimeJournalPlan struct {
	Service       string `json:"service"`
	SourceSHA256  string `json:"sourceSHA256"`
	TargetSHA256  string `json:"targetSHA256"`
	StageName     string `json:"stageName"`
	TombstoneName string `json:"tombstoneName"`
}

type vmRuntimeJournalDBClassification string

const (
	vmRuntimeJournalDBOld     vmRuntimeJournalDBClassification = "old"
	vmRuntimeJournalDBNew     vmRuntimeJournalDBClassification = "new"
	vmRuntimeJournalDBNeither vmRuntimeJournalDBClassification = "neither"
	vmRuntimeJournalDBMixed   vmRuntimeJournalDBClassification = "mixed"
)

type vmRuntimeJournalGroup struct {
	TransactionID string
	Members       []string
	Records       []vmRuntimeJournalRecord
	Phase         vmRuntimeJournalPhase
	Tombstones    []string
}

// ClassifyDB compares only the database fields owned by a runtime transaction.
// Unrelated service changes do not affect recovery classification.
func (group vmRuntimeJournalGroup) ClassifyDB(data *db.Data) vmRuntimeJournalDBClassification {
	oldCount, newCount, ok := classifyVMRuntimeJournalRecords(data, group.Records)
	if !ok {
		return vmRuntimeJournalDBNeither
	}
	total := len(group.Records)
	oldCount, newCount, total, ok = classifyVMRuntimeJournalHost(data, group.Records, oldCount, newCount, total)
	if !ok {
		return vmRuntimeJournalDBNeither
	}
	return vmRuntimeJournalDBClassificationForCounts(oldCount, newCount, total)
}

func classifyVMRuntimeJournalRecords(data *db.Data, records []vmRuntimeJournalRecord) (oldCount, newCount int, ok bool) {
	for _, record := range records {
		projection, ok := vmRuntimeJournalProjectionFromData(data, record.Service)
		if !ok {
			return 0, 0, false
		}
		oldMatch := equalVMRuntimeJournalProjection(projection, record.OldDB)
		newMatch := equalVMRuntimeJournalProjection(projection, record.NewDB)
		if !oldMatch && !newMatch {
			return 0, 0, false
		}
		if oldMatch {
			oldCount++
		}
		if newMatch {
			newCount++
		}
	}
	return oldCount, newCount, true
}

func classifyVMRuntimeJournalHost(data *db.Data, records []vmRuntimeJournalRecord, oldCount, newCount, total int) (int, int, int, bool) {
	if len(records) == 0 || !records[0].VMHostProjection {
		return oldCount, newCount, total, true
	}
	if records[0].OldVMHost == nil || records[0].NewVMHost == nil {
		return 0, 0, 0, false
	}
	current := vmRuntimeJournalVMHostProjectionFromConfig(data.VMHost)
	hostOld := current == *records[0].OldVMHost
	hostNew := current == *records[0].NewVMHost
	if !hostOld && !hostNew {
		return 0, 0, 0, false
	}
	if hostOld {
		oldCount++
	}
	if hostNew {
		newCount++
	}
	return oldCount, newCount, total + 1, true
}

func vmRuntimeJournalDBClassificationForCounts(oldCount, newCount, total int) vmRuntimeJournalDBClassification {
	switch {
	case oldCount == total && newCount < total:
		return vmRuntimeJournalDBOld
	case newCount == total && oldCount < total:
		return vmRuntimeJournalDBNew
	default:
		return vmRuntimeJournalDBMixed
	}
}

func vmRuntimeJournalProjectionFromData(data *db.Data, service string) (vmRuntimeJournalDBProjection, bool) {
	if data == nil || data.Services == nil {
		return vmRuntimeJournalDBProjection{}, false
	}
	svc, ok := data.Services[service]
	if !ok || svc == nil || svc.VM == nil {
		return vmRuntimeJournalDBProjection{}, false
	}
	return vmRuntimeJournalDBProjection{Components: svc.VM.Components.Clone(), ImageKernel: svc.VM.Image.Kernel}, true
}

func vmRuntimeJournalVMHostProjectionFromConfig(host *db.VMHostConfig) vmRuntimeJournalVMHostProjection {
	if host == nil {
		return vmRuntimeJournalVMHostProjection{}
	}
	return vmRuntimeJournalVMHostProjection{RuntimePolicy: host.RuntimePolicy, RuntimeChannel: host.RuntimeChannel}
}

func cloneVMRuntimeJournalVMHostProjection(projection *vmRuntimeJournalVMHostProjection) *vmRuntimeJournalVMHostProjection {
	if projection == nil {
		return nil
	}
	clone := *projection
	return &clone
}

func equalVMRuntimeJournalProjection(a, b vmRuntimeJournalDBProjection) bool {
	return a.ImageKernel == b.ImageKernel && reflect.DeepEqual(a.Components, b.Components)
}

type vmRuntimeJournalPublicationRetainedError struct {
	cause          error
	canonicalPaths []string
	tombstonePaths []string
	uncertainPaths []string
	markerPresent  bool
}

type vmRuntimeJournalRemovalPendingError struct {
	TransactionID string
}

func (err *vmRuntimeJournalRemovalPendingError) Error() string {
	return fmt.Sprintf("VM runtime journal removal %s is durably tombstoned and requires an agreement recheck before commit", err.TransactionID)
}

func (err *vmRuntimeJournalPublicationRetainedError) Error() string {
	if err.markerPresent {
		return fmt.Sprintf("%v; VM runtime journal publication is recoverable from its durable marker", err.cause)
	}
	return fmt.Sprintf("%v; VM runtime journal artifacts were retained for a safe retry", err.cause)
}

func (err *vmRuntimeJournalPublicationRetainedError) Unwrap() error { return err.cause }

func (err *vmRuntimeJournalPublicationRetainedError) CanonicalPaths() []string {
	return append([]string(nil), err.canonicalPaths...)
}

func (err *vmRuntimeJournalPublicationRetainedError) TombstonePaths() []string {
	return append([]string(nil), err.tombstonePaths...)
}

func (err *vmRuntimeJournalPublicationRetainedError) UncertainPaths() []string {
	return append([]string(nil), err.uncertainPaths...)
}

type vmRuntimeJournalStoreDeps struct {
	uid                 uint32
	random              io.Reader
	flock               func(int, int) error
	renameAt            func(int, string, int, string) error
	renameNoReplaceAt   func(int, string, int, string) error
	unlinkAt            func(int, string, int) error
	syncFile            func(*os.File) error
	syncDir             func(*os.File) error
	maxEntries          int
	maxFileSize         int64
	maxMarkerSize       int64
	maxAggregateRaw     int64
	maxAggregateDecoded int64
}

func defaultVMRuntimeJournalStoreDeps() vmRuntimeJournalStoreDeps {
	return vmRuntimeJournalStoreDeps{
		uid:                 uint32(os.Geteuid()),
		random:              rand.Reader,
		flock:               unix.Flock,
		renameAt:            unix.Renameat,
		renameNoReplaceAt:   renameVMJailerUnitNameNoReplaceAt,
		unlinkAt:            unix.Unlinkat,
		syncFile:            func(file *os.File) error { return file.Sync() },
		syncDir:             func(dir *os.File) error { return dir.Sync() },
		maxEntries:          vmRuntimeJournalMaxEntries,
		maxFileSize:         vmRuntimeJournalMaxFileSize,
		maxMarkerSize:       vmRuntimeJournalMaxMarkerSize,
		maxAggregateRaw:     vmRuntimeJournalMaxAggregateRaw,
		maxAggregateDecoded: vmRuntimeJournalMaxAggregateDecoded,
	}
}

func completeVMRuntimeJournalStoreDeps(deps vmRuntimeJournalStoreDeps) vmRuntimeJournalStoreDeps {
	defaults := defaultVMRuntimeJournalStoreDeps()
	deps = completeVMRuntimeJournalStoreFunctions(deps, defaults)
	return completeVMRuntimeJournalStoreLimits(deps, defaults)
}

func completeVMRuntimeJournalStoreFunctions(deps, defaults vmRuntimeJournalStoreDeps) vmRuntimeJournalStoreDeps {
	if deps.random == nil {
		deps.random = defaults.random
	}
	if deps.flock == nil {
		deps.flock = defaults.flock
	}
	if deps.renameAt == nil {
		deps.renameAt = defaults.renameAt
	}
	if deps.renameNoReplaceAt == nil {
		deps.renameNoReplaceAt = defaults.renameNoReplaceAt
	}
	if deps.unlinkAt == nil {
		deps.unlinkAt = defaults.unlinkAt
	}
	if deps.syncFile == nil {
		deps.syncFile = defaults.syncFile
	}
	if deps.syncDir == nil {
		deps.syncDir = defaults.syncDir
	}
	return deps
}

func completeVMRuntimeJournalStoreLimits(deps, defaults vmRuntimeJournalStoreDeps) vmRuntimeJournalStoreDeps {
	if deps.maxEntries <= 0 {
		deps.maxEntries = defaults.maxEntries
	}
	if deps.maxFileSize <= 0 {
		deps.maxFileSize = defaults.maxFileSize
	}
	if deps.maxMarkerSize <= 0 {
		deps.maxMarkerSize = defaults.maxMarkerSize
	}
	if deps.maxAggregateRaw <= 0 {
		deps.maxAggregateRaw = defaults.maxAggregateRaw
	}
	if deps.maxAggregateDecoded <= 0 {
		deps.maxAggregateDecoded = defaults.maxAggregateDecoded
	}
	return deps
}

type vmRuntimeJournalStore struct {
	rootDir string
	root    *os.File
	dir     *os.File
	lock    *os.File
	deps    vmRuntimeJournalStoreDeps
	mu      sync.Mutex
	closed  bool
}

func openVMRuntimeJournalStore(ctx context.Context, rootDir string, deps vmRuntimeJournalStoreDeps) (_ *vmRuntimeJournalStore, retErr error) {
	deps = completeVMRuntimeJournalStoreDeps(deps)
	if strings.TrimSpace(rootDir) == "" || strings.TrimSpace(rootDir) != rootDir || !filepath.IsAbs(rootDir) || filepath.Clean(rootDir) != rootDir {
		return nil, fmt.Errorf("VM runtime journal root must be clean and absolute")
	}
	root, err := openVMRuntimeJournalRoot(rootDir, deps.uid)
	if err != nil {
		return nil, err
	}
	var dir, lock *os.File
	ready := false
	defer func() {
		if !ready {
			retErr = errors.Join(retErr, closeVMRuntimeJournalStoreFiles(lock, dir, root))
		}
	}()
	dir, err = openAndSyncVMRuntimeJournalDir(root, deps)
	if err != nil {
		return nil, err
	}
	lock, err = openAndLockVMRuntimeJournal(ctx, root, deps)
	if err != nil {
		return nil, err
	}
	ready = true
	return &vmRuntimeJournalStore{rootDir: rootDir, root: root, dir: dir, lock: lock, deps: deps}, nil
}

func openVMRuntimeJournalRoot(rootDir string, uid uint32) (*os.File, error) {
	root, parent, _, err := openVMJailStoragePath(rootDir)
	if err != nil {
		return nil, fmt.Errorf("open VM runtime journal root component by component: %w", err)
	}
	if parent != nil {
		if err := parent.Close(); err != nil {
			return nil, errors.Join(fmt.Errorf("close VM runtime journal root parent: %w", err), root.Close())
		}
	}
	if err := validateVMRuntimeJournalRoot(root, uid); err != nil {
		return nil, errors.Join(err, root.Close())
	}
	return root, nil
}

func openAndSyncVMRuntimeJournalDir(root *os.File, deps vmRuntimeJournalStoreDeps) (*os.File, error) {
	dir, created, err := openVMRuntimeJournalDir(root, deps.uid)
	if err != nil {
		return nil, err
	}
	if created {
		if err := deps.syncDir(root); err != nil {
			return nil, errors.Join(fmt.Errorf("sync VM runtime journal directory creation: %w", err), dir.Close())
		}
	}
	return dir, nil
}

func openAndLockVMRuntimeJournal(ctx context.Context, root *os.File, deps vmRuntimeJournalStoreDeps) (*os.File, error) {
	lock, err := openVMRuntimeJournalLockAt(root, deps.uid)
	if err != nil {
		return nil, err
	}
	if err := deps.syncDir(root); err != nil {
		return nil, errors.Join(fmt.Errorf("sync VM runtime transaction lock creation: %w", err), lock.Close())
	}
	if err := acquireVMRuntimeJournalLock(ctx, lock, deps.flock); err != nil {
		return nil, errors.Join(fmt.Errorf("lock VM runtime transactions: %w", err), lock.Close())
	}
	return lock, nil
}

func closeVMRuntimeJournalStoreFiles(files ...*os.File) error {
	var retErr error
	for _, file := range files {
		if file != nil {
			retErr = errors.Join(retErr, file.Close())
		}
	}
	return retErr
}

func validateVMRuntimeJournalRoot(root *os.File, uid uint32) error {
	var stat unix.Stat_t
	if err := unix.Fstat(int(root.Fd()), &stat); err != nil {
		return fmt.Errorf("inspect VM runtime journal root: %w", err)
	}
	if uint32(stat.Mode)&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uid || uint32(stat.Mode)&0o022 != 0 {
		return fmt.Errorf("VM runtime journal root must be an owner-controlled directory for UID %d", uid)
	}
	return nil
}

func openVMRuntimeJournalDir(root *os.File, uid uint32) (*os.File, bool, error) {
	created := false
	if err := unix.Mkdirat(int(root.Fd()), vmRuntimeJournalDirName, 0o700); err == nil {
		created = true
	} else if !errors.Is(err, unix.EEXIST) {
		return nil, false, fmt.Errorf("create VM runtime journal directory: %w", err)
	}
	fd, err := unix.Openat(int(root.Fd()), vmRuntimeJournalDirName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, false, fmt.Errorf("open VM runtime journal directory without following symlinks: %w", err)
	}
	dir := os.NewFile(uintptr(fd), filepath.Join(root.Name(), vmRuntimeJournalDirName))
	if dir == nil {
		return nil, false, errors.Join(fmt.Errorf("bind VM runtime journal directory descriptor"), wrapVMRuntimeJournalError("close unbound VM runtime journal directory descriptor", unix.Close(fd)))
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, false, closeVMJailerFileOnError(dir, fmt.Errorf("inspect VM runtime journal directory: %w", err))
	}
	permissions := uint32(stat.Mode) & 0o7777
	if uint32(stat.Mode)&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uid || permissions != 0o700 && permissions != 0o2700 {
		return nil, false, closeVMJailerFileOnError(dir, fmt.Errorf("VM runtime journal directory must be owned by UID %d with permissions 0700 or 02700", uid))
	}
	return dir, created, nil
}

type vmRuntimeJournalFileMetadata struct {
	mode uint32
	uid  uint32
	gid  uint32
}

func validateVMRuntimeJournalLockMetadata(metadata vmRuntimeJournalFileMetadata, expectedUID uint32) error {
	if metadata.mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("VM runtime transaction lock is not a regular file")
	}
	if metadata.uid != expectedUID {
		return fmt.Errorf("VM runtime transaction lock owner UID is %d, want %d", metadata.uid, expectedUID)
	}
	if metadata.mode&0o777 != 0o600 {
		return fmt.Errorf("VM runtime transaction lock permissions are %o, want 0600", metadata.mode&0o777)
	}
	return nil
}

func openVMRuntimeJournalLockAt(root *os.File, uid uint32) (*os.File, error) {
	fd, err := unix.Openat(int(root.Fd()), vmRuntimeJournalLockFileName, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open VM runtime transaction lock without following symlinks: %w", err)
	}
	lock := os.NewFile(uintptr(fd), filepath.Join(root.Name(), vmRuntimeJournalLockFileName))
	if lock == nil {
		return nil, errors.Join(fmt.Errorf("bind VM runtime transaction lock descriptor"), wrapVMRuntimeJournalError("close unbound VM runtime transaction lock descriptor", unix.Close(fd)))
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, closeVMJailerFileOnError(lock, fmt.Errorf("inspect VM runtime transaction lock: %w", err))
	}
	if err := validateVMRuntimeJournalLockMetadata(vmRuntimeJournalFileMetadata{mode: uint32(stat.Mode), uid: stat.Uid, gid: stat.Gid}, uid); err != nil {
		return nil, closeVMJailerFileOnError(lock, err)
	}
	return lock, nil
}

func acquireVMRuntimeJournalLock(ctx context.Context, lock *os.File, flock func(int, int) error) error {
	for {
		err := flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (store *vmRuntimeJournalStore) Close() error {
	if store == nil {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return nil
	}
	store.closed = true
	return errors.Join(
		wrapVMRuntimeJournalError("unlock VM runtime transactions", store.deps.flock(int(store.lock.Fd()), unix.LOCK_UN)),
		wrapVMRuntimeJournalError("close VM runtime transaction lock", store.lock.Close()),
		wrapVMRuntimeJournalError("close VM runtime journal directory", store.dir.Close()),
		wrapVMRuntimeJournalError("close VM runtime journal root", store.root.Close()),
	)
}

func (store *vmRuntimeJournalStore) Prepare(records []vmRuntimeJournalRecord) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.checkOpen(); err != nil {
		return err
	}
	records = cloneVMRuntimeJournalRecords(records)
	if err := validateVMRuntimeJournalCohort(records); err != nil {
		return fmt.Errorf("validate prepared VM runtime journal cohort: %w", err)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Service < records[j].Service })
	if records[0].State != vmRuntimeJournalStatePrepared {
		return fmt.Errorf("new VM runtime journal cohort must be in prepared state")
	}
	inv, err := store.loadInventoryLocked()
	if err != nil {
		return err
	}
	id := records[0].TransactionID
	if existing, ok := inv.markers[id]; ok {
		return store.prepareExistingVMRuntimeJournal(id, records, existing.marker)
	}
	if err := rejectVMRuntimeJournalTargetOverlap(inv, records); err != nil {
		return err
	}
	marker, err := store.newVMRuntimeJournalMarker(records, vmRuntimeJournalOperationPrepare, vmRuntimeJournalPhasePublishing, "", vmRuntimeJournalStatePrepared, records[0].UpdatedAt, nil, records)
	if err != nil {
		return err
	}
	if err := store.publishNewMarker(marker); err != nil {
		return err
	}
	return store.resumePrepare(marker)
}

func (store *vmRuntimeJournalStore) prepareExistingVMRuntimeJournal(id string, records []vmRuntimeJournalRecord, marker vmRuntimeJournalMarker) error {
	if marker.Operation == vmRuntimeJournalOperationStable && reflect.DeepEqual(marker.Desired, records) {
		return store.revalidateStable(marker)
	}
	if marker.Operation != vmRuntimeJournalOperationPrepare || !reflect.DeepEqual(marker.Desired, records) {
		return store.retainedError(fmt.Errorf("VM runtime journal transaction %s already has a different durable intent", id), marker)
	}
	return store.resumePrepare(marker)
}

// Resume continues a durable prepare or transition intent. Removal is resumed
// only through BeginRemoval and committed explicitly after a fresh agreement check.
func (store *vmRuntimeJournalStore) Resume(transactionID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.checkOpen(); err != nil {
		return err
	}
	inv, err := store.loadInventoryLocked()
	if err != nil {
		return err
	}
	loaded, ok := inv.markers[transactionID]
	if !ok {
		return fmt.Errorf("VM runtime journal transaction %s was not found", transactionID)
	}
	switch loaded.marker.Operation {
	case vmRuntimeJournalOperationStable:
		return store.revalidateStable(loaded.marker)
	case vmRuntimeJournalOperationPrepare:
		return store.resumePrepare(loaded.marker)
	case vmRuntimeJournalOperationTransition:
		return store.resumeTransition(loaded.marker)
	case vmRuntimeJournalOperationRemove:
		return &vmRuntimeJournalRemovalPendingError{TransactionID: transactionID}
	default:
		return store.retainedError(fmt.Errorf("unsupported VM runtime journal operation %q", loaded.marker.Operation), loaded.marker)
	}
}

func (store *vmRuntimeJournalStore) resumePrepare(marker vmRuntimeJournalMarker) error {
	if err := store.syncExpectedMarker(vmRuntimeJournalMarkerName(marker.TransactionID), marker); err != nil {
		return store.retainedError(fmt.Errorf("sync durable prepare marker before member publication: %w", err), marker)
	}
	for _, record := range marker.Desired {
		if err := store.resumePreparedVMRuntimeJournalRecord(record, marker); err != nil {
			return err
		}
	}
	stable, err := stableVMRuntimeJournalMarker(marker, marker.Desired, marker.UpdatedAt)
	if err != nil {
		return store.retainedError(err, marker)
	}
	return store.replaceMarker(marker, stable)
}

func (store *vmRuntimeJournalStore) resumePreparedVMRuntimeJournalRecord(record vmRuntimeJournalRecord, marker vmRuntimeJournalMarker) error {
	state, err := store.inspectRecordPath(record.Service, record)
	if err != nil {
		return store.retainedError(err, marker)
	}
	switch state {
	case vmRuntimeJournalPathDesired:
		if err := store.cleanupBoundStage(record, marker); err != nil {
			return err
		}
		if err := store.syncExpectedRecord(record.Service+".json", record); err != nil {
			return store.retainedError(fmt.Errorf("sync idempotent VM runtime journal %s: %w", record.Service, err), marker)
		}
		return nil
	case vmRuntimeJournalPathAbsent:
		return store.publishNewRecord(record, marker)
	default:
		return store.retainedError(fmt.Errorf("VM runtime journal %s contradicts its durable prepare intent", record.Service), marker)
	}
}

func (store *vmRuntimeJournalStore) revalidateStable(marker vmRuntimeJournalMarker) error {
	for _, record := range marker.Desired {
		if err := store.syncExpectedRecord(record.Service+".json", record); err != nil {
			return store.retainedError(fmt.Errorf("sync stable VM runtime journal %s: %w", record.Service, err), marker)
		}
	}
	if err := store.syncExpectedMarker(vmRuntimeJournalMarkerName(marker.TransactionID), marker); err != nil {
		return store.retainedError(fmt.Errorf("sync stable VM runtime journal marker: %w", err), marker)
	}
	return nil
}

func (store *vmRuntimeJournalStore) Transition(transactionID string, next vmRuntimeJournalState, at time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.checkOpen(); err != nil {
		return err
	}
	inv, err := store.loadInventoryLocked()
	if err != nil {
		return err
	}
	loaded, ok := inv.markers[transactionID]
	if !ok {
		return fmt.Errorf("VM runtime journal transaction %s was not found", transactionID)
	}
	marker := loaded.marker
	if marker.Operation == vmRuntimeJournalOperationTransition {
		return store.resumeExistingVMRuntimeJournalTransition(transactionID, next, at, marker)
	}
	if marker.Operation != vmRuntimeJournalOperationStable {
		return store.retainedError(fmt.Errorf("VM runtime journal transaction %s is in %s phase", transactionID, marker.Phase), marker)
	}
	return store.beginVMRuntimeJournalTransition(marker, next, at)
}

func (store *vmRuntimeJournalStore) resumeExistingVMRuntimeJournalTransition(transactionID string, next vmRuntimeJournalState, at time.Time, marker vmRuntimeJournalMarker) error {
	if marker.TargetState != next || !marker.UpdatedAt.Equal(at) {
		return store.retainedError(fmt.Errorf("VM runtime journal transaction %s already has a different transition intent", transactionID), marker)
	}
	return store.resumeTransition(marker)
}

func (store *vmRuntimeJournalStore) beginVMRuntimeJournalTransition(marker vmRuntimeJournalMarker, next vmRuntimeJournalState, at time.Time) error {
	current := marker.Desired[0].State
	if current == next {
		return store.revalidateStable(marker)
	}
	if !legalVMRuntimeJournalTransition(current, next) {
		return fmt.Errorf("illegal VM runtime journal transition from %s to %s", current, next)
	}
	if at.Location() != time.UTC || !at.After(marker.Desired[0].UpdatedAt) {
		return fmt.Errorf("VM runtime journal transition timestamp must be UTC and later than the current timestamp")
	}
	desired := cloneVMRuntimeJournalRecords(marker.Desired)
	for i := range desired {
		desired[i].State = next
		desired[i].UpdatedAt = at
	}
	intent, err := store.newVMRuntimeJournalMarker(desired, vmRuntimeJournalOperationTransition, vmRuntimeJournalPhaseTransitioning, current, next, at, marker.Desired, desired)
	if err != nil {
		return store.retainedError(err, marker)
	}
	if err := store.replaceMarker(marker, intent); err != nil {
		return err
	}
	return store.resumeTransition(intent)
}

func (store *vmRuntimeJournalStore) resumeTransition(marker vmRuntimeJournalMarker) error {
	if err := store.syncExpectedMarker(vmRuntimeJournalMarkerName(marker.TransactionID), marker); err != nil {
		return store.retainedError(fmt.Errorf("sync durable transition marker before member publication: %w", err), marker)
	}
	for i, desired := range marker.Desired {
		if err := store.resumeVMRuntimeJournalTransitionRecord(marker.Previous[i], desired, marker); err != nil {
			return err
		}
	}
	stable, err := stableVMRuntimeJournalMarker(marker, marker.Desired, marker.UpdatedAt)
	if err != nil {
		return store.retainedError(err, marker)
	}
	return store.replaceMarker(marker, stable)
}

func (store *vmRuntimeJournalStore) resumeVMRuntimeJournalTransitionRecord(previous, desired vmRuntimeJournalRecord, marker vmRuntimeJournalMarker) error {
	state, err := store.inspectRecordAlternatives(desired.Service, previous, desired)
	if err != nil {
		return store.retainedError(err, marker)
	}
	switch state {
	case vmRuntimeJournalPathDesired:
		if err := store.cleanupBoundStage(desired, marker); err != nil {
			return err
		}
		if err := store.syncExpectedRecord(desired.Service+".json", desired); err != nil {
			return store.retainedError(fmt.Errorf("sync idempotent VM runtime journal transition for %s: %w", desired.Service, err), marker)
		}
		return nil
	case vmRuntimeJournalPathPrevious:
		return store.replaceRecord(previous, desired, marker)
	default:
		return store.retainedError(fmt.Errorf("VM runtime journal %s contradicts its durable transition intent", desired.Service), marker)
	}
}

func (store *vmRuntimeJournalStore) cleanupBoundStage(record vmRuntimeJournalRecord, marker vmRuntimeJournalMarker) error {
	plan, ok := vmRuntimeJournalPlanForService(marker, record.Service)
	if !ok {
		return store.retainedError(fmt.Errorf("missing marker plan for %s", record.Service), marker)
	}
	entry, err := store.inspectExpectedRecord(plan.StageName, record)
	if err != nil {
		return store.retainedError(err, marker)
	}
	if !entry.present {
		return nil
	}
	if err := unlinkVMJailerNameIfIdentityWith(store.dir, plan.StageName, entry.identity, store.deps.unlinkAt); err != nil {
		return store.retainedError(fmt.Errorf("remove consumed VM runtime journal stage %s: %w", record.Service, err), marker)
	}
	if err := store.deps.syncDir(store.dir); err != nil {
		return store.retainedError(fmt.Errorf("sync consumed VM runtime journal stage %s: %w", record.Service, err), marker)
	}
	return nil
}

func legalVMRuntimeJournalTransition(current, next vmRuntimeJournalState) bool {
	return current == vmRuntimeJournalStatePrepared && next == vmRuntimeJournalStateDerivedPublished ||
		current == vmRuntimeJournalStateDerivedPublished && next == vmRuntimeJournalStateDatabaseCommitted
}

func (store *vmRuntimeJournalStore) BeginRemoval(transactionID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.checkOpen(); err != nil {
		return err
	}
	inv, err := store.loadInventoryLocked()
	if err != nil {
		return err
	}
	loaded, ok := inv.markers[transactionID]
	if !ok {
		return fmt.Errorf("VM runtime journal transaction %s was not found", transactionID)
	}
	marker, err := store.ensureVMRuntimeJournalRemovalIntent(transactionID, loaded.marker)
	if err != nil {
		return err
	}
	return store.resumeVMRuntimeJournalRemoval(transactionID, marker)
}

func (store *vmRuntimeJournalStore) ensureVMRuntimeJournalRemovalIntent(transactionID string, marker vmRuntimeJournalMarker) (vmRuntimeJournalMarker, error) {
	if marker.Operation == vmRuntimeJournalOperationStable {
		intent, err := store.newVMRuntimeJournalMarker(marker.Desired, vmRuntimeJournalOperationRemove, vmRuntimeJournalPhaseRemoving, marker.Desired[0].State, "", time.Now().UTC(), marker.Desired, nil)
		if err != nil {
			return marker, store.retainedError(err, marker)
		}
		if err := store.replaceMarker(marker, intent); err != nil {
			return marker, err
		}
		return intent, nil
	}
	if marker.Operation != vmRuntimeJournalOperationRemove {
		return marker, store.retainedError(fmt.Errorf("VM runtime journal transaction %s is in %s phase", transactionID, marker.Phase), marker)
	}
	return marker, nil
}

func (store *vmRuntimeJournalStore) resumeVMRuntimeJournalRemoval(transactionID string, marker vmRuntimeJournalMarker) error {
	switch marker.Phase {
	case vmRuntimeJournalPhaseRemoving:
		return store.tombstoneVMRuntimeJournalRemoval(transactionID, marker)
	case vmRuntimeJournalPhaseTombstoned:
		return store.revalidateVMRuntimeJournalRemoval(transactionID, marker)
	default:
		return store.retainedError(fmt.Errorf("unsupported recoverable removal phase %s", marker.Phase), marker)
	}
}

func (store *vmRuntimeJournalStore) tombstoneVMRuntimeJournalRemoval(transactionID string, marker vmRuntimeJournalMarker) error {
	if err := store.syncExpectedMarker(vmRuntimeJournalMarkerName(transactionID), marker); err != nil {
		return store.retainedError(fmt.Errorf("sync durable removal marker before tombstoning members: %w", err), marker)
	}
	for _, record := range marker.Previous {
		if err := store.tombstoneRecord(record, marker); err != nil {
			return err
		}
	}
	tombstoned := marker
	tombstoned.Phase = vmRuntimeJournalPhaseTombstoned
	tombstoned.UpdatedAt = time.Now().UTC()
	if err := store.replaceMarker(marker, tombstoned); err != nil {
		return err
	}
	return &vmRuntimeJournalRemovalPendingError{TransactionID: transactionID}
}

func (store *vmRuntimeJournalStore) revalidateVMRuntimeJournalRemoval(transactionID string, marker vmRuntimeJournalMarker) error {
	for _, record := range marker.Previous {
		plan, _ := vmRuntimeJournalPlanForService(marker, record.Service)
		if err := store.syncExpectedRecord(plan.TombstoneName, record); err != nil {
			return store.retainedError(fmt.Errorf("sync durably tombstoned VM runtime journal %s: %w", record.Service, err), marker)
		}
	}
	if err := store.syncExpectedMarker(vmRuntimeJournalMarkerName(transactionID), marker); err != nil {
		return store.retainedError(fmt.Errorf("sync durably tombstoned VM runtime journal marker: %w", err), marker)
	}
	return &vmRuntimeJournalRemovalPendingError{TransactionID: transactionID}
}

// CommitRemoval is intentionally separate from BeginRemoval. Its caller must
// perform the fresh DB and derived-file agreement check immediately before
// invoking this irreversible marker commit.
func (store *vmRuntimeJournalStore) CommitRemoval(transactionID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.checkOpen(); err != nil {
		return err
	}
	inv, err := store.loadInventoryLocked()
	if err != nil {
		return err
	}
	loaded, ok := inv.markers[transactionID]
	if !ok {
		return store.cleanupUnreferencedTombstones(inv, transactionID)
	}
	marker := loaded.marker
	if marker.Operation != vmRuntimeJournalOperationRemove || marker.Phase != vmRuntimeJournalPhaseTombstoned {
		return store.retainedError(fmt.Errorf("VM runtime journal transaction %s is not durably tombstoned for commit", transactionID), marker)
	}
	if err := store.commitMarkerRemoval(marker); err != nil {
		return err
	}
	for _, record := range marker.Previous {
		if err := store.removeRecordTombstone(record, marker); err != nil {
			return err
		}
	}
	return nil
}

// FinalizeRemovalWithLatestDataLocked performs the irreversible marker commit
// while the latest database view is still protected by the database's stable
// writer lock. Keeping this operation journal-owned preserves the global lock
// order: journal mutex, Store mutex, database lock, marker unlink and fsync.
// Record tombstones are cleanup debris once the marker commit succeeds and are
// therefore removed only after the database lock has been released.
func (store *vmRuntimeJournalStore) FinalizeRemovalWithLatestDataLocked(
	transactionID string,
	database *db.Store,
	expected vmRuntimeJournalDBClassification,
	verify func([]vmRuntimeJournalRecord) error,
) error {
	if err := validateVMRuntimeJournalFinalizationArgs(database, expected, verify); err != nil {
		return err
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.checkOpen(); err != nil {
		return err
	}
	inv, err := store.loadInventoryLocked()
	if err != nil {
		return err
	}
	loaded, ok := inv.markers[transactionID]
	if !ok {
		return store.cleanupUnreferencedTombstones(inv, transactionID)
	}
	marker := loaded.marker
	if marker.Operation != vmRuntimeJournalOperationRemove || marker.Phase != vmRuntimeJournalPhaseTombstoned {
		return store.retainedError(fmt.Errorf("VM runtime journal transaction %s is not durably tombstoned for finalization", transactionID), marker)
	}
	records := cloneVMRuntimeJournalRecords(marker.Previous)
	group := vmRuntimeJournalGroup{TransactionID: transactionID, Members: append([]string(nil), marker.Members...), Records: records}
	markerCommitted, finalizeErr := store.commitRemovalWithLatestDataLocked(database, expected, verify, group, marker, records)
	if !markerCommitted {
		return finalizeErr
	}

	var cleanupErr error
	for _, record := range marker.Previous {
		cleanupErr = errors.Join(cleanupErr, store.removeRecordTombstone(record, marker))
	}
	return errors.Join(finalizeErr, cleanupErr)
}

func validateVMRuntimeJournalFinalizationArgs(
	database *db.Store,
	expected vmRuntimeJournalDBClassification,
	verify func([]vmRuntimeJournalRecord) error,
) error {
	if database == nil {
		return fmt.Errorf("VM runtime journal finalization requires a database")
	}
	if expected != vmRuntimeJournalDBOld && expected != vmRuntimeJournalDBNew {
		return fmt.Errorf("VM runtime journal finalization requires an exact old or new database classification")
	}
	if verify == nil {
		return fmt.Errorf("VM runtime journal finalization requires a derived-state verifier")
	}
	return nil
}

func (store *vmRuntimeJournalStore) commitRemovalWithLatestDataLocked(
	database *db.Store,
	expected vmRuntimeJournalDBClassification,
	verify func([]vmRuntimeJournalRecord) error,
	group vmRuntimeJournalGroup,
	marker vmRuntimeJournalMarker,
	records []vmRuntimeJournalRecord,
) (bool, error) {
	markerCommitted := false
	err := database.WithLatestDataLocked(func(view db.DataView) error {
		if classification := group.ClassifyDB(view.AsStruct()); classification != expected {
			return fmt.Errorf("VM runtime journal database classification is %s, want %s", classification, expected)
		}
		if err := verify(cloneVMRuntimeJournalRecords(records)); err != nil {
			return fmt.Errorf("verify VM runtime journal derived state: %w", err)
		}
		if err := store.commitMarkerRemoval(marker); err != nil {
			return err
		}
		markerCommitted = true
		return nil
	})
	return markerCommitted, err
}

// CleanupCommittedTombstones removes record tombstones whose transaction
// marker is already absent. Marker absence is the durable commit boundary, so
// this cleanup is safe and retryable without another database agreement check.
func (store *vmRuntimeJournalStore) CleanupCommittedTombstones() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.checkOpen(); err != nil {
		return err
	}
	inv, err := store.loadInventoryLocked()
	if err != nil {
		return err
	}
	transactionIDs := make(map[string]struct{})
	for _, entry := range inv.tombstones {
		if _, active := inv.markers[entry.record.TransactionID]; !active {
			transactionIDs[entry.record.TransactionID] = struct{}{}
		}
	}
	ids := make([]string, 0, len(transactionIDs))
	for id := range transactionIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var retErr error
	for _, id := range ids {
		retErr = errors.Join(retErr, store.cleanupUnreferencedTombstones(inv, id))
	}
	return retErr
}

func (store *vmRuntimeJournalStore) tombstoneRecord(record vmRuntimeJournalRecord, marker vmRuntimeJournalMarker) error {
	canonical := record.Service + ".json"
	plan, ok := vmRuntimeJournalPlanForService(marker, record.Service)
	if !ok {
		return store.retainedError(fmt.Errorf("missing marker plan for %s", record.Service), marker)
	}
	tombstone := plan.TombstoneName
	canonicalEntry, tombstoneEntry, err := store.inspectVMRuntimeJournalTombstoneEntries(canonical, tombstone, record, marker)
	if err != nil {
		return err
	}
	if canonicalEntry.present && tombstoneEntry.present {
		return store.retainedError(fmt.Errorf("VM runtime journal %s has both canonical and tombstone entries", record.Service), marker)
	}
	if tombstoneEntry.present {
		if err := store.syncExpectedRecord(tombstone, record); err != nil {
			return store.retainedError(fmt.Errorf("sync resumed VM runtime journal tombstone %s: %w", record.Service, err), marker)
		}
		return nil
	}
	if !canonicalEntry.present {
		return store.retainedError(fmt.Errorf("VM runtime journal %s is missing both canonical and tombstone entries", record.Service), marker)
	}
	return store.publishVMRuntimeJournalTombstone(canonical, tombstone, record, marker)
}

func (store *vmRuntimeJournalStore) inspectVMRuntimeJournalTombstoneEntries(canonical, tombstone string, record vmRuntimeJournalRecord, marker vmRuntimeJournalMarker) (vmRuntimeJournalLoadedRecord, vmRuntimeJournalLoadedRecord, error) {
	canonicalEntry, canonicalErr := store.inspectExpectedRecord(canonical, record)
	tombstoneEntry, tombstoneErr := store.inspectExpectedRecord(tombstone, record)
	if canonicalErr != nil || tombstoneErr != nil {
		return canonicalEntry, tombstoneEntry, store.retainedError(errors.Join(canonicalErr, tombstoneErr), marker)
	}
	return canonicalEntry, tombstoneEntry, nil
}

func (store *vmRuntimeJournalStore) publishVMRuntimeJournalTombstone(canonical, tombstone string, record vmRuntimeJournalRecord, marker vmRuntimeJournalMarker) error {
	if err := store.deps.renameNoReplaceAt(int(store.dir.Fd()), canonical, int(store.dir.Fd()), tombstone); err != nil {
		return store.retainedError(fmt.Errorf("tombstone VM runtime journal %s: %w", record.Service, err), marker)
	}
	if _, err := store.requireExpectedRecord(tombstone, record); err != nil {
		return store.retainedError(fmt.Errorf("validate VM runtime journal tombstone %s: %w", record.Service, err), marker)
	}
	if err := store.deps.syncDir(store.dir); err != nil {
		return store.retainedError(fmt.Errorf("sync VM runtime journal tombstone %s: %w", record.Service, err), marker)
	}
	return nil
}

func (store *vmRuntimeJournalStore) removeRecordTombstone(record vmRuntimeJournalRecord, marker vmRuntimeJournalMarker) error {
	plan, ok := vmRuntimeJournalPlanForService(marker, record.Service)
	if !ok {
		return store.retainedError(fmt.Errorf("missing marker plan for %s", record.Service), marker)
	}
	name := plan.TombstoneName
	entry, err := store.inspectExpectedRecord(name, record)
	if err != nil {
		return store.retainedError(err, marker)
	}
	if !entry.present {
		if err := store.deps.syncDir(store.dir); err != nil {
			return store.retainedError(fmt.Errorf("sync absent VM runtime journal tombstone %s: %w", record.Service, err), marker)
		}
		return nil
	}
	if err := unlinkVMJailerNameIfIdentityWith(store.dir, name, entry.identity, store.deps.unlinkAt); err != nil {
		return store.retainedError(fmt.Errorf("remove VM runtime journal tombstone %s: %w", record.Service, err), marker)
	}
	if err := store.deps.syncDir(store.dir); err != nil {
		return store.retainedError(fmt.Errorf("sync VM runtime journal tombstone removal %s: %w", record.Service, err), marker)
	}
	return nil
}

func (store *vmRuntimeJournalStore) commitMarkerRemoval(marker vmRuntimeJournalMarker) error {
	canonical := vmRuntimeJournalMarkerName(marker.TransactionID)
	entry, err := store.inspectExpectedMarker(canonical, marker)
	if err != nil {
		return store.retainedError(err, marker)
	}
	if !entry.present {
		return nil
	}
	if err := unlinkVMJailerNameIfIdentityWith(store.dir, canonical, entry.identity, store.deps.unlinkAt); err != nil {
		return store.retainedError(fmt.Errorf("commit VM runtime journal marker removal: %w", err), marker)
	}
	if err := store.deps.syncDir(store.dir); err != nil {
		return store.retainedError(fmt.Errorf("sync committed VM runtime journal marker removal: %w", err), marker)
	}
	return nil
}

func (store *vmRuntimeJournalStore) cleanupUnreferencedTombstones(inv vmRuntimeJournalInventory, transactionID string) error {
	// A prior marker or tombstone unlink may have become visible before its
	// directory sync failed. Make the complete observed state durable even when
	// no matching tombstone remains, then clean any debris identity-safely.
	if err := store.deps.syncDir(store.dir); err != nil {
		return fmt.Errorf("sync committed VM runtime journal absence before tombstone cleanup: %w", err)
	}
	var retErr error
	for name, entry := range inv.tombstones {
		if entry.record.TransactionID != transactionID {
			continue
		}
		if err := unlinkVMJailerNameIfIdentityWith(store.dir, name, entry.identity, store.deps.unlinkAt); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("remove unreferenced VM runtime journal tombstone %s: %w", name, err))
			continue
		}
		retErr = errors.Join(retErr, wrapVMRuntimeJournalError("sync unreferenced VM runtime journal tombstone cleanup", store.deps.syncDir(store.dir)))
	}
	return retErr
}

func (store *vmRuntimeJournalStore) LoadAll() ([]vmRuntimeJournalGroup, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.checkOpen(); err != nil {
		return nil, err
	}
	inv, err := store.loadInventoryLocked()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(inv.markers))
	for id := range inv.markers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	groups := make([]vmRuntimeJournalGroup, 0, len(ids))
	for _, id := range ids {
		marker := inv.markers[id].marker
		records := marker.Desired
		if marker.Operation == vmRuntimeJournalOperationRemove {
			records = marker.Previous
		}
		tombstones := make([]string, 0, len(records))
		for _, record := range records {
			plan, _ := vmRuntimeJournalPlanForService(marker, record.Service)
			name := plan.TombstoneName
			if entry, ok := inv.tombstones[name]; ok && entry.present {
				tombstones = append(tombstones, filepath.Join(store.rootDir, vmRuntimeJournalDirName, name))
			}
		}
		groups = append(groups, vmRuntimeJournalGroup{
			TransactionID: id,
			Members:       append([]string(nil), marker.Members...),
			Records:       cloneVMRuntimeJournalRecords(records),
			Phase:         marker.Phase,
			Tombstones:    tombstones,
		})
	}
	return groups, nil
}

type vmRuntimeJournalLoadedMarker struct {
	marker   vmRuntimeJournalMarker
	identity vmJailerFileIdentity
}

type vmRuntimeJournalLoadedRecord struct {
	record    vmRuntimeJournalRecord
	identity  vmJailerFileIdentity
	present   bool
	decodeErr error
}

type vmRuntimeJournalInventory struct {
	markers    map[string]vmRuntimeJournalLoadedMarker
	canonicals map[string]vmRuntimeJournalLoadedRecord
	tombstones map[string]vmRuntimeJournalLoadedRecord
	stages     map[string]vmRuntimeJournalLoadedRecord
}

type vmRuntimeJournalBudget struct {
	raw     int64
	decoded int64
}

func (store *vmRuntimeJournalStore) loadInventoryLocked() (vmRuntimeJournalInventory, error) {
	if _, err := store.dir.Seek(0, io.SeekStart); err != nil {
		return vmRuntimeJournalInventory{}, fmt.Errorf("rewind VM runtime journal directory: %w", err)
	}
	names, err := store.dir.Readdirnames(store.deps.maxEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return vmRuntimeJournalInventory{}, fmt.Errorf("list VM runtime journals: %w", err)
	}
	if len(names) > store.deps.maxEntries {
		return vmRuntimeJournalInventory{}, fmt.Errorf("VM runtime journal directory has %d entries, limit %d", len(names), store.deps.maxEntries)
	}
	sort.Strings(names)
	inv := vmRuntimeJournalInventory{
		markers: make(map[string]vmRuntimeJournalLoadedMarker), canonicals: make(map[string]vmRuntimeJournalLoadedRecord), tombstones: make(map[string]vmRuntimeJournalLoadedRecord), stages: make(map[string]vmRuntimeJournalLoadedRecord),
	}
	budget := &vmRuntimeJournalBudget{}
	for _, name := range names {
		if err := store.loadVMRuntimeJournalInventoryEntry(name, &inv, budget); err != nil {
			return vmRuntimeJournalInventory{}, err
		}
		if budget.decoded > store.deps.maxAggregateDecoded {
			return vmRuntimeJournalInventory{}, fmt.Errorf("VM runtime journal aggregate decoded payload exceeds %d", store.deps.maxAggregateDecoded)
		}
	}
	if err := validateVMRuntimeJournalInventory(inv); err != nil {
		return vmRuntimeJournalInventory{}, err
	}
	return inv, nil
}

func (store *vmRuntimeJournalStore) loadVMRuntimeJournalInventoryEntry(name string, inv *vmRuntimeJournalInventory, budget *vmRuntimeJournalBudget) error {
	metadata, identity, err := store.inspectJournalEntry(name)
	if err != nil {
		return err
	}
	budget.raw += metadata.size
	if budget.raw > store.deps.maxAggregateRaw {
		return fmt.Errorf("VM runtime journal aggregate raw bytes exceed %d", store.deps.maxAggregateRaw)
	}
	switch {
	case isVMRuntimeJournalMarkerName(name):
		return store.loadVMRuntimeJournalMarker(name, metadata, identity, inv, budget)
	case isVMRuntimeJournalRecordTombstoneName(name):
		return store.loadVMRuntimeJournalTombstone(name, metadata, identity, inv, budget)
	case isVMRuntimeJournalRecordStageName(name):
		return store.loadVMRuntimeJournalStage(name, metadata, identity, inv, budget)
	case isVMRuntimeJournalRecognizedDebrisName(name):
		return nil
	case strings.HasPrefix(name, "."):
		return fmt.Errorf("unknown hidden VM runtime journal entry %s", name)
	case strings.HasSuffix(name, ".json"):
		return store.loadVMRuntimeJournalCanonical(name, metadata, identity, inv, budget)
	default:
		return fmt.Errorf("unexpected entry in VM runtime journal directory: %s", name)
	}
}

func (store *vmRuntimeJournalStore) loadVMRuntimeJournalMarker(name string, metadata vmRuntimeJournalEntryMetadata, identity vmJailerFileIdentity, inv *vmRuntimeJournalInventory, budget *vmRuntimeJournalBudget) error {
	raw, err := store.readEntry(name, identity, metadata, store.deps.maxMarkerSize)
	if err != nil {
		return err
	}
	marker, err := decodeVMRuntimeJournalMarker(raw)
	if err != nil {
		return fmt.Errorf("decode VM runtime journal marker %s: %w", name, err)
	}
	if name != vmRuntimeJournalMarkerName(marker.TransactionID) {
		return fmt.Errorf("VM runtime journal marker filename does not match transaction %s", marker.TransactionID)
	}
	budget.decoded += vmRuntimeJournalMarkerDecodedBytes(marker)
	inv.markers[marker.TransactionID] = vmRuntimeJournalLoadedMarker{marker: marker, identity: identity}
	return nil
}

func (store *vmRuntimeJournalStore) loadVMRuntimeJournalTombstone(name string, metadata vmRuntimeJournalEntryMetadata, identity vmJailerFileIdentity, inv *vmRuntimeJournalInventory, budget *vmRuntimeJournalBudget) error {
	service, _, ok := parseVMRuntimeJournalRecordTombstoneName(name)
	if !ok {
		return fmt.Errorf("invalid VM runtime journal tombstone filename %s", name)
	}
	record, err := store.loadVMRuntimeJournalRecord(name, metadata, identity, "tombstone")
	if err != nil {
		return err
	}
	if record.Service != service {
		return fmt.Errorf("VM runtime journal tombstone %s contradicts its contents", name)
	}
	budget.decoded += vmRuntimeJournalRecordDecodedBytes(record)
	inv.tombstones[name] = vmRuntimeJournalLoadedRecord{record: record, identity: identity, present: true}
	return nil
}

func (store *vmRuntimeJournalStore) loadVMRuntimeJournalStage(name string, metadata vmRuntimeJournalEntryMetadata, identity vmJailerFileIdentity, inv *vmRuntimeJournalInventory, budget *vmRuntimeJournalBudget) error {
	service, _, _ := parseVMRuntimeJournalRecordStageName(name)
	raw, err := store.readEntry(name, identity, metadata, store.deps.maxFileSize)
	if err != nil {
		return err
	}
	record, err := decodeVMRuntimeJournalRecord(raw)
	if err != nil {
		inv.stages[name] = vmRuntimeJournalLoadedRecord{identity: identity, present: true, decodeErr: fmt.Errorf("decode VM runtime journal stage %s: %w", name, err)}
		return nil
	}
	if record.Service != service {
		return fmt.Errorf("VM runtime journal stage %s contradicts its contents", name)
	}
	budget.decoded += vmRuntimeJournalRecordDecodedBytes(record)
	inv.stages[name] = vmRuntimeJournalLoadedRecord{record: record, identity: identity, present: true}
	return nil
}

func (store *vmRuntimeJournalStore) loadVMRuntimeJournalCanonical(name string, metadata vmRuntimeJournalEntryMetadata, identity vmJailerFileIdentity, inv *vmRuntimeJournalInventory, budget *vmRuntimeJournalBudget) error {
	service := strings.TrimSuffix(name, ".json")
	if err := serviceid.Validate(service); err != nil {
		return fmt.Errorf("invalid VM runtime journal filename %s: %w", name, err)
	}
	record, err := store.loadVMRuntimeJournalRecord(name, metadata, identity, "")
	if err != nil {
		return err
	}
	if record.Service != service {
		return fmt.Errorf("VM runtime journal filename service %q does not match record service %q", service, record.Service)
	}
	budget.decoded += vmRuntimeJournalRecordDecodedBytes(record)
	inv.canonicals[service] = vmRuntimeJournalLoadedRecord{record: record, identity: identity, present: true}
	return nil
}

func (store *vmRuntimeJournalStore) loadVMRuntimeJournalRecord(name string, metadata vmRuntimeJournalEntryMetadata, identity vmJailerFileIdentity, kind string) (vmRuntimeJournalRecord, error) {
	raw, err := store.readEntry(name, identity, metadata, store.deps.maxFileSize)
	if err != nil {
		return vmRuntimeJournalRecord{}, err
	}
	record, err := decodeVMRuntimeJournalRecord(raw)
	if err != nil {
		label := "journal"
		if kind != "" {
			label += " " + kind
		}
		return vmRuntimeJournalRecord{}, fmt.Errorf("decode VM runtime %s %s: %w", label, name, err)
	}
	return record, nil
}

type vmRuntimeJournalEntryMetadata struct {
	mode uint32
	uid  uint32
	gid  uint32
	size int64
}

func (store *vmRuntimeJournalStore) inspectJournalEntry(name string) (vmRuntimeJournalEntryMetadata, vmJailerFileIdentity, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(int(store.dir.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return vmRuntimeJournalEntryMetadata{}, vmJailerFileIdentity{}, fmt.Errorf("inspect VM runtime journal entry %s: %w", name, err)
	}
	metadata := vmRuntimeJournalEntryMetadata{mode: uint32(stat.Mode), uid: stat.Uid, gid: stat.Gid, size: stat.Size}
	if metadata.mode&unix.S_IFMT != unix.S_IFREG || metadata.uid != store.deps.uid || metadata.mode&0o777 != 0o600 || metadata.size < 0 {
		return vmRuntimeJournalEntryMetadata{}, vmJailerFileIdentity{}, fmt.Errorf("VM runtime journal entry %s must be a UID %d regular file with permissions 0600", name, store.deps.uid)
	}
	return metadata, vmJailerFileIdentity{dev: uint64(stat.Dev), ino: stat.Ino}, nil
}

func (store *vmRuntimeJournalStore) readEntry(name string, want vmJailerFileIdentity, metadata vmRuntimeJournalEntryMetadata, max int64) ([]byte, error) {
	if metadata.size > max {
		return nil, fmt.Errorf("VM runtime journal entry %s exceeds %d bytes", name, max)
	}
	file, err := store.openVMRuntimeJournalEntry(name)
	if err != nil {
		return nil, err
	}
	if err := validateOpenVMRuntimeJournalEntry(file, name, want, metadata); err != nil {
		return nil, closeVMJailerFileOnError(file, err)
	}
	raw, err := readAndCloseVMRuntimeJournalEntry(file, name, max)
	if err != nil {
		return nil, err
	}
	if err := store.revalidateVMRuntimeJournalEntry(name, want, metadata); err != nil {
		return nil, err
	}
	return raw, nil
}

func (store *vmRuntimeJournalStore) openVMRuntimeJournalEntry(name string) (*os.File, error) {
	fd, err := unix.Openat(int(store.dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open VM runtime journal entry %s without following symlinks: %w", name, err)
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		return nil, errors.Join(fmt.Errorf("bind VM runtime journal entry %s descriptor", name), wrapVMRuntimeJournalError("close unbound VM runtime journal entry descriptor", unix.Close(fd)))
	}
	return file, nil
}

func validateOpenVMRuntimeJournalEntry(file *os.File, name string, want vmJailerFileIdentity, metadata vmRuntimeJournalEntryMetadata) error {
	got, stat, err := vmJailerFileIdentityForFile(file)
	if err != nil {
		return err
	}
	if got != want || uint32(stat.Mode) != metadata.mode || stat.Uid != metadata.uid || stat.Gid != metadata.gid || stat.Size != metadata.size {
		return fmt.Errorf("VM runtime journal entry %s changed before read", name)
	}
	return nil
}

func readAndCloseVMRuntimeJournalEntry(file *os.File, name string, max int64) ([]byte, error) {
	raw, readErr := io.ReadAll(io.LimitReader(file, max+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(wrapVMRuntimeJournalError("read VM runtime journal entry "+name, readErr), wrapVMRuntimeJournalError("close VM runtime journal entry "+name, closeErr))
	}
	if int64(len(raw)) > max {
		return nil, fmt.Errorf("VM runtime journal entry %s exceeds %d bytes", name, max)
	}
	return raw, nil
}

func (store *vmRuntimeJournalStore) revalidateVMRuntimeJournalEntry(name string, want vmJailerFileIdentity, metadata vmRuntimeJournalEntryMetadata) error {
	gotMetadata, gotIdentity, err := store.inspectJournalEntry(name)
	if err != nil || gotIdentity != want || gotMetadata != metadata {
		return errors.Join(fmt.Errorf("VM runtime journal entry %s changed while read", name), err)
	}
	return nil
}

func validateVMRuntimeJournalInventory(inv vmRuntimeJournalInventory) error {
	owners, referencedStages, err := validateVMRuntimeJournalInventoryMarkers(inv)
	if err != nil {
		return err
	}
	for service := range inv.canonicals {
		if _, ok := owners[service]; !ok {
			return fmt.Errorf("VM runtime journal %s has no durable transaction marker", service)
		}
	}
	for name, stage := range inv.stages {
		if _, ok := referencedStages[name]; !ok && stage.decodeErr != nil {
			return stage.decodeErr
		}
	}
	return nil
}

func validateVMRuntimeJournalInventoryMarkers(inv vmRuntimeJournalInventory) (map[string]string, map[string]struct{}, error) {
	owners := make(map[string]string)
	descriptorTargets := make(map[string]string)
	unitTargets := make(map[string]string)
	referencedStages := make(map[string]struct{})
	for id, loaded := range inv.markers {
		marker := loaded.marker
		basis := marker.Desired
		if marker.Operation == vmRuntimeJournalOperationRemove {
			basis = marker.Previous
		}
		for _, record := range basis {
			if err := addVMRuntimeJournalInventoryOwner(owners, descriptorTargets, unitTargets, id, record); err != nil {
				return nil, nil, err
			}
		}
		if err := validateVMRuntimeJournalMarkerDisk(marker, inv); err != nil {
			return nil, nil, fmt.Errorf("invalid VM runtime journal transaction %s: %w", id, err)
		}
		for _, plan := range marker.Plans {
			referencedStages[plan.StageName] = struct{}{}
		}
	}
	return owners, referencedStages, nil
}

func addVMRuntimeJournalInventoryOwner(owners, descriptorTargets, unitTargets map[string]string, id string, record vmRuntimeJournalRecord) error {
	if owner, ok := owners[record.Service]; ok && owner != id {
		return fmt.Errorf("service %s is owned by contradictory VM runtime journal transactions", record.Service)
	}
	owners[record.Service] = id
	if owner, ok := descriptorTargets[record.NewDescriptor.Path]; ok && owner != record.Service {
		return fmt.Errorf("duplicate VM runtime descriptor target %s", record.NewDescriptor.Path)
	}
	descriptorTargets[record.NewDescriptor.Path] = record.Service
	if owner, ok := unitTargets[record.NewUnit.Path]; ok && owner != record.Service {
		return fmt.Errorf("duplicate VM runtime unit target %s", record.NewUnit.Path)
	}
	unitTargets[record.NewUnit.Path] = record.Service
	return nil
}

func validateVMRuntimeJournalMarkerDisk(marker vmRuntimeJournalMarker, inv vmRuntimeJournalInventory) error {
	switch marker.Operation {
	case vmRuntimeJournalOperationStable:
		return validateStableVMRuntimeJournalMarkerDisk(marker, inv)
	case vmRuntimeJournalOperationPrepare:
		return validatePreparingVMRuntimeJournalMarkerDisk(marker, inv)
	case vmRuntimeJournalOperationTransition:
		return validateTransitioningVMRuntimeJournalMarkerDisk(marker, inv)
	case vmRuntimeJournalOperationRemove:
		return validateRemovingVMRuntimeJournalMarkerDisk(marker, inv)
	}
	return nil
}

func validateStableVMRuntimeJournalMarkerDisk(marker vmRuntimeJournalMarker, inv vmRuntimeJournalInventory) error {
	for _, record := range marker.Desired {
		plan, _ := vmRuntimeJournalPlanForService(marker, record.Service)
		entry, ok := inv.canonicals[record.Service]
		if !ok || !reflect.DeepEqual(entry.record, record) {
			return fmt.Errorf("stable member %s is missing or contradictory", record.Service)
		}
		if _, ok := inv.tombstones[plan.TombstoneName]; ok {
			return fmt.Errorf("stable member %s has a tombstone", record.Service)
		}
		if _, ok := inv.stages[plan.StageName]; ok {
			return fmt.Errorf("stable member %s has a referenced stage", record.Service)
		}
	}
	return nil
}

func validatePreparingVMRuntimeJournalMarkerDisk(marker vmRuntimeJournalMarker, inv vmRuntimeJournalInventory) error {
	for _, desired := range marker.Desired {
		plan, _ := vmRuntimeJournalPlanForService(marker, desired.Service)
		canonical, hasCanonical := inv.canonicals[desired.Service]
		stage, hasStage := inv.stages[plan.StageName]
		if hasCanonical && !reflect.DeepEqual(canonical.record, desired) || hasStage && stage.decodeErr == nil && !reflect.DeepEqual(stage.record, desired) || hasCanonical && hasStage {
			return fmt.Errorf("publishing member %s contradicts marker", desired.Service)
		}
	}
	return nil
}

func validateTransitioningVMRuntimeJournalMarkerDisk(marker vmRuntimeJournalMarker, inv vmRuntimeJournalInventory) error {
	for i, desired := range marker.Desired {
		plan, _ := vmRuntimeJournalPlanForService(marker, desired.Service)
		entry, ok := inv.canonicals[desired.Service]
		if !ok || !reflect.DeepEqual(entry.record, desired) && !reflect.DeepEqual(entry.record, marker.Previous[i]) {
			return fmt.Errorf("transitioning member %s is missing or contradictory", desired.Service)
		}
		if stage, ok := inv.stages[plan.StageName]; ok {
			if stage.decodeErr == nil && !reflect.DeepEqual(stage.record, desired) || reflect.DeepEqual(entry.record, desired) {
				return fmt.Errorf("transitioning member %s has contradictory stage state", desired.Service)
			}
		}
	}
	return nil
}

func validateRemovingVMRuntimeJournalMarkerDisk(marker vmRuntimeJournalMarker, inv vmRuntimeJournalInventory) error {
	for _, previous := range marker.Previous {
		if err := validateRemovingVMRuntimeJournalMemberDisk(marker, inv, previous); err != nil {
			return err
		}
	}
	return nil
}

func validateRemovingVMRuntimeJournalMemberDisk(marker vmRuntimeJournalMarker, inv vmRuntimeJournalInventory, previous vmRuntimeJournalRecord) error {
	canonical, hasCanonical := inv.canonicals[previous.Service]
	plan, _ := vmRuntimeJournalPlanForService(marker, previous.Service)
	tombstone, hasTombstone := inv.tombstones[plan.TombstoneName]
	if removingVMRuntimeJournalStateContradicts(previous, canonical, hasCanonical, tombstone, hasTombstone) {
		return fmt.Errorf("removing member %s has contradictory canonical/tombstone state", previous.Service)
	}
	if err := validateRemovingVMRuntimeJournalPhaseState(marker.Phase, previous.Service, hasCanonical, hasTombstone); err != nil {
		return err
	}
	if _, ok := inv.stages[plan.StageName]; ok {
		return fmt.Errorf("removing member %s has a referenced stage", previous.Service)
	}
	return nil
}

func removingVMRuntimeJournalStateContradicts(previous vmRuntimeJournalRecord, canonical vmRuntimeJournalLoadedRecord, hasCanonical bool, tombstone vmRuntimeJournalLoadedRecord, hasTombstone bool) bool {
	return hasCanonical && hasTombstone ||
		hasCanonical && !reflect.DeepEqual(canonical.record, previous) ||
		hasTombstone && !reflect.DeepEqual(tombstone.record, previous)
}

func validateRemovingVMRuntimeJournalPhaseState(phase vmRuntimeJournalPhase, service string, hasCanonical, hasTombstone bool) error {
	if phase == vmRuntimeJournalPhaseRemoving && !hasCanonical && !hasTombstone {
		return fmt.Errorf("removing member %s is missing canonical and tombstone state", service)
	}
	if phase != vmRuntimeJournalPhaseTombstoned {
		return nil
	}
	if hasCanonical {
		return fmt.Errorf("tombstoned member %s still has a canonical journal", service)
	}
	if !hasTombstone {
		return fmt.Errorf("tombstoned member %s is missing its bound tombstone", service)
	}
	return nil
}

func rejectVMRuntimeJournalTargetOverlap(inv vmRuntimeJournalInventory, records []vmRuntimeJournalRecord) error {
	services := make(map[string]struct{})
	descriptors := make(map[string]struct{})
	units := make(map[string]struct{})
	for _, marker := range inv.markers {
		basis := marker.marker.Desired
		if marker.marker.Operation == vmRuntimeJournalOperationRemove {
			basis = marker.marker.Previous
		}
		for _, record := range basis {
			services[record.Service] = struct{}{}
			descriptors[record.NewDescriptor.Path] = struct{}{}
			units[record.NewUnit.Path] = struct{}{}
		}
	}
	for _, record := range records {
		if _, ok := services[record.Service]; ok {
			return fmt.Errorf("service %s already has a VM runtime journal transaction", record.Service)
		}
		if _, ok := descriptors[record.NewDescriptor.Path]; ok {
			return fmt.Errorf("duplicate VM runtime descriptor target %s", record.NewDescriptor.Path)
		}
		if _, ok := units[record.NewUnit.Path]; ok {
			return fmt.Errorf("duplicate VM runtime unit target %s", record.NewUnit.Path)
		}
	}
	return nil
}

type vmRuntimeJournalPathState int

const (
	vmRuntimeJournalPathAbsent vmRuntimeJournalPathState = iota
	vmRuntimeJournalPathPrevious
	vmRuntimeJournalPathDesired
	vmRuntimeJournalPathContradictory
)

func (store *vmRuntimeJournalStore) inspectRecordPath(service string, desired vmRuntimeJournalRecord) (vmRuntimeJournalPathState, error) {
	entry, err := store.inspectExpectedRecord(service+".json", desired)
	if err != nil {
		return vmRuntimeJournalPathContradictory, err
	}
	if !entry.present {
		return vmRuntimeJournalPathAbsent, nil
	}
	return vmRuntimeJournalPathDesired, nil
}

func (store *vmRuntimeJournalStore) inspectRecordAlternatives(service string, previous, desired vmRuntimeJournalRecord) (vmRuntimeJournalPathState, error) {
	name := service + ".json"
	entry, err := store.readRecordIfPresent(name)
	if err != nil {
		return vmRuntimeJournalPathContradictory, err
	}
	if !entry.present {
		return vmRuntimeJournalPathAbsent, nil
	}
	switch {
	case reflect.DeepEqual(entry.record, desired):
		return vmRuntimeJournalPathDesired, nil
	case reflect.DeepEqual(entry.record, previous):
		return vmRuntimeJournalPathPrevious, nil
	default:
		return vmRuntimeJournalPathContradictory, nil
	}
}

func (store *vmRuntimeJournalStore) inspectExpectedRecord(name string, expected vmRuntimeJournalRecord) (vmRuntimeJournalLoadedRecord, error) {
	entry, err := store.readRecordIfPresent(name)
	if err != nil || !entry.present {
		return entry, err
	}
	if !reflect.DeepEqual(entry.record, expected) {
		return entry, fmt.Errorf("VM runtime journal entry %s does not match durable intent", name)
	}
	return entry, nil
}

func (store *vmRuntimeJournalStore) requireExpectedRecord(name string, expected vmRuntimeJournalRecord) (vmRuntimeJournalLoadedRecord, error) {
	entry, err := store.inspectExpectedRecord(name, expected)
	if err != nil {
		return entry, err
	}
	if !entry.present {
		return entry, fmt.Errorf("VM runtime journal entry %s is absent", name)
	}
	return entry, nil
}

func (store *vmRuntimeJournalStore) syncExpectedRecord(name string, expected vmRuntimeJournalRecord) error {
	entry, err := store.requireExpectedRecord(name, expected)
	if err != nil {
		return err
	}
	if err := store.syncVerifiedEntry(name, entry.identity); err != nil {
		return err
	}
	after, err := store.requireExpectedRecord(name, expected)
	if err != nil {
		return err
	}
	if after.identity != entry.identity {
		return fmt.Errorf("VM runtime journal entry %s changed while syncing", name)
	}
	return nil
}

func (store *vmRuntimeJournalStore) syncExpectedMarker(name string, expected vmRuntimeJournalMarker) error {
	entry, err := store.inspectExpectedMarker(name, expected)
	if err != nil {
		return err
	}
	if !entry.present {
		return fmt.Errorf("VM runtime journal marker %s is absent", name)
	}
	if err := store.syncVerifiedEntry(name, entry.identity); err != nil {
		return err
	}
	after, err := store.inspectExpectedMarker(name, expected)
	if err != nil {
		return err
	}
	if !after.present || after.identity != entry.identity {
		return fmt.Errorf("VM runtime journal marker %s changed while syncing", name)
	}
	return nil
}

func (store *vmRuntimeJournalStore) syncVerifiedEntry(name string, want vmJailerFileIdentity) error {
	fd, err := unix.Openat(int(store.dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open VM runtime journal entry %s for sync: %w", name, err)
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		return errors.Join(fmt.Errorf("bind VM runtime journal entry %s for sync", name), wrapVMRuntimeJournalError("close unbound VM runtime journal sync descriptor", unix.Close(fd)))
	}
	if err := validateOpenVMRuntimeJournalFile(file, want, store.deps.uid); err != nil {
		return closeVMJailerFileOnError(file, err)
	}
	syncErr := store.deps.syncFile(file)
	closeErr := file.Close()
	if syncErr != nil || closeErr != nil {
		return errors.Join(wrapVMRuntimeJournalError("sync VM runtime journal entry "+name, syncErr), wrapVMRuntimeJournalError("close synced VM runtime journal entry "+name, closeErr))
	}
	if err := store.validatePublishedName(name, want); err != nil {
		return err
	}
	if err := store.deps.syncDir(store.dir); err != nil {
		return fmt.Errorf("sync VM runtime journal directory for %s: %w", name, err)
	}
	return nil
}

func (store *vmRuntimeJournalStore) readRecordIfPresent(name string) (vmRuntimeJournalLoadedRecord, error) {
	metadata, identity, err := store.inspectOptionalEntry(name)
	if err != nil || metadata == nil {
		return vmRuntimeJournalLoadedRecord{}, err
	}
	raw, err := store.readEntry(name, identity, *metadata, vmRuntimeJournalMaxFileSize)
	if err != nil {
		return vmRuntimeJournalLoadedRecord{}, err
	}
	record, err := decodeVMRuntimeJournalRecord(raw)
	if err != nil {
		return vmRuntimeJournalLoadedRecord{}, fmt.Errorf("decode VM runtime journal entry %s: %w", name, err)
	}
	return vmRuntimeJournalLoadedRecord{record: record, identity: identity, present: true}, nil
}

func (store *vmRuntimeJournalStore) inspectExpectedMarker(name string, expected vmRuntimeJournalMarker) (vmRuntimeJournalLoadedRecord, error) {
	metadata, identity, err := store.inspectOptionalEntry(name)
	if err != nil || metadata == nil {
		return vmRuntimeJournalLoadedRecord{}, err
	}
	raw, err := store.readEntry(name, identity, *metadata, vmRuntimeJournalMaxMarkerSize)
	if err != nil {
		return vmRuntimeJournalLoadedRecord{}, err
	}
	marker, err := decodeVMRuntimeJournalMarker(raw)
	if err != nil {
		return vmRuntimeJournalLoadedRecord{}, err
	}
	if !reflect.DeepEqual(marker, expected) {
		return vmRuntimeJournalLoadedRecord{}, fmt.Errorf("VM runtime journal marker %s does not match durable intent", name)
	}
	return vmRuntimeJournalLoadedRecord{identity: identity, present: true}, nil
}

func (store *vmRuntimeJournalStore) inspectOptionalEntry(name string) (*vmRuntimeJournalEntryMetadata, vmJailerFileIdentity, error) {
	var stat unix.Stat_t
	err := unix.Fstatat(int(store.dir.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil, vmJailerFileIdentity{}, nil
	}
	if err != nil {
		return nil, vmJailerFileIdentity{}, err
	}
	metadata := &vmRuntimeJournalEntryMetadata{mode: uint32(stat.Mode), uid: stat.Uid, gid: stat.Gid, size: stat.Size}
	if metadata.mode&unix.S_IFMT != unix.S_IFREG || metadata.uid != store.deps.uid || metadata.mode&0o777 != 0o600 || metadata.size < 0 {
		return nil, vmJailerFileIdentity{}, fmt.Errorf("VM runtime journal entry %s has untrusted metadata", name)
	}
	return metadata, vmJailerFileIdentity{dev: uint64(stat.Dev), ino: stat.Ino}, nil
}

func (store *vmRuntimeJournalStore) publishNewRecord(record vmRuntimeJournalRecord, marker vmRuntimeJournalMarker) error {
	raw, err := encodeVMRuntimeJournalRecord(record)
	if err != nil {
		return store.retainedError(err, marker)
	}
	plan, ok := vmRuntimeJournalPlanForService(marker, record.Service)
	if !ok {
		return store.retainedError(fmt.Errorf("missing marker plan for %s", record.Service), marker)
	}
	return store.publishRawNoReplace(record.Service+".json", plan.StageName, raw, marker)
}

func (store *vmRuntimeJournalStore) replaceRecord(previous, desired vmRuntimeJournalRecord, marker vmRuntimeJournalMarker) error {
	name := desired.Service + ".json"
	old, err := store.requireExpectedRecord(name, previous)
	if err != nil {
		return store.retainedError(err, marker)
	}
	raw, err := encodeVMRuntimeJournalRecord(desired)
	if err != nil {
		return store.retainedError(err, marker)
	}
	plan, ok := vmRuntimeJournalPlanForService(marker, desired.Service)
	if !ok {
		return store.retainedError(fmt.Errorf("missing marker plan for %s", desired.Service), marker)
	}
	return store.publishRawReplacement(name, plan.StageName, raw, old.identity, marker)
}

func (store *vmRuntimeJournalStore) publishNewMarker(marker vmRuntimeJournalMarker) error {
	raw, err := encodeVMRuntimeJournalMarker(marker)
	if err != nil {
		return err
	}
	name := vmRuntimeJournalMarkerName(marker.TransactionID)
	staged, err := store.stageRaw(name, raw)
	if err != nil {
		return err
	}
	if err := store.deps.renameNoReplaceAt(int(store.dir.Fd()), staged.name, int(store.dir.Fd()), name); err != nil {
		cause := fmt.Errorf("publish VM runtime journal marker without replacement: %w", err)
		published, inspectErr := store.inspectExpectedMarker(name, marker)
		cleanupErr := store.cleanupStaged(staged)
		if inspectErr != nil {
			return store.retainedError(errors.Join(cause, fmt.Errorf("classify ambiguous VM runtime journal marker publication: %w", inspectErr), cleanupErr), marker)
		}
		if published.present {
			return store.retainedError(errors.Join(cause, cleanupErr), marker)
		}
		return errors.Join(cause, cleanupErr)
	}
	if err := store.validatePublishedName(name, staged.identity); err != nil {
		return store.retainedError(err, marker)
	}
	if err := store.deps.syncDir(store.dir); err != nil {
		return store.retainedError(fmt.Errorf("sync VM runtime journal marker publication: %w", err), marker)
	}
	return nil
}

func (store *vmRuntimeJournalStore) replaceMarker(previous, next vmRuntimeJournalMarker) error {
	name, old, staged, err := store.stageVMRuntimeJournalMarkerReplacement(previous, next)
	if err != nil {
		return err
	}
	current, err := store.inspectExpectedMarker(name, previous)
	if err != nil || !current.present || current.identity != old.identity {
		return store.retainedError(errors.Join(fmt.Errorf("VM runtime journal marker changed before replacement"), err, store.cleanupStaged(staged)), previous)
	}
	if err := store.deps.renameAt(int(store.dir.Fd()), staged.name, int(store.dir.Fd()), name); err != nil {
		cause := errors.Join(fmt.Errorf("replace VM runtime journal marker: %w", err), store.cleanupStaged(staged))
		return store.classifyVMRuntimeJournalMarkerReplacementError(name, previous, next, cause)
	}
	if err := store.validatePublishedName(name, staged.identity); err != nil {
		return store.retainedError(err, next)
	}
	if err := store.deps.syncDir(store.dir); err != nil {
		return store.retainedError(fmt.Errorf("sync VM runtime journal marker replacement: %w", err), next)
	}
	return nil
}

func (store *vmRuntimeJournalStore) stageVMRuntimeJournalMarkerReplacement(previous, next vmRuntimeJournalMarker) (string, vmRuntimeJournalLoadedRecord, vmRuntimeJournalStaged, error) {
	name := vmRuntimeJournalMarkerName(previous.TransactionID)
	old, err := store.inspectExpectedMarker(name, previous)
	if err != nil || !old.present {
		return "", old, vmRuntimeJournalStaged{}, store.retainedError(errors.Join(fmt.Errorf("validate prior VM runtime journal marker"), err), previous)
	}
	raw, err := encodeVMRuntimeJournalMarker(next)
	if err != nil {
		return "", old, vmRuntimeJournalStaged{}, store.retainedError(err, previous)
	}
	staged, err := store.stageRaw(name, raw)
	if err != nil {
		return "", old, staged, store.retainedError(err, previous)
	}
	return name, old, staged, nil
}

func (store *vmRuntimeJournalStore) classifyVMRuntimeJournalMarkerReplacementError(name string, previous, next vmRuntimeJournalMarker, cause error) error {
	if published, err := store.inspectExpectedMarker(name, next); err == nil && published.present {
		return store.retainedError(cause, next)
	}
	if retained, err := store.inspectExpectedMarker(name, previous); err == nil && retained.present {
		return store.retainedError(cause, previous)
	}
	return store.retainedError(errors.Join(cause, fmt.Errorf("VM runtime journal marker destination is absent or contradictory after replacement error")), next)
}

func (store *vmRuntimeJournalStore) publishRawNoReplace(name, stageName string, raw []byte, marker vmRuntimeJournalMarker) error {
	staged, err := store.ensureBoundStage(stageName, raw)
	if err != nil {
		return store.retainedError(err, marker)
	}
	if err := store.deps.renameNoReplaceAt(int(store.dir.Fd()), staged.name, int(store.dir.Fd()), name); err != nil {
		return store.retainedError(errors.Join(fmt.Errorf("publish VM runtime journal %s without replacement: %w", name, err), store.cleanupStaged(staged)), marker)
	}
	if err := store.validatePublishedName(name, staged.identity); err != nil {
		return store.retainedError(err, marker)
	}
	if err := store.deps.syncDir(store.dir); err != nil {
		return store.retainedError(fmt.Errorf("sync VM runtime journal publication %s: %w", name, err), marker)
	}
	return nil
}

func (store *vmRuntimeJournalStore) publishRawReplacement(name, stageName string, raw []byte, oldIdentity vmJailerFileIdentity, marker vmRuntimeJournalMarker) error {
	staged, err := store.ensureBoundStage(stageName, raw)
	if err != nil {
		return store.retainedError(err, marker)
	}
	metadata, identity, err := store.inspectOptionalEntry(name)
	if err != nil || metadata == nil || identity != oldIdentity {
		return store.retainedError(errors.Join(fmt.Errorf("VM runtime journal %s changed before replacement", name), err, store.cleanupStaged(staged)), marker)
	}
	if err := store.deps.renameAt(int(store.dir.Fd()), staged.name, int(store.dir.Fd()), name); err != nil {
		return store.retainedError(errors.Join(fmt.Errorf("replace VM runtime journal %s: %w", name, err), store.cleanupStaged(staged)), marker)
	}
	if err := store.validatePublishedName(name, staged.identity); err != nil {
		return store.retainedError(err, marker)
	}
	if err := store.deps.syncDir(store.dir); err != nil {
		return store.retainedError(fmt.Errorf("sync VM runtime journal replacement %s: %w", name, err), marker)
	}
	return nil
}

type vmRuntimeJournalStaged struct {
	name     string
	identity vmJailerFileIdentity
}

func (store *vmRuntimeJournalStore) ensureBoundStage(name string, raw []byte) (vmRuntimeJournalStaged, error) {
	metadata, identity, err := store.inspectOptionalEntry(name)
	if err != nil {
		return vmRuntimeJournalStaged{}, err
	}
	if metadata != nil {
		staged, reusable, err := store.reuseOrRemoveVMRuntimeJournalBoundStage(name, raw, *metadata, identity)
		if err != nil || reusable {
			return staged, err
		}
	}
	return store.publishVMRuntimeJournalBoundStage(name, raw)
}

func (store *vmRuntimeJournalStore) reuseOrRemoveVMRuntimeJournalBoundStage(name string, raw []byte, metadata vmRuntimeJournalEntryMetadata, identity vmJailerFileIdentity) (vmRuntimeJournalStaged, bool, error) {
	got, err := store.readEntry(name, identity, metadata, store.deps.maxFileSize)
	if err != nil {
		return vmRuntimeJournalStaged{}, false, err
	}
	if bytes.Equal(got, raw) {
		staged, err := store.revalidateVMRuntimeJournalBoundStage(name, raw, identity)
		return staged, true, err
	}
	// A marker contains the complete target cohort, so a partial stage from an
	// interrupted older writer is disposable after identity verification.
	if err := unlinkVMJailerNameIfIdentityWith(store.dir, name, identity, store.deps.unlinkAt); err != nil {
		return vmRuntimeJournalStaged{}, false, fmt.Errorf("remove incomplete marker-bound stage %s: %w", name, err)
	}
	if err := store.deps.syncDir(store.dir); err != nil {
		return vmRuntimeJournalStaged{}, false, fmt.Errorf("sync incomplete marker-bound stage removal %s: %w", name, err)
	}
	return vmRuntimeJournalStaged{}, false, nil
}

func (store *vmRuntimeJournalStore) revalidateVMRuntimeJournalBoundStage(name string, raw []byte, identity vmJailerFileIdentity) (vmRuntimeJournalStaged, error) {
	if err := store.syncVerifiedEntry(name, identity); err != nil {
		return vmRuntimeJournalStaged{}, err
	}
	metadata, afterIdentity, err := store.inspectOptionalEntry(name)
	if err != nil || metadata == nil || afterIdentity != identity {
		return vmRuntimeJournalStaged{}, errors.Join(fmt.Errorf("marker-bound stage %s changed while syncing", name), err)
	}
	got, err := store.readEntry(name, identity, *metadata, store.deps.maxFileSize)
	if err != nil || !bytes.Equal(got, raw) {
		return vmRuntimeJournalStaged{}, errors.Join(fmt.Errorf("marker-bound stage %s changed while syncing", name), err)
	}
	return vmRuntimeJournalStaged{name: name, identity: identity}, nil
}

func (store *vmRuntimeJournalStore) publishVMRuntimeJournalBoundStage(name string, raw []byte) (vmRuntimeJournalStaged, error) {
	// Never write the deterministic bound name in place. A crash may leave the
	// random recognized-debris temp, but cannot leave a partial live stage.
	staged, err := store.stageRaw(name, raw)
	if err != nil {
		return vmRuntimeJournalStaged{}, err
	}
	if err := store.deps.renameNoReplaceAt(int(store.dir.Fd()), staged.name, int(store.dir.Fd()), name); err != nil {
		return store.classifyVMRuntimeJournalBoundStagePublishError(name, raw, staged, err)
	}
	if err := store.validatePublishedName(name, staged.identity); err != nil {
		return vmRuntimeJournalStaged{}, err
	}
	if err := store.deps.syncDir(store.dir); err != nil {
		return vmRuntimeJournalStaged{}, fmt.Errorf("sync marker-bound stage %s: %w", name, err)
	}
	return vmRuntimeJournalStaged{name: name, identity: staged.identity}, nil
}

func (store *vmRuntimeJournalStore) classifyVMRuntimeJournalBoundStagePublishError(name string, raw []byte, staged vmRuntimeJournalStaged, publishErr error) (vmRuntimeJournalStaged, error) {
	cause := fmt.Errorf("publish marker-bound stage %s: %w", name, publishErr)
	metadata, identity, inspectErr := store.inspectOptionalEntry(name)
	if inspectErr == nil && metadata != nil {
		got, readErr := store.readEntry(name, identity, *metadata, store.deps.maxFileSize)
		if readErr == nil && bytes.Equal(got, raw) {
			cleanupErr := store.cleanupStaged(staged)
			syncErr := store.syncVerifiedEntry(name, identity)
			if cleanupErr != nil || syncErr != nil {
				return vmRuntimeJournalStaged{}, errors.Join(cause, cleanupErr, syncErr)
			}
			return vmRuntimeJournalStaged{name: name, identity: identity}, nil
		}
		inspectErr = errors.Join(readErr, fmt.Errorf("marker-bound stage %s contradicts target bytes", name))
	}
	return vmRuntimeJournalStaged{}, errors.Join(cause, inspectErr, store.cleanupStaged(staged))
}

func (store *vmRuntimeJournalStore) stageRaw(canonical string, raw []byte) (vmRuntimeJournalStaged, error) {
	for range 128 {
		staged, retry, err := store.stageVMRuntimeJournalAttempt(canonical, raw)
		if retry {
			continue
		}
		if err != nil {
			return vmRuntimeJournalStaged{}, err
		}
		return staged, nil
	}
	return vmRuntimeJournalStaged{}, fmt.Errorf("exhausted unique VM runtime journal staging names")
}

func (store *vmRuntimeJournalStore) stageVMRuntimeJournalAttempt(canonical string, raw []byte) (vmRuntimeJournalStaged, bool, error) {
	var suffix [12]byte
	if _, err := io.ReadFull(store.deps.random, suffix[:]); err != nil {
		return vmRuntimeJournalStaged{}, false, err
	}
	name := "." + canonical + ".staged-" + hex.EncodeToString(suffix[:])
	fd, err := unix.Openat(int(store.dir.Fd()), name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if errors.Is(err, unix.EEXIST) {
		return vmRuntimeJournalStaged{}, true, nil
	}
	if err != nil {
		return vmRuntimeJournalStaged{}, false, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		return vmRuntimeJournalStaged{}, false, errors.Join(
			fmt.Errorf("bind staged VM runtime journal descriptor; retain %s because its identity could not be verified", name),
			wrapVMRuntimeJournalError("close unbound staged VM runtime journal descriptor", unix.Close(fd)),
			wrapVMRuntimeJournalError("sync retained staged VM runtime journal", store.deps.syncDir(store.dir)),
		)
	}
	identity, _, inspectErr := vmJailerFileIdentityForFile(file)
	if inspectErr != nil {
		return vmRuntimeJournalStaged{}, false, errors.Join(inspectErr, store.cleanupOpenStaged(file, name, vmJailerFileIdentity{}))
	}
	if err := store.writeVMRuntimeJournalStage(file, name, identity, raw); err != nil {
		return vmRuntimeJournalStaged{}, false, err
	}
	return vmRuntimeJournalStaged{name: name, identity: identity}, false, nil
}

func (store *vmRuntimeJournalStore) writeVMRuntimeJournalStage(file *os.File, name string, identity vmJailerFileIdentity, raw []byte) error {
	n, err := file.Write(raw)
	if err == nil && n != len(raw) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = file.Chmod(0o600)
	}
	if err == nil {
		err = store.deps.syncFile(file)
	}
	if err == nil {
		err = validateOpenVMRuntimeJournalFile(file, identity, store.deps.uid)
	}
	if err != nil {
		return errors.Join(err, store.cleanupOpenStaged(file, name, identity))
	}
	if err := file.Close(); err != nil {
		return errors.Join(err, store.cleanupClosedStaged(name, identity))
	}
	if err := store.validatePublishedName(name, identity); err != nil {
		return errors.Join(err, store.cleanupClosedStaged(name, identity))
	}
	return nil
}

func (store *vmRuntimeJournalStore) cleanupOpenStaged(file *os.File, name string, identity vmJailerFileIdentity) error {
	return errors.Join(file.Close(), store.cleanupClosedStaged(name, identity))
}

func (store *vmRuntimeJournalStore) cleanupClosedStaged(name string, identity vmJailerFileIdentity) error {
	if identity == (vmJailerFileIdentity{}) {
		return errors.Join(
			fmt.Errorf("retain staged VM runtime journal %s because its identity could not be verified", name),
			wrapVMRuntimeJournalError("sync retained staged VM runtime journal", store.deps.syncDir(store.dir)),
		)
	}
	unlinkErr := unlinkVMJailerNameIfIdentityWith(store.dir, name, identity, store.deps.unlinkAt)
	return errors.Join(wrapVMRuntimeJournalError("remove staged VM runtime journal", unlinkErr), wrapVMRuntimeJournalError("sync staged VM runtime journal cleanup", store.deps.syncDir(store.dir)))
}

func (store *vmRuntimeJournalStore) cleanupStaged(staged vmRuntimeJournalStaged) error {
	return store.cleanupClosedStaged(staged.name, staged.identity)
}

func validateOpenVMRuntimeJournalFile(file *os.File, want vmJailerFileIdentity, uid uint32) error {
	got, stat, err := vmJailerFileIdentityForFile(file)
	if err != nil {
		return fmt.Errorf("inspect VM runtime journal: %w", err)
	}
	if got != want || uint32(stat.Mode)&unix.S_IFMT != unix.S_IFREG || stat.Uid != uid || uint32(stat.Mode)&0o777 != 0o600 {
		return fmt.Errorf("VM runtime journal identity, owner, or 0600 permissions changed")
	}
	return nil
}

func (store *vmRuntimeJournalStore) validatePublishedName(name string, want vmJailerFileIdentity) error {
	metadata, got, err := store.inspectOptionalEntry(name)
	if err != nil {
		return err
	}
	if metadata == nil || got != want {
		return fmt.Errorf("VM runtime journal %s identity changed after publication", name)
	}
	return nil
}

func (store *vmRuntimeJournalStore) retainedError(cause error, marker vmRuntimeJournalMarker) error {
	canonical, tombstones, uncertain, markerPresent, inventoryErr := store.publicationInventory(marker)
	return &vmRuntimeJournalPublicationRetainedError{
		cause: errors.Join(cause, inventoryErr), canonicalPaths: canonical, tombstonePaths: tombstones, uncertainPaths: uncertain, markerPresent: markerPresent,
	}
}

func (store *vmRuntimeJournalStore) publicationInventory(marker vmRuntimeJournalMarker) ([]string, []string, []string, bool, error) {
	inv := vmRuntimeJournalPublicationInventory{store: store}
	markerPresent := false
	markerName := vmRuntimeJournalMarkerName(marker.TransactionID)
	markerEntry, markerErr := store.inspectExpectedMarker(markerName, marker)
	if markerErr != nil {
		inv.uncertain = append(inv.uncertain, inv.pathFor(markerName))
		inv.retErr = errors.Join(inv.retErr, fmt.Errorf("inventory VM runtime journal marker: %w", markerErr))
	} else if markerEntry.present {
		markerPresent = true
		inv.canonical = append(inv.canonical, inv.pathFor(markerName))
	}
	for i, plan := range marker.Plans {
		canonicalExpected, tombstoneExpected, stageExpected := vmRuntimeJournalPublicationExpected(marker, i)
		inv.inspectRecord(plan.Service+".json", canonicalExpected, &inv.canonical, false)
		inv.inspectRecord(plan.TombstoneName, tombstoneExpected, &inv.tombstones, len(tombstoneExpected) == 0)
		// A bound stage has exact target bytes, but it is not canonical or a
		// tombstone. Report it as an unresolved path so callers never lose it.
		inv.inspectRecord(plan.StageName, stageExpected, &inv.uncertain, true)
	}
	sort.Strings(inv.canonical)
	sort.Strings(inv.tombstones)
	sort.Strings(inv.uncertain)
	return inv.canonical, inv.tombstones, inv.uncertain, markerPresent, inv.retErr
}

type vmRuntimeJournalPublicationInventory struct {
	store      *vmRuntimeJournalStore
	canonical  []string
	tombstones []string
	uncertain  []string
	retErr     error
}

func (inv *vmRuntimeJournalPublicationInventory) pathFor(name string) string {
	return filepath.Join(inv.store.rootDir, vmRuntimeJournalDirName, name)
}

func (inv *vmRuntimeJournalPublicationInventory) inspectRecord(name string, expected []vmRuntimeJournalRecord, target *[]string, classifyAsUncertain bool) {
	entry, err := inv.store.readRecordIfPresent(name)
	if err != nil {
		inv.uncertain = append(inv.uncertain, inv.pathFor(name))
		inv.retErr = errors.Join(inv.retErr, fmt.Errorf("inventory VM runtime journal artifact %s: %w", name, err))
		return
	}
	if !entry.present {
		return
	}
	exact := slices.ContainsFunc(expected, func(record vmRuntimeJournalRecord) bool {
		return reflect.DeepEqual(entry.record, record)
	})
	if !exact || classifyAsUncertain {
		inv.uncertain = append(inv.uncertain, inv.pathFor(name))
		if !exact {
			inv.retErr = errors.Join(inv.retErr, fmt.Errorf("VM runtime journal artifact %s contradicts durable intent", name))
		}
		return
	}
	*target = append(*target, inv.pathFor(name))
}

func vmRuntimeJournalPublicationExpected(marker vmRuntimeJournalMarker, index int) (canonical, tombstone, stage []vmRuntimeJournalRecord) {
	switch marker.Operation {
	case vmRuntimeJournalOperationStable, vmRuntimeJournalOperationPrepare:
		canonical = []vmRuntimeJournalRecord{marker.Desired[index]}
	case vmRuntimeJournalOperationTransition:
		canonical = []vmRuntimeJournalRecord{marker.Previous[index], marker.Desired[index]}
	case vmRuntimeJournalOperationRemove:
		canonical = []vmRuntimeJournalRecord{marker.Previous[index]}
		tombstone = []vmRuntimeJournalRecord{marker.Previous[index]}
	}
	if marker.Operation == vmRuntimeJournalOperationPrepare || marker.Operation == vmRuntimeJournalOperationTransition {
		stage = []vmRuntimeJournalRecord{marker.Desired[index]}
	}
	return canonical, tombstone, stage
}

func (store *vmRuntimeJournalStore) newVMRuntimeJournalMarker(records []vmRuntimeJournalRecord, operation vmRuntimeJournalOperation, phase vmRuntimeJournalPhase, from, target vmRuntimeJournalState, at time.Time, previous, desired []vmRuntimeJournalRecord) (vmRuntimeJournalMarker, error) {
	var operationIDBytes [32]byte
	if _, err := io.ReadFull(store.deps.random, operationIDBytes[:]); err != nil {
		return vmRuntimeJournalMarker{}, fmt.Errorf("generate VM runtime journal operation ID: %w", err)
	}
	operationID := hex.EncodeToString(operationIDBytes[:])
	plans, err := buildVMRuntimeJournalPlans(operationID, operation, previous, desired)
	if err != nil {
		return vmRuntimeJournalMarker{}, err
	}
	return vmRuntimeJournalMarker{
		Schema: vmRuntimeJournalMarkerSchema, SchemaVersion: vmRuntimeJournalMarkerSchemaVersion,
		TransactionID: records[0].TransactionID, OperationID: operationID, Members: append([]string(nil), records[0].Members...), Plans: plans,
		Operation: operation, Phase: phase, FromState: from, TargetState: target, UpdatedAt: at,
		Previous: cloneVMRuntimeJournalRecords(previous), Desired: cloneVMRuntimeJournalRecords(desired),
	}, nil
}

func stableVMRuntimeJournalMarker(intent vmRuntimeJournalMarker, records []vmRuntimeJournalRecord, at time.Time) (vmRuntimeJournalMarker, error) {
	plans, err := buildVMRuntimeJournalPlans(intent.OperationID, vmRuntimeJournalOperationStable, records, records)
	if err != nil {
		return vmRuntimeJournalMarker{}, err
	}
	return vmRuntimeJournalMarker{
		Schema: vmRuntimeJournalMarkerSchema, SchemaVersion: vmRuntimeJournalMarkerSchemaVersion,
		TransactionID: intent.TransactionID, OperationID: intent.OperationID, Members: append([]string(nil), intent.Members...), Plans: plans,
		Operation: vmRuntimeJournalOperationStable, Phase: vmRuntimeJournalPhaseStable,
		FromState: records[0].State, TargetState: records[0].State, UpdatedAt: at,
		Desired: cloneVMRuntimeJournalRecords(records),
	}, nil
}

func buildVMRuntimeJournalPlans(operationID string, operation vmRuntimeJournalOperation, previous, desired []vmRuntimeJournalRecord) ([]vmRuntimeJournalPlan, error) {
	basis := desired
	if len(basis) == 0 {
		basis = previous
	}
	previousByService := indexVMRuntimeJournalRecords(previous)
	desiredByService := indexVMRuntimeJournalRecords(desired)
	plans := make([]vmRuntimeJournalPlan, 0, len(basis))
	for _, record := range basis {
		plan, err := buildVMRuntimeJournalPlan(operationID, operation, record.Service, previousByService, desiredByService)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	sort.Slice(plans, func(i, j int) bool { return plans[i].Service < plans[j].Service })
	return plans, nil
}

func indexVMRuntimeJournalRecords(records []vmRuntimeJournalRecord) map[string]vmRuntimeJournalRecord {
	indexed := make(map[string]vmRuntimeJournalRecord, len(records))
	for _, record := range records {
		indexed[record.Service] = record
	}
	return indexed
}

func buildVMRuntimeJournalPlan(operationID string, operation vmRuntimeJournalOperation, service string, previous, desired map[string]vmRuntimeJournalRecord) (vmRuntimeJournalPlan, error) {
	plan := vmRuntimeJournalPlan{
		Service: service, StageName: "." + service + ".json.stage-" + operationID,
		TombstoneName: "." + service + ".json.tombstone-" + operationID,
	}
	var err error
	plan.SourceSHA256, err = vmRuntimeJournalRecordDigestIfPresent(previous, service)
	if err != nil {
		return vmRuntimeJournalPlan{}, err
	}
	plan.TargetSHA256, err = vmRuntimeJournalRecordDigestIfPresent(desired, service)
	if err != nil {
		return vmRuntimeJournalPlan{}, err
	}
	normalizeVMRuntimeJournalPlanDigests(&plan, operation)
	return plan, nil
}

func vmRuntimeJournalRecordDigestIfPresent(records map[string]vmRuntimeJournalRecord, service string) (string, error) {
	record, ok := records[service]
	if !ok {
		return "", nil
	}
	return vmRuntimeJournalRecordDigest(record)
}

func normalizeVMRuntimeJournalPlanDigests(plan *vmRuntimeJournalPlan, operation vmRuntimeJournalOperation) {
	switch operation {
	case vmRuntimeJournalOperationStable:
		plan.SourceSHA256 = plan.TargetSHA256
	case vmRuntimeJournalOperationPrepare:
		plan.SourceSHA256 = ""
	case vmRuntimeJournalOperationRemove:
		plan.TargetSHA256 = ""
	}
}

func vmRuntimeJournalRecordDigest(record vmRuntimeJournalRecord) (string, error) {
	raw, err := encodeVMRuntimeJournalRecord(record)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func vmRuntimeJournalMarkerName(transactionID string) string {
	return "transaction-" + transactionID + ".json"
}

func isVMRuntimeJournalMarkerName(name string) bool {
	if !strings.HasPrefix(name, "transaction-") || !strings.HasSuffix(name, ".json") {
		return false
	}
	return isLowerSHA256(strings.TrimSuffix(strings.TrimPrefix(name, "transaction-"), ".json"))
}

func isVMRuntimeJournalRecordTombstoneName(name string) bool {
	_, _, ok := parseVMRuntimeJournalRecordTombstoneName(name)
	return ok
}

func parseVMRuntimeJournalRecordTombstoneName(name string) (string, string, bool) {
	if !strings.HasPrefix(name, ".") {
		return "", "", false
	}
	rest := strings.TrimPrefix(name, ".")
	parts := strings.SplitN(rest, ".json.tombstone-", 2)
	if len(parts) != 2 || serviceid.Validate(parts[0]) != nil || !isLowerSHA256(parts[1]) {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func isVMRuntimeJournalRecordStageName(name string) bool {
	_, _, ok := parseVMRuntimeJournalRecordStageName(name)
	return ok
}

func parseVMRuntimeJournalRecordStageName(name string) (string, string, bool) {
	if !strings.HasPrefix(name, ".") {
		return "", "", false
	}
	parts := strings.SplitN(strings.TrimPrefix(name, "."), ".json.stage-", 2)
	if len(parts) != 2 || serviceid.Validate(parts[0]) != nil || !isLowerSHA256(parts[1]) {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func isVMRuntimeJournalRecognizedDebrisName(name string) bool {
	if strings.HasSuffix(name, ".tombstone") && strings.HasPrefix(name, ".transaction-") {
		id := strings.TrimSuffix(strings.TrimPrefix(name, ".transaction-"), ".json.tombstone")
		return isLowerSHA256(id)
	}
	if !strings.HasPrefix(name, ".") || !strings.Contains(name, ".staged-") {
		return false
	}
	parts := strings.SplitN(name, ".staged-", 2)
	if len(parts) != 2 || len(parts[1]) != 24 {
		return false
	}
	_, err := hex.DecodeString(parts[1])
	return err == nil
}

func vmRuntimeJournalPlanForService(marker vmRuntimeJournalMarker, service string) (vmRuntimeJournalPlan, bool) {
	i := sort.Search(len(marker.Plans), func(i int) bool { return marker.Plans[i].Service >= service })
	if i >= len(marker.Plans) || marker.Plans[i].Service != service {
		return vmRuntimeJournalPlan{}, false
	}
	return marker.Plans[i], true
}

func encodeVMRuntimeJournalRecord(record vmRuntimeJournalRecord) ([]byte, error) {
	return encodeVMRuntimeJournalRecordWithLimit(record, vmRuntimeJournalMaxFileSize)
}

func encodeVMRuntimeJournalRecordWithLimit(record vmRuntimeJournalRecord, limit int) ([]byte, error) {
	if err := validateVMRuntimeJournalRecord(record); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("encode VM runtime journal: %w", err)
	}
	if len(raw)+1 > limit {
		return nil, fmt.Errorf("VM runtime journal including newline exceeds %d bytes", limit)
	}
	return append(raw, '\n'), nil
}

func decodeVMRuntimeJournalRecord(raw []byte) (vmRuntimeJournalRecord, error) {
	if err := validateVMRuntimeJournalRecordJSONShape(raw); err != nil {
		return vmRuntimeJournalRecord{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var record vmRuntimeJournalRecord
	if err := decoder.Decode(&record); err != nil {
		return vmRuntimeJournalRecord{}, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return vmRuntimeJournalRecord{}, err
	}
	if err := validateVMRuntimeJournalRecord(record); err != nil {
		return vmRuntimeJournalRecord{}, err
	}
	return record, nil
}

func validateVMRuntimeJournalRecordJSONShape(raw []byte) error {
	required := []string{
		"schema", "schemaVersion", "transactionID", "members", "service", "serviceRoot", "state", "preparedAt", "updatedAt", "preconditionSHA256",
		"oldDB", "newDB", "oldDescriptor", "newDescriptor", "oldUnit", "newUnit",
	}
	if err := rejectDuplicateVMRuntimeJournalFields(raw); err != nil {
		return err
	}
	if err := requireVMRuntimeJournalFieldsWithOptional(raw, required, []string{"vmHostProjection", "oldVMHost", "newVMHost"}); err != nil {
		return err
	}
	var outer map[string]json.RawMessage
	if err := json.Unmarshal(raw, &outer); err != nil {
		return err
	}
	if err := validateVMRuntimeJournalProjectionFields(outer); err != nil {
		return err
	}
	return validateVMRuntimeJournalRecoveryFields(outer)
}

func validateVMRuntimeJournalProjectionFields(outer map[string]json.RawMessage) error {
	for _, name := range []string{"oldDB", "newDB"} {
		if err := requireVMRuntimeJournalFields(outer[name], []string{"components", "imageKernel"}); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	var ownsVMHostProjection bool
	if rawProjection, ok := outer["vmHostProjection"]; ok {
		if err := json.Unmarshal(rawProjection, &ownsVMHostProjection); err != nil {
			return fmt.Errorf("vmHostProjection: %w", err)
		}
	}
	if !ownsVMHostProjection {
		return nil
	}
	for _, name := range []string{"oldVMHost", "newVMHost"} {
		rawProjection, ok := outer[name]
		if !ok {
			return fmt.Errorf("missing required field %q", name)
		}
		if err := requireVMRuntimeJournalFields(rawProjection, []string{"runtimePolicy", "runtimeChannel"}); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func validateVMRuntimeJournalRecoveryFields(outer map[string]json.RawMessage) error {
	for _, name := range []string{"oldDescriptor", "newDescriptor", "oldUnit", "newUnit"} {
		if err := requireVMRuntimeJournalFields(outer[name], []string{"path", "exists", "contents", "mode", "uid", "gid", "sha256"}); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func encodeVMRuntimeJournalMarker(marker vmRuntimeJournalMarker) ([]byte, error) {
	if err := validateVMRuntimeJournalMarker(marker); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(marker)
	if err != nil {
		return nil, err
	}
	if len(raw)+1 > vmRuntimeJournalMaxMarkerSize {
		return nil, fmt.Errorf("VM runtime journal marker including newline exceeds %d bytes", vmRuntimeJournalMaxMarkerSize)
	}
	return append(raw, '\n'), nil
}

func decodeVMRuntimeJournalMarker(raw []byte) (vmRuntimeJournalMarker, error) {
	if err := rejectDuplicateVMRuntimeJournalFields(raw); err != nil {
		return vmRuntimeJournalMarker{}, err
	}
	if err := requireVMRuntimeJournalFields(raw, []string{"schema", "schemaVersion", "transactionID", "operationID", "members", "plans", "operation", "phase", "fromState", "targetState", "updatedAt", "previous", "desired"}); err != nil {
		return vmRuntimeJournalMarker{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var marker vmRuntimeJournalMarker
	if err := decoder.Decode(&marker); err != nil {
		return vmRuntimeJournalMarker{}, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return vmRuntimeJournalMarker{}, err
	}
	if err := validateVMRuntimeJournalMarker(marker); err != nil {
		return vmRuntimeJournalMarker{}, err
	}
	return marker, nil
}

func validateVMRuntimeJournalMarker(marker vmRuntimeJournalMarker) error {
	if err := validateVMRuntimeJournalMarkerIdentity(marker); err != nil {
		return err
	}
	if err := validateVMRuntimeJournalMarkerRecords(marker, marker.Previous); err != nil {
		return err
	}
	if err := validateVMRuntimeJournalMarkerRecords(marker, marker.Desired); err != nil {
		return err
	}
	if err := validateVMRuntimeJournalMarkerPlans(marker); err != nil {
		return err
	}
	return validateVMRuntimeJournalMarkerOperation(marker)
}

func validateVMRuntimeJournalMarkerIdentity(marker vmRuntimeJournalMarker) error {
	if marker.Schema != vmRuntimeJournalMarkerSchema || marker.SchemaVersion != vmRuntimeJournalMarkerSchemaVersion || !isLowerSHA256(marker.TransactionID) || !isLowerSHA256(marker.OperationID) {
		return fmt.Errorf("unsupported or invalid VM runtime journal marker identity")
	}
	if len(marker.Members) == 0 || len(marker.Members) > vmRuntimeJournalMaxMembers || !sort.StringsAreSorted(marker.Members) {
		return fmt.Errorf("marker members must be a sorted bounded list")
	}
	if marker.UpdatedAt.IsZero() || marker.UpdatedAt.Location() != time.UTC {
		return fmt.Errorf("marker timestamp must be non-zero UTC")
	}
	return nil
}

func validateVMRuntimeJournalMarkerRecords(marker vmRuntimeJournalMarker, records []vmRuntimeJournalRecord) error {
	if len(records) == 0 {
		return nil
	}
	if err := validateVMRuntimeJournalCohort(records); err != nil {
		return err
	}
	if !sort.SliceIsSorted(records, func(i, j int) bool { return records[i].Service < records[j].Service }) {
		return fmt.Errorf("marker records must be sorted by service")
	}
	if records[0].TransactionID != marker.TransactionID || !reflect.DeepEqual(records[0].Members, marker.Members) {
		return fmt.Errorf("marker records contradict transaction identity")
	}
	return nil
}

func validateVMRuntimeJournalMarkerPlans(marker vmRuntimeJournalMarker) error {
	if len(marker.Plans) != len(marker.Members) {
		return fmt.Errorf("marker plans do not cover every member")
	}
	wantPlans, err := buildVMRuntimeJournalPlans(marker.OperationID, marker.Operation, marker.Previous, marker.Desired)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(marker.Plans, wantPlans) {
		return fmt.Errorf("marker plans contradict exact cohort digests or recovery names")
	}
	return nil
}

func validateVMRuntimeJournalMarkerOperation(marker vmRuntimeJournalMarker) error {
	switch marker.Operation {
	case vmRuntimeJournalOperationStable:
		return validateStableVMRuntimeJournalMarker(marker)
	case vmRuntimeJournalOperationPrepare:
		return validatePrepareVMRuntimeJournalMarker(marker)
	case vmRuntimeJournalOperationTransition:
		return validateTransitionVMRuntimeJournalMarker(marker)
	case vmRuntimeJournalOperationRemove:
		return validateRemoveVMRuntimeJournalMarker(marker)
	default:
		return fmt.Errorf("unsupported VM runtime journal marker operation %q", marker.Operation)
	}

}

func validateStableVMRuntimeJournalMarker(marker vmRuntimeJournalMarker) error {
	if marker.Phase != vmRuntimeJournalPhaseStable || len(marker.Previous) != 0 || len(marker.Desired) == 0 || marker.FromState != marker.TargetState || marker.Desired[0].State != marker.TargetState {
		return fmt.Errorf("contradictory stable VM runtime journal marker")
	}
	return nil
}

func validatePrepareVMRuntimeJournalMarker(marker vmRuntimeJournalMarker) error {
	if marker.Phase != vmRuntimeJournalPhasePublishing || len(marker.Previous) != 0 || len(marker.Desired) == 0 || marker.TargetState != vmRuntimeJournalStatePrepared || marker.Desired[0].State != marker.TargetState {
		return fmt.Errorf("contradictory prepare VM runtime journal marker")
	}
	return nil
}

func validateTransitionVMRuntimeJournalMarker(marker vmRuntimeJournalMarker) error {
	if marker.Phase != vmRuntimeJournalPhaseTransitioning || len(marker.Previous) == 0 || len(marker.Previous) != len(marker.Desired) || !legalVMRuntimeJournalTransition(marker.FromState, marker.TargetState) || marker.Previous[0].State != marker.FromState || marker.Desired[0].State != marker.TargetState {
		return fmt.Errorf("contradictory transition VM runtime journal marker")
	}
	for i := range marker.Previous {
		previous, desired := marker.Previous[i], marker.Desired[i]
		previous.State, previous.UpdatedAt = desired.State, desired.UpdatedAt
		if !reflect.DeepEqual(previous, desired) {
			return fmt.Errorf("transition marker changes fields outside journal state and timestamp")
		}
	}
	return nil
}

func validateRemoveVMRuntimeJournalMarker(marker vmRuntimeJournalMarker) error {
	validPhase := marker.Phase == vmRuntimeJournalPhaseRemoving || marker.Phase == vmRuntimeJournalPhaseTombstoned
	if !validPhase || len(marker.Previous) == 0 || len(marker.Desired) != 0 || marker.Previous[0].State != marker.FromState {
		return fmt.Errorf("contradictory remove VM runtime journal marker")
	}
	return nil
}

func rejectDuplicateVMRuntimeJournalFields(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := walkVMRuntimeJournalJSONValue(decoder); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

func walkVMRuntimeJournalJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		return walkVMRuntimeJournalJSONObject(decoder)
	case '[':
		return walkVMRuntimeJournalJSONArray(decoder)
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
}

func walkVMRuntimeJournalJSONObject(decoder *json.Decoder) error {
	seen := make(map[string]struct{})
	for decoder.More() {
		nameToken, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := nameToken.(string)
		if !ok {
			return fmt.Errorf("JSON object field name is not a string")
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("duplicate field %q", name)
		}
		seen[name] = struct{}{}
		if err := walkVMRuntimeJournalJSONValue(decoder); err != nil {
			return err
		}
	}
	_, err := decoder.Token()
	return err
}

func walkVMRuntimeJournalJSONArray(decoder *json.Decoder) error {
	for decoder.More() {
		if err := walkVMRuntimeJournalJSONValue(decoder); err != nil {
			return err
		}
	}
	_, err := decoder.Token()
	return err
}

func requireVMRuntimeJournalFields(raw []byte, required []string) error {
	return requireVMRuntimeJournalFieldsWithOptional(raw, required, nil)
}

func requireVMRuntimeJournalFieldsWithOptional(raw []byte, required, optional []string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if fields == nil {
		return fmt.Errorf("expected JSON object")
	}
	allowed := make(map[string]struct{}, len(required)+len(optional))
	for _, name := range required {
		allowed[name] = struct{}{}
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("missing required field %q", name)
		}
	}
	for _, name := range optional {
		allowed[name] = struct{}{}
	}
	for name := range fields {
		if _, ok := allowed[name]; !ok {
			return fmt.Errorf("unknown field %q", name)
		}
	}
	return nil
}

func validateVMRuntimeJournalCohort(records []vmRuntimeJournalRecord) error {
	if len(records) == 0 || len(records) > vmRuntimeJournalMaxMembers {
		return fmt.Errorf("cohort member count must be between 1 and %d", vmRuntimeJournalMaxMembers)
	}
	seen := make(map[string]struct{}, len(records))
	first := records[0]
	descriptors := make(map[string]string)
	units := make(map[string]string)
	for _, record := range records {
		if err := validateVMRuntimeJournalCohortMember(first, record, seen, descriptors, units); err != nil {
			return err
		}
	}
	return validateCompleteVMRuntimeJournalCohort(first.Members, seen)
}

func validateVMRuntimeJournalCohortMember(first, record vmRuntimeJournalRecord, seen map[string]struct{}, descriptors, units map[string]string) error {
	if err := validateVMRuntimeJournalRecord(record); err != nil {
		return fmt.Errorf("service %s: %w", record.Service, err)
	}
	if err := validateVMRuntimeJournalCohortConsistency(first, record); err != nil {
		return err
	}
	if _, ok := seen[record.Service]; ok {
		return fmt.Errorf("duplicate cohort service %s", record.Service)
	}
	seen[record.Service] = struct{}{}
	if err := addVMRuntimeJournalCohortTarget(descriptors, record.NewDescriptor.Path, record.Service, "descriptor"); err != nil {
		return err
	}
	return addVMRuntimeJournalCohortTarget(units, record.NewUnit.Path, record.Service, "unit")
}

func validateVMRuntimeJournalCohortConsistency(first, record vmRuntimeJournalRecord) error {
	if record.TransactionID != first.TransactionID || !reflect.DeepEqual(record.Members, first.Members) {
		return fmt.Errorf("contradictory transaction identity or members")
	}
	if record.State != first.State || !record.PreparedAt.Equal(first.PreparedAt) || !record.UpdatedAt.Equal(first.UpdatedAt) {
		return fmt.Errorf("contradictory cohort state or timestamps")
	}
	if record.VMHostProjection != first.VMHostProjection || !reflect.DeepEqual(record.OldVMHost, first.OldVMHost) || !reflect.DeepEqual(record.NewVMHost, first.NewVMHost) {
		return fmt.Errorf("contradictory cohort VM host projections")
	}
	return nil
}

func addVMRuntimeJournalCohortTarget(owners map[string]string, path, service, label string) error {
	if owner, ok := owners[path]; ok {
		return fmt.Errorf("duplicate VM runtime %s target for %s and %s", label, owner, service)
	}
	owners[path] = service
	return nil
}

func validateCompleteVMRuntimeJournalCohort(members []string, seen map[string]struct{}) error {
	if len(seen) != len(members) {
		return fmt.Errorf("incomplete cohort: found %d records for %d members", len(seen), len(members))
	}
	for _, member := range members {
		if _, ok := seen[member]; !ok {
			return fmt.Errorf("incomplete cohort: missing member %s", member)
		}
	}
	return nil
}

func validateVMRuntimeJournalRecord(record vmRuntimeJournalRecord) error {
	for _, validate := range []func(vmRuntimeJournalRecord) error{
		validateVMRuntimeJournalRecordIdentity,
		validateVMRuntimeJournalRecordState,
		validateVMRuntimeJournalRecordProjections,
		validateVMRuntimeJournalRecordRecoveryTargets,
	} {
		if err := validate(record); err != nil {
			return err
		}
	}
	return nil
}

func validateVMRuntimeJournalRecordIdentity(record vmRuntimeJournalRecord) error {
	if record.Schema != vmRuntimeJournalSchema || record.SchemaVersion != vmRuntimeJournalSchemaVersion {
		return fmt.Errorf("unsupported VM runtime journal schema %q version %d", record.Schema, record.SchemaVersion)
	}
	if !isLowerSHA256(record.TransactionID) {
		return fmt.Errorf("transaction ID must be a lowercase 256-bit identifier")
	}
	if err := serviceid.Validate(record.Service); err != nil {
		return err
	}
	return validateVMRuntimeJournalMembers(record.Service, record.Members)
}

func validateVMRuntimeJournalMembers(service string, members []string) error {
	if len(members) == 0 || len(members) > vmRuntimeJournalMaxMembers || !sort.StringsAreSorted(members) {
		return fmt.Errorf("members must be a non-empty sorted list of at most %d services", vmRuntimeJournalMaxMembers)
	}
	for i, member := range members {
		if err := serviceid.Validate(member); err != nil {
			return fmt.Errorf("member: %w", err)
		}
		if i > 0 && member == members[i-1] {
			return fmt.Errorf("members contain duplicate service %s", member)
		}
	}
	if i := sort.SearchStrings(members, service); i >= len(members) || members[i] != service {
		return fmt.Errorf("service %s is not in members", service)
	}
	return nil
}

func validateVMRuntimeJournalRecordState(record vmRuntimeJournalRecord) error {
	if strings.TrimSpace(record.ServiceRoot) == "" || !filepath.IsAbs(record.ServiceRoot) || filepath.Clean(record.ServiceRoot) != record.ServiceRoot {
		return fmt.Errorf("service root must be clean and absolute")
	}
	switch record.State {
	case vmRuntimeJournalStatePrepared, vmRuntimeJournalStateDerivedPublished, vmRuntimeJournalStateDatabaseCommitted:
	default:
		return fmt.Errorf("unsupported VM runtime journal state %q", record.State)
	}
	if !validVMRuntimeJournalTimestamps(record.PreparedAt, record.UpdatedAt) {
		return fmt.Errorf("journal timestamps must be non-zero UTC values with updatedAt not before preparedAt")
	}
	if !isLowerSHA256(record.PreconditionSHA256) {
		return fmt.Errorf("precondition digest must be a lowercase SHA-256")
	}
	return nil
}

func validVMRuntimeJournalTimestamps(preparedAt, updatedAt time.Time) bool {
	return !preparedAt.IsZero() && !updatedAt.IsZero() &&
		preparedAt.Location() == time.UTC && updatedAt.Location() == time.UTC &&
		!updatedAt.Before(preparedAt)
}

func validateVMRuntimeJournalRecordProjections(record vmRuntimeJournalRecord) error {
	if equalVMRuntimeJournalProjection(record.OldDB, record.NewDB) {
		return fmt.Errorf("old and new database projections must differ")
	}
	if record.VMHostProjection && (record.OldVMHost == nil || record.NewVMHost == nil) {
		return fmt.Errorf("VM host projections are required")
	}
	if !record.VMHostProjection && (record.OldVMHost != nil || record.NewVMHost != nil) {
		return fmt.Errorf("VM host projections require VM host projection ownership")
	}
	return nil
}

func validateVMRuntimeJournalRecordRecoveryTargets(record vmRuntimeJournalRecord) error {
	wantDescriptor := filepath.Join(serviceDataDirForRoot(record.ServiceRoot), vmRuntimeDescriptorFileName)
	wantUnit := filepath.Join(vmSystemdSystemDir, vmSystemdUnitName(record.Service))
	if record.OldDescriptor.Path != wantDescriptor || record.NewDescriptor.Path != wantDescriptor {
		return fmt.Errorf("descriptor path must be exactly %s", wantDescriptor)
	}
	if record.OldUnit.Path != wantUnit || record.NewUnit.Path != wantUnit {
		return fmt.Errorf("unit path must be exactly %s", wantUnit)
	}
	if wantDescriptor == wantUnit {
		return fmt.Errorf("descriptor and unit recovery targets must differ")
	}
	if err := validateVMRuntimeJournalFilePair(record.OldDescriptor, record.NewDescriptor, "descriptor"); err != nil {
		return err
	}
	return validateVMRuntimeJournalFilePair(record.OldUnit, record.NewUnit, "unit")
}

func validateVMRuntimeJournalFilePair(oldFile, newFile vmRuntimeJournalFile, label string) error {
	if oldFile.Path != newFile.Path {
		return fmt.Errorf("old and new %s paths differ", label)
	}
	if !newFile.Exists {
		return fmt.Errorf("new %s must exist", label)
	}
	for _, file := range []vmRuntimeJournalFile{oldFile, newFile} {
		if err := validateVMRuntimeJournalFile(file, label); err != nil {
			return err
		}
	}
	return nil
}

func validateVMRuntimeJournalFile(file vmRuntimeJournalFile, label string) error {
	if !file.Exists {
		return validateAbsentVMRuntimeJournalFile(file, label)
	}
	return validatePresentVMRuntimeJournalFile(file, label)
}

func validateAbsentVMRuntimeJournalFile(file vmRuntimeJournalFile, label string) error {
	if len(file.Contents) != 0 || file.Mode != 0 || file.UID != 0 || file.GID != 0 || file.SHA256 != "" {
		return fmt.Errorf("absent %s contains contradictory bytes or metadata", label)
	}
	return nil
}

func validatePresentVMRuntimeJournalFile(file vmRuntimeJournalFile, label string) error {
	if len(file.Contents) > vmRuntimeJournalMaxPayload {
		return fmt.Errorf("%s contents exceed %d bytes", label, vmRuntimeJournalMaxPayload)
	}
	if file.Mode&unix.S_IFMT != unix.S_IFREG || file.Mode & ^uint32(unix.S_IFMT|0o7777) != 0 {
		return fmt.Errorf("%s mode is not a supported regular file mode", label)
	}
	digest := sha256.Sum256(file.Contents)
	if file.SHA256 != hex.EncodeToString(digest[:]) {
		return fmt.Errorf("%s digest does not match exact contents", label)
	}
	return nil
}

func vmRuntimeJournalRecordDecodedBytes(record vmRuntimeJournalRecord) int64 {
	return int64(len(record.OldDescriptor.Contents) + len(record.NewDescriptor.Contents) + len(record.OldUnit.Contents) + len(record.NewUnit.Contents))
}

func vmRuntimeJournalMarkerDecodedBytes(marker vmRuntimeJournalMarker) int64 {
	var total int64
	for _, record := range marker.Previous {
		total += vmRuntimeJournalRecordDecodedBytes(record)
	}
	for _, record := range marker.Desired {
		total += vmRuntimeJournalRecordDecodedBytes(record)
	}
	return total
}

func cloneVMRuntimeJournalRecords(records []vmRuntimeJournalRecord) []vmRuntimeJournalRecord {
	cloned := make([]vmRuntimeJournalRecord, len(records))
	for i, record := range records {
		cloned[i] = record
		cloned[i].Members = append([]string(nil), record.Members...)
		cloned[i].OldDB.Components = record.OldDB.Components.Clone()
		cloned[i].NewDB.Components = record.NewDB.Components.Clone()
		cloned[i].OldVMHost = cloneVMRuntimeJournalVMHostProjection(record.OldVMHost)
		cloned[i].NewVMHost = cloneVMRuntimeJournalVMHostProjection(record.NewVMHost)
		cloned[i].OldDescriptor.Contents = append([]byte(nil), record.OldDescriptor.Contents...)
		cloned[i].NewDescriptor.Contents = append([]byte(nil), record.NewDescriptor.Contents...)
		cloned[i].OldUnit.Contents = append([]byte(nil), record.OldUnit.Contents...)
		cloned[i].NewUnit.Contents = append([]byte(nil), record.NewUnit.Contents...)
	}
	return cloned
}

func (store *vmRuntimeJournalStore) checkOpen() error {
	if store == nil || store.closed {
		return fmt.Errorf("VM runtime journal store is closed")
	}
	return nil
}

func wrapVMRuntimeJournalError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
