// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/yeetrun/yeet/pkg/db"
	"golang.org/x/sys/unix"
)

type vmJailerUpgradeVM struct {
	Service                   string
	Payload                   string
	ImageVersion              string
	Architecture              string
	ServiceRoot               string
	Disk                      string
	Firecracker               string
	Jailer                    string
	UnitPath                  string
	UnitContent               []byte
	Readiness                 vmJailerReadiness
	Running                   bool
	Manifest                  vmImageManifest
	NormalizeManagedArtifacts bool
	NeedsUnitUpgrade          bool
}

type VMJailerUpgradeSummary struct {
	Ready          []string
	PendingRestart []string
}

type vmJailerUpgradePlan struct {
	VMs     []vmJailerUpgradeVM
	Summary VMJailerUpgradeSummary
}

type VMJailerUpgrade struct {
	*vmUnitTransaction
	adoption *VMRuntimeAdoption
	summary  VMJailerUpgradeSummary
}

type vmJailerCandidate struct {
	Path         string
	ArtifactName string
	SHA256       string
	Architecture string
}

type vmJailerUpgradeDeps struct {
	preJailerUnit         func(context.Context, string) (bool, error)
	sibling               func(context.Context, vmJailerUpgradeVM) (string, bool, error)
	cached                func(context.Context, vmJailerUpgradeVM) (vmJailerCandidate, bool, error)
	localPayload          func(string) bool
	official              func(context.Context, vmJailerUpgradeVM) (vmJailerCandidate, error)
	install               func(context.Context, vmJailerUpgradeVM, vmJailerCandidate) (string, error)
	readiness             func(string) (vmJailerReadiness, error)
	isRunning             func(*Server, string) (bool, error)
	renderUnit            func(vmSystemdConfig) (string, error)
	ensureRuntimeIdentity func() (vmRuntimeIdentity, error)
	normalizeArtifacts    func(vmJailerUpgradeVM) error
	validateNextStart     func(context.Context, *Config, vmJailerUpgradeVM, vmRuntimeIdentity) error
	renameAt              func(int, string, int, string) error
	exchangeAt            func(int, string, int, string) error
	renameNoReplaceAt     func(int, string, int, string) error
	restoreUnitAt         func(*os.File, string, vmJailerFileIdentity, []byte, os.FileMode, uint32, uint32, func(int, string, int, string) error, func(int, string, int) error) error
	unlinkAt              func(int, string, int) error
	systemctl             func(...string) error
	unitUID               uint32
	unitGID               uint32
}

var (
	errVMJailerUpgradeUnknownPayload             = errors.New("VM image payload is not in the trusted official catalog")
	errVMJailerUpgradeIncompatibleCacheCandidate = errors.New("cached VM jailer is incompatible with the target runtime")
	prepareVMRuntimeAdoptionForJailerUpgrade     = PrepareVMRuntimeAdoption
)

// PrepareVMJailerUpgrade is retained for callers compiled against the old
// jailer-only API. The durable operation now adopts the complete matching
// Firecracker and jailer runtime pair through the fleet transaction.
func PrepareVMJailerUpgrade(ctx context.Context, cfg *Config) (*VMJailerUpgrade, error) {
	adoption, err := prepareVMRuntimeAdoptionForJailerUpgrade(ctx, cfg)
	if err != nil {
		return nil, err
	}
	summary := adoption.Summary()
	if len(summary.Blocked) != 0 {
		return nil, errors.Join(
			fmt.Errorf("legacy jailer upgrade compatibility API cannot adopt blocked VMs: %s", strings.Join(summary.Blocked, ", ")),
			adoption.Close(),
		)
	}
	return &VMJailerUpgrade{
		adoption: adoption,
		summary: VMJailerUpgradeSummary{
			Ready:          summary.Ready,
			PendingRestart: summary.PendingRestart,
		},
	}, nil
}

// PrepareLegacyVMJailerUpgrade stages the one-way unit conversion required by
// VMs created before Catch passed an explicit matching jailer to vm-run. It is
// used only as an install-time bridge before complete runtime adoption.
func PrepareLegacyVMJailerUpgrade(ctx context.Context, cfg *Config) (*VMJailerUpgrade, error) {
	return prepareVMJailerUpgradeWithDeps(ctx, cfg, defaultVMJailerUpgradeDeps())
}

func prepareVMJailerUpgradeWithDeps(ctx context.Context, cfg *Config, deps vmJailerUpgradeDeps) (*VMJailerUpgrade, error) {
	deps = completeVMJailerUpgradeRuntimeDeps(completeVMJailerUpgradeTransactionDeps(deps))
	plan, err := planVMJailerUpgrade(ctx, cfg, deps)
	if err != nil {
		return nil, err
	}
	if len(plan.VMs) > 0 {
		identity, err := deps.ensureRuntimeIdentity()
		if err != nil {
			return nil, fmt.Errorf("ensure VM runtime identity for jailer upgrade: %w", err)
		}
		for _, vm := range plan.VMs {
			if err := deps.normalizeArtifacts(vm); err != nil {
				return nil, fmt.Errorf("normalize managed VM image artifacts for %q: %w", vm.Service, err)
			}
			if err := deps.validateNextStart(ctx, cfg, vm, identity); err != nil {
				return nil, fmt.Errorf("validate next jailer start for %q: %w", vm.Service, err)
			}
		}
	}
	return prepareVMJailerUnitTransaction(ctx, vmJailerUpgradeUnitVMs(plan.VMs), plan.Summary, deps)
}

func prepareVMJailerUnitTransaction(ctx context.Context, vms []vmJailerUpgradeVM, summary VMJailerUpgradeSummary, deps vmJailerUpgradeDeps) (*VMJailerUpgrade, error) {
	deps = completeVMJailerUpgradeTransactionDeps(deps)
	specs := make([]vmUnitSpec, 0, len(vms))
	for _, vm := range vms {
		specs = append(specs, vmUnitSpec{
			Service: vm.Service,
			Path:    vm.UnitPath,
			Content: vm.UnitContent,
		})
	}
	tx, err := prepareVMUnitTransaction(ctx, specs, vmUnitTransactionDepsForJailerUpgrade(deps))
	if err != nil {
		return nil, err
	}
	return &VMJailerUpgrade{vmUnitTransaction: tx, summary: summary}, nil
}

func stageVMJailerUnit(vm vmJailerUpgradeVM, deps vmJailerUpgradeDeps) (vmUnitReplacement, error) {
	deps = completeVMJailerUpgradeTransactionDeps(deps)
	return stageVMUnit(vmUnitSpec{
		Service: vm.Service,
		Path:    vm.UnitPath,
		Content: vm.UnitContent,
	}, vmUnitTransactionDepsForJailerUpgrade(deps))
}

func vmUnitTransactionDepsForJailerUpgrade(deps vmJailerUpgradeDeps) vmUnitTransactionDeps {
	return vmUnitTransactionDeps{
		renameAt:          deps.renameAt,
		exchangeAt:        deps.exchangeAt,
		renameNoReplaceAt: deps.renameNoReplaceAt,
		restoreUnitAt:     deps.restoreUnitAt,
		unlinkAt:          deps.unlinkAt,
		systemctl:         deps.systemctl,
		unitUID:           deps.unitUID,
		unitGID:           deps.unitGID,
	}
}

func (tx *VMJailerUpgrade) Commit() error {
	if tx == nil {
		return nil
	}
	if tx.adoption != nil {
		return tx.adoption.Commit()
	}
	return tx.vmUnitTransaction.Commit()
}

func (tx *VMJailerUpgrade) Close() error {
	if tx == nil {
		return nil
	}
	if tx.adoption != nil {
		return tx.adoption.Close()
	}
	return tx.vmUnitTransaction.Close()
}

func (tx *VMJailerUpgrade) RestorePreviousAndVerify() error {
	if tx == nil {
		return nil
	}
	if tx.adoption != nil {
		return fmt.Errorf("legacy jailer upgrade rollback is unavailable for a VM runtime adoption transaction; close it before commit or use runtime rollback")
	}
	return tx.vmUnitTransaction.RestorePreviousAndVerify()
}

