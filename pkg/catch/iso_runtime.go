// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/iso"
	"github.com/yeetrun/yeet/pkg/netns"
	"github.com/yeetrun/yeet/pkg/svc"
	"golang.org/x/sys/unix"
)

const (
	isoDNSPort                   = 5353
	isoOperationLockFileName     = "iso-network.lock"
	isoOperationLockPollInterval = 50 * time.Millisecond
)

var isoSecurityCleanupTimeout = 30 * time.Second

var (
	detectISOFirewallBackendForRuntime = netns.DetectFirewallBackend
	ensureISOPolicyForRuntime          = netns.EnsureISOPolicy
	verifyISOPolicyForRuntime          = netns.VerifyISOPolicy
	ensureISOTopologyForRuntime        = netns.EnsureISOTopology
	verifyISOTopologyForRuntime        = netns.VerifyISOTopology
	verifyISOTopologyAbsentForRuntime  = netns.VerifyISOTopologyAbsent
	removeISOTopologyForRuntime        = netns.RemoveISOTopology
	acquireISOOperationLockForRuntime  = acquireISOOperationLock
	verifyISODNSReadyForVM             = verifyISODNSListenerReady
	dockerComposeServiceForISO         = func(server *Server, service string) (*svc.DockerComposeService, error) {
		return server.dockerComposeService(service)
	}
	inspectISOProjectForRuntime = svc.InspectISOProject
	runISOSystemctlForRuntime   = func(ctx context.Context, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, "systemctl", args...).CombinedOutput()
	}
	runISOReconcileVMSystemctl = func(ctx context.Context, action, service string) error {
		output, err := runISOSystemctlForRuntime(ctx, action, vmSystemdUnitName(service))
		if err != nil {
			return fmt.Errorf("systemctl %s %s: %w: %s", action, vmSystemdUnitName(service), err, strings.TrimSpace(string(output)))
		}
		return nil
	}
)

func verifyISODNSListenerReady(ctx context.Context) error {
	dialer := net.Dialer{Timeout: time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", isoDNSPort))
	if err != nil {
		return fmt.Errorf("verify ISO DNS listener: %w", err)
	}
	if err := conn.Close(); err != nil {
		return fmt.Errorf("close ISO DNS readiness connection: %w", err)
	}
	return nil
}

type isoRuntimeNetworkSpec struct {
	Backend    netns.FirewallBackend
	Policy     netns.ISOPolicySpec
	Topology   netns.ISOTopologySpec
	Topologies []isoRuntimeTopology
	VM         bool
}

type isoRuntimeTopology struct {
	Service string
	Spec    netns.ISOTopologySpec
}

// isoRuntimeSpec derives the global root policy from every allocation that has
// not durably completed cleanup. A per-service ensure therefore cannot erase
// another service's still-reserved firewall identity.
func (s *Server) isoRuntimeSpec(dv db.DataView, service string) (isoRuntimeNetworkSpec, error) {
	if !dv.Valid() {
		return isoRuntimeNetworkSpec{}, fmt.Errorf("ISO network requires database state")
	}
	pool := dv.ISOPool()
	if !pool.Valid() {
		return isoRuntimeNetworkSpec{}, fmt.Errorf("ISO pool is not configured")
	}
	if err := validateISORuntimeState(dv); err != nil {
		return isoRuntimeNetworkSpec{}, err
	}
	targetView, ok := dv.Services().GetOk(service)
	if !ok || !targetView.ISO().Valid() {
		return isoRuntimeNetworkSpec{}, fmt.Errorf("service %q has no ISO allocation", service)
	}
	backend, err := detectISOFirewallBackendForRuntime()
	if err != nil {
		return isoRuntimeNetworkSpec{}, fmt.Errorf("detect ISO firewall backend: %w", err)
	}

	endpoints := isoRuntimeEndpoints(dv)
	target := targetView.ISO().AsStruct()
	topologies := isoRuntimeTopologies(dv, backend, pool.Prefix())
	return isoRuntimeNetworkSpec{
		Backend: backend,
		Policy: netns.ISOPolicySpec{
			Pool:      pool.Prefix(),
			DNSPort:   isoDNSPort,
			Endpoints: endpoints,
		},
		Topology: netns.ISOTopologySpec{
			Backend:            backend,
			Pool:               pool.Prefix(),
			Allocation:         *target,
			TailscaleInterface: isoRuntimeTailscaleInterface(target.DesiredModes),
		},
		Topologies: topologies,
		VM:         target.Kind == string(iso.PayloadVM),
	}, nil
}

func isoRuntimeTopologies(dv db.DataView, backend netns.FirewallBackend, pool netip.Prefix) []isoRuntimeTopology {
	names := make([]string, 0, dv.Services().Len())
	for name := range dv.Services().All() {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]isoRuntimeTopology, 0, len(names))
	for _, name := range names {
		view := dv.Services().Get(name).ISO()
		if !view.Valid() || view.RemoveRequested() && view.CleanupVerified() {
			continue
		}
		allocation := view.AsStruct()
		out = append(out, isoRuntimeTopology{Service: name, Spec: netns.ISOTopologySpec{
			Backend:            backend,
			Pool:               pool,
			Allocation:         *allocation,
			TailscaleInterface: isoRuntimeTailscaleInterface(allocation.DesiredModes),
		}})
	}
	return out
}

func validateISORuntimeState(dv db.DataView) error {
	pool := dv.ISOPool()
	if !pool.Valid() {
		return fmt.Errorf("ISO pool is not configured")
	}
	layout, err := iso.NewLayout(pool.Prefix())
	if err != nil {
		return err
	}
	names := make([]string, 0, dv.Services().Len())
	for name := range dv.Services().All() {
		names = append(names, name)
	}
	sort.Strings(names)
	links := map[netip.Prefix]string{}
	projects := map[netip.Prefix]string{}
	identities := map[string]string{}
	for _, name := range names {
		view := dv.Services().Get(name).ISO()
		if !view.Valid() {
			continue
		}
		allocation := view.AsStruct()
		if err := validateISORuntimeAllocation(layout, name, allocation, links, projects, identities); err != nil {
			return err
		}
	}
	return nil
}

func validateISORuntimeAllocation(
	layout iso.Layout,
	service string,
	allocation *db.ISOAllocation,
	links map[netip.Prefix]string,
	projects map[netip.Prefix]string,
	identities map[string]string,
) error {
	if allocation == nil {
		return isoRuntimeAllocationError(service, "is missing")
	}
	if allocation.AllocatorVersion != iso.AllocatorVersion || allocation.PolicyVersion != iso.PolicyVersion {
		return isoRuntimeAllocationError(service, "version mismatch")
	}
	if err := validateISORuntimeLink(layout, service, allocation, links); err != nil {
		return err
	}
	if err := validateISORuntimeLinkIdentities(service, allocation, identities); err != nil {
		return err
	}
	if err := validateISORuntimePayload(service, allocation); err != nil {
		return err
	}
	if iso.PayloadKind(allocation.Kind) == iso.PayloadVM {
		return validateISORuntimeVM(service, allocation)
	}
	return validateISORuntimeRouter(layout, service, allocation, projects, identities)
}

func isoRuntimeAllocationError(service, format string, args ...any) error {
	return fmt.Errorf("service %q ISO allocation: %s", service, fmt.Sprintf(format, args...))
}

func validateISORuntimeLink(
	layout iso.Layout,
	service string,
	allocation *db.ISOAllocation,
	links map[netip.Prefix]string,
) error {
	link := allocation.Link.Masked()
	if allocation.Link != link || link.Bits() != 30 || !layout.Links.Contains(link.Addr()) {
		return isoRuntimeAllocationError(service, "link %v is not an allocated lower-half /30", allocation.Link)
	}
	if allocation.HostIP != link.Addr().Next() || allocation.PeerIP != link.Addr().Next().Next() {
		return isoRuntimeAllocationError(service, "link addresses must be .1 and .2")
	}
	if owner, exists := links[link]; exists {
		return isoRuntimeAllocationError(service, "link route %s duplicates service %q", link, owner)
	}
	links[link] = service
	return nil
}

func validateISORuntimeLinkIdentities(service string, allocation *db.ISOAllocation, identities map[string]string) error {
	token := isoNameToken(service)
	if allocation.Interface != "yi-"+token || allocation.PeerInterface != "yo-"+token {
		return isoRuntimeAllocationError(service, "generated interface identities do not match service token")
	}
	for _, identity := range []string{allocation.Interface, allocation.PeerInterface} {
		if owner, exists := identities[identity]; exists {
			return isoRuntimeAllocationError(service, "identity %q duplicates service %q", identity, owner)
		}
		identities[identity] = service
	}
	return nil
}

func validateISORuntimePayload(service string, allocation *db.ISOAllocation) error {
	kind := iso.PayloadKind(allocation.Kind)
	if kind != iso.PayloadVM && kind != iso.PayloadCompose && kind != iso.PayloadContainer {
		return isoRuntimeAllocationError(service, "unsupported payload kind %q", allocation.Kind)
	}
	if err := iso.ValidateNetwork(iso.NetworkRequest{Payload: kind, Modes: allocation.DesiredModes}); err != nil {
		return isoRuntimeAllocationError(service, "invalid modes: %v", err)
	}
	return nil
}

func validateISORuntimeVM(service string, allocation *db.ISOAllocation) error {
	if allocation.Project.IsValid() || allocation.Gateway.IsValid() || allocation.NetNS != "" || allocation.Bridge != "" ||
		len(allocation.Components) != 0 || len(allocation.RetiredComponents) != 0 {
		return isoRuntimeAllocationError(service, "VM contains router or project state")
	}
	return nil
}

