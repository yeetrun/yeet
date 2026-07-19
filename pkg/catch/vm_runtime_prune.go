// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/yeetrun/yeet/pkg/db"
	"golang.org/x/sys/unix"
)

const (
	vmRuntimePruneActionKeep    = "keep"
	vmRuntimePruneActionRemove  = "remove"
	vmRuntimePruneActionRemoved = "removed"

	vmRuntimePruneReasonConfigured   = "configured"
	vmRuntimePruneReasonStaged       = "staged"
	vmRuntimePruneReasonPrevious     = "previous"
	vmRuntimePruneReasonRunning      = "running"
	vmRuntimePruneReasonJournal      = "active-transaction"
	vmRuntimePruneReasonStable       = "stable"
	vmRuntimePruneReasonProtected    = "protected"
	vmRuntimePruneReasonUnknown      = "unknown-reference"
	vmRuntimePruneReasonUnreferenced = "unreferenced"
)

type vmRuntimePruneRow struct {
	RuntimeID string `json:"runtimeId"`
	Path      string `json:"path"`
	Action    string `json:"action"`
	Reason    string `json:"reason"`
}

type vmRuntimePruneDeps struct {
	fetchCatalog func(context.Context) (vmRuntimeCatalog, error)
	unitState    func(context.Context, string) (vmRuntimeUnitState, error)
	processAlive func(int) bool
	uid          uint32
	gid          uint32
}

type vmRuntimePruneEntry struct {
	row          vmRuntimePruneRow
	architecture string
	manifestSHA  string
	valid        bool
}

type vmRuntimePruneReference struct {
	reason string
}

type vmRuntimePruneReferences struct {
	identities map[string]vmRuntimePruneReference
	protected  map[string]struct{}
}

func (s *Server) runtimePruneDependencies() vmRuntimePruneDeps {
	cache := vmRuntimeCache{Root: filepath.Join(s.cfg.RootDir, "vm-runtimes")}
	deps := vmRuntimePruneDeps{
		fetchCatalog: cache.FetchCatalog,
		unitState: func(ctx context.Context, service string) (vmRuntimeUnitState, error) {
			return readVMRuntimeUnitState(ctx, vmSystemdUnitName(service))
		},
		processAlive: vmRuntimeProcessAlive,
		uid:          0,
		gid:          0,
	}
	if s.vmRuntimePruneDeps == nil {
		return deps
	}
	override := *s.vmRuntimePruneDeps
	if override.fetchCatalog != nil {
		deps.fetchCatalog = override.fetchCatalog
	}
	if override.unitState != nil {
		deps.unitState = override.unitState
	}
	if override.processAlive != nil {
		deps.processAlive = override.processAlive
	}
	deps.uid = override.uid
	deps.gid = override.gid
	return deps
}

func (s *Server) pruneVMRuntimes(ctx context.Context, w io.Writer, dryRun bool) (retErr error) {
	deps := s.runtimePruneDependencies()
	catalog, err := deps.fetchCatalog(ctx)
	if err != nil {
		return fmt.Errorf("fetch VM runtime catalog for prune: %w", err)
	}
	store, err := openVMRuntimeJournalStore(ctx, s.cfg.RootDir, defaultVMRuntimeJournalStoreDeps())
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, store.Close()) }()
	groups, err := store.LoadAll()
	if err != nil {
		return fmt.Errorf("inspect VM runtime transactions for prune: %w", err)
	}
	rows, err := s.planVMRuntimePruneWithCatalogAndGroups(ctx, catalog, groups)
	if err != nil {
		return err
	}
	if !dryRun {
		rows = applyVMRuntimePruneRows(ctx, filepath.Join(s.cfg.RootDir, "vm-runtimes"), rows)
	}
	return renderVMRuntimePruneRows(w, rows)
}

