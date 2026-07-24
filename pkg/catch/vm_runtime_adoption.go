// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yeetrun/yeet/pkg/db"
	"golang.org/x/sys/unix"
)

const (
	vmRuntimeAdoptionReceiptFileName            = "install-receipt.json"
	vmRuntimeAdoptionReceiptSchema              = "yeet.vm.image-install-receipt"
	vmRuntimeAdoptionReceiptSchemaVersion       = 1
	vmRuntimeAdoptionPreconditionSchema         = "yeet.vm.runtime-adoption-precondition"
	vmRuntimeAdoptionPreconditionVersion        = 1
	vmRuntimeAdoptionMetadataMaxBytes     int64 = 1 << 20
)

type vmRuntimeAdoptionClassification string

const (
	vmRuntimeAdoptionAlreadyAdopted vmRuntimeAdoptionClassification = "already-adopted"
	vmRuntimeAdoptionOfficialLegacy vmRuntimeAdoptionClassification = "official-legacy"
	vmRuntimeAdoptionLocalLegacy    vmRuntimeAdoptionClassification = "local-legacy"
	vmRuntimeAdoptionCustomLegacy   vmRuntimeAdoptionClassification = "custom-legacy"
	vmRuntimeAdoptionBlocked        vmRuntimeAdoptionClassification = "adoption-blocked"
)

// vmRuntimeAdoptionReceipt is intentionally strict and small. A future image
// installer may publish it only after it has verified both downloaded metadata
// objects and the prepared, launchable rootfs. Old image bundles do not have a
// receipt and are therefore measured as custom legacy artifacts.
type vmRuntimeAdoptionReceipt struct {
	Schema               string `json:"schema"`
	SchemaVersion        int    `json:"schemaVersion"`
	Payload              string `json:"payload"`
	Version              string `json:"version"`
	CatalogURL           string `json:"catalogURL"`
	CatalogSHA256        string `json:"catalogSHA256"`
	ManifestURL          string `json:"manifestURL"`
	ManifestSHA256       string `json:"manifestSHA256"`
	PreparedRootFS       string `json:"preparedRootFS"`
	PreparedRootFSSHA256 string `json:"preparedRootFSSHA256"`
}

type vmRuntimeAdoptionUnitFragment struct {
	Path     string
	Evidence vmRuntimeAdoptionFileEvidence
}

// vmRuntimeAdoptionLoadedUnit is evidence about the unit systemd has loaded,
// not a unit guessed from the database or from the rootfs directory.
type vmRuntimeAdoptionLoadedUnit struct {
	Name             string
	Runner           string
	JailerBase       string
	ExecStart        []string
	Fragments        []vmRuntimeAdoptionUnitFragment
	ActiveState      string
	MainPID          int
	NeedDaemonReload string
}

type vmRuntimeAdoptionInventoryDeps struct {
	architecture string
	evidence     vmRuntimeAdoptionEvidenceDeps
	loadUnit     func(context.Context, string) (vmRuntimeAdoptionLoadedUnit, error)
	runtimePair  func(context.Context, string, string) (string, error)
	readiness    func(string) (vmJailerReadiness, error)
}

type vmRuntimeAdoptionSummary struct {
	AlreadyAdopted []string
	Adoptable      []string
	Blocked        []string
	BlockedReasons map[string]string
}

type vmRuntimeAdoptionPreparation struct {
	Service            string
	ServiceRoot        string
	Classification     vmRuntimeAdoptionClassification
	BlockedReason      string
	OldDB              vmRuntimeJournalDBProjection
	NewDB              vmRuntimeJournalDBProjection
	Components         *db.VMComponentsConfig
	EffectiveUnit      vmRuntimeAdoptionLoadedUnit
	EffectiveKernel    string
	EffectiveRuntime   db.VMRuntimeArtifactConfig
	Evidence           vmRuntimeAdoptionPreconditionEvidence
	PreconditionSHA256 string
	Composition        vmLegacyCompositionRecord
	CompositionSHA256  string
}

type vmRuntimeAdoptionInventory struct {
	VMs     []vmRuntimeAdoptionPreparation
	Summary vmRuntimeAdoptionSummary
}

type vmRuntimeAdoptionPrecondition struct {
	Schema         string                                `json:"schema"`
	SchemaVersion  int                                   `json:"schemaVersion"`
	Service        string                                `json:"service"`
	ServiceRoot    string                                `json:"serviceRoot"`
	ServiceRootZFS string                                `json:"serviceRootZFS"`
	DataRoot       string                                `json:"dataRoot"`
	ServicesRoot   string                                `json:"servicesRoot"`
	JailerBase     string                                `json:"jailerBase"`
	Runtime        string                                `json:"runtime"`
	SetupState     string                                `json:"setupState"`
	Image          db.VMImageConfig                      `json:"image"`
	Disk           db.VMDiskConfig                       `json:"disk"`
	StoredService  vmRuntimeAdoptionStoredPrecondition   `json:"storedService"`
	OldDB          vmRuntimeJournalDBProjection          `json:"oldDB"`
	Unit           vmRuntimeAdoptionUnitPrecondition     `json:"unit"`
	Evidence       vmRuntimeAdoptionPreconditionEvidence `json:"evidence"`
}

// vmRuntimeAdoptionStoredPrecondition binds the complete static VM
// configuration while excluding fields owned by this transaction (Components)
// and unrelated live telemetry (Balloon.LastTargetBytes).
type vmRuntimeAdoptionStoredPrecondition struct {
	Name           string         `json:"name"`
	ServiceType    db.ServiceType `json:"serviceType"`
	ServiceRoot    string         `json:"serviceRoot"`
	ServiceRootZFS string         `json:"serviceRootZFS"`
	VM             *db.VMConfig   `json:"vm"`
}

type vmRuntimeAdoptionUnitPrecondition struct {
	Name             string   `json:"name"`
	Runner           string   `json:"runner"`
	JailerBase       string   `json:"jailerBase"`
	ExecStart        []string `json:"execStart"`
	Fragments        []string `json:"fragments"`
	ActiveState      string   `json:"activeState"`
	MainPID          int      `json:"mainPID"`
	NeedDaemonReload string   `json:"needDaemonReload"`
}

type vmRuntimeAdoptionManifestState struct {
	Path       string
	SHA256     string
	Manifest   vmImageManifest
	Evidence   vmRuntimeAdoptionFileEvidence
	Present    bool
	RootFSPath string
}

type vmRuntimeAdoptionSourceState struct {
	classification vmRuntimeAdoptionClassification
	receipt        *vmRuntimeAdoptionReceipt
}

func defaultVMRuntimeAdoptionInventoryDeps() vmRuntimeAdoptionInventoryDeps {
	return vmRuntimeAdoptionInventoryDeps{
		architecture: runtime.GOARCH,
		evidence:     defaultVMRuntimeAdoptionEvidenceDeps(),
		loadUnit:     loadEffectiveVMRuntimeAdoptionUnit,
		runtimePair:  inspectVMRuntimeAdoptionPair,
		readiness:    vmJailerReadinessForRoot,
	}
}

func completeVMRuntimeAdoptionInventoryDeps(deps vmRuntimeAdoptionInventoryDeps) vmRuntimeAdoptionInventoryDeps {
	defaults := defaultVMRuntimeAdoptionInventoryDeps()
	if strings.TrimSpace(deps.architecture) == "" {
		deps.architecture = defaults.architecture
	}
	deps.evidence = completeVMRuntimeAdoptionEvidenceDeps(deps.evidence)
	if deps.loadUnit == nil {
		deps.loadUnit = defaults.loadUnit
	}
	if deps.runtimePair == nil {
		deps.runtimePair = defaults.runtimePair
	}
	if deps.readiness == nil {
		deps.readiness = defaults.readiness
	}
	return deps
}

// inventoryVMRuntimeAdoptionFleet performs no network requests and publishes
// no files. Invalid or contradictory per-VM metadata becomes an explicit
// blocked result so a later coordinator can leave the current launch unit
// untouched while still reporting the rest of the fleet deterministically.
func inventoryVMRuntimeAdoptionFleet(ctx context.Context, cfg *Config, deps vmRuntimeAdoptionInventoryDeps) (vmRuntimeAdoptionInventory, error) {
	if err := ctx.Err(); err != nil {
		return vmRuntimeAdoptionInventory{}, err
	}
	effectiveCfg, err := prepareVMRuntimeAdoptionConfig(cfg)
	if err != nil {
		return vmRuntimeAdoptionInventory{}, err
	}
	deps = completeVMRuntimeAdoptionInventoryDeps(deps)
	dv, err := effectiveCfg.DB.Get()
	if err != nil {
		return vmRuntimeAdoptionInventory{}, fmt.Errorf("read VM runtime adoption inventory: %w", err)
	}
	if !dv.Valid() {
		return vmRuntimeAdoptionInventory{}, fmt.Errorf("read VM runtime adoption inventory: database is invalid")
	}
	data := dv.AsStruct()
	names := make([]string, 0, len(data.Services))
	for name, service := range data.Services {
		if isVMJailerUpgradeService(service) {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	result := vmRuntimeAdoptionInventory{VMs: make([]vmRuntimeAdoptionPreparation, 0, len(names))}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return vmRuntimeAdoptionInventory{}, err
		}
		service := data.Services[name]
		preparation := inventoryVMRuntimeAdoptionService(ctx, effectiveCfg, *service, deps)
		result.VMs = append(result.VMs, preparation)
		addVMRuntimeAdoptionSummary(&result.Summary, preparation)
	}
	sort.Strings(result.Summary.AlreadyAdopted)
	sort.Strings(result.Summary.Adoptable)
	sort.Strings(result.Summary.Blocked)
	return result, nil
}

func prepareVMRuntimeAdoptionConfig(cfg *Config) (*Config, error) {
	if cfg == nil || cfg.DB == nil {
		return nil, fmt.Errorf("catch configuration and database are required for VM runtime adoption")
	}
	root, err := cleanRequiredVMRuntimeAdoptionPath("configured VM data root", cfg.RootDir)
	if err != nil {
		return nil, err
	}
	effective := *cfg
	if strings.TrimSpace(effective.ServicesRoot) == "" {
		effective.ServicesRoot = filepath.Join(root, "services")
	}
	if _, err := cleanRequiredVMRuntimeAdoptionPath("configured VM services root", effective.ServicesRoot); err != nil {
		return nil, err
	}
	return &effective, nil
}

type vmRuntimeAdoptionServiceInventory struct {
	ctx         context.Context
	cfg         *Config
	service     db.Service
	deps        vmRuntimeAdoptionInventoryDeps
	preparation *vmRuntimeAdoptionPreparation

	root                     string
	configPath               string
	configEvidence           vmRuntimeAdoptionFileEvidence
	fcConfig                 firecrackerConfig
	activeDiskPath           string
	activeDisk               vmRuntimeAdoptionActiveDiskEvidence
	unit                     vmRuntimeAdoptionLoadedUnit
	unitArgs                 vmRuntimeAdoptionUnitArgs
	rootFSPath               string
	rootFSEvidence           vmRuntimeAdoptionFileEvidence
	kernelPath               string
	kernelEvidence           vmRuntimeAdoptionFileEvidence
	kernelConfigEvidence     vmRuntimeAdoptionFileEvidence
	initrdEvidence           vmRuntimeAdoptionFileEvidence
	firecrackerEvidence      vmRuntimeAdoptionFileEvidence
	jailerEvidence           vmRuntimeAdoptionFileEvidence
	runtimeVersion           string
	manifest                 vmRuntimeAdoptionManifestState
	manifestArtifactEvidence []vmRuntimeAdoptionFileEvidence
	source                   vmRuntimeAdoptionSourceState
	sourceEvidence           []vmRuntimeAdoptionFileEvidence
}

