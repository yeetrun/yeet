// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/serviceid"
)

const (
	serviceIdentityJournalVersion   = 1
	serviceIdentityJournalSyncBatch = 256
	serviceIdentityJournalSealPhase = "inventory-sealed"
	serviceIdentityJournalMaxLine   = 1 << 20
)

var (
	serviceIdentityJournalSync          = func(file *os.File) error { return file.Sync() }
	serviceIdentityJournalDirectorySync = syncServiceIdentityJournalDirectory
	serviceIdentityRestoreMutation      = mutateServiceIdentityPath
	serviceIdentityApplyMutation        = mutateServiceIdentityPath
)

type serviceIdentityJournalHeader struct {
	Version                int                               `json:"version"`
	ID                     string                            `json:"id"`
	Service                string                            `json:"service"`
	Root                   string                            `json:"root"`
	PreviousIdentity       *db.ServiceIdentity               `json:"previousIdentity,omitempty"`
	TargetIdentity         db.ServiceIdentity                `json:"targetIdentity"`
	PreviousUnit           string                            `json:"previousUnit"`
	PreviousUnitPresent    bool                              `json:"previousUnitPresent,omitempty"`
	PreviousUnitPath       string                            `json:"previousUnitPath,omitempty"`
	PreviousUnitMode       os.FileMode                       `json:"previousUnitMode,omitempty"`
	WasRunning             bool                              `json:"wasRunning"`
	PreviousRuntimeUnits   []serviceIdentityRuntimeUnitState `json:"previousRuntimeUnits,omitempty"`
	PreviousRoot           string                            `json:"previousRoot,omitempty"`
	PreviousDataset        string                            `json:"previousDataset,omitempty"`
	TargetRoot             string                            `json:"targetRoot,omitempty"`
	TargetDataset          string                            `json:"targetDataset,omitempty"`
	TargetDatasetCreate    bool                              `json:"targetDatasetCreate,omitempty"`
	PreviousServicePresent bool                              `json:"previousServicePresent,omitempty"`
	PreviousService        *db.Service                       `json:"previousService,omitempty"`
	ObservedService        *db.Service                       `json:"observedService,omitempty"`
	TargetService          *db.Service                       `json:"targetService,omitempty"`
	RootPlan               *serviceRootMigrationPlan         `json:"rootPlan,omitempty"`
	GenerationBackups      []serviceIdentityGenerationBackup `json:"generationBackups,omitempty"`
	PreviousUnitProof      serviceIdentityPathProof          `json:"previousUnitProof,omitempty"`
	GenerationUnits        []serviceIdentityUnitEnablement   `json:"generationUnits,omitempty"`
}

type serviceIdentityGenerationBackup struct {
	Path       string                   `json:"path"`
	BackupPath string                   `json:"backupPath"`
	Present    bool                     `json:"present"`
	Original   serviceIdentityPathProof `json:"original"`
	Backup     serviceIdentityPathProof `json:"backup,omitempty"`
}

type serviceIdentityPathProof struct {
	Path    string      `json:"path"`
	Present bool        `json:"present"`
	Mode    os.FileMode `json:"mode,omitempty"`
	UID     uint32      `json:"uid,omitempty"`
	GID     uint32      `json:"gid,omitempty"`
	Dev     uint64      `json:"dev,omitempty"`
	Ino     uint64      `json:"ino,omitempty"`
	Nlink   uint64      `json:"nlink,omitempty"`
	Size    int64       `json:"size,omitempty"`
	SHA256  string      `json:"sha256,omitempty"`
}

type serviceIdentityPathState struct {
	Path    string      `json:"path"`
	Present bool        `json:"present"`
	Mode    os.FileMode `json:"mode,omitempty"`
	UID     uint32      `json:"uid,omitempty"`
	GID     uint32      `json:"gid,omitempty"`
	Nlink   uint64      `json:"nlink,omitempty"`
	Size    int64       `json:"size,omitempty"`
	SHA256  string      `json:"sha256,omitempty"`
}

type serviceIdentityUnitEnablement struct {
	Unit          string `json:"unit"`
	Enabled       bool   `json:"enabled"`
	TargetEnabled bool   `json:"targetEnabled"`
}

type serviceIdentityRuntimeUnitState struct {
	Unit   string `json:"unit"`
	Active bool   `json:"active"`
}

type serviceIdentityInodeRecord struct {
	Path  string      `json:"path"`
	UID   uint32      `json:"uid"`
	GID   uint32      `json:"gid"`
	Mode  os.FileMode `json:"mode"`
	Dev   uint64      `json:"dev,omitempty"`
	Ino   uint64      `json:"ino,omitempty"`
	Nlink uint64      `json:"nlink,omitempty"`
}

type serviceIdentityPhaseRecord struct {
	Phase             string                            `json:"phase"`
	ZFSSnapshot       string                            `json:"zfsSnapshot,omitempty"`
	DatasetCreated    bool                              `json:"datasetCreated,omitempty"`
	DatasetGUID       string                            `json:"datasetGuid,omitempty"`
	RootCreated       bool                              `json:"rootCreated,omitempty"`
	RootDev           uint64                            `json:"rootDev,omitempty"`
	RootIno           uint64                            `json:"rootIno,omitempty"`
	InventoryDigest   string                            `json:"inventoryDigest,omitempty"`
	InventoryCount    int                               `json:"inventoryCount,omitempty"`
	BackupDir         string                            `json:"backupDir,omitempty"`
	StagePath         string                            `json:"stagePath,omitempty"`
	GenerationBackups []serviceIdentityGenerationBackup `json:"generationBackups,omitempty"`
	GenerationPaths   []serviceIdentityPathProof        `json:"generationPaths,omitempty"`
	GenerationIntents []serviceIdentityPathState        `json:"generationIntents,omitempty"`
	RuntimeBackups    []serviceIdentityGenerationBackup `json:"runtimeBackups,omitempty"`
	PrimaryUnit       serviceIdentityPathProof          `json:"primaryUnit,omitempty"`
	PrimaryUnitIntent serviceIdentityPathState          `json:"primaryUnitIntent,omitempty"`
	Error             string                            `json:"error,omitempty"`
}