func validateISORuntimeRouter(
	layout iso.Layout,
	service string,
	allocation *db.ISOAllocation,
	projects map[netip.Prefix]string,
	identities map[string]string,
) error {
	wantNetNS := isoRouterNamespace(service)
	if allocation.NetNS != wantNetNS {
		return isoRuntimeAllocationError(service, "namespace %q does not match %q", allocation.NetNS, wantNetNS)
	}
	if owner, exists := identities[allocation.NetNS]; exists {
		return isoRuntimeAllocationError(service, "identity %q duplicates service %q", allocation.NetNS, owner)
	}
	identities[allocation.NetNS] = service
	if allocation.Bridge != "" && allocation.Bridge != "br0" {
		return isoRuntimeAllocationError(service, "router bridge must be br0")
	}
	project := allocation.Project.Masked()
	if allocation.Project != project || project.Bits() != 27 || !layout.Projects.Contains(project.Addr()) {
		return isoRuntimeAllocationError(service, "project %v is not an allocated upper-half /27", allocation.Project)
	}
	if allocation.Gateway != project.Addr().Next() {
		return isoRuntimeAllocationError(service, "project gateway must be .1")
	}
	if owner, exists := projects[project]; exists {
		return isoRuntimeAllocationError(service, "project route %s duplicates service %q", project, owner)
	}
	projects[project] = service
	return validateISORuntimeComponents(service, project, allocation.Components, allocation.RetiredComponents)
}

func validateISORuntimeComponents(service string, project netip.Prefix, groups ...map[string]db.ISOComponent) error {
	names := map[string]bool{}
	addresses := map[netip.Addr]string{}
	for _, group := range groups {
		if err := validateISORuntimeComponentGroup(service, project, group, names, addresses); err != nil {
			return err
		}
	}
	return nil
}

func validateISORuntimeComponentGroup(
	service string,
	project netip.Prefix,
	components map[string]db.ISOComponent,
	names map[string]bool,
	addresses map[netip.Addr]string,
) error {
	for name, component := range components {
		if err := validateISORuntimeComponent(service, project, name, component, names, addresses); err != nil {
			return err
		}
	}
	return nil
}

func validateISORuntimeComponent(
	service string,
	project netip.Prefix,
	name string,
	component db.ISOComponent,
	names map[string]bool,
	addresses map[netip.Addr]string,
) error {
	if name == "" || names[name] {
		return isoRuntimeAllocationError(service, "duplicate or empty component %q", name)
	}
	names[name] = true
	if !isUsableISOComponentAddress(project, component.Address) {
		return isoRuntimeAllocationError(service, "component %q address %v is outside usable project hosts", name, component.Address)
	}
	if owner, exists := addresses[component.Address]; exists {
		return isoRuntimeAllocationError(service, "component address %v duplicates %q", component.Address, owner)
	}
	addresses[component.Address] = name
	return nil
}

func isUsableISOComponentAddress(project netip.Prefix, address netip.Addr) bool {
	if !address.Is4() || !project.Contains(address) {
		return false
	}
	offset := int(address.As4()[3]) - int(project.Addr().As4()[3])
	return offset >= 2 && offset <= 30
}

func isoRuntimeEndpoints(dv db.DataView) []netns.ISOEndpoint {
	names := make([]string, 0, dv.Services().Len())
	for name := range dv.Services().All() {
		names = append(names, name)
	}
	sort.Strings(names)
	endpoints := make([]netns.ISOEndpoint, 0, len(names))
	for _, name := range names {
		allocation := dv.Services().Get(name).ISO()
		if !allocation.Valid() || allocation.RemoveRequested() && allocation.CleanupVerified() {
			continue
		}
		endpoints = append(endpoints, netns.ISOEndpoint{
			Interface: allocation.Interface(),
			Link:      allocation.Link(),
			PeerIP:    allocation.PeerIP(),
			Project:   allocation.Project(),
			Tailscale: slices.Contains(allocation.DesiredModes().AsSlice(), "ts"),
		})
	}
	return endpoints
}

func isoRuntimeTailscaleInterface(modes []string) string {
	if slices.Contains(modes, "ts") {
		return isoTailscaleInterface
	}
	return ""
}

const isoTailscaleInterface = "ts0"

func acquireISOOperationLock(ctx context.Context, rootDir string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, _, err := openValidatedISOOperationRootDir(rootDir)
	if err != nil {
		return nil, err
	}
	file, fd, err := openISOOperationLockFileAt(int(dir.Fd()))
	_ = dir.Close()
	if err != nil {
		return nil, err
	}
	if err := waitForISOOperationLock(ctx, fd); err != nil {
		_ = file.Close()
		return nil, err
	}
	return newISOOperationUnlock(
		func() { _ = unix.Flock(fd, unix.LOCK_UN) },
		func() { _ = file.Close() },
	), nil
}

func openISOOperationLockFileAt(dirFD int) (*os.File, int, error) {
	fd, created, err := openISOOperationLockFDAt(dirFD, unix.Openat)
	if err != nil {
		return nil, -1, err
	}
	file := os.NewFile(uintptr(fd), isoOperationLockFileName)
	if file == nil {
		_ = unix.Close(fd)
		return nil, -1, fmt.Errorf("open ISO operation lock %s: invalid file descriptor", isoOperationLockFileName)
	}
	if err := secureISOOperationLockFile(fd, created); err != nil {
		_ = file.Close()
		return nil, -1, err
	}
	return file, fd, nil
}

type isoOperationOpenAtFunc func(int, string, int, uint32) (int, error)

func openISOOperationLockFDAt(dirFD int, openAt isoOperationOpenAtFunc) (int, bool, error) {
	flags := unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW
	fd, err := openAt(dirFD, isoOperationLockFileName, flags|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err == nil {
		return fd, true, nil
	}
	if !errors.Is(err, unix.EEXIST) {
		return -1, false, fmt.Errorf("create ISO operation lock %s: %w", isoOperationLockFileName, err)
	}
	fd, err = openAt(dirFD, isoOperationLockFileName, flags, 0)
	if err != nil {
		return -1, false, fmt.Errorf("open existing ISO operation lock %s: %w", isoOperationLockFileName, err)
	}
	return fd, false, nil
}

func secureISOOperationLockFile(fd int, created bool) error {
	effectiveUID := uint32(os.Geteuid())
	stat, err := inspectAndValidateISOOperationLock(fd, effectiveUID)
	if err != nil {
		return err
	}
	if !created && stat.mode&0o7777 != 0o600 {
		return fmt.Errorf("existing ISO operation lock %s mode is %#o, want 0600", isoOperationLockFileName, stat.mode&0o7777)
	}
	if created {
		if err := unix.Fchmod(fd, 0o600); err != nil {
			return fmt.Errorf("secure ISO operation lock %s: %w", isoOperationLockFileName, err)
		}
	}
	stat, err = inspectAndValidateISOOperationLock(fd, effectiveUID)
	if err != nil {
		return err
	}
	if stat.mode&0o7777 != 0o600 {
		return fmt.Errorf("ISO operation lock %s mode is %#o, want 0600", isoOperationLockFileName, stat.mode&0o7777)
	}
	return nil
}

func inspectAndValidateISOOperationLock(fd int, effectiveUID uint32) (isoOperationInodeStat, error) {
	stat, err := inspectISOOperationInode(fd)
	if err != nil {
		return isoOperationInodeStat{}, fmt.Errorf("inspect ISO operation lock %s: %w", isoOperationLockFileName, err)
	}
	if err := validateISOOperationLockStat(isoOperationLockFileName, stat, effectiveUID); err != nil {
		return isoOperationInodeStat{}, err
	}
	return stat, nil
}

func newISOOperationUnlock(unlock, closeFile func()) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			unlock()
			closeFile()
		})
	}
}

func waitForISOOperationLock(ctx context.Context, fd int) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err == nil {
			return nil
		} else if !isISOOperationLockBusy(err) {
			return fmt.Errorf("lock ISO network operations: %w", err)
		}
		if err := waitForISOOperationLockPoll(ctx); err != nil {
			return err
		}
	}
}

func waitForISOOperationLockPoll(ctx context.Context) error {
	timer := time.NewTimer(isoOperationLockPollInterval)
	select {
	case <-ctx.Done():
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type isoOperationInodeStat struct {
	mode  uint32
	uid   uint32
	nlink uint64
}

func inspectISOOperationInode(fd int) (isoOperationInodeStat, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return isoOperationInodeStat{}, err
	}
	return isoOperationInodeStat{
		mode:  uint32(stat.Mode),
		uid:   stat.Uid,
		nlink: uint64(stat.Nlink),
	}, nil
}

func openValidatedISOOperationRootDir(rootDir string) (*os.File, string, error) {
	cleaned, err := cleanISOOperationRootDir(rootDir)
	if err != nil {
		return nil, "", err
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, "", fmt.Errorf("open filesystem root for ISO network: %w", err)
	}
	currentPath := string(filepath.Separator)
	effectiveUID := uint32(os.Geteuid())
	components := strings.Split(strings.TrimPrefix(cleaned, string(filepath.Separator)), string(filepath.Separator))
	if err := validateISOOperationDirectoryFD(currentPath, fd, effectiveUID, false); err != nil {
		_ = unix.Close(fd)
		return nil, "", err
	}
	for index, component := range components {
		nextFD, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, "", fmt.Errorf("open ISO network RootDir component %s: %w", filepath.Join(currentPath, component), openErr)
		}
		fd = nextFD
		currentPath = filepath.Join(currentPath, component)
		if err := validateISOOperationDirectoryFD(currentPath, fd, effectiveUID, index == len(components)-1); err != nil {
			_ = unix.Close(fd)
			return nil, "", err
		}
	}
	dir := os.NewFile(uintptr(fd), cleaned)
	if dir == nil {
		_ = unix.Close(fd)
		return nil, "", fmt.Errorf("open ISO network RootDir %s: invalid file descriptor", cleaned)
	}
	return dir, cleaned, nil
}

