// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	localVMImageKernelPolicyManaged = "yeet-managed"
	localVMImageKernelPolicyLocal   = "local"
)

var localVMImageNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*(/[a-z0-9][a-z0-9._-]*)*$`)

type localVMImageImportRequest struct {
	Name             string
	Reader           io.Reader
	AllowLocalKernel bool
}

type localVMImageImporter struct {
	CacheRoot          string
	Catalog            *vmImageCatalog
	EnsureManagedAsset func(context.Context) (vmImageAsset, error)
	Now                func() time.Time
}

type localVMImageRef struct {
	Name         string `json:"name"`
	Payload      string `json:"payload"`
	Version      string `json:"version"`
	ContentID    string `json:"contentID"`
	Root         string `json:"root"`
	RootFS       string `json:"rootfs"`
	Kernel       string `json:"kernel"`
	Firecracker  string `json:"firecracker"`
	Jailer       string `json:"jailer,omitempty"`
	KernelPolicy string `json:"kernelPolicy"`
	CreatedAt    string `json:"createdAt"`
}

type localVMImageManifestCapabilities struct {
	ImageProfile        string
	Distro              string
	DistroVersion       string
	DefaultUser         string
	KernelPolicy        string
	GuestInit           string
	GuestSystemInit     string
	MetadataDriver      string
	SnapSupportSet      bool
	SnapSupport         bool
	KernelVersion       string
	UbuntuKernelVersion string
}

func validateLocalVMImageName(name string) error {
	if name == "" {
		return fmt.Errorf("local VM image name is required")
	}
	if name != strings.TrimSpace(name) || !localVMImageNamePattern.MatchString(name) {
		return fmt.Errorf("local VM image name %q must use lowercase path segments with letters, numbers, dots, underscores, or dashes", name)
	}
	return nil
}

func validateLocalVMImageNameForImport(name string, catalog *vmImageCatalog) error {
	if catalog == nil {
		return validateLocalVMImageName(name)
	}
	return validateLocalVMImageNameForCatalog(name, *catalog)
}

func (i localVMImageImporter) Import(ctx context.Context, req localVMImageImportRequest) (localVMImageRef, error) {
	cacheRoot, err := i.validateImportRequest(req)
	if err != nil {
		return localVMImageRef{}, err
	}
	managed, err := i.EnsureManagedAsset(ctx)
	if err != nil {
		return localVMImageRef{}, fmt.Errorf("ensure managed VM image asset: %w", err)
	}
	stagingDir, err := os.MkdirTemp("", "yeet-local-vm-image-*")
	if err != nil {
		return localVMImageRef{}, fmt.Errorf("create local VM image staging dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(stagingDir) }()
	if err := extractLocalVMImageBundle(req.Reader, stagingDir); err != nil {
		return localVMImageRef{}, err
	}
	return i.importExtracted(cacheRoot, req, managed, stagingDir)
}

func (i localVMImageImporter) validateImportRequest(req localVMImageImportRequest) (string, error) {
	if err := validateLocalVMImageNameForImport(req.Name, i.Catalog); err != nil {
		return "", err
	}
	if req.Reader == nil {
		return "", fmt.Errorf("local VM image bundle reader is required")
	}
	cacheRoot := strings.TrimSpace(i.CacheRoot)
	if cacheRoot == "" {
		return "", fmt.Errorf("local VM image cache root is required")
	}
	if i.EnsureManagedAsset == nil {
		return "", fmt.Errorf("managed VM image asset resolver is required")
	}
	return cacheRoot, nil
}

func (i localVMImageImporter) importExtracted(cacheRoot string, req localVMImageImportRequest, managed vmImageAsset, stagingDir string) (localVMImageRef, error) {
	rootFSName, rootFSPath, rootFSSize, err := localVMImageRootFS(stagingDir)
	if err != nil {
		return localVMImageRef{}, err
	}
	sourceManifest, hasSourceManifest, err := localVMImageSourceManifest(stagingDir, rootFSName)
	if err != nil {
		return localVMImageRef{}, err
	}
	kernelSource, kernelPolicy, err := localVMImageKernelSource(stagingDir, managed.Paths.KernelPath, req.AllowLocalKernel)
	if err != nil {
		return localVMImageRef{}, err
	}
	jailerSource, err := managed.RequireJailer()
	if err != nil {
		return localVMImageRef{}, err
	}
	capabilities := localVMImageCapabilities(sourceManifest, hasSourceManifest, managed.Manifest, kernelPolicy)
	contentID, err := localVMImageContentID(req.Name, rootFSPath, kernelSource, managed.Paths.FirecrackerPath, jailerSource, capabilities)
	if err != nil {
		return localVMImageRef{}, err
	}
	version := fmt.Sprintf("local-%s-%s", strings.ReplaceAll(req.Name, "/", "-"), contentID[:12])
	blobDir := filepath.Join(cacheRoot, "local", "blobs", contentID)
	if err := installLocalVMImageBlob(blobDir, rootFSName, rootFSPath, kernelSource, managed.Paths.FirecrackerPath, jailerSource, version, req.Name, rootFSSize, capabilities); err != nil {
		return localVMImageRef{}, err
	}

	now := time.Now
	if i.Now != nil {
		now = i.Now
	}
	ref := localVMImageRef{
		Name:         req.Name,
		Payload:      vmImagePayloadPrefix + req.Name,
		Version:      version,
		ContentID:    contentID,
		Root:         blobDir,
		RootFS:       rootFSName,
		Kernel:       "vmlinux",
		Firecracker:  "firecracker",
		Jailer:       "jailer",
		KernelPolicy: kernelPolicy,
		CreatedAt:    now().UTC().Format(time.RFC3339Nano),
	}
	if err := writeLocalVMImageRef(localVMImageRefPath(cacheRoot, req.Name), ref); err != nil {
		return localVMImageRef{}, err
	}
	return ref, nil
}

func localVMImageKernelSource(stagingDir, managedKernelPath string, allowLocalKernel bool) (string, string, error) {
	localKernelPath := filepath.Join(stagingDir, "vmlinux")
	if _, err := os.Lstat(localKernelPath); err != nil {
		if os.IsNotExist(err) {
			return managedKernelPath, localVMImageKernelPolicyManaged, nil
		}
		return "", "", fmt.Errorf("inspect local VM image kernel: %w", err)
	}
	if !allowLocalKernel {
		return "", "", fmt.Errorf("local VM image bundle includes vmlinux; pass --allow-local-kernel to import it")
	}
	kernelSource, err := localVMImageSafeArtifactPath(stagingDir, "vmlinux")
	if err != nil {
		return "", "", err
	}
	return kernelSource, localVMImageKernelPolicyLocal, nil
}

func localVMImageSourceManifest(stagingDir, rootFSName string) (vmImageManifest, bool, error) {
	raw, err := os.ReadFile(filepath.Join(stagingDir, "manifest.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return vmImageManifest{}, false, nil
		}
		return vmImageManifest{}, false, fmt.Errorf("read local VM image source manifest: %w", err)
	}
	var manifest vmImageManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return vmImageManifest{}, false, fmt.Errorf("decode local VM image source manifest: %w", err)
	}
	if manifest.RootFS != "" && manifest.RootFS != rootFSName {
		return vmImageManifest{}, false, fmt.Errorf("local VM image source manifest rootfs %q does not match imported rootfs %q", manifest.RootFS, rootFSName)
	}
	return manifest, true, nil
}

func localVMImageCapabilities(source vmImageManifest, hasSource bool, managed vmImageManifest, kernelPolicy string) localVMImageManifestCapabilities {
	caps := localVMImageManifestCapabilities{
		ImageProfile: "local",
		KernelPolicy: kernelPolicy,
	}
	if hasSource {
		caps.applySource(source)
	} else {
		caps.applyManagedRootFSDefaults(managed, kernelPolicy)
	}
	caps.applyManagedKernel(managed, kernelPolicy)
	return caps
}

func (c *localVMImageManifestCapabilities) applySource(source vmImageManifest) {
	c.applySourceIdentity(source)
	c.applySourceBoot(source)
	c.applySourceKernel(source)
	if source.SnapSupport != nil {
		c.SnapSupportSet = true
		c.SnapSupport = *source.SnapSupport
	}
}

func (c *localVMImageManifestCapabilities) applySourceIdentity(source vmImageManifest) {
	if strings.TrimSpace(source.ImageProfile) != "" {
		c.ImageProfile = source.ImageProfile
	}
	if strings.TrimSpace(source.Distro) != "" {
		c.Distro = source.Distro
	}
	if strings.TrimSpace(source.DistroVersion) != "" {
		c.DistroVersion = source.DistroVersion
	}
	if strings.TrimSpace(source.DefaultUser) != "" {
		c.DefaultUser = source.DefaultUser
	}
	if strings.TrimSpace(source.MetadataDriver) != "" {
		c.MetadataDriver = source.MetadataDriver
	}
}

func (c *localVMImageManifestCapabilities) applySourceBoot(source vmImageManifest) {
	if strings.TrimSpace(source.GuestInit) == vmGuestInitPath {
		c.GuestInit = vmGuestInitPath
	}
	if strings.TrimSpace(source.GuestSystemInit) != "" {
		c.GuestSystemInit = source.GuestSystemInit
	}
}

func (c *localVMImageManifestCapabilities) applySourceKernel(source vmImageManifest) {
	if strings.TrimSpace(source.KernelVersion) != "" {
		c.KernelVersion = source.KernelVersion
	}
	if strings.TrimSpace(source.UbuntuKernelVersion) != "" {
		c.UbuntuKernelVersion = source.UbuntuKernelVersion
	}
}

func (c *localVMImageManifestCapabilities) applyManagedRootFSDefaults(managed vmImageManifest, kernelPolicy string) {
	if kernelPolicy != localVMImageKernelPolicyManaged {
		return
	}
	if strings.TrimSpace(managed.ImageProfile) != "" {
		c.ImageProfile = managed.ImageProfile
	}
	if strings.TrimSpace(managed.GuestInit) == vmGuestInitPath {
		c.GuestInit = vmGuestInitPath
	}
}

func (c *localVMImageManifestCapabilities) applyManagedKernel(managed vmImageManifest, kernelPolicy string) {
	if kernelPolicy != localVMImageKernelPolicyManaged {
		return
	}
	if strings.TrimSpace(managed.KernelVersion) != "" {
		c.KernelVersion = managed.KernelVersion
	}
}

func resolveLocalVMImageAsset(ctx context.Context, cacheRoot, name string) (vmImageAsset, error) {
	ref, manifest, paths, err := resolveLocalVMImageMetadata(cacheRoot, name)
	if err != nil {
		return vmImageAsset{}, err
	}
	if err := verifyResolvedLocalVMImage(ref, manifest, paths); err != nil {
		return vmImageAsset{}, err
	}
	if err := os.Chmod(paths.FirecrackerPath, 0o755); err != nil {
		return vmImageAsset{}, fmt.Errorf("chmod local VM image firecracker: %w", err)
	}
	if paths.JailerPath != "" {
		if err := os.Chmod(paths.JailerPath, 0o755); err != nil {
			return vmImageAsset{}, fmt.Errorf("chmod local VM image jailer: %w", err)
		}
	}
	preparedRootFS, err := prepareVMRootFSFunc(ctx, paths.RootFSPath)
	if err != nil {
		return vmImageAsset{}, err
	}
	return vmImageAsset{Paths: paths, PreparedRootFSPath: preparedRootFS, Manifest: manifest}, nil
}

func resolveLocalVMImageMetadata(cacheRoot, name string) (localVMImageRef, vmImageManifest, vmImagePaths, error) {
	if err := validateLocalVMImageName(name); err != nil {
		return localVMImageRef{}, vmImageManifest{}, vmImagePaths{}, err
	}
	ref, err := readLocalVMImageRef(localVMImageRefPath(cacheRoot, name))
	if err != nil {
		return localVMImageRef{}, vmImageManifest{}, vmImagePaths{}, err
	}
	if err := validateLocalVMImageName(ref.Name); err != nil {
		return localVMImageRef{}, vmImageManifest{}, vmImagePaths{}, err
	}
	if err := validateResolvedLocalVMImageRefIdentity(name, ref); err != nil {
		return localVMImageRef{}, vmImageManifest{}, vmImagePaths{}, err
	}
	if err := validateLocalVMImageRefRoot(cacheRoot, ref); err != nil {
		return localVMImageRef{}, vmImageManifest{}, vmImagePaths{}, err
	}
	manifest, err := readResolvedLocalVMImageManifest(ref)
	if err != nil {
		return localVMImageRef{}, vmImageManifest{}, vmImagePaths{}, err
	}
	paths := vmImagePaths{
		Manifest:        filepath.Join(ref.Root, "manifest.json"),
		Dir:             ref.Root,
		KernelPath:      filepath.Join(ref.Root, ref.Kernel),
		RootFSPath:      filepath.Join(ref.Root, ref.RootFS),
		FirecrackerPath: filepath.Join(ref.Root, ref.Firecracker),
	}
	if strings.TrimSpace(ref.Jailer) != "" {
		paths.JailerPath = filepath.Join(ref.Root, ref.Jailer)
	}
	return ref, manifest, paths, nil
}

func validateResolvedLocalVMImageRefIdentity(name string, ref localVMImageRef) error {
	if ref.Name != name {
		return fmt.Errorf("local VM image ref name %q does not match path name %q", ref.Name, name)
	}
	wantPayload := "vm://" + name
	if ref.Payload != wantPayload {
		return fmt.Errorf("local VM image ref payload %q does not match %q", ref.Payload, wantPayload)
	}
	return nil
}

func readResolvedLocalVMImageManifest(ref localVMImageRef) (vmImageManifest, error) {
	manifest, err := readLocalVMImageBlobManifest(ref.Root)
	if err != nil {
		return vmImageManifest{}, err
	}
	if manifest.Version != ref.Version {
		return vmImageManifest{}, fmt.Errorf("local VM image manifest version %q does not match ref version %q", manifest.Version, ref.Version)
	}
	if ref.RootFS != manifest.RootFS || ref.Kernel != manifest.Kernel || ref.Firecracker != manifest.Firecracker || ref.Jailer != manifest.Jailer {
		return vmImageManifest{}, fmt.Errorf("local VM image ref artifacts do not match manifest")
	}
	return manifest, nil
}

func verifyResolvedLocalVMImage(ref localVMImageRef, manifest vmImageManifest, paths vmImagePaths) error {
	if err := localVMImageVerifyManifestArtifacts(ref.Root, manifest); err != nil {
		return err
	}
	capabilities := localVMImageCapabilitiesFromManifest(manifest)
	contentID, err := localVMImageContentID(ref.Name, paths.RootFSPath, paths.KernelPath, paths.FirecrackerPath, paths.JailerPath, capabilities)
	if err != nil {
		return err
	}
	if contentID == ref.ContentID {
		return nil
	}
	legacyContentID, err := legacyLocalVMImageContentID(ref.Name, paths.RootFSPath, paths.KernelPath, paths.FirecrackerPath, capabilities)
	if err != nil {
		return err
	}
	if legacyContentID == ref.ContentID {
		return nil
	}
	return fmt.Errorf("local VM image content ID mismatch: got %s, want %s", contentID, ref.ContentID)
}

func validateLocalVMImageRefRoot(cacheRoot string, ref localVMImageRef) error {
	want := filepath.Join(cacheRoot, "local", "blobs", ref.ContentID)
	if filepath.Clean(ref.Root) != filepath.Clean(want) {
		return fmt.Errorf("local VM image ref root %q does not match content root %q", ref.Root, want)
	}
	return nil
}

func removeLocalVMImage(cacheRoot, name string) error {
	if err := validateLocalVMImageName(name); err != nil {
		return err
	}
	refPath := localVMImageRefPath(cacheRoot, name)
	ref, err := readLocalVMImageRef(refPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := removeLocalVMImageRefFile(cacheRoot, refPath); err != nil {
		return err
	}
	return removeLocalVMImageBlobIfUnreferenced(cacheRoot, ref.ContentID)
}

func removeLocalVMImageRefFile(cacheRoot, refPath string) error {
	if err := os.Remove(refPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove local VM image ref: %w", err)
	}
	removeEmptyLocalVMImageRefDirs(cacheRoot, filepath.Dir(refPath))
	return nil
}

func removeLocalVMImageBlobIfUnreferenced(cacheRoot, contentID string) error {
	if contentID == "" {
		return nil
	}
	refs, err := listLocalVMImages(cacheRoot)
	if err != nil {
		return err
	}
	for _, other := range refs {
		if other.ContentID == contentID {
			return nil
		}
	}
	if err := os.RemoveAll(filepath.Join(cacheRoot, "local", "blobs", contentID)); err != nil {
		return fmt.Errorf("remove local VM image blob: %w", err)
	}
	return nil
}

func listLocalVMImages(cacheRoot string) ([]localVMImageRef, error) {
	refsRoot := filepath.Join(cacheRoot, "local", "refs")
	var refs []localVMImageRef
	if err := filepath.WalkDir(refsRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "ref.json" {
			return nil
		}
		ref, err := readLocalVMImageRef(path)
		if err != nil {
			return err
		}
		refs = append(refs, ref)
		return nil
	}); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list local VM image refs: %w", err)
	}
	sort.Slice(refs, func(a, b int) bool {
		return refs[a].Name < refs[b].Name
	})
	return refs, nil
}

func localVMImageRefPath(cacheRoot, name string) string {
	parts := []string{cacheRoot, "local", "refs"}
	parts = append(parts, strings.Split(name, "/")...)
	parts = append(parts, "ref.json")
	return filepath.Join(parts...)
}

func localVMImageRefExists(cacheRoot, name string) (bool, error) {
	if strings.TrimSpace(cacheRoot) == "" {
		return false, nil
	}
	if _, err := os.Stat(localVMImageRefPath(cacheRoot, name)); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("inspect local VM image ref: %w", err)
	}
	return true, nil
}

func extractLocalVMImageBundle(r io.Reader, dest string) error {
	dest = filepath.Clean(dest)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fmt.Errorf("create local VM image bundle dir: %w", err)
	}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read local VM image bundle: %w", err)
		}
		target, ok, err := localVMImageTarTarget(dest, hdr.Name)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if err := extractLocalVMImageTarEntry(tr, dest, target, hdr); err != nil {
			return err
		}
	}
}

func localVMImageTarTarget(dest, name string) (string, bool, error) {
	if localVMImageTarNameHasParentTraversal(name) {
		return "", false, fmt.Errorf("invalid local VM image bundle entry %q", name)
	}
	cleanName := path.Clean(name)
	if cleanName == "." || cleanName == "" {
		return "", false, nil
	}
	if path.IsAbs(cleanName) || cleanName == ".." || strings.HasPrefix(cleanName, "../") {
		return "", false, fmt.Errorf("invalid local VM image bundle entry %q", name)
	}
	target := filepath.Join(dest, filepath.FromSlash(cleanName))
	if !localVMImageIsSubpath(dest, target) {
		return "", false, fmt.Errorf("invalid local VM image bundle entry %q", name)
	}
	return target, true, nil
}

func localVMImageTarNameHasParentTraversal(name string) bool {
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

func extractLocalVMImageTarEntry(r io.Reader, root, target string, hdr *tar.Header) error {
	switch hdr.Typeflag {
	case tar.TypeDir:
		return extractLocalVMImageTarDir(root, target, hdr)
	case tar.TypeReg, 0:
		return extractLocalVMImageTarFile(r, root, target, hdr)
	case tar.TypeSymlink:
		return extractLocalVMImageTarSymlink(root, target, hdr)
	default:
		return fmt.Errorf("unsupported local VM image bundle entry %q", hdr.Name)
	}
}

func extractLocalVMImageTarDir(root, target string, hdr *tar.Header) error {
	if err := rejectLocalVMImageSymlinkInPath(root, target); err != nil {
		return err
	}
	mode := os.FileMode(hdr.Mode).Perm()
	if mode == 0 {
		mode = 0o755
	}
	if err := os.MkdirAll(target, mode); err != nil {
		return fmt.Errorf("extract local VM image bundle dir: %w", err)
	}
	return nil
}

func extractLocalVMImageTarFile(r io.Reader, root, target string, hdr *tar.Header) error {
	if err := rejectLocalVMImageSymlinkInPath(root, filepath.Dir(target)); err != nil {
		return err
	}
	if err := rejectExistingLocalVMImageSymlink(target); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create local VM image bundle file dir: %w", err)
	}
	mode := os.FileMode(hdr.Mode).Perm()
	if mode == 0 {
		mode = 0o644
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("extract local VM image bundle file: %w", err)
	}
	if _, err := io.Copy(f, r); err != nil {
		closeErr := f.Close()
		if closeErr != nil {
			return errors.Join(err, closeErr)
		}
		return fmt.Errorf("write local VM image bundle file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write local VM image bundle file: %w", err)
	}
	return os.Chmod(target, mode)
}

func extractLocalVMImageTarSymlink(root, target string, hdr *tar.Header) error {
	if err := rejectLocalVMImageSymlinkInPath(root, filepath.Dir(target)); err != nil {
		return err
	}
	if err := validateLocalVMImageSymlinkTarget(root, target, hdr.Linkname); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create local VM image bundle symlink dir: %w", err)
	}
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("local VM image bundle entry %q already exists", hdr.Name)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect local VM image bundle symlink: %w", err)
	}
	if err := os.Symlink(hdr.Linkname, target); err != nil {
		return fmt.Errorf("extract local VM image bundle symlink: %w", err)
	}
	return nil
}

func rejectLocalVMImageSymlinkInPath(root, target string) error {
	rel, err := localVMImageRelativePath(root, target)
	if err != nil {
		return err
	}
	return rejectLocalVMImageSymlinkRelativePath(root, rel)
}

func rejectLocalVMImageSymlinkRelativePath(root, rel string) error {
	current := filepath.Clean(root)
	for _, part := range localVMImagePathParts(rel) {
		current = filepath.Join(current, part)
		stop, err := rejectLocalVMImagePathPart(current)
		if err != nil || stop {
			return err
		}
	}
	return nil
}

func localVMImagePathParts(rel string) []string {
	var parts []string
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part != "" && part != "." {
			parts = append(parts, part)
		}
	}
	return parts
}

func localVMImageRelativePath(root, target string) (string, error) {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	if !localVMImageIsSubpath(root, target) {
		return "", fmt.Errorf("local VM image bundle path %q escapes staging dir", target)
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." {
		return "", err
	}
	return rel, nil
}

func rejectLocalVMImagePathPart(current string) (bool, error) {
	info, err := os.Lstat(current)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, fmt.Errorf("inspect local VM image bundle path: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("local VM image bundle path %q contains symlink", current)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("local VM image bundle path %q is not a directory", current)
	}
	return false, nil
}

func rejectExistingLocalVMImageSymlink(target string) error {
	info, err := os.Lstat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect local VM image bundle target: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("local VM image bundle target %q is a symlink", target)
	}
	return nil
}

func localVMImageRootFS(stagingDir string) (string, string, int64, error) {
	var foundName string
	for _, name := range []string{"rootfs.ext4", "rootfs.ext4.zst", "rootfs.ext4.zstd"} {
		if _, err := os.Lstat(filepath.Join(stagingDir, name)); err == nil {
			if foundName != "" {
				return "", "", 0, fmt.Errorf("local VM image bundle must include exactly one rootfs artifact")
			}
			foundName = name
		} else if !os.IsNotExist(err) {
			return "", "", 0, fmt.Errorf("inspect local VM image rootfs: %w", err)
		}
	}
	if foundName == "" {
		return "", "", 0, fmt.Errorf("local VM image bundle must include exactly one rootfs artifact")
	}
	path, err := localVMImageSafeArtifactPath(stagingDir, foundName)
	if err != nil {
		return "", "", 0, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", "", 0, fmt.Errorf("stat local VM image rootfs: %w", err)
	}
	if info.Size() <= 0 {
		return "", "", 0, fmt.Errorf("local VM image rootfs must be non-empty")
	}
	return foundName, path, info.Size(), nil
}

func localVMImageSafeArtifactPath(stagingDir, name string) (string, error) {
	path := filepath.Join(stagingDir, name)
	root, err := filepath.EvalSymlinks(stagingDir)
	if err != nil {
		return "", fmt.Errorf("resolve local VM image staging dir: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve local VM image artifact %q: %w", name, err)
	}
	if !localVMImageIsSubpath(root, resolved) {
		return "", fmt.Errorf("local VM image artifact %q escapes bundle", name)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat local VM image artifact %q: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("local VM image artifact %q must be a regular file", name)
	}
	return resolved, nil
}

func localVMImageContentID(name, rootFSPath, kernelPath, firecrackerPath, jailerPath string, capabilities localVMImageManifestCapabilities) (string, error) {
	artifacts := []string{rootFSPath, kernelPath, firecrackerPath}
	if strings.TrimSpace(jailerPath) != "" {
		artifacts = append(artifacts, jailerPath)
	}
	return localVMImageContentIDWithCapabilitiesHash(name, artifacts, capabilities, hashLocalVMImageCapabilities)
}

func legacyLocalVMImageContentID(name, rootFSPath, kernelPath, firecrackerPath string, capabilities localVMImageManifestCapabilities) (string, error) {
	return localVMImageContentIDWithCapabilitiesHash(name, []string{rootFSPath, kernelPath, firecrackerPath}, capabilities, hashLegacyLocalVMImageCapabilities)
}

func localVMImageContentIDWithCapabilitiesHash(name string, artifactPaths []string, capabilities localVMImageManifestCapabilities, hashCapabilities func(io.Writer, localVMImageManifestCapabilities) error) (string, error) {
	h := sha256.New()
	if _, err := h.Write([]byte(name)); err != nil {
		return "", err
	}
	if err := hashCapabilities(h, capabilities); err != nil {
		return "", err
	}
	for _, path := range artifactPaths {
		if _, err := h.Write([]byte{0}); err != nil {
			return "", err
		}
		if err := hashLocalVMImageFile(h, path); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashLocalVMImageCapabilities(w io.Writer, capabilities localVMImageManifestCapabilities) error {
	parts := []string{
		capabilities.ImageProfile,
		capabilities.Distro,
		capabilities.DistroVersion,
		capabilities.DefaultUser,
		capabilities.KernelPolicy,
		capabilities.GuestInit,
		capabilities.GuestSystemInit,
		capabilities.MetadataDriver,
		fmt.Sprintf("%t", capabilities.SnapSupportSet),
		fmt.Sprintf("%t", capabilities.SnapSupport),
		capabilities.KernelVersion,
		capabilities.UbuntuKernelVersion,
	}
	for _, part := range parts {
		if _, err := w.Write([]byte{0}); err != nil {
			return err
		}
		if _, err := w.Write([]byte(part)); err != nil {
			return err
		}
	}
	return nil
}

func hashLegacyLocalVMImageCapabilities(w io.Writer, capabilities localVMImageManifestCapabilities) error {
	parts := []string{
		capabilities.ImageProfile,
		capabilities.KernelPolicy,
		capabilities.GuestInit,
		fmt.Sprintf("%t", capabilities.SnapSupportSet),
		fmt.Sprintf("%t", capabilities.SnapSupport),
		capabilities.KernelVersion,
		capabilities.UbuntuKernelVersion,
	}
	for _, part := range parts {
		if _, err := w.Write([]byte{0}); err != nil {
			return err
		}
		if _, err := w.Write([]byte(part)); err != nil {
			return err
		}
	}
	return nil
}

func hashLocalVMImageFile(w io.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open local VM image artifact: %w", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := io.Copy(w, f); err != nil {
		return fmt.Errorf("hash local VM image artifact: %w", err)
	}
	return nil
}

func installLocalVMImageBlob(blobDir, rootFSName, rootFSSource, kernelSource, firecrackerSource, jailerSource, version, name string, rootFSSize int64, capabilities localVMImageManifestCapabilities) error {
	tmpDir, err := createLocalVMImageBlobTemp(blobDir)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(tmpDir)
		}
	}()
	manifest, err := populateLocalVMImageBlob(tmpDir, rootFSName, rootFSSource, kernelSource, firecrackerSource, jailerSource, version, name, rootFSSize, capabilities)
	if err != nil {
		return err
	}
	installed, err := installCompletedLocalVMImageBlob(blobDir, tmpDir, manifest)
	if err != nil {
		return err
	}
	cleanup = !installed
	return nil
}

func createLocalVMImageBlobTemp(blobDir string) (string, error) {
	blobsRoot := filepath.Dir(blobDir)
	if err := os.MkdirAll(blobsRoot, 0o755); err != nil {
		return "", fmt.Errorf("create local VM image blobs dir: %w", err)
	}
	tmpDir, err := os.MkdirTemp(blobsRoot, ".tmp-*")
	if err != nil {
		return "", fmt.Errorf("create local VM image blob temp dir: %w", err)
	}
	return tmpDir, nil
}

func populateLocalVMImageBlob(tmpDir, rootFSName, rootFSSource, kernelSource, firecrackerSource, jailerSource, version, name string, rootFSSize int64, capabilities localVMImageManifestCapabilities) (vmImageManifest, error) {
	if err := copyLocalVMImageArtifacts(tmpDir, rootFSName, rootFSSource, kernelSource, firecrackerSource, jailerSource); err != nil {
		return vmImageManifest{}, err
	}
	checksums, err := localVMImageChecksums(tmpDir, rootFSName, "jailer")
	if err != nil {
		return vmImageManifest{}, err
	}
	manifest := localVMImageManifest(name, version, rootFSName, rootFSSize, checksums, capabilities)
	if err := manifest.validate(); err != nil {
		return vmImageManifest{}, err
	}
	if err := writeManifestFile(filepath.Join(tmpDir, "manifest.json"), manifest); err != nil {
		return vmImageManifest{}, err
	}
	if err := writeLocalVMImageChecksums(filepath.Join(tmpDir, "checksums.txt"), checksums, rootFSName, "jailer"); err != nil {
		return vmImageManifest{}, err
	}
	return manifest, nil
}

func copyLocalVMImageArtifacts(tmpDir, rootFSName, rootFSSource, kernelSource, firecrackerSource, jailerSource string) error {
	if err := copyLocalVMImageFile(rootFSSource, filepath.Join(tmpDir, rootFSName), 0o644); err != nil {
		return err
	}
	if err := copyLocalVMImageFile(kernelSource, filepath.Join(tmpDir, "vmlinux"), 0o644); err != nil {
		return err
	}
	if err := copyLocalVMImageFile(firecrackerSource, filepath.Join(tmpDir, "firecracker"), 0o755); err != nil {
		return err
	}
	return copyLocalVMImageFile(jailerSource, filepath.Join(tmpDir, "jailer"), 0o755)
}

func localVMImageManifest(name, version, rootFSName string, rootFSSize int64, checksums map[string]string, capabilities localVMImageManifestCapabilities) vmImageManifest {
	manifest := vmImageManifest{
		Name:         "yeet-local-" + name,
		Version:      version,
		Architecture: "x86_64",
		Kernel:       "vmlinux",
		RootFS:       rootFSName,
		Firecracker:  "firecracker",
		Jailer:       "jailer",
		RootFSSize:   rootFSSize,
		Checksums:    checksums,
	}
	capabilities.applyToManifest(&manifest)
	return manifest
}

func (c localVMImageManifestCapabilities) applyToManifest(manifest *vmImageManifest) {
	manifest.ImageProfile = c.ImageProfile
	manifest.Distro = c.Distro
	manifest.DistroVersion = c.DistroVersion
	manifest.DefaultUser = c.DefaultUser
	manifest.KernelPolicy = c.KernelPolicy
	manifest.GuestInit = c.GuestInit
	manifest.GuestSystemInit = c.GuestSystemInit
	manifest.MetadataDriver = c.MetadataDriver
	if c.SnapSupportSet {
		snapSupport := c.SnapSupport
		manifest.SnapSupport = &snapSupport
	}
	manifest.KernelVersion = c.KernelVersion
	manifest.UbuntuKernelVersion = c.UbuntuKernelVersion
}

func localVMImageCapabilitiesFromManifest(manifest vmImageManifest) localVMImageManifestCapabilities {
	caps := localVMImageManifestCapabilities{
		ImageProfile:        manifest.ImageProfile,
		Distro:              manifest.Distro,
		DistroVersion:       manifest.DistroVersion,
		DefaultUser:         manifest.DefaultUser,
		KernelPolicy:        manifest.KernelPolicy,
		GuestInit:           manifest.GuestInit,
		GuestSystemInit:     manifest.GuestSystemInit,
		MetadataDriver:      manifest.MetadataDriver,
		KernelVersion:       manifest.KernelVersion,
		UbuntuKernelVersion: manifest.UbuntuKernelVersion,
	}
	if manifest.SnapSupport != nil {
		caps.SnapSupportSet = true
		caps.SnapSupport = *manifest.SnapSupport
	}
	return caps
}

func installCompletedLocalVMImageBlob(blobDir, tmpDir string, manifest vmImageManifest) (bool, error) {
	if _, err := os.Stat(blobDir); err == nil {
		return false, validateExistingLocalVMImageBlob(blobDir, manifest)
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("inspect local VM image blob: %w", err)
	}
	if err := os.Rename(tmpDir, blobDir); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, validateExistingLocalVMImageBlob(blobDir, manifest)
		}
		if _, statErr := os.Stat(blobDir); statErr == nil {
			return false, validateExistingLocalVMImageBlob(blobDir, manifest)
		}
		return false, fmt.Errorf("install local VM image blob: %w", err)
	}
	return true, nil
}

func validateExistingLocalVMImageBlob(blobDir string, want vmImageManifest) error {
	info, err := os.Stat(blobDir)
	if err != nil {
		return fmt.Errorf("existing local VM image blob invalid: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("existing local VM image blob invalid: not a directory")
	}
	got, err := readLocalVMImageBlobManifest(blobDir)
	if err != nil {
		return fmt.Errorf("existing local VM image blob invalid: %w", err)
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("existing local VM image blob invalid: manifest differs")
	}
	if err := localVMImageVerifyManifestArtifacts(blobDir, got); err != nil {
		return fmt.Errorf("existing local VM image blob invalid: %w", err)
	}
	if _, err := os.Stat(filepath.Join(blobDir, "checksums.txt")); err != nil {
		return fmt.Errorf("existing local VM image blob invalid: %w", err)
	}
	return nil
}

func readLocalVMImageBlobManifest(blobDir string) (vmImageManifest, error) {
	raw, err := os.ReadFile(filepath.Join(blobDir, "manifest.json"))
	if err != nil {
		return vmImageManifest{}, fmt.Errorf("read manifest: %w", err)
	}
	var manifest vmImageManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return vmImageManifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	if err := manifest.validate(); err != nil {
		return vmImageManifest{}, err
	}
	return manifest, nil
}

func copyLocalVMImageFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open local VM image artifact: %w", err)
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create local VM image artifact dir: %w", err)
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("create local VM image artifact: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy local VM image artifact: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("write local VM image artifact: %w", err)
	}
	if err := os.Chmod(dst, mode); err != nil {
		return fmt.Errorf("chmod local VM image artifact: %w", err)
	}
	return nil
}

func localVMImageChecksums(dir, rootFSName, jailerName string) (map[string]string, error) {
	names := []string{rootFSName, "vmlinux", "firecracker"}
	if strings.TrimSpace(jailerName) != "" {
		names = append(names, jailerName)
	}
	checksums := make(map[string]string, len(names))
	for _, name := range names {
		sum, err := sha256File(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("checksum local VM image artifact %q: %w", name, err)
		}
		checksums[name] = sum
	}
	return checksums, nil
}

func localVMImageVerifyManifestArtifacts(dir string, manifest vmImageManifest) error {
	for _, name := range manifest.artifactNames() {
		want := strings.TrimSpace(manifest.Checksums[name])
		got, err := sha256File(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("verify local VM image artifact %q checksum: %w", name, err)
		}
		if !strings.EqualFold(got, want) {
			return fmt.Errorf("local VM image artifact %q checksum mismatch: got %s, want %s", name, got, want)
		}
	}
	return nil
}

func writeLocalVMImageChecksums(path string, checksums map[string]string, rootFSName, jailerName string) error {
	var b strings.Builder
	names := []string{rootFSName, "vmlinux", "firecracker"}
	if strings.TrimSpace(jailerName) != "" {
		names = append(names, jailerName)
	}
	for _, name := range names {
		_, _ = fmt.Fprintf(&b, "%s  %s\n", checksums[name], name)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write local VM image checksums: %w", err)
	}
	return nil
}

func readLocalVMImageRef(path string) (localVMImageRef, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return localVMImageRef{}, err
	}
	var ref localVMImageRef
	if err := json.Unmarshal(raw, &ref); err != nil {
		return localVMImageRef{}, fmt.Errorf("decode local VM image ref: %w", err)
	}
	return ref, nil
}

func writeLocalVMImageRef(path string, ref localVMImageRef) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create local VM image ref dir: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".ref.json.tmp-*")
	if err != nil {
		return fmt.Errorf("create temp local VM image ref: %w", err)
	}
	tmpPath := f.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(ref); err != nil {
		_ = f.Close()
		return fmt.Errorf("write local VM image ref: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write local VM image ref: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("install local VM image ref: %w", err)
	}
	cleanup = false
	return nil
}

func removeEmptyLocalVMImageRefDirs(cacheRoot, start string) {
	refsRoot := filepath.Join(cacheRoot, "local", "refs")
	for dir := filepath.Clean(start); localVMImageIsSubpath(refsRoot, dir) && dir != refsRoot; dir = filepath.Dir(dir) {
		if err := os.Remove(dir); err != nil {
			return
		}
	}
}

func validateLocalVMImageSymlink(root, linkPath string) error {
	target, err := os.Readlink(linkPath)
	if err != nil {
		return fmt.Errorf("read local VM image symlink: %w", err)
	}
	return validateLocalVMImageSymlinkTarget(root, linkPath, target)
}

func validateLocalVMImageSymlinkTarget(root, linkPath, target string) error {
	root = filepath.Clean(root)
	canonicalRoot := root
	if resolvedRoot, err := filepath.EvalSymlinks(root); err == nil {
		canonicalRoot = resolvedRoot
	}
	resolved := target
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(filepath.Dir(linkPath), target)
	}
	resolved = filepath.Clean(resolved)
	if localVMImageIsSubpath(root, resolved) {
		return nil
	}
	if canonical, err := filepath.EvalSymlinks(resolved); err == nil && localVMImageIsSubpath(canonicalRoot, canonical) {
		return nil
	}
	return fmt.Errorf("local VM image symlink %q escapes bundle", linkPath)
}

func localVMImageIsSubpath(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
