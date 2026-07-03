// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"errors"
	"fmt"
	"os"
	osuser "os/user"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/db"
)

type hostStoragePathReason string

const (
	hostStoragePathReasonCatchRoot   hostStoragePathReason = "catch-root"
	hostStoragePathReasonServiceRoot hostStoragePathReason = "service-root"
	hostStoragePathReasonServicesDir hostStoragePathReason = "services-root"
	hostStoragePathReasonDataDir     hostStoragePathReason = "data-dir"
)

type hostStoragePathMapping struct {
	From    string
	To      string
	Reason  hostStoragePathReason
	Service string
}

type hostStoragePathMappings []hostStoragePathMapping

type hostStorageReferenceKind string

const (
	hostStorageReferenceDB       hostStorageReferenceKind = "db"
	hostStorageReferenceSystemd  hostStorageReferenceKind = "systemd"
	hostStorageReferenceArtifact hostStorageReferenceKind = "artifact"
)

type hostStorageReference struct {
	Kind    hostStorageReferenceKind
	Service string
	Field   string
	Path    string
	Unit    string
	File    string
	Line    int
}

func (m hostStoragePathMappings) Sorted() hostStoragePathMappings {
	out := append(hostStoragePathMappings(nil), m...)
	slices.SortFunc(out, func(a, b hostStoragePathMapping) int {
		aFrom := cleanHostStoragePath(a.From)
		bFrom := cleanHostStoragePath(b.From)
		if len(aFrom) != len(bFrom) {
			return len(bFrom) - len(aFrom)
		}
		if c := strings.Compare(aFrom, bFrom); c != 0 {
			return c
		}
		return strings.Compare(string(a.Reason), string(b.Reason))
	})
	return out
}

func (m hostStoragePathMappings) Rewrite(value string) (string, bool, error) {
	if strings.TrimSpace(value) == "" || !filepath.IsAbs(value) {
		return value, false, nil
	}
	for _, mapping := range m.Sorted() {
		rewritten, ok, err := relocatePathUnderRoot(value, mapping.From, mapping.To)
		if err != nil {
			return "", false, err
		}
		if ok {
			return rewritten, cleanHostStoragePath(rewritten) != cleanHostStoragePath(value), nil
		}
	}
	return filepath.Clean(value), false, nil
}

func hostStorageMappingsFromPlan(plan catchrpc.HostStoragePlan) hostStoragePathMappings {
	var mappings hostStoragePathMappings
	if plan.CatchAction.Move && !hostStoragePathsEqual(plan.CatchAction.From, plan.CatchAction.To) {
		mappings = append(mappings, hostStoragePathMapping{From: plan.CatchAction.From, To: plan.CatchAction.To, Reason: hostStoragePathReasonCatchRoot})
	}
	for _, move := range plan.ServicesAction.AffectedServices {
		if hostStoragePathsEqual(move.From, move.To) {
			continue
		}
		mappings = append(mappings, hostStoragePathMapping{From: move.From, To: move.To, Reason: hostStoragePathReasonServiceRoot, Service: move.Name})
	}
	if strings.TrimSpace(plan.Current.ServicesRoot) != "" &&
		strings.TrimSpace(plan.Desired.ServicesRoot) != "" &&
		!hostStoragePathsEqual(plan.Current.ServicesRoot, plan.Desired.ServicesRoot) {
		mappings = append(mappings, hostStoragePathMapping{From: plan.Current.ServicesRoot, To: plan.Desired.ServicesRoot, Reason: hostStoragePathReasonServicesDir})
	}
	if plan.DataDirAction.Move && !hostStoragePathsEqual(plan.DataDirAction.From, plan.DataDirAction.To) {
		mappings = append(mappings, hostStoragePathMapping{From: plan.DataDirAction.From, To: plan.DataDirAction.To, Reason: hostStoragePathReasonDataDir})
	}
	return mappings.Sorted()
}

var hostStorageLookupUserHomeFn = hostStorageLookupUserHome

