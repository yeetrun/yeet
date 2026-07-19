// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/yeetrun/yeet/pkg/db"
)

var (
	nativeServiceLchown                = os.Lchown
	nativeServiceOwner                 = nativeServiceFileOwner
	serviceIdentityMountPointsFn       = readHostStorageMountPoints
	serviceIdentityDatasetBoundariesFn = discoverServiceIdentityDatasetBoundaries
	serviceIdentityDefaultZFSRunner    = runZFSCommand
)

type nativeServiceLayoutEntry struct {
	path       string
	uid        uint32
	gid        uint32
	mode       os.FileMode
	required   os.FileMode
	allowTight bool
}

type serviceIdentityDatasetBoundary struct {
	Dataset    string
	MountPoint string
}

type serviceIdentityInspectionRequest struct {
	Root           string
	Dataset        string
	Target         db.ServiceIdentity
	ZFSRunner      zfsCommandRunner
	MountPoints    []string
	NestedDatasets []serviceIdentityDatasetBoundary
	ListXattrs     func(string) ([]string, error)
	FileMode       func(string, os.FileMode) os.FileMode
	Metadata       func(string, os.FileInfo) (serviceIdentityInodeMetadata, error)
}

type serviceIdentityInspection struct {
	Records   []serviceIdentityInodeRecord
	Mutations []serviceIdentityMutation
}

type serviceIdentityMutation struct {
	Path       string
	UID        uint32
	GID        uint32
	Mode       os.FileMode
	ChangeMode bool
	Dev        uint64
	Ino        uint64
	Symlink    bool
}

type serviceIdentityMutationTarget struct {
	uid        uint32
	gid        uint32
	mode       os.FileMode
	changeMode bool
}

type serviceIdentityInodeMetadata struct {
	UID   uint32
	GID   uint32
	Dev   uint64
	Ino   uint64
	Nlink uint64
}

type serviceIdentityInodeKey struct {
	dev uint64
	ino uint64
}

type serviceIdentityLlistxattr func(string, []byte) (int, error)

type serviceIdentityHardLink struct {
	path  string
	links uint64
}

type serviceIdentityInspector struct {
	ctx        context.Context
	root       string
	target     db.ServiceIdentity
	mounts     []string
	datasets   map[string]string
	listXattrs func(string) ([]string, error)
	fileMode   func(string, os.FileMode) os.FileMode
	metadata   func(string, os.FileInfo) (serviceIdentityInodeMetadata, error)
	rootDev    uint64
	inspection serviceIdentityInspection
	seenLinks  map[serviceIdentityInodeKey]uint64
	hardLinks  map[serviceIdentityInodeKey]serviceIdentityHardLink
}

func inspectServiceIdentityChange(ctx context.Context, req serviceIdentityInspectionRequest) (serviceIdentityInspection, error) {
	rescanBoundaries := req.MountPoints == nil && (strings.TrimSpace(req.Dataset) == "" || req.NestedDatasets == nil)
	inspector, err := newServiceIdentityInspector(ctx, req)
	if err != nil {
		return serviceIdentityInspection{}, err
	}
	if err := filepath.WalkDir(inspector.root, inspector.inspectPath); err != nil {
		return serviceIdentityInspection{}, err
	}
	if rescanBoundaries {
		if err := validateServiceIdentityCurrentBoundaries(ctx, inspector.root, req.Dataset, req.ZFSRunner); err != nil {
			return serviceIdentityInspection{}, err
		}
	}
	if err := inspector.validateHardLinks(); err != nil {
		return serviceIdentityInspection{}, err
	}
	return inspector.inspection, nil
}

