// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"archive/tar"
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/yeetrun/yeet/pkg/db"
	"golang.org/x/sys/unix"
)

const (
	maxVMRuntimeImportNameBytes = 128
	maxVMRuntimeTarRemainder    = 10 * 512
	maxVMRuntimeLocalAliasBytes = 64 << 10
	vmRuntimeLocalAliasDirname  = "local-aliases"
)

var vmRuntimeImportFiles = map[string]struct {
	mode os.FileMode
	max  int64
}{
	vmRuntimeManifestFilename: {mode: 0o644, max: maxVMRuntimeManifestBytes},
	"firecracker":             {mode: 0o755, max: maxVMRuntimeBinaryBytes},
	"jailer":                  {mode: 0o755, max: maxVMRuntimeBinaryBytes},
}

type vmRuntimeLocalAlias struct {
	SchemaVersion  int    `json:"schema_version"`
	Name           string `json:"name"`
	Architecture   string `json:"architecture"`
	RuntimeID      string `json:"runtime_id"`
	ManifestSHA256 string `json:"manifest_sha256"`
	Source         string `json:"source"`
}

// importVMRuntime consumes the authenticated client's archive as untrusted
// input, validates the complete runtime pair, and publishes one immutable cache
// entry without replacing an existing name.
func importVMRuntime(ctx context.Context, cacheRoot, name string, reader io.Reader) (artifact db.VMRuntimeArtifactConfig, retErr error) {
	root, err := prepareVMRuntimeImportRequest(cacheRoot, name, reader)
	if err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}

	staging, manifest, ref, err := prepareVMRuntimeImport(ctx, root, reader)
	if err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}
	published := false
	defer func() {
		retErr = cleanupUnpublishedVMRuntimeImport(staging, published, retErr)
	}()
	alias := vmRuntimeLocalAlias{
		SchemaVersion: 1, Name: name, Architecture: manifest.Architecture,
		RuntimeID: manifest.RuntimeID, ManifestSHA256: ref.ManifestSHA, Source: "local:" + name,
	}
	aliasDir, aliasPath, err := prepareVMRuntimeLocalAlias(root, alias)
	if err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}
	aliasLock := vmRuntimeCacheLock(aliasPath)
	aliasLock.Lock()
	defer aliasLock.Unlock()
	if err := rejectVMRuntimeLocalAliasCollision(aliasPath, alias); err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}

	artifact, published, err = publishVMRuntimeImportedCacheEntry(ctx, root, staging, manifest, ref)
	if err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}
	if err := publishVMRuntimeLocalAlias(aliasDir, alias); err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}
	artifact.Source = alias.Source
	return artifact, nil
}

func prepareVMRuntimeImportRequest(cacheRoot, name string, reader io.Reader) (string, error) {
	if err := validateVMRuntimeImportName(name); err != nil {
		return "", err
	}
	if reader == nil {
		return "", fmt.Errorf("VM runtime import archive is required")
	}
	root, err := validatedVMRuntimeCacheRoot(cacheRoot)
	if err != nil {
		return "", err
	}
	if _, err := ensureTrustedVMRuntimeCacheTree(root); err != nil {
		return "", err
	}
	return root, nil
}

func cleanupUnpublishedVMRuntimeImport(staging string, published bool, retErr error) error {
	if published {
		return retErr
	}
	if err := os.RemoveAll(staging); err != nil {
		return errors.Join(retErr, fmt.Errorf("remove VM runtime import staging directory: %w", err))
	}
	return retErr
}