func hostStorageKnownLegacyRepairMappings(current catchrpc.HostStorageState, catchRoot string, legacyDataDirs []string) hostStoragePathMappings {
	var mappings hostStoragePathMappings
	for _, dataDir := range legacyDataDirs {
		dataDir = cleanHostStoragePath(dataDir)
		if dataDir == "" {
			continue
		}
		servicesRoot := filepath.Join(dataDir, "services")
		if catchRoot != "" && !hostStoragePathsEqual(catchRoot, filepath.Join(servicesRoot, CatchService)) {
			mappings = append(mappings, hostStoragePathMapping{From: filepath.Join(servicesRoot, CatchService), To: catchRoot, Reason: hostStoragePathReasonCatchRoot})
		}
		if current.ServicesRoot != "" && !hostStoragePathsEqual(current.ServicesRoot, servicesRoot) {
			mappings = append(mappings, hostStoragePathMapping{From: servicesRoot, To: current.ServicesRoot, Reason: hostStoragePathReasonServicesDir})
		}
		if current.DataDir != "" && !hostStoragePathsEqual(current.DataDir, dataDir) {
			mappings = append(mappings, hostStoragePathMapping{From: dataDir, To: current.DataDir, Reason: hostStoragePathReasonDataDir})
		}
	}
	return mappings.Sorted()
}

func inferredHostStorageRepairMappings(current catchrpc.HostStorageState, catchRoot string, refs []hostStorageReference, legacyDataDirs []string) hostStoragePathMappings {
	var mappings hostStoragePathMappings
	legacyRefs := hostStorageLegacyRepairRefs(refs, legacyDataDirs)
	for _, dataDir := range legacyDataDirs {
		dataDir = cleanHostStoragePath(dataDir)
		if dataDir == "" {
			continue
		}
		mappings = append(mappings, inferredHostStorageRepairMappingsForDataDir(current, catchRoot, dataDir, legacyRefs[dataDir])...)
	}
	return mappings.Sorted()
}

func inferredHostStorageRepairMappingsForDataDir(current catchrpc.HostStorageState, catchRoot, dataDir string, presence hostStorageLegacyRepairPresence) hostStoragePathMappings {
	var mappings hostStoragePathMappings
	servicesRoot := filepath.Join(dataDir, "services")
	if presence.catchRoot && catchRoot != "" && !hostStoragePathsEqual(catchRoot, filepath.Join(servicesRoot, CatchService)) {
		mappings = append(mappings, hostStoragePathMapping{From: filepath.Join(servicesRoot, CatchService), To: catchRoot, Reason: hostStoragePathReasonCatchRoot})
	}
	if presence.servicesRoot && current.ServicesRoot != "" && !hostStoragePathsEqual(current.ServicesRoot, servicesRoot) {
		mappings = append(mappings, hostStoragePathMapping{From: servicesRoot, To: current.ServicesRoot, Reason: hostStoragePathReasonServicesDir})
	}
	if presence.dataDir && current.DataDir != "" && !hostStoragePathsEqual(current.DataDir, dataDir) {
		mappings = append(mappings, hostStoragePathMapping{From: dataDir, To: current.DataDir, Reason: hostStoragePathReasonDataDir})
	}
	return mappings
}

type hostStorageLegacyRepairPresence struct {
	dataDir      bool
	servicesRoot bool
	catchRoot    bool
}

func hostStorageLegacyRepairRefs(refs []hostStorageReference, legacyDataDirs []string) map[string]hostStorageLegacyRepairPresence {
	presence := make(map[string]hostStorageLegacyRepairPresence)
	for _, ref := range refs {
		hostStorageAddLegacyRepairRefPresence(presence, ref.Path, legacyDataDirs)
		hostStorageAddLegacyRepairRefPresence(presence, ref.File, legacyDataDirs)
	}
	return presence
}

func hostStorageAddLegacyRepairRefPresence(presence map[string]hostStorageLegacyRepairPresence, value string, legacyDataDirs []string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	for _, dataDir := range legacyDataDirs {
		dataDir = cleanHostStoragePath(dataDir)
		if dataDir == "" {
			continue
		}
		item := presence[dataDir]
		if hostStoragePathHasPrefix(value, dataDir) {
			item.dataDir = true
		}
		if hostStoragePathHasPrefix(value, filepath.Join(dataDir, "services")) {
			item.servicesRoot = true
		}
		if hostStoragePathHasPrefix(value, filepath.Join(dataDir, "services", CatchService)) {
			item.catchRoot = true
		}
		presence[dataDir] = item
	}
}