func cleanISOOperationRootDir(rootDir string) (string, error) {
	if strings.TrimSpace(rootDir) == "" {
		return "", fmt.Errorf("ISO network requires Config.RootDir")
	}
	rootDir = filepath.Clean(rootDir)
	if !filepath.IsAbs(rootDir) || rootDir == string(filepath.Separator) {
		return "", fmt.Errorf("ISO network requires a safe absolute Config.RootDir, got %q", rootDir)
	}
	return rootDir, nil
}

func validateISOOperationDirectoryFD(path string, fd int, effectiveUID uint32, final bool) error {
	stat, err := inspectISOOperationInode(fd)
	if err != nil {
		return fmt.Errorf("inspect ISO network RootDir component %s: %w", path, err)
	}
	return validateISOOperationDirectoryStat(path, stat, effectiveUID, final)
}

func validateISOOperationDirectoryStat(path string, stat isoOperationInodeStat, effectiveUID uint32, final bool) error {
	if stat.mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("ISO network RootDir component %s is not a directory", path)
	}
	if stat.uid != 0 && stat.uid != effectiveUID {
		return fmt.Errorf("ISO network RootDir component %s is owned by uid %d, want root or effective uid %d", path, stat.uid, effectiveUID)
	}
	if final && stat.uid != effectiveUID {
		return fmt.Errorf("ISO network RootDir %s is owned by uid %d, want effective uid %d", path, stat.uid, effectiveUID)
	}
	if stat.mode&0o022 != 0 && (final || stat.mode&unix.S_ISVTX == 0) {
		return fmt.Errorf("ISO network RootDir component %s is writable by group or world without safe sticky protection", path)
	}
	return nil
}

func validateISOOperationLockStat(path string, stat isoOperationInodeStat, effectiveUID uint32) error {
	if stat.mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("ISO operation lock %s is not a regular file", path)
	}
	if stat.uid != effectiveUID {
		return fmt.Errorf("ISO operation lock %s is owned by uid %d, want effective uid %d", path, stat.uid, effectiveUID)
	}
	if stat.nlink != 1 {
		return fmt.Errorf("ISO operation lock %s has %d links, want exactly one", path, stat.nlink)
	}
	return nil
}

func isISOOperationLockBusy(err error) bool {
	return errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN)
}

func (s *Server) withISOOperationLock(ctx context.Context, operation func() error) error {
	unlock, err := acquireISOOperationLockForRuntime(ctx, s.cfg.RootDir)
	if err != nil {
		return err
	}
	defer unlock()
	return operation()
}

// EnsureISONetwork verifies the complete host policy before allowing any
// service veth to be attached. Service lifecycle callers already require the
// manage permission; this local helper adds no RPC or remote operation.
func (s *Server) EnsureISONetwork(ctx context.Context, service string) error {
	if s == nil || s.cfg.DB == nil {
		return fmt.Errorf("ISO network requires a config DB")
	}
	return s.withISOOperationLock(ctx, func() error {
		return s.ensureISONetworkLocked(ctx, service)
	})
}

func (s *Server) ensureISONetworkLocked(ctx context.Context, service string) error {
	if err := s.ensureISONetworkBoundaryLocked(ctx, service); err != nil {
		return err
	}
	if err := s.markISOReady(service); err != nil {
		return s.failISORuntime(err)
	}
	return nil
}

// EnsureISONetworkBoundary verifies policy and topology without changing the
// allocation to ready. Preparation and background reconciliation use this
// form; only the actual workload start gate may mark ready.
func (s *Server) EnsureISONetworkBoundary(ctx context.Context, service string) error {
	if s == nil || s.cfg.DB == nil {
		return fmt.Errorf("ISO network requires a config DB")
	}
	return s.withISOOperationLock(ctx, func() error {
		return s.ensureISONetworkBoundaryLocked(ctx, service)
	})
}

func (s *Server) ensureISONetworkBoundaryLocked(ctx context.Context, service string) error {
	dv, err := s.cfg.DB.Get()
	if err != nil {
		return err
	}
	if err := validateISORuntimeState(dv); err != nil {
		return s.failISORuntime(err)
	}
	spec, err := s.isoRuntimeSpec(dv, service)
	if err != nil {
		return s.failISORuntime(err)
	}
	if err := reconcileISONetwork(ctx, service, spec); err != nil {
		return s.failISORuntime(err)
	}
	return nil
}

type isoReconcileSteps interface {
	ValidatePool(context.Context) error
	InstallDNS(context.Context) error
	EnsurePolicy(context.Context, string) error
	VerifyPolicy(context.Context, string) error
	EnsureTopology(context.Context, string) error
	VerifyTopology(context.Context, string) error
	InspectRuntime(context.Context, string) (isoReconcileRuntimeState, error)
	VerifyStopped(context.Context, string) error
	VerifyTailscale(context.Context, string) error
	ResumeRemoval(context.Context, string) error
	StopUntrusted(context.Context, string) error
	Quarantine(context.Context, string, error) error
	VerifyGlobalPolicy(context.Context) error
	RestartTrusted(context.Context, string) error
}

type isoReconcileRuntimeState string

const (
	isoReconcileRuntimeRunning isoReconcileRuntimeState = "running"
	isoReconcileRuntimeAbsent  isoReconcileRuntimeState = "absent"
)

type isoConcreteReconcileSteps struct {
	server *Server
	unlock func()
}

func (s *Server) reconcileISONetworks(ctx context.Context) error {
	return s.reconcileISONetworksWith(ctx, &isoConcreteReconcileSteps{server: s})
}

func (r *isoConcreteReconcileSteps) ValidatePool(context.Context) error {
	dv, err := r.server.cfg.DB.Get()
	if err != nil {
		return err
	}
	if !hasISOAllocations(dv) {
		return nil
	}
	return validateISORuntimeState(dv)
}

func hasISOAllocations(dv db.DataView) bool {
	for _, service := range dv.Services().All() {
		if service.ISO().Valid() {
			return true
		}
	}
	return false
}

func (r *isoConcreteReconcileSteps) InstallDNS(context.Context) error {
	return installISODNSServiceForServer(r.server.cfg.RootDir)
}

func (r *isoConcreteReconcileSteps) EnsurePolicy(ctx context.Context, service string) error {
	unlock, err := acquireISOOperationLockForRuntime(ctx, r.server.cfg.RootDir)
	if err != nil {
		return err
	}
	r.unlock = unlock
	spec, err := r.server.loadISORuntimeSpec(service)
	if err != nil {
		r.release()
		return err
	}
	rules, err := netns.RenderISOPolicy(spec.Backend, spec.Policy)
	if err != nil {
		r.release()
		return err
	}
	if err := ensureISOPolicyForRuntime(ctx, rules); err != nil {
		r.release()
		return err
	}
	return nil
}

func (r *isoConcreteReconcileSteps) VerifyPolicy(ctx context.Context, service string) error {
	spec, err := r.server.loadISORuntimeSpec(service)
	if err != nil {
		r.release()
		return err
	}
	rules, err := netns.RenderISOPolicy(spec.Backend, spec.Policy)
	if err != nil {
		r.release()
		return err
	}
	if err := verifyISOPolicyForRuntime(ctx, rules); err != nil {
		r.release()
		return err
	}
	if spec.VM {
		defer r.release()
		return r.server.ensureOrVerifyVMISOAttachmentForStartup(ctx, service)
	}
	return nil
}

func (s *Server) ensureOrVerifyVMISOAttachmentForStartup(ctx context.Context, service string) error {
	dv, err := s.getDB()
	if err != nil {
		return err
	}
	plan, _, err := vmNetworkPlanAndISOForService(dv, service)
	if err != nil {
		return err
	}
	if !plan.hasNetworkMode("iso") {
		return fmt.Errorf("service %q has a VM ISO allocation but no ISO VM network", service)
	}
	identity, err := vmNetworkEnsureRuntimeIdentity()
	if err != nil {
		return err
	}
	plan = plan.WithTapOwner(identity)
	running, err := s.IsServiceRunning(service)
	if err != nil {
		return err
	}
	if err := ensureVMISOAttachmentForStartup(plan, service, running); err != nil {
		return err
	}
	if err := verifyVMNetworkPlanForReconcile(ctx, plan); err != nil {
		if running {
			return err
		}
		return errors.Join(err, cleanupVMISONetworkPlan(plan))
	}
	return nil
}

func ensureVMISOAttachmentForStartup(plan vmNetworkPlan, service string, running bool) error {
	if running {
		return ensureRunningVMISOSecuritySysctls(plan, service)
	}
	if err := ensureOwnedVMNetwork(plan, service); err != nil {
		return errors.Join(err, cleanupVMISONetworkPlan(plan))
	}
	return nil
}

func (r *isoConcreteReconcileSteps) EnsureTopology(ctx context.Context, service string) error {
	spec, err := r.server.loadISORuntimeSpec(service)
	if err != nil {
		r.release()
		return err
	}
	if err := ensureISOTopologyForRuntime(ctx, spec.Topology); err != nil {
		r.release()
		return err
	}
	return nil
}

func (r *isoConcreteReconcileSteps) VerifyTopology(ctx context.Context, service string) error {
	defer r.release()
	spec, err := r.server.loadISORuntimeSpec(service)
	if err != nil {
		return err
	}
	return verifyISOTopologyForRuntime(ctx, spec.Topology)
}

