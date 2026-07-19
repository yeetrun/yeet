// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"

	"golang.org/x/sys/unix"
)

func serviceIdentityMigrationBackupDir(rootDir, migrationID string) string {
	return filepath.Join(rootDir, "migrations", "service-identity", "backups", migrationID)
}

func captureServiceIdentityPathProof(path string) (serviceIdentityPathProof, error) {
	path = filepath.Clean(path)
	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return serviceIdentityPathProof{Path: path}, nil
	}
	if err != nil {
		return serviceIdentityPathProof{}, fmt.Errorf("open transaction path %s without following links: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	return captureServiceIdentityOpenFileProof(file, path)
}

func captureServiceIdentityOpenFileProof(file *os.File, path string) (serviceIdentityPathProof, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return serviceIdentityPathProof{}, fmt.Errorf("seek transaction path %s: %w", path, err)
	}
	info, err := file.Stat()
	if err != nil {
		return serviceIdentityPathProof{}, fmt.Errorf("inspect transaction path %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return serviceIdentityPathProof{}, fmt.Errorf("transaction path %s must be a regular file, got %s", path, info.Mode())
	}
	metadata, err := serviceIdentityMetadata(info)
	if err != nil {
		return serviceIdentityPathProof{}, fmt.Errorf("inspect transaction metadata %s: %w", path, err)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return serviceIdentityPathProof{}, fmt.Errorf("hash transaction path %s: %w", path, err)
	}
	proof := serviceIdentityPathProof{
		Path: path, Present: true, Mode: info.Mode(), UID: metadata.UID, GID: metadata.GID,
		Dev: metadata.Dev, Ino: metadata.Ino, Nlink: metadata.Nlink, Size: info.Size(), SHA256: hex.EncodeToString(hash.Sum(nil)),
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return serviceIdentityPathProof{}, fmt.Errorf("rewind transaction path %s: %w", path, err)
	}
	return proof, nil
}

func validateServiceIdentityPathProof(proof serviceIdentityPathProof) error {
	actual, err := captureServiceIdentityPathProof(proof.Path)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, proof) {
		return fmt.Errorf("transaction path %s changed from its durable provenance", proof.Path)
	}
	return nil
}

func captureServiceIdentityPathProofAt(root, rel, path string) (serviceIdentityPathProof, error) {
	parentFD, name, closeParent, err := openServiceIdentityMutationParent(root, rel)
	if err != nil {
		return serviceIdentityPathProof{}, err
	}
	defer closeParent()
	return captureServiceIdentityPathProofFromParent(parentFD, name, path)
}

func captureServiceIdentityPathProofFromParent(parentFD int, name, path string) (serviceIdentityPathProof, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return serviceIdentityPathProof{Path: filepath.Clean(path)}, nil
	}
	if err != nil {
		return serviceIdentityPathProof{}, fmt.Errorf("open transaction path %s relative to stable parent: %w", path, err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return serviceIdentityPathProof{}, fmt.Errorf("wrap transaction path %s", path)
	}
	defer func() { _ = file.Close() }()
	return captureServiceIdentityOpenFileProof(file, filepath.Clean(path))
}

func captureServiceIdentityTransactionProofFromParent(parentFD int, name, path string) (serviceIdentityPathProof, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return serviceIdentityPathProof{Path: filepath.Clean(path)}, nil
	}
	if err != nil {
		return serviceIdentityPathProof{}, fmt.Errorf("open transaction path %s relative to stable parent: %w", path, err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return serviceIdentityPathProof{}, fmt.Errorf("wrap transaction path %s", path)
	}
	defer func() { _ = file.Close() }()
	proof, err := captureServiceIdentityOpenFileProof(file, filepath.Clean(path))
	if err != nil {
		return serviceIdentityPathProof{}, err
	}
	if err := validateServiceIdentityPathProofRecord(proof, path); err != nil {
		return serviceIdentityPathProof{}, err
	}
	xattrs, err := listServiceIdentityOpenFileXattrs(file)
	if err != nil {
		return serviceIdentityPathProof{}, fmt.Errorf("inspect open transaction xattrs %s: %w", path, err)
	}
	if xattrs = unsupportedServiceIdentityTransactionXattrs(xattrs); len(xattrs) != 0 {
		return serviceIdentityPathProof{}, fmt.Errorf("transaction path %s has extended attributes that exact rollback cannot preserve: %s", path, strings.Join(xattrs, ", "))
	}
	return proof, nil
}