func inventoryVMRuntimeAdoptionService(ctx context.Context, cfg *Config, service db.Service, deps vmRuntimeAdoptionInventoryDeps) vmRuntimeAdoptionPreparation {
	oldDB := vmRuntimeJournalDBProjection{Components: service.VM.Components.Clone(), ImageKernel: service.VM.Image.Kernel}
	preparation := vmRuntimeAdoptionPreparation{Service: service.Name, Classification: vmRuntimeAdoptionBlocked, OldDB: oldDB, NewDB: oldDB}
	root, err := effectiveVMRuntimeAdoptionServiceRoot(*cfg, service)
	if err != nil {
		return blockVMRuntimeAdoption(preparation, err)
	}
	preparation.ServiceRoot = root
	if service.VM.Components != nil {
		preparation.Classification = vmRuntimeAdoptionAlreadyAdopted
		preparation.Components = service.VM.Components.Clone()
		return preparation
	}
	inventory := &vmRuntimeAdoptionServiceInventory{ctx: ctx, cfg: cfg, service: service, deps: deps, preparation: &preparation, root: root}
	stages := []func() error{
		inventory.validateStoredState,
		inventory.inventoryConfigAndDisk,
		inventory.inventoryLoadedUnit,
		inventory.inventoryBootArtifacts,
		inventory.inventoryRuntimePair,
		inventory.inventorySource,
		inventory.composeComponents,
		inventory.buildPrecondition,
	}
	for _, stage := range stages {
		if err := stage(); err != nil {
			return blockVMRuntimeAdoption(preparation, err)
		}
	}
	return preparation
}

func (inventory *vmRuntimeAdoptionServiceInventory) validateStoredState() error {
	if err := inventory.ctx.Err(); err != nil {
		return err
	}
	service := inventory.service
	if service.VM.Image.Version == "" || service.VM.Image.Version != strings.TrimSpace(service.VM.Image.Version) {
		return fmt.Errorf("stored VM image version must be nonempty and trimmed")
	}
	if service.VM.Image.Payload != strings.TrimSpace(service.VM.Image.Payload) {
		return fmt.Errorf("stored VM image payload must be trimmed")
	}
	return nil
}

func (inventory *vmRuntimeAdoptionServiceInventory) inventoryConfigAndDisk() error {
	inventory.configPath = filepath.Join(serviceRunDirForRoot(inventory.root), "firecracker.json")
	configRaw, configEvidence, err := readTrustedVMRuntimeAdoptionFile(inventory.configPath, vmRuntimeAdoptionMetadataMaxBytes, inventory.deps.evidence)
	if err != nil {
		return fmt.Errorf("read effective Firecracker config: %w", err)
	}
	inventory.configEvidence = configEvidence
	inventory.fcConfig, err = decodeVMRuntimeAdoptionFirecrackerConfig(configRaw)
	if err != nil {
		return err
	}
	inventory.activeDiskPath, err = vmRuntimeAdoptionRootDrive(inventory.fcConfig)
	if err != nil {
		return err
	}
	if err := validateVMRuntimeAdoptionDiskAgreement(inventory.service.VM.Disk, inventory.activeDiskPath); err != nil {
		return err
	}
	inventory.activeDisk, err = collectVMRuntimeActiveDiskEvidence(inventory.activeDiskPath, inventory.service.VM.Disk.Backend, inventory.service.VM.Disk.Bytes, inventory.deps.evidence)
	if err != nil {
		return fmt.Errorf("inventory active VM disk without reading its contents: %w", err)
	}
	return nil
}

func (inventory *vmRuntimeAdoptionServiceInventory) inventoryLoadedUnit() error {
	unit, err := inventory.deps.loadUnit(inventory.ctx, vmSystemdUnitName(inventory.service.Name))
	if err != nil {
		return fmt.Errorf("load effective VM systemd unit: %w", err)
	}
	inventory.unit = unit
	inventory.unitArgs, err = validateVMRuntimeAdoptionUnit(
		unit, inventory.service.Name, inventory.root, inventory.configPath, inventory.activeDiskPath,
		vmJailerBaseForDataRoot(inventory.cfg.RootDir),
	)
	if err != nil {
		return err
	}
	inventory.unit.Runner = inventory.unitArgs.runner
	inventory.unit.JailerBase = inventory.unitArgs.jailerBase
	inventory.preparation.EffectiveUnit = cloneVMRuntimeAdoptionLoadedUnit(inventory.unit)
	return nil
}

func (inventory *vmRuntimeAdoptionServiceInventory) inventoryBootArtifacts() error {
	if err := inventory.inventoryRootFS(); err != nil {
		return err
	}
	return inventory.inventoryKernelArtifacts()
}

func (inventory *vmRuntimeAdoptionServiceInventory) inventoryRootFS() error {
	rootFSPath, err := cleanRequiredVMRuntimeAdoptionPath("stored immutable prepared rootfs", inventory.service.VM.Image.RootFS)
	if err != nil {
		return err
	}
	inventory.rootFSPath = rootFSPath
	if rootFSPath == inventory.activeDiskPath {
		return fmt.Errorf("stored immutable prepared rootfs is the active mutable disk")
	}
	inventory.rootFSEvidence, err = collectTrustedVMRuntimeAdoptionFileEvidence(rootFSPath, true, inventory.deps.evidence)
	if err != nil {
		return fmt.Errorf("inventory immutable prepared rootfs: %w", err)
	}
	if inventory.activeDisk.Backend == vmDiskBackendRaw && inventory.rootFSEvidence.Device == inventory.activeDisk.Device && inventory.rootFSEvidence.Inode == inventory.activeDisk.Inode {
		return fmt.Errorf("stored immutable prepared rootfs aliases the active mutable disk")
	}
	return nil
}

func (inventory *vmRuntimeAdoptionServiceInventory) inventoryKernelArtifacts() error {
	var err error
	inventory.kernelPath, err = cleanRequiredVMRuntimeAdoptionPath("effective kernel", inventory.fcConfig.BootSource.KernelImagePath)
	if err != nil {
		return err
	}
	if err := validateVMRuntimeAdoptionStoredKernel(inventory.service, inventory.root, inventory.kernelPath); err != nil {
		return err
	}
	inventory.kernelEvidence, err = collectTrustedVMRuntimeAdoptionFileEvidence(inventory.kernelPath, true, inventory.deps.evidence)
	if err != nil {
		return fmt.Errorf("inventory effective VM kernel: %w", err)
	}
	configSibling := filepath.Join(filepath.Dir(inventory.kernelPath), "kernel.config")
	inventory.kernelConfigEvidence, err = collectTrustedVMRuntimeAdoptionFileEvidence(configSibling, false, inventory.deps.evidence)
	if err != nil {
		return fmt.Errorf("inventory effective VM kernel config: %w", err)
	}
	if strings.TrimSpace(inventory.fcConfig.BootSource.InitrdPath) != "" {
		initrdPath, err := cleanRequiredVMRuntimeAdoptionPath("effective initrd", inventory.fcConfig.BootSource.InitrdPath)
		if err != nil {
			return err
		}
		inventory.initrdEvidence, err = collectTrustedVMRuntimeAdoptionFileEvidence(initrdPath, true, inventory.deps.evidence)
		if err != nil {
			return fmt.Errorf("inventory effective VM initrd: %w", err)
		}
	}
	return nil
}

func (inventory *vmRuntimeAdoptionServiceInventory) inventoryRuntimePair() error {
	var err error
	inventory.firecrackerEvidence, err = collectTrustedVMRuntimeAdoptionFileEvidence(inventory.unitArgs.firecracker, true, inventory.deps.evidence)
	if err != nil {
		return fmt.Errorf("inventory effective Firecracker: %w", err)
	}
	inventory.jailerEvidence, err = collectTrustedVMRuntimeAdoptionFileEvidence(inventory.unitArgs.jailer, true, inventory.deps.evidence)
	if err != nil {
		return fmt.Errorf("inventory effective jailer: %w", err)
	}
	inventory.runtimeVersion, err = inventory.deps.runtimePair(inventory.ctx, inventory.unitArgs.firecracker, inventory.unitArgs.jailer)
	if err != nil {
		return fmt.Errorf("validate effective Firecracker and jailer pair: %w", err)
	}
	return nil
}

func (inventory *vmRuntimeAdoptionServiceInventory) inventorySource() error {
	manifest, err := inspectVMRuntimeAdoptionManifest(inventory.rootFSPath, inventory.deps.evidence)
	if err != nil {
		return err
	}
	inventory.manifest = manifest
	if manifest.Present {
		if err := validateVMRuntimeAdoptionManifestRelation(inventory.service, inventory.root, manifest, inventory.rootFSPath, inventory.kernelPath, inventory.initrdEvidence.Path, inventory.unitArgs.firecracker, inventory.unitArgs.jailer); err != nil {
			return err
		}
	}
	inventory.manifestArtifactEvidence, err = validateVMRuntimeAdoptionManifestArtifacts(
		manifest, inventory.rootFSEvidence, inventory.kernelEvidence, inventory.initrdEvidence,
		inventory.firecrackerEvidence, inventory.jailerEvidence, inventory.deps.evidence,
	)
	if err != nil {
		return err
	}
	inventory.source, inventory.sourceEvidence, err = classifyVMRuntimeAdoptionSource(
		*inventory.cfg, inventory.service, manifest, inventory.rootFSEvidence, inventory.kernelEvidence,
		inventory.firecrackerEvidence, inventory.jailerEvidence, inventory.deps.evidence,
	)
	if err != nil {
		return err
	}
	return nil
}