type serviceIdentityJournal struct {
	path        string
	file        *os.File
	inodeCount  int
	sealed      bool
	failed      error
	header      serviceIdentityJournalHeader
	recordPaths map[string]struct{}
	records     []serviceIdentityInodeRecord
	phaseRanks  map[string]struct{}
	lastRank    int
}

type serviceIdentityJournalContents struct {
	Header serviceIdentityJournalHeader
	Inodes []serviceIdentityInodeRecord
	Phases []serviceIdentityPhaseRecord
	Sealed bool
}

func serviceIdentityJournalPath(rootDir, service, id string) string {
	name := service + "-" + id + ".jsonl"
	return filepath.Join(rootDir, "migrations", "service-identity", name)
}

func createServiceIdentityJournal(rootDir string, header serviceIdentityJournalHeader) (*serviceIdentityJournal, error) {
	validated, err := validateNewServiceIdentityJournalHeader(header)
	if err != nil {
		return nil, err
	}
	dir, err := ensureServiceIdentityJournalDir(rootDir)
	if err != nil {
		return nil, err
	}
	path := serviceIdentityJournalPath(rootDir, validated.Service, validated.ID)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create service identity journal %s: %w", path, err)
	}
	j := &serviceIdentityJournal{
		path: path, file: file, header: validated, recordPaths: make(map[string]struct{}),
		phaseRanks: make(map[string]struct{}), lastRank: -1,
	}
	if err := initializeServiceIdentityJournal(j, dir); err != nil {
		_ = file.Close()
		return nil, err
	}
	return j, nil
}

func validateNewServiceIdentityJournalHeader(header serviceIdentityJournalHeader) (serviceIdentityJournalHeader, error) {
	if err := serviceid.Validate(header.Service); err != nil {
		return header, fmt.Errorf("invalid service for identity journal: %w", err)
	}
	if err := validateServiceIdentityJournalID(header.ID); err != nil {
		return header, err
	}
	if header.Version == 0 {
		header.Version = serviceIdentityJournalVersion
	}
	if header.Version != serviceIdentityJournalVersion {
		return header, fmt.Errorf("unsupported service identity journal version %d", header.Version)
	}
	if err := validateJournalServiceIdentity(header.TargetIdentity); err != nil {
		return header, fmt.Errorf("invalid target identity: %w", err)
	}
	if err := validatePreviousJournalServiceIdentity(header.PreviousIdentity); err != nil {
		return header, err
	}
	root, err := validateServiceIdentityInspectionRoot(header.Root)
	if err != nil {
		return header, err
	}
	header.Root = root
	return header, nil
}

func validatePreviousJournalServiceIdentity(identity *db.ServiceIdentity) error {
	if identity == nil {
		return nil
	}
	if err := validateJournalServiceIdentity(*identity); err != nil {
		return fmt.Errorf("invalid previous identity: %w", err)
	}
	return nil
}

func initializeServiceIdentityJournal(j *serviceIdentityJournal, dir string) error {
	if err := j.appendJSON(j.header); err != nil {
		return err
	}
	if err := serviceIdentityJournalSync(j.file); err != nil {
		return fmt.Errorf("sync service identity journal header %s: %w", j.path, err)
	}
	return syncServiceIdentityJournalDir(dir)
}

func validateServiceIdentityJournalID(id string) error {
	if id == "" || len(id) > 128 {
		return fmt.Errorf("invalid service identity journal id %q", id)
	}
	for _, r := range id {
		if validServiceIdentityJournalIDRune(r) {
			continue
		}
		return fmt.Errorf("invalid service identity journal id %q", id)
	}
	return nil
}

func validServiceIdentityJournalIDRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-'
}

func ensureServiceIdentityJournalDir(rootDir string) (string, error) {
	rootDir = filepath.Clean(rootDir)
	if rootDir == "." || rootDir == string(filepath.Separator) || !filepath.IsAbs(rootDir) {
		return "", fmt.Errorf("service identity journal root must be a non-root absolute path, got %q", rootDir)
	}
	if err := os.MkdirAll(rootDir, 0o700); err != nil {
		return "", fmt.Errorf("create service identity journal root %s: %w", rootDir, err)
	}
	current := rootDir
	for _, name := range []string{"migrations", "service-identity"} {
		current = filepath.Join(current, name)
		created, err := ensureRootOnlyDirectory(current)
		if err != nil {
			return "", err
		}
		if created {
			if err := syncServiceIdentityJournalDir(filepath.Dir(current)); err != nil {
				return "", err
			}
		}
	}
	return current, nil
}

