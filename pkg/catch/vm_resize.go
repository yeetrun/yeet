// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
)

var (
	vmServiceSetHostProfileFunc       func(*Server, string, int64) (vmHostProfile, error)
	vmServiceSetDiskRunner            vmCommandRunner
	vmServiceSetNetworkRunner         vmNetworkCommandRunner
	vmServiceSetMetadataInjector      func(context.Context, string, vmMetadataConfig) error
	isServiceRunningForVMSettings     = (*Server).IsServiceRunning
	vmServiceSetEnsureRuntimeIdentity = ensureVMRuntimeIdentity
)

type vmSettingsPlan struct {
	Service               string
	Root                  string
	OldVM                 db.VMConfig
	NewCPUs               int
	NewMemoryBytes        int64
	NewBalloon            db.VMBalloonConfig
	NewDiskBytes          int64
	DiskSteps             []vmDiskPlanStep
	OldNetwork            vmNetworkPlan
	NewNetwork            vmNetworkPlan
	NetworkChanged        bool
	SvcNetwork            *db.SvcNetwork
	OldMetadata           vmMetadataConfig
	Metadata              vmMetadataConfig
	RewriteMetadata       bool
	OldFirecrackerConfig  []byte
	FirecrackerExisted    bool
	FirecrackerConfigPath string
	FirecrackerConfig     []byte
}

func (s *Server) updateVMServiceSettings(ctx context.Context, name string, flags cli.VMSetFlags) (retErr error) {
	plan, err := s.planVMServiceSettings(name, flags)
	if err != nil {
		return err
	}
	transition, err := s.applyVMServiceSettingsPlan(ctx, plan)
	defer func() {
		if retErr == nil {
			return
		}
		if err := transition.rollback(ctx); err != nil {
			retErr = errors.Join(retErr, err)
		}
	}()
	if err != nil {
		return err
	}
	return s.commitVMServiceSettingsPlan(name, plan)
}

func (s *Server) planVMServiceSettings(name string, flags cli.VMSetFlags) (vmSettingsPlan, error) {
	dv, service, plan, err := s.baseVMSettingsPlan(name)
	if err != nil {
		return vmSettingsPlan{}, err
	}
	if err := s.applyVMShapeSettings(service, flags, &plan); err != nil {
		return vmSettingsPlan{}, err
	}
	if err := applyVMDiskSettings(flags, &plan); err != nil {
		return vmSettingsPlan{}, err
	}
	if err := s.applyVMNetworkSettings(dv, name, service, flags, &plan); err != nil {
		return vmSettingsPlan{}, err
	}
	if err := plan.finalizeFirecrackerSettings(); err != nil {
		return vmSettingsPlan{}, err
	}
	return plan, nil
}

func (s *Server) baseVMSettingsPlan(name string) (*db.DataView, *db.Service, vmSettingsPlan, error) {
	dv, err := s.getDB()
	if err != nil {
		return nil, nil, vmSettingsPlan{}, err
	}
	sv, ok := dv.Services().GetOk(name)
	if !ok {
		return nil, nil, vmSettingsPlan{}, fmt.Errorf("service %q not found", name)
	}
	service := sv.AsStruct()
	if service.ServiceType != db.ServiceTypeVM || service.VM == nil {
		return nil, nil, vmSettingsPlan{}, fmt.Errorf("service %q is not a VM service", name)
	}
	runningCheck := isServiceRunningForVMSettings
	if runningCheck == nil {
		runningCheck = (*Server).IsServiceRunning
	}
	running, err := runningCheck(s, name)
	if err != nil {
		return nil, nil, vmSettingsPlan{}, err
	}
	if running {
		return nil, nil, vmSettingsPlan{}, fmt.Errorf("cannot change VM settings while %q is running; stop it first", name)
	}

	root := s.serviceRootFromView(sv)
	oldVM := *service.VM.Clone()
	identity, err := vmServiceSetEnsureRuntimeIdentity()
	if err != nil {
		return nil, nil, vmSettingsPlan{}, err
	}
	oldNetwork := vmNetworkPlanFromDB(name, oldVM.Networks).WithTapOwner(identity)
	return dv, service, vmSettingsPlan{
		Service:               name,
		Root:                  root,
		OldVM:                 oldVM,
		NewCPUs:               oldVM.CPUs,
		NewMemoryBytes:        oldVM.MemoryBytes,
		NewBalloon:            oldVM.Balloon,
		NewDiskBytes:          oldVM.Disk.Bytes,
		OldNetwork:            oldNetwork,
		NewNetwork:            oldNetwork,
		SvcNetwork:            cloneSvcNetwork(service.SvcNetwork),
		FirecrackerConfigPath: filepath.Join(serviceRunDirForRoot(root), "firecracker.json"),
	}, nil
}