func validateServiceIdentityCurrentBoundaries(ctx context.Context, root, dataset string, runner zfsCommandRunner) error {
	mounts, err := serviceIdentityMountPointsFn()
	if err != nil {
		return fmt.Errorf("re-scan mount boundaries below %s: %w", root, err)
	}
	if nested := serviceIdentityNestedPaths(root, mounts); len(nested) != 0 {
		return fmt.Errorf("mount boundary at %s appeared during service identity operation", nested[0])
	}
	if strings.TrimSpace(dataset) == "" {
		return nil
	}
	boundaries, err := serviceIdentityDatasetBoundariesFn(ctx, runner, root)
	if err != nil {
		return fmt.Errorf("re-scan ZFS boundaries below %s: %w", root, err)
	}
	datasets := serviceIdentityDatasetMap(root, boundaries)
	if len(datasets) == 0 {
		return nil
	}
	paths := make([]string, 0, len(datasets))
	for path := range datasets {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return fmt.Errorf("nested ZFS dataset %q at %s appeared during service identity operation", datasets[paths[0]], paths[0])
}

func newServiceIdentityInspector(ctx context.Context, req serviceIdentityInspectionRequest) (*serviceIdentityInspector, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := validateServiceIdentityInspectionRoot(req.Root)
	if err != nil {
		return nil, err
	}
	req, err = prepareServiceIdentityInspectionRequest(ctx, root, req)
	if err != nil {
		return nil, err
	}
	rootDev, err := serviceIdentityInspectionRootDevice(root, req.Metadata)
	if err != nil {
		return nil, err
	}
	return &serviceIdentityInspector{
		ctx:        ctx,
		root:       root,
		target:     req.Target,
		mounts:     serviceIdentityNestedPaths(root, req.MountPoints),
		datasets:   serviceIdentityDatasetMap(root, req.NestedDatasets),
		listXattrs: req.ListXattrs,
		fileMode:   req.FileMode,
		metadata:   req.Metadata,
		rootDev:    rootDev,
		seenLinks:  make(map[serviceIdentityInodeKey]uint64),
		hardLinks:  make(map[serviceIdentityInodeKey]serviceIdentityHardLink),
	}, nil
}

func prepareServiceIdentityInspectionRequest(ctx context.Context, root string, req serviceIdentityInspectionRequest) (serviceIdentityInspectionRequest, error) {
	if req.ListXattrs == nil {
		req.ListXattrs = listServiceIdentityXattrs
	}
	if req.Metadata == nil {
		req.Metadata = func(_ string, info os.FileInfo) (serviceIdentityInodeMetadata, error) {
			return serviceIdentityMetadata(info)
		}
	}
	if req.MountPoints == nil {
		mounts, err := serviceIdentityMountPointsFn()
		if err != nil {
			return serviceIdentityInspectionRequest{}, fmt.Errorf("discover nested mounts below %s: %w", root, err)
		}
		req.MountPoints = mounts
	}
	if req.NestedDatasets == nil && strings.TrimSpace(req.Dataset) != "" {
		datasets, err := serviceIdentityDatasetBoundariesFn(ctx, req.ZFSRunner, root)
		if err != nil {
			return serviceIdentityInspectionRequest{}, fmt.Errorf("discover nested ZFS datasets below %s: %w", root, err)
		}
		req.NestedDatasets = datasets
	}
	return req, nil
}

func serviceIdentityDatasetMap(root string, boundaries []serviceIdentityDatasetBoundary) map[string]string {
	datasets := make(map[string]string, len(boundaries))
	for _, boundary := range boundaries {
		path := filepath.Clean(boundary.MountPoint)
		if path != root && pathWithinServiceIdentityRoot(root, path) {
			datasets[path] = boundary.Dataset
		}
	}
	return datasets
}

func serviceIdentityInspectionRootDevice(root string, metadata func(string, os.FileInfo) (serviceIdentityInodeMetadata, error)) (uint64, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return 0, fmt.Errorf("inspect service identity root %s: %w", root, err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return 0, fmt.Errorf("service identity root %s must be a non-symlink directory", root)
	}
	rootMeta, err := metadata(root, rootInfo)
	if err != nil {
		return 0, fmt.Errorf("inspect service identity root metadata %s: %w", root, err)
	}
	return rootMeta.Dev, nil
}

func (inspector *serviceIdentityInspector) inspectPath(path string, _ os.DirEntry, walkErr error) error {
	if walkErr != nil {
		return walkErr
	}
	if err := inspector.ctx.Err(); err != nil {
		return err
	}
	path = filepath.Clean(path)
	if err := inspector.validateBoundary(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect service identity path %s: %w", path, err)
	}
	mode := info.Mode()
	if inspector.fileMode != nil {
		mode = inspector.fileMode(path, mode)
	}
	meta, err := inspector.metadata(path, info)
	if err != nil {
		return fmt.Errorf("inspect service identity metadata %s: %w", path, err)
	}
	if err := inspector.validatePath(path, mode, meta); err != nil {
		return err
	}
	inspector.recordHardLink(path, mode, meta)
	return inspector.recordMutation(path, mode, meta)
}

func (inspector *serviceIdentityInspector) validateBoundary(path string) error {
	if path == inspector.root {
		return nil
	}
	if dataset, ok := inspector.datasets[path]; ok {
		return fmt.Errorf("nested ZFS dataset %q at %s blocks service identity migration", dataset, path)
	}
	if slices.Contains(inspector.mounts, path) {
		return fmt.Errorf("mount boundary at %s blocks service identity migration", path)
	}
	return nil
}

func (inspector *serviceIdentityInspector) validatePath(path string, mode os.FileMode, meta serviceIdentityInodeMetadata) error {
	if meta.Dev != inspector.rootDev {
		return fmt.Errorf("device boundary at %s blocks service identity migration", path)
	}
	if mode&(os.ModeSetuid|os.ModeSetgid) != 0 {
		return fmt.Errorf("setuid or setgid path %s blocks service identity migration", path)
	}
	if !mode.IsDir() && meta.Nlink != 1 {
		return fmt.Errorf("hard-linked path %s has %d links; service identity migration requires one", path, meta.Nlink)
	}
	xattrs, err := inspector.listXattrs(path)
	if err != nil {
		return fmt.Errorf("inspect service identity xattrs %s: %w", path, err)
	}
	if name := blockedServiceIdentityXattr(xattrs); name != "" {
		return fmt.Errorf("extended attribute %s at %s blocks service identity migration", name, path)
	}
	if !mode.IsRegular() && !mode.IsDir() && mode&os.ModeSymlink == 0 {
		return fmt.Errorf("special filesystem object %s with mode %s blocks service identity migration", path, mode)
	}
	return nil
}

func blockedServiceIdentityXattr(names []string) string {
	for _, name := range names {
		switch name {
		case "system.posix_acl_access", "system.posix_acl_default", "security.capability":
			return name
		}
	}
	return ""
}

func (inspector *serviceIdentityInspector) recordHardLink(path string, mode os.FileMode, meta serviceIdentityInodeMetadata) {
	if !mode.IsRegular() || meta.Nlink <= 1 {
		return
	}
	key := serviceIdentityInodeKey{dev: meta.Dev, ino: meta.Ino}
	inspector.seenLinks[key]++
	inspector.hardLinks[key] = serviceIdentityHardLink{path: path, links: meta.Nlink}
}

func (inspector *serviceIdentityInspector) recordMutation(path string, mode os.FileMode, meta serviceIdentityInodeMetadata) error {
	mutation, ok := nativeServiceIdentityMutation(inspector.root, path, mode, meta, inspector.target)
	if !ok {
		return nil
	}
	rel, err := filepath.Rel(inspector.root, path)
	if err != nil {
		return fmt.Errorf("make service identity path relative %s: %w", path, err)
	}
	inspector.inspection.Records = append(inspector.inspection.Records, serviceIdentityInodeRecord{
		Path:  rel,
		UID:   meta.UID,
		GID:   meta.GID,
		Mode:  mode,
		Dev:   meta.Dev,
		Ino:   meta.Ino,
		Nlink: meta.Nlink,
	})
	inspector.inspection.Mutations = append(inspector.inspection.Mutations, mutation)
	return nil
}

func (inspector *serviceIdentityInspector) validateHardLinks() error {
	keys := make([]serviceIdentityInodeKey, 0, len(inspector.hardLinks))
	for key := range inspector.hardLinks {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b serviceIdentityInodeKey) int {
		return strings.Compare(inspector.hardLinks[a].path, inspector.hardLinks[b].path)
	})
	for _, key := range keys {
		hardLink := inspector.hardLinks[key]
		if inspector.seenLinks[key] != hardLink.links {
			return fmt.Errorf("external hard link for %s has %d links but only %d are inside the service root", hardLink.path, hardLink.links, inspector.seenLinks[key])
		}
	}
	return nil
}