func prepareVMRuntimeImport(ctx context.Context, root string, reader io.Reader) (staging string, manifest vmRuntimeManifest, ref vmRuntimeCatalogRef, retErr error) {
	staging, err := os.MkdirTemp(root, ".runtime-import-")
	if err != nil {
		return "", manifest, ref, fmt.Errorf("create VM runtime import staging directory: %w", err)
	}
	cleanupPath := staging
	ready := false
	defer func() {
		if !ready {
			retErr = errors.Join(retErr, os.RemoveAll(cleanupPath))
		}
	}()
	if err := secureVMRuntimeStagingDirectory(staging); err != nil {
		return "", manifest, ref, err
	}
	if err := extractVMRuntimeImportArchive(ctx, staging, reader); err != nil {
		return "", manifest, ref, err
	}
	raw, manifest, ref, err := readVMRuntimeImportManifest(staging)
	if err != nil {
		return "", manifest, ref, err
	}
	if _, err := validateVMRuntimeImportedArtifact(ctx, staging, ref, raw); err != nil {
		return "", manifest, ref, err
	}
	if err := finalizeVMRuntimeImportStaging(staging); err != nil {
		return "", manifest, ref, err
	}
	ready = true
	return staging, manifest, ref, nil
}

func finalizeVMRuntimeImportStaging(staging string) error {
	if err := os.Chmod(staging, 0o755); err != nil {
		return fmt.Errorf("set imported VM runtime directory permissions: %w", err)
	}
	return syncVMRuntimeDirectory(staging)
}

func readVMRuntimeImportManifest(staging string) ([]byte, vmRuntimeManifest, vmRuntimeCatalogRef, error) {
	raw, err := os.ReadFile(filepath.Join(staging, vmRuntimeManifestFilename))
	if err != nil {
		return nil, vmRuntimeManifest{}, vmRuntimeCatalogRef{}, fmt.Errorf("read imported VM runtime manifest: %w", err)
	}
	manifest, err := decodeVMRuntimeManifest(raw)
	if err != nil {
		return nil, vmRuntimeManifest{}, vmRuntimeCatalogRef{}, err
	}
	ref := vmRuntimeCatalogRef{
		RuntimeID: manifest.RuntimeID, ManifestSHA: vmRuntimeSHA256Bytes(raw),
		UpstreamVersion: manifest.Upstream.Version, Support: manifest.Support.State,
	}
	return raw, manifest, ref, nil
}

func prepareVMRuntimeLocalAlias(root string, alias vmRuntimeLocalAlias) (string, string, error) {
	aliasDir, err := ensureVMRuntimeLocalAliasDir(root)
	if err != nil {
		return "", "", err
	}
	aliasPath := vmRuntimeLocalAliasPath(aliasDir, alias.Name)
	return aliasDir, aliasPath, nil
}

func publishVMRuntimeImportedCacheEntry(ctx context.Context, root, staging string, manifest vmRuntimeManifest, ref vmRuntimeCatalogRef) (db.VMRuntimeArtifactConfig, bool, error) {
	runtimeParentPath := filepath.Join(root, manifest.Architecture, manifest.RuntimeID)
	runtimeLock := vmRuntimeCacheLock(runtimeParentPath)
	runtimeLock.Lock()
	defer runtimeLock.Unlock()
	runtimeParent, err := ensureTrustedVMRuntimeCacheTree(root, manifest.Architecture, manifest.RuntimeID)
	if err != nil {
		return db.VMRuntimeArtifactConfig{}, false, err
	}
	if err := rejectVMRuntimeImportDigestCollision(runtimeParent, ref.ManifestSHA); err != nil {
		return db.VMRuntimeArtifactConfig{}, false, err
	}
	return publishVMRuntimeImportTarget(ctx, root, staging, runtimeParent, ref)
}

