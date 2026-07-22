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
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/yeetrun/yeet/pkg/db"
)

const (
	vmImagePruneKindCache     = "cache"
	vmImagePruneKindZFSBase   = "zfs-base"
	vmImagePruneKindGuestBase = "guest-base"
	vmImagePruneKindKernel    = "kernel"

	vmImagePruneStateCurrent  = "current"
	vmImagePruneStateInUse    = "in-use"
	vmImagePruneStatePrunable = "prunable"
	vmImagePruneStateRemoved  = "removed"
	vmImagePruneStateSkipped  = "skipped"
)

var fetchVMImagePruneComponentCatalogs = func(ctx context.Context, refs *vmImageComponentCatalogs) (vmImageGuestKernelCatalogs, error) {
	return fetchVMImageGuestKernelCatalogs(ctx, nil, refs, true)
}

type vmImagePruneRow struct {
	Kind    string `json:"kind"`
	State   string `json:"state"`
	Payload string `json:"payload,omitempty"`
	Version string `json:"version,omitempty"`
	Path    string `json:"path,omitempty"`
	Reason  string `json:"reason,omitempty"`

	componentArchitecture   string
	componentManifestSHA256 string
	guestBaseRef            *vmGuestBaseCatalogRef
	kernelRef               *vmKernelCatalogRef
}

type cachedVMImagePruneEntry struct {
	Version string
	Dir     string
}

type vmImageZFSBase struct {
	Version       string
	Dataset       string
	Snapshot      string
	ParentDataset string
}

func (s *Server) planVMImagePrune(ctx context.Context, cache vmImageCache) ([]vmImagePruneRow, error) {
	catalog, err := cache.FetchCatalog(ctx)
	if err != nil {
		return nil, err
	}
	return s.planVMImagePruneWithCatalog(ctx, cache, catalog)
}

func (s *Server) planVMImagePruneWithCatalog(ctx context.Context, cache vmImageCache, catalog vmImageCatalog) ([]vmImagePruneRow, error) {
	cacheEntries, err := listCachedVMImagePruneEntries(cache.Root, catalog)
	if err != nil {
		return nil, err
	}
	zfsBases, err := s.listVMImageZFSBases(ctx, catalog)
	if err != nil {
		return nil, err
	}
	currentVersions := currentVMImagePruneVersions(cacheEntries, zfsBases, catalog)
	inUseVersions, err := s.inUseVMImageVersions()
	if err != nil {
		return nil, err
	}

	rows := make([]vmImagePruneRow, 0, len(cacheEntries)+len(zfsBases))
	for _, entry := range cacheEntries {
		rows = append(rows, classifyVMImagePruneRow(vmImagePruneKindCache, entry.Version, entry.Dir, currentVersions, inUseVersions, false, nil, catalog))
	}
	runner := s.vmImagePruneZFSRunner()
	for _, base := range zfsBases {
		hasClones := false
		var cloneErr error
		if !vmImageVersionIsCurrent(currentVersions, base.Version, catalog) && !vmImageVersionInUse(inUseVersions, base.Version) {
			hasClones, cloneErr = vmImageZFSSnapshotHasClones(ctx, runner, base.Snapshot)
		}
		rows = append(rows, classifyVMImagePruneRow(vmImagePruneKindZFSBase, base.Version, base.Dataset, currentVersions, inUseVersions, hasClones, cloneErr, catalog))
	}
	if catalog.ComponentCatalogs != nil {
		componentCatalogs, err := fetchVMImagePruneComponentCatalogs(ctx, catalog.ComponentCatalogs)
		if err != nil {
			return nil, err
		}
		componentRows, err := s.planVMComponentImagePrune(componentCatalogs)
		if err != nil {
			return nil, err
		}
		rows = append(rows, componentRows...)
	}
	sortVMImagePruneRows(rows)
	return rows, nil
}