func (s *Server) applyVMShapeSettings(service *db.Service, flags cli.VMSetFlags, plan *vmSettingsPlan) error {
	if flags.CPUs > 0 {
		plan.NewCPUs = flags.CPUs
	}
	if err := applyVMSetMemoryFlag(flags, plan); err != nil {
		return err
	}
	if err := applyVMSetBalloonFlags(flags, plan); err != nil {
		return err
	}
	return s.validateVMSettingsShape(plan.Root, service, plan.NewCPUs, plan.NewMemoryBytes, plan.NewBalloon.MinBytes, vmSetMemoryAdmissionChanged(flags))
}

func applyVMSetMemoryFlag(flags cli.VMSetFlags, plan *vmSettingsPlan) error {
	if strings.TrimSpace(flags.Memory) == "" {
		return nil
	}
	memoryBytes, err := parseVMSize(flags.Memory)
	if err != nil {
		return err
	}
	plan.NewMemoryBytes = memoryBytes
	return nil
}

func applyVMSetBalloonFlags(flags cli.VMSetFlags, plan *vmSettingsPlan) error {
	balloonChanged := vmSetBalloonConfigChanged(flags)
	if mode := strings.TrimSpace(flags.Balloon); mode != "" {
		normalized, err := normalizeVMBalloonMode(mode)
		if err != nil {
			return err
		}
		plan.NewBalloon.Mode = normalized
	}
	if value := strings.TrimSpace(flags.MemoryMin); value != "" {
		minBytes, err := parseVMSize(value)
		if err != nil {
			return fmt.Errorf("invalid --memory-min: %w", err)
		}
		plan.NewBalloon.MinBytes = minBytes
	}
	balloon, err := effectiveVMSetBalloonConfig(plan.NewMemoryBytes, plan.NewBalloon, balloonChanged)
	if err != nil {
		return err
	}
	plan.NewBalloon = balloon
	return nil
}

func vmSetBalloonConfigChanged(flags cli.VMSetFlags) bool {
	return strings.TrimSpace(flags.MemoryMin) != "" ||
		strings.TrimSpace(flags.Balloon) != ""
}

func effectiveVMSetBalloonConfig(memoryBytes int64, cfg db.VMBalloonConfig, balloonChanged bool) (db.VMBalloonConfig, error) {
	if balloonChanged {
		return effectiveVMBalloonConfig(memoryBytes, cfg)
	}
	return effectiveExistingVMBalloonConfig(memoryBytes, cfg)
}

func vmSetMemoryAdmissionChanged(flags cli.VMSetFlags) bool {
	return strings.TrimSpace(flags.Memory) != "" ||
		strings.TrimSpace(flags.MemoryMin) != "" ||
		strings.TrimSpace(flags.Balloon) != ""
}

func applyVMDiskSettings(flags cli.VMSetFlags, plan *vmSettingsPlan) error {
	if strings.TrimSpace(flags.Disk) != "" {
		diskBytes, err := parseVMSize(flags.Disk)
		if err != nil {
			return err
		}
		plan.NewDiskBytes = diskBytes
		steps, err := vmDiskResizeStepsFromConfig(plan.OldVM.Disk, diskBytes)
		if err != nil {
			return err
		}
		plan.DiskSteps = steps
	}
	return nil
}

func (s *Server) applyVMNetworkSettings(dv *db.DataView, name string, service *db.Service, flags cli.VMSetFlags, plan *vmSettingsPlan) error {
	if hasCatchVMSetNetworkChange(flags) {
		network, svcNet, err := s.planVMServiceSetNetwork(dv, name, service, plan.OldVM.Networks, flags)
		if err != nil {
			return err
		}
		plan.NewNetwork = network.WithTapOwner(plan.OldNetwork.TapOwner)
		plan.SvcNetwork = svcNet
		plan.NetworkChanged = true
		plan.RewriteMetadata = true
	}
	return nil
}