func (r *isoConcreteReconcileSteps) InspectRuntime(ctx context.Context, service string) (isoReconcileRuntimeState, error) {
	view, err := r.server.serviceView(service)
	if err != nil {
		return "", err
	}
	if view.ServiceType() == db.ServiceTypeVM {
		return r.inspectISOVMRuntime(service)
	}
	return r.inspectISOComposeRuntime(ctx, service)
}

func (r *isoConcreteReconcileSteps) inspectISOVMRuntime(service string) (isoReconcileRuntimeState, error) {
	running, err := r.server.IsServiceRunning(service)
	if err != nil {
		return "", err
	}
	if running {
		return isoReconcileRuntimeRunning, nil
	}
	return isoReconcileRuntimeAbsent, nil
}

func (r *isoConcreteReconcileSteps) inspectISOComposeRuntime(ctx context.Context, service string) (isoReconcileRuntimeState, error) {
	compose, record, err := r.composeService(service)
	if err != nil {
		return "", err
	}
	running, err := compose.AnyRunningContext(ctx)
	if err != nil {
		return "", err
	}
	if !running {
		return inspectAbsentISOComposeRuntime(ctx, compose)
	}
	return r.inspectRunningISOComposeRuntime(ctx, service, compose, record)
}

func inspectAbsentISOComposeRuntime(ctx context.Context, compose *svc.DockerComposeService) (isoReconcileRuntimeState, error) {
	err := errors.Join(compose.VerifyProjectAbsent(ctx), compose.VerifyDefaultNetworkAbsent(ctx))
	if err != nil {
		return "", fmt.Errorf("ISO Compose runtime is not cleanly absent: %w", err)
	}
	return isoReconcileRuntimeAbsent, nil
}

func (r *isoConcreteReconcileSteps) inspectRunningISOComposeRuntime(ctx context.Context, service string, compose *svc.DockerComposeService, record *db.Service) (isoReconcileRuntimeState, error) {
	base, overlay, err := isoComposeInspectionArtifacts(record)
	if err != nil {
		return "", err
	}
	inspection, err := inspectISOProjectForRuntime(ctx, svc.ISOInspectOptions{
		ProjectName: svc.ComposeProjectName(service), ProjectDir: compose.DataDir,
		ComposeFiles: []string{base, overlay}, NetworkName: svc.ComposeProjectName(service) + "_default",
		ServiceRoot: r.server.serviceRootFromView(record.View()), Components: isoComponentAddresses(record.ISO.Components),
	})
	if err != nil {
		return "", err
	}
	if err := inspection.Verify(); err != nil {
		return "", err
	}
	return isoReconcileRuntimeRunning, nil
}

func isoComposeInspectionArtifacts(record *db.Service) (string, string, error) {
	base, ok := record.Artifacts.Gen(db.ArtifactDockerComposeFile, record.Generation)
	if !ok {
		return "", "", fmt.Errorf("ISO Compose base artifact is missing for generation %d", record.Generation)
	}
	overlay, ok := record.Artifacts.Gen(db.ArtifactDockerComposeNetwork, record.Generation)
	if !ok {
		return "", "", fmt.Errorf("ISO Compose overlay artifact is missing for generation %d", record.Generation)
	}
	return base, overlay, nil
}

func isoComponentAddresses(components map[string]db.ISOComponent) map[string]netip.Addr {
	addresses := make(map[string]netip.Addr, len(components))
	for name, component := range components {
		addresses[name] = component.Address
	}
	return addresses
}

func (r *isoConcreteReconcileSteps) VerifyStopped(ctx context.Context, service string) error {
	view, err := r.server.serviceView(service)
	if err != nil {
		return err
	}
	if view.ServiceType() == db.ServiceTypeVM {
		return r.verifyStoppedISOVM(service)
	}
	return r.verifyStoppedISOCompose(ctx, service, view)
}

func (r *isoConcreteReconcileSteps) verifyStoppedISOVM(service string) error {
	running, err := r.server.IsServiceRunning(service)
	if err != nil {
		return err
	}
	if running {
		return fmt.Errorf("ISO VM %q is running while allocation is stopped", service)
	}
	return nil
}

func (r *isoConcreteReconcileSteps) verifyStoppedISOCompose(ctx context.Context, service string, view db.ServiceView) error {
	compose, _, err := r.composeService(service)
	if err != nil {
		return err
	}
	running, err := compose.AnyRunningContext(ctx)
	if err != nil {
		return err
	}
	if running {
		return fmt.Errorf("ISO Compose workload %q is running while allocation is stopped", service)
	}
	return verifyISOAuxiliaryUnitsStopped(ctx, view.AsStruct())
}

func (r *isoConcreteReconcileSteps) composeService(service string) (*svc.DockerComposeService, *db.Service, error) {
	view, err := r.server.serviceView(service)
	if err != nil {
		return nil, nil, err
	}
	record := view.AsStruct()
	compose, err := dockerComposeServiceForISO(r.server, service)
	return compose, record, err
}

func (r *isoConcreteReconcileSteps) VerifyTailscale(ctx context.Context, service string) error {
	view, err := r.server.serviceView(service)
	if err != nil {
		return err
	}
	record := view.AsStruct()
	if record.TSNet == nil {
		return fmt.Errorf("ISO service %q requests Tailscale without persisted Tailscale state", service)
	}
	unit := "yeet-" + service + "-ts.service"
	if err := verifyISOTailscaleUnit(ctx, unit); err != nil {
		return err
	}
	socket := filepath.Join(serviceRunDirForRoot(r.server.serviceRootFromView(view)), "tailscaled.sock")
	return verifyISOTailscaleSocket(socket)
}

func verifyISOTailscaleUnit(ctx context.Context, unit string) error {
	output, err := runISOSystemctlForRuntime(ctx, "show", "--property=ActiveState", "--value", unit)
	if err != nil {
		return fmt.Errorf("verify ISO Tailscale unit %s: %w: %s", unit, err, strings.TrimSpace(string(output)))
	}
	if state := strings.TrimSpace(string(output)); state != "active" {
		return fmt.Errorf("ISO Tailscale unit %s is %s", unit, state)
	}
	return nil
}