func (s *Server) planVMRuntimePruneWithCatalogAndGroups(ctx context.Context, catalog vmRuntimeCatalog, groups []vmRuntimeJournalGroup) ([]vmRuntimePruneRow, error) {
	stable, ok := catalog.RuntimeForChannel("amd64", "stable")
	if !ok {
		return nil, fmt.Errorf("VM runtime catalog has no promoted stable runtime")
	}
	entries, err := listVMRuntimePruneEntries(ctx, filepath.Join(s.cfg.RootDir, "vm-runtimes"), catalog)
	if err != nil {
		return nil, err
	}
	references, err := s.collectVMRuntimePruneReferences(ctx, groups)
	if err != nil {
		return nil, err
	}
	addVMRuntimePruneReference(references.identities, stable.RuntimeID, stable.ManifestSHA, vmRuntimePruneReasonStable)

	rows := make([]vmRuntimePruneRow, 0, len(entries))
	for _, entry := range entries {
		row := entry.row
		if !entry.valid {
			rows = append(rows, row)
			continue
		}
		if reference, ok := references.identities[vmRuntimePruneIdentity(entry.row.RuntimeID, entry.manifestSHA)]; ok {
			row.Action = vmRuntimePruneActionKeep
			row.Reason = reference.reason
		} else if _, ok := references.protected[entry.row.RuntimeID]; ok {
			row.Action = vmRuntimePruneActionKeep
			row.Reason = vmRuntimePruneReasonProtected
		} else {
			row.Action = vmRuntimePruneActionRemove
			row.Reason = vmRuntimePruneReasonUnreferenced
		}
		rows = append(rows, row)
	}
	slices.SortFunc(rows, func(left, right vmRuntimePruneRow) int {
		if byID := strings.Compare(left.RuntimeID, right.RuntimeID); byID != 0 {
			return byID
		}
		return strings.Compare(left.Path, right.Path)
	})
	return rows, nil
}

func (s *Server) collectVMRuntimePruneReferences(ctx context.Context, groups []vmRuntimeJournalGroup) (vmRuntimePruneReferences, error) {
	references := vmRuntimePruneReferences{
		identities: make(map[string]vmRuntimePruneReference),
		protected:  make(map[string]struct{}),
	}
	dv, err := s.getDB()
	if err != nil {
		return references, err
	}
	data := dv.AsStruct()
	collectVMRuntimeProtectedReferences(data.VMHost, references.protected)
	names := make([]string, 0, len(data.Services))
	for name := range data.Services {
		names = append(names, name)
	}
	slices.Sort(names)
	deps := s.runtimePruneDependencies()
	for _, name := range names {
		if err := s.collectVMRuntimeServicePruneReferences(ctx, name, data.Services[name], references.identities, deps); err != nil {
			return references, err
		}
	}
	collectVMRuntimeJournalPruneReferences(groups, references.identities)
	return references, nil
}

func collectVMRuntimeProtectedReferences(host *db.VMHostConfig, protected map[string]struct{}) {
	if host == nil {
		return
	}
	for _, id := range host.ProtectedRuntimeIDs {
		if id = strings.TrimSpace(id); id != "" {
			protected[id] = struct{}{}
		}
	}
}

func (s *Server) collectVMRuntimeServicePruneReferences(ctx context.Context, name string, service *db.Service, references map[string]vmRuntimePruneReference, deps vmRuntimePruneDeps) error {
	if service == nil || service.VM == nil || service.VM.Components == nil {
		return nil
	}
	collectVMRuntimeLifecyclePruneReferences(service.VM.Components.Runtime, references)
	unit, err := deps.unitState(ctx, name)
	if err != nil {
		return fmt.Errorf("inspect VM %s before runtime prune: %w", name, err)
	}
	if unit.ActiveState != "active" {
		return nil
	}
	return s.collectRunningVMRuntimePruneReference(name, service, unit, references, deps)
}

func (s *Server) collectRunningVMRuntimePruneReference(name string, service *db.Service, unit vmRuntimeUnitState, references map[string]vmRuntimePruneReference, deps vmRuntimePruneDeps) error {
	if unit.MainPID <= 0 || !deps.processAlive(unit.MainPID) {
		return fmt.Errorf("active VM %s has no live runner during runtime prune", name)
	}
	root := serviceRootFromConfig(s.cfg, *service)
	marker, err := readTrustedVMRuntimeRunningMarker(filepath.Join(serviceRunDirForRoot(root), vmRuntimeRunningMarkerFileName), name, deps.uid, deps.gid)
	if err != nil {
		return fmt.Errorf("read active VM %s runtime marker for prune: %w", name, err)
	}
	if marker.RunnerPID != unit.MainPID || !deps.processAlive(marker.RunnerPID) || !deps.processAlive(marker.ChildPID) {
		return fmt.Errorf("active VM %s runtime marker is stale during prune", name)
	}
	addVMRuntimePruneReference(references, marker.RuntimeID, marker.ManifestSHA256, vmRuntimePruneReasonRunning)
	return nil
}