func (p *vmSettingsPlan) finalizeFirecrackerSettings() error {
	fc, fastBoot, rawFirecrackerConfig, firecrackerExisted, err := p.readFirecrackerConfig()
	if err != nil {
		return err
	}
	p.OldFirecrackerConfig = append([]byte(nil), rawFirecrackerConfig...)
	p.FirecrackerExisted = firecrackerExisted
	if p.RewriteMetadata {
		oldMetadata, err := vmMetadataForSettings(p.Root, p.Service, p.OldVM, p.OldNetwork, fastBoot)
		if err != nil {
			return err
		}
		metadata, err := vmMetadataForSettings(p.Root, p.Service, p.OldVM, p.NewNetwork, fastBoot)
		if err != nil {
			return err
		}
		p.OldMetadata = oldMetadata
		p.Metadata = metadata
	}
	raw, err := p.renderFirecrackerConfig(fc, fastBoot)
	if err != nil {
		return err
	}
	p.FirecrackerConfig = raw
	return nil
}

func (s *Server) validateVMSettingsShape(root string, service *db.Service, cpus int, memoryBytes, minMemoryBytes int64, admitMemory bool) error {
	if err := validateVMShape(vmShape{CPUs: cpus, MemoryBytes: memoryBytes, MinMemoryBytes: minMemoryBytes, DiskBytes: service.VM.Disk.Bytes, DiskBackend: service.VM.Disk.Backend}); err != nil {
		return err
	}
	if !admitMemory {
		return nil
	}
	runningBytes, runningMinBytes, err := s.runningVMMemoryExcluding(service.Name)
	if err != nil {
		return err
	}
	var profile vmHostProfile
	if vmServiceSetHostProfileFunc != nil {
		profile, err = vmServiceSetHostProfileFunc(s, root, runningBytes)
		if err != nil {
			return err
		}
	} else {
		profile = localVMHostProfile(availableStorageBytes(root), service.ServiceRootZFS != "", runningBytes)
	}
	if runningMinBytes > 0 && profile.RunningVMMinBytes == 0 {
		profile.RunningVMMinBytes = runningMinBytes
	}
	policy, err := s.vmHostMemoryPolicy()
	if err != nil {
		return err
	}
	return admitVMMemory(profile, memoryBytes, minMemoryBytes, policy)
}

func (s *Server) runningVMMemoryExcluding(name string) (int64, int64, error) {
	dv, err := s.getDB()
	if err != nil {
		return 0, 0, err
	}
	var maxTotal int64
	var minTotal int64
	for serviceName, service := range dv.AsStruct().Services {
		if serviceName == name || service == nil || service.VM == nil || service.VM.SetupState != "ready" {
			continue
		}
		maxTotal += service.VM.MemoryBytes
		balloon, err := effectiveExistingVMBalloonConfig(service.VM.MemoryBytes, service.VM.Balloon)
		if err != nil {
			return 0, 0, fmt.Errorf("VM %q balloon config: %w", serviceName, err)
		}
		minTotal += balloon.MinBytes
	}
	return maxTotal, minTotal, nil
}

func vmDiskResizeStepsFromConfig(disk db.VMDiskConfig, requestedBytes int64) ([]vmDiskPlanStep, error) {
	switch disk.Backend {
	case vmDiskBackendZVOL:
		dataset := strings.TrimPrefix(disk.Path, "/dev/zvol/")
		return zvolVMDiskResizeSteps(dataset, disk.Bytes, requestedBytes)
	default:
		return rawVMDiskResizeSteps(disk.Path, disk.Bytes, requestedBytes)
	}
}

func hasCatchVMSetNetworkChange(flags cli.VMSetFlags) bool {
	return flags.NetworkChange ||
		strings.TrimSpace(flags.MacvlanMac) != "" ||
		flags.MacvlanVlan != 0 ||
		strings.TrimSpace(flags.MacvlanParent) != ""
}

