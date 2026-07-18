// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/cmdutil"
	"github.com/yeetrun/yeet/pkg/codecutil"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/fileutil"
	"github.com/yeetrun/yeet/pkg/ftdetect"
	"github.com/yeetrun/yeet/pkg/iso"
	"github.com/yeetrun/yeet/pkg/netns"
	"github.com/yeetrun/yeet/pkg/svc"
	"gopkg.in/yaml.v3"
	"tailscale.com/net/netmon"
	"tailscale.com/tstime/rate"
	"tailscale.com/types/lazy"
	"tailscale.com/util/mak"
)

type FileInstallerCfg struct {
	InstallerCfg
	EnvFile bool

	Args                 []string
	Network              NetworkOpts
	StageOnly            bool
	NoBinary             bool
	Publish              []string
	PublishReset         bool
	SnapshotPolicyChange bool
	SnapshotPolicy       *db.SnapshotPolicy
	snapshotPolicyFlags  *cli.ServiceSetFlags
	// PayloadName preserves the original filename for type detection.
	PayloadName string

	// NewCmd, if set, will be used to create a new exec.Cmd.
	NewCmd func(name string, arg ...string) *exec.Cmd
}

type TailscaleOpts struct {
	Version  string
	ExitNode string
	Tags     []string
	AuthKey  string
}

type MacvlanOpts struct {
	Mac    string
	Parent string
	VLAN   int
}

type NetworkOpts struct {
	Interfaces string
	Tailscale  TailscaleOpts
	Macvlan    MacvlanOpts
	Modes      []string
	ISO        bool
}

type FileInstaller struct {
	s   *Server
	cfg FileInstallerCfg
	ch  chan struct{}

	existingService            db.ServiceView
	installedGeneration        int
	readInstalledGeneration    func() (int, error)
	installGenerationIfCurrent func(*Installer, int, int) error
	svcNet                     *db.SvcNetwork
	macvlan                    *db.MacvlanNetwork
	tsNet                      *db.TailscaleNetwork
	isoAllocation              *db.ISOAllocation
	tsAuthKey                  string
	artifacts                  map[db.ArtifactName]string
	lazyNetwork                lazy.GValue[*networkConfig]

	File     *os.File
	received atomic.Int64
	rateVal  rate.Value

	err    error
	closed bool

	ver string // memoized version number

	failed bool

	tmpDir  string
	tmpPath string

	serviceRoot    string
	serviceRootZFS string

	transitionHandled bool
	transitionFromISO func(context.Context, string, []string, isoTransitionSteps) error
}

type isoComposeInstallSteps interface {
	ResolveBase(context.Context) error
	AdmitBase(context.Context) error
	Reserve(context.Context) error
	RenderOverlay(context.Context) error
	ResolveMerged(context.Context) error
	AdmitMerged(context.Context) error
	InstallDNS(context.Context) error
	EnsurePolicy(context.Context) error
	VerifyPolicy(context.Context) error
	EnsureTopology(context.Context) error
	VerifyTopology(context.Context) error
	InstallTailscale(context.Context, *FileInstaller) error
	Pull(context.Context) error
	Build(context.Context) error
	AttachNetwork(context.Context) error
	StartAux(context.Context) error
	ComposeUp(context.Context) error
	InspectRuntime(context.Context) error
	MarkReady(context.Context) error
	ComposeDownRemoveOrphans(context.Context) error
	Quarantine(context.Context, error) error
}

type isoComposeResolveFunc func(context.Context, svc.ComposeResolveOptions) ([]byte, error)

// prepareISOCompose performs both canonical admission passes before the
// installer is allowed to pull, create a Docker network, start an auxiliary
// unit, or execute a container. Reservation is deliberately between the two
// passes because the merged overlay is derived only from persisted state.
//
//nolint:cyclop // Admission and quarantine ordering stays explicit at this security boundary.
func (i *FileInstaller) prepareISOCompose(ctx context.Context, resolve isoComposeResolveFunc) (ISOComposeModel, error) {
	if i == nil || i.s == nil || i.s.cfg.DB == nil {
		return ISOComposeModel{}, fmt.Errorf("ISO Compose preparation requires a config database")
	}
	if resolve == nil {
		resolve = svc.ResolveComposeJSON
	}
	composePath, err := i.isoBaseComposePath()
	if err != nil {
		return ISOComposeModel{}, err
	}
	projectName := svc.ComposeProjectName(i.cfg.ServiceName)
	resolveOpts := svc.ComposeResolveOptions{
		ProjectName: projectName,
		ProjectDir:  i.serviceDataDir(),
		Files:       []string{composePath},
	}
	if i.cfg.NewCmd != nil {
		resolveOpts.NewCmd = func(_ context.Context, name string, args ...string) *exec.Cmd {
			return i.cfg.NewCmd(name, args...)
		}
	}
	baseJSON, err := resolve(ctx, resolveOpts)
	if err != nil {
		return ISOComposeModel{}, fmt.Errorf("resolve base ISO Compose model: %w", err)
	}
	base, err := AdmitISOCompose(baseJSON, ISOComposeAdmissionOptions{
		ServiceRoot:   i.effectiveServiceRoot(),
		ProjectName:   projectName,
		MaxComponents: iso.MaxComponents,
	})
	if err != nil {
		return ISOComposeModel{}, fmt.Errorf("admit base ISO Compose model: %w", err)
	}
	allocation, err := i.s.reserveISOAllocation(ctx, i.cfg.ServiceName, isoReservationRequest{
		Kind:       iso.PayloadCompose,
		Modes:      slices.Clone(i.cfg.Network.Modes),
		Components: slices.Clone(base.Components),
	})
	if err != nil {
		return ISOComposeModel{}, fmt.Errorf("reserve ISO Compose allocation: %w", err)
	}
	i.isoAllocation = allocation.Clone()
	failReserved := func(cause error) (ISOComposeModel, error) {
		cleanupErr := stopAndQuarantineISO(ctx, &isoConcreteReconcileSteps{server: i.s}, i.cfg.ServiceName, cause)
		return ISOComposeModel{}, errors.Join(cause, cleanupErr)
	}
	overlay, err := renderISOComposeOverlay(allocation, base)
	if err != nil {
		return failReserved(fmt.Errorf("render persisted ISO Compose overlay: %w", err))
	}
	overlayPath, err := i.stageISOComposeOverlay(overlay)
	if err != nil {
		return failReserved(err)
	}
	resolveOpts.Files = []string{composePath, overlayPath}
	mergedJSON, err := resolve(ctx, resolveOpts)
	if err != nil {
		return failReserved(fmt.Errorf("resolve merged ISO Compose model: %w", err))
	}
	merged, err := AdmitISOCompose(mergedJSON, ISOComposeAdmissionOptions{
		ServiceRoot:       i.effectiveServiceRoot(),
		ProjectName:       projectName,
		MaxComponents:     iso.MaxComponents,
		RequireISOOverlay: allocation,
	})
	if err != nil {
		return failReserved(fmt.Errorf("admit merged ISO Compose model: %w", err))
	}
	if !slices.Equal(base.Components, merged.Components) {
		return failReserved(fmt.Errorf("ISO overlay changed Compose components: base %v, merged %v", base.Components, merged.Components))
	}
	if err := i.stageISONetworkGate(); err != nil {
		return failReserved(err)
	}
	return merged, nil
}

func (i *FileInstaller) isoBaseComposePath() (string, error) {
	if path := i.artifacts[db.ArtifactDockerComposeFile]; strings.TrimSpace(path) != "" {
		return path, nil
	}
	if i.existingService.Valid() {
		artifacts := i.existingService.AsStruct().Artifacts
		if path, ok := artifacts.Latest(db.ArtifactDockerComposeFile); ok {
			return path, nil
		}
	}
	return "", fmt.Errorf("ISO Compose base file is not staged")
}

func (i *FileInstaller) stageISOComposeOverlay(content string) (string, error) {
	path := filepath.Join(i.serviceBinDir(), fileutil.ApplyVersion("compose.network"))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write ISO Compose overlay: %w", err)
	}
	mak.Set(&i.artifacts, db.ArtifactDockerComposeNetwork, path)
	return path, nil
}

func (i *FileInstaller) stageISONetworkGate() error {
	catchBin, err := catchExecutablePath()
	if err != nil {
		return fmt.Errorf("resolve catch binary for ISO network gate: %w", err)
	}
	unit, err := newISONetworkGateUnit(catchBin, i.s.cfg.RootDir, i.cfg.ServiceName)
	if err != nil {
		return err
	}
	artifacts, err := unit.WriteOutUnitFiles(i.serviceBinDir())
	if err != nil {
		return fmt.Errorf("write ISO network gate unit: %w", err)
	}
	path := artifacts[db.ArtifactSystemdUnit]
	if path == "" {
		return fmt.Errorf("ISO network gate did not render a systemd unit")
	}
	mak.Set(&i.artifacts, db.ArtifactNetNSService, path)
	return nil
}