func publishVMRuntimeImportTarget(ctx context.Context, root, staging, runtimeParent string, ref vmRuntimeCatalogRef) (artifact db.VMRuntimeArtifactConfig, published bool, err error) {
	final := filepath.Join(runtimeParent, ref.ManifestSHA)
	if _, err := os.Lstat(final); err == nil {
		artifact, err = validateVMRuntimeImportedCachedArtifact(ctx, final, ref)
		if err != nil {
			return db.VMRuntimeArtifactConfig{}, false, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return db.VMRuntimeArtifactConfig{}, false, fmt.Errorf("inspect imported VM runtime cache target: %w", err)
	} else {
		if err := publishVMRuntimeImportNoReplace(root, staging, runtimeParent, final); err != nil {
			if !errors.Is(err, syscall.EEXIST) {
				return db.VMRuntimeArtifactConfig{}, false, fmt.Errorf("publish imported VM runtime cache entry: %w", err)
			}
			if err := rejectVMRuntimeImportDigestCollision(runtimeParent, ref.ManifestSHA); err != nil {
				return db.VMRuntimeArtifactConfig{}, false, err
			}
		} else {
			published = true
			if err := syncVMRuntimeDirectory(runtimeParent); err != nil {
				return db.VMRuntimeArtifactConfig{}, published, err
			}
		}
		artifact, err = validateVMRuntimeImportedCachedArtifact(ctx, final, ref)
		if err != nil {
			return db.VMRuntimeArtifactConfig{}, published, err
		}
	}
	return artifact, published, nil
}

func validateVMRuntimeImportName(name string) error {
	if name == "" || name != strings.TrimSpace(name) || len(name) > maxVMRuntimeImportNameBytes || !utf8.ValidString(name) {
		return fmt.Errorf("invalid VM runtime import name %q", name)
	}
	for _, char := range name {
		if unicode.IsControl(char) {
			return fmt.Errorf("invalid VM runtime import name %q", name)
		}
	}
	return nil
}

// resolveLocalVMRuntime resolves a durable local alias without consulting the
// network and revalidates the exact immutable cache entry before returning it.
func resolveLocalVMRuntime(ctx context.Context, cacheRoot, name string) (db.VMRuntimeArtifactConfig, error) {
	if err := validateVMRuntimeImportName(name); err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}
	root, err := validatedVMRuntimeCacheRoot(cacheRoot)
	if err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}
	if err := validateTrustedVMRuntimeCachePath(root, true); err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}
	alias, err := resolveVMRuntimeLocalAlias(root, name)
	if err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}
	dir, manifestRaw, manifest, err := readVMRuntimeLocalManifest(root, name, alias)
	if err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}
	ref := vmRuntimeCatalogRef{
		RuntimeID: alias.RuntimeID, ManifestSHA: alias.ManifestSHA256,
		UpstreamVersion: manifest.Upstream.Version, Support: manifest.Support.State,
	}
	artifact, err := validateVMRuntimeImportedArtifact(ctx, dir, ref, manifestRaw)
	if err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}
	artifact.Source = alias.Source
	return artifact, nil
}

func resolveVMRuntimeLocalAlias(root, name string) (vmRuntimeLocalAlias, error) {
	aliasDir := filepath.Join(root, vmRuntimeLocalAliasDirname)
	if err := validateVMRuntimeLocalAliasDir(aliasDir); err != nil {
		return vmRuntimeLocalAlias{}, err
	}
	alias, err := readVMRuntimeLocalAlias(vmRuntimeLocalAliasPath(aliasDir, name))
	if err != nil {
		return vmRuntimeLocalAlias{}, err
	}
	if alias.Name != name {
		return vmRuntimeLocalAlias{}, fmt.Errorf("VM runtime local alias name %q does not match requested %q", alias.Name, name)
	}
	return alias, nil
}

func readVMRuntimeLocalManifest(root, name string, alias vmRuntimeLocalAlias) (string, []byte, vmRuntimeManifest, error) {
	dir, err := validateExistingVMRuntimeLocalArtifactTree(root, alias)
	if err != nil {
		return "", nil, vmRuntimeManifest{}, err
	}
	manifestPath := filepath.Join(dir, vmRuntimeManifestFilename)
	if err := validateTrustedVMRuntimeCachePath(manifestPath, false); err != nil {
		return "", nil, vmRuntimeManifest{}, err
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", nil, vmRuntimeManifest{}, fmt.Errorf("read local VM runtime manifest: %w", err)
	}
	if vmRuntimeSHA256Bytes(raw) != alias.ManifestSHA256 {
		return "", nil, vmRuntimeManifest{}, fmt.Errorf("local VM runtime alias %q manifest digest does not match cached manifest", name)
	}
	manifest, err := decodeVMRuntimeManifest(raw)
	if err != nil {
		return "", nil, vmRuntimeManifest{}, err
	}
	if manifest.RuntimeID != alias.RuntimeID || manifest.Architecture != alias.Architecture {
		return "", nil, vmRuntimeManifest{}, fmt.Errorf("local VM runtime alias %q does not bind the cached manifest identity", name)
	}
	return dir, raw, manifest, nil
}