func (inventory *vmRuntimeAdoptionServiceInventory) composeComponents() error {
	guestName, kernelVersion := vmRuntimeAdoptionIdentityNames(inventory.service, inventory.root, inventory.manifest, inventory.kernelPath)
	composition, err := newVMLegacyCompositionRecord(vmLegacyCompositionInput{
		Architecture: inventory.deps.architecture, GuestName: guestName, GuestVersion: inventory.service.VM.Image.Version,
		KernelVersion: kernelVersion, FirecrackerVersion: inventory.runtimeVersion,
		RootFS: inventory.rootFSEvidence, Kernel: inventory.kernelEvidence, KernelConfig: inventory.kernelConfigEvidence,
		Initrd: inventory.initrdEvidence, Firecracker: inventory.firecrackerEvidence, Jailer: inventory.jailerEvidence,
	})
	if err != nil {
		return fmt.Errorf("create measured legacy composition: %w", err)
	}
	_, compositionSHA, err := canonicalVMLegacyComposition(composition)
	if err != nil {
		return err
	}
	guestID, kernelID, runtimeID, err := vmLegacyCompositionIDs(composition, compositionSHA)
	if err != nil {
		return err
	}

	guestManifestSHA, kernelManifestSHA, runtimeManifestSHA := vmRuntimeAdoptionComponentManifestDigests(
		inventory.source, inventory.manifest, compositionSHA, inventory.rootFSEvidence,
		inventory.kernelEvidence, inventory.firecrackerEvidence, inventory.jailerEvidence,
	)
	kernelSource := inventory.source.classification
	if isVMRuntimeAdoptionSyncedKernel(inventory.root, inventory.service.Name, inventory.kernelPath) {
		kernelSource = vmRuntimeAdoptionCustomLegacy
		kernelManifestSHA = compositionSHA
	}
	components := &db.VMComponentsConfig{
		GuestBase: db.VMGuestBaseConfig{
			ID: guestID, ManifestSHA256: guestManifestSHA, Source: string(inventory.source.classification),
			RootFSProvenance: compositionSHA,
		},
		Kernel: db.VMKernelArtifactConfig{
			ID: kernelID, ManifestSHA256: kernelManifestSHA, SHA256: inventory.kernelEvidence.SHA256,
			Path: inventory.kernelPath, Source: string(kernelSource),
		},
		Runtime: db.VMRuntimeLifecycleConfig{
			Policy: "manual", Channel: "stable",
			Configured: db.VMRuntimeArtifactConfig{
				ID: runtimeID, ManifestSHA256: runtimeManifestSHA,
				FirecrackerSHA256: inventory.firecrackerEvidence.SHA256, JailerSHA256: inventory.jailerEvidence.SHA256,
				Firecracker: inventory.unitArgs.firecracker, Jailer: inventory.unitArgs.jailer, Source: string(inventory.source.classification),
			},
		},
	}
	preparation := inventory.preparation
	preparation.Classification = inventory.source.classification
	preparation.Components = components.Clone()
	preparation.Composition = composition
	preparation.CompositionSHA256 = compositionSHA
	preparation.EffectiveKernel = inventory.kernelPath
	preparation.EffectiveRuntime = components.Runtime.Configured
	preparation.NewDB = vmRuntimeJournalDBProjection{Components: components.Clone(), ImageKernel: inventory.kernelPath}
	return nil
}

func (inventory *vmRuntimeAdoptionServiceInventory) buildPrecondition() error {
	files := []vmRuntimeAdoptionFileEvidence{
		inventory.configEvidence, inventory.rootFSEvidence, inventory.kernelEvidence,
		inventory.kernelConfigEvidence, inventory.initrdEvidence,
		inventory.firecrackerEvidence, inventory.jailerEvidence,
	}
	files = append(files, inventory.manifestArtifactEvidence...)
	files = append(files, inventory.sourceEvidence...)
	for _, fragment := range inventory.unit.Fragments {
		files = append(files, fragment.Evidence)
	}
	files, err := normalizeVMRuntimeAdoptionEvidence(files)
	if err != nil {
		return err
	}
	preparation := inventory.preparation
	preparation.Evidence = vmRuntimeAdoptionPreconditionEvidence{
		Service: inventory.service.Name, ServiceRoot: inventory.root, Files: files, ActiveDisk: inventory.activeDisk,
	}
	preconditionSHA, err := vmRuntimeAdoptionPreconditionDigest(*inventory.cfg, inventory.service, *preparation)
	if err != nil {
		return err
	}
	preparation.PreconditionSHA256 = preconditionSHA
	return nil
}

func blockVMRuntimeAdoption(preparation vmRuntimeAdoptionPreparation, err error) vmRuntimeAdoptionPreparation {
	preparation.Classification = vmRuntimeAdoptionBlocked
	preparation.BlockedReason = err.Error()
	preparation.Components = nil
	preparation.NewDB = preparation.OldDB
	preparation.PreconditionSHA256 = ""
	preparation.Composition = vmLegacyCompositionRecord{}
	preparation.CompositionSHA256 = ""
	return preparation
}

func addVMRuntimeAdoptionSummary(summary *vmRuntimeAdoptionSummary, preparation vmRuntimeAdoptionPreparation) {
	switch preparation.Classification {
	case vmRuntimeAdoptionAlreadyAdopted:
		summary.AlreadyAdopted = append(summary.AlreadyAdopted, preparation.Service)
	case vmRuntimeAdoptionOfficialLegacy, vmRuntimeAdoptionLocalLegacy, vmRuntimeAdoptionCustomLegacy:
		summary.Adoptable = append(summary.Adoptable, preparation.Service)
	case vmRuntimeAdoptionBlocked:
		summary.Blocked = append(summary.Blocked, preparation.Service)
		if summary.BlockedReasons == nil {
			summary.BlockedReasons = make(map[string]string)
		}
		summary.BlockedReasons[preparation.Service] = preparation.BlockedReason
	}
}

func effectiveVMRuntimeAdoptionServiceRoot(cfg Config, service db.Service) (string, error) {
	root := serviceRootFromConfig(cfg, service)
	if strings.TrimSpace(service.ServiceRootZFS) != "" && strings.TrimSpace(service.ServiceRoot) == "" {
		return "", fmt.Errorf("ZFS VM service root has no persisted mountpoint")
	}
	return cleanRequiredVMRuntimeAdoptionPath("effective service root", root)
}

func cleanRequiredVMRuntimeAdoptionPath(label, value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed != value || !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return "", fmt.Errorf("%s must be a clean, absolute, trimmed path: %q", label, value)
	}
	return value, nil
}

func decodeVMRuntimeAdoptionFirecrackerConfig(raw []byte) (firecrackerConfig, error) {
	var cfg firecrackerConfig
	if err := decodeStrictVMRuntimeAdoptionJSON(raw, &cfg); err != nil {
		return firecrackerConfig{}, fmt.Errorf("decode effective Firecracker config: %w", err)
	}
	return cfg, nil
}

func vmRuntimeAdoptionRootDrive(cfg firecrackerConfig) (string, error) {
	root := ""
	for _, drive := range cfg.Drives {
		if !drive.IsRootDevice && drive.DriveID != "rootfs" {
			continue
		}
		if root != "" {
			return "", fmt.Errorf("effective Firecracker config has multiple root drives")
		}
		if drive.IsReadOnly {
			return "", fmt.Errorf("effective Firecracker root drive is unexpectedly read-only")
		}
		var err error
		root, err = cleanRequiredVMRuntimeAdoptionPath("effective active disk", drive.PathOnHost)
		if err != nil {
			return "", err
		}
	}
	if root == "" {
		return "", fmt.Errorf("effective Firecracker config has no root drive")
	}
	return root, nil
}

func validateVMRuntimeAdoptionDiskAgreement(disk db.VMDiskConfig, effective string) error {
	stored := strings.TrimSpace(disk.Path)
	if stored == "" {
		return fmt.Errorf("stored VM disk path is required")
	}
	if disk.Backend == vmDiskBackendZVOL && !strings.HasPrefix(stored, "/dev/zvol/") {
		stored = "/dev/zvol/" + strings.TrimPrefix(stored, "/")
	}
	stored, err := cleanRequiredVMRuntimeAdoptionPath("stored active disk", stored)
	if err != nil {
		return err
	}
	if stored != effective {
		return fmt.Errorf("stored active disk %q does not match effective Firecracker root drive %q", stored, effective)
	}
	return nil
}

type vmRuntimeAdoptionUnitArgs struct {
	firecracker string
	jailer      string
	runner      string
	jailerBase  string
}

func validateVMRuntimeAdoptionUnit(unit vmRuntimeAdoptionLoadedUnit, service, root, configPath, diskPath, expectedJailerBase string) (vmRuntimeAdoptionUnitArgs, error) {
	if err := validateVMRuntimeAdoptionLoadedUnitEvidence(unit, service); err != nil {
		return vmRuntimeAdoptionUnitArgs{}, err
	}
	runner, flags, err := validateVMRuntimeAdoptionLoadedCommand(unit.ExecStart)
	if err != nil {
		return vmRuntimeAdoptionUnitArgs{}, err
	}
	if err := validateVMRuntimeAdoptionRequiredFlags(flags, service, root, configPath, diskPath); err != nil {
		return vmRuntimeAdoptionUnitArgs{}, err
	}
	paths, err := validateVMRuntimeAdoptionLoadedPaths(flags, expectedJailerBase)
	if err != nil {
		return vmRuntimeAdoptionUnitArgs{}, err
	}
	paths.runner = runner
	return paths, nil
}

func validateVMRuntimeAdoptionLoadedUnitEvidence(unit vmRuntimeAdoptionLoadedUnit, service string) error {
	if unit.Name != vmSystemdUnitName(service) {
		return fmt.Errorf("loaded VM unit name %q does not match %q", unit.Name, vmSystemdUnitName(service))
	}
	if len(unit.ExecStart) == 0 {
		return fmt.Errorf("loaded VM unit has no effective ExecStart")
	}
	if len(unit.Fragments) == 0 {
		return fmt.Errorf("loaded VM unit has no immutable fragment evidence")
	}
	if err := validateVMRuntimeAdoptionLoadedUnitState(unit); err != nil {
		return err
	}
	return validateVMRuntimeAdoptionReloadState(unit.NeedDaemonReload)
}

func validateVMRuntimeAdoptionLoadedUnitState(unit vmRuntimeAdoptionLoadedUnit) error {
	switch unit.ActiveState {
	case "active", "inactive", "activating", "deactivating", "failed", "reloading", "maintenance":
	default:
		return fmt.Errorf("loaded VM unit ActiveState %q is invalid", unit.ActiveState)
	}
	if unit.MainPID < 0 {
		return fmt.Errorf("loaded VM unit MainPID is invalid")
	}
	return nil
}

func validateVMRuntimeAdoptionReloadState(value string) error {
	switch value {
	case "no":
		return nil
	case "yes":
		return fmt.Errorf("loaded VM unit requires daemon-reload before its effective launch command can be trusted")
	default:
		return fmt.Errorf("loaded VM unit NeedDaemonReload %q is invalid", value)
	}
}

func validateVMRuntimeAdoptionLoadedCommand(argv []string) (string, map[string]string, error) {
	if len(argv) < 2 {
		return "", nil, fmt.Errorf("loaded VM unit must invoke vm-run directly through its runner")
	}
	runner, err := cleanRequiredVMRuntimeAdoptionPath("loaded unit runner", argv[0])
	if err != nil {
		return "", nil, err
	}
	flags, err := vmRuntimeAdoptionExecFlags(argv)
	if err != nil {
		return "", nil, err
	}
	return runner, flags, nil
}

func validateVMRuntimeAdoptionRequiredFlags(flags map[string]string, service, root, configPath, diskPath string) error {
	required := []struct {
		name string
		want string
	}{
		{name: "--service", want: service},
		{name: "--service-root", want: root},
		{name: "--config-file", want: configPath},
		{name: "--disk-path", want: diskPath},
	}
	for _, flag := range required {
		if flags[flag.name] != flag.want {
			return fmt.Errorf("loaded VM unit %s is %q, want %q", flag.name, flags[flag.name], flag.want)
		}
	}
	for _, name := range []string{"--runtime-descriptor", "--runtime-running-marker", "--runtime-trial-result"} {
		if _, present := flags[name]; present {
			return fmt.Errorf("unadopted VM unexpectedly contains descriptor-mode flag %s", name)
		}
	}
	return nil
}