func (tx *VMJailerUpgrade) Summary() VMJailerUpgradeSummary {
	if tx == nil {
		return VMJailerUpgradeSummary{}
	}
	return VMJailerUpgradeSummary{
		Ready:          append([]string(nil), tx.summary.Ready...),
		PendingRestart: append([]string(nil), tx.summary.PendingRestart...),
	}
}
func resolveVMUpgradeJailer(ctx context.Context, vm vmJailerUpgradeVM, deps vmJailerUpgradeDeps) (string, string, error) {
	if path, ok, err := deps.sibling(ctx, vm); err != nil {
		return "", "", err
	} else if ok {
		return path, "sibling", nil
	}
	if candidate, ok, err := deps.cached(ctx, vm); err != nil {
		return "", "", err
	} else if ok {
		path, err := deps.install(ctx, vm, candidate)
		return path, "cache", err
	}
	if !strings.HasPrefix(strings.TrimSpace(vm.Payload), vmImagePayloadPrefix) || deps.localPayload(vm.Payload) {
		return "", "", vmJailerUpgradeReimportError(vm)
	}
	candidate, err := deps.official(ctx, vm)
	if err != nil {
		if errors.Is(err, errVMJailerUpgradeUnknownPayload) {
			return "", "", vmJailerUpgradeReimportError(vm)
		}
		return "", "", fmt.Errorf("VM %q: resolve a matching official jailer: %w", vm.Service, err)
	}
	path, err := deps.install(ctx, vm, candidate)
	return path, "remote", err
}

func vmJailerUpgradeReimportError(vm vmJailerUpgradeVM) error {
	return fmt.Errorf("VM %q has no trusted jailer for Firecracker %s; re-import the custom image with a matching jailer", vm.Service, vm.ImageVersion)
}

func planVMJailerUpgrade(ctx context.Context, cfg *Config, deps vmJailerUpgradeDeps) (vmJailerUpgradePlan, error) {
	if err := ctx.Err(); err != nil {
		return vmJailerUpgradePlan{}, err
	}
	effectiveCfg, err := prepareVMJailerUpgradeConfig(cfg)
	if err != nil {
		return vmJailerUpgradePlan{}, err
	}
	deps = completeVMJailerUpgradeDeps(&effectiveCfg, deps)
	if err := validateVMJailerUpgradeDeps(deps); err != nil {
		return vmJailerUpgradePlan{}, err
	}
	services, names, unitUpgrades, err := readVMJailerUpgradeServices(ctx, &effectiveCfg, deps)
	if err != nil {
		return vmJailerUpgradePlan{}, err
	}
	services = selectedVMJailerUpgradeServices(services, names)
	server := &Server{cfg: effectiveCfg}
	retired, err := inventoryRetiredVMCheckpoints(ctx, server, services)
	if err != nil {
		return vmJailerUpgradePlan{}, fmt.Errorf("inventory retired VM checkpoints: %w", err)
	}
	if err := validateNoRetiredVMCheckpoints(retired); err != nil {
		return vmJailerUpgradePlan{}, err
	}
	return planVMJailerUpgradeServices(ctx, &effectiveCfg, server, services, names, unitUpgrades, deps)
}

func planVMJailerUpgradeServices(ctx context.Context, cfg *Config, server *Server, services map[string]*db.Service, names []string, unitUpgrades map[string]bool, deps vmJailerUpgradeDeps) (vmJailerUpgradePlan, error) {
	plan := vmJailerUpgradePlan{VMs: make([]vmJailerUpgradeVM, 0, len(names))}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return vmJailerUpgradePlan{}, err
		}
		vm, err := planVMJailerUpgradeService(ctx, cfg, server, *services[name], unitUpgrades[name], deps)
		if err != nil {
			return vmJailerUpgradePlan{}, err
		}
		plan.VMs = append(plan.VMs, vm)
		if vm.NeedsUnitUpgrade {
			if err := addVMJailerUpgradeSummary(&plan.Summary, vm); err != nil {
				return vmJailerUpgradePlan{}, err
			}
		}
	}
	sort.Slice(plan.VMs, func(i, j int) bool { return plan.VMs[i].Service < plan.VMs[j].Service })
	sort.Strings(plan.Summary.Ready)
	sort.Strings(plan.Summary.PendingRestart)
	return plan, nil
}

func prepareVMJailerUpgradeConfig(cfg *Config) (Config, error) {
	if cfg == nil || cfg.DB == nil {
		return Config{}, fmt.Errorf("catch configuration and database are required for VM jailer upgrade")
	}
	dataRoot := filepath.Clean(strings.TrimSpace(cfg.RootDir))
	if !filepath.IsAbs(dataRoot) {
		return Config{}, fmt.Errorf("configured VM data root must be absolute for jailer upgrade: %s", cfg.RootDir)
	}
	effectiveCfg := *cfg
	if strings.TrimSpace(effectiveCfg.ServicesRoot) == "" {
		effectiveCfg.ServicesRoot = filepath.Join(dataRoot, "services")
	}
	return effectiveCfg, nil
}

func readVMJailerUpgradeServices(ctx context.Context, cfg *Config, deps vmJailerUpgradeDeps) (map[string]*db.Service, []string, map[string]bool, error) {
	dv, err := cfg.DB.Get()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read VM upgrade inventory: %w", err)
	}
	if !dv.Valid() {
		return nil, nil, nil, fmt.Errorf("read VM upgrade inventory: database is invalid")
	}
	services := dv.AsStruct().Services
	names := make([]string, 0, len(services))
	unitUpgrades := make(map[string]bool, len(services))
	for name, service := range services {
		if !isVMJailerUpgradeService(service) || service.VM.Components != nil {
			continue
		}
		preJailer, err := deps.preJailerUnit(ctx, name)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("classify effective VM unit for %q: %w", name, err)
		}
		names = append(names, name)
		unitUpgrades[name] = preJailer
	}
	sort.Strings(names)
	return services, names, unitUpgrades, nil
}

func selectedVMJailerUpgradeServices(services map[string]*db.Service, names []string) map[string]*db.Service {
	selected := make(map[string]*db.Service, len(names))
	for _, name := range names {
		if service := services[name]; service != nil {
			selected[name] = service
		}
	}
	return selected
}

func isPreJailerVMRuntimeUnit(unit vmRuntimeAdoptionLoadedUnit, service string) bool {
	preJailer, err := classifyPreJailerVMRuntimeUnit(unit, service)
	return err == nil && preJailer
}

func classifyPreJailerVMRuntimeUnit(unit vmRuntimeAdoptionLoadedUnit, service string) (bool, error) {
	if err := validateVMRuntimeAdoptionLoadedUnitEvidence(unit, service); err != nil {
		return false, err
	}
	_, flags, err := validateVMRuntimeAdoptionLoadedCommand(unit.ExecStart)
	if err != nil {
		return false, err
	}
	if flags["--service"] != service {
		return false, fmt.Errorf("loaded VM unit --service is %q, want %q", flags["--service"], service)
	}
	if err := validateVMJailerUpgradeUnitPaths(flags, "loaded VM unit", "--service-root", "--disk-path", "--config-file"); err != nil {
		return false, err
	}
	descriptorMode, err := classifyVMJailerUpgradeDescriptorMode(flags)
	if err != nil || descriptorMode {
		return false, err
	}
	explicitMode, err := classifyVMJailerUpgradeExplicitMode(flags)
	if err != nil || explicitMode {
		return false, err
	}
	if err := validateVMJailerUpgradeUnitPaths(flags, "loaded pre-jailer unit", "--firecracker"); err != nil {
		return false, err
	}
	return true, nil
}

