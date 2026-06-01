// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
)

var (
	vmProvisionHostProfileFunc  func(*ttyExecer, resolvedServiceRoot, int64) (vmHostProfile, error)
	vmProvisionImageEnsureFunc  func(context.Context, vmImageCache) (vmImageAsset, error)
	vmProvisionDiskRunner       vmCommandRunner
	vmProvisionNetworkRunner    vmNetworkCommandRunner
	vmProvisionMetadataInjector func(context.Context, string, vmMetadataConfig) error
	vmProvisionSSHKeyFunc       func() (string, error)
	vmProvisionSystemdDir       string
	vmProvisionSystemctlFunc    func(args ...string) error
)

type vmProvisionPlan struct {
	Service     string
	ServiceRoot resolvedServiceRoot
	Shape       vmShape
	Image       vmImageAsset
	Disk        vmDiskPlan
	DiskPath    string
	Network     vmNetworkPlan
	SvcNetwork  *db.SvcNetwork
	Metadata    vmMetadataConfig

	FirecrackerConfigPath  string
	FirecrackerConfig      []byte
	SystemdUnitStagePath   string
	SystemdUnitInstallPath string
	SystemdUnitContent     string
	SerialSocket           string
	SerialLog              string
	APISocket              string
	PIDFile                string
}

func (e *ttyExecer) provisionVM(flags cli.RunFlags, payload string) (retErr error) {
	if payload != vmUbuntu2604Payload {
		return fmt.Errorf("unsupported VM payload %q; supported payload: %s", payload, vmUbuntu2604Payload)
	}
	serviceExisted, err := e.serviceExists()
	if err != nil {
		return err
	}
	rollbackNewService := false
	defer func() {
		if retErr == nil || !rollbackNewService {
			return
		}
		if err := e.rollbackNewVMProvisionReservation(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("rollback VM service reservation: %w", err))
		}
	}()
	inputs, err := e.vmProvisionInputs(flags)
	if err != nil {
		return err
	}
	svcNet, err := e.reserveVMServiceNetwork(flags)
	if err != nil {
		return err
	}
	rollbackNewService = !serviceExisted
	plan, err := e.newVMProvisionPlan(flags, inputs.ServiceRoot, inputs.Shape, inputs.Image, svcNet, inputs.SSHKey)
	if err != nil {
		return err
	}
	e.printVMProvisionSummary(plan, payload)
	if err := e.finishVMProvision(inputs.Context, plan, payload, flags.Restart); err != nil {
		return err
	}
	rollbackNewService = false
	return nil
}

type vmProvisionInputs struct {
	Context     context.Context
	ServiceRoot resolvedServiceRoot
	Shape       vmShape
	Image       vmImageAsset
	SSHKey      string
}

func (e *ttyExecer) vmProvisionInputs(flags cli.RunFlags) (vmProvisionInputs, error) {
	ctx := e.vmProvisionContext()
	resolvedRoot, err := e.prepareVMServiceRoot(flags)
	if err != nil {
		return vmProvisionInputs{}, err
	}
	shape, err := e.vmProvisionShape(resolvedRoot, flags)
	if err != nil {
		return vmProvisionInputs{}, err
	}
	sshKey, err := e.vmSSHKey()
	if err != nil {
		return vmProvisionInputs{}, err
	}
	image, err := e.ensureVMProvisionImage(ctx)
	if err != nil {
		return vmProvisionInputs{}, err
	}
	return vmProvisionInputs{Context: ctx, ServiceRoot: resolvedRoot, Shape: shape, Image: image, SSHKey: sshKey}, nil
}

func (e *ttyExecer) vmProvisionContext() context.Context {
	if e.ctx != nil {
		return e.ctx
	}
	return context.Background()
}

func (e *ttyExecer) prepareVMServiceRoot(flags cli.RunFlags) (resolvedServiceRoot, error) {
	resolvedRoot, err := e.s.prepareServiceRootForInstall(e.sn, flags.ServiceRoot, flags.ZFS)
	if err != nil {
		return resolvedServiceRoot{}, err
	}
	if err := ensureDirsForRoot(resolvedRoot.Root, e.user); err != nil {
		return resolvedServiceRoot{}, fmt.Errorf("prepare VM service root: %w", err)
	}
	return resolvedRoot, nil
}

