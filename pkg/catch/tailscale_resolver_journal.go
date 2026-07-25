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
	"sort"
	"strings"

	"github.com/yeetrun/yeet/pkg/serviceid"
	"golang.org/x/sys/unix"
)

const (
	tailscaleResolverJournalVersion = 1
	tailscaleResolverJournalMaxLine = 1 << 20

	tailscaleResolverPhasePrepared         = "prepared"
	tailscaleResolverPhaseFilesWritten     = "files-written"
	tailscaleResolverPhaseDaemonReloaded   = "daemon-reloaded"
	tailscaleResolverPhaseServicesVerified = "services-verified"
	tailscaleResolverPhaseCommitted        = "committed"
)

var (
	tailscaleResolverJournalOwnerUID = uint32(0)
	tailscaleResolverJournalSync     = func(file *os.File) error { return file.Sync() }
	tailscaleResolverJournalDirSync  = syncServiceIdentityJournalDirectory
	tailscaleResolverNewJournalID    = newServiceIdentityMigrationID
)

type tailscaleResolverJournalHeader struct {
	Version  int                               `json:"version"`
	ID       string                            `json:"id"`
	Services []tailscaleResolverJournalService `json:"services"`
	Files    []tailscaleResolverJournalFile    `json:"files"`
}

type tailscaleResolverJournalService struct {
	ServiceName string                              `json:"serviceName"`
	UnitName    string                              `json:"unitName"`
	WasActive   bool                                `json:"wasActive"`
	Record      tailscaleResolverServiceRecordProof `json:"record"`
}

type tailscaleResolverJournalFile struct {
	Path          string                   `json:"path"`
	Original      []byte                   `json:"original"`
	Next          []byte                   `json:"next"`
	OriginalProof serviceIdentityPathProof `json:"originalProof"`
}

type tailscaleResolverJournalPhase struct {
	Phase string `json:"phase"`
}

type tailscaleResolverJournal struct {
	path      string
	file      *os.File
	lastPhase int
	failed    error
}

type tailscaleResolverJournalContents struct {
	Header tailscaleResolverJournalHeader
	Phases []string
}

func newTailscaleResolverJournalHeader(
	plan tailscaleResolverFleetPlan,
) (tailscaleResolverJournalHeader, error) {
	id, err := tailscaleResolverNewJournalID()
	if err != nil {
		return tailscaleResolverJournalHeader{}, fmt.Errorf(
			"create tailscale resolver journal id: %w",
			err,
		)
	}
	header := tailscaleResolverJournalHeader{
		Version:  tailscaleResolverJournalVersion,
		ID:       id,
		Services: make([]tailscaleResolverJournalService, 0, len(plan.Services)),
	}
	for _, service := range plan.Services {
		header.Services = append(header.Services, tailscaleResolverJournalService{
			ServiceName: service.ServiceName,
			UnitName:    service.UnitName,
			WasActive:   service.WasActive,
			Record:      service.Record,
		})
		for _, file := range service.Files {
			header.Files = append(header.Files, tailscaleResolverJournalFile{
				Path:          file.Path,
				Original:      append([]byte(nil), file.Original...),
				Next:          append([]byte(nil), file.Next...),
				OriginalProof: file.Proof,
			})
		}
	}
	sort.Slice(header.Files, func(i, j int) bool { return header.Files[i].Path < header.Files[j].Path })
	return header, nil
}

func tailscaleResolverJournalDir(root string) string {
	return filepath.Join(root, "migrations", "tailscale-resolver")
}

func tailscaleResolverJournalPath(root, id string) string {
	return filepath.Join(tailscaleResolverJournalDir(root), id+".jsonl")
}

func ensureTailscaleResolverJournalDir(root string) (string, error) {
	root = filepath.Clean(root)
	if !cleanAbsolutePath(root) || root == string(filepath.Separator) {
		return "", fmt.Errorf("tailscale resolver journal root must be a non-root absolute clean path: %q", root)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create tailscale resolver journal root %s: %w", root, err)
	}
	current := root
	for _, name := range []string{"migrations", "tailscale-resolver"} {
		current = filepath.Join(current, name)
		created, err := ensureTailscaleResolverPrivateDir(current)
		if err != nil {
			return "", err
		}
		if created {
			if err := tailscaleResolverJournalDirSync(filepath.Dir(current)); err != nil {
				return "", fmt.Errorf("sync tailscale resolver journal parent %s: %w", filepath.Dir(current), err)
			}
		}
	}
	return current, nil
}