func classifyVMJailerUpgradeDescriptorMode(flags map[string]string) (bool, error) {
	descriptorFlags := []string{"--runtime-descriptor", "--runtime-running-marker", "--runtime-trial-result"}
	descriptorCount := 0
	for _, name := range descriptorFlags {
		if _, present := flags[name]; present {
			descriptorCount++
		}
	}
	if descriptorCount == 0 {
		return false, nil
	}
	if descriptorCount != len(descriptorFlags) {
		return false, fmt.Errorf("loaded VM unit has incomplete runtime descriptor mode")
	}
	return true, validateVMJailerUpgradeUnitPaths(flags, "loaded descriptor unit", append(descriptorFlags, "--jailer-base")...)
}

func classifyVMJailerUpgradeExplicitMode(flags map[string]string) (bool, error) {
	_, hasJailer := flags["--jailer"]
	_, hasJailerBase := flags["--jailer-base"]
	if !hasJailer && !hasJailerBase {
		return false, nil
	}
	if !hasJailer || !hasJailerBase {
		return false, fmt.Errorf("loaded VM unit has incomplete explicit jailer mode")
	}
	return true, validateVMJailerUpgradeUnitPaths(flags, "loaded explicit unit", "--firecracker", "--jailer", "--jailer-base")
}

func validateVMJailerUpgradeUnitPaths(flags map[string]string, label string, names ...string) error {
	for _, name := range names {
		if _, err := cleanRequiredVMRuntimeAdoptionPath(label+" "+name, flags[name]); err != nil {
			return err
		}
	}
	return nil
}

func isVMJailerUpgradeService(service *db.Service) bool {
	return service != nil && service.ServiceType == db.ServiceTypeVM && service.VM != nil && strings.TrimSpace(service.Name) != ""
}

func planVMJailerUpgradeService(ctx context.Context, cfg *Config, server *Server, service db.Service, needsUnitUpgrade bool, deps vmJailerUpgradeDeps) (vmJailerUpgradeVM, error) {
	vm, renderCfg, err := inventoryVMJailerUpgrade(ctx, cfg, server, service, deps)
	if err != nil {
		return vmJailerUpgradeVM{}, fmt.Errorf("plan VM jailer upgrade for %q: %w", service.Name, err)
	}
	vm.NeedsUnitUpgrade = needsUnitUpgrade
	if !needsUnitUpgrade {
		return vm, nil
	}
	unit, err := deps.renderUnit(renderCfg)
	if err != nil {
		return vmJailerUpgradeVM{}, fmt.Errorf("render VM jailer upgrade unit for %q: %w", service.Name, err)
	}
	vm.UnitContent = []byte(unit)
	return vm, nil
}

func vmJailerUpgradeUnitVMs(vms []vmJailerUpgradeVM) []vmJailerUpgradeVM {
	selected := make([]vmJailerUpgradeVM, 0, len(vms))
	for _, vm := range vms {
		if vm.NeedsUnitUpgrade {
			selected = append(selected, vm)
		}
	}
	return selected
}

func addVMJailerUpgradeSummary(summary *VMJailerUpgradeSummary, vm vmJailerUpgradeVM) error {
	switch vm.Readiness {
	case vmJailerReady:
		summary.Ready = append(summary.Ready, vm.Service)
	case vmJailerPendingRestart:
		summary.PendingRestart = append(summary.PendingRestart, vm.Service)
	default:
		return fmt.Errorf("VM %q has unsupported jailer readiness %q", vm.Service, vm.Readiness)
	}
	return nil
}

type vmJailerUpgradeRuntime struct {
	disk        string
	firecracker string
	manifest    vmImageManifest
}

func inventoryVMJailerUpgrade(ctx context.Context, cfg *Config, server *Server, service db.Service, deps vmJailerUpgradeDeps) (vmJailerUpgradeVM, vmSystemdConfig, error) {
	root := filepath.Clean(strings.TrimSpace(serviceRootFromConfig(*cfg, service)))
	if !filepath.IsAbs(root) {
		return vmJailerUpgradeVM{}, vmSystemdConfig{}, fmt.Errorf("effective service root must be absolute: %s", root)
	}
	vmRuntime, err := inspectVMJailerUpgradeRuntime(service, runtime.GOARCH)
	if err != nil {
		return vmJailerUpgradeVM{}, vmSystemdConfig{}, err
	}
	readiness, err := deps.readiness(root)
	if err != nil {
		return vmJailerUpgradeVM{}, vmSystemdConfig{}, err
	}
	running, err := deps.isRunning(server, service.Name)
	if err != nil {
		return vmJailerUpgradeVM{}, vmSystemdConfig{}, fmt.Errorf("inspect VM running state: %w", err)
	}
	vm := vmJailerUpgradeVM{
		Service:      service.Name,
		Payload:      strings.TrimSpace(service.VM.Image.Payload),
		ImageVersion: vmRuntime.manifest.Version,
		Architecture: vmRuntime.manifest.Architecture,
		ServiceRoot:  root,
		Disk:         vmRuntime.disk,
		Firecracker:  vmRuntime.firecracker,
		Jailer:       filepath.Join(filepath.Dir(vmRuntime.firecracker), "jailer"),
		UnitPath:     filepath.Join(vmSystemdSystemDir, vmSystemdUnitName(service.Name)),
		Readiness:    readiness,
		Running:      running,
		Manifest:     vmRuntime.manifest,
	}
	vm.NormalizeManagedArtifacts = shouldNormalizeManagedVMJailerUpgradeArtifacts(
		cfg.RootDir,
		vm.Payload,
		vm.Firecracker,
		vm.Manifest,
		deps.localPayload,
	)
	resolvedJailer, _, err := resolveVMUpgradeJailer(ctx, vm, deps)
	if err != nil {
		return vmJailerUpgradeVM{}, vmSystemdConfig{}, err
	}
	vm.Jailer = resolvedJailer
	renderCfg := vmJailerUpgradeSystemdConfig(cfg, server, service, vm)
	return vm, renderCfg, nil
}

func inspectVMJailerUpgradeRuntime(service db.Service, hostArchitecture string) (vmJailerUpgradeRuntime, error) {
	rootFS := filepath.Clean(strings.TrimSpace(service.VM.Image.RootFS))
	if !filepath.IsAbs(rootFS) {
		return vmJailerUpgradeRuntime{}, fmt.Errorf("stored VM rootfs must be absolute: %s", rootFS)
	}
	imageDir := filepath.Dir(rootFS)
	firecracker := filepath.Join(imageDir, "firecracker")
	manifest, err := inspectVMJailerUpgradeManifest(service, imageDir, firecracker, hostArchitecture)
	if err != nil {
		return vmJailerUpgradeRuntime{}, err
	}
	if manifest.Version != strings.TrimSpace(service.VM.Image.Version) {
		return vmJailerUpgradeRuntime{}, fmt.Errorf("stored VM image version %q does not match runtime manifest version %q", service.VM.Image.Version, manifest.Version)
	}
	disk := filepath.Clean(strings.TrimSpace(service.VM.Disk.Path))
	if disk == "." || strings.TrimSpace(service.VM.Disk.Path) == "" {
		disk = rootFS
	}
	if !filepath.IsAbs(disk) {
		return vmJailerUpgradeRuntime{}, fmt.Errorf("stored VM disk must be absolute: %s", disk)
	}
	return vmJailerUpgradeRuntime{disk: disk, firecracker: firecracker, manifest: manifest}, nil
}

func inspectVMJailerUpgradeManifest(service db.Service, imageDir, firecracker, hostArchitecture string) (vmImageManifest, error) {
	manifestPath := filepath.Join(imageDir, "manifest.json")
	_, manifestErr := os.Lstat(manifestPath)
	if errors.Is(manifestErr, os.ErrNotExist) {
		if err := validateLegacyVMJailerUpgradeBundle(imageDir, firecracker); err != nil {
			return vmImageManifest{}, err
		}
		return legacyVMJailerUpgradeManifest(service, firecracker, hostArchitecture)
	}
	if manifestErr != nil {
		return vmImageManifest{}, fmt.Errorf("inspect VM image runtime manifest: %w", manifestErr)
	}
	return readValidatedVMImageRuntimeManifest(firecracker)
}