func collectVMRuntimeLifecyclePruneReferences(runtime db.VMRuntimeLifecycleConfig, references map[string]vmRuntimePruneReference) {
	addVMRuntimePruneArtifactReference(references, runtime.Configured, vmRuntimePruneReasonConfigured)
	if runtime.Staged != nil {
		addVMRuntimePruneArtifactReference(references, *runtime.Staged, vmRuntimePruneReasonStaged)
	}
	if runtime.Previous != nil {
		addVMRuntimePruneArtifactReference(references, *runtime.Previous, vmRuntimePruneReasonPrevious)
	}
}

func collectVMRuntimeJournalPruneReferences(groups []vmRuntimeJournalGroup, references map[string]vmRuntimePruneReference) {
	for _, group := range groups {
		for _, record := range group.Records {
			for _, projection := range []vmRuntimeJournalDBProjection{record.OldDB, record.NewDB} {
				if projection.Components != nil {
					for _, artifact := range vmRuntimeLifecycleArtifacts(projection.Components.Runtime) {
						addVMRuntimePruneArtifactReference(references, artifact, vmRuntimePruneReasonJournal)
					}
				}
			}
		}
	}
}

func addVMRuntimePruneArtifactReference(references map[string]vmRuntimePruneReference, artifact db.VMRuntimeArtifactConfig, reason string) {
	addVMRuntimePruneReference(references, artifact.ID, artifact.ManifestSHA256, reason)
}

func addVMRuntimePruneReference(references map[string]vmRuntimePruneReference, runtimeID, manifestSHA, reason string) {
	runtimeID = strings.TrimSpace(runtimeID)
	manifestSHA = strings.TrimSpace(manifestSHA)
	if runtimeID == "" || !isLowerSHA256(manifestSHA) {
		return
	}
	key := vmRuntimePruneIdentity(runtimeID, manifestSHA)
	if current, ok := references[key]; !ok || vmRuntimePruneReasonPriority(reason) < vmRuntimePruneReasonPriority(current.reason) {
		references[key] = vmRuntimePruneReference{reason: reason}
	}
}

func vmRuntimePruneReasonPriority(reason string) int {
	for index, candidate := range []string{
		vmRuntimePruneReasonConfigured, vmRuntimePruneReasonStaged, vmRuntimePruneReasonPrevious,
		vmRuntimePruneReasonRunning, vmRuntimePruneReasonJournal, vmRuntimePruneReasonStable,
	} {
		if reason == candidate {
			return index
		}
	}
	return 100
}

func vmRuntimePruneIdentity(runtimeID, manifestSHA string) string {
	return runtimeID + "\x00" + manifestSHA
}

func listVMRuntimePruneEntries(ctx context.Context, root string, catalog vmRuntimeCatalog) ([]vmRuntimePruneEntry, error) {
	root, rootEntries, err := openVMRuntimePruneRoot(ctx, root)
	if err != nil {
		return nil, err
	}
	if rootEntries == nil {
		return nil, nil
	}
	var entries []vmRuntimePruneEntry
	for _, architectureEntry := range rootEntries {
		if architectureEntry.Name() == vmRuntimeLocalAliasDirname && architectureEntry.IsDir() {
			continue
		}
		architectureEntries, err := listVMRuntimePruneArchitecture(ctx, root, architectureEntry, catalog)
		if err != nil {
			return nil, err
		}
		entries = append(entries, architectureEntries...)
	}
	return entries, nil
}

func openVMRuntimePruneRoot(ctx context.Context, root string) (string, []os.DirEntry, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	root, err := validatedVMRuntimeCacheRoot(root)
	if err != nil {
		return "", nil, err
	}
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return root, nil, nil
	} else if err != nil {
		return "", nil, fmt.Errorf("inspect VM runtime cache root: %w", err)
	}
	if err := validateTrustedVMRuntimeCachePath(root, true); err != nil {
		return "", nil, err
	}
	if err := validateVMRuntimePruneAliases(root); err != nil {
		return "", nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", nil, fmt.Errorf("read VM runtime cache root: %w", err)
	}
	return root, entries, nil
}

