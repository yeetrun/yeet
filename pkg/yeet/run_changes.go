// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/ftdetect"
)

type runChangeSummary struct {
	payloadChanged bool
	envChanged     bool
	argsChanged    bool
	payloadLabel   string
}

func (s runChangeSummary) hasChanges() bool {
	return s.payloadChanged || s.envChanged || s.argsChanged
}

func (s runChangeSummary) requiresRun() bool {
	return s.payloadChanged || s.argsChanged
}

func extractEnvFileFlag(args []string) (string, []string, bool, error) {
	if len(args) == 0 {
		return "", args, false, nil
	}
	out := make([]string, 0, len(args))
	var envFile string
	found := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			out = append(out, args[i:]...)
			break
		}
		if arg == "--env-file" {
			if i+1 >= len(args) {
				return "", nil, false, fmt.Errorf("--env-file requires a value")
			}
			envFile = args[i+1]
			found = true
			i++
			continue
		}
		if strings.HasPrefix(arg, "--env-file=") {
			envFile = strings.TrimPrefix(arg, "--env-file=")
			found = true
			continue
		}
		out = append(out, arg)
	}
	return envFile, out, found, nil
}

type serviceRootOptions struct {
	Root string
	ZFS  bool
}

type serviceRootParseState struct {
	opts      serviceRootOptions
	foundRoot bool
	foundZFS  bool
}

func extractServiceRootOptions(args []string) (serviceRootOptions, []string, bool, error) {
	if len(args) == 0 {
		return serviceRootOptions{}, args, false, nil
	}
	out := make([]string, 0, len(args))
	state := serviceRootParseState{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			out = append(out, args[i:]...)
			break
		}
		next, handled, err := parseServiceRootControlArg(args, i, &state)
		if err != nil {
			return serviceRootOptions{}, nil, false, err
		}
		if handled {
			i = next
			continue
		}
		out = append(out, arg)
	}
	if err := validateServiceRootOptions(state); err != nil {
		return serviceRootOptions{}, nil, false, err
	}
	return state.opts, out, state.foundRoot || state.foundZFS, nil
}

func parseServiceRootControlArg(args []string, i int, state *serviceRootParseState) (int, bool, error) {
	arg := args[i]
	switch {
	case arg == "--zfs":
		state.opts.ZFS = true
		state.foundZFS = true
		return i, true, nil
	case strings.HasPrefix(arg, "--zfs="):
		value := strings.TrimPrefix(arg, "--zfs=")
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return i, false, fmt.Errorf("invalid --zfs value %q", value)
		}
		state.opts.ZFS = parsed
		state.foundZFS = true
		return i, true, nil
	case arg == "--service-root":
		if i+1 >= len(args) {
			return i, false, fmt.Errorf("--service-root requires a value")
		}
		state.opts.Root = strings.TrimSpace(args[i+1])
		state.foundRoot = true
		return i + 1, true, nil
	case strings.HasPrefix(arg, "--service-root="):
		state.opts.Root = strings.TrimSpace(strings.TrimPrefix(arg, "--service-root="))
		state.foundRoot = true
		return i, true, nil
	default:
		return i, false, nil
	}
}

func validateServiceRootOptions(state serviceRootParseState) error {
	if state.foundRoot && strings.TrimSpace(state.opts.Root) == "" {
		return fmt.Errorf("--service-root requires a value")
	}
	if state.foundZFS && !state.foundRoot {
		return fmt.Errorf("--zfs requires --service-root")
	}
	if state.foundRoot && !state.opts.ZFS && !filepath.IsAbs(state.opts.Root) {
		return fmt.Errorf("--service-root must be absolute unless --zfs is set")
	}
	return nil
}

func runArgsWithServiceRootOptions(args []string, opts serviceRootOptions) []string {
	args = append([]string{}, args...)
	opts.Root = strings.TrimSpace(opts.Root)
	if opts.Root == "" {
		return args
	}
	out := make([]string, 0, len(args)+2)
	out = append(out, "--service-root="+opts.Root)
	if opts.ZFS {
		out = append(out, "--zfs")
	}
	out = append(out, args...)
	return out
}