func ensureRootOnlyDirectory(path string) (bool, error) {
	created := false
	if err := os.Mkdir(path, 0o700); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return false, fmt.Errorf("create service identity journal directory %s: %w", path, err)
		}
	} else {
		created = true
	}
	info, err := os.Lstat(path)
	if err != nil {
		return false, fmt.Errorf("inspect service identity journal directory %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, fmt.Errorf("service identity journal directory %s must be a non-symlink directory", path)
	}
	uid, _, err := nativeServiceFileOwner(info)
	if err != nil {
		return false, fmt.Errorf("inspect service identity journal directory owner %s: %w", path, err)
	}
	if uid != uint32(os.Geteuid()) {
		return false, fmt.Errorf("service identity journal directory %s is owned by uid %d, want %d", path, uid, os.Geteuid())
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return false, fmt.Errorf("set service identity journal directory mode %s: %w", path, err)
	}
	return created, nil
}

func syncServiceIdentityJournalDir(path string) error {
	return serviceIdentityJournalDirectorySync(path)
}

func syncServiceIdentityJournalDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open service identity journal directory %s: %w", path, err)
	}
	defer func() { _ = dir.Close() }()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync service identity journal directory %s: %w", path, err)
	}
	return nil
}

func (j *serviceIdentityJournal) Path() string {
	if j == nil {
		return ""
	}
	return j.path
}

func (j *serviceIdentityJournal) AppendInode(record serviceIdentityInodeRecord) error {
	if err := j.usable(); err != nil {
		return err
	}
	if j.sealed {
		return fmt.Errorf("service identity journal %s is already sealed", j.path)
	}
	if err := validateServiceIdentityJournalRecordPath(record.Path); err != nil {
		return err
	}
	if _, exists := j.recordPaths[record.Path]; exists {
		return fmt.Errorf("service identity journal contains duplicate inode path %q", record.Path)
	}
	if err := j.appendJSON(record); err != nil {
		return err
	}
	j.inodeCount++
	j.recordPaths[record.Path] = struct{}{}
	j.records = append(j.records, record)
	if j.inodeCount%serviceIdentityJournalSyncBatch == 0 {
		if err := serviceIdentityJournalSync(j.file); err != nil {
			j.failed = err
			return fmt.Errorf("sync service identity journal inode batch %s: %w", j.path, err)
		}
	}
	return nil
}

func (j *serviceIdentityJournal) Seal(zfsSnapshot ...string) error {
	if err := j.usable(); err != nil {
		return err
	}
	if j.sealed {
		return nil
	}
	snapshot := ""
	if len(zfsSnapshot) > 0 {
		snapshot = zfsSnapshot[0]
	}
	if len(zfsSnapshot) > 1 {
		return fmt.Errorf("service identity journal seal accepts at most one ZFS snapshot")
	}
	sealRank, _ := serviceIdentityJournalPhaseRank(serviceIdentityJournalSealPhase)
	if sealRank < j.lastRank {
		return fmt.Errorf("service identity journal seal is out of order after rank %d", j.lastRank)
	}
	if err := j.appendJSON(serviceIdentityPhaseRecord{Phase: serviceIdentityJournalSealPhase, ZFSSnapshot: snapshot}); err != nil {
		return err
	}
	if err := serviceIdentityJournalSync(j.file); err != nil {
		j.failed = err
		return fmt.Errorf("sync sealed service identity journal %s: %w", j.path, err)
	}
	j.sealed = true
	j.phaseRanks[serviceIdentityJournalSealPhase] = struct{}{}
	j.lastRank = sealRank
	return nil
}

func (j *serviceIdentityJournal) AppendPhase(record serviceIdentityPhaseRecord) error {
	if err := j.usable(); err != nil {
		return err
	}
	rank, err := j.validatePhaseAppend(record.Phase)
	if err != nil {
		return err
	}
	if err := j.appendJSON(record); err != nil {
		return err
	}
	if err := serviceIdentityJournalSync(j.file); err != nil {
		j.failed = err
		return fmt.Errorf("sync service identity journal phase %q: %w", record.Phase, err)
	}
	j.phaseRanks[record.Phase] = struct{}{}
	j.lastRank = rank
	return nil
}

func (j *serviceIdentityJournal) validatePhaseAppend(phase string) (int, error) {
	if !j.sealed && !serviceIdentityPhaseAllowedBeforeSeal(phase) {
		return 0, fmt.Errorf("service identity journal %s must be sealed before phase %q", j.path, phase)
	}
	if strings.TrimSpace(phase) == "" {
		return 0, fmt.Errorf("service identity journal phase is required")
	}
	if phase == serviceIdentityJournalSealPhase {
		return 0, fmt.Errorf("service identity journal seal phase cannot be appended directly")
	}
	rank, known := serviceIdentityJournalPhaseRank(phase)
	if !known {
		return 0, fmt.Errorf("service identity journal phase %q is unknown", phase)
	}
	if _, duplicate := j.phaseRanks[phase]; duplicate {
		return 0, fmt.Errorf("service identity journal phase %q is duplicated", phase)
	}
	if rank < j.lastRank {
		return 0, fmt.Errorf("service identity journal phase %q is out of order after rank %d", phase, j.lastRank)
	}
	return rank, nil
}

func (j *serviceIdentityJournal) appendJSON(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode service identity journal %s: %w", j.path, err)
	}
	data = append(data, '\n')
	n, err := j.file.Write(data)
	if err != nil {
		j.failed = err
		return fmt.Errorf("write service identity journal %s: %w", j.path, err)
	}
	if n != len(data) {
		j.failed = io.ErrShortWrite
		return fmt.Errorf("write service identity journal %s: %w", j.path, io.ErrShortWrite)
	}
	return nil
}

func (j *serviceIdentityJournal) usable() error {
	if j == nil || j.file == nil {
		return fmt.Errorf("service identity journal is closed")
	}
	if j.failed != nil {
		return fmt.Errorf("service identity journal %s failed: %w", j.path, j.failed)
	}
	return nil
}