func validateLegacyVMJailerUpgradeBundle(imageDir, firecracker string) error {
	dirInfo, err := os.Lstat(imageDir)
	if err != nil {
		return fmt.Errorf("inspect legacy VM image bundle %s: %w", imageDir, err)
	}
	if !dirInfo.IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("legacy VM image bundle %s must be a directory without symlinks", imageDir)
	}
	firecrackerInfo, err := os.Lstat(firecracker)
	if err != nil {
		return fmt.Errorf("inspect legacy VM image Firecracker %s: %w", firecracker, err)
	}
	if !firecrackerInfo.Mode().IsRegular() || firecrackerInfo.Mode()&os.ModeSymlink != 0 || firecrackerInfo.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("legacy VM image Firecracker %s must be an executable regular file without symlinks", firecracker)
	}
	return nil
}

func legacyVMJailerUpgradeManifest(service db.Service, firecracker, hostArchitecture string) (vmImageManifest, error) {
	architecture, err := normalizeVMImageArchitecture(hostArchitecture)
	if err != nil {
		return vmImageManifest{}, fmt.Errorf("identify legacy VM image runtime architecture: %w", err)
	}
	version := strings.TrimSpace(service.VM.Image.Version)
	if version == "" {
		return vmImageManifest{}, fmt.Errorf("stored VM image version is required for a legacy bundle without manifest.json")
	}
	return vmImageManifest{
		Version:      version,
		Architecture: architecture,
		Firecracker:  filepath.Base(firecracker),
	}, nil
}

func vmJailerUpgradeSystemdConfig(cfg *Config, server *Server, service db.Service, vm vmJailerUpgradeVM) vmSystemdConfig {
	runDir := serviceRunDirForRoot(vm.ServiceRoot)
	return vmSystemdConfig{
		Service:          vm.Service,
		Runner:           server.catchRunnerPath(),
		DataDir:          cfg.RootDir,
		ServicesRoot:     cfg.ServicesRoot,
		ServiceRoot:      vm.ServiceRoot,
		DiskPath:         vm.Disk,
		Firecracker:      vm.Firecracker,
		Jailer:           vm.Jailer,
		JailerBase:       vmJailerBaseForDataRoot(cfg.RootDir),
		ConfigPath:       filepath.Join(runDir, "firecracker.json"),
		APISocket:        service.VM.Sockets.APISocketPath,
		ConsoleSocket:    service.VM.Console.SocketPath,
		VsockSocket:      service.VM.Sockets.VsockSocketPath,
		WorkingDirectory: vm.ServiceRoot,
	}
}

func validateVMJailerUpgradeDeps(deps vmJailerUpgradeDeps) error {
	missing := ""
	for name, present := range map[string]bool{
		"pre-jailer unit classifier": deps.preJailerUnit != nil,
		"sibling":                    deps.sibling != nil, "cached": deps.cached != nil,
		"local payload": deps.localPayload != nil, "official": deps.official != nil,
		"install": deps.install != nil, "readiness": deps.readiness != nil,
		"running state": deps.isRunning != nil, "unit renderer": deps.renderUnit != nil,
		"artifact normalizer":  deps.normalizeArtifacts != nil,
		"next-start validator": deps.validateNextStart != nil,
	} {
		if !present {
			missing = name
			break
		}
	}
	if missing != "" {
		return fmt.Errorf("VM jailer upgrade dependency %s is required", missing)
	}
	return nil
}

func defaultVMJailerUpgradeDeps() vmJailerUpgradeDeps {
	return vmJailerUpgradeDeps{
		preJailerUnit: func(ctx context.Context, service string) (bool, error) {
			unit, err := loadEffectiveVMRuntimeAdoptionUnit(ctx, vmSystemdUnitName(service))
			if err != nil {
				return false, err
			}
			return classifyPreJailerVMRuntimeUnit(unit, service)
		},
		sibling:   resolveSiblingVMUpgradeJailer,
		install:   installUpgradeJailer,
		readiness: vmJailerReadinessForRoot,
		isRunning: func(server *Server, service string) (bool, error) {
			return server.IsServiceRunning(service)
		},
		renderUnit:            renderVMSystemdUnit,
		ensureRuntimeIdentity: ensureVMRuntimeIdentity,
		normalizeArtifacts:    normalizeManagedVMJailerUpgradeArtifacts,
		validateNextStart:     validateVMJailerUpgradeNextStart,
		renameAt:              unix.Renameat,
		exchangeAt:            exchangeVMJailerUnitNamesAt,
		renameNoReplaceAt:     renameVMJailerUnitNameNoReplaceAt,
		restoreUnitAt:         restoreVMUnitAt,
		unlinkAt:              unix.Unlinkat,
		systemctl:             runVMSystemctl,
		unitUID:               0,
		unitGID:               0,
	}
}

func completeVMJailerUpgradeTransactionDeps(deps vmJailerUpgradeDeps) vmJailerUpgradeDeps {
	defaults := defaultVMJailerUpgradeDeps()
	if deps.renameAt == nil {
		deps.renameAt = defaults.renameAt
	}
	if deps.exchangeAt == nil {
		deps.exchangeAt = defaults.exchangeAt
	}
	if deps.renameNoReplaceAt == nil {
		deps.renameNoReplaceAt = defaults.renameNoReplaceAt
	}
	if deps.restoreUnitAt == nil {
		deps.restoreUnitAt = defaults.restoreUnitAt
	}
	if deps.unlinkAt == nil {
		deps.unlinkAt = defaults.unlinkAt
	}
	if deps.systemctl == nil {
		deps.systemctl = defaults.systemctl
	}
	return deps
}

func completeVMJailerUpgradeDeps(cfg *Config, deps vmJailerUpgradeDeps) vmJailerUpgradeDeps {
	deps = completeVMJailerUpgradeRuntimeDeps(deps)
	return completeVMJailerUpgradeSourceDeps(cfg, deps)
}

func completeVMJailerUpgradeRuntimeDeps(deps vmJailerUpgradeDeps) vmJailerUpgradeDeps {
	defaults := defaultVMJailerUpgradeDeps()
	if deps.preJailerUnit == nil {
		deps.preJailerUnit = defaults.preJailerUnit
	}
	if deps.sibling == nil {
		deps.sibling = defaults.sibling
	}
	if deps.install == nil {
		deps.install = defaults.install
	}
	if deps.readiness == nil {
		deps.readiness = defaults.readiness
	}
	if deps.isRunning == nil {
		deps.isRunning = defaults.isRunning
	}
	if deps.renderUnit == nil {
		deps.renderUnit = defaults.renderUnit
	}
	if deps.ensureRuntimeIdentity == nil {
		deps.ensureRuntimeIdentity = defaults.ensureRuntimeIdentity
	}
	if deps.normalizeArtifacts == nil {
		deps.normalizeArtifacts = defaults.normalizeArtifacts
	}
	if deps.validateNextStart == nil {
		deps.validateNextStart = defaults.validateNextStart
	}
	return deps
}