func ensureVMRuntimeLocalAliasDir(root string) (string, error) {
	dir, err := ensureTrustedVMRuntimeCacheTree(root, vmRuntimeLocalAliasDirname)
	if err != nil {
		return "", err
	}
	wantUID, wantGID := vmRuntimeLocalAliasOwner()
	if err := os.Chown(dir, int(wantUID), int(wantGID)); err != nil {
		return "", fmt.Errorf("set VM runtime local alias directory owner: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("secure VM runtime local alias directory: %w", err)
	}
	if err := validateVMRuntimeLocalAliasDir(dir); err != nil {
		return "", err
	}
	return dir, nil
}

func validateVMRuntimeLocalAliasDir(dir string) error {
	if err := validateTrustedVMRuntimeCachePath(dir, true); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect VM runtime local alias directory: %w", err)
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("VM runtime local alias directory permissions are %04o, want 0700", info.Mode().Perm())
	}
	return validateVMRuntimeLocalAliasOwner(dir, info)
}

func vmRuntimeLocalAliasPath(dir, name string) string {
	encoded := base64.RawURLEncoding.EncodeToString([]byte(name))
	return filepath.Join(dir, encoded+".json")
}

func (alias vmRuntimeLocalAlias) validate() error {
	if alias.SchemaVersion != 1 {
		return fmt.Errorf("unsupported VM runtime local alias schema_version %d", alias.SchemaVersion)
	}
	if err := validateVMRuntimeImportName(alias.Name); err != nil {
		return err
	}
	architecture, err := normalizeVMRuntimeArchitecture(alias.Architecture)
	if err != nil || architecture != alias.Architecture {
		return fmt.Errorf("VM runtime local alias has invalid architecture %q", alias.Architecture)
	}
	if _, err := vmRuntimeVersionFromID(alias.RuntimeID); err != nil {
		return err
	}
	if !validVMRuntimeSHA256(alias.ManifestSHA256) {
		return fmt.Errorf("VM runtime local alias has invalid manifest_sha256 %q", alias.ManifestSHA256)
	}
	if alias.Source != "local:"+alias.Name {
		return fmt.Errorf("VM runtime local alias source %q does not match name %q", alias.Source, alias.Name)
	}
	return nil
}

func decodeVMRuntimeLocalAlias(raw []byte) (vmRuntimeLocalAlias, error) {
	object, err := decodeVMRuntimeJSONObject(raw, "VM runtime local alias")
	if err != nil {
		return vmRuntimeLocalAlias{}, err
	}
	if err := requireVMRuntimeJSONFields(
		object, "VM runtime local alias",
		"schema_version", "name", "architecture", "runtime_id", "manifest_sha256", "source",
	); err != nil {
		return vmRuntimeLocalAlias{}, err
	}
	var alias vmRuntimeLocalAlias
	if err := decodeStrictVMRuntimeJSON(raw, &alias, "VM runtime local alias"); err != nil {
		return vmRuntimeLocalAlias{}, err
	}
	if err := alias.validate(); err != nil {
		return vmRuntimeLocalAlias{}, err
	}
	return alias, nil
}