type snapshotOptions struct {
	Snapshots string
	KeepLast  int
	MaxAge    string
	Required  *bool
	Events    []string
}

func runArgsWithSnapshotOptions(args []string, opts snapshotOptions) []string {
	out := append([]string{}, args...)
	if opts.Snapshots != "" {
		out = append([]string{"--snapshots=" + opts.Snapshots}, out...)
	}
	if opts.KeepLast != 0 {
		out = append([]string{fmt.Sprintf("--snapshot-keep-last=%d", opts.KeepLast)}, out...)
	}
	if opts.MaxAge != "" {
		out = append([]string{"--snapshot-max-age=" + opts.MaxAge}, out...)
	}
	if opts.Required != nil {
		out = append([]string{fmt.Sprintf("--snapshot-required=%t", *opts.Required)}, out...)
	}
	if len(opts.Events) != 0 {
		out = append([]string{"--snapshot-events=" + strings.Join(opts.Events, ",")}, out...)
	}
	return out
}

func extractForceFlag(args []string) (bool, []string, error) {
	if len(args) == 0 {
		return false, args, nil
	}
	out := make([]string, 0, len(args))
	force := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			out = append(out, args[i:]...)
			break
		}
		if arg == "--force" {
			force = true
			continue
		}
		if strings.HasPrefix(arg, "--force=") {
			value := strings.TrimPrefix(arg, "--force=")
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return false, nil, fmt.Errorf("invalid --force value %q", value)
			}
			force = parsed
			continue
		}
		out = append(out, arg)
	}
	return force, out, nil
}

func filterRemoveArgs(args []string) []string {
	if len(args) == 0 {
		return args
	}
	out := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "--clean-config" {
			continue
		}
		if strings.HasPrefix(arg, "--clean-config=") {
			continue
		}
		out = append(out, arg)
	}
	return out
}

func serviceEntryForConfig(cfgLoc *projectConfigLocation, hostOverride string) (ServiceEntry, bool) {
	if cfgLoc == nil || cfgLoc.Config == nil {
		return ServiceEntry{}, false
	}
	if serviceOverride == "" {
		return ServiceEntry{}, false
	}
	entry, ok := cfgLoc.Config.ServiceEntry(serviceOverride, serviceConfigHost(hostOverride))
	return entry, ok
}

func hasServiceConfig(cfgLoc *projectConfigLocation, hostOverride string) bool {
	_, ok := serviceEntryForConfig(cfgLoc, hostOverride)
	return ok
}

func removeServiceConfig(cfgLoc *projectConfigLocation, hostOverride string) error {
	cfg, service, host, ok := removableServiceConfig(cfgLoc, hostOverride)
	if !ok {
		return nil
	}
	if !cfg.RemoveServiceEntry(service, host) {
		return nil
	}
	return saveProjectConfig(cfgLoc)
}

func removableServiceConfig(cfgLoc *projectConfigLocation, hostOverride string) (*ProjectConfig, string, string, bool) {
	if cfgLoc == nil || cfgLoc.Config == nil || serviceOverride == "" {
		return nil, "", "", false
	}
	return cfgLoc.Config, serviceOverride, serviceConfigHost(hostOverride), true
}

func serviceConfigHost(hostOverride string) string {
	host := strings.TrimSpace(hostOverride)
	if host == "" {
		host = Host()
	}
	return host
}

