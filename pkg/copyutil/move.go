// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package copyutil

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// MoveTree moves src into dst, merging into dst when it already exists.
// It attempts to use renames for atomic moves where possible.
func MoveTree(src, dst string) error {
	if src == "" || dst == "" || src == dst {
		return nil
	}
	if _, err := os.Lstat(dst); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(src, dst); err == nil {
			return nil
		}
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return moveTreeContents(src, dst, false)
}

func moveTreeContents(src, dst string, applyMeta bool) error {
	if st, err := os.Lstat(dst); err == nil && !st.IsDir() {
		if err := os.RemoveAll(dst); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	srcInfo, _ := os.Lstat(src)
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			if err := moveTreeContents(srcPath, dstPath, true); err != nil {
				return err
			}
			continue
		}
		if err := replacePath(srcPath, dstPath, info); err != nil {
			return err
		}
	}
	if applyMeta && srcInfo != nil {
		if err := applyFileInfoMetadata(dst, srcInfo, false); err != nil {
			return err
		}
	}
	return os.Remove(src)
}

func replacePath(src, dst string, info fs.FileInfo) error {
	if err := os.RemoveAll(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		return err
	}
	return applyFileInfoMetadata(dst, info, info.Mode()&os.ModeSymlink != 0)
}