func readVMRuntimeLocalAlias(path string) (vmRuntimeLocalAlias, error) {
	dir, parent, _, err := openVMJailStoragePath(filepath.Dir(path))
	if err != nil {
		return vmRuntimeLocalAlias{}, fmt.Errorf("open VM runtime local alias directory without following symlinks: %w", err)
	}
	defer func() { _ = dir.Close() }()
	if parent != nil {
		defer func() { _ = parent.Close() }()
	}
	dirInfo, err := dir.Stat()
	if err != nil {
		return vmRuntimeLocalAlias{}, fmt.Errorf("inspect opened VM runtime local alias directory: %w", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		return vmRuntimeLocalAlias{}, fmt.Errorf("VM runtime local alias directory permissions are %04o, want 0700", dirInfo.Mode().Perm())
	}
	if err := validateVMRuntimeLocalAliasOwner(filepath.Dir(path), dirInfo); err != nil {
		return vmRuntimeLocalAlias{}, err
	}
	name := filepath.Base(path)
	file, err := openVMRuntimeLocalAliasFile(dir, name, path)
	if err != nil {
		return vmRuntimeLocalAlias{}, err
	}
	return readOpenVMRuntimeLocalAlias(dir, file, name, path)
}

func openVMRuntimeLocalAliasFile(dir *os.File, name, path string) (*os.File, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ELOOP) {
		return nil, fmt.Errorf("VM runtime local alias %s is a symbolic link", path)
	}
	if err != nil {
		return nil, fmt.Errorf("open VM runtime local alias without following symlinks: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("bind VM runtime local alias file descriptor")
	}
	return file, nil
}

func readOpenVMRuntimeLocalAlias(dir, file *os.File, name, path string) (vmRuntimeLocalAlias, error) {
	id, stat, err := vmJailerFileIdentityForFile(file)
	if err != nil {
		return vmRuntimeLocalAlias{}, closeVMJailerFileOnError(file, err)
	}
	if err := validateVMRuntimeLocalAliasStat(path, stat); err != nil {
		return vmRuntimeLocalAlias{}, closeVMJailerFileOnError(file, err)
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxVMRuntimeLocalAliasBytes+1))
	if err != nil {
		return vmRuntimeLocalAlias{}, closeVMJailerFileOnError(file, fmt.Errorf("read VM runtime local alias: %w", err))
	}
	if len(raw) > maxVMRuntimeLocalAliasBytes {
		return vmRuntimeLocalAlias{}, closeVMJailerFileOnError(file, fmt.Errorf("VM runtime local alias exceeds %d byte limit", maxVMRuntimeLocalAliasBytes))
	}
	if err := revalidateVMRuntimeLocalAlias(dir, file, name, path, id, stat); err != nil {
		return vmRuntimeLocalAlias{}, closeVMJailerFileOnError(file, err)
	}
	if err := file.Close(); err != nil {
		return vmRuntimeLocalAlias{}, fmt.Errorf("close VM runtime local alias: %w", err)
	}
	return decodeVMRuntimeLocalAlias(raw)
}

func revalidateVMRuntimeLocalAlias(dir, file *os.File, name, path string, id vmJailerFileIdentity, stat unix.Stat_t) error {
	postID, postStat, err := vmJailerFileIdentityForFile(file)
	if err != nil {
		return err
	}
	nameID, nameStat, err := vmJailerNameIdentityAt(dir, name)
	if err != nil {
		return fmt.Errorf("revalidate VM runtime local alias name: %w", err)
	}
	if id != postID || id != nameID || stat.Size != postStat.Size || stat.Size != nameStat.Size {
		return fmt.Errorf("VM runtime local alias changed while it was read")
	}
	if err := validateVMRuntimeLocalAliasStat(path, postStat); err != nil {
		return err
	}
	return validateVMRuntimeLocalAliasStat(path, nameStat)
}

func validateVMRuntimeLocalAliasStat(path string, stat unix.Stat_t) error {
	if uint32(stat.Mode)&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("VM runtime local alias %s must be a regular file", path)
	}
	if uint32(stat.Mode)&0o777 != 0o600 {
		return fmt.Errorf("VM runtime local alias permissions are %04o, want 0600", uint32(stat.Mode)&0o777)
	}
	wantUID, wantGID := vmRuntimeLocalAliasOwner()
	if stat.Uid != wantUID || stat.Gid != wantGID {
		return fmt.Errorf("VM runtime local alias %s is owned by %d:%d, want %d:%d", path, stat.Uid, stat.Gid, wantUID, wantGID)
	}
	return nil
}

