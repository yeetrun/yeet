// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package copyutil

import (
	"archive/tar"
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	copyHeaderPrefix = "YEETCOPY1"
	copyHeaderEmpty  = "-"
)

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
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlinks not supported: %s", p)
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		if prefix != "" {
			name = path.Join(prefix, name)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
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
		if d.IsDir() {
			return nil
		}
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
	})
}

// ExtractTar extracts a tar archive stream into dest.
// It rejects absolute or parent-traversal paths.
func ExtractTar(r io.Reader, dest string) error {
	tr := tar.NewReader(r)
	dest = filepath.Clean(dest)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
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
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(hdr.Mode).Perm()
			if mode == 0 {
				mode = 0o644
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
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
		default:
			return fmt.Errorf("unsupported tar entry %q", hdr.Name)
		}
	}
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