// installISOCompose enforces the security-sensitive first-start ordering. The
// concrete lifecycle adapter is wired by the installer transaction; the
// explicit step interface keeps every host-side mutation failure-injectable.
//
//nolint:cyclop // Phase ordering stays linear so every fail-closed transition is visible.
func (i *FileInstaller) installISOCompose(ctx context.Context, steps isoComposeInstallSteps) error {
	if i == nil || i.s == nil || i.s.cfg.DB == nil {
		return fmt.Errorf("ISO Compose install requires a config database")
	}
	type phase struct {
		name string
		run  func(context.Context) error
	}
	phases := []phase{
		{name: "resolve-base", run: steps.ResolveBase},
		{name: "admit-base", run: steps.AdmitBase},
		{name: "reserve", run: steps.Reserve},
	}
	for _, current := range phases {
		if err := runISOInstallPhase(ctx, current.name, current.run); err != nil {
			return quarantineISOInstallFailure(ctx, steps, err)
		}
	}
	allocation, err := i.persistedISOAllocation()
	if err != nil {
		return quarantineISOInstallFailure(ctx, steps, err)
	}
	i.isoAllocation = allocation
	remaining := []phase{
		{name: "render-overlay", run: steps.RenderOverlay},
		{name: "resolve-merged", run: steps.ResolveMerged},
		{name: "admit-merged", run: steps.AdmitMerged},
		{name: "install-dns", run: steps.InstallDNS},
		{name: "ensure-policy", run: steps.EnsurePolicy},
		{name: "verify-policy", run: steps.VerifyPolicy},
		{name: "ensure-topology", run: steps.EnsureTopology},
		{name: "verify-topology", run: steps.VerifyTopology},
		{name: "install-tailscale", run: func(ctx context.Context) error { return steps.InstallTailscale(ctx, i) }},
		{name: "pull", run: steps.Pull},
		{name: "build", run: steps.Build},
		{name: "attach-network", run: steps.AttachNetwork},
		{name: "start-aux", run: steps.StartAux},
		{name: "compose-up", run: steps.ComposeUp},
	}
	for _, current := range remaining {
		if err := runISOInstallPhase(ctx, current.name, current.run); err != nil {
			return quarantineISOInstallFailure(ctx, steps, err)
		}
	}
	if err := runISOInstallPhase(ctx, "inspect-runtime", steps.InspectRuntime); err != nil {
		return quarantineISOInstallFailure(ctx, steps, err)
	}
	if err := runISOInstallPhase(ctx, "mark-ready", steps.MarkReady); err != nil {
		return quarantineISOInstallFailure(ctx, steps, err)
	}
	return nil
}

func runISOInstallPhase(ctx context.Context, name string, run func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if err := run(ctx); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func quarantineISOInstallFailure(ctx context.Context, steps isoComposeInstallSteps, cause error) error {
	downCtx, downCancel := isoSecurityCleanupContext(ctx)
	cleanupErr := steps.ComposeDownRemoveOrphans(downCtx)
	downCancel()
	quarantineCtx, quarantineCancel := isoSecurityCleanupContext(ctx)
	quarantineErr := steps.Quarantine(quarantineCtx, cause)
	quarantineCancel()
	return errors.Join(cause, cleanupErr, quarantineErr)
}

func (i *FileInstaller) persistedISOAllocation() (*db.ISOAllocation, error) {
	dv, err := i.s.cfg.DB.Get()
	if err != nil {
		return nil, fmt.Errorf("load persisted ISO allocation for %q: %w", i.cfg.ServiceName, err)
	}
	service, ok := dv.Services().GetOk(i.cfg.ServiceName)
	if !ok || !service.ISO().Valid() {
		return nil, fmt.Errorf("service %q has no persisted ISO allocation", i.cfg.ServiceName)
	}
	allocation := service.ISO().AsStruct()
	if allocation == nil {
		return nil, fmt.Errorf("service %q has no persisted ISO allocation", i.cfg.ServiceName)
	}
	return allocation, nil
}

func (i *FileInstaller) WriteAt(p []byte, offset int64) (n int, err error) {
	if i.File == nil {
		return 0, fmt.Errorf("no temporary file")
	}
	i.received.Add(int64(len(p)))
	i.rateVal.Add(float64(len(p)))
	return i.File.WriteAt(p, offset)
}

func (i *FileInstaller) Write(p []byte) (n int, err error) {
	if i.File == nil {
		return 0, fmt.Errorf("no temporary file")
	}
	i.received.Add(int64(len(p)))
	i.rateVal.Add(float64(len(p)))
	return i.File.Write(p)
}

func (i *FileInstaller) Wait() error {
	<-i.ch
	return nil
}

func (i *FileInstaller) Received() float64 {
	return float64(i.received.Load())
}

func (i *FileInstaller) Rate() float64 {
	return i.rateVal.Rate()
}

func First[T1, T2 any](t1 T1, _ T2) T1 {
	return t1
}

var reservedServiceNames = map[string]struct{}{
	string(db.ArtifactTSBinary): {},
}

func NewFileInstaller(s *Server, cfg FileInstallerCfg) (*FileInstaller, error) {
	if _, ok := reservedServiceNames[cfg.ServiceName]; ok {
		return nil, fmt.Errorf("%s is a reserved service name", cfg.ServiceName)
	}
	existingService := First(s.serviceView(cfg.ServiceName))
	resolvedRoot, err := s.prepareServiceRootForInstall(cfg.ServiceName, cfg.ServiceRoot, cfg.ServiceRootZFS)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare service root: %w", err)
	}
	cfg.ServiceRoot = resolvedRoot.Root
	printServiceRootWarnings(cfg, resolvedRoot.Warnings)
	i := &FileInstaller{
		s:                          s,
		cfg:                        cfg,
		ch:                         make(chan struct{}),
		installGenerationIfCurrent: (*Installer).InstallGenIfCurrent,
		readInstalledGeneration: func() (int, error) {
			sv, err := s.serviceView(cfg.ServiceName)
			if err != nil {
				return 0, err
			}
			return sv.Generation(), nil
		},
		rateVal: rate.Value{
			HalfLife: 250 * time.Millisecond,
		},
		existingService: existingService,
		serviceRoot:     resolvedRoot.Root,
		serviceRootZFS:  resolvedRoot.Dataset,
	}
	if i.cfg.NewCmd == nil {
		i.cfg.NewCmd = cmdutil.NewStdCmd
	}
	if err := ensureDirsForRoot(resolvedRoot.Root, cfg.User); err != nil {
		return nil, fmt.Errorf("failed to ensure directories: %w", err)
	}
	if err := i.initTempFile(); err != nil {
		return nil, err
	}
	// Create temporary file.
	file, err := os.OpenFile(i.tempFilePath(), os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		i.cleanupTemp()
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	i.File = file
	return i, nil
}

func printServiceRootWarnings(cfg FileInstallerCfg, warnings []string) {
	for _, warning := range warnings {
		warning = strings.TrimSpace(warning)
		if warning == "" {
			continue
		}
		if cfg.ClientOut != nil {
			_, _ = fmt.Fprintf(cfg.ClientOut, "warning: %s\n", warning)
			continue
		}
		if cfg.Printer != nil {
			cfg.Printer("warning: %s", warning)
		}
	}
}

func (i *FileInstaller) serviceBinDir() string {
	return serviceBinDirForRoot(i.effectiveServiceRoot())
}

func (i *FileInstaller) serviceRunDir() string {
	return serviceRunDirForRoot(i.effectiveServiceRoot())
}

func (i *FileInstaller) serviceDataDir() string {
	return serviceDataDirForRoot(i.effectiveServiceRoot())
}

func (i *FileInstaller) serviceEnvDir() string {
	return serviceEnvDirForRoot(i.effectiveServiceRoot())
}

func (i *FileInstaller) effectiveServiceRoot() string {
	if i.serviceRoot != "" {
		return i.serviceRoot
	}
	if i.s == nil {
		return ""
	}
	return i.s.defaultServiceRootDir(i.cfg.ServiceName)
}

func (i *FileInstaller) printf(format string, args ...interface{}) {
	if i.cfg.Printer != nil {
		i.cfg.Printer(format, args...)
	}
}

func closeAndLog(c io.Closer, name string) {
	if err := c.Close(); err != nil {
		log.Printf("failed to close %s: %v", name, err)
	}
}

func removeFileIfExists(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("failed to remove %s: %v", path, err)
	}
}