func completeVMJailerUpgradeSourceDeps(cfg *Config, deps vmJailerUpgradeDeps) vmJailerUpgradeDeps {
	cacheRoot := filepath.Join(filepath.Clean(strings.TrimSpace(cfg.RootDir)), "vm-images")
	cache := vmImageCache{Root: cacheRoot}
	runtimeCache := vmRuntimeCache{Root: filepath.Join(filepath.Clean(strings.TrimSpace(cfg.RootDir)), "vm-runtimes")}
	if deps.cached == nil {
		deps.cached = func(ctx context.Context, vm vmJailerUpgradeVM) (vmJailerCandidate, bool, error) {
			return cachedVMUpgradeJailerCandidate(ctx, vm, cacheRoot, validateVMJailerRuntimePair)
		}
	}
	if deps.localPayload == nil {
		deps.localPayload = func(payload string) bool {
			payload = strings.TrimSpace(payload)
			if !strings.HasPrefix(payload, vmImagePayloadPrefix) {
				return true
			}
			name := strings.TrimPrefix(payload, vmImagePayloadPrefix)
			exists, err := localVMImageRefExists(cacheRoot, name)
			return err != nil || exists
		}
	}
	if deps.official == nil {
		deps.official = func(ctx context.Context, vm vmJailerUpgradeVM) (vmJailerCandidate, error) {
			return fetchOfficialVMUpgradeJailer(
				ctx,
				vm,
				cache,
				runtimeCache.FetchCatalog,
				runtimeCache.Ensure,
				officialVMUpgradeFirecrackerVersion,
				validateVMJailerRuntimePair,
			)
		}
	}
	return deps
}

func resolveSiblingVMUpgradeJailer(ctx context.Context, vm vmJailerUpgradeVM) (string, bool, error) {
	path := filepath.Join(filepath.Dir(filepath.Clean(vm.Firecracker)), "jailer")
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("inspect sibling VM jailer for %q: %w", vm.Service, err)
	}
	if err := validateVMJailerRuntimePair(ctx, vm.Firecracker, path); err != nil {
		return "", true, fmt.Errorf("validate sibling VM jailer for %q: %w", vm.Service, err)
	}
	return path, true, nil
}

func cachedVMUpgradeJailerCandidate(ctx context.Context, vm vmJailerUpgradeVM, cacheRoot string, validatePair func(context.Context, string, string) error) (vmJailerCandidate, bool, error) {
	targetArchitecture, entries, err := cachedVMUpgradeJailerInputs(vm, cacheRoot, validatePair)
	if err != nil {
		return vmJailerCandidate{}, false, err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return vmJailerCandidate{}, false, err
		}
		manifest, dir, ok, err := cachedVMImageManifestFromEntry(cacheRoot, entry)
		if err != nil {
			return vmJailerCandidate{}, false, err
		}
		if !ok || strings.TrimSpace(manifest.Jailer) == "" {
			continue
		}
		candidate, err := validateCachedVMUpgradeJailerCandidate(ctx, vm, dir, manifest, targetArchitecture, validatePair)
		if err != nil {
			if errors.Is(err, errVMJailerUpgradeIncompatibleCacheCandidate) {
				continue
			}
			return vmJailerCandidate{}, false, err
		}
		return candidate, true, nil
	}
	return vmJailerCandidate{}, false, nil
}

func cachedVMUpgradeJailerInputs(vm vmJailerUpgradeVM, cacheRoot string, validatePair func(context.Context, string, string) error) (string, []os.DirEntry, error) {
	if validatePair == nil {
		return "", nil, fmt.Errorf("VM jailer runtime-pair validator is required")
	}
	targetArchitecture, err := normalizeVMImageArchitecture(vm.Architecture)
	if err != nil {
		return "", nil, err
	}
	entries, err := readVMImageCacheEntries(cacheRoot)
	if err != nil {
		return "", nil, err
	}
	return targetArchitecture, entries, nil
}

func validateCachedVMUpgradeJailerCandidate(
	ctx context.Context,
	vm vmJailerUpgradeVM,
	dir string,
	manifest vmImageManifest,
	targetArchitecture string,
	validatePair func(context.Context, string, string) error,
) (vmJailerCandidate, error) {
	architecture, err := normalizeVMImageArchitecture(manifest.Architecture)
	if err != nil {
		return vmJailerCandidate{}, fmt.Errorf("cached VM jailer %s architecture: %w", dir, err)
	}
	if err := classifyCachedVMUpgradeJailerArchitecture(architecture, targetArchitecture); err != nil {
		return vmJailerCandidate{}, err
	}
	firecracker := filepath.Join(dir, manifest.Firecracker)
	jailer := filepath.Join(dir, manifest.Jailer)
	if err := verifyVMImageArtifactChecksum(firecracker, manifest.Firecracker, manifest.Checksums[manifest.Firecracker]); err != nil {
		return vmJailerCandidate{}, fmt.Errorf("verify cached VM Firecracker: %w", err)
	}
	if err := verifyVMImageArtifactChecksum(jailer, manifest.Jailer, manifest.Checksums[manifest.Jailer]); err != nil {
		return vmJailerCandidate{}, fmt.Errorf("verify cached VM jailer: %w", err)
	}
	if err := validatePair(ctx, firecracker, jailer); err != nil {
		return vmJailerCandidate{}, fmt.Errorf("validate cached Firecracker/jailer pair: %w", err)
	}
	if err := validatePair(ctx, vm.Firecracker, jailer); err != nil {
		if errors.Is(err, errVMJailerRuntimeVersionMismatch) && strings.TrimSpace(manifest.Version) != strings.TrimSpace(vm.ImageVersion) {
			return vmJailerCandidate{}, fmt.Errorf("%w: validate cached jailer against target Firecracker: %w", errVMJailerUpgradeIncompatibleCacheCandidate, err)
		}
		return vmJailerCandidate{}, fmt.Errorf("validate cached jailer against target Firecracker: %w", err)
	}
	return vmJailerCandidate{
		Path:         jailer,
		ArtifactName: manifest.Jailer,
		SHA256:       manifest.Checksums[manifest.Jailer],
		Architecture: architecture,
	}, nil
}

func classifyCachedVMUpgradeJailerArchitecture(candidateArchitecture, targetArchitecture string) error {
	if candidateArchitecture == targetArchitecture {
		return nil
	}
	return fmt.Errorf("%w: cached VM jailer architecture %q does not match target architecture %q", errVMJailerUpgradeIncompatibleCacheCandidate, candidateArchitecture, targetArchitecture)
}

func fetchOfficialVMUpgradeJailer(
	ctx context.Context,
	vm vmJailerUpgradeVM,
	imageCache vmImageCache,
	fetchRuntimeCatalog func(context.Context) (vmRuntimeCatalog, error),
	ensureRuntime func(context.Context, vmRuntimeCatalogRef) (db.VMRuntimeArtifactConfig, error),
	probeFirecrackerVersion func(context.Context, string) (string, error),
	validatePair func(context.Context, string, string) error,
) (vmJailerCandidate, error) {
	if err := validateOfficialVMUpgradeJailerResolverDeps(fetchRuntimeCatalog, ensureRuntime, probeFirecrackerVersion, validatePair); err != nil {
		return vmJailerCandidate{}, err
	}
	_, targetArchitecture, err := officialVMUpgradeJailerFamily(ctx, vm, imageCache)
	if err != nil {
		return vmJailerCandidate{}, err
	}
	upstreamVersion, err := probeFirecrackerVersion(ctx, vm.Firecracker)
	if err != nil {
		return vmJailerCandidate{}, fmt.Errorf("read target Firecracker version: %w", err)
	}
	catalog, err := fetchRuntimeCatalog(ctx)
	if err != nil {
		return vmJailerCandidate{}, fmt.Errorf("fetch official VM runtime catalog: %w", err)
	}
	ref, ok := catalog.RuntimeForUpstreamVersion(targetArchitecture, upstreamVersion)
	if !ok {
		return vmJailerCandidate{}, fmt.Errorf("official VM runtime catalog has no non-revoked runtime for Firecracker %s", upstreamVersion)
	}
	artifact, err := ensureRuntime(ctx, ref)
	if err != nil {
		return vmJailerCandidate{}, fmt.Errorf("cache official VM runtime %s: %w", ref.RuntimeID, err)
	}
	if err := validatePair(ctx, vm.Firecracker, artifact.Jailer); err != nil {
		return vmJailerCandidate{}, fmt.Errorf("validate official jailer against target Firecracker: %w", err)
	}
	return vmJailerCandidate{
		Path:         artifact.Jailer,
		ArtifactName: "jailer",
		SHA256:       artifact.JailerSHA256,
		Architecture: targetArchitecture,
	}, nil
}