func (s *Server) planVMComponentImagePrune(catalogs vmImageGuestKernelCatalogs) ([]vmImagePruneRow, error) {
	inUse, err := s.inUseVMImageComponents()
	if err != nil {
		return nil, err
	}
	current := currentVMImageComponents(catalogs)
	guestEntries, err := listCachedVMGuestBasePruneEntries(s.vmGuestBaseCache(), catalogs.GuestBases)
	if err != nil {
		return nil, err
	}
	kernelEntries, err := listCachedVMKernelPruneEntries(s.vmKernelArtifactCache(), catalogs.Kernels)
	if err != nil {
		return nil, err
	}
	rows := make([]vmImagePruneRow, 0, len(guestEntries)+len(kernelEntries))
	for _, entry := range guestEntries {
		rows = append(rows, classifyVMComponentImagePruneRow(vmImagePruneKindGuestBase, entry, current, inUse))
	}
	for _, entry := range kernelEntries {
		rows = append(rows, classifyVMComponentImagePruneRow(vmImagePruneKindKernel, entry, current, inUse))
	}
	return rows, nil
}

type vmComponentImagePruneEntry struct {
	id             string
	architecture   string
	manifestSHA256 string
	path           string
	payload        string
	guestBaseRef   *vmGuestBaseCatalogRef
	kernelRef      *vmKernelCatalogRef
}

func listCachedVMGuestBasePruneEntries(cache vmGuestBaseCache, catalog vmGuestBaseCatalog) ([]vmComponentImagePruneEntry, error) {
	entries := make([]vmComponentImagePruneEntry, 0, len(catalog.GuestBases))
	for _, ref := range catalog.GuestBases {
		path := filepath.Join(cache.Root, ref.Architecture, ref.GuestBaseID, ref.ManifestSHA256)
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf("inspect cached VM guest base %s: %w", ref.GuestBaseID, err)
		}
		if _, err := validateVMGuestBaseArtifactDirectory(path, ref); err != nil {
			return nil, fmt.Errorf("validate cached VM guest base %s before prune: %w", ref.GuestBaseID, err)
		}
		refCopy := ref
		entries = append(entries, vmComponentImagePruneEntry{
			id: ref.GuestBaseID, architecture: ref.Architecture, manifestSHA256: ref.ManifestSHA256, path: path,
			payload: vmImagePayloadPrefix + ref.OS + "/" + ref.OSVersion, guestBaseRef: &refCopy,
		})
	}
	return entries, nil
}

func listCachedVMKernelPruneEntries(cache vmKernelArtifactCache, catalog vmKernelCatalog) ([]vmComponentImagePruneEntry, error) {
	entries := make([]vmComponentImagePruneEntry, 0, len(catalog.Kernels))
	for _, ref := range catalog.Kernels {
		path := filepath.Join(cache.Root, ref.Architecture, ref.KernelID, ref.ManifestSHA256)
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf("inspect cached VM kernel %s: %w", ref.KernelID, err)
		}
		if _, err := validateVMKernelArtifactDirectory(path, ref); err != nil {
			return nil, fmt.Errorf("validate cached VM kernel %s before prune: %w", ref.KernelID, err)
		}
		refCopy := ref
		entries = append(entries, vmComponentImagePruneEntry{
			id: ref.KernelID, architecture: ref.Architecture, manifestSHA256: ref.ManifestSHA256,
			path: path, payload: ref.Architecture, kernelRef: &refCopy,
		})
	}
	return entries, nil
}

func currentVMImageComponents(catalogs vmImageGuestKernelCatalogs) map[string]struct{} {
	current := map[string]struct{}{}
	for _, channels := range catalogs.GuestBases.Channels {
		for _, identity := range []*vmGuestBaseCatalogIdentity{channels.Stable, channels.Candidate} {
			if identity != nil {
				current[vmImageComponentPruneKey(vmImagePruneKindGuestBase, identity.GuestBaseID, identity.ManifestSHA256)] = struct{}{}
			}
		}
	}
	for _, channels := range catalogs.Kernels.Channels {
		for _, identity := range []*vmKernelCatalogIdentity{channels.Stable, channels.Candidate} {
			if identity != nil {
				current[vmImageComponentPruneKey(vmImagePruneKindKernel, identity.KernelID, identity.ManifestSHA256)] = struct{}{}
			}
		}
	}
	return current
}