type networkConfig struct {
	NetNS      string
	Deps       []string
	ResolvConf string
}

func hexStr(n int) string {
	bytes := make([]byte, n)
	for i := range bytes {
		bytes[i] = byte(rand.N(256))
	}
	return hex.EncodeToString(bytes)
}

var hostDefaultRouteInterfaceFn = hostDefaultRouteInterface

func hostDefaultRouteInterface() (string, error) {
	if runtime.GOOS == "linux" {
		f, err := os.Open("/proc/1/net/route")
		if err == nil {
			defer closeAndLog(f, "/proc/1/net/route")
			iface, err := hostDefaultRouteInterfaceFromProcRoute(f)
			if err == nil {
				return iface, nil
			}
			log.Printf("failed to parse /proc/1/net/route, falling back to netmon: %v", err)
		} else if !os.IsNotExist(err) {
			log.Printf("failed to open /proc/1/net/route, falling back to netmon: %v", err)
		}
	}
	return netmon.DefaultRouteInterface()
}

func hostDefaultRouteInterfaceFromProcRoute(r io.Reader) (string, error) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || fields[0] == "Iface" {
			continue
		}
		if len(fields) < 2 {
			continue
		}
		if fields[1] != "00000000" {
			continue
		}
		iface := strings.TrimSpace(fields[0])
		if iface == "" {
			return "", fmt.Errorf("default route interface is empty")
		}
		return iface, nil
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("default route interface not found")
}

func (i *FileInstaller) parseNetwork() error {
	nets := strings.Split(i.cfg.Network.Interfaces, ",")
	if len(nets) == 0 {
		return fmt.Errorf("invalid network: %q", i.cfg.Network.Interfaces)
	}
	dv, err := i.s.cfg.DB.Get()
	if err != nil {
		return fmt.Errorf("failed to get db view: %w", err)
	}
	for _, net := range nets {
		if err := i.parseNetworkPart(net, dv); err != nil {
			return err
		}
	}
	return nil
}

func parseNetworkForPayload(opts NetworkOpts, payload iso.PayloadKind, published bool) (NetworkOpts, error) {
	modes, err := iso.NormalizeModes(strings.Split(opts.Interfaces, ","))
	if err != nil {
		return NetworkOpts{}, err
	}
	if err := iso.ValidateNetwork(iso.NetworkRequest{Payload: payload, Modes: modes, Published: published}); err != nil {
		return NetworkOpts{}, err
	}
	opts.Interfaces = strings.Join(modes, ",")
	opts.Modes = modes
	opts.ISO = slices.Contains(modes, "iso")
	return opts, nil
}

func (i *FileInstaller) parseNetworkPart(net string, dv db.DataView) error {
	switch net {
	case "ts":
		i.tsNet, i.tsAuthKey = tailscaleNetworkFromOpts(i.cfg.Network.Tailscale)
	case "svc":
		svcNet, err := svcNetworkFromData(dv)
		if err != nil {
			return err
		}
		i.svcNet = svcNet
	case "lan":
		macvlan, err := macvlanNetworkFromOpts(i.cfg.Network.Macvlan)
		if err != nil {
			return err
		}
		if existing, ok := reusableExistingMacvlan(dv, i.cfg.ServiceName, macvlan, i.cfg.Network.Macvlan); ok {
			macvlan = existing
		}
		i.macvlan = macvlan
	case "iso":
		i.cfg.Network.ISO = true
	default:
		return fmt.Errorf("unknown network: %q", net)
	}
	return nil
}

func tailscaleNetworkFromOpts(opts TailscaleOpts) (*db.TailscaleNetwork, string) {
	tsNet := &db.TailscaleNetwork{
		Interface: "yts-" + hexStr(4),
		Version:   "1.77.33",
	}
	if opts.Version != "" {
		tsNet.Version = opts.Version
	}
	if opts.Tags != nil {
		tsNet.Tags = opts.Tags
	}
	if opts.ExitNode != "" {
		tsNet.ExitNode = opts.ExitNode
	}
	return tsNet, opts.AuthKey
}

func svcNetworkFromData(dv db.DataView) (*db.SvcNetwork, error) {
	ip, err := unassignedIP(dv)
	if err != nil {
		return nil, fmt.Errorf("failed to get unassigned IP: %v", err)
	}
	return &db.SvcNetwork{IPv4: ip}, nil
}

func macvlanNetworkFromOpts(opts MacvlanOpts) (*db.MacvlanNetwork, error) {
	iface, err := hostDefaultRouteInterfaceFn()
	if err != nil {
		return nil, fmt.Errorf("failed to get default route interface: %v", err)
	}
	log.Printf("default route interface: %v", iface)
	macvlan := &db.MacvlanNetwork{
		Interface: "ymv-" + hexStr(4),
		Parent:    iface,
		Mac:       randomMAC(),
	}
	if opts.Parent != "" {
		macvlan.Parent = opts.Parent
	}
	if opts.VLAN != 0 {
		macvlan.VLAN = opts.VLAN
	}
	if opts.Mac != "" {
		macvlan.Mac = opts.Mac
	}
	return macvlan, nil
}

func reusableExistingMacvlan(dv db.DataView, serviceName string, desired *db.MacvlanNetwork, opts MacvlanOpts) (*db.MacvlanNetwork, bool) {
	if desired == nil {
		return nil, false
	}
	sv, ok := dv.Services().GetOk(serviceName)
	if !ok {
		return nil, false
	}
	existing, ok := sv.Macvlan().GetOk()
	if !ok {
		return nil, false
	}
	if existing.Interface == "" || existing.Mac == "" {
		return nil, false
	}
	if existing.Parent != desired.Parent || existing.VLAN != desired.VLAN {
		return nil, false
	}
	if opts.Mac != "" && !strings.EqualFold(existing.Mac, opts.Mac) {
		return nil, false
	}
	return &existing, true
}

const tailscaledResolvConf = `nameserver 100.100.100.100` + "\n"

func (i *FileInstaller) configureNetwork() (*networkConfig, error) {
	return i.lazyNetwork.GetErr(func() (*networkConfig, error) {
		return i.configureNetworkOnce()
	})
}

func (i *FileInstaller) configureNetworkOnce() (*networkConfig, error) {
	if !networkInterfacesEnabled(i.cfg.Network.Interfaces) {
		return nil, nil
	}
	env, runTSInNetNS, tsTapMode, err := i.prepareNetworkConfig()
	if err != nil {
		return nil, err
	}
	if i.svcNet != nil {
		if err := checkSvcSubnetAvailableFn(); err != nil {
			return nil, err
		}
	}
	deps, err := i.installNetworkConfig(&env, runTSInNetNS, tsTapMode)
	if err != nil {
		return nil, err
	}
	log.Printf("artifacts: %v", i.artifacts)
	return &networkConfig{
		NetNS:      env.NetNS(),
		Deps:       deps,
		ResolvConf: runtimeNetNSResolvConf(env.NetNS()),
	}, nil
}

func (i *FileInstaller) prepareNetworkConfig() (netns.Service, string, bool, error) {
	if err := i.parseNetwork(); err != nil {
		return netns.Service{}, "", false, fmt.Errorf("failed to parse network: %v", err)
	}
	env := i.netNSServiceEnv()
	runTSInNetNS, _, tsTapMode, err := i.tailscaleNetNSMode(&env)
	return env, runTSInNetNS, tsTapMode, err
}

func (i *FileInstaller) installNetworkConfig(env *netns.Service, runTSInNetNS string, tsTapMode bool) ([]string, error) {
	if err := i.writeBaseNetworkConfig(env); err != nil {
		return nil, err
	}
	deps, err := i.installTailscaleDependency(*env, runTSInNetNS, tsTapMode)
	if err != nil {
		return nil, err
	}
	if err := i.writeDockerComposeNetwork(*env); err != nil {
		return nil, err
	}
	return deps, nil
}

func (i *FileInstaller) writeBaseNetworkConfig(env *netns.Service) error {
	_, tailscaleResolvConf, _, err := i.tailscaleNetNSMode(env)
	if err != nil {
		return err
	}
	if resolvConf := netNSResolvConfFor(env, tailscaleResolvConf); resolvConf != "" {
		if err := i.writeNetNSResolvConf(env, resolvConf); err != nil {
			return err
		}
	}
	return i.writeServiceNetNSFiles(*env)
}