func ensureTailscaleResolverPrivateDir(path string) (bool, error) {
	created := false
	if err := os.Mkdir(path, 0o700); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return false, fmt.Errorf("create tailscale resolver journal directory %s: %w", path, err)
		}
	} else {
		created = true
	}
	info, err := os.Lstat(path)
	if err != nil {
		return false, fmt.Errorf("inspect tailscale resolver journal directory %s: %w", path, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("tailscale resolver journal directory %s must be a non-symlink directory", path)
	}
	uid, _, err := nativeServiceFileOwner(info)
	if err != nil {
		return false, fmt.Errorf("inspect tailscale resolver journal directory owner %s: %w", path, err)
	}
	if uid != tailscaleResolverJournalOwnerUID {
		return false, fmt.Errorf(
			"tailscale resolver journal directory %s is owned by uid %d, want %d",
			path,
			uid,
			tailscaleResolverJournalOwnerUID,
		)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return false, fmt.Errorf("set tailscale resolver journal directory mode %s: %w", path, err)
	}
	return created, nil
}

func createTailscaleResolverJournal(
	root string,
	header tailscaleResolverJournalHeader,
) (*tailscaleResolverJournal, error) {
	if err := validateTailscaleResolverJournalHeader(header); err != nil {
		return nil, err
	}
	dir, err := ensureTailscaleResolverJournalDir(root)
	if err != nil {
		return nil, err
	}
	path := tailscaleResolverJournalPath(root, header.ID)
	file, err := os.OpenFile(
		path,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY|unix.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return nil, fmt.Errorf("create tailscale resolver journal %s: %w", path, err)
	}
	journal := &tailscaleResolverJournal{path: path, file: file, lastPhase: -1}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("set tailscale resolver journal mode %s: %w", path, err)
	}
	if err := file.Chown(int(tailscaleResolverJournalOwnerUID), -1); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("set tailscale resolver journal owner %s: %w", path, err)
	}
	if err := journal.appendJSON(header); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := tailscaleResolverJournalSync(file); err != nil {
		journal.failed = err
		_ = file.Close()
		return nil, fmt.Errorf("sync tailscale resolver journal header %s: %w", path, err)
	}
	if err := tailscaleResolverJournalDirSync(dir); err != nil {
		journal.failed = err
		_ = file.Close()
		return nil, fmt.Errorf("sync tailscale resolver journal directory %s: %w", dir, err)
	}
	return journal, nil
}

func (j *tailscaleResolverJournal) Path() string {
	if j == nil {
		return ""
	}
	return j.path
}

func (j *tailscaleResolverJournal) AppendPhase(phase string) error {
	if j == nil || j.file == nil {
		return errors.New("tailscale resolver journal is closed")
	}
	if j.failed != nil {
		return fmt.Errorf("tailscale resolver journal %s failed: %w", j.path, j.failed)
	}
	rank, ok := tailscaleResolverPhaseRank(phase)
	if !ok {
		return fmt.Errorf("unknown tailscale resolver journal phase %q", phase)
	}
	if rank != j.lastPhase+1 {
		return fmt.Errorf(
			"tailscale resolver journal phase %q is out of order after rank %d",
			phase,
			j.lastPhase,
		)
	}
	if err := j.appendJSON(tailscaleResolverJournalPhase{Phase: phase}); err != nil {
		return err
	}
	if err := tailscaleResolverJournalSync(j.file); err != nil {
		j.failed = err
		return fmt.Errorf("sync tailscale resolver journal phase %q: %w", phase, err)
	}
	j.lastPhase = rank
	return nil
}

func (j *tailscaleResolverJournal) appendJSON(value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode tailscale resolver journal %s: %w", j.path, err)
	}
	if len(raw)+1 > tailscaleResolverJournalMaxLine {
		return fmt.Errorf("tailscale resolver journal %s record is too large", j.path)
	}
	raw = append(raw, '\n')
	n, err := j.file.Write(raw)
	if err != nil {
		j.failed = err
		return fmt.Errorf("write tailscale resolver journal %s: %w", j.path, err)
	}
	if n != len(raw) {
		j.failed = io.ErrShortWrite
		return fmt.Errorf("write tailscale resolver journal %s: %w", j.path, io.ErrShortWrite)
	}
	return nil
}