func (s *Server) inUseVMImageComponents() (map[string]struct{}, error) {
	inUse := map[string]struct{}{}
	if s == nil || s.cfg.DB == nil {
		return inUse, nil
	}
	dv, err := s.getDB()
	if err != nil {
		return nil, err
	}
	for _, sv := range dv.Services().All() {
		if sv.ServiceType() != db.ServiceTypeVM || !sv.VM().Valid() || !sv.VM().Components().Valid() {
			continue
		}
		components := sv.VM().Components()
		guest := components.GuestBase()
		kernel := components.Kernel()
		if guest.ID != "" && guest.ManifestSHA256 != "" {
			inUse[vmImageComponentPruneKey(vmImagePruneKindGuestBase, guest.ID, guest.ManifestSHA256)] = struct{}{}
		}
		if kernel.ID != "" && kernel.ManifestSHA256 != "" {
			inUse[vmImageComponentPruneKey(vmImagePruneKindKernel, kernel.ID, kernel.ManifestSHA256)] = struct{}{}
		}
	}
	return inUse, nil
}

func vmImageComponentPruneKey(kind, id, manifestSHA256 string) string {
	return kind + "\x00" + id + "\x00" + manifestSHA256
}

func classifyVMComponentImagePruneRow(kind string, entry vmComponentImagePruneEntry, current, inUse map[string]struct{}) vmImagePruneRow {
	row := vmImagePruneRow{
		Kind: kind, Version: entry.id, Path: entry.path, Payload: entry.payload,
		componentArchitecture: entry.architecture, componentManifestSHA256: entry.manifestSHA256,
		guestBaseRef: entry.guestBaseRef, kernelRef: entry.kernelRef,
	}
	key := vmImageComponentPruneKey(kind, entry.id, entry.manifestSHA256)
	if _, ok := current[key]; ok {
		row.State = vmImagePruneStateCurrent
		row.Reason = "promoted component catalog entry"
	} else if _, ok := inUse[key]; ok {
		row.State = vmImagePruneStateInUse
		row.Reason = "referenced by a VM component lock"
	} else {
		row.State = vmImagePruneStatePrunable
		row.Reason = "unreferenced immutable component"
	}
	return row
}

func listCachedVMImagePruneEntries(root string, catalog vmImageCatalog) ([]cachedVMImagePruneEntry, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("VM image cache root is required")
	}
	entries, err := readVMImageCacheEntries(root)
	if err != nil {
		return nil, err
	}
	var cached []cachedVMImagePruneEntry
	for _, entry := range entries {
		manifest, dir, ok, err := cachedVMImageManifestFromEntry(root, entry)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if !isManagedVMImagePruneVersion(manifest.Version, catalog) {
			continue
		}
		cached = append(cached, cachedVMImagePruneEntry{Version: manifest.Version, Dir: dir})
	}
	return cached, nil
}

