// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
)

var (
	vmServiceSetHostProfileFunc   func(*Server, string, int64) (vmHostProfile, error)
	vmServiceSetDiskRunner        vmCommandRunner
	vmServiceSetNetworkRunner     vmNetworkCommandRunner
	vmServiceSetMetadataInjector  func(context.Context, string, vmMetadataConfig) error
	isServiceRunningForVMSettings = (*Server).IsServiceRunning
)

type vmSettingsPlan struct {
	Service               string
	Root                  string
	OldVM                 db.VMConfig
	NewCPUs               int
	NewMemoryBytes        int64
	NewDiskBytes          int64
	DiskSteps             []vmDiskPlanStep
	OldNetwork            vmNetworkPlan
	NewNetwork            vmNetworkPlan
	NetworkChanged        bool
	SvcNetwork            *db.SvcNetwork
	Metadata              vmMetadataConfig
	RewriteMetadata       bool
	FirecrackerConfigPath string
	FirecrackerConfig     []byte
}

func (s *Server) updateVMServiceSettings(ctx context.Context, name string, flags cli.VMSetFlags) error {
	plan, err := s.planVMServiceSettings(name, flags)
	if err != nil {
		return err
	}
	if err := s.applyVMServiceSettingsPlan(ctx, plan); err != nil {
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
	return dv, service, vmSettingsPlan{
		Service:               name,
		Root:                  root,
		OldVM:                 oldVM,
		NewCPUs:               oldVM.CPUs,
		NewMemoryBytes:        oldVM.MemoryBytes,
		NewDiskBytes:          oldVM.Disk.Bytes,
		OldNetwork:            vmNetworkPlanFromDB(name, oldVM.Networks),
		NewNetwork:            vmNetworkPlanFromDB(name, oldVM.Networks),
		SvcNetwork:            cloneSvcNetwork(service.SvcNetwork),
		FirecrackerConfigPath: filepath.Join(serviceRunDirForRoot(root), "firecracker.json"),
	}, nil
}

func (s *Server) applyVMShapeSettings(service *db.Service, flags cli.VMSetFlags, plan *vmSettingsPlan) error {
	if flags.CPUs > 0 {
		plan.NewCPUs = flags.CPUs
	}
	if strings.TrimSpace(flags.Memory) != "" {
		memoryBytes, err := parseVMSize(flags.Memory)
		if err != nil {
			return err
		}
		plan.NewMemoryBytes = memoryBytes
	}
	shapeChanged := flags.CPUs > 0 || strings.TrimSpace(flags.Memory) != ""
	return s.validateVMSettingsShape(plan.Root, service, plan.NewCPUs, plan.NewMemoryBytes, shapeChanged)
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
		plan.NewNetwork = network
		plan.SvcNetwork = svcNet
		plan.NetworkChanged = true
		plan.RewriteMetadata = true
	}
	return nil
}

func (p *vmSettingsPlan) finalizeFirecrackerSettings() error {
	fc, fastBoot, err := p.readFirecrackerConfig()
	if err != nil {
		return err
	}
	if p.RewriteMetadata {
		metadata, err := vmMetadataForSettings(p.Root, p.Service, p.OldVM, p.NewNetwork, fastBoot)
		if err != nil {
			return err
		}
		p.Metadata = metadata
	}
	raw, err := p.renderFirecrackerConfig(fc, fastBoot)
	if err != nil {
		return err
	}
	p.FirecrackerConfig = raw
	return nil
}

func (s *Server) validateVMSettingsShape(root string, service *db.Service, cpus int, memoryBytes int64, admitMemory bool) error {
	if err := validateVMShape(vmShape{CPUs: cpus, MemoryBytes: memoryBytes, DiskBytes: service.VM.Disk.Bytes, DiskBackend: service.VM.Disk.Backend}); err != nil {
		return err
	}
	if !admitMemory {
		return nil
	}
	runningBytes, err := s.runningVMBytesExcluding(service.Name)
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
	return admitVMMemory(profile, memoryBytes)
}