func (s *Server) planVMServiceSetNetwork(dv *db.DataView, name string, service *db.Service, current []db.VMNetworkConfig, flags cli.VMSetFlags) (vmNetworkPlan, *db.SvcNetwork, error) {
	netValue := vmNetworkValueForServiceSet(current, flags)
	modes := vmRequestedNetworkModes(netValue)
	if err := validateVMNetworkOptions(modes, flags.MacvlanParent, flags.MacvlanVlan, flags.MacvlanMac); err != nil {
		return vmNetworkPlan{}, nil, err
	}
	svcNet, err := svcNetworkForVMServiceSet(dv, service.SvcNetwork, modes)
	if err != nil {
		return vmNetworkPlan{}, nil, err
	}
	input, err := vmNetworkInputForServiceSet(svcNet, modes, flags)
	if err != nil {
		return vmNetworkPlan{}, nil, err
	}
	return newVMNetworkPlan(name, modes, input), svcNet, nil
}

func vmNetworkValueForServiceSet(current []db.VMNetworkConfig, flags cli.VMSetFlags) string {
	netValue := strings.TrimSpace(flags.Net)
	if netValue == "" && !flags.NetworkChange {
		return vmNetworkModesForServiceSet(current)
	}
	return netValue
}

func svcNetworkForVMServiceSet(dv *db.DataView, current *db.SvcNetwork, modes []string) (*db.SvcNetwork, error) {
	if vmModeListContains(modes, "svc") {
		svcNet := cloneSvcNetwork(current)
		if svcNet == nil || !svcNet.IPv4.IsValid() {
			next, err := svcNetworkFromData(*dv)
			if err != nil {
				return nil, err
			}
			svcNet = next
		}
		return svcNet, nil
	}
	return nil, nil
}

func vmNetworkInputForServiceSet(svcNet *db.SvcNetwork, modes []string, flags cli.VMSetFlags) (vmNetworkInputs, error) {
	input := vmNetworkInputs{
		LANParent: strings.TrimSpace(flags.MacvlanParent),
		LANVLAN:   flags.MacvlanVlan,
		LANMAC:    strings.TrimSpace(flags.MacvlanMac),
	}
	if svcNet != nil && svcNet.IPv4.IsValid() {
		input.ServiceIP = svcNet.IPv4.String()
	}
	if vmModeListContains(modes, "lan") {
		if err := resolveVMLANNetworkInput(&input); err != nil {
			return vmNetworkInputs{}, err
		}
		if input.LANMAC == "" {
			input.LANMAC = randomMAC()
		}
	}
	return input, nil
}

func vmNetworkModesForServiceSet(networks []db.VMNetworkConfig) string {
	modes := make([]string, 0, len(networks))
	for _, network := range networks {
		if strings.TrimSpace(network.Mode) != "" {
			modes = append(modes, strings.TrimSpace(network.Mode))
		}
	}
	if len(modes) == 0 {
		return "svc"
	}
	return strings.Join(modes, ",")
}

func vmNetworkPlanFromDB(service string, networks []db.VMNetworkConfig) vmNetworkPlan {
	plan := vmNetworkPlan{Service: service}
	short := shortVMName(service)
	for i, network := range networks {
		iface := vmNetworkInterfacePlan{
			Mode:      network.Mode,
			GuestName: network.Interface,
			Tap:       network.Tap,
			MAC:       network.MAC,
			Parent:    network.Parent,
			VLAN:      network.VLAN,
		}
		switch network.Mode {
		case "svc":
			iface.Bridge = fmt.Sprintf("yvm-%s-b%d", short, i)
			if network.IP.IsValid() {
				iface.GuestIP = network.IP.String() + "/24"
			}
			iface.Gateway = vmSvcGuestGateway
		case "lan":
			if network.VLAN != 0 {
				if parent, ok := vmNetworkRecoveredDerivedVLANParent(network.Parent, network.VLAN); ok {
					iface.Parent = parent
					iface.Bridge = vmGeneratedVLANBridgeName(parent, network.VLAN)
					iface.VLANDevice = vmGeneratedVLANDeviceName(parent, network.VLAN)
				} else if vmLANParentIsBridge(network.Parent) {
					iface.Bridge = network.Parent
				} else {
					iface.Bridge = vmGeneratedVLANBridgeName(network.Parent, network.VLAN)
					iface.VLANDevice = vmGeneratedVLANDeviceName(network.Parent, network.VLAN)
				}
			} else {
				if vmLANParentIsBridge(network.Parent) {
					iface.Bridge = network.Parent
				}
			}
			iface.DHCP = true
		}
		plan.Interfaces = append(plan.Interfaces, iface)
	}
	plan.applyGuestRoutePolicy()
	return plan
}