func (i *FileInstaller) installTailscaleDependency(env netns.Service, runTSInNetNS string, tsTapMode bool) ([]string, error) {
	deps := []string{env.ServiceUnit()}
	if i.tsNet == nil {
		return deps, nil
	}
	if err := i.installTailscaleForNetNS(env, runTSInNetNS, tsTapMode); err != nil {
		return nil, err
	}
	return append(deps, "yeet-"+i.cfg.ServiceName+"-ts.service"), nil
}

func networkInterfacesEnabled(interfaces string) bool {
	return interfaces != "" && interfaces != "host"
}

func (i *FileInstaller) netNSServiceEnv() netns.Service {
	env := netns.Service{ServiceName: i.cfg.ServiceName}
	applySvcNetwork(&env, i.svcNet)
	applyMacvlanNetwork(&env, i.macvlan)
	return env
}

func applySvcNetwork(env *netns.Service, svcNet *db.SvcNetwork) {
	if svcNet == nil {
		return
	}
	env.ServiceIP = netip.PrefixFrom(svcNet.IPv4, svcNet.IPv4.BitLen())
	env.Range = netip.MustParsePrefix(netns.ServiceSubnetCIDR)
	env.HostIP = netip.MustParseAddr(netns.ServiceHostIP)
	env.YeetIP = netip.MustParseAddr(netns.ServiceGatewayIP)
}

func applyMacvlanNetwork(env *netns.Service, macvlan *db.MacvlanNetwork) {
	if macvlan == nil {
		return
	}
	env.MacvlanParent = macvlan.Parent
	env.MacvlanMac = macvlan.Mac
	env.MacvlanInterface = macvlan.Interface
	if macvlan.VLAN != 0 {
		env.MacvlanVLAN = strconv.Itoa(macvlan.VLAN)
	}
}

func (i *FileInstaller) tailscaleNetNSMode(env *netns.Service) (runTSInNetNS string, netnsResolvConf string, tapMode bool, err error) {
	if i.tsNet == nil {
		return "", "", false, nil
	}
	if i.isoAllocation != nil {
		if exitNode := strings.TrimSpace(i.tsNet.ExitNode); exitNode != "" {
			return "", "", false, fmt.Errorf("ISO Tailscale does not support exit node %q", exitNode)
		}
		if err := validateISOTailscaleAllocation(i.cfg.ServiceName, i.isoAllocation); err != nil {
			return "", "", false, err
		}
		i.tsNet.Interface = isoTailscaleInterface
		return i.isoAllocation.NetNS, "", false, nil
	}
	tapMode = i.svcNet == nil && i.macvlan == nil
	if tapMode {
		env.TailscaleTAPInterface = i.tsNet.Interface
		return "", tailscaledResolvConf, true, nil
	}
	return env.NetNS(), "", false, nil
}

func validateISOTailscaleAllocation(service string, allocation *db.ISOAllocation) error {
	kind := iso.PayloadKind(allocation.Kind)
	if kind != iso.PayloadCompose && kind != iso.PayloadContainer {
		return fmt.Errorf("ISO Tailscale requires a non-VM container allocation, got %q", allocation.Kind)
	}
	if !slices.Equal(allocation.DesiredModes, []string{"iso", "ts"}) {
		return fmt.Errorf("ISO Tailscale requires normalized persisted modes [iso ts], got %v", allocation.DesiredModes)
	}
	want := isoRouterNamespace(service)
	if allocation.NetNS != want {
		return fmt.Errorf("persisted ISO router namespace %q does not belong to service %q (want %q)", allocation.NetNS, service, want)
	}
	return nil
}

func (i *FileInstaller) writeNetNSResolvConf(env *netns.Service, resolvConf string) error {
	fp := filepath.Join(i.serviceBinDir(), fileutil.ApplyVersion("resolv.conf"))
	if err := os.WriteFile(fp, []byte(resolvConf), 0644); err != nil {
		return fmt.Errorf("failed to write resolv.conf: %v", err)
	}
	mak.Set(&i.artifacts, db.ArtifactNetNSResolv, fp)
	env.ResolvConf = fp
	return nil
}

func netNSResolvConfFor(env *netns.Service, tailscaleResolvConf string) string {
	if tailscaleResolvConf != "" {
		return tailscaleResolvConf
	}
	if env != nil && env.ServiceIP.IsValid() {
		return defaultSvcNetNSResolvConf()
	}
	return ""
}

func defaultSvcNetNSResolvConf() string {
	if dns := os.Getenv("DEFAULT_NS"); dns != "" {
		return buildNetNSResolvConf(dns, os.Getenv("DEFAULT_SEARCH_DOMAINS"))
	}
	searchDomains := os.Getenv("DEFAULT_SEARCH_DOMAINS")
	if searchDomains == "" {
		searchDomains = strings.TrimSuffix(yeetDNSDomain, ".")
	}
	return buildNetNSResolvConf(yeetDNSHostIP, searchDomains)
}

func buildNetNSResolvConf(dns, searchDomains string) string {
	resolvConf := fmt.Sprintf("nameserver %s\n", dns)
	if searchDomains != "" {
		resolvConf += fmt.Sprintf("search %s\n", searchDomains)
	}
	return resolvConf
}

func runtimeNetNSResolvConf(netNS string) string {
	netNS = strings.TrimSpace(netNS)
	if netNS == "" {
		return ""
	}
	return fmt.Sprintf("/etc/netns/%s/resolv.conf", netNS)
}

func (i *FileInstaller) writeServiceNetNSFiles(env netns.Service) error {
	files, err := netns.WriteServiceNetNS(
		i.serviceBinDir(),
		i.serviceRunDir(),
		env,
	)
	if err != nil {
		return fmt.Errorf("failed to write netns: %v", err)
	}
	i.setArtifacts(files)
	return nil
}

func (i *FileInstaller) installTailscaleForNetNS(_ netns.Service, runTSInNetNS string, tsTapMode bool) error {
	rc := ""
	if !tsTapMode && strings.TrimSpace(runTSInNetNS) != "" {
		rc = runtimeNetNSResolvConf(runTSInNetNS)
	}
	files, err := i.s.installTSAtRoot(i.effectiveServiceRoot(), i.cfg.ServiceName, runTSInNetNS, i.tsNet, i.tsAuthKey, rc)
	if err != nil {
		return fmt.Errorf("failed to install tailscale: %v", err)
	}
	i.setArtifacts(files)
	return nil
}

func (i *FileInstaller) writeDockerComposeNetwork(env netns.Service) error {
	if networkRequestsISO(i.cfg.Network) {
		return nil
	}
	services, err := i.composeDNSOverlayServices(env)
	if err != nil {
		return err
	}
	dockerNet, err := renderDockerComposeNetwork(env, services)
	if err != nil {
		return err
	}
	dnf := filepath.Join(i.serviceBinDir(), "compose.network")
	if err := os.WriteFile(dnf, []byte(dockerNet), 0644); err != nil {
		return fmt.Errorf("failed to write docker compose network: %v", err)
	}
	mak.Set(&i.artifacts, db.ArtifactDockerComposeNetwork, dnf)
	return nil
}

func (i *FileInstaller) composeDNSOverlayServices(env netns.Service) ([]composeDNSService, error) {
	composePath, ok := i.artifacts[db.ArtifactDockerComposeFile]
	if !ok || !env.ServiceIP.IsValid() {
		return nil, nil
	}
	raw, err := os.ReadFile(composePath)
	if err != nil {
		return nil, fmt.Errorf("read compose file for DNS overlay: %w", err)
	}
	services, err := composeDNSServices(raw)
	if err != nil {
		return nil, fmt.Errorf("compose DNS overlay: %w", err)
	}
	for _, service := range services {
		if service.CustomResolver {
			i.printf("warning: compose service %q defines dns or dns_search; leaving resolver configuration unchanged\n", service.Name)
		}
	}
	return services, nil
}

func (i *FileInstaller) setArtifacts(files map[db.ArtifactName]string) {
	for k, v := range files {
		mak.Set(&i.artifacts, k, v)
	}
}

// Close closes the temporary file and installs the service.
func (i *FileInstaller) Close() (err error) {
	done, err := i.closePreflight()
	if err != nil || done {
		return err
	}

	defer i.finishClose(&err)
	if err := i.closeAndInstall(); err != nil {
		return err
	}
	if i.cfg.ServiceName == CatchService && i.RollbackInstalledGenerationAvailable() {
		return i.captureInstalledGenerationOrRollback()
	}
	return nil
}

func (i *FileInstaller) RollbackInstalledGenerationAvailable() bool {
	return i != nil && i.existingService.Valid() && i.existingService.Generation() > 0
}

func (i *FileInstaller) RollbackInstalledGeneration() error {
	if !i.RollbackInstalledGenerationAvailable() {
		return fmt.Errorf("no previous Catch generation to restore")
	}
	if i.installedGeneration <= 0 {
		return fmt.Errorf("no installed Catch generation was recorded for rollback")
	}
	return i.restorePreviousGeneration(i.installedGeneration)
}

