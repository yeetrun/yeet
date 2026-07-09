// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const updateCheckCacheTTL = 24 * time.Hour

var updateCheckCacheFile = filepath.Join(os.Getenv("HOME"), ".yeet", "update-check.json")

type updateCheckCache struct {
	LatestStable  releaseCacheEntry           `json:"latestStable,omitempty"`
	LatestNightly releaseCacheEntry           `json:"latestNightly,omitempty"`
	CatchHosts    map[string]catchObservation `json:"catchHosts,omitempty"`
	LastAdvisory  map[string]time.Time        `json:"lastAdvisory,omitempty"`
}

type releaseCacheEntry struct {
	Tag         string        `json:"tag,omitempty"`
	PublishedAt string        `json:"publishedAt,omitempty"`
	CheckedAt   time.Time     `json:"checkedAt,omitempty"`
	Assets      []githubAsset `json:"assets,omitempty"`
	Nightly     bool          `json:"nightly,omitempty"`
}

type catchObservation struct {
	Version    string    `json:"version,omitempty"`
	ObservedAt time.Time `json:"observedAt,omitempty"`
	Error      string    `json:"error,omitempty"`
}

func (e releaseCacheEntry) fresh(now time.Time, ttl time.Duration) bool {
	if e.Tag == "" || e.CheckedAt.IsZero() || e.CheckedAt.After(now) {
		return false
	}
	return now.Sub(e.CheckedAt) < ttl
}

func newUpdateCheckCache() updateCheckCache {
	return updateCheckCache{
		CatchHosts:   map[string]catchObservation{},
		LastAdvisory: map[string]time.Time{},
	}
}

func readUpdateCheckCache(path string) updateCheckCache {
	raw, err := os.ReadFile(path)
	if err != nil {
		return newUpdateCheckCache()
	}

	var cache updateCheckCache
	if err := json.Unmarshal(raw, &cache); err != nil {
		return newUpdateCheckCache()
	}
	cache.initMaps()
	return cache
}

func writeUpdateCheckCache(path string, cache updateCheckCache) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	raw, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')

	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	removeTmp = false
	return nil
}

func (c *updateCheckCache) initMaps() {
	if c.CatchHosts == nil {
		c.CatchHosts = map[string]catchObservation{}
	}
	if c.LastAdvisory == nil {
		c.LastAdvisory = map[string]time.Time{}
	}
}