func (s *Server) listVMImageZFSBases(ctx context.Context, catalog vmImageCatalog) ([]vmImageZFSBase, error) {
	runner := s.vmImagePruneZFSRunner()
	if runner == nil {
		return nil, nil
	}
	stdout, stderr, err := runner(ctx, "list", "-H", "-o", "name", "-t", "volume")
	if err != nil {
		if isZFSListUnavailable(stderr, err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list ZFS VM image bases: %w: %s", err, strings.TrimSpace(stderr))
	}
	var bases []vmImageZFSBase
	for _, line := range strings.Split(stdout, "\n") {
		base, ok := parseVMImageZFSBaseDataset(strings.TrimSpace(line), catalog)
		if ok {
			bases = append(bases, base)
		}
	}
	return bases, nil
}

func (s *Server) vmImagePruneZFSRunner() zfsCommandRunner {
	if s != nil && s.zfsRunner != nil {
		return s.zfsRunner
	}
	if _, err := exec.LookPath("zfs"); err != nil {
		return nil
	}
	return runZFSCommand
}

func isZFSListUnavailable(stderr string, err error) bool {
	text := strings.ToLower(strings.TrimSpace(stderr + " " + err.Error()))
	return strings.Contains(text, "executable file not found") ||
		strings.Contains(text, "command not found") ||
		strings.Contains(text, "no such file or directory") ||
		strings.Contains(text, "no datasets available") ||
		strings.Contains(text, "the zfs modules are not loaded")
}

func parseVMImageZFSBaseDataset(name string, catalog vmImageCatalog) (vmImageZFSBase, bool) {
	base, ok := parseVMImageZFSBaseDatasetPath(name)
	if !ok {
		return vmImageZFSBase{}, false
	}
	if !isManagedVMImagePruneVersion(base.Version, catalog) {
		return vmImageZFSBase{}, false
	}
	return base, true
}

func parseVMImageZFSBaseDatasetPath(name string) (vmImageZFSBase, bool) {
	const marker = "/yeet/vm-images/"
	if name == "" || !strings.HasSuffix(name, "/root") {
		return vmImageZFSBase{}, false
	}
	idx := strings.Index(name, marker)
	if idx <= 0 {
		return vmImageZFSBase{}, false
	}
	version := strings.TrimSuffix(name[idx+len(marker):], "/root")
	if err := validateVMImageCacheDirName(version); err != nil {
		return vmImageZFSBase{}, false
	}
	parent := strings.TrimSuffix(name, "/root")
	return vmImageZFSBase{
		Version:       version,
		Dataset:       name,
		Snapshot:      name + "@" + version,
		ParentDataset: parent,
	}, true
}

func isManagedVMImagePruneVersion(version string, catalog vmImageCatalog) bool {
	_, ok := catalog.ImageByVersion(version)
	return ok
}

func currentVMImagePruneVersions(cacheEntries []cachedVMImagePruneEntry, zfsBases []vmImageZFSBase, catalog vmImageCatalog) map[string]string {
	current := map[string]string{}
	consider := func(version string) {
		image, ok := catalog.ImageByVersion(version)
		if !ok {
			return
		}
		if current[image.Payload] == "" || compareVMImageVersions(version, current[image.Payload]) > 0 {
			current[image.Payload] = version
		}
	}
	for _, entry := range cacheEntries {
		consider(entry.Version)
	}
	for _, base := range zfsBases {
		consider(base.Version)
	}
	return current
}

func vmImageVersionIsCurrent(currentVersions map[string]string, version string, catalog vmImageCatalog) bool {
	image, ok := catalog.ImageByVersion(version)
	return ok && currentVersions[image.Payload] == version
}

func (s *Server) inUseVMImageVersions() (map[string]struct{}, error) {
	versions := map[string]struct{}{}
	if s == nil || s.cfg.DB == nil {
		return versions, nil
	}
	dv, err := s.getDB()
	if err != nil {
		return nil, err
	}
	for _, sv := range dv.Services().All() {
		if sv.ServiceType() != db.ServiceTypeVM {
			continue
		}
		vm := sv.VM()
		if !vm.Valid() {
			continue
		}
		version := strings.TrimSpace(vm.Image().Version)
		if version != "" {
			versions[version] = struct{}{}
		}
	}
	return versions, nil
}

func vmImageVersionInUse(inUse map[string]struct{}, version string) bool {
	_, ok := inUse[version]
	return ok
}

func vmImageZFSSnapshotHasClones(ctx context.Context, runner zfsCommandRunner, snapshot string) (bool, error) {
	if runner == nil {
		return false, fmt.Errorf("zfs is not available")
	}
	stdout, stderr, err := runner(ctx, "get", "-H", "-o", "value", "clones", snapshot)
	if err != nil {
		return false, fmt.Errorf("inspect ZFS clones for %s: %w: %s", snapshot, err, strings.TrimSpace(stderr))
	}
	value := strings.TrimSpace(stdout)
	return value != "" && value != "-", nil
}

func classifyVMImagePruneRow(kind, version, path string, currentVersions map[string]string, inUse map[string]struct{}, hasClones bool, cloneErr error, catalog vmImageCatalog) vmImagePruneRow {
	row := vmImagePruneRow{Kind: kind, Version: version, Path: path}
	if image, ok := catalog.ImageByVersion(version); ok {
		row.Payload = image.Payload
	}
	switch {
	case vmImageVersionIsCurrent(currentVersions, version, catalog):
		row.State = vmImagePruneStateCurrent
		row.Reason = "newest cached version"
	case vmImageVersionInUse(inUse, version):
		row.State = vmImagePruneStateInUse
		row.Reason = "referenced by a VM"
	case cloneErr != nil:
		row.State = vmImagePruneStateSkipped
		row.Reason = cloneErr.Error()
	case hasClones:
		row.State = vmImagePruneStateInUse
		row.Reason = "has ZFS clones"
	default:
		row.State = vmImagePruneStatePrunable
		row.Reason = "old unreferenced version"
	}
	return row
}

func sortVMImagePruneRows(rows []vmImagePruneRow) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Kind != rows[j].Kind {
			return rows[i].Kind < rows[j].Kind
		}
		if rows[i].Version != rows[j].Version {
			return compareVMImageVersions(rows[i].Version, rows[j].Version) < 0
		}
		return rows[i].Path < rows[j].Path
	})
}