func discoverServiceIdentityDatasetBoundaries(ctx context.Context, runner zfsCommandRunner, root string) ([]serviceIdentityDatasetBoundary, error) {
	if runner == nil {
		runner = serviceIdentityDefaultZFSRunner
	}
	boundaries, err := hostStorageZFSBoundaries(ctx, runner, root)
	if err != nil {
		return nil, err
	}
	out := make([]serviceIdentityDatasetBoundary, 0, len(boundaries))
	for _, boundary := range boundaries {
		if filepath.Clean(boundary.mountPoint) == filepath.Clean(root) {
			continue
		}
		out = append(out, serviceIdentityDatasetBoundary{Dataset: boundary.dataset, MountPoint: boundary.mountPoint})
	}
	return out, nil
}

func validateServiceIdentityInspectionRoot(root string) (string, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "." || root == string(filepath.Separator) || !filepath.IsAbs(root) {
		return "", fmt.Errorf("service identity root must be a non-root absolute path, got %q", root)
	}
	return root, nil
}

func serviceIdentityNestedPaths(root string, paths []string) []string {
	var nested []string
	for _, path := range paths {
		path = filepath.Clean(path)
		if path != root && pathWithinServiceIdentityRoot(root, path) {
			nested = append(nested, path)
		}
	}
	slices.Sort(nested)
	return slices.Compact(nested)
}

func pathWithinServiceIdentityRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func serviceIdentityMetadata(info os.FileInfo) (serviceIdentityInodeMetadata, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return serviceIdentityInodeMetadata{}, fmt.Errorf("unsupported file stat type %T", info.Sys())
	}
	return serviceIdentityInodeMetadata{
		UID: stat.Uid, GID: stat.Gid, Dev: uint64(stat.Dev), Ino: uint64(stat.Ino), Nlink: uint64(stat.Nlink),
	}, nil
}

func nativeServiceIdentityMutation(root, path string, mode os.FileMode, meta serviceIdentityInodeMetadata, target db.ServiceIdentity) (serviceIdentityMutation, bool) {
	desired, managed := nativeServiceIdentityMutationTarget(root, path, mode, target)
	if !managed {
		return serviceIdentityMutation{}, false
	}
	if desired.uid == meta.UID && desired.gid == meta.GID && (!desired.changeMode || desired.mode.Perm() == mode.Perm()) {
		return serviceIdentityMutation{}, false
	}
	return serviceIdentityMutation{
		Path: path, UID: desired.uid, GID: desired.gid, Mode: desired.mode, ChangeMode: desired.changeMode,
		Dev: meta.Dev, Ino: meta.Ino, Symlink: mode&os.ModeSymlink != 0,
	}, true
}

func nativeServiceIdentityMutationTarget(root, path string, mode os.FileMode, target db.ServiceIdentity) (serviceIdentityMutationTarget, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return serviceIdentityMutationTarget{}, false
	}
	switch {
	case rel == ".", rel == "bin", rel == "env":
		return serviceIdentityMutationTarget{uid: 0, gid: target.GID, mode: 0o750, changeMode: true}, true
	case rel == "data", rel == "run":
		return serviceIdentityMutationTarget{uid: target.UID, gid: target.GID, mode: 0o750, changeMode: true}, true
	case rel == "data" || strings.HasPrefix(rel, "data"+string(filepath.Separator)), rel == "run" || strings.HasPrefix(rel, "run"+string(filepath.Separator)):
		return serviceIdentityMutationTarget{uid: target.UID, gid: target.GID, mode: mode}, true
	case strings.HasPrefix(rel, "bin"+string(filepath.Separator)) || strings.HasPrefix(rel, "env"+string(filepath.Separator)):
		return nativeServiceArtifactMutationTarget(root, path, mode, target.GID)
	default:
		return serviceIdentityMutationTarget{}, false
	}
}

