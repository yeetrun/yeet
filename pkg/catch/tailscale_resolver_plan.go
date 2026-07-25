// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/yeetrun/yeet/pkg/db"
	"golang.org/x/sys/unix"
	"tailscale.com/ipn"
)

type tailscaleResolverGenerationLayout string

const (
	tailscaleResolverGenerationHistorical tailscaleResolverGenerationLayout = "historical-run"
	tailscaleResolverGenerationCurrent    tailscaleResolverGenerationLayout = "current-bin-env"
)

type tailscaleResolverGeneration struct {
	Layout           tailscaleResolverGenerationLayout
	Daemon           string
	EnvironmentFile  string
	ConfigFile       string
	SocketFile       string
	WorkingDirectory string
	Interface        string
	Args             []string
}

type tailscaleResolverServiceRecordProof struct {
	Generation        int    `json:"generation"`
	LatestGeneration  int    `json:"latestGeneration"`
	ServiceRoot       string `json:"serviceRoot"`
	Interface         string `json:"interface"`
	TSServiceArtifact string `json:"tsServiceArtifact"`
	TSBinaryArtifact  string `json:"tsBinaryArtifact"`
	TSEnvArtifact     string `json:"tsEnvArtifact"`
	TSConfigArtifact  string `json:"tsConfigArtifact"`
}

type tailscaleResolverUnitFilePlan struct {
	Root     string
	Relative string
	Path     string
	Original []byte
	Next     []byte
	Proof    serviceIdentityPathProof
}

type tailscaleResolverManagedPathProof struct {
	Root     string
	Relative string
	Proof    serviceIdentityPathProof
}

type tailscaleResolverServicePlan struct {
	ServiceName string
	UnitName    string
	WasActive   bool
	Record      tailscaleResolverServiceRecordProof
	Generation  tailscaleResolverGeneration
	Files       []tailscaleResolverUnitFilePlan
	Provenance  []tailscaleResolverManagedPathProof
}

type tailscaleResolverFleetPlan struct {
	CatchRunner tailscaleResolverManagedPathProof
	Services    []tailscaleResolverServicePlan
}

var (
	tailscaleResolverUnitActive      = inspectTailscaleResolverUnitActive
	tailscaleResolverUnitDropInPaths = inspectTailscaleResolverUnitDropInPaths
)

type tailscaleResolverUnitInput struct {
	root     string
	relative string
	path     string
	raw      []byte
	proof    serviceIdentityPathProof
	parsed   tailscaleResolverUnit
}

func expectedTailscaleResolverGeneration(
	service db.Service,
	layout tailscaleResolverGenerationLayout,
) (tailscaleResolverGeneration, error) {
	root := service.ServiceRoot
	if !cleanAbsolutePath(root) {
		return tailscaleResolverGeneration{}, fmt.Errorf("service root must be an absolute clean path: %q", service.ServiceRoot)
	}
	if service.TSNet == nil || service.TSNet.Interface == "" {
		return tailscaleResolverGeneration{}, errors.New("tailscale interface is required")
	}
	if !validLinuxInterfaceName(service.TSNet.Interface) {
		return tailscaleResolverGeneration{}, fmt.Errorf("tailscale interface must be plain: %q", service.TSNet.Interface)
	}

	runDir := serviceRunDirForRoot(root)
	envDir := serviceEnvDirForRoot(root)
	generation := tailscaleResolverGeneration{
		Layout:           layout,
		SocketFile:       filepath.Join(runDir, "tailscaled.sock"),
		WorkingDirectory: filepath.Join(root, "tailscale"),
		Interface:        service.TSNet.Interface,
	}
	switch layout {
	case tailscaleResolverGenerationHistorical:
		generation.Daemon = filepath.Join(runDir, "tailscaled")
		generation.EnvironmentFile = filepath.Join(runDir, "tailscaled.env")
		generation.ConfigFile = filepath.Join(runDir, "tailscaled.json")
	case tailscaleResolverGenerationCurrent:
		generation.Daemon = filepath.Join(serviceBinDirForRoot(root), "tailscaled")
		generation.EnvironmentFile = filepath.Join(envDir, "tailscaled.env")
		generation.ConfigFile = filepath.Join(envDir, "tailscaled.json")
	default:
		return tailscaleResolverGeneration{}, fmt.Errorf("unknown tailscale resolver generation layout %q", layout)
	}
	generation.Args = []string{
		"--statedir=.",
		"--socket=" + generation.SocketFile,
		"--config=" + generation.ConfigFile,
		"--tun=" + generation.Interface,
	}
	return generation, nil
}