func vmImagePruneRowsHavePrunable(rows []vmImagePruneRow) bool {
	for _, row := range rows {
		if row.State == vmImagePruneStatePrunable {
			return true
		}
	}
	return false
}

func (s *Server) applyVMImagePrune(ctx context.Context, rows []vmImagePruneRow) []vmImagePruneRow {
	out := append([]vmImagePruneRow(nil), rows...)
	for i := range out {
		if out[i].State != vmImagePruneStatePrunable {
			continue
		}
		var err error
		switch out[i].Kind {
		case vmImagePruneKindCache:
			err = os.RemoveAll(out[i].Path)
		case vmImagePruneKindGuestBase, vmImagePruneKindKernel:
			err = s.quarantineAndRemoveVMImageComponentLeaf(ctx, out[i])
		case vmImagePruneKindZFSBase:
			err = s.destroyVMImageZFSBase(ctx, out[i])
		default:
			err = fmt.Errorf("unknown VM image prune kind %q", out[i].Kind)
		}
		if err != nil {
			out[i].State = vmImagePruneStateSkipped
			out[i].Reason = err.Error()
			continue
		}
		out[i].State = vmImagePruneStateRemoved
		out[i].Reason = ""
	}
	return out
}

func (s *Server) quarantineAndRemoveVMImageComponentLeaf(ctx context.Context, row vmImagePruneRow) (retErr error) {
	root, path, err := s.validateVMImageComponentPruneTarget(ctx, row)
	if err != nil {
		return err
	}
	lock := vmRuntimeCacheLock(path)
	lock.Lock()
	defer lock.Unlock()
	if _, _, err := s.validateVMImageComponentPruneTarget(ctx, row); err != nil {
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
	removeEmptyVMComponentPruneParents(root, parentPath)
	return nil
}

func (s *Server) validateVMImageComponentPruneTarget(ctx context.Context, row vmImagePruneRow) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	var rawRoot, label string
	switch row.Kind {
	case vmImagePruneKindGuestBase:
		rawRoot, label = s.vmGuestBaseCache().Root, "guest-base"
	case vmImagePruneKindKernel:
		rawRoot, label = s.vmKernelArtifactCache().Root, "kernel"
	default:
		return "", "", fmt.Errorf("unsupported VM component prune kind %q", row.Kind)
	}
	root, err := validatedVMComponentCacheRoot(rawRoot, label)
	if err != nil {
		return "", "", err
	}
	if err := validateTrustedVMRuntimeCachePath(root, true); err != nil {
		return "", "", err
	}
	path := filepath.Clean(row.Path)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("VM %s prune path is outside cache root", label)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 3 ||
		parts[0] != row.componentArchitecture ||
		parts[1] != row.Version ||
		parts[2] != row.componentManifestSHA256 ||
		!isLowerSHA256(parts[2]) {
		return "", "", fmt.Errorf("VM %s prune path is not an immutable cache leaf", label)
	}
	switch row.Kind {
	case vmImagePruneKindGuestBase:
		if row.guestBaseRef == nil ||
			row.guestBaseRef.Architecture != parts[0] ||
			row.guestBaseRef.GuestBaseID != parts[1] ||
			row.guestBaseRef.ManifestSHA256 != parts[2] {
			return "", "", fmt.Errorf("VM guest-base prune row lacks its exact catalog identity")
		}
		if _, err := validateVMGuestBaseArtifactDirectory(path, *row.guestBaseRef); err != nil {
			return "", "", err
		}
	case vmImagePruneKindKernel:
		if row.kernelRef == nil ||
			row.kernelRef.Architecture != parts[0] ||
			row.kernelRef.KernelID != parts[1] ||
			row.kernelRef.ManifestSHA256 != parts[2] {
			return "", "", fmt.Errorf("VM kernel prune row lacks its exact catalog identity")
		}
		if _, err := validateVMKernelArtifactDirectory(path, *row.kernelRef); err != nil {
			return "", "", err
		}
	}
	return root, path, nil
}