func validateServiceIdentityPathProofAt(root, rel string, proof serviceIdentityPathProof) error {
	actual, err := captureServiceIdentityPathProofAt(root, rel, proof.Path)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, proof) {
		return fmt.Errorf("transaction path %s changed from its durable provenance", proof.Path)
	}
	return nil
}

func validateServiceIdentityPathProofRecord(proof serviceIdentityPathProof, expectedPath string) error {
	expectedPath = filepath.Clean(expectedPath)
	if !filepath.IsAbs(expectedPath) || filepath.Clean(proof.Path) != expectedPath {
		return fmt.Errorf("transaction path proof %q does not match %q", proof.Path, expectedPath)
	}
	if proof.Present {
		if !validPresentServiceIdentityPathProof(proof) {
			return fmt.Errorf("transaction path %s lacks exact regular-file provenance", expectedPath)
		}
		return nil
	}
	if !emptyAbsentServiceIdentityPathProof(proof) {
		return fmt.Errorf("absent transaction path %s carries file provenance", expectedPath)
	}
	return nil
}

func validPresentServiceIdentityPathProof(proof serviceIdentityPathProof) bool {
	return proof.Mode.IsRegular() && proof.Mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0 &&
		proof.Dev != 0 && proof.Ino != 0 && proof.Nlink == 1 && proof.SHA256 != "" && proof.Size >= 0
}

func emptyAbsentServiceIdentityPathProof(proof serviceIdentityPathProof) bool {
	return proof.Mode == 0 && proof.UID == 0 && proof.GID == 0 && proof.Dev == 0 && proof.Ino == 0 &&
		proof.Nlink == 0 && proof.Size == 0 && proof.SHA256 == ""
}

func validateServiceIdentityTransactionPath(proof serviceIdentityPathProof) error {
	if err := validateServiceIdentityPathProofRecord(proof, proof.Path); err != nil {
		return err
	}
	if !proof.Present {
		return nil
	}
	xattrs, err := listServiceIdentityXattrs(proof.Path)
	if err != nil {
		return fmt.Errorf("inspect transaction xattrs %s: %w", proof.Path, err)
	}
	xattrs = unsupportedServiceIdentityTransactionXattrs(xattrs)
	if len(xattrs) != 0 {
		return fmt.Errorf("transaction path %s has extended attributes that exact rollback cannot preserve: %s", proof.Path, strings.Join(xattrs, ", "))
	}
	return nil
}

func unsupportedServiceIdentityTransactionXattrs(xattrs []string) []string {
	return unsupportedServiceIdentityTransactionXattrsForOS(runtime.GOOS, xattrs)
}

func unsupportedServiceIdentityTransactionXattrsForOS(goos string, xattrs []string) []string {
	result := slices.Clone(xattrs)
	if goos == "darwin" {
		result = slices.DeleteFunc(result, func(name string) bool {
			// macOS attaches this provenance marker to ordinary files created by
			// local processes. It is system-managed rather than workload state and
			// is reattached to restored files, so it is not part of the rollback
			// payload. All other extended attributes still fail closed.
			return name == "com.apple.provenance"
		})
	}
	return result
}

// serviceIdentityPathStateEqual deliberately ignores device and inode identity.
// Rollback creates a new inode atomically, so a replay must recognize the exact
// content and metadata of an already-restored path without accepting a hardlink
// or any other operator-visible state change.
func serviceIdentityPathStateEqual(actual, expected serviceIdentityPathProof) bool {
	if actual.Present != expected.Present || filepath.Clean(actual.Path) != filepath.Clean(expected.Path) {
		return false
	}
	if !expected.Present {
		return true
	}
	return serviceIdentityPathPayloadEqual(actual, expected)
}

func serviceIdentityPathPayloadEqual(actual, expected serviceIdentityPathProof) bool {
	return actual.Present == expected.Present && actual.Mode == expected.Mode && actual.UID == expected.UID && actual.GID == expected.GID &&
		actual.Nlink == expected.Nlink && actual.Size == expected.Size && actual.SHA256 == expected.SHA256
}