func (s *Server) planTailscaleResolverIsolationFleet(
	ctx context.Context,
	dv *db.DataView,
) (tailscaleResolverFleetPlan, error) {
	if err := ctx.Err(); err != nil {
		return tailscaleResolverFleetPlan{}, err
	}
	if dv == nil || !dv.Valid() {
		return tailscaleResolverFleetPlan{}, errors.New("tailscale resolver fleet preflight requires a valid database view")
	}
	names := tailscaleResolverCandidateNames(*dv)
	if len(names) == 0 {
		return tailscaleResolverFleetPlan{}, nil
	}
	catchRunner := s.catchRunnerPath()
	if err := validateTailscaleResolverCatchPath(catchRunner); err != nil {
		return tailscaleResolverFleetPlan{}, err
	}
	catchRunnerLocation, err := tailscaleResolverCatchRunnerLocation(catchRunner)
	if err != nil {
		return tailscaleResolverFleetPlan{}, err
	}
	catchRunnerProof, _, err := captureTailscaleResolverManagedFileAt(
		catchRunnerLocation.root,
		catchRunnerLocation.relative,
		catchRunnerLocation.path,
		"Catch runner",
	)
	if err != nil {
		return tailscaleResolverFleetPlan{}, err
	}

	plan := tailscaleResolverFleetPlan{
		CatchRunner: tailscaleResolverManagedPathProof{
			Root:     catchRunnerLocation.root,
			Relative: catchRunnerLocation.relative,
			Proof:    catchRunnerProof,
		},
		Services: make([]tailscaleResolverServicePlan, 0, len(names)),
	}
	services, err := s.planTailscaleResolverServices(ctx, dv, names, catchRunner)
	if err != nil {
		return tailscaleResolverFleetPlan{}, err
	}
	plan.Services = services
	return plan, nil
}

func (s *Server) planTailscaleResolverServices(
	ctx context.Context,
	dv *db.DataView,
	names []string,
	catchRunner string,
) ([]tailscaleResolverServicePlan, error) {
	services := make([]tailscaleResolverServicePlan, 0, len(names))
	var validationErrors []error
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		serviceView := dv.Services().Get(name)
		service := *serviceView.AsStruct()
		service.ServiceRoot = s.serviceRootFromView(serviceView)
		servicePlan, err := s.planTailscaleResolverService(ctx, service, catchRunner)
		if err != nil {
			validationErrors = append(validationErrors, fmt.Errorf("service %q: %w", name, err))
			continue
		}
		services = append(services, servicePlan)
	}
	if err := errors.Join(validationErrors...); err != nil {
		return nil, err
	}
	return services, nil
}

