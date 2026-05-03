// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/yeetrun/yeet/pkg/cmdutil"
	"github.com/yeetrun/yeet/pkg/codecutil"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/fileutil"
	"github.com/yeetrun/yeet/pkg/ftdetect"
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

	Args      []string
	Network   NetworkOpts
	StageOnly bool
	NoBinary  bool
	Publish   []string
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
}

type FileInstaller struct {
	s   *Server
	cfg FileInstallerCfg
	ch  chan struct{}

	existingService db.ServiceView
	svcNet          *db.SvcNetwork
	macvlan         *db.MacvlanNetwork
	tsNet           *db.TailscaleNetwork
	tsAuthKey       string
	artifacts       map[db.ArtifactName]string
	lazyNetwork     lazy.GValue[*networkConfig]

	File     *os.File
	received atomic.Int64
	rateVal  rate.Value

	err    error
	closed bool

	ver string // memoized version number

	failed bool

	tmpDir  string
	tmpPath string
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
	i := &FileInstaller{
		s:   s,
		cfg: cfg,
		ch:  make(chan struct{}),
		rateVal: rate.Value{
			HalfLife: 250 * time.Millisecond,
		},
		existingService: First(s.serviceView(cfg.ServiceName)),
	}
	if i.cfg.NewCmd == nil {
		i.cfg.NewCmd = cmdutil.NewStdCmd
	}
	if err := s.ensureDirs(cfg.ServiceName, cfg.User); err != nil {
		return nil, fmt.Errorf("failed to ensure directories: %w", err)
	}
	if err := i.initTempFile(); err != nil {
		return nil, err
	}
	// Create temporary file.
	var err error
	i.File, err = os.OpenFile(i.tempFilePath(), os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		i.cleanupTemp()
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	return i, nil
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
	NetNS string
	Deps  []string
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
		i.macvlan = macvlan
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
	deps, err := i.installNetworkConfig(env, runTSInNetNS, tsTapMode)
	if err != nil {
		return nil, err
	}
	log.Printf("artifacts: %v", i.artifacts)
	return &networkConfig{
		NetNS: env.NetNS(),
		Deps:  deps,
	}, nil
}

func (i *FileInstaller) prepareNetworkConfig() (netns.Service, string, bool, error) {
	if err := i.parseNetwork(); err != nil {
		return netns.Service{}, "", false, fmt.Errorf("failed to parse network: %v", err)
	}
	env := i.netNSServiceEnv()
	runTSInNetNS, _, tsTapMode := i.tailscaleNetNSMode(&env)
	return env, runTSInNetNS, tsTapMode, nil
}

func (i *FileInstaller) installNetworkConfig(env netns.Service, runTSInNetNS string, tsTapMode bool) ([]string, error) {
	if err := i.writeBaseNetworkConfig(&env); err != nil {
		return nil, err
	}
	deps, err := i.installTailscaleDependency(env, runTSInNetNS, tsTapMode)
	if err != nil {
		return nil, err
	}
	if err := i.writeDockerComposeNetwork(env); err != nil {
		return nil, err
	}
	return deps, nil
}

func (i *FileInstaller) writeBaseNetworkConfig(env *netns.Service) error {
	_, netnsResolvConf, _ := i.tailscaleNetNSMode(env)
	if err := i.writeNetNSResolvConf(env, netnsResolvConf); err != nil {
		return err
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
	env.Range = netip.MustParsePrefix("192.168.100.0/24")
	env.HostIP = netip.MustParseAddr("192.168.100.1")
	env.YeetIP = netip.MustParseAddr("192.168.100.254")
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

func (i *FileInstaller) tailscaleNetNSMode(env *netns.Service) (runTSInNetNS string, netnsResolvConf string, tapMode bool) {
	if i.tsNet == nil {
		return "", "", false
	}
	tapMode = i.svcNet == nil && i.macvlan == nil
	if tapMode {
		env.TailscaleTAPInterface = i.tsNet.Interface
		return "", tailscaledResolvConf, true
	}
	return env.NetNS(), "", false
}

func (i *FileInstaller) writeNetNSResolvConf(env *netns.Service, resolvConf string) error {
	if resolvConf == "" {
		resolvConf = defaultNetNSResolvConf()
	}
	fp := filepath.Join(i.s.serviceBinDir(i.cfg.ServiceName), fileutil.ApplyVersion("resolv.conf"))
	if err := os.WriteFile(fp, []byte(resolvConf), 0644); err != nil {
		return fmt.Errorf("failed to write resolv.conf: %v", err)
	}
	mak.Set(&i.artifacts, db.ArtifactNetNSResolv, fp)
	env.ResolvConf = fp
	return nil
}

func defaultNetNSResolvConf() string {
	const defaultNameserver = "8.8.8.8"
	dns := defaultNameserver
	if v := os.Getenv("DEFAULT_NS"); v != "" {
		dns = v
	}
	return buildNetNSResolvConf(dns, os.Getenv("DEFAULT_SEARCH_DOMAINS"))
}

func buildNetNSResolvConf(dns, searchDomains string) string {
	resolvConf := fmt.Sprintf("nameserver %s\n", dns)
	if searchDomains != "" {
		resolvConf += fmt.Sprintf("search %s\n", searchDomains)
	}
	return resolvConf
}

func (i *FileInstaller) writeServiceNetNSFiles(env netns.Service) error {
	files, err := netns.WriteServiceNetNS(
		i.s.serviceBinDir(i.cfg.ServiceName),
		i.s.serviceRunDir(i.cfg.ServiceName),
		env,
	)
	if err != nil {
		return fmt.Errorf("failed to write netns: %v", err)
	}
	i.setArtifacts(files)
	return nil
}

func (i *FileInstaller) installTailscaleForNetNS(env netns.Service, runTSInNetNS string, tsTapMode bool) error {
	rc := "/etc/netns/" + env.NetNS() + "/resolv.conf"
	if tsTapMode {
		// Tailscale in TAP mode runs in the host namespace, so no netns
		// resolv.conf path should be passed to tailscaled.
		rc = ""
	}
	files, err := i.s.installTS(i.cfg.ServiceName, runTSInNetNS, i.tsNet, i.tsAuthKey, rc)
	if err != nil {
		return fmt.Errorf("failed to install tailscale: %v", err)
	}
	i.setArtifacts(files)
	return nil
}

func (i *FileInstaller) writeDockerComposeNetwork(env netns.Service) error {
	dockerNet := fmt.Sprintf(`networks:
  default:
    driver: yeet
    driver_opts:
      dev.catchit.netns: %q
`, filepath.Join("/var/run/netns", env.NetNS()))
	dnf := filepath.Join(i.s.serviceBinDir(i.cfg.ServiceName), "compose.network")
	if err := os.WriteFile(dnf, []byte(dockerNet), 0644); err != nil {
		return fmt.Errorf("failed to write docker compose network: %v", err)
	}
	mak.Set(&i.artifacts, db.ArtifactDockerComposeNetwork, dnf)
	return nil
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
	return i.closeAndInstall()
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
	exe := filepath.Join(i.s.serviceRunDir(i.cfg.ServiceName), i.cfg.ServiceName)
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

func (i *FileInstaller) newSystemdUnit(exe string) (*svc.SystemdUnit, error) {
	su := &svc.SystemdUnit{
		Name:             i.cfg.ServiceName,
		Executable:       exe,
		WorkingDirectory: i.s.serviceDataDir(i.cfg.ServiceName),
		Arguments:        i.cfg.Args,
		EnvFile:          "-" + filepath.Join(i.s.serviceRunDir(i.cfg.ServiceName), "env"),
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
	su.ResolvConf = fmt.Sprintf("/etc/netns/%s/resolv.conf", su.NetNS)
	return nil
}

func (i *FileInstaller) writeSystemdUnit(su *svc.SystemdUnit) error {
	log.Printf("NetNS: %v", su.NetNS)
	log.Printf("Requires: %v", su.Requires)
	units, err := su.WriteOutUnitFiles(i.s.serviceBinDir(i.cfg.ServiceName))
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
	if _, err := i.configureNetwork(); err != nil {
		return fmt.Errorf("failed to configure network: %v", err)
	}
	return i.stageInstallPlan(plan)
}

func (i *FileInstaller) installIfRequested() error {
	if i.cfg.StageOnly {
		return nil
	}
	return i.installStagedService()
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
	dst := filepath.Join(i.s.serviceEnvDir(i.cfg.ServiceName), "env-"+i.version())
	mak.Set(&i.artifacts, db.ArtifactEnvFile, dst)
	return fileInstallPlan{dst: dst}
}

func (i *FileInstaller) prepareNoBinaryInstall() (fileInstallPlan, error) {
	var plan fileInstallPlan
	if !i.existingService.Valid() {
		return plan, nil
	}
	plan.detectedServiceType = i.existingService.ServiceType()
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
	return i.preparePayloadByType(bin, binFT)
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
	dst := filepath.Join(i.s.serviceBinDir(i.cfg.ServiceName), fmt.Sprintf("%s-%s", i.cfg.ServiceName, i.version()))
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
	if len(i.cfg.Publish) > 0 {
		if err := updateComposePorts(bin, i.cfg.ServiceName, i.cfg.Publish); err != nil {
			return fileInstallPlan{}, fmt.Errorf("failed to apply publish ports: %w", err)
		}
	}
	dst := filepath.Join(i.s.serviceBinDir(i.cfg.ServiceName), fmt.Sprintf("docker-compose.%s.yml", i.version()))
	mak.Set(&i.artifacts, db.ArtifactDockerComposeFile, dst)
	return fileInstallPlan{
		dst:                 dst,
		detectedServiceType: db.ServiceTypeDockerCompose,
	}, nil
}

type composePayloadRenderer func(serviceName, runDir, dataDir string, args []string, publish []string) (string, error)

func (i *FileInstaller) prepareGeneratedComposePayload(message, payloadName string, artifactName db.ArtifactName, kind string, render composePayloadRenderer) (fileInstallPlan, error) {
	i.printf(message)
	binDir := i.s.serviceBinDir(i.cfg.ServiceName)
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
	}, nil
}

func (i *FileInstaller) writeGeneratedComposeFile(binDir, kind string, render composePayloadRenderer) (string, error) {
	composePath := filepath.Join(binDir, fmt.Sprintf("docker-compose.%s.yml", i.version()))
	composeContent, err := render(
		i.cfg.ServiceName,
		i.s.serviceRunDir(i.cfg.ServiceName),
		i.s.serviceDataDir(i.cfg.ServiceName),
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
	applyInstallNetworks(s, i.macvlan, i.svcNet, i.tsNet)
	stageArtifacts(s, i.artifacts)
	return nil
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
	if err := si.Install(); err != nil {
		return fmt.Errorf("failed to install service: %w", err)
	}
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
	return filepath.Join(i.s.serviceBinDir(i.cfg.ServiceName),
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
	tmpDir, err := os.MkdirTemp(i.s.serviceBinDir(i.cfg.ServiceName),
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