func listVMRuntimePruneArchitecture(ctx context.Context, root string, architectureEntry os.DirEntry, catalog vmRuntimeCatalog) ([]vmRuntimePruneEntry, error) {
	architecturePath := filepath.Join(root, architectureEntry.Name())
	architecture, normalizeErr := normalizeVMRuntimeArchitecture(architectureEntry.Name())
	if normalizeErr != nil || architecture != architectureEntry.Name() || !architectureEntry.IsDir() || architectureEntry.Type()&os.ModeSymlink != 0 {
		return []vmRuntimePruneEntry{unknownVMRuntimePruneEntry(architecturePath, "")}, nil
	}
	if err := validateTrustedVMRuntimeCachePath(architecturePath, true); err != nil {
		return []vmRuntimePruneEntry{unknownVMRuntimePruneEntry(architecturePath, "")}, nil
	}
	runtimeEntries, err := os.ReadDir(architecturePath)
	if err != nil {
		return nil, fmt.Errorf("read VM runtime architecture directory: %w", err)
	}
	var entries []vmRuntimePruneEntry
	for _, runtimeEntry := range runtimeEntries {
		runtimeLeaves, err := listVMRuntimePruneRuntime(ctx, architecturePath, architecture, runtimeEntry, catalog)
		if err != nil {
			return nil, err
		}
		entries = append(entries, runtimeLeaves...)
	}
	return entries, nil
}

func listVMRuntimePruneRuntime(ctx context.Context, architecturePath, architecture string, runtimeEntry os.DirEntry, catalog vmRuntimeCatalog) ([]vmRuntimePruneEntry, error) {
	runtimePath := filepath.Join(architecturePath, runtimeEntry.Name())
	if _, err := vmRuntimeVersionFromID(runtimeEntry.Name()); err != nil || !runtimeEntry.IsDir() || runtimeEntry.Type()&os.ModeSymlink != 0 {
		return []vmRuntimePruneEntry{unknownVMRuntimePruneEntry(runtimePath, runtimeEntry.Name())}, nil
	}
	if err := validateTrustedVMRuntimeCachePath(runtimePath, true); err != nil {
		return []vmRuntimePruneEntry{unknownVMRuntimePruneEntry(runtimePath, runtimeEntry.Name())}, nil
	}
	digestEntries, err := os.ReadDir(runtimePath)
	if err != nil {
		return nil, fmt.Errorf("read VM runtime identity directory: %w", err)
	}
	if len(digestEntries) == 0 {
		return []vmRuntimePruneEntry{unknownVMRuntimePruneEntry(runtimePath, runtimeEntry.Name())}, nil
	}
	entries := make([]vmRuntimePruneEntry, 0, len(digestEntries))
	for _, digestEntry := range digestEntries {
		entries = append(entries, inspectVMRuntimePruneLeaf(ctx, runtimePath, architecture, runtimeEntry.Name(), digestEntry, catalog))
	}
	return entries, nil
}

func inspectVMRuntimePruneLeaf(ctx context.Context, runtimePath, architecture, runtimeID string, digestEntry os.DirEntry, catalog vmRuntimeCatalog) vmRuntimePruneEntry {
	leaf := filepath.Join(runtimePath, digestEntry.Name())
	entry := unknownVMRuntimePruneEntry(leaf, runtimeID)
	if !isLowerSHA256(digestEntry.Name()) || !digestEntry.IsDir() || digestEntry.Type()&os.ModeSymlink != 0 {
		return entry
	}
	if _, err := validateVMRuntimePruneLeaf(ctx, leaf, architecture, runtimeID, digestEntry.Name(), catalog); err == nil {
		entry.architecture = architecture
		entry.manifestSHA = digestEntry.Name()
		entry.valid = true
	}
	return entry
}

func unknownVMRuntimePruneEntry(path, runtimeID string) vmRuntimePruneEntry {
	return vmRuntimePruneEntry{row: vmRuntimePruneRow{
		RuntimeID: runtimeID, Path: path, Action: vmRuntimePruneActionKeep, Reason: vmRuntimePruneReasonUnknown,
	}}
}