func nativeServiceArtifactMutationTarget(root, path string, mode os.FileMode, gid uint32) (serviceIdentityMutationTarget, bool) {
	dir, name := filepath.Base(filepath.Dir(path)), filepath.Base(path)
	allowed, required, managed := managedNativeArtifactMode(dir, filepath.Base(root), name)
	if !managed {
		return serviceIdentityMutationTarget{}, false
	}
	desiredMode := mode
	changeMode := false
	if mode&os.ModeSymlink == 0 {
		desiredMode = tightenedUsableMode(mode.Perm(), allowed, required)
		changeMode = desiredMode.Perm() != mode.Perm()
	}
	return serviceIdentityMutationTarget{uid: 0, gid: gid, mode: desiredMode, changeMode: changeMode}, true
}

func listServiceIdentityXattrs(path string) ([]string, error) {
	return listServiceIdentityXattrsWith(path, unix.Llistxattr)
}

func listServiceIdentityXattrsWith(path string, list serviceIdentityLlistxattr) ([]string, error) {
	size, err := list(path, nil)
	if err != nil {
		if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.ENODATA) {
			return nil, nil
		}
		return nil, err
	}
	if size == 0 {
		return nil, nil
	}
	buf := make([]byte, size)
	n, err := list(path, buf)
	if err != nil {
		return nil, err
	}
	return parseServiceIdentityXattrBuffer(buf, n), nil
}

func parseServiceIdentityXattrBuffer(buf []byte, n int) []string {
	var names []string
	for _, raw := range strings.Split(string(buf[:n]), "\x00") {
		if raw != "" {
			names = append(names, raw)
		}
	}
	slices.Sort(names)
	return names
}

func applyNativeServiceLayout(root string, identity db.ServiceIdentity) error {
	entries, err := nativeServiceLayout(root, identity)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := applyNativeServiceLayoutEntry(entry); err != nil {
			return err
		}
	}
	return nil
}

func applyNativeExecutableLayout(path string, gid uint32) error {
	return applyNativeServiceLayoutEntry(nativeServiceLayoutEntry{
		path: path, uid: 0, gid: gid, mode: 0o750, required: 0o050, allowTight: true,
	})
}