func validateOfficialVMUpgradeJailerResolverDeps(
	fetchRuntimeCatalog func(context.Context) (vmRuntimeCatalog, error),
	ensureRuntime func(context.Context, vmRuntimeCatalogRef) (db.VMRuntimeArtifactConfig, error),
	probeFirecrackerVersion func(context.Context, string) (string, error),
	validatePair func(context.Context, string, string) error,
) error {
	switch {
	case fetchRuntimeCatalog == nil:
		return fmt.Errorf("official VM runtime catalog fetcher is required")
	case ensureRuntime == nil:
		return fmt.Errorf("official VM runtime cache is required")
	case probeFirecrackerVersion == nil:
		return fmt.Errorf("target Firecracker version probe is required")
	case validatePair == nil:
		return fmt.Errorf("VM jailer runtime-pair validator is required")
	default:
		return nil
	}
}

func officialVMUpgradeFirecrackerVersion(ctx context.Context, firecracker string) (string, error) {
	if err := vmJailValidateTrustedInput(firecracker); err != nil {
		return "", err
	}
	output, err := probeVMRuntimeVersion(ctx, firecracker)
	if err != nil {
		return "", err
	}
	version := vmJailerVersion(output)
	if version == "" {
		return "", fmt.Errorf("unrecognized Firecracker version output %q", strings.TrimSpace(output))
	}
	return "v" + version, nil
}

func officialVMUpgradeJailerFamily(ctx context.Context, vm vmJailerUpgradeVM, cache vmImageCache) (vmImageCatalogImage, string, error) {
	catalog, err := cache.FetchCatalog(ctx)
	if err != nil {
		return vmImageCatalogImage{}, "", err
	}
	family, ok := catalog.ImageByPayload(vm.Payload)
	if !ok {
		return vmImageCatalogImage{}, "", fmt.Errorf("%w: %s", errVMJailerUpgradeUnknownPayload, vm.Payload)
	}
	if err := validateTrustedVMImageRepoURL(family.ManifestURL, "manifest"); err != nil {
		return vmImageCatalogImage{}, "", err
	}
	targetArchitecture, err := normalizeVMImageArchitecture(vm.Architecture)
	if err != nil {
		return vmImageCatalogImage{}, "", err
	}
	familyArchitecture, err := normalizeVMImageArchitecture(family.Architecture)
	if err != nil {
		return vmImageCatalogImage{}, "", err
	}
	if familyArchitecture != targetArchitecture {
		return vmImageCatalogImage{}, "", fmt.Errorf("official VM image architecture %q does not match target architecture %q", familyArchitecture, targetArchitecture)
	}
	return family, targetArchitecture, nil
}

type vmJailerUpgradeInstallOps struct {
	copy               func(io.Writer, io.Reader) (int64, error)
	fchown             func(*os.File, int, int) error
	fchmod             func(*os.File, os.FileMode) error
	publishNoReplace   func(*os.File, *os.File, string, string) error
	verifyFileChecksum func(*os.File, string, string) error
	validatePair       func(context.Context, string, string) error
	beforePublish      func(*os.File, string) error
	afterPublish       func(*os.File) error
	trustedDirUID      uint32
}

func defaultVMJailerUpgradeInstallOps() vmJailerUpgradeInstallOps {
	return vmJailerUpgradeInstallOps{
		copy: io.Copy,
		fchown: func(file *os.File, uid, gid int) error {
			return file.Chown(uid, gid)
		},
		fchmod: func(file *os.File, mode os.FileMode) error {
			return file.Chmod(mode)
		},
		publishNoReplace:   publishOpenVMJailerNoReplace,
		verifyFileChecksum: verifyOpenVMImageArtifactChecksum,
		validatePair:       validateVMJailerRuntimePair,
		trustedDirUID:      0,
	}
}

func installUpgradeJailer(ctx context.Context, vm vmJailerUpgradeVM, candidate vmJailerCandidate) (string, error) {
	return installUpgradeJailerWithOps(ctx, vm, candidate, defaultVMJailerUpgradeInstallOps())
}

func installUpgradeJailerWithOps(ctx context.Context, vm vmJailerUpgradeVM, candidate vmJailerCandidate, ops vmJailerUpgradeInstallOps) (_ string, retErr error) {
	target, err := validateUpgradeJailerInstallInput(ctx, vm, candidate, ops)
	if err != nil {
		return "", err
	}
	targetDir := filepath.Dir(target)
	return withLockedVMJailerUpgradeDir(ctx, targetDir, ops.trustedDirUID, func(dir *os.File, dirID vmJailerFileIdentity) (string, error) {
		return installUpgradeJailerInLockedDir(ctx, vm, candidate, target, targetDir, dir, dirID, ops)
	})
}

func withLockedVMJailerUpgradeDir(
	ctx context.Context,
	targetDir string,
	trustedUID uint32,
	fn func(*os.File, vmJailerFileIdentity) (string, error),
) (_ string, retErr error) {
	dir, dirID, err := openValidatedVMJailerUpgradeDir(targetDir, trustedUID)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := dir.Close(); closeErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("close VM jailer target directory: %w", closeErr))
		}
	}()
	if err := acquireVMJailerUpgradeDirLock(ctx, dir); err != nil {
		return "", err
	}
	defer func() {
		if unlockErr := releaseVMJailerUpgradeDirLock(dir); unlockErr != nil {
			retErr = errors.Join(retErr, unlockErr)
		}
	}()
	return fn(dir, dirID)
}

func installUpgradeJailerInLockedDir(
	ctx context.Context,
	vm vmJailerUpgradeVM,
	candidate vmJailerCandidate,
	target string,
	targetDir string,
	dir *os.File,
	dirID vmJailerFileIdentity,
	ops vmJailerUpgradeInstallOps,
) (_ string, retErr error) {
	temp, tempName, tempID, err := prepareUpgradeJailerTempAt(dir, candidate, ops)
	if err != nil {
		return "", err
	}
	tempOpen := true
	defer func() {
		if !tempOpen {
			return
		}
		if closeErr := temp.Close(); closeErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("close staged VM jailer: %w", closeErr))
		}
	}()
	tempPublished := false
	defer func() {
		if tempPublished {
			return
		}
		if cleanupErr := unlinkVMJailerNameIfIdentity(dir, tempName, tempID); cleanupErr != nil {
			retErr = errors.Join(retErr, cleanupErr)
		}
	}()
	installedID, err := publishUpgradeJailerNoReplace(dir, temp, tempName, filepath.Base(target), ops)
	if err != nil {
		return "", err
	}
	tempPublished = true
	closeErr := temp.Close()
	tempOpen = false
	if closeErr != nil {
		cause := fmt.Errorf("close published VM jailer staging descriptor: %w", closeErr)
		return "", cleanupFailedUpgradeJailerPublish(dir, filepath.Base(target), installedID, cause)
	}
	if _, err := validatePublishedUpgradeJailer(ctx, vm, candidate, target, targetDir, dir, dirID, installedID, ops); err != nil {
		return "", err
	}
	return target, nil
}

func validateUpgradeJailerInstallInput(ctx context.Context, vm vmJailerUpgradeVM, candidate vmJailerCandidate, ops vmJailerUpgradeInstallOps) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	targetArchitecture, err := normalizeVMImageArchitecture(vm.Architecture)
	if err != nil {
		return "", err
	}
	candidateArchitecture, err := normalizeVMImageArchitecture(candidate.Architecture)
	if err != nil {
		return "", err
	}
	if candidateArchitecture != targetArchitecture {
		return "", fmt.Errorf("VM jailer candidate architecture %q does not match target architecture %q", candidateArchitecture, targetArchitecture)
	}
	if err := validateVMImageArtifactName(candidate.ArtifactName); err != nil {
		return "", err
	}
	if err := verifyVMImageArtifactChecksum(candidate.Path, candidate.ArtifactName, candidate.SHA256); err != nil {
		return "", err
	}
	if ops.validatePair == nil {
		return "", fmt.Errorf("VM jailer runtime-pair validator is required")
	}
	if err := ops.validatePair(ctx, vm.Firecracker, candidate.Path); err != nil {
		return "", fmt.Errorf("validate VM jailer candidate against target Firecracker: %w", err)
	}
	return vmUpgradeJailerTarget(vm)
}

