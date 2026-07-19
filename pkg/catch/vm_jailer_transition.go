// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yeetrun/yeet/pkg/db"
)

type vmJailerTransitionInput struct {
	DataRoot    string
	Service     string
	ServiceRoot string
}

type vmJailerTransitionPlan struct {
	Service  string
	Root     string
	Disk     string
	Runtime  VMConsoleProxyConfig
	Identity vmRuntimeIdentity
	Network  vmNetworkPlan
	Jail     vmJailPlan
}

type vmJailerTransitionDeps struct {
	validate        func(context.Context, vmJailerTransitionPlan) error
	cleanupJail     func(vmJailPlan) error
	delegateStorage func(string, string, vmRuntimeIdentity) error
	runNetwork      func([][]string, vmNetworkCommandMode) error
	markReady       func(string) error
}

type vmJailerTransitionTrustedPath struct {
	label string
	path  string
}

var readVMJailerTransitionRuntimeDescriptor = ReadVMRuntimeDescriptor

func newVMJailerTransitionPlan(dv *db.DataView, input vmJailerTransitionInput, identity vmRuntimeIdentity) (vmJailerTransitionPlan, error) {
	service := strings.TrimSpace(input.Service)
	vm, err := vmJailerTransitionVM(dv, service)
	if err != nil {
		return vmJailerTransitionPlan{}, err
	}
	plan, diskConfig, vsockSocket, err := deriveVMJailerTransitionPlan(dv, input, service, vm, identity)
	if err != nil {
		return vmJailerTransitionPlan{}, err
	}
	if err := validateVMJailerTransitionInputs(plan, diskConfig, vsockSocket); err != nil {
		return vmJailerTransitionPlan{}, err
	}
	if err := validateVMJailerTransitionPlanningPaths(plan, input.DataRoot, diskConfig.Backend, vsockSocket); err != nil {
		return vmJailerTransitionPlan{}, err
	}
	if err := validateVMJailerTransitionConfigDisk(plan.Runtime.ConfigFile, plan.Disk); err != nil {
		return vmJailerTransitionPlan{}, err
	}
	jail, err := buildVMJailPlan(plan.Runtime, identity)
	if err != nil {
		return vmJailerTransitionPlan{}, err
	}
	plan.Jail = jail
	if err := validateVMJailerTransitionJailPaths(plan); err != nil {
		return vmJailerTransitionPlan{}, err
	}
	if err := validateVMJailerTransitionResources(plan, vsockSocket); err != nil {
		return vmJailerTransitionPlan{}, err
	}
	return plan, nil
}

func deriveVMJailerTransitionPlan(dv *db.DataView, input vmJailerTransitionInput, service string, vm db.VMConfigView, identity vmRuntimeIdentity) (vmJailerTransitionPlan, db.VMDiskConfig, string, error) {
	if identity.UID <= 0 || identity.GID <= 0 {
		return vmJailerTransitionPlan{}, db.VMDiskConfig{}, "", fmt.Errorf("VM jail runtime identity must be non-root")
	}
	root, disk, diskConfig, firecracker, jailer, err := resolveVMJailerTransitionRuntime(vm, input.ServiceRoot, service)
	if err != nil {
		return vmJailerTransitionPlan{}, db.VMDiskConfig{}, "", err
	}
	runDir := serviceRunDirForRoot(root)
	runtime := VMConsoleProxyConfig{
		Firecracker:   firecracker,
		Jailer:        jailer,
		JailerBase:    vmJailerBaseForDataRoot(input.DataRoot),
		APISocket:     vm.Sockets().APISocketPath,
		ConfigFile:    filepath.Join(runDir, "firecracker.json"),
		ConsoleSocket: vm.Console().SocketPath,
		Service:       service,
		ServiceRoot:   root,
		DiskPath:      disk,
	}
	network, err := vmNetworkPlanForVMService(dv, service)
	if err != nil {
		return vmJailerTransitionPlan{}, db.VMDiskConfig{}, "", err
	}
	plan := vmJailerTransitionPlan{
		Service:  service,
		Root:     root,
		Disk:     disk,
		Runtime:  runtime,
		Identity: identity,
		Network:  network.WithTapOwner(identity),
	}
	return plan, diskConfig, vm.Sockets().VsockSocketPath, nil
}