func tailscaleResolverCandidateNames(dv db.DataView) []string {
	names := make([]string, 0, dv.Services().Len())
	for name, service := range dv.Services().All() {
		record := service.AsStruct()
		if tailscaleResolverPersistedRecord(*record) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func tailscaleResolverPersistedRecord(service db.Service) bool {
	if service.TSNet != nil {
		return true
	}
	for _, name := range []db.ArtifactName{
		db.ArtifactTSService,
		db.ArtifactTSBinary,
		db.ArtifactTSEnv,
		db.ArtifactTSConfig,
	} {
		if _, ok := service.Artifacts[name]; ok {
			return true
		}
	}
	return false
}

func (s *Server) planTailscaleResolverService(
	ctx context.Context,
	service db.Service,
	catchRunner string,
) (tailscaleResolverServicePlan, error) {
	unitName := "yeet-" + service.Name + "-ts.service"
	record, err := tailscaleResolverRecordProof(service)
	if err != nil {
		return tailscaleResolverServicePlan{}, err
	}
	installedPath := tailscaleSidecarInstalledUnitPath(service.Name)
	if record.TSServiceArtifact == installedPath {
		return tailscaleResolverServicePlan{}, errors.New("canonical and installed Tailscale unit paths must be distinct")
	}
	inputs, err := loadTailscaleResolverUnitInputs(service, record, installedPath)
	if err != nil {
		return tailscaleResolverServicePlan{}, err
	}
	generation, err := classifyAndValidateTailscaleResolverUnits(
		service,
		record.TSServiceArtifact,
		inputs,
		catchRunner,
	)
	if err != nil {
		return tailscaleResolverServicePlan{}, err
	}
	provenance, err := s.captureTailscaleResolverProvenance(service, record, generation)
	if err != nil {
		return tailscaleResolverServicePlan{}, err
	}
	files, err := buildTailscaleResolverUnitFilePlans(inputs, catchRunner)
	if err != nil {
		return tailscaleResolverServicePlan{}, err
	}
	if err := ensureTailscaleResolverUnitHasNoDropIns(ctx, unitName); err != nil {
		return tailscaleResolverServicePlan{}, err
	}
	wasActive, err := tailscaleResolverUnitActive(unitName)
	if err != nil {
		return tailscaleResolverServicePlan{}, fmt.Errorf("inspect active state for %s: %w", unitName, err)
	}
	return tailscaleResolverServicePlan{
		ServiceName: service.Name,
		UnitName:    unitName,
		WasActive:   wasActive,
		Record:      record,
		Generation:  generation,
		Files:       files,
		Provenance:  provenance,
	}, nil
}

func loadTailscaleResolverUnitInputs(
	service db.Service,
	record tailscaleResolverServiceRecordProof,
	installedPath string,
) ([]tailscaleResolverUnitInput, error) {
	locations, err := tailscaleResolverUnitLocations(service, record, installedPath)
	if err != nil {
		return nil, err
	}
	inputs := make([]tailscaleResolverUnitInput, 0, len(locations))
	for _, location := range locations {
		proof, raw, err := captureTailscaleResolverManagedFileAt(
			location.root,
			location.relative,
			location.path,
			"unit",
		)
		if err != nil {
			return nil, err
		}
		parsed, err := parseTailscaleResolverUnit(string(raw))
		if err != nil {
			return nil, fmt.Errorf("parse tailscale unit %s: %w", location.path, err)
		}
		inputs = append(inputs, tailscaleResolverUnitInput{
			root: location.root, relative: location.relative,
			path: location.path, raw: raw, proof: proof, parsed: parsed,
		})
	}
	if sameTailscaleResolverFileIdentity(inputs[0].proof, inputs[1].proof) {
		return nil, errors.New("canonical and installed Tailscale units must have distinct file identities")
	}
	return inputs, nil
}

func classifyAndValidateTailscaleResolverUnits(
	service db.Service,
	canonicalPath string,
	inputs []tailscaleResolverUnitInput,
	catchRunner string,
) (tailscaleResolverGeneration, error) {
	var canonical *tailscaleResolverUnitInput
	for i := range inputs {
		if inputs[i].path == canonicalPath {
			canonical = &inputs[i]
			break
		}
	}
	if canonical == nil {
		return tailscaleResolverGeneration{}, errors.New("canonical Tailscale unit input is missing")
	}
	generation, err := classifyTailscaleResolverGeneration(service, canonical.parsed, catchRunner)
	if err != nil {
		return tailscaleResolverGeneration{}, fmt.Errorf("canonical unit: %w", err)
	}
	for _, input := range inputs {
		if err := validateTailscaleResolverUnitGeneration(service, input.parsed, generation, catchRunner); err != nil {
			if input.path == canonicalPath {
				return tailscaleResolverGeneration{}, fmt.Errorf("canonical unit %s: %w", input.path, err)
			}
			return tailscaleResolverGeneration{}, fmt.Errorf("installed unit %s diverges from canonical artifact: %w", input.path, err)
		}
	}
	return generation, nil
}

func (s *Server) captureTailscaleResolverProvenance(
	service db.Service,
	record tailscaleResolverServiceRecordProof,
	generation tailscaleResolverGeneration,
) ([]tailscaleResolverManagedPathProof, error) {
	for _, paths := range []struct {
		label      string
		generation string
		runtime    string
	}{
		{label: "tailscaled binary", generation: record.TSBinaryArtifact, runtime: generation.Daemon},
		{label: "tailscaled environment", generation: record.TSEnvArtifact, runtime: generation.EnvironmentFile},
		{label: "tailscaled config", generation: record.TSConfigArtifact, runtime: generation.ConfigFile},
	} {
		if paths.generation == paths.runtime {
			return nil, fmt.Errorf(
				"%s generation artifact must differ from selected runtime",
				paths.label,
			)
		}
	}
	pairs, err := s.tailscaleResolverProvenancePairs(service, record, generation)
	if err != nil {
		return nil, err
	}
	provenance := make([]tailscaleResolverManagedPathProof, 0, 6)
	for _, artifact := range pairs {
		captured, err := captureTailscaleResolverProvenancePair(artifact, generation.ConfigFile)
		if err != nil {
			return nil, err
		}
		provenance = append(provenance, captured...)
	}
	return provenance, nil
}

func captureTailscaleResolverProvenancePair(
	artifact tailscaleResolverProvenancePair,
	configFile string,
) ([]tailscaleResolverManagedPathProof, error) {
	runtimeProof, runtimeRaw, err := captureTailscaleResolverManagedFileAt(
		artifact.runtime.root,
		artifact.runtime.relative,
		artifact.runtime.path,
		artifact.label+" selected runtime",
	)
	if err != nil {
		return nil, err
	}
	generationProof, _, err := captureTailscaleResolverManagedFileAt(
		artifact.generation.root,
		artifact.generation.relative,
		artifact.generation.path,
		artifact.label+" generation artifact",
	)
	if err != nil {
		return nil, err
	}
	if sameTailscaleResolverFileIdentity(runtimeProof, generationProof) {
		return nil, fmt.Errorf("%s generation artifact and selected runtime must have distinct file identities", artifact.label)
	}
	if runtimeProof.SHA256 != generationProof.SHA256 {
		return nil, fmt.Errorf(
			"%s generation artifact %s does not match selected runtime %s",
			artifact.label,
			artifact.generation.path,
			artifact.runtime.path,
		)
	}
	if artifact.runtime.path == configFile {
		if err := validateTailscaleResolverAcceptDNSFalse(runtimeRaw); err != nil {
			return nil, fmt.Errorf("%s %s: %w", artifact.label, artifact.runtime.path, err)
		}
	}
	return []tailscaleResolverManagedPathProof{
		{Root: artifact.runtime.root, Relative: artifact.runtime.relative, Proof: runtimeProof},
		{Root: artifact.generation.root, Relative: artifact.generation.relative, Proof: generationProof},
	}, nil
}

type tailscaleResolverProvenancePair struct {
	label      string
	runtime    tailscaleResolverManagedLocation
	generation tailscaleResolverManagedLocation
}

func (s *Server) tailscaleResolverProvenancePairs(
	service db.Service,
	record tailscaleResolverServiceRecordProof,
	generation tailscaleResolverGeneration,
) ([]tailscaleResolverProvenancePair, error) {
	runtimeBinary, err := tailscaleResolverServiceLocation(service.ServiceRoot, generation.Daemon)
	if err != nil {
		return nil, err
	}
	runtimeEnv, err := tailscaleResolverServiceLocation(service.ServiceRoot, generation.EnvironmentFile)
	if err != nil {
		return nil, err
	}
	runtimeConfig, err := tailscaleResolverServiceLocation(service.ServiceRoot, generation.ConfigFile)
	if err != nil {
		return nil, err
	}
	generationBinary, err := tailscaleResolverGenerationBinaryLocation(s.cfg.RootDir, record.TSBinaryArtifact)
	if err != nil {
		return nil, err
	}
	generationEnv, err := tailscaleResolverGenerationDataLocation(
		service,
		record.TSEnvArtifact,
		".env",
	)
	if err != nil {
		return nil, err
	}
	generationConfig, err := tailscaleResolverGenerationDataLocation(
		service,
		record.TSConfigArtifact,
		".json",
	)
	if err != nil {
		return nil, err
	}
	return []tailscaleResolverProvenancePair{
		{
			label: "tailscaled binary", runtime: runtimeBinary, generation: generationBinary,
		},
		{
			label: "tailscaled environment", runtime: runtimeEnv, generation: generationEnv,
		},
		{
			label: "tailscaled config", runtime: runtimeConfig, generation: generationConfig,
		},
	}, nil
}

func buildTailscaleResolverUnitFilePlans(
	inputs []tailscaleResolverUnitInput,
	catchRunner string,
) ([]tailscaleResolverUnitFilePlan, error) {
	files := make([]tailscaleResolverUnitFilePlan, 0, len(inputs))
	for _, input := range inputs {
		next, _, err := ensureTailscaleUnitResolverIsolation(string(input.raw), catchRunner)
		if err != nil {
			return nil, fmt.Errorf("plan resolver isolation for %s: %w", input.path, err)
		}
		files = append(files, tailscaleResolverUnitFilePlan{
			Root: input.root, Relative: input.relative,
			Path: input.path, Original: input.raw, Next: []byte(next), Proof: input.proof,
		})
	}
	return files, nil
}

func (s *Server) catchRunnerPathFromDataView(dv db.DataView) string {
	root := s.defaultServiceRootDir(CatchService)
	if catchView, ok := dv.Services().GetOk(CatchService); ok {
		root = s.serviceRootFromView(catchView)
	}
	return filepath.Join(serviceRunDirForRoot(root), "catch")
}

type tailscaleResolverManagedLocation struct {
	root     string
	relative string
	path     string
}

func tailscaleResolverCatchRunnerLocation(path string) (tailscaleResolverManagedLocation, error) {
	if !cleanAbsolutePath(path) {
		return tailscaleResolverManagedLocation{}, fmt.Errorf("catch runner path must be absolute and clean: %q", path)
	}
	root := filepath.Dir(filepath.Dir(path))
	return newTailscaleResolverManagedLocation(root, filepath.Join("run", "catch"), path, "Catch runner")
}

func tailscaleResolverUnitLocations(
	service db.Service,
	record tailscaleResolverServiceRecordProof,
	installedPath string,
) ([]tailscaleResolverManagedLocation, error) {
	if !cleanAbsolutePath(service.ServiceRoot) {
		return nil, fmt.Errorf("service root must be an absolute clean path: %q", service.ServiceRoot)
	}
	canonicalBase := filepath.Base(record.TSServiceArtifact)
	if !validTailscaleResolverVersionedFilename(
		canonicalBase,
		"yeet-"+service.Name+"-ts-",
		".service",
	) {
		return nil, fmt.Errorf("canonical Tailscale unit has an unmanaged generation filename: %q", record.TSServiceArtifact)
	}
	canonical, err := newTailscaleResolverManagedLocation(
		service.ServiceRoot,
		filepath.Join("bin", canonicalBase),
		record.TSServiceArtifact,
		"managed generation artifact location for canonical Tailscale unit",
	)
	if err != nil {
		return nil, err
	}
	installed, err := newTailscaleResolverManagedLocation(
		systemdSystemDir,
		"yeet-"+service.Name+"-ts.service",
		installedPath,
		"installed Tailscale unit",
	)
	if err != nil {
		return nil, err
	}
	locations := []tailscaleResolverManagedLocation{canonical, installed}
	sort.Slice(locations, func(i, j int) bool { return locations[i].path < locations[j].path })
	return locations, nil
}

func tailscaleResolverServiceLocation(root, path string) (tailscaleResolverManagedLocation, error) {
	if !cleanAbsolutePath(root) || !cleanAbsolutePath(path) {
		return tailscaleResolverManagedLocation{}, fmt.Errorf("managed Tailscale runtime path must be absolute and clean: %q", path)
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return tailscaleResolverManagedLocation{}, fmt.Errorf("locate managed Tailscale runtime path %s: %w", path, err)
	}
	return newTailscaleResolverManagedLocation(root, relative, path, "managed Tailscale runtime")
}

func tailscaleResolverGenerationBinaryLocation(
	dataRoot string,
	path string,
) (tailscaleResolverManagedLocation, error) {
	if _, err := tailscaleResolverGenerationBinaryVersion(path); err != nil {
		return tailscaleResolverManagedLocation{}, err
	}
	base := filepath.Base(path)
	return newTailscaleResolverManagedLocation(
		dataRoot,
		filepath.Join("tsd", base),
		path,
		"managed generation artifact location for tailscaled binary",
	)
}

func tailscaleResolverGenerationBinaryVersion(path string) (string, error) {
	const prefix = "tailscaled-"
	base := filepath.Base(path)
	version := strings.TrimPrefix(base, prefix)
	parsed, err := semver.NewVersion(version)
	if !strings.HasPrefix(base, prefix) || err != nil || parsed.String() != version {
		return "", fmt.Errorf(
			"tailscaled generation artifact must have a versioned filename: %q",
			path,
		)
	}
	return version, nil
}

func tailscaleResolverGenerationDataLocation(
	service db.Service,
	path string,
	suffix string,
) (tailscaleResolverManagedLocation, error) {
	base := filepath.Base(path)
	if !validTailscaleResolverVersionedFilename(base, "tailscaled-", suffix) {
		return tailscaleResolverManagedLocation{}, fmt.Errorf(
			"tailscaled generation artifact must have a versioned filename: %q",
			path,
		)
	}
	return newTailscaleResolverManagedLocation(
		service.ServiceRoot,
		filepath.Join("tailscale", base),
		path,
		"managed generation artifact location",
	)
}

func newTailscaleResolverManagedLocation(
	root string,
	relative string,
	path string,
	label string,
) (tailscaleResolverManagedLocation, error) {
	if !cleanAbsolutePath(root) {
		return tailscaleResolverManagedLocation{}, fmt.Errorf("%s root must be absolute and clean: %q", label, root)
	}
	if !cleanAbsolutePath(path) || relative == "" || filepath.IsAbs(relative) ||
		filepath.Clean(relative) != relative || relative == "." ||
		relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
		filepath.Join(root, relative) != path {
		return tailscaleResolverManagedLocation{}, fmt.Errorf("%s path is outside its exact managed location: %q", label, path)
	}
	return tailscaleResolverManagedLocation{root: root, relative: relative, path: path}, nil
}

func validTailscaleResolverVersionedFilename(name, prefix, suffix string) bool {
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return false
	}
	version := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
	if len(version) != 14 {
		return false
	}
	for _, char := range version {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func sameTailscaleResolverFileIdentity(left, right serviceIdentityPathProof) bool {
	return left.Present && right.Present && left.Dev == right.Dev && left.Ino == right.Ino
}

func validLinuxInterfaceName(name string) bool {
	if len(name) == 0 || len(name) > 15 || name == "." || name == ".." {
		return false
	}
	for _, char := range name {
		if !validLinuxInterfaceNameCharacter(char) {
			return false
		}
	}
	return true
}

func validLinuxInterfaceNameCharacter(char rune) bool {
	return (char >= 'a' && char <= 'z') ||
		(char >= 'A' && char <= 'Z') ||
		(char >= '0' && char <= '9') ||
		char == '_' || char == '.' || char == '-'
}

func inspectTailscaleResolverUnitActive(unit string) (bool, error) {
	output, err := exec.Command("systemctl", "show", "--property=ActiveState", "--value", unit).CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("systemctl show ActiveState for %s: %w: %s", unit, err, strings.TrimSpace(string(output)))
	}
	switch state := strings.TrimSpace(string(output)); state {
	case "active":
		return true, nil
	case "inactive", "failed":
		return false, nil
	default:
		return false, fmt.Errorf("systemctl returned unsupported ActiveState %q for %s", state, unit)
	}
}

func inspectTailscaleResolverUnitDropInPaths(
	ctx context.Context,
	unit string,
) ([]string, error) {
	output, err := exec.CommandContext(
		ctx,
		"systemctl",
		"show",
		"--property=DropInPaths",
		"--value",
		unit,
	).Output()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("systemctl show DropInPaths for %s: %w", unit, ctxErr)
		}
		return nil, fmt.Errorf("systemctl show DropInPaths for %s: %w", unit, err)
	}
	value := strings.TrimSpace(string(output))
	if value == "" {
		return nil, nil
	}
	if strings.ContainsAny(value, "\r\n\x00") {
		return nil, fmt.Errorf("systemctl returned malformed DropInPaths for %s", unit)
	}
	return strings.Fields(value), nil
}

