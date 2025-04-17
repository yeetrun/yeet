// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fileutil

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// CopyFile copies a file from src to dst. It is able to overwrite existing
// files that are in use. It does this by writing to a temporary file and then
// moving it into place.
func CopyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer srcFile.Close()

	srcStat, err := srcFile.Stat()
	if err != nil {
		return err
	}

	// We write to a temporary file and then move it into place to avoid issues
	// with the destination file already existing / being in use.
	tempDst := dst + ".tmp"
	dstFile, err := os.OpenFile(tempDst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, srcStat.Mode())
	if err != nil {
		return err
	}
	defer func() {
		dstFile.Close()
		if err == nil {
			err = os.Rename(tempDst, dst)
		}
		if err != nil {
			os.Remove(tempDst)
		}
	}()
	if err != nil {
		return err
	}
	defer dstFile.Close()

	if _, err = io.Copy(dstFile, srcFile); err != nil {
		return err
	}

	return dstFile.Sync()
}

// version returns a version string based on the current time.
func Version() string {
	return time.Now().Format("20060102150405")
}

var removeVersionRe = regexp.MustCompile(`[-.](\d+(\.\d+)?)(\.[^.]+)?$`)

// RemoveVersion removes the version part from the given filename.
func RemoveVersion(filename string) string {
	// This regex matches a hyphen or dot followed by digits, potentially
	// another dot, and more digits It's designed to match patterns like
	// "-20230405", ".20230405"
	return removeVersionRe.ReplaceAllString(filename, "$3")
}

// UpdateVersion updates the version of the given filename.
func UpdateVersion(filename string) string {
	dir := filepath.Dir(filename)
	base := filepath.Base(filename)
	name, ext, _ := strings.Cut(base, ".")
	name = RemoveVersion(name)
	return filepath.Join(dir, name+"-"+Version()+"."+ext)
}

func ApplyVersion(path string) string {
	b, a, _ := strings.Cut(path, ".")
	return b + "-" + Version() + "." + a
}

// Identical reports whether the contents of two files are identical.
func Identical(file1, file2 string) (bool, error) {
	f1, err := os.Open(file1)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to open file1: %w", err)
	}
	defer f1.Close()

	f2, err := os.Open(file2)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to open file2: %w", err)
	}
	defer f2.Close()

	hasher1 := sha256.New()
	hasher2 := sha256.New()
	if _, err := io.Copy(hasher1, f1); err != nil {
		return false, fmt.Errorf("failed to hash file1: %v", err)
	}
	if _, err := io.Copy(hasher2, f2); err != nil {
		return false, fmt.Errorf("failed to hash file2: %v", err)
	}

	return bytes.Equal(hasher1.Sum(nil), hasher2.Sum(nil)), nil
}