func resolveVMJailerTransitionRuntime(vm db.VMConfigView, serviceRoot, service string) (root, disk string, diskConfig db.VMDiskConfig, firecracker, jailer string, err error) {
	root = strings.TrimSpace(serviceRoot)
	if root == "" || !filepath.IsAbs(root) {
		return "", "", diskConfig, "", "", fmt.Errorf("VM service root must be an absolute path for jailer transition: %s", root)
	}
	rootFS := strings.TrimSpace(vm.Image().RootFS)
	if rootFS == "" || !filepath.IsAbs(rootFS) {
		return "", "", diskConfig, "", "", fmt.Errorf("VM image rootfs must be an absolute path for jailer transition: %s", rootFS)
	}
	diskConfig = vm.Disk()
	disk = strings.TrimSpace(diskConfig.Path)
	if disk == "" {
		disk = rootFS
	}
	imageDir := filepath.Dir(rootFS)
	firecracker = filepath.Join(imageDir, "firecracker")
	jailer = filepath.Join(imageDir, "jailer")
	components := vm.Components()
	if !components.Valid() {
		return root, disk, diskConfig, firecracker, jailer, nil
	}
	configured := components.Runtime().Configured()
	descriptorPath := filepath.Join(serviceDataDirForRoot(root), vmRuntimeDescriptorFileName)
	descriptorRuntime, readErr := readVMJailerTransitionRuntimeDescriptor(descriptorPath, service)
	if readErr != nil {
		return "", "", diskConfig, "", "", fmt.Errorf("read adopted VM runtime descriptor: %w", readErr)
	}
	if descriptorRuntime != configured {
		return "", "", diskConfig, "", "", fmt.Errorf("adopted VM runtime descriptor does not match configured component state")
	}
	return root, disk, diskConfig, configured.Firecracker, configured.Jailer, nil
}

func vmJailerTransitionVM(dv *db.DataView, service string) (db.VMConfigView, error) {
	if dv == nil {
		return db.VMConfigView{}, fmt.Errorf("service %q not found", service)
	}
	sv, ok := dv.Services().GetOk(service)
	if !ok {
		return db.VMConfigView{}, fmt.Errorf("service %q not found", service)
	}
	if sv.ServiceType() != db.ServiceTypeVM {
		return db.VMConfigView{}, fmt.Errorf("service %q is not a VM service", service)
	}
	vm := sv.VM()
	if !vm.Valid() {
		return db.VMConfigView{}, fmt.Errorf("service %q is not a VM service", service)
	}
	return vm, nil
}

func validateVMJailerTransitionInputs(plan vmJailerTransitionPlan, disk db.VMDiskConfig, vsockSocket string) error {
	if err := validateVMConsoleProxyConfig(plan.Runtime); err != nil {
		return err
	}
	for _, socket := range []struct {
		label    string
		path     string
		required bool
	}{
		{label: "Firecracker API socket", path: plan.Runtime.APISocket, required: true},
		{label: "VM console socket", path: plan.Runtime.ConsoleSocket, required: true},
		{label: "Firecracker vsock socket", path: vsockSocket},
	} {
		path := strings.TrimSpace(socket.path)
		if path == "" && !socket.required {
			continue
		}
		if path == "" || !filepath.IsAbs(path) {
			return fmt.Errorf("%s must be an absolute path for jailer transition: %s", socket.label, path)
		}
	}
	if err := plan.Network.validateExecutable(); err != nil {
		return err
	}
	return validateVMJailerTransitionDisk(disk, plan.Disk)
}

func validateVMJailerTransitionDisk(config db.VMDiskConfig, disk string) error {
	disk = filepath.Clean(strings.TrimSpace(disk))
	if disk == "." || !filepath.IsAbs(disk) {
		return fmt.Errorf("VM disk must be an absolute path for jailer transition: %s", disk)
	}
	switch strings.TrimSpace(config.Backend) {
	case vmDiskBackendRaw:
		return validateVMJailerTransitionRawDisk(disk)
	case vmDiskBackendZVOL:
		return validateVMJailerTransitionZVOL(disk)
	default:
		return fmt.Errorf("unsupported VM disk backend %q for jailer transition", config.Backend)
	}
}