func validateVMRuntimePruneLeaf(ctx context.Context, path, architecture, runtimeID, manifestSHA string, catalog vmRuntimeCatalog) (db.VMRuntimeArtifactConfig, error) {
	if err := validateTrustedVMRuntimeCachePath(path, true); err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}
	manifestPath := filepath.Join(path, vmRuntimeManifestFilename)
	if err := validateTrustedVMRuntimeCachePath(manifestPath, false); err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}
	if vmRuntimeSHA256Bytes(raw) != manifestSHA {
		return db.VMRuntimeArtifactConfig{}, fmt.Errorf("VM runtime manifest path digest does not match contents")
	}
	manifest, err := decodeVMRuntimeManifest(raw)
	if err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}
	if manifest.Architecture != architecture || manifest.RuntimeID != runtimeID {
		return db.VMRuntimeArtifactConfig{}, fmt.Errorf("VM runtime cache path does not match manifest identity")
	}
	ref := vmRuntimeCatalogRef{
		RuntimeID: runtimeID, ManifestSHA: manifestSHA, UpstreamVersion: manifest.Upstream.Version, Support: manifest.Support.State,
	}
	if official, ok := catalog.RuntimeByID(architecture, runtimeID); ok && official.ManifestSHA == manifestSHA {
		ref = official
	}
	return validateVMRuntimeImportedCachedArtifact(ctx, path, ref)
}

func applyVMRuntimePruneRows(ctx context.Context, root string, rows []vmRuntimePruneRow) []vmRuntimePruneRow {
	out := append([]vmRuntimePruneRow(nil), rows...)
	for index := range out {
		if out[index].Action != vmRuntimePruneActionRemove {
			continue
		}
		if err := quarantineAndRemoveVMRuntimeLeaf(ctx, root, out[index]); err != nil {
			out[index].Action = vmRuntimePruneActionKeep
			out[index].Reason = vmRuntimePruneReasonUnknown
			continue
		}
		out[index].Action = vmRuntimePruneActionRemoved
	}
	return out
}

func quarantineAndRemoveVMRuntimeLeaf(ctx context.Context, root string, row vmRuntimePruneRow) (retErr error) {
	root, path, parts, err := validateVMRuntimePruneRemovalTarget(ctx, root, row)
	if err != nil {
		return err
	}
	parentPath := filepath.Dir(path)
	parent, err := os.Open(parentPath)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, parent.Close()) }()
	if err := validateTrustedVMRuntimeCachePath(parentPath, true); err != nil {
		return err
	}
	if err := quarantineAndDeleteVMRuntimePruneLeaf(parent, parentPath, filepath.Base(path)); err != nil {
		return err
	}
	if err := removeVMRuntimePruneAliases(root, parts[1], parts[2]); err != nil {
		return err
	}
	removeEmptyVMRuntimePruneParents(root, parentPath)
	return nil
}

func validateVMRuntimePruneRemovalTarget(ctx context.Context, root string, row vmRuntimePruneRow) (string, string, []string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", nil, err
	}
	root, err := validatedVMRuntimeCacheRoot(root)
	if err != nil {
		return "", "", nil, err
	}
	path := filepath.Clean(row.Path)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", nil, fmt.Errorf("VM runtime prune path is outside cache root")
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 3 || parts[1] != row.RuntimeID || !isLowerSHA256(parts[2]) {
		return "", "", nil, fmt.Errorf("VM runtime prune path is not an immutable cache leaf")
	}
	catalog := vmRuntimeCatalog{Architectures: map[string]vmRuntimeCatalogArchitecture{}}
	if _, err := validateVMRuntimePruneLeaf(ctx, path, parts[0], parts[1], parts[2], catalog); err != nil {
		return "", "", nil, err
	}
	return root, path, parts, nil
}

