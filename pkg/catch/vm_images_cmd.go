// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/yeetrun/yeet/pkg/cli"
)

const vmImagesUsage = "usage: yeet vm images [update]"

var vmImageInspectFunc = func(ctx context.Context, cache vmImageCache, payload string) (vmImageCacheState, vmImageManifest, error) {
	return cache.Inspect(ctx, payload)
}

var vmImageEnsureFunc = func(ctx context.Context, cache vmImageCache, payload string, ui ProgressUI) (vmImageAsset, error) {
	return ensureVMImageAssetWithProgress(ctx, cache, payload, ui)
}

func (e *ttyExecer) vmImagesCmdFunc(flags cli.VMImagesFlags, args []string) error {
	switch {
	case len(args) == 0:
		state, _, err := vmImageInspectFunc(e.vmImagesContext(), e.vmImageCache(), vmUbuntu2604Payload)
		if err != nil {
			return err
		}
		return renderVMImageCacheState(e.rw, flags.Format, state)
	case len(args) == 1 && args[0] == "update":
		return e.vmImagesUpdateCmdFunc(flags)
	default:
		return fmt.Errorf("%s", vmImagesUsage)
	}
}

func (e *ttyExecer) vmImagesUpdateCmdFunc(flags cli.VMImagesFlags) error {
	cache := e.vmImageCache()
	asset, err := vmImageEnsureFunc(e.vmImagesContext(), cache, vmUbuntu2604Payload, e.newProgressUI("vm images"))
	if err != nil {
		return err
	}
	state := vmImageCacheState{
		Payload:       vmUbuntu2604Payload,
		CachedVersion: asset.Manifest.Version,
		LatestVersion: asset.Manifest.Version,
		State:         vmImageCacheCurrent,
		CachePath:     asset.Paths.Dir,
		ManifestURL:   cache.manifestURL(),
	}
	if state.CachePath == "" && state.CachedVersion != "" {
		state.CachePath = filepath.Join(cache.Root, state.CachedVersion)
	}
	return renderVMImageCacheState(e.rw, flags.Format, state)
}

func (e *ttyExecer) vmImagesContext() context.Context {
	if e.ctx != nil {
		return e.ctx
	}
	return context.Background()
}

func renderVMImageCacheState(w io.Writer, formatOut string, state vmImageCacheState) error {
	switch strings.TrimSpace(formatOut) {
	case "json":
		return json.NewEncoder(w).Encode(state)
	case "json-pretty":
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(state)
	case "", "table":
		return renderVMImageCacheStateTable(w, state)
	default:
		return fmt.Errorf("unsupported vm images format %q", formatOut)
	}
}

func renderVMImageCacheStateTable(w io.Writer, state vmImageCacheState) error {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(tw, "PAYLOAD\tSTATE\tCACHED\tLATEST\tCACHE"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
		state.Payload,
		state.State,
		dash(state.CachedVersion),
		dash(state.LatestVersion),
		dash(state.CachePath),
	); err != nil {
		return err
	}
	return tw.Flush()
}