func (e *ttyExecer) vmProvisionShape(resolvedRoot resolvedServiceRoot, flags cli.RunFlags) (vmShape, error) {
	runningVMBytes, err := e.runningVMBytes()
	if err != nil {
		return vmShape{}, err
	}
	profile, err := e.vmHostProfile(resolvedRoot, runningVMBytes)
	if err != nil {
		return vmShape{}, err
	}
	return vmShapeFromRunFlags(profile, flags)
}

func (e *ttyExecer) ensureVMProvisionImage(ctx context.Context) (vmImageAsset, error) {
	ensureImage := vmProvisionImageEnsureFunc
	if ensureImage == nil {
		ensureImage = ensureVMImageAsset
	}
	return ensureImage(ctx, vmImageCache{
		Root:        filepath.Join(e.s.cfg.RootDir, "vm-images"),
		ManifestURL: defaultVMImageManifestURL,
	})
}

func (e *ttyExecer) finishVMProvision(ctx context.Context, plan vmProvisionPlan, payload string, restart bool) error {
	if err := e.applyVMProvisionArtifacts(ctx, plan); err != nil {
		return err
	}
	if err := e.installVMSystemdUnit(plan); err != nil {
		return err
	}
	if err := e.commitVMProvision(plan, payload); err != nil {
		return err
	}
	if restart {
		e.vmProgressf("Starting VM...\n")
		if err := e.restartVMSystemdUnit(plan); err != nil {
			return err
		}
	}
	e.printVMNextCommands(plan, restart)
	return nil
}

func (e *ttyExecer) serviceExists() (bool, error) {
	dv, err := e.s.getDB()
	if err != nil {
		return false, err
	}
	_, ok := dv.Services().GetOk(e.sn)
	return ok, nil
}

func (e *ttyExecer) rollbackNewVMProvisionReservation() error {
	_, err := e.s.cfg.DB.MutateData(func(d *db.Data) error {
		s := d.Services[e.sn]
		if s == nil {
			return nil
		}
		if s.VM != nil && s.VM.SetupState == "ready" {
			return nil
		}
		delete(d.Services, e.sn)
		return nil
	})
	return err
}

func (e *ttyExecer) vmSSHKey() (string, error) {
	if key, ok := normalizeVMAuthorizedKeyLine(e.vmSSHAuthorizedKey); ok {
		return key, nil
	}
	if strings.TrimSpace(e.vmSSHAuthorizedKey) != "" {
		return "", fmt.Errorf("invalid VM SSH public key from client")
	}
	keyFunc := vmProvisionSSHKeyFunc
	if keyFunc == nil {
		keyFunc = defaultVMSSHKey
	}
	key, err := keyFunc()
	if err != nil {
		return "", fmt.Errorf("select VM SSH key: %w", err)
	}
	return key, nil
}

func (e *ttyExecer) vmHostProfile(resolvedRoot resolvedServiceRoot, runningVMBytes int64) (vmHostProfile, error) {
	if vmProvisionHostProfileFunc != nil {
		return vmProvisionHostProfileFunc(e, resolvedRoot, runningVMBytes)
	}
	return localVMHostProfile(availableStorageBytes(resolvedRoot.Root), resolvedRoot.ZFS, runningVMBytes), nil
}

func vmShapeFromRunFlags(profile vmHostProfile, flags cli.RunFlags) (vmShape, error) {
	shape, err := defaultVMShape(profile)
	if err != nil {
		return vmShape{}, err
	}
	if err := applyVMShapeOverrides(&shape, flags); err != nil {
		return vmShape{}, err
	}
	if err := validateVMShape(shape); err != nil {
		return vmShape{}, err
	}
	if err := admitVMMemory(profile, shape.MemoryBytes); err != nil {
		return vmShape{}, err
	}
	return shape, nil
}