func (j *tailscaleResolverJournal) Close() error {
	if j == nil || j.file == nil {
		return nil
	}
	err := j.file.Close()
	j.file = nil
	return err
}

func validateTailscaleResolverJournalHeader(header tailscaleResolverJournalHeader) error {
	if header.Version != tailscaleResolverJournalVersion {
		return fmt.Errorf("unsupported tailscale resolver journal version %d", header.Version)
	}
	if err := validateTailscaleResolverJournalID(header.ID); err != nil {
		return err
	}
	if len(header.Services) == 0 {
		return errors.New("tailscale resolver journal services are required")
	}
	serviceNames, err := validateTailscaleResolverJournalServices(header.Services)
	if err != nil {
		return err
	}
	return validateTailscaleResolverJournalFiles(header.Files, serviceNames)
}

func validateTailscaleResolverJournalServices(
	services []tailscaleResolverJournalService,
) (map[string]tailscaleResolverJournalService, error) {
	serviceNames := make(map[string]tailscaleResolverJournalService, len(services))
	lastService := ""
	for _, service := range services {
		if err := serviceid.Validate(service.ServiceName); err != nil {
			return nil, fmt.Errorf("invalid tailscale resolver journal service: %w", err)
		}
		if service.ServiceName <= lastService {
			return nil, errors.New("tailscale resolver journal services must be unique and lexically ordered")
		}
		if service.UnitName != "yeet-"+service.ServiceName+"-ts.service" {
			return nil, fmt.Errorf(
				"invalid tailscale resolver unit %q for service %q",
				service.UnitName,
				service.ServiceName,
			)
		}
		if err := validateTailscaleResolverJournalServiceRecord(service); err != nil {
			return nil, err
		}
		serviceNames[service.ServiceName] = service
		lastService = service.ServiceName
	}
	return serviceNames, nil
}

func validateTailscaleResolverJournalFiles(
	files []tailscaleResolverJournalFile,
	serviceNames map[string]tailscaleResolverJournalService,
) error {
	if len(files) != 2*len(serviceNames) {
		return fmt.Errorf(
			"tailscale resolver journal has %d files for %d services, want exactly two per service",
			len(files),
			len(serviceNames),
		)
	}
	managedCounts := make(map[string]int, len(serviceNames))
	lastPath := ""
	for _, file := range files {
		if err := validateTailscaleResolverJournalFile(file, serviceNames); err != nil {
			return err
		}
		if file.Path <= lastPath {
			return errors.New("tailscale resolver journal files must have unique lexically ordered paths")
		}
		service, ok := tailscaleResolverManagedPathService(file.Path, serviceNames)
		if !ok {
			return fmt.Errorf("tailscale resolver journal path is not an exact managed unit path: %q", file.Path)
		}
		managedCounts[service]++
		lastPath = file.Path
	}
	for service := range serviceNames {
		if managedCounts[service] != 2 {
			return fmt.Errorf("tailscale resolver journal service %q has %d managed files, want two", service, managedCounts[service])
		}
	}
	return nil
}

func validateTailscaleResolverJournalFile(
	file tailscaleResolverJournalFile,
	services map[string]tailscaleResolverJournalService,
) error {
	if !cleanAbsolutePath(file.Path) {
		return fmt.Errorf("tailscale resolver journal path must be absolute and clean: %q", file.Path)
	}
	if err := validateServiceIdentityPathProofRecord(file.OriginalProof, file.Path); err != nil {
		return err
	}
	if !file.OriginalProof.Present {
		return fmt.Errorf("tailscale resolver journal original %s must be present", file.Path)
	}
	original := serviceIdentityDesiredFileState(
		file.Path,
		file.Original,
		file.OriginalProof.Mode,
		file.OriginalProof.UID,
		file.OriginalProof.GID,
	)
	if !serviceIdentityPathMatchesState(file.OriginalProof, original) {
		return fmt.Errorf("tailscale resolver journal original bytes for %s do not match their proof", file.Path)
	}
	if _, ok := tailscaleResolverManagedPathService(file.Path, services); !ok {
		return fmt.Errorf("tailscale resolver journal path is not managed: %q", file.Path)
	}
	return nil
}