func validateVMRuntimeAdoptionLoadedPaths(flags map[string]string, expectedJailerBase string) (vmRuntimeAdoptionUnitArgs, error) {
	firecracker, err := cleanRequiredVMRuntimeAdoptionPath("loaded unit Firecracker", flags["--firecracker"])
	if err != nil {
		return vmRuntimeAdoptionUnitArgs{}, err
	}
	jailer, err := cleanRequiredVMRuntimeAdoptionPath("loaded unit jailer", flags["--jailer"])
	if err != nil {
		return vmRuntimeAdoptionUnitArgs{}, err
	}
	jailerBase, err := cleanRequiredVMRuntimeAdoptionPath("loaded unit jailer base", flags["--jailer-base"])
	if err != nil {
		return vmRuntimeAdoptionUnitArgs{}, err
	}
	if jailerBase != expectedJailerBase {
		return vmRuntimeAdoptionUnitArgs{}, fmt.Errorf("loaded unit jailer base %q does not match configured data-root jailer base %q", jailerBase, expectedJailerBase)
	}
	return vmRuntimeAdoptionUnitArgs{firecracker: firecracker, jailer: jailer, jailerBase: jailerBase}, nil
}

func vmRuntimeAdoptionExecFlags(argv []string) (map[string]string, error) {
	commandIndex, globals, err := vmRuntimeAdoptionCommandIndex(argv)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{
		"--service": true, "--service-root": true, "--disk-path": true,
		"--firecracker": true, "--jailer": true, "--runtime-descriptor": true,
		"--runtime-running-marker": true, "--runtime-trial-result": true,
		"--jailer-base": true, "--api-sock": true, "--config-file": true, "--console-sock": true,
	}
	flags := globals
	for i := commandIndex + 1; i < len(argv); i++ {
		name, value := argv[i], ""
		if split := strings.IndexByte(name, '='); split >= 0 {
			value, name = name[split+1:], name[:split]
		} else {
			if i+1 >= len(argv) {
				return nil, fmt.Errorf("loaded VM unit flag %q has no value", name)
			}
			i++
			value = argv[i]
		}
		if !allowed[name] {
			return nil, fmt.Errorf("loaded VM unit has unexpected vm-run argument %q", name)
		}
		if _, exists := flags[name]; exists {
			return nil, fmt.Errorf("loaded VM unit repeats vm-run flag %q", name)
		}
		flags[name] = value
	}
	return flags, nil
}

func vmRuntimeAdoptionCommandIndex(argv []string) (int, map[string]string, error) {
	if len(argv) < 2 {
		return 0, nil, fmt.Errorf("loaded VM unit ExecStart does not invoke vm-run")
	}
	allowed := map[string]bool{"-data-dir": true, "-services-root": true}
	globals := make(map[string]string)
	for i := 1; i < len(argv); {
		if argv[i] == "vm-run" {
			if len(globals) != 0 && len(globals) != len(allowed) {
				return 0, nil, fmt.Errorf("loaded VM unit must provide both -data-dir and -services-root")
			}
			return i, globals, nil
		}
		name, value, next, err := vmRuntimeAdoptionGlobalFlag(argv, i)
		if err != nil {
			return 0, nil, err
		}
		if !allowed[name] {
			return 0, nil, fmt.Errorf("loaded VM unit has unexpected global argument %q before vm-run", name)
		}
		if _, exists := globals[name]; exists {
			return 0, nil, fmt.Errorf("loaded VM unit repeats global flag %q", name)
		}
		clean, err := cleanRequiredVMRuntimeAdoptionPath("loaded unit "+name, value)
		if err != nil {
			return 0, nil, err
		}
		globals[name] = clean
		i = next
	}
	return 0, nil, fmt.Errorf("loaded VM unit ExecStart does not invoke vm-run")
}

func vmRuntimeAdoptionGlobalFlag(argv []string, index int) (name, value string, next int, err error) {
	name = argv[index]
	if split := strings.IndexByte(name, '='); split >= 0 {
		return name[:split], name[split+1:], index + 1, nil
	}
	if index+1 >= len(argv) {
		return "", "", index, fmt.Errorf("loaded VM unit global flag %q has no value", name)
	}
	return name, argv[index+1], index + 2, nil
}

func validateVMRuntimeAdoptionStoredKernel(service db.Service, root, effective string) error {
	stored := strings.TrimSpace(service.VM.Image.Kernel)
	if stored == effective {
		return nil
	}
	if isVMRuntimeAdoptionSyncedKernel(root, service.Name, effective) {
		return nil
	}
	return fmt.Errorf("stored VM kernel %q does not match effective Firecracker kernel %q outside the trusted service-local sync path", stored, effective)
}

func isVMRuntimeAdoptionSyncedKernel(root, service, kernel string) bool {
	prefix := filepath.Join(serviceRunDirForRoot(root), "kernels", service)
	rel, err := filepath.Rel(prefix, kernel)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	return len(parts) == 2 && parts[0] != "" && parts[1] == "vmlinux" && normalizeVMLegacyIDSegment(parts[0]) != ""
}

func inspectVMRuntimeAdoptionManifest(rootFSPath string, deps vmRuntimeAdoptionEvidenceDeps) (vmRuntimeAdoptionManifestState, error) {
	path := filepath.Join(filepath.Dir(rootFSPath), "manifest.json")
	present, err := vmRuntimeAdoptionPathPresent(path, deps)
	if err != nil {
		return vmRuntimeAdoptionManifestState{}, fmt.Errorf("inspect installed VM manifest: %w", err)
	}
	if !present {
		return vmRuntimeAdoptionManifestState{Path: path}, nil
	}
	raw, evidence, err := readTrustedVMRuntimeAdoptionFile(path, vmRuntimeAdoptionMetadataMaxBytes, deps)
	if err != nil {
		return vmRuntimeAdoptionManifestState{}, fmt.Errorf("read installed VM manifest: %w", err)
	}
	var manifest vmImageManifest
	if err := decodeStrictVMRuntimeAdoptionJSON(raw, &manifest); err != nil {
		return vmRuntimeAdoptionManifestState{}, fmt.Errorf("decode installed VM manifest: %w", err)
	}
	if err := manifest.validate(); err != nil {
		return vmRuntimeAdoptionManifestState{}, fmt.Errorf("validate installed VM manifest: %w", err)
	}
	return vmRuntimeAdoptionManifestState{
		Path: path, SHA256: evidence.SHA256, Manifest: manifest, Evidence: evidence,
		Present: true, RootFSPath: filepath.Join(filepath.Dir(path), manifest.RootFS),
	}, nil
}

func validateVMRuntimeAdoptionManifestRelation(service db.Service, root string, state vmRuntimeAdoptionManifestState, preparedRootFS, kernel, initrd, firecracker, jailer string) error {
	manifest := state.Manifest
	if manifest.Version != strings.TrimSpace(service.VM.Image.Version) {
		return fmt.Errorf("installed VM manifest version %q contradicts stored image version %q", manifest.Version, service.VM.Image.Version)
	}
	architecture, err := normalizeVMImageArchitecture(manifest.Architecture)
	if err != nil || architecture != "amd64" {
		return fmt.Errorf("installed VM manifest architecture is not amd64")
	}
	if err := validateVMRuntimeAdoptionManifestBootRelation(service, root, state, preparedRootFS, kernel, initrd); err != nil {
		return err
	}
	return validateVMRuntimeAdoptionManifestRuntimeRelation(state, firecracker, jailer)
}

func validateVMRuntimeAdoptionManifestBootRelation(service db.Service, root string, state vmRuntimeAdoptionManifestState, preparedRootFS, kernel, initrd string) error {
	manifest := state.Manifest
	wantPrepared, compressed := vmRootFSDecompressedPath(state.RootFSPath)
	if preparedRootFS != state.RootFSPath && (!compressed || preparedRootFS != wantPrepared) {
		return fmt.Errorf("installed VM manifest rootfs does not bind prepared rootfs %q", preparedRootFS)
	}
	manifestKernel := filepath.Join(filepath.Dir(state.Path), manifest.Kernel)
	if kernel != manifestKernel && !isVMRuntimeAdoptionSyncedKernel(root, service.Name, kernel) {
		return fmt.Errorf("installed VM manifest kernel contradicts effective kernel %q", kernel)
	}
	if manifest.Initrd == "" && initrd != "" || manifest.Initrd != "" && initrd != filepath.Join(filepath.Dir(state.Path), manifest.Initrd) {
		return fmt.Errorf("installed VM manifest initrd contradicts effective initrd %q", initrd)
	}
	return nil
}

func validateVMRuntimeAdoptionManifestRuntimeRelation(state vmRuntimeAdoptionManifestState, firecracker, jailer string) error {
	manifest := state.Manifest
	dir := filepath.Dir(state.Path)
	if firecracker != filepath.Join(dir, manifest.Firecracker) {
		return fmt.Errorf("installed VM manifest Firecracker contradicts loaded unit path %q", firecracker)
	}
	if strings.TrimSpace(manifest.Jailer) == "" {
		if jailer != filepath.Join(dir, "jailer") {
			return fmt.Errorf("installed legacy VM manifest requires loaded unit legacy sibling jailer, got %q", jailer)
		}
		return nil
	}
	if jailer != filepath.Join(dir, manifest.Jailer) {
		return fmt.Errorf("installed VM manifest jailer contradicts loaded unit path %q", jailer)
	}
	return nil
}

func validateVMRuntimeAdoptionManifestArtifacts(
	state vmRuntimeAdoptionManifestState,
	rootFS, kernel, initrd, firecracker, jailer vmRuntimeAdoptionFileEvidence,
	deps vmRuntimeAdoptionEvidenceDeps,
) ([]vmRuntimeAdoptionFileEvidence, error) {
	if !state.Present {
		return nil, nil
	}
	dir := filepath.Dir(state.Path)
	effective := map[string]vmRuntimeAdoptionFileEvidence{
		rootFS.Path: rootFS, kernel.Path: kernel, firecracker.Path: firecracker, jailer.Path: jailer,
	}
	if initrd.Exists {
		effective[initrd.Path] = initrd
	}
	artifacts := state.Manifest.artifactNames()
	evidence := make([]vmRuntimeAdoptionFileEvidence, 0, len(artifacts))
	for _, name := range artifacts {
		path := filepath.Join(dir, name)
		fileEvidence, ok := effective[path]
		if !ok {
			var err error
			fileEvidence, err = collectTrustedVMRuntimeAdoptionFileEvidence(path, true, deps)
			if err != nil {
				return nil, fmt.Errorf("inventory installed VM manifest artifact %q: %w", name, err)
			}
		}
		if !strings.EqualFold(fileEvidence.SHA256, state.Manifest.Checksums[name]) {
			return nil, fmt.Errorf("installed VM manifest checksum for %q contradicts exact installed bytes", name)
		}
		evidence = append(evidence, fileEvidence)
	}
	return evidence, nil
}

func classifyVMRuntimeAdoptionSource(cfg Config, service db.Service, manifest vmRuntimeAdoptionManifestState, rootFS, kernel, firecracker, jailer vmRuntimeAdoptionFileEvidence, deps vmRuntimeAdoptionEvidenceDeps) (vmRuntimeAdoptionSourceState, []vmRuntimeAdoptionFileEvidence, error) {
	state := vmRuntimeAdoptionSourceState{classification: vmRuntimeAdoptionCustomLegacy}
	var evidence []vmRuntimeAdoptionFileEvidence
	if manifest.Present {
		evidence = append(evidence, manifest.Evidence)
	}
	receiptPresent, receipt, receiptEvidence, err := inspectVMRuntimeAdoptionReceipt(service, manifest, rootFS, deps)
	if err != nil {
		return state, nil, err
	}
	if receiptPresent {
		state.classification = vmRuntimeAdoptionOfficialLegacy
		state.receipt = &receipt
		evidence = append(evidence, receiptEvidence)
	}
	localPresent, refEvidence, err := inspectLocalVMRuntimeAdoptionRef(cfg, service, manifest, rootFS, kernel, firecracker, jailer, deps)
	if err != nil {
		return state, nil, err
	}
	if receiptPresent && localPresent {
		return state, nil, fmt.Errorf("installed VM has contradictory official receipt and local ref")
	}
	if localPresent {
		state.classification = vmRuntimeAdoptionLocalLegacy
		evidence = append(evidence, refEvidence)
	}
	return state, evidence, nil
}