func ensureTailscaleResolverUnitHasNoDropIns(ctx context.Context, unit string) error {
	paths, err := tailscaleResolverUnitDropInPaths(ctx, unit)
	if err != nil {
		return fmt.Errorf("inspect effective systemd drop-ins for %s: %w", unit, err)
	}
	if len(paths) != 0 {
		return fmt.Errorf(
			"effective systemd drop-ins are present for %s; remove overriding unit drop-ins",
			unit,
		)
	}
	return nil
}

func tailscaleResolverRecordProof(service db.Service) (tailscaleResolverServiceRecordProof, error) {
	interfaceName := ""
	if service.TSNet != nil {
		interfaceName = service.TSNet.Interface
	}
	record := tailscaleResolverServiceRecordProof{
		Generation:       service.Generation,
		LatestGeneration: service.LatestGeneration,
		ServiceRoot:      service.ServiceRoot,
		Interface:        interfaceName,
	}
	artifacts := []struct {
		name db.ArtifactName
		dst  *string
	}{
		{name: db.ArtifactTSService, dst: &record.TSServiceArtifact},
		{name: db.ArtifactTSBinary, dst: &record.TSBinaryArtifact},
		{name: db.ArtifactTSEnv, dst: &record.TSEnvArtifact},
		{name: db.ArtifactTSConfig, dst: &record.TSConfigArtifact},
	}
	for _, artifact := range artifacts {
		path, ok := service.Artifacts.Gen(artifact.name, service.Generation)
		if !ok {
			return tailscaleResolverServiceRecordProof{}, fmt.Errorf("missing generation %d artifact %q", service.Generation, artifact.name)
		}
		if !cleanAbsolutePath(path) {
			return tailscaleResolverServiceRecordProof{}, fmt.Errorf("artifact %q path must be absolute and clean: %q", artifact.name, path)
		}
		*artifact.dst = path
	}
	return record, nil
}