func (j *serviceIdentityJournal) Close() error {
	if j == nil || j.file == nil {
		return nil
	}
	err := j.file.Close()
	j.file = nil
	return err
}

func applyServiceIdentityInspection(inspection serviceIdentityInspection, journal *serviceIdentityJournal) error {
	if journal == nil || !journal.sealed || journal.failed != nil {
		return fmt.Errorf("service identity journal must be durably sealed before ownership mutation")
	}
	if len(inspection.Records) != len(inspection.Mutations) || journal.inodeCount != len(inspection.Records) {
		return fmt.Errorf("service identity journal does not contain the complete mutation inventory")
	}
	for index, mutation := range inspection.Mutations {
		if err := applySealedServiceIdentityMutation(inspection, journal, index, mutation); err != nil {
			return err
		}
	}
	return nil
}

func applySealedServiceIdentityMutation(inspection serviceIdentityInspection, journal *serviceIdentityJournal, index int, mutation serviceIdentityMutation) error {
	sealed := journal.records[index]
	if sealed != inspection.Records[index] {
		return fmt.Errorf("service identity mutation %s inventory differs from sealed journal", mutation.Path)
	}
	rel, err := filepath.Rel(journal.header.Root, mutation.Path)
	if err != nil || rel != inspection.Records[index].Path || sealed.Path != rel {
		return fmt.Errorf("service identity mutation %s does not match sealed inode record %q", mutation.Path, inspection.Records[index].Path)
	}
	if _, exists := journal.recordPaths[rel]; !exists {
		return fmt.Errorf("service identity mutation %s is absent from sealed journal", mutation.Path)
	}
	if sealed.Dev != mutation.Dev || sealed.Ino != mutation.Ino || (sealed.Mode&os.ModeSymlink != 0) != mutation.Symlink {
		return fmt.Errorf("service identity mutation %s inode identity does not match sealed journal", mutation.Path)
	}
	if err := serviceIdentityApplyMutation(journal.header.Root, rel, sealed, mutation.UID, mutation.GID, mutation.Mode, mutation.ChangeMode); err != nil {
		return fmt.Errorf("set service identity owner %s to %d:%d: %w", mutation.Path, mutation.UID, mutation.GID, err)
	}
	return nil
}

func loadServiceIdentityJournal(path string) (serviceIdentityJournalContents, error) {
	return loadServiceIdentityJournalContents(path, true)
}

func loadServiceIdentityJournalForRecovery(path string) (serviceIdentityJournalContents, error) {
	return loadServiceIdentityJournalContents(path, false)
}

func loadServiceIdentityJournalContents(path string, requireSeal bool) (serviceIdentityJournalContents, error) {
	file, err := openValidatedServiceIdentityJournal(path)
	if err != nil {
		return serviceIdentityJournalContents{}, err
	}
	defer func() { _ = file.Close() }()
	contents, lineNumber, err := readServiceIdentityJournalContents(file, path)
	if err != nil {
		return serviceIdentityJournalContents{}, err
	}
	if lineNumber == 0 {
		return serviceIdentityJournalContents{}, fmt.Errorf("service identity journal %s is empty", path)
	}
	if requireSeal && !contents.Sealed {
		return serviceIdentityJournalContents{}, fmt.Errorf("service identity journal %s is not sealed", path)
	}
	if err := validateLoadedServiceIdentityJournalHeader(path, contents.Header); err != nil {
		return serviceIdentityJournalContents{}, err
	}
	return contents, nil
}

func openValidatedServiceIdentityJournal(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open service identity journal %s: %w", path, err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect service identity journal %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		_ = file.Close()
		return nil, fmt.Errorf("service identity journal %s must be a private regular file", path)
	}
	uid, _, err := nativeServiceFileOwner(info)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if uid != uint32(os.Geteuid()) {
		_ = file.Close()
		return nil, fmt.Errorf("service identity journal %s is owned by uid %d, want %d", path, uid, os.Geteuid())
	}
	return file, nil
}