func applyVMShapeOverrides(shape *vmShape, flags cli.RunFlags) error {
	if flags.CPUs < 0 {
		return fmt.Errorf("VM CPU count must be positive")
	}
	if flags.CPUs > 0 {
		shape.CPUs = flags.CPUs
	}
	if err := applyVMSizeOverride(&shape.MemoryBytes, flags.Memory); err != nil {
		return err
	}
	return applyVMSizeOverride(&shape.DiskBytes, flags.Disk)
}

func applyVMSizeOverride(dst *int64, raw string) error {
	bytes, err := parseVMSize(raw)
	if err != nil {
		return err
	}
	if bytes > 0 {
		*dst = bytes
	}
	return nil
}

func validateVMShape(shape vmShape) error {
	switch {
	case shape.CPUs <= 0:
		return fmt.Errorf("VM CPU count must be positive")
	case shape.MemoryBytes <= 0:
		return fmt.Errorf("VM memory must be positive")
	case shape.DiskBytes <= 0:
		return fmt.Errorf("VM disk size must be positive")
	default:
		return nil
	}
}

func (e *ttyExecer) newVMProvisionPlan(flags cli.RunFlags, resolvedRoot resolvedServiceRoot, shape vmShape, image vmImageAsset, svcNet *db.SvcNetwork, sshKey string) (vmProvisionPlan, error) {
	networkPlan, err := e.vmNetworkPlanFromFlags(flags, svcNet)
	if err != nil {
		return vmProvisionPlan{}, err
	}

	runDir := serviceRunDirForRoot(resolvedRoot.Root)
	binDir := serviceBinDirForRoot(resolvedRoot.Root)
	diskPlan := vmDiskPlan{
		Service:    e.sn,
		Backend:    shape.DiskBackend,
		Path:       filepath.Join(serviceDataDirForRoot(resolvedRoot.Root), "rootfs.raw"),
		Bytes:      shape.DiskBytes,
		BaseRootFS: image.DiskRootFSPath(),
		BaseBytes:  image.Manifest.RootFSSize,
	}
	if shape.DiskBackend == vmDiskBackendZVOL {
		baseDataset := vmZVOLBaseDataset(resolvedRoot, image.Manifest.Version)
		diskPlan.Path = vmZVOLRootDataset(resolvedRoot, e.sn)
		diskPlan.BaseDataset = baseDataset
		diskPlan.ImageVersion = image.Manifest.Version
	}
	diskPath := vmDiskPathForRuntime(diskPlan)

	firecrackerPath := filepath.Join(runDir, "firecracker.json")
	apiSocket := filepath.Join(runDir, "firecracker.sock")
	unitName := vmSystemdUnitName(e.sn)
	systemdDir := vmProvisionSystemdDir
	if systemdDir == "" {
		systemdDir = vmSystemdSystemDir
	}

	firecrackerConfig, err := renderFirecrackerConfig(firecrackerConfig{
		BootSource: firecrackerBootSource{
			KernelImagePath: image.Paths.KernelPath,
			InitrdPath:      image.Paths.InitrdPath,
			BootArgs:        "console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda rw",
		},
		Drives: []firecrackerDrive{{
			DriveID:      "rootfs",
			PathOnHost:   diskPath,
			IsRootDevice: true,
			IsReadOnly:   false,
		}},
		NetworkInterfaces: networkPlan.FirecrackerInterfaces(),
		MachineConfig: firecrackerMachineConfig{
			VCPUCount:  shape.CPUs,
			MemSizeMib: int(shape.MemoryBytes >> 20),
		},
	})
	if err != nil {
		return vmProvisionPlan{}, err
	}
	unit := renderVMSystemdUnit(vmSystemdConfig{
		Service:          e.sn,
		Runner:           e.s.catchRunnerPath(),
		Firecracker:      image.Paths.FirecrackerPath,
		ConfigPath:       firecrackerPath,
		APISocket:        apiSocket,
		ConsoleSocket:    filepath.Join(runDir, "serial.sock"),
		WorkingDirectory: resolvedRoot.Root,
	})

	return vmProvisionPlan{
		Service:                e.sn,
		ServiceRoot:            resolvedRoot,
		Shape:                  shape,
		Image:                  image,
		Disk:                   diskPlan,
		DiskPath:               diskPath,
		Network:                networkPlan,
		SvcNetwork:             svcNet,
		Metadata:               vmMetadataConfig{Hostname: e.sn, User: "ubuntu", SSHKey: sshKey, Networks: networkPlan.MetadataNetworks(), HostKeyDir: filepath.Join(resolvedRoot.Root, "metadata", "ssh-host-keys")},
		FirecrackerConfigPath:  firecrackerPath,
		FirecrackerConfig:      firecrackerConfig,
		SystemdUnitStagePath:   filepath.Join(binDir, unitName),
		SystemdUnitInstallPath: filepath.Join(systemdDir, unitName),
		SystemdUnitContent:     unit,
		SerialSocket:           filepath.Join(runDir, "serial.sock"),
		SerialLog:              filepath.Join(runDir, "serial.log"),
		APISocket:              apiSocket,
		PIDFile:                filepath.Join(runDir, "firecracker.pid"),
	}, nil
}