func serviceIdentityStateFromProof(proof serviceIdentityPathProof) serviceIdentityPathState {
	return serviceIdentityPathState{
		Path: proof.Path, Present: proof.Present, Mode: proof.Mode, UID: proof.UID, GID: proof.GID,
		Nlink: proof.Nlink, Size: proof.Size, SHA256: proof.SHA256,
	}
}

func serviceIdentityDesiredFileState(path string, content []byte, mode os.FileMode, uid, gid uint32) serviceIdentityPathState {
	digest := sha256.Sum256(content)
	return serviceIdentityPathState{
		Path: filepath.Clean(path), Present: true, Mode: mode, UID: uid, GID: gid,
		Nlink: 1, Size: int64(len(content)), SHA256: hex.EncodeToString(digest[:]),
	}
}

func validateServiceIdentityPathState(state serviceIdentityPathState, expectedPath string) error {
	expectedPath = filepath.Clean(expectedPath)
	if !filepath.IsAbs(expectedPath) || filepath.Clean(state.Path) != expectedPath {
		return fmt.Errorf("transaction path state %q does not match %q", state.Path, expectedPath)
	}
	if state.Present {
		if !validPresentServiceIdentityPathState(state) {
			return fmt.Errorf("transaction path %s lacks a safe intended regular-file state", expectedPath)
		}
		return nil
	}
	if !emptyAbsentServiceIdentityPathState(state) {
		return fmt.Errorf("absent transaction path %s carries intended file state", expectedPath)
	}
	return nil
}

func validPresentServiceIdentityPathState(state serviceIdentityPathState) bool {
	return state.Mode.IsRegular() && state.Mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0 &&
		state.Nlink == 1 && state.Size >= 0 && state.SHA256 != ""
}

func emptyAbsentServiceIdentityPathState(state serviceIdentityPathState) bool {
	return state.Mode == 0 && state.UID == 0 && state.GID == 0 && state.Nlink == 0 && state.Size == 0 && state.SHA256 == ""
}

func serviceIdentityPathMatchesState(proof serviceIdentityPathProof, state serviceIdentityPathState) bool {
	return filepath.Clean(proof.Path) == filepath.Clean(state.Path) && proof.Present == state.Present &&
		(!state.Present || proof.Mode == state.Mode && proof.UID == state.UID && proof.GID == state.GID &&
			proof.Nlink == state.Nlink && proof.Size == state.Size && proof.SHA256 == state.SHA256)
}

func serviceIdentityStateForPath(states []serviceIdentityPathState, path string) (serviceIdentityPathState, bool) {
	path = filepath.Clean(path)
	for _, state := range states {
		if filepath.Clean(state.Path) == path {
			return state, true
		}
	}
	return serviceIdentityPathState{}, false
}

func copyServiceIdentityProof(source serviceIdentityPathProof, destination string) (serviceIdentityPathProof, error) {
	input, err := openVerifiedServiceIdentityProof(source)
	if err != nil {
		return serviceIdentityPathProof{}, err
	}
	defer func() { _ = input.Close() }()
	tmpPath, err := stageServiceIdentityProofCopy(input, source, destination)
	if err != nil {
		return serviceIdentityPathProof{}, err
	}
	if err := os.Rename(tmpPath, destination); err != nil {
		_ = os.Remove(tmpPath)
		return serviceIdentityPathProof{}, err
	}
	if err := syncServiceIdentityJournalDirectory(filepath.Dir(destination)); err != nil {
		return serviceIdentityPathProof{}, err
	}
	return validateCopiedServiceIdentityProof(source, destination)
}

func validateServiceIdentityCopySource(source serviceIdentityPathProof) error {
	if !source.Present {
		return fmt.Errorf("cannot copy absent transaction path %s", source.Path)
	}
	if source.Nlink != 1 {
		return fmt.Errorf("transaction path %s has %d hard links; exact copy restoration requires one", source.Path, source.Nlink)
	}
	return nil
}