func saveEnvFileConfig(cfgLoc *projectConfigLocation, hostOverride string, envFile string) error {
	if serviceOverride == "" {
		return nil
	}
	envFile = strings.TrimSpace(envFile)
	if envFile == "" {
		return nil
	}
	loc := cfgLoc
	if loc == nil {
		var err error
		loc, err = loadOrCreateProjectConfigFromCwd()
		if err != nil {
			return err
		}
	}
	entry := ServiceEntry{
		Name:    serviceOverride,
		Host:    serviceConfigHost(hostOverride),
		EnvFile: relativeEnvFilePath(loc.Dir, envFile),
	}
	if existing, ok := loc.Config.ServiceEntry(serviceOverride, entry.Host); ok {
		entry.Type = existing.Type
		entry.Payload = existing.Payload
		entry.ServiceRoot = existing.ServiceRoot
		entry.ServiceRootZFS = existing.ServiceRootZFS
		entry.Snapshots = existing.Snapshots
		entry.SnapshotKeepLast = existing.SnapshotKeepLast
		entry.SnapshotMaxAge = existing.SnapshotMaxAge
		entry.SnapshotRequired = existing.SnapshotRequired
		entry.SnapshotEvents = append([]string{}, existing.SnapshotEvents...)
		entry.Schedule = existing.Schedule
		entry.Args = existing.Args
	}
	loc.Config.SetServiceEntry(entry)
	return saveProjectConfig(loc)
}

func ensureLockedRunFlags(entry ServiceEntry, runArgs []string) error {
	if entry.Name == "" || entry.Host == "" {
		return nil
	}
	storedFlags, _, err := cli.ParseRun(rehydrateRunArgs(entry.Args))
	if err != nil {
		return err
	}
	newFlags, _, err := cli.ParseRun(runArgs)
	if err != nil {
		return err
	}
	if strings.TrimSpace(storedFlags.Net) != strings.TrimSpace(newFlags.Net) || !tagsEqual(storedFlags.TsTags, newFlags.TsTags) {
		return fmt.Errorf("cannot change --net or --ts-tags after initial deploy; remove with --clean-config and redeploy")
	}
	return nil
}

func tagsEqual(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	aa := append([]string{}, a...)
	bb := append([]string{}, b...)
	sort.Strings(aa)
	sort.Strings(bb)
	return reflect.DeepEqual(aa, bb)
}

func runWithChanges(payload string, runArgs []string, envFile string, entry ServiceEntry, forceDeploy bool) error {
	return runWithChangesTo(os.Stdout, payload, runArgs, envFile, entry, forceDeploy)
}

func runWithChangesTo(stdout io.Writer, payload string, runArgs []string, envFile string, entry ServiceEntry, forceDeploy bool) error {
	storedArgs := runArgsWithServiceRootOptions(entry.Args, serviceRootOptions{Root: entry.ServiceRoot, ZFS: entry.ServiceRootZFS})
	storedArgs = runArgsWithSnapshotOptions(storedArgs, snapshotOptions{
		Snapshots: entry.Snapshots,
		KeepLast:  entry.SnapshotKeepLast,
		MaxAge:    entry.SnapshotMaxAge,
		Required:  entry.SnapshotRequired,
		Events:    entry.SnapshotEvents,
	})
	summary, err := detectRunChanges(payload, runArgs, envFile, storedArgs)
	if err != nil {
		return err
	}
	return applyRunChangeSummary(stdout, payload, runArgs, envFile, summary, forceDeploy)
}

func applyRunChangeSummary(stdout io.Writer, payload string, runArgs []string, envFile string, summary runChangeSummary, forceDeploy bool) error {
	if !summary.hasChanges() {
		return applyUnchangedRun(stdout, payload, runArgs, forceDeploy)
	}
	if summary.envChanged {
		if err := runEnvCopy(envFile); err != nil {
			return err
		}
		if err := writeRunChangeLine(stdout, "Updated env file"); err != nil {
			return err
		}
	}
	if summary.requiresRun() {
		if err := runRun(payload, runArgs); err != nil {
			return err
		}
		return writeRunDeployStatus(stdout, summary)
	}
	return nil
}