func verifyISOTailscaleSocket(socket string) error {
	info, err := os.Stat(socket)
	if err != nil {
		return fmt.Errorf("verify ISO Tailscale socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("ISO Tailscale socket path %q is not a socket", socket)
	}
	return nil
}

func (r *isoConcreteReconcileSteps) ResumeRemoval(ctx context.Context, service string) error {
	view, err := r.server.serviceView(service)
	if err != nil {
		return err
	}
	_, err = r.server.RemoveServiceWithOptions(service, RemoveOptions{CleanData: view.ISO().RemoveCleanData()})
	return err
}

func (r *isoConcreteReconcileSteps) StopUntrusted(ctx context.Context, service string) error {
	r.release()
	view, viewErr := r.server.serviceView(service)
	if viewErr != nil {
		return viewErr
	}
	if view.ServiceType() == db.ServiceTypeVM {
		return runISOReconcileVMSystemctl(ctx, "stop", service)
	}
	compose, record, err := r.composeService(service)
	if err != nil {
		return err
	}
	downErr := compose.StopProjectContainers(ctx)
	auxErr := stopAndVerifyISOAuxiliaryUnits(ctx, record)
	return errors.Join(downErr, auxErr)
}

func (r *isoConcreteReconcileSteps) Quarantine(_ context.Context, service string, cause error) error {
	r.release()
	state := string(iso.StateQuarantined)
	if view, err := r.server.serviceView(service); err == nil && view.ISO().RemoveRequested() {
		state = string(iso.StateTombstoned)
	}
	return r.server.markISOState(service, state, cause)
}

func (r *isoConcreteReconcileSteps) VerifyGlobalPolicy(ctx context.Context) error {
	r.release()
	unlock, err := acquireISOOperationLockForRuntime(ctx, r.server.cfg.RootDir)
	if err != nil {
		return err
	}
	defer unlock()
	rules, present, err := r.server.currentGlobalISOPolicy()
	if err != nil || !present {
		return err
	}
	return verifyISOPolicyForRuntime(ctx, rules)
}

func (r *isoConcreteReconcileSteps) RestartTrusted(ctx context.Context, service string) error {
	r.release()
	view, err := r.server.serviceView(service)
	if err != nil {
		return err
	}
	if view.ServiceType() == db.ServiceTypeVM {
		return runISOReconcileVMSystemctl(ctx, "start", service)
	}
	installer, err := r.server.NewInstaller(InstallerCfg{ServiceName: service})
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return installer.installGen(view.Generation())
}

func (r *isoConcreteReconcileSteps) release() {
	if r.unlock != nil {
		r.unlock()
		r.unlock = nil
	}
}

func (s *Server) currentGlobalISOPolicy() (netns.ISOPolicyRules, bool, error) {
	dv, err := s.cfg.DB.Get()
	if err != nil {
		return netns.ISOPolicyRules{}, false, err
	}
	pool := dv.ISOPool()
	if !pool.Valid() {
		if hasISOAllocations(dv) {
			return netns.ISOPolicyRules{}, false, fmt.Errorf("ISO pool is not configured")
		}
		return netns.ISOPolicyRules{}, false, nil
	}
	if err := validateISORuntimeState(dv); err != nil {
		return netns.ISOPolicyRules{}, false, err
	}
	backend, err := detectISOFirewallBackendForRuntime()
	if err != nil {
		return netns.ISOPolicyRules{}, false, err
	}
	rules, err := netns.RenderISOPolicy(backend, netns.ISOPolicySpec{
		Pool: pool.Prefix(), DNSPort: isoDNSPort, Endpoints: isoRuntimeEndpoints(dv),
	})
	return rules, true, err
}

// reconcileISONetworksWith is the synchronous startup security gate. The
// concrete adapter uses Docker, DNS, firewall, topology, and Tailscale state;
// the interface keeps stop-before-quarantine ordering failure-injectable.
//
//nolint:cyclop,gocognit // Startup reconciliation is an ordered fail-closed state machine kept linear for auditability.
func (s *Server) reconcileISONetworksWith(ctx context.Context, steps isoReconcileSteps) error {
	if s == nil || s.cfg.DB == nil {
		return fmt.Errorf("ISO reconciliation requires a config database")
	}
	dv, err := s.cfg.DB.Get()
	if err != nil {
		return err
	}
	data := dv.AsStruct()
	names := make([]string, 0, len(data.Services))
	allocations := make(map[string]*db.ISOAllocation, len(data.Services))
	for name, service := range data.Services {
		if service == nil || service.ISO == nil {
			continue
		}
		names = append(names, name)
		allocations[name] = service.ISO
	}
	sort.Strings(names)

	var reconciliationErrs []error
	var restartCandidates []string
	if err := runISOReconcileGlobalPhase(ctx, "validate ISO pool", steps.ValidatePool); err != nil {
		reconciliationErrs = append(reconciliationErrs, err)
	}
	if len(reconciliationErrs) == 0 {
		if err := runISOReconcileGlobalPhase(ctx, "install ISO DNS", steps.InstallDNS); err != nil {
			reconciliationErrs = append(reconciliationErrs, err)
		}
	}
	if len(reconciliationErrs) != 0 {
		cause := errors.Join(reconciliationErrs...)
		for _, name := range names {
			reconciliationErrs = append(reconciliationErrs, stopAndQuarantineISO(ctx, steps, name, cause))
		}
	} else {
		for _, name := range names {
			if allocations[name].RemoveRequested {
				if err := steps.ResumeRemoval(ctx, name); err != nil {
					reconciliationErrs = append(reconciliationErrs, fmt.Errorf("resume ISO removal for %q: %w", name, err))
				}
				continue
			}
			restart, err := reconcileISOServiceWith(ctx, steps, name, allocations[name])
			if err != nil {
				reconciliationErrs = append(reconciliationErrs, err)
				continue
			}
			if restart {
				restartCandidates = append(restartCandidates, name)
			}
		}
	}
	if err := runISOReconcileGlobalPhase(ctx, "verify global ISO policy", steps.VerifyGlobalPolicy); err != nil {
		reconciliationErrs = append(reconciliationErrs, err)
		stopNames := names
		if current, loadErr := s.currentISOAllocationNames(); loadErr != nil {
			reconciliationErrs = append(reconciliationErrs, fmt.Errorf("reload ISO records after global policy failure: %w", loadErr))
		} else {
			stopNames = current
		}
		for _, name := range stopNames {
			reconciliationErrs = append(reconciliationErrs, stopAndQuarantineISO(ctx, steps, name, err))
		}
	} else {
		for _, name := range restartCandidates {
			if err := ctx.Err(); err != nil {
				reconciliationErrs = append(reconciliationErrs, stopAndQuarantineISO(ctx, steps, name, fmt.Errorf("restart verified ISO service %q: %w", name, err)))
				continue
			}
			if err := steps.RestartTrusted(ctx, name); err != nil {
				reconciliationErrs = append(reconciliationErrs, stopAndQuarantineISO(ctx, steps, name, fmt.Errorf("restart verified ISO service %q: %w", name, err)))
			}
		}
	}
	return errors.Join(reconciliationErrs...)
}

func (s *Server) currentISOAllocationNames() ([]string, error) {
	dv, err := s.cfg.DB.Get()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, service := range dv.Services().All() {
		if service.ISO().Valid() {
			names = append(names, service.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func (s *Server) failClosedISONetworks(ctx context.Context, cause error) error {
	if s == nil || s.cfg.DB == nil {
		return fmt.Errorf("fail-closed ISO startup requires a config database")
	}
	names, err := s.currentISOAllocationNames()
	if err != nil {
		return fmt.Errorf("load ISO records for fail-closed startup: %w", err)
	}
	steps := &isoConcreteReconcileSteps{server: s}
	var errs []error
	for _, name := range names {
		errs = append(errs, stopAndQuarantineISO(ctx, steps, name, cause))
	}
	return errors.Join(errs...)
}

//nolint:cyclop // Per-service reconciliation keeps policy, topology, runtime, and quarantine order explicit.
func reconcileISOServiceWith(ctx context.Context, steps isoReconcileSteps, service string, allocation *db.ISOAllocation) (bool, error) {
	phases := []struct {
		name string
		run  func(context.Context, string) error
	}{
		{name: "ensure policy", run: steps.EnsurePolicy},
		{name: "verify policy", run: steps.VerifyPolicy},
	}
	if allocation.Kind != string(iso.PayloadVM) {
		phases = append(phases,
			struct {
				name string
				run  func(context.Context, string) error
			}{name: "ensure topology", run: steps.EnsureTopology},
			struct {
				name string
				run  func(context.Context, string) error
			}{name: "verify topology", run: steps.VerifyTopology},
		)
	}
	for _, phase := range phases {
		if err := ctx.Err(); err != nil {
			return false, stopAndQuarantineISO(ctx, steps, service, fmt.Errorf("%s for %q: %w", phase.name, service, err))
		}
		if err := phase.run(ctx, service); err != nil {
			return false, stopAndQuarantineISO(ctx, steps, service, fmt.Errorf("%s for %q: %w", phase.name, service, err))
		}
	}
	if allocation.State != string(iso.StateReady) {
		if err := steps.VerifyStopped(ctx, service); err != nil {
			return false, stopAndQuarantineISO(ctx, steps, service, fmt.Errorf("verify stopped runtime for %q: %w", service, err))
		}
		return false, nil
	}
	runtimeState, err := steps.InspectRuntime(ctx, service)
	if err != nil {
		return false, stopAndQuarantineISO(ctx, steps, service, fmt.Errorf("inspect runtime for %q: %w", service, err))
	}
	switch runtimeState {
	case isoReconcileRuntimeRunning:
		if slices.Contains(allocation.DesiredModes, "ts") {
			if err := steps.VerifyTailscale(ctx, service); err != nil {
				return false, stopAndQuarantineISO(ctx, steps, service, fmt.Errorf("verify Tailscale for %q: %w", service, err))
			}
		}
		return false, nil
	case isoReconcileRuntimeAbsent:
		return true, nil
	default:
		cause := fmt.Errorf("inspect runtime for %q returned unknown state %q", service, runtimeState)
		return false, stopAndQuarantineISO(ctx, steps, service, cause)
	}
}

func stopAndQuarantineISO(ctx context.Context, steps isoReconcileSteps, service string, cause error) error {
	stopCtx, stopCancel := isoSecurityCleanupContext(ctx)
	stopErr := steps.StopUntrusted(stopCtx, service)
	stopCancel()
	quarantineCtx, quarantineCancel := isoSecurityCleanupContext(ctx)
	quarantineErr := steps.Quarantine(quarantineCtx, service, cause)
	quarantineCancel()
	return errors.Join(cause, stopErr, quarantineErr)
}

func isoSecurityCleanupContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(parent), isoSecurityCleanupTimeout)
}

func runISOReconcileGlobalPhase(ctx context.Context, name string, run func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if err := run(ctx); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

type isoRetirementSteps interface {
	StopProject(context.Context, string) error
	VerifyContainersAbsent(context.Context, string, map[string]db.ISOComponent) error
	VerifyDockerEndpointsAbsent(context.Context, string, map[string]db.ISOComponent) error
	VerifyDNetRecordsAbsent(context.Context, string, map[string]db.ISOComponent) error
	Reserve(context.Context, string) error
	Quarantine(context.Context, string, error) error
}

// finalizeISORetirementsWith implements stop-clean-reserve. Retired component
// ownership remains persisted until Docker containers, Docker endpoints, and
// dnet endpoint records are all proven absent.
//
//nolint:cyclop // Retirement proof steps stay linear so address reuse cannot skip a check.
func (s *Server) finalizeISORetirementsWith(ctx context.Context, service string, steps isoRetirementSteps) error {
	dv, err := s.cfg.DB.Get()
	if err != nil {
		return err
	}
	view, ok := dv.Services().GetOk(service)
	if !ok || !view.ISO().Valid() {
		return fmt.Errorf("service %q has no ISO allocation", service)
	}
	retired := view.ISO().AsStruct().RetiredComponents
	if len(retired) == 0 {
		return nil
	}
	stopCtx, stopCancel := isoSecurityCleanupContext(ctx)
	stopErr := steps.StopProject(stopCtx, service)
	stopCancel()
	if stopErr != nil {
		return quarantineISORetirement(ctx, steps, service, fmt.Errorf("stop ISO project before component retirement: %w", stopErr))
	}
	checks := []struct {
		name string
		run  func(context.Context, string, map[string]db.ISOComponent) error
	}{
		{name: "verify retired containers absent", run: steps.VerifyContainersAbsent},
		{name: "verify retired Docker endpoints absent", run: steps.VerifyDockerEndpointsAbsent},
		{name: "verify retired dnet records absent", run: steps.VerifyDNetRecordsAbsent},
	}
	for _, check := range checks {
		if err := ctx.Err(); err != nil {
			return quarantineISORetirement(ctx, steps, service, fmt.Errorf("%s: %w", check.name, err))
		}
		if err := check.run(ctx, service, retired); err != nil {
			return quarantineISORetirement(ctx, steps, service, fmt.Errorf("%s: %w", check.name, err))
		}
	}
	if _, _, err := s.cfg.DB.MutateService(service, func(_ *db.Data, record *db.Service) error {
		if record.ISO == nil {
			return fmt.Errorf("service %q lost its ISO allocation", service)
		}
		record.ISO.RetiredComponents = nil
		return nil
	}); err != nil {
		return quarantineISORetirement(ctx, steps, service, fmt.Errorf("clear verified retired component mappings: %w", err))
	}
	if err := steps.Reserve(ctx, service); err != nil {
		return quarantineISORetirement(ctx, steps, service, fmt.Errorf("re-reserve ISO components: %w", err))
	}
	return nil
}

func quarantineISORetirement(ctx context.Context, steps isoRetirementSteps, service string, cause error) error {
	quarantineCtx, cancel := isoSecurityCleanupContext(ctx)
	defer cancel()
	return errors.Join(cause, steps.Quarantine(quarantineCtx, service, cause))
}

type isoReplacementNetwork struct {
	Modes      []string
	SvcNetwork *db.SvcNetwork
	Macvlan    *db.MacvlanNetwork
	Tailscale  *db.TailscaleNetwork
	Artifacts  db.ArtifactStore
}

type isoTransitionSteps interface {
	PrepareReplacement(context.Context, string, []string) (isoReplacementNetwork, error)
	StopISO(context.Context, string) error
	CleanISO(context.Context, string) error
	VerifyISOAbsent(context.Context, string) error
	StartReplacement(context.Context, string, isoReplacementNetwork) error
}

type isoRemoveSteps interface {
	StopWorkload(context.Context, string) error
	StopTailscale(context.Context, string) error
	RemoveDockerEndpoints(context.Context, string) error
	CleanTopology(context.Context, string) error
	VerifyTopologyAbsent(context.Context, string) error
	VerifyDockerAbsent(context.Context, string) error
	VerifyDNetAbsent(context.Context, string) error
	RenderGlobalPolicy(context.Context, string) error
	VerifyGlobalPolicy(context.Context, string) error
	BeforeDelete(context.Context, string) error
}

type isoConcreteRemoveSteps struct {
	server     *Server
	service    *db.Service
	compose    *svc.DockerComposeService
	vm         bool
	spec       isoRuntimeNetworkSpec
	options    RemoveOptions
	report     *RemoveReport
	zfsDataset string
}

func (s *Server) newConcreteISORemoveSteps(service string, options RemoveOptions, report *RemoveReport, zfsDataset string) (isoRemoveSteps, error) {
	view, err := s.serviceView(service)
	if err != nil {
		return nil, err
	}
	record := view.AsStruct()
	isVM := record.ServiceType == db.ServiceTypeVM || record.ISO != nil && record.ISO.Kind == string(iso.PayloadVM)
	if record.ServiceType != db.ServiceTypeDockerCompose && !isVM {
		return nil, fmt.Errorf("ISO removal for service type %q is not implemented in the container lifecycle", record.ServiceType)
	}
	var compose *svc.DockerComposeService
	if !isVM {
		compose, err = dockerComposeServiceForISO(s, service)
		if err != nil {
			return nil, err
		}
	}
	spec, err := s.loadISORuntimeSpec(service)
	if err != nil {
		return nil, err
	}
	return &isoConcreteRemoveSteps{
		server: s, service: record, compose: compose, vm: isVM, spec: spec,
		options: options, report: report, zfsDataset: zfsDataset,
	}, nil
}

func (r *isoConcreteRemoveSteps) StopWorkload(ctx context.Context, _ string) error {
	if r.vm {
		return runISOReconcileVMSystemctl(ctx, "stop", r.service.Name)
	}
	return r.compose.StopProjectContainers(ctx)
}

func (r *isoConcreteRemoveSteps) StopTailscale(ctx context.Context, _ string) error {
	if r.vm {
		return nil
	}
	return stopAndVerifyISOAuxiliaryUnits(ctx, r.service)
}

func stopAndVerifyISOAuxiliaryUnits(ctx context.Context, service *db.Service) error {
	units := isoAuxiliaryUnits(service)
	if len(units) == 0 {
		return nil
	}
	args := append([]string{"stop"}, units...)
	if output, err := runISOSystemctlForRuntime(ctx, args...); err != nil {
		return fmt.Errorf("stop ISO auxiliary units: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return verifyISOAuxiliaryUnitsStopped(ctx, service)
}

func verifyISOAuxiliaryUnitsStopped(ctx context.Context, service *db.Service) error {
	for _, unit := range isoAuxiliaryUnits(service) {
		output, err := runISOSystemctlForRuntime(ctx, "show", "--property=ActiveState", "--value", unit)
		if err != nil {
			return fmt.Errorf("verify ISO auxiliary unit %s stopped: %w: %s", unit, err, strings.TrimSpace(string(output)))
		}
		state := strings.TrimSpace(string(output))
		if state != "inactive" && state != "failed" {
			return fmt.Errorf("ISO auxiliary unit %s remains %s", unit, state)
		}
	}
	return nil
}

func isoAuxiliaryUnits(service *db.Service) []string {
	var units []string
	generation := service.Generation
	if _, ok := service.Artifacts.Gen(db.ArtifactTSService, generation); ok {
		units = append(units, "yeet-"+service.Name+"-ts.service")
	}
	if _, ok := service.Artifacts.Gen(db.ArtifactNetNSService, generation); ok {
		units = append(units, "yeet-"+service.Name+"-ns.service")
	}
	return units
}

func (r *isoConcreteRemoveSteps) RemoveDockerEndpoints(ctx context.Context, _ string) error {
	if r.vm {
		return nil
	}
	return r.compose.StopProjectContainers(ctx)
}

func (r *isoConcreteRemoveSteps) CleanTopology(ctx context.Context, _ string) error {
	if r.vm {
		var networks []db.VMNetworkConfig
		if r.service.VM != nil {
			networks = r.service.VM.Networks
		}
		plan := vmNetworkPlanFromDB(r.service.Name, networks, r.service.ISO)
		if !vmNetworkMatchesISOAllocation(plan, r.service.ISO) {
			plan = newVMNetworkPlan(r.service.Name, []string{"iso"}, vmNetworkInputs{
				ISOHostIP: r.service.ISO.HostIP, ISOGuestIP: r.service.ISO.PeerIP,
				ISOLink: r.service.ISO.Link, ISOTap: r.service.ISO.Interface,
			})
		}
		runner := vmRemovalNetworkRunner
		if runner == nil {
			runner = execVMNetworkCommand
		}
		return plan.ExecuteCleanup(runner)
	}
	return removeISOTopologyForRuntime(ctx, r.spec.Topology)
}

func (r *isoConcreteRemoveSteps) VerifyTopologyAbsent(ctx context.Context, _ string) error {
	return verifyISOTopologyAbsentForRuntime(ctx, r.spec.Topology)
}

func (r *isoConcreteRemoveSteps) VerifyDockerAbsent(ctx context.Context, _ string) error {
	if r.vm {
		return nil
	}
	return errors.Join(r.compose.VerifyProjectAbsent(ctx), r.compose.VerifyDefaultNetworkAbsent(ctx))
}

func (r *isoConcreteRemoveSteps) VerifyDNetAbsent(_ context.Context, _ string) error {
	return verifyISOAllocationDNetAbsent(r.server, *r.service.ISO)
}

func verifyISOAllocationDNetAbsent(server *Server, allocation db.ISOAllocation) error {
	dv, err := server.cfg.DB.Get()
	if err != nil {
		return err
	}
	addresses := map[netip.Addr]bool{}
	for _, component := range allocation.Components {
		addresses[component.Address] = true
	}
	for _, component := range allocation.RetiredComponents {
		addresses[component.Address] = true
	}
	return verifyDNetAddressesAbsent(dv.AsStruct().DockerNetworks, addresses, "ISO address")
}

func verifyDNetAddressesAbsent(networks map[string]*db.DockerNetwork, addresses map[netip.Addr]bool, label string) error {
	for networkID, network := range networks {
		endpointID, address, found := dnetAddressOwner(network, addresses)
		if found {
			return fmt.Errorf("dnet network %q endpoint %q still owns %s %s", networkID, endpointID, label, address)
		}
	}
	return nil
}

func dnetAddressOwner(network *db.DockerNetwork, addresses map[netip.Addr]bool) (string, netip.Addr, bool) {
	if network == nil {
		return "", netip.Addr{}, false
	}
	for endpointID, endpoint := range network.Endpoints {
		if endpoint != nil && addresses[endpoint.IPv4.Addr()] {
			return endpointID, endpoint.IPv4.Addr(), true
		}
	}
	return "", netip.Addr{}, false
}

func (r *isoConcreteRemoveSteps) RenderGlobalPolicy(ctx context.Context, service string) error {
	rules, present, err := r.server.currentGlobalISOPolicy()
	if err != nil || !present {
		return err
	}
	return ensureISOPolicyForRuntime(ctx, rules)
}

func (r *isoConcreteRemoveSteps) VerifyGlobalPolicy(ctx context.Context, service string) error {
	rules, present, err := r.server.currentGlobalISOPolicy()
	if err != nil || !present {
		return err
	}
	return verifyISOPolicyForRuntime(ctx, rules)
}

func (r *isoConcreteRemoveSteps) BeforeDelete(_ context.Context, _ string) error {
	if err := r.cleanVMJailBeforeDelete(); err != nil {
		return err
	}
	if !r.options.CleanData {
		return nil
	}
	return r.server.destroyServiceRootZFS(r.zfsDataset)
}

func (r *isoConcreteRemoveSteps) cleanVMJailBeforeDelete() error {
	if !r.vm || r.service.VM == nil {
		return nil
	}
	rootFS := strings.TrimSpace(r.service.VM.Image.RootFS)
	if !filepath.IsAbs(rootFS) {
		return nil
	}
	plan := newVMJailCleanupPlan(r.service.Name, filepath.Join(filepath.Dir(rootFS), "firecracker"), vmJailerBaseForDataRoot(r.server.cfg.RootDir), []string{
		r.service.VM.Sockets.APISocketPath,
		r.service.VM.Sockets.VsockSocketPath,
	})
	if err := vmRemovalJailCleanup(plan); err != nil {
		return fmt.Errorf("clean up VM jail: %w", err)
	}
	return nil
}

func (s *Server) removeISOServiceWith(ctx context.Context, service string, steps isoRemoveSteps) error {
	return s.removeISOServiceWithOptions(ctx, service, false, steps)
}

func (s *Server) removeISOServiceWithOptions(ctx context.Context, service string, cleanData bool, steps isoRemoveSteps) error {
	return s.withISOOperationLock(ctx, func() error {
		if err := s.recordISORemovalIntent(service, cleanData); err != nil {
			return fmt.Errorf("record ISO removal intent: %w", err)
		}
		return s.removeISOServiceLockedWith(ctx, service, steps)
	})
}

//nolint:cyclop // Removal is a security-sensitive tombstone transaction with explicit phase order.
func (s *Server) removeISOServiceLockedWith(ctx context.Context, service string, steps isoRemoveSteps) error {
	dv, err := s.cfg.DB.Get()
	if err != nil {
		return fmt.Errorf("load ISO removal state: %w", err)
	}
	serviceView, ok := dv.Services().GetOk(service)
	if !ok || !serviceView.ISO().Valid() {
		return fmt.Errorf("service %q has no ISO allocation", service)
	}
	allocation := serviceView.ISO()
	cleanupVerified := allocation.RemoveRequested() && allocation.CleanupVerified()
	if !allocation.RemoveRequested() {
		if err := s.markISORemoveRequested(service); err != nil {
			return fmt.Errorf("record ISO removal intent: %w", err)
		}
	}
	if !cleanupVerified {
		cleanup := []struct {
			name string
			run  func(context.Context, string) error
		}{
			{name: "stop-workload", run: steps.StopWorkload},
			{name: "stop-tailscale", run: steps.StopTailscale},
			{name: "remove-docker-endpoints", run: steps.RemoveDockerEndpoints},
			{name: "clean-topology", run: steps.CleanTopology},
			{name: "verify-topology-absent", run: steps.VerifyTopologyAbsent},
			{name: "verify-docker-absent", run: steps.VerifyDockerAbsent},
			{name: "verify-dnet-absent", run: steps.VerifyDNetAbsent},
		}
		for _, step := range cleanup {
			if err := runISORemoveStep(ctx, service, step.run); err != nil {
				return s.retainISOTombstone(service, fmt.Errorf("%s: %w", step.name, err))
			}
		}
		if err := s.markISOCleanupVerified(service); err != nil {
			return s.retainISOTombstone(service, fmt.Errorf("record verified ISO cleanup: %w", err))
		}
	}
	policy := []struct {
		name string
		run  func(context.Context, string) error
	}{
		{name: "render-global-policy", run: steps.RenderGlobalPolicy},
		{name: "verify-global-policy", run: steps.VerifyGlobalPolicy},
	}
	for _, step := range policy {
		if err := runISORemoveStep(ctx, service, step.run); err != nil {
			return s.retainISOTombstone(service, fmt.Errorf("%s: %w", step.name, err))
		}
	}
	if err := runISORemoveStep(ctx, service, steps.BeforeDelete); err != nil {
		return s.retainISOTombstone(service, fmt.Errorf("before-delete: %w", err))
	}
	if err := s.removeServiceFromDB(service); err != nil {
		return s.retainISOTombstone(service, fmt.Errorf("delete verified ISO service: %w", err))
	}
	return nil
}

func runISORemoveStep(ctx context.Context, service string, run func(context.Context, string) error) error {
	stepCtx, cancel := isoSecurityCleanupContext(ctx)
	defer cancel()
	return run(stepCtx, service)
}

// transitionFromISOWith prepares the replacement without activating it, then
// releases ISO only in the same DB mutation that commits the verified new
// network. Cleanup uncertainty retains a tombstone and never starts the new
// mode.
func (s *Server) transitionFromISOWith(ctx context.Context, service string, desired []string, steps isoTransitionSteps) error {
	prepared, err := steps.PrepareReplacement(ctx, service, slices.Clone(desired))
	if err != nil {
		return fmt.Errorf("prepare replacement network for %q: %w", service, err)
	}
	if err := validateISOReplacementNetwork(desired, prepared); err != nil {
		return fmt.Errorf("validate prepared replacement network for %q: %w", service, err)
	}
	stopCtx, stopCancel := isoSecurityCleanupContext(ctx)
	stopErr := steps.StopISO(stopCtx, service)
	stopCancel()
	if stopErr != nil {
		return s.retainISOTransitionTombstone(service, fmt.Errorf("stop ISO service before transition: %w", stopErr))
	}
	cleanCtx, cleanCancel := isoSecurityCleanupContext(ctx)
	cleanErr := steps.CleanISO(cleanCtx, service)
	cleanCancel()
	if cleanErr != nil {
		return s.retainISOTransitionTombstone(service, fmt.Errorf("clean ISO service before transition: %w", cleanErr))
	}
	if err := ctx.Err(); err != nil {
		return s.retainISOTransitionTombstone(service, fmt.Errorf("verify ISO absence before transition: %w", err))
	}
	if err := steps.VerifyISOAbsent(ctx, service); err != nil {
		return s.retainISOTransitionTombstone(service, fmt.Errorf("verify ISO absence before transition: %w", err))
	}
	if err := s.commitReplacementNetwork(service, prepared); err != nil {
		return s.retainISOTransitionTombstone(service, fmt.Errorf("commit replacement network: %w", err))
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("start replacement network for %q: %w", service, err)
	}
	if err := steps.StartReplacement(ctx, service, prepared); err != nil {
		return fmt.Errorf("start replacement network for %q: %w", service, err)
	}
	return nil
}

func (s *Server) transitionFromISO(ctx context.Context, service string, desired []string, steps isoTransitionSteps) error {
	return s.withISOOperationLock(ctx, func() error {
		return s.transitionFromISOWith(ctx, service, desired, steps)
	})
}

func validateISOReplacementNetwork(desired []string, prepared isoReplacementNetwork) error {
	normalizedDesired, err := normalizedISOReplacementModes("requested", desired)
	if err != nil {
		return err
	}
	if _, err := normalizedISOReplacementModes("prepared", prepared.Modes); err != nil {
		return err
	}
	if !slices.Equal(prepared.Modes, normalizedDesired) {
		return fmt.Errorf("prepared replacement modes %v do not match requested modes %v", prepared.Modes, normalizedDesired)
	}
	if slices.Contains(prepared.Modes, "iso") {
		return fmt.Errorf("prepared replacement modes must not contain iso")
	}
	if err := validateISOReplacementModeState(prepared); err != nil {
		return err
	}
	return validateISOReplacementArtifacts(prepared.Artifacts)
}

func normalizedISOReplacementModes(label string, modes []string) ([]string, error) {
	normalized, err := iso.NormalizeModes(modes)
	if err != nil {
		return nil, fmt.Errorf("normalize %s replacement modes: %w", label, err)
	}
	if !slices.Equal(modes, normalized) {
		return nil, fmt.Errorf("%s replacement modes must be normalized: got %v, want %v", label, modes, normalized)
	}
	return normalized, nil
}

func validateISOReplacementModeState(prepared isoReplacementNetwork) error {
	if slices.Contains(prepared.Modes, "svc") != (prepared.SvcNetwork != nil) {
		return fmt.Errorf("svc mode and network state disagree")
	}
	if slices.Contains(prepared.Modes, "lan") != (prepared.Macvlan != nil) {
		return fmt.Errorf("lan mode and network state disagree")
	}
	if slices.Contains(prepared.Modes, "ts") != (prepared.Tailscale != nil) {
		return fmt.Errorf("ts mode and network state disagree")
	}
	return nil
}

func validateISOReplacementArtifacts(artifacts db.ArtifactStore) error {
	for name := range artifacts {
		if !isoNetworkArtifactNames[name] {
			return fmt.Errorf("prepared artifact %q is not network-owned", name)
		}
	}
	return nil
}

func (s *Server) retainISOTransitionTombstone(service string, cause error) error {
	return errors.Join(cause, s.markISOState(service, string(iso.StateTombstoned), cause))
}

func (s *Server) commitReplacementNetwork(service string, prepared isoReplacementNetwork) error {
	_, _, err := s.cfg.DB.MutateService(service, func(_ *db.Data, record *db.Service) error {
		if record.ISO == nil {
			return fmt.Errorf("service %q lost its ISO allocation before replacement commit", service)
		}
		record.SvcNetwork = cloneISOReplacementSvcNetwork(prepared.SvcNetwork)
		record.Macvlan = cloneISOReplacementMacvlan(prepared.Macvlan)
		record.TSNet = prepared.Tailscale.Clone()
		record.Artifacts = mergeISOReplacementNetworkArtifacts(record.Artifacts, prepared.Artifacts)
		record.ISO = nil
		return nil
	})
	return err
}

// Replacement preparation owns only generated network artifacts. Payload
// artifacts remain part of the service definition and survive a network-mode
// transition unchanged.
func mergeISOReplacementNetworkArtifacts(existing, replacement db.ArtifactStore) db.ArtifactStore {
	merged := cloneISOReplacementArtifacts(existing)
	for name := range isoNetworkArtifactNames {
		delete(merged, name)
	}
	if len(replacement) == 0 {
		return merged
	}
	if merged == nil {
		merged = db.ArtifactStore{}
	}
	for name, artifact := range replacement {
		merged[name] = artifact.Clone()
	}
	return merged
}

var isoNetworkArtifactNames = map[db.ArtifactName]bool{
	db.ArtifactDockerComposeNetwork: true,
	db.ArtifactNetNSService:         true,
	db.ArtifactNetNSEnv:             true,
	db.ArtifactNetNSResolv:          true,
	db.ArtifactTSService:            true,
	db.ArtifactTSEnv:                true,
	db.ArtifactTSBinary:             true,
	db.ArtifactTSConfig:             true,
}

func clearISOCloneState(service *db.Service) {
	if service == nil {
		return
	}
	service.ISO = nil
	for name := range isoNetworkArtifactNames {
		delete(service.Artifacts, name)
	}
}

func cloneISOReplacementSvcNetwork(network *db.SvcNetwork) *db.SvcNetwork {
	if network == nil {
		return nil
	}
	clone := *network
	return &clone
}

func cloneISOReplacementMacvlan(network *db.MacvlanNetwork) *db.MacvlanNetwork {
	if network == nil {
		return nil
	}
	clone := *network
	return &clone
}

func cloneISOReplacementArtifacts(artifacts db.ArtifactStore) db.ArtifactStore {
	if artifacts == nil {
		return nil
	}
	clone := make(db.ArtifactStore, len(artifacts))
	for name, artifact := range artifacts {
		clone[name] = artifact.Clone()
	}
	return clone
}

func reconcileISONetwork(ctx context.Context, service string, spec isoRuntimeNetworkSpec) error {
	policy, err := netns.RenderISOPolicy(spec.Backend, spec.Policy)
	if err != nil {
		return err
	}
	if err := ensureISOPolicyForRuntime(ctx, policy); err != nil {
		return err
	}
	if err := ensureISOTopologyForRuntime(ctx, spec.Topology); err != nil {
		return err
	}
	return verifyISORuntimeSiblings(ctx, service, spec.Topologies)
}

func verifyISORuntimeSiblings(ctx context.Context, service string, topologies []isoRuntimeTopology) error {
	for _, topology := range topologies {
		if topology.Service == service {
			continue
		}
		if err := verifyISOTopologyForRuntime(ctx, topology.Spec); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) failISORuntime(err error) error {
	_ = s.markISORuntimeConflict(err)
	return err
}

func (s *Server) markISOReady(service string) error {
	_, err := s.cfg.DB.MutateData(func(data *db.Data) error {
		if data.ISOPool == nil {
			return fmt.Errorf("ISO pool disappeared while ensuring %q", service)
		}
		allocation, ok := isoAllocationInData(data, service)
		if !ok {
			return fmt.Errorf("service %q has no ISO allocation", service)
		}
		if allocation.RemoveRequested || allocation.CleanupVerified {
			return fmt.Errorf("service %q ISO removal or cleanup is in progress", service)
		}
		switch iso.AllocationState(allocation.State) {
		case iso.StateRemoving, iso.StateTombstoned, iso.StateQuarantined:
			return fmt.Errorf("service %q ISO lifecycle state %q cannot become ready", service, allocation.State)
		}
		data.ISOPool.AggregateRouteState = "ready"
		data.ISOPool.LastConflict = ""
		allocation.State = string(iso.StateReady)
		allocation.LastError = ""
		return nil
	})
	return err
}

func (s *Server) markISOStoppedIfAllocated(service string) error {
	if s == nil || s.cfg.DB == nil {
		return nil
	}
	_, _, err := s.cfg.DB.MutateService(service, func(_ *db.Data, record *db.Service) error {
		if record.ISO == nil || record.ISO.RemoveRequested {
			return nil
		}
		record.ISO.State = string(iso.StateStopped)
		record.ISO.LastError = ""
		return nil
	})
	return err
}

func (s *Server) markISORuntimeConflict(cause error) error {
	_, err := s.cfg.DB.MutateData(func(data *db.Data) error {
		if data.ISOPool != nil {
			data.ISOPool.AggregateRouteState = "conflict"
			data.ISOPool.LastConflict = cause.Error()
		}
		for _, service := range data.Services {
			if service == nil || service.ISO == nil || service.ISO.RemoveRequested && service.ISO.CleanupVerified {
				continue
			}
			service.ISO.State = string(iso.StateQuarantined)
			service.ISO.LastError = cause.Error()
		}
		return nil
	})
	return err
}

// CleanISONetwork records cleanup intent, proves the routed namespace absent,
// then removes the endpoint from the global policy. It intentionally retains
// the allocation tombstone; later lifecycle reconciliation owns final release.
func (s *Server) CleanISONetwork(ctx context.Context, service string) error {
	if s == nil || s.cfg.DB == nil {
		return fmt.Errorf("ISO network requires a config DB")
	}
	return s.withISOOperationLock(ctx, func() error {
		return s.cleanISONetworkLocked(ctx, service)
	})
}

func (s *Server) cleanISONetworkLocked(ctx context.Context, service string) error {
	spec, err := s.loadISORuntimeSpec(service)
	if err != nil {
		_ = s.markISORuntimeConflict(err)
		return err
	}
	if spec.VM {
		err := fmt.Errorf("VM ISO topology cleanup must be verified by the VM network lifecycle")
		_ = s.markISOState(service, string(iso.StateQuarantined), err)
		return err
	}
	if err := s.markISORemoveRequested(service); err != nil {
		return err
	}
	if err := removeISOTopologyForRuntime(ctx, spec.Topology); err != nil {
		return s.retainISOTombstone(service, err)
	}
	if err := s.markISOCleanupVerified(service); err != nil {
		return s.retainISOTombstone(service, err)
	}
	return s.installISOPolicyAfterCleanup(ctx, service)
}

func (s *Server) loadISORuntimeSpec(service string) (isoRuntimeNetworkSpec, error) {
	dv, err := s.cfg.DB.Get()
	if err != nil {
		return isoRuntimeNetworkSpec{}, err
	}
	return s.isoRuntimeSpec(dv, service)
}

func (s *Server) installISOPolicyAfterCleanup(ctx context.Context, service string) error {
	spec, err := s.loadISORuntimeSpec(service)
	if err != nil {
		_ = s.markISORuntimeConflict(err)
		return s.retainISOTombstone(service, err)
	}
	policy, err := netns.RenderISOPolicy(spec.Backend, spec.Policy)
	if err == nil {
		err = ensureISOPolicyForRuntime(ctx, policy)
	}
	if err != nil {
		_ = s.markISORuntimeConflict(err)
		return s.retainISOTombstone(service, err)
	}
	return nil
}

func (s *Server) retainISOTombstone(service string, cause error) error {
	_ = s.markISOState(service, string(iso.StateTombstoned), cause)
	return cause
}

func (s *Server) markISORemoveRequested(service string) error {
	return s.recordISORemovalIntent(service, false)
}

func (s *Server) recordISORemovalIntent(service string, cleanData bool) error {
	_, _, err := s.cfg.DB.MutateService(service, func(_ *db.Data, record *db.Service) error {
		if record.ISO == nil {
			return fmt.Errorf("service %q has no ISO allocation", service)
		}
		if !record.ISO.RemoveRequested {
			record.ISO.RemoveRequested = true
			record.ISO.CleanupVerified = false
			record.ISO.State = string(iso.StateRemoving)
			record.ISO.LastError = ""
		}
		record.ISO.RemoveCleanData = record.ISO.RemoveCleanData || cleanData
		return nil
	})
	return err
}

func (s *Server) markISOCleanupVerified(service string) error {
	_, _, err := s.cfg.DB.MutateService(service, func(_ *db.Data, record *db.Service) error {
		if record.ISO == nil || !record.ISO.RemoveRequested {
			return fmt.Errorf("service %q ISO cleanup was not requested", service)
		}
		record.ISO.CleanupVerified = true
		record.ISO.State = string(iso.StateTombstoned)
		record.ISO.LastError = ""
		return nil
	})
	return err
}

func isoAllocationInData(data *db.Data, service string) (*db.ISOAllocation, bool) {
	if data == nil {
		return nil, false
	}
	record, ok := data.Services[service]
	if !ok || record == nil || record.ISO == nil {
		return nil, false
	}
	return record.ISO, true
}

// EnsureISONetwork is the local Catch command wrapper. It is deliberately not
// registered in the remote TTY/RPC command registry.
func EnsureISONetwork(ctx context.Context, cfg *Config, service string) error {
	if cfg == nil || cfg.DB == nil {
		return fmt.Errorf("ISO network requires a config DB")
	}
	server := &Server{cfg: *cfg}
	return server.EnsureISONetwork(ctx, service)
}

func EnsureISONetworkBoundary(ctx context.Context, cfg *Config, service string) error {
	if cfg == nil || cfg.DB == nil {
		return fmt.Errorf("ISO network requires a config DB")
	}
	server := &Server{cfg: *cfg}
	return server.EnsureISONetworkBoundary(ctx, service)
}

// CleanISONetwork is the matching local-only cleanup wrapper.
func CleanISONetwork(ctx context.Context, cfg *Config, service string) error {
	if cfg == nil || cfg.DB == nil {
		return fmt.Errorf("ISO network requires a config DB")
	}
	server := &Server{cfg: *cfg}
	return server.CleanISONetwork(ctx, service)
}