func validateVMRuntimeLocalAliasOwner(path string, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("inspect VM runtime local alias owner %s", path)
	}
	wantUID, wantGID := vmRuntimeLocalAliasOwner()
	if stat.Uid != wantUID || stat.Gid != wantGID {
		return fmt.Errorf("VM runtime local alias %s is owned by %d:%d, want %d:%d", path, stat.Uid, stat.Gid, wantUID, wantGID)
	}
	return nil
}

func vmRuntimeLocalAliasOwner() (uint32, uint32) {
	if os.Geteuid() == 0 {
		return 0, 0
	}
	// Catch runs as root in production. Local development validates the
	// equivalent invariant against the developer process owner.
	return uint32(os.Geteuid()), uint32(os.Getegid())
}

func rejectVMRuntimeLocalAliasCollision(path string, want vmRuntimeLocalAlias) error {
	got, err := readVMRuntimeLocalAlias(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("VM runtime local alias %q already resolves to a different runtime identity", want.Name)
	}
	return nil
}

func publishVMRuntimeLocalAlias(dir string, alias vmRuntimeLocalAlias) (retErr error) {
	path := vmRuntimeLocalAliasPath(dir, alias.Name)
	if err := rejectVMRuntimeLocalAliasCollision(path, alias); err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect VM runtime local alias target: %w", err)
	}
	raw, err := json.Marshal(alias)
	if err != nil {
		return fmt.Errorf("encode VM runtime local alias: %w", err)
	}
	tempPath, err := stageVMRuntimeLocalAlias(dir, path, raw)
	if err != nil {
		return err
	}
	defer func() {
		retErr = errors.Join(retErr, cleanupVMRuntimeLocalAliasStaging(tempPath))
	}()
	if err := publishStagedVMRuntimeLocalAlias(dir, tempPath, path, alias); err != nil {
		return err
	}
	if err := syncVMRuntimeDirectory(dir); err != nil {
		return err
	}
	return verifyPublishedVMRuntimeLocalAlias(path, alias)
}

func cleanupVMRuntimeLocalAliasStaging(path string) error {
	err := os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove VM runtime local alias staging file: %w", err)
	}
	return nil
}

func verifyPublishedVMRuntimeLocalAlias(path string, alias vmRuntimeLocalAlias) error {
	got, err := readVMRuntimeLocalAlias(path)
	if err != nil {
		return err
	}
	if got != alias {
		return fmt.Errorf("published VM runtime local alias %q is not exact", alias.Name)
	}
	return nil
}

func stageVMRuntimeLocalAlias(dir, path string, raw []byte) (string, error) {
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return "", fmt.Errorf("create VM runtime local alias staging file: %w", err)
	}
	fail := func(err error) (string, error) {
		return "", errors.Join(err, temp.Close(), os.Remove(temp.Name()))
	}
	if _, err := temp.Write(raw); err != nil {
		return fail(fmt.Errorf("write VM runtime local alias: %w", err))
	}
	wantUID, wantGID := vmRuntimeLocalAliasOwner()
	if err := temp.Chown(int(wantUID), int(wantGID)); err != nil {
		return fail(fmt.Errorf("set VM runtime local alias owner: %w", err))
	}
	if err := temp.Chmod(0o600); err != nil {
		return fail(fmt.Errorf("set VM runtime local alias permissions: %w", err))
	}
	if err := temp.Sync(); err != nil {
		return fail(fmt.Errorf("sync VM runtime local alias: %w", err))
	}
	if err := temp.Close(); err != nil {
		return "", fmt.Errorf("close VM runtime local alias: %w", err)
	}
	return temp.Name(), nil
}