func quarantineAndDeleteVMRuntimePruneLeaf(parent *os.File, parentPath, name string) error {
	wantID, stat, err := vmJailerNameIdentityAt(parent, name)
	if err != nil {
		return err
	}
	if uint32(stat.Mode)&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("VM runtime prune leaf is not a directory")
	}
	quarantineName, err := newVMRuntimePruneQuarantineName(name)
	if err != nil {
		return err
	}
	if err := renameVMJailerUnitNameNoReplaceAt(int(parent.Fd()), name, int(parent.Fd()), quarantineName); err != nil {
		return fmt.Errorf("quarantine VM runtime cache leaf: %w", err)
	}
	if err := parent.Sync(); err != nil {
		return fmt.Errorf("sync VM runtime quarantine: %w", err)
	}
	if err := validateVMRuntimePruneQuarantine(parent, quarantineName, wantID); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(parentPath, quarantineName)); err != nil {
		return fmt.Errorf("remove quarantined VM runtime: %w", err)
	}
	if err := parent.Sync(); err != nil {
		return fmt.Errorf("sync VM runtime removal: %w", err)
	}
	return nil
}

func validateVMRuntimePruneQuarantine(parent *os.File, name string, wantID vmJailerFileIdentity) error {
	gotID, gotStat, err := vmJailerNameIdentityAt(parent, name)
	if err != nil || gotID != wantID || uint32(gotStat.Mode)&unix.S_IFMT != unix.S_IFDIR {
		return errors.Join(fmt.Errorf("VM runtime quarantine identity changed"), err)
	}
	return nil
}

func validateVMRuntimePruneAliases(root string) error {
	aliasDir := filepath.Join(root, vmRuntimeLocalAliasDirname)
	if _, err := os.Lstat(aliasDir); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect VM runtime local aliases for prune: %w", err)
	}
	if err := validateVMRuntimeLocalAliasDir(aliasDir); err != nil {
		return fmt.Errorf("validate VM runtime local aliases for prune: %w", err)
	}
	entries, err := os.ReadDir(aliasDir)
	if err != nil {
		return fmt.Errorf("read VM runtime local aliases for prune: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(entry.Name(), ".json") {
			return fmt.Errorf("VM runtime local aliases contain unknown entry %q", entry.Name())
		}
		if _, err := readVMRuntimeLocalAlias(filepath.Join(aliasDir, entry.Name())); err != nil {
			return fmt.Errorf("read VM runtime local alias %s for prune: %w", entry.Name(), err)
		}
	}
	return nil
}

func removeVMRuntimePruneAliases(root, runtimeID, manifestSHA string) (retErr error) {
	aliasDir := filepath.Join(root, vmRuntimeLocalAliasDirname)
	if _, err := os.Lstat(aliasDir); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := validateVMRuntimeLocalAliasDir(aliasDir); err != nil {
		return err
	}
	dir, err := os.Open(aliasDir)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, dir.Close()) }()
	entries, err := os.ReadDir(aliasDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := removeMatchingVMRuntimePruneAlias(dir, aliasDir, entry.Name(), runtimeID, manifestSHA); err != nil {
			return err
		}
	}
	return dir.Sync()
}

func removeMatchingVMRuntimePruneAlias(dir *os.File, aliasDir, name, runtimeID, manifestSHA string) error {
	alias, err := readVMRuntimeLocalAlias(filepath.Join(aliasDir, name))
	if err != nil {
		return err
	}
	if alias.RuntimeID != runtimeID || alias.ManifestSHA256 != manifestSHA {
		return nil
	}
	identity, _, err := vmJailerNameIdentityAt(dir, name)
	if err != nil {
		return err
	}
	if err := unlinkVMJailerNameIfIdentityWith(dir, name, identity, unix.Unlinkat); err != nil {
		return fmt.Errorf("remove VM runtime local alias %q: %w", alias.Name, err)
	}
	return nil
}

func newVMRuntimePruneQuarantineName(name string) (string, error) {
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate VM runtime prune quarantine name: %w", err)
	}
	return "." + name + ".prune-" + hex.EncodeToString(random[:]), nil
}

func removeEmptyVMRuntimePruneParents(root, runtimeDir string) {
	_ = os.Remove(runtimeDir)
	architectureDir := filepath.Dir(runtimeDir)
	if architectureDir != root {
		_ = os.Remove(architectureDir)
	}
}

func renderVMRuntimePruneRows(w io.Writer, rows []vmRuntimePruneRow) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintln(w, "No VM runtimes to prune.")
		return err
	}
	table := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(table, "RUNTIME\tACTION\tPATH\tREASON"); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", dash(row.RuntimeID), row.Action, row.Path, row.Reason); err != nil {
			return err
		}
	}
	return table.Flush()
}