func applyNativeServiceLayoutEntry(entry nativeServiceLayoutEntry) error {
	info, err := os.Lstat(entry.path)
	if err != nil {
		return fmt.Errorf("inspect managed service path %s: %w", entry.path, err)
	}
	if err := nativeServiceLchown(entry.path, int(entry.uid), int(entry.gid)); err != nil {
		return fmt.Errorf("set managed service owner %s to %d:%d: %w", entry.path, entry.uid, entry.gid, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	mode := entry.mode
	if entry.allowTight {
		mode = tightenedUsableMode(info.Mode().Perm(), entry.mode, entry.required)
	}
	if err := os.Chmod(entry.path, mode); err != nil {
		return fmt.Errorf("set managed service mode %s to %04o: %w", entry.path, mode.Perm(), err)
	}
	return nil
}

func validateNativeServiceLayout(root string, identity db.ServiceIdentity) error {
	entries, err := nativeServiceLayout(root, identity)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := validateNativeServiceLayoutEntry(entry); err != nil {
			return err
		}
	}
	return nil
}

func validateNativeServiceLayoutEntry(entry nativeServiceLayoutEntry) error {
	info, err := os.Lstat(entry.path)
	if err != nil {
		return fmt.Errorf("inspect managed service path %s: %w", entry.path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("managed service path %s is a symlink", entry.path)
	}
	uid, gid, err := nativeServiceOwner(info)
	if err != nil {
		return fmt.Errorf("inspect managed service owner %s: %w", entry.path, err)
	}
	if uid != entry.uid || gid != entry.gid {
		return fmt.Errorf("managed service owner %s is %d:%d, want %d:%d", entry.path, uid, gid, entry.uid, entry.gid)
	}
	mode := info.Mode().Perm()
	if entry.allowTight {
		if mode&^entry.mode != 0 || mode&entry.required != entry.required {
			return fmt.Errorf("managed service mode %s is %04o, want no broader than %04o with %04o required", entry.path, mode, entry.mode, entry.required)
		}
		return nil
	}
	if mode != entry.mode {
		return fmt.Errorf("managed service mode %s is %04o, want %04o", entry.path, mode, entry.mode)
	}
	return nil
}

func nativeServiceLayout(root string, identity db.ServiceIdentity) ([]nativeServiceLayoutEntry, error) {
	root = filepath.Clean(root)
	if root == "." || root == string(filepath.Separator) {
		return nil, fmt.Errorf("invalid native service root %q", root)
	}
	service := filepath.Base(root)
	managedDirs := []string{
		root,
		filepath.Join(root, "bin"),
		filepath.Join(root, "env"),
		filepath.Join(root, "data"),
		filepath.Join(root, "run"),
	}
	for _, dir := range managedDirs {
		info, err := os.Lstat(dir)
		if err != nil {
			return nil, fmt.Errorf("inspect managed service directory %s: %w", dir, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("managed service directory %s is a symlink", dir)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("managed service directory %s is not a directory", dir)
		}
	}
	entries := []nativeServiceLayoutEntry{
		{path: root, uid: 0, gid: identity.GID, mode: 0o750},
		{path: filepath.Join(root, "bin"), uid: 0, gid: identity.GID, mode: 0o750},
		{path: filepath.Join(root, "env"), uid: 0, gid: identity.GID, mode: 0o750},
		{path: filepath.Join(root, "data"), uid: identity.UID, gid: identity.GID, mode: 0o750},
		{path: filepath.Join(root, "run"), uid: identity.UID, gid: identity.GID, mode: 0o750},
	}
	managed, err := managedNativeServiceArtifacts(root, service, identity.GID)
	if err != nil {
		return nil, err
	}
	return append(entries, managed...), nil
}

func managedNativeServiceArtifacts(root, service string, gid uint32) ([]nativeServiceLayoutEntry, error) {
	var entries []nativeServiceLayoutEntry
	for _, dir := range []string{"bin", "env"} {
		base := filepath.Join(root, dir)
		children, err := os.ReadDir(base)
		if err != nil {
			return nil, fmt.Errorf("read managed service directory %s: %w", base, err)
		}
		for _, child := range children {
			mode, required, ok := managedNativeArtifactMode(dir, service, child.Name())
			if !ok {
				continue
			}
			entries = append(entries, nativeServiceLayoutEntry{
				path: filepath.Join(base, child.Name()), uid: 0, gid: gid,
				mode: mode, required: required, allowTight: true,
			})
		}
	}
	return entries, nil
}

func managedNativeArtifactMode(dir, service, name string) (os.FileMode, os.FileMode, bool) {
	if dir == "bin" {
		if name == "tailscaled" || versionedNativeArtifact(name, service) {
			return 0o750, 0o050, true
		}
		return 0, 0, false
	}
	if name == "env" || strings.HasPrefix(name, "env-") || name == "netns.env" || name == "tailscaled.env" || name == "tailscaled.json" {
		return 0o640, 0o040, true
	}
	return 0, 0, false
}

func versionedNativeArtifact(name, service string) bool {
	suffix, ok := strings.CutPrefix(name, service+"-")
	return ok && suffix != "" && strings.Trim(suffix, "0123456789.") == ""
}

func tightenedUsableMode(current, allowed, required os.FileMode) os.FileMode {
	mode := current.Perm() & allowed.Perm()
	if mode&required.Perm() != required.Perm() {
		mode |= required.Perm()
	}
	return mode
}

func nativeServiceFileOwner(info os.FileInfo) (uint32, uint32, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, fmt.Errorf("unsupported file stat type %T", info.Sys())
	}
	return stat.Uid, stat.Gid, nil
}