func openVerifiedServiceIdentityProof(source serviceIdentityPathProof) (*os.File, error) {
	if err := validateServiceIdentityCopySource(source); err != nil {
		return nil, err
	}
	input, err := os.OpenFile(source.Path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	if err := validateOpenServiceIdentityProof(input, source); err != nil {
		_ = input.Close()
		return nil, err
	}
	return input, nil
}

func validateOpenServiceIdentityProof(input *os.File, source serviceIdentityPathProof) error {
	actual, err := captureServiceIdentityOpenFileProof(input, source.Path)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, source) {
		return fmt.Errorf("transaction path %s changed from its durable provenance", source.Path)
	}
	xattrs, err := listServiceIdentityOpenFileXattrs(input)
	if err != nil {
		return fmt.Errorf("inspect open transaction xattrs %s: %w", source.Path, err)
	}
	if xattrs = unsupportedServiceIdentityTransactionXattrs(xattrs); len(xattrs) != 0 {
		return fmt.Errorf("transaction path %s has extended attributes that exact rollback cannot preserve", source.Path)
	}
	return nil
}

func stageServiceIdentityProofCopy(input *os.File, source serviceIdentityPathProof, destination string) (path string, err error) {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), "."+filepath.Base(destination)+".yeet-identity-")
	if err != nil {
		return "", err
	}
	path = tmp.Name()
	defer func() {
		err = errors.Join(err, tmp.Close())
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	if _, err = io.Copy(tmp, input); err == nil {
		err = applyServiceIdentityProofMetadata(tmp, source)
	}
	return path, err
}

func applyServiceIdentityProofMetadata(file *os.File, source serviceIdentityPathProof) error {
	if err := file.Chmod(source.Mode.Perm()); err != nil {
		return err
	}
	if err := file.Chown(int(source.UID), int(source.GID)); err != nil {
		return err
	}
	return file.Sync()
}

func validateCopiedServiceIdentityProof(source serviceIdentityPathProof, destination string) (serviceIdentityPathProof, error) {
	proof, err := captureServiceIdentityPathProof(destination)
	if err != nil {
		return serviceIdentityPathProof{}, err
	}
	if proof.Nlink != 1 {
		return serviceIdentityPathProof{}, fmt.Errorf("transaction backup %s has %d hard links, want one", destination, proof.Nlink)
	}
	if err := validateServiceIdentityTransactionPath(proof); err != nil {
		return serviceIdentityPathProof{}, err
	}
	if !serviceIdentityPathPayloadEqual(proof, source) {
		return serviceIdentityPathProof{}, fmt.Errorf("transaction backup %s does not match source content and metadata", destination)
	}
	return proof, nil
}

func copyServiceIdentityProofAt(
	sourceRoot, sourceRel string,
	source serviceIdentityPathProof,
	destinationRoot, destinationRel string,
	expectedDestination serviceIdentityPathProof,
) (serviceIdentityPathProof, error) {
	input, err := openVerifiedServiceIdentityProofAt(sourceRoot, sourceRel, source)
	if err != nil {
		return serviceIdentityPathProof{}, err
	}
	defer func() { _ = input.Close() }()
	destinationParent, destinationName, closeDestinationParent, err := openServiceIdentityMutationParent(destinationRoot, destinationRel)
	if err != nil {
		return serviceIdentityPathProof{}, err
	}
	defer closeDestinationParent()
	return publishServiceIdentityProofAt(destinationParent, destinationName, input, source, expectedDestination)
}

func publishServiceIdentityProofAt(destinationParent int, destinationName string, input *os.File, source, expectedDestination serviceIdentityPathProof) (serviceIdentityPathProof, error) {
	if err := validateServiceIdentityDestinationProof(destinationParent, destinationName, expectedDestination); err != nil {
		return serviceIdentityPathProof{}, err
	}
	tmpID, err := newServiceIdentityMigrationID()
	if err != nil {
		return serviceIdentityPathProof{}, fmt.Errorf("name transaction temporary file beside %s: %w", expectedDestination.Path, err)
	}
	tmpName := "." + destinationName + ".yeet-identity-" + tmpID
	tmpProof, err := stageServiceIdentityProofCopyAt(destinationParent, tmpName, input, source, expectedDestination.Path)
	if err != nil {
		return serviceIdentityPathProof{}, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = unix.Unlinkat(destinationParent, tmpName, 0)
		}
	}()
	if err := validateServiceIdentityDestinationProof(destinationParent, destinationName, expectedDestination); err != nil {
		return serviceIdentityPathProof{}, err
	}
	if err := unix.Renameat(destinationParent, tmpName, destinationParent, destinationName); err != nil {
		return serviceIdentityPathProof{}, fmt.Errorf("publish transaction file %s: %w", expectedDestination.Path, err)
	}
	cleanup = false
	if err := unix.Fsync(destinationParent); err != nil {
		return serviceIdentityPathProof{}, fmt.Errorf("sync transaction parent for %s: %w", expectedDestination.Path, err)
	}
	proof, err := captureServiceIdentityTransactionProofFromParent(destinationParent, destinationName, expectedDestination.Path)
	if err != nil {
		return serviceIdentityPathProof{}, err
	}
	if !reflect.DeepEqual(proof, tmpProof) {
		return serviceIdentityPathProof{}, fmt.Errorf("transaction destination %s changed while being published", expectedDestination.Path)
	}
	return proof, nil
}