func inspectVMRuntimeAdoptionReceipt(service db.Service, manifest vmRuntimeAdoptionManifestState, rootFS vmRuntimeAdoptionFileEvidence, deps vmRuntimeAdoptionEvidenceDeps) (bool, vmRuntimeAdoptionReceipt, vmRuntimeAdoptionFileEvidence, error) {
	receiptPath := filepath.Join(filepath.Dir(rootFS.Path), vmRuntimeAdoptionReceiptFileName)
	receiptPresent, err := vmRuntimeAdoptionPathPresent(receiptPath, deps)
	if err != nil {
		return false, vmRuntimeAdoptionReceipt{}, vmRuntimeAdoptionFileEvidence{}, fmt.Errorf("inspect installed VM receipt: %w", err)
	}
	if !receiptPresent {
		return false, vmRuntimeAdoptionReceipt{}, vmRuntimeAdoptionFileEvidence{}, nil
	}
	if !manifest.Present {
		return false, vmRuntimeAdoptionReceipt{}, vmRuntimeAdoptionFileEvidence{}, fmt.Errorf("installed VM receipt is present without manifest")
	}
	receipt, evidence, err := readVMRuntimeAdoptionReceipt(receiptPath, service, manifest, rootFS, deps)
	return true, receipt, evidence, err
}

func inspectLocalVMRuntimeAdoptionRef(cfg Config, service db.Service, manifest vmRuntimeAdoptionManifestState, rootFS, kernel, firecracker, jailer vmRuntimeAdoptionFileEvidence, deps vmRuntimeAdoptionEvidenceDeps) (bool, vmRuntimeAdoptionFileEvidence, error) {
	localName := ""
	payload := strings.TrimSpace(service.VM.Image.Payload)
	if strings.HasPrefix(payload, vmImagePayloadPrefix) {
		candidate := strings.TrimPrefix(payload, vmImagePayloadPrefix)
		if validateLocalVMImageName(candidate) == nil {
			localName = candidate
		}
	}
	refPath := ""
	refPresent := false
	if localName != "" {
		refPath = localVMImageRefPath(filepath.Join(cfg.RootDir, "vm-images"), localName)
		present, err := vmRuntimeAdoptionPathPresent(refPath, deps)
		if err != nil {
			return false, vmRuntimeAdoptionFileEvidence{}, fmt.Errorf("inspect installed local VM ref: %w", err)
		}
		refPresent = present
	}
	if !refPresent {
		return false, vmRuntimeAdoptionFileEvidence{}, nil
	}
	if !manifest.Present {
		return false, vmRuntimeAdoptionFileEvidence{}, fmt.Errorf("installed local VM ref is present without manifest")
	}
	_, refEvidence, err := readExactLocalVMRuntimeAdoptionRef(refPath, filepath.Join(cfg.RootDir, "vm-images"), localName, serviceRootFromConfig(cfg, service), service, manifest, rootFS, kernel, firecracker, jailer, deps)
	return true, refEvidence, err
}

func readVMRuntimeAdoptionReceipt(path string, service db.Service, manifest vmRuntimeAdoptionManifestState, rootFS vmRuntimeAdoptionFileEvidence, deps vmRuntimeAdoptionEvidenceDeps) (vmRuntimeAdoptionReceipt, vmRuntimeAdoptionFileEvidence, error) {
	raw, evidence, err := readTrustedVMRuntimeAdoptionFile(path, vmRuntimeAdoptionMetadataMaxBytes, deps)
	if err != nil {
		return vmRuntimeAdoptionReceipt{}, evidence, fmt.Errorf("read installed VM receipt: %w", err)
	}
	var receipt vmRuntimeAdoptionReceipt
	if err := decodeStrictVMRuntimeAdoptionJSON(raw, &receipt); err != nil {
		return receipt, evidence, fmt.Errorf("decode installed VM receipt: %w", err)
	}
	if err := validateVMRuntimeAdoptionReceiptIdentity(receipt, service); err != nil {
		return receipt, evidence, err
	}
	if err := validateVMRuntimeAdoptionReceiptDigests(receipt, manifest, rootFS); err != nil {
		return receipt, evidence, err
	}
	return receipt, evidence, nil
}

func validateVMRuntimeAdoptionReceiptIdentity(receipt vmRuntimeAdoptionReceipt, service db.Service) error {
	if receipt.Schema != vmRuntimeAdoptionReceiptSchema || receipt.SchemaVersion != vmRuntimeAdoptionReceiptSchemaVersion {
		return fmt.Errorf("unsupported installed VM receipt schema")
	}
	if receipt.Payload != strings.TrimSpace(service.VM.Image.Payload) || receipt.Version != strings.TrimSpace(service.VM.Image.Version) {
		return fmt.Errorf("installed VM receipt contradicts stored payload or version")
	}
	if err := validateTrustedVMImageRepoURL(receipt.CatalogURL, "catalog"); err != nil {
		return err
	}
	if err := validateTrustedVMImageRepoURL(receipt.ManifestURL, "manifest"); err != nil {
		return err
	}
	return nil
}

func validateVMRuntimeAdoptionReceiptDigests(receipt vmRuntimeAdoptionReceipt, manifest vmRuntimeAdoptionManifestState, rootFS vmRuntimeAdoptionFileEvidence) error {
	digests := []struct {
		label string
		value string
	}{
		{label: "catalog", value: receipt.CatalogSHA256},
		{label: "manifest", value: receipt.ManifestSHA256},
		{label: "prepared rootfs", value: receipt.PreparedRootFSSHA256},
	}
	for _, digest := range digests {
		if !isLowerSHA256(digest.value) {
			return fmt.Errorf("installed VM receipt %s digest must be an exact lowercase SHA-256", digest.label)
		}
	}
	if receipt.ManifestSHA256 != manifest.SHA256 {
		return fmt.Errorf("installed VM receipt manifest digest does not match exact installed bytes")
	}
	if receipt.PreparedRootFS != rootFS.Path || receipt.PreparedRootFSSHA256 != rootFS.SHA256 {
		return fmt.Errorf("installed VM receipt does not bind the effective prepared rootfs")
	}
	return nil
}

func readExactLocalVMRuntimeAdoptionRef(path, cacheRoot, name, serviceRoot string, service db.Service, manifest vmRuntimeAdoptionManifestState, rootFS, kernel, firecracker, jailer vmRuntimeAdoptionFileEvidence, deps vmRuntimeAdoptionEvidenceDeps) (localVMImageRef, vmRuntimeAdoptionFileEvidence, error) {
	raw, refEvidence, err := readTrustedVMRuntimeAdoptionFile(path, vmRuntimeAdoptionMetadataMaxBytes, deps)
	if err != nil {
		return localVMImageRef{}, refEvidence, fmt.Errorf("read installed local VM ref: %w", err)
	}
	var ref localVMImageRef
	if err := decodeStrictVMRuntimeAdoptionJSON(raw, &ref); err != nil {
		return ref, refEvidence, fmt.Errorf("decode installed local VM ref: %w", err)
	}
	if err := validateLocalVMRuntimeAdoptionRefMetadata(ref, cacheRoot, name, service); err != nil {
		return ref, refEvidence, err
	}
	paths, err := validateLocalVMRuntimeAdoptionRefBinding(ref, serviceRoot, service, manifest, rootFS, kernel, firecracker, jailer)
	if err != nil {
		return ref, refEvidence, err
	}
	contentID, legacyID, err := trustedLocalVMRuntimeAdoptionContentIDs(ref, manifest.Manifest, paths, deps)
	if err != nil {
		return ref, refEvidence, err
	}
	if ref.ContentID != contentID && ref.ContentID != legacyID {
		return ref, refEvidence, fmt.Errorf("installed local VM ref content ID mismatch")
	}
	return ref, refEvidence, nil
}

func validateLocalVMRuntimeAdoptionRefMetadata(ref localVMImageRef, cacheRoot, name string, service db.Service) error {
	if !isLowerSHA256(ref.ContentID) {
		return fmt.Errorf("installed local VM ref content ID must be an exact lowercase SHA-256")
	}
	if ref.Root != strings.TrimSpace(ref.Root) || !filepath.IsAbs(ref.Root) || filepath.Clean(ref.Root) != ref.Root {
		return fmt.Errorf("installed local VM ref root must be clean, absolute, and trimmed")
	}
	if ref.CreatedAt != strings.TrimSpace(ref.CreatedAt) {
		return fmt.Errorf("installed local VM ref creation time is malformed")
	}
	if _, err := time.Parse(time.RFC3339Nano, ref.CreatedAt); err != nil {
		return fmt.Errorf("installed local VM ref creation time is malformed: %w", err)
	}
	if err := validateResolvedLocalVMImageRefIdentity(name, ref); err != nil {
		return err
	}
	if err := validateLocalVMImageRefRoot(cacheRoot, ref); err != nil {
		return err
	}
	if ref.Version != strings.TrimSpace(service.VM.Image.Version) {
		return fmt.Errorf("installed local VM ref version contradicts stored image version")
	}
	return nil
}

func validateLocalVMRuntimeAdoptionRefBinding(ref localVMImageRef, serviceRoot string, service db.Service, manifest vmRuntimeAdoptionManifestState, rootFS, kernel, firecracker, jailer vmRuntimeAdoptionFileEvidence) (vmImagePaths, error) {
	if err := validateLocalVMRuntimeAdoptionManifestBinding(ref, manifest); err != nil {
		return vmImagePaths{}, err
	}
	if err := validateLocalVMRuntimeAdoptionArtifactNames(ref); err != nil {
		return vmImagePaths{}, err
	}
	paths := localVMRuntimeAdoptionPaths(ref, manifest, firecracker, jailer)
	if err := validateLocalVMRuntimeAdoptionEffectivePaths(ref, serviceRoot, service, paths, rootFS, kernel, firecracker, jailer); err != nil {
		return vmImagePaths{}, err
	}
	return paths, nil
}

func validateLocalVMRuntimeAdoptionManifestBinding(ref localVMImageRef, manifest vmRuntimeAdoptionManifestState) error {
	if manifest.Path != filepath.Join(ref.Root, "manifest.json") {
		return fmt.Errorf("installed local VM ref does not bind the effective manifest")
	}
	if ref.RootFS != manifest.Manifest.RootFS || ref.Kernel != manifest.Manifest.Kernel || ref.Firecracker != manifest.Manifest.Firecracker || ref.Jailer != manifest.Manifest.Jailer {
		return fmt.Errorf("installed local VM ref artifacts contradict the exact installed manifest")
	}
	if ref.KernelPolicy != manifest.Manifest.KernelPolicy || ref.KernelPolicy != localVMImageKernelPolicyManaged && ref.KernelPolicy != localVMImageKernelPolicyLocal {
		return fmt.Errorf("installed local VM ref kernel policy contradicts the exact installed manifest")
	}
	return nil
}

