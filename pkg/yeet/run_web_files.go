// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type runWebFileList struct {
	Dir     string            `json:"dir"`
	Entries []runWebFileEntry `json:"entries"`
}

type runWebFileEntry struct {
	Name          string `json:"name"`
	Path          string `json:"path"`
	Dir           bool   `json:"dir"`
	LikelyPayload bool   `json:"likelyPayload,omitempty"`
	LikelyEnv     bool   `json:"likelyEnv,omitempty"`
}

func (l runWebFileList) entry(name string) *runWebFileEntry {
	for i := range l.Entries {
		if l.Entries[i].Name == name {
			return &l.Entries[i]
		}
	}
	return nil
}

func listRunWebFiles(root, rel string) (runWebFileList, error) {
	rootReal, rel, targetReal, err := resolveRunWebListTarget(root, rel)
	if err != nil {
		return runWebFileList{}, err
	}

	entries, err := os.ReadDir(targetReal)
	if err != nil {
		return runWebFileList{}, err
	}

	out := runWebFileList{Dir: rel}
	for _, entry := range entries {
		name := entry.Name()
		entryPath := filepath.ToSlash(filepath.Join(rel, name))
		if rel == "." {
			entryPath = name
		}
		fileEntry, err := newRunWebFileEntry(rootReal, targetReal, entryPath, name)
		if err != nil {
			return runWebFileList{}, err
		}
		out.Entries = append(out.Entries, fileEntry)
	}

	sort.Slice(out.Entries, func(i, j int) bool {
		if out.Entries[i].Dir != out.Entries[j].Dir {
			return out.Entries[i].Dir
		}
		return out.Entries[i].Name < out.Entries[j].Name
	})

	return out, nil
}

func resolveRunWebListTarget(root, rel string) (string, string, string, error) {
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", "", err
	}
	rootReal = filepath.Clean(rootReal)

	rel, err = cleanRunWebRel(rel)
	if err != nil {
		return "", "", "", err
	}

	targetReal, err := filepath.EvalSymlinks(filepath.Join(rootReal, rel))
	if err != nil {
		return "", "", "", err
	}
	targetReal = filepath.Clean(targetReal)
	if !runWebPathInsideRoot(rootReal, targetReal) {
		return "", "", "", fmt.Errorf("path escapes project root")
	}
	return rootReal, rel, targetReal, nil
}

func newRunWebFileEntry(rootReal, targetReal, entryPath, name string) (runWebFileEntry, error) {
	entryReal, err := filepath.EvalSymlinks(filepath.Join(targetReal, name))
	if err != nil {
		return runWebFileEntry{}, err
	}
	entryReal = filepath.Clean(entryReal)
	if !runWebPathInsideRoot(rootReal, entryReal) {
		return runWebFileEntry{}, fmt.Errorf("path escapes project root")
	}
	info, err := os.Stat(entryReal)
	if err != nil {
		return runWebFileEntry{}, err
	}
	return runWebFileEntry{
		Name:          name,
		Path:          entryPath,
		Dir:           info.IsDir(),
		LikelyPayload: runWebLikelyPayload(name, info.Mode()),
		LikelyEnv:     runWebLikelyEnv(name),
	}, nil
}

func cleanRunWebRel(rel string) (string, error) {
	rel = filepath.Clean(strings.TrimSpace(rel))
	if rel == "" || rel == "." {
		return ".", nil
	}
	if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes project root")
	}
	return rel, nil
}

func runWebPathInsideRoot(rootReal, targetReal string) bool {
	return targetReal == rootReal || strings.HasPrefix(targetReal, rootReal+string(filepath.Separator))
}

func runWebLikelyPayload(name string, mode os.FileMode) bool {
	base := strings.ToLower(name)
	if base == "compose.yml" || base == "compose.yaml" || base == "docker-compose.yml" || base == "docker-compose.yaml" || name == "Dockerfile" {
		return true
	}
	return mode.IsRegular() && mode&0o111 != 0
}

func runWebLikelyEnv(name string) bool {
	base := strings.ToLower(name)
	return base == ".env" || strings.HasSuffix(base, ".env") || strings.Contains(base, ".env.")
}