func validateVMJailerTransitionRawDisk(disk string) error {
	info, err := os.Lstat(disk)
	if err != nil {
		return fmt.Errorf("inspect VM raw disk for jailer transition: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("VM raw disk %s must be a regular non-symlink file", disk)
	}
	return nil
}

func validateVMJailerTransitionZVOL(disk string) error {
	if !strings.HasPrefix(disk, "/dev/zvol/") || strings.TrimPrefix(disk, "/dev/zvol/") == "" {
		return fmt.Errorf("VM zvol disk must use an absolute /dev/zvol path: %s", disk)
	}
	return nil
}

func validateVMJailerTransitionConfigDisk(configFile, expectedDisk string) error {
	config, err := readVMJailFirecrackerConfig(configFile)
	if err != nil {
		return err
	}
	rootIndexes, writableIndexes := vmJailerTransitionDriveIndexes(config.Drives)
	if len(rootIndexes) > 1 {
		return fmt.Errorf("firecracker config has %d root drives; jailer transition requires exactly one configured VM disk", len(rootIndexes))
	}

	var selectedIndex int
	if len(rootIndexes) == 1 {
		selectedIndex = rootIndexes[0]
		if err := rejectVMJailerTransitionExtraWritableDrives(config.Drives, writableIndexes, selectedIndex); err != nil {
			return err
		}
	} else {
		if len(writableIndexes) != 1 {
			return fmt.Errorf("firecracker config has %d writable drives; jailer transition requires exactly one configured VM disk", len(writableIndexes))
		}
		selectedIndex = writableIndexes[0]
	}

	selected := config.Drives[selectedIndex]
	configuredDisk := filepath.Clean(strings.TrimSpace(selected.PathOnHost))
	expectedDisk = filepath.Clean(strings.TrimSpace(expectedDisk))
	if configuredDisk == "." || !filepath.IsAbs(configuredDisk) {
		return fmt.Errorf("firecracker configured VM disk must be absolute: %s", configuredDisk)
	}
	if configuredDisk != expectedDisk {
		return fmt.Errorf("firecracker configured VM disk %s does not match database VM disk %s", configuredDisk, expectedDisk)
	}
	return nil
}

func vmJailerTransitionDriveIndexes(drives []firecrackerDrive) ([]int, []int) {
	var roots []int
	var writable []int
	for index, drive := range drives {
		if drive.IsRootDevice {
			roots = append(roots, index)
		}
		if !drive.IsReadOnly {
			writable = append(writable, index)
		}
	}
	return roots, writable
}

func rejectVMJailerTransitionExtraWritableDrives(drives []firecrackerDrive, writable []int, selected int) error {
	for _, index := range writable {
		if index != selected {
			return fmt.Errorf("firecracker config has extra writable drive %q; jailer transition only delegates the configured VM disk", drives[index].DriveID)
		}
	}
	return nil
}

func validateVMJailerTransitionPlanningPaths(plan vmJailerTransitionPlan, dataRoot, diskBackend, vsockSocket string) error {
	dataRoot = filepath.Clean(strings.TrimSpace(dataRoot))
	if !filepath.IsAbs(dataRoot) {
		return fmt.Errorf("configured VM data root must be absolute for jailer transition: %s", dataRoot)
	}
	expectedJailerBase := vmJailerBaseForDataRoot(dataRoot)
	if filepath.Clean(plan.Runtime.JailerBase) != expectedJailerBase {
		return fmt.Errorf("VM jailer base %s must be contained in configured data root %s", plan.Runtime.JailerBase, dataRoot)
	}
	if filepath.Clean(plan.Runtime.ServiceRoot) != filepath.Clean(plan.Root) {
		return fmt.Errorf("VM runtime service root %s does not match configured service root %s", plan.Runtime.ServiceRoot, plan.Root)
	}

	runRoot := serviceRunDirForRoot(plan.Root)
	if filepath.Clean(plan.Runtime.ConfigFile) != filepath.Join(runRoot, "firecracker.json") {
		return fmt.Errorf("firecracker config %s must be the configured service run file", plan.Runtime.ConfigFile)
	}
	if err := validateVMJailerTransitionSocketPaths(runRoot, []vmJailerTransitionTrustedPath{
		{label: "Firecracker API socket", path: plan.Runtime.APISocket},
		{label: "Firecracker vsock socket", path: vsockSocket},
		{label: "VM console socket", path: plan.Runtime.ConsoleSocket},
	}); err != nil {
		return err
	}

	trustedPaths := vmJailerTransitionPlanningTrustedPaths(plan, dataRoot, runRoot, diskBackend, vsockSocket)
	for _, trusted := range trustedPaths {
		if err := validateVMJailerTransitionTrustedExistingPath(trusted.path, trusted.label, plan.Identity); err != nil {
			return err
		}
	}
	return nil
}

func validateVMJailerTransitionSocketPaths(runRoot string, sockets []vmJailerTransitionTrustedPath) error {
	for _, socket := range sockets {
		path := strings.TrimSpace(socket.path)
		if path == "" {
			continue
		}
		if !vmJailerTransitionStrictlyWithin(runRoot, path) {
			return fmt.Errorf("%s %s must be within the configured service run directory %s", socket.label, path, runRoot)
		}
	}
	return nil
}

func vmJailerTransitionPlanningTrustedPaths(plan vmJailerTransitionPlan, dataRoot, runRoot, diskBackend, vsockSocket string) []vmJailerTransitionTrustedPath {
	trusted := []vmJailerTransitionTrustedPath{
		{label: "configured data root", path: dataRoot},
		{label: "configured service root", path: plan.Root},
		{label: "service run directory", path: runRoot},
		{label: "Firecracker config", path: plan.Runtime.ConfigFile},
		{label: "VM jailer base", path: plan.Runtime.JailerBase},
		{label: "Firecracker API socket parent", path: filepath.Dir(plan.Runtime.APISocket)},
		{label: "VM console socket parent", path: filepath.Dir(plan.Runtime.ConsoleSocket)},
	}
	if strings.TrimSpace(vsockSocket) != "" {
		trusted = append(trusted, vmJailerTransitionTrustedPath{label: "Firecracker vsock socket parent", path: filepath.Dir(vsockSocket)})
	}
	if strings.TrimSpace(diskBackend) == vmDiskBackendZVOL {
		trusted = append(trusted, vmJailerTransitionTrustedPath{label: "VM zvol disk parent", path: filepath.Dir(plan.Disk)})
	} else {
		trusted = append(trusted, vmJailerTransitionTrustedPath{label: "VM raw disk", path: plan.Disk})
	}
	return trusted
}

func validateVMJailerTransitionJailPaths(plan vmJailerTransitionPlan) error {
	expectedInstanceRoot := filepath.Join(plan.Runtime.JailerBase, filepath.Base(plan.Runtime.Firecracker), vmJailerID(plan.Service))
	if filepath.Clean(plan.Jail.InstanceRoot) != filepath.Clean(expectedInstanceRoot) {
		return fmt.Errorf("VM jail instance root %s is outside configured jailer base %s", plan.Jail.InstanceRoot, plan.Runtime.JailerBase)
	}
	expectedJailRoot := filepath.Join(expectedInstanceRoot, "root")
	if filepath.Clean(plan.Jail.JailRoot) != filepath.Clean(expectedJailRoot) {
		return fmt.Errorf("VM jail root %s is outside configured instance root %s", plan.Jail.JailRoot, expectedInstanceRoot)
	}
	for _, trusted := range []vmJailerTransitionTrustedPath{
		{label: "VM jail instance root", path: plan.Jail.InstanceRoot},
		{label: "VM jail root", path: plan.Jail.JailRoot},
	} {
		if err := validateVMJailerTransitionTrustedExistingPath(trusted.path, trusted.label, plan.Identity); err != nil {
			return err
		}
	}
	return nil
}

func validateVMJailerTransitionTrustedExistingPath(path, label string, identity vmRuntimeIdentity) error {
	path = filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%s must be absolute for jailer transition: %s", label, path)
	}
	existing, err := vmJailerTransitionDeepestExistingPath(path)
	if err != nil {
		return fmt.Errorf("inspect %s %s: %w", label, path, err)
	}

	ownerUID := uint32(os.Geteuid())
	err = validateTrustedVMJailerPath(existing, ownerUID)
	if err != nil && identity.UID > 0 && uint32(identity.UID) != ownerUID {
		if identityErr := validateTrustedVMJailerPath(existing, uint32(identity.UID)); identityErr == nil {
			return nil
		}
	}
	if err != nil {
		return fmt.Errorf("validate %s %s: %w", label, path, err)
	}
	return nil
}