func (i *FileInstaller) captureInstalledGenerationOrRollback() error {
	installed, err := i.currentInstalledGeneration()
	if err == nil && installed <= 0 {
		err = fmt.Errorf("observed invalid Catch generation %d after install", installed)
	}
	if err == nil && i.installedGeneration > 0 && installed != i.installedGeneration {
		err = fmt.Errorf("observed Catch generation %d after committing generation %d", installed, i.installedGeneration)
	}
	if err != nil {
		captureErr := fmt.Errorf("record installed Catch generation: %w", err)
		if i.installedGeneration <= 0 {
			return captureErr
		}
		return errors.Join(captureErr, i.restorePreviousGeneration(i.installedGeneration))
	}
	if i.installedGeneration <= 0 {
		i.installedGeneration = installed
	}
	return nil
}

func (i *FileInstaller) currentInstalledGeneration() (int, error) {
	if i == nil {
		return 0, fmt.Errorf("catch installer is required")
	}
	if i.readInstalledGeneration != nil {
		return i.readInstalledGeneration()
	}
	if i.s == nil {
		return 0, fmt.Errorf("installer server is required")
	}
	sv, err := i.s.serviceView(i.cfg.ServiceName)
	if err != nil {
		return 0, err
	}
	return sv.Generation(), nil
}

func (i *FileInstaller) restorePreviousGeneration(expected int) error {
	if !i.RollbackInstalledGenerationAvailable() {
		return fmt.Errorf("no previous Catch generation to restore")
	}
	if i.s == nil {
		return fmt.Errorf("restore previous Catch generation: installer server is required")
	}
	installer, err := i.s.NewInstaller(i.cfg.InstallerCfg)
	if err != nil {
		return fmt.Errorf("restore previous Catch generation: %w", err)
	}
	installGeneration := i.installGenerationIfCurrent
	if installGeneration == nil {
		installGeneration = (*Installer).InstallGenIfCurrent
	}
	if err := installGeneration(installer, expected, i.existingService.Generation()); err != nil {
		return fmt.Errorf("restore previous Catch generation %d: %w", i.existingService.Generation(), err)
	}
	return nil
}

func (i *FileInstaller) closeAndInstall() error {
	if err := i.closeTempFile(); err != nil {
		return err
	}
	if i.failed {
		return i.installationFailedError()
	}
	if err := i.installOnClose(); err != nil {
		return i.closeInstallError(err)
	}
	return nil
}

func (i *FileInstaller) closePreflight() (bool, error) {
	if i.err != nil {
		return true, i.err
	}
	if i.closed {
		return true, nil
	}
	if i.File == nil {
		return true, fmt.Errorf("no temporary file")
	}
	return false, nil
}

func (i *FileInstaller) finishClose(err *error) {
	i.cleanupTemp()
	i.File = nil
	i.closed = true
	close(i.ch)
	i.err = *err
}

func (i *FileInstaller) closeTempFile() error {
	if err := i.File.Close(); err != nil {
		return fmt.Errorf("failed to close temporary file: %v", err)
	}
	return nil
}

func (i *FileInstaller) installationFailedError() error {
	log.Printf("Installation of %q failed\n", i.cfg.ServiceName)
	i.printf("Installation of %q failed\n", i.cfg.ServiceName)
	return fmt.Errorf("installation failed")
}

func (i *FileInstaller) closeInstallError(err error) error {
	log.Printf("Failed to install service: %v", err)
	i.printf("Failed to install service: %v", err)
	return fmt.Errorf("failed to install service: %w", err)
}

func rewriteSystemdUnit(p, exe string, args []string) (string, error) {
	raw, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("failed to read systemd unit: %w", err)
	}
	out := fileutil.UpdateVersion(p)
	content := rewriteSystemdUnitContent(string(raw), exe, args)
	if err := os.WriteFile(out, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("failed to write systemd unit: %w", err)
	}
	return out, nil
}

