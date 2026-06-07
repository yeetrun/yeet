// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUpdateCheckCacheFileDefaultsUnderYeetHome(t *testing.T) {
	if !strings.HasSuffix(updateCheckCacheFile, filepath.Join(".yeet", "update-check.json")) {
		t.Fatalf("updateCheckCacheFile = %q, want path under .yeet/update-check.json", updateCheckCacheFile)
	}
}

func TestUpdateCheckCacheRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "update-check.json")
	publishedAt := "2026-06-07T12:00:00Z"
	cache := updateCheckCache{
		LatestStable: releaseCacheEntry{
			Tag:         "v0.5.13",
			PublishedAt: publishedAt,
			CheckedAt:   time.Unix(10, 0).UTC(),
			Assets: []githubAsset{
				{Name: "yeet-linux-amd64.tar.gz", BrowserDownloadURL: "https://example.com/yeet.tgz"},
			},
		},
		LatestNightly: releaseCacheEntry{Tag: "nightly", CheckedAt: time.Unix(11, 0).UTC()},
		CatchHosts: map[string]catchObservation{
			"edge-a": {Version: "v0.5.12", ObservedAt: time.Unix(12, 0).UTC()},
			"edge-b": {Error: "connection refused", ObservedAt: time.Unix(13, 0).UTC()},
		},
		LastAdvisory: map[string]time.Time{"v0.5.13": time.Unix(14, 0).UTC()},
	}

	if err := writeUpdateCheckCache(path, cache); err != nil {
		t.Fatalf("write cache: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat cache: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("cache mode = %v, want 0600", got)
	}

	got := readUpdateCheckCache(path)
	if got.LatestStable.Tag != "v0.5.13" ||
		got.LatestStable.PublishedAt != publishedAt ||
		got.LatestStable.Assets[0].Name != "yeet-linux-amd64.tar.gz" ||
		got.LatestNightly.Tag != "nightly" ||
		got.CatchHosts["edge-a"].Version != "v0.5.12" ||
		got.CatchHosts["edge-b"].Error != "connection refused" ||
		!got.LastAdvisory["v0.5.13"].Equal(time.Unix(14, 0).UTC()) {
		t.Fatalf("cache round trip = %#v", got)
	}
}

func TestWriteUpdateCheckCacheIgnoresStaleFixedTempAndWritesPrivateMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-check.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte("stale"), 0o644); err != nil {
		t.Fatalf("write stale temp: %v", err)
	}

	cache := updateCheckCache{
		LatestStable: releaseCacheEntry{Tag: "v0.5.13", CheckedAt: time.Unix(10, 0).UTC()},
	}
	if err := writeUpdateCheckCache(path, cache); err != nil {
		t.Fatalf("write cache: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat cache: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("cache mode = %v, want 0600", got)
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Fatalf("stale fixed temp should be left alone, stat err: %v", err)
	}
}

func TestWriteUpdateCheckCacheConcurrentWritersLeaveReadableCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-check.json")
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < cap(errs); i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cache := updateCheckCache{
				LatestStable: releaseCacheEntry{
					Tag:       "v0.5.13",
					CheckedAt: time.Unix(int64(i), 0).UTC(),
				},
			}
			errs <- writeUpdateCheckCache(path, cache)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent write error: %v", err)
		}
	}

	got := readUpdateCheckCache(path)
	if got.LatestStable.Tag == "" {
		t.Fatalf("final cache missing latest stable: %#v", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat cache: %v", err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o600 {
		t.Fatalf("cache mode = %v, want 0600", gotMode)
	}
}

func TestReadUpdateCheckCacheIgnoresCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-check.json")
	if err := os.WriteFile(path, []byte("{bad"), 0o600); err != nil {
		t.Fatalf("write corrupt cache: %v", err)
	}

	got := readUpdateCheckCache(path)
	if got.LatestStable.Tag != "" || len(got.CatchHosts) != 0 || len(got.LastAdvisory) != 0 {
		t.Fatalf("corrupt cache = %#v, want empty", got)
	}
	if got.CatchHosts == nil {
		t.Fatal("corrupt cache returned nil CatchHosts")
	}
	if got.LastAdvisory == nil {
		t.Fatal("corrupt cache returned nil LastAdvisory")
	}
}

func TestReadUpdateCheckCacheInitializesMissingAndNilMaps(t *testing.T) {
	missing := readUpdateCheckCache(filepath.Join(t.TempDir(), "missing.json"))
	if missing.CatchHosts == nil {
		t.Fatal("missing cache returned nil CatchHosts")
	}
	if missing.LastAdvisory == nil {
		t.Fatal("missing cache returned nil LastAdvisory")
	}

	path := filepath.Join(t.TempDir(), "nil-maps.json")
	if err := os.WriteFile(path, []byte(`{"latestStable":{"tag":"v0.5.13"}}`), 0o600); err != nil {
		t.Fatalf("write nil map cache: %v", err)
	}

	got := readUpdateCheckCache(path)
	if got.LatestStable.Tag != "v0.5.13" {
		t.Fatalf("latest stable tag = %q, want v0.5.13", got.LatestStable.Tag)
	}
	if got.CatchHosts == nil {
		t.Fatal("nil map cache returned nil CatchHosts")
	}
	if got.LastAdvisory == nil {
		t.Fatal("nil map cache returned nil LastAdvisory")
	}
}

func TestCacheReleaseFreshness(t *testing.T) {
	now := time.Unix(1000, 0)
	tests := []struct {
		name  string
		entry releaseCacheEntry
		want  bool
	}{
		{
			name:  "fresh",
			entry: releaseCacheEntry{Tag: "v0.5.13", CheckedAt: now.Add(-time.Hour)},
			want:  true,
		},
		{
			name:  "stale",
			entry: releaseCacheEntry{Tag: "v0.5.12", CheckedAt: now.Add(-48 * time.Hour)},
			want:  false,
		},
		{
			name:  "at ttl boundary",
			entry: releaseCacheEntry{Tag: "v0.5.12", CheckedAt: now.Add(-24 * time.Hour)},
			want:  false,
		},
		{
			name:  "missing tag",
			entry: releaseCacheEntry{CheckedAt: now.Add(-time.Hour)},
			want:  false,
		},
		{
			name:  "missing checked at",
			entry: releaseCacheEntry{Tag: "v0.5.13"},
			want:  false,
		},
		{
			name:  "future checked at",
			entry: releaseCacheEntry{Tag: "v0.5.13", CheckedAt: now.Add(time.Second)},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.entry.fresh(now, updateCheckCacheTTL); got != tt.want {
				t.Fatalf("fresh() = %v, want %v", got, tt.want)
			}
		})
	}
}