func applyUnchangedRun(stdout io.Writer, payload string, runArgs []string, forceDeploy bool) error {
	if !forceDeploy {
		return writeRunChangeLine(stdout, "No changes detected")
	}
	if err := writeRunChangeLine(stdout, "No changes detected, forcing deploy"); err != nil {
		return err
	}
	return runRun(payload, runArgs)
}

func writeRunDeployStatus(stdout io.Writer, summary runChangeSummary) error {
	if summary.payloadChanged && summary.payloadLabel != "" {
		return writeRunChangeLine(stdout, "Updated %s", summary.payloadLabel)
	}
	if summary.argsChanged && !summary.payloadChanged {
		return writeRunChangeLine(stdout, "Updated run config")
	}
	return nil
}

func writeRunChangeLine(stdout io.Writer, format string, args ...any) error {
	_, err := fmt.Fprintf(stdout, format+"\n", args...)
	return err
}

func detectRunChanges(payload string, runArgs []string, envFile string, storedArgs []string) (runChangeSummary, error) {
	summary := runChangeSummary{
		argsChanged: runArgsChanged(normalizeRunArgs(runArgs), storedArgs),
	}
	needs := classifyRunChangeNeeds(payload, envFile)
	remoteHashes, supported, err := fetchHashesForRunChanges(needs)
	if err != nil {
		return summary, err
	}
	if !supported {
		return summaryForUnsupportedHashes(summary, payload, needs), nil
	}
	return detectHashBackedRunChanges(summary, payload, envFile, remoteHashes, needs)
}

type runChangeNeeds struct {
	payloadHash         bool
	envHash             bool
	alwaysDeployPayload bool
}

func classifyRunChangeNeeds(payload string, envFile string) runChangeNeeds {
	alwaysDeploy := shouldAlwaysDeployPayload(payload)
	return runChangeNeeds{
		payloadHash:         !alwaysDeploy,
		envHash:             strings.TrimSpace(envFile) != "",
		alwaysDeployPayload: alwaysDeploy,
	}
}

func (n runChangeNeeds) remoteHashes() bool {
	return n.payloadHash || n.envHash
}

func runArgsChanged(currentArgs []string, storedArgs []string) bool {
	if storedArgs == nil {
		return len(currentArgs) > 0
	}
	if len(currentArgs) == 0 && len(storedArgs) == 0 {
		return false
	}
	return !reflect.DeepEqual(currentArgs, storedArgs)
}

func fetchHashesForRunChanges(needs runChangeNeeds) (catchrpc.ArtifactHashesResponse, bool, error) {
	if !needs.remoteHashes() {
		return catchrpc.ArtifactHashesResponse{}, true, nil
	}
	return fetchRemoteArtifactHashesFn(context.Background(), getService())
}

func summaryForUnsupportedHashes(summary runChangeSummary, payload string, needs runChangeNeeds) runChangeSummary {
	summary.payloadChanged = needs.payloadHash || needs.alwaysDeployPayload
	summary.envChanged = needs.envHash
	if needs.payloadHash {
		summary.payloadLabel = payloadLabelFromLocal(payload, "")
	}
	return summary
}

func detectHashBackedRunChanges(summary runChangeSummary, payload string, envFile string, remoteHashes catchrpc.ArtifactHashesResponse, needs runChangeNeeds) (runChangeSummary, error) {
	if needs.alwaysDeployPayload {
		summary.payloadChanged = true
	} else if needs.payloadHash {
		changed, label, err := detectPayloadHashChange(payload, remoteHashes)
		if err != nil {
			return summary, err
		}
		summary.payloadChanged = changed
		summary.payloadLabel = label
	}
	if needs.envHash {
		changed, err := detectEnvHashChange(envFile, remoteHashes)
		if err != nil {
			return summary, err
		}
		summary.envChanged = changed
	}
	return summary, nil
}

func detectPayloadHashChange(payload string, remoteHashes catchrpc.ArtifactHashesResponse) (bool, string, error) {
	localHash, err := hashFileSHA256(payload)
	if err != nil {
		return false, "", err
	}
	remoteHash, remoteKind := remotePayloadHash(remoteHashes)
	return hashChanged(localHash, remoteHash), payloadLabelFromLocal(payload, remoteKind), nil
}