func hostStorageLegacyDefaultDataDirs(cfg Config) []string {
	var homes []string
	addHome := func(home string) {
		home = cleanHostStoragePath(home)
		if home == "" {
			return
		}
		if !slices.Contains(homes, home) {
			homes = append(homes, home)
		}
	}
	addHome("/root")
	if home, ok := hostStorageLookupUserHomeFn(cfg.InstallUser); ok {
		addHome(home)
	}
	var dirs []string
	for _, home := range homes {
		for _, name := range []string{"data", "yeet-data"} {
			dir := filepath.Join(home, name)
			if !slices.Contains(dirs, dir) {
				dirs = append(dirs, dir)
			}
		}
	}
	return dirs
}

func hostStorageLookupUserHome(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}
	if u, err := osuser.Lookup(name); err == nil && strings.TrimSpace(u.HomeDir) != "" {
		return u.HomeDir, true
	}
	if name == "root" {
		return "/root", true
	}
	if !hostStorageInstallUserCanInferHome(name) {
		return "", false
	}
	return filepath.Join("/home", name), true
}

func hostStorageInstallUserCanInferHome(name string) bool {
	if name == "." || name == ".." {
		return false
	}
	return !strings.ContainsAny(name, `/\@`)
}

func hostStoragePathHasPrefix(value, root string) bool {
	value = cleanHostStoragePath(value)
	root = cleanHostStoragePath(root)
	if value == "" || root == "" {
		return false
	}
	if value == root {
		return true
	}
	return strings.HasPrefix(value, root+string(filepath.Separator))
}

func hostStorageMappingSources(mappings hostStoragePathMappings) []string {
	roots := make([]string, 0, len(mappings))
	for _, mapping := range mappings {
		roots = append(roots, cleanHostStoragePath(mapping.From))
	}
	return uniqueSortedStrings(roots)
}

func hostStorageRepairActionFromRefs(dbRefs, systemdRefs, artifactRefs []hostStorageReference, mappings hostStoragePathMappings) catchrpc.HostStorageRepairAction {
	references := len(dbRefs) + len(systemdRefs) + len(artifactRefs)
	if references == 0 {
		return catchrpc.HostStorageRepairAction{}
	}
	return catchrpc.HostStorageRepairAction{
		References:      references,
		DatabaseRefs:    len(dbRefs),
		SystemdRefs:     len(systemdRefs),
		ArtifactRefs:    len(artifactRefs),
		RegenerateUnits: hostStorageRepairRegenerateUnits(systemdRefs),
		RestartServices: hostStorageRepairRestartServices(dbRefs, systemdRefs, artifactRefs),
		ValidationRoots: hostStorageMappingSources(mappings),
	}
}

func hostStorageRepairRegenerateUnits(refs []hostStorageReference) []string {
	units := make([]string, 0, len(refs))
	for _, ref := range refs {
		if strings.TrimSpace(ref.Unit) == "" {
			continue
		}
		units = append(units, ref.Unit)
	}
	return uniqueSortedStrings(units)
}

func hostStorageRepairRestartServices(dbRefs, systemdRefs, artifactRefs []hostStorageReference) []string {
	services := make([]string, 0, len(dbRefs)+len(systemdRefs)+len(artifactRefs))
	for _, ref := range dbRefs {
		if service := strings.TrimSpace(ref.Service); service != "" && !hostStorageSelfManagedService(service) {
			services = append(services, service)
		}
	}
	for _, ref := range artifactRefs {
		if service := strings.TrimSpace(ref.Service); service != "" && !hostStorageSelfManagedService(service) {
			services = append(services, service)
		}
	}
	for _, ref := range systemdRefs {
		if service, ok := hostStorageRepairServiceFromUnit(ref.Unit); ok && !hostStorageSelfManagedService(service) {
			services = append(services, service)
		}
	}
	return uniqueSortedStrings(services)
}

func hostStorageRepairServiceFromUnit(unit string) (string, bool) {
	base := strings.TrimSpace(unit)
	if base == "" {
		return "", false
	}
	base = strings.TrimSuffix(base, ".service")
	base = strings.TrimSuffix(base, ".timer")
	if base == "" {
		return "", false
	}
	if strings.HasPrefix(base, "yeet-vm-") {
		base = strings.TrimPrefix(base, "yeet-vm-")
	} else if strings.HasPrefix(base, "yeet-") {
		base = strings.TrimPrefix(base, "yeet-")
		for _, suffix := range []string{"-ns", "-ts"} {
			base = strings.TrimSuffix(base, suffix)
		}
	}
	if base == "" || strings.ContainsAny(base, ".@") {
		return "", false
	}
	return base, true
}

