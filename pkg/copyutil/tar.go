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
	line = strings.TrimSpace(line)
	parts := strings.Fields(line)
	if len(parts) < 3 || parts[0] != copyHeaderPrefix {
		return "", "", fmt.Errorf("invalid copy header")
	}
	kind := parts[1]
	enc := parts[2]
	if enc == copyHeaderEmpty {
		return kind, "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", "", fmt.Errorf("invalid copy header")
	}
	return kind, string(raw), nil
}

// TarDirectory writes a tar archive of src into w.
// Entries are relative to src, with an optional prefix applied.
func TarDirectory(w io.Writer, src string, prefix string) error {
	return TarDirectoryWithObserver(w, src, prefix, nil)
}

// TarDirectoryWithObserver writes a tar archive of src into w, invoking observer for each entry.
func TarDirectoryWithObserver(w io.Writer, src string, prefix string, observer TarObserver) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("expected directory, got %q", src)
	}
	tw := tar.NewWriter(w)
	defer tw.Close()

	src = filepath.Clean(src)
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == src {
			return nil
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		if prefix != "" {
			name = path.Join(prefix, name)
		}
		var linkname string
		if info.Mode()&os.ModeSymlink != 0 {
			linkname, err = os.Readlink(p)
			if err != nil {
				return err
			}
		}
		hdr, err := tar.FileInfoHeader(info, linkname)
		if err != nil {
			return err
		}
		hdr.Name = name
		if d.IsDir() && !strings.HasSuffix(hdr.Name, "/") {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if observer != nil {
			observer(tarEntryFromHeader(hdr))
		}
		if d.IsDir() {
			return nil
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			if _, err := io.Copy(tw, f); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
			return nil
		}
		return nil
	})
}

// TarFile writes a single file or symlink entry to w with the provided name.
func TarFile(w io.Writer, src string, name string) error {
	return TarFileWithObserver(w, src, name, nil)
}

// TarFileWithObserver writes a single file or symlink entry to w, invoking observer.
func TarFileWithObserver(w io.Writer, src string, name string, observer TarObserver) error {
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
	defer tw.Close()
	var linkname string
	if info.Mode()&os.ModeSymlink != 0 {
		linkname, err = os.Readlink(src)
		if err != nil {
			return err
		}
	}
	hdr, err := tar.FileInfoHeader(info, linkname)
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
		f, err := os.Open(src)
		if err != nil {
			return err
		}
		if _, err := io.Copy(tw, f); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
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
	type dirMeta struct {
		path string
		hdr  *tar.Header
	}
	var dirs []dirMeta
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := path.Clean(hdr.Name)
		if name == "." || name == "" {
			continue
		}
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("invalid tar entry %q", hdr.Name)
		}
		target := filepath.Join(dest, filepath.FromSlash(name))
		if !isSubpath(dest, target) {
			return fmt.Errorf("invalid tar entry %q", hdr.Name)
		}
		if opts.OnEntry != nil {
			opts.OnEntry(tarEntryFromHeader(hdr))
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			mode := fileModeForHeader(hdr, 0o755)
			if err := os.MkdirAll(target, mode); err != nil {
				return err
			}
			dirs = append(dirs, dirMeta{path: target, hdr: hdr})
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := fileModeForHeader(hdr, 0o644)
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm())
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
			if err := applyHeaderMetadata(target, hdr, false); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.RemoveAll(target); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
			if err := applyHeaderMetadata(target, hdr, true); err != nil {
				return err
			}
		case tar.TypeLink:
			linkTarget := filepath.Join(dest, filepath.FromSlash(path.Clean(hdr.Linkname)))
			if !isSubpath(dest, linkTarget) {
				return fmt.Errorf("invalid tar entry %q", hdr.Name)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.RemoveAll(target); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := os.Link(linkTarget, target); err != nil {
				return err
			}
		case tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.RemoveAll(target); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := createSpecial(target, hdr); err != nil {
				return err
			}
			if err := applyHeaderMetadata(target, hdr, false); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported tar entry %q", hdr.Name)
		}
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := applyHeaderMetadata(dirs[i].path, dirs[i].hdr, false); err != nil {
			return err
		}
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