func classifyTailscaleResolverGeneration(
	service db.Service,
	unit tailscaleResolverUnit,
	catchRunner string,
) (tailscaleResolverGeneration, error) {
	if unit.guardRunner != "" && unit.guardRunner != catchRunner {
		return tailscaleResolverGeneration{}, fmt.Errorf(
			"resolver guard runner = %q, want configured Catch runner %q",
			unit.guardRunner,
			catchRunner,
		)
	}
	for _, layout := range []tailscaleResolverGenerationLayout{
		tailscaleResolverGenerationHistorical,
		tailscaleResolverGenerationCurrent,
	} {
		expected, err := expectedTailscaleResolverGeneration(service, layout)
		if err != nil {
			return tailscaleResolverGeneration{}, err
		}
		if validateTailscaleResolverUnitGeneration(service, unit, expected, catchRunner) == nil {
			return expected, nil
		}
	}
	return tailscaleResolverGeneration{}, errors.New("unit does not select an exact managed generation")
}

func validateTailscaleResolverUnitGeneration(
	service db.Service,
	unit tailscaleResolverUnit,
	expected tailscaleResolverGeneration,
	catchRunner string,
) error {
	expectedNamespace := filepath.Join("/var/run/netns", "yeet-"+service.Name+"-ns")
	if unit.networkNamespace != expectedNamespace {
		return fmt.Errorf("network namespace = %q, want %q", unit.networkNamespace, expectedNamespace)
	}
	expectedSource := filepath.Join("/etc/netns", filepath.Base(expectedNamespace), "resolv.conf")
	if unit.resolverSource != "" && unit.resolverSource != expectedSource {
		return fmt.Errorf("resolver source = %q, want %q", unit.resolverSource, expectedSource)
	}
	if unit.guardRunner != "" && unit.guardRunner != catchRunner {
		return fmt.Errorf("resolver guard runner = %q, want configured Catch runner %q", unit.guardRunner, catchRunner)
	}
	if unit.daemon != expected.Daemon ||
		unit.environmentFile != expected.EnvironmentFile ||
		unit.workingDirectory != expected.WorkingDirectory ||
		!reflect.DeepEqual(unit.args, expected.Args) {
		return fmt.Errorf(
			"unit tuple daemon=%q environment=%q working=%q args=%q does not match %s",
			unit.daemon,
			unit.environmentFile,
			unit.workingDirectory,
			unit.args,
			expected.Layout,
		)
	}
	return nil
}