func readServiceIdentityJournalContents(file *os.File, path string) (serviceIdentityJournalContents, int, error) {
	reader := bufio.NewReader(file)
	var contents serviceIdentityJournalContents
	paths := make(map[string]struct{})
	lineNumber := 0
	for {
		line, readErr := reader.ReadBytes('\n')
		if err := validateServiceIdentityJournalRead(path, line, readErr, lineNumber+1); err != nil {
			return serviceIdentityJournalContents{}, lineNumber, err
		}
		if len(line) != 0 {
			lineNumber++
			if err := appendDecodedServiceIdentityJournalLine(path, line, lineNumber, &contents, paths); err != nil {
				return serviceIdentityJournalContents{}, lineNumber, err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return serviceIdentityJournalContents{}, lineNumber, fmt.Errorf("read service identity journal %s: %w", path, readErr)
		}
	}
	return contents, lineNumber, nil
}

func validateServiceIdentityJournalRead(path string, line []byte, readErr error, number int) error {
	if len(line) > serviceIdentityJournalMaxLine {
		return fmt.Errorf("service identity journal %s line %d is too large", path, number)
	}
	if errors.Is(readErr, io.EOF) && len(line) != 0 {
		return fmt.Errorf("service identity journal %s has a truncated final line", path)
	}
	return nil
}

func appendDecodedServiceIdentityJournalLine(path string, line []byte, number int, contents *serviceIdentityJournalContents, paths map[string]struct{}) error {
	line = bytes.TrimSuffix(line, []byte{'\n'})
	kind := kindOfServiceIdentityJournalLine(line)
	if err := decodeServiceIdentityJournalLine(line, number, contents); err != nil {
		return fmt.Errorf("service identity journal %s: %w", path, err)
	}
	if kind != "inode" {
		return nil
	}
	last := contents.Inodes[len(contents.Inodes)-1].Path
	if _, duplicate := paths[last]; duplicate {
		return fmt.Errorf("service identity journal %s contains duplicate inode path %q", path, last)
	}
	paths[last] = struct{}{}
	return nil
}

func kindOfServiceIdentityJournalLine(line []byte) string {
	var kind map[string]json.RawMessage
	if json.Unmarshal(line, &kind) != nil {
		return ""
	}
	if kind["path"] != nil {
		return "inode"
	}
	return ""
}

func decodeServiceIdentityJournalLine(line []byte, number int, contents *serviceIdentityJournalContents) error {
	var kind map[string]json.RawMessage
	if err := json.Unmarshal(line, &kind); err != nil {
		return fmt.Errorf("decode line %d: %w", number, err)
	}
	switch {
	case number == 1:
		return decodeServiceIdentityJournalHeader(line, kind, contents)
	case kind["version"] != nil:
		return fmt.Errorf("line %d rewrites immutable header", number)
	case kind["path"] != nil:
		return decodeServiceIdentityJournalInode(line, number, contents)
	case kind["phase"] != nil:
		return decodeServiceIdentityJournalPhase(line, number, contents)
	default:
		return fmt.Errorf("line %d has unknown record type", number)
	}
}

func decodeServiceIdentityJournalHeader(line []byte, kind map[string]json.RawMessage, contents *serviceIdentityJournalContents) error {
	if _, ok := kind["version"]; !ok {
		return fmt.Errorf("line 1 must be the immutable header")
	}
	if err := decodeStrictServiceIdentityJSON(line, &contents.Header); err != nil {
		return fmt.Errorf("decode header: %w", err)
	}
	if contents.Header.Version != serviceIdentityJournalVersion {
		return fmt.Errorf("unsupported version %d", contents.Header.Version)
	}
	if err := serviceid.Validate(contents.Header.Service); err != nil {
		return err
	}
	if err := validateServiceIdentityJournalID(contents.Header.ID); err != nil {
		return err
	}
	if err := validateJournalServiceIdentity(contents.Header.TargetIdentity); err != nil {
		return fmt.Errorf("invalid target identity: %w", err)
	}
	if err := validatePreviousJournalServiceIdentity(contents.Header.PreviousIdentity); err != nil {
		return err
	}
	root, err := validateServiceIdentityInspectionRoot(contents.Header.Root)
	if err != nil || root != contents.Header.Root {
		return fmt.Errorf("invalid journal root %q", contents.Header.Root)
	}
	return nil
}

func decodeServiceIdentityJournalInode(line []byte, number int, contents *serviceIdentityJournalContents) error {
	if contents.Sealed {
		return fmt.Errorf("line %d appends inode after journal seal", number)
	}
	var record serviceIdentityInodeRecord
	if err := decodeStrictServiceIdentityJSON(line, &record); err != nil {
		return fmt.Errorf("decode inode line %d: %w", number, err)
	}
	if err := validateServiceIdentityJournalRecordPath(record.Path); err != nil {
		return fmt.Errorf("line %d: %w", number, err)
	}
	contents.Inodes = append(contents.Inodes, record)
	return nil
}

func decodeServiceIdentityJournalPhase(line []byte, number int, contents *serviceIdentityJournalContents) error {
	var phase serviceIdentityPhaseRecord
	if err := decodeStrictServiceIdentityJSON(line, &phase); err != nil {
		return fmt.Errorf("decode phase line %d: %w", number, err)
	}
	if strings.TrimSpace(phase.Phase) == "" {
		return fmt.Errorf("line %d has empty phase", number)
	}
	if err := validateServiceIdentityJournalPhaseOrder(contents, phase.Phase, number); err != nil {
		return err
	}
	if phase.Phase != serviceIdentityJournalSealPhase && !contents.Sealed && !serviceIdentityPhaseAllowedBeforeSeal(phase.Phase) {
		return fmt.Errorf("line %d records phase %q before journal seal", number, phase.Phase)
	}
	if err := recordServiceIdentityJournalSeal(contents, phase.Phase, number); err != nil {
		return err
	}
	contents.Phases = append(contents.Phases, phase)
	return nil
}

func recordServiceIdentityJournalSeal(contents *serviceIdentityJournalContents, phase string, number int) error {
	if phase != serviceIdentityJournalSealPhase {
		return nil
	}
	if contents.Sealed {
		return fmt.Errorf("line %d duplicates journal seal", number)
	}
	contents.Sealed = true
	return nil
}

func validateServiceIdentityJournalPhaseOrder(contents *serviceIdentityJournalContents, phase string, number int) error {
	lastRank := -1
	seen := make(map[string]struct{}, len(contents.Phases))
	for _, recorded := range contents.Phases {
		if recorded.Phase == serviceIdentityPhaseComplete {
			return fmt.Errorf("line %d records phase %q after complete", number, phase)
		}
		seen[recorded.Phase] = struct{}{}
		if rank, ok := serviceIdentityJournalPhaseRank(recorded.Phase); ok && rank > lastRank {
			lastRank = rank
		}
	}
	rank, known := serviceIdentityJournalPhaseRank(phase)
	if !known {
		return fmt.Errorf("line %d records unknown phase %q", number, phase)
	}
	if _, duplicate := seen[phase]; duplicate {
		if phase == serviceIdentityJournalSealPhase {
			return fmt.Errorf("line %d duplicates journal seal", number)
		}
		return fmt.Errorf("line %d duplicates phase %q", number, phase)
	}
	if rank < lastRank {
		return fmt.Errorf("line %d records phase %q out of order", number, phase)
	}
	return nil
}

func serviceIdentityJournalPhaseRank(phase string) (int, bool) {
	rank, ok := serviceIdentityJournalPhaseRanks[phase]
	return rank, ok
}

var serviceIdentityJournalPhaseRanks = map[string]int{
	serviceIdentityPhaseJournal: 0, serviceIdentityPhaseStop: 10, serviceIdentityPhaseSourceSnapshot: 15,
	serviceIdentityPhaseMaterializeIntent: 20, serviceIdentityPhaseMaterializeCreated: 22,
	serviceIdentityPhaseMaterializePublish: 24, serviceIdentityPhaseMaterialize: 25,
	serviceIdentityPhaseRuntimePlan: 26, serviceIdentityPhaseRuntimeBackup: 27,
	serviceIdentityPhaseRuntimeBackedUp: 28, serviceIdentityPhaseGenerationBackup: 30,
	serviceIdentityPhaseGenerationBackedUp: 31, serviceIdentityPhaseGenerationStageIntent: 32,
	serviceIdentityPhaseGenerationStage: 33, serviceIdentityPhaseMaterializeFinal: 34,
	serviceIdentityPhaseSnapshot: 35, serviceIdentityJournalSealPhase: 40,
	serviceIdentityPhaseOwnership: 50, serviceIdentityPhaseUnitWriteIntent: 59,
	serviceIdentityPhaseUnitWrite: 60, serviceIdentityPhaseDaemonReload: 70,
	serviceIdentityPhaseGeneration: 80, serviceIdentityPhaseGenerationActivate: 82,
	serviceIdentityPhaseGenerationEnabled: 84, serviceIdentityPhaseStart: 90,
	serviceIdentityPhaseVerify: 100, serviceIdentityPhaseDBCommit: 110,
	serviceIdentityPhaseComplete: 120,
}

func serviceIdentityPhaseAllowedBeforeSeal(phase string) bool {
	switch phase {
	case serviceIdentityPhaseStop, serviceIdentityPhaseSourceSnapshot, serviceIdentityPhaseMaterializeIntent,
		serviceIdentityPhaseMaterializeCreated, serviceIdentityPhaseMaterializePublish, serviceIdentityPhaseMaterialize, serviceIdentityPhaseSnapshot, serviceIdentityPhaseGenerationBackup,
		serviceIdentityPhaseGenerationBackedUp, serviceIdentityPhaseGenerationStageIntent, serviceIdentityPhaseGenerationStage,
		serviceIdentityPhaseMaterializeFinal, serviceIdentityPhaseRuntimePlan, serviceIdentityPhaseRuntimeBackup,
		serviceIdentityPhaseRuntimeBackedUp:
		return true
	default:
		return false
	}
}

func validateJournalServiceIdentity(identity db.ServiceIdentity) error {
	if strings.ContainsAny(identity.RequestedUser, "\x00\r\n") || strings.ContainsAny(identity.RequestedGroup, "\x00\r\n") {
		return fmt.Errorf("identity names contain control characters")
	}
	if (identity.RequestedUser == "") != (identity.RequestedGroup == "") {
		return fmt.Errorf("requested user and group must both be present or absent")
	}
	return nil
}

func validateLoadedServiceIdentityJournalHeader(path string, header serviceIdentityJournalHeader) error {
	want := filepath.Base(serviceIdentityJournalPath(filepath.Dir(filepath.Dir(filepath.Dir(path))), header.Service, header.ID))
	if filepath.Base(path) != want {
		return fmt.Errorf("service identity journal filename %q does not match immutable header %q", filepath.Base(path), want)
	}
	return nil
}

func decodeStrictServiceIdentityJSON(line []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if decoder.More() {
		return fmt.Errorf("multiple JSON values")
	}
	return nil
}

func validateServiceIdentityJournalRecordPath(path string) error {
	if path == "." {
		return nil
	}
	clean := filepath.Clean(path)
	if clean != path || filepath.IsAbs(path) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("invalid service identity journal inode path %q", path)
	}
	return nil
}