func (s *Server) runningVMBytesExcluding(name string) (int64, error) {
	dv, err := s.getDB()
	if err != nil {
		return 0, err
	}
	var total int64
	for serviceName, service := range dv.AsStruct().Services {
		if serviceName == name || service == nil || service.VM == nil || service.VM.SetupState != "ready" {
			continue
		}
		total += service.VM.MemoryBytes
	}
	return total, nil
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
			iface.Gateway = vmSvcGateway
		case "lan":
			if network.VLAN != 0 {
				if vmLANParentIsBridge(network.Parent) {
					iface.Bridge = network.Parent
				} else {
					iface.Bridge = fmt.Sprintf("yvm-%s-b%d", short, i)
					iface.VLANDevice = fmt.Sprintf("yvm-%s-v%d", short, i)
				}
			} else {
				iface.Bridge = network.Parent
			}
			iface.DHCP = true
		}
		plan.Interfaces = append(plan.Interfaces, iface)
	}
	return plan
}

func cloneSvcNetwork(in *db.SvcNetwork) *db.SvcNetwork {
	if in == nil {
		return nil
	}
	return &db.SvcNetwork{IPv4: in.IPv4}
}

func (p vmSettingsPlan) readFirecrackerConfig() (firecrackerConfig, bool, error) {
	raw, err := os.ReadFile(p.FirecrackerConfigPath)
	if err != nil && !os.IsNotExist(err) {
		return firecrackerConfig{}, false, err
	}
	if len(raw) == 0 {
		cfg := firecrackerConfig{
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
		return cfg, false, nil
	}
	var cfg firecrackerConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return firecrackerConfig{}, false, err
	}
	return cfg, strings.Contains(cfg.BootSource.BootArgs, "init="+vmGuestInitPath), nil
}

func (p vmSettingsPlan) renderFirecrackerConfig(cfg firecrackerConfig, fastBoot bool) ([]byte, error) {
	cfg.MachineConfig = firecrackerMachineConfig{VCPUCount: p.NewCPUs, MemSizeMib: int(p.NewMemoryBytes >> 20)}
	cfg.NetworkInterfaces = p.NewNetwork.FirecrackerInterfaces()
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

func (s *Server) applyVMServiceSettingsPlan(ctx context.Context, plan vmSettingsPlan) error {
	if err := applyVMServiceDiskSettings(ctx, plan); err != nil {
		return err
	}
	if err := applyVMServiceNetworkSettings(plan); err != nil {
		return err
	}
	if err := applyVMServiceMetadataSettings(ctx, plan); err != nil {
		return err
	}
	return writeVMFile(plan.FirecrackerConfigPath, plan.FirecrackerConfig, 0o644)
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

func applyVMServiceNetworkSettings(plan vmSettingsPlan) error {
	if !plan.NetworkChanged {
		return nil
	}
	runner := vmServiceSetNetworkRunner
	if runner == nil {
		runner = execVMNetworkCommand
	}
	if err := plan.OldNetwork.ExecuteCleanup(runner); err != nil {
		return fmt.Errorf("clean up VM network: %w", err)
	}
	if err := plan.NewNetwork.ExecuteSetup(runner); err != nil {
		return fmt.Errorf("set up VM network: %w", err)
	}
	return nil
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

func (s *Server) commitVMServiceSettingsPlan(name string, plan vmSettingsPlan) error {
	_, err := s.cfg.DB.MutateData(func(d *db.Data) error {
		service := d.Services[name]
		if service == nil || service.VM == nil {
			return fmt.Errorf("service %q is not a VM service", name)
		}
		service.VM.CPUs = plan.NewCPUs
		service.VM.MemoryBytes = plan.NewMemoryBytes
		service.VM.Disk.Bytes = plan.NewDiskBytes
		if plan.NetworkChanged {
			service.VM.Networks = plan.NewNetwork.DBNetworks()
			service.SvcNetwork = cloneSvcNetwork(plan.SvcNetwork)
		}
		return nil
	})
	return err
}