func (e *ttyExecer) applyVMProvisionArtifacts(ctx context.Context, plan vmProvisionPlan) error {
	e.vmProgressf("Preparing disk...\n")
	if err := runVMProvisionDiskPlan(ctx, plan.Disk, vmProvisionDiskRunner); err != nil {
		return err
	}
	if err := writeVMMetadata(plan.ServiceRoot.Root, plan.Metadata); err != nil {
		return fmt.Errorf("write VM metadata: %w", err)
	}
	injectMetadata := vmProvisionMetadataInjector
	if injectMetadata == nil {
		injectMetadata = injectVMMetadataIntoRootFS
	}
	e.vmProgressf("Injecting guest metadata...\n")
	if err := injectMetadata(ctx, plan.DiskPath, plan.Metadata); err != nil {
		return fmt.Errorf("inject VM metadata: %w", err)
	}
	e.vmProgressf("Writing Firecracker config...\n")
	if err := writeVMFile(plan.FirecrackerConfigPath, plan.FirecrackerConfig, 0o644); err != nil {
		return fmt.Errorf("write Firecracker config: %w", err)
	}
	e.vmProgressf("Configuring network...\n")
	if err := plan.Network.ExecuteSetup(vmProvisionNetworkRunner); err != nil {
		return fmt.Errorf("set up VM network: %w", err)
	}
	if err := writeVMFile(plan.SystemdUnitStagePath, []byte(plan.SystemdUnitContent), 0o644); err != nil {
		return fmt.Errorf("stage VM systemd unit: %w", err)
	}
	return nil
}