func detectEnvHashChange(envFile string, remoteHashes catchrpc.ArtifactHashesResponse) (bool, error) {
	localHash, err := hashFileSHA256(envFile)
	if err != nil {
		return false, err
	}
	return hashChanged(localHash, remoteEnvHash(remoteHashes)), nil
}

func hashChanged(localHash, remoteHash string) bool {
	return remoteHash == "" || localHash != remoteHash
}

func remotePayloadHash(resp catchrpc.ArtifactHashesResponse) (string, string) {
	if !resp.Found || resp.Payload == nil {
		return "", ""
	}
	return resp.Payload.SHA256, resp.Payload.Kind
}

func remoteEnvHash(resp catchrpc.ArtifactHashesResponse) string {
	if !resp.Found || resp.Env == nil {
		return ""
	}
	return resp.Env.SHA256
}

func shouldAlwaysDeployPayload(payload string) bool {
	if looksLikeImageRef(payload) {
		// TODO: add change detection for image refs.
		return true
	}
	if filepath.Base(payload) == "Dockerfile" {
		// TODO: decide how to hash Dockerfile builds for change detection.
		return true
	}
	return false
}

var payloadLabelsByFileType = map[ftdetect.FileType]string{
	ftdetect.Binary:        "binary",
	ftdetect.Script:        "script",
	ftdetect.DockerCompose: "docker compose file",
	ftdetect.TypeScript:    "typescript file",
	ftdetect.Python:        "python file",
}

var payloadLabelsByKind = map[string]string{
	"binary":         "binary",
	"script":         "script",
	"docker compose": "docker compose file",
	"compose":        "docker compose file",
	"docker-compose": "docker compose file",
	"typescript":     "typescript file",
	"ts":             "typescript file",
	"python":         "python file",
	"py":             "python file",
}

func payloadLabelFromLocal(payloadPath, remoteKind string) string {
	if remoteKind != "" {
		return payloadLabelFromKind(remoteKind)
	}
	ft, err := detectPayloadFileType(payloadPath)
	if err != nil {
		return "payload"
	}
	return payloadLabelFromFileType(ft)
}

func detectPayloadFileType(payloadPath string) (ftdetect.FileType, error) {
	goos, goarch := payloadDetectionTarget()
	return ftdetect.DetectFile(payloadPath, goos, goarch)
}

func payloadDetectionTarget() (string, string) {
	goos, goarch, err := remoteCatchOSAndArchFn()
	if err != nil || goos == "" || goarch == "" {
		return runtime.GOOS, runtime.GOARCH
	}
	return goos, goarch
}

func payloadLabelFromFileType(ft ftdetect.FileType) string {
	if label, ok := payloadLabelsByFileType[ft]; ok {
		return label
	}
	return "payload"
}

func payloadLabelFromKind(kind string) string {
	if label, ok := payloadLabelsByKind[strings.ToLower(strings.TrimSpace(kind))]; ok {
		return label
	}
	return "payload"
}

func hashFileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	return hashReadCloserSHA256(f)
}

func hashReadCloserSHA256(r io.ReadCloser) (sum string, err error) {
	defer func() {
		if closeErr := r.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func fetchRemoteArtifactHashes(ctx context.Context, service string) (catchrpc.ArtifactHashesResponse, bool, error) {
	var resp catchrpc.ArtifactHashesResponse
	if err := newRPCClient(Host()).Call(ctx, "catch.ArtifactHashes", catchrpc.ArtifactHashesRequest{Service: service}, &resp); err != nil {
		if isRPCMethodNotFound(err) {
			return resp, false, nil
		}
		return resp, true, err
	}
	return resp, true, nil
}

func isRPCMethodNotFound(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "method not found")
}

var fetchRemoteArtifactHashesFn = fetchRemoteArtifactHashes