func restoreServiceIdentityJournal(path string) error {
	contents, err := loadServiceIdentityJournal(path)
	if err != nil {
		return err
	}
	if err := restoreServiceIdentityJournalContents(contents); err != nil {
		return err
	}
	return removeServiceIdentityJournal(path)
}

func restoreServiceIdentityJournalContents(contents serviceIdentityJournalContents) error {
	for i := len(contents.Inodes) - 1; i >= 0; i-- {
		if err := restoreServiceIdentityJournalRecord(contents.Header, contents.Inodes[i]); err != nil {
			return err
		}
	}
	return nil
}

func restoreServiceIdentityJournalRecord(header serviceIdentityJournalHeader, record serviceIdentityInodeRecord) error {
	state, err := inspectServiceIdentityJournalRestoreRecord(header, record)
	if err != nil || state.restored {
		return err
	}
	if !validServiceIdentityJournalRestoreState(state.info, state.meta, record, state.desired, state.managed, state.symlink, state.originalOwner, state.originalMode) {
		return fmt.Errorf("restore service identity owner %s: metadata matches neither migrated nor restored state", state.target)
	}
	expected := serviceIdentityInodeRecord{
		Path: record.Path, UID: state.meta.UID, GID: state.meta.GID, Mode: state.info.Mode(),
		Dev: state.meta.Dev, Ino: state.meta.Ino, Nlink: state.meta.Nlink,
	}
	if err := serviceIdentityRestoreMutation(header.Root, record.Path, expected, record.UID, record.GID, record.Mode, !state.symlink); err != nil {
		return fmt.Errorf("restore service identity owner %s: %w", state.target, err)
	}
	return nil
}

