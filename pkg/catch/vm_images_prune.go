// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/yeetrun/yeet/pkg/db"
)

const (
	vmImagePruneKindCache   = "cache"
	vmImagePruneKindZFSBase = "zfs-base"

	vmImagePruneStateCurrent  = "current"
	vmImagePruneStateInUse    = "in-use"
	vmImagePruneStatePrunable = "prunable"
	vmImagePruneStateRemoved  = "removed"
	vmImagePruneStateSkipped  = "skipped"
)

type vmImagePruneRow struct {
	Kind    string `json:"kind"`
	State   string `json:"state"`
	Payload string `json:"payload,omitempty"`
	Version string `json:"version,omitempty"`
	Path    string `json:"path,omitempty"`
	Reason  string `json:"reason,omitempty"`
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
	sortVMImagePruneRows(rows)
	return rows, nil
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