func tailscaleResolverManagedPathService(
	path string,
	services map[string]tailscaleResolverJournalService,
) (string, bool) {
	for service, record := range services {
		if path == filepath.Join(systemdSystemDir, record.UnitName) {
			return service, true
		}
		if path == record.Record.TSServiceArtifact {
			return service, true
		}
	}
	return "", false
}

func validateTailscaleResolverJournalServiceRecord(
	service tailscaleResolverJournalService,
) error {
	record := service.Record
	if record.Generation <= 0 || record.LatestGeneration < record.Generation {
		return fmt.Errorf(
			"invalid tailscale resolver journal generation for service %q",
			service.ServiceName,
		)
	}
	if !cleanAbsolutePath(record.ServiceRoot) ||
		record.Interface == "" ||
		!validLinuxInterfaceName(record.Interface) {
		return fmt.Errorf(
			"invalid tailscale resolver journal record for service %q",
			service.ServiceName,
		)
	}
	for name, path := range map[string]string{
		"Tailscale service": record.TSServiceArtifact,
		"Tailscale binary":  record.TSBinaryArtifact,
		"Tailscale env":     record.TSEnvArtifact,
		"Tailscale config":  record.TSConfigArtifact,
	} {
		if !cleanAbsolutePath(path) {
			return fmt.Errorf(
				"invalid %s artifact in tailscale resolver journal for service %q",
				name,
				service.ServiceName,
			)
		}
	}
	if filepath.Dir(record.TSServiceArtifact) != filepath.Join(record.ServiceRoot, "bin") ||
		!validTailscaleResolverVersionedFilename(
			filepath.Base(record.TSServiceArtifact),
			"yeet-"+service.ServiceName+"-ts-",
			".service",
		) {
		return fmt.Errorf(
			"invalid canonical Tailscale service artifact in journal for service %q",
			service.ServiceName,
		)
	}
	return nil
}

func validateTailscaleResolverJournalID(id string) error {
	if id == "" || len(id) > 128 {
		return fmt.Errorf("invalid tailscale resolver journal id %q", id)
	}
	const allowed = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-"
	if strings.IndexFunc(id, func(char rune) bool {
		return !strings.ContainsRune(allowed, char)
	}) >= 0 {
		return fmt.Errorf("invalid tailscale resolver journal id %q", id)
	}
	return nil
}

func tailscaleResolverPhaseRank(phase string) (int, bool) {
	for rank, candidate := range []string{
		tailscaleResolverPhasePrepared,
		tailscaleResolverPhaseFilesWritten,
		tailscaleResolverPhaseDaemonReloaded,
		tailscaleResolverPhaseServicesVerified,
		tailscaleResolverPhaseCommitted,
	} {
		if phase == candidate {
			return rank, true
		}
	}
	return 0, false
}

func discoverTailscaleResolverJournals(root string) ([]string, error) {
	dir := tailscaleResolverJournalDir(root)
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect tailscale resolver journal directory %s: %w", dir, err)
	}
	if err := validateTailscaleResolverJournalDirectory(dir, info); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read tailscale resolver journal directory %s: %w", dir, err)
	}
	return tailscaleResolverJournalEntryPaths(dir, entries)
}

func validateTailscaleResolverJournalDirectory(dir string, info os.FileInfo) error {
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("tailscale resolver journal directory %s must be a non-symlink directory", dir)
	}
	uid, _, err := nativeServiceFileOwner(info)
	if err != nil {
		return err
	}
	if uid != tailscaleResolverJournalOwnerUID || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("tailscale resolver journal directory %s is not private and root-owned", dir)
	}
	return nil
}

func tailscaleResolverJournalEntryPaths(
	dir string,
	entries []os.DirEntry,
) ([]string, error) {
	paths := make([]string, 0, len(entries))
	var entryErrs []error
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".jsonl") {
			entryErrs = append(
				entryErrs,
				fmt.Errorf("unexpected tailscale resolver journal entry %q", entry.Name()),
			)
			continue
		}
		paths = append(paths, filepath.Join(dir, entry.Name()))
	}
	sort.Strings(paths)
	return paths, errors.Join(entryErrs...)
}