func validateLocalVMRuntimeAdoptionArtifactNames(ref localVMImageRef) error {
	for _, artifact := range []string{ref.RootFS, ref.Kernel, ref.Firecracker, ref.Jailer} {
		if err := validateVMImageArtifactName(artifact); err != nil {
			return fmt.Errorf("installed local VM ref artifact: %w", err)
		}
	}
	return nil
}

func localVMRuntimeAdoptionPaths(ref localVMImageRef, manifest vmRuntimeAdoptionManifestState, firecracker, jailer vmRuntimeAdoptionFileEvidence) vmImagePaths {
	return vmImagePaths{
		Manifest:        manifest.Path,
		Dir:             ref.Root,
		RootFSPath:      filepath.Join(ref.Root, ref.RootFS),
		KernelPath:      filepath.Join(ref.Root, ref.Kernel),
		FirecrackerPath: firecracker.Path,
		JailerPath:      jailer.Path,
	}
}

func validateLocalVMRuntimeAdoptionEffectivePaths(ref localVMImageRef, serviceRoot string, service db.Service, paths vmImagePaths, rootFS, kernel, firecracker, jailer vmRuntimeAdoptionFileEvidence) error {
	if ref.Kernel == "" || kernel.Path != paths.KernelPath && !isVMRuntimeAdoptionSyncedKernel(serviceRoot, service.Name, kernel.Path) {
		return fmt.Errorf("installed local VM ref does not bind the effective kernel")
	}
	if firecracker.Path != filepath.Join(ref.Root, ref.Firecracker) || jailer.Path != filepath.Join(ref.Root, ref.Jailer) {
		return fmt.Errorf("installed local VM ref does not bind the effective runtime pair")
	}
	prepared, _ := vmRootFSDecompressedPath(paths.RootFSPath)
	if rootFS.Path != paths.RootFSPath && rootFS.Path != prepared {
		return fmt.Errorf("installed local VM ref does not bind the prepared rootfs")
	}
	return nil
}

func trustedLocalVMRuntimeAdoptionContentIDs(ref localVMImageRef, manifest vmImageManifest, paths vmImagePaths, deps vmRuntimeAdoptionEvidenceDeps) (string, string, error) {
	capabilities := localVMImageCapabilitiesFromManifest(manifest)
	current, err := hashTrustedLocalVMRuntimeAdoptionContent(ref.Name, []string{paths.RootFSPath, paths.KernelPath, paths.FirecrackerPath, paths.JailerPath}, capabilities, hashLocalVMImageCapabilities, deps)
	if err != nil {
		return "", "", err
	}
	legacy, err := hashTrustedLocalVMRuntimeAdoptionContent(ref.Name, []string{paths.RootFSPath, paths.KernelPath, paths.FirecrackerPath}, capabilities, hashLegacyLocalVMImageCapabilities, deps)
	return current, legacy, err
}

func hashTrustedLocalVMRuntimeAdoptionContent(name string, paths []string, capabilities localVMImageManifestCapabilities, hashCapabilities func(io.Writer, localVMImageManifestCapabilities) error, deps vmRuntimeAdoptionEvidenceDeps) (string, error) {
	hasher := sha256.New()
	if _, err := io.WriteString(hasher, name); err != nil {
		return "", err
	}
	if err := hashCapabilities(hasher, capabilities); err != nil {
		return "", err
	}
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			return "", fmt.Errorf("local VM content identity has an empty artifact path")
		}
		if _, err := hasher.Write([]byte{0}); err != nil {
			return "", err
		}
		if err := hashTrustedVMRuntimeAdoptionFileInto(hasher, path, deps); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func vmRuntimeAdoptionIdentityNames(service db.Service, root string, manifest vmRuntimeAdoptionManifestState, kernelPath string) (string, string) {
	guestName := strings.TrimSpace(service.VM.Image.Distro)
	if manifest.Present && strings.TrimSpace(manifest.Manifest.Name) != "" {
		guestName = manifest.Manifest.Name
	}
	if guestName == "" {
		guestName = strings.TrimPrefix(strings.TrimSpace(service.VM.Image.Payload), vmImagePayloadPrefix)
	}
	if guestName == "" {
		guestName = "legacy"
	}
	kernelVersion := ""
	if manifest.Present && kernelPath == filepath.Join(filepath.Dir(manifest.Path), manifest.Manifest.Kernel) {
		kernelVersion = manifest.Manifest.KernelVersion
	}
	if kernelVersion == "" && isVMRuntimeAdoptionSyncedKernel(root, service.Name, kernelPath) {
		kernelVersion = filepath.Base(filepath.Dir(kernelPath))
	}
	if kernelVersion == "" {
		kernelVersion = service.VM.Image.Version
	}
	return guestName, kernelVersion
}

func vmRuntimeAdoptionComponentManifestDigests(source vmRuntimeAdoptionSourceState, manifest vmRuntimeAdoptionManifestState, compositionSHA string, rootFS, kernel, firecracker, jailer vmRuntimeAdoptionFileEvidence) (string, string, string) {
	guest, kernelDigest, runtimeDigest := compositionSHA, compositionSHA, compositionSHA
	if !manifest.Present {
		return guest, kernelDigest, runtimeDigest
	}
	dir := filepath.Dir(manifest.Path)
	if source.receipt != nil && source.receipt.PreparedRootFS == rootFS.Path && source.receipt.PreparedRootFSSHA256 == rootFS.SHA256 {
		guest = manifest.SHA256
	} else if vmRuntimeAdoptionManifestArtifactMatches(dir, manifest.Manifest.RootFS, manifest.Manifest.Checksums[manifest.Manifest.RootFS], rootFS) {
		guest = manifest.SHA256
	}
	if vmRuntimeAdoptionManifestArtifactMatches(dir, manifest.Manifest.Kernel, manifest.Manifest.Checksums[manifest.Manifest.Kernel], kernel) {
		kernelDigest = manifest.SHA256
	}
	if vmRuntimeAdoptionManifestArtifactMatches(dir, manifest.Manifest.Firecracker, manifest.Manifest.Checksums[manifest.Manifest.Firecracker], firecracker) &&
		vmRuntimeAdoptionManifestArtifactMatches(dir, manifest.Manifest.Jailer, manifest.Manifest.Checksums[manifest.Manifest.Jailer], jailer) {
		runtimeDigest = manifest.SHA256
	}
	return guest, kernelDigest, runtimeDigest
}

func vmRuntimeAdoptionManifestArtifactMatches(dir, name, checksum string, evidence vmRuntimeAdoptionFileEvidence) bool {
	return evidence.Path == filepath.Join(dir, name) && strings.EqualFold(evidence.SHA256, checksum)
}