func publishStagedVMRuntimeLocalAlias(dir, tempPath, path string, alias vmRuntimeLocalAlias) error {
	dirFile, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open VM runtime local alias directory: %w", err)
	}
	publishErr := renameVMJailerUnitNameNoReplaceAt(
		int(dirFile.Fd()), filepath.Base(tempPath), int(dirFile.Fd()), filepath.Base(path),
	)
	closeErr := dirFile.Close()
	if publishErr != nil {
		if !errors.Is(publishErr, syscall.EEXIST) {
			return errors.Join(fmt.Errorf("publish VM runtime local alias: %w", publishErr), closeErr)
		}
		if err := rejectVMRuntimeLocalAliasCollision(path, alias); err != nil {
			return errors.Join(err, closeErr)
		}
		return closeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return nil
}

func validateExistingVMRuntimeLocalArtifactTree(root string, alias vmRuntimeLocalAlias) (string, error) {
	current := root
	for _, component := range []string{alias.Architecture, alias.RuntimeID, alias.ManifestSHA256} {
		current = filepath.Join(current, component)
		if err := validateTrustedVMRuntimeCachePath(current, true); err != nil {
			return "", err
		}
	}
	return current, nil
}

func extractVMRuntimeImportArchive(ctx context.Context, staging string, reader io.Reader) error {
	buffered := bufio.NewReader(&vmRuntimeContextReader{ctx: ctx, reader: reader})
	archive := tar.NewReader(buffered)
	seen := make(map[string]struct{}, len(vmRuntimeImportFiles))
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read VM runtime import archive: %w", err)
		}
		spec, ok := vmRuntimeImportFiles[header.Name]
		if !ok {
			return fmt.Errorf("VM runtime import archive has unexpected entry %q", header.Name)
		}
		if _, duplicate := seen[header.Name]; duplicate {
			return fmt.Errorf("VM runtime import archive has duplicate entry %q", header.Name)
		}
		if err := validateVMRuntimeImportHeader(header, spec.mode, spec.max); err != nil {
			return err
		}
		if err := extractVMRuntimeImportFile(staging, header.Name, spec.mode, header.Size, archive); err != nil {
			return err
		}
		seen[header.Name] = struct{}{}
	}
	for name := range vmRuntimeImportFiles {
		if _, ok := seen[name]; !ok {
			return fmt.Errorf("VM runtime import archive is missing required entry %q", name)
		}
	}
	return validateVMRuntimeTarRemainder(buffered)
}

func validateVMRuntimeImportHeader(header *tar.Header, mode os.FileMode, maxSize int64) error {
	if err := validateVMRuntimeImportHeaderLayout(header, mode); err != nil {
		return err
	}
	return validateVMRuntimeImportHeaderMetadata(header, maxSize)
}

func validateVMRuntimeImportHeaderLayout(header *tar.Header, mode os.FileMode) error {
	if header.Typeflag != tar.TypeReg {
		return fmt.Errorf("VM runtime import entry %q must be a regular file", header.Name)
	}
	if header.Format != tar.FormatUSTAR {
		return fmt.Errorf("VM runtime import entry %q must use USTAR format", header.Name)
	}
	if header.Mode != int64(mode) {
		return fmt.Errorf("VM runtime import entry %q permissions are %04o, want %04o", header.Name, header.Mode, mode)
	}
	return nil
}

func validateVMRuntimeImportHeaderMetadata(header *tar.Header, maxSize int64) error {
	if header.Uid != 0 || header.Gid != 0 {
		return fmt.Errorf("VM runtime import entry %q archive owner must be 0:0", header.Name)
	}
	if header.Linkname != "" || len(header.PAXRecords) != 0 {
		return fmt.Errorf("VM runtime import entry %q has unsupported metadata", header.Name)
	}
	if !header.ModTime.Equal(time.Unix(0, 0).UTC()) {
		return fmt.Errorf("VM runtime import entry %q has non-deterministic modification time", header.Name)
	}
	if header.Size <= 0 || header.Size > maxSize {
		return fmt.Errorf("VM runtime import entry %q size %d is outside the 1..%d byte limit", header.Name, header.Size, maxSize)
	}
	return nil
}