func loadTailscaleResolverJournal(path string) (tailscaleResolverJournalContents, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return tailscaleResolverJournalContents{}, fmt.Errorf("open tailscale resolver journal %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return tailscaleResolverJournalContents{}, fmt.Errorf("inspect tailscale resolver journal %s: %w", path, err)
	}
	if err := validateTailscaleResolverJournalFileMetadata(path, info); err != nil {
		return tailscaleResolverJournalContents{}, err
	}
	contents, err := decodeTailscaleResolverJournal(file, path)
	if err != nil {
		return tailscaleResolverJournalContents{}, err
	}
	if filepath.Base(path) != contents.Header.ID+".jsonl" {
		return tailscaleResolverJournalContents{}, fmt.Errorf(
			"tailscale resolver journal filename %q does not match header id %q",
			filepath.Base(path),
			contents.Header.ID,
		)
	}
	return contents, nil
}

func validateTailscaleResolverJournalFileMetadata(path string, info os.FileInfo) error {
	uid, _, ownerErr := nativeServiceFileOwner(info)
	if ownerErr != nil {
		return ownerErr
	}
	metadata, metadataErr := serviceIdentityMetadata(info)
	if metadataErr != nil {
		return metadataErr
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 ||
		uid != tailscaleResolverJournalOwnerUID || metadata.Nlink != 1 {
		return fmt.Errorf(
			"tailscale resolver journal %s must be a single-link root-owned 0600 regular file",
			path,
		)
	}
	return nil
}

func decodeTailscaleResolverJournal(
	file *os.File,
	path string,
) (tailscaleResolverJournalContents, error) {
	reader := bufio.NewReaderSize(file, tailscaleResolverJournalMaxLine)
	var contents tailscaleResolverJournalContents
	lineNumber := 0
	for {
		line, readErr := reader.ReadSlice('\n')
		if errors.Is(readErr, bufio.ErrBufferFull) {
			return contents, fmt.Errorf(
				"tailscale resolver journal %s line %d is too large",
				path,
				lineNumber+1,
			)
		}
		if errors.Is(readErr, io.EOF) {
			if len(line) != 0 && lineNumber == 0 {
				return contents, fmt.Errorf(
					"tailscale resolver journal %s has a truncated header",
					path,
				)
			}
			break
		}
		if readErr != nil {
			return contents, fmt.Errorf("read tailscale resolver journal %s: %w", path, readErr)
		}
		lineNumber++
		if err := decodeTailscaleResolverJournalLine(&contents, lineNumber, line); err != nil {
			return contents, err
		}
	}
	if lineNumber == 0 {
		return contents, fmt.Errorf("tailscale resolver journal %s is empty", path)
	}
	return contents, nil
}

func decodeTailscaleResolverJournalLine(
	contents *tailscaleResolverJournalContents,
	lineNumber int,
	line []byte,
) error {
	line = bytes.TrimSuffix(line, []byte{'\n'})
	if lineNumber == 1 {
		if err := decodeStrictTailscaleResolverJSON(line, &contents.Header); err != nil {
			return fmt.Errorf("decode tailscale resolver journal header: %w", err)
		}
		return validateTailscaleResolverJournalHeader(contents.Header)
	}
	var phase tailscaleResolverJournalPhase
	if err := decodeStrictTailscaleResolverJSON(line, &phase); err != nil {
		return fmt.Errorf("decode tailscale resolver journal phase line %d: %w", lineNumber, err)
	}
	rank, ok := tailscaleResolverPhaseRank(phase.Phase)
	if !ok || rank != len(contents.Phases) {
		return fmt.Errorf(
			"tailscale resolver journal phase %q is illegal at line %d",
			phase.Phase,
			lineNumber,
		)
	}
	contents.Phases = append(contents.Phases, phase.Phase)
	return nil
}

func decodeStrictTailscaleResolverJSON(raw []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func removeTailscaleResolverJournal(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove tailscale resolver journal %s: %w", path, err)
	}
	if err := tailscaleResolverJournalDirSync(filepath.Dir(path)); err != nil {
		return fmt.Errorf("sync tailscale resolver journal removal %s: %w", path, err)
	}
	return nil
}