func vmJailerTransitionDeepestExistingPath(path string) (string, error) {
	for {
		_, err := os.Lstat(path)
		if err == nil {
			return path, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", fmt.Errorf("no existing ancestor")
		}
		path = parent
	}
}

func vmJailerTransitionStrictlyWithin(root, path string) bool {
	return filepath.Clean(root) != filepath.Clean(path) && vmJailerTransitionPathWithin(root, path)
}

func validateVMJailerTransitionResources(plan vmJailerTransitionPlan, vsockSocket string) error {
	if err := validateVMJailerTransitionBinds(plan); err != nil {
		return err
	}
	if err := validateVMJailerTransitionDevices(plan.Jail); err != nil {
		return err
	}
	if err := validateVMJailerTransitionSocketLinks(plan.Jail); err != nil {
		return err
	}
	if !hasVMJailerTransitionSocket(plan.Jail, plan.Runtime.APISocket) {
		return fmt.Errorf("VM jail plan is missing stored API socket %s", plan.Runtime.APISocket)
	}
	vsockSocket = strings.TrimSpace(vsockSocket)
	if vsockSocket != "" && !hasVMJailerTransitionSocket(plan.Jail, vsockSocket) {
		return fmt.Errorf("VM jail plan is missing stored vsock socket %s", vsockSocket)
	}
	return nil
}