func vmUpgradeJailerTarget(vm vmJailerUpgradeVM) (string, error) {
	target := filepath.Join(filepath.Dir(filepath.Clean(vm.Firecracker)), "jailer")
	if strings.TrimSpace(vm.Jailer) != "" && filepath.Clean(vm.Jailer) != target {
		return "", fmt.Errorf("VM jailer target %s is not beside Firecracker %s", vm.Jailer, vm.Firecracker)
	}
	return target, nil
}

type vmJailerFileIdentity struct {
	dev uint64
	ino uint64
}

func openValidatedVMJailerUpgradeDir(path string, trustedUID uint32) (*os.File, vmJailerFileIdentity, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, vmJailerFileIdentity{}, fmt.Errorf("open VM jailer target directory without following symlinks: %w", err)
	}
	dir := os.NewFile(uintptr(fd), path)
	if dir == nil {
		_ = unix.Close(fd)
		return nil, vmJailerFileIdentity{}, fmt.Errorf("bind VM jailer target directory file descriptor")
	}
	id, stat, err := vmJailerFileIdentityForFile(dir)
	if err != nil {
		return nil, vmJailerFileIdentity{}, closeVMJailerFileOnError(dir, err)
	}
	mode := uint32(stat.Mode)
	if mode&unix.S_IFMT != unix.S_IFDIR {
		return nil, vmJailerFileIdentity{}, closeVMJailerFileOnError(dir, fmt.Errorf("VM jailer target is not a directory"))
	}
	if stat.Uid != trustedUID {
		return nil, vmJailerFileIdentity{}, closeVMJailerFileOnError(dir, fmt.Errorf("VM jailer target directory owner is %d, want %d", stat.Uid, trustedUID))
	}
	if mode&0o022 != 0 {
		return nil, vmJailerFileIdentity{}, closeVMJailerFileOnError(dir, fmt.Errorf("VM jailer target directory is writable by group or others: mode %o", mode&0o7777))
	}
	return dir, id, nil
}

func prepareUpgradeJailerTempAt(dir *os.File, candidate vmJailerCandidate, ops vmJailerUpgradeInstallOps) (*os.File, string, vmJailerFileIdentity, error) {
	source, err := os.Open(candidate.Path)
	if err != nil {
		return nil, "", vmJailerFileIdentity{}, fmt.Errorf("open VM jailer candidate: %w", err)
	}
	defer func() { _ = source.Close() }()
	temp, tempName, err := createVMJailerTempAt(dir)
	if err != nil {
		return nil, "", vmJailerFileIdentity{}, err
	}
	tempID, _, err := vmJailerFileIdentityForFile(temp)
	if err != nil {
		cause := errors.Join(err, fmt.Errorf("leave safely named temporary VM jailer %q because its inode identity is unavailable", tempName))
		if closeErr := temp.Close(); closeErr != nil {
			cause = errors.Join(cause, fmt.Errorf("close unidentified temporary VM jailer: %w", closeErr))
		}
		return nil, "", vmJailerFileIdentity{}, cause
	}
	if err := writeAndValidateUpgradeJailerTemp(source, temp, tempID, candidate, ops); err != nil {
		return cleanupFailedUpgradeJailerTemp(dir, temp, tempName, tempID, err)
	}
	return temp, tempName, tempID, nil
}

func writeAndValidateUpgradeJailerTemp(source, temp *os.File, tempID vmJailerFileIdentity, candidate vmJailerCandidate, ops vmJailerUpgradeInstallOps) error {
	if _, err := ops.copy(temp, source); err != nil {
		return fmt.Errorf("copy VM jailer candidate: %w", err)
	}
	if err := secureUpgradeJailerTemp(temp, ops); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("sync temporary VM jailer: %w", err)
	}
	if err := ops.verifyFileChecksum(temp, candidate.ArtifactName, candidate.SHA256); err != nil {
		return err
	}
	id, stat, err := vmJailerFileIdentityForFile(temp)
	if err != nil {
		return err
	}
	if uint32(stat.Mode)&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("staged VM jailer is not a regular file")
	}
	if id != tempID {
		return fmt.Errorf("staged VM jailer file descriptor inode changed")
	}
	return nil
}

func cleanupFailedUpgradeJailerTemp(dir, temp *os.File, tempName string, tempID vmJailerFileIdentity, cause error) (*os.File, string, vmJailerFileIdentity, error) {
	if cleanupErr := unlinkVMJailerNameIfIdentity(dir, tempName, tempID); cleanupErr != nil {
		cause = errors.Join(cause, cleanupErr)
	}
	if closeErr := temp.Close(); closeErr != nil {
		cause = errors.Join(cause, fmt.Errorf("close failed staged VM jailer: %w", closeErr))
	}
	return nil, "", vmJailerFileIdentity{}, cause
}

func secureUpgradeJailerTemp(temp *os.File, ops vmJailerUpgradeInstallOps) error {
	if err := ops.fchown(temp, 0, 0); err != nil {
		return fmt.Errorf("chown temporary VM jailer: %w", err)
	}
	if err := ops.fchmod(temp, 0o755); err != nil {
		return fmt.Errorf("chmod temporary VM jailer: %w", err)
	}
	return nil
}

func createVMJailerTempAt(dir *os.File) (*os.File, string, error) {
	for range 128 {
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, "", fmt.Errorf("generate temporary VM jailer name: %w", err)
		}
		name := ".jailer.tmp-" + hex.EncodeToString(random[:])
		fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return nil, "", fmt.Errorf("create temporary VM jailer relative to target directory: %w", err)
		}
		file := os.NewFile(uintptr(fd), name)
		if file == nil {
			return nil, "", cleanupUnboundVMJailerTemp(dir, fd, name, fmt.Errorf("bind temporary VM jailer file descriptor"))
		}
		return file, name, nil
	}
	return nil, "", fmt.Errorf("create temporary VM jailer: exhausted unique names")
}

func cleanupUnboundVMJailerTemp(dir *os.File, fd int, name string, cause error) error {
	var stat unix.Stat_t
	statErr := unix.Fstat(fd, &stat)
	if closeErr := unix.Close(fd); closeErr != nil {
		cause = errors.Join(cause, fmt.Errorf("close unbound temporary VM jailer: %w", closeErr))
	}
	if statErr != nil {
		return errors.Join(cause, fmt.Errorf("leave safely named temporary VM jailer %q because its inode identity is unavailable: %w", name, statErr))
	}
	id := vmJailerFileIdentity{dev: uint64(stat.Dev), ino: uint64(stat.Ino)}
	if cleanupErr := unlinkVMJailerNameIfIdentity(dir, name, id); cleanupErr != nil {
		return errors.Join(cause, cleanupErr)
	}
	return cause
}

func publishUpgradeJailerNoReplace(dir, temp *os.File, tempName, targetName string, ops vmJailerUpgradeInstallOps) (vmJailerFileIdentity, error) {
	tempID, _, err := vmJailerFileIdentityForFile(temp)
	if err != nil {
		return vmJailerFileIdentity{}, err
	}
	if ops.beforePublish != nil {
		if err := ops.beforePublish(dir, tempName); err != nil {
			return vmJailerFileIdentity{}, fmt.Errorf("before VM jailer publish: %w", err)
		}
	}
	if err := validateUpgradeJailerTempName(dir, tempName, tempID); err != nil {
		return vmJailerFileIdentity{}, err
	}
	if err := ops.publishNoReplace(dir, temp, tempName, targetName); err != nil {
		return vmJailerFileIdentity{}, formatVMJailerPublishError(err)
	}
	installedID, err := finishUpgradeJailerPublish(dir, tempName, targetName, tempID)
	if err != nil {
		return vmJailerFileIdentity{}, cleanupFailedUpgradeJailerPublish(dir, targetName, tempID, err)
	}
	return installedID, nil
}