func normalizeVMRuntimeAdoptionEvidence(files []vmRuntimeAdoptionFileEvidence) ([]vmRuntimeAdoptionFileEvidence, error) {
	byPath := make(map[string]vmRuntimeAdoptionFileEvidence, len(files))
	for _, file := range files {
		if !file.Exists {
			continue
		}
		if err := validateVMRuntimeAdoptionEvidencePath(file.Path); err != nil {
			return nil, err
		}
		if prior, ok := byPath[file.Path]; ok && prior != file {
			return nil, fmt.Errorf("VM adoption evidence for %s is contradictory", file.Path)
		}
		byPath[file.Path] = file
	}
	result := make([]vmRuntimeAdoptionFileEvidence, 0, len(byPath))
	for _, file := range byPath {
		result = append(result, file)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

func vmRuntimeAdoptionPreconditionDigest(cfg Config, service db.Service, preparation vmRuntimeAdoptionPreparation) (string, error) {
	fragmentPaths := make([]string, 0, len(preparation.EffectiveUnit.Fragments))
	for _, fragment := range preparation.EffectiveUnit.Fragments {
		fragmentPaths = append(fragmentPaths, fragment.Path)
	}
	precondition := vmRuntimeAdoptionPrecondition{
		Schema: vmRuntimeAdoptionPreconditionSchema, SchemaVersion: vmRuntimeAdoptionPreconditionVersion,
		Service: service.Name, ServiceRoot: preparation.ServiceRoot, ServiceRootZFS: service.ServiceRootZFS,
		DataRoot: cfg.RootDir, ServicesRoot: cfg.ServicesRoot, JailerBase: preparation.EffectiveUnit.JailerBase,
		Runtime: service.VM.Runtime, SetupState: service.VM.SetupState, Image: service.VM.Image, Disk: service.VM.Disk,
		StoredService: vmRuntimeAdoptionStoredServicePrecondition(service),
		OldDB: vmRuntimeJournalDBProjection{
			Components:  service.VM.Components.Clone(),
			ImageKernel: service.VM.Image.Kernel,
		},
		Unit: vmRuntimeAdoptionUnitPrecondition{
			Name: preparation.EffectiveUnit.Name, Runner: preparation.EffectiveUnit.Runner, JailerBase: preparation.EffectiveUnit.JailerBase,
			ExecStart: append([]string(nil), preparation.EffectiveUnit.ExecStart...),
			Fragments: fragmentPaths, ActiveState: preparation.EffectiveUnit.ActiveState, MainPID: preparation.EffectiveUnit.MainPID,
			NeedDaemonReload: preparation.EffectiveUnit.NeedDaemonReload,
		},
		Evidence: preparation.Evidence,
	}
	raw, err := json.Marshal(precondition)
	if err != nil {
		return "", fmt.Errorf("encode VM runtime adoption precondition: %w", err)
	}
	return vmLegacySHA256Bytes(raw), nil
}

func vmRuntimeAdoptionStoredServicePrecondition(service db.Service) vmRuntimeAdoptionStoredPrecondition {
	result := vmRuntimeAdoptionStoredPrecondition{
		Name: service.Name, ServiceType: service.ServiceType,
		ServiceRoot: service.ServiceRoot, ServiceRootZFS: service.ServiceRootZFS,
	}
	if service.VM != nil {
		result.VM = service.VM.Clone()
		result.VM.Components = nil
		result.VM.Balloon.LastTargetBytes = 0
	}
	return result
}

func inspectVMRuntimeAdoptionPair(ctx context.Context, firecracker, jailer string) (string, error) {
	if err := validateVMJailerRuntimePair(ctx, firecracker, jailer); err != nil {
		return "", err
	}
	raw, err := probeVMRuntimeVersion(ctx, firecracker)
	if err != nil {
		return "", err
	}
	match := vmJailerVersionPattern.FindStringSubmatch(raw)
	if len(match) < 2 {
		return "", fmt.Errorf("firecracker version output is unrecognized")
	}
	return strings.TrimPrefix(match[1], "v"), nil
}

func cloneVMRuntimeAdoptionLoadedUnit(unit vmRuntimeAdoptionLoadedUnit) vmRuntimeAdoptionLoadedUnit {
	clone := unit
	clone.ExecStart = append([]string(nil), unit.ExecStart...)
	clone.Fragments = append([]vmRuntimeAdoptionUnitFragment(nil), unit.Fragments...)
	return clone
}

func loadEffectiveVMRuntimeAdoptionUnit(ctx context.Context, unit string) (vmRuntimeAdoptionLoadedUnit, error) {
	if strings.TrimSpace(unit) == "" || filepath.Base(unit) != unit {
		return vmRuntimeAdoptionLoadedUnit{}, fmt.Errorf("VM unit name is invalid")
	}
	paths, err := discoverVMRuntimeAdoptionUnitFragments(ctx, unit)
	if err != nil {
		return vmRuntimeAdoptionLoadedUnit{}, err
	}
	argv, fragments, err := readVMRuntimeAdoptionUnitFragments(paths)
	if err != nil {
		return vmRuntimeAdoptionLoadedUnit{}, err
	}
	activeState, mainPID, needReload, err := readVMRuntimeAdoptionUnitState(ctx, unit)
	if err != nil {
		return vmRuntimeAdoptionLoadedUnit{}, err
	}
	return vmRuntimeAdoptionLoadedUnit{
		Name: unit, ExecStart: argv, Fragments: fragments, ActiveState: activeState, MainPID: mainPID,
		NeedDaemonReload: needReload,
	}, nil
}

func discoverVMRuntimeAdoptionUnitFragments(ctx context.Context, unit string) ([]string, error) {
	command := exec.CommandContext(ctx, "systemctl", "cat", "--no-pager", unit)
	raw, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("systemctl cat %s: %w", unit, err)
	}
	_, paths, err := parseVMRuntimeAdoptionUnit(raw)
	if err != nil {
		return nil, err
	}
	return paths, nil
}

func readVMRuntimeAdoptionUnitFragments(paths []string) ([]string, []vmRuntimeAdoptionUnitFragment, error) {
	deps := defaultVMRuntimeAdoptionEvidenceDeps()
	fragments := make([]vmRuntimeAdoptionUnitFragment, 0, len(paths))
	var trustedUnit bytes.Buffer
	for _, path := range paths {
		fragmentRaw, evidence, err := readTrustedVMRuntimeAdoptionFile(path, vmRuntimeAdoptionMetadataMaxBytes, deps)
		if err != nil {
			return nil, nil, fmt.Errorf("inventory loaded unit fragment %s: %w", path, err)
		}
		fragments = append(fragments, vmRuntimeAdoptionUnitFragment{Path: path, Evidence: evidence})
		_, _ = fmt.Fprintf(&trustedUnit, "# %s\n", path)
		trustedUnit.Write(fragmentRaw)
		if len(fragmentRaw) == 0 || fragmentRaw[len(fragmentRaw)-1] != '\n' {
			trustedUnit.WriteByte('\n')
		}
	}
	argv, trustedPaths, err := parseVMRuntimeAdoptionUnit(trustedUnit.Bytes())
	if err != nil {
		return nil, nil, fmt.Errorf("parse trusted loaded VM unit fragments: %w", err)
	}
	if !slicesEqualVMRuntimeAdoptionStrings(paths, trustedPaths) {
		return nil, nil, fmt.Errorf("loaded VM unit fragment set changed while it was inventoried")
	}
	return argv, fragments, nil
}

func readVMRuntimeAdoptionUnitState(ctx context.Context, unit string) (string, int, string, error) {
	activeState, err := systemctlVMRuntimeAdoptionProperty(ctx, unit, "ActiveState")
	if err != nil {
		return "", 0, "", err
	}
	mainPIDRaw, err := systemctlVMRuntimeAdoptionProperty(ctx, unit, "MainPID")
	if err != nil {
		return "", 0, "", err
	}
	mainPID, err := strconv.Atoi(mainPIDRaw)
	if err != nil || mainPID < 0 {
		return "", 0, "", fmt.Errorf("systemctl MainPID for %s is invalid", unit)
	}
	needReload, err := systemctlVMRuntimeAdoptionProperty(ctx, unit, "NeedDaemonReload")
	if err != nil {
		return "", 0, "", err
	}
	if needReload != "yes" && needReload != "no" {
		return "", 0, "", fmt.Errorf("systemctl NeedDaemonReload for %s is invalid: %q", unit, needReload)
	}
	return activeState, mainPID, needReload, nil
}

func slicesEqualVMRuntimeAdoptionStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func systemctlVMRuntimeAdoptionProperty(ctx context.Context, unit, property string) (string, error) {
	raw, err := exec.CommandContext(ctx, "systemctl", "show", "--property="+property, "--value", unit).Output()
	if err != nil {
		return "", fmt.Errorf("systemctl show %s for %s: %w", property, unit, err)
	}
	value := strings.TrimSpace(string(raw))
	if value == "" || strings.ContainsAny(value, "\r\n\x00") {
		return "", fmt.Errorf("systemctl %s for %s is empty or malformed", property, unit)
	}
	return value, nil
}

func parseVMRuntimeAdoptionUnit(raw []byte) ([]string, []string, error) {
	if len(raw) == 0 || len(raw) > int(vmRuntimeAdoptionMetadataMaxBytes) || !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return nil, nil, fmt.Errorf("loaded VM unit text is empty, oversized, or malformed")
	}
	lines, err := vmRuntimeAdoptionLogicalUnitLines(string(raw))
	if err != nil {
		return nil, nil, err
	}
	state := vmRuntimeAdoptionUnitParser{pathSet: make(map[string]struct{})}
	for _, line := range lines {
		if err := state.consume(line); err != nil {
			return nil, nil, err
		}
	}
	if len(state.execStarts) != 1 {
		return nil, nil, fmt.Errorf("loaded VM unit must have exactly one effective ExecStart, got %d", len(state.execStarts))
	}
	if len(state.paths) == 0 {
		return nil, nil, fmt.Errorf("loaded VM unit text has no fragment paths")
	}
	return state.execStarts[0], state.paths, nil
}

type vmRuntimeAdoptionUnitParser struct {
	section    string
	execStarts [][]string
	pathSet    map[string]struct{}
	paths      []string
}

func (state *vmRuntimeAdoptionUnitParser) consume(line string) error {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "# ") {
		return state.recordFragmentPath(strings.TrimSpace(strings.TrimPrefix(trimmed, "# ")))
	}
	if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
		return nil
	}
	if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
		state.section = strings.TrimSpace(trimmed[1 : len(trimmed)-1])
		return nil
	}
	if state.section != "Service" {
		return nil
	}
	return state.consumeServiceDirective(line)
}

func (state *vmRuntimeAdoptionUnitParser) recordFragmentPath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil
	}
	if _, exists := state.pathSet[path]; exists {
		return fmt.Errorf("loaded VM unit repeats fragment path %s", path)
	}
	state.pathSet[path] = struct{}{}
	state.paths = append(state.paths, path)
	return nil
}

func (state *vmRuntimeAdoptionUnitParser) consumeServiceDirective(line string) error {
	key, value, ok := strings.Cut(line, "=")
	if !ok || strings.TrimSpace(key) != "ExecStart" {
		return nil
	}
	value = strings.TrimSpace(value)
	if value == "" {
		state.execStarts = nil
		return nil
	}
	argv, err := parseVMRuntimeAdoptionExecStart(value)
	if err != nil {
		return fmt.Errorf("parse loaded VM unit ExecStart: %w", err)
	}
	state.execStarts = append(state.execStarts, argv)
	return nil
}

func vmRuntimeAdoptionLogicalUnitLines(raw string) ([]string, error) {
	physical := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	logical := make([]string, 0, len(physical))
	var current strings.Builder
	for _, line := range physical {
		if current.Len() > 0 {
			current.WriteByte(' ')
		}
		continued := vmRuntimeAdoptionLineContinues(line)
		if continued {
			line = line[:len(line)-1]
		}
		current.WriteString(line)
		if continued {
			continue
		}
		logical = append(logical, current.String())
		current.Reset()
	}
	if current.Len() != 0 {
		return nil, fmt.Errorf("loaded VM unit ends in a line continuation")
	}
	return logical, nil
}

func vmRuntimeAdoptionLineContinues(line string) bool {
	count := 0
	for i := len(line) - 1; i >= 0 && line[i] == '\\'; i-- {
		count++
	}
	return count%2 == 1
}

func parseVMRuntimeAdoptionExecStart(value string) ([]string, error) {
	parser := vmRuntimeAdoptionExecParser{value: value}
	return parser.parse()
}

type vmRuntimeAdoptionExecParser struct {
	value  string
	args   []string
	word   strings.Builder
	inWord bool
	quote  byte
	index  int
}

func (parser *vmRuntimeAdoptionExecParser) parse() ([]string, error) {
	for parser.index < len(parser.value) {
		if err := parser.consume(); err != nil {
			return nil, err
		}
	}
	if parser.quote != 0 {
		return nil, fmt.Errorf("unterminated quote")
	}
	parser.finishWord()
	if len(parser.args) == 0 || strings.TrimSpace(parser.args[0]) == "" {
		return nil, fmt.Errorf("empty command")
	}
	return parser.args, nil
}

func (parser *vmRuntimeAdoptionExecParser) consume() error {
	c := parser.value[parser.index]
	if parser.consumeUnquotedWhitespace(c) {
		return nil
	}
	switch c {
	case '\'', '"':
		if !parser.consumeQuote(c) {
			parser.writeByte(c)
		}
	case '\\':
		return parser.consumeEscape()
	case '%':
		return parser.consumeDoubledLiteral('%', "unresolved systemd specifier")
	case '$':
		return parser.consumeDoubledLiteral('$', "unresolved systemd environment expansion")
	default:
		parser.writeByte(c)
	}
	return nil
}

func (parser *vmRuntimeAdoptionExecParser) consumeUnquotedWhitespace(value byte) bool {
	if parser.quote != 0 || value != ' ' && value != '\t' {
		return false
	}
	parser.finishWord()
	parser.index++
	return true
}

func (parser *vmRuntimeAdoptionExecParser) finishWord() {
	if !parser.inWord {
		return
	}
	parser.args = append(parser.args, parser.word.String())
	parser.word.Reset()
	parser.inWord = false
}

func (parser *vmRuntimeAdoptionExecParser) consumeQuote(value byte) bool {
	if parser.quote == 0 {
		parser.quote = value
		parser.inWord = true
		parser.index++
		return true
	}
	if parser.quote != value {
		return false
	}
	parser.quote = 0
	parser.index++
	return true
}

func (parser *vmRuntimeAdoptionExecParser) consumeEscape() error {
	decoded, consumed, err := decodeVMRuntimeAdoptionEscape(parser.value[parser.index:])
	if err != nil {
		return err
	}
	parser.word.WriteString(decoded)
	parser.inWord = true
	parser.index += consumed
	return nil
}

func (parser *vmRuntimeAdoptionExecParser) consumeDoubledLiteral(value byte, errorMessage string) error {
	if parser.index+1 >= len(parser.value) || parser.value[parser.index+1] != value {
		return errors.New(errorMessage)
	}
	parser.word.WriteByte(value)
	parser.inWord = true
	parser.index += 2
	return nil
}

func (parser *vmRuntimeAdoptionExecParser) writeByte(value byte) {
	parser.word.WriteByte(value)
	parser.inWord = true
	parser.index++
}

func decodeVMRuntimeAdoptionEscape(value string) (string, int, error) {
	if len(value) < 2 {
		return "", 0, fmt.Errorf("trailing escape")
	}
	if decoded, ok := vmRuntimeAdoptionSimpleEscape(value[1]); ok {
		return decoded, 2, nil
	}
	if value[1] != 'x' {
		return "", 0, fmt.Errorf("unsupported escape \\%c", value[1])
	}
	if len(value) < 4 {
		return "", 0, fmt.Errorf("short hexadecimal escape")
	}
	decoded, err := strconv.ParseUint(value[2:4], 16, 8)
	if err != nil || decoded == 0 {
		return "", 0, fmt.Errorf("invalid hexadecimal escape")
	}
	return string([]byte{byte(decoded)}), 4, nil
}

