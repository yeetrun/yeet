// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	githubAPIBase = "https://api.github.com"
	githubOwner   = "shayne"
	githubRepo    = "yeet"
)

type githubRelease struct {
	TagName     string        `json:"tag_name"`
	Name        string        `json:"name"`
	Prerelease  bool          `json:"prerelease"`
	PublishedAt string        `json:"published_at"`
	Assets      []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func fetchGitHubRelease(nightly bool) (githubRelease, error) {
	return fetchGitHubReleaseFromURL(githubReleaseURL(nightly), &http.Client{Timeout: 30 * time.Second})
}

func githubReleaseURL(nightly bool) string {
	path := fmt.Sprintf("/repos/%s/%s/releases/latest", githubOwner, githubRepo)
	if nightly {
		path = fmt.Sprintf("/repos/%s/%s/releases/tags/nightly", githubOwner, githubRepo)
	}
	return githubAPIBase + path
}

func fetchGitHubReleaseFromURL(url string, client *http.Client) (rel githubRelease, err error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return githubRelease{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return githubRelease{}, err
	}
	defer func() {
		if closeErr := resp.Body.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return githubRelease{}, fmt.Errorf("github api error: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return githubRelease{}, err
	}
	if rel.TagName == "" {
		return githubRelease{}, errors.New("missing release tag")
	}
	return rel, nil
}

func findGitHubAssetURL(assets []githubAsset, name string) (string, error) {
	for _, asset := range assets {
		if asset.Name == name {
			if asset.BrowserDownloadURL == "" {
				return "", fmt.Errorf("asset %s missing download url", name)
			}
			return asset.BrowserDownloadURL, nil
		}
	}
	return "", fmt.Errorf("asset not found: %s", name)
}
