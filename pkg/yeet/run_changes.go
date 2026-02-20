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
	host := strings.TrimSpace(hostOverride)
	if host == "" {
		host = Host()
	}
	entry, ok := cfgLoc.Config.ServiceEntry(serviceOverride, host)
	return entry, ok
}

func hasServiceConfig(cfgLoc *projectConfigLocation, hostOverride string) bool {
	_, ok := serviceEntryForConfig(cfgLoc, hostOverride)
	return ok
}

func removeServiceConfig(cfgLoc *projectConfigLocation, hostOverride string) error {
	if cfgLoc == nil || cfgLoc.Config == nil {
		return nil
	}
	if serviceOverride == "" {
		return nil
	}
	host := strings.TrimSpace(hostOverride)
	if host == "" {
		host = Host()
	}
	if !cfgLoc.Config.RemoveServiceEntry(serviceOverride, host) {
		return nil
	}
	return saveProjectConfig(cfgLoc)
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
	host := strings.TrimSpace(hostOverride)
	if host == "" {
		host = Host()
	}
	entry := ServiceEntry{
		Name:    serviceOverride,
		Host:    host,
		EnvFile: relativeEnvFilePath(loc.Dir, envFile),
	}
	if existing, ok := loc.Config.ServiceEntry(serviceOverride, host); ok {
		entry.Type = existing.Type
		entry.Payload = existing.Payload
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
	summary, err := detectRunChanges(payload, runArgs, envFile, entry.Args)
	if err != nil {
		return err
	}
	if !summary.payloadChanged && !summary.envChanged && !summary.argsChanged {
		if forceDeploy {
			fmt.Fprintln(os.Stdout, "No changes detected, forcing deploy")
			return runRun(payload, runArgs)
		}
		fmt.Fprintln(os.Stdout, "No changes detected")
		return nil
	}
	if summary.envChanged {
		if err := runEnvCopy(envFile); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "Updated env file")
	}
	if summary.payloadChanged || summary.argsChanged {
		if err := runRun(payload, runArgs); err != nil {
			return err
		}
		if summary.payloadChanged && summary.payloadLabel != "" {
			fmt.Fprintf(os.Stdout, "Updated %s\n", summary.payloadLabel)
		}
		if summary.argsChanged && !summary.payloadChanged {
			fmt.Fprintln(os.Stdout, "Updated run config")
		}
	}
	return nil
}

func detectRunChanges(payload string, runArgs []string, envFile string, storedArgs []string) (runChangeSummary, error) {
	summary := runChangeSummary{}
	currentArgs := normalizeRunArgs(runArgs)
	if storedArgs == nil {
		if len(currentArgs) > 0 {
			summary.argsChanged = true
		}
	} else if len(currentArgs) == 0 && len(storedArgs) == 0 {
		summary.argsChanged = false
	} else {
		summary.argsChanged = !reflect.DeepEqual(currentArgs, storedArgs)
	}

	payloadNeedsHash := !shouldAlwaysDeployPayload(payload)
	envNeedsHash := strings.TrimSpace(envFile) != ""

	var remoteHashes catchrpc.ArtifactHashesResponse
	if payloadNeedsHash || envNeedsHash {
		resp, supported, err := fetchRemoteArtifactHashesFn(context.Background(), getService())
		if err != nil {
			return summary, err
		}
		if !supported {
			summary.payloadChanged = payloadNeedsHash || shouldAlwaysDeployPayload(payload)
			summary.envChanged = envNeedsHash
			if payloadNeedsHash {
				summary.payloadLabel = payloadLabelFromLocal(payload, "")
			}
			return summary, nil
		}
		remoteHashes = resp
	}

	if shouldAlwaysDeployPayload(payload) {
		summary.payloadChanged = true
		summary.payloadLabel = ""
	} else if payloadNeedsHash {
		localHash, err := hashFileSHA256(payload)
		if err != nil {
			return summary, err
		}
		remoteHash := ""
		remoteKind := ""
		if remoteHashes.Found && remoteHashes.Payload != nil {
			remoteHash = remoteHashes.Payload.SHA256
			remoteKind = remoteHashes.Payload.Kind
		}
		if remoteHash == "" {
			summary.payloadChanged = true
		} else {
			summary.payloadChanged = localHash != remoteHash
		}
		summary.payloadLabel = payloadLabelFromLocal(payload, remoteKind)
	}

	if envNeedsHash {
		localHash, err := hashFileSHA256(envFile)
		if err != nil {
			return summary, err
		}
		remoteHash := ""
		if remoteHashes.Found && remoteHashes.Env != nil {
			remoteHash = remoteHashes.Env.SHA256
		}
		if remoteHash == "" {
			summary.envChanged = true
		} else {
			summary.envChanged = localHash != remoteHash
		}
	}
	return summary, nil
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

func payloadLabelFromLocal(payloadPath, remoteKind string) string {
	if remoteKind != "" {
		return payloadLabelFromKind(remoteKind)
	}
	goos, goarch, err := remoteCatchOSAndArchFn()
	if err != nil || goos == "" || goarch == "" {
		goos, goarch = runtime.GOOS, runtime.GOARCH
	}
	ft, err := ftdetect.DetectFile(payloadPath, goos, goarch)
	if err != nil {
		return "payload"
	}
	switch ft {
	case ftdetect.Binary:
		return "binary"
	case ftdetect.Script:
		return "script"
	case ftdetect.DockerCompose:
		return "docker compose file"
	case ftdetect.TypeScript:
		return "typescript file"
	case ftdetect.Python:
		return "python file"
	default:
		return "payload"
	}
}

func payloadLabelFromKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "binary":
		return "binary"
	case "script":
		return "script"
	case "docker compose", "compose", "docker-compose":
		return "docker compose file"
	case "typescript", "ts":
		return "typescript file"
	case "python", "py":
		return "python file"
	default:
		return "payload"
	}
}

func hashFileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
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