func captureTailscaleResolverManagedFileAt(
	root string,
	relative string,
	path string,
	label string,
) (serviceIdentityPathProof, []byte, error) {
	proof, err := captureServiceIdentityPathProofAt(root, relative, path)
	if err != nil {
		return serviceIdentityPathProof{}, nil, err
	}
	if err := validateTailscaleResolverManagedFileProof(proof, label, path); err != nil {
		return serviceIdentityPathProof{}, nil, err
	}
	openedProof, raw, err := readTailscaleResolverManagedFileAt(root, relative, path, label)
	if err != nil {
		return serviceIdentityPathProof{}, nil, err
	}
	if !reflect.DeepEqual(proof, openedProof) {
		return serviceIdentityPathProof{}, nil, fmt.Errorf("%s %s changed while being captured", label, path)
	}
	return proof, raw, nil
}

func validateTailscaleResolverManagedFileProof(
	proof serviceIdentityPathProof,
	label string,
	path string,
) error {
	if !proof.Present {
		return fmt.Errorf("%s %s is missing", label, path)
	}
	if !proof.Mode.IsRegular() {
		return fmt.Errorf("%s %s must be a regular file", label, path)
	}
	if proof.Nlink != 1 {
		return fmt.Errorf("%s %s must have exactly one hard link, got %d", label, path, proof.Nlink)
	}
	return nil
}