func vmNetworkRecoveredDerivedVLANParent(parent string, vlan int) (string, bool) {
	parent = strings.TrimSpace(parent)
	if parent == "" || vlan == 0 || vmLANLiveBridgeExistsFn(parent) {
		return "", false
	}
	base, ok := vmNetworkDerivedVLANBridgeBase(parent, vlan)
	if !ok || !vmLANLiveBridgeExistsFn(base) {
		return "", false
	}
	uplink, err := vmLANBridgeUplinkFn(base)
	if err != nil {
		return "", false
	}
	uplink = strings.TrimSpace(uplink)
	if uplink == "" {
		return "", false
	}
	return uplink, true
}

func vmNetworkDerivedVLANBridgeBase(name string, vlan int) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" || vlan == 0 {
		return "", false
	}
	for _, suffix := range []string{fmt.Sprintf("v%d", vlan), fmt.Sprintf(".%d", vlan)} {
		base := strings.TrimSuffix(name, suffix)
		if base != name && base != "" {
			return base, true
		}
	}
	return "", false
}

func cloneSvcNetwork(in *db.SvcNetwork) *db.SvcNetwork {
	if in == nil {
		return nil
	}
	return &db.SvcNetwork{IPv4: in.IPv4}
}

func (p vmSettingsPlan) readFirecrackerConfig() (firecrackerConfig, bool, []byte, bool, error) {
	raw, err := os.ReadFile(p.FirecrackerConfigPath)
	if os.IsNotExist(err) {
		return p.defaultFirecrackerConfig(), false, nil, false, nil
	}
	if err != nil {
		return firecrackerConfig{}, false, nil, false, err
	}
	if len(raw) == 0 {
		return p.defaultFirecrackerConfig(), false, raw, true, nil
	}
	var cfg firecrackerConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return firecrackerConfig{}, false, nil, false, err
	}
	return cfg, strings.Contains(cfg.BootSource.BootArgs, "init="+vmGuestInitPath), raw, true, nil
}

func (p vmSettingsPlan) defaultFirecrackerConfig() firecrackerConfig {
	return firecrackerConfig{
		BootSource: firecrackerBootSource{
			KernelImagePath: p.OldVM.Image.Kernel,
			BootArgs:        vmLegacyKernelBootArgs,
		},
		Drives: []firecrackerDrive{{
			DriveID:      "rootfs",
			PathOnHost:   p.OldVM.Disk.Path,
			IsRootDevice: true,
			IsReadOnly:   false,
		}},
	}
}

func (p vmSettingsPlan) renderFirecrackerConfig(cfg firecrackerConfig, fastBoot bool) ([]byte, error) {
	cfg.MachineConfig = firecrackerMachineConfig{VCPUCount: p.NewCPUs, MemSizeMib: int(p.NewMemoryBytes >> 20)}
	cfg.NetworkInterfaces = p.NewNetwork.FirecrackerInterfaces()
	cfg.Balloon = firecrackerBalloonFromConfig(p.NewBalloon)
	if len(cfg.Drives) == 0 {
		cfg.Drives = []firecrackerDrive{{
			DriveID:      "rootfs",
			PathOnHost:   p.OldVM.Disk.Path,
			IsRootDevice: true,
			IsReadOnly:   false,
		}}
	} else {
		cfg.Drives[0].PathOnHost = p.OldVM.Disk.Path
	}
	if fastBoot {
		guestSystemInit := guestSystemInitFromBootArgs(cfg.BootSource.BootArgs)
		if guestSystemInit == "" {
			guestSystemInit = strings.TrimSpace(p.OldVM.Image.GuestSystemInit)
		}
		bootArgs, err := vmKernelBootArgs(p.Service, p.NewNetwork, vmImageManifest{
			GuestInit:       vmGuestInitPath,
			GuestSystemInit: guestSystemInit,
		})
		if err != nil {
			return nil, err
		}
		cfg.BootSource.BootArgs = bootArgs
	}
	return renderFirecrackerConfig(cfg)
}

func guestSystemInitFromBootArgs(args string) string {
	for _, field := range strings.Fields(args) {
		if value, ok := strings.CutPrefix(field, "yeet.system_init="); ok {
			return value
		}
	}
	return ""
}