func scanHostStorageDataRefs(data *db.Data, mappings hostStoragePathMappings) []hostStorageReference {
	if data == nil || len(mappings) == 0 {
		return nil
	}
	var refs []hostStorageReference
	serviceKeys := make([]string, 0, len(data.Services))
	for key := range data.Services {
		serviceKeys = append(serviceKeys, key)
	}
	slices.Sort(serviceKeys)
	for _, key := range serviceKeys {
		service := data.Services[key]
		if service == nil {
			continue
		}
		serviceName := service.Name
		if serviceName == "" {
			serviceName = key
		}
		refs = scanHostStorageServiceArtifactRefs(refs, serviceName, service, mappings)
		if service.VM != nil {
			refs = appendHostStoragePathRef(refs, serviceName, "VM.Image.Kernel", service.VM.Image.Kernel, mappings)
			refs = appendHostStoragePathRef(refs, serviceName, "VM.Image.RootFS", service.VM.Image.RootFS, mappings)
			refs = appendHostStoragePathRef(refs, serviceName, "VM.Disk.Path", service.VM.Disk.Path, mappings)
			refs = appendHostStoragePathRef(refs, serviceName, "VM.Console.SocketPath", service.VM.Console.SocketPath, mappings)
			refs = appendHostStoragePathRef(refs, serviceName, "VM.Console.LogPath", service.VM.Console.LogPath, mappings)
			refs = appendHostStoragePathRef(refs, serviceName, "VM.Sockets.APISocketPath", service.VM.Sockets.APISocketPath, mappings)
			refs = appendHostStoragePathRef(refs, serviceName, "VM.Sockets.VsockSocketPath", service.VM.Sockets.VsockSocketPath, mappings)
			refs = appendHostStoragePathRef(refs, serviceName, "VM.PIDFile", service.VM.PIDFile, mappings)
		}
	}
	return refs
}

func scanHostStorageServiceArtifactRefs(refs []hostStorageReference, serviceName string, service *db.Service, mappings hostStoragePathMappings) []hostStorageReference {
	artifactNames := make([]db.ArtifactName, 0, len(service.Artifacts))
	for name := range service.Artifacts {
		artifactNames = append(artifactNames, name)
	}
	slices.SortFunc(artifactNames, func(a, b db.ArtifactName) int {
		return strings.Compare(string(a), string(b))
	})
	for _, name := range artifactNames {
		ref, path, ok := currentHostStorageArtifactRef(service.Artifacts[name], service.Generation, service.LatestGeneration)
		if !ok {
			continue
		}
		field := "Artifacts." + string(name) + ".Refs." + string(ref)
		refs = appendHostStoragePathRef(refs, serviceName, field, path, mappings)
	}
	return refs
}

func scanHostStorageGeneratedArtifactRefs(data *db.Data, mappings hostStoragePathMappings) ([]hostStorageReference, error) {
	if data == nil || len(mappings) == 0 {
		return nil, nil
	}
	var refs []hostStorageReference
	serviceKeys := make([]string, 0, len(data.Services))
	for key := range data.Services {
		serviceKeys = append(serviceKeys, key)
	}
	slices.Sort(serviceKeys)
	for _, serviceKey := range serviceKeys {
		service := data.Services[serviceKey]
		if service == nil {
			continue
		}
		serviceRefs, err := scanHostStorageGeneratedArtifactRefsForService(serviceKey, service, mappings)
		if err != nil {
			return nil, err
		}
		refs = append(refs, serviceRefs...)
	}
	return refs, nil
}

func scanHostStorageGeneratedArtifactRefsForService(serviceKey string, service *db.Service, mappings hostStoragePathMappings) ([]hostStorageReference, error) {
	serviceName := service.Name
	if serviceName == "" {
		serviceName = serviceKey
	}
	artifactNames := make([]db.ArtifactName, 0, len(service.Artifacts))
	for name := range service.Artifacts {
		if hostStorageArtifactMayContainPaths(name) {
			artifactNames = append(artifactNames, name)
		}
	}
	slices.SortFunc(artifactNames, func(a, b db.ArtifactName) int {
		return strings.Compare(string(a), string(b))
	})
	var refs []hostStorageReference
	for _, name := range artifactNames {
		paths := hostStorageGeneratedArtifactPathsForRepair(service.Artifacts[name], service.Generation, service.LatestGeneration)
		for _, artifactPath := range paths {
			fileRefs, err := scanHostStorageTextFileRefs(artifactPath.Path, string(name), mappings)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("scan generated artifact %s for %s: %w", name, serviceName, err)
			}
			for _, ref := range fileRefs {
				ref.Kind = hostStorageReferenceArtifact
				ref.Service = serviceName
				ref.Field = "Artifacts." + string(name)
				refs = append(refs, ref)
			}
		}
	}
	return refs, nil
}