type serviceIdentityJournalRestoreState struct {
	target        string
	info          os.FileInfo
	meta          serviceIdentityInodeMetadata
	desired       serviceIdentityMutationTarget
	managed       bool
	symlink       bool
	originalOwner bool
	originalMode  bool
	restored      bool
}

func inspectServiceIdentityJournalRestoreRecord(header serviceIdentityJournalHeader, record serviceIdentityInodeRecord) (serviceIdentityJournalRestoreState, error) {
	target, err := resolveServiceIdentityJournalRecordPath(header.Root, record.Path)
	if err != nil {
		return serviceIdentityJournalRestoreState{}, err
	}
	info, err := os.Lstat(target)
	if err != nil {
		return serviceIdentityJournalRestoreState{}, fmt.Errorf("inspect service identity owner %s before restore: %w", target, err)
	}
	meta, err := serviceIdentityMetadata(info)
	if err != nil {
		return serviceIdentityJournalRestoreState{}, err
	}
	symlink := record.Mode&os.ModeSymlink != 0
	if !serviceIdentityJournalRecordInodeMatches(info, meta, record, symlink) {
		return serviceIdentityJournalRestoreState{}, fmt.Errorf("restore service identity owner %s: inode identity or type changed", target)
	}
	originalOwner := meta.UID == record.UID && meta.GID == record.GID
	originalMode := symlink || serviceIdentityUnixMode(info.Mode()) == serviceIdentityUnixMode(record.Mode)
	desired, managed := nativeServiceIdentityMutationTarget(header.Root, target, record.Mode, header.TargetIdentity)
	return serviceIdentityJournalRestoreState{
		target: target, info: info, meta: meta, desired: desired, managed: managed, symlink: symlink,
		originalOwner: originalOwner, originalMode: originalMode, restored: originalOwner && originalMode,
	}, nil
}

func serviceIdentityJournalRecordInodeMatches(info os.FileInfo, meta serviceIdentityInodeMetadata, record serviceIdentityInodeRecord, symlink bool) bool {
	return meta.Dev == record.Dev && meta.Ino == record.Ino && (info.Mode()&os.ModeSymlink != 0) == symlink
}

func validServiceIdentityJournalRestoreState(info os.FileInfo, meta serviceIdentityInodeMetadata, record serviceIdentityInodeRecord, desired serviceIdentityMutationTarget, managed, symlink, originalOwner, originalMode bool) bool {
	targetOwner := managed && meta.UID == desired.uid && meta.GID == desired.gid
	targetMode := symlink || serviceIdentityUnixMode(info.Mode()) == serviceIdentityUnixMode(desired.mode)
	return managed && (originalOwner || targetOwner) && (originalMode || targetMode) && meta.Nlink == record.Nlink
}

func removeServiceIdentityJournal(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove service identity journal %s: %w", path, err)
	}
	return syncServiceIdentityJournalDir(filepath.Dir(path))
}

func mutateServiceIdentityPath(root, rel string, expected serviceIdentityInodeRecord, uid, gid uint32, mode os.FileMode, changeMode bool) error {
	parentFD, name, closeParent, err := openServiceIdentityMutationParent(root, rel)
	if err != nil {
		return err
	}
	defer closeParent()
	if expected.Mode&os.ModeSymlink != 0 {
		return mutateServiceIdentitySymlinkPath(parentFD, name, expected, uid, gid)
	}
	return mutateServiceIdentityRegularPath(parentFD, name, expected, uid, gid, mode, changeMode)
}

func mutateServiceIdentitySymlinkPath(parentFD int, name string, expected serviceIdentityInodeRecord, uid, gid uint32) error {
	if name == "" {
		return fmt.Errorf("service identity root cannot be restored as a symlink")
	}
	if err := mutateServiceIdentitySymlink(parentFD, name, expected, uid, gid); err != nil {
		return err
	}
	if err := unix.Fsync(parentFD); err != nil {
		return fmt.Errorf("sync symlink parent after ownership change: %w", err)
	}
	return nil
}

func mutateServiceIdentityRegularPath(parentFD int, name string, expected serviceIdentityInodeRecord, uid, gid uint32, mode os.FileMode, changeMode bool) error {
	fd := parentFD
	closeFD := func() {}
	var err error
	if name != "" {
		fd, err = unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			return fmt.Errorf("open inode without following symlinks: %w", err)
		}
		closeFD = func() { _ = unix.Close(fd) }
	}
	defer closeFD()
	if err := validateServiceIdentityRegularMutationFD(fd, expected); err != nil {
		return err
	}
	if err := applyServiceIdentityRegularMutation(fd, expected, uid, gid, mode, changeMode); err != nil {
		return err
	}
	if err := unix.Fsync(fd); err != nil {
		return fmt.Errorf("sync inode after ownership change: %w", err)
	}
	if fd == parentFD {
		return nil
	}
	if err := unix.Fsync(parentFD); err != nil {
		return fmt.Errorf("sync parent after ownership change: %w", err)
	}
	return nil
}

