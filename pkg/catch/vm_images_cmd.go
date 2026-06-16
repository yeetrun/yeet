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
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/cmdutil"
)

const vmImagesUsage = "usage: yeet vm images [ls|catalog|update|import <name>|rm <name>|prune]"

type vmImageListRow struct {
	Payload      string `json:"payload"`
	Kind         string `json:"kind"`
	State        string `json:"state"`
	Version      string `json:"version,omitempty"`
	CachePath    string `json:"cachePath,omitempty"`
	KernelPolicy string `json:"kernelPolicy,omitempty"`
}

type vmImageCatalogRow struct {
	Payload       string `json:"payload"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	DefaultUser   string `json:"defaultUser,omitempty"`
	VersionPrefix string `json:"versionPrefix,omitempty"`
	KernelPolicy  string `json:"kernelPolicy,omitempty"`
}

var vmImageInspectFunc = func(ctx context.Context, cache vmImageCache, payload string) (vmImageCacheState, vmImageManifest, error) {
	return cache.Inspect(ctx, payload)
}

var vmImageInspectCatalogFunc = func(ctx context.Context, cache vmImageCache, image vmImageCatalogImage) (vmImageCacheState, vmImageManifest, error) {
	return cache.withManifestURL(image.ManifestURL).inspectRemote(ctx, image.Payload, image)
}

var vmImageEnsureFunc = func(ctx context.Context, cache vmImageCache, payload string, ui ProgressUI) (vmImageAsset, error) {
	return ensureVMImageAssetWithProgress(ctx, cache, payload, ui)
}

var vmImageEnsureCatalogFunc = func(ctx context.Context, cache vmImageCache, image vmImageCatalogImage, ui ProgressUI) (vmImageAsset, error) {
	return ensureVMImageCatalogAssetWithProgress(ctx, cache, image, ui)
}

func (e *ttyExecer) vmImagesCmdFunc(flags cli.VMImagesFlags, args []string) error {
	if len(args) == 0 {
		return e.vmImagesListCmdFunc(flags)
	}
	return e.vmImagesActionCmdFunc(flags, args[0], args[1:])
}

func (e *ttyExecer) vmImagesActionCmdFunc(flags cli.VMImagesFlags, action string, args []string) error {
	switch action {
	case "ls":
		return vmImagesNoArgAction(args, func() error { return e.vmImagesListCmdFunc(flags) })
	case "catalog":
		return vmImagesNoArgAction(args, func() error { return e.vmImagesCatalogCmdFunc(flags) })
	case "update":
		return e.vmImagesUpdateCmdFunc(flags, args)
	case "import":
		return vmImagesNameAction(args, func(name string) error { return e.vmImagesImportCmdFunc(flags, name) })
	case "rm":
		return vmImagesNameAction(args, func(name string) error { return e.vmImagesRemoveCmdFunc(flags, name) })
	case "prune":
		return vmImagesNoArgAction(args, func() error { return e.vmImagesPruneCmdFunc(flags) })
	default:
		return fmt.Errorf("%s", vmImagesUsage)
	}
}

func vmImagesNoArgAction(args []string, run func() error) error {
	if len(args) != 0 {
		return fmt.Errorf("%s", vmImagesUsage)
	}
	return run()
}

func vmImagesNameAction(args []string, run func(string) error) error {
	name, ok := vmImagesSingleNameArg(args)
	if !ok {
		return fmt.Errorf("%s", vmImagesUsage)
	}
	return run(name)
}

func vmImagesSingleNameArg(args []string) (string, bool) {
	if len(args) != 1 {
		return "", false
	}
	return args[0], true
}

func (e *ttyExecer) vmImagesListCmdFunc(flags cli.VMImagesFlags) error {
	cache := e.vmImageCache()
	ctx := e.vmImagesContext()
	catalog, err := cache.FetchCatalog(ctx)
	if err != nil {
		return err
	}
	var rows []vmImageListRow
	for _, image := range catalog.Images {
		state, _, err := vmImageInspectCatalogFunc(ctx, cache, image)
		if err != nil {
			return err
		}
		rows = append(rows, vmImageListRowFromCacheState(state))
	}
	refs, err := listLocalVMImages(cache.Root)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		rows = append(rows, vmImageListRowFromLocalRef(ref, "ready"))
	}
	pruneRows, err := e.s.planVMImagePrune(ctx, cache)
	if err != nil {
		return err
	}
	for _, row := range pruneRows {
		if row.Kind == vmImagePruneKindCache && row.State == vmImagePruneStateCurrent {
			continue
		}
		rows = append(rows, vmImageListRowFromPruneRow(row))
	}
	return renderVMImageListRows(e.rw, flags.Format, rows)
}

func (e *ttyExecer) vmImagesCatalogCmdFunc(flags cli.VMImagesFlags) error {
	cache := e.vmImageCache()
	catalog, err := cache.FetchCatalog(e.vmImagesContext())
	if err != nil {
		return err
	}
	rows := make([]vmImageCatalogRow, 0, len(catalog.Images))
	for _, image := range catalog.Images {
		rows = append(rows, vmImageCatalogRowFromCatalogImage(image))
	}
	refs, err := listLocalVMImages(cache.Root)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		rows = append(rows, vmImageCatalogRowFromLocalRef(ref))
	}
	return renderVMImageCatalogRows(e.rw, flags.Format, rows)
}

func (e *ttyExecer) vmImagesUpdateCmdFunc(flags cli.VMImagesFlags, args []string) error {
	cache := e.vmImageCache()
	ctx := e.vmImagesContext()
	catalog, err := cache.FetchCatalog(ctx)
	if err != nil {
		return err
	}
	images, err := vmImagesUpdateImages(args, catalog)
	if err != nil {
		return err
	}
	states := make([]vmImageCacheState, 0, len(images))
	for _, image := range images {
		asset, err := e.ensureCatalogVMImageAndPrune(ctx, cache, image, e.vmImagesProgressUI(flags))
		if err != nil {
			return err
		}
		state := vmImageCacheState{
			Payload:       image.Payload,
			CachedVersion: asset.Manifest.Version,
			LatestVersion: asset.Manifest.Version,
			State:         vmImageCacheCurrent,
			CachePath:     asset.Paths.Dir,
			ManifestURL:   image.ManifestURL,
		}
		if state.CachePath == "" && state.CachedVersion != "" {
			state.CachePath = filepath.Join(cache.Root, state.CachedVersion)
		}
		states = append(states, state)
	}
	if len(states) == 1 {
		return renderVMImageCacheState(e.rw, flags.Format, states[0])
	}
	return renderVMImageCacheStates(e.rw, flags.Format, states)
}

func vmImagesUpdateImages(args []string, catalog vmImageCatalog) ([]vmImageCatalogImage, error) {
	if len(args) == 0 {
		images := make([]vmImageCatalogImage, 0, len(catalog.Images))
		for _, image := range catalog.Images {
			images = append(images, image.normalized())
		}
		sort.Slice(images, func(i, j int) bool {
			return images[i].Payload < images[j].Payload
		})
		return images, nil
	}
	if len(args) != 1 {
		return nil, fmt.Errorf("%s", vmImagesUsage)
	}
	source, err := resolveVMImagePayloadFromCatalog(args[0], catalog)
	if err != nil {
		return nil, err
	}
	if source.Kind != vmImageSourceRemote {
		return nil, fmt.Errorf("VM image update only supports catalog images: %s", vmImageCatalogPayloadsForError(catalog))
	}
	return []vmImageCatalogImage{source.Family}, nil
}

func (e *ttyExecer) vmImagesImportCmdFunc(flags cli.VMImagesFlags, name string) error {
	if !flags.Stdin {
		return fmt.Errorf("use yeet vm images import from the client")
	}
	cache := e.vmImageCache()
	catalog, err := cache.FetchCatalog(e.vmImagesContext())
	if err != nil {
		return err
	}
	defaultImage, ok := catalog.DefaultImage()
	if !ok {
		return fmt.Errorf("VM image catalog has no default image for local import")
	}
	importer := localVMImageImporter{
		CacheRoot: cache.Root,
		EnsureManagedAsset: func(ctx context.Context) (vmImageAsset, error) {
			return vmImageEnsureCatalogFunc(ctx, cache, defaultImage, e.vmImagesProgressUI(flags))
		},
	}
	ref, err := importer.Import(e.vmImagesContext(), localVMImageImportRequest{
		Name:             name,
		Reader:           e.payloadReader(),
		AllowLocalKernel: flags.AllowLocalKernel,
	})
	if err != nil {
		return err
	}
	return renderVMImageListRows(e.rw, flags.Format, []vmImageListRow{vmImageListRowFromLocalRef(ref, "imported")})
}

func (e *ttyExecer) vmImagesRemoveCmdFunc(flags cli.VMImagesFlags, name string) error {
	if !flags.Yes {
		return fmt.Errorf("rerun with --yes to remove local VM image %q", name)
	}
	if err := removeLocalVMImage(e.vmImageCache().Root, name); err != nil {
		return err
	}
	row := vmImageListRow{
		Payload: "vm://" + name,
		Kind:    "local",
		State:   "removed",
	}
	return renderVMImageListRows(e.rw, flags.Format, []vmImageListRow{row})
}

func (e *ttyExecer) vmImagesPruneCmdFunc(flags cli.VMImagesFlags) error {
	cache := e.vmImageCache()
	rows, err := e.s.planVMImagePrune(e.vmImagesContext(), cache)
	if err != nil {
		return err
	}
	if flags.DryRun {
		return renderVMImagePruneRows(e.rw, flags.Format, rows)
	}
	if !vmImagePruneRowsHavePrunable(rows) {
		return renderVMImagePruneRows(e.rw, flags.Format, rows)
	}
	if !flags.Yes {
		if strings.TrimSpace(flags.Format) != "" && strings.TrimSpace(flags.Format) != "table" {
			return fmt.Errorf("rerun with --yes or --dry-run to prune VM images with %s output", flags.Format)
		}
		if err := renderVMImagePruneRows(e.rw, flags.Format, rows); err != nil {
			return err
		}
		ok, err := cmdutil.Confirm(e.rw, e.rw, "Remove prunable VM images?")
		if err != nil {
			return fmt.Errorf("failed to confirm VM image prune: %w", err)
		}
		if !ok {
			return nil
		}
	}
	rows = e.s.applyVMImagePrune(e.vmImagesContext(), rows)
	return renderVMImagePruneRows(e.rw, flags.Format, rows)
}

func (e *ttyExecer) vmImagesProgressUI(flags cli.VMImagesFlags) ProgressUI {
	switch strings.TrimSpace(flags.Format) {
	case "json", "json-pretty":
		return nil
	default:
		return e.newProgressUI("vm images")
	}
}

func (e *ttyExecer) vmImagesContext() context.Context {
	if e.ctx != nil {
		return e.ctx
	}
	return context.Background()
}

func (e *ttyExecer) ensureCatalogVMImageAndPrune(ctx context.Context, cache vmImageCache, image vmImageCatalogImage, ui ProgressUI) (vmImageAsset, error) {
	asset, err := vmImageEnsureCatalogFunc(ctx, cache, image, ui)
	if err != nil {
		return vmImageAsset{}, err
	}
	e.pruneVMImagesAfterManagedUpdate(ctx, cache, image.Payload)
	return asset, nil
}

func ensureVMImageCatalogAssetWithProgress(ctx context.Context, cache vmImageCache, image vmImageCatalogImage, ui ProgressUI) (asset vmImageAsset, retErr error) {
	cache = cache.withManifestURL(image.ManifestURL)
	if ui != nil {
		state, _, err := cache.inspectRemote(ctx, image.Payload, image)
		if err != nil {
			return vmImageAsset{}, err
		}
		if state.State == vmImageCacheCurrent {
			return cachedVMImageAsset(ctx, cache, state.CachedVersion)
		}
	}

	var progress *byteProgress
	if ui != nil {
		progress = newByteProgress(0)
		ui.Start()
		ui.StartStep("Download VM image")
		defer func() {
			if retErr != nil {
				ui.FailStep(retErr.Error())
			} else {
				ui.DoneStep(progress.finalDetail())
			}
			ui.Stop()
		}()
	}
	return ensureVMImageAssetFromCatalog(ctx, cache, image, progress, ui)
}

func vmImageCatalogRowFromCatalogImage(image vmImageCatalogImage) vmImageCatalogRow {
	return vmImageCatalogRow{
		Payload:       image.Payload,
		Kind:          "builtin",
		Name:          image.Name,
		DefaultUser:   image.DefaultUser,
		VersionPrefix: image.VersionPrefix,
	}
}

func vmImageCatalogRowFromLocalRef(ref localVMImageRef) vmImageCatalogRow {
	row := vmImageCatalogRow{
		Payload:      ref.Payload,
		Kind:         "local",
		Name:         ref.Name,
		KernelPolicy: ref.KernelPolicy,
	}
	if manifest, err := readLocalVMImageBlobManifest(ref.Root); err == nil {
		row.DefaultUser = manifest.DefaultUser
	}
	return row
}

func vmImageListRowFromCacheState(state vmImageCacheState) vmImageListRow {
	version := state.LatestVersion
	if version == "" {
		version = state.CachedVersion
	}
	return vmImageListRow{
		Payload:   state.Payload,
		Kind:      "builtin",
		State:     string(state.State),
		Version:   version,
		CachePath: state.CachePath,
	}
}

func vmImageListRowFromLocalRef(ref localVMImageRef, state string) vmImageListRow {
	return vmImageListRow{
		Payload:      ref.Payload,
		Kind:         "local",
		State:        state,
		Version:      ref.Version,
		CachePath:    ref.Root,
		KernelPolicy: ref.KernelPolicy,
	}
}

func vmImageListRowFromPruneRow(row vmImagePruneRow) vmImageListRow {
	return vmImageListRow{
		Payload:   row.Payload,
		Kind:      row.Kind,
		State:     row.State,
		Version:   row.Version,
		CachePath: row.Path,
	}
}

func renderVMImageCatalogRows(w io.Writer, formatOut string, rows []vmImageCatalogRow) error {
	switch strings.TrimSpace(formatOut) {
	case "json":
		return json.NewEncoder(w).Encode(rows)
	case "json-pretty":
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(rows)
	case "", "table":
		return renderVMImageCatalogRowsTable(w, rows)
	default:
		return fmt.Errorf("unsupported vm images format %q", formatOut)
	}
}

func renderVMImageCatalogRowsTable(w io.Writer, rows []vmImageCatalogRow) error {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(tw, "PAYLOAD\tKIND\tNAME\tDEFAULT_USER\tKERNEL_POLICY"); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			row.Payload,
			row.Kind,
			row.Name,
			dash(row.DefaultUser),
			dash(row.KernelPolicy),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func renderVMImageListRows(w io.Writer, formatOut string, rows []vmImageListRow) error {
	switch strings.TrimSpace(formatOut) {
	case "json":
		return json.NewEncoder(w).Encode(rows)
	case "json-pretty":
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(rows)
	case "", "table":
		return renderVMImageListRowsTable(w, rows)
	default:
		return fmt.Errorf("unsupported vm images format %q", formatOut)
	}
}

func renderVMImageListRowsTable(w io.Writer, rows []vmImageListRow) error {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(tw, "PAYLOAD\tKIND\tSTATE\tVERSION\tCACHE"); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			row.Payload,
			row.Kind,
			row.State,
			dash(row.Version),
			dash(row.CachePath),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
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

func renderVMImageCacheStates(w io.Writer, formatOut string, states []vmImageCacheState) error {
	switch strings.TrimSpace(formatOut) {
	case "json":
		return json.NewEncoder(w).Encode(states)
	case "json-pretty":
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(states)
	case "", "table":
		return renderVMImageCacheStatesTable(w, states)
	default:
		return fmt.Errorf("unsupported vm images format %q", formatOut)
	}
}

func renderVMImageCacheStatesTable(w io.Writer, states []vmImageCacheState) error {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(tw, "PAYLOAD\tSTATE\tCACHED\tLATEST\tCACHE"); err != nil {
		return err
	}
	for _, state := range states {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			state.Payload,
			state.State,
			dash(state.CachedVersion),
			dash(state.LatestVersion),
			dash(state.CachePath),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
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