func validateVMJailerTransitionBinds(plan vmJailerTransitionPlan) error {
	for _, bind := range plan.Jail.Binds {
		if err := validateVMJailerTransitionBind(plan.Jail.JailRoot, bind, plan.Identity); err != nil {
			return err
		}
	}
	return nil
}

func validateVMJailerTransitionBind(jailRoot string, bind vmJailBind, identity vmRuntimeIdentity) error {
	if !filepath.IsAbs(bind.Source) || !vmJailerTransitionPathWithin(jailRoot, bind.Target) {
		return fmt.Errorf("invalid VM jail bind path %s -> %s", bind.Source, bind.Target)
	}
	info, err := os.Lstat(bind.Source)
	if errors.Is(err, os.ErrNotExist) && bind.CreateDirectory {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect VM jail resource %s: %w", bind.Source, err)
	}
	return validateVMJailerTransitionBindInfo(bind, info, identity)
}

func validateVMJailerTransitionBindInfo(bind vmJailBind, info os.FileInfo, identity vmRuntimeIdentity) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("VM jail resource %s must not be a symbolic link", bind.Source)
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return fmt.Errorf("VM jail resource %s must be a regular file or directory", bind.Source)
	}
	if bind.ReadOnly {
		return validateVMJailReadOnlyResource(bind.Source, info, identity)
	}
	return nil
}

func validateVMJailerTransitionDevices(plan vmJailPlan) error {
	for _, device := range plan.Devices {
		if !filepath.IsAbs(device.Source) || !vmJailerTransitionPathWithin(plan.JailRoot, device.Target) {
			return fmt.Errorf("invalid VM jail device path %s -> %s", device.Source, device.Target)
		}
	}
	return nil
}