func validateServiceIdentityRegularMutationFD(fd int, expected serviceIdentityInodeRecord) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("inspect opened inode: %w", err)
	}
	if stat.Mode&unix.S_IFMT == unix.S_IFLNK {
		return fmt.Errorf("path type changed: unexpected symlink")
	}
	if err := validateServiceIdentityMutationState(stat, expected, false); err != nil {
		return err
	}
	xattrs, err := listServiceIdentityOpenFDXattrs(fd)
	if err != nil {
		return fmt.Errorf("inspect opened inode xattrs: %w", err)
	}
	if name := blockedServiceIdentityXattr(xattrs); name != "" {
		return fmt.Errorf("extended attribute %s appeared after inventory seal", name)
	}
	return nil
}

func applyServiceIdentityRegularMutation(fd int, expected serviceIdentityInodeRecord, uid, gid uint32, mode os.FileMode, changeMode bool) error {
	if err := unix.Fchown(fd, int(uid), int(gid)); err != nil {
		return err
	}
	if changeMode {
		if err := unix.Fchmod(fd, serviceIdentityUnixMode(mode)); err != nil {
			return err
		}
	}
	var changed unix.Stat_t
	if err := unix.Fstat(fd, &changed); err != nil {
		return fmt.Errorf("verify opened inode after ownership change: %w", err)
	}
	target := expected
	target.UID, target.GID = uid, gid
	if changeMode {
		target.Mode = expected.Mode&os.ModeType | mode
	}
	if err := validateServiceIdentityMutationState(changed, target, false); err != nil {
		return fmt.Errorf("verify opened inode after ownership change: %w", err)
	}
	xattrs, err := listServiceIdentityOpenFDXattrs(fd)
	if err != nil {
		return fmt.Errorf("verify opened inode xattrs: %w", err)
	}
	if name := blockedServiceIdentityXattr(xattrs); name != "" {
		return fmt.Errorf("extended attribute %s appeared while changing ownership", name)
	}
	return nil
}

func openServiceIdentityMutationParent(root, rel string) (int, string, func(), error) {
	if err := validateServiceIdentityJournalRecordPath(rel); err != nil {
		return -1, "", func() {}, err
	}
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, "", func() {}, fmt.Errorf("open service identity root without following symlinks: %w", err)
	}
	current := rootFD
	closeAll := func() { _ = unix.Close(current) }
	if rel == "." {
		return current, "", closeAll, nil
	}
	parts := strings.Split(rel, string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		next, openErr := unix.Openat(current, part, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			_ = unix.Close(current)
			return -1, "", func() {}, fmt.Errorf("open service identity parent %q without following symlinks: %w", part, openErr)
		}
		_ = unix.Close(current)
		current = next
	}
	return current, parts[len(parts)-1], closeAll, nil
}

func validateServiceIdentityMutationState(stat unix.Stat_t, expected serviceIdentityInodeRecord, symlink bool) error {
	if err := validateServiceIdentityMutationInode(stat, expected); err != nil {
		return err
	}
	return validateServiceIdentityMutationTypeAndMode(stat, expected, symlink)
}

func validateServiceIdentityMutationInode(stat unix.Stat_t, expected serviceIdentityInodeRecord) error {
	if expected.Dev != 0 && uint64(stat.Dev) != expected.Dev {
		return fmt.Errorf("inode device changed from %d to %d", expected.Dev, uint64(stat.Dev))
	}
	if expected.Ino != 0 && uint64(stat.Ino) != expected.Ino {
		return fmt.Errorf("inode changed from %d to %d", expected.Ino, uint64(stat.Ino))
	}
	if uint64(stat.Nlink) != expected.Nlink {
		return fmt.Errorf("inode link count changed from %d to %d", expected.Nlink, uint64(stat.Nlink))
	}
	if stat.Uid != expected.UID || stat.Gid != expected.GID {
		return fmt.Errorf("inode owner changed from %d:%d to %d:%d", expected.UID, expected.GID, stat.Uid, stat.Gid)
	}
	return nil
}

func validateServiceIdentityMutationTypeAndMode(stat unix.Stat_t, expected serviceIdentityInodeRecord, symlink bool) error {
	wantType := uint32(unix.S_IFREG)
	if expected.Mode.IsDir() {
		wantType = unix.S_IFDIR
	} else if symlink {
		wantType = unix.S_IFLNK
	}
	if uint32(stat.Mode)&unix.S_IFMT != wantType {
		return fmt.Errorf("path type changed")
	}
	if !symlink && uint32(stat.Mode)&0o7777 != serviceIdentityUnixMode(expected.Mode) {
		return fmt.Errorf("inode mode changed from %s to %o", expected.Mode, stat.Mode&0o7777)
	}
	return nil
}

func serviceIdentityUnixMode(mode os.FileMode) uint32 {
	out := uint32(mode.Perm())
	if mode&os.ModeSetuid != 0 {
		out |= unix.S_ISUID
	}
	if mode&os.ModeSetgid != 0 {
		out |= unix.S_ISGID
	}
	if mode&os.ModeSticky != 0 {
		out |= unix.S_ISVTX
	}
	return out
}

func resolveServiceIdentityJournalRecordPath(root, rel string) (string, error) {
	if err := validateServiceIdentityJournalRecordPath(rel); err != nil {
		return "", err
	}
	if rel == "." {
		return root, nil
	}
	current := root
	parts := strings.Split(rel, string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", fmt.Errorf("inspect service identity restore parent %s: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("service identity restore parent %s is not a non-symlink directory", current)
		}
	}
	return filepath.Join(root, rel), nil
}
