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
	"github.com/yeetrun/yeet/pkg/cmdutil"
)

const vmImagesUsage = "usage: yeet vm images [ls|update|import <name>|rm <name>|prune]"

type vmImageListRow struct {
	Payload      string `json:"payload"`
	Kind         string `json:"kind"`
	State        string `json:"state"`
	Version      string `json:"version,omitempty"`
	CachePath    string `json:"cachePath,omitempty"`
	KernelPolicy string `json:"kernelPolicy,omitempty"`
}

var vmImageInspectFunc = func(ctx context.Context, cache vmImageCache, payload string) (vmImageCacheState, vmImageManifest, error) {
	return cache.Inspect(ctx, payload)
}

var vmImageEnsureFunc = func(ctx context.Context, cache vmImageCache, payload string, ui ProgressUI) (vmImageAsset, error) {
	return ensureVMImageAssetWithProgress(ctx, cache, payload, ui)
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
	var rows []vmImageListRow
	for _, image := range officialVMImages {
		state, _, err := vmImageInspectFunc(e.vmImagesContext(), cache, image.Payload)
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
	pruneRows, err := e.s.planVMImagePrune(e.vmImagesContext(), cache)
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

func (e *ttyExecer) vmImagesUpdateCmdFunc(flags cli.VMImagesFlags, args []string) error {
	payloads, err := vmImagesUpdatePayloads(args)
	if err != nil {
		return err
	}
	cache := e.vmImageCache()
	states := make([]vmImageCacheState, 0, len(payloads))
	for _, payload := range payloads {
		asset, err := e.ensureManagedVMImageAndPrune(e.vmImagesContext(), cache, payload, e.vmImagesProgressUI(flags))
		if err != nil {
			return err
		}
		state := vmImageCacheState{
			Payload:       payload,
			CachedVersion: asset.Manifest.Version,
			LatestVersion: asset.Manifest.Version,
			State:         vmImageCacheCurrent,
			CachePath:     asset.Paths.Dir,
			ManifestURL:   vmImageManifestURLForPayload(payload),
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

func vmImagesUpdatePayloads(args []string) ([]string, error) {
	if len(args) == 0 {
		payloads := make([]string, 0, len(officialVMImages))
		for _, image := range officialVMImages {
			payloads = append(payloads, image.Payload)
		}
		return payloads, nil
	}
	if len(args) != 1 {
		return nil, fmt.Errorf("%s", vmImagesUsage)
	}
	source, err := resolveVMImagePayload(args[0])
	if err != nil {
		return nil, err
	}
	if source.Kind != vmImageSourceRemote {
		return nil, fmt.Errorf("VM image update only supports official images: %s", officialVMImagePayloadsForError())
	}
	return []string{strings.TrimSpace(args[0])}, nil
}

func vmImageManifestURLForPayload(payload string) string {
	if image, ok := officialVMImageByPayload(payload); ok {
		return image.ManifestURL
	}
	return defaultVMImageManifestURL
}

func (e *ttyExecer) vmImagesImportCmdFunc(flags cli.VMImagesFlags, name string) error {
	if !flags.Stdin {
		return fmt.Errorf("use yeet vm images import from the client")
	}
	cache := e.vmImageCache()
	importer := localVMImageImporter{
		CacheRoot: cache.Root,
		EnsureManagedAsset: func(ctx context.Context) (vmImageAsset, error) {
			return vmImageEnsureFunc(ctx, cache, vmUbuntu2604Payload, e.vmImagesProgressUI(flags))
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

func vmImageListRowFromCacheState(state vmImageCacheState) vmImageListRow {
	version := state.LatestVersion
	if version == "" {
		version = state.CachedVersion
	}
	payload := state.Payload
	if payload == "" {
		payload = vmUbuntu2604Payload
	}
	return vmImageListRow{
		Payload:   payload,
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
	payload := row.Payload
	if payload == "" {
		payload = vmUbuntu2604Payload
	}
	return vmImageListRow{
		Payload:   payload,
		Kind:      row.Kind,
		State:     row.State,
		Version:   row.Version,
		CachePath: row.Path,
	}
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