func validateUpgradeJailerTempName(dir *os.File, tempName string, tempID vmJailerFileIdentity) error {
	boundNameID, _, err := vmJailerNameIdentityAt(dir, tempName)
	if err != nil {
		return fmt.Errorf("inspect temporary VM jailer before publish: %w", err)
	}
	if boundNameID != tempID {
		return fmt.Errorf("temporary VM jailer inode changed before publish")
	}
	return nil
}

func formatVMJailerPublishError(err error) error {
	if errors.Is(err, unix.EEXIST) {
		return fmt.Errorf("publish VM jailer without replacing target: target already exists: %w", err)
	}
	return fmt.Errorf("publish VM jailer without replacing target: %w", err)
}

func finishUpgradeJailerPublish(dir *os.File, tempName, targetName string, tempID vmJailerFileIdentity) (vmJailerFileIdentity, error) {
	installedID, _, err := vmJailerNameIdentityAt(dir, targetName)
	if err != nil {
		return vmJailerFileIdentity{}, fmt.Errorf("inspect published VM jailer: %w", err)
	}
	if installedID != tempID {
		return vmJailerFileIdentity{}, fmt.Errorf("published VM jailer inode does not match staged inode")
	}
	if err := unlinkVMJailerNameIfIdentity(dir, tempName, tempID); err != nil {
		return vmJailerFileIdentity{}, fmt.Errorf("remove temporary VM jailer name after publish: %w", err)
	}
	if err := dir.Sync(); err != nil {
		return vmJailerFileIdentity{}, fmt.Errorf("sync VM jailer target directory: %w", err)
	}
	return installedID, nil
}

func cleanupFailedUpgradeJailerPublish(dir *os.File, targetName string, installedID vmJailerFileIdentity, cause error) error {
	if cleanupErr := unlinkVMJailerNameIfIdentity(dir, targetName, installedID); cleanupErr != nil {
		return errors.Join(cause, cleanupErr)
	}
	return cause
}

func validatePublishedUpgradeJailer(
	ctx context.Context,
	vm vmJailerUpgradeVM,
	candidate vmJailerCandidate,
	target string,
	targetDir string,
	dir *os.File,
	dirID vmJailerFileIdentity,
	installedID vmJailerFileIdentity,
	ops vmJailerUpgradeInstallOps,
) (string, error) {
	if err := inspectPublishedUpgradeJailer(ctx, vm, candidate, target, targetDir, dir, dirID, installedID, ops); err != nil {
		return "", cleanupFailedUpgradeJailerPublish(dir, filepath.Base(target), installedID, err)
	}
	return target, nil
}

func inspectPublishedUpgradeJailer(
	ctx context.Context,
	vm vmJailerUpgradeVM,
	candidate vmJailerCandidate,
	target string,
	targetDir string,
	dir *os.File,
	dirID vmJailerFileIdentity,
	installedID vmJailerFileIdentity,
	ops vmJailerUpgradeInstallOps,
) error {
	if err := runVMJailerAfterPublishHook(dir, ops.afterPublish); err != nil {
		return err
	}
	if err := validateVMJailerUpgradeDirPathIdentity(targetDir, dirID, ops.trustedDirUID); err != nil {
		return err
	}
	installed, err := openVMJailerNameAt(dir, filepath.Base(target), installedID)
	if err != nil {
		return err
	}
	if err := ops.verifyFileChecksum(installed, candidate.ArtifactName, candidate.SHA256); err != nil {
		return closeVMJailerFileOnError(installed, err)
	}
	if err := installed.Close(); err != nil {
		return fmt.Errorf("close published VM jailer: %w", err)
	}
	if err := ops.validatePair(ctx, vm.Firecracker, target); err != nil {
		return fmt.Errorf("validate installed VM jailer: %w", err)
	}
	if err := validateVMJailerUpgradeDirPathIdentity(targetDir, dirID, ops.trustedDirUID); err != nil {
		return err
	}
	currentID, _, err := vmJailerNameIdentityAt(dir, filepath.Base(target))
	if err != nil {
		return fmt.Errorf("inspect installed VM jailer after validation: %w", err)
	}
	if currentID != installedID {
		return fmt.Errorf("installed VM jailer inode changed during validation")
	}
	return nil
}

func runVMJailerAfterPublishHook(dir *os.File, hook func(*os.File) error) error {
	if hook == nil {
		return nil
	}
	if err := hook(dir); err != nil {
		return fmt.Errorf("after VM jailer publish: %w", err)
	}
	return nil
}

func validateVMJailerUpgradeDirPathIdentity(path string, want vmJailerFileIdentity, trustedUID uint32) error {
	dir, got, err := openValidatedVMJailerUpgradeDir(path, trustedUID)
	if err != nil {
		return fmt.Errorf("target directory changed after VM jailer publish: %w", err)
	}
	if err := dir.Close(); err != nil {
		return fmt.Errorf("close revalidated VM jailer target directory: %w", err)
	}
	if got != want {
		return fmt.Errorf("target directory changed after VM jailer publish")
	}
	return nil
}

func openVMJailerNameAt(dir *os.File, name string, want vmJailerFileIdentity) (*os.File, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open published VM jailer relative to target directory: %w", err)
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("bind published VM jailer file descriptor")
	}
	got, stat, err := vmJailerFileIdentityForFile(file)
	if err != nil {
		return nil, closeVMJailerFileOnError(file, err)
	}
	if got != want || uint32(stat.Mode)&unix.S_IFMT != unix.S_IFREG {
		return nil, closeVMJailerFileOnError(file, fmt.Errorf("published VM jailer inode changed before validation"))
	}
	return file, nil
}

func vmJailerFileIdentityForFile(file *os.File) (vmJailerFileIdentity, unix.Stat_t, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return vmJailerFileIdentity{}, unix.Stat_t{}, fmt.Errorf("inspect VM jailer file descriptor: %w", err)
	}
	return vmJailerFileIdentity{dev: uint64(stat.Dev), ino: uint64(stat.Ino)}, stat, nil
}

func vmJailerNameIdentityAt(dir *os.File, name string) (vmJailerFileIdentity, unix.Stat_t, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(int(dir.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return vmJailerFileIdentity{}, unix.Stat_t{}, err
	}
	return vmJailerFileIdentity{dev: uint64(stat.Dev), ino: uint64(stat.Ino)}, stat, nil
}

func unlinkVMJailerNameIfIdentity(dir *os.File, name string, want vmJailerFileIdentity) error {
	return unlinkVMJailerNameIfIdentityWith(dir, name, want, unix.Unlinkat)
}

func unlinkVMJailerNameIfIdentityWith(dir *os.File, name string, want vmJailerFileIdentity, unlinkAt func(int, string, int) error) error {
	got, _, err := vmJailerNameIdentityAt(dir, name)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect VM jailer cleanup target %q: %w", name, err)
	}
	if got != want {
		return fmt.Errorf("refuse to remove VM jailer cleanup target %q: inode changed", name)
	}
	if err := unlinkAt(int(dir.Fd()), name, 0); err != nil {
		return fmt.Errorf("remove VM jailer cleanup target %q: %w", name, err)
	}
	return nil
}

func verifyOpenVMImageArtifactChecksum(file *os.File, artifactName, want string) error {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek VM image artifact %q for checksum: %w", artifactName, err)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("verify downloaded VM image artifact %q: %w", artifactName, err)
	}
	got := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("VM image artifact %q checksum mismatch: got %s, want %s", artifactName, got, want)
	}
	return nil
}

func closeVMJailerFileOnError(file *os.File, cause error) error {
	if err := file.Close(); err != nil {
		return errors.Join(cause, fmt.Errorf("close VM jailer file descriptor: %w", err))
	}
	return cause
}