func removeEmptyVMComponentPruneParents(root, componentDir string) {
	_ = os.Remove(componentDir)
	architectureDir := filepath.Dir(componentDir)
	if architectureDir != root {
		_ = os.Remove(architectureDir)
	}
}

func (e *ttyExecer) ensureManagedVMImageAndPrune(ctx context.Context, cache vmImageCache, payload string, ui ProgressUI) (vmImageAsset, error) {
	asset, err := vmImageEnsureFunc(ctx, cache, payload, ui)
	if err != nil {
		return vmImageAsset{}, err
	}
	e.pruneVMImagesAfterManagedUpdate(ctx, cache)
	return asset, nil
}

func (e *ttyExecer) pruneVMImagesAfterManagedUpdate(ctx context.Context, cache vmImageCache) {
	if e == nil || e.s == nil {
		return
	}
	done := e.traceBlock("vm image auto prune")
	defer done()
	rows, err := e.s.planVMImagePrune(ctx, cache)
	if err != nil {
		e.tracef("vm image auto prune skipped: %v", err)
		return
	}
	if !vmImagePruneRowsHavePrunable(rows) {
		e.tracef("vm image auto prune removed=0 skipped=0")
		return
	}
	rows = e.s.applyVMImagePrune(ctx, rows)
	removed, skipped := countVMImagePruneResults(rows)
	e.tracef("vm image auto prune removed=%d skipped=%d", removed, skipped)
}

func countVMImagePruneResults(rows []vmImagePruneRow) (int, int) {
	removed := 0
	skipped := 0
	for _, row := range rows {
		switch row.State {
		case vmImagePruneStateRemoved:
			removed++
		case vmImagePruneStateSkipped:
			skipped++
		}
	}
	return removed, skipped
}

func (s *Server) destroyVMImageZFSBase(ctx context.Context, row vmImagePruneRow) error {
	base, ok := parseVMImageZFSBaseDatasetPath(row.Path)
	if !ok {
		return fmt.Errorf("invalid ZFS VM image base dataset %q", row.Path)
	}
	runner := s.vmImagePruneZFSRunner()
	if runner == nil {
		return fmt.Errorf("zfs is not available")
	}
	for _, target := range []string{base.Snapshot, base.Dataset, base.ParentDataset} {
		if err := runVMImageZFSDestroy(ctx, runner, target); err != nil {
			return err
		}
	}
	return nil
}

func runVMImageZFSDestroy(ctx context.Context, runner zfsCommandRunner, target string) error {
	_, stderr, err := runner(ctx, "destroy", target)
	if err == nil {
		return nil
	}
	if zfsDestroyDatasetMissing(stderr) {
		return nil
	}
	return fmt.Errorf("zfs destroy %s: %w: %s", target, err, strings.TrimSpace(stderr))
}

func renderVMImagePruneRows(w io.Writer, formatOut string, rows []vmImagePruneRow) error {
	switch strings.TrimSpace(formatOut) {
	case "json":
		return json.NewEncoder(w).Encode(rows)
	case "json-pretty":
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(rows)
	case "", "table":
		return renderVMImagePruneRowsTable(w, rows)
	default:
		return fmt.Errorf("unsupported vm images format %q", formatOut)
	}
}

func renderVMImagePruneRowsTable(w io.Writer, rows []vmImagePruneRow) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintln(w, "No VM images to prune.")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(tw, "KIND\tSTATE\tPAYLOAD\tVERSION\tPATH\tREASON"); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			row.Kind,
			row.State,
			dash(row.Payload),
			dash(row.Version),
			dash(row.Path),
			dash(row.Reason),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}