func currentHostStorageArtifactRef(artifact *db.Artifact, generation, latestGeneration int) (db.ArtifactRef, string, bool) {
	if artifact == nil {
		return "", "", false
	}
	if generation != 0 {
		ref := db.Gen(generation)
		if path, ok := artifact.Refs[ref]; ok {
			return ref, path, true
		}
	}
	if latestGeneration != 0 && latestGeneration != generation {
		ref := db.Gen(latestGeneration)
		if path, ok := artifact.Refs[ref]; ok {
			return ref, path, true
		}
	}
	path, ok := artifact.Refs["latest"]
	return "latest", path, ok
}

func appendHostStoragePathRef(refs []hostStorageReference, service, field, value string, mappings hostStoragePathMappings) []hostStorageReference {
	if !hostStorageValueMatchesMappings(value, mappings) {
		return refs
	}
	return append(refs, hostStorageReference{
		Kind:    hostStorageReferenceDB,
		Service: service,
		Field:   field,
		Path:    filepath.Clean(value),
	})
}

func hostStorageValueMatchesMappings(value string, mappings hostStoragePathMappings) bool {
	_, changed, err := mappings.Rewrite(value)
	return err == nil && changed
}

func scanHostStorageSystemdRefs(systemdDir string, mappings hostStoragePathMappings) ([]hostStorageReference, error) {
	if strings.TrimSpace(systemdDir) == "" || len(mappings) == 0 {
		return nil, nil
	}
	entries, err := os.ReadDir(systemdDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var refs []hostStorageReference
	for _, entry := range entries {
		if entry.IsDir() || !hostStorageYeetUnitName(entry.Name()) {
			continue
		}
		path := filepath.Join(systemdDir, entry.Name())
		fileRefs, err := scanHostStorageTextFileRefs(path, entry.Name(), mappings)
		if err != nil {
			return nil, err
		}
		refs = append(refs, fileRefs...)
	}
	return refs, nil
}

func hostStorageYeetUnitName(name string) bool {
	if !hostStorageSystemdUnitFileName(name) {
		return false
	}
	if name == CatchService+".service" ||
		strings.HasPrefix(name, "catch.") ||
		strings.HasPrefix(name, "yeet-") {
		return true
	}
	return hostStoragePrimaryUnitName(name)
}

func hostStorageSystemdUnitFileName(name string) bool {
	for _, suffix := range []string{".service", ".timer", ".target", ".socket", ".path", ".mount"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func hostStoragePrimaryUnitName(name string) bool {
	base, ok := strings.CutSuffix(name, ".service")
	if !ok {
		base, ok = strings.CutSuffix(name, ".timer")
	}
	return ok && base != "" && !strings.ContainsAny(base, ".@")
}

func scanHostStorageTextFileRefs(path, unit string, mappings hostStoragePathMappings) ([]hostStorageReference, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(raw), "\n")
	var refs []hostStorageReference
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		seen := map[string]bool{}
		for _, candidate := range hostStorageTextPathCandidates(line) {
			if seen[candidate] || !hostStorageValueMatchesMappings(candidate, mappings) {
				continue
			}
			seen[candidate] = true
			refs = append(refs, hostStorageReference{
				Kind: hostStorageReferenceSystemd,
				Path: filepath.Clean(candidate),
				Unit: unit,
				File: path,
				Line: i + 1,
			})
		}
	}
	return refs, nil
}

func hostStorageTextPathCandidates(line string) []string {
	fields := strings.FieldsFunc(line, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(`"'=:,;()[]{}<>`, r)
	})
	candidates := make([]string, 0, len(fields))
	for _, field := range fields {
		if strings.HasPrefix(field, "/") {
			candidates = append(candidates, strings.TrimRight(field, "."))
		}
	}
	return candidates
}