func vmRuntimeAdoptionSimpleEscape(value byte) (string, bool) {
	escapes := map[byte]string{
		'a': "\a", 'b': "\b", 'f': "\f", 'n': "\n", 'r': "\r", 's': " ", 't': "\t", 'v': "\v",
		'\\': "\\", '"': "\"", '\'': "'",
	}
	decoded, ok := escapes[value]
	return decoded, ok
}

func collectTrustedVMRuntimeAdoptionFileEvidence(path string, required bool, deps vmRuntimeAdoptionEvidenceDeps) (vmRuntimeAdoptionFileEvidence, error) {
	deps = completeVMRuntimeAdoptionEvidenceDeps(deps)
	if err := validateTrustedVMRuntimeAdoptionAncestors(path, deps.trustedUID); err != nil {
		return vmRuntimeAdoptionFileEvidence{Path: path}, err
	}
	return collectVMRuntimeAdoptionFileEvidence(path, required, deps)
}

// validateTrustedVMRuntimeAdoptionAncestors closes the rename gap between a
// measured root-owned file and the path that a future launch will reopen. Root
// and the effective trusted owner are both accepted so tests and non-root
// inventory helpers can use private trees, but no ancestor may be writable by
// another group or account.
func validateTrustedVMRuntimeAdoptionAncestors(path string, trustedUID uint32) error {
	return validateTrustedVMRuntimeAdoptionAncestorsWithMissing(path, trustedUID, false)
}

func validateTrustedVMRuntimeAdoptionAncestorsWithMissing(path string, trustedUID uint32, allowMissing bool) error {
	if err := validateVMRuntimeAdoptionEvidencePath(path); err != nil {
		return err
	}
	for ancestor := filepath.Dir(path); ; ancestor = filepath.Dir(ancestor) {
		dir, parent, _, err := openVMRuntimeAdoptionPath(ancestor)
		if err != nil {
			if missingErr := vmRuntimeAdoptionMissingAncestorError(ancestor, err, allowMissing); missingErr == nil {
				continue
			} else {
				return missingErr
			}
		}
		metadata, inspectErr := vmRuntimeAdoptionMetadataForFile(dir, ancestor)
		closeErr := closeVMRuntimeAdoptionPath(dir, parent)
		if inspectErr != nil || closeErr != nil {
			return errors.Join(inspectErr, closeErr)
		}
		if err := validateVMRuntimeAdoptionAncestorMetadata(ancestor, metadata, trustedUID); err != nil {
			return err
		}
		if ancestor == string(filepath.Separator) {
			return nil
		}
	}
}

func vmRuntimeAdoptionMissingAncestorError(ancestor string, err error, allowMissing bool) error {
	if !allowMissing || !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("open trusted VM adoption ancestor %s: %w", ancestor, err)
	}
	if ancestor == string(filepath.Separator) {
		return fmt.Errorf("filesystem root is missing")
	}
	return nil
}

func validateVMRuntimeAdoptionAncestorMetadata(ancestor string, metadata vmRuntimeAdoptionFileMetadata, trustedUID uint32) error {
	if metadata.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("trusted VM adoption ancestor %s must be a directory", ancestor)
	}
	if metadata.UID != 0 && metadata.UID != trustedUID {
		return fmt.Errorf("trusted VM adoption ancestor %s owner UID is %d, want 0 or %d", ancestor, metadata.UID, trustedUID)
	}
	if metadata.Mode&0o022 != 0 {
		return fmt.Errorf("trusted VM adoption ancestor %s is group or other writable", ancestor)
	}
	return nil
}

func readTrustedVMRuntimeAdoptionFile(path string, maxBytes int64, deps vmRuntimeAdoptionEvidenceDeps) (raw []byte, evidence vmRuntimeAdoptionFileEvidence, retErr error) {
	deps = completeVMRuntimeAdoptionEvidenceDeps(deps)
	if err := validateTrustedVMRuntimeAdoptionAncestors(path, deps.trustedUID); err != nil {
		return nil, evidence, err
	}
	maxBytes = effectiveVMRuntimeAdoptionReadLimit(maxBytes, deps.maxFileBytes)
	file, parent, name, err := openVMRuntimeAdoptionPath(path)
	if err != nil {
		return nil, evidence, err
	}
	defer func() { retErr = errors.Join(retErr, closeVMRuntimeAdoptionPath(file, parent)) }()
	before, err := validateOpenTrustedVMRuntimeAdoptionFile(file, path, maxBytes, deps.trustedUID)
	if err != nil {
		return nil, evidence, err
	}
	raw, err = io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, evidence, fmt.Errorf("read trusted VM adoption file %s: %w", path, err)
	}
	if vmRuntimeAdoptionReadSizeChanged(len(raw), before.Size, maxBytes) {
		return nil, evidence, fmt.Errorf("trusted VM adoption file %s changed size while reading", path)
	}
	after, err := vmRuntimeAdoptionMetadataForFile(file, path)
	if err != nil {
		return nil, evidence, err
	}
	if before != after {
		return nil, evidence, fmt.Errorf("trusted VM adoption file %s changed while reading", path)
	}
	evidence = vmRuntimeAdoptionFileEvidence{
		Path: path, Exists: true, Device: before.Device, Inode: before.Inode, Size: before.Size,
		Mode: before.Mode, UID: before.UID, GID: before.GID, MTimeNS: before.MTimeNS,
		SHA256: vmLegacySHA256Bytes(raw),
	}
	if err := revalidateVMRuntimeAdoptionName(parent, name, evidence.metadata()); err != nil {
		return nil, vmRuntimeAdoptionFileEvidence{Path: path}, fmt.Errorf("revalidate trusted VM adoption file %s: %w", path, err)
	}
	return raw, evidence, nil
}

func effectiveVMRuntimeAdoptionReadLimit(requested, maximum int64) int64 {
	if requested <= 0 || requested > maximum {
		return maximum
	}
	return requested
}

func vmRuntimeAdoptionReadSizeChanged(actual int, expected, maximum int64) bool {
	return int64(actual) != expected || int64(actual) > maximum
}

func hashTrustedVMRuntimeAdoptionFileInto(writer io.Writer, path string, deps vmRuntimeAdoptionEvidenceDeps) (retErr error) {
	deps = completeVMRuntimeAdoptionEvidenceDeps(deps)
	if err := validateTrustedVMRuntimeAdoptionAncestors(path, deps.trustedUID); err != nil {
		return err
	}
	file, parent, name, err := openVMRuntimeAdoptionPath(path)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, closeVMRuntimeAdoptionPath(file, parent)) }()
	before, err := validateOpenTrustedVMRuntimeAdoptionFile(file, path, deps.maxFileBytes, deps.trustedUID)
	if err != nil {
		return err
	}
	n, err := deps.copy(writer, io.LimitReader(file, deps.maxFileBytes+1))
	if err != nil {
		return fmt.Errorf("read trusted local VM artifact %s: %w", path, err)
	}
	if n != before.Size || n > deps.maxFileBytes {
		return fmt.Errorf("trusted local VM artifact %s changed size while reading", path)
	}
	after, err := vmRuntimeAdoptionMetadataForFile(file, path)
	if err != nil {
		return err
	}
	if before != after {
		return fmt.Errorf("trusted local VM artifact %s changed while reading", path)
	}
	if err := revalidateVMRuntimeAdoptionName(parent, name, before); err != nil {
		return fmt.Errorf("revalidate trusted local VM artifact %s: %w", path, err)
	}
	return nil
}

func validateOpenTrustedVMRuntimeAdoptionFile(file *os.File, path string, maxBytes int64, trustedUID uint32) (vmRuntimeAdoptionFileMetadata, error) {
	metadata, err := vmRuntimeAdoptionMetadataForFile(file, path)
	if err != nil {
		return metadata, err
	}
	if metadata.Mode&unix.S_IFMT != unix.S_IFREG {
		return metadata, fmt.Errorf("trusted VM adoption file %s must be a regular file", path)
	}
	if metadata.UID != trustedUID || metadata.Mode&0o022 != 0 {
		return metadata, fmt.Errorf("trusted VM adoption file %s is not controlled by UID %d", path, trustedUID)
	}
	if metadata.Size < 0 || metadata.Size > maxBytes {
		return metadata, fmt.Errorf("trusted VM adoption file %s exceeds %d bytes", path, maxBytes)
	}
	return metadata, nil
}

func vmRuntimeAdoptionPathPresent(path string, deps vmRuntimeAdoptionEvidenceDeps) (bool, error) {
	deps = completeVMRuntimeAdoptionEvidenceDeps(deps)
	if err := validateTrustedVMRuntimeAdoptionAncestorsWithMissing(path, deps.trustedUID, true); err != nil {
		return false, err
	}
	file, parent, _, err := openVMRuntimeAdoptionPath(path)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return false, nil
		}
		return false, err
	}
	if err := closeVMRuntimeAdoptionPath(file, parent); err != nil {
		return false, err
	}
	return true, nil
}

func decodeStrictVMRuntimeAdoptionJSON(raw []byte, target any) error {
	if len(raw) == 0 || len(raw) > int(vmRuntimeAdoptionMetadataMaxBytes) || !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return fmt.Errorf("JSON is empty, oversized, or malformed")
	}
	if err := rejectDuplicateVMRuntimeAdoptionJSONFields(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return requireVMRuntimeAdoptionJSONEOF(decoder)
}

func rejectDuplicateVMRuntimeAdoptionJSONFields(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := consumeVMRuntimeAdoptionJSONValue(decoder); err != nil {
		return err
	}
	return requireVMRuntimeAdoptionJSONEOF(decoder)
}

func consumeVMRuntimeAdoptionJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		return consumeVMRuntimeAdoptionJSONObject(decoder)
	case '[':
		return consumeVMRuntimeAdoptionJSONArray(decoder)
	default:
		return fmt.Errorf("JSON delimiter %q is malformed", delimiter)
	}
}

func consumeVMRuntimeAdoptionJSONObject(decoder *json.Decoder) error {
	var seen []string
	for decoder.More() {
		key, err := consumeVMRuntimeAdoptionJSONObjectKey(decoder, seen)
		if err != nil {
			return err
		}
		seen = append(seen, key)
		if err := consumeVMRuntimeAdoptionJSONValue(decoder); err != nil {
			return err
		}
	}
	return consumeVMRuntimeAdoptionJSONEnd(decoder, '}', "object")
}

func consumeVMRuntimeAdoptionJSONObjectKey(decoder *json.Decoder, seen []string) (string, error) {
	keyToken, err := decoder.Token()
	if err != nil {
		return "", err
	}
	key, ok := keyToken.(string)
	if !ok {
		return "", fmt.Errorf("JSON object field name is malformed")
	}
	for _, prior := range seen {
		if strings.EqualFold(prior, key) {
			return "", fmt.Errorf("JSON contains duplicate field %q", key)
		}
	}
	return key, nil
}

func consumeVMRuntimeAdoptionJSONArray(decoder *json.Decoder) error {
	for decoder.More() {
		if err := consumeVMRuntimeAdoptionJSONValue(decoder); err != nil {
			return err
		}
	}
	return consumeVMRuntimeAdoptionJSONEnd(decoder, ']', "array")
}

func consumeVMRuntimeAdoptionJSONEnd(decoder *json.Decoder, want rune, label string) error {
	end, err := decoder.Token()
	if err != nil {
		return err
	}
	if end != json.Delim(want) {
		return fmt.Errorf("JSON %s is malformed", label)
	}
	return nil
}

func requireVMRuntimeAdoptionJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("JSON has trailing value")
		}
		return err
	}
	return nil
}