func extractVMRuntimeImportFile(staging, name string, mode os.FileMode, size int64, reader io.Reader) (retErr error) {
	path := filepath.Join(staging, name)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create staged VM runtime import entry %q: %w", name, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil && retErr == nil {
			retErr = fmt.Errorf("close staged VM runtime import entry %q: %w", name, closeErr)
		}
	}()
	written, err := io.CopyN(file, reader, size)
	if err != nil {
		return fmt.Errorf("read VM runtime import entry %q: %w", name, err)
	}
	if written != size {
		return fmt.Errorf("read VM runtime import entry %q: got %d bytes, want %d", name, written, size)
	}
	if err := file.Chmod(mode); err != nil {
		return fmt.Errorf("set staged VM runtime import entry %q permissions: %w", name, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync staged VM runtime import entry %q: %w", name, err)
	}
	return nil
}

func validateVMRuntimeTarRemainder(reader io.Reader) error {
	remainder, err := io.ReadAll(io.LimitReader(reader, maxVMRuntimeTarRemainder+1))
	if err != nil {
		return fmt.Errorf("read VM runtime import archive trailer: %w", err)
	}
	if len(remainder) > maxVMRuntimeTarRemainder {
		return fmt.Errorf("VM runtime import archive trailer is too large")
	}
	for _, value := range remainder {
		if value != 0 {
			return fmt.Errorf("VM runtime import archive has trailing non-zero data")
		}
	}
	return nil
}

func validateVMRuntimeImportedArtifact(ctx context.Context, dir string, ref vmRuntimeCatalogRef, manifestRaw []byte) (db.VMRuntimeArtifactConfig, error) {
	manifestPath := filepath.Join(dir, vmRuntimeManifestFilename)
	info, err := os.Lstat(manifestPath)
	if err != nil {
		return db.VMRuntimeArtifactConfig{}, fmt.Errorf("inspect imported VM runtime manifest: %w", err)
	}
	if info.Mode().Perm() != 0o644 {
		return db.VMRuntimeArtifactConfig{}, fmt.Errorf("imported VM runtime manifest permissions are %04o, want 0644", info.Mode().Perm())
	}
	return validateVMRuntimeArtifactDirectory(ctx, dir, ref, manifestRaw)
}

func validateVMRuntimeImportedCachedArtifact(ctx context.Context, dir string, ref vmRuntimeCatalogRef) (db.VMRuntimeArtifactConfig, error) {
	if err := validateTrustedVMRuntimeCachePath(dir, true); err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}
	manifestPath := filepath.Join(dir, vmRuntimeManifestFilename)
	if err := validateTrustedVMRuntimeCachePath(manifestPath, false); err != nil {
		return db.VMRuntimeArtifactConfig{}, err
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return db.VMRuntimeArtifactConfig{}, fmt.Errorf("read imported VM runtime manifest: %w", err)
	}
	return validateVMRuntimeImportedArtifact(ctx, dir, ref, raw)
}

func rejectVMRuntimeImportDigestCollision(runtimeParent, manifestSHA string) error {
	entries, err := os.ReadDir(runtimeParent)
	if err != nil {
		return fmt.Errorf("read VM runtime import identity directory: %w", err)
	}
	for _, entry := range entries {
		if entry.Name() != manifestSHA {
			return fmt.Errorf("VM runtime ID %q already exists with a different manifest digest", filepath.Base(runtimeParent))
		}
	}
	return nil
}

func publishVMRuntimeImportNoReplace(root, staging, parent, final string) error {
	rootDir, err := os.Open(root)
	if err != nil {
		return fmt.Errorf("open VM runtime import staging parent: %w", err)
	}
	defer func() { _ = rootDir.Close() }()
	parentDir, err := os.Open(parent)
	if err != nil {
		return fmt.Errorf("open VM runtime import target parent: %w", err)
	}
	defer func() { _ = parentDir.Close() }()
	return renameVMJailerUnitNameNoReplaceAt(
		int(rootDir.Fd()), filepath.Base(staging),
		int(parentDir.Fd()), filepath.Base(final),
	)
}

type vmRuntimeContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *vmRuntimeContextReader) Read(buffer []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.reader.Read(buffer)
	}
}