func validateVMJailerTransitionSocketLinks(plan vmJailPlan) error {
	for _, link := range plan.SocketLinks {
		if !filepath.IsAbs(link.HostPath) || !vmJailerTransitionPathWithin(plan.JailRoot, link.JailPath) {
			return fmt.Errorf("invalid VM jail socket path %s -> %s", link.HostPath, link.JailPath)
		}
	}
	return nil
}

func vmJailerTransitionPathWithin(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if !filepath.IsAbs(root) || !filepath.IsAbs(path) {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func hasVMJailerTransitionSocket(plan vmJailPlan, hostPath string) bool {
	hostPath = filepath.Clean(strings.TrimSpace(hostPath))
	for _, link := range plan.SocketLinks {
		if filepath.Clean(link.HostPath) == hostPath {
			return true
		}
	}
	return false
}

func executeVMJailerTransition(ctx context.Context, plan vmJailerTransitionPlan, deps vmJailerTransitionDeps) error {
	// EnsureVMNetwork is the systemd pre-start transition: the VM is stopped here.
	// Keep this final validation immediately before every filesystem and network
	// mutation so checked paths cannot be reused across a running VM transition.
	if err := deps.validate(ctx, plan); err != nil {
		return err
	}
	if err := deps.cleanupJail(plan.Jail); err != nil {
		return fmt.Errorf("clean stale VM jail: %w", err)
	}
	if err := deps.delegateStorage(plan.Root, plan.Disk, plan.Identity); err != nil {
		return err
	}
	if err := deps.runNetwork(plan.Network.CleanupCommands(), vmNetworkCommandModeCleanup); err != nil {
		return fmt.Errorf("remove pre-jailer VM network: %w", err)
	}
	if err := deps.runNetwork(plan.Network.SetupCommands(), vmNetworkCommandModeSetup); err != nil {
		return fmt.Errorf("create jailed VM network: %w", err)
	}
	return deps.markReady(plan.Root)
}

func defaultVMJailerTransitionDeps() vmJailerTransitionDeps {
	return vmJailerTransitionDeps{
		validate:        validateVMJailerTransition,
		cleanupJail:     cleanupVMJail,
		delegateStorage: delegateVMJailStorage,
		runNetwork:      runVMJailerTransitionNetwork,
		markReady:       markVMJailerReady,
	}
}

func validateVMJailerTransition(ctx context.Context, plan vmJailerTransitionPlan) error {
	expectedVsock := vmJailerTransitionVsockSocket(plan.Jail, plan.Runtime.APISocket)
	diskBackend := vmDiskBackendRaw
	if strings.HasPrefix(filepath.Clean(plan.Disk), "/dev/zvol/") {
		diskBackend = vmDiskBackendZVOL
	}
	dataRoot := filepath.Dir(filepath.Clean(plan.Runtime.JailerBase))
	if err := validateVMJailerTransitionPlanningPaths(plan, dataRoot, diskBackend, expectedVsock); err != nil {
		return err
	}
	if err := validateVMJailerTransitionConfigDisk(plan.Runtime.ConfigFile, plan.Disk); err != nil {
		return err
	}
	if err := validateVMJailerRuntimePair(ctx, plan.Runtime.Firecracker, plan.Runtime.Jailer); err != nil {
		return err
	}
	rebuilt, err := buildVMJailPlan(plan.Runtime, plan.Identity)
	if err != nil {
		return err
	}
	plan.Jail = rebuilt
	if err := validateVMJailerTransitionJailPaths(plan); err != nil {
		return err
	}
	return validateVMJailerTransitionResources(plan, expectedVsock)
}

func vmJailerTransitionVsockSocket(plan vmJailPlan, apiSocket string) string {
	apiSocket = filepath.Clean(strings.TrimSpace(apiSocket))
	for _, link := range plan.SocketLinks {
		if filepath.Clean(link.HostPath) != apiSocket {
			return link.HostPath
		}
	}
	return ""
}

func runVMJailerTransitionNetwork(commands [][]string, mode vmNetworkCommandMode) error {
	runner := vmNetworkReconcileRunner
	if runner == nil {
		runner = execVMNetworkCommand
	}
	return runVMNetworkCommands(runner, commands, mode)
}
