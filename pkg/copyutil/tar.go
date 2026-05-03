// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package copyutil

import (
	"archive/tar"
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	copyHeaderPrefix = "YEETCOPY1"
	copyHeaderEmpty  = "-"
)

type TarEntry struct {
	Name     string
	Size     int64
	Mode     fs.FileMode
	Type     byte
	Linkname string
	ModTime  time.Time
}

type TarObserver func(entry TarEntry)

// WriteHeader writes a copy stream header that precedes payload data.
func WriteHeader(w io.Writer, kind, base string) error {
	if kind == "" {
		return fmt.Errorf("copy header kind required")
	}
	enc := copyHeaderEmpty
	if base != "" {
		enc = base64.StdEncoding.EncodeToString([]byte(base))
	}
	_, err := fmt.Fprintf(w, "%s %s %s\n", copyHeaderPrefix, kind, enc)
	return err
}

// ReadHeader reads a copy stream header and returns kind and base name.
func ReadHeader(r *bufio.Reader) (string, string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", "", err
	}
	return parseCopyHeaderLine(line)
}

func parseCopyHeaderLine(line string) (string, string, error) {
	parts := strings.Fields(strings.TrimSpace(line))
	if !isValidCopyHeaderParts(parts) {
		return "", "", fmt.Errorf("invalid copy header")
	}
	kind := parts[1]
	base, err := decodeCopyHeaderBase(parts[2])
	if err != nil {
		return "", "", err
	}
	return kind, base, nil
}

func isValidCopyHeaderParts(parts []string) bool {
	return len(parts) >= 3 && parts[0] == copyHeaderPrefix
}

func decodeCopyHeaderBase(enc string) (string, error) {
	if enc == copyHeaderEmpty {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", fmt.Errorf("invalid copy header")
	}
	return string(raw), nil
}

// TarDirectory writes a tar archive of src into w.
// Entries are relative to src, with an optional prefix applied.
func TarDirectory(w io.Writer, src string, prefix string) error {
	return TarDirectoryWithObserver(w, src, prefix, nil)
}

// TarDirectoryWithObserver writes a tar archive of src into w, invoking observer for each entry.
func TarDirectoryWithObserver(w io.Writer, src string, prefix string, observer TarObserver) (err error) {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("expected directory, got %q", src)
	}
	tw := tar.NewWriter(w)
	defer closeTarWriter(tw, &err)

	src = filepath.Clean(src)
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		return writeDirectoryTarEntry(tw, src, prefix, p, d, err, observer)
	})
}

// TarFile writes a single file or symlink entry to w with the provided name.
func TarFile(w io.Writer, src string, name string) error {
	return TarFileWithObserver(w, src, name, nil)
}

// TarFileWithObserver writes a single file or symlink entry to w, invoking observer.
func TarFileWithObserver(w io.Writer, src string, name string, observer TarObserver) (err error) {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("expected file, got directory %q", src)
	}
	if name == "" {
		name = filepath.Base(src)
	}
	tw := tar.NewWriter(w)
	defer closeTarWriter(tw, &err)

	hdr, err := tarHeaderForPath(src, info)
	if err != nil {
		return err
	}
	hdr.Name = filepath.ToSlash(name)
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if observer != nil {
		observer(tarEntryFromHeader(hdr))
	}
	if info.Mode().IsRegular() {
		if err := copyFileToTar(tw, src); err != nil {
			return err
		}
	}
	return nil
}

// ExtractTar extracts a tar archive stream into dest.
// It rejects absolute or parent-traversal paths.
func ExtractTar(r io.Reader, dest string) error {
	return ExtractTarWithOptions(r, dest, ExtractOptions{})
}

type ExtractOptions struct {
	OnEntry TarObserver
}

// ExtractTarWithOptions extracts a tar archive stream into dest with optional callbacks.
func ExtractTarWithOptions(r io.Reader, dest string, opts ExtractOptions) error {
	tr := tar.NewReader(r)
	dest = filepath.Clean(dest)
	var dirs []dirMeta
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		target, ok, err := tarTargetPath(dest, hdr.Name)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		notifyTarObserver(opts.OnEntry, hdr)
		dir, err := extractTarEntry(tr, dest, target, hdr)
		if err != nil {
			return err
		}
		if dir != nil {
			dirs = append(dirs, *dir)
		}
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := applyHeaderMetadata(dirs[i].path, dirs[i].hdr, false); err != nil {
			return err
		}
	}
	return nil
}

type dirMeta struct {
	path string
	hdr  *tar.Header
}

func closeTarWriter(tw *tar.Writer, errp *error) {
	if err := tw.Close(); err != nil && *errp == nil {
		*errp = err
	}
}

func writeDirectoryTarEntry(tw *tar.Writer, src, prefix, filePath string, d fs.DirEntry, walkErr error, observer TarObserver) error {
	if walkErr != nil {
		return walkErr
	}
	if filePath == src {
		return nil
	}
	info, err := os.Lstat(filePath)
	if err != nil {
		return err
	}
	hdr, err := directoryTarHeader(src, prefix, filePath, d, info)
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	notifyTarObserver(observer, hdr)
	if d.IsDir() || !info.Mode().IsRegular() {
		return nil
	}
	return copyFileToTar(tw, filePath)
}

func directoryTarHeader(src, prefix, filePath string, d fs.DirEntry, info fs.FileInfo) (*tar.Header, error) {
	rel, err := filepath.Rel(src, filePath)
	if err != nil {
		return nil, err
	}
	hdr, err := tarHeaderForPath(filePath, info)
	if err != nil {
		return nil, err
	}
	hdr.Name = filepath.ToSlash(rel)
	if prefix != "" {
		hdr.Name = path.Join(prefix, hdr.Name)
	}
	if d.IsDir() && !strings.HasSuffix(hdr.Name, "/") {
		hdr.Name += "/"
	}
	return hdr, nil
}