func openVerifiedServiceIdentityProofAt(root, rel string, source serviceIdentityPathProof) (*os.File, error) {
	if err := validateServiceIdentityCopySource(source); err != nil {
		return nil, err
	}
	parent, name, closeParent, err := openServiceIdentityMutationParent(root, rel)
	if err != nil {
		return nil, err
	}
	defer closeParent()
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open transaction source %s relative to stable parent: %w", source.Path, err)
	}
	input := os.NewFile(uintptr(fd), source.Path)
	if input == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("wrap transaction source %s", source.Path)
	}
	if err := validateOpenServiceIdentityProof(input, source); err != nil {
		_ = input.Close()
		return nil, err
	}
	return input, nil
}

func validateServiceIdentityDestinationProof(parent int, name string, expected serviceIdentityPathProof) error {
	actual, err := captureServiceIdentityPathProofFromParent(parent, name, expected.Path)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, expected) {
		return fmt.Errorf("transaction destination %s changed from its durable provenance", expected.Path)
	}
	return nil
}

func stageServiceIdentityProofCopyAt(parent int, name string, input *os.File, source serviceIdentityPathProof, path string) (proof serviceIdentityPathProof, err error) {
	fd, err := unix.Openat(parent, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return proof, fmt.Errorf("create transaction temporary file beside %s: %w", path, err)
	}
	tmp := os.NewFile(uintptr(fd), filepath.Join(filepath.Dir(path), name))
	if tmp == nil {
		_ = unix.Close(fd)
		_ = unix.Unlinkat(parent, name, 0)
		return proof, fmt.Errorf("wrap transaction temporary file for %s", path)
	}
	defer func() {
		err = errors.Join(err, tmp.Close())
		if err != nil {
			_ = unix.Unlinkat(parent, name, 0)
		}
	}()
	if _, err = io.Copy(tmp, input); err == nil {
		err = applyServiceIdentityProofMetadata(tmp, source)
	}
	if err != nil {
		return proof, err
	}
	return captureServiceIdentityTransactionProofFromParent(parent, name, path)
}

func removeServiceIdentityProofAt(root, rel string, proof serviceIdentityPathProof) error {
	parentFD, name, closeParent, err := openServiceIdentityMutationParent(root, rel)
	if err != nil {
		return err
	}
	defer closeParent()
	actual, err := captureServiceIdentityPathProofFromParent(parentFD, name, proof.Path)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, proof) {
		return fmt.Errorf("transaction path %s changed from its durable provenance", proof.Path)
	}
	if err := unix.Unlinkat(parentFD, name, 0); err != nil {
		return err
	}
	if err := unix.Fsync(parentFD); err != nil {
		return fmt.Errorf("sync transaction parent after removing %s: %w", proof.Path, err)
	}
	return nil
}

func listServiceIdentityOpenFileXattrs(file *os.File) ([]string, error) {
	return listServiceIdentityOpenFDXattrs(int(file.Fd()))
}

func listServiceIdentityOpenFDXattrs(fd int) ([]string, error) {
	size, err := unix.Flistxattr(fd, nil)
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
	n, err := unix.Flistxattr(fd, buf)
	if err != nil {
		return nil, err
	}
	return parseServiceIdentityXattrBuffer(buf, n), nil
}