func vmMetadataForSettings(root, service string, vm db.VMConfig, network vmNetworkPlan, fastBoot bool) (vmMetadataConfig, error) {
	user := strings.TrimSpace(vm.SSH.User)
	if user == "" {
		user = strings.TrimSpace(vm.Image.DefaultUser)
	}
	if user == "" {
		user = "ubuntu"
	}
	sshKey, err := os.ReadFile(filepath.Join(root, "metadata", "authorized_keys"))
	if err != nil {
		return vmMetadataConfig{}, fmt.Errorf("read VM metadata authorized_keys: %w", err)
	}
	return vmMetadataConfig{
		Hostname:       service,
		User:           user,
		SSHKey:         strings.TrimSpace(string(sshKey)),
		Networks:       network.MetadataNetworks(),
		FastBoot:       fastBoot,
		MetadataDriver: vmMetadataDriverForExistingVM(vm),
		HostKeyDir:     filepath.Join(root, "metadata", "ssh-host-keys"),
	}, nil
}

func vmMetadataDriverForExistingVM(vm db.VMConfig) string {
	if driver := strings.TrimSpace(vm.Image.MetadataDriver); driver != "" {
		return driver
	}
	return "ubuntu"
}

type vmSettingsApplyResult struct {
	network            vmNetworkTransitionResult
	plan               vmSettingsPlan
	metadataTouched    bool
	firecrackerTouched bool
}

func (r vmSettingsApplyResult) rollback(ctx context.Context) error {
	var retErr error
	if r.firecrackerTouched {
		if err := restoreVMServiceFirecrackerSettings(r.plan); err != nil {
			retErr = errors.Join(retErr, err)
		}
	}
	if r.metadataTouched {
		if err := restoreVMServiceMetadataSettings(ctx, r.plan); err != nil {
			retErr = errors.Join(retErr, err)
		}
	}
	if err := r.network.rollback(); err != nil {
		retErr = errors.Join(retErr, err)
	}
	return retErr
}

func (s *Server) applyVMServiceSettingsPlan(ctx context.Context, plan vmSettingsPlan) (vmSettingsApplyResult, error) {
	result := vmSettingsApplyResult{plan: plan}
	if err := applyVMServiceDiskSettings(ctx, plan); err != nil {
		return result, err
	}
	transition, err := applyVMServiceNetworkSettings(plan)
	result.network = transition
	if err != nil {
		return result, err
	}
	result.metadataTouched = plan.RewriteMetadata
	if err := applyVMServiceMetadataSettings(ctx, plan); err != nil {
		return result, err
	}
	if err := persistVMServiceRuntimeSettings(plan, &result); err != nil {
		return result, err
	}
	return result, nil
}

func persistVMServiceRuntimeSettings(plan vmSettingsPlan, result *vmSettingsApplyResult) error {
	result.firecrackerTouched = true
	if err := writeVMFile(plan.FirecrackerConfigPath, plan.FirecrackerConfig, 0o644); err != nil {
		return err
	}
	return nil
}

func applyVMServiceDiskSettings(ctx context.Context, plan vmSettingsPlan) error {
	if len(plan.DiskSteps) == 0 {
		return nil
	}
	runner := vmServiceSetDiskRunner
	if runner == nil {
		runner = runVMCommand
	}
	diskPlan := vmDiskPlan{Path: plan.OldVM.Disk.Path}
	if plan.OldVM.Disk.Backend == vmDiskBackendZVOL {
		diskPlan.Path = strings.TrimPrefix(plan.OldVM.Disk.Path, "/dev/zvol/")
	}
	return runVMDiskStepsWithRunner(ctx, diskPlan, plan.DiskSteps, runner, nil)
}

type vmNetworkTransitionResult struct {
	applied bool
	old     vmNetworkPlan
	new     vmNetworkPlan
	runner  vmNetworkCommandRunner
}

func (r vmNetworkTransitionResult) rollback() error {
	if !r.applied {
		return nil
	}
	var retErr error
	if err := r.new.ExecuteCleanup(r.runner); err != nil {
		retErr = errors.Join(retErr, fmt.Errorf("clean up new VM network: %w", err))
	}
	if err := r.old.ExecuteSetup(r.runner); err != nil {
		retErr = errors.Join(retErr, fmt.Errorf("restore old VM network: %w", err))
	}
	return retErr
}