func rewriteSystemdUnitContent(unit, exe string, args []string) string {
	var b strings.Builder
	sc := bufio.NewScanner(strings.NewReader(unit))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(strings.TrimSpace(line), "ExecStart=") {
			b.WriteString("ExecStart=")
			b.WriteString(exe)
			b.WriteByte(' ')
			b.WriteString(strings.Join(args, " "))
			b.WriteByte('\n')
		} else {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func (i *FileInstaller) ensureSystemdUnit() error {
	exe := filepath.Join(i.serviceRunDir(), i.cfg.ServiceName)
	if reused, err := i.reuseExistingSystemdUnit(exe); err != nil || reused {
		return err
	}
	if i.skipSystemdUnitGeneration() {
		return nil
	}
	su, err := i.newSystemdUnit(exe)
	if err != nil {
		return err
	}
	return i.writeSystemdUnit(su)
}

func (i *FileInstaller) reuseExistingSystemdUnit(exe string) (bool, error) {
	if !i.canReuseExistingSystemdUnit() {
		return false, nil
	}
	if networkInterfacesEnabled(i.cfg.Network.Interfaces) {
		return false, nil
	}
	s := i.existingService.AsStruct()
	p, ok := s.Artifacts.Staged(db.ArtifactSystemdUnit)
	if !ok {
		return false, nil
	}
	if i.cfg.Args == nil {
		return true, nil
	}
	p, err := rewriteSystemdUnit(p, exe, i.cfg.Args)
	if err != nil {
		return false, fmt.Errorf("failed to rewrite systemd unit: %w", err)
	}
	mak.Set(&i.artifacts, db.ArtifactSystemdUnit, p)
	return true, nil
}

func (i *FileInstaller) canReuseExistingSystemdUnit() bool {
	return i.existingService.Valid() && i.cfg.ServiceName != CatchService
}

func (i *FileInstaller) skipSystemdUnitGeneration() bool {
	return i.cfg.StageOnly && i.cfg.Network.Interfaces == "" && i.cfg.Args == nil
}

// ISO intentionally rejects native root services. Host root can reconfigure or
// leave a network namespace, so non-root systemd sandboxing is a prerequisite
// for adding native ISO support without making a false security claim.
func (i *FileInstaller) newSystemdUnit(exe string) (*svc.SystemdUnit, error) {
	su := &svc.SystemdUnit{
		Name:             i.cfg.ServiceName,
		Executable:       exe,
		WorkingDirectory: i.serviceDataDir(),
		Arguments:        i.cfg.Args,
		EnvFile:          "-" + filepath.Join(i.serviceRunDir(), "env"),
		Timer:            i.cfg.Timer,
	}
	if i.cfg.ServiceName == CatchService {
		configureCatchSystemdUnit(su)
	}
	if err := i.applyNetworkToSystemdUnit(su); err != nil {
		return nil, err
	}
	return su, nil
}

func (i *FileInstaller) applyNetworkToSystemdUnit(su *svc.SystemdUnit) error {
	n, err := i.configureNetwork()
	if err != nil {
		return fmt.Errorf("failed to configure network: %v", err)
	}
	if n == nil {
		return nil
	}
	su.NetNS = n.NetNS
	su.Requires = strings.Join(n.Deps, " ")
	if n.ResolvConf != "" {
		su.ResolvConf = n.ResolvConf
	}
	return nil
}

func (i *FileInstaller) writeSystemdUnit(su *svc.SystemdUnit) error {
	log.Printf("NetNS: %v", su.NetNS)
	log.Printf("Requires: %v", su.Requires)
	units, err := su.WriteOutUnitFiles(i.serviceBinDir())
	if err != nil {
		return fmt.Errorf("failed to write unit files: %v", err)
	}
	i.setArtifacts(units)
	return nil
}

func configureCatchSystemdUnit(su *svc.SystemdUnit) {
	su.Wants = "containerd.service"
	su.After = "containerd.service"
	su.Before = dockerPrereqsTargetUnit + " " + dockerServiceUnit
	su.ExecStartPost = append(su.ExecStartPost, dockerPluginSocketWaitCommand())
}

type fileInstallPlan struct {
	dst                     string
	postRenameActions       []func() error
	detectedServiceType     db.ServiceType
	allowServiceTypeUpgrade bool
	publish                 []string
	publishSet              bool
}

func (i *FileInstaller) installOnClose() error {
	if i.File == nil {
		return fmt.Errorf("no temporary file")
	}
	plan, err := i.prepareAndInstallTempFile(i.tempFilePath())
	if err != nil {
		return err
	}
	if err := i.configureAndStageInstall(plan); err != nil {
		return err
	}
	return i.installIfRequested()
}

func (i *FileInstaller) prepareAndInstallTempFile(tmppath string) (fileInstallPlan, error) {
	plan, err := i.prepareInstallPlan(tmppath)
	if err != nil {
		return fileInstallPlan{}, err
	}
	if err := i.installPreparedFile(tmppath, plan); err != nil {
		return fileInstallPlan{}, err
	}
	return plan, nil
}

func (i *FileInstaller) configureAndStageInstall(plan fileInstallPlan) error {
	if networkRequestsISO(i.cfg.Network) {
		return i.configureAndStageISOInstall(plan)
	}
	return i.configureAndStageRegularInstall(plan)
}

func (i *FileInstaller) configureAndStageISOInstall(plan fileInstallPlan) error {
	if i.isoInstallServiceType(plan) != db.ServiceTypeDockerCompose {
		return fmt.Errorf("ISO installation requires a Docker Compose payload")
	}
	if _, err := i.prepareISOCompose(context.Background(), nil); err != nil {
		return err
	}
	if err := i.parseNetwork(); err != nil {
		return fmt.Errorf("failed to parse ISO network: %w", err)
	}
	if err := i.validateISOInstallTailscale(); err != nil {
		return err
	}
	return i.stageInstallPlan(plan)
}

func (i *FileInstaller) isoInstallServiceType(plan fileInstallPlan) db.ServiceType {
	if plan.detectedServiceType != "" {
		return plan.detectedServiceType
	}
	if i.existingService.Valid() {
		return i.existingService.ServiceType()
	}
	return ""
}

func (i *FileInstaller) validateISOInstallTailscale() error {
	if i.tsNet == nil {
		return nil
	}
	_, _, _, err := i.tailscaleNetNSMode(&netns.Service{ServiceName: i.cfg.ServiceName})
	if err == nil {
		return nil
	}
	return errors.Join(err, i.s.markISOState(i.cfg.ServiceName, string(iso.StateQuarantined), err))
}

func (i *FileInstaller) configureAndStageRegularInstall(plan fileInstallPlan) error {
	if _, err := i.configureNetwork(); err != nil {
		return fmt.Errorf("failed to configure network: %v", err)
	}
	explicitHost := strings.EqualFold(strings.TrimSpace(i.cfg.Network.Interfaces), "host")
	if i.existingService.Valid() && i.existingService.ISO().Valid() && (networkInterfacesEnabled(i.cfg.Network.Interfaces) || explicitHost) {
		return i.transitionAwayFromISO(context.Background(), plan)
	}
	return i.stageInstallPlan(plan)
}

func (i *FileInstaller) installIfRequested() error {
	if i.transitionHandled {
		return nil
	}
	if i.cfg.StageOnly {
		return nil
	}
	return i.installStagedService()
}

type fileInstallerISOTransition struct {
	installer *FileInstaller
	plan      fileInstallPlan
	prepared  isoReplacementNetwork
	compose   *svc.DockerComposeService
	spec      isoRuntimeNetworkSpec
}

func (i *FileInstaller) transitionAwayFromISO(ctx context.Context, plan fileInstallPlan) error {
	prepared := isoReplacementNetwork{
		Modes:      slices.Clone(i.cfg.Network.Modes),
		SvcNetwork: cloneISOReplacementSvcNetwork(i.svcNet),
		Macvlan:    cloneISOReplacementMacvlan(i.macvlan),
		Tailscale:  i.tsNet.Clone(),
		Artifacts:  stagedISONetworkArtifacts(i.artifacts),
	}
	view, err := i.s.serviceView(i.cfg.ServiceName)
	if err != nil {
		return err
	}
	compose, err := i.s.dockerComposeService(i.cfg.ServiceName)
	if err != nil {
		return fmt.Errorf("load ISO Compose service for transition: %w", err)
	}
	spec, err := i.s.loadISORuntimeSpec(i.cfg.ServiceName)
	if err != nil {
		return fmt.Errorf("load ISO network for transition: %w", err)
	}
	steps := &fileInstallerISOTransition{installer: i, plan: plan, prepared: prepared, compose: compose, spec: spec}
	transition := i.transitionFromISO
	if transition == nil {
		transition = i.s.transitionFromISO
	}
	if err := transition(ctx, view.Name(), slices.Clone(prepared.Modes), steps); err != nil {
		return err
	}
	i.transitionHandled = true
	return nil
}

func stagedISONetworkArtifacts(paths map[db.ArtifactName]string) db.ArtifactStore {
	artifacts := db.ArtifactStore{}
	for name, path := range paths {
		if !isoNetworkArtifactNames[name] {
			continue
		}
		artifacts[name] = &db.Artifact{Refs: map[db.ArtifactRef]string{"staged": path}}
	}
	return artifacts
}

func (t *fileInstallerISOTransition) PrepareReplacement(context.Context, string, []string) (isoReplacementNetwork, error) {
	return t.prepared, nil
}

func (t *fileInstallerISOTransition) StopISO(ctx context.Context, _ string) error {
	view, err := t.installer.s.serviceView(t.installer.cfg.ServiceName)
	if err != nil {
		return err
	}
	return errors.Join(t.compose.StopProjectContainers(ctx), stopAndVerifyISOAuxiliaryUnits(ctx, view.AsStruct()))
}

func (t *fileInstallerISOTransition) CleanISO(ctx context.Context, _ string) error {
	return removeISOTopologyForRuntime(ctx, t.spec.Topology)
}

func (t *fileInstallerISOTransition) VerifyISOAbsent(ctx context.Context, _ string) error {
	return errors.Join(
		netns.VerifyISOTopologyAbsent(ctx, t.spec.Topology),
		t.compose.VerifyProjectAbsent(ctx),
		t.compose.VerifyDefaultNetworkAbsent(ctx),
		verifyISOAllocationDNetAbsent(t.installer.s, t.spec.Topology.Allocation),
	)
}

func (t *fileInstallerISOTransition) StartReplacement(ctx context.Context, _ string, _ isoReplacementNetwork) error {
	rules, present, err := t.installer.s.currentGlobalISOPolicy()
	if err != nil {
		return err
	}
	if present {
		if err := ensureISOPolicyForRuntime(ctx, rules); err != nil {
			return err
		}
		if err := verifyISOPolicyForRuntime(ctx, rules); err != nil {
			return err
		}
	}
	if err := t.installer.stageInstallPlan(t.plan); err != nil {
		return err
	}
	if t.installer.cfg.StageOnly {
		return nil
	}
	return t.installer.installStagedService()
}

func (i *FileInstaller) prepareInstallPlan(tmppath string) (fileInstallPlan, error) {
	switch {
	case i.cfg.EnvFile:
		return i.prepareEnvFileInstall(), nil
	case i.cfg.NoBinary:
		return i.prepareNoBinaryInstall()
	default:
		return i.preparePayloadInstall(tmppath)
	}
}

func (i *FileInstaller) prepareEnvFileInstall() fileInstallPlan {
	dst := filepath.Join(i.serviceEnvDir(), "env-"+i.version())
	mak.Set(&i.artifacts, db.ArtifactEnvFile, dst)
	return fileInstallPlan{dst: dst}
}

func (i *FileInstaller) prepareNoBinaryInstall() (fileInstallPlan, error) {
	var plan fileInstallPlan
	if !i.existingService.Valid() {
		return plan, nil
	}
	plan.detectedServiceType = i.existingService.ServiceType()
	service := i.existingService.AsStruct()
	requestedPublished := len(normalizePublish(i.cfg.Publish)) != 0 || i.cfg.PublishReset
	published := len(service.Publish) != 0 || requestedPublished
	if !networkInterfacesEnabled(i.cfg.Network.Interfaces) && service.ISO != nil {
		if err := validateInstallNetworkRequestWithPublished(service, requestedPublished); err != nil {
			return plan, err
		}
	}
	if err := i.normalizeNetworkForServiceType(plan.detectedServiceType, published); err != nil {
		return plan, err
	}
	if plan.detectedServiceType != db.ServiceTypeSystemd {
		return plan, nil
	}
	if err := i.ensureSystemdUnit(); err != nil {
		return plan, fmt.Errorf("failed to ensure systemd unit: %w", err)
	}
	return plan, nil
}

func (i *FileInstaller) preparePayloadInstall(bin string) (fileInstallPlan, error) {
	binFT, err := detectInstallPayloadType(bin)
	if err != nil {
		return fileInstallPlan{}, err
	}
	if err := validatePullPayloadType(i.cfg.Pull, binFT); err != nil {
		return fileInstallPlan{}, err
	}
	serviceType, ok := payloadServiceType(binFT)
	if ok {
		composePublished := false
		if networkRequestsISO(i.cfg.Network) {
			composePublished, err = i.composePayloadPublishesPorts(bin, binFT)
			if err != nil {
				return fileInstallPlan{}, err
			}
		}
		published := i.cfg.PublishReset || len(normalizePublish(i.cfg.Publish)) != 0 || composePublished
		if err := i.normalizeNetworkForServiceType(serviceType, published); err != nil {
			return fileInstallPlan{}, err
		}
	}
	return i.preparePayloadByType(bin, binFT)
}

func networkRequestsISO(network NetworkOpts) bool {
	if network.ISO {
		return true
	}
	for _, mode := range strings.Split(network.Interfaces, ",") {
		if strings.EqualFold(strings.TrimSpace(mode), "iso") {
			return true
		}
	}
	return false
}

func payloadServiceType(binFT ftdetect.FileType) (db.ServiceType, bool) {
	switch {
	case systemdPayloadType(binFT):
		return db.ServiceTypeSystemd, true
	case binFT == ftdetect.DockerCompose:
		return db.ServiceTypeDockerCompose, true
	default:
		_, ok := generatedPayloadTypes[binFT]
		return db.ServiceTypeDockerCompose, ok
	}
}

func (i *FileInstaller) normalizeNetworkForServiceType(serviceType db.ServiceType, published bool) error {
	if !networkInterfacesEnabled(i.cfg.Network.Interfaces) {
		return nil
	}
	network, err := parseNetworkForPayload(i.cfg.Network, networkPayloadKind(serviceType), published)
	if err != nil {
		return err
	}
	i.cfg.Network = network
	return nil
}

func (i *FileInstaller) composePayloadPublishesPorts(bin string, binFT ftdetect.FileType) (bool, error) {
	if binFT != ftdetect.DockerCompose {
		return false, nil
	}
	publish, err := readComposePorts(bin, i.cfg.ServiceName)
	if err != nil {
		return false, fmt.Errorf("inspect compose published ports: %w", err)
	}
	return len(publish) != 0, nil
}

func (i *FileInstaller) preparePayloadByType(bin string, binFT ftdetect.FileType) (fileInstallPlan, error) {
	if systemdPayloadType(binFT) {
		return i.prepareSystemdPayload(binFT)
	}
	if binFT == ftdetect.DockerCompose {
		return i.prepareDockerComposePayload(bin)
	}
	if cfg, ok := generatedPayloadTypes[binFT]; ok {
		return i.prepareGeneratedComposePayload(cfg.message, cfg.payloadName, cfg.artifactName, cfg.kind, cfg.render)
	}
	return fileInstallPlan{}, fmt.Errorf("unknown file type")
}

func systemdPayloadType(binFT ftdetect.FileType) bool {
	return binFT == ftdetect.Binary || binFT == ftdetect.Script
}

type generatedPayloadType struct {
	message      string
	payloadName  string
	artifactName db.ArtifactName
	kind         string
	render       composePayloadRenderer
}

var generatedPayloadTypes = map[ftdetect.FileType]generatedPayloadType{
	ftdetect.TypeScript: {
		message:      "Detected TypeScript file\n",
		payloadName:  "main.%s.ts",
		artifactName: db.ArtifactTypeScriptFile,
		kind:         "typescript",
		render:       typescriptComposeFile,
	},
	ftdetect.Python: {
		message:      "Detected Python file\n",
		payloadName:  "main.%s.py",
		artifactName: db.ArtifactPythonFile,
		kind:         "python",
		render:       pythonComposeFile,
	},
}

func detectInstallPayloadType(bin string) (ftdetect.FileType, error) {
	binFT, err := detectPayloadFileType(bin)
	if err != nil {
		return ftdetect.Unknown, err
	}
	if binFT != ftdetect.Zstd {
		return binFT, nil
	}
	return decompressAndDetectPayload(bin)
}

func detectPayloadFileType(bin string) (ftdetect.FileType, error) {
	binFT, err := ftdetect.DetectFile(bin, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return ftdetect.Unknown, fmt.Errorf("failed to detect file type: %w", err)
	}
	return binFT, nil
}

func decompressAndDetectPayload(bin string) (ftdetect.FileType, error) {
	unpackPath := bin + ".unpack"
	defer removeFileIfExists(unpackPath)
	if err := codecutil.ZstdDecompress(bin, unpackPath); err != nil {
		return ftdetect.Unknown, fmt.Errorf("failed to decompress file: %w", err)
	}
	if err := os.Rename(unpackPath, bin); err != nil {
		return ftdetect.Unknown, fmt.Errorf("failed to rename file: %w", err)
	}
	return detectPayloadFileType(bin)
}

func validatePullPayloadType(pull bool, binFT ftdetect.FileType) error {
	if !pull || pullSupportedPayloadType(binFT) {
		return nil
	}
	return fmt.Errorf("--pull is only valid for docker compose, python, or typescript payloads")
}

func pullSupportedPayloadType(binFT ftdetect.FileType) bool {
	return binFT == ftdetect.DockerCompose || binFT == ftdetect.Python || binFT == ftdetect.TypeScript
}

func (i *FileInstaller) prepareSystemdPayload(binFT ftdetect.FileType) (fileInstallPlan, error) {
	i.printDetectedSystemdPayload(binFT)
	dst := filepath.Join(i.serviceBinDir(), fmt.Sprintf("%s-%s", i.cfg.ServiceName, i.version()))
	plan := fileInstallPlan{
		dst:                 dst,
		postRenameActions:   []func() error{chmodExecutableAction(dst)},
		detectedServiceType: db.ServiceTypeSystemd,
	}
	mak.Set(&i.artifacts, db.ArtifactBinary, dst)
	if err := i.ensureSystemdUnit(); err != nil {
		return plan, fmt.Errorf("failed to ensure systemd unit: %w", err)
	}
	return plan, nil
}

func (i *FileInstaller) printDetectedSystemdPayload(binFT ftdetect.FileType) {
	if binFT == ftdetect.Script {
		i.printf("Detected script file\n")
		return
	}
	i.printf("Detected binary file\n")
}

func chmodExecutableAction(path string) func() error {
	return func() error {
		if err := os.Chmod(path, 0755); err != nil {
			return fmt.Errorf("failed to make binary executable: %w", err)
		}
		return nil
	}
}

func (i *FileInstaller) prepareDockerComposePayload(bin string) (fileInstallPlan, error) {
	i.printf("Detected Docker Compose file\n")
	publishChanged := i.cfg.PublishReset || len(i.cfg.Publish) > 0
	if publishChanged {
		if err := updateComposePorts(bin, i.cfg.ServiceName, i.cfg.Publish); err != nil {
			return fileInstallPlan{}, fmt.Errorf("failed to apply publish ports: %w", err)
		}
	}
	publish, err := readComposePorts(bin, i.cfg.ServiceName)
	if err != nil {
		if publishChanged {
			return fileInstallPlan{}, fmt.Errorf("failed to read publish ports: %w", err)
		}
		publish = nil
	}
	publishSet := err == nil || publishChanged
	dst := filepath.Join(i.serviceBinDir(), fmt.Sprintf("docker-compose.%s.yml", i.version()))
	mak.Set(&i.artifacts, db.ArtifactDockerComposeFile, dst)
	return fileInstallPlan{
		dst:                 dst,
		detectedServiceType: db.ServiceTypeDockerCompose,
		publish:             publish,
		publishSet:          publishSet,
	}, nil
}

type composePayloadRenderer func(serviceName, runDir, dataDir string, args []string, publish []string) (string, error)

func (i *FileInstaller) prepareGeneratedComposePayload(message, payloadName string, artifactName db.ArtifactName, kind string, render composePayloadRenderer) (fileInstallPlan, error) {
	i.printf(message)
	binDir := i.serviceBinDir()
	dst := filepath.Join(binDir, fmt.Sprintf(payloadName, i.version()))

	composePath, err := i.writeGeneratedComposeFile(binDir, kind, render)
	if err != nil {
		return fileInstallPlan{}, err
	}
	mak.Set(&i.artifacts, db.ArtifactDockerComposeFile, composePath)
	mak.Set(&i.artifacts, artifactName, dst)
	return fileInstallPlan{
		dst:                     dst,
		detectedServiceType:     db.ServiceTypeDockerCompose,
		allowServiceTypeUpgrade: true,
		publish:                 normalizePublish(i.cfg.Publish),
		publishSet:              true,
	}, nil
}

func (i *FileInstaller) writeGeneratedComposeFile(binDir, kind string, render composePayloadRenderer) (string, error) {
	composePath := filepath.Join(binDir, fmt.Sprintf("docker-compose.%s.yml", i.version()))
	composeContent, err := render(
		i.cfg.ServiceName,
		i.serviceRunDir(),
		i.serviceDataDir(),
		i.cfg.Args,
		i.cfg.Publish,
	)
	if err != nil {
		return "", fmt.Errorf("failed to render %s compose file: %w", kind, err)
	}
	if err := os.WriteFile(composePath, []byte(composeContent), 0644); err != nil {
		return "", fmt.Errorf("failed to write %s compose file: %w", kind, err)
	}
	return composePath, nil
}

func (i *FileInstaller) installPreparedFile(tmppath string, plan fileInstallPlan) error {
	if plan.dst == "" {
		removeFileIfExists(tmppath)
		return nil
	}
	if err := os.Rename(tmppath, plan.dst); err != nil {
		return fmt.Errorf("failed to move file in place: %w", err)
	}
	log.Printf("File moved to %q", plan.dst)
	for _, action := range plan.postRenameActions {
		if err := action(); err != nil {
			return fmt.Errorf("failed to run post-action: %w", err)
		}
	}
	return nil
}

func (i *FileInstaller) stageInstallPlan(plan fileInstallPlan) error {
	_, _, err := i.s.cfg.DB.MutateService(i.cfg.ServiceName, func(_ *db.Data, s *db.Service) error {
		return i.applyInstallPlanToService(s, plan)
	})
	if err != nil {
		return fmt.Errorf("failed to update service: %w", err)
	}
	return nil
}

func (i *FileInstaller) applyInstallPlanToService(s *db.Service, plan fileInstallPlan) error {
	if err := applyInstallServiceType(s, plan); err != nil {
		return err
	}
	i.applyInstallServiceRoot(s)
	if err := i.applyInstallSnapshotPolicy(s); err != nil {
		return err
	}
	applyInstallNetworks(s, i.macvlan, i.svcNet, i.tsNet)
	applyInstallPublish(s, plan)
	stageArtifacts(s, i.artifacts)
	return nil
}

func applyInstallPublish(s *db.Service, plan fileInstallPlan) {
	if plan.detectedServiceType == db.ServiceTypeDockerCompose && plan.publishSet {
		s.Publish = normalizePublish(plan.publish)
	}
}

func (i *FileInstaller) applyInstallSnapshotPolicy(s *db.Service) error {
	if i.cfg.snapshotPolicyFlags != nil {
		return applySnapshotFlagsToService(s, *i.cfg.snapshotPolicyFlags)
	}
	if !i.cfg.SnapshotPolicyChange && i.cfg.SnapshotPolicy == nil {
		return nil
	}
	if i.cfg.SnapshotPolicy == nil {
		s.SnapshotPolicy = nil
		return nil
	}
	s.SnapshotPolicy = i.cfg.SnapshotPolicy.Clone()
	return nil
}

func (i *FileInstaller) applyInstallServiceRoot(s *db.Service) {
	s.ServiceRootZFS = i.serviceRootZFS
	if filepath.Clean(i.serviceRoot) == filepath.Clean(i.s.defaultServiceRootDir(i.cfg.ServiceName)) && i.serviceRootZFS == "" {
		s.ServiceRoot = ""
		return
	}
	s.ServiceRoot = i.serviceRoot
}

func applyInstallServiceType(s *db.Service, plan fileInstallPlan) error {
	if s.ServiceType == "" {
		s.ServiceType = plan.detectedServiceType
		return nil
	}
	if plan.detectedServiceType == "" || s.ServiceType == plan.detectedServiceType {
		return nil
	}
	if plan.allowServiceTypeUpgrade && s.ServiceType == db.ServiceTypeSystemd && plan.detectedServiceType == db.ServiceTypeDockerCompose {
		s.ServiceType = plan.detectedServiceType
		return nil
	}
	return fmt.Errorf("service type mismatch: %v != %v", s.ServiceType, plan.detectedServiceType)
}

func applyInstallNetworks(s *db.Service, macvlan *db.MacvlanNetwork, svcNet *db.SvcNetwork, tsNet *db.TailscaleNetwork) {
	if macvlan != nil {
		s.Macvlan = macvlan
	}
	if svcNet != nil {
		s.SvcNetwork = svcNet
	}
	if tsNet != nil {
		s.TSNet = tsNet
	}
}

func stageArtifacts(s *db.Service, artifacts map[db.ArtifactName]string) {
	for a, p := range artifacts {
		af, ok := s.Artifacts[a]
		if !ok {
			af = &db.Artifact{
				Refs: map[db.ArtifactRef]string{},
			}
			mak.Set(&s.Artifacts, a, af)
		}
		af.Refs[db.ArtifactRef("staged")] = p
	}
}

func (i *FileInstaller) installStagedService() error {
	i.printf("File received\n")
	i.printf("Installing service\n")
	si, err := i.s.NewInstaller(i.cfg.InstallerCfg)
	if err != nil {
		return fmt.Errorf("failed to create installer: %w", err)
	}
	si.NewCmd = i.cfg.NewCmd
	si.isoTailscaleAuthKey = i.tsAuthKey
	if err := si.Install(); err != nil {
		return fmt.Errorf("failed to install service: %w", err)
	}
	i.installedGeneration = si.committedGeneration
	i.printf("Service %q installed\n", i.cfg.ServiceName)
	return nil
}

func (i *FileInstaller) Fail() {
	i.failed = true
}

func (i *FileInstaller) tempFilePath() string {
	if i.tmpPath != "" {
		return i.tmpPath
	}
	return filepath.Join(i.serviceBinDir(),
		fmt.Sprintf("%s-%s.tmp", i.cfg.ServiceName, i.version()))
}

func (i *FileInstaller) version() string {
	if i.ver == "" {
		i.ver = fileutil.Version()
	}
	return i.ver
}

type composeFile struct {
	Services map[string]composeService `yaml:"services"`
}

type composeService struct {
	Image      string   `yaml:"image,omitempty"`
	Restart    string   `yaml:"restart,omitempty"`
	Volumes    []string `yaml:"volumes,omitempty"`
	Command    []string `yaml:"command,omitempty"`
	WorkingDir string   `yaml:"working_dir,omitempty"`
	Ports      []string `yaml:"ports,omitempty"`
}

func pythonComposeFile(serviceName, runDir, dataDir string, args []string, publish []string) (string, error) {
	command := append([]string{"uv", "run", "/main.py"}, args...)
	ports := normalizePublish(publish)
	compose := composeFile{
		Services: map[string]composeService{
			serviceName: {
				Image:   "ghcr.io/astral-sh/uv:python3.13-bookworm-slim",
				Restart: "unless-stopped",
				Volumes: []string{
					fmt.Sprintf("%s:/data", dataDir),
					fmt.Sprintf("%s:/main.py:ro", filepath.Join(runDir, "main.py")),
				},
				Command: command,
				Ports:   ports,
			},
		},
	}
	content, err := yaml.Marshal(compose)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func typescriptComposeFile(serviceName, runDir, dataDir string, args []string, publish []string) (string, error) {
	command := append([]string{"deno", "run", "--allow-net", "/main.ts"}, args...)
	ports := normalizePublish(publish)
	compose := composeFile{
		Services: map[string]composeService{
			serviceName: {
				Image:   "denoland/deno:2.0.0-rc.2",
				Restart: "unless-stopped",
				Volumes: []string{
					fmt.Sprintf("%s:/data", dataDir),
					fmt.Sprintf("%s:/main.ts:ro", filepath.Join(runDir, "main.ts")),
				},
				Command: command,
				Ports:   ports,
			},
		},
	}
	content, err := yaml.Marshal(compose)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func (i *FileInstaller) tempPayloadName() string {
	name := strings.TrimSpace(i.cfg.PayloadName)
	if name != "" {
		base := filepath.Base(name)
		if base != "." && base != string(filepath.Separator) && base != ".." {
			return base
		}
	}
	return fmt.Sprintf("%s-%s.tmp", i.cfg.ServiceName, i.version())
}

func (i *FileInstaller) initTempFile() error {
	if i.tmpPath != "" {
		return nil
	}
	tmpDir, err := os.MkdirTemp(i.serviceBinDir(),
		fmt.Sprintf("%s-%s-", i.cfg.ServiceName, i.version()))
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	i.tmpDir = tmpDir
	i.tmpPath = filepath.Join(tmpDir, i.tempPayloadName())
	return nil
}

func (i *FileInstaller) cleanupTemp() {
	if i.tmpDir == "" {
		return
	}
	if err := os.RemoveAll(i.tmpDir); err != nil {
		log.Printf("failed to remove temp dir: %v", err)
	}
	i.tmpDir = ""
	i.tmpPath = ""
}