func readTailscaleResolverManagedFileAt(
	root string,
	relative string,
	path string,
	label string,
) (serviceIdentityPathProof, []byte, error) {
	parentFD, name, closeParent, err := openServiceIdentityMutationParent(root, relative)
	if err != nil {
		return serviceIdentityPathProof{}, nil, err
	}
	defer closeParent()
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return serviceIdentityPathProof{}, nil, fmt.Errorf("open %s %s relative to stable parent: %w", label, path, err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return serviceIdentityPathProof{}, nil, fmt.Errorf("wrap %s %s", label, path)
	}
	defer func() { _ = file.Close() }()
	openedProof, err := captureServiceIdentityOpenFileProof(file, path)
	if err != nil {
		return serviceIdentityPathProof{}, nil, err
	}
	raw, err := io.ReadAll(file)
	if err != nil {
		return serviceIdentityPathProof{}, nil, fmt.Errorf("read %s %s relative to stable parent: %w", label, path, err)
	}
	return openedProof, raw, nil
}

func validateTailscaleResolverAcceptDNSFalse(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	value, ok := fields["acceptDNS"]
	if !ok || strings.TrimSpace(string(value)) != "false" {
		return errors.New("config must explicitly set acceptDNS=false")
	}
	var config ipn.ConfigVAlpha
	if err := json.Unmarshal(raw, &config); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	if !config.AcceptDNS.EqualBool(false) {
		return errors.New("config must explicitly set acceptDNS=false")
	}
	return nil
}