func applyVMServiceNetworkSettings(plan vmSettingsPlan) (vmNetworkTransitionResult, error) {
	result := vmNetworkTransitionResult{old: plan.OldNetwork, new: plan.NewNetwork}
	if !plan.NetworkChanged {
		return result, nil
	}
	runner := vmServiceSetNetworkRunner
	if runner == nil {
		runner = execVMNetworkCommand
	}
	result.runner = runner
	if err := plan.OldNetwork.ExecuteCleanup(runner); err != nil {
		retErr := fmt.Errorf("clean up VM network: %w", err)
		if restoreErr := plan.OldNetwork.ExecuteSetup(runner); restoreErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("restore old VM network: %w", restoreErr))
		}
		return result, retErr
	}
	if err := plan.NewNetwork.ExecuteSetup(runner); err != nil {
		retErr := fmt.Errorf("set up VM network: %w", err)
		if cleanupErr := plan.NewNetwork.ExecuteCleanup(runner); cleanupErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("clean up partial VM network: %w", cleanupErr))
		}
		if restoreErr := plan.OldNetwork.ExecuteSetup(runner); restoreErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("restore old VM network: %w", restoreErr))
		}
		return result, retErr
	}
	result.applied = true
	return result, nil
}

func applyVMServiceMetadataSettings(ctx context.Context, plan vmSettingsPlan) error {
	if !plan.RewriteMetadata {
		return nil
	}
	if err := writeVMMetadata(plan.Root, plan.Metadata); err != nil {
		return fmt.Errorf("write VM metadata: %w", err)
	}
	injector := vmServiceSetMetadataInjector
	if injector == nil {
		injector = injectVMMetadataIntoRootFS
	}
	if err := injector(ctx, plan.OldVM.Disk.Path, plan.Metadata); err != nil {
		return fmt.Errorf("inject VM metadata: %w", err)
	}
	return nil
}

func restoreVMServiceMetadataSettings(ctx context.Context, plan vmSettingsPlan) error {
	if !plan.RewriteMetadata {
		return nil
	}
	var retErr error
	if err := writeVMMetadata(plan.Root, plan.OldMetadata); err != nil {
		retErr = errors.Join(retErr, fmt.Errorf("restore VM metadata: %w", err))
	}
	injector := vmServiceSetMetadataInjector
	if injector == nil {
		injector = injectVMMetadataIntoRootFS
	}
	if err := injector(ctx, plan.OldVM.Disk.Path, plan.OldMetadata); err != nil {
		retErr = errors.Join(retErr, fmt.Errorf("restore VM metadata in rootfs: %w", err))
	}
	return retErr
}

func restoreVMServiceFirecrackerSettings(plan vmSettingsPlan) error {
	if !plan.FirecrackerExisted {
		if err := os.RemoveAll(plan.FirecrackerConfigPath); err != nil {
			return fmt.Errorf("remove new VM firecracker config: %w", err)
		}
		return nil
	}
	if err := os.RemoveAll(plan.FirecrackerConfigPath); err != nil {
		return fmt.Errorf("remove changed VM firecracker config: %w", err)
	}
	if err := writeVMFile(plan.FirecrackerConfigPath, plan.OldFirecrackerConfig, 0o644); err != nil {
		return fmt.Errorf("restore VM firecracker config: %w", err)
	}
	return nil
}

func (s *Server) commitVMServiceSettingsPlan(name string, plan vmSettingsPlan) error {
	_, err := s.cfg.DB.MutateData(func(d *db.Data) error {
		service := d.Services[name]
		if service == nil || service.VM == nil {
			return fmt.Errorf("service %q is not a VM service", name)
		}
		service.VM.CPUs = plan.NewCPUs
		service.VM.MemoryBytes = plan.NewMemoryBytes
		service.VM.Balloon = plan.NewBalloon
		service.VM.Disk.Bytes = plan.NewDiskBytes
		if plan.NetworkChanged {
			service.VM.Networks = plan.NewNetwork.DBNetworks()
			service.SvcNetwork = cloneSvcNetwork(plan.SvcNetwork)
		}
		return nil
	})
	return err
}