func (e *ttyExecer) commitVMProvision(plan vmProvisionPlan, payload string) error {
	_, _, err := e.s.cfg.DB.MutateService(e.sn, func(_ *db.Data, s *db.Service) error {
		applyVMServiceRoot(s, e.s.defaultServiceRootDir(e.sn), plan.ServiceRoot)
		s.ServiceType = db.ServiceTypeVM
		if plan.SvcNetwork != nil {
			s.SvcNetwork = plan.SvcNetwork
		}
		s.VM = &db.VMConfig{
			Runtime: vmRuntimeFirecracker,
			Image: db.VMImageConfig{
				Payload: payload,
				Version: plan.Image.Manifest.Version,
				Kernel:  plan.Image.Paths.KernelPath,
				RootFS:  plan.Image.DiskRootFSPath(),
			},
			CPUs:        plan.Shape.CPUs,
			MemoryBytes: plan.Shape.MemoryBytes,
			Disk: db.VMDiskConfig{
				Backend: plan.Shape.DiskBackend,
				Bytes:   plan.Shape.DiskBytes,
				Path:    plan.DiskPath,
			},
			Networks:   plan.Network.DBNetworks(),
			SSH:        db.VMSSHConfig{User: "ubuntu"},
			Console:    db.VMConsoleConfig{SocketPath: plan.SerialSocket, LogPath: plan.SerialLog},
			Sockets:    db.VMSocketConfig{APISocketPath: plan.APISocket},
			PIDFile:    plan.PIDFile,
			SetupState: "ready",
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("commit VM service: %w", err)
	}
	return nil
}

func (e *ttyExecer) installVMSystemdUnit(plan vmProvisionPlan) error {
	e.vmProgressf("Installing VM service...\n")
	if err := writeVMFile(plan.SystemdUnitInstallPath, []byte(plan.SystemdUnitContent), 0o644); err != nil {
		return fmt.Errorf("install VM systemd unit: %w", err)
	}
	systemctl := vmProvisionSystemctlFunc
	if systemctl == nil {
		systemctl = runVMSystemctl
	}
	unit := filepath.Base(plan.SystemdUnitInstallPath)
	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	if err := systemctl("enable", unit); err != nil {
		return err
	}
	return nil
}

func (e *ttyExecer) restartVMSystemdUnit(plan vmProvisionPlan) error {
	systemctl := vmProvisionSystemctlFunc
	if systemctl == nil {
		systemctl = runVMSystemctl
	}
	return systemctl("restart", filepath.Base(plan.SystemdUnitInstallPath))
}

func writeVMFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func runVMSystemctl(args ...string) error {
	cmd := exec.Command("systemctl", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl %v failed: %w\n%s", args, err, string(out))
	}
	return nil
}

func (e *ttyExecer) printVMProvisionSummary(plan vmProvisionPlan, payload string) {
	e.vmProgressf("VM %s\n", plan.Service)
	e.vmProgressf("Image: %s (%s)\n", payload, plan.Image.Manifest.Version)
	e.vmProgressf("Shape: %d vCPU, %s memory, %s disk\n", plan.Shape.CPUs, formatVMProvisionBytes(plan.Shape.MemoryBytes), formatVMProvisionBytes(plan.Shape.DiskBytes))
	e.vmProgressf("Network: %s\n", formatVMProvisionNetwork(plan.Network))
}

func (e *ttyExecer) printVMNextCommands(plan vmProvisionPlan, restarted bool) {
	if restarted {
		e.vmProgressf("VM %s is running.\n", plan.Service)
	} else {
		e.vmProgressf("VM %s is ready. Start: yeet start %s\n", plan.Service, plan.Service)
	}
	e.vmProgressf("SSH: yeet ssh %s\n", plan.Service)
	e.vmProgressf("Console: yeet vm console %s\n", plan.Service)
}

func (e *ttyExecer) vmProgressf(format string, args ...any) {
	if e == nil || e.rw == nil {
		return
	}
	e.printf(format, args...)
}

func formatVMProvisionNetwork(plan vmNetworkPlan) string {
	modes := make([]string, 0, len(plan.Interfaces))
	for _, iface := range plan.Interfaces {
		if iface.Mode != "" {
			modes = append(modes, iface.Mode)
		}
	}
	if len(modes) == 0 {
		return "none"
	}
	return strings.Join(modes, ",")
}

func formatVMProvisionBytes(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	value := float64(bytes)
	unit := "B"
	for _, next := range []string{"KB", "MB", "GB", "TB"} {
		if value < 1024 {
			break
		}
		value /= 1024
		unit = next
	}
	return fmt.Sprintf("%.1f %s", value, unit)
}

func applyVMServiceRoot(s *db.Service, defaultRoot string, resolved resolvedServiceRoot) {
	s.ServiceRootZFS = resolved.Dataset
	if filepath.Clean(resolved.Root) == filepath.Clean(defaultRoot) && resolved.Dataset == "" {
		s.ServiceRoot = ""
		return
	}
	s.ServiceRoot = resolved.Root
}

func vmDiskPathForRuntime(plan vmDiskPlan) string {
	if plan.Backend == vmDiskBackendZVOL {
		return "/dev/zvol/" + strings.TrimPrefix(plan.Path, "/")
	}
	return plan.Path
}

func (e *ttyExecer) runningVMBytes() (int64, error) {
	dv, err := e.s.getDB()
	if err != nil {
		return 0, err
	}
	var total int64
	for _, service := range dv.AsStruct().Services {
		if service == nil || service.VM == nil || service.VM.SetupState != "ready" {
			continue
		}
		total += service.VM.MemoryBytes
	}
	return total, nil
}

func (e *ttyExecer) reserveVMServiceNetwork(flags cli.RunFlags) (*db.SvcNetwork, error) {
	if !vmNetworkModeRequested(flags.Net, "svc") {
		return nil, nil
	}
	_, service, err := e.s.cfg.DB.MutateService(e.sn, func(d *db.Data, s *db.Service) error {
		if s.SvcNetwork != nil && s.SvcNetwork.IPv4.IsValid() {
			return nil
		}
		svcNet, err := svcNetworkFromData(d.View())
		if err != nil {
			return err
		}
		s.SvcNetwork = svcNet
		return nil
	})
	if err != nil {
		return nil, err
	}
	if service == nil || service.SvcNetwork == nil || !service.SvcNetwork.IPv4.IsValid() {
		return nil, fmt.Errorf("failed to reserve VM service IP")
	}
	return &db.SvcNetwork{IPv4: service.SvcNetwork.IPv4}, nil
}

func (e *ttyExecer) vmNetworkPlanFromFlags(flags cli.RunFlags, svcNet *db.SvcNetwork) (vmNetworkPlan, error) {
	input := vmNetworkInputs{
		LANParent: strings.TrimSpace(flags.MacvlanParent),
		LANVLAN:   flags.MacvlanVlan,
		LANMAC:    strings.TrimSpace(flags.MacvlanMac),
	}
	if svcNet != nil && svcNet.IPv4.IsValid() {
		input.ServiceIP = svcNet.IPv4.String()
	}
	modes := vmRequestedNetworkModes(flags.Net)
	if err := validateVMNetworkModes(modes); err != nil {
		return vmNetworkPlan{}, err
	}
	if vmModeListContains(modes, "lan") {
		if input.LANParent == "" {
			parent, err := hostDefaultRouteInterfaceFn()
			if err != nil {
				return vmNetworkPlan{}, fmt.Errorf("resolve VM LAN parent: %w", err)
			}
			input.LANParent = parent
		}
		input.LANParentIsBridge = vmLANParentIsBridge(input.LANParent)
		if input.LANMAC == "" {
			input.LANMAC = randomMAC()
		}
	}
	return newVMNetworkPlan(e.sn, modes, input), nil
}

func validateVMNetworkModes(modes []string) error {
	for _, mode := range vmNetworkModes(modes) {
		switch mode {
		case "svc", "lan":
		default:
			return fmt.Errorf("unsupported VM network mode %q; supported modes: svc, lan", mode)
		}
	}
	return nil
}

func vmRequestedNetworkModes(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{"svc"}
	}
	return []string{raw}
}

func vmNetworkModeRequested(raw, mode string) bool {
	return vmModeListContains(vmRequestedNetworkModes(raw), mode)
}

func vmModeListContains(modes []string, want string) bool {
	for _, mode := range vmNetworkModes(modes) {
		if mode == want {
			return true
		}
	}
	return false
}

func vmLANParentIsBridge(parent string) bool {
	if parent == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join("/sys/class/net", parent, "bridge")); err == nil {
		return true
	}
	return strings.HasPrefix(parent, "br") || strings.HasPrefix(parent, "vmbr")
}

func vmZVOLBaseDataset(root resolvedServiceRoot, version string) string {
	dataset := strings.Trim(root.Dataset, "/")
	if dataset == "" {
		dataset = "yeet/vm-images"
	}
	return dataset + "/base/" + version
}

func vmZVOLRootDataset(root resolvedServiceRoot, service string) string {
	dataset := strings.Trim(root.Dataset, "/")
	if dataset == "" {
		dataset = "yeet/vms"
	}
	return dataset + "/vm/" + shortVMName(service) + "/root"
}