func (s *Server) revalidateTailscaleResolverFleetPlan(
	ctx context.Context,
	plan tailscaleResolverFleetPlan,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.cfg.DB.WithLatestDataLocked(func(latest db.DataView) error {
		latestNames := tailscaleResolverCandidateNames(latest)
		plannedNames := make([]string, len(plan.Services))
		for i := range plan.Services {
			plannedNames[i] = plan.Services[i].ServiceName
		}
		if !reflect.DeepEqual(latestNames, plannedNames) {
			return fmt.Errorf(
				"stale tailscale resolver fleet plan: candidate set changed from %q to %q",
				plannedNames,
				latestNames,
			)
		}
		if len(plannedNames) == 0 {
			return ctx.Err()
		}
		latestCatchRunner := s.catchRunnerPathFromDataView(latest)
		if latestCatchRunner != plan.CatchRunner.Proof.Path {
			return fmt.Errorf(
				"stale tailscale resolver fleet plan: Catch runner path changed from %q to %q",
				plan.CatchRunner.Proof.Path,
				latestCatchRunner,
			)
		}
		if err := validateServiceIdentityPathProofAt(
			plan.CatchRunner.Root,
			plan.CatchRunner.Relative,
			plan.CatchRunner.Proof,
		); err != nil {
			return fmt.Errorf("stale tailscale resolver fleet plan: Catch runner changed: %w", err)
		}
		for _, servicePlan := range plan.Services {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := s.revalidateTailscaleResolverServicePlan(ctx, latest, servicePlan); err != nil {
				return err
			}
		}
		return ctx.Err()
	})
}

func (s *Server) revalidateTailscaleResolverServicePlan(
	ctx context.Context,
	latest db.DataView,
	servicePlan tailscaleResolverServicePlan,
) error {
	if err := ensureTailscaleResolverUnitHasNoDropIns(ctx, servicePlan.UnitName); err != nil {
		return fmt.Errorf(
			"stale tailscale resolver fleet plan for %q: %w",
			servicePlan.ServiceName,
			err,
		)
	}
	serviceView, ok := latest.Services().GetOk(servicePlan.ServiceName)
	if !ok {
		return fmt.Errorf("stale tailscale resolver fleet plan: service %q was removed", servicePlan.ServiceName)
	}
	service := *serviceView.AsStruct()
	service.ServiceRoot = s.serviceRootFromView(serviceView)
	record, err := tailscaleResolverRecordProof(service)
	if err != nil {
		return fmt.Errorf("stale tailscale resolver fleet plan for %q: %w", servicePlan.ServiceName, err)
	}
	if record != servicePlan.Record {
		return fmt.Errorf("stale tailscale resolver fleet plan: service %q database record changed", servicePlan.ServiceName)
	}
	if err := revalidateTailscaleResolverProofs(servicePlan); err != nil {
		return fmt.Errorf("stale tailscale resolver fleet plan for %q: %w", servicePlan.ServiceName, err)
	}
	active, err := tailscaleResolverUnitActive(servicePlan.UnitName)
	if err != nil {
		return fmt.Errorf(
			"stale tailscale resolver fleet plan: inspect active state for service %q: %w",
			servicePlan.ServiceName,
			err,
		)
	}
	if active != servicePlan.WasActive {
		return fmt.Errorf("stale tailscale resolver fleet plan: service %q active state changed", servicePlan.ServiceName)
	}
	return nil
}

func revalidateTailscaleResolverProofs(servicePlan tailscaleResolverServicePlan) error {
	for _, file := range servicePlan.Files {
		if err := validateServiceIdentityPathProofAt(file.Root, file.Relative, file.Proof); err != nil {
			return err
		}
	}
	for _, proof := range servicePlan.Provenance {
		if err := validateServiceIdentityPathProofAt(proof.Root, proof.Relative, proof.Proof); err != nil {
			return err
		}
	}
	return nil
}