func tarHeaderForPath(filePath string, info fs.FileInfo) (*tar.Header, error) {
	var linkname string
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(filePath)
		if err != nil {
			return nil, err
		}
		linkname = target
	}
	return tar.FileInfoHeader(info, linkname)
}

func copyFileToTar(tw *tar.Writer, filePath string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(tw, f); err != nil {
		closeErr := f.Close()
		if closeErr != nil {
			return errors.Join(err, closeErr)
		}
		return err
	}
	return f.Close()
}

func notifyTarObserver(observer TarObserver, hdr *tar.Header) {
	if observer != nil {
		observer(tarEntryFromHeader(hdr))
	}
}

func tarTargetPath(dest, name string) (string, bool, error) {
	cleanName := path.Clean(name)
	if cleanName == "." || cleanName == "" {
		return "", false, nil
	}
	if path.IsAbs(cleanName) || cleanName == ".." || strings.HasPrefix(cleanName, "../") {
		return "", false, fmt.Errorf("invalid tar entry %q", name)
	}
	target := filepath.Join(dest, filepath.FromSlash(cleanName))
	if !isSubpath(dest, target) {
		return "", false, fmt.Errorf("invalid tar entry %q", name)
	}
	return target, true, nil
}

func extractTarEntry(tr *tar.Reader, dest, target string, hdr *tar.Header) (*dirMeta, error) {
	switch hdr.Typeflag {
	case tar.TypeDir:
		return extractDirectory(target, hdr)
	case tar.TypeReg, 0:
		return nil, extractRegularFile(tr, target, hdr)
	case tar.TypeSymlink:
		return nil, extractSymlink(target, hdr)
	case tar.TypeLink:
		return nil, extractHardlink(dest, target, hdr)
	case tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
		return nil, extractSpecial(target, hdr)
	default:
		return nil, fmt.Errorf("unsupported tar entry %q", hdr.Name)
	}
}

func extractDirectory(target string, hdr *tar.Header) (*dirMeta, error) {
	mode := fileModeForHeader(hdr, 0o755)
	if err := os.MkdirAll(target, mode); err != nil {
		return nil, err
	}
	return &dirMeta{path: target, hdr: hdr}, nil
}

func extractRegularFile(tr *tar.Reader, target string, hdr *tar.Header) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	mode := fileModeForHeader(hdr, 0o644)
	f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, tr); err != nil {
		closeErr := f.Close()
		if closeErr != nil {
			return errors.Join(err, closeErr)
		}
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return applyHeaderMetadata(target, hdr, false)
}

func extractSymlink(target string, hdr *tar.Header) error {
	if err := prepareReplacement(target); err != nil {
		return err
	}
	if err := os.Symlink(hdr.Linkname, target); err != nil {
		return err
	}
	return applyHeaderMetadata(target, hdr, true)
}

func extractHardlink(dest, target string, hdr *tar.Header) error {
	linkTarget := filepath.Join(dest, filepath.FromSlash(path.Clean(hdr.Linkname)))
	if !isSubpath(dest, linkTarget) {
		return fmt.Errorf("invalid tar entry %q", hdr.Name)
	}
	if err := prepareReplacement(target); err != nil {
		return err
	}
	return os.Link(linkTarget, target)
}

func extractSpecial(target string, hdr *tar.Header) error {
	if err := prepareReplacement(target); err != nil {
		return err
	}
	if err := createSpecial(target, hdr); err != nil {
		return err
	}
	return applyHeaderMetadata(target, hdr, false)
}

func prepareReplacement(target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := os.RemoveAll(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func isSubpath(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func tarEntryFromHeader(hdr *tar.Header) TarEntry {
	return TarEntry{
		Name:     strings.TrimSuffix(hdr.Name, "/"),
		Size:     hdr.Size,
		Mode:     os.FileMode(hdr.Mode),
		Type:     hdr.Typeflag,
		Linkname: hdr.Linkname,
		ModTime:  hdr.ModTime,
	}
}

func fileModeForHeader(hdr *tar.Header, defaultMode os.FileMode) os.FileMode {
	mode := os.FileMode(hdr.Mode)
	if mode == 0 {
		mode = defaultMode
	}
	return mode
}

func applyHeaderMetadata(path string, hdr *tar.Header, isSymlink bool) error {
	mode := os.FileMode(hdr.Mode)
	if !isSymlink {
		if err := os.Chmod(path, mode); err != nil && !shouldIgnorePermError(err) {
			return err
		}
		if !hdr.ModTime.IsZero() {
			if err := os.Chtimes(path, hdr.ModTime, hdr.ModTime); err != nil && !shouldIgnoreTimeError(err) {
				return err
			}
		}
	}
	return nil
}

func applyFileInfoMetadata(path string, info fs.FileInfo, isSymlink bool) error {
	if info == nil {
		return nil
	}
	mode := info.Mode()
	if !isSymlink {
		if err := os.Chmod(path, mode); err != nil && !shouldIgnorePermError(err) {
			return err
		}
		if mt := info.ModTime(); !mt.IsZero() {
			if err := os.Chtimes(path, mt, mt); err != nil && !shouldIgnoreTimeError(err) {
				return err
			}
		}
	}
	return nil
}

func shouldIgnorePermError(err error) bool {
	return errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.ENOTSUP)
}

func shouldIgnoreTimeError(err error) bool {
	return errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.ENOTSUP)
}
